package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// Guards for the spoken brief going missing (2026-10-19 to 2026-10-21): every
// brief wrote its recording, but a Today read while the voice was speaking
// renumbered the stored brief's evidence, the finished recording no longer
// matched and was never attached, so Today and the phone had nothing to play.

// audioKeepRig builds a brief end to end through the service with a voice
// that waits until the test lets it finish.
func audioKeepRig(t *testing.T) (*handoffTestRig, *mobileBriefService, *fakeMobileBriefClock, MobileBriefV1, chan struct{}) {
	t.Helper()
	rig := newHandoffTestRig(t)
	clock := &fakeMobileBriefClock{now: mobileBriefTestDay(7, 0)}
	svc, _ := newTestMobileBriefService(t, clock)
	svc.app, rig.app.mobileBriefSvc = rig.app, svc
	seedMobileBriefReadableHistory(t, rig.app, "h-morning")
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	speaker := fakeSpeaker("en", testWAV(2), nil)
	speaker.synthesize = func(context.Context, string, string) ([]byte, error) {
		once.Do(func() { close(started) })
		<-release
		return testWAV(2), nil
	}
	svc.speaker = speaker
	// A recording is made from model text only (design R74), and only when the model text
	// reaches the 100-word publication floor, and fits the 600-byte paragraph bound speech reads;
	// the reply cites its evidence, as a model brief does.
	svc.compose = func(_ context.Context, _ *App, brief MobileBriefV1, _ []MobileBriefCard) ([]MobileBriefPara, string, []mobileBriefCardWords) {
		var refs []string
		if len(brief.Evidence) > 0 {
			refs = []string{brief.Evidence[0].Ref}
		}
		return []MobileBriefPara{{Text: "All clear, one quick check left today. All clear, one quick check left today. All clear, one quick check left today. All clear, one quick check left today. All clear, one quick check left today. All clear, one quick check left today. All clear, one quick check left today. All clear, one quick check left today. All clear, one quick check left today. All clear, one quick check left today. All clear, one quick check left today. All clear, one quick check left today. All clear, one quick check left today. All clear, one quick check left today. All clear, one quick check left today.", Refs: refs}}, "ok", nil
	}
	brief, err := svc.generate(context.Background(), mobileBriefSlotMorning, mobileBriefReasonSchedule)
	if err != nil {
		t.Fatal(err)
	}
	<-started
	return rig, svc, clock, brief, release
}

func assertBriefPlays(t *testing.T, rig *handoffTestRig, svc *mobileBriefService, briefID, when string) {
	t.Helper()
	if stored, ok := svc.store.load(briefID); !ok || stored.Audio == nil || stored.Audio.Status != mobileBriefAudioStatusOK {
		t.Fatalf("%s: the stored brief has no recording: %+v", when, stored.Audio)
	}
	view, err := rig.app.GetLatestBrief()
	if err != nil || view.Brief == nil || view.Brief.Audio == nil {
		t.Fatalf("%s: Today has nothing to play: %+v, %v", when, view.Brief, err)
	}
	if health := rig.app.GetMobileBriefHealth(); health.LatestBriefID != briefID || health.Audio == nil {
		t.Fatalf("%s: Today's player is not offered: %+v", when, health)
	}
	if phone, ok := svc.GetMobileBrief(briefID); !ok || phone.Audio == nil {
		t.Fatalf("%s: the phone's brief has nothing to play: %+v", when, phone.Audio)
	}
	if data, err := rig.app.GetMobileBriefAudio(briefID); err != nil || data.Base64 == "" {
		t.Fatalf("%s: the recording does not play: %+v, %v", when, data, err)
	}
}

