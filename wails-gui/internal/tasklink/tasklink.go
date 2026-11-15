// Package tasklink serves one-time task links: /t/<token> (design:
// docs/design/task-link-handoff.md). GET gives an agent its task and how to
// answer; POST takes one JSON result. A link serves one task for one agent
// and is spent by its result or after 24 hours. Tokens are kept only as
// hashes. The package is pure: the app supplies the checks (Closed Eyes, a
// withdrawn source) and hears about taken and results through callbacks.
package tasklink

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	// Lifetime is how long a link answers without a result.
	Lifetime = 24 * time.Hour
	// MaxGets and MaxPosts bound each token.
	MaxGets  = 30
	MaxPosts = 3
	// MaxBody bounds a result POST.
	MaxBody = 8 << 10
	// MaxSummary bounds a result's summary.
	MaxSummary = 2000
	// MaxLinks bounds a result's links.
	MaxLinks = 10
	// PathPrefix is where links live.
	PathPrefix = "/t/"
)

// Result statuses an agent may post.
const (
	StatusDone     = "done"
	StatusBlocked  = "blocked"
	StatusDeclined = "declined"
)

// Result is one agent's answer. Its text is data: shown and stored, never
// run as an instruction.
type Result struct {
	Status  string   `json:"status"`
	Summary string   `json:"summary"`
	Links   []string `json:"links,omitempty"`
}

// Link is one stored link. Token is never kept: only its hash.
type Link struct {
	ID        string  `json:"id"`
	TokenHash string  `json:"tokenHash"`
	AgentID   string  `json:"agentId"`
	Text      string  `json:"text"`
	CreatedAt int64   `json:"createdAt"`
	ExpiresAt int64   `json:"expiresAt"`
	Gets      int     `json:"gets"`
	Posts     int     `json:"posts"`
	TakenAt   int64   `json:"takenAt,omitempty"`
	Result    *Result `json:"result,omitempty"`
}

// Hooks connect the store to the app.
type Hooks struct {
	// Allowed is checked on every request; an error withdraws the link
	// (Closed Eyes on, the task's source forgotten).
	Allowed func(link Link) error
	// Taken is told the first real GET.
	Taken func(link Link)
	// Resulted is told the accepted result.
	Resulted func(link Link, result Result)
	// Now is the clock.
	Now func() time.Time
}

// Store holds the open links in memory. Persisting them is the app's
// choice (Links and Restore).
type Store struct {
	mu    sync.Mutex
	links map[string]*Link // by token hash
	hooks Hooks
}

// NewStore makes an empty store.
func NewStore(hooks Hooks) *Store {
	if hooks.Now == nil {
		hooks.Now = time.Now
	}
	return &Store{links: map[string]*Link{}, hooks: hooks}
}

// Create makes a link for one task and one agent and returns its token. The
// token is shown once, in the URL; only its hash is kept.
func (s *Store) Create(id, agentID, text string) (string, error) {
	if strings.TrimSpace(text) == "" {
		return "", errors.New("a task link needs a task")
	}
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	token := strings.ToLower(strings.TrimRight(base32.StdEncoding.EncodeToString(raw[:]), "="))
	now := s.hooks.Now()
	link := &Link{ID: id, TokenHash: hashToken(token), AgentID: agentID, Text: text,
		CreatedAt: now.UnixMilli(), ExpiresAt: now.Add(Lifetime).UnixMilli()}
	s.mu.Lock()
	s.links[link.TokenHash] = link
	s.mu.Unlock()
	return token, nil
}

// Withdraw spends every open link, for Closed Eyes or a forget.
func (s *Store) Withdraw(match func(Link) bool) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for hash, link := range s.links {
		if match == nil || match(*link) {
			delete(s.links, hash)
			n++
		}
	}
	return n
}

// Links returns the open links, for persisting.
func (s *Store) Links() []Link {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked()
	out := make([]Link, 0, len(s.links))
	for _, link := range s.links {
		out = append(out, *link)
	}
	return out
}

// Restore loads persisted links, dropping expired or answered ones.
func (s *Store) Restore(links []Link) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range links {
		link := links[i]
		s.links[link.TokenHash] = &link
	}
	s.sweepLocked()
}

