package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

type ShadowEvidenceReference struct {
	Role        string `json:"role"`
	SourceKind  string `json:"sourceKind"`
	SourceID    string `json:"sourceId"`
	Content     string `json:"content,omitempty"`
	ContentHash string `json:"contentHash"`
	Summary     string `json:"summary,omitempty"`
	ObservedAt  int64  `json:"observedAt,omitempty"`
}

type ShadowContextSnapshot struct {
	CapturedAt           int64                     `json:"capturedAt"`
	ProductionEventAt    int64                     `json:"productionEventAt,omitempty"`
	PromptContext        string                    `json:"promptContext"`
	SelectionBudgetChars int                       `json:"selectionBudgetChars,omitempty"`
	References           []ShadowEvidenceReference `json:"references"`
}

type ShadowReasoningHypothesis struct {
	Statement         string   `json:"statement"`
	Confidence        float64  `json:"confidence"`
	Basis             string   `json:"basis,omitempty"`
	SourceQuote       string   `json:"sourceQuote,omitempty"`
	EvidenceRefs      []string `json:"evidenceRefs,omitempty"`
	ProfileRefs       []string `json:"profileRefs,omitempty"`
	ContradictionRefs []string `json:"contradictionRefs,omitempty"`
}

type ShadowPlannedEffect struct {
	Capability string `json:"capability"`
	Effect     string `json:"effect"`
	Resource   string `json:"resource,omitempty"`
	Recipient  string `json:"recipient,omitempty"`
}

type ShadowCandidateExecutionPlan struct {
	Kind             string          `json:"kind"`
	Name             string          `json:"name"`
	Description      string          `json:"description,omitempty"`
	Prompt           string          `json:"prompt"`
	ScheduleKind     string          `json:"scheduleKind"`
	ScheduleConfig   json.RawMessage `json:"scheduleConfig"`
	ScheduleEvidence string          `json:"scheduleEvidence,omitempty"`
	Rationale        string          `json:"rationale,omitempty"`
	Confidence       float64         `json:"confidence"`
}

type ShadowPrediction struct {
	Goal                   string                        `json:"goal"`
	PredictedAction        string                        `json:"predictedAction"`
	ExpectedResult         string                        `json:"expectedResult"`
	ExpectedTiming         string                        `json:"expectedTiming,omitempty"`
	EvidenceRationale      string                        `json:"evidenceRationale,omitempty"`
	ReasoningHypotheses    []ShadowReasoningHypothesis   `json:"reasoningHypotheses"`
	CandidateExecutionPlan *ShadowCandidateExecutionPlan `json:"candidateExecutionPlan,omitempty"`
	RequiredCapabilities   []string                      `json:"requiredCapabilities,omitempty"`
	PlannedEffects         []ShadowPlannedEffect         `json:"plannedEffects,omitempty"`
	ProfileInfluenceRefs   []string                      `json:"profileInfluenceRefs,omitempty"`
	ProfileConflict        bool                          `json:"profileConflict,omitempty"`
	GoalConfidence         float64                       `json:"goalConfidence"`
	ActionConfidence       float64                       `json:"actionConfidence"`
	ResultConfidence       float64                       `json:"resultConfidence"`
	TimingConfidence       float64                       `json:"timingConfidence"`
}

type ShadowRun struct {
	ID                      string                `json:"id"`
	DecisionKey             string                `json:"decisionKey"`
	CapabilityKey           string                `json:"capabilityKey"`
	Mode                    string                `json:"mode"`
	TaskEpisodeID           string                `json:"taskEpisodeId,omitempty"`
	TaskContextHash         string                `json:"taskContextHash,omitempty"`
	WorkProfileSnapshotID   string                `json:"workProfileSnapshotId,omitempty"`
	WorkProfileSHA256       string                `json:"workProfileSha256,omitempty"`
	SourceKind              string                `json:"sourceKind"`
	SourceID                string                `json:"sourceId"`
	ContextHash             string                `json:"contextHash"`
	Context                 ShadowContextSnapshot `json:"context"`
	PredictionHash          string                `json:"predictionHash"`
	Prediction              ShadowPrediction      `json:"prediction"`
	Model                   string                `json:"model"`
	PromptVersion           string                `json:"promptVersion"`
	RunnerVersion           string                `json:"runnerVersion"`
	ObservationHorizonStart int64                 `json:"observationHorizonStart"`
	ObservationHorizonEnd   int64                 `json:"observationHorizonEnd"`
	CreatedAt               int64                 `json:"createdAt"`
}

const (
	shadowTriagePromptVersion = "activity-shadow-v2"
	shadowObservationWindow   = 24 * time.Hour
)

type ShadowReasoningEvidence struct {
	Kind       string   `json:"kind"`
	Statement  string   `json:"statement"`
	Confidence float64  `json:"confidence"`
	SourceRefs []string `json:"sourceRefs,omitempty"`
}

type ShadowRunObservation struct {
	ID                string                    `json:"id"`
	ShadowRunID       string                    `json:"shadowRunId"`
	SourceKind        string                    `json:"sourceKind"`
	SourceID          string                    `json:"sourceId"`
	ObservedAction    string                    `json:"observedAction"`
	ObservedResult    string                    `json:"observedResult"`
	ReasoningEvidence []ShadowReasoningEvidence `json:"reasoningEvidence"`
	Evidence          []ShadowEvidenceReference `json:"evidence"`
	ObservedAt        int64                     `json:"observedAt"`
	CreatedAt         int64                     `json:"createdAt"`
}

type ShadowEvaluationDimension struct {
	Assessment   string   `json:"assessment"`
	Score        *float64 `json:"score,omitempty"`
	Confidence   float64  `json:"confidence"`
	Reason       string   `json:"reason,omitempty"`
	EvidenceRefs []string `json:"evidenceRefs,omitempty"`
}

