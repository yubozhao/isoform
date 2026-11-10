package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"strings"
	"testing"
)

// writtenBriefFixture is the picks fixture with its buttons, as the brief
// service holds it when the model writes.
func writtenBriefFixture() (MobileBriefV1, []mobileBriefPick) {
	brief := briefPicksFixture()
	// The calendar lead in these legacy reply fixtures is now explicitly tied
	// to an observed attendee; it is no longer a calendar-only item.
	brief.Evidence = append(brief.Evidence, MobileBriefRef{Ref: "observed-andy", SourceKind: "principal", SourceID: "andy", Label: "Andy Reed"})
	for i := range brief.Sections.Meetings {
		if len(brief.Sections.Meetings[i].Refs) > 0 && brief.Sections.Meetings[i].Refs[0] == "e23" {
			brief.Sections.Meetings[i].Refs = append(brief.Sections.Meetings[i].Refs, "observed-andy")
		}
	}
	_, brief.Offers = mobileBriefClosing(brief, "English")
	for i := range brief.Offers {
		brief.Offers[i].ID = "offer-1"
		if len(brief.Offers[i].Refs) > 0 && brief.Offers[i].Refs[0] == "e7" {
			brief.Offers[i].ID = "offer-2"
		}
	}
	return brief, mobileBriefPicks(brief)
}

func writtenOfferID(brief MobileBriefV1, ref string) string {
	for _, offer := range brief.Offers {
		if len(offer.Refs) > 0 && offer.Refs[0] == ref {
			return offer.ID
		}
	}
	return ""
}

// writtenReply is a model reply: the paragraphs and the model's own closing.
func writtenReply(t *testing.T, closing *mobileBriefNarrativeReplyClosing, paragraphs ...mobileBriefNarrativeReplyParagraph) string {
	t.Helper()
	raw, err := json.Marshal(struct {
		Paragraphs []mobileBriefNarrativeReplyParagraph `json:"paragraphs"`
		Closing    *mobileBriefNarrativeReplyClosing    `json:"closing,omitempty"`
		Cards      []mobileBriefCardWords               `json:"cards"`
	}{paragraphs, closing, []mobileBriefCardWords{{ID: "opener", Message: "I'd start with the Northwind check-in: it is on now. Want to begin there?"}}})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func captureMobileBriefLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var out bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&out)
	t.Cleanup(func() { log.SetOutput(previous) })
	return &out
}

const (
	writtenCheckIn = "Good morning. Your Northwind weekly check-in with Andy, Aaron and Diego is on now on Teams, and you haven't answered the invite."
	writtenDay     = "After that you're clear until deep work at 12:30pm, then Dr Lee at 3pm, dinner at 6pm and your 1:1 with Wei at 7:30pm."
	writtenShoe    = "The shoe insert follow-up with Sam has been open since last Sunday, and I can draft the note for you."
)

func writtenGoodReply(t *testing.T) string {
	return writtenReply(t, &mobileBriefNarrativeReplyClosing{Text: "Want me to draft the note to Sam?", Card: "offer-2"},
		mobileBriefNarrativeReplyParagraph{Text: writtenCheckIn + " " + writtenDay, Refs: []string{"e23", "e9", "e10", "e11", "e22"}},
		mobileBriefNarrativeReplyParagraph{Text: writtenShoe, Refs: []string{"e7"}})
}

// The voice the user liked (2026-10-20) passes as written: the model's own
// closing offers a ready button by its subject and carries its refs.
func TestMobileBriefWrittenBriefKeepsTheModelsClosing(t *testing.T) {
	brief, picks := writtenBriefFixture()
	logged := captureMobileBriefLog(t)
	paragraphs, words, err := mobileBriefNarrativeFromReply(brief, picks, writtenGoodReply(t), "English")
	if err != nil {
		t.Fatalf("rule=%s", mobileBriefNarrativeRule(err))
	}
	if len(paragraphs) != 3 || paragraphs[2].Text != "Want me to draft the note to Sam?" || strings.Join(paragraphs[2].Refs, ",") != "e7" {
		t.Fatalf("paragraphs = %+v", paragraphs)
	}
	if len(words) != 1 || logged.Len() != 0 {
		t.Fatalf("card words %d, log %q", len(words), logged.String())
	}
	brief.Narrative, brief.NarrativeStatus = paragraphs, "ok"
	if got := mobileBriefDeliveredClosing(brief, "English"); got.Text != paragraphs[2].Text {
		t.Fatalf("delivered closing = %q", got.Text)
	}
	if n := mobileBriefNarrativeWords(paragraphs); n > mobileBriefMaxWords {
		t.Fatalf("words = %d", n)
	}
}

