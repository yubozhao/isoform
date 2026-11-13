package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"wails-gui/internal/engagement"
	persistencesvc "wails-gui/internal/persistence"
	"wails-gui/storage"
)

// mainChatWithheldKind marks a card that is not shown and whose source is not gone.
const mainChatWithheldKind = "withheld"

type MainChatRowClass string

const (
	MainChatRowConversation MainChatRowClass = "conversation"
	MainChatRowWaiting      MainChatRowClass = "waiting"
	MainChatRowFinished     MainChatRowClass = "finished"
	MainChatRowRoutine      MainChatRowClass = "routine"
	MainChatRowOther        MainChatRowClass = "other"
)

// mainChatRowClass is shared by all main-chat views, including the phone.
func mainChatRowClass(row storage.StreamItemRow, now time.Time) MainChatRowClass {
	switch row.Kind {
	case "user":
		return MainChatRowConversation
	case "assistant":
		if strings.TrimSpace(row.Content) != "" {
			return MainChatRowConversation
		}
	case "system":
		switch row.ToolName {
		case mainChatObservationPauseTool:
			var pause mainChatObservationPause
			if json.Unmarshal([]byte(row.ToolInput), &pause) == nil && (pause.EndAt > 0 || pause.ResumedUnknown) {
				return MainChatRowRoutine
			}
			return MainChatRowOther
		case mainChatStatusTool:
			return MainChatRowRoutine
		case mainChatReportTool:
			var report mainChatReport
			if json.Unmarshal([]byte(row.ToolInput), &report) == nil && mainChatReportIsRoutine(report) {
				return MainChatRowRoutine
			}
			return MainChatRowFinished
		case mainChatProactiveTool:
			var card mainChatProactive
			if json.Unmarshal([]byte(row.ToolInput), &card) != nil {
				return MainChatRowOther
			}
			return mainChatProactiveRowClass(card, now)
		}
	}
	return MainChatRowOther
}

// A digest is actionable only while a non-routine child needs an answer.
// Terminal envelope state and expiry still end the whole card's wait.
// A left-off observation is waiting only when it actually asks the person a question.
func mainChatLeftOffQuestion(card mainChatProactive) bool {
	if card.Kind != "decision_due" || card.Target.Kind != mainChatTargetThread {
		return true
	}
	if strings.ContainsAny(card.Headline+card.Body, "?？") {
		return true
	}
	for _, copy := range card.Localized {
		if strings.ContainsAny(copy.Headline+copy.Body, "?？") {
			return true
		}
	}
	return false
}

func mainChatProactiveRowClass(card mainChatProactive, now time.Time) MainChatRowClass {
	if card.Kind == mainChatWithheldKind {
		return MainChatRowRoutine
	}
	if !mainChatLeftOffQuestion(card) {
		return MainChatRowRoutine
	}
	if card.KindMuted && !mainChatProactiveNeedsUser(card) {
		if spec, known := engagement.Spec(engagement.Kind(card.Kind)); !known || !spec.Direct {
			return MainChatRowRoutine
		}
	}
	switch card.Kind {
	case "first_read":
		return MainChatRowOther
	case "brief_ready", "capture_paused", "thread_update_unavailable":
		return MainChatRowRoutine
	case "work_finished":
		return MainChatRowFinished
	case "meeting_prep":
		// Once the meeting has started the card is no longer an action: it
		// folds into that day's updates.
		if mainChatMeetingStarted(card, now) {
			return MainChatRowRoutine
		}
	}
	if (card.State != "pending" && card.State != "opened") || (card.ExpiresAt != 0 && card.ExpiresAt <= now.UnixMilli()) {
		return MainChatRowOther
	}
	if card.Kind == "digest" {
		allRoutine := len(card.Items) > 0
		for _, child := range card.Items {
			class := mainChatProactiveRowClass(child, now)
			if class == MainChatRowWaiting {
				return MainChatRowWaiting
			}
			allRoutine = allRoutine && class == MainChatRowRoutine
		}
		if allRoutine {
			return MainChatRowRoutine
		}
		return MainChatRowOther
	}
	return MainChatRowWaiting
}

// A thread update whose thread moved on can only say "unavailable"; the app
// does not show it (frontend isWithdrawnThreadUpdate), so it is not counted.
func mainChatWithdrawnThreadUpdate(input string) bool {
	var card mainChatProactive
	if json.Unmarshal([]byte(input), &card) != nil {
		return false
	}
	if card.Kind == mainChatWithheldKind {
		return true
	}
	if card.Kind == "digest" && len(card.Items) > 0 {
		for _, child := range card.Items {
			if child.Kind != "thread_update_unavailable" {
				return false
			}
		}
		return true
	}
	return card.Kind == "thread_update_unavailable"
}

// Notification preferences never resolve work or remove a pending decision.
func mainChatMeetingStarted(card mainChatProactive, now time.Time) bool {
	if card.Event == nil {
		return false
	}
	start, err := time.Parse(time.RFC3339, card.Event.Start)
	return err == nil && !start.After(now)
}

