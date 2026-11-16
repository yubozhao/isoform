package main

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"wails-gui/internal/engagement"
	"wails-gui/internal/handoff"
	"wails-gui/internal/pii"
	"wails-gui/storage"
)

// Release 3 (PROTOCOL.md section 15): the phone is a client of the desktop's
// own chat sessions. Turns run through executePrompt exactly as the composer
// runs them; the phone reads projections of stored stream items and hears
// about turns through coalesced chat_turn events that carry no content.

const (
	mobileChatListDefault    = 20
	mobileChatListMax        = 50
	mobileChatItemsDefault   = 50
	mobileChatItemsMax       = 50
	mobileChatTitleMax       = 120
	mobileChatTextMax        = 4096
	mobileChatToolSummaryMax = 240
	mobileChatInputMax       = 16 * 1024
	mobileChatBriefMax       = 8 * 1024
	mobileChatTitleInputMax  = 200
	mobileHandoffTitleMax    = 200
	mobileCorrectionNoteMax  = 1000
	// mobileChatPageBudget bounds the escaped size of the items in one page, so
	// the sealed response stays under the 64 KiB plaintext limit (section 4).
	mobileChatPageBudget = 48 * 1024
	// mobileChatEventInterval is the coalescing window for chat_turn events.
	mobileChatEventInterval = time.Second
	// mobileChatReplyProbe is how many of a chat's newest rows the hub reads
	// to tell whether the desktop renderer stored a settled turn.
	mobileChatReplyProbe = 50
	// mobileChatReplyToolMax bounds a tool result in the hub's own copy of a
	// turn; the renderer stores the full text when it is there to store it.
	mobileChatReplyToolMax = 32 * 1024

	mobileEventChatTurn = "chat_turn"

	mobileChatStateIdle    = "idle"
	mobileChatStateRunning = "running"
	mobileChatStateWaiting = "waiting"

	mobileChatEventRunning    = "running"
	mobileChatEventDone       = "done"
	mobileChatEventError      = "error"
	mobileChatEventPermission = "permission"

	mobileChatKindChat    = "chat"
	mobileChatKindHandoff = "handoff"
	mobileChatKindMain    = "main"
	mobileChatKindSide    = "side"
)

// mobileChatSettleDelay waits after a turn closes before the final event, so
// the desktop renderer has written the turn's last rows (PROTOCOL.md 15.5).
// Tests shorten it.
var mobileChatSettleDelay = 2 * time.Second

var (
	errMobileChatBusy     = errors.New("a turn is already running in this chat")
	errMobileChatWorkDir  = errors.New("this chat's folder is not on your computer any more")
	errMobileChatNotReady = errors.New("the chat store is not ready")
)

// Test seams: the relay end-to-end harness answers a canned turn instead of
// calling a model, and replaces the memory store for a correction. Production
// leaves both nil. The override reports events through app.emitAgentEvent and
// must emit close when it finishes. It stops early when cancelled is closed.
var (
	mobileChatRunnerOverride   func(app *App, chatID, turnID, prompt string, cancelled <-chan struct{})
	mobileMemoryRejectOverride func(memoryID, reason string) error
	// Tests withdraw a source after admission but before dispatch, without
	// running an app or model. Production leaves this nil.
	mobileChatOfferDispatchHook func()
	// This seam runs after INSERT while its transaction is still invisible.
	mobileChatOfferCommitHook func()
)

// mobileChatState is the phone's view of running chats. It is fed by the
// agent event listener and read by the route handlers.
type mobileChatState struct {
	mu        sync.Mutex
	turns     map[string]string                 // chat id -> turn id of the phone-visible turn
	overrides map[string]func()                 // chat id -> cancel for a test-override turn
	errored   map[string]bool                   // chat id -> the current turn reported an error
	runs      map[string]string                 // chat id -> last run id seen
	replies   map[string]*mobileChatReply       // chat id -> the hub's copy of the phone turn's rows
	pending   map[string]*mobileChatPermission  // request id -> pending tool permission
	byChat    map[string]string                 // chat id -> request id of its pending permission
	answers   map[string]*mobileChatAnswerBatch // chat id -> answers to waited messages the phone has not read
	coalesce  *mobileChatCoalescer
	sendMu    sync.Mutex // serializes sends so the busy check and the first write are atomic
	detach    func()
}

// mobileChatPermission is a tool permission the runner is blocked on. It is
// session state on the desktop, never a stored item, so the phone sees it only
// while it is pending.
type mobileChatPermission struct {
	RequestID      string
	WorkRequestID  string
	ChatID         string
	AgentSessionID string
	ToolName       string
	Text           string
	Options        []mobileChatOption
}

type mobileChatOption struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// mobileChatReply is the hub's copy of a phone turn, built from the agent
// event stream the way the renderer builds its rows. The desktop renderer is
// the writer of a turn's rows (PROTOCOL.md 15.5); this copy is stored only
// when the renderer stored nothing by the time the turn settled, which happens
// while the app is still starting or when no window ever mounted. Thinking is
// left out: the phone never shows it.
type mobileChatReply struct {
	turnID    string
	userOrder int // sort order of the user row the turn answers
	ask       bool
	waited    bool // the phone queued the message while the computer was away
	cancel    context.CancelFunc
	rows      []storage.StreamItemRow // finished rows in event order
	runID     string                  // run of the open assistant row
	text      strings.Builder         // the open assistant row
}

// note folds one agent event into the copy. The caller holds chat.mu.
func (reply *mobileChatReply) note(evt AgentEvent) {
	switch evt.Type {
	case "assistant":
		if evt.Content == "" {
			return
		}
		if evt.RunID != reply.runID {
			reply.flush()
			reply.runID = evt.RunID
		}
		reply.text.WriteString(evt.Content)
	case "tool_use":
		// The renderer's row kinds: "tool" for a call, "result" for its result.
		reply.flush()
		name := evt.ToolName
		if name == "" {
			name = "tool"
		}
		reply.rows = append(reply.rows, storage.StreamItemRow{
			ID: uuid.NewString(), Kind: "tool", ToolName: name, ToolInput: string(evt.ToolInput),
			ToolUseID: evt.ToolUseID, RunID: evt.RunID, SessionID: evt.SessionID,
		})
	case "tool_result":
		reply.flush()
		content := chatContextResultContent(evt.ToolName, evt.Content)
		if content == "" {
			content = "Result received."
		}
		reply.rows = append(reply.rows, storage.StreamItemRow{
			ID: uuid.NewString(), Kind: "result", Content: mobileChatClip(content, mobileChatReplyToolMax),
			ToolName: evt.ToolName, ToolUseID: evt.ToolUseID, RunID: evt.RunID, SessionID: evt.SessionID,
		})
	case "error":
		reply.flush()
		message := evt.Error
		if message == "" {
			message = evt.Content
		}
		if message == "" {
			message = "Unknown error"
		}
		reply.rows = append(reply.rows, storage.StreamItemRow{
			ID: uuid.NewString(), Kind: "error", Content: message, ToolInput: chatErrorCauseJSON(evt.Code, evt.Assistant),
			RunID: evt.RunID, SessionID: evt.SessionID,
		})
	}
}

// flush closes the open assistant row.
func (reply *mobileChatReply) flush() {
	if reply.text.Len() == 0 {
		return
	}
	reply.rows = append(reply.rows, storage.StreamItemRow{
		ID: uuid.NewString(), Kind: "assistant", Content: reply.text.String(), RunID: reply.runID,
	})
	reply.text.Reset()
}

func newMobileChatState(now func() time.Time) *mobileChatState {
	return &mobileChatState{
		turns:     make(map[string]string),
		overrides: make(map[string]func()),
		errored:   make(map[string]bool),
		runs:      make(map[string]string),
		replies:   make(map[string]*mobileChatReply),
		pending:   make(map[string]*mobileChatPermission),
		byChat:    make(map[string]string),
		answers:   make(map[string]*mobileChatAnswerBatch),
		coalesce:  newMobileChatCoalescer(mobileChatEventInterval, now),
	}
}

// mobileChatCoalescer decides when a chat's change may be published. The first
// change publishes at once; changes inside the window wait for one trailing
// publish at the window's end, so a streaming turn costs about one event per
// second.
type mobileChatCoalescer struct {
	interval time.Duration
	now      func() time.Time
	last     map[string]time.Time
	armed    map[string]bool
}

func newMobileChatCoalescer(interval time.Duration, now func() time.Time) *mobileChatCoalescer {
	return &mobileChatCoalescer{interval: interval, now: now, last: make(map[string]time.Time), armed: make(map[string]bool)}
}

// request reports whether to publish now. Otherwise it reports the delay after
// which the trailing publish should run, or zero when one is already armed.
func (c *mobileChatCoalescer) request(chatID string) (bool, time.Duration) {
	if c.armed[chatID] {
		return false, 0
	}
	now := c.now()
	last, seen := c.last[chatID]
	if !seen || now.Sub(last) >= c.interval {
		c.last[chatID] = now
		return true, 0
	}
	c.armed[chatID] = true
	return false, c.interval - now.Sub(last)
}

// fired records the trailing publish for a chat.
func (c *mobileChatCoalescer) fired(chatID string) {
	delete(c.armed, chatID)
	c.last[chatID] = c.now()
}

// mobileChatTurnEvent is the h2d event of PROTOCOL.md section 15.5. It carries
// identifiers and a state only, never content.
type mobileChatTurnEvent struct {
	Event  string `json:"event"`
	ChatID string `json:"chatId"`
	TurnID string `json:"turnId"`
	State  string `json:"state"`
	Seq    int64  `json:"seq"`
	At     string `json:"at"`
}

