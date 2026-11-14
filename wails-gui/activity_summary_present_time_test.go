package main

import (
	"context"
	"testing"
	"time"

	"wails-gui/internal/platform/activitymon"
)

func TestActivitySummaryPresentTimeSurfaces(t *testing.T) {
	for _, tc := range []struct {
		name        string
		gapMinutes  int
		wantMinutes int
		wantCount   int
	}{
		{"reading", 3, 12, 2},
		{"ten minute gap", 10, 19, 2},
		{"long gap", 11, 9, 2},
		{"locked", 3, 9, 2},
		{"sleep", 3, 9, 2},
		{"screensaver", 3, 9, 2},
		{"paused gap", 3, 9, 2},
		{"paused session", 3, 5, 1},
		{"open pause", 3, 5, 1},
		{"protected", 3, 5, 1},
		{"sensitive", 3, 5, 1},
		{"closed eyes", 3, 0, 0},
		{"unreadable pause log", 3, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := newThreadTestApp(t)
			today, _ := threadTestDay(t)
			day := today.AddDate(0, 0, -(int(today.Weekday())+6)%7)
			nextStart := at(day, 9, 5+tc.gapMinutes)
			var events []activitymon.ActivityEvent
			switch tc.name {
			case "locked", "sleep":
				state := "locked"
				if tc.name == "sleep" {
					state = "sleep"
				}
				events = append(events, activitymon.ActivityEvent{Kind: activitymon.EventSessionState, StateName: state, Timestamp: at(day, 9, 5).UnixMilli()})
			case "screensaver":
				events = append(events, activitymon.ActivityEvent{Kind: activitymon.EventAppSwitch, AppName: "ScreenSaverEngine", AppBundleID: "com.apple.screensaver.engine", Timestamp: at(day, 9, 5).UnixMilli()})
			}
			seedPresentSession(t, app, "first", at(day, 9, 0), at(day, 9, 5), events...)
			seedPresentSession(t, app, "second", nextStart, nextStart.Add(4*time.Minute))
			switch tc.name {
			case "paused gap":
				app.saveObservationPauses([]observationPause{{Start: at(day, 9, 6).UnixMilli(), End: at(day, 9, 7).UnixMilli()}})
			case "paused session":
				app.saveObservationPauses([]observationPause{{Start: nextStart.UnixMilli(), End: nextStart.Add(time.Minute).UnixMilli()}})
			case "open pause":
				app.saveObservationPauses([]observationPause{{Start: at(day, 9, 6).UnixMilli()}})
			case "protected":
				if _, err := app.activity.store.db.Exec(`UPDATE activity_sessions SET triage_status = 'protected' WHERE id = 'second'`); err != nil {
					t.Fatal(err)
				}
			case "sensitive":
				if _, err := app.activity.store.db.Exec(`UPDATE activity_sessions SET app_names = '["Private Notes"]' WHERE id = 'second'`); err != nil {
					t.Fatal(err)
				}
			case "closed eyes":
				app.closedEyes.enabled.Store(true)
			case "unreadable pause log":
				if err := app.store.SetUIState(context.Background(), observationPausesKey, "{broken"); err != nil {
					t.Fatal(err)
				}
			}

			month, err := app.GetActivityMonthStats(day.Format("2006-01"))
			if err != nil {
				t.Fatal(err)
			}
			for _, row := range month {
				if row.Date == day.Format("2006-01-02") && (row.ActiveMs != (time.Duration(tc.wantMinutes)*time.Minute).Milliseconds() || row.SessionCount != tc.wantCount) {
					t.Fatalf("heatmap=%+v want %d minutes, %d sessions", row, tc.wantMinutes, tc.wantCount)
				}
			}
			want := (time.Duration(tc.wantMinutes) * time.Minute).Milliseconds()
			daily, err := app.GetDailySummary(day.Format("2006-01-02"))
			if err != nil {
				t.Fatal(err)
			}
			week, err := app.GetWeeklySummary(day.Format("2006-01-02"))
			if err != nil {
				t.Fatal(err)
			}
			// DailyStats also supplies the calendar heatmap through useCalendarData.
			row := week.DailyStats[0]
			if daily.TotalActiveMs != want || row.ActiveMs != want || week.TotalActiveMs != want || week.DailyAvgMs != want {
				t.Fatalf("Day=%d Week row=%d total=%d average=%d, want %d", daily.TotalActiveMs, row.ActiveMs, week.TotalActiveMs, week.DailyAvgMs, want)
			}
			if row.SessionCount != tc.wantCount || row.SessionCount != daily.SessionCount {
				t.Fatalf("Week row sessions=%d Day sessions=%d, want %d", row.SessionCount, daily.SessionCount, tc.wantCount)
			}

			nextWeek := day.AddDate(0, 0, 7).Format("2006-01-02")
			comparison, err := app.GetDailySummary(nextWeek)
			if err != nil {
				t.Fatal(err)
			}
			if comparison.ActiveAvgMs != want || comparison.TotalActiveMs != 0 || comparison.SessionAvg != float64(tc.wantCount) {
				t.Fatalf("7-day comparison=%+v, want average %d, session average %d and today's total 0", comparison, want, tc.wantCount)
			}
			following, err := app.GetWeeklySummary(nextWeek)
			if err != nil {
				t.Fatal(err)
			}
			if following.PrevWeekMs != week.TotalActiveMs {
				t.Fatalf("previous week=%d, want Week total %d", following.PrevWeekMs, week.TotalActiveMs)
			}

			input := mobileBriefInput{AsOf: at(day, 12, 0), Location: time.Local}
			collectMobileBriefSessions(app, &input)
			if want == 0 {
				if input.DayShape != nil {
					t.Fatalf("unreadable phone day shape = %+v", input.DayShape)
				}
			} else if input.DayShape == nil || input.DayShape.ActiveMinutes != tc.wantMinutes {
				t.Fatalf("phone day shape=%+v, want %d minutes", input.DayShape, tc.wantMinutes)
			}
		})
	}
}

