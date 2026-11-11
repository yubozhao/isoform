package main

import (
	"path/filepath"
	"testing"
	"time"

	"wails-gui/internal/handoff"
)

// mobileBriefCurrentWritten is a stored brief whose written words are there.
func mobileBriefCurrentWritten(t *testing.T, localDate, slot string, hour int, words string) MobileBriefV1 {
	t.Helper()
	brief := mobileBriefStoredBrief(t, localDate, slot, hour)
	brief.NarrativeStatus = "ok"
	brief.Narrative = []MobileBriefPara{{Text: words}}
	return brief
}

func TestMobileBriefChooseCurrentWrittenBeatsNewerTemplate(t *testing.T) {
	newerTemplate := mobileBriefStoredBrief(t, "2026-10-09", mobileBriefSlotMorning, 7)
	olderWritten := mobileBriefCurrentWritten(t, "2026-10-08", mobileBriefSlotEvening, 18, "Evening words.")

	got, ok := mobileBriefChooseCurrent([]MobileBriefV1{newerTemplate, olderWritten})
	if !ok || got.BriefID != olderWritten.BriefID {
		t.Fatalf("current = %s (ok=%v), want the written brief %s", got.BriefID, ok, olderWritten.BriefID)
	}

	newerWritten := mobileBriefCurrentWritten(t, "2026-10-09", mobileBriefSlotOnRequest, 12, "Asked for words.")
	got, _ = mobileBriefChooseCurrent([]MobileBriefV1{newerWritten, newerTemplate, olderWritten})
	if got.BriefID != newerWritten.BriefID {
		t.Fatalf("current = %s, want the newer written brief %s", got.BriefID, newerWritten.BriefID)
	}

	olderTemplate := mobileBriefStoredBrief(t, "2026-10-07", mobileBriefSlotEvening, 18)
	got, _ = mobileBriefChooseCurrent([]MobileBriefV1{newerTemplate, olderTemplate})
	if got.BriefID != newerTemplate.BriefID {
		t.Fatalf("with no written brief, current = %s, want the newest %s", got.BriefID, newerTemplate.BriefID)
	}
}

func TestMobileBriefChooseCurrentNeverPicksAQuietSlot(t *testing.T) {
	quiet := mobileBriefStoredBrief(t, "2026-10-09", mobileBriefSlotMorning, 9)
	quiet.NarrativeStatus = "quiet"
	written := mobileBriefCurrentWritten(t, "2026-10-08", mobileBriefSlotEvening, 18, "Evening words.")

	got, ok := mobileBriefChooseCurrent([]MobileBriefV1{quiet, written})
	if !ok || got.BriefID != written.BriefID {
		t.Fatalf("current = %s (ok=%v), want %s past the quiet slot", got.BriefID, ok, written.BriefID)
	}
	if _, ok := mobileBriefChooseCurrent([]MobileBriefV1{quiet}); ok {
		t.Fatal("a quiet slot alone must not be current")
	}
}

// The store keeps the choice in memory: a stored brief, or a removal by the
// retention sweep, changes which brief the next read returns.
func TestMobileBriefStoreCurrentFollowsStoredBriefs(t *testing.T) {
	store := newMobileBriefStore(t.TempDir())
	written := mobileBriefCurrentWritten(t, "2026-10-10", mobileBriefSlotEvening, 18, "Evening words.")
	if err := store.save(written, time.Date(2026, 10, 10, 20, 0, 0, 0, time.Local)); err != nil {
		t.Fatal(err)
	}
	if got, ok := store.current(); !ok || got.BriefID != written.BriefID {
		t.Fatalf("current = %s (ok=%v), want %s", got.BriefID, ok, written.BriefID)
	}

	// A newer template-only brief does not replace the written one. The save
	// is clocked on 12 October, so the retention sweep keeps the written brief.
	template := mobileBriefStoredBrief(t, "2026-10-26", mobileBriefSlotMorning, 7)
	if err := store.save(template, time.Date(2026, 10, 12, 8, 0, 0, 0, time.Local)); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.current(); got.BriefID != written.BriefID {
		t.Fatalf("after a newer template, current = %s, want %s", got.BriefID, written.BriefID)
	}

	// Saving a brief on 27 October at 14:00 sweeps the 10 October brief (the
	// window starts on 13 October), so the written brief leaves and the next
	// read must name the newest remaining brief, not the one cached before.
	fresh := mobileBriefStoredBrief(t, "2026-10-27", mobileBriefSlotOnRequest, 9)
	if err := store.save(fresh, time.Date(2026, 10, 27, 14, 0, 0, 0, time.Local)); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.load(written.BriefID); ok {
		t.Fatal("the retention sweep should have removed the written brief")
	}
	if got, _ := store.current(); got.BriefID != fresh.BriefID {
		t.Fatalf("after the sweep, current = %s, want the newest %s", got.BriefID, fresh.BriefID)
	}
}

