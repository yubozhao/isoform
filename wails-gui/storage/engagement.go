package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Engagement moment states. A moment is pending until the pacing rules look
// at it, scheduled while it waits for a digest or the end of quiet hours, sent
// once delivered, and suppressed or expired when it never goes out.
const (
	EngagementStatePending    = "pending"
	EngagementStateScheduled  = "scheduled"
	EngagementStateSent       = "sent"
	EngagementStateSuppressed = "suppressed"
	EngagementStateExpired    = "expired"
)

// Engagement receipt states. Receipts form an append-only lifecycle; later
// observations never replace an earlier state.
const (
	EngagementReceiptCreated   = "created"
	EngagementReceiptScheduled = "scheduled"
	EngagementReceiptSent      = "sent"
	EngagementReceiptDelivered = "delivered"
	EngagementReceiptOpened    = "opened"
	EngagementReceiptActed     = "acted"
	EngagementReceiptDismissed = "dismissed"
	EngagementReceiptMuted     = "muted"
)

// Completion receipts carry an internal provenance marker. Older acted
// receipts described button clicks, so the state alone cannot prove completion.
const (
	EngagementCompletionChannel = "inferred"
	EngagementCompletionAction  = "item_completed"
)

// EngagementMomentRow is one candidate reason to reach the user. Times are
// Unix milliseconds; zero means unset.
type EngagementMomentRow struct {
	ID        string
	Kind      string
	SubjectID string
	DedupeKey string
	Score     float64
	// Stakes is what the producer estimated; Score is derived from it.
	Stakes  float64
	State   string
	Channel string
	Level   string
	Reason  string
	// Params is the JSON object the copy templates read.
	Params    string
	CreatedAt int64
	DueAt     int64
	HoldUntil int64
	SentAt    int64
	ExpiresAt int64
	UpdatedAt int64
}

// EngagementReceiptRow records what happened to a delivery.
type EngagementReceiptRow struct {
	ID       int64
	MomentID string
	Channel  string
	State    string
	Action   string
	At       int64
}

// EngagementPrefRow holds one kind's settings. Weight below zero and Enabled
// below zero mean "use the default".
type EngagementPrefRow struct {
	Kind       string
	Weight     float64
	Enabled    int
	MutedUntil int64
	UpdatedAt  int64
}

// EngagementKindStats summarizes deliveries of one kind for fatigue and
// auto-pause decisions.
type EngagementKindStats struct {
	Kind             string
	Created          int
	Scheduled        int
	Sent             int
	Delivered        int
	Opened           int
	Acted            int
	Dismissed        int
	Muted            int
	DismissedOrMuted int
	OpenedOrActed    int
	// Receipt buckets are independent of the current per-kind delivery mode.
	// A notice reached on both channels contributes once to each bucket.
	NotificationSent          int
	NotificationOpenedOrActed int
	NotificationActed         int
	NotificationUseful        int
	QuietSent                 int
	QuietOpenedOrActed        int
	// LatestActivityThread selects the non-private generic copy variant of the latest send.
	LatestActivityThread bool
	// Channels contains receipt channel identifiers for sends/deliveries in the window.
	Channels []string
}

// ErrEngagementMomentOpen is returned by CreateEngagementMoment when an open
// moment with the same dedupe key already exists.
var ErrEngagementMomentOpen = errors.New("an open moment with this dedupe key already exists")

