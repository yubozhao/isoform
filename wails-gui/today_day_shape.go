package main

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"wails-gui/internal/calendar"
)

// TodayDayShape is a read-only calendar projection. A hold is a question's
// preview, never a calendar event or permission to create one.
type TodayDayShape struct {
	RangeStart        string               `json:"rangeStart"`
	RangeEnd          string               `json:"rangeEnd"`
	Now               string               `json:"now"`
	Events            []TodayDayShapeEvent `json:"events"`
	Hold              *TodayDayShapeHold   `json:"hold"`
	CalendarConnected bool                 `json:"calendarConnected"`
}

type TodayDayShapeEvent struct {
	ID           string `json:"id"`
	Start        string `json:"start"`
	End          string `json:"end"`
	Title        string `json:"title"`
	ShortName    string `json:"shortName"`
	ShortestName string `json:"shortestName"`
	Initial      string `json:"initial,omitempty"`
	Kind         string `json:"kind"` // meeting | personal
	HoldID       string `json:"holdId,omitempty"`
	// Private is an event whose title must not be shown; its names are empty
	// and the strip says only "Private" (today-review v1.1.13 D7).
	Private bool `json:"private,omitempty"`
}

type TodayDayShapeHold struct {
	Start string `json:"start"`
	End   string `json:"end"`
	Label string `json:"label"`
}

// TodayDayShapeDateError gives callers a stable code for malformed dates or
// dates outside the computer's current today/tomorrow window.
type TodayDayShapeDateError struct{}

func (*TodayDayShapeDateError) Error() string { return "TODAY_DAY_SHAPE_DATE_INVALID" }

func todayDayShapeDates(now time.Time) [2]time.Time {
	local := now.In(time.Local)
	today := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.Local)
	return [2]time.Time{today, today.AddDate(0, 0, 1)}
}

func validateTodayDayShapeDate(day, now time.Time) error {
	for _, allowed := range todayDayShapeDates(now) {
		if day.Format("2006-01-02") == allowed.Format("2006-01-02") {
			return nil
		}
	}
	return &TodayDayShapeDateError{}
}

// GetTodayDayShape reads today or tomorrow (local YYYY-MM-DD), without asking
// for permission, generating a brief or writing to the calendar.
func (a *App) GetTodayDayShape(dateISO string) (perfValue TodayDayShape, perfErr error) {
	defer logPageBinding("GetTodayDayShape", time.Now(), &perfValue)
	day, err := time.ParseInLocation("2006-01-02", dateISO, time.Local)
	if err != nil {
		return TodayDayShape{}, &TodayDayShapeDateError{}
	}
	if err := validateTodayDayShapeDate(day, a.todayDayShapeNow()); err != nil {
		return TodayDayShape{}, err
	}
	shape, err := derivedRead(a, derivedTodayDayShape, dateISO, func() (TodayDayShape, error) {
		return a.todayDayShapeNowFor(day)
	}, nil)
	if err == nil {
		// The clock moves; the snapshot does not carry it.
		shape.Now = a.todayDayShapeNow().In(day.Location()).Format(time.RFC3339)
	}
	return shape, err
}

func (a *App) todayDayShapeNowFor(day time.Time) (TodayDayShape, error) {
	var brief MobileBriefV1
	if a != nil && a.store != nil {
		if svc, ok := a.ensureMobileBriefService().(*mobileBriefService); ok && svc != nil && svc.store != nil {
			// The strip reads only the stored closing question. The delivered
			// projection (storedLatestForLaunch) and today's live signal
			// refresh (LatestMobileBrief) both re-run the expensive delivery
			// path before todayDayShapeAt applies it again, so the day shape
			// resolves well after the brief and the strip flashes its
			// placeholder. Read the stored brief directly and let
			// todayDayShapeAt apply the delivery gates exactly once.
			brief, _ = svc.store.latest()
			now := a.todayDayShapeNow()
			if current, found := svc.store.currentToday(); found && current.LocalDate == now.In(time.Local).Format("2006-01-02") && brief.LocalDate != current.LocalDate {
				brief = current
			}
		}
	}
	derived := a.derivedViews()
	if a != nil && !derived.active() {
		// Establish the update baseline without delaying the strip read: the
		// fingerprint computes both days, which would otherwise double the
		// work of this read. noteTodayDayShapeUpdate already tolerates an
		// unset baseline. With snapshots, the scheduled build publishes
		// changes instead.
		go a.primeTodayDayShapeUpdates()
	}
	now := a.todayDayShapeNow()
	shape, err := a.todayDayShapeAt(day, now, brief)
	if err == nil && a != nil && a.store != nil && shape.CalendarConnected {
		a.markTodayHeldEvents(&shape, day, now)
	}
	if err == nil && derived.active() {
		shape.Now = ""
		if !shape.CalendarConnected {
			derived.dayShapeDisconnected.Store(true)
		}
	}
	return shape, err
}

