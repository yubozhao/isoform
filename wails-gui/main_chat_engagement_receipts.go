package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"wails-gui/internal/engagement"
	"wails-gui/storage"
)

// Older stored cards and digest children gain the same local preference action
// as new cards, without reading another source or changing their authority.
func normalizeMainChatProactiveActions(item *mainChatProactive) {
	for i := range item.Items {
		normalizeMainChatProactiveActions(&item.Items[i])
	}
	if _, known := engagement.Spec(engagement.Kind(item.Kind)); !known || item.MomentID == "" {
		return
	}
	found := false
	for _, id := range item.ActionIDs {
		found = found || id == engagement.ActionMuteKind
	}
	if !found {
		item.ActionIDs = append(item.ActionIDs, engagement.ActionMuteKind)
	}
	if item.ActionLabels == nil {
		item.ActionLabels = map[string]string{}
	}
	lang := "en"
	if copy, _ := engagement.Copy(engagement.Kind(item.Kind), "zh-CN"); item.Headline == copy.PushBody {
		lang = "zh-CN"
	}
	if item.Kind == string(engagement.KindWaitingOnReply) {
		found := false
		for _, id := range item.ActionIDs {
			found = found || id == "not_now"
		}
		if !found {
			item.ActionIDs = append(item.ActionIDs, "not_now")
		}
		catalog, _ := engagement.Copy(engagement.KindWaitingOnReply, lang)
		item.ActionLabels["not_now"] = catalog.Actions["not_now"]
	}
	item.ActionLabels[engagement.ActionMuteKind] = engagement.MuteKindLabel(lang)
	if item.Kind == string(engagement.KindFirstWeek) {
		item.ActionLabels[engagement.ActionMuteKind] = engagement.NotForMeLabel(lang)
	}
	for lang, copy := range item.Localized {
		if copy.ActionLabels == nil {
			copy.ActionLabels = map[string]string{}
		}
		if item.Kind == string(engagement.KindWaitingOnReply) {
			catalog, _ := engagement.Copy(engagement.KindWaitingOnReply, lang)
			copy.ActionLabels["not_now"] = catalog.Actions["not_now"]
		}
		copy.ActionLabels[engagement.ActionMuteKind] = engagement.MuteKindLabel(lang)
		if item.Kind == string(engagement.KindFirstWeek) {
			copy.ActionLabels[engagement.ActionMuteKind] = engagement.NotForMeLabel(lang)
		}
		item.Localized[lang] = copy
	}
}

func mainChatProactiveChild(item *mainChatProactive, momentID string) *mainChatProactive {
	if item.MomentID == momentID {
		return item
	}
	for i := range item.Items {
		if child := mainChatProactiveChild(&item.Items[i], momentID); child != nil {
			return child
		}
	}
	return nil
}

// recordMainChatDelivery covers direct work and permission reports that do not
// pass through the pacing service. Existing moments keep their routing state.
// It runs only after durable chat storage, and retries repair a partial write.
func (a *App) recordMainChatDelivery(item mainChatProactive, at int64) error {
	if a == nil || a.store == nil || a.engagement == nil {
		return nil
	}
	for _, child := range item.Items {
		if err := a.recordMainChatDelivery(child, at); err != nil {
			return err
		}
	}
	if _, known := engagement.Spec(engagement.Kind(item.Kind)); !known || item.MomentID == "" {
		return nil
	}
	ctx := context.Background()
	momentID, err := a.mainChatReceiptMomentID(ctx, item.MomentID)
	if err != nil {
		return err
	}
	existing, err := a.store.GetEngagementMoment(ctx, momentID)
	if err != nil {
		return err
	}
	if existing != nil {
		return a.store.RecordEngagementChatSent(ctx, momentID, time.UnixMilli(at))
	}
	subjectID := item.Target.ID
	if item.Work != nil {
		subjectID = item.Work.HandoffID
	}
	if item.Permission != nil {
		subjectID = item.Permission.HandoffID
	}
	_, err = a.store.CreateEngagementMoment(ctx, storage.EngagementMomentRow{
		ID: momentID, Kind: item.Kind, SubjectID: subjectID,
		DedupeKey: "chat:" + item.MomentID, State: storage.EngagementStateSent,
		Channel: string(engagement.ChannelChat), CreatedAt: at, SentAt: at,
	})
	return err
}

