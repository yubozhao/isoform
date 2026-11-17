package storage

import (
	"context"
	"testing"
)

func TestMessageAsksSizeCountsNotesAndTheirWords(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if count, bytes, err := s.MessageAsksSize(ctx); err != nil || count != 0 || bytes != 0 {
		t.Fatalf("empty = %d %d %v", count, bytes, err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO message_asks (id, chat_key, person_name, kind, summary, at, created_at) VALUES ('a', 'c', 'Lena', 'asked', 'the plan', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	count, bytes, err := s.MessageAsksSize(ctx)
	if err != nil || count != 1 || bytes != int64(len("the plan")+len("Lena")) {
		t.Fatalf("size = %d %d %v", count, bytes, err)
	}
}
