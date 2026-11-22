package storage

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"strings"
	"time"
)

// DeleteAllVoiceEmbeddings erases explicit enrollments as part of Privacy's
// Forget voices action. Call-derived signatures are erased in activity.db.
func (s *Store) DeleteAllVoiceEmbeddings(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM principal_voice_embeddings`)
	return err
}

// VoiceEmbeddingRow is one enrolled voice fingerprint sample for a
// principal. Multiple rows per principal are normal — the verification
// path can average them or compare against the best individual match.
type VoiceEmbeddingRow struct {
	ID           int64
	PrincipalID  string
	Embedding    []float32
	Dim          int
	SampleRate   int
	AudioSeconds float64
	Label        string
	CapturedAt   int64
}

// CreateVoiceEmbedding inserts one enrollment sample. The embedding is
// stored as little-endian float32 bytes; dim is recorded separately for
// schema clarity even though it equals len(embedding).
func (s *Store) CreateVoiceEmbedding(ctx context.Context, row VoiceEmbeddingRow) (VoiceEmbeddingRow, error) {
	if strings.TrimSpace(row.PrincipalID) == "" {
		return VoiceEmbeddingRow{}, fmt.Errorf("CreateVoiceEmbedding: empty principalID")
	}
	if len(row.Embedding) == 0 {
		return VoiceEmbeddingRow{}, fmt.Errorf("CreateVoiceEmbedding: empty embedding")
	}
	if row.Dim == 0 {
		row.Dim = len(row.Embedding)
	}
	if row.Dim != len(row.Embedding) {
		return VoiceEmbeddingRow{}, fmt.Errorf("CreateVoiceEmbedding: dim %d mismatches len %d", row.Dim, len(row.Embedding))
	}
	if row.CapturedAt <= 0 {
		row.CapturedAt = time.Now().UnixMilli()
	}
	if row.SampleRate <= 0 {
		row.SampleRate = 16000
	}
	blob := encodeEmbedding(row.Embedding)
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO principal_voice_embeddings
		    (principal_id, embedding, dim, sample_rate, audio_seconds, label, captured_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, row.PrincipalID, blob, row.Dim, row.SampleRate, row.AudioSeconds, row.Label, row.CapturedAt)
	if err != nil {
		return VoiceEmbeddingRow{}, fmt.Errorf("insert voice embedding: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return VoiceEmbeddingRow{}, fmt.Errorf("last insert id: %w", err)
	}
	row.ID = id
	return row, nil
}

// ListVoiceEmbeddingsByPrincipal returns all enrolled samples for a
// principal, newest first.
func (s *Store) ListVoiceEmbeddingsByPrincipal(ctx context.Context, principalID string) ([]VoiceEmbeddingRow, error) {
	rows, err := s.committedReader().QueryContext(ctx, `
		SELECT id, principal_id, embedding, dim, sample_rate, audio_seconds, label, captured_at
		  FROM principal_voice_embeddings
		 WHERE principal_id = ? AND EXISTS (SELECT 1 FROM principals p WHERE p.id=principal_id AND p.merged_into='')
		 ORDER BY captured_at DESC
	`, principalID)
	if err != nil {
		return nil, fmt.Errorf("query voice embeddings: %w", err)
	}
	defer rows.Close()
	var out []VoiceEmbeddingRow
	for rows.Next() {
		var r VoiceEmbeddingRow
		var blob []byte
		if err := rows.Scan(&r.ID, &r.PrincipalID, &blob, &r.Dim, &r.SampleRate, &r.AudioSeconds, &r.Label, &r.CapturedAt); err != nil {
			return nil, fmt.Errorf("scan voice embedding: %w", err)
		}
		r.Embedding = decodeEmbedding(blob, r.Dim)
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iter voice embeddings: %w", err)
	}
	return out, nil
}

// ListVoiceEmbeddingsForKind returns every enrolled embedding belonging
// to any principal of the given kind. Used by the verification cache
// loader on app startup to fetch all Self enrollments in one query.
func (s *Store) ListVoiceEmbeddingsForKind(ctx context.Context, kind string) ([]VoiceEmbeddingRow, error) {
	rows, err := s.committedReader().QueryContext(ctx, `
		SELECT pve.id, pve.principal_id, pve.embedding, pve.dim, pve.sample_rate,
		       pve.audio_seconds, pve.label, pve.captured_at
		  FROM principal_voice_embeddings pve
		  JOIN principals p ON p.id = pve.principal_id
		 WHERE p.kind = ? AND p.merged_into = ''
		 ORDER BY pve.captured_at DESC
	`, kind)
	if err != nil {
		return nil, fmt.Errorf("query voice embeddings by kind: %w", err)
	}
	defer rows.Close()
	var out []VoiceEmbeddingRow
	for rows.Next() {
		var r VoiceEmbeddingRow
		var blob []byte
		if err := rows.Scan(&r.ID, &r.PrincipalID, &blob, &r.Dim, &r.SampleRate, &r.AudioSeconds, &r.Label, &r.CapturedAt); err != nil {
			return nil, fmt.Errorf("scan voice embedding: %w", err)
		}
		r.Embedding = decodeEmbedding(blob, r.Dim)
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iter voice embeddings by kind: %w", err)
	}
	return out, nil
}

// DeleteVoiceEmbedding removes one sample by ID. Returns ErrNoRows-shaped
// error if the row didn't exist.
func (s *Store) DeleteVoiceEmbedding(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM principal_voice_embeddings WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete voice embedding: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("delete voice embedding: id %d not found", id)
	}
	return nil
}

// CountVoiceEmbeddingsByPrincipal returns the number of enrolled samples
// for a principal — cheaper than ListVoiceEmbeddingsByPrincipal when the
// caller only needs to render an "N samples enrolled" status badge.
func (s *Store) CountVoiceEmbeddingsByPrincipal(ctx context.Context, principalID string) (int, error) {
	var n int
	if err := s.committedReader().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM principal_voice_embeddings WHERE principal_id = ? AND EXISTS (SELECT 1 FROM principals p WHERE p.id=principal_id AND p.merged_into='')`, principalID,
	).Scan(&n); err != nil {
		return 0, fmt.Errorf("count voice embeddings: %w", err)
	}
	return n, nil
}

// ── encoding helpers ──

func encodeEmbedding(v []float32) []byte {
	buf := make([]byte, 4*len(v))
	for i, f := range v {
		binary.LittleEndian.PutUint32(buf[i*4:i*4+4], math.Float32bits(f))
	}
	return buf
}

func decodeEmbedding(buf []byte, dim int) []float32 {
	if len(buf) < 4*dim {
		return nil
	}
	out := make([]float32, dim)
	for i := 0; i < dim; i++ {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(buf[i*4 : i*4+4]))
	}
	return out
}
