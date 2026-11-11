package brief

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// readShadowFileParts returns the narrative parts stored in the shadow copy.
// It is a test helper: the production readers return counts only.
func readShadowFileParts(t *testing.T, dir string) []NarrativePart {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, ShadowDirName, testBriefID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Brief struct {
			NarrativeParts []NarrativePart `json:"narrativeParts"`
		} `json:"brief"`
	}
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatal(err)
	}
	return file.Brief.NarrativeParts
}

// TestGoldenFixtureComposesBothPartsWithNextLarger runs the golden fixture
// through the shadow path with a well-formed reply and checks the parts: both
// present, next the larger, and every paragraph citing refs in the evidence.
func TestGoldenFixtureComposesBothPartsWithNextLarger(t *testing.T) {
	dir := t.TempDir()
	deps := &fakeDeps{items: twoItems()}
	deps.queue(partsReply, passJudgment)
	report, err := RunShadow(context.Background(), deps, dir, testBriefID, testWindow, ShadowOld{})
	if err != nil {
		t.Fatalf("RunShadow: %v", err)
	}
	if report.DoneParts == 0 || report.NextParts == 0 {
		t.Fatalf("parts: done=%d next=%d, want both", report.DoneParts, report.NextParts)
	}
	// twoItems gives two evidence rows, refs e1 and e2.
	evidence := map[string]bool{"e1": true, "e2": true}
	parts := readShadowFileParts(t, dir)
	words := map[string]int{}
	for _, part := range parts {
		for _, p := range part.Paragraphs {
			words[part.Kind] += len(strings.Fields(p.Text))
			for _, r := range p.Refs {
				if !evidence[r] {
					t.Fatalf("part %s cites ref %s that is not in the evidence", part.Kind, r)
				}
			}
			if len(p.Refs) == 0 {
				t.Fatalf("part %s paragraph cites no refs", part.Kind)
			}
		}
	}
	if words[PartNext] <= words[PartDone] {
		t.Fatalf("next part (%d words) is not larger than done (%d words)", words[PartNext], words[PartDone])
	}
}

// TestJudgeRejectsEmptyNextAndPastOnlyDraft covers the deterministic judge: an
// empty next part and a next part that only restates the past both fail, and
// neither reaches the batched model judge.
func TestJudgeRejectsEmptyNextAndPastOnlyDraft(t *testing.T) {
	cases := map[string]string{
		"empty next": `{"done":[{"text":"Shipped the draft.","refs":["e1"]}],"next":[]}`,
		"next only restates the past": `{"done":[{"text":"Shipped the draft and reviewed the plan.","refs":["e1"]}],` +
			`"next":[{"text":"Shipped the draft.","refs":["e1"]}]}`,
	}
	for name, reply := range cases {
		t.Run(name, func(t *testing.T) {
			deps := &fakeDeps{items: twoItems()}
			deps.queue(reply)
			report, err := RunShadow(context.Background(), deps, t.TempDir(), testBriefID, testWindow, ShadowOld{})
			if err != nil {
				t.Fatalf("RunShadow: %v", err)
			}
			if report.Judge != JudgeReject || report.JudgePass {
				t.Fatalf("judge=%s pass=%v, want reject", report.Judge, report.JudgePass)
			}
			if len(deps.asks) != 1 {
				t.Fatalf("model calls: got %d, want 1", len(deps.asks))
			}
		})
	}
}

// TestModelJudgeRejectKeepsNoParts keeps the privacy rule for a rejected draft:
// the words are not kept on the composed brief, so nothing reaches a reader.
func TestModelJudgeRejectKeepsNoParts(t *testing.T) {
	deps := &fakeDeps{items: twoItems()}
	reject := `{"judgments":[{"question":2,"pass":false,"critique":"past only"},{"question":1,"pass":true},{"question":3,"pass":true},{"question":4,"pass":true}]}`
	deps.queue(partsReply, reject)
	report, err := RunShadow(context.Background(), deps, t.TempDir(), testBriefID, testWindow, ShadowOld{})
	if err != nil {
		t.Fatalf("RunShadow: %v", err)
	}
	if report.JudgePass || report.DoneParts != 0 || report.NextParts != 0 || report.New.Narrative != 0 {
		t.Fatalf("a rejected draft kept parts: pass=%v done=%d next=%d", report.JudgePass, report.DoneParts, report.NextParts)
	}
}

