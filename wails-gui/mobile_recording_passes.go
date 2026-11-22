package main

// What a phone's recording passes came to (PROTOCOL.md 24.2b).
//
// On 2026-11-20 a phone woke four times, the relay took its uploads, and no part
// request reached this computer. Nothing said whether the phone's pass ran, what
// it found waiting or where it stopped. The phone now keeps counts for its last
// passes and tells them here; each is written to the log as one line. A record
// holds counts, a duration and short words. Nothing of a recording is in it.

import (
	"fmt"
	"log"
	"net/http"
	"time"
)

const mobileRecordingPassesMax = 20

type mobileRecordingPass struct {
	StartedAt   string   `json:"startedAt"`
	Seconds     *float64 `json:"seconds,omitempty"`
	Pending     int      `json:"pending"`
	Whole       int      `json:"whole"`
	NotDue      int      `json:"notDue"`
	Began       int      `json:"began"`
	BeganWhole  int      `json:"beganWhole"`
	BeganHanded int      `json:"beganHanded"`
	BeganNo     int      `json:"beganNo"`
	Told        int      `json:"told"`
	ToldFailed  int      `json:"toldFailed"`
	Posted      int      `json:"posted"`
	Taken       int      `json:"taken"`
	InFlight    int      `json:"inFlight"`
	Refused     int      `json:"refused"`
	Outcome     string   `json:"outcome,omitempty"`
	StoppedBy   string   `json:"stoppedBy,omitempty"`
	Skipped     int      `json:"skipped"`
	// ReplacedStale is 1 when the pass replaced one that was still running and had done nothing for three minutes.
	ReplacedStale int `json:"replacedStale,omitempty"`
	// Restarted is how many parts the phone's system had held for minutes without an end and the pass started afresh.
	Restarted int `json:"restarted,omitempty"`
	// SentInWake is how many parts the phone sent itself in a background wake until the relay held them whole.
	SentInWake int   `json:"sentInWake,omitempty"`
	Reported   *bool `json:"reported,omitempty"`
}

type mobileRecordingPassesRequest struct {
	Passes []mobileRecordingPass `json:"passes"`
}

// mobilePassWord reports whether s is a short word of letters, digits and
// dashes: the only free text a record may carry into the log.
func mobilePassWord(s string) bool {
	if len(s) > 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-':
		default:
			return false
		}
	}
	return true
}

func (pass mobileRecordingPass) valid() bool {
	if _, err := time.Parse(time.RFC3339, pass.StartedAt); err != nil {
		return false
	}
	if pass.Seconds != nil && (*pass.Seconds < 0 || *pass.Seconds > 7*24*3600) {
		return false
	}
	for _, n := range []int{pass.Pending, pass.Whole, pass.NotDue, pass.Began, pass.BeganWhole, pass.BeganHanded,
		pass.BeganNo, pass.Told, pass.ToldFailed, pass.Posted, pass.Taken, pass.InFlight, pass.Refused, pass.Skipped, pass.ReplacedStale, pass.Restarted, pass.SentInWake} {
		if n < 0 || n > 100000 {
			return false
		}
	}
	return mobilePassWord(pass.Outcome) && mobilePassWord(pass.StoppedBy)
}

// recordingInWakeSendEnabled is the rollback switch for the phone's send in a background wake:
// off, /status stops saying so and every phone hands its pieces to its system as before.
var recordingInWakeSendEnabled = true

func mobileRecordingPassLine(pass mobileRecordingPass) string {
	seconds := "running"
	if pass.Seconds != nil {
		seconds = time.Duration(*pass.Seconds * float64(time.Second)).Round(100 * time.Millisecond).String()
	}
	outcome, stopped := pass.Outcome, pass.StoppedBy
	if outcome == "" {
		outcome = "none"
	}
	if stopped == "" {
		stopped = "nothing"
	}
	return "[recordings] phone pass " + pass.StartedAt + ": " + seconds + ", outcome " + outcome + ", stopped by " + stopped +
		mobilePassCounts(pass)
}

func mobilePassCounts(pass mobileRecordingPass) string {
	return fmt.Sprintf("; pending %d (whole %d, not due %d); handed %d (whole %d, on the way %d, not %d); told %d (unanswered %d); part requests %d (taken %d, on the way %d, refused %d); wakes skipped %d; a stale pass was replaced %d; held parts started afresh %d; parts sent in the wake %d",
		pass.Pending, pass.Whole, pass.NotDue, pass.Began, pass.BeganWhole, pass.BeganHanded, pass.BeganNo,
		pass.Told, pass.ToldFailed, pass.Posted, pass.Taken, pass.InFlight, pass.Refused, pass.Skipped, pass.ReplacedStale, pass.Restarted, pass.SentInWake)
}

func (bridge *mobileBridge) serveRecordingPasses(w http.ResponseWriter, r *http.Request) {
	var req mobileRecordingPassesRequest
	if !mobileDecode(w, r, &req) {
		return
	}
	if len(req.Passes) == 0 || len(req.Passes) > mobileRecordingPassesMax {
		mobileJSON(w, http.StatusBadRequest, nil, "1 to 20 passes are required")
		return
	}
	for _, pass := range req.Passes {
		if !pass.valid() {
			mobileJSON(w, http.StatusBadRequest, nil, "a pass is not valid")
			return
		}
	}
	for _, pass := range req.Passes {
		log.Print(mobileRecordingPassLine(pass))
	}
	mobileJSON(w, http.StatusOK, map[string]int{"received": len(req.Passes)}, "")
}