// observeChatEvent is the in-process listener on agent events. It runs on the
// streaming goroutine, so it only updates in-memory state and schedules work.
func (bridge *mobileBridge) observeChatEvent(evt AgentEvent) {
	chatID := evt.Tag
	if chatID == "" || bridge.chat == nil {
		return
	}
	chat := bridge.chat
	closed := false
	finalState, finalTurn := "", ""
	var reply *mobileChatReply
	chat.mu.Lock()
	if draft := chat.replies[chatID]; draft != nil {
		draft.note(evt)
	}
	switch evt.Type {
	case "permission_request":
		chat.setPermissionLocked(chatID, evt)
	case "close":
		closed = true
		finalState = mobileChatEventDone
		if chat.errored[chatID] {
			finalState = mobileChatEventError
		}
		finalTurn = chat.turnIDLocked(chatID)
		reply = chat.replies[chatID]
		if reply != nil {
			reply.flush()
		}
		delete(chat.replies, chatID)
		delete(chat.turns, chatID)
		delete(chat.errored, chatID)
		chat.clearPermissionLocked(chatID)
	default:
		chat.clearPermissionLocked(chatID)
		switch evt.Type {
		case "error":
			chat.errored[chatID] = true
		case "run_created":
			delete(chat.errored, chatID)
			if evt.RunID != "" {
				chat.runs[chatID] = evt.RunID
			}
		}
	}
	chat.mu.Unlock()
	if closed {
		bridge.scheduleFinalChatTurn(chatID, finalTurn, finalState, reply)
		return
	}
	bridge.requestChatTurnEvent(chatID)
}

func (chat *mobileChatState) setPermissionLocked(chatID string, evt AgentEvent) {
	chat.clearPermissionLocked(chatID)
	options := make([]mobileChatOption, 0, len(evt.PermissionOptions))
	for _, option := range evt.PermissionOptions {
		options = append(options, mobileChatOption{ID: option.ID, Label: mobileChatClip(option.Name, mobileChatTitleMax)})
	}
	requestID := evt.PermissionRequestID
	if requestID == "" {
		requestID = uuid.NewString()
	}
	chat.pending[requestID] = &mobileChatPermission{
		RequestID: requestID, WorkRequestID: evt.PermissionRequestID, ChatID: chatID, AgentSessionID: evt.SessionID,
		ToolName: mobileChatClip(evt.ToolName, mobileChatTitleMax),
		Text:     mobileChatBound(evt.Content, mobileChatTextMax),
		Options:  options,
	}
	chat.byChat[chatID] = requestID
}

func (chat *mobileChatState) clearPermissionLocked(chatID string) {
	if requestID, ok := chat.byChat[chatID]; ok {
		delete(chat.pending, requestID)
		delete(chat.byChat, chatID)
	}
}

func (chat *mobileChatState) turnIDLocked(chatID string) string {
	if turn := chat.turns[chatID]; turn != "" {
		return turn
	}
	return chat.runs[chatID]
}

// requestChatTurnEvent publishes now or arms the trailing publish for a chat.
// Without a device that may read chats there is nothing to send.
func (bridge *mobileBridge) requestChatTurnEvent(chatID string) {
	if !bridge.hasChatReaders() {
		return
	}
	chat := bridge.chat
	chat.mu.Lock()
	publish, wait := chat.coalesce.request(chatID)
	chat.mu.Unlock()
	switch {
	case publish:
		go func() {
			if !bridge.trackBackground() {
				return
			}
			defer bridge.background.Done()
			bridge.publishChatTurnNow(chatID)
		}()
	case wait > 0:
		time.AfterFunc(wait, func() {
			if !bridge.trackBackground() {
				return
			}
			defer bridge.background.Done()
			chat.mu.Lock()
			chat.coalesce.fired(chatID)
			chat.mu.Unlock()
			bridge.publishChatTurnNow(chatID)
		})
	}
}

// scheduleFinalChatTurn publishes the completion event once the turn's rows
// have settled. The state is fixed at close, so a later trailing event cannot
// overwrite it with running. A phone turn's copy is stored first when the
// renderer stored nothing, so the phone's re-read finds the reply.
func (bridge *mobileBridge) scheduleFinalChatTurn(chatID, turnID, state string, reply *mobileChatReply) {
	time.AfterFunc(mobileChatSettleDelay, func() {
		if !bridge.trackBackground() {
			return
		}
		defer bridge.background.Done()
		if reply != nil {
			bridge.storeChatReplyIfMissing(chatID, reply)
		}
		bridge.publishChatTurn(chatID, turnID, state)
		if reply != nil && reply.waited && state == mobileChatEventDone {
			bridge.noticeWaitedChatAnswer(chatID, reply)
		}
	})
}

// storeChatReplyIfMissing writes the hub's copy of a phone turn when the
// desktop renderer stored none of it. Any row after the user's message means
// a window saw the turn, and the hub stays out of its way.
func (bridge *mobileBridge) storeChatReplyIfMissing(chatID string, reply *mobileChatReply) {
	if len(reply.rows) == 0 {
		return
	}
	store := bridge.app.storeForMobile()
	if store == nil {
		return
	}
	ctx := bridge.ctx
	page, err := store.LoadStreamItemsLatest(ctx, chatID, mobileChatReplyProbe)
	if err != nil {
		log.Printf("[mobile-chat] could not check turn %s before storing the reply: %v", reply.turnID, err)
		return
	}
	for _, item := range page.Items {
		// Private context counts do not prove a window stored the reply.
		if item.Kind == "context_status" {
			continue
		}
		// A main-chat report or texted reply that landed mid-turn is not the
		// desktop's copy of the reply.
		if item.Kind == "system" && (item.ToolName == mainChatReportTool || item.ToolName == mainChatSetupTool || item.ToolName == mainChatTextReplyTool) {
			continue
		}
		if item.Kind != "user" && item.SortOrder > reply.userOrder {
			return
		}
	}
	stored := 0
	for _, row := range reply.rows {
		if _, err := store.AppendStreamItem(ctx, chatID, row); err != nil {
			log.Printf("[mobile-chat] could not store a %s row for turn %s: %v", row.Kind, reply.turnID, err)
			continue
		}
		stored++
	}
	if stored > 0 {
		bridge.app.mainChatRowsChanged(chatID)
	}
	log.Printf("[mobile-chat] no desktop window stored turn %s; the hub stored its %d rows", reply.turnID, stored)
	bridge.app.notifySpotlightSessionChanged(chatID)
}

func (bridge *mobileBridge) publishChatTurnNow(chatID string) {
	chat := bridge.chat
	chat.mu.Lock()
	state := chat.eventStateLocked(chatID)
	turnID := chat.turnIDLocked(chatID)
	chat.mu.Unlock()
	bridge.publishChatTurn(chatID, turnID, state)
}

// eventStateLocked is the state a coalesced chat_turn event reports (section
// 15.5). Completion is published by scheduleFinalChatTurn instead.
func (chat *mobileChatState) eventStateLocked(chatID string) string {
	switch {
	case chat.byChat[chatID] != "":
		return mobileChatEventPermission
	case chat.errored[chatID]:
		return mobileChatEventError
	default:
		return mobileChatEventRunning
	}
}