// Today, the menu bar, Chat's brief and the launch read name the same brief:
// the current one, which stays current on an earlier day.
func TestMobileBriefSurfacesReadOneCurrentBrief(t *testing.T) {
	clock := &fakeMobileBriefClock{now: mobileBriefTestDay(14, 0)}
	svc, _ := newTestMobileBriefService(t, clock)
	yesterdayEvening := mobileBriefCurrentWritten(t, "2026-10-08", mobileBriefSlotEvening, 18, "Evening words.")
	todayMorning := mobileBriefStoredBrief(t, "2026-10-09", mobileBriefSlotMorning, 7)
	for _, brief := range []MobileBriefV1{yesterdayEvening, todayMorning} {
		if err := svc.store.save(brief, clock.Now()); err != nil {
			t.Fatal(err)
		}
	}
	want := yesterdayEvening.BriefID

	if got, ok := currentMobileBrief(svc); !ok || got.BriefID != want {
		t.Fatalf("phone current = %s (ok=%v), want %s", got.BriefID, ok, want)
	}
	if got, ok := svc.LatestMobileBrief(); !ok || got.BriefID != want {
		t.Fatalf("Mac latest = %s (ok=%v), want %s", got.BriefID, ok, want)
	}
	if got, ok := svc.storedLatestForLaunch(); !ok || got.BriefID != want {
		t.Fatalf("launch read = %s (ok=%v), want %s", got.BriefID, ok, want)
	}

	latest, ok := svc.LatestMobileBrief()
	if !ok {
		t.Fatal("no current brief")
	}
	now := clock.Now()
	row := menuBarBriefRow(menuBarWords("en"), "en", latest, ok, false, MobileBriefHealth{}, now)
	if row.ID != want {
		t.Fatalf("menu bar names %q, want %s", row.ID, want)
	}
	view := todayBriefView(now, time.UTC, latest, ok, MobileBriefHealth{}, false, nil)
	if view.Brief == nil || view.Brief.BriefID != want {
		t.Fatalf("Today shows %+v, want %s", view.Brief, want)
	}
}

func TestTodayBriefRepairsStaleLatestShownAfterEveningSeal(t *testing.T) {
	clock := &fakeMobileBriefClock{now: mobileBriefTestDay(21, 13)}
	svc, _ := newTestMobileBriefService(t, clock)
	svc.app.mobileBriefSvc = svc
	morning := mobileBriefCurrentWritten(t, "2026-10-09", mobileBriefSlotMorning, 7, "Morning fixture.")
	evening := mobileBriefCurrentWritten(t, "2026-10-09", mobileBriefSlotEvening, 17, "Evening fixture.")
	for _, brief := range []MobileBriefV1{morning, evening} {
		for _, ref := range brief.Evidence {
			if _, err := svc.app.handoffStore.Save(handoff.Entry{ID: ref.SourceID, Title: ref.Label, Status: handoff.StatusCompleted}); err != nil {
				t.Fatal(err)
			}
		}
		if err := svc.store.saveDocument(brief, clock.Now()); err != nil {
			t.Fatal(err)
		}
		raw, err := marshalStoredMobileBrief(brief)
		if err != nil {
			t.Fatal(err)
		}
		if err := svc.store.saveFinishedRead(brief.BriefID, mobileBriefFinishedRead{Schema: mobileBriefSealedSchema, Epoch: mobileBriefPrivacyEpoch.Load(), Print: svc.app.briefPrivacyPrint(), Source: mobileBriefSourceDigest(brief), Day: svc.readDay(), Brief: raw}); err != nil {
			t.Fatal(err)
		}
	}
	if err := svc.store.writeLatestShown(mobileBriefLatestShown{Version: 1, BriefID: morning.BriefID, Slot: morning.Slot, At: morning.AsOf}); err != nil {
		t.Fatal(err)
	}
	view, err := svc.app.GetLatestBrief()
	if err != nil || view.Brief == nil || view.Brief.BriefID != evening.BriefID || view.Brief.Slot != mobileBriefSlotEvening {
		t.Fatalf("Today = %+v, error %v; want the evening brief", view.Brief, err)
	}
	if record, ok := svc.store.readLatestShown(); !ok || record.BriefID != evening.BriefID || record.Slot != mobileBriefSlotEvening {
		t.Fatalf("latest shown = %+v, found %v; want evening", record, ok)
	}
}

