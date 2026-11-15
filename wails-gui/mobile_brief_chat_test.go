package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"wails-gui/storage"
	"yansu-agent/agent"
)

func mobileBriefChatFixture(t *testing.T) (*mobileBridge, string, MobileBriefV1, *upcomingCaptureRunner) {
	t.Helper()
	app := newTodayContextTestApp(t, true)
	main, err := app.ensureMainChat(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	runner := &upcomingCaptureRunner{inputs: make(chan *agent.TaskInput, 2)}
	app.runner = runner
	app.sessionTempDirs = map[string]string{}
	app.streams = map[string]context.CancelFunc{}
	app.runIDByTag = map[string]string{}
	app.pendingTraceIDs = map[string]string{}
	app.eventBroadcaster = NewEventBroadcaster()
	brief := boundMemoryConversationFixture(t, app)
	bindConversationBriefFixture(t, app, &brief)
	dir, _ := mobileBriefStateDir()
	if err := newMobileBriefStore(dir).save(brief, time.Now()); err != nil {
		t.Fatal(err)
	}
	bridge, err := newMobileBridge(app, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(bridge.Stop)
	return bridge, main.LocalID, app.mobileBriefForDelivery(brief), runner
}

func mobileBriefChatBody(t *testing.T, input mobileBriefChatInput) string {
	t.Helper()
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func mobileBriefChatDevice() mobileDeviceRecord {
	return mobileTestDevice(mobileScopeChatWrite, mobileScopeBriefRead)
}

func TestMobileBriefChatRequiresBothScopes(t *testing.T) {
	bridge := &mobileBridge{}
	for _, scopes := range [][]string{nil, {mobileScopeChatWrite}, {mobileScopeBriefRead}} {
		reply := mobileTestChatCall(bridge, mobileTestDevice(scopes...), http.MethodPost, "/chats/main/brief-messages", `{}`)
		if reply.Code != http.StatusForbidden {
			t.Fatalf("scopes %v: %d %s", scopes, reply.Code, reply.Body.String())
		}
	}
}

func TestMobileBriefParagraphAskUsesVerifiedContextAndChatTurn(t *testing.T) {
	bridge, chatID, brief, runner := mobileBriefChatFixture(t)
	index := 0
	input := mobileBriefChatInput{IdempotencyKey: "ask", Text: "What should I do next?", BriefID: brief.BriefID,
		BriefVersion: brief.ConversationKey, Ask: true, Paragraph: &index, ParagraphText: brief.Narrative[index].Text}
	path := "/chats/" + chatID + "/brief-messages"
	bad := input
	bad.IdempotencyKey, bad.ParagraphText = "bad", "Invented paragraph"
	if got := mobileTestChatCall(bridge, mobileBriefChatDevice(), http.MethodPost, path, mobileBriefChatBody(t, bad)); got.Code != http.StatusConflict {
		t.Fatalf("altered quote accepted: %d %s", got.Code, got.Body.String())
	}
	first := mobileTestChatCall(bridge, mobileBriefChatDevice(), http.MethodPost, path, mobileBriefChatBody(t, input))
	if first.Code != http.StatusAccepted {
		t.Fatalf("ask: %d %s", first.Code, first.Body.String())
	}
	var accepted struct {
		TurnID string `json:"turnId"`
		Seq    int    `json:"seq"`
	}
	if err := json.Unmarshal(mobileTestDecodeReply(t, first).Data, &accepted); err != nil || accepted.Seq < 1 {
		t.Fatalf("invalid acceptance: %+v %v", accepted, err)
	}
	select {
	case task := <-runner.inputs:
		prompt, _ := task.PromptFunc()
		if !task.NoTools || task.ProgrammaticToolCalling || len(task.ExtraTools) != 0 || !strings.Contains(prompt, "The selected paragraph is: "+input.ParagraphText) {
			t.Fatalf("ask did not use read-only paragraph context: noTools=%v programmatic=%v extra=%d", task.NoTools, task.ProgrammaticToolCalling, len(task.ExtraTools))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Ask did not start a chat turn")
	}
	waitForTagIdle(t, bridge.app, chatID)
	bridge.chat.mu.Lock()
	live := &mobileChatReply{turnID: accepted.TurnID, userOrder: accepted.Seq - 1, ask: true}
	live.text.WriteString("A partial answer")
	bridge.chat.replies[chatID] = live
	bridge.chat.mu.Unlock()
	reader := mobileTestDevice(mobileScopeChatRead, mobileScopeBriefRead)
	page := mobileTestChatCall(bridge, reader, http.MethodGet, "/chats/"+chatID, "")
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "A partial answer") {
		t.Fatalf("live answer missing: %d %s", page.Code, page.Body.String())
	}
	withoutBrief := mobileTestChatCall(bridge, mobileTestDevice(mobileScopeChatRead), http.MethodGet, "/chats/"+chatID, "")
	if withoutBrief.Code != http.StatusOK || strings.Contains(withoutBrief.Body.String(), "A partial answer") {
		t.Fatalf("live brief answer without brief:read: %d %s", withoutBrief.Code, withoutBrief.Body.String())
	}
	bridge.app.closedEyes.enabled.Store(true)
	page = mobileTestChatCall(bridge, reader, http.MethodGet, "/chats/"+chatID, "")
	if strings.Contains(page.Body.String(), "A partial answer") {
		t.Fatal("live answer survived withdrawn source access")
	}
	bridge.app.closedEyes.enabled.Store(false)
	retry := mobileTestChatCall(bridge, mobileBriefChatDevice(), http.MethodPost, path, mobileBriefChatBody(t, input))
	if retry.Code != first.Code || retry.Body.String() != first.Body.String() {
		t.Fatalf("Ask replay: %d %s", retry.Code, retry.Body.String())
	}
	status := mobileTestChatCall(bridge, mobileBriefChatDevice(), http.MethodGet, "/status", "")
	if !strings.Contains(status.Body.String(), `"briefAsk":true`) {
		t.Fatalf("capability missing: %s", status.Body.String())
	}
}

func TestMobileBriefAskStopTargetsOnlyItsTurn(t *testing.T) {
	bridge, chatID, _, _ := mobileBriefChatFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bridge.chat.mu.Lock()
	bridge.chat.replies[chatID] = &mobileChatReply{turnID: "ask-turn", ask: true, cancel: cancel}
	bridge.chat.mu.Unlock()
	path := "/chats/" + chatID + "/cancel"
	device := mobileTestDevice(mobileScopeChatWrite)
	wrong := mobileTestChatCall(bridge, device, http.MethodPost, path, `{"idempotencyKey":"wrong","turnId":"another-turn"}`)
	if wrong.Code != http.StatusOK || !strings.Contains(wrong.Body.String(), `"cancelled":false`) || ctx.Err() != nil {
		t.Fatalf("wrong turn stopped work: %d %s", wrong.Code, wrong.Body.String())
	}
	body := `{"idempotencyKey":"stop","turnId":"ask-turn"}`
	first := mobileTestChatCall(bridge, device, http.MethodPost, path, body)
	if first.Code != http.StatusOK || !strings.Contains(first.Body.String(), `"cancelled":true`) || ctx.Err() == nil {
		t.Fatalf("Ask did not stop: %d %s", first.Code, first.Body.String())
	}
	next, nextCancel := context.WithCancel(context.Background())
	defer nextCancel()
	bridge.chat.mu.Lock()
	bridge.chat.replies[chatID] = &mobileChatReply{turnID: "next-turn", ask: true, cancel: nextCancel}
	bridge.chat.mu.Unlock()
	retry := mobileTestChatCall(bridge, device, http.MethodPost, path, body)
	if retry.Body.String() != first.Body.String() || next.Err() != nil {
		t.Fatalf("stop replay affected later work: %s", retry.Body.String())
	}
}

func TestMobileBriefParagraphSavedAnswerRechecksSourceAndScopes(t *testing.T) {
	bridge, chatID, brief, _ := mobileBriefChatFixture(t)
	index := 0
	input := mobileBriefChatInput{BriefID: brief.BriefID, BriefVersion: brief.ConversationKey,
		Ask: true, Paragraph: &index, ParagraphText: brief.Narrative[index].Text}
	ref, _, err := bridge.app.mobileBriefChatReference(input, "What happened?")
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = bridge.app.admitTodayChatTurn(t.Context(), chatID,
		storage.StreamItemRow{ID: "saved-ask-user", Kind: "user", Content: "What happened?"}, ref, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := bridge.app.store.BindAnswerChipUserRun(t.Context(), chatID, "saved-ask-user", "saved-run"); err != nil {
		t.Fatal(err)
	}
	if _, err := bridge.app.store.AppendStreamItem(t.Context(), chatID, storage.StreamItemRow{ID: "saved-ask-answer", Kind: "assistant", RunID: "saved-run", Content: "Here is the answer."}); err != nil {
		t.Fatal(err)
	}
	bridge.app.savePanelAskAnswer(chatID, "saved-ask-user", "saved-run", "saved-ask-answer", "Here is the answer.")
	query := url.Values{"version": {brief.ConversationKey}, "paragraph": {"0"}, "text": {input.ParagraphText}}
	path := "/briefs/" + brief.BriefID + "/ask-answer?" + query.Encode()
	for _, scopes := range [][]string{{mobileScopeChatRead}, {mobileScopeBriefRead}} {
		got := mobileTestChatCall(bridge, mobileTestDevice(scopes...), http.MethodGet, path, "")
		if got.Code != http.StatusForbidden {
			t.Fatalf("scopes %v: %d", scopes, got.Code)
		}
	}
	device := mobileTestDevice(mobileScopeChatRead, mobileScopeBriefRead)
	got := mobileTestChatCall(bridge, device, http.MethodGet, path, "")
	if got.Code != http.StatusOK || !strings.Contains(got.Body.String(), "Here is the answer.") {
		t.Fatalf("saved answer: %d %s", got.Code, got.Body.String())
	}
	bridge.app.closedEyes.enabled.Store(true)
	got = mobileTestChatCall(bridge, device, http.MethodGet, path, "")
	if got.Code != http.StatusConflict || strings.Contains(got.Body.String(), "Here is the answer.") {
		t.Fatalf("withdrawn answer: %d %s", got.Code, got.Body.String())
	}
}

func TestMobileBriefCardAskRunsAndReopensItsVerifiedAnswer(t *testing.T) {
	bridge, chatID, brief, runner := mobileBriefChatFixture(t)
	var card MobileBriefCard
	for _, candidate := range bridge.app.mobileBriefWithCards(brief).Cards {
		if candidate.Mode == mobileBriefCardSend {
			card = candidate
			break
		}
	}
	if card.ID == "" {
		t.Fatal("fixture has no send card")
	}
	input := mobileBriefChatInput{IdempotencyKey: "ask-card", BriefID: brief.BriefID, BriefVersion: brief.ConversationKey,
		CardID: card.ID, CardMessage: card.Message, Text: card.Message, Ask: true}
	path := "/chats/" + chatID + "/brief-messages"
	got := mobileTestChatCall(bridge, mobileBriefChatDevice(), http.MethodPost, path, mobileBriefChatBody(t, input))
	if got.Code != http.StatusAccepted {
		t.Fatalf("card Ask: %d %s", got.Code, got.Body.String())
	}
	select {
	case task := <-runner.inputs:
		prompt, _ := task.PromptFunc()
		if !task.NoTools || !strings.Contains(prompt, "<today-item-context>") || !strings.HasSuffix(prompt, card.Message) {
			t.Fatal("card Ask did not use its verified context as a read-only chat turn")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("card Ask did not start")
	}
	waitForTagIdle(t, bridge.app, chatID)
	rows, err := bridge.app.store.LoadStreamItems(t.Context(), chatID)
	if err != nil {
		t.Fatal(err)
	}
	var user storage.StreamItemRow
	for _, row := range rows {
		if row.Kind == "user" {
			user = row
		}
	}
	if user.ID == "" || user.Content != card.Message {
		t.Fatalf("missing posted card question: %+v", user)
	}
	if err := bridge.app.store.BindAnswerChipUserRun(t.Context(), chatID, user.ID, "card-answer-run"); err != nil {
		t.Fatal(err)
	}
	if _, err := bridge.app.store.AppendStreamItem(t.Context(), chatID, storage.StreamItemRow{ID: "card-ask-answer", Kind: "assistant", RunID: "card-answer-run", Content: "Card answer."}); err != nil {
		t.Fatal(err)
	}
	bridge.app.savePanelAskAnswer(chatID, user.ID, "card-answer-run", "card-ask-answer", "Card answer.")
	query := url.Values{"version": {brief.ConversationKey}, "cardId": {card.ID}, "cardMessage": {card.Message}}
	answerPath := "/briefs/" + brief.BriefID + "/ask-answer?" + query.Encode()
	reader := mobileTestDevice(mobileScopeChatRead, mobileScopeBriefRead)
	got = mobileTestChatCall(bridge, reader, http.MethodGet, answerPath, "")
	if got.Code != http.StatusOK || !strings.Contains(got.Body.String(), "Card answer.") {
		t.Fatalf("saved card answer: %d %s", got.Code, got.Body.String())
	}
	query.Set("version", strings.Repeat("0", 64))
	got = mobileTestChatCall(bridge, reader, http.MethodGet, "/briefs/"+brief.BriefID+"/ask-answer?"+query.Encode(), "")
	if got.Code != http.StatusConflict || strings.Contains(got.Body.String(), "Card answer.") {
		t.Fatalf("stale card answer: %d %s", got.Code, got.Body.String())
	}
}

func TestMobileBriefOpenerAnswerIsAReadOnlyAsk(t *testing.T) {
	bridge, chatID, brief, runner := mobileBriefChatFixture(t)
	var answer MobileBriefCard
	for _, card := range bridge.app.mobileBriefWithCards(brief).Cards {
		if card.Kind == mobileBriefCardOpenerAnswer {
			answer = card
			break
		}
	}
	if answer.ID == "" {
		t.Fatal("fixture has no opener answer")
	}
	input := mobileBriefChatInput{IdempotencyKey: "opener-answer", BriefID: brief.BriefID, BriefVersion: brief.ConversationKey,
		CardID: answer.ID, CardMessage: answer.Message, Text: answer.Message, Ask: true}
	reply := mobileTestChatCall(bridge, mobileBriefChatDevice(), http.MethodPost, "/chats/"+chatID+"/brief-messages", mobileBriefChatBody(t, input))
	if reply.Code != http.StatusAccepted {
		t.Fatalf("opener answer: %d %s", reply.Code, reply.Body.String())
	}
	select {
	case task := <-runner.inputs:
		prompt, _ := task.PromptFunc()
		if !task.NoTools || !strings.Contains(prompt, "<today-item-context>") || !strings.HasSuffix(prompt, answer.Message) {
			t.Fatal("opener answer did not retain its read-only source context")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("opener answer did not start")
	}
	waitForTagIdle(t, bridge.app, chatID)
}

func TestMobileBriefChatUsesTodayContextAndPhoneAuthority(t *testing.T) {
	for _, withOffer := range []bool{false, true} {
		t.Run(map[bool]string{false: "edited opener", true: "offer"}[withOffer], func(t *testing.T) {
			bridge, chatID, brief, runner := mobileBriefChatFixture(t)
			input := mobileBriefChatInput{IdempotencyKey: "send", Text: "About this morning's brief: Focus on Friday.", BriefID: brief.BriefID, BriefVersion: brief.ConversationKey}
			sent := input.Text
			if withOffer {
				if len(brief.Offers) == 0 {
					t.Fatal("fixture needs a final-paragraph offer")
				}
				// An offer tap is the offer's card: the computer sends the
				// card's words, never what the phone sent with it.
				input.OfferID, input.Text = brief.Offers[0].ID, "Words the phone wrote for this offer."
				sent = brief.Offers[0].Prompt
			}
			path := "/chats/" + chatID + "/brief-messages"
			reply := mobileTestChatCall(bridge, mobileBriefChatDevice(), http.MethodPost, path, mobileBriefChatBody(t, input))
			if reply.Code != http.StatusAccepted {
				t.Fatalf("send: %d %s", reply.Code, reply.Body.String())
			}
			var accepted struct {
				TurnID string `json:"turnId"`
				Seq    int64  `json:"seq"`
			}
			if err := json.Unmarshal(mobileTestDecodeReply(t, reply).Data, &accepted); err != nil || accepted.TurnID == "" || accepted.Seq < 1 {
				t.Fatalf("acceptance: %+v %v", accepted, err)
			}
			select {
			case task := <-runner.inputs:
				for i := 0; i < 2; i++ {
					prompt, _ := task.PromptFunc()
					// The opener's turn carries the whole brief; an offer's turn
					// carries its card's subject.
					if strings.Count(prompt, "<today-item-context>") != 1 || (!withOffer && !strings.Contains(prompt, brief.Narrative[0].Text)) || !strings.Contains(prompt, `"id":"launch"`) || !strings.Contains(prompt, "Prepare the release") || !strings.HasSuffix(prompt, sent) {
						t.Fatal("brief/evidence missing, repeated, or not followed by the edited prompt")
					}
					if !strings.Contains(prompt, "can be changed only on the computer") {
						t.Fatal("phone origin instructions missing")
					}
				}
				// GoalCreate and the read-only SearchWeb (#925), nothing that
				// changes the computer.
				if len(task.ExtraTools) != 2 || task.ExtraTools[0].Name() != goalToolCreate || task.ExtraTools[1].Name() != chatWebSearchToolName {
					t.Fatal("brief context granted computer-only tools")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("runner did not receive the brief turn")
			}
			waitForTagIdle(t, bridge.app, chatID)
			rows, err := bridge.app.store.LoadStreamItems(t.Context(), chatID)
			if err != nil {
				t.Fatal(err)
			}
			users := 0
			for _, row := range rows {
				if row.Kind == "user" {
					users++
					if row.Content != sent || strings.Contains(row.Content, "<today-item-context>") {
						t.Fatalf("hidden context entered visible bubble: %q", row.Content)
					}
				}
			}
			if users != 1 {
				t.Fatalf("user rows = %d", users)
			}
			// Context metadata must not suppress the hub's reply when no
			// computer window is open to store assistant rows.
			bridge.storeChatReplyIfMissing(chatID, &mobileChatReply{turnID: accepted.TurnID, userOrder: int(accepted.Seq - 1), rows: []storage.StreamItemRow{{ID: "phone-reply", Kind: "assistant", Content: "Let's plan Friday."}}})
			rows, err = bridge.app.store.LoadStreamItems(t.Context(), chatID)
			if err != nil || rows[len(rows)-1].ID != "phone-reply" {
				t.Fatalf("context status suppressed phone reply: %+v %v", rows, err)
			}
			// A successful receipt is returned even after its brief changes;
			// the retry must not run a second turn or append a second user row.
			bridge.app.closedEyes.enabled.Store(true)
			retry := mobileTestChatCall(bridge, mobileBriefChatDevice(), http.MethodPost, path, mobileBriefChatBody(t, input))
			if retry.Code != reply.Code || retry.Body.String() != reply.Body.String() {
				t.Fatalf("accepted retry changed: %d %s", retry.Code, retry.Body.String())
			}
			select {
			case <-runner.inputs:
				t.Fatal("accepted retry dispatched again")
			default:
			}
		})
	}
}

func TestMobileBriefChatRejectsStaleAndPrivateSources(t *testing.T) {
	for _, change := range []string{"stale offer", "memory_read_provenance", "delivery filter", "Closed Eyes", "unknown offer", "other chat", "busy"} {
		t.Run(change, func(t *testing.T) {
			bridge, chatID, brief, runner := mobileBriefChatFixture(t)
			input := mobileBriefChatInput{IdempotencyKey: "reject", Text: "About this morning's brief: Help with the plan.", BriefID: brief.BriefID, BriefVersion: brief.ConversationKey}
			want, message := http.StatusConflict, mobileBriefChangedMessage
			switch change {
			case "stale offer":
				// A button the phone showed for an older revision may now do
				// something else.
				input.OfferID, input.BriefVersion = brief.Offers[0].ID, strings.Repeat("0", 64)
			case "memory_read_provenance":
				writeTodayContextMemory(t, "launch", renderOKFDocument(map[string]any{"source": "user_edit", "read_provenance": memoryReadProvenanceJSON(memoryReadProvenance{Version: 2, ReadBlocked: true})}, "# Launch plan\nPrepare the release."))
			case "delivery filter":
				dir, _ := memoryDir()
				if err := os.Remove(filepath.Join(dir, "launch.md")); err != nil {
					t.Fatal(err)
				}
			case "Closed Eyes":
				bridge.app.closedEyes.enabled.Store(true)
			case "unknown offer":
				input.OfferID = "offer-from-another-brief"
			case "other chat":
				main, err := bridge.app.store.LoadSession(t.Context(), chatID)
				if err != nil {
					t.Fatal(err)
				}
				chatID = "side-chat"
				if err := bridge.app.store.SaveSession(t.Context(), storage.SessionRow{LocalID: chatID, ProjectID: main.ProjectID, WorkspaceID: main.WorkspaceID, Kind: storage.SessionKindSide}); err != nil {
					t.Fatal(err)
				}
				want, message = http.StatusNotFound, ""
			case "busy":
				bridge.app.activeTags = map[string]bool{chatID: true}
				want, message = http.StatusConflict, "busy"
			}
			if change == "memory_read_provenance" || change == "delivery filter" || change == "Closed Eyes" {
				if delivered := bridge.app.mobileBriefForDelivery(brief); len(delivered.Narrative) != 0 || len(delivered.Offers) != 0 {
					t.Fatal("fixture did not withdraw the readable brief")
				}
			}
			reply := mobileTestChatCall(bridge, mobileBriefChatDevice(), http.MethodPost, "/chats/"+chatID+"/brief-messages", mobileBriefChatBody(t, input))
			if reply.Code != want || (message != "" && mobileTestDecodeReply(t, reply).Error != message) {
				t.Fatalf("rejected request: %d %s", reply.Code, reply.Body.String())
			}
			rows, err := bridge.app.store.LoadStreamItems(t.Context(), chatID)
			if err != nil || len(rows) != 0 {
				t.Fatalf("rejected request left history: %+v %v", rows, err)
			}
			select {
			case <-runner.inputs:
				t.Fatal("rejected request reached runner")
			default:
			}
		})
	}
}

// A chip question sent after its brief moved on (Bo's Oct 28 and 29 409s) is
// the person's own question: the computer answers it as a plain turn in the
// same chat, with none of the old brief's context, instead of refusing it.
func TestMobileBriefChatStaleBriefAnswersThePlainQuestion(t *testing.T) {
	for _, change := range []string{"version", "replaced brief", "card from an older revision"} {
		t.Run(change, func(t *testing.T) {
			bridge, chatID, brief, runner := mobileBriefChatFixture(t)
			text := "Where do I stand on “Launch plan”, due today, and what's next?"
			input := mobileBriefChatInput{IdempotencyKey: "stale", Text: text, BriefID: brief.BriefID, BriefVersion: brief.ConversationKey}
			switch change {
			case "version":
				input.BriefVersion = strings.Repeat("0", 64)
			case "replaced brief":
				brief.Narrative[0].Text += " Updated plan."
				dir, _ := mobileBriefStateDir()
				if err := newMobileBriefStore(dir).save(brief, time.Now()); err != nil {
					t.Fatal(err)
				}
			case "card from an older revision":
				input.BriefVersion = strings.Repeat("0", 64)
				input.CardID, input.CardMessage = "card-from-an-older-revision", text
			}
			reply := mobileTestChatCall(bridge, mobileBriefChatDevice(), http.MethodPost, "/chats/"+chatID+"/brief-messages", mobileBriefChatBody(t, input))
			if reply.Code != http.StatusAccepted {
				t.Fatalf("stale chip question: %d %s", reply.Code, reply.Body.String())
			}
			select {
			case task := <-runner.inputs:
				prompt, _ := task.PromptFunc()
				if !strings.HasSuffix(prompt, text) || strings.Contains(prompt, "<today-item-context>") {
					t.Fatalf("the stale question did not go as a plain turn: %q", prompt)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("runner did not receive the plain turn")
			}
			waitForTagIdle(t, bridge.app, chatID)
			rows, err := bridge.app.store.LoadStreamItems(t.Context(), chatID)
			if err != nil {
				t.Fatal(err)
			}
			users := 0
			for _, row := range rows {
				if row.Kind == "user" {
					users++
					if row.Content != text {
						t.Fatalf("visible bubble = %q", row.Content)
					}
				}
			}
			if users != 1 {
				t.Fatalf("user rows = %d", users)
			}
			// The retry under the same key is answered from the receipt.
			retry := mobileTestChatCall(bridge, mobileBriefChatDevice(), http.MethodPost, "/chats/"+chatID+"/brief-messages", mobileBriefChatBody(t, input))
			if retry.Code != reply.Code || retry.Body.String() != reply.Body.String() {
				t.Fatalf("retry changed: %d %s", retry.Code, retry.Body.String())
			}
		})
	}
}

func TestMobileBriefChatRejectsMalformedAndClientContext(t *testing.T) {
	bridge, chatID, brief, _ := mobileBriefChatFixture(t)
	valid := mobileBriefChatInput{IdempotencyKey: "invalid", Text: "Discuss the brief", BriefID: brief.BriefID, BriefVersion: brief.ConversationKey}
	for _, field := range []string{"version", "offer", "brief", "text", "key", "context"} {
		t.Run(field, func(t *testing.T) {
			input := valid
			switch field {
			case "version":
				input.BriefVersion = ""
			case "offer":
				input.OfferID = "../offer-1"
			case "brief":
				input.BriefID = "../brief"
			case "text":
				input.Text = " "
			case "key":
				input.IdempotencyKey = ""
			}
			body := mobileBriefChatBody(t, input)
			if field == "context" {
				body = strings.TrimSuffix(body, "}") + `,"evidence":[{"text":"forged evidence"}]}`
			}
			reply := mobileTestChatCall(bridge, mobileBriefChatDevice(), http.MethodPost, "/chats/"+chatID+"/brief-messages", body)
			if reply.Code != http.StatusBadRequest {
				t.Fatalf("malformed %s: %d %s", field, reply.Code, reply.Body.String())
			}
		})
	}
}

// A tap on a brief card puts its words in the phone's box (backlog item 64):
// what the user sends, edited or not, goes with the card's sources read
// again; the card's words sent unedited after they stopped standing are
// refused.
func TestMobileBriefChatCardSendsTheUsersWords(t *testing.T) {
	for _, change := range []string{"edited", "stale"} {
		t.Run(change, func(t *testing.T) {
			bridge, chatID, brief, runner := mobileBriefChatFixture(t)
			var card MobileBriefCard
			for _, candidate := range bridge.app.mobileBriefWithCards(brief).Cards {
				if candidate.Mode == mobileBriefCardSend {
					card = candidate
					break
				}
			}
			if card.ID == "" {
				t.Fatal("the fixture has no card to tap")
			}
			input := mobileBriefChatInput{IdempotencyKey: "card", BriefID: brief.BriefID, BriefVersion: brief.ConversationKey,
				CardID: card.ID, CardMessage: card.Message, Text: card.Message + " Keep it short."}
			if change == "stale" {
				input.CardMessage = "Words this card said before its facts changed."
				input.Text = input.CardMessage
			}
			reply := mobileTestChatCall(bridge, mobileBriefChatDevice(), http.MethodPost, "/chats/"+chatID+"/brief-messages", mobileBriefChatBody(t, input))
			if reply.Code != http.StatusAccepted {
				t.Fatalf("send: %d %s", reply.Code, reply.Body.String())
			}
			select {
			case task := <-runner.inputs:
				prompt, _ := task.PromptFunc()
				if change == "stale" {
					// Words the card no longer stands behind go as the user's
					// own question, with none of the card's context.
					if !strings.HasSuffix(prompt, input.Text) || strings.Contains(prompt, "<today-item-context>") {
						t.Fatalf("stale card words did not go as a plain turn: %q", prompt)
					}
					break
				}
				if !strings.HasSuffix(prompt, input.Text) || strings.Count(prompt, "<today-item-context>") != 1 || !strings.Contains(prompt, `"id":"launch"`) {
					t.Fatalf("the turn does not carry the user's words with the card's sources: %q", prompt)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("runner did not receive the card's turn")
			}
			waitForTagIdle(t, bridge.app, chatID)
			rows, err := bridge.app.store.LoadStreamItems(t.Context(), chatID)
			if err != nil {
				t.Fatal(err)
			}
			for _, row := range rows {
				if row.Kind == "user" && row.Content != input.Text {
					t.Fatalf("visible bubble = %q", row.Content)
				}
			}
		})
	}
}

// A card tap carries the words the phone showed: without them the request is
// refused, and the user's words name the item as it reads now.
func TestMobileBriefChatCardChecksTheWordsItSends(t *testing.T) {
	bridge, chatID, brief, _ := mobileBriefChatFixture(t)
	var card MobileBriefCard
	for _, candidate := range bridge.app.mobileBriefWithCards(brief).Cards {
		if candidate.Mode == mobileBriefCardSend && strings.Contains(candidate.Message, "Launch plan") {
			card = candidate
			break
		}
	}
	if card.ID == "" {
		t.Fatal("the fixture has no card naming the launch plan")
	}
	input := mobileBriefChatInput{IdempotencyKey: "card", BriefID: brief.BriefID, BriefVersion: brief.ConversationKey, CardID: card.ID, Text: card.Message}
	reply := mobileTestChatCall(bridge, mobileBriefChatDevice(), http.MethodPost, "/chats/"+chatID+"/brief-messages", mobileBriefChatBody(t, input))
	if reply.Code != http.StatusBadRequest {
		t.Fatalf("a card tap without the words it showed: %d %s", reply.Code, reply.Body.String())
	}
	if _, err := bridge.app.SaveMemoryEntry("launch", "# Release checklist\nPrepare the release."); err != nil {
		t.Fatal(err)
	}
	input.CardMessage, input.Text = card.Message, "About the Launch plan: keep it short."
	_, text, err := bridge.app.mobileBriefChatReference(input, input.Text)
	if err != nil || !strings.Contains(text, "Release checklist") || strings.Contains(text, "Launch plan") {
		t.Fatalf("the user's words = %q, %v", text, err)
	}
}