// CreateEngagementMoment inserts a pending moment. The ID is generated when
// empty; CreatedAt defaults to now.
func (s *Store) CreateEngagementMoment(ctx context.Context, r EngagementMomentRow) (EngagementMomentRow, error) {
	r.Kind = strings.TrimSpace(r.Kind)
	r.DedupeKey = strings.TrimSpace(r.DedupeKey)
	if r.Kind == "" || r.DedupeKey == "" {
		return r, errors.New("engagement moment needs a kind and a dedupe key")
	}
	if r.ID == "" {
		r.ID = uuid.NewString()
	}
	now := time.Now().UnixMilli()
	if r.CreatedAt == 0 {
		r.CreatedAt = now
	}
	r.UpdatedAt = now
	if r.State == "" {
		r.State = EngagementStatePending
	}
	if r.State == EngagementStateSent && r.SentAt == 0 {
		r.SentAt = r.CreatedAt
	}
	if r.Params == "" {
		r.Params = "{}"
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return r, err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `
		INSERT INTO engagement_moments
			(id, kind, subject_id, dedupe_key, score, stakes, state, channel, level, reason, params,
			 created_at, due_at, hold_until, sent_at, expires_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, r.ID, r.Kind, r.SubjectID, r.DedupeKey, r.Score, r.Stakes, r.State, r.Channel, r.Level, r.Reason, r.Params,
		r.CreatedAt, r.DueAt, r.HoldUntil, r.SentAt, r.ExpiresAt, r.UpdatedAt)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return r, ErrEngagementMomentOpen
		}
		return r, err
	}
	if err := insertEngagementReceipt(ctx, tx, EngagementReceiptRow{
		MomentID: r.ID, State: EngagementReceiptCreated, At: r.CreatedAt,
	}); err != nil {
		return r, err
	}
	if err := recordEngagementLifecycle(ctx, tx, r.ID, r.State, r.Channel, r.SentAt, r.UpdatedAt); err != nil {
		return r, err
	}
	if err := recordEngagementPacing(ctx, tx, r.ID, r.State, r.Channel, r.Level, r.SentAt); err != nil {
		return r, err
	}
	return r, tx.Commit()
}

// GetEngagementMoment returns one moment, or nil when it does not exist.
func (s *Store) GetEngagementMoment(ctx context.Context, id string) (*EngagementMomentRow, error) {
	rows, err := s.queryEngagementMoments(ctx, "WHERE id = ?", 1, id)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	return &rows[0], nil
}

// ForgetEngagementMomentsOfKind blanks the words of every moment of one
// kind and suppresses the ones not yet delivered, for a source whose reading
// stopped: its moments quote what the source saw. The rows stay, without
// their words, so EngagementMomentKeyExists still knows the signals were
// announced and reading again later does not announce them a second time.
func (s *Store) ForgetEngagementMomentsOfKind(ctx context.Context, kind, reason string) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		UPDATE engagement_moments
		SET params = '{}',
			reason = CASE WHEN state IN (?, ?) OR reason = 'chat report' THEN ? ELSE reason END,
			state = CASE WHEN state IN (?, ?) THEN ? ELSE state END,
			updated_at = ?
		WHERE kind = ? AND (params != '{}' OR state IN (?, ?) OR reason = 'chat report')
	`, EngagementStatePending, EngagementStateScheduled, reason,
		EngagementStatePending, EngagementStateScheduled, EngagementStateSuppressed,
		time.Now().UnixMilli(), kind, EngagementStatePending, EngagementStateScheduled)
	if err != nil {
		return 0, fmt.Errorf("forget engagement moments of a kind: %w", err)
	}
	return res.RowsAffected()
}

// OpenEngagementMomentsOfKind returns the pending and scheduled moments of
// one kind, oldest first.
func (s *Store) OpenEngagementMomentsOfKind(ctx context.Context, kind string) ([]EngagementMomentRow, error) {
	return s.queryEngagementMoments(ctx, "WHERE kind = ? AND state IN (?, ?)", -1,
		kind, EngagementStatePending, EngagementStateScheduled)
}

