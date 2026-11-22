package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	"wails-gui/internal/platform/mailheaders"
	"wails-gui/storage"
)

// Goals for the phone (PROTOCOL.md section 20.5): the Purpose Map's commitments
// and goals the user is tracking, the ones done, adding one from a kind and a
// sentence, and checking one off or reopening it.
const (
	mobileGoalsDefaultLimit = 30
	mobileGoalsMaxLimit     = 100
	mobileGoalTextMaxBytes  = 1000
	mobileGoalGroupTracking = "tracking"
	mobileGoalGroupDone     = "done"
	// mobileGoalGroupForgotten names a goal the user just forgot, only in the
	// answer to forget; no list or read shows it.
	mobileGoalGroupForgotten = "forgotten"
	mobileGoalHistoryLimit   = 10
	// mobileGoalSubjectMinRunes is the shortest mail subject the echo check
	// looks for; "Hi" or "Re: notes" would hold back ordinary goals.
	mobileGoalSubjectMinRunes = 12
)

// MobileGoalView is one goal as the phone shows it. Status is the line under
// the title, built here so the phone never shows raw lifecycle words.
type MobileGoalView struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Title     string `json:"title"`
	State     string `json:"state"` // active | waiting | resolved, or dismissed in a forget's answer
	Group     string `json:"group"` // tracking | done | forgotten
	Status    string `json:"status"`
	Slipping  bool   `json:"slipping"`
	DueAt     string `json:"dueAt,omitempty"`
	UpdatedAt string `json:"updatedAt"`
	// EventID is the goal's current event; a write names it, so a goal that
	// moved on the Mac since the phone read it answers 409.
	EventID string `json:"eventId"`
	// AddedByYou is set when the user wrote the goal (on the phone, the Mac or
	// in Reminders) rather than Isoform noticing it.
	AddedByYou bool `json:"addedByYou"`
}

type MobileGoalCounts struct {
	Tracking int `json:"tracking"`
	Done     int `json:"done"`
	// OnMacOnly counts goals held back because their words repeat the subject
	// of a mail the Mac has seen; they stay on the Mac.
	OnMacOnly int `json:"onMacOnly"`
}

// MobileGoalPage is GET /goals: tracking goals first, then done ones.
type MobileGoalPage struct {
	Goals   []MobileGoalView `json:"goals"`
	Counts  MobileGoalCounts `json:"counts"`
	Cursor  string           `json:"cursor,omitempty"`
	HasMore bool             `json:"hasMore"`
}

type MobileGoalHistoryEntry struct {
	What string `json:"what"`
	At   string `json:"at"`
}

// MobileGoalDetail is GET /goals/{id}.
type MobileGoalDetail struct {
	Goal    MobileGoalView           `json:"goal"`
	Outcome string                   `json:"outcome,omitempty"`
	History []MobileGoalHistoryEntry `json:"history"`
}

// mobileGoalVisible reports whether a Purpose belongs on the phone: tracked
// (active or waiting) or done (resolved). Candidates, contradicted, dismissed
// and superseded Purposes stay on the Mac.
func mobileGoalVisible(detail PurposeOpenLoopDetail) bool {
	switch detail.Current.LifecycleState {
	case PurposeStateActive, PurposeStateWaiting:
		return detail.Current.SupportState == PurposeSupportSupported
	case PurposeStateResolved:
		return true
	default:
		return false
	}
}

// mobileGoalText bounds a goal's text the way the brief bounds a Purpose's.
// Text from extraction was redacted when it was stored (purpose_extraction.go);
// redacting again here turned ordinary words into "[REDACTED:...]", because
// the label patterns read "contract draft" as a contract number.
func mobileGoalText(value string) string {
	return mobileBriefText(value, mobileBriefTextLimit)
}

// mobileGoalAddedByYou reports whether the goal's first event rests on the
// user's own words.
func mobileGoalAddedByYou(detail PurposeOpenLoopDetail) bool {
	first := ""
	for _, event := range detail.Events {
		if event.Sequence == 1 {
			first = event.ID
		}
	}
	for _, evidence := range detail.Evidence {
		if evidence.EventID == first && evidence.EvidenceClass == PurposeEvidenceExplicitUser {
			return true
		}
	}
	return false
}