func TestActivitySummarySevenDayAverageUsesActiveDays(t *testing.T) {
	app := newThreadTestApp(t)
	day, _ := threadTestDay(t)
	seedPresentSession(t, app, "first", at(day, 9, 0), at(day, 9, 5))
	seedPresentSession(t, app, "second", at(day, 9, 8), at(day, 9, 12))
	other := day.AddDate(0, 0, 2)
	seedPresentSession(t, app, "third", at(other, 9, 0), at(other, 9, 20))
	// Outside the lookback and on the comparison day must not enter the average.
	seedPresentSession(t, app, "old", at(day.AddDate(0, 0, -1), 9, 0), at(day.AddDate(0, 0, -1), 10, 0))
	comparisonDay := day.AddDate(0, 0, 7)
	seedPresentSession(t, app, "today", at(comparisonDay, 9, 0), at(comparisonDay, 10, 0))
	data, err := app.GetDailySummary(comparisonDay.Format("2006-01-02"))
	if err != nil {
		t.Fatal(err)
	}
	if data.ActiveAvgMs != (16*time.Minute).Milliseconds() || data.TotalActiveMs != time.Hour.Milliseconds() || data.SessionAvg != 1.5 || data.UniqueAppsAvg != 1 {
		t.Fatalf("7-day average must use the two active days (12 and 20 minutes): %+v", data)
	}
}

