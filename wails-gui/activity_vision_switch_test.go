package main

import (
	"context"
	"path/filepath"
	"testing"

	"wails-gui/internal/pii"
	"wails-gui/storage"

	"yansu-agent/agent"
)

// withVisionPrivacyModel stands in for whether the privacy model is on this
// computer.
func withVisionPrivacyModel(t *testing.T, ready bool) {
	t.Helper()
	prior := visionPrivacyModelReady
	visionPrivacyModelReady = func() bool { return ready }
	t.Cleanup(func() { visionPrivacyModelReady = prior })
}

func newVisionSwitchStore(t *testing.T) *storage.Store {
	t.Helper()
	store, err := storage.New(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// visionSendCalls runs both vision sends for a public session and returns how
// many reached the model.
func visionSendCalls(t *testing.T, app *App) int {
	t.Helper()
	runner := &memoryTranslationFakeRunner{ready: true, response: "Read text"}
	app.runner = runner
	triager := NewActivityTriager(app, nil)
	img := &agent.ImageAttachment{DataBase64: "frame"}
	triager.describeWeChatScreenshot(context.Background(), img, "vision-source")
	triager.describeScreenshotForVision(context.Background(), img, "vision-source")
	return runner.calls
}

func visionSwitchApp(t *testing.T) *App {
	t.Helper()
	app := newPrivacyReadTestApp(t)
	priorApps := pii.GetUserSensitiveApps()
	t.Cleanup(func() { pii.SetUserSensitiveApps(priorApps) })
	pii.SetUserSensitiveApps(nil)
	insertTestSession(t, app.activity.store, "vision-source", "pending", 100_000)
	return app
}

// An install that never set the switch keeps its pictures on; a saved
// choice wins.
func TestWeChatPicturesStayOnUntilTurnedOff(t *testing.T) {
	withVisionPrivacyModel(t, true)
	app := visionSwitchApp(t)
	view, err := app.GetActivityVision()
	if err != nil || !view.Enabled || !view.PrivacyModelReady {
		t.Fatalf("never set: %+v %v", view, err)
	}
	if calls := visionSendCalls(t, app); calls != 2 {
		t.Fatalf("never set sent %d pictures, want 2", calls)
	}
	if view, err = app.SetActivityVisionEnabled(false); err != nil || view.Enabled {
		t.Fatalf("turned off: %+v %v", view, err)
	}
	if calls := visionSendCalls(t, app); calls != 0 {
		t.Fatalf("off sent %d pictures", calls)
	}
	if view, err = app.SetActivityVisionEnabled(true); err != nil || !view.Enabled {
		t.Fatalf("turned on: %+v %v", view, err)
	}
	if calls := visionSendCalls(t, app); calls != 2 {
		t.Fatalf("on again sent %d pictures, want 2", calls)
	}
}

// No picture leaves before the privacy model is on this computer (S13).
func TestWeChatPicturesWaitForThePrivacyModel(t *testing.T) {
	withVisionPrivacyModel(t, false)
	app := visionSwitchApp(t)
	view, err := app.GetActivityVision()
	if err != nil || !view.Enabled || view.PrivacyModelReady {
		t.Fatalf("model missing: %+v %v", view, err)
	}
	if calls := visionSendCalls(t, app); calls != 0 {
		t.Fatalf("a picture left without the privacy model: %d", calls)
	}
}

// A failed read sends no picture and the row gets the error, never "off".
func TestWeChatPicturesFailedReadSendsNothing(t *testing.T) {
	withVisionPrivacyModel(t, true)
	app := visionSwitchApp(t)
	if err := app.store.SetUIState(context.Background(), activityVisionEnabledKey, "not-a-bool"); err != nil {
		t.Fatal(err)
	}
	if _, err := app.GetActivityVision(); err == nil {
		t.Fatal("an unreadable switch read as a state")
	}
	if calls := visionSendCalls(t, app); calls != 0 {
		t.Fatalf("an unreadable switch sent %d pictures", calls)
	}
	app.store = nil
	if calls := visionSendCalls(t, app); calls != 0 {
		t.Fatalf("no settings store sent %d pictures", calls)
	}
}

// The WeChat pictures row names only WeChat because the generic picture pass
// (enrichWeakObservations, any app whose text is too thin) is compiled off.
// Turning it on would make the words say less than the code.
func TestGenericPicturePassStaysOffWhileTheWordsSayWeChat(t *testing.T) {
	if featureSemanticEvents {
		t.Fatal("the generic picture pass sends pictures of any app; update sees.wechatPic and What leaves before enabling featureSemanticEvents")
	}
}
