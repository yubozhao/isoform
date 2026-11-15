package main

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func purposeAgencyContextCreate(
	t *testing.T,
	store *PurposeMapStore,
	key string,
	evidenceClass string,
	occurredAt int64,
) PurposeOpenLoopDetail {
	t.Helper()
	result, added, err := store.Create(purposeTestCreateInput(
		key,
		PurposeKindCommitment,
		key,
		[]PurposeEvidenceInput{
			purposeTestEvidence(key, evidenceClass, PurposeStanceSupport, occurredAt),
		},
		occurredAt,
	))
	if err != nil || !added {
		t.Fatalf("create Purpose %q: added=%v err=%v", key, added, err)
	}
	detail, err := store.GetDetail(result.OpenLoop.ID)
	if err != nil {
		t.Fatal(err)
	}
	return detail
}

func purposeAgencyContextExclusion(
	items []TaskContextSourceExclusion,
	sourceID, reason string,
) bool {
	for _, item := range items {
		if item.SourceKind == taskContextSourcePurposeOpenLoopKind &&
			item.SourceID == sourceID && item.Reason == reason {
			return true
		}
	}
	return false
}

func TestGatherTaskContextSourcesProjectsCanonicalPurposeAtCutoff(
	t *testing.T,
) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	activity := newTestStore(t)
	store := NewPurposeMapStore(activity.db)
	base := time.Now().Add(-10 * time.Minute).UnixMilli()

	active := purposeAgencyContextCreate(
		t, store, "active-customer-brief", PurposeEvidenceExplicitUser, base,
	)
	waiting := purposeAgencyContextCreate(
		t, store, "waiting-customer-reply", PurposeEvidenceExplicitUser, base+1,
	)
	waitingMutation, added, err := store.AppendEvent(AppendPurposeOpenLoopEventInput{
		OpenLoopID:       waiting.OpenLoop.ID,
		ExpectedEventID:  waiting.Current.ID,
		IdempotencyKey:   "waiting-customer-reply:waiting",
		EventType:        PurposeEventWaiting,
		Title:            waiting.Current.Title,
		DesiredOutcome:   waiting.Current.DesiredOutcome,
		Owner:            waiting.Current.Owner,
		Counterparty:     waiting.Current.Counterparty,
		Scope:            waiting.Current.Scope,
		DueKind:          waiting.Current.DueKind,
		DueValue:         waiting.Current.DueValue,
		WaitingCondition: "Customer replies",
		ReasoningStatus:  PurposeReasoningExplicit,
		Rationale:        "The customer explicitly asked for time to review.",
		Confidence:       1,
		OccurredAt:       base + 2,
		Evidence: []PurposeEvidenceInput{
			purposeTestEvidence(
				"waiting-customer-reply:waiting",
				PurposeEvidenceExplicitUser,
				PurposeStanceWait,
				base+2,
			),
		},
	})
	if err != nil || !added {
		t.Fatalf("mark Purpose waiting: added=%v err=%v", added, err)
	}
	candidate := purposeAgencyContextCreate(
		t, store, "candidate-observation", PurposeEvidenceObservedUser, base+3,
	)
	resolved := purposeAgencyContextCreate(
		t, store, "resolved-release", PurposeEvidenceExplicitUser, base+4,
	)
	resolvedMutation, added, err := store.AppendEvent(purposeTestAppendInput(
		resolved,
		"resolved-release:verified",
		PurposeEventResolved,
		PurposeStanceResolve,
		PurposeEvidenceVerifiedOutcome,
		"resolved-release:verified",
		base+5,
	))
	if err != nil || !added {
		t.Fatalf("resolve Purpose: added=%v err=%v", added, err)
	}
	canonical := purposeAgencyContextCreate(
		t, store, "canonical-release", PurposeEvidenceExplicitUser, base+6,
	)
	duplicate := purposeAgencyContextCreate(
		t, store, "duplicate-release", PurposeEvidenceExplicitUser, base+7,
	)
	if _, added, err := store.CreateRelation(CreatePurposeRelationInput{
		IdempotencyKey:  "duplicate-release:canonical-release",
		SubjectID:       duplicate.OpenLoop.ID,
		ObjectID:        canonical.OpenLoop.ID,
		RelationType:    PurposeRelationDuplicateOf,
		EvidenceEventID: duplicate.Current.ID,
		OccurredAt:      base + 8,
	}); err != nil || !added {
		t.Fatalf("link duplicate Purpose: added=%v err=%v", added, err)
	}

	cutoff := time.Now().UnixMilli()
	time.Sleep(2 * time.Millisecond)
	futureAt := time.Now().UnixMilli()
	future := purposeAgencyContextCreate(
		t, store, "future-loop", PurposeEvidenceExplicitUser, futureAt,
	)
	time.Sleep(2 * time.Millisecond)
	futureRevisionAt := time.Now().UnixMilli()
	activeMutation, added, err := store.AppendEvent(AppendPurposeOpenLoopEventInput{
		OpenLoopID:      active.OpenLoop.ID,
		ExpectedEventID: active.Current.ID,
		IdempotencyKey:  "active-customer-brief:future-revision",
		EventType:       PurposeEventRevised,
		Title:           "A title that did not exist at the planning cutoff",
		DesiredOutcome:  active.Current.DesiredOutcome,
		Owner:           active.Current.Owner,
		Counterparty:    active.Current.Counterparty,
		Scope:           active.Current.Scope,
		DueKind:         active.Current.DueKind,
		ReasoningStatus: PurposeReasoningUnknown,
		Confidence:      0.8,
		OccurredAt:      futureRevisionAt,
		Evidence: []PurposeEvidenceInput{
			purposeTestEvidence(
				"active-customer-brief:future-revision",
				PurposeEvidenceCorrection,
				PurposeStanceUpdate,
				futureRevisionAt,
			),
		},
	})
	if err != nil || !added {
		t.Fatalf("append future Purpose revision: added=%v err=%v", added, err)
	}

	beforeCounts := purposeCountRows(t, activity, "purpose_open_loop_events_v1")
	request := TaskContextSourceRequest{
		Store: activity, CutoffAt: cutoff, Query: "customer release work",
	}
	first, err := GatherTaskContextSources(request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := GatherTaskContextSources(request)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatal("Purpose task context projection is not deterministic")
	}
	if got := purposeCountRows(t, activity, "purpose_open_loop_events_v1"); got != beforeCounts {
		t.Fatalf("Purpose context read mutated the ledger: before=%d after=%d", beforeCounts, got)
	}

	purposeCandidates := taskContextSourceCandidatesByKind(
		first.Candidates,
		taskContextSourcePurposeOpenLoopKind,
	)
	if len(purposeCandidates) != 3 {
		t.Fatalf("expected active, waiting, and canonical Purpose, got %+v", purposeCandidates)
	}
	found := make(map[string]TaskContextCandidate, len(purposeCandidates))
	for _, item := range purposeCandidates {
		found[item.SourceID] = item
		if item.Section != TaskContextSectionCommitments {
			t.Fatalf("Purpose entered the wrong context section: %+v", item)
		}
		parsed, ok := parsePurposeTaskContextReference(item.Ref)
		if !ok || parsed.OpenLoopID != item.SourceID {
			t.Fatalf("Purpose ref is not exact and parseable: %+v", item)
		}
	}
	activeCandidate, ok := found[active.OpenLoop.ID]
	if !ok || !strings.Contains(activeCandidate.Content, active.Current.Title) ||
		strings.Contains(activeCandidate.Content, activeMutation.Event.Title) {
		t.Fatalf("post-cutoff Purpose revision leaked into context: %+v", activeCandidate)
	}
	parsed, _ := parsePurposeTaskContextReference(activeCandidate.Ref)
	if parsed.CurrentEventID != active.Current.ID ||
		!strings.Contains(activeCandidate.Content, "Projection hash: "+parsed.ProjectionHash) {
		t.Fatalf("Purpose ref does not identify the frozen event and projection: %+v", activeCandidate)
	}
	if item := found[waiting.OpenLoop.ID]; !strings.Contains(item.Content, waitingMutation.Event.WaitingCondition) {
		t.Fatalf("waiting condition is absent from Purpose context: %+v", item)
	}

	checks := []struct {
		id     string
		reason string
	}{
		{candidate.OpenLoop.ID, TaskContextSourceReasonPurposeUnsupported},
		{resolvedMutation.OpenLoop.ID, TaskContextSourceReasonPurposeTerminal},
		{duplicate.OpenLoop.ID, TaskContextSourceReasonPurposeDuplicate},
		{future.OpenLoop.ID, TaskContextSourceReasonFuture},
		{active.OpenLoop.ID, TaskContextSourceReasonFuture},
	}
	for _, check := range checks {
		if !purposeAgencyContextExclusion(first.Exclusions, check.id, check.reason) {
			t.Fatalf("missing Purpose exclusion %s/%s: %+v", check.id, check.reason, first.Exclusions)
		}
	}
}

