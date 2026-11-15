package imessage

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// typedstream builds an attributedBody as Messages writes it, around text.
func typedstream(text string) []byte {
	body := []byte("\x04\x0bstreamtyped\x81\xe8\x03\x84\x01@\x84\x84\x84\x12NSAttributedString\x00\x84\x84\x08NSObject\x00\x85\x92\x84\x84\x84\x08NSString\x01\x94\x84\x01+")
	n := len(text)
	switch {
	case n < 0x80:
		body = append(body, byte(n))
	default:
		body = append(body, 0x81, byte(n), byte(n>>8))
	}
	body = append(body, text...)
	return append(body, 0x86, 0x84)
}

func fakeChatDB(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "chat.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`PRAGMA journal_mode=WAL;
		CREATE TABLE handle (ROWID INTEGER PRIMARY KEY, id TEXT);
		CREATE TABLE chat (ROWID INTEGER PRIMARY KEY, guid TEXT, display_name TEXT);
		CREATE TABLE chat_handle_join (chat_id INTEGER, handle_id INTEGER);
		CREATE TABLE message (ROWID INTEGER PRIMARY KEY, date INTEGER, is_from_me INTEGER, text TEXT, attributedBody BLOB,
			handle_id INTEGER, associated_message_type INTEGER DEFAULT 0, item_type INTEGER DEFAULT 0);
		CREATE TABLE chat_message_join (chat_id INTEGER, message_id INTEGER);
		INSERT INTO handle VALUES (1,'+14155550100'),(2,'sam@example.com');
		INSERT INTO chat VALUES (1,'iMessage;-;+14155550100',''),(2,'iMessage;+;chat99','Trip');
		INSERT INTO chat_handle_join VALUES (1,1),(2,1),(2,2);`); err != nil {
		t.Fatal(err)
	}
	at := appleDate(time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC))
	long := string(make([]byte, 0))
	for i := 0; i < 30; i++ {
		long += "abcdefghij"
	}
	for _, m := range []struct {
		id, chat, handle, fromMe, assoc int
		text                            string
		body                            []byte
	}{
		{1, 1, 1, 0, 0, "Can you send the deck by Friday?", nil},
		{2, 1, 0, 1, 0, "", typedstream("Sure, Friday it is")},
		{3, 1, 1, 0, 2000, "Loved “Sure, Friday it is”", nil},
		{4, 2, 2, 0, 0, "", typedstream(long)},
	} {
		if _, err := db.Exec(`INSERT INTO message (ROWID,date,is_from_me,text,attributedBody,handle_id,associated_message_type) VALUES (?,?,?,?,?,?,?)`,
			m.id, at+int64(m.id)*int64(time.Second), m.fromMe, m.text, m.body, m.handle, m.assoc); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO chat_message_join VALUES (?,?)`, m.chat, m.id); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func TestReadMessagesSkipsTapbacksAndDecodesBodies(t *testing.T) {
	path := fakeChatDB(t)
	if got := Access(path); got != StateAllowed {
		t.Fatalf("access = %v", got)
	}
	msgs, err := Read(context.Background(), path, 0, time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 3 {
		t.Fatalf("got %d messages, want 3 (the tapback is left out): %+v", len(msgs), msgs)
	}
	if msgs[0].Handle != "+14155550100" || msgs[0].FromMe || msgs[0].Group {
		t.Fatalf("first = %+v", msgs[0])
	}
	if msgs[1].Text != "Sure, Friday it is" || !msgs[1].FromMe || msgs[1].Handle != "+14155550100" {
		t.Fatalf("sent reply = %+v", msgs[1])
	}
	if !msgs[2].Group || msgs[2].ChatName != "Trip" || len(msgs[2].Text) != 300 {
		t.Fatalf("group message = %+v (len %d)", msgs[2], len(msgs[2].Text))
	}
	if !msgs[0].At.Equal(time.Date(2026, 11, 14, 9, 0, 1, 0, time.UTC)) {
		t.Fatalf("date = %v", msgs[0].At)
	}
	after, err := Read(context.Background(), path, 2, time.Time{}, 10)
	if err != nil || len(after) != 1 || after[0].RowID != 4 {
		t.Fatalf("after row 2 = %+v, %v", after, err)
	}
	chat, err := ReadChat(context.Background(), path, "iMessage;-;+14155550100", time.Time{}, 1)
	if err != nil || len(chat) != 1 || chat[0].RowID != 2 {
		t.Fatalf("latest of the conversation = %+v, %v", chat, err)
	}
	if len(msgs[2].Members) != 2 {
		t.Fatalf("group members = %v", msgs[2].Members)
	}
	if max, err := MaxRowID(context.Background(), path); err != nil || max != 4 {
		t.Fatalf("max = %d, %v", max, err)
	}
}

func TestAccessMissingAndRefused(t *testing.T) {
	dir := t.TempDir()
	if got := Access(filepath.Join(dir, "chat.db")); got != StateMissing {
		t.Fatalf("missing = %v", got)
	}
	locked := filepath.Join(dir, "locked.db")
	if err := os.WriteFile(locked, []byte("x"), 0o000); err != nil {
		t.Fatal(err)
	}
	if os.Getuid() != 0 {
		if got := Access(locked); got != StateDenied {
			t.Fatalf("refused = %v", got)
		}
	}
	if _, err := Read(context.Background(), locked, 0, time.Time{}, 10); err == nil {
		t.Fatal("reading a refused file must fail")
	}
}

func TestAttributedTextRejectsGarbage(t *testing.T) {
	for _, body := range [][]byte{nil, []byte("NSString"), []byte("NSString\x01\x94\x84\x01+\x50ab")} {
		if got := AttributedText(body); got != "" {
			t.Fatalf("AttributedText(%q) = %q", body, got)
		}
	}
}
