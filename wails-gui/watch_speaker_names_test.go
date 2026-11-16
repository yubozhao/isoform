package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func speakerAnswer(bridge *mobileBridge, conversationID, speaker, answer, name string) (int, MobileConversationView) {
	body, _ := json.Marshal(MobileSpeakerAnswerRequest{ConversationID: conversationID, Speaker: speaker, Answer: answer, Name: name})
	got := watchDeskCall(bridge, http.MethodPost, "/recordings/conversation/speaker", string(body), mobileScopeChatWrite, mobileScopeBriefRead)
	var envelope struct {
		Data MobileConversationView `json:"data"`
	}
	_ = json.Unmarshal(got.Body.Bytes(), &envelope)
	return got.Code, envelope.Data
}

// A guess is offered only when the calendar leaves one person it could be.
func TestASpeakerIsGuessedOnlyWhenTheCalendarLeavesOnePerson(t *testing.T) {
	c := WatchConversation{ID: "dev:1", StartAt: 1_000_000, EndAt: 2_000_000, Speakers: []string{"you", "speaker_1"}}
	one := []watchSpeakerEvent{{StartAt: 900_000, EndAt: 2_500_000, Others: []string{"Lena Ortiz"}}}
	guess := watchSpeakerCalendarGuess(c, watchSpeakerNamesEntry{}, one)
	if guess == nil || guess.Speaker != "speaker_1" || guess.Name != "Lena Ortiz" || guess.Reason != "calendar" || guess.EventStartAt != 900_000 {
		t.Fatalf("guess %+v", guess)
	}
	for name, events := range map[string][]watchSpeakerEvent{
		"no event":             nil,
		"an event elsewhere":   {{StartAt: 3_000_000, EndAt: 4_000_000, Others: []string{"Lena Ortiz"}}},
		"two people":           {{StartAt: 900_000, EndAt: 2_500_000, Others: []string{"Lena Ortiz", "Sam Li"}}},
		"a person and a blank": {{StartAt: 900_000, EndAt: 2_500_000, Others: []string{"Lena Ortiz", ""}}},
		"nobody else":          {{StartAt: 900_000, EndAt: 2_500_000}},
		"two events":           {one[0], {StartAt: 1_500_000, EndAt: 1_600_000, Others: []string{"Sam Li"}}},
		"no name":              {{StartAt: 900_000, EndAt: 2_500_000, Others: []string{""}}},
	} {
		if got := watchSpeakerCalendarGuess(c, watchSpeakerNamesEntry{}, events); got != nil {
			t.Fatalf("%s: guessed %+v", name, got)
		}
	}
	two := c
	two.Speakers = []string{"you", "speaker_1", "speaker_2"}
	if got := watchSpeakerCalendarGuess(two, watchSpeakerNamesEntry{}, one); got != nil {
		t.Fatalf("two unnamed speakers: guessed %+v", got)
	}
	// Once one of the two is named, the other is the one left.
	named := watchSpeakerNamesEntry{Names: map[string]string{"speaker_1": "Sam Li"}}
	if got := watchSpeakerCalendarGuess(two, named, one); got == nil || got.Speaker != "speaker_2" {
		t.Fatalf("the speaker left over: %+v", got)
	}
	declined := watchSpeakerNamesEntry{Declined: map[string][]string{"speaker_1": {"Lena Ortiz"}}}
	if got := watchSpeakerCalendarGuess(c, declined, one); got != nil {
		t.Fatalf("a declined name was offered again: %+v", got)
	}
	if got := watchSpeakerCalendarGuess(WatchConversation{Speakers: []string{"you"}}, watchSpeakerNamesEntry{}, one); got != nil {
		t.Fatalf("nobody to name: %+v", got)
	}
}

func TestASpeakersNameIsShortAndPlain(t *testing.T) {
	for raw, want := range map[string]string{"  Lena   Ortiz ": "Lena Ortiz", "李雷": "李雷"} {
		if got, ok := cleanWatchSpeakerName(raw); !ok || got != want {
			t.Fatalf("%q: %q %v", raw, got, ok)
		}
	}
	long := make([]byte, watchSpeakerNameLimit+1)
	for i := range long {
		long[i] = 'a'
	}
	for _, raw := range []string{"", "   ", "you", "speaker_3", "Lena\x00Ortiz", "<b>Lena</b>", string(long)} {
		if got, ok := cleanWatchSpeakerName(raw); ok {
			t.Fatalf("%q was accepted as %q", raw, got)
		}
	}
}

