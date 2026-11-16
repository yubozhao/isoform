package main

import (
	"strings"
	"testing"

	"wails-gui/internal/platform/activitymon"
)

// The 2026-11-15 evening brief said you "talked with Jing Jin about your
// relationship": background listening heard you while her Messages chat was
// open, and the summary prompt showed that speech as "dictated" in the window
// "Jing Jin". Speech not dictated into an app now carries no window or app,
// says the listener is unknown, and reads "said aloud".
func TestAmbientSpeechIsNotGivenToTheChatOnScreen(t *testing.T) {
	heard := renderTimelineSummaryEvent(ActivityTimelineEvent{EventID: "ae_1", ActivityEvent: activitymon.ActivityEvent{
		Kind: activitymon.EventSpeech, AppName: "Messages", WindowTitle: "Jing Jin", SpeechSource: "microphone",
		SpeechText: "I am not taking this relationship for granted.",
	}})
	for _, banned := range []string{`window="Jing Jin"`, `app="Messages"`, `"dictated"`} {
		if strings.Contains(heard, banned) {
			t.Fatalf("ambient speech carries %s: %s", banned, heard)
		}
	}
	if !strings.Contains(heard, `action="said aloud"`) || !strings.Contains(heard, `listener="unknown"`) {
		t.Fatalf("ambient speech line = %s", heard)
	}
	audio := renderTimelineSummaryEvent(ActivityTimelineEvent{EventID: "ae_2", ActivityEvent: activitymon.ActivityEvent{
		Kind: activitymon.EventSpeech, AppName: "Zoom", WindowTitle: "Weekly", SpeechSource: "system_audio", SpeechText: "Let's ship Friday.",
	}})
	if !strings.Contains(audio, `action="heard from the computer's audio"`) || strings.Contains(audio, `window="Weekly"`) {
		t.Fatalf("computer audio line = %s", audio)
	}
	// Dictation into an app went to that app, so it keeps its window.
	dictated := renderTimelineSummaryEvent(ActivityTimelineEvent{EventID: "ae_3", ActivityEvent: activitymon.ActivityEvent{
		Kind: activitymon.EventSpeech, AppName: "Claude", WindowTitle: "isoform hub", SpeechSource: "microphone",
		SpeechDictationApp: "Claude", SpeechText: "Run the tests.",
	}})
	if !strings.Contains(dictated, `action="dictated into Claude"`) || !strings.Contains(dictated, `window="isoform hub"`) {
		t.Fatalf("dictated line = %s", dictated)
	}
	if !strings.Contains(activitySecondPersonRule, `never write that it was said to, told to, sent to or about the person whose chat`) {
		t.Fatal("the prompts lost the listener rule")
	}
}

func TestReplacePersonNameTakesTheWholeName(t *testing.T) {
	for _, tc := range []struct{ in, wrong, right, want string }{
		{"Talk with Jing Jin about your relationship", "Jing", "Coral", "Talk with Coral about your relationship"},
		{"Talk with Jing Jin about your relationship", "Jing Jin", "Coral", "Talk with Coral about your relationship"},
		{"Jing's location; Jingidy's 1:1", "Jing", "Coral", "Coral's location; Jingidy's 1:1"},
	} {
		if got := replacePersonName(tc.in, tc.wrong, tc.right); got != tc.want {
			t.Errorf("replacePersonName(%q, %q) = %q, want %q", tc.in, tc.wrong, got, tc.want)
		}
	}
}

// "It was Coral, not Jing" corrects the moments the brief cited as your own
// correction: it locks their words and teaches later titles. Other moments
// and non-summary sources are left alone.
func TestBriefPersonCorrectionFixesTheCitedMoment(t *testing.T) {
	app := newThreadTestApp(t)
	store := app.activity.store
	day, _ := threadTestDay(t)
	talk := seedThreadMoment(t, app, threadFixtureMoment{id: "talk", title: "Talk with Jing Jin about your relationship", app: "Messages", start: at(day, 15, 46), end: at(day, 15, 52)})
	other := seedThreadMoment(t, app, threadFixtureMoment{id: "other", title: "Dinner plans with Jing Jin", app: "Messages", start: at(day, 18, 0), end: at(day, 18, 10)})
	if _, err := store.db.Exec(`UPDATE activities SET description = ? WHERE id = ?`, "You are telling Jing Jin that you don't take the relationship for granted.", talk.ID); err != nil {
		t.Fatal(err)
	}
	evidence := []MobileBriefRef{
		{SourceKind: "summary", SourceID: talk.ID, Label: talk.Title},
		{SourceKind: "meeting_attendance", SourceID: "b1a5", Label: "1:1 with Jingidy"},
	}
	result, err := app.correctPersonInSources(evidence, "Jing", "Coral")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Corrected) != 1 || result.Corrected[0] != talk.ID {
		t.Fatalf("corrected = %+v", result)
	}
	got, _ := store.GetActivityByID(talk.ID)
	if got.Title != "Talk with Coral about your relationship" || strings.Contains(got.Description, "Jing") {
		t.Fatalf("corrected moment = %q / %q", got.Title, got.Description)
	}
	var locked string
	if err := store.db.QueryRow(`SELECT COALESCE(user_title, '') FROM activities WHERE id = ?`, talk.ID).Scan(&locked); err != nil || locked != got.Title {
		t.Fatalf("the correction is not locked as yours: %q %v", locked, err)
	}
	corrections, err := store.recentActivityNameCorrections(5)
	if err != nil || len(corrections) == 0 || corrections[0].After != "Talk with Coral about your relationship" {
		t.Fatalf("the composer does not learn it: %+v %v", corrections, err)
	}
	if untouched, _ := store.GetActivityByID(other.ID); untouched.Title != "Dinner plans with Jing Jin" {
		t.Fatalf("an uncited moment changed: %q", untouched.Title)
	}
	if _, err := app.CorrectActivityPerson(talk.ID, "Coral", "coral"); err != errPersonCorrectionNames {
		t.Fatalf("same name = %v", err)
	}
}
