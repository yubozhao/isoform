// Package engagement decides what the user hears about, when, and where.
//
// Producers in the desktop app turn observations, memory, the Purpose Map and
// work transitions into Moments. This package scores each Moment, applies the
// pacing and quiet rules, and picks a channel. It has no dependency on the
// app, the store or any transport, so every rule is a table test.
package engagement

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Kind names one reason to reach the user. The set is fixed by the
// notification design spec; adding one means adding a spec entry and copy.
type Kind string

const (
	KindCommitmentSlipping Kind = "commitment_slipping"
	KindMeetingEnded       Kind = "meeting_ended"
	KindPersonReappeared   Kind = "person_reappeared"
	KindGoalCheckIn        Kind = "goal_check_in"
	KindWorkFinished       Kind = "work_finished"
	KindWorkBlocked        Kind = "work_blocked"
	KindDecisionDue        Kind = "decision_due"
	KindBriefReady         Kind = "brief_ready"
	KindBriefMissed        Kind = "brief_missed"
	// A schedule on Upcoming failed or was missed twice in a row.
	KindScheduleMissed   Kind = "schedule_missed"
	KindSuggestion       Kind = "suggestion"
	KindLearnedToConfirm Kind = "learned_to_confirm"
	KindCapturePaused    Kind = "capture_paused"
	// The day ahead: a meeting about to start, two events that clash or an
	// invite still waiting, and the follow-ups a meeting left the user with.
	KindMeetingPrep      Kind = "meeting_prep"
	KindCalendarConflict Kind = "calendar_conflict"
	KindMeetingFollowup  Kind = "meeting_followup"
	// Mail: someone the user knows wrote to them directly more than a day
	// ago and has no answer yet.
	KindWaitingOnReply Kind = "waiting_on_reply"
	// New finds on the topics the user has been looking at, from the daily
	// search after the morning brief.
	KindTopicFinds Kind = "topic_finds"
	// Playbooks: Isoform noticed a routine and offers to do it, and a playbook
	// run finished (or failed) with its result, sent where the user chose.
	KindPlaybookSuggested Kind = "playbook_suggested"
	KindPlaybookResult    Kind = "playbook_result"
	KindAnswerReady       Kind = "answer_ready"
	KindFirstWeek         Kind = "first_week"
	KindWeeklyRecap       Kind = "weekly_recap"
	// A reminder the user set on a moment in Activity, delivered at the time
	// they chose to the computer and the phone.
	KindReminder Kind = "reminder"
)

// AllKinds lists every kind in display order.
func AllKinds() []Kind {
	return []Kind{
		KindFirstWeek,
		KindWeeklyRecap,
		KindWorkBlocked,
		KindDecisionDue,
		KindWorkFinished,
		KindPlaybookResult,
		KindAnswerReady,
		KindReminder,
		KindCommitmentSlipping,
		KindMeetingPrep,
		KindCapturePaused,
		KindMeetingEnded,
		KindMeetingFollowup,
		KindBriefReady,
		KindCalendarConflict,
		KindWaitingOnReply,
		KindBriefMissed,
		KindScheduleMissed,
		KindSuggestion,
		KindTopicFinds,
		KindPlaybookSuggested,
		KindGoalCheckIn,
		KindPersonReappeared,
		KindLearnedToConfirm,
	}
}

// Interruption is the APNs interruption level a delivery asks for.
type Interruption string

const (
	InterruptionPassive       Interruption = "passive"
	InterruptionActive        Interruption = "active"
	InterruptionTimeSensitive Interruption = "time-sensitive"
)

// Action is one button a delivered moment offers. Label is a copy key the app
// resolves in the user's language; Authenticated marks actions the phone must
// gate behind the owner check.
type Action struct {
	ID            string
	Label         string
	Authenticated bool
}

// Moment is one candidate reason to reach the user. It carries identifiers and
// parameters for copy, never raw observed content.
type Moment struct {
	ID        string
	Kind      Kind
	SubjectID string
	// DedupeKey collapses repeats of the same situation; see DedupeKey.
	DedupeKey string
	CreatedAt time.Time
	// DueAt is when the situation stops mattering or a deadline passes. Zero
	// means no particular time.
	DueAt time.Time
	// HardDeadline marks DueAt as a real deadline (an approval window closing,
	// a blocked task with a due date) rather than a soft horizon.
	HardDeadline bool
	// ExpiresAt is when the moment is dropped unsent. Zero means never.
	ExpiresAt time.Time
	// Stakes is the producer's estimate in [0, 1] of what is lost by missing
	// this. Zero means unknown and scores as 0.5.
	Stakes float64
	// Params feed the copy templates (a person's name, a count). The sealed
	// text built from them travels end to end encrypted; the push body never
	// includes them.
	Params map[string]string
	// ReadReceipt is code-owned delivery metadata, never template prose.
	ReadReceipt string `json:"-"`
	// NotBefore holds the moment until this time (a reminder the user set).
	// Zero means it is paced as soon as it is submitted.
	NotBefore time.Time
	// Channels are the channels the user chose for this moment (a playbook's
	// result). When set, the moment goes to each of them that can be reached
	// instead of the one channel the router would pick.
	Channels []Channel
}

