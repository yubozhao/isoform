package main

import (
	"context"
	"testing"

	"wails-gui/internal/topicday"
)

func TestForgetAllTopicsKeepsOnlyTheSwitches(t *testing.T) {
	app, _, _ := dailyTopicsTestApp(t)
	off := false
	svc := app.topicsSvc()
	if _, err := svc.store.update(func(s *topicsStateFile) {
		s.Enabled, s.RefreshIntervalMinutes = &off, 30
		s.Weights = map[string]float64{"halcyon": 2}
		s.Visited = map[string]int64{"https://halcyon.example": 1}
		s.SidebarPins = map[string]bool{"halcyon": true}
	}); err != nil {
		t.Fatal(err)
	}
	daily, err := app.topicsDailyStore()
	if err != nil {
		t.Fatal(err)
	}
	if err := daily.AppendFeedback(topicday.Feedback{TopicID: "t", TopicName: "Halcyon", Verdict: topicday.VerdictNotMine}); err != nil {
		t.Fatal(err)
	}
	if err := app.forgetAllTopics(context.Background()); err != nil {
		t.Fatal(err)
	}
	state := svc.store.load()
	if len(state.Weights)+len(state.Visited)+len(state.SidebarPins)+len(state.Projects)+len(state.Journal) != 0 {
		t.Fatalf("topics after forget = %+v", state)
	}
	if state.Enabled == nil || *state.Enabled || state.RefreshIntervalMinutes != 30 {
		t.Fatalf("the switches must stay: %+v", state)
	}
	if feedback, _ := daily.Feedback(); len(feedback) != 0 {
		t.Fatalf("daily answers after forget = %+v", feedback)
	}
}
