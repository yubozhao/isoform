package main

import (
	"context"

	"wails-gui/internal/privacy"
)

// forgetAllTopics removes what Isoform learned about the person's topics
// (Your data's Forget everything): the topics, their journal, weights,
// overrides, pins and chat topics; every saved topic snapshot; the research
// log; and the daily topics store with its answers and Not mine. The two
// switches stay: whether topics are on, and how often they refresh.
func (a *App) forgetAllTopics(_ context.Context) error {
	svc := a.topicsSvc()
	if svc != nil && svc.store != nil {
		if _, err := svc.store.update(func(state *topicsStateFile) {
			*state = topicsStateFile{Enabled: state.Enabled, RefreshIntervalMinutes: state.RefreshIntervalMinutes}
		}); err != nil && svc.store.path != "" {
			return err
		}
	}
	if svc != nil && svc.snapshots != nil {
		if _, err := svc.snapshots.RemoveMatching(func(topicSnapshotEntry) bool { return true }); err != nil {
			return err
		}
	}
	if job := a.findTopicFindsJob(); job != nil {
		a.dropResearchLog(job)
	}
	if store, err := a.topicsDailyStore(); err == nil {
		if err := store.ForgetAll(); err != nil {
			return err
		}
	}
	// Every reader of topics drops what it holds.
	publishPrivacyWithdrawal(privacy.Scope{Kind: privacy.Source, SourceKind: "topics"})
	a.emitBrowserEvent("topics:updated")
	return nil
}
