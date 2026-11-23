package destinations

import (
	"context"
	"log"
	"maps"
	"net/http"
	"net/http/httptrace"
	"sync"

	"time"
)

// Call carries only a registry ID and, for searches and MCP answers, an
// allowlisted service or authenticated client ID.
type Call struct {
	ID     ID
	Detail string
	At     time.Time
}

// RecorderStore owns the crash marker and the rolling count and loss buckets.
type RecorderStore interface {
	BeginDestinationRun(context.Context) (unclean bool, err error)
	EndDestinationRun(context.Context) error
	RecordDestination(context.Context, Call) error
	MarkDestinationIncomplete(context.Context, ID, time.Time) error
	PruneDestinationCalls(context.Context) error
}

type Recorder struct {
	store RecorderStore
	queue chan Call
	stop  chan struct{}
	done  chan struct{}
	// Scheduling and loss timestamps share a short lock with publication.
	// The worker publishes stopErr by closing done. No I/O holds recorderMu.
	lost      map[ID]time.Time
	accepted  uint64
	completed uint64
	changed   chan struct{}
	waiters   bool
	stopErr   error
}

var recorderMu sync.Mutex
var active *Recorder
var beforeRecorder = make(map[ID]time.Time)

func StartRecorder(store RecorderStore) (*Recorder, error) {
	unclean, err := store.BeginDestinationRun(context.Background())
	if err != nil {
		return nil, err
	}
	r := &Recorder{store: store, queue: make(chan Call, 256), stop: make(chan struct{}), done: make(chan struct{}), changed: make(chan struct{})}
	recorderMu.Lock()
	r.lost = beforeRecorder
	beforeRecorder = make(map[ID]time.Time)
	if unclean {
		// Sends never wait for a per-event disk journal. After a crash the
		// pending IDs are unknowable, so recovery covers all content routes.
		now := time.Now()
		for _, d := range List() {
			if d.Content != ContentNone {
				r.lost[d.ID] = now
			}
		}
	}
	active = r
	recorderMu.Unlock()
	go r.run()
	return r, nil
}

func enqueue(id ID, detail string) {
	recorderMu.Lock()
	defer recorderMu.Unlock()
	now := time.Now()
	if active == nil {
		beforeRecorder[id] = now
		return
	}
	select {
	case active.queue <- Call{id, detail, now}:
		active.accepted++
	default:
		active.lost[id] = now
	}
}

func (r *Recorder) record(call Call) {
	err := r.store.RecordDestination(context.Background(), call)
	recorderMu.Lock()
	defer recorderMu.Unlock()
	if err != nil && call.At.After(r.lost[call.ID]) {
		r.lost[call.ID] = call.At
	}
	r.completed++
	if r.waiters {
		close(r.changed)
		r.changed = make(chan struct{})
		r.waiters = false
	}
}

// Wait only on the Settings read side. Later sends cannot extend this fence.
func (r *Recorder) Wait(ctx context.Context) error {
	recorderMu.Lock()
	defer recorderMu.Unlock()
	target := r.accepted
	for r.completed < target {
		ch := r.changed
		r.waiters = true
		recorderMu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
		}
		recorderMu.Lock()
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	return nil
}

// Losses also covers a full or unavailable database. Keep the in-memory
// timestamp even after persistence so a simultaneous read sees either copy.
func (r *Recorder) Losses() map[ID]time.Time {
	recorderMu.Lock()
	defer recorderMu.Unlock()
	return maps.Clone(r.lost)
}

func (r *Recorder) run() {
	defer close(r.done)
	ctx := context.Background()
	saved := make(map[ID]time.Time)
	flushLosses := func() error {
		var lastErr error
		for id, at := range r.Losses() {
			if !at.After(saved[id]) {
				continue
			}
			if err := r.store.MarkDestinationIncomplete(ctx, id, at); err != nil {
				log.Printf("[destinations] incomplete mark write failed: %s", id)
				lastErr = err
			} else {
				log.Printf("[destinations] weekly count incomplete: %s", id)
				saved[id] = at
			}
		}
		return lastErr
	}
	prune := func() {
		if err := r.store.PruneDestinationCalls(ctx); err != nil {
			log.Print("[destinations] count pruning failed")
		}
	}
	flushLosses()
	prune()
	hour := time.NewTicker(time.Hour)
	defer hour.Stop()
	retry := time.NewTicker(time.Second)
	defer retry.Stop()
	for {
		select {
		case call := <-r.queue:
			r.record(call)
		case <-retry.C:
			flushLosses()
		case <-hour.C:
			prune()
		case <-r.stop:
			for len(r.queue) > 0 {
				r.record(<-r.queue)
			}
			r.stopErr = flushLosses()
			if r.stopErr == nil {
				r.stopErr = r.store.EndDestinationRun(ctx)
			}
			return
		}
	}
}

// Close is called after the senders stop and before SQLite closes. The same
// lock used by enqueue fences the last accepted event before the drain.
func (r *Recorder) Close() error {
	recorderMu.Lock()
	if active == r {
		active = nil
	}
	select {
	case <-r.stop:
	default:
		close(r.stop)
	}
	recorderMu.Unlock()
	<-r.done
	return r.stopErr
}

func Record(id ID) {
	if _, ok := Lookup(id); ok {
		enqueue(id, "")
	}
}

func RecordSearch(service string) {
	switch service {
	case "", "claude_code", "grok", "gemini", "doubao":
		enqueue(AssistantSearch, service)
	}
}

func RecordAnswer(client string) {
	switch client {
	case "claude-code", "codex":
		enqueue(AgentsMCPRead, client)
	}
}

// CountRequest counts one logical send once the transport writes it, including
// a send whose reply never arrives. A dial or TLS failure sends no payload.
func CountRequest(req *http.Request, id ID) *http.Request {
	return req.WithContext(CountHTTP(req.Context(), id))
}

// CountHTTP attaches the same write observation to SDK calls that take a context.
func CountHTTP(ctx context.Context, id ID) context.Context {
	var once sync.Once
	trace := &httptrace.ClientTrace{WroteRequest: func(info httptrace.WroteRequestInfo) {
		if info.Err == nil {
			once.Do(func() { Record(id) })
		}
	}}
	return httptrace.WithClientTrace(ctx, trace)
}
