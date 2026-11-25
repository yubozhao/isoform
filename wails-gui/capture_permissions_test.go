package main

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"log"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"wails-gui/internal/platform/activitymon"
	"wails-gui/internal/platform/browserhistory"
	"wails-gui/internal/platform/clipboard"
	"wails-gui/internal/platform/imessage"
	"wails-gui/storage"
)

// Break only the preference query. The source stores and model seams remain
// available, and the same service instances see the repair on their next tick.
func TestCapturePermissionFailureAndRecovery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "switches.db")
	st, err := storage.New(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	a := &App{store: st}
	ctx := t.Context()
	now := time.Now().Truncate(time.Second)
	for _, key := range []string{mailPermissionKey, filesPermissionKey, calendarPermissionKey, remindersPermissionKey, messagesPermissionKey, clipboardPermissionKey, "screen_recording", "accessibility", "screen_capture", "input_automation"} {
		if err := st.SetUIState(ctx, permissionUIStateKey(key), "true"); err != nil {
			t.Fatal(err)
		}
	}

	mail := newTestMailService(t, now, nil)
	mail.mail.out = mailSnapshotJSON(t, nil, nil)
	mail.mailSyncService.enabled = newMailSyncService(a).enabled
	files := newTestFilesReader(t, now)
	files.filesActivityReader.enabled = newFilesActivityReader(a).enabled
	events := &fakeCalendarSource{}
	calendar, _ := newCalendarTestService(t, events, now)
	calendar.wasGranted = true
	calendar.enabled = newCalendarSyncService(a).enabled
	webReads := 0
	calendar.webDetect = func(context.Context) { webReads++ }
	calendar.peekEnabled = newCalendarSyncService(a).peekEnabled
	reminderSource := &fakeReminderSource{}
	reminders, _ := newRemindersTestService(t, reminderSource, now)
	reminders.enabled = newRemindersSyncService(a).enabled
	messages := newIMessageFixture(t)
	messages.svc.enabled = newIMessageService(a).enabled
	messageReads := 0
	access := messages.svc.access
	messages.svc.access = func() imessage.State {
		messageReads++
		return access()
	}

	board := &resumeRaceBoard{count: 1}
	copies, frontSamples := 0, 0
	observer := &clipboardObserver{app: a}
	watcher := &clipboard.Watcher{Board: board, Allowed: observer.allowed,
		Front: func() clipboard.App {
			frontSamples++
			return clipboard.App{}
		},
		OnCopy: func(clipboard.Copy) { copies++ },
	}
	watcher.Tick()
	frontSamples = 0

	home := t.TempDir()
	writeChromeHistory(t, home, []browserhistory.Visit{{URL: "https://example.org/", At: now.Add(time.Second)}})
	history, _, _ := newTestHistoryReader(t, home, now)
	history.allowed = newBrowserHistoryReader(a).allowed
	driver := &fakePeekDriver{front: "com.apple.finder", browser: "com.google.Chrome", tree: googleWeekTree()}
	peek, _, _ := newPeekTestService(t, driver, now)
	productionPeek := newCalendarPeekService(a)
	peek.guard = func() string {
		// The lock and idle clock belong to the driver fixture. Exercise the
		// live switch without depending on the host's lock state.
		if !a.calendarPeekCaptureAllowed() {
			return "switched off"
		}
		return ""
	}

	monitor := &permissionTestMonitor{}
	a.activity = &activityMonitorState{monitor: monitor}

	for _, readable := range []bool{false, true} {
		stmt := "ALTER TABLE ui_state RENAME TO unavailable_ui_state"
		if readable {
			stmt = "ALTER TABLE unavailable_ui_state RENAME TO ui_state"
		}
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
		mail.syncIfAllowed(ctx, "interval")
		if _, err := files.readOnce(ctx); err != nil {
			t.Fatal(err)
		}
		calendar.syncIfAllowed(ctx, "interval")
		calendar.detectWeb(ctx)
		reminders.syncIfAllowed(ctx, "interval")
		messages.svc.syncIfAllowed(ctx, "interval")
		board.count++
		watcher.Tick()
		added, err := history.readOnce(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !readable && productionPeek.state(now).Enabled {
			t.Fatal("calendar peek scheduled during outage")
		}
		_, peekErr := peek.run(ctx, []string{"google"}, "idle")
		if readable && peekErr != nil {
			t.Fatal(peekErr)
		}
		// Stop at the native start boundary without installing a real input tap.
		err = a.startActivityMonitor()
		if readable != errors.Is(err, errPermissionTestMonitorStarted) {
			t.Fatalf("monitor start: %v", err)
		}

		for source, ran := range map[string]bool{
			"mail": mail.mail.runs > 0, "files": len(files.spot.queries) > 0,
			"calendar": events.calls > 0, "calendar web detection": webReads > 0,
			"reminders": !reminderSource.since.IsZero(), "messages": messageReads > 0,
			"messages model": len(messages.prompts) > 0, "clipboard front app": frontSamples > 0,
			"clipboard copy": copies > 0, "browser history": added > 0, "calendar peek": len(driver.readBundles) > 0,
			"activity monitor": monitor.starts > 0,
		} {
			if ran != readable {
				t.Errorf("%s ran = %t, switch readable = %t", source, ran, readable)
			}
		}
	}
}

var errPermissionTestMonitorStarted = errors.New("reached native start")

type permissionTestMonitor struct {
	runningMonitor
	starts int
}

func (m *permissionTestMonitor) Start(context.Context, func(activitymon.ActivityEvent)) error {
	m.starts++
	return errPermissionTestMonitorStarted
}

func TestCapturePermissionUnknownStreak(t *testing.T) {
	a := &App{}
	ctx := t.Context()
	var buf bytes.Buffer
	out := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(out)
	check := func(ctx context.Context) bool {
		return a.captureAllowed(ctx, "activity_monitor", "screen_recording", "accessibility")
	}
	if check(ctx) || check(ctx) {
		t.Fatal("nil store allowed capture")
	}
	a.store = newIdentityTestStore(t)
	if !check(ctx) {
		t.Fatal("absent default-on switches must be known")
	}
	if on, err := a.permissionSwitchRead(ctx, filesPermissionKey); on || err != nil {
		t.Fatalf("absent Files switch must be known off: on=%t err=%v", on, err)
	}
	failedRead, cancel := context.WithCancel(ctx)
	cancel()
	if check(failedRead) || check(failedRead) {
		t.Fatal("failed reads allowed capture")
	}
	if err := a.store.SetUIState(ctx, permissionUIStateKey("accessibility"), "false"); err != nil {
		t.Fatal(err)
	}
	if check(ctx) || check(failedRead) {
		t.Fatal("known off or a subsequent read failure allowed capture")
	}

	log.SetOutput(out)
	if got := strings.Count(buf.String(), "[capture] activity_monitor skipped: switch state unknown"); got != 3 {
		t.Fatalf("got %d outage messages, want 3", got)
	}
}

func TestActivityMonitorUnknownStartRetryStopsWithMonitor(t *testing.T) {
	am := &activityMonitorState{}
	a := &App{ctx: t.Context(), activity: am}
	if err := a.startActivityMonitor(); err != nil {
		t.Fatal(err)
	}
	am.startMu.Lock()
	first := am.startRetry
	am.startMu.Unlock()
	if first == nil {
		t.Fatal("missing retry for unknown initial switch")
	}
	if err := a.startActivityMonitor(); err != nil {
		t.Fatal(err)
	}
	am.startMu.Lock()
	same := am.startRetry == first
	am.startMu.Unlock()
	a.stopActivityMonitor()
	if !same || am.startRetry != nil {
		t.Fatal("expected one pending retry, cancelled by stop")
	}
}

func TestCalendarPeekCaptureReadsSavedWebSwitch(t *testing.T) {
	a := &App{store: newIdentityTestStore(t)}
	for _, value := range []string{"false", "true"} {
		if err := a.store.SetUIState(t.Context(), calendarPeekEnabledKey, value); err != nil {
			t.Fatal(err)
		}
		if got := a.calendarPeekCaptureAllowed(); got != (value == "true") {
			t.Fatalf("web switch %q: capture allowed = %t", value, got)
		}
	}
}