func (a *App) todayDayShapeAt(day, now time.Time, brief MobileBriefV1) (TodayDayShape, error) {
	if err := validateTodayDayShapeDate(day, now); err != nil {
		return TodayDayShape{}, err
	}
	return a.todayDayShapeForDay(day, now, brief)
}

// GetHoldDayShape uses the same privacy gates as the Today strip, but accepts
// the full proposal window. It does not read or publish a brief hold offer.
func (a *App) GetHoldDayShape(dateISO string) (TodayDayShape, error) {
	now := a.todayDayShapeNow()
	day, err := time.ParseInLocation("2006-01-02", dateISO, time.Local)
	if err != nil {
		return TodayDayShape{}, &TodayDayShapeDateError{}
	}
	local := now.In(time.Local)
	today := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.Local)
	if day.Before(today) || !day.Before(today.AddDate(0, 0, 8)) {
		return TodayDayShape{}, &TodayDayShapeDateError{}
	}
	shape, err := a.todayDayShapeForDay(day, now, MobileBriefV1{})
	if err == nil && a != nil && a.store != nil && shape.CalendarConnected {
		a.markTodayHeldEvents(&shape, day, now)
	}
	return shape, err
}

func (a *App) todayDayShapeForDay(day, now time.Time, brief MobileBriefV1) (TodayDayShape, error) {
	loc := day.Location()
	from := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, loc)
	to := from.AddDate(0, 0, 1)
	out := shapeTodayCalendar(nil, from, now)
	if a == nil || a.store == nil {
		return out, nil
	}
	ctx := context.Background()
	policy, err := a.mcpReadPrivacy(ctx)
	if err != nil || !policy.calendar {
		return out, nil
	}
	subjects, err := mobileBriefMailSubjects(a)
	if err != nil {
		return out, nil
	}
	rows, err := a.store.ListCalendarEvents(ctx, from.UnixMilli(), to.UnixMilli(), false)
	if err != nil {
		return out, err
	}
	// A successful import proves a connection even on an empty day. Merely
	// detecting a calendar website does not prove its events have been read.
	connected, err := a.todayCalendarConnected(ctx)
	if err != nil {
		return out, err
	}
	events := []calendar.Event{}
	for _, row := range rows {
		if !policy.calendarAllowed(row) || mobileBriefValueEchoesMail(row, subjects) {
			continue
		}
		connected = true
		ev := calendarEventFromRow(row)
		if ev.AllDay || !ev.End.After(ev.Start) {
			continue
		}
		events = append(events, ev)
	}
	out = shapeTodayCalendar(events, from, now)
	out.CalendarConnected = connected
	briefDay, briefDayErr := todayDayShapeBriefDay(brief)
	if connected && briefDayErr == nil && briefDay.Format("2006-01-02") == from.Format("2006-01-02") {
		readable := a.mobileBriefForDelivery(brief)
		question := mobileBriefClosingParagraph(readable)
		if question.Hold != nil && a.todayHoldCalendarReadable(ctx, policy, readable, question) {
			out.Hold, _ = validatedTodayCalendarHold(question.Hold, from, now, out)
		}
	}
	return out, nil
}

// A question's calendar dependencies must pass the same current row gates as
// the strip. A stored closing is no authority to reveal a withdrawn event.
func (a *App) todayHoldCalendarReadable(ctx context.Context, policy *mcpReadPrivacy, brief MobileBriefV1, question MobileBriefPara) bool {
	byRef := mobileBriefEvidenceByRef(brief)
	for _, id := range question.Refs {
		ref := byRef[id]
		switch ref.SourceKind {
		case mobileBriefCalendarSource:
			row, found, err := a.store.GetCalendarEvent(ctx, ref.SourceID)
			if err != nil || !found || !policy.calendarAllowed(row) {
				return false
			}
		case mobileBriefDayShapeSource:
			day, err := time.ParseInLocation("2006-01-02", ref.SourceID, mobileBriefNarrativeLocation(brief))
			if err != nil {
				return false
			}
			rows, err := a.store.ListCalendarEvents(ctx, day.UnixMilli(), day.AddDate(0, 0, 1).UnixMilli(), false)
			if err != nil || len(rows) == 0 {
				return false
			}
			for _, row := range rows {
				if !policy.calendarAllowed(row) {
					return false
				}
			}
		}
	}
	return true
}

