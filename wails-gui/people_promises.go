package main

import (
	"context"
	"errors"
	"log"
	"sort"
	"strings"
	"time"

	"wails-gui/storage"
)

// Who you know (redesign-you v1.0.0 rules 16 to 18): the promises between you
// and the people you know, each answered under "Still open?", and the voices
// Isoform knows from conversations where you named a speaker.
//
// A promise is a Purpose map commitment with a counterparty. Answering it
// moves the loop through the same user path every goal move takes
// (moveUserPurpose), so Today's list, the brief and the phone rebuild from
// the Purpose map as they do after any other move. Nothing here edits
// Today's caches.

const (
	personLoopSourcePurpose = "purpose"

	answerKindPromise    = "promise"
	answerSourcePeople   = "people"
	personPromiseItemPfx = "loop:"

	// The reasons a No on a promise may carry (who.reasonDone, who.reasonNot).
	personPromiseReasonDone = "already_done"
	personPromiseReasonNot  = "not_a_promise"
)

var (
	errPersonPromiseInvalid = errors.New("promise_invalid")
	errPersonPromiseChanged = errors.New("promise_changed")
	errPersonPromiseGone    = errors.New("promise_unavailable")
)

// PersonPromiseAnswer is one answer to a promise: yes, no (with an optional
// reason), not_mine, or edit (new words and, with SetDue, a new date).
type PersonPromiseAnswer struct {
	LoopID          string `json:"loopId"`
	ExpectedEventID string `json:"expectedEventId,omitempty"`
	PersonID        string `json:"personId,omitempty"`
	Answer          string `json:"answer"`
	Reason          string `json:"reason,omitempty"`
	Title           string `json:"title,omitempty"`
	// DueDate is YYYY-MM-DD, empty for none; it applies only with SetDue.
	DueDate string `json:"dueDate,omitempty"`
	SetDue  bool   `json:"setDue,omitempty"`
	Key     string `json:"key"`
}

// PersonPromiseResult is the promise after the answer. Open is false once it
// left Open between you (done, not a promise, someone else's).
type PersonPromiseResult struct {
	LoopID  string `json:"loopId"`
	EventID string `json:"eventId"`
	Title   string `json:"title"`
	Open    bool   `json:"open"`
	// FollowUpDrafted is false until Today can draft a follow-up from a
	// promise: Yes on one you owe records the answer only.
	FollowUpDrafted bool `json:"followUpDrafted"`
}

// AnswerPersonPromise records the answer and moves the promise.
func (a *App) AnswerPersonPromise(input PersonPromiseAnswer) (PersonPromiseResult, error) {
	return a.answerPersonPromise(input, time.Now())
}

// personPromiseAction is the Purpose move an answer makes; "" moves nothing.
func personPromiseAction(answer, reason, state string) (string, error) {
	switch answer {
	case workActionYes:
		// Still open: a kept promise is reinforced; a candidate stays as it is.
		if state == PurposeStateActive || state == PurposeStateWaiting {
			return userPurposeKeep, nil
		}
		return "", nil
	case workActionNo:
		switch reason {
		case "", personPromiseReasonDone:
			if state == PurposeStateActive || state == PurposeStateWaiting {
				return userPurposeResolve, nil
			}
			// A candidate was never kept, so done closes it the only way it can.
			return userPurposeForget, nil
		case personPromiseReasonNot:
			return userPurposeForget, nil
		}
		return "", errPersonPromiseInvalid
	case workActionNotMine:
		return userPurposeForget, nil
	case workActionEdit:
		return userPurposeRevise, nil
	}
	return "", errPersonPromiseInvalid
}

