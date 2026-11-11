package main

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// madeAheadRig is a test service on UTC whose compose step counts builds. The
// compose result is set per test: writeText makes a brief with words, otherwise
// the build writes no text (its status is unavailable and it has no paragraphs).
type madeAheadRig struct {
	svc       *mobileBriefService
	clock     *fakeMobileBriefClock
	builds    *atomic.Int32
	writeText *atomic.Bool
}

func newMadeAheadRig(t *testing.T, start time.Time) madeAheadRig {
	t.Helper()
	clock := &fakeMobileBriefClock{now: start}
	svc, _ := newTestMobileBriefService(t, clock)
	rig := madeAheadRig{svc: svc, clock: clock, builds: &atomic.Int32{}, writeText: &atomic.Bool{}}
	rig.writeText.Store(true)
	svc.compose = func(_ context.Context, _ *App, brief MobileBriefV1, cards []MobileBriefCard) ([]MobileBriefPara, string, []mobileBriefCardWords) {
		rig.builds.Add(1)
		if !rig.writeText.Load() {
			return nil, "unavailable", nil
		}
		return testModelCompose(context.Background(), nil, brief, cards)
	}
	return rig
}

// madeAheadWords is a paragraph long enough to publish (mobileBriefPublishMinWords).
func madeAheadWords() string {
	return strings.TrimSpace(strings.Repeat("The brief names the work you finished and the next step it needs. ", 12))
}

// madeAheadSlot is the occurrence of a slot on the given UTC day.
func madeAheadSlot(t *testing.T, svc *mobileBriefService, day time.Time, slot string) mobileBriefSlotOccurrence {
	t.Helper()
	for _, occurrence := range mobileBriefTodaySlots(day, time.UTC, svc.config.load()) {
		if occurrence.Slot == slot {
			return occurrence
		}
	}
	t.Fatalf("no %s slot on %s", slot, day.Format("2006-01-02"))
	return mobileBriefSlotOccurrence{}
}

// storeMadeAhead saves a made-ahead copy with words, as an early build leaves it.
func storeMadeAhead(t *testing.T, svc *mobileBriefService, occurrence mobileBriefSlotOccurrence, asOf time.Time) MobileBriefV1 {
	t.Helper()
	brief := aheadBriefFor(t, occurrence, asOf)
	brief.Narrative = []MobileBriefPara{{Text: madeAheadWords()}}
	if err := svc.store.save(brief, asOf); err != nil {
		t.Fatal(err)
	}
	return brief
}

// The 21:56 made-ahead copy is replaced by the real 07:00 build (the morning
// slot's window opens 10 minutes before it), and the phone never sees the copy
// once the real one is stored.
func TestBriefMadeAheadCopyIsReplacedByTheRealMorningBuild(t *testing.T) {
	rig := newMadeAheadRig(t, mobileBriefTestDay(21, 56).AddDate(0, 0, -1))
	morning := madeAheadSlot(t, rig.svc, mobileBriefTestDay(0, 0), mobileBriefSlotMorning)
	storeMadeAhead(t, rig.svc, morning, rig.clock.Now())

	rig.clock.Set(mobileBriefTestDay(6, 40))
	rig.svc.checkDue(context.Background())
	if rig.builds.Load() != 0 {
		t.Fatalf("a build ran before the build lead: %d", rig.builds.Load())
	}

	rig.clock.Set(mobileBriefTestDay(6, 50))
	rig.svc.checkDue(context.Background())
	stored, ok := rig.svc.store.load(morning.BriefID)
	if rig.builds.Load() != 1 || !ok {
		t.Fatalf("builds = %d, stored = %v; want the real build to run once", rig.builds.Load(), ok)
	}
	if mobileBriefIsAhead(stored) || !mobileBriefHasWrittenText(stored) {
		t.Fatalf("the stored copy is still the made-ahead one: %+v", stored.Generation)
	}
	if got := stored.AsOf; got != formatMobileBriefTime(mobileBriefTestDay(6, 50)) {
		t.Fatalf("the real build is stamped %s, want its own build time", got)
	}
	if current, found := mobileBriefChooseCurrent([]MobileBriefV1{stored}); !found || mobileBriefIsAhead(current) {
		t.Fatalf("the current brief is provisional once the real one exists: %+v", current.Generation)
	}
}

