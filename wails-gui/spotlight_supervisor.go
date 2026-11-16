package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// spotlightSupervisor owns the lifecycle of the quick-chat-spotlight
// helper process: locating the binary, spawning it with a fresh shared
// IPC token, periodic health-checking, restart-on-crash, and graceful
// shutdown. The hotkey trigger handler in quick_chat_hotkey_*.go calls
// (*spotlightSupervisor).Show to signal the helper without caring
// whether the binary is even installed — if it isn't, Show returns a
// "not available" error and the caller can fall back to the in-app
// overlay path.
//
// We deliberately keep this surface narrow (Start / Show / Hide /
// Stop / IsReady / Token) so the rest of the codebase doesn't grow
// dependencies on the helper's internals.
type spotlightSupervisor struct {
	mu    sync.Mutex
	cmd   *exec.Cmd
	port  int
	token string
	// assetPort is where the macOS helper serves its webview's pages, so
	// its Origin is http://127.0.0.1:<assetPort>. The helper binds it and
	// reports it on stdout (SPOTLIGHT_ASSET_PORT=); unused on Windows and
	// Linux.
	assetPort int
	ready     atomic.Bool
	cancelCtx context.CancelFunc
	binPath   string
	httpc     *http.Client
	// exited is closed by reapOnExit once cmd.Wait returns. Stop
	// blocks on this rather than calling cmd.Wait itself — Wait can
	// only be invoked once per *exec.Cmd, and on Windows a duplicate
	// Wait blocks indefinitely waiting on a released process handle,
	// which used to make Stop hit its 1.5s timeout even when the
	// helper had already exited cleanly (see yansu-gui.log: pairs of
	// "helper exited cleanly" + "did not exit in 1.5s, killing").
	exited chan struct{}
	// yansuHostPort is the host-bridge port the spotlight's webview
	// should call back to (for /api/spotlight/suggestions, /submit,
	// /stream/:id). Forwarded to the helper via --yansu-port=<n>.
	yansuHostPort int
}

// spotlightHelperBinaryName returns the on-disk filename of the helper
// binary on the current platform.
func spotlightHelperBinaryName() string {
	if goruntime.GOOS == "windows" {
		return "quick-chat-spotlight.exe"
	}
	return "quick-chat-spotlight"
}

func isOrphanedSpotlightHelper(
	currentPID, candidatePID, parentPID int,
	commandLine, binaryPath string,
) bool {
	if currentPID <= 0 || candidatePID <= 0 || candidatePID == currentPID ||
		parentPID != 1 {
		return false
	}
	binaryPath = strings.TrimSpace(binaryPath)
	return binaryPath != "" && strings.Contains(commandLine, binaryPath)
}

// newSpotlightSupervisor returns a supervisor that has not yet spawned
// anything. Call Start to launch. yansuHostPort is the port the host
// bridge is listening on; the spotlight forwards it to its frontend.
func newSpotlightSupervisor(yansuHostPort int) *spotlightSupervisor {
	return &spotlightSupervisor{
		httpc:         &http.Client{Timeout: 1500 * time.Millisecond},
		yansuHostPort: yansuHostPort,
	}
}

// Start locates the helper binary and spawns it. Non-fatal: if the
// binary isn't installed (typical in dev), Start returns nil and
// IsReady stays false. The caller can keep going.
func (s *spotlightSupervisor) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.cmd != nil {
		return nil
	}

	bin := findSpotlightBinary()
	if bin == "" {
		log.Printf("[spotlight] helper binary not found; global hotkey will fall back to in-app overlay")
		return nil
	}
	cleanupOrphanedSpotlightHelpers(bin)

	token, err := generateSpotlightToken()
	if err != nil {
		return fmt.Errorf("generate spotlight token: %w", err)
	}

	supCtx, cancel := context.WithCancel(ctx)
	s.cancelCtx = cancel

	// --listen-port=0: the helper binds its own ports and reports them on
	// stdout, so no other local process can take a port between our pick
	// and its bind and receive the token meant for the helper.
	cmd := exec.CommandContext(supCtx, bin,
		"--listen-port=0",
		fmt.Sprintf("--ipc-token=%s", token),
		fmt.Sprintf("--yansu-port=%d", s.yansuHostPort),
	)
	cmd.Stdout = &spotlightLogWriter{stream: "stdout", onLine: func(line string) { s.recordHelperPort(cmd, line) }}
	cmd.Stderr = newSpotlightLogWriter("stderr")

	if err := cmd.Start(); err != nil {
		cancel()
		return fmt.Errorf("spawn spotlight: %w", err)
	}

	// On Windows: assign the helper to a Job Object with KILL_ON_JOB_CLOSE
	// so it dies automatically when Yansu exits — even on hard crash, or
	// when the user kills Yansu via Task Manager. No-op on other platforms.
	bindHelperToParentLifecycle(cmd)

	exited := make(chan struct{})
	s.cmd = cmd
	s.port = 0
	s.token = token
	s.assetPort = 0
	s.binPath = bin
	s.exited = exited

	go s.waitForReady(supCtx)
	go s.reapOnExit(cmd, exited)

	log.Printf("[spotlight] helper spawned: bin=%s yansu_port=%d", bin, s.yansuHostPort)
	return nil
}

