package main

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"sync"
	"time"
)

// One quiet check-in on Today for a goal that has gone stale (item 66 A3): a
// goal still tracked, more than 30 days past its due date, with nothing on it
// for 30 days (no ledger event, no new evidence, no linked activity). Today
// asks "Still working on Discovery?" with Keep, Done and Forget, at most one
// goal at a time, never as a notification and never on the phone. Each goal
// is asked about once: shown, it stays for the rest of that day while
// nothing changes, and is not asked about again.
const (
	goalCheckInOverdue = 30 * 24 * time.Hour
	goalCheckInQuiet   = 30 * 24 * time.Hour
	// goalCheckInStateKey holds the goals already asked about, by goal id.
	goalCheckInStateKey = "goal_checkins"
)

// GoalCheckInView is the goal Today asks about.
type GoalCheckInView struct {
	GoalID  string `json:"goalId"`
	EventID string `json:"eventId"`
	Title   string `json:"title"`
	DueAt   string `json:"dueAt"`
}

// goalCheckInRecord is one goal Today asked about: the goal's event then and
// when it was first shown.
type goalCheckInRecord struct {
	EventID string `json:"eventId"`
	ShownAt int64  `json:"shownAt"`
}

// goalCheckInNow is the clock the check-in reads; tests replace it.
var goalCheckInNow = time.Now

// goalCheckInMu makes NoteGoalCheckInShown's read-modify-write of the
// asked-about records one step, so two notes at once never drop a record
// (and ask that goal again).
var goalCheckInMu sync.Mutex

// goalCheckInBeforeWrite runs between that read and its write; tests use it
// to force two notes to overlap.
var goalCheckInBeforeWrite = func() {}

// GetGoalCheckIn returns the goal Today asks about now, or nil.
func (a *App) GetGoalCheckIn() (perfValue *GoalCheckInView, perfErr error) {
	// Playbooks, SOPs and goals are off unless their switch is on
	// (feature_switches.go).
	if !featureOn(featurePlaybooks) {
		return nil, nil
	}
	defer logPageBinding("GetGoalCheckIn", time.Now(), &perfValue)
	if a == nil || a.store == nil || a.activity == nil || a.activity.store == nil {
		return nil, nil
	}
	now := goalCheckInNow()
	// While observation is paused (Closed Eyes, or a pause that has not
	// ended, or a pause log that cannot be read) Today reads nothing it
	// observed, so it asks nothing.
	if a.IsClosedEyesModeEnabled() {
		return nil, nil
	}
	pauses, pauseErr := a.observationPauses()
	if pauseErr != nil || pausedAt(pauses, now) {
		return nil, nil
	}
	details, err := a.userPurposeDetails()
	if err != nil {
		return nil, errGoalMoveUnavailable
	}
	records, err := a.goalCheckInRecords()
	if err != nil {
		return nil, errGoalMoveUnavailable
	}
	// Today shows a goal only as the brief may: not one whose sources are
	// private or whose words repeat a mail subject.
	subjects, mailErr := mobileBriefMailSubjects(a)
	if mailErr != nil {
		return nil, nil
	}
	reader := mobileBriefPurposeReader{app: a, store: a.activity.store, asOf: now, pauses: pauses, pausesReadable: true, cache: map[string]mobileBriefPurposeObservation{}}
	touched := func(detail PurposeOpenLoopDetail) (time.Time, bool) {
		if mobileBriefValueEchoesMail(mobileGoalForeignText(detail), subjects) {
			return time.Time{}, false
		}
		observation := reader.purpose(detail)
		if observation.Sensitive || observation.Unavailable {
			return time.Time{}, false
		}
		return observation.LastTouch, true
	}
	checkIn := goalCheckInFor(details, records, touched, now)
	// A pause that began while the goals were read hides the answer too.
	if checkIn != nil {
		if pauses, err := a.observationPauses(); a.IsClosedEyesModeEnabled() || err != nil || pausedAt(pauses, goalCheckInNow()) {
			return nil, nil
		}
	}
	return checkIn, nil
}

