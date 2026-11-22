package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// Each source kind lands in its group; a session is the watch's only when a
// watch recording made it, else screen; a kind outside the six has none.
func TestBriefSourceGroupMapsKindsAndNeverGuessesTheWatch(t *testing.T) {
	watch := map[string]bool{"sess-watch": true}
	for _, c := range []struct {
		kind, id, want string
	}{
		{"calendar", "e", briefGroupCalendar},
		{"calendar_day", "d", briefGroupCalendar},
		{"meeting_attendance", "m", briefGroupCalls},
		{"meeting", "m", briefGroupCalls},
		{"meeting_transcript", "t", briefGroupCalls},
		{"reminder", "r", briefGroupReminders},
		{"schedule", "j", briefGroupAgents},
		{"brief_waiting_reply", "w", briefGroupMail},
		{"mail", "m", briefGroupMail},
		{"message_ask", "a", briefGroupMessages},
		{"activity_session", "sess-watch", briefGroupWatch},
		{"activity_session", "sess-screen", briefGroupScreen},
		{"history", "h", briefGroupAgents},
		{"handoff", "h", briefGroupAgents},
		{"deliverable", "d", briefGroupAgents},
		{"thread_outcome", "t", briefGroupAgents},
		{"agent_handoff", "wo", briefGroupAgents},
		{"activity", "a", briefGroupScreen},
		{"file", "f", briefGroupScreen},
		{"web", "w", briefGroupScreen},
		{"purpose", "p", ""},
		{"memory", "m", ""},
		{"some_new_kind", "x", ""},
	} {
		if got := briefSourceGroup(MobileBriefRef{SourceKind: c.kind, SourceID: c.id}, watch); got != c.want {
			t.Errorf("%s/%s = %q, want %q", c.kind, c.id, got, c.want)
		}
	}
}

// The group is additive on the wire: omitted when empty, "group" when set.
func TestBriefSourceGroupJSON(t *testing.T) {
	app := &App{}
	brief := MobileBriefV1{Evidence: []MobileBriefRef{{Ref: "e1", SourceKind: "calendar", SourceID: "ev"}, {Ref: "e2", SourceKind: "purpose", SourceID: "p"}}}
	app.mobileBriefWithSourceGroups(&brief)
	raw, err := json.Marshal(brief.Evidence)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"group":"calendar"`) || strings.Count(string(raw), `"group"`) != 1 {
		t.Fatalf("evidence = %s", raw)
	}
}
