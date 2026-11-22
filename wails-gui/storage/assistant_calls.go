package storage

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
)

// The record of what each assistant call was given (computer-data v1.0.0
// "What the build needs", v1.1.0 See them; checklist B2). One row per call
// says what it was for and when; its sources say what it was written from,
// by kind and opaque id, never the text. An entry is kept as long as its
// sources: forgetting a source removes every entry that names it, and the
// entries age out with the activity they came from.

// Assistant call purposes, the groups of See them.
const (
	AssistantCallDay      = "day"      // making sense of the day: screen, WeChat, watch, calls
	AssistantCallBrief    = "brief"    // a brief, ref = brief id
	AssistantCallAnswer   = "answer"   // an answer in Chat, ref = the question's item id
	AssistantCallProposal = "proposal" // a topic proposal, ref = the proposal id
	// AssistantCallLearning is every other background call that reads the
	// person's data (activity passes on their own schedules, memory,
	// playbooks, About you, people research, suggestions), one entry per
	// kind per hour. See them has no group for it yet; the record keeps it
	// so it is complete.
	AssistantCallLearning = "learning"
)

// Source kinds, by the names What Isoform sees uses.
var assistantCallSourceKinds = map[string]bool{
	"screen": true, "wechat": true, "watch": true, "call": true,
	"mail": true, "calendar": true, "messages": true, "agents_history": true,
	"memory": true, "knowledge": true, "people": true, "activity": true,
	"topic_names": true, "goals": true, "web": true, "chat": true, "work": true, "files": true,
}

// AssistantCallSourceKinds lists every source kind, so a test can hold each
// to a name on the page (design T15: no kind goes unnamed).
func AssistantCallSourceKinds() []string {
	out := make([]string, 0, len(assistantCallSourceKinds))
	for kind := range assistantCallSourceKinds {
		out = append(out, kind)
	}
	sort.Strings(out)
	return out
}

func validAssistantCallPurpose(p string) bool {
	switch p {
	case AssistantCallDay, AssistantCallBrief, AssistantCallAnswer, AssistantCallProposal, AssistantCallLearning:
		return true
	default:
		return false
	}
}

// AssistantCallSourceKind reports whether kind is a recorded source kind.
func AssistantCallSourceKind(kind string) bool { return assistantCallSourceKinds[kind] }

// AssistantCallSource is one thing a call was written from. Ref is an opaque
// id (a session, recording, brief evidence or entry id), never prose; an
// empty ref counts a kind without naming one item (a WeChat window).
type AssistantCallSource struct {
	Kind  string `json:"kind"`
	Ref   string `json:"ref,omitempty"`
	Count int    `json:"count"`
}

// AssistantCall is one call to the assistant.
type AssistantCall struct {
	ID        int64  `json:"id"`
	Purpose   string `json:"purpose"`
	Ref       string `json:"ref,omitempty"`
	At        int64  `json:"at"`
	SpanStart int64  `json:"spanStart,omitempty"`
	SpanEnd   int64  `json:"spanEnd,omitempty"`
	Times     int    `json:"times"`
	// Made is how many claims to check the work made (a learning pass's new
	// lines on You), for See them's "Made: {n} claims to check".
	Made    int                   `json:"made,omitempty"`
	Sources []AssistantCallSource `json:"sources"`
}

