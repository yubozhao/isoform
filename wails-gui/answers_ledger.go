package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"log"
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
	// Source is where the item came from: brief, topic, watch, prep, agent
	// (a result back), about_you or chat.
	Source string `json:"source,omitempty"`
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
	Removed   []string    `json:"removed,omitempty"`
	AgentID   string      `json:"agentId,omitempty"`
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

	// answerActionShown marks the first time an item was put in front of the
	// person, on its surface. It is a measure, never an answer.
	answerActionShown = "shown"

	answerSourceAboutYou = "about_you"
	answerSourceChat     = "chat"
	answerSourceOther    = "other"
)

type answersLedgerStore struct {
	mu   sync.Mutex
	path string
	// shown holds the items already recorded as shown, read from the ledger
	// once; guarded by mu.
	shown map[string]bool
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
	defer s.mu.Unlock()
	return s.entriesLocked()
}

// entriesLocked reads the ledger; the caller holds mu.
func (s *answersLedgerStore) entriesLocked() []AnswerRecord {
	raw, err := os.ReadFile(s.path)
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
		if record.Action == workActionUndo || record.Action == answerActionShown || undone[record.EntryID] {
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
		if record.Action == workActionUndo || record.Action == answerActionShown {
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

// noteWorkShown records each item of a list the first time it is put in front
// of the person, with its source and the surface it appeared on.
func (a *App) noteWorkShown(list WorkList, surface string) {
	ledger := a.answersLedger()
	if ledger.path == "" {
		return
	}
	ledger.mu.Lock()
	if ledger.shown == nil {
		ledger.mu.Unlock()
		shown := map[string]bool{}
		for _, record := range ledger.entries() {
			if record.Action == answerActionShown {
				shown[record.ItemID] = true
			}
		}
		ledger.mu.Lock()
		if ledger.shown == nil {
			ledger.shown = shown
		}
	}
	fresh := []WorkItem{}
	for _, item := range list.Items {
		if !ledger.shown[item.ID] {
			ledger.shown[item.ID] = true
			fresh = append(fresh, item)
		}
	}
	ledger.mu.Unlock()
	for _, item := range fresh {
		if _, err := ledger.append(AnswerRecord{ItemID: item.ID, Kind: answerKindWork, Source: item.Source, Title: item.Work.Title,
			Action: answerActionShown, Surface: surface}); err != nil {
			log.Printf("[answers] an item shown was not kept: %v", err)
			return
		}
	}
}

var errAnswerNotFound = errors.New("this answer is no longer kept")

// ForgetAnswer deletes one answer for good, the person's choice from Your
// data: the entry and any Undo of it leave the ledger, which is written
// again whole and synced. An item it answered can be answered again.
func (a *App) ForgetAnswer(entryID string) error {
	if err := a.answersLedger().forget(entryID); err != nil {
		return err
	}
	a.emitBrowserEvent(workAnsweredEvent, "")
	return nil
}

// forget rewrites the ledger without the entry and its Undo, under one lock
// over the read and the rewrite, so no answer given meanwhile is lost.
func (ledger *answersLedgerStore) forget(entryID string) error {
	if ledger.path == "" {
		return errMobileStoreUnavailable
	}
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	entries := ledger.entriesLocked()
	kept := make([]AnswerRecord, 0, len(entries))
	found := false
	for _, record := range entries {
		if record.EntryID == entryID && record.Action != workActionUndo && record.Action != answerActionShown {
			found = true
			continue
		}
		if record.Action == workActionUndo && record.Undoes == entryID {
			continue
		}
		kept = append(kept, record)
	}
	if !found {
		return errAnswerNotFound
	}
	var buf bytes.Buffer
	for _, record := range kept {
		line, err := json.Marshal(record)
		if err != nil {
			return err
		}
		buf.Write(append(line, '\n'))
	}
	tmp := ledger.path + ".tmp"
	file, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(buf.Bytes()); err != nil {
		_ = file.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, ledger.path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if dir, err := os.Open(filepath.Dir(ledger.path)); err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	return nil
}

// JudgmentCounts are one day's measures for one source (or all of them).
type JudgmentCounts struct {
	// Shown counts items first put in front of the person.
	Shown int `json:"shown"`
	// Answered counts answers that stand; an undone answer counts as Undone.
	Answered  int `json:"answered"`
	Accepted  int `json:"accepted"`
	Declined  int `json:"declined"`
	NotMine   int `json:"notMine"`
	Useful    int `json:"useful"`
	NotUseful int `json:"notUseful"`
	Undone    int `json:"undone"`
}

// JudgmentDay is one local day's measures, all sources and by source.
type JudgmentDay struct {
	Day      string                    `json:"day"`
	Total    JudgmentCounts            `json:"total"`
	BySource map[string]JudgmentCounts `json:"bySource"`
}

// JudgmentStats are the daily measures of the thesis, newest day first:
// proposals made (shown), answered and accepted, per source. A brief's items
// acted on are BySource["brief"].Accepted.
type JudgmentStats struct {
	Days []JudgmentDay `json:"days"`
}

// GetJudgmentStats reads the ledger into daily counts for the last days
// local days (1 to 90, default 7), today first.
func (a *App) GetJudgmentStats(days int) (JudgmentStats, error) {
	return judgmentStatsAt(a.answersLedger().entries(), days, time.Now()), nil
}

func judgmentStatsAt(entries []AnswerRecord, days int, now time.Time) JudgmentStats {
	if days <= 0 || days > 90 {
		days = 7
	}
	undone := answersUndone(entries)
	byDay := map[string]*JudgmentDay{}
	stats := JudgmentStats{Days: []JudgmentDay{}}
	order := []string{}
	today := now.In(time.Local)
	for i := 0; i < days; i++ {
		day := today.AddDate(0, 0, -i).Format("2006-01-02")
		byDay[day] = &JudgmentDay{Day: day, BySource: map[string]JudgmentCounts{}}
		order = append(order, day)
	}
	for _, record := range entries {
		if record.Action == workActionUndo {
			continue
		}
		at, err := time.Parse(time.RFC3339, record.At)
		if err != nil {
			continue
		}
		day := byDay[at.In(time.Local).Format("2006-01-02")]
		if day == nil {
			continue
		}
		source := record.Source
		if source == "" {
			source = answerSourceOther
		}
		counts := day.BySource[source]
		judgmentCount(&counts, record, undone[record.EntryID])
		day.BySource[source] = counts
		judgmentCount(&day.Total, record, undone[record.EntryID])
	}
	for _, day := range order {
		stats.Days = append(stats.Days, *byDay[day])
	}
	return stats
}

func judgmentCount(c *JudgmentCounts, record AnswerRecord, undone bool) {
	if record.Action == answerActionShown {
		c.Shown++
		return
	}
	if undone {
		c.Undone++
		return
	}
	c.Answered++
	switch record.Action {
	case workActionYes, workActionEdit:
		c.Accepted++
	case workActionNo:
		c.Declined++
	case workActionNotMine:
		c.NotMine++
	case workActionUseful:
		c.Useful++
	case workActionNotUseful:
		c.NotUseful++
	}
}
