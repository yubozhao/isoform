package main

import (
	"context"
	"testing"
	"time"

	"wails-gui/internal/engagement"
	"wails-gui/internal/handoff"
	"wails-gui/storage"
)

// Three kinds ring (notify v1.0.0 B3): the brief, meeting prep, finished
// work, and a reminder the person set. Every other kind is a quiet line on
// Today, unless the person routed it somewhere.
func TestOnlyThreeKindsRing(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		kind    engagement.Kind
		chosen  []engagement.Channel
		channel string
	}{
		{engagement.KindMeetingPrep, nil, "push"},
		{engagement.KindReminder, nil, "push"},
		{engagement.KindCommitmentSlipping, nil, string(engagement.ChannelTodayQuiet)},
		{engagement.KindWorkBlocked, nil, string(engagement.ChannelTodayQuiet)},
		{engagement.KindWaitingOnReply, nil, string(engagement.ChannelTodayQuiet)},
		{engagement.KindWorkBlocked, []engagement.Channel{engagement.ChannelPush}, "push"},
	} {
		rig := newEngagementTestRig(t)
		row, err := rig.svc.Submit(ctx, engagement.Moment{Kind: c.kind, SubjectID: "s-" + string(c.kind), Channels: c.chosen,
			DueAt: rig.clock.Add(time.Hour), Params: map[string]string{"title": "The Q3 plan"}})
		if err != nil {
			t.Fatal(err)
		}
		rig.svc.Pass(ctx)
		stored, err := rig.store.GetEngagementMoment(ctx, row.ID)
		if err != nil || stored == nil {
			t.Fatalf("%s: %v", c.kind, err)
		}
		if c.channel == string(engagement.ChannelTodayQuiet) {
			// Quiet on Today, or off by default: never a toast or a push.
			if len(rig.pushed) != 0 || len(rig.desktop) != 0 || stored.Channel == "push" || stored.Channel == "desktop" {
				t.Fatalf("%s rang: %+v pushed=%d desktop=%d", c.kind, stored, len(rig.pushed), len(rig.desktop))
			}
			continue
		}
		if stored.State != storage.EngagementStateSent || stored.Channel != c.channel {
			t.Fatalf("%s (chosen %v): %+v", c.kind, c.chosen, stored)
		}
	}
}

// The phone's switches are the three kinds and nothing else (phone-rings
// v1.0.0 17c).
func TestThePhoneSwitchesAreTheThreeKinds(t *testing.T) {
	view := engagementPhoneSettingsView(EngagementSettingsView{Kinds: []EngagementKindView{
		{Kind: "brief_ready"}, {Kind: "work_blocked"}, {Kind: "meeting_prep"}, {Kind: "commitment_slipping"}, {Kind: "work_finished"},
	}})
	if len(view.Kinds) != 3 || view.Kinds[0].Kind != "brief_ready" || view.Kinds[1].Kind != "meeting_prep" || view.Kinds[2].Kind != "work_finished" {
		t.Fatalf("phone kinds = %+v", view.Kinds)
	}
}

// Every action of the kind is a button (B6); only open opens the app, and
// a notification's Useful is the result's judgment, from the notification.
func TestTheToastCarriesEveryActionAndItsAnswer(t *testing.T) {
	actions := engagementToastActions([]engagement.Action{{ID: "open"}, {ID: "snooze"}, {ID: "unlabeled"}},
		engagementText{Actions: map[string]string{"open": "Read it", "snooze": "Later"}})
	if len(actions) != 2 || actions[0] != (toastAction{ID: "open", Label: "Read it"}) || actions[1] != (toastAction{ID: "snooze", Label: "Later"}) {
		t.Fatalf("actions = %+v", actions)
	}
	if !toastActionOpens("open") || toastActionOpens("not_needed") || toastActionOpens("useful") {
		t.Fatal("only open opens the app")
	}

	app, handoffs := workAnswersTestApp(t)
	saved, err := handoffs.Save(handoff.Entry{ID: "hf-done", Title: "The test plan", Status: handoff.StatusInProgress, Source: handoff.SourceDesktop, Body: "Write it."})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handoffs.UpdateStatus(saved.ID, handoff.StatusCompleted); err != nil {
		t.Fatal(err)
	}
	app.answerWorkFromNotification("result:hf-done", "open")
	if _, ok := app.answersLedger().current()["result:hf-done"]; ok {
		t.Fatal("open answered the item")
	}
	app.answerWorkFromNotification("result:hf-done", "useful")
	record, ok := app.answersLedger().current()["result:hf-done"]
	if !ok || record.Action != workActionUseful || record.Surface != workSurfaceNotification {
		t.Fatalf("Useful from the notification = %+v %v", record, ok)
	}
}
