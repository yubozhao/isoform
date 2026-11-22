package main

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"wails-gui/storage"
)

type defaultsFixture struct {
	*App
	db       *sql.DB
	recorder *recordingPostHogClient
}

func newDefaultsFixture(t *testing.T, existing bool) defaultsFixture {
	t.Helper()
	telemetry, recorder := newRecordingTelemetry(t)
	dir := t.TempDir()
	t.Setenv(agentHomeEnv, dir)
	store, err := openStartupStorage()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	db, err := sql.Open("sqlite", filepath.Join(dir, "sessions.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	f := defaultsFixture{&App{store: store, telemetry: telemetry}, db, recorder}
	if existing {
		f.exec(t, "DELETE FROM ui_state WHERE key=?", storage.SettingsDefaultsMarker)
	}
	return f
}

func (f defaultsFixture) exec(t *testing.T, stmt string, args ...any) {
	t.Helper()
	if _, err := f.db.Exec(stmt, args...); err != nil {
		t.Fatal(err)
	}
}
func (f defaultsFixture) restore(t *testing.T) {
	t.Helper()
	f.restoreSettingsDefaults(context.Background(), f.store)
}
func (f defaultsFixture) checkEffective(t *testing.T, messages, topics bool, tier string) {
	t.Helper()
	if got := f.isPermissionEnabled(messagesPermissionKey); got != messages {
		t.Errorf("Messages = %t, want %t", got, messages)
	}
	if got, err := f.topicsDailyEnabled(context.Background()); err != nil || got != topics {
		t.Errorf("topics = %t, %v; want %t", got, err, topics)
	}
	if got := f.GetTelemetryTier(); got != tier {
		t.Errorf("telemetry = %q, want %q", got, tier)
	}
}
func (f defaultsFixture) checkHeld(t *testing.T) {
	t.Helper()
	if on, err := f.messagesSwitchRead(t.Context()); on || !errors.Is(err, errSettingsDefaultsUnavailable) {
		t.Errorf("held Messages display = %t, %v", on, err)
	}
	if f.isPermissionEnabled(messagesPermissionKey) {
		t.Error("held Messages permission allowed reading")
	}
	if view, err := f.GetDailyTopics(); view.Enabled || !errors.Is(err, errSettingsDefaultsUnavailable) {
		t.Errorf("held proposals display = %t, %v", view.Enabled, err)
	}
	if on, err := f.topicsDailyEnabled(t.Context()); on || !errors.Is(err, errSettingsDefaultsUnavailable) {
		t.Errorf("held proposals build gate = %t, %v", on, err)
	}
	if got := f.GetTelemetryTier(); got != TelemetryTierOff {
		t.Errorf("held telemetry tier = %q", got)
	}
}

func (f defaultsFixture) checkState(t *testing.T, key, want string, saved bool) {
	t.Helper()
	got, found, err := f.store.LookupUIState(context.Background(), key)
	if err != nil || found != saved || got != want {
		t.Errorf("%s = %q (present %t), %v; want %q (present %t)", key, got, found, err, want, saved)
	}
}

func TestSettingsDefaultsFresh(t *testing.T) {
	withoutStoreSeeds(t)
	f := newDefaultsFixture(t, false)
	for range 2 {
		f.restore(t)
		f.checkEffective(t, false, true, TelemetryTierOff)
		f.checkState(t, permissionUIStateKey(messagesPermissionKey), "", false)
		f.checkState(t, topicsDailyEnabledKey, "", false)
		f.checkState(t, storage.SettingsDefaultsMarker, storage.SettingsDefaultsFresh, true)
		if _, err := os.Stat(telemetryTierPath()); !os.IsNotExist(err) {
			t.Fatalf("fresh install wrote tier or failed to read: %v", err)
		}
		f.telemetry.Capture("app.open", nil)
		if f.recorder.count() != 0 {
			t.Fatal("fresh install queued a capture")
		}
	}
}

func TestSettingsDefaultsExistingOnce(t *testing.T) {
	f := newDefaultsFixture(t, true)
	f.restore(t)
	f.checkEffective(t, true, false, TelemetryTierDetailed)
	f.checkState(t, permissionUIStateKey(messagesPermissionKey), "true", true)
	f.checkState(t, topicsDailyEnabledKey, "false", true)
	f.checkState(t, storage.SettingsDefaultsMarker, "true", true)
	if b, err := os.ReadFile(telemetryTierPath()); err != nil || string(b) != TelemetryTierDetailed {
		t.Fatalf("saved tier = %q, %v", b, err)
	}
	// Removing a choice after completion must not replay the grandfathering.
	f.exec(t, "DELETE FROM ui_state WHERE key=?", topicsDailyEnabledKey)
	f.restore(t)
	f.checkState(t, topicsDailyEnabledKey, "", false)
	f.checkEffective(t, true, true, TelemetryTierDetailed)
}

func TestSettingsDefaultsSavedChoices(t *testing.T) {
	for _, tc := range []struct {
		value, tier      string
		messages, topics bool
		effectiveTier    string
	}{
		{"true", "detailed", true, true, "detailed"},
		{"false", "off", false, false, "off"},
		{"", "basic", true, false, "detailed"},
		{"other", "other", true, false, "detailed"},
	} {
		t.Run(tc.tier, func(t *testing.T) {
			f := newDefaultsFixture(t, true)
			for _, key := range []string{permissionUIStateKey(messagesPermissionKey), topicsDailyEnabledKey} {
				f.exec(t, "INSERT INTO ui_state(key,value) VALUES (?,?)", key, tc.value)
			}
			if err := os.WriteFile(telemetryTierPath(), []byte(tc.tier), 0600); err != nil {
				t.Fatal(err)
			}
			f.restore(t)
			f.checkEffective(t, tc.messages, tc.topics, tc.effectiveTier)
			f.checkState(t, permissionUIStateKey(messagesPermissionKey), tc.value, true)
			f.checkState(t, topicsDailyEnabledKey, tc.value, true)
			if b, err := os.ReadFile(telemetryTierPath()); err != nil || string(b) != tc.tier {
				t.Fatalf("changed tier = %q, %v", b, err)
			}
		})
	}
}

func TestSettingsDefaultsFailedWriteRetries(t *testing.T) {
	f := newDefaultsFixture(t, true)
	f.exec(t, "CREATE TRIGGER fail_default BEFORE INSERT ON ui_state BEGIN SELECT RAISE(ABORT, 'write failed'); END")
	f.restore(t)
	f.checkEffective(t, true, false, TelemetryTierDetailed)
	f.checkState(t, storage.SettingsDefaultsMarker, "", false)
	f.checkState(t, permissionUIStateKey(messagesPermissionKey), "", false)
	f.exec(t, "DROP TRIGGER fail_default")
	// The person opts out between attempts.
	f.exec(t, "INSERT INTO ui_state(key,value) VALUES (?, 'false')", permissionUIStateKey(messagesPermissionKey))
	f.restore(t)
	f.checkState(t, storage.SettingsDefaultsMarker, "true", true)
	f.checkEffective(t, false, false, TelemetryTierDetailed)
}

func TestSettingsDefaultsPartialWriteRetries(t *testing.T) {
	f := newDefaultsFixture(t, true)
	// The first switch saves, then the second fails.
	f.exec(t, "CREATE TRIGGER fail_topics BEFORE INSERT ON ui_state WHEN NEW.key = 'topics_daily_enabled' BEGIN SELECT RAISE(ABORT, 'write failed'); END")
	f.restore(t)
	f.checkState(t, permissionUIStateKey(messagesPermissionKey), "true", true)
	f.checkState(t, topicsDailyEnabledKey, "", false)
	f.checkState(t, storage.SettingsDefaultsMarker, "", false)
	f.checkEffective(t, true, false, TelemetryTierDetailed)
	f.exec(t, "DROP TRIGGER fail_topics")
	f.restore(t)
	f.checkState(t, storage.SettingsDefaultsMarker, "true", true)
	f.checkState(t, topicsDailyEnabledKey, "false", true)
	f.checkEffective(t, true, false, TelemetryTierDetailed)
}

func (f *defaultsFixture) start(t *testing.T) {
	t.Helper()
	app := &App{storageStartup: newStorageStartupState(), telemetry: f.telemetry, activity: &activityMonitorState{}}
	started := false
	app.startStorageStartup(t.Context(), openStartupStorage, func() { started = true })
	awaitStorageStartup(t, app.storageStartup.done)
	if !started || app.store == nil || app.GetStorageStartupStatus().Code != "" {
		t.Fatalf("store unavailable: %+v", app.GetStorageStartupStatus())
	}
	t.Cleanup(func() { app.store.Close() })
	if _, err := app.store.GetUIState(t.Context(), "unrelated"); err != nil {
		t.Fatalf("published store unreadable: %v", err)
	}
	f.App = app
}

func TestSettingsDefaultsStartupReadError(t *testing.T) {
	for _, fault := range []string{"marker read", "invalid marker", "unreadable tier", "setting read"} {
		t.Run(fault, func(t *testing.T) {
			f := newDefaultsFixture(t, true)
			// Both saved switches would allow work if the run-local hold leaked.
			messagesKey := permissionUIStateKey(messagesPermissionKey)
			f.exec(t, "INSERT INTO ui_state(key,value) VALUES (?, 'true'), (?, 'true')", messagesKey, topicsDailyEnabledKey)
			switch fault {
			case "marker read", "setting read":
				badKey := storage.SettingsDefaultsMarker
				if fault == "setting read" {
					badKey = topicsDailyEnabledKey
				} else {
					// An absent row would never evaluate the failing expression.
					f.exec(t, "INSERT INTO ui_state(key,value) VALUES (?, 'broken')", badKey)
				}
				f.exec(t, "ALTER TABLE ui_state RENAME TO underlying_ui_state")
				// Only the selected key fails. Other readers can use the database.
				f.exec(t, "CREATE VIEW ui_state AS SELECT key, CASE WHEN key='"+badKey+"' THEN abs(-9223372036854775808) ELSE value END AS value FROM underlying_ui_state")
			case "invalid marker":
				f.exec(t, "INSERT INTO ui_state(key,value) VALUES (?, 'broken')", storage.SettingsDefaultsMarker)
			case "unreadable tier":
				if err := os.Mkdir(telemetryTierPath(), 0700); err != nil {
					t.Fatal(err)
				}
			}

			f.start(t)
			f.checkHeld(t)
			if f.telemetry.enabled.Load() || f.telemetry.client != nil || f.recorder.count() != 0 {
				t.Fatal("telemetry active during settings hold")
			}
			if err := f.SetTelemetryTier(TelemetryTierDetailed); !errors.Is(err, errSettingsDefaultsUnavailable) {
				t.Fatalf("telemetry change during hold: %v", err)
			}
			if err := f.SetPermissionEnabled(messagesPermissionKey, true); !errors.Is(err, errSettingsDefaultsUnavailable) {
				t.Fatalf("Messages change during hold: %v", err)
			}
			if _, err := f.SetDailyTopicsEnabled(true); !errors.Is(err, errSettingsDefaultsUnavailable) {
				t.Fatal("settings hold allowed proposals to change")
			}
			if err := f.store.Close(); err != nil {
				t.Fatal(err)
			}
			switch fault {
			case "marker read", "setting read":
				f.exec(t, "DROP VIEW ui_state")
				f.exec(t, "ALTER TABLE underlying_ui_state RENAME TO ui_state")
			case "unreadable tier":
				if err := os.Remove(telemetryTierPath()); err != nil {
					t.Fatal(err)
				}
			}
			// Check durable values before repairing the marker / restarting.
			var marker string
			err := f.db.QueryRow("SELECT value FROM ui_state WHERE key=?", storage.SettingsDefaultsMarker).Scan(&marker)
			if fault == "marker read" || fault == "invalid marker" {
				if err != nil || marker != "broken" {
					t.Fatalf("marker overwritten: %q, %v", marker, err)
				}
				f.exec(t, "DELETE FROM ui_state WHERE key=?", storage.SettingsDefaultsMarker)
			} else if err != sql.ErrNoRows {
				t.Fatalf("completion marker = %q, %v", marker, err)
			}
			if _, err := os.Stat(telemetryTierPath()); !os.IsNotExist(err) {
				t.Fatalf("tier persisted during hold: %v", err)
			}
			f.start(t)
			f.checkEffective(t, true, true, TelemetryTierDetailed)
			f.checkState(t, storage.SettingsDefaultsMarker, "true", true)
			if b, err := os.ReadFile(telemetryTierPath()); err != nil || string(b) != TelemetryTierDetailed {
				t.Fatalf("retry did not persist tier: %q, %v", b, err)
			}
		})
	}
}

func TestSettingsDefaultsFreshUnreadableConsent(t *testing.T) {
	f := newDefaultsFixture(t, false)
	if err := os.WriteFile(telemetryTierPath(), []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	var diagnostic bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&diagnostic)
	func() {
		defer log.SetOutput(previous)
		f.restore(t)
	}()
	if got := diagnostic.String(); strings.Count(got, "\n") != 1 || !strings.HasSuffix(got, "[settings] preferences unavailable; holding Messages, proposals and telemetry off until relaunch\n") {
		t.Fatalf("unexpected settings diagnostic: %q", got)
	}
	f.checkHeld(t)
	f.checkState(t, storage.SettingsDefaultsMarker, storage.SettingsDefaultsFresh, true)
	if data, err := os.ReadFile(telemetryTierPath()); err != nil || string(data) != "invalid" {
		t.Fatalf("saved consent changed: %q, %v", data, err)
	}
}
