package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"

	"yansu-agent/agent"
)

const (
	taskLifecyclePromptVersion     = "task-lifecycle-v7"
	taskLifecycleMaxEpisodes       = 24
	taskLifecycleMaxPrefixEvidence = 240
	taskLifecycleMaxSuffixEvidence = 200
	taskLifecycleMaxObservations   = 64
	taskLifecycleMaxAlternatives   = 3
	taskLifecycleMaxReasoning      = 16
	taskLifecycleContextBudget     = 256_000

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

func normalizeTaskLifecycleConfidence(value float64) float64 {
	switch {
	case math.IsNaN(value), math.IsInf(value, 0):
		return 0
	case value < 0:
		return 0
	case value > 1:
		return 1
	default:
		return value
	}
}

func normalizeTaskLifecycleKind(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "complete", TaskLifecycleKindCompleted:
		return TaskLifecycleKindCompleted
	case "continue", "active", "confirmed", TaskLifecycleKindContinued:
		return TaskLifecycleKindContinued
	case "pause", TaskLifecycleKindPaused:
		return TaskLifecycleKindPaused
	case "resume", TaskLifecycleKindResumed:
		return TaskLifecycleKindResumed
	case "abandon", "abandonment", TaskLifecycleKindAbandoned:
		return TaskLifecycleKindAbandoned
	case "switch", "task_switch", TaskLifecycleKindSwitched:
		return TaskLifecycleKindSwitched
	case "merge", TaskLifecycleKindMerged:
		return TaskLifecycleKindMerged
	case TaskLifecycleKindSplit:
		return TaskLifecycleKindSplit
	case "result_observed", TaskLifecycleKindResult:
		return TaskLifecycleKindResult
	case "unclear", "uncertain", TaskLifecycleKindUnknown:
		return TaskLifecycleKindUnknown
	default:
		return TaskLifecycleKindUnknown
	}
}

func taskLifecycleEvidenceEqual(left, right TaskLifecycleEvidence) bool {
	return left.Ref == right.Ref &&
		left.Kind == right.Kind &&
		left.SourceKind == right.SourceKind &&
		left.SourceID == right.SourceID &&
		left.OccurredAt == right.OccurredAt &&
		left.Summary == right.Summary &&
		left.ContentKind == right.ContentKind &&
		left.Content == right.Content &&
		left.ContentTruncated == right.ContentTruncated &&
		left.Actor == right.Actor &&
		taskLifecycleResultDestinationsEqual(left.Destination, right.Destination)
}

func normalizeTaskLifecycleEvidence(
	input []TaskLifecycleEvidence,
	phase string,
	prefixCutoffAt, cutoffAt int64,
	maxItems int,
) ([]TaskLifecycleEvidence, error) {
	if len(input) > maxItems {
		return nil, fmt.Errorf(
			"task lifecycle %s evidence has %d items, exceeds %d-item limit",
			phase, len(input), maxItems,
		)
	}

	items := append([]TaskLifecycleEvidence(nil), input...)
	for i := range items {
		items[i].Ref = strings.TrimSpace(items[i].Ref)
		items[i].Kind = strings.ToLower(strings.TrimSpace(items[i].Kind))
		items[i].SourceKind = strings.TrimSpace(items[i].SourceKind)
		items[i].SourceID = strings.TrimSpace(items[i].SourceID)
		items[i].Summary = strings.TrimSpace(items[i].Summary)
		items[i].ContentKind = strings.ToLower(
			strings.TrimSpace(items[i].ContentKind),
		)
		items[i].Content = strings.TrimSpace(items[i].Content)
		items[i].Actor = strings.ToLower(strings.TrimSpace(items[i].Actor))
		if items[i].Actor == "" {
			items[i].Actor = TaskLifecycleActorUnknown
		}
		switch items[i].Actor {
		case TaskLifecycleActorUser,
			TaskLifecycleActorOtherParty,
			TaskLifecycleActorSystem,
			TaskLifecycleActorUnknown:
		default:
			return nil, fmt.Errorf(
				"task lifecycle %s evidence at index %d has unsupported actor %q",
				phase,
				i,
				items[i].Actor,
			)
		}
		if (items[i].ContentKind == "") != (items[i].Content == "") {
			return nil, fmt.Errorf(
				"task lifecycle %s evidence at index %d has incomplete structured content",
				phase, i,
			)
		}
		if items[i].ContentKind != "" &&
			items[i].ContentKind != TaskLifecycleContentCommittedText {
			return nil, fmt.Errorf(
				"task lifecycle %s evidence at index %d has unsupported content kind %q",
				phase, i, items[i].ContentKind,
			)
		}
		var err error
		items[i].Destination, err = normalizeTaskLifecycleResultDestination(
			items[i].Destination,
		)
		if err != nil {
			return nil, fmt.Errorf(
				"task lifecycle %s evidence at index %d has invalid destination: %w",
				phase, i, err,
			)
		}
		if items[i].Destination != nil &&
			items[i].ContentKind != TaskLifecycleContentCommittedText {
			return nil, fmt.Errorf(
				"task lifecycle %s evidence at index %d has destination without committed text",
				phase, i,
			)
		}
		if items[i].Ref == "" ||
			items[i].Kind == "" ||
			items[i].SourceKind == "" ||
			items[i].SourceID == "" ||
			items[i].Summary == "" ||
			items[i].OccurredAt <= 0 {
			return nil, fmt.Errorf(
				"task lifecycle %s evidence at index %d is incomplete",
				phase, i,
			)
		}
		switch phase {
		case "prefix":
			if items[i].OccurredAt > prefixCutoffAt {
				return nil, fmt.Errorf(
					"task lifecycle prefix evidence %q at %d is after prefix cutoff %d",
					items[i].Ref, items[i].OccurredAt, prefixCutoffAt,
				)
			}
		case "suffix":
			if items[i].OccurredAt <= prefixCutoffAt ||
				items[i].OccurredAt > cutoffAt {
				return nil, fmt.Errorf(
					"task lifecycle suffix evidence %q at %d is outside (%d,%d]",
					items[i].Ref, items[i].OccurredAt, prefixCutoffAt, cutoffAt,
				)
			}
		default:
			return nil, fmt.Errorf("unsupported task lifecycle evidence phase %q", phase)
		}
	}

	sort.SliceStable(items, func(i, j int) bool {
		if items[i].OccurredAt != items[j].OccurredAt {
			return items[i].OccurredAt < items[j].OccurredAt
		}
		if items[i].Ref != items[j].Ref {
			return items[i].Ref < items[j].Ref
		}
		if items[i].SourceKind != items[j].SourceKind {
			return items[i].SourceKind < items[j].SourceKind
		}
		return items[i].SourceID < items[j].SourceID
	})

	seenRefs := make(map[string]TaskLifecycleEvidence, len(items))
	seenSources := make(map[string]TaskLifecycleEvidence, len(items))
	normalized := make([]TaskLifecycleEvidence, 0, len(items))
	for _, item := range items {
		if existing, ok := seenRefs[item.Ref]; ok {
			if !taskLifecycleEvidenceEqual(existing, item) {
				return nil, fmt.Errorf(
					"task lifecycle evidence ref %q has conflicting payloads",
					item.Ref,
				)
			}
			continue
		}
		sourceKey := strings.Join([]string{
			item.SourceKind,
			item.SourceID,
			fmt.Sprint(item.OccurredAt),
		}, "\x00")
		if existing, ok := seenSources[sourceKey]; ok {
			if !taskLifecycleEvidenceEqual(existing, item) {
				return nil, fmt.Errorf(
					"task lifecycle source %q/%q at %d is represented more than once",
					item.SourceKind, item.SourceID, item.OccurredAt,
				)
			}
			continue
		}
		seenRefs[item.Ref] = item
		seenSources[sourceKey] = item
		normalized = append(normalized, item)
	}
	return normalized, nil
}