// A window opened by a made-ahead copy is not its real start: the next build
// reads from the last brief whose words were written, not from 21:56 the night
// before.
func TestBriefMadeAheadCopyDoesNotSetTheNextBuildWindow(t *testing.T) {
	rig := newMadeAheadRig(t, mobileBriefTestDay(21, 56).AddDate(0, 0, -1))
	morning := madeAheadSlot(t, rig.svc, mobileBriefTestDay(0, 0), mobileBriefSlotMorning)
	ahead := storeMadeAhead(t, rig.svc, morning, rig.clock.Now())
	asOf := mobileBriefTestDay(7, 0)
	if start := rig.svc.windowStart(asOf); !start.Equal(asOf.Add(-mobileBriefDefaultWindow)) {
		t.Fatalf("window start %s was set by the made-ahead copy %s", start, ahead.AsOf)
	}
}

// A build with no written words does not block a later build that writes them.
func TestBriefBuildWithNoWordsDoesNotBlockALaterBuildWithWords(t *testing.T) {
	rig := newMadeAheadRig(t, mobileBriefTestDay(6, 50))
	rig.writeText.Store(false)
	rig.svc.checkDue(context.Background())
	if rig.builds.Load() != 1 {
		t.Fatalf("builds = %d after the first due check, want 1", rig.builds.Load())
	}
	if rig.svc.slotFilled("2026-10-09-morning") {
		t.Fatal("a build with no words fills its slot")
	}

	// Inside the retry hold: no new build, the no-words build keeps its place.
	rig.clock.Set(mobileBriefTestDay(7, 10))
	rig.svc.checkDue(context.Background())
	if rig.builds.Load() != 1 {
		t.Fatalf("builds = %d inside the retry hold, want 1", rig.builds.Load())
	}

	rig.writeText.Store(true)
	rig.clock.Set(mobileBriefTestDay(7, 21))
	rig.svc.checkDue(context.Background())
	stored, ok := rig.svc.store.load("2026-10-09-morning")
	if rig.builds.Load() != 2 || !ok || !mobileBriefHasWrittenText(stored) {
		t.Fatalf("builds = %d, has words = %v; want a second build with words", rig.builds.Load(), ok && mobileBriefHasWrittenText(stored))
	}
}

// A real build with no words keeps a made-ahead copy that has words: the copy is
// the best one for the slot, and the scheduler retries on the normal hold.
func TestBriefRealBuildWithNoWordsKeepsMadeAheadCopyWithWords(t *testing.T) {
	rig := newMadeAheadRig(t, mobileBriefTestDay(21, 56).AddDate(0, 0, -1))
	morning := madeAheadSlot(t, rig.svc, mobileBriefTestDay(0, 0), mobileBriefSlotMorning)
	storeMadeAhead(t, rig.svc, morning, rig.clock.Now())

	rig.writeText.Store(false)
	rig.clock.Set(mobileBriefTestDay(6, 50))
	rig.svc.checkDue(context.Background())
	stored, ok := rig.svc.store.load(morning.BriefID)
	if !ok || !mobileBriefIsAhead(stored) || !mobileBriefHasWrittenText(stored) {
		t.Fatalf("the made-ahead copy with words was replaced by a build with none: %+v", stored)
	}
	if rig.svc.slotFilled(morning.BriefID) {
		t.Fatal("a made-ahead copy fills its slot")
	}
	rig.clock.Set(mobileBriefTestDay(7, 5))
	rig.svc.checkDue(context.Background())
	if rig.builds.Load() != 1 {
		t.Fatalf("builds = %d inside the retry hold, want 1", rig.builds.Load())
	}
}

