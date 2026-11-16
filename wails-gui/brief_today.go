package main

import (
	"context"
	"strings"
	"time"

	"wails-gui/internal/pii"
)

// Today shows the brief the brief service builds (until now only paired
// phones read it): the newest brief of today's local date, whether one is
// being built, when the next one comes, and, when the brief has nothing in
// it, what Isoform cannot see. The permission check only reads status; Today
// never asks macOS for anything.

// todayBriefPermissions are the permissions without which a brief is empty:
// Screen Recording and Accessibility (Isoform sees the screen) and Calendars
// (the meetings section).
var todayBriefPermissions = []string{"screen_recording", "accessibility", calendarPermissionKey}

// TodayDailySummary is local-only activity text without mobile read provenance.
// It must never be a field of MobileBriefV1 (including stored or legacy briefs).
type TodayDailySummary struct {
	Text string `json:"text"`
	At   string `json:"at"`
}

// TodayBriefView is Today's desktop response, not a phone document.
type TodayBriefView struct {
	NothingNew bool `json:"nothingNew,omitempty"`
	// WordsWithheld names why a brief read has no words: store_unavailable while a
	// store is still opening, or another reason. Empty when the words are there.
	WordsWithheld string              `json:"wordsWithheld,omitempty"`
	Quiet         bool                `json:"quiet,omitempty"`
	QuietSince    string              `json:"quietSince,omitempty"`
	QuietScreens  *int                `json:"quietScreens,omitempty"`
	DailySummary  *TodayDailySummary  `json:"dailySummary,omitempty"`
	WeekAnswer    *TodayWeekAnswer    `json:"weekAnswer,omitempty"`
	ClosingAnswer *TodayClosingAnswer `json:"closingAnswer,omitempty"`
	// Brief is the current brief (mobile_brief_current.go); nil when none is
	// stored and the day's snapshot is not there either.
	Brief    *MobileBriefV1    `json:"brief"`
	BriefRun *BriefScheduleRun `json:"briefRun,omitempty"`
	// FirstBrief: no brief has ever been made and a slot is due. The card is
	// "Your first brief"; any current-work snapshot stays below it
	// (today-review v1.1.11 #1).
	FirstBrief bool `json:"firstBrief,omitempty"`
	Building   bool `json:"building"`
	// Enabled is the schedule switch; NextAt and NextSlot name the next
	// scheduled brief when it is on.
	Enabled  bool   `json:"enabled"`
	NextAt   string `json:"nextAt"`
	NextSlot string `json:"nextSlot"`
	// PendingSlot names today's slot whose time has passed with no brief yet
	// (MobileBriefHealth.PendingSlot): Today says it is on its way instead of
	// presenting an older brief as current. PendingAt is its scheduled time
	// and RetryAt, after a failed try, when the next try runs.
	PendingSlot string `json:"pendingSlot,omitempty"`
	PendingAt   string `json:"pendingAt,omitempty"`
	RetryAt     string `json:"retryAt,omitempty"`
	// PendingAttempts is how many tries of the pending slot failed, only
	// when its run record counts them.
	PendingAttempts int `json:"pendingAttempts,omitempty"`
	// PendingCause is why the last try failed, when Isoform knows (e.g. "timeout").
	PendingCause     string `json:"pendingCause,omitempty"`
	PendingAssistant string `json:"pendingAssistant,omitempty"`
	// LastBriefID names the newest stored brief when it is not the one shown,
	// so Today can open it; LastSlot and LastAt describe it.
	LastBriefID string `json:"lastBriefId,omitempty"`
	LastSlot    string `json:"lastSlot,omitempty"`
	LastAt      string `json:"lastAt,omitempty"`
	// Empty says the brief has nothing in it.
	Empty bool `json:"empty"`
	// Missing lists what Isoform cannot see: screen_recording,
	// accessibility, calendars (not granted, or the user's switch is off).
	Missing []string `json:"missing"`
}

// todayBriefPermissionStatus reads the permission status without asking.
var todayBriefPermissionStatus = getTodayBriefPermissionStatus

