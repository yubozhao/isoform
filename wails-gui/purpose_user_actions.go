package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log"
	"strings"
	"time"
	"unicode/utf8"
)

// App-level wrappers over the Purpose Map for what the user does by hand: list
// the ledger, read one Purpose, add one, and move it (done, reopened, waiting,
// forgotten, or back to where it was before the user's last done or forget).
// The user is the source, so every event carries explicit_user evidence and
// the Purpose is supported from the start, like a reminder's commitment
// (reminders_purpose.go).
const (
	userPurposeSourceKind = "user_goal"
	userPurposeConfidence = 0.9
	// userPurposeTitleMaxRunes keeps a pasted wall of text out of a title.
	userPurposeTitleMaxRunes = 300
	// userPurposeWaitingMaxRunes bounds what the user says a goal waits on.
	userPurposeWaitingMaxRunes = 200

	// User transitions.
	userPurposeResolve = "resolve"
	userPurposeReopen  = "reopen"
	userPurposeWaiting = "waiting"
	// userPurposeForget hides a goal: a dismissed event, so the ledger keeps
	// the goal and the goal finder knows not to bring it back
	// (purposeClosedMatch). Nothing is erased.
	userPurposeForget = "forget"
	// userPurposeRestore is Undo for done and forget: the goal goes back to
	// active, or to waiting on the same thing when it was waiting.
	userPurposeRestore = "restore"
	// userPurposeKeep is the user saying a goal still matters (the check-in's
	// Keep): a reinforced event that changes nothing else.
	userPurposeKeep = "keep"
	// userPurposeRevise is the user's edit: a new title and due date (or
	// none), a revised event. A waiting goal stays waiting.
	userPurposeRevise = "revise"
)

var (
	errUserPurposeChanged = errors.New("this goal changed on your computer")
	errUserPurposeState   = errors.New("this goal cannot do that now")
	errUserPurposeStore   = errors.New("Purpose storage is unavailable")
	errUserPurposeEdit    = errors.New("an edited goal needs a title and a valid date")
	// errUserPurposeUndo: restore must name the done or forget it takes back,
	// so no caller can bring back a goal it never saw closed.
	errUserPurposeUndo = errors.New("restore names the event it undoes")
)

// userPurposeCreate is one goal the user adds: a kind and a sentence. Key is
// the caller's idempotency key, scoped to its origin (a paired iPhone's key is
// "mobile:<device>:<key>"); Origin names where the user said it. Category is
// an optional label (health, finance, ...) the goal keeps as its scope.
type userPurposeCreate struct {
	Key      string
	Origin   string
	Kind     string
	Text     string
	Category string
	// EvidenceKind and EvidenceSourceID, when set, name the source the user
	// added the goal from (a moment in Activity) instead of the goal itself.
	EvidenceKind     string
	EvidenceSourceID string
}

// userPurposeTransition is one move the user makes on a goal. ExpectedEventID
// is the event the user saw; a goal that moved since answers
// errUserPurposeChanged.
type userPurposeTransition struct {
	Key             string
	Origin          string
	OpenLoopID      string
	ExpectedEventID string
	Action          string
	WaitingOn       string
	// Title, and DueDate when SetDue (YYYY-MM-DD, empty for none), are the
	// edit (revise). Without SetDue the due stays as it is, a window or a
	// condition included.
	Title   string
	DueDate string
	SetDue  bool
}

func (a *App) userPurposeStore() (*PurposeMapStore, error) {
	store, err := a.purposeProductStore()
	if err != nil {
		return nil, errUserPurposeStore
	}
	return store, nil
}

func (a *App) userPurposeDetails() ([]PurposeOpenLoopDetail, error) {
	store, err := a.userPurposeStore()
	if err != nil {
		return nil, err
	}
	return store.ListDetailsAll()
}

func (a *App) userPurposeDetail(openLoopID string) (PurposeOpenLoopDetail, error) {
	store, err := a.userPurposeStore()
	if err != nil {
		return PurposeOpenLoopDetail{}, err
	}
	return store.GetDetail(openLoopID)
}

