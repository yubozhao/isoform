package main

import (
	"context"
	"encoding/json"
	"strings"

	"wails-gui/internal/engagement"
	"wails-gui/storage"
)

// briefChatBackfillKey records that every held-back brief has its Chat card (R36).
const briefChatBackfillKey = "brief-chat-cards-backfill-v1"

// firstWeekDiscoveryReason is the reason the first-week rule gave the brief_ready
// moments it held back. Those briefs never reached Chat.
const firstWeekDiscoveryReason = "first week discovery"

// backfillBriefChatCards gives every brief the first-week rule held back its
// Chat card, once. It posts the card only: it never pushes, never buzzes and
// never changes the moment's state. Each card keeps the time its brief was ready.
func (a *App) backfillBriefChatCards(ctx context.Context) {
	if chatFeatureErr() != nil {
		return
	}
	a.dedupeBriefChatCards(ctx)
	svc, ok := a.mobileBriefSource().(*mobileBriefService)
	if !ok || svc == nil || svc.store == nil {
		return
	}
	a.runBriefChatCardBackfill(ctx, svc.store.load)
}

// briefChatCardDedupeKey records that the saved brief cards hold one card per
// brief (R40). Earlier launches posted a card per moment, so one brief could
// have several.
const briefChatCardDedupeKey = "brief-chat-cards-dedupe-v1"

// dedupeBriefChatCards keeps the earliest Chat card of each brief and deletes
// the copies, once. It runs at engagement start and records completion only
// after the deletes, so a failed launch retries. A card without a brief id is
// left alone.
func (a *App) dedupeBriefChatCards(ctx context.Context) bool {
	if a == nil || a.store == nil {
		return false
	}
	done, err := a.store.GetUIState(ctx, briefChatCardDedupeKey)
	if err != nil {
		return false
	}
	if done == "true" {
		return true
	}
	main, err := a.ensureMainChat(ctx)
	if err != nil {
		return false
	}
	rows, err := a.store.ListProactiveBriefCards(ctx, main.LocalID)
	if err != nil {
		return false
	}
	// A card that carries text outranks an empty one; otherwise the earliest
	// stays. Nothing is deleted until every brief has its card chosen.
	keep := map[string]storage.StreamItemRow{}
	var drop []string
	for _, row := range rows {
		briefID := proactiveCardBriefID(row)
		if briefID == "" {
			continue
		}
		current, seen := keep[briefID]
		if !seen {
			keep[briefID] = row
			continue
		}
		if strings.TrimSpace(current.Content) == "" && strings.TrimSpace(row.Content) != "" {
			drop = append(drop, current.ID)
			keep[briefID] = row
			continue
		}
		drop = append(drop, row.ID)
	}
	for _, id := range drop {
		if err := a.store.DeleteStreamItemByID(ctx, id); err != nil {
			return false
		}
	}
	return a.store.SetUIState(ctx, briefChatCardDedupeKey, "true") == nil
}

// proactiveCardBriefID reads the brief a stored card belongs to, or "" when
// the payload has none.
func proactiveCardBriefID(row storage.StreamItemRow) string {
	var payload struct {
		BriefID string `json:"briefId"`
	}
	if json.Unmarshal([]byte(row.ToolInput), &payload) != nil {
		return ""
	}
	return payload.BriefID
}

// runBriefChatCardBackfill posts the missing cards and records completion only
// when every card is posted or has nothing to show. A failed card is retried on
// the next launch; posting is idempotent, so a card is never posted twice.
func (a *App) runBriefChatCardBackfill(ctx context.Context, briefFor func(string) (MobileBriefV1, bool)) bool {
	if a == nil || a.store == nil || a.engagement == nil {
		return false
	}
	done, err := a.store.GetUIState(ctx, briefChatBackfillKey)
	if err != nil {
		return false
	}
	if done == "true" {
		return true
	}
	rows, err := a.store.ListEngagementMoments(ctx, storage.EngagementStateSuppressed, 1000)
	if err != nil {
		return false
	}
	complete := true
	for _, row := range rows {
		if row.Kind != string(engagement.KindBriefReady) || row.Reason != firstWeekDiscoveryReason {
			continue
		}
		if !a.postBriefChatCard(momentFromRow(row), briefFor) {
			complete = false
		}
	}
	if complete {
		_ = a.store.SetUIState(ctx, briefChatBackfillKey, "true")
	}
	return complete
}

// postBriefChatCard posts one brief's Chat card at its ready time. The card holds
// only the moment's generic copy, never the brief's words. A brief with nothing
// written (quiet) or one no longer stored has no card. It reports whether the
// card is now posted or has nothing to show.
func (a *App) postBriefChatCard(m engagement.Moment, briefFor func(string) (MobileBriefV1, bool)) bool {
	brief, found := briefFor(m.SubjectID)
	if !found || brief.NarrativeStatus == "quiet" {
		return true
	}
	spec, known := engagement.Spec(m.Kind)
	if !known {
		return true
	}
	if engagementChatKindMuted(spec, a.engagement.Prefs(), a.engagement.Paused(m.Kind), a.engagement.now()) {
		return true
	}
	item, ok := a.proactiveItem(m, a.engagement.render(spec, m))
	if !ok {
		return false
	}
	item.postedAt = m.CreatedAt
	return a.postProactiveItem(item, engagementReportKey(m, m.CreatedAt)) != ""
}
