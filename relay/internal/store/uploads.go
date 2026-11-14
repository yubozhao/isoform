package store

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"
)

// Uploads are sealed files a phone sends to its hub, today a recording made on
// the watch or the phone (PROTOCOL.md section 24). They are attachments in the
// other direction: the phone seals the file with a random per-file key, puts
// the ciphertext here with its device bearer, and sends the key to the hub
// inside an end-to-end-sealed request. The relay stores the bytes as given and
// never opens them. The hub fetches the body with its hub credential and
// deletes it once it holds the recording. Bodies live on disk under
// <state dir>/uploads/<hubId>/<id>; the table holds the metadata.

const (
	// MaxUploadBytes bounds one upload body. A longer recording is sent in
	// several parts.
	MaxUploadBytes = 16 << 20
	// KeptUploads is how many uploads one device keeps waiting for its hub.
	KeptUploads = 48
	// UploadTTL is how long an upload waits for its hub. It outlives the
	// 24-hour request TTL so a request replayed late still finds its body.
	UploadTTL = 48 * time.Hour
)

// Quotas are variables so tests can lower them without writing gigabytes.
var (
	// MaxUploadBytesPerDevice bounds one device's waiting bytes. A new upload
	// over it is refused; the phone keeps the recording and tries later. A
	// phone's own recordings are never evicted, because the hub has not
	// received them yet.
	MaxUploadBytesPerDevice int64 = 192 << 20
	// MaxUploadStoreBytes bounds every device together.
	MaxUploadStoreBytes int64 = 2 << 30
)

// ErrQuotaFull reports a device whose waiting uploads already fill its quota.
var ErrQuotaFull = errors.New("upload quota is full")

const uploadsSchema = `
CREATE TABLE IF NOT EXISTS uploads (
	id TEXT PRIMARY KEY,
	hub_id TEXT NOT NULL REFERENCES hubs(id) ON DELETE CASCADE,
	device_id TEXT NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
	size INTEGER NOT NULL,
	stored_at INTEGER NOT NULL,
	expires_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS uploads_device ON uploads(device_id, stored_at);
CREATE INDEX IF NOT EXISTS uploads_expiry ON uploads(expires_at);
`

// uploadPath is where a hub's upload body lives.
func (s *Store) uploadPath(hubID, id string) string {
	return filepath.Join(s.files, "uploads", hubID, id)
}

// PutUpload streams a device's upload body to disk and records it. body may be
// longer than MaxUploadBytes; the extra byte is detected and the upload refused
// with ErrTooLarge. An id that belongs to another device is ErrConflict; a
// repeat under the same id from the same device replaces the body.
func (s *Store) PutUpload(ctx context.Context, device Device, id string, body io.Reader, now time.Time) error {
	if s.files == "" {
		return errors.New("upload storage is not configured")
	}
	dir := filepath.Dir(s.uploadPath(device.HubID, id))
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".upload-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	size, err := io.Copy(tmp, io.LimitReader(body, MaxUploadBytes+1))
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		cleanup()
		return err
	}
	if size == 0 || size > MaxUploadBytes {
		cleanup()
		return ErrTooLarge
	}
	if err := os.Chmod(tmpName, 0600); err != nil {
		cleanup()
		return err
	}
	if err := s.recordUpload(ctx, device, id, size, now); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpName, s.uploadPath(device.HubID, id)); err != nil {
		cleanup()
		_, _ = s.db.ExecContext(ctx, "DELETE FROM uploads WHERE id=?", id)
		return err
	}
	return nil
}

