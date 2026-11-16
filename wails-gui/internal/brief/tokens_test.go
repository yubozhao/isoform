package brief

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

type tokenGolden struct {
	Name  string   `json:"name"`
	Text  string   `json:"text"`
	Refs  []string `json:"refs"`
	Timed []string `json:"timed"`
	Want  []string `json:"want"`
}

// The validator's golden set (testdata/golden/tokens.json) is synthetic: no
// person's data. Briefs made from real data are checked on the test machine
// only and never stored in the repository.
func TestValidateTokensGolden(t *testing.T) {
	raw, err := os.ReadFile("testdata/golden/tokens.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []tokenGolden
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) < 10 {
		t.Fatalf("golden set too small: %d", len(cases))
	}
	for _, tc := range cases {
		timed := map[string]bool{}
		for _, ref := range tc.Timed {
			timed[ref] = true
		}
		got := ValidateTokens(Paragraph{Text: tc.Text, Refs: tc.Refs}, timed)
		if len(got) == 0 {
			got = nil
		}
		if !reflect.DeepEqual(got, tc.Want) {
			t.Errorf("%s: codes %v, want %v", tc.Name, got, tc.Want)
		}
	}
}

func TestReplaceAndStripTokens(t *testing.T) {
	text := "The interview is {when:e3}, and the prep call {when:e9}."
	got := ReplaceTokens(text, func(ref string) string {
		if ref == "e3" {
			return "tomorrow at 3:00 PM"
		}
		return ""
	})
	if want := "The interview is tomorrow at 3:00 PM, and the prep call ."; got != want {
		t.Fatalf("ReplaceTokens = %q, want %q", got, want)
	}
	if got := StripTokens(text); got != "The interview is , and the prep call ." {
		t.Fatalf("StripTokens = %q", got)
	}
	if got := TokenRefs(text); !reflect.DeepEqual(got, []string{"e3", "e9"}) {
		t.Fatalf("TokenRefs = %v", got)
	}
}

// Tokens mode: the writer names when items happen with {when:id}. A valid
// paragraph keeps its template and reads, for any older reader, with the
// item's own when words; a token's item is cited even if refs left it out. A
// paragraph that writes a day word itself falls back to the old checks.
func TestRepairDraftKeepsTokenTemplates(t *testing.T) {
	m := Material{Slot: "morning", Larger: PartNext, Tokens: true, Lang: "en", Items: []MaterialItem{
		{ID: "m1", Group: GroupDone, Title: "Launch memo", When: "yesterday"},
		{ID: "m2", Group: GroupAhead, Title: "Design review", When: "tomorrow 3:00 PM"},
		{ID: "m3", Group: GroupCommitment, Title: "Send the deck"},
	}}
	raw := `{"done":[{"text":"You finished the launch memo {when:m1}.","refs":["m1"]}],` +
		`"next":[{"text":"The design review is {when:m2}, so send the deck before it.","refs":["m3"]},` +
		`{"text":"Send the deck today.","refs":["m3"]}]}`
	draft, codes, err := RepairDraft(raw, m)
	if err != nil {
		t.Fatal(err)
	}
	if got := draft.Done[0]; got.Template != "You finished the launch memo {when:m1}." || got.Text != "You finished the launch memo yesterday." {
		t.Fatalf("done paragraph: %+v", got)
	}
	next := draft.Next[0]
	if next.Template == "" || next.Text != "The design review is tomorrow 3:00 PM, so send the deck before it." {
		t.Fatalf("next paragraph: %+v", next)
	}
	if !reflect.DeepEqual(next.Refs, []string{"m3", "m2"}) {
		t.Fatalf("the token's item is not cited: %v", next.Refs)
	}
	if draft.Next[1].Template != "" {
		t.Fatalf("a paragraph with a free day word kept a template: %+v", draft.Next[1])
	}
	kept, fellBack := 0, 0
	for _, c := range codes {
		switch c {
		case CodeTokenKept:
			kept++
		case CodeTokenFallback:
			fellBack++
		}
	}
	if kept != 2 {
		t.Fatalf("codes %v: want two kept templates", codes)
	}
	_ = fellBack
}

func TestWritePromptAsksForTokensOnlyInTokensMode(t *testing.T) {
	m := Material{Slot: "morning", Larger: PartNext, Lang: "en", Today: time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC)}
	if strings.Contains(WritePrompt(m, "en"), "{when:") {
		t.Fatal("the old prompt asks for tokens")
	}
	m.Tokens = true
	prompt := WritePrompt(m, "en")
	if !strings.Contains(prompt, "{when:ID}") || strings.Contains(prompt, "Today is ") {
		t.Fatal("the tokens prompt does not ask for tokens, or still names the days")
	}
}

// The side-by-side line holds counts only: no text of either draft.
func TestTokensShadowLineIsCountsOnly(t *testing.T) {
	tokens := Draft{Done: []Paragraph{{Text: "Secret words {when:m1}.", Refs: []string{"m1"}}}, Next: []Paragraph{{Text: "More secret words.", Refs: []string{"m2", "m3"}}}}
	old := Draft{Done: []Paragraph{{Text: "Old secret words yesterday.", Refs: []string{"m1"}}}, Next: []Paragraph{{Text: "Old next.", Refs: []string{"m2", "m4"}}}}
	line := TokensShadowLine(tokens, old, []string{CodeTokenKept, CodeTokenFallback}, []string{CodeDayFixed})
	want := "done=1/1 next=1/1 items_shared=2 items_only_tokens=1 items_only_old=1 templates=1 fallbacks=1 old_day_repairs=1"
	if line != want {
		t.Fatalf("line = %q, want %q", line, want)
	}
	if strings.Contains(line, "secret") || strings.Contains(line, "Secret") {
		t.Fatal("the line carries draft text")
	}
}

// A moment marked on the watch is dated by its token like any other item: the
// paragraph keeps its template, so each reader gets their own day word.
func TestRepairDraftKeepsTheTemplateOfAMarkedMoment(t *testing.T) {
	m := Material{Slot: "evening", Larger: PartDone, Tokens: true, Lang: "en", Items: []MaterialItem{
		{ID: "m1", Group: GroupDone, Title: "A moment you marked on your watch", When: "today", Marked: true,
			Detail: "what you were saying then, to put in your own words and never quote: the pricing number is the one that matters"},
		{ID: "m2", Group: GroupDone, Title: "Launch memo", When: "today"},
	}}
	raw := `{"done":[{"text":"You marked a moment on your watch {when:m1}, about the pricing number.","refs":["m1"]},` +
		`{"text":"You also finished the launch memo.","refs":["m2"]}],"next":[]}`
	draft, _, err := RepairDraft(raw, m)
	if err != nil {
		t.Fatal(err)
	}
	got := draft.Done[0]
	if got.Template != "You marked a moment on your watch {when:m1}, about the pricing number." ||
		got.Text != "You marked a moment on your watch today, about the pricing number." {
		t.Fatalf("marked paragraph: %+v", got)
	}
	prompt := WritePrompt(m, "en")
	if !strings.Contains(prompt, `"marked":true`) || !strings.Contains(prompt, "Open the brief with them") || !strings.Contains(prompt, "Never quote the detail") {
		t.Fatal("the prompt must carry the marked flag and say to open with it without quoting")
	}
	plain := WritePrompt(Material{Slot: "evening", Larger: PartDone, Items: m.Items[1:]}, "en")
	if strings.Contains(plain, `"marked":true`) {
		t.Fatal("an unmarked item carried the flag")
	}
}