// A closing that does not match a ready button is refused with its reason,
// so the one retry can fix it.
func TestMobileBriefWrittenClosingMustMatchACard(t *testing.T) {
	brief, picks := writtenBriefFixture()
	body := []mobileBriefNarrativeReplyParagraph{{Text: writtenCheckIn, Refs: []string{"e23"}}, {Text: writtenShoe, Refs: []string{"e7"}}}
	for name, closing := range map[string]*mobileBriefNarrativeReplyClosing{
		"no closing":         nil,
		"an unknown button":  {Text: "Want me to draft the note to Sam?", Card: "offer-9"},
		"another subject":    {Text: "Want me to plan your afternoon?", Card: "offer-2"},
		"two questions":      {Text: "Ready? Want me to draft the note to Sam?", Card: "offer-2"},
		"not a question":     {Text: "I can draft the note to Sam.", Card: "offer-2"},
		"uncertainty copy":   {Text: "I noticed Sam is waiting; want me to draft the note to Sam?", Card: "offer-2"},
		"a past clock time":  {Text: "Want me to draft the note to Sam you started at 7am?", Card: "offer-2"},
		"the template's own": nil,
	} {
		t.Run(name, func(t *testing.T) {
			body := append([]mobileBriefNarrativeReplyParagraph{}, body...)
			if name == "the template's own" {
				// A reply that copies the old fixed closing as a paragraph
				// still owes its own closing.
				own, _ := mobileBriefClosing(brief, "English")
				body = append(body, mobileBriefNarrativeReplyParagraph{Text: own.Text, Refs: own.Refs})
			}
			_, _, err := mobileBriefNarrativeFromReply(brief, picks, writtenReply(t, closing, body...), "English")
			if mobileBriefNarrativeRule(err) != mobileBriefRuleClosing {
				t.Fatalf("rule=%s (%v)", mobileBriefNarrativeRule(err), err)
			}
		})
	}
}

// A failed check gets one retry with the reason; a second failure is the
// template, and the log names the picks by tier and the check, never text.
func TestMobileBriefWrittenBriefRetriesOnceWithTheReason(t *testing.T) {
	brief, picks := writtenBriefFixture()
	bad := writtenReply(t, &mobileBriefNarrativeReplyClosing{Text: "Want me to help you pick up where you left off?", Card: "offer-2"},
		mobileBriefNarrativeReplyParagraph{Text: writtenShoe, Refs: []string{"e7"}})
	var asked []string
	replies := []string{bad, writtenGoodReply(t)}
	call := func(prompt string) (string, error) {
		asked = append(asked, prompt)
		return replies[len(asked)-1], nil
	}
	paragraphs, _, check := writeMobileBriefNarrative(context.Background(), brief, picks, "PROMPT", "English", call)
	if check != "" || len(paragraphs) != 3 || len(asked) != 2 {
		t.Fatalf("check=%q paragraphs=%d asked=%d", check, len(paragraphs), len(asked))
	}
	if !strings.HasPrefix(asked[1], "PROMPT\nYour previous reply could not be used: the closing must name the subject of the button it offers.") {
		t.Fatalf("retry prompt = %q", asked[1])
	}

	asked, replies = nil, []string{bad, bad}
	if _, _, check := writeMobileBriefNarrative(context.Background(), brief, picks, "PROMPT", "English", call); check != mobileBriefRuleClosing || len(asked) != 2 {
		t.Fatalf("check=%q asked=%d", check, len(asked))
	}
	if _, _, check := writeMobileBriefNarrative(context.Background(), brief, picks, "PROMPT", "English", func(string) (string, error) {
		return "", errors.New("timeout")
	}); check != "model_call" {
		t.Fatalf("check=%q", check)
	}

	logged := captureMobileBriefLog(t)
	paragraphs, status, _ := composeMobileBriefNarrative(context.Background(), nil, brief, nil)
	if status != "unavailable" || len(paragraphs) == 0 {
		t.Fatalf("status=%s paragraphs=%d", status, len(paragraphs))
	}
	// The composer logs no line of its own; the store step logs the one summary line (design R74 v1.0.0).
	if line := logged.String(); strings.Contains(line, "[mobile-brief] morning brief:") {
		t.Fatalf("the composer logged a per-outcome line: %q", line)
	}
	summary := mobileBriefLogSummary(MobileBriefV1{Slot: "morning", Sections: brief.Sections, Narrative: paragraphs, NarrativeStatus: status})
	if !strings.Contains(summary, "writer=template sentences=") || !strings.Contains(summary, "stored morning brief:") {
		t.Fatalf("summary = %q", summary)
	}
	if want := mobileBriefPickCounts(picks); !strings.Contains(want, "picks=3") || !strings.Contains(want, "meeting=1") || !strings.Contains(want, "follow_up=1") || !strings.Contains(want, "observed=1") {
		t.Fatalf("picks tally = %q", want)
	}
	for _, word := range []string{"Northwind", "Sam", "Wei"} {
		if strings.Contains(summary, word) {
			t.Fatalf("the log carries item text: %q", summary)
		}
	}
}