func TestMobileBriefAudioSurvivesTodayReadDuringSynthesis(t *testing.T) {
	rig, svc, clock, brief, release := audioKeepRig(t)
	// Today reads the brief the moment it is stored, with new live rows.
	clock.Set(mobileBriefTestDay(7, 1))
	addTodayRefreshSources(t, rig, clock.Now())
	if view, err := latestBriefAfterRefresh(t, rig.app); err != nil || view.Brief == nil {
		t.Fatalf("Today read = %+v, %v", view, err)
	}
	if stored, _ := svc.store.load(brief.BriefID); len(stored.Evidence) == len(brief.Evidence) {
		t.Fatal("the fixture must renumber the evidence while the voice speaks")
	}
	close(release)
	svc.wg.Wait()
	assertBriefPlays(t, rig, svc, brief.BriefID, "after the voice finished")
}

// The later rewrites (Today refreshes with changing live rows, the phone's
// reads) keep the recording, on disk and on every read.
func TestMobileBriefAudioSurvivesLaterRewrites(t *testing.T) {
	rig, svc, clock, brief, release := audioKeepRig(t)
	close(release)
	svc.wg.Wait()
	assertBriefPlays(t, rig, svc, brief.BriefID, "once stored")
	clock.Set(mobileBriefTestDay(9, 0))
	addTodayRefreshSources(t, rig, clock.Now())
	assertBriefPlays(t, rig, svc, brief.BriefID, "after a Today refresh with new rows")
	clock.Set(mobileBriefTestDay(11, 0))
	dir, _ := memoryDir()
	if err := os.Remove(filepath.Join(dir, "live-note.md")); err != nil {
		t.Fatal(err)
	}
	checkMemoryFolderNow(rig.app)
	assertBriefPlays(t, rig, svc, brief.BriefID, "after a live row left")
	if latest, ok := svc.LatestMobileBrief(); !ok || latest.Audio == nil {
		t.Fatalf("the phone's latest brief lost its recording: %+v", latest.Audio)
	}
	assertBriefPlays(t, rig, svc, brief.BriefID, "after the phone's read")
}

// Today opens the saved brief without waiting for its live refresh: a live
// row that left before that refresh has stored the brief again keeps the
// recording on every read (Today, its player, the phone and the audio).
func TestMobileBriefAudioPlaysBeforeTodayRefreshStoresTheBrief(t *testing.T) {
	rig, svc, clock, brief, release := audioKeepRig(t)
	close(release)
	svc.wg.Wait()
	clock.Set(mobileBriefTestDay(9, 0))
	addTodayRefreshSources(t, rig, clock.Now())
	if _, err := latestBriefAfterRefresh(t, rig.app); err != nil {
		t.Fatal(err)
	}
	assertBriefPlays(t, rig, svc, brief.BriefID, "after a Today refresh with new rows")
	svc.todayRefreshWG.Wait()
	clock.Set(mobileBriefTestDay(11, 0))
	dir, _ := memoryDir()
	if err := os.Remove(filepath.Join(dir, "live-note.md")); err != nil {
		t.Fatal(err)
	}
	checkMemoryFolderNow(rig.app)
	func() {
		svc.todayMu.Lock()
		defer svc.todayMu.Unlock()
		assertBriefPlays(t, rig, svc, brief.BriefID, "after a live row left, before the refresh")
	}()
	svc.todayRefreshWG.Wait()
	assertBriefPlays(t, rig, svc, brief.BriefID, "after the refresh")
}

// The live briefs that lost their recording had written text (narrative
// "ok"): the brief keeps both its text and its recording across Today
// refreshes with live rows coming and going.
func TestMobileBriefAudioSurvivesLaterRewritesWithWrittenText(t *testing.T) {
	rig, svc, clock, brief, release := audioKeepRig(t)
	stored, _ := svc.store.load(brief.BriefID)
	if len(stored.Evidence) == 0 {
		t.Fatal("the fixture brief has no evidence")
	}
	stored.NarrativeStatus = "ok"
	stored.Narrative = []MobileBriefPara{{Text: "You shipped the plan yesterday. What do you want to start with?", Refs: []string{stored.Evidence[0].Ref}}}
	if err := svc.store.save(stored, svc.now()); err != nil {
		t.Fatal(err)
	}
	close(release)
	svc.wg.Wait()
	assertBriefPlays(t, rig, svc, brief.BriefID, "written text, once stored")
	clock.Set(mobileBriefTestDay(9, 0))
	addTodayRefreshSources(t, rig, clock.Now())
	assertBriefPlays(t, rig, svc, brief.BriefID, "written text, after a Today refresh with new rows")
	clock.Set(mobileBriefTestDay(11, 0))
	dir, _ := memoryDir()
	if err := os.Remove(filepath.Join(dir, "live-note.md")); err != nil {
		t.Fatal(err)
	}
	assertBriefPlays(t, rig, svc, brief.BriefID, "written text, after a live row left")
	if kept, _ := svc.store.load(brief.BriefID); kept.NarrativeStatus != "ok" || len(kept.Narrative) != 1 {
		t.Fatalf("the written text was not kept: %s %+v", kept.NarrativeStatus, kept.Narrative)
	}
}