// Wants reports whether the user chose the channel for this moment.
func (m Moment) Wants(channel Channel) bool {
	for _, c := range m.Channels {
		if c == channel {
			return true
		}
	}
	return false
}

// KindSpec is the fixed policy for one kind.
type KindSpec struct {
	Kind Kind
	// Weight is the default importance in [0, 1] before user preferences.
	Weight float64
	// DefaultOn is whether a new user hears about this kind at all.
	DefaultOn bool
	// Digestable kinds wait for the next digest when they are passive.
	Digestable bool
	// Cooldown is the minimum gap between two deliveries with the same
	// dedupe key. Zero means a key is delivered at most once.
	Cooldown time.Duration
	// DailyCap limits deliveries of this kind per local day; zero is no cap.
	DailyCap int
	// Requested kinds are deliveries the user asked for (a playbook's
	// result): the daily interruption budget never holds them back.
	Requested bool
	// Direct kinds are deliveries the user set up one by one (a reminder):
	// a per-kind mute and the dismiss-or-mute auto-pause never stop them.
	Direct  bool
	Actions []Action
}

// BriefRequestParam marks a brief_ready moment whose brief the person asked for
// with Make my brief now. Its value is BriefRequestManual.
const (
	BriefRequestParam  = "brief_request"
	BriefRequestManual = "manual"
)

// Interruption returns the level a moment of this kind asks for right now.
func (s KindSpec) Interruption(m Moment, now time.Time) Interruption {
	switch s.Kind {
	case KindWorkBlocked, KindDecisionDue:
		if m.HardDeadline && !m.DueAt.IsZero() {
			return InterruptionTimeSensitive
		}
		return InterruptionActive
	case KindReminder:
		// The user chose this time, so it may break quiet hours.
		return InterruptionTimeSensitive
	case KindAnswerReady:
		if m.HardDeadline {
			return InterruptionTimeSensitive
		}
		return InterruptionActive
	case KindCapturePaused:
		if !m.CreatedAt.IsZero() && now.Sub(m.CreatedAt) >= time.Hour {
			return InterruptionTimeSensitive
		}
		return InterruptionActive
	case KindMeetingPrep:
		// Prep is Time Sensitive on both devices (notify v1.0.0 rule 11, B4):
		// it passes a Focus only where the person allows that for Isoform.
		return InterruptionTimeSensitive
	case KindCommitmentSlipping, KindWorkFinished, KindPlaybookResult, KindFirstWeek, KindWeeklyRecap:
		return InterruptionActive
	case KindBriefReady:
		// A scheduled brief is the notification people act on, so it uses the
		// normal sound and buzz. A brief the person asked for is already in
		// front of them and arrives without one (ST86).
		if m.Params[BriefRequestParam] == BriefRequestManual {
			return InterruptionPassive
		}
		return InterruptionActive
	case KindMeetingEnded, KindPersonReappeared, KindGoalCheckIn, KindLearnedToConfirm, KindCalendarConflict, KindMeetingFollowup, KindBriefMissed, KindScheduleMissed, KindSuggestion, KindWaitingOnReply, KindTopicFinds, KindPlaybookSuggested:
		return InterruptionPassive
	default:
		return InterruptionPassive
	}
}

