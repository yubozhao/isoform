package brief

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

const (
	testDay    = "2026-10-11"
	testID     = "2026-10-11-evening"
	testOlder  = "2026-10-10-morning"
	testPrint5 = "5|false|false|apps"
)

// writeTestFile writes content to dir/name and fails the test on error.
func writeTestFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

// testBrief is a built brief with two paragraphs, one card and audio.
const testBrief = `{"briefId":"` + testID + `","slot":"evening","localDate":"` + testDay + `",` +
	`"asOf":"2026-10-11T19:00:00Z","narrativeStatus":"ok",` +
	`"narrative":[{"text":"a","refs":["r1"]},{"text":"b","refs":["r2"]}],` +
	`"cards":[{"title":"c"}],"evidence":[{"ref":"r1","sourceId":"s1"},{"ref":"r2","sourceId":"s2"}],` +
	`"audio":{"status":"ready"}}`

// copyJSON returns a sealed copy of brief under print and day.
func copyJSON(print, day, brief string, extra string) string {
	return `{"schema":2,"rule":2,"epoch":5,"print":"` + print + `","day":"` + day + `"` + extra +
		`,"source":"x","brief":` + brief + `}`
}

func newTestFolder(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeTestFile(t, dir, testID+".json", testBrief)
	writeTestFile(t, dir, "privacy-epoch", "5")
	return dir
}

func TestGetShowsCopyUnderCurrentPrint(t *testing.T) {
	dir := newTestFolder(t)
	writeTestFile(t, dir, testID+".read", copyJSON(testPrint5, testDay, testBrief, ""))
	r := NewReader(ReaderOptions{Dir: dir, Print: func() string { return testPrint5 }, Day: func() string { return testDay }})
	sealed, ok := r.Get(testID)
	if !ok || sealed.Decision != DecisionShown {
		t.Fatalf("Get = %+v, %t; want shown", sealed.Decision, ok)
	}
	want := Counts{Narrative: 2, Cards: 1, Evidence: 2, Audio: true}
	if sealed.Counts != want {
		t.Fatalf("counts = %+v, want %+v", sealed.Counts, want)
	}
}

func TestGetWithholdsContentWhenPrintMovedInAnotherInput(t *testing.T) {
	dir := newTestFolder(t)
	writeTestFile(t, dir, testID+".read", copyJSON(testPrint5, testDay, testBrief, ""))
	pause := "5|true|false|apps"
	r := NewReader(ReaderOptions{Dir: dir, Print: func() string { return pause }})
	sealed, ok := r.Get(testID)
	if ok || sealed.Decision != DecisionAmendGates {
		t.Fatalf("Get decision = %q, ok %t; want amend_gates without ok", sealed.Decision, ok)
	}
	if sealed.Raw != nil || sealed.Document.BriefID != "" || sealed.Counts != (Counts{}) {
		t.Fatalf("amend result carries content: %+v", sealed)
	}
}

func TestEpochOnlyChangeWithKeyedLogIsAmendKeyed(t *testing.T) {
	dir := newTestFolder(t)
	writeTestFile(t, dir, testID+".read", copyJSON(testPrint5, testDay, testBrief, ""))
	writeTestFile(t, dir, "privacy-epoch", "6")
	writeTestFile(t, dir, "withdrawals.json", `{"floor":5,"entries":[{"epoch":6,"keys":[{"id":"s1"}]}]}`)
	r := NewReader(ReaderOptions{Dir: dir})
	if got := r.Inspect(testID).Decision; got != DecisionAmendKeyed {
		t.Fatalf("decision = %q, want amend_keyed", got)
	}
}

func TestUnnamedChangeInLogIsAmendGates(t *testing.T) {
	dir := newTestFolder(t)
	writeTestFile(t, dir, testID+".read", copyJSON(testPrint5, testDay, testBrief, ""))
	writeTestFile(t, dir, "privacy-epoch", "6")
	writeTestFile(t, dir, "withdrawals.json", `{"floor":5,"entries":[{"epoch":6}]}`)
	r := NewReader(ReaderOptions{Dir: dir})
	if got := r.Inspect(testID).Decision; got != DecisionAmendGates {
		t.Fatalf("decision = %q, want amend_gates", got)
	}
}

