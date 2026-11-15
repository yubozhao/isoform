package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func productionSizedWorkPatternProfileFixture(
	t *testing.T,
) WorkPatternProfileContext {
	t.Helper()
	intervalRefs := make([]string, 0, 5_000)
	sessionRefs := make([]string, 0, 1_000)
	manifest := make([]WorkPatternProfileSelectionEntry, 0, 5_001)
	for index := 0; index < 5_000; index++ {
		intervalRefs = append(
			intervalRefs,
			fmt.Sprintf("interval:%05d:%s", index, strings.Repeat("x", 40)),
		)
		manifest = append(manifest, WorkPatternProfileSelectionEntry{
			Ref:           fmt.Sprintf("projection:app:omitted-%05d", index),
			Kind:          "projection",
			EvidenceClass: WorkPatternEvidenceObserved,
			Reason:        "not_selected_for_current_context",
		})
	}
	for index := 0; index < 1_000; index++ {
		sessionRefs = append(
			sessionRefs,
			fmt.Sprintf("session:%05d:%s", index, strings.Repeat("y", 40)),
		)
	}
	selected := WorkPatternProjection{
		ScopeKind:    WorkPatternScopeGlobal,
		ScopeKey:     "all",
		Label:        "All retained work",
		EvidenceKind: WorkPatternEvidenceObserved,
		Status:       WorkPatternProfileStatusQualified,
		Confidence:   0.9,
		SampleCount:  len(intervalRefs),
		SessionCount: len(sessionRefs),
		FreshnessAt:  900,
		Stability:    0.8,
		DecayWeight:  0.9,
		IntervalRefs: intervalRefs,
		SessionRefs:  sessionRefs,
		Metrics: []WorkPatternMetric{{
			Name:              "median_dwell_seconds",
			Value:             2,
			Unit:              "seconds",
			SampleCount:       len(intervalRefs),
			Confidence:        0.9,
			SourceWindowStart: 100,
			SourceWindowEnd:   900,
			Stability:         0.8,
			DecayWeight:       0.9,
		}},
	}
	manifest = append(manifest, WorkPatternProfileSelectionEntry{
		Ref:           "projection:global:all",
		Kind:          "projection",
		EvidenceClass: WorkPatternEvidenceObserved,
		Included:      true,
		Reason:        "selected_for_current_context",
	})
	semantic := make([]WorkPatternSemanticSignal, 0, 20)
	for index := 0; index < 20; index++ {
		semantic = append(semantic, WorkPatternSemanticSignal{
			Ref:           fmt.Sprintf("memory:%02d", index),
			Section:       TaskContextSectionKnowledge,
			SourceKind:    "memory_markdown",
			SourceID:      fmt.Sprintf("memory-%02d", index),
			ObservedAt:    800 + int64(index),
			Content:       fmt.Sprintf("Preference %02d: %s", index, strings.Repeat("context ", 120)),
			EvidenceClass: TaskContextEvidenceExplicitUser,
			Relevance:     1 - float64(index)/100,
		})
	}
	payload := workPatternProfileContextPayload{
		SnapshotID:          "production-sized-profile",
		SnapshotSHA256:      strings.Repeat("a", 64),
		PolicyVersion:       WorkPatternProfilePolicyVersion,
		SourceCutoffAt:      999,
		Status:              WorkPatternProfileStatusQualified,
		Confidence:          0.9,
		InfluenceMode:       WorkPatternInfluenceProfile,
		SelectedProjections: []WorkPatternProjection{selected},
		SemanticSignals:     semantic,
		CapabilitySignals:   []WorkPatternCapabilitySignal{},
		Contradictions:      []WorkPatternProfileCorrection{},
		SelectionManifest:   manifest,
		SelectionReason:     "qualified_profile_available",
	}
	canonical, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if len(canonical) < 500_000 {
		t.Fatalf("production-sized profile=%d bytes, want a large fixture", len(canonical))
	}
	return WorkPatternProfileContext{
		SnapshotID:          payload.SnapshotID,
		SnapshotSHA256:      payload.SnapshotSHA256,
		PolicyVersion:       payload.PolicyVersion,
		SourceCutoffAt:      payload.SourceCutoffAt,
		Status:              payload.Status,
		Confidence:          payload.Confidence,
		InfluenceMode:       payload.InfluenceMode,
		SelectedProjections: payload.SelectedProjections,
		SemanticSignals:     payload.SemanticSignals,
		CapabilitySignals:   payload.CapabilitySignals,
		Contradictions:      payload.Contradictions,
		SelectionManifest:   payload.SelectionManifest,
		SelectionReason:     payload.SelectionReason,
		CanonicalJSON:       string(canonical),
	}
}

