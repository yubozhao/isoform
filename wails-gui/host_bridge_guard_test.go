package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	goruntime "runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const (
	testHostBridgePort       = 4242
	testHostBridgeToken      = "host-bridge-token-0123456789abcdef0123456789"
	testHostBridgeMediaToken = "media-token-0123456789abcdef0123456789abcdef"
	testSpotlightToken       = "spotlight-token-0123456789abcdef"
	testSpotlightAssetPort   = 5555
)

// newTestHostBridge returns a bridge with known tokens, a running Spotlight
// helper (as far as the supervisor knows), so every route is registered. Rejected requests never reach a handler.
func newTestHostBridge(t *testing.T) (*hostBridge, http.Handler) {
	t.Helper()
	app := &App{}
	app.spotlight = &spotlightSupervisor{
		cmd:       &exec.Cmd{},
		token:     testSpotlightToken,
		assetPort: testSpotlightAssetPort,
	}
	hb := &hostBridge{
		port:         testHostBridgePort,
		ctx:          context.Background(),
		app:          app,
		token:        testHostBridgeToken,
		mediaToken:   testHostBridgeMediaToken,
		allowedHosts: browserBridgeAllowedHosts(testHostBridgePort),
	}
	app.bridge = hb
	return hb, hb.routes()
}

func testSpotlightOrigin() string {
	return spotlightWebviewOrigin(goruntime.GOOS, testSpotlightAssetPort)
}

// hostBridgeRequest builds a request to a route that passes every check:
// the right method, Content-Type and token, from 127.0.0.1, with no Origin.
func hostBridgeRequest(rt hostBridgeRoute) *http.Request {
	path := rt.path
	if strings.HasSuffix(path, "/") {
		path += "session"
	}
	var req *http.Request
	switch rt.method {
	case http.MethodPost:
		req = httptest.NewRequest(http.MethodPost, path, strings.NewReader("{}"))
		req.Header.Set("Content-Type", "application/json")
	default:
		req = httptest.NewRequest(http.MethodGet, path, nil)
	}
	req.Host = "127.0.0.1:4242"
	req.RemoteAddr = "127.0.0.1:50000"
	switch rt.access {
	case hostAccessHelper:
		req.Header.Set(hostBridgeTokenHeader, testHostBridgeToken)
	case hostAccessSpotlight:
		req.Header.Set(spotlightTokenHeader, testSpotlightToken)
	case hostAccessMedia:
		q := req.URL.Query()
		q.Set(hostBridgeMediaTokenKey, testHostBridgeMediaToken)
		req.URL.RawQuery = q.Encode()
	default:
	}
	return req
}

