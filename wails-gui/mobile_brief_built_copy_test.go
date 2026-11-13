package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The build's copy is written by the build only. A later save of a stripped
// copy (a Today refresh, an amend) leaves it, so the words can be gated again.
func TestBuiltCopySurvivesAStrippedSave(t *testing.T) {
	store := newMobileBriefStore(t.TempDir())
	now := time.Date(2026, 11, 12, 17, 51, 0, 0, time.UTC)
	brief := buildMobileBriefV1(mobileBriefInput{Slot: mobileBriefSlotEvening, AsOf: now, WindowStart: now.Add(-8 * time.Hour), Location: time.UTC,
		Moved: []mobileBriefCandidate{{SourceKind: mobileBriefRecapSource, SourceID: "act-1", Title: "Shipped the brief fix", Status: "done", Kind: "recap", At: now.Add(-time.Hour)}}})
	brief.NarrativeStatus = "ok"
	brief.Narrative = []MobileBriefPara{{Text: "You shipped the brief fix.", Refs: []string{brief.Evidence[0].Ref}, Part: briefPartDone}}
	brief.Audio = &MobileBriefAudio{Status: mobileBriefAudioStatusOK}
	if err := store.saveDocument(brief, now); err != nil {
		t.Fatal(err)
	}
	if err := store.saveBuilt(brief); err != nil {
		t.Fatal(err)
	}
	stripped := brief
	stripped.Narrative, stripped.NarrativeStatus, stripped.Audio = []MobileBriefPara{}, "unavailable", nil
	if err := store.save(stripped, now); err != nil {
		t.Fatal(err)
	}
	built, ok := store.loadBuilt(brief.BriefID)
	if !ok || len(built.Narrative) != 1 {
		t.Fatal("a stripped save overwrote the build's copy")
	}
	loaded, _ := store.load(brief.BriefID)
	restored := store.withBuiltWords(loaded)
	if len(restored.Narrative) != 1 || restored.NarrativeStatus != "ok" || restored.Audio == nil {
		t.Fatalf("the build's words did not come back for the gates: %+v", restored.Narrative)
	}
	// A different build of the same slot never takes these words.
	other := loaded
	other.AsOf = formatMobileBriefTime(now.Add(time.Hour))
	if got := store.withBuiltWords(other); len(got.Narrative) != 0 {
		t.Fatal("words from another build were attached")
	}
	if _, err := os.Stat(filepath.Join(store.dir, brief.BriefID+mobileBriefBuiltExt)); err != nil {
		t.Fatal(err)
	}
}

// What a refresh stores keeps the build's words even when the gates withheld
// them; the gated copy goes to the seal only.
func TestRefreshStoresTheWordsNotTheGatedCopy(t *testing.T) {
	base := MobileBriefV1{NarrativeStatus: "ok", Narrative: []MobileBriefPara{{Text: "You shipped it.", Refs: []string{"e1"}, Part: briefPartDone}},
		Evidence: []MobileBriefRef{{Ref: "e1", SourceKind: "summary", SourceID: "act-1"}}}
	gated := MobileBriefV1{NarrativeStatus: "unavailable", Narrative: []MobileBriefPara{}}
	saved := mobileBriefWithWordsOf(gated, base)
	if len(saved.Narrative) != 1 || saved.NarrativeStatus != "ok" || len(saved.Evidence) != 1 {
		t.Fatalf("the stored brief lost the build's words: %+v", saved)
	}
	if len(gated.Narrative) != 0 {
		t.Fatal("the gated copy was changed")
	}
}
