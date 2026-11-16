// Package main is the Yansu Quick Chat Spotlight helper.
//
// This binary is spawned by the Yansu desktop app (wails-gui) at
// startup. It owns one frameless always-on-top floating panel that
// hosts the pill + conversation React UI, and exposes a tiny
// localhost-only HTTP server that Yansu posts to (`/show`, `/hide`,
// `/quit`, `/health`) to drive the panel.
//
// Platform shells diverge (see run_darwin.go / run_other.go):
//   - macOS: a native AppKit NSPanel with the non-activating style mask,
//     so summoning the panel does NOT steal focus from the user's
//     frontmost app. Assets are served from an in-process HTTP server
//     and loaded into a WKWebView.
//   - Windows/Linux: Wails (a regular activating window). A non-activating
//     panel there needs WS_EX_NOACTIVATE and is handled separately.
//
// This file holds the platform-agnostic surface: launch flags, the IPC
// server, and the request handlers, which delegate window operations to
// per-platform presentPanel / dismissPanel / quitApp.
package main

import (
	"context"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

//go:embed all:frontend/dist
var assets embed.FS

const tokenHeader = "X-Yansu-Spotlight-Token"

const (
	hostHealthCheckInterval = 5 * time.Second
	hostHealthCheckTimeout  = 2 * time.Second
	hostHealthFailureLimit  = 3
)

// spotlightWindowTitle is set on the window AND used by the Win32
// foreground/positioning code (winfocus_windows.go) to find the HWND.
// Both sides must reference the same constant — keep them in sync.
const spotlightWindowTitle = "Isoform Spotlight"

// pillSize / conversationSize are the two window dimensions the panel
// switches between. Conversation gets resized on first prompt submit
// via the `resize` bridge call from the frontend.
const (
	pillWidth  = 768
	pillHeight = 88
	// conversationWidth must match pillWidth.
	conversationWidth  = 768
	conversationHeight = 650
)

// spotlightBottomFraction is the screen-height fraction where the
// panel's bottom edge sits (chat-input dock pattern). 0.88 leaves ~12%
// margin below for the system taskbar / Dock.
const spotlightBottomFraction = 0.88

// launchArgs is the parsed CLI flags Yansu's spotlightSupervisor passes
// when spawning us. Standalone dev runs get zeros and the frontend
// falls back to VITE_YANSU_PORT.
type launchArgs struct {
	listenPort    int
	ipcToken      string
	yansuHostPort int
}

func parseLaunchArgs() launchArgs {
	var a launchArgs
	flag.IntVar(&a.listenPort, "listen-port", 0, "TCP port for the Isoform→spotlight IPC server")
	flag.StringVar(&a.ipcToken, "ipc-token", "", "Per-session token Isoform generated for us")
	flag.IntVar(&a.yansuHostPort, "yansu-port", 0, "Isoform host-bridge TCP port for /api/spotlight/*")
	flag.Parse()
	return a
}

// app holds shared runtime state. On Windows/Linux its methods are also
// bound into Wails as JS-callable bindings; on macOS the frontend talks
// to the native bridge instead (see wails-app-stub.ts).
type app struct {
	ctx context.Context
	cfg launchArgs
	mu  sync.Mutex
	// lastSize tracks the last-applied window dimensions (Wails path).
	lastSizeW, lastSizeH int
	// autoHideOnBlur: pill auto-dismisses on focus loss, conversation
	// stays open. Toggled by SetAutoHideOnBlur on view changes.
	autoHideOnBlur bool
	// darkMode mirrors the React theme so the Wails path can re-apply
	// the DWM dark-mode hint on every surface (Windows only).
	darkMode bool
}

// SpotlightConfig is the public-facing shape of GetConfig's return — the
// host-bridge port + IPC token the frontend forwards. On macOS the same
// values are injected as window.__spotlightConfig instead.
type SpotlightConfig struct {
	YansuPort int    `json:"yansuPort"`
	Token     string `json:"token"`
}

// GetConfig is the Wails binding the frontend awaits at startup
// (Windows/Linux). Unused on macOS, where window.__spotlightConfig is
// injected at document start.
func (a *app) GetConfig() SpotlightConfig {
	a.mu.Lock()
	defer a.mu.Unlock()
	return SpotlightConfig{
		YansuPort: a.cfg.yansuHostPort,
		Token:     a.cfg.ipcToken,
	}
}

// serveIPC binds the localhost-only HTTP server Yansu drives us through.
// Routes and auth header match what spotlight_supervisor.go expects.
func (a *app) serveIPC(ctx context.Context) {
	addr := fmt.Sprintf("127.0.0.1:%d", a.cfg.listenPort)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		log.Printf("[spotlight] ipc listen %s failed: %v", addr, err)
		return
	}
	bound := listener.Addr().(*net.TCPAddr).Port
	// Isoform's supervisor learns the port from this exact line (it passes
	// --listen-port=0 so no other process can take the port first).
	fmt.Printf("SPOTLIGHT_IPC_PORT=%d\n", bound)
	log.Printf("[spotlight] ipc server listening on 127.0.0.1:%d (token_set=%v yansu_port=%d)",
		bound, a.cfg.ipcToken != "", a.cfg.yansuHostPort)

	mux := http.NewServeMux()
	mux.HandleFunc("/health", a.handleHealth)
	mux.HandleFunc("/show", a.requireAuth(a.handleShow))
	mux.HandleFunc("/hide", a.requireAuth(a.handleHide))
	mux.HandleFunc("/quit", a.requireAuth(a.handleQuit))

	srv := &http.Server{Handler: mux, ReadTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	if err := srv.Serve(listener); err != nil && err != http.ErrServerClosed {
		log.Printf("[spotlight] ipc serve error: %v", err)
	}
}

// watchYansuHost prevents orphaned Spotlight helpers from surviving a
// crashed or force-stopped Yansu process. The supervisor normally sends
// /quit, but that signal cannot run after an abrupt parent exit on macOS.
func (a *app) watchYansuHost(ctx context.Context) {
	if a.cfg.yansuHostPort == 0 {
		return
	}

	client := &http.Client{Timeout: hostHealthCheckTimeout}
	ticker := time.NewTicker(hostHealthCheckInterval)
	defer ticker.Stop()

	consecutiveFailures := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if yansuHostHealthy(client, a.cfg.yansuHostPort) {
				consecutiveFailures = 0
				continue
			}

			consecutiveFailures++
			if consecutiveFailures < hostHealthFailureLimit {
				continue
			}

			log.Printf("Isoform host bridge unavailable for %d consecutive checks; quitting orphaned helper",
				consecutiveFailures)
			a.quitApp()
			return
		}
	}
}

