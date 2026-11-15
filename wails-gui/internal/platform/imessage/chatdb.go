// Package imessage reads the Messages app's database on macOS
// (~/Library/Messages/chat.db): the messages of the last days, who sent them
// and in which conversation. It needs Full Disk Access; without it opening
// the file fails and Access says so, without asking macOS for anything
// (macOS has no prompt for Full Disk Access).
//
// The database is opened in place, read-only. Message text is returned to
// the caller in memory and never written anywhere by this package.
package imessage

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	_ "modernc.org/sqlite" // the "sqlite" driver
)

// BundleID is the Messages app, for the user's protected-apps list.
const BundleID = "com.apple.MobileSMS"

// AppName is the Messages app's name, for the same list.
const AppName = "Messages"

// DefaultPath is chat.db under home.
func DefaultPath(home string) string {
	return filepath.Join(home, "Library", "Messages", "chat.db")
}

// State is whether Isoform can read the database.
type State int

const (
	// StateMissing is no database: Messages was never used on this computer.
	StateMissing State = iota
	// StateAllowed is the file opens.
	StateAllowed
	// StateDenied is macOS refusing it: Full Disk Access is not granted.
	StateDenied
)

// Access reports whether the database at path can be opened. It never
// prompts: macOS refuses the open without a dialog when Full Disk Access is
// missing.
func Access(path string) State {
	f, err := os.Open(path)
	if err == nil {
		f.Close()
		return StateAllowed
	}
	if errors.Is(err, os.ErrNotExist) {
		return StateMissing
	}
	// EPERM is macOS refusing it; any other failure reads as refused too.
	return StateDenied
}

// Message is one message as read. Text lives only in memory.
type Message struct {
	RowID int64
	// Chat is the conversation's stable id (chat.guid); ChatName the group's
	// name when it has one.
	Chat     string
	ChatName string
	Group    bool
	// Members are the conversation's other people: phone numbers or
	// addresses.
	Members []string
	// Handle is the other person's phone number or address: the sender of a
	// received message, and for a sent one the conversation's other member
	// when there is exactly one.
	Handle string
	FromMe bool
	At     time.Time
	Text   string
}

// appleEpoch is 2001-01-01 UTC, the zero of Messages' dates.
var appleEpoch = time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)

// appleTime turns a Messages date into a time: nanoseconds since 2001 on
// current macOS, seconds on old databases.
func appleTime(v int64) time.Time {
	if v > 1e12 || v < -1e12 {
		return appleEpoch.Add(time.Duration(v))
	}
	return appleEpoch.Add(time.Duration(v) * time.Second)
}

func appleDate(t time.Time) int64 {
	return t.Sub(appleEpoch).Nanoseconds()
}