// userPurposeCreatedDetail reads the projection after the create committed;
// tests replace it to exercise a failed read without failing the write.
var userPurposeCreatedDetail = func(store *PurposeMapStore, openLoopID string) (PurposeOpenLoopDetail, error) {
	return store.GetDetail(openLoopID)
}

// createUserPurpose adds a goal the user wrote. The same key returns the same
// goal, so a retry never adds a second one. A read-back error keeps the
// committed records in the detail, so callers can distinguish a saved goal
// from a refused write without treating that detail as a fresh projection.
func (a *App) createUserPurpose(input userPurposeCreate, now time.Time) (PurposeOpenLoopDetail, error) {
	store, err := a.userPurposeStore()
	if err != nil {
		return PurposeOpenLoopDetail{}, err
	}
	createInput, err := userPurposeCreateInput(input, now)
	if err != nil {
		return PurposeOpenLoopDetail{}, err
	}
	// A later retry keeps the original occurrence and observation times.
	// The ledger still compares every other field, so reusing a key for a
	// different goal is refused instead of returning the old goal as success.
	existing, err := store.GetDetail(deterministicAgencyID("purpose-open-loop", createInput.IdempotencyKey))
	if err == nil {
		for _, event := range existing.Events {
			if event.Sequence == 1 && event.IdempotencyKey == createInput.IdempotencyKey {
				createInput.OccurredAt = event.OccurredAt
				for i := range createInput.Evidence {
					createInput.Evidence[i].SourceObservedAt = event.OccurredAt
				}
				break
			}
		}
	} else if !errors.Is(err, errPurposeOpenLoopNotFound) {
		return PurposeOpenLoopDetail{}, err
	}
	result, _, err := store.Create(createInput)
	if err != nil {
		return PurposeOpenLoopDetail{}, err
	}
	detail, err := userPurposeCreatedDetail(store, result.OpenLoop.ID)
	if err != nil {
		return PurposeOpenLoopDetail{
			OpenLoop: result.OpenLoop,
			Current:  result.Event,
			Events:   []PurposeOpenLoopEventRecord{result.Event},
			Evidence: result.Evidence,
		}, err
	}
	return detail, nil
}

// moveUserPurpose resolves, reopens or parks a goal for the user.
func (a *App) moveUserPurpose(input userPurposeTransition, now time.Time) (PurposeOpenLoopDetail, error) {
	store, err := a.userPurposeStore()
	if err != nil {
		return PurposeOpenLoopDetail{}, err
	}
	detail, err := store.GetDetail(input.OpenLoopID)
	if err != nil {
		return PurposeOpenLoopDetail{}, err
	}
	// A retry of a move the Mac already committed (its first answer was lost
	// after the write) is the same move: answer with the goal as it is, before
	// the expected-event check would call it a conflict.
	if userPurposeMoveApplied(detail, input.Key) {
		if input.Action == userPurposeForget {
			w, err := a.topicWithdrawalForSource("purpose", []string{detail.OpenLoop.ID}, nil)
			if err != nil {
				return PurposeOpenLoopDetail{}, err
			}
			err = a.withTopicWithdrawal(w, func() error { return nil })
			publishPrivacyWithdrawal(privacyWithdrawalScope{Kind: privacyWithdrawalSource, SourceKind: "purpose", ID: detail.OpenLoop.ID})
			if err != nil {
				return PurposeOpenLoopDetail{}, err
			}
		}
		return userPurposeRestoreWaiting(store, detail, input, now)
	}
	appendInput, err := userPurposeAppendInput(detail, input, now)
	if err != nil {
		return PurposeOpenLoopDetail{}, err
	}
	appendEvent := func() error { _, _, err := store.AppendEvent(appendInput); return err }
	if input.Action == userPurposeForget {
		w, err := a.topicWithdrawalForSource("purpose", []string{detail.OpenLoop.ID}, nil)
		if err != nil {
			return PurposeOpenLoopDetail{}, err
		}
		forgotten := false
		err = a.withTopicWithdrawal(w, func() error {
			err := appendEvent()
			forgotten = err == nil
			return err
		})
		if forgotten {
			publishPrivacyWithdrawal(privacyWithdrawalScope{Kind: privacyWithdrawalSource, SourceKind: "purpose", ID: detail.OpenLoop.ID})
		}
		if err != nil {
			return PurposeOpenLoopDetail{}, err
		}
	} else if err := appendEvent(); err != nil {
		return PurposeOpenLoopDetail{}, err
	}
	detail, err = store.GetDetail(detail.OpenLoop.ID)
	if err != nil {
		return PurposeOpenLoopDetail{}, err
	}
	return userPurposeRestoreWaiting(store, detail, input, now)
}