func serve(h http.Handler, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func guardedRoutes(t *testing.T, hb *hostBridge) []hostBridgeRoute {
	t.Helper()
	var out []hostBridgeRoute
	for _, rt := range hb.routeTable() {
		if rt.access != hostAccessOpen {
			out = append(out, rt)
		}
	}
	if len(out) < 30 {
		t.Fatalf("expected every host bridge route in the table, got %d guarded routes", len(out))
	}
	return out
}

// Only /api/health goes without a token, and every route names a method.
func TestHostBridgeEveryRouteButHealthNeedsAToken(t *testing.T) {
	hb, _ := newTestHostBridge(t)
	seen := map[string]bool{}
	for _, rt := range hb.routeTable() {
		if seen[rt.path] {
			t.Errorf("%s is registered twice", rt.path)
		}
		seen[rt.path] = true
		if rt.access == hostAccessOpen && rt.path != "/api/health" {
			t.Errorf("%s needs no token; only /api/health may", rt.path)
		}
		if rt.method != http.MethodGet && rt.method != http.MethodPost {
			t.Errorf("%s has method %q", rt.path, rt.method)
		}
		if strings.HasPrefix(rt.path, "/api/spotlight/") && rt.access != hostAccessSpotlight {
			t.Errorf("%s must need the Spotlight token", rt.path)
		}
		if strings.HasPrefix(rt.path, "/api/activity/") && rt.access != hostAccessMedia {
			t.Errorf("%s must need the media token", rt.path)
		}
	}
	for _, path := range []string{
		"/api/computer-use/type", "/api/computer-use/hotkey", "/api/computer-use/paste",
		"/api/open-path", "/api/open-url", "/api/clipboard/write", "/api/notify",
	} {
		if !seen[path] {
			t.Errorf("%s is missing from the route table", path)
		}
	}
}

func TestHostBridgeRejectsForeignHosts(t *testing.T) {
	hb, h := newTestHostBridge(t)
	routes := append(guardedRoutes(t, hb), hostBridgeRoute{"/api/health", hostAccessOpen, http.MethodGet, nil})
	for _, rt := range routes {
		for _, host := range []string{"evil.example:4242", "127.0.0.1:9999", "127.0.0.1", "localhost.evil.example:4242", ""} {
			req := hostBridgeRequest(rt)
			req.Host = host
			if rec := serve(h, req); rec.Code != http.StatusForbidden {
				t.Errorf("%s with Host %q: got %d, want 403", rt.path, host, rec.Code)
			}
		}
	}
}

func TestHostBridgeRejectsNonLoopbackPeers(t *testing.T) {
	hb, h := newTestHostBridge(t)
	for _, rt := range guardedRoutes(t, hb) {
		req := hostBridgeRequest(rt)
		req.RemoteAddr = "192.168.1.20:50000"
		if rec := serve(h, req); rec.Code != http.StatusForbidden {
			t.Errorf("%s from 192.168.1.20: got %d, want 403", rt.path, rec.Code)
		}
	}
}

// A web page's Origin is refused on every route, and so is each allowed
// page's Origin on routes that are not its own.
func TestHostBridgeRejectsForeignOrigins(t *testing.T) {
	hb, h := newTestHostBridge(t)
	routes := append(guardedRoutes(t, hb), hostBridgeRoute{"/api/health", hostAccessOpen, http.MethodGet, nil})
	for _, rt := range routes {
		foreign := []string{"https://evil.example", "null", "http://127.0.0.1:4242", "http://localhost:5173"}
		switch rt.access {
		case hostAccessSpotlight:
			foreign = append(foreign, appWindowOrigin(), "http://127.0.0.1:5556")
		case hostAccessMedia:
			foreign = append(foreign, testSpotlightOrigin())
		default:
			foreign = append(foreign, appWindowOrigin(), testSpotlightOrigin())
		}
		for _, origin := range foreign {
			if origin == hb.allowedOrigin(rt.access) {
				t.Logf("%s: %q is the platform shared app/Spotlight origin, covered by allowed-origin tests", rt.path, origin)
				continue
			}
			req := hostBridgeRequest(rt)
			req.Header.Set("Origin", origin)
			rec := serve(h, req)
			if rec.Code != http.StatusForbidden {
				t.Errorf("%s from Origin %q: got %d, want 403", rt.path, origin, rec.Code)
			}
			if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
				t.Errorf("%s from Origin %q: Access-Control-Allow-Origin %q", rt.path, origin, got)
			}
		}
	}
}

// Preflights are answered only for the Spotlight webview on its routes.
func TestHostBridgeAnswersOnlyTheSpotlightPreflight(t *testing.T) {
	hb, h := newTestHostBridge(t)
	for _, rt := range guardedRoutes(t, hb) {
		for _, origin := range []string{"https://evil.example", appWindowOrigin(), testSpotlightOrigin(), ""} {
			req := httptest.NewRequest(http.MethodOptions, hostBridgeRequest(rt).URL.String(), nil)
			req.Host = "127.0.0.1:4242"
			req.RemoteAddr = "127.0.0.1:50000"
			if origin != "" {
				req.Header.Set("Origin", origin)
			}
			req.Header.Set("Access-Control-Request-Method", rt.method)
			rec := serve(h, req)
			allowed := rt.access == hostAccessSpotlight && origin == testSpotlightOrigin()
			if allowed {
				if rec.Code != http.StatusNoContent || rec.Header().Get("Access-Control-Allow-Origin") != origin {
					t.Errorf("%s preflight from the Spotlight webview: got %d %q", rt.path, rec.Code, rec.Header().Get("Access-Control-Allow-Origin"))
				}
				if !strings.Contains(rec.Header().Get("Access-Control-Allow-Headers"), spotlightTokenHeader) {
					t.Errorf("%s preflight must allow the Spotlight token header", rt.path)
				}
				continue
			}
			if rec.Code != http.StatusForbidden {
				t.Errorf("%s preflight from Origin %q: got %d, want 403", rt.path, origin, rec.Code)
			}
			if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
				t.Errorf("%s preflight from Origin %q: Access-Control-Allow-Origin %q", rt.path, origin, got)
			}
		}
	}
}