func canonicalTaskLifecycleInputRefs(
	refs []string,
	allowed map[string]TaskLifecycleEvidence,
) ([]string, error) {
	seen := make(map[string]struct{}, len(refs))
	normalized := make([]string, 0, len(refs))
	for _, ref := range refs {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}
		if _, ok := allowed[ref]; !ok {
			return nil, fmt.Errorf("task lifecycle episode references unknown prefix evidence %q", ref)
		}
		if _, ok := seen[ref]; ok {
			continue
		}
		seen[ref] = struct{}{}
		normalized = append(normalized, ref)
	}
	sort.Strings(normalized)
	return normalized, nil
}

func normalizeTaskLifecycleInput(input TaskLifecycleInput) (TaskLifecycleInput, error) {
	input.SourceKind = strings.TrimSpace(input.SourceKind)
	input.SourceID = strings.TrimSpace(input.SourceID)
	if input.SourceKind == "" || input.SourceID == "" {
		return TaskLifecycleInput{}, errors.New(
			"task lifecycle observation source kind and id are required",
		)
	}
	if input.PrefixCutoffAt <= 0 ||
		input.CutoffAt <= input.PrefixCutoffAt {
		return TaskLifecycleInput{}, errors.New(
			"task lifecycle observation requires an increasing prefix and observation cutoff",
		)
	}
	if len(input.Episodes) > taskLifecycleMaxEpisodes {
		return TaskLifecycleInput{}, fmt.Errorf(
			"task lifecycle observation has %d episodes, exceeds %d-episode limit",
			len(input.Episodes), taskLifecycleMaxEpisodes,
		)
	}

	var err error
	input.PrefixEvidence, err = normalizeTaskLifecycleEvidence(
		input.PrefixEvidence,
		"prefix",
		input.PrefixCutoffAt,
		input.CutoffAt,
		taskLifecycleMaxPrefixEvidence,
	)
	if err != nil {
		return TaskLifecycleInput{}, err
	}
	input.SuffixEvidence, err = normalizeTaskLifecycleEvidence(
		input.SuffixEvidence,
		"suffix",
		input.PrefixCutoffAt,
		input.CutoffAt,
		taskLifecycleMaxSuffixEvidence,
	)
	if err != nil {
		return TaskLifecycleInput{}, err
	}

	prefixByRef := make(map[string]TaskLifecycleEvidence, len(input.PrefixEvidence))
	allSourceKeys := make(map[string]string, len(input.PrefixEvidence)+len(input.SuffixEvidence))
	for _, item := range input.PrefixEvidence {
		prefixByRef[item.Ref] = item
		allSourceKeys[strings.Join([]string{
			item.SourceKind,
			item.SourceID,
			fmt.Sprint(item.OccurredAt),
		}, "\x00")] = item.Ref
	}
	for _, item := range input.SuffixEvidence {
		if _, exists := prefixByRef[item.Ref]; exists {
			return TaskLifecycleInput{}, fmt.Errorf(
				"task lifecycle evidence ref %q appears in both prefix and suffix",
				item.Ref,
			)
		}
		sourceKey := strings.Join([]string{
			item.SourceKind,
			item.SourceID,
			fmt.Sprint(item.OccurredAt),
		}, "\x00")
		if priorRef, exists := allSourceKeys[sourceKey]; exists {
			return TaskLifecycleInput{}, fmt.Errorf(
				"task lifecycle source evidence %q duplicates %q across prefix and suffix",
				item.Ref, priorRef,
			)
		}
		allSourceKeys[sourceKey] = item.Ref
	}

	episodes := append([]TaskLifecycleEpisodeSnapshot(nil), input.Episodes...)
	seenEpisodeIDs := make(map[string]struct{}, len(episodes))
	for i := range episodes {
		episode := &episodes[i]
		episode.EpisodeID = strings.TrimSpace(episode.EpisodeID)
		episode.Task = strings.TrimSpace(episode.Task)
		episode.DesiredResult = strings.TrimSpace(episode.DesiredResult)
		if episode.EpisodeID == "" ||
			episode.Task == "" ||
			episode.DesiredResult == "" ||
			episode.InferredStartedAt <= 0 ||
			episode.InferredStartedAt > input.PrefixCutoffAt {
			return TaskLifecycleInput{}, fmt.Errorf(
				"task lifecycle episode at index %d is incomplete or outside the prefix snapshot",
				i,
			)
		}
		if _, exists := seenEpisodeIDs[episode.EpisodeID]; exists {
			return TaskLifecycleInput{}, fmt.Errorf(
				"task lifecycle episode %q appears more than once",
				episode.EpisodeID,
			)
		}
		seenEpisodeIDs[episode.EpisodeID] = struct{}{}
		episode.CurrentStatus, err = normalizeTaskEpisodeStatus(episode.CurrentStatus)
		if err != nil {
			return TaskLifecycleInput{}, err
		}
		episode.TaskConfidence = normalizeTaskLifecycleConfidence(episode.TaskConfidence)
		episode.BoundaryConfidence = normalizeTaskLifecycleConfidence(
			episode.BoundaryConfidence,
		)
		episode.EvidenceRefs, err = canonicalTaskLifecycleInputRefs(
			episode.EvidenceRefs,
			prefixByRef,
		)
		if err != nil {
			return TaskLifecycleInput{}, err
		}
		if len(episode.EvidenceRefs) == 0 {
			return TaskLifecycleInput{}, fmt.Errorf(
				"task lifecycle episode %q requires prefix evidence",
				episode.EpisodeID,
			)
		}
		episode.KnownResultEvidenceRefs, err = canonicalTaskLifecycleInputRefs(
			episode.KnownResultEvidenceRefs,
			prefixByRef,
		)
		if err != nil {
			return TaskLifecycleInput{}, err
		}
		episodeEvidence := make(map[string]struct{}, len(episode.EvidenceRefs))
		for _, ref := range episode.EvidenceRefs {
			episodeEvidence[ref] = struct{}{}
		}
		for _, ref := range episode.KnownResultEvidenceRefs {
			if _, ok := episodeEvidence[ref]; !ok {
				return TaskLifecycleInput{}, fmt.Errorf(
					"task lifecycle episode %q result evidence %q is not attached to the episode",
					episode.EpisodeID,
					ref,
				)
			}
		}

		alternatives := append(
			[]TaskLifecycleEpisodeAlternative(nil),
			episode.Alternatives...,
		)
		for j := range alternatives {
			alternatives[j].Task = strings.TrimSpace(alternatives[j].Task)
			alternatives[j].DesiredResult = strings.TrimSpace(
				alternatives[j].DesiredResult,
			)
			alternatives[j].Confidence = normalizeTaskLifecycleConfidence(
				alternatives[j].Confidence,
			)
		}
		sort.SliceStable(alternatives, func(left, right int) bool {
			if alternatives[left].Confidence != alternatives[right].Confidence {
				return alternatives[left].Confidence > alternatives[right].Confidence
			}
			if alternatives[left].Task != alternatives[right].Task {
				return alternatives[left].Task < alternatives[right].Task
			}
			return alternatives[left].DesiredResult < alternatives[right].DesiredResult
		})
		episode.Alternatives = episode.Alternatives[:0]
		for _, alternative := range alternatives {
			if alternative.Task == "" || alternative.DesiredResult == "" {
				continue
			}
			episode.Alternatives = append(episode.Alternatives, alternative)
			if len(episode.Alternatives) == taskLifecycleMaxAlternatives {
				break
			}
		}
	}
	sort.SliceStable(episodes, func(i, j int) bool {
		if episodes[i].InferredStartedAt != episodes[j].InferredStartedAt {
			return episodes[i].InferredStartedAt < episodes[j].InferredStartedAt
		}
		return episodes[i].EpisodeID < episodes[j].EpisodeID
	})
	input.Episodes = episodes
	return input, nil
}

