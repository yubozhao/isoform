package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"wails-gui/internal/engagement"
)

// Notification controls (rework plan, Phase 1): every proactive kind has a
// switch, a kind that paused itself after too many dismissals says so, and the
// counts of the stats window show what each kind earned. The Mac's Settings
// and the phone's Settings read the same view.

var errEngagementUnavailable = errors.New("notifications are not available yet")

// EngagementKindView is one proactive kind with its switch and its counts.
type EngagementKindView struct {
	Kind      string `json:"kind"`
	Enabled   bool   `json:"enabled"`
	DefaultOn bool   `json:"defaultOn"`
	// Paused is set when the kind paused itself after most deliveries were
	// dismissed or muted; turning it on again resumes it.
	Paused     bool   `json:"paused"`
	MutedUntil string `json:"mutedUntil,omitempty"`
	// QuietOnToday is set when the kind shows as a quiet line on Today instead
	// of interrupting. QuietAllowed and CanQuiet reflect explicit user choice;
	// QuietSuggestionAllowed separately governs Isoform's suggestions.
	QuietOnToday           bool                        `json:"quietOnToday"`
	DeliveryMode           engagement.KindDeliveryMode `json:"deliveryMode"`
	QuietAllowed           bool                        `json:"quietAllowed"`
	QuietSuggestionAllowed bool                        `json:"quietSuggestionAllowed"`
	Sent                   int                         `json:"sent"`
	Opened                 int                         `json:"opened"`
	Acted                  int                         `json:"acted"`
	Dismissed              int                         `json:"dismissed"`
	Muted                  int                         `json:"muted"`
	Channels               []string                    `json:"channels"`
	// Keep the legacy lifecycle counts above; rank and suggest from the
	// interruption receipt cohort, with quiet-line counts shown separately.
	NotificationSent          int `json:"notificationSent"`
	NotificationOpenedOrActed int `json:"notificationOpenedOrActed"`
	NotificationActed         int `json:"notificationActed"`
	NotificationUseful        int `json:"notificationUseful"`
	QuietSent                 int `json:"quietSent"`
	QuietOpenedOrActed        int `json:"quietOpenedOrActed"`
	// SampleCopyKey selects the latest notice's generic PushBody through
	// engagement.NotificationSampleCopy, including code-owned copy variants.
	// No personal parameters or source text are returned.
	SampleCopyKey string                     `json:"sampleCopyKey"`
	Quiet         bool                       `json:"quiet"`
	CanQuiet      bool                       `json:"canQuiet"`
	Tag           engagement.NotificationTag `json:"tag"`
}

// EngagementStatsSummary separates external notifications from quiet Today
// lines using actual receipts. Each bucket counts a moment once; a notice
// reached in both modes belongs to both buckets, regardless of current prefs.
type EngagementStatsSummary struct {
	Useful             int `json:"useful"`
	Sent               int `json:"sent"`
	OpenedOrActed      int `json:"openedOrActed"`
	QuietSent          int `json:"quietSent"`
	QuietOpenedOrActed int `json:"quietOpenedOrActed"`
}

// EngagementSettingsView is what the Mac and the phone show as notification
// settings. Quiet hours, the budget and the digest times are read-only here.
// Level is the dial position the switches and rules match: off, fewer,
// normal, more, or custom once one of them was changed by hand.
type EngagementSettingsView struct {
	Available            bool                   `json:"available"`
	Level                string                 `json:"level"`
	Kinds                []EngagementKindView   `json:"kinds"`
	QuietStart           string                 `json:"quietStart"`
	QuietEnd             string                 `json:"quietEnd"`
	DailyInterruptBudget int                    `json:"dailyInterruptBudget"`
	DigestTimes          []string               `json:"digestTimes"`
	StatsDays            int                    `json:"statsDays"`
	Summary              EngagementStatsSummary `json:"summary"`
}

// GetEngagementSettings returns the notification controls. Before the service
// exists (no store) the view says so instead of failing.
func (a *App) GetEngagementSettings() (perfValue EngagementSettingsView, perfErr error) {
	defer logPageBinding("GetEngagementSettings", time.Now(), &perfValue)
	svc := a.engagementService()
	if svc == nil {
		return EngagementSettingsView{Kinds: []EngagementKindView{}, DigestTimes: []string{}}, nil
	}
	return engagementSettingsView(context.Background(), svc)
}

