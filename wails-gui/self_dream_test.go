package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"yansu-agent/agent"
)

const selfDreamNoChangeResponse = `{
  "operations": [{
    "operation": "no_change",
    "targetEntryIds": [],
    "selfKind": "preference",
    "scope": {"kind": "global", "key": ""},
    "statement": "",
    "splitStatements": [],
    "reasoningStatus": "unknown",
    "rationale": "",
    "uncertainty": "No broader change is sufficiently supported.",
    "confidence": 1,
    "evidenceRefs": []
  }],
  "uncertainties": []
}`

type selfDreamFakeRunner struct {
	response string
	err      error
	inputs   []*agent.TaskInput
	entered  chan struct{}
	block    <-chan struct{}
}

func (r *selfDreamFakeRunner) Init(context.Context) error { return nil }
func (r *selfDreamFakeRunner) IsReady() bool              { return true }
func (r *selfDreamFakeRunner) ExecuteTask(
	ctx context.Context,
	input *agent.TaskInput,
	callback agent.StreamCallback,
) (string, error) {
	r.inputs = append(r.inputs, input)
	if r.entered != nil {
		select {
		case r.entered <- struct{}{}:
		default:
		}
	}
	if r.block != nil {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-r.block:
		}
	}
	if r.response != "" {
		if err := callback(&agent.StreamEvent{
			Type:    "assistant",
			Content: r.response,
		}); err != nil {
			return "", err
		}
	}
	return "self-dream-agent-run", r.err
}
func (r *selfDreamFakeRunner) CancelRun(string) error { return nil }
func (r *selfDreamFakeRunner) RespondToPermission(string, string) error {
	return nil
}
func (r *selfDreamFakeRunner) CancelPermission(string) {}
func (r *selfDreamFakeRunner) Shutdown() error         { return nil }

type selfDreamTestRig struct {
	app    *App
	store  *ActivityStore
	runner *selfDreamFakeRunner
}

func newSelfDreamTestRig(t *testing.T) selfDreamTestRig {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", root)
	store, err := NewActivityStore(filepath.Join(root, "activity"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close Self Dream activity store: %v", err)
		}
	})
	runner := &selfDreamFakeRunner{response: selfDreamNoChangeResponse}
	app := &App{
		runner: runner,
		activity: &activityMonitorState{
			store: store,
		},
	}
	return selfDreamTestRig{app: app, store: store, runner: runner}
}

func addSelfDreamTestEntry(
	t *testing.T,
	store *ActivityStore,
	statement string,
	createdAt int64,
) SelfModelEntry {
	t.Helper()
	entry, added, err := store.AddSelfModelEntry(SelfModelEntry{
		Kind:            selfModelKindPreference,
		Scope:           "project:yansu",
		Statement:       statement,
		Status:          selfModelStatusActive,
		Confidence:      0.9,
		ReasoningStatus: "explicit_user",
		Evidence: []SelfModelEvidence{{
			SourceKind: "explicit_user_statement",
			SourceID:   deterministicAuthorityID("self-dream-test", statement),
			Statement:  statement,
			Confidence: 1,
			OccurredAt: createdAt,
		}},
		CreatedAt: createdAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !added {
		t.Fatalf("Self Dream test entry %q was not added", statement)
	}
	return entry
}

func seedSelfDreamDuplicateEntries(
	t *testing.T,
	rig selfDreamTestRig,
) (SelfModelEntry, SelfModelEntry) {
	t.Helper()
	now := time.Now().Add(-time.Minute).UnixMilli()
	left := addSelfDreamTestEntry(
		t,
		rig.store,
		"Draft concise project updates.",
		now,
	)
	right := addSelfDreamTestEntry(
		t,
		rig.store,
		"Draft concise project updates",
		now+1,
	)
	return left, right
}

func TestSelfDreamMigrationCreatesImmutableLedgers(t *testing.T) {
	rig := newSelfDreamTestRig(t)
	for _, table := range []string{
		"self_dream_runs_v1",
		"self_dream_events_v1",
		"self_dream_attempts_v1",
		"self_dream_operations_v1",
		"self_dream_evaluations_v1",
		"self_dream_decisions_v1",
		"self_dream_compilations_v1",
		"self_dream_rollout_events_v1",
		"self_dream_replays_v1",
	} {
		ok, err := rig.store.hasTable(table)
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			t.Fatalf("missing Self Dream table %s", table)
		}
		for _, suffix := range []string{"no_update", "no_delete"} {
			ok, err := rig.store.hasTrigger(table + "_" + suffix)
			if err != nil {
				t.Fatal(err)
			}
			if !ok {
				t.Fatalf("missing immutable trigger %s_%s", table, suffix)
			}
		}
	}
	seedSelfDreamDuplicateEntries(t, rig)
	detail, err := rig.app.runSelfDreamShadow(
		context.Background(),
		"scheduled",
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rig.store.db.Exec(`
		UPDATE self_dream_runs_v1
		SET trigger_kind = 'manual'
		WHERE id = ?`,
		detail.Run.ID,
	); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("Self Dream run update error = %v", err)
	}
	if _, err := rig.store.db.Exec(`
		DELETE FROM self_dream_events_v1
		WHERE run_id = ?`,
		detail.Run.ID,
	); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("Self Dream event delete error = %v", err)
	}
}

