package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"wails-gui/internal/pii"
	"yansu-agent/agent"
)

// Your portrait (handoff-2026-10-28-1434-portrait-v1.0.0.md): a name from a
// fixed set of 16 and up to four sentences, saved ahead so About you shows it
// in the first frame. Pass 1 (about_you_portrait_pass1.go) needs no model;
// pass 2 (about_you_portrait_writer.go) is one call to the person's own AI.

const (
	portraitVersion      = 2
	portraitTick         = 10 * time.Minute
	portraitCadence      = 7 * 24 * time.Hour
	portraitWriteTimeout = 3 * time.Minute
	portraitInputLimit   = 60
	portraitEvent        = "portrait:updated"
)

// A failure the AI caused, or an answer Isoform couldn't read, backs off 15
// minutes, then an hour, then every 6 hours (v2.0.0 section 4).
var portraitBackoff = []time.Duration{15 * time.Minute, time.Hour, 6 * time.Hour}

// The cap that protects the person's plan: at most 6 writes a day, never two
// within 10 minutes except Try now. A confirmation from the card waits 30
// seconds for more before it rewrites, and three unreadable answers in a row
// are the first that show.
const (
	portraitWritesPerDay   = 6
	portraitWriteGap       = 10 * time.Minute
	portraitConfirmWait    = 30 * time.Second
	portraitUnusableShown  = 3
	portraitCandidateLimit = 3
	portraitLinesShown     = 2
)

// Reasons a write did not land, worded by the card.
const (
	portraitReasonLimit    = "limit"
	portraitReasonNoAI     = "no_ai"
	portraitReasonNoAnswer = "no_answer"
	portraitReasonUnusable = "unusable"
)

type portraitInput struct {
	MemoryID     string                `json:"memoryId"`
	Digest       string                `json:"digest"`
	Area         string                `json:"area,omitempty"`
	Source       string                `json:"source,omitempty"`
	Confirmed    bool                  `json:"confirmed,omitempty"`
	NotQuite     bool                  `json:"notQuite,omitempty"`
	Days         []string              `json:"days,omitempty"`
	Observations []portraitObservation `json:"observations,omitempty"`
	// Private beliefs (sensitive, or about other people) never become
	// confirm rows.
	Private bool `json:"private,omitempty"`
}

// observations are the dated things the belief rests on. A portrait saved
// before v2.0.0 stored days only: each day counts as one observation.
func (input portraitInput) observations() []portraitObservation {
	if len(input.Observations) > 0 {
		return input.Observations
	}
	out := make([]portraitObservation, 0, len(input.Days))
	for _, day := range input.Days {
		out = append(out, portraitObservation{ID: input.MemoryID + "@" + day, Kind: portraitObservedMoment, Day: day})
	}
	return out
}

type portraitAttempt struct {
	Outcome   string `json:"outcome"`
	Reason    string `json:"reason,omitempty"`
	At        int64  `json:"at"`
	RetryAt   int64  `json:"retryAt,omitempty"`
	Assistant string `json:"assistant,omitempty"`
	Failures  int    `json:"failures,omitempty"`
}

// portraitDocument is the saved portrait: everything the card shows, the
// inputs it rests on, and how the last attempt went.
type portraitDocument struct {
	Version     int                       `json:"version"`
	GeneratedAt int64                     `json:"generatedAt,omitempty"`
	Language    string                    `json:"language,omitempty"`
	Sides       []float64                 `json:"sides,omitempty"`
	Poles       []int                     `json:"poles,omitempty"`
	NameAreas   [][]string                `json:"nameAreas,omitempty"`
	Sentences   []portraitSentence        `json:"sentences"`
	Lately      *portraitLatelyLine       `json:"lately,omitempty"`
	WorkWithMe  []portraitLine            `json:"workWithMe,omitempty"`
	Inputs      []portraitInput           `json:"inputs"`
	BeliefCount int                       `json:"beliefCount,omitempty"`
	Previous    []portraitSentence        `json:"previous,omitempty"`
	Progress    *AboutYouPortraitProgress `json:"progress,omitempty"`
	Attempt     *portraitAttempt          `json:"attempt,omitempty"`
	Removed     bool                      `json:"removed,omitempty"`
	// Dropped are the claims the checks dropped, with their reasons; never
	// shown. Candidates are the confirm rows, as belief ids.
	Dropped    []portraitDropped `json:"dropped,omitempty"`
	Candidates []string          `json:"candidates,omitempty"`
	// Writes are the times of model writes, for the daily cap. CheckedAt is
	// the last answer that left the shown portrait as it was.
	Writes    []int64 `json:"writes,omitempty"`
	CheckedAt int64   `json:"checkedAt,omitempty"`
	Announced string  `json:"announced,omitempty"`
	// TodaySeen is the name the Today card last showed; it shows again only
	// for a new name.
	TodaySeen string `json:"todaySeen,omitempty"`
	// SaidAt is That's me on the version written at SaidVersion. While it
	// holds, an automatic write waits as Pending behind "Show it" (answer
	// feedback section 11.5); the person's own asks apply at once.
	SaidAt      int64             `json:"saidAt,omitempty"`
	SaidVersion int64             `json:"saidVersion,omitempty"`
	Pending     *portraitDocument `json:"pending,omitempty"`
}

// said is true while That's me holds for the version shown.
func (doc *portraitDocument) said() bool {
	return doc.SaidAt > 0 && doc.SaidVersion == doc.GeneratedAt && len(doc.Sentences) > 0
}

// settlePending drops a waiting version whose beliefs went, and shows it
// once That's me no longer holds. It reports whether the file changed.
func (doc *portraitDocument) settlePending(entries []MemoryEntry) bool {
	if doc.Pending == nil {
		return false
	}
	pending := doc.Pending
	confirmed := confirmedIDs(entries)
	_, stored := pending.keepSupported(liveInputs(pending.Inputs, entries), confirmed)
	switch true {
	case len(pending.Sentences) == 0:
		doc.Pending = nil
		return true
	case !doc.said():
		doc.showPending(confirmed)
		return true
	}
	return stored
}

// showPending makes the waiting version current, keeping the counts that
// moved on since it was written. The shown version's confirmed lines carry
// over too: a confirmation made after the version waited is not lost (Y41).
func (doc *portraitDocument) showPending(confirmed map[string]bool) {
	next := *doc.Pending
	next.Pending, next.SaidAt, next.SaidVersion = nil, 0, 0
	next.Progress, next.Announced, next.TodaySeen, next.Writes, next.Attempt, next.CheckedAt = doc.Progress, doc.Announced, doc.TodaySeen, doc.Writes, doc.Attempt, doc.CheckedAt
	if len(doc.Sentences) > 0 {
		next.Previous = doc.Sentences
	}
	if doc.Language == next.Language {
		next.WorkWithMe = pinLines(doc.WorkWithMe, next.WorkWithMe, confirmed)
	}
	*doc = next
}

type AboutYouPortraitPart struct {
	Text      string   `json:"text"`
	MemoryIDs []string `json:"memoryIds"`
	Confirmed bool     `json:"confirmed"`
}

type AboutYouPortraitSentence struct {
	Role    string                 `json:"role"`
	Parts   []AboutYouPortraitPart `json:"parts"`
	Changed bool                   `json:"changed"`
	// Cause is set on a changed sentence the person's answer changed.
	Cause *AboutYouPortraitCause `json:"cause"`
}

// AboutYouPortraitCause is why a sentence changed: the beliefs confirmed,
// or Not quite with the note, if any.
type AboutYouPortraitCause struct {
	Confirm  []string `json:"confirm"`
	Note     string   `json:"note"`
	NotQuite bool     `json:"notQuite"`
}

type AboutYouPortraitLine struct {
	Text      string   `json:"text"`
	MemoryIDs []string `json:"memoryIds"`
	Area      string   `json:"area"`
	// Source is the one real place every belief under the line came from
	// ("slack"); empty when there isn't one, and then the row has no label.
	Source    string `json:"source,omitempty"`
	Confirmed bool   `json:"confirmed"`
}

type AboutYouPortraitLately struct {
	Parts     []AboutYouPortraitPart `json:"parts"`
	MemoryID  string                 `json:"memoryId"`
	ThisMonth int                    `json:"thisMonth"`
	Usual     int                    `json:"usual"`
}

// AboutYouPortraitCandidate is one confirm row. Answer is "", "confirmed" or
// "noted".
type AboutYouPortraitCandidate struct {
	MemoryID string `json:"memoryId"`
	Text     string `json:"text"`
	Answer   string `json:"answer"`
}

type AboutYouPortraitFailure struct {
	Reason    string `json:"reason"`
	At        int64  `json:"at"`
	RetryAt   int64  `json:"retryAt"`
	Assistant string `json:"assistant"`
}

// AboutYouPortrait is what the card shows. State is learning, writing, ready,
// shape (the name and the shape with no sentence yet) or failed (v2.0.0
// sections 2 and 3).
type AboutYouPortrait struct {
	State       string                      `json:"state"`
	Name        []string                    `json:"name"`
	NameAreas   [][]string                  `json:"nameAreas"`
	Sides       []float64                   `json:"sides"`
	Sentences   []AboutYouPortraitSentence  `json:"sentences"`
	Lately      *AboutYouPortraitLately     `json:"lately"`
	WorkWithMe  []AboutYouPortraitLine      `json:"workWithMe"`
	BeliefCount int                         `json:"beliefCount"`
	GeneratedAt int64                       `json:"generatedAt"`
	Progress    *AboutYouPortraitProgress   `json:"progress"`
	Failure     *AboutYouPortraitFailure    `json:"failure"`
	Writing     bool                        `json:"writing"`
	Rewriting   bool                        `json:"rewriting"`
	Forgotten   bool                        `json:"forgotten"`
	Assistant   string                      `json:"assistant"`
	Candidates  []AboutYouPortraitCandidate `json:"candidates"`
	// Confirming is true from That's me on a row until the rewrite lands.
	Confirming bool `json:"confirming"`
	// SaidAt is That's me on the version shown; Pending is a newer version
	// waiting behind "Show it" (answer feedback section 3).
	SaidAt  int64 `json:"saidAt"`
	Pending bool  `json:"pending"`
}