// EngagementMomentKeyExists reports whether a moment with this dedupe key was
// ever recorded, in any state. The partial unique index only covers open
// moments, so producers use this to avoid announcing a signal again after its
// moment was sent, suppressed or expired.
func (s *Store) EngagementMomentKeyExists(ctx context.Context, dedupeKey string) (bool, error) {
	var one int
	err := s.db.QueryRowContext(ctx,
		`SELECT 1 FROM engagement_moments WHERE dedupe_key = ? LIMIT 1`,
		strings.TrimSpace(dedupeKey)).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// GetOpenEngagementMomentByKey returns the pending or scheduled moment with
// the dedupe key, or nil when none is open.
func (s *Store) GetOpenEngagementMomentByKey(ctx context.Context, dedupeKey string) (*EngagementMomentRow, error) {
	rows, err := s.queryEngagementMoments(ctx, "WHERE dedupe_key = ? AND state IN (?, ?)", 1,
		dedupeKey, EngagementStatePending, EngagementStateScheduled)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	return &rows[0], nil
}

// SentEngagementMomentsSince returns moments sent at or after the given
// time, oldest first. SentAt is the first measured send, including chat;
// pacing restoration uses EngagementPacingBetween instead.
func (s *Store) SentEngagementMomentsSince(ctx context.Context, since time.Time, limit int) ([]EngagementMomentRow, error) {
	if limit <= 0 {
		limit = 1000
	}
	return s.queryEngagementMoments(ctx, "WHERE state = ? AND sent_at >= ?", limit, EngagementStateSent, since.UnixMilli())
}

// ListEngagementMoments returns moments in a state, oldest first, up to limit.
// A zero or negative limit returns up to 200 rows.
func (s *Store) ListEngagementMoments(ctx context.Context, state string, limit int) ([]EngagementMomentRow, error) {
	if limit <= 0 {
		limit = 200
	}
	return s.queryEngagementMoments(ctx, "WHERE state = ?", limit, state)
}

// DueEngagementMoments returns pending moments plus scheduled moments whose
// hold has ended by now, plus sent moments awaiting a durable chat report.
func (s *Store) DueEngagementMoments(ctx context.Context, now time.Time, limit int) ([]EngagementMomentRow, error) {
	if limit <= 0 {
		limit = 200
	}
	return s.queryEngagementMoments(ctx,
		"WHERE state = ? OR ((state = ? OR (state = ? AND reason = 'chat report')) AND hold_until <= ?)", limit,
		EngagementStatePending, EngagementStateScheduled, EngagementStateSent, now.UnixMilli())
}

// LastSentEngagementMoments returns, for every dedupe key, the most recent
// measured send time, limited to deliveries since the given time. Pacing
// restoration uses EngagementPacingBetween instead.
func (s *Store) LastSentEngagementMoments(ctx context.Context, since time.Time) (map[string]time.Time, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT dedupe_key, MAX(sent_at) FROM engagement_moments
		WHERE state = ? AND sent_at >= ?
		GROUP BY dedupe_key
	`, EngagementStateSent, since.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]time.Time{}
	for rows.Next() {
		var key string
		var sentAt int64
		if err := rows.Scan(&key, &sentAt); err != nil {
			return nil, err
		}
		out[key] = time.UnixMilli(sentAt)
	}
	return out, rows.Err()
}

// SetEngagementMomentState moves a moment to a state and records the reason.
// Sent moments also get their channel, level and sent time; scheduled moments
// their hold time. Earlier delivery evidence repairs sent_at after a delayed
// write, while later retries never restart the completion window.
func (s *Store) SetEngagementMomentState(ctx context.Context, id, state, reason string, upd EngagementMomentRow) error {
	now := upd.UpdatedAt
	if now == 0 {
		now = time.Now().UnixMilli()
	}
	receiptSentAt := upd.SentAt
	if state == EngagementStateSent && upd.SentAt == 0 {
		upd.SentAt = now
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `
		UPDATE engagement_moments
		SET state = ?, reason = ?, score = CASE WHEN ? > 0 THEN ? ELSE score END,
			channel = CASE WHEN ? != '' THEN ? ELSE channel END,
			level = CASE WHEN ? != '' THEN ? ELSE level END,
			hold_until = CASE WHEN ? > 0 THEN ? ELSE hold_until END,
			sent_at = CASE WHEN ? > 0 AND (sent_at = 0 OR ? < sent_at) THEN ? ELSE sent_at END,
			updated_at = ?
		WHERE id = ?
	`, state, reason, upd.Score, upd.Score, upd.Channel, upd.Channel, upd.Level, upd.Level,
		upd.HoldUntil, upd.HoldUntil, upd.SentAt, upd.SentAt, upd.SentAt, now, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	var channel string
	var sentAt int64
	if err := tx.QueryRowContext(ctx, `SELECT channel, sent_at FROM engagement_moments WHERE id = ?`, id).Scan(&channel, &sentAt); err != nil {
		return err
	}
	if receiptSentAt == 0 {
		receiptSentAt = sentAt
	}
	if err := recordEngagementLifecycle(ctx, tx, id, state, channel, receiptSentAt, now); err != nil {
		return err
	}
	if err := recordEngagementPacing(ctx, tx, id, state, channel, upd.Level, upd.SentAt); err != nil {
		return err
	}
	return tx.Commit()
}

// SetEngagementMomentParams replaces a moment's params JSON.
func (s *Store) SetEngagementMomentParams(ctx context.Context, id, params string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE engagement_moments SET params = ?, updated_at = ? WHERE id = ?`,
		params, time.Now().UnixMilli(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// ExpireEngagementMoments marks open moments whose expiry passed as expired
// and returns how many changed.
func (s *Store) ExpireEngagementMoments(ctx context.Context, now time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		UPDATE engagement_moments SET state = ?, reason = 'expired', updated_at = ?
		WHERE state IN (?, ?) AND expires_at > 0 AND expires_at <= ?
			AND NOT (reason = 'chat report' AND channel != '')
	`, EngagementStateExpired, now.UnixMilli(), EngagementStatePending, EngagementStateScheduled, now.UnixMilli())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// RecordEngagementReceipt stores an observation once per moment, channel and
// state. Acted also retains its action so a completion marker can coexist with
// legacy interactions. Replays never rewrite the first observation, and
// out-of-order delivery of receipts keeps every state.
func (s *Store) RecordEngagementReceipt(ctx context.Context, r EngagementReceiptRow) error {
	if r.MomentID == "" {
		return errors.New("engagement receipt needs a moment id")
	}
	switch r.State {
	case EngagementReceiptCreated, EngagementReceiptScheduled, EngagementReceiptSent,
		EngagementReceiptDelivered, EngagementReceiptOpened, EngagementReceiptActed,
		EngagementReceiptDismissed, EngagementReceiptMuted:
	default:
		return errors.New("unknown engagement receipt state")
	}
	if r.At == 0 {
		r.At = time.Now().UnixMilli()
	}
	var exists int
	if err := s.db.QueryRowContext(ctx, `SELECT 1 FROM engagement_moments WHERE id = ?`, r.MomentID).Scan(&exists); err != nil {
		return err
	}
	return insertEngagementReceipt(ctx, s.db, r)
}

type engagementReceiptWriter interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func insertEngagementReceipt(ctx context.Context, db engagementReceiptWriter, r EngagementReceiptRow) error {
	_, err := db.ExecContext(ctx, `
		INSERT OR IGNORE INTO engagement_receipts (moment_id, channel, state, action, at)
		SELECT ?, ?, ?, ?, ? WHERE NOT EXISTS (
			SELECT 1 FROM engagement_receipts WHERE moment_id = ? AND channel = ? AND state = ?
				AND (state != 'acted' OR action = ?)
		)
	`, r.MomentID, r.Channel, r.State, r.Action, r.At, r.MomentID, r.Channel, r.State, r.Action)
	return err
}

func recordEngagementLifecycle(ctx context.Context, db engagementReceiptWriter, id, state, channel string, sentAt, at int64) error {
	if state == EngagementStateScheduled {
		if err := insertEngagementReceipt(ctx, db, EngagementReceiptRow{MomentID: id, State: EngagementReceiptScheduled, At: at}); err != nil {
			return err
		}
	}
	if sentAt > 0 {
		for _, sentChannel := range strings.Split(channel, "+") {
			if err := insertEngagementReceipt(ctx, db, EngagementReceiptRow{
				MomentID: id, Channel: sentChannel, State: EngagementReceiptSent, At: sentAt,
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

// RecordEngagementChatSent records durable chat delivery even when a moment
// is still waiting for an external channel. Routing and pacing state stay
// unchanged; the earliest observed send anchors attribution across channels.
func (s *Store) RecordEngagementChatSent(ctx context.Context, momentID string, at time.Time) error {
	if at.IsZero() {
		at = time.Now()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE engagement_moments
		SET sent_at = CASE WHEN sent_at = 0 OR ? < sent_at THEN ? ELSE sent_at END WHERE id = ?`, at.UnixMilli(), at.UnixMilli(), momentID)
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil || n == 0 {
		if err != nil {
			return err
		}
		return sql.ErrNoRows
	}
	if err := insertEngagementReceipt(ctx, tx, EngagementReceiptRow{
		MomentID: momentID, Channel: "chat", State: EngagementReceiptSent, At: at.UnixMilli(),
	}); err != nil {
		return err
	}
	return tx.Commit()
}

// ListEngagementReceipts returns a moment's receipts in time order.
func (s *Store) ListEngagementReceipts(ctx context.Context, momentID string) ([]EngagementReceiptRow, error) {
	rows, err := s.committedReader().QueryContext(ctx, `
		SELECT id, moment_id, channel, state, action, at FROM engagement_receipts
		WHERE moment_id = ? ORDER BY at, id
	`, momentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EngagementReceiptRow
	for rows.Next() {
		var r EngagementReceiptRow
		if err := rows.Scan(&r.ID, &r.MomentID, &r.Channel, &r.State, &r.Action, &r.At); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// EngagementStatsSince summarizes lifecycle events and sent-cohort outcomes
// since the given time. Use EngagementStatsBetween for a bounded snapshot.
func (s *Store) EngagementStatsSince(ctx context.Context, since time.Time) (map[string]EngagementKindStats, error) {
	return s.engagementStatsBetween(ctx, since.UnixMilli(), 1<<63-1)
}

// EngagementStatsBetween counts creations and schedules in the inclusive
// window, plus sends in that window and their receipts through until. Receipt
// outcomes before the send do not belong to that delivery. Each state counts
// a moment once, across channels and actions; dismissal plus mute is a union.
// Only internally attributed completions count as acted. Historical acted
// receipts from buttons remain observations of opening or interacting.
func (s *Store) EngagementStatsBetween(ctx context.Context, since, until time.Time) (map[string]EngagementKindStats, error) {
	return s.engagementStatsBetween(ctx, since.UnixMilli(), until.UnixMilli())
}

func (s *Store) engagementStatsBetween(ctx context.Context, since, until int64) (map[string]EngagementKindStats, error) {
	rows, err := s.db.QueryContext(ctx, `
		WITH bounds(since, until) AS (VALUES (?, ?)), events AS (
			SELECT m.kind, m.id, r.channel,
				r.state AS original_state,
				CASE WHEN r.state = 'acted' AND (r.channel != ? OR r.action != ?)
					THEN 'opened' ELSE r.state END AS state,
				m.created_at BETWEEN b.since AND b.until AS created,
				r.state = 'scheduled' AND r.at BETWEEN b.since AND b.until AS scheduled,
				m.sent_at > 0 AND m.sent_at BETWEEN b.since AND b.until AS sent,
				r.at >= m.sent_at AND r.at <= b.until AS received,
				MAX(CASE WHEN r.state IN ('sent', 'delivered') AND r.channel IN ('desktop', 'push', 'text', 'email')
					AND r.at >= m.sent_at AND r.at <= b.until THEN 1 ELSE 0 END) OVER (PARTITION BY m.id) AS notified,
				MAX(CASE WHEN r.state IN ('sent', 'delivered') AND r.channel = 'today_quiet'
					AND r.at >= m.sent_at AND r.at <= b.until THEN 1 ELSE 0 END) OVER (PARTITION BY m.id) AS quiet
			FROM engagement_moments m CROSS JOIN bounds b
			LEFT JOIN engagement_receipts r ON r.moment_id = m.id
			WHERE m.created_at BETWEEN b.since AND b.until
				OR m.sent_at BETWEEN b.since AND b.until
				OR (r.state = 'scheduled' AND r.at BETWEEN b.since AND b.until)
		)
		SELECT kind,
			COUNT(DISTINCT CASE WHEN created THEN id END),
			COUNT(DISTINCT CASE WHEN scheduled THEN id END),
			COUNT(DISTINCT CASE WHEN sent THEN id END),
			COUNT(DISTINCT CASE WHEN sent AND received AND state = 'delivered' THEN id END),
			COUNT(DISTINCT CASE WHEN sent AND received AND state = 'opened' THEN id END),
			COUNT(DISTINCT CASE WHEN sent AND received AND state = 'acted' THEN id END),
			COUNT(DISTINCT CASE WHEN sent AND received AND state = 'dismissed' THEN id END),
			COUNT(DISTINCT CASE WHEN sent AND received AND state = 'muted' THEN id END),
			COUNT(DISTINCT CASE WHEN sent AND received AND state IN ('dismissed', 'muted') THEN id END),
			COUNT(DISTINCT CASE WHEN sent AND received AND state IN ('opened', 'acted') THEN id END),
			COUNT(DISTINCT CASE WHEN sent AND notified THEN id END),
			COUNT(DISTINCT CASE WHEN sent AND notified AND received AND state IN ('opened', 'acted') THEN id END),
			COUNT(DISTINCT CASE WHEN sent AND notified AND received AND state = 'acted' THEN id END),
			COUNT(DISTINCT CASE WHEN sent AND notified AND received AND
				CASE WHEN kind IN ('brief_ready', 'meeting_ended', 'weekly_recap')
					THEN state = 'opened' ELSE original_state = 'acted' END THEN id END),
			COUNT(DISTINCT CASE WHEN sent AND quiet THEN id END),
			COUNT(DISTINCT CASE WHEN sent AND quiet AND received AND state IN ('opened', 'acted') THEN id END),
			COALESCE(GROUP_CONCAT(DISTINCT CASE WHEN sent AND received AND state IN ('sent', 'delivered') THEN channel END), ''),
			COALESCE((SELECT CASE WHEN latest.kind = 'decision_due' AND json_valid(latest.params)
				THEN COALESCE(json_extract(latest.params, '$._activity_thread'), '') != '' ELSE 0 END
				FROM engagement_moments latest
				WHERE latest.kind = events.kind AND latest.sent_at > 0 AND latest.sent_at BETWEEN ? AND ?
				ORDER BY latest.sent_at DESC, latest.id DESC LIMIT 1), 0)
		FROM events GROUP BY kind
	`, since, until, EngagementCompletionChannel, EngagementCompletionAction, since, until)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]EngagementKindStats{}
	for rows.Next() {
		var stat EngagementKindStats
		var channels string
		if err := rows.Scan(&stat.Kind, &stat.Created, &stat.Scheduled, &stat.Sent, &stat.Delivered,
			&stat.Opened, &stat.Acted, &stat.Dismissed, &stat.Muted, &stat.DismissedOrMuted, &stat.OpenedOrActed,
			&stat.NotificationSent, &stat.NotificationOpenedOrActed, &stat.NotificationActed, &stat.NotificationUseful, &stat.QuietSent, &stat.QuietOpenedOrActed, &channels, &stat.LatestActivityThread); err != nil {
			return nil, err
		}
		stat.Channels = []string{}
		if channels != "" {
			stat.Channels = strings.Split(channels, ",")
			sort.Strings(stat.Channels)
		}
		out[stat.Kind] = stat
	}
	return out, rows.Err()
}

// ListEngagementPrefs returns every stored preference.
func (s *Store) ListEngagementPrefs(ctx context.Context) ([]EngagementPrefRow, error) {
	rows, err := s.committedReader().QueryContext(ctx, `
		SELECT kind, weight, enabled, muted_until, updated_at FROM engagement_prefs ORDER BY kind
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EngagementPrefRow
	for rows.Next() {
		var r EngagementPrefRow
		if err := rows.Scan(&r.Kind, &r.Weight, &r.Enabled, &r.MutedUntil, &r.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// UpsertEngagementPref stores one kind's settings.
func (s *Store) UpsertEngagementPref(ctx context.Context, r EngagementPrefRow) error {
	r.Kind = strings.TrimSpace(r.Kind)
	if r.Kind == "" {
		return errors.New("engagement pref needs a kind")
	}
	if r.UpdatedAt == 0 {
		r.UpdatedAt = time.Now().UnixMilli()
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO engagement_prefs (kind, weight, enabled, muted_until, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(kind) DO UPDATE SET
			weight = excluded.weight, enabled = excluded.enabled,
			muted_until = excluded.muted_until, updated_at = excluded.updated_at
	`, r.Kind, r.Weight, r.Enabled, r.MutedUntil, r.UpdatedAt)
	return err
}

func (s *Store) queryEngagementMoments(ctx context.Context, where string, limit int, args ...any) ([]EngagementMomentRow, error) {
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, kind, subject_id, dedupe_key, score, stakes, state, channel, level, reason, params,
			created_at, due_at, hold_until, sent_at, expires_at, updated_at
		FROM engagement_moments `+where+`
		ORDER BY created_at, id
		LIMIT ?
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EngagementMomentRow
	for rows.Next() {
		var r EngagementMomentRow
		if err := rows.Scan(&r.ID, &r.Kind, &r.SubjectID, &r.DedupeKey, &r.Score, &r.Stakes, &r.State, &r.Channel, &r.Level,
			&r.Reason, &r.Params, &r.CreatedAt, &r.DueAt, &r.HoldUntil, &r.SentAt, &r.ExpiresAt, &r.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// EngagementRulesRow is the user's global pacing rules: the quiet window as
// minutes from local midnight, the daily interruption budget and the digest
// times as minutes from local midnight. It is one JSON value in ui_state,
// absent until the user changes a rule. DigestMinutes is nil in rows written
// before the intensity dial existed; readers keep the defaults then. An empty
// list means no digests and is written as [], never dropped.
type EngagementRulesRow struct {
	QuietStartMinutes    int   `json:"quietStartMinutes"`
	QuietEndMinutes      int   `json:"quietEndMinutes"`
	DailyInterruptBudget int   `json:"dailyInterruptBudget"`
	DigestMinutes        []int `json:"digestMinutes"`
}

// EngagementWeekCounts is the weekly line on Today: how many moments went out
// and how many linked items completed.
type EngagementWeekCounts struct {
	Sent    int
	ActedOn int
}

// EngagementCountsSince counts moments sent since the given time and, among
// them, the distinct moments with an attributed completion after the send.
func (s *Store) EngagementCountsSince(ctx context.Context, since time.Time) (EngagementWeekCounts, error) {
	var out EngagementWeekCounts
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(DISTINCT m.id),
			COUNT(DISTINCT CASE WHEN r.state = ? AND r.channel = ? AND r.action = ?
				AND r.at >= m.sent_at THEN m.id END)
		FROM engagement_moments m LEFT JOIN engagement_receipts r ON r.moment_id = m.id
		WHERE m.sent_at > 0 AND m.sent_at >= ?
	`, EngagementReceiptActed, EngagementCompletionChannel, EngagementCompletionAction, since.UnixMilli()).Scan(&out.Sent, &out.ActedOn)
	return out, err
}

const engagementRulesKey = "engagement_rules"

// GetEngagementRules returns the stored rules, or nil when none were saved.
func (s *Store) GetEngagementRules(ctx context.Context) (*EngagementRulesRow, error) {
	raw, err := s.GetUIState(ctx, engagementRulesKey)
	if err != nil || raw == "" {
		return nil, err
	}
	var row EngagementRulesRow
	if err := json.Unmarshal([]byte(raw), &row); err != nil {
		return nil, err
	}
	return &row, nil
}

// SetEngagementRules stores the rules. A nil DigestMinutes is written as an
// empty list, so a stored row always says what the digests are.
func (s *Store) SetEngagementRules(ctx context.Context, row EngagementRulesRow) error {
	if row.DigestMinutes == nil {
		row.DigestMinutes = []int{}
	}
	raw, err := json.Marshal(row)
	if err != nil {
		return err
	}
	return s.SetUIState(ctx, engagementRulesKey, string(raw))
}
