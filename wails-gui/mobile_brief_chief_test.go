package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"wails-gui/internal/calendar"
	"wails-gui/internal/pii"
)

func TestMobileBriefChiefEvidenceSurvivesBusyDay(t *testing.T) {
	in := mobileBriefGoldenInput()
	in.Attendance = []mobileBriefAttendanceObservation{{EventID: "missed", Title: "Review with Tim", State: "skipped", Reason: "active_elsewhere_on_computer", ObservedAt: in.AsOf}}
	in.Moved, in.Meetings, in.OpenLoops = nil, nil, nil
	for i := range 12 {
		at := in.AsOf.Add(time.Duration(i+1) * time.Minute)
		in.Moved = append(in.Moved, mobileBriefCandidate{SourceKind: "history", SourceID: fmt.Sprintf("work-%d", i), Title: "Observed work", At: at.Add(-time.Hour)})
		in.Meetings = append(in.Meetings, mobileBriefCandidate{SourceKind: "calendar", SourceID: fmt.Sprintf("event-%d", i), Title: fmt.Sprintf("Meeting %d", i), Status: "upcoming", At: at})
		in.OpenLoops = append(in.OpenLoops, mobileBriefCandidate{SourceKind: "purpose", SourceID: fmt.Sprintf("purpose-%d", i), Title: "Open commitment", Kind: "commitment", Status: "active"})
		for _, dest := range []*[]mobileBriefCandidate{&in.Doing, &in.Watching, &in.People, &in.Learned, &in.Deliverables, &in.Schedules} {
			*dest = append(*dest, mobileBriefCandidate{SourceKind: fmt.Sprintf("source-%p", dest), SourceID: fmt.Sprint(i), Title: "Legacy context"})
		}
	}
	in.Moved = append(in.Moved, mobileBriefCandidate{SourceKind: "history", SourceID: "old-ready", Title: "Draft ready for review", Status: "needs_review", At: in.AsOf.Add(-24 * time.Hour)})
	brief := buildMobileBriefV1(in)
	if len(brief.MeetingAttendance) != 1 || len(brief.Sections.Review) != 1 || brief.Sections.Review[0].Title != "Draft ready for review" {
		t.Fatalf("review was starved by legacy evidence: %+v, attendance %+v", brief.Sections.Review, brief.MeetingAttendance)
	}
	if len(brief.Sections.Decide) == 0 || len(brief.Sections.DayAhead) != 2 || brief.Sections.DayAhead[0].Title != "Meeting 0" {
		t.Fatalf("decisions or nearest meeting dropped: decide %+v day ahead %+v", brief.Sections.Decide, brief.Sections.DayAhead)
	}
	if len(brief.Evidence) > mobileBriefEvidenceLimit {
		t.Fatalf("evidence limit exceeded: %d", len(brief.Evidence))
	}
}

