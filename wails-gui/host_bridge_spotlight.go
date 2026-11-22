package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"wails-gui/internal/persistence"
	"wails-gui/storage"
	"wails-gui/internal/platform/activitymon"
)

// /api/spotlight/* endpoints are the Spotlight helper's only way to
// drive Yansu. They mirror the in-renderer overlay flow
// (frontmost-app → suggestions → submit → SSE stream) but without
// requiring a Wails-bound JS context in the helper, so the standalone
// helper process can produce the same UX as the in-app overlay.
//
// Every route requires the per-session X-Yansu-Spotlight-Token header
// that Yansu hands the helper at spawn time, and the only browser Origin
// allowed is the helper's own webview (spotlightSupervisor.WebviewOrigin).
// With no helper running there is no token and every call is refused, so
// a web page on localhost can't impersonate the helper. guard
// (host_bridge_guard.go) answers the webview's CORS preflights, which by
// spec carry no custom headers, before the token check.

const spotlightTokenHeader = "X-Yansu-Spotlight-Token"

// spotlightRoutes lists the /api/spotlight/* endpoints.
func (hb *hostBridge) spotlightRoutes() []hostBridgeRoute {
	get := func(path string, h http.HandlerFunc) hostBridgeRoute {
		return hostBridgeRoute{path, hostAccessSpotlight, http.MethodGet, h}
	}
	post := func(path string, h http.HandlerFunc) hostBridgeRoute {
		return hostBridgeRoute{path, hostAccessSpotlight, http.MethodPost, h}
	}
	return []hostBridgeRoute{
		get("/api/spotlight/frontmost-app", hb.handleSpotlightFrontmostApp),
		get("/api/spotlight/suggestions", hb.handleSpotlightSuggestions),
		post("/api/spotlight/submit", hb.handleSpotlightSubmit),
		get("/api/spotlight/stream/", hb.handleSpotlightStream),
		get("/api/spotlight/snapshot", hb.handleSpotlightSnapshot),
		post("/api/spotlight/build-deliverable", hb.handleSpotlightBuildDeliverable),
		post("/api/spotlight/dismiss", hb.handleSpotlightDismiss),
		// macOS: the helper hits this whenever the pill leaves the "shown and
		// awaiting first input" state — the user engaged it (clicked in) or it
		// hid (any path). Either way, disarm the dismiss-on-typing tap flag so
		// a stale flag doesn't dismiss/re-show race on the next keystroke.
		post("/api/spotlight/disarm-dismiss-on-key", hb.handleSpotlightDisarmDismiss),
		post("/api/spotlight/expand", hb.handleSpotlightExpand),
		// Ready work first (quick_chat_ready.go).
		post("/api/spotlight/ready", hb.handleSpotlightReady),
		post("/api/spotlight/ready/open", hb.handleSpotlightReadyOpen),
		// The hand-off card and its sheet in the panel (quick_chat_handoff.go).
		post("/api/spotlight/work-on-it", hb.handleSpotlightWorkOnIt),
		get("/api/spotlight/theme", hb.handleSpotlightTheme),
		post("/api/spotlight/permission", hb.handleSpotlightPermission),
		post("/api/spotlight/cancel", hb.handleSpotlightCancel),
		// Voice transcription: spotlight records audio in WebView2 (it
		// can't do sherpa-onnx itself — sherpa-onnx is a cgo lib only the
		// main Yansu binary loads), POSTs base64 WAV here, gets text back.
		// Mirrors `App.TranscribeAudio` but via HTTP so spotlight gets the
		// same offline STT engine as the main composer.
		post("/api/spotlight/voice/transcribe", hb.handleSpotlightVoiceTranscribe),
		// Voice readiness probe — spotlight calls this before recording so
		// the user sees a "loading model" hint if sherpa-onnx isn't ready.
		post("/api/spotlight/voice/init", hb.handleSpotlightVoiceInit),
	}
}

// ── /api/spotlight/frontmost-app ──────────────────────────────────

type spotlightFrontmostAppResponse struct {
	BundleID string `json:"bundleID"`
	AppName  string `json:"appName"`
}

func (hb *hostBridge) handleSpotlightFrontmostApp(w http.ResponseWriter, r *http.Request) {
	resp := spotlightFrontmostAppResponse{}

	// Both platforms have a frontmost-app helper; on Windows it
	// returns just appName + the .exe basename via a lower layer.
	info := activitymon.FrontmostAppInfo()
	resp.BundleID = info.BundleID
	resp.AppName = info.AppName

	// Skip Yansu / spotlight itself so the chip is always about the
	// "other" app the user is looking at.
	if isOwnAppName(resp.AppName) {
		resp.BundleID = ""
		resp.AppName = ""
	}

	writeJSON(w, http.StatusOK, resp)
}