// Over the budget, the last paragraphs before the closing go; the closing
// stays and the brief ends within 300 words.
func TestMobileBriefWrittenBriefKeepsTheWordBudget(t *testing.T) {
	brief, picks := writtenBriefFixture()
	filler := strings.Repeat(" It is a calm, open stretch to think ahead.", 8)
	var body []mobileBriefNarrativeReplyParagraph
	for _, text := range []string{writtenCheckIn, writtenDay, writtenShoe, "Your 1:1 with Wei is tonight at 7:30pm on Zoom.", "Deep work starts at 12:30pm."} {
		body = append(body, mobileBriefNarrativeReplyParagraph{Text: text + filler, Refs: []string{"e23", "e9", "e10", "e11", "e22", "e7"}})
	}
	paragraphs, _, err := mobileBriefNarrativeFromReply(brief, picks,
		writtenReply(t, &mobileBriefNarrativeReplyClosing{Text: "Want me to draft the note to Sam?", Card: "offer-2"}, body...), "English")
	if err != nil {
		t.Fatalf("rule=%s", mobileBriefNarrativeRule(err))
	}
	if n := mobileBriefNarrativeWords(paragraphs); n > mobileBriefMaxWords || len(paragraphs) >= 6 || paragraphs[len(paragraphs)-1].Text != "Want me to draft the note to Sam?" {
		t.Fatalf("words=%d paragraphs=%d", n, len(paragraphs))
	}
}

