package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"wails-gui/internal/handoff"
	"wails-gui/internal/pii"
	"wails-gui/storage"
)

var updateMobileBriefGolden = flag.Bool(
	"update-mobile-brief-golden",
	false,
	"rewrite testdata/mobile_brief_golden_v1.json from the current builder output",
)

// mobileBriefGoldenInput is a fixed fixture with one or more items in every
// section. Its output is compared byte for byte with the golden file.
func mobileBriefGoldenInput() mobileBriefInput {
	return mobileBriefInput{
		Slot:        "morning",
		WindowStart: time.Date(2026, 10, 8, 1, 0, 0, 0, time.UTC),
		AsOf:        time.Date(2026, 10, 9, 14, 0, 12, 0, time.UTC),
		Location:    time.FixedZone("PDT", -7*60*60),
		Timezone:    "America/Los_Angeles",
		HubName:     "Alex's MacBook Pro",
		Moved: []mobileBriefCandidate{
			{
				SourceKind: "history", SourceID: "h-1",
				Title: "Sent the Q3 deck to Dana", Detail: "Dana replied within the hour",
				Status: "verified", Kind: "initiative_completed",
				At: time.Date(2026, 10, 8, 19, 5, 0, 0, time.UTC),
			},
			{
				SourceKind: "history", SourceID: "h-2",
				Title: "Filed the expense report", Detail: "Receipts attached for September",
				Kind: "initiative_completed",
				At:   time.Date(2026, 10, 8, 22, 40, 0, 0, time.UTC),
			},
		},
		Doing: []mobileBriefCandidate{
			{
				SourceKind: "initiative", SourceID: "i-1",
				Title: "Draft the launch note", Detail: "Waiting on legal review",
				Status: "waiting", Kind: "initiative",
				At: time.Date(2026, 10, 9, 9, 15, 0, 0, time.UTC),
			},
		},
		Watching: []mobileBriefCandidate{
			{
				SourceKind: "watching", SourceID: "w-1",
				Title: "Vendor quote", Detail: "Waiting for a revised quote",
				Status: "watching", Kind: "gmail",
				At: time.Date(2026, 10, 9, 8, 0, 0, 0, time.UTC),
			},
		},
		Meetings: []mobileBriefCandidate{
			{
				SourceKind: "meeting", SourceID: "m-1",
				Title: "Zoom", Detail: "09:30-10:00", Kind: "meeting",
				At:   time.Date(2026, 10, 9, 16, 30, 0, 0, time.UTC),
				Apps: []string{"Zoom"},
			},
		},
		People: []mobileBriefCandidate{
			{
				SourceKind: "principal", SourceID: "p-1",
				Title: "Dana Lee", Status: "seen", Kind: "contact",
				At:        time.Date(2026, 10, 9, 13, 10, 0, 0, time.UTC),
				Apps:      []string{"Slack"},
				AppsShown: true,
			},
		},
		OpenLoops: []mobileBriefCandidate{
			{
				SourceKind: "purpose", SourceID: "o-1",
				Title: "Send the pricing draft", Detail: "Dana is waiting on the numbers",
				Status: "active", Kind: "commitment",
				At: time.Date(2026, 10, 9, 7, 0, 0, 0, time.UTC),
			},
			{
				SourceKind: "purpose", SourceID: "o-2",
				Title: "Book the offsite room", Status: "waiting", Kind: "commitment",
				At: time.Date(2026, 10, 8, 20, 0, 0, 0, time.UTC),
			},
			{
				SourceKind: "purpose", SourceID: "o-3",
				Title: "Check the vendor invoice", Status: "active", Kind: "follow_up",
				At: time.Date(2026, 10, 7, 20, 0, 0, 0, time.UTC),
			},
		},
		Learned: []mobileBriefCandidate{
			{
				SourceKind: "learning", SourceID: "l-1",
				Title:  "Design review replies usually arrive in the afternoon",
				Detail: "Three weeks of evidence", Status: "supported", Kind: "supported",
				At: time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC),
			},
			{
				SourceKind: "memory", SourceID: "mem-1",
				Title: "Prefers short status updates", Status: "updated", Kind: "memory",
				At:        time.Date(2026, 10, 8, 10, 0, 0, 0, time.UTC),
				Apps:      []string{"Slack"},
				AppsShown: true,
			},
		},
		Deliverables: []mobileBriefCandidate{
			{
				SourceKind: "deliverable", SourceID: "chat:d-1",
				Title: "Q3 deck v3", Status: "completed", Kind: "chat",
				At: time.Date(2026, 10, 8, 18, 0, 0, 0, time.UTC),
			},
		},
		Schedules: []mobileBriefCandidate{
			{
				SourceKind: "schedule_run", SourceID: "r-1",
				Title: "Inbox summary", Status: "ok", Kind: "schedule",
				At: time.Date(2026, 10, 9, 14, 0, 0, 0, time.UTC),
			},
		},
		DayShape: &MobileBriefDay{
			ActiveMinutes: 312,
			Hours: []MobileBriefHour{
				{Hour: 9, ActiveMinutes: 42},
				{Hour: 10, ActiveMinutes: 55},
			},
			TopApps: []MobileBriefAppTime{
				{Name: "Figma", ActiveMinutes: 90},
				{Name: "Slack", ActiveMinutes: 60},
			},
		},
	}
}

