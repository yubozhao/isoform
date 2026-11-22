package main

import (
	"context"
	"strings"
	"testing"
	"time"

	briefcore "wails-gui/internal/brief"
	"wails-gui/internal/pii"
	"wails-gui/storage"
)

// A Messages ask reaches the writer as a follow-up in Isoform's words, and
// every gate keeps it out: Closed Eyes, the switch, protecting Messages,
// leaving the person out, a pause over it and retention.
func TestMessageAskReachesTheBriefAndEveryGateHoldsIt(t *testing.T) {
	isolateActivityPrivacyTest(t)
	t.Setenv("YANSU_MOBILE_STATE_DIR", t.TempDir())
	app := newPrivacyReadTestApp(t)
	now := mobileBriefTestDay(18, 0)
	ctx := context.Background()
	if err := app.store.SetUIState(ctx, permissionUIStateKey(messagesPermissionKey), "true"); err != nil {
		t.Fatal(err)
	}
	priya, err := app.store.CreatePrincipal(ctx, storage.PrincipalRow{Kind: storage.PrincipalKindContact, DisplayName: "Priya"})
	if err != nil {
		t.Fatal(err)
	}
	ask := storage.MessageAskRow{ID: "ask1", ChatKey: "chat1", PrincipalID: priya.ID, PersonName: "Priya", Kind: storage.MessageAskAsked,
		Summary: "Priya asked you for the launch deck by Friday.", NextStep: "Send Priya the launch deck", Action: "reply", At: now.Add(-2 * time.Hour).UnixMilli()}
	if err := app.store.UpsertMessageAsks(ctx, []storage.MessageAskRow{ask}, now.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	collect := func() []mobileBriefCandidate {
		in := mobileBriefInput{Slot: mobileBriefSlotEvening, AsOf: now, WindowStart: now.Add(-12 * time.Hour), Location: time.UTC, Timezone: "UTC"}
		collectMobileBriefMessageAsks(ctx, app, &in)
		return in.Conversations
	}
	in := mobileBriefInput{Slot: mobileBriefSlotEvening, AsOf: now, WindowStart: now.Add(-12 * time.Hour), Location: time.UTC, Timezone: "UTC"}
	collectMobileBriefMessageAsks(ctx, app, &in)
	if len(in.Conversations) != 1 {
		t.Fatalf("conversations = %+v", in.Conversations)
	}
	brief := buildMobileBriefV1(in)
	m, _ := newBriefMaterial(in, brief, briefPartDone, "")
	found := false
	for _, item := range m.Items {
		if strings.Contains(item.Detail, ask.Summary) {
			found = true
			if item.Group != briefcore.GroupFollowUp || !strings.Contains(item.Detail, ask.NextStep) {
				t.Fatalf("the ask is not a follow-up with its next step: %+v", item)
			}
		}
	}
	if !found {
		t.Fatalf("the ask did not reach the writer: %+v", m.Items)
	}
	if mobileBriefSourceAnchor(mobileBriefMessageAskSource) != mobileBriefAnchorPerson {
		t.Fatal("a Messages ask anchors on its person")
	}

	held := func(name string, undo func()) {
		t.Helper()
		if got := collect(); len(got) != 0 {
			t.Fatalf("%s: the ask reached the brief: %+v", name, got)
		}
		if _, ok := app.mobileBriefMessageAsk(ctx, ask.ID, now); ok {
			t.Fatalf("%s: the ask is still readable at read time", name)
		}
		undo()
		if got := collect(); len(got) != 1 {
			t.Fatalf("after %s: conversations = %+v", name, got)
		}
	}
	app.closedEyes.enabled.Store(true)
	held("closed eyes", func() { app.closedEyes.enabled.Store(false) })

	if err := app.store.SetUIState(ctx, permissionUIStateKey(messagesPermissionKey), "false"); err != nil {
		t.Fatal(err)
	}
	held("switch off", func() { _ = app.store.SetUIState(ctx, permissionUIStateKey(messagesPermissionKey), "true") })

	prior := pii.GetUserSensitiveApps()
	pii.SetUserSensitiveApps(map[string]string{"com.apple.MobileSMS": "Messages"})
	held("protected", func() { pii.SetUserSensitiveApps(prior) })
	t.Cleanup(func() { pii.SetUserSensitiveApps(prior) })

	if err := setPeopleLeftOut(ctx, app.store, []string{priya.ID}); err != nil {
		t.Fatal(err)
	}
	held("left out", func() { _ = setPeopleLeftOut(ctx, app.store, nil) })

	if _, ok := app.mobileBriefMessageAsk(ctx, ask.ID, now.Add(storage.MessageAskRetention+time.Hour)); ok {
		t.Fatal("an ask past retention is still readable")
	}
	if err := app.store.DeletePrincipal(ctx, priya.ID); err != nil {
		t.Fatal(err)
	}
	if got := collect(); len(got) != 0 {
		t.Fatalf("a deleted person's ask reached the brief: %+v", got)
	}
}
