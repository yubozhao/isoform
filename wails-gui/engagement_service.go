package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"strings"
	"sync"
	"time"

	"wails-gui/internal/engagement"
	"wails-gui/storage"
)

const (
	engagementTickInterval = time.Minute
	engagementRetryDelay   = 5 * time.Minute
	engagementStopWait     = 3 * time.Second
	// engagementStatsWindow is how far back receipts count for fatigue and
	// auto-pause. It is longer than the pause threshold divided by the
	// slowest daily cap, so a once-a-day kind can still reach the threshold.
	engagementStatsWindow = 30 * 24 * time.Hour
	// engagementAutoPauseMinSent is how many deliveries a kind needs before
	// its dismiss-and-mute rate can pause it.
	engagementAutoPauseMinSent = 20
	// engagementAutoPauseRate is the dismissed-plus-muted share that pauses a
	// kind.
	engagementAutoPauseRate = 0.6
)

// engagementText is the rendered copy for one delivery.
type engagementText struct {
	// PushBody is the generic line that travels in the clear.
	PushBody string
	// Title is the sealed text the phone shows after decrypting.
	Title string
	// Actions maps action ids to labels in the user's language.
	Actions map[string]string
	// Category groups the actions for the phone.
	Category string
	// EmailSubject and EmailBody are the email, for kinds that have one.
	EmailSubject string
	EmailBody    string
}

// engagementDelivery holds the channel senders. A nil sender means the
// channel is unavailable, and the router skips it.
type engagementDelivery struct {
	Push    func(ctx context.Context, row storage.EngagementMomentRow, m engagement.Moment, v engagement.Verdict, text engagementText) error
	Desktop func(ctx context.Context, row storage.EngagementMomentRow, m engagement.Moment, v engagement.Verdict, text engagementText) error
	Text    func(ctx context.Context, row storage.EngagementMomentRow, m engagement.Moment, v engagement.Verdict, text engagementText) error
	Email   func(ctx context.Context, row storage.EngagementMomentRow, m engagement.Moment, v engagement.Verdict, text engagementText) error
	Digest  func(ctx context.Context, items []engagementDigestItem) (engagement.Channel, error)
	// Report acknowledges a durable chat record, or an intentional skip for
	// a kind with its own report. False keeps the chat record retryable.
	Report func(m engagement.Moment, text engagementText) bool
}

// engagementChannelsParam stores a moment's chosen channels among its params;
// momentFromRow takes it back out, so no template ever sees it.
const engagementChannelsParam = "_channels"

// engagementChatReportedParam records presentation separately from channel
// delivery. A chosen channel can remain unavailable across retries or restarts;
// the routine result still belongs in chat exactly once. Like _channels, this
// internal state is removed before parameters reach copy or channel senders.
const engagementChatReportedParam = "_chat_reported"

// engagementService turns submitted moments into deliveries. It owns the
// pacing state, the preferences and the receipts; producers only Submit.
type engagementService struct {
	store                  *storage.Store
	now                    func() time.Time
	tick                   time.Duration
	lang                   func() string
	presence               func(now time.Time) engagement.Presence
	deliver                engagementDelivery
	threadOutcomeCopy      func(engagement.Moment, string) (engagementText, bool)
	sourceAllowsRead       func(context.Context, engagement.Moment) bool
	prepareSource          func(context.Context, engagement.Moment) (engagement.Moment, bool)
	holdStagedListReminder func(context.Context, engagement.Moment) (bool, error)
	completion             func(context.Context, storage.EngagementMomentRow) (time.Time, error)
	settingsChanged        func(engagement.Prefs)
	paceChanged            func()
	journey                func(context.Context, time.Time)
	afterPass              func()

	// chatReportRetry, when set, says when a failed chat report is tried
	// again; activity-driven cards wait for the hourly batch.
	chatReportRetry func(engagement.Kind, time.Time) time.Time

	// deliveryMu serializes a quiet-mode change with a complete delivery pass.
	// Once the setter returns, no older pass can publish on the previous mode.
	// Dedicated handoff reports make a fast entry-time decision outside this lock.
	deliveryMu sync.Mutex
	// notificationIDs is owned by deliveryMu and counts actual notices in the current pass.
	notificationIDs map[string]bool
	mu              sync.Mutex
	prefs           engagement.Prefs
	state           *engagement.PaceState
	paused          map[engagement.Kind]bool
	// resumedAt is when the user last turned a kind back on; receipts from
	// before it never pause the kind again.
	resumedAt map[engagement.Kind]time.Time
	// unrecorded holds deliveries whose state could not be written, by
	// moment id, so a later pass neither sends them again nor loses them.
	unrecorded  map[string]engagementUnrecorded
	ctx         context.Context
	cancel      context.CancelFunc
	wake        chan struct{}
	started     bool
	stopped     bool
	loadPending bool
	wg          sync.WaitGroup
}

// engagementUnrecorded is a delivery the store did not take: the channels
// that got the moment, and whether it was counted against the pace.
type engagementUnrecorded struct {
	channels string
	paced    bool
	sentAt   int64
	level    string
	score    float64
}

// noteUnrecorded remembers (or, with an empty channels, forgets) a delivery
// of the moment id that could not be recorded.
func (s *engagementService) noteUnrecorded(id string, note engagementUnrecorded) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if note.channels == "" {
		delete(s.unrecorded, id)
		return
	}
	if s.unrecorded == nil {
		s.unrecorded = map[string]engagementUnrecorded{}
	}
	s.unrecorded[id] = note
}

// persistUnrecorded restores the delivery receipt before expiry or pacing can
// discard its pending chat report. If SQLite still refuses writes, leave the
// in-memory receipt intact and retry the pass without sending another notice.
func (s *engagementService) persistUnrecorded(ctx context.Context, now time.Time) bool {
	s.mu.Lock()
	pending := make(map[string]engagementUnrecorded, len(s.unrecorded))
	for id, note := range s.unrecorded {
		pending[id] = note
	}
	s.mu.Unlock()
	for id, note := range pending {
		row, err := s.store.GetEngagementMoment(ctx, id)
		if err != nil {
			return false
		}
		if row == nil || row.State == storage.EngagementStateSuppressed || row.State == storage.EngagementStateExpired {
			s.noteUnrecorded(id, engagementUnrecorded{})
			continue
		}
		state := storage.EngagementStateScheduled
		if note.paced {
			state = storage.EngagementStateSent
		}
		if err := s.store.SetEngagementMomentState(ctx, id, state, "chat report", storage.EngagementMomentRow{
			Channel: note.channels, SentAt: note.sentAt, Level: note.level, Score: note.score, HoldUntil: now.UnixMilli(),
		}); err != nil {
			log.Printf("[engagement] recording prior delivery of %s: %v", row.Kind, err)
			return false
		}
		s.noteUnrecorded(id, engagementUnrecorded{})
	}
	return true
}

