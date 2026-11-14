package brief

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// replayNov14Material is the shape of the material of the brief of Saturday,
// November 14, 2026 at 6:50 AM: fourteen done items and two agent items from
// Friday ("yesterday"), a meetup attended yesterday, two items ahead today, a
// commitment and a follow-up, and ten context items including a habit that
// slipped. Titles are patterns, not the user's words.
func replayNov14Material(t *testing.T) Material {
	t.Helper()
	loc, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatal(err)
	}
	m := Material{Slot: "morning", Larger: PartNext, Now: "the morning of Saturday, November 14 (the brief is read then)",
		Today: time.Date(2026, 11, 14, 0, 0, 0, 0, loc), Lang: "en"}
	for i := 0; i < 14; i++ {
		m.AddItem(MaterialItem{Group: GroupDone, Title: fmt.Sprintf("<work item %d>", i), When: "yesterday", Since: true})
	}
	for i := 0; i < 4; i++ {
		m.AddItem(MaterialItem{Group: GroupAgents, Title: fmt.Sprintf("<agent work %d>", i), When: "yesterday", Agent: true})
	}
	m.AddItem(MaterialItem{Group: GroupAttended, Title: "<meetup>", When: "yesterday"})
	m.AddItem(MaterialItem{Group: GroupAhead, Title: "<meeting ahead>", When: "today 10:00 AM"})
	m.AddItem(MaterialItem{Group: GroupAhead, Title: "<second meeting>", When: "today 2:00 PM"})
	m.AddItem(MaterialItem{Group: GroupCommitment, Title: "<daily habit>", Detail: "slipping since November 5", When: "November 5"})
	m.AddItem(MaterialItem{Group: GroupFollowUp, Title: "<reply owed>", When: "yesterday"})
	for i := 0; i < 10; i++ {
		m.AddItem(MaterialItem{Group: GroupContext, Title: fmt.Sprintf("<context %d>", i)})
	}
	return m
}

func idsOf(m Material, group string) []string {
	var ids []string
	for _, item := range m.Items {
		if item.Group == group {
			ids = append(ids, item.ID)
		}
	}
	return ids
}

// The replay of the Nov 14 brief: the reply makes both mistakes the user saw,
// and the repaired draft says Friday and gives the habit's next step today.
func TestReplayNov14DayWords(t *testing.T) {
	m := replayNov14Material(t)
	prompt := WritePrompt(m, "en")
	for _, want := range []string{"Today is Saturday, November 14.", "Yesterday was Friday, November 13."} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("the prompt does not say %q", want)
		}
	}
	done := idsOf(m, GroupDone)
	habit := idsOf(m, GroupCommitment)
	reply, _ := json.Marshal(map[string][]map[string]any{
		"done": {{"text": "Thursday was full: you moved the launch work and the pricing page.", "refs": done[:6]}},
		"next": {
			{"text": "Your daily habit has been slipping since November 5. Do a set November 5 to restart it.", "refs": habit},
			{"text": "The meetup follow-up is still open from Friday.", "refs": idsOf(m, GroupFollowUp)},
		},
	})
	draft, codes, err := RepairDraft(string(reply), m)
	if err != nil {
		t.Fatal(err)
	}
	if got := draft.Done[0].Text; !strings.HasPrefix(got, "Friday was full") || strings.Contains(got, "Thursday") {
		t.Fatalf("done = %q", got)
	}
	if got := draft.Next[0].Text; !strings.Contains(got, "since November 5") || !strings.Contains(got, "Do a set today to restart it.") {
		t.Fatalf("next = %q", got)
	}
	if got := draft.Next[1].Text; !strings.Contains(got, "Friday") {
		t.Fatalf("a supported weekday was changed: %q", got)
	}
	joined := strings.Join(codes, ",")
	if !strings.Contains(joined, CodeDayFixed) || !strings.Contains(joined, CodePastDateNext) {
		t.Fatalf("codes = %v", codes)
	}
}

// Day checks keep what is right: a weekday ahead, a future date, a past date
// given as a reason, and a done paragraph over several days (only reported).
func TestDayChecksKeepSupportedWords(t *testing.T) {
	m := replayNov14Material(t)
	ahead := m.AddItem(MaterialItem{Group: GroupAhead, Title: "<review>", When: "Monday 9:00 AM"})
	older := m.AddItem(MaterialItem{Group: GroupDone, Title: "<older work>", When: "Wednesday"})
	done := idsOf(m, GroupDone)
	var codes []string
	cases := []struct {
		text  string
		done  bool
		refs  []string
		want  string
		codes int
	}{
		{"Prepare for the review on Monday.", false, []string{ahead}, "Prepare for the review on Monday.", 0},
		{"Send it by November 20.", false, []string{ahead}, "Send it by November 20.", 0},
		{"Picking it up again since November 5 matters.", false, []string{ahead}, "Picking it up again since November 5 matters.", 0},
		{"Thursday was busy.", true, []string{done[0], older}, "Thursday was busy.", 1},
	}
	byID := map[string]MaterialItem{}
	for _, item := range m.Items {
		byID[item.ID] = item
	}
	for _, c := range cases {
		codes = codes[:0]
		var cited []MaterialItem
		for _, r := range c.refs {
			cited = append(cited, byID[r])
		}
		if got := m.checkDays(c.text, c.done, cited, &codes); got != c.want || len(codes) != c.codes {
			t.Fatalf("%q -> %q, codes %v", c.text, got, codes)
		}
	}
	m.Lang = "zh"
	if got := m.checkDays("Thursday was busy.", true, []MaterialItem{byID[done[0]]}, &codes); got != "Thursday was busy." {
		t.Fatalf("a Chinese brief was rewritten: %q", got)
	}
}