// RecordMainChatProactiveReceipt observes a rendered computer card. Clients
// cannot claim acted: that state requires the linked item's completion time.
func (a *App) RecordMainChatProactiveReceipt(momentID, state string) error {
	if err := chatFeatureErr(); err != nil {
		return err
	}
	return a.recordMainChatProactiveReceipt(momentID, state, engagement.ChannelChat)
}

func (a *App) recordMainChatProactiveReceipt(momentID, state string, channel engagement.Channel) error {
	switch state {
	case storage.EngagementReceiptDelivered, storage.EngagementReceiptOpened:
	default:
		return errors.New("only delivered and opened card receipts are accepted")
	}
	if a == nil || a.store == nil || a.engagementService() == nil {
		return errEngagementUnavailable
	}
	ctx := context.Background()
	main, err := a.ensureMainChat(ctx)
	if err != nil {
		return err
	}
	card, createdAt, err := a.mainChatMeasuredItem(ctx, main.LocalID, momentID)
	if err != nil {
		return err
	}
	if err := a.recordMainChatDelivery(*card, createdAt); err != nil {
		return err
	}
	return a.recordMainChatCardReceipt(ctx, card, channel, state, "")
}

func (a *App) recordMainChatCardReceipt(ctx context.Context, card *mainChatProactive, channel engagement.Channel, state, action string) error {
	if a.engagement == nil {
		return nil
	}
	momentID, err := a.mainChatReceiptMomentID(ctx, card.MomentID)
	if err != nil {
		return err
	}
	return a.engagement.Receipt(ctx, momentID, string(channel), state, action, time.Time{})
}

// Notice ids must remain UUIDs. Direct chat cards retain their original action
// identity while producers and measurement share this deterministic notice id.
func mainChatEngagementMomentID(cardID string) string {
	if _, err := uuid.Parse(cardID); err == nil {
		return cardID
	}
	sum := sha256.Sum256([]byte("isoform:engagement:chat:" + cardID))
	var id uuid.UUID
	copy(id[:], sum[:16])
	// The existing phone contract accepts the v4/RFC4122 wire shape only.
	// This id is deterministic; the bits preserve that format, not randomness.
	id[6] = (id[6] & 0x0f) | 0x40
	id[8] = (id[8] & 0x3f) | 0x80
	return id.String()
}

func (a *App) mainChatReceiptMomentID(ctx context.Context, cardID string) (string, error) {
	// Preserve an already-persisted service identity, including older fixtures.
	row, err := a.store.GetEngagementMoment(ctx, cardID)
	if err != nil || row != nil {
		return cardID, err
	}
	mappedID := mainChatEngagementMomentID(cardID)
	mapped, err := a.store.GetEngagementMoment(ctx, mappedID)
	if err != nil || mapped != nil {
		return mappedID, err
	}
	// Old work notices used a random UUID while their cards used a lifecycle
	// identity. Reconnect that pair before introducing a new measurement.
	main, err := a.ensureMainChat(ctx)
	if err != nil {
		return "", err
	}
	stored, err := a.store.FindProactiveStreamItem(ctx, main.LocalID, cardID)
	if err != nil || stored == nil {
		return mappedID, err
	}
	var payload mainChatProactive
	if json.Unmarshal([]byte(stored.ToolInput), &payload) != nil {
		return mappedID, nil
	}
	card := mainChatProactiveChild(&payload, cardID)
	if card == nil || card.Work == nil {
		return mappedID, nil
	}
	kind := engagement.Kind(card.Kind)
	switch kind {
	case engagement.KindWorkFinished, engagement.KindWorkBlocked:
	default:
		return mappedID, nil
	}
	prefix := mainChatReportKey(mainChatSourceHandoff, card.Work.HandoffID, string(card.Work.Status)) + ":"
	if !strings.HasPrefix(cardID, prefix) {
		return mappedID, nil
	}
	transition := strings.TrimPrefix(cardID, prefix)
	separator := strings.LastIndex(transition, ":")
	if separator < 0 {
		return mappedID, nil
	}
	since, err := strconv.ParseInt(transition[separator+1:], 10, 64)
	if err != nil {
		return mappedID, nil
	}
	if since <= 0 {
		since = stored.CreatedAt - time.Minute.Milliseconds()
	}
	key := engagement.DedupeKey(kind, card.Work.HandoffID)
	if kind == engagement.KindWorkBlocked {
		key = engagement.DedupeKey(kind, card.Work.HandoffID, engagement.HashPart(transition[:separator]))
	}
	legacy, err := a.store.FindEngagementWorkMoment(ctx, card.Kind, card.Work.HandoffID, key, since, stored.CreatedAt+time.Second.Milliseconds())
	if err != nil || legacy == nil {
		return mappedID, err
	}
	return legacy.ID, nil
}

