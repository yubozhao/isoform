package main

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"wails-gui/internal/engagement"
)

const threadOutcomeMomentPrefix = "thread-outcome:"

func threadOutcomeMoment(row ActivityRow, outcome *ActivityThreadOutcome, now time.Time) (engagement.Moment, bool) {
	if outcome == nil || outcome.NextStep == "" || outcome.Action == "" || outcome.Confidence < .8 {
		return engagement.Moment{}, false
	}
	// Reusing a permission kind must not bypass the proactive voice policy.
	if proactiveVoiceRestrictedText(strings.Join([]string{row.Title, row.Description, row.LatestChange, outcome.Summary, outcome.NextStep, outcome.Question}, " ")) {
		return engagement.Moment{}, false
	}
	if !threadOutcomeUnfinished(outcome) {
		return engagement.Moment{}, false
	}
	return engagement.Moment{Kind: engagement.KindDecisionDue, SubjectID: threadOutcomeMomentPrefix + row.ID,
		DedupeKey: engagement.DedupeKey(engagement.KindDecisionDue, threadOutcomeMomentPrefix+row.ID),
		CreatedAt: now, ExpiresAt: now.Add(24 * time.Hour), Stakes: .5,
		Params: map[string]string{"_activity_thread": row.ID, "_activity_outcome": threadOutcomeSourceID(row.ID, outcome)}}, true
}

func (a *App) produceThreadOutcome(ctx context.Context, row ActivityRow, outcome *ActivityThreadOutcome, now time.Time) {
	if outcome != nil && outcome.Kind == "conversation" {
		// A chat's ask reaches the brief only (messagingOutcomes), not a card.
		return
	}
	m, ok := threadOutcomeMoment(row, outcome, now)
	if !ok || !a.threadOutcomeSourceReadable(ctx, m.Params["_activity_outcome"]) {
		return
	}
	_ = a.submitEngagementSignal(ctx, m)
}

// Both submission and every pending delivery re-read the canonical outcome.
// Params cannot turn an old observation into a permission request or an action.
func (a *App) threadOutcomeMomentAllowsRead(ctx context.Context, m engagement.Moment) bool {
	if !strings.HasPrefix(m.SubjectID, threadOutcomeMomentPrefix) {
		return m.Params["_activity_thread"] == "" && m.Params["_activity_outcome"] == ""
	}
	id := strings.TrimPrefix(m.SubjectID, threadOutcomeMomentPrefix)
	if m.Kind != engagement.KindDecisionDue || m.Params["_activity_thread"] != id || !a.threadOutcomeSourceReadable(ctx, m.Params["_activity_outcome"]) {
		return false
	}
	row, err := a.activity.store.GetActivityByID(id)
	if err != nil || row == nil {
		return false
	}
	outcome := a.readableThreadOutcome(ctx, *row)
	fresh, ok := threadOutcomeMoment(*row, outcome, m.CreatedAt)
	return ok && fresh.Params["_activity_outcome"] == m.Params["_activity_outcome"] && m.Params["title"] == "" && m.Params["next_step"] == ""
}

// projectThreadOutcomeItem keeps a card whose outcome was refreshed after it was
// written (chat v1.0.12): the card keeps its place and time and shows the newer
// outcome, with UpdatedAt for the quiet "Updated" line. A card is withdrawn only
// when its source is gone (forgotten, private app or window) or held for privacy.
func (a *App) projectThreadOutcomeItem(ctx context.Context, item *mainChatProactive, lang string) bool {
	if item.ActivityOutcomeSource == "" {
		return false
	}
	if proactiveVoiceRestrictedText(item.Body) || a.IsClosedEyesModeEnabled() {
		withdrawThreadOutcomeItem(item, lang)
		return true
	}
	if a.threadOutcomeSourceReadable(ctx, item.ActivityOutcomeSource) {
		return true
	}
	rowID, rowTitle, outcome, refreshedAt, ok := a.refreshedThreadOutcome(ctx, item.ActivityOutcomeSource)
	if !ok {
		withdrawThreadOutcomeItem(item, lang)
		return true
	}
	applyRefreshedThreadOutcome(item, rowID, rowTitle, outcome, refreshedAt)
	return true
}

