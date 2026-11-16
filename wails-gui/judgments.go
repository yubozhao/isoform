package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// Judgments (docs/rework-2026-11.md, Bo Nov 14): Isoform gathers signal,
// turns it into work people judge, and the judgments are the most valuable
// data. These are the daily measures: proposals made, answered and accepted,
// by kind (topic work, Task offers, chat cards), and brief items acted on.
// Counts only, kept on this computer. Proposal IDs are kept as short hashes,
// only to count each proposal once a day.
//
// Today's counts are in judgments/<day>.json under the data folder. When a day
// ends (the first measure of a later day, or launch), its one-line summary is
// appended to judgments/summary.log and logged as a [judgments] line, for the
// morning report.

const judgmentsDirName = "judgments"

// Kinds of proposal.
const (
	judgmentTopic = "topic" // a topic's proposed work
	judgmentOffer = "offer" // a Task offer (Up next, ideas)
	judgmentCard  = "card"  // a proactive card in the main chat
)

type judgmentCounts struct {
	Made     int `json:"made"`
	Answered int `json:"answered"`
	Accepted int `json:"accepted"`
}

type judgmentDay struct {
	Day        string                     `json:"day"`
	Kinds      map[string]*judgmentCounts `json:"kinds"`
	BriefActed int                        `json:"briefActed"`
	// Seen are hashes of the proposals counted today, per measure.
	Seen map[string]bool `json:"seen"`
}

var (
	judgmentsMu  sync.Mutex
	judgmentsDay *judgmentDay
	// judgmentsNow is the measures' clock; tests move it.
	judgmentsNow = time.Now
	// judgmentsDirHook, when set, replaces the data folder; tests.
	judgmentsDirHook func() (string, error)
)

func judgmentsDir() (string, error) {
	if judgmentsDirHook != nil {
		return judgmentsDirHook()
	}
	if testing.Testing() {
		// Tests that run a hooked path never count into the person's own
		// measures, whatever HOME they set.
		return filepath.Join(os.TempDir(), fmt.Sprintf("isoform-judgments-test-%d", os.Getpid())), nil
	}
	root, err := flavorDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, judgmentsDirName), nil
}

func judgmentHash(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:8])
}

// noteProposalMade counts a proposal shown for the first time today.
func noteProposalMade(kind, id string) {
	if id == "" {
		return
	}
	updateJudgments(func(d *judgmentDay) {
		key := judgmentHash("made", kind, id)
		if d.Seen[key] {
			return
		}
		d.Seen[key] = true
		d.counts(kind).Made++
	})
}

// noteProposalAnswered counts the person's answer to a proposal, once a day;
// accepted is a yes (the work runs, the card's action is taken).
func noteProposalAnswered(kind, id string, accepted bool) {
	if id == "" {
		return
	}
	updateJudgments(func(d *judgmentDay) {
		key := judgmentHash("answered", kind, id)
		if d.Seen[key] {
			return
		}
		d.Seen[key] = true
		c := d.counts(kind)
		c.Answered++
		if accepted {
			c.Accepted++
		}
	})
}

// noteBriefActed counts the person acting on a brief item: feedback on it,
// a reply sent from it, an answer to the week's question.
func noteBriefActed() {
	updateJudgments(func(d *judgmentDay) { d.BriefActed++ })
}

func (d *judgmentDay) counts(kind string) *judgmentCounts {
	c := d.Kinds[kind]
	if c == nil {
		c = &judgmentCounts{}
		d.Kinds[kind] = c
	}
	return c
}

// summary is the day's one line: counts only.
func (d *judgmentDay) summary() string {
	kinds := []string{judgmentTopic, judgmentOffer, judgmentCard}
	for kind := range d.Kinds {
		if kind != judgmentTopic && kind != judgmentOffer && kind != judgmentCard {
			kinds = append(kinds, kind)
		}
	}
	sort.Strings(kinds[3:])
	total := judgmentCounts{}
	parts := []string{"day=" + d.Day}
	for _, kind := range kinds {
		c := d.Kinds[kind]
		if c == nil {
			c = &judgmentCounts{}
		}
		total.Made, total.Answered, total.Accepted = total.Made+c.Made, total.Answered+c.Answered, total.Accepted+c.Accepted
		parts = append(parts, fmt.Sprintf("%s=%d/%d/%d", kind, c.Made, c.Answered, c.Accepted))
	}
	parts = append(parts, fmt.Sprintf("total=%d/%d/%d", total.Made, total.Answered, total.Accepted), fmt.Sprintf("brief_acted=%d", d.BriefActed))
	return strings.Join(parts, " ") + " (made/answered/accepted)"
}

