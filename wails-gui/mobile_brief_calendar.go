package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"wails-gui/internal/calendar"
)

// The brief's meetings section, from the calendar. The morning brief lists
// today's events (title, time, place or link, attendees) and the shape of the
// day; the evening brief joins the past events to their meeting sessions and
// transcripts, so the phone reads what happened against what was planned.
const (
	mobileBriefCalendarSource = "calendar"
	mobileBriefDayShapeSource = "calendar_day"
	mobileBriefAttendeeLimit  = 4
)

// collectMobileBriefCalendar adds calendar candidates to in.Meetings.
func collectMobileBriefCalendar(a *App, in *mobileBriefInput) {
	collectMobileBriefCalendarContext(context.Background(), a, in)
}

func collectMobileBriefCalendarContext(ctx context.Context, a *App, in *mobileBriefInput) {
	if a == nil || a.store == nil {
		return
	}
	loc := in.Location
	if loc == nil {
		loc = time.Local
	}
	from, to := mobileBriefCalendarWindow(in.Slot, in.briefDayRef(loc), loc)
	rankedReadAllowed := in.Slot == mobileBriefSlotMorning && a.morningBriefCalendarReadAllowed(ctx)
	if rankedReadAllowed {
		to = to.AddDate(0, 0, 1)
	}
	events, err := a.listCalendarEvents(ctx, from, to)
	if err != nil {
		log.Printf("[mobile-brief] calendar unavailable: %v", err)
		return
	}
	var today, tomorrow []calendar.Event
	tomorrowStart := from.AddDate(0, 0, 1)
	for _, event := range events {
		if mobileBriefUnprovedCalendarNotice(event) {
			continue
		}
		if in.Slot == mobileBriefSlotMorning && !event.Start.Before(tomorrowStart) {
			tomorrow = append(tomorrow, event)
		} else {
			today = append(today, event)
		}
	}
	todayCandidates := mobileBriefCalendarCandidatesContext(ctx, a, today, in.Slot, in.AsOf, loc, in.ClockLayout)
	tomorrowCandidates := mobileBriefCalendarCandidatesContext(ctx, a, tomorrow, in.Slot, in.AsOf, loc, in.ClockLayout)
	if rankedReadAllowed {
		origins := a.morningBriefCalendarOrigins(ctx, from, to)
		for _, candidates := range [][]mobileBriefCandidate{todayCandidates, tomorrowCandidates} {
			for i := range candidates {
				origin := origins[candidates[i].SourceID]
				if origin.SourceDigest != "" && origin.SourceDigest == candidates[i].rankedSourceDigest {
					candidates[i].rankedOriginDigest = origin.OriginDigest
				}
			}
		}
	}
	in.Meetings = append(in.Meetings, todayCandidates...)
	in.TomorrowMeetings = append(in.TomorrowMeetings, tomorrowCandidates...)
	if in.Slot != mobileBriefSlotEvening {
		shape := calendar.ShapeDay(today, in.AsOf, loc, in.AsOf)
		if line := mobileBriefDayShapeLine(shape, in.AsOf, loc, mobileBriefVoiceLang(in.Language)); len(shape.Events) > 0 && line != "" {
			in.Meetings = append(in.Meetings, mobileBriefCandidate{
				SourceKind: mobileBriefDayShapeSource,
				SourceID:   from.Format("2006-01-02"),
				Title:      "The shape of today",
				Detail:     line,
				Status:     "day_shape",
				Kind:       "day_shape",
				At:         from,
			})
		}
	}
}