func TestWorkPatternTaskContextCompilesProductionSizedProfile(
	t *testing.T,
) {
	profile := productionSizedWorkPatternProfileFixture(t)
	candidate, ok, err := workPatternTaskContextCandidate(profile)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("production-sized profile did not produce a task context candidate")
	}
	if len(candidate.Content) > workPatternProfilePromptMaximumBytes {
		t.Fatalf(
			"compact profile=%d bytes, budget=%d",
			len(candidate.Content),
			workPatternProfilePromptMaximumBytes,
		)
	}
	for _, forbidden := range []string{
		"interval:04999",
		"session:00999",
		"projection:app:omitted-04999",
	} {
		if strings.Contains(candidate.Content, forbidden) {
			t.Fatalf("large source detail %q leaked into prompt projection", forbidden)
		}
	}
	var compact WorkPatternProfileContext
	if err := json.Unmarshal([]byte(candidate.Content), &compact); err != nil {
		t.Fatal(err)
	}
	if compact.SourceContextSHA256 != taskContextHash(profile.CanonicalJSON) ||
		len(compact.SelectionManifestSHA256) != 64 ||
		compact.SelectionSummary == nil ||
		compact.SelectionSummary.ManifestEntryCount !=
			len(profile.SelectionManifest) ||
		len(compact.SelectedProjections) != 1 ||
		len(compact.SelectedProjections[0].IntervalRefs) != 0 ||
		len(compact.SelectedProjections[0].SessionRefs) != 0 {
		t.Fatalf("compact profile lost required linkage: %+v", compact)
	}

	evidence, err := buildWorkPatternProfileSelectionEvidence(
		profile,
		compact,
		profile,
		candidate.Content,
		"episode-large-profile",
		2_000,
	)
	if err != nil {
		t.Fatal(err)
	}
	provenance, ok := decodeWorkPatternProfileSelectionEvidence(evidence)
	if !ok ||
		provenance.Version != workPatternProfileSelectionEvidenceVersion ||
		len(provenance.OnsetSelectionManifest) != len(profile.SelectionManifest) ||
		len(provenance.SelectionManifest) != len(profile.SelectionManifest) ||
		provenance.OnsetSourceContextSHA256 != taskContextHash(profile.CanonicalJSON) ||
		provenance.OnsetPromptProjectionSHA256 == "" ||
		provenance.OnsetPromptSelectionManifestSHA256 == "" ||
		provenance.PromptProjectionSHA256 !=
			taskContextHash(candidate.Content) {
		t.Fatalf("selection provenance=%+v ok=%v", provenance, ok)
	}
	selectedOnset := false
	omittedOnset := false
	for _, entry := range provenance.OnsetSelectionManifest {
		if entry.OnsetPromptIncluded == nil {
			t.Fatalf("onset prompt admission is unknown: %+v", entry)
		}
		if entry.Ref == "projection:global:all" &&
			*entry.OnsetPromptIncluded {
			selectedOnset = true
		}
		if entry.Ref == "projection:app:omitted-04999" &&
			!*entry.OnsetPromptIncluded &&
			entry.OnsetPromptReason == "not_admitted_to_onset_profile" {
			omittedOnset = true
		}
	}
	if !selectedOnset || !omittedOnset {
		t.Fatalf(
			"exact onset admission was not frozen: selected=%v omitted=%v",
			selectedOnset,
			omittedOnset,
		)
	}
}

func TestWorkPatternTaskContextNeutralProjectionCarriesNoInfluence(
	t *testing.T,
) {
	profile := neutralWorkPatternProfileContext(
		productionSizedWorkPatternProfileFixture(t),
	)
	candidate, ok, err := workPatternTaskContextCandidate(profile)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("neutral profile did not produce a provenance projection")
	}
	var compact WorkPatternProfileContext
	if err := json.Unmarshal([]byte(candidate.Content), &compact); err != nil {
		t.Fatal(err)
	}
	if compact.InfluenceMode != WorkPatternInfluenceNeutral ||
		len(compact.SelectedProjections) != 0 ||
		len(compact.SemanticSignals) != 0 ||
		len(compact.CapabilitySignals) != 0 ||
		len(compact.Contradictions) != 0 ||
		len(compact.SelectionManifest) != 0 ||
		compact.SelectionSummary == nil ||
		compact.SelectionSummary.ManifestEntryCount == 0 {
		t.Fatalf("neutral prompt projection contains influence: %+v", compact)
	}
	if len(candidate.Content) >= 2_000 {
		t.Fatalf("neutral prompt projection=%d bytes, want metadata only", len(candidate.Content))
	}
}