func buildTaskLifecyclePrompt(
	input TaskLifecycleInput,
) (string, TaskLifecycleInput, error) {
	normalized, err := normalizeTaskLifecycleInput(input)
	if err != nil {
		return "", TaskLifecycleInput{}, err
	}
	payload, err := json.Marshal(normalized)
	if err != nil {
		return "", TaskLifecycleInput{}, fmt.Errorf(
			"marshal task lifecycle input: %w",
			err,
		)
	}
	if len(payload) > taskLifecycleContextBudget {
		return "", TaskLifecycleInput{}, fmt.Errorf(
			"task lifecycle context is %d bytes, exceeds %d-byte budget",
			len(payload), taskLifecycleContextBudget,
		)
	}

	prompt := fmt.Sprintf(`You are Yansu's task-lifecycle observer.

Infer only what the user demonstrably did between prefixCutoffAt and cutoffAt.
Prefix evidence is prior context. Suffix evidence is newly observed production
evidence. This is an observation pass, not a recommendation, Initiative,
permission decision, or execution request.

Hard rules:
1. Use only supplied evidence at or before cutoffAt. Never infer a later event.
2. Multiple TaskEpisodes may overlap or interleave. Treat each independently.
3. App switching, saving, closing a window, silence, or idle time alone does
   not prove completion or abandonment.
4. A result requires an observable artifact, delivery, status, test result, or
   explicit outcome. Do not invent a result from activity.
5. A late result for an already-completed episode is kind=result. Do not emit
   completed again and do not replace earlier result evidence.
6. EvidenceRefs may contain only exact suffix evidence refs. Prefix refs are
   context only. Each suffix ref may appear in at most one observation.
7. Use kind=unknown with alternatives when completion, state, scope, or task
   identity is not supported. Absence of evidence is not negative evidence.
8. continued confirms that a candidate task is still being performed. It uses
   one episodeId and source-backed action evidence.
9. completed, continued, paused, resumed, abandoned, and result use one episodeId.
10. switched uses one source episodeId and one targetEpisodeId.
11. merged uses one or more source episodeIds and exactly one distinct target.
12. split uses one source episodeId and at least two distinct targets.
13. Use only exact supplied episode IDs. Do not create episodes or IDs.
14. occurredAt must be supported by the cited suffix evidence. Confidence and
    boundaryConfidence are numbers from 0 to 1.
15. resultSummary must be empty unless kind is completed or result.
16. Preserve plausible alternatives instead of forcing certainty.
17. text_commit_candidate means a keyboard burst ended with Return or
    Command+Return. It is ambiguous: Return may insert a newline. Treat it as
    terminal by itself only for a known messaging surface where Return sends.
    In email, document, note, and other editor surfaces, require separate sent,
    submission, published, or explicit-completion evidence. destination is
    backend-retained source provenance. Only
    semanticRole=user_result_candidate with captureMethod=ax_field_diff may
    form a result; delegated_ai_prompt and unknown never do. Do not infer or
    rewrite destination fields.
18. For a direct text-composition completion, cite both the committed_text
    evidence that forms the user's result and the terminal completion evidence.
    One text_commit_candidate may satisfy both only when it carries the exact
    committed text and its destination is a known send-on-Return messaging
    surface.
19. reasoningObservations is separate from lifecycle state. It may identify a
    stated_rationale or correction, but it cannot complete, continue, pause,
    abandon, merge, split, or otherwise change an episode.
20. A reasoning observation requires actor=user and sourceQuote copied
    literally from cited user keyboard content or user speech. Never use
    other_party, system, unknown, app, assistant, or model-authored content.
21. stated_rationale requires an explicit rationale cue such as because, so
    that, in order to, or the reason. correction requires explicit correction
    language. If either is absent, emit no reasoning observation.
22. statement is only a cautious normalization. The backend replaces it with
    the literal quote before persistence, so do not add unsupported meaning.

Return strict JSON only:
{
  "observations": [{
    "localKey": "stable key unique in this response",
    "kind": "completed|continued|paused|resumed|abandoned|switched|merged|split|result|unknown",
    "episodeIds": ["exact-episode-id"],
    "targetEpisodeIds": [],
    "occurredAt": 0,
    "confidence": 0.0,
    "boundaryConfidence": 0.0,
    "resultSummary": "",
    "resultConfidence": 0.0,
    "evidenceRefs": ["exact-suffix-ref"],
    "reason": "short source-backed explanation",
    "alternatives": [{
      "kind": "completed|continued|paused|resumed|abandoned|switched|merged|split|result|unknown",
      "episodeIds": ["exact-episode-id"],
      "targetEpisodeIds": [],
      "confidence": 0.0,
      "reason": "why this interpretation remains plausible"
    }]
  }],
  "reasoningObservations": [{
    "localKey": "stable reasoning key",
    "episodeId": "exact-episode-id",
    "kind": "stated_rationale|correction",
    "statement": "cautious normalization of the quote",
    "sourceQuote": "literal substring copied from user evidence",
    "confidence": 0.0,
    "evidenceRefs": ["exact-suffix-ref"]
  }]
}

Prompt version: %s

Observation:
%s`, taskLifecyclePromptVersion, string(payload))
	return prompt, normalized, nil
}