// AboutYouPortraitService runs one write at a time in the background. Reads
// only read the saved document; they never wait for a model.
type AboutYouPortraitService struct {
	// readMemo is the last read's result and the stamp of what it read
	// (portraitReadKey); a read with the same stamp returns a copy of it.
	readMemoMu  sync.Mutex
	readMemoKey string
	readMemoAt  time.Time
	readMemo    []byte
	app         *App
	mu          sync.Mutex
	stop        context.CancelFunc
	done        chan struct{}
	wake        chan struct{}
	attempt     sync.Mutex
	fileMu      sync.Mutex
	stateMu     sync.Mutex
	force       bool
	notQuite    bool
	note        string
	noteID      string
	running     bool
	writer      string
	// confirmAt is when a confirmation from the card rewrites; each new one
	// waits portraitConfirmWait again.
	confirmAt time.Time
	// confirmedRows are the rows confirmed since the last check, so the
	// sentences they bring back can say so.
	confirmedRows []string
	now           func() time.Time
	// names lists other people's names the prose must never carry.
	names func() []string
}

func NewAboutYouPortraitService(app *App) *AboutYouPortraitService {
	s := &AboutYouPortraitService{app: app, wake: make(chan struct{}, 1), now: time.Now}
	s.names = s.peopleNames
	return s
}

// peopleNames reads the names on Who you know, for the no-names check.
func (s *AboutYouPortraitService) peopleNames() []string {
	if s.app == nil || s.app.activity == nil || s.app.activity.store == nil {
		return nil
	}
	overview, err := s.app.ListPeopleOverview()
	if err != nil {
		return nil
	}
	var names []string
	for _, person := range overview.Everyone {
		for _, word := range strings.Fields(person.Name) {
			if len([]rune(word)) >= 3 {
				names = append(names, word)
			}
		}
	}
	return names
}

func (s *AboutYouPortraitService) Start(parent context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stop != nil {
		return
	}
	ctx, cancel := context.WithCancel(parent)
	s.stop, s.done = cancel, make(chan struct{})
	go func() {
		defer close(s.done)
		for {
			s.check(ctx, s.now())
			timer := time.NewTimer(s.nextCheck(s.now()))
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			case <-s.wake:
				timer.Stop()
			}
		}
	}()
}

func (s *AboutYouPortraitService) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stop != nil {
		s.stop()
		<-s.done
		s.stop = nil
	}
}

// nextCheck wakes at the retry time when it comes before the next tick, so a
// limit that resets at 4:00 PM is retried then.
func (s *AboutYouPortraitService) nextCheck(now time.Time) time.Duration {
	wait := portraitTick
	path, err := portraitPath()
	if err != nil {
		return wait
	}
	s.fileMu.Lock()
	doc, _ := readPortrait(path)
	s.fileMu.Unlock()
	if doc != nil && doc.Attempt != nil && doc.Attempt.Outcome == "failed" && doc.Attempt.RetryAt > 0 {
		if until := time.UnixMilli(doc.Attempt.RetryAt).Sub(now); until > 0 && until < wait {
			wait = until + time.Second
		}
	}
	s.stateMu.Lock()
	confirmAt := s.confirmAt
	s.stateMu.Unlock()
	if !confirmAt.IsZero() {
		if until := confirmAt.Sub(now); until < wait {
			wait = max(until, 0) + time.Second
		}
	}
	return wait
}

func (s *AboutYouPortraitService) poke() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func portraitPath() (string, error) {
	root, err := flavorDataDir()
	return filepath.Join(root, "about-you", "portrait.json"), err
}

// readPortrait returns nil for a missing file. A file from an older version
// or one that does not parse is an error; the scheduler writes a new one.
func readPortrait(path string) (*portraitDocument, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var doc portraitDocument
	if err := decodePortraitJSON(raw, &doc); err != nil {
		return nil, err
	}
	if doc.Version != portraitVersion || (len(doc.Sentences) > 0 && (len(doc.Inputs) == 0 || len(doc.Poles) != 4)) || (doc.GeneratedAt > 0 && len(doc.Poles) != 4) {
		return nil, errors.New("invalid_storage")
	}
	return &doc, nil
}

// A synced temporary file is never a candidate for reading. Rename is the only
// publication point, so an interrupted write leaves the last portrait intact.
func writePortrait(path string, doc *portraitDocument) error {
	doc.Version = portraitVersion
	raw, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".portrait-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err := file.Write(raw); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

func decodePortraitJSON(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return errors.New("invalid_json")
	}
	if decoder.Decode(new(any)) != io.EOF {
		return errors.New("invalid_json")
	}
	return nil
}

type portraitBelief struct {
	Title  string   `json:"title"`
	Points []string `json:"points"`
}

func portraitDigest(entry MemoryEntry) string {
	raw, _ := json.Marshal(portraitBelief{Title: entry.Title, Points: parseMemoryPoints(entry.Content)})
	return fmt.Sprintf("%x", sha256.Sum256(raw))
}

func portraitConfirmed(entry MemoryEntry) bool {
	if portraitNotQuite(entry) {
		return false
	}
	if parseMemoryFrontmatter(entry.Content).Source == "user_edit" || entry.Source == "user_edit" {
		return true
	}
	return memoryConfirmedAt(entry.Content) != 0
}

// portraitNotQuite is a belief the person marked Not quite and has not
// confirmed since. It never supports a claim.
func portraitNotQuite(entry MemoryEntry) bool {
	return memoryNotQuiteAt(entry.Content) != 0
}

// keepSupported removes every sentence, clause, Lately line and How to work
// with me line whose support falls below the bar with only the live beliefs.
// A How to work with me line that rests on a confirmed belief (confirmed,
// readable now, whatever its digest) stays while the belief does (Y41).
// It never calls a model. It reports whether shown text went and whether the
// file changed at all: gone beliefs are erased from the inputs and from the
// previous version too.
func (doc *portraitDocument) keepSupported(live map[string]portraitInput, confirmed map[string]bool) (bool, bool) {
	check := portraitCheck{live: live}
	changed := false
	var sentences []portraitSentence
	for _, sentence := range doc.Sentences {
		kept, ok := check.keepBelieved(sentence)
		if !ok || len(kept.Parts) != len(sentence.Parts) {
			changed = true
		}
		if ok {
			sentences = append(sentences, kept)
		}
	}
	// The action sentence never shows alone.
	if visible := portraitVisible(sentences); len(visible) != len(sentences) {
		sentences, changed = visible, true
	}
	doc.Sentences = sentences
	var lines []portraitLine
	flagged := false
	for _, line := range doc.WorkWithMe {
		pin := pinnedLine(line, confirmed)
		if pin || portraitBelieved(line.IDs, live) {
			flagged = flagged || line.Pinned != pin
			line.Pinned = pin
			lines = append(lines, line)
		} else {
			changed = true
		}
	}
	doc.WorkWithMe = lines
	if doc.Lately != nil {
		if _, ok := live[doc.Lately.Fact.MemoryID]; !ok {
			doc.Lately, changed = nil, true
		}
	}
	var previous []portraitSentence
	for _, sentence := range doc.Previous {
		if check.known(sentence.ids()) {
			previous = append(previous, sentence)
		}
	}
	stored := changed || flagged || len(previous) != len(doc.Previous)
	doc.Previous = previous
	inputs := []portraitInput{}
	for _, input := range doc.Inputs {
		if _, ok := live[input.MemoryID]; ok {
			inputs = append(inputs, input)
		} else {
			stored = true
		}
	}
	doc.Inputs = inputs
	var dropped []portraitDropped
	for _, claim := range doc.Dropped {
		ids := []string{}
		if claim.Sentence != nil {
			ids = claim.Sentence.ids()
		}
		if claim.Line != nil {
			ids = claim.Line.IDs
		}
		if check.known(ids) {
			dropped = append(dropped, claim)
		}
	}
	stored = stored || len(dropped) != len(doc.Dropped)
	doc.Dropped = dropped
	// Rows stay as they were answered until the next write or confirmation
	// rewrite; only a belief that went takes its row with it.
	var candidates []string
	for _, id := range doc.Candidates {
		if _, ok := live[id]; ok {
			candidates = append(candidates, id)
		}
	}
	stored = stored || len(candidates) != len(doc.Candidates)
	doc.Candidates = candidates
	return changed, stored
}

// confirmedIDs are the beliefs the person has confirmed and not since marked
// Not quite, read from the sources as they are now (a changed digest counts).
func confirmedIDs(entries []MemoryEntry) map[string]bool {
	out := map[string]bool{}
	for _, entry := range entries {
		if portraitConfirmed(entry) {
			out[entry.ID] = true
		}
	}
	return out
}

// confirmedFromInputs is confirmedIDs for a set of live inputs, for paths
// that have no sources in hand.
func confirmedFromInputs(live map[string]portraitInput) map[string]bool {
	out := map[string]bool{}
	for id, input := range live {
		if input.Confirmed && !input.NotQuite {
			out[id] = true
		}
	}
	return out
}

// pinnedLine is true while a line rests on a confirmed belief (Y41).
func pinnedLine(line portraitLine, confirmed map[string]bool) bool {
	for _, id := range line.IDs {
		if confirmed[id] {
			return true
		}
	}
	return false
}

// portraitMaxLines is how many How to work with me lines a version holds,
// pinned ones included; a pinned line is never cut to make room.
const portraitMaxLines = 5

