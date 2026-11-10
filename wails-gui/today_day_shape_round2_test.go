package main

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"wails-gui/internal/calendar"
)

func TestTodayDayShapeRound2DateWindow(t *testing.T) {
	app, clock, _, _ := dayShapeHoldFixture(t)
	now := clock.Now().In(time.Local)
	for _, delta := range []int{-1, 2, -30, 30} {
		date := now.AddDate(0, 0, delta).Format("2006-01-02")
		_, err := app.GetTodayDayShape(date)
		var invalid *TodayDayShapeDateError
		if !errors.As(err, &invalid) || err.Error() != "TODAY_DAY_SHAPE_DATE_INVALID" {
			t.Errorf("unsupported date %s did not return the typed error: %v", date, err)
		}
	}
	for _, malformed := range []string{"not-a-date", "2026-10-23T00:00:00Z", "2026-02-30"} {
		_, err := app.GetTodayDayShape(malformed)
		var invalid *TodayDayShapeDateError
		if !errors.As(err, &invalid) {
			t.Errorf("malformed date %s did not return typed error: %v", malformed, err)
		}
	}
	for _, delta := range []int{0, 1} {
		date := now.AddDate(0, 0, delta).Format("2006-01-02")
		if _, err := app.GetTodayDayShape(date); err != nil {
			t.Errorf("supported date %s: %v", date, err)
		}
	}
}

func TestTodayDayShapeRound2TomorrowRenameEmitsOneUpdate(t *testing.T) {
	app, clock, _, _ := dayShapeHoldFixture(t)
	tomorrow := clock.Now().In(time.Local).AddDate(0, 0, 1)
	event := calendar.Event{ID: "tomorrow", Source: calendar.SourceEventKit, Title: "Original title", Start: tomorrow.Add(time.Hour), End: tomorrow.Add(2 * time.Hour)}
	storeEvent(t, app.store, event)
	app.eventBroadcaster = NewEventBroadcaster()
	client := app.eventBroadcaster.AddClient("round2-tomorrow")
	defer app.eventBroadcaster.RemoveClient("round2-tomorrow")
	app.primeTodayDayShapeUpdates()
	event.Title = "Renamed title"
	storeEvent(t, app.store, event)
	app.emitBrowserEvent("calendar:synced")
	app.emitBrowserEvent("calendar:synced")
	if got := countDayShapeUpdates(t, client, 200*time.Millisecond); got != 1 {
		t.Fatalf("tomorrow rename emitted %d updates", got)
	}
}

func TestTodayDayShapeRound2SingleEndpointQuestion(t *testing.T) {
	brief, _ := writtenBriefFixture()
	day, err := todayDayShapeBriefDay(brief)
	if err != nil {
		t.Fatal(err)
	}
	hold := &TodayDayShapeHold{Start: day.Add(11 * time.Hour).Format(time.RFC3339), End: day.Add(12 * time.Hour).Format(time.RFC3339), Label: "Sam memo"}
	for _, test := range []struct{ language, question string }{
		{"English", "Want me to hold 11:00 AM for the note to Sam?"},
		{"English", "Want me to hold time until 12:00 PM for the note to Sam?"},
		{"Chinese", "要我预留上午11点给Sam写跟进消息吗？"},
		{"Chinese", "要我预留时间到中午12点给Sam写跟进消息吗？"},
	} {
		t.Run(test.language+test.question, func(t *testing.T) {
			_, brief.Offers = mobileBriefClosing(brief, test.language)
			closing := &mobileBriefNarrativeReplyClosing{Text: test.question, Card: writtenOfferID(brief, "e7"), Hold: hold}
			got, err := mobileBriefWrittenClosing(brief, closing, test.language)
			if err != nil || !reflect.DeepEqual(got.Hold, hold) {
				t.Fatalf("valid single endpoint refused: %+v err=%v", got, err)
			}
		})
	}
}