func canonicalTaskLifecycleOutputIDs(
	ids []string,
	allowed map[string]TaskLifecycleEpisodeSnapshot,
) []string {
	seen := make(map[string]struct{}, len(ids))
	normalized := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if _, ok := allowed[id]; !ok {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		normalized = append(normalized, id)
	}
	sort.Strings(normalized)
	return normalized
}

func canonicalTaskLifecycleOutputRefs(
	refs []string,
	allowed map[string]TaskLifecycleEvidence,
) []string {
	seen := make(map[string]struct{}, len(refs))
	normalized := make([]string, 0, len(refs))
	for _, ref := range refs {
		ref = strings.TrimSpace(ref)
		if _, ok := allowed[ref]; !ok {
			continue
		}
		if _, ok := seen[ref]; ok {
			continue
		}
		seen[ref] = struct{}{}
		normalized = append(normalized, ref)
	}
	sort.Strings(normalized)
	return normalized
}

func taskLifecycleIDsDisjoint(left, right []string) bool {
	seen := make(map[string]struct{}, len(left))
	for _, id := range left {
		seen[id] = struct{}{}
	}
	for _, id := range right {
		if _, ok := seen[id]; ok {
			return false
		}
	}
	return true
}

func taskLifecycleShapeAllowed(
	kind string,
	episodeIDs, targetEpisodeIDs []string,
) bool {
	switch kind {
	case TaskLifecycleKindCompleted,
		TaskLifecycleKindContinued,
		TaskLifecycleKindPaused,
		TaskLifecycleKindResumed,
		TaskLifecycleKindAbandoned,
		TaskLifecycleKindResult:
		return len(episodeIDs) == 1 && len(targetEpisodeIDs) == 0
	case TaskLifecycleKindSwitched:
		return len(episodeIDs) == 1 &&
			len(targetEpisodeIDs) == 1 &&
			taskLifecycleIDsDisjoint(episodeIDs, targetEpisodeIDs)
	case TaskLifecycleKindMerged:
		return len(episodeIDs) >= 1 &&
			len(targetEpisodeIDs) == 1 &&
			taskLifecycleIDsDisjoint(episodeIDs, targetEpisodeIDs)
	case TaskLifecycleKindSplit:
		return len(episodeIDs) == 1 &&
			len(targetEpisodeIDs) >= 2 &&
			taskLifecycleIDsDisjoint(episodeIDs, targetEpisodeIDs)
	case TaskLifecycleKindUnknown:
		return len(episodeIDs) >= 1
	default:
		return false
	}
}

func taskLifecycleEpisodeOpen(status string) bool {
	return status == TaskEpisodeStatusCandidate ||
		status == TaskEpisodeStatusActive ||
		status == TaskEpisodeStatusPaused
}

func taskLifecycleStatusAllowed(
	kind string,
	episodeIDs, targetEpisodeIDs []string,
	episodes map[string]TaskLifecycleEpisodeSnapshot,
) bool {
	switch kind {
	case TaskLifecycleKindCompleted:
		status := episodes[episodeIDs[0]].CurrentStatus
		return status == TaskEpisodeStatusCandidate ||
			status == TaskEpisodeStatusActive ||
			status == TaskEpisodeStatusPaused
	case TaskLifecycleKindContinued:
		status := episodes[episodeIDs[0]].CurrentStatus
		return status == TaskEpisodeStatusCandidate ||
			status == TaskEpisodeStatusActive
	case TaskLifecycleKindPaused:
		return episodes[episodeIDs[0]].CurrentStatus == TaskEpisodeStatusActive
	case TaskLifecycleKindResumed:
		return episodes[episodeIDs[0]].CurrentStatus == TaskEpisodeStatusPaused
	case TaskLifecycleKindAbandoned:
		return taskLifecycleEpisodeOpen(episodes[episodeIDs[0]].CurrentStatus)
	case TaskLifecycleKindSwitched:
		source := episodes[episodeIDs[0]].CurrentStatus
		target := episodes[targetEpisodeIDs[0]].CurrentStatus
		return (source == TaskEpisodeStatusActive || source == TaskEpisodeStatusPaused) &&
			taskLifecycleEpisodeOpen(target)
	case TaskLifecycleKindMerged, TaskLifecycleKindSplit:
		for _, id := range append(
			append([]string(nil), episodeIDs...),
			targetEpisodeIDs...,
		) {
			if !taskLifecycleEpisodeOpen(episodes[id].CurrentStatus) {
				return false
			}
		}
		return true
	case TaskLifecycleKindResult, TaskLifecycleKindUnknown:
		return true
	default:
		return false
	}
}

func taskLifecycleStrongCompletionKind(kind string) bool {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "completion",
		"explicit_completion",
		"result",
		"artifact_result",
		"delivery",
		"submission",
		"sent",
		"published",
		"merged",
		"test_result",
		"terminal_result",
		"success_state",
		"failure_state",
		TaskLifecycleEvidenceTextCommit:
		return true
	default:
		return false
	}
}

func taskLifecycleStrongResultKind(kind string) bool {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "result",
		"artifact_result",
		"delivery",
		"submission",
		"sent",
		"published",
		"merged",
		"test_result",
		"terminal_result",
		"success_state",
		"failure_state",
		TaskLifecycleEvidenceTextCommit:
		return true
	default:
		return false
	}
}