func (a *App) todayCalendarConnected(ctx context.Context) (bool, error) {
	synced, err := a.store.GetUIState(ctx, calendarLastSyncKey)
	if err != nil {
		return false, err
	}
	if at, err := time.Parse(time.RFC3339, synced); err == nil && !at.IsZero() {
		return true, nil
	}
	sources, err := a.store.ListCalendarSources(ctx)
	if err != nil {
		return false, err
	}
	for _, source := range sources {
		if source.LastReadAt > 0 {
			return true, nil
		}
	}
	return false, nil
}

func shapeTodayCalendar(events []calendar.Event, day, now time.Time) TodayDayShape {
	loc := day.Location()
	start := time.Date(day.Year(), day.Month(), day.Day(), 8, 0, 0, 0, loc)
	end := time.Date(day.Year(), day.Month(), day.Day(), 19, 0, 0, 0, loc)
	calendar.SortByStart(events)
	out := TodayDayShape{Now: now.In(loc).Format(time.RFC3339), Events: []TodayDayShapeEvent{}}
	for _, ev := range events {
		if ev.AllDay || ev.Status == "canceled" || ev.MyStatus == calendar.StatusDeclined || !ev.End.After(ev.Start) {
			continue
		}
		if before := ev.Start.Add(-time.Hour); before.Before(start) {
			start = before
		}
		if after := ev.End.Add(time.Hour); after.After(end) {
			end = after
		}
		short, shortest, initial := todayEventNames(ev)
		item := TodayDayShapeEvent{
			ID: ev.ID, Start: ev.Start.In(loc).Format(time.RFC3339), End: ev.End.In(loc).Format(time.RFC3339),
			Title: ev.Title, ShortName: short, ShortestName: shortest, Initial: initial, Kind: todayEventKind(ev),
		}
		// The same rule as a private event in a calendar conflict.
		if strings.TrimSpace(ev.Title) == "" || proactiveVoiceRestrictedText(ev.Title) {
			item.Title, item.ShortName, item.ShortestName, item.Initial, item.Private = "", "", "", "", true
		}
		out.Events = append(out.Events, item)
	}
	out.RangeStart, out.RangeEnd = start.Format(time.RFC3339), end.Format(time.RFC3339)
	return out
}

func todayEventKind(ev calendar.Event) string {
	// These calendar names explicitly identify the user's own time. Do not
	// infer personal status from an attendee's identity or a private flag.
	switch strings.ToLower(strings.TrimSpace(ev.CalendarTitle)) {
	case "personal", "个人", "私人":
		return "personal"
	}
	if !ev.HasOthers() {
		return "personal"
	}
	return "meeting"
}

var todayDayShapeURLToken = regexp.MustCompile(`(?i)^(?:https?://|www\.|[a-z0-9][a-z0-9.-]*\.[a-z]{2,}(?:/|$))`)

