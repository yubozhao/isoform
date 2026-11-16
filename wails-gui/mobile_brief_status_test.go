package main

import (
	"context"
	"sync"
	"testing"
	"time"
)

// A scheduled build says it is being made, not only a build the phone asked
// for (phone Today rule 7, PROTOCOL.md 8.2).
func TestMobileBriefStatusSaysAScheduledBuildIsBeingMade(t *testing.T) {
	clock := &fakeMobileBriefClock{now: mobileBriefTestDay(7, 1)}
	svc, _ := newTestMobileBriefService(t, clock)
	gathering, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	gather := svc.gather
	svc.gather = func(ctx context.Context, slot string, windowStart, asOf time.Time) (mobileBriefInput, error) {
		once.Do(func() { close(gathering) })
		<-release
		return gather(ctx, slot, windowStart, asOf)
	}
	done := make(chan struct{})
	go func() { svc.checkDue(context.Background()); close(done) }()
	select {
	case <-gathering:
	case <-time.After(5 * time.Second):
		t.Fatal("the morning build never started")
	}
	making := svc.makingStatus()
	close(release)
	<-done
	svc.wg.Wait()
	if making == nil || making.Kind != mobileBriefSlotMorning || making.StartedAt == "" {
		t.Fatalf("a scheduled build was not reported as being made: %+v", making)
	}
	started, err := time.Parse(time.RFC3339, making.StartedAt)
	if err != nil {
		t.Fatal(err)
	}
	if ready, err := time.Parse(time.RFC3339, making.ReadyAt); err != nil || !ready.Equal(started.Add(mobileBriefMakingEstimate)) {
		t.Fatalf("readyAt %q is not the build start plus the estimate", making.ReadyAt)
	}
	if after := svc.makingStatus(); after != nil {
		t.Fatalf("a finished build still says it is being made: %+v", after)
	}
}

// A brief made ahead of its slot reaches the phone only at the slot, so the
// phone is never told it is being made.
func TestMobileBriefStatusLeavesOutABriefMadeAhead(t *testing.T) {
	svc, _ := newTestMobileBriefService(t, &fakeMobileBriefClock{now: mobileBriefTestDay(6, 0)})
	svc.mu.Lock()
	svc.running, svc.runningSince, svc.runningSlot = true, svc.now(), ""
	svc.mu.Unlock()
	if making := svc.makingStatus(); making != nil {
		t.Fatalf("a build without a running slot was reported: %+v", making)
	}
}

// The status names the next scheduled brief, the time the phone's "Your first
// brief comes at" and "comes when your computer wakes" lines use.
func TestMobileBriefStatusNamesTheNextScheduledBrief(t *testing.T) {
	bridge, _ := mobileTestHub(t)
	svc := &statusTestBriefSource{schedule: MobileBriefSchedule{Enabled: true, Morning: "07:00", Evening: "18:00"}}
	bridge.app.mobileBriefSvc = svc
	now := time.Now()
	status := bridge.briefStatus(now)
	if status == nil || status.Next == nil {
		t.Fatalf("no next brief: %+v", status)
	}
	want := mobileBriefSlotTimes(now, time.Local, svc.schedule)[0]
	if status.Next.DueAt != want.At || status.Next.Slot != want.Slot || status.Current != nil || status.Making != nil {
		t.Fatalf("next = %+v, want %+v", status.Next, want)
	}
	svc.schedule.Enabled = false
	if status := bridge.briefStatus(now); status == nil || status.Next != nil {
		t.Fatalf("a schedule that is off still names a next brief: %+v", status)
	}
}

type statusTestBriefSource struct {
	mobileTestBriefSource
	schedule MobileBriefSchedule
}

func (s *statusTestBriefSource) Schedule() (MobileBriefSchedule, error) { return s.schedule, nil }

func TestMobileBriefPackageHasAudio(t *testing.T) {
	for raw, want := range map[string]bool{
		`{"briefId":"b"}`:                     false,
		`{"audio":{"status":"rendering"}}`:    false,
		`{"audio":{"attachment":null}}`:       false,
		`{"audio":{"attachment":{"id":"a"}}}`: true,
	} {
		if got := mobileBriefPackageHasAudio([]byte(raw)); got != want {
			t.Errorf("%s: got %v, want %v", raw, got, want)
		}
	}
}