type ShadowRunEvaluation struct {
	ID                  string                      `json:"id"`
	ShadowRunID         string                      `json:"shadowRunId"`
	ObservationID       string                      `json:"observationId"`
	IdempotencyKey      string                      `json:"idempotencyKey"`
	ComparisonVersion   string                      `json:"comparisonVersion"`
	EvaluatorVersion    string                      `json:"evaluatorVersion,omitempty"`
	EvaluatorModel      string                      `json:"evaluatorModel"`
	ComparatorPrompt    string                      `json:"comparatorPrompt,omitempty"`
	ComparatorHash      string                      `json:"comparatorHash,omitempty"`
	EvaluatorRaw        string                      `json:"evaluatorRaw,omitempty"`
	Action              ShadowEvaluationDimension   `json:"action"`
	Outcome             ShadowEvaluationDimension   `json:"outcome"`
	Timing              ShadowEvaluationDimension   `json:"timing"`
	Reasoning           ShadowEvaluationDimension   `json:"reasoning"`
	Safety              ShadowEvaluationDimension   `json:"safety"`
	TaskEvaluationInput *ShadowTaskEvaluationInput  `json:"taskEvaluationInput,omitempty"`
	TaskEvaluation      *ShadowTaskEvaluationOutput `json:"taskEvaluation,omitempty"`
	Summary             string                      `json:"summary,omitempty"`
	CreatedAt           int64                       `json:"createdAt"`
}

type ShadowRunEvidence struct {
	ID          string `json:"id"`
	ShadowRunID string `json:"shadowRunId"`
	ShadowEvidenceReference
	CreatedAt int64 `json:"createdAt"`
}

type ShadowRunDetail struct {
	Run          ShadowRun              `json:"run"`
	Status       string                 `json:"status"`
	Evidence     []ShadowRunEvidence    `json:"evidence"`
	Observations []ShadowRunObservation `json:"observations"`
	Evaluations  []ShadowRunEvaluation  `json:"evaluations"`
}

const shadowRunSelectColumns = `
	id, decision_key, capability_key, mode, task_episode_id, task_context_hash,
	work_profile_snapshot_id, work_profile_sha256,
	source_kind, source_id,
	context_hash, context_snapshot_json, prediction_hash, prediction_json,
	model, prompt_version, runner_version, observation_horizon_start,
	observation_horizon_end, created_at`

func normalizeShadowCapabilityKey(value, fallbackSeed string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var b strings.Builder
	lastSeparator := false
	for _, r := range value {
		isAlphaNumeric := r >= 'a' && r <= 'z' || r >= '0' && r <= '9'
		if isAlphaNumeric {
			b.WriteRune(r)
			lastSeparator = false
			continue
		}
		if !lastSeparator && b.Len() > 0 {
			b.WriteByte('.')
			lastSeparator = true
		}
	}
	key := strings.Trim(b.String(), ".")
	if key != "" {
		return key
	}
	sum := sha256.Sum256([]byte(strings.TrimSpace(fallbackSeed)))
	return "behavior." + hex.EncodeToString(sum[:8])
}

func canonicalShadowEvidenceRef(ref, sourceKind, sourceID string) string {
	ref = strings.TrimSpace(ref)
	if ref == "production_activity" {
		return sourceKind + ":" + sourceID
	}
	return ref
}

func (a *App) createShadowRunFromTriage(
	response *activityTriageResponse,
	sourceKind, sourceID, promptContext, summary, model string,
	productionEventAt, createdAt int64,
) (ShadowRun, bool, error) {
	if a == nil || a.activity == nil || a.activity.store == nil {
		return ShadowRun{}, false, errors.New("activity store unavailable")
	}
	if response == nil || !response.WorthInitiative {
		return ShadowRun{}, false, errors.New("shadow triage response is not a prediction")
	}
	goal := strings.TrimSpace(response.InitiativeGoal)
	if goal == "" {
		goal = strings.TrimSpace(response.Description)
	}
	expectedResult := strings.TrimSpace(response.ShadowExpectedResult)
	if expectedResult == "" {
		expectedResult = goal
	}
	if productionEventAt <= 0 {
		productionEventAt = createdAt
	}
	if createdAt <= 0 {
		return ShadowRun{}, false, errors.New("shadow prediction requires a stable creation timestamp")
	}
	hypotheses := append([]ShadowReasoningHypothesis(nil), response.ShadowReasoningHypotheses...)
	for i := range hypotheses {
		for j, ref := range hypotheses[i].EvidenceRefs {
			hypotheses[i].EvidenceRefs[j] = canonicalShadowEvidenceRef(ref, sourceKind, sourceID)
		}
		for j, ref := range hypotheses[i].ContradictionRefs {
			hypotheses[i].ContradictionRefs[j] = canonicalShadowEvidenceRef(ref, sourceKind, sourceID)
		}
	}
	var candidatePlan *ShadowCandidateExecutionPlan
	if response.WorthAutomation &&
		strings.TrimSpace(response.AutomationName) != "" &&
		strings.TrimSpace(response.AutomationPrompt) != "" {
		candidatePlan = &ShadowCandidateExecutionPlan{
			Kind:             "automation",
			Name:             strings.TrimSpace(response.AutomationName),
			Description:      strings.TrimSpace(response.AutomationDescription),
			Prompt:           strings.TrimSpace(response.AutomationPrompt),
			ScheduleKind:     strings.TrimSpace(response.AutomationScheduleKind),
			ScheduleConfig:   append(json.RawMessage(nil), response.AutomationScheduleConfig...),
			ScheduleEvidence: strings.TrimSpace(response.AutomationScheduleEvidence),
			Rationale:        strings.TrimSpace(response.AutomationRationale),
			Confidence:       response.AutomationConfidence,
		}
	}
	run := ShadowRun{
		DecisionKey:   sourceKind + ":" + sourceID + ":" + shadowTriagePromptVersion,
		CapabilityKey: normalizeShadowCapabilityKey(response.ShadowCapabilityKey, response.InitiativeTitle+"\x00"+response.PredictedUserAction),
		SourceKind:    sourceKind,
		SourceID:      sourceID,
		Context: ShadowContextSnapshot{
			CapturedAt:        createdAt,
			ProductionEventAt: productionEventAt,
			PromptContext:     promptContext,
			References: []ShadowEvidenceReference{{
				Role:       "production_context",
				SourceKind: sourceKind,
				SourceID:   sourceID,
				Content:    summary,
				Summary:    strings.TrimSpace(response.Description),
				ObservedAt: productionEventAt,
			}},
		},
		Prediction: ShadowPrediction{
			Goal:                   goal,
			PredictedAction:        strings.TrimSpace(response.PredictedUserAction),
			ExpectedResult:         expectedResult,
			ExpectedTiming:         strings.TrimSpace(response.ShadowExpectedTiming),
			EvidenceRationale:      strings.TrimSpace(response.InitiativeRationale),
			ReasoningHypotheses:    hypotheses,
			CandidateExecutionPlan: candidatePlan,
			GoalConfidence:         response.ShadowGoalConfidence,
			ActionConfidence:       response.InitiativeConfidence,
			ResultConfidence:       response.ShadowResultConfidence,
			TimingConfidence:       response.ShadowTimingConfidence,
		},
		Model:                   strings.TrimSpace(model),
		PromptVersion:           shadowTriagePromptVersion,
		RunnerVersion:           "activity-triage",
		ObservationHorizonStart: productionEventAt,
		ObservationHorizonEnd:   productionEventAt + shadowObservationWindow.Milliseconds(),
		CreatedAt:               createdAt,
	}
	return a.activity.store.CreateShadowRun(run)
}