// A recording plays only while the brief still says what it speaks. Once its
// text has changed for good, the next stored rewrite lets it go.
func TestMobileBriefAudioNeverSpeaksTextTheBriefNoLongerShows(t *testing.T) {
	rig, svc, _, brief, release := audioKeepRig(t)
	close(release)
	svc.wg.Wait()
	stored, ok := svc.store.load(brief.BriefID)
	if !ok || stored.Audio == nil || stored.Audio.binding == "" || len(stored.Narrative) == 0 {
		t.Fatalf("the recording was stored without its text binding: %+v", stored.Audio)
	}
	stored.Narrative[0].Text += " And one more thing."
	if err := svc.store.save(stored, svc.now()); err != nil {
		t.Fatal(err)
	}
	if phone, _ := svc.GetMobileBrief(brief.BriefID); phone.Audio != nil {
		t.Fatal("the phone plays a recording of other words")
	}
	if health := rig.app.GetMobileBriefHealth(); health.Audio != nil {
		t.Fatalf("Today's player is offered for other words: %+v", health.Audio)
	}
	if _, err := rig.app.GetMobileBriefAudio(brief.BriefID); err == nil {
		t.Fatal("the recording of other words was served")
	}
	if view, err := latestBriefAfterRefresh(t, rig.app); err != nil || view.Brief == nil || view.Brief.Audio != nil {
		t.Fatalf("Today plays a recording of other words: %+v, %v", view.Brief, err)
	}
	if kept, _ := svc.store.load(brief.BriefID); kept.Audio != nil {
		t.Fatal("Today's rewrite kept a recording of words the brief no longer says")
	}
}

// A goal the user closes hides the recording that speaks of it on every read,
// never in the stored brief; Undo brings the goal and its recording back.
func TestMobileBriefAudioReturnsWhenAClosedGoalIsReopened(t *testing.T) {
	rig, svc, clock, brief, release := audioKeepRig(t)
	store := NewPurposeMapStore(rig.activity.db)
	goal := mobileTestPurpose(t, store, "beta", PurposeKindGoal, "Launch the beta", false, nil)
	// While the voice speaks, the brief comes to speak of the goal: the
	// voice reads it again as it reads now.
	stored, _ := svc.store.load(brief.BriefID)
	stored.Evidence = append(stored.Evidence, MobileBriefRef{Ref: "goal-ref", SourceKind: "purpose", SourceID: goal.OpenLoop.ID, Label: "Launch the beta"})
	stored.Narrative = append(stored.Narrative, MobileBriefPara{Text: "The beta launch has not moved this week.", Refs: []string{"goal-ref"}})
	if err := svc.store.save(stored, svc.now()); err != nil {
		t.Fatal(err)
	}
	close(release)
	svc.wg.Wait()
	assertBriefPlays(t, rig, svc, brief.BriefID, "with the goal open")
	done, err := rig.app.MoveGoal(GoalMoveInput{GoalID: goal.OpenLoop.ID, Action: userPurposeResolve, Key: "done-beta"})
	if err != nil {
		t.Fatal(err)
	}
	clock.Set(mobileBriefTestDay(8, 0))
	if view, err := rig.app.GetLatestBrief(); err != nil || view.Brief == nil || view.Brief.Audio != nil {
		t.Fatalf("Today plays a recording that speaks of a closed goal: %+v, %v", view.Brief, err)
	}
	if phone, _ := svc.GetMobileBrief(brief.BriefID); phone.Audio != nil {
		t.Fatal("the phone plays a recording that speaks of a closed goal")
	}
	if kept, _ := svc.store.load(brief.BriefID); kept.Audio == nil {
		t.Fatal("closing a goal deleted the stored recording")
	}
	if _, err := rig.app.MoveGoal(GoalMoveInput{GoalID: goal.OpenLoop.ID, ExpectedEventID: done.EventID, Action: userPurposeRestore, Key: "undo-beta"}); err != nil {
		t.Fatal(err)
	}
	assertBriefPlays(t, rig, svc, brief.BriefID, "after Undo")
}

