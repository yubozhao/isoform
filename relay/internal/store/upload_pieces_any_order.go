package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Pieces in any order (PROTOCOL.md section 24.1a). A phone whose app is closed
// hands every piece of an upload to the system at once, and the system sends
// them when and in the order it likes, each retried by itself. So the relay
// takes a piece at any place: the upload is cut into pieces of one size (the
// last may be shorter), the relay marks which it holds, and the upload is
// finished when it holds them all. A piece it already holds is answered as
// stored and not written again.

// MaxUploadPieceCount bounds how many pieces one upload may have.
const MaxUploadPieceCount = 1024

const uploadPieceMarksSchema = `
CREATE TABLE IF NOT EXISTS upload_piece_marks (
	id TEXT PRIMARY KEY REFERENCES upload_pieces(id) ON DELETE CASCADE,
	size INTEGER NOT NULL,
	have TEXT NOT NULL
);
`

type rowQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// uploadPieceMarks returns the piece size and the marks of an upload whose
// pieces arrive in any order; ok is false for one that arrives in order.
func (s *Store) uploadPieceMarks(ctx context.Context, db rowQuerier, id string) (size int64, have string, ok bool) {
	if err := db.QueryRowContext(ctx, "SELECT size,have FROM upload_piece_marks WHERE id=?", id).Scan(&size, &have); err != nil {
		return 0, "", false
	}
	return size, have, true
}

func uploadPieceCount(total, size int64) int64 {
	return (total + size - 1) / size
}

// PutUploadPieceAnyOrder stores the piece that starts at offset of an upload of
// total bytes cut into pieces of size bytes. offset must be a multiple of
// size, and the piece must be size bytes long, or end the upload. The first
// piece to arrive, whichever it is, starts the upload and reserves total
// against the quotas. An upload that began in order continues here: what it
// held up to a whole piece counts as held.
func (s *Store) PutUploadPieceAnyOrder(ctx context.Context, device Device, id string, offset, total, size int64, piece []byte, now time.Time) (UploadProgress, error) {
	if s.files == "" {
		return UploadProgress{}, errors.New("upload storage is not configured")
	}
	if total <= 0 || total > MaxUploadBytes || total > MaxUploadBytesPerDevice || len(piece) == 0 || len(piece) > MaxUploadPieceBytes {
		return UploadProgress{}, ErrTooLarge
	}
	uploadPieceMu.Lock()
	defer uploadPieceMu.Unlock()
	var owner string
	var stored int64
	switch err := s.db.QueryRowContext(ctx, "SELECT device_id,size FROM uploads WHERE id=?", id).Scan(&owner, &stored); {
	case err == nil:
		if owner != device.ID || stored != total {
			return UploadProgress{}, ErrConflict
		}
		return UploadProgress{Held: stored, Total: stored, Complete: true}, nil
	case errors.Is(err, sql.ErrNoRows):
	default:
		return UploadProgress{}, err
	}
	return s.putUploadPieceAnyOrderLocked(ctx, device, id, offset, total, size, piece, now)
}

// putUploadPieceAnyOrderLocked does the work; the caller holds uploadPieceMu
// and has ruled out a finished upload.
func (s *Store) putUploadPieceAnyOrderLocked(ctx context.Context, device Device, id string, offset, total, size int64, piece []byte, now time.Time) (UploadProgress, error) {
	if size <= 0 || size > MaxUploadPieceBytes || uploadPieceCount(total, size) > MaxUploadPieceCount {
		return UploadProgress{}, ErrTooLarge
	}
	end := offset + int64(len(piece))
	if offset < 0 || offset%size != 0 || end > total || (int64(len(piece)) != size && end != total) {
		return UploadProgress{}, ErrTooLarge
	}
	held, err := s.startOrReadUploadPieces(ctx, device, id, offset, total, true, now)
	if err != nil {
		return UploadProgress{Held: held, Total: total}, err
	}
	count := uploadPieceCount(total, size)
	knownSize, have, marked := s.uploadPieceMarks(ctx, s.db, id)
	switch {
	case marked && knownSize != size:
		// The phone changed its piece size: the relay says what it has.
		return UploadProgress{Held: held, Total: total, Size: knownSize, Have: have}, ErrUploadOffset
	case !marked:
		// New, or begun in order: whole pieces already held count.
		whole := held / size
		have = strings.Repeat("1", int(whole)) + strings.Repeat("0", int(count-whole))
		held = whole * size
		if _, err := s.db.ExecContext(ctx, "INSERT OR REPLACE INTO upload_piece_marks(id,size,have) VALUES(?,?,?)", id, size, have); err != nil {
			return UploadProgress{}, err
		}
		if _, err := s.db.ExecContext(ctx, "UPDATE upload_pieces SET held=? WHERE id=?", held, id); err != nil {
			return UploadProgress{}, err
		}
	}
	if int64(len(have)) != count {
		return UploadProgress{}, ErrConflict
	}
	index := offset / size
	if have[index] == '1' {
		return UploadProgress{Held: held, Total: total, Size: size, Have: have}, nil
	}
	path := s.uploadPiecePath(device.HubID, id)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return UploadProgress{}, err
	}
	if held == 0 {
		// Nothing is held: bytes left by an earlier start of this id are dropped.
		_ = os.Remove(path)
	}
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return UploadProgress{}, err
	}
	if _, err = file.WriteAt(piece, offset); err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return UploadProgress{}, err
	}
	have = have[:index] + "1" + have[index+1:]
	held += int64(len(piece))
	if strings.Contains(have, "0") {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return UploadProgress{}, err
		}
		defer tx.Rollback()
		if _, err = tx.ExecContext(ctx, "UPDATE upload_piece_marks SET have=? WHERE id=?", have, id); err != nil {
			return UploadProgress{}, err
		}
		if _, err = tx.ExecContext(ctx, "UPDATE upload_pieces SET held=? WHERE id=?", held, id); err != nil {
			return UploadProgress{}, err
		}
		if err = tx.Commit(); err != nil {
			return UploadProgress{}, err
		}
		return UploadProgress{Held: held, Total: total, Size: size, Have: have}, nil
	}
	if err := s.finishUploadPieces(ctx, device, id, total, now); err != nil {
		return UploadProgress{}, err
	}
	return UploadProgress{Held: total, Total: total, Complete: true}, nil
}