func encodeShadowPayload(value any) ([]byte, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode shadow payload: %w", err)
	}
	return payload, nil
}

func normalizeShadowReference(ref ShadowEvidenceReference) (ShadowEvidenceReference, error) {
	ref.Role = strings.ToLower(strings.TrimSpace(ref.Role))
	if ref.Role == "" {
		ref.Role = "context"
	}
	ref.SourceKind = strings.TrimSpace(ref.SourceKind)
	ref.SourceID = strings.TrimSpace(ref.SourceID)
	if ref.SourceKind == "" || ref.SourceID == "" {
		return ShadowEvidenceReference{}, errors.New("shadow evidence requires source kind and id")
	}
	ref.ContentHash = strings.TrimSpace(ref.ContentHash)
	if ref.ContentHash == "" {
		payload := ref.Content
		if payload == "" {
			payload = ref.SourceKind + "\x00" + ref.SourceID + "\x00" + ref.Summary
		}
		ref.ContentHash = sha256Hex([]byte(payload))
	}
	return ref, nil
}

func normalizeShadowPrediction(prediction ShadowPrediction) (ShadowPrediction, error) {
	prediction.Goal = strings.TrimSpace(prediction.Goal)
	prediction.PredictedAction = strings.TrimSpace(prediction.PredictedAction)
	prediction.ExpectedResult = strings.TrimSpace(prediction.ExpectedResult)
	if prediction.Goal == "" || prediction.PredictedAction == "" || prediction.ExpectedResult == "" {
		return ShadowPrediction{}, errors.New("shadow prediction requires goal, action, and expected result")
	}
	prediction.GoalConfidence = clampConfidence(prediction.GoalConfidence)
	prediction.ActionConfidence = clampConfidence(prediction.ActionConfidence)
	prediction.ResultConfidence = clampConfidence(prediction.ResultConfidence)
	prediction.TimingConfidence = clampConfidence(prediction.TimingConfidence)
	seenProfileRefs := make(map[string]struct{})
	profileRefs := make(
		[]string,
		0,
		len(prediction.ProfileInfluenceRefs),
	)
	for _, ref := range prediction.ProfileInfluenceRefs {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}
		if _, exists := seenProfileRefs[ref]; exists {
			continue
		}
		seenProfileRefs[ref] = struct{}{}
		profileRefs = append(profileRefs, ref)
	}
	sort.Strings(profileRefs)
	prediction.ProfileInfluenceRefs = profileRefs
	if len(profileRefs) == 0 {
		prediction.ProfileConflict = false
	}
	if prediction.ProfileConflict {
		prediction.GoalConfidence = math.Min(
			prediction.GoalConfidence,
			0.6,
		)
		prediction.ActionConfidence = math.Min(
			prediction.ActionConfidence,
			0.6,
		)
		prediction.ResultConfidence = math.Min(
			prediction.ResultConfidence,
			0.6,
		)
		prediction.TimingConfidence = math.Min(
			prediction.TimingConfidence,
			0.6,
		)
	}
	if prediction.CandidateExecutionPlan != nil {
		plan := prediction.CandidateExecutionPlan
		plan.Kind = strings.ToLower(strings.TrimSpace(plan.Kind))
		plan.Name = strings.TrimSpace(plan.Name)
		plan.Description = strings.TrimSpace(plan.Description)
		plan.Prompt = strings.TrimSpace(plan.Prompt)
		plan.ScheduleKind = strings.ToLower(strings.TrimSpace(plan.ScheduleKind))
		plan.ScheduleEvidence = strings.TrimSpace(plan.ScheduleEvidence)
		plan.Rationale = strings.TrimSpace(plan.Rationale)
		plan.Confidence = clampConfidence(plan.Confidence)
		if (plan.Kind != "automation" && plan.Kind != "shadow_task") ||
			plan.Name == "" ||
			plan.Prompt == "" {
			return ShadowPrediction{}, errors.New(
				"shadow candidate execution plan requires automation or shadow_task kind, name, and prompt",
			)
		}
		if plan.ScheduleKind == "" {
			plan.ScheduleKind = "manual"
		}
		if len(plan.ScheduleConfig) == 0 {
			plan.ScheduleConfig = json.RawMessage(`{}`)
		}
	}
	for i := range prediction.ReasoningHypotheses {
		hypothesis := &prediction.ReasoningHypotheses[i]
		hypothesis.Statement = strings.TrimSpace(hypothesis.Statement)
		if hypothesis.Statement == "" {
			return ShadowPrediction{}, errors.New("shadow reasoning hypothesis requires a statement")
		}
		hypothesis.Confidence = clampConfidence(hypothesis.Confidence)
		hypothesis.Basis = strings.ToLower(strings.TrimSpace(hypothesis.Basis))
		hypothesis.SourceQuote = strings.TrimSpace(hypothesis.SourceQuote)
		hypothesis.EvidenceRefs = uniqueSortedStrings(hypothesis.EvidenceRefs)
		hypothesis.ProfileRefs = uniqueSortedStrings(hypothesis.ProfileRefs)
		hypothesis.ContradictionRefs = uniqueSortedStrings(
			hypothesis.ContradictionRefs,
		)
	}
	return prediction, nil
}