// mobileGoalTitle gives a commitment the short name it has on Today, in Chat
// and on the phone (thursday-polish C1); the full words stay in its detail.
func mobileGoalTitle(kind, title string) string {
	if kind == PurposeKindCommitment {
		if short := commitmentShortName(title); short != "" {
			return mobileGoalText(short)
		}
	}
	return mobileGoalText(title)
}

// mobileGoalView builds the phone's view of one Purpose at now.
func mobileGoalView(detail PurposeOpenLoopDetail, now time.Time) MobileGoalView {
	current := detail.Current
	view := MobileGoalView{
		ID:         detail.OpenLoop.ID,
		Kind:       detail.OpenLoop.Kind,
		Title:      mobileGoalTitle(detail.OpenLoop.Kind, current.Title),
		State:      current.LifecycleState,
		Group:      mobileGoalGroupTracking,
		UpdatedAt:  formatMobileBriefTime(time.UnixMilli(current.OccurredAt)),
		EventID:    current.ID,
		AddedByYou: mobileGoalAddedByYou(detail),
	}
	switch current.LifecycleState {
	case PurposeStateResolved:
		view.Group = mobileGoalGroupDone
	case PurposeStateDismissed:
		view.Group = mobileGoalGroupForgotten
	}
	if current.DueKind == PurposeDueDate {
		if due, ok := parseEngagementDue(current.DueValue, now.Location()); ok {
			view.DueAt = formatMobileBriefTime(due)
		}
	}
	if view.Group == mobileGoalGroupTracking {
		_, view.Slipping = engagementCommitmentSlippingMoment(detail, now)
	}
	view.Status = mobileGoalText(mobileGoalStatus(detail, view, now))
	return view
}

// mobileGoalStatus is the sentence under a goal, two or three lines on the
// phone: what is due or what it waits on, with the date or the person, then
// when it last moved and, for a goal still tracked, the outcome when it says
// more than the title.
func mobileGoalStatus(detail PurposeOpenLoopDetail, view MobileGoalView, now time.Time) string {
	current := detail.Current
	touched := time.UnixMilli(current.OccurredAt)
	due, dated := parseEngagementDue(current.DueValue, now.Location())
	dated = dated && view.DueAt != ""
	if current.LifecycleState == PurposeStateDismissed {
		return "Forgotten " + mobileGoalDay(touched, now) + "."
	}
	if current.LifecycleState == PurposeStateResolved {
		return "Done " + mobileGoalDay(touched, now) + "."
	}
	var parts []string
	if current.LifecycleState == PurposeStateWaiting {
		parts = append(parts, mobileGoalWaitingText(current, touched, now))
	}
	if dated {
		parts = append(parts, mobileGoalDueText(due, view.Slipping, now))
	}
	if current.LifecycleState != PurposeStateWaiting {
		parts = append(parts, mobileGoalTouchedText(current.EventType, view.AddedByYou, touched, now))
	}
	if outcome := strings.TrimSpace(current.DesiredOutcome); outcome != "" &&
		!strings.EqualFold(outcome, strings.TrimSpace(current.Title)) {
		parts = append(parts, "The aim: "+strings.TrimRight(outcome, ".")+".")
	}
	return strings.Join(parts, " ")
}

// mobileGoalWaitingText is "Waiting on Ana since Tuesday: her signature.":
// the person, else what the goal waits on, then since when, then what is
// awaited when the person is named too.
func mobileGoalWaitingText(current PurposeOpenLoopEventRecord, since, now time.Time) string {
	who := strings.TrimSpace(current.Counterparty)
	condition := strings.TrimRight(strings.TrimSpace(current.WaitingCondition), ".")
	switch {
	case who != "" && condition != "" && !strings.EqualFold(who, condition):
		return "Waiting on " + who + " " + mobileGoalSince(since, now) + ": " + condition + "."
	case who != "":
		return "Waiting on " + strings.TrimRight(who, ".") + " " + mobileGoalSince(since, now) + "."
	case condition != "":
		return "Waiting on " + condition + " " + mobileGoalSince(since, now) + "."
	default:
		return "Waiting " + mobileGoalSince(since, now) + "."
	}
}

