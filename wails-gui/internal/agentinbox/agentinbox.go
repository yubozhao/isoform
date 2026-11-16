// Package agentinbox is the channel a joined agent works through
// (phone-agent v1.1.0 rules 7 and 8, B5): it reads the hand-offs the person
// said yes to at /a/inbox and posts each result, with any files it made, to
// /a/results/<id>. It sees only what each hand-off sends. Results are data:
// stored and shown, never run.
package agentinbox

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"wails-gui/internal/tasklink"
)

const (
	// InboxPath and ResultsPrefix are the agent's two routes.
	InboxPath     = "/a/inbox"
	ResultsPrefix = "/a/results/"
	// Lifetime is how long a hand-off waits for its result.
	Lifetime = tasklink.Lifetime
	// MaxBody bounds a result with its files.
	MaxBody = 1 << 20
	// MaxFiles and MaxFileBytes bound the files a result carries.
	MaxFiles     = 5
	MaxFileBytes = 256 << 10
)

// Item is one hand-off waiting in an agent's inbox.
type Item struct {
	ID         string `json:"id"`
	AgentID    string `json:"agentId"` // the joined agent's id
	SourceKind string `json:"sourceKind,omitempty"`
	SourceID   string `json:"sourceId,omitempty"`
	Text       string `json:"text"`
	SentAt     int64  `json:"sentAt"`
	ExpiresAt  int64  `json:"expiresAt"`
	TakenAt    int64  `json:"takenAt,omitempty"`
}

// File is one text file an agent made.
type File struct {
	Name string `json:"name"`
	Text string `json:"text"`
}

// Result is an agent's answer to one hand-off.
type Result struct {
	tasklink.Result
	Files []File `json:"files,omitempty"`
}

// Hooks connect the inbox to the app.
type Hooks struct {
	// Authenticate names the joined agent a request comes from.
	Authenticate func(r *http.Request) (agentID string, ok bool)
	// Allowed runs on every request: task links on, Closed Eyes off.
	Allowed func() error
	// Changed is told when items change (to save them).
	Changed func()
	// Taken is told the first time an agent reads an item.
	Taken func(Item)
	// Resulted is told an accepted result; the item is gone after it.
	Resulted func(Item, Result)
	Now      func() time.Time
}

// Inbox holds the waiting hand-offs.
type Inbox struct {
	mu    sync.Mutex
	items map[string]*Item
	hooks Hooks
}

// New makes an empty inbox.
func New(hooks Hooks) *Inbox { return &Inbox{items: map[string]*Item{}, hooks: hooks} }

func (b *Inbox) now() time.Time {
	if b.hooks.Now != nil {
		return b.hooks.Now()
	}
	return time.Now()
}

func (b *Inbox) changed() {
	if b.hooks.Changed != nil {
		b.hooks.Changed()
	}
}

// Add puts a hand-off in an agent's inbox; the person's Send is the call.
func (b *Inbox) Add(item Item) {
	now := b.now()
	item.SentAt, item.ExpiresAt = now.UnixMilli(), now.Add(Lifetime).UnixMilli()
	b.mu.Lock()
	b.items[item.ID] = &item
	b.mu.Unlock()
	b.changed()
}

// Withdraw removes the items that match (all when match is nil).
func (b *Inbox) Withdraw(match func(Item) bool) int {
	b.mu.Lock()
	n := 0
	for id, item := range b.items {
		if match == nil || match(*item) {
			delete(b.items, id)
			n++
		}
	}
	b.mu.Unlock()
	if n > 0 {
		b.changed()
	}
	return n
}

// Items lists every waiting item, oldest first, to save.
func (b *Inbox) Items() []Item {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sweepLocked()
	out := make([]Item, 0, len(b.items))
	for _, item := range b.items {
		out = append(out, *item)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SentAt < out[j].SentAt })
	return out
}

// Restore loads saved items.
func (b *Inbox) Restore(items []Item) {
	b.mu.Lock()
	for i := range items {
		item := items[i]
		b.items[item.ID] = &item
	}
	b.sweepLocked()
	b.mu.Unlock()
}

func (b *Inbox) sweepLocked() {
	now := b.now().UnixMilli()
	for id, item := range b.items {
		if now >= item.ExpiresAt {
			delete(b.items, id)
		}
	}
}

func headers(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Cache-Control", "no-store")
	h.Set("X-Robots-Tag", "noindex, nofollow")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Content-Type-Options", "nosniff")
}

// ServeHTTP answers /a/inbox and /a/results/<id>.
func (b *Inbox) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	headers(w)
	agentID, ok := "", false
	if b.hooks.Authenticate != nil {
		agentID, ok = b.hooks.Authenticate(r)
	}
	if !ok {
		w.Header().Set("WWW-Authenticate", `Bearer realm="isoform"`)
		http.Error(w, "Send the token you got when you joined, as Authorization: Bearer <token>. If you were disconnected, ask your owner to connect you again.", http.StatusUnauthorized)
		return
	}
	if b.hooks.Allowed != nil {
		if err := b.hooks.Allowed(); err != nil {
			http.Error(w, "Your owner's Isoform isn't taking agent work right now. Try again later.", http.StatusServiceUnavailable)
			return
		}
	}
	switch {
	case r.URL.Path == InboxPath && (r.Method == http.MethodGet || r.Method == http.MethodHead):
		b.serveInbox(w, r, agentID)
	case strings.HasPrefix(r.URL.Path, ResultsPrefix) && r.Method == http.MethodPost:
		b.serveResult(w, r, agentID, strings.TrimPrefix(r.URL.Path, ResultsPrefix))
	default:
		http.NotFound(w, r)
	}
}

