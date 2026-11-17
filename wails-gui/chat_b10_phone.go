package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"wails-gui/internal/workonit"
	"wails-gui/storage"
	"yansu-agent/agent/tools"
)

// Chat on the phone (phone-chat v1.0.0, phone-map B10): the computer's chat
// rows for a hand-off card, a correction and the sources line reach the phone
// as fields of their item, the phone answers a card and undoes a correction
// through their own routes, and a message can be attached to a Your work item
// or a brief sentence. /status reports chatB10 so a phone shows these only to
// a computer that serves them (PROTOCOL.md 15.4, 15.10).

// chatB10Enabled is the rollback switch for all of it: off, /status stops
// reporting chatB10, the phone's turns lose the three tools and the routes
// answer 404.
var chatB10Enabled = true

// mobileScopeChatCorrect lets a paired phone's chat turns save a correction
// with CorrectIsoform (design rule 8, hub 2026-11-16). It is the one thing a
// phone turn may change on the computer: a note in the person's own words,
// in the answers ledger, with "Changed · Undo" on both devices. Every other
// phone-authority guard stays.
const mobileScopeChatCorrect = "chat:correct"

// phoneTurnCorrections holds, per app, whether the phone that sent the main
// chat's current turn holds chat:correct. Phone sends set it before their turn
// starts; tools are chosen when the turn is prepared.
var phoneTurnCorrections sync.Map

// notePhoneTurnSender records the scope of the phone whose message starts the
// next main-chat turn.
func (a *App) notePhoneTurnSender(device mobileDeviceRecord) {
	if a == nil {
		return
	}
	if mobileHasScope(device.Scopes, mobileScopeChatCorrect) {
		phoneTurnCorrections.Store(a, true)
		return
	}
	phoneTurnCorrections.Delete(a)
}

// chatCorrectionAllowed reports whether a turn from this origin gets
// CorrectIsoform: computer turns, and phone turns from a phone that holds
// chat:correct.
func (a *App) chatCorrectionAllowed(origin promptOrigin) bool {
	switch origin {
	case promptOriginMac:
		return true
	case promptOriginPhone:
		allowed, _ := phoneTurnCorrections.Load(a)
		return chatB10Enabled && allowed == true
	default:
		return false
	}
}

// chatB10Origin reports whether a main-chat turn from this origin gets
// HandOffWork, CorrectIsoform and CiteSources: turns typed on the computer,
// and phone turns once the phone can show their rows. A phone Ask answers
// without tool authority, so it gets none.
func chatB10Origin(origin promptOrigin) bool {
	switch origin {
	case promptOriginMac:
		return true
	case promptOriginPhone:
		return chatB10Enabled
	default:
		return false
	}
}

// mainChatTurnTools are the extra tools a main-chat turn from this origin
// gets, in the order the runner lists them.
func (a *App) mainChatTurnTools(tag string, origin promptOrigin) []tools.Tool {
	out := append(a.mainChatExtraTools(tag, origin), a.mainChatDraftTools(tag, origin)...)
	out = append(out, a.mainChatHandoffTools(tag, origin)...)
	out = append(out, a.mainChatCorrectionTools(tag, origin)...)
	return append(out, a.mainChatSourcesTools(tag, origin)...)
}

// chatAnswerSurface is the answers-ledger surface of a turn's origin.
func chatAnswerSurface(origin promptOrigin) string {
	if origin == promptOriginPhone {
		return workSurfacePhone
	}
	return "computer"
}

// mobileChatAbout is what a message is attached to, as the phone sends and
// reads it. A brief sentence's id is "brief:<briefId>:p<index>".
type mobileChatAbout struct {
	Kind  string `json:"kind"`
	ID    string `json:"id"`
	Title string `json:"title,omitempty"`
}

const mobileChatAboutTitleMax = 400

// mobileChatHandoffView is a hand-off card for the phone (15.10).
type mobileChatHandoffView struct {
	Title  string                  `json:"title"`
	Detail string                  `json:"detail,omitempty"`
	Done   string                  `json:"done,omitempty"`
	Gets   []WorkGets              `json:"gets"`
	Status string                  `json:"status"`
	Kind   string                  `json:"kind"`
	Agent  *mobileChatHandoffAgent `json:"agent,omitempty"`
}