func TestDetectDeterministicSelfDreamOperationsIsStrict(t *testing.T) {
	left := SelfModelEntry{
		ID: "left", Kind: selfModelKindPreference,
		Scope: "project:yansu", Statement: "Keep updates concise.",
		Status: selfModelStatusActive,
	}
	right := SelfModelEntry{
		ID: "right", Kind: selfModelKindPreference,
		Scope: "project:yansu", Statement: "Keep updates concise",
		Status: selfModelStatusActive,
	}
	manifest := []TaskContextManifestEntry{
		{
			Ref: "self:left", SourceKind: taskContextSourceSelfKind,
			SourceID: "left", Included: true,
		},
		{
			Ref: "self:right", SourceKind: taskContextSourceSelfKind,
			SourceID: "right", Included: true,
		},
	}
	drafts := detectDeterministicSelfDreamOperations(
		[]SelfModelEntry{left, right},
		manifest,
	)
	if len(drafts) != 1 ||
		drafts[0].Operation != SelfDreamOperationMerge {
		t.Fatalf("deterministic drafts = %+v", drafts)
	}
	operation := validateSelfDreamOperation(
		drafts[0],
		"deterministic",
		[]SelfModelEntry{left, right},
		manifest,
		time.Now().UnixMilli(),
	)
	if operation.ValidationStatus != SelfDreamValidationQualified ||
		!operation.AutoEligible {
		t.Fatalf("validated exact duplicate = %+v", operation)
	}
	right.Scope = "project:other"
	if got := detectDeterministicSelfDreamOperations(
		[]SelfModelEntry{left, right},
		manifest,
	); len(got) != 0 {
		t.Fatalf("cross-context duplicate was merged: %+v", got)
	}
	right.Scope = left.Scope
	right.Contradictions = []SelfModelEvidence{{
		SourceKind: "explicit_correction",
		SourceID:   "correction-1",
	}}
	if got := detectDeterministicSelfDreamOperations(
		[]SelfModelEntry{left, right},
		manifest,
	); len(got) != 0 {
		t.Fatalf("contradicted duplicate was merged: %+v", got)
	}
}