func TestActivityWeekPresentTimeHonorsCapturedSources(t *testing.T) {
	app := newThreadTestApp(t)
	day, _ := threadTestDay(t)
	seedPresentSession(t, app, "first", at(day, 9, 0), at(day, 9, 5))
	seedPresentSession(t, app, "second", at(day, 9, 8), at(day, 9, 12))
	date := day.Format("2006-01-02")
	sources, err := app.captureSummarySources(date, "weekly")
	if err != nil {
		t.Fatal(err)
	}
	seedPresentSession(t, app, "later", at(day, 9, 15), at(day, 9, 20))
	daily, err := app.getDailySummary(date, &sources, true)
	if err != nil {
		t.Fatal(err)
	}
	weekly, err := app.getWeeklySummary(date, &sources)
	if err != nil {
		t.Fatal(err)
	}
	if want := (12 * time.Minute).Milliseconds(); daily.TotalActiveMs != want || weekly.TotalActiveMs != want {
		t.Fatalf("captured Day=%d Week=%d, want %d without the later session or its gap", daily.TotalActiveMs, weekly.TotalActiveMs, want)
	}
}

func TestActivitySummaryWeekClipsDaysAcrossMidnightAndDST(t *testing.T) {
	loc, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatal(err)
	}
	previousLocal := time.Local
	time.Local = loc
	t.Cleanup(func() { time.Local = previousLocal })
	for _, date := range []string{"2026-10-19", "2026-03-08", "2026-11-01"} {
		t.Run(date, func(t *testing.T) {
			app := newThreadTestApp(t)
			day, err := time.ParseInLocation("2006-01-02", date, loc)
			if err != nil {
				t.Fatal(err)
			}
			next := day.AddDate(0, 0, 1)
			seedPresentSession(t, app, "before", day.Add(-10*time.Minute), day.Add(5*time.Minute))
			seedPresentSession(t, app, "reading", day.Add(8*time.Minute), day.Add(12*time.Minute))
			seedPresentSession(t, app, "after", next.Add(-5*time.Minute), next.Add(5*time.Minute))
			for _, current := range []time.Time{day.AddDate(0, 0, -1), day, next} {
				week, err := app.GetWeeklySummary(current.Format("2006-01-02"))
				if err != nil {
					t.Fatal(err)
				}
				var sum int64
				for _, row := range week.DailyStats {
					daily, err := app.GetDailySummary(row.Date)
					if err != nil {
						t.Fatal(err)
					}
					if row.ActiveMs != daily.TotalActiveMs {
						t.Fatalf("%s: Week row=%d Day=%d", row.Date, row.ActiveMs, daily.TotalActiveMs)
					}
					sum += daily.TotalActiveMs
				}
				if sum != week.TotalActiveMs {
					t.Fatalf("Week total=%d, Day sum=%d", week.TotalActiveMs, sum)
				}
			}
			daily, err := app.GetDailySummary(date)
			if err != nil {
				t.Fatal(err)
			}
			if daily.TotalActiveMs != (17 * time.Minute).Milliseconds() {
				t.Fatalf("clipped day=%d, want 17 minutes", daily.TotalActiveMs)
			}
			comparison, err := app.GetDailySummary(next.Format("2006-01-02"))
			if err != nil {
				t.Fatal(err)
			}
			if comparison.ActiveAvgMs != (13*time.Minute + 30*time.Second).Milliseconds() {
				t.Fatalf("clipped 7-day average=%d, want (10+17)/2 minutes", comparison.ActiveAvgMs)
			}
		})
	}
}

func TestActivitySummaryWeekDoesNotBridgeMidnight(t *testing.T) {
	app := newThreadTestApp(t)
	day, _ := threadTestDay(t)
	next := day.AddDate(0, 0, 1)
	seedPresentSession(t, app, "before", next.Add(-5*time.Minute), next.Add(-2*time.Minute))
	seedPresentSession(t, app, "after", next.Add(2*time.Minute), next.Add(5*time.Minute))
	for _, current := range []time.Time{day, next} {
		week, err := app.GetWeeklySummary(current.Format("2006-01-02"))
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range week.DailyStats {
			if row.Date == current.Format("2006-01-02") && row.ActiveMs != (3*time.Minute).Milliseconds() {
				t.Fatalf("%s: Week row=%d, want 3 minutes without a cross-day gap", row.Date, row.ActiveMs)
			}
		}
	}
}