// pinLines is the How to work with me list a rewrite publishes: every line
// of the shown version that rests on a confirmed belief, verbatim and first,
// then the written lines that bring beliefs the pinned lines don't cover.
// What the person confirmed never disappears or changes without them.
func pinLines(shown, written []portraitLine, confirmed map[string]bool) []portraitLine {
	var out []portraitLine
	covered := map[string]bool{}
	for _, line := range shown {
		if !pinnedLine(line, confirmed) {
			continue
		}
		line.Pinned = true
		out = append(out, line)
		for _, id := range line.IDs {
			covered[id] = true
		}
	}
	limit := max(portraitMaxLines, len(out))
	for _, line := range written {
		if len(out) >= limit {
			break
		}
		// A written line whose beliefs a pinned line already covers repeats it.
		if len(line.IDs) == 0 || !slices.ContainsFunc(line.IDs, func(id string) bool { return !covered[id] }) {
			continue
		}
		line.Pinned = false
		out = append(out, line)
	}
	return out
}

// portraitCandidates picks the confirm rows (section 6): up to 3 beliefs whose
// confirmation would let the most claims dropped for support pass, ties to the
// most observations. Never a confirmed, Not quite or private belief.
func portraitCandidates(dropped []portraitDropped, live map[string]portraitInput) []string {
	unlocks := map[string]int{}
	for _, claim := range dropped {
		if claim.Reason != portraitDropSupport || claim.Sentence == nil {
			continue
		}
		seen := map[string]bool{}
		for _, id := range claim.Sentence.ids() {
			input, ok := live[id]
			if !ok || seen[id] || input.Confirmed || input.NotQuite || input.Private {
				continue
			}
			seen[id] = true
			trial := make(map[string]portraitInput, len(live))
			for key, value := range live {
				trial[key] = value
			}
			input.Confirmed = true
			trial[id] = input
			if _, ok := (portraitCheck{live: trial}).keepBelieved(*claim.Sentence); ok {
				unlocks[id]++
			}
		}
	}
	ids := make([]string, 0, len(unlocks))
	for id := range unlocks {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		if unlocks[ids[i]] != unlocks[ids[j]] {
			return unlocks[ids[i]] > unlocks[ids[j]]
		}
		if a, b := portraitObservationCount(live[ids[i]]), portraitObservationCount(live[ids[j]]); a != b {
			return a > b
		}
		return ids[i] < ids[j]
	})
	if len(ids) > portraitCandidateLimit {
		ids = ids[:portraitCandidateLimit]
	}
	return ids
}

// restoreConfirmed brings back every claim dropped only for support that a
// confirmation now lets pass, tinted, without another write. It returns how
// many came back.
func (doc *portraitDocument) restoreConfirmed(live map[string]portraitInput) int {
	check := portraitCheck{live: live}
	have := map[string]bool{}
	for _, sentence := range doc.Sentences {
		have[sentence.Role] = true
	}
	restored := 0
	var sentences []portraitSentence
	var left []portraitDropped
	for _, claim := range doc.Dropped {
		switch true {
		case claim.Reason == portraitDropSupport && claim.Sentence != nil && !have[claim.Sentence.Role]:
			if kept, ok := check.keepBelieved(*claim.Sentence); ok {
				kept.Changed = true
				sentences = append(sentences, kept)
				have[kept.Role] = true
				continue
			}
		case claim.Reason == portraitDropSupport && claim.Line != nil && len(doc.WorkWithMe) < 5:
			if portraitBelieved(claim.Line.IDs, live) {
				doc.WorkWithMe = append(doc.WorkWithMe, *claim.Line)
				restored++
				continue
			}
		}
		left = append(left, claim)
	}
	all := append(append([]portraitSentence(nil), doc.Sentences...), sentences...)
	sort.SliceStable(all, func(i, j int) bool {
		return slices.Index(portraitRoles, all[i].Role) < slices.Index(portraitRoles, all[j].Role)
	})
	visible := portraitVisible(all)
	if len(visible) > len(doc.Sentences) {
		restored += len(visible) - len(doc.Sentences)
		doc.Sentences = visible
	} else {
		// An action sentence alone still can't show: keep it dropped.
		for _, sentence := range sentences {
			kept := sentence
			left = append(left, portraitDropped{Role: kept.Role, Reason: portraitDropSupport, Code: "alone", Sentence: &kept})
		}
	}
	doc.Dropped = left
	doc.Candidates = portraitCandidates(doc.Dropped, live)
	return restored
}

// liveInputs keeps the stored inputs whose beliefs are still readable and
// unchanged, with their current confirmation.
func liveInputs(stored []portraitInput, entries []MemoryEntry) map[string]portraitInput {
	current := make(map[string]MemoryEntry, len(entries))
	for _, entry := range entries {
		current[entry.ID] = entry
	}
	live := make(map[string]portraitInput, len(stored))
	for _, input := range stored {
		entry, ok := current[input.MemoryID]
		if !ok || input.Digest == "" || portraitDigest(entry) != input.Digest {
			continue
		}
		input.Confirmed = portraitConfirmed(entry)
		input.NotQuite = portraitNotQuite(entry)
		live[input.MemoryID] = input
	}
	return live
}

// Forget already owns memoryMutationMu and the per-file publication barrier.
// Do not re-enter withSources here. Persist the erasure before suppression or
// deletion: only what rested on the forgotten beliefs goes, at once, and a
// rewrite is queued. Closed Eyes never blocks cleanup.
func (a *App) forgetAboutYouPortrait(ids []string) error {
	return a.forgetAboutYouPortraitVersions(ids, nil)
}

// An independently corrected belief keeps its ID. Remove a portrait input
// only when its digest matches an erased prior version (or is ambiguous).
func (a *App) forgetAboutYouPortraitVersions(ids []string, records []*memoryForgetRecord) error {
	s := a.portraitService()
	path, err := portraitPath()
	if err != nil {
		return err
	}
	s.fileMu.Lock()
	defer s.fileMu.Unlock()
	doc, err := readPortrait(path)
	if err != nil || doc == nil {
		return err
	}
	forgotten := map[string]bool{}
	for _, id := range ids {
		forgotten[id] = true
	}
	forgottenVersions := map[string]map[string]bool{}
	for _, record := range records {
		if !record.preserveLive {
			continue
		}
		if forgottenVersions[record.id] == nil {
			forgottenVersions[record.id] = map[string]bool{}
		}
		for _, content := range record.contents {
			entry := MemoryEntry{Title: pii.DisplayText(extractTitle(content)), Content: content}
			forgottenVersions[record.id][portraitDigest(entry)] = true
		}
	}
	live := map[string]portraitInput{}
	for _, input := range doc.Inputs {
		versions := forgottenVersions[input.MemoryID]
		if !forgotten[input.MemoryID] && !(len(versions) > 0 && (input.Digest == "" || versions[input.Digest])) {
			live[input.MemoryID] = input
		}
	}
	// A waiting version resting on what was forgotten goes; it is rewritten.
	pendingGone := false
	if doc.Pending != nil {
		for _, input := range doc.Pending.Inputs {
			pendingGone = pendingGone || forgotten[input.MemoryID]
		}
	}
	if pendingGone {
		doc.Pending = nil
	}
	if len(live) == len(doc.Inputs) {
		if pendingGone {
			return writePortrait(path, doc)
		}
		return nil
	}
	if shown, _ := doc.keepSupported(live, confirmedFromInputs(live)); shown {
		doc.Removed = true
	}
	if err := writePortrait(path, doc); err != nil {
		return err
	}
	s.poke()
	return nil
}

// readableEntries keeps the owner-only Closed Eyes exception local to this
// stored projection; model inputs always use memorySourceAllowsRead, which
// denies while paused.
func (s *AboutYouPortraitService) readableEntries(direct bool) ([]MemoryEntry, error) {
	portraitSourceReads.Add(1)
	entries, err := s.app.GetMemoryEntries()
	if err != nil {
		return nil, err
	}
	dir, err := memoryDir()
	if err != nil {
		return nil, err
	}
	suppressed := map[string]struct{}{}
	gate := s.app.memorySourceReadGateContext(context.Background())
	if s.app.activity != nil && s.app.activity.store != nil {
		suppressed, err = s.app.activity.store.ListCorrectedAgencyLearningTargetIDs()
		if err != nil {
			return nil, err
		}
	}
	policy, err := s.app.activityReadPrivacyPolicy()
	if err != nil {
		return nil, err
	}
	allowsDependency := func(id string, apps []string) bool {
		if s.app.activity == nil || s.app.activity.store == nil {
			return false
		}
		source, err := s.app.activity.store.activityDependencyProvenance(id, apps)
		return err == nil && policy.allowsStoredDerivative(source)
	}
	out := make([]MemoryEntry, 0, len(entries))
	for _, entry := range entries {
		if _, blocked := suppressed[entry.ID]; blocked {
			continue
		}
		forgotten, err := s.app.memoryForgottenNow(dir, entry.ID, entry.Content, time.Now())
		if err != nil {
			return nil, err
		}
		if forgotten {
			continue
		}
		allowed := false
		if direct && s.app.IsClosedEyesModeEnabled() {
			allowed = s.app.memorySourceAllowsReadWithDependency(entry.Content, allowsDependency)
		} else {
			allowed = gate.allows(entry.Content)
		}
		if allowed {
			out = append(out, entry)
		}
	}
	return out, nil
}

// withSources holds the existing publication barriers while a write is
// checked and published. No model call runs while these locks are owned.
func (s *AboutYouPortraitService) withSources(ctx context.Context, use func([]MemoryEntry) error) error {
	var releases []func()
	release := func() {
		for i := len(releases) - 1; i >= 0; i-- {
			releases[i]()
		}
		releases = nil
	}
	locks := []struct {
		try    func() bool
		unlock func()
	}{
		{s.app.closedEyes.mu.TryLock, s.app.closedEyes.mu.Unlock},
		{memoryMutationMu.TryLock, memoryMutationMu.Unlock},
		{memoryFileLocks.publication.TryLock, memoryFileLocks.publication.Unlock},
		{pii.TryLockSensitiveAppsForPublication, pii.UnlockSensitiveAppsForPublication},
	}
	if s.app.activity != nil && s.app.activity.store != nil {
		store := s.app.activity.store
		locks = append(locks, struct {
			try    func() bool
			unlock func()
		}{store.readPrivacyMu.TryLock, store.readPrivacyMu.Unlock})
	}
	if err := lockTodayChatPublication(ctx, func() bool {
		for _, lock := range locks {
			if !lock.try() {
				release()
				return false
			}
			releases = append(releases, lock.unlock)
		}
		return true
	}); err != nil {
		return err
	}
	defer release()
	if s.app.IsClosedEyesModeEnabled() {
		return errors.New("closed_eyes")
	}
	entries, err := s.readableEntries(false)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return use(entries)
}