// mobileGoalDueText is "Due on Oct 20." or "Was due yesterday."; a slipping
// goal adds that nothing has moved on it.
func mobileGoalDueText(due time.Time, slipping bool, now time.Time) string {
	text := "Due " + engagementDueText(due, now)
	if due.Before(now) {
		text = "Was due " + mobileGoalDay(due, now)
	}
	if slipping {
		return text + ", and nothing has moved on it yet."
	}
	return text + "."
}

// mobileGoalTouchedText says when a tracked goal last moved and who started it.
func mobileGoalTouchedText(eventType string, addedByYou bool, touched, now time.Time) string {
	day := mobileGoalDay(touched, now)
	switch eventType {
	case PurposeEventDiscovered:
		if addedByYou {
			return "You added it " + day + "."
		}
		return "Isoform noticed it " + day + "."
	case PurposeEventReopened:
		return "Reopened " + day + "."
	default:
		return "Last updated " + day + "."
	}
}

// mobileGoalSince is "since today", "since yesterday", "since Tuesday" within
// the week, then "since Oct 2".
func mobileGoalSince(at, now time.Time) string {
	day := mobileGoalDay(at, now)
	switch {
	case day == "today", day == "yesterday":
		return "since " + day
	case strings.HasPrefix(day, "on "):
		return "since " + strings.TrimPrefix(day, "on ")
	default:
		return "since " + at.In(now.Location()).Format("Monday")
	}
}

// mobileGoalDay says when, the way a list says it: "today", "yesterday",
// "3 days ago", "on Oct 2".
func mobileGoalDay(at, now time.Time) string {
	loc := now.Location()
	day := func(t time.Time) time.Time {
		l := t.In(loc)
		return time.Date(l.Year(), l.Month(), l.Day(), 0, 0, 0, 0, loc)
	}
	days := int(math.Round(day(now).Sub(day(at)).Hours() / 24))
	if days <= 0 {
		return "today"
	}
	if days == 1 {
		return "yesterday"
	}
	if days < 7 {
		return fmt.Sprintf("%d days ago", days)
	}
	return "on " + at.In(loc).Format("Jan 2")
}

func mobileGoalHistoryWhat(event PurposeOpenLoopEventRecord) string {
	switch event.EventType {
	case PurposeEventDiscovered:
		return "Added"
	case PurposeEventReinforced:
		return "Seen again"
	case PurposeEventRevised:
		return "Updated"
	case PurposeEventWaiting:
		return "Waiting"
	case PurposeEventResolved:
		return "Done"
	case PurposeEventReopened:
		return "Reopened"
	default:
		return "Changed"
	}
}

func mobileGoalDetailView(detail PurposeOpenLoopDetail, now time.Time) MobileGoalDetail {
	view := MobileGoalDetail{Goal: mobileGoalView(detail, now), History: []MobileGoalHistoryEntry{}}
	if outcome := strings.TrimSpace(detail.Current.DesiredOutcome); outcome != "" && outcome != strings.TrimSpace(detail.Current.Title) {
		view.Outcome = mobileGoalText(outcome)
	}
	events := append([]PurposeOpenLoopEventRecord(nil), detail.Events...)
	sort.SliceStable(events, func(i, j int) bool { return events[i].Sequence > events[j].Sequence })
	for _, event := range events {
		if len(view.History) >= mobileGoalHistoryLimit {
			break
		}
		view.History = append(view.History, MobileGoalHistoryEntry{
			What: mobileGoalHistoryWhat(event), At: formatMobileBriefTime(time.UnixMilli(event.OccurredAt)),
		})
	}
	return view
}

// mobileGoalSortKey orders goals: tracking before done; slipping first, then
// the soonest due, then the most recently touched; done ones newest first.
type mobileGoalSortKey struct {
	Group    int    `json:"g"`
	Slipping int    `json:"s"`
	Due      int64  `json:"d"`
	Touched  int64  `json:"t"`
	ID       string `json:"i"`
}

func mobileGoalKey(view MobileGoalView, detail PurposeOpenLoopDetail) mobileGoalSortKey {
	key := mobileGoalSortKey{Due: math.MaxInt64, Touched: -detail.Current.OccurredAt, ID: view.ID}
	if view.Group == mobileGoalGroupDone {
		key.Group = 1
		return key
	}
	if !view.Slipping {
		key.Slipping = 1
	}
	if view.DueAt != "" {
		if due, err := time.Parse(time.RFC3339, view.DueAt); err == nil {
			key.Due = due.UnixMilli()
		}
	}
	return key
}