func newEngagementService(store *storage.Store, presence func(time.Time) engagement.Presence, deliver engagementDelivery) *engagementService {
	return &engagementService{
		store:     store,
		now:       time.Now,
		tick:      engagementTickInterval,
		lang:      func() string { return "en" },
		presence:  presence,
		deliver:   deliver,
		prefs:     engagement.DefaultPrefs(),
		state:     engagement.NewPaceState(),
		paused:    map[engagement.Kind]bool{},
		resumedAt: map[engagement.Kind]time.Time{},
		wake:      make(chan struct{}, 1),
	}
}

// Start loads preferences and the recent delivery history, then runs the
// pacing loop until Stop or the context ends.
func (s *engagementService) Start(ctx context.Context) {
	s.mu.Lock()
	if s.started || s.stopped || s.store == nil {
		s.mu.Unlock()
		return
	}
	s.started = true
	s.ctx, s.cancel = context.WithCancel(ctx)
	s.mu.Unlock()

	if err := s.load(s.ctx); err != nil {
		log.Printf("[engagement] loading state: %v", err)
		s.mu.Lock()
		s.loadPending = true
		s.mu.Unlock()
	}
	s.suppressSilentKinds(s.ctx)
	s.wg.Add(1)
	go s.run()
}

// engagementSilentKinds are moment kinds the desktop no longer produces:
// recording a call and writing up its notes never notifies. The kinds stay
// in the engagement package for the moments stored before; Start suppresses
// those, and notification settings leave the retired kinds out.
var engagementSilentKinds = []engagement.Kind{engagement.KindMeetingEnded, engagement.KindMeetingFollowup}

func engagementKindSilent(kind engagement.Kind) bool {
	for _, silent := range engagementSilentKinds {
		if kind == silent {
			return true
		}
	}
	return false
}

// suppressSilentKinds retires stored moments of the silent kinds that have
// not gone out yet, such as a "meeting notes are ready" waiting for a digest.
func (s *engagementService) suppressSilentKinds(ctx context.Context) {
	for _, kind := range engagementSilentKinds {
		rows, err := s.store.OpenEngagementMomentsOfKind(ctx, string(kind))
		if err != nil {
			log.Printf("[engagement] reading open %s moments: %v", kind, err)
			continue
		}
		for _, row := range rows {
			if err := s.store.SetEngagementMomentState(ctx, row.ID, storage.EngagementStateSuppressed, "silent kind", storage.EngagementMomentRow{}); err != nil {
				log.Printf("[engagement] suppressing %s moment %s: %v", kind, row.ID, err)
			}
		}
	}
}

// Stop ends the loop and waits briefly for an in-flight pass.
func (s *engagementService) Stop() {
	s.mu.Lock()
	if !s.started || s.stopped {
		s.stopped = true
		s.mu.Unlock()
		return
	}
	s.stopped = true
	cancel := s.cancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(engagementStopWait):
		log.Printf("[engagement] stop timed out waiting for the pacing loop")
	}
}

// Submit records a moment and nudges the loop. A moment whose dedupe key is
// already open is returned as the existing row without error.
func (s *engagementService) Submit(ctx context.Context, m engagement.Moment) (storage.EngagementMomentRow, error) {
	if s == nil || s.store == nil {
		return storage.EngagementMomentRow{}, errors.New("engagement service is not available")
	}
	if _, ok := engagement.Spec(m.Kind); !ok {
		return storage.EngagementMomentRow{}, errors.New("unknown engagement kind " + string(m.Kind))
	}
	if m.DedupeKey == "" {
		m.DedupeKey = engagement.DedupeKey(m.Kind, m.SubjectID)
	}
	now := s.now()
	if m.CreatedAt.IsZero() {
		m.CreatedAt = now
	}
	m, allowed := sanitizeEngagementMoment(m)
	if s.prepareSource != nil {
		var prepared bool
		m, prepared = s.prepareSource(ctx, m)
		allowed = allowed && prepared
	}
	if s.sourceAllowsRead != nil {
		allowed = allowed && s.sourceAllowsRead(ctx, m)
	}
	if !allowed {
		m.Params = nil
	}
	if spec, _ := engagement.Spec(m.Kind); allowed && spec.Cooldown == 0 {
		if prior, err := s.store.QuietEngagementMomentByKey(ctx, m.DedupeKey); err != nil {
			return storage.EngagementMomentRow{}, err
		} else if prior != nil {
			return *prior, nil
		}
	}
	params := momentParamsJSON(m)
	row := storage.EngagementMomentRow{
		ID:        m.ID,
		Kind:      string(m.Kind),
		SubjectID: m.SubjectID,
		DedupeKey: m.DedupeKey,
		Params:    params,
		CreatedAt: m.CreatedAt.UnixMilli(),
	}
	if !allowed {
		row.State = storage.EngagementStateSuppressed
		row.Reason = "sensitive source"
	}
	if !m.DueAt.IsZero() {
		row.DueAt = m.DueAt.UnixMilli()
	}
	if !m.ExpiresAt.IsZero() {
		row.ExpiresAt = m.ExpiresAt.UnixMilli()
	}
	if m.HardDeadline {
		row.Level = "deadline"
	}
	if m.Stakes > 0 {
		row.Stakes = m.Stakes
	}
	// A moment for later (a reminder) waits as scheduled until its time; the
	// loop paces it on the first pass after, including one after a wake.
	if row.State == "" && m.NotBefore.After(now) {
		row.State = storage.EngagementStateScheduled
		row.HoldUntil = m.NotBefore.UnixMilli()
	}
	created, err := s.store.CreateEngagementMoment(ctx, row)
	if errors.Is(err, storage.ErrEngagementMomentOpen) {
		open, lookupErr := s.openMoment(ctx, m.DedupeKey)
		if lookupErr == nil && open != nil {
			return *open, nil
		}
		return storage.EngagementMomentRow{}, err
	}
	if err != nil {
		return storage.EngagementMomentRow{}, err
	}
	s.nudge()
	return created, nil
}

// Retire suppresses the open (pending or scheduled) moment with the dedupe key,
// if any, so a situation the user already handled elsewhere is not delivered.
// A moment already sent stays as it is.
func (s *engagementService) Retire(ctx context.Context, dedupeKey, reason string) error {
	if s == nil || s.store == nil {
		return errors.New("engagement service is not available")
	}
	open, err := s.openMoment(ctx, dedupeKey)
	if err != nil || open == nil {
		return err
	}
	return s.store.SetEngagementMomentState(ctx, open.ID, storage.EngagementStateSuppressed, reason, storage.EngagementMomentRow{})
}

