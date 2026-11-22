package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"wails-gui/storage"
)

func watchSettingsTestApp(t *testing.T) *App {
	t.Helper()
	db, err := storage.New(filepath.Join(t.TempDir(), "settings.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return &App{store: db}
}

func watchContextTestRelays(t *testing.T) mobileRecordingRelays {
	t.Helper()
	app := watchSettingsTestApp(t)
	for _, key := range []string{watchHeartRateKey, watchPlaceKey} {
		if err := app.writeWatchSetting(key, true); err != nil {
			t.Fatal(err)
		}
	}
	return mobileRecordingRelays{app: app, primaryName: mobileRecordingRelayPinned}
}

func TestWatchSettingsSharedChoices(t *testing.T) {
	t.Setenv(featureSwitchesEnv, "")
	app := watchSettingsTestApp(t)
	b := &mobileBridge{app: app, stateDir: t.TempDir()}
	app.mobileBridge = b
	view, err := app.GetWatchSettings()
	if err != nil || view.HeartRate != (MobileWatchNotesSetting{}) || view.Place != (MobileWatchNotesSetting{}) || view.Notes != (MobileWatchNotesSetting{}) || view.Paired == nil || *view.Paired {
		t.Fatalf("defaults %+v %v", view, err)
	}
	for _, tc := range []struct {
		path, field string
		set         func(bool) (WatchSettingsView, error)
		read        func(WatchSettingsView) MobileWatchNotesSetting
	}{
		{"watch-heart-rate", "watchHeartRate", app.SetWatchHeartRate, func(v WatchSettingsView) MobileWatchNotesSetting { return v.HeartRate }},
		{"watch-place", "watchPlace", app.SetWatchPlace, func(v WatchSettingsView) MobileWatchNotesSetting { return v.Place }},
		{"watch-notes", "watchNotes", app.SetWatchNotes, func(v WatchSettingsView) MobileWatchNotesSetting { return v.Notes }},
	} {
		path := "/settings/" + tc.path
		for _, method := range []string{"GET", "POST"} {
			if got := watchDeskCall(b, method, path, "", mobileScopeBriefRead); got.Code != http.StatusForbidden {
				t.Fatal(tc.path, method, got.Code)
			}
		}
		if got := watchDeskCall(b, "POST", path, "{}", mobileScopeChatWrite); got.Code != 400 {
			t.Fatal(tc.path, got.Code)
		}
		view, err := tc.set(false)
		if err != nil || tc.read(view) != (MobileWatchNotesSetting{Chosen: true}) {
			t.Fatal(tc.path, err)
		}
		get := watchDeskCall(b, "GET", path, "", mobileScopeChatWrite)
		if get.Code != 200 || !bytes.Contains(get.Body.Bytes(), []byte(`"on":false,"chosen":true`)) {
			t.Fatal(tc.path, get.Code)
		}
		got := watchDeskCall(b, "POST", path, `{"on":true}`, mobileScopeChatWrite)
		view, err = app.GetWatchSettings()
		if got.Code != 200 || err != nil || tc.read(view) != (MobileWatchNotesSetting{On: true, Chosen: true}) {
			t.Fatal(tc.path, got.Code, err)
		}
		status := watchDeskCall(b, "GET", "/status", "", mobileScopeBriefRead)
		var env struct{ Data map[string]json.RawMessage }
		var setting MobileWatchNotesSetting
		if status.Code != 200 || json.Unmarshal(status.Body.Bytes(), &env) != nil || json.Unmarshal(env.Data[tc.field], &setting) != nil || setting != tc.read(view) {
			t.Fatal(tc.field, "missing or wrong status")
		}
	}
	view, err = app.SetWatchListeningOff()
	if err != nil || !view.Listening.Off || view.Listening.Since == "" {
		t.Fatal(view.Listening, err)
	}
	got := watchDeskCall(b, "GET", "/watch/listening", "", mobileScopeChatWrite)
	if got.Code != 200 || !bytes.Contains(got.Body.Bytes(), []byte(view.Listening.Since)) {
		t.Fatal("phone did not read desktop off")
	}
}

func TestWatchSettingsReadFailure(t *testing.T) {
	app := watchSettingsTestApp(t)
	b := &mobileBridge{app: app}
	if err := app.store.SetUIState(t.Context(), watchNotesSettingKey, "invalid"); err != nil {
		t.Fatal(err)
	}
	if _, err := app.GetWatchSettings(); err == nil {
		t.Fatal("unreadable notes returned a view")
	}
	app.store.Close()
	if _, err := app.GetWatchSettings(); err == nil {
		t.Fatal("closed store returned a view")
	}
	for _, path := range []string{"/settings/watch-notes", "/settings/watch-heart-rate", "/settings/watch-place", "/watch/listening"} {
		if got := watchDeskCall(b, "GET", path, "", mobileScopeChatWrite); got.Code != 503 {
			t.Fatal(path, got.Code)
		}
	}
	status := b.statusWithExtras(mobileStatusView{}).(map[string]any)
	for _, key := range []string{"watchNotes", "watchHeartRate", "watchPlace", "watchListening"} {
		if _, ok := status[key]; ok {
			t.Fatal(key, "claimed a value")
		}
	}
}

func TestWatchContextChoicesAlwaysKeepThePart(t *testing.T) {
	app := watchSettingsTestApp(t)
	b := &mobileBridge{app: app, stateDir: t.TempDir()}
	phone := mobileTestDevice(mobileScopeChatWrite)
	b.devices = map[string]*mobileDeviceRecord{phone.DeviceID: &phone}
	app.mobileBridge = b
	store := app.watchRecordingStore()
	view, err := app.GetWatchSettings()
	if err != nil || view.Paired != nil || view.LastHeardAt != "" {
		t.Fatal("legacy phone has unknown pairing", err)
	}
	uploads := &mapUploads{bodies: map[string][]byte{}}
	relays := b.recordingRelaysLocked()
	relays.primary = uploads
	audio := []byte{0, 1, 2, 3, 255}
	// The first part is from an older watch whose local switches are both on.
	// The remaining four cover every pair of explicit computer choices.
	choices := []struct{ chosen, heart, place bool }{
		{false, true, true}, {true, false, false}, {true, false, true}, {true, true, false}, {true, true, true},
	}
	for _, c := range choices {
		heart, place := c.heart, c.place
		if c.chosen {
			if _, err := app.SetWatchHeartRate(heart); err != nil {
				t.Fatal(err)
			}
			if _, err := app.SetWatchPlace(place); err != nil {
				t.Fatal(err)
			}
		} else if view.HeartRate.Chosen || view.Place.Chosen {
			t.Fatal("expected unset choices")
		}
		req := sealedRecordingPart(t, uploads, phone.DeviceID, uuid.NewString(), 0, true, audio, time.Now())
		req.Context = &MobileRecordingPartContext{HeartRate: &MobileRecordingHeartRate{Avg: 88, Min: 70, Max: 99, Samples: 10}, Location: &MobileRecordingLocation{Latitude: 1, Longitude: 2, Place: "Town"}}
		req.Marks = []float64{1, 2}
		req.Title = "talk"
		if err := b.expectRecordingPart(store, phone.DeviceID, req, time.Now()); err != nil {
			t.Fatal(err)
		}
		pending := store.expected()
		ctx := pending[len(pending)-1].Request.Context
		if (ctx != nil && ctx.HeartRate != nil) != heart || (ctx != nil && ctx.Location != nil) != place {
			t.Fatal("announcement kept wrong fields")
		}
		got, complete, err := relays.receivePart(t.Context(), store, recordingTestHub, phone.DeviceID, req)
		if err != nil || !complete || got.PartsReceived != 1 {
			t.Fatal("part not accepted", err)
		}
		disk := newMobileRecordingStore(b.stateDir)
		rec, ok := disk.get(req.RecordingID, phone.DeviceID)
		if !ok || !rec.complete() || rec.Title != req.Title || rec.Source != req.Source || rec.Mime != req.Mime || rec.ID != req.RecordingID {
			t.Fatal("incomplete stored recording", err)
		}
		p := rec.Received[0]
		ctx = p.Context
		if (ctx != nil && ctx.HeartRate != nil) != heart || (ctx != nil && ctx.Location != nil) != place {
			t.Fatal("stored wrong fields")
		}
		if heart && !reflect.DeepEqual(ctx.HeartRate, req.Context.HeartRate) || place && !reflect.DeepEqual(ctx.Location, req.Context.Location) {
			t.Fatal("stored context values changed")
		}
		if p.Part != req.Part || p.SHA256 != req.SHA256 || p.Bytes != req.Bytes || p.StartedAt != req.StartedAt || p.DurationSeconds != req.DurationSeconds || p.UploadID != req.Upload.ID || !reflect.DeepEqual(p.Marks, req.Marks) {
			t.Fatal("part metadata changed")
		}
		raw, err := os.ReadFile(filepath.Join(disk.dir, rec.ID, p.File))
		if err != nil || !bytes.Equal(raw, audio) {
			t.Fatal("stored audio changed", err)
		}
	}
	got := watchDeskCall(b, "POST", "/watch/pairing", `{"paired":true}`, mobileScopeChatWrite)
	view, err = app.GetWatchSettings()
	if got.Code != 200 || err != nil || view.Paired == nil || !*view.Paired || view.LastHeardAt == "" {
		t.Fatal("missing watch facts", err)
	}
	phone.ExpiresAt = mobileTimestamp(time.Now().Add(-time.Minute))
	view, err = app.GetWatchSettings()
	if err != nil || view.Paired == nil || *view.Paired || view.LastHeardAt != "" {
		t.Fatal("expired phone contributed watch facts", err)
	}
	delete(b.devices, phone.DeviceID)
	view, err = app.GetWatchSettings()
	if err != nil || view.Paired == nil || *view.Paired || view.LastHeardAt != "" {
		t.Fatal("revoked phone contributed watch facts", err)
	}
}

// On startup the persisted grants are restored. A setting write in progress
// cannot stall an incoming part or prevent its other field from being kept.
func TestWatchContextRestoreAndBusyChoice(t *testing.T) {
	app := watchSettingsTestApp(t)
	if err := app.SaveUIState(watchHeartRateKey, "on"); err != nil {
		t.Fatal(err)
	}
	if err := app.SaveUIState(watchPlaceKey, "off"); err != nil {
		t.Fatal(err)
	}
	restarted := &App{store: app.store}
	check := func(stage string, wantPlace bool) {
		t.Helper()
		req := MobileRecordingRequest{Context: &MobileRecordingPartContext{HeartRate: &MobileRecordingHeartRate{}, Location: &MobileRecordingLocation{}}}
		release := restarted.filterWatchContext(&req)
		release()
		if req.Context == nil || req.Context.HeartRate == nil || (req.Context.Location != nil) != wantPlace {
			t.Fatal(stage, "lost last known choice")
		}
	}
	check("before restore", true)
	restarted.restoreWatchContextChoices()
	check("after restore", false)
	func() {
		restarted.watchHeartRate.mu.Lock()
		defer restarted.watchHeartRate.mu.Unlock()
		restarted.watchPlace.mu.Lock()
		defer restarted.watchPlace.mu.Unlock()
		check("while saving", false)
	}()
	app.store.Close()
	for _, key := range []string{watchHeartRateKey, watchPlaceKey} {
		if _, err := restarted.readWatchSetting(key); err == nil {
			t.Fatal("failed read returned a setting")
		}
	}
	check("after failed reads", false)
}

func TestWatchPendingPartRechecksSettingsOnReceipt(t *testing.T) {
	relays := watchContextTestRelays(t)
	b := &mobileBridge{app: relays.app, stateDir: t.TempDir()}
	store := b.recordingStoreLocked()
	uploads := &mapUploads{bodies: map[string][]byte{}}
	relays.primary = uploads
	req := sealedRecordingPart(t, uploads, mobileTestDeviceID, uuid.NewString(), 0, false, []byte("pending audio"), time.Now())
	req.Context = &MobileRecordingPartContext{HeartRate: &MobileRecordingHeartRate{Avg: 80, Min: 70, Max: 90, Samples: 2}, Location: &MobileRecordingLocation{Latitude: 1, Longitude: 2}}
	if err := b.expectRecordingPart(store, mobileTestDeviceID, req, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := relays.app.SetWatchPlace(false); err != nil {
		t.Fatal(err)
	}
	if n := b.takeExpectedParts(relays, store, recordingTestHub, map[string]bool{mobileTestDeviceID: true}, time.Now()); n != 1 {
		t.Fatal("expected part did not arrive")
	}
	rec, ok := newMobileRecordingStore(b.stateDir).get(req.RecordingID, mobileTestDeviceID)
	if !ok || len(rec.Received) != 1 || rec.Received[0].Context == nil || rec.Received[0].Context.Location != nil || rec.Received[0].Context.HeartRate == nil {
		t.Fatal("final receipt did not honor current choices")
	}
	if _, err := relays.app.SetWatchHeartRate(false); err != nil {
		t.Fatal(err)
	}
	if _, _, err := relays.receivePart(t.Context(), store, recordingTestHub, mobileTestDeviceID, req); err != nil {
		t.Fatal(err)
	}
	rec, ok = newMobileRecordingStore(b.stateDir).get(req.RecordingID, mobileTestDeviceID)
	if !ok || rec.Received[0].Context == nil || rec.Received[0].Context.HeartRate == nil {
		t.Fatal("retry rewrote previously accepted context")
	}
}

func TestWatchPairingReportRemovedOnUnpair(t *testing.T) {
	for _, path := range []string{"unpair", "reconnect"} {
		t.Run(path, func(t *testing.T) {
			app := watchSettingsTestApp(t)
			phone := mobileTestDevice(mobileScopeChatWrite)
			other := mobileTestDevice(mobileScopeChatWrite)
			other.DeviceID = uuid.NewString()
			b := &mobileBridge{app: app, stateDir: t.TempDir(), devices: map[string]*mobileDeviceRecord{phone.DeviceID: &phone, other.DeviceID: &other}}
			post := func() int {
				return watchDeskCall(b, "POST", "/watch/pairing", `{"paired":true}`, mobileScopeChatWrite).Code
			}
			if post() != http.StatusOK {
				t.Fatal("failed to report pairing")
			}
			if err := app.writeWatchSetting("watch_paired_"+other.DeviceID, true); err != nil {
				t.Fatal(err)
			}
			switch path {
			case "unpair":
				if err := b.dropDevice(phone.DeviceID); err != nil {
					t.Fatal(err)
				}
			case "reconnect":
				b.mu.Lock()
				b.reconcileLocked([]mobileRelayPresence{{DeviceID: other.DeviceID}})
				b.mu.Unlock()
			}
			if post() != http.StatusForbidden {
				t.Fatal("stale report accepted after unpair")
			}
			for id, want := range map[string]string{phone.DeviceID: "missing", other.DeviceID: "on"} {
				// An empty row would still prevent this insert.
				value, err := app.store.ReserveUIStateValue(t.Context(), "watch_paired_"+id, "missing")
				if err != nil || value != want {
					t.Fatal("incorrect persisted report after unpair", err)
				}
			}
		})
	}
}
