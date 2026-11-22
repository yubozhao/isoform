package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Upload pieces (PROTOCOL.md section 24.1a): a phone on a connection that cuts
// long requests sends an upload as consecutive pieces. The relay appends each
// piece to a body it keeps apart from finished uploads, under
// <state dir>/upload-pieces/<hubId>/<id>, and the hub sees nothing until the
// last piece is in: then the body moves to where PutUpload would have put it
// and becomes an ordinary upload. A cut costs the piece in flight, not the
// upload. The first piece reserves the whole size against the device's quota,
// so a started upload always has room to finish.

// MaxUploadPieceBytes bounds one piece.
const MaxUploadPieceBytes = 1 << 20

// ErrUploadOffset reports a piece that does not start where the relay's copy
// ends. The caller reads the held size and continues from there.
var ErrUploadOffset = errors.New("the piece does not start where the upload ends")

const uploadPiecesSchema = `
CREATE TABLE IF NOT EXISTS upload_pieces (
	id TEXT PRIMARY KEY,
	hub_id TEXT NOT NULL REFERENCES hubs(id) ON DELETE CASCADE,
	device_id TEXT NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
	total INTEGER NOT NULL,
	held INTEGER NOT NULL,
	started_at INTEGER NOT NULL,
	expires_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS upload_pieces_device ON upload_pieces(device_id);
CREATE INDEX IF NOT EXISTS upload_pieces_expiry ON upload_pieces(expires_at);
`

// uploadPieceMu serializes piece writes. A piece is at most 1 MiB and already
// in memory when the lock is taken, so the wait is a disk write.
var uploadPieceMu sync.Mutex

// UploadProgress is how much of an upload the relay holds.
type UploadProgress struct {
	Held     int64
	Total    int64
	Complete bool
	// Size and Have are set for an upload whose pieces may arrive in any
	// order (upload_pieces_any_order.go): the piece size, and one character
	// per piece, '1' for a piece the relay holds.
	Size int64
	Have string
}

func (s *Store) uploadPiecePath(hubID, id string) string {
	return filepath.Join(s.files, "upload-pieces", hubID, id)
}

// UploadProgress reports what the relay holds of a device's upload: a finished
// upload, one still arriving in pieces, or ErrNotFound. An id that belongs to
// another device is ErrNotFound too, so ids cannot be probed.
func (s *Store) UploadProgress(ctx context.Context, device Device, id string, now time.Time) (UploadProgress, error) {
	var owner string
	var size, total, held, expires int64
	switch err := s.db.QueryRowContext(ctx, "SELECT device_id,size,expires_at FROM uploads WHERE id=?", id).Scan(&owner, &size, &expires); {
	case err == nil:
		if owner != device.ID || expires <= millis(now) {
			return UploadProgress{}, ErrNotFound
		}
		return UploadProgress{Held: size, Total: size, Complete: true}, nil
	case errors.Is(err, sql.ErrNoRows):
	default:
		return UploadProgress{}, err
	}
	switch err := s.db.QueryRowContext(ctx, "SELECT device_id,total,held,expires_at FROM upload_pieces WHERE id=?", id).Scan(&owner, &total, &held, &expires); {
	case err == nil:
		if owner != device.ID || expires <= millis(now) {
			return UploadProgress{}, ErrNotFound
		}
		progress := UploadProgress{Held: held, Total: total}
		if size, have, ok := s.uploadPieceMarks(ctx, s.db, id); ok {
			progress.Size, progress.Have = size, have
		}
		return progress, nil
	case errors.Is(err, sql.ErrNoRows):
		return UploadProgress{}, ErrNotFound
	default:
		return UploadProgress{}, err
	}
}