// A left-off card is for the day it is about. After the next morning brief
// (a brief built on a later day) it folds into that day's updates.
func mainChatLeftOffFolded(card mainChatProactive, createdAt int64, briefAt time.Time) bool {
	if card.Kind != string(engagement.KindDecisionDue) || card.Target.Kind != mainChatTargetThread || briefAt.IsZero() || mainChatLeftOffQuestion(card) {
		return false
	}
	at := time.UnixMilli(createdAt)
	if card.Place != nil {
		if left, err := time.Parse(time.RFC3339, card.Place.At); err == nil {
			at = left
		}
	}
	return briefAt.In(time.Local).Format("2006-01-02") > at.In(time.Local).Format("2006-01-02")
}

func mainChatProactiveNeedsUser(card mainChatProactive) bool {
	if card.Permission != nil {
		return true
	}
	switch engagement.Kind(card.Kind) {
	case engagement.KindDecisionDue, engagement.KindWorkBlocked, engagement.KindCommitmentSlipping, engagement.KindWaitingOnReply:
		return true
	default:
		return false
	}
}

func mainChatReportIsRoutine(report mainChatReport) bool {
	if report.Source != mainChatSourceSchedule {
		return false
	}
	if report.Routine != nil {
		return *report.Routine
	}
	// Only creation identifies an older API report unambiguously. Other
	// legacy statuses can be chat confirmations, so keep them visible.
	return report.Status == "created"
}

// Project each row once per overview/page read. Classification still overlays
// only display state, preserving canonical receipt state and legacy expiry.
func (a *App) projectMainChatRows(ctx context.Context, rows []storage.StreamItemRow) []StreamItemData {
	items := make([]StreamItemData, len(rows))
	for i, row := range rows {
		items[i] = persistencesvc.ToStreamItemData(row)
	}
	a.projectStreamItemsContext(ctx, items)
	return items
}

func mainChatRowClassForDisplay(row storage.StreamItemRow, input string, now time.Time) MainChatRowClass {
	if row.ToolName == mainChatObservationPauseTool && input != "" {
		row.ToolInput = input
		return mainChatRowClass(row, now)
	}
	if row.ToolName == mainChatProactiveTool {
		var card, display mainChatProactive
		if json.Unmarshal([]byte(row.ToolInput), &card) == nil && json.Unmarshal([]byte(input), &display) == nil {
			applyMainChatDisplayState(&card, display)
			return mainChatProactiveRowClass(card, now)
		}
	}
	// Persisted metadata carries canonical state and legacy expiry. Retain
	// that classification when display input is absent or cannot be decoded.
	return mainChatRowClass(row, now)
}

func applyMainChatDisplayState(card *mainChatProactive, display mainChatProactive) {
	card.KindMuted = display.KindMuted
	if display.Kind == "thread_update_unavailable" {
		card.Kind = display.Kind
	}
	for i := range card.Items {
		for _, child := range display.Items {
			if child.MomentID == card.Items[i].MomentID {
				applyMainChatDisplayState(&card.Items[i], child)
				break
			}
		}
	}
}

type MainChatOverview struct {
	SessionID      string   `json:"sessionId"`
	Day            string   `json:"day"`
	Conversations  int      `json:"conversations"`
	Waiting        int      `json:"waiting"`
	Finished       int      `json:"finished"`
	Routine        int      `json:"routine"`
	WaitingItemIDs []string `json:"waitingItemIds"`
	HideRoutine    bool     `json:"hideRoutine"`
}
type MainChatWaitingPage struct {
	SessionID  string           `json:"sessionId"`
	Items      []StreamItemData `json:"items"`
	HasMore    bool             `json:"hasMore"`
	NextCursor string           `json:"nextCursor"`
}
type MainChatWaitingCursorError struct{}

func (*MainChatWaitingCursorError) Error() string { return "MAIN_CHAT_WAITING_CURSOR_INVALID" }

type mainChatWaitingCursor struct {
	SessionID string `json:"sessionId"`
	At        int64  `json:"at"`
	ID        string `json:"id"`
}

// GetMainChatOverview reads current counts and the local view preference without
// creating a session or changing any chat row.
func (a *App) GetMainChatOverview() (perfValue MainChatOverview, perfErr error) {
	defer logPageBinding("GetMainChatOverview", time.Now(), &perfValue)
	return a.mainChatOverview(context.Background(), time.Now())
}
func (a *App) mainChatOverview(ctx context.Context, now time.Time) (MainChatOverview, error) {
	return a.mainChatOverviewRead(withMainChatReadMemo(ctx), now)
}