type mobileChatHandoffAgent struct {
	Name  string `json:"name"`
	Since string `json:"since,omitempty"`
}

// mobileChatCorrectionView is a correction line for the phone (15.10).
type mobileChatCorrectionView struct {
	Where  string `json:"where"`
	Status string `json:"status"`
}

// mobileChatB10Item projects a hand-off card, a correction line or a sources
// row. It reports false for any other row, or one that cannot be read, which
// stays plain text.
func mobileChatB10Item(view *mobileChatItemView, row storage.StreamItemRow) bool {
	if !chatB10Enabled {
		return false
	}
	switch row.ToolName {
	case chatHandoffCardTool:
		var payload chatHandoffPayload
		if err := json.Unmarshal([]byte(row.ToolInput), &payload); err != nil || payload.Title == "" {
			return false
		}
		card := &mobileChatHandoffView{
			Title: mobileChatClip(payload.Title, chatHandoffTitleMax), Detail: mobileChatBound(payload.Detail, mobileChatTextMax),
			Done: payload.Done, Gets: []WorkGets{}, Status: payload.Status, Kind: workonit.KindOf(workonit.Input{Kind: payload.Kind}),
		}
		for i, label := range payload.Gets {
			card.Gets = append(card.Gets, WorkGets{ID: "get-" + strconv.Itoa(i), Label: label, Removable: true})
		}
		if payload.Status == chatHandoffHanded && payload.Agent != "" {
			card.Agent = &mobileChatHandoffAgent{Name: payload.Agent, Since: payload.HandedAt}
		}
		view.Kind, view.Handoff = "handoff", card
	case chatCorrectionLineTool:
		var payload chatCorrectionPayload
		if err := json.Unmarshal([]byte(row.ToolInput), &payload); err != nil || payload.Where == "" {
			return false
		}
		view.Kind, view.Correction = "correction", &mobileChatCorrectionView{Where: payload.Where, Status: payload.Status}
	case chatSourcesLineTool:
		var payload chatSourcesPayload
		if err := json.Unmarshal([]byte(row.ToolInput), &payload); err != nil || len(payload.Sources) == 0 {
			return false
		}
		view.Kind, view.Sources, view.Claims = "sources", payload.Sources, payload.Claims
		if view.Claims == nil {
			view.Claims = []chatSourceClaim{}
		}
	default:
		return false
	}
	return true
}

// mobileChatAboutFromReceipt reads the About a context receipt row names, with
// the user row it belongs to.
func mobileChatAboutFromReceipt(row storage.StreamItemRow) (string, *mobileChatAbout) {
	if row.Kind != "context_status" || row.ToolName != "today_context" {
		return "", nil
	}
	var receipt struct {
		UserItemID string `json:"userItemId"`
		About      string `json:"about"`
		AboutKind  string `json:"aboutKind"`
		AboutID    string `json:"aboutId"`
	}
	if err := json.Unmarshal([]byte(row.ToolInput), &receipt); err != nil || receipt.UserItemID == "" || strings.TrimSpace(receipt.About) == "" {
		return "", nil
	}
	id := receipt.AboutID
	if id == "" {
		id = receipt.UserItemID
	}
	return receipt.UserItemID, &mobileChatAbout{Kind: mobileChatAboutKind(receipt.AboutKind), ID: id, Title: mobileChatClip(receipt.About, mobileChatAboutTitleMax)}
}

// mobileChatAboutKind maps a Today source kind to the phone's five.
func mobileChatAboutKind(sourceKind string) string {
	switch {
	case strings.HasPrefix(sourceKind, "brief"):
		return "brief"
	case sourceKind == "topic" || sourceKind == "interest":
		return "topic"
	case sourceKind == "handoff" || sourceKind == "history" || sourceKind == "schedule_run":
		return "result"
	case sourceKind == "activity_session" || sourceKind == "meeting":
		return "conversation"
	default:
		return "item"
	}
}