// What the agent reads with its inbox.
const (
	InboxInstructions = "These are the tasks your owner said yes to in Isoform. Do each, then POST its result to its resultUrl. Read this inbox again for new work."
	Limits            = "Do the work a task asks for: read, research, compare, summarise, draft, or write code on a new branch. Stop at a draft for anything that sends, posts, buys, books or deletes, and say so in the summary."
	ResultShape       = `{"status": "done" | "blocked" | "declined", "summary": "<what you did, under 2,000 characters>", "links": ["<optional URLs>"], "files": [{"name": "<file name>", "text": "<its text, under 256 KB>"}]}`
)

type inboxItem struct {
	ID        string `json:"id"`
	Task      string `json:"task"`
	SentAt    int64  `json:"sentAt"`
	ExpiresAt int64  `json:"expiresAt"`
	ResultURL string `json:"resultUrl"`
}

func (b *Inbox) serveInbox(w http.ResponseWriter, r *http.Request, agentID string) {
	base := "http://" + r.Host
	if r.TLS != nil {
		base = "https://" + r.Host
	}
	var taken []Item
	out := []inboxItem{}
	b.mu.Lock()
	b.sweepLocked()
	for _, item := range b.items {
		if item.AgentID != agentID {
			continue
		}
		if item.TakenAt == 0 && r.Method == http.MethodGet {
			item.TakenAt = b.now().UnixMilli()
			taken = append(taken, *item)
		}
		out = append(out, inboxItem{ID: item.ID, Task: item.Text, SentAt: item.SentAt, ExpiresAt: item.ExpiresAt, ResultURL: base + ResultsPrefix + item.ID})
	}
	b.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].SentAt < out[j].SentAt })
	for _, item := range taken {
		if b.hooks.Taken != nil {
			b.hooks.Taken(item)
		}
	}
	if len(taken) > 0 {
		b.changed()
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"instructions": InboxInstructions,
		"note":         tasklink.DataNote,
		"limits":       Limits,
		"resultShape":  ResultShape,
		"items":        out,
	})
}

var fileName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._ -]{0,79}$`)

// ParseResult checks a result: the task link's shape plus a few text files.
func ParseResult(body []byte) (Result, error) {
	var raw struct {
		Status  string          `json:"status"`
		Summary string          `json:"summary"`
		Links   []string        `json:"links,omitempty"`
		Files   json.RawMessage `json:"files,omitempty"`
	}
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&raw); err != nil {
		return Result{}, errors.New("the answer must be " + ResultShape)
	}
	base, _ := json.Marshal(struct {
		Status  string   `json:"status"`
		Summary string   `json:"summary"`
		Links   []string `json:"links,omitempty"`
	}{raw.Status, raw.Summary, raw.Links})
	parsed, err := tasklink.ParseResult(base)
	if err != nil {
		return Result{}, err
	}
	out := Result{Result: parsed}
	if len(raw.Files) > 0 {
		if err := json.Unmarshal(raw.Files, &out.Files); err != nil {
			return Result{}, errors.New("files must be [{\"name\", \"text\"}]")
		}
	}
	if len(out.Files) > MaxFiles {
		return Result{}, fmt.Errorf("at most %d files", MaxFiles)
	}
	seen := map[string]bool{}
	for _, f := range out.Files {
		if !fileName.MatchString(f.Name) || strings.Contains(f.Name, "..") {
			return Result{}, errors.New("a file name is letters, digits, dot, dash, underscore or space, up to 80")
		}
		if seen[strings.ToLower(f.Name)] {
			return Result{}, errors.New("file names must differ")
		}
		seen[strings.ToLower(f.Name)] = true
		if len(f.Text) > MaxFileBytes || !utf8.ValidString(f.Text) {
			return Result{}, fmt.Errorf("a file is UTF-8 text under %d KB", MaxFileBytes>>10)
		}
	}
	return out, nil
}

func (b *Inbox) serveResult(w http.ResponseWriter, r *http.Request, agentID, id string) {
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(strings.ToLower(strings.TrimSpace(ct)), "application/json") {
		http.Error(w, "send application/json", http.StatusUnsupportedMediaType)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, MaxBody+1))
	if err != nil || len(body) > MaxBody {
		http.Error(w, "the answer is too large", http.StatusRequestEntityTooLarge)
		return
	}
	result, err := ParseResult(body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	b.mu.Lock()
	b.sweepLocked()
	item, ok := b.items[id]
	if !ok || item.AgentID != agentID {
		b.mu.Unlock()
		http.Error(w, "No task with that id is waiting for you.", http.StatusNotFound)
		return
	}
	if item.TakenAt == 0 {
		item.TakenAt = b.now().UnixMilli()
	}
	snapshot := *item
	delete(b.items, id)
	b.mu.Unlock()
	b.changed()
	if b.hooks.Resulted != nil {
		b.hooks.Resulted(snapshot, result)
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, "Result received.\n")
}