func TestGatherTaskContextSourcesDoesNotTopNLimitPurpose(
	t *testing.T,
) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	activity := newTestStore(t)
	store := NewPurposeMapStore(activity.db)
	base := time.Now().Add(-time.Hour).UnixMilli()
	const count = 31
	for index := 0; index < count; index++ {
		purposeAgencyContextCreate(
			t,
			store,
			fmt.Sprintf("portfolio-loop-%02d", index),
			PurposeEvidenceExplicitUser,
			base+int64(index),
		)
	}
	result, err := GatherTaskContextSources(TaskContextSourceRequest{
		Store: activity, CutoffAt: time.Now().UnixMilli(), Query: "portfolio",
	})
	if err != nil {
		t.Fatal(err)
	}
	items := taskContextSourceCandidatesByKind(
		result.Candidates,
		taskContextSourcePurposeOpenLoopKind,
	)
	if len(items) != count {
		t.Fatalf("Purpose source scan applied a fixed item cap: got=%d want=%d", len(items), count)
	}
}

func TestPurposeTaskContextDoesNotCarryExplicitTrustAcrossInferredRevision(
	t *testing.T,
) {
	t.Setenv("HOME", t.TempDir())
	activity := newTestStore(t)
	store := NewPurposeMapStore(activity.db)
	base := time.Now().Add(-time.Minute).UnixMilli()
	detail := purposeAgencyContextCreate(
		t,
		store,
		"explicit-then-inferred",
		PurposeEvidenceExplicitUser,
		base,
	)
	mutation, added, err := store.AppendEvent(AppendPurposeOpenLoopEventInput{
		OpenLoopID:      detail.OpenLoop.ID,
		ExpectedEventID: detail.Current.ID,
		IdempotencyKey:  "explicit-then-inferred:revision",
		EventType:       PurposeEventRevised,
		Title:           "An inferred revision",
		DesiredOutcome:  detail.Current.DesiredOutcome,
		Owner:           detail.Current.Owner,
		Counterparty:    detail.Current.Counterparty,
		Scope:           detail.Current.Scope,
		DueKind:         detail.Current.DueKind,
		ReasoningStatus: PurposeReasoningUnknown,
		Confidence:      0.7,
		OccurredAt:      base + 1,
		Evidence: []PurposeEvidenceInput{
			purposeTestEvidence(
				"explicit-then-inferred:revision",
				PurposeEvidenceObservedUser,
				PurposeStanceUpdate,
				base+1,
			),
		},
	})
	if err != nil || !added {
		t.Fatalf("append inferred Purpose revision: added=%v err=%v", added, err)
	}
	result, err := GatherTaskContextSources(TaskContextSourceRequest{
		Store: activity, CutoffAt: time.Now().UnixMilli(), Query: "inferred revision",
	})
	if err != nil {
		t.Fatal(err)
	}
	items := taskContextSourceCandidatesByKind(
		result.Candidates,
		taskContextSourcePurposeOpenLoopKind,
	)
	if len(items) != 1 || items[0].Ref == "" ||
		items[0].EvidenceClass != TaskContextEvidenceEvaluatedProjection ||
		!strings.Contains(items[0].Content, mutation.Event.Title) {
		t.Fatalf("inferred revision inherited explicit trust: %+v", items)
	}
}