// A repairable device reference is rewritten; only an unsafe sentence costs
// that sentence, not the brief.
func TestMobileBriefWrittenBriefDropsOnlyTheSentenceThatBreaksARule(t *testing.T) {
	brief, picks := writtenBriefFixture()
	closing := &mobileBriefNarrativeReplyClosing{Text: "Want me to draft the note to Sam?", Card: "offer-2"}
	for _, tc := range []struct{ name, sentence, rule, kept string }{
		{"a clock time for the past", " The check-in started at 7am.", mobileBriefRuleClock, ""},
		{"a second question", " Should we skip it?", mobileBriefRuleQuestion, ""},
		{"uncertainty copy", " I noticed you joined late.", mobileBriefRuleBanned, ""},
		{"a device name", " Teams is open on your Mac.", mobileBriefRuleVoice, " Teams is open on your computer."},
		{"an ambiguous device quotation", ` The document "Your Mac setup is ready.`, mobileBriefRuleVoice, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logged := captureMobileBriefLog(t)
			raw := writtenReply(t, closing,
				mobileBriefNarrativeReplyParagraph{Text: writtenCheckIn + tc.sentence + " I can pull up what is open with them.", Refs: []string{"e23"}},
				mobileBriefNarrativeReplyParagraph{Text: writtenShoe, Refs: []string{"e7"}})
			paragraphs, _, err := mobileBriefNarrativeFromReply(brief, picks, raw, "English")
			if err != nil {
				t.Fatalf("rule=%s", mobileBriefNarrativeRule(err))
			}
			if want := writtenCheckIn + tc.kept + " I can pull up what is open with them."; paragraphs[0].Text != want || len(paragraphs) != 3 {
				t.Fatalf("paragraphs = %+v", paragraphs)
			}
			if !strings.Contains(logged.String(), "rules="+tc.rule) {
				t.Fatalf("log = %q", logged.String())
			}
		})
	}
	t.Run("an exclamation", func(t *testing.T) {
		raw := writtenReply(t, closing, mobileBriefNarrativeReplyParagraph{Text: writtenShoe[:len(writtenShoe)-1] + "!", Refs: []string{"e7"}})
		paragraphs, _, err := mobileBriefNarrativeFromReply(brief, picks, raw, "English")
		if err != nil || paragraphs[0].Text != writtenShoe {
			t.Fatalf("paragraphs = %+v, rule=%s", paragraphs, mobileBriefNarrativeRule(err))
		}
	})
	t.Run("the cards' shape", func(t *testing.T) {
		raw := strings.Replace(writtenGoodReply(t), `"cards":[{"id":"opener"`, `"cards":[{"tone":"warm","id":"opener"`, 1)
		if _, _, err := mobileBriefNarrativeFromReply(brief, picks, raw, "English"); err != nil {
			t.Fatalf("a card field cost the brief its text: rule=%s", mobileBriefNarrativeRule(err))
		}
	})
	t.Run("an item that is not a pick", func(t *testing.T) {
		raw := writtenReply(t, closing,
			mobileBriefNarrativeReplyParagraph{Text: "The SOC 2 thread is worth a conclusion.", Refs: []string{"e12"}},
			mobileBriefNarrativeReplyParagraph{Text: writtenShoe, Refs: []string{"e7"}})
		paragraphs, _, err := mobileBriefNarrativeFromReply(brief, picks, raw, "English")
		if err != nil || len(paragraphs) != 2 || strings.Contains(paragraphs[0].Text, "SOC") {
			t.Fatalf("paragraphs = %+v, rule=%s", paragraphs, mobileBriefNarrativeRule(err))
		}
	})
}

// The pr-desk nit on #79: when repair drops a sentence, the next one may lean
// on it with a pronoun ("It has been open since last Sunday."). No paragraph
// opens, and no sentence follows a dropped one, with a pronoun whose noun
// was dropped.
func TestMobileBriefRepairLeavesNoDanglingPronoun(t *testing.T) {
	brief, _ := writtenBriefFixture()
	for _, text := range []string{
		"I noticed the shoe insert follow-up with Sam. It has been open since last Sunday. I can draft the note for you.",
		"The check-in started at 7am. They are waiting on your answer to the invite. I can pull up what is open with them.",
		"I can't tell whether Sam replied. That leaves the note open. Its draft can be ready in a minute. I can draft the note for you.",
		"I saw the check-in. This is the one to answer first. These people meet weekly. I can pull up what is open with them.",
	} {
		paragraphs, repaired := repairMobileBriefNarrative(brief, []MobileBriefPara{{Text: text, Refs: []string{"e7", "e23"}}}, "English")
		if len(paragraphs) != 1 || !strings.HasPrefix(paragraphs[0].Text, "I can ") {
			t.Fatalf("%q -> %+v", text, paragraphs)
		}
		if !strings.Contains(strings.Join(repaired, ","), mobileBriefRulePronoun) {
			t.Fatalf("%q: repaired %v", text, repaired)
		}
		for _, paragraph := range paragraphs {
			if mobileBriefOpensWithPronoun(splitIntoSentences(paragraph.Text)[0]) {
				t.Fatalf("a paragraph opens with a dangling pronoun: %q", paragraph.Text)
			}
		}
	}
	// A pronoun after a kept sentence is fine.
	kept := "The shoe insert follow-up with Sam is still open. It has been open since last Sunday."
	if paragraphs, _ := repairMobileBriefNarrative(brief, []MobileBriefPara{{Text: kept, Refs: []string{"e7"}}}, "English"); len(paragraphs) != 1 || paragraphs[0].Text != kept {
		t.Fatalf("paragraphs = %+v", paragraphs)
	}
	// Chinese too.
	paragraphs, _ := repairMobileBriefNarrative(brief, []MobileBriefPara{{Text: "我注意到Sam的跟进还没完成。它从上周日起一直未结。我可以帮你起草消息。", Refs: []string{"e7"}}}, "Chinese")
	if len(paragraphs) != 1 || paragraphs[0].Text != "我可以帮你起草消息。" {
		t.Fatalf("paragraphs = %+v", paragraphs)
	}
}