func (k mobileGoalSortKey) less(other mobileGoalSortKey) bool {
	if k.Group != other.Group {
		return k.Group < other.Group
	}
	if k.Slipping != other.Slipping {
		return k.Slipping < other.Slipping
	}
	if k.Due != other.Due {
		return k.Due < other.Due
	}
	if k.Touched != other.Touched {
		return k.Touched < other.Touched
	}
	return k.ID < other.ID
}

func encodeMobileGoalCursor(key mobileGoalSortKey) string {
	raw, _ := json.Marshal(key)
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeMobileGoalCursor(cursor string) (mobileGoalSortKey, bool) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil || len(raw) > 1024 {
		return mobileGoalSortKey{}, false
	}
	var key mobileGoalSortKey
	if err := json.Unmarshal(raw, &key); err != nil || key.ID == "" {
		return mobileGoalSortKey{}, false
	}
	return key, true
}

// errMobileGoalMailUnavailable means the mail the hold compares against could
// not be read, so no goal can be shown to be safe; the routes answer 503 and
// the phone keeps what it has.
var errMobileGoalMailUnavailable = errors.New("mail subjects unavailable")

// mobileGoalMailRows lists the mail the Mac keeps; tests replace it.
var mobileGoalMailRows = func(a *App) ([]storage.MailMessageRow, error) {
	if a == nil || a.store == nil {
		return nil, errMobileGoalMailUnavailable
	}
	return a.store.ListMailMessages(context.Background(), 0)
}

// mobileGoalSubjects are the normalized subjects of the mail the Mac keeps
// (mail_sync.go, about a week). Purpose text can come from activity summaries
// that saw a mail, so a goal that repeats a subject stays on the Mac. When the
// mail cannot be read the hold fails closed: the error, never an empty list.
func mobileGoalSubjects(a *App) ([]string, error) {
	rows, err := mobileGoalMailRows(a)
	if err != nil {
		log.Printf("[mobile-goals] mail subjects unavailable: %v", err)
		return nil, errMobileGoalMailUnavailable
	}
	seen := map[string]bool{}
	out := []string{}
	for _, row := range rows {
		subject := mobileGoalFold(mailheaders.NormalizeSubject(row.Subject))
		if len([]rune(subject)) < mobileGoalSubjectMinRunes || seen[subject] {
			continue
		}
		seen[subject] = true
		out = append(out, subject)
	}
	return out, nil
}

func mobileGoalFold(text string) string {
	return strings.Join(strings.Fields(strings.ToLower(text)), " ")
}

// mobileGoalEchoesMail reports whether the text the phone would show contains
// a mail subject, or is itself part of one. Words the user wrote for the goal
// are theirs and never held (mobileGoalUserWords); everything else on it, a
// waiting condition or a title Isoform's goal finder revised, is checked.
func mobileGoalEchoesMail(detail PurposeOpenLoopDetail, view MobileGoalView, subjects []string) bool {
	if len(subjects) == 0 {
		return false
	}
	texts := []string{view.Title, view.Status, detail.Current.DesiredOutcome}
	if len(mobileGoalUserWords(detail)) > 0 {
		// The status line is built from these fields and dates; check the
		// fields one by one so the user's own title can be left out.
		texts = mobileGoalForeignText(detail)
	}
	for _, text := range texts {
		folded := mobileGoalFold(text)
		if folded == "" {
			continue
		}
		for _, subject := range subjects {
			if strings.Contains(folded, subject) ||
				(len([]rune(folded)) >= mobileGoalSubjectMinRunes && strings.Contains(subject, folded)) {
				return true
			}
		}
	}
	return false
}