// SetEngagementKindEnabled turns one kind on or off. Turning a kind on also
// ends its auto-pause and its mute.
func (a *App) SetEngagementKindEnabled(kind string, enabled bool) (EngagementSettingsView, error) {
	svc := a.engagementService()
	if svc == nil {
		return EngagementSettingsView{}, errEngagementUnavailable
	}
	k, ok := engagementKind(kind)
	if !ok {
		return EngagementSettingsView{}, fmt.Errorf("unknown notification kind %q", kind)
	}
	ctx := context.Background()
	if err := svc.SetKindEnabled(ctx, k, enabled); err != nil {
		return EngagementSettingsView{}, err
	}
	return engagementSettingsView(ctx, svc)
}

// SetEngagementKindQuiet sets whether one kind shows as a quiet line on Today
// instead of interrupting. A kind that needs the user's answer (a decision
// due, a permission request) refuses with engagement.ErrKindCannotBeQuiet.
func (a *App) SetEngagementKindQuiet(kind string, quiet bool) (EngagementSettingsView, error) {
	svc := a.engagementService()
	if svc == nil {
		return EngagementSettingsView{}, errEngagementUnavailable
	}
	k, ok := engagementKind(kind)
	if !ok {
		return EngagementSettingsView{}, errors.New("engagement_kind_unknown")
	}
	ctx := context.Background()
	if err := svc.SetKindQuiet(ctx, k, quiet); err != nil {
		return EngagementSettingsView{}, err
	}
	return engagementSettingsView(ctx, svc)
}

// SetEngagementLevel turns the one dial (off, fewer, normal, more). It
// rewrites every kind switch, the budget, the digest times and the quiet
// hours in one step; the phone reads the result through GET /notifications.
func (a *App) SetEngagementLevel(level string) (EngagementSettingsView, error) {
	svc := a.engagementService()
	if svc == nil {
		return EngagementSettingsView{}, errEngagementUnavailable
	}
	parsed, ok := engagement.ParseLevel(strings.TrimSpace(level))
	if !ok {
		return EngagementSettingsView{}, fmt.Errorf("unknown notification level %q", level)
	}
	ctx := context.Background()
	if err := svc.SetLevel(ctx, parsed); err != nil {
		return EngagementSettingsView{}, err
	}
	return engagementSettingsView(ctx, svc)
}

// EngagementWeekSummary is the weekly line on Today: "Isoform spoke up 9
// times this week, you acted on 6." Acted on counts completed linked items.
type EngagementWeekSummary struct {
	Available bool                 `json:"available"`
	Sent      int                  `json:"sent"`
	ActedOn   int                  `json:"actedOn"`
	Days      int                  `json:"days"`
	Kinds     []EngagementWeekKind `json:"kinds"`
}

// EngagementWeekKind counts distinct messages, not receipt retries or devices.
// ActRate uses sent messages as its denominator and is zero without sends.
type EngagementWeekKind struct {
	Kind      string  `json:"kind"`
	Created   int     `json:"created"`
	Scheduled int     `json:"scheduled"`
	Sent      int     `json:"sent"`
	Delivered int     `json:"delivered"`
	Opened    int     `json:"opened"`
	Acted     int     `json:"acted"`
	Dismissed int     `json:"dismissed"`
	Muted     int     `json:"muted"`
	ActRate   float64 `json:"actRate"`
}