// GetAboutYouPortrait reads the saved portrait. It never calls a model and
// never waits for one: only what still rests on readable, unchanged beliefs
// is returned, and anything that fell below the bar is removed for good.
func (a *App) GetAboutYouPortrait() (perfValue AboutYouPortrait, perfErr error) {
	defer logPageBinding("GetAboutYouPortrait", time.Now(), &perfValue)
	return derivedRead(a, derivedPortrait, "", a.portraitService().read, derivedPortraitStamp)
}

func (a *App) portraitService() *AboutYouPortraitService {
	a.mu.Lock()
	if a.aboutYouPortrait == nil {
		a.aboutYouPortrait = NewAboutYouPortraitService(a)
	}
	service := a.aboutYouPortrait
	a.mu.Unlock()
	return service
}

// portraitSourceReads counts readableEntries calls, for tests.
var portraitSourceReads atomic.Uint64

// portraitReadMemoMaxAge bounds how long a read result is reused.
const portraitReadMemoMaxAge = time.Minute

// portraitReadKey stamps what read() reads without reading it: the portrait
// file, memory writes, the memory snapshot, the forget markers, the privacy
// epoch (withdrawals, corrections, Closed Eyes and the other brief-read
// events) and Closed Eyes.
func (s *AboutYouPortraitService) portraitReadKey(path string) string {
	snapshot, err := s.app.memorySnapshot()
	if err != nil {
		return ""
	}
	var size, modified int64
	if info, err := os.Stat(path); err == nil {
		size, modified = info.Size(), info.ModTime().UnixNano()
	} else if !os.IsNotExist(err) {
		return ""
	}
	return fmt.Sprintf("%d|%d|%d|%p|%d|%d|%t|%s", size, modified, s.app.memoryExternalReads.revision.Load(), snapshot,
		memoryTombstoneGeneration.Load(), mobileBriefReadEpoch.Load(), s.app.IsClosedEyesModeEnabled(), s.app.currentUILanguage())
}

func (s *AboutYouPortraitService) read() (AboutYouPortrait, error) {
	path, err := portraitPath()
	if err != nil {
		return emptyPortrait("learning"), errors.New("portrait_storage")
	}
	key := s.portraitReadKey(path)
	if key != "" {
		s.readMemoMu.Lock()
		raw := s.readMemo
		if s.readMemoKey != key || time.Since(s.readMemoAt) >= portraitReadMemoMaxAge {
			raw = nil
		}
		s.readMemoMu.Unlock()
		var cached AboutYouPortrait
		if raw != nil && json.Unmarshal(raw, &cached) == nil {
			return cached, nil
		}
	}
	result, remember, err := s.readNow(path)
	if err == nil && remember && key != "" && key == s.portraitReadKey(path) {
		if raw, err := json.Marshal(result); err == nil {
			s.readMemoMu.Lock()
			s.readMemoKey, s.readMemoAt, s.readMemo = key, time.Now(), raw
			s.readMemoMu.Unlock()
		}
	}
	return result, err
}

// readNow is read without the memo; remember is false when the read wrote
// the portrait or asked for a rewrite.
func (s *AboutYouPortraitService) readNow(path string) (AboutYouPortrait, bool, error) {
	entries, err := s.readableEntries(true)
	if err != nil {
		return emptyPortrait("learning"), false, errors.New("portrait_unavailable")
	}
	s.fileMu.Lock()
	doc, err := readPortrait(path)
	if err != nil {
		// An unreadable file is rewritten; the card shows Writing.
		s.fileMu.Unlock()
		log.Print("[portrait] invalid_storage")
		s.requestWrite(false)
		result := emptyPortrait("writing")
		result.Writing = true
		return result, false, nil
	}
	var live map[string]portraitInput
	remember := true
	if doc != nil {
		live = liveInputs(doc.Inputs, entries)
		shown, stored := doc.keepSupported(live, confirmedIDs(entries))
		if doc.settlePending(entries) {
			stored, live = true, liveInputs(doc.Inputs, entries)
		}
		if stored {
			remember = false
			doc.Removed = doc.Removed || (shown && doc.GeneratedAt > 0)
			if err := writePortrait(path, doc); err != nil {
				s.fileMu.Unlock()
				return emptyPortrait("learning"), false, errors.New("portrait_unavailable")
			}
			s.poke()
		}
	}
	s.fileMu.Unlock()
	if doc != nil && doc.GeneratedAt > 0 && (doc.Language != s.app.currentUILanguage() || !portraitLanguageMatches(s.app.currentUILanguage(), doc.Sentences, doc.WorkWithMe)) {
		s.poke()
		result := s.project(doc, live, entries)
		result.Name, result.NameAreas = []string{}, [][]string{}
		result.Sentences, result.WorkWithMe, result.Lately = []AboutYouPortraitSentence{}, []AboutYouPortraitLine{}, nil
		if result.Failure != nil {
			result.State = "failed"
		} else {
			result.State = "writing"
			result.Writing = true
		}
		return result, false, nil
	}
	return s.project(doc, live, entries), remember, nil
}

func emptyPortrait(state string) AboutYouPortrait {
	return AboutYouPortrait{State: state, Name: []string{}, NameAreas: [][]string{}, Sides: []float64{}, Sentences: []AboutYouPortraitSentence{}, WorkWithMe: []AboutYouPortraitLine{}, Candidates: []AboutYouPortraitCandidate{}}
}

func (s *AboutYouPortraitService) project(doc *portraitDocument, live map[string]portraitInput, entries []MemoryEntry) AboutYouPortrait {
	s.stateMu.Lock()
	running, notQuite, writer, confirming := s.running, s.notQuite, s.writer, !s.confirmAt.IsZero()
	s.stateMu.Unlock()
	result := emptyPortrait("learning")
	result.Writing, result.Rewriting, result.Assistant, result.Confirming = running, notQuite, writer, confirming
	if doc == nil {
		if running {
			result.State = "writing"
		}
		return result
	}
	parts := func(in []portraitPart) []AboutYouPortraitPart {
		out := make([]AboutYouPortraitPart, 0, len(in))
		for _, part := range in {
			confirmed := false
			for _, id := range part.IDs {
				confirmed = confirmed || live[id].Confirmed
			}
			out = append(out, AboutYouPortraitPart{Text: pii.DisplayText(part.Text), MemoryIDs: append([]string{}, part.IDs...), Confirmed: confirmed})
		}
		return out
	}
	for _, sentence := range doc.Sentences {
		out := AboutYouPortraitSentence{Role: sentence.Role, Parts: parts(sentence.Parts), Changed: sentence.Changed}
		if cause := sentence.Cause; cause != nil && sentence.Changed {
			out.Cause = &AboutYouPortraitCause{Confirm: append([]string{}, cause.Confirm...), Note: pii.DisplayText(cause.Note), NotQuite: cause.NotQuite}
		}
		result.Sentences = append(result.Sentences, out)
	}
	for _, line := range doc.WorkWithMe {
		// A pinned line stays "You confirmed this" even when its belief's
		// digest changed and it is not live (Y41).
		out := AboutYouPortraitLine{Text: pii.DisplayText(line.Text), MemoryIDs: append([]string{}, line.IDs...), Confirmed: line.Pinned}
		areas := map[string]float64{}
		sources := map[string]bool{}
		for _, id := range line.IDs {
			out.Confirmed = out.Confirmed || live[id].Confirmed
			areas[memoryAreaOrOther(live[id].Area)]++
			sources[strings.ToLower(live[id].Source)] = true
		}
		if len(sources) == 1 {
			for source := range sources {
				if portraitRealSources[source] {
					out.Source = source
				}
			}
		}
		if top := portraitTopAreas(areas, 1); len(top) > 0 {
			out.Area = top[0]
		}
		result.WorkWithMe = append(result.WorkWithMe, out)
	}
	// How to work with me shows when two or more of its lines pass.
	if len(result.WorkWithMe) < portraitLinesShown {
		result.WorkWithMe = []AboutYouPortraitLine{}
	}
	if doc.Lately != nil {
		result.Lately = &AboutYouPortraitLately{Parts: parts(doc.Lately.Parts), MemoryID: doc.Lately.Fact.MemoryID, ThisMonth: doc.Lately.Fact.ThisMonth, Usual: doc.Lately.Fact.Usual}
	}
	result.Sides = append(result.Sides, doc.Sides...)
	result.Progress = doc.Progress
	result.BeliefCount, result.GeneratedAt, result.Forgotten = doc.BeliefCount, doc.GeneratedAt, doc.Removed
	if doc.said() {
		result.SaidAt = doc.SaidAt
	}
	result.Pending = doc.Pending != nil
	// An unreadable answer shows only from the third in a row; the ones
	// before retry quietly (section 3).
	if attempt := doc.Attempt; attempt != nil && attempt.Outcome == "failed" && !running && (attempt.Reason != portraitReasonUnusable || attempt.Failures >= portraitUnusableShown) {
		result.Failure = &AboutYouPortraitFailure{Reason: attempt.Reason, At: attempt.At, RetryAt: attempt.RetryAt, Assistant: attempt.Assistant}
		if result.Assistant == "" {
			result.Assistant = attempt.Assistant
		}
	}
	titles := map[string]string{}
	for _, entry := range entries {
		titles[entry.ID] = entry.Title
	}
	for _, id := range doc.Candidates {
		input, ok := live[id]
		if !ok || titles[id] == "" {
			continue
		}
		answer := ""
		switch true {
		case input.NotQuite:
			answer = "noted"
		case input.Confirmed:
			answer = "confirmed"
		}
		result.Candidates = append(result.Candidates, AboutYouPortraitCandidate{MemoryID: id, Text: pii.DisplayText(titles[id]), Answer: answer})
	}
	published := doc.GeneratedAt > 0 && len(doc.Poles) == 4
	switch true {
	case len(result.Sentences) > 0, published:
		result.State = "ready"
		if len(result.Sentences) == 0 {
			result.State = "shape"
		}
		first, second := portraitNameWords(doc.Poles)
		result.Name = []string{first, second}
		result.NameAreas = doc.NameAreas
	case doc.Progress == nil || !doc.Progress.Met:
		if running {
			result.State = "writing"
		}
	case result.Failure != nil:
		result.State = "failed"
	default:
		result.State = "writing"
	}
	return result
}

