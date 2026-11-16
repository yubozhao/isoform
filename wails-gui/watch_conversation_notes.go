package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"wails-gui/internal/pii"
)

// Notes for the longer conversations the watch heard: what it was about,
// what was decided, and what has to happen next. It is the after-call pass
// for meetings (meeting_followup.go: the same prompt, the same model seam,
// the same check of the answer), run on a conversation the watch heard in
// person. The follow-ups the owner has to do become commitments in the
// Purpose Map, so heard work turns into things they can judge.
//
// Behind the "watch-notes" switch (YANSU_FEATURES=watch-notes): off, nothing
// here runs and no model call is made.
//
// The rules of everything else from the watch hold:
//   - never while observing is paused;
//   - a transcript marked private is never read, so never sent;
//   - the notes are tied to the conversation's exact words. Delete an hour or
//     withdraw its transcript and the words change: the notes are dropped, and
//     the commitments made from them are purged from the Purpose Map
//     (purpose_purge.go). A commitment's source is the hour's own transcript,
//     so every reader that checks its source hides it the moment that hour is
//     gone, even before the purge runs.

const (
	// featureWatchNotes: notes and commitments from watch conversations.
	featureWatchNotes featureSwitch = "watch-notes"
	// watchConversationNotesMinimum: shorter conversations get no notes.
	watchConversationNotesMinimum = 5 * time.Minute
	watchConversationNotesFile    = "conversation-notes.json"
	// watchConversationNotesPerRun bounds the model calls one pass makes.
	watchConversationNotesPerRun = 3
)

// watchFollowupItemPrefix makes a follow-up's item id from its commitment's
// id, as the one list of work names it (the phone redesign's B2).
const watchFollowupItemPrefix = "followup:"

// WatchConversationFollowup is one thing someone has to do after a
// conversation. Owner is "me", "other" or "unknown".
type WatchConversationFollowup struct {
	Text   string `json:"text"`
	Owner  string `json:"owner"`
	Person string `json:"person"`
	Due    string `json:"due"`
}

// watchConversationNotesEntry is the kept notes of one conversation. Hash is
// of the conversation's words; LoopIDs are the commitments made from it.
type watchConversationNotesEntry struct {
	Hash         string                      `json:"hash"`
	StartAt      int64                       `json:"startAt,omitempty"`
	RecordingIDs []string                    `json:"recordingIds,omitempty"`
	Summary      string                      `json:"summary,omitempty"`
	Decisions    []string                    `json:"decisions,omitempty"`
	Followups    []WatchConversationFollowup `json:"followups,omitempty"`
	LoopIDs      []string                    `json:"loopIds,omitempty"`
}

var watchConversationNotesMu sync.Mutex

func readWatchConversationNotes(dir string) map[string]watchConversationNotesEntry {
	notes := map[string]watchConversationNotesEntry{}
	raw, err := os.ReadFile(filepath.Join(dir, watchConversationNotesFile))
	if err != nil {
		return notes
	}
	if err := json.Unmarshal(raw, &notes); err != nil {
		return map[string]watchConversationNotesEntry{}
	}
	return notes
}