// todayBriefMissing lists the permissions Isoform lacks or the user turned
// off, in the order Today names them.
func todayBriefMissing(perms PlatformPermissions, enabled func(key string) bool) []string {
	granted := map[string]bool{}
	known := map[string]bool{}
	for _, item := range perms.Items {
		known[item.Key] = true
		granted[item.Key] = item.Granted
	}
	missing := []string{}
	for _, key := range todayBriefPermissions {
		if !known[key] {
			continue
		}
		if !granted[key] || (enabled != nil && !enabled(key)) {
			missing = append(missing, key)
		}
	}
	return missing
}

// mobileBriefIsEmpty says a brief has nothing to read: no narrative, week,
// section item or shape of the day.
func mobileBriefIsEmpty(brief MobileBriefV1) bool {
	if len(brief.Narrative) > 0 || len(brief.WeeklyRecap) > 0 || brief.YourWeek != nil || len(brief.Upcoming) > 0 || !mobileBriefSectionsEmpty(brief.Sections) {
		return false
	}
	return brief.DayShape == nil || brief.DayShape.ActiveMinutes == 0
}

// todayBriefView composes the view from the current brief and the health. The
// current brief is shown whatever its date: an earlier day's brief stays
// current until today's arrives.
func todayBriefView(now time.Time, loc *time.Location, latest MobileBriefV1, hasLatest bool, health MobileBriefHealth, building bool, missing []string) TodayBriefView {
	if loc == nil {
		loc = time.Local
	}
	view := TodayBriefView{
		Building:    building,
		Enabled:     health.Enabled,
		NextAt:      health.NextRunAt,
		NextSlot:    health.NextSlot,
		Missing:     missing,
		PendingSlot: health.PendingSlot,
		PendingAt:   health.PendingAt,
		RetryAt:     health.RetryAt,
	}
	if health.PendingSlot != "" {
		view.PendingAttempts = health.PendingAttempts
		view.PendingCause = health.PendingCause
		view.PendingAssistant = health.PendingAssistant
	}
	if view.Missing == nil {
		view.Missing = []string{}
	}
	if hasLatest {
		brief := latest
		// Read-along needs where each paragraph ends, older recordings too.
		if audio := mobileBriefAudioWithParagraphEnds(brief.Audio); audio != nil {
			shown := *audio
			shown.Marks = mobileBriefAudioMarks(brief)
			brief.Audio = &shown
		}
		view.Brief = &brief
		view.Quiet = brief.NarrativeStatus == "quiet"
		view.QuietSince = brief.WindowStart
		view.Empty = view.Quiet || mobileBriefIsEmpty(brief)
	}
	if health.LatestBriefID != "" && (view.Brief == nil || view.Brief.BriefID != health.LatestBriefID) {
		view.LastBriefID, view.LastSlot, view.LastAt = health.LatestBriefID, health.LastSlot, health.LastRunAt
	}
	return view
}

// mobileBriefWithTemplate gives a delivered brief that has no written text
// (the model wrote none, or its text was withdrawn) Isoform's template of the
// cards as delivered now, the text the spoken brief reads in that case
// (mobileBriefSpokenText), with the offers it closes on. Built only from cards
// that can be read now, it names nothing withdrawn. Every read of a delivered
// brief (Today, the phone) and the brief's chat apply it, so they show the same
// text and conversation key; chat still proves the stored brief before any
// written text reaches the model.
func (a *App) mobileBriefWithTemplate(brief MobileBriefV1) MobileBriefV1 {
	if a == nil || brief.rankedEdition || brief.NarrativeStatus == "quiet" || brief.NarrativeStatus == "ok" || len(brief.Narrative) > 0 || brief.ColdStart || mobileBriefSectionsEmpty(brief.Sections) || a.IsClosedEyesModeEnabled() {
		return brief
	}
	language := a.currentUILanguageName()
	brief.Narrative = mobileBriefNarrativeTemplateOnly(brief, language)
	if len(brief.Narrative) == 0 {
		return brief
	}
	if len(brief.Offers) == 0 {
		_, brief.Offers = mobileBriefClosing(brief, language)
	}
	brief.ConversationKey = mobileBriefConversationKey(brief)
	return brief
}

