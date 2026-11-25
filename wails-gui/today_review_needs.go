package main

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"wails-gui/internal/handoff"
	"wails-gui/internal/pii"
)

// TodayNeed is a waiting item with the identity of the thing its action opens.
// Its sentence and source are display-only and never enter telemetry.
type TodayNeed struct {
	ID                string            `json:"id"`
	Kind              string            `json:"kind"`
	Sentence          string            `json:"sentence"`
	SentenceLocalized map[string]string `json:"sentenceLocalized,omitempty"`
	Source            string            `json:"source,omitempty"`
	CreatedAt         int64             `json:"createdAt"`
	DueAt             int64             `json:"dueAt,omitempty"`
	TargetKind        string            `json:"targetKind"`
	TargetID          string            `json:"targetId"`
	// StopCause says why a handoff stopped without finishing.
	StopCause string          `json:"stopCause,omitempty"`
	ChatID    string          `json:"chatId,omitempty"`
	Item      *StreamItemData `json:"item,omitempty"`
	// AsksKeep is the second return of an item put off twice: Needs you asks
	// Keep · Done · Forget.
	AsksKeep bool `json:"asksKeep,omitempty"`
	// PutOffUntil is when a put-off item comes back (unix ms).
	PutOffUntil int64 `json:"putOffUntil,omitempty"`
}

// orderTodayNeeds is shared by the Today preview and the full Tasks list.
// A recent call group comes first, then dated work by deadline, then the
// longest-waiting undated work. Equal keys keep their input order.
func orderTodayNeeds(items []TodayNeed, now time.Time) {
	recentCall := func(item TodayNeed) bool {
		return item.Kind == "call_promises" && item.CreatedAt > 0 &&
			item.CreatedAt <= now.UnixMilli() && item.CreatedAt >= now.Add(-2*time.Hour).UnixMilli()
	}
	sort.SliceStable(items, func(i, j int) bool {
		left, right := items[i], items[j]
		if recentCall(left) != recentCall(right) {
			return recentCall(left)
		}
		if (left.DueAt > 0) != (right.DueAt > 0) {
			return left.DueAt > 0
		}
		if left.DueAt > 0 && left.DueAt != right.DueAt {
			return left.DueAt < right.DueAt
		}
		if left.CreatedAt == 0 || right.CreatedAt == 0 {
			return left.CreatedAt != 0 && right.CreatedAt == 0
		}
		return left.CreatedAt < right.CreatedAt
	})
}

// TodayNeedsPage carries the complete count even when the caller requests a
// short preview. Callers must not infer the Tasks badge from the preview.
type TodayNeedsPage struct {
	Items []TodayNeed `json:"items"`
	Total int         `json:"total"`
	// PutOff is what Not now put away, shown as "Put off: {n}" at the foot.
	PutOff []TodayNeed `json:"putOff"`
	// NextReturnAt is when the next put-off item comes back (unix ms).
	NextReturnAt int64 `json:"nextReturnAt,omitempty"`
}

// GetNeedsYou returns the waiting-work projection. A zero limit requests the
// complete list; a positive limit bounds the preview without changing Total.
func (a *App) GetNeedsYou(limit int) (TodayNeedsPage, error) {
	result := TodayNeedsPage{Items: []TodayNeed{}, PutOff: []TodayNeed{}}
	handoffFor := map[string]string{}
	if a == nil {
		return result, nil
	}
	now := time.Now()
	var entries []handoff.Entry
	hiddenHandoffs := map[string]bool{}
	if a.handoffStore != nil {
		var err error
		entries, err = a.handoffStore.List()
		if err != nil {
			return result, err
		}
		for _, entry := range entries {
			if pii.IsSensitiveAppName(entry.AppName) {
				hiddenHandoffs[entry.ID] = true
			}
		}
	}
	seenHandoffs := map[string]bool{}
	if a.store != nil {
		cursor := ""
		for {
			page, err := a.readMainChatWaitingItems(cursor, 200)
			if err != nil {
				return TodayNeedsPage{}, err
			}
			for i := range page.Items {
				for _, projection := range todayNeedsFromChatItem(page.Items[i], page.SessionID, now) {
					need := projection.need
					if hiddenHandoffs[projection.handoffID] {
						continue
					}
					if err := a.todayNeedAddDeadline(context.Background(), &need); err != nil {
						return TodayNeedsPage{}, err
					}
					if projection.handoffID != "" {
						seenHandoffs[projection.handoffID] = true
						handoffFor[need.ID] = projection.handoffID
					}
					result.Items = append(result.Items, need)
				}
			}
			if !page.HasMore {
				break
			}
			if page.NextCursor == "" || page.NextCursor == cursor {
				return TodayNeedsPage{}, errors.New("waiting list cursor did not advance")
			}
			cursor = page.NextCursor
		}
	}
	for _, entry := range entries {
		if seenHandoffs[entry.ID] || hiddenHandoffs[entry.ID] {
			continue
		}
		title := mobileOfferText(entry.Title, mobileTaskTitleMax)
		if title == "" || !mobileValidID(entry.ID) {
			continue
		}
		kind := ""
		switch entry.Status {
		case handoff.StatusActive:
			kind = "decision_due"
		case handoff.StatusBlocked:
			kind = "work_blocked"
		case handoff.StatusNeedsReview:
			kind = "review"
		case handoff.StatusAborted:
			if group, _ := handoff.Placement(entry); group != handoff.BoardNeedsYou {
				continue
			}
			kind = "work_blocked"
		default:
			continue
		}
		_, cause := handoff.Placement(entry)
		result.Items = append(result.Items, TodayNeed{
			ID: entry.ID, Kind: kind, Sentence: title, Source: mobileOfferText(entry.AppName, 120),
			CreatedAt: entry.CreatedAt, TargetKind: "handoff", TargetID: entry.ID, StopCause: cause,
		})
	}
	result.Items, result.PutOff, result.NextReturnAt = a.partitionPutOff(result.Items, handoffFor, entries, now)
	orderTodayNeeds(result.Items, now)
	orderTodayNeeds(result.PutOff, now)
	result.Total = len(result.Items)
	if limit > 0 && limit < len(result.Items) {
		result.Items = result.Items[:limit]
	}
	return result, nil
}

