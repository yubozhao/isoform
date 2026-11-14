package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ErrSelfAlreadyExists is returned by EnsureSelf when called concurrently and
// the unique self-row constraint catches the second writer. Callers should
// re-read the row instead of treating it as a hard error.
var ErrSelfAlreadyExists = errors.New("storage: self principal already exists")

// validPrincipalKinds enumerates the values permitted in principals.kind.
// Used by CreatePrincipal to reject typos at the boundary instead of letting
// invalid rows reach the resolver.
var validPrincipalKinds = map[string]struct{}{
	PrincipalKindSelf:           {},
	PrincipalKindContact:        {},
	PrincipalKindContentSubject: {},
	PrincipalKindAnonymous:      {},
}

// CreatePrincipal inserts a new principal row plus any aliases.
// The kind must be one of the validPrincipalKinds; ID is generated if empty.
// For kind=self the caller should prefer EnsureSelf, which is idempotent.
func (s *Store) CreatePrincipal(ctx context.Context, p PrincipalRow) (PrincipalRow, error) {
	if _, ok := validPrincipalKinds[p.Kind]; !ok {
		return PrincipalRow{}, fmt.Errorf("invalid principal kind: %q", p.Kind)
	}
	if p.ID == "" {
		p.ID = uuid.New().String()
	}
	now := time.Now().UnixMilli()
	p.CreatedAt = now
	p.UpdatedAt = now
	if p.Confidence == 0 {
		p.Confidence = 1.0
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return PrincipalRow{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO principals
			(id, kind, display_name, relationship, notes, created_from, source, external_ref, confidence, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.Kind, p.DisplayName, p.Relationship, p.Notes, p.CreatedFrom, p.Source, p.ExternalRef, p.Confidence, p.CreatedAt, p.UpdatedAt,
	); err != nil {
		if isSelfUniqueViolation(err) {
			return PrincipalRow{}, ErrSelfAlreadyExists
		}
		return PrincipalRow{}, fmt.Errorf("insert principal: %w", err)
	}

	if err := writeAliasesTx(ctx, tx, p.ID, p.Aliases); err != nil {
		return PrincipalRow{}, err
	}

	if err := tx.Commit(); err != nil {
		return PrincipalRow{}, fmt.Errorf("commit: %w", err)
	}
	return p, nil
}

// UpdatePrincipal replaces the mutable fields and the alias set in one
// transaction. Returns sql.ErrNoRows if the principal does not exist.
func (s *Store) UpdatePrincipal(ctx context.Context, p PrincipalRow) (PrincipalRow, error) {
	if p.ID == "" {
		return PrincipalRow{}, errors.New("UpdatePrincipal: empty id")
	}
	now := time.Now().UnixMilli()
	p.UpdatedAt = now

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return PrincipalRow{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx, `
		UPDATE principals
		   SET display_name = ?, relationship = ?, notes = ?, created_from = ?, source = ?, external_ref = ?, confidence = ?, updated_at = ?
		 WHERE id = ? AND merged_into = ''`,
		p.DisplayName, p.Relationship, p.Notes, p.CreatedFrom, p.Source, p.ExternalRef, p.Confidence, p.UpdatedAt, p.ID,
	)
	if err != nil {
		return PrincipalRow{}, fmt.Errorf("update principal: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return PrincipalRow{}, sql.ErrNoRows
	}

	previousAliases, err := loadAliasesTx(ctx, tx, p.ID)
	if err != nil {
		return PrincipalRow{}, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM principal_aliases WHERE principal_id = ?`, p.ID); err != nil {
		return PrincipalRow{}, fmt.Errorf("delete aliases: %w", err)
	}
	if err := writeAliasesTx(ctx, tx, p.ID, p.Aliases); err != nil {
		return PrincipalRow{}, err
	}

	if err := preserveEditedMergeAliases(ctx, tx, p.ID, previousAliases, p.Aliases, p.UpdatedAt); err != nil {
		return PrincipalRow{}, err
	}

	row, err := getPrincipalTx(ctx, tx, p.ID)
	if err != nil {
		return PrincipalRow{}, err
	}
	if err := tx.Commit(); err != nil {
		return PrincipalRow{}, fmt.Errorf("commit: %w", err)
	}
	return row, nil
}

// DeletePrincipal removes a principal and (via ON DELETE CASCADE) all of its
// aliases and handles. Deleting the self principal is allowed but should be
// rare; callers driving onboarding should re-create via EnsureSelf afterward.
func (s *Store) DeletePrincipal(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := requirePrincipalExists(ctx, tx, id); err != nil {
		return err
	}
	// Hidden descendants retain private data for Undo and belong to the same
	// deletion boundary. Remove receipts too, including already-undone ones.
	const family = `WITH RECURSIVE family(id) AS (SELECT id FROM principals WHERE id=? UNION SELECT p.id FROM principals p JOIN family f ON p.merged_into=f.id) `
	if _, err = tx.ExecContext(ctx, family+`DELETE FROM principal_merges WHERE target_id IN (SELECT id FROM family) OR EXISTS(SELECT 1 FROM json_each(merged_ids_json) WHERE value IN (SELECT id FROM family))`, id); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, family+`DELETE FROM principals WHERE id IN (SELECT id FROM family)`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return tx.Commit()
}

// GetPrincipal returns the principal with the given id including its aliases.
// Returns sql.ErrNoRows if missing.
func (s *Store) GetPrincipal(ctx context.Context, id string) (PrincipalRow, error) {
	return getPrincipalTx(ctx, s.db, id)
}

// ListPrincipals returns all principals ordered by kind then display name.
// Aliases are populated in a single batched query.
func (s *Store) ListPrincipals(ctx context.Context) ([]PrincipalRow, error) {
	return s.listPrincipals(ctx, "")
}

// SearchMCPPrincipals returns only matching identity metadata. It never loads
// notes or aliases, and callers validate capture provenance after the name
// match. The partial flag depends on the whole source count, not on matches.
func (s *Store) SearchMCPPrincipals(ctx context.Context, name string, limit int) ([]PrincipalRow, bool, error) {
	if limit <= 0 {
		return nil, false, errors.New("principal search limit must be positive")
	}
	var limited bool
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM principals WHERE merged_into = '' LIMIT 1 OFFSET ?)`, limit).Scan(&limited); err != nil {
		return nil, false, err
	}
	// SQLite lower is ASCII-only. Also scan non-ASCII names through Go's
	// Unicode case folding so international names keep the existing behavior.
	rows, err := s.db.QueryContext(ctx, `SELECT id,display_name,created_from,updated_at FROM principals
		WHERE merged_into = '' AND (instr(lower(display_name),lower(?))>0 OR display_name GLOB '*[^ -~]*')
		ORDER BY kind,display_name,id`, name)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	folded := strings.ToLower(name)
	var out []PrincipalRow
	for rows.Next() {
		var row PrincipalRow
		if err := rows.Scan(&row.ID, &row.DisplayName, &row.CreatedFrom, &row.UpdatedAt); err != nil {
			return nil, false, err
		}
		if !strings.Contains(strings.ToLower(row.DisplayName), folded) {
			continue
		}
		out = append(out, row)
		if len(out) == limit {
			break
		}
	}
	return out, limited, rows.Err()
}

// ListPrincipalsByKind returns principals of a single kind.
func (s *Store) ListPrincipalsByKind(ctx context.Context, kind string) ([]PrincipalRow, error) {
	if _, ok := validPrincipalKinds[kind]; !ok {
		return nil, fmt.Errorf("invalid principal kind: %q", kind)
	}
	return s.listPrincipals(ctx, kind)
}

func (s *Store) listPrincipals(ctx context.Context, kind string) ([]PrincipalRow, error) {
	var (
		rows *sql.Rows
		err  error
	)
	if kind == "" {
		rows, err = s.db.QueryContext(ctx, `
			SELECT id, kind, display_name, relationship, notes, created_from, source, external_ref, confidence, created_at, updated_at
			  FROM principals
			 WHERE merged_into = ''
			 ORDER BY kind, display_name`)
	} else {
		rows, err = s.db.QueryContext(ctx, `
			SELECT id, kind, display_name, relationship, notes, created_from, source, external_ref, confidence, created_at, updated_at
			  FROM principals
			 WHERE kind = ? AND merged_into = ''
			 ORDER BY display_name`, kind)
	}
	if err != nil {
		return nil, fmt.Errorf("list principals: %w", err)
	}
	defer rows.Close()

	var out []PrincipalRow
	for rows.Next() {
		var p PrincipalRow
		if err := rows.Scan(&p.ID, &p.Kind, &p.DisplayName, &p.Relationship, &p.Notes, &p.CreatedFrom, &p.Source, &p.ExternalRef, &p.Confidence, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan principal: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iter principals: %w", err)
	}

	if len(out) == 0 {
		return nil, nil
	}

	aliases, err := s.loadAliases(ctx, principalIDs(out))
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Aliases = aliases[out[i].ID]
	}
	return out, nil
}

func principalIDs(ps []PrincipalRow) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.ID
	}
	return out
}

func (s *Store) loadAliases(ctx context.Context, ids []string) (map[string][]string, error) {
	if len(ids) == 0 {
		return map[string][]string{}, nil
	}
	placeholders := strings.Repeat("?,", len(ids))
	placeholders = placeholders[:len(placeholders)-1]
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	q := fmt.Sprintf("SELECT principal_id, alias FROM principal_aliases WHERE principal_id IN (%s) ORDER BY principal_id, alias", placeholders)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("load aliases: %w", err)
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var pid, alias string
		if err := rows.Scan(&pid, &alias); err != nil {
			return nil, fmt.Errorf("scan alias: %w", err)
		}
		out[pid] = append(out[pid], alias)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iter aliases: %w", err)
	}
	return out, nil
}

