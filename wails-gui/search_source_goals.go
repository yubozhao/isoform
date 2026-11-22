package main

import (
	"context"
	"strings"
	"time"
)

func (a *App) searchGoals(ctx context.Context, words []string) ([]searchCandidate, error) {
	if !featureOn(featurePlaybooks) {
		return nil, nil
	}
	if a == nil || a.activity == nil || a.activity.store == nil {
		return nil, nil
	}
	store, err := a.userPurposeStore()
	if err != nil {
		return nil, err
	}
	details, err := store.ListDetailsAllContext(ctx)
	if err != nil {
		return nil, err
	}
	// Mail subjects affect only which page group holds a goal, not its
	// displayed words or membership. Avoid an unrelated source read here.
	page := goalsPage(details, nil, time.Now())
	out := []searchCandidate{}
	for _, group := range [][]GoalItemView{page.Tracking, page.Done, page.Held} {
		for _, g := range group {
			body := strings.Join([]string{g.Outcome, g.WaitingOn, g.DueDate}, "\n")
			if !searchMatches(g.Title+"\n"+body, words) {
				continue
			}
			out = append(out, searchCandidate{result: SearchResult{ID: g.ID, Place: "goal", Title: g.Title, At: mobileBriefParseTime(g.AddedAt).UnixMilli(), Target: SearchTarget{Kind: "goal", ID: g.ID}}, body: body})
			if len(out) == searchCandidateLimit {
				return out, ctx.Err()
			}
		}
	}
	return out, ctx.Err()
}