// Over a simulated morning a slot whose builds never write words is built three
// times (the first and two retries, 30 minutes apart) and then stops until the
// next slot, however many ticks remain before the window closes.
func TestBriefNoWordsMorningIsRetriedOnTheHoldNotEveryTick(t *testing.T) {
	rig := newMadeAheadRig(t, mobileBriefTestDay(6, 50))
	rig.writeText.Store(false)
	for at := mobileBriefTestDay(6, 50); at.Before(mobileBriefTestDay(12, 0)); at = at.Add(time.Minute) {
		rig.clock.Set(at)
		rig.svc.checkDue(context.Background())
	}
	// 06:50, then 07:20 and 07:50 on the hold; then the slot is spent.
	const want = 3
	if got := rig.builds.Load(); got != want {
		t.Fatalf("builds over the morning = %d, want %d (first plus two retries, %s apart)", got, want, mobileBriefNoTextRetry)
	}
	if !rig.svc.noTextSpent("2026-10-09-morning") || !rig.svc.slotFilled("2026-10-09-morning") {
		t.Fatal("the slot is not spent after its builds")
	}
}

// A made-ahead copy with words is never shown as the slot's brief once a real
// one with words exists, and a copy with no words never outranks one with words.
func TestCurrentBriefPrefersWordsThenRealOverMadeAhead(t *testing.T) {
	morningAhead := aheadBriefFor(t, mobileBriefSlotOccurrence{BriefID: "2026-10-09-morning", Slot: mobileBriefSlotMorning, ScheduledAt: mobileBriefTestDay(7, 0)}, mobileBriefTestDay(21, 56).AddDate(0, 0, -1))
	morningAhead.Narrative = []MobileBriefPara{{Text: "provisional words"}}
	morningReal := MobileBriefV1{BriefID: "2026-10-09-morning", Slot: mobileBriefSlotMorning, AsOf: formatMobileBriefTime(mobileBriefTestDay(7, 0)),
		NarrativeStatus: "ok", Narrative: []MobileBriefPara{{Text: "real words"}}}
	quietReal := MobileBriefV1{BriefID: "2026-10-09-evening", Slot: mobileBriefSlotEvening, AsOf: formatMobileBriefTime(mobileBriefTestDay(18, 0)),
		NarrativeStatus: "unavailable"}

	if got, _ := mobileBriefChooseCurrent([]MobileBriefV1{morningReal, morningAhead}); got.BriefID != morningReal.BriefID || got.Narrative[0].Text != "real words" {
		t.Fatalf("the real copy with words is not chosen: %+v", got.Narrative)
	}
	if got, _ := mobileBriefChooseCurrent([]MobileBriefV1{quietReal, morningAhead}); got.BriefID != morningAhead.BriefID {
		t.Fatalf("a copy with no words outranks one with words: %s", got.BriefID)
	}
}

func TestBriefSlotFilledOnlyByAFinalCopyWithWords(t *testing.T) {
	rig := newMadeAheadRig(t, mobileBriefTestDay(7, 0))
	morning := madeAheadSlot(t, rig.svc, mobileBriefTestDay(0, 0), mobileBriefSlotMorning)
	if rig.svc.slotFilled(morning.BriefID) {
		t.Fatal("an empty slot is filled")
	}
	storeMadeAhead(t, rig.svc, morning, mobileBriefTestDay(0, 0))
	if rig.svc.slotFilled(morning.BriefID) {
		t.Fatal("a made-ahead copy fills its slot")
	}
	final := MobileBriefV1{BriefID: morning.BriefID, Slot: morning.Slot, AsOf: formatMobileBriefTime(mobileBriefTestDay(7, 0)),
		NarrativeStatus: "ok", Narrative: []MobileBriefPara{{Text: "words"}}}
	if err := rig.svc.store.save(final, mobileBriefTestDay(7, 0)); err != nil {
		t.Fatal(err)
	}
	if !rig.svc.slotFilled(morning.BriefID) {
		t.Fatal("a final copy with words does not fill its slot")
	}
}
