package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"yansu-agent/agent/authority"
)

func addQualificationEvidence(
	t *testing.T,
	store *ActivityStore,
	index int,
	action authority.Action,
) ShadowRun {
	t.Helper()
	suffix := fmt.Sprintf("%02d", index)
	run := newShadowRunFixture(
		"shadow-qualification-run-"+suffix,
		"shadow-qualification-decision-"+suffix,
	)
	run.SourceID = "qualification-prediction-" + suffix
	run.Context.PromptContext = "Qualification context " + suffix
	run.Context.References[0].SourceID = run.SourceID
	run.CreatedAt += int64(index * 10_000)
	run.Context.CapturedAt = run.CreatedAt
	run.Context.ProductionEventAt = run.CreatedAt - 100
	run.Context.References[0].ObservedAt = run.CreatedAt - 100
	run.ObservationHorizonStart = run.CreatedAt
	run.ObservationHorizonEnd = run.CreatedAt + shadowObservationWindow.Milliseconds()
	run.Prediction.CandidateExecutionPlan = &ShadowCandidateExecutionPlan{
		Kind:           "automation",
		Name:           "draft-customer-follow-up",
		Description:    "Draft a customer follow-up.",
		Prompt:         "Review the customer context and prepare the next follow-up draft.",
		ScheduleKind:   "manual",
		ScheduleConfig: json.RawMessage(`{}`),
		Confidence:     0.9,
	}
	createdRun, added, err := store.CreateShadowRun(run)
	if err != nil || !added {
		t.Fatalf("create qualification run %d: added=%v err=%v", index, added, err)
	}
	observation, added, err := store.AddShadowRunObservation(ShadowRunObservation{
		ShadowRunID:    createdRun.ID,
		SourceKind:     "activity",
		SourceID:       "qualification-observation-" + suffix,
		ObservedAction: "Prepared customer follow-up " + suffix,
		ObservedResult: "A draft was ready for review.",
		ObservedAt:     createdRun.CreatedAt + 1_000,
		CreatedAt:      createdRun.CreatedAt + 1_100,
	})
	if err != nil || !added {
		t.Fatalf("create qualification observation %d: added=%v err=%v", index, added, err)
	}
	_, added, err = store.AddShadowRunEvaluation(ShadowRunEvaluation{
		ShadowRunID:       createdRun.ID,
		ObservationID:     observation.ID,
		IdempotencyKey:    "qualification-evaluation-" + suffix,
		ComparisonVersion: shadowCorrelationPromptVersion,
		EvaluatorModel:    "gpt-5.6-terra",
		Action:            ShadowEvaluationDimension{Assessment: "matched", Score: shadowScore(1), Confidence: 0.9},
		Outcome:           ShadowEvaluationDimension{Assessment: "matched", Score: shadowScore(1), Confidence: 0.9},
		Timing:            ShadowEvaluationDimension{Assessment: "matched", Score: shadowScore(1), Confidence: 0.8},
		Reasoning:         ShadowEvaluationDimension{Assessment: "unknown", Confidence: 0.2},
		Safety:            ShadowEvaluationDimension{Assessment: "safe", Score: shadowScore(1), Confidence: 0.9},
		CreatedAt:         createdRun.CreatedAt + 1_200,
	})
	if err != nil || !added {
		t.Fatalf("create qualification evaluation %d: added=%v err=%v", index, added, err)
	}

	evaluation := authority.NewPolicy(nil).Evaluate(context.Background(), authority.Context{
		RunID:      "qualification-agent-" + suffix,
		ShadowRun:  createdRun.ID,
		Mode:       authority.ExecutionModeShadow,
		Unattended: true,
	}, action)
	_, added, err = store.AddShadowExecutionAttempt(ShadowExecutionAttempt{
		ShadowRunID:    createdRun.ID,
		IdempotencyKey: createdRun.ID + ":" + shadowExecutionRuntimeVersion,
		RuntimeVersion: shadowExecutionRuntimeVersion,
		Status:         "completed",
		PolicyVersion:  authority.ShadowPolicyVersion,
		AgentRunID:     "qualification-agent-" + suffix,
		Model:          "gpt-5.6-sol",
		Prompt:         createdRun.Prediction.CandidateExecutionPlan.Prompt,
		Plan:           *createdRun.Prediction.CandidateExecutionPlan,
		Decisions:      []authority.Evaluation{evaluation},
		StartedAt:      createdRun.CreatedAt + 1_300,
		CompletedAt:    createdRun.CreatedAt + 1_400,
		CreatedAt:      createdRun.CreatedAt + 1_400,
	})
	if err != nil || !added {
		t.Fatalf("create qualification runtime %d: added=%v err=%v", index, added, err)
	}
	return createdRun
}

