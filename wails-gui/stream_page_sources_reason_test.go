package main

import (
	"reflect"
	"testing"
)

// A dropped first-page copy names what moved, by field only, so the log tells
// a real change from a check that ran out of time (2026-11-15: two launches
// recomputed the page for sources-changed, 1.7 and 2.1 s).
func TestStreamPageSourcesNameWhatMoved(t *testing.T) {
	was := streamPageSessionState{Status: "done", Apps: "Mail", End: 10, Notes: "a.md", NotesSize: 3}
	is := was
	is.End, is.NotesSize = 20, 4
	if got := streamPageSessionStateDiff(was, is); !reflect.DeepEqual(got, []string{"end", "notes"}) {
		t.Fatalf("moved = %v", got)
	}
	if got := streamPageSessionStateDiff(was, was); len(got) != 0 {
		t.Fatalf("an unchanged session moved %v", got)
	}
	// Without an activity store the check cannot finish: the copy is not
	// served, and not thrown away either.
	app := &App{}
	if got := app.streamPageSourcesCheck(map[string]streamPageSessionState{"s1": was}); got != "sources-unread" {
		t.Fatalf("unfinished check = %q, want sources-unread", got)
	}
	if app.streamPageSourcesHold(map[string]streamPageSessionState{"s1": was}) {
		t.Fatal("an unfinished check held the copy's sources")
	}
}