// A foreign page can send text/plain, a form or multipart without a
// preflight; none of them reaches a handler, even with the right token.
func TestHostBridgeRequiresJSONOnEveryPost(t *testing.T) {
	hb, h := newTestHostBridge(t)
	posts := 0
	for _, rt := range guardedRoutes(t, hb) {
		if rt.method != http.MethodPost {
			continue
		}
		posts++
		for _, ct := range []string{"", "text/plain", "text/plain;charset=UTF-8", "application/x-www-form-urlencoded", "multipart/form-data; boundary=x", "application/jsonx"} {
			req := hostBridgeRequest(rt)
			if ct == "" {
				req.Header.Del("Content-Type")
			} else {
				req.Header.Set("Content-Type", ct)
			}
			if rec := serve(h, req); rec.Code != http.StatusForbidden {
				t.Errorf("%s with Content-Type %q: got %d, want 403", rt.path, ct, rec.Code)
			}
		}
		// GET in place of POST is refused too.
		req := hostBridgeRequest(rt)
		req.Method = http.MethodGet
		if rec := serve(h, req); rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("GET %s: got %d, want 405", rt.path, rec.Code)
		}
	}
	if posts < 20 {
		t.Fatalf("expected the POST routes, got %d", posts)
	}
}

func TestHostBridgeRequiresTheRightToken(t *testing.T) {
	hb, h := newTestHostBridge(t)
	for _, rt := range guardedRoutes(t, hb) {
		cases := map[string]func(*http.Request){
			"no token": func(r *http.Request) {
				r.Header.Del(hostBridgeTokenHeader)
				r.Header.Del(spotlightTokenHeader)
				q := r.URL.Query()
				q.Del(hostBridgeMediaTokenKey)
				r.URL.RawQuery = q.Encode()
			},
			"wrong token": func(r *http.Request) {
				for _, k := range []string{hostBridgeTokenHeader, spotlightTokenHeader} {
					if r.Header.Get(k) != "" {
						r.Header.Set(k, "wrong")
					}
				}
				if r.URL.Query().Get(hostBridgeMediaTokenKey) != "" {
					q := r.URL.Query()
					q.Set(hostBridgeMediaTokenKey, "wrong")
					r.URL.RawQuery = q.Encode()
				}
			},
			// Each caller's token opens its own routes only.
			"another caller's token": func(r *http.Request) {
				r.Header.Del(hostBridgeTokenHeader)
				r.Header.Del(spotlightTokenHeader)
				q := r.URL.Query()
				q.Del(hostBridgeMediaTokenKey)
				switch rt.access {
				case hostAccessHelper:
					r.Header.Set(spotlightTokenHeader, testSpotlightToken)
					r.Header.Set(hostBridgeTokenHeader, testHostBridgeMediaToken)
					q.Set(hostBridgeMediaTokenKey, testHostBridgeMediaToken)
				case hostAccessSpotlight:
					r.Header.Set(hostBridgeTokenHeader, testHostBridgeToken)
					r.Header.Set(spotlightTokenHeader, testHostBridgeToken)
				case hostAccessMedia:
					r.Header.Set(hostBridgeTokenHeader, testHostBridgeToken)
					q.Set(hostBridgeMediaTokenKey, testHostBridgeToken)
				default:
				}
				r.URL.RawQuery = q.Encode()
			},
		}
		for name, mutate := range cases {
			req := hostBridgeRequest(rt)
			mutate(req)
			if rec := serve(h, req); rec.Code != http.StatusUnauthorized {
				t.Errorf("%s with %s: got %d, want 401", rt.path, name, rec.Code)
			}
		}
	}
}

