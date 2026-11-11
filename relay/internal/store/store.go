// Package store is the relay's only SQLite owner. It keeps credential hashes,
// ciphertext envelopes, encrypted briefs and routing metadata. It never holds
// keys, plaintext, device names or APNs credentials.
package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

var (
	// ErrNotFound reports a missing row.
	ErrNotFound = errors.New("not found")
	// ErrConflict reports a state transition or duplicate that is not allowed.
	ErrConflict = errors.New("conflicting state")
	// ErrUnauthorized reports an unknown, expired or revoked credential.
	ErrUnauthorized = errors.New("unauthorized")
	// ErrForbidden reports a presented secret that does not match.
	ErrForbidden = errors.New("forbidden")
	// ErrExpired reports a pairing or envelope past its expiry.
	ErrExpired = errors.New("expired")
	// ErrQueueFull reports a request queue at its retention cap.
	ErrQueueFull = errors.New("request queue is full")
)

const (
	// MaxQueued caps queued envelopes per (device, direction). Responses are
	// counted here; events have their own cap so they cannot evict responses.
	MaxQueued = 200
	// MaxEvents caps the hub-to-device event envelopes kept per device. Events
	// (chat turn updates, needs-you counts, receipts) are re-read on the next
	// poll, so only the newest are worth keeping.
	MaxEvents = 50
	// KeptBriefs is how many due briefs each device retains.
	KeptBriefs = 7
	// KeptHeldBriefs is the separate limit for briefs not yet due.
	KeptHeldBriefs = 7
	// RequestTTL is how long a queued request remains deliverable. A phone request
	// waits this long for a sleeping computer (PROTOCOL.md section 23).
	RequestTTL = 24 * time.Hour
)

const schema = `
CREATE TABLE IF NOT EXISTS hubs (
	id TEXT PRIMARY KEY,
	credential_hash TEXT NOT NULL,
	last_seen_at INTEGER NOT NULL DEFAULT 0,
	created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS hub_goodbyes (
	hub_id TEXT PRIMARY KEY REFERENCES hubs(id) ON DELETE CASCADE,
	cause TEXT NOT NULL,
	reason TEXT NOT NULL DEFAULT '',
	at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS pairings (
	id TEXT PRIMARY KEY,
	hub_id TEXT NOT NULL REFERENCES hubs(id) ON DELETE CASCADE,
	claim_hash TEXT NOT NULL,
	state TEXT NOT NULL CHECK (state IN ('open','claimed','approved','declined')),
	expires_at INTEGER NOT NULL,
	created_at INTEGER NOT NULL,
	device_id TEXT NOT NULL DEFAULT '',
	device_pub TEXT NOT NULL DEFAULT '',
	claim_ct TEXT NOT NULL DEFAULT '',
	approval_ct TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS pairings_hub_state ON pairings(hub_id, state);
CREATE INDEX IF NOT EXISTS pairings_expiry ON pairings(expires_at);
CREATE TABLE IF NOT EXISTS devices (
	id TEXT PRIMARY KEY,
	hub_id TEXT NOT NULL REFERENCES hubs(id) ON DELETE CASCADE,
	bearer_hash TEXT NOT NULL UNIQUE,
	scopes TEXT NOT NULL,
	created_at INTEGER NOT NULL,
	expires_at INTEGER NOT NULL,
	last_seen_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS devices_hub ON devices(hub_id);
CREATE TABLE IF NOT EXISTS envelopes (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	device_id TEXT NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
	dir TEXT NOT NULL CHECK (dir IN ('d2h','h2d')),
	seq INTEGER NOT NULL,
	kind TEXT NOT NULL,
	body TEXT NOT NULL,
	expires_at INTEGER NOT NULL DEFAULT 0,
	UNIQUE(device_id, dir, seq)
);
CREATE TABLE IF NOT EXISTS briefs (
	device_id TEXT NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
	version INTEGER NOT NULL,
	as_of TEXT NOT NULL,
	body TEXT NOT NULL,
	stored_at INTEGER NOT NULL,
	deliver_at INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (device_id, version)
);
CREATE TABLE IF NOT EXISTS push_devices (
	device_id TEXT PRIMARY KEY REFERENCES devices(id) ON DELETE CASCADE,
	token TEXT NOT NULL,
	environment TEXT NOT NULL CHECK (environment IN ('development','production')),
	gateway_grant TEXT NOT NULL DEFAULT '',
	registered_at INTEGER NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS push_devices_token ON push_devices(token, environment);
CREATE TABLE IF NOT EXISTS push_outbox (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	device_id TEXT NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
	event_id TEXT NOT NULL,
	notification_id TEXT NOT NULL UNIQUE,
	state TEXT NOT NULL CHECK (state IN ('queued','inflight','accepted','permanent','exhausted','cancelled')),
	created_at INTEGER NOT NULL,
	expires_at INTEGER NOT NULL,
	next_at INTEGER NOT NULL,
	attempts INTEGER NOT NULL DEFAULT 0,
	lease_until INTEGER NOT NULL DEFAULT 0,
	generation INTEGER NOT NULL DEFAULT 0,
	kind TEXT NOT NULL DEFAULT 'brief',
	moment_kind TEXT NOT NULL DEFAULT '',
	notice_id TEXT NOT NULL DEFAULT '',
	ct TEXT NOT NULL DEFAULT '',
	category TEXT NOT NULL DEFAULT '',
	collapse_id TEXT NOT NULL DEFAULT '',
	level TEXT NOT NULL DEFAULT '',
	UNIQUE(device_id, event_id)
);
CREATE INDEX IF NOT EXISTS push_outbox_due ON push_outbox(state, next_at);
CREATE TABLE IF NOT EXISTS notice_receipts (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	notification_id TEXT NOT NULL,
	device_id TEXT NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
	state TEXT NOT NULL CHECK (state IN ('delivered','opened','acted','dismissed')),
	action TEXT NOT NULL DEFAULT '',
	at INTEGER NOT NULL,
	received_at INTEGER NOT NULL,
	forwarded INTEGER NOT NULL DEFAULT 0,
	UNIQUE(notification_id, device_id, state, action)
);
CREATE INDEX IF NOT EXISTS notice_receipts_forward ON notice_receipts(device_id, forwarded);
`

