package main

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"wails-gui/internal/topicday"
)

// A topic the person names (Topics v1.0.0 rules 30 and 32) is a topic at
// once, with its first proposal in a minute: the one exception to the daily
// build. The daily build keeps it in place from then on
// (addedTopicCandidates).

// addedTopicCandidates are the topics the person named, as daily build
// candidates. They carry their naming time as last seen, so they rank as
// recent, and no apps, so no excluded app removes them.
func (a *App) addedTopicCandidates() []topicday.Candidate {
	added := a.topicsSvc().store.load().Added
	out := make([]topicday.Candidate, 0, len(added))
	for _, t := range added {
		out = append(out, topicday.Candidate{ID: t.ID, Kind: topicday.KindSubject, Name: t.Label, LastSeenAt: t.At})
	}
	return out
}

// proposeAddedTopics puts every named topic that is not in today's saved
// copy into it, and asks for its first proposal. It runs under the daily
// build's lock, so it never races a build.
func (a *App) proposeAddedTopics(ctx context.Context) {
	lock, _ := topicsDailyBuild.LoadOrStore(a, &sync.Mutex{})
	mu := lock.(*sync.Mutex)
	mu.Lock()
	defer mu.Unlock()
	if a.IsClosedEyesModeEnabled() {
		return
	}
	store, err := a.topicsDailyStore()
	if err != nil {
		return
	}
	feedback, _ := store.Feedback()
	choices := a.topicsDailyChoices(feedback)
	now := topicsDailyNow()
	saved, ok := store.Load()
	if !ok {
		saved = topicday.Copy{Version: topicday.CopyVersion, Day: now.Format("2006-01-02"), BuiltAt: now.UnixMilli()}
	}
	present := map[string]bool{}
	for _, t := range saved.Topics {
		present[t.ID] = true
	}
	var fresh []topicday.Topic
	for _, c := range a.addedTopicCandidates() {
		if present[c.ID] || !choices.Allowed(c.ID, c.Name, nil) {
			continue
		}
		fresh = append(fresh, topicday.Topic{ID: c.ID, Kind: c.Kind, Name: strings.TrimSpace(c.Name), LastSeenAt: c.LastSeenAt})
	}
	if len(fresh) == 0 {
		return
	}
	started := time.Now()
	drafts, err := a.proposeDailyTopicWork(ctx, fresh, feedback)
	if err != nil {
		log.Printf("[topics-added] first proposal failed: topics=%d err=%v", len(fresh), err)
	}
	proposed := 0
	for i := range fresh {
		if d, ok := drafts[fresh[i].ID]; ok && strings.TrimSpace(d.Title) != "" && strings.TrimSpace(d.Prompt) != "" {
			fresh[i].Proposal = &topicday.Proposal{ID: uuid.NewString(), Title: strings.TrimSpace(d.Title), Why: strings.TrimSpace(d.Why),
				Prompt: strings.TrimSpace(d.Prompt), State: topicday.StateProposed}
			proposed++
		}
	}
	// The named topics lead: the person just asked for them.
	saved.Topics = append(fresh, saved.Topics...)
	if err := store.Save(saved); err != nil {
		log.Printf("[topics-added] saving: %v", err)
		return
	}
	for _, t := range fresh {
		if t.Proposal != nil {
			noteProposalMade(judgmentTopic, t.Proposal.ID)
		}
	}
	log.Printf("[topics-added] topics=%d proposals=%d ms=%d", len(fresh), proposed, time.Since(started).Milliseconds())
	a.emitBrowserEvent("topics:updated")
	a.emitBrowserEvent(workUpdatedEvent)
}