// EnsureSelf returns the singleton self principal, creating it if missing.
// Concurrent callers race-safely converge on the same row thanks to the
// uq_principals_self partial unique index.
func (s *Store) EnsureSelf(ctx context.Context, displayName string, aliases []string) (PrincipalRow, error) {
	existing, err := s.getSelf(ctx)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return PrincipalRow{}, err
	}

	created, err := s.CreatePrincipal(ctx, PrincipalRow{
		Kind:        PrincipalKindSelf,
		DisplayName: displayName,
		CreatedFrom: "onboarding",
		Confidence:  1.0,
		Aliases:     aliases,
	})
	if errors.Is(err, ErrSelfAlreadyExists) {
		return s.getSelf(ctx)
	}
	if err != nil {
		return PrincipalRow{}, err
	}
	return created, nil
}

func (s *Store) getSelf(ctx context.Context) (PrincipalRow, error) {
	var p PrincipalRow
	row := s.db.QueryRowContext(ctx, `
		SELECT id, kind, display_name, relationship, notes, created_from, source, external_ref, confidence, created_at, updated_at
		  FROM principals
		 WHERE kind = ?`, PrincipalKindSelf)
	if err := row.Scan(&p.ID, &p.Kind, &p.DisplayName, &p.Relationship, &p.Notes, &p.CreatedFrom, &p.Source, &p.ExternalRef, &p.Confidence, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return PrincipalRow{}, err
	}
	aliases, err := s.loadAliases(ctx, []string{p.ID})
	if err != nil {
		return PrincipalRow{}, err
	}
	p.Aliases = aliases[p.ID]
	return p, nil
}

// BindAppHandle attaches a per-app account identifier to a principal.
// Returns the inserted row, or sql.ErrNoRows wrapped if the principal does
// not exist. When the (app, handle_id, display_handle) tuple already exists
// the existing row is returned instead of creating a duplicate.
func (s *Store) BindAppHandle(ctx context.Context, principalID, app, handleID, displayHandle string) (AppHandleRow, error) {
	app = strings.TrimSpace(strings.ToLower(app))
	if app == "" {
		return AppHandleRow{}, errors.New("BindAppHandle: empty app")
	}
	if handleID == "" && displayHandle == "" {
		return AppHandleRow{}, errors.New("BindAppHandle: need at least one of handleID or displayHandle")
	}

	var exists int
	if err := s.db.QueryRowContext(ctx, "SELECT 1 FROM principals WHERE id = ? AND merged_into = ''", principalID).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return AppHandleRow{}, fmt.Errorf("BindAppHandle principal %q: %w", principalID, sql.ErrNoRows)
		}
		return AppHandleRow{}, fmt.Errorf("check principal: %w", err)
	}

	now := time.Now().UnixMilli()
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO app_handles (principal_id, app, handle_id, display_handle, created_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(app, handle_id, display_handle) DO UPDATE SET principal_id = excluded.principal_id`,
		principalID, app, handleID, displayHandle, now,
	)
	if err != nil {
		return AppHandleRow{}, fmt.Errorf("insert app_handle: %w", err)
	}
	id, _ := res.LastInsertId()
	if id == 0 {
		// ON CONFLICT path: re-read the existing row id.
		if err := s.db.QueryRowContext(ctx,
			"SELECT id, created_at FROM app_handles WHERE app = ? AND handle_id = ? AND display_handle = ?",
			app, handleID, displayHandle,
		).Scan(&id, &now); err != nil {
			return AppHandleRow{}, fmt.Errorf("re-read app_handle: %w", err)
		}
	}
	return AppHandleRow{
		ID:            id,
		PrincipalID:   principalID,
		App:           app,
		HandleID:      handleID,
		DisplayHandle: displayHandle,
		CreatedAt:     now,
	}, nil
}

// UnbindAppHandle removes a handle binding by row id.
func (s *Store) UnbindAppHandle(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, "DELETE FROM app_handles WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete app_handle: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// ListAppHandlesByPrincipal returns every app handle bound to a principal.
func (s *Store) ListAppHandlesByPrincipal(ctx context.Context, principalID string) ([]AppHandleRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, principal_id, app, handle_id, display_handle, created_at
		  FROM app_handles
		 WHERE principal_id = ? AND EXISTS (SELECT 1 FROM principals p WHERE p.id=principal_id AND p.merged_into='')
		 ORDER BY app, display_handle`, principalID)
	if err != nil {
		return nil, fmt.Errorf("list app_handles: %w", err)
	}
	defer rows.Close()
	var out []AppHandleRow
	for rows.Next() {
		var h AppHandleRow
		if err := rows.Scan(&h.ID, &h.PrincipalID, &h.App, &h.HandleID, &h.DisplayHandle, &h.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan app_handle: %w", err)
		}
		out = append(out, h)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iter app_handles: %w", err)
	}
	return out, nil
}