// Receipt records what happened to a delivery. A muted receipt turns the
// kind off; dismiss and mute rates feed the fatigue score and auto-pause.
func (s *engagementService) Receipt(ctx context.Context, momentID, channel, state, action string, at time.Time) error {
	if s == nil || s.store == nil {
		return errors.New("engagement service is not available")
	}
	if at.IsZero() {
		at = s.now()
	}
	if state == storage.EngagementReceiptActed && (channel != storage.EngagementCompletionChannel || action != storage.EngagementCompletionAction) {
		state = storage.EngagementReceiptOpened
	}
	row, err := s.store.GetEngagementMoment(ctx, momentID)
	if err != nil {
		return err
	}
	if row == nil {
		return errors.New("unknown moment " + momentID)
	}
	if err := s.store.RecordEngagementReceipt(ctx, storage.EngagementReceiptRow{
		MomentID: momentID, Channel: channel, State: state, Action: action, At: at.UnixMilli(),
	}); err != nil {
		return err
	}
	kind := engagement.Kind(row.Kind)
	if spec, _ := engagement.Spec(kind); spec.Direct {
		// Each one was a request of its own: muting it ends only that one.
		return nil
	}
	if state == storage.EngagementReceiptMuted {
		if err := s.SetKindEnabled(ctx, kind, false); err != nil {
			return err
		}
	}
	return s.autoPause(ctx, kind)
}

// SetKindEnabled turns a kind on or off and persists it.
func (s *engagementService) SetKindEnabled(ctx context.Context, kind engagement.Kind, on bool) error {
	if _, ok := engagement.Spec(kind); !ok {
		return errors.New("unknown notification kind")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	pref := s.prefRow(kind)
	pref.Enabled = 0
	control := s.controlRow(kind)
	if on {
		pref.Enabled, pref.MutedUntil = 1, 0
		control.Paused, control.ResumedAt = false, s.now().UnixMilli()
	}
	if err := s.store.SaveEngagementKindControl(ctx, pref, control); err != nil {
		return err
	}
	s.prefs.Enabled[kind] = on
	if on {
		delete(s.paused, kind)
		delete(s.prefs.MutedUntil, kind)
		s.resumedAt[kind] = time.UnixMilli(control.ResumedAt)
	}
	if s.settingsChanged != nil {
		s.settingsChanged(s.prefsCopyLocked())
	}
	return nil
}

// MuteKind silences a kind until the given time.
func (s *engagementService) MuteKind(ctx context.Context, kind engagement.Kind, until time.Time) error {
	s.mu.Lock()
	s.prefs.MutedUntil[kind] = until
	pref := s.prefRow(kind)
	s.mu.Unlock()
	return s.store.UpsertEngagementPref(ctx, pref)
}

// SetKindQuiet sets whether a kind shows as a quiet line on Today instead of
// interrupting, and persists it. A kind that needs the user's answer refuses
// with ErrKindCannotBeQuiet.
func (s *engagementService) SetKindQuiet(ctx context.Context, kind engagement.Kind, quiet bool) error {
	if _, ok := engagement.Spec(kind); !ok {
		return errors.New("engagement_kind_unknown")
	}
	if quiet && !engagement.QuietOnTodayAllowed(kind) {
		return &engagement.KindCannotBeQuietError{Kind: kind}
	}
	s.deliveryMu.Lock()
	defer s.deliveryMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	control := s.controlRow(kind)
	control.DeliveryMode = string(engagement.KindDeliveryNormal)
	if quiet {
		control.DeliveryMode = string(engagement.KindDeliveryQuietOnToday)
	}
	if err := s.store.SaveEngagementKindControl(ctx, s.prefRow(kind), control); err != nil {
		return err
	}
	if quiet {
		s.prefs.QuietOnToday[kind] = true
	} else {
		delete(s.prefs.QuietOnToday, kind)
	}
	if s.settingsChanged != nil {
		s.settingsChanged(s.prefsCopyLocked())
	}
	return nil
}

// kindQuiet reports whether a kind shows on Today instead of interrupting.
func (s *engagementService) kindQuiet(kind engagement.Kind) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.prefs.KindQuietOnToday(kind)
}

// engagementRulesLimit bounds the daily interruption budget; zero is no cap.
const engagementRulesBudgetMax = 24

var errEngagementRules = errors.New("quiet hours must be times of day and the daily budget 0 to 24")

// SetRules changes the quiet window and the daily interruption budget and
// persists them. The same start and end means no quiet hours; a zero budget
// means no cap.
func (s *engagementService) SetRules(ctx context.Context, quiet engagement.QuietWindow, budget int) error {
	day := 24 * time.Hour
	if quiet.Start < 0 || quiet.Start >= day || quiet.End < 0 || quiet.End >= day || budget < 0 || budget > engagementRulesBudgetMax {
		return errEngagementRules
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rules := s.rulesRow()
	rules.QuietStartMinutes = int(quiet.Start / time.Minute)
	rules.QuietEndMinutes = int(quiet.End / time.Minute)
	rules.DailyInterruptBudget = budget
	if err := s.store.SetEngagementRules(ctx, rules); err != nil {
		return err
	}
	s.prefs.Quiet = quiet
	s.prefs.DailyInterruptBudget = budget
	return nil
}

// SetLevel turns the one dial: every kind switch, the kind weights, the
// budget, the digest times and the quiet window are rewritten for the level
// and persisted. Kinds the level turns on resume from an auto-pause.
func (s *engagementService) SetLevel(ctx context.Context, level engagement.Level) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	prefs, ok := engagement.ApplyLevel(s.prefs, level)
	if !ok {
		return errors.New("unknown notification level " + string(level))
	}
	s.prefs = prefs
	now := s.now()
	rows := make([]storage.EngagementPrefRow, 0, len(engagement.AllKinds()))
	controls := map[string]storage.EngagementKindControl{}
	for _, kind := range engagement.AllKinds() {
		if s.prefs.Enabled[kind] {
			delete(s.paused, kind)
			s.resumedAt[kind] = now
		}
		rows = append(rows, s.prefRow(kind))
		controls[string(kind)] = s.controlRow(kind)
	}
	rules := s.rulesRow()
	for _, row := range rows {
		if err := s.store.SaveEngagementKindControl(ctx, row, controls[row.Kind]); err != nil {
			return err
		}
	}
	if err := s.store.SetEngagementRules(ctx, rules); err != nil {
		return err
	}
	if s.settingsChanged != nil {
		s.settingsChanged(s.prefsCopyLocked())
	}
	return nil
}

// Level reports the dial position the effective preferences match, or custom.
func (s *engagementService) Level() engagement.Level {
	s.mu.Lock()
	defer s.mu.Unlock()
	return engagement.LevelOf(s.prefs)
}

// rulesRow is the persisted form of the global rules. The caller holds s.mu.
func (s *engagementService) rulesRow() storage.EngagementRulesRow {
	row := storage.EngagementRulesRow{
		QuietStartMinutes:    int(s.prefs.Quiet.Start / time.Minute),
		QuietEndMinutes:      int(s.prefs.Quiet.End / time.Minute),
		DailyInterruptBudget: s.prefs.DailyInterruptBudget,
		DigestMinutes:        make([]int, 0, len(s.prefs.DigestTimes)),
	}
	for _, offset := range s.prefs.DigestTimes {
		row.DigestMinutes = append(row.DigestMinutes, int(offset/time.Minute))
	}
	return row
}

