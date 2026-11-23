package main

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func peoplePromiseLoop(t *testing.T, a *App, personID, loopID string) PersonLoop {
	t.Helper()
	detail, err := a.getPersonDetail(personID, newPeopleReadGateCache())
	if err != nil {
		t.Fatal(err)
	}
	for _, loop := range detail.Loops {
		if loop.ID == loopID {
			return loop
		}
	}
	return PersonLoop{}
}

func peoplePromiseAnswers(a *App, itemID string) []AnswerRecord {
	var out []AnswerRecord
	for _, record := range a.answersLedger().entries() {
		if record.ItemID == itemID {
			out = append(out, record)
		}
	}
	return out
}

// A promise on a person carries what Still open? needs: its Purpose source,
// the event to expect, and where it was heard.
func TestPersonPromiseLoopCarriesSourceAndEvent(t *testing.T) {
	a := newTodayContextTestApp(t, true)
	person := peopleTestContact(t, a, "Lena")
	id := peopleTestPurpose(t, a, "Send the import test plan", person.DisplayName, PurposeOwnerUser, time.Time{})
	loop := peoplePromiseLoop(t, a, person.ID, id)
	if loop.Kind != "you_owe" || loop.Source != personLoopSourcePurpose || loop.EventID == "" || loop.Where != "you" {
		t.Fatalf("loop=%+v", loop)
	}
}

// Each answer moves the promise through the user Purpose path and records a
// promise answer for Your answers.
func TestAnswerPersonPromiseMovesAndRecords(t *testing.T) {
	a := newTodayContextTestApp(t, true)
	person := peopleTestContact(t, a, "Lena")
	cases := []struct {
		answer, reason string
		open           bool
		state          string
	}{
		{answer: "yes", open: true, state: PurposeStateActive},
		{answer: "no", reason: personPromiseReasonDone, state: PurposeStateResolved},
		{answer: "no", reason: personPromiseReasonNot, state: PurposeStateDismissed},
		{answer: "not_mine", state: PurposeStateDismissed},
	}
	for i, c := range cases {
		title := "promise " + c.answer + c.reason
		id := peopleTestPurpose(t, a, title, person.DisplayName, PurposeOwnerUser, time.Time{})
		loop := peoplePromiseLoop(t, a, person.ID, id)
		got, err := a.AnswerPersonPromise(PersonPromiseAnswer{LoopID: id, ExpectedEventID: loop.EventID, PersonID: person.ID, Answer: c.answer, Reason: c.reason, Key: "k" + string(rune('a'+i))})
		if err != nil || got.Open != c.open || got.FollowUpDrafted {
			t.Fatalf("%s: %+v %v", title, got, err)
		}
		detail, err := a.userPurposeDetail(id)
		if err != nil || purposeCurrentLedgerEvent(detail).LifecycleState != c.state {
			t.Fatalf("%s: state %q %v", title, purposeCurrentLedgerEvent(detail).LifecycleState, err)
		}
		records := peoplePromiseAnswers(a, personPromiseItemPfx+id)
		if len(records) != 1 || records[0].Kind != answerKindPromise || records[0].Source != answerSourcePeople || records[0].Action != c.answer || records[0].Reason != c.reason {
			t.Fatalf("%s: records %+v", title, records)
		}
		if closed := peoplePromiseLoop(t, a, person.ID, id); c.open == (closed.ID == "") {
			t.Fatalf("%s: still listed=%v", title, closed.ID != "")
		}
	}
}

// Edit changes the words and the date through the Purpose revise path.
func TestAnswerPersonPromiseEdit(t *testing.T) {
	a := newTodayContextTestApp(t, true)
	person := peopleTestContact(t, a, "Sam")
	id := peopleTestPurpose(t, a, "Send the contract", person.DisplayName, PurposeOwnerOther, time.Time{})
	loop := peoplePromiseLoop(t, a, person.ID, id)
	if loop.Kind != "waiting_on_them" {
		t.Fatalf("loop=%+v", loop)
	}
	got, err := a.AnswerPersonPromise(PersonPromiseAnswer{LoopID: id, ExpectedEventID: loop.EventID, Answer: "edit", Title: "Send the signed contract", DueDate: "2030-01-15", SetDue: true, Key: "edit"})
	if err != nil || !got.Open || got.Title != "Send the signed contract" {
		t.Fatalf("%+v %v", got, err)
	}
	edited := peoplePromiseLoop(t, a, person.ID, id)
	if edited.Title != "Send the signed contract" || edited.DueAt == 0 {
		t.Fatalf("edited=%+v", edited)
	}
	if _, err := a.AnswerPersonPromise(PersonPromiseAnswer{LoopID: id, Answer: "edit", Title: "  ", Key: "blank"}); !errors.Is(err, errPersonPromiseInvalid) {
		t.Fatalf("blank edit: %v", err)
	}
}

// A promise that moved since it was shown is not answered over.
func TestAnswerPersonPromiseRefusesChangedAndUnknown(t *testing.T) {
	a := newTodayContextTestApp(t, true)
	person := peopleTestContact(t, a, "Lena")
	id := peopleTestPurpose(t, a, "Call back", person.DisplayName, PurposeOwnerUser, time.Time{})
	if _, err := a.AnswerPersonPromise(PersonPromiseAnswer{LoopID: id, ExpectedEventID: "stale", Answer: "no", Key: "x"}); !errors.Is(err, errPersonPromiseChanged) {
		t.Fatalf("stale: %v", err)
	}
	if _, err := a.AnswerPersonPromise(PersonPromiseAnswer{LoopID: id, Answer: "maybe", Key: "y"}); !errors.Is(err, errPersonPromiseInvalid) {
		t.Fatalf("unknown answer: %v", err)
	}
	if _, err := a.AnswerPersonPromise(PersonPromiseAnswer{LoopID: "missing-loop", Answer: "no", Key: "z"}); !errors.Is(err, errPersonPromiseGone) {
		t.Fatalf("missing: %v", err)
	}
	if records := peoplePromiseAnswers(a, personPromiseItemPfx+id); len(records) != 0 {
		t.Fatalf("refused answers recorded: %+v", records)
	}
}