// open opens the database read-only in place. Messages keeps it in WAL mode
// with its -shm present, so a read-only connection sees the latest writes;
// when that fails an immutable open still reads the main file.
func open(path string) (*sql.DB, error) {
	u := url.URL{Scheme: "file", Path: path}
	for _, query := range []string{"mode=ro&_pragma=query_only(1)&_pragma=busy_timeout(2000)", "mode=ro&immutable=1"} {
		db, err := sql.Open("sqlite", u.String()+"?"+query)
		if err != nil {
			continue
		}
		db.SetMaxOpenConns(1)
		if err := db.Ping(); err != nil {
			db.Close()
			continue
		}
		var n int
		if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name='message'`).Scan(&n); err != nil || n == 0 {
			db.Close()
			continue
		}
		return db, nil
	}
	return nil, errors.New("the Messages database could not be opened")
}

// messageSelect is the columns scanMessages reads.
const messageSelect = `SELECT m.ROWID, m.date, m.is_from_me, COALESCE(m.text,''), m.attributedBody,
		COALESCE(h.id,''), COALESCE(c.guid,''), COALESCE(c.display_name,''),
		COALESCE((SELECT group_concat(h2.id, char(10)) FROM chat_handle_join j2 JOIN handle h2 ON h2.ROWID=j2.handle_id WHERE j2.chat_id=c.ROWID),'')
		FROM message m
		JOIN chat_message_join cm ON cm.message_id=m.ROWID
		JOIN chat c ON c.ROWID=cm.chat_id
		LEFT JOIN handle h ON h.ROWID=m.handle_id`

// Read returns the messages after afterRowID and at or after since, oldest
// first, at most limit. Tapbacks, edits' bookkeeping rows, system notices
// and messages with no text are left out.
func Read(ctx context.Context, path string, afterRowID int64, since time.Time, limit int) ([]Message, error) {
	if limit <= 0 {
		limit = 500
	}
	db, err := open(path)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, messageSelect+`
		WHERE m.ROWID>? AND m.date>=? AND COALESCE(m.associated_message_type,0)=0 AND COALESCE(m.item_type,0)=0
		ORDER BY m.ROWID LIMIT ?`, afterRowID, appleDate(since), limit)
	if err != nil {
		return nil, fmt.Errorf("read messages: %w", err)
	}
	return scanMessages(rows)
}

// ReadChat returns the latest messages of one conversation at or after
// since, oldest first, at most limit: the context around new ones.
func ReadChat(ctx context.Context, path, chat string, since time.Time, limit int) ([]Message, error) {
	if limit <= 0 {
		limit = 30
	}
	db, err := open(path)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, `SELECT * FROM (`+messageSelect+`
		WHERE c.guid=? AND m.date>=? AND COALESCE(m.associated_message_type,0)=0 AND COALESCE(m.item_type,0)=0
		ORDER BY m.ROWID DESC LIMIT ?) ORDER BY 1`, chat, appleDate(since), limit)
	if err != nil {
		return nil, fmt.Errorf("read conversation: %w", err)
	}
	return scanMessages(rows)
}

func scanMessages(rows *sql.Rows) ([]Message, error) {
	defer rows.Close()
	var out []Message
	for rows.Next() {
		var m Message
		var date int64
		var fromMe int
		var body []byte
		var members string
		if err := rows.Scan(&m.RowID, &date, &fromMe, &m.Text, &body, &m.Handle, &m.Chat, &m.ChatName, &members); err != nil {
			return nil, err
		}
		m.At = appleTime(date)
		m.FromMe = fromMe != 0
		for _, member := range strings.Split(members, "\n") {
			if member = strings.TrimSpace(member); member != "" {
				m.Members = append(m.Members, member)
			}
		}
		m.Group = len(m.Members) > 1
		if m.Handle == "" && !m.Group && len(m.Members) == 1 {
			m.Handle = m.Members[0]
		}
		if strings.TrimSpace(m.Text) == "" {
			m.Text = AttributedText(body)
		}
		m.Text = strings.TrimSpace(strings.ReplaceAll(m.Text, "\uFFFC", ""))
		if m.Text == "" || m.Chat == "" {
			continue
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// MaxRowID is the newest message's row id, so a first read can start at
// the present without reading the past.
func MaxRowID(ctx context.Context, path string) (int64, error) {
	db, err := open(path)
	if err != nil {
		return 0, err
	}
	defer db.Close()
	var id sql.NullInt64
	if err := db.QueryRowContext(ctx, `SELECT MAX(ROWID) FROM message`).Scan(&id); err != nil {
		return 0, err
	}
	return id.Int64, nil
}

// AttributedText takes the plain text out of a message's attributedBody,
// the typedstream archive current macOS writes instead of the text column.
// The string follows the "NSString" class name: a marker, a length (one
// byte, or 0x81 and two bytes little-endian, or 0x82 and four), then UTF-8.
func AttributedText(body []byte) string {
	i := bytes.Index(body, []byte("NSString"))
	if i < 0 {
		return ""
	}
	rest := body[i+len("NSString"):]
	j := bytes.IndexByte(rest, '+')
	if j < 0 || j > 8 {
		return ""
	}
	rest = rest[j+1:]
	if len(rest) == 0 {
		return ""
	}
	var n int
	switch rest[0] {
	case 0x81:
		if len(rest) < 3 {
			return ""
		}
		n, rest = int(binary.LittleEndian.Uint16(rest[1:3])), rest[3:]
	case 0x82:
		if len(rest) < 5 {
			return ""
		}
		n, rest = int(binary.LittleEndian.Uint32(rest[1:5])), rest[5:]
	default:
		n, rest = int(rest[0]), rest[1:]
	}
	if n <= 0 || n > len(rest) {
		return ""
	}
	text := rest[:n]
	if !utf8.Valid(text) {
		return ""
	}
	return string(text)
}
