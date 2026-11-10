package main

import (
	"bytes"
	"log"
	"os"
	"strings"
	"testing"
)

func TestBriefReadLogsOneContentFreeOutcome(t *testing.T) {
	_, svc, brief := newStoredMobileBriefPrivacyFixture(t)
	svc.todayRefreshWG.Wait()
	svc.wg.Wait()
	var output bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&output)
	t.Cleanup(func() { log.SetOutput(previous) })
	read, withheld, err := svc.finishedReadWithholding(brief)
	if err != nil || withheld.Reason != "" || !mobileBriefHasWrittenText(read) {
		t.Fatalf("fixture was not shown: reason=%s error=%v", withheld.Reason, err)
	}
	line := output.String()
	if strings.Count(line, "[mobile-brief] read brief=") != 1 || !strings.Contains(line, "written=shown") || strings.Contains(line, read.Narrative[0].Text) {
		t.Fatalf("unexpected read outcome log: %q", line)
	}
}

func TestBriefReadLogsVisibleTemplateText(t *testing.T) {
	_, svc, brief := newStoredMobileBriefPrivacyFixture(t)
	svc.todayRefreshWG.Wait()
	svc.wg.Wait()
	brief.NarrativeStatus = "unavailable"
	brief.Audio = nil
	if err := svc.store.save(brief, svc.now()); err != nil {
		t.Fatal(err)
	}
	if _, _, wrote := svc.seal(t.Context(), brief); !wrote {
		t.Fatal("fixture did not seal")
	}
	var output bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&output)
	t.Cleanup(func() { log.SetOutput(previous) })
	read, _, err := svc.finishedReadWithholding(brief)
	if err != nil || len(read.Narrative) == 0 {
		t.Fatalf("template text was not shown: error=%v", err)
	}
	line := output.String()
	if strings.Count(line, "[mobile-brief] read brief=") != 1 || !strings.Contains(line, "written=shown") || strings.Contains(line, read.Narrative[0].Text) {
		t.Fatalf("unexpected template read outcome log: %q", line)
	}
}

// Reading a sealed brief is a plain read of its saved copy: no gate runs, so a
// store that is still opening cannot hide the brief (docs/brief-rebuild.md).
func TestSealedBriefReadRunsNoGate(t *testing.T) {
	_, svc, brief := cardRig(t)
	svc.todayRefreshWG.Wait()
	svc.wg.Wait()
	// The first read seals the brief with the gates (its cards are built then).
	sealed, _, err := svc.finishedLatest()
	if err != nil || sealed.BriefID != brief.BriefID {
		t.Fatalf("latest = %s (err=%v), want %s", sealed.BriefID, err, brief.BriefID)
	}
	before := mobileBriefGateRuns.Load()
	read, ok, err := svc.finishedLatest()
	if err != nil || !ok || read.BriefID != brief.BriefID {
		t.Fatalf("latest = %s (ok=%v, err=%v), want %s", read.BriefID, ok, err, brief.BriefID)
	}
	if runs := mobileBriefGateRuns.Load() - before; runs != 0 {
		t.Fatalf("a plain read of a sealed brief ran the gates %d times", runs)
	}
	if len(read.Cards) != len(sealed.Cards) {
		t.Fatalf("the sealed read has %d cards, the sealing had %d", len(read.Cards), len(sealed.Cards))
	}
}

// A store that is still opening at read time does not hide a sealed brief.
func TestSealedBriefShowsWhileTheStoresAreStillOpening(t *testing.T) {
	_, svc, brief := cardRig(t)
	svc.todayRefreshWG.Wait()
	svc.wg.Wait()
	sealed, _, err := svc.finishedLatest()
	if err != nil || sealed.BriefID != brief.BriefID {
		t.Fatalf("sealing failed: id=%s err=%v", sealed.BriefID, err)
	}
	mobileBriefGateHold = func() bool { return true }
	t.Cleanup(func() { mobileBriefGateHold = nil })

	before := mobileBriefGateRuns.Load()
	read, ok, err := svc.finishedLatest()
	if err != nil || !ok {
		t.Fatalf("latest read failed: ok=%v err=%v", ok, err)
	}
	if read.BriefID != brief.BriefID || len(read.Cards) != len(sealed.Cards) || len(read.Cards) == 0 {
		t.Fatalf("a store still opening hid the sealed brief: id=%s cards=%d want %d", read.BriefID, len(read.Cards), len(sealed.Cards))
	}
	if runs := mobileBriefGateRuns.Load() - before; runs != 0 {
		t.Fatalf("a plain read ran the gates %d times while the stores were opening", runs)
	}
}