// refreshedThreadOutcome reads the thread's current outcome when the thread
// still exists and its outcome is readable (a refresh changed the outcome, so
// the card's source hash no longer matches).
func (a *App) refreshedThreadOutcome(ctx context.Context, sourceID string) (string, string, *ActivityThreadOutcome, int64, bool) {
	if a == nil || a.activity == nil || a.activity.store == nil {
		return "", "", nil, 0, false
	}
	at := strings.LastIndex(sourceID, ":")
	if at < 1 {
		return "", "", nil, 0, false
	}
	row, err := a.activity.store.GetActivityByID(strings.TrimPrefix(sourceID[:at], threadOutcomeMomentPrefix))
	if err != nil || row == nil {
		return "", "", nil, 0, false
	}
	outcome := a.readableThreadOutcome(ctx, *row)
	if outcome == nil {
		return "", "", nil, 0, false
	}
	var record activityThreadOutcomeRecord
	if json.Unmarshal([]byte(row.threadOutcomeRecord), &record) != nil {
		return "", "", nil, 0, false
	}
	return row.ID, row.Title, outcome, record.AttemptedAt, true
}

// applyRefreshedThreadOutcome shows the newer outcome on the same card. A
// refresh that says the thread is done (the person acted on it) marks the card done.
func applyRefreshedThreadOutcome(item *mainChatProactive, rowID, rowTitle string, outcome *ActivityThreadOutcome, refreshedAt int64) {
	body := strings.TrimSpace(outcome.Summary)
	if next := strings.TrimSpace(outcome.NextStep); next != "" {
		body = strings.TrimSpace(body + " Next: " + next)
	}
	if body == "" || proactiveVoiceRestrictedText(body) {
		withdrawThreadOutcomeItem(item, "")
		return
	}
	// Keep the card's own title in every language. Dropping the copy would let
	// the generic decision copy ("Work needs your permission.") take its place.
	title := strings.TrimSpace(item.Headline)
	if title == "" {
		title = strings.TrimSpace(rowTitle)
	}
	localized := make(map[string]mainChatProactiveCopy, len(engagement.Languages))
	for _, lang := range engagement.Languages {
		copy := item.Localized[lang]
		if strings.TrimSpace(copy.Headline) == "" {
			copy.Headline = title
		}
		copy.Body = body
		localized[lang] = copy
	}
	item.Localized = localized
	item.Body = body
	item.UpdatedAt = refreshedAt
	item.ActivityOutcomeSource = threadOutcomeSourceID(rowID, outcome)
	if outcome.State == "acted_on" {
		item.State = "done"
	}
}

// withdrawThreadOutcomeItem removes an outcome card from Chat: its source is gone or held.
func withdrawThreadOutcomeItem(item *mainChatProactive, lang string) {
	item.Kind = "thread_update_unavailable"
	item.Draft, item.DraftContent = false, nil
	item.VoiceVersion, item.VoiceReceipt = "", ""
	item.Work, item.Permission = nil, nil
	item.Evidence = nil
	item.ActionIDs = nil
	item.ActionLabels = nil
	item.Localized = map[string]mainChatProactiveCopy{
		"en":    {Headline: "Thread update unavailable.", Body: "This thread is no longer available."},
		"zh-CN": {Headline: "工作主线更新不可用。", Body: "这条工作主线已不可用。"},
	}
	copy := item.Localized[lang]
	if copy.Headline == "" {
		copy = item.Localized["en"]
	}
	item.Headline, item.Body = copy.Headline, copy.Body
	item.Target = mainChatTarget{Kind: "none"}
	// Nothing is left to answer. Keeping the pending state would let the phone label it "Waiting on you".
	item.State = "expired"
	item.ActivityOutcomeSource = ""
}

