package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/skratchdot/open-golang/open"

	"wails-gui/internal/platform/activitymon"
)

// /api/computer-use/* targets *bundled apps*: the bun apps Isoform
// generates on the user's behalf. They want a small synchronous HTTP surface
// (one click, one type, one scroll) without an agent loop. Backed by
// activitymon, which goes through the same Accessibility APIs the activity
// recorder already uses, so no extra TCC grant is needed. Agent-driven
// computer use belongs to the agents themselves (Claude Code, Codex).

// maxComputerUseBodySize limits request body size for computer-use endpoints (1MB).
const maxComputerUseBodySize = 1 << 20

// computerUseGuard rejects requests when automation is not available (wrong
// platform) or not permitted (Accessibility not granted in System Settings).
func (hb *hostBridge) computerUseGuard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !activitymon.ComputerUseSupported() {
			hb.writeError(w, http.StatusServiceUnavailable,
				"computer use is not available on this platform (macOS only for now)")
			return
		}
		if !activitymon.ComputerUsePermitted() {
			hb.writeError(w, http.StatusForbidden,
				"computer use requires Accessibility permission — grant it in System Settings > Privacy & Security > Accessibility")
			return
		}
		next(w, r)
	}
}

// limitBody wraps a handler to enforce a request body size limit.
func (hb *hostBridge) limitBody(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxComputerUseBodySize)
		next(w, r)
	}
}

// computerUseRoutes lists the /api/computer-use/* endpoints. They need the
// host-bridge token: they type, click and paste with Isoform's
// Accessibility grant.
func (hb *hostBridge) computerUseRoutes() []hostBridgeRoute {
	guard := hb.computerUseGuard
	limit := hb.limitBody
	post := func(path string, h http.HandlerFunc) hostBridgeRoute {
		return hostBridgeRoute{path, hostAccessHelper, http.MethodPost, guard(limit(h))}
	}
	get := func(path string, h http.HandlerFunc) hostBridgeRoute {
		return hostBridgeRoute{path, hostAccessHelper, http.MethodGet, guard(h)}
	}
	return []hostBridgeRoute{
		post("/api/computer-use/open", hb.handleAutoOpen),
		post("/api/computer-use/see", hb.handleAutoSee),
		post("/api/computer-use/click", hb.handleAutoClick),
		post("/api/computer-use/type", hb.handleAutoType),
		post("/api/computer-use/press", hb.handleAutoPress),
		post("/api/computer-use/hotkey", hb.handleAutoHotkey),
		post("/api/computer-use/scroll", hb.handleAutoScroll),
		post("/api/computer-use/drag", hb.handleAutoDrag),
		post("/api/computer-use/paste", hb.handleAutoPaste),
		get("/api/computer-use/apps", hb.handleAutoApps),
		get("/api/computer-use/windows", hb.handleAutoWindows),
	}
}

// ── See: capture accessibility tree ──────────────────────────────────

func (hb *hostBridge) handleAutoSee(w http.ResponseWriter, r *http.Request) {
	var body struct {
		App string `json:"app"` // app name (e.g., "Safari", "Finder")
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		hb.writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if body.App == "" {
		hb.writeError(w, http.StatusBadRequest, "app name required")
		return
	}

	elements := activitymon.CollectAppElements(body.App)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"success": true,
		"data": map[string]any{
			"app":           body.App,
			"element_count": len(elements),
			"ui_elements":   elements,
		},
	})
}

// ── Open: open URL or file ───────────────────────────────────────────