func TestValidateSelfDreamOperationRejectsUnsupportedReasoningAndAuthority(
	t *testing.T,
) {
	entry := SelfModelEntry{
		ID: "entry-1", Kind: selfModelKindDecisionPolicy,
		Statement: "Review material changes before release.",
		Status:    selfModelStatusActive,
	}
	manifest := []TaskContextManifestEntry{{
		Ref:           "memory:one",
		SourceKind:    taskContextSourceMemoryKind,
		SourceID:      "one",
		EvidenceClass: TaskContextEvidenceObservedUser,
		Included:      true,
	}}
	draft := SelfDreamOperationDraft{
		Operation:       SelfDreamOperationSupersede,
		TargetEntryIDs:  []string{entry.ID},
		SelfKind:        entry.Kind,
		Scope:           SelfDreamScope{Kind: "global"},
		Statement:       entry.Statement,
		ReasoningStatus: "explicit",
		Confidence:      0.8,
		EvidenceRefs:    []string{"memory:one"},
	}
	operation := validateSelfDreamOperation(
		draft,
		"model",
		[]SelfModelEntry{entry},
		manifest,
		time.Now().UnixMilli(),
	)
	if operation.ValidationReason !=
		"explicit_reasoning_requires_explicit_evidence" {
		t.Fatalf("unsupported reasoning validation = %+v", operation)
	}
	draft.ReasoningStatus = "unknown"
	draft.Statement = "Grant authority to execute without approval."
	operation = validateSelfDreamOperation(
		draft,
		"model",
		[]SelfModelEntry{entry},
		manifest,
		time.Now().UnixMilli(),
	)
	if operation.ValidationReason != "authority_mutation_is_out_of_scope" {
		t.Fatalf("authority validation = %+v", operation)
	}
}

func TestFilterSelfDreamCandidatesRecordsEveryContainmentReason(t *testing.T) {
	const cutoff = int64(1_000)
	candidates := []TaskContextCandidate{
		{
			Ref:             "memory:allowed",
			SourceKind:      taskContextSourceMemoryKind,
			SourceID:        "allowed",
			ObservedAt:      900,
			EvidenceClass:   TaskContextEvidenceExplicitUser,
			PrivacyDecision: TaskContextPrivacyRedact,
		},
		{
			Ref:             "memory:future",
			SourceKind:      taskContextSourceMemoryKind,
			SourceID:        "future",
			ObservedAt:      1_001,
			EvidenceClass:   TaskContextEvidenceExplicitUser,
			PrivacyDecision: TaskContextPrivacyRedact,
		},
		{
			Ref:             "activity:unsupported",
			SourceKind:      "activity",
			SourceID:        "unsupported",
			ObservedAt:      900,
			EvidenceClass:   TaskContextEvidenceObservedUser,
			PrivacyDecision: TaskContextPrivacyRedact,
		},
		{
			Ref:             "memory:hidden-shadow",
			SourceKind:      taskContextSourceMemoryKind,
			SourceID:        "hidden-shadow",
			ObservedAt:      900,
			EvidenceClass:   TaskContextEvidenceHiddenShadow,
			PrivacyDecision: TaskContextPrivacyRedact,
		},
		{
			Ref:             "knowledge:agent-output",
			SourceKind:      taskContextSourceKnowledgeKind,
			SourceID:        "agent-output",
			ObservedAt:      900,
			EvidenceClass:   TaskContextEvidenceAgentGenerated,
			PrivacyDecision: TaskContextPrivacyRedact,
		},
		{
			Ref:             "memory:observed-context",
			SourceKind:      taskContextSourceMemoryKind,
			SourceID:        "observed-context",
			ObservedAt:      900,
			EvidenceClass:   TaskContextEvidenceObservedContext,
			PrivacyDecision: TaskContextPrivacyRedact,
		},
		{
			Ref:             "memory:private",
			SourceKind:      taskContextSourceMemoryKind,
			SourceID:        "private",
			ObservedAt:      900,
			EvidenceClass:   TaskContextEvidenceExplicitUser,
			PrivacyDecision: TaskContextPrivacyExclude,
		},
	}

	allowed, excluded := filterSelfDreamCandidates(candidates, cutoff)
	if len(allowed) != 1 || allowed[0].Ref != "memory:allowed" {
		t.Fatalf("allowed candidates = %+v", allowed)
	}
	reasons := make(map[string]string)
	for _, item := range excluded {
		reasons[item.Ref] = item.Reason
	}
	for ref, reason := range map[string]string{
		"memory:future":           TaskContextSourceReasonFuture,
		"activity:unsupported":    "self_dream_source_class_excluded",
		"memory:hidden-shadow":    "self_dream_contamination_excluded",
		"knowledge:agent-output":  "self_dream_contamination_excluded",
		"memory:observed-context": "self_dream_contamination_excluded",
		"memory:private":          TaskContextSourceReasonPrivacy,
	} {
		if reasons[ref] != reason {
			t.Fatalf("reason[%s] = %q, want %q", ref, reasons[ref], reason)
		}
	}
}

