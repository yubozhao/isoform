package main

import "context"

type MergeResult struct {
	MergeID string     `json:"mergeId"`
	Target  PersonCard `json:"target"`
}

// MergePeople hides source profiles and returns an Undo receipt and target card.
func (a *App) MergePeople(targetID string, sourceIDs []string) (MergeResult, error) {
	store, err := a.requireStore()
	if err != nil {
		return MergeResult{}, err
	}
	id, merged, err := store.MergePeople(context.Background(), targetID, sourceIDs)
	if err != nil {
		return MergeResult{}, err
	}
	a.touchDerivedViews(derivedPeopleOverview)
	// The merge is already durable; a failed enrichment must not hide its Undo ID.
	result := MergeResult{MergeID: id, Target: PersonCard{ID: targetID, Kind: merged.Kind, Name: merged.DisplayName, Relationship: merged.Relationship, Loops: []PersonLoop{}}}
	if overview, err := a.ListPeopleOverview(); err == nil {
		for _, card := range append(overview.Open, overview.Everyone...) {
			if card.ID == targetID {
				result.Target = card
				break
			}
		}
	}
	return result, nil
}

// UndoMerge atomically restores the rows recorded by one People merge.
func (a *App) UndoMerge(mergeID string) error {
	// Who you know shows the person's own change at once.
	defer a.touchDerivedViews(derivedPeopleOverview)
	store, err := a.requireStore()
	if err != nil {
		return err
	}
	return store.UndoMerge(context.Background(), mergeID)
}

// NotSamePeople is Not the same on a merge suggestion: Isoform never
// suggests merging these profiles again.
func (a *App) NotSamePeople(ids []string) error {
	// Who you know shows the person's own change at once.
	defer a.touchDerivedViews(derivedPeopleOverview)
	store, err := a.requireStore()
	if err != nil {
		return err
	}
	return store.MarkPeopleNotSame(context.Background(), ids)
}

// UndoNotSamePeople takes Not the same back.
func (a *App) UndoNotSamePeople(ids []string) error {
	// Who you know shows the person's own change at once.
	defer a.touchDerivedViews(derivedPeopleOverview)
	store, err := a.requireStore()
	if err != nil {
		return err
	}
	return store.UndoPeopleNotSame(context.Background(), ids)
}