// Lines the helper prints on stdout once it has bound a port.
const (
	spotlightIPCPortLine   = "SPOTLIGHT_IPC_PORT="
	spotlightAssetPortLine = "SPOTLIGHT_ASSET_PORT="
)

// recordHelperPort takes the IPC and asset ports from the helper's stdout.
// Only the first report of each counts, and only while cmd is the running
// helper.
func (s *spotlightSupervisor) recordHelperPort(cmd *exec.Cmd, line string) {
	var key string
	switch {
	case strings.HasPrefix(line, spotlightIPCPortLine):
		key = spotlightIPCPortLine
	case strings.HasPrefix(line, spotlightAssetPortLine):
		key = spotlightAssetPortLine
	default:
		return
	}
	port, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, key)))
	if err != nil || port <= 0 || port > 65535 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cmd != cmd {
		return
	}
	switch key {
	case spotlightIPCPortLine:
		if s.port == 0 {
			s.port = port
		}
	case spotlightAssetPortLine:
		if s.assetPort == 0 {
			s.assetPort = port
		}
	default:
	}
}

// SetHostBridgePort sets the host-bridge port passed to the next helper.
func (s *spotlightSupervisor) SetHostBridgePort(port int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.yansuHostPort = port
}

// IsReady reports whether the supervisor has confirmed the helper's
// HTTP server is responding on /health.
func (s *spotlightSupervisor) IsReady() bool { return s.ready.Load() }

// Token returns the per-session IPC token, used by host_bridge_spotlight
// to authenticate inbound calls from the helper.
func (s *spotlightSupervisor) Token() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.token
}

// WebviewOrigin is the Origin the running helper's webview sends to the
// host bridge, or "" when no helper runs.
func (s *spotlightSupervisor) WebviewOrigin() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cmd == nil || s.token == "" {
		return ""
	}
	return spotlightWebviewOrigin(goruntime.GOOS, s.assetPort)
}

// spotlightWebviewOrigin is the helper webview's Origin on goos: on macOS
// the native panel loads http://127.0.0.1:<assetPort>/ from the helper's
// asset server (quick-chat-spotlight/asset_server_darwin.go); elsewhere the
// helper is a Wails window, served from http://wails.localhost on Windows
// and wails://wails on Linux.
func spotlightWebviewOrigin(goos string, assetPort int) string {
	switch goos {
	case "darwin":
		if assetPort <= 0 {
			return ""
		}
		return fmt.Sprintf("http://127.0.0.1:%d", assetPort)
	case "windows":
		return "http://wails.localhost"
	default:
		return "wails://wails"
	}
}

// Stop sends a /quit signal to the helper, waits briefly, then kills.
// Safe to call when not started.
func (s *spotlightSupervisor) Stop() {
	s.mu.Lock()
	cmd := s.cmd
	cancel := s.cancelCtx
	port := s.port
	token := s.token
	exited := s.exited
	s.cmd = nil
	s.cancelCtx = nil
	s.port = 0
	s.token = ""
	s.assetPort = 0
	s.exited = nil
	s.mu.Unlock()
	s.ready.Store(false)

	if cmd == nil {
		return
	}

	if port != 0 {
		req, err := http.NewRequest(http.MethodPost,
			fmt.Sprintf("http://127.0.0.1:%d/quit", port), nil)
		if err == nil {
			req.Header.Set("X-Yansu-Spotlight-Token", token)
			if resp, err := s.httpc.Do(req); err == nil {
				resp.Body.Close()
			}
		}
	}

	// Wait for reapOnExit to confirm the OS-level process exit.
	// We can't call cmd.Wait ourselves — it's already in-flight in
	// reapOnExit, and Wait isn't safe to call twice.
	select {
	case <-exited:
	case <-time.After(1500 * time.Millisecond):
		log.Printf("[spotlight] helper did not exit in 1.5s, killing")
		_ = cmd.Process.Kill()
		<-exited
	}

	if cancel != nil {
		cancel()
	}
}

