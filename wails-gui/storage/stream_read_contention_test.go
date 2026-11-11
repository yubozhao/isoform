package storage

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestStreamPageReadersDoNotWaitForWriter(t *testing.T) {
	s := newTestStore(t)
	if err := s.SaveProject(ctx, ProjectRow{ID: "p", Name: "Fixture", Path: "fixture"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveSession(ctx, SessionRow{LocalID: "chat", ProjectID: "p", Label: "Fixture", Kind: SessionKindMain}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendStreamItem(ctx, "chat", StreamItemRow{ID: "one", Kind: "user", Content: "committed"}); err != nil {
		t.Fatal(err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE stream_items SET content='uncommitted' WHERE id='one'`); err != nil {
		t.Fatal(err)
	}
	// The old single-connection path cannot even start the query while the
	// writer owns the only connection; the deadline makes that reproducible.
	oldCtx, oldCancel := context.WithTimeout(ctx, 50*time.Millisecond)
	var oldVersion int64
	err = s.db.QueryRowContext(oldCtx, `SELECT version FROM stream_item_versions WHERE session_local_id='chat'`).Scan(&oldVersion)
	oldCancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("single-connection baseline: %v", err)
	}
	readCtx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	version, err := s.StreamItemVersion(readCtx, "chat")
	if err != nil || version != 1 {
		t.Fatalf("version = %d, %v", version, err)
	}
	page, err := s.LoadStreamItemsLatest(readCtx, "chat", 50)
	if err != nil || len(page.Items) != 1 || page.Items[0].Content != "committed" {
		t.Fatalf("page = %+v, %v", page, err)
	}
	metadata, err := s.LoadMainChatRowMetadata(readCtx, "chat", []string{"one"})
	if err != nil || metadata["one"].Row.Content != "committed" {
		t.Fatalf("metadata = %+v, %v", metadata, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	page, err = s.LoadStreamItemsLatest(context.Background(), "chat", 50)
	if err != nil || page.Items[0].Content != "uncommitted" {
		t.Fatalf("committed page = %+v, %v", page, err)
	}
}