// The unchanged historical snapshots predate observation-first reservation.
// Apply only the expected five-reference permutation, retaining a byte-exact
// comparison of every other field without rewriting the non-Go fixtures.
func mobileBriefObservedGolden(t *testing.T, original []byte) []byte {
	t.Helper()
	var expected MobileBriefV1
	if err := json.Unmarshal(original, &expected); err != nil {
		t.Fatal(err)
	}
	if len(expected.Evidence) != 13 {
		t.Fatalf("unexpected golden evidence count: %d", len(expected.Evidence))
	}
	oldKinds := []string{"watching", "meeting", "principal", "memory", "learning"}
	newKinds := []string{"principal", "memory", "learning", "watching", "meeting"}
	var kinds []string
	for _, ref := range expected.Evidence[6:11] {
		kinds = append(kinds, ref.SourceKind)
	}
	if reflect.DeepEqual(kinds, newKinds) {
		return original
	}
	if !reflect.DeepEqual(kinds, oldKinds) {
		t.Fatalf("unexpected golden evidence order: %v", kinds)
	}
	previous := append([]MobileBriefRef(nil), expected.Evidence...)
	remap := map[string]string{}
	for offset, oldIndex := range []int{8, 9, 10, 6, 7} {
		index := 6 + offset
		expected.Evidence[index] = previous[oldIndex]
		expected.Evidence[index].Ref = previous[index].Ref
		remap[previous[oldIndex].Ref] = previous[index].Ref
	}
	for _, section := range mobileBriefSectionLines(expected.Sections) {
		for i := range section.items {
			for j, ref := range section.items[i].Refs {
				if next, ok := remap[ref]; ok {
					section.items[i].Refs[j] = next
				}
			}
		}
	}
	result, err := json.MarshalIndent(expected, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(result, '\n')
}

func TestBuildMobileBriefGoldenV1(t *testing.T) {
	brief := buildMobileBriefV1(mobileBriefGoldenInput())
	got, err := json.MarshalIndent(brief, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')

	path := filepath.Join("testdata", "mobile_brief_golden_v1.json")
	if *updateMobileBriefGolden {
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden file (create it with -update-mobile-brief-golden): %v", err)
	}
	want = mobileBriefObservedGolden(t, want)
	if !bytes.Equal(got, want) {
		t.Fatalf("brief differs from %s; rerun with -update-mobile-brief-golden after reviewing:\n%s", path, got)
	}
}

// An early brief made at 11:20 PM on October 9 (Pacific) for the next morning's
// slot takes that morning's day for its ID and date, and gathers nothing after
// it was made. Its output is locked by its own golden file.
func TestBuildMobileBriefGoldenEarlyV1(t *testing.T) {
	in := mobileBriefGoldenInput()
	in.AsOf = time.Date(2026, 10, 10, 6, 20, 0, 0, time.UTC)
	in.BriefDay = time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	brief := buildMobileBriefV1(in)
	if brief.BriefID != "2026-10-10-morning" || brief.LocalDate != "2026-10-10" {
		t.Fatalf("early brief %q for %q, want the next morning's slot", brief.BriefID, brief.LocalDate)
	}
	got, err := json.MarshalIndent(brief, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')

	path := filepath.Join("testdata", "mobile_brief_golden_early_v1.json")
	if *updateMobileBriefGolden {
		if err := os.WriteFile(path, got, 0644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden file (create it with -update-mobile-brief-golden): %v", err)
	}
	want = mobileBriefObservedGolden(t, want)
	if !bytes.Equal(got, want) {
		t.Fatalf("early brief differs from %s; rerun with -update-mobile-brief-golden after reviewing:\n%s", path, got)
	}
}

func TestBuildMobileBriefRefsResolve(t *testing.T) {
	brief := buildMobileBriefV1(mobileBriefGoldenInput())
	if len(brief.Evidence) == 0 {
		t.Fatal("golden brief has no evidence")
	}
	known := map[string]bool{}
	for i, ref := range brief.Evidence {
		if want := "e" + strconv.Itoa(i+1); ref.Ref != want {
			t.Fatalf("evidence %d has ref %q, want %q", i, ref.Ref, want)
		}
		known[ref.Ref] = true
	}
	for _, section := range mobileBriefSectionLines(brief.Sections) {
		for _, item := range section.items {
			if len(item.Refs) == 0 {
				t.Errorf("%s item %s has no refs", section.name, item.ID)
			}
			for _, ref := range item.Refs {
				if !known[ref] {
					t.Errorf("%s item %s cites unknown ref %s", section.name, item.ID, ref)
				}
			}
		}
	}
}

func TestBuildMobileBriefColdStartSkipsNarrative(t *testing.T) {
	in := mobileBriefGoldenInput()
	in.ColdStart = true
	brief := buildMobileBriefV1(in)
	if !brief.ColdStart {
		t.Fatal("coldStart was not carried into the brief")
	}
	if brief.NarrativeStatus != "skipped" {
		t.Fatalf("narrativeStatus = %q, want skipped", brief.NarrativeStatus)
	}
}

func TestBuildMobileBriefEmptySectionsSkipNarrative(t *testing.T) {
	brief := buildMobileBriefV1(mobileBriefInput{
		AsOf:     time.Date(2026, 10, 9, 14, 0, 0, 0, time.UTC),
		Location: time.UTC,
	})
	if brief.NarrativeStatus != "skipped" {
		t.Fatalf("narrativeStatus = %q, want skipped for an empty brief", brief.NarrativeStatus)
	}
}

// TestBuildMobileBriefEmptyInputIsNilSafe checks that empty input yields
// arrays and objects rather than nulls, and no day shape.
func TestBuildMobileBriefEmptyInputIsNilSafe(t *testing.T) {
	brief := buildMobileBriefV1(mobileBriefInput{})
	raw, err := json.Marshal(brief)
	if err != nil {
		t.Fatal(err)
	}
	var generic map[string]any
	if err := json.Unmarshal(raw, &generic); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"narrative", "evidence"} {
		if _, ok := generic[key].([]any); !ok {
			t.Errorf("%s is %T, want an array", key, generic[key])
		}
	}
	sections, _ := generic["sections"].(map[string]any)
	for _, name := range []string{"moved", "doing", "watching", "meetings", "people",
		"openLoops", "learned", "deliverables", "schedules"} {
		if _, ok := sections[name].([]any); !ok {
			t.Errorf("sections.%s is %T, want an array", name, sections[name])
		}
	}
	limits, _ := generic["limits"].(map[string]any)
	if _, ok := limits["truncatedSections"].([]any); !ok {
		t.Errorf("limits.truncatedSections is %T, want an array", limits["truncatedSections"])
	}
	counts, _ := generic["counts"].(map[string]any)
	if _, ok := counts["openLoopsByKind"].(map[string]any); !ok {
		t.Errorf("counts.openLoopsByKind is %T, want an object", counts["openLoopsByKind"])
	}
	if generic["dayShape"] != nil {
		t.Errorf("dayShape = %v, want null without activity", generic["dayShape"])
	}
	if brief.Version != 1 || brief.Timezone != mobileBriefTimezoneName(time.Local) || brief.HubName != mobileBriefDefaultHub {
		t.Errorf("unexpected defaults: version=%d timezone=%q hub=%q",
			brief.Version, brief.Timezone, brief.HubName)
	}
}

func TestBuildMobileBriefSectionsAreBounded(t *testing.T) {
	asOf := time.Date(2026, 10, 9, 14, 0, 0, 0, time.UTC)
	in := mobileBriefInput{Location: time.UTC, AsOf: asOf}
	for i := 0; i < 12; i++ {
		in.Moved = append(in.Moved, mobileBriefCandidate{
			SourceKind: "history",
			SourceID:   "h-" + strconv.Itoa(i),
			Title:      "Item " + strconv.Itoa(i),
			At:         asOf.Add(-time.Duration(i) * time.Minute),
		})
	}
	brief := buildMobileBriefV1(in)
	if got := len(brief.Sections.Moved); got != mobileBriefItemsPerSection {
		t.Fatalf("moved has %d items, want %d", got, mobileBriefItemsPerSection)
	}
	if !reflect.DeepEqual(brief.Limits.TruncatedSections, []string{"moved"}) {
		t.Fatalf("truncatedSections = %v, want [moved]", brief.Limits.TruncatedSections)
	}
	if brief.Sections.Moved[0].Title != "Item 0" {
		t.Fatalf("newest item first: got %q", brief.Sections.Moved[0].Title)
	}
	if brief.Limits.ItemsPerSection != 8 || brief.Limits.EvidenceRefs != 60 {
		t.Fatalf("limits = %+v", brief.Limits)
	}
}

func TestBuildMobileBriefEvidenceBudgetIsBounded(t *testing.T) {
	asOf := time.Date(2026, 10, 9, 14, 0, 0, 0, time.UTC)
	fill := func(prefix string) []mobileBriefCandidate {
		var out []mobileBriefCandidate
		for i := 0; i < 8; i++ {
			out = append(out, mobileBriefCandidate{
				SourceKind: prefix,
				SourceID:   prefix + "-" + strconv.Itoa(i),
				Title:      prefix + " item " + strconv.Itoa(i),
				At:         asOf.Add(-time.Duration(i+1) * time.Minute),
			})
		}
		return out
	}
	in := mobileBriefInput{
		Location:     time.UTC,
		AsOf:         asOf,
		Moved:        fill("history"),
		Doing:        fill("initiative"),
		Watching:     fill("watching"),
		Meetings:     fill("meeting"),
		People:       fill("principal"),
		OpenLoops:    fill("purpose"),
		Learned:      fill("memory"),
		Deliverables: fill("deliverable"),
		Schedules:    fill("schedule_run"),
	}
	brief := buildMobileBriefV1(in)
	if got := len(brief.Evidence); got != mobileBriefEvidenceLimit {
		t.Fatalf("evidence has %d refs, want %d", got, mobileBriefEvidenceLimit)
	}
	want := []string{"openLoops", "schedules"}
	if !reflect.DeepEqual(brief.Limits.TruncatedSections, want) {
		t.Fatalf("truncatedSections = %v, want %v", brief.Limits.TruncatedSections, want)
	}
	if got := len(brief.Sections.Deliverables); got != mobileBriefItemsPerSection {
		t.Fatalf("deliverables has %d items, want %d that fit the budget", got, mobileBriefItemsPerSection)
	}
	if len(brief.Sections.Schedules) != 0 {
		t.Fatalf("schedules has %d items after the budget was spent", len(brief.Sections.Schedules))
	}
}

func TestBuildMobileBriefSharesRefsForOneSource(t *testing.T) {
	asOf := time.Date(2026, 10, 9, 14, 0, 0, 0, time.UTC)
	shared := mobileBriefCandidate{
		SourceKind: "history", SourceID: "same", Title: "Shared record", At: asOf,
	}
	in := mobileBriefInput{Location: time.UTC, AsOf: asOf}
	in.Moved = []mobileBriefCandidate{shared}
	in.Doing = []mobileBriefCandidate{{
		SourceKind: "history", SourceID: "same", Title: "Shared record again", At: asOf,
	}}
	brief := buildMobileBriefV1(in)
	if len(brief.Evidence) != 1 {
		t.Fatalf("evidence has %d refs, want 1 shared ref", len(brief.Evidence))
	}
	if brief.Sections.Moved[0].Refs[0] != brief.Sections.Doing[0].Refs[0] {
		t.Fatal("items from one source record should cite the same ref")
	}
}

func TestMobileBriefTextIsByteBounded(t *testing.T) {
	cjk := strings.Repeat("界", 200)
	got := mobileBriefText(cjk, mobileBriefTextLimit)
	if len(got) > mobileBriefTextLimit {
		t.Fatalf("bounded text is %d bytes, want <= %d", len(got), mobileBriefTextLimit)
	}
	if !utf8.ValidString(got) {
		t.Fatal("bounded text is not valid UTF-8")
	}
	if !strings.HasSuffix(got, "...") {
		t.Fatalf("bounded text %q should end with an ellipsis", got)
	}

	ascii := strings.Repeat("a", 500)
	if got := mobileBriefText(ascii, mobileBriefTextLimit); len(got) != mobileBriefTextLimit {
		t.Fatalf("ascii bounded length = %d, want %d", len(got), mobileBriefTextLimit)
	}
	if got := mobileBriefText("  hello  ", 320); got != "hello" {
		t.Fatalf("trimmed text = %q", got)
	}
	if got := mobileBriefText("short", 320); got != "short" {
		t.Fatalf("short text changed to %q", got)
	}
}

func TestBuildMobileBriefItemStringsAreBounded(t *testing.T) {
	asOf := time.Date(2026, 10, 9, 14, 0, 0, 0, time.UTC)
	long := strings.Repeat("界", 300)
	in := mobileBriefInput{
		Location: time.UTC,
		AsOf:     asOf,
		Moved: []mobileBriefCandidate{{
			SourceKind: "history", SourceID: "h-long", Title: long, Detail: long, At: asOf,
		}},
	}
	brief := buildMobileBriefV1(in)
	item := brief.Sections.Moved[0]
	if len(item.Title) > mobileBriefTextLimit || len(item.Detail) > mobileBriefTextLimit {
		t.Fatalf("item strings exceed %d bytes: title=%d detail=%d",
			mobileBriefTextLimit, len(item.Title), len(item.Detail))
	}
	if len(brief.Evidence[0].Label) > mobileBriefLabelLimit {
		t.Fatalf("evidence label is %d bytes, want <= %d", len(brief.Evidence[0].Label), mobileBriefLabelLimit)
	}
}

func TestBuildMobileBriefPrivacyFilterDropsProtectedApps(t *testing.T) {
	pii.SetUserSensitiveApps(map[string]string{"com.example.secret": "Secret Notes"})
	t.Cleanup(func() { pii.SetUserSensitiveApps(map[string]string{}) })

	asOf := time.Date(2026, 10, 9, 14, 0, 0, 0, time.UTC)
	in := mobileBriefInput{
		Location: time.UTC,
		AsOf:     asOf,
		Meetings: []mobileBriefCandidate{
			{SourceKind: "meeting", SourceID: "m-secret", Title: "Secret Notes",
				Kind: "meeting", At: asOf, Apps: []string{"Secret Notes"}},
			{SourceKind: "meeting", SourceID: "m-zoom", Title: "Zoom",
				Kind: "meeting", At: asOf, Apps: []string{"Zoom"}},
		},
		People: []mobileBriefCandidate{
			{SourceKind: "principal", SourceID: "p-only-secret", Title: "Dana",
				Kind: "contact", At: asOf, Apps: []string{"Secret Notes"}},
			{SourceKind: "principal", SourceID: "p-mixed", Title: "Lee",
				Kind: "contact", At: asOf, Apps: []string{"Slack", "secret notes"}, AppsShown: true},
		},
		Learned: []mobileBriefCandidate{
			{SourceKind: "memory", SourceID: "mem-protected", Title: "Protected memory",
				Kind: "memory", At: asOf, Protected: true},
			{SourceKind: "memory", SourceID: "mem-mixed", Title: "Open memory",
				Kind: "memory", At: asOf, Apps: []string{"Secret Notes", "Mail"}, AppsShown: true},
		},
		DayShape: &MobileBriefDay{
			ActiveMinutes: 60,
			Hours:         []MobileBriefHour{{Hour: 9, ActiveMinutes: 60}},
			TopApps: []MobileBriefAppTime{
				{Name: "Secret Notes", ActiveMinutes: 50},
				{Name: "Figma", ActiveMinutes: 10},
			},
		},
	}
	brief := buildMobileBriefV1(in)

	if got := brief.Sections.Meetings; len(got) != 1 || got[0].Title != "Zoom" {
		t.Fatalf("meetings = %+v, want only the Zoom meeting", got)
	}
	if got := brief.Sections.People; len(got) != 1 || got[0].Title != "Lee" || got[0].Detail != "Slack" {
		t.Fatalf("people = %+v, want Lee seen only in Slack", got)
	}
	if got := brief.Sections.Learned; len(got) != 1 || got[0].Title != "Open memory" || got[0].Detail != "Mail" {
		t.Fatalf("learned = %+v, want the memory with only the Mail app", got)
	}
	// The day keeps its active time; no app or minute list reaches the brief.
	if brief.DayShape == nil || brief.DayShape.ActiveMinutes != 60 || len(brief.DayShape.TopApps) != 0 {
		t.Fatalf("dayShape = %+v, want active time without a top-app list", brief.DayShape)
	}

	raw, err := json.Marshal(brief)
	if err != nil {
		t.Fatal(err)
	}
	lower := strings.ToLower(string(raw))
	for _, forbidden := range []string{"secret notes", "protected memory"} {
		if strings.Contains(lower, forbidden) {
			t.Fatalf("brief leaks %q", forbidden)
		}
	}
}

func TestBuildMobileBriefDropsRecordsWithoutIdentity(t *testing.T) {
	asOf := time.Date(2026, 10, 9, 14, 0, 0, 0, time.UTC)
	in := mobileBriefInput{
		Location: time.UTC,
		AsOf:     asOf,
		Moved: []mobileBriefCandidate{
			{SourceKind: "history", SourceID: "", Title: "No identity", At: asOf},
			{SourceKind: "history", SourceID: "h-blank", Title: "   ", At: asOf},
			{SourceKind: "history", SourceID: "h-ok", Title: "Kept", At: asOf},
		},
	}
	brief := buildMobileBriefV1(in)
	if len(brief.Sections.Moved) != 1 || brief.Sections.Moved[0].Title != "Kept" {
		t.Fatalf("moved = %+v, want only the record with an identity and a title", brief.Sections.Moved)
	}
}

func TestBuildMobileBriefCountsCoverWhatIsKnown(t *testing.T) {
	asOf := time.Date(2026, 10, 9, 14, 0, 0, 0, time.UTC)
	var loops []mobileBriefCandidate
	for i := 0; i < 10; i++ {
		loops = append(loops, mobileBriefCandidate{
			SourceKind: "purpose", SourceID: "o-" + strconv.Itoa(i),
			Title: "Loop " + strconv.Itoa(i), Kind: "commitment", At: asOf,
		})
	}
	loops = append(loops, mobileBriefCandidate{
		SourceKind: "purpose", SourceID: "o-other", Title: "Untyped", At: asOf,
	})
	in := mobileBriefInput{
		Location:  time.UTC,
		AsOf:      asOf,
		OpenLoops: loops,
		Learned: []mobileBriefCandidate{
			{SourceKind: "learning", SourceID: "l-1", Title: "A pattern", At: asOf},
			{SourceKind: "memory", SourceID: "m-1", Title: "A memory", At: asOf},
		},
	}
	brief := buildMobileBriefV1(in)
	if got := brief.Counts.OpenLoopsByKind; got["commitment"] != 10 || got["other"] != 1 {
		t.Fatalf("openLoopsByKind = %v, want commitment 10 and other 1", got)
	}
	if len(brief.Sections.OpenLoops) != mobileBriefOpenLoopCap {
		t.Fatalf("openLoops shows %d items, want %d", len(brief.Sections.OpenLoops), mobileBriefOpenLoopCap)
	}
	if brief.Counts.LearningItems != 1 {
		t.Fatalf("learningItems = %d, want 1 supported pattern", brief.Counts.LearningItems)
	}
	if brief.Counts.NeedsYou != 0 || brief.NeedsYouCount != 0 || brief.Counts.SelfUpdatesPending != 0 {
		t.Fatalf("the retired Desk counts must stay 0: %+v needsYou=%d", brief.Counts, brief.NeedsYouCount)
	}
}

func TestBuildMobileBriefIDFormats(t *testing.T) {
	asOf := time.Date(2026, 10, 9, 14, 0, 12, 0, time.UTC)
	loc := time.FixedZone("PDT", -7*60*60)
	cases := []struct {
		slot string
		want string
	}{
		{"morning", "2026-10-09-morning"},
		{"evening", "2026-10-09-evening"},
		{"on_request", "2026-10-09-on_request-070012"},
		{"unexpected", "2026-10-09-on_request-070012"},
	}
	for _, tc := range cases {
		brief := buildMobileBriefV1(mobileBriefInput{Slot: tc.slot, AsOf: asOf, Location: loc})
		if brief.BriefID != tc.want {
			t.Errorf("slot %q: briefId = %q, want %q", tc.slot, brief.BriefID, tc.want)
		}
		if !mobileBriefIDPattern.MatchString(brief.BriefID) {
			t.Errorf("slot %q: briefId %q does not match the stored-ID pattern", tc.slot, brief.BriefID)
		}
		if brief.LocalDate != "2026-10-09" || brief.Slot != mobileBriefSlot(tc.slot) {
			t.Errorf("slot %q: localDate=%q slot=%q", tc.slot, brief.LocalDate, brief.Slot)
		}
	}
}

func TestMobileBriefDayShapeSplitsActivityByHour(t *testing.T) {
	loc := time.UTC
	dayStart := time.Date(2026, 10, 9, 0, 0, 0, 0, loc)
	asOf := time.Date(2026, 10, 9, 12, 0, 0, 0, loc)
	ms := func(h, m int) int64 {
		return time.Date(2026, 10, 9, h, m, 0, 0, loc).UnixMilli()
	}
	rows := []ActivitySessionRow{
		{StartTime: ms(9, 30), EndTime: ms(10, 15)},
		{StartTime: ms(11, 50), EndTime: ms(13, 0)},
		{StartTime: time.Date(2026, 10, 8, 23, 50, 0, 0, loc).UnixMilli(), EndTime: ms(0, 10)},
	}
	day := mobileBriefDayShape(rows, loc, dayStart, asOf, (65 * time.Minute).Milliseconds())
	if day == nil {
		t.Fatal("day shape is nil for sessions in the day")
	}
	// 9:30-10:15 gives 30 and 15 minutes, 11:50-12:00 gives 10 minutes after
	// clipping, and 00:00-00:10 gives 10 minutes from the session that began
	// the previous evening.
	if day.ActiveMinutes != 65 {
		t.Fatalf("activeMinutes = %d, want 65", day.ActiveMinutes)
	}
	want := []MobileBriefHour{{Hour: 0, ActiveMinutes: 10}, {Hour: 9, ActiveMinutes: 30}, {Hour: 10, ActiveMinutes: 15}, {Hour: 11, ActiveMinutes: 10}}
	if !reflect.DeepEqual(day.Hours, want) {
		t.Fatalf("hours = %+v, want %+v", day.Hours, want)
	}
	if got := mobileBriefDayShape(nil, loc, dayStart, asOf, 0); got != nil {
		t.Fatalf("day shape without sessions = %+v, want nil", got)
	}
}

// One-time work whose state changed in the window is what Isoform moved;
// dismissed or retired work and work outside the window are left out.
func TestCollectMobileBriefWorkListsOneTimeWorkThatMoved(t *testing.T) {
	store, err := handoff.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	asOf := time.Now()
	inWindow := asOf.Add(-time.Hour).UnixMilli()
	for _, entry := range []handoff.Entry{
		{ID: "hf-done", Title: "Send the Q3 deck", Status: handoff.StatusCompleted, Source: handoff.SourceMobile, UpdatedAt: inWindow},
		{ID: "hf-review", Title: "Summarise the notes", Status: handoff.StatusNeedsReview, Source: handoff.SourceMobile, UpdatedAt: inWindow},
		{ID: "hf-stale", Title: "Old work", Status: handoff.StatusStale, Source: handoff.SourceAgency, UpdatedAt: inWindow},
		{ID: "hf-aborted", Title: "Dismissed", Status: handoff.StatusAborted, Source: handoff.SourceMobile, UpdatedAt: inWindow},
		{ID: "hf-old", Title: "Last week", Status: handoff.StatusCompleted, Source: handoff.SourceMobile, UpdatedAt: asOf.Add(-48 * time.Hour).UnixMilli()},
	} {
		if _, err := store.Save(entry); err != nil {
			t.Fatal(err)
		}
	}
	in := mobileBriefInput{Location: time.UTC, WindowStart: asOf.Add(-24 * time.Hour), AsOf: asOf}
	collectMobileBriefWork(&App{handoffStore: store}, &in)
	got := map[string]mobileBriefCandidate{}
	for _, item := range in.Moved {
		got[item.SourceID] = item
	}
	if len(got) != 2 || got["hf-done"].Detail != "Done." ||
		got["hf-review"].Detail != "Finished, ready for your review." ||
		got["hf-done"].SourceKind != "history" || got["hf-done"].Kind != "one_time_work" {
		t.Fatalf("moved = %+v", in.Moved)
	}
}

// The app's own Daily News job and the jobs behind one-time work are not the
// user's automations, so a fresh Mac that only has them is still in its cold
// start; the user's first automation ends it.
func TestCollectMobileBriefColdStartIgnoresTheAppsOwnJobs(t *testing.T) {
	rig := newHandoffTestRig(t)
	ctx := context.Background()
	for _, job := range []storage.CronJobRow{
		{ID: "daily-news", Name: todayNewsJobName, SurfaceKind: todayNewsSurfaceKind, Enabled: true,
			ScheduleKind: "cron", ScheduleConfig: `{"expr":"0 7 * * *"}`, TargetKind: "agent", TargetConfig: `{"prompt":"news"}`},
		{ID: "handoff-run", Name: "One-time work", SurfaceKind: "handoff", Enabled: true,
			ScheduleKind: "manual", ScheduleConfig: `{}`, TargetKind: "agent", TargetConfig: `{"prompt":"work"}`},
	} {
		if err := rig.state.SaveCronJob(ctx, job); err != nil {
			t.Fatal(err)
		}
	}
	in := mobileBriefInput{Location: time.UTC, WindowStart: time.Now().Add(-24 * time.Hour), AsOf: time.Now()}
	collectMobileBriefUnderstanding(rig.app, &in)
	if !in.ColdStart {
		t.Fatal("a Mac with only its own jobs must still be in its cold start")
	}

	if err := rig.state.SaveCronJob(ctx, storage.CronJobRow{
		ID: "weekly-review", Name: "Weekly review", Enabled: true,
		ScheduleKind: "cron", ScheduleConfig: `{"expr":"0 9 * * 1"}`, TargetKind: "agent", TargetConfig: `{"prompt":"review"}`,
	}); err != nil {
		t.Fatal(err)
	}
	in = mobileBriefInput{Location: time.UTC, WindowStart: time.Now().Add(-24 * time.Hour), AsOf: time.Now()}
	collectMobileBriefUnderstanding(rig.app, &in)
	if in.ColdStart {
		t.Fatal("the user's first automation must end the cold start")
	}
}

func TestCollectMobileBriefInputWithoutStores(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	asOf := time.Now()
	in, err := collectMobileBriefInput(context.Background(), &App{}, "morning", asOf.Add(-24*time.Hour), asOf)
	if err != nil {
		t.Fatalf("collect on an app without stores: %v", err)
	}
	brief := buildMobileBriefV1(in)
	if brief.NarrativeStatus != "skipped" || len(brief.Evidence) != 0 {
		t.Fatalf("empty collection produced narrativeStatus=%q evidence=%d",
			brief.NarrativeStatus, len(brief.Evidence))
	}
	if _, err := collectMobileBriefInput(context.Background(), nil, "morning", asOf, asOf); err == nil {
		t.Fatal("collect with no app should report an error")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := collectMobileBriefInput(canceled, &App{}, "morning", asOf, asOf); err == nil {
		t.Fatal("collect with a canceled context should report an error")
	}
}

// TestCollectMobileBriefSessionsUsesRealStore exercises the activity-backed
// sources against SQLite: protected and sensitive-app sessions must not contribute
// to the day shape, and a user-sensitive app must be dropped from top apps.
func TestCollectMobileBriefSessionsUsesRealStore(t *testing.T) {
	store := newTestStore(t)
	base := time.Date(2026, 10, 9, 9, 0, 0, 0, time.Local)
	startMs := base.UnixMilli()
	insertTestSession(t, store, "normal-1", "pending", startMs)
	insertTestSession(t, store, "meeting-1", "pending", startMs+10*60_000)
	insertTestSession(t, store, "protected-1", "protected", startMs+20*60_000)
	insertTestSession(t, store, "protected-meeting", "protected", startMs+30*60_000)
	insertTestSession(t, store, "ordinary-1", "pending", startMs+40*60_000)
	insertTestSession(t, store, "ordinary-2", "pending", startMs+60*60_000)
	for _, query := range []string{
		`UPDATE activity_sessions SET app_names = '["Editor"]' WHERE id IN ('ordinary-1', 'ordinary-2', 'protected-1')`,
		`UPDATE activity_events SET data = '{"appName":"Editor"}' WHERE session_id IN ('ordinary-1', 'ordinary-2', 'protected-1')`,
		`UPDATE activity_sessions SET is_meeting = 1, meeting_app = 'Zoom' WHERE id = 'meeting-1'`,
		`UPDATE activity_sessions SET is_meeting = 1, meeting_app = 'Teams' WHERE id = 'protected-meeting'`,
	} {
		if _, err := store.db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	app := &App{activity: &activityMonitorState{store: store}}

	pii.SetUserSensitiveApps(map[string]string{"com.example.test": "TestApp"})
	t.Cleanup(func() { pii.SetUserSensitiveApps(map[string]string{}) })

	in := mobileBriefInput{
		Location:    time.Local,
		WindowStart: base.Add(-time.Hour),
		AsOf:        base.Add(3 * time.Hour),
	}
	collectMobileBriefSessions(app, &in)
	brief := buildMobileBriefV1(in)

	if got := brief.Sections.Meetings; len(got) != 0 {
		t.Fatalf("recorded calls must not announce themselves in Today: %+v", got)
	}
	for _, item := range brief.Sections.Meetings {
		if strings.Contains(strings.ToLower(item.Title+item.Detail), "teams") {
			t.Fatal("a protected meeting leaked into the brief")
		}
	}
	if brief.DayShape == nil {
		t.Fatal("day shape is nil although the day has sessions")
	}
	for _, app := range brief.DayShape.TopApps {
		if app.Name == "TestApp" {
			t.Fatal("a user-sensitive app appeared in top apps")
		}
	}
	if brief.DayShape.ActiveMinutes != 2 {
		t.Fatalf("activeMinutes = %d, want only the two ordinary sessions counted", brief.DayShape.ActiveMinutes)
	}
}

func TestMobileBriefHubAndTimezoneNames(t *testing.T) {
	if got := mobileBriefHubName(); strings.HasSuffix(got, ".local") {
		t.Fatalf("hub name %q still has the .local suffix", got)
	}
	if got := mobileBriefTimezone("", time.UTC); got != "UTC" {
		t.Fatalf("timezone for UTC = %q", got)
	}
	if got := mobileBriefTimezone("America/Los_Angeles", time.UTC); got != "America/Los_Angeles" {
		t.Fatalf("explicit timezone = %q", got)
	}
	if got := mobileBriefTimezoneName(time.UTC); got != "UTC" {
		t.Fatalf("timezone name for UTC = %q", got)
	}
}
