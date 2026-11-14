package brief

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"
)

// Replay tests for the writer's rules. Each row is made-up material and a
// model reply that keeps or breaks one rule; the row checks what the prompt
// asks and what RepairDraft keeps, repairs or drops. The rules:
//   - balance: the slot's larger part is the larger part;
//   - morning looks forward, evening reflects back;
//   - day words match the dates of the cited items;
//   - no past date in a next step;
//   - every paragraph cites real items;
//   - agent work is never credited to the user;
//   - excluded material never reaches the reply (app side: see
//     mobile_brief_writer_rules_test.go, with messaging paraphrase).

type ruleRow struct {
	name     string
	material func() Material
	reply    Draft
	check    func(t *testing.T, d Draft, codes []string)
}

func ruleDay(t *testing.T, year int, month time.Month, day int) time.Time {
	t.Helper()
	loc, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatal(err)
	}
	return time.Date(year, month, day, 0, 0, 0, 0, loc)
}

// ruleMaterial is a small day: two done items, an agent item, a meeting
// ahead, a commitment, a follow-up and a context note. Ids m1..m7 in order.
func ruleMaterial(slot, larger string, today time.Time, doneWhen string) Material {
	m := Material{Slot: slot, Larger: larger, Today: today, Lang: "en"}
	m.AddItem(MaterialItem{Group: GroupDone, Title: "<own work A>", When: doneWhen})
	m.AddItem(MaterialItem{Group: GroupDone, Title: "<own work B>", When: doneWhen})
	m.AddItem(MaterialItem{Group: GroupAgents, Title: "<agent work>", When: doneWhen, Agent: true})
	m.AddItem(MaterialItem{Group: GroupAhead, Title: "<meeting ahead>", When: "tomorrow 10:00 AM"})
	m.AddItem(MaterialItem{Group: GroupCommitment, Title: "<commitment>", When: "due Monday"})
	m.AddItem(MaterialItem{Group: GroupFollowUp, Title: "<reply owed>", When: doneWhen})
	m.AddItem(MaterialItem{Group: GroupContext, Title: "<context>"})
	return m
}

func para(text string, refs ...string) Paragraph { return Paragraph{Text: text, Refs: refs} }

func allText(d Draft) string {
	var parts []string
	for _, p := range append(append([]Paragraph(nil), d.Done...), d.Next...) {
		parts = append(parts, p.Text)
	}
	return strings.Join(parts, " | ")
}

func wantCode(t *testing.T, codes []string, code string, want bool) {
	t.Helper()
	if slices.Contains(codes, code) != want {
		t.Fatalf("code %s present=%t, want %t (codes %v)", code, !want, want, codes)
	}
}

func runRuleRows(t *testing.T, rows []ruleRow) {
	t.Helper()
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			raw, err := json.Marshal(row.reply)
			if err != nil {
				t.Fatal(err)
			}
			d, codes, err := RepairDraft(string(raw), row.material())
			if err != nil {
				t.Fatalf("repair: %v (codes %v)", err, codes)
			}
			row.check(t, d, codes)
		})
	}
}

// Friday, November 13, 2026 is the evening; Saturday the 14th the morning.
func TestWriterRuleBalance(t *testing.T) {
	morning := func() Material { return ruleMaterial("morning", PartNext, ruleDay(t, 2026, 11, 14), "yesterday") }
	evening := func() Material { return ruleMaterial("evening", PartDone, ruleDay(t, 2026, 11, 13), "today") }
	long := "You closed both pieces of work, kept the pace up through the afternoon, and left the follow-up ready to send."
	short := "Send the reply first."
	runRuleRows(t, []ruleRow{
		{"morning next is larger", morning, Draft{Done: []Paragraph{para(short, "m1")}, Next: []Paragraph{para(long, "m4", "m5")}},
			func(t *testing.T, d Draft, codes []string) { wantCode(t, codes, CodeBalance, false) }},
		{"morning done larger is flagged", morning, Draft{Done: []Paragraph{para(long, "m1", "m2")}, Next: []Paragraph{para(short, "m6")}},
			func(t *testing.T, d Draft, codes []string) { wantCode(t, codes, CodeBalance, true) }},
		{"evening done is larger", evening, Draft{Done: []Paragraph{para(long, "m1", "m2")}, Next: []Paragraph{para(short, "m6")}},
			func(t *testing.T, d Draft, codes []string) { wantCode(t, codes, CodeBalance, false) }},
		{"evening next larger is flagged", evening, Draft{Done: []Paragraph{para(short, "m1")}, Next: []Paragraph{para(long, "m4", "m5")}},
			func(t *testing.T, d Draft, codes []string) { wantCode(t, codes, CodeBalance, true) }},
		{"no next part is flagged", morning, Draft{Done: []Paragraph{para(long, "m1")}},
			func(t *testing.T, d Draft, codes []string) {
				wantCode(t, codes, CodeNoNext, true)
				wantCode(t, codes, CodeBalance, true)
			}},
	})
}