// userPurposeRestoreWaiting finishes a restore whose goal was waiting before
// the user's done or forget: the reopened event brings it back to active, and
// a second event under the same key puts it back to waiting on the same
// thing. A retry that finds the reopen already written writes only this one.
func userPurposeRestoreWaiting(store *PurposeMapStore, detail PurposeOpenLoopDetail, input userPurposeTransition, now time.Time) (PurposeOpenLoopDetail, error) {
	if input.Action != userPurposeRestore || strings.TrimSpace(input.Key) == "" {
		return detail, nil
	}
	key := "user-goal-event:" + input.Key
	var reopened PurposeOpenLoopEventRecord
	for _, event := range detail.Events {
		if event.IdempotencyKey == key+":waiting" {
			return detail, nil
		}
		if event.IdempotencyKey == key {
			reopened = event
		}
	}
	current := purposeCurrentLedgerEvent(detail)
	if reopened.ID == "" || current.ID != reopened.ID || current.LifecycleState != PurposeStateActive {
		return detail, nil
	}
	closed, ok := userPurposeEventByID(detail, reopened.PredecessorEventID)
	if !ok {
		return detail, nil
	}
	before, ok := userPurposeEventByID(detail, closed.PredecessorEventID)
	if !ok || before.LifecycleState != PurposeStateWaiting {
		return detail, nil
	}
	appendInput, err := userPurposeAppendInput(detail, userPurposeTransition{
		Key: input.Key + ":waiting", Origin: input.Origin, OpenLoopID: input.OpenLoopID,
		ExpectedEventID: current.ID, Action: userPurposeWaiting, WaitingOn: before.WaitingCondition,
	}, now)
	if err != nil {
		return PurposeOpenLoopDetail{}, err
	}
	if _, _, err := store.AppendEvent(appendInput); err != nil {
		return PurposeOpenLoopDetail{}, err
	}
	return store.GetDetail(detail.OpenLoop.ID)
}

func userPurposeEventByID(detail PurposeOpenLoopDetail, id string) (PurposeOpenLoopEventRecord, bool) {
	if id == "" {
		return PurposeOpenLoopEventRecord{}, false
	}
	for _, event := range detail.Events {
		if event.ID == id {
			return event, true
		}
	}
	return PurposeOpenLoopEventRecord{}, false
}

// userPurposeMoveApplied reports whether an event with this idempotency key
// is already on the goal.
func userPurposeMoveApplied(detail PurposeOpenLoopDetail, key string) bool {
	if strings.TrimSpace(key) == "" {
		return false
	}
	key = "user-goal-event:" + key
	for _, event := range detail.Events {
		if event.IdempotencyKey == key {
			return true
		}
	}
	return detail.Current.IdempotencyKey == key
}

func userPurposeTitle(text string) string {
	text = strings.Join(strings.Fields(strings.ToValidUTF8(text, "")), " ")
	if utf8.RuneCountInString(text) <= userPurposeTitleMaxRunes {
		return text
	}
	return string([]rune(text)[:userPurposeTitleMaxRunes])
}

func userPurposeEvidence(key, origin, stance, text string, occurredAt int64) PurposeEvidenceInput {
	sum := sha256.Sum256([]byte(key + "\x00" + stance + "\x00" + text))
	hash := hex.EncodeToString(sum[:])
	return PurposeEvidenceInput{
		SourceKind:       userPurposeSourceKind,
		SourceID:         key,
		SourceVersion:    hash[:16],
		SourceObservedAt: occurredAt,
		SourceHash:       hash,
		EvidenceClass:    PurposeEvidenceExplicitUser,
		Stance:           stance,
		IndependenceKey:  "user:" + origin,
		PrivacyDecision:  TaskContextPrivacyRedact,
	}
}