// mobileGoalUserWords are the words the user wrote for this goal, folded:
// the title and outcome of an event that rests on their own sentence (typed
// in the main chat on either device, on the phone, or edited on the Goals
// page) and the waiting condition of a waiting move they made. Text a user
// move only carried over (the title on a check-off) is not theirs, and
// neither is anything from Isoform's finds, reminders, meeting or Activity
// follow-ups (whose words come from an observed moment).
func mobileGoalUserWords(detail PurposeOpenLoopDetail) map[string]bool {
	byUser := map[string]bool{}
	for _, evidence := range detail.Evidence {
		if evidence.EvidenceClass == PurposeEvidenceExplicitUser && evidence.SourceKind == userPurposeSourceKind {
			byUser[evidence.EventID] = true
		}
	}
	words := map[string]bool{}
	add := func(text string) {
		if folded := mobileGoalFold(text); folded != "" {
			words[folded] = true
		}
	}
	for _, event := range detail.Events {
		if !byUser[event.ID] {
			continue
		}
		switch event.EventType {
		case PurposeEventDiscovered:
			add(event.Title)
			add(event.DesiredOutcome)
		case PurposeEventRevised:
			add(event.Title)
			// An edit's outcome is the user's only when it follows the title.
			if mobileGoalFold(event.DesiredOutcome) == mobileGoalFold(event.Title) {
				add(event.DesiredOutcome)
			}
		case PurposeEventWaiting:
			add(event.WaitingCondition)
		}
	}
	return words
}

// mobileGoalForeignText is the goal's current text the user did not write:
// what every mail-subject check of a goal reads.
func mobileGoalForeignText(detail PurposeOpenLoopDetail) []string {
	words := mobileGoalUserWords(detail)
	var out []string
	for _, text := range []string{detail.Current.Title, detail.Current.DesiredOutcome, detail.Current.WaitingCondition, detail.Current.Counterparty} {
		if folded := mobileGoalFold(text); folded != "" && !words[folded] {
			out = append(out, text)
		}
	}
	return out
}

// mobileGoalPage builds one page of goals after the cursor. The cursor names
// the last goal of the previous page by its sort key, so a goal that moves
// between pages is neither skipped twice nor lost.
func mobileGoalPage(details []PurposeOpenLoopDetail, subjects []string, now time.Time, limit int, after *mobileGoalSortKey) MobileGoalPage {
	type entry struct {
		view MobileGoalView
		key  mobileGoalSortKey
	}
	entries := []entry{}
	page := MobileGoalPage{Goals: []MobileGoalView{}}
	for _, detail := range details {
		if !mobileGoalVisible(detail) {
			continue
		}
		view := mobileGoalView(detail, now)
		if view.Title == "" {
			continue
		}
		if mobileGoalEchoesMail(detail, view, subjects) {
			page.Counts.OnMacOnly++
			continue
		}
		if view.Group == mobileGoalGroupDone {
			page.Counts.Done++
		} else {
			page.Counts.Tracking++
		}
		entries = append(entries, entry{view: view, key: mobileGoalKey(view, detail)})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].key.less(entries[j].key) })
	for _, item := range entries {
		if after != nil && !after.less(item.key) {
			continue
		}
		if len(page.Goals) >= limit {
			page.HasMore = true
			break
		}
		page.Goals = append(page.Goals, item.view)
		page.Cursor = encodeMobileGoalCursor(item.key)
	}
	if !page.HasMore {
		page.Cursor = ""
	}
	return page
}

// mobileGoalsNow is the clock the goal routes read; tests replace it.
var mobileGoalsNow = time.Now

func (bridge *mobileBridge) serveGoalList(w http.ResponseWriter, r *http.Request) {
	limit, ok := mobileParseLimit(r.URL.Query().Get("limit"), mobileGoalsDefaultLimit, mobileGoalsMaxLimit)
	if !ok {
		mobileJSON(w, http.StatusBadRequest, nil, "limit must be between 1 and 100")
		return
	}
	var after *mobileGoalSortKey
	if raw := r.URL.Query().Get("cursor"); raw != "" {
		key, ok := decodeMobileGoalCursor(raw)
		if !ok {
			mobileJSON(w, http.StatusBadRequest, nil, "the cursor is not valid")
			return
		}
		after = &key
	}
	// Goals are off unless their switch is on: the phone gets an empty page.
	if !featureOn(featurePlaybooks) {
		mobileJSON(w, http.StatusOK, mobileGoalPage(nil, nil, mobileGoalsNow(), limit, nil), "")
		return
	}
	details, err := bridge.app.userPurposeDetails()
	if err != nil {
		mobileJSON(w, http.StatusServiceUnavailable, nil, errMobileStoreUnavailable.Error())
		return
	}
	subjects, err := mobileGoalSubjects(bridge.app)
	if err != nil {
		mobileJSON(w, http.StatusServiceUnavailable, nil, errMobileStoreUnavailable.Error())
		return
	}
	mobileJSON(w, http.StatusOK, mobileGoalPage(details, subjects, mobileGoalsNow(), limit, after), "")
}