// WeekCounts returns the weekly line: moments sent in the last seven days
// and how many of them the user opened or acted on.
func (s *engagementService) WeekCounts(ctx context.Context) (storage.EngagementWeekCounts, error) {
	return s.store.EngagementCountsSince(ctx, s.now().Add(-7*24*time.Hour))
}

// Direct deliveries were explicitly requested one by one. Their chat record
// follows the delivery even when the notification kind is temporarily muted.
// Like Pace, all kinds still honor the enabled switch and auto-pause.
func engagementChatKindMuted(spec engagement.KindSpec, prefs engagement.Prefs, paused bool, now time.Time) bool {
	return !prefs.KindEnabled(spec) || paused || (!spec.Direct && prefs.MutedUntil[spec.Kind].After(now))
}

// Prefs returns a copy of the effective preferences.
func (s *engagementService) Prefs() engagement.Prefs {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.prefsCopyLocked()
}

// prefsCopyLocked returns the preferences with every map and slice copied. The
// caller holds s.mu.
func (s *engagementService) prefsCopyLocked() engagement.Prefs {
	out := s.prefs
	out.Weights = copyFloatMap(s.prefs.Weights)
	out.Enabled = copyBoolMap(s.prefs.Enabled)
	out.MutedUntil = copyTimeMap(s.prefs.MutedUntil)
	out.QuietOnToday = copyBoolMap(s.prefs.QuietOnToday)
	out.DigestTimes = append([]time.Duration(nil), s.prefs.DigestTimes...)
	return out
}

// Stats returns delivery counts per kind over the stats window.
func (s *engagementService) Stats(ctx context.Context) (map[string]storage.EngagementKindStats, error) {
	return s.store.EngagementStatsBetween(ctx, s.now().Add(-engagementStatsWindow), s.now())
}

// Pass runs one pacing pass now. The loop calls it on every tick and nudge;
// tests call it directly.
func (s *engagementService) Pass(ctx context.Context) {
	s.deliveryMu.Lock()
	defer func() {
		s.deliveryMu.Unlock()
		if s.afterPass != nil {
			s.afterPass()
		}
	}()
	if !s.ensureLoaded(ctx) {
		return
	}
	now := s.now()
	if s.journey != nil {
		s.journey(ctx, now)
	}
	s.inferCompletedReceipts(ctx, now)
	if !s.persistUnrecorded(ctx, now) {
		return
	}
	if err := s.refreshNotificationBudget(ctx, now); err != nil {
		log.Printf("[engagement] reading notification budget: %v", err)
		return
	}
	if n, err := s.store.ExpireEngagementMoments(ctx, now); err != nil {
		log.Printf("[engagement] expiring moments: %v", err)
	} else if n > 0 {
		log.Printf("[engagement] %d moments expired unsent", n)
	}
	due, err := s.store.DueEngagementMoments(ctx, now, 0)
	if err != nil {
		log.Printf("[engagement] reading due moments: %v", err)
		return
	}
	var digest []storage.EngagementMomentRow
	for _, row := range due {
		if ctx.Err() != nil {
			return
		}
		spec, known := engagement.Spec(engagement.Kind(row.Kind))
		moment := momentFromRow(row)
		routine := s.deliver.Report != nil && engagementMomentIsStatus(moment, now)
		// A quiet kind never joins the digest: the digest posts to the chat.
		if s.deliver.Digest != nil && known && spec.Digestable && !routine && !s.kindQuiet(spec.Kind) && row.Channel == "" && len(moment.Channels) == 0 && row.State == storage.EngagementStateScheduled && row.HoldUntil > 0 && row.HoldUntil <= now.UnixMilli() {
			digest = append(digest, row)
			continue
		}
		s.handle(ctx, row, now)
	}
	s.handleDigest(ctx, digest, now)
}

// ensureLoaded refuses delivery until persisted preferences can be read.
// Call with deliveryMu held.
func (s *engagementService) ensureLoaded(ctx context.Context) bool {
	s.mu.Lock()
	pending := s.loadPending
	s.mu.Unlock()
	if !pending {
		return true
	}
	if err := s.load(ctx); err != nil {
		return false
	}
	s.mu.Lock()
	s.loadPending = false
	s.mu.Unlock()
	return true
}

