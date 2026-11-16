package main

import (
	"strings"
	"testing"
	"time"

	briefcore "wails-gui/internal/brief"
)

// A brief whose words carry tokens reads right on its own day, the evening
// after, and the next morning: the day words are rendered from each item's
// time for the reader, never rewritten from stored words.
func TestBriefTokensRenderForTheReadersDay(t *testing.T) {
	loc, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatal(err)
	}
	interview := time.Date(2026, 11, 16, 15, 0, 0, 0, loc)
	raises := time.Date(2026, 11, 15, 0, 0, 0, 0, loc)
	brief := MobileBriefV1{
		BriefID: "2026-11-15-morning", LocalDate: "2026-11-15", Timezone: "America/Los_Angeles", NarrativeStatus: "ok",
		Narrative: []MobileBriefPara{
			{Text: "Your calf raises are due today.", Template: "Your calf raises are due {when:e1}.", Refs: []string{"e1"}},
			{Text: "The interview is tomorrow 3:00 PM.", Template: "The interview is {when:e2}.", Refs: []string{"e2"}},
		},
		Evidence: []MobileBriefRef{{Ref: "e1", SourceKind: "purpose"}, {Ref: "e2", SourceKind: mobileBriefCalendarSource}},
		Sections: MobileBriefSections{
			Commitments: []MobileBriefItem{{DueAt: raises.Format(time.RFC3339), Refs: []string{"e1"}}},
			Meetings:    []MobileBriefItem{{At: interview.Format(time.RFC3339), Refs: []string{"e2"}}},
		},
	}
	for _, tc := range []struct {
		name string
		now  time.Time
		want [2]string
	}{
		{"that morning", time.Date(2026, 11, 15, 7, 0, 0, 0, loc), [2]string{"Your calf raises are due today.", "The interview is tomorrow at 3:00 PM."}},
		{"that evening", time.Date(2026, 11, 15, 21, 0, 0, 0, loc), [2]string{"Your calf raises are due today.", "The interview is tomorrow at 3:00 PM."}},
		{"the next morning", time.Date(2026, 11, 16, 7, 0, 0, 0, loc), [2]string{"Your calf raises are due yesterday.", "The interview is today at 3:00 PM."}},
	} {
		got := mobileBriefRenderTokens(brief, tc.now, "English")
		for i, want := range tc.want {
			if got.Narrative[i].Text != want {
				t.Errorf("%s: paragraph %d = %q, want %q", tc.name, i, got.Narrative[i].Text, want)
			}
		}
	}
	if brief.Narrative[1].Template != "The interview is {when:e2}." || brief.Narrative[1].Text != "The interview is tomorrow 3:00 PM." {
		t.Fatal("rendering changed the stored words")
	}
	timed := mobileBriefTimedRefs(brief)
	for _, para := range brief.Narrative {
		if codes := briefcore.ValidateTokens(briefcore.Paragraph{Text: para.Template, Refs: para.Refs}, timed); len(codes) > 0 {
			t.Fatalf("a valid tokenized paragraph fails the validator: %v", codes)
		}
	}
}

// Text the writer wrote without tokens (every brief before them) renders as is.
func TestBriefTokensLeaveTokenlessTextAlone(t *testing.T) {
	brief := MobileBriefV1{Narrative: []MobileBriefPara{{Text: "A lot moved on the launch memo."}}}
	if got := mobileBriefRenderTokens(brief, time.Now(), "English"); got.Narrative[0].Text != brief.Narrative[0].Text {
		t.Fatalf("tokenless text changed: %q", got.Narrative[0].Text)
	}
}

// The whole read: a tokenized brief built for its morning reads with the
// reader's day words the next morning, and its recording keeps its binding
// (the binding is made from the template, not the day's words).
func TestTokenizedBriefReadsForTheNextDayAndKeepsItsRecording(t *testing.T) {
	app, svc, brief := newStoredMobileBriefPrivacyFixture(t)
	svc.todayRefreshWG.Wait()
	svc.wg.Wait()
	var ref string
	for _, e := range brief.Evidence {
		if e.SourceID == "safe-recall" {
			ref = e.Ref
		}
	}
	asOf := mobileBriefAsOfTime(brief)
	brief.Sections.Learned = append(brief.Sections.Learned, MobileBriefItem{ID: "due", DueAt: asOf.Format(time.RFC3339), Refs: []string{ref}})
	brief.Narrative = []MobileBriefPara{{Text: "The safe recall is due today.", Template: "The safe recall is due {when:" + ref + "}.", Refs: []string{ref}}}
	brief.NarrativeStatus = "ok"
	brief.Audio = &MobileBriefAudio{Status: mobileBriefAudioStatusOK, Bytes: 4, Mime: mobileBriefAudioMimeWAV}
	brief.Audio.binding = mobileBriefAudioBinding(brief, "en")
	sameDay := app.mobileBriefForDelivery(brief)
	if len(sameDay.Narrative) != 1 || !strings.Contains(sameDay.Narrative[0].Text, "due today") {
		t.Fatalf("same day: %+v", sameDay.Narrative)
	}
	next := asOf.Add(24 * time.Hour)
	svc.now = func() time.Time { return next }
	nextDay := app.mobileBriefForDelivery(brief)
	if len(nextDay.Narrative) != 1 || !strings.Contains(nextDay.Narrative[0].Text, "due yesterday") {
		t.Fatalf("next day: %+v", nextDay.Narrative)
	}
	// The recording's binding does not change with the reader's day: it is
	// made from the template, so a recording bound on the brief's day still
	// matches the next day.
	if mobileBriefAudioBinding(sameDay, "en") != mobileBriefAudioBinding(nextDay, "en") {
		t.Fatal("the recording's binding changed with the day words")
	}
}
