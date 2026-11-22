package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func postSpotlightWorkOnIt(t *testing.T, hb *hostBridge, body map[string]any) (int, map[string]any) {
	t.Helper()
	blob, _ := json.Marshal(body)
	rec := httptest.NewRecorder()
	hb.handleSpotlightWorkOnIt(rec, httptest.NewRequest(http.MethodPost, "/api/spotlight/work-on-it", bytes.NewReader(blob)))
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

// Work asked for in Quick chat shows the same card in the panel, and the
// card's and sheet's bindings reach Isoform through the panel's route, so
// nothing brings the main window forward (computer-chat v1.0.0 rule 11).
func TestQuickChatHandOffCardReachesThePanelAndAnswers(t *testing.T) {
	app, tag, runner := spotlightMainRig(t)
	if _, err := app.startSpotlightSubmission("Draft the Q4 plan outline", "", "", "req-card"); err != nil {
		t.Fatal(err)
	}
	<-runner.inputs
	waitForTagIdle(t, app, tag)
	events, cancel, err := app.spotlightStream.Subscribe(tag, app.spotlightStream.LatestSequence(tag))
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	tool := app.mainChatHandoffTools(tag, promptOriginMac)[0]
	result, err := tool.Execute(t.Context(), draftCall("toolu_qc", map[string]any{"title": "Draft the Q4 plan outline", "detail": "From Friday's notes."}))
	if err != nil || result.IsError {
		t.Fatalf("hand-off = %v %+v", err, result)
	}
	var cardID string
	select {
	case ev := <-events:
		var item StreamItemData
		if ev.Kind != "item" || json.Unmarshal([]byte(ev.Data), &item) != nil || item.ToolName != chatHandoffCardTool {
			t.Fatalf("event = %+v", ev)
		}
		cardID = item.ID
	case <-time.After(2 * time.Second):
		t.Fatal("the hand-off card did not reach the panel")
	}

	hb := &hostBridge{app: app}
	if code, out := postSpotlightWorkOnIt(t, hb, map[string]any{"op": "enabled"}); code != http.StatusOK {
		t.Fatalf("enabled = %d %v", code, out)
	}
	if code, out := postSpotlightWorkOnIt(t, hb, map[string]any{"op": "answer_card", "itemId": cardID, "answer": chatHandoffDeclined}); code != http.StatusOK || out["result"] != chatHandoffDeclined {
		t.Fatalf("No = %d %v", code, out)
	}
	if code, _ := postSpotlightWorkOnIt(t, hb, map[string]any{"op": "answer_card", "itemId": cardID, "answer": "run"}); code != http.StatusConflict {
		t.Fatalf("an answer other than declined or handed = %d", code)
	}
	if code, _ := postSpotlightWorkOnIt(t, hb, map[string]any{"op": "run_anything"}); code != http.StatusBadRequest {
		t.Fatalf("unknown op = %d", code)
	}
	row, err := app.store.LoadStreamItem(context.Background(), cardID)
	if err != nil || row == nil || !strings.Contains(row.ToolInput, chatHandoffDeclined) {
		t.Fatalf("card row = %+v %v", row, err)
	}
}