func taskLifecycleStrongAbandonmentKind(kind string) bool {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "abandonment",
		"explicit_abandonment",
		"cancelled",
		"discarded",
		"deadline_expired":
		return true
	default:
		return false
	}
}

func taskLifecycleHasEvidenceKind(
	refs []string,
	evidence map[string]TaskLifecycleEvidence,
	matches func(string) bool,
) bool {
	for _, ref := range refs {
		if matches(evidence[ref].Kind) {
			return true
		}
	}
	return false
}

func taskLifecycleTextCommitSupportsResult(
	observation TaskLifecycleObservation,
	item TaskLifecycleEvidence,
	episodes map[string]TaskLifecycleEpisodeSnapshot,
) bool {
	if item.Kind != TaskLifecycleEvidenceTextCommit ||
		item.SourceKind != "activity_event" ||
		item.ContentKind != TaskLifecycleContentCommittedText ||
		item.Actor != TaskLifecycleActorUser ||
		strings.TrimSpace(item.Content) == "" ||
		len(observation.EpisodeIDs) != 1 {
		return false
	}
	if _, ok := taskLifecycleCurrentUserResultDestination(item.Destination); !ok {
		return false
	}
	episode, ok := episodes[observation.EpisodeIDs[0]]
	if !ok {
		return false
	}
	return taskWorkflowTextResult(taskWorkflowCapabilityKey(
		episode.Task,
		episode.DesiredResult,
		episode.EpisodeID,
	))
}

func taskLifecycleHasSupportedTerminalEvidence(
	observation TaskLifecycleObservation,
	refs []string,
	evidence map[string]TaskLifecycleEvidence,
	episodes map[string]TaskLifecycleEpisodeSnapshot,
	resultOnly bool,
) bool {
	for _, ref := range refs {
		item := evidence[ref]
		if item.Kind == TaskLifecycleEvidenceTextCommit {
			if taskLifecycleTextCommitSupportsResult(
				observation,
				item,
				episodes,
			) && taskLifecycleTextCommitCanTerminate(item.Destination) {
				return true
			}
			continue
		}
		if resultOnly {
			if taskLifecycleStrongResultKind(item.Kind) {
				return true
			}
			continue
		}
		if taskLifecycleStrongCompletionKind(item.Kind) {
			return true
		}
	}
	return false
}

func taskLifecycleHasNonTextTerminalEvidence(
	refs []string,
	evidence map[string]TaskLifecycleEvidence,
	resultOnly bool,
) bool {
	for _, ref := range refs {
		item := evidence[ref]
		if item.Kind == TaskLifecycleEvidenceTextCommit {
			continue
		}
		if resultOnly {
			if taskLifecycleStrongResultKind(item.Kind) {
				return true
			}
			continue
		}
		if taskLifecycleStrongCompletionKind(item.Kind) {
			return true
		}
	}
	return false
}

func taskLifecycleTextResultDestinationConflict(
	observation TaskLifecycleObservation,
	refs []string,
	evidence map[string]TaskLifecycleEvidence,
	episodes map[string]TaskLifecycleEpisodeSnapshot,
) bool {
	var first *TaskLifecycleResultDestination
	for _, ref := range refs {
		item := evidence[ref]
		if !taskLifecycleTextCommitSupportsResult(
			observation,
			item,
			episodes,
		) {
			continue
		}
		destination, ok := taskLifecycleCurrentUserResultDestination(
			item.Destination,
		)
		if !ok {
			continue
		}
		if first == nil {
			first = destination
			continue
		}
		if !taskLifecycleResultDestinationsEqual(first, destination) {
			return true
		}
	}
	return false
}

func materializeTaskLifecycleTextResult(
	observation TaskLifecycleObservation,
	evidence map[string]TaskLifecycleEvidence,
	episodes map[string]TaskLifecycleEpisodeSnapshot,
) *TaskLifecycleMaterializedResult {
	if len(observation.EpisodeIDs) != 1 ||
		(observation.Kind != TaskLifecycleKindCompleted &&
			observation.Kind != TaskLifecycleKindResult) {
		return nil
	}
	episode, ok := episodes[observation.EpisodeIDs[0]]
	if !ok {
		return nil
	}
	workflow := taskWorkflowCapabilityKey(
		episode.Task,
		episode.DesiredResult,
		episode.EpisodeID,
	)
	if !taskWorkflowTextResult(workflow) ||
		!taskLifecycleHasSupportedTerminalEvidence(
			observation,
			observation.EvidenceRefs,
			evidence,
			episodes,
			false,
		) {
		return nil
	}

	content := make([]string, 0, len(observation.EvidenceRefs))
	refs := make([]string, 0, len(observation.EvidenceRefs))
	seen := make(map[string]struct{})
	truncated := false
	var destination *TaskLifecycleResultDestination
	for _, ref := range observation.EvidenceRefs {
		item := evidence[ref]
		if !taskLifecycleTextCommitSupportsResult(
			observation,
			item,
			episodes,
		) {
			continue
		}
		itemDestination, ok := taskLifecycleCurrentUserResultDestination(
			item.Destination,
		)
		if !ok {
			continue
		}
		if destination == nil {
			copyDestination := *itemDestination
			destination = &copyDestination
		} else if !taskLifecycleResultDestinationsEqual(
			destination,
			itemDestination,
		) {
			return nil
		}
		if _, duplicate := seen[item.Content]; duplicate {
			continue
		}
		seen[item.Content] = struct{}{}
		content = append(content, item.Content)
		refs = append(refs, ref)
		truncated = truncated || item.ContentTruncated
	}
	if len(content) == 0 || destination == nil {
		return nil
	}
	body := strings.TrimSpace(strings.Join(content, "\n"))
	if body == "" {
		return nil
	}
	destinationSHA256, err := taskLifecycleResultDestinationSHA256(destination)
	if err != nil {
		return nil
	}
	return &TaskLifecycleMaterializedResult{
		Workflow:          workflow,
		Kind:              TaskLifecycleContentCommittedText,
		Content:           body,
		SHA256:            sha256Hex([]byte(body)),
		Truncated:         truncated,
		EvidenceRefs:      refs,
		Destination:       destination,
		DestinationSHA256: destinationSHA256,
	}
}

