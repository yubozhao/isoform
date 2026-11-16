package main

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"

	"wails-gui/internal/privacy"
	"wails-gui/internal/topicday"
)

// answers_delete.go is Settings › Your data › Delete your answers
// (computer-data v1.0.0 rule 4, hub decision of 2026-11-15): the answers
// ledger goes, with everything learned from it, and each Your work item keeps
// the state it shows now, without its Undo.
//
// What goes, each through its own store:
//   - the answers ledger (answers/ledger.jsonl): every answer, Undo and shown mark;
//   - the daily measures' answered, accepted and brief-acted counts (judgments/);
//   - the topics' Yes and No answers, which choose and rank the next proposals.
//
// What stays: a topic's Not mine (it keeps a topic the person said isn't
// theirs away, a privacy choice, not a preference), and each item's state,
// kept as its action alone in answers/settled.json, so nothing answered
// reopens. A withdrawal for SourceKind "answers" then tells every reader,
// the brief among them, to drop its copies.

const answersSettledFileName = "settled.json"

// answersSourceKind names the answers in a privacy withdrawal.
const answersSourceKind = "answers"

// settledAnswer is what an item keeps of its answer after Delete: the action
// it shows and the task a Yes started, nothing the person wrote.
type settledAnswer struct {
	Action string `json:"action"`
	TaskID string `json:"taskId,omitempty"`
}

func (s *answersLedgerStore) settledPath() string {
	if s == nil || s.path == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(s.path), answersSettledFileName)
}

// settled reads the items' kept states; a missing file is none.
func (s *answersLedgerStore) settled() map[string]settledAnswer {
	out := map[string]settledAnswer{}
	path := s.settledPath()
	if path == "" {
		return out
	}
	if raw, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(raw, &out)
	}
	return out
}

// standing is each item's answer that stands: the ledger's, or the state an
// item kept through Delete. A kept state has no Undo.
func (s *answersLedgerStore) standing() map[string]AnswerRecord {
	out := s.current()
	for id, kept := range s.settled() {
		if _, ok := out[id]; !ok {
			out[id] = AnswerRecord{ItemID: id, Action: kept.Action, TaskID: kept.TaskID}
		}
	}
	return out
}

// clear empties the ledger. The items' states are saved first, so a stop in
// between leaves the ledger whole and Delete can run again.
func (s *answersLedgerStore) clear() error {
	if s == nil || s.path == "" {
		return errMobileStoreUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entries := s.entriesLocked()
	kept := s.settled()
	for id, record := range answersStanding(entries) {
		kept[id] = settledAnswer{Action: record.Action, TaskID: record.TaskID}
	}
	raw, err := json.Marshal(kept)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	path := s.settledPath()
	if err := os.WriteFile(path+".tmp", raw, 0o600); err != nil {
		return err
	}
	if err := os.Rename(path+".tmp", path); err != nil {
		return err
	}
	if len(entries) == 0 {
		return nil
	}
	if err := s.writeLocked(nil); err != nil {
		return err
	}
	s.shown = nil
	return nil
}

// DeleteAnswers is Your data's Delete your answers.
func (a *App) DeleteAnswers() error {
	if err := a.answersLedger().clear(); err != nil {
		return err
	}
	if err := clearJudgmentAnswers(); err != nil {
		log.Printf("[answers] daily measures not cleared: %v", err)
		return err
	}
	if store, err := a.topicsDailyStore(); err == nil {
		// Not mine keeps only the topic it names, not the proposal answered.
		notMine := func(f topicday.Feedback) (topicday.Feedback, bool) {
			return topicday.Feedback{TopicID: f.TopicID, TopicName: f.TopicName, Verdict: f.Verdict, At: f.At}, f.Verdict == topicday.VerdictNotMine
		}
		if err := store.KeepFeedback(notMine); err != nil {
			return err
		}
	}
	publishPrivacyWithdrawal(privacy.Scope{Kind: privacy.Source, SourceKind: answersSourceKind})
	a.emitBrowserEvent(workAnsweredEvent, "")
	log.Printf("[answers] deleted: the ledger, the answered measures and the topic answers")
	return nil
}