func TestWriterRuleMorningForwardEveningBack(t *testing.T) {
	cases := []struct {
		slot, larger string
		want, not    []string
	}{
		{"morning", PartNext, []string{`"next" (the larger part`, "what needs to happen next", "since the last brief"}, []string{"reflect on the day"}},
		{"evening", PartDone, []string{`"done" (the larger part`, "reflect on the day", "what is for tomorrow"}, []string{"what needs to happen next"}},
	}
	for _, c := range cases {
		t.Run(c.slot, func(t *testing.T) {
			if got := BalanceFor(Window{Slot: c.slot}); got != c.larger {
				t.Fatalf("BalanceFor(%s) = %s, want %s", c.slot, got, c.larger)
			}
			prompt := WritePrompt(ruleMaterial(c.slot, c.larger, ruleDay(t, 2026, 11, 14), "yesterday"), "en")
			for _, w := range c.want {
				if !strings.Contains(prompt, w) {
					t.Fatalf("%s prompt lacks %q", c.slot, w)
				}
			}
			for _, w := range c.not {
				if strings.Contains(prompt, w) {
					t.Fatalf("%s prompt has %q", c.slot, w)
				}
			}
		})
	}
	for _, c := range []struct {
		hour int
		want string
	}{{9, PartNext}, {11, PartNext}, {12, PartDone}, {20, PartDone}} {
		end := time.Date(2026, 11, 14, c.hour, 0, 0, 0, time.UTC)
		if got := BalanceFor(Window{Slot: "on_request", End: end}); got != c.want {
			t.Fatalf("on request at %d = %s, want %s", c.hour, got, c.want)
		}
	}
}

func TestWriterRuleDayWordsMatchCitedDates(t *testing.T) {
	sat := func() Material { return ruleMaterial("morning", PartNext, ruleDay(t, 2026, 11, 14), "yesterday") }
	fri := func() Material { return ruleMaterial("evening", PartDone, ruleDay(t, 2026, 11, 13), "today") }
	tue := func() Material {
		m := ruleMaterial("morning", PartNext, ruleDay(t, 2026, 11, 17), "Sunday")
		m.Items[1].When = "yesterday" // Monday
		return m
	}
	contains := func(want string) func(*testing.T, Draft, []string) {
		return func(t *testing.T, d Draft, codes []string) {
			if !strings.Contains(allText(d), want) {
				t.Fatalf("text %q lacks %q (codes %v)", allText(d), want, codes)
			}
		}
	}
	runRuleRows(t, []ruleRow{
		{"morning: wrong weekday for yesterday is fixed", sat, Draft{Done: []Paragraph{para("Thursday was full.", "m1", "m2")}, Next: []Paragraph{para("Prepare for the meeting.", "m4")}},
			func(t *testing.T, d Draft, codes []string) {
				contains("Friday was full.")(t, d, codes)
				wantCode(t, codes, CodeDayFixed, true)
			}},
		{"evening: wrong weekday for today is fixed", fri, Draft{Done: []Paragraph{para("Wednesdays like this one go fast.", "m1")}, Next: []Paragraph{para("Prepare for the meeting.", "m4")}},
			contains("Fridays like this one go fast.")},
		{"supported weekday is kept", sat, Draft{Done: []Paragraph{para("Friday was full.", "m1")}, Next: []Paragraph{para("The commitment is due Monday.", "m5")}},
			func(t *testing.T, d Draft, codes []string) {
				contains("Friday was full.")(t, d, codes)
				contains("due Monday")(t, d, codes)
				wantCode(t, codes, CodeDayFixed, false)
				wantCode(t, codes, CodeDayUnsupported, false)
			}},
		{"today's weekday is always allowed", sat, Draft{Done: []Paragraph{para("Friday was full.", "m1")}, Next: []Paragraph{para("Use Saturday to send the reply.", "m6")}},
			func(t *testing.T, d Draft, codes []string) { wantCode(t, codes, CodeDayUnsupported, false) }},
		{"two past days: an unsupported weekday is only reported", tue, Draft{Done: []Paragraph{para("Thursday went well.", "m1", "m2")}, Next: []Paragraph{para("Prepare for the meeting.", "m4")}},
			func(t *testing.T, d Draft, codes []string) {
				contains("Thursday went well.")(t, d, codes)
				wantCode(t, codes, CodeDayUnsupported, true)
			}},
		{"next part weekday is reported, never rewritten", sat, Draft{Done: []Paragraph{para("Friday was full.", "m1")}, Next: []Paragraph{para("Send the reply Wednesday.", "m6")}},
			func(t *testing.T, d Draft, codes []string) {
				contains("Wednesday")(t, d, codes)
				wantCode(t, codes, CodeDayUnsupported, true)
			}},
	})
	// Chinese briefs carry no English weekday words; the check stays off.
	zh := sat()
	zh.Lang = "zh"
	raw, _ := json.Marshal(Draft{Done: []Paragraph{para("Thursday", "m1")}, Next: []Paragraph{para("x", "m4")}})
	if _, codes, _ := RepairDraft(string(raw), zh); slices.Contains(codes, CodeDayFixed) {
		t.Fatalf("zh brief was day-checked: %v", codes)
	}
}