func todayEventNames(ev calendar.Event) (string, string, string) {
	title := strings.Join(strings.Fields(ev.Title), " ")
	oneToOne := strings.Contains(strings.ToLower(title), "1:1") || strings.Contains(strings.ToLower(title), "1/1")
	attendees := make([]calendar.Attendee, 0, len(ev.Attendees))
	for _, attendee := range ev.Attendees {
		if !attendee.IsMe {
			attendees = append(attendees, attendee)
		}
	}
	personal := todayEventKind(ev) == "personal"
	if oneToOne || !personal && len(attendees) == 1 {
		name := ""
		if len(attendees) == 1 {
			name = todayAttendeeFirstName(attendees[0])
		}
		if name == "" && oneToOne {
			name = todayOneToOneTitleName(title)
		}
		if name != "" {
			return name, name, todayInitial(name)
		}
		if oneToOne {
			return "", "", ""
		}
	}
	if personal {
		words := strings.Fields(title)
		kept := make([]string, 0, len(words))
		for _, word := range words {
			if !todayDayShapeLinkToken(word) {
				kept = append(kept, word)
			}
		}
		short := strings.Join(kept, " ")
		return short, todayFirstWord(short), ""
	}
	ownWords := map[string]bool{}
	for _, attendee := range ev.Attendees {
		if attendee.IsMe {
			for _, word := range strings.Fields(attendee.Name) {
				ownWords[todayEventNameWord(word)] = true
			}
		}
	}
	removeWords := map[string]bool{
		"isoform": true, "weekly": true, "daily": true, "biweekly": true,
		"monthly": true, "meeting": true,
	}
	var kept []string
	for _, word := range strings.Fields(title) {
		plain := strings.Trim(word, ".,;:!?()[]{}<>\"'")
		if plain == "" || todayDayShapeLinkToken(plain) {
			continue
		}
		normalized := todayEventNameWord(plain)
		if removeWords[normalized] || ownWords[normalized] {
			continue
		}
		kept = append(kept, word)
	}
	short := strings.Join(kept, " ")
	for _, separator := range []string{" | ", " - ", ": "} {
		if before, _, ok := strings.Cut(short, separator); ok && before != "" {
			short = before
		}
	}
	short = strings.Trim(short, " \t\r\n,.;:-|!?()[]{}\"'")
	short = strings.Join(strings.Fields(short), " ")
	return short, todayFirstWord(short), ""
}

func todayAttendeeFirstName(attendee calendar.Attendee) string {
	if fields := strings.Fields(attendee.Name); len(fields) > 0 {
		name := strings.Trim(fields[0], ".,;:!?()[]{}<>\"'")
		if name != "" && !todayDayShapeLinkToken(name) {
			return name
		}
	}
	local, _, ok := strings.Cut(strings.TrimSpace(attendee.Email), "@")
	if !ok {
		return ""
	}
	first := strings.SplitN(local, ".", 2)[0]
	first = strings.Trim(first, "._-+")
	if first == "" {
		return ""
	}
	return todayUpperFirst(first)
}

func todayOneToOneTitleName(title string) string {
	words := strings.Fields(title)
	for i, word := range words {
		if strings.EqualFold(strings.Trim(word, ".,;:!?()[]{}<>\"'"), "with") && i+1 < len(words) {
			name := strings.Trim(words[i+1], ".,;:!?()[]{}<>\"'")
			if todayDayShapeLinkToken(name) {
				if !strings.Contains(name, "@") {
					return ""
				}
				return todayAttendeeFirstName(calendar.Attendee{Email: name})
			}
			return name
		}
	}
	return ""
}

func todayInitial(name string) string {
	first, _ := utf8.DecodeRuneInString(name)
	if first == utf8.RuneError {
		return ""
	}
	return strings.ToUpper(string(first))
}

func todayUpperFirst(value string) string {
	first, size := utf8.DecodeRuneInString(value)
	if first == utf8.RuneError {
		return value
	}
	return strings.ToUpper(string(first)) + value[size:]
}

func todayEventNameWord(word string) string {
	return strings.ReplaceAll(strings.ToLower(strings.Trim(word, ".,;:!?()[]{}<>\"'")), "-", "")
}

func todayDayShapeLinkToken(word string) bool {
	plain := strings.Trim(word, ".,;:!?()[]{}<>\"'")
	return strings.Contains(plain, "@") || todayDayShapeURLToken.MatchString(plain)
}

func todayFirstWord(value string) string {
	if words := strings.Fields(value); len(words) > 0 {
		return strings.Trim(words[0], ".,;:")
	}
	return ""
}

// Validate absolute endpoints against the brief's local calendar day and
// the current acceptance or publication clock. Withholding never moves time
// or rewrites the stored question.
func validatedTodayHold(hold *TodayDayShapeHold, day, asOf time.Time) (*TodayDayShapeHold, error) {
	if hold == nil {
		return nil, nil
	}
	start, startErr := time.Parse(time.RFC3339, hold.Start)
	end, endErr := time.Parse(time.RFC3339, hold.End)
	label := strings.TrimSpace(hold.Label)
	from := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, day.Location())
	if startErr != nil || endErr != nil || !end.After(start) || start.Before(from) || end.After(from.AddDate(0, 0, 1)) ||
		!asOf.IsZero() && start.Before(asOf) || label == "" || utf8.RuneCountInString(label) > 80 ||
		strings.ContainsAny(label, "\n\r") || reEmoji.MatchString(label) || redactThreadOutcomeText(label) != label || !mobileBriefCopySafe(label) {
		return nil, errors.New("closing.hold must have a safe label and a future, positive time range within the brief's local day")
	}
	return &TodayDayShapeHold{Start: start.In(day.Location()).Format(time.RFC3339), End: end.In(day.Location()).Format(time.RFC3339), Label: label}, nil
}