func userPurposeCreateInput(input userPurposeCreate, now time.Time) (CreatePurposeOpenLoopInput, error) {
	kind, err := normalizePurposeKind(input.Kind)
	if err != nil {
		return CreatePurposeOpenLoopInput{}, err
	}
	title := userPurposeTitle(input.Text)
	if title == "" {
		return CreatePurposeOpenLoopInput{}, errors.New("a goal needs a sentence")
	}
	key := "user-goal:" + input.Key
	occurredAt := now.UnixMilli()
	evidence := userPurposeEvidence(key, input.Origin, PurposeStanceSupport, title, occurredAt)
	if input.EvidenceKind != "" && input.EvidenceSourceID != "" {
		evidence.SourceKind, evidence.SourceID = input.EvidenceKind, input.EvidenceSourceID
	}
	return CreatePurposeOpenLoopInput{
		IdempotencyKey: key,
		CanonicalKey:   key,
		Kind:           kind,
		Title:          title,
		DesiredOutcome: title,
		Owner:          PurposeOwnerUser,
		Scope:          strings.TrimSpace(input.Category),
		DueKind:        PurposeDueNone,
		// Unknown reasoning cannot carry a rationale (purpose_map.go rejects it).
		ReasoningStatus: PurposeReasoningUnknown,
		Confidence:      userPurposeConfidence,
		OccurredAt:      occurredAt,
		Evidence:        []PurposeEvidenceInput{evidence},
	}, nil
}