func TestSelfDreamScopeContractAndStrictParser(t *testing.T) {
	if scope, err := normalizeSelfDreamScope(SelfDreamScope{
		Kind: "global",
	}); err != nil || scope.Key != "" {
		t.Fatalf("global scope = %+v err=%v", scope, err)
	}
	for _, kind := range []string{
		"app",
		"app_transition",
		"work_mode",
		"capability",
		"project",
		"relationship",
	} {
		scope, err := normalizeSelfDreamScope(SelfDreamScope{
			Kind: kind,
			Key:  "qualified-context",
		})
		if err != nil || scope.Kind != kind ||
			scope.Key != "qualified-context" {
			t.Fatalf("%s scope = %+v err=%v", kind, scope, err)
		}
	}
	if _, err := normalizeSelfDreamScope(SelfDreamScope{
		Kind: "global",
		Key:  "not-allowed",
	}); err == nil {
		t.Fatal("global scope accepted a contextual key")
	}
	if _, err := normalizeSelfDreamScope(SelfDreamScope{
		Kind: "project",
	}); err == nil {
		t.Fatal("contextual scope accepted an empty key")
	}

	if _, err := decodeSelfDreamResponse(
		selfDreamNoChangeResponse,
	); err != nil {
		t.Fatalf("strict production response failed: %v", err)
	}
	for name, raw := range map[string]string{
		"unknown field": `{
			"operations": [{
				"operation": "no_change",
				"targetEntryIds": [],
				"selfKind": "preference",
				"scope": {"kind": "global"},
				"reasoningStatus": "unknown",
				"confidence": 1,
				"evidenceRefs": [],
				"unexpected": true
			}],
			"uncertainties": []
		}`,
		"trailing value":   selfDreamNoChangeResponse + ` {}`,
		"empty operations": `{"operations":[],"uncertainties":[]}`,
	} {
		if _, err := decodeSelfDreamResponse(raw); err == nil {
			t.Fatalf("parser accepted %s", name)
		}
	}
}

func TestSelfDreamShadowRunFreezesContextWithoutMutatingSelf(t *testing.T) {
	rig := newSelfDreamTestRig(t)
	left, right := seedSelfDreamDuplicateEntries(t, rig)
	before, err := rig.store.ListCurrentSelfModelEntries()
	if err != nil {
		t.Fatal(err)
	}
	detail, err := rig.app.runSelfDreamShadow(
		context.Background(),
		"scheduled",
	)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Status != "completed" || detail.Attempt == nil {
		t.Fatalf("Self Dream detail = %+v", detail)
	}
	if detail.Attempt.ProductMutation ||
		detail.Attempt.LearningEligible ||
		detail.Attempt.SafetyMode != SelfDreamSafetyMode {
		t.Fatalf("unsafe Self Dream attempt = %+v", detail.Attempt)
	}
	if len(rig.runner.inputs) != 1 {
		t.Fatalf("model calls = %d", len(rig.runner.inputs))
	}
	input := rig.runner.inputs[0]
	if !input.NoTools || !input.NoSystem || !input.Ephemeral ||
		!input.DenyExternalWrites || !input.SuppressTelemetry ||
		input.BackendHint != agent.BackendHintLearning {
		t.Fatalf("unsafe model input = %+v", input)
	}
	after, err := rig.store.ListCurrentSelfModelEntries()
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("shadow run mutated canonical Self: %d -> %d", len(before), len(after))
	}
	var merge *SelfDreamOperationRecord
	for index := range detail.Operations {
		if detail.Operations[index].Operation == SelfDreamOperationMerge {
			merge = &detail.Operations[index]
			break
		}
	}
	if merge == nil || !merge.AutoEligible {
		t.Fatalf("missing exact merge operation: %+v", detail.Operations)
	}
	if len(detail.Run.Manifest) == 0 ||
		detail.Run.ContextHash == "" ||
		detail.Run.InputHash == "" {
		t.Fatalf("incomplete frozen input: %+v", detail.Run)
	}
	targets := strings.Join(merge.TargetEntryIDs, " ")
	if !strings.Contains(targets, left.ID) ||
		!strings.Contains(targets, right.ID) {
		t.Fatalf("merge targets = %v", merge.TargetEntryIDs)
	}
}