// ListAppHandlesByPrincipals returns active handles grouped by principal.
// Ordering within each principal matches ListAppHandlesByPrincipal.
func (s *Store) ListAppHandlesByPrincipals(ctx context.Context) (map[string][]AppHandleRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, principal_id, app, handle_id, display_handle, created_at
		  FROM app_handles
		 WHERE EXISTS (SELECT 1 FROM principals p WHERE p.id=principal_id AND p.merged_into='')
		 ORDER BY principal_id, app, display_handle`)
	if err != nil {
		return nil, fmt.Errorf("list app_handles: %w", err)
	}
	defer rows.Close()
	out := make(map[string][]AppHandleRow)
	for rows.Next() {
		var h AppHandleRow
		if err := rows.Scan(&h.ID, &h.PrincipalID, &h.App, &h.HandleID, &h.DisplayHandle, &h.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan app_handle: %w", err)
		}
		out[h.PrincipalID] = append(out[h.PrincipalID], h)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iter app_handles: %w", err)
	}
	return out, nil
}

// ResolveByAppHandle returns the principal bound to (app, handleID).
// Lookup is case-insensitive on app. Returns (nil, nil) when no binding
// exists — this is a hot path during chat rendering and a missing binding
// is the common case, not an error.
func (s *Store) ResolveByAppHandle(ctx context.Context, app, handleID string) (*PrincipalRow, error) {
	app = strings.TrimSpace(strings.ToLower(app))
	handleID = strings.TrimSpace(handleID)
	if app == "" || handleID == "" {
		return nil, nil
	}
	var id string
	err := s.db.QueryRowContext(ctx,
		"SELECT h.principal_id FROM app_handles h JOIN principals p ON p.id=h.principal_id WHERE p.merged_into='' AND h.app = ? AND h.handle_id = ? LIMIT 1",
		app, handleID,
	).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("resolve app handle: %w", err)
	}
	p, err := s.GetPrincipal(ctx, id)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// ResolveByDisplayHandle is like ResolveByAppHandle but matches the
// human-visible handle field. Used when chat parsers only have the
// rendered "@user" string and not a stable id.
func (s *Store) ResolveByDisplayHandle(ctx context.Context, app, displayHandle string) (*PrincipalRow, error) {
	app = strings.TrimSpace(strings.ToLower(app))
	displayHandle = strings.TrimSpace(displayHandle)
	if app == "" || displayHandle == "" {
		return nil, nil
	}
	var id string
	err := s.db.QueryRowContext(ctx,
		"SELECT h.principal_id FROM app_handles h JOIN principals p ON p.id=h.principal_id WHERE p.merged_into='' AND h.app = ? AND lower(h.display_handle) = lower(?) LIMIT 1",
		app, displayHandle,
	).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("resolve display handle: %w", err)
	}
	p, err := s.GetPrincipal(ctx, id)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// ResolveByAlias returns the first principal whose DisplayName or aliases
// match the given name (case-insensitive). Returns (nil, nil) when no
// match exists. When more than one principal matches the lookup returns
// the most recently updated one to favor user corrections.
func (s *Store) ResolveByAlias(ctx context.Context, name string) (*PrincipalRow, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, nil
	}
	var id string
	// Aliases first — aliases are explicitly added by the user/onboarding and
	// outrank display_name matches when both exist.
	err := s.db.QueryRowContext(ctx, `
		SELECT pa.principal_id
		  FROM principal_aliases pa
		  JOIN principals p ON p.id = pa.principal_id
		 WHERE p.merged_into = '' AND lower(pa.alias) = lower(?)
		 ORDER BY p.updated_at DESC
		 LIMIT 1`, name).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		err = s.db.QueryRowContext(ctx, `
			SELECT id FROM principals
			 WHERE merged_into = '' AND lower(display_name) = lower(?)
			 ORDER BY updated_at DESC
			 LIMIT 1`, name).Scan(&id)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("resolve alias: %w", err)
	}
	p, err := s.GetPrincipal(ctx, id)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// EnsureAnonymousContact returns (or creates) the anonymous-bucket principal
// for an (app, handleID OR displayHandle) tuple. The bucket lets chat
// renderers consistently tag unknown senders so they can be grouped and
// promoted to a real contact later via the onboarding UI.
//
// The dedup key uses handleID when present, falling back to displayHandle.
// This keeps the anonymous-buckets table from exploding when the same
// untagged sender appears across many sessions.
func (s *Store) EnsureAnonymousContact(ctx context.Context, app, handleID, displayHandle string) (PrincipalRow, error) {
	app = strings.TrimSpace(strings.ToLower(app))
	handleID = strings.TrimSpace(handleID)
	displayHandle = strings.TrimSpace(displayHandle)
	if app == "" || (handleID == "" && displayHandle == "") {
		return PrincipalRow{}, errors.New("EnsureAnonymousContact: need app and at least one identifier")
	}

	if handleID != "" {
		if existing, err := s.ResolveByAppHandle(ctx, app, handleID); err != nil {
			return PrincipalRow{}, err
		} else if existing != nil {
			return *existing, nil
		}
	}
	if displayHandle != "" {
		if existing, err := s.ResolveByDisplayHandle(ctx, app, displayHandle); err != nil {
			return PrincipalRow{}, err
		} else if existing != nil {
			return *existing, nil
		}
	}

	displayName := displayHandle
	if displayName == "" {
		displayName = handleID
	}
	created, err := s.CreatePrincipal(ctx, PrincipalRow{
		Kind:        PrincipalKindAnonymous,
		DisplayName: displayName,
		CreatedFrom: "auto_from_chat",
		Confidence:  0.3,
	})
	if err != nil {
		return PrincipalRow{}, err
	}
	if _, err := s.BindAppHandle(ctx, created.ID, app, handleID, displayHandle); err != nil {
		return PrincipalRow{}, fmt.Errorf("bind anonymous handle: %w", err)
	}
	return created, nil
}

// PromoteAnonymousToContact converts an anonymous bucket into a named
// contact. Aliases are appended; existing handles are preserved.
func (s *Store) PromoteAnonymousToContact(ctx context.Context, principalID, displayName, relationship string, aliases []string) (PrincipalRow, error) {
	row, err := s.GetPrincipal(ctx, principalID)
	if err != nil {
		return PrincipalRow{}, err
	}
	if row.Kind != PrincipalKindAnonymous {
		return PrincipalRow{}, fmt.Errorf("principal %s is not anonymous (kind=%s)", principalID, row.Kind)
	}
	now := time.Now().UnixMilli()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return PrincipalRow{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `
		UPDATE principals
		   SET kind = ?, display_name = ?, relationship = ?, created_from = ?, confidence = ?, updated_at = ?
		 WHERE id = ?`,
		PrincipalKindContact, displayName, relationship, "manual_promotion", 1.0, now, principalID,
	); err != nil {
		return PrincipalRow{}, fmt.Errorf("promote principal: %w", err)
	}
	if err := writeAliasesTx(ctx, tx, principalID, aliases); err != nil {
		return PrincipalRow{}, err
	}
	updated, err := getPrincipalTx(ctx, tx, principalID)
	if err != nil {
		return PrincipalRow{}, err
	}
	if err := tx.Commit(); err != nil {
		return PrincipalRow{}, fmt.Errorf("commit: %w", err)
	}
	return updated, nil
}

// ── helpers ─────────────────────────────────────────────────────────

type dbExecutor interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func writeAliasesTx(ctx context.Context, tx *sql.Tx, principalID string, aliases []string) error {
	seen := map[string]struct{}{}
	for _, a := range aliases {
		a = strings.TrimSpace(a)
		if a == "" {
			continue
		}
		key := strings.ToLower(a)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		if _, err := tx.ExecContext(ctx,
			"INSERT OR IGNORE INTO principal_aliases (principal_id, alias) VALUES (?, ?)",
			principalID, a,
		); err != nil {
			return fmt.Errorf("insert alias %q: %w", a, err)
		}
	}
	return nil
}

func getPrincipalTx(ctx context.Context, q dbExecutor, id string) (PrincipalRow, error) {
	var p PrincipalRow
	row := q.QueryRowContext(ctx, `
		SELECT id, kind, display_name, relationship, notes, created_from, source, external_ref, confidence, created_at, updated_at
		  FROM principals
		 WHERE id = ? AND merged_into = ''`, id)
	if err := row.Scan(&p.ID, &p.Kind, &p.DisplayName, &p.Relationship, &p.Notes, &p.CreatedFrom, &p.Source, &p.ExternalRef, &p.Confidence, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return PrincipalRow{}, err
	}
	aliases, err := loadAliasesTx(ctx, q, id)
	if err != nil {
		return PrincipalRow{}, err
	}
	p.Aliases = aliases
	return p, nil
}

func loadAliasesTx(ctx context.Context, q dbExecutor, principalID string) ([]string, error) {
	rows, err := q.QueryContext(ctx,
		"SELECT alias FROM principal_aliases WHERE principal_id = ? ORDER BY alias", principalID)
	if err != nil {
		return nil, fmt.Errorf("load aliases: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			return nil, fmt.Errorf("scan alias: %w", err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iter aliases: %w", err)
	}
	return out, nil
}

func isSelfUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	// modernc.org/sqlite formats unique violations as
	//   "constraint failed: UNIQUE constraint failed: ..." or with the index name.
	return strings.Contains(msg, "uq_principals_self") ||
		(strings.Contains(msg, "UNIQUE") && strings.Contains(msg, "principals.kind"))
}

// ── Principal sightings ──

// defaultRecentMessagesWindow is the rolling-window size kept per principal
// in principal_recent_messages. Picked as a compromise: large enough that
// the Who you know page detail panel can show "what did X recently say" without
// re-querying parsed content, small enough that a hot principal (frequent
// chat partner) doesn't blow the table out over months.
const defaultRecentMessagesWindow = 20

// BumpPrincipalSighting records a single resolved-sender event for a
// principal in a given app. The (principal_id, app) sighting row is
// UPSERTed: first call inserts with first_seen_at = last_seen_at = ts and
// count = 1; subsequent calls bump count, max-update last_seen_at, and
// leave first_seen_at alone.
//
// When sessionID is non-empty (the common case during render — see
// sender_labeler), a row is also UPSERTed into principal_session_sightings
// so the Who you know page can drill from a principal to the sessions they
// appeared in. Pass "" when there is no surrounding session (debug calls,
// tests).
//
// When preview is non-empty, a row is also appended to
// principal_recent_messages and the rolling window is trimmed to
// defaultRecentMessagesWindow rows. preview should already be the redacted
// short form (caller-side rune-safe truncate) — we store it verbatim.
func (s *Store) BumpPrincipalSighting(ctx context.Context, principalID, app, sessionID string, ts int64, preview string) error {
	principalID = strings.TrimSpace(principalID)
	app = strings.TrimSpace(strings.ToLower(app))
	sessionID = strings.TrimSpace(sessionID)
	if principalID == "" || app == "" {
		return fmt.Errorf("BumpPrincipalSighting: empty principalID or app")
	}
	if ts <= 0 {
		ts = time.Now().UnixMilli()
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	if err := requirePrincipalExists(ctx, tx, principalID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO principal_merge_updates
        (merge_id,principal_id,app,session_id,message_count,first_seen_at,last_seen_at)
        SELECT id,?,?,?,1,?,? FROM principal_merges WHERE target_id=? AND undone_at=0
        ON CONFLICT(merge_id,principal_id,app,session_id) DO UPDATE SET
        message_count=message_count+1,first_seen_at=MIN(first_seen_at,excluded.first_seen_at),last_seen_at=MAX(last_seen_at,excluded.last_seen_at)`, principalID, app, sessionID, ts, ts, principalID); err != nil {
		return err
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO principal_sightings (principal_id, app, message_count, first_seen_at, last_seen_at)
		VALUES (?, ?, 1, ?, ?)
		ON CONFLICT(principal_id, app) DO UPDATE SET
			message_count = message_count + 1,
			last_seen_at  = MAX(last_seen_at, excluded.last_seen_at)
	`, principalID, app, ts, ts); err != nil {
		return fmt.Errorf("upsert sighting: %w", err)
	}

	// Reversible contact links retain the original source contribution and
	// record new observations separately from that baseline.
	if _, err := tx.ExecContext(ctx, `INSERT INTO principal_contact_link_updates
		(principal_id,app,session_id,message_count,first_seen_at,last_seen_at)
		SELECT principal_id,?,?,1,?,? FROM principal_contact_links WHERE principal_id=?
		ON CONFLICT(principal_id,app,session_id) DO UPDATE SET
		message_count=message_count+1,first_seen_at=MIN(first_seen_at,excluded.first_seen_at),last_seen_at=MAX(last_seen_at,excluded.last_seen_at)`, app, sessionID, ts, ts, principalID); err != nil {
		return fmt.Errorf("record linked contact observation: %w", err)
	}

	if sessionID != "" {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO principal_session_sightings (principal_id, session_id, first_seen_at, last_seen_at)
			VALUES (?, ?, ?, ?)
			ON CONFLICT(principal_id, session_id) DO UPDATE SET
				first_seen_at = MIN(first_seen_at, excluded.first_seen_at),
				last_seen_at  = MAX(last_seen_at, excluded.last_seen_at)
		`, principalID, sessionID, ts, ts); err != nil {
			return fmt.Errorf("upsert session sighting: %w", err)
		}
	}

	preview = strings.TrimSpace(preview)
	if preview != "" {
		// seen_at is the primary key, so two preview rows landing in the
		// same millisecond would collide. ON CONFLICT DO NOTHING is the
		// safe choice — the second writer's preview is dropped, the
		// first wins, and we never block.
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO principal_recent_messages (principal_id, app, seen_at, preview)
			VALUES (?, ?, ?, ?)
			ON CONFLICT(principal_id, seen_at) DO NOTHING
		`, principalID, app, ts, preview); err != nil {
			return fmt.Errorf("insert recent message: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
			DELETE FROM principal_recent_messages
			 WHERE principal_id = ?
			   AND seen_at NOT IN (
			       SELECT seen_at FROM principal_recent_messages
			        WHERE principal_id = ?
			        ORDER BY seen_at DESC
			        LIMIT ?
			   )
		`, principalID, principalID, defaultRecentMessagesWindow); err != nil {
			return fmt.Errorf("trim recent messages: %w", err)
		}
	}

	return tx.Commit()
}

