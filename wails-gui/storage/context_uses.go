package storage

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

const contextUseQueueLimit = 256

type ContextUse struct {
	Kind    string
	ID      string
	Surface string
	Ref     string
	At      int64
	Epoch   uint64 // Captured before reading the entry; never persisted.
}

type EntryUseSurface struct {
	Surface string `json:"surface"`
	Count   int    `json:"count"`
	LastAt  int64  `json:"lastAt"`
}

type EntryUses struct {
	Total    int               `json:"total"`
	LastAt   int64             `json:"lastAt"`
	Surfaces []EntryUseSurface `json:"surfaces"`
}

// Persistence and erasure share a barrier; producers touch only a short queue
// lock using TryLock and a fixed capacity, never waiting on SQLite.
type contextUseWriter struct {
	mu           sync.Mutex
	persistMu    sync.Mutex
	changes      int64 // Exact log row changes, protected by persistMu.
	pending      []ContextUse
	wake         chan struct{}
	done         chan struct{}
	cancel       context.CancelFunc
	closed       bool
	epoch        atomic.Uint64
	erased       map[string]uint64
	erasureLimit bool
}

func validContextKind(kind string) bool { return kind == "memory" || kind == "knowledge" }

// ContextUseID accepts opaque file/session/brief identifiers, never prose,
// paths, URLs or captured text. Empty refs represent a use without a session.
func ContextUseID(id string) bool {
	if len(id) == 0 || len(id) > 256 {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.' || r == ':') {
			return false
		}
	}
	return true
}

func (s *Store) migrateContextUses() error {
	_, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS context_uses (
        id INTEGER PRIMARY KEY AUTOINCREMENT,
        entry_kind TEXT NOT NULL CHECK(entry_kind IN ('memory', 'knowledge')),
        entry_id TEXT NOT NULL,
        surface TEXT NOT NULL CHECK(surface IN ('chat', 'today_chat', 'brief', 'assistant_tool')),
        used_at INTEGER NOT NULL,
        ref TEXT NOT NULL DEFAULT ''
    ); CREATE INDEX IF NOT EXISTS idx_context_uses_entry ON context_uses(entry_kind, entry_id, used_at);`)
	return err
}

func (s *Store) startContextUses() {
	ctx, cancel := context.WithCancel(context.Background())
	s.contextUses = contextUseWriter{wake: make(chan struct{}, 1), done: make(chan struct{}), cancel: cancel}
	// Maintenance is best effort; an unavailable use log never prevents startup.
	_, _ = s.db.Exec(`DELETE FROM context_uses WHERE used_at < ?`, time.Now().Add(-400*24*time.Hour).UnixMilli())
	go func() {
		defer close(s.contextUses.done)
		for {
			select {
			case <-ctx.Done():
				return
			case <-s.contextUses.wake:
				s.contextUses.mu.Lock()
				pending := s.contextUses.pending
				s.contextUses.pending = nil
				s.contextUses.mu.Unlock()
				for _, use := range pending {
					if ctx.Err() != nil {
						break
					}
					// Release persistence ownership between rows so a busy
					// log cannot monopolize source admission or erasure.
					s.contextUses.persistMu.Lock()
					s.contextUses.mu.Lock()
					erased := s.contextUses.erasureLimit || use.Epoch < s.contextUses.erased[use.Kind+":"+use.ID]
					s.contextUses.mu.Unlock()
					if !erased {
						writeCtx, finish := context.WithTimeout(ctx, 250*time.Millisecond)
						_ = s.insertContextUse(writeCtx, use)
						finish()
					}
					s.contextUses.persistMu.Unlock()
				}
			}
		}
	}()
}

func (s *Store) insertContextUse(ctx context.Context, use ContextUse) error {
	result, err := s.db.ExecContext(ctx, `INSERT INTO context_uses(entry_kind, entry_id, surface, used_at, ref)
        SELECT ?, ?, ?, ?, ? WHERE NOT EXISTS (
            SELECT 1 FROM context_uses WHERE entry_kind=? AND entry_id=? AND surface=? AND ref=? AND used_at > ?
        )`, use.Kind, use.ID, use.Surface, use.At, use.Ref, use.Kind, use.ID, use.Surface, use.Ref, use.At-int64(10*time.Minute/time.Millisecond))
	if err == nil {
		changed, countErr := result.RowsAffected()
		if countErr != nil {
			return countErr
		}
		s.contextUses.changes += changed
	}
	return err
}

// RecordContextUses is bounded, asynchronous and deliberately best effort.
func (s *Store) RecordContextUses(kind string, ids []string, surface, ref string) {
	uses := make([]ContextUse, 0, min(contextUseQueueLimit, len(ids)))
	for _, id := range ids[:min(contextUseQueueLimit, len(ids))] {
		uses = append(uses, ContextUse{Kind: kind, ID: id, Surface: surface, Ref: ref})
	}
	s.RecordContextUseBatch(uses)
}

// RecordContextUseBatch admits both entry kinds from a single context pass
// under one short queue lock, so its own worker cannot split the admission.
func (s *Store) RecordContextUseBatch(uses []ContextUse) {
	if s == nil {
		return
	}
	epoch := s.ContextUseEpoch()
	captured := make([]ContextUse, len(uses[:min(contextUseQueueLimit, len(uses))]))
	copy(captured, uses)
	for i := range captured {
		captured[i].Epoch = epoch
	}
	s.RecordCapturedContextUseBatch(captured)
}

// ContextUseEpoch snapshots the deletion sequence before a source read starts.
// A later deletion rejects that read's receipts even if the ID is recreated.
func (s *Store) ContextUseEpoch() uint64 {
	if s == nil {
		return 0
	}
	return s.contextUses.epoch.Load()
}

// RecordCapturedContextUseBatch retains each read's epoch across async work.
func (s *Store) RecordCapturedContextUseBatch(uses []ContextUse) {
	if s == nil {
		return
	}
	w := &s.contextUses
	if !w.mu.TryLock() {
		return
	}
	defer w.mu.Unlock()
	if w.closed || w.erasureLimit || w.wake == nil {
		return
	}
	now := time.Now().UnixMilli()
	seen := map[[4]string]bool{}
	for _, use := range uses[:min(contextUseQueueLimit, len(uses))] {
		if len(w.pending) >= contextUseQueueLimit {
			break
		}
		switch use.Surface {
		case "chat", "today_chat", "brief", "assistant_tool":
		default:
			continue
		}
		key := [4]string{use.Kind, use.ID, use.Surface, use.Ref}
		if !validContextKind(use.Kind) || !ContextUseID(use.ID) ||
			(use.Ref != "" && !ContextUseID(use.Ref)) || seen[key] || use.Epoch < w.erased[use.Kind+":"+use.ID] || use.Epoch > w.epoch.Load() {
			continue
		}
		seen[key] = true
		use.At = now
		w.pending = append(w.pending, use)
	}
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// DeleteContextUses also discards queued rows under the writer's barrier.
func (s *Store) DeleteContextUses(ctx context.Context, kind string, ids ...string) error {
	finish, err := s.PrepareContextUseDeletion(ctx, kind, ids...)
	if err != nil {
		return err
	}
	return finish(true)
}

// PrepareContextUseDeletion holds the writer barrier through the caller's
// commit decision. Rollback leaves both queued and persisted uses intact.
// Call the returned function exactly once.
func (s *Store) PrepareContextUseDeletion(ctx context.Context, kind string, ids ...string) (func(bool) error, error) {
	if s == nil {
		return func(bool) error { return nil }, nil
	}
	if !validContextKind(kind) {
		return nil, fmt.Errorf("invalid entry kind")
	}
	w := &s.contextUses
	w.persistMu.Lock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		w.persistMu.Unlock()
		return nil, err
	}
	var changes int64
	for _, id := range ids {
		result, err := tx.ExecContext(ctx, "DELETE FROM context_uses WHERE entry_kind=? AND entry_id=?", kind, id)
		if err == nil {
			var n int64
			n, err = result.RowsAffected()
			changes += n
		}
		if err != nil {
			tx.Rollback()
			w.persistMu.Unlock()
			return nil, err
		}
	}
	return func(commit bool) error {
		defer w.persistMu.Unlock()
		if !commit {
			return tx.Rollback()
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		w.changes += changes
		w.mu.Lock()
		removed := map[string]bool{}
		if w.erased == nil {
			w.erased = map[string]uint64{}
		}
		epoch := w.epoch.Add(1)
		for _, id := range ids {
			removed[id] = true
			// Deterministic entry IDs can be reused. Only reads begun before this
			// deletion are invalid; a new read can log the new entry's lifetime.
			// Bound the process-local barrier; saturation disables logging.
			if len(w.erased) < 4096 || w.erased[kind+":"+id] != 0 {
				w.erased[kind+":"+id] = epoch
			} else {
				w.erasureLimit = true
			}
		}
		kept := w.pending[:0]
		for _, use := range w.pending {
			if use.Kind != kind || !removed[use.ID] {
				kept = append(kept, use)
			}
		}
		w.pending = kept
		w.mu.Unlock()
		return nil
	}, nil
}

func (s *Store) EntryUses(ctx context.Context, kind, id string) (EntryUses, error) {
	out := EntryUses{Surfaces: []EntryUseSurface{}}
	if !validContextKind(kind) || !ContextUseID(id) {
		return out, fmt.Errorf("invalid entry identity")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT surface, COUNT(*), MAX(used_at) FROM context_uses WHERE entry_kind=? AND entry_id=? GROUP BY surface ORDER BY surface`, kind, id)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var surface EntryUseSurface
		if err := rows.Scan(&surface.Surface, &surface.Count, &surface.LastAt); err != nil {
			return out, err
		}
		out.Surfaces = append(out.Surfaces, surface)
		out.Total += surface.Count
		out.LastAt = max(out.LastAt, surface.LastAt)
	}
	return out, rows.Err()
}

