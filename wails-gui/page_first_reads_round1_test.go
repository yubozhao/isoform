package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"wails-gui/storage"
)

func allowPageWarmer(tb testing.TB, a *App) {
	tb.Helper()
	ctx := context.Background()
	for key, value := range map[string]string{
		onboardingStateKey:                       `{"completed":true}`,
		triageEnableMemoryKey:                    "true",
		triageEnableKnowledgeKey:                 "true",
		triageEnableSOPKey:                       "true",
		permissionUIStateKey("screen_recording"): "true",
	} {
		if err := a.store.SetUIState(ctx, key, value); err != nil {
			tb.Fatal(err)
		}
	}
	path, err := contactsConsentPath()
	if err != nil {
		tb.Fatal(err)
	}
	if err := recordContactsConsent(path, "people", time.Now()); err != nil {
		tb.Fatal(err)
	}
}

// Permission display/onboarding guards need the real session store, not the
// unrelated Activity migration history. The held-reader guard below still uses
// both real stores. Keep lifecycle tests affordable under the race detector.
func newPageFirstReadTestApp(t *testing.T) *App {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("YANSU_MOBILE_STATE_DIR", filepath.Join(home, "mobile"))
	store, err := storage.New(filepath.Join(home, "sessions.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	a := &App{ctx: context.Background(), store: store}
	t.Cleanup(a.stopPageFirstReads)
	return a
}

func TestPageWarmerRequiresCompletionAndReschedulesAfterOnboarding(t *testing.T) {
	for _, raw := range []string{"", "invalid", "{}", `{"completed":false}`, `{"neverShowAgain":true}`} {
		t.Run(raw, func(t *testing.T) {
			a := newPageFirstReadTestApp(t)
			defer a.stopPageFirstReads()
			if err := a.store.SetUIState(t.Context(), onboardingStateKey, raw); err != nil {
				t.Fatal(err)
			}
			a.pageFirstReadsReady()
			a.pageFirstReadsPainted()
			if a.pageWarm.started || a.pageWarm.done != nil {
				t.Fatal("personal page worker started during onboarding")
			}
			if err := a.SaveUIState(onboardingStateKey, `{"completed":true}`); err != nil {
				t.Fatal(err)
			}
			if !a.pageWarm.started || a.pageWarm.done == nil {
				t.Fatal("completion did not reschedule the first-paint worker")
			}
		})
	}
}

func TestPageWarmerCompletionDoesNotInventFirstPaintOrServiceReadiness(t *testing.T) {
	for _, ready := range []bool{false, true} {
		t.Run(map[bool]string{false: "services", true: "paint"}[ready], func(t *testing.T) {
			a := newPageFirstReadTestApp(t)
			defer a.stopPageFirstReads()
			if ready {
				a.pageFirstReadsReady()
			} else {
				a.pageFirstReadsPainted()
			}
			if err := a.SaveUIState(onboardingStateKey, `{"completed":true}`); err != nil {
				t.Fatal(err)
			}
			if a.pageWarm.started {
				t.Fatal("completion bypassed service or first-paint gate")
			}
		})
	}
}

func TestPageWarmerConsentFailsClosedAndRechecksEveryStep(t *testing.T) {
	a := newMCPReadTestApp(t)
	if got := len(a.pageFirstReadSteps(t.Context())); got != 6 {
		t.Fatalf("scheduled optional pages without affirmative consent: %d steps", got)
	}
	for _, consent := range []string{triageEnableMemoryKey, triageEnableKnowledgeKey, triageEnableSOPKey, "activity", "people"} {
		if a.pageWarmConsent(t.Context(), consent) {
			t.Fatalf("missing consent admitted %s", consent)
		}
	}
	allowPageWarmer(t, a)
	if got := len(a.pageFirstReadSteps(t.Context())); got != 14 {
		t.Fatalf("affirmative consent did not admit page steps: %d", got)
	}
	for _, consent := range []string{triageEnableMemoryKey, triageEnableKnowledgeKey, triageEnableSOPKey, "activity", "people"} {
		if !a.pageWarmConsent(t.Context(), consent) {
			t.Fatalf("affirmative consent withheld %s", consent)
		}
	}
	// Already-built steps must observe a subsequent onboarding reset. A held
	// reader makes an accidental personal read observable without a mock.
	steps := a.pageFirstReadSteps(context.Background())
	if err := a.store.SetUIState(t.Context(), onboardingStateKey, "{}"); err != nil {
		t.Fatal(err)
	}
	a.activity.store.reader().SetMaxOpenConns(1)
	conn, err := a.activity.store.reader().Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	steps[3](ctx)
	if ctx.Err() != nil {
		t.Fatal("a scheduled Activity step read after onboarding was reset")
	}
	if err := a.store.SetUIState(t.Context(), onboardingStateKey, `{"completed":true}`); err != nil {
		t.Fatal(err)
	}
	if err := a.store.SetUIState(t.Context(), permissionUIStateKey("screen_recording"), "false"); err != nil {
		t.Fatal(err)
	}
	steps[3](ctx)
	if ctx.Err() != nil {
		t.Fatal("a scheduled Activity step read after consent was revoked")
	}
}

func TestPageFirstReadFilesKeepTheirScheduledRoot(t *testing.T) {
	a := newPageFirstReadTestApp(t)
	allowPageWarmer(t, a)
	steps := a.pageFirstReadSteps(t.Context())
	nextHome := t.TempDir()
	t.Setenv("HOME", nextHome)
	steps[7](t.Context()) // About you directory metadata.
	if _, err := os.Stat(filepath.Join(nextHome, configDirName, "memory")); !os.IsNotExist(err) {
		t.Fatal("queued metadata read followed HOME or created a directory")
	}
}

func TestPageWarmerHeldSQLiteReaderCancelsBeforeCriticalShutdownFlush(t *testing.T) {
	a := newMCPReadTestApp(t)
	allowPageWarmer(t, a)
	prev := agentSegmentWriteDelay
	agentSegmentWriteDelay = time.Hour
	defer func() { agentSegmentWriteDelay = prev }()
	main, err := a.ensureMainChat(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var itemID string
	a.onAgentEvent(func(event AgentEvent) {
		if event.Type == "assistant" && itemID == "" {
			itemID = event.ItemID
			if _, err := a.store.AppendStreamItem(t.Context(), main.LocalID, storage.StreamItemRow{ID: itemID, Kind: "assistant", Content: event.Content}); err != nil {
				t.Error(err)
			}
		}
	})
	a.emitAgentEvent(AgentEvent{Type: "assistant", Tag: main.LocalID, Content: "Pending "})
	a.emitAgentEvent(AgentEvent{Type: "assistant", Tag: main.LocalID, Content: "reply"})

	reader := a.activity.store.reader()
	reader.SetMaxOpenConns(1)
	conn, err := reader.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	held, err := conn.QueryContext(t.Context(), "SELECT 1 UNION ALL SELECT 2")
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	if !held.Next() {
		t.Fatal("could not hold a SQLite reader")
	}
	ctx, cancel := context.WithCancel(t.Context())
	a.pageWarm.cancel, a.pageWarm.done = cancel, make(chan struct{})
	done := a.pageWarm.done
	go func() {
		defer close(done)
		a.pageFirstReadSteps(context.Background())[3](ctx)
	}()
	deadline := time.Now().Add(time.Second)
	for reader.Stats().WaitCount == 0 {
		if time.Now().After(deadline) {
			t.Fatal("warm query did not reach held SQLite reader")
		}
		time.Sleep(time.Millisecond)
	}
	started := time.Now()
	a.flushAndStopPageFirstReads()
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("preload teardown consumed critical exit budget: %s", elapsed)
	}
	select {
	case <-done:
	default:
		t.Fatal("SQLite read did not inherit warm cancellation")
	}
	rows, err := a.store.LoadStreamItems(t.Context(), main.LocalID)
	if err != nil || len(rows) != 1 || rows[0].Content != "Pending reply" {
		t.Fatalf("pending reply was not flushed before shutdown: rows=%v err=%v", rows, err)
	}
}

func TestPagePermissionShutdownBoundsUninterruptibleNativeProbe(t *testing.T) {
	a := newPageFirstReadTestApp(t)
	entered, release := make(chan struct{}), make(chan struct{})
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	_, err := a.pageNativePermissionsContext(ctx, func() PlatformPermissions {
		close(entered)
		<-release
		return permissionSnapshotFixture(true)
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cold native wait was not bounded: %v", err)
	}
	<-entered
	started := time.Now()
	a.stopPageFirstReads()
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("native join was not bounded: %s", elapsed)
	}
	close(release)
	awaitPagePermissionRefresh(t, a)
	path, _ := pagePermissionSnapshotPath()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("late native probe published after shutdown")
	}
}

func TestPageFirstReadShutdownFlushPrecedesUninterruptibleJoin(t *testing.T) {
	a := newPageFirstReadTestApp(t)
	prev := agentSegmentWriteDelay
	agentSegmentWriteDelay = time.Hour
	defer func() { agentSegmentWriteDelay = prev }()
	main, err := a.ensureMainChat(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var firstID string
	a.onAgentEvent(func(event AgentEvent) {
		if event.Type == "assistant" && firstID == "" {
			firstID = event.ItemID
			_, err := a.store.AppendStreamItem(t.Context(), main.LocalID, storage.StreamItemRow{ID: event.ItemID, Kind: "assistant", Content: event.Content})
			if err != nil {
				t.Error(err)
			}
		}
	})
	a.emitAgentEvent(AgentEvent{Type: "assistant", Tag: main.LocalID, Content: "Reply "})
	a.emitAgentEvent(AgentEvent{Type: "assistant", Tag: main.LocalID, Content: "saved"})
	// Retain an uninterruptible native join through the whole observation.
	release := make(chan struct{})
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	_, _ = a.pageNativePermissionsContext(ctx, func() PlatformPermissions {
		<-release
		return permissionSnapshotFixture(true)
	})
	shutdown := make(chan struct{})
	go func() {
		a.flushAndStopPageFirstReads()
		close(shutdown)
	}()
	// "saved" exists only in the backend's open-turn buffer until flush.
	select {
	case <-shutdown:
		t.Error("native join finished before its held probe")
	case <-time.After(30 * time.Millisecond):
	}
	rows, err := a.store.LoadStreamItems(t.Context(), main.LocalID)
	if err != nil || len(rows) != 1 || rows[0].Content != "Reply saved" {
		t.Errorf("reply was unavailable during native join: rows=%v err=%v", rows, err)
	}
	close(release)
	awaitPagePermissionRefresh(t, a)
	select {
	case <-shutdown:
	case <-time.After(time.Second):
		t.Fatal("bounded shutdown did not finish")
	}
}

func TestPagePermissionEveryAdmissionRejectsAgeBuildAndSchema(t *testing.T) {
	for _, location := range []string{"memory", "disk"} {
		for _, invalid := range []string{"expired", "future", "build", "schema", "legacy"} {
			t.Run(location+"/"+invalid, func(t *testing.T) {
				a := newPageFirstReadTestApp(t)
				a.pageNativePermissions(func() PlatformPermissions { return permissionSnapshotFixture(false) })
				path, _ := pagePermissionSnapshotPath()
				a.pagePermissions.mu.Lock()
				saved := *a.pagePermissions.value
				switch invalid {
				case "expired":
					saved.At = time.Now().Add(-25 * time.Hour)
				case "future":
					saved.At = time.Now().Add(time.Hour)
				case "build":
					saved.Build = "previous-build"
				case "schema":
					saved.Schema = "previous-schema"
				case "legacy":
					saved.Version = 1
				}
				a.pagePermissions.value = &saved
				a.pagePermissions.mu.Unlock()
				raw, _ := json.Marshal(saved)
				if err := os.WriteFile(path, raw, 0600); err != nil {
					t.Fatal(err)
				}
				if location == "disk" {
					a = &App{store: a.store}
				}
				entered, release := make(chan struct{}), make(chan struct{})
				result := make(chan PlatformPermissions, 1)
				go func() {
					result <- a.pageNativePermissions(func() PlatformPermissions {
						close(entered)
						<-release
						return permissionSnapshotFixture(true)
					})
				}()
				<-entered
				select {
				case <-result:
					t.Fatal("invalid snapshot was served before the live read")
				default:
				}
				a.pagePermissions.mu.Lock()
				retained := a.pagePermissions.value != nil
				a.pagePermissions.mu.Unlock()
				if retained {
					t.Error("invalid in-memory value survived admission")
				}
				if _, err := os.Stat(filepath.Clean(path)); !os.IsNotExist(err) {
					t.Error("invalid disk value survived admission")
				}
				close(release)
				select {
				case got := <-result:
					if len(got.Items) != 1 || !got.Items[0].Granted {
						t.Fatal("did not fall back to the live result")
					}
				case <-time.After(time.Second):
					t.Fatal("live fallback did not finish")
				}
			})
		}
	}
}

func TestPagePermissionForegroundKeepsLiveFallbackAfterErasure(t *testing.T) {
	a := newPageFirstReadTestApp(t)
	calls := 0
	got := a.pageNativePermissions(func() PlatformPermissions {
		calls++
		if calls == 1 {
			a.clearPageSnapshots()
		}
		return permissionSnapshotFixture(true)
	})
	if calls != 2 || len(got.Items) != 1 || !got.Items[0].Granted {
		t.Fatalf("foreground fallback lost live status: calls=%d view=%+v", calls, got)
	}
	path, _ := pagePermissionSnapshotPath()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("erased refresh persisted through the foreground live fallback")
	}
}
