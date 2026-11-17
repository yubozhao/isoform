package storage

import (
	"context"
	"errors"
	"testing"
)

func TestClearMainChatEmptiesItAndKeepsTheSession(t *testing.T) {
	s := topicTestStore(t)
	ctx := context.Background()
	if err := s.SaveSession(ctx, SessionRow{LocalID: "other", ProjectID: "p"}); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct{ session, id, topic string }{{"main", "m1", "alpha"}, {"main", "m2", ""}, {"other", "o1", ""}} {
		if _, err := s.AppendUserStreamItemAtomic(ctx, item.session, StreamItemRow{ID: item.id, Kind: "user", Content: "words", TopicID: item.topic}); err != nil {
			t.Fatal(err)
		}
	}
	id, err := s.ClearMainChat(ctx)
	if err != nil || id != "main" {
		t.Fatalf("clear = %q %v", id, err)
	}
	var mainItems, otherItems, topics int
	_ = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM stream_items WHERE session_local_id = 'main'`).Scan(&mainItems)
	_ = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM stream_items WHERE session_local_id = 'other'`).Scan(&otherItems)
	_ = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM stream_item_topics WHERE session_id = 'main'`).Scan(&topics)
	if mainItems != 0 || topics != 0 || otherItems != 1 {
		t.Fatalf("main=%d topics=%d other=%d", mainItems, topics, otherItems)
	}
	row, err := s.LoadSession(ctx, "main")
	if err != nil || row == nil || row.Kind != SessionKindMain {
		t.Fatalf("the main chat must stay: %+v %v", row, err)
	}
	// It still can't be deleted.
	if err := s.DeleteSession(ctx, "main"); !errors.Is(err, ErrMainChatProtected) {
		t.Fatalf("delete main = %v", err)
	}
}
