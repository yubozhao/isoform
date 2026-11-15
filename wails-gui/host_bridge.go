package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/skratchdot/open-golang/open"
	"github.com/wailsapp/wails/v2/pkg/runtime"

	"wails-gui/internal/platform/clipboard"
)

// hostBridge is a localhost-only HTTP server that exposes OS-level
// operations to Isoform's helper processes (the Spotlight helper), the
// activity page's screenshots and recordings, and local tools that hold the
// host-bridge token (the computer-use e2e tests). Every route goes through
// guard (host_bridge_guard.go).
type hostBridge struct {
	srv  *http.Server
	port int
	ctx  context.Context // Wails app context for BrowserOpenURL
	app  *App            // back-reference for handlers needing App state

	token           string // per-launch host-bridge token
	mediaToken      string // per-launch token for /api/activity/*
	allowedHosts    map[string]bool
	mcpEndpointPath string
	mcpSocketPath   string
	mcpServer       *http.Server
	mcpLifecycleMu  sync.Mutex
}

type bridgeResponse struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// startHostBridge starts the host bridge HTTP server on a random free port
// bound to 127.0.0.1. The provided ctx is the Wails app context used for
// runtime.BrowserOpenURL.
func startHostBridge(ctx context.Context, app *App) (*hostBridge, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("host bridge listen: %w", err)
	}

	port := listener.Addr().(*net.TCPAddr).Port

	hb, err := newHostBridge(ctx, app, port)
	if err != nil {
		listener.Close()
		return nil, err
	}

	hb.srv = &http.Server{
		Handler:      hb.routes(),
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 120 * time.Second,
		IdleTimeout:  30 * time.Second,
	}
	if err := hb.publishMCPReadEndpoint(); err != nil {
		log.Printf("[host-bridge] MCP read discovery unavailable: %v", err)
	}

	go func() {
		log.Printf("[host-bridge] listening on 127.0.0.1:%d", port)
		if err := hb.srv.Serve(listener); err != nil && err != http.ErrServerClosed {
			log.Printf("[host-bridge] serve error: %v", err)
		}
	}()

	return hb, nil
}

// newHostBridge sets up the bridge for port with fresh per-launch tokens.
func newHostBridge(ctx context.Context, app *App, port int) (*hostBridge, error) {
	token := hostBridgeTokenFromEnv()
	if token == "" {
		var err error
		if token, err = newBrowserBridgeToken(); err != nil {
			return nil, fmt.Errorf("host bridge token: %w", err)
		}
	}
	mediaToken, err := newBrowserBridgeToken()
	if err != nil {
		return nil, fmt.Errorf("host bridge media token: %w", err)
	}
	return &hostBridge{
		port:         port,
		ctx:          ctx,
		app:          app,
		token:        token,
		mediaToken:   mediaToken,
		allowedHosts: browserBridgeAllowedHosts(port),
	}, nil
}

// routes registers every route through guard.
func (hb *hostBridge) routes() *http.ServeMux {
	mux := http.NewServeMux()
	for _, rt := range hb.routeTable() {
		mux.HandleFunc(rt.path, hb.guard(rt, rt.handler))
	}
	return mux
}

// routeTable lists every host bridge route.
func (hb *hostBridge) routeTable() []hostBridgeRoute {
	routes := []hostBridgeRoute{
		{"/api/health", hostAccessOpen, http.MethodGet, hb.handleHealth},
		{"/api/open-url", hostAccessHelper, http.MethodPost, hb.handleOpenURL},
		{"/api/open-path", hostAccessHelper, http.MethodPost, hb.handleOpenPath},
		{"/api/clipboard/write", hostAccessHelper, http.MethodPost, hb.handleClipboardWrite},
		{"/api/notify", hostAccessHelper, http.MethodPost, hb.handleNotify},
		{"/api/activity/video", hostAccessMedia, http.MethodGet, hb.app.handleActivityVideo},
		{"/api/activity/frame", hostAccessMedia, http.MethodGet, hb.app.handleActivityFrame},
		{"/api/activity/snapshot", hostAccessMedia, http.MethodGet, hb.app.handleActivitySnapshot},
		{"/api/activity/audio", hostAccessMedia, http.MethodGet, hb.app.handleActivityMeetingAudio},
		{"/api/activity/recording", hostAccessMedia, http.MethodGet, hb.app.handleActivityRecordingAudio},
	}
	// Computer use endpoints for desktop interaction.
	routes = append(routes, hb.computerUseRoutes()...)
	// Quick Chat Spotlight endpoints, see DESIGN.md in
	// quick-chat-spotlight/. Registered unconditionally; until the
	// supervisor runs a helper there is no Spotlight token and every
	// call is refused.
	routes = append(routes, hb.spotlightRoutes()...)
	// cua-driver passthrough (macOS-only when the bundle is shipped).
	routes = append(routes, hb.cuaRoutes()...)
	return routes
}

// URL returns the base URL of the host bridge (e.g. "http://127.0.0.1:12345").
func (hb *hostBridge) URL() string {
	return fmt.Sprintf("http://127.0.0.1:%d", hb.port)
}

// Port returns the TCP port the bridge is listening on. Used by the
// spotlight supervisor to forward the address to the helper process so
// its frontend can call back into /api/spotlight/* without the user
// having to set VITE_YANSU_PORT at build time.
func (hb *hostBridge) Port() int {
	return hb.port
}

// Stop gracefully shuts down the host bridge server.
func (hb *hostBridge) Stop() {
	hb.removeMCPReadEndpoint()
	if hb.srv == nil {
		return
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := hb.srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("[host-bridge] shutdown error: %v", err)
	}
	log.Printf("[host-bridge] stopped")
}

// ── Handlers ─────────────────────────────────────────────────────────

func (hb *hostBridge) handleHealth(w http.ResponseWriter, _ *http.Request) {
	hb.writeOK(w)
}

func (hb *hostBridge) handleOpenURL(w http.ResponseWriter, r *http.Request) {
	var body struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		hb.writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	parsed, err := url.Parse(body.URL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		hb.writeError(w, http.StatusBadRequest, "only http/https URLs allowed")
		return
	}

	runtime.BrowserOpenURL(hb.ctx, body.URL)
	hb.writeOK(w)
}

func (hb *hostBridge) handleOpenPath(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		hb.writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	if body.Path == "" || strings.Contains(body.Path, "..") {
		hb.writeError(w, http.StatusBadRequest, "invalid path")
		return
	}

	if err := open.Start(body.Path); err != nil {
		hb.writeError(w, http.StatusInternalServerError, fmt.Sprintf("open failed: %v", err))
		return
	}
	hb.writeOK(w)
}

func (hb *hostBridge) handleClipboardWrite(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		hb.writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	if err := clipboard.Write(body.Text); err != nil {
		hb.writeError(w, http.StatusInternalServerError, fmt.Sprintf("clipboard write failed: %v", err))
		return
	}
	hb.writeOK(w)
}

func (hb *hostBridge) handleNotify(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Title string `json:"title"`
		Body  string `json:"body"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		hb.writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	hb.app.notifyAutomaticDesktop(body.Title, body.Body, "")

	hb.writeOK(w)
}

// ── Response helpers ─────────────────────────────────────────────────

func (hb *hostBridge) writeOK(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(bridgeResponse{OK: true})
}

func (hb *hostBridge) writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(bridgeResponse{OK: false, Error: msg})
}