// GetEngagementWeekSummary returns the counts behind the weekly line.
func (a *App) GetEngagementWeekSummary() (perfValue EngagementWeekSummary, perfErr error) {
	defer logPageBinding("GetEngagementWeekSummary", time.Now(), &perfValue)
	svc := a.engagementService()
	if svc == nil {
		return EngagementWeekSummary{Days: 7, Kinds: []EngagementWeekKind{}}, nil
	}
	now := svc.now()
	svc.inferCompletedReceipts(context.Background(), now)
	stats, err := svc.store.EngagementStatsBetween(context.Background(), now.Add(-7*24*time.Hour), now)
	if err != nil {
		return EngagementWeekSummary{}, err
	}
	notices, err := svc.store.EngagementNotificationsBetween(context.Background(), now.Add(-7*24*time.Hour), now)
	if err != nil {
		return EngagementWeekSummary{}, err
	}
	notified := map[string]int{}
	acted := map[string]int{}
	totalSent, totalActed := 0, 0
	for _, notice := range notices {
		if !engagementAutomaticNotification(engagement.Kind(notice.Kind)) {
			continue
		}
		notified[notice.Kind]++
		totalSent++
		if notice.Acted {
			acted[notice.Kind]++
			totalActed++
		}
	}
	out := EngagementWeekSummary{Available: true, Days: 7, Sent: totalSent, ActedOn: totalActed, Kinds: []EngagementWeekKind{}}
	for _, kind := range engagement.AllKinds() {
		if engagementKindSilent(kind) || !engagementAutomaticNotification(kind) {
			continue
		}
		stat := stats[string(kind)]
		row := EngagementWeekKind{Kind: string(kind), Created: stat.Created, Scheduled: stat.Scheduled,
			Sent: notified[string(kind)], Delivered: stat.Delivered, Opened: stat.Opened, Acted: acted[string(kind)],
			Dismissed: stat.Dismissed, Muted: stat.Muted}
		if row.Sent > 0 {
			row.ActRate = float64(row.Acted) / float64(row.Sent)
		}
		if kind != engagement.KindCapturePaused {
			out.Kinds = append(out.Kinds, row)
		}
	}
	return out, nil
}

// EngagementRulesInput is the editable part of the global rules, times as HH:MM.
type EngagementRulesInput struct {
	QuietStart           string `json:"quietStart"`
	QuietEnd             string `json:"quietEnd"`
	DailyInterruptBudget int    `json:"dailyInterruptBudget"`
}

// SetEngagementRules changes the quiet window and the daily budget. The same
// start and end means no quiet hours; a zero budget means no cap.
func (a *App) SetEngagementRules(input EngagementRulesInput) (EngagementSettingsView, error) {
	svc := a.engagementService()
	if svc == nil {
		return EngagementSettingsView{}, errEngagementUnavailable
	}
	start, ok := engagementParseClock(input.QuietStart)
	if !ok {
		return EngagementSettingsView{}, fmt.Errorf("quiet hours start %q is not a time of day", input.QuietStart)
	}
	end, ok := engagementParseClock(input.QuietEnd)
	if !ok {
		return EngagementSettingsView{}, fmt.Errorf("quiet hours end %q is not a time of day", input.QuietEnd)
	}
	ctx := context.Background()
	if err := svc.SetRules(ctx, engagement.QuietWindow{Start: start, End: end}, input.DailyInterruptBudget); err != nil {
		return EngagementSettingsView{}, err
	}
	return engagementSettingsView(ctx, svc)
}

// engagementParseClock reads HH:MM as an offset from local midnight.
func engagementParseClock(raw string) (time.Duration, bool) {
	parsed, err := time.Parse("15:04", strings.TrimSpace(raw))
	if err != nil {
		return 0, false
	}
	return time.Duration(parsed.Hour())*time.Hour + time.Duration(parsed.Minute())*time.Minute, true
}

func (a *App) engagementService() *engagementService {
	if a == nil || a.engagement == nil || a.engagement.store == nil {
		return nil
	}
	return a.engagement
}

// engagementKind parses a kind the Mac's UI or the phone sent.
func engagementKind(raw string) (engagement.Kind, bool) {
	kind := engagement.Kind(strings.TrimSpace(raw))
	if _, ok := engagement.Spec(kind); !ok {
		return "", false
	}
	return kind, true
}