// The phone judges a guess, names a speaker by hand, and the name goes with
// the words it belongs to.
func TestThePhoneAnswersWhoASpeakerIs(t *testing.T) {
	app, bridge, store, first := watchDeskFixture(t)
	old := watchSpeakerEvents
	t.Cleanup(func() { watchSpeakerEvents = old })
	watchSpeakerEvents = func(_ *App, from, to int64) []watchSpeakerEvent {
		return []watchSpeakerEvent{{StartAt: from, EndAt: to, Others: []string{"Lena Ortiz"}}}
	}
	conv := phoneConversations(t, bridge).Conversations[0]
	if conv.SpeakerGuess == nil || conv.SpeakerGuess.Speaker != "speaker_1" || conv.SpeakerGuess.Name != "Lena Ortiz" || len(conv.SpeakerNames) != 0 {
		t.Fatalf("the open guess: %+v %+v", conv.SpeakerGuess, conv.SpeakerNames)
	}

	if code, _ := speakerAnswer(bridge, conv.ID, "speaker_9", "yes", ""); code != http.StatusBadRequest {
		t.Fatalf("a speaker that is not there: %d", code)
	}
	if code, _ := speakerAnswer(bridge, "someone-else:1", "speaker_1", "yes", ""); code != http.StatusNotFound {
		t.Fatalf("another phone's conversation: %d", code)
	}
	if code, _ := speakerAnswer(bridge, conv.ID, "speaker_1", "edit", "<script>"); code != http.StatusBadRequest {
		t.Fatalf("an unusable name: %d", code)
	}
	if got := watchDeskCall(bridge, http.MethodPost, "/recordings/conversation/speaker", `{}`, mobileScopeChatWrite); got.Code != http.StatusForbidden {
		t.Fatalf("without brief:read: %d", got.Code)
	}

	// No: the name is not offered again.
	code, after := speakerAnswer(bridge, conv.ID, "speaker_1", "no", "")
	if code != http.StatusOK || after.SpeakerGuess != nil || len(after.SpeakerNames) != 0 {
		t.Fatalf("after No: %d %+v", code, after)
	}
	if code, _ := speakerAnswer(bridge, conv.ID, "speaker_1", "yes", ""); code != http.StatusBadRequest {
		t.Fatalf("Yes with no guess open: %d", code)
	}
	// Edit: named by hand.
	code, after = speakerAnswer(bridge, conv.ID, "speaker_1", "edit", "  Sam  Li ")
	if code != http.StatusOK || after.SpeakerNames["speaker_1"] != "Sam Li" {
		t.Fatalf("after Edit: %d %+v", code, after.SpeakerNames)
	}
	if again := phoneConversations(t, bridge).Conversations[0]; again.SpeakerNames["speaker_1"] != "Sam Li" || again.SpeakerGuess != nil {
		t.Fatalf("the name on the next read: %+v %+v", again.SpeakerNames, again.SpeakerGuess)
	}
	if info, err := os.Stat(filepath.Join(store.dir, watchSpeakerNamesFile)); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("the names file: %v %v", info, err)
	}
	// Edit with nothing takes the name away.
	if code, after = speakerAnswer(bridge, conv.ID, "speaker_1", "edit", ""); code != http.StatusOK || len(after.SpeakerNames) != 0 {
		t.Fatalf("after clearing: %d %+v", code, after.SpeakerNames)
	}
	if code, _ = speakerAnswer(bridge, conv.ID, "speaker_1", "edit", "Sam Li"); code != http.StatusOK {
		t.Fatalf("naming again: %d", code)
	}

	// Paused: nothing is answered.
	app.closedEyes.enabled.Store(true)
	if code, _ := speakerAnswer(bridge, conv.ID, "speaker_1", "edit", "Lena"); code != http.StatusNotFound {
		t.Fatalf("answered while paused: %d", code)
	}
	app.closedEyes.enabled.Store(false)

	// Deleted means deleted: the hour takes the name.
	if _, err := app.deleteWatchRecording(store, first, ""); err != nil {
		t.Fatal(err)
	}
	if names := readWatchSpeakerNames(store.dir); len(names) != 0 {
		t.Fatalf("a name outlived its words: %+v", names)
	}
}
