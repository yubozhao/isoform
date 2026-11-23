package storage

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"wails-gui/internal/destinations"
)

func lossFlags(t *testing.T, s *Store, now time.Time) map[destinations.ID]bool {
	t.Helper()
	rows, err := s.WeeklyDestinationCounts(t.Context(), now)
	if err != nil {
		t.Fatal(err)
	}
	flags := map[destinations.ID]bool{}
	for _, row := range rows {
		if row.Incomplete {
			flags[row.ID] = true
		}
	}
	return flags
}

func TestDestinationCleanShutdownDrainsPending(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.db")
	open := func() *Store {
		t.Helper()
		s, err := New(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.StartDestinationRecorder(); err != nil {
			t.Fatal(err)
		}
		return s
	}
	s := open()
	for i := 0; i < 64; i++ {
		destinations.RecordAnswer("codex")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = open()
	defer s.Close()
	rows, err := s.WeeklyDestinationCounts(t.Context(), time.Now())
	if err != nil || len(rows) != 1 || rows[0].ID != destinations.AgentsMCPRead || rows[0].Calls != 64 || rows[0].Incomplete {
		t.Fatalf("after clean restart: %+v %v", rows, err)
	}
}

func TestDestinationForcedDropAndExpiry(t *testing.T) {
	s := newTestStore(t)
	if err := s.StartDestinationRecorder(); err != nil {
		t.Fatal(err)
	}
	// The store's only writer is held. Sends must still finish and saturate
	// the bounded queue. All lost sends have the email destination.
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	sent := make(chan struct{})
	go func() {
		for i := 0; i < 1024; i++ {
			destinations.Record(destinations.Email)
		}
		close(sent)
	}()
	select {
	case <-sent:
	case <-time.After(2 * time.Second):
		t.Fatal("send waited on SQLite")
	}
	at, ok := s.destinationRecorder.Losses()[destinations.Email]
	if !ok {
		t.Fatal("queue loss was invisible while SQLite was busy")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := s.destinationRecorder.Close(); err != nil {
		t.Fatal(err)
	}
	// Read just SQLite, as a new process would.
	s.destinationRecorder = nil
	before := at.Add(destinationWeek - time.Nanosecond)
	if flags := lossFlags(t, s, before); len(flags) != 1 || !flags[destinations.Email] {
		t.Fatalf("loss flags %v", flags)
	}
	if flags := lossFlags(t, s, at.Add(destinationWeek)); len(flags) != 0 {
		t.Fatalf("expired flags %v", flags)
	}
}

func TestDestinationFailedWriteStaysIncomplete(t *testing.T) {
	s := newTestStore(t)
	if err := s.StartDestinationRecorder(); err != nil {
		t.Fatal(err)
	}
	_, err := s.db.Exec(`CREATE TRIGGER fail_count BEFORE INSERT ON destination_calls BEGIN SELECT RAISE(FAIL,'count failure'); END;
		CREATE TRIGGER fail_mark BEFORE INSERT ON destination_count_losses BEGIN SELECT RAISE(FAIL,'mark failure'); END;`)
	if err != nil {
		t.Fatal(err)
	}
	destinations.RecordSearch("grok")
	if flags := lossFlags(t, s, time.Now()); !flags[destinations.AssistantSearch] {
		t.Fatal("lost count was presented as exact")
	}
	if err := s.destinationRecorder.Close(); err == nil {
		t.Fatal("shutdown claimed a clean flush")
	}
	s.destinationRecorder = nil
	if _, err := s.db.Exec("DROP TRIGGER fail_mark"); err != nil {
		t.Fatal(err)
	}
	if err := s.StartDestinationRecorder(); err != nil {
		t.Fatal(err)
	}
	if flags := lossFlags(t, s, time.Now()); !flags[destinations.AssistantSearch] {
		t.Fatal("unflushed mark lost at recovery")
	}
}

func TestDestinationCrashRecovery(t *testing.T) {
	const childDB = "ISOFORM_TEST_DESTINATION_CRASH_DB"
	if path := os.Getenv(childDB); path != "" {
		s, err := New(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.StartDestinationRecorder(); err != nil {
			t.Fatal(err)
		}
		tx, err := s.db.BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		destinations.RecordAnswer("codex")
		os.Exit(23) // actual process death with an uncommitted event
	}
	path := filepath.Join(t.TempDir(), "sessions.db")
	cmd := exec.Command(os.Args[0], "-test.run=^TestDestinationCrashRecovery$")
	cmd.Env = append(os.Environ(), childDB+"="+path)
	output, err := cmd.CombinedOutput()
	if cmd.ProcessState == nil || cmd.ProcessState.ExitCode() != 23 {
		t.Fatalf("child: %v %s", err, output)
	}
	s, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.StartDestinationRecorder(); err != nil {
		t.Fatal(err)
	}
	recovered := s.destinationRecorder.Losses()[destinations.Email]
	if recovered.IsZero() {
		t.Fatal("lost the dirty run marker")
	}
	flags := lossFlags(t, s, recovered)
	for _, d := range destinations.List() {
		if flags[d.ID] != (d.Content != destinations.ContentNone) {
			t.Errorf("wrong recovery flag for %s", d.ID)
		}
	}
	if flags := lossFlags(t, s, recovered.Add(destinationWeek)); len(flags) != 0 {
		t.Fatalf("expired recovery flags %v", flags)
	}
}

func TestDestinationPreviewCountsHaveUnknownCoverage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preview.db")
	s, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("DROP TABLE destination_count_losses; PRAGMA user_version=78"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = New(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.StartDestinationRecorder(); err != nil {
		t.Fatal(err)
	}
	if !lossFlags(t, s, time.Now())[destinations.Email] {
		t.Fatal("preview totals reported as exact")
	}
}

func TestDestinationFailedWritePersistsOnlyItsDestination(t *testing.T) {
	s := newTestStore(t)
	if err := s.StartDestinationRecorder(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`CREATE TRIGGER fail_text BEFORE INSERT ON destination_calls
		WHEN NEW.destination='text_relay' BEGIN SELECT RAISE(FAIL,'unavailable'); END;`); err != nil {
		t.Fatal(err)
	}
	destinations.Record(destinations.TextRelay)
	destinations.Record(destinations.Email)
	if err := s.destinationRecorder.Close(); err != nil {
		t.Fatal(err)
	}
	s.destinationRecorder = nil
	if err := s.StartDestinationRecorder(); err != nil {
		t.Fatal(err)
	}
	flags := lossFlags(t, s, time.Now())
	if len(flags) != 1 || !flags[destinations.TextRelay] {
		t.Fatalf("expected only text_relay after clean restart, got %v", flags)
	}
}

func TestDestinationStartupMarkerWriteFailureKeepsSendsAvailable(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.db.Exec(`CREATE TRIGGER reject_marker BEFORE INSERT ON destination_count_run
		BEGIN SELECT RAISE(FAIL,'marker unavailable'); END;`); err != nil {
		t.Fatal(err)
	}
	if err := s.StartDestinationRecorder(); err != nil {
		t.Fatal(err)
	}
	destinations.Record(destinations.Email)
	rows, err := s.WeeklyDestinationCounts(t.Context(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var email int
	for _, row := range rows {
		if row.ID == destinations.Email {
			email += row.Calls
		}
	}
	if email != 1 {
		t.Fatalf("recorded %d sends", email)
	}
	if _, err := os.Stat(s.destinationRunFile); err != nil {
		t.Fatal(err)
	}
	if err := s.destinationRecorder.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.destinationRunFile); !os.IsNotExist(err) {
		t.Fatalf("clean close retained fallback marker: %v", err)
	}
}