func (s *Store) migrateAssistantCalls() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS assistant_calls (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			purpose    TEXT    NOT NULL CHECK (purpose IN ('day', 'brief', 'answer', 'proposal', 'learning')),
			ref        TEXT    NOT NULL DEFAULT '',
			at         INTEGER NOT NULL,
			span_start INTEGER NOT NULL DEFAULT 0,
			span_end   INTEGER NOT NULL DEFAULT 0,
			times      INTEGER NOT NULL DEFAULT 1
		);
		CREATE INDEX IF NOT EXISTS idx_assistant_calls_at ON assistant_calls(at);
		CREATE UNIQUE INDEX IF NOT EXISTS idx_assistant_calls_ref ON assistant_calls(purpose, ref) WHERE ref != '';
		CREATE TABLE IF NOT EXISTS assistant_call_sources (
			call_id INTEGER NOT NULL,
			kind    TEXT    NOT NULL,
			ref     TEXT    NOT NULL DEFAULT '',
			count   INTEGER NOT NULL DEFAULT 1,
			PRIMARY KEY (call_id, kind, ref)
		);
		CREATE INDEX IF NOT EXISTS idx_assistant_call_sources_ref ON assistant_call_sources(kind, ref);
	`)
	return err
}

func cleanAssistantCall(call AssistantCall) (AssistantCall, error) {
	if !validAssistantCallPurpose(call.Purpose) {
		return call, fmt.Errorf("assistant call: unknown purpose %q", call.Purpose)
	}
	if call.Ref != "" && !ContextUseID(call.Ref) {
		return call, fmt.Errorf("assistant call: ref is not an opaque id")
	}
	if call.At <= 0 {
		return call, fmt.Errorf("assistant call: no time")
	}
	if call.Times <= 0 {
		call.Times = 1
	}
	merged := map[[2]string]int{}
	for _, src := range call.Sources {
		if !assistantCallSourceKinds[src.Kind] {
			return call, fmt.Errorf("assistant call: unknown source kind %q", src.Kind)
		}
		if src.Ref != "" && !ContextUseID(src.Ref) {
			return call, fmt.Errorf("assistant call: source ref is not an opaque id")
		}
		n := src.Count
		if n <= 0 {
			n = 1
		}
		merged[[2]string{src.Kind, src.Ref}] += n
	}
	call.Sources = call.Sources[:0:0]
	for key, n := range merged {
		call.Sources = append(call.Sources, AssistantCallSource{Kind: key[0], Ref: key[1], Count: n})
	}
	sortAssistantCallSources(call.Sources)
	return call, nil
}

func sortAssistantCallSources(sources []AssistantCallSource) {
	sort.Slice(sources, func(i, j int) bool {
		if sources[i].Kind != sources[j].Kind {
			return sources[i].Kind < sources[j].Kind
		}
		return sources[i].Ref < sources[j].Ref
	})
}

// RecordAssistantCall keeps one entry: Times calls (at least one) made for
// one piece of work. An entry with a ref joins the one already kept for it
// (an answer asked again is one answer): its sources and times are added
// and its time and span widen.
func (s *Store) RecordAssistantCall(ctx context.Context, call AssistantCall) error {
	call, err := cleanAssistantCall(call)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var id int64
	if call.Ref != "" {
		err = tx.QueryRowContext(ctx, `SELECT id FROM assistant_calls WHERE purpose = ? AND ref = ?`, call.Purpose, call.Ref).Scan(&id)
		switch {
		case err == sql.ErrNoRows:
			id = 0
		case err != nil:
			return err
		default:
			if _, err := tx.ExecContext(ctx, `UPDATE assistant_calls SET
				at = MAX(at, ?),
				span_start = CASE WHEN span_start = 0 OR (? > 0 AND ? < span_start) THEN ? ELSE span_start END,
				span_end = MAX(span_end, ?),
				times = times + ?,
				made = made + ?
				WHERE id = ?`, call.At, call.SpanStart, call.SpanStart, call.SpanStart, call.SpanEnd, call.Times, max(call.Made, 0), id); err != nil {
				return err
			}
		}
	}
	if id == 0 {
		res, err := tx.ExecContext(ctx, `INSERT INTO assistant_calls (purpose, ref, at, span_start, span_end, times, made) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			call.Purpose, call.Ref, call.At, call.SpanStart, call.SpanEnd, call.Times, max(call.Made, 0))
		if err != nil {
			return err
		}
		if id, err = res.LastInsertId(); err != nil {
			return err
		}
	}
	for _, src := range call.Sources {
		if _, err := tx.ExecContext(ctx, `INSERT INTO assistant_call_sources (call_id, kind, ref, count) VALUES (?, ?, ?, ?)
			ON CONFLICT(call_id, kind, ref) DO UPDATE SET count = count + excluded.count`, id, src.Kind, src.Ref, src.Count); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ListAssistantCalls returns the calls at or after since, newest first,
// each with its sources.
func (s *Store) ListAssistantCalls(ctx context.Context, since int64) ([]AssistantCall, error) {
	reader := s.committedReader()
	rows, err := reader.QueryContext(ctx, `SELECT id, purpose, ref, at, span_start, span_end, times, made FROM assistant_calls WHERE at >= ? ORDER BY at DESC, id DESC`, since)
	if err != nil {
		return nil, err
	}
	var calls []AssistantCall
	index := map[int64]int{}
	for rows.Next() {
		var c AssistantCall
		if err := rows.Scan(&c.ID, &c.Purpose, &c.Ref, &c.At, &c.SpanStart, &c.SpanEnd, &c.Times, &c.Made); err != nil {
			rows.Close()
			return nil, err
		}
		c.Sources = []AssistantCallSource{}
		index[c.ID] = len(calls)
		calls = append(calls, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(calls) == 0 {
		return calls, nil
	}
	srcRows, err := reader.QueryContext(ctx, `SELECT s.call_id, s.kind, s.ref, s.count FROM assistant_call_sources s
		JOIN assistant_calls c ON c.id = s.call_id WHERE c.at >= ?`, since)
	if err != nil {
		return nil, err
	}
	defer srcRows.Close()
	for srcRows.Next() {
		var id int64
		var src AssistantCallSource
		if err := srcRows.Scan(&id, &src.Kind, &src.Ref, &src.Count); err != nil {
			return nil, err
		}
		if i, ok := index[id]; ok {
			calls[i].Sources = append(calls[i].Sources, src)
		}
	}
	for i := range calls {
		sortAssistantCallSources(calls[i].Sources)
	}
	return calls, srcRows.Err()
}

// CountAssistantCalls counts the calls at or after since by purpose: the
// "this week {n} briefs, {m} answers" of Your data.
func (s *Store) CountAssistantCalls(ctx context.Context, since int64) (map[string]int, error) {
	rows, err := s.committedReader().QueryContext(ctx, `SELECT purpose, COUNT(*) FROM assistant_calls WHERE at >= ? GROUP BY purpose`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var purpose string
		var n int
		if err := rows.Scan(&purpose, &n); err != nil {
			return nil, err
		}
		out[purpose] = n
	}
	return out, rows.Err()
}

func (s *Store) forgetAssistantCallsWhere(ctx context.Context, where string, args ...any) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT id FROM assistant_calls WHERE `+where, args...)
	if err != nil {
		return 0, err
	}
	var ids []any
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for start := 0; start < len(ids); start += 500 {
		end := min(start+500, len(ids))
		marks := strings.TrimSuffix(strings.Repeat("?,", end-start), ",")
		if _, err := tx.ExecContext(ctx, `DELETE FROM assistant_call_sources WHERE call_id IN (`+marks+`)`, ids[start:end]...); err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM assistant_calls WHERE id IN (`+marks+`)`, ids[start:end]...); err != nil {
			return 0, err
		}
	}
	return len(ids), tx.Commit()
}