// What cannot be repaired is refused with its rule, for the retry and the log.
func TestMobileBriefWrittenBriefNamesTheRuleItCannotRepair(t *testing.T) {
	brief, picks := writtenBriefFixture()
	for raw, rule := range map[string]string{
		"Here is your brief.": mobileBriefRuleJSON,
		`{"paragraphs":[{"text":"x","refs":["e7"]}]} and more`:               mobileBriefRuleTrailing,
		`{"paragraphs":[{"text":"x","refs":["e99"]}]}`:                       mobileBriefRuleNoText,
		`{"paragraphs":[{"text":"I noticed nothing.","refs":["e7"]}]}`:       mobileBriefRuleNoText,
		`{"paragraphs":[{"text":"The SOC 2 thread waits.","refs":["e12"]}]}`: mobileBriefRuleNoText,
	} {
		if _, _, err := mobileBriefNarrativeFromReply(brief, picks, raw, "English"); mobileBriefNarrativeRule(err) != rule {
			t.Errorf("%q: rule=%s, want %s", raw, mobileBriefNarrativeRule(err), rule)
		}
	}
}

// Chinese copy is repaired the same way: a full stop for an exclamation
// mark, and no question outside the closing.
func TestMobileBriefWrittenBriefRepairsChineseCopy(t *testing.T) {
	brief := briefPicksFixture()
	_, brief.Offers = mobileBriefClosing(brief, "Chinese")
	raw := writtenReply(t, &mobileBriefNarrativeReplyClosing{Text: "要我起草给Sam的跟进消息吗？", Card: writtenOfferID(brief, "e7")},
		mobileBriefNarrativeReplyParagraph{Text: "Sam的鞋垫跟进从上周日起一直未结！要不要现在就写？我可以帮你起草。", Refs: []string{"e7"}})
	paragraphs, _, err := mobileBriefNarrativeFromReply(brief, mobileBriefPicks(brief), raw, "Chinese")
	if err != nil {
		t.Fatalf("rule=%s (%v)", mobileBriefNarrativeRule(err), err)
	}
	if got := paragraphs[0].Text; got != "Sam的鞋垫跟进从上周日起一直未结。我可以帮你起草。" || paragraphs[1].Text != "要我起草给Sam的跟进消息吗？" {
		t.Fatalf("paragraphs = %+v", paragraphs)
	}
}

