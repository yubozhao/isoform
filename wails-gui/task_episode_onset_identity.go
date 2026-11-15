package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
)

var ErrTaskEpisodeRepeatedOnset = errors.New(
	"task episode shares one exact observed start with another immutable episode",
)

type taskEpisodeOnsetIdentityRecord struct {
	EpisodeID       string
	Anchor          taskEpisodeOnsetAnchor
	InferredStartAt int64
	DetectedAt      int64
	CreatedAt       int64
}

type taskEpisodeOnsetIdentityIndex struct {
	byEpisode map[string]taskEpisodeOnsetIdentityRecord
	byKey     map[string][]taskEpisodeOnsetIdentityRecord
}

func taskEpisodeOnsetIdentityKey(
	anchor taskEpisodeOnsetAnchor,
	inferredStartAt int64,
) string {
	return taskEpisodeOnsetAnchorKey(anchor) + "\x00" +
		fmt.Sprint(inferredStartAt)
}

func taskEpisodePrimaryEvidenceSet(raw string) (map[string]struct{}, bool) {
	var ids []string
	if json.Unmarshal([]byte(raw), &ids) != nil {
		return nil, false
	}
	result := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" {
			result[id] = struct{}{}
		}
	}
	return result, true
}

func taskEpisodeOnsetIdentityRecordLess(
	left, right taskEpisodeOnsetIdentityRecord,
) bool {
	if left.DetectedAt != right.DetectedAt {
		return left.DetectedAt < right.DetectedAt
	}
	if left.CreatedAt != right.CreatedAt {
		return left.CreatedAt < right.CreatedAt
	}
	return left.EpisodeID < right.EpisodeID
}

func (s *ActivityStore) taskEpisodeOnsetIdentityIndex(
	inferredStartAt int64,
) (taskEpisodeOnsetIdentityIndex, error) {
	return s.taskEpisodeOnsetIdentityIndexAt(inferredStartAt, 0)
}

func (s *ActivityStore) taskEpisodeOnsetIdentityIndexAt(
	inferredStartAt int64,
	createdCutoffAt int64,
) (taskEpisodeOnsetIdentityIndex, error) {
	return s.taskEpisodeOnsetIdentityIndexAtContext(context.Background(), inferredStartAt, createdCutoffAt)
}

func (s *ActivityStore) taskEpisodeOnsetIdentityIndexAtContext(ctx context.Context, inferredStartAt, createdCutoffAt int64) (taskEpisodeOnsetIdentityIndex, error) {
	result := taskEpisodeOnsetIdentityIndex{
		byEpisode: make(map[string]taskEpisodeOnsetIdentityRecord),
		byKey:     make(map[string][]taskEpisodeOnsetIdentityRecord),
	}
	if s == nil || s.db == nil {
		return result, errors.New("activity store is unavailable")
	}
	query := `
		SELECT episode.id, episode.inferred_start_at, episode.detected_at,
			episode.created_at, hypothesis.evidence_ids_json,
			evidence.id, evidence.source_kind, evidence.source_id,
			evidence.observed_at, evidence.payload_json
		FROM task_episodes episode
		JOIN task_episode_hypotheses hypothesis
			ON hypothesis.task_episode_id = episode.id
			AND hypothesis.revision = 1
		JOIN task_episode_evidence evidence
			ON evidence.task_episode_id = episode.id
			AND evidence.role = ?`
	args := []any{TaskEpisodeEvidenceOnset}
	query += ` WHERE 1 = 1`
	if inferredStartAt > 0 {
		query += ` AND episode.inferred_start_at = ?`
		args = append(args, inferredStartAt)
	}
	if createdCutoffAt > 0 {
		query += ` AND episode.created_at <= ?
			AND hypothesis.created_at <= ? AND evidence.created_at <= ?`
		args = append(
			args,
			createdCutoffAt,
			createdCutoffAt,
			createdCutoffAt,
		)
	}
	query += `
		ORDER BY episode.inferred_start_at DESC, episode.detected_at ASC,
			episode.created_at ASC, episode.id ASC, evidence.id ASC`
	rows, err := s.reader().QueryContext(ctx, query, args...)
	if err != nil {
		return result, fmt.Errorf("list primary task onset identities: %w", err)
	}
	type episodeCandidate struct {
		record  taskEpisodeOnsetIdentityRecord
		anchors map[string]taskEpisodeOnsetAnchor
	}
	candidates := make(map[string]*episodeCandidate)
	for rows.Next() {
		var (
			episodeID, primaryJSON, evidenceID string
			sourceKind, sourceID, payloadJSON  string
			startedAt, detectedAt, createdAt   int64
			observedAt                         int64
		)
		if err := rows.Scan(
			&episodeID,
			&startedAt,
			&detectedAt,
			&createdAt,
			&primaryJSON,
			&evidenceID,
			&sourceKind,
			&sourceID,
			&observedAt,
			&payloadJSON,
		); err != nil {
			rows.Close()
			return result, err
		}
		primary, valid := taskEpisodePrimaryEvidenceSet(primaryJSON)
		if !valid {
			continue
		}
		if _, named := primary[evidenceID]; !named {
			continue
		}
		anchor, valid := taskEpisodeOnsetAnchorFromPayload(payloadJSON)
		if !valid || anchor.SourceKind != sourceKind ||
			anchor.SourceID != sourceID || anchor.ObservedAt != observedAt ||
			anchor.ObservedAt != startedAt {
			continue
		}
		candidate := candidates[episodeID]
		if candidate == nil {
			candidate = &episodeCandidate{
				record: taskEpisodeOnsetIdentityRecord{
					EpisodeID:       episodeID,
					InferredStartAt: startedAt,
					DetectedAt:      detectedAt,
					CreatedAt:       createdAt,
				},
				anchors: make(map[string]taskEpisodeOnsetAnchor),
			}
			candidates[episodeID] = candidate
		}
		candidate.anchors[taskEpisodeOnsetAnchorKey(anchor)] = anchor
	}
	rowsErr := rows.Err()
	if err := rows.Close(); err != nil && rowsErr == nil {
		rowsErr = err
	}
	if rowsErr != nil {
		return result, rowsErr
	}

	for _, candidate := range candidates {
		keys := make([]string, 0, len(candidate.anchors))
		for key := range candidate.anchors {
			keys = append(keys, key)
		}
		if len(keys) == 0 {
			continue
		}
		sort.Strings(keys)
		candidate.record.Anchor = candidate.anchors[keys[0]]
		identityKey := taskEpisodeOnsetIdentityKey(
			candidate.record.Anchor,
			candidate.record.InferredStartAt,
		)
		result.byEpisode[candidate.record.EpisodeID] = candidate.record
		result.byKey[identityKey] = append(
			result.byKey[identityKey],
			candidate.record,
		)
	}
	for key := range result.byKey {
		sort.Slice(result.byKey[key], func(i, j int) bool {
			return taskEpisodeOnsetIdentityRecordLess(
				result.byKey[key][i],
				result.byKey[key][j],
			)
		})
	}
	return result, nil
}