// ForgetAssistantCallsForSource removes every entry written from the source
// kind/ref: Forget in Activity takes its entries in See them with it.
func (s *Store) ForgetAssistantCallsForSource(ctx context.Context, kind, ref string) (int, error) {
	if ref == "" {
		return 0, nil
	}
	return s.forgetAssistantCallsWhere(ctx, `id IN (SELECT call_id FROM assistant_call_sources WHERE kind = ? AND ref = ?)`, kind, ref)
}

// ForgetAssistantCallsForKind removes every entry written from a kind, for
// a source turned off or protected as a whole (Messages, a protected app).
func (s *Store) ForgetAssistantCallsForKind(ctx context.Context, kind string) (int, error) {
	return s.forgetAssistantCallsWhere(ctx, `id IN (SELECT call_id FROM assistant_call_sources WHERE kind = ?)`, kind)
}

// ForgetAssistantCall removes the entry for purpose/ref: a deleted answer,
// brief or proposal.
func (s *Store) ForgetAssistantCall(ctx context.Context, purpose, ref string) (int, error) {
	return s.forgetAssistantCallsWhere(ctx, `purpose = ? AND ref = ?`, purpose, ref)
}

// ForgetAssistantCallsBefore removes entries older than before: they age
// out with the activity they were written from.
func (s *Store) ForgetAssistantCallsBefore(ctx context.Context, before int64) (int, error) {
	return s.forgetAssistantCallsWhere(ctx, `at < ?`, before)
}

// migrateAssistantCallsMade adds what each entry made (v77).
func (s *Store) migrateAssistantCallsMade() error {
	if _, err := s.db.Exec(`ALTER TABLE assistant_calls ADD COLUMN made INTEGER NOT NULL DEFAULT 0`); err != nil &&
		!strings.Contains(strings.ToLower(err.Error()), "duplicate column") {
		return fmt.Errorf("add made: %w", err)
	}
	return nil
}

// ForgetAssistantCallsInRange removes the day and learning entries whose
// span overlaps [startMs, endMs): a stretch of time withdrawn as a whole.
func (s *Store) ForgetAssistantCallsInRange(ctx context.Context, startMs, endMs int64) (int, error) {
	if endMs <= startMs {
		return 0, nil
	}
	return s.forgetAssistantCallsWhere(ctx, `purpose IN ('day', 'learning') AND span_start < ? AND span_end > ?`, endMs, startMs)
}

// ForgetAllAssistantCalls empties the record (Forget everything).
func (s *Store) ForgetAllAssistantCalls(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM assistant_call_sources; DELETE FROM assistant_calls;`)
	return err
}