// Store owns the SQLite handle. One connection serializes writers; code inside
// a transaction must use that transaction for every statement. files is the
// directory beside the database where attachment bodies live.
type Store struct {
	db    *sql.DB
	files string
}

// Open creates or opens relay.sqlite with owner-only permissions, WAL mode,
// foreign keys and a busy timeout.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	// Create the file private before SQLite creates its journal files.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	_ = f.Close()
	if err = os.Chmod(path, 0600); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err = db.Exec("PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000; PRAGMA foreign_keys=ON;"); err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err = db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err = db.Exec(attachmentsSchema); err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err = db.Exec(smsSchema); err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err = db.Exec(emailSchema); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err = migrate(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{db: db, files: filepath.Dir(path)}, nil
}

// migrate brings a database created by an earlier release up to the current
// schema. CREATE TABLE IF NOT EXISTS leaves an existing table as it was, so each
// column added after the first release is checked before it is added.
// outboxColumns were added to push_outbox after the first releases. A database
// created before a column existed gets it by ALTER TABLE; fresh databases get
// every column from schema. Existing rows take the default.
var outboxColumns = []struct{ name, definition string }{
	{"kind", "TEXT NOT NULL DEFAULT 'brief'"},
	{"moment_kind", "TEXT NOT NULL DEFAULT ''"},
	{"notice_id", "TEXT NOT NULL DEFAULT ''"},
	{"ct", "TEXT NOT NULL DEFAULT ''"},
	{"category", "TEXT NOT NULL DEFAULT ''"},
	{"collapse_id", "TEXT NOT NULL DEFAULT ''"},
	{"level", "TEXT NOT NULL DEFAULT ''"},
}

var goodbyeColumns = []struct{ name, definition string }{
	{"reason", "TEXT NOT NULL DEFAULT ''"},
}

// briefColumns were added to briefs after the first releases. deliver_at is the
// time a brief becomes visible and its push is due; 0 means at once.
var briefColumns = []struct{ name, definition string }{
	{"deliver_at", "INTEGER NOT NULL DEFAULT 0"},
}

var pushDeviceColumns = []struct{ name, definition string }{
	{"gateway_grant", "TEXT NOT NULL DEFAULT ''"},
}

func migrate(db *sql.DB) error {
	for _, column := range goodbyeColumns {
		present, err := hasColumn(db, "hub_goodbyes", column.name)
		if err != nil {
			return err
		}
		if present {
			continue
		}
		if _, err = db.Exec("ALTER TABLE hub_goodbyes ADD COLUMN " + column.name + " " + column.definition); err != nil {
			return err
		}
	}
	for _, column := range briefColumns {
		present, err := hasColumn(db, "briefs", column.name)
		if err != nil {
			return err
		}
		if present {
			continue
		}
		if _, err = db.Exec("ALTER TABLE briefs ADD COLUMN " + column.name + " " + column.definition); err != nil {
			return err
		}
	}
	for _, column := range pushDeviceColumns {
		present, err := hasColumn(db, "push_devices", column.name)
		if err != nil {
			return err
		}
		if present {
			continue
		}
		if _, err = db.Exec("ALTER TABLE push_devices ADD COLUMN " + column.name + " " + column.definition); err != nil {
			return err
		}
	}
	for _, column := range outboxColumns {
		present, err := hasColumn(db, "push_outbox", column.name)
		if err != nil {
			return err
		}
		if present {
			continue
		}
		if _, err = db.Exec("ALTER TABLE push_outbox ADD COLUMN " + column.name + " " + column.definition); err != nil {
			return err
		}
	}
	return nil
}

// hasColumn reports whether a table has the named column. Its rows are closed
// before it returns, because the pool holds a single connection.
func hasColumn(db *sql.DB, table, column string) (bool, error) {
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return false, err
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		var cid, notNull, pk int
		var name, colType string
		var defaultValue any
		if err = rows.Scan(&cid, &name, &colType, &notNull, &defaultValue, &pk); err != nil {
			return false, err
		}
		if name == column {
			found = true
		}
	}
	return found, rows.Err()
}

// Close releases the database handle.
func (s *Store) Close() error { return s.db.Close() }

// querier is satisfied by *sql.DB and *sql.Tx.
type querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func millis(t time.Time) int64 { return t.UnixMilli() }

func fromMillis(v int64) time.Time { return time.UnixMilli(v).UTC() }