// publishChatTurn seals one chat_turn event to every device holding chat:read.
func (bridge *mobileBridge) publishChatTurn(chatID, turnID, state string) {
	if !bridge.hasChatReaders() {
		return
	}
	payload, err := json.Marshal(mobileChatTurnEvent{
		Event: mobileEventChatTurn, ChatID: chatID, TurnID: turnID, State: state,
		Seq: bridge.latestChatSeq(chatID), At: time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		return
	}
	bridge.publishEvent(mobileScopeChatRead, payload)
}

// hasChatReaders reports whether a paired device may read chats.
func (bridge *mobileBridge) hasChatReaders() bool {
	bridge.mu.Lock()
	defer bridge.mu.Unlock()
	for _, record := range bridge.devices {
		if mobileHasScope(record.Scopes, mobileScopeChatRead) {
			return true
		}
	}
	return false
}

// latestChatSeq is the newest stored item's seq, or zero for an empty chat.
func (bridge *mobileBridge) latestChatSeq(chatID string) int64 {
	store := bridge.app.storeForMobile()
	if store == nil {
		return 0
	}
	order, err := store.GetMaxSortOrder(bridge.ctx, chatID)
	if err != nil || order < 0 {
		return 0
	}
	return int64(order) + 1
}

// chatRunning reports whether a turn is in flight: a desktop run, a phone run
// or a test-override run.
func (bridge *mobileBridge) chatRunning(chatID string) bool {
	app := bridge.app
	app.mu.Lock()
	active := app.activeTags[chatID]
	app.mu.Unlock()
	if active {
		return true
	}
	bridge.chat.mu.Lock()
	defer bridge.chat.mu.Unlock()
	_, override := bridge.chat.overrides[chatID]
	return override
}

// chatListState is the state the list and detail routes report.
func (bridge *mobileBridge) chatListState(chatID string) string {
	bridge.chat.mu.Lock()
	waiting := bridge.chat.byChat[chatID] != ""
	bridge.chat.mu.Unlock()
	switch {
	case waiting:
		return mobileChatStateWaiting
	case bridge.chatRunning(chatID):
		return mobileChatStateRunning
	default:
		return mobileChatStateIdle
	}
}

// startChatTurn runs one phone message in a chat. It refuses a busy chat before
// it writes anything, so a rejected send leaves no user row behind. It returns
// the turn id and the seq of the stored user item (PROTOCOL.md 15.3).
func (bridge *mobileBridge) startChatTurn(row storage.MobileChatRow, prompt string, refs []mobileOfferReference) (string, int64, error) {
	return bridge.startChatTurnWaited(row, prompt, refs, false)
}

// startChatTurnWaited starts a turn; waited marks a message the phone queued while the computer was away.
func (bridge *mobileBridge) startChatTurnWaited(row storage.MobileChatRow, prompt string, refs []mobileOfferReference, waited bool) (string, int64, error) {
	chat := bridge.chat
	chat.sendMu.Lock()
	defer chat.sendMu.Unlock()
	if bridge.chatRunning(row.ID) {
		return "", 0, errMobileChatBusy
	}
	store := bridge.app.storeForMobile()
	ctx := bridge.ctx
	// Resolve after the send lock: a request can wait here while its sources
	// are withdrawn. Nothing source-derived is durable until this succeeds.
	originalPrompt := prompt
	var err error
	prompt, provenance, err := bridge.app.mobileOfferChatAdmission(ctx, originalPrompt, refs)
	if err != nil {
		return "", 0, err
	}
	workDir := ""
	model := row.Model
	if mobileChatRunnerOverride == nil {
		workDir, err = mobileChatWorkDir(row)
		if err != nil {
			return "", 0, err
		}
		if model == "" {
			model = bridge.app.chatTaskModel()
		}
	}
	if len(refs) != 0 {
		if mobileChatOfferDispatchHook != nil {
			mobileChatOfferDispatchHook()
		}
		// Folder/model resolution can take time. Repeat the source and text
		// checks before even the first durable user-row publication.
		prompt, provenance, err = bridge.app.mobileOfferChatAdmission(ctx, originalPrompt, refs)
		if err != nil {
			return "", 0, err
		}
	}
	userItemID := uuid.NewString()
	userItem := storage.StreamItemRow{ID: userItemID, Kind: "user", Content: prompt, AutomationContext: provenance, ToolInput: mobileChatPhoneOriginToolInput}
	var userOrder int
	if len(refs) == 0 {
		userOrder, err = store.AppendStreamItem(ctx, row.ID, userItem)
	} else {
		// Keep the prospective row uncommitted while re-resolving its source.
		// Main-store policy writes serialize with this transaction. Explicit
		// activity deletion shares the guard held from validation through commit.
		userOrder, err = bridge.app.appendOfferChatTurn(ctx, store, row.ID, userItem, originalPrompt, refs)
	}
	if err != nil {
		return "", 0, err
	}
	bridge.app.mainChatRowsChanged(row.ID)
	userSeq := int64(userOrder) + 1
	turnID := uuid.NewString()
	chat.mu.Lock()
	chat.turns[row.ID] = turnID
	chat.replies[row.ID] = &mobileChatReply{turnID: turnID, userOrder: userOrder, waited: waited}
	delete(chat.errored, row.ID)
	chat.mu.Unlock()
	rollback := func(cause error) (string, int64, error) {
		chat.mu.Lock()
		if chat.turns[row.ID] == turnID {
			delete(chat.turns, row.ID)
			delete(chat.replies, row.ID)
		}
		chat.mu.Unlock()
		if derr := store.DeleteStreamItemByID(ctx, userItemID); derr != nil {
			log.Printf("[mobile-chat] could not remove a rejected message: %v", derr)
		}
		return "", 0, cause
	}
	// The row became visible only after its source check committed.
	bridge.app.notifySpotlightSessionChanged(row.ID)

	if mobileChatRunnerOverride != nil {
		if _, _, err := bridge.app.sourceBoundChatHistory(ctx, row.ID); err != nil {
			return rollback(err)
		}
		cancelled := make(chan struct{})
		var once sync.Once
		cancel := func() { once.Do(func() { close(cancelled) }) }
		chat.mu.Lock()
		chat.overrides[row.ID] = cancel
		chat.mu.Unlock()
		override := mobileChatRunnerOverride
		go func() {
			if _, _, err := bridge.app.sourceBoundChatHistory(ctx, row.ID); err != nil {
				bridge.app.emitAgentEvent(AgentEvent{Type: "error", Tag: row.ID, Error: errChatSourceAccessChanged.Error()})
				bridge.app.emitAgentEvent(AgentEvent{Type: "close", Tag: row.ID})
			} else {
				override(bridge.app, row.ID, turnID, prompt, cancelled)
			}
			chat.mu.Lock()
			if chat.turns[row.ID] == turnID {
				delete(chat.turns, row.ID)
			}
			delete(chat.overrides, row.ID)
			chat.mu.Unlock()
		}()
		return turnID, userSeq, nil
	}

	// executePrompt persists nothing for the phone's turn; the renderer stores the
	// assistant and tool rows as it does for every chat (PROTOCOL.md section 15.5),
	// and the hub's copy covers a turn no window saw (storeChatReplyIfMissing).
	// The phone's turn gets no Upcoming tools: those changes are made on the Mac.
	if err := bridge.app.executePrompt(prompt, workDir, model, row.SessionID, row.ID, nil, row.PlanMode, row.ThinkingEnabled, "", nil, nil, "", "", promptOriginPhone); err != nil {
		if errors.Is(err, errPromptAlreadyRunning) {
			return rollback(errMobileChatBusy)
		}
		return rollback(err)
	}
	return turnID, userSeq, nil
}

// mobileChatWorkDir resolves the folder a phone turn runs in, the same way the
// composer does for a project chat. An empty result gives the session's own
// temporary folder.
func mobileChatWorkDir(row storage.MobileChatRow) (string, error) {
	dir := row.WorktreePath
	if dir == "" {
		dir = row.ProjectPath
	}
	if dir == "" {
		return "", nil
	}
	if !filepath.IsAbs(dir) {
		return "", errMobileChatWorkDir
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return "", errMobileChatWorkDir
	}
	return filepath.Clean(dir), nil
}

// cancelChatTurn stops the chat's running turn. It reports whether one was running.
func (bridge *mobileBridge) cancelChatTurn(chatID string) bool {
	running := bridge.chatRunning(chatID)
	bridge.chat.mu.Lock()
	cancel := bridge.chat.overrides[chatID]
	bridge.chat.mu.Unlock()
	if cancel != nil {
		cancel()
		return true
	}
	if running {
		bridge.app.CancelByTag(chatID)
	}
	return running
}

// mobileChatClip cuts s to at most limit bytes on a rune boundary.
func mobileChatClip(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// mobileChatBound cuts s to limit bytes and marks the cut.
func mobileChatBound(s string, limit int) string {
	const marker = " [truncated]"
	if len(s) <= limit {
		return s
	}
	return mobileChatClip(s, limit-len(marker)) + marker
}

// mobileChatToolSummary turns a tool input into one line. Values are cut short,
// so a file's contents never travel with the summary.
func mobileChatToolSummary(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "null" {
		return ""
	}
	var fields map[string]any
	if err := json.Unmarshal([]byte(raw), &fields); err != nil || len(fields) == 0 {
		return mobileChatBound(strings.Join(strings.Fields(raw), " "), mobileChatToolSummaryMax)
	}
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		value, ok := fields[key].(string)
		if !ok {
			encoded, _ := json.Marshal(fields[key])
			value = string(encoded)
		}
		value = mobileChatClip(strings.Join(strings.Fields(value), " "), 80)
		parts = append(parts, key+": "+value)
	}
	return mobileChatBound(strings.Join(parts, "; "), mobileChatToolSummaryMax)
}

// mobileChatItemView is one entry of a chat page (PROTOCOL.md section 15.4).
type mobileChatItemView struct {
	Seq        int64                     `json:"seq"`
	ID         string                    `json:"id,omitempty"`
	Role       string                    `json:"role"`
	Kind       string                    `json:"kind"`
	Text       string                    `json:"text"`
	ToolName   string                    `json:"toolName"`
	At         string                    `json:"at"`
	Permission *mobileChatPermissionView `json:"permission,omitempty"`
	Proactive  *mainChatProactive        `json:"proactive,omitempty"`
	Status     *mainChatStatus           `json:"status,omitempty"`
	Report     *mainChatReport           `json:"report,omitempty"`
	RowClass   string                    `json:"rowClass,omitempty"`
	TopicIDs   []string                  `json:"topicIds,omitempty"`
	LocalDay   string                    `json:"localDay,omitempty"`
	Chips      []string                  `json:"chips,omitempty"`
	// AskedAt is when the user asked the question an answer replies to.
	AskedAt string `json:"askedAt,omitempty"`
	// Code and Assistant are an error row's proven cause and the AI it names
	// (chat_cause.go), so the phone's line matches the computer's.
	Code      string `json:"code,omitempty"`
	Assistant string `json:"assistant,omitempty"`
}

type mobileChatPermissionView struct {
	RequestID string             `json:"requestId"`
	Options   []mobileChatOption `json:"options"`
}

// mobileChatItemFromRow maps a stored stream item to the phone's shape. Thinking
// and compaction rows are internal and are not shown; the bool is false for them.
func mobileChatItemFromRow(row storage.StreamItemRow) (mobileChatItemView, bool) {
	view := mobileChatItemView{
		Seq:      int64(row.SortOrder) + 1,
		ID:       row.ID,
		ToolName: mobileChatClip(row.ToolName, mobileChatTitleMax),
		At:       time.UnixMilli(row.CreatedAt).UTC().Format(time.RFC3339),
	}
	switch row.Kind {
	case "user":
		view.Role, view.Kind, view.Text = "user", "text", mobileChatBound(row.Content, mobileChatTextMax)
	case "assistant":
		view.Role, view.Kind, view.Text = "assistant", "text", mobileChatBound(row.Content, mobileChatTextMax)
	case "system":
		// The brief lives on Today, and Chat never speaks first
		// (pass-questions v1.0.0 rule 8): a brief's card is not a chat item.
		if mainChatRowIsBriefCard(row) {
			return view, false
		}
		view.Role, view.Kind, view.Text = "system", "text", mobileChatBound(row.Content, mobileChatTextMax)
		mobileChatStructuredItem(&view, row)
	case "error":
		view.Role, view.Kind, view.Text = "system", "error", mobileChatBound(row.Content, mobileChatTextMax)
		cause := parseChatErrorCause(row.ToolInput)
		view.Code, view.Assistant = cause.Code, mobileChatClip(cause.Assistant, mobileChatTitleMax)
	case "tool":
		view.Role, view.Kind, view.Text = "tool", "tool_use", mobileChatToolSummary(row.ToolInput)
	case "result":
		view.Role, view.Kind, view.Text = "tool", "tool_result", mobileChatBound(chatContextResultContent(row.ToolName, row.Content), mobileChatTextMax)
	default:
		return view, false
	}
	return view, true
}

// mobileChatStructuredItem adds the optional presentation payload while keeping
// text for older phones. Invalid or oversized payloads stay plain text, so a
// single damaged row cannot prevent a phone from advancing its page cursor.
func mobileChatStructuredItem(view *mobileChatItemView, row storage.StreamItemRow) {
	view.Report = nil
	if len(row.ToolInput) > mobileChatPageBudget {
		return
	}
	switch row.ToolName {
	case mainChatProactiveTool:
		var item mainChatProactive
		if err := json.Unmarshal([]byte(row.ToolInput), &item); err != nil ||
			item.MomentID == "" || item.Kind == "" || item.Headline == "" || item.State == "" {
			return
		}
		normalizeMainChatProactiveTargets(&item)
		populateMainChatProactiveCopyForRead(&item, nil)
		view.Kind, view.Proactive = "proactive", &item
	case mainChatStatusTool:
		var status mainChatStatus
		if err := json.Unmarshal([]byte(row.ToolInput), &status); err != nil || status.Period == "" || len(status.Entries) == 0 {
			return
		}
		for i := range status.Entries {
			populateMainChatStatusCopy(&status.Entries[i])
			if status.Entries[i].Kind == "capture_paused" {
				status.Entries[i].Target = mainChatTarget{Kind: "settings", ID: "activity"}
			}
		}
		view.Kind, view.Status = "status", &status
	case mainChatReportTool:
		var report mainChatReport
		if err := json.Unmarshal([]byte(row.ToolInput), &report); err != nil || report.Source != mainChatSourceMessageSend ||
			(report.Channel != "mail" && report.Channel != "slack") || report.SentAt <= 0 {
			return
		}
		view.Report = &report
	default:
		return
	}
	if mobileChatItemCost(*view) > mobileChatPageBudget {
		view.Kind, view.Proactive, view.Status, view.Report = "text", nil, nil, nil
	}
}

// mobileChatItemCost is the item's size inside the sealed response: the body is
// JSON, and the sealed plaintext escapes that JSON again.
func mobileChatItemCost(view mobileChatItemView) int {
	raw, err := json.Marshal(view)
	if err != nil {
		return 0
	}
	escaped, err := json.Marshal(string(raw))
	if err != nil {
		return 0
	}
	return len(escaped) + 1
}

type mobileChatPage struct {
	Items   []mobileChatItemView
	Cursor  int64
	HasMore bool
}

// mobileChatPageFromRows projects rows (oldest first) into a page within the
// byte budget. The newest page keeps the newest rows; an after page keeps the
// oldest rows after the cursor. Cursor is the seq of the last row consumed,
// including rows that are not shown, so the phone never re-reads them.
func mobileChatPageFromRows(rows []storage.StreamItemRow, hasMore, newest bool, after int64) mobileChatPage {
	return mobileChatPageWithView(rows, hasMore, newest, after, nil)
}

// Enrichment runs before measuring costs, including the envelope escape layer.
func mobileChatPageWithView(rows []storage.StreamItemRow, hasMore, newest bool, after int64, enrich func(*mobileChatItemView), languages ...string) mobileChatPage {
	language := "en"
	if len(languages) > 0 {
		language = languages[0]
	}
	page := mobileChatPage{Cursor: after, HasMore: hasMore}
	if newest {
		page.Cursor = 0
	}
	views := make([]mobileChatItemView, len(rows))
	keep := make([]bool, len(rows))
	costs := make([]int, len(rows))
	for i, row := range rows {
		views[i], keep[i] = mobileChatItemFromRow(row)
		if keep[i] {
			if enrich != nil {
				enrich(&views[i])
				if mobileChatItemCost(views[i]) > mobileChatPageBudget {
					views[i].Kind, views[i].Proactive, views[i].Status, views[i].Report = "text", nil, nil, nil
				}
			}
			views[i] = displayModelCopy(views[i], language)
			costs[i] = mobileChatItemCost(views[i])
		}
	}
	mobileChatMarkAskedAt(views, keep)
	if len(rows) > 0 && newest {
		page.Cursor = int64(rows[len(rows)-1].SortOrder) + 1
	}
	if newest {
		used, first := 0, len(rows)
		for i := len(rows) - 1; i >= 0; i-- {
			if keep[i] {
				if used+costs[i] > mobileChatPageBudget {
					page.HasMore = true
					break
				}
				used += costs[i]
			}
			first = i
		}
		for i := first; i < len(rows); i++ {
			if keep[i] {
				page.Items = append(page.Items, views[i])
			}
		}
		return page
	}
	used := 0
	for i, row := range rows {
		seq := int64(row.SortOrder) + 1
		if !keep[i] {
			page.Cursor = seq
			continue
		}
		if used+costs[i] > mobileChatPageBudget {
			page.HasMore = true
			break
		}
		used += costs[i]
		page.Items = append(page.Items, views[i])
		page.Cursor = seq
	}
	return page
}

type mobileChatSummaryView struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	UpdatedAt string `json:"updatedAt"`
	State     string `json:"state"`
	Kind      string `json:"kind"`
}