func (a *App) answerPersonPromise(input PersonPromiseAnswer, now time.Time) (PersonPromiseResult, error) {
	key := strings.TrimSpace(input.Key)
	loopID := strings.TrimSpace(input.LoopID)
	if key == "" || len(key) > 128 || !mobileValidID(loopID) {
		return PersonPromiseResult{}, errPersonPromiseInvalid
	}
	detail, err := a.userPurposeDetail(loopID)
	if err != nil {
		return PersonPromiseResult{}, errPersonPromiseGone
	}
	if detail.OpenLoop.Kind != PurposeKindCommitment {
		return PersonPromiseResult{}, errPersonPromiseInvalid
	}
	current := purposeCurrentLedgerEvent(detail)
	action, err := personPromiseAction(input.Answer, input.Reason, current.LifecycleState)
	if err != nil {
		return PersonPromiseResult{}, err
	}
	if action == userPurposeRevise && strings.TrimSpace(input.Title) == "" {
		return PersonPromiseResult{}, errPersonPromiseInvalid
	}
	if action != "" {
		// A promise move changes what a sealed brief may say, as a goal move does.
		defer briefPrivacyChanged()
		title, due := input.Title, input.DueDate
		if action != userPurposeRevise {
			title, due = "", ""
		}
		detail, err = a.moveUserPurpose(userPurposeTransition{
			Key: "people:" + key, Origin: "computer", OpenLoopID: loopID,
			ExpectedEventID: input.ExpectedEventID, Action: action,
			Title: title, DueDate: due, SetDue: action == userPurposeRevise && input.SetDue,
		}, now)
		if err != nil {
			switch {
			case errors.Is(err, errUserPurposeChanged):
				return PersonPromiseResult{}, errPersonPromiseChanged
			case errors.Is(err, errUserPurposeEdit), errors.Is(err, errUserPurposeState):
				return PersonPromiseResult{}, errPersonPromiseInvalid
			}
			log.Printf("[people] promise answer failed: %v", err)
			return PersonPromiseResult{}, errPersonPromiseGone
		}
		a.personPromiseMoved()
	}
	current = purposeCurrentLedgerEvent(detail)
	record := AnswerRecord{ItemID: personPromiseItemPfx + loopID, Kind: answerKindPromise, Source: answerSourcePeople,
		Title: current.Title, Action: input.Answer, Surface: workSurfaceComputer}
	if input.Answer == workActionNo {
		record.Reason = input.Reason
	}
	if _, err := a.recordAnswer(record); err != nil {
		// The move stands; a lost record is logged, never a failed answer.
		log.Printf("[people] promise answer not recorded: %v", err)
	}
	open := current.LifecycleState == PurposeStateActive || current.LifecycleState == PurposeStateWaiting || current.LifecycleState == PurposeStateCandidate
	return PersonPromiseResult{LoopID: loopID, EventID: current.ID, Title: current.Title, Open: open}, nil
}

// personPromiseMoved publishes a promise move the way a goal move does: the
// Purpose map changed (agency:updated), and the views built from it, Who you
// know and Today's list, rebuild now instead of at their next hour.
func (a *App) personPromiseMoved() {
	a.emitBrowserEvent("agency:updated")
	a.touchDerivedViews(derivedPeopleOverview, derivedWorkList)
}

// personPromiseWhere says where a promise was heard, from its first evidence:
// a meeting or call, mail, a conversation on the watch, Chat, Activity, or
// you (added by hand). "" when the source has no plain name.
func personPromiseWhere(detail PurposeOpenLoopDetail) (string, int64) {
	evidence := append([]PurposeEvidenceRecord{}, detail.Evidence...)
	sort.SliceStable(evidence, func(i, j int) bool { return evidence[i].SourceObservedAt < evidence[j].SourceObservedAt })
	for _, e := range evidence {
		kind := strings.ToLower(e.SourceKind)
		at := e.SourceObservedAt
		if at == 0 {
			at = detail.OpenLoop.CreatedAt
		}
		switch {
		case kind == "meeting" || kind == "calendar" || strings.Contains(kind, "call"):
			return "meeting", at
		case strings.Contains(kind, "mail"):
			return "mail", at
		case strings.Contains(kind, "watch") || strings.Contains(kind, "recording") || strings.Contains(kind, "conversation"):
			return "conversation", at
		case strings.Contains(kind, "chat") || kind == "history":
			return "chat", at
		case strings.HasPrefix(kind, "activity"):
			return "activity", at
		case kind == userPurposeSourceKind || kind == "explicit_user_statement" || kind == "user_statement" || kind == reminderPurposeSourceKind:
			return "you", at
		}
	}
	return "", detail.OpenLoop.CreatedAt
}

