package main

import (
	"net/http"
	"strings"
	"testing"
)

// The phone (for the watch) pauses the computer, and may end only a pause it
// started. A pause made at the computer is not the phone's to end or shorten.
func TestThePhonePausesTheComputerAndEndsOnlyItsOwnPause(t *testing.T) {
	_, bridge, _, _ := watchDeskFixture(t)
	oldPause, oldResume := mobilePauseObserving, mobileResumeObserving
	t.Cleanup(func() {
		mobilePauseObserving, mobileResumeObserving = oldPause, oldResume
		mobilePhonePauseUntil.Store(0)
	})
	var paused []int
	resumed := 0
	mobilePauseObserving = func(a *App, minutes int) error {
		paused = append(paused, minutes)
		a.closedEyes.enabled.Publish(true, a.observationWallNow())
		return nil
	}
	mobileResumeObserving = func(a *App) error {
		resumed++
		a.closedEyes.enabled.Publish(false, a.observationWallNow())
		return nil
	}
	call := func(path, body string, scopes ...string) (int, string) {
		got := watchDeskCall(bridge, http.MethodPost, path, body, scopes...)
		return got.Code, got.Body.String()
	}

	if code, _ := call("/observing/pause", `{"minutes":60}`, mobileScopeBriefRead); code != http.StatusForbidden {
		t.Fatalf("pause without chat:write: %d", code)
	}
	if code, body := call("/observing/pause", `{"minutes":3}`, mobileScopeChatWrite); code != http.StatusBadRequest {
		t.Fatalf("three minutes: %d %s", code, body)
	}
	if code, body := call("/observing/pause", `{"minutes":999}`, mobileScopeChatWrite); code != http.StatusBadRequest {
		t.Fatalf("999 minutes: %d %s", code, body)
	}
	// Nothing to resume, and nothing was paused by the refused calls.
	if code, body := call("/observing/resume", `{}`, mobileScopeChatWrite); code != http.StatusOK || len(paused) != 0 || resumed != 0 {
		t.Fatalf("resume with no pause: %d %s", code, body)
	}

	// No minutes means an hour: the length of a meeting or a visit.
	code, body := call("/observing/pause", `{}`, mobileScopeChatWrite)
	if code != http.StatusOK || len(paused) != 1 || paused[0] != 60 || !strings.Contains(body, `"startedHere":true`) || !strings.Contains(body, `"observingPause":{`) {
		t.Fatalf("pause: %d %s %v", code, body, paused)
	}
	// A second ask while paused changes nothing.
	code, body = call("/observing/pause", `{"minutes":15}`, mobileScopeChatWrite)
	if code != http.StatusOK || len(paused) != 1 || !strings.Contains(body, `"startedHere":false`) {
		t.Fatalf("second pause: %d %s %v", code, body, paused)
	}
	// The phone ends the pause it started.
	if code, body := call("/observing/resume", `{}`, mobileScopeChatWrite); code != http.StatusOK || resumed != 1 {
		t.Fatalf("resume: %d %s", code, body)
	}

	// A pause made at the computer is not the phone's to end.
	bridge.app.closedEyes.enabled.Publish(true, bridge.app.observationWallNow())
	code, body = call("/observing/resume", `{}`, mobileScopeChatWrite)
	if code != http.StatusConflict || !strings.Contains(body, "pause_not_from_phone") || resumed != 1 {
		t.Fatalf("resume of the computer's own pause: %d %s", code, body)
	}
	if !bridge.app.IsClosedEyesModeEnabled() {
		t.Fatal("the computer's pause was ended from the phone")
	}
}
