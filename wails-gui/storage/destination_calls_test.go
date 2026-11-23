package storage

import (
	"testing"
	"time"

	"wails-gui/internal/destinations"
)

func TestWeeklyDestinationCalls(t *testing.T) {
	s := newTestStore(t)
	now := time.Now()
	if _, err := s.db.Exec("INSERT INTO destination_calls VALUES('email', ?, '', 32, 0)", now.Add(-destinationWeek-time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	for _, detail := range []string{"codex", "claude-code", "codex"} {
		if err := s.RecordDestination(ctx, destinations.Call{ID: destinations.AgentsMCPRead, Detail: detail, At: now}); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := s.WeeklyDestinationCounts(ctx, now)
	if err != nil || len(rows) != 2 || rows[0].Detail != "claude-code" || rows[0].Calls != 1 || rows[1].Detail != "codex" || rows[1].Calls != 2 {
		t.Fatalf("counts: %+v, %v", rows, err)
	}
	var buckets int
	if err := s.db.QueryRow("SELECT count(*) FROM destination_calls").Scan(&buckets); err != nil || buckets != 2 {
		t.Fatalf("expected 2 live buckets: %d, %v", buckets, err)
	}
	if _, err := s.db.Exec("DROP TABLE destination_calls"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WeeklyDestinationCounts(ctx, now); err == nil {
		t.Fatal("lost counter returned success")
	}
}

func TestLastSearchServiceOutlivesWeek(t *testing.T) {
	s := newTestStore(t)
	now := time.Now()
	for _, call := range []destinations.Call{
		{ID: destinations.AssistantSearch, Detail: "gemini", At: now},
		{ID: destinations.AssistantSearch, Detail: "grok", At: now.Add(-time.Millisecond)},
	} {
		if err := s.RecordDestination(ctx, call); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.Exec("UPDATE destination_calls SET second=0"); err != nil {
		t.Fatal(err)
	}
	if err := s.PruneDestinationCalls(ctx); err != nil {
		t.Fatal(err)
	}
	rows, err := s.WeeklyDestinationCounts(ctx, now)
	if err != nil || len(rows) != 0 {
		t.Fatalf("expired: %+v %v", rows, err)
	}
	service, found, err := s.LookupUIState(ctx, LastSearchServiceKey)
	if err != nil || !found || service != "gemini" {
		t.Fatalf("last service: %q %v %v", service, found, err)
	}
}
