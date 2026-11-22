package httpapi

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"isoform-relay/internal/limit"
)

// statusWriter keeps the status a handler answered with.
type statusWriter struct {
	http.ResponseWriter
	status    int
	limitLine string
}

func (w *statusWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusWriter) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(body)
}

// Unwrap lets http.ResponseController reach the real writer.
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// logUpload serves an upload route and writes one line saying how it ended:
// who asked (a phone or the computer), the method, the first eight characters
// of the upload id and the status. On 2026-11-19 four finished parts sat on
// the relay for hours and nothing here said whether the computer had asked
// for them, or what a phone's piece was answered with. A stored piece (200 to
// a piece PUT) is not written: a part has eleven of them, and the 201 that
// ends the part says they arrived.
func logUpload(w http.ResponseWriter, r *http.Request, prefix string, serve func(http.ResponseWriter, *http.Request)) {
	kept := &statusWriter{ResponseWriter: w}
	id, piece := strings.CutSuffix(strings.TrimPrefix(r.URL.Path, prefix), "/pieces")
	phonePiece := piece && r.Method == http.MethodPut && prefix == "/v1/uploads/"
	atOnce := 0
	if phonePiece {
		atOnce = pieces.arriving(1)
	}
	serve(kept, r)
	status := kept.status
	if status == 0 {
		status = http.StatusOK
	}
	if phonePiece {
		pieces.arriving(-1)
		pieces.count(time.Now(), status, atOnce)
	}
	if piece && r.Method == http.MethodPut && status == http.StatusOK {
		return
	}
	kept.logStatus(uploadLogLine(prefix == "/v1/hub/uploads/", r.Method, id, piece, status))
}

func (w *statusWriter) logStatus(line string) {
	if w.limitLine != "" {
		line += " " + w.limitLine
	}
	log.Print(line)
}

// logLimit keeps the existing upload or request context on the same line.
func (h *Handler) logLimit(w http.ResponseWriter, r *http.Request, name string, count, cap int64) {
	line := limit.Line(h.limitRoute(r), name, count, cap)
	if kept, ok := w.(*statusWriter); ok {
		kept.limitLine = line
		return
	}
	log.Print(line)
}

func (h *Handler) logLimitError(w http.ResponseWriter, r *http.Request, err error) {
	var refusal *limit.Refusal
	if errors.As(err, &refusal) {
		h.logLimit(w, r, refusal.Name, refusal.Count, refusal.Cap)
	}
}

// uploadLogLine is the line itself. Only the start of a well-formed id is
// written, so nothing a caller made up reaches the log.
func uploadLogLine(computer bool, method, id string, piece bool, status int) string {
	who, what := "phone", "upload"
	if computer {
		who = "computer"
	}
	if piece {
		what = "upload pieces"
	}
	short := "invalid"
	if len(id) == 36 && strings.IndexFunc(id, func(r rune) bool {
		return !(r == '-' || (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f'))
	}) < 0 {
		short = id[:8]
	}
	switch method {
	case http.MethodGet, http.MethodPut, http.MethodDelete:
	default:
		method = "other"
	}
	return who + " " + method + " " + what + " " + short + ": " + http.StatusText(status) + " (" + strconv.Itoa(status) + ")"
}

// logRequest writes one line for each request a phone sends for the computer,
// with the status it was answered with. The request itself is sealed, so the
// line cannot say what was asked. On 2026-11-19 a phone woke for eight
// seconds, read its uploads and the computer logged no request for a part:
// nothing here said whether the phone had sent one.
func logRequest(serve func(http.ResponseWriter, *http.Request)) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		kept := &statusWriter{ResponseWriter: w}
		serve(kept, r)
		status := kept.status
		if status == 0 {
			status = http.StatusOK
		}
		kept.logStatus(requestLogLine(status))
	}
}

func requestLogLine(status int) string {
	return "phone request for the computer: " + http.StatusText(status) + " (" + strconv.Itoa(status) + ")"
}

// queueFailureLine says why a phone request could not be queued. It names the
// cause only; nothing of the request is in it.
func queueFailureLine(err error) string {
	return "phone request not queued: " + failureCause(err)
}

// failureCause names why a store call failed, and nothing of what was being
// stored. A store error is a SQL or file error text, which holds no bound values.
func failureCause(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "the phone went away first"
	case errors.Is(err, context.DeadlineExceeded):
		return "timed out"
	default:
		return "store: " + err.Error()
	}
}

// refusedPieceLine says why a piece was answered 400: the reason, where the
// piece belongs, and how much of it arrived. Numbers only; nothing of the piece.
// On 2026-11-20 one upload had sixteen pieces answered 400 over two hours and
// the log could not say which of the three reasons applied or how far a piece got.
func refusedPieceLine(reason string, offset, total, size, arrived, announced int64) string {
	return fmt.Sprintf("piece refused: %s; offset %d of %d, piece size %d, %d of %d bytes arrived", reason, offset, total, size, arrived, announced)
}