func (s *engagementService) handle(ctx context.Context, row storage.EngagementMomentRow, now time.Time) {
	spec, ok := engagement.Spec(engagement.Kind(row.Kind))
	if !ok {
		_ = s.store.SetEngagementMomentState(ctx, row.ID, storage.EngagementStateSuppressed, "unknown kind", storage.EngagementMomentRow{})
		return
	}
	quiet := s.kindQuiet(spec.Kind)
	reported := engagementReportedInChat(row)
	m, allowed := sanitizeEngagementMoment(momentFromRow(row))
	if s.holdStagedListReminder != nil {
		hold, err := s.holdStagedListReminder(ctx, m)
		if err != nil || hold {
			_ = s.store.SetEngagementMomentState(ctx, row.ID, storage.EngagementStateScheduled, "list batch pending",
				storage.EngagementMomentRow{HoldUntil: now.Add(engagementRetryDelay).UnixMilli()})
			return
		}
	}
	if s.sourceAllowsRead != nil {
		allowed = allowed && s.sourceAllowsRead(ctx, m)
	}
	if !allowed {
		_ = s.store.SetEngagementMomentParams(ctx, row.ID, "{}")
		_ = s.store.SetEngagementMomentState(ctx, row.ID, storage.EngagementStateSuppressed, "sensitive source", storage.EngagementMomentRow{})
		return
	}
	if params := momentParamsJSON(m, reported); params != row.Params {
		if err := s.store.SetEngagementMomentParams(ctx, row.ID, params); err != nil {
			return
		}
		row.Params = params
	}
	report := func() bool {
		prefs := s.Prefs()
		if quiet || reported || engagementChatKindMuted(spec, prefs, s.Paused(spec.Kind), now) || s.deliver.Report == nil {
			return true
		}
		if !s.deliver.Report(m, s.render(spec, m)) {
			return false
		}
		params := momentParamsJSON(m, true)
		if err := s.store.SetEngagementMomentParams(ctx, row.ID, params); err != nil {
			log.Printf("[engagement] recording %s as reported in chat: %v", row.Kind, err)
			return false
		}
		reported = true
		row.Params = params
		return true
	}
	// A channel already delivered this moment. Retry its chat record without
	// another send or pacing event. Passive kinds can now be muted or quiet;
	// Direct deliveries retain their chat record across temporary kind mutes.
	// Canonical delivery receipts survive either presentation outcome.
	if row.Channel != "" && row.Reason == "chat report" {
		if !report() {
			_ = s.store.SetEngagementMomentState(ctx, row.ID, row.State, "chat report",
				storage.EngagementMomentRow{HoldUntil: s.chatReportRetryAt(engagement.Kind(row.Kind), now).UnixMilli()})
			return
		}
		if row.State == storage.EngagementStateSent {
			reason := "chat reported"
			if quiet {
				reason = "quiet report skipped"
			}
			_ = s.store.SetEngagementMomentState(ctx, row.ID, row.State, reason, storage.EngagementMomentRow{})
			return
		}
		if !m.ExpiresAt.IsZero() && !m.ExpiresAt.After(now) {
			_ = s.store.SetEngagementMomentState(ctx, row.ID, storage.EngagementStateExpired, "expired", storage.EngagementMomentRow{})
			return
		}
		if err := s.store.SetEngagementMomentState(ctx, row.ID, row.State, "partly sent", storage.EngagementMomentRow{}); err != nil {
			return
		}
		row.Reason = "partly sent"
	}
	fatigue := s.fatigue(ctx, spec.Kind)

	s.mu.Lock()
	prefs := s.prefs
	if s.paused[spec.Kind] {
		prefs.Enabled = copyBoolMap(prefs.Enabled)
		prefs.Enabled[spec.Kind] = false
	}
	verdict := engagement.Pace(m, spec, prefs, s.state, now)
	score := engagement.Score(m, spec, prefs, fatigue, now)
	s.mu.Unlock()

	// A digest or a hold whose time has come is delivered now, quietly for a
	// digest and at its own level for a hold.
	holdExpired := row.State == storage.EngagementStateScheduled && row.HoldUntil > 0 && row.HoldUntil <= now.UnixMilli()
	switch verdict.Mode {
	case engagement.ModeSuppress:
		_ = s.store.SetEngagementMomentState(ctx, row.ID, storage.EngagementStateSuppressed, verdict.Reason, storage.EngagementMomentRow{UpdatedAt: now.UnixMilli(), Score: score})
		return
	case engagement.ModeHold:
		// A hold is a reason not to deliver yet (quiet hours, the budget, a
		// retry); it is re-evaluated when its time comes, never forced.
		_ = s.store.SetEngagementMomentState(ctx, row.ID, storage.EngagementStateScheduled, verdict.Reason,
			storage.EngagementMomentRow{UpdatedAt: now.UnixMilli(), Score: score, HoldUntil: verdict.HoldUntil.UnixMilli()})
		return
	case engagement.ModeDigest:
		if !holdExpired || prefs.Quiet.Contains(now) {
			_ = s.store.SetEngagementMomentState(ctx, row.ID, storage.EngagementStateScheduled, verdict.Reason,
				storage.EngagementMomentRow{UpdatedAt: now.UnixMilli(), Score: score, HoldUntil: verdict.HoldUntil.UnixMilli()})
			return
		}
		verdict.Mode = engagement.ModeInterrupt
		if verdict.Interruption == "" {
			verdict.Interruption = engagement.InterruptionPassive
		}
	case engagement.ModeInterrupt, engagement.ModeQuiet:
	default:
		return
	}

	presence := engagement.Presence{}
	if s.presence != nil {
		presence = s.presence(now)
	}
	// Non-urgent deliveries wait while the screen is locked.
	if held := engagement.HoldWhileLocked(m, verdict, presence, now); held.Mode == engagement.ModeHold {
		_ = s.store.SetEngagementMomentState(ctx, row.ID, storage.EngagementStateScheduled, held.Reason,
			storage.EngagementMomentRow{UpdatedAt: now.UnixMilli(), Score: score, HoldUntil: held.HoldUntil.UnixMilli()})
		return
	}
	// A delivery an earlier pass made that the store did not take.
	s.mu.Lock()
	earlier, unrecorded := s.unrecorded[row.ID]
	s.mu.Unlock()

	// A moment with channels the user chose goes to each of them that can be
	// reached; when none can, it waits (until it expires) for one to come
	// back, never going to a channel the user did not pick. A moment without
	// chosen channels goes where the router picks.
	markSent := func(delivered string) {
		reason, holdUntil := verdict.Reason, int64(0)
		// A quiet delivery never posts to the chat, so it never waits on a
		// chat report retry.
		if !quiet && !report() {
			reason = "chat report"
			holdUntil = s.chatReportRetryAt(engagement.Kind(row.Kind), now).UnixMilli()
		}
		if err := s.store.SetEngagementMomentState(ctx, row.ID, storage.EngagementStateSent, reason, storage.EngagementMomentRow{
			Score: score, Channel: delivered, Level: string(verdict.Interruption), SentAt: now.UnixMilli(), HoldUntil: holdUntil,
		}); err != nil {
			log.Printf("[engagement] recording %s as sent: %v", row.Kind, err)
			s.noteUnrecorded(row.ID, engagementUnrecorded{
				channels: delivered, paced: true, sentAt: now.UnixMilli(),
				level: string(verdict.Interruption), score: score,
			})
		} else {
			s.noteUnrecorded(row.ID, engagementUnrecorded{})
		}
		if !quiet && !earlier.paced {
			s.mu.Lock()
			s.state.Record(m, verdict, now)
			s.mu.Unlock()
			if s.paceChanged != nil {
				s.paceChanged()
			}
		}
		if !quiet {
			if err := s.autoPause(ctx, m.Kind); err != nil {
				log.Printf("[engagement] pausing kind: %v", err)
			}
		}
	}
	if quiet {
		// A quiet kind never toasts, pushes, texts or posts to the chat: the
		// moment is recorded as delivered on the quiet channel, and Today
		// shows it as a quiet line. Channels an earlier pass reached stay on
		// the record. Quiet deliveries never consume notification pacing.
		delivered := string(engagement.ChannelTodayQuiet)
		if row.Channel != "" {
			delivered = row.Channel + "+" + delivered
		}
		markSent(delivered)
		return
	}
	// Capture interruptions are retired. Preserve the unchosen stop as its
	// existing chat report, without sending a desktop or phone notification.
	if m.Kind == engagement.KindCapturePaused {
		markSent(string(engagement.ChannelChat))
		return
	}
	if s.deliver.Report != nil && engagementMomentIsStatus(m, now) {
		// Chat records the result even if every chosen channel is unavailable.
		// Reporting does not count as an external delivery or consume pacing;
		// explicit choices retain their ordinary delivery and retry path.
		if !report() {
			_ = s.store.SetEngagementMomentState(ctx, row.ID, storage.EngagementStateScheduled, "chat report",
				storage.EngagementMomentRow{UpdatedAt: now.UnixMilli(), Score: score, HoldUntil: s.chatReportRetryAt(engagement.Kind(row.Kind), now).UnixMilli()})
			return
		}
		if len(m.Channels) == 0 {
			markSent(string(engagement.ChannelChat))
			return
		}
	}
	holdForBudget := func() bool {
		if s.notificationAllowed(row, m, prefs) {
			return false
		}
		_ = s.store.SetEngagementMomentState(ctx, row.ID, storage.EngagementStateScheduled, "budget",
			storage.EngagementMomentRow{UpdatedAt: now.UnixMilli(), Score: score, HoldUntil: engagementNextMidnight(now).UnixMilli()})
		return true
	}
	if s.deliver.Report != nil && m.Kind == engagement.KindBriefReady {
		if !report() {
			_ = s.store.SetEngagementMomentState(ctx, row.ID, storage.EngagementStateScheduled, "chat report",
				storage.EngagementMomentRow{HoldUntil: s.chatReportRetryAt(engagement.Kind(row.Kind), now).UnixMilli()})
			return
		}
		if holdForBudget() {
			return
		}
		delivered := string(engagement.ChannelChat)
		for _, channel := range []engagement.Channel{engagement.ChannelDesktop, engagement.ChannelPush} {
			if sender := s.senderFor(channel); sender != nil {
				if err := sender(ctx, row, m, verdict, s.render(spec, m)); err == nil {
					delivered += "+" + string(channel)
					s.notificationSent(row, m)
				} else {
					log.Printf("[engagement] %s delivery failed on %s: %v", row.Kind, channel, err)
				}
			}
		}
		markSent(delivered)
		return
	}
	if s.deliver.Report != nil && (m.Kind == engagement.KindFirstWeek || m.Kind == engagement.KindWeeklyRecap) {
		if !report() {
			_ = s.store.SetEngagementMomentState(ctx, row.ID, storage.EngagementStateScheduled, "chat report",
				storage.EngagementMomentRow{HoldUntil: s.chatReportRetryAt(engagement.Kind(row.Kind), now).UnixMilli()})
			return
		}
		delivered := string(engagement.ChannelChat)
		if m.Params["stage"] != "welcome" {
			if holdForBudget() {
				return
			}
			channel := engagement.ChannelForKind(spec.Kind, verdict, presence, now)
			if sender := s.senderFor(channel); sender != nil {
				if err := sender(ctx, row, m, verdict, s.render(spec, m)); err == nil {
					delivered += "+" + string(channel)
					s.notificationSent(row, m)
				}
			}
		}
		markSent(delivered)
		return
	}

	// The channels an earlier pass already reached are never sent to again,
	// even when that pass could not record them, or which ones were left.
	reached := row.Channel
	if unrecorded {
		reached = earlier.channels
	}
	if reached != "" && (len(m.Channels) > 0 || unrecorded) {
		m.Channels = remainingChannels(m.Channels, strings.Split(reached, "+"))
		if len(m.Channels) == 0 {
			markSent(reached)
			return
		}
	}
	channels := engagement.RequestedChannels(m, presence)
	if len(channels) == 0 {
		if len(m.Channels) > 0 {
			_ = s.store.SetEngagementMomentState(ctx, row.ID, storage.EngagementStateScheduled, "no chosen channel",
				storage.EngagementMomentRow{UpdatedAt: now.UnixMilli(), Score: score, HoldUntil: now.Add(engagementRetryDelay).UnixMilli()})
			return
		}
		channels = []engagement.Channel{engagement.ChannelForKind(spec.Kind, verdict, presence, now)}
	}
	if holdForBudget() {
		return
	}
	text := s.render(spec, m)
	var sent, stopped []string
	for _, channel := range channels {
		sender := s.senderFor(channel)
		if sender == nil {
			continue
		}
		if err := sender(ctx, row, m, verdict, text); err != nil {
			log.Printf("[engagement] %s delivery failed on %s: %v", row.Kind, channel, err)
			if errors.Is(err, errEngagementTextPermanent) {
				stopped = append(stopped, string(channel))
			}
			continue
		}
		sent = append(sent, string(channel))
		s.notificationSent(row, m)
	}
	if len(stopped) > 0 {
		m.Channels = remainingChannels(m.Channels, stopped)
		if len(sent) == 0 && len(m.Channels) == 0 {
			if reached != "" {
				markSent(reached)
				return
			}
			_ = s.store.SetEngagementMomentState(ctx, row.ID, storage.EngagementStateSuppressed, "text needs attention in Settings", storage.EngagementMomentRow{UpdatedAt: now.UnixMilli(), Score: score})
			return
		}
		if err := s.store.SetEngagementMomentParams(ctx, row.ID, momentParamsJSON(m, reported)); err != nil {
			log.Printf("[engagement] recording a stopped text: %v", err)
		}
	}
	if len(sent) == 0 {
		reason := "retry"
		if len(channels) == 1 && s.senderFor(channels[0]) == nil {
			reason = "no channel"
		}
		_ = s.store.SetEngagementMomentState(ctx, row.ID, storage.EngagementStateScheduled, reason,
			storage.EngagementMomentRow{UpdatedAt: now.UnixMilli(), Score: score, HoldUntil: now.Add(engagementRetryDelay).UnixMilli()})
		return
	}
	delivered := strings.Join(sent, "+")
	if reached != "" {
		delivered = reached + "+" + delivered
	}
	// A chosen channel that could not be reached or failed keeps the moment
	// open for that channel alone: the ones that worked are not sent again.
	if remaining := remainingChannels(m.Channels, sent); len(remaining) > 0 {
		reason := "partly sent"
		if !report() {
			reason = "chat report"
		}
		m.Channels = remaining
		if err := s.store.SetEngagementMomentParams(ctx, row.ID, momentParamsJSON(m, reported)); err != nil {
			// The remaining channels could not be recorded: the moment
			// stays open with every chosen channel, and the next pass
			// leaves out the ones the delivered list below names.
			log.Printf("[engagement] recording a partial delivery of %s: %v", row.Kind, err)
		}
		if err := s.store.SetEngagementMomentState(ctx, row.ID, storage.EngagementStateScheduled, reason, storage.EngagementMomentRow{
			Score: score, Channel: delivered, SentAt: now.UnixMilli(), HoldUntil: now.Add(engagementRetryDelay).UnixMilli(),
		}); err != nil {
			log.Printf("[engagement] recording a partial delivery of %s: %v", row.Kind, err)
			s.noteUnrecorded(row.ID, engagementUnrecorded{channels: delivered, sentAt: now.UnixMilli(), score: score})
		} else {
			s.noteUnrecorded(row.ID, engagementUnrecorded{})
		}
		return
	}
	markSent(delivered)
}

