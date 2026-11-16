package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// The answers ledger (design A37; PROTOCOL.md section 26.2): every judgment
// the person gives, on every surface, kept in one local file. Your work's
// Yes, No with a reason, Not mine, Edit, Useful and Not useful from the
// computer, the phone and notifications, About you's That's me and Not quite,
// and chat corrections. It is the thesis's most valuable data, so it is
// append-only (an Undo is its own entry naming the one it takes back),
// written through to disk on every answer, kept only on this computer (mode
// 0600) and shown back to the person in Your data.

// AnswerRecord is one entry in the ledger.
type AnswerRecord struct {
	V int `json:"v"`
	// EntryID names this entry; an undo names the entry it takes back.
	EntryID string `json:"id"`
	// ItemID is what was answered: a Your work id, belief:<memoryId>, portrait,
	// or correction:<id>.
	ItemID string `json:"item"`
	// Kind is work, belief, portrait or correction.
	Kind string `json:"kind"`
	// Title is what the item said when it was answered, for Your answers.
	Title     string      `json:"title,omitempty"`
	Action    string      `json:"action"`
	Reason    string      `json:"reason,omitempty"`
	Note      string      `json:"note,omitempty"`
	Edited    *WorkEdited `json:"edited,omitempty"`
	At        string      `json:"at"`
	Surface   string      `json:"surface"`
	DeviceID  string      `json:"deviceId,omitempty"`
	Seq       uint64      `json:"seq,omitempty"`
	TaskID    string      `json:"taskId,omitempty"`
	UndoUntil string      `json:"undoUntil,omitempty"`
	Undoes    string      `json:"undoes,omitempty"`
}

const (
	answerKindWork       = "work"
	answerKindBelief     = "belief"
	answerKindPortrait   = "portrait"
	answerKindCorrection = "correction"

	answersLedgerDirName  = "answers"
	answersLedgerFileName = "ledger.jsonl"
	answersTitleMaxRunes  = 160
	answersPageMax        = 200
)

type answersLedgerStore struct {
	mu   sync.Mutex
	path string
}

var answersLedgers sync.Map // *App -> *answersLedgerStore

func (a *App) answersLedger() *answersLedgerStore {
	if s, ok := answersLedgers.Load(a); ok {
		return s.(*answersLedgerStore)
	}
	path := ""
	if root, err := flavorDataDir(); err == nil {
		path = filepath.Join(root, answersLedgerDirName, answersLedgerFileName)
	}
	s, _ := answersLedgers.LoadOrStore(a, &answersLedgerStore{path: path})
	return s.(*answersLedgerStore)
}

// append writes one entry at the end of the ledger and syncs it to disk.
func (s *answersLedgerStore) append(record AnswerRecord) (AnswerRecord, error) {
	if s == nil || s.path == "" {
		return record, errMobileStoreUnavailable
	}
	record.V = 1
	if record.EntryID == "" {
		record.EntryID = uuid.NewString()
	}
	if record.At == "" {
		record.At = mobileTimestamp(time.Now())
	}
	record.Title = answersBound(record.Title)
	line, err := json.Marshal(record)
	if err != nil {
		return record, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return record, err
	}
	file, err := os.OpenFile(s.path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return record, err
	}
	if _, err := file.Write(append(line, '\n')); err != nil {
		_ = file.Close()
		return record, err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return record, err
	}
	return record, file.Close()
}

// entries reads the ledger, oldest first. A line that cannot be read (a
// torn last write) is skipped, never repaired in place.
func (s *answersLedgerStore) entries() []AnswerRecord {
	if s == nil || s.path == "" {
		return nil
	}
	s.mu.Lock()
	raw, err := os.ReadFile(s.path)
	s.mu.Unlock()
	if err != nil {
		return nil
	}
	out := []AnswerRecord{}
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		var record AnswerRecord
		if json.Unmarshal(scanner.Bytes(), &record) == nil && record.EntryID != "" && record.ItemID != "" {
			out = append(out, record)
		}
	}
	return out
}

// answersUndone lists the entries an undo took back.
func answersUndone(entries []AnswerRecord) map[string]bool {
	undone := map[string]bool{}
	for _, record := range entries {
		if record.Action == workActionUndo && record.Undoes != "" {
			undone[record.Undoes] = true
		}
	}
	return undone
}

// answersStanding is each item's newest answer that wasn't undone. A result's
// Useful keeps the task the Yes before it started.
func answersStanding(entries []AnswerRecord) map[string]AnswerRecord {
	undone := answersUndone(entries)
	out := map[string]AnswerRecord{}
	for _, record := range entries {
		if record.Action == workActionUndo || undone[record.EntryID] {
			continue
		}
		previous, seen := out[record.ItemID]
		if seen && workResultJudgment(record.Action) && !workResultJudgment(previous.Action) {
			record.TaskID = previous.TaskID
		}
		out[record.ItemID] = record
	}
	// An undone answer takes the item back to the answer before it.
	return out
}

func (s *answersLedgerStore) current() map[string]AnswerRecord {
	return answersStanding(s.entries())
}

// recordAnswer adds an answer given outside Your work (About you, chat
// corrections). Failures are logged by the caller's own path and never block
// the answer itself.
func (a *App) recordAnswer(record AnswerRecord) (AnswerRecord, error) {
	return a.answersLedger().append(record)
}

// undoAnswerFor records an Undo of the item's last standing answer.
func (a *App) undoAnswerFor(itemID, kind, surface string) error {
	ledger := a.answersLedger()
	standing, ok := answersStanding(ledger.entries())[itemID]
	if !ok {
		return nil
	}
	_, err := ledger.append(AnswerRecord{ItemID: itemID, Kind: kind, Title: standing.Title, Action: workActionUndo,
		Undoes: standing.EntryID, Surface: surface})
	return err
}

// AnswerView is one answer as Your data shows it.
type AnswerView struct {
	AnswerRecord
	Undone bool `json:"undone,omitempty"`
}

// AnswersPage is a page of the ledger, newest first. Next is the cursor for
// the page after it.
type AnswersPage struct {
	Answers []AnswerView `json:"answers"`
	Next    string       `json:"next,omitempty"`
}

// GetAnswers reads the ledger for Your data and Your answers: answers newest
// first, each marked when an Undo took it back. Before is the id of the last
// answer of the previous page.
func (a *App) GetAnswers(limit int, before string) (AnswersPage, error) {
	if limit <= 0 || limit > answersPageMax {
		limit = 50
	}
	entries := a.answersLedger().entries()
	undone := answersUndone(entries)
	page := AnswersPage{Answers: []AnswerView{}}
	started := before == ""
	for i := len(entries) - 1; i >= 0; i-- {
		record := entries[i]
		if !started {
			started = record.EntryID == before
			continue
		}
		if record.Action == workActionUndo {
			continue
		}
		if len(page.Answers) == limit {
			page.Next = page.Answers[len(page.Answers)-1].EntryID
			break
		}
		page.Answers = append(page.Answers, AnswerView{AnswerRecord: record, Undone: undone[record.EntryID]})
	}
	return page, nil
}

func answersBound(text string) string {
	text = strings.TrimSpace(text)
	runes := []rune(text)
	if len(runes) > answersTitleMaxRunes {
		return string(runes[:answersTitleMaxRunes])
	}
	return text
}
