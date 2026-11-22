package main

import (
	"encoding/json"
	"net/http"

	"wails-gui/internal/workonit"
)

// The hand-off card in Quick chat (computer-chat v1.0.0 rule 11): work asked
// for there shows the same card as in Chat, and Hand it to opens the Work on
// it sheet over the app you're in. The panel renders Chat's own card and the
// Agents owner's sheet inside its own window, so Isoform's main window never
// comes forward; their bindings reach the app through this one route. Each
// op is the binding Chat's window calls, with the same gates.

type spotlightWorkOnItRequest struct {
	Op string `json:"op"`
	// Input is the sheet's input (prepare).
	Input workonit.Input `json:"input"`
	// TaskID, AgentID and Method pick what goes where (send).
	TaskID  string `json:"taskId"`
	AgentID string `json:"agentId"`
	Method  string `json:"method"`
	// Handle is the person's handle for an agent (handle).
	Handle string `json:"handle"`
	// ItemID and Answer answer a hand-off card (answer_card).
	ItemID string `json:"itemId"`
	Answer string `json:"answer"`
	// Text is what Copy puts on the clipboard (copy).
	Text string `json:"text"`
}

// handleSpotlightWorkOnIt runs one of the card's or the sheet's bindings.
func (hb *hostBridge) handleSpotlightWorkOnIt(w http.ResponseWriter, r *http.Request) {
	var req spotlightWorkOnItRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 96<<10)).Decode(&req); err != nil {
		hb.writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	app := hb.app
	if app == nil {
		hb.writeError(w, http.StatusServiceUnavailable, "app not initialized")
		return
	}
	var result any
	var err error
	switch req.Op {
	case "enabled":
		result = app.GetWorkOnItEnabled()
	case "prepare":
		result, err = app.PrepareWorkOnIt(req.Input)
	case "agents":
		result, err = app.GetWorkOnItAgents()
	case "your_agents":
		result, err = app.GetYourAgents()
	case "send":
		result, err = app.SendWorkOnIt(req.TaskID, req.AgentID, req.Method)
	case "handle":
		err = app.SetWorkOnItHandle(req.AgentID, req.Handle)
	case "answer_card":
		result, err = app.AnswerChatHandoffCard(req.ItemID, req.Answer)
	case "copy":
		err = app.NativeClipboardSetText(req.Text)
	default:
		hb.writeError(w, http.StatusBadRequest, "unknown op")
		return
	}
	if err != nil {
		// The sheet tells Closed Eyes apart by these words.
		hb.writeError(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"result": result})
}
