package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// selfDreamManifestRef returns the frozen manifest ref the next run gives a
// canonical Self entry.
func selfDreamManifestRef(t *testing.T, rig selfDreamTestRig, entryID string) string {
	t.Helper()
	input, err := rig.app.buildSelfDreamFrozenInput(time.Now().UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	ref := selfDreamEntryManifestRefs(input.Run.Manifest)[entryID]
	if ref == "" {
		t.Fatalf("no manifest ref for Self entry %s", entryID)
	}
	return ref
}

func selfDreamModelResponse(t *testing.T, drafts ...SelfDreamOperationDraft) string {
	t.Helper()
	raw, err := json.Marshal(SelfDreamModelResponse{
		Operations:    drafts,
		Uncertainties: []string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestListSelfDreamUpdatesShowsModelProposalsUntilReviewed(t *testing.T) {
	rig := newSelfDreamTestRig(t)
	seedSelfDreamDuplicateEntries(t, rig)
	focus := addSelfDreamTestEntry(
		t,
		rig.store,
		"Prefer morning focus blocks for deep work.",
		time.Now().Add(-30*time.Second).UnixMilli(),
	)
	proposed := "Prefer morning focus blocks for deep work and planning."
	rig.runner.response = selfDreamModelResponse(t, SelfDreamOperationDraft{
		Operation:       SelfDreamOperationSupersede,
		TargetEntryIDs:  []string{focus.ID},
		SelfKind:        selfModelKindPreference,
		Scope:           SelfDreamScope{Kind: "project", Key: "yansu"},
		Statement:       proposed,
		ReasoningStatus: "unknown",
		Uncertainty:     "Planning is mentioned once.",
		Confidence:      0.7,
		EvidenceRefs:    []string{selfDreamManifestRef(t, rig, focus.ID)},
	})
	if _, err := rig.app.runSelfDreamShadow(context.Background(), "scheduled"); err != nil {
		t.Fatal(err)
	}

	updates, err := rig.app.ListSelfDreamUpdates()
	if err != nil {
		t.Fatal(err)
	}
	// The exact-duplicate merge waits for the rollout gate, not for the user.
	if len(updates) != 1 {
		t.Fatalf("updates = %+v", updates)
	}
	update := updates[0]
	if update.Operation != SelfDreamOperationSupersede ||
		len(update.Before) != 1 || update.Before[0] != focus.Statement ||
		len(update.After) != 1 || update.After[0] != proposed ||
		update.Scope != (SelfDreamScope{Kind: "project", Key: "yansu"}) ||
		update.EvidenceCount != 1 ||
		update.Uncertainty != "Planning is mentioned once." {
		t.Fatalf("update = %+v", update)
	}

	if _, err := rig.app.ReviewSelfDreamOperation(ReviewSelfDreamOperationInput{
		OperationID:    update.ID,
		IdempotencyKey: "settings-review-1",
		Decision:       SelfDreamDecisionAccept,
	}); err != nil {
		t.Fatal(err)
	}
	updates, err = rig.app.ListSelfDreamUpdates()
	if err != nil {
		t.Fatal(err)
	}
	if len(updates) != 0 {
		t.Fatalf("reviewed update is still listed: %+v", updates)
	}
	latest, err := rig.store.GetLatestSelfModelEntry(focus.EntryKey)
	if err != nil {
		t.Fatal(err)
	}
	if latest.Status != selfModelStatusRetracted {
		t.Fatalf("accepted supersede left the old entry active: %+v", latest)
	}
}

func TestListPendingSelfDreamUpdatesCollapsesRepeatsAndHonorsDecisions(t *testing.T) {
	rig := newSelfDreamTestRig(t)
	now := time.Now().Add(-time.Minute).UnixMilli()
	focus := addSelfDreamTestEntry(t, rig.store, "Prefer morning focus blocks for deep work.", now)
	notes := addSelfDreamTestEntry(t, rig.store, "Keep meeting notes short.", now+1)
	supersedeFocus := func(rationale string) SelfDreamOperationDraft {
		return SelfDreamOperationDraft{
			Operation:       SelfDreamOperationSupersede,
			TargetEntryIDs:  []string{focus.ID},
			SelfKind:        selfModelKindPreference,
			Scope:           SelfDreamScope{Kind: "project", Key: "yansu"},
			Statement:       "Prefer morning focus blocks for deep work and planning.",
			ReasoningStatus: "unknown",
			Rationale:       rationale,
			Confidence:      0.6,
			EvidenceRefs:    []string{selfDreamManifestRef(t, rig, focus.ID)},
		}
	}
	rig.runner.response = selfDreamModelResponse(t, supersedeFocus("first run"))
	first, err := rig.app.runSelfDreamShadow(context.Background(), "scheduled")
	if err != nil {
		t.Fatal(err)
	}

	// The second run sees a new entry and an exact duplicate, repeats the
	// first run's proposal with new wording, and adds two more.
	addSelfDreamTestEntry(t, rig.store, "Review pull requests after lunch.", now+2)
	seedSelfDreamDuplicateEntries(t, rig)
	rig.runner.response = selfDreamModelResponse(t,
		supersedeFocus("second run"),
		SelfDreamOperationDraft{
			Operation:       SelfDreamOperationContradict,
			TargetEntryIDs:  []string{notes.ID},
			SelfKind:        selfModelKindPreference,
			Scope:           SelfDreamScope{Kind: "project", Key: "yansu"},
			Statement:       "Keep meeting notes detailed when decisions are made.",
			ReasoningStatus: "unknown",
			Confidence:      0.5,
			EvidenceRefs:    []string{selfDreamManifestRef(t, rig, notes.ID)},
		},
		SelfDreamOperationDraft{
			Operation:       SelfDreamOperationSupersede,
			TargetEntryIDs:  []string{notes.ID},
			SelfKind:        selfModelKindPreference,
			Scope:           SelfDreamScope{Kind: "project", Key: "yansu"},
			Statement:       "Keep meeting notes short and shared.",
			ReasoningStatus: "unknown",
			Confidence:      0.5,
			EvidenceRefs:    []string{"not-in-the-frozen-manifest"},
		},
	)
	second, err := rig.app.runSelfDreamShadow(context.Background(), "scheduled")
	if err != nil {
		t.Fatal(err)
	}
	if first.Run.ID == second.Run.ID {
		t.Fatal("expected a second run")
	}
	find := func(detail SelfDreamRunDetail, operation string) SelfDreamOperationRecord {
		t.Helper()
		for _, op := range detail.Operations {
			if op.Operation == operation && op.ValidationStatus == SelfDreamValidationReviewRequired {
				return op
			}
		}
		t.Fatalf("run %s has no reviewable %s: %+v", detail.Run.ID, operation, detail.Operations)
		return SelfDreamOperationRecord{}
	}
	olderFocus := find(first, SelfDreamOperationSupersede)
	newerFocus := find(second, SelfDreamOperationSupersede)
	contradict := find(second, SelfDreamOperationContradict)
	if olderFocus.OperationHash == newerFocus.OperationHash ||
		selfDreamProposalKey(olderFocus) != selfDreamProposalKey(newerFocus) {
		t.Fatal("a repeated proposal with a new rationale must share its key")
	}

	pending := func() map[string]bool {
		t.Helper()
		updates, err := rig.store.ListPendingSelfDreamUpdates(0)
		if err != nil {
			t.Fatal(err)
		}
		out := make(map[string]bool, len(updates))
		for _, update := range updates {
			out[update.ID] = true
		}
		return out
	}
	// The repeat is listed once, as the first run's operation, so its id does
	// not change while it waits. The exact duplicate merge and the rejected
	// draft never wait for the user.
	if got := pending(); len(got) != 2 || !got[olderFocus.ID] || !got[contradict.ID] {
		t.Fatalf("pending = %v, want %s and %s", got, olderFocus.ID, contradict.ID)
	}

	// A decision on either copy answers the proposal.
	if _, err := rig.app.ReviewSelfDreamOperation(ReviewSelfDreamOperationInput{
		OperationID:    newerFocus.ID,
		IdempotencyKey: "keep-separate-1",
		Decision:       SelfDreamDecisionKeepSeparate,
	}); err != nil {
		t.Fatal(err)
	}
	if got := pending(); len(got) != 1 || !got[contradict.ID] {
		t.Fatalf("pending after decision = %v", got)
	}

	// A newer revision of the target makes the proposal stale.
	notes.Confidence = 0.95
	notes.Evidence = append(notes.Evidence, SelfModelEvidence{
		SourceKind: "explicit_user_statement",
		SourceID:   "newer-notes-source",
		Statement:  "Keep meeting notes short.",
		Confidence: 1,
		OccurredAt: time.Now().UnixMilli(),
	})
	notes.CreatedAt = time.Now().UnixMilli()
	if _, added, err := rig.store.AddSelfModelEntry(notes); err != nil || !added {
		t.Fatalf("append newer Self revision: added=%v err=%v", added, err)
	}
	if got := pending(); len(got) != 0 {
		t.Fatalf("stale proposal is still pending: %v", got)
	}
}

func TestSelfDreamProposalKeyIgnoresRunSpecificDetails(t *testing.T) {
	base := SelfDreamOperationRecord{
		ID:             "op-1",
		Operation:      SelfDreamOperationSplit,
		TargetEntryIDs: []string{"b", "a"},
		SelfKind:       selfModelKindPreference,
		SplitStatements: []string{
			"Write short notes.",
			"Share notes the same day.",
		},
		EvidenceRefs: []string{"work-profile:snapshot-1:global:all"},
		Rationale:    "first",
		Confidence:   0.5,
	}
	repeat := base
	repeat.ID = "op-2"
	repeat.TargetEntryIDs = []string{"a", "b"}
	repeat.SplitStatements = []string{"share notes the same day", "Write short notes"}
	repeat.EvidenceRefs = []string{"work-profile:snapshot-2:global:all", "memory:extra"}
	repeat.Rationale = "second"
	repeat.Confidence = 0.8
	if selfDreamProposalKey(base) != selfDreamProposalKey(repeat) {
		t.Fatal("ordering, punctuation, evidence refs, rationale and confidence must not change the key")
	}
	newerTarget := base
	newerTarget.TargetEntryIDs = []string{"a", "b-revision-2"}
	if selfDreamProposalKey(base) == selfDreamProposalKey(newerTarget) {
		t.Fatal("a new revision of a target must make a new proposal")
	}
}

// selfDreamManifestRefOfKind returns the included frozen manifest ref the next
// run gives the first source of a kind.
func selfDreamManifestRefOfKind(t *testing.T, rig selfDreamTestRig, sourceKind string) string {
	t.Helper()
	input, err := rig.app.buildSelfDreamFrozenInput(time.Now().UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range input.Run.Manifest {
		if item.Included && item.SourceKind == sourceKind {
			return item.Ref
		}
	}
	t.Fatalf("no included %s source in the frozen manifest", sourceKind)
	return ""
}

func TestSelfDreamAcceptWithoutCompilationLeavesTheProposalOpen(t *testing.T) {
	rig := newSelfDreamTestRig(t)
	focus := addSelfDreamTestEntry(t, rig.store, "Prefer morning focus blocks for deep work.",
		time.Now().Add(-time.Minute).UnixMilli())
	addSelfDreamTestEntry(t, rig.store, "Keep meeting notes short.", time.Now().Add(-50*time.Second).UnixMilli())
	rig.runner.response = selfDreamModelResponse(t, SelfDreamOperationDraft{
		Operation:       SelfDreamOperationSupersede,
		TargetEntryIDs:  []string{focus.ID},
		SelfKind:        selfModelKindPreference,
		Scope:           SelfDreamScope{Kind: "project", Key: "yansu"},
		Statement:       "Prefer morning focus blocks for deep work and planning.",
		ReasoningStatus: "unknown",
		Confidence:      0.6,
		EvidenceRefs:    []string{selfDreamManifestRef(t, rig, focus.ID)},
	})
	if _, err := rig.app.runSelfDreamShadow(context.Background(), "scheduled"); err != nil {
		t.Fatal(err)
	}
	updates, err := rig.app.ListSelfDreamUpdates()
	if err != nil || len(updates) != 1 {
		t.Fatalf("updates = %+v err=%v", updates, err)
	}
	// An accept whose compilation never happened (it failed after the decision
	// row was written) is not an answer.
	if _, _, err := rig.store.AddSelfDreamDecision(SelfDreamDecisionRecord{
		OperationID:    updates[0].ID,
		IdempotencyKey: "accept-without-compilation",
		ActorKind:      "user",
		Decision:       SelfDreamDecisionAccept,
	}); err != nil {
		t.Fatal(err)
	}
	if again, err := rig.app.ListSelfDreamUpdates(); err != nil || len(again) != 1 {
		t.Fatalf("an unapplied accept hid the proposal: %+v err=%v", again, err)
	}
	// Trying again applies it.
	if _, err := rig.app.ReviewSelfDreamOperation(ReviewSelfDreamOperationInput{
		OperationID:    updates[0].ID,
		IdempotencyKey: "accept-again",
		Decision:       SelfDreamDecisionAccept,
	}); err != nil {
		t.Fatal(err)
	}
	if again, err := rig.app.ListSelfDreamUpdates(); err != nil || len(again) != 0 {
		t.Fatalf("applied proposal is still listed: %+v err=%v", again, err)
	}
	if _, err := rig.store.GetSelfDreamCompilation(updates[0].ID); err != nil {
		t.Fatalf("retry did not compile: %v", err)
	}
}