// GetLatestBrief returns today's brief for the Today page.
func (a *App) GetLatestBrief() (perfValue TodayBriefView, perfErr error) {
	defer logPageBinding("GetLatestBrief", time.Now(), &perfValue)
	recordLaunchStage("brief_read_start")
	defer recordLaunchStage("brief_read_done")
	recordLaunchStage("brief_permissions_start")
	missing := todayBriefMissing(todayBriefPermissionStatus(a), a.isPermissionEnabled)
	recordLaunchStage("brief_permissions_done")
	svc := a.ensureMobileBriefService()
	if svc == nil {
		return todayBriefView(time.Now(), time.Local, MobileBriefV1{}, false, MobileBriefHealth{}, false, missing), nil
	}
	storedRead := a.launchStoredReads()
	var latest MobileBriefV1
	var ok bool
	if service, isService := svc.(*mobileBriefService); isService && service != nil {
		// Today shows the stored brief, checked against the current privacy
		// rules, at once. Its live signals are read in the background: that
		// read can take minutes and waits for any refresh in progress.
		var readErr error
		if latest, ok, readErr = service.finishedLatest(); readErr != nil {
			return TodayBriefView{}, readErr
		}
		// A read starts a live refresh only when the last one is past its budget;
		// source events refresh on their own, coalesced (R24).
		if !storedRead && service.todayRefreshDue(time.Now()) {
			service.refreshTodaySoon()
		}
	} else {
		latest, ok = svc.LatestMobileBrief()
	}
	building := false
	if b, isBuilding := svc.(mobileBriefBuilding); isBuilding {
		building = b.Building()
	}
	// Today is the service's today: its clock and the zone it stamps each
	// brief's local date in, so the two can never disagree.
	now, loc := time.Now(), time.Local
	if s, isService := svc.(*mobileBriefService); isService && s != nil {
		now, loc = s.now(), s.location
	}
	latestReady := mobileBriefAsOfTime(latest)
	// Use the same stored choice as the menu bar. Later empty requests are
	// still recorded, but cannot displace its written words.
	snapshot, everMade, firstBrief := false, false, false
	var quiet MobileBriefV1
	var quietAt time.Time
	nothingNew, seenRequest := false, false
	if service, isService := svc.(*mobileBriefService); isService && service != nil {
		// Headers, not decoded briefs: only the quiet brief Today may show is read.
		quietID := ""
		for _, candidate := range service.store.headers() {
			everMade = true
			ready := candidate.asOf
			if ready.IsZero() || ready.After(now) {
				continue
			}
			if candidate.NarrativeStatus == "quiet" && (quietAt.IsZero() || ready.After(quietAt)) {
				quietID, quietAt = candidate.BriefID, ready
			}
			if ok && !seenRequest && candidate.Slot == mobileBriefSlotOnRequest && ready.After(latestReady) {
				seenRequest = true
				nothingNew = candidate.NarrativeStatus == "quiet" || (candidate.NarrativeStatus == "skipped" && candidate.empty)
			}
		}
		if quietID != "" {
			if loaded, found := service.store.load(quietID); found {
				quiet = loaded
			} else {
				quietAt = time.Time{}
			}
		}
		if ok {
			_, stored := service.store.load(latest.BriefID)
			snapshot = !stored
		}
	}
	var health MobileBriefHealth
	if service, isService := svc.(*mobileBriefService); isService && storedRead {
		// Health reads the stored slot history, never the Today snapshot.
		stored, found := service.store.latest()
		settings := service.config.load()
		health = service.withNextRun(service.withPending(mobileBriefHealthView(now, loc, settings, stored, found, service.runs.list()), now, settings, stored, found), now, settings)
	} else {
		health = svc.Health()
	}
	if !ok {
		// A quiet observation is not a last brief to open.
		health.LatestBriefID, health.LastSlot, health.LastRunAt = "", "", ""
	}
	// A scheduled slot owns the card from its scheduled time until a brief
	// made for that window is ready, even after the scheduler stops retrying.
	if service, isService := svc.(*mobileBriefService); isService && service != nil {
		settings := service.config.load()
		if settings.Enabled {
			zone := loc
			if zone == nil {
				zone = time.Local
			}
			var due mobileBriefSlotOccurrence
			days := []int{-1, 0}
			if !everMade {
				// Yesterday's slots came before the first brief could be made.
				days = []int{0}
			}
			for _, day := range days {
				for _, slot := range mobileBriefTodaySlots(now.In(zone).AddDate(0, 0, day), zone, settings) {
					if !slot.ScheduledAt.After(now) && (due.ScheduledAt.IsZero() || slot.ScheduledAt.After(due.ScheduledAt)) {
						due = slot
					}
				}
			}
			quietForSlot := (!ok || !mobileBriefHasWrittenText(latest)) && !quietAt.IsZero() && !due.ScheduledAt.IsZero() && quiet.Slot != mobileBriefSlotOnRequest && !quietAt.Before(due.ScheduledAt) && (!ok || snapshot || quietAt.After(latestReady))
			if quietForSlot {
				// The slot ran and had nothing to say: Today shows its quiet
				// state, as it always has, not a pending one.
				latest, ok, snapshot, latestReady = quiet, true, false, quietAt
				health.PendingSlot, health.PendingAt, health.RetryAt, health.PendingAttempts, health.PendingCause = "", "", "", 0, ""
			} else if !due.ScheduledAt.IsZero() && !service.store.exists(due.BriefID) && (!ok || snapshot || latestReady.Before(due.ScheduledAt)) {
				health.PendingSlot, health.PendingAt = due.Slot, formatMobileBriefTime(due.ScheduledAt)
				if ok && !snapshot {
					health.LatestBriefID, health.LastSlot, health.LastRunAt = latest.BriefID, latest.Slot, formatMobileBriefTime(latestReady)
				}
				// The day's current work is real content: it stays below the
				// first brief's card, never removed by it.
				firstBrief = !everMade
				ok = ok && (mobileBriefHasWrittenText(latest) || snapshot && firstBrief)
			} else if ok && !due.ScheduledAt.IsZero() && (service.store.exists(due.BriefID) || !latestReady.Before(due.ScheduledAt)) {
				health.PendingSlot, health.PendingAt, health.RetryAt, health.PendingAttempts, health.PendingCause = "", "", "", 0, ""
			}
		}
	}
	shown := latest
	if ok && a.IsClosedEyesModeEnabled() && shown.NarrativeStatus != "quiet" {
		if brief, readable := a.todayDesktopPausedBrief(context.Background(), shown.BriefID); readable {
			shown = brief
		}
	}
	if ok {
		shown = mobileBriefWithoutStartedEventQuestion(shown, now)
	}
	view := todayBriefView(now, loc, shown, ok, health, building, missing)
	view.FirstBrief = firstBrief
	view.NothingNew = nothingNew && view.Brief != nil && mobileBriefHasWrittenText(*view.Brief)
	if view.Brief != nil {
		view.BriefRun = todayBriefRecordedRun(svc, view.Brief.BriefID)
	}
	if view.Brief != nil {
		view.WeekAnswer = a.todayWeekAnswer(*view.Brief, now)
		view.ClosingAnswer = a.todayClosingAnswer(*view.Brief)
	}
	if view.Quiet {
		start := mobileBriefParseTime(shown.WindowStart)
		view.QuietSince = ""
		if service, isService := svc.(*mobileBriefService); isService {
			for _, previous := range service.store.headers() {
				if previous.NarrativeStatus != "quiet" && previous.asOf.Before(mobileBriefAsOfTime(shown)) {
					start = previous.asOf
					view.QuietSince = previous.AsOf
					break
				}
			}
		}
		view.QuietScreens = a.mobileBriefQuietScreens(start, now)
	}
	if loc == nil {
		loc = time.Local
	}
	// This field belongs only to the local desktop response. Phone documents
	// omit it entirely; their shared source reader must keep the stronger gate.
	if a.activity != nil && a.activity.store != nil {
		if summary, found := a.activity.store.cachedActivitySummarySnapshot(now.In(loc).Format("2006-01-02"), "daily", a.currentUILanguage()); found && strings.TrimSpace(summary.Summary) != "" {
			view.DailySummary = &TodayDailySummary{Text: pii.DisplayModelText(mobileBriefText(summary.Summary, 8000), a.modelDisplayLanguage()), At: formatMobileBriefTime(mobileBriefMillis(summary.CreatedAt))}
		}
	}
	return view, nil
}

