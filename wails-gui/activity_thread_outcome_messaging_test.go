package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	briefcore "wails-gui/internal/brief"
	"wails-gui/internal/pii"
	"wails-gui/internal/platform/activitymon"
)

type chatThreadSegment struct {
	app    string
	frames []string
}

// seedChatThread stores one activity made of one run per segment, each with
// its own session and OCR frames, in order.
func seedChatThread(t *testing.T, app *App, id string, segments []chatThreadSegment, now time.Time) ActivityRow {
	t.Helper()
	store := app.activity.store
	start := now.Add(-time.Hour).UnixMilli()
	span := int64(time.Hour/time.Millisecond) / int64(len(segments))
	var runIDs []string
	dwell := map[string]int64{}
	for i, segment := range segments {
		from, to := start+int64(i)*span, start+int64(i+1)*span-1
		sessionID := id + "-session-" + string(rune('a'+i))
		events := []activitymon.ActivityEvent{{Kind: activitymon.EventFocus, Timestamp: from, AppName: segment.app, AppBundleID: "com.example." + strings.ToLower(segment.app), WindowTitle: segment.app}}
		apps, _ := json.Marshal([]string{segment.app})
		if err := store.SaveSession(ActivitySessionRow{ID: sessionID, StartTime: from, EndTime: to, AppNames: string(apps), TriageStatus: "done"}, events, nil); err != nil {
			t.Fatal(err)
		}
		run := testRun(id+"-run-"+string(rune('a'+i)), segment.app, from, to, to-from, len(events))
		run.SessionIDs = []string{sessionID}
		if err := store.db.QueryRow(`SELECT MIN(id),MAX(id) FROM activity_events WHERE session_id=?`, sessionID).Scan(&run.FirstEventRowID, &run.LastEventRowID); err != nil {
			t.Fatal(err)
		}
		insertComposableTimelineRun(t, store, run, ActivityTimelineGroupSummary{Title: segment.app + " work"})
		for j, text := range segment.frames {
			result, err := store.db.Exec(`INSERT INTO activity_snapshots(session_id,timestamp,file_path,app,storage_tier) VALUES(?,?,'',?,'hot')`, sessionID, from+int64(j+1)*1000, segment.app)
			if err != nil {
				t.Fatal(err)
			}
			snapshotID, _ := result.LastInsertId()
			if err := store.SaveOCRFrame(OCRFrameRow{SessionID: sessionID, SegmentID: snapshotID, FrameTime: float64(j + 1), Text: text, Confidence: .99}); err != nil {
				t.Fatal(err)
			}
		}
		runIDs = append(runIDs, run.ID)
		dwell[segment.app] += to - from
	}
	active, _ := json.Marshal(dwell)
	row := ActivityRow{ID: id, Title: "Launch planning", Status: activityStatusIdle, RunIDs: composeJSON(runIDs), ActiveMsByApp: string(active), StartMs: start, EndMs: start + int64(len(segments))*span - 1, CreatedAt: start, UpdatedAt: start + int64(len(segments))*span - 1}
	if err := store.InsertActivity(row); err != nil {
		t.Fatal(err)
	}
	return row
}

const chatAskText = "Priya: can you send me the launch deck by Friday? thanks"

// chatReply is a model reply that cites the latest chat frame.
func chatReply(evidence []threadOutcomeEvidence, kind, state, summary, nextStep string) string {
	var cite threadOutcomeEvidence
	for _, item := range evidence {
		if item.Messaging && item.SourceKind == "activity_snapshot" {
			cite = item
		}
	}
	raw, _ := json.Marshal(map[string]any{"kind": kind, "state": state, "summary": summary, "nextStep": nextStep, "action": "reply", "confidence": .9,
		"evidence": []map[string]string{{"ref": cite.Ref, "quote": "launch deck by Friday"}}})
	return string(raw)
}