// Closing a promise here takes the path every goal move takes: the Purpose
// map's agency:updated, and Today's work list (with Who you know) rebuilt
// from it, which publishes work:updated when it changed. Nothing here edits
// Today's state.
func TestAnswerPersonPromiseRebuildsWorkListFromPurpose(t *testing.T) {
	a := newTodayContextTestApp(t, true)
	person := peopleTestContact(t, a, "Lena")
	id := peopleTestPurpose(t, a, "Send the plan", person.DisplayName, PurposeOwnerUser, time.Time{})
	views := a.derivedViews()
	work, people := views.touches[derivedWorkList].Load(), views.touches[derivedPeopleOverview].Load()
	if _, err := a.AnswerPersonPromise(PersonPromiseAnswer{LoopID: id, Answer: "no", Reason: personPromiseReasonDone, Key: "close"}); err != nil {
		t.Fatal(err)
	}
	if views.touches[derivedWorkList].Load() != work+1 || views.touches[derivedPeopleOverview].Load() != people+1 {
		t.Fatal("closing a promise did not rebuild the work list and Who you know")
	}
	if derivedViewSpecs[derivedWorkList].event != workUpdatedEvent {
		t.Fatal("the work list no longer publishes work:updated")
	}
}

func TestPersonVoiceCountsNamedConversationsAndForgets(t *testing.T) {
	a := newTodayContextTestApp(t, true)
	person := peopleTestContact(t, a, "Lena Park", "Lena")
	other := peopleTestContact(t, a, "Sam")
	dir := t.TempDir()
	prior := personVoiceDirOf
	personVoiceDirOf = func(*App) string { return dir }
	t.Cleanup(func() { personVoiceDirOf = prior })
	entries := map[string]watchSpeakerNamesEntry{
		"c1": {Hash: "h1", StartAt: 100, Names: map[string]string{"speaker_1": "Lena"}},
		"c2": {Hash: "h2", StartAt: 200, Names: map[string]string{"speaker_1": "lena park", "speaker_2": "Sam"}},
		"c3": {Hash: "h3", StartAt: 300, Names: map[string]string{"speaker_1": "Sam"}},
	}
	if err := writeWatchSpeakerNames(dir, entries); err != nil {
		t.Fatal(err)
	}
	voice, err := a.GetPersonVoice(person.ID)
	if err != nil || len(voice.Conversations) != 2 || voice.Conversations[0].ID != "c2" || voice.Conversations[1].ID != "c1" {
		t.Fatalf("voice=%+v %v", voice, err)
	}
	changed, err := a.ForgetPersonVoice(person.ID)
	if err != nil || changed != 2 {
		t.Fatalf("forget changed=%d %v", changed, err)
	}
	after := readWatchSpeakerNames(dir)
	if after["c1"].Names["speaker_1"] != "" || after["c2"].Names["speaker_1"] != "" || after["c2"].Names["speaker_2"] != "Sam" || after["c3"].Names["speaker_1"] != "Sam" {
		t.Fatalf("names after forget=%+v", after)
	}
	if after["c2"].Hash != "h2" {
		t.Fatal("forget touched the conversation it named")
	}
	if voice, err := a.GetPersonVoice(person.ID); err != nil || len(voice.Conversations) != 0 {
		t.Fatalf("after forget=%+v %v", voice, err)
	}
	if voice, err := a.GetPersonVoice(other.ID); err != nil || len(voice.Conversations) != 2 {
		t.Fatalf("other=%+v %v", voice, err)
	}
}

// A Chat correction about a person is in their detail as its own read-only
// note, never in the directory row the editor saves, and goes with Undo.
func TestPersonDetailChatNotesShownAndGoneOnUndo(t *testing.T) {
	a := newTodayContextTestApp(t, true)
	person := peopleTestContact(t, a, "Mei Chen")
	c := correctionTestTool(t, a, nil, promptOriginMac)
	id, p := submitTestCorrection(t, c, map[string]any{"kind": "person", "id": person.ID})
	detail, err := a.GetPersonDetail(person.ID)
	if err != nil || len(detail.ChatNotes) != 1 || !strings.Contains(detail.ChatNotes[0].Text, p.Statement) {
		t.Fatalf("chat notes=%+v %v", detail.ChatNotes, err)
	}
	stored, err := a.store.GetPrincipal(t.Context(), person.ID)
	if err != nil || strings.Contains(stored.Notes, p.Statement) || strings.Contains(principalToDTO(stored).Notes, p.Statement) {
		t.Fatalf("the correction reached the directory row: %v", err)
	}
	if _, err := a.UndoChatCorrection(id); err != nil {
		t.Fatal(err)
	}
	detail, err = a.GetPersonDetail(person.ID)
	if err != nil || len(detail.ChatNotes) != 0 {
		t.Fatalf("after Undo=%+v %v", detail.ChatNotes, err)
	}
}
