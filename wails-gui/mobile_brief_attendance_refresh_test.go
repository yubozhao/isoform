package main

import (
	"reflect"
	"testing"
	"time"
)

func TestMobileBriefTodayRefreshKeepsAttendanceEvidence(t *testing.T) {
	for _, order := range []string{"meeting-first", "task-first"} {
		t.Run(order, func(t *testing.T) {
			now := mobileBriefTestDay(12, 0)
			task := mobileBriefCandidate{SourceKind: "history", SourceID: "review-task", Title: "Review the draft", Status: "needs_review", Kind: "one_time_work", At: now}
			base := buildMobileBriefV1(mobileBriefInput{
				Slot: mobileBriefSlotOnRequest, AsOf: now, WindowStart: now.Add(-4 * time.Hour), Location: time.UTC,
				Attendance: []mobileBriefAttendanceObservation{{EventID: "missed-meeting", Title: "Planning discussion", State: "skipped", Reason: "active_elsewhere_on_computer", ObservedAt: now, SourceSessionIDs: []string{"fixture-session"}}},
				OpenLoops:  []mobileBriefCandidate{task},
			})
			if len(base.Evidence) != 2 || base.Evidence[0].SourceID != task.SourceID || base.Evidence[1].SourceID != "missed-meeting" {
				t.Fatalf("fixture must reserve the task before attendance: %+v", base.Evidence)
			}
			base.Narrative = []MobileBriefPara{{Text: "Review whether the planning discussion needs a follow-up.", Refs: append([]string(nil), base.MeetingAttendance[0].Refs...)}}
			if order == "meeting-first" {
				// A stored brief can have a different reservation order. Both
				// attendance and the task must follow the source, not its ID.
				remap := map[string]string{"e1": "e2", "e2": "e1"}
				for i := range base.Evidence {
					base.Evidence[i].Ref = remap[base.Evidence[i].Ref]
				}
				for _, section := range mobileBriefSectionLines(base.Sections) {
					for i := range section.items {
						section.items[i].Refs = []string{remap[section.items[i].Refs[0]]}
					}
				}
				base.MeetingAttendance[0].Refs = []string{"e1"}
				base.Narrative[0].Refs = []string{"e1"}
			}
			originalAttendance := base.MeetingAttendance[0]
			task.At = now.Add(time.Hour)
			input := mobileBriefInput{
				AsOf: task.At, Location: time.UTC, OpenLoops: []mobileBriefCandidate{task},
				Today: &mobileBriefTodayInput{Context: map[string]MobileBriefItemContext{}, Unavailable: map[string]bool{}},
			}
			got := projectMobileBriefToday(base, input)
			assertTodayRefsResolve(t, got)
			if len(got.MeetingAttendance) != 1 {
				t.Fatalf("refresh dropped attendance: %+v", got.MeetingAttendance)
			}
			attendance := got.MeetingAttendance[0]
			originalAttendance.Refs = attendance.Refs
			if !reflect.DeepEqual(attendance, originalAttendance) {
				t.Fatalf("refresh changed the observation: got %+v, want %+v", attendance, originalAttendance)
			}
			byRef := map[string]MobileBriefRef{}
			for _, ref := range got.Evidence {
				byRef[ref.Ref] = ref
			}
			checkMeeting := func(refs []string) {
				t.Helper()
				if len(refs) != 1 {
					t.Fatalf("meeting refs = %v", refs)
				}
				ref := byRef[refs[0]]
				if ref.SourceKind != mobileBriefAttendanceSource || ref.SourceID != "missed-meeting" {
					t.Fatalf("attendance points at another source: %+v", ref)
				}
			}
			checkMeeting(attendance.Refs)
			foundMeeting, foundTask := false, false
			for _, item := range got.Sections.Review {
				if item.Kind == mobileBriefAttendanceSource {
					checkMeeting(item.Refs)
					foundMeeting = true
				}
				if item.Title == task.Title {
					foundTask = len(item.Refs) == 1 && byRef[item.Refs[0]].SourceID == task.SourceID
				}
			}
			if foundMeeting || !foundTask {
				t.Fatalf("refresh promoted attendance or lost the task in Review: %+v", got.Sections.Review)
			}
			if len(got.Narrative) != 1 {
				t.Fatal("refresh lost the narrative")
			}
			checkMeeting(got.Narrative[0].Refs)
			if next := projectMobileBriefToday(got, input); !reflect.DeepEqual(next, got) {
				t.Fatal("unchanged refresh changed attendance or refs again")
			}
		})
	}
}