// PutUploadPiece appends piece at offset to the device's upload of total
// bytes. The first piece (offset 0) starts the upload and reserves total
// against the quotas PutUpload enforces. A piece that does not start at the
// held size returns ErrUploadOffset with the held size, also for a repeat of
// a piece whose answer was lost. The piece that reaches total finishes the
// upload, which the hub can then fetch. A finished upload of the same size
// answers as complete, so a repeat of the last piece succeeds.
func (s *Store) PutUploadPiece(ctx context.Context, device Device, id string, offset, total int64, piece []byte, now time.Time) (UploadProgress, error) {
	if s.files == "" {
		return UploadProgress{}, errors.New("upload storage is not configured")
	}
	if total <= 0 || total > MaxUploadBytes || total > MaxUploadBytesPerDevice || len(piece) == 0 || len(piece) > MaxUploadPieceBytes {
		return UploadProgress{}, ErrTooLarge
	}
	if offset < 0 || offset+int64(len(piece)) > total {
		return UploadProgress{}, ErrTooLarge
	}
	uploadPieceMu.Lock()
	defer uploadPieceMu.Unlock()

	var owner string
	var size int64
	switch err := s.db.QueryRowContext(ctx, "SELECT device_id,size FROM uploads WHERE id=?", id).Scan(&owner, &size); {
	case err == nil:
		if owner != device.ID || size != total {
			return UploadProgress{}, ErrConflict
		}
		return UploadProgress{Held: size, Total: size, Complete: true}, nil
	case errors.Is(err, sql.ErrNoRows):
	default:
		return UploadProgress{}, err
	}

	// An upload whose pieces arrive in any order takes this piece by its rules.
	if size, _, ok := s.uploadPieceMarks(ctx, s.db, id); ok {
		return s.putUploadPieceAnyOrderLocked(ctx, device, id, offset, total, size, piece, now)
	}
	held, err := s.startOrReadUploadPieces(ctx, device, id, offset, total, false, now)
	if err != nil {
		return UploadProgress{Held: held, Total: total}, err
	}
	path := s.uploadPiecePath(device.HubID, id)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return UploadProgress{}, err
	}
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return UploadProgress{}, err
	}
	// The row says how much is held; anything past it is a write whose row
	// update was lost, and is written again.
	if err = file.Truncate(held); err == nil {
		_, err = file.WriteAt(piece, held)
	}
	if err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return UploadProgress{}, err
	}
	held += int64(len(piece))
	if held < total {
		if _, err := s.db.ExecContext(ctx, "UPDATE upload_pieces SET held=? WHERE id=?", held, id); err != nil {
			return UploadProgress{}, err
		}
		return UploadProgress{Held: held, Total: total}, nil
	}
	if err := s.finishUploadPieces(ctx, device, id, total, now); err != nil {
		return UploadProgress{}, err
	}
	return UploadProgress{Held: total, Total: total, Complete: true}, nil
}

// startOrReadUploadPieces returns how much of the upload is held, creating the
// row for a first piece after the quota checks.
// With anyOffset the piece may be any one of the upload: no offset is refused.
func (s *Store) startOrReadUploadPieces(ctx context.Context, device Device, id string, offset, total int64, anyOffset bool, now time.Time) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var owner string
	var knownTotal, held, expires int64
	switch err = tx.QueryRowContext(ctx, "SELECT device_id,total,held,expires_at FROM upload_pieces WHERE id=?", id).Scan(&owner, &knownTotal, &held, &expires); {
	case err == nil:
		if owner != device.ID {
			return 0, ErrConflict
		}
		if expires > millis(now) && knownTotal == total {
			if offset != held && !anyOffset {
				return held, ErrUploadOffset
			}
			return held, nil
		}
		// Expired, or the phone starts the id again with another size.
		if offset != 0 && !anyOffset {
			return 0, ErrUploadOffset
		}
		if _, err = tx.ExecContext(ctx, "DELETE FROM upload_piece_marks WHERE id=?", id); err != nil {
			return 0, err
		}
		if _, err = tx.ExecContext(ctx, "DELETE FROM upload_pieces WHERE id=?", id); err != nil {
			return 0, err
		}
	case errors.Is(err, sql.ErrNoRows):
		if offset != 0 && !anyOffset {
			return 0, ErrUploadOffset
		}
	default:
		return 0, err
	}
	var count, arriving int
	var used, reserved, all, allReserved int64
	if err = tx.QueryRowContext(ctx, "SELECT count(*), COALESCE(SUM(size),0) FROM uploads WHERE device_id=? AND expires_at>?", device.ID, millis(now)).Scan(&count, &used); err != nil {
		return 0, err
	}
	if err = tx.QueryRowContext(ctx, "SELECT count(*), COALESCE(SUM(total),0) FROM upload_pieces WHERE device_id=? AND expires_at>?", device.ID, millis(now)).Scan(&arriving, &reserved); err != nil {
		return 0, err
	}
	if err := waitingUploadQuota(count+arriving, used+reserved, total); err != nil {
		return 0, err
	}
	if err = tx.QueryRowContext(ctx, "SELECT COALESCE(SUM(size),0) FROM uploads WHERE expires_at>?", millis(now)).Scan(&all); err != nil {
		return 0, err
	}
	if err = tx.QueryRowContext(ctx, "SELECT COALESCE(SUM(total),0) FROM upload_pieces WHERE expires_at>?", millis(now)).Scan(&allReserved); err != nil {
		return 0, err
	}
	if all+allReserved+total > MaxUploadStoreBytes {
		return 0, ErrStorageFull
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO upload_pieces(id,hub_id,device_id,total,held,started_at,expires_at) VALUES(?,?,?,?,0,?,?)",
		id, device.HubID, device.ID, total, millis(now), millis(now.Add(UploadTTL))); err != nil {
		return 0, err
	}
	return 0, tx.Commit()
}