func normalizeShadowRun(run ShadowRun) (ShadowRun, []byte, []byte, error) {
	run.DecisionKey = strings.TrimSpace(run.DecisionKey)
	run.CapabilityKey = strings.TrimSpace(run.CapabilityKey)
	run.Mode = strings.ToLower(strings.TrimSpace(run.Mode))
	run.TaskEpisodeID = strings.TrimSpace(run.TaskEpisodeID)
	run.TaskContextHash = strings.ToLower(strings.TrimSpace(run.TaskContextHash))
	run.WorkProfileSnapshotID = strings.TrimSpace(run.WorkProfileSnapshotID)
	run.WorkProfileSHA256 = strings.ToLower(strings.TrimSpace(
		run.WorkProfileSHA256,
	))
	run.SourceKind = strings.TrimSpace(run.SourceKind)
	run.SourceID = strings.TrimSpace(run.SourceID)
	run.PromptVersion = strings.TrimSpace(run.PromptVersion)
	if run.DecisionKey == "" || run.CapabilityKey == "" {
		return ShadowRun{}, nil, nil, errors.New("shadow run requires decision and capability keys")
	}
	if run.Mode == "" {
		run.Mode = "legacy_prediction"
	}
	switch run.Mode {
	case "legacy_prediction":
	case "task_mirror":
		if run.TaskEpisodeID == "" || len(run.TaskContextHash) != 64 {
			return ShadowRun{}, nil, nil, errors.New(
				"task-mirror shadow run requires a task episode and frozen context hash",
			)
		}
	default:
		return ShadowRun{}, nil, nil, fmt.Errorf("unsupported shadow run mode %q", run.Mode)
	}
	if (run.WorkProfileSnapshotID == "") != (run.WorkProfileSHA256 == "") {
		return ShadowRun{}, nil, nil, errors.New(
			"shadow run work profile reference is incomplete",
		)
	}
	if run.WorkProfileSHA256 != "" && len(run.WorkProfileSHA256) != 64 {
		return ShadowRun{}, nil, nil, errors.New(
			"shadow run work profile hash is invalid",
		)
	}
	if run.SourceKind == "" || run.SourceID == "" {
		return ShadowRun{}, nil, nil, errors.New("shadow run requires a source")
	}
	if run.PromptVersion == "" {
		return ShadowRun{}, nil, nil, errors.New("shadow run requires a prompt version")
	}
	if strings.TrimSpace(run.Context.PromptContext) == "" {
		return ShadowRun{}, nil, nil, errors.New("shadow run requires the exact prompt context")
	}
	if run.CreatedAt == 0 {
		return ShadowRun{}, nil, nil, errors.New("shadow run requires a stable creation timestamp")
	}
	if run.Context.CapturedAt == 0 {
		return ShadowRun{}, nil, nil, errors.New("shadow run requires a stable context capture timestamp")
	}
	if run.ID == "" {
		run.ID = uuid.NewString()
	}
	if run.ObservationHorizonEnd <= run.ObservationHorizonStart {
		return ShadowRun{}, nil, nil, errors.New("shadow run observation horizon must end after it starts")
	}
	for i := range run.Context.References {
		ref, err := normalizeShadowReference(run.Context.References[i])
		if err != nil {
			return ShadowRun{}, nil, nil, err
		}
		run.Context.References[i] = ref
	}
	prediction, err := normalizeShadowPrediction(run.Prediction)
	if err != nil {
		return ShadowRun{}, nil, nil, err
	}
	run.Prediction = prediction
	contextPayload, err := encodeShadowPayload(run.Context)
	if err != nil {
		return ShadowRun{}, nil, nil, err
	}
	predictionPayload, err := encodeShadowPayload(run.Prediction)
	if err != nil {
		return ShadowRun{}, nil, nil, err
	}
	contextHash := sha256Hex(contextPayload)
	if run.ContextHash != "" && run.ContextHash != contextHash {
		return ShadowRun{}, nil, nil, errors.New("shadow context hash does not match payload")
	}
	predictionHash := sha256Hex(predictionPayload)
	if run.PredictionHash != "" && run.PredictionHash != predictionHash {
		return ShadowRun{}, nil, nil, errors.New("shadow prediction hash does not match payload")
	}
	run.ContextHash = contextHash
	run.PredictionHash = predictionHash
	return run, contextPayload, predictionPayload, nil
}

