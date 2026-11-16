package main

import (
	"testing"
	"time"

	"wails-gui/internal/activitybuf"
	"wails-gui/internal/platform/activitymon"
	"wails-gui/internal/privacy"
)

func newProtectedWithdrawalTestApp(t *testing.T) (*App, *ActivityStore) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv(topicsStateDirEnvVar, t.TempDir())
	store := newLifecycleStore(t)
	app := mainChatTestApp(t)
	app.activity = &activityMonitorState{
		buf:     activitybuf.NewRingBuffer(16, time.Minute),
		store:   store,
		capture: failingScreenCapture{},
	}
	return app, store
}

func pushProtectedWithdrawalTestEvent(app *App, appName, bundleID string) {
	app.activity.buf.Push(activitymon.ActivityEvent{
		Timestamp:   time.Now().UnixMilli(),
		Kind:        activitymon.EventKeyboard,
		AppName:     appName,
		AppBundleID: bundleID,
		KeyChars:    []string{"typed"},
	})
}

func protectedWithdrawalSession(t *testing.T, store *ActivityStore) ActivitySessionRow {
	t.Helper()
	rows, err := store.GetSessionsByAge()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("sessions=%+v, want one extracted session", rows)
	}
	return rows[0]
}

func TestExtractSessionSensitiveAppPublishesWithdrawal(t *testing.T) {
	app, store := newProtectedWithdrawalTestApp(t)
	events := recordPrivacyWithdrawals(t)
	pushProtectedWithdrawalTestEvent(app, "1Password", "com.1password.1password")

	app.extractSession("periodic_flush")

	session := protectedWithdrawalSession(t, store)
	if session.TriageStatus != "protected" {
		t.Fatalf("triage status=%q, want protected", session.TriageStatus)
	}
	requirePrivacyWithdrawal(t, events(), privacy.Scope{
		Kind: privacy.Session,
		ID:   session.ID,
	})
}

func TestExtractSessionProtectedMeetingMetadataPublishesWithdrawals(t *testing.T) {
	app, store := newProtectedWithdrawalTestApp(t)
	events := recordPrivacyWithdrawals(t)
	writer, err := newMeetingAudioWriter(store.AudioDir())
	if err != nil {
		t.Fatal(err)
	}
	startMs := time.Now().Add(-time.Minute).UnixMilli()
	writer.SetMeeting("Zoom", "us.zoom.xos", "meeting-protected")
	writer.mu.Lock()
	writer.meta.StartMs = startMs
	writer.meta.ActivitySessions = []string{"earlier-rotation"}
	writer.meta.Protected = true
	writer.writeMetaLocked()
	writer.mu.Unlock()

	app.activity.mu.Lock()
	app.activity.inMeeting = true
	app.activity.meetingAppName = "Zoom"
	app.activity.meetingBundleID = "us.zoom.xos"
	app.activity.meetingID = "meeting-protected"
	app.activity.meetingStartMs = startMs
	app.activity.meetingAudioWriter = writer
	app.activity.mu.Unlock()
	pushProtectedWithdrawalTestEvent(app, "Zoom", "us.zoom.xos")

	app.extractSession("meeting_ended")

	session := protectedWithdrawalSession(t, store)
	if session.TriageStatus != "protected" {
		t.Fatalf("triage status=%q, want protected", session.TriageStatus)
	}
	got := events()
	want := []privacy.Scope{
		{Kind: privacy.Session, ID: session.ID},
		{Kind: privacy.Session, ID: "earlier-rotation"},
		{Kind: privacy.TimeRange, StartMs: startMs, EndMs: session.EndTime},
	}
	if len(got) != len(want) {
		t.Fatalf("withdrawal scopes=%+v, want %+v", got, want)
	}
	for i := range want {
		requirePrivacyWithdrawal(t, []privacy.Withdrawal{got[i]}, want[i])
	}
}

func TestExtractSessionOrdinarySessionPublishesNoWithdrawal(t *testing.T) {
	app, store := newProtectedWithdrawalTestApp(t)
	events := recordPrivacyWithdrawals(t)
	pushProtectedWithdrawalTestEvent(app, "Code", "com.example.code")

	app.extractSession("periodic_flush")

	session := protectedWithdrawalSession(t, store)
	if session.TriageStatus == "protected" {
		t.Fatalf("ordinary session unexpectedly protected: %+v", session)
	}
	if got := events(); len(got) != 0 {
		t.Fatalf("ordinary session published withdrawals: %+v", got)
	}
}

func TestExtractSessionInsertFailurePublishesNoWithdrawal(t *testing.T) {
	app, store := newProtectedWithdrawalTestApp(t)
	if _, err := store.db.Exec(`
		CREATE TRIGGER fail_protected_withdrawal_insert
		BEFORE INSERT ON activity_sessions
		BEGIN
			SELECT RAISE(ABORT, 'injected protected session insert failure');
		END;
	`); err != nil {
		t.Fatal(err)
	}
	events := recordPrivacyWithdrawals(t)
	pushProtectedWithdrawalTestEvent(app, "1Password", "com.1password.1password")

	app.extractSession("periodic_flush")

	if got := events(); len(got) != 0 {
		t.Fatalf("failed session insert published withdrawals: %+v", got)
	}
	var count int
	if err := store.db.QueryRow("SELECT COUNT(*) FROM activity_sessions").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("persisted sessions=%d after failed insert, want 0", count)
	}
}

func TestExtractSessionWithdrawalRemovesTodayTopic(t *testing.T) {
	app, _ := newProtectedWithdrawalTestApp(t)
	svc := app.topicsSvc()
	svc.privacyWithdrawalUnsubscribe()
	var seedErr error
	svc.privacyWithdrawalUnsubscribe = subscribePrivacyWithdrawals(func(withdrawal privacy.Withdrawal) {
		if withdrawal.Scope.Kind == privacy.Session {
			policyHash, err := currentTopicPolicyHash()
			if err != nil {
				seedErr = err
				return
			}
			seedErr = svc.snapshots.Replace(&topicSnapshot{
				Version:    topicSnapshotSchemaVersion,
				PolicyHash: policyHash,
				At:         time.Now(),
				Topics: []topicSnapshotEntry{
					{
						Topic: TopicView{ID: "protected-topic", Name: "Protected", PinnedToSidebar: true},
						Refs:  []topicSourceReference{{Kind: topicRefSession, Value: withdrawal.Scope.ID}},
					},
					{
						Topic: TopicView{ID: "unrelated-topic", Name: "Unrelated", PinnedToSidebar: true},
						Refs:  []topicSourceReference{{Kind: topicRefSession, Value: "unrelated-session"}},
					},
				},
			})
		}
		svc.applyPrivacyWithdrawal(withdrawal.Scope)
	})
	t.Cleanup(svc.privacyWithdrawalUnsubscribe)
	events := recordPrivacyWithdrawals(t)
	pushProtectedWithdrawalTestEvent(app, "1Password", "com.1password.1password")

	app.extractSession("periodic_flush")

	if seedErr != nil {
		t.Fatalf("seed published snapshot from withdrawal: %v", seedErr)
	}
	session := protectedWithdrawalSession(t, app.activity.store)
	requirePrivacyWithdrawal(t, events(), privacy.Scope{
		Kind: privacy.Session,
		ID:   session.ID,
	})
	today, err := app.GetTodayTopics()
	if err != nil {
		t.Fatal(err)
	}
	if len(today.Topics) != 1 || today.Topics[0].ID != "unrelated-topic" {
		t.Fatalf("Today topics after protected-session withdrawal=%+v, want only unrelated topic", today.Topics)
	}
}
