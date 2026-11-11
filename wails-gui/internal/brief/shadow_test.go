package brief

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
)

const testBriefID = "2026-10-09-morning"

// fakeDeps is a unit-test double for ShadowDeps. It records every Ask prompt
// and every Snapshot call, so the tests can count model calls and reads.
type fakeDeps struct {
	items    []Item
	closed   bool
	low      bool
	snapErr  error
	askErr   error
	replies  []string
	asks     []string
	snapshot int
}

func (f *fakeDeps) Snapshot(_ context.Context, w Window) (Inputs, error) {
	f.snapshot++
	if f.snapErr != nil {
		return Inputs{}, f.snapErr
	}
	return Inputs{Snapshot: Snapshot{Window: w, Items: f.items}, Language: "en", Print: "print"}, nil
}

func (f *fakeDeps) Ask(_ context.Context, prompt string) (string, error) {
	f.asks = append(f.asks, prompt)
	if f.askErr != nil {
		return "", f.askErr
	}
	if len(f.replies) == 0 {
		return "", errors.New("no reply queued")
	}
	reply := f.replies[0]
	f.replies = f.replies[1:]
	return reply, nil
}

func (f *fakeDeps) Speak(context.Context, string, string) ([]byte, error) {
	return nil, errors.New("speech is not used by the shadow")
}
func (f *fakeDeps) ClosedEyes() bool        { return f.closed }
func (f *fakeDeps) Language() string        { return "en" }
func (f *fakeDeps) Emit(string, any)        {}
func (f *fakeDeps) BudgetLow() bool         { return f.low }
func (f *fakeDeps) queue(replies ...string) { f.replies = append(f.replies, replies...) }

var passJudgment = `{"judgments":[{"question":1,"pass":true},{"question":2,"pass":true},{"question":3,"pass":true},{"question":4,"pass":true}]}`

// partsReply is a well-formed done and next reply: the next part is the larger.
const partsReply = `{"done":[{"text":"Shipped the draft.","refs":["e1"]}],"next":[{"text":"Send the draft to Sam before the review, because Sam is waiting on it.","refs":["e1"]}]}`

var testWindow = Window{
	Slot:      "morning",
	LocalDate: "2026-10-09",
	Timezone:  "UTC",
	Start:     time.Date(2026, 10, 8, 1, 0, 0, 0, time.UTC),
	End:       time.Date(2026, 10, 9, 14, 0, 0, 0, time.UTC),
}

func twoItems() []Item {
	return []Item{
		{Section: SectionMoved, SourceKind: "work", SourceID: "w1", Title: "Ship the draft", At: testWindow.End},
		{Section: SectionMeetings, SourceKind: "calendar", SourceID: "c1", Title: "Design review", At: testWindow.End},
	}
}

