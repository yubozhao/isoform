package main

import (
	"context"
	"encoding/json"
	"os"
	"sort"
	"time"

	"wails-gui/internal/engagement"
	"wails-gui/storage"
)

const firstWeekInstallKey = "engagement.first_week.install.v1"
const firstWeekPendingKey = "engagement.first_week.pending.v1"

// Only a fresh onboarding enrolls. Upgrades never restart a first week.
// These local milestones do not create an analytics identity or send telemetry.
func (a *App) initializeFirstWeek(ctx context.Context, now time.Time) {
	if !a.onboardingFinished(ctx) {
		_ = a.store.SetUIState(ctx, firstWeekPendingKey, "true")
	}
}

func (a *App) firstWeekInstall(ctx context.Context) time.Time {
	if a == nil || a.store == nil {
		return time.Time{}
	}
	raw, err := a.store.GetUIState(ctx, firstWeekInstallKey)
	if err != nil {
		return time.Time{}
	}
	at, _ := time.Parse(time.RFC3339Nano, raw)
	return at
}

func (a *App) noteFirstWeekOnboarding(ctx context.Context, raw string) error {
	var state struct {
		Completed      bool `json:"completed"`
		NeverShowAgain bool `json:"neverShowAgain"`
	}
	if json.Unmarshal([]byte(raw), &state) != nil || (!state.Completed && !state.NeverShowAgain) || !a.firstWeekInstall(ctx).IsZero() {
		return nil
	}
	pending, err := a.store.GetUIState(ctx, firstWeekPendingKey)
	if err != nil || pending != "true" || a.onboardingFinished(ctx) {
		return err
	}
	now := time.Now()
	if a.engagement != nil {
		now = a.engagement.now()
	}
	if err := a.store.SetUIState(ctx, firstWeekInstallKey, now.Format(time.RFC3339Nano)); err != nil {
		return err
	}
	a.scheduleFirstBrief(ctx, now)
	return nil
}

func (a *App) journeyEnabled(kind engagement.Kind, now time.Time) bool {
	if a == nil || a.engagement == nil {
		return false
	}
	prefs := a.engagement.Prefs()
	spec, _ := engagement.Spec(kind)
	return prefs.KindEnabled(spec) && !prefs.MutedUntil[kind].After(now)
}

func (a *App) checkEngagementJourney(ctx context.Context, now time.Time) {
	installed := a.firstWeekInstall(ctx)
	day := engagement.FirstWeekDay(installed, now)
	firstWeek := day >= 2 && day <= 7 && a.journeyEnabled(engagement.KindFirstWeek, now)
	a.engagement.mu.Lock()
	a.engagement.prefs.FirstWeek = firstWeek
	a.engagement.mu.Unlock()
	if !a.onboardingFinished(ctx) {
		return
	}
	a.firstBriefDue(ctx, now)
	// "Your week" rings with its Friday evening brief (produceYourWeekReady).
	if day < 0 || day > 7 || !a.journeyEnabled(engagement.KindFirstWeek, now) {
		return
	}
	key := engagement.DedupeKey(engagement.KindFirstWeek, installed.Format(time.RFC3339Nano), now.Format("2006-01-02"))
	if exists, err := a.store.EngagementMomentKeyExists(ctx, key); err != nil || exists {
		return
	}
	end := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, now.Location())
	m := engagement.Moment{Kind: engagement.KindFirstWeek, DedupeKey: key, CreatedAt: now, ExpiresAt: end,
		Params: map[string]string{}}
	switch day {
	case 0:
		m.SubjectID = "welcome"
		m.Params["stage"] = "welcome"
		m.Params["first_brief"] = a.firstWeekBriefTime(now)
		// A late onboarding welcome can wait through quiet hours into day 1.
		m.ExpiresAt = end.Add(12 * time.Hour)
	case 1:
		return // The first successful brief, never an empty timed reminder.
	default:
		// Friday reserves the discovery slot for "Your week".
		if now.Weekday() == time.Friday && a.journeyEnabled(engagement.KindWeeklyRecap, now) {
			return
		}
		if a.engagement.Prefs().Quiet.Contains(now) || a.IsClosedEyesModeEnabled() {
			return
		}
		candidate, ok := a.firstWeekCandidate(ctx, installed, now, day)
		if !ok {
			return
		}
		m.SubjectID, m.Params = candidate.SubjectID, candidate.Params
	}
	_ = a.submitEngagementSignal(ctx, m)
}

func (a *App) firstWeekBriefTime(now time.Time) string {
	settings := defaultMobileBriefSettings()
	if source := a.mobileBriefSource(); source != nil {
		if schedule, err := source.Schedule(); err == nil {
			settings.Enabled, settings.Morning = schedule.Enabled, schedule.Morning
		}
	}
	if !settings.Enabled {
		return "off"
	}
	hour, minute, _ := parseMobileBriefClock(settings.Morning)
	next := time.Date(now.Year(), now.Month(), now.Day()+1, hour, minute, 0, 0, now.Location())
	next = a.engagement.Prefs().Quiet.EndAfter(next)
	return next.Format("2006-01-02 15:04")
}

