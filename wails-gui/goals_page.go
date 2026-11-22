package main

import (
	"errors"
	"sort"
	"strings"
	"time"
)

// The computer's Goals page (item 66, C1 to C4): the goals the phone tracks,
// in the phone's order, plus the ones held on this computer because their
// words repeat a mail subject. The page writes every sentence itself in the
// user's language, so this sends facts, not the phone's English status lines.
// Moves go through MoveGoal, the same user path as Today and the phone.

const (
	goalsPageGroupTracking = "tracking"
	goalsPageGroupDone     = "done"
	goalsPageGroupHeld     = "held"

	// Who added a goal, for "You added it from Activity on Tuesday."
	goalOriginChat     = "chat"
	goalOriginPhone    = "phone"
	goalOriginActivity = "activity"
	goalOriginReminder = "reminder"
	goalOriginMeeting  = "meeting"
	goalOriginNoticed  = "noticed"
)

// GoalItemView is one goal on the Goals page.
type GoalItemView struct {
	ID      string `json:"id"`
	EventID string `json:"eventId"`
	Kind    string `json:"kind"`
	Title   string `json:"title"`
	// State is active, waiting or resolved; Group is tracking, done or held.
	State    string `json:"state"`
	Group    string `json:"group"`
	Slipping bool   `json:"slipping"`
	// DueAt is the end of the due day (RFC 3339); DueDate is the day itself,
	// YYYY-MM-DD, for the edit form.
	DueAt     string `json:"dueAt,omitempty"`
	DueDate   string `json:"dueDate,omitempty"`
	AddedAt   string `json:"addedAt"`
	UpdatedAt string `json:"updatedAt"`
	// LastEvent is the current event's type (discovered, reinforced, revised,
	// waiting, resolved, reopened).
	LastEvent string `json:"lastEvent"`
	Origin    string `json:"origin"`
	WaitingOn string `json:"waitingOn,omitempty"`
	// Outcome is the aim, when it says more than the title.
	Outcome string `json:"outcome,omitempty"`
}

// GoalsPageView is the page: tracking first in the phone's order, then done,
// then held.
type GoalsPageView struct {
	Tracking []GoalItemView `json:"tracking"`
	Done     []GoalItemView `json:"done"`
	Held     []GoalItemView `json:"held"`
}

// GoalHistoryView is one thing that happened to a goal.
type GoalHistoryView struct {
	Event string `json:"event"`
	At    string `json:"at"`
}

// GoalSourceView counts what a goal rests on, by kind.
type GoalSourceView struct {
	Kind  string `json:"kind"`
	Count int    `json:"count"`
}

// GoalStoryView is one goal opened: the goal, what happened to it (newest
// first) and what it rests on.
type GoalStoryView struct {
	Goal    GoalItemView      `json:"goal"`
	History []GoalHistoryView `json:"history"`
	Sources []GoalSourceView  `json:"sources"`
}

// ListGoals is the Goals page.
func (a *App) ListGoals() (perfValue GoalsPageView, perfErr error) {
	// Playbooks, SOPs and goals are off unless their switch is on
	// (feature_switches.go).
	if !featureOn(featurePlaybooks) {
		return GoalsPageView{Tracking: []GoalItemView{}, Done: []GoalItemView{}, Held: []GoalItemView{}}, nil
	}
	defer logPageBinding("ListGoals", time.Now(), &perfValue)
	details, err := a.userPurposeDetails()
	if err != nil {
		return GoalsPageView{}, errGoalMoveUnavailable
	}
	// Mail that cannot be read holds nothing back here: the page never leaves
	// this computer. The held list then waits for the next read.
	subjects, _ := mobileGoalSubjects(a)
	return goalsPage(details, subjects, time.Now()), nil
}

// GetGoal is one goal's story. Goals the page does not list answer
// goal_invalid.
func (a *App) GetGoal(goalID string) (perfValue GoalStoryView, perfErr error) {
	// Playbooks, SOPs and goals are off unless their switch is on
	// (feature_switches.go).
	if !featureOn(featurePlaybooks) {
		return GoalStoryView{}, errFeatureOff(featurePlaybooks)
	}
	defer logPageBinding("GetGoal", time.Now(), &perfValue)
	if !mobileValidID(goalID) {
		return GoalStoryView{}, errGoalMoveInvalid
	}
	detail, err := a.userPurposeDetail(goalID)
	if err != nil || !mobileGoalVisible(detail) {
		if err == nil || errors.Is(err, errPurposeOpenLoopNotFound) {
			return GoalStoryView{}, errGoalMoveInvalid
		}
		return GoalStoryView{}, errGoalMoveUnavailable
	}
	subjects, _ := mobileGoalSubjects(a)
	now := time.Now()
	return goalStory(detail, subjects, now), nil
}