// requestWrite asks for a write now. Try now, Not quite and an unreadable
// file skip the backoff; a running write finishes first.
func (s *AboutYouPortraitService) requestWrite(force bool) {
	s.stateMu.Lock()
	s.force = s.force || force
	s.stateMu.Unlock()
	s.poke()
}

// portraitMemoryChanged wakes the scheduler after a belief changed, so the
// first portrait starts the moment the bar is met and a rewrite after enough
// changes. The scheduler itself decides; this never blocks.
func (a *App) portraitMemoryChanged() {
	a.mu.Lock()
	service := a.aboutYouPortrait
	a.mu.Unlock()
	if service != nil {
		service.poke()
	}
}

func (a *App) emitPortraitUpdated() {
	// The portrait service changed what the portrait shows (a write, a
	// failure, That's me, Not quite): its snapshots build again.
	a.touchDerivedViews(derivedPortrait, derivedPortraitTodayCard, derivedAboutYou)
	if a.ctx != nil {
		a.emitBrowserEvent(portraitEvent)
	}
}

// portraitSources reads what pass 1 needs from each readable belief: its
// words, group, confirmation and the times its permitted sessions happened.
func (s *AboutYouPortraitService) portraitSources(entries []MemoryEntry) []portraitSource {
	sources := make([]portraitSource, 0, len(entries))
	for _, entry := range entries {
		fm := parseMemoryFrontmatter(entry.Content)
		source := portraitSource{ID: entry.ID, Title: entry.Title, Points: parseMemoryPoints(entry.Content), Area: entry.Area, Source: entry.Source, Confirmed: portraitConfirmed(entry), NotQuite: portraitNotQuite(entry), FirstLearnedAt: entry.UpdatedAt, SentMessageCount: fm.SentMessageCount, Digest: portraitDigest(entry)}
		if source.FirstLearnedAt == 0 {
			source.FirstLearnedAt = entry.ModTime
		}
		if s.app.activity != nil && s.app.activity.store != nil {
			for _, id := range memoryEvidenceSessionIDs(entry.Content) {
				session, err := s.app.activity.store.GetSessionByID(id)
				if err != nil || session == nil || !s.app.activitySessionAllowsModelRead(s.app.activity.store, *session) {
					continue
				}
				source.SessionTimes = append(source.SessionTimes, session.StartTime)
				source.Observations = append(source.Observations, portraitObservation{ID: "moment:" + id, Kind: portraitObservedMoment, Day: portraitLocalDay(session.StartTime)})
			}
		}
		source.Observations = append(source.Observations, portraitImportedObservations(entry)...)
		sources = append(sources, source)
	}
	return sources
}

// portraitImportedObservations dates a belief brought from an assistant's
// history by its own sessions, never by the import (section 1). The same
// session behind two beliefs counts once.
func portraitImportedObservations(entry MemoryEntry) []portraitObservation {
	if entry.Source != assistantHistorySource && entry.Source != assistantHistoryClaudeMd {
		return nil
	}
	doc, ok := parseOKFDocument(legacyMemoryWithoutConfirmation(entry.Content))
	if !ok {
		return nil
	}
	kind := portraitObservedSession
	if entry.Source == assistantHistoryClaudeMd {
		kind = portraitObservedRule
	}
	assistant := okfString(doc.Frontmatter, "history_assistant")
	stamps := append(okfStringSlice(doc.Frontmatter, "history_sessions_at"), okfString(doc.Frontmatter, "history_session_at"))
	seen := map[int64]bool{}
	var out []portraitObservation
	for _, stamp := range stamps {
		at := parseOKFTimestampMs(stamp)
		if at == 0 || seen[at] {
			continue
		}
		seen[at] = true
		out = append(out, portraitObservation{ID: fmt.Sprintf("%s:%s:%d", kind, assistant, at), Kind: kind, Day: portraitLocalDay(at)})
	}
	return out
}

// portraitRealSources are the places a How to work with me row can name
// (about-you v1.0.1 Y29); activity, manual and user_edit aren't one.
var portraitRealSources = map[string]bool{"slack": true, "discord": true, "telegram": true, "feishu": true, "email": true}

func portraitInputOf(source portraitSource) portraitInput {
	private := memoryAreaOrOther(source.Area) == "people" || portraitSensitiveTopic(source.text())
	return portraitInput{MemoryID: source.ID, Digest: source.Digest, Area: memoryAreaOrOther(source.Area), Source: source.Source, Confirmed: source.Confirmed, NotQuite: source.NotQuite, Observations: source.observations(), Private: private}
}

// check is one pass of the scheduler: pass 1 always, pass 2 when a write is
// due. Single-flight: a check that finds another running returns at once.
func (s *AboutYouPortraitService) check(ctx context.Context, now time.Time) {
	if !s.attempt.TryLock() {
		return
	}
	defer s.attempt.Unlock()
	if ctx.Err() != nil {
		return
	}
	path, err := portraitPath()
	if err != nil {
		log.Print("[portrait] storage")
		return
	}
	s.stateMu.Lock()
	force, note, noteID, notQuite, confirmAt := s.force, s.note, s.noteID, s.notQuite, s.confirmAt
	s.force = false
	confirmed := !confirmAt.IsZero() && !now.Before(confirmAt)
	var rows []string
	if confirmed {
		rows = s.confirmedRows
		s.confirmAt, s.confirmedRows = time.Time{}, nil
	}
	s.stateMu.Unlock()

	if s.app.IsClosedEyesModeEnabled() {
		// Paused: only privacy maintenance, through the read path.
		_, _ = s.read()
		return
	}
	entries, err := s.readableEntries(false)
	if err != nil {
		log.Print("[portrait] source_read")
		return
	}
	sources := s.portraitSources(entries)

	s.fileMu.Lock()
	doc, err := readPortrait(path)
	if err != nil {
		log.Print("[portrait] invalid_storage, writing a new one")
		doc, force = nil, true
	}
	if doc == nil {
		doc = &portraitDocument{Sentences: []portraitSentence{}, Inputs: []portraitInput{}}
	}
	live := liveInputs(doc.Inputs, entries)
	if shown, _ := doc.keepSupported(live, confirmedIDs(entries)); shown && doc.GeneratedAt > 0 {
		doc.Removed = true
	}
	if doc.settlePending(entries) {
		live = liveInputs(doc.Inputs, entries)
	}
	progress := portraitReadiness(sources)
	sides, sideAreas := portraitSides(sources)
	doc.Progress = &progress
	if doc.GeneratedAt == 0 {
		doc.Sides = sides[:]
	}
	// That's me on a row: what it lets through comes back without a write.
	restored, announce := 0, func() {}
	if confirmed {
		before := append([]portraitSentence(nil), doc.Sentences...)
		restored = doc.restoreConfirmed(live)
		if restored > 0 {
			portraitCauseConfirmed(doc.Sentences, before, rows, live)
			if len(before) > 0 {
				doc.Previous = before
			}
			if len(before) == 0 && len(doc.Sentences) > 0 {
				announce = s.announce(doc, nil)
			}
			doc.GeneratedAt, doc.Removed = now.UnixMilli(), false
			log.Printf("[portrait] %d claims back after a confirmation", restored)
		}
	}
	language := s.app.currentUILanguage()
	published := doc.GeneratedAt > 0
	since := max(doc.GeneratedAt, doc.CheckedAt)
	due := ""
	switch true {
	case !published && !progress.Met:
	case notQuite:
		// Before Try now: the note sets force too, and a Not quite always
		// replaces the shown version.
		due = "not_quite"
	case force:
		due = "asked"
	case confirmed && restored == 0:
		due = "confirmed"
	case !published:
		due = "first"
	case doc.Removed:
		due = "forgotten"
	case doc.Language != language || !portraitLanguageMatches(language, doc.Sentences, doc.WorkWithMe):
		due = "language"
	case portraitChanges(doc.Inputs, sources, since) >= portraitRewriteChanges:
		due = "changes"
	case now.UnixMilli()-since >= portraitCadence.Milliseconds():
		due = "weekly"
	}
	if due != "" && !force && !notQuite && !confirmed && doc.Attempt != nil && doc.Attempt.Outcome == "failed" {
		switch doc.Attempt.Reason {
		case portraitReasonNoAI:
			// No timed retry: it writes the moment an AI is signed in.
			if s.firstAssistant() == "" {
				due = ""
			}
		default:
			if now.UnixMilli() < doc.Attempt.RetryAt {
				due = ""
			}
		}
	}
	if due != "" && !portraitWithinCap(doc, now, due) {
		log.Printf("[portrait] %s waits for the cap", due)
		due = ""
	}
	if due != "" {
		doc.Writes = append(doc.Writes, now.UnixMilli())
	}
	err = writePortrait(path, doc)
	s.fileMu.Unlock()
	if err != nil {
		log.Print("[portrait] storage")
		return
	}
	if restored > 0 {
		announce()
		s.app.emitPortraitUpdated()
	}
	if due == "" {
		s.app.maybePostFirstReads(ctx)
		return
	}
	s.write(ctx, path, now, due, sources, sides, sideAreas, language, note, noteID, rows)
	s.app.maybePostFirstReads(ctx)
}