// PrincipalLastSeenAt returns the latest sighting of a principal in any app, or
// zero when it has never been sighted. Read it before BumpPrincipalSighting so
// a reappearance can be told apart from a conversation that is still going.
func (s *Store) PrincipalLastSeenAt(ctx context.Context, principalID string) (int64, error) {
	var last int64
	err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(last_seen_at), 0) FROM principal_sightings WHERE principal_id = ?`,
		strings.TrimSpace(principalID)).Scan(&last)
	return last, err
}

// ListPrincipalSessionSightings returns up to limit sessions the principal
// appeared in, newest first. The activity_sessions table itself is not
// joined here — callers that need session metadata (start/end time, app
// names) can JOIN at the App-binding layer.
func (s *Store) ListPrincipalSessionSightings(ctx context.Context, principalID string, limit int) ([]PrincipalSessionSightingRow, error) {
	return s.ListPrincipalSessionSightingsPage(ctx, principalID, limit, 0)
}

// ListPrincipalSessionSightingsPage visits all provenance with bounded pages.
// Stable ordering avoids skipping sessions when sightings have equal times.
func (s *Store) ListPrincipalSessionSightingsPage(ctx context.Context, principalID string, limit, offset int) ([]PrincipalSessionSightingRow, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT principal_id, session_id, first_seen_at, last_seen_at
		  FROM principal_session_sightings
		 WHERE principal_id = ? AND EXISTS (SELECT 1 FROM principals p WHERE p.id=principal_id AND p.merged_into='')
		 ORDER BY last_seen_at DESC, session_id ASC
		 LIMIT ? OFFSET ?
	`, principalID, limit, max(0, offset))
	if err != nil {
		return nil, fmt.Errorf("query session sightings: %w", err)
	}
	defer rows.Close()
	var out []PrincipalSessionSightingRow
	for rows.Next() {
		var r PrincipalSessionSightingRow
		if err := rows.Scan(&r.PrincipalID, &r.SessionID, &r.FirstSeenAt, &r.LastSeenAt); err != nil {
			return nil, fmt.Errorf("scan session sighting: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iter session sightings: %w", err)
	}
	return out, nil
}