// A refreshed card follows the current Settings switch, including after Resume.
func (a *App) projectMainChatEngagementPrefs(tool, input string) string {
	if tool != mainChatProactiveTool || a.engagementService() == nil {
		return input
	}
	var item mainChatProactive
	if json.Unmarshal([]byte(input), &item) != nil {
		return input
	}
	prefs := a.engagement.Prefs()
	var project func(*mainChatProactive)
	project = func(card *mainChatProactive) {
		if spec, ok := engagement.Spec(engagement.Kind(card.Kind)); ok {
			card.KindMuted = engagementChatKindMuted(spec, prefs, a.engagement.Paused(spec.Kind), a.engagement.now())
		}
		for i := range card.Items {
			project(&card.Items[i])
		}
	}
	project(&item)
	payload, err := json.Marshal(item)
	if err != nil {
		return input
	}
	return string(payload)
}

// Folded routine messages retain a service moment link. Synthetic status entries
// (such as digest completion) are presentation of other sends, not extra sends.
func (a *App) mainChatMeasuredItem(ctx context.Context, chatID, itemID string) (*mainChatProactive, int64, error) {
	row, err := a.store.FindProactiveStreamItem(ctx, chatID, itemID)
	if err != nil {
		return nil, 0, err
	}
	if row != nil {
		var item mainChatProactive
		if json.Unmarshal([]byte(row.ToolInput), &item) != nil {
			return nil, 0, errors.New("proactive message is invalid")
		}
		card := mainChatProactiveChild(&item, itemID)
		if card == nil || card.Kind == "digest" {
			return nil, 0, errors.New("receipt must identify a proactive message")
		}
		return card, row.CreatedAt, nil
	}
	row, err = a.store.FindStatusStreamItem(ctx, chatID, itemID)
	if err != nil {
		return nil, 0, err
	}
	if row != nil {
		var status mainChatStatus
		if json.Unmarshal([]byte(row.ToolInput), &status) == nil {
			for _, entry := range status.Entries {
				if entry.ID == itemID && entry.MomentID != "" {
					sentAt := entry.SentAt
					if sentAt == 0 {
						sentAt = row.CreatedAt
					}
					return &mainChatProactive{MomentID: entry.MomentID, Kind: entry.Kind, Target: entry.Target}, sentAt, nil
				}
			}
		}
	}
	return nil, 0, errors.New("proactive message was not found")
}

// Repair from the committed payload, not the retry's possibly smaller batch.
// All dedupe paths use this so a failed child write keeps its first-send time.
func (a *App) repairMainChatDelivery(row storage.StreamItemRow) error {
	var item mainChatProactive
	if json.Unmarshal([]byte(row.ToolInput), &item) != nil {
		return errors.New("proactive message is invalid")
	}
	return a.recordMainChatDelivery(item, row.CreatedAt)
}
