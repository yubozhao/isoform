package main

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"wails-gui/internal/handoff"
	"wails-gui/storage"
)

// fakeMobileBriefClock is the injectable clock behind the service's now func.
type fakeMobileBriefClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeMobileBriefClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeMobileBriefClock) Set(value time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = value
}

func (c *fakeMobileBriefClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func mobileBriefTestDay(hour, minute int) time.Time {
	return time.Date(2026, 10, 9, hour, minute, 0, 0, time.UTC)
}

// newTestMobileBriefService wires a service to a temporary state directory, a
// fake clock, and a gather step that counts calls and returns fixed input. No
// source is read and no model is called.
func newTestMobileBriefService(t *testing.T, clock *fakeMobileBriefClock) (*mobileBriefService, *atomic.Int32) {
	t.Helper()
	// Brief privacy checks also read memory; isolate that data from the real app.
	t.Setenv("YANSU_AGENT_HOME", t.TempDir())
	t.Setenv("YANSU_MOBILE_STATE_DIR", t.TempDir())
	// Delivery must be able to prove the mail holdback is empty. An absent
	// store is an unavailable privacy dependency, not a successful empty read.
	state, err := storage.New(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = state.Close() })
	svc := newMobileBriefService(&App{store: state})
	work, err := handoff.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	svc.app.handoffStore = work
	for _, id := range []string{"h-morning", "h-evening", "h-on_request", "h1", "h-1", "h-2"} {
		if _, err := work.Save(handoff.Entry{ID: id, Title: "Shipped the plan", Source: "manual", Status: handoff.StatusCompleted}); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(svc.Stop)
	svc.now = clock.Now
	svc.sleepState = nil // Synthetic sleep observations are supplied by each test.
	svc.location = time.UTC
	svc.tick = time.Hour
	svc.power = func(string) (func(), error) { return func() {}, nil }
	// No voice in tests: a zero speaker stores briefs as text only.
	svc.speaker = mobileBriefSpeaker{}
	var gathers atomic.Int32
	svc.gather = func(_ context.Context, slot string, windowStart, asOf time.Time) (mobileBriefInput, error) {
		gathers.Add(1)
		return mobileBriefInput{
			Slot:        slot,
			WindowStart: windowStart,
			AsOf:        asOf,
			Location:    time.UTC,
			Moved: []mobileBriefCandidate{{
				SourceKind: "history", SourceID: "h-" + slot, Title: "Shipped the plan", At: asOf.Add(-time.Hour),
			}},
		}, nil
	}
	// Lifecycle fixtures need a publishable reading. Model execution has its
	// own tests; this seam supplies a substantial source-bound reading when
	// a small template would otherwise turn the slot quiet under the 100-word
	// rule. Hygiene tests restore the production composer explicitly.
	svc.compose = composePublishableMobileBriefForTest
	return svc, &gathers
}

func composePublishableMobileBriefForTest(ctx context.Context, app *App, brief MobileBriefV1, cards []MobileBriefCard) ([]MobileBriefPara, string, []mobileBriefCardWords) {
	paragraphs, status, words := composeMobileBriefNarrative(ctx, app, brief, cards)
	if len(paragraphs) == 0 && len(brief.Evidence) > 0 {
		refs := make([]string, 0, len(brief.Evidence))
		for _, ref := range brief.Evidence {
			refs = append(refs, ref.Ref)
		}
		paragraphs = []MobileBriefPara{{Refs: refs}}
	}
	if len(paragraphs) > 0 && status != "ok" && mobileBriefNarrativeWords(paragraphs) < mobileBriefPublishMinWords {
		parts := strings.SplitN(mobileBriefPublishableTestReading, " The review can", 2)
		extra := []MobileBriefPara{{Text: parts[0], Refs: paragraphs[0].Refs}, {Text: "The review can" + parts[1], Refs: paragraphs[0].Refs}}
		end := len(paragraphs)
		if strings.ContainsAny(paragraphs[end-1].Text, "?？") {
			end--
		}
		paragraphs = append(append(append([]MobileBriefPara{}, paragraphs[:end]...), extra...), paragraphs[end:]...)
		if paragraphs[0].Text == "" {
			paragraphs = paragraphs[1:]
		}
	}
	return paragraphs, status, words
}

// A fixed reading for lifecycle tests, attached only to their own retained
// source refs. Production never reads this text or pads a short brief.
const mobileBriefPublishableTestReading = "The release plan brings together the work that is ready for review and the decisions that still need an answer. The next review covers the proposed scope, the supporting notes, and the remaining questions about delivery. The source record keeps the draft and its current status together so the next conversation can start with the same facts. The review can compare the draft with the agreed requirements, identify any gaps in the explanation, and prepare a clear reply about the next step. The existing notes remain available for that discussion, including the context behind the plan and the changes already recorded during this work."

func waitMobileBriefIdle(t *testing.T, svc *mobileBriefService) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		svc.mu.Lock()
		running := svc.running
		svc.mu.Unlock()
		if !running {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("generation did not finish")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestMobileBriefPendingSlotDueWindows(t *testing.T) {
	defaults := defaultMobileBriefSettings()
	none := func(string) bool { return false }
	has := func(ids ...string) func(string) bool {
		set := map[string]bool{}
		for _, id := range ids {
			set[id] = true
		}
		return func(id string) bool { return set[id] }
	}
	const morningID = "2026-10-09-morning"
	const eveningID = "2026-10-09-evening"
	cases := []struct {
		name       string
		now        time.Time
		exists     func(string) bool
		wantSlot   string
		wantDue    bool
		wantMissed []string
	}{
		{"before the morning build starts", mobileBriefTestDay(6, 49), none, "", false, nil},
		{"morning build starts ten minutes early", mobileBriefTestDay(6, 50), none, "morning", true, nil},
		{"at the morning slot", mobileBriefTestDay(7, 0), none, "morning", true, nil},
		{"late, within morning grace", mobileBriefTestDay(7, 29), none, "morning", true, nil},
		{"late morning, until noon", mobileBriefTestDay(11, 59), none, "morning", true, nil},
		{"from noon the morning gives way", mobileBriefTestDay(12, 0), none, "", false, []string{morningID}},
		{"evening after a missed morning", mobileBriefTestDay(18, 0), none, "evening", true, []string{morningID}},
		{"morning already stored", mobileBriefTestDay(8, 0), has(morningID), "", false, nil},
		{"evening already stored", mobileBriefTestDay(18, 30), has(eveningID), "", false, []string{morningID}},
		{"late evening, until midnight", mobileBriefTestDay(23, 59), none, "evening", true, []string{morningID}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			slot, due, missed := mobileBriefPendingSlot(tc.now, time.UTC, defaults, tc.exists)
			if slot != tc.wantSlot || due != tc.wantDue {
				t.Fatalf("slot=%q due=%v, want slot=%q due=%v", slot, due, tc.wantSlot, tc.wantDue)
			}
			if !reflect.DeepEqual(missed, tc.wantMissed) {
				t.Fatalf("missed = %v, want %v", missed, tc.wantMissed)
			}
		})
	}
}

func TestMobileBriefPendingSlotHonoursCustomTimes(t *testing.T) {
	settings := mobileBriefSettings{Enabled: true, Morning: "20:00", Evening: "08:30"}
	slot, due, _ := mobileBriefPendingSlot(mobileBriefTestDay(9, 0), time.UTC, settings, func(string) bool { return false })
	if !due || slot != "evening" {
		t.Fatalf("with evening at 08:30, 09:00 should be due for evening, got slot=%q due=%v", slot, due)
	}
}

func TestMobileBriefCatchUpOnStart(t *testing.T) {
	clock := &fakeMobileBriefClock{now: mobileBriefTestDay(7, 20)}
	svc, gathers := newTestMobileBriefService(t, clock)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	svc.Start(ctx)
	deadline := time.Now().Add(5 * time.Second)
	for !svc.store.exists("2026-10-09-morning") {
		if time.Now().After(deadline) {
			t.Fatal("the morning brief was not generated after start")
		}
		time.Sleep(5 * time.Millisecond)
	}
	svc.Stop()

	if got := gathers.Load(); got != 1 {
		t.Fatalf("gather ran %d times, want once", got)
	}
	latest, ok := svc.LatestMobileBrief()
	if !ok || latest.BriefID != "2026-10-09-morning" || latest.Slot != "morning" {
		t.Fatalf("latest = %+v (ok=%v)", latest, ok)
	}
	// Without the native runner no model text is written. The brief still opens with its
	// template sentence (design R74), so the status is "skipped" when nothing was picked.
	if latest.NarrativeStatus == "ok" {
		t.Fatalf("narrativeStatus = %q, want model text absent without the native runner", latest.NarrativeStatus)
	}
	if rows := svc.ListMobileBriefs(10); len(rows) != 1 || rows[0].BriefID != latest.BriefID {
		t.Fatalf("list = %+v", rows)
	}
	if brief, ok := svc.GetMobileBrief(latest.BriefID); !ok || brief.BriefID != latest.BriefID {
		t.Fatal("GetMobileBrief did not return the stored brief")
	}
	if _, ok := svc.GetMobileBrief("../2026-10-09-morning"); ok {
		t.Fatal("GetMobileBrief accepted a path-like ID")
	}
}

func TestMobileBriefScheduledSlotIsIdempotent(t *testing.T) {
	clock := &fakeMobileBriefClock{now: mobileBriefTestDay(7, 5)}
	svc, gathers := newTestMobileBriefService(t, clock)
	ctx := context.Background()

	svc.checkDue(ctx)
	svc.checkDue(ctx)
	clock.Advance(time.Hour)
	svc.checkDue(ctx)

	if got := gathers.Load(); got != 1 {
		t.Fatalf("gather ran %d times for one slot, want once", got)
	}
}

func TestMobileBriefMissedSlotIsRecordedNotGenerated(t *testing.T) {
	// The morning is missed, but the evening's catch-up grace is still open.
	clock := &fakeMobileBriefClock{now: mobileBriefTestDay(18, 20)}
	svc, gathers := newTestMobileBriefService(t, clock)
	// The schedule has been watched since midnight, so the morning counts.
	svc.runs.firstSeen(mobileBriefTestDay(0, 0))
	ctx := context.Background()

	svc.checkDue(ctx)
	svc.mu.Lock()
	missed := svc.missedLogged["2026-10-09-morning"]
	svc.mu.Unlock()
	if !missed {
		t.Fatal("the missed morning slot was not recorded")
	}
	if got := gathers.Load(); got != 1 || svc.store.exists("2026-10-09-morning") || !svc.store.exists("2026-10-09-evening") {
		t.Fatalf("want only the evening brief, %d generated", got)
	}
}

func TestMobileBriefDisabledScheduleSkipsDueCheck(t *testing.T) {
	clock := &fakeMobileBriefClock{now: mobileBriefTestDay(7, 5)}
	svc, gathers := newTestMobileBriefService(t, clock)
	if err := svc.config.save(mobileBriefSettings{Enabled: false, Morning: "07:00", Evening: "18:00"}); err != nil {
		t.Fatal(err)
	}
	svc.checkDue(context.Background())
	if got := gathers.Load(); got != 0 {
		t.Fatalf("a disabled schedule generated %d briefs", got)
	}
}

func TestMobileBriefOnDemandCooldownAndStarted(t *testing.T) {
	clock := &fakeMobileBriefClock{now: mobileBriefTestDay(10, 0)}
	svc, gathers := newTestMobileBriefService(t, clock)

	result, err := svc.RequestMobileBrief("settings")
	if err != nil || result.Status != "started" {
		t.Fatalf("first request = %+v, %v; want started", result, err)
	}
	waitMobileBriefIdle(t, svc)

	result, err = svc.RequestMobileBrief("settings")
	if err != nil {
		t.Fatal(err)
	}
	wantNext := mobileBriefTestDay(10, 10).UTC().Format(time.RFC3339)
	if result.Status != "cooldown" || result.NextAllowedAt != wantNext {
		t.Fatalf("second request = %+v, want cooldown until %s", result, wantNext)
	}

	result, err = svc.RequestMobileBrief("phone")
	if err != nil || result.Status != "making" {
		t.Fatalf("phone request = %+v, %v; want making, since cooldowns are per source", result, err)
	}
	waitMobileBriefIdle(t, svc)

	clock.Advance(10 * time.Minute)
	result, err = svc.RequestMobileBrief("settings")
	if err != nil || result.Status != "started" {
		t.Fatalf("request after cooldown = %+v, %v; want started", result, err)
	}
	waitMobileBriefIdle(t, svc)

	if got := gathers.Load(); got != 3 {
		t.Fatalf("gather ran %d times, want 3 on-demand generations", got)
	}
	if _, err := svc.RequestMobileBrief("cron"); err == nil {
		t.Fatal("an unknown request reason was accepted")
	}
}

func TestMobileBriefRunningStateBlocksSecondGeneration(t *testing.T) {
	clock := &fakeMobileBriefClock{now: mobileBriefTestDay(7, 5)}
	svc, gathers := newTestMobileBriefService(t, clock)
	entered := make(chan struct{}, 4)
	release := make(chan struct{})
	base := svc.gather
	svc.gather = func(ctx context.Context, slot string, windowStart, asOf time.Time) (mobileBriefInput, error) {
		entered <- struct{}{}
		<-release
		return base(ctx, slot, windowStart, asOf)
	}

	result, err := svc.RequestMobileBrief("settings")
	if err != nil || result.Status != "started" {
		t.Fatalf("request = %+v, %v; want started", result, err)
	}
	<-entered

	result, err = svc.RequestMobileBrief("phone")
	if err != nil || result.Status != "making" {
		t.Fatalf("second request while in flight = %+v, %v; want making", result, err)
	}
	svc.checkDue(context.Background())

	close(release)
	waitMobileBriefIdle(t, svc)
	if got := gathers.Load(); got != 1 {
		t.Fatalf("gather ran %d times while one generation was in flight, want 1", got)
	}
}

func TestMobileBriefPhoneCooldownQueuesAndCoalesces(t *testing.T) {
	clock := &fakeMobileBriefClock{now: mobileBriefTestDay(10, 0)}
	svc, gathers := newTestMobileBriefService(t, clock)
	t.Cleanup(svc.Stop)
	svc.mu.Lock()
	svc.lastOnDemand[mobileBriefReasonPhone] = clock.Now().Add(-mobileBriefCooldown + 100*time.Millisecond)
	svc.mu.Unlock()
	first, err := svc.RequestMobileBrief(mobileBriefReasonPhone)
	if err != nil || first.Status != "queued" {
		t.Fatalf("first request = %+v, %v", first, err)
	}
	ready, err := time.Parse(time.RFC3339, first.ReadyAt)
	if err != nil || !ready.After(clock.Now()) {
		t.Fatalf("readyAt = %q, %v; want a future time", first.ReadyAt, err)
	}
	second, err := svc.RequestMobileBrief(mobileBriefReasonPhone)
	if err != nil || second != first {
		t.Fatalf("coalesced request = %+v, %v; want %+v", second, err, first)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if gathers.Load() == 1 {
			waitMobileBriefIdle(t, svc)
			if got := gathers.Load(); got != 1 {
				t.Fatalf("generated %d times, want one", got)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("queued request did not start")
}

func TestMobileBriefQueuedPhoneRequestJoinsAnotherRun(t *testing.T) {
	clock := &fakeMobileBriefClock{now: mobileBriefTestDay(10, 0)}
	svc, gathers := newTestMobileBriefService(t, clock)
	t.Cleanup(svc.Stop)
	svc.mu.Lock()
	svc.lastOnDemand[mobileBriefReasonPhone] = clock.Now()
	svc.mu.Unlock()
	queued, err := svc.RequestMobileBrief(mobileBriefReasonPhone)
	if err != nil || queued.Status != "queued" {
		t.Fatalf("queued = %+v, %v", queued, err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	base := svc.gather
	svc.gather = func(ctx context.Context, slot string, from, to time.Time) (mobileBriefInput, error) {
		close(entered)
		<-release
		return base(ctx, slot, from, to)
	}
	started, err := svc.RequestMobileBrief(mobileBriefReasonSettings)
	if err != nil || started.Status != "started" {
		t.Fatalf("settings = %+v, %v", started, err)
	}
	<-entered
	joined, err := svc.RequestMobileBrief(mobileBriefReasonPhone)
	if err != nil || joined.Status != "making" {
		t.Fatalf("joined = %+v, %v", joined, err)
	}
	close(release)
	waitMobileBriefIdle(t, svc)
	if gathers.Load() != 1 {
		t.Fatalf("generated %d times", gathers.Load())
	}
}

func TestMobileBriefPowerAssertionHeldPerGeneration(t *testing.T) {
	clock := &fakeMobileBriefClock{now: mobileBriefTestDay(7, 5)}
	svc, _ := newTestMobileBriefService(t, clock)
	var mu sync.Mutex
	var reasons []string
	released := 0
	svc.power = func(reason string) (func(), error) {
		mu.Lock()
		reasons = append(reasons, reason)
		mu.Unlock()
		return func() {
			mu.Lock()
			released++
			mu.Unlock()
		}, nil
	}
	svc.checkDue(context.Background())

	mu.Lock()
	defer mu.Unlock()
	if !reflect.DeepEqual(reasons, []string{"scheduled"}) || released != 1 {
		t.Fatalf("power reasons=%v released=%d, want one scheduled hold that is released", reasons, released)
	}
}

func TestMobileBriefPowerAssertionFailureDoesNotBlock(t *testing.T) {
	clock := &fakeMobileBriefClock{now: mobileBriefTestDay(7, 5)}
	svc, gathers := newTestMobileBriefService(t, clock)
	svc.power = func(string) (func(), error) {
		return nil, errors.New("assertion refused")
	}
	svc.checkDue(context.Background())
	if got := gathers.Load(); got != 1 {
		t.Fatalf("a refused power assertion stopped generation (gather=%d)", got)
	}
}

func TestMobileBriefStopCancelsGenerationAndRejectsRequests(t *testing.T) {
	clock := &fakeMobileBriefClock{now: mobileBriefTestDay(6, 0)}
	svc, _ := newTestMobileBriefService(t, clock)
	entered := make(chan struct{}, 1)
	svc.gather = func(ctx context.Context, _ string, _, _ time.Time) (mobileBriefInput, error) {
		entered <- struct{}{}
		<-ctx.Done()
		return mobileBriefInput{}, ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	svc.Start(ctx)

	if result, err := svc.RequestMobileBrief("phone"); err != nil || result.Status != "making" {
		t.Fatalf("request = %+v, %v; want making", result, err)
	}
	<-entered

	stopped := make(chan struct{})
	go func() {
		svc.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop did not return while a generation was canceled")
	}
	if _, err := svc.RequestMobileBrief("settings"); err == nil {
		t.Fatal("a stopped service accepted a request")
	}
	if _, ok := svc.LatestMobileBrief(); ok {
		t.Fatal("a canceled generation stored a brief")
	}
	svc.Stop()
}

func TestMobileBriefStopWaitsForTodayRefresh(t *testing.T) {
	clock := &fakeMobileBriefClock{now: mobileBriefTestDay(6, 0)}
	svc, _ := newTestMobileBriefService(t, clock)
	svc.todayMu.Lock()
	svc.refreshTodaySoon()
	stopped := make(chan struct{})
	go func() {
		svc.Stop()
		close(stopped)
	}()
	deadline := time.After(2 * time.Second)
	for {
		svc.mu.Lock()
		stopping := svc.stopped
		svc.mu.Unlock()
		if stopping {
			break
		}
		select {
		case <-deadline:
			svc.todayMu.Unlock()
			t.Fatal("Stop did not begin")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	secondStop := make(chan struct{})
	go func() { svc.Stop(); close(secondStop) }()
	select {
	case <-stopped:
		svc.todayMu.Unlock()
		t.Fatal("Stop returned while the refresh was blocked")
	case <-secondStop:
		svc.todayMu.Unlock()
		t.Fatal("concurrent Stop returned while the refresh was blocked")
	case <-time.After(50 * time.Millisecond):
	}
	svc.todayMu.Unlock()
	for _, done := range []<-chan struct{}{stopped, secondStop} {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("Stop did not finish after the refresh")
		}
	}
	svc.refreshTodaySoon()
	svc.todayRefreshMu.Lock()
	refreshing := svc.todayRefreshing
	svc.todayRefreshMu.Unlock()
	if refreshing {
		t.Fatal("a refresh started after Stop")
	}
	if _, err := svc.finishedRead(MobileBriefV1{}); !errors.Is(err, errMobileBriefUnavailable) {
		t.Fatalf("finished read after Stop: %v", err)
	}
	if _, ok := svc.BriefPackage("2026-10-09-morning"); ok {
		t.Fatal("a package was accepted after Stop")
	}
	if _, changed := svc.PatchLatestMobileBrief(func(b MobileBriefV1) (MobileBriefV1, bool) {
		t.Error("patch ran after Stop")
		return b, true
	}, nil); changed {
		t.Fatal("a patch was accepted after Stop")
	}
}

func TestMobileBriefStopWaitsForGeneratedAudio(t *testing.T) {
	clock := &fakeMobileBriefClock{now: mobileBriefTestDay(7, 0)}
	svc, _ := newTestMobileBriefService(t, clock)
	svc.compose = testModelCompose // only model text is read aloud (design R74)
	entered, release := make(chan struct{}), make(chan struct{})
	var enteredOnce sync.Once
	svc.speaker = fakeSpeaker("en", testWAV(1), nil)
	svc.speaker.synthesize = func(context.Context, string, string) ([]byte, error) {
		enteredOnce.Do(func() { close(entered) })
		<-release
		return testWAV(1), nil
	}
	if _, err := svc.generate(context.Background(), mobileBriefSlotMorning, mobileBriefReasonSchedule); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("audio synthesis did not start")
	}
	stopped := make(chan struct{})
	go func() { svc.Stop(); close(stopped) }()
	select {
	case <-stopped:
		close(release)
		t.Fatal("Stop returned during audio synthesis")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not finish after audio synthesis")
	}
}

func TestMobileBriefWindowStart(t *testing.T) {
	clock := &fakeMobileBriefClock{now: mobileBriefTestDay(14, 0)}
	svc, _ := newTestMobileBriefService(t, clock)
	asOf := mobileBriefTestDay(14, 0)

	if got := svc.windowStart(asOf); !got.Equal(asOf.Add(-24 * time.Hour)) {
		t.Fatalf("no previous brief: window starts %s, want 24 hours before asOf", got)
	}

	previous := mobileBriefStoredBrief(t, "2026-10-09", mobileBriefSlotMorning, 7)
	previous.AsOf = mobileBriefTestDay(7, 0).Format(time.RFC3339)
	// A brief the window can start from has written words.
	previous.NarrativeStatus, previous.Narrative = "ok", []MobileBriefPara{{Text: madeAheadWords()}}
	if err := svc.store.save(previous, asOf); err != nil {
		t.Fatal(err)
	}
	if got := svc.windowStart(asOf); !got.Equal(mobileBriefTestDay(7, 0)) {
		t.Fatalf("previous brief: window starts %s, want its asOf", got)
	}

	farAsOf := mobileBriefTestDay(14, 0).Add(10 * 24 * time.Hour)
	if got := svc.windowStart(farAsOf); !got.Equal(farAsOf.Add(-mobileBriefMaxWindow)) {
		t.Fatalf("a window older than seven days starts %s, want the seven-day floor", got)
	}
}

func TestMobileBriefScheduleViewAndValidation(t *testing.T) {
	clock := &fakeMobileBriefClock{now: mobileBriefTestDay(9, 0)}
	svc, _ := newTestMobileBriefService(t, clock)

	view, err := svc.Schedule()
	if err != nil {
		t.Fatal(err)
	}
	adaptive := false
	want := MobileBriefSchedule{Enabled: true, Morning: "07:00", Evening: "18:00", AdaptiveTiming: &adaptive}
	if !reflect.DeepEqual(view, want) {
		t.Fatalf("default view = %+v, want %+v", view, want)
	}
	if view.NarrativeAvailable {
		t.Fatal("narrativeAvailable must be false without the native runner")
	}

	if _, err := svc.SetSchedule(MobileBriefSchedule{Enabled: true, Morning: "7am", Evening: "18:00"}); err == nil {
		t.Fatal("an invalid morning time was accepted")
	}
	saved, err := svc.SetSchedule(MobileBriefSchedule{Enabled: false, Morning: "06:30", Evening: "21:15"})
	if err != nil {
		t.Fatal(err)
	}
	if saved.Enabled || saved.Morning != "06:30" || saved.Evening != "21:15" {
		t.Fatalf("saved view = %+v", saved)
	}
	if got, _ := svc.Schedule(); !reflect.DeepEqual(got, saved) {
		t.Fatalf("Schedule() = %+v after save, want %+v", got, saved)
	}
}

// TestMobileBriefPowerAssertionAcquireAndRelease takes the real platform
// assertion where one exists. Release is idempotent, so calling it twice must
// not fail.
func TestMobileBriefPowerAssertionAcquireAndRelease(t *testing.T) {
	release, err := holdMobileBriefPowerAssertion("Yansu brief test")
	if err != nil {
		t.Skipf("the platform did not grant a sleep assertion here: %v", err)
	}
	release()
	release()
}

func TestBriefMissedDetailsNameOnlyWhatTheRunRecordProves(t *testing.T) {
	s := &mobileBriefService{location: time.UTC}
	got := s.briefMissedDetails(mobileBriefRun{ScheduledAt: "2026-10-31T07:30:00Z"})
	if !reflect.DeepEqual(got, map[string]string{"time": "07:30"}) {
		t.Fatalf("run without attempts = %v, want only its time", got)
	}
	got = s.briefMissedDetails(mobileBriefRun{ScheduledAt: "2026-10-31T07:30:00Z", Attempts: 2})
	if got["reason"] != "failed" || got["time"] != "07:30" {
		t.Fatalf("failed run = %v, want the failed reason and its time", got)
	}
	got = s.briefMissedDetails(mobileBriefRun{ScheduledAt: "2026-10-31T07:30:00Z", Reason: mobileBriefMissedAsleep})
	if got["reason"] != mobileBriefMissedAsleep {
		t.Fatalf("asleep run = %v, want the recorded reason", got)
	}
	if got := s.briefMissedDetails(mobileBriefRun{}); len(got) != 0 {
		t.Fatalf("empty run = %v, want nothing guessed", got)
	}
	params := briefMissedParams("morning", map[string]string{"reason": "failed", "time": "07:30", "other": "x"})
	if params["reason"] != "failed" || params["time"] != "07:30" || params["other"] != "" || params["slot"] == "" {
		t.Fatalf("params = %v", params)
	}
}