func yansuHostHealthy(client *http.Client, port int) bool {
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/api/health", port))
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// requireAuth is the only middleware. /health is unauthenticated so the
// supervisor can probe before we're ready; everything else needs the
// per-session token Yansu generated.
func (a *app) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		expected := a.cfg.ipcToken
		if expected == "" {
			next(w, r)
			return
		}
		got := r.Header.Get(tokenHeader)
		if subtle.ConstantTimeCompare([]byte(got), []byte(expected)) != 1 {
			http.Error(w, `{"error":"invalid token"}`, http.StatusForbidden)
			return
		}
		next(w, r)
	}
}

func (a *app) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// showPayload is what Yansu posts to /show — the foreground app context
// the user pressed the hotkey from.
type showPayload struct {
	BundleID    string `json:"bundleID,omitempty"`
	AppName     string `json:"appName,omitempty"`
	WindowTitle string `json:"windowTitle,omitempty"`
}

func (a *app) handleShow(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}
	var p showPayload
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		// Empty body is fine — Yansu may /show without context.
		p = showPayload{}
	}

	a.mu.Lock()
	ready := a.ctx != nil
	// Reset to pill defaults every summon so the user always sees the
	// same entry-point UI and a fresh auto-hide-on-blur.
	a.autoHideOnBlur = true
	a.lastSizeW, a.lastSizeH = pillWidth, pillHeight
	a.mu.Unlock()
	if !ready {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "app not started"})
		return
	}

	a.presentPanel(p)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *app) handleHide(w http.ResponseWriter, r *http.Request) {
	a.dismissPanel()
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *app) handleQuit(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	// Defer the actual quit by a tick so the response can flush.
	go func() {
		time.Sleep(100 * time.Millisecond)
		a.quitApp()
	}()
}

// newDisarmDismissRequest builds the POST that tells Isoform to disarm its
// dismiss-on-key flag. The host bridge wants the helper's token and a JSON
// body on every POST.
func newDisarmDismissRequest(yansuPort int, token string) (*http.Request, error) {
	req, err := http.NewRequest(http.MethodPost,
		fmt.Sprintf("http://127.0.0.1:%d/api/spotlight/disarm-dismiss-on-key", yansuPort),
		strings.NewReader("{}"))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(tokenHeader, token)
	return req, nil
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func main() {
	args := parseLaunchArgs()
	log.SetPrefix("[spotlight] ")
	log.Printf("launched listen_port=%d yansu_port=%d token_set=%v",
		args.listenPort, args.yansuHostPort, args.ipcToken != "")

	configureSpotlightAppPresentation()

	a := &app{cfg: args}
	lifecycleCtx, cancelLifecycle := context.WithCancel(context.Background())
	defer cancelLifecycle()
	go a.watchYansuHost(lifecycleCtx)
	run(a)
}
