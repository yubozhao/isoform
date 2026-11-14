package main

import (
	"context"
	"log"
	"sync"
	"time"

	"wails-gui/storage"

	"github.com/google/uuid"
)

// chatCodeNoReply marks an error row the hub stored for a chat turn that
// ended with no answer: the run finished silently, or the app quit mid-turn.
// The chat shows it as one line with Send again.
const chatCodeNoReply = "no_reply"

// desktopTurnUnansweredWindow bounds the startup sweep: a message older than
// this is history, not a turn someone is waiting on.
const desktopTurnUnansweredWindow = 7 * 24 * time.Hour

// desktopTurnKeeper is the hub's copy of a desktop chat turn, the same copy
// the phone bridge keeps for phone turns (mobileChatReply). The window is the
// writer of a turn's rows; this copy is stored only when the window stored
// nothing by the time the turn settled (closed, reloaded, or never mounted).
// A turn that ends with nothing to show stores a coded error row, so a
// message never sits unanswered without a word.
type desktopTurnKeeper struct {
	mu      sync.Mutex
	replies map[string]*mobileChatReply
	once    sync.Once
}

// desktopTurnKept says whether a turn from this origin gets the hub's copy.
// Phone turns have the phone bridge's copy; panel asks keep their own answer;
// a resume has nobody waiting on it.
func desktopTurnKept(origin promptOrigin) bool {
	return origin == promptOriginMac
}