// portraitCauseConfirmed says why each sentence a confirmation brought back
// is back: the confirm rows it rests on, else its confirmed beliefs.
func portraitCauseConfirmed(sentences, before []portraitSentence, rows []string, live map[string]portraitInput) {
	shown := map[string]bool{}
	for _, sentence := range before {
		shown[sentence.text()] = true
	}
	for i := range sentences {
		sentence := &sentences[i]
		if !sentence.Changed || shown[sentence.text()] {
			continue
		}
		var ids, confirmed []string
		for _, id := range sentence.ids() {
			switch true {
			case slices.Contains(rows, id):
				ids = append(ids, id)
			case live[id].Confirmed:
				confirmed = append(confirmed, id)
			}
		}
		if len(ids) == 0 {
			ids = confirmed
		}
		if len(ids) > 0 {
			sentence.Cause = &portraitCause{Confirm: ids}
		}
	}
}

// portraitAutomatic is a write the person didn't ask for: the cadence, a
// forgotten belief, a new language or the first write.
func portraitAutomatic(due string) bool {
	switch due {
	case "asked", "not_quite", "confirmed":
		return false
	default:
		return true
	}
}

// portraitWithinCap protects the person's plan (section 4): at most 6 writes
// a local day, never two within 10 minutes except Try now or the person's own
// answer on the card. Old write times are pruned.
func portraitWithinCap(doc *portraitDocument, now time.Time, due string) bool {
	day := portraitLocalDay(now.UnixMilli())
	today, last := 0, int64(0)
	var kept []int64
	for _, at := range doc.Writes {
		if now.UnixMilli()-at > 2*24*time.Hour.Milliseconds() {
			continue
		}
		kept = append(kept, at)
		if portraitLocalDay(at) == day {
			today++
		}
		last = max(last, at)
	}
	doc.Writes = kept
	if today >= portraitWritesPerDay {
		return false
	}
	switch due {
	case "asked", "not_quite", "confirmed":
		return true
	default:
		return now.UnixMilli()-last >= portraitWriteGap.Milliseconds()
	}
}

// write runs pass 2 and publishes what survived, or records why the AI
// couldn't be used.
func (s *AboutYouPortraitService) write(ctx context.Context, path string, now time.Time, due string, sources []portraitSource, sides [4]float64, sideAreas [4][]string, language, note, noteID string, rows []string) {
	selected := append([]portraitSource(nil), sources...)
	sort.SliceStable(selected, func(i, j int) bool {
		if selected[i].Confirmed != selected[j].Confirmed {
			return selected[i].Confirmed
		}
		if selected[i].weight() != selected[j].weight() {
			return selected[i].weight() > selected[j].weight()
		}
		return len(selected[i].observations()) > len(selected[j].observations())
	})
	// A belief marked Not quite never supports a claim, so the writer never
	// sees it.
	kept := selected[:0]
	for _, source := range selected {
		if !source.NotQuite {
			kept = append(kept, source)
		}
	}
	selected = kept
	if len(selected) > portraitInputLimit {
		selected = selected[:portraitInputLimit]
	}
	// The Not quite note is always in, even past the cap.
	for _, source := range sources {
		if noteID != "" && source.ID == noteID && !containsPortraitSource(selected, noteID) {
			if len(selected) >= portraitInputLimit {
				selected = selected[:portraitInputLimit-1]
			}
			selected = append(selected, source)
		}
	}
	check := portraitCheck{live: map[string]portraitInput{}, text: map[string]string{}, areas: map[string]string{}, facts: map[string]bool{}, behaviors: portraitBehaviorsOn()}
	if s.names != nil {
		check.names = s.names()
	}
	var beliefs []portraitWriterBelief
	var inputs []portraitInput
	for _, source := range selected {
		title, _ := pii.RedactText(source.Title)
		points := append([]string(nil), source.Points...)
		for i := range points {
			points[i], _ = pii.RedactText(points[i])
		}
		status := "inferred"
		if source.Confirmed {
			status = "confirmed"
		}
		days := source.days()
		beliefs = append(beliefs, portraitWriterBelief{ID: source.ID, Group: memoryAreaOrOther(source.Area), Status: status, Seen: fmt.Sprintf("%d times, on %s", len(source.observations()), strings.Join(days, ", ")), Title: title, Points: points})
		input := portraitInputOf(source)
		inputs = append(inputs, input)
		check.live[source.ID] = input
		check.text[source.ID] = title + " " + strings.Join(points, " ")
		check.areas[source.ID] = input.Area
	}
	facts := portraitFacts(selected)
	for _, fact := range facts {
		check.facts[fact.ID] = true
	}
	check.lately = portraitLately(selected, now)

	s.fileMu.Lock()
	previous, _ := readPortrait(path)
	s.fileMu.Unlock()
	var prevPoles []int
	unchanged := map[int]bool{}
	if previous != nil {
		prevPoles = previous.Poles
	}
	if previous != nil && previous.Language == language && portraitLanguageMatches(language, previous.Sentences, previous.WorkWithMe) {
		check.previous = previous.Sentences
		for i, sentence := range previous.Sentences {
			same := true
			for _, id := range sentence.ids() {
				old, current := portraitInput{}, check.live[id]
				for _, input := range previous.Inputs {
					if input.MemoryID == id {
						old = input
					}
				}
				same = same && old.Digest != "" && old.Digest == current.Digest && old.Confirmed == current.Confirmed && !current.NotQuite
			}
			unchanged[i] = same
		}
	}
	var ids []string
	for _, source := range selected {
		ids = append(ids, source.ID)
	}
	aliases := newPortraitAliases(ids)
	prompt := portraitPrompt(aliases.promptInput(portraitPromptInput{Language: language, Beliefs: beliefs, Facts: facts, Lately: check.lately, Previous: check.previous, Unchanged: unchanged, Note: note, NoteID: noteID, Behaviors: check.behaviors}))

	writer := s.firstAssistant()
	s.setRunning(true, writer)
	s.app.emitPortraitUpdated()
	log.Printf("[portrait] writing (%s, %d beliefs, %s)", due, len(selected), portraitAssistantLabel(writer))
	defer s.app.emitPortraitUpdated()
	var best *portraitChecked
	// One retry (PR #365): when the answer can't be read, nothing in it
	// passed, or it left out a sentence the beliefs could carry, the writer
	// gets the reasons and writes it again. An empty answer gets its own
	// immediate retry first. The better answer is kept.
	possible := portraitPossibleRoles(selected, facts)
	retried, emptyRetried := false, false
	for calls := 0; calls < 3; calls++ {
		output, err := s.app.runEphemeralClaudeModelObserved(ctx, agent.BackendHintEphemeral, prompt, s.app.chatTaskModel(), func(event *agent.StreamEvent) {
			if event != nil && event.Type == "backend_route" && event.Backend != "" {
				writer = event.Backend
			}
		}, portraitWriteTimeout)
		if err != nil {
			if best != nil {
				break
			}
			s.setRunning(false, writer)
			if ctx.Err() != nil {
				return
			}
			reason, retryAt := s.classify(err, writer, now, previous)
			log.Printf("[portrait] %s: %v", reason, err)
			s.recordFailure(path, now, reason, retryAt, writer)
			return
		}
		draft, err := parsePortraitDraft(output)
		if err == nil && len(draft.Sentences) == 0 && len(draft.WorkWithMe) == 0 {
			// An answer with nothing in it is unreadable: never the person's
			// words in the log, only its shape.
			log.Printf("[portrait] empty answer (%s)", portraitAnswerShape(output))
			if !emptyRetried {
				emptyRetried = true
				prompt += "\nYour previous answer had no sentences and no lines. Write the answer now, in the JSON shape above: at least the \"connect\" sentence, citing several beliefs from different days for each claim.\n"
				continue
			}
			break
		}
		if err != nil {
			log.Printf("[portrait] invalid_json (%s)", portraitAnswerShape(output))
			if retried {
				break
			}
			retried = true
			prompt += "\nYour previous answer was not strict JSON in the shape above. Return ONLY that JSON.\n"
			continue
		}
		if !portraitLanguageMatches(language, draft.Sentences, draft.WorkWithMe) {
			if retried {
				break
			}
			retried = true
			prompt += "\nWrite every sentence and every How to work with me line in Simplified Chinese.\n"
			continue
		}
		checked := check.checkDraft(aliases.draft(draft))
		log.Printf("[portrait] %d of %d sentences kept, %d of %d lines kept, lately %t", len(checked.Sentences), len(draft.Sentences), len(checked.Lines), len(draft.WorkWithMe), checked.Lately != nil)
		if best == nil || len(checked.Sentences) > len(best.Sentences) {
			best = &checked
		}
		missing := portraitMissingRoles(best.Sentences, possible)
		if retried || (len(best.Sentences) > 0 && len(missing) == 0) {
			break
		}
		retried = true
		prompt += check.retryNote(checked, missing, output)
	}
	s.setRunning(false, writer)
	if best == nil {
		s.recordFailure(path, now, portraitReasonUnusable, s.backoff(now, previous, portraitReasonUnusable), writer)
		return
	}
	nameFirst, nameSecond := portraitNameAreas(sideAreas)
	poles := portraitPoles(sides, prevPoles)
	err := s.withSources(ctx, func(entries []MemoryEntry) error {
		// A belief that changed or went during the write takes only what
		// rested on it; the rest is published, one sentence or none.
		live := liveInputs(inputs, entries)
		doc := &portraitDocument{GeneratedAt: now.UnixMilli(), Language: language, Sides: sides[:], Poles: poles, NameAreas: [][]string{nameFirst, nameSecond}, Sentences: best.Sentences, Lately: best.Lately, WorkWithMe: best.Lines, Inputs: inputs, BeliefCount: len(sources), Dropped: best.Dropped, Attempt: &portraitAttempt{Outcome: "written", At: now.UnixMilli(), Assistant: writer}}
		if doc.Sentences == nil {
			doc.Sentences = []portraitSentence{}
		}
		confirmed := confirmedIDs(entries)
		doc.keepSupported(live, confirmed)
		doc.Candidates = portraitCandidates(doc.Dropped, live)
		// The person's own answer says why the words changed, once.
		for i := range doc.Sentences {
			if !doc.Sentences[i].Changed {
				continue
			}
			switch due {
			case "not_quite":
				doc.Sentences[i].Cause = &portraitCause{Note: note, NotQuite: true}
			case "confirmed":
				doc.Sentences[i].Cause = &portraitCause{Confirm: rows}
			}
		}
		s.fileMu.Lock()
		defer s.fileMu.Unlock()
		old, _ := readPortrait(path)
		if old != nil {
			// Y41: the confirmed lines of the shown version are carried over
			// verbatim, first. A change of language rewrites the text, so the
			// old lines are not carried into it.
			if old.Language == language {
				doc.WorkWithMe = pinLines(old.WorkWithMe, doc.WorkWithMe, confirmed)
			}
			doc.Progress, doc.Announced, doc.TodaySeen, doc.Writes = old.Progress, old.Announced, old.TodaySeen, old.Writes
			if len(old.Sentences) > 0 {
				doc.Previous = old.Sentences
			}
			// A rewrite never makes it worse on its own: with fewer
			// sentences passing and nothing under the shown ones changed,
			// the card keeps what it shows.
			if due != "not_quite" && portraitKeepShown(old, doc, unchanged) {
				log.Printf("[portrait] kept %d shown sentences over %d", len(old.Sentences), len(doc.Sentences))
				old.Attempt, old.CheckedAt, old.Removed = doc.Attempt, now.UnixMilli(), false
				return writePortrait(path, old)
			}
			// That's me keeps the version: an automatic write waits behind
			// "Show it" on every open (answer feedback section 11.5).
			if old.said() && portraitAutomatic(due) {
				log.Print("[portrait] the version you said is you stays; the new one waits")
				old.Pending, old.Attempt, old.CheckedAt, old.Removed = doc, doc.Attempt, now.UnixMilli(), false
				return writePortrait(path, old)
			}
		}
		announce := func() {}
		if len(doc.Sentences) > 0 {
			announce = s.announce(doc, old)
		}
		if err := writePortrait(path, doc); err != nil {
			return err
		}
		announce()
		return nil
	})
	if err != nil {
		log.Printf("[portrait] publication: %v", err)
		s.recordFailure(path, now, portraitReasonUnusable, s.backoff(now, previous, portraitReasonUnusable), writer)
		return
	}
	s.stateMu.Lock()
	if s.noteID == noteID {
		s.note, s.noteID, s.notQuite = "", "", false
	}
	s.stateMu.Unlock()
	log.Printf("[portrait] written (%s)", portraitAssistantLabel(writer))
}