func TestTodayDayShapeRound2UnrecognisedPairsCannotUseSourceClocks(t *testing.T) {
	day := dayShapeTestDay(t)
	hold := &TodayDayShapeHold{Start: day.Add(11 * time.Hour).Format(time.RFC3339), End: day.Add(12 * time.Hour).Format(time.RFC3339), Label: "Prep"}
	clocks := map[string]map[string]bool{"e1": {"11:00 AM": true, "12:00 PM": true, "中午12点": true, "上午11点": true}}
	for _, question := range []string{
		"hold 12:00 PM till 11:00 AM", "hold 12:00 PM ~ 11:00 AM", "hold 12:00 PM ～ 11:00 AM",
		"预留中午12点～上午11点", "预留中午12点~上午11点", "预留中午12点至上午11点", "预留中午12点到上午11点",
		"hold 12:00 PM before 11:00 AM", "hold 11:00 AM / 12:00 PM",
	} {
		if !mobileBriefClocksSupported(question, []string{"e1"}, clocks) {
			t.Fatalf("fixture clocks not source-backed: %s", question)
		}
		if mobileBriefClosingClocksSupported(question, []string{"e1"}, clocks, hold, day.Location()) {
			t.Errorf("unvalidated pair accepted: %s", question)
		}
	}
	for _, question := range []string{
		"hold 11:00 AM till 12:00 PM", "hold 11:00 AM ~ 12:00 PM", "hold 11:00 AM ～ 12:00 PM",
		"预留上午11点～中午12点", "预留上午11点~中午12点", "预留上午11点至中午12点", "预留上午11点到中午12点",
	} {
		if !mobileBriefClosingClocksSupported(question, nil, nil, hold, day.Location()) {
			t.Errorf("valid ordered range refused: %s", question)
		}
	}
}

func TestTodayDayShapeRound2HistoricalBriefCannotExpandUpdates(t *testing.T) {
	app, clock, brief, day := dayShapeHoldFixture(t)
	yesterday := day.AddDate(0, 0, -1)
	brief.LocalDate = yesterday.Format("2006-01-02")
	brief.BriefID = mobileBriefID(brief.LocalDate, brief.Slot, yesterday.Add(8*time.Hour))
	brief.AsOf = yesterday.Add(8 * time.Hour).Format(time.RFC3339)
	brief.Narrative[0].Hold = &TodayDayShapeHold{Start: yesterday.Add(11 * time.Hour).Format(time.RFC3339), End: yesterday.Add(12 * time.Hour).Format(time.RFC3339), Label: "Prep"}
	svc := app.mobileBriefSvc.(*mobileBriefService)
	if err := svc.store.save(brief, clock.Now()); err != nil {
		t.Fatal(err)
	}
	event := calendar.Event{ID: "yesterday", Source: calendar.SourceEventKit, Title: "Historical title", Start: yesterday.Add(16 * time.Hour), End: yesterday.Add(17 * time.Hour)}
	storeEvent(t, app.store, event)
	app.eventBroadcaster = NewEventBroadcaster()
	client := app.eventBroadcaster.AddClient("round2-history")
	defer app.eventBroadcaster.RemoveClient("round2-history")
	app.primeTodayDayShapeUpdates()
	event.Title = "Renamed historical event"
	storeEvent(t, app.store, event)
	app.emitBrowserEvent("calendar:synced")
	if count := countDayShapeUpdates(t, client, 200*time.Millisecond); count != 0 {
		t.Fatalf("unsupported historical date emitted %d updates", count)
	}
	// Retain legacy phone statistics, clearing any previously attached strip.
	brief.DayShape = &MobileBriefDay{ActiveMinutes: 42, TodayDayShape: &TodayDayShape{CalendarConnected: true}}
	before := append([]MobileBriefPara(nil), brief.Narrative...)
	phone := app.mobileBriefWithDayShape(brief)
	if phone.DayShape.TodayDayShape != nil || phone.DayShape.ActiveMinutes != 42 || !reflect.DeepEqual(phone.Narrative, before) {
		t.Fatalf("historical phone projection=%+v", phone.DayShape)
	}
	if brief.DayShape.TodayDayShape == nil {
		t.Fatal("phone projection mutated stored strip data")
	}
}

