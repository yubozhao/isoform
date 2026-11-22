package main

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"testing"
)

// The 08:30 wake on 2026-11-20 as the phone would have told it: ten parts
// whole on the relay, eight uploads asked about, and no part request sent.
func TestMobileRecordingPassesAreWrittenAsCountsOnly(t *testing.T) {
	bridge, _ := mobileTestChatBridge(t)
	status := watchDeskCall(bridge, http.MethodGet, "/status", "", mobileScopeBriefRead)
	var flags struct {
		RecordingPasses       bool `json:"recordingPasses"`
		RecordingPassRestarts bool `json:"recordingPassRestarts"`
	}
	if err := json.Unmarshal(mobileTestDecodeReply(t, status).Data, &flags); err != nil {
		t.Fatal(err)
	}
	if !flags.RecordingPasses || !flags.RecordingPassRestarts {
		t.Fatal("GET /status must advertise recordingPasses and recordingPassRestarts")
	}

	var out bytes.Buffer
	log.SetOutput(&out)
	t.Cleanup(func() { log.SetOutput(log.Default().Writer()) })
	body := `{"passes":[{"startedAt":"2026-11-20T16:30:54Z","seconds":58.5,"pending":10,"whole":2,"notDue":0,"began":8,"beganWhole":8,"beganHanded":0,"beganNo":0,"told":0,"toldFailed":8,"posted":0,"taken":0,"inFlight":0,"refused":0,"outcome":"waiting","stoppedBy":"tell-relay-timed-out","skipped":3,"replacedStale":1,"restarted":4,"sentInWake":3},{"startedAt":"2026-11-20T16:45:00Z","pending":4,"whole":4,"notDue":0,"began":0,"beganWhole":0,"beganHanded":0,"beganNo":0,"told":0,"toldFailed":0,"posted":1,"taken":0,"inFlight":0,"refused":0,"skipped":0}]}`
	response := watchDeskCall(bridge, http.MethodPost, "/recordings/passes", body, mobileScopeChatWrite)
	if response.Code != http.StatusOK {
		t.Fatalf("POST /recordings/passes returned %d: %s", response.Code, response.Body.String())
	}
	logged := out.String()
	for _, want := range []string{
		"[recordings] phone pass 2026-11-20T16:30:54Z: 58.5s, outcome waiting, stopped by tell-relay-timed-out; pending 10 (whole 2, not due 0); handed 8 (whole 8, on the way 0, not 0); told 0 (unanswered 8); part requests 0 (taken 0, on the way 0, refused 0); wakes skipped 3; a stale pass was replaced 1; held parts started afresh 4; parts sent in the wake 3",
		"[recordings] phone pass 2026-11-20T16:45:00Z: running, outcome none, stopped by nothing; pending 4 (whole 4",
	} {
		if !strings.Contains(logged, want) {
			t.Fatalf("log misses %q in:\n%s", want, logged)
		}
	}

	// Free text never reaches the log: a word is letters, digits and dashes.
	out.Reset()
	for _, bad := range []string{
		`{"passes":[{"startedAt":"2026-11-20T16:30:54Z","outcome":"said: hello there"}]}`,
		`{"passes":[{"startedAt":"yesterday","outcome":"waiting"}]}`,
		`{"passes":[{"startedAt":"2026-11-20T16:30:54Z","pending":-1}]}`,
		`{"passes":[{"startedAt":"2026-11-20T16:30:54Z","transcript":"x"}]}`,
		`{"passes":[]}`,
	} {
		if code := watchDeskCall(bridge, http.MethodPost, "/recordings/passes", bad, mobileScopeChatWrite).Code; code != http.StatusBadRequest {
			t.Fatalf("%s: got %d, want 400", bad, code)
		}
	}
	if strings.Contains(out.String(), "phone pass") {
		t.Fatalf("a refused request was written: %s", out.String())
	}
	if code := watchDeskCall(bridge, http.MethodPost, "/recordings/passes", body, mobileScopeBriefRead).Code; code != http.StatusForbidden {
		t.Fatalf("without chat:write: got %d, want 403", code)
	}
}