// mobileChatMarkAbout puts each receipt's About on its user row.
func mobileChatMarkAbout(rows []storage.StreamItemRow, views []mobileChatItemView, keep []bool) {
	if !chatB10Enabled {
		return
	}
	abouts := map[string]*mobileChatAbout{}
	for _, row := range rows {
		if userID, about := mobileChatAboutFromReceipt(row); about != nil {
			abouts[userID] = about
		}
	}
	if len(abouts) == 0 {
		return
	}
	for i := range views {
		if keep[i] && views[i].Role == "user" {
			if about, ok := abouts[views[i].ID]; ok {
				views[i].About = about
			}
		}
	}
}

// mobileChatAboutRef reads what a phone message is attached to again on the
// computer: the title the phone sent is never used. It reports false when the
// item is gone, hidden or unknown; the message then sends without it.
func (a *App) mobileChatAboutRef(ctx context.Context, about *mobileChatAbout) (TodayChatContextRef, bool) {
	if a == nil || about == nil || !chatB10Enabled || a.IsClosedEyesModeEnabled() {
		return TodayChatContextRef{}, false
	}
	id := strings.TrimSpace(about.ID)
	if id == "" || len(id) > 512 {
		return TodayChatContextRef{}, false
	}
	switch about.Kind {
	case "item":
		list, err := a.workListNow()
		if err != nil {
			return TodayChatContextRef{}, false
		}
		if _, ok := workFind(list, id); !ok {
			return TodayChatContextRef{}, false
		}
		return TodayChatContextRef{SourceKind: todayChatWorkKind, SourceID: id, AboutID: id}, true
	case "brief":
		briefID, paragraph, ok := mobileChatBriefSentenceID(id)
		if !ok {
			return TodayChatContextRef{}, false
		}
		brief, body, err := a.todayBriefParagraphMessage(ctx, briefID, "", paragraph, "", false)
		if err != nil || strings.TrimSpace(body) == "" {
			return TodayChatContextRef{}, false
		}
		sentence := strings.TrimSpace(brief.Narrative[paragraph].Text)
		return TodayChatContextRef{SourceKind: "brief", SourceID: brief.ConversationKey, BriefID: brief.BriefID,
			ParagraphDigest: mobileBriefModelDigest(sentence), AboutID: id, AboutTitle: sentence}, true
	default:
		return TodayChatContextRef{}, false
	}
}

// mobileChatBriefSentenceID parses "brief:<briefId>:p<index>".
func mobileChatBriefSentenceID(id string) (string, int, bool) {
	rest, ok := strings.CutPrefix(id, "brief:")
	if !ok {
		return "", 0, false
	}
	at := strings.LastIndex(rest, ":p")
	if at <= 0 {
		return "", 0, false
	}
	index, err := strconv.Atoi(rest[at+2:])
	if err != nil || index < 0 || index > 64 {
		return "", 0, false
	}
	return rest[:at], index, true
}

// startAboutChatTurn sends a phone message attached to what it names, through
// the same admission the computer's Today send uses.
func (bridge *mobileBridge) startAboutChatTurn(row storage.MobileChatRow, prompt string, ref TodayChatContextRef, waited bool) (string, int64, error) {
	chat := bridge.chat
	chat.sendMu.Lock()
	defer chat.sendMu.Unlock()
	if bridge.chatRunning(row.ID) {
		return "", 0, errMobileChatBusy
	}
	workDir, err := mobileChatWorkDir(row)
	if err != nil {
		return "", 0, err
	}
	model := row.Model
	if model == "" {
		model = bridge.app.chatTaskModel()
	}
	turnID := uuid.NewString()
	onAdmitted := func(order int) {
		bridge.app.mu.Lock()
		cancel := bridge.app.tagCancels[row.ID]
		bridge.app.mu.Unlock()
		chat.mu.Lock()
		chat.turns[row.ID] = turnID
		chat.replies[row.ID] = &mobileChatReply{turnID: turnID, userOrder: order, waited: waited, cancel: cancel}
		delete(chat.errored, row.ID)
		chat.mu.Unlock()
		bridge.app.notifySpotlightSessionChanged(row.ID)
	}
	order, err := bridge.app.sendPromptWithTodayContext(prompt, workDir, model, row.SessionID, row.ID, nil, nil, row.PlanMode, row.ThinkingEnabled, "", "",
		StreamItemData{ID: uuid.NewString(), Kind: "user"}, ref, promptOriginPhone, onAdmitted)
	if err != nil {
		return "", 0, err
	}
	return turnID, int64(order) + 1, nil
}