// handoffChatIDs maps execution chat ids to their handoffs, so the phone can
// mark those chats as handoffs.
func (bridge *mobileBridge) handoffChatIDs() map[string]bool {
	ids := map[string]bool{}
	if bridge.app.handoffStore == nil {
		return ids
	}
	entries, err := bridge.app.handoffStore.List()
	if err != nil {
		return ids
	}
	for _, entry := range entries {
		if entry.ChatSessionID != "" {
			ids[entry.ChatSessionID] = true
		}
	}
	return ids
}

func (bridge *mobileBridge) serveChatList(w http.ResponseWriter, rawLimit string) {
	limit, ok := mobileParseLimit(rawLimit, mobileChatListDefault, mobileChatListMax)
	if !ok {
		mobileJSON(w, http.StatusBadRequest, nil, "limit must be between 1 and 50")
		return
	}
	store := bridge.app.storeForMobile()
	if store == nil {
		mobileJSON(w, http.StatusServiceUnavailable, nil, errMobileChatNotReady.Error())
		return
	}
	rows, err := store.MobileChatList(bridge.ctx, limit)
	if err != nil {
		log.Printf("[mobile-chat] list failed: %v", err)
		mobileJSON(w, http.StatusInternalServerError, nil, "the chats could not be read")
		return
	}
	rows = mobileChatListMainFirst(bridge.ctx, store, rows)
	handoffs := bridge.handoffChatIDs()
	chats := make([]mobileChatSummaryView, 0, len(rows))
	for _, row := range rows {
		chats = append(chats, bridge.chatSummary(row, handoffs))
	}
	mobileJSON(w, http.StatusOK, map[string]any{"chats": chats}, "")
}

// mobileChatListMainFirst puts the main chat at the head of the list, fetching
// it when recency pushed it past the limit, so the phone's Chat tab can open
// it first (PROTOCOL.md 15.4).
func mobileChatListMainFirst(ctx context.Context, store *storage.Store, rows []storage.MobileChatRow) []storage.MobileChatRow {
	for i, row := range rows {
		if row.Kind == storage.SessionKindMain {
			if i == 0 {
				return rows
			}
			out := make([]storage.MobileChatRow, 0, len(rows))
			out = append(out, row)
			out = append(out, rows[:i]...)
			return append(out, rows[i+1:]...)
		}
	}
	main, err := store.LoadMainSession(ctx)
	if err != nil || main == nil || main.Hidden {
		return rows
	}
	row, found, err := store.MobileChat(ctx, main.LocalID)
	if err != nil || !found {
		return rows
	}
	return append([]storage.MobileChatRow{row}, rows...)
}

func (bridge *mobileBridge) chatSummary(row storage.MobileChatRow, handoffs map[string]bool) mobileChatSummaryView {
	kind := mobileChatKindChat
	switch row.Kind {
	case storage.SessionKindMain:
		kind = mobileChatKindMain
	case storage.SessionKindSide:
		kind = mobileChatKindSide
	default:
		if handoffs[row.ID] {
			kind = mobileChatKindHandoff
		}
	}
	return mobileChatSummaryView{
		ID: row.ID, Title: mobileChatClip(pii.DisplayModelText(row.Label, bridge.app.modelDisplayLanguage()), mobileChatTitleMax),
		UpdatedAt: time.UnixMilli(row.UpdatedAt).UTC().Format(time.RFC3339),
		State:     bridge.chatListState(row.ID), Kind: kind,
	}
}

