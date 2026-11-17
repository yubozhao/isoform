// Package topicday is the daily saved copy of the person's topics: the
// projects they work on and the subjects they follow, each with Isoform's
// proposed next piece of work.
//
// The copy is built once a day (docs/rework-2026-11.md, Bo Nov 14). There is no
// live registry and no privacy check per read: the person's own choices
// (forgotten topics, excluded apps, "not mine", Closed Eyes) are applied when
// the copy is built, and again to anything that leaves the computer (an agent
// task, the phone) through the same Filter.
//
// The package holds no app state: the app passes in the candidates, the
// choices, the feedback so far and the function that proposes the work.
package topicday

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"
)

// Kinds of topic.
const (
	KindProject = "project" // something the person works on
	KindSubject = "subject" // something the person follows
)

// Verdicts on a proposal.
const (
	VerdictYes     = "yes"      // run the work
	VerdictNo      = "no"       // not this work
	VerdictNotMine = "not_mine" // the topic is not the person's
)

// Proposal states.
const (
	StateProposed = "proposed"
	StateRunning  = "running" // a yes started a task
	StateDone     = "done"    // the task finished; Result holds its line
	StateFailed   = "failed"  // the task could not finish
	StateDeclined = "declined"
	StateNotMine  = "not_mine"
)

// CopyVersion is the saved copy's format.
const CopyVersion = 1

// MaxTopics bounds a copy.
const MaxTopics = 12

// Candidate is one topic offered to a build.
type Candidate struct {
	ID         string
	Kind       string
	Name       string
	Why        string
	LastSeenAt string
	Latest     string // the newest thing that happened, in a line
	Next       string // what is open, in a line
	// Apps are the apps the topic was seen in, for the excluded-apps choice.
	Apps []string
	// Named is a topic the person named themselves (Topics v1.0.0 rule 32):
	// it keeps its place, first, and no cut drops it.
	Named bool
}

// Proposal is Isoform's proposed next piece of work for a topic.
type Proposal struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Why    string `json:"why,omitempty"`
	Prompt string `json:"prompt"` // the task's instructions; stays on the computer
	State  string `json:"state"`
	TaskID string `json:"taskId,omitempty"`
	Result string `json:"result,omitempty"`
	// AnsweredAt is when the person said yes, no or "not mine" (unix ms).
	AnsweredAt int64 `json:"answeredAt,omitempty"`
}

// Topic is one topic in the saved copy.
type Topic struct {
	ID         string    `json:"id"`
	Kind       string    `json:"kind"`
	Name       string    `json:"name"`
	Why        string    `json:"why,omitempty"`
	LastSeenAt string    `json:"lastSeenAt,omitempty"`
	Latest     string    `json:"latest,omitempty"`
	Next       string    `json:"next,omitempty"`
	Apps       []string  `json:"apps,omitempty"`
	Named      bool      `json:"named,omitempty"`
	Proposal   *Proposal `json:"proposal,omitempty"`
}

// Copy is the saved daily copy.
type Copy struct {
	Version int     `json:"version"`
	Day     string  `json:"day"` // local date it was built for, YYYY-MM-DD
	BuiltAt int64   `json:"builtAt"`
	Topics  []Topic `json:"topics"`
}

// Choices are the person's own choices a build and a send honor.
type Choices struct {
	ClosedEyes bool
	// Forgotten holds topic IDs and lowercased names the person removed or
	// forgot, and the topics they said are "not mine".
	Forgotten map[string]bool
	// ExcludedApps are lowercased app names the person excluded.
	ExcludedApps []string
}

func (c Choices) forgets(id, name string) bool {
	return c.Forgotten[id] || c.Forgotten[normalize(name)]
}

// excludedBy reports a topic seen only in excluded apps, or named after one.
func (c Choices) excludedBy(name string, apps []string) bool {
	if len(c.ExcludedApps) == 0 {
		return false
	}
	excluded := map[string]bool{}
	for _, app := range c.ExcludedApps {
		excluded[normalize(app)] = true
	}
	if excluded[normalize(name)] {
		return true
	}
	if len(apps) == 0 {
		return false
	}
	for _, app := range apps {
		if !excluded[normalize(app)] {
			return false
		}
	}
	return true
}