func (s *Store) sweepLocked() {
	now := s.hooks.Now().UnixMilli()
	for hash, link := range s.links {
		if link.ExpiresAt <= now || link.Result != nil {
			delete(s.links, hash)
		}
	}
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// lookup finds a link by token with a constant-time compare against each
// stored hash, so timing says nothing about near misses.
func (s *Store) lookupLocked(token string) *Link {
	want := hashToken(token)
	var found *Link
	for hash, link := range s.links {
		if subtle.ConstantTimeCompare([]byte(hash), []byte(want)) == 1 {
			found = link
		}
	}
	return found
}

// previewAgents are link-preview fetchers: they get a title only and never
// count as the agent taking the task.
var previewAgents = []string{
	"facebookexternalhit", "slackbot", "twitterbot", "discordbot", "whatsapp",
	"telegrambot", "linkedinbot", "applebot", "skypeuripreview", "iframely",
	"embedly", "redditbot", "googlebot", "bingbot",
}

func isPreview(r *http.Request) bool {
	ua := strings.ToLower(r.UserAgent())
	for _, bot := range previewAgents {
		if strings.Contains(ua, bot) {
			return true
		}
	}
	// iMessage's preview fetcher identifies as a plain Safari with this tag.
	return strings.Contains(ua, "facebot twitterbot") || r.Header.Get("X-Purpose") == "preview" || r.Header.Get("Purpose") == "prefetch"
}

// ServeHTTP answers /t/<token>.
func (s *Store) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Cache-Control", "no-store")
	h.Set("X-Robots-Tag", "noindex, nofollow")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Content-Type-Options", "nosniff")
	if !strings.HasPrefix(r.URL.Path, PathPrefix) {
		http.NotFound(w, r)
		return
	}
	token := strings.TrimPrefix(r.URL.Path, PathPrefix)
	if token == "" || len(token) > 64 || strings.Contains(token, "/") {
		http.NotFound(w, r)
		return
	}
	s.mu.Lock()
	s.sweepLocked()
	link := s.lookupLocked(token)
	if link == nil {
		s.mu.Unlock()
		http.Error(w, "This task link has expired or was already used.", http.StatusGone)
		return
	}
	snapshot := *link
	s.mu.Unlock()
	if s.hooks.Allowed != nil {
		if err := s.hooks.Allowed(snapshot); err != nil {
			s.Withdraw(func(l Link) bool { return l.TokenHash == snapshot.TokenHash })
			http.Error(w, "This task link has expired or was already used.", http.StatusGone)
			return
		}
	}
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		s.serveGet(w, r, link)
	case http.MethodPost:
		s.servePost(w, r, link)
	default:
		h.Set("Allow", "GET, HEAD, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Store) serveGet(w http.ResponseWriter, r *http.Request, link *Link) {
	if isPreview(r) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, "A task from Isoform\n")
		return
	}
	s.mu.Lock()
	if link.Gets >= MaxGets {
		s.mu.Unlock()
		http.Error(w, "too many reads of this link", http.StatusTooManyRequests)
		return
	}
	link.Gets++
	first := link.TakenAt == 0
	if first {
		link.TakenAt = s.hooks.Now().UnixMilli()
	}
	snapshot := *link
	s.mu.Unlock()
	if first && s.hooks.Taken != nil {
		s.hooks.Taken(snapshot)
	}
	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"task":         snapshot.Text,
			"answer":       "POST a JSON object to this same URL",
			"resultSchema": map[string]any{"status": []string{StatusDone, StatusBlocked, StatusDeclined}, "summary": fmt.Sprintf("string, under %d characters", MaxSummary), "links": "optional list of URLs"},
			"expiresAt":    time.UnixMilli(snapshot.ExpiresAt).UTC().Format(time.RFC3339),
		})
		return
	}
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	if r.Method == http.MethodHead {
		return
	}
	_, _ = io.WriteString(w, Markdown(snapshot.Text, time.UnixMilli(snapshot.ExpiresAt)))
}

// Markdown is the agent-readable page: the task exactly as the person saw
// it, then the fixed instructions for answering.
func Markdown(text string, expires time.Time) string {
	return "# A task from Isoform\n\n" + text + "\n\n## When you're done\n\n" +
		"POST a JSON object to this same URL:\n\n" +
		`{"status": "done" | "blocked" | "declined", "summary": "<what you did, under 2,000 characters>", "links": ["<optional URLs to what you made>"]}` +
		"\n\nThis link works for one result and expires " + expires.UTC().Format(time.RFC3339) + ".\n"
}

func (s *Store) servePost(w http.ResponseWriter, r *http.Request, link *Link) {
	s.mu.Lock()
	if link.Posts >= MaxPosts {
		s.mu.Unlock()
		http.Error(w, "too many answers to this link", http.StatusTooManyRequests)
		return
	}
	link.Posts++
	s.mu.Unlock()
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
	s.mu.Lock()
	if link.Result != nil {
		s.mu.Unlock()
		http.Error(w, "This task link has expired or was already used.", http.StatusGone)
		return
	}
	link.Result = &result
	if link.TakenAt == 0 {
		link.TakenAt = s.hooks.Now().UnixMilli()
	}
	snapshot := *link
	delete(s.links, link.TokenHash)
	s.mu.Unlock()
	if s.hooks.Resulted != nil {
		s.hooks.Resulted(snapshot, result)
	}
	w.WriteHeader(http.StatusNoContent)
}

// ParseResult checks a posted answer against the schema: known fields only,
// a known status, a bounded summary and a few http(s) links.
func ParseResult(body []byte) (Result, error) {
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.DisallowUnknownFields()
	var result Result
	if err := dec.Decode(&result); err != nil {
		return Result{}, errors.New("the answer must be {\"status\", \"summary\", \"links\"}")
	}
	if dec.More() {
		return Result{}, errors.New("send one JSON object")
	}
	switch result.Status {
	case StatusDone, StatusBlocked, StatusDeclined:
	default:
		return Result{}, errors.New("status must be done, blocked or declined")
	}
	result.Summary = strings.TrimSpace(result.Summary)
	if len([]rune(result.Summary)) > MaxSummary {
		return Result{}, fmt.Errorf("summary must be under %d characters", MaxSummary)
	}
	if len(result.Links) > MaxLinks {
		return Result{}, fmt.Errorf("at most %d links", MaxLinks)
	}
	for _, raw := range result.Links {
		u, err := url.Parse(strings.TrimSpace(raw))
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return Result{}, errors.New("links must be http or https URLs")
		}
	}
	return result, nil
}

// Prompt is the one line the agent receives; the task text never enters it.
func Prompt(link string) string {
	return "Your task from Isoform is at " + link + ". Open it, do what it asks, then POST your result to the same link as it explains. The link works once and expires in 24 hours."
}