// mobileMainChatView reuses C1's classification and derived-tag read checks,
// intersecting tags with A8's bounded phone list. A failed/racing optional read
// withholds tags, never the conversation. The caller holds release through JSON.
func (a *App) mobileMainChatView(ctx context.Context, sessionID string, rows []storage.StreamItemRow) (func(*mobileChatItemView), func()) {
	if len(rows) == 0 {
		return nil, func() {}
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	now := time.Now()
	items := make([]StreamItemData, len(rows))
	ids := make([]string, 0, len(rows))
	for i, row := range rows {
		// Keep the already-projected payload for the shared classifier to
		// overlay mute/withdrawal state onto canonical lifecycle and expiry.
		items[i] = StreamItemData{ID: row.ID, Kind: row.Kind, ToolInput: row.ToolInput,
			RowClass: string(mainChatRowClass(row, now)),
			LocalDay: time.UnixMilli(row.CreatedAt).In(time.Local).Format("2006-01-02")}
		if row.Kind == "assistant" {
			ids = append(ids, row.ID)
		}
	}
	var chips map[string][]string
	if len(ids) > 0 {
		chips, _ = a.ListMainChatAnswerChips(ids)
	}
	allowed := map[string]bool{}
	if !a.IsClosedEyesModeEnabled() && a.topicSource != nil {
		// Match serveTopicList's source gates, publication fence and size trim.
		before, beforeErr := a.topicsSvc().captureRegistryRevision(ctx)
		topics, listErr := a.todayTopicViews()
		revision, revisionErr := a.captureTopicPageRevision(ctx)
		if beforeErr == nil && listErr == nil && revisionErr == nil && reflect.DeepEqual(before, revision.sources) {
			rowRevision, rowErr := a.computeMainChatView(ctx, sessionID, items, now)
			if rowErr == nil && (rowRevision == nil || reflect.DeepEqual(*rowRevision, revision)) {
				phoneTopics := []mobileTopicView{}
				for _, topic := range mobileBounded(topics, topicRegistryLimit) {
					phoneTopics = append(phoneTopics, mobileTopicProjection(topic))
				}
				for !mobileTopicResponseFits(map[string]any{"topics": phoneTopics}) && len(phoneTopics) > 0 {
					phoneTopics = phoneTopics[:len(phoneTopics)-1]
				}
				if unlock, err := a.validateTopicPageRevision(ctx, revision); err == nil {
					for _, topic := range phoneTopics {
						allowed[topic.ID] = true
					}
					return mobileMainChatEnricher(items, chips, allowed), unlock
				}
			}
		}
	}
	if len(chips) == 0 {
		return mobileMainChatEnricher(items, nil, allowed), func() {}
	}
	// Even without a topic snapshot, fence C2's Closed Eyes gate at publication.
	a.closedEyes.mu.Lock()
	if a.IsClosedEyesModeEnabled() {
		chips = nil
	}
	return mobileMainChatEnricher(items, chips, allowed), a.closedEyes.mu.Unlock
}

func mobileMainChatEnricher(items []StreamItemData, chips map[string][]string, allowed map[string]bool) func(*mobileChatItemView) {
	metadata := make(map[string]StreamItemData, len(items))
	for _, item := range items {
		metadata[item.ID] = item
	}
	return func(view *mobileChatItemView) {
		item := metadata[view.ID]
		view.RowClass, view.LocalDay = item.RowClass, item.LocalDay
		for _, id := range item.TopicIDs {
			if allowed[id] {
				view.TopicIDs = append(view.TopicIDs, id)
			}
		}
		if view.Role == "assistant" {
			for _, chip := range mobileBounded(chips[view.ID], 2) {
				// C2 stores bounded chips; also bound legacy/corrupt stored data.
				if text := string([]rune(chip)[:min(60, utf8.RuneCountInString(chip))]); text != "" {
					view.Chips = append(view.Chips, text)
				}
			}
		}
	}
}

type mobileChatDetailView struct {
	Chat         mobileChatSummaryView `json:"chat"`
	Items        []mobileChatItemView  `json:"items"`
	Cursor       int64                 `json:"cursor"`
	HasMore      bool                  `json:"hasMore"`
	WaitingCount *int                  `json:"waitingCount,omitempty"`
	Live         *mobileChatLiveAnswer `json:"live,omitempty"`
}

type mobileChatLiveAnswer struct {
	TurnID string `json:"turnId"`
	Text   string `json:"text"`
}

func (bridge *mobileBridge) serveChatDetail(w http.ResponseWriter, r *http.Request, device mobileDeviceRecord, chatID string) {
	// Resolve copy before mobileMainChatView reserves the database connection
	// for publication. No store reads may run while that fence is held.
	language := bridge.app.modelDisplayLanguage()
	bridge.chat.clearChatAnswers(chatID)
	bridge.app.retryWorkPermissionWrites()
	if !mobileValidID(chatID) {
		mobileJSON(w, http.StatusBadRequest, nil, "the chat id is not valid")
		return
	}
	limit, ok := mobileParseLimit(r.URL.Query().Get("limit"), mobileChatItemsDefault, mobileChatItemsMax)
	if !ok {
		mobileJSON(w, http.StatusBadRequest, nil, "limit must be between 1 and 50")
		return
	}
	afterRaw := r.URL.Query().Get("after")
	var after int64
	if afterRaw != "" {
		parsed, err := strconv.ParseInt(afterRaw, 10, 64)
		if err != nil || parsed < 0 {
			mobileJSON(w, http.StatusBadRequest, nil, "after must be a sequence number")
			return
		}
		after = parsed
	}
	store := bridge.app.storeForMobile()
	if store == nil {
		mobileJSON(w, http.StatusServiceUnavailable, nil, errMobileChatNotReady.Error())
		return
	}
	row, found, err := store.MobileChat(bridge.ctx, chatID)
	if err != nil {
		log.Printf("[mobile-chat] read failed: %v", err)
		mobileJSON(w, http.StatusInternalServerError, nil, "the chat could not be read")
		return
	}
	if !found {
		mobileJSON(w, http.StatusNotFound, nil, "this chat is no longer on your computer")
		return
	}
	var rows []storage.StreamItemRow
	var hasMore bool
	if afterRaw == "" {
		page, err := store.LoadStreamItemsLatest(bridge.ctx, chatID, limit)
		if err != nil {
			mobileJSON(w, http.StatusInternalServerError, nil, "the chat could not be read")
			return
		}
		rows, hasMore = page.Items, page.HasMore
	} else {
		// Sequence numbers are sort order plus one, so "after" is an exclusive bound
		// and zero means the start of the chat.
		page, err := store.LoadStreamItemsAfter(bridge.ctx, chatID, int(after-1), limit)
		if err != nil {
			mobileJSON(w, http.StatusInternalServerError, nil, "the chat could not be read")
			return
		}
		rows, hasMore = page.Items, page.HasMore
	}
	// A page can start after the source-bound turn that tainted its replies.
	// Resolve dependencies against the complete conversation before projecting
	// this page, keeping hidden rows' positions so polling still advances.
	history, err := store.LoadStreamItems(r.Context(), chatID)
	if err != nil {
		mobileJSON(w, http.StatusInternalServerError, nil, "the chat could not be read")
		return
	}
	readable := bridge.app.mobileOfferHistoryRows(r.Context(), history)
	rows, historyChanged := mobileChatSourcePage(rows, history, readable)
	var live *mobileChatLiveAnswer
	if !historyChanged && mobileHasScope(device.Scopes, mobileScopeBriefRead) {
		live = bridge.mobileBriefLiveAnswer(r.Context(), chatID, readable)
	}
	for i := range rows {
		row := &rows[i]
		if row.ToolName == mainChatProactiveTool {
			var item mainChatProactive
			if json.Unmarshal([]byte(row.ToolInput), &item) == nil && item.Kind == "first_read" &&
				(r.URL.Query().Get("firstRead") != "1" || item.MomentID != firstReadMoment(device.MobileDeviceView)) {
				*row = storage.StreamItemRow{ID: row.ID, SortOrder: row.SortOrder}
				continue
			}
		}
		row.Content, row.ToolInput = bridge.app.projectProactiveVoiceDelivery(bridge.ctx, row.ToolName, row.Content, row.ToolInput)
		if row.ToolName == mainChatProactiveTool {
			var item mainChatProactive
			if json.Unmarshal([]byte(row.ToolInput), &item) == nil {
				if item.Kind == "thread_update_unavailable" && item.Headline == "" {
					*row = storage.StreamItemRow{ID: row.ID, SortOrder: row.SortOrder}
					continue
				}
				if item.Kind == "first_read" && !mobileHasScope(device.Scopes, mobileScopeOffersRead) {
					item.OfferCount = 0
					if raw, err := json.Marshal(item); err == nil {
						row.ToolInput = string(raw)
					}
				}
			}
		}
		row.ToolInput = bridge.app.projectMainChatEngagementPrefs(row.ToolName, row.ToolInput)
	}
	// Resolve chat-state readers before acquiring source publication fences.
	summary := bridge.chatSummary(row, bridge.handoffChatIDs())
	var permissionItem *mobileChatItemView
	if !historyChanged {
		if permission, ok := bridge.chatPermissionView(chatID); ok {
			maxOrder, err := store.GetMaxSortOrder(bridge.ctx, chatID)
			if seq := int64(maxOrder) + 2; err == nil && seq > after {
				item := permission(seq)
				permissionItem = &item
			}
		}
	}
	var enrich func(*mobileChatItemView)
	var waitingCount *int
	if row.Kind == "main" {
		waitingCount = bridge.app.mobileChatWaitingCount(chatID)
		var release func()
		enrich, release = bridge.app.mobileMainChatView(r.Context(), chatID, rows)
		defer release()
	}
	page := mobileChatPageWithView(rows, hasMore, afterRaw == "", after, enrich, language)
	if !page.HasMore && permissionItem != nil {
		page.Items = append(page.Items, *permissionItem)
	}
	items := page.Items
	if items == nil {
		items = []mobileChatItemView{}
	}
	mobileJSON(w, http.StatusOK, mobileChatDetailView{
		Chat: summary, Items: items,
		Cursor: page.Cursor, HasMore: page.HasMore, WaitingCount: waitingCount, Live: live,
	}, "")
}

// The waiting badge is optional; a slow or unavailable resync must not block
// the transcript. A stale, unready or different chat's count is unknown.
func (a *App) mobileChatWaitingCount(chatID string) *int {
	holder := &a.mainChatWaiting
	holder.mu.Lock()
	defer holder.mu.Unlock()
	state := holder.state
	if !state.Ready || holder.running || holder.dirty || holder.retryTimer != nil || state.SessionID != chatID || state.Day != mainChatDayStart(time.Now()).Format("2006-01-02") {
		return nil
	}
	count := state.Count
	return &count
}

// mobileChatSourcePage preserves page sequence numbers for withheld rows. A
// permission request is also withheld when its conversation context changed.
func mobileChatSourcePage(page, history, readable []storage.StreamItemRow) ([]storage.StreamItemRow, bool) {
	byID := make(map[string]storage.StreamItemRow, len(readable))
	changed := len(history) != len(readable)
	for i, row := range readable {
		byID[row.ID] = row
		if i < len(history) && (row.Content != history[i].Content || row.ToolInput != history[i].ToolInput) {
			changed = true
		}
	}
	projected := make([]storage.StreamItemRow, len(page))
	for i, row := range page {
		if visible, ok := byID[row.ID]; ok {
			projected[i] = visible
		} else {
			projected[i] = storage.StreamItemRow{ID: row.ID, SortOrder: row.SortOrder}
		}
	}
	return projected, changed
}

// chatPermissionView returns a builder for the transient permission item, which
// takes the seq one past the newest stored item.
func (bridge *mobileBridge) chatPermissionView(chatID string) (func(seq int64) mobileChatItemView, bool) {
	bridge.chat.mu.Lock()
	requestID := bridge.chat.byChat[chatID]
	request := bridge.chat.pending[requestID]
	bridge.chat.mu.Unlock()
	if request == nil {
		return nil, false
	}
	return func(seq int64) mobileChatItemView {
		return mobileChatItemView{
			Seq: seq, Role: "system", Kind: "permission_request", Text: request.Text, ToolName: request.ToolName,
			At:         time.Now().UTC().Format(time.RFC3339),
			Permission: &mobileChatPermissionView{RequestID: request.RequestID, Options: request.Options},
		}
	}, true
}

// serveChatCreate makes an empty chat the way the desktop does: a session in the
// standalone chat project.
func (bridge *mobileBridge) serveChatCreate(w http.ResponseWriter, r *http.Request, device mobileDeviceRecord) {
	var input struct {
		IdempotencyKey string `json:"idempotencyKey"`
		Title          string `json:"title"`
	}
	if !mobileDecode(w, r, &input) {
		return
	}
	key, valid := mobileKey(device.DeviceID, input.IdempotencyKey)
	if !valid {
		mobileJSON(w, http.StatusBadRequest, nil, "an idempotency key of 1 to 128 bytes is required")
		return
	}
	title := strings.TrimSpace(input.Title)
	if len(title) > mobileChatTitleInputMax {
		mobileJSON(w, http.StatusBadRequest, nil, "the title must be at most 200 bytes")
		return
	}
	if title == "" {
		title = "New chat"
	}
	bridge.runReceipted(w, device, key, r.Method+" "+r.URL.Path, func(w http.ResponseWriter) {
		app := bridge.app
		store := app.storeForMobile()
		if store == nil {
			mobileJSON(w, http.StatusServiceUnavailable, nil, errMobileChatNotReady.Error())
			return
		}
		projectID, workspaceID, err := app.standaloneChatProjectAndWorkspace(bridge.ctx)
		if err != nil {
			log.Printf("[mobile-chat] standalone project failed: %v", err)
			mobileJSON(w, http.StatusInternalServerError, nil, "the chat could not be created")
			return
		}
		id := uuid.NewString()
		if err := store.SaveSession(bridge.ctx, storage.SessionRow{
			LocalID: id, ProjectID: projectID, WorkspaceID: workspaceID,
			Label: mobileChatClip(title, mobileChatTitleMax), Model: app.chatTaskModel(),
		}); err != nil {
			log.Printf("[mobile-chat] create failed: %v", err)
			mobileJSON(w, http.StatusInternalServerError, nil, "the chat could not be created")
			return
		}
		app.notifySpotlightSessionChanged(id)
		mobileJSON(w, http.StatusCreated, map[string]string{"id": id}, "")
	})
}

type mobileChatMessageInput struct {
	IdempotencyKey  string                 `json:"idempotencyKey"`
	Text            string                 `json:"text"`
	OfferReferences []mobileOfferReference `json:"offerReferences,omitempty"`
	// QueuedAt is when the phone queued a message that waited for the computer.
	QueuedAt string `json:"queuedAt,omitempty"`
	// Undone counts the waiting messages the phone took back since its last message.
	Undone int `json:"undone,omitempty"`
}

// mobileChatPhoneOriginToolInput marks a user row sent from the phone, so the computer's Chat can say "From your
// iPhone" (design, #635). It lives in ToolInput, not AutomationContext: AutomationContext marks automation and
// keeps a user row out of the proactive voice sources.
const mobileChatPhoneOriginToolInput = `{"origin":"phone"}`

// mobileMainChatAlias names the computer's main chat in the message route when the phone does not list it yet
// (PROTOCOL.md 15.5a). Chat IDs are never this value: they are generated UUIDs.
const mobileMainChatAlias = "main"

func (bridge *mobileBridge) serveChatMessage(w http.ResponseWriter, r *http.Request, device mobileDeviceRecord, chatID string) {
	if !mobileValidID(chatID) {
		mobileJSON(w, http.StatusBadRequest, nil, "the chat id is not valid")
		return
	}
	var input mobileChatMessageInput
	if !mobileDecode(w, r, &input) {
		return
	}
	key, valid := mobileKey(device.DeviceID, input.IdempotencyKey)
	if !valid {
		mobileJSON(w, http.StatusBadRequest, nil, "an idempotency key of 1 to 128 bytes is required")
		return
	}
	text := strings.TrimSpace(input.Text)
	if text == "" || len(input.Text) > mobileChatInputMax {
		mobileJSON(w, http.StatusBadRequest, nil, "the message must be 1 to 16384 bytes")
		return
	}
	if !mobileOfferReferencesValid(input.OfferReferences) {
		mobileJSON(w, http.StatusBadRequest, nil, "the idea references are not valid")
		return
	}
	queuedAt, valid := mobileChatQueuedAt(input.QueuedAt, time.Now())
	if !valid || input.Undone < 0 || input.Undone > mobileChatUndoneMax {
		mobileJSON(w, http.StatusBadRequest, nil, "the waiting fields are not valid")
		return
	}
	if mobileRouteFamily(r.Method, strings.TrimPrefix(r.URL.Path, mobileAPIPrefix)) == "POST /chats/*/offer-messages" && len(input.OfferReferences) == 0 {
		mobileJSON(w, http.StatusBadRequest, nil, "an idea reference is required")
		return
	}
	if len(input.OfferReferences) != 0 && !mobileRequireScope(w, device, mobileScopeOffersRead) {
		return
	}
	bridge.runReceipted(w, device, key, r.Method+" "+r.URL.Path, func(w http.ResponseWriter) {
		store := bridge.app.storeForMobile()
		if store == nil {
			mobileJSON(w, http.StatusServiceUnavailable, nil, errMobileChatNotReady.Error())
			return
		}
		var row storage.MobileChatRow
		var found bool
		var err error
		if chatID == mobileMainChatAlias {
			// A phone that has no main chat listed sends here; the computer makes sure the main chat
			// exists and takes the message (PROTOCOL.md 15.5a). The same scope and idempotency key apply.
			main, ensureErr := bridge.app.ensureMainChat(bridge.ctx)
			if ensureErr != nil {
				log.Printf("[mobile-chat] main chat unavailable: %v", ensureErr)
				mobileJSON(w, http.StatusServiceUnavailable, nil, "the main chat could not be prepared")
				return
			}
			row, found, err = store.MobileChat(bridge.ctx, main.LocalID)
		} else {
			row, found, err = store.MobileChat(bridge.ctx, chatID)
		}
		if err != nil {
			mobileJSON(w, http.StatusInternalServerError, nil, "the chat could not be read")
			return
		}
		if !found {
			mobileJSON(w, http.StatusNotFound, nil, "this chat is no longer on your computer")
			return
		}
		turnID, seq, err := bridge.startChatTurnWaited(row, text, input.OfferReferences, !queuedAt.IsZero())
		switch {
		case err == nil:
			bridge.recordPhoneChatWait(queuedAt, input.Undone)
			mobileJSON(w, http.StatusAccepted, map[string]any{"turnId": turnID, "seq": seq}, "")
		case errors.Is(err, errMobileChatBusy):
			mobileJSON(w, http.StatusConflict, nil, "busy")
		case errors.Is(err, errMobileChatWorkDir):
			mobileJSON(w, http.StatusConflict, nil, errMobileChatWorkDir.Error())
		case errors.Is(err, errMobileOfferUnavailable):
			mobileJSON(w, http.StatusConflict, nil, errMobileOfferUnavailable.Error())
		case errors.Is(err, errChatSourceAccessChanged):
			mobileJSON(w, http.StatusConflict, nil, errChatSourceAccessChanged.Error())
		case errors.Is(err, errMobileStoreUnavailable):
			mobileJSON(w, http.StatusServiceUnavailable, nil, errMobileStoreUnavailable.Error())
		default:
			log.Printf("[mobile-chat] send failed: %v", err)
			mobileJSON(w, http.StatusInternalServerError, nil, "the message could not be sent")
		}
	})
}

func (bridge *mobileBridge) serveChatCancel(w http.ResponseWriter, r *http.Request, device mobileDeviceRecord, chatID string) {
	if !mobileValidID(chatID) {
		mobileJSON(w, http.StatusBadRequest, nil, "the chat id is not valid")
		return
	}
	var input struct {
		IdempotencyKey string `json:"idempotencyKey"`
		TurnID         string `json:"turnId,omitempty"`
	}
	if !mobileDecode(w, r, &input) {
		return
	}
	key, valid := mobileKey(device.DeviceID, input.IdempotencyKey)
	if !valid {
		mobileJSON(w, http.StatusBadRequest, nil, "an idempotency key of 1 to 128 bytes is required")
		return
	}
	if input.TurnID != "" && !mobileValidID(input.TurnID) {
		mobileJSON(w, http.StatusBadRequest, nil, "the turn id is not valid")
		return
	}
	bridge.runReceipted(w, device, key, r.Method+" "+r.URL.Path, func(w http.ResponseWriter) {
		cancelled := false
		if input.TurnID == "" {
			cancelled = bridge.cancelChatTurn(chatID)
		} else {
			bridge.chat.mu.Lock()
			var cancel context.CancelFunc
			if reply := bridge.chat.replies[chatID]; reply != nil && reply.ask && reply.turnID == input.TurnID {
				cancel = reply.cancel
			}
			bridge.chat.mu.Unlock()
			if cancel != nil {
				cancel()
				cancelled = true
			}
		}
		mobileJSON(w, http.StatusOK, map[string]bool{"cancelled": cancelled}, "")
	})
}

func (bridge *mobileBridge) servePermissionAnswer(w http.ResponseWriter, r *http.Request, device mobileDeviceRecord, chatID, requestID string) {
	if !mobileValidID(chatID) || !mobileValidID(requestID) {
		mobileJSON(w, http.StatusBadRequest, nil, "the chat or request id is not valid")
		return
	}
	var input struct {
		IdempotencyKey string `json:"idempotencyKey"`
		OptionID       string `json:"optionId"`
	}
	if !mobileDecode(w, r, &input) {
		return
	}
	key, valid := mobileKey(device.DeviceID, input.IdempotencyKey)
	if !valid {
		mobileJSON(w, http.StatusBadRequest, nil, "an idempotency key of 1 to 128 bytes is required")
		return
	}
	optionID := strings.TrimSpace(input.OptionID)
	if optionID == "" {
		mobileJSON(w, http.StatusBadRequest, nil, "an option id is required")
		return
	}
	bridge.runReceipted(w, device, key, r.Method+" "+r.URL.Path, func(w http.ResponseWriter) {
		if bridge.serveMainChatWorkPermission(w, chatID, requestID, optionID) {
			return
		}
		bridge.chat.mu.Lock()
		request := bridge.chat.pending[requestID]
		bridge.chat.mu.Unlock()
		if request == nil || request.ChatID != chatID {
			mobileJSON(w, http.StatusNotFound, nil, "no such pending request")
			return
		}
		valid := false
		for _, option := range request.Options {
			valid = valid || option.ID == optionID
		}
		if !valid {
			mobileJSON(w, http.StatusBadRequest, nil, "the option is not offered for this request")
			return
		}
		// The work can request another permission while this handler is
		// responding. Its saved identity prevents answering that newer request.
		var err error
		if request.WorkRequestID != "" {
			err = bridge.app.respondToWorkPermission(request.AgentSessionID, optionID, request.WorkRequestID)
		} else {
			err = bridge.app.RespondToPermission(request.AgentSessionID, optionID)
		}
		if err != nil {
			mobileJSON(w, http.StatusConflict, nil, "this request is no longer pending")
			return
		}
		bridge.chat.mu.Lock()
		if bridge.chat.pending[requestID] == request {
			bridge.chat.clearPermissionLocked(chatID)
		}
		bridge.chat.mu.Unlock()
		mobileJSON(w, http.StatusOK, map[string]bool{"answered": true}, "")
	})
}

// Main-chat work cards use the existing permission route. The original work
// session remains available for history and its pending card shares the same
// runner response boundary.
func (bridge *mobileBridge) serveMainChatWorkPermission(w http.ResponseWriter, chatID, requestID, optionID string) bool {
	store := bridge.app.storeForMobile()
	if store == nil {
		return false
	}
	session, err := store.LoadSession(bridge.ctx, chatID)
	if err != nil || session == nil || session.Kind != storage.SessionKindMain {
		return false
	}
	row, err := store.FindProactiveStreamItem(bridge.ctx, chatID, requestID)
	if err != nil || row == nil {
		return false
	}
	var card mainChatProactive
	if json.Unmarshal([]byte(row.ToolInput), &card) != nil || card.Permission == nil || card.Permission.RequestID != requestID {
		return false
	}
	action := ""
	switch optionID {
	case card.Permission.ApproveOptionID:
		action = "approve"
	case card.Permission.DeclineOptionID:
		action = "decline"
	default:
		mobileJSON(w, http.StatusBadRequest, nil, "the option is not offered for this request")
		return true
	}
	state, err := bridge.app.resolveMainChatProactiveAction(requestID, action, engagement.ChannelPush, "", nil)
	if err != nil || state != "acted" {
		mobileJSON(w, http.StatusConflict, nil, "this request is no longer pending")
		return true
	}
	mobileJSON(w, http.StatusOK, map[string]bool{"answered": true}, "")
	return true
}

// serveProactiveCardAction exposes only the desktop's stored-card decision path.
// A client-supplied moment, destination or action is never execution authority.
func (bridge *mobileBridge) serveProactiveCardAction(w http.ResponseWriter, r *http.Request, device mobileDeviceRecord, chatID string) {
	if !mobileValidID(chatID) {
		mobileJSON(w, http.StatusBadRequest, nil, "the chat id is not valid")
		return
	}
	var input struct {
		IdempotencyKey string `json:"idempotencyKey"`
		MomentID       string `json:"momentId"`
		ActionID       string `json:"actionId"`
	}
	if !mobileDecode(w, r, &input) {
		return
	}
	// Stored work identities can contain paths, spaces and Unicode. Keep them
	// exact in JSON rather than requiring a URL-safe identity or reconstructing it.
	momentID := input.MomentID
	if strings.TrimSpace(momentID) == "" || len(momentID) > mobileChatPageBudget {
		mobileJSON(w, http.StatusBadRequest, nil, "a message id of 1 to 49152 bytes is required")
		return
	}
	key, valid := mobileKey(device.DeviceID, input.IdempotencyKey)
	if !valid || !mobileValidID(input.ActionID) {
		mobileJSON(w, http.StatusBadRequest, nil, "an idempotency key and action id of 1 to 128 bytes are required")
		return
	}
	// Notification preferences keep their dedicated route and push scope.
	if input.ActionID == engagement.ActionMuteKind {
		mobileJSON(w, http.StatusForbidden, nil, "this action is not available on this message")
		return
	}
	// Bind the key to the exact choice too; another button must never replay
	// the first button's receipt as if its own choice had been applied.
	choicePayload, _ := json.Marshal([]string{momentID, input.ActionID})
	route := r.Method + " " + r.URL.Path + " choice=" + agencyPayloadHash(choicePayload)
	bridge.runReceipted(w, device, key, route, func(w http.ResponseWriter) {
		store := bridge.app.storeForMobile()
		chat, found, err := store.MobileChat(bridge.ctx, chatID)
		if err != nil {
			mobileJSON(w, http.StatusInternalServerError, nil, "the chat could not be read")
			return
		}
		if !found || chat.Kind != storage.SessionKindMain {
			mobileJSON(w, http.StatusNotFound, nil, "this message is not in the chat")
			return
		}
		row, err := store.FindProactiveStreamItem(bridge.ctx, chatID, momentID)
		if err != nil {
			mobileJSON(w, http.StatusInternalServerError, nil, "the message could not be read")
			return
		}
		if row == nil {
			mobileJSON(w, http.StatusNotFound, nil, "this message is no longer on your computer")
			return
		}
		var item mainChatProactive
		if err := json.Unmarshal([]byte(row.ToolInput), &item); err != nil {
			mobileJSON(w, http.StatusBadRequest, nil, "this message is not available")
			return
		}
		card := &item
		if card.MomentID != momentID {
			card = nil
			for i := range item.Items {
				if item.Items[i].MomentID == momentID {
					card = &item.Items[i]
					break
				}
			}
		}
		allowed := false
		if card != nil {
			for _, action := range card.ActionIDs {
				allowed = allowed || action == input.ActionID
			}
		}
		if !allowed {
			mobileJSON(w, http.StatusBadRequest, nil, "this action is no longer offered on the message")
			return
		}
		// The resolver re-reads and validates the card under the same mutex as
		// desktop choices, so a concurrent choice cannot become our result.
		// Permission acceptance is independent of its chat presentation. Save
		// its exact success receipt at the runner boundary, before attempting
		// the card update. A receipt failure keeps this key retryable.
		body, _ := json.Marshal(mobileResponse{OK: true, Data: map[string]string{"state": "acted"}})
		permissionReceipt := &storage.MobileReceipt{DeviceID: device.DeviceID, RequestKey: key, Route: route,
			Status: http.StatusOK, Body: string(body), At: time.Now().UnixMilli()}
		state, err := bridge.app.resolveMainChatProactiveAction(momentID, input.ActionID, engagement.ChannelPush, chatID, permissionReceipt)
		if err != nil {
			if errors.Is(err, errMainChatPermissionReceiptPending) {
				mobileJSON(w, http.StatusServiceUnavailable, nil, "Isoform is saving this permission result. Retry this same action to check its result.")
				return
			}
			if errors.Is(err, errMainChatSendFinalize) || errors.Is(err, errMainChatSendUnknown) {
				mobileJSON(w, http.StatusServiceUnavailable, nil, "Isoform is checking this send. Retry this same action to check its result.")
				return
			}
			log.Printf("[mobile-chat] proactive action refused")
			mobileJSON(w, http.StatusConflict, nil, "Isoform could not apply this action. Refresh the chat and try again.")
			return
		}
		mobileJSON(w, http.StatusOK, map[string]string{"state": state}, "")
	})
}

// serveHandoffCreate starts one-time work from the phone. The entry is written
// directly to the handoff store, so it appears in One-time work on the Mac as waiting work.
func (bridge *mobileBridge) serveHandoffCreate(w http.ResponseWriter, r *http.Request, device mobileDeviceRecord) {
	var input struct {
		IdempotencyKey string `json:"idempotencyKey"`
		Title          string `json:"title"`
		Brief          string `json:"brief"`
		WorkDir        string `json:"workDir"`
		// Origin is where the phone started this work (20.7b); Macs that
		// advertise taskThreads accept it.
		Origin *struct {
			Kind  string `json:"kind"`
			Label string `json:"label,omitempty"`
		} `json:"origin,omitempty"`
	}
	if !mobileDecode(w, r, &input) {
		return
	}
	key, valid := mobileKey(device.DeviceID, input.IdempotencyKey)
	if !valid {
		mobileJSON(w, http.StatusBadRequest, nil, "an idempotency key of 1 to 128 bytes is required")
		return
	}
	var origin *handoff.Origin
	if input.Origin != nil {
		switch input.Origin.Kind {
		case handoff.OriginUpNext, handoff.OriginBriefChip, handoff.OriginChat, handoff.OriginList:
		default:
			mobileJSON(w, http.StatusBadRequest, nil, "unknown task origin")
			return
		}
		label := strings.TrimSpace(input.Origin.Label)
		if len(label) > mobileTaskSummaryMax || strings.ContainsAny(label, "\r\n") {
			mobileJSON(w, http.StatusBadRequest, nil, "the origin label must be one line of at most 200 bytes")
			return
		}
		origin = &handoff.Origin{Kind: input.Origin.Kind, Label: label}
	}
	validated, err := validateCreateTask(CreateTaskInput{Title: input.Title, Brief: input.Brief, WorkDir: input.WorkDir})
	if err != nil {
		mobileJSON(w, http.StatusBadRequest, nil, err.Error())
		return
	}
	bridge.runReceipted(w, device, key, r.Method+" "+r.URL.Path, func(w http.ResponseWriter) {
		app := bridge.app
		if app.handoffStore == nil {
			mobileJSON(w, http.StatusServiceUnavailable, nil, "the handoff store is not ready")
			return
		}
		saved, err := app.createTaskEntryFrom(validated, handoff.SourceMobile, origin)
		if err != nil {
			log.Printf("[mobile-chat] handoff save failed: %v", err)
			mobileJSON(w, http.StatusInternalServerError, nil, "the handoff could not be saved")
			return
		}
		mobileJSON(w, http.StatusCreated, map[string]string{"id": saved.ID}, "")
	})
}

// runReceipted runs a phone POST once per idempotency key (PROTOCOL.md 15.2).
// A repeat with the same route replays the stored answer. Only successful
// outcomes are stored, so a retry after a busy or failed answer runs again.
func (bridge *mobileBridge) runReceipted(w http.ResponseWriter, device mobileDeviceRecord, key, route string, action func(http.ResponseWriter)) {
	store := bridge.app.storeForMobile()
	if store == nil {
		mobileJSON(w, http.StatusServiceUnavailable, nil, errMobileChatNotReady.Error())
		return
	}
	receipt, found, err := store.MobileReceipt(bridge.ctx, key)
	if err != nil {
		log.Printf("[mobile-chat] receipt lookup failed: %v", err)
		mobileJSON(w, http.StatusInternalServerError, nil, "the request could not be checked")
		return
	}
	if found {
		if receipt.Route != route {
			mobileJSON(w, http.StatusConflict, nil, "this idempotency key was used for another request")
			return
		}
		bridge.app.reconcileMobileWorkPermissionReceipt(receipt)
		mobileReplay(w, receipt.Status, receipt.Body)
		return
	}
	captured := &mobileCapturedResponse{header: http.Header{}}
	action(captured)
	status := captured.status
	if status == 0 {
		status = http.StatusOK
	}
	body := strings.TrimSuffix(captured.body.String(), "\n")
	if status >= 200 && status < 300 {
		if err := store.SaveMobileReceipt(bridge.ctx, storage.MobileReceipt{
			DeviceID: device.DeviceID, RequestKey: key, Route: route,
			Status: status, Body: body, At: time.Now().UnixMilli(),
		}); err != nil {
			log.Printf("[mobile-chat] receipt save failed: %v", err)
		}
	}
	mobileReplay(w, status, body)
}

func mobileReplay(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body + "\n"))
}