func TestSelfDreamAnalysisLearningHintRoutesNativeWithAssistantPick(t *testing.T) {
	for _, pick := range []string{"claude-code", "codex"} {
		t.Run(pick, func(t *testing.T) {
			nativeRunner := &selfDreamFakeRunner{response: selfDreamNoChangeResponse}
			assistant := &selfDreamFakeRunner{response: selfDreamNoChangeResponse}
			router := agent.NewOrderedRoutingAgentRunner(
				nativeRunner,
				map[string]agent.AgentRunner{pick: assistant},
				agent.RoutingPolicy{
					ManagedOrder: []string{pick},
					Assistants: map[string]agent.AssistantState{
						pick: {SignedIn: true, Available: true, ControlProfile: true},
					},
				},
			)
			// Chat follows the assistant the user picked.
			if _, err := router.ExecuteTask(context.Background(), &agent.TaskInput{
				Prompt:      "chat turn",
				BackendHint: agent.BackendHintChat,
			}, nil); err != nil {
				t.Fatal(err)
			}
			if len(assistant.inputs) != 1 || len(nativeRunner.inputs) != 0 {
				t.Fatalf(
					"chat did not use the picked assistant: assistant=%d native=%d",
					len(assistant.inputs),
					len(nativeRunner.inputs),
				)
			}
			// The nightly Self Dream still stays on Isoform's own runner.
			if _, _, err := runSelfDreamAnalysis(
				context.Background(),
				router,
				"test-model",
				"self dream prompt",
			); err != nil {
				t.Fatal(err)
			}
			if len(assistant.inputs) != 1 || len(nativeRunner.inputs) != 1 {
				t.Fatalf(
					"Self Dream escaped the native runner: assistant=%d native=%d",
					len(assistant.inputs),
					len(nativeRunner.inputs),
				)
			}
			if input := nativeRunner.inputs[0]; input.BackendHint != agent.BackendHintLearning {
				t.Fatalf("Self Dream task dropped the learning hint: %+v", input)
			}
		})
	}
}

