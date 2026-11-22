package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"wails-gui/internal/calendar"
)

func (a *App) yourWeekCalendarHold(ctx context.Context, day, now time.Time, subject string) *TodayDayShapeHold {
	if a == nil || a.store == nil || !featureOn(featureCalendarHolds) {
		return nil
	}
	policy, err := a.mcpReadPrivacy(ctx)
	if err != nil || !policy.calendar {
		return nil
	}
	connected, err := a.todayCalendarConnected(ctx)
	if err != nil || !connected {
		return nil
	}
	from := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, day.Location())
	to := from.AddDate(0, 0, 1)
	rows, err := a.store.ListCalendarEvents(ctx, from.UnixMilli(), to.UnixMilli(), false)
	if err != nil {
		return nil
	}
	subjects, err := mobileBriefMailSubjects(a)
	if err != nil {
		return nil
	}
	var events []calendar.Event
	for _, row := range rows {
		if row.Status == "canceled" || row.Status == "cancelled" || row.MyStatus == calendar.StatusDeclined ||
			row.Availability == "free" || row.Availability == "transparent" {
			continue
		}
		if !policy.calendarAllowed(row) || mobileBriefValueEchoesMail(row, subjects) {
			return nil
		}
		if row.AllDay {
			return nil
		}
		if row.StartAt <= 0 || row.EndAt <= row.StartAt {
			return nil
		}
		events = append(events, calendarEventFromRow(row))
	}
	busy := func(start, end time.Time) bool {
		for _, event := range events {
			if start.Before(event.End) && end.After(event.Start) {
				return true
			}
		}
		return false
	}
	for _, hours := range []int{2, 1} {
		for startHour := 9; startHour+hours <= 12; startHour++ {
			start := time.Date(from.Year(), from.Month(), from.Day(), startHour, 0, 0, 0, from.Location())
			end := start.Add(time.Duration(hours) * time.Hour)
			if start.Before(now) || busy(start, end) {
				continue
			}
			return &TodayDayShapeHold{
				Start: start.Format(time.RFC3339),
				End:   end.Format(time.RFC3339),
				Label: strings.TrimSpace(subject),
			}
		}
	}
	return nil
}

func yourWeekHoldAsk(subject string, hold TodayDayShapeHold, lang string) (string, string) {
	start, startErr := time.Parse(time.RFC3339, hold.Start)
	end, endErr := time.Parse(time.RFC3339, hold.End)
	if startErr != nil || endErr != nil {
		return pick(lang, "Start next week on "+subject+"?", "下周从"+subject+"开始？"),
			pick(lang, "Help me plan how to start next week. Don't write to my calendar.",
				"请帮我计划下周如何开始，不要写入日历。")
	}
	weekday := yourWeekWeekdayName(start.Weekday(), lang)
	if lang == "zh" {
		rangeText := yourWeekClockRange(start, end, lang)
		date := fmt.Sprintf("%d月%d日", start.Month(), start.Day())
		return fmt.Sprintf("下周从%s开始？我可以在%s%s留出时间。", subject, weekday, rangeText),
			fmt.Sprintf("请在%s（%s）%s为%s留出时间。不要邀请任何人。", date, weekday, yourWeekExactClockRange(start, end, lang), subject)
	}
	rangeText := yourWeekClockRange(start, end, lang)
	date := start.Format("January 2")
	return fmt.Sprintf("Start next week on %s? I can hold %s %s.", subject, weekday, rangeText),
		fmt.Sprintf("Hold %s, %s, %s for %s. Don't invite anyone.", weekday, date, yourWeekExactClockRange(start, end, lang), subject)
}

func yourWeekClockRange(start, end time.Time, lang string) string {
	if lang == "zh" {
		// 24-hour, no 上午/下午 (today-review v1.1.20 R47): "9点到11点", "13点到15点".
		startClock := strings.TrimSuffix(briefShortClock(start.Hour(), start.Minute(), lang), ":00")
		endClock := strings.TrimSuffix(briefShortClock(end.Hour(), end.Minute(), lang), ":00")
		return startClock + "点到" + endClock + "点"
	}
	startHour := fmt.Sprintf("%d", (start.Hour()+11)%12+1)
	endHour := fmt.Sprintf("%d", (end.Hour()+11)%12+1)
	startPeriod, endPeriod := "AM", "AM"
	if start.Hour() >= 12 {
		startPeriod = "PM"
	}
	if end.Hour() >= 12 {
		endPeriod = "PM"
	}
	if startPeriod != endPeriod {
		return startHour + " " + startPeriod + " to " + endHour + " " + endPeriod
	}
	return startHour + " to " + endHour + " " + endPeriod
}

func yourWeekExactClockRange(start, end time.Time, lang string) string {
	separator := " to "
	if lang == "zh" {
		separator = "至"
	}
	return briefShortClock(start.Hour(), start.Minute(), lang) + separator +
		briefShortClock(end.Hour(), end.Minute(), lang)
}