func normalizeTaskLifecycleAlternatives(
	alternatives []TaskLifecycleAlternative,
	episodes map[string]TaskLifecycleEpisodeSnapshot,
) []TaskLifecycleAlternative {
	normalized := make([]TaskLifecycleAlternative, 0, len(alternatives))
	seen := make(map[string]struct{}, len(alternatives))
	for _, alternative := range alternatives {
		alternative.Kind = normalizeTaskLifecycleKind(alternative.Kind)
		alternative.EpisodeIDs = canonicalTaskLifecycleOutputIDs(
			alternative.EpisodeIDs,
			episodes,
		)
		alternative.TargetEpisodeIDs = canonicalTaskLifecycleOutputIDs(
			alternative.TargetEpisodeIDs,
			episodes,
		)
		alternative.Confidence = normalizeTaskLifecycleConfidence(
			alternative.Confidence,
		)
		alternative.Reason = strings.TrimSpace(alternative.Reason)
		if !taskLifecycleShapeAllowed(
			alternative.Kind,
			alternative.EpisodeIDs,
			alternative.TargetEpisodeIDs,
		) {
			continue
		}
		key := strings.Join([]string{
			alternative.Kind,
			strings.Join(alternative.EpisodeIDs, ","),
			strings.Join(alternative.TargetEpisodeIDs, ","),
			alternative.Reason,
		}, "\x00")
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		normalized = append(normalized, alternative)
	}
	sort.SliceStable(normalized, func(i, j int) bool {
		if normalized[i].Confidence != normalized[j].Confidence {
			return normalized[i].Confidence > normalized[j].Confidence
		}
		leftKey := strings.Join([]string{
			normalized[i].Kind,
			strings.Join(normalized[i].EpisodeIDs, ","),
			strings.Join(normalized[i].TargetEpisodeIDs, ","),
			normalized[i].Reason,
		}, "\x00")
		rightKey := strings.Join([]string{
			normalized[j].Kind,
			strings.Join(normalized[j].EpisodeIDs, ","),
			strings.Join(normalized[j].TargetEpisodeIDs, ","),
			normalized[j].Reason,
		}, "\x00")
		return leftKey < rightKey
	})
	if len(normalized) > taskLifecycleMaxAlternatives {
		normalized = normalized[:taskLifecycleMaxAlternatives]
	}
	return normalized
}

func taskLifecycleKindOrder(kind string) int {
	switch kind {
	case TaskLifecycleKindCompleted:
		return 0
	case TaskLifecycleKindResult:
		return 1
	case TaskLifecycleKindMerged:
		return 2
	case TaskLifecycleKindSplit:
		return 3
	case TaskLifecycleKindSwitched:
		return 4
	case TaskLifecycleKindContinued:
		return 5
	case TaskLifecycleKindPaused:
		return 6
	case TaskLifecycleKindResumed:
		return 7
	case TaskLifecycleKindAbandoned:
		return 8
	default:
		return 9
	}
}

func taskLifecycleObservationLess(
	left, right TaskLifecycleObservation,
) bool {
	if left.OccurredAt != right.OccurredAt {
		return left.OccurredAt < right.OccurredAt
	}
	if taskLifecycleKindOrder(left.Kind) != taskLifecycleKindOrder(right.Kind) {
		return taskLifecycleKindOrder(left.Kind) < taskLifecycleKindOrder(right.Kind)
	}
	leftKey := strings.Join([]string{
		strings.Join(left.EpisodeIDs, ","),
		strings.Join(left.TargetEpisodeIDs, ","),
		left.LocalKey,
		left.Reason,
	}, "\x00")
	rightKey := strings.Join([]string{
		strings.Join(right.EpisodeIDs, ","),
		strings.Join(right.TargetEpisodeIDs, ","),
		right.LocalKey,
		right.Reason,
	}, "\x00")
	return leftKey < rightKey
}

func taskLifecycleEvidenceTime(
	refs []string,
	evidence map[string]TaskLifecycleEvidence,
) int64 {
	var occurredAt int64
	for _, ref := range refs {
		if evidence[ref].OccurredAt > occurredAt {
			occurredAt = evidence[ref].OccurredAt
		}
	}
	return occurredAt
}

func normalizeTaskLifecycleReasoningKind(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case TaskLifecycleReasoningStatedRationale:
		return TaskLifecycleReasoningStatedRationale
	case TaskLifecycleReasoningCorrection:
		return TaskLifecycleReasoningCorrection
	default:
		return ""
	}
}

func taskLifecycleReasoningHasCorrectionCue(value string) bool {
	value = normalizeTaskReasoningText(value)
	for _, cue := range []string{
		"actually",
		"instead",
		"rather than",
		"not because",
		"i changed",
		"we changed",
		"that's wrong",
		"that is wrong",
		"i was wrong",
		"correction",
	} {
		if strings.Contains(value, cue) {
			return true
		}
	}
	return false
}

func taskLifecycleReasoningSourceText(
	item TaskLifecycleEvidence,
) (string, bool) {
	if item.Actor != TaskLifecycleActorUser {
		return "", false
	}
	if item.ContentKind == TaskLifecycleContentCommittedText &&
		strings.TrimSpace(item.Content) != "" {
		return item.Content, true
	}
	if strings.Contains(
		strings.ToLower(strings.TrimSpace(item.Kind)),
		"speech",
	) || strings.HasPrefix(strings.TrimSpace(item.Summary), "User speech ") {
		return item.Summary, true
	}
	return "", false
}

func taskLifecycleReasoningQuoteSupported(
	observation TaskLifecycleReasoningObservation,
	evidence map[string]TaskLifecycleEvidence,
) bool {
	quote := normalizeTaskReasoningText(observation.SourceQuote)
	if quote == "" {
		return false
	}
	switch observation.Kind {
	case TaskLifecycleReasoningStatedRationale:
		if !taskOnsetReasoningHasExplicitCue(quote) {
			return false
		}
	case TaskLifecycleReasoningCorrection:
		if !taskLifecycleReasoningHasCorrectionCue(quote) {
			return false
		}
	default:
		return false
	}
	for _, ref := range observation.EvidenceRefs {
		source, ok := taskLifecycleReasoningSourceText(evidence[ref])
		if ok && strings.Contains(normalizeTaskReasoningText(source), quote) {
			return true
		}
	}
	return false
}