func TestDayChangeIsResealButStillShown(t *testing.T) {
	dir := newTestFolder(t)
	writeTestFile(t, dir, testID+".read", copyJSON(testPrint5, testDay, testBrief, ""))
	r := NewReader(ReaderOptions{Dir: dir, Print: func() string { return testPrint5 }, Day: func() string { return "2026-10-12" }})
	sealed, ok := r.Get(testID)
	if !ok || sealed.Decision != DecisionReseal || sealed.Raw == nil {
		t.Fatalf("Get = %q, ok %t; want reseal with content", sealed.Decision, ok)
	}
}

func TestMissingUnreadableAndOlderCopies(t *testing.T) {
	dir := newTestFolder(t)
	r := NewReader(ReaderOptions{Dir: dir})
	if got := r.Inspect(testID).Decision; got != DecisionNoCopy {
		t.Fatalf("missing copy: %q", got)
	}
	writeTestFile(t, dir, testID+".read", "{not json")
	if got := r.Inspect(testID).Decision; got != DecisionUnreadable {
		t.Fatalf("garbage copy: %q", got)
	}
	writeTestFile(t, dir, testID+".read", `{"schema":1,"brief":{}}`)
	if got := r.Inspect(testID).Decision; got != DecisionOlderSchema {
		t.Fatalf("schema 1 copy: %q", got)
	}
}

func TestHeldBriefIsNotLatestAndQuietIsNotListed(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, testOlder+".json", `{"briefId":"`+testOlder+`","asOf":"2026-10-10T08:00:00Z","localDate":"2026-10-10","slot":"morning","narrativeStatus":"quiet"}`)
	writeTestFile(t, dir, testID+".json", `{"briefId":"`+testID+`","asOf":"2026-10-11T19:00:00Z","localDate":"`+testDay+`","slot":"evening","narrativeStatus":"ok",`+
		`"generation":{"reason":"made_ahead","scheduledAt":"2099-01-01T08:00:00Z"}}`)
	writeTestFile(t, dir, "latest.json", `{"version":1,"briefId":"`+testID+`"}`)
	now := time.Date(2026, 10, 11, 20, 0, 0, 0, time.UTC)
	r := NewReader(ReaderOptions{Dir: dir, Now: func() time.Time { return now }})
	// The pointer names a held brief, so the newest brief that is not held is
	// returned instead (the app's latest() fallback). Quiet briefs stay latest-eligible.
	if doc, ok := r.LatestBuilt(); !ok || doc.BriefID != testOlder {
		t.Fatalf("latest = %q, %t; want the older brief after the held pointer", doc.BriefID, ok)
	}
	if rows := r.List(0); len(rows) != 0 {
		t.Fatalf("list = %+v, want none (held and quiet)", rows)
	}
}

// TestReaderNeverWritesTheFolder checks that every read leaves each file's bytes
// unchanged and creates or removes nothing, including on an unreadable copy.
func TestReaderNeverWritesTheFolder(t *testing.T) {
	dir := newTestFolder(t)
	writeTestFile(t, dir, testID+".read", "{not json")
	writeTestFile(t, dir, "latest.json", `{"version":1,"briefId":"`+testID+`"}`)
	before := snapshotFolder(t, dir)
	r := NewReader(ReaderOptions{Dir: dir})
	r.Get(testID)
	r.Inspect(testID)
	r.LatestBuilt()
	r.List(0)
	r.Withdrawn()
	r.Epoch()
	after := snapshotFolder(t, dir)
	if len(before) != len(after) {
		t.Fatalf("file count changed: %d -> %d", len(before), len(after))
	}
	for name, data := range before {
		if !bytes.Equal(after[name], data) {
			t.Fatalf("%s changed during reads", name)
		}
	}
}

func snapshotFolder(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]byte{}
	for _, entry := range entries {
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		out[entry.Name()] = data
	}
	return out
}

func TestEpochNumberIsBase10OnDisk(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "privacy-epoch", strconv.FormatUint(42, 10)+"\n")
	if got := NewReader(ReaderOptions{Dir: dir}).Epoch(); got != 42 {
		t.Fatalf("Epoch = %d, want 42", got)
	}
}