// recordUpload writes the row inside one transaction, enforcing ownership,
// the device's count and byte quota, and the store-wide cap.
func (s *Store) recordUpload(ctx context.Context, device Device, id string, size int64, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var owner string
	switch err = tx.QueryRowContext(ctx, "SELECT device_id FROM uploads WHERE id=?", id).Scan(&owner); {
	case err == nil:
		if owner != device.ID {
			return ErrConflict
		}
		if _, err = tx.ExecContext(ctx, "DELETE FROM uploads WHERE id=?", id); err != nil {
			return err
		}
	case errors.Is(err, sql.ErrNoRows):
	default:
		return err
	}
	var count int
	var used int64
	if err = tx.QueryRowContext(ctx, "SELECT count(*), COALESCE(SUM(size),0) FROM uploads WHERE device_id=? AND expires_at>?", device.ID, millis(now)).Scan(&count, &used); err != nil {
		return err
	}
	if size > MaxUploadBytesPerDevice {
		return ErrTooLarge
	}
	if count >= KeptUploads || used+size > MaxUploadBytesPerDevice {
		return ErrQuotaFull
	}
	var total int64
	if err = tx.QueryRowContext(ctx, "SELECT COALESCE(SUM(size),0) FROM uploads WHERE expires_at>?", millis(now)).Scan(&total); err != nil {
		return err
	}
	if total+size > MaxUploadStoreBytes {
		return ErrStorageFull
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO uploads(id,hub_id,device_id,size,stored_at,expires_at) VALUES(?,?,?,?,?,?)",
		id, device.HubID, device.ID, size, millis(now), millis(now.Add(UploadTTL))); err != nil {
		return err
	}
	return tx.Commit()
}

// GetUpload returns the file path and size of an upload sent to hubID, or
// ErrNotFound when it is unknown, was sent to another hub, or has expired.
func (s *Store) GetUpload(ctx context.Context, hubID, id string, now time.Time) (string, int64, error) {
	var size, expires int64
	err := s.db.QueryRowContext(ctx, "SELECT size,expires_at FROM uploads WHERE id=? AND hub_id=?", id, hubID).Scan(&size, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, ErrNotFound
	}
	if err != nil {
		return "", 0, err
	}
	if expires <= millis(now) {
		return "", 0, ErrNotFound
	}
	path := s.uploadPath(hubID, id)
	if _, err := os.Stat(path); err != nil {
		return "", 0, ErrNotFound
	}
	return path, size, nil
}

// DeleteUpload removes an upload the hub has taken. Deleting an unknown id is
// not an error, so a repeat after a lost answer succeeds.
func (s *Store) DeleteUpload(ctx context.Context, hubID, id string) error {
	result, err := s.db.ExecContext(ctx, "DELETE FROM uploads WHERE id=? AND hub_id=?", id, hubID)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n > 0 {
		_ = os.Remove(s.uploadPath(hubID, id))
	}
	return nil
}

// sweepUploads removes expired uploads and uploads whose device is gone,
// files first, and returns how many rows went.
func (s *Store) sweepUploads(ctx context.Context, now time.Time) (int64, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT hub_id,id FROM uploads WHERE expires_at<=?", millis(now))
	if err != nil {
		return 0, err
	}
	var refs []attachmentRef
	for rows.Next() {
		var ref attachmentRef
		if err = rows.Scan(&ref.hubID, &ref.id); err != nil {
			rows.Close()
			return 0, err
		}
		refs = append(refs, ref)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return 0, err
	}
	for _, ref := range refs {
		_ = os.Remove(s.uploadPath(ref.hubID, ref.id))
	}
	result, err := s.db.ExecContext(ctx, "DELETE FROM uploads WHERE expires_at<=?", millis(now))
	if err != nil {
		return 0, err
	}
	s.removeOrphanUploads(ctx, now)
	return result.RowsAffected()
}

// removeOrphanUploads deletes bodies whose row went with an unpaired device or
// a removed hub (ON DELETE CASCADE leaves the file). Files younger than an hour
// are left alone: they may be an upload still being recorded.
func (s *Store) removeOrphanUploads(ctx context.Context, now time.Time) {
	root := filepath.Join(s.files, "uploads")
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
			info, err := file.Info()
			if err != nil || now.Sub(info.ModTime()) < time.Hour {
				continue
			}
			var known int
			if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM uploads WHERE id=? AND hub_id=?", file.Name(), hub.Name()).Scan(&known); err != nil || known > 0 {
				continue
			}
			_ = os.Remove(filepath.Join(root, hub.Name(), file.Name()))
		}
	}
}