func (a *App) mainChatOverviewRead(ctx context.Context, now time.Time) (MainChatOverview, error) {
	now = now.In(time.Local)
	start := mainChatDayStart(now)
	out := MainChatOverview{Day: start.Format("2006-01-02"), WaitingItemIDs: []string{}}
	if a == nil || a.store == nil {
		return out, nil
	}
	snapshot, err := a.store.ReadMainChatView(ctx, start.UnixMilli())
	if err != nil {
		return out, err
	}
	out.SessionID, out.HideRoutine = snapshot.SessionID, snapshot.HideRoutine
	projected := a.projectMainChatRows(ctx, snapshot.Items)
	out.Waiting, out.WaitingItemIDs, _ = projectMainChatWaitingRows(snapshot.Items, projected, now, 50)
	out.Conversations, out.Finished, out.Routine = mainChatTodayCounts(snapshot.Items, projected, now)
	return out, nil
}

func mainChatDayStart(now time.Time) time.Time {
	local := now.In(time.Local)
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.Local)
}

func mainChatTodayCounts(rows []storage.StreamItemRow, projected []StreamItemData, now time.Time) (conversations, finished, routine int) {
	start := mainChatDayStart(now).UnixMilli()
	end := mainChatDayStart(now).AddDate(0, 0, 1).UnixMilli()
	for i, row := range rows {
		if row.CreatedAt < start || row.CreatedAt >= end {
			continue
		}
		toolInput := ""
		if i < len(projected) {
			toolInput = projected[i].ToolInput
		}
		class := mainChatRowClassForDisplay(row, toolInput, now)
		switch class {
		case MainChatRowConversation:
			if row.Kind == "user" {
				conversations++
			}
		case MainChatRowFinished:
			finished++
		case MainChatRowRoutine:
			if row.ToolName == mainChatStatusTool {
				var status mainChatStatus
				if json.Unmarshal([]byte(row.ToolInput), &status) == nil {
					routine += len(status.Entries)
				}
			} else if !mainChatWithdrawnThreadUpdate(toolInput) {
				routine++
			}
		}
	}
	return conversations, finished, routine
}

// ListMainChatWaitingItems reads a backwards page of actionable cards, ordered
// oldest first within the page; its exclusive cursor can be used to prepend.
func (a *App) ListMainChatWaitingItems(cursor string, limit int) (perfValue MainChatWaitingPage, perfErr error) {
	defer logPageBinding("ListMainChatWaitingItems", time.Now(), &perfValue)
	return derivedRead(a, derivedWaitingItems, fmt.Sprintf("%s|%d", cursor, limit), func() (MainChatWaitingPage, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return a.listMainChatWaitingItems(withMainChatReadMemo(ctx), cursor, limit)
	}, nil)
}

func (a *App) listMainChatWaitingItems(ctx context.Context, cursor string, limit int) (MainChatWaitingPage, error) {
	out := MainChatWaitingPage{Items: []StreamItemData{}}
	before := mainChatWaitingCursor{At: math.MaxInt64, ID: "~"}
	if len(cursor) > 6000 {
		return out, &MainChatWaitingCursorError{}
	}
	if cursor != "" {
		raw, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil || len(raw) > 4096 || json.Unmarshal(raw, &before) != nil || before.SessionID == "" || before.At < 0 || before.ID == "" {
			return out, &MainChatWaitingCursorError{}
		}
	}
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	if a == nil || a.store == nil {
		return out, nil
	}
	snapshot, err := a.store.ReadMainChatWaitingCandidates(ctx, before.At, before.ID, limit+1)
	if err != nil {
		return out, err
	}
	out.SessionID = snapshot.SessionID
	if cursor != "" && before.SessionID != snapshot.SessionID {
		return out, &MainChatWaitingCursorError{}
	}
	now := time.Now()
	candidates := snapshot.Items
	out.HasMore = len(candidates) > limit
	if out.HasMore {
		candidates = candidates[:limit]
	}
	projected := a.projectMainChatRows(ctx, candidates)
	var oldest storage.StreamItemRow
	for _, row := range candidates {
		oldest = row
	}
	_, _, out.Items = projectMainChatWaitingRows(candidates, projected, now, limit)
	if len(candidates) > 0 {
		raw, _ := json.Marshal(mainChatWaitingCursor{SessionID: out.SessionID, At: oldest.CreatedAt, ID: oldest.ID})
		out.NextCursor = base64.RawURLEncoding.EncodeToString(raw)
	}
	release, err := a.projectMainChatView(ctx, out.SessionID, out.Items, now)
	if err != nil {
		return MainChatWaitingPage{}, err
	}
	defer release()
	for i, j := 0, len(out.Items)-1; i < j; i, j = i+1, j-1 {
		out.Items[i], out.Items[j] = out.Items[j], out.Items[i]
	}
	return out, ctx.Err()
}

// SetMainChatHideRoutine persists a view preference; it never changes rows.
func (a *App) SetMainChatHideRoutine(hide bool) error {
	if a == nil || a.store == nil {
		return errTodayChatItemUnavailable
	}
	return a.store.SetUIState(context.Background(), "main_chat_hide_routine", strconv.FormatBool(hide))
}
