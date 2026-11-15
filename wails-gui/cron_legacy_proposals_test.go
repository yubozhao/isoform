package main

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"wails-gui/storage"
)

const agencyProposalTestID = "agency-schedule-proposal-1"

// newAgencyProposalTestRig is a running scheduler holding one paused schedule
// the Agency proposed and nobody approved, stored the way the Agency stored
// its proposals before it was removed.
func newAgencyProposalTestRig(t *testing.T) *handoffTestRig {
	t.Helper()
	rig := newHandoffTestRig(t)
	if err := rig.state.SaveCronJob(context.Background(), storage.CronJobRow{
		ID:               agencyProposalTestID,
		Name:             "Weekly release notes",
		Description:      "Draft the weekly release notes",
		Enabled:          false,
		ScheduleKind:     "every",
		ScheduleConfig:   `{"every_ms":604800000}`,
		ScheduleEvidence: "You write the release notes every Friday.",
		TargetKind:       "agent",
		TargetConfig:     `{"prompt":"Draft the weekly release notes."}`,
		SourcePlatform:   "agency",
		SurfaceKind:      "schedule_permission",
		NotifyDM:         true,
		NextRunAt:        time.Now().Add(7 * 24 * time.Hour).UnixMilli(),
	}); err != nil {
		t.Fatal(err)
	}
	scheduler := NewCronScheduler(rig.state, func(string, any) {}, func(string, string) {}, nil)
	rig.app.cronScheduler = scheduler
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		scheduler.Run(ctx)
	}()
	select {
	case <-scheduler.loadedCh:
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatal("scheduler did not load")
	}
	t.Cleanup(func() {
		cancel()
		scheduler.Stop()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("scheduler did not stop")
		}
	})
	return rig
}

func agencyProposalApprovals(t *testing.T, rig *handoffTestRig) int {
	t.Helper()
	var count int
	if err := rig.activity.db.QueryRow(`
		SELECT COUNT(*) FROM agency_permission_decision_receipts_v1
		WHERE request_id = ? AND decision = 'approve'`,
		agencyProposalTestID,
	).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestAgencyScheduleProposalWaitsForTheUser(t *testing.T) {
	rig := newAgencyProposalTestRig(t)
	job, err := rig.app.GetCronJob(agencyProposalTestID)
	if err != nil {
		t.Fatal(err)
	}
	if unapproved, err := rig.app.agencyScheduleUnapproved(job); err != nil || !unapproved {
		t.Fatalf("fixture is not an unapproved proposal: unapproved=%v err=%v", unapproved, err)
	}
	enabled := true
	if _, err := rig.app.updateCronJobFromAPI(agencyProposalTestID, CronJobPatch{Enabled: &enabled}); !errors.Is(err, errAgencyScheduleAwaitsUser) {
		t.Fatalf("an agent CLI turned the proposal on: %v", err)
	}
	if err := rig.app.deleteCronJobFromAPI(agencyProposalTestID); !errors.Is(err, errAgencyScheduleAwaitsUser) {
		t.Fatalf("an agent CLI deleted the proposal: %v", err)
	}
	if err := rig.app.RunCronJobNow(agencyProposalTestID); !errors.Is(err, errAgencyScheduleAwaitsUser) {
		t.Fatalf("Run now ran an unapproved proposal: %v", err)
	}
	if got := agencyProposalApprovals(t, rig); got != 0 {
		t.Fatalf("refusals wrote %d approvals", got)
	}
}

func TestAutomationsTurningOnAnAgencyProposalApprovesIt(t *testing.T) {
	rig := newAgencyProposalTestRig(t)
	enabled := true
	name := "Friday release notes"
	state, err := rig.app.UpdateCronJob(agencyProposalTestID, CronJobPatch{Enabled: &enabled, Name: &name})
	if err != nil {
		t.Fatalf("turn on the proposal: %v", err)
	}
	if !state.Enabled || state.Name != name {
		t.Fatalf("approved schedule = %+v", state)
	}
	if got := agencyProposalApprovals(t, rig); got != 1 {
		t.Fatalf("approvals = %d, want 1", got)
	}

	// A later pause does not make it a proposal again.
	disabled := false
	paused, err := rig.app.UpdateCronJob(agencyProposalTestID, CronJobPatch{Enabled: &disabled})
	if err != nil || paused.Enabled {
		t.Fatalf("pause = %+v err=%v", paused, err)
	}
	if unapproved, err := rig.app.agencyScheduleUnapproved(paused); err != nil || unapproved {
		t.Fatalf("paused approved schedule reads as unapproved=%v err=%v", unapproved, err)
	}
	if _, err := rig.app.updateCronJobFromAPI(agencyProposalTestID, CronJobPatch{Enabled: &enabled}); err != nil {
		t.Fatalf("an approved schedule is an ordinary automation for agent CLIs: %v", err)
	}
	if got := agencyProposalApprovals(t, rig); got != 1 {
		t.Fatalf("resuming wrote another approval: %d", got)
	}
}

func TestAutomationsDeletingAnAgencyProposalRemovesIt(t *testing.T) {
	rig := newAgencyProposalTestRig(t)
	if err := rig.app.DeleteCronJob(agencyProposalTestID); err != nil {
		t.Fatalf("delete the proposal: %v", err)
	}
	if _, err := rig.state.LoadCronJob(context.Background(), agencyProposalTestID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("deleted proposal is still stored: %v", err)
	}
	if got := agencyProposalApprovals(t, rig); got != 0 {
		t.Fatalf("deleting wrote %d approvals", got)
	}
}

func TestAgencyScheduleProposalFailsClosedWithoutItsReceipts(t *testing.T) {
	rig := newAgencyProposalTestRig(t)
	job, err := rig.app.GetCronJob(agencyProposalTestID)
	if err != nil {
		t.Fatal(err)
	}
	withoutReceipts := &App{cronScheduler: rig.app.cronScheduler}
	if _, err := withoutReceipts.agencyScheduleUnapproved(job); err == nil {
		t.Fatal("a proposal was told apart from an approved schedule without its receipts")
	}
	if err := withoutReceipts.RunCronJobNow(agencyProposalTestID); err == nil {
		t.Fatal("Run now ran a proposal whose approval could not be checked")
	}
	enabled := true
	if _, err := withoutReceipts.UpdateCronJob(agencyProposalTestID, CronJobPatch{Enabled: &enabled}); err == nil {
		t.Fatal("a proposal was turned on without recording its approval")
	}
}