// ── /api/spotlight/suggestions ────────────────────────────────────

type spotlightSuggestionsResponse struct {
	Suggestions []RecommendedQuestion `json:"suggestions"`
}

func (hb *hostBridge) handleSpotlightSuggestions(w http.ResponseWriter, r *http.Request) {
	bundleID := strings.TrimSpace(r.URL.Query().Get("bundleID"))
	lang := strings.TrimSpace(r.URL.Query().Get("lang"))

	app := hb.app
	if app == nil {
		hb.writeError(w, http.StatusServiceUnavailable, "app not initialized")
		return
	}

	suggestions, err := app.GetQuickChatSuggestionsForApp(bundleID, lang)
	if err != nil {
		hb.writeError(w, http.StatusInternalServerError, fmt.Sprintf("get suggestions: %v", err))
		return
	}
	writeJSON(w, http.StatusOK, spotlightSuggestionsResponse{Suggestions: suggestions})
}

// ── /api/spotlight/submit ─────────────────────────────────────────

func (hb *hostBridge) handleSpotlightSubmit(w http.ResponseWriter, r *http.Request) {
	var req spotlightSubmitRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		hb.writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if strings.TrimSpace(req.Prompt) == "" {
		hb.writeError(w, http.StatusBadRequest, "prompt is required")
		return
	}
	if !validSpotlightRequestID(req.RequestID) {
		hb.writeError(w, http.StatusBadRequest, "invalid request ID")
		return
	}
	if (strings.TrimSpace(req.EditItemID) != "") != (req.EditFromSortOrder != nil) {
		hb.writeError(w, http.StatusBadRequest, "edit identity and order are required together")
		return
	}

	app := hb.app
	if app == nil {
		hb.writeError(w, http.StatusServiceUnavailable, "app not initialized")
		return
	}

	response, err := onceSpotlightSubmit(&app.spotlightSubmitReceipts, req, func() (spotlightSubmitResponse, error) {
		if strings.TrimSpace(req.EditItemID) != "" && req.EditFromSortOrder != nil {
			if strings.TrimSpace(req.SessionLocalID) == "" {
				return spotlightSubmitResponse{}, fmt.Errorf("sessionLocalId is required for edit")
			}
			if err := app.editSpotlightSubmission(req.Prompt, req.BundleID, req.SessionLocalID, req.EditItemID, *req.EditFromSortOrder); err != nil {
				return spotlightSubmitResponse{}, fmt.Errorf("edit: %w", err)
			}
			return spotlightSubmitResponse{SessionLocalID: req.SessionLocalID}, nil
		}
		response, err := app.startSpotlightSubmission(req.Prompt, req.BundleID, req.SessionLocalID, req.RequestID)
		if err != nil {
			return spotlightSubmitResponse{}, fmt.Errorf("submit: %w", err)
		}
		return response, nil
	})
	if err != nil {
		hb.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, response)
}

// ── /api/spotlight/stream/:sessionLocalId (SSE) ───────────────────

func (hb *hostBridge) handleSpotlightStream(w http.ResponseWriter, r *http.Request) {
	sessionLocalID := strings.TrimPrefix(r.URL.Path, "/api/spotlight/stream/")
	sessionLocalID = strings.TrimSpace(sessionLocalID)
	if sessionLocalID == "" {
		hb.writeError(w, http.StatusBadRequest, "session id required")
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		hb.writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	after := uint64(0)
	if cursor := r.URL.Query().Get("after"); cursor != "" {
		var err error
		after, err = strconv.ParseUint(cursor, 10, 64)
		if err != nil {
			hb.writeError(w, http.StatusBadRequest, "invalid stream cursor")
			return
		}
	}
	if hb.app == nil {
		hb.writeError(w, http.StatusServiceUnavailable, "app not initialized")
		return
	}
	hb.app.spotlightTagsMu.Lock()
	_, exists := hb.app.spotlightTags[sessionLocalID]
	hb.app.spotlightTagsMu.Unlock()
	if !exists {
		hb.writeError(w, http.StatusNotFound, "spotlight session unavailable")
		return
	}
	events, cancel, err := hb.app.subscribeSpotlightStream(sessionLocalID, after)
	if err != nil {
		hb.writeError(w, http.StatusConflict, err.Error())
		return
	}
	defer cancel()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-store")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	// Heartbeat so proxies don't close the connection.
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			if _, err := w.Write([]byte(": ping\n\n")); err != nil {
				return
			}
			flusher.Flush()
		case ev, ok := <-events:
			if !ok {
				return
			}
			writeSSE(w, flusher, ev.Sequence, ev.Kind, ev.Data)
		}
	}
}