// engagementSettingsView builds the shared view from the service's effective
// preferences and the stats window.
func engagementSettingsView(ctx context.Context, svc *engagementService) (EngagementSettingsView, error) {
	prefs := svc.Prefs()
	stats, err := svc.Stats(ctx)
	if err != nil {
		return EngagementSettingsView{}, err
	}
	now := svc.now()
	notices, err := svc.store.EngagementNotificationsBetween(ctx, now.Add(-engagementStatsWindow), now)
	if err != nil {
		return EngagementSettingsView{}, err
	}
	type notificationCounts struct{ sent, opened, acted, useful int }
	countedNotices := map[string]notificationCounts{}
	var summary EngagementStatsSummary
	for _, notice := range notices {
		if !engagementAutomaticNotification(engagement.Kind(notice.Kind)) {
			continue
		}
		counts := countedNotices[notice.Kind]
		counts.sent++
		summary.Sent++
		if notice.OpenedOrActed {
			counts.opened++
			summary.OpenedOrActed++
		}
		if notice.Acted {
			counts.acted++
		}
		if notice.Useful {
			counts.useful++
			summary.Useful++
		}
		countedNotices[notice.Kind] = counts
	}
	all := engagement.AllKinds()
	kinds := make([]EngagementKindView, 0, len(all)-len(engagementSilentKinds))
	for _, kind := range all {
		spec, ok := engagement.Spec(kind)
		if !ok || engagementKindSilent(kind) {
			continue
		}
		view := EngagementKindView{
			Kind:                   string(kind),
			Channels:               []string{},
			Quiet:                  prefs.KindQuietOnToday(kind),
			CanQuiet:               engagement.QuietOnTodayAllowed(kind),
			Enabled:                prefs.KindEnabled(spec),
			DefaultOn:              spec.DefaultOn,
			Paused:                 svc.Paused(kind),
			QuietOnToday:           prefs.KindQuietOnToday(kind),
			DeliveryMode:           prefs.KindDeliveryMode(kind),
			QuietAllowed:           engagement.QuietOnTodayAllowed(kind),
			QuietSuggestionAllowed: engagement.QuietSuggestionAllowed(kind),
		}
		if until, muted := prefs.MutedUntil[kind]; muted && until.After(now) {
			view.MutedUntil = until.UTC().Format(time.RFC3339)
		}
		if s, counted := stats[string(kind)]; counted {
			view.Sent, view.Opened, view.Acted, view.Dismissed, view.Muted = s.Sent, s.Opened, s.Acted, s.Dismissed, s.Muted
			notice := countedNotices[string(kind)]
			view.NotificationSent, view.NotificationOpenedOrActed, view.NotificationActed = notice.sent, notice.opened, notice.acted
			view.NotificationUseful = notice.useful
			view.QuietSent, view.QuietOpenedOrActed = s.QuietSent, s.QuietOpenedOrActed
			view.Channels = engagementNotificationChannels(s.Channels)
			if s.Sent > 0 {
				view.SampleCopyKey = engagement.NotificationSampleCopyKey(kind, s.LatestActivityThread)
			}
			summary.QuietSent += s.QuietSent
			summary.QuietOpenedOrActed += s.QuietOpenedOrActed
			view.Tag = engagement.NotificationStatsTag(kind, view.NotificationSent, view.NotificationOpenedOrActed, view.NotificationActed, view.Quiet)
		} else {
			notice := countedNotices[string(kind)]
			view.NotificationSent, view.NotificationOpenedOrActed, view.NotificationActed, view.NotificationUseful = notice.sent, notice.opened, notice.acted, notice.useful
			view.Tag = engagement.NotificationStatsTag(kind, notice.sent, notice.opened, notice.acted, view.Quiet)
		}
		if kind != engagement.KindCapturePaused {
			kinds = append(kinds, view)
		}
	}
	digests := make([]string, 0, len(prefs.DigestTimes))
	for _, offset := range prefs.DigestTimes {
		digests = append(digests, engagementClock(offset))
	}
	return EngagementSettingsView{
		Available:            true,
		Summary:              summary,
		Level:                string(engagement.LevelOf(prefs)),
		Kinds:                kinds,
		QuietStart:           engagementClock(prefs.Quiet.Start),
		QuietEnd:             engagementClock(prefs.Quiet.End),
		DailyInterruptBudget: prefs.DailyInterruptBudget,
		DigestTimes:          digests,
		StatsDays:            int(engagementStatsWindow / (24 * time.Hour)),
	}, nil
}