func userPurposeAppendInput(detail PurposeOpenLoopDetail, input userPurposeTransition, now time.Time) (AppendPurposeOpenLoopEventInput, error) {
	current := purposeCurrentLedgerEvent(detail)
	if strings.TrimSpace(input.ExpectedEventID) != "" && input.ExpectedEventID != current.ID {
		return AppendPurposeOpenLoopEventInput{}, errUserPurposeChanged
	}
	var eventType, stance, waitingOn string
	title, outcome := current.Title, current.DesiredOutcome
	dueKind, dueValue := current.DueKind, current.DueValue
	switch input.Action {
	case userPurposeResolve:
		if current.LifecycleState != PurposeStateActive && current.LifecycleState != PurposeStateWaiting {
			return AppendPurposeOpenLoopEventInput{}, errUserPurposeState
		}
		eventType, stance = PurposeEventResolved, PurposeStanceResolve
	case userPurposeReopen:
		if current.LifecycleState != PurposeStateResolved {
			return AppendPurposeOpenLoopEventInput{}, errUserPurposeState
		}
		eventType, stance = PurposeEventReopened, PurposeStanceSupport
	case userPurposeWaiting:
		if current.LifecycleState != PurposeStateActive || current.SupportState != PurposeSupportSupported {
			return AppendPurposeOpenLoopEventInput{}, errUserPurposeState
		}
		waitingOn = strings.Join(strings.Fields(strings.ToValidUTF8(input.WaitingOn, "")), " ")
		if utf8.RuneCountInString(waitingOn) > userPurposeWaitingMaxRunes {
			waitingOn = string([]rune(waitingOn)[:userPurposeWaitingMaxRunes])
		}
		if waitingOn == "" {
			waitingOn = "Someone else"
		}
		eventType, stance = PurposeEventWaiting, PurposeStanceWait
	case userPurposeForget:
		if current.LifecycleState != PurposeStateCandidate && current.LifecycleState != PurposeStateActive &&
			current.LifecycleState != PurposeStateWaiting {
			return AppendPurposeOpenLoopEventInput{}, errUserPurposeState
		}
		eventType, stance = PurposeEventDismissed, PurposeStanceDismiss
	case userPurposeRestore:
		if strings.TrimSpace(input.ExpectedEventID) == "" {
			return AppendPurposeOpenLoopEventInput{}, errUserPurposeUndo
		}
		if current.LifecycleState != PurposeStateResolved && current.LifecycleState != PurposeStateDismissed {
			return AppendPurposeOpenLoopEventInput{}, errUserPurposeState
		}
		eventType, stance = PurposeEventReopened, PurposeStanceSupport
	case userPurposeKeep:
		// Keep answers the check-in, which asks only about goals being tracked.
		if current.LifecycleState != PurposeStateActive && current.LifecycleState != PurposeStateWaiting {
			return AppendPurposeOpenLoopEventInput{}, errUserPurposeState
		}
		// A waiting goal stays waiting on the same thing.
		waitingOn = current.WaitingCondition
		eventType, stance = PurposeEventReinforced, PurposeStanceSupport
	case userPurposeRevise:
		if current.LifecycleState != PurposeStateCandidate && current.LifecycleState != PurposeStateActive &&
			current.LifecycleState != PurposeStateWaiting {
			return AppendPurposeOpenLoopEventInput{}, errUserPurposeState
		}
		edited := userPurposeTitle(input.Title)
		if edited == "" {
			return AppendPurposeOpenLoopEventInput{}, errUserPurposeEdit
		}
		// An outcome that only repeated the title follows it.
		if purposeNormalizedOutcome(outcome) == purposeNormalizedOutcome(title) || strings.TrimSpace(outcome) == "" {
			outcome = edited
		}
		title = edited
		if input.SetDue {
			dueKind, dueValue = PurposeDueNone, ""
			if due := strings.TrimSpace(input.DueDate); due != "" {
				if _, err := time.Parse("2006-01-02", due); err != nil {
					return AppendPurposeOpenLoopEventInput{}, errUserPurposeEdit
				}
				dueKind, dueValue = PurposeDueDate, due
			}
		}
		waitingOn = current.WaitingCondition
		eventType, stance = PurposeEventRevised, PurposeStanceUpdate
	default:
		return AppendPurposeOpenLoopEventInput{}, errors.New("unknown goal action")
	}
	// Never before the event it follows, never ahead of the wall clock: the
	// ledger rejects both.
	occurredAt := now.UnixMilli()
	if occurredAt < current.OccurredAt {
		occurredAt = current.OccurredAt
	}
	if wall := time.Now().UnixMilli(); occurredAt > wall {
		occurredAt = wall
	}
	key := "user-goal-event:" + input.Key
	return AppendPurposeOpenLoopEventInput{
		OpenLoopID:       detail.OpenLoop.ID,
		ExpectedEventID:  current.ID,
		IdempotencyKey:   key,
		EventType:        eventType,
		Title:            title,
		DesiredOutcome:   outcome,
		Owner:            current.Owner,
		Counterparty:     current.Counterparty,
		Scope:            current.Scope,
		DueKind:          dueKind,
		DueValue:         dueValue,
		WaitingCondition: waitingOn,
		ReasoningStatus:  PurposeReasoningUnknown,
		Confidence:       userPurposeConfidence,
		OccurredAt:       occurredAt,
		Evidence:         []PurposeEvidenceInput{userPurposeEvidence(key, input.Origin, stance, title, occurredAt)},
	}, nil
}

// goalMoveError names a refused move for the page.
func goalMoveError(action string, err error) error {
	if errors.Is(err, errUserPurposeEdit) || errors.Is(err, errUserPurposeUndo) || errors.Is(err, errPurposeOpenLoopNotFound) {
		return errGoalMoveInvalid
	}
	if errors.Is(err, errUserPurposeChanged) || errors.Is(err, errUserPurposeState) {
		return errGoalMoveChanged
	}
	log.Printf("[goals] move failed: action=%s %v", action, err)
	return errGoalMoveUnavailable
}