func writeSSE(w http.ResponseWriter, flusher http.Flusher, sequence uint64, event, data string) {
	fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", sequence, event, data)
	flusher.Flush()
}

type spotlightSnapshotResponse struct {
	Items      []StreamItemData `json:"items"`
	Sequence   uint64           `json:"sequence"`
	Streaming  bool             `json:"streaming"`
	RunID      string           `json:"runId,omitempty"`
	SessionID  string           `json:"sessionId,omitempty"`
	Permission *AgentEvent      `json:"permission,omitempty"`
}

// A replay cursor can expire. Capture persisted and live state with the
// stream sequence under the same lock, so later events can be replayed.
func (a *App) spotlightSnapshot(ctx context.Context, tag string, state *spotlightTagState) (spotlightSnapshotResponse, error) {
	state.operationMu.Lock()
	defer state.operationMu.Unlock()
	state.streamMu.Lock()
	defer state.streamMu.Unlock()
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.persistErr != nil {
		return spotlightSnapshotResponse{}, errSpotlightReplayUnavailable
	}
	var rows []storage.StreamItemRow
	var err error
	switch {
	case !state.main:
		rows, err = a.store.LoadStreamItems(ctx, tag)
	case state.fromSortOrder >= 0:
		// A Quick chat exchange in the main chat shows only itself, from
		// its first question on, never the whole thread.
		var page storage.StreamItemPage
		page, err = a.store.LoadStreamItemsAfter(ctx, tag, state.fromSortOrder-1, 500)
		rows = page.Items
	}
	if err != nil {
		return spotlightSnapshotResponse{}, err
	}
	items := make([]StreamItemData, 0, len(rows)+len(state.pending)+2)
	for _, row := range rows {
		items = append(items, persistence.ToStreamItemData(row))
	}
	live := append([]StreamItemData(nil), state.pending...)
	for _, item := range []struct {
		kind  string
		value *spotlightLiveItem
	}{{"assistant", state.currentAssistant}, {"thinking", state.currentThinking}} {
		if item.value != nil && item.value.buf.Len() > 0 {
			live = append(live, StreamItemData{ID: item.value.id, Kind: item.kind, Content: item.value.buf.String(), RunID: item.value.runID, SessionID: item.value.sessionID, SortOrder: item.value.sortOrder})
		}
	}
	sort.SliceStable(live, func(i, j int) bool { return live[i].SortOrder < live[j].SortOrder })
	items = append(items, live...)
	running := state.streaming
	var permission *AgentEvent
	if state.permission != nil {
		copied := *state.permission
		permission = &copied
	}
	return spotlightSnapshotResponse{Items: items, Sequence: a.spotlightStream.LatestSequence(tag), Streaming: running, RunID: state.runID, SessionID: state.agentSessionID, Permission: permission}, nil
}

func (hb *hostBridge) handleSpotlightSnapshot(w http.ResponseWriter, r *http.Request) {
	tag := strings.TrimSpace(r.URL.Query().Get("sessionLocalId"))
	if tag == "" || hb.app == nil || hb.app.store == nil || hb.app.spotlightStream == nil {
		hb.writeError(w, http.StatusBadRequest, "spotlight session unavailable")
		return
	}
	app := hb.app
	app.spotlightTagsMu.Lock()
	state := app.spotlightTags[tag]
	app.spotlightTagsMu.Unlock()
	if state == nil {
		hb.writeError(w, http.StatusNotFound, "spotlight session unavailable")
		return
	}
	snapshot, err := app.spotlightSnapshot(r.Context(), tag, state)
	if err != nil {
		if errors.Is(err, errSpotlightReplayUnavailable) {
			hb.writeError(w, http.StatusConflict, "history could not be saved")
		} else {
			hb.writeError(w, http.StatusInternalServerError, "history unavailable")
		}
		return
	}
	writeJSON(w, http.StatusOK, snapshot)
}

