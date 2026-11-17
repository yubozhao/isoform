// Command topic-proposal-replay measures topic proposals on a data folder:
// how many the person answered, how many they took, why they turned the rest
// down, and how many of those the daily build's rest rules
// (internal/topicday/rest.go) would have left out. It is read-only and prints
// counts only: never a title, a topic name, an id or a path.
//
// Each answered proposal is replayed in order against the answers that stood
// before it: had the rules been on, would that topic's proposal have rested?
// The accept rate before is over every answered proposal; after is over the
// ones the rules would still have made. A topic's last-seen time is known only
// from the current copy, so it is used for answers made after it; otherwise
// the stale rule and the already-done rule's "moved since" check stay off for
// that answer.
//
// Usage:
//
//	go run ./cmd/topic-proposal-replay                  # the app data folder
//	go run ./cmd/topic-proposal-replay -data /path/to/data-root
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"wails-gui/internal/topicday"
)

// ledgerRecord is the part of an answers-ledger entry this tool reads
// (answers_ledger.go AnswerRecord).
type ledgerRecord struct {
	EntryID string `json:"id"`
	ItemID  string `json:"item"`
	Source  string `json:"source"`
	Action  string `json:"action"`
	Reason  string `json:"reason"`
	Undoes  string `json:"undoes"`
}

// Counts is the replay's result.
type Counts struct {
	Answered, Yes, No, NotMine int
	Reasons                    map[string]int
	Rested                     map[string]int
	AfterAnswered, AfterYes    int
}

func main() {
	home, _ := os.UserHomeDir()
	data := flag.String("data", filepath.Join(home, ".isoform-agent"), "the app's data folder")
	flag.Parse()
	counts, err := replay(*data, time.Now())
	if err != nil {
		fmt.Fprintln(os.Stderr, "topic-proposal-replay:", err)
		os.Exit(1)
	}
	write(os.Stdout, counts)
}

func replay(data string, now time.Time) (Counts, error) {
	store := topicday.NewStore(filepath.Join(data, "topics-daily"))
	feedback, err := store.Feedback()
	if err != nil {
		return Counts{}, err
	}
	ledger, err := readLedger(filepath.Join(data, "answers", "ledger.jsonl"))
	if err != nil {
		return Counts{}, err
	}
	feedback = withLedger(feedback, ledger)
	seen := map[string]time.Time{}
	if saved, ok := store.Load(); ok {
		for _, t := range saved.Topics {
			if at, err := time.Parse(time.RFC3339, t.LastSeenAt); err == nil {
				seen[t.ID] = at
			}
		}
	}
	return count(topicday.Standing(feedback), seen), nil
}

// count replays each answer against the answers before it.
func count(standing []topicday.Feedback, seen map[string]time.Time) Counts {
	c := Counts{Reasons: map[string]int{}, Rested: map[string]int{}}
	for i, f := range standing {
		switch f.Verdict {
		case topicday.VerdictYes:
			c.Yes++
		case topicday.VerdictNo:
			c.No++
			reason := f.Reason
			if reason == "" {
				reason = "none"
			}
			c.Reasons[reason]++
		case topicday.VerdictNotMine:
			c.NotMine++
		default:
			continue
		}
		c.Answered++
		at := time.UnixMilli(f.At)
		topic := topicday.Topic{ID: f.TopicID}
		if last, ok := seen[f.TopicID]; ok && last.Before(at) {
			topic.LastSeenAt = last.Format(time.RFC3339)
		}
		if why := topicday.Rest(topic, standing[:i], at); why != "" {
			c.Rested[why]++
			continue
		}
		c.AfterAnswered++
		if f.Verdict == topicday.VerdictYes {
			c.AfterYes++
		}
	}
	return c
}

// withLedger gives a topic No from Your work the reason the ledger kept, and
// drops a topic answer the ledger shows was undone (before the build kept
// undos itself).
func withLedger(feedback []topicday.Feedback, ledger []ledgerRecord) []topicday.Feedback {
	undone := map[string]bool{}
	for _, r := range ledger {
		if r.Action == "undo" && r.Undoes != "" {
			undone[r.Undoes] = true
		}
	}
	type standing struct {
		action, reason string
		any            bool
	}
	items := map[string]*standing{}
	for _, r := range ledger {
		if r.Source != "topic" || r.Action == "undo" || r.Action == "shown" {
			continue
		}
		s := items[r.ItemID]
		if s == nil {
			s = &standing{}
			items[r.ItemID] = s
		}
		s.any = true
		if !undone[r.EntryID] {
			s.action, s.reason = r.Action, r.Reason
		}
	}
	out := make([]topicday.Feedback, 0, len(feedback))
	for _, f := range feedback {
		s := items["topic:"+f.TopicID+":"+f.ProposalID]
		if s != nil && s.any && s.action == "" && f.Verdict != topicday.VerdictUndo {
			continue
		}
		if s != nil && f.Verdict == topicday.VerdictNo && f.Reason == "" && s.action == "no" {
			f.Reason = s.reason
		}
		out = append(out, f)
	}
	return out
}

func readLedger(path string) ([]ledgerRecord, error) {
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var out []ledgerRecord
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 1<<20)
	for scanner.Scan() {
		var r ledgerRecord
		if json.Unmarshal(scanner.Bytes(), &r) == nil && r.EntryID != "" && r.ItemID != "" {
			out = append(out, r)
		}
	}
	return out, scanner.Err()
}

func rate(yes, answered int) string {
	if answered == 0 {
		return "n/a"
	}
	return fmt.Sprintf("%.0f%%", 100*float64(yes)/float64(answered))
}

func line(m map[string]int, keys ...string) string {
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", k, m[k]))
	}
	return strings.Join(parts, " ")
}

func write(w io.Writer, c Counts) {
	fmt.Fprintf(w, "answered=%d yes=%d no=%d not_mine=%d accept_before=%s\n", c.Answered, c.Yes, c.No, c.NotMine, rate(c.Yes, c.Answered))
	fmt.Fprintf(w, "no_reasons %s\n", line(c.Reasons, topicday.ReasonAlreadyDone, topicday.ReasonNotNow, topicday.ReasonNotUseful, topicday.ReasonMyself, "none"))
	fmt.Fprintf(w, "rested %s\n", line(c.Rested, topicday.RestAlreadyDone, topicday.RestNotNow, topicday.RestDeclined, topicday.RestStale))
	fmt.Fprintf(w, "after answered=%d yes=%d accept_after=%s\n", c.AfterAnswered, c.AfterYes, rate(c.AfterYes, c.AfterAnswered))
}