// writeOldBrief puts a stand-in for the old brief and its latest pointer under
// dir, so a test can check that a shadow run leaves them byte-for-byte alone.
func writeOldBrief(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		testBriefID + ".json": `{"briefId":"` + testBriefID + `","narrative":[]}`,
		"latest.json":         `{"briefId":"` + testBriefID + `"}`,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// listTree returns every path under root with its content, so a test can
// compare the whole folder before and after a run.
func listTree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		rel, _ := filepath.Rel(root, path)
		out[rel] = string(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestRunShadowWritesOnlyUnderShadowAndUsesOneJudgeCall(t *testing.T) {
	dir := t.TempDir()
	writeOldBrief(t, dir)
	before := listTree(t, dir)

	deps := &fakeDeps{items: twoItems()}
	deps.queue(partsReply, passJudgment)

	report, err := RunShadow(context.Background(), deps, dir, testBriefID, testWindow, ShadowOld{Narrative: 1, Evidence: 2})
	if err != nil {
		t.Fatalf("RunShadow: %v", err)
	}
	if len(deps.asks) != 2 {
		t.Fatalf("model calls: got %d, want 2 (one narrative, one batched judge)", len(deps.asks))
	}
	if report.Judge != JudgePass || report.New.Narrative != 2 || report.New.Evidence != 2 {
		t.Fatalf("report: judge=%s narrative=%d evidence=%d", report.Judge, report.New.Narrative, report.New.Evidence)
	}
	if report.DoneParts != 1 || report.NextParts != 1 || !report.JudgePass {
		t.Fatalf("parts: done=%d next=%d pass=%v, want 1 1 true", report.DoneParts, report.NextParts, report.JudgePass)
	}
	done, next, pass, found := ReadShadowParts(dir, testBriefID)
	if !found || !pass || done != 1 || next != 1 {
		t.Fatalf("stored parts: done=%d next=%d pass=%v found=%v", done, next, pass, found)
	}
	for _, part := range readShadowFileParts(t, dir) {
		if len(part.Paragraphs) == 0 {
			t.Fatalf("part %s has no paragraphs", part.Kind)
		}
		for _, p := range part.Paragraphs {
			if len(p.Refs) == 0 {
				t.Fatalf("part %s paragraph cites no refs", part.Kind)
			}
		}
	}
	after := listTree(t, dir)
	added := []string{}
	for path, body := range after {
		if before[path] != body {
			added = append(added, path)
		}
	}
	if len(added) != 1 || added[0] != filepath.Join("shadow", testBriefID+".json") {
		t.Fatalf("files changed: %v, want only shadow/%s.json", added, testBriefID)
	}
	for path := range before {
		if _, ok := after[path]; !ok {
			t.Fatalf("file removed: %s", path)
		}
	}
}

func TestRunShadowFailureLeavesOldBriefUnchanged(t *testing.T) {
	t.Run("gather fails", func(t *testing.T) {
		dir := t.TempDir()
		writeOldBrief(t, dir)
		before := listTree(t, dir)
		deps := &fakeDeps{snapErr: errors.New("store is busy")}
		if _, err := RunShadow(context.Background(), deps, dir, testBriefID, testWindow, ShadowOld{}); err == nil {
			t.Fatal("RunShadow succeeded, want the gather error")
		}
		if diff := listTree(t, dir); len(diff) != len(before) {
			t.Fatalf("a failed gather wrote files: %d before, %d after", len(before), len(diff))
		}
	})
	t.Run("model fails", func(t *testing.T) {
		dir := t.TempDir()
		writeOldBrief(t, dir)
		before := listTree(t, dir)
		deps := &fakeDeps{items: twoItems(), askErr: errors.New("model unavailable")}
		report, err := RunShadow(context.Background(), deps, dir, testBriefID, testWindow, ShadowOld{})
		if err != nil {
			t.Fatalf("a model failure should be recorded, not returned: %v", err)
		}
		if report.Narrative != NarrativeError || report.New.Narrative != 0 {
			t.Fatalf("narrative: status=%s count=%d, want error and 0", report.Narrative, report.New.Narrative)
		}
		after := listTree(t, dir)
		for path, body := range before {
			if after[path] != body {
				t.Fatalf("old brief file %s changed", path)
			}
		}
	})
}

func TestRunShadowClosedEyesDoesNothing(t *testing.T) {
	dir := t.TempDir()
	writeOldBrief(t, dir)
	before := listTree(t, dir)
	deps := &fakeDeps{items: twoItems(), closed: true}
	_, err := RunShadow(context.Background(), deps, dir, testBriefID, testWindow, ShadowOld{})
	if !errors.Is(err, ErrClosedEyes) {
		t.Fatalf("err = %v, want ErrClosedEyes", err)
	}
	if deps.snapshot != 0 || len(deps.asks) != 0 {
		t.Fatalf("closed eyes read %d snapshots and made %d model calls", deps.snapshot, len(deps.asks))
	}
	if after := listTree(t, dir); len(after) != len(before) {
		t.Fatal("closed eyes wrote a file")
	}
}

func TestRunShadowSkipsWhenBudgetIsLow(t *testing.T) {
	dir := t.TempDir()
	deps := &fakeDeps{items: twoItems(), low: true}
	_, err := RunShadow(context.Background(), deps, dir, testBriefID, testWindow, ShadowOld{})
	if !errors.Is(err, ErrBudgetLow) {
		t.Fatalf("err = %v, want ErrBudgetLow", err)
	}
	if deps.snapshot != 0 || len(deps.asks) != 0 {
		t.Fatalf("low budget read %d snapshots and made %d model calls", deps.snapshot, len(deps.asks))
	}
	if _, statErr := os.Stat(filepath.Join(dir, ShadowDirName)); !os.IsNotExist(statErr) {
		t.Fatal("low budget created the shadow folder")
	}
}

func TestRunShadowRejectsIDsOutsideTheBriefShape(t *testing.T) {
	dir := t.TempDir()
	for _, id := range []string{"../escape", "2026-10-09-morning/../../x", "latest", ""} {
		deps := &fakeDeps{items: twoItems()}
		if _, err := RunShadow(context.Background(), deps, dir, id, testWindow, ShadowOld{}); !errors.Is(err, ErrShadowID) {
			t.Fatalf("id %q: err = %v, want ErrShadowID", id, err)
		}
		if deps.snapshot != 0 || len(deps.asks) != 0 {
			t.Fatalf("id %q reached the sources", id)
		}
	}
}

func TestRunShadowNoEvidenceMakesNoModelCall(t *testing.T) {
	dir := t.TempDir()
	deps := &fakeDeps{}
	report, err := RunShadow(context.Background(), deps, dir, testBriefID, testWindow, ShadowOld{})
	if err != nil {
		t.Fatalf("RunShadow: %v", err)
	}
	if len(deps.asks) != 0 || report.Judge != JudgeSkipped {
		t.Fatalf("empty window: %d model calls, judge=%s; want none and skipped", len(deps.asks), report.Judge)
	}
}

func TestRunShadowJudgeRejectKeepsNoParagraphs(t *testing.T) {
	dir := t.TempDir()
	deps := &fakeDeps{items: twoItems()}
	reject := `{"judgments":[{"question":1,"pass":false,"critique":"weak lead"},{"question":2,"pass":true},{"question":3,"pass":true},{"question":4,"pass":true}]}`
	deps.queue(partsReply, reject)
	report, err := RunShadow(context.Background(), deps, dir, testBriefID, testWindow, ShadowOld{})
	if err != nil {
		t.Fatalf("RunShadow: %v", err)
	}
	if report.Judge != JudgeReject || report.New.Narrative != 0 {
		t.Fatalf("judge=%s narrative=%d, want reject and 0", report.Judge, report.New.Narrative)
	}
	if len(deps.asks) != 2 {
		t.Fatalf("model calls: got %d, want 2", len(deps.asks))
	}
}

func TestLogShadowLineHasCountsOnly(t *testing.T) {
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(prev)

	dir := t.TempDir()
	deps := &fakeDeps{items: twoItems()}
	deps.queue(`{"done":[{"text":"Secret project name here.","refs":["e1"]}],"next":[{"text":"Send the draft before the review tomorrow.","refs":["e1"]}]}`, passJudgment)
	if _, err := RunShadow(context.Background(), deps, dir, testBriefID, testWindow, ShadowOld{Narrative: 1, Evidence: 2, Sections: map[string]int{SectionMoved: 1}}); err != nil {
		t.Fatalf("RunShadow: %v", err)
	}
	line := strings.TrimSpace(buf.String())
	pattern := regexp.MustCompile(`^\S* ?\S* ?\[brief-shadow\] id=2026-10-09-morning old_narrative=1 new_narrative=2 old_cards=0 new_cards=0 old_evidence=2 new_evidence=2 sections_diff=\S+ done_parts=1 next_parts=1 judge_pass=true ms=\d+$`)
	if !pattern.MatchString(line) {
		t.Fatalf("log line does not match the shadow format: %q", line)
	}
	if strings.Contains(line, "Ship") || strings.Contains(line, "Secret") || strings.Contains(line, dir) {
		t.Fatalf("log line carries text or a path: %q", line)
	}
}

func TestReadShadowCountsReadsOnlyCounts(t *testing.T) {
	dir := t.TempDir()
	deps := &fakeDeps{items: twoItems()}
	deps.queue(partsReply, passJudgment)
	if _, err := RunShadow(context.Background(), deps, dir, testBriefID, testWindow, ShadowOld{}); err != nil {
		t.Fatalf("RunShadow: %v", err)
	}
	counts, sections, ok := ReadShadowCounts(dir, testBriefID)
	if !ok || counts.Narrative != 2 || counts.Evidence != 2 {
		t.Fatalf("counts = %+v ok=%v, want narrative 2 evidence 2", counts, ok)
	}
	names := make([]string, 0, len(sections))
	for name := range sections {
		names = append(names, name)
	}
	sort.Strings(names)
	if len(names) != len(ComposedSectionNames()) {
		t.Fatalf("sections: got %v", names)
	}
}

func TestParseNarrativePartsKeepsOnlyCitedEvidence(t *testing.T) {
	evidence := []Evidence{{Ref: "e1"}, {Ref: "e2"}}
	cases := []struct {
		name       string
		raw        string
		wantDone   int
		wantNext   int
		wantStatus string
	}{
		{"valid", `{"done":[{"text":"One.","refs":["e1"]}],"next":[{"text":"Two.","refs":["e2"]}]}`, 1, 1, NarrativeOK},
		{"unknown ref dropped", `{"done":[{"text":"One.","refs":["e9"]}],"next":[]}`, 0, 0, NarrativeEmpty},
		{"mixed refs kept", `{"done":[],"next":[{"text":"One.","refs":["e9","e2"]}]}`, 0, 1, NarrativeOK},
		{"uncited paragraph rejected", `{"done":[],"next":[{"text":"One.","refs":[]},{"text":"Two.","refs":["e1"]}]}`, 0, 1, NarrativeRejected},
		{"done capped at two", `{"done":[{"text":"A.","refs":["e1"]},{"text":"B.","refs":["e1"]},{"text":"C.","refs":["e1"]}],"next":[{"text":"D.","refs":["e2"]}]}`, 2, 1, NarrativeRejected},
		{"fenced reply", "```json\n" + partsReply + "\n```", 1, 1, NarrativeOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			done, next, status, err := ParseNarrativeParts(tc.raw, evidence)
			if err != nil {
				t.Fatalf("ParseNarrativeParts: %v", err)
			}
			if len(done) != tc.wantDone || len(next) != tc.wantNext || status != tc.wantStatus {
				t.Fatalf("got done=%d next=%d status=%s, want done=%d next=%d status=%s",
					len(done), len(next), status, tc.wantDone, tc.wantNext, tc.wantStatus)
			}
		})
	}
	if _, _, _, err := ParseNarrativeParts("not json", evidence); err == nil {
		t.Fatal("non-JSON reply parsed")
	}
}

func TestCheckNarrativePartsRejectsEmptyNextAndPastRestatement(t *testing.T) {
	short := []Paragraph{{Text: "Done."}}
	long := []Paragraph{{Text: "Send the draft to Sam before the review because Sam is waiting."}}
	if err := CheckNarrativeParts(PartNext, short, nil); err == nil {
		t.Fatal("an empty next part passed")
	}
	if err := CheckNarrativeParts(PartNext, short, long); err != nil {
		t.Fatalf("a larger next part was rejected: %v", err)
	}
	if err := CheckNarrativeParts(PartNext, long, long); err == nil {
		t.Fatal("a next part that only restates the done part passed")
	}
	clock := []Paragraph{{Text: "You shipped the draft at 9:30."}}
	if err := CheckNarrativeParts(PartNext, clock, long); err == nil {
		t.Fatal("a clock time in the done part passed")
	}
}

func TestRunShadowRejectsEmptyNextPartWithoutJudgeCall(t *testing.T) {
	dir := t.TempDir()
	deps := &fakeDeps{items: twoItems()}
	deps.queue(`{"done":[{"text":"Shipped the draft.","refs":["e1"]}],"next":[]}`)
	report, err := RunShadow(context.Background(), deps, dir, testBriefID, testWindow, ShadowOld{})
	if err != nil {
		t.Fatalf("RunShadow: %v", err)
	}
	if report.Judge != JudgeReject || report.JudgePass || report.NextParts != 0 {
		t.Fatalf("judge=%s pass=%v next=%d, want reject and no next part", report.Judge, report.JudgePass, report.NextParts)
	}
	if len(deps.asks) != 1 {
		t.Fatalf("model calls: got %d, want 1 (the judge is not called for an empty next part)", len(deps.asks))
	}
}

func TestParseJudgmentNeedsEveryAnswer(t *testing.T) {
	if pass, err := ParseJudgment(passJudgment, JudgeQuestionCount()); err != nil || !pass {
		t.Fatalf("four passes: pass=%v err=%v", pass, err)
	}
	if _, err := ParseJudgment(`{"judgments":[{"question":1,"pass":true}]}`, JudgeQuestionCount()); err == nil {
		t.Fatal("a partial judgment was accepted")
	}
	if _, err := ParseJudgment(`{"judgments":[{"question":1},{"question":2,"pass":true},{"question":3,"pass":true},{"question":4,"pass":true}]}`, JudgeQuestionCount()); err == nil {
		t.Fatal("a judgment without a verdict was accepted")
	}
}