// ── /api/spotlight/build-deliverable ──────────────────────────────

type spotlightBuildDeliverableRequest struct {
	SessionLocalID string `json:"sessionLocalId"`
	DeliverableID  string `json:"deliverableID"`
}

type spotlightBuildDeliverableResponse struct {
	JobID string `json:"jobID"`
}

func (hb *hostBridge) handleSpotlightBuildDeliverable(w http.ResponseWriter, r *http.Request) {
	var req spotlightBuildDeliverableRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		hb.writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if strings.TrimSpace(req.SessionLocalID) == "" {
		hb.writeError(w, http.StatusBadRequest, "sessionLocalId is required")
		return
	}

	app := hb.app
	if app == nil {
		hb.writeError(w, http.StatusServiceUnavailable, "app not initialized")
		return
	}
	jobID, err := app.buildSpotlightDeliverable(req.SessionLocalID, req.DeliverableID)
	if err != nil {
		hb.writeError(w, http.StatusInternalServerError, fmt.Sprintf("build deliverable: %v", err))
		return
	}

	writeJSON(w, http.StatusOK, spotlightBuildDeliverableResponse{JobID: jobID})
}

// ── /api/spotlight/permission ─────────────────────────────────────

type spotlightPermissionRequest struct {
	// SessionID is the agent (Anthropic) session id the runner gave
	// us in run_created — NOT the spotlight tag. RespondToPermission
	// keys its blocked-channel map by this id.
	SessionID      string `json:"sessionID"`
	OptionID       string `json:"optionID"`
	SessionLocalID string `json:"sessionLocalId,omitempty"`
}