func setMessagingOutcomes(t *testing.T, on bool) {
	t.Helper()
	dir, err := mobileBriefStateDir()
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"version": mobileBriefScheduleVersion, "messagingOutcomes": on})
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, mobileBriefScheduleFileName), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

// A chat's ask reaches the brief writer as a paraphrase: who asked for what
// and by when. A reply that quotes the chat, repeats a run of it, or reports
// the chat as another kind of outcome is refused, so no quote reaches the
// writer.
func TestThreadOutcomeChatAskReachesTheWriterAsParaphrase(t *testing.T) {
	isolateActivityPrivacyTest(t)
	t.Setenv("YANSU_MOBILE_STATE_DIR", t.TempDir())
	app := newPrivacyReadTestApp(t)
	now := mobileBriefTestDay(18, 0)
	row := seedChatThread(t, app, "chat", []chatThreadSegment{{app: "WeChat", frames: []string{"Priya: morning", chatAskText}}}, now)
	evidence, ok := app.threadOutcomeEvidence(t.Context(), row)
	if !ok || len(evidence) == 0 {
		t.Fatalf("chat evidence = %+v, %v", evidence, ok)
	}
	for _, item := range evidence {
		if !item.Messaging {
			t.Fatalf("a chat frame is not marked: %+v", item)
		}
	}
	if prompt := buildThreadOutcomePrompt(evidence, "English", row.Title); !strings.Contains(prompt, threadOutcomeMessagingRule) {
		t.Fatal("the messaging rule is missing from a chat prompt")
	}
	for name, reply := range map[string]string{
		"quoted":   chatReply(evidence, "conversation", "asked", `Priya wrote "send me the launch deck".`, "Send Priya the launch deck"),
		"repeated": chatReply(evidence, "conversation", "asked", "Priya: can you send me the launch deck by Friday.", "Send Priya the launch deck"),
		"as work":  chatReply(evidence, "document", "draft", "You were drafting the launch deck for Priya.", "Send Priya the launch deck"),
	} {
		if outcome, err := parseThreadOutcome(reply, evidence); err == nil || outcome != nil {
			t.Fatalf("%s reply was kept: %+v", name, outcome)
		}
	}
	paraphrase := chatReply(evidence, "conversation", "asked", "Priya asked you for the launch deck by Friday.", "Send Priya the launch deck")
	outcome, err := parseThreadOutcome(paraphrase, evidence)
	if err != nil || outcome == nil {
		t.Fatalf("paraphrase refused: %v", err)
	}
	record := activityThreadOutcomeRecord{InputHash: threadOutcomeInputHash(row, evidence), AttemptedAt: row.EndMs, Outcome: outcome}
	if err := app.activity.store.saveThreadOutcome(t.Context(), row, record); err != nil {
		t.Fatal(err)
	}
	saved, err := app.activity.store.GetActivityByID(row.ID)
	if err != nil || saved == nil {
		t.Fatal(err)
	}
	if strings.Contains(saved.threadOutcomeRecord, "can you send me") {
		t.Fatal("the stored outcome keeps chat text")
	}
	in := mobileBriefInput{Slot: mobileBriefSlotEvening, AsOf: now, WindowStart: now.Add(-12 * time.Hour), Location: time.UTC, Timezone: "UTC"}
	collectMobileBriefRecap(app, &in)
	brief := buildMobileBriefV1(in)
	m, _ := newBriefMaterial(in, brief, briefPartDone, "")
	found := false
	for _, item := range m.Items {
		raw, _ := json.Marshal(item)
		if strings.Contains(string(raw), "can you send me") || strings.Contains(string(raw), "thanks") {
			t.Fatalf("chat text reached the writer: %s", raw)
		}
		if strings.Contains(item.Detail, "Priya asked you for the launch deck by Friday.") {
			found = true
			if item.Group != briefcore.GroupFollowUp || !strings.Contains(item.Detail, "Send Priya the launch deck") {
				t.Fatalf("the ask is not a follow-up with its next step: %+v", item)
			}
		}
	}
	if !found {
		t.Fatalf("the ask did not reach the writer: %+v", m.Items)
	}
	for _, block := range []string{"closed", "excluded"} {
		switch block {
		case "closed":
			app.closedEyes.enabled.Store(true)
		case "excluded":
			app.closedEyes.enabled.Store(false)
			prior := pii.GetUserSensitiveApps()
			pii.SetUserSensitiveApps(map[string]string{"com.example.wechat": "WeChat"})
			t.Cleanup(func() { pii.SetUserSensitiveApps(prior) })
		}
		if got := app.readableThreadOutcome(t.Context(), *saved); got != nil {
			t.Fatalf("%s: the chat outcome is still readable: %+v", block, got)
		}
		in := mobileBriefInput{Slot: mobileBriefSlotEvening, AsOf: now, WindowStart: now.Add(-12 * time.Hour), Location: time.UTC, Timezone: "UTC"}
		collectMobileBriefRecap(app, &in)
		for _, c := range in.Conversations {
			if strings.Contains(c.writerDetail+c.NextStep, "Priya") {
				t.Fatalf("%s: the ask reached the brief: %+v", block, c)
			}
		}
	}
}