func TestWorkPatternSelectionEvidenceDistinguishesSourceAdmissionFromPromptUse(
	t *testing.T,
) {
	profile := productionSizedWorkPatternProfileFixture(t)
	compact, err := compileWorkPatternProfilePrompt(
		profile,
		900,
	)
	if err != nil {
		t.Fatal(err)
	}
	shadowCandidate, ok, err := workPatternTaskContextCandidate(profile)
	if err != nil || !ok {
		t.Fatalf("compile Shadow profile: ok=%v err=%v", ok, err)
	}
	evidence, err := buildWorkPatternProfileSelectionEvidence(
		profile,
		compact,
		profile,
		shadowCandidate.Content,
		"episode-stage-admission",
		2_000,
	)
	if err != nil {
		t.Fatal(err)
	}
	provenance, ok := decodeWorkPatternProfileSelectionEvidence(evidence)
	if !ok {
		t.Fatalf("decode exact stage admission: %+v", evidence)
	}
	var onsetIncluded, shadowIncluded *bool
	for _, entry := range provenance.OnsetSelectionManifest {
		if entry.Ref == "projection:global:all" {
			onsetIncluded = entry.OnsetPromptIncluded
		}
	}
	for _, entry := range provenance.SelectionManifest {
		if entry.Ref == "projection:global:all" {
			shadowIncluded = entry.ShadowContextIncluded
		}
	}
	if onsetIncluded == nil || shadowIncluded == nil ||
		*onsetIncluded || !*shadowIncluded {
		t.Fatalf(
			"stage admission collapsed source eligibility into prompt use: onset=%v shadow=%v",
			onsetIncluded,
			shadowIncluded,
		)
	}

	provenance.OnsetSelectionManifest[0].OnsetPromptReason = "tampered"
	payload, err := json.Marshal(provenance)
	if err != nil {
		t.Fatal(err)
	}
	evidence.PayloadJSON = string(payload)
	if _, ok := decodeWorkPatternProfileSelectionEvidence(evidence); ok {
		t.Fatal("tampered exact stage admission was accepted")
	}
}

