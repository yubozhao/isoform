//go:build darwin

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"runtime"
	"time"
)

// Cocoa requires GUI work on the process's main thread. Locking the main
// goroutine to it at startup guarantees startSpotlightPanel and the run
// loop run there.
func init() { runtime.LockOSThread() }

// Host-bridge coordinates for reporting back to the main Yansu app. Set
// once in run() from the launch flags.
var (
	spotlightYansuPort int
	spotlightToken     string
)

// run is the macOS shell: a native non-activating NSPanel hosting a
// WKWebView. Assets come from an in-process HTTP server; the panel never
// activates the spotlight process, so summoning it doesn't steal focus
// from the user's frontmost app.
func run(a *app) {
	a.mu.Lock()
	a.ctx = context.Background()
	a.mu.Unlock()

	spotlightYansuPort = a.cfg.yansuHostPort
	spotlightToken = a.cfg.ipcToken

	assetPort, err := startAssetServer()
	if err != nil {
		log.Fatalf("[spotlight] asset server: %v", err)
	}
	// Isoform's host bridge allows exactly this page origin; report it
	// before the webview loads and calls the bridge.
	fmt.Printf("SPOTLIGHT_ASSET_PORT=%d\n", assetPort)

	url := fmt.Sprintf("http://127.0.0.1:%d/", assetPort)
	startSpotlightPanel(url, spotlightConfigJSON(a.cfg),
		pillWidth, pillHeight, conversationWidth, conversationHeight, spotlightBottomFraction)

	go a.serveIPC(a.ctx)

	runSpotlightPanelLoop() // blocks until quit
}

// spotlightConfigJSON serializes the host-bridge port + IPC token the
// native panel injects as window.__spotlightConfig (replaces the Wails
// GetConfig binding). Keys must match SpotlightConfig's json tags.
func spotlightConfigJSON(cfg launchArgs) string {
	b, err := json.Marshal(SpotlightConfig{
		YansuPort: cfg.yansuHostPort,
		Token:     cfg.ipcToken,
	})
	if err != nil {
		return "{}"
	}
	return string(b)
}

// presentPanel surfaces the pill with the given frontmost-app context.
func (a *app) presentPanel(p showPayload) {
	b, err := json.Marshal(p)
	if err != nil {
		b = []byte("{}")
	}
	showSpotlightPanel(string(b))
}

// reportSpotlightDisarmDismiss tells the main Yansu app to disarm its
// dismiss-on-key flag — the pill either got engaged or hid, so the next
// keystroke must not dismiss/re-show race. Best-effort, fire-and-forget.
//
//isoform:destination local
func reportSpotlightDisarmDismiss() {
	port := spotlightYansuPort
	token := spotlightToken
	if port == 0 {
		return
	}
	go func() {
		req, err := newDisarmDismissRequest(port, token)
		if err != nil {
			return
		}
		client := &http.Client{Timeout: 2 * time.Second}
		if resp, err := client.Do(req); err == nil {
			// Drain before close so the keep-alive connection is reused
			// instead of piling up TIME_WAIT sockets on localhost.
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
	}()
}

// dismissPanel hides the panel.
func (a *app) dismissPanel() { hideSpotlightPanel() }

// quitApp terminates the helper process.
func (a *app) quitApp() { quitSpotlightPanel() }