func rebuildAndQualify(
	t *testing.T,
	store *ActivityStore,
	createdAt int64,
) (ShadowCapabilityProfile, ShadowCapabilityQualification) {
	t.Helper()
	profile, _, err := store.RebuildShadowCapabilityProfile(
		"customer.follow_up.draft",
		createdAt,
	)
	if err != nil {
		t.Fatal(err)
	}
	app := &App{activity: &activityMonitorState{store: store}}
	app.qualifyShadowCapability(profile)
	qualification, err := store.GetLatestShadowCapabilityQualification(profile.CapabilityKey)
	if err != nil {
		t.Fatal(err)
	}
	return profile, qualification
}

func saveLegacyQualifiedProposalForTest(
	t *testing.T,
	store *ActivityStore,
	qualification ShadowCapabilityQualification,
) (ShadowCapabilityQualification, Initiative) {
	t.Helper()
	run, err := store.GetShadowRun(
		qualification.RepresentativeShadowRunID,
	)
	if err != nil {
		t.Fatal(err)
	}
	plan := run.Prediction.CandidateExecutionPlan
	if plan == nil {
		t.Fatal("qualification has no candidate execution plan")
	}
	legacyQualification := qualification
	legacyQualification.ID = "legacy-qualification-" +
		qualification.QualificationKey[:24]
	legacyQualification.QualificationKey = sha256Hex([]byte(
		qualification.QualificationKey + "\x00legacy-compatibility",
	))

	legacyQualification.Status = "proposed"
	legacyQualification.ProposalInitiativeID = "legacy-proposal-" +
		qualification.QualificationKey[:24]
	legacyQualification.CreatedAt++
	persistedQualification, added, err :=
		store.AddShadowCapabilityQualification(legacyQualification)
	if err != nil || !added {
		t.Fatalf(
			"save legacy qualification: added=%v err=%v",
			added,
			err,
		)
	}
	reversibility := "reversible"
	for _, effect := range qualification.Effects {
		if effect.Effect != authority.EffectRead && !effect.Reversible {
			reversibility = "irreversible"
			break
		}
	}
	confidence := qualification.Actual.ActionReliability
	if confidence <= 0 {
		confidence = run.Prediction.ActionConfidence
	}
	description := strings.TrimSpace(plan.Description)
	if description == "" {
		description = strings.TrimSpace(run.Prediction.ExpectedResult)
	}
	proposal := Initiative{
		ID:              persistedQualification.ProposalInitiativeID,
		Kind:            "automation",
		Status:          InitiativeStatusProposed,
		AutonomyMode:    InitiativeModeApproval,
		SourceKind:      "shadow_qualification",
		SourceID:        persistedQualification.ID,
		Title:           strings.TrimSpace(plan.Name),
		Description:     description,
		Goal:            strings.TrimSpace(run.Prediction.Goal),
		UserIntent:      strings.TrimSpace(run.Prediction.Goal),
		PredictedAction: strings.TrimSpace(run.Prediction.PredictedAction),
		Rationale:       strings.TrimSpace(run.Prediction.EvidenceRationale),
		Confidence:      clampConfidence(confidence),
		RiskLevel:       qualification.RiskTier,
		Reversibility:   reversibility,
		Evidence: []InitiativeEvidence{{
			Kind:       "shadow_qualification",
			SourceKind: "shadow_run",
			SourceID:   run.ID,
			Summary:    "Historical proposal compatibility fixture.",
			OccurredAt: persistedQualification.CreatedAt,
		}},
		ExecutionPlan: InitiativeExecutionPlan{
			Kind: "automation",
			Automation: &AutomationExecutionPlan{
				Name:         plan.Name,
				Description:  plan.Description,
				Prompt:       plan.Prompt,
				ScheduleKind: plan.ScheduleKind,
				ScheduleConfig: append(
					json.RawMessage(nil),
					plan.ScheduleConfig...,
				),
				ScheduleEvidence: plan.ScheduleEvidence,
			},
		},
		Fingerprint: qualifiedInitiativeFingerprint(
			qualification.CapabilityKey,
			qualification.CandidatePlanHash,
		),
		MemoryState: InitiativeMemoryUnverified,
		CreatedAt:   persistedQualification.CreatedAt,
		UpdatedAt:   persistedQualification.CreatedAt,
	}
	if err := store.SaveInitiative(proposal); err != nil {
		t.Fatal(err)
	}
	if _, err := store.EnsureCapabilityManifest(
		persistedQualification,
		proposal,
	); err != nil {
		t.Fatal(err)
	}
	return persistedQualification, proposal
}