var specs = map[Kind]KindSpec{
	KindFirstWeek: {Kind: KindFirstWeek, Weight: 0.4, DefaultOn: true, DailyCap: 1,
		Actions: []Action{{ID: "open", Label: "open"}}},
	KindWeeklyRecap: {Kind: KindWeeklyRecap, Weight: 0.4, DefaultOn: true, DailyCap: 1,
		Actions: []Action{{ID: "open", Label: "open"}}},
	KindDecisionDue: {Kind: KindDecisionDue, Weight: 0.95, DefaultOn: true,
		Actions: []Action{{ID: "approve", Label: "approve", Authenticated: true}, {ID: "decline", Label: "decline", Authenticated: true}}},
	KindCommitmentSlipping: {Kind: KindCommitmentSlipping, Weight: 0.8, DefaultOn: true, Cooldown: 24 * time.Hour,
		Actions: []Action{{ID: "open", Label: "open"}}},
	KindMeetingEnded: {Kind: KindMeetingEnded, Weight: 0.5, DefaultOn: true, Digestable: true,
		Actions: []Action{{ID: "open_notes", Label: "open_notes"}, {ID: "later", Label: "later"}}},
	KindPersonReappeared: {Kind: KindPersonReappeared, Weight: 0.3, DefaultOn: false, Digestable: true, Cooldown: 14 * 24 * time.Hour,
		Actions: []Action{{ID: "open_person", Label: "open_person"}, {ID: "snooze", Label: "snooze"}, {ID: "stop_person", Label: "stop_person"}}},
	KindGoalCheckIn: {Kind: KindGoalCheckIn, Weight: 0.35, DefaultOn: false, Digestable: true, Cooldown: 7 * 24 * time.Hour, DailyCap: 1,
		Actions: []Action{{ID: "update", Label: "update"}, {ID: "snooze", Label: "snooze"}, {ID: "dismiss", Label: "dismiss"}}},
	KindWorkFinished: {Kind: KindWorkFinished, Weight: 0.7, DefaultOn: true,
		Actions: []Action{{ID: "review", Label: "review"}}},
	KindWorkBlocked: {Kind: KindWorkBlocked, Weight: 0.95, DefaultOn: true,
		Actions: []Action{{ID: "answer", Label: "answer"}, {ID: "stop", Label: "stop", Authenticated: true}, {ID: "later", Label: "later"}}},
	KindBriefReady: {Kind: KindBriefReady, Weight: 0.4, DefaultOn: true, Digestable: false,
		Actions: []Action{{ID: "open", Label: "open"}}},
	// A missed brief is said once per slot: the key names the slot and the
	// zero cooldown means a key is delivered at most once.
	KindBriefMissed: {Kind: KindBriefMissed, Weight: 0.5, DefaultOn: true, Digestable: false,
		Actions: []Action{{ID: "open", Label: "open"}}},
	// A schedule that did not run as planned twice in a row is said once a
	// day: the key names the schedule and the local date. Several can fail
	// together (no model set up), so at most three are delivered a day and
	// they wait for a digest time; the rest stay visible on Upcoming.
	KindScheduleMissed: {Kind: KindScheduleMissed, Weight: 0.5, DefaultOn: true, Digestable: true, DailyCap: 3,
		Actions: []Action{{ID: "run_now", Label: "run_now"}, {ID: "open", Label: "open"}}},
	// One suggestion a week: the producer keys it by ISO week and the
	// cooldown refuses a second one inside seven days.
	KindSuggestion: {Kind: KindSuggestion, Weight: 0.35, DefaultOn: true, Digestable: true, Cooldown: 7 * 24 * time.Hour, DailyCap: 1,
		Actions: []Action{{ID: "see", Label: "see"}, {ID: "not_now", Label: "not_now"}, {ID: "dismiss", Label: "dismiss"}}},
	KindLearnedToConfirm: {Kind: KindLearnedToConfirm, Weight: 0.3, DefaultOn: false, Digestable: true, DailyCap: 2,
		Actions: []Action{{ID: "keep", Label: "keep"}, {ID: "correct", Label: "correct"}, {ID: "not_now", Label: "not_now"}}},
	KindCapturePaused: {Kind: KindCapturePaused, Weight: 0.9, DefaultOn: true,
		Actions: []Action{{ID: "resume_capture", Label: "resume_capture"}}},
	// One per event, fifteen minutes before it; it expires at the start.
	KindMeetingPrep: {Kind: KindMeetingPrep, Weight: 0.75, DefaultOn: true,
		Actions: []Action{{ID: "open", Label: "open"}}},
	// Passive and digestable: a clash or a waiting invite can wait for the digest.
	KindCalendarConflict: {Kind: KindCalendarConflict, Weight: 0.45, DefaultOn: true, Digestable: true, Cooldown: 24 * time.Hour,
		Actions: []Action{{ID: "open", Label: "open"}}},
	// The follow-ups a meeting left the user with, once the notes are ready.
	KindMeetingFollowup: {Kind: KindMeetingFollowup, Weight: 0.6, DefaultOn: true, Digestable: true,
		Actions: []Action{{ID: "open_notes", Label: "open_notes"}, {ID: "later", Label: "later"}}},
	// A mail thread waiting on the user: passive, it can wait for the
	// digest. The producer keys it by thread and the zero cooldown says each
	// thread at most once.
	KindWaitingOnReply: {Kind: KindWaitingOnReply, Weight: 0.5, DefaultOn: true, Digestable: true,
		Actions: []Action{{ID: "open", Label: "open"}, {ID: "not_now", Label: "not_now"}}},
	// New finds on the user's topics: at most one a day. The producer keys it
	// by local date, the cooldown refuses a second one inside twenty hours,
	// and it waits for a digest time like any passive update.
	KindTopicFinds: {Kind: KindTopicFinds, Weight: 0.3, DefaultOn: true, Digestable: true, Cooldown: 20 * time.Hour, DailyCap: 1,
		Actions: []Action{{ID: "open", Label: "open"}, {ID: "not_now", Label: "not_now"}}},
	// A routine Isoform could take on, asked once per playbook (the key names
	// it): passive, it waits for a digest.
	KindPlaybookSuggested: {Kind: KindPlaybookSuggested, Weight: 0.45, DefaultOn: true, Digestable: true, DailyCap: 1,
		Actions: []Action{{ID: "see", Label: "see"}, {ID: "not_now", Label: "not_now"}}},
	// Each run's result once (the key names the run), at once and to every
	// channel the user chose for the playbook; quiet hours still hold it.
	KindPlaybookResult: {Kind: KindPlaybookResult, Weight: 0.8, DefaultOn: true, Requested: true, DailyCap: 20,
		Actions: []Action{{ID: "review", Label: "review"}}},
	// A reminder the user set: at the time they chose, to the computer and
	// the phone. The key names the moment and the time, so each reminder is
	// delivered once.
	KindReminder: {Kind: KindReminder, Weight: 0.9, DefaultOn: true, Requested: true, Direct: true,
		Actions: []Action{{ID: "done", Label: "done"}, {ID: "not_now", Label: "not_now"}}},
	KindAnswerReady: {Kind: KindAnswerReady, Weight: 0.7, DefaultOn: true, Digestable: false,
		Actions: []Action{{ID: "open", Label: "open"}}},
}