func TestMobileBriefChiefSectionsSeparateForwardReviewFromHighlights(t *testing.T) {
	asOf := time.Date(2026, 10, 16, 18, 0, 0, 0, time.UTC)
	brief := buildMobileBriefV1(mobileBriefInput{
		AsOf: asOf, WindowStart: asOf.Add(-12 * time.Hour), Location: time.UTC,
		Moved: []mobileBriefCandidate{
			{SourceKind: "history", SourceID: "review", Title: "Pricing draft", Status: "needs_review", At: asOf.Add(-time.Hour)},
			{SourceKind: "history", SourceID: "blocked", Title: "Launch plan", Status: "blocked", At: asOf.Add(-2 * time.Hour)},
			{SourceKind: mobileBriefRecapSource, SourceID: "summary", Title: "Worked on the hiring plan", Kind: "recap", Status: "done"},
		},
		OpenLoops: []mobileBriefCandidate{
			{SourceKind: "purpose", SourceID: "commitment", Title: "Review the offer", Kind: "commitment", Status: "active"},
			{SourceKind: "purpose", SourceID: "goal", Title: "Prepare the next release", Kind: "goal", Status: "active"},
		},
		Meetings: []mobileBriefCandidate{
			{SourceKind: "calendar", SourceID: "later", Title: "Team review", Status: "upcoming", At: asOf.Add(2 * time.Hour)},
			{SourceKind: "calendar", SourceID: "next", Title: "Hiring discussion", Status: "upcoming", At: asOf.Add(time.Hour)},
		},
		People: []mobileBriefCandidate{{SourceKind: "principal", SourceID: "person", Title: "Dana", Status: "seen"}},
	})
	if got := brief.Sections.Review; len(got) != 1 || got[0].Title != "Pricing draft" {
		t.Fatalf("review = %+v", got)
	}
	if got := brief.Sections.Decide; len(got) != 2 || got[0].Title != "Launch plan" || got[1].Title != "Review the offer" {
		t.Fatalf("decide = %+v", got)
	}
	if got := brief.Sections.Plan; len(got) != 1 || got[0].Title != "Prepare the next release" {
		t.Fatalf("plan = %+v", got)
	}
	if got := brief.Sections.DayAhead; len(got) != 2 || got[0].Title != "Hiring discussion" || got[1].Title != "Team review" {
		t.Fatalf("day ahead = %+v", got)
	}
	seen := map[string]bool{}
	for _, section := range []struct {
		name  string
		items []MobileBriefItem
	}{{"review", brief.Sections.Review}, {"decide", brief.Sections.Decide}, {"plan", brief.Sections.Plan}} {
		for _, item := range section.items {
			if !strings.HasPrefix(item.Key, section.name+".") || seen[item.Key] || len(item.Refs) != 1 || item.Title == "Dana" {
				t.Fatalf("invalid grouping or invented follow-up: %+v", item)
			}
			seen[item.Key] = true
		}
	}
}

func TestMobileBriefChiefSectionsKeepPrivacyAndBounds(t *testing.T) {
	prior := pii.GetUserSensitiveApps()
	t.Cleanup(func() { pii.SetUserSensitiveApps(prior) })
	pii.SetUserSensitiveApps(map[string]string{"com.example.secret": "Secret Notes"})
	in := mobileBriefGoldenInput()
	in.Moved = append(in.Moved,
		mobileBriefCandidate{SourceKind: "history", SourceID: "protected", Title: "Purged evidence", Protected: true},
		mobileBriefCandidate{SourceKind: "history", SourceID: "sensitive", Title: "Private account", Apps: []string{"Secret Notes"}},
	)
	in.Doing = append(in.Doing, mobileBriefCandidate{SourceKind: mobileBriefFileSource, SourceID: "file", Title: "Private filename"})
	in.Watching = append(in.Watching, mobileBriefCandidate{SourceKind: mobileBriefWebSource, SourceID: "web", Title: "Private search"})
	brief := buildMobileBriefV1(in)
	// Browsing and file rows stay in their own sections (Today lists the
	// documents worked on); they never reach the chief of staff sections or the
	// narrative prompt. Protected and sensitive rows are in no section at all.
	chief := map[string]bool{mobileBriefSectionHighlights: true, mobileBriefSectionDayAhead: true, mobileBriefSectionReview: true, mobileBriefSectionDecide: true, mobileBriefSectionPlan: true}
	for _, section := range mobileBriefSectionLines(brief.Sections) {
		for _, item := range section.items {
			for _, forbidden := range []string{"Purged evidence", "Private account"} {
				if strings.Contains(item.Title, forbidden) {
					t.Fatalf("%q leaked in %s", forbidden, section.name)
				}
			}
			if chief[section.name] {
				for _, forbidden := range []string{"Private filename", "Private search"} {
					if strings.Contains(item.Title, forbidden) {
						t.Fatalf("%q leaked in %s", forbidden, section.name)
					}
				}
			}
		}
	}
	prompt := mobileBriefTestPrompt(brief, "English")
	for _, forbidden := range []string{"Purged evidence", "Private account", "Private filename", "Private search"} {
		if strings.Contains(prompt, forbidden) {
			t.Fatalf("%q reached the narrative prompt", forbidden)
		}
	}
	for _, items := range [][]MobileBriefItem{brief.Sections.Review, brief.Sections.Decide, brief.Sections.Plan, brief.Sections.Highlights, brief.Sections.DayAhead} {
		if len(items) > mobileBriefChiefLimit {
			t.Fatalf("too many chief of staff items: %d", len(items))
		}
	}
	if len(brief.Evidence) > mobileBriefEvidenceLimit {
		t.Fatal("new sections expanded the evidence budget")
	}
}