func (index taskEpisodeOnsetIdentityIndex) repeatedEpisodeIDs() map[string]struct{} {
	result := make(map[string]struct{})
	for _, records := range index.byKey {
		if len(records) < 2 {
			continue
		}
		for _, record := range records {
			result[record.EpisodeID] = struct{}{}
		}
	}
	return result
}

// taskEpisodeRepeatedOnsetSetContext is the all-time repeated-onset set. It is
// served from a cache keyed on the trigger-maintained read revision (inserts only:
// task episodes are immutable). Concurrent callers at one revision share one scan.
// The revision is read before the scan, so a change during the scan leaves the cache
// on an older key and the next read rescans rather than serving it. The returned
// map is read-only.
func (s *ActivityStore) taskEpisodeRepeatedOnsetSetContext(ctx context.Context) (map[string]struct{}, error) {
	revision, err := s.taskEpisodeReadRevision(ctx)
	if err != nil {
		return nil, err
	}
	if set, ok := s.repeatedOnsets.get(revision); ok {
		return set, nil
	}
	value, err, _ := s.repeatedOnsetFlight.Do(strconv.FormatInt(revision, 10), func() (any, error) {
		if set, ok := s.repeatedOnsets.get(revision); ok {
			return set, nil
		}
		if repeatedOnsetScanGate != nil {
			repeatedOnsetScanGate()
		}
		repeatedOnsetScans.Add(1)
		index, err := s.taskEpisodeOnsetIdentityIndexAtContext(ctx, 0, 0)
		if err != nil {
			return nil, err
		}
		set := index.repeatedEpisodeIDs()
		s.repeatedOnsets.put(revision, set)
		return set, nil
	})
	if err != nil {
		return nil, err
	}
	return value.(map[string]struct{}), nil
}

// repeatedOnsetScans counts scans actually run (tests read it). repeatedOnsetScanGate,
// when set by a test, runs inside the scan so the test can hold it open.
var (
	repeatedOnsetScans    atomic.Int64
	repeatedOnsetScanGate func()
)

func (s *ActivityStore) taskEpisodeRepeatedOnsetSetAt(
	createdCutoffAt int64,
) (map[string]struct{}, error) {
	index, err := s.taskEpisodeOnsetIdentityIndexAt(0, createdCutoffAt)
	if err != nil {
		return nil, err
	}
	return index.repeatedEpisodeIDs(), nil
}