// Spec returns the policy for a kind and whether the kind is known.
func Spec(kind Kind) (KindSpec, bool) {
	s, ok := specs[kind]
	return s, ok
}

// ErrKindCannotBeQuiet refuses a quiet-on-Today setting for a kind that needs
// the user's answer: it must still interrupt.
var ErrKindCannotBeQuiet = errors.New("engagement_kind_cannot_be_quiet")

// KindCannotBeQuietError identifies a refused kind without UI copy. Wails
// exposes the stable Error code; Go callers can use errors.As for Kind.
type KindCannotBeQuietError struct {
	Kind Kind `json:"kind"`
}

func (e *KindCannotBeQuietError) Error() string { return ErrKindCannotBeQuiet.Error() }
func (e *KindCannotBeQuietError) Unwrap() error { return ErrKindCannotBeQuiet }

// QuietOnTodayAllowed reports whether a kind may show as a quiet line on Today
// instead of interrupting. Kinds that wait on the user's decision (a decision
// due, a permission request) always interrupt.
func QuietOnTodayAllowed(kind Kind) bool {
	if _, known := Spec(kind); !known {
		return false
	}
	switch kind {
	case KindDecisionDue:
		return false
	default:
		return true
	}
}

// DedupeKey builds the key that collapses repeats: the kind followed by the
// identifying parts, for example work_finished:<handoffId>.
func DedupeKey(kind Kind, parts ...string) string {
	trimmed := make([]string, 0, len(parts)+1)
	trimmed = append(trimmed, string(kind))
	for _, p := range parts {
		trimmed = append(trimmed, strings.TrimSpace(p))
	}
	return strings.Join(trimmed, ":")
}

// HashPart shortens free text (a blocked reason, a title) into a stable key
// part so the key never carries the text itself.
func HashPart(text string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(text)))
	return hex.EncodeToString(sum[:6])
}

// WeekKey returns the ISO week of t in the form 2026-W41, for keys that repeat
// at most once a week.
func WeekKey(t time.Time) string {
	year, week := t.ISOWeek()
	return fmt.Sprintf("%d-W%02d", year, week)
}

// QuietSuggestionAllowed keeps reminders and needs-you kinds out of Isoform's
// quiet suggestions. The user may still choose quiet when QuietOnTodayAllowed.
func QuietSuggestionAllowed(kind Kind) bool {
	switch kind {
	case KindReminder, KindCommitmentSlipping, KindWaitingOnReply, KindWorkBlocked:
		return false
	default:
		return QuietOnTodayAllowed(kind)
	}
}