// PersonVoice is what Isoform knows of someone's voice: the conversations on
// this computer where you named one of the speakers as them (watch-voice
// v1.0.0; RY.18). Nothing of it leaves the computer.
type PersonVoice struct {
	Conversations []PersonVoiceConversation `json:"conversations"`
	// Samples are stored voiceprint samples for the person, if any.
	Samples int `json:"samples"`
}

// PersonVoiceConversation is one conversation where you named them.
type PersonVoiceConversation struct {
	ID      string `json:"id"`
	StartAt int64  `json:"startAt"`
}

// GetPersonVoice reads the conversations where you named a speaker as the
// person. Only names you gave count, never a guess.
func (a *App) GetPersonVoice(personID string) (PersonVoice, error) {
	out := PersonVoice{Conversations: []PersonVoiceConversation{}}
	store, err := a.requireStore()
	if err != nil {
		return out, err
	}
	ctx := context.Background()
	p, err := store.GetPrincipal(ctx, personID)
	if err != nil {
		return out, err
	}
	if a.IsClosedEyesModeEnabled() || p.Kind == storage.PrincipalKindSelf {
		return out, nil
	}
	names := personNames(p.DisplayName, p.Aliases)
	if dir := a.personVoiceDir(); dir != "" {
		watchSpeakerNamesMu.Lock()
		entries := readWatchSpeakerNames(dir)
		watchSpeakerNamesMu.Unlock()
		out.Conversations = personVoiceConversations(entries, names)
	}
	samples, err := store.ListVoiceEmbeddingsByPrincipal(ctx, p.ID)
	if err != nil {
		return out, err
	}
	out.Samples = len(samples)
	return out, nil
}

// ForgetPersonVoice takes back every speaker naming of the person and any
// voiceprint stored for them, so their next conversation shows Speaker 1
// again. The conversations' words are untouched. It answers how many
// conversations it changed.
func (a *App) ForgetPersonVoice(personID string) (int, error) {
	store, err := a.requireStore()
	if err != nil {
		return 0, err
	}
	ctx := context.Background()
	p, err := store.GetPrincipal(ctx, personID)
	if err != nil {
		return 0, err
	}
	if p.Kind == storage.PrincipalKindSelf {
		// Your own voice print has its own place and its own delete.
		return 0, errPersonPromiseInvalid
	}
	names := personNames(p.DisplayName, p.Aliases)
	changed := 0
	if dir := a.personVoiceDir(); dir != "" {
		changed, err = forgetPersonVoiceNames(dir, names)
		if err != nil {
			return 0, err
		}
	}
	samples, err := store.ListVoiceEmbeddingsByPrincipal(ctx, p.ID)
	if err != nil {
		return changed, err
	}
	for _, sample := range samples {
		if err := store.DeleteVoiceEmbedding(ctx, sample.ID); err != nil {
			return changed, err
		}
	}
	a.touchDerivedViews(derivedPeopleOverview)
	return changed, nil
}

// personVoiceDir is the recordings folder that holds the speaker names; a
// variable so tests need no paired phone.
var personVoiceDirOf = func(a *App) string {
	if store := a.watchRecordingStore(); store != nil {
		return store.dir
	}
	return ""
}

func (a *App) personVoiceDir() string { return personVoiceDirOf(a) }

func personVoiceConversations(entries map[string]watchSpeakerNamesEntry, names map[string]bool) []PersonVoiceConversation {
	out := []PersonVoiceConversation{}
	for id, entry := range entries {
		for _, name := range entry.Names {
			if names[threadNorm(name)] {
				out = append(out, PersonVoiceConversation{ID: id, StartAt: entry.StartAt})
				break
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].StartAt != out[j].StartAt {
			return out[i].StartAt > out[j].StartAt
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func forgetPersonVoiceNames(dir string, names map[string]bool) (int, error) {
	watchSpeakerNamesMu.Lock()
	defer watchSpeakerNamesMu.Unlock()
	entries := readWatchSpeakerNames(dir)
	changed := 0
	for id, entry := range entries {
		touched := false
		for speaker, name := range entry.Names {
			if names[threadNorm(name)] {
				delete(entry.Names, speaker)
				touched = true
			}
		}
		if touched {
			changed++
			entries[id] = entry
		}
	}
	if changed == 0 {
		return 0, nil
	}
	return changed, writeWatchSpeakerNames(dir, entries)
}