func normalize(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// Allowed reports whether a topic passes the person's choices.
func (c Choices) Allowed(id, name string, apps []string) bool {
	return !c.ClosedEyes && !c.forgets(id, name) && !c.excludedBy(name, apps)
}

// Filter keeps the topics that pass the person's choices. It is the one
// filter for the build and for anything that leaves the computer.
func Filter(topics []Topic, c Choices) []Topic {
	out := make([]Topic, 0, len(topics))
	for _, t := range topics {
		if c.Allowed(t.ID, t.Name, t.Apps) {
			out = append(out, t)
		}
	}
	return out
}

// Feedback is one answer, kept as a record the next build reads.
type Feedback struct {
	TopicID       string `json:"topicId"`
	TopicName     string `json:"topicName"`
	ProposalID    string `json:"proposalId"`
	ProposalTitle string `json:"proposalTitle"`
	Verdict       string `json:"verdict"`
	At            int64  `json:"at"`
}

// ProposalDraft is what a Proposer returns for a topic.
type ProposalDraft struct {
	Title  string
	Why    string
	Prompt string
}

// Proposer proposes the next piece of work for each topic, keyed by topic ID.
// It gets the feedback so far, so it does not propose declined work again.
type Proposer func(ctx context.Context, topics []Topic, feedback []Feedback) (map[string]ProposalDraft, error)

// ErrClosedEyes is returned by Build while Closed Eyes is on: no copy is made.
var ErrClosedEyes = errors.New("closed eyes is on")

// Build makes the day's copy: the candidates that pass the person's choices,
// newest first, at most MaxTopics, each with a proposal when the proposer has
// one. A topic whose proposal is still running in the previous copy keeps it,
// so a task's result is never lost to a rebuild. The proposer runs once for all
// topics. A proposer error still saves the topics, without new proposals.
func Build(ctx context.Context, day string, now time.Time, candidates []Candidate, choices Choices, previous *Copy, feedback []Feedback, propose Proposer, newID func() string) (Copy, error) {
	if choices.ClosedEyes {
		return Copy{}, ErrClosedEyes
	}
	topics := make([]Topic, 0, len(candidates))
	seen := map[string]bool{}
	for _, c := range candidates {
		name := strings.TrimSpace(c.Name)
		if c.ID == "" || name == "" || seen[c.ID] || (c.Kind != KindProject && c.Kind != KindSubject) {
			continue
		}
		if !choices.Allowed(c.ID, name, c.Apps) {
			continue
		}
		seen[c.ID] = true
		topics = append(topics, Topic{ID: c.ID, Kind: c.Kind, Name: name, Why: strings.TrimSpace(c.Why), LastSeenAt: c.LastSeenAt,
			Latest: strings.TrimSpace(c.Latest), Next: strings.TrimSpace(c.Next), Apps: append([]string(nil), c.Apps...), Named: c.Named})
	}
	// The topics the person named go first, so the cut never drops them; then
	// the newest.
	sort.SliceStable(topics, func(i, j int) bool {
		if topics[i].Named != topics[j].Named {
			return topics[i].Named
		}
		return topics[i].LastSeenAt > topics[j].LastSeenAt
	})
	if len(topics) > MaxTopics {
		topics = topics[:MaxTopics]
	}
	kept := map[string]*Proposal{}
	if previous != nil {
		for _, t := range previous.Topics {
			if t.Proposal != nil && t.Proposal.State == StateRunning {
				p := *t.Proposal
				kept[t.ID] = &p
			}
		}
	}
	var ask []Topic
	for i := range topics {
		if p := kept[topics[i].ID]; p != nil {
			topics[i].Proposal = p
			continue
		}
		ask = append(ask, topics[i])
	}
	if propose != nil && len(ask) > 0 {
		drafts, err := propose(ctx, ask, feedback)
		if err == nil {
			for i := range topics {
				if topics[i].Proposal != nil {
					continue
				}
				d, ok := drafts[topics[i].ID]
				if !ok || strings.TrimSpace(d.Title) == "" || strings.TrimSpace(d.Prompt) == "" {
					continue
				}
				topics[i].Proposal = &Proposal{ID: newID(), Title: strings.TrimSpace(d.Title), Why: strings.TrimSpace(d.Why),
					Prompt: strings.TrimSpace(d.Prompt), State: StateProposed}
			}
		}
	}
	return Copy{Version: CopyVersion, Day: day, BuiltAt: now.UnixMilli(), Topics: topics}, nil
}