// The text changed while the voice was speaking: the voice reads the brief
// once more as it reads now, and that recording is the one attached.
func TestMobileBriefAudioIsReadAgainWhenTheTextChangesWhileSpeaking(t *testing.T) {
	rig := newHandoffTestRig(t)
	clock := &fakeMobileBriefClock{now: mobileBriefTestDay(7, 0)}
	svc, _ := newTestMobileBriefService(t, clock)
	svc.app, rig.app.mobileBriefSvc = rig.app, svc
	seedMobileBriefReadableHistory(t, rig.app, "h-morning")
	started, release := make(chan struct{}), make(chan struct{})
	var spoken []string
	speaker := fakeSpeaker("en", testWAV(2), nil)
	speaker.synthesize = func(_ context.Context, _ string, text string) ([]byte, error) {
		spoken = append(spoken, text)
		if len(spoken) == 1 {
			close(started)
			<-release
		}
		return testWAV(2), nil
	}
	svc.speaker = speaker
	// A recording is made from model text only (design R74), and only when the model text
	// reaches the 100-word publication floor, and fits the 600-byte paragraph bound speech reads;
	// the reply cites its evidence, as a model brief does.
	svc.compose = func(_ context.Context, _ *App, brief MobileBriefV1, _ []MobileBriefCard) ([]MobileBriefPara, string, []mobileBriefCardWords) {
		var refs []string
		if len(brief.Evidence) > 0 {
			refs = []string{brief.Evidence[0].Ref}
		}
		return []MobileBriefPara{{Text: "All clear, one quick check left today. All clear, one quick check left today. All clear, one quick check left today. All clear, one quick check left today. All clear, one quick check left today. All clear, one quick check left today. All clear, one quick check left today. All clear, one quick check left today. All clear, one quick check left today. All clear, one quick check left today. All clear, one quick check left today. All clear, one quick check left today. All clear, one quick check left today. All clear, one quick check left today. All clear, one quick check left today.", Refs: refs}}, "ok", nil
	}
	brief, err := svc.generate(context.Background(), mobileBriefSlotMorning, mobileBriefReasonSchedule)
	if err != nil {
		t.Fatal(err)
	}
	<-started
	stored, _ := svc.store.load(brief.BriefID)
	stored.Narrative[0].Text = "Changed plan, " + stored.Narrative[0].Text
	if err := svc.store.save(stored, svc.now()); err != nil {
		t.Fatal(err)
	}
	close(release)
	svc.wg.Wait()
	// The voice reads the brief again as it reads now: its last reading starts with the changed text.
	if !strings.Contains(strings.Join(spoken, " "), "Changed plan,") {
		t.Fatalf("the voice did not read the brief again as it reads now: %q", spoken)
	}
	assertBriefPlays(t, rig, svc, brief.BriefID, "after the second reading")
}