func (s *engagementService) senderFor(channel engagement.Channel) func(context.Context, storage.EngagementMomentRow, engagement.Moment, engagement.Verdict, engagementText) error {
	switch channel {
	case engagement.ChannelPush:
		return s.deliver.Push
	case engagement.ChannelDesktop:
		return s.deliver.Desktop
	case engagement.ChannelText:
		return s.deliver.Text
	case engagement.ChannelEmail:
		return s.deliver.Email
	default:
		return nil
	}
}

func (s *engagementService) render(spec engagement.KindSpec, m engagement.Moment) engagementText {
	lang := "en"
	if s.lang != nil {
		lang = s.lang()
	}
	if m.Params["_activity_thread"] != "" {
		if s.threadOutcomeCopy != nil {
			if text, ok := s.threadOutcomeCopy(m, lang); ok {
				return text
			}
		}
		return engagementText{}
	}
	set, _ := engagement.Copy(spec.Kind, lang, m.Params)
	actions := map[string]string{}
	for _, action := range spec.Actions {
		actions[action.ID] = set.Actions[action.ID]
	}
	text := engagementText{
		PushBody: set.PushBody,
		Title:    engagement.Render(set.Sealed, mainChatCopyParams(m.Params, lang)),
		Actions:  actions,
		Category: engagementCategory(spec.Kind),
	}
	if m.Kind == engagement.KindFirstWeek {
		text.Title = engagement.FirstWeekText(m.Params["stage"], m.Params["first_brief"], m.Params["history"], m.Params["name"], lang)
	}
	if set.EmailSubject != "" {
		text.EmailSubject = engagement.Render(set.EmailSubject, m.Params)
		text.EmailBody = renderEmailBody(set.EmailBody, m.Params)
	}
	return sanitizeEngagementText(text)
}