func TestMobileBriefFixtureDayHasNoPastClockTimes(t *testing.T) {
	loc := time.UTC
	asOf := time.Date(2026, 10, 16, 18, 0, 0, 0, loc)
	past := calendar.Event{ID: "past", Title: "Pricing review", Start: asOf.Add(-8 * time.Hour), End: asOf.Add(-7 * time.Hour)}
	future := calendar.Event{ID: "future", Title: "Team plan", Start: asOf.Add(time.Hour), End: asOf.Add(2 * time.Hour)}
	in := mobileBriefInput{
		AsOf: asOf, WindowStart: asOf.Add(-12 * time.Hour), Location: loc,
		Meetings: mobileBriefCalendarCandidates(nil, []calendar.Event{past, future}, mobileBriefSlotEvening, asOf, loc, mobileBriefClockLayout("English")),
		Moved:    []mobileBriefCandidate{{SourceKind: mobileBriefRecapSource, SourceID: "recap", Title: "09:30 design review", Detail: "09:30 to 10:00, 30 min", At: asOf.Add(-8 * time.Hour)}},
	}
	brief := buildMobileBriefV1(in)
	for _, section := range mobileBriefSectionLines(brief.Sections) {
		for _, item := range section.items {
			text := item.Title + " " + item.Detail
			for _, clock := range []string{"09:30", "10:00", "11:00"} {
				if strings.Contains(text, clock) {
					t.Fatalf("past clock in %s: %q", section.name, text)
				}
			}
		}
	}
	if len(brief.Sections.DayAhead) != 1 || !strings.Contains(brief.Sections.DayAhead[0].Detail, "7:00 PM") {
		t.Fatalf("needed upcoming time lost: %+v", brief.Sections.DayAhead)
	}
	prompt := mobileBriefTestPrompt(brief, "English")
	_, dayData, ok := strings.Cut(prompt, "The day,")
	if !ok {
		t.Fatal("prompt has no day data")
	}
	for _, clock := range []string{"09:30", "10:00", "11:00", "6:00 PM"} {
		if strings.Contains(dayData, clock) {
			t.Fatalf("past clock %s reached the model", clock)
		}
	}
}

func TestEveningBriefPlansForTomorrow(t *testing.T) {
	app, st := newLinkTestApp(t)
	asOf := time.Date(2026, 10, 16, 18, 0, 0, 0, time.UTC)
	event := prepEvent("tomorrow", "Founder review", asOf.Add(15*time.Hour), 30, "Zoom", 1)
	storeEvent(t, st, event)
	in := mobileBriefInput{Slot: mobileBriefSlotEvening, WindowStart: asOf.Add(-time.Hour), AsOf: asOf, Location: time.UTC}
	collectMobileBriefCalendar(app, &in)
	brief := buildMobileBriefV1(in)
	if len(brief.Sections.DayAhead) != 1 || brief.Sections.DayAhead[0].Title != "Founder review" {
		t.Fatalf("tomorrow is missing: %+v", brief.Sections.DayAhead)
	}
}