// beginDesktopTurn starts the copy of tag's turn. The user's message is the
// last stored row (or is about to be), so any row after the current last one
// that is not the user's means a window stored the turn.
func (a *App) beginDesktopTurn(tag string, origin promptOrigin) {
	if a == nil || a.store == nil || tag == "" || !desktopTurnKept(origin) {
		return
	}
	keeper := &a.desktopTurns
	keeper.once.Do(func() {
		keeper.mu.Lock()
		keeper.replies = make(map[string]*mobileChatReply)
		keeper.mu.Unlock()
		a.onAgentEvent(keeper.observe)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	last, err := a.store.GetMaxSortOrder(ctx, tag)
	if err != nil {
		log.Printf("[desktop-turn] could not read tag=%s before the turn: %v", tag, err)
		return
	}
	keeper.mu.Lock()
	keeper.replies[tag] = &mobileChatReply{turnID: uuid.NewString(), userOrder: last}
	keeper.mu.Unlock()
}

func (k *desktopTurnKeeper) observe(evt AgentEvent) {
	if evt.Tag == "" {
		return
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if reply := k.replies[evt.Tag]; reply != nil {
		reply.note(evt)
	}
}

// take ends tag's copy and returns it with its open text closed.
func (k *desktopTurnKeeper) take(tag string) *mobileChatReply {
	k.mu.Lock()
	defer k.mu.Unlock()
	reply := k.replies[tag]
	delete(k.replies, tag)
	if reply != nil {
		reply.flush()
	}
	return reply
}

// finishDesktopTurn settles tag's turn once the window has had time to store
// it. A run the person stopped with nothing to show stores nothing.
func (a *App) finishDesktopTurn(tag string, stopped bool) {
	if a == nil {
		return
	}
	reply := a.desktopTurns.take(tag)
	if reply == nil || stopped && len(reply.rows) == 0 {
		return
	}
	time.AfterFunc(mobileChatSettleDelay, func() { a.storeDesktopTurnIfMissing(tag, reply) })
}

// storeDesktopTurnIfMissing writes the hub's copy when no window stored any
// of the turn, or a no-reply error row when the turn had nothing at all.
func (a *App) storeDesktopTurnIfMissing(tag string, reply *mobileChatReply) {
	if a == nil || a.store == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	page, err := a.store.LoadStreamItemsLatest(ctx, tag, mobileChatReplyProbe)
	if err != nil {
		log.Printf("[desktop-turn] could not check tag=%s before storing the turn: %v", tag, err)
		return
	}
	if turnStoredAfter(page.Items, reply.userOrder) {
		return
	}
	rows := reply.rows
	if len(rows) == 0 {
		rows = []storage.StreamItemRow{noReplyRow()}
	}
	stored := 0
	for _, row := range rows {
		if _, err := a.store.AppendStreamItem(ctx, tag, row); err != nil {
			log.Printf("[desktop-turn] could not store a %s row for tag=%s: %v", row.Kind, tag, err)
			continue
		}
		stored++
	}
	if stored > 0 {
		a.mainChatRowsChanged(tag)
		a.notifySpotlightSessionChanged(tag)
	}
	log.Printf("[desktop-turn] no window stored the turn for tag=%s; the hub stored %d rows", tag, stored)
}

// turnStoredAfter reports whether a window stored any of the turn: a row
// after the user's message that is not a user row, private context count, or
// a main-chat card that landed mid-turn.
func turnStoredAfter(items []storage.StreamItemRow, userOrder int) bool {
	for _, item := range items {
		if item.SortOrder <= userOrder || item.Kind == "user" || item.Kind == "context_status" {
			continue
		}
		if item.Kind == "system" && (item.ToolName == mainChatReportTool || item.ToolName == mainChatSetupTool || item.ToolName == mainChatTextReplyTool || item.ToolName == mainChatProactiveTool) {
			continue
		}
		return true
	}
	return false
}

func noReplyRow() storage.StreamItemRow {
	return storage.StreamItemRow{
		ID: uuid.NewString(), Kind: "error", Content: "No answer came back.",
		ToolInput: chatErrorCauseJSON(chatCodeNoReply, ""),
	}
}

// markUnansweredTurns runs once at startup, before any turn can start: a
// chat whose last message is the person's own, typed on this computer in
// the last week, was cut off when the app quit. It gets a no-reply row, so
// the chat says so and offers Send again instead of sitting silent.
func (a *App) markUnansweredTurns(ctx context.Context) {
	if a == nil || a.store == nil {
		return
	}
	byProject, err := a.store.LoadAllSessions(ctx)
	if err != nil {
		log.Printf("[desktop-turn] startup sweep could not list chats: %v", err)
		return
	}
	cutoff := time.Now().Add(-desktopTurnUnansweredWindow).UnixMilli()
	marked := 0
	for _, sessions := range byProject {
		for _, session := range sessions {
			if a.markUnansweredTurn(ctx, session.LocalID, cutoff) {
				marked++
			}
		}
	}
	if marked > 0 {
		log.Printf("[desktop-turn] startup sweep marked %d unanswered messages", marked)
	}
}

func (a *App) markUnansweredTurn(ctx context.Context, tag string, cutoff int64) bool {
	// A turn that started since launch is not one the quit cut off.
	a.mu.Lock()
	active := a.activeTags[tag]
	a.mu.Unlock()
	if active {
		return false
	}
	page, err := a.store.LoadStreamItemsLatest(ctx, tag, 10)
	if err != nil {
		return false
	}
	last := lastTurnRow(page.Items)
	// A phone message may still be waiting for this computer to answer it.
	if last == nil || last.Kind != "user" || last.CreatedAt < cutoff || last.ToolInput == mobileChatPhoneOriginToolInput {
		return false
	}
	if _, err := a.store.AppendStreamItem(ctx, tag, noReplyRow()); err != nil {
		log.Printf("[desktop-turn] startup sweep could not mark tag=%s: %v", tag, err)
		return false
	}
	a.mainChatRowsChanged(tag)
	return true
}

// lastTurnRow is the newest row that belongs to the conversation, skipping
// private context counts and main-chat cards.
func lastTurnRow(items []storage.StreamItemRow) *storage.StreamItemRow {
	var last *storage.StreamItemRow
	for i := range items {
		item := &items[i]
		if item.Kind == "context_status" || item.Kind == "system" && item.ToolName != "" {
			continue
		}
		if last == nil || item.SortOrder > last.SortOrder {
			last = item
		}
	}
	return last
}