// ListPrincipalSessionSightingsForActivePrincipals returns all active sightings
// grouped by principal, preserving ListPrincipalSessionSightingsPage's
// per-principal order. The People read path applies its read-revision fence
// around this batch.
func (s *Store) ListPrincipalSessionSightingsForActivePrincipals(ctx context.Context) (map[string][]PrincipalSessionSightingRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT pss.principal_id, pss.session_id, pss.first_seen_at, pss.last_seen_at
		  FROM principal_session_sightings pss
		 WHERE EXISTS (SELECT 1 FROM principals p WHERE p.id=pss.principal_id AND p.merged_into='')`)
	if err != nil {
		return nil, fmt.Errorf("query session sightings for active principals: %w", err)
	}
	defer rows.Close()
	out := make(map[string][]PrincipalSessionSightingRow)
	for rows.Next() {
		var r PrincipalSessionSightingRow
		if err := rows.Scan(&r.PrincipalID, &r.SessionID, &r.FirstSeenAt, &r.LastSeenAt); err != nil {
			return nil, fmt.Errorf("scan session sighting: %w", err)
		}
		out[r.PrincipalID] = append(out[r.PrincipalID], r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iter session sightings for active principals: %w", err)
	}
	for principalID, sightings := range out {
		sort.Slice(sightings, func(i, j int) bool {
			if sightings[i].LastSeenAt != sightings[j].LastSeenAt {
				return sightings[i].LastSeenAt > sightings[j].LastSeenAt
			}
			return sightings[i].SessionID < sightings[j].SessionID
		})
		out[principalID] = sightings
	}
	return out, nil
}

// ListSessionPrincipalSightings returns every principal that was seen
// at least once in the given session, newest sighting first. JOINS
// principals so the caller can render display_name + kind without a
// second round-trip. Sightings for principals that were since deleted
// are filtered out by the inner JOIN (principal_session_sightings
// cascades on principals delete anyway, but defending against drift).
func (s *Store) ListSessionPrincipalSightings(ctx context.Context, sessionID string, limit int) ([]SessionPrincipalRow, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT pss.principal_id, p.display_name, p.kind,
		       pss.first_seen_at, pss.last_seen_at
		  FROM principal_session_sightings pss
		  JOIN principals p ON p.id = pss.principal_id
		 WHERE pss.session_id = ? AND p.merged_into = ''
		 ORDER BY pss.last_seen_at DESC
		 LIMIT ?
	`, sessionID, limit)
	if err != nil {
		return nil, fmt.Errorf("query session principals: %w", err)
	}
	defer rows.Close()
	var out []SessionPrincipalRow
	for rows.Next() {
		var r SessionPrincipalRow
		if err := rows.Scan(&r.PrincipalID, &r.DisplayName, &r.Kind, &r.FirstSeenAt, &r.LastSeenAt); err != nil {
			return nil, fmt.Errorf("scan session principal: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iter session principals: %w", err)
	}
	return out, nil
}

// GetPrincipalStats returns the aggregate row for a single principal. The
// returned struct is always populated (zero counters for principals with
// no sightings yet) so the frontend can render a stats panel without a
// nil check. The principal must exist; missing IDs return sql.ErrNoRows.
func (s *Store) GetPrincipalStats(ctx context.Context, principalID string) (PrincipalStatsRow, error) {
	var exists int
	if err := s.db.QueryRowContext(ctx, "SELECT 1 FROM principals WHERE id = ? AND merged_into = ''", principalID).Scan(&exists); err != nil {
		return PrincipalStatsRow{}, err
	}

	row := PrincipalStatsRow{PrincipalID: principalID}

	sightings, err := s.listSightings(ctx, principalID)
	if err != nil {
		return PrincipalStatsRow{}, err
	}
	row.ByApp = sightings
	for _, sg := range sightings {
		row.TotalMessages += sg.MessageCount
		if row.FirstSeenAt == 0 || sg.FirstSeenAt < row.FirstSeenAt {
			row.FirstSeenAt = sg.FirstSeenAt
		}
		if sg.LastSeenAt > row.LastSeenAt {
			row.LastSeenAt = sg.LastSeenAt
		}
	}

	recent, err := s.GetPrincipalRecentMessages(ctx, principalID, defaultRecentMessagesWindow)
	if err != nil {
		return PrincipalStatsRow{}, err
	}
	row.RecentMessages = recent
	return row, nil
}