func TestMobileBriefChiefLeavesPurposeCommitmentAliasesOut(t *testing.T) {
	for _, tc := range []struct {
		name       string
		chatReport bool
		legacyChat bool
		progress   bool
	}{
		{name: "chat report with independent progress", chatReport: true, progress: true},
		{name: "represented by purpose card"},
		{name: "legacy chat reference", legacyChat: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, status := range []string{"active", "waiting"} {
				t.Run(status, func(t *testing.T) {
					now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
					legacy := mobileBriefCandidate{SourceKind: "purpose", SourceID: "draft", Title: "Send the reviewed draft", Kind: "commitment", Status: status}
					in := mobileBriefInput{AsOf: now, Location: time.UTC}
					if tc.legacyChat {
						legacy.ChatMessageID = "reported-message"
					} else {
						card := legacy
						card.Status = "due_today"
						if tc.chatReport {
							card.ChatMessageID = "reported-message"
							card.MomentID = "reported-moment"
							card.Evidence = []mobileBriefCandidate{{SourceKind: "engagement_moment", SourceID: card.MomentID, Title: "CHAT_ONLY_REMINDER"}}
						}
						in.Commitments = []mobileBriefCandidate{card}
					}
					if tc.progress {
						progress := legacy
						progress.Detail = "Compared the release options"
						progress.NextStep = "Review the proposed scope"
						progress.Evidence = []mobileBriefCandidate{{SourceKind: "activity", SourceID: "progress", Title: "Reviewed release options"}}
						in.WorkingToward = []mobileBriefCandidate{progress}
					}
					in.OpenLoops = []mobileBriefCandidate{legacy}
					brief := buildMobileBriefV1(in)
					if len(brief.Sections.OpenLoops) != 1 || brief.Sections.OpenLoops[0].Title != legacy.Title {
						t.Fatalf("legacy phone row was removed: %+v", brief.Sections.OpenLoops)
					}
					if len(brief.Sections.Review)+len(brief.Sections.Decide)+len(brief.Sections.Plan) != 0 {
						t.Fatalf("legacy commitment returned through chief sections: %+v", brief.Sections)
					}
					prompt := mobileBriefTestPrompt(brief, "English")
					for _, unwanted := range []string{"\nReview:\n", "\nDecide:\n", "\nPlan:\n", "CHAT_ONLY_REMINDER"} {
						if strings.Contains(prompt, unwanted) {
							t.Errorf("legacy reminder reached the prompt: %q", unwanted)
						}
					}
					if tc.chatReport && strings.Contains(prompt, "\ncommitments:") {
						t.Error("main chat commitment was repeated in the Purpose block")
					}
					if tc.progress {
						// Progress is what the user worked on: never in the
						// written brief's prompt (backlog item 65).
						for _, unwanted := range []string{"Compared the release options", "Reviewed release options"} {
							if strings.Contains(prompt, unwanted) {
								t.Errorf("purpose progress text reached the prompt: %q", unwanted)
							}
						}
					} else if tc.legacyChat && strings.Contains(prompt, legacy.Title) {
						t.Error("legacy chat reference reached the prompt without independent progress")
					}
					for _, language := range []string{"English", "Chinese"} {
						want := 0
						// An unreported due card now has its own forward section;
						// its legacy alias must not add a second reminder.
						if !tc.chatReport && !tc.legacyChat {
							want = 2 // Source-backed item plus the required closing offer.
						}
						if got := withoutOpening(mobileBriefNarrativeTemplate(brief, language)); len(got) != want {
							t.Errorf("%s fallback: got %+v, want %d due-card reminders", language, got, want)
						}
					}
				})
			}
		})
	}
}

