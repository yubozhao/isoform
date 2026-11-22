package main

import (
	"context"
	"testing"

	"wails-gui/internal/pii"

	"yansu-agent/agent"
)

func TestSemanticVisionRechecksSessionPrivacy(t *testing.T) {
	for _, protection := range []string{"protected", "sensitive-app", "private-window", "paused-capture", "closed-eyes"} {
		t.Run(protection, func(t *testing.T) {
			app := newPrivacyReadTestApp(t)
			withVisionPrivacyModel(t, true)
			store := app.activity.store
			priorApps := pii.GetUserSensitiveApps()
			t.Cleanup(func() { pii.SetUserSensitiveApps(priorApps) })
			pii.SetUserSensitiveApps(nil)
			insertTestSession(t, store, "vision-source", "pending", 100_000)
			runner := &memoryTranslationFakeRunner{ready: true, response: "Private content"}
			app.runner = runner
			triager := NewActivityTriager(app, nil)
			if !triager.visionSessionAllowsModelRead("vision-source") {
				t.Fatal("public source rejected before privacy change")
			}
			exec := func(query string, args ...any) {
				t.Helper()
				if _, err := store.db.Exec(query, args...); err != nil {
					t.Fatal(err)
				}
			}
			switch protection {
			case "protected":
				exec(`UPDATE activity_sessions SET triage_status = 'protected' WHERE id = 'vision-source'`)
			case "sensitive-app":
				pii.AddUserSensitiveApp("com.example.test", "TestApp")
			case "private-window":
				exec(`UPDATE activity_snapshots SET parsed_json = '{"title":"Private document - Incognito"}' WHERE session_id = 'vision-source'`)
			case "paused-capture":
				app.saveObservationPauses([]observationPause{{Start: 110_000, End: 120_000}})
			case "closed-eyes":
				app.closedEyes.enabled.Store(true)
			}
			if triager.visionSessionAllowsModelRead("vision-source") {
				t.Fatal("historical vision source remained readable after privacy change")
			}
			session, err := store.GetSessionByID("vision-source")
			if err != nil {
				t.Fatal(err)
			}
			triager.runSemanticEventExtraction(context.Background(), sessionGroup{Sessions: []ActivitySessionRow{*session}})
			var status string
			if err := store.db.QueryRow(`SELECT semantic_status FROM activity_sessions WHERE id = 'vision-source'`).Scan(&status); err != nil {
				t.Fatal(err)
			}
			if status != "pending" {
				t.Fatalf("protected group entered semantic processing: status=%q", status)
			}
			// Neither an already cropped frame nor a cached result may bypass a
			// privacy change. No image decoding or external model is involved.
			img := &agent.ImageAttachment{DataBase64: "previously-cropped-frame"}
			for _, cached := range []bool{false, true} {
				if cached {
					key := visionImageKey(img)
					triager.visionCache.putWeChat(key, wechatVisionResult{Substance: "Private cached content"})
					triager.visionCache.putGeneric(key, "Private cached content")
				}
				if got := triager.describeWeChatScreenshot(context.Background(), img, session.ID); got.Substance != "" || len(got.Messages) != 0 {
					t.Fatalf("WeChat vision returned protected content (cached=%t)", cached)
				}
				if got := triager.describeScreenshotForVision(context.Background(), img, session.ID); got != "" {
					t.Fatalf("generic vision returned protected content (cached=%t)", cached)
				}
			}
			if runner.calls != 0 {
				t.Fatalf("protected screenshots reached a model %d times", runner.calls)
			}
		})
	}
}
