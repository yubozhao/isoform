package storage

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestPrincipalIDsReadsCommittedRowsWhileWriterIsBusy(t *testing.T) {
	s := newTestStore(t)
	person := mkContact(t, s, "Ada")
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec("DELETE FROM principals WHERE id = ?", person.ID); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	ids, err := s.PrincipalIDs(ctx)
	if err != nil || !ids[person.ID] {
		t.Fatalf("committed principal missing during pending delete: ids=%v err=%v", ids, err)
	}
}

func TestCommittedReaderWriterRollback(t *testing.T) {
	s := newTestStore(t)
	prev := storeReadsOnWriter
	storeReadsOnWriter = true
	t.Cleanup(func() { storeReadsOnWriter = prev })
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	if _, err := s.PrincipalIDs(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("read with writer occupied: got %v, want deadline exceeded", err)
	}
}

func TestLoadMainSessionReadsCommittedRowsWhileWriterIsBusy(t *testing.T) {
	s := newTestStore(t)
	seedKindProject(t, s)
	if err := s.SaveSession(t.Context(), SessionRow{LocalID: "main", ProjectID: "__standalone_chat__", WorkspaceID: "ws-standalone", Kind: SessionKindMain}); err != nil {
		t.Fatal(err)
	}
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	got, err := s.LoadMainSession(ctx)
	if err != nil || got == nil || got.LocalID != "main" {
		t.Fatalf("read with writer occupied: got %+v, err %v", got, err)
	}
}

func TestListCronRunsReadsCommittedRowsWhileWriterIsBusy(t *testing.T) {
	s := newTestStore(t)
	if err := s.SaveCronJob(t.Context(), CronJobRow{ID: "job", Name: "Playbook", ScheduleKind: "manual", TargetKind: "agent"}); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertCronRun(t.Context(), CronRunRow{ID: "run", JobID: "job", Status: "ok"}); err != nil {
		t.Fatal(err)
	}
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	got, err := s.ListCronRuns(ctx, "job", 1)
	if err != nil || len(got) != 1 || got[0].ID != "run" {
		t.Fatalf("read with writer occupied: got %+v, err %v", got, err)
	}
}
