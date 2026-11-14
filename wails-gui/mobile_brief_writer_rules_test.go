package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	briefcore "wails-gui/internal/brief"
)

// The writer rules that live on the app side, as replay rows with made-up
// data (the writer's own rules are in internal/brief/rules_test.go):
//   - excluded items never reach the writer, wherever the excluded term sits;
//   - messaging reaches the writer only as the outcome's paraphrase.

// TestWriterRuleExcludedItemsNeverReachTheWriter puts the excluded term in
// one field of one source per row, applies the exclusions the way the
// service does, and checks the writer's whole prompt.
func TestWriterRuleExcludedItemsNeverReachTheWriter(t *testing.T) {
	const term = "Juniper"
	rows := []struct {
		name string
		add  func(in *mobileBriefInput)
	}{
		{"moved title", func(in *mobileBriefInput) {
			in.Moved = append(in.Moved, mobileBriefCandidate{SourceKind: mobileBriefRecapSource, SourceID: "x-moved", Title: "Plan the Juniper visit", Status: "done", Kind: "recap", At: in.AsOf.Add(-time.Hour)})
		}},
		{"commitment detail", func(in *mobileBriefInput) {
			in.Commitments = append(in.Commitments, mobileBriefCandidate{SourceKind: "commitment", SourceID: "x-commit", Title: "Book the session", Detail: "for Juniper", Status: "open", Kind: "commitment", At: in.AsOf.Add(-time.Hour)})
		}},
		{"follow-up next step", func(in *mobileBriefInput) {
			in.Noticed = append(in.Noticed, mobileBriefCandidate{SourceKind: "noticed", SourceID: "x-noticed", Title: "Reply about the class", NextStep: "Tell Juniper the time", Status: "open", Kind: "follow_up", At: in.AsOf.Add(-time.Hour)})
		}},
		{"conversation person", func(in *mobileBriefInput) {
			in.Conversations = append(in.Conversations, mobileBriefCandidate{SourceKind: mobileBriefRecapSource, SourceID: "x-person", Title: "Talk about the class", Person: "Juniper", Status: "done", Kind: "conversation", At: in.AsOf.Add(-time.Hour)})
		}},
		{"conversation screen summary", func(in *mobileBriefInput) {
			in.Conversations = append(in.Conversations, mobileBriefCandidate{SourceKind: mobileBriefRecapSource, SourceID: "x-summary", Title: "Messages about the class", writerDetail: "Juniper asked you for the schedule by Friday", NextStep: "Send the schedule", Status: "needs_reply", Kind: "conversation", At: in.AsOf.Add(-time.Hour)})
		}},
		{"meeting attended", func(in *mobileBriefInput) {
			in.Attendance = append(in.Attendance, mobileBriefAttendanceObservation{EventID: "x-event", Title: "Juniper session", Start: in.AsOf.Add(-3 * time.Hour), End: in.AsOf.Add(-2 * time.Hour), State: "attended", ObservedAt: in.AsOf.Add(-2 * time.Hour)})
		}},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			in := newWriterTestInput()
			row.add(&in)
			// Without the exclusion the term reaches the writer, so the row
			// tests the field it means to.
			if prompt := writerRulesPrompt(in); !strings.Contains(prompt, term) {
				t.Fatalf("the row's source did not reach the writer at all")
			}
			in = mobileBriefWithoutExcluded(in, []string{strings.ToLower(term)})
			if prompt := writerRulesPrompt(in); strings.Contains(strings.ToLower(prompt), strings.ToLower(term)) {
				t.Fatalf("an excluded item reached the writer's prompt")
			}
		})
	}
}

func writerRulesPrompt(in mobileBriefInput) string {
	brief := buildMobileBriefV1(in)
	m, _ := newBriefMaterial(in, brief, briefPartDone, "")
	return briefcore.WritePrompt(m, "en")
}

// TestWriterRuleMessagingIsParaphrased: a chat outcome reaches the writer as
// its summary and next step only; the chat itself (the candidate's detail and
// its apps) never does, and a conversation without an outcome carries its
// title alone.
func TestWriterRuleMessagingIsParaphrased(t *testing.T) {
	rows := []struct {
		name     string
		c        mobileBriefCandidate
		want     []string
		mustNot  []string
		wantSeen bool
	}{
		{"chat ask as paraphrase", mobileBriefCandidate{SourceKind: mobileBriefRecapSource, SourceID: "chat-ask", Title: "Messages with Priya", Apps: []string{"WeChat"},
			writerDetail: "Priya asked you for the launch deck by Friday", NextStep: "Send Priya the launch deck", Status: "needs_reply", Kind: "conversation"},
			[]string{"Priya asked you for the launch deck by Friday", "Send Priya the launch deck"}, []string{"WeChat"}, true},
		{"chat commitment as paraphrase", mobileBriefCandidate{SourceKind: mobileBriefRecapSource, SourceID: "chat-commit", Title: "Messages with Sam", Apps: []string{"Messages"},
			writerDetail: "You told Sam you would review the draft tomorrow", Status: "done", Kind: "conversation"},
			[]string{"You told Sam you would review the draft tomorrow"}, []string{"Messages\""}, true},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			in := newWriterTestInput()
			row.c.At = in.AsOf.Add(-time.Hour)
			in.Conversations = append(in.Conversations, row.c)
			brief := buildMobileBriefV1(in)
			m, _ := newBriefMaterial(in, brief, briefPartDone, "")
			var item *briefcore.MaterialItem
			for i := range m.Items {
				if m.Items[i].Title == row.c.Title {
					item = &m.Items[i]
				}
			}
			if (item != nil) != row.wantSeen {
				t.Fatalf("conversation reached the writer = %t, want %t", item != nil, row.wantSeen)
			}
			raw, _ := json.Marshal(item)
			for _, w := range row.want {
				if !strings.Contains(string(raw), w) {
					t.Fatalf("material item lacks %q: %s", w, raw)
				}
			}
			for _, w := range row.mustNot {
				if strings.Contains(string(raw), w) {
					t.Fatalf("material item carries %q: %s", w, raw)
				}
			}
		})
	}
}