func (bridge *mobileBridge) serveGoalDetail(w http.ResponseWriter, goalID string) {
	if !mobileValidID(goalID) {
		mobileJSON(w, http.StatusBadRequest, nil, "the goal id is not valid")
		return
	}
	detail, err := mobileGoalDetail(bridge.app, goalID)
	if err != nil || !mobileGoalVisible(detail) {
		mobileGoalMissing(w, err)
		return
	}
	now := mobileGoalsNow()
	view := mobileGoalDetailView(detail, now)
	if held, ok := bridge.mobileGoalHeld(w, detail, view.Goal); held || !ok {
		return
	}
	mobileJSON(w, http.StatusOK, view, "")
}

// mobileGoalHeld applies the mail-subject hold on a single goal. A held goal
// answers 404 as if it were not on the Mac (PROTOCOL.md 20.5); when the mail
// cannot be read it answers 503, so the phone keeps what it has and retries.
// It returns (held, ok): the caller continues only when both are false and true.
func (bridge *mobileBridge) mobileGoalHeld(w http.ResponseWriter, detail PurposeOpenLoopDetail, view MobileGoalView) (bool, bool) {
	// A goal whose every word is the user's own needs no mail read at all.
	if len(mobileGoalUserWords(detail)) > 0 && len(mobileGoalForeignText(detail)) == 0 {
		return false, true
	}
	subjects, err := mobileGoalSubjects(bridge.app)
	if err != nil {
		mobileJSON(w, http.StatusServiceUnavailable, nil, errMobileStoreUnavailable.Error())
		return false, false
	}
	if mobileGoalEchoesMail(detail, view, subjects) {
		mobileGoalMissing(w, nil)
		return true, true
	}
	return false, true
}

// mobileGoalDetail reads one goal for the routes; tests replace it.
var mobileGoalDetail = func(a *App, goalID string) (PurposeOpenLoopDetail, error) {
	return a.userPurposeDetail(goalID)
}

// mobileGoalMissing answers for a goal the route will not serve: 404 when the
// Mac does not have it (or holds it back, err nil), 503 when the Mac could not
// read it, so the phone keeps its journal key and retries the same write.
func mobileGoalMissing(w http.ResponseWriter, err error) {
	if err == nil || errors.Is(err, errPurposeOpenLoopNotFound) {
		mobileJSON(w, http.StatusNotFound, nil, "this goal is no longer on your computer")
		return
	}
	mobileJSON(w, http.StatusServiceUnavailable, nil, errMobileStoreUnavailable.Error())
}

// serveGoalCreate adds a goal from a kind chip and one sentence.
func (bridge *mobileBridge) serveGoalCreate(w http.ResponseWriter, r *http.Request, device mobileDeviceRecord) {
	var input struct {
		IdempotencyKey string `json:"idempotencyKey"`
		Kind           string `json:"kind"`
		Text           string `json:"text"`
	}
	if !mobileDecode(w, r, &input) {
		return
	}
	key, valid := mobileKey(device.DeviceID, input.IdempotencyKey)
	if !valid {
		mobileJSON(w, http.StatusBadRequest, nil, "an idempotency key of 1 to 128 bytes is required")
		return
	}
	if _, err := normalizePurposeKind(input.Kind); err != nil {
		mobileJSON(w, http.StatusBadRequest, nil, "kind must be commitment, responsibility, goal, concern or opportunity")
		return
	}
	text := strings.TrimSpace(input.Text)
	if text == "" || len(text) > mobileGoalTextMaxBytes {
		mobileJSON(w, http.StatusBadRequest, nil, "a goal needs a sentence of at most 1000 bytes")
		return
	}
	bridge.runReceipted(w, device, key, r.Method+" "+r.URL.Path, func(w http.ResponseWriter) {
		detail, err := bridge.app.createUserPurpose(userPurposeCreate{
			Key: key, Origin: "mobile:" + device.DeviceID, Kind: input.Kind, Text: text,
		}, mobileGoalsNow())
		if err != nil {
			mobileGoalWriteFailed(w, err)
			return
		}
		bridge.app.emitBrowserEvent("agency:updated")
		mobileJSON(w, http.StatusCreated, map[string]any{"goal": mobileGoalView(detail, mobileGoalsNow())}, "")
	})
}