func TestPersistTaskOnsetEpisodesFreezesOnlyPreStartContext(t *testing.T) {
	store := newTestStore(t)
	input := TaskOnsetInput{
		SourceKind:  "activity",
		SourceID:    "session-1",
		WindowStart: 100,
		CutoffAt:    130,
		Evidence: []TaskOnsetEvidence{
			{
				Ref:        "start",
				Kind:       "focus",
				SourceKind: "activity_event",
				SourceID:   "event-1",
				OccurredAt: 105,
				Summary:    "Focused the draft",
			},
			{
				Ref:        "later-result",
				Kind:       "keyboard",
				SourceKind: "activity_event",
				SourceID:   "event-2",
				OccurredAt: 125,
				Summary:    "Finished the answer",
			},
		},
	}
	response := TaskOnsetResponse{Episodes: []TaskOnsetPrediction{{
		LocalKey:           "draft",
		Task:               "Draft an answer",
		DesiredResult:      "A completed answer",
		InferredStartedAt:  105,
		TaskConfidence:     0.8,
		BoundaryConfidence: 0.7,
		ResultConfidence:   0.6,
		EvidenceRefs:       []string{"start", "later-result"},
	}}}
	results, err := PersistTaskOnsetEpisodes(store, input, response, 140, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || !results[0].Added {
		t.Fatalf("results = %+v", results)
	}
	episode := results[0].Episode
	if episode.FrozenContext.CutoffAt != 105 {
		t.Fatalf("cutoff = %d, want 105", episode.FrozenContext.CutoffAt)
	}
	if strings.Contains(episode.FrozenContext.CanonicalJSON, "Finished the answer") {
		t.Fatalf("post-start result leaked into frozen context: %s", episode.FrozenContext.CanonicalJSON)
	}
	detail, err := store.GetTaskEpisodeDetail(episode.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Evidence) != 2 {
		t.Fatalf("evidence count = %d, want both detection observations", len(detail.Evidence))
	}
	if episode.LatestHypothesis.BoundaryConfidence != 0.7 ||
		episode.LatestHypothesis.DesiredResultConfidence != 0.6 {
		t.Fatalf("confidence dimensions were not preserved: %+v", episode.LatestHypothesis)
	}
}

func TestPersistTaskOnsetEpisodesFreezesReasoningHypotheses(t *testing.T) {
	store := newTestStore(t)
	input := TaskOnsetInput{
		SourceKind:  "activity",
		SourceID:    "session-reasoning",
		WindowStart: 100,
		CutoffAt:    120,
		Evidence: []TaskOnsetEvidence{{
			Ref:        "reason",
			Kind:       "keyboard",
			SourceKind: "activity_event",
			SourceID:   "reason-event",
			OccurredAt: 105,
			Summary: `Entered text in Chat: "I am drafting this because the ` +
				`customer needs a concrete answer."`,
		}},
	}
	response := TaskOnsetResponse{Episodes: []TaskOnsetPrediction{{
		LocalKey:           "draft",
		Task:               "Draft a customer answer",
		DesiredResult:      "A concrete answer",
		InferredStartedAt:  105,
		TaskConfidence:     0.8,
		BoundaryConfidence: 0.7,
		ResultConfidence:   0.7,
		EvidenceRefs:       []string{"reason"},
		ReasoningHypotheses: []TaskOnsetReasoningHypothesis{{
			Statement:    "The customer needs a concrete answer.",
			Confidence:   0.86,
			Basis:        TaskReasoningBasisExplicitStatement,
			SourceQuote:  "because the customer needs a concrete answer",
			EvidenceRefs: []string{"reason"},
		}},
	}}}
	results, err := PersistTaskOnsetEpisodes(store, input, response, 130, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := results[0].Episode.LatestHypothesis.ReasoningHypotheses
	if len(got) != 1 || got[0].Basis != TaskReasoningBasisExplicitStatement ||
		got[0].SourceQuote == "" || len(got[0].EvidenceIDs) != 1 {
		t.Fatalf("persisted reasoning=%+v", got)
	}
	detail, err := store.GetTaskEpisodeDetail(results[0].Episode.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Hypotheses) != 1 ||
		len(detail.Hypotheses[0].ReasoningHypotheses) != 1 ||
		detail.Hypotheses[0].ReasoningHypotheses[0].EvidenceIDs[0] !=
			detail.Evidence[0].ID {
		t.Fatalf("reasoning provenance was not round-tripped: %+v", detail)
	}
}

func TestPersistTaskOnsetEpisodesSupportsOverlappingHypotheses(t *testing.T) {
	store := newTestStore(t)
	input := TaskOnsetInput{
		SourceKind:  "activity",
		SourceID:    "session-overlap",
		WindowStart: 100,
		CutoffAt:    120,
		Evidence: []TaskOnsetEvidence{
			{Ref: "mail", Kind: "focus", SourceKind: "event", SourceID: "1", OccurredAt: 105, Summary: "Focused email"},
			{Ref: "notes", Kind: "focus", SourceKind: "event", SourceID: "2", OccurredAt: 110, Summary: "Focused notes"},
		},
	}
	response := TaskOnsetResponse{Episodes: []TaskOnsetPrediction{
		{
			LocalKey: "reply", Task: "Reply to a customer", DesiredResult: "A sent reply",
			InferredStartedAt: 105, TaskConfidence: 0.8, BoundaryConfidence: 0.8,
			ResultConfidence: 0.7, EvidenceRefs: []string{"mail"},
		},
		{
			LocalKey: "plan", Task: "Plan follow-up work", DesiredResult: "A task list",
			InferredStartedAt: 110, TaskConfidence: 0.7, BoundaryConfidence: 0.6,
			ResultConfidence: 0.6, EvidenceRefs: []string{"notes"},
		},
	}}
	results, err := PersistTaskOnsetEpisodes(store, input, response, 130, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || results[0].Episode.ID == results[1].Episode.ID {
		t.Fatalf("overlapping episodes were not preserved: %+v", results)
	}
}

func TestPersistTaskOnsetEpisodesReusesExactAnchorWithoutMutatingContext(t *testing.T) {
	store := newTestStore(t)
	input := TaskOnsetInput{
		SourceKind: "activity", SourceID: "session-idempotent",
		WindowStart: 100, CutoffAt: 110,
		Evidence: []TaskOnsetEvidence{{
			Ref: "start", Kind: "focus", SourceKind: "event", SourceID: "1",
			OccurredAt: 105, Summary: "Focused a draft",
		}},
	}
	response := TaskOnsetResponse{Episodes: []TaskOnsetPrediction{{
		LocalKey: "draft", Task: "Draft", DesiredResult: "Draft complete",
		InferredStartedAt: 105, TaskConfidence: 0.8, BoundaryConfidence: 0.8,
		ResultConfidence: 0.7, EvidenceRefs: []string{"start"},
	}}}
	first, err := PersistTaskOnsetEpisodes(store, input, response, 120, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := PersistTaskOnsetEpisodes(store, input, response, 120, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !first[0].Added || second[0].Added || first[0].Episode.ID != second[0].Episode.ID {
		t.Fatalf("idempotency mismatch: first=%+v second=%+v", first, second)
	}

	changed := append([]TaskContextCandidate(nil), taskOnsetContextCandidates(input, response.Episodes[0], nil)...)
	changed = append(changed, taskContextCandidate(
		"new-prestart",
		TaskContextSectionKnowledge,
		"new immutable context",
		100,
	))
	reused, err := PersistTaskOnsetEpisodes(store, input, response, 120, changed)
	if err != nil {
		t.Fatalf("reuse exact onset anchor: %v", err)
	}
	if reused[0].Added || reused[0].Episode.ID != first[0].Episode.ID ||
		reused[0].Episode.FrozenContext.SHA256 !=
			first[0].Episode.FrozenContext.SHA256 {
		t.Fatalf("exact anchor changed immutable episode: %+v", reused[0])
	}
	detail, err := store.GetTaskEpisodeDetail(first[0].Episode.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Hypotheses) != 1 || len(detail.Evidence) != 1 {
		t.Fatalf("exact-anchor reuse appended history: %+v", detail)
	}
}

func TestPersistTaskOnsetEpisodesReusesHistoricalLegacyEpisode(t *testing.T) {
	store := newTestStore(t)
	evidence := TaskOnsetEvidence{
		Ref:        "legacy-start",
		Kind:       "keyboard",
		SourceKind: "event",
		SourceID:   "legacy-event-7",
		OccurredAt: 210,
		Summary:    "Entered a request",
	}
	payload, err := json.Marshal(evidence)
	if err != nil {
		t.Fatal(err)
	}
	legacyID := deterministicTaskEpisodeID("task-onset", "historical-key")
	legacy, added, err := store.CreateTaskEpisode(CreateTaskEpisodeInput{
		ID:              legacyID,
		InferredStartAt: 210,
		DetectedAt:      220,
		InitialStatus:   TaskEpisodeStatusCandidate,
		FrozenContext: TaskEpisodeFrozenContext{
			CutoffAt:      210,
			CanonicalJSON: `{}`,
			Manifest:      []TaskContextManifestEntry{},
		},
		InitialEvidence: []TaskEpisodeEvidence{{
			ID:          "legacy-onset-evidence",
			Role:        TaskEpisodeEvidenceOnset,
			SourceKind:  evidence.SourceKind,
			SourceID:    evidence.SourceID,
			Summary:     evidence.Summary,
			PayloadJSON: string(payload),
			ObservedAt:  evidence.OccurredAt,
		}},
		InitialHypothesis: TaskEpisodeHypothesisRevision{
			TaskHypothesis:          "Ask for an update",
			DesiredResultHypothesis: "A progress update",
			TaskConfidence:          0.8,
			BoundaryConfidence:      0.8,
			DesiredResultConfidence: 0.7,
			EvidenceIDs:             []string{"legacy-onset-evidence"},
		},
		CreatedAt: 220,
	})
	if err != nil || !added {
		t.Fatalf("create legacy episode: added=%v err=%v", added, err)
	}
	input := TaskOnsetInput{
		SourceKind:  "activity",
		SourceID:    "new-detector-window",
		WindowStart: 200,
		CutoffAt:    230,
		Evidence:    []TaskOnsetEvidence{evidence},
	}
	response := TaskOnsetResponse{Episodes: []TaskOnsetPrediction{{
		LocalKey:           "new-wording",
		Task:               "Compose a progress request",
		DesiredResult:      "A response from the assistant",
		InferredStartedAt:  210,
		TaskConfidence:     0.8,
		BoundaryConfidence: 0.8,
		ResultConfidence:   0.7,
		EvidenceRefs:       []string{evidence.Ref},
	}}}
	results, err := PersistTaskOnsetEpisodes(store, input, response, 240, nil)
	if err != nil || len(results) != 1 || results[0].Added ||
		results[0].Episode.ID != legacy.ID {
		t.Fatalf("legacy source anchor was not reused: results=%+v err=%v", results, err)
	}
}

func TestPersistTaskOnsetEpisodesDoesNotCoalesceUnanchoredPredictions(
	t *testing.T,
) {
	store := newTestStore(t)
	for index := 0; index < 2; index++ {
		input := TaskOnsetInput{
			SourceKind:  "activity",
			SourceID:    fmt.Sprintf("ambiguous-window-%d", index),
			WindowStart: 400,
			CutoffAt:    430,
			Evidence: []TaskOnsetEvidence{{
				Ref:        "earlier-context",
				Kind:       "focus",
				SourceKind: "event",
				SourceID:   "focus-event",
				OccurredAt: 405,
				Summary:    "Focused a document",
			}},
		}
		response := TaskOnsetResponse{Episodes: []TaskOnsetPrediction{{
			LocalKey:           fmt.Sprintf("ambiguous-%d", index),
			Task:               "Work on a document",
			DesiredResult:      "A changed document",
			InferredStartedAt:  410,
			TaskConfidence:     0.6,
			BoundaryConfidence: 0.5,
			ResultConfidence:   0.5,
			EvidenceRefs:       []string{"earlier-context"},
		}}}
		results, err := PersistTaskOnsetEpisodes(
			store,
			input,
			response,
			440,
			nil,
		)
		if err != nil || len(results) != 1 || !results[0].Added {
			t.Fatalf("ambiguous capture %d=%+v err=%v", index, results, err)
		}
	}
	episodes, err := store.ListTaskEpisodes("", 10)
	if err != nil || len(episodes) != 2 || episodes[0].ID == episodes[1].ID {
		t.Fatalf("unanchored predictions were coalesced: %+v err=%v", episodes, err)
	}
}

func TestPersistTaskOnsetEpisodesStoresContextSelectionManifest(t *testing.T) {
	store := newTestStore(t)
	input := taskOnsetFixture()
	response := TaskOnsetResponse{Episodes: []TaskOnsetPrediction{{
		LocalKey:           "manifest",
		Task:               "Draft the incident update",
		DesiredResult:      "A reviewed update",
		InferredStartedAt:  2_000,
		TaskConfidence:     0.8,
		BoundaryConfidence: 0.7,
		ResultConfidence:   0.7,
		EvidenceRefs:       []string{"event:1"},
	}}}
	results, err := PersistTaskOnsetEpisodesWithSources(
		store,
		input,
		response,
		4_100,
		TaskContextSourceResult{
			Candidates: []TaskContextCandidate{{
				Ref:             "self:boundary",
				Section:         TaskContextSectionBoundaries,
				SourceKind:      "self_model_entry",
				SourceID:        "boundary-1",
				ObservedAt:      1_000,
				Content:         "Never send an external update without authority.",
				EvidenceClass:   TaskContextEvidenceExplicitUser,
				PrivacyDecision: TaskContextPrivacyInclude,
				Relevance:       1,
			}},
			Exclusions: []TaskContextSourceExclusion{{
				Ref:        "shadow:hidden",
				SourceKind: "shadow_run",
				SourceID:   "shadow-1",
				ObservedAt: 1_500,
				Reason:     TaskContextSourceReasonHiddenShadow,
			}},
		},
	)
	if err != nil {
		t.Fatalf("PersistTaskOnsetEpisodesWithSources: %v", err)
	}
	manifest := results[0].Episode.FrozenContext.Manifest
	if len(manifest) < 2 {
		t.Fatalf("manifest = %#v", manifest)
	}
	foundHiddenExclusion := false
	foundBoundary := false
	for _, entry := range manifest {
		if entry.Ref == "shadow:hidden" &&
			!entry.Included &&
			entry.Reason == TaskContextSourceReasonHiddenShadow {
			foundHiddenExclusion = true
		}
		if entry.Ref == "self:boundary" && entry.Included {
			foundBoundary = true
		}
	}
	if !foundHiddenExclusion || !foundBoundary {
		t.Fatalf("manifest does not preserve selection audit: %#v", manifest)
	}
	if strings.Contains(results[0].Episode.FrozenContext.CanonicalJSON, "shadow:hidden") {
		t.Fatal("excluded shadow metadata leaked into worker context")
	}
}