// refuse answers 401 or 403 and writes one line saying which route refused and
// why. On 2026-11-20 a phone's pass ended on a 403 and no log could say whether
// this relay or the tunnel in front of it had answered: a refusal here now
// leaves a line, so a 403 with no line came from somewhere else.
func refuse(w http.ResponseWriter, r *http.Request, status int, reason string) {
	log.Print(refusalLine(r.Method, r.URL.Path, status, reason))
	reply(w, status, nil, reason)
}

// refusalLine is the line itself: method, route, status and the fixed reason.
// Nothing of the credential, the device or the body is in it.
func refusalLine(method, path string, status int, reason string) string {
	switch method {
	case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch, http.MethodHead:
	default:
		method = "other"
	}
	return "refused " + method + " " + routeName(path) + ": " + http.StatusText(status) + " (" + strconv.Itoa(status) + "), " + reason
}

// refusalPrefixes are the routes whose path ends in an id.
var refusalPrefixes = []string{"/v1/hub/attachments/", "/v1/hub/uploads/", "/v1/attachments/", "/v1/uploads/", "/v1/pairings/", "/v1/notices/"}

// routeName is the route a path names, without anything a caller chose: an
// id is cut off, and a path that is not made of lower-case words is not written.
func routeName(path string) string {
	for _, prefix := range refusalPrefixes {
		if strings.HasPrefix(path, prefix) {
			return prefix + "..."
		}
	}
	if len(path) > 64 || !strings.HasPrefix(path, "/v1/") {
		return "another route"
	}
	for _, c := range strings.TrimPrefix(path, "/v1/") {
		if (c < 'a' || c > 'z') && c != '/' && c != '-' {
			return "another route"
		}
	}
	return path
}

// pieceMinute counts how the pieces of one minute ended. A stored piece leaves
// no line of its own (a part has eleven), so until 2026-11-20 the log could not
// say whether a phone sent pieces while its app was away, only when a part
// became whole. One line a minute, written when the minute is over, says it.
type pieceMinute struct {
	mu      sync.Mutex
	minute  time.Time
	stored  int
	whole   int
	refused int
	failed  int
	// flying is how many pieces are arriving right now; most is the most at once in the minute.
	flying int
	most   int
	print  func(string)
	after  func(time.Duration, func())
}

var pieces = &pieceMinute{
	print: func(line string) { log.Print(line) },
	after: func(wait time.Duration, run func()) { time.AfterFunc(wait, run) },
}

// count adds one piece's answer to its minute. The first piece of a minute
// sets the line to be written when that minute is over.
func (p *pieceMinute) count(now time.Time, status int, atOnce int) {
	minute := now.Truncate(time.Minute)
	p.mu.Lock()
	line := ""
	if !p.minute.IsZero() && !minute.Equal(p.minute) {
		line = p.takeLocked()
	}
	first := p.minute.IsZero()
	p.minute = minute
	p.most = max(p.most, atOnce)
	switch {
	case status == http.StatusCreated:
		p.whole++
	case status >= 200 && status < 300:
		p.stored++
	case status >= 400 && status < 500:
		p.refused++
	default:
		p.failed++
	}
	p.mu.Unlock()
	if line != "" {
		p.print(line)
	}
	if first {
		p.after(minute.Add(time.Minute+time.Second).Sub(now), func() { p.flush(minute) })
	}
}

// flush writes the line of minute if no later piece has written it already.
func (p *pieceMinute) flush(minute time.Time) {
	p.mu.Lock()
	line := ""
	if minute.Equal(p.minute) {
		line = p.takeLocked()
	}
	p.mu.Unlock()
	if line != "" {
		p.print(line)
	}
}

func (p *pieceMinute) takeLocked() string {
	line := pieceMinuteLine(p.minute, p.stored, p.whole, p.refused, p.failed, p.most)
	p.minute, p.stored, p.whole, p.refused, p.failed, p.most = time.Time{}, 0, 0, 0, 0, 0
	return line
}

// pieceMinuteLine is the line itself. Numbers only: nothing of a piece, an
// upload or a phone.
func pieceMinuteLine(minute time.Time, stored, whole, refused, failed, most int) string {
	return fmt.Sprintf("pieces in the minute from %s: %d stored, %d that made a part whole, %d refused, %d failed, at most %d arriving at once",
		minute.Format("15:04"), stored, whole, refused, failed, most)
}

// arriving moves the count of pieces arriving right now and says what it is
// after. A phone hands up to eight parts of eleven pieces to its system at a
// time; until 2026-11-20 nothing said how many of them were on the wire together.
func (p *pieceMinute) arriving(change int) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.flying += change
	return p.flying
}

// answerPollLine says how a phone's wait for the computer's answers ended: how
// long the relay held the request and how many answers it carried, or that the
// phone went away first. On 2026-11-20 the computer answered a phone within a
// second and the phone acted on the answers 8 and 13 seconds later; no log
// could say whether the relay held them or the phone did. A wait that ends
// with nothing, as most do, leaves no line. Numbers only.
func answerPollLine(held time.Duration, answers int, gone bool) string {
	seconds := fmt.Sprintf("%.1f s", held.Seconds())
	if gone {
		return "phone answer poll: the phone went away after " + seconds
	}
	return fmt.Sprintf("phone answer poll: %d answers after %s", answers, seconds)
}
