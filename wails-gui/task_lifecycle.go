package main

const (
	TaskLifecycleKindCompleted = "completed"
	TaskLifecycleKindContinued = "continued"
	TaskLifecycleKindPaused    = "paused"
	TaskLifecycleKindResumed   = "resumed"
	TaskLifecycleKindAbandoned = "abandoned"
	TaskLifecycleKindSwitched  = "switched"
	TaskLifecycleKindMerged    = "merged"
	TaskLifecycleKindSplit     = "split"
	TaskLifecycleKindResult    = "result"
	TaskLifecycleKindUnknown   = "unknown"

	TaskLifecycleContentCommittedText = "committed_text"
	TaskLifecycleEvidenceTextCommit   = "text_commit_candidate"

	TaskLifecycleActorUser       = "user"
	TaskLifecycleActorOtherParty = "other_party"
	TaskLifecycleActorSystem     = "system"
	TaskLifecycleActorUnknown    = "unknown"

	TaskLifecycleReasoningStatedRationale = "stated_rationale"
	TaskLifecycleReasoningCorrection      = "correction"
)

// TaskLifecycleEvidence is one independently observed, timestamped production
// fact. Ref is local to the lifecycle observation; SourceKind and SourceID
// retain the durable provenance needed by the persistence adapter.
type TaskLifecycleEvidence struct {
	Ref              string                          `json:"ref"`
	Kind             string                          `json:"kind"`
	SourceKind       string                          `json:"sourceKind"`
	SourceID         string                          `json:"sourceId"`
	OccurredAt       int64                           `json:"occurredAt"`
	Summary          string                          `json:"summary"`
	ContentKind      string                          `json:"contentKind,omitempty"`
	Content          string                          `json:"content,omitempty"`
	ContentTruncated bool                            `json:"contentTruncated,omitempty"`
	Actor            string                          `json:"actor,omitempty"`
	Destination      *TaskLifecycleResultDestination `json:"destination,omitempty"`
}

type TaskLifecycleEpisodeAlternative struct {
	Task          string  `json:"task"`
	DesiredResult string  `json:"desiredResult"`
	Confidence    float64 `json:"confidence"`
}

// TaskLifecycleEpisodeSnapshot is the read-only episode state available at the
// observation cutoff. EvidenceRefs point only into PrefixEvidence. New suffix
// evidence is deliberately unassigned until normalization accepts one model
// observation.
type TaskLifecycleEpisodeSnapshot struct {
	EpisodeID               string                            `json:"episodeId"`
	CurrentStatus           string                            `json:"currentStatus"`
	InferredStartedAt       int64                             `json:"inferredStartedAt"`
	Task                    string                            `json:"task"`
	DesiredResult           string                            `json:"desiredResult"`
	TaskConfidence          float64                           `json:"taskConfidence"`
	BoundaryConfidence      float64                           `json:"boundaryConfidence"`
	EvidenceRefs            []string                          `json:"evidenceRefs"`
	KnownResultEvidenceRefs []string                          `json:"knownResultEvidenceRefs,omitempty"`
	Alternatives            []TaskLifecycleEpisodeAlternative `json:"alternatives,omitempty"`
}

// TaskLifecycleInput separates previously known prefix evidence from the
// newly observed suffix. Prefix evidence must occur at or before
// PrefixCutoffAt; suffix evidence must occur after it and no later than
// CutoffAt. This makes accidental future-evidence leakage a validation error.
type TaskLifecycleInput struct {
	SourceKind     string                         `json:"sourceKind"`
	SourceID       string                         `json:"sourceId"`
	PrefixCutoffAt int64                          `json:"prefixCutoffAt"`
	CutoffAt       int64                          `json:"cutoffAt"`
	Episodes       []TaskLifecycleEpisodeSnapshot `json:"episodes"`
	PrefixEvidence []TaskLifecycleEvidence        `json:"prefixEvidence"`
	SuffixEvidence []TaskLifecycleEvidence        `json:"suffixEvidence"`
}

// TaskLifecycleAlternative retains another source-compatible interpretation
// of the same evidence. It does not consume evidence independently; its
// evidence is exactly the parent observation's EvidenceRefs.
type TaskLifecycleAlternative struct {
	Kind             string   `json:"kind"`
	EpisodeIDs       []string `json:"episodeIds"`
	TargetEpisodeIDs []string `json:"targetEpisodeIds,omitempty"`
	Confidence       float64  `json:"confidence"`
	Reason           string   `json:"reason,omitempty"`
}

// TaskLifecycleMaterializedResult is reconstructed only from supplied
// production evidence. Model output cannot populate it.
type TaskLifecycleMaterializedResult struct {
	Workflow          string                          `json:"workflow"`
	Kind              string                          `json:"kind"`
	Content           string                          `json:"content"`
	SHA256            string                          `json:"sha256"`
	Truncated         bool                            `json:"truncated"`
	EvidenceRefs      []string                        `json:"evidenceRefs"`
	Destination       *TaskLifecycleResultDestination `json:"destination,omitempty"`
	DestinationSHA256 string                          `json:"destinationSha256,omitempty"`
}

// TaskLifecycleObservation is an append-only interpretation of new suffix
// evidence. ResultSummary is valid only for completed or result observations.
// A later result is represented by another Kind=result observation rather
// than replacing an earlier completion observation.
type TaskLifecycleObservation struct {
	LocalKey           string                           `json:"localKey"`
	Kind               string                           `json:"kind"`
	EpisodeIDs         []string                         `json:"episodeIds"`
	TargetEpisodeIDs   []string                         `json:"targetEpisodeIds,omitempty"`
	OccurredAt         int64                            `json:"occurredAt"`
	Confidence         float64                          `json:"confidence"`
	BoundaryConfidence float64                          `json:"boundaryConfidence"`
	ResultSummary      string                           `json:"resultSummary,omitempty"`
	ResultConfidence   float64                          `json:"resultConfidence,omitempty"`
	MaterializedResult *TaskLifecycleMaterializedResult `json:"materializedResult,omitempty"`
	EvidenceRefs       []string                         `json:"evidenceRefs"`
	Reason             string                           `json:"reason,omitempty"`
	Alternatives       []TaskLifecycleAlternative       `json:"alternatives,omitempty"`
}

// TaskLifecycleReasoningObservation is a model-selected pointer to separately
// retained user-authored evidence. Normalization replaces Statement with the
// literal SourceQuote so a model paraphrase cannot become an observed fact.
type TaskLifecycleReasoningObservation struct {
	LocalKey     string   `json:"localKey"`
	EpisodeID    string   `json:"episodeId"`
	Kind         string   `json:"kind"`
	Statement    string   `json:"statement"`
	SourceQuote  string   `json:"sourceQuote"`
	Confidence   float64  `json:"confidence"`
	EvidenceRefs []string `json:"evidenceRefs"`
	OccurredAt   int64    `json:"occurredAt,omitempty"`
}

type TaskLifecycleResponse struct {
	Observations           []TaskLifecycleObservation          `json:"observations"`
	ReasoningObservations  []TaskLifecycleReasoningObservation `json:"reasoningObservations,omitempty"`
	UnconsumedEvidenceRefs []string                            `json:"unconsumedEvidenceRefs"`
}
