package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"wails-gui/internal/pii"
	"wails-gui/internal/platform/activitymon"
)

func TestTriageSummaryRedactsBeforeTruncationAndExcludesProtectedSources(t *testing.T) {
	isolateActivityPrivacyTest(t)
	store := newTimelineTestStore(t)
	app := &App{activity: &activityMonitorState{store: store}}
	triager := &ActivityTriager{app: app}
	var sessions []ActivitySessionRow
	var events []activitymon.ActivityEvent
	for _, id := range []string{"public", "private"} {
		session := ActivitySessionRow{ID: id, StartTime: 1000, EndTime: 2000, AppNames: `["Notes"]`}
		event := activitymon.ActivityEvent{Kind: activitymon.EventSpeech, AppName: "Notes", AppBundleID: "com.apple.Notes", SpeechText: strings.Repeat("x", 280) + " " + activityPrivacySecret}
		ocr := strings.Repeat("y", 180) + " " + activityPrivacySecret
		if id == "private" {
			session.AppNames = `["Private Notes"]`
			event.AppName, event.AppBundleID, event.SpeechText = "Private Notes", "com.example.private", "excluded-private-speech"
			ocr = "excluded-private-ocr"
		}
		if err := store.SaveSession(session, []activitymon.ActivityEvent{event}, nil); err != nil {
			t.Fatal(err)
		}
		if err := store.SaveOCRFrame(OCRFrameRow{SessionID: id, Text: ocr, Confidence: 1}); err != nil {
			t.Fatal(err)
		}
		if id == "public" {
			sessions = append(sessions, session)
		}
		events = append(events, event)
	}
	summary, _, _ := triager.buildGroupSummary(sessionGroup{Sessions: sessions}, events, SessionContentProfile{})
	if strings.Contains(summary, "sk-proj-") || strings.Contains(summary, "excluded-private") || strings.Contains(summary, "Private Notes") {
		t.Fatalf("unsafe summary: %s", summary)
	}
	if !strings.Contains(summary, "[REDACTED:") || !strings.Contains(summary, "Spoke") {
		t.Fatalf("redacted useful speech missing: %s", summary)
	}
}

func TestTriageModelPassRedactsFinalPrompt(t *testing.T) {
	isolateActivityPrivacyTest(t)
	runner := &memoryTranslationFakeRunner{response: `{}`, ready: true}
	app := newTranslationTestApp(t, runner)
	triager := &ActivityTriager{app: app}
	_, _, _, _, err := triager.runActivityTriage(context.Background(), "Activity token "+activityPrivacySecret, "")
	if err != nil {
		t.Fatal(err)
	}
	if runner.calls != 1 || strings.Contains(runner.lastPrompt, activityPrivacySecret) || !strings.Contains(runner.lastPrompt, "[REDACTED:") {
		t.Fatalf("model did not receive a redacted prompt: calls=%d", runner.calls)
	}
}

func TestTriageProtectedHistoryStopsBeforeObserverAndModel(t *testing.T) {
	isolateActivityPrivacyTest(t)
	for _, protection := range []string{"sensitive-app", "protected-status"} {
		t.Run(protection, func(t *testing.T) {
			store := newTimelineTestStore(t)
			session := ActivitySessionRow{ID: protection, StartTime: 1000, EndTime: 5000, AppNames: `["Notes"]`, EventCount: triagerMinEvents, TriageStatus: "pending"}
			event := activitymon.ActivityEvent{Timestamp: 3000, Kind: activitymon.EventClick, AppName: "Notes", ClickLabel: "Retained historical content"}
			if protection == "sensitive-app" {
				event.AppBundleID = "com.example.private"
			} else {
				session.TriageStatus = "protected"
			}
			if err := store.SaveSession(session, []activitymon.ActivityEvent{event}, nil); err != nil {
				t.Fatal(err)
			}
			runner := &memoryTranslationFakeRunner{response: `{}`, ready: true}
			triager := NewActivityTriager(&App{runner: runner, activity: &activityMonitorState{store: store}}, nil)
			triager.triageGroup(context.Background(), sessionGroup{Sessions: []ActivitySessionRow{session}})
			triager.wg.Wait()
			if runner.calls != 0 {
				t.Fatalf("protected history reached the model: calls=%d", runner.calls)
			}
			episodes, err := store.ListTaskEpisodes("", 10)
			if err != nil || len(episodes) != 0 {
				t.Fatalf("protected history reached observer: episodes=%d err=%v", len(episodes), err)
			}
			saved, err := store.GetSessionByID(session.ID)
			if err != nil || saved == nil || saved.TriageStatus != session.TriageStatus {
				t.Fatalf("protected source status changed: session=%+v err=%v", saved, err)
			}
		})
	}
}

func TestTriageTypingFragmentsRedactAfterAssembly(t *testing.T) {
	isolateActivityPrivacyTest(t)
	triager := &ActivityTriager{app: &App{}}
	const split = len("sk-proj-abcd")
	events := []activitymon.ActivityEvent{
		{Kind: activitymon.EventKeyboard, AppName: "Notes", TypedText: activityPrivacySecret[:split]},
		{Kind: activitymon.EventKeyboard, AppName: "Notes", TypedText: activityPrivacySecret[split:]},
	}
	group := sessionGroup{Sessions: []ActivitySessionRow{{ID: "typing", AppNames: `["Notes"]`}}}
	summary, _, _ := triager.buildGroupSummary(group, events, SessionContentProfile{})
	if strings.Contains(summary, activityPrivacySecret[split:]) || !strings.Contains(summary, `[REDACTED:api_key]`) {
		t.Fatalf("assembled typing was not redacted: %s", summary)
	}
	if events[0].TypedText != activityPrivacySecret[:split] {
		t.Fatal("prompt assembly changed retained source events")
	}
}