// The first, text-only package of a written brief reaches the phone silently;
// the push goes with the package that has the recording, once.
func TestMobileBriefPushWaitsForTheRecording(t *testing.T) {
	svc, _ := newTestMobileBriefService(t, &fakeMobileBriefClock{now: mobileBriefTestDay(7, 1)})
	text := MobileBriefV1{BriefID: "2026-10-09-morning", AsOf: "2026-10-09T07:01:00Z", NarrativeStatus: "ok"}
	if svc.textPhoneNotify(text, true) {
		t.Fatal("the text-only package carried the push")
	}
	spoken := text
	spoken.Audio = &MobileBriefAudio{Status: mobileBriefAudioStatusOK, Attachment: &MobileBriefAudioAttachment{}}
	if !svc.takePhoneNotify(spoken) {
		t.Fatal("the package with the recording did not carry the owed push")
	}
	if svc.takePhoneNotify(spoken) {
		t.Fatal("the push was owed twice")
	}
	// A brief that already plays, one without words, or one with no push asked
	// keeps its flag as it was.
	if !svc.textPhoneNotify(spoken, true) || svc.textPhoneNotify(text, false) {
		t.Fatal("a package with its recording, or one without a push, changed its flag")
	}
	quiet := text
	quiet.NarrativeStatus = "unavailable"
	if !svc.textPhoneNotify(quiet, true) {
		t.Fatal("a brief that will not be recorded waited for a recording")
	}
}

// A newer brief's push replaces an older one's: the phone is never pushed an
// older brief after a newer one.
func TestMobileBriefPushOwedOnlyForTheNewestBrief(t *testing.T) {
	svc, _ := newTestMobileBriefService(t, &fakeMobileBriefClock{now: mobileBriefTestDay(18, 1)})
	morning := MobileBriefV1{BriefID: "2026-10-09-morning", AsOf: "2026-10-09T07:01:00Z", NarrativeStatus: "ok"}
	evening := MobileBriefV1{BriefID: "2026-10-09-evening", AsOf: "2026-10-09T18:01:00Z", NarrativeStatus: "ok"}
	svc.textPhoneNotify(morning, true)
	svc.textPhoneNotify(evening, true)
	if svc.takePhoneNotify(morning) {
		t.Fatal("the older brief kept its push")
	}
	if !svc.takePhoneNotify(evening) {
		t.Fatal("the newest brief lost its push")
	}
	// A rebuild of the same id is another version: it is not pushed for the old one.
	svc.textPhoneNotify(evening, true)
	rebuilt := evening
	rebuilt.AsOf = "2026-10-09T18:05:00Z"
	if svc.takePhoneNotify(rebuilt) {
		t.Fatal("a rebuilt brief took the push owed to the version before it")
	}
}

// When the recording never comes, the push still goes, with the words, after
// mobileBriefNotifyHoldMax.
func TestMobileBriefPushGoesWithoutTheRecordingAfterTheCap(t *testing.T) {
	previous := mobileBriefNotifyHoldMax
	mobileBriefNotifyHoldMax = 20 * time.Millisecond
	t.Cleanup(func() { mobileBriefNotifyHoldMax = previous })
	rig := newHandoffTestRig(t)
	svc, _ := newTestMobileBriefService(t, &fakeMobileBriefClock{now: mobileBriefTestDay(7, 1)})
	svc.app = rig.app
	brief := buildMobileBriefV1(mobileBriefInput{Slot: mobileBriefSlotMorning, AsOf: svc.now(), Location: time.UTC})
	brief.NarrativeStatus = "ok"
	brief.Narrative = []MobileBriefPara{{Text: "The plan shipped."}}
	if err := svc.store.saveDocument(brief, svc.now()); err != nil {
		t.Fatal(err)
	}
	pushed := make(chan MobileBriefV1, 4)
	svc.publish = func(current MobileBriefV1, notify bool) error {
		if notify {
			pushed <- current
		}
		return nil
	}
	if svc.textPhoneNotify(brief, true) {
		t.Fatal("the text-only package carried the push")
	}
	select {
	case got := <-pushed:
		if got.BriefID != brief.BriefID {
			t.Fatalf("pushed %s, want %s", got.BriefID, brief.BriefID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the push never went without the recording")
	}
	// The recording step ending later sends no second push.
	svc.payPhoneNotify(brief)
	select {
	case <-pushed:
		t.Fatal("the brief was pushed twice")
	case <-time.After(100 * time.Millisecond):
	}
}