func TestWriterRuleNoPastDateInNext(t *testing.T) {
	at := func(year int, month time.Month, day int) func() Material {
		return func() Material { return ruleMaterial("morning", PartNext, ruleDay(t, year, month, day), "yesterday") }
	}
	done := para("Friday was full.", "m1")
	next := func(text string) Draft { return Draft{Done: []Paragraph{done}, Next: []Paragraph{para(text, "m5")}} }
	is := func(want string, code bool) func(*testing.T, Draft, []string) {
		return func(t *testing.T, d Draft, codes []string) {
			if len(d.Next) != 1 || d.Next[0].Text != want {
				t.Fatalf("next = %q, want %q (codes %v)", allText(d), want, codes)
			}
			wantCode(t, codes, CodePastDateNext, code)
		}
	}
	runRuleRows(t, []ruleRow{
		{"past date becomes today", at(2026, 11, 14), next("Do a set November 5."), is("Do a set today.", true)},
		{"on past date becomes today", at(2026, 11, 14), next("Do a set on Nov. 5th."), is("Do a set today.", true)},
		{"reason date is kept", at(2026, 11, 14), next("Do a set today; it has slipped since November 5."), is("Do a set today; it has slipped since November 5.", false)},
		{"today's date is kept", at(2026, 11, 14), next("Do a set November 14."), is("Do a set November 14.", false)},
		{"future date is kept", at(2026, 11, 14), next("Send it by November 20."), is("Send it by November 20.", false)},
		{"next year's date is kept", at(2026, 11, 14), next("Book the trip for January 5."), is("Book the trip for January 5.", false)},
		{"last year's date is past", at(2027, 1, 3), next("Do a set December 30."), is("Do a set today.", true)},
		{"done part keeps its dates", at(2026, 11, 14), Draft{Done: []Paragraph{para("The habit slipped on November 5.", "m1")}, Next: []Paragraph{para("Do a set.", "m5")}},
			func(t *testing.T, d Draft, codes []string) {
				if d.Done[0].Text != "The habit slipped on November 5." {
					t.Fatalf("done = %q", d.Done[0].Text)
				}
				wantCode(t, codes, CodePastDateNext, false)
			}},
	})
}