func TestReadOnlyQualificationStaysInternalAndCreatesNoProposal(t *testing.T) {
	store := newTestStore(t)
	for i := 0; i < 5; i++ {
		addQualificationEvidence(t, store, i, authority.Action{
			Capability: "customer.context.read",
			ToolName:   "Read",
			Effect:     authority.EffectRead,
			Resource:   "customer/" + fmt.Sprint(i),
			Reversible: true,
		})
	}
	profile, qualification := rebuildAndQualify(t, store, 3_000_000)
	if !profile.Readiness.Ready {
		t.Fatalf("profile readiness=%+v", profile.Readiness)
	}
	if qualification.Status != "qualified" ||
		qualification.RiskTier != "read_only" ||
		qualification.ProposalInitiativeID != "" ||
		len(qualification.Reasons) != 0 {
		t.Fatalf("qualification=%+v", qualification)
	}
	actionable, err := store.ListInitiativesByStatus("actionable", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(actionable) != 0 {
		t.Fatalf("qualification created actionable work: %+v", actionable)
	}

	app := &App{activity: &activityMonitorState{store: store}}
	app.qualifyShadowCapability(profile)
	var qualificationCount, initiativeCount, authorityCount int
	for query, target := range map[string]*int{
		`SELECT COUNT(*) FROM shadow_capability_qualifications`: &qualificationCount,
		`SELECT COUNT(*) FROM initiatives`:                      &initiativeCount,
		`SELECT COUNT(*) FROM authority_decisions`:              &authorityCount,
	} {
		if err := store.db.QueryRow(query).Scan(target); err != nil {
			t.Fatal(err)
		}
	}
	if qualificationCount != 1 || initiativeCount != 0 || authorityCount != 0 {
		t.Fatalf(
			"counts qualifications=%d initiatives=%d authority=%d",
			qualificationCount,
			initiativeCount,
			authorityCount,
		)
	}
	entries, err := store.ListCurrentSelfModelEntries()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 ||
		entries[0].Status != selfModelStatusProvisional {
		t.Fatalf("qualification did not retain provisional learning: %+v", entries)
	}
	if _, err := store.db.Exec(
		`UPDATE shadow_capability_qualifications SET status = 'blocked' WHERE id = ?`,
		qualification.ID,
	); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("qualification update error=%v, want immutable rejection", err)
	}
}