// With no helper running there is no Spotlight token, and the routes refuse
// every caller, including one that sends no token at all.
func TestHostBridgeSpotlightRoutesRefuseWithoutAHelper(t *testing.T) {
	for name, sup := range map[string]*spotlightSupervisor{
		"no supervisor":      nil,
		"helper not running": {},
		"helper stopped":     {cmd: nil, token: "", assetPort: testSpotlightAssetPort},
	} {
		hb, _ := newTestHostBridge(t)
		hb.app.spotlight = sup
		h := hb.routes()
		for _, rt := range hb.spotlightRoutes() {
			for _, token := range []string{"", testSpotlightToken} {
				req := hostBridgeRequest(rt)
				if token == "" {
					req.Header.Del(spotlightTokenHeader)
				}
				if rec := serve(h, req); rec.Code != http.StatusUnauthorized {
					t.Errorf("%s: %s with token %q: got %d, want 401", name, rt.path, token, rec.Code)
				}
			}
			req := httptest.NewRequest(http.MethodOptions, hostBridgeRequest(rt).URL.String(), nil)
			req.Host = "127.0.0.1:4242"
			req.RemoteAddr = "127.0.0.1:50000"
			req.Header.Set("Origin", testSpotlightOrigin())
			if rec := serve(h, req); rec.Code != http.StatusForbidden {
				t.Errorf("%s: %s preflight: got %d, want 403", name, rt.path, rec.Code)
			}
		}
	}
}

// Screenshots and recordings no longer carry Access-Control-Allow-Origin: *.
func TestHostBridgeActivityMediaHasNoWildcardCORS(t *testing.T) {
	hb, h := newTestHostBridge(t)
	for _, rt := range hb.routeTable() {
		if rt.access != hostAccessMedia {
			continue
		}
		for _, origin := range []string{"", appWindowOrigin()} {
			req := hostBridgeRequest(rt)
			if origin != "" {
				req.Header.Set("Origin", origin)
			}
			rec := serve(h, req)
			// No path and no activity store: the handler answers 400 or 503,
			// past every check.
			if rec.Code != http.StatusBadRequest && rec.Code != http.StatusServiceUnavailable {
				t.Errorf("%s from %q: got %d %s, want the handler's 400 or 503", rt.path, origin, rec.Code, rec.Body.String())
			}
			if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
				t.Errorf("%s from %q: Access-Control-Allow-Origin %q", rt.path, origin, got)
			}
		}
	}
}