func normalizeTaskLifecycleReasoningObservations(
	input TaskLifecycleInput,
	observations []TaskLifecycleReasoningObservation,
) []TaskLifecycleReasoningObservation {
	episodes := make(map[string]TaskLifecycleEpisodeSnapshot, len(input.Episodes))
	for _, episode := range input.Episodes {
		episodes[episode.EpisodeID] = episode
	}
	suffix := make(map[string]TaskLifecycleEvidence, len(input.SuffixEvidence))
	for _, item := range input.SuffixEvidence {
		suffix[item.Ref] = item
	}

	seenKeys := make(map[string]struct{}, len(observations))
	seenEvidence := make(map[string]struct{}, len(observations))
	result := make(
		[]TaskLifecycleReasoningObservation,
		0,
		min(len(observations), taskLifecycleMaxReasoning),
	)
	for _, observation := range observations {
		observation.LocalKey = strings.TrimSpace(observation.LocalKey)
		observation.EpisodeID = strings.TrimSpace(observation.EpisodeID)
		observation.Kind = normalizeTaskLifecycleReasoningKind(observation.Kind)
		observation.Statement = strings.TrimSpace(observation.Statement)
		observation.SourceQuote = strings.TrimSpace(observation.SourceQuote)
		observation.Confidence = normalizeTaskLifecycleConfidence(
			observation.Confidence,
		)
		observation.EvidenceRefs = canonicalTaskLifecycleOutputRefs(
			observation.EvidenceRefs,
			suffix,
		)
		if observation.LocalKey == "" || observation.Kind == "" ||
			len(observation.EvidenceRefs) == 0 {
			continue
		}
		if _, exists := episodes[observation.EpisodeID]; !exists {
			continue
		}
		if _, duplicate := seenKeys[observation.LocalKey]; duplicate {
			continue
		}
		if !taskLifecycleReasoningQuoteSupported(observation, suffix) {
			continue
		}
		// The observed fact is the user's quote. A model paraphrase remains only
		// in the immutable raw attempt and cannot become evidence text.
		observation.Statement = observation.SourceQuote
		observation.OccurredAt = taskLifecycleEvidenceTime(
			observation.EvidenceRefs,
			suffix,
		)
		identity := strings.Join([]string{
			observation.EpisodeID,
			observation.Kind,
			normalizeTaskReasoningText(observation.SourceQuote),
			strings.Join(observation.EvidenceRefs, ","),
		}, "\x00")
		if _, duplicate := seenEvidence[identity]; duplicate {
			continue
		}
		seenKeys[observation.LocalKey] = struct{}{}
		seenEvidence[identity] = struct{}{}
		result = append(result, observation)
		if len(result) == taskLifecycleMaxReasoning {
			break
		}
	}
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].OccurredAt != result[j].OccurredAt {
			return result[i].OccurredAt < result[j].OccurredAt
		}
		if result[i].EpisodeID != result[j].EpisodeID {
			return result[i].EpisodeID < result[j].EpisodeID
		}
		if result[i].Kind != result[j].Kind {
			return result[i].Kind < result[j].Kind
		}
		return result[i].LocalKey < result[j].LocalKey
	})
	return result
}

func degradeTaskLifecycleObservation(
	observation TaskLifecycleObservation,
	reason string,
	episodes map[string]TaskLifecycleEpisodeSnapshot,
) TaskLifecycleObservation {
	original := TaskLifecycleAlternative{
		Kind:             observation.Kind,
		EpisodeIDs:       append([]string(nil), observation.EpisodeIDs...),
		TargetEpisodeIDs: append([]string(nil), observation.TargetEpisodeIDs...),
		Confidence:       observation.Confidence,
		Reason:           observation.Reason,
	}
	observation.Kind = TaskLifecycleKindUnknown
	observation.Confidence = 0
	observation.ResultSummary = ""
	observation.ResultConfidence = 0
	observation.MaterializedResult = nil
	observation.Reason = reason
	observation.Alternatives = normalizeTaskLifecycleAlternatives(
		append([]TaskLifecycleAlternative{original}, observation.Alternatives...),
		episodes,
	)
	return observation
}