// portraitKeepShown is the no-worse rule (section 2). The person's own words
// (a Not quite note) or a new language always replace it.
func portraitKeepShown(old, next *portraitDocument, unchanged map[int]bool) bool {
	if len(old.Sentences) == 0 || len(next.Sentences) >= len(old.Sentences) || old.Language != next.Language || old.Removed {
		return false
	}
	for i := range old.Sentences {
		if !unchanged[i] {
			return false
		}
	}
	return true
}

// portraitAnswerShape describes an answer for the log without its words:
// its length, and whether its first 200 characters look like the JSON asked
// for.
func portraitAnswerShape(output string) string {
	head := strings.TrimSpace(output)
	if len(head) > 200 {
		head = head[:200]
	}
	return fmt.Sprintf("%d chars, starts with { %t, has sentences %t", len(output), strings.HasPrefix(head, "{"), strings.Contains(head, `"sentences"`))
}

// portraitPossibleRoles are the sentences the beliefs could carry: connect
// and action always, insight when a fact exists, life when the interests and
// routines groups have three or more beliefs.
func portraitPossibleRoles(sources []portraitSource, facts []portraitFact) []string {
	life := 0
	for _, source := range sources {
		if area := memoryAreaOrOther(source.Area); area == "interests" || area == "routines" {
			life++
		}
	}
	var roles []string
	for _, role := range portraitRoles {
		switch true {
		case role == "insight" && len(facts) == 0, role == "life" && life < 3:
			continue
		}
		roles = append(roles, role)
	}
	return roles
}

// portraitMissingRoles are the possible insight and life sentences that did
// not pass; the retry asks for them.
func portraitMissingRoles(kept []portraitSentence, possible []string) []string {
	have := map[string]bool{}
	for _, sentence := range kept {
		have[sentence.Role] = true
	}
	var missing []string
	for _, role := range possible {
		if (role == "insight" || role == "life") && !have[role] {
			missing = append(missing, role)
		}
	}
	return missing
}

func containsPortraitSource(sources []portraitSource, id string) bool {
	for _, source := range sources {
		if source.ID == id {
			return true
		}
	}
	return false
}

func (s *AboutYouPortraitService) setRunning(running bool, writer string) {
	s.stateMu.Lock()
	s.running, s.writer = running, writer
	s.stateMu.Unlock()
}

// firstAssistant is who answers first: the user's assistant in their order,
// or Isoform's own runner when none is signed in.
func (s *AboutYouPortraitService) firstAssistant() string {
	s.app.mu.Lock()
	runner := s.app.runner
	s.app.mu.Unlock()
	if router, ok := runner.(*agent.RoutingAgentRunner); ok && router != nil {
		if format, ok := router.ExternalRunner(); ok {
			return format
		}
	}
	return ""
}

func portraitAssistantLabel(format string) string {
	switch format {
	case string(agent.BackendClaudeCode):
		return "Claude Code"
	case string(agent.BackendCodex):
		return "Codex"
	default:
		return "Isoform"
	}
}

// classify names why the AI couldn't be used and when to try again by
// itself: at the limit's reset, never by a timer when no AI is signed in, and
// 15 minutes, an hour, then every 6 hours when it didn't answer.
func (s *AboutYouPortraitService) classify(err error, writer string, now time.Time, previous *portraitDocument) (string, time.Time) {
	var limit *agent.AssistantUsageLimitError
	message := strings.ToLower(err.Error())
	quota := errors.As(err, &limit)
	for _, marker := range []string{"rate_limit_error", "rate_limit_exceeded", "insufficient_quota", "usage limit", "http 429", "status code: 429"} {
		quota = quota || strings.Contains(message, marker)
	}
	s.app.mu.Lock()
	runner := s.app.runner
	s.app.mu.Unlock()
	router, _ := runner.(*agent.RoutingAgentRunner)
	if quota {
		retry := now.Add(30 * time.Minute)
		if router != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			for _, health := range router.AssistantHealth(ctx) {
				if health.Code == agent.HealthUsageLimit && (writer == "" || health.Format == writer) && health.RetryAt.After(now) {
					retry = health.RetryAt
				}
			}
			cancel()
		}
		return portraitReasonLimit, retry
	}
	if router != nil {
		if _, ok := router.ExternalRunner(); !ok {
			return portraitReasonNoAI, time.Time{}
		}
	}
	return portraitReasonNoAnswer, s.backoff(now, previous, portraitReasonNoAnswer)
}

// backoff is 15 minutes, then an hour, then every 6 hours for failures of the
// same cause in a row.
func (s *AboutYouPortraitService) backoff(now time.Time, previous *portraitDocument, reason string) time.Time {
	failures := 0
	if previous != nil && previous.Attempt != nil && previous.Attempt.Outcome == "failed" && previous.Attempt.Reason == reason {
		failures = previous.Attempt.Failures
	}
	return now.Add(portraitBackoff[min(failures, len(portraitBackoff)-1)])
}

// recordFailure keeps the saved portrait and stores the attempt's outcome so
// the card can say why, and when. A zero retry time means no timed retry.
func (s *AboutYouPortraitService) recordFailure(path string, now time.Time, reason string, retryAt time.Time, writer string) {
	s.fileMu.Lock()
	defer s.fileMu.Unlock()
	doc, err := readPortrait(path)
	if err != nil || doc == nil {
		doc = &portraitDocument{Sentences: []portraitSentence{}, Inputs: []portraitInput{}}
	}
	failures := 1
	if doc.Attempt != nil && doc.Attempt.Outcome == "failed" && doc.Attempt.Reason == reason {
		failures = doc.Attempt.Failures + 1
	}
	retry := int64(0)
	if !retryAt.IsZero() {
		retry = retryAt.UnixMilli()
	}
	doc.Attempt = &portraitAttempt{Outcome: "failed", Reason: reason, At: now.UnixMilli(), RetryAt: retry, Assistant: writer, Failures: failures}
	if err := writePortrait(path, doc); err != nil {
		log.Print("[portrait] storage")
	}
	s.stateMu.Lock()
	s.notQuite = false
	s.stateMu.Unlock()
}

// announce sends the one notification the first time a portrait is ready.
// It runs after the file is published.
func (s *AboutYouPortraitService) announce(doc, old *portraitDocument) func() {
	first, second := portraitNameWords(doc.Poles)
	name := first + "-" + second
	if doc.Announced != "" {
		doc.Announced = name
		return func() {}
	}
	doc.Announced = name
	language := doc.Language
	return func() {
		if s.app.ctx == nil {
			return
		}
		title, body := portraitNotification(language, first, second)
		s.app.NotifyUser(title, body, "memory:")
	}
}

var portraitNameWordText = map[string][2]string{
	"blueprint": {"Blueprint", "蓝图型"}, "steady": {"Steady", "稳健型"}, "curious": {"Curious", "好奇型"}, "practical": {"Practical", "务实型"},
	"builder": {"Builder", "建造者"}, "author": {"Author", "写作者"}, "conductor": {"Conductor", "指挥者"}, "connector": {"Connector", "连接者"},
}

