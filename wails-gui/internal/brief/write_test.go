package brief

import (
	"strings"
	"testing"
)

func testMaterial() Material {
	m := Material{Slot: "morning", Larger: PartNext}
	m.AddItem(MaterialItem{Group: GroupDone, Title: "Interview with Anthropic", When: "today"})
	m.AddItem(MaterialItem{Group: GroupAgents, Title: "Codex merged two brief fixes", Agent: true})
	m.AddItem(MaterialItem{Group: GroupAhead, Title: "Coffee chat with Vaishant", When: "today 1:00 PM"})
	m.AddItem(MaterialItem{Group: GroupFollowUp, Title: "Send a thank-you note"})
	return m
}

func TestMaterialDedupesTitlesAndCapsGroups(t *testing.T) {
	m := testMaterial()
	if id := m.AddItem(MaterialItem{Group: GroupDone, Title: "interview with anthropic"}); id != "" {
		t.Fatalf("duplicate title was added as %s", id)
	}
	for i := 0; i < 40; i++ {
		m.AddItem(MaterialItem{Group: GroupContext, Title: "note " + itoa(i)})
	}
	n := 0
	for _, item := range m.Items {
		if item.Group == GroupContext {
			n++
		}
	}
	if n != groupLimits[GroupContext] {
		t.Fatalf("context items = %d, want %d", n, groupLimits[GroupContext])
	}
}

func TestWritePromptCarriesAllGroupsAndAgentRule(t *testing.T) {
	prompt := WritePrompt(testMaterial(), "en")
	for _, want := range []string{"Interview with Anthropic", "Coffee chat with Vaishant", `"agent":true`, "never make them a next step", "Never say nothing moved"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt is missing %q", want)
		}
	}
}

func TestRepairDraftKeepsGoodDraft(t *testing.T) {
	raw := "```json\n" + `{"done":[{"text":"You finished the Anthropic interview, and your agents merged two brief fixes.","refs":["m1","m2"]}],
"next":[{"text":"Coffee with Vaishant comes first; bring the question about the call.","refs":["m3"]},{"text":"Then send the thank-you note while the interview is fresh, so it lands today.","refs":["m4","m1"]}]}` + "\n```"
	d, codes, err := RepairDraft(raw, testMaterial())
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Done) != 1 || len(d.Next) != 2 {
		t.Fatalf("parts = %d/%d, codes %v", len(d.Done), len(d.Next), codes)
	}
	if len(codes) != 0 {
		t.Fatalf("unexpected codes %v", codes)
	}
}

func TestRepairDraftRepairsInsteadOfRejecting(t *testing.T) {
	raw := `Here it is: {"done":[{"text":"- You finished the interview at 9:30 am, then reviewed notes.","refs":["m1","m99"]},{"text":"No refs here.","refs":[]}],
"next":[{"text":"Watch the agents finish the merge.","refs":["m2"]},{"text":"Coffee with Vaishant is next.","refs":["m3"]}]}`
	d, codes, err := RepairDraft(raw, testMaterial())
	if err != nil {
		t.Fatal(err)
	}
	if got := d.Done[0].Text; strings.Contains(got, "9:30") || strings.HasPrefix(got, "-") || got != "You finished the interview, then reviewed notes." {
		t.Fatalf("done text not repaired: %q", got)
	}
	if len(d.Done[0].Refs) != 1 || d.Done[0].Refs[0] != "m1" {
		t.Fatalf("unknown ref kept: %v", d.Done[0].Refs)
	}
	if len(d.Next) != 1 || d.Next[0].Refs[0] != "m3" {
		t.Fatalf("agent-only next step kept: %+v", d.Next)
	}
	summary := SummarizeCodes(codes)
	for _, want := range []string{CodeListMarker, CodeClockRemoved, CodeRefUnknown, CodeNoRefs, CodeNextAgentOnly} {
		if !strings.Contains(summary, want+"=") {
			t.Fatalf("codes %s missing %s", summary, want)
		}
	}
}

func TestRepairDraftFailsOnlyWhenNothingIsLeft(t *testing.T) {
	if _, codes, err := RepairDraft("not json", testMaterial()); err == nil || codes[0] != CodeNotJSON {
		t.Fatalf("err=%v codes=%v", err, codes)
	}
	if _, codes, err := RepairDraft(`{"done":[{"text":"x","refs":["m42"]}],"next":[]}`, testMaterial()); err == nil || codes[len(codes)-1] != CodeEmpty {
		t.Fatalf("err=%v codes=%v", err, codes)
	}
}

func TestRepairDraftTrimsLongParagraphToSentences(t *testing.T) {
	long := strings.Repeat("You moved the release forward. ", 30)
	raw := `{"done":[{"text":"` + long + `","refs":["m1"]}],"next":[{"text":"Coffee first.","refs":["m3"]}]}`
	d, codes, err := RepairDraft(raw, testMaterial())
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Done[0].Text) > writeParagraphMax || !strings.HasSuffix(d.Done[0].Text, ".") {
		t.Fatalf("not trimmed to a sentence: %d bytes", len(d.Done[0].Text))
	}
	if !strings.Contains(SummarizeCodes(codes), CodeTrimmed) {
		t.Fatalf("codes %v", codes)
	}
}

func TestAgentDriven(t *testing.T) {
	cases := []struct {
		apps []string
		text string
		want bool
	}{
		{[]string{"Claude"}, "Build the Scrollback Phase 0 spike in Claude Code", true},
		{[]string{"Claude"}, "Plan a silent-speech wearable", false},
		{[]string{"Google Chrome"}, "Review PR #821 in example/app", false},
		{nil, "You have the assistant check hourly for open PRs", true},
		{nil, "AFO calf raises, every day", false},
		{[]string{"Claude", "iTerm2"}, "Refine the isoform hub morning brief", true},
	}
	for _, c := range cases {
		if got := AgentDriven(c.apps, c.text); got != c.want {
			t.Errorf("AgentDriven(%v, %q) = %v, want %v", c.apps, c.text, got, c.want)
		}
	}
}

func TestRepairDraftNeverCitesContext(t *testing.T) {
	m := testMaterial()
	ctx := m.AddItem(MaterialItem{Group: GroupContext, Title: "You prefer short notes"})
	raw := `{"done":[{"text":"You finished the interview.","refs":["m1","` + ctx + `"]},{"text":"You like short notes.","refs":["` + ctx + `"]}],"next":[{"text":"Coffee first.","refs":["m3"]}]}`
	d, codes, err := RepairDraft(raw, m)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Done) != 1 || len(d.Done[0].Refs) != 1 || d.Done[0].Refs[0] != "m1" {
		t.Fatalf("context cited: %+v", d.Done)
	}
	if !strings.Contains(SummarizeCodes(codes), CodeContextRef+"=2") {
		t.Fatalf("codes %v", codes)
	}
}

func TestRepairDraftReadsFirstObjectAmidText(t *testing.T) {
	raw := `Sure. {"done":[{"text":"You finished the interview.","refs":["m1"]}],"next":[{"text":"Coffee first.","refs":["m3"]}]}
Here is a second take: {"done":[]}`
	if _, codes, err := RepairDraft(raw, testMaterial()); err != nil {
		t.Fatalf("a reply with words around its JSON was unusable: %v %v", err, codes)
	}
}