func TestLocalWriteQualificationRequiresStrongerEvidence(t *testing.T) {
	store := newTestStore(t)
	action := authority.Action{
		Capability: "filesystem.write",
		ToolName:   "Write",
		Effect:     authority.EffectLocalWrite,
		Resource:   "/tmp/customer-follow-up.md",
		Reversible: true,
	}
	for i := 0; i < 5; i++ {
		addQualificationEvidence(t, store, i, action)
	}
	_, collecting := rebuildAndQualify(t, store, 3_000_000)
	if collecting.Status != "collecting" ||
		collecting.RiskTier != "local_write" ||
		!containsString(collecting.Reasons, "need_qualified_evaluations") {
		t.Fatalf("five-evidence local qualification=%+v", collecting)
	}
	var initiativeCount int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM initiatives`).Scan(&initiativeCount); err != nil {
		t.Fatal(err)
	}
	if initiativeCount != 0 {
		t.Fatalf("collecting qualification created %d initiatives", initiativeCount)
	}

	for i := 5; i < 7; i++ {
		addQualificationEvidence(t, store, i, action)
	}
	_, qualified := rebuildAndQualify(t, store, 4_000_000)
	if qualified.Status != "qualified" ||
		qualified.ProposalInitiativeID != "" ||
		qualified.Actual.QualifiedEvaluations != 7 ||
		qualified.Actual.CompletedRuntimeAttempts != 7 {
		t.Fatalf("seven-evidence local qualification=%+v", qualified)
	}
}

func TestUnknownEffectAndInvalidAllowVetoQualification(t *testing.T) {
	tests := []struct {
		name   string
		action authority.Action
		mutate func(*ShadowExecutionAttempt)
		reason string
	}{
		{
			name: "unknown effect",
			action: authority.Action{
				Capability: "tool.unknown",
				ToolName:   "opaque",
				Effect:     authority.EffectUnknown,
			},
			reason: "unknown_effect_not_qualifiable",
		},
		{
			name: "side effect allowed",
			action: authority.Action{
				Capability: "filesystem.write",
				ToolName:   "Write",
				Effect:     authority.EffectLocalWrite,
				Resource:   "/tmp/output",
				Reversible: true,
			},
			mutate: func(attempt *ShadowExecutionAttempt) {
				attempt.Decisions[0].Decision = authority.DecisionAllow
			},
			reason: "shadow_side_effect_was_allowed",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newTestStore(t)
			for i := 0; i < 7; i++ {
				run := addQualificationEvidence(t, store, i, tt.action)
				if tt.mutate == nil {
					continue
				}
				attempts, err := store.ListShadowExecutionAttempts(run.ID)
				if err != nil || len(attempts) != 1 {
					t.Fatalf("attempts=%+v err=%v", attempts, err)
				}
				attempt := attempts[0]
				tt.mutate(&attempt)
				payload, _ := json.Marshal(attempt)
				if _, err := store.db.Exec(
					`DROP TRIGGER shadow_execution_attempts_no_update`,
				); err != nil {
					t.Fatal(err)
				}
				if _, err := store.db.Exec(
					`UPDATE shadow_execution_attempts SET attempt_json = ? WHERE id = ?`,
					string(payload),
					attempt.ID,
				); err != nil {
					t.Fatal(err)
				}
				if _, err := store.db.Exec(`
					CREATE TRIGGER shadow_execution_attempts_no_update
					BEFORE UPDATE ON shadow_execution_attempts
					BEGIN
						SELECT RAISE(ABORT, 'shadow execution attempts are immutable');
					END;
				`); err != nil {
					t.Fatal(err)
				}
			}
			_, qualification := rebuildAndQualify(t, store, 3_000_000)
			if qualification.Status != "blocked" ||
				!containsString(qualification.Reasons, tt.reason) {
				t.Fatalf("qualification=%+v, want blocker %q", qualification, tt.reason)
			}
			var initiativeCount int
			if err := store.db.QueryRow(`SELECT COUNT(*) FROM initiatives`).Scan(&initiativeCount); err != nil {
				t.Fatal(err)
			}
			if initiativeCount != 0 {
				t.Fatalf("blocked qualification created %d initiatives", initiativeCount)
			}
		})
	}
}
