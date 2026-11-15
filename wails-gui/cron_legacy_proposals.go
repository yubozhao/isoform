package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Schedules the Agency proposed.
//
// The Agency, removed on 2026-10-13, proposed recurring schedules as paused
// automations (source "agency" with proposal markers) that waited for the
// user's approval. Nothing proposes them any more, but proposals created
// before stay in Automations as paused automations. Turning one on in
// Automations is the user's approval: it records an approve receipt in
// agency_permission_decision_receipts_v1, where earlier approvals were
// written, then turns the schedule on, so a later pause never makes it a
// proposal again. Deleting one just deletes it.
//
// Only the user decides a proposal. The cron REST API serves agent CLIs (a
// chat or a scheduled run calling `yansu cron`), so it cannot turn a proposal
// on or delete it, and nobody can run one before it is approved.

// errAgencyScheduleAwaitsUser marks a change refused because the automation is
// an Agency proposal nobody approved yet.
var errAgencyScheduleAwaitsUser = errors.New(
	"it is a schedule Isoform proposed that is waiting for approval",
)

const (
	agencyScheduleUserDecidesRemedy = "only the user can turn it on or delete it, in Automations on the computer"
	agencyScheduleRunRemedy         = "it can run once it is turned on in Automations"
)

// agencyScheduleHasProposalMarkers reports whether job carries the marks the
// Agency put on a schedule it proposed.
func agencyScheduleHasProposalMarkers(job CronJobState) bool {
	surface := strings.ToLower(strings.TrimSpace(job.SurfaceKind))
	return strings.TrimSpace(job.ScheduleEvidence) != "" ||
		surface == "schedule_permission" ||
		surface == "scheduled_pending"
}

// agencyScheduleDecisionDB is the activity database that holds the approval
// receipts.
func (a *App) agencyScheduleDecisionDB() (*sql.DB, error) {
	if a == nil || a.activity == nil || a.activity.store == nil ||
		a.activity.store.db == nil {
		return nil, errors.New("agency schedule decisions are unavailable")
	}
	return a.activity.store.db, nil
}

// agencyScheduleUnapproved reports whether job is a paused Agency proposal
// nobody approved. Without the receipt storage a proposal cannot be told apart
// from an approved schedule, so it fails closed.
func (a *App) agencyScheduleUnapproved(job CronJobState) (bool, error) {
	if job.Enabled ||
		!strings.EqualFold(strings.TrimSpace(job.SourcePlatform), "agency") ||
		!agencyScheduleHasProposalMarkers(job) {
		return false, nil
	}
	db, err := a.agencyScheduleDecisionDB()
	if err != nil {
		return false, fmt.Errorf("check Agency schedule %s: %w", job.ID, err)
	}
	approved, err := agencyScheduleHasApproveReceipt(db, job.ID)
	if err != nil {
		return false, fmt.Errorf(
			"load Agency schedule approval for %s: %w",
			job.ID,
			err,
		)
	}
	return !approved, nil
}

// refuseUnapprovedAgencySchedule returns an error wrapping
// errAgencyScheduleAwaitsUser when job is an unapproved Agency proposal.
// action completes "cannot ... <name>" and remedy says what may happen
// instead.
func (a *App) refuseUnapprovedAgencySchedule(
	job CronJobState,
	action, remedy string,
) error {
	unapproved, err := a.agencyScheduleUnapproved(job)
	if err != nil {
		return err
	}
	if !unapproved {
		return nil
	}
	name := strings.TrimSpace(job.Name)
	if name == "" {
		name = job.ID
	}
	return fmt.Errorf("cannot %s %q: %w; %s", action, name, errAgencyScheduleAwaitsUser, remedy)
}

// approveAgencyScheduleFromAutomations turns on a pending Agency proposal for
// the user. Other edits in the same patch land first, while the schedule is
// still paused, so the approval covers what the user actually turned on.
func (a *App) approveAgencyScheduleFromAutomations(
	id string,
	patch CronJobPatch,
) (CronJobState, error) {
	db, err := a.agencyScheduleDecisionDB()
	if err != nil {
		return CronJobState{}, fmt.Errorf("turn on Agency schedule %s: %w", id, err)
	}
	edits := patch
	edits.Enabled = nil
	if edits != (CronJobPatch{}) {
		if _, err := a.cronScheduler.UpdateJob(a.ctx, id, edits); err != nil {
			return CronJobState{}, err
		}
	}
	if err := recordAgencyScheduleApproval(db, id, time.Now()); err != nil {
		return CronJobState{}, fmt.Errorf("turn on Agency schedule %s: %w", id, err)
	}
	return a.cronScheduler.UpdateJob(a.ctx, id, CronJobPatch{Enabled: patch.Enabled})
}

// recordAgencyScheduleApproval appends the terminal approve receipt that
// agencyScheduleHasApproveReceipt reads.
func recordAgencyScheduleApproval(db *sql.DB, jobID string, now time.Time) error {
	jobID = strings.TrimSpace(jobID)
	if db == nil || jobID == "" {
		return errors.New("agency schedule approval requires a schedule")
	}
	payload, err := json.Marshal(map[string]string{
		"requestId": jobID,
		"kind":      "schedule",
		"decision":  "approve",
		"source":    "automations",
	})
	if err != nil {
		return err
	}
	attempt := uuid.NewString()
	_, err = db.Exec(`
		INSERT INTO agency_permission_decision_receipts_v1 (
			id, idempotency_key, stage, status, request_id, kind, decision,
			request_source, request_snapshot_json, provenance_json,
			before_state_json, after_state_json, result_json,
			occurred_at, payload_json, payload_hash
		) VALUES (?, ?, 'terminal', 'succeeded', ?, 'schedule', 'approve',
			'schedule', '{}', '{}', '{}', '{}', '{}', ?, ?, ?)`,
		deterministicAgencyID("agency-schedule-automations-approval", jobID+"\x00"+attempt),
		"automations:approve:"+jobID+":"+attempt,
		jobID,
		now.UnixMilli(),
		string(payload),
		agencyPayloadHash(payload),
	)
	return err
}

// agencyScheduleHasApproveReceipt reports whether the schedule's proposal was
// approved, from Automations or earlier from the Desk or the phone.
func agencyScheduleHasApproveReceipt(db *sql.DB, requestID string) (bool, error) {
	requestID = strings.TrimSpace(requestID)
	if db == nil || requestID == "" {
		return false, nil
	}
	var count int
	err := db.QueryRow(`
		SELECT COUNT(*)
		FROM agency_permission_decision_receipts_v1
		WHERE request_id = ?
			AND stage = 'terminal'
			AND status = 'succeeded'
			AND kind = 'schedule'
			AND decision = 'approve'`, requestID).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}