var errChatItemGone = errors.New("this item is no longer in the chat")
var errChatItemChanged = errors.New("item_changed")
var errChatAgentNotConnected = errors.New("agent_not_connected")

// chatB10ItemResponse writes the row as it now stands, or the error the
// phone handles: 404 for a row that is gone, 409 for one already answered.
func (bridge *mobileBridge) chatB10ItemResponse(w http.ResponseWriter, itemID string, err error) {
	store := bridge.app.storeForMobile()
	var view *mobileChatItemView
	if store != nil {
		if row, loadErr := store.LoadStreamItem(bridge.ctx, itemID); loadErr == nil && row != nil {
			if item, ok := mobileChatItemFromRow(*row); ok {
				view = &item
			}
		}
	}
	switch {
	case err == nil && view != nil:
		mobileJSON(w, http.StatusOK, map[string]any{"item": view}, "")
	case err == nil, errors.Is(err, errChatItemGone):
		mobileJSON(w, http.StatusNotFound, nil, errChatItemGone.Error())
	case errors.Is(err, errChatItemChanged):
		mobileJSON(w, http.StatusConflict, map[string]any{"code": "item_changed", "item": view}, errChatItemChanged.Error())
	case errors.Is(err, errChatAgentNotConnected):
		mobileJSON(w, http.StatusConflict, map[string]any{"code": "agent_not_connected", "item": view}, errChatAgentNotConnected.Error())
	default:
		log.Printf("[mobile-chat] chat item answer failed: %v", err)
		mobileJSON(w, http.StatusServiceUnavailable, nil, "the answer could not be saved; try again")
	}
}

// serveChatHandoffAnswer answers POST /chats/{chatId}/items/{itemId}/handoff:
// yes hands the card's work to the agent on the computer, no declines it. The
// first answer stands.
func (bridge *mobileBridge) serveChatHandoffAnswer(w http.ResponseWriter, r *http.Request, device mobileDeviceRecord, chatID, itemID string) {
	var input struct {
		IdempotencyKey string   `json:"idempotencyKey"`
		Action         string   `json:"action"`
		Reason         string   `json:"reason,omitempty"`
		AgentID        string   `json:"agentId,omitempty"`
		Removed        []string `json:"removed,omitempty"`
	}
	if !mobileValidID(chatID) || !mobileValidID(itemID) {
		mobileJSON(w, http.StatusNotFound, nil, errChatItemGone.Error())
		return
	}
	if !mobileDecode(w, r, &input) {
		return
	}
	key, valid := mobileKey(device.DeviceID, input.IdempotencyKey)
	if !valid {
		mobileJSON(w, http.StatusBadRequest, nil, "an idempotency key of 1 to 128 bytes is required")
		return
	}
	if input.Action != "yes" && input.Action != "no" || len(input.Reason) > 200 || len(input.AgentID) > 64 || len(input.Removed) > chatHandoffGetsMax {
		mobileJSON(w, http.StatusBadRequest, nil, "action must be yes or no")
		return
	}
	bridge.runReceipted(w, device, key, r.Method+" "+r.URL.Path, func(w http.ResponseWriter) {
		var err error
		if input.Action == "no" {
			err = bridge.app.answerChatHandoffFromPhone(bridge.ctx, chatID, itemID, chatHandoffDeclined, "", time.Time{})
		} else {
			err = bridge.app.handChatCardFromPhone(bridge.ctx, chatID, itemID, input.AgentID)
		}
		bridge.chatB10ItemResponse(w, itemID, err)
	})
}