func scanShadowRun(scanner interface{ Scan(dest ...any) error }) (ShadowRun, error) {
	var run ShadowRun
	var contextJSON, predictionJSON string
	if err := scanner.Scan(
		&run.ID,
		&run.DecisionKey,
		&run.CapabilityKey,
		&run.Mode,
		&run.TaskEpisodeID,
		&run.TaskContextHash,
		&run.WorkProfileSnapshotID,
		&run.WorkProfileSHA256,
		&run.SourceKind,
		&run.SourceID,
		&run.ContextHash,
		&contextJSON,
		&run.PredictionHash,
		&predictionJSON,
		&run.Model,
		&run.PromptVersion,
		&run.RunnerVersion,
		&run.ObservationHorizonStart,
		&run.ObservationHorizonEnd,
		&run.CreatedAt,
	); err != nil {
		return ShadowRun{}, err
	}
	if err := json.Unmarshal([]byte(contextJSON), &run.Context); err != nil {
		return ShadowRun{}, fmt.Errorf("decode shadow context %s: %w", run.ID, err)
	}
	if err := json.Unmarshal([]byte(predictionJSON), &run.Prediction); err != nil {
		return ShadowRun{}, fmt.Errorf("decode shadow prediction %s: %w", run.ID, err)
	}
	return run, nil
}

func shadowRunsEquivalent(a, b ShadowRun) bool {
	return a.DecisionKey == b.DecisionKey &&
		a.CapabilityKey == b.CapabilityKey &&
		a.Mode == b.Mode &&
		a.TaskEpisodeID == b.TaskEpisodeID &&
		a.TaskContextHash == b.TaskContextHash &&
		a.WorkProfileSnapshotID == b.WorkProfileSnapshotID &&
		a.WorkProfileSHA256 == b.WorkProfileSHA256 &&
		a.SourceKind == b.SourceKind &&
		a.SourceID == b.SourceID &&
		a.ContextHash == b.ContextHash &&
		a.PredictionHash == b.PredictionHash &&
		a.Model == b.Model &&
		a.PromptVersion == b.PromptVersion &&
		a.RunnerVersion == b.RunnerVersion &&
		a.ObservationHorizonStart == b.ObservationHorizonStart &&
		a.ObservationHorizonEnd == b.ObservationHorizonEnd
}