func (hb *hostBridge) handleSpotlightPermission(w http.ResponseWriter, r *http.Request) {
	var req spotlightPermissionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		hb.writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if strings.TrimSpace(req.SessionID) == "" || strings.TrimSpace(req.OptionID) == "" {
		hb.writeError(w, http.StatusBadRequest, "sessionID and optionID are required")
		return
	}
	app := hb.app
	if app == nil {
		hb.writeError(w, http.StatusServiceUnavailable, "app not initialized")
		return
	}
	var state *spotlightTagState
	var permissionID string
	if req.SessionLocalID != "" {
		app.spotlightTagsMu.Lock()
		state = app.spotlightTags[req.SessionLocalID]
		app.spotlightTagsMu.Unlock()
		if state != nil {
			state.streamMu.Lock()
			state.mu.Lock()
			if state.permission != nil && state.permission.SessionID == req.SessionID {
				permissionID = state.permission.EventID
			}
			state.mu.Unlock()
			state.streamMu.Unlock()
		}
	}
	if err := app.RespondToPermission(req.SessionID, req.OptionID); err != nil {
		hb.writeError(w, http.StatusInternalServerError, fmt.Sprintf("respond to permission: %v", err))
		return
	}
	if state != nil && permissionID != "" {
		state.streamMu.Lock()
		state.mu.Lock()
		if state.permission != nil && state.permission.EventID == permissionID {
			state.permission = nil
			if app.spotlightStream != nil {
				app.spotlightStream.Publish(req.SessionLocalID, spotlightStreamEvent{Kind: "permission_cleared", Data: jsonEncodeOrEmpty(map[string]string{"eventId": permissionID})})
			}
		}
		state.mu.Unlock()
		state.streamMu.Unlock()
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ── /api/spotlight/cancel ─────────────────────────────────────────

type spotlightCancelRequest struct {
	// SessionLocalID is the spotlight tag (e.g. "spotlight_<nanos>")
	// — same id we returned from /submit. CancelByTag is keyed on it.
	SessionLocalID string `json:"sessionLocalId"`
}

func (hb *hostBridge) handleSpotlightCancel(w http.ResponseWriter, r *http.Request) {
	var req spotlightCancelRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		hb.writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if strings.TrimSpace(req.SessionLocalID) == "" {
		hb.writeError(w, http.StatusBadRequest, "sessionLocalId is required")
		return
	}
	app := hb.app
	if app == nil {
		hb.writeError(w, http.StatusServiceUnavailable, "app not initialized")
		return
	}
	app.CancelByTag(req.SessionLocalID)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ── /api/spotlight/dismiss ────────────────────────────────────────

func (hb *hostBridge) handleSpotlightDismiss(w http.ResponseWriter, r *http.Request) {
	app := hb.app
	if app != nil {
		app.dismissSpotlight()
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleSpotlightDisarmDismiss is called by the macOS helper whenever the
// pill leaves the "shown and awaiting first input" state (engaged or hidden,
// any path). It disarms the dismiss-on-typing tap flag so a stale flag can't
// dismiss/re-show race on the next keystroke (incl. the hotkey). No-op off
// macOS.
func (hb *hostBridge) handleSpotlightDisarmDismiss(w http.ResponseWriter, r *http.Request) {
	setSpotlightDismissOnKey(false)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ── /api/spotlight/theme ──────────────────────────────────────────

type spotlightThemeResponse struct {
	Theme    string `json:"theme"`
	Language string `json:"language"`
}

// handleSpotlightTheme returns the user's currently-selected theme
// ("paper" or "onedark") so the spotlight helper can apply the
// matching palette. It also carries the actual UI language, mirrored by
// the main renderer into ui.language, because the helper has a separate
// WebView origin and cannot read the main renderer's localStorage.
// Never errors: defaults are the light "paper" theme and English.
func (hb *hostBridge) handleSpotlightTheme(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, spotlightThemeResponse{
		Theme:    currentThemePreference(),
		Language: hb.app.currentUILanguage(),
	})
}

// ── /api/spotlight/expand ─────────────────────────────────────────

type spotlightExpandRequest struct {
	// SessionLocalID is the spotlight tag that identifies the in-flight
	// chat. Main UI's standalone chat project keys sessions by this id,
	// so the React side can re-open the conversation directly.
	SessionLocalID string `json:"sessionLocalId"`
}

// handleSpotlightExpand handles the spotlight's "maximize" button:
// surfaces the main Yansu window and signals the React side to switch
// to the standalone-chat project + activate the spotlight session.
// The spotlight helper hides its own window after this returns.
func (hb *hostBridge) handleSpotlightExpand(w http.ResponseWriter, r *http.Request) {
	var req spotlightExpandRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		hb.writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	app := hb.app
	if app == nil {
		hb.writeError(w, http.StatusServiceUnavailable, "app not initialized")
		return
	}
	app.expandSpotlightToMain(strings.TrimSpace(req.SessionLocalID))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ── /api/spotlight/voice/* ────────────────────────────────────────

type spotlightVoiceTranscribeRequest struct {
	WavBase64 string `json:"wavBase64"`
}

type spotlightVoiceTranscribeResponse struct {
	Text string `json:"text"`
}

// handleSpotlightVoiceTranscribe wraps App.TranscribeAudio so the
// spotlight WebView2 (which has no sherpa-onnx cgo) can use the same
// offline STT engine the main composer uses. The spotlight records
// audio into a WAV blob, base64-encodes it, POSTs here, and gets the
// transcript back.
func (hb *hostBridge) handleSpotlightVoiceTranscribe(w http.ResponseWriter, r *http.Request) {
	var req spotlightVoiceTranscribeRequest
	// Audio payloads can be a few MB — relax the default body limit
	// so large WAV blobs aren't truncated.
	r.Body = http.MaxBytesReader(w, r.Body, 32<<20) // 32 MB
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		hb.writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if strings.TrimSpace(req.WavBase64) == "" {
		hb.writeError(w, http.StatusBadRequest, "wavBase64 is required")
		return
	}
	app := hb.app
	if app == nil {
		hb.writeError(w, http.StatusServiceUnavailable, "app not initialized")
		return
	}
	text, err := app.TranscribeAudio(req.WavBase64)
	if err != nil {
		hb.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, spotlightVoiceTranscribeResponse{Text: text})
}

// handleSpotlightVoiceInit forwards to App.InitVoiceInput so spotlight
// can warm up the model before recording. Returns ok=true on success;
// errors propagate as 500.
func (hb *hostBridge) handleSpotlightVoiceInit(w http.ResponseWriter, r *http.Request) {
	app := hb.app
	if app == nil {
		hb.writeError(w, http.StatusServiceUnavailable, "app not initialized")
		return
	}
	if err := app.InitVoiceInput(); err != nil {
		hb.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// isOwnAppName returns true when the foreground app appears to be
// Yansu itself (we don't want to chip-suggest about ourselves).
func isOwnAppName(appName string) bool {
	a := strings.ToLower(strings.TrimSpace(appName))
	return a == "isoform" || a == "yansu" || a == "yansu spotlight" || a == "wails-gui" ||
		a == "wails-gui.exe" || a == "quick-chat-spotlight" ||
		a == "quick-chat-spotlight.exe"
}
