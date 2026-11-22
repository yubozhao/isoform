package httpapi

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestUploadLogLineSaysWhoAskedAndHowItEnded(t *testing.T) {
	id := "9a287bb9-8c8a-4a45-930e-b61afea9a1d1"
	cases := []struct {
		computer bool
		method   string
		id       string
		piece    bool
		status   int
		want     string
	}{
		{false, http.MethodPut, id, true, http.StatusCreated, "phone PUT upload pieces 9a287bb9: Created (201)"},
		{false, http.MethodPut, id, true, http.StatusTooManyRequests, "phone PUT upload pieces 9a287bb9: Too Many Requests (429)"},
		{false, http.MethodGet, id, true, http.StatusOK, "phone GET upload pieces 9a287bb9: OK (200)"},
		{true, http.MethodGet, id, false, http.StatusNotFound, "computer GET upload 9a287bb9: Not Found (404)"},
		{true, http.MethodDelete, id, false, http.StatusOK, "computer DELETE upload 9a287bb9: OK (200)"},
		// Nothing a caller made up is written.
		{false, "BREW\nx", "some words\nand a line", false, http.StatusNotFound, "phone other upload invalid: Not Found (404)"},
	}
	for _, c := range cases {
		if got := uploadLogLine(c.computer, c.method, c.id, c.piece, c.status); got != c.want {
			t.Errorf("got %q, want %q", got, c.want)
		}
	}
}

func TestLogUploadKeepsTheStatusAndTheBody(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/v1/hub/uploads/9a287bb9-8c8a-4a45-930e-b61afea9a1d1", nil)
	logUpload(recorder, request, "/v1/hub/uploads/", func(w http.ResponseWriter, _ *http.Request) {
		reply(w, http.StatusNotFound, nil, "Upload not found")
	})
	if recorder.Code != http.StatusNotFound || recorder.Body.Len() == 0 {
		t.Fatalf("status %d, body %q", recorder.Code, recorder.Body.String())
	}
	// A handler that only writes a body answered 200.
	kept := &statusWriter{ResponseWriter: httptest.NewRecorder()}
	_, _ = kept.Write([]byte("x"))
	if kept.status != http.StatusOK {
		t.Fatalf("status %d", kept.status)
	}
}

func TestLogRequestSaysHowAPhoneRequestEnded(t *testing.T) {
	if got, want := requestLogLine(http.StatusAccepted), "phone request for the computer: Accepted (202)"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	recorder := httptest.NewRecorder()
	logRequest(func(w http.ResponseWriter, _ *http.Request) {
		reply(w, http.StatusTooManyRequests, nil, "Request queue is full")
	})(recorder, httptest.NewRequest(http.MethodPost, "/v1/requests", nil))
	if recorder.Code != http.StatusTooManyRequests || recorder.Body.Len() == 0 {
		t.Fatalf("status %d, body %q", recorder.Code, recorder.Body.String())
	}
}

// A phone request that is not queued says why, and only why.
func TestQueueFailureLineNamesTheCauseOnly(t *testing.T) {
	for _, c := range []struct {
		err  error
		want string
	}{
		{context.Canceled, "phone request not queued: the phone went away first"},
		{fmt.Errorf("begin: %w", context.Canceled), "phone request not queued: the phone went away first"},
		{context.DeadlineExceeded, "phone request not queued: timed out"},
		{errors.New("database is locked (5) (SQLITE_BUSY)"), "phone request not queued: store: database is locked (5) (SQLITE_BUSY)"},
	} {
		if got := queueFailureLine(c.err); got != c.want {
			t.Fatalf("%v: got %q, want %q", c.err, got, c.want)
		}
	}
}