func writeWatchConversationNotes(dir string, notes map[string]watchConversationNotesEntry) error {
	path := filepath.Join(dir, watchConversationNotesFile)
	if len(notes) == 0 {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	raw, err := json.Marshal(notes)
	if err != nil {
		return err
	}
	return mobileRecordingWriteFile(path, raw, 0o600)
}

// watchConversationNotesBody is the transcript the pass reads, in the form
// its prompt expects ("Me:" for the owner, "Other:" for everyone else),
// redacted and bounded like a meeting's.
func watchConversationNotesBody(c WatchConversation) string {
	var b strings.Builder
	for _, line := range c.Lines {
		switch {
		case line.Speaker == mobileRecordingSpeakerOwner:
			b.WriteString("Me: ")
		case line.Speaker != "":
			b.WriteString("Other: ")
		}
		b.WriteString(line.Text)
		b.WriteString("\n")
	}
	body, _ := pii.RedactText(b.String())
	words := strings.Fields(body)
	if len(words) <= meetingFollowupMaxWords {
		return body
	}
	return strings.Join(words[len(words)-meetingFollowupMaxWords:], " ")
}

// applyWatchConversationNotes sets the notes on the conversations whose kept
// notes were made from the words they have now.
func applyWatchConversationNotes(convs []WatchConversation, notes map[string]watchConversationNotesEntry) {
	for i := range convs {
		entry, ok := notes[convs[i].ID]
		if !ok || entry.Hash != watchConversationHash(convs[i]) {
			continue
		}
		convs[i].Summary = entry.Summary
		convs[i].Decisions = append([]string{}, entry.Decisions...)
		convs[i].Followups = append([]WatchConversationFollowup{}, entry.Followups...)
		for _, id := range entry.LoopIDs {
			convs[i].ItemIDs = append(convs[i].ItemIDs, watchFollowupItemPrefix+id)
		}
	}
}

// dismissWatchConversationCommitments closes the commitments made from notes
// that no longer stand: the words they came from are gone.
func (a *App) dismissWatchConversationCommitments(loopIDs []string) {
	if len(loopIDs) == 0 {
		return
	}
	store, err := a.purposeProductStore()
	if err != nil || store == nil {
		return
	}
	for _, id := range loopIDs {
		detail, err := store.GetDetail(id)
		if err != nil {
			continue
		}
		current := purposeCurrentLedgerEvent(detail)
		switch current.LifecycleState {
		case PurposeStateActive, PurposeStateWaiting, PurposeStateCandidate:
		default:
			continue
		}
		occurredAt := time.Now().UnixMilli()
		if occurredAt < current.OccurredAt {
			occurredAt = current.OccurredAt
		}
		key := "watch-notes-withdrawn:" + detail.OpenLoop.ID + ":" + current.ID
		if _, _, err := store.AppendEvent(AppendPurposeOpenLoopEventInput{
			OpenLoopID:       detail.OpenLoop.ID,
			ExpectedEventID:  current.ID,
			IdempotencyKey:   key,
			EventType:        PurposeEventDismissed,
			Title:            current.Title,
			DesiredOutcome:   current.DesiredOutcome,
			Owner:            current.Owner,
			Counterparty:     current.Counterparty,
			Scope:            current.Scope,
			DueKind:          current.DueKind,
			DueValue:         current.DueValue,
			WaitingCondition: current.WaitingCondition,
			ReasoningStatus:  PurposeReasoningUnknown,
			Confidence:       userPurposeConfidence,
			OccurredAt:       occurredAt,
			// Deleting the hour is the owner's own act.
			Evidence: []PurposeEvidenceInput{userPurposeEvidence(key, "watch", PurposeStanceDismiss, current.Title, occurredAt)},
		}); err != nil {
			log.Printf("[recordings] a commitment from a withdrawn conversation was not dismissed: %s", activityPrivateLogField("error", err.Error()))
		}
	}
}

// pruneWatchConversationNotes drops, without asking anything of a model, the
// kept notes that fit no conversation any more, and dismisses the commitments
// made from them. It returns the notes that still fit.
func (a *App) pruneWatchConversationNotes(dir string, convs []WatchConversation) map[string]watchConversationNotesEntry {
	watchConversationNotesMu.Lock()
	defer watchConversationNotesMu.Unlock()
	notes := readWatchConversationNotes(dir)
	if len(notes) == 0 {
		return notes
	}
	fits := map[string]string{}
	for _, c := range convs {
		fits[c.ID] = watchConversationHash(c)
	}
	kept := map[string]watchConversationNotesEntry{}
	for key, entry := range notes {
		if fits[key] == entry.Hash {
			kept[key] = entry
			continue
		}
		// Deleted means deleted: the commitments made from words that are
		// gone are purged from the Purpose Map. If that fails they are at
		// least dismissed, and their source hides them.
		if _, err := a.purgePurposeOpenLoops(entry.LoopIDs); err != nil {
			log.Printf("[recordings] commitments of a withdrawn conversation were not purged: %s", activityPrivateLogField("error", err.Error()))
			a.dismissWatchConversationCommitments(entry.LoopIDs)
		}
	}
	if len(kept) != len(notes) {
		if err := writeWatchConversationNotes(dir, kept); err != nil {
			log.Printf("[recordings] conversation notes not pruned: %v", err)
		}
	}
	return kept
}

// writeWatchConversationCommitments creates one commitment per follow-up the
// owner has to do. Its source is the transcript of the first hour the
// conversation draws from, so readers hide it when that hour is gone.
func (a *App) writeWatchConversationCommitments(c WatchConversation, title, body string, extraction meetingFollowupExtraction) []string {
	store, err := a.purposeProductStore()
	if err != nil || store == nil || len(c.RecordingIDs) == 0 {
		return nil
	}
	sum := sha256.Sum256([]byte(body))
	hash := hex.EncodeToString(sum[:])
	sessionID := "rec-" + c.RecordingIDs[0]
	var ids []string
	for i, f := range extraction.Followups {
		if f.Owner != meetingFollowupOwnerMe {
			continue
		}
		input := meetingFollowupCommitmentInput(sessionID, title, hash, i, f, time.UnixMilli(c.EndAt))
		if title == "" {
			input.Scope = "From a conversation your watch heard"
		} else {
			input.Scope = "From a conversation your watch heard: " + title
		}
		result, _, err := store.Create(input)
		if err != nil {
			log.Printf("[recordings] a follow-up was not kept as a commitment: %s", activityPrivateLogField("error", err.Error()))
			continue
		}
		ids = append(ids, result.OpenLoop.ID)
	}
	return ids
}

// noteWatchConversations writes the notes of each conversation that is long
// enough, is over, and has none that fit its words. It makes at most
// watchConversationNotesPerRun calls, and none with the switch off.
func (a *App) noteWatchConversations(ctx context.Context, store *mobileRecordingStore) {
	if a == nil || store == nil || !a.watchNotesOn() || a.IsClosedEyesModeEnabled() {
		return
	}
	convs, err := a.watchConversationsOf(store) // also prunes notes whose words are gone
	if err != nil {
		return
	}
	now := time.Now()
	calls := 0
	for _, c := range convs {
		if calls >= watchConversationNotesPerRun || ctx.Err() != nil || a.IsClosedEyesModeEnabled() {
			return
		}
		long := time.Duration(c.EndAt-c.StartAt)*time.Millisecond >= watchConversationNotesMinimum
		over := now.Sub(time.UnixMilli(c.EndAt)) >= watchConversationSettle
		hash := watchConversationHash(c)
		watchConversationNotesMu.Lock()
		_, done := readWatchConversationNotes(store.dir)[c.ID]
		watchConversationNotesMu.Unlock()
		if !long || !over || done {
			continue
		}
		calls++
		body := watchConversationNotesBody(c)
		if strings.TrimSpace(body) == "" {
			continue
		}
		raw, err := meetingFollowupModel(a, buildMeetingFollowupPrompt(c.Name, nil, body))
		if err != nil {
			log.Printf("[recordings] a conversation got no notes: %v", err)
			continue
		}
		extraction, err := parseMeetingFollowupJSON(raw)
		if err != nil {
			// Kept as "no notes for these words", so it is not asked again.
			extraction = meetingFollowupExtraction{}
		}
		if a.IsClosedEyesModeEnabled() {
			return
		}
		entry := watchConversationNotesEntry{Hash: hash, StartAt: c.StartAt, RecordingIDs: c.RecordingIDs,
			Summary: strings.TrimSpace(extraction.Summary), Decisions: extraction.Decisions}
		for _, f := range extraction.Followups {
			entry.Followups = append(entry.Followups, WatchConversationFollowup{Text: f.Text, Owner: f.Owner, Person: f.Person, Due: f.Due})
		}
		entry.LoopIDs = a.writeWatchConversationCommitments(c, c.Name, body, extraction)
		watchConversationNotesMu.Lock()
		notes := readWatchConversationNotes(store.dir)
		notes[c.ID] = entry
		err = writeWatchConversationNotes(store.dir, notes)
		watchConversationNotesMu.Unlock()
		if err != nil {
			log.Printf("[recordings] conversation notes not kept: %v", err)
			a.dismissWatchConversationCommitments(entry.LoopIDs)
		}
	}
}
