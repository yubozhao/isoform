package main

import (
	"testing"
	"time"
)

// Today's brief reads list the stored briefs by header; a header must match
// briefs() and a brief file is decoded again only after it changed on disk
// (2026-11-15: decoding all 42 briefs per read held GetLatestBrief and
// GetMobileBriefHealth at 100 ms to 1.4 s).
func TestMobileBriefHeadersDecodeEachFileOnce(t *testing.T) {
	store := newMobileBriefStore(t.TempDir())
	now := time.Date(2026, 11, 15, 20, 0, 0, 0, time.UTC)
	for i, slot := range []string{mobileBriefSlotMorning, mobileBriefSlotEvening} {
		brief := MobileBriefV1{
			BriefID: "2026-11-15-" + slot, Slot: slot, LocalDate: "2026-11-15",
			AsOf:            now.Add(time.Duration(i-2) * time.Hour).Format(time.RFC3339Nano),
			NarrativeStatus: "ok", Narrative: []MobileBriefPara{{Text: "words"}},
		}
		if err := store.save(brief, now); err != nil {
			t.Fatal(err)
		}
	}
	same := func() {
		t.Helper()
		headers, briefs := store.headers(), store.briefs()
		if len(headers) != len(briefs) {
			t.Fatalf("headers %d, briefs %d", len(headers), len(briefs))
		}
		for i := range briefs {
			if headers[i] != mobileBriefHeaderOf(briefs[i]) {
				t.Fatalf("header %d = %+v, brief says %+v", i, headers[i], mobileBriefHeaderOf(briefs[i]))
			}
		}
	}
	start := mobileBriefHeaderDecodes(store.dir)
	same()
	same()
	if got := mobileBriefHeaderDecodes(store.dir) - start; got != 2 {
		t.Fatalf("two reads of two briefs decoded %d files, want 2", got)
	}
	changed, _ := store.load("2026-11-15-evening")
	changed.NarrativeStatus, changed.Narrative = "quiet", nil
	if err := store.save(changed, now); err != nil {
		t.Fatal(err)
	}
	same()
	if got := mobileBriefHeaderDecodes(store.dir) - start; got != 3 {
		t.Fatalf("after one rewrite decoded %d files, want 3", got)
	}
	if store.headers()[0].NarrativeStatus != "quiet" {
		t.Fatal("the rewritten brief kept its old header")
	}
}