func TestRefusedPieceLineHoldsNumbersOnly(t *testing.T) {
	got := refusedPieceLine("the piece did not arrive whole", 524288, 2883584, 262144, 65536, 262144)
	want := "piece refused: the piece did not arrive whole; offset 524288 of 2883584, piece size 262144, 65536 of 262144 bytes arrived"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestRefusalLineNamesTheRouteAndTheReasonOnly(t *testing.T) {
	cases := []struct{ method, path, want string }{
		{http.MethodPost, "/v1/requests", "refused POST /v1/requests: Forbidden (403), Device lacks the required scope"},
		{http.MethodPut, "/v1/uploads/9a287bb9-8c8a-4a45-930e-b61afea9a1d1/pieces", "refused PUT /v1/uploads/...: Forbidden (403), Device lacks the required scope"},
		{http.MethodGet, "/v1/Secret9", "refused GET another route: Forbidden (403), Device lacks the required scope"},
		{"BREW", "/elsewhere", "refused other another route: Forbidden (403), Device lacks the required scope"},
	}
	for _, c := range cases {
		if got := refusalLine(c.method, c.path, http.StatusForbidden, "Device lacks the required scope"); got != c.want {
			t.Fatalf("got %q, want %q", got, c.want)
		}
	}
}

func TestARefusedDeviceRequestLeavesALineWithoutItsCredential(t *testing.T) {
	var out strings.Builder
	before := log.Writer()
	log.SetOutput(&out)
	defer log.SetOutput(before)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/requests", nil)
	request.Header.Set("Authorization", "Bearer not-a-real-credential")
	refuse(recorder, request, http.StatusUnauthorized, "Device authentication required")
	if recorder.Code != http.StatusUnauthorized || recorder.Body.Len() == 0 {
		t.Fatalf("status %d, body %q", recorder.Code, recorder.Body.String())
	}
	line := out.String()
	if !strings.Contains(line, "refused POST /v1/requests: Unauthorized (401), Device authentication required") || strings.Contains(line, "not-a-real-credential") {
		t.Fatalf("line %q", line)
	}
}

func TestPiecesOfAMinuteLeaveOneLineOfNumbers(t *testing.T) {
	var lines []string
	var waits []time.Duration
	var runs []func()
	tally := &pieceMinute{
		print: func(line string) { lines = append(lines, line) },
		after: func(wait time.Duration, run func()) { waits = append(waits, wait); runs = append(runs, run) },
	}
	start := time.Date(2026, 11, 20, 19, 39, 10, 0, time.UTC)
	for i, status := range []int{200, 200, 201, 400, 500} {
		tally.count(start, status, i+1)
	}
	if len(lines) != 0 || len(waits) != 1 || waits[0] != 51*time.Second {
		t.Fatalf("lines %v, waits %v", lines, waits)
	}
	// The minute is over and no later piece came: the line is written once.
	runs[0]()
	runs[0]()
	want := "pieces in the minute from 19:39: 2 stored, 1 that made a part whole, 1 refused, 1 failed, at most 5 arriving at once"
	if len(lines) != 1 || lines[0] != want {
		t.Fatalf("lines %q", lines)
	}
	// A piece in a later minute writes the earlier minute's line itself.
	if tally.arriving(1) != 1 || tally.arriving(1) != 2 || tally.arriving(-1) != 1 {
		t.Fatal("the count of pieces arriving does not follow")
	}
	tally.count(start.Add(2*time.Minute), 200, 2)
	tally.count(start.Add(3*time.Minute), 200, 1)
	if len(lines) != 2 || lines[1] != "pieces in the minute from 19:41: 1 stored, 0 that made a part whole, 0 refused, 0 failed, at most 2 arriving at once" {
		t.Fatalf("lines %q", lines)
	}
	// The earlier minute's timer finds its line written and writes nothing.
	runs[1]()
	if len(lines) != 2 {
		t.Fatalf("lines %q", lines)
	}
}

func TestAnAnswerPollLeavesNumbersOnly(t *testing.T) {
	if got := answerPollLine(1300*time.Millisecond, 4, false); got != "phone answer poll: 4 answers after 1.3 s" {
		t.Fatalf("line %q", got)
	}
	if got := answerPollLine(8*time.Second, 0, true); got != "phone answer poll: the phone went away after 8.0 s" {
		t.Fatalf("line %q", got)
	}
}
