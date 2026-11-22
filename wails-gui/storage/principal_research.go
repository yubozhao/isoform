package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// GetPrincipalResearch returns the dossier row for a principal. When no row
// exists yet it returns a zero-value row with Status == idle (never
// sql.ErrNoRows), so callers can treat "never researched" uniformly.
func (s *Store) GetPrincipalResearch(ctx context.Context, principalID string) (PrincipalResearchRow, error) {
	if principalID == "" {
		return PrincipalResearchRow{}, fmt.Errorf("GetPrincipalResearch: empty principal id")
	}
	var (
		r          PrincipalResearchRow
		sourcesRaw string
	)
	err := s.committedReader().QueryRowContext(ctx, `
		SELECT principal_id, status, summary, markdown, sources, model, error, researched_at, created_at, updated_at
		  FROM principal_research
		 WHERE principal_id = ? AND EXISTS (SELECT 1 FROM principals p WHERE p.id=principal_id AND p.merged_into='')`, principalID,
	).Scan(&r.PrincipalID, &r.Status, &r.Summary, &r.Markdown, &sourcesRaw, &r.Model, &r.Error, &r.ResearchedAt, &r.CreatedAt, &r.UpdatedAt)
	switch {
	case err == sql.ErrNoRows:
		return PrincipalResearchRow{PrincipalID: principalID, Status: PrincipalResearchStatusIdle}, nil
	case err != nil:
		return PrincipalResearchRow{}, fmt.Errorf("query principal research: %w", err)
	}
	r.Sources = decodeResearchSources(sourcesRaw)
	return r, nil
}

// UpsertPrincipalResearchStatus sets just the status (and error string) for a
// principal, creating the row if needed. Used to flip a dossier into
// "researching" before a background run and into "failed" on error without
// touching the markdown/summary of any previous successful run.
func (s *Store) UpsertPrincipalResearchStatus(ctx context.Context, principalID, status, errMsg string) error {
	if principalID == "" {
		return fmt.Errorf("UpsertPrincipalResearchStatus: empty principal id")
	}
	now := time.Now().UnixMilli()
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO principal_research (principal_id, status, error, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(principal_id) DO UPDATE SET
			status     = excluded.status,
			error      = excluded.error,
			updated_at = excluded.updated_at`,
		principalID, status, errMsg, now, now,
	)
	if err != nil {
		return fmt.Errorf("upsert principal research status: %w", err)
	}
	return nil
}

// SavePrincipalResearch persists a completed dossier, marking it ready and
// stamping researched_at. Sources is JSON-encoded into the sources column.
func (s *Store) SavePrincipalResearch(ctx context.Context, r PrincipalResearchRow) error {
	if r.PrincipalID == "" {
		return fmt.Errorf("SavePrincipalResearch: empty principal id")
	}
	now := time.Now().UnixMilli()
	if r.Status == "" {
		r.Status = PrincipalResearchStatusReady
	}
	if r.ResearchedAt == 0 {
		r.ResearchedAt = now
	}
	sourcesRaw, err := json.Marshal(r.Sources)
	if err != nil {
		return fmt.Errorf("encode research sources: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO principal_research
			(principal_id, status, summary, markdown, sources, model, error, researched_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, '', ?, ?, ?)
		ON CONFLICT(principal_id) DO UPDATE SET
			status        = excluded.status,
			summary       = excluded.summary,
			markdown      = excluded.markdown,
			sources       = excluded.sources,
			model         = excluded.model,
			error         = '',
			researched_at = excluded.researched_at,
			updated_at    = excluded.updated_at`,
		r.PrincipalID, r.Status, r.Summary, r.Markdown, string(sourcesRaw), r.Model, r.ResearchedAt, now, now,
	)
	if err != nil {
		return fmt.Errorf("save principal research: %w", err)
	}
	return nil
}

// ResetStaleResearch clears any row left in 'researching' state by an
// interrupted run. The research goroutine does not survive an app restart /
// crash, but the DB row stays 'researching' forever — and the UI disables the
// Research button while researching, so the person can never recover. Called
// once at startup: rows that already hold a dossier revert to 'ready', rows
// that never produced one revert to 'idle'. Returns the number reset.
func (s *Store) ResetStaleResearch(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		UPDATE principal_research
		   SET status = CASE WHEN markdown != '' THEN ? ELSE ? END,
		       error = '',
		       updated_at = ?
		 WHERE status = ?`,
		PrincipalResearchStatusReady, PrincipalResearchStatusIdle,
		time.Now().UnixMilli(), PrincipalResearchStatusResearching,
	)
	if err != nil {
		return 0, fmt.Errorf("reset stale research: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// DeletePrincipalResearch removes the dossier for a principal. Deleting a
// non-existent row is a no-op (no error).
func (s *Store) DeletePrincipalResearch(ctx context.Context, principalID string) error {
	if principalID == "" {
		return fmt.Errorf("DeletePrincipalResearch: empty principal id")
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM principal_research WHERE principal_id = ?`, principalID); err != nil {
		return fmt.Errorf("delete principal research: %w", err)
	}
	return nil
}

// decodeResearchSources tolerantly decodes the sources JSON column; a
// malformed or empty value yields an empty slice rather than an error so a
// corrupt sources blob never blocks rendering the dossier body.
func decodeResearchSources(raw string) []ResearchSource {
	if raw == "" {
		return nil
	}
	var sources []ResearchSource
	if err := json.Unmarshal([]byte(raw), &sources); err != nil {
		return nil
	}
	return sources
}