// engagementCategory maps a kind to the phone's notification category.
func engagementCategory(kind engagement.Kind) string {
	switch kind {
	case engagement.KindBriefReady:
		return "YANSU_BRIEF"
	default:
		return "YANSU_MOMENT"
	}
}

func (s *engagementService) fatigue(ctx context.Context, kind engagement.Kind) float64 {
	stats, err := s.store.EngagementStatsBetween(ctx, s.now().Add(-engagementStatsWindow), s.now())
	if err != nil {
		return 0
	}
	stat := stats[string(kind)]
	if stat.Sent == 0 {
		return 0
	}
	return float64(stat.DismissedOrMuted) / float64(stat.Sent)
}

func (s *engagementService) autoPause(ctx context.Context, kind engagement.Kind) error {
	if s.kindQuiet(kind) {
		return nil
	}
	if spec, _ := engagement.Spec(kind); spec.Direct {
		return nil
	}
	since := s.now().Add(-engagementStatsWindow)
	s.mu.Lock()
	resumeBoundary := s.resumedAt[kind]
	if !resumeBoundary.IsZero() && !resumeBoundary.Before(since) {
		since = resumeBoundary.Add(time.Millisecond)
	}
	s.mu.Unlock()
	stats, err := s.store.EngagementStatsBetween(ctx, since, s.now())
	if err != nil {
		return err
	}
	stat := stats[string(kind)]
	if stat.Sent < engagementAutoPauseMinSent {
		return nil
	}
	if float64(stat.DismissedOrMuted)/float64(stat.Sent) < engagementAutoPauseRate {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// A concurrent Resume wins over a calculation made with older receipts.
	if !s.resumedAt[kind].Equal(resumeBoundary) || s.paused[kind] {
		return nil
	}
	pref := s.prefRow(kind)
	pref.Enabled = 0
	control := s.controlRow(kind)
	control.Paused = true
	if err := s.store.SaveEngagementKindControl(ctx, pref, control); err != nil {
		return err
	}
	s.paused[kind] = true
	s.prefs.Enabled[kind] = false
	if s.settingsChanged != nil {
		s.settingsChanged(s.prefsCopyLocked())
	}
	log.Printf("[engagement] pausing %s: %d of %d recent deliveries were dismissed or muted", kind, stat.DismissedOrMuted, stat.Sent)
	return nil
}

// Paused reports whether a kind is paused by its own receipts.
func (s *engagementService) Paused(kind engagement.Kind) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.paused[kind]
}

func (s *engagementService) load(ctx context.Context) error {
	prefs, err := s.store.ListEngagementPrefs(ctx)
	if err != nil {
		return err
	}
	rules, err := s.store.GetEngagementRules(ctx)
	if err != nil {
		return err
	}
	now := s.now()
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	pacing, err := s.store.EngagementPacingBetween(ctx, now.Add(-engagementStatsWindow), now)
	if err != nil {
		return err
	}
	controls := map[engagement.Kind]storage.EngagementKindControl{}
	for _, p := range prefs {
		control, err := s.store.GetEngagementKindControl(ctx, p.Kind)
		if err != nil {
			return err
		}
		controls[engagement.Kind(p.Kind)] = control
	}
	s.mu.Lock()
	if rules != nil {
		s.prefs.Quiet = engagement.QuietWindow{
			Start: time.Duration(rules.QuietStartMinutes) * time.Minute,
			End:   time.Duration(rules.QuietEndMinutes) * time.Minute,
		}
		s.prefs.DailyInterruptBudget = rules.DailyInterruptBudget
		if rules.DigestMinutes != nil {
			s.prefs.DigestTimes = make([]time.Duration, 0, len(rules.DigestMinutes))
			for _, minutes := range rules.DigestMinutes {
				s.prefs.DigestTimes = append(s.prefs.DigestTimes, time.Duration(minutes)*time.Minute)
			}
		}
	}
	stored := map[engagement.Kind]bool{}
	for _, p := range prefs {
		kind := engagement.Kind(p.Kind)
		if p.Weight >= 0 {
			s.prefs.Weights[kind] = p.Weight
		}
		if p.Enabled >= 0 {
			stored[kind] = true
			s.prefs.Enabled[kind] = p.Enabled == 1
			if p.Enabled == 1 && p.UpdatedAt > 0 {
				s.resumedAt[kind] = time.UnixMilli(p.UpdatedAt)
			}
		}
		control := controls[kind]
		if control.Paused {
			s.paused[kind] = true
			s.prefs.Enabled[kind] = false
		}
		if control.ResumedAt > 0 {
			s.resumedAt[kind] = time.UnixMilli(control.ResumedAt)
		}
		if control.DeliveryMode == string(engagement.KindDeliveryQuietOnToday) && engagement.QuietOnTodayAllowed(kind) {
			s.prefs.QuietOnToday[kind] = true
		}
		if p.MutedUntil > 0 {
			s.prefs.MutedUntil[kind] = time.UnixMilli(p.MutedUntil)
		}
	}
	// A kind added after the user picked Off or Fewer follows that level
	// instead of its default.
	s.prefs = engagement.FillMissingKinds(s.prefs, stored)
	// Restore pacing from the channel delivery, which can happen on a later
	// day than the first durable chat card used for engagement measurement.
	s.state = engagement.NewPaceState()
	s.state.Day = now.Format("2006-01-02")
	for _, row := range pacing {
		sentAt := time.UnixMilli(row.At)
		if sentAt.After(s.state.LastSentByKey[row.DedupeKey]) {
			s.state.LastSentByKey[row.DedupeKey] = sentAt
		}
		if sentAt.Before(midnight) {
			continue
		}
		s.state.SentByKindToday[engagement.Kind(row.Kind)]++
		switch engagement.Interruption(row.Level) {
		case engagement.InterruptionActive:
			if spec, ok := engagement.Spec(engagement.Kind(row.Kind)); !ok || !spec.Requested {
				s.state.InterruptsToday++
			}
		case engagement.InterruptionTimeSensitive:
			if s.prefs.Quiet.Contains(sentAt) && sentAt.After(s.state.LastQuietBreak) {
				s.state.LastQuietBreak = sentAt
			}
		default:
		}
	}
	s.mu.Unlock()
	// A kind the user tired of stays paused across a restart.
	for _, kind := range engagement.AllKinds() {
		if err := s.autoPause(ctx, kind); err != nil {
			return err
		}
	}
	return nil
}