// mobileGoalMovable reports whether the events route may move a goal: one the
// phone can see, or, for restore (Undo), one the user just forgot, which no
// list shows any more.
func mobileGoalMovable(detail PurposeOpenLoopDetail, action string) bool {
	return mobileGoalVisible(detail) || action == userPurposeRestore && detail.Current.LifecycleState == PurposeStateDismissed
}

// serveGoalEvent checks a goal off, reopens it, marks it waiting, forgets it,
// or restores it after a done or forget (Undo).
func (bridge *mobileBridge) serveGoalEvent(w http.ResponseWriter, r *http.Request, device mobileDeviceRecord, goalID string) {
	if !mobileValidID(goalID) {
		mobileJSON(w, http.StatusBadRequest, nil, "the goal id is not valid")
		return
	}
	var input struct {
		IdempotencyKey  string `json:"idempotencyKey"`
		Action          string `json:"action"`
		ExpectedEventID string `json:"expectedEventId"`
		WaitingOn       string `json:"waitingOn"`
	}
	if !mobileDecode(w, r, &input) {
		return
	}
	key, valid := mobileKey(device.DeviceID, input.IdempotencyKey)
	if !valid {
		mobileJSON(w, http.StatusBadRequest, nil, "an idempotency key of 1 to 128 bytes is required")
		return
	}
	switch input.Action {
	case userPurposeResolve, userPurposeReopen, userPurposeWaiting, userPurposeForget, userPurposeRestore:
	default:
		mobileJSON(w, http.StatusBadRequest, nil, "action must be resolve, reopen, waiting, forget or restore")
		return
	}
	if len(input.WaitingOn) > mobileGoalTextMaxBytes || len(input.ExpectedEventID) > mobileIDMaxLength {
		mobileJSON(w, http.StatusBadRequest, nil, "the request is too long")
		return
	}
	// Undo names the done or forget it takes back, so a phone cannot bring
	// back a goal it never saw closed (one the goal finder or a deleted
	// reminder dismissed).
	if input.Action == userPurposeRestore && strings.TrimSpace(input.ExpectedEventID) == "" {
		mobileJSON(w, http.StatusBadRequest, nil, "restore needs the expectedEventId of the move it undoes")
		return
	}
	bridge.runReceipted(w, device, key, r.Method+" "+r.URL.Path, func(w http.ResponseWriter) {
		current, err := mobileGoalDetail(bridge.app, goalID)
		if err != nil || !mobileGoalMovable(current, input.Action) {
			mobileGoalMissing(w, err)
			return
		}
		if held, ok := bridge.mobileGoalHeld(w, current, mobileGoalView(current, mobileGoalsNow())); held || !ok {
			return
		}
		detail, err := bridge.app.moveUserPurpose(userPurposeTransition{
			Key: key, Origin: "mobile:" + device.DeviceID, OpenLoopID: goalID,
			ExpectedEventID: input.ExpectedEventID, Action: input.Action, WaitingOn: input.WaitingOn,
		}, mobileGoalsNow())
		if err != nil {
			mobileGoalWriteFailed(w, err)
			return
		}
		bridge.app.emitBrowserEvent("agency:updated")
		mobileJSON(w, http.StatusOK, map[string]any{"goal": mobileGoalView(detail, mobileGoalsNow())}, "")
	})
}

func mobileGoalWriteFailed(w http.ResponseWriter, err error) {
	if errors.Is(err, errUserPurposeChanged) || errors.Is(err, errUserPurposeState) {
		mobileJSON(w, http.StatusConflict, nil, "this goal changed on your computer; refresh and try again")
		return
	}
	if errors.Is(err, errUserPurposeStore) {
		mobileJSON(w, http.StatusServiceUnavailable, nil, errMobileStoreUnavailable.Error())
		return
	}
	// Anything else is the Mac's problem, not a conflict: 503 keeps the phone's
	// journal key, so the same write is retried once the Mac is well.
	log.Printf("[mobile-goals] write failed: %v", err)
	mobileJSON(w, http.StatusServiceUnavailable, nil, errMobileStoreUnavailable.Error())
}