// The rollback: with messagingOutcomes off no chat segment is read.
func TestThreadOutcomeMessagingOffLeavesChatUnread(t *testing.T) {
	isolateActivityPrivacyTest(t)
	t.Setenv("YANSU_MOBILE_STATE_DIR", t.TempDir())
	setMessagingOutcomes(t, false)
	app := newPrivacyReadTestApp(t)
	now := mobileBriefTestDay(18, 0)
	row := seedChatThread(t, app, "chat-off", []chatThreadSegment{{app: "Notes", frames: []string{"Launch plan: final checklist"}}, {app: "WeChat", frames: []string{chatAskText}}}, now)
	evidence, ok := app.threadOutcomeEvidence(t.Context(), row)
	if !ok {
		t.Fatal("the work segment is held")
	}
	for _, item := range evidence {
		if item.Messaging || strings.Contains(item.Text, "Priya") {
			t.Fatalf("a chat segment was read with messagingOutcomes off: %+v", item)
		}
	}
	if len(evidence) == 0 {
		t.Fatal("the work segment was not read")
	}
}

// Per segment: a glance at a chat no longer holds the work session around it,
// and its chat frames never count as work evidence. Mail still holds it all.
func TestThreadOutcomeChatGlanceDoesNotBlockWork(t *testing.T) {
	isolateActivityPrivacyTest(t)
	t.Setenv("YANSU_MOBILE_STATE_DIR", t.TempDir())
	app := newPrivacyReadTestApp(t)
	now := mobileBriefTestDay(18, 0)
	row := seedChatThread(t, app, "mixed", []chatThreadSegment{{app: "Notes", frames: []string{"Launch plan: final checklist"}}, {app: "WeChat", frames: []string{chatAskText}}}, now)
	evidence, ok := app.threadOutcomeEvidence(t.Context(), row)
	if !ok {
		t.Fatal("a chat glance held the whole work session")
	}
	work, chat := 0, 0
	for _, item := range evidence {
		if item.Messaging {
			chat++
		} else {
			work++
			if strings.Contains(item.Text, "Priya") {
				t.Fatalf("chat text counted as work evidence: %+v", item)
			}
		}
	}
	if work == 0 || chat == 0 {
		t.Fatalf("work %d, chat %d frames", work, chat)
	}
	mail := seedChatThread(t, app, "mail", []chatThreadSegment{{app: "Notes", frames: []string{"Launch plan"}}, {app: "Mail", frames: []string{"Inbox"}}}, now)
	if _, ok := app.threadOutcomeEvidence(t.Context(), mail); ok {
		t.Fatal("mail no longer holds the thread")
	}
}