func updateJudgments(change func(*judgmentDay)) {
	judgmentsMu.Lock()
	defer judgmentsMu.Unlock()
	dir, err := judgmentsDir()
	if err != nil {
		return
	}
	d := currentJudgmentDayLocked(dir)
	change(d)
	_ = writeJudgmentDay(dir, d)
}

// currentJudgmentDayLocked is today's counts, closing an earlier day first.
func currentJudgmentDayLocked(dir string) *judgmentDay {
	today := judgmentsNow().Format("2006-01-02")
	if judgmentsDay == nil {
		judgmentsDay = readJudgmentDay(dir, today)
		closeEarlierJudgmentDays(dir, today)
	}
	if judgmentsDay.Day != today {
		closeJudgmentDay(dir, judgmentsDay)
		judgmentsDay = readJudgmentDay(dir, today)
	}
	return judgmentsDay
}

func readJudgmentDay(dir, day string) *judgmentDay {
	d := &judgmentDay{Day: day, Kinds: map[string]*judgmentCounts{}, Seen: map[string]bool{}}
	raw, err := os.ReadFile(filepath.Join(dir, day+".json"))
	if err != nil {
		return d
	}
	var saved judgmentDay
	if json.Unmarshal(raw, &saved) == nil && saved.Day == day {
		if saved.Kinds != nil {
			d.Kinds = saved.Kinds
		}
		if saved.Seen != nil {
			d.Seen = saved.Seen
		}
		d.BriefActed = saved.BriefActed
	}
	return d
}

func writeJudgmentDay(dir string, d *judgmentDay) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	raw, err := json.Marshal(d)
	if err != nil {
		return err
	}
	path := filepath.Join(dir, d.Day+".json")
	if err := os.WriteFile(path+".tmp", raw, 0o600); err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}

// closeJudgmentDay appends the day's line to summary.log once and logs it.
func closeJudgmentDay(dir string, d *judgmentDay) {
	marker := filepath.Join(dir, d.Day+".closed")
	if _, err := os.Stat(marker); err == nil {
		return
	}
	line := d.summary()
	file, err := os.OpenFile(filepath.Join(dir, "summary.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	_, werr := file.WriteString(line + "\n")
	_ = file.Close()
	if werr == nil {
		_ = os.WriteFile(marker, nil, 0o600)
		log.Printf("[judgments] %s", line)
	}
}

// closeEarlierJudgmentDays closes any earlier day still open, for a launch
// after a day with no measures.
func closeEarlierJudgmentDays(dir, today string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		day := strings.TrimSuffix(name, ".json")
		if len(day) != len("2006-01-02") || day >= today {
			continue
		}
		closeJudgmentDay(dir, readJudgmentDay(dir, day))
	}
}

// closeJudgmentDaysAtLaunch closes yesterday's line even when today has no
// measure yet.
func closeJudgmentDaysAtLaunch() {
	updateJudgments(func(*judgmentDay) {})
}

// clearJudgmentAnswers takes the answers out of the daily measures (Your
// data's Delete your answers): each day keeps the proposals it made and
// loses what was answered and accepted, and the brief items acted on. The
// closed days' summary lines, which carry the same counts, go too.
func clearJudgmentAnswers() error {
	judgmentsMu.Lock()
	defer judgmentsMu.Unlock()
	dir, err := judgmentsDir()
	if err != nil {
		return err
	}
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return err
	}
	for _, path := range files {
		day := strings.TrimSuffix(filepath.Base(path), ".json")
		d := readJudgmentDay(dir, day)
		for _, c := range d.Kinds {
			c.Answered, c.Accepted = 0, 0
		}
		// Seen keeps its short hashes, so a proposal is still counted made
		// once a day; they hold no answer.
		d.BriefActed = 0
		if err := writeJudgmentDay(dir, d); err != nil {
			return err
		}
		if judgmentsDay != nil && judgmentsDay.Day == day {
			judgmentsDay = d
		}
	}
	if err := os.Remove(filepath.Join(dir, "summary.log")); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