func (s *ActivityStore) CreateShadowRun(input ShadowRun) (ShadowRun, bool, error) {
	run, contextPayload, predictionPayload, err := normalizeShadowRun(input)
	if err != nil {
		return ShadowRun{}, false, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return ShadowRun{}, false, fmt.Errorf("begin shadow run: %w", err)
	}
	defer tx.Rollback()

	result, err := tx.Exec(`
		INSERT OR IGNORE INTO shadow_runs (
			id, decision_key, capability_key, mode, task_episode_id,
			task_context_hash, work_profile_snapshot_id, work_profile_sha256,
			source_kind, source_id,
			context_hash, context_snapshot_json, prediction_hash, prediction_json,
			model, prompt_version, runner_version, observation_horizon_start,
			observation_horizon_end, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		run.ID, run.DecisionKey, run.CapabilityKey, run.Mode, run.TaskEpisodeID,
		run.TaskContextHash, run.WorkProfileSnapshotID, run.WorkProfileSHA256,
		run.SourceKind, run.SourceID,
		run.ContextHash, string(contextPayload), run.PredictionHash, string(predictionPayload),
		run.Model, run.PromptVersion, run.RunnerVersion, run.ObservationHorizonStart,
		run.ObservationHorizonEnd, run.CreatedAt,
	)
	if err != nil {
		return ShadowRun{}, false, fmt.Errorf("create shadow run: %w", err)
	}
	added, err := result.RowsAffected()
	if err != nil {
		return ShadowRun{}, false, fmt.Errorf("shadow run rows affected: %w", err)
	}
	if added == 0 {
		if err := tx.Rollback(); err != nil {
			return ShadowRun{}, false, err
		}
		existing, getErr := s.GetShadowRunByDecisionKey(run.DecisionKey)
		if getErr != nil {
			return ShadowRun{}, false, getErr
		}
		if !shadowRunsEquivalent(existing, run) {
			return ShadowRun{}, false, fmt.Errorf("shadow decision key %q already has a different immutable payload", run.DecisionKey)
		}
		return existing, false, nil
	}

	for _, ref := range run.Context.References {
		if _, err := tx.Exec(`
			INSERT INTO shadow_run_evidence (
				id, shadow_run_id, role, source_kind, source_id, content_hash,
				summary, observed_at, created_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			uuid.NewString(), run.ID, ref.Role, ref.SourceKind, ref.SourceID,
			ref.ContentHash, ref.Summary, ref.ObservedAt, run.CreatedAt,
		); err != nil {
			return ShadowRun{}, false, fmt.Errorf("save shadow run evidence: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return ShadowRun{}, false, fmt.Errorf("commit shadow run: %w", err)
	}
	return run, true, nil
}

func (s *ActivityStore) GetShadowRun(id string) (ShadowRun, error) {
	return scanShadowRun(s.db.QueryRow(
		`SELECT `+shadowRunSelectColumns+` FROM shadow_runs WHERE id = ?`,
		strings.TrimSpace(id),
	))
}

func (s *ActivityStore) GetShadowRunByDecisionKey(decisionKey string) (ShadowRun, error) {
	return scanShadowRun(s.db.QueryRow(
		`SELECT `+shadowRunSelectColumns+` FROM shadow_runs WHERE decision_key = ?`,
		strings.TrimSpace(decisionKey),
	))
}

func normalizeShadowReasoningEvidence(items []ShadowReasoningEvidence) ([]ShadowReasoningEvidence, error) {
	for i := range items {
		item := &items[i]
		item.Kind = strings.ToLower(strings.TrimSpace(item.Kind))
		item.Statement = strings.TrimSpace(item.Statement)
		switch item.Kind {
		case "stated_rationale", "correction", "repeated_choice", "contextual_constraint":
		default:
			return nil, fmt.Errorf("unsupported shadow reasoning evidence kind %q", item.Kind)
		}
		if item.Statement == "" {
			return nil, errors.New("shadow reasoning evidence requires a statement")
		}
		item.Confidence = clampConfidence(item.Confidence)
	}
	return items, nil
}

func scanShadowRunObservation(scanner interface{ Scan(dest ...any) error }) (ShadowRunObservation, error) {
	var item ShadowRunObservation
	var reasoningJSON, evidenceJSON string
	if err := scanner.Scan(
		&item.ID,
		&item.ShadowRunID,
		&item.SourceKind,
		&item.SourceID,
		&item.ObservedAction,
		&item.ObservedResult,
		&reasoningJSON,
		&evidenceJSON,
		&item.ObservedAt,
		&item.CreatedAt,
	); err != nil {
		return ShadowRunObservation{}, err
	}
	if err := json.Unmarshal([]byte(reasoningJSON), &item.ReasoningEvidence); err != nil {
		return ShadowRunObservation{}, fmt.Errorf("decode shadow reasoning evidence %s: %w", item.ID, err)
	}
	if err := json.Unmarshal([]byte(evidenceJSON), &item.Evidence); err != nil {
		return ShadowRunObservation{}, fmt.Errorf("decode shadow observation evidence %s: %w", item.ID, err)
	}
	return item, nil
}

const shadowObservationSelectColumns = `
	id, shadow_run_id, source_kind, source_id, observed_action, observed_result,
	reasoning_evidence_json, evidence_json, observed_at, created_at`

func shadowObservationsEquivalent(a, b ShadowRunObservation) bool {
	a.ID, b.ID = "", ""
	a.CreatedAt, b.CreatedAt = 0, 0
	left, _ := json.Marshal(a)
	right, _ := json.Marshal(b)
	return string(left) == string(right)
}

func (s *ActivityStore) AddShadowRunObservation(input ShadowRunObservation) (ShadowRunObservation, bool, error) {
	input.ShadowRunID = strings.TrimSpace(input.ShadowRunID)
	input.SourceKind = strings.TrimSpace(input.SourceKind)
	input.SourceID = strings.TrimSpace(input.SourceID)
	input.ObservedAction = strings.TrimSpace(input.ObservedAction)
	if input.ShadowRunID == "" || input.SourceKind == "" || input.SourceID == "" {
		return ShadowRunObservation{}, false, errors.New("shadow observation requires a run and source")
	}
	if input.ObservedAction == "" {
		return ShadowRunObservation{}, false, errors.New("shadow observation requires an observed action")
	}
	reasoning, err := normalizeShadowReasoningEvidence(input.ReasoningEvidence)
	if err != nil {
		return ShadowRunObservation{}, false, err
	}
	input.ReasoningEvidence = reasoning
	for i := range input.Evidence {
		ref, err := normalizeShadowReference(input.Evidence[i])
		if err != nil {
			return ShadowRunObservation{}, false, err
		}
		input.Evidence[i] = ref
	}
	if input.ID == "" {
		input.ID = uuid.NewString()
	}
	if input.ObservedAt == 0 {
		return ShadowRunObservation{}, false, errors.New("shadow observation requires a stable source timestamp")
	}
	if input.CreatedAt == 0 {
		input.CreatedAt = time.Now().UnixMilli()
	}
	reasoningJSON, err := encodeShadowPayload(input.ReasoningEvidence)
	if err != nil {
		return ShadowRunObservation{}, false, err
	}
	evidenceJSON, err := encodeShadowPayload(input.Evidence)
	if err != nil {
		return ShadowRunObservation{}, false, err
	}
	result, err := s.db.Exec(`
		INSERT OR IGNORE INTO shadow_run_observations (
			id, shadow_run_id, source_kind, source_id, observed_action,
			observed_result, reasoning_evidence_json, evidence_json,
			observed_at, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		input.ID, input.ShadowRunID, input.SourceKind, input.SourceID,
		input.ObservedAction, input.ObservedResult, string(reasoningJSON),
		string(evidenceJSON), input.ObservedAt, input.CreatedAt,
	)
	if err != nil {
		return ShadowRunObservation{}, false, fmt.Errorf("save shadow observation: %w", err)
	}
	added, err := result.RowsAffected()
	if err != nil {
		return ShadowRunObservation{}, false, err
	}
	if added == 1 {
		return input, true, nil
	}
	existing, err := s.GetShadowRunObservationBySource(input.ShadowRunID, input.SourceKind, input.SourceID)
	if err != nil {
		return ShadowRunObservation{}, false, err
	}
	if !shadowObservationsEquivalent(existing, input) {
		return ShadowRunObservation{}, false, errors.New("shadow observation source already has a different immutable payload")
	}
	return existing, false, nil
}

func (s *ActivityStore) GetShadowRunObservationBySource(runID, sourceKind, sourceID string) (ShadowRunObservation, error) {
	return scanShadowRunObservation(s.db.QueryRow(
		`SELECT `+shadowObservationSelectColumns+`
		FROM shadow_run_observations
		WHERE shadow_run_id = ? AND source_kind = ? AND source_id = ?`,
		runID, sourceKind, sourceID,
	))
}

func (s *ActivityStore) ListShadowRunObservations(runID string) ([]ShadowRunObservation, error) {
	rows, err := s.db.Query(
		`SELECT `+shadowObservationSelectColumns+`
		FROM shadow_run_observations
		WHERE shadow_run_id = ?
		ORDER BY observed_at ASC, created_at ASC`,
		runID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]ShadowRunObservation, 0)
	for rows.Next() {
		item, err := scanShadowRunObservation(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func normalizeShadowEvaluationDimension(
	name string,
	dimension ShadowEvaluationDimension,
) (ShadowEvaluationDimension, error) {
	dimension.Assessment = strings.ToLower(strings.TrimSpace(dimension.Assessment))
	if name == "safety" {
		switch dimension.Assessment {
		case "safe", "unsafe", "unknown":
		default:
			return ShadowEvaluationDimension{}, fmt.Errorf("unsupported shadow safety assessment %q", dimension.Assessment)
		}
	} else {
		switch dimension.Assessment {
		case "matched", "partial", "different", "acceptable_alternative", "unknown":
		default:
			return ShadowEvaluationDimension{}, fmt.Errorf("unsupported shadow %s assessment %q", name, dimension.Assessment)
		}
	}
	if dimension.Assessment == "unknown" {
		if dimension.Score != nil {
			return ShadowEvaluationDimension{}, fmt.Errorf("unknown shadow %s assessment cannot have a score", name)
		}
	} else {
		if dimension.Score == nil {
			return ShadowEvaluationDimension{}, fmt.Errorf("shadow %s assessment requires a score", name)
		}
		score := clampConfidence(*dimension.Score)
		dimension.Score = &score
	}
	dimension.Confidence = clampConfidence(dimension.Confidence)
	return dimension, nil
}

func normalizeShadowRunEvaluation(input ShadowRunEvaluation) (ShadowRunEvaluation, error) {
	input.ShadowRunID = strings.TrimSpace(input.ShadowRunID)
	input.ObservationID = strings.TrimSpace(input.ObservationID)
	input.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey)
	input.ComparisonVersion = strings.TrimSpace(input.ComparisonVersion)
	input.EvaluatorVersion = strings.TrimSpace(input.EvaluatorVersion)
	if input.ShadowRunID == "" || input.ObservationID == "" {
		return ShadowRunEvaluation{}, errors.New("shadow evaluation requires a run and observation")
	}
	if input.IdempotencyKey == "" || input.ComparisonVersion == "" {
		return ShadowRunEvaluation{}, errors.New("shadow evaluation requires idempotency and comparison versions")
	}
	input.ComparatorHash = strings.TrimSpace(input.ComparatorHash)
	if input.ComparatorPrompt != "" {
		expectedHash := sha256Hex([]byte(input.ComparatorPrompt))
		if input.ComparatorHash != "" && input.ComparatorHash != expectedHash {
			return ShadowRunEvaluation{}, errors.New("shadow comparator hash does not match prompt")
		}
		input.ComparatorHash = expectedHash
	}
	var err error
	if input.Action, err = normalizeShadowEvaluationDimension("action", input.Action); err != nil {
		return ShadowRunEvaluation{}, err
	}
	if input.Outcome, err = normalizeShadowEvaluationDimension("outcome", input.Outcome); err != nil {
		return ShadowRunEvaluation{}, err
	}
	if input.Timing, err = normalizeShadowEvaluationDimension("timing", input.Timing); err != nil {
		return ShadowRunEvaluation{}, err
	}
	if input.Reasoning, err = normalizeShadowEvaluationDimension("reasoning", input.Reasoning); err != nil {
		return ShadowRunEvaluation{}, err
	}
	if input.Safety, err = normalizeShadowEvaluationDimension("safety", input.Safety); err != nil {
		return ShadowRunEvaluation{}, err
	}
	if input.TaskEvaluation != nil &&
		input.TaskEvaluation.Version != ShadowTaskEvaluationOutputVersion {
		return ShadowRunEvaluation{}, errors.New("shadow task evaluation has an unsupported version")
	}
	if input.TaskEvaluationInput != nil {
		normalized, err := normalizeShadowTaskEvaluationInput(*input.TaskEvaluationInput)
		if err != nil {
			return ShadowRunEvaluation{}, fmt.Errorf("normalize shadow task evaluation input: %w", err)
		}
		input.TaskEvaluationInput = &normalized
	}
	if input.ID == "" {
		input.ID = uuid.NewString()
	}
	if input.CreatedAt == 0 {
		input.CreatedAt = time.Now().UnixMilli()
	}
	return input, nil
}

func scanShadowRunEvaluation(scanner interface{ Scan(dest ...any) error }) (ShadowRunEvaluation, error) {
	var id, raw string
	var createdAt int64
	if err := scanner.Scan(&id, &raw, &createdAt); err != nil {
		return ShadowRunEvaluation{}, err
	}
	var item ShadowRunEvaluation
	if err := json.Unmarshal([]byte(raw), &item); err != nil {
		return ShadowRunEvaluation{}, fmt.Errorf("decode shadow evaluation %s: %w", id, err)
	}
	item.ID = id
	item.CreatedAt = createdAt
	return item, nil
}

func shadowEvaluationsEquivalent(a, b ShadowRunEvaluation) bool {
	a.ID, b.ID = "", ""
	a.CreatedAt, b.CreatedAt = 0, 0
	left, _ := json.Marshal(a)
	right, _ := json.Marshal(b)
	return string(left) == string(right)
}

func (s *ActivityStore) AddShadowRunEvaluation(input ShadowRunEvaluation) (ShadowRunEvaluation, bool, error) {
	input, err := normalizeShadowRunEvaluation(input)
	if err != nil {
		return ShadowRunEvaluation{}, false, err
	}
	var observationRunID string
	if err := s.db.QueryRow(
		`SELECT shadow_run_id FROM shadow_run_observations WHERE id = ?`,
		input.ObservationID,
	).Scan(&observationRunID); err != nil {
		return ShadowRunEvaluation{}, false, err
	}
	if observationRunID != input.ShadowRunID {
		return ShadowRunEvaluation{}, false, errors.New("shadow evaluation observation belongs to a different run")
	}
	payload, err := encodeShadowPayload(input)
	if err != nil {
		return ShadowRunEvaluation{}, false, err
	}
	result, err := s.db.Exec(`
		INSERT OR IGNORE INTO shadow_run_evaluations (
			id, shadow_run_id, observation_id, idempotency_key,
			comparison_version, evaluator_model, action_assessment,
			outcome_assessment, timing_assessment, reasoning_assessment,
			safety_assessment, evaluation_json, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		input.ID, input.ShadowRunID, input.ObservationID, input.IdempotencyKey,
		input.ComparisonVersion, input.EvaluatorModel, input.Action.Assessment,
		input.Outcome.Assessment, input.Timing.Assessment,
		input.Reasoning.Assessment, input.Safety.Assessment,
		string(payload), input.CreatedAt,
	)
	if err != nil {
		return ShadowRunEvaluation{}, false, fmt.Errorf("save shadow evaluation: %w", err)
	}
	added, err := result.RowsAffected()
	if err != nil {
		return ShadowRunEvaluation{}, false, err
	}
	if added == 1 {
		return input, true, nil
	}
	existing, err := s.GetShadowRunEvaluationByKey(input.IdempotencyKey)
	if err != nil {
		return ShadowRunEvaluation{}, false, err
	}
	if !shadowEvaluationsEquivalent(existing, input) {
		return ShadowRunEvaluation{}, false, errors.New("shadow evaluation key already has a different immutable payload")
	}
	return existing, false, nil
}

func (s *ActivityStore) GetShadowRunEvaluationByKey(idempotencyKey string) (ShadowRunEvaluation, error) {
	return scanShadowRunEvaluation(s.db.QueryRow(`
		SELECT id, evaluation_json, created_at
		FROM shadow_run_evaluations
		WHERE idempotency_key = ?`,
		idempotencyKey,
	))
}

func (s *ActivityStore) ListShadowRunEvaluations(runID string) ([]ShadowRunEvaluation, error) {
	rows, err := s.db.Query(`
		SELECT id, evaluation_json, created_at
		FROM shadow_run_evaluations
		WHERE shadow_run_id = ?
		ORDER BY created_at ASC`,
		runID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]ShadowRunEvaluation, 0)
	for rows.Next() {
		item, err := scanShadowRunEvaluation(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *ActivityStore) ListShadowRunEvidence(runID string) ([]ShadowRunEvidence, error) {
	rows, err := s.db.Query(`
		SELECT id, shadow_run_id, role, source_kind, source_id, content_hash,
			summary, observed_at, created_at
		FROM shadow_run_evidence
		WHERE shadow_run_id = ?
		ORDER BY created_at ASC, rowid ASC`,
		runID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]ShadowRunEvidence, 0)
	for rows.Next() {
		var item ShadowRunEvidence
		if err := rows.Scan(
			&item.ID,
			&item.ShadowRunID,
			&item.Role,
			&item.SourceKind,
			&item.SourceID,
			&item.ContentHash,
			&item.Summary,
			&item.ObservedAt,
			&item.CreatedAt,
		); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *ActivityStore) ListPendingShadowRuns(now int64, limit int) ([]ShadowRun, error) {
	if now == 0 {
		now = time.Now().UnixMilli()
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.Query(`
		SELECT `+shadowRunSelectColumns+`
		FROM shadow_runs r
		WHERE r.mode = 'legacy_prediction'
			AND r.observation_horizon_start <= ?
			AND r.observation_horizon_end >= ?
			AND NOT EXISTS (
				SELECT 1 FROM shadow_run_observations o
				WHERE o.shadow_run_id = r.id
			)
		ORDER BY r.created_at ASC
		LIMIT ?`,
		now, now, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]ShadowRun, 0)
	for rows.Next() {
		item, err := scanShadowRun(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *ActivityStore) GetShadowRunDetail(runID string, now int64) (ShadowRunDetail, error) {
	run, err := s.GetShadowRun(runID)
	if err != nil {
		return ShadowRunDetail{}, err
	}
	evidence, err := s.ListShadowRunEvidence(runID)
	if err != nil {
		return ShadowRunDetail{}, err
	}
	observations, err := s.ListShadowRunObservations(runID)
	if err != nil {
		return ShadowRunDetail{}, err
	}
	evaluations, err := s.ListShadowRunEvaluations(runID)
	if err != nil {
		return ShadowRunDetail{}, err
	}
	if now == 0 {
		now = time.Now().UnixMilli()
	}
	status := "awaiting_observation"
	evaluatedObservations := make(map[string]struct{}, len(evaluations))
	for _, evaluation := range evaluations {
		evaluatedObservations[evaluation.ObservationID] = struct{}{}
	}
	switch {
	case len(observations) > 0 && len(evaluatedObservations) == len(observations):
		status = "evaluated"
	case len(observations) > 0:
		status = "observed"
	case now > run.ObservationHorizonEnd:
		status = "expired"
	}
	return ShadowRunDetail{
		Run:          run,
		Status:       status,
		Evidence:     evidence,
		Observations: observations,
		Evaluations:  evaluations,
	}, nil
}