// serveMemoryReject removes a memory the user says is wrong about them.
func (bridge *mobileBridge) serveMemoryReject(w http.ResponseWriter, r *http.Request, device mobileDeviceRecord, memoryID string) {
	if !mobileValidID(memoryID) {
		mobileJSON(w, http.StatusBadRequest, nil, "the memory id is not valid")
		return
	}
	var input struct {
		IdempotencyKey string `json:"idempotencyKey"`
		Reason         string `json:"reason"`
	}
	if !mobileDecode(w, r, &input) {
		return
	}
	key, valid := mobileKey(device.DeviceID, input.IdempotencyKey)
	if !valid {
		mobileJSON(w, http.StatusBadRequest, nil, "an idempotency key of 1 to 128 bytes is required")
		return
	}
	reason := strings.TrimSpace(input.Reason)
	if len(reason) > mobileCorrectionNoteMax {
		mobileJSON(w, http.StatusBadRequest, nil, "the reason is too long")
		return
	}
	bridge.runReceipted(w, device, key, r.Method+" "+r.URL.Path, func(w http.ResponseWriter) {
		err := bridge.rejectMemoryForPhone(memoryID, reason)
		if errors.Is(err, fs.ErrNotExist) {
			mobileJSON(w, http.StatusNotFound, nil, "this memory is no longer on your computer")
			return
		}
		mobileMutationResult(w, map[string]bool{"removed": err == nil}, err)
	})
}

