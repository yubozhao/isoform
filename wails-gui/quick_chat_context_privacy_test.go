package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestFocusedChatContextRechecksPrivacyBeforeInjection(t *testing.T) {
	for _, kind := range []string{"closed-eyes", "sensitive-bundle", "sensitive-name", "private-window", "private-parser-title", "paused-capture", "malformed-parser", "unreadable-pauses", "missing-source"} {
		t.Run(kind, func(t *testing.T) {
			isolateActivityPrivacyTest(t)
			app := newPrivacyReadTestApp(t)
			now := time.Now().UnixMilli()
			snap := &focusedAppSnapshot{
				BundleID: "com.apple.Notes", AppName: "Notes", WindowTitle: "Useful note",
				Current:      &ActivitySnapshotRow{Timestamp: now, AXText: "protected-context-marker"},
				CurrentFresh: true,
			}
			switch kind {
			case "closed-eyes":
				app.closedEyes.enabled.Store(true)
			case "sensitive-bundle":
				snap.BundleID = "com.example.private"
			case "sensitive-name":
				snap.AppName = "Private Notes"
			case "private-window":
				snap.WindowTitle = "Bank - Incognito"
			case "private-parser-title":
				snap.Current.ParsedJSON = `{"title":"Bank - InPrivate","body":"protected-context-marker"}`
			case "paused-capture":
				app.saveObservationPauses([]observationPause{{Start: now - 100, End: now + 100}})
			case "malformed-parser":
				snap.Current.ParsedJSON = `{broken`
			case "unreadable-pauses":
				if err := app.store.Close(); err != nil {
					t.Fatal(err)
				}
			case "missing-source":
				snap.Current.ID, snap.Current.SessionID = 1, "deleted"
			}
			stashFocusedAppContext(t.Name(), snap)
			if got := app.injectFocusedAppContext(t.Name()); got != "" {
				t.Fatalf("privacy-excluded quick-chat context entered chat: %s", got)
			}
		})
	}
}

func TestFocusedChatContextFiltersPriorSourcesBeforeDedupAndPrompt(t *testing.T) {
	isolateActivityPrivacyTest(t)
	app := newPrivacyReadTestApp(t)
	store := app.activity.store
	now := time.Now().UnixMilli()
	var recent []*ActivitySnapshotRow
	for _, id := range []string{"safe-prior", "protected-prior"} {
		seedSnapshotForApp(t, store, id, "Notes", id+" marker", now-1000)
		parsed := map[string]any{"title": id + " marker", "body": id + " body", "metadata": map[string]string{"contentHash": id, "bundleId": "com.apple.Notes"}}
		raw, _ := json.Marshal(parsed)
		if _, err := store.db.Exec(`UPDATE activity_snapshots SET parsed_json=? WHERE session_id=?`, string(raw), id); err != nil {
			t.Fatal(err)
		}
		rows, err := store.GetSessionSnapshots(id)
		if err != nil || len(rows) != 1 {
			t.Fatalf("source rows=%v err=%v", rows, err)
		}
		recent = append(recent, &rows[0])
	}
	if _, err := store.db.Exec(`UPDATE activity_sessions SET app_names='["Private Notes"]' WHERE id='protected-prior'`); err != nil {
		t.Fatal(err)
	}
	snap := &focusedAppSnapshot{
		BundleID: "com.apple.Notes", AppName: "Notes", WindowTitle: "Current note",
		Current: &ActivitySnapshotRow{Timestamp: now, ParsedJSON: `{"dedupOfHash":"protected-prior"}`},
		Recent:  recent, CurrentFresh: true,
	}
	stashFocusedAppContext(t.Name(), snap)
	prompt := app.injectFocusedAppContext(t.Name())
	if strings.Contains(prompt, "protected-prior") || !strings.Contains(prompt, "safe-prior marker") {
		t.Fatalf("protected sibling reached prompt or safe history lost: %s", prompt)
	}
}

func TestFocusedChatRedactsBeforePromptCaps(t *testing.T) {
	isolateActivityPrivacyTest(t)
	prefix := strings.Repeat("x", 1489) + " "
	snap := &focusedAppSnapshot{AppName: "Notes", Current: &ActivitySnapshotRow{AXText: prefix + activityPrivacySecret}}
	prompt := formatFocusedAppContextForPrompt(snap, nil)
	if strings.Contains(prompt, "sk-proj-") || !strings.Contains(prompt, "[REDACTED:") {
		t.Fatalf("prompt cap split a credential before redaction: %s", prompt)
	}
}

func TestFocusedChatCaptureStopsBeforeProtectedAppRead(t *testing.T) {
	isolateActivityPrivacyTest(t)
	app := &App{store: newIdentityTestStore(t)}
	app.closedEyes.enabled.Store(true)
	if got := app.captureFocusedAppContext("com.apple.Notes", time.Second); got != nil {
		t.Fatal("paused app capture returned content")
	}
	app.closedEyes.enabled.Store(false)
	if got := app.captureFocusedAppContext("com.example.private", time.Second); got != nil {
		t.Fatal("sensitive app capture returned content")
	}
	// No model call is needed to verify privacy defaults when no safe
	// capture remains; the existing question API must return local fallbacks.
	runner := &modelPrivacyTaskRecorder{memoryTranslationFakeRunner: memoryTranslationFakeRunner{ready: true, response: `[]`}}
	app.ctx, app.runner = context.Background(), runner
	app.closedEyes.enabled.Store(true)
	if _, err := app.GetQuickChatSuggestionsForApp("com.apple.Notes", "en"); err != nil || len(runner.inputs) != 0 {
		t.Fatalf("paused focused-app suggestions reached model: calls=%d err=%v", len(runner.inputs), err)
	}
}