// When the voice reads Isoform's template from the brief's rows (no written
// text, or text too long to read aloud), the recording is bound to those
// rows too; written text that is read as written is bound to the text alone.
func TestMobileBriefAudioBindingFollowsWhatTheVoiceReads(t *testing.T) {
	_, svc, _, brief, release := audioKeepRig(t)
	close(release)
	svc.wg.Wait()
	base := svc.app.mobileBriefForDelivery(brief)
	if len(base.Sections.Moved) == 0 {
		t.Fatalf("the fixture has no row the template reads: %+v", base.Sections)
	}
	retitled := func(b MobileBriefV1) MobileBriefV1 {
		b.Sections.Moved = append([]MobileBriefItem{}, b.Sections.Moved...)
		b.Sections.Moved[0].Title = "Shipped the launch plan"
		return b
	}
	long := MobileBriefPara{Text: strings.TrimSpace(strings.Repeat("word ", mobileBriefMaxWords+20)), Refs: base.Narrative[0].Refs}
	// A template is never spoken (design R74): only written text is read, so only its rows bind the audio.
	for _, tc := range []struct {
		name      string
		narrative []MobileBriefPara
		spoken    bool
	}{
		{"written text", base.Narrative, true},
		{"no written text", nil, false},
		{"long written text", []MobileBriefPara{long}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := base
			b.Narrative = tc.narrative
			before, _ := mobileBriefSpeech(b, "en")
			if (before != "") != tc.spoken {
				t.Fatalf("spoken=%v, want %v: %q", before != "", tc.spoken, before)
			}
			changed := mobileBriefAudioBinding(b, "en") != mobileBriefAudioBinding(retitled(b), "en")
			// Written text does not read the rows, and a template is not read, so no row changes the binding.
			if changed {
				t.Fatalf("a row changed the binding although the voice does not read rows")
			}
		})
	}
}

// A recording without a binding cannot prove which text it speaks.
func TestMobileBriefAudioWithoutABindingIsWithheld(t *testing.T) {
	rig, svc, _, brief, release := audioKeepRig(t)
	close(release)
	svc.wg.Wait()
	stored, _ := svc.store.load(brief.BriefID)
	stored.Audio.binding = ""
	if err := svc.store.save(stored, svc.now()); err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(filepath.Join(svc.store.dir, brief.BriefID+".json")); strings.Contains(string(raw), "audioBinding") {
		t.Fatal("the fixture still has a binding")
	}
	if _, err := rig.app.GetMobileBriefAudio(brief.BriefID); err == nil {
		t.Fatal("an unbound recording was playable")
	}
}

// The binding stays on this computer: the stored file carries it, the
// document a phone or Today reads never does.
func TestMobileBriefAudioBindingStaysLocal(t *testing.T) {
	_, svc, _, brief, release := audioKeepRig(t)
	close(release)
	svc.wg.Wait()
	raw, err := os.ReadFile(filepath.Join(svc.store.dir, brief.BriefID+".json"))
	if err != nil || !strings.Contains(string(raw), `"audioBinding"`) {
		t.Fatalf("the stored brief has no audio binding: %v", err)
	}
	phone, ok := svc.GetMobileBrief(brief.BriefID)
	wire, _ := json.Marshal(phone)
	if !ok || phone.Audio == nil || strings.Contains(string(wire), "audioBinding") || strings.Contains(string(wire), phone.Audio.binding) {
		t.Fatalf("the binding left this computer: %s", wire)
	}
}

// Leaving out a goal the user closed silences the recording only when the
// written text cited that goal; a normal brief keeps it, and the stored copy
// is never touched.
func TestClosedGoalSilencesOnlyTheBriefThatSpeaksOfIt(t *testing.T) {
	brief := goalTestBrief("goal-1")
	brief.Audio = &MobileBriefAudio{Status: mobileBriefAudioStatusOK}
	if got := mobileBriefDropClosedPurposes(brief, map[string]bool{"another-goal": true}); got.Audio == nil {
		t.Fatal("a goal the brief never mentions silenced its recording")
	}
	if got := mobileBriefDropClosedPurposes(brief, map[string]bool{}); got.Audio == nil {
		t.Fatal("a brief with no closed goal lost its recording")
	}
	if got := mobileBriefDropClosedPurposes(brief, map[string]bool{"goal-1": true}); got.Audio != nil {
		t.Fatal("the recording still speaks of a closed goal")
	}
	if brief.Audio == nil {
		t.Fatal("the read changed the stored brief")
	}
}