// Show signals the helper to bring up the pill window with the given
// foreground-app context. Returns ErrSpotlightUnavailable when the
// helper isn't installed, or when it doesn't become ready within a
// short grace window.
func (s *spotlightSupervisor) Show(bundleID, appName, windowTitle string) error {
	s.mu.Lock()
	running := s.cmd != nil
	s.mu.Unlock()
	if !running {
		// No helper process spawned at all (binary missing, etc.);
		// fall back immediately so the caller can route to NSPanel.
		return ErrSpotlightUnavailable
	}
	// Helper is spawned but may not have reported its ports on stdout
	// yet, or may still be inside its initial /health probe loop —
	// typically <300ms after launch, bounded by waitForReady's 8s
	// budget. Wait briefly to absorb the cold-start window so the very
	// first hotkey press after app launch lands on the pill UI instead
	// of the legacy NSPanel. 750ms stays well under the threshold where
	// users perceive a delay, and the loop exits the instant ready flips.
	if !s.ready.Load() {
		deadline := time.Now().Add(750 * time.Millisecond)
		for !s.ready.Load() && time.Now().Before(deadline) {
			time.Sleep(25 * time.Millisecond)
		}
		if !s.ready.Load() {
			return ErrSpotlightUnavailable
		}
	}
	s.mu.Lock()
	port := s.port
	token := s.token
	origin := spotlightWebviewOrigin(goruntime.GOOS, s.assetPort)
	s.mu.Unlock()
	if port == 0 || origin == "" {
		// Stopped meanwhile, or a helper whose webview the host bridge
		// would refuse (no asset port reported): fall back.
		return ErrSpotlightUnavailable
	}
	body, err := json.Marshal(map[string]string{
		"bundleID":    bundleID,
		"appName":     appName,
		"windowTitle": windowTitle,
	})
	if err != nil {
		return fmt.Errorf("marshal show payload: %w", err)
	}
	req, err := http.NewRequest(http.MethodPost,
		fmt.Sprintf("http://127.0.0.1:%d/show", port), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Yansu-Spotlight-Token", token)
	resp, err := s.httpc.Do(req)
	if err != nil {
		return fmt.Errorf("spotlight /show: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("spotlight /show: status %d", resp.StatusCode)
	}
	return nil
}

// Hide signals the helper to dismiss the pill (or conversation) window.
// Best-effort: errors are logged, not returned.
func (s *spotlightSupervisor) Hide() {
	s.mu.Lock()
	port := s.port
	token := s.token
	s.mu.Unlock()
	if port == 0 {
		return
	}
	req, err := http.NewRequest(http.MethodPost,
		fmt.Sprintf("http://127.0.0.1:%d/hide", port), nil)
	if err != nil {
		return
	}
	req.Header.Set("X-Yansu-Spotlight-Token", token)
	if resp, err := s.httpc.Do(req); err == nil {
		resp.Body.Close()
	}
}

// ── Internal ────────────────────────────────────────────────────────

// ErrSpotlightUnavailable is returned by Show when the helper isn't
// installed or hasn't finished starting up.
var ErrSpotlightUnavailable = errors.New("spotlight helper not available")

// waitForReady polls the helper's /health endpoint every 100ms for up
// to 8 seconds (covers cold starts on slow machines). Sets s.ready
// when healthy and its webview origin is known. Logs and gives up
// otherwise — Show will simply return ErrSpotlightUnavailable until a
// future health probe succeeds.
func (s *spotlightSupervisor) waitForReady(ctx context.Context) {
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return
		default:
		}
		s.mu.Lock()
		running := s.cmd != nil
		port := s.port
		origin := spotlightWebviewOrigin(goruntime.GOOS, s.assetPort)
		s.mu.Unlock()
		if !running {
			return
		}
		// The ports arrive on the helper's stdout. /health needs no token,
		// so none is sent before the helper has answered. A macOS helper
		// that never reports its asset port (a stale build) stays unready:
		// the host bridge would refuse its webview, so the pill would be dead.
		if port != 0 && origin != "" {
			req, err := http.NewRequest(http.MethodGet,
				fmt.Sprintf("http://127.0.0.1:%d/health", port), nil)
			if err == nil {
				if resp, err := s.httpc.Do(req); err == nil {
					resp.Body.Close()
					if resp.StatusCode == 200 {
						s.ready.Store(true)
						log.Printf("[spotlight] helper ready on 127.0.0.1:%d", port)
						return
					}
				}
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	log.Printf("[spotlight] helper did not become ready in 8s — Show calls will fall through")
}

// reapOnExit waits for the helper to exit and clears state. Logs
// non-zero exits so users can debug from yansu-gui.log. Closes the
// supplied `exited` channel last so any Stop() caller waiting on it
// is unblocked only after cmd.Wait has fully returned.
func (s *spotlightSupervisor) reapOnExit(cmd *exec.Cmd, exited chan struct{}) {
	err := cmd.Wait()
	s.mu.Lock()
	if s.cmd == cmd {
		s.cmd = nil
		s.port = 0
		s.token = ""
		s.assetPort = 0
		s.exited = nil
	}
	s.mu.Unlock()
	s.ready.Store(false)
	if err != nil {
		log.Printf("[spotlight] helper exited: %v", err)
	} else {
		log.Printf("[spotlight] helper exited cleanly")
	}
	close(exited)
}

// findSpotlightBinary locates the helper using the same fallback
// pattern bundled_assets.go uses for the Yansu CLI: macOS packaged
// helper .app, legacy flat release layouts, then cwd/dev paths.
// Returns "" if no candidate exists.
func findSpotlightBinary() string {
	if override := strings.TrimSpace(os.Getenv("YANSU_SPOTLIGHT_PATH")); override != "" {
		if spotlightFileExists(override) {
			return override
		}
	}
	for _, c := range spotlightBinaryCandidates() {
		if spotlightFileExists(c) {
			return c
		}
	}
	return ""
}

// spotlightBinaryCandidates returns the on-disk locations to probe
// for the helper binary, in priority order: the packaged macOS helper
// .app, legacy flat release layouts, and finally the dev build dir for
// runs out of source. YANSU_SPOTLIGHT_PATH wins outright (see
// findSpotlightBinary).
//
// On macOS, Wails packages the helper as its own `.app` bundle under
// build/bin/, so the actual executable lives at
// `quick-chat-spotlight.app/Contents/MacOS/quick-chat-spotlight` rather
// than directly in build/bin. Release builds preserve that helper .app
// under Yansu.app/Contents/Resources so LaunchServices can read its
// LSUIElement plist and keep it out of the Dock. We still probe the old
// flat layouts after the .app candidate for compatibility with existing
// local builds.
func spotlightBinaryCandidates() []string {
	name := spotlightHelperBinaryName()
	darwinBundleExe := func(appPath string) string {
		return filepath.Join(appPath, "Contents", "MacOS", name)
	}
	devPaths := func(base string) []string {
		binDir := filepath.Join(base, "quick-chat-spotlight", "build", "bin")
		flat := filepath.Join(binDir, name)
		if goruntime.GOOS == "darwin" {
			// `wails build` on macOS only ever emits the .app bundle, so
			// the freshest helper always lives inside it. A flat binary
			// in build/bin/ on darwin is either a stale leftover from an
			// older build script or a manual drop-in — probe it last so
			// the rebuilt .app wins over a forgotten artifact.
			return []string{
				filepath.Join(binDir, "quick-chat-spotlight.app", "Contents", "MacOS", name),
				flat,
			}
		}
		return []string{flat}
	}
	out := make([]string, 0, 8)
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		if goruntime.GOOS == "darwin" {
			resourcesDir := filepath.Join(dir, "..", "Resources")
			out = append(out,
				darwinBundleExe(filepath.Join(resourcesDir, "quick-chat-spotlight.app")),
				filepath.Join(resourcesDir, name),
				filepath.Join(dir, name),
			)
		} else {
			out = append(out,
				filepath.Join(dir, name),
				filepath.Join(dir, "..", "Resources", name),
			)
		}
		out = append(out, devPaths(dir)...)
	}
	if wd, err := os.Getwd(); err == nil {
		out = append(out, devPaths(wd)...)
	}
	return out
}

func spotlightFileExists(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return !info.IsDir()
}

func generateSpotlightToken() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// spotlightLogWriter forwards each line the helper writes to its
// stdout/stderr into Yansu's main log under a tagged prefix, so
// the whole story shows up in ~/.yansu-agent/yansu-gui.log.
type spotlightLogWriter struct {
	stream string
	buf    bytes.Buffer
	// onLine, when set, sees every complete line.
	onLine func(line string)
}

func newSpotlightLogWriter(stream string) io.Writer {
	return &spotlightLogWriter{stream: stream}
}

func (w *spotlightLogWriter) Write(p []byte) (int, error) {
	w.buf.Write(p)
	for {
		line, err := w.buf.ReadString('\n')
		if err == io.EOF {
			// no newline yet — push back the partial line
			w.buf.Reset()
			w.buf.WriteString(line)
			break
		}
		line = strings.TrimRight(line, "\r\n")
		if w.onLine != nil {
			w.onLine(line)
		}
		log.Printf("[spotlight:%s] %s", w.stream, line)
	}
	return len(p), nil
}
