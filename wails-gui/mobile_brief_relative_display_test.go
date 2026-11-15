package main

import (
	"strings"
	"testing"
	"time"
)

func TestBriefV118RelativeDaysAtReadTime(t *testing.T) {
	brief := MobileBriefV1{AsOf: "2026-10-30T16:00:00Z", Timezone: "America/Los_Angeles", Narrative: []MobileBriefPara{{Text: "The meeting is tomorrow. The plan was due yesterday."}}, Audio: &MobileBriefAudio{}}
	now := time.Date(2026, 11, 2, 20, 0, 0, 0, time.UTC)
	got := mobileBriefRelativeDaysForRead(brief, now, "English")
	if got.Narrative[0].Text != "The meeting is last Saturday. The plan was due last Thursday." || got.Audio != nil {
		t.Fatalf("relative reading = %+v", got)
	}
	if brief.Narrative[0].Text != "The meeting is tomorrow. The plan was due yesterday." || brief.Audio == nil {
		t.Fatal("read changed the stored brief")
	}
	brief.Narrative[0].Text = "会议明天举行，计划昨天到期。"
	got = mobileBriefRelativeDaysForRead(brief, now, "Chinese")
	if got.Narrative[0].Text != "会议上周六举行，计划上周四到期。" {
		t.Fatalf("Chinese relative reading = %q", got.Narrative[0].Text)
	}
}

func TestBriefV110UsesReadyTimeAndItemDate(t *testing.T) {
	la, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatal(err)
	}
	ready := time.Date(2026, 11, 1, 23, 30, 0, 0, la)
	read := time.Date(2026, 11, 2, 0, 30, 0, 0, la)
	meeting := time.Date(2026, 11, 3, 9, 0, 0, 0, la)
	brief := MobileBriefV1{AsOf: "2026-11-01T06:30:00Z", Timezone: "America/Los_Angeles", Narrative: []MobileBriefPara{{Text: "Tomorrow at 9:00 AM.", Refs: []string{"event"}}, {Text: "Tomorrow looks busy."}}, Evidence: []MobileBriefRef{{Ref: "event", SourceKind: mobileBriefCalendarSource}}, Sections: MobileBriefSections{Meetings: []MobileBriefItem{{At: meeting.Format(time.RFC3339), Refs: []string{"event"}}}}}
	got := mobileBriefRelativeDaysAtRead(brief, read, ready, la, "English")
	if got.Narrative[0].Text != "Tomorrow at 9:00 AM." || got.Narrative[1].Text != "Today looks busy." {
		t.Fatalf("relative words did not use the item date and ready time: %+v", got.Narrative)
	}
}

func TestBriefV110DoesNotUseActivityObservationAsItemDate(t *testing.T) {
	base := time.Date(2026, 10, 9, 7, 0, 0, 0, time.UTC)
	brief := MobileBriefV1{AsOf: base.Format(time.RFC3339), Timezone: "UTC", Narrative: []MobileBriefPara{{Text: "The plan moved yesterday.", Refs: []string{"work"}}}, Sections: MobileBriefSections{Moved: []MobileBriefItem{{At: base.Format(time.RFC3339), Refs: []string{"work"}}}}}
	got := mobileBriefRelativeDaysAtRead(brief, base.Add(time.Hour), base, time.UTC, "English")
	if got.Narrative[0].Text != brief.Narrative[0].Text {
		t.Fatalf("activity observation changed an unrelated date: %q", got.Narrative[0].Text)
	}
}

// Brief of Nov 14, 2026: a next step cites one habit due November 5 and says
// "yesterday". Read the same morning, the word stays: the item's date words a
// day word only when it is that day, and nothing changed, so the recording
// is kept (no second synthesis).
func TestRelativeDaysKeepAWordThatIsNotTheItemsDate(t *testing.T) {
	la := attentionTestZone(t)
	ready := time.Date(2026, 11, 14, 6, 50, 0, 0, la)
	due := time.Date(2026, 11, 5, 9, 0, 0, 0, la)
	brief := MobileBriefV1{AsOf: ready.UTC().Format(time.RFC3339), Timezone: "America/Los_Angeles",
		Narrative: []MobileBriefPara{{Text: "Do a set this morning; it slipped again yesterday.", Refs: []string{"habit"}}},
		Evidence:  []MobileBriefRef{{Ref: "habit", SourceKind: "purpose"}},
		Sections:  MobileBriefSections{Commitments: []MobileBriefItem{{DueAt: due.Format(time.RFC3339), Refs: []string{"habit"}}}},
		Audio:     &MobileBriefAudio{Status: mobileBriefAudioStatusOK},
	}
	got := mobileBriefRelativeDaysAtRead(brief, ready.Add(3*time.Hour), ready, la, "English")
	if got.Narrative[0].Text != brief.Narrative[0].Text || got.Audio == nil {
		t.Fatalf("same-morning read = %q (audio kept %t)", got.Narrative[0].Text, got.Audio != nil)
	}
	next := mobileBriefRelativeDaysAtRead(brief, ready.Add(24*time.Hour), ready, la, "English")
	if strings.Contains(next.Narrative[0].Text, "November 5") || !strings.Contains(next.Narrative[0].Text, "Friday") {
		t.Fatalf("next-day read = %q, want yesterday worded as Friday", next.Narrative[0].Text)
	}
}