func goalsPage(details []PurposeOpenLoopDetail, subjects []string, now time.Time) GoalsPageView {
	type entry struct {
		item GoalItemView
		key  mobileGoalSortKey
	}
	var entries []entry
	for _, detail := range details {
		if !mobileGoalVisible(detail) {
			continue
		}
		view := mobileGoalView(detail, now)
		if view.Title == "" {
			continue
		}
		entries = append(entries, entry{item: goalItem(detail, view, subjects, now), key: mobileGoalKey(view, detail)})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].key.less(entries[j].key) })
	page := GoalsPageView{Tracking: []GoalItemView{}, Done: []GoalItemView{}, Held: []GoalItemView{}}
	for _, item := range entries {
		switch item.item.Group {
		case goalsPageGroupDone:
			page.Done = append(page.Done, item.item)
		case goalsPageGroupHeld:
			page.Held = append(page.Held, item.item)
		default:
			page.Tracking = append(page.Tracking, item.item)
		}
	}
	return page
}

func goalItem(detail PurposeOpenLoopDetail, view MobileGoalView, subjects []string, now time.Time) GoalItemView {
	current := detail.Current
	item := GoalItemView{
		ID: view.ID, EventID: view.EventID, Kind: view.Kind, Title: view.Title,
		State: view.State, Group: goalsPageGroupTracking, Slipping: view.Slipping,
		DueAt: view.DueAt, UpdatedAt: view.UpdatedAt, LastEvent: current.EventType,
		Origin: goalOrigin(detail), AddedAt: formatMobileBriefTime(time.UnixMilli(detail.OpenLoop.CreatedAt)),
	}
	if view.Group == mobileGoalGroupDone {
		item.Group = goalsPageGroupDone
	} else if mobileGoalEchoesMail(detail, view, subjects) {
		item.Group = goalsPageGroupHeld
	}
	if current.DueKind == PurposeDueDate {
		if due, ok := parseEngagementDue(current.DueValue, now.Location()); ok {
			item.DueDate = due.Format("2006-01-02")
		}
	}
	if current.LifecycleState == PurposeStateWaiting {
		item.WaitingOn = mobileGoalText(current.WaitingCondition)
	}
	if outcome := mobileGoalText(current.DesiredOutcome); outcome != "" &&
		purposeNormalizedOutcome(outcome) != purposeNormalizedOutcome(current.Title) {
		item.Outcome = outcome
	}
	return item
}

// goalOrigin names who added the goal, from its first event's evidence.
func goalOrigin(detail PurposeOpenLoopDetail) string {
	first := ""
	for _, event := range detail.Events {
		if event.Sequence == 1 {
			first = event.ID
		}
	}
	for _, evidence := range detail.Evidence {
		if evidence.EventID != first {
			continue
		}
		switch evidence.SourceKind {
		case userPurposeSourceKind:
			if strings.HasPrefix(evidence.IndependenceKey, "user:mobile:") {
				return goalOriginPhone
			}
			return goalOriginChat
		case activityFollowUpSourceKind:
			return goalOriginActivity
		case reminderPurposeSourceKind:
			return goalOriginReminder
		case meetingFollowupSourceKind:
			return goalOriginMeeting
		}
	}
	return goalOriginNoticed
}

func goalStory(detail PurposeOpenLoopDetail, subjects []string, now time.Time) GoalStoryView {
	view := mobileGoalView(detail, now)
	story := GoalStoryView{Goal: goalItem(detail, view, subjects, now), History: []GoalHistoryView{}, Sources: []GoalSourceView{}}
	events := append([]PurposeOpenLoopEventRecord(nil), detail.Events...)
	sort.Slice(events, func(i, j int) bool { return events[i].Sequence > events[j].Sequence })
	for _, event := range events {
		if len(story.History) >= mobileGoalHistoryLimit {
			break
		}
		story.History = append(story.History, GoalHistoryView{Event: event.EventType, At: formatMobileBriefTime(time.UnixMilli(event.OccurredAt))})
	}
	counts := map[string]int{}
	seen := map[string]bool{}
	for _, evidence := range detail.Evidence {
		key := evidence.SourceKind + "\x00" + evidence.SourceID
		if seen[key] {
			continue
		}
		seen[key] = true
		counts[goalSourceKind(evidence.SourceKind)]++
	}
	for kind, count := range counts {
		story.Sources = append(story.Sources, GoalSourceView{Kind: kind, Count: count})
	}
	sort.Slice(story.Sources, func(i, j int) bool { return story.Sources[i].Kind < story.Sources[j].Kind })
	return story
}

// goalSourceKind folds the ledger's source kinds into the few the page names.
func goalSourceKind(kind string) string {
	switch kind {
	case userPurposeSourceKind:
		return "you"
	case activityFollowUpSourceKind, "activity", "activity_summary", "activity_run", "activity_session", "activity_timeline_group", "session":
		return "activity"
	case reminderPurposeSourceKind:
		return "reminder"
	case meetingFollowupSourceKind:
		return "meeting"
	case "memory", "knowledge", taskContextSourceMemoryKind, taskContextSourceKnowledgeKind:
		return "memory"
	default:
		return "other"
	}
}