// finishUploadPieces turns a fully arrived body into an ordinary upload: the
// row moves tables in one transaction, then the file moves beside the others.
func (s *Store) finishUploadPieces(ctx context.Context, device Device, id string, total int64, now time.Time) error {
	if err := os.MkdirAll(filepath.Dir(s.uploadPath(device.HubID, id)), 0700); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "DELETE FROM upload_piece_marks WHERE id=?", id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM upload_pieces WHERE id=?", id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO uploads(id,hub_id,device_id,size,stored_at,expires_at) VALUES(?,?,?,?,?,?)",
		id, device.HubID, device.ID, total, millis(now), millis(now.Add(UploadTTL))); err != nil {
		return err
	}
	// The file moves before the commit: a hub that sees the row finds the body.
	// If the commit fails the piece row is back and the body is moved back.
	if err = os.Rename(s.uploadPiecePath(device.HubID, id), s.uploadPath(device.HubID, id)); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		_ = os.Rename(s.uploadPath(device.HubID, id), s.uploadPiecePath(device.HubID, id))
		return err
	}
	return nil
}

// sweepUploadPieces removes unfinished uploads that expired, and bodies whose
// row went with an unpaired device or a removed hub.
func (s *Store) sweepUploadPieces(ctx context.Context, now time.Time) {
	uploadPieceMu.Lock()
	defer uploadPieceMu.Unlock()
	_, _ = s.db.ExecContext(ctx, "DELETE FROM upload_piece_marks WHERE id IN (SELECT id FROM upload_pieces WHERE expires_at<=?)", millis(now))
	_, _ = s.db.ExecContext(ctx, "DELETE FROM upload_pieces WHERE expires_at<=?", millis(now))
	_, _ = s.db.ExecContext(ctx, "DELETE FROM upload_piece_marks WHERE id NOT IN (SELECT id FROM upload_pieces)")
	root := filepath.Join(s.files, "upload-pieces")
	hubs, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, hub := range hubs {
		if !hub.IsDir() {
			continue
		}
		files, err := os.ReadDir(filepath.Join(root, hub.Name()))
		if err != nil {
			continue
		}
		for _, file := range files {
			var known int
			if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM upload_pieces WHERE id=? AND hub_id=?", file.Name(), hub.Name()).Scan(&known); err != nil || known > 0 {
				continue
			}
			_ = os.Remove(filepath.Join(root, hub.Name(), file.Name()))
		}
	}
}

// DeviceUpload is one upload the relay holds for a device.
type DeviceUpload struct {
	ID       string
	Held     int64
	Total    int64
	Complete bool
}

// DeviceUploads lists what the relay holds of a device's uploads and has not
// let expire: the whole ones its hub has not fetched, then the ones still
// arriving, each oldest first. The quotas bound how many there can be.
func (s *Store) DeviceUploads(ctx context.Context, device Device, now time.Time) ([]DeviceUpload, error) {
	var out []DeviceUpload
	whole, err := s.db.QueryContext(ctx, "SELECT id,size FROM uploads WHERE device_id=? AND expires_at>? ORDER BY stored_at,id", device.ID, millis(now))
	if err != nil {
		return nil, err
	}
	defer whole.Close()
	for whole.Next() {
		upload := DeviceUpload{Complete: true}
		if err := whole.Scan(&upload.ID, &upload.Total); err != nil {
			return nil, err
		}
		upload.Held = upload.Total
		out = append(out, upload)
	}
	if err := whole.Err(); err != nil {
		return nil, err
	}
	arriving, err := s.db.QueryContext(ctx, "SELECT id,held,total FROM upload_pieces WHERE device_id=? AND expires_at>? ORDER BY started_at,id", device.ID, millis(now))
	if err != nil {
		return nil, err
	}
	defer arriving.Close()
	for arriving.Next() {
		var upload DeviceUpload
		if err := arriving.Scan(&upload.ID, &upload.Held, &upload.Total); err != nil {
			return nil, err
		}
		out = append(out, upload)
	}
	return out, arriving.Err()
}