// GoalMoveInput is one move the user makes on a goal on the computer (Today,
// the Goals page). Key is the caller's idempotency key for this one move, so a
// retried call never moves the goal twice; ExpectedEventID, when set, is the
// event the user saw.
type GoalMoveInput struct {
	GoalID          string `json:"goalId"`
	ExpectedEventID string `json:"expectedEventId,omitempty"`
	Action          string `json:"action"`
	Key             string `json:"key"`
	WaitingOn       string `json:"waitingOn,omitempty"`
	// Title, and DueDate when SetDue (YYYY-MM-DD, empty for none), are an
	// edit (revise); without SetDue the due is kept.
	Title   string `json:"title,omitempty"`
	DueDate string `json:"dueDate,omitempty"`
	SetDue  bool   `json:"setDue,omitempty"`
}

// GoalMoveResult is the goal after the move. EventID is what Undo names.
type GoalMoveResult struct {
	ID      string `json:"id"`
	EventID string `json:"eventId"`
	State   string `json:"state"`
	Title   string `json:"title"`
}

// Errors MoveGoal returns by name, so the page says each in the user's language.
var (
	errGoalMoveChanged     = errors.New("goal_changed")
	errGoalMoveUnavailable = errors.New("goal_unavailable")
	errGoalMoveInvalid     = errors.New("goal_invalid")
)

// MoveGoal marks a goal done, reopens it, parks it as waiting, forgets it,
// restores it after the user's done or forget (Undo), keeps it (the
// check-in's Keep), or edits its title and due date (revise).
func (a *App) MoveGoal(input GoalMoveInput) (GoalMoveResult, error) {
	key := strings.TrimSpace(input.Key)
	if key == "" || len(key) > 128 || !mobileValidID(input.GoalID) {
		return GoalMoveResult{}, errGoalMoveInvalid
	}
	switch input.Action {
	case userPurposeResolve, userPurposeReopen, userPurposeWaiting, userPurposeForget, userPurposeRestore, userPurposeKeep, userPurposeRevise:
	default:
		return GoalMoveResult{}, errGoalMoveInvalid
	}
	// A goal move changes what a sealed brief may say (its words and recording), and
	// no source is named for it, so the briefs are amended with the gates before they
	// are shown again. Forgetting a goal also publishes the goal as a named withdrawal.
	defer briefPrivacyChanged()
	detail, err := a.moveUserPurpose(userPurposeTransition{
		Key: "computer:" + key, Origin: "computer", OpenLoopID: input.GoalID,
		ExpectedEventID: input.ExpectedEventID, Action: input.Action, WaitingOn: input.WaitingOn,
		Title: input.Title, DueDate: input.DueDate, SetDue: input.SetDue,
	}, time.Now())
	if err != nil {
		return GoalMoveResult{}, goalMoveError(input.Action, err)
	}
	a.emitBrowserEvent("agency:updated")
	return GoalMoveResult{
		ID: detail.OpenLoop.ID, EventID: detail.Current.ID,
		State: detail.Current.LifecycleState, Title: detail.Current.Title,
	}, nil
}

// GoalCreateInput is a goal the user adds by hand on About you (Add a goal).
// Key is the caller's idempotency key, so a retried call adds it once.
type GoalCreateInput struct {
	Text string `json:"text"`
	Key  string `json:"key"`
}

// CreateGoal adds a goal the user typed on the computer. It answers with the
// goal as MoveGoal does, or one of MoveGoal's named errors.
func (a *App) CreateGoal(input GoalCreateInput) (GoalMoveResult, error) {
	key := strings.TrimSpace(input.Key)
	if key == "" || len(key) > 128 || strings.TrimSpace(input.Text) == "" {
		return GoalMoveResult{}, errGoalMoveInvalid
	}
	detail, err := a.createUserPurpose(userPurposeCreate{
		Key: "computer:" + key, Origin: "computer", Kind: PurposeKindGoal, Text: input.Text,
	}, time.Now())
	if err != nil && detail.OpenLoop.ID == "" {
		log.Printf("[goals] create failed: %v", err)
		return GoalMoveResult{}, errGoalMoveUnavailable
	}
	a.emitBrowserEvent("agency:updated")
	return GoalMoveResult{
		ID: detail.OpenLoop.ID, EventID: detail.Current.ID,
		State: detail.Current.LifecycleState, Title: detail.Current.Title,
	}, nil
}