// A privacy change seals the affected brief again on its next read, and the
// copy is then read plainly again.
func TestPrivacyChangeAmendsSealedBriefOnNextRead(t *testing.T) {
	_, svc, brief := cardRig(t)
	svc.todayRefreshWG.Wait()
	svc.wg.Wait()
	sealsBefore := mobileBriefFinishedReadKeys.Load()
	briefPrivacyChanged()
	if _, _, err := svc.finishedLatest(); err != nil {
		t.Fatal(err)
	}
	if mobileBriefFinishedReadKeys.Load() == sealsBefore {
		t.Fatal("a privacy change did not seal the brief again")
	}
	sealsAfter := mobileBriefFinishedReadKeys.Load()
	if read, ok, err := svc.finishedLatest(); err != nil || !ok || read.BriefID != brief.BriefID {
		t.Fatalf("amended read = %s (ok=%v err=%v)", read.BriefID, ok, err)
	}
	if mobileBriefFinishedReadKeys.Load() != sealsAfter {
		t.Fatal("a read with no change after the amend sealed the brief again")
	}
}

// A freshness change (cron, activity summaries) does not seal a brief again:
// it is carried by the next brief made or refreshed.
func TestFreshnessChangeDoesNotSealSealedBrief(t *testing.T) {
	_, svc, _ := cardRig(t)
	svc.todayRefreshWG.Wait()
	svc.wg.Wait()
	before := mobileBriefFinishedReadKeys.Load()
	noteMobileBriefReadCachesChanged()
	if _, _, err := svc.finishedLatest(); err != nil {
		t.Fatal(err)
	}
	if mobileBriefFinishedReadKeys.Load() != before {
		t.Fatal("a freshness change sealed a brief again")
	}
}

// An old-format saved read is sealed once, on first read, with the current gates.
func TestOldFormatBriefIsSealedOnceOnFirstRead(t *testing.T) {
	_, svc, brief := cardRig(t)
	svc.todayRefreshWG.Wait()
	svc.wg.Wait()
	raw, err := marshalStoredMobileBrief(brief)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.store.saveFinishedRead(brief.BriefID, mobileBriefFinishedRead{Schema: 1, Key: "retired", Brief: raw}); err != nil {
		t.Fatal(err)
	}
	if _, found := readSealedBrief(svc.store, brief.BriefID, svc.app.briefPrivacyPrint(), svc.readDay()); found {
		t.Fatal("an old-format copy was read as sealed")
	}
	if read, ok, err := svc.finishedLatest(); err != nil || !ok || read.BriefID != brief.BriefID {
		t.Fatalf("old-format read = %s (ok=%v err=%v)", read.BriefID, ok, err)
	}
	saved, found, err := svc.store.loadFinishedRead(brief.BriefID)
	if err != nil || !found || saved.Schema != mobileBriefSealedSchema {
		t.Fatalf("old-format copy was not sealed: %+v found=%v err=%v", saved, found, err)
	}
	sealsAfter := mobileBriefFinishedReadKeys.Load()
	if _, _, err := svc.finishedLatest(); err != nil {
		t.Fatal(err)
	}
	if mobileBriefFinishedReadKeys.Load() != sealsAfter {
		t.Fatal("the sealed copy was sealed again on the next read")
	}
}

// The privacy epoch is saved, so a restart does not seal every brief again.
func TestPrivacyEpochSurvivesRestart(t *testing.T) {
	_, svc, brief := cardRig(t)
	svc.todayRefreshWG.Wait()
	svc.wg.Wait()
	briefPrivacyChanged()
	briefPrivacyChanged()
	if _, _, err := svc.finishedLatest(); err != nil {
		t.Fatal(err)
	}
	epoch := mobileBriefPrivacyEpoch.Load()

	// A restart: the in-memory epoch is gone and the directory is opened again.
	mobileBriefPrivacyEpoch.Store(0)
	mobileBriefPrivacyEpochSavedDir = ""
	adoptMobileBriefPrivacyEpoch(svc.store.dir)
	if got := mobileBriefPrivacyEpoch.Load(); got != epoch {
		t.Fatalf("epoch after restart = %d, want %d", got, epoch)
	}
	if _, found := readSealedBrief(svc.store, brief.BriefID, svc.app.briefPrivacyPrint(), svc.readDay()); !found {
		t.Fatal("a restart made the sealed brief unreadable as sealed")
	}
}