func (hb *hostBridge) handleAutoOpen(w http.ResponseWriter, r *http.Request) {
	var body struct {
		URL  string `json:"url"`
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		hb.writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	if body.URL != "" {
		parsed, err := url.Parse(body.URL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			hb.writeError(w, http.StatusBadRequest, "only http/https URLs allowed")
			return
		}
		if !activitymon.OpenURL(body.URL) {
			hb.writeError(w, http.StatusInternalServerError, "failed to open URL")
			return
		}
		hb.writeOK(w)
		return
	}

	if body.Path != "" {
		if strings.Contains(body.Path, "..") || !strings.HasPrefix(body.Path, "/") {
			hb.writeError(w, http.StatusBadRequest, "invalid path")
			return
		}
		if err := open.Start(body.Path); err != nil {
			hb.writeError(w, http.StatusInternalServerError, fmt.Sprintf("open failed: %v", err))
			return
		}
		hb.writeOK(w)
		return
	}

	hb.writeError(w, http.StatusBadRequest, "url or path required")
}

// ── Click: click at coordinates or element center ────────────────────

func (hb *hostBridge) handleAutoClick(w http.ResponseWriter, r *http.Request) {
	var body struct {
		X   *float64 `json:"x"`
		Y   *float64 `json:"y"`
		App string   `json:"app"` // optional: app name to find element
		ID  string   `json:"id"`  // optional: element ID (e.g., "elem_42")
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		hb.writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	// Direct coordinate click.
	if body.X != nil && body.Y != nil {
		activitymon.ClickAt(*body.X, *body.Y)
		hb.writeOK(w)
		return
	}

	// Click by element ID — find element, click its center.
	if body.App != "" && body.ID != "" {
		elements := activitymon.CollectAppElements(body.App)
		for _, el := range elements {
			if el.ID == body.ID {
				cx := el.X + el.Width/2
				cy := el.Y + el.Height/2
				activitymon.ClickAt(cx, cy)
				hb.writeOK(w)
				return
			}
		}
		hb.writeError(w, http.StatusNotFound, fmt.Sprintf("element %s not found in %s", body.ID, body.App))
		return
	}

	hb.writeError(w, http.StatusBadRequest, "provide (x, y) coordinates or (app, id) to click")
}

// ── Type: type text character by character ────────────────────────────

func (hb *hostBridge) handleAutoType(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		hb.writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if body.Text == "" {
		hb.writeError(w, http.StatusBadRequest, "text required")
		return
	}

	activitymon.TypeText(body.Text)
	hb.writeOK(w)
}

// ── Press: press a named key ─────────────────────────────────────────

func (hb *hostBridge) handleAutoPress(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Key string `json:"key"` // e.g., "return", "tab", "escape", "up"
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		hb.writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if body.Key == "" {
		hb.writeError(w, http.StatusBadRequest, "key required")
		return
	}

	activitymon.PressKey(body.Key)
	hb.writeOK(w)
}

// ── Hotkey: press a key combination ──────────────────────────────────

func (hb *hostBridge) handleAutoHotkey(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Combo string `json:"combo"` // e.g., "cmd,c" or "cmd,shift,a"
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		hb.writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if body.Combo == "" {
		hb.writeError(w, http.StatusBadRequest, "combo required")
		return
	}

	activitymon.Hotkey(body.Combo)
	hb.writeOK(w)
}

// ── Scroll: scroll by delta ──────────────────────────────────────────

func (hb *hostBridge) handleAutoScroll(w http.ResponseWriter, r *http.Request) {
	var body struct {
		DeltaX int `json:"deltaX"`
		DeltaY int `json:"deltaY"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		hb.writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	activitymon.Scroll(body.DeltaX, body.DeltaY)
	hb.writeOK(w)
}

// ── Drag: drag from one point to another ─────────────────────────────

func (hb *hostBridge) handleAutoDrag(w http.ResponseWriter, r *http.Request) {
	var body struct {
		FromX float64 `json:"fromX"`
		FromY float64 `json:"fromY"`
		ToX   float64 `json:"toX"`
		ToY   float64 `json:"toY"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		hb.writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	activitymon.Drag(body.FromX, body.FromY, body.ToX, body.ToY)
	hb.writeOK(w)
}

// ── Paste: atomic paste (set clipboard + Cmd+V + restore) ────────────

func (hb *hostBridge) handleAutoPaste(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		hb.writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if body.Text == "" {
		hb.writeError(w, http.StatusBadRequest, "text required")
		return
	}

	activitymon.PasteText(body.Text)
	hb.writeOK(w)
}

// ── Apps: list running applications ──────────────────────────────────

func (hb *hostBridge) handleAutoApps(w http.ResponseWriter, r *http.Request) {
	apps := activitymon.ListRunningApps()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"success": true,
		"data":    apps,
	})
}

// ── Windows: list windows for an app ─────────────────────────────────

func (hb *hostBridge) handleAutoWindows(w http.ResponseWriter, r *http.Request) {
	app := r.URL.Query().Get("app")
	if app == "" {
		hb.writeError(w, http.StatusBadRequest, "app query parameter required")
		return
	}

	windows := activitymon.ListAppWindows(app)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"success": true,
		"data":    windows,
	})
}