// engagementClock renders an offset from local midnight as HH:MM.
func engagementClock(offset time.Duration) string {
	minutes := int(offset.Minutes()) % (24 * 60)
	if minutes < 0 {
		minutes += 24 * 60
	}
	return fmt.Sprintf("%02d:%02d", minutes/60, minutes%60)
}

// serveNotificationSettings answers GET /notifications (PROTOCOL.md section 16).
func (bridge *mobileBridge) serveNotificationSettings(w http.ResponseWriter) {
	svc := bridge.app.engagementService()
	if svc == nil {
		mobileJSON(w, http.StatusServiceUnavailable, nil, errEngagementUnavailable.Error())
		return
	}
	view, err := engagementSettingsView(bridge.ctx, svc)
	if err != nil {
		mobileJSON(w, http.StatusInternalServerError, nil, "could not read the notification settings")
		return
	}
	mobileJSON(w, http.StatusOK, engagementPhoneSettingsView(view), "")
}

// engagementPhoneSettingsView keeps the phone's switches to the three kinds
// (phone-rings v1.0.0 17c: "Three kinds, and nothing else").
func engagementPhoneSettingsView(view EngagementSettingsView) EngagementSettingsView {
	byKind := map[string]EngagementKindView{}
	for _, kind := range view.Kinds {
		byKind[kind.Kind] = kind
	}
	kinds := []EngagementKindView{}
	for _, kind := range engagement.NotificationKinds() {
		if found, ok := byKind[string(kind)]; ok {
			kinds = append(kinds, found)
		}
	}
	view.Kinds = kinds
	return view
}

// serveNotificationKind answers POST /notifications/{kind}: one switch, replayed
// from the device's receipts like every other write.
func (bridge *mobileBridge) serveNotificationKind(w http.ResponseWriter, r *http.Request, device mobileDeviceRecord, raw string) {
	kind, ok := engagementKind(raw)
	if !ok {
		mobileJSON(w, http.StatusBadRequest, nil, "unknown notification kind")
		return
	}
	var input struct {
		IdempotencyKey string `json:"idempotencyKey"`
		Enabled        *bool  `json:"enabled"`
	}
	if !mobileDecode(w, r, &input) {
		return
	}
	key, valid := mobileKey(device.DeviceID, input.IdempotencyKey)
	if !valid {
		mobileJSON(w, http.StatusBadRequest, nil, "an idempotency key of 1 to 128 bytes is required")
		return
	}
	if input.Enabled == nil {
		mobileJSON(w, http.StatusBadRequest, nil, "enabled is required")
		return
	}
	svc := bridge.app.engagementService()
	if svc == nil {
		mobileJSON(w, http.StatusServiceUnavailable, nil, errEngagementUnavailable.Error())
		return
	}
	enabled := *input.Enabled
	bridge.runReceipted(w, device, key, r.Method+" "+r.URL.Path, func(w http.ResponseWriter) {
		if err := svc.SetKindEnabled(bridge.ctx, kind, enabled); err != nil {
			mobileJSON(w, http.StatusInternalServerError, nil, "could not save the notification setting")
			return
		}
		view, err := engagementSettingsView(bridge.ctx, svc)
		if err != nil {
			mobileJSON(w, http.StatusInternalServerError, nil, "could not read the notification settings")
			return
		}
		mobileJSON(w, http.StatusOK, view, "")
	})
}

// engagementNotificationChannels translates transport identifiers into the
// Settings contract, in stable order. Internal bookkeeping is never a channel.
func engagementNotificationChannels(receipts []string) []string {
	reached := map[string]bool{}
	for _, channel := range receipts {
		for _, part := range strings.Split(channel, "+") {
			reached[part] = true
		}
	}
	out := []string{}
	for _, pair := range [][2]string{{"desktop", "computer"}, {"push", "phone"}, {"text", "text"}, {"email", "email"}, {"today_quiet", "today_quiet"}} {
		if reached[pair[0]] {
			out = append(out, pair[1])
		}
	}
	return out
}