// The strip shows the day the brief talks about: its own day, an evening
// brief included (today-review v1.1.20 R44). The binding's date argument
// always remains explicit.
func todayDayShapeBriefDay(brief MobileBriefV1) (time.Time, error) {
	return time.ParseInLocation("2006-01-02", brief.LocalDate, mobileBriefNarrativeLocation(brief))
}

// Keep the phone's legacy activity statistics, adding the identical strip
// fields only at delivery. Live calendar data is never frozen into the brief.
func (a *App) mobileBriefWithDayShape(brief MobileBriefV1) MobileBriefV1 {
	day, err := todayDayShapeBriefDay(brief)
	if err != nil || validateTodayDayShapeDate(day, a.todayDayShapeNow()) != nil {
		return mobileBriefWithoutLiveDayShape(brief)
	}
	if a != nil {
		a.primeTodayDayShapeUpdates()
	}
	shape, err := a.todayDayShapeAt(day, a.todayDayShapeNow(), brief)
	if err != nil {
		var invalid *TodayDayShapeDateError
		if errors.As(err, &invalid) {
			return mobileBriefWithoutLiveDayShape(brief)
		}
		shape = shapeTodayCalendar(nil, day, a.todayDayShapeNow())
	}
	legacy := MobileBriefDay{Hours: []MobileBriefHour{}, TopApps: []MobileBriefAppTime{}}
	if brief.DayShape != nil {
		legacy = *brief.DayShape
	}
	legacy.TodayDayShape = &shape
	brief.DayShape = &legacy
	return brief
}

// Use the service's injected clock for delivery too. Reading a brief must not
// use the reference captured before its model call as the current clock.
func (a *App) todayDayShapeNow() time.Time {
	if a != nil {
		if svc, ok := a.mobileBriefSource().(*mobileBriefService); ok && svc.now != nil {
			return svc.now()
		}
	}
	return time.Now()
}

func validatedTodayCalendarHold(hold *TodayDayShapeHold, day, now time.Time, shape TodayDayShape) (*TodayDayShapeHold, error) {
	validated, err := validatedTodayHold(hold, day, now)
	if err != nil || validated == nil {
		return validated, err
	}
	if !shape.CalendarConnected {
		return nil, errors.New("closing.hold requires a readable connected calendar")
	}
	start, _ := time.Parse(time.RFC3339, validated.Start)
	end, _ := time.Parse(time.RFC3339, validated.End)
	for _, event := range shape.Events {
		eventStart, _ := time.Parse(time.RFC3339, event.Start)
		eventEnd, _ := time.Parse(time.RFC3339, event.End)
		if start.Before(eventEnd) && end.After(eventStart) {
			return nil, errors.New("closing.hold overlaps a readable calendar event; choose an available interval")
		}
	}
	return validated, nil
}

// Called after every model reply, inside the existing two-attempt retry loop.
// Source reads are live and gated, and the clock is sampled after the call.
func (a *App) validateMobileBriefQuestionHold(brief MobileBriefV1, paragraphs []MobileBriefPara, now time.Time) error {
	written := brief
	written.Narrative = paragraphs
	hold := mobileBriefClosingParagraph(written).Hold
	if hold == nil {
		return nil
	}
	day, err := todayDayShapeBriefDay(brief)
	if err == nil {
		var shape TodayDayShape
		shape, err = a.todayDayShapeAt(day, now, MobileBriefV1{})
		if err == nil {
			_, err = validatedTodayCalendarHold(hold, day, now, shape)
		}
	}
	if err != nil {
		return mobileBriefNarrativeRuleErr(mobileBriefRuleClosing, err.Error())
	}
	return nil
}

// Historical briefs retain their prose and activity statistics, with no live
// strip fields outside the computer's supported today/tomorrow window.
func mobileBriefWithoutLiveDayShape(brief MobileBriefV1) MobileBriefV1 {
	if brief.DayShape != nil && brief.DayShape.TodayDayShape != nil {
		legacy := *brief.DayShape
		legacy.TodayDayShape = nil
		brief.DayShape = &legacy
	}
	return brief
}