// An empty or template brief never replaces the record of the last brief with
// words (R61).
func TestEmptyBriefNeverReplacesTheLatestShownRecord(t *testing.T) {
	_, svc, _ := cardRig(t)
	svc.todayRefreshWG.Wait()
	svc.wg.Wait()
	written := mobileBriefCurrentWritten(t, "2026-11-04", mobileBriefSlotEvening, 18, "Evening words.")
	if err := svc.store.save(written, svc.now()); err != nil {
		t.Fatal(err)
	}
	svc.noteLatestShown(written)

	empty := mobileBriefStoredBrief(t, "2026-11-05", mobileBriefSlotMorning, 7)
	if err := svc.store.save(empty, svc.now()); err != nil {
		t.Fatal(err)
	}
	svc.noteLatestShownIfNewer(empty)
	record, ok := svc.store.readLatestShown()
	if !ok || record.BriefID != written.BriefID {
		t.Fatalf("the record names %+v, want %s to stay", record, written.BriefID)
	}
}

// A brief stored before sealed copies existed (legacy) has no copy. The policy-loaded
// event at startup seals every stored brief once, so the legacy brief gets its copy
// without a read doing the work.
func TestLegacyBriefGetsItsCopyAtPolicyLoad(t *testing.T) {
	_, svc, brief := cardRig(t)
	svc.wg.Wait()
	path, ok := svc.store.finishedReadPath(brief.BriefID)
	if !ok {
		t.Fatal("no copy path")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	briefPolicyPending.Store(true)
	t.Cleanup(func() { briefPolicyPending.Store(false) })
	briefObservationPolicyLoaded()
	svc.wg.Wait()
	saved, found, err := svc.store.loadFinishedRead(brief.BriefID)
	if err != nil || !found || saved.Schema != mobileBriefSealedSchema {
		t.Fatalf("the legacy brief was not sealed at policy load: found=%v err=%v", found, err)
	}
}

// A privacy change whose seal loses a race to a newer stored version is sealed
// again from that version, not withheld: the brief was hidden for a minute when
// a privacy epoch bump and a Today refresh landed during one seal.
func TestPrivacyAmendSealsTheNewerStoredVersionAfterARace(t *testing.T) {
	_, svc, brief := cardRig(t)
	svc.todayRefreshWG.Wait()
	svc.wg.Wait()
	if _, _, err := svc.finishedLatest(); err != nil {
		t.Fatal(err)
	}
	briefPrivacyChanged()
	newer := brief
	newer.WeekLead = "An invented week lead for the race test."
	raced := false
	mobileBriefBeforeSealSave = func(id string) {
		if raced || id != brief.BriefID {
			return
		}
		raced = true
		if err := svc.store.save(newer, mobileBriefAsOfTime(newer)); err != nil {
			t.Errorf("the racing save failed: %v", err)
		}
	}
	t.Cleanup(func() { mobileBriefBeforeSealSave = nil })

	if _, _, err := svc.finishedLatest(); err != nil {
		t.Fatal(err)
	}
	if !raced {
		t.Fatal("the race did not happen")
	}
	saved, found, err := svc.store.loadFinishedRead(brief.BriefID)
	if err != nil || !found {
		t.Fatalf("no amended copy: found=%v err=%v", found, err)
	}
	if saved.WithheldReason != "" {
		t.Fatalf("the amended copy is withheld (%s) after a race a retry could win", saved.WithheldReason)
	}
	stored, ok := svc.store.load(brief.BriefID)
	if !ok || saved.Source != mobileBriefSourceDigest(stored) {
		t.Fatal("the amended copy was not sealed from the newer stored version")
	}
	if saved.Print != svc.app.briefPrivacyPrint() {
		t.Fatal("the amended copy was not sealed under the current privacy print")
	}
}

// The withholding names the privacy input that moved, so the log explains itself.
func TestBriefPrivacyChangeKindNamesTheInput(t *testing.T) {
	cases := []struct{ saved, current, want string }{
		{"s|false|false|aa", "t|false|false|aa", "epoch"},
		{"t|false|false|aa", "t|true|false|aa", "pause"},
		{"t|false|false|aa", "t|false|false|bb", "sensitive_apps"},
		{"t|false|false|aa", "u|false|false|bb", "epoch,sensitive_apps"},
		{"t|false|false|aa", "t|false|false|aa", "print"},
	}
	for _, c := range cases {
		if got := briefPrivacyChangeKind(c.saved, c.current); got != c.want {
			t.Fatalf("briefPrivacyChangeKind(%q, %q) = %q, want %q", c.saved, c.current, got, c.want)
		}
	}
}
