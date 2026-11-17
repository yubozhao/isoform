package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"wails-gui/internal/engagement"
	"wails-gui/storage"
)

func restartJourneyPrivacyApp(t *testing.T, app *App, now time.Time) *App {
	t.Helper()
	if err := app.store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := app.activity.store.Close(); err != nil {
		t.Fatal(err)
	}
	state, err := storage.New(filepath.Join(os.Getenv("HOME"), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	activity, err := NewActivityStore(filepath.Join(os.Getenv("HOME"), "activity"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { state.Close(); activity.Close() })
	fresh := &App{store: state, activity: &activityMonitorState{store: activity}}
	fresh.engagement = newEngagementService(state, nil, engagementDelivery{Report: fresh.postEngagementReport})
	fresh.engagement.now = func() time.Time { return now }
	if err := fresh.engagement.load(t.Context()); err != nil {
		t.Fatal(err)
	}
	return fresh
}

// These are production storage reads and the authenticated phone route. Check
// plain Content as well as every localized copy, evidence and action locator.
func journeyDeliveryJSON(t *testing.T, app *App) (string, string) {
	t.Helper()
	main, err := app.ensureMainChat(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	page, err := app.LoadStreamItemsPage(main.LocalID, -1, 50)
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("computer read: %+v %v", page, err)
	}
	raw, err := json.Marshal(page)
	if err != nil {
		t.Fatal(err)
	}
	phone, wire := proactiveVoicePhoneRead(t, app, main.LocalID)
	if len(phone.Items) != 1 {
		t.Fatalf("phone read: %s", wire)
	}
	for _, content := range []string{string(raw), wire} {
		if strings.Contains(content, "voiceReceipt") || strings.Contains(content, "PurposeContributors") {
			t.Fatalf("local provenance escaped: %s", content)
		}
	}
	return string(raw), wire
}

func TestFirstWeekSavedSourceCardsRevalidateOnBothDevicesAfterRestart(t *testing.T) {
	for _, reason := range []string{"protected meeting", "deleted meeting", "deleted notes", "closed eyes", "revoked commitment", "missing receipt", "legacy card"} {
		t.Run(reason, func(t *testing.T) {
			app := newMobileBriefPurposeTestApp(t)
			ctx := t.Context()
			now := time.Date(2026, 10, 16, 10, 0, 0, 0, time.UTC)
			app.engagement = newEngagementService(app.store, nil, engagementDelivery{})
			app.engagement.now = func() time.Time { return now }
			stage, sourceID := "meeting", "journey-local-meeting"
			notes := filepath.Join(t.TempDir(), "notes.md")
			if err := os.WriteFile(notes, []byte("Source-only meeting text"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := app.activity.store.SaveSession(ActivitySessionRow{ID: sourceID, IsMeeting: true, TranscriptPath: notes, StartTime: now.Add(-time.Hour).UnixMilli(), EndTime: now.Add(-time.Minute).UnixMilli(), AppNames: `["Editor"]`, TriageStatus: "done", CreatedAt: now.Add(-time.Hour).UnixMilli()}, nil, nil); err != nil {
				t.Fatal(err)
			}
			switch reason {
			case "revoked commitment":
				stage, sourceID = "commitment", journeyReminderPurpose(t, app, now)
			}
			m := engagement.Moment{ID: "journey-source-card", Kind: engagement.KindFirstWeek, SubjectID: sourceID, DedupeKey: "journey-source-card", CreatedAt: now, Params: map[string]string{"stage": stage}}
			if !app.postEngagementReport(m, engagementText{}) {
				t.Fatal("readable source was not posted")
			}
			original := mainChatProactiveItems(t, app)[0]
			computer, phone := journeyDeliveryJSON(t, app)
			for _, wire := range []string{computer, phone} {
				if !strings.Contains(wire, engagement.FirstWeekCopy(stage, "", "en")) || !strings.Contains(wire, sourceID) {
					t.Fatalf("readable source was withdrawn: %s", wire)
				}
			}
			app = restartJourneyPrivacyApp(t, app, now)
			computer, phone = journeyDeliveryJSON(t, app)
			if !strings.Contains(computer, original.Body) || !strings.Contains(phone, original.Body) {
				t.Fatal("restart lost readable saved provenance")
			}
			switch reason {
			case "protected meeting":
				_, err := app.activity.store.db.Exec(`UPDATE activity_sessions SET triage_status='protected' WHERE id=?`, sourceID)
				if err != nil {
					t.Fatal(err)
				}
			case "deleted meeting":
				if err := app.activity.store.DeleteSession(sourceID); err != nil {
					t.Fatal(err)
				}
			case "deleted notes":
				if err := os.Remove(notes); err != nil {
					t.Fatal(err)
				}
			case "closed eyes":
				app.closedEyes.enabled.Store(true)
			case "revoked commitment":
				if err := app.store.SetUIState(ctx, permissionUIStateKey(remindersPermissionKey), "false"); err != nil {
					t.Fatal(err)
				}
			case "missing receipt":
				if err := app.store.SetUIState(ctx, engagementJourneyReceiptPrefix+original.VoiceReceipt, ""); err != nil {
					t.Fatal(err)
				}
			case "legacy card":
				original.VoiceReceipt, original.VoiceVersion = "", ""
				raw, _ := json.Marshal(original)
				row := mainChatStructuredRows(t, app, mainChatProactiveTool)[0]
				row.ToolInput = string(raw)
				if err := app.store.UpdateStreamItemToolInput(ctx, row.ID, row.ToolInput); err != nil {
					t.Fatal(err)
				}
			}
			computer, phone = journeyDeliveryJSON(t, app)
			for _, wire := range []string{computer, phone} {
				for _, forbidden := range []string{sourceID, original.Body, original.Localized["zh-CN"].Body, original.VoiceReceipt} {
					if forbidden != "" && strings.Contains(wire, forbidden) {
						t.Fatalf("%s leaked %q: %s", reason, forbidden, wire)
					}
				}
			}
			if reason != "legacy card" && mainChatProactiveItems(t, app)[0].VoiceReceipt != original.VoiceReceipt {
				t.Fatal("delivery rewrote stored history")
			}
		})
	}
}

func journeyReminderPurpose(t *testing.T, app *App, now time.Time) string {
	t.Helper()
	row := storage.ReminderRow{UID: "journey-local-reminder", Title: "Send project notes", DueAt: now.Add(time.Hour).UnixMilli(), List: "Work"}
	if err := app.store.UpsertReminders(t.Context(), []storage.ReminderRow{row}, now.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	created, _, err := NewPurposeMapStore(app.activity.store.db).Create(reminderCreateInput(reminderFromRow(row), now.Add(-time.Hour).UnixMilli(), time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	return created.OpenLoop.ID
}

func TestFirstWeekHeldWelcomeOmitsElapsedBriefPrediction(t *testing.T) {
	// A locked computer no longer holds a notice for the unlock (notify
	// v1.0.0 B2), so only quiet hours hold the welcome.
	for _, locked := range []bool{false} {
		t.Run(map[bool]string{false: "quiet hours", true: "locked computer"}[locked], func(t *testing.T) {
			app, rig := newJourneyRig(t)
			ctx := context.Background()
			rig.clock = time.Date(2026, 10, 12, 23, 0, 0, 0, rig.clock.Location())
			if locked {
				rig.clock = rig.clock.Add(-2 * time.Hour)
				rig.presence.ScreenLocked = true
			}
			finishJourneyOnboarding(t, app, rig.clock)
			rig.svc.Pass(ctx)
			if len(mainChatProactiveItems(t, app)) != 0 {
				t.Fatal("held welcome was delivered")
			}
			rig.clock = time.Date(2026, 10, 13, 9, 0, 0, 0, rig.clock.Location())
			rig.presence.ScreenLocked = false
			rig.svc.Pass(ctx)
			rig.svc.Pass(ctx)
			items := mainChatProactiveItems(t, app)
			if len(items) != 1 {
				t.Fatalf("daily duplicate: %+v", items)
			}
			for lang, copy := range items[0].Localized {
				if strings.Contains(copy.Body, "2026-10-13 08:00") || copy.Body != engagement.FirstWeekCopy("welcome", "", lang) {
					t.Fatalf("%s retained elapsed estimate: %s", lang, copy.Body)
				}
			}
			if rig.svc.state.SentByKindToday[engagement.KindFirstWeek] != 1 {
				t.Fatal("held welcome did not consume exactly one daily slot")
			}
		})
	}
}

func TestFirstWeekPeopleCopyMatchesListDestination(t *testing.T) {
	m := engagement.Moment{Kind: engagement.KindFirstWeek, SubjectID: "person-id", Params: map[string]string{"stage": "person"}}
	target, _ := engagementChatTarget(m)
	if target.Kind != "person" {
		t.Fatal(target)
	}
	for lang, listName := range map[string]string{"en": "People list", "zh-CN": "人物列表"} {
		if copy := engagement.FirstWeekCopy("person", "", lang); !strings.Contains(copy, listName) {
			t.Fatalf("%s promises a different destination: %s", lang, copy)
		}
	}
}