func TestHostBridgeHealthNeedsNoToken(t *testing.T) {
	_, h := newTestHostBridge(t)
	for _, host := range []string{"127.0.0.1:4242", "localhost:4242", "LOCALHOST:4242"} {
		req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
		req.Host = host
		req.RemoteAddr = "127.0.0.1:50000"
		rec := serve(h, req)
		if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"ok":true}` {
			t.Errorf("health on %s: %d %s", host, rec.Code, rec.Body.String())
		}
	}
	req := httptest.NewRequest(http.MethodPost, "/api/health", strings.NewReader("{}"))
	req.Host = "127.0.0.1:4242"
	req.RemoteAddr = "127.0.0.1:50000"
	if rec := serve(h, req); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST health: got %d, want 405", rec.Code)
	}
}

// The callers that hold a token get through: the Spotlight webview (with
// its Origin and a preflighted JSON POST), the helper's own Go code (no
// Origin), a local tool with the host-bridge token, and the activity page.
func TestHostBridgeLetsEachCallerWithItsTokenThrough(t *testing.T) {
	_, h := newTestHostBridge(t)

	webview := hostBridgeRequest(hostBridgeRoute{"/api/spotlight/disarm-dismiss-on-key", hostAccessSpotlight, http.MethodPost, nil})
	webview.Header.Set("Origin", testSpotlightOrigin())
	rec := serve(h, webview)
	if rec.Code != http.StatusOK || rec.Header().Get("Access-Control-Allow-Origin") != testSpotlightOrigin() {
		t.Errorf("Spotlight webview: %d %q %s", rec.Code, rec.Header().Get("Access-Control-Allow-Origin"), rec.Body.String())
	}

	helper := hostBridgeRequest(hostBridgeRoute{"/api/spotlight/disarm-dismiss-on-key", hostAccessSpotlight, http.MethodPost, nil})
	if rec := serve(h, helper); rec.Code != http.StatusOK {
		t.Errorf("Spotlight helper: %d %s", rec.Code, rec.Body.String())
	}

	// An empty URL fails the handler's own check: past the guard, nothing opens.
	tool := hostBridgeRequest(hostBridgeRoute{"/api/open-url", hostAccessHelper, http.MethodPost, nil})
	tool.Header.Set("Content-Type", "application/json; charset=utf-8")
	rec = serve(h, tool)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "only http/https") {
		t.Errorf("host-bridge token holder: %d %s", rec.Code, rec.Body.String())
	}

	media := hostBridgeRequest(hostBridgeRoute{"/api/activity/snapshot", hostAccessMedia, http.MethodGet, nil})
	rec = serve(h, media)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "path parameter required") {
		t.Errorf("activity page: %d %s", rec.Code, rec.Body.String())
	}
}

func TestSpotlightWebviewOrigin(t *testing.T) {
	cases := []struct {
		goos string
		port int
		want string
	}{
		{"darwin", 5555, "http://127.0.0.1:5555"},
		{"darwin", 0, ""},
		{"windows", 0, "http://wails.localhost"},
		{"linux", 0, "wails://wails"},
	}
	for _, c := range cases {
		if got := spotlightWebviewOrigin(c.goos, c.port); got != c.want {
			t.Errorf("spotlightWebviewOrigin(%s, %d) = %q, want %q", c.goos, c.port, got, c.want)
		}
	}
	running := &spotlightSupervisor{cmd: &exec.Cmd{}, token: "t", assetPort: 5555}
	if got := running.WebviewOrigin(); got != spotlightWebviewOrigin(goruntime.GOOS, 5555) {
		t.Errorf("running helper origin = %q", got)
	}
	if got := (&spotlightSupervisor{assetPort: 5555}).WebviewOrigin(); got != "" {
		t.Errorf("stopped helper origin = %q, want none", got)
	}
}

func TestNewHostBridgeTokens(t *testing.T) {
	t.Setenv(hostBridgeTokenEnv, "")
	a, err := newHostBridge(context.Background(), &App{}, 1)
	if err != nil {
		t.Fatal(err)
	}
	b, err := newHostBridge(context.Background(), &App{}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.token) != 64 || len(a.mediaToken) != 64 {
		t.Fatalf("token lengths %d %d", len(a.token), len(a.mediaToken))
	}
	if a.token == b.token || a.mediaToken == b.mediaToken || a.token == a.mediaToken {
		t.Fatal("tokens must be fresh per launch and differ from each other")
	}
	if !a.allowedHosts["127.0.0.1:1"] || !a.allowedHosts["localhost:1"] || len(a.allowedHosts) != 2 {
		t.Fatalf("allowed hosts %v", a.allowedHosts)
	}

	t.Setenv(hostBridgeTokenEnv, "too-short")
	if c, _ := newHostBridge(context.Background(), &App{}, 1); c.token == "too-short" {
		t.Fatal("a short YANSU_HOST_BRIDGE_TOKEN must be ignored")
	}
	chosen := strings.Repeat("e2e", 11)
	t.Setenv(hostBridgeTokenEnv, chosen)
	if c, _ := newHostBridge(context.Background(), &App{}, 1); c.token != chosen || c.mediaToken == chosen {
		t.Fatalf("YANSU_HOST_BRIDGE_TOKEN sets the host-bridge token only: %q %q", c.token, c.mediaToken)
	}
	if _, ok := os.LookupEnv(hostBridgeTokenEnv); ok {
		t.Fatal("YANSU_HOST_BRIDGE_TOKEN must leave the environment so child processes never inherit it")
	}
}

// The helper binds its own ports and reports them on stdout; only the first
// report of each counts, and only from the running helper.
func TestSpotlightSupervisorTakesPortsFromTheHelpersStdout(t *testing.T) {
	cmd := &exec.Cmd{}
	s := &spotlightSupervisor{cmd: cmd, token: "t"}
	w := &spotlightLogWriter{stream: "stdout", onLine: func(line string) { s.recordHelperPort(cmd, line) }}
	_, _ = w.Write([]byte("[spotlight] launched\nSPOTLIGHT_ASSET_PORT=5555\nSPOTLIGHT_IPC_"))
	_, _ = w.Write([]byte("PORT=6666\r\nSPOTLIGHT_IPC_PORT=7777\nSPOTLIGHT_ASSET_PORT=8888\n"))
	if s.port != 6666 || s.assetPort != 5555 {
		t.Fatalf("port %d asset %d, want the first reports 6666 and 5555", s.port, s.assetPort)
	}
	for _, line := range []string{"SPOTLIGHT_IPC_PORT=abc", "SPOTLIGHT_IPC_PORT=0", "SPOTLIGHT_IPC_PORT=70000", "x SPOTLIGHT_IPC_PORT=1"} {
		fresh := &spotlightSupervisor{cmd: cmd}
		fresh.recordHelperPort(cmd, line)
		if fresh.port != 0 {
			t.Errorf("%q set port %d", line, fresh.port)
		}
	}
	stale := &spotlightSupervisor{cmd: &exec.Cmd{}}
	stale.recordHelperPort(cmd, "SPOTLIGHT_IPC_PORT=1234")
	if stale.port != 0 {
		t.Error("a previous helper's line must not set the running helper's port")
	}
	if got := s.WebviewOrigin(); got != spotlightWebviewOrigin(goruntime.GOOS, 5555) {
		t.Errorf("WebviewOrigin = %q", got)
	}
}

// fakeSpotlightHelper answers /health and counts /show calls that carry the
// IPC token "t". It returns its port as the helper would print it.
func fakeSpotlightHelper(t *testing.T) (port string, shows *atomic.Int32) {
	shows = &atomic.Int32{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/show" && r.Header.Get("X-Yansu-Spotlight-Token") == "t" {
			shows.Add(1)
		}
	}))
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return u.Port(), shows
}

// The first hotkey press after launch can come before the helper has printed
// its ports; Show waits for them instead of falling back at once.
func TestSpotlightShowWaitsForTheHelpersPorts(t *testing.T) {
	port, shows := fakeSpotlightHelper(t)
	s := newSpotlightSupervisor(0)
	if err := s.Show("", ""); !errors.Is(err, ErrSpotlightUnavailable) {
		t.Fatalf("no helper: Show = %v, want ErrSpotlightUnavailable", err)
	}
	cmd := &exec.Cmd{}
	s.cmd, s.token = cmd, "t"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.waitForReady(ctx)
	go func() {
		time.Sleep(100 * time.Millisecond)
		s.recordHelperPort(cmd, "SPOTLIGHT_ASSET_PORT=5555")
		s.recordHelperPort(cmd, "SPOTLIGHT_IPC_PORT="+port)
	}()
	if err := s.Show("com.example", "Example"); err != nil {
		t.Fatalf("Show before the ports arrived = %v, want the helper to show", err)
	}
	if shows.Load() != 1 {
		t.Fatalf("/show calls = %d, want 1", shows.Load())
	}
}

// On macOS a helper that answers /health but never reported its asset port
// (a stale build) would show a pill the host bridge refuses; Show falls back
// to the in-app overlay instead.
func TestSpotlightShowFallsBackWithoutAnAssetPort(t *testing.T) {
	if spotlightWebviewOrigin(goruntime.GOOS, 0) != "" {
		t.Skip("the helper's webview origin needs no asset port on " + goruntime.GOOS)
	}
	port, shows := fakeSpotlightHelper(t)
	cmd := &exec.Cmd{}
	s := newSpotlightSupervisor(0)
	s.cmd, s.token = cmd, "t"
	s.recordHelperPort(cmd, "SPOTLIGHT_IPC_PORT="+port)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.waitForReady(ctx)
	if err := s.Show("", ""); !errors.Is(err, ErrSpotlightUnavailable) {
		t.Fatalf("Show = %v, want ErrSpotlightUnavailable", err)
	}
	if s.IsReady() {
		t.Fatal("a helper with no asset port must not be ready")
	}
	s.ready.Store(true)
	if err := s.Show("", ""); !errors.Is(err, ErrSpotlightUnavailable) {
		t.Fatalf("ready without an asset port: Show = %v, want ErrSpotlightUnavailable", err)
	}
	if shows.Load() != 0 {
		t.Fatalf("/show calls = %d, want 0", shows.Load())
	}
}
