package main

import (
	"regexp"
	"strings"
	"testing"
	"time"
)

var dayAnchorWords = regexp.MustCompile(`(?i)\b(?:today|tomorrow|yesterday|monday|tuesday|wednesday|thursday|friday|saturday|sunday)\b`)

func dayAnchorWordsOf(brief MobileBriefV1) string {
	var words []string
	for _, para := range brief.Narrative {
		words = append(words, dayAnchorWords.FindAllString(para.Text, -1)...)
	}
	return strings.ToLower(strings.Join(words, " "))
}

// 2026-11-15: the morning brief was made ahead at 21:52 the evening before,
// then the slot's own build replaced it at 06:51. The day words were anchored
// on the first run recorded under the ID, the evening copy's, so every day word
// moved back one day ("Start yesterday with your calf raises", Monday's
// interview "today"), and those words were saved with the recording. A run older
// than the brief is another build's; the day the words are written for
// (briefWordsDay, the brief's LocalDate) anchors them.
func TestBriefDayWordsAnchorOnTheirOwnBuild(t *testing.T) {
	loc, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatal(err)
	}
	built := time.Date(2026, 11, 15, 6, 50, 51, 0, loc)
	madeAhead := time.Date(2026, 11, 14, 21, 52, 17, 0, loc)
	brief := MobileBriefV1{
		BriefID: "2026-11-15-morning", Slot: mobileBriefSlotMorning, LocalDate: "2026-11-15",
		AsOf: built.UTC().Format(time.RFC3339), Timezone: "America/Los_Angeles", NarrativeStatus: "ok",
		Narrative: []MobileBriefPara{
			{Text: "A lot moved yesterday."},
			{Text: "Start today with your calf raises."},
			{Text: "The interview is tomorrow at 3:00 PM."},
		},
	}
	for _, tc := range []struct {
		name string
		now  time.Time
		want string
	}{
		{"read that morning", built.Add(10 * time.Minute), "yesterday today tomorrow"},
		{"read that evening, after a restore", time.Date(2026, 11, 15, 19, 30, 0, 0, loc), "yesterday today tomorrow"},
		{"read the next morning", time.Date(2026, 11, 16, 8, 0, 0, 0, loc), "saturday yesterday today"},
	} {
		// The made-ahead copy's run (madeAhead) plays no part: the words are
		// about the brief's LocalDate (brief_clock.go).
		_ = madeAhead
		anchor := briefWordsDay(brief)
		read := mobileBriefRelativeDaysAtRead(brief, tc.now, anchor, loc, "English")
		if got := dayAnchorWordsOf(read); got != tc.want {
			t.Errorf("%s: day words %q, want %q", tc.name, got, tc.want)
		}
	}
}

// The whole read: a brief whose ID also carries the made-ahead copy's earlier
// run reads with the day words it was written with.
func TestBriefReadKeepsDayWordsWhenAnEarlierCopyRanUnderItsID(t *testing.T) {
	app, svc, brief := newStoredMobileBriefPrivacyFixture(t)
	svc.todayRefreshWG.Wait()
	svc.wg.Wait()
	for i := range brief.Narrative {
		brief.Narrative[i].Text = "Start today with the review; the demo is tomorrow."
	}
	asOf := mobileBriefAsOfTime(brief)
	svc.recordRun(mobileBriefRun{BriefID: brief.BriefID, Slot: brief.Slot, ScheduledAt: formatMobileBriefTime(asOf), ScheduledReady: true, Status: mobileBriefRunOK, At: formatMobileBriefTime(asOf.Add(-24 * time.Hour))})
	read := app.mobileBriefForDelivery(brief)
	if len(read.Narrative) == 0 {
		t.Fatal("the fixture brief shows no words")
	}
	if got := dayAnchorWordsOf(read); got != "today tomorrow" {
		t.Fatalf("day words %q after an earlier copy's run, want %q", got, "today tomorrow")
	}
}

// A brief made ahead: written at 21:52 for the next morning's slot. Its words
// are about that morning (its LocalDate), so read that morning they stay as
// written. Anchoring on the time it was made moved them a day.
func TestMadeAheadBriefDayWordsAreAboutItsOwnDay(t *testing.T) {
	loc, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatal(err)
	}
	made := time.Date(2026, 11, 14, 21, 52, 0, 0, loc)
	brief := MobileBriefV1{
		BriefID: "2026-11-15-morning", Slot: mobileBriefSlotMorning, LocalDate: "2026-11-15",
		AsOf: made.UTC().Format(time.RFC3339), Timezone: "America/Los_Angeles", NarrativeStatus: "ok",
		Narrative: []MobileBriefPara{{Text: "Start today with your calf raises; the interview is tomorrow."}},
	}
	if got := briefWordsDay(brief); got.Format("2006-01-02") != "2026-11-15" {
		t.Fatalf("the brief's words day is %s, want its LocalDate", got.Format("2006-01-02"))
	}
	read := mobileBriefRelativeDaysForRead(brief, time.Date(2026, 11, 15, 7, 5, 0, 0, loc), "English")
	if got := dayAnchorWordsOf(read); got != "today tomorrow" {
		t.Fatalf("read that morning: %q, want %q", got, "today tomorrow")
	}
	next := mobileBriefRelativeDaysForRead(brief, time.Date(2026, 11, 16, 7, 5, 0, 0, loc), "English")
	if got := dayAnchorWordsOf(next); got != "yesterday today" {
		t.Fatalf("read the next morning: %q, want %q", got, "yesterday today")
	}
}
