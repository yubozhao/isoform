package main

import (
	"strings"
)

const (
	taskOnsetPromptVersion = "task-onset-v5"
	taskOnsetMaxEvidence   = 200
	taskOnsetMaxEpisodes   = 3
	taskOnsetMaxAlternates = 3
	taskOnsetMaxReasoning  = 3
	taskOnsetContextBudget = 48_000

	TaskReasoningBasisExplicitStatement   = "explicit_statement"
	TaskReasoningBasisRepeatedPattern     = "repeated_pattern"
	TaskReasoningBasisContextualInference = "contextual_inference"
)

// TaskOnsetEvidence is a redacted, timestamped observation available inside a
// bounded onset window. Later user behavior must never be placed in this
// structure and is rejected when its timestamp exceeds CutoffAt.
type TaskOnsetEvidence struct {
	Ref        string `json:"ref"`
	Kind       string `json:"kind"`
	SourceKind string `json:"sourceKind"`
	SourceID   string `json:"sourceId"`
	OccurredAt int64  `json:"occurredAt"`
	AppKey     string `json:"appKey,omitempty"`
	AppName    string `json:"appName,omitempty"`
	Summary    string `json:"summary"`
}

type TaskOnsetInput struct {
	SourceKind   string                     `json:"sourceKind"`
	SourceID     string                     `json:"sourceId"`
	WindowStart  int64                      `json:"windowStart"`
	CutoffAt     int64                      `json:"cutoffAt"`
	Evidence     []TaskOnsetEvidence        `json:"evidence"`
	PrivacyNotes []string                   `json:"privacyNotes,omitempty"`
	WorkProfile  *WorkPatternProfileContext `json:"workProfile,omitempty"`
}

type TaskOnsetAlternative struct {
	Task              string   `json:"task"`
	DesiredResult     string   `json:"desiredResult"`
	Confidence        float64  `json:"confidence"`
	EvidenceRefs      []string `json:"evidenceRefs"`
	ContradictionRefs []string `json:"contradictionRefs,omitempty"`
}

// TaskOnsetReasoningHypothesis is prediction-time model output. It is not an
// observed fact about why the user acted. Explicit statements are accepted as
// such only after deterministic literal-source validation.
type TaskOnsetReasoningHypothesis struct {
	Statement         string   `json:"statement"`
	Confidence        float64  `json:"confidence"`
	Basis             string   `json:"basis"`
	SourceQuote       string   `json:"sourceQuote,omitempty"`
	EvidenceRefs      []string `json:"evidenceRefs,omitempty"`
	ProfileRefs       []string `json:"profileRefs,omitempty"`
	ContradictionRefs []string `json:"contradictionRefs,omitempty"`
}

type TaskOnsetPrediction struct {
	LocalKey             string                         `json:"localKey"`
	Task                 string                         `json:"task"`
	DesiredResult        string                         `json:"desiredResult"`
	InferredStartedAt    int64                          `json:"inferredStartedAt"`
	TaskConfidence       float64                        `json:"taskConfidence"`
	BoundaryConfidence   float64                        `json:"boundaryConfidence"`
	ResultConfidence     float64                        `json:"resultConfidence"`
	EvidenceRefs         []string                       `json:"evidenceRefs"`
	ProfileInfluenceRefs []string                       `json:"profileInfluenceRefs,omitempty"`
	ProfileConflict      bool                           `json:"profileConflict,omitempty"`
	Alternatives         []TaskOnsetAlternative         `json:"alternatives,omitempty"`
	ReasoningHypotheses  []TaskOnsetReasoningHypothesis `json:"reasoningHypotheses,omitempty"`
}

type TaskOnsetResponse struct {
	Episodes []TaskOnsetPrediction `json:"episodes"`
}

func normalizeTaskReasoningText(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(value)), " "))
}

func taskOnsetReasoningHasExplicitCue(value string) bool {
	value = normalizeTaskReasoningText(value)
	for _, cue := range []string{
		"because",
		"so that",
		"in order to",
		"the reason",
		"this is why",
		"that's why",
		"so i can",
		"so we can",
		"my goal is",
		"our goal is",
	} {
		if strings.Contains(value, cue) {
			return true
		}
	}
	return false
}
