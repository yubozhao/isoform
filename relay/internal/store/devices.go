package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"isoform-relay/internal/wire"
)

// Device is an approved phone. Its bearer is never stored, only its digest.
type Device struct {
	ID         string
	HubID      string
	BearerHash string
	Scopes     []string
	ExpiresAt  time.Time
}

// Has reports whether the device holds scope.
func (d Device) Has(scope string) bool { return wire.HasScope(d.Scopes, scope) }

// DeviceSeen is one entry of a hub's device list.
type DeviceSeen struct {
	ID         string
	LastSeenAt time.Time
}

// DeviceByBearer resolves a presented bearer digest. Unknown, expired and
// revoked bearers all report ErrUnauthorized.
func (s *Store) DeviceByBearer(ctx context.Context, bearerHash string, now time.Time) (Device, error) {
	var d Device
	var scopes string
	var expires int64
	err := s.db.QueryRowContext(ctx, "SELECT id,hub_id,bearer_hash,scopes,expires_at FROM devices WHERE bearer_hash=?", bearerHash).Scan(&d.ID, &d.HubID, &d.BearerHash, &scopes, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return Device{}, ErrUnauthorized
	}
	if err != nil {
		return Device{}, err
	}
	if !sameDigest(d.BearerHash, bearerHash) || expires <= millis(now) {
		return Device{}, ErrUnauthorized
	}
	if err = json.Unmarshal([]byte(scopes), &d.Scopes); err != nil {
		return Device{}, err
	}
	d.ExpiresAt = fromMillis(expires)
	return d, nil
}

// TouchDevice records device activity, at most once a minute per device.
func (s *Store) TouchDevice(ctx context.Context, deviceID string, now time.Time) error {
	_, err := s.db.ExecContext(ctx, "UPDATE devices SET last_seen_at=? WHERE id=? AND last_seen_at<?", millis(now), deviceID, millis(now.Add(-time.Minute)))
	return err
}

// DevicesOf lists the devices of one hub, oldest first.
func (s *Store) DevicesOf(ctx context.Context, hubID string) ([]DeviceSeen, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id,last_seen_at FROM devices WHERE hub_id=? ORDER BY created_at,id", hubID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	devices := []DeviceSeen{}
	for rows.Next() {
		var d DeviceSeen
		var seen int64
		if err = rows.Scan(&d.ID, &seen); err != nil {
			return nil, err
		}
		if seen != 0 {
			d.LastSeenAt = fromMillis(seen)
		}
		devices = append(devices, d)
	}
	return devices, rows.Err()
}

// RemoveDevice deletes the calling device and everything stored for it. The
// cascade removes its envelopes, briefs and push registration.
func (s *Store) RemoveDevice(ctx context.Context, deviceID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	removed, err := removeDevice(ctx, tx, "", deviceID)
	if err != nil {
		return err
	}
	if !removed {
		return ErrNotFound
	}
	return tx.Commit()
}

// RevokeDevice deletes a device on the hub's instruction. It reports whether a
// device of that hub was removed. An unknown device is not an error.
func (s *Store) RevokeDevice(ctx context.Context, hubID, deviceID string) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	removed, err := removeDevice(ctx, tx, hubID, deviceID)
	if err != nil {
		return false, err
	}
	return removed, tx.Commit()
}

// removeDevice deletes a device, and with a non-empty hubID only if it belongs
// to that hub. Approved pairings that named the device are removed as well.
func removeDevice(ctx context.Context, q querier, hubID, deviceID string) (bool, error) {
	var result sql.Result
	var err error
	if hubID == "" {
		result, err = q.ExecContext(ctx, "DELETE FROM devices WHERE id=?", deviceID)
	} else {
		result, err = q.ExecContext(ctx, "DELETE FROM devices WHERE id=? AND hub_id=?", deviceID, hubID)
	}
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	if err != nil || n == 0 {
		return false, err
	}
	_, err = q.ExecContext(ctx, "DELETE FROM pairings WHERE device_id=? AND state=?", deviceID, stateApproved)
	return true, err
}

// insertDevice writes a device created by approval. Duplicate IDs and bearers
// are conflicts.
func insertDevice(ctx context.Context, q querier, hubID string, d Device, now time.Time) error {
	scopes, err := scopesJSON(d.Scopes)
	if err != nil {
		return err
	}
	var duplicates int
	if err = q.QueryRowContext(ctx, "SELECT count(*) FROM devices WHERE id=? OR bearer_hash=?", d.ID, d.BearerHash).Scan(&duplicates); err != nil {
		return err
	}
	if duplicates != 0 {
		return ErrConflict
	}
	_, err = q.ExecContext(ctx, "INSERT INTO devices(id,hub_id,bearer_hash,scopes,created_at,expires_at,last_seen_at) VALUES(?,?,?,?,?,?,0)", d.ID, hubID, d.BearerHash, scopes, millis(now), millis(d.ExpiresAt))
	return err
}

// deviceInHub confirms that a device belongs to the hub.
func deviceInHub(ctx context.Context, q querier, hubID, deviceID string) error {
	var found int
	err := q.QueryRowContext(ctx, "SELECT count(*) FROM devices WHERE id=? AND hub_id=?", deviceID, hubID).Scan(&found)
	if err != nil {
		return err
	}
	if found == 0 {
		return ErrNotFound
	}
	return nil
}

// GrantDevice records a device the hub already paired elsewhere, so the same
// phone can use this relay too (PROTOCOL.md section 6, grant). A device of the
// same hub is updated in place: its bearer, scopes and expiry are replaced. A
// device ID of another hub, or a bearer digest held by another device, is a
// conflict.
func (s *Store) GrantDevice(ctx context.Context, hubID string, d Device, now time.Time) error {
	scopes, err := scopesJSON(d.Scopes)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var owner string
	switch err = tx.QueryRowContext(ctx, "SELECT hub_id FROM devices WHERE id=?", d.ID).Scan(&owner); {
	case errors.Is(err, sql.ErrNoRows):
		owner = ""
	case err != nil:
		return err
	}
	if owner != "" && owner != hubID {
		return ErrConflict
	}
	var others int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM devices WHERE bearer_hash=? AND id<>?", d.BearerHash, d.ID).Scan(&others); err != nil {
		return err
	}
	if others != 0 {
		return ErrConflict
	}
	if owner == "" {
		_, err = tx.ExecContext(ctx, "INSERT INTO devices(id,hub_id,bearer_hash,scopes,created_at,expires_at,last_seen_at) VALUES(?,?,?,?,?,?,0)", d.ID, hubID, d.BearerHash, scopes, millis(now), millis(d.ExpiresAt))
	} else {
		_, err = tx.ExecContext(ctx, "UPDATE devices SET bearer_hash=?, scopes=?, expires_at=? WHERE id=? AND hub_id=?", d.BearerHash, scopes, millis(d.ExpiresAt), d.ID, hubID)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}