// mobileBriefDayShapeLine says what is left of today's calendar the way a
// person would ("Your 10am 1:1 with Priya and Alex overlaps the Halcyon
// Spanish lesson, Maya C."). Only time still ahead appears, so no past clock
// is written and nothing is left as an empty fragment. Events are today's, so
// no weekday is named. English says titles plainly for speech; Chinese quotes them.
func mobileBriefDayShapeLine(shape calendar.DayShape, asOf time.Time, loc *time.Location, lang string) string {
	if loc == nil {
		loc = time.Local
	}
	clock := func(t time.Time) string {
		local := t.In(loc)
		return briefShortClock(local.Hour(), local.Minute(), lang)
	}
	noun := func(title string) string { return mobileBriefNounSubject(title, lang) }
	// A title may already end a sentence ("Maya C."); never double it.
	sentence := func(text string) string {
		if strings.HasSuffix(text, ".") || strings.HasSuffix(text, ".”") || strings.HasSuffix(text, "。”") {
			return text
		}
		return text + pick(lang, ".", "。")
	}
	var parts []string
	clashes := map[string]bool{}
	for _, c := range shape.Conflicts {
		// Name the meeting that has not started; its clock is still ahead.
		ahead, other := c.First, c.Second
		if !ahead.Start.After(asOf) {
			ahead, other = c.Second, c.First
		}
		if !ahead.Start.After(asOf) || strings.TrimSpace(ahead.Title) == "" || strings.TrimSpace(other.Title) == "" {
			continue
		}
		clashes[c.First.ID], clashes[c.Second.ID] = true, true
		parts = append(parts, sentence(pick(lang,
			"Your "+clock(ahead.Start)+" "+mobileBriefSpokenTitle(ahead.Title)+" overlaps "+noun(other.Title),
			clock(ahead.Start)+"的"+noun(ahead.Title)+"和"+noun(other.Title)+"时间冲突")))
	}
	for i := range shape.Events {
		ev := shape.Events[i]
		if ev.AllDay || !ev.Busy() || !ev.IsMeetingLike() || !ev.Start.After(asOf) || strings.TrimSpace(ev.Title) == "" {
			continue
		}
		// A meeting already named in a clash is not repeated.
		if !clashes[ev.ID] {
			first := shape.FirstMeeting != nil && shape.FirstMeeting.ID == ev.ID
			line := pick(lang, "Your next meeting is "+noun(ev.Title)+" at "+clock(ev.Start), "下一个会议是"+clock(ev.Start)+"的"+noun(ev.Title))
			if first {
				line = pick(lang, "Your first meeting is "+noun(ev.Title)+" at "+clock(ev.Start), "今天第一个会议是"+clock(ev.Start)+"的"+noun(ev.Title))
			}
			parts = append([]string{sentence(line)}, parts...)
		}
		break
	}
	for _, block := range shape.BackToBack {
		if !block.End.After(asOf) {
			continue
		}
		if block.Start.After(asOf) {
			parts = append(parts, sentence(pick(lang,
				fmt.Sprintf("You have %d meetings back to back from %s to %s", block.Events, clock(block.Start), clock(block.End)),
				fmt.Sprintf("%s到%s连着%d个会", clock(block.Start), clock(block.End), block.Events))))
		} else {
			parts = append(parts, sentence(pick(lang, "Your back-to-back meetings run until "+clock(block.End), "连续的会议一直到"+clock(block.End))))
		}
	}
	var free []string
	for _, block := range shape.FreeBlocks {
		if !block.End.After(asOf) || len(free) == 3 {
			continue
		}
		if block.Start.After(asOf) {
			free = append(free, pick(lang, clock(block.Start)+" to "+clock(block.End), clock(block.Start)+"到"+clock(block.End)))
		} else {
			free = append(free, pick(lang, "now until "+clock(block.End), "从现在到"+clock(block.End)))
		}
	}
	if len(free) > 0 {
		joined := strings.Join(free, pick(lang, ", ", "、"))
		if lang != "zh" {
			joined = mobileBriefJoinAnd(free)
		}
		parts = append(parts, sentence(pick(lang, "You're free "+joined, "空闲时段是"+joined)))
	}
	if n := len(shape.PendingInvites); n == 1 && strings.TrimSpace(shape.PendingInvites[0].Title) != "" {
		parts = append(parts, sentence(pick(lang, mobileBriefCapitalize(noun(shape.PendingInvites[0].Title))+" invite still needs your answer", noun(shape.PendingInvites[0].Title)+"还在等你回复邀请")))
	} else if n > 1 {
		parts = append(parts, sentence(pick(lang, fmt.Sprintf("%d invites still need your answer", n), fmt.Sprintf("%d个邀请还在等你回复", n))))
	}
	return strings.Join(parts, pick(lang, " ", ""))
}

// mobileBriefCalendarWindow is shared by the brief collector and exact-source
// Today chat reads, so every visible meeting remains eligible for its draft.
func mobileBriefCalendarWindow(slot string, asOf time.Time, loc *time.Location) (time.Time, time.Time) {
	if loc == nil {
		loc = time.Local
	}
	localAsOf := asOf.In(loc)
	from := time.Date(localAsOf.Year(), localAsOf.Month(), localAsOf.Day(), 0, 0, 0, 0, loc)
	to := from.AddDate(0, 0, 1)
	if slot == mobileBriefSlotEvening {
		// Review the whole day and plan for tomorrow, including meetings
		// before the last on-demand brief.
		to = to.AddDate(0, 0, 1)
	}
	return from, to
}

// mobileBriefCalendarCandidates turns events into candidates. Past events
// with a meeting session say so, with the decision count when the notes have
// been read.
func mobileBriefCalendarCandidates(a *App, events []calendar.Event, slot string, asOf time.Time, loc *time.Location, clock string) []mobileBriefCandidate {
	return mobileBriefCalendarCandidatesContext(context.Background(), a, events, slot, asOf, loc, clock)
}