// GetAllPrincipalStats returns stats for every principal in one round-trip.
// The Who you know page loads everyone at once so this needs to be cheap; the
// aggregate query joins sightings to principals.
func (s *Store) GetAllPrincipalStats(ctx context.Context) (map[string]PrincipalStatsRow, error) {
	out := map[string]PrincipalStatsRow{}

	pids, err := s.allPrincipalIDs(ctx)
	if err != nil {
		return nil, err
	}
	for _, id := range pids {
		out[id] = PrincipalStatsRow{PrincipalID: id}
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT principal_id, app, message_count, first_seen_at, last_seen_at
		  FROM principal_sightings
		 ORDER BY principal_id, last_seen_at DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("query sightings: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var sg PrincipalSightingRow
		if err := rows.Scan(&sg.PrincipalID, &sg.App, &sg.MessageCount, &sg.FirstSeenAt, &sg.LastSeenAt); err != nil {
			return nil, fmt.Errorf("scan sighting: %w", err)
		}
		stats := out[sg.PrincipalID]
		stats.PrincipalID = sg.PrincipalID
		stats.ByApp = append(stats.ByApp, sg)
		stats.TotalMessages += sg.MessageCount
		if stats.FirstSeenAt == 0 || sg.FirstSeenAt < stats.FirstSeenAt {
			stats.FirstSeenAt = sg.FirstSeenAt
		}
		if sg.LastSeenAt > stats.LastSeenAt {
			stats.LastSeenAt = sg.LastSeenAt
		}
		out[sg.PrincipalID] = stats
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iter sightings: %w", err)
	}
	return out, nil
}

// GetPrincipalRecentMessages returns up to limit most-recent preview rows
// for a principal, newest first.
func (s *Store) GetPrincipalRecentMessages(ctx context.Context, principalID string, limit int) ([]PrincipalRecentMessageRow, error) {
	if limit <= 0 {
		limit = defaultRecentMessagesWindow
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT principal_id, app, seen_at, preview
		  FROM principal_recent_messages
		 WHERE principal_id = ? AND EXISTS (SELECT 1 FROM principals p WHERE p.id=principal_id AND p.merged_into='')
		 ORDER BY seen_at DESC
		 LIMIT ?
	`, principalID, limit)
	if err != nil {
		return nil, fmt.Errorf("query recent messages: %w", err)
	}
	defer rows.Close()
	var out []PrincipalRecentMessageRow
	for rows.Next() {
		var r PrincipalRecentMessageRow
		if err := rows.Scan(&r.PrincipalID, &r.App, &r.SeenAt, &r.Preview); err != nil {
			return nil, fmt.Errorf("scan recent message: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iter recent messages: %w", err)
	}
	return out, nil
}

// allPrincipalIDs returns every principal id without fetching the heavier
// metadata. Used by GetAllPrincipalStats to seed zero-stat entries for
// principals that have never been sighted (so the Who you know page can still
// show them in the list).
func (s *Store) allPrincipalIDs(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id FROM principals WHERE merged_into = ''")
	if err != nil {
		return nil, fmt.Errorf("query principal ids: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan principal id: %w", err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iter principal ids: %w", err)
	}
	return out, nil
}

// MergePrincipals moves every alias / app handle / sighting / recent
// message owned by srcID over to dstID, then deletes srcID. Used by the
// Who you know page's "Merge into…" affordance to consolidate duplicate
// principals (e.g. an Anonymous bucket that turned out to be the same
// person as an existing Contact, or two Contacts that were created with
// slightly different aliases for the same person).
//
// Conflict rules (each one mirrors what would happen if the records had
// been entered against dst originally):
//
//   - Principal fields, including source/external_ref, stay with dst.
//     Merging imported handles into a curated person never marks them imported.
//   - Aliases: INSERT OR IGNORE — duplicates already on dst are preserved
//     once; aliases only on src move over.
//   - App handles: keyed by (app, handle_id, display_handle). If dst
//     already has the same tuple, src's row is dropped (its principal_id
//     was overridden by the existing dst binding). Otherwise the row
//     transfers verbatim.
//   - Sightings: keyed by (principal_id, app). When both sides have a row
//     for the same app, counts add, first_seen takes the MIN, last_seen
//     takes the MAX. Otherwise src's row transfers.
//   - Recent messages: keyed by (principal_id, seen_at). Same-key
//     collisions keep dst's preview (the rolling window already drops
//     duplicates intentionally).
//   - Kind promotion: when src is an Anonymous and dst is a Contact, the
//     anonymous-source rows are inherited by the Contact and src is
//     deleted — effectively a fold-in. When src is a Contact and dst is
//     Self, callers should typically be doing this only after a
//     deliberate confirmation; the operation itself is unrestricted.
//
// Returns sql.ErrNoRows if either principal is missing, an explicit error
// if srcID == dstID (a no-op call would still drop one of the rows under
// the cascade), or wraps any DB error from the transaction.
func (s *Store) MergePrincipals(ctx context.Context, srcID, dstID string) (PrincipalRow, error) {
	if strings.TrimSpace(srcID) == "" || strings.TrimSpace(dstID) == "" {
		return PrincipalRow{}, errors.New("MergePrincipals: empty src or dst id")
	}
	if srcID == dstID {
		return PrincipalRow{}, errors.New("MergePrincipals: src and dst are the same")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return PrincipalRow{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	var linked bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM principal_contact_links WHERE principal_id = ?)`, srcID).Scan(&linked); err != nil {
		return PrincipalRow{}, err
	}
	if linked {
		return PrincipalRow{}, errors.New("unlink the contact before merging these people")
	}
	if err := trackMergedContactActivityTx(ctx, tx, srcID, dstID); err != nil {
		return PrincipalRow{}, err
	}
	row, err := mergePrincipalsTx(ctx, tx, srcID, dstID)
	if err != nil {
		return PrincipalRow{}, err
	}
	if err := tx.Commit(); err != nil {
		return PrincipalRow{}, err
	}
	return row, nil
}

func mergePrincipalsTx(ctx context.Context, tx *sql.Tx, srcID, dstID string) (PrincipalRow, error) {
	return mergePrincipalRowsTx(ctx, tx, srcID, dstID, false)
}

func mergePrincipalRowsTx(ctx context.Context, tx *sql.Tx, srcID, dstID string, reversible bool) (PrincipalRow, error) {
	// Existence checks first so a missing principal does not silently
	// half-mutate the other tables.
	if err := requirePrincipalExists(ctx, tx, srcID); err != nil {
		return PrincipalRow{}, fmt.Errorf("src %s: %w", srcID, err)
	}
	if err := requirePrincipalExists(ctx, tx, dstID); err != nil {
		return PrincipalRow{}, fmt.Errorf("dst %s: %w", dstID, err)
	}

	// 1. Aliases: merge into dst with dedup.
	if _, err := tx.ExecContext(ctx, `
		INSERT OR IGNORE INTO principal_aliases (principal_id, alias)
		SELECT ?, alias FROM principal_aliases WHERE principal_id = ?
	`, dstID, srcID); err != nil {
		return PrincipalRow{}, fmt.Errorf("merge aliases: %w", err)
	}
	// Source aliases will cascade-delete with the src principal at the
	// end; no explicit cleanup needed.

	// 2. App handles: transfer rows whose (app, handle_id, display_handle)
	//    is not already bound to dst. Bind via UPDATE because that tuple
	//    is the UNIQUE key; INSERT…ON CONFLICT would require dst's own
	//    row to differ on principal_id and risk swapping ownership the
	//    wrong way.
	if _, err := tx.ExecContext(ctx, `
		UPDATE app_handles
		   SET principal_id = ?
		 WHERE principal_id = ?
		   AND NOT EXISTS (
		       SELECT 1 FROM app_handles dh
		        WHERE dh.principal_id = ?
		          AND dh.app = app_handles.app
		          AND dh.handle_id = app_handles.handle_id
		          AND dh.display_handle = app_handles.display_handle
		   )
	`, dstID, srcID, dstID); err != nil {
		return PrincipalRow{}, fmt.Errorf("merge app_handles: %w", err)
	}
	// Any handle that didn't transfer (because dst already owned an
	// equivalent binding) gets dropped along with the src principal.

	// 3. Sightings: per-(app) merge with count sum and timestamp min/max.
	//    SQLite lacks ON CONFLICT … DO UPDATE on partial PK syntax that
	//    chains across SELECTs cleanly, so we do this in two passes —
	//    update where dst already has the row, then insert the rest.
	if _, err := tx.ExecContext(ctx, `
		UPDATE principal_sightings AS d
		   SET message_count = d.message_count + s.message_count,
		       first_seen_at = MIN(d.first_seen_at, s.first_seen_at),
		       last_seen_at  = MAX(d.last_seen_at, s.last_seen_at)
		  FROM principal_sightings AS s
		 WHERE s.principal_id = ?
		   AND d.principal_id = ?
		   AND d.app = s.app
	`, srcID, dstID); err != nil {
		return PrincipalRow{}, fmt.Errorf("merge sightings (update): %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO principal_sightings
		    (principal_id, app, message_count, first_seen_at, last_seen_at)
		SELECT ?, app, message_count, first_seen_at, last_seen_at
		  FROM principal_sightings
		 WHERE principal_id = ?
		   AND app NOT IN (SELECT app FROM principal_sightings WHERE principal_id = ?)
	`, dstID, srcID, dstID); err != nil {
		return PrincipalRow{}, fmt.Errorf("merge sightings (insert): %w", err)
	}

	// 4. Recent messages: transfer non-colliding rows. Same-seen_at on
	//    dst wins (dst's preview was authored by dst's renderer).
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO principal_recent_messages (principal_id, app, seen_at, preview)
		SELECT ?, app, seen_at, preview
		  FROM principal_recent_messages
		 WHERE principal_id = ?
		   AND seen_at NOT IN (SELECT seen_at FROM principal_recent_messages WHERE principal_id = ?)
	`, dstID, srcID, dstID); err != nil {
		return PrincipalRow{}, fmt.Errorf("merge recent_messages: %w", err)
	}

	// 4b. Session sightings: per-session merge with MIN(first_seen) +
	//     MAX(last_seen) when both sides reference the same session.
	//     Two-pass mirrors the per-app sightings pattern above.
	if _, err := tx.ExecContext(ctx, `
		UPDATE principal_session_sightings AS d
		   SET first_seen_at = MIN(d.first_seen_at, s.first_seen_at),
		       last_seen_at  = MAX(d.last_seen_at, s.last_seen_at)
		  FROM principal_session_sightings AS s
		 WHERE s.principal_id = ?
		   AND d.principal_id = ?
		   AND d.session_id = s.session_id
	`, srcID, dstID); err != nil {
		return PrincipalRow{}, fmt.Errorf("merge session_sightings (update): %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO principal_session_sightings
		    (principal_id, session_id, first_seen_at, last_seen_at)
		SELECT ?, session_id, first_seen_at, last_seen_at
		  FROM principal_session_sightings
		 WHERE principal_id = ?
		   AND session_id NOT IN (SELECT session_id FROM principal_session_sightings WHERE principal_id = ?)
	`, dstID, srcID, dstID); err != nil {
		return PrincipalRow{}, fmt.Errorf("merge session_sightings (insert): %w", err)
	}

	if reversible {
		// Match the old cascade for aggregated rows, retaining the source profile.
		for _, table := range []string{"principal_aliases", "principal_sightings", "principal_recent_messages", "principal_session_sightings"} {
			if _, err := tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE principal_id = ?", srcID); err != nil {
				return PrincipalRow{}, err
			}
		}
		if _, err := tx.ExecContext(ctx, "UPDATE principals SET merged_into = ? WHERE id = ?", dstID, srcID); err != nil {
			return PrincipalRow{}, err
		}
	} else if _, err := tx.ExecContext(ctx, "DELETE FROM principals WHERE id = ?", srcID); err != nil {
		return PrincipalRow{}, fmt.Errorf("delete src principal: %w", err)
	}

	// 6. Bump dst's updated_at so the Who you know page surfaces the change.
	if _, err := tx.ExecContext(ctx,
		"UPDATE principals SET updated_at = ? WHERE id = ?", time.Now().UnixMilli(), dstID,
	); err != nil {
		return PrincipalRow{}, fmt.Errorf("bump dst updated_at: %w", err)
	}

	final, err := getPrincipalTx(ctx, tx, dstID)
	if err != nil {
		return PrincipalRow{}, fmt.Errorf("read merged principal: %w", err)
	}
	return final, nil
}

// CleanupReport summarises a CleanupAnonymousPrincipals run so the UI can
// show "we tidied N entries, merging M duplicates" without re-querying.
type CleanupReport struct {
	Scanned           int      // total Anonymous principals examined
	Renamed           int      // principals whose displayName was normalized in place
	Merged            int      // anonymous principals folded into another anonymous on the SAME app(s)
	CrossAppMerged    int      // anonymous principals folded into another anonymous on a DIFFERENT app (same nickname → likely same real person)
	MergedIntoNamed   int      // anonymous principals folded into a Self / Contact whose name (or alias) matched
	MergedIDs         []string // ids of the principals that were deleted via merge (covers Merged, CrossAppMerged, and MergedIntoNamed)
	PrunedNonChat     int      // anonymous principals deleted because all sightings came from non-chat apps
	PrunedImplausible int      // anonymous principals deleted because the display name was message body / system text, not a real name
}

// CleanupAnonymousPrincipals tidies the long tail of auto-arrived
// Anonymous buckets in three passes:
//
//  1. Prune: when isImplausibleName is non-nil, any Anonymous principal
//     whose display name is message body / system text rather than a real
//     name (a Discord welcome card "<x> Server Tag: A\ I'm new here, say
//     hi!" is the canonical leak) is deleted outright — the upstream
//     guard now blocks these at ingestion, so this pass clears the rows
//     created before the guard existed. Then, when isChatApp is non-nil,
//     any Anonymous principal whose sightings are exclusively from apps
//     that aren't chat apps (Mail.app newsletters, Sentry alerts, GitHub
//     PR bot, etc.) is deleted — these are services / brands / one-off
//     email addresses, not people. Anonymous principals with zero
//     sightings at all are also pruned as dead weight from earlier bugs.
//  2. Rename: apply normalize to each surviving Anonymous's display
//     name (strips trailing timestamps / role badges / message body
//     bleed from upstream parser leaks).
//  3. Merge: fold duplicates that collapse to the same normalized
//     name into the oldest survivor so sightings, recent messages,
//     handles, and session links transfer intact.
//
// All three callbacks are supplied by the caller (kept package-free so
// storage doesn't depend on the identity layer) — pass
// identity.NormalizeSenderName, identity.IsChatApp, and a wrapper around
// identity.PlausibleAutoContactName from the wiring layer. Any may be nil
// to skip that pass.
//
// Each anonymous row is examined in updated_at ASC order so the OLDEST
// row "wins" when duplicates exist (its principal_id is the merge
// target; newer duplicates fold into it). This preserves the longest
// sighting history.
func (s *Store) CleanupAnonymousPrincipals(ctx context.Context, normalize func(string) string, isChatApp func(string) bool, isImplausibleName func(string) bool) (CleanupReport, error) {
	if normalize == nil {
		return CleanupReport{}, fmt.Errorf("CleanupAnonymousPrincipals: nil normalize")
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, display_name
		  FROM principals
		 WHERE kind = ? AND merged_into = ''
		 ORDER BY updated_at ASC, id ASC`, PrincipalKindAnonymous)
	if err != nil {
		return CleanupReport{}, fmt.Errorf("list anonymous: %w", err)
	}
	type anonRow struct {
		id   string
		name string
	}
	var anons []anonRow
	for rows.Next() {
		var r anonRow
		if err := rows.Scan(&r.id, &r.name); err != nil {
			rows.Close()
			return CleanupReport{}, fmt.Errorf("scan anonymous: %w", err)
		}
		anons = append(anons, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return CleanupReport{}, fmt.Errorf("iter anonymous: %w", err)
	}
	rows.Close()

	report := CleanupReport{Scanned: len(anons)}

	// Pass 1: prune Anonymous principals whose sightings are all from
	// non-chat apps (or who have zero sightings — orphans from earlier
	// auto-bucket bugs). Keep a survivors list for the rename/merge
	// passes below.
	survivors := make([]anonRow, 0, len(anons))
	for _, a := range anons {
		if isImplausibleName != nil && isImplausibleName(a.name) {
			if err := s.DeletePrincipal(ctx, a.id); err != nil {
				return report, fmt.Errorf("prune implausible %s: %w", a.id, err)
			}
			report.PrunedImplausible++
			continue
		}
		if isChatApp != nil {
			sightings, err := s.listSightings(ctx, a.id)
			if err != nil {
				return report, fmt.Errorf("list sightings for %s: %w", a.id, err)
			}
			anyChat := false
			for _, sg := range sightings {
				if isChatApp(sg.App) {
					anyChat = true
					break
				}
			}
			if !anyChat {
				if err := s.DeletePrincipal(ctx, a.id); err != nil {
					return report, fmt.Errorf("prune %s: %w", a.id, err)
				}
				report.PrunedNonChat++
				continue
			}
		}
		survivors = append(survivors, a)
	}

	// Pre-build a name index covering Self + Contacts (display names AND
	// aliases). Used by the rename/merge pass below so an Anonymous whose
	// normalized name matches a real named principal gets folded INTO that
	// principal instead of being kept as a separate Anonymous "yetone"
	// alongside Self "yetone". Without this pass the user would see
	// duplicates after the parser pre-normalization era — the original
	// Anonymous would never merge to Self even after a Clean up.
	namedIndex, err := s.buildNamedNameIndex(ctx)
	if err != nil {
		return report, fmt.Errorf("build named index: %w", err)
	}

	// Pass 2+3: rename + merge survivors.
	//
	// For each Anonymous, normalize the name. Then in order:
	//   a. If the cleaned name matches an existing Self or Contact, merge
	//      the Anonymous INTO that named principal (cross-kind fold).
	//   b. If another Anonymous already claimed the cleaned name in this
	//      pass, merge into that Anonymous (intra-kind fold).
	//   c. Otherwise this Anonymous wins the name — rename if needed.
	winners := make(map[string]string, len(survivors))
	for _, a := range survivors {
		clean := strings.TrimSpace(normalize(a.name))
		if clean == "" {
			clean = strings.TrimSpace(a.name)
		}
		if clean == "" {
			continue
		}
		key := strings.ToLower(clean)

		// a) named principal absorbs the Anonymous.
		if namedID, ok := namedIndex[key]; ok && namedID != a.id {
			if _, err := s.MergePrincipals(ctx, a.id, namedID); err != nil {
				return report, fmt.Errorf("merge %s into named %s: %w", a.id, namedID, err)
			}
			report.MergedIntoNamed++
			report.MergedIDs = append(report.MergedIDs, a.id)
			continue
		}

		// b) another Anonymous already owns this name in this pass.
		if existingID, ok := winners[key]; ok {
			// Classify the merge as same-app (typical: parser leak left
			// two Anonymous on Discord both called "yetone" — they're
			// the same person on the same platform) vs cross-app (the
			// same nickname appeared on WeChat AND Discord — strong
			// signal it's the same real person, but a different kind
			// of merge the user might want to audit separately). Look
			// at sightings for both sides and check for any overlap.
			crossApp, err := anonymousMergeIsCrossApp(ctx, s, a.id, existingID)
			if err != nil {
				return report, fmt.Errorf("classify merge %s→%s: %w", a.id, existingID, err)
			}
			if _, err := s.MergePrincipals(ctx, a.id, existingID); err != nil {
				return report, fmt.Errorf("merge %s into %s: %w", a.id, existingID, err)
			}
			if crossApp {
				report.CrossAppMerged++
			} else {
				report.Merged++
			}
			report.MergedIDs = append(report.MergedIDs, a.id)
			continue
		}

		// c) this Anonymous wins the name.
		winners[key] = a.id
		if clean != a.name {
			if _, err := s.db.ExecContext(ctx,
				"UPDATE principals SET display_name = ?, updated_at = ? WHERE id = ?",
				clean, time.Now().UnixMilli(), a.id,
			); err != nil {
				return report, fmt.Errorf("rename %s: %w", a.id, err)
			}
			report.Renamed++
		}
	}
	return report, nil
}

// anonymousMergeIsCrossApp reports whether merging src into dst would
// fold together two anonymous principals whose sighting apps don't
// overlap — the strong "same nickname across different apps → likely
// the same real person" signal we want to count separately in the
// cleanup report. An empty sighting list on either side returns false
// (treat as same-app for accounting; the merge still proceeds).
func anonymousMergeIsCrossApp(ctx context.Context, s *Store, srcID, dstID string) (bool, error) {
	srcApps, err := listSightingAppSet(ctx, s, srcID)
	if err != nil {
		return false, err
	}
	dstApps, err := listSightingAppSet(ctx, s, dstID)
	if err != nil {
		return false, err
	}
	if len(srcApps) == 0 || len(dstApps) == 0 {
		return false, nil
	}
	for app := range srcApps {
		if _, overlap := dstApps[app]; overlap {
			return false, nil
		}
	}
	return true, nil
}

func listSightingAppSet(ctx context.Context, s *Store, principalID string) (map[string]struct{}, error) {
	sightings, err := s.listSightings(ctx, principalID)
	if err != nil {
		return nil, err
	}
	out := make(map[string]struct{}, len(sightings))
	for _, sg := range sightings {
		out[strings.ToLower(strings.TrimSpace(sg.App))] = struct{}{}
	}
	return out, nil
}

// buildNamedNameIndex returns a lower-case lookup of every Self / Contact
// display name and alias → principal_id. When the same key is bound by
// multiple principals (e.g. two contacts share an alias) the last write
// wins; this is intentional — duplicates among named principals are a
// merge candidate themselves but not the job of CleanupAnonymousPrincipals.
func (s *Store) buildNamedNameIndex(ctx context.Context) (map[string]string, error) {
	out := map[string]string{}

	displayRows, err := s.db.QueryContext(ctx, `
		SELECT id, display_name FROM principals
		 WHERE merged_into = '' AND kind IN (?, ?)
		   AND display_name <> ''`, PrincipalKindSelf, PrincipalKindContact)
	if err != nil {
		return nil, fmt.Errorf("query named display_names: %w", err)
	}
	for displayRows.Next() {
		var id, name string
		if err := displayRows.Scan(&id, &name); err != nil {
			displayRows.Close()
			return nil, fmt.Errorf("scan named display_name: %w", err)
		}
		out[strings.ToLower(strings.TrimSpace(name))] = id
	}
	if err := displayRows.Err(); err != nil {
		displayRows.Close()
		return nil, fmt.Errorf("iter named display_names: %w", err)
	}
	displayRows.Close()

	aliasRows, err := s.db.QueryContext(ctx, `
		SELECT pa.principal_id, pa.alias
		  FROM principal_aliases pa
		  JOIN principals p ON p.id = pa.principal_id
		 WHERE p.merged_into = '' AND p.kind IN (?, ?)`, PrincipalKindSelf, PrincipalKindContact)
	if err != nil {
		return nil, fmt.Errorf("query named aliases: %w", err)
	}
	for aliasRows.Next() {
		var id, alias string
		if err := aliasRows.Scan(&id, &alias); err != nil {
			aliasRows.Close()
			return nil, fmt.Errorf("scan named alias: %w", err)
		}
		key := strings.ToLower(strings.TrimSpace(alias))
		if key == "" {
			continue
		}
		// Display name wins over alias if both happen to hit the same key.
		if _, ok := out[key]; !ok {
			out[key] = id
		}
	}
	if err := aliasRows.Err(); err != nil {
		aliasRows.Close()
		return nil, fmt.Errorf("iter named aliases: %w", err)
	}
	aliasRows.Close()

	return out, nil
}

func requirePrincipalExists(ctx context.Context, tx *sql.Tx, id string) error {
	var n int
	err := tx.QueryRowContext(ctx, "SELECT 1 FROM principals WHERE id = ? AND merged_into = ''", id).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return sql.ErrNoRows
	}
	return err
}

func (s *Store) listSightings(ctx context.Context, principalID string) ([]PrincipalSightingRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT principal_id, app, message_count, first_seen_at, last_seen_at
		  FROM principal_sightings
		 WHERE principal_id = ?
		 ORDER BY message_count DESC, app
	`, principalID)
	if err != nil {
		return nil, fmt.Errorf("query sightings: %w", err)
	}
	defer rows.Close()
	var out []PrincipalSightingRow
	for rows.Next() {
		var sg PrincipalSightingRow
		if err := rows.Scan(&sg.PrincipalID, &sg.App, &sg.MessageCount, &sg.FirstSeenAt, &sg.LastSeenAt); err != nil {
			return nil, fmt.Errorf("scan sighting: %w", err)
		}
		out = append(out, sg)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iter sightings: %w", err)
	}
	return out, nil
}

// PrincipalIDs returns the id of every stored principal, merged ones
// included. Saved page snapshots check it to find people deleted since.
func (s *Store) PrincipalIDs(ctx context.Context) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM principals`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}
