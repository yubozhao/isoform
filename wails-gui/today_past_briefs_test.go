package main

import "testing"

// Today's Past briefs lists what the phone's brief history lists, newest
// first, and never fails when no brief service is running.
func TestListPastBriefsReadsThePhoneIndex(t *testing.T) {
	a, _, svc, brief, _ := round1TopicBrief(t, mobileBriefTestDay(8, 0))
	got, err := a.ListPastBriefs()
	if err != nil {
		t.Fatal(err)
	}
	want := mobileBriefSummariesWithLeads(svc, todayPastBriefsMax)
	if len(got) != 1 || got[0].BriefID != brief.BriefID || len(want) != 1 || got[0] != want[0] {
		t.Fatalf("past briefs = %+v, want %+v", got, want)
	}
}

func TestListPastBriefsWithoutServiceIsEmpty(t *testing.T) {
	a := &App{mobileBridgeStopping: true}
	got, err := a.ListPastBriefs()
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("past briefs = %#v, %v; want an empty list", got, err)
	}
}
