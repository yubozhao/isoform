package brief

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// goldenBrief is the part of a golden fixture (testdata, written by the app's
// builder) that the parity test reads: its sections and its evidence.
type goldenBrief struct {
	Sections map[string][]struct {
		Title  string   `json:"title"`
		Detail string   `json:"detail"`
		Status string   `json:"status"`
		Kind   string   `json:"kind"`
		At     string   `json:"at"`
		Refs   []string `json:"refs"`
	} `json:"sections"`
	Evidence []Evidence `json:"evidence"`
}

func readGolden(t *testing.T, name string) goldenBrief {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", name))
	if err != nil {
		t.Fatalf("read golden fixture: %v", err)
	}
	var g goldenBrief
	if err := json.Unmarshal(data, &g); err != nil {
		t.Fatalf("decode golden fixture: %v", err)
	}
	return g
}

// TestComposeMatchesGoldenDeterministicSections feeds the items of each golden
// fixture back in as a snapshot and checks that compose returns the same
// titles, the same item counts per section and the same evidence count for the
// sections this step composes. Chief sections are out of scope and not checked.
func TestComposeMatchesGoldenDeterministicSections(t *testing.T) {
	for _, name := range []string{"mobile_brief_golden_v1.json", "mobile_brief_golden_early_v1.json"} {
		t.Run(name, func(t *testing.T) {
			g := readGolden(t, name)
			byRef := map[string]Evidence{}
			for _, e := range g.Evidence {
				byRef[e.Ref] = e
			}
			var items []Item
			wantEvidence := map[string]bool{}
			want := map[string][]string{}
			for _, section := range ComposedSectionNames() {
				for _, item := range g.Sections[section] {
					if len(item.Refs) == 0 {
						t.Fatalf("golden %s item %q has no ref", section, item.Title)
					}
					src := byRef[item.Refs[0]]
					at, _ := time.Parse(time.RFC3339, item.At)
					items = append(items, Item{
						Section: section, SourceKind: src.SourceKind, SourceID: src.SourceID,
						Title: item.Title, Detail: item.Detail, Status: item.Status, Kind: item.Kind, At: at,
					})
					want[section] = append(want[section], item.Title)
					wantEvidence[src.SourceKind+"\x00"+src.SourceID] = true
				}
			}
			c := Compose(Inputs{Snapshot: Snapshot{Items: items}})
			for _, section := range ComposedSectionNames() {
				got := c.Sections[section]
				if len(got) != len(want[section]) {
					t.Fatalf("section %s: got %d items, golden has %d", section, len(got), len(want[section]))
				}
				for i, item := range got {
					if item.Title != want[section][i] {
						t.Fatalf("section %s item %d: got title %q, golden %q", section, i, item.Title, want[section][i])
					}
				}
			}
			if len(c.Evidence) != len(wantEvidence) {
				t.Fatalf("evidence: got %d refs, golden shows %d distinct sources", len(c.Evidence), len(wantEvidence))
			}
		})
	}
}

func TestComposeOpenLoopsDropsSourcesMovedAlreadyShows(t *testing.T) {
	items := []Item{
		{Section: SectionMoved, SourceKind: "work", SourceID: "w1", Title: "Ship the draft"},
		{Section: SectionOpenLoops, SourceKind: "work", SourceID: "w1", Title: "Ship the draft"},
		{Section: SectionOpenLoops, SourceKind: "work", SourceID: "w2", Title: "Reply to Sam"},
	}
	c := Compose(Inputs{Snapshot: Snapshot{Items: items}})
	if got := len(c.Sections[SectionOpenLoops]); got != 1 {
		t.Fatalf("openLoops: got %d items, want 1 (the moved source is dropped)", got)
	}
	if c.Sections[SectionOpenLoops][0].Title != "Reply to Sam" {
		t.Fatalf("openLoops kept the wrong item: %q", c.Sections[SectionOpenLoops][0].Title)
	}
	if len(c.Evidence) != 2 {
		t.Fatalf("evidence: got %d, want 2 distinct sources", len(c.Evidence))
	}
}

func TestComposeEvidenceBudgetTruncatesSection(t *testing.T) {
	// Eight sections of eight distinct sources are 64 refs, so the last section
	// to spend the budget (deliverables, in composedOrder) is cut short.
	var items []Item
	for _, section := range []string{SectionMoved, SectionDoing, SectionWatching, SectionMeetings, SectionPeople, SectionOpenLoops, SectionLearned, SectionDeliverables} {
		for i := 0; i < ItemsPerSection; i++ {
			items = append(items, Item{Section: section, SourceKind: "activity", SourceID: fmt.Sprintf("%s-%d", section, i), Title: "work"})
		}
	}
	c := Compose(Inputs{Snapshot: Snapshot{Items: items}})
	if len(c.Evidence) != EvidenceLimit {
		t.Fatalf("evidence: got %d, want %d", len(c.Evidence), EvidenceLimit)
	}
	if got := len(c.Sections[SectionDeliverables]); got != EvidenceLimit-7*ItemsPerSection {
		t.Fatalf("deliverables: got %d items, want the %d left in the budget", got, EvidenceLimit-7*ItemsPerSection)
	}
	if len(c.Truncated) != 1 || c.Truncated[0] != SectionDeliverables {
		t.Fatalf("truncated: got %v, want [deliverables]", c.Truncated)
	}
}

func TestComposeClipsTextAtRuneBoundary(t *testing.T) {
	long := ""
	for i := 0; i < TextLimit; i++ {
		long += string(rune(0x4e2d))
	}
	c := Compose(Inputs{Snapshot: Snapshot{Items: []Item{{Section: SectionMoved, SourceKind: "work", SourceID: "w", Title: long}}}})
	title := c.Sections[SectionMoved][0].Title
	if len(title) > TextLimit {
		t.Fatalf("title is %d bytes, want at most %d", len(title), TextLimit)
	}
	if !json.Valid([]byte(`"` + title + `"`)) {
		t.Fatalf("title is not valid UTF-8 after clipping")
	}
}
