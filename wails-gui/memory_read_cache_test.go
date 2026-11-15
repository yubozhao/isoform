package main

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"wails-gui/internal/pii"
	"wails-gui/storage"
)

// P0 (today-review v1.1.13 D1, D2): every brief read re-read and redacted all
// of memory, and brief reads are polled, so Isoform sat at 400-750% CPU at
// rest. Reads with unchanged memory must reuse the redacted copy.

type memoryReadCountRig struct {
	app     *App
	session string
	dir     string
	loads   map[string]int
}

func newMemoryReadCountRig(t *testing.T) *memoryReadCountRig {
	t.Helper()
	rig := newHandoffTestRig(t)
	// The counts come from a process-wide hook, so the chat's background
	// recompute (its own memory read) must not run inside a counted read.
	rig.app.streamPages.mu.Lock()
	rig.app.streamPages.settings()
	rig.app.streamPages.launchDelay = time.Hour
	rig.app.streamPages.eventDelay = time.Hour
	rig.app.streamPages.mu.Unlock()
	clock := &fakeMobileBriefClock{now: mobileBriefTestDay(7, 0)}
	svc, _ := newTestMobileBriefService(t, clock)
	svc.app, rig.app.mobileBriefSvc = rig.app, svc
	svc.speaker = fakeSpeaker("en", testWAV(2), nil)
	seedMobileBriefReadableHistory(t, rig.app, "h-morning")
	dir, err := memoryDir()
	if err != nil {
		t.Fatal(err)
	}
	for id, body := range map[string]string{
		"brief-preference": "# Brief preference\n\nKeep Juniper Gym out of my morning brief.\n",
		"contact-rowan":    "# Rowan Vale\n\nRowan Vale writes from rowan.vale@example.com about the Harbor Lane lease.\n",
		"weekly-rhythm":    "# Weekly rhythm\n\nPlans the week on Monday mornings.\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, id+".md"), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	settleMemoryFolder(t, dir)
	if _, err := svc.generate(context.Background(), mobileBriefSlotMorning, mobileBriefReasonSchedule); err != nil {
		t.Fatal(err)
	}
	// The query-count probe swaps the store handles. Let the generated audio
	// finish using them before any test installs that probe.
	svc.wg.Wait()
	main, err := rig.app.ensureMainChat(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	if err := rig.app.store.SaveStreamItems(t.Context(), main.LocalID, []storage.StreamItemRow{
		{ID: "ask-week", Kind: "user", Content: "What should I look at this week?", CreatedAt: now},
		{ID: "answer-week", Kind: "assistant", Content: "Start with the Harbor Lane lease.", CreatedAt: now + 1},
	}); err != nil {
		t.Fatal(err)
	}
	r := &memoryReadCountRig{app: rig.app, session: main.LocalID, dir: dir, loads: map[string]int{}}
	previous := memoryExternalReadLoad
	memoryExternalReadLoad = func(kind string) { r.loads[kind]++ }
	t.Cleanup(func() { memoryExternalReadLoad = previous })
	return r
}

// settleMemoryFolder ages the notes past memoryFolderSettle, as notes at rest are.
func settleMemoryFolder(t *testing.T, dir string) {
	t.Helper()
	old := time.Now().Add(-time.Hour)
	files, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if strings.HasSuffix(file.Name(), ".md") {
			if err := os.Chtimes(filepath.Join(dir, file.Name()), old, old); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func (r *memoryReadCountRig) read(t *testing.T, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		_ = r.app.GetMobileBriefHealth()
		if _, err := r.app.LoadStreamItemsPage(r.session, -1, 50); err != nil {
			t.Fatal(err)
		}
	}
}

func TestBriefReadsDoNotRedactMemoryAgainWhileItIsUnchanged(t *testing.T) {
	r := newMemoryReadCountRig(t)
	// Start cold, as at launch: the first read does the work once.
	r.app.invalidateMemoryExternalReads()
	// Intended cost change (brief-rebuild PR 1): sealed briefs read plainly, so the
	// memory work happens when a brief is sealed. A source change seals them now.
	briefSourceChanged()
	r.read(t, 1)
	t.Logf("first read: memory reads=%d memory redactions=%d exclusion scans=%d", r.loads["entries"], r.loads["redact"], r.loads["exclusions"])
	if r.loads["entries"] != 1 || r.loads["redact"] == 0 || r.loads["exclusions"] == 0 {
		t.Fatalf("first read did not read, redact and scan memory once: %v", r.loads)
	}
	clear(r.loads)
	for _, n := range []int{1, 10, 50} {
		piiBefore := pii.RedactTextCalls()
		r.read(t, n)
		t.Logf("%d x (GetMobileBriefHealth + LoadStreamItemsPage): memory reads=%d memory redactions=%d exclusion scans=%d outcome redactions=%d pii.RedactText=%d",
			n, r.loads["entries"], r.loads["redact"], r.loads["exclusions"], r.loads["threadOutcomeRedact"], pii.RedactTextCalls()-piiBefore)
		for _, kind := range []string{"entries", "redact", "exclusions"} {
			if r.loads[kind] != 0 {
				t.Fatalf("%d reads with unchanged memory: %s ran %d times, want 0", n, kind, r.loads[kind])
			}
		}
		if calls := pii.RedactTextCalls() - piiBefore; calls != 0 {
			t.Fatalf("%d reads with unchanged memory: pii.RedactText ran %d times, want 0", n, calls)
		}
		clear(r.loads)
	}
}

func TestBriefMemoryCacheKeepsPrivacyAndTakesChangesAtOnce(t *testing.T) {
	r := newMemoryReadCountRig(t)
	a := r.app
	entries, err := a.memoryEntriesForExternalRead()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := a.GetMemoryEntries()
	if err != nil {
		t.Fatal(err)
	}
	var want []MemoryEntry
	for _, entry := range raw {
		want = append(want, redactMemoryEntryForExternalRead(entry))
	}
	if len(entries) != len(want) {
		t.Fatalf("external read has %d entries, want %d", len(entries), len(want))
	}
	for i := range want {
		if entries[i].ID != want[i].ID || entries[i].Title != want[i].Title || entries[i].Content != want[i].Content {
			t.Fatalf("cached redaction differs from a fresh one:\ngot  %+v\nwant %+v", entries[i], want[i])
		}
		if strings.Contains(entries[i].Content, "rowan.vale@example.com") {
			t.Fatalf("email reached an external reader: %q", entries[i].Content)
		}
	}
	// A caller editing its copy cannot change what the next reader sees.
	entries[0].Content, entries[0].Apps = "edited", append(entries[0].Apps, "Edited")
	again, _ := a.memoryEntriesForExternalRead()
	if again[0].Content == "edited" || slices.Contains(again[0].Apps, "Edited") {
		t.Fatalf("a caller's edit leaked into the cache: %+v", again[0])
	}

	if got := a.mobileBriefExclusions(); !slices.Equal(got, []string{"Juniper Gym"}) {
		t.Fatalf("exclusions = %v", got)
	}

	// An app write takes effect on the next read.
	if _, err := a.SaveMemoryEntry("brief-preference", "# Brief preference\n\nKeep Harbor Choir out of my morning brief.\n"); err != nil {
		t.Fatal(err)
	}
	if got := a.mobileBriefExclusions(); !slices.Equal(got, []string{"Harbor Choir"}) {
		t.Fatalf("exclusions after a write = %v", got)
	}

	// So does an edit made outside the app, even a same-size one, by the
	// next folder check.
	advance := fakeMemoryReadClock(t)
	settleMemoryFolder(t, r.dir)
	_ = a.mobileBriefExclusions()
	if err := os.WriteFile(filepath.Join(r.dir, "brief-preference.md"), []byte("# Brief preference\n\nKeep Harbor Chair out of my morning brief.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	advance(memoryFolderCheckEvery)
	if got := a.mobileBriefExclusions(); !slices.Equal(got, []string{"Harbor Chair"}) {
		t.Fatalf("exclusions after an outside edit = %v", got)
	}

	// A forgotten note is gone at once.
	if err := a.DeleteMemoryEntry("brief-preference"); err != nil {
		t.Fatal(err)
	}
	if got := a.mobileBriefExclusions(); len(got) != 0 {
		t.Fatalf("exclusions after delete = %v", got)
	}
	after, _ := a.memoryEntriesForExternalRead()
	if slices.ContainsFunc(after, func(entry MemoryEntry) bool { return entry.ID == "brief-preference" }) {
		t.Fatal("deleted note still read")
	}

	// The read policy still runs on every read: Closed Eyes hides a warm cache.
	settleMemoryFolder(t, r.dir)
	_, _ = a.memoryEntriesForExternalRead()
	if err := a.SetClosedEyesModeEnabled(true); err != nil {
		t.Fatal(err)
	}
	if closed, _ := a.memoryEntriesForExternalRead(); len(closed) != 0 {
		t.Fatalf("Closed Eyes read %d entries", len(closed))
	}
	if err := a.SetClosedEyesModeEnabled(false); err != nil {
		t.Fatal(err)
	}
	if open, _ := a.memoryEntriesForExternalRead(); len(open) == 0 {
		t.Fatal("entries did not come back after Closed Eyes")
	}
}

func TestThreadOutcomeRedactionIsRememberedByInput(t *testing.T) {
	previous := memoryExternalReadLoad
	loads := 0
	memoryExternalReadLoad = func(kind string) {
		if kind == "threadOutcomeRedact" {
			loads++
		}
	}
	t.Cleanup(func() { memoryExternalReadLoad = previous })
	text := "Order 88812345 for Juniper Gym, reply to rowan.vale@example.com, code 482913"
	want := redactThreadOutcomeTextUncached(text)
	loads = 0
	for i := 0; i < 5; i++ {
		if got := redactThreadOutcomeText(text); got != want {
			t.Fatalf("remembered redaction = %q, want %q", got, want)
		}
	}
	if loads != 1 {
		t.Fatalf("redacted %d times for one input, want 1", loads)
	}
	if strings.Contains(want, "rowan.vale@example.com") {
		t.Fatalf("email kept: %q", want)
	}
}