func TestTodayDayShapeRound2SingleQuestionUsesOneAttempt(t *testing.T) {
	for _, test := range []struct{ locale, language, question, body string }{
		{"en", "English", "Want me to hold 11:00 AM for the note to Sam?", writtenShoe},
		{"zh-CN", "Chinese", "要我预留上午11点给Sam写跟进消息吗？", "Sam的鞋垫跟进从上周日起一直未结，我可以起草跟进消息。"},
	} {
		t.Run(test.locale, func(t *testing.T) {
			brief, picks := writtenBriefFixture()
			_, brief.Offers = mobileBriefClosing(brief, test.language)
			day, _ := todayDayShapeBriefDay(brief)
			hold := &TodayDayShapeHold{Start: day.Add(11 * time.Hour).Format(time.RFC3339), End: day.Add(12 * time.Hour).Format(time.RFC3339), Label: "Sam memo"}
			raw := writtenReply(t, &mobileBriefNarrativeReplyClosing{Text: test.question, Card: writtenOfferID(brief, "e7"), Hold: hold}, mobileBriefNarrativeReplyParagraph{Text: test.body, Refs: []string{"e7"}})
			calls := 0
			paragraphs, _, check := writeMobileBriefNarrative(context.Background(), brief, picks, "PROMPT", test.language, func(string) (string, error) { calls++; return raw, nil })
			if check != "" || calls != 1 || len(paragraphs) == 0 || !reflect.DeepEqual(paragraphs[len(paragraphs)-1].Hold, hold) {
				t.Fatalf("check=%s calls=%d paragraphs=%+v", check, calls, paragraphs)
			}
		})
	}
}

func TestTodayDayShapeRound2UnrecognisedPairRetries(t *testing.T) {
	for _, question := range []string{"Want me to hold 12:00 PM till 11:00 AM for the note to Sam?", "要我预留中午12点～上午11点给Sam写跟进消息吗？"} {
		t.Run(question, func(t *testing.T) {
			brief, picks := writtenBriefFixture()
			language := "English"
			if strings.Contains(question, "预留") {
				language = "Chinese"
			}
			_, brief.Offers = mobileBriefClosing(brief, language)
			day, _ := todayDayShapeBriefDay(brief)
			hold := &TodayDayShapeHold{Start: day.Add(11 * time.Hour).Format(time.RFC3339), End: day.Add(12 * time.Hour).Format(time.RFC3339), Label: "Sam memo"}
			raw := writtenReply(t, &mobileBriefNarrativeReplyClosing{Text: question, Card: writtenOfferID(brief, "e7"), Hold: hold}, mobileBriefNarrativeReplyParagraph{Text: writtenShoe, Refs: []string{"e7"}})
			calls := 0
			_, _, check := writeMobileBriefNarrative(context.Background(), brief, picks, "PROMPT", language, func(ask string) (string, error) {
				calls++
				if calls == 2 && !strings.Contains(ask, "Your previous reply could not be used:") {
					t.Fatal("invalid pair bypassed retry")
				}
				return raw, nil
			})
			if calls != 2 || check != mobileBriefRuleClosing {
				t.Fatalf("calls=%d check=%s", calls, check)
			}
		})
	}
}

func TestTodayDayShapeRound2PhoneWindowRecheckedAtPublication(t *testing.T) {
	app, _, brief, day := dayShapeHoldFixture(t)
	calls := 0
	app.mobileBriefSvc.(*mobileBriefService).now = func() time.Time {
		calls++
		if calls == 1 {
			return day.Add(23*time.Hour + 59*time.Minute)
		}
		return day.AddDate(0, 0, 1)
	}
	brief.DayShape = &MobileBriefDay{ActiveMinutes: 42, TodayDayShape: &TodayDayShape{CalendarConnected: true}}
	phone := app.mobileBriefWithDayShape(brief)
	if phone.DayShape.TodayDayShape != nil || phone.DayShape.ActiveMinutes != 42 {
		t.Fatalf("past date reached phone after midnight: %+v", phone.DayShape)
	}
}
