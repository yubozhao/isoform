package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// taskEpisodeOnsetAnchor is an exact production source fact at the inferred
// task boundary. Model wording and detector-window identity are deliberately
// absent so overlapping observations of one start converge on one episode.
type taskEpisodeOnsetAnchor struct {
	Ref        string
	SourceKind string
	SourceID   string
	ObservedAt int64
}

type TaskEpisodeCaptureResult struct {
	Episode TaskEpisode `json:"episode"`
	Added   bool        `json:"added"`
}

func normalizeTaskEpisodeOnsetAnchor(
	anchor taskEpisodeOnsetAnchor,
) (taskEpisodeOnsetAnchor, bool) {
	anchor.Ref = strings.TrimSpace(anchor.Ref)
	anchor.SourceKind = strings.TrimSpace(anchor.SourceKind)
	anchor.SourceID = strings.TrimSpace(anchor.SourceID)
	return anchor, anchor.Ref != "" && anchor.SourceKind != "" &&
		anchor.SourceID != "" && anchor.ObservedAt > 0
}

func taskEpisodeOnsetAnchorKey(anchor taskEpisodeOnsetAnchor) string {
	return strings.Join([]string{
		anchor.SourceKind,
		anchor.SourceID,
		fmt.Sprint(anchor.ObservedAt),
		anchor.Ref,
	}, "\x00")
}

func taskEpisodeOnsetAnchorFromPayload(
	raw string,
) (taskEpisodeOnsetAnchor, bool) {
	var evidence TaskOnsetEvidence
	if err := json.Unmarshal([]byte(raw), &evidence); err != nil {
		return taskEpisodeOnsetAnchor{}, false
	}
	return normalizeTaskEpisodeOnsetAnchor(taskEpisodeOnsetAnchor{
		Ref:        evidence.Ref,
		SourceKind: evidence.SourceKind,
		SourceID:   evidence.SourceID,
		ObservedAt: evidence.OccurredAt,
	})
}

// FindTaskEpisodeByOnsetAnchor returns the earliest immutable episode that was
// created from the exact source-bound start. It validates the stored evidence
// payload so a coincident source timestamp cannot alias a different ref.
func (s *ActivityStore) FindTaskEpisodeByOnsetAnchor(
	anchor taskEpisodeOnsetAnchor,
	inferredStartAt int64,
) (TaskEpisode, bool, error) {
	if s == nil || s.db == nil {
		return TaskEpisode{}, false, errors.New("activity store is unavailable")
	}
	anchor, ok := normalizeTaskEpisodeOnsetAnchor(anchor)
	if !ok || inferredStartAt <= 0 || anchor.ObservedAt != inferredStartAt {
		return TaskEpisode{}, false, errors.New("exact task onset anchor is invalid")
	}
	index, err := s.taskEpisodeOnsetIdentityIndex(inferredStartAt)
	if err != nil {
		return TaskEpisode{}, false, fmt.Errorf("find task episode by onset anchor: %w", err)
	}
	records := index.byKey[taskEpisodeOnsetIdentityKey(anchor, inferredStartAt)]
	if len(records) == 0 {
		return TaskEpisode{}, false, nil
	}
	episode, err := s.GetTaskEpisode(records[0].EpisodeID)
	if err != nil {
		return TaskEpisode{}, false, err
	}
	if episode.InferredStartAt != inferredStartAt ||
		episode.FrozenContext.CutoffAt != inferredStartAt {
		return TaskEpisode{}, false, errors.New(
			"source-bound task episode has an invalid frozen-context boundary",
		)
	}
	return episode, true, nil
}
