package main

// What your agents did, for the brief (brief-agents v1.0.0). The brief owns
// where the paragraph goes and its words; this is the data, from the same
// records as Agents, saying only what Isoform knows: what came back, and
// what has had no word for two hours. Never "working".

import (
	"sort"
	"time"
)

// BriefAgentResult is one result back since the last brief.
type BriefAgentResult struct {
	AgentName string   `json:"agentName"`
	Title     string   `json:"title"`
	Summary   string   `json:"summary"`
	LinkID    string   `json:"linkId"`
	At        int64    `json:"at"`
	Files     []string `json:"files,omitempty"`
	// Overnight is a result that came back between the evening brief and
	// the morning one (brief-agents rule 3).
	Overnight bool `json:"overnight,omitempty"`
}

// BriefAgentNoWord is a hand-off with no word for two hours.
type BriefAgentNoWord struct {
	ID        string `json:"id"`
	AgentName string `json:"agentName"`
	Title     string `json:"title"`
	Since     int64  `json:"since"`
}

// BriefAgentNews is the brief's "Your agents:" data. Requests between
// agents stay empty until agents can ask each other (C2).
type BriefAgentNews struct {
	Results  []BriefAgentResult `json:"results"`
	Requests []struct{}         `json:"requests"`
	NoWord   []BriefAgentNoWord `json:"noWord"`
}

// Empty is true when nothing happened with the agents: the brief then has
// no agents paragraph (rule 1).
func (n BriefAgentNews) Empty() bool {
	return len(n.Results) == 0 && len(n.Requests) == 0 && len(n.NoWord) == 0
}

// briefAgentNews is what happened with the agents since the last brief.
// overnightFrom, when set, is the evening brief's time: results after it
// are overnight in a morning brief.
func (a *App) briefAgentNews(since, overnightFrom time.Time) BriefAgentNews {
	out := BriefAgentNews{Results: []BriefAgentResult{}, Requests: []struct{}{}, NoWord: []BriefAgentNoWord{}}
	statuses, err := a.GetHandoffStatuses()
	if err != nil {
		return out
	}
	results, _ := a.GetTaskLinkResults()
	byLink := map[string]TaskLinkResult{}
	for _, r := range results {
		byLink[r.LinkID] = r
	}
	cutoff := since.UnixMilli()
	for _, s := range statuses {
		switch s.State {
		case handoffBack:
			at := s.SentAt
			var files []string
			if r, ok := byLink[s.ID]; ok {
				at, files = r.ResultAt, r.Files
			}
			if at < cutoff {
				continue
			}
			out.Results = append(out.Results, BriefAgentResult{
				AgentName: s.AgentName, Title: s.Title, Summary: s.Line, LinkID: s.ID, At: at, Files: files,
				Overnight: !overnightFrom.IsZero() && at >= overnightFrom.UnixMilli(),
			})
		case handoffNoWord:
			out.NoWord = append(out.NoWord, BriefAgentNoWord{ID: s.ID, AgentName: s.AgentName, Title: s.Title, Since: s.Since})
		}
	}
	sort.SliceStable(out.Results, func(i, j int) bool { return out.Results[i].At > out.Results[j].At })
	return out
}