func (a *App) threadOutcomeCopyParams(ctx context.Context, m engagement.Moment) (map[string]string, bool) {
	if !a.threadOutcomeMomentAllowsRead(ctx, m) {
		return nil, false
	}
	row, err := a.activity.store.GetActivityByID(m.Params["_activity_thread"])
	if err != nil || row == nil {
		return nil, false
	}
	outcome := a.readableThreadOutcome(ctx, *row)
	if outcome == nil {
		return nil, false
	}
	params := map[string]string{"_activity_thread": row.ID, "title": outcome.Summary, "next_step": outcome.NextStep, "work": row.Title}
	if place := a.threadOutcomeCardPlace(ctx, *row); place != nil {
		params["app"] = place.App
		params["bundle_id"] = place.BundleID
		params["at"] = place.At
		if observedAt, err := time.Parse(time.RFC3339, place.At); err == nil {
			params["time"] = observedAt.Format("15:04")
		}
	}
	return params, true
}

func (a *App) threadOutcomeCardPlace(ctx context.Context, row ActivityRow) *mainChatProactivePlace {
	evidence, readable := a.threadOutcomeEvidence(ctx, row)
	if !readable || len(evidence) == 0 || evidence[0].ObservedAt <= 0 || a.activity == nil || a.activity.store == nil {
		return nil
	}
	run, err := a.activity.store.GetTimelineRunByIDContext(ctx, evidence[0].RunID)
	if err != nil || run == nil {
		return nil
	}
	app := strings.TrimSpace(run.AppName)
	bundleID := strings.TrimSpace(run.AppKey)
	if bundleID == "" || bundleID == "unknown" || strings.HasPrefix(bundleID, "app:") || !strings.Contains(bundleID, ".") {
		bundleID = ""
	}
	if bundleID == "" || app == "" {
		events, err := a.activity.store.LoadTimelineRunEvents(*run)
		if err == nil {
			eventApp, eventBundleID := threadOutcomeRunPlace(*run, events, evidence[0].ObservedAt)
			if app == "" {
				app = eventApp
			}
			if bundleID == "" {
				bundleID = eventBundleID
			}
		}
	}
	if app == "" {
		return nil
	}
	at := time.UnixMilli(evidence[0].ObservedAt).In(time.Local)
	return &mainChatProactivePlace{
		App:      app,
		BundleID: bundleID,
		At:       at.Format(time.RFC3339),
	}
}

func threadOutcomeRunPlace(run ActivityTimelineRunRow, events []ActivityTimelineEvent, observedAt int64) (string, string) {
	app := strings.TrimSpace(run.AppName)
	bundleID := strings.TrimSpace(run.AppKey)
	if bundleID == "" || bundleID == "unknown" || strings.HasPrefix(bundleID, "app:") || !strings.Contains(bundleID, ".") {
		bundleID = ""
	}
	if app != "" && bundleID != "" {
		return app, bundleID
	}
	closestDistance := int64(^uint64(0) >> 1)
	for _, event := range events {
		eventBundleID := strings.TrimSpace(event.AppBundleID)
		if eventBundleID == "" {
			continue
		}
		distance := event.Timestamp - observedAt
		if distance < 0 {
			distance = -distance
		}
		if distance >= closestDistance {
			continue
		}
		closestDistance = distance
		if strings.TrimSpace(run.AppName) == "" {
			app = strings.TrimSpace(event.AppName)
		}
		bundleID = eventBundleID
	}
	return app, bundleID
}

func (a *App) renderThreadOutcomeMoment(m engagement.Moment, lang string) (engagementText, bool) {
	params, ok := a.threadOutcomeCopyParams(context.Background(), m)
	if !ok {
		return engagementText{}, false
	}
	copy, _ := engagement.Copy(m.Kind, lang, params)
	return sanitizeEngagementText(engagementText{PushBody: copy.PushBody, Title: engagement.Render(copy.Sealed, params), Actions: map[string]string{"open": copy.Actions["open"]}, Category: engagementCategory(m.Kind)}), true
}