// NoteGoalCheckInShown records that Today showed the check-in for a goal.
func (a *App) NoteGoalCheckInShown(goalID, eventID string) error {
	// Playbooks, SOPs and goals are off unless their switch is on
	// (feature_switches.go).
	if !featureOn(featurePlaybooks) {
		return nil
	}
	if a == nil || a.store == nil || !mobileValidID(goalID) {
		return errGoalMoveInvalid
	}
	goalCheckInMu.Lock()
	defer goalCheckInMu.Unlock()
	records, err := a.goalCheckInRecords()
	if err != nil {
		return errGoalMoveUnavailable
	}
	if _, asked := records[goalID]; asked {
		return nil
	}
	goalCheckInBeforeWrite()
	records[goalID] = goalCheckInRecord{EventID: eventID, ShownAt: goalCheckInNow().UnixMilli()}
	raw, err := json.Marshal(records)
	if err != nil {
		return err
	}
	if err := a.store.SetUIState(context.Background(), goalCheckInStateKey, string(raw)); err != nil {
		return errGoalMoveUnavailable
	}
	return nil
}

// goalCheckInRecords reads the asked-about records. One read is atomic; a
// writer holds goalCheckInMu across its read and write.
func (a *App) goalCheckInRecords() (map[string]goalCheckInRecord, error) {
	raw, err := a.store.GetUIState(context.Background(), goalCheckInStateKey)
	if err != nil {
		return nil, err
	}
	records := map[string]goalCheckInRecord{}
	if raw == "" {
		return records, nil
	}
	if err := json.Unmarshal([]byte(raw), &records); err != nil {
		return nil, errors.New("goal check-ins are unreadable")
	}
	return records, nil
}

// goalCheckInFor picks the goal to ask about: the most overdue stale goal not
// asked about before, or the one shown earlier today while it is unchanged.
// touched reports a goal's last linked activity, and false for a goal Today
// may not show.
func goalCheckInFor(details []PurposeOpenLoopDetail, records map[string]goalCheckInRecord, touched func(PurposeOpenLoopDetail) (time.Time, bool), now time.Time) *GoalCheckInView {
	type candidate struct {
		view GoalCheckInView
		due  time.Time
	}
	var candidates []candidate
	for _, detail := range details {
		current := detail.Current
		if current.LifecycleState != PurposeStateActive && current.LifecycleState != PurposeStateWaiting ||
			current.SupportState != PurposeSupportSupported || current.DueKind != PurposeDueDate {
			continue
		}
		if record, asked := records[detail.OpenLoop.ID]; asked {
			shown := time.UnixMilli(record.ShownAt).In(now.Location())
			if record.EventID != current.ID || shown.Format("2006-01-02") != now.Format("2006-01-02") {
				continue
			}
		}
		due, ok := parseEngagementDue(current.DueValue, now.Location())
		if !ok || now.Sub(due) <= goalCheckInOverdue {
			continue
		}
		quietSince := now.Add(-goalCheckInQuiet).UnixMilli()
		if current.OccurredAt > quietSince {
			continue
		}
		recent := false
		for _, evidence := range detail.Evidence {
			recent = recent || evidence.SourceObservedAt > quietSince
		}
		if recent {
			continue
		}
		last, ok := touched(detail)
		if !ok || last.UnixMilli() > quietSince {
			continue
		}
		title := mobileGoalText(current.Title)
		if title == "" {
			continue
		}
		candidates = append(candidates, candidate{
			view: GoalCheckInView{GoalID: detail.OpenLoop.ID, EventID: current.ID, Title: title, DueAt: formatMobileBriefTime(due)},
			due:  due,
		})
	}
	if len(candidates) == 0 {
		return nil
	}
	sort.Slice(candidates, func(i, j int) bool {
		if !candidates[i].due.Equal(candidates[j].due) {
			return candidates[i].due.Before(candidates[j].due)
		}
		return candidates[i].view.GoalID < candidates[j].view.GoalID
	})
	return &candidates[0].view
}