func (s *engagementService) prefRow(kind engagement.Kind) storage.EngagementPrefRow {
	row := storage.EngagementPrefRow{Kind: string(kind), Weight: -1, Enabled: -1, UpdatedAt: s.now().UnixMilli()}
	if w, ok := s.prefs.Weights[kind]; ok {
		row.Weight = w
	}
	if on, ok := s.prefs.Enabled[kind]; ok {
		if on {
			row.Enabled = 1
		} else {
			row.Enabled = 0
		}
	}
	if until, ok := s.prefs.MutedUntil[kind]; ok && !until.IsZero() {
		row.MutedUntil = until.UnixMilli()
	}
	return row
}

// controlRow is the persisted explanation of a kind's switch: its pause, its
// resume boundary and its quiet-on-Today mode, so every writer keeps the
// parts it does not change. The caller holds s.mu.
func (s *engagementService) controlRow(kind engagement.Kind) storage.EngagementKindControl {
	control := storage.EngagementKindControl{Paused: s.paused[kind], DeliveryMode: string(s.prefs.KindDeliveryMode(kind))}
	if resumed := s.resumedAt[kind]; !resumed.IsZero() {
		control.ResumedAt = resumed.UnixMilli()
	}
	return control
}

func (s *engagementService) openMoment(ctx context.Context, dedupeKey string) (*storage.EngagementMomentRow, error) {
	return s.store.GetOpenEngagementMomentByKey(ctx, dedupeKey)
}

func (s *engagementService) nudge() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *engagementService) run() {
	defer s.wg.Done()
	ticker := time.NewTicker(s.tick)
	defer ticker.Stop()
	s.Pass(s.ctx)
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			s.Pass(s.ctx)
		case <-s.wake:
			s.Pass(s.ctx)
		}
	}
}

func momentFromRow(row storage.EngagementMomentRow) engagement.Moment {
	m := engagement.Moment{
		ID:           row.ID,
		Kind:         engagement.Kind(row.Kind),
		SubjectID:    row.SubjectID,
		DedupeKey:    row.DedupeKey,
		CreatedAt:    time.UnixMilli(row.CreatedAt),
		HardDeadline: row.Level == "deadline",
		Stakes:       row.Stakes,
		Params:       map[string]string{},
	}
	if row.DueAt > 0 {
		m.DueAt = time.UnixMilli(row.DueAt)
	}
	if row.ExpiresAt > 0 {
		m.ExpiresAt = time.UnixMilli(row.ExpiresAt)
	}
	if strings.TrimSpace(row.Params) != "" {
		_ = json.Unmarshal([]byte(row.Params), &m.Params)
	}
	// Reserved metadata leaves Params before any prose privacy/redaction pass.
	m.ReadReceipt = m.Params[engagementReadReceiptParam]
	delete(m.Params, engagementReadReceiptParam)
	if names, ok := m.Params[engagementChannelsParam]; ok {
		m.Channels = engagement.ParseChannels(strings.Split(names, ","))
		delete(m.Params, engagementChannelsParam)
	}
	delete(m.Params, engagementChatReportedParam)
	return m
}

func engagementReportedInChat(row storage.EngagementMomentRow) bool {
	var stored struct {
		Reported string `json:"_chat_reported"`
	}
	return json.Unmarshal([]byte(row.Params), &stored) == nil && stored.Reported == "true"
}

// remainingChannels are the chosen channels not in sent, in order.
func remainingChannels(chosen []engagement.Channel, sent []string) []engagement.Channel {
	done := map[string]bool{}
	for _, c := range sent {
		done[c] = true
	}
	var out []engagement.Channel
	for _, c := range chosen {
		if !done[string(c)] {
			out = append(out, c)
		}
	}
	return out
}

// momentParamsJSON is the stored params of a moment, its chosen channels
// among them (as Submit writes them). Only the service can set the optional
// reporting marker; producer-supplied parameters cannot suppress a report.
func momentParamsJSON(m engagement.Moment, reported ...bool) string {
	stored := make(map[string]string, len(m.Params)+2)
	for k, v := range m.Params {
		stored[k] = v
	}
	delete(stored, engagementChannelsParam)
	delete(stored, engagementChatReportedParam)
	delete(stored, engagementReadReceiptParam)
	if m.ReadReceipt != "" {
		stored[engagementReadReceiptParam] = m.ReadReceipt
	}
	if len(reported) > 0 && reported[0] {
		stored[engagementChatReportedParam] = "true"
	}
	if len(m.Channels) > 0 {
		names := make([]string, 0, len(m.Channels))
		for _, c := range m.Channels {
			names = append(names, string(c))
		}
		stored[engagementChannelsParam] = strings.Join(names, ",")
	}
	b, err := json.Marshal(stored)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// renderEmailBody fills an email template paragraph by paragraph, so the
// blank lines between paragraphs survive (Render makes each one line).
func renderEmailBody(template string, params map[string]string) string {
	paragraphs := strings.Split(template, "\n\n")
	out := make([]string, 0, len(paragraphs))
	for _, paragraph := range paragraphs {
		if strings.TrimSpace(paragraph) == "{detail}" {
			// The result keeps its own lines.
			if detail := strings.TrimSpace(params["detail"]); detail != "" {
				out = append(out, detail)
			}
			continue
		}
		if line := engagement.Render(paragraph, params); line != "" {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n\n")
}

func copyFloatMap(in map[engagement.Kind]float64) map[engagement.Kind]float64 {
	out := make(map[engagement.Kind]float64, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func copyBoolMap(in map[engagement.Kind]bool) map[engagement.Kind]bool {
	out := make(map[engagement.Kind]bool, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func copyTimeMap(in map[engagement.Kind]time.Time) map[engagement.Kind]time.Time {
	out := make(map[engagement.Kind]time.Time, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// chatReportRetryAt is when a chat report that could not be posted is tried
// again: the next hourly batch for activity-driven cards, else five minutes.
func (s *engagementService) chatReportRetryAt(kind engagement.Kind, now time.Time) time.Time {
	if s.chatReportRetry != nil {
		return s.chatReportRetry(kind, now)
	}
	return now.Add(engagementRetryDelay)
}