func (s *Store) EntryUseCounts(ctx context.Context, kind string, ids []string) (map[string]int, error) {
	out := map[string]int{}
	if !validContextKind(kind) || len(ids) > 10000 {
		return nil, fmt.Errorf("invalid entry list")
	}
	// Bound each statement below SQLite's variable limit and avoid scanning
	// uses of unrelated entries. Duplicate IDs still produce one map key.
	for start := 0; start < len(ids); start += 200 {
		end := min(start+200, len(ids))
		args := []any{kind}
		placeholders := ""
		for _, id := range ids[start:end] {
			if !ContextUseID(id) {
				return nil, fmt.Errorf("invalid entry identity")
			}
			out[id] = 0
			if placeholders != "" {
				placeholders += ","
			}
			placeholders += "?"
			args = append(args, id)
		}
		rows, err := s.db.QueryContext(ctx, `SELECT entry_id, COUNT(*) FROM context_uses WHERE entry_kind=? AND entry_id IN (`+placeholders+`) GROUP BY entry_id`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id string
			var count int
			if err := rows.Scan(&id, &count); err != nil {
				rows.Close()
				return nil, err
			}
			out[id] = count
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Keep the connection's log-only change count consistent with source revision
// reads. Cancellation remains effective while an async writer owns the barrier.
func (s *Store) lockContextUseRevision(ctx context.Context) (func(), error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if s.contextUses.persistMu.TryLock() {
			return s.contextUses.persistMu.Unlock, nil
		}
		timer := time.NewTimer(time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}
