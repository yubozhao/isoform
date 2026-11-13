package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"wails-gui/internal/calendar"
	"wails-gui/internal/engagement"
	"wails-gui/storage"
)

const todayBeforeYouTalkWindow = 2 * time.Hour
const todayBeforeYouTalkDismissedPrefix = "today_before_you_talk_dismissed:"

type TodayBeforeYouTalk struct {
	MeetingID      string `json:"meetingId"`
	MeetingTitle   string `json:"meetingTitle"`
	StartAt        int64  `json:"startAt"`
	EndAt          int64  `json:"endAt"`
	ConferenceURL  string `json:"conferenceUrl,omitempty"`
	PersonID       string `json:"personId"`
	PersonName     string `json:"personName"`
	YouOwe         string `json:"youOwe,omitempty"`
	TheyAsked      string `json:"theyAsked,omitempty"`
	HasUnsentDraft bool   `json:"hasUnsentDraft,omitempty"`
	LastTime       string `json:"lastTime,omitempty"`
	LastTimeAt     int64  `json:"lastTimeAt,omitempty"`
	LastDurationMs int64  `json:"lastDurationMs,omitempty"`
}

func (a *App) GetTodayBeforeYouTalk() (perfValue *TodayBeforeYouTalk, perfErr error) {
	defer logPageBinding("GetTodayBeforeYouTalk", time.Now(), &perfValue)
	return derivedRead(a, derivedBeforeYouTalk, time.Now().Format("2006-01-02"), a.todayBeforeYouTalkNow, nil)
}

func (a *App) todayBeforeYouTalkNow() (*TodayBeforeYouTalk, error) {
	ctx := context.Background()
	stamp, err := a.peopleReadStamp(ctx)
	if err != nil {
		return nil, err
	}
	view, err := a.todayBeforeYouTalkAt(time.Now())
	if err != nil || view == nil {
		return view, err
	}
	release, err := a.lockPeoplePublication(ctx, stamp)
	if err != nil {
		return nil, err
	}
	defer release()
	return view, nil
}