// GetTodayBriefByID opens a stored brief from a navigation target. If it has
// been removed, Today falls back to its normal latest-brief view.
func (a *App) GetTodayBriefByID(briefID string) (TodayBriefView, error) {
	if strings.TrimSpace(briefID) == "" {
		return a.GetLatestBrief()
	}
	missing := todayBriefMissing(todayBriefPermissionStatus(a), a.isPermissionEnabled)
	svc := a.ensureMobileBriefService()
	if svc == nil {
		return a.GetLatestBrief()
	}
	var brief MobileBriefV1
	var ok bool
	var withheld mobileBriefWithholding
	if service, isService := svc.(*mobileBriefService); isService && service != nil {
		var err error
		if brief, withheld, ok, err = service.finishedBriefWithholding(briefID); err != nil {
			return TodayBriefView{}, err
		}
	} else {
		brief, ok = svc.GetMobileBrief(briefID)
	}
	if !ok || brief.BriefID != briefID {
		return a.GetLatestBrief()
	}
	brief = a.mobileBriefWithTemplate(brief)
	if a.IsClosedEyesModeEnabled() {
		if readable, found := a.todayDesktopPausedBrief(context.Background(), briefID); found {
			brief = readable
		}
	}
	loc := time.Local
	if service, isService := svc.(*mobileBriefService); isService && service.location != nil {
		loc = service.location
	}
	selectedAt, err := time.ParseInLocation("2006-01-02", brief.LocalDate, loc)
	if err != nil {
		return a.GetLatestBrief()
	}
	building := false
	if current, isBuilding := svc.(mobileBriefBuilding); isBuilding {
		building = current.Building()
	}
	now := time.Now()
	if service, isService := svc.(*mobileBriefService); isService {
		now = service.now()
	}
	brief = mobileBriefWithoutStartedEventQuestion(brief, now)
	view := todayBriefView(selectedAt, loc, brief, true, svc.Health(), building, missing)
	if !mobileBriefHasWrittenText(brief) {
		view.WordsWithheld = withheld.Reason
	}
	view.BriefRun = todayBriefRecordedRun(svc, brief.BriefID)
	view.WeekAnswer = a.todayWeekAnswer(brief, now)
	view.ClosingAnswer = a.todayClosingAnswer(brief)
	return view, nil
}

// The run is the only source for the cause shown beside a delivered brief.
func todayBriefRecordedRun(source mobileBriefSource, briefID string) *BriefScheduleRun {
	reader, ok := source.(upcomingBriefReader)
	if !ok {
		return nil
	}
	return todayBriefRecordedRunIn(reader.upcomingBrief().Runs, briefID)
}

func todayBriefRecordedRunIn(runs []mobileBriefRun, briefID string) *BriefScheduleRun {
	for _, run := range runs {
		if run.BriefID != briefID || run.Status != mobileBriefRunOK {
			continue
		}
		scheduled, err := time.Parse(time.RFC3339Nano, run.ScheduledAt)
		if err != nil {
			return nil
		}
		at, err := time.Parse(time.RFC3339Nano, run.At)
		if err != nil {
			return nil
		}
		return &BriefScheduleRun{BriefID: run.BriefID, Status: run.Status, ScheduledAt: run.ScheduledAt, At: run.At, Late: at.Sub(scheduled) > mobileBriefLateAfter, Reason: briefScheduleRecordedReason(run.Reason)}
	}
	return nil
}