type todayNeedProjection struct {
	need      TodayNeed
	handoffID string
}

// Keep the source's translated sentences so a language change can update both
// Today and Tasks without changing the identity of the underlying item.
func todayProactiveSentences(card mainChatProactive) map[string]string {
	var localized map[string]string
	for language, copy := range card.Localized {
		value := strings.TrimSpace(copy.Body)
		if value == "" {
			value = strings.TrimSpace(copy.Headline)
		}
		if value != "" {
			if localized == nil {
				localized = map[string]string{}
			}
			localized[language] = value
		}
	}
	return localized
}

// A digest may hold several waiting cards. Preserve the original row as the
// navigation target while projecting each card's own moment and state.
func todayNeedsFromChatItem(row StreamItemData, chatID string, now time.Time) []todayNeedProjection {
	var root mainChatProactive
	if json.Unmarshal([]byte(row.ToolInput), &root) != nil {
		return nil
	}
	if root.Kind != "digest" {
		need, handoffID, ok := todayNeedFromChatItem(row, chatID, now)
		if !ok {
			return nil
		}
		return []todayNeedProjection{{need, handoffID}}
	}
	var result []todayNeedProjection
	for _, card := range root.Items {
		raw, err := json.Marshal(card)
		if err != nil {
			continue
		}
		child := row
		child.ToolInput = string(raw)
		need, handoffID, ok := todayNeedFromChatItem(child, chatID, now)
		if ok {
			original := row
			need.Item = &original
			result = append(result, todayNeedProjection{need, handoffID})
		}
	}
	return result
}

// todayNeedFromChatItem uses the already privacy-projected waiting row; it
// does not derive authority from a title or reproduce source content.
func todayNeedFromChatItem(row StreamItemData, chatID string, now time.Time) (TodayNeed, string, bool) {
	if row.ToolName != mainChatProactiveTool || row.ID == "" || chatID == "" {
		return TodayNeed{}, "", false
	}
	var card mainChatProactive
	if json.Unmarshal([]byte(row.ToolInput), &card) != nil || card.MomentID == "" || !mobileTodayLiveCard(&card, now) {
		return TodayNeed{}, "", false
	}
	if !mainChatLeftOffQuestion(card) {
		return TodayNeed{}, "", false
	}
	switch card.Kind {
	case "waiting_on_reply", "decision_due", "work_blocked", "commitment_slipping":
	default:
		return TodayNeed{}, "", false
	}
	sentence := strings.TrimSpace(card.Body)
	if sentence == "" {
		sentence = strings.TrimSpace(card.Headline)
	}
	if sentence == "" {
		return TodayNeed{}, "", false
	}
	source := ""
	if card.Place != nil {
		source = card.Place.App
	}
	handoffID := ""
	if card.Work != nil {
		handoffID = card.Work.HandoffID
	}
	if card.Permission != nil && card.Permission.HandoffID != "" {
		handoffID = card.Permission.HandoffID
	}
	if card.Target.Kind == mainChatTargetHandoff && card.Target.ID != "" {
		handoffID = card.Target.ID
	}
	item := row
	return TodayNeed{ID: card.MomentID, Kind: card.Kind, Sentence: sentence, SentenceLocalized: todayProactiveSentences(card), Source: source, CreatedAt: row.CreatedAt,
		TargetKind: "chat_item", TargetID: row.ID, ChatID: chatID, Item: &item}, handoffID, true
}

// Read the authoritative engagement deadline, never the notice expiry. Older
// cards use an encoded moment ID or the receipt's mapped identity.
func (a *App) todayNeedAddDeadline(ctx context.Context, need *TodayNeed) error {
	if a == nil || a.store == nil || need == nil || need.ID == "" {
		return nil
	}
	row, err := a.store.GetEngagementMoment(ctx, need.ID)
	if err != nil {
		return err
	}
	if row == nil {
		row, err = a.store.GetEngagementMoment(ctx, mainChatEngagementMomentID(need.ID))
		if err != nil {
			return err
		}
	}
	if row == nil {
		id, err := a.mainChatReceiptMomentID(ctx, need.ID)
		if err != nil {
			return err
		}
		if id != "" {
			row, err = a.store.GetEngagementMoment(ctx, id)
			if err != nil {
				return err
			}
		}
	}
	if row != nil {
		need.DueAt = row.DueAt
	}
	return nil
}