func TestWriterRuleEveryParagraphCitesRealItems(t *testing.T) {
	m := func() Material { return ruleMaterial("morning", PartNext, ruleDay(t, 2026, 11, 14), "yesterday") }
	runRuleRows(t, []ruleRow{
		{"unknown refs are dropped, the rest kept", m, Draft{Done: []Paragraph{para("Friday was full.", "m1", "m99", "m1")}, Next: []Paragraph{para("Prepare.", "m4")}},
			func(t *testing.T, d Draft, codes []string) {
				if !slices.Equal(d.Done[0].Refs, []string{"m1"}) {
					t.Fatalf("refs %v", d.Done[0].Refs)
				}
				wantCode(t, codes, CodeRefUnknown, true)
			}},
		{"a paragraph with only unknown refs is dropped", m, Draft{Done: []Paragraph{para("Something invented.", "m42"), para("Friday was full.", "m1")}, Next: []Paragraph{para("Prepare.", "m4")}},
			func(t *testing.T, d Draft, codes []string) {
				if len(d.Done) != 1 || d.Done[0].Text != "Friday was full." {
					t.Fatalf("done %q", allText(d))
				}
				wantCode(t, codes, CodeNoRefs, true)
			}},
		{"a paragraph with no refs is dropped", m, Draft{Done: []Paragraph{para("Friday was full.", "m1")}, Next: []Paragraph{para("A thought with no ground."), para("Prepare.", "m4")}},
			func(t *testing.T, d Draft, codes []string) {
				if len(d.Next) != 1 || d.Next[0].Text != "Prepare." {
					t.Fatalf("next %q", allText(d))
				}
			}},
		{"context only is never a citation", m, Draft{Done: []Paragraph{para("Friday was full.", "m1")}, Next: []Paragraph{para("Lean on the note.", "m7"), para("Prepare.", "m4", "m7")}},
			func(t *testing.T, d Draft, codes []string) {
				if len(d.Next) != 1 || !slices.Equal(d.Next[0].Refs, []string{"m4"}) {
					t.Fatalf("next %+v", d.Next)
				}
				wantCode(t, codes, CodeContextRef, true)
			}},
	})
	raw, _ := json.Marshal(Draft{Done: []Paragraph{para("Invented.", "m50")}, Next: []Paragraph{para("Invented.", "m51")}})
	if _, codes, err := RepairDraft(string(raw), m()); err == nil {
		t.Fatalf("a reply citing nothing real was kept (codes %v)", codes)
	}
}

func TestWriterRuleAgentWorkIsNotTheUsers(t *testing.T) {
	m := func() Material {
		m := ruleMaterial("morning", PartNext, ruleDay(t, 2026, 11, 14), "yesterday")
		m.AddItem(MaterialItem{Group: GroupOpenLoop, Title: "<agent loop>", Agent: true}) // m8
		return m
	}
	runRuleRows(t, []ruleRow{
		{"agent-only next step is dropped", m, Draft{Done: []Paragraph{para("Friday was full.", "m1")}, Next: []Paragraph{para("Review the agent work.", "m3"), para("Prepare.", "m4")}},
			func(t *testing.T, d Draft, codes []string) {
				if len(d.Next) != 1 || d.Next[0].Text != "Prepare." {
					t.Fatalf("next %q", allText(d))
				}
				wantCode(t, codes, CodeNextAgentOnly, true)
			}},
		{"an agent-marked forward item is not ground for a next step", m, Draft{Done: []Paragraph{para("Friday was full.", "m1")}, Next: []Paragraph{para("Close the loop.", "m8"), para("Prepare.", "m4")}},
			func(t *testing.T, d Draft, codes []string) { wantCode(t, codes, CodeNextAgentOnly, true) }},
		{"agent work named as the agents' is kept", m, Draft{Done: []Paragraph{para("Your agents merged two fixes.", "m3"), para("Friday was full.", "m1")}, Next: []Paragraph{para("Prepare.", "m4")}},
			func(t *testing.T, d Draft, codes []string) {
				if len(d.Done) != 2 {
					t.Fatalf("done %q", allText(d))
				}
				wantCode(t, codes, CodeAgentCredited, false)
			}},
		{"agent work credited to the user is dropped", m, Draft{Done: []Paragraph{para("You merged two fixes.", "m3"), para("Friday was full.", "m1")}, Next: []Paragraph{para("Prepare.", "m4")}},
			func(t *testing.T, d Draft, codes []string) {
				if len(d.Done) != 1 || d.Done[0].Text != "Friday was full." {
					t.Fatalf("done %q", allText(d))
				}
				wantCode(t, codes, CodeAgentCredited, true)
			}},
		{"a mixed paragraph stays: it rests on the user's work too", m, Draft{Done: []Paragraph{para("You finished both pieces, and your agents merged two fixes.", "m1", "m3")}, Next: []Paragraph{para("Prepare.", "m4")}},
			func(t *testing.T, d Draft, codes []string) { wantCode(t, codes, CodeAgentCredited, false) }},
	})
	prompt := WritePrompt(m(), "en")
	for _, want := range []string{"Never present them as the user's own work", "never make them a next step"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt lacks %q", want)
		}
	}
}
