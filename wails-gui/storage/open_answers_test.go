package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func openAnswerFixture(id int) OpenAnswerRow {
	return OpenAnswerRow{ID: fmt.Sprintf("answer-%d", id), ChatSessionID: "chat", AssistantMessageID: fmt.Sprintf("message-%d", id), Question: "When?", Topic: "Result", Settles: "result", Source: "Results", WindowStart: 2000, WindowEnd: 3000, Delivery: "around", ExpiresAt: 4000, State: "open", CreatedAt: 1000}
}

func TestOpenAnswerAdmissionRequiresUnchangedTurn(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*testing.T, *Store, *OpenAnswerRow, *OpenAnswerTurn)
		want   bool
	}{
		{name: "same turn", want: true},
		{name: "question mismatch", mutate: func(_ *testing.T, _ *Store, a *OpenAnswerRow, _ *OpenAnswerTurn) { a.Question = "Another question" }},
		{name: "different run", mutate: func(_ *testing.T, _ *Store, _ *OpenAnswerRow, turn *OpenAnswerTurn) { turn.RunID = "other-run" }},
		{name: "empty run", mutate: func(_ *testing.T, _ *Store, _ *OpenAnswerRow, turn *OpenAnswerTurn) { turn.RunID = "" }},
		{name: "edited user", mutate: func(t *testing.T, s *Store, _ *OpenAnswerRow, _ *OpenAnswerTurn) {
			if err := s.UpdateStreamItemContent(t.Context(), "user", "Changed question"); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "edited answer", mutate: func(t *testing.T, s *Store, _ *OpenAnswerRow, _ *OpenAnswerTurn) {
			if err := s.UpdateStreamItemContent(t.Context(), "message-1", "Changed answer"); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "changed user provenance", mutate: func(t *testing.T, s *Store, _ *OpenAnswerRow, _ *OpenAnswerTurn) {
			if _, err := s.db.ExecContext(t.Context(), `UPDATE stream_items SET automation_context='changed' WHERE id='user'`); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "changed user attachments", mutate: func(t *testing.T, s *Store, _ *OpenAnswerRow, _ *OpenAnswerTurn) {
			if _, err := s.db.ExecContext(t.Context(), `UPDATE stream_items SET files='changed' WHERE id='user'`); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "changed user images", mutate: func(t *testing.T, s *Store, _ *OpenAnswerRow, _ *OpenAnswerTurn) {
			if _, err := s.db.ExecContext(t.Context(), `UPDATE stream_items SET images='changed' WHERE id='user'`); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "changed answer provenance", mutate: func(t *testing.T, s *Store, _ *OpenAnswerRow, _ *OpenAnswerTurn) {
			if _, err := s.db.ExecContext(t.Context(), `UPDATE stream_items SET automation_context='changed' WHERE id='message-1'`); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "changed answer attachments", mutate: func(t *testing.T, s *Store, _ *OpenAnswerRow, _ *OpenAnswerTurn) {
			if _, err := s.db.ExecContext(t.Context(), `UPDATE stream_items SET files='changed' WHERE id='message-1'`); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "changed answer images", mutate: func(t *testing.T, s *Store, _ *OpenAnswerRow, _ *OpenAnswerTurn) {
			if _, err := s.db.ExecContext(t.Context(), `UPDATE stream_items SET images='changed' WHERE id='message-1'`); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "matching provenance and attachments", want: true, mutate: func(t *testing.T, s *Store, _ *OpenAnswerRow, turn *OpenAnswerTurn) {
			if _, err := s.db.ExecContext(t.Context(), `UPDATE stream_items SET automation_context='context', images='images', files='files' WHERE id IN ('user','message-1')`); err != nil {
				t.Fatal(err)
			}
			turn.UserAutomationContext, turn.UserImages, turn.UserFiles = "context", "images", "files"
			turn.AnswerAutomationContext, turn.AnswerImages, turn.AnswerFiles = "context", "images", "files"
		}},
		{name: "rewound answer", mutate: func(t *testing.T, s *Store, _ *OpenAnswerRow, _ *OpenAnswerTurn) {
			if err := s.DeleteStreamItemsFrom(t.Context(), "chat", 1); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "another session", mutate: func(_ *testing.T, _ *Store, a *OpenAnswerRow, _ *OpenAnswerTurn) { a.ChatSessionID = "other" }},
		{name: "not main chat", mutate: func(t *testing.T, s *Store, _ *OpenAnswerRow, _ *OpenAnswerTurn) {
			if _, err := s.db.ExecContext(t.Context(), `UPDATE sessions SET kind='side' WHERE local_id='chat'`); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "intervening user", mutate: func(t *testing.T, s *Store, _ *OpenAnswerRow, _ *OpenAnswerTurn) {
			if _, err := s.db.ExecContext(t.Context(), `UPDATE stream_items SET sort_order=2 WHERE id='message-1'`); err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.ExecContext(t.Context(), `INSERT INTO stream_items(id,session_local_id,kind,sort_order,created_at) VALUES('intervening','chat','user',1,1)`); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "later user", mutate: func(t *testing.T, s *Store, _ *OpenAnswerRow, _ *OpenAnswerTurn) {
			appendProactiveTestItem(t, s, "chat", StreamItemRow{ID: "later-user", Kind: "user", Content: "What now?", RunID: "next-run"})
		}},
		{name: "later answer in same run", mutate: func(t *testing.T, s *Store, _ *OpenAnswerRow, _ *OpenAnswerTurn) {
			appendProactiveTestItem(t, s, "chat", StreamItemRow{ID: "later-answer", Kind: "assistant", Content: "Actually, it settled.", RunID: "run"})
		}},
		{name: "later tool in same run", mutate: func(t *testing.T, s *Store, _ *OpenAnswerRow, _ *OpenAnswerTurn) {
			appendProactiveTestItem(t, s, "chat", StreamItemRow{ID: "later-tool", Kind: "tool", ToolName: "Read", RunID: "run"})
		}},
		{name: "later tool use in same run", mutate: func(t *testing.T, s *Store, _ *OpenAnswerRow, _ *OpenAnswerTurn) {
			appendProactiveTestItem(t, s, "chat", StreamItemRow{ID: "later-use", Kind: "tool_use", ToolName: "Read", RunID: "run"})
		}},
		{name: "later tool result in same run", mutate: func(t *testing.T, s *Store, _ *OpenAnswerRow, _ *OpenAnswerTurn) {
			appendProactiveTestItem(t, s, "chat", StreamItemRow{ID: "later-result", Kind: "tool_result", Content: "New evidence", RunID: "run"})
		}},
		{name: "earlier tool in same run", want: true, mutate: func(t *testing.T, s *Store, _ *OpenAnswerRow, _ *OpenAnswerTurn) {
			if _, err := s.db.ExecContext(t.Context(), `UPDATE stream_items SET sort_order=2 WHERE id='message-1'`); err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.ExecContext(t.Context(), `INSERT INTO stream_items(id,session_local_id,kind,tool_name,run_id,sort_order,created_at) VALUES('earlier-tool','chat','tool','Read','run',1,1)`); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "later unrelated tool", want: true, mutate: func(t *testing.T, s *Store, _ *OpenAnswerRow, _ *OpenAnswerTurn) {
			appendProactiveTestItem(t, s, "chat", StreamItemRow{ID: "unrelated-tool", Kind: "tool", ToolName: "Read", RunID: "other-run"})
		}},
		{name: "later unrelated assistant", want: true, mutate: func(t *testing.T, s *Store, _ *OpenAnswerRow, _ *OpenAnswerTurn) {
			appendProactiveTestItem(t, s, "chat", StreamItemRow{ID: "unrelated", Kind: "assistant", Content: "Other output", RunID: "other-run"})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := proactiveItemsTestStore(t)
			if _, err := s.db.ExecContext(t.Context(), `UPDATE sessions SET kind='main' WHERE local_id='chat'`); err != nil {
				t.Fatal(err)
			}
			appendProactiveTestItem(t, s, "chat", StreamItemRow{ID: "user", Kind: "user", Content: "When?", RunID: "run"})
			appendProactiveTestItem(t, s, "chat", StreamItemRow{ID: "message-1", Kind: "assistant", Content: "Not yet.", RunID: "run"})
			a := openAnswerFixture(1)
			turn := OpenAnswerTurn{UserID: "user", UserContent: "When?", AnswerContent: "Not yet.", RunID: "run"}
			if tc.mutate != nil {
				tc.mutate(t, s, &a, &turn)
			}
			err := s.CreateOpenAnswerForTurn(t.Context(), a, turn)
			if tc.want && err != nil {
				t.Fatal(err)
			}
			if !tc.want && !errors.Is(err, ErrOpenAnswerTurnChanged) {
				t.Fatalf("error = %v", err)
			}
			rows, err := s.ListOpenAnswers(t.Context())
			if err != nil || (len(rows) == 1) != tc.want {
				t.Fatalf("rows = %+v, error = %v", rows, err)
			}
		})
	}
}

func TestOpenAnswerAdmissionRequiresCurrentSourceRevision(t *testing.T) {
	for _, writer := range []string{"unchanged", "local", "external"} {
		t.Run(writer, func(t *testing.T) {
			s, path := proactiveItemsTestStore(t)
			if _, err := s.db.ExecContext(t.Context(), `UPDATE sessions SET kind='main' WHERE local_id='chat'`); err != nil {
				t.Fatal(err)
			}
			appendProactiveTestItem(t, s, "chat", StreamItemRow{ID: "user", Kind: "user", Content: "When?", RunID: "run"})
			appendProactiveTestItem(t, s, "chat", StreamItemRow{ID: "message-1", Kind: "assistant", Content: "Not yet.", RunID: "run"})
			revision, err := s.ReadRevision(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if writer != "unchanged" {
				db := s.db
				if writer == "external" {
					db, err = sql.Open("sqlite", path)
					if err != nil {
						t.Fatal(err)
					}
					defer db.Close()
				}
				if _, err := db.ExecContext(t.Context(), `UPDATE sessions SET label='withdrawn' WHERE local_id='chat'`); err != nil {
					t.Fatal(err)
				}
			}
			turn := OpenAnswerTurn{UserID: "user", UserContent: "When?", AnswerContent: "Not yet.", RunID: "run"}
			err = s.CreateOpenAnswerForTurnIfCurrent(t.Context(), openAnswerFixture(1), turn, revision)
			if writer == "unchanged" && err != nil {
				t.Fatal(err)
			}
			if writer != "unchanged" && !errors.Is(err, ErrReadRevisionChanged) {
				t.Fatalf("stale admission error = %v", err)
			}
			rows, err := s.ListOpenAnswers(t.Context())
			if err != nil || (len(rows) == 1) != (writer == "unchanged") {
				t.Fatalf("rows = %+v, error = %v", rows, err)
			}
		})
	}
}

func TestOpenAnswerAdmissionRechecksRevisionAfterWaiting(t *testing.T) {
	s, _ := proactiveItemsTestStore(t)
	if _, err := s.db.ExecContext(t.Context(), `UPDATE sessions SET kind='main' WHERE local_id='chat'`); err != nil {
		t.Fatal(err)
	}
	appendProactiveTestItem(t, s, "chat", StreamItemRow{ID: "user", Kind: "user", Content: "When?", RunID: "run"})
	appendProactiveTestItem(t, s, "chat", StreamItemRow{ID: "message-1", Kind: "assistant", Content: "Not yet.", RunID: "run"})
	revision, err := s.ReadRevision(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	conn, err := s.db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	result := make(chan error, 1)
	go func() {
		turn := OpenAnswerTurn{UserID: "user", UserContent: "When?", AnswerContent: "Not yet.", RunID: "run"}
		result <- s.CreateOpenAnswerForTurnIfCurrent(t.Context(), openAnswerFixture(1), turn, revision)
	}()
	if _, err := conn.ExecContext(t.Context(), `UPDATE sessions SET label='withdrawn' WHERE local_id='chat'`); err != nil {
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if !errors.Is(err, ErrReadRevisionChanged) {
			t.Fatalf("queued admission error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("queued admission did not resume")
	}
	rows, err := s.ListOpenAnswers(t.Context())
	if err != nil || len(rows) != 0 {
		t.Fatalf("queued admission inserted rows = %+v, error = %v", rows, err)
	}
}

func TestOpenAnswerAdmissionAndChanges(t *testing.T) {
	s, _ := proactiveItemsTestStore(t)
	ctx := t.Context()
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(id int) { defer wg.Done(); results <- s.CreateOpenAnswer(ctx, openAnswerFixture(id)) }(i)
	}
	wg.Wait()
	close(results)
	admitted, refused := 0, 0
	for err := range results {
		switch errors.Is(err, ErrOpenAnswerLimit) {
		case true:
			refused++
		case false:
			if err != nil {
				t.Fatal(err)
			}
			admitted++
		}
	}
	if admitted != 5 || refused != 3 {
		t.Fatalf("admitted %d refused %d", admitted, refused)
	}
	rows, err := s.ListOpenAnswers(ctx)
	if err != nil || len(rows) != 5 {
		t.Fatalf("rows %v, error %v", rows, err)
	}
	if err := s.ChangeOpenAnswer(ctx, rows[0].ID, "brief"); err != nil {
		t.Fatal(err)
	}
	if err := s.ChangeOpenAnswer(ctx, rows[0].ID, "invalid"); err == nil {
		t.Fatal("invalid delivery accepted")
	}
	if err := s.CancelOpenAnswer(ctx, rows[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := s.ChangeOpenAnswer(ctx, rows[0].ID, "around"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("terminal update: %v", err)
	}
	if err := s.CreateOpenAnswer(ctx, openAnswerFixture(99)); err != nil {
		t.Fatal(err)
	}
	duplicate := openAnswerFixture(100)
	duplicate.AssistantMessageID = rows[0].AssistantMessageID
	if err := s.CancelOpenAnswer(ctx, rows[1].ID); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateOpenAnswer(ctx, duplicate); err == nil {
		t.Fatal("duplicate assistant answer accepted")
	}
}

func TestOpenAnswerHeldTransitionClearsWhenCommitted(t *testing.T) {
	s, _ := proactiveItemsTestStore(t)
	ctx := t.Context()
	answer := openAnswerFixture(1)
	if err := s.CreateOpenAnswer(ctx, answer); err != nil {
		t.Fatal(err)
	}
	if err := s.HoldOpenAnswerTransition(ctx, answer.ID, "gave_up", "I stopped checking.", 5000); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordOpenAnswerCheck(ctx, answer.ID, "stale value", 5500); err == nil {
		t.Fatal("held answer accepted a new check value")
	}
	if err := s.CommitOpenAnswerTransition(ctx, answer.ID, "interim", 5750, &StreamItemRow{
		ID: "stale-held-post", Kind: "assistant", Content: "The result is still pending.",
	}); err == nil {
		t.Fatal("held answer accepted a transition that did not match its held post")
	}
	held, err := s.GetOpenAnswer(ctx, answer.ID)
	if err != nil || held.State != "open" || held.Posts != 0 ||
		held.HeldTransition != "gave_up" || held.HeldText != "I stopped checking." || held.HeldAt != 5000 {
		t.Fatalf("held answer = %+v, %v", held, err)
	}
	if err := s.HoldOpenAnswerTransition(ctx, answer.ID, "settled", "The result is final.", 6000); err != nil {
		t.Fatal(err)
	}
	if err := s.HoldOpenAnswerTransition(ctx, answer.ID, "gave_up", "I stopped checking.", 7000); err != nil {
		t.Fatal(err)
	}
	held, err = s.GetOpenAnswer(ctx, answer.ID)
	if err != nil || held.HeldTransition != "settled" || held.HeldAt != 6000 {
		t.Fatalf("higher-priority held answer = %+v, %v", held, err)
	}
	message := &StreamItemRow{ID: "held-post", Kind: "assistant", Content: "The result is final."}
	if err := s.CommitOpenAnswerTransition(ctx, answer.ID, "settled", 8000, message); err != nil {
		t.Fatal(err)
	}
	committed, err := s.GetOpenAnswer(ctx, answer.ID)
	if err != nil || committed.State != "settled" || committed.Posts != 1 ||
		committed.HeldTransition != "" || committed.HeldText != "" || committed.HeldAt != 0 {
		t.Fatalf("committed answer = %+v, %v", committed, err)
	}
}

func TestOpenAnswerV72MigrationAddsQuietHoldColumns(t *testing.T) {
	s, dbPath := proactiveItemsTestStore(t)
	answer := openAnswerFixture(1)
	if err := s.CreateOpenAnswer(t.Context(), answer); err != nil {
		s.Close()
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, column := range []string{"held_transition", "held_text", "held_at"} {
		if _, err := raw.Exec(`ALTER TABLE open_answers DROP COLUMN ` + column); err != nil {
			raw.Close()
			t.Fatalf("remove %s from v71 fixture: %v", column, err)
		}
	}
	if _, err := raw.Exec(`PRAGMA user_version=71`); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	upgraded, err := New(dbPath)
	if err != nil {
		t.Fatalf("upgrade v71 database: %v", err)
	}
	defer upgraded.Close()
	migrated, err := upgraded.GetOpenAnswer(t.Context(), answer.ID)
	if err != nil || migrated.State != "open" || migrated.HeldTransition != "" || migrated.HeldText != "" || migrated.HeldAt != 0 {
		t.Fatalf("v72 migrated answer = %+v, %v", migrated, err)
	}
	var version int
	if err := upgraded.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != SchemaVersion {
		t.Fatalf("migrated schema version = %d, %v; want %d", version, err, SchemaVersion)
	}
}

func TestOpenAnswerAdmissionHonorsCurrentPreference(t *testing.T) {
	s, _ := proactiveItemsTestStore(t)
	ctx := t.Context()
	if _, err := s.db.ExecContext(ctx, `UPDATE sessions SET kind='main' WHERE local_id='chat'`); err != nil {
		t.Fatal(err)
	}
	appendProactiveTestItem(t, s, "chat", StreamItemRow{ID: "user", Kind: "user", Content: "When?", RunID: "run"})
	appendProactiveTestItem(t, s, "chat", StreamItemRow{ID: "message-1", Kind: "assistant", Content: "Not yet.", RunID: "run"})
	answer := openAnswerFixture(1)
	turn := OpenAnswerTurn{UserID: "user", UserContent: "When?", AnswerContent: "Not yet.", RunID: "run"}
	if err := s.SetOpenAnswerKindMuted(ctx, answer.Topic, true); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateOpenAnswerForTurn(ctx, answer, turn); !errors.Is(err, ErrOpenAnswerKindMuted) {
		t.Fatalf("muted admission error = %v", err)
	}
	if rows, err := s.ListOpenAnswers(ctx); err != nil || len(rows) != 0 {
		t.Fatalf("muted admission wrote rows = %+v, error = %v", rows, err)
	}
	if err := s.SetOpenAnswerKindMuted(ctx, answer.Topic, false); err != nil {
		t.Fatal(err)
	}
	if err := s.SetOpenAnswerKindMuted(ctx, "Other", true); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateOpenAnswerForTurn(ctx, answer, turn); err != nil {
		t.Fatalf("admission after undo: %v", err)
	}
}

func TestOpenAnswerPreferenceNormalizesAndUndoesLegacyKinds(t *testing.T) {
	s, _ := proactiveItemsTestStore(t)
	ctx := t.Context()
	if _, err := s.db.ExecContext(ctx, `UPDATE sessions SET kind='main' WHERE local_id='chat'`); err != nil {
		t.Fatal(err)
	}
	appendProactiveTestItem(t, s, "chat", StreamItemRow{ID: "user", Kind: "user", Content: "When?", RunID: "run"})
	appendProactiveTestItem(t, s, "chat", StreamItemRow{ID: "message-1", Kind: "assistant", Content: "Not yet.", RunID: "run"})
	// Existing installations may contain multiple differently spelled keys.
	for _, kind := range []string{"  ÉLECTION\tResults ", "e\u0301lection results", "Other"} {
		if _, err := s.db.ExecContext(ctx, `INSERT INTO open_answer_muted_kinds(kind) VALUES(?)`, kind); err != nil {
			t.Fatal(err)
		}
	}
	answer := openAnswerFixture(1)
	answer.Topic = "élection   RESULTS"
	turn := OpenAnswerTurn{UserID: "user", UserContent: "When?", AnswerContent: "Not yet.", RunID: "run"}
	if err := s.CreateOpenAnswerForTurn(ctx, answer, turn); !errors.Is(err, ErrOpenAnswerKindMuted) {
		t.Fatalf("equivalent legacy preference did not prevent admission: %v", err)
	}
	if err := s.SetOpenAnswerKindMuted(ctx, "ÉLECTION results", false); err != nil {
		t.Fatal(err)
	}
	if muted, err := s.OpenAnswerKindMuted(ctx, answer.Topic); err != nil || muted {
		t.Fatalf("undo left an equivalent preference: muted=%v err=%v", muted, err)
	}
	if muted, err := s.OpenAnswerKindMuted(ctx, "other"); err != nil || !muted {
		t.Fatalf("undo changed an unrelated preference: muted=%v err=%v", muted, err)
	}
	if err := s.SetOpenAnswerKindMuted(ctx, "  Straße\tresults ", true); err != nil {
		t.Fatal(err)
	}
	if muted, err := s.OpenAnswerKindMuted(ctx, "STRASSE results"); err != nil || !muted {
		t.Fatalf("case-folded preference missing: muted=%v err=%v", muted, err)
	}
	if err := s.CreateOpenAnswerForTurn(ctx, answer, turn); err != nil {
		t.Fatalf("admission after undo: %v", err)
	}
}

func TestOpenAnswerTransitionsAreAtomic(t *testing.T) {
	s, _ := proactiveItemsTestStore(t)
	ctx := t.Context()
	if err := s.CreateOpenAnswer(ctx, openAnswerFixture(1)); err != nil {
		t.Fatal(err)
	}
	post := func(id string) *StreamItemRow { return &StreamItemRow{ID: id, Kind: "assistant", Content: "News"} }
	if err := s.CommitOpenAnswerTransition(ctx, "answer-1", "interim", 3000, post("post-1")); err != nil {
		t.Fatal(err)
	}
	if err := s.CommitOpenAnswerTransition(ctx, "answer-1", "interim", 3000, post("post-2")); err == nil {
		t.Fatal("second interim accepted")
	}
	if err := s.CommitOpenAnswerTransition(ctx, "answer-1", "settled", 3001, post("post-1")); err == nil {
		t.Fatal("existing post id accepted")
	}
	row, err := s.GetOpenAnswer(ctx, "answer-1")
	if err != nil || row.Posts != 1 || row.State != "open" {
		t.Fatalf("failed write changed record: %+v %v", row, err)
	}
	if err := s.CommitOpenAnswerTransition(ctx, "answer-1", "settled", 3001, post("post-2")); err != nil {
		t.Fatal(err)
	}
	if err := s.CommitOpenAnswerTransition(ctx, "answer-1", "gave_up", 4000, post("post-3")); err == nil {
		t.Fatal("third post accepted")
	}
	row, err = s.GetOpenAnswer(ctx, "answer-1")
	if err != nil || row.Posts != 2 || row.State != "settled" {
		t.Fatalf("settled: %+v %v", row, err)
	}
	if err := s.CreateOpenAnswer(ctx, openAnswerFixture(2)); err != nil {
		t.Fatal(err)
	}
	if err := s.CommitOpenAnswerTransition(ctx, "answer-2", "saw_it", 3001, nil); err != nil {
		t.Fatal(err)
	}
	row, err = s.GetOpenAnswer(ctx, "answer-2")
	if err != nil || row.Posts != 0 || row.State != "saw_it" || row.SawAt != 3001 {
		t.Fatalf("saw_it: %+v %v", row, err)
	}
	if err := s.CreateOpenAnswer(ctx, openAnswerFixture(3)); err != nil {
		t.Fatal(err)
	}
	if err := s.CommitOpenAnswerTransition(ctx, "answer-3", "gave_up", 4000, post("closing")); err != nil {
		t.Fatal(err)
	}
	row, err = s.GetOpenAnswer(ctx, "answer-3")
	if err != nil || row.Posts != 1 || row.State != "gave_up" {
		t.Fatalf("gave up: %+v %v", row, err)
	}
}

func TestOpenAnswerMessageBatch(t *testing.T) {
	s, _ := proactiveItemsTestStore(t)
	ctx := t.Context()
	if err := s.CreateOpenAnswer(ctx, openAnswerFixture(1)); err != nil {
		t.Fatal(err)
	}
	if err := s.CommitOpenAnswerTransition(ctx, "answer-1", "settled", 3000, &StreamItemRow{ID: "post", Kind: "assistant", Content: "News"}); err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 605)
	for i := range ids {
		ids[i] = fmt.Sprintf("ordinary-%d", i)
	}
	ids[0], ids[600] = "message-1", "post"
	got, err := s.OpenAnswersForMessages(ctx, ids)
	if err != nil || len(got) != 2 || got["message-1"].Answer.ID != "answer-1" || got["message-1"].Post || got["post"].Answer.ID != "answer-1" || !got["post"].Post {
		t.Fatalf("batch = %+v, %v", got, err)
	}
}

func TestStreamOpenAnswerAssociationsReadDuringWriter(t *testing.T) {
	s, _ := proactiveItemsTestStore(t)
	ctx := t.Context()
	if err := s.CreateOpenAnswer(ctx, openAnswerFixture(1)); err != nil {
		t.Fatal(err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE open_answers SET topic='uncommitted' WHERE id='answer-1'`); err != nil {
		t.Fatal(err)
	}
	readCtx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	got, err := s.OpenAnswersForMessages(readCtx, []string{"message-1"})
	if err != nil || got["message-1"].Answer.ID != "answer-1" || got["message-1"].Answer.Topic == "uncommitted" {
		t.Fatalf("committed association while writer held = %+v, %v", got, err)
	}
}

func TestOpenAnswerAskDoesNotCountAndPromotesTransactionally(t *testing.T) {
	s, _ := proactiveItemsTestStore(t)
	ctx := t.Context()
	if _, err := s.db.ExecContext(ctx, `UPDATE sessions SET kind='main' WHERE local_id='chat'`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if err := s.CreateOpenAnswer(ctx, openAnswerFixture(i)); err != nil {
			t.Fatal(err)
		}
	}
	appendProactiveTestItem(t, s, "chat", StreamItemRow{ID: "ask-user", Kind: "user", Content: "When?", RunID: "ask-run"})
	appendProactiveTestItem(t, s, "chat", StreamItemRow{ID: "ask-message", Kind: "assistant", Content: "Not yet.", RunID: "ask-run"})
	answer := openAnswerFixture(9)
	answer.AssistantMessageID = "ask-message"
	answer.State = "ask"
	turn := OpenAnswerTurn{UserID: "ask-user", UserContent: "When?", AnswerContent: "Not yet.", RunID: "ask-run"}
	revision, err := s.ReadRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateOpenAnswerAskForTurnIfCurrent(ctx, answer, turn, revision); err != nil {
		t.Fatal(err)
	}
	appendProactiveTestItem(t, s, "chat", StreamItemRow{ID: "followup", Kind: "user", Content: "Let me know when it's settled."})
	rows, err := s.ListOpenAnswers(ctx)
	if err != nil || len(rows) != 5 {
		t.Fatalf("open rows = %d, error = %v", len(rows), err)
	}
	askRows, err := s.OpenAnswersForMessages(ctx, []string{"ask-message"})
	if err != nil || askRows["ask-message"].Answer.State != "ask" {
		t.Fatalf("ask projection = %+v, error = %v", askRows, err)
	}
	found, ok, err := s.OpenAnswerAskForTurn(ctx, "chat", "followup")
	if err != nil || !ok || found.ID != answer.ID {
		t.Fatalf("latest ask = %+v, found=%v, error=%v", found, ok, err)
	}
	if err := s.PromoteOpenAnswerAsk(ctx, answer.ID, time.UnixMilli(3000)); !errors.Is(err, ErrOpenAnswerLimit) {
		t.Fatalf("promotion at five-open limit = %v", err)
	}
	if err := s.CancelOpenAnswer(ctx, "answer-0"); err != nil {
		t.Fatal(err)
	}
	if err := s.PromoteOpenAnswerAsk(ctx, answer.ID, time.UnixMilli(3000)); err != nil {
		t.Fatal(err)
	}
	promoted, err := s.GetOpenAnswer(ctx, answer.ID)
	if err != nil || promoted.State != "open" {
		t.Fatalf("promoted answer = %+v, error = %v", promoted, err)
	}
}

func TestOpenAnswerAskForTurnFindsLatestAskBeforeInterveningReply(t *testing.T) {
	for _, tc := range []struct {
		name      string
		secondAsk bool
		wantID    string
	}{
		{name: "intervening assistant reply", wantID: "answer-20"},
		{name: "later ask wins", secondAsk: true, wantID: "answer-21"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := proactiveItemsTestStore(t)
			ctx := t.Context()
			if _, err := s.db.ExecContext(ctx, `UPDATE sessions SET kind='main' WHERE local_id='chat'`); err != nil {
				t.Fatal(err)
			}
			createAsk := func(index int, answerID string) {
				userID := fmt.Sprintf("ask-user-%d", index)
				assistantID := fmt.Sprintf("ask-message-%d", index)
				runID := fmt.Sprintf("ask-run-%d", index)
				appendProactiveTestItem(t, s, "chat", StreamItemRow{ID: userID, Kind: "user", Content: "When?", RunID: runID})
				appendProactiveTestItem(t, s, "chat", StreamItemRow{ID: assistantID, Kind: "assistant", Content: "Not yet.", RunID: runID})
				answer := openAnswerFixture(index)
				answer.ID = answerID
				answer.AssistantMessageID = assistantID
				answer.State = "ask"
				revision, err := s.ReadRevision(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if err := s.CreateOpenAnswerAskForTurnIfCurrent(ctx, answer, OpenAnswerTurn{
					UserID: userID, UserContent: "When?", AnswerContent: "Not yet.", RunID: runID,
				}, revision); err != nil {
					t.Fatal(err)
				}
			}
			createAsk(20, "answer-20")
			appendProactiveTestItem(t, s, "chat", StreamItemRow{ID: "intervening-reply", Kind: "assistant", Content: "Still waiting."})
			if tc.secondAsk {
				createAsk(21, "answer-21")
				appendProactiveTestItem(t, s, "chat", StreamItemRow{ID: "reply-after-second-ask", Kind: "assistant", Content: "Still waiting."})
			}
			appendProactiveTestItem(t, s, "chat", StreamItemRow{ID: "followup", Kind: "user", Content: "Let me know when it's settled."})
			found, ok, err := s.OpenAnswerAskForTurn(ctx, "chat", "followup")
			if err != nil || !ok || found.ID != tc.wantID {
				t.Fatalf("latest ask = %+v, found=%v, error=%v; want %s", found, ok, err, tc.wantID)
			}
		})
	}
}

func TestOpenAnswerAskPromotionRejectsMutedAndExpired(t *testing.T) {
	for _, tc := range []struct {
		name string
		now  time.Time
		mute bool
		want error
	}{
		{name: "muted", now: time.UnixMilli(3000), mute: true, want: ErrOpenAnswerKindMuted},
		{name: "expired", now: time.UnixMilli(4000), want: ErrOpenAnswerExpired},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := proactiveItemsTestStore(t)
			ctx := t.Context()
			if _, err := s.db.ExecContext(ctx, `UPDATE sessions SET kind='main' WHERE local_id='chat'`); err != nil {
				t.Fatal(err)
			}
			answer := openAnswerFixture(1)
			answer.State = "ask"
			appendProactiveTestItem(t, s, "chat", StreamItemRow{ID: "user", Kind: "user", Content: "When?", RunID: "run"})
			appendProactiveTestItem(t, s, "chat", StreamItemRow{ID: answer.AssistantMessageID, Kind: "assistant", Content: "Not yet.", RunID: "run"})
			if err := s.CreateOpenAnswerAskForTurnIfCurrent(ctx, answer, OpenAnswerTurn{
				UserID: "user", UserContent: "When?", AnswerContent: "Not yet.", RunID: "run",
			}, func() ReadRevision {
				revision, err := s.ReadRevision(ctx)
				if err != nil {
					t.Fatal(err)
				}
				return revision
			}()); err != nil {
				t.Fatal(err)
			}
			if tc.mute {
				if err := s.SetOpenAnswerKindMuted(ctx, answer.Topic, true); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.PromoteOpenAnswerAsk(ctx, answer.ID, tc.now); !errors.Is(err, tc.want) {
				t.Fatalf("promotion error = %v, want %v", err, tc.want)
			}
			stored, err := s.GetOpenAnswer(ctx, answer.ID)
			if err != nil || stored.State != "ask" {
				t.Fatalf("refused promotion changed row: %+v, %v", stored, err)
			}
		})
	}
}

func addUnopenedAnswerPost(t *testing.T, s *Store, id, topic string, createdAt time.Time, withMoment bool) string {
	t.Helper()
	answer := openAnswerFixture(1)
	answer.ID = id
	answer.AssistantMessageID = "promise-" + id
	answer.Topic = topic
	answer.CreatedAt = createdAt.UnixMilli()
	answer.WindowStart = createdAt.Add(-time.Hour).UnixMilli()
	answer.WindowEnd = createdAt.Add(time.Hour).UnixMilli()
	answer.ExpiresAt = createdAt.Add(7 * 24 * time.Hour).UnixMilli()
	if err := s.CreateOpenAnswer(t.Context(), answer); err != nil {
		t.Fatal(err)
	}
	messageID := "post-" + id
	if err := s.CommitOpenAnswerTransition(t.Context(), id, "settled", createdAt.UnixMilli(),
		&StreamItemRow{ID: messageID, Kind: "assistant", Content: "The answer.", CreatedAt: createdAt.UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	momentID := "moment-" + id
	if withMoment {
		if _, err := s.CreateEngagementMoment(t.Context(), EngagementMomentRow{
			ID: momentID, Kind: "answer_ready", SubjectID: id, DedupeKey: "answer_ready:" + id,
			CreatedAt: createdAt.UnixMilli(), Params: fmt.Sprintf(`{"message_id":%q}`, messageID),
		}); err != nil {
			t.Fatal(err)
		}
	}
	return momentID
}

func TestOpenAnswerUnopenedPostsMuteAndLearnNewerRecord(t *testing.T) {
	for _, tc := range []struct {
		name        string
		newerAge    time.Duration
		newerMoment bool
		opened      bool
		secondOlder bool
		wantMute    bool
	}{
		{name: "two unopened", newerAge: 48 * time.Hour, newerMoment: true, wantMute: true},
		{name: "one opened", newerAge: 48 * time.Hour, newerMoment: true, opened: true},
		{name: "recent unopened displaces two old posts", newerAge: 23 * time.Hour, newerMoment: true, secondOlder: true},
		{name: "recent opened displaces two old posts", newerAge: 23 * time.Hour, newerMoment: true, opened: true, secondOlder: true},
		{name: "notifications off", newerAge: 48 * time.Hour},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := proactiveItemsTestStore(t)
			ctx := t.Context()
			now := time.Now().UTC().Truncate(time.Millisecond)
			addUnopenedAnswerPost(t, s, "older", "Results", now.Add(-72*time.Hour), true)
			if tc.secondOlder {
				addUnopenedAnswerPost(t, s, "second-older", "Results", now.Add(-48*time.Hour), true)
			}
			newerMoment := addUnopenedAnswerPost(t, s, "newer", " results ", now.Add(-tc.newerAge), tc.newerMoment)
			if tc.opened {
				if err := s.RecordEngagementReceipt(ctx, EngagementReceiptRow{
					MomentID: newerMoment, Channel: "desktop", State: EngagementReceiptOpened, At: now.UnixMilli(),
				}); err != nil {
					t.Fatal(err)
				}
			}

			learned, err := s.LearnOpenAnswerKindsAfterUnopenedPosts(ctx, "RESULTS", now)
			if err != nil {
				t.Fatal(err)
			}
			muted, err := s.OpenAnswerKindMuted(ctx, "results")
			if err != nil {
				t.Fatal(err)
			}
			newer, err := s.GetOpenAnswer(ctx, "newer")
			if err != nil {
				t.Fatal(err)
			}
			if tc.wantMute {
				if len(learned) != 1 || learned[0].MessageID != "post-newer" || !muted || newer.LearnedAt != now.UnixMilli() {
					t.Fatalf("learned=%+v muted=%v newer=%+v", learned, muted, newer)
				}
				again, err := s.LearnOpenAnswerKindsAfterUnopenedPosts(ctx, "results", now.Add(time.Minute))
				if err != nil || len(again) != 0 {
					t.Fatalf("repeated evaluation = %+v, %v", again, err)
				}
				if err := s.SetOpenAnswerKindMuted(ctx, "RESULTS", false); err != nil {
					t.Fatal(err)
				}
				newer, err = s.GetOpenAnswer(ctx, "newer")
				if err != nil || newer.LearnedAt >= 0 {
					t.Fatalf("undo did not clear learned_at: %+v, %v", newer, err)
				}
				if muted, err = s.OpenAnswerKindMuted(ctx, "results"); err != nil || muted {
					t.Fatalf("undo left kind muted: %v, %v", muted, err)
				}
				undoAt := time.UnixMilli(-newer.LearnedAt)
				again, err = s.LearnOpenAnswerKindsAfterUnopenedPosts(ctx, "results", undoAt.Add(time.Minute))
				if err != nil || len(again) != 0 {
					t.Fatalf("pre-undo posts relearned the kind: %+v, %v", again, err)
				}
				if muted, err = s.OpenAnswerKindMuted(ctx, "results"); err != nil || muted {
					t.Fatalf("pre-undo posts remuted the kind: %v, %v", muted, err)
				}

				addUnopenedAnswerPost(t, s, "new-older", "Results", undoAt.Add(time.Hour), true)
				addUnopenedAnswerPost(t, s, "newer-recent", " results ", undoAt.Add(49*time.Hour), true)
				notYet, err := s.LearnOpenAnswerKindsAfterUnopenedPosts(ctx, "results", undoAt.Add(50*time.Hour))
				if err != nil || len(notYet) != 0 {
					t.Fatalf("one new old post learned the kind: %+v, %v", notYet, err)
				}
				again, err = s.LearnOpenAnswerKindsAfterUnopenedPosts(ctx, "results", undoAt.Add(75*time.Hour))
				if err != nil || len(again) != 1 || again[0].MessageID != "post-newer-recent" {
					t.Fatalf("two new old posts did not relearn the kind: %+v, %v", again, err)
				}
				if muted, err = s.OpenAnswerKindMuted(ctx, "results"); err != nil || !muted {
					t.Fatalf("new posts did not remute the kind: %v, %v", muted, err)
				}
			} else if len(learned) != 0 || muted || newer.LearnedAt != 0 {
				t.Fatalf("unexpected learning: learned=%+v muted=%v newer=%+v", learned, muted, newer)
			}
		})
	}
}

func TestOpenAnswerV73MigrationAddsLearnedAt(t *testing.T) {
	s, path := proactiveItemsTestStore(t)
	if err := s.CreateOpenAnswer(t.Context(), openAnswerFixture(1)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`ALTER TABLE open_answers DROP COLUMN learned_at; PRAGMA user_version=72`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	row, err := reopened.GetOpenAnswer(t.Context(), "answer-1")
	if err != nil || row.LearnedAt != 0 || row.Question != "When?" {
		t.Fatalf("migrated row = %+v, %v", row, err)
	}
}

func TestOpenAnswerPreferencesMigrateFrom68(t *testing.T) {
	s, path := proactiveItemsTestStore(t)
	ctx := t.Context()
	if err := s.CreateOpenAnswer(ctx, openAnswerFixture(1)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`DROP TABLE open_answer_muted_kinds; PRAGMA user_version=68`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	answer, err := reopened.GetOpenAnswer(ctx, "answer-1")
	if err != nil || answer.Question != "When?" {
		t.Fatalf("answer after migration = %+v, %v", answer, err)
	}
	if err := reopened.SetOpenAnswerKindMuted(ctx, "result", true); err != nil {
		t.Fatal(err)
	}
	muted, err := reopened.OpenAnswerKindMuted(ctx, "result")
	if err != nil || !muted {
		t.Fatalf("preference after migration = %v, %v", muted, err)
	}
}

func TestRestoreOpenAnswerSchedule(t *testing.T) {
	for _, tc := range []struct {
		name string
		held bool
	}{
		{name: "restores held values", held: true},
		{name: "stores empty held values as null"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := proactiveItemsTestStore(t)
			answer := openAnswerFixture(1)
			answer.WindowStart, answer.WindowEnd, answer.ExpiresAt = 2000, 3000, 4000
			if err := s.CreateOpenAnswer(t.Context(), answer); err != nil {
				t.Fatal(err)
			}
			if err := s.RecordOpenAnswerCheck(t.Context(), answer.ID, "previous value", 3500); err != nil {
				t.Fatal(err)
			}
			answer.LastValue, answer.LastCheckAt = "previous value", 3500
			if tc.held {
				if err := s.HoldOpenAnswerTransition(t.Context(), answer.ID, "settled", "previous held text", 3600); err != nil {
					t.Fatal(err)
				}
				answer.HeldTransition, answer.HeldText, answer.HeldAt = "settled", "previous held text", 3600
			}
			if err := s.RetimeOpenAnswer(t.Context(), answer.ID, 5000, 9000); err != nil {
				t.Fatal(err)
			}
			if err := s.RestoreOpenAnswerSchedule(t.Context(), answer); err != nil {
				t.Fatal(err)
			}
			got, err := s.GetOpenAnswer(t.Context(), answer.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.WindowStart != answer.WindowStart || got.WindowEnd != answer.WindowEnd ||
				got.ExpiresAt != answer.ExpiresAt || got.LastValue != answer.LastValue ||
				got.LastCheckAt != answer.LastCheckAt || got.HeldTransition != answer.HeldTransition ||
				got.HeldText != answer.HeldText || got.HeldAt != answer.HeldAt {
				t.Fatalf("restored schedule = %+v, want %+v", got, answer)
			}
			var heldTransition, heldText sql.NullString
			var heldAt sql.NullInt64
			if err := s.db.QueryRowContext(t.Context(), `SELECT held_transition, held_text, held_at
				FROM open_answers WHERE id=?`, answer.ID).Scan(&heldTransition, &heldText, &heldAt); err != nil {
				t.Fatal(err)
			}
			if tc.held {
				if !heldTransition.Valid || !heldText.Valid || !heldAt.Valid {
					t.Fatalf("held fields were null: transition=%+v text=%+v at=%+v", heldTransition, heldText, heldAt)
				}
			} else if heldTransition.Valid || heldText.Valid || heldAt.Valid {
				t.Fatalf("empty held fields were not null: transition=%+v text=%+v at=%+v", heldTransition, heldText, heldAt)
			}
		})
	}
}

func TestOpenAnswerBriefPostsIncludesFirstOpenedReceiptTime(t *testing.T) {
	s, _ := proactiveItemsTestStore(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	moment := addUnopenedAnswerPost(t, s, "post-time", "Results", now.Add(-time.Hour), true)
	for _, at := range []int64{now.Add(-time.Minute).UnixMilli(), now.UnixMilli()} {
		if err := s.RecordEngagementReceipt(t.Context(), EngagementReceiptRow{MomentID: moment, Channel: "desktop", State: EngagementReceiptOpened, At: at}); err != nil {
			t.Fatal(err)
		}
	}
	posts, err := s.OpenAnswerBriefPosts(t.Context(), now.Add(-2*time.Hour), now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(posts) != 1 || !posts[0].OpenedOrActed || posts[0].OpenedAt != now.Add(-time.Minute).UnixMilli() {
		t.Fatalf("posts = %+v", posts)
	}
}