func TestTriageSummaryRedactsBeforeHeadTailCap(t *testing.T) {
	isolateActivityPrivacyTest(t)
	triager := &ActivityTriager{app: &App{}}
	group := sessionGroup{Sessions: []ActivitySessionRow{{ID: "long-app", AppNames: `["marker"]`}}}
	baseline, _ := triager.buildGroupSummaryBody(group, nil, newSenderLabeler(nil))
	markerOffset := strings.Index(baseline, "marker")
	if markerOffset < 0 {
		t.Fatal("missing app label in summary fixture")
	}
	// Place a credential across the head cap in one unbroken source field.
	// Redacting only after the cap leaves the incomplete prefix "sk-pr".
	const headBudget = 200_000 - 40_000 - 200
	appLabel := strings.Repeat(".", headBudget-markerOffset-5) + activityPrivacySecret + strings.Repeat(".", 90_000)
	apps, err := json.Marshal([]string{appLabel})
	if err != nil {
		t.Fatal(err)
	}
	group.Sessions[0].AppNames = string(apps)
	summary, _ := triager.buildGroupSummaryBody(group, nil, newSenderLabeler(nil))
	if strings.Contains(summary, "sk-pr") {
		t.Fatal("summary cap exposed a credential fragment")
	}
	if !strings.Contains(summary, "mid-session activity omitted") {
		t.Fatal("fixture did not exercise summary cap")
	}
}

func TestTriageSelectedFrameAuditMatchesRedactedPrompt(t *testing.T) {
	isolateActivityPrivacyTest(t)
	store := newTimelineTestStore(t)
	session := ActivitySessionRow{ID: "parsed-audit", StartTime: 1000, EndTime: 5000, AppNames: `["Notes"]`}
	parsed, err := json.Marshal(map[string]any{
		"title": "Credential " + activityPrivacySecret,
		"url":   "https://example.com/?token=" + activityPrivacySecret,
		"body":  strings.Repeat("Useful document content. ", 15),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSessionWithSnapshots(session, []ActivitySnapshotRow{{SessionID: session.ID, Timestamp: 2000, App: "Notes", ParsedJSON: string(parsed)}}); err != nil {
		t.Fatal(err)
	}
	triager := &ActivityTriager{app: &App{activity: &activityMonitorState{store: store}}}
	summary, frames, _ := triager.buildGroupSummary(sessionGroup{Sessions: []ActivitySessionRow{session}}, nil, SessionContentProfile{})
	if len(frames) != 1 || frames[0].PromptText == "" {
		t.Fatalf("missing parsed audit frame: %+v", frames)
	}
	for _, text := range []string{summary, frames[0].PromptText, frames[0].Label, frames[0].BrowserURL} {
		if strings.Contains(text, activityPrivacySecret) || !strings.Contains(text, "[REDACTED:") {
			t.Fatalf("prompt/audit field not redacted: %s", text)
		}
	}
	if !strings.Contains(summary, frames[0].PromptText) {
		t.Fatal("audit text differs from redacted model input")
	}
}

func TestTriageGenerationRechecksPrivacyAfterAdmission(t *testing.T) {
	for _, kind := range []string{"memory", "knowledge"} {
		t.Run(kind, func(t *testing.T) {
			isolateActivityPrivacyTest(t)
			store := newTimelineTestStore(t)
			session := ActivitySessionRow{ID: "admitted-source", StartTime: 1000, EndTime: 5000, AppNames: `["Notes"]`}
			if err := store.SaveSession(session, nil, nil); err != nil {
				t.Fatal(err)
			}
			runner := &memoryTranslationFakeRunner{response: `{}`, ready: true}
			app := newTranslationTestApp(t, runner)
			app.activity = &activityMonitorState{store: store}
			triager := NewActivityTriager(app, nil)
			group := sessionGroup{Sessions: []ActivitySessionRow{session}}
			if !triager.triageGroupAllowsModelRead(group) {
				t.Fatal("public source rejected before settings change")
			}
			run := newGroupPipelineRun(group, time.Now())
			run.MemoryStatus, run.KnowledgeStatus = "dispatched", "dispatched"
			triager.recordPipelineRun(&run)
			if run.ID == 0 {
				t.Fatal("missing dispatched pipeline record")
			}
			pii.AddUserSensitiveApp("com.apple.Notes", "Notes")
			switch kind {
			case "memory":
				triager.generateAndSaveMemory(context.Background(), run.ID, group, "Previously admitted private context", SessionContentProfile{})
			case "knowledge":
				triager.generateAndSaveKnowledge(context.Background(), run.ID, group, "Previously admitted private context", SessionContentProfile{})
			}
			if runner.calls != 0 {
				t.Fatalf("follow-on %s generation sent protected context to a model", kind)
			}
			saved, err := store.GetActivityPipelineRunByID(run.ID)
			if err != nil {
				t.Fatalf("load pipeline outcome: %v", err)
			}
			status, reason := saved.MemoryStatus, saved.MemoryReason
			if kind == "knowledge" {
				status, reason = saved.KnowledgeStatus, saved.KnowledgeReason
			}
			if status != "skipped" || !strings.Contains(reason, "privacy settings") {
				t.Fatalf("privacy rejection did not finish dispatched outcome: status=%s reason=%s", status, reason)
			}
		})
	}
}