func (a *App) todayBeforeYouTalkAt(now time.Time) (*TodayBeforeYouTalk, error) {
	if a == nil || a.store == nil {
		return nil, errors.New("the store is not ready")
	}
	ctx := context.Background()
	if svc := a.engagementService(); svc != nil {
		prefs := svc.Prefs()
		spec, _ := engagement.Spec(engagement.KindMeetingPrep)
		if !prefs.KindEnabled(spec) || prefs.MutedUntil[engagement.KindMeetingPrep].After(now) {
			return nil, nil
		}
	}
	if a.IsClosedEyesModeEnabled() || !a.isPermissionEnabledContext(ctx, calendarPermissionKey) || !a.peopleAppReadable(ctx, "calendar") || !a.peopleAppReadable(ctx, "com.apple.iCal") {
		return nil, nil
	}
	privacy, err := a.mcpReadPrivacy(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := a.store.ListCalendarEvents(ctx, now.UnixMilli(), now.Add(todayBeforeYouTalkWindow).UnixMilli(), false)
	if err != nil {
		return nil, err
	}
	for _, event := range rows {
		if !todayBeforeYouTalkEventEligible(event, now) || !privacy.calendarAllowed(event) {
			continue
		}
		personID := todayBeforeYouTalkPerson(event)
		if personID == "" {
			continue
		}
		dismissed, err := a.store.GetUIState(ctx, todayBeforeYouTalkDismissedPrefix+event.ID)
		if err != nil {
			return nil, err
		}
		if dismissed == "true" {
			continue
		}
		detail, err := a.getPersonDetail(personID, newPeopleReadGateCache())
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(detail.Name) == "" {
			continue
		}
		view := &TodayBeforeYouTalk{
			MeetingID: event.ID, MeetingTitle: event.Title, StartAt: event.StartAt, EndAt: event.EndAt,
			ConferenceURL: safeConferenceURL(event.ConferenceURL),
			PersonID:      detail.ID, PersonName: detail.Name,
		}
		a.fillTodayBeforeYouTalkPerson(ctx, view, detail)
		a.fillTodayBeforeYouTalkLastTime(ctx, view, now)
		return view, nil
	}
	return nil, nil
}

func todayBeforeYouTalkEventEligible(event storage.CalendarEventRow, now time.Time) bool {
	if event.AllDay || event.DeletedAt != 0 || event.Status == "canceled" || event.MyStatus == calendar.StatusDeclined || event.EndAt <= event.StartAt || !calendarEventFromRow(event).Busy() {
		return false
	}
	nowMs := now.UnixMilli()
	return event.StartAt <= now.Add(todayBeforeYouTalkWindow).UnixMilli() && event.EndAt > nowMs
}

func todayBeforeYouTalkPerson(event storage.CalendarEventRow) string {
	for _, attendee := range event.Attendees {
		if !attendee.IsMe && attendee.Status != calendar.StatusDeclined && strings.TrimSpace(attendee.PrincipalID) != "" {
			return attendee.PrincipalID
		}
	}
	return ""
}

// A selected meeting keeps its own prep even when another meeting becomes
// the first card. All calendar, person and observation gates still apply.
func (a *App) todayBeforeYouTalkMeetingAt(ctx context.Context, meetingID string, now time.Time) (*TodayBeforeYouTalk, error) {
	return a.todayBeforeYouTalkMeeting(ctx, meetingID, now, false)
}

// A phone can inspect the next meeting earlier in the day than the desktop's
// one-hour Before you talk card, under the same source and privacy gates.
func (a *App) todayBeforeYouTalkMeeting(ctx context.Context, meetingID string, now time.Time, anyTimeToday bool) (*TodayBeforeYouTalk, error) {
	if a == nil || a.store == nil || a.IsClosedEyesModeEnabled() || !a.isPermissionEnabledContext(ctx, calendarPermissionKey) || !a.peopleAppReadable(ctx, "calendar") || !a.peopleAppReadable(ctx, "com.apple.iCal") {
		return nil, errTodayChatItemUnavailable
	}
	privacy, err := a.mcpReadPrivacy(ctx)
	if err != nil {
		return nil, err
	}
	event, ok, err := a.store.GetCalendarEvent(ctx, meetingID)
	if err != nil {
		return nil, err
	}
	eligibilityTime := now
	if anyTimeToday && ok {
		local := now.In(time.Local)
		endOfDay := time.Date(local.Year(), local.Month(), local.Day()+1, 0, 0, 0, 0, time.Local)
		start := time.UnixMilli(event.StartAt)
		if !start.Before(endOfDay) || !start.After(now) {
			return nil, errTodayChatItemUnavailable
		}
		if earlier := start.Add(-todayBeforeYouTalkWindow); earlier.After(now) {
			eligibilityTime = earlier
		}
	}
	if !ok || !todayBeforeYouTalkEventEligible(event, eligibilityTime) || !privacy.calendarAllowed(event) {
		return nil, errTodayChatItemUnavailable
	}
	personID := todayBeforeYouTalkPerson(event)
	if personID == "" {
		return nil, errTodayChatItemUnavailable
	}
	detail, err := a.getPersonDetail(personID, newPeopleReadGateCache())
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(detail.Name) == "" {
		return nil, errTodayChatItemUnavailable
	}
	view := &TodayBeforeYouTalk{MeetingID: event.ID, MeetingTitle: event.Title, StartAt: event.StartAt, EndAt: event.EndAt, PersonID: detail.ID, PersonName: detail.Name}
	a.fillTodayBeforeYouTalkPerson(ctx, view, detail)
	a.fillTodayBeforeYouTalkLastTime(ctx, view, now)
	return view, nil
}

func (a *App) fillTodayBeforeYouTalkPerson(ctx context.Context, view *TodayBeforeYouTalk, detail PersonDetail) {
	for _, loop := range detail.Loops {
		switch loop.Kind {
		case "you_owe":
			if view.YouOwe == "" {
				view.YouOwe = strings.TrimSpace(loop.Title)
			}
		case "reply_owed":
			if view.TheyAsked == "" && !view.HasUnsentDraft {
				a.fillTodayBeforeYouTalkReply(ctx, view, loop.ID)
			}
		}
	}
}

func (a *App) fillTodayBeforeYouTalkLastTime(ctx context.Context, view *TodayBeforeYouTalk, now time.Time) {
	if a.activity == nil || a.activity.store == nil {
		return
	}
	privacy, err := a.mcpReadPrivacy(ctx)
	if err != nil {
		return
	}
	events, err := a.store.ListCalendarEvents(ctx, 0, now.UnixMilli()+1, false)
	if err != nil {
		return
	}
	for i := len(events) - 1; i >= 0; i-- {
		event := events[i]
		if event.EndAt > now.UnixMilli() || event.SessionID == "" || !privacy.calendarAllowed(event) {
			continue
		}
		found := false
		for _, attendee := range event.Attendees {
			found = found || !attendee.IsMe && attendee.PrincipalID == view.PersonID
		}
		if !found {
			continue
		}
		session, err := a.activity.store.GetSessionByID(event.SessionID)
		if err != nil || session == nil {
			continue
		}
		policy, err := a.activityReadPrivacyPolicy()
		if err != nil {
			return
		}
		moments, err := a.activity.store.GetActivitiesInRange(session.StartTime, session.EndTime+1)
		if err != nil {
			continue
		}
		moments = a.filterComposerModelCandidates(a.activity.store, moments)
		var latest *ActivityRow
		for _, moment := range moments {
			if !a.peopleActivityReadable(ctx, moment, policy.pauses) || !a.todayBeforeYouTalkMomentInSession(ctx, moment, session.ID) {
				continue
			}
			candidate := moment
			if latest == nil || candidate.StartMs > latest.StartMs {
				latest = &candidate
			}
		}
		if latest != nil && strings.TrimSpace(latest.Title) != "" {
			view.LastTime = strings.TrimSpace(latest.Title)
			view.LastTimeAt = event.StartAt
			view.LastDurationMs = event.EndAt - event.StartAt
		}
		return
	}
}

func (a *App) todayBeforeYouTalkMomentInSession(ctx context.Context, moment ActivityRow, sessionID string) bool {
	for _, runID := range composeParseStrings(moment.RunIDs) {
		run, err := a.activity.store.GetTimelineRunByIDContext(ctx, runID)
		if err != nil || run == nil {
			return false
		}
		for _, candidate := range run.SessionIDs {
			if candidate == sessionID {
				return true
			}
		}
	}
	return false
}

func (a *App) fillTodayBeforeYouTalkReply(ctx context.Context, view *TodayBeforeYouTalk, momentID string) {
	session, err := a.store.LoadMainSession(ctx)
	if err != nil || session == nil {
		return
	}
	row, err := a.store.FindProactiveStreamItem(ctx, session.LocalID, momentID)
	if err != nil || row == nil {
		return
	}
	var card mainChatProactive
	if json.Unmarshal([]byte(row.ToolInput), &card) != nil {
		return
	}
	a.projectProactiveVoiceItem(ctx, &card, a.currentUILanguage())
	if card.MomentID != momentID {
		for _, child := range card.Items {
			if child.MomentID == momentID {
				card = child
				break
			}
		}
	}
	if card.MomentID != momentID || (card.State != "pending" && card.State != "opened") {
		return
	}
	view.TheyAsked = strings.TrimSpace(card.Body)
	view.HasUnsentDraft = card.Draft && card.DraftContent != nil && strings.TrimSpace(card.DraftContent.Text) != ""
}

func (a *App) DismissTodayBeforeYouTalk(meetingID string) error {
	if a == nil || a.store == nil {
		return errors.New("the store is not ready")
	}
	meetingID = strings.TrimSpace(meetingID)
	if meetingID == "" || len(meetingID) > 2048 {
		return errors.New("a meeting is required")
	}
	return a.store.SetUIState(context.Background(), todayBeforeYouTalkDismissedPrefix+meetingID, "true")
}