// serveChatCorrectionUndo answers POST /chats/{chatId}/items/{itemId}/correction/undo.
func (bridge *mobileBridge) serveChatCorrectionUndo(w http.ResponseWriter, r *http.Request, device mobileDeviceRecord, chatID, itemID string) {
	var input struct {
		IdempotencyKey string `json:"idempotencyKey"`
	}
	if !mobileValidID(chatID) || !mobileValidID(itemID) {
		mobileJSON(w, http.StatusNotFound, nil, errChatItemGone.Error())
		return
	}
	if !mobileDecode(w, r, &input) {
		return
	}
	key, valid := mobileKey(device.DeviceID, input.IdempotencyKey)
	if !valid {
		mobileJSON(w, http.StatusBadRequest, nil, "an idempotency key of 1 to 128 bytes is required")
		return
	}
	bridge.runReceipted(w, device, key, r.Method+" "+r.URL.Path, func(w http.ResponseWriter) {
		row, err := bridge.app.mainChatB10Row(bridge.ctx, chatID, itemID, chatCorrectionLineTool)
		if err != nil {
			bridge.chatB10ItemResponse(w, itemID, err)
			return
		}
		var payload chatCorrectionPayload
		if json.Unmarshal([]byte(row.ToolInput), &payload) == nil && payload.Status == chatCorrectionUndone {
			bridge.chatB10ItemResponse(w, itemID, errChatItemChanged)
			return
		}
		status, err := bridge.app.undoChatCorrection(itemID, workSurfacePhone)
		if err == nil && status != chatCorrectionUndone {
			err = errChatItemChanged
		}
		if err != nil && !errors.Is(err, errChatItemChanged) {
			err = errChatItemGone
		}
		bridge.chatB10ItemResponse(w, itemID, err)
	})
}

// mainChatB10Row loads a card or line of the main chat by its id, checking the
// chat the phone named is the main chat.
func (a *App) mainChatB10Row(ctx context.Context, chatID, itemID, toolName string) (*storage.StreamItemRow, error) {
	if a == nil || a.store == nil {
		return nil, errChatItemGone
	}
	main, err := a.store.LoadMainSession(ctx)
	if err != nil || main == nil || chatID != mobileMainChatAlias && chatID != main.LocalID {
		return nil, errChatItemGone
	}
	row, err := a.store.LoadStreamItem(ctx, itemID)
	if err != nil || row == nil || row.SessionLocalID != main.LocalID || row.Kind != "system" || row.ToolName != toolName {
		return nil, errChatItemGone
	}
	return row, nil
}

// answerChatHandoffFromPhone records No, or Yes with who took it.
func (a *App) answerChatHandoffFromPhone(ctx context.Context, chatID, itemID, answer, agent string, at time.Time) error {
	if _, err := a.mainChatB10Row(ctx, chatID, itemID, chatHandoffCardTool); err != nil {
		return err
	}
	status, err := a.answerChatHandoffCard(itemID, answer, agent, at)
	if err != nil {
		return errChatItemGone
	}
	if status != answer {
		return errChatItemChanged
	}
	return nil
}

// handChatCardFromPhone hands the card's work to the agent on the computer, in
// one request: it prepares the task from the card's own words and sends it by
// a task link, the one way that needs no tap on the computer. An agent without
// one answers agent_not_connected, and the card stays open.
func (a *App) handChatCardFromPhone(ctx context.Context, chatID, itemID, agentID string) error {
	row, err := a.mainChatB10Row(ctx, chatID, itemID, chatHandoffCardTool)
	if err != nil {
		return err
	}
	var payload chatHandoffPayload
	if err := json.Unmarshal([]byte(row.ToolInput), &payload); err != nil {
		return errChatItemGone
	}
	if payload.Status != "" {
		return errChatItemChanged
	}
	in := workonit.Input{SourceKind: workonit.SourceChatCard, SourceID: row.ID, Title: payload.Title, Detail: payload.Detail, Done: payload.Done, Kind: payload.Kind}
	prepared, err := a.PrepareWorkOnIt(in)
	if err != nil {
		return errChatAgentNotConnected
	}
	if agentID == "" {
		for _, agent := range prepared.Agents {
			if agent.Default {
				agentID = agent.ID
			}
		}
	}
	var chosen *WorkOnItAgentView
	for i := range prepared.Agents {
		if prepared.Agents[i].ID == agentID {
			chosen = &prepared.Agents[i]
		}
	}
	if chosen == nil || !slices.Contains(chosen.Methods, workonit.MethodTaskLink) {
		return errChatAgentNotConnected
	}
	if _, err := a.SendWorkOnIt(prepared.TaskID, chosen.ID, workonit.MethodTaskLink); err != nil {
		log.Printf("[mobile-chat] hand-off card not sent: %v", err)
		return errChatAgentNotConnected
	}
	return a.answerChatHandoffFromPhone(ctx, chatID, itemID, chatHandoffHanded, chosen.Name, time.Now())
}