func TestMobileBriefChiefKeepsChatDedupAfterPurposeBudgetTruncation(t *testing.T) {
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	legacy := mobileBriefCandidate{SourceKind: "purpose", SourceID: "reported-draft", Title: "Send the reviewed draft", Kind: "commitment", Status: "active"}
	progress := legacy
	for i := 0; i < mobileBriefEvidenceLimit; i++ {
		progress.Evidence = append(progress.Evidence, mobileBriefCandidate{SourceKind: "activity", SourceID: fmt.Sprintf("progress-%d", i), Title: "Reviewed draft sections"})
	}
	card := progress
	card.ChatMessageID, card.ChatSessionID = "reported-message", "main-chat"
	brief := buildMobileBriefV1(mobileBriefInput{
		AsOf: now, Location: time.UTC, OpenLoops: []mobileBriefCandidate{legacy},
		WorkingToward: []mobileBriefCandidate{progress}, Commitments: []mobileBriefCandidate{card},
	})
	if len(brief.Sections.WorkingToward)+len(brief.Sections.Commitments) != 0 ||
		!containsString(brief.Limits.TruncatedSections, mobileBriefSectionWorkingToward) ||
		!containsString(brief.Limits.TruncatedSections, mobileBriefSectionCommitments) {
		t.Fatalf("fixture did not exhaust atomic card budget: %+v", brief)
	}
	if len(brief.Sections.OpenLoops) != 1 || len(brief.Evidence) != 1 || brief.Sections.OpenLoops[0].ChatMessageID != card.ChatMessageID {
		t.Fatalf("legacy row lost its retained report marker: %+v", brief)
	}
	wire, err := json.Marshal(brief)
	if err != nil {
		t.Fatal(err)
	}
	var reloaded MobileBriefV1
	if err := json.Unmarshal(wire, &reloaded); err != nil {
		t.Fatal(err)
	}
	refreshed := projectMobileBriefToday(reloaded, mobileBriefInput{
		AsOf: now.Add(time.Hour), Location: time.UTC,
		Today: &mobileBriefTodayInput{Context: map[string]MobileBriefItemContext{}, Unavailable: map[string]bool{}},
	})
	for name, document := range map[string]MobileBriefV1{"built": brief, "reloaded": reloaded, "refreshed": refreshed} {
		t.Run(name, func(t *testing.T) {
			if len(document.Sections.OpenLoops) != 1 || document.Sections.OpenLoops[0].ChatMessageID != card.ChatMessageID {
				t.Fatalf("retained legacy row lost its report marker: %+v", document.Sections.OpenLoops)
			}
			sections := mobileBriefChiefSections(document)
			if len(sections.Review)+len(sections.Decide)+len(sections.Plan) != 0 {
				t.Fatalf("truncated chat reminder returned through chief sections: %+v", sections)
			}
			if prompt := mobileBriefTestPrompt(document, "English"); strings.Contains(prompt, legacy.Title) {
				t.Fatal("truncated chat reminder returned to the prompt")
			}
			if paragraphs := withoutOpening(mobileBriefNarrativeTemplate(document, "English")); len(paragraphs) != 0 {
				t.Fatalf("truncated chat reminder returned to the template: %+v", paragraphs)
			}
		})
	}
}

func TestMobileBriefChiefKeepsGoalPlanBesidePurposeProgress(t *testing.T) {
	goal := mobileBriefCandidate{SourceKind: "purpose", SourceID: "release", Title: "Prepare the release", Kind: "goal", Status: "active"}
	progress := goal
	progress.Detail = "Compared the release options"
	brief := buildMobileBriefV1(mobileBriefInput{
		OpenLoops: []mobileBriefCandidate{goal}, WorkingToward: []mobileBriefCandidate{progress},
	})
	if len(brief.Sections.Plan) != 1 || brief.Sections.Plan[0].Title != goal.Title || len(brief.Sections.WorkingToward) != 1 {
		t.Fatalf("commitment deduplication removed independent goal planning: %+v", brief.Sections)
	}
}

func TestMobileBriefChiefDoesNotCopyUnreadableCommitmentReport(t *testing.T) {
	legacy := mobileBriefCandidate{SourceKind: "purpose", SourceID: "draft", Title: "Review the draft", Kind: "commitment", Status: "active"}
	card := legacy
	card.ChatMessageID = "private-report"
	card.Evidence = []mobileBriefCandidate{{SourceKind: "activity", SourceID: "private-activity", Title: "Private activity", Protected: true}}
	brief := buildMobileBriefV1(mobileBriefInput{OpenLoops: []mobileBriefCandidate{legacy}, Commitments: []mobileBriefCandidate{card}})
	if len(brief.Sections.OpenLoops) != 1 || brief.Sections.OpenLoops[0].ChatMessageID != "" || len(brief.Sections.Commitments) != 0 {
		t.Fatalf("unreadable report metadata was propagated: %+v", brief.Sections)
	}
}