func normalizeTaskLifecycleResponse(
	input TaskLifecycleInput,
	response TaskLifecycleResponse,
) TaskLifecycleResponse {
	episodes := make(
		map[string]TaskLifecycleEpisodeSnapshot,
		len(input.Episodes),
	)
	for _, episode := range input.Episodes {
		episodes[episode.EpisodeID] = episode
	}
	suffix := make(map[string]TaskLifecycleEvidence, len(input.SuffixEvidence))
	for _, item := range input.SuffixEvidence {
		suffix[item.Ref] = item
	}

	candidates := make(
		[]TaskLifecycleObservation,
		0,
		min(len(response.Observations), taskLifecycleMaxObservations),
	)
	for _, observation := range response.Observations {
		observation.LocalKey = strings.TrimSpace(observation.LocalKey)
		observation.Kind = normalizeTaskLifecycleKind(observation.Kind)
		observation.EpisodeIDs = canonicalTaskLifecycleOutputIDs(
			observation.EpisodeIDs,
			episodes,
		)
		observation.TargetEpisodeIDs = canonicalTaskLifecycleOutputIDs(
			observation.TargetEpisodeIDs,
			episodes,
		)
		observation.Confidence = normalizeTaskLifecycleConfidence(
			observation.Confidence,
		)
		observation.BoundaryConfidence = normalizeTaskLifecycleConfidence(
			observation.BoundaryConfidence,
		)
		observation.ResultConfidence = normalizeTaskLifecycleConfidence(
			observation.ResultConfidence,
		)
		observation.ResultSummary = strings.TrimSpace(observation.ResultSummary)
		// This field is source-derived below. Discard any model-supplied value.
		observation.MaterializedResult = nil
		observation.Reason = strings.TrimSpace(observation.Reason)
		observation.EvidenceRefs = canonicalTaskLifecycleOutputRefs(
			observation.EvidenceRefs,
			suffix,
		)
		observation.Alternatives = normalizeTaskLifecycleAlternatives(
			observation.Alternatives,
			episodes,
		)
		if observation.Kind == TaskLifecycleKindResumed &&
			len(observation.EpisodeIDs) == 1 &&
			episodes[observation.EpisodeIDs[0]].CurrentStatus ==
				TaskEpisodeStatusCandidate {
			// Older prompts and some model responses use "resumed" for the
			// first independently observed continuation of a candidate.
			observation.Kind = TaskLifecycleKindContinued
		}
		if observation.LocalKey == "" ||
			len(observation.EvidenceRefs) == 0 ||
			!taskLifecycleShapeAllowed(
				observation.Kind,
				observation.EpisodeIDs,
				observation.TargetEpisodeIDs,
			) {
			continue
		}
		observation.OccurredAt = taskLifecycleEvidenceTime(
			observation.EvidenceRefs,
			suffix,
		)
		if !taskLifecycleStatusAllowed(
			observation.Kind,
			observation.EpisodeIDs,
			observation.TargetEpisodeIDs,
			episodes,
		) {
			observation = degradeTaskLifecycleObservation(
				observation,
				"The proposed lifecycle change is incompatible with the episode's observed state.",
				episodes,
			)
		}
		candidates = append(candidates, observation)
		if len(candidates) == taskLifecycleMaxObservations {
			break
		}
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		return taskLifecycleObservationLess(candidates[i], candidates[j])
	})

	consumed := make(map[string]struct{}, len(input.SuffixEvidence))
	normalized := make([]TaskLifecycleObservation, 0, len(candidates))
	seenLocalKeys := make(map[string]struct{}, len(candidates))
	for _, observation := range candidates {
		if _, exists := seenLocalKeys[observation.LocalKey]; exists {
			continue
		}
		availableRefs := make([]string, 0, len(observation.EvidenceRefs))
		for _, ref := range observation.EvidenceRefs {
			if _, alreadyConsumed := consumed[ref]; alreadyConsumed {
				continue
			}
			availableRefs = append(availableRefs, ref)
		}
		if len(availableRefs) == 0 {
			continue
		}
		observation.EvidenceRefs = availableRefs
		observation.OccurredAt = taskLifecycleEvidenceTime(availableRefs, suffix)
		destinationConflict := taskLifecycleTextResultDestinationConflict(
			observation,
			availableRefs,
			suffix,
			episodes,
		)

		switch observation.Kind {
		case TaskLifecycleKindCompleted:
			if destinationConflict &&
				!taskLifecycleHasNonTextTerminalEvidence(
					availableRefs,
					suffix,
					false,
				) {
				observation = degradeTaskLifecycleObservation(
					observation,
					"Completion is unknown because committed text spans multiple destinations without separate terminal evidence.",
					episodes,
				)
			} else if !taskLifecycleHasSupportedTerminalEvidence(
				observation,
				availableRefs,
				suffix,
				episodes,
				false,
			) {
				observation = degradeTaskLifecycleObservation(
					observation,
					"Completion is unknown because no explicit completion or result evidence was observed.",
					episodes,
				)
			} else if destinationConflict {
				observation.ResultSummary = ""
				observation.ResultConfidence = 0
			} else if observation.ResultSummary != "" &&
				!taskLifecycleHasSupportedTerminalEvidence(
					observation,
					availableRefs,
					suffix,
					episodes,
					true,
				) {
				observation.ResultSummary = ""
				observation.ResultConfidence = 0
			}
			observation.MaterializedResult = materializeTaskLifecycleTextResult(
				observation,
				suffix,
				episodes,
			)
			if observation.MaterializedResult != nil {
				observation.ResultSummary =
					"Observed committed text:\n" +
						observation.MaterializedResult.Content
			}
		case TaskLifecycleKindResult:
			if destinationConflict {
				observation = degradeTaskLifecycleObservation(
					observation,
					"The result is unknown because committed text spans multiple destinations.",
					episodes,
				)
			} else if observation.ResultSummary == "" ||
				!taskLifecycleHasSupportedTerminalEvidence(
					observation,
					availableRefs,
					suffix,
					episodes,
					true,
				) {
				observation = degradeTaskLifecycleObservation(
					observation,
					"The result is unknown because no source-backed result evidence was observed.",
					episodes,
				)
			}
			observation.MaterializedResult = materializeTaskLifecycleTextResult(
				observation,
				suffix,
				episodes,
			)
			if observation.MaterializedResult != nil {
				observation.ResultSummary =
					"Observed committed text:\n" +
						observation.MaterializedResult.Content
			}
		case TaskLifecycleKindAbandoned:
			if !taskLifecycleHasEvidenceKind(
				availableRefs,
				suffix,
				taskLifecycleStrongAbandonmentKind,
			) {
				observation = degradeTaskLifecycleObservation(
					observation,
					"Abandonment is unknown because absence or task switching is not abandonment evidence.",
					episodes,
				)
			}
		default:
			observation.ResultSummary = ""
			observation.ResultConfidence = 0
			observation.MaterializedResult = nil
		}

		for _, ref := range availableRefs {
			consumed[ref] = struct{}{}
		}
		seenLocalKeys[observation.LocalKey] = struct{}{}
		normalized = append(normalized, observation)
	}
	sort.SliceStable(normalized, func(i, j int) bool {
		return taskLifecycleObservationLess(normalized[i], normalized[j])
	})

	unconsumed := make([]string, 0, len(input.SuffixEvidence)-len(consumed))
	for _, item := range input.SuffixEvidence {
		if _, ok := consumed[item.Ref]; !ok {
			unconsumed = append(unconsumed, item.Ref)
		}
	}
	sort.Strings(unconsumed)
	return TaskLifecycleResponse{
		Observations: normalized,
		ReasoningObservations: normalizeTaskLifecycleReasoningObservations(
			input,
			response.ReasoningObservations,
		),
		UnconsumedEvidenceRefs: unconsumed,
	}
}

func runTaskLifecycleObservation(
	ctx context.Context,
	runner agent.AgentRunner,
	model string,
	input TaskLifecycleInput,
) (TaskLifecycleResponse, string, string, error) {
	if runner == nil {
		return TaskLifecycleResponse{}, "", "", errors.New(
			"task lifecycle runner is not initialized",
		)
	}
	model = strings.TrimSpace(model)
	if model == "" {
		return TaskLifecycleResponse{}, "", "", errors.New(
			"task lifecycle model is required",
		)
	}
	prompt, normalized, err := buildTaskLifecyclePrompt(input)
	if err != nil {
		return TaskLifecycleResponse{}, "", "", err
	}

	var result strings.Builder
	_, err = runner.ExecuteTask(ctx, &agent.TaskInput{
		Priority:          agent.PriorityBackground,
		AutoApproveTools:  false,
		Prompt:            prompt,
		Model:             model,
		PlanMode:          true,
		Ephemeral:         true,
		NoTools:           true,
		NoSystem:          true,
		SuppressTelemetry: true,
	}, func(event *agent.StreamEvent) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if event == nil {
			return nil
		}
		switch event.Type {
		case "assistant":
			result.WriteString(event.Content)
		case "error":
			message := strings.TrimSpace(event.Error)
			if message == "" {
				message = "task lifecycle model stream failed"
			}
			return errors.New(message)
		}
		return nil
	})
	raw := strings.TrimSpace(result.String())
	if err != nil {
		return TaskLifecycleResponse{}, raw, prompt, fmt.Errorf(
			"execute task lifecycle observation: %w",
			err,
		)
	}

	var response TaskLifecycleResponse
	if err := unmarshalJSONBestEffort(stripCodeFences(raw), &response); err != nil {
		return TaskLifecycleResponse{}, raw, prompt, fmt.Errorf(
			"parse task lifecycle response: %w",
			err,
		)
	}
	return normalizeTaskLifecycleResponse(normalized, response), raw, prompt, nil
}