// serveDirectiveRetract retracts a Self directive. The current Self entry is
// read on the Mac when the phone does not send one.
func (bridge *mobileBridge) serveDirectiveRetract(w http.ResponseWriter, r *http.Request, device mobileDeviceRecord, directiveID string) {
	if !mobileValidID(directiveID) {
		mobileJSON(w, http.StatusBadRequest, nil, "the directive id is not valid")
		return
	}
	var input struct {
		IdempotencyKey      string `json:"idempotencyKey"`
		ExpectedSelfEntryID string `json:"expectedSelfEntryId"`
		Reason              string `json:"reason"`
	}
	if !mobileDecode(w, r, &input) {
		return
	}
	key, valid := mobileKey(device.DeviceID, input.IdempotencyKey)
	if !valid {
		mobileJSON(w, http.StatusBadRequest, nil, "an idempotency key of 1 to 128 bytes is required")
		return
	}
	if len(input.Reason) > mobileCorrectionNoteMax {
		mobileJSON(w, http.StatusBadRequest, nil, "the reason is too long")
		return
	}
	bridge.runReceipted(w, device, key, r.Method+" "+r.URL.Path, func(w http.ResponseWriter) {
		app := bridge.app
		if !app.mobileStoreReady() {
			mobileMutationResult(w, nil, errMobileStoreUnavailable)
			return
		}
		expected := strings.TrimSpace(input.ExpectedSelfEntryID)
		if expected == "" {
			detail, err := app.GetSelfDirectiveDetail(directiveID)
			if err != nil {
				mobileMutationResult(w, nil, errMobileNotFound)
				return
			}
			expected = detail.Current.ID
		}
		reason := strings.TrimSpace(input.Reason)
		if reason == "" {
			reason = "Retracted on the phone."
		}
		_, err := app.RetractSelfDirective(RetractSelfDirectiveRequest{
			DirectiveID: directiveID, ExpectedSelfEntryID: expected, IdempotencyKey: key, Reason: reason,
		})
		mobileMutationResult(w, map[string]bool{"retracted": true}, err)
	})
}