func mobileBriefCalendarCandidatesContext(ctx context.Context, a *App, events []calendar.Event, slot string, asOf time.Time, loc *time.Location, clock string) []mobileBriefCandidate {
	if loc == nil {
		loc = time.UTC
	}
	out := make([]mobileBriefCandidate, 0, len(events))
	for _, ev := range events {
		if ctx.Err() != nil {
			return nil
		}
		if ev.Status == "canceled" || ev.MyStatus == calendar.StatusDeclined || mobileBriefUnprovedCalendarNotice(ev) {
			continue
		}
		status := "upcoming"
		if !ev.End.After(asOf) {
			status = "done"
		} else if ev.Start.Before(asOf) {
			status = "now"
		}
		// A personal event already underway is not a change to report.
		if mobileBriefCalendarAlreadyUnderway(ev, asOf) {
			continue
		}
		parts := []string{}
		if status == "upcoming" {
			language := "en"
			if clock == "15:04" {
				language = "zh"
			}
			span := "All day"
			if !ev.AllDay {
				start, end := ev.Start.In(loc), ev.End.In(loc)
				span = briefShortClock(start.Hour(), start.Minute(), language) + " to " + briefShortClock(end.Hour(), end.Minute(), language)
			}
			parts = append(parts, span)
		}
		if place := calendar.Place(ev); place != "" {
			parts = append(parts, place)
		}
		if names := calendar.AttendeeNames(ev, mobileBriefAttendeeLimit); names != "" {
			parts = append(parts, names)
		}
		if ev.MyStatus == calendar.StatusPending {
			parts = append(parts, "invite not answered")
		}
		if status == "done" && a != nil && a.store != nil {
			parts = append(parts, mobileBriefCalendarOutcomeContext(ctx, a, ev)...)
		}
		var attendees []string
		for _, person := range ev.Attendees {
			name := mobileBriefText(strings.Join(strings.Fields(person.Name), " "), mobileBriefLabelLimit)
			if person.IsMe || name == "" || strings.Contains(name, "@") || !mobileBriefCopySafe(name) || redactThreadOutcomeText(name) != name {
				continue
			}
			attendees = append(attendees, name)
			if len(attendees) == mobileBriefAttendeeLimit {
				break
			}
		}
		out = append(out, mobileBriefCandidate{
			SourceKind:          mobileBriefCalendarSource,
			SourceID:            ev.ID,
			Title:               mobileBriefCalendarName(ev),
			Detail:              strings.Join(parts, "; "),
			Status:              status,
			Kind:                "meeting",
			At:                  ev.Start,
			EndAt:               ev.End,
			URL:                 mobileBriefMeetingLink(ev, status),
			rankedSourceDigest:  morningBriefCalendarSourceDigest(ev),
			rankedAttendees:     attendees,
			rankedInvitePending: ev.MyStatus == calendar.StatusPending,
		})
	}
	return out
}

// mobileBriefMeetingLink is the join link of a meeting that has not ended,
// http or https only.
func mobileBriefMeetingLink(ev calendar.Event, status string) string {
	if status == "done" {
		return ""
	}
	return mobileBriefLink(ev.ConferenceURL)
}

func mobileBriefCalendarOutcomeContext(ctx context.Context, a *App, ev calendar.Event) []string {
	row, found, err := a.store.GetCalendarEvent(ctx, ev.ID)
	if err != nil || !found || row.SessionID == "" {
		return nil
	}
	var parts []string
	if decisions, mine, ok := a.meetingFollowupCounts(ctx, row.SessionID); ok {
		parts = append(parts, fmt.Sprintf("%d decisions, %d follow-ups for you", decisions, mine))
	}
	return parts
}

// A holiday-calendar entry alone establishes a notice, not that it changes
// this person's plans. A separately recorded plan remains eligible.
func mobileBriefUnprovedCalendarNotice(ev calendar.Event) bool {
	calendarName := strings.ToLower(strings.TrimSpace(ev.CalendarTitle))
	return ev.AllDay && (calendarName == "holiday" || calendarName == "holidays" ||
		strings.HasSuffix(calendarName, " holidays") || strings.HasPrefix(calendarName, "holidays in ") ||
		strings.HasSuffix(calendarName, "节假日") || strings.HasSuffix(calendarName, "假日") || strings.HasSuffix(calendarName, "节日"))
}

func mobileBriefCalendarAlreadyUnderway(ev calendar.Event, at time.Time) bool {
	return !ev.AllDay && !at.IsZero() && ev.Start.Before(at) && ev.End.After(at) && !ev.IsMeetingLike()
}

func mobileBriefCalendarName(ev calendar.Event) string {
	title := strings.ToLower(ev.Title)
	if strings.Contains(title, "1:1") || strings.Contains(title, "1/1") {
		return mobileBriefMeetingName(ev)
	}
	return ev.Title
}