func TestMenuBarPlaysTheCurrentBriefOnlyWithItsWords(t *testing.T) {
	w := menuBarWords("en")
	now := time.Date(2026, 10, 9, 14, 0, 0, 0, time.Local)
	audio := &MobileBriefAudio{Status: mobileBriefAudioStatusOK, Mime: "audio/wav", Bytes: 100, DurationSeconds: 12}

	yesterday := mobileBriefCurrentWritten(t, "2026-10-08", mobileBriefSlotEvening, 18, "Evening words.")
	yesterday.Audio = audio
	if row := menuBarBriefRow(w, "en", yesterday, true, false, MobileBriefHealth{}, now); row.Kind != "ready" || row.ID != yesterday.BriefID {
		t.Fatalf("written brief with audio: row = %+v, want ready on %s", row, yesterday.BriefID)
	}

	silent := mobileBriefStoredBrief(t, "2026-10-08", mobileBriefSlotEvening, 18)
	silent.Audio = audio
	if row := menuBarBriefRow(w, "en", silent, true, false, MobileBriefHealth{}, now); row.Kind == "ready" {
		t.Fatalf("audio without words must not be playable: row = %+v", row)
	}

	week := mobileBriefCurrentWritten(t, "2026-10-08", mobileBriefSlotEvening, 18, "Week words.")
	week.Audio = audio
	week.YourWeek = &MobileBriefWeek{}
	if row := menuBarBriefRow(w, "en", week, true, false, MobileBriefHealth{}, now); row.Kind != "ready" || row.Title != w["week"] {
		t.Fatalf("week brief: row = %+v, want the week title", row)
	}

	// A brief someone asked for is "Brief", even at a morning hour.
	asked := mobileBriefCurrentWritten(t, "2026-10-09", mobileBriefSlotOnRequest, 8, "Asked for words.")
	asked.Audio = audio
	if row := menuBarBriefRow(w, "en", asked, true, false, MobileBriefHealth{}, now); row.Kind != "ready" || row.Title != w["brief"] {
		t.Fatalf("asked-for brief: row = %+v, want the Brief title", row)
	}

	if row := menuBarBriefRow(w, "en", MobileBriefV1{}, false, false, MobileBriefHealth{}, now); row.Kind != "none" {
		t.Fatalf("no brief: row = %+v, want none", row)
	}
}

func TestMobileBriefCurrentSkipsABriefWhoseWordsAreNowWithheld(t *testing.T) {
	app, svc, withdrawn := newStoredMobileBriefPrivacyFixture(t)
	now := svc.now()
	older := buildMobileBriefV1(mobileBriefInput{
		AsOf: now.Add(-12 * time.Hour), WindowStart: now.Add(-13 * time.Hour), Slot: mobileBriefSlotEvening, Location: time.UTC,
		Learned: []mobileBriefCandidate{{SourceKind: "memory", SourceID: "safe-recall", Title: "Safe recall title", At: now.Add(-12 * time.Hour)}},
	})
	older.NarrativeStatus = "ok"
	older.Narrative = []MobileBriefPara{{Text: "Evening words from a source that stays readable.", Refs: []string{older.Evidence[0].Ref}}}
	if err := svc.store.save(older, now); err != nil {
		t.Fatal(err)
	}
	if got, ok := svc.CurrentMobileBrief(); !ok || got.BriefID != withdrawn.BriefID {
		t.Fatalf("before the withdrawal current = %s (ok=%v), want %s", got.BriefID, ok, withdrawn.BriefID)
	}
	// The app's own path: its privacy hook re-seals the briefs before it returns.
	if err := app.AddSensitiveApp("com.example.test", "TestApp"); err != nil {
		t.Fatal(err)
	}
	got, ok := svc.CurrentMobileBrief()
	if !ok || got.BriefID != older.BriefID || !mobileBriefHasWrittenText(got) {
		t.Fatalf("a brief whose words are withheld stayed current: %s (ok=%v, status %q), want %s", got.BriefID, ok, got.NarrativeStatus, older.BriefID)
	}
}