func portraitNotification(language, first, second string) (string, string) {
	if language == "zh-CN" {
		return "你的画像写好了", portraitNameWordText[first][1] + portraitNameWordText[second][1] + "。看看 Isoform 怎么看你。"
	}
	return "Your portrait is ready", "The " + portraitNameWordText[first][0] + " " + portraitNameWordText[second][0] + ". See how Isoform reads you."
}

// RetryAboutYouPortrait is Try now: it writes at once, past any backoff.
func (a *App) RetryAboutYouPortrait() error {
	a.portraitService().requestWrite(true)
	return nil
}

// ConfirmAboutYouPortrait is That's me: it keeps this version, so anything
// newer waits for the person, and confirms every belief under it. It
// returns the beliefs it confirmed, for Undo.
func (a *App) ConfirmAboutYouPortrait() ([]string, error) {
	portrait, err := a.GetAboutYouPortrait()
	if err != nil {
		return nil, err
	}
	// Kept first, so a write that the confirmations wake already waits.
	if err := a.portraitService().setSaid(portrait.GeneratedAt, true); err != nil {
		return nil, err
	}
	defer a.emitPortraitUpdated()
	seen := map[string]bool{}
	confirmed := []string{}
	for _, sentence := range portrait.Sentences {
		for _, part := range sentence.Parts {
			for _, id := range part.MemoryIDs {
				if seen[id] {
					continue
				}
				seen[id] = true
				entry, err := a.GetMemoryEntry(id)
				if err != nil || portraitConfirmed(entry) {
					continue
				}
				if _, err := a.ConfirmMemoryEntry(id); err != nil {
					return confirmed, err
				}
				confirmed = append(confirmed, id)
			}
		}
	}
	return confirmed, nil
}

// AnswerAboutYouPortraitBelief is That's me or Not quite on a confirm row
// (section 6). That's me confirms the belief, and the claims it lets through
// come back within a minute, after 30 seconds without another answer. Not
// quite marks the belief, which then supports nothing; the full Not quite,
// with its note, stays in the belief's detail.
func (a *App) AnswerAboutYouPortraitBelief(id string, thatsMe bool) error {
	if thatsMe {
		if _, err := a.ConfirmMemoryEntry(id); err != nil {
			return err
		}
	} else if err := a.markMemoryNotQuite(id); err != nil {
		return err
	}
	s := a.portraitService()
	if thatsMe {
		s.stateMu.Lock()
		s.confirmAt = s.now().Add(portraitConfirmWait)
		if !slices.Contains(s.confirmedRows, id) {
			s.confirmedRows = append(s.confirmedRows, id)
		}
		s.stateMu.Unlock()
	}
	s.poke()
	a.emitPortraitUpdated()
	return nil
}

// UndoAboutYouPortraitBelief is Undo on a confirm row: That's me is taken
// back, or Not quite with the confirmation the belief had (rows are never
// confirmed beliefs). With no confirmation left waiting, nothing is
// rewritten for it.
func (a *App) UndoAboutYouPortraitBelief(id string, thatsMe bool) error {
	if !thatsMe {
		if err := a.UndoNotQuiteMemoryEntry(id, 0); err != nil {
			return err
		}
		a.emitPortraitUpdated()
		return nil
	}
	if err := a.unconfirmMemoryEntry(id); err != nil {
		return err
	}
	s := a.portraitService()
	s.stateMu.Lock()
	s.confirmedRows = slices.DeleteFunc(s.confirmedRows, func(row string) bool { return row == id })
	if len(s.confirmedRows) == 0 {
		s.confirmAt = time.Time{}
	}
	s.stateMu.Unlock()
	s.poke()
	a.emitPortraitUpdated()
	return nil
}

// UndoConfirmAboutYouPortrait takes back the confirmations That's me added
// and stops keeping the version.
func (a *App) UndoConfirmAboutYouPortrait(ids []string) error {
	defer a.emitPortraitUpdated()
	for _, id := range ids {
		if err := a.unconfirmMemoryEntry(id); err != nil {
			return err
		}
	}
	return a.portraitService().setSaid(0, false)
}

// ShowAboutYouPortraitPending is "Show it": the waiting version becomes
// current, with its own That's me and Not quite.
func (a *App) ShowAboutYouPortraitPending() error {
	s := a.portraitService()
	path, err := portraitPath()
	if err != nil {
		return err
	}
	entries, err := s.readableEntries(false)
	if err != nil {
		return err
	}
	s.fileMu.Lock()
	doc, err := readPortrait(path)
	if err != nil || doc == nil || doc.Pending == nil {
		s.fileMu.Unlock()
		return err
	}
	doc.showPending(confirmedIDs(entries))
	announce := s.announce(doc, nil)
	err = writePortrait(path, doc)
	s.fileMu.Unlock()
	if err != nil {
		return err
	}
	announce()
	a.emitPortraitUpdated()
	return nil
}

// setSaid records That's me on the version written at version, or clears it.
func (s *AboutYouPortraitService) setSaid(version int64, said bool) error {
	path, err := portraitPath()
	if err != nil {
		return err
	}
	s.fileMu.Lock()
	defer s.fileMu.Unlock()
	doc, err := readPortrait(path)
	if err != nil || doc == nil {
		return err
	}
	switch true {
	case said && version == doc.GeneratedAt:
		doc.SaidAt, doc.SaidVersion = s.now().UnixMilli(), version
	case !said && doc.SaidAt != 0:
		doc.SaidAt, doc.SaidVersion = 0, 0
	default:
		return nil
	}
	return writePortrait(path, doc)
}

// candidateIDs are the confirm rows, for About you's line on That's me.
func (s *AboutYouPortraitService) candidateIDs() map[string]bool {
	ids := map[string]bool{}
	path, err := portraitPath()
	if err != nil {
		return ids
	}
	s.fileMu.Lock()
	doc, _ := readPortrait(path)
	s.fileMu.Unlock()
	if doc != nil {
		for _, id := range doc.Candidates {
			ids[id] = true
		}
	}
	return ids
}

// NotQuiteAboutYouPortrait saves the note as a confirmed belief in the
// person's own words and starts the rewrite at once. The current version
// stays until the rewrite lands.
func (a *App) NotQuiteAboutYouPortrait(note string) error {
	note = strings.TrimSpace(note)
	noteID := "not-quite"
	if note != "" {
		if len([]rune(note)) > 500 {
			note = string([]rune(note)[:500])
		}
		line := strings.ReplaceAll(note, "\n", " ")
		saved, err := a.SaveMemoryEntry("", "# "+line+"\n\n## Memory Points\n\n- "+line+"\n")
		if err != nil {
			return err
		}
		noteID = saved.ID
	}
	return a.queuePortraitCorrection(note, noteID)
}

func (a *App) queuePortraitCorrection(note, noteID string) error {
	s := a.portraitService()
	// Not quite takes back That's me on this version.
	if err := s.setSaid(0, false); err != nil {
		return err
	}
	s.stateMu.Lock()
	s.note, s.noteID, s.notQuite, s.force = note, noteID, true, true
	s.stateMu.Unlock()
	s.poke()
	a.emitPortraitUpdated()
	return nil
}

// PortraitTodayCard is the Today card: once when the first portrait is
// ready, and again only when its name changes (handoff section 10).
type PortraitTodayCard struct {
	Renamed  bool                   `json:"renamed"`
	Name     []string               `json:"name"`
	Sides    []float64              `json:"sides"`
	Sentence []AboutYouPortraitPart `json:"sentence"`
}

// GetPortraitTodayCard returns the card, or nil when Today has nothing to
// show. It reads the saved portrait through the same gates as About you.
func (a *App) GetPortraitTodayCard() (perfValue *PortraitTodayCard, perfErr error) {
	defer logPageBinding("GetPortraitTodayCard", time.Now(), &perfValue)
	return derivedRead(a, derivedPortraitTodayCard, "", a.portraitTodayCardNow, derivedPortraitStamp)
}

func (a *App) portraitTodayCardNow() (*PortraitTodayCard, error) {
	portrait, err := a.GetAboutYouPortrait()
	if err != nil || portrait.State != "ready" || len(portrait.Sentences) == 0 {
		return nil, nil
	}
	path, err := portraitPath()
	if err != nil {
		return nil, nil
	}
	doc, err := readPortrait(path)
	if err != nil || doc == nil {
		return nil, nil
	}
	name := strings.Join(portrait.Name, "-")
	if doc.TodaySeen == name {
		return nil, nil
	}
	return &PortraitTodayCard{Renamed: doc.TodaySeen != "", Name: portrait.Name, Sides: portrait.Sides, Sentence: portrait.Sentences[0].Parts}, nil
}

// DismissPortraitTodayCard is Read it and Not now: the card stays away until
// the name changes.
func (a *App) DismissPortraitTodayCard() error {
	s := a.portraitService()
	defer a.touchDerivedViews(derivedPortrait, derivedPortraitTodayCard)
	path, err := portraitPath()
	if err != nil {
		return err
	}
	s.fileMu.Lock()
	defer s.fileMu.Unlock()
	doc, err := readPortrait(path)
	if err != nil || doc == nil || len(doc.Sentences) == 0 {
		return err
	}
	first, second := portraitNameWords(doc.Poles)
	doc.TodaySeen = first + "-" + second
	return writePortrait(path, doc)
}

// MarkAboutYouPortraitSeen clears the tint on words that changed, once the
// person has seen them.
func (a *App) MarkAboutYouPortraitSeen() error {
	s := a.portraitService()
	defer a.touchDerivedViews(derivedPortrait, derivedPortraitTodayCard)
	path, err := portraitPath()
	if err != nil {
		return err
	}
	s.fileMu.Lock()
	defer s.fileMu.Unlock()
	doc, err := readPortrait(path)
	if err != nil || doc == nil {
		return err
	}
	changed := false
	for i := range doc.Sentences {
		changed = changed || doc.Sentences[i].Changed
		doc.Sentences[i].Changed, doc.Sentences[i].Cause = false, nil
	}
	if !changed {
		return nil
	}
	return writePortrait(path, doc)
}