func TestReviewSelfDreamOperationCompilesAppendOnlyRevision(t *testing.T) {
	rig := newSelfDreamTestRig(t)
	left, right := seedSelfDreamDuplicateEntries(t, rig)
	detail, err := rig.app.runSelfDreamShadow(
		context.Background(),
		"scheduled",
	)
	if err != nil {
		t.Fatal(err)
	}
	var merge SelfDreamOperationRecord
	for _, operation := range detail.Operations {
		if operation.Operation == SelfDreamOperationMerge {
			merge = operation
		}
	}
	if merge.ID == "" {
		t.Fatal("missing merge operation")
	}
	reviewed, err := rig.app.ReviewSelfDreamOperation(
		ReviewSelfDreamOperationInput{
			OperationID:    merge.ID,
			IdempotencyKey: "review-merge-1",
			Decision:       SelfDreamDecisionAccept,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(reviewed.Compilations) != 1 {
		t.Fatalf("compilations = %+v", reviewed.Compilations)
	}
	for _, sourceID := range []string{left.ID, right.ID} {
		source, err := rig.store.GetSelfModelEntryByID(sourceID)
		if err != nil {
			t.Fatal(err)
		}
		if source.Status != selfModelStatusActive {
			t.Fatalf("historical source %s was rewritten: %+v", sourceID, source)
		}
		latest, err := rig.store.GetLatestSelfModelEntry(source.EntryKey)
		if err != nil {
			t.Fatal(err)
		}
		if latest.Status != selfModelStatusRetracted ||
			latest.Revision != source.Revision+1 {
			t.Fatalf("source revision was not appended: %+v", latest)
		}
	}
	outputID := reviewed.Compilations[0].OutputEntryIDs[0]
	output, err := rig.store.GetSelfModelEntryByID(outputID)
	if err != nil {
		t.Fatal(err)
	}
	if output.Status != selfModelStatusActive ||
		output.ReasoningStatus != "user_confirmed" ||
		len(output.Evidence) < 2 {
		t.Fatalf("compiled canonical Self = %+v", output)
	}
}

func TestReviewSelfDreamOperationCorrectionTakesPrecedence(t *testing.T) {
	rig := newSelfDreamTestRig(t)
	seedSelfDreamDuplicateEntries(t, rig)
	detail, err := rig.app.runSelfDreamShadow(
		context.Background(),
		"scheduled",
	)
	if err != nil {
		t.Fatal(err)
	}
	var merge SelfDreamOperationRecord
	for _, operation := range detail.Operations {
		if operation.Operation == SelfDreamOperationMerge {
			merge = operation
		}
	}
	reviewed, err := rig.app.ReviewSelfDreamOperation(
		ReviewSelfDreamOperationInput{
			OperationID:    merge.ID,
			IdempotencyKey: "correct-merge-1",
			Decision:       SelfDreamDecisionCorrect,
			Note:           "Use a detailed update when a decision needs explicit tradeoffs.",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(reviewed.Compilations) != 1 ||
		reviewed.Compilations[0].CompilationKind !=
			SelfDreamOperationContradict {
		t.Fatalf("correction compilation = %+v", reviewed.Compilations)
	}
	output, err := rig.store.GetSelfModelEntryByID(
		reviewed.Compilations[0].OutputEntryIDs[0],
	)
	if err != nil {
		t.Fatal(err)
	}
	if output.ReasoningStatus != "self_dream_user_correction" ||
		len(output.Contradictions) == 0 {
		t.Fatalf("correction did not become contradiction evidence: %+v", output)
	}
}

func TestSelfDreamCompilationFailsClosedOnStaleTarget(t *testing.T) {
	rig := newSelfDreamTestRig(t)
	left, _ := seedSelfDreamDuplicateEntries(t, rig)
	detail, err := rig.app.runSelfDreamShadow(
		context.Background(),
		"scheduled",
	)
	if err != nil {
		t.Fatal(err)
	}
	var merge SelfDreamOperationRecord
	for _, operation := range detail.Operations {
		if operation.Operation == SelfDreamOperationMerge {
			merge = operation
		}
	}
	left.Confidence = 0.99
	left.Evidence = append(left.Evidence, SelfModelEvidence{
		SourceKind: "explicit_user_statement",
		SourceID:   "newer-source",
		Statement:  "Newer source",
		Confidence: 1,
		OccurredAt: time.Now().UnixMilli(),
	})
	left.CreatedAt = time.Now().UnixMilli()
	if _, added, err := rig.store.AddSelfModelEntry(left); err != nil || !added {
		t.Fatalf("append newer Self revision: added=%v err=%v", added, err)
	}
	_, err = rig.app.ReviewSelfDreamOperation(
		ReviewSelfDreamOperationInput{
			OperationID:    merge.ID,
			IdempotencyKey: "stale-review-1",
			Decision:       SelfDreamDecisionAccept,
		},
	)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "stale") {
		t.Fatalf("stale review error = %v", err)
	}
	if _, err := rig.store.GetSelfDreamCompilation(merge.ID); !errors.Is(
		err,
		sql.ErrNoRows,
	) {
		t.Fatalf("stale compilation persisted: %v", err)
	}
}

func TestReplaySelfDreamDebugIsExactAndMutationFree(t *testing.T) {
	rig := newSelfDreamTestRig(t)
	seedSelfDreamDuplicateEntries(t, rig)
	detail, err := rig.app.runSelfDreamShadow(
		context.Background(),
		"scheduled",
	)
	if err != nil {
		t.Fatal(err)
	}
	before, err := rig.store.ListCurrentSelfModelEntries()
	if err != nil {
		t.Fatal(err)
	}
	result, err := rig.app.ReplaySelfDreamDebug(
		ReplaySelfDreamDebugInput{
			RunID:  detail.Run.ID,
			Prompt: detail.Run.Prompt,
			Model:  "gpt-5.6-sol",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Replay.ContextMatchesSource ||
		result.Replay.ProductMutation ||
		result.Replay.LearningEligible ||
		result.Replay.SafetyMode != SelfDreamSafetyMode ||
		result.Replay.PromptVersion != SelfDreamPromptVersion {
		t.Fatalf("unsafe Self Dream replay = %+v", result.Replay)
	}
	after, err := rig.store.ListCurrentSelfModelEntries()
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("replay mutated canonical Self: %d -> %d", len(before), len(after))
	}
	replays, err := rig.store.ListSelfDreamReplays(detail.Run.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(replays) != 1 {
		t.Fatalf("replays = %+v", replays)
	}
	lastInput := rig.runner.inputs[len(rig.runner.inputs)-1]
	if !lastInput.NoTools || !lastInput.DenyExternalWrites ||
		!lastInput.Ephemeral {
		t.Fatalf("unsafe replay input = %+v", lastInput)
	}
}

func TestSelfDreamInputCacheAvoidsRepeatedModelCalls(t *testing.T) {
	rig := newSelfDreamTestRig(t)
	seedSelfDreamDuplicateEntries(t, rig)
	first, err := rig.app.runSelfDreamShadow(
		context.Background(),
		"scheduled",
	)
	if err != nil {
		t.Fatal(err)
	}
	second, err := rig.app.runSelfDreamShadow(
		context.Background(),
		"scheduled",
	)
	if err != nil {
		t.Fatal(err)
	}
	if first.Run.ID != second.Run.ID {
		t.Fatalf("cache miss: %s != %s", first.Run.ID, second.Run.ID)
	}
	if len(rig.runner.inputs) != 1 {
		t.Fatalf("cached model calls = %d", len(rig.runner.inputs))
	}
}

func TestSelfDreamSchedulerIsSingleFlightRateLimitedAndCancellable(
	t *testing.T,
) {
	rig := newSelfDreamTestRig(t)
	seedSelfDreamDuplicateEntries(t, rig)
	service := NewSelfDreamService(rig.app)
	parent, cancelParent := context.WithCancel(context.Background())
	defer cancelParent()

	firstRelease := make(chan struct{})
	rig.runner.entered = make(chan struct{}, 1)
	rig.runner.block = firstRelease
	service.schedule(parent)
	select {
	case <-rig.runner.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("scheduled Self Dream run did not start")
	}
	service.schedule(parent)
	close(firstRelease)
	waitSelfDreamServiceIdle(t, service)
	if len(rig.runner.inputs) != 1 {
		t.Fatalf("single-flight model calls = %d", len(rig.runner.inputs))
	}

	service.schedule(parent)
	time.Sleep(25 * time.Millisecond)
	if len(rig.runner.inputs) != 1 {
		t.Fatalf("minimum interval allowed another model call: %d", len(rig.runner.inputs))
	}

	addSelfDreamTestEntry(
		t,
		rig.store,
		"Review customer-facing changes before release.",
		time.Now().UnixMilli(),
	)
	secondRelease := make(chan struct{})
	rig.runner.entered = make(chan struct{}, 1)
	rig.runner.block = secondRelease
	service.mu.Lock()
	service.lastStart = time.Time{}
	service.mu.Unlock()
	service.schedule(parent)
	select {
	case <-rig.runner.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("cancellable Self Dream run did not start")
	}
	service.Stop()
	waitSelfDreamServiceIdle(t, service)
	if len(rig.runner.inputs) != 2 {
		t.Fatalf("cancellation run count = %d", len(rig.runner.inputs))
	}
}

func waitSelfDreamServiceIdle(t *testing.T, service *SelfDreamService) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		service.mu.Lock()
		running := service.running
		service.mu.Unlock()
		if !running {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("Self Dream service did not become idle")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestSelfDreamRolloutRequiresRealPairedEvidence(t *testing.T) {
	rig := newSelfDreamTestRig(t)
	now := time.Now().Add(-time.Hour).UnixMilli()
	for index := 0; index < 20; index++ {
		base := []SelfModelEntry{}
		baseJSON, _ := json.Marshal(base)
		runID := deterministicAuthorityID(
			"self-dream-rollout-run",
			string(rune(index)),
		)
		run, added, err := rig.store.AddSelfDreamRun(SelfDreamRunRecord{
			ID:               runID,
			IdempotencyKey:   "rollout:" + runID,
			TriggerKind:      "scheduled",
			PolicyVersion:    SelfDreamPolicyVersion,
			AssemblerVersion: SelfDreamAssemblerVersion,
			SourceCutoffAt:   now + int64(index),
			BaseSelf:         base,
			BaseSelfHash:     sha256Hex(baseJSON),
			Context: AssembledTaskContext{
				CanonicalJSON: "{}",
				SHA256:        sha256Hex([]byte("{}")),
			},
			ContextHash:   sha256Hex([]byte("{}")),
			Manifest:      []TaskContextManifestEntry{},
			InputHash:     runID,
			PromptVersion: SelfDreamPromptVersion,
			Prompt:        "test",
			PromptHash:    sha256Hex([]byte("test")),
			CreatedAt:     now + int64(index),
		})
		if err != nil || !added {
			t.Fatalf("add rollout run %d: added=%v err=%v", index, added, err)
		}
		if _, err := rig.store.AddSelfDreamEvent(SelfDreamEventRecord{
			RunID:      run.ID,
			EventKind:  "completed",
			DetailJSON: "{}",
			CreatedAt:  now + int64(index),
		}); err != nil {
			t.Fatal(err)
		}
		evaluation := SelfDreamEvaluationRecord{
			ID:                "evaluation:" + run.ID,
			RunID:             run.ID,
			EvaluationVersion: SelfDreamEvaluationVersion,
			Neutral: SelfDreamProjectionMetrics{
				EntryCount:       2,
				EvidenceCoverage: 1,
				Quality:          0.8,
			},
			Dream: SelfDreamProjectionMetrics{
				EntryCount:       1,
				EvidenceCoverage: 1,
				Quality:          0.85,
			},
			Improvement:  0.05,
			RealEvidence: true,
			CreatedAt:    now + int64(index),
		}
		if _, err := rig.store.AddSelfDreamEvaluation(evaluation); err != nil {
			t.Fatal(err)
		}
	}
	status, err := rig.store.SelfDreamRolloutStatus()
	if err != nil {
		t.Fatal(err)
	}
	if !status.Ready || status.Enabled {
		t.Fatalf("pre-enable rollout = %+v", status)
	}
	if err := rig.store.setSelfDreamRollout(
		true,
		"Reviewed 20 real paired evaluations.",
	); err != nil {
		t.Fatal(err)
	}
	status, err = rig.store.SelfDreamRolloutStatus()
	if err != nil {
		t.Fatal(err)
	}
	if !status.Ready || !status.Enabled ||
		status.CompletedRealRuns != 20 ||
		status.MeanImprovement < 0.049 {
		t.Fatalf("enabled rollout = %+v", status)
	}
}