func TestMobileBriefLatestTransition(t *testing.T) {
	older := mobileBriefCurrentWritten(t, "2026-10-09", mobileBriefSlotMorning, 7, "Older.")
	newer := mobileBriefCurrentWritten(t, "2026-10-09", mobileBriefSlotEvening, 18, "Newer.")
	tie := newer
	tie.AsOf = older.AsOf
	for _, tc := range []struct {
		name      string
		current   MobileBriefV1
		state     mobileBriefLatestState
		candidate MobileBriefV1
		want      mobileBriefLatestMove
	}{
		{"missing", older, mobileBriefLatestMissing, older, mobileBriefLatestAdvance},
		{"newer", older, mobileBriefLatestRecorded, newer, mobileBriefLatestAdvance},
		{"older", newer, mobileBriefLatestRecorded, older, mobileBriefLatestKeep},
		{"same", older, mobileBriefLatestRecorded, older, mobileBriefLatestKeep},
		{"equal time lesser ID", older, mobileBriefLatestRecorded, tie, mobileBriefLatestKeep},
		{"equal time greater ID", tie, mobileBriefLatestRecorded, older, mobileBriefLatestAdvance},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := mobileBriefLatestTransition(tc.state, tc.current, tc.candidate); got != tc.want {
				t.Fatalf("move = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestMobileBriefLatestConcurrentCompletionAndRestart(t *testing.T) {
	clock := &fakeMobileBriefClock{now: mobileBriefTestDay(21, 0)}
	svc, _ := newTestMobileBriefService(t, clock)
	older := mobileBriefCurrentWritten(t, "2026-10-09", mobileBriefSlotMorning, 7, "Older.")
	newer := mobileBriefCurrentWritten(t, "2026-10-09", mobileBriefSlotEvening, 18, "Newer.")
	for _, brief := range []MobileBriefV1{older, newer} {
		if err := svc.store.saveDocument(brief, clock.Now()); err != nil {
			t.Fatal(err)
		}
	}
	// Hold the shared operation lock while an older completion is queued, then
	// install the newer record before allowing that completion to compare.
	svc.noteLatestShownIfNewer(older)
	other := &mobileBriefService{store: newMobileBriefStore(filepath.Dir(svc.store.dir))}
	mobileBriefLatestShownMu.Lock()
	started, done := make(chan struct{}), make(chan struct{})
	go func() {
		close(started)
		other.noteLatestShownIfNewer(older)
		close(done)
	}()
	<-started
	err := svc.store.writeLatestShown(mobileBriefLatestShown{Version: 1, BriefID: newer.BriefID, Slot: newer.Slot, At: newer.AsOf})
	mobileBriefLatestShownMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	<-done
	restarted := newMobileBriefStore(filepath.Dir(svc.store.dir))
	if record, ok := restarted.readLatestShown(); !ok || record.BriefID != newer.BriefID {
		t.Fatalf("after restart latest = %+v, found %v", record, ok)
	}
	// A record whose source no longer has words can be repaired to the last
	// written brief, even though that brief has an earlier timestamp.
	newer.NarrativeStatus, newer.Narrative = "quiet", nil
	if err := svc.store.saveDocument(newer, clock.Now()); err != nil {
		t.Fatal(err)
	}
	other.noteLatestShownIfNewer(older)
	if record, ok := restarted.readLatestShown(); !ok || record.BriefID != older.BriefID {
		t.Fatalf("after quiet replacement latest = %+v, found %v", record, ok)
	}
}

func TestLatestBriefReadsWithheldCandidateOnce(t *testing.T) {
	_, svc, source := newStoredMobileBriefPrivacyFixture(t)
	svc.wg.Wait()
	if _, _, wrote := svc.seal(t.Context(), source); !wrote {
		t.Fatal("initial seal failed")
	}
	writePrivacyMemory(t, "safe-recall", "# Changed memory\n")
	reads := 0
	mobileBriefAfterSealedRead = func(id string) {
		if id == source.BriefID {
			reads++
		}
	}
	t.Cleanup(func() { mobileBriefAfterSealedRead = nil })
	got, ok, err := svc.latestShown()
	if err != nil || !ok || got.BriefID != source.BriefID || len(got.Narrative) != 0 {
		t.Fatalf("latest withheld brief = %s, ok=%v, err=%v", got.BriefID, ok, err)
	}
	if reads != 1 {
		t.Fatalf("candidate was read %d times, want once", reads)
	}
}