// mobileParseLimit reads an optional limit in 1...max.
func mobileParseLimit(raw string, fallback, max int) (int, bool) {
	if raw == "" {
		return fallback, true
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil || parsed < 1 || parsed > max {
		return 0, false
	}
	return parsed, true
}

// storeForMobile returns the store the phone routes read and write, or nil.
func (a *App) storeForMobile() *storage.Store {
	if a == nil {
		return nil
	}
	return a.store
}

// registerChatListener attaches the chat state to the agent event stream.
func (bridge *mobileBridge) registerChatListener() {
	bridge.chat = newMobileChatState(time.Now)
	if bridge.app != nil {
		bridge.chat.detach = bridge.app.onAgentEvent(bridge.observeChatEvent)
	}
}

// mainChatRowIsBriefCard is a brief's card in the main chat: the brief ready,
// the week's recap, or the first week's brief.
func mainChatRowIsBriefCard(row storage.StreamItemRow) bool {
	if row.Kind != "system" || row.ToolName != mainChatProactiveTool {
		return false
	}
	var item mainChatProactive
	if json.Unmarshal([]byte(row.ToolInput), &item) != nil || item.BriefID == "" {
		return false
	}
	switch item.Kind {
	case string(engagement.KindBriefReady), string(engagement.KindWeeklyRecap), string(engagement.KindFirstWeek):
		return true
	}
	return false
}