// Only identities leave these existing read gates, never transcripts, names,
// titles or mail subjects. Rotate source priority; no evidence means silence.
func (a *App) firstWeekCandidate(ctx context.Context, installed, now time.Time, day int) (engagement.Moment, bool) {
	for offset := 0; offset < 3; offset++ {
		switch (day - 2 + offset) % 3 {
		case 0:
			store, err := a.purposeProductStore()
			if err != nil {
				continue
			}
			details, err := store.ListDetailsAll()
			if err != nil {
				continue
			}
			sort.SliceStable(details, func(i, j int) bool { return details[i].OpenLoop.ID < details[j].OpenLoop.ID })
			for _, item := range details {
				if item.OpenLoop.Kind != PurposeKindCommitment || !mobileBriefPurposeCurrent(item) {
					continue
				}
				if _, allowed := a.proactiveVoicePurpose(ctx, item.OpenLoop.ID, now); allowed {
					return engagement.Moment{SubjectID: item.OpenLoop.ID, Params: map[string]string{"stage": "commitment"}}, true
				}
			}
		case 1:
			people, err := a.store.ListPrincipals(ctx)
			if err != nil {
				continue
			}
			for _, person := range people {
				if person.Kind != storage.PrincipalKindContact || !a.proactiveVoicePersonAllowed(ctx, person) {
					continue
				}
				last, err := a.store.PrincipalLastSeenAt(ctx, person.ID)
				if err == nil && last >= installed.UnixMilli() && last <= now.UnixMilli() {
					return engagement.Moment{SubjectID: person.ID, Params: map[string]string{"stage": "person"}}, true
				}
			}
		case 2:
			if a.activity == nil || a.activity.store == nil {
				continue
			}
			rows, err := a.activity.store.GetFilteredSessions(mobileBriefSessionLimit, 0, installed.UnixMilli(), now.UnixMilli(), "")
			if err != nil {
				continue
			}
			for _, row := range rows {
				if !row.IsMeeting || row.TranscriptPath == "" || !a.proactiveVoiceSessionsAllowedContext(ctx, []string{row.ID}, nil) {
					continue
				}
				if info, err := os.Stat(row.TranscriptPath); err == nil && info.Size() > 0 {
					return engagement.Moment{SubjectID: row.ID, Params: map[string]string{"stage": "meeting"}}, true
				}
			}
		}
	}
	return engagement.Moment{}, false
}

func (a *App) firstWeekSourceReadable(ctx context.Context, m engagement.Moment, now time.Time) bool {
	if a.IsClosedEyesModeEnabled() {
		return false
	}
	switch m.Params["stage"] {
	case "commitment":
		detail, ok := a.proactiveVoicePurpose(ctx, m.SubjectID, now)
		return ok && mobileBriefPurposeCurrent(detail)
	case "person":
		person, err := a.store.GetPrincipal(ctx, m.SubjectID)
		return err == nil && a.proactiveVoicePersonAllowed(ctx, person)
	case "meeting":
		if a.activity == nil || a.activity.store == nil {
			return false
		}
		row, err := a.activity.store.GetSessionByID(m.SubjectID)
		if err != nil || row == nil || !row.IsMeeting || !a.proactiveVoiceSessionsAllowedContext(ctx, []string{row.ID}, nil) {
			return false
		}
		info, err := os.Stat(row.TranscriptPath)
		return err == nil && info.Size() > 0
	default:
		return true
	}
}

func (a *App) produceFirstWeekBrief(brief MobileBriefV1) bool {
	if a.engagement == nil {
		return false
	}
	now := a.engagement.now()
	installed := a.firstWeekInstall(context.Background())
	if engagement.FirstWeekDay(installed, now) != 1 || !a.onboardingFinished(context.Background()) || !a.journeyEnabled(engagement.KindFirstWeek, now) {
		return false
	}
	key := engagement.DedupeKey(engagement.KindFirstWeek, installed.Format(time.RFC3339Nano), "brief")
	_ = a.submitEngagementSignal(context.Background(), engagement.Moment{Kind: engagement.KindFirstWeek, SubjectID: brief.BriefID,
		DedupeKey: key, CreatedAt: now, ExpiresAt: now.Add(12 * time.Hour), Params: map[string]string{"stage": "brief"}})
	return true
}

// Automatic briefs begin on day 1. A user can still request one immediately.
func (a *App) firstWeekBriefWait(now time.Time) bool {
	if a == nil || a.engagement == nil || !a.journeyEnabled(engagement.KindFirstWeek, now) {
		return false
	}
	ctx := context.Background()
	installed := a.firstWeekInstall(ctx)
	if !installed.IsZero() {
		return engagement.FirstWeekDay(installed, now) == 0
	}
	pending, err := a.store.GetUIState(ctx, firstWeekPendingKey)
	return err == nil && pending == "true" && !a.onboardingFinished(ctx)
}