// The About-you notes the prompt read are in the model-read binding: a note
// deleted, withheld or retitled after the brief was written withdraws its
// text, as any other input does (#79).
func TestMobileBriefBindingHoldsTheAboutYouNotes(t *testing.T) {
	for _, change := range []string{"none", "deleted", "retitled"} {
		t.Run(change, func(t *testing.T) {
			app := newTodayContextTestApp(t, true)
			if _, err := app.SaveMemoryEntry("works", "---\ndimension: deep_user_model\n---\n# Builds product with coding agents\nPrefers short status reports."); err != nil {
				t.Fatal(err)
			}
			brief := boundMemoryConversationFixture(t, app)
			if profile := app.mobileBriefProfileFor(brief); strings.Join(profile.NoteIDs, ",") != "works" {
				t.Fatalf("profile = %+v", profile)
			}
			bindConversationBriefFixture(t, app, &brief)
			if got := brief.modelBinding.Profile; len(got) != 1 || got[0].ID != "works" {
				t.Fatalf("binding profile = %+v", got)
			}
			brief.NarrativeStatus = "ok"
			switch change {
			case "deleted":
				if err := app.DeleteMemoryEntry("works"); err != nil {
					t.Fatal(err)
				}
			case "retitled":
				if _, err := app.SaveMemoryEntry("works", "---\ndimension: deep_user_model\n---\n# Builds hardware now\nPrefers short status reports."); err != nil {
					t.Fatal(err)
				}
			}
			proven := app.validateMobileBriefModelSources(t.Context(), brief, brief.modelBinding)
			delivered, withheld := app.mobileBriefDelivery(brief)
			if change == "none" {
				if !proven || len(delivered.Narrative) == 0 || withheld.Reason != "" {
					t.Fatalf("an unchanged note withdrew the text: proven=%v withheld=%+v", proven, withheld)
				}
				return
			}
			if proven || len(delivered.Narrative) != 0 || delivered.NarrativeStatus != "unavailable" || withheld.SourceKind != "memory" {
				t.Fatalf("a %s note kept the text: proven=%v narrative=%d withheld=%+v", change, proven, len(delivered.Narrative), withheld)
			}
		})
	}
}

func TestMobileBriefPromptRequiresAnswersAndParagraphChips(t *testing.T) {
	brief, picks := writtenBriefFixture()
	prompt := buildMobileBriefNarrativePrompt(brief, picks, mobileBriefProfile{}, nil, "English")
	for _, rule := range []string{"Never give the user homework", "Never put interface sentences in paragraphs", "I can open/show/add ... in <page>", "put the suggested next step in a chip instead", "A body paragraph may have at most one chip", "named by its first ref", "Omit chip otherwise", "closing.answers in the same pass as closing.text", "Never copy an unrelated action card as an answer", "leave answers empty", `"answers":[`} {
		if !strings.Contains(prompt, rule) {
			t.Errorf("missing prompt rule %q", rule)
		}
	}
}

func TestMobileBriefStripsInterfaceSentencesAndLogsOnce(t *testing.T) {
	brief, picks := writtenBriefFixture()
	logged := captureMobileBriefLog(t)
	raw := writtenReply(t, &mobileBriefNarrativeReplyClosing{Text: "Want me to draft the note to Sam?", Card: "offer-2"},
		mobileBriefNarrativeReplyParagraph{Text: "Sam's shoe insert follow-up is still open. I can open the follow-up field in Activity for the reply to 加一. I can open the follow-up field in Activity for that too.", Refs: []string{"e7"}},
		mobileBriefNarrativeReplyParagraph{Text: "Sam is waiting for the note. I can show the note in Today. I can add a follow-up in the Activity page. I can open it for that too.", Refs: []string{"e7"}})
	paragraphs, _, err := mobileBriefNarrativeFromReply(brief, picks, raw, "English")
	if err != nil || len(paragraphs) != 3 || paragraphs[0].Text != "Sam's shoe insert follow-up is still open." || paragraphs[1].Text != "Sam is waiting for the note." {
		t.Fatalf("interface repair: %+v %v", paragraphs, err)
	}
	if strings.Count(logged.String(), "interface_sentence") != 1 || strings.Contains(logged.String(), "加一") || strings.Contains(logged.String(), "Sam") {
		t.Fatalf("repair log must name only the rule once: %s", logged.String())
	}
	for _, text := range []string{"I can draft the note for Sam.", "Sam can add a note in Activity.", "I can add detail to the draft."} {
		if mobileBriefInterfaceSentence.MatchString(text) {
			t.Errorf("ordinary content stripped: %s", text)
		}
	}
	for _, text := range []string{"我可以打开活动页面里的跟进字段。", "我可以在活动页面添加跟进。", "I can open the field in 活动页面.", "I can open Activity for that too.", "I can show this in Settings."} {
		if !mobileBriefInterfaceSentence.MatchString(text) {
			t.Errorf("interface content kept: %s", text)
		}
	}
}
