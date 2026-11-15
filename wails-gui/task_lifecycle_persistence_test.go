package main

import (
	"testing"
)

func createLifecycleEpisode(
	t *testing.T,
	store *ActivityStore,
	id, status string,
	start int64,
) TaskEpisode {
	t.Helper()
	input := taskEpisodeFixture(id, start, start+10, status)
	input.InitialHypothesis.BoundaryConfidence = 0.7
	episode, added, err := store.CreateTaskEpisode(input)
	if err != nil || !added {
		t.Fatalf("create episode %s: added=%v err=%v", id, added, err)
	}
	return episode
}

func lifecycleCandidateInput(
	episode TaskEpisode,
	suffixKind, suffixSummary string,
) TaskLifecycleInput {
	return TaskLifecycleInput{
		SourceKind:     "activity",
		SourceID:       "candidate-window",
		PrefixCutoffAt: 2_000,
		CutoffAt:       4_000,
		Episodes: []TaskLifecycleEpisodeSnapshot{{
			EpisodeID:          episode.ID,
			CurrentStatus:      episode.CurrentStatus,
			InferredStartedAt:  episode.InferredStartAt,
			Task:               episode.LatestHypothesis.TaskHypothesis,
			DesiredResult:      episode.LatestHypothesis.DesiredResultHypothesis,
			TaskConfidence:     episode.LatestHypothesis.TaskConfidence,
			BoundaryConfidence: episode.LatestHypothesis.BoundaryConfidence,
			EvidenceRefs:       []string{"prefix"},
		}},
		PrefixEvidence: []TaskLifecycleEvidence{{
			Ref:        "prefix",
			Kind:       "onset",
			SourceKind: "event",
			SourceID:   "onset",
			OccurredAt: 1_000,
			Summary:    "Task began",
		}},
		SuffixEvidence: []TaskLifecycleEvidence{{
			Ref:        "suffix",
			Kind:       suffixKind,
			SourceKind: "event",
			SourceID:   "suffix",
			OccurredAt: 3_000,
			Summary:    suffixSummary,
		}},
	}
}
