package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTaskContextManifestStatsMigrationBackfillsImmutableReadModel(
	t *testing.T,
) {
	store := newTestStore(t)
	input := taskEpisodeFixture(
		"episode-manifest-stats-backfill",
		1_000,
		1_100,
		TaskEpisodeStatusCandidate,
	)
	input.FrozenContext.Manifest = []TaskContextManifestEntry{
		{
			Ref:        "activity-snapshot:one",
			SourceKind: taskContextSourceActivitySnapshotKind,
			ObservedAt: 1_000,
			Included:   true,
		},
		{
			Ref:        "activity-snapshot:late",
			SourceKind: taskContextSourceActivitySnapshotKind,
			ObservedAt: 1_001,
			Included:   true,
		},
		{
			Ref:        "private:excluded",
			SourceKind: "private_source",
			ObservedAt: 900,
			Included:   false,
		},
	}
	episode, added, err := store.CreateTaskEpisode(input)
	if err != nil || !added {
		t.Fatalf("create task episode: added=%v err=%v", added, err)
	}

	if _, err := store.db.Exec(`DROP TABLE task_context_manifest_stats_v1`); err != nil {
		t.Fatalf("drop manifest stats for migration fixture: %v", err)
	}
	tx, err := store.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := migrateTaskContextManifestStats(tx); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	manifestJSON, err := json.Marshal(episode.FrozenContext.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	var (
		version                  string
		manifestSHA256           string
		manifestEntryCount       int
		includedCount            int
		excludedCount            int
		frozenSourceContextCount int
	)
	if err := store.db.QueryRow(`
		SELECT version, manifest_sha256, manifest_entry_count,
			included_count, excluded_count, frozen_source_context_count
		FROM task_context_manifest_stats_v1
		WHERE task_episode_id = ?`,
		episode.ID,
	).Scan(
		&version,
		&manifestSHA256,
		&manifestEntryCount,
		&includedCount,
		&excludedCount,
		&frozenSourceContextCount,
	); err != nil {
		t.Fatal(err)
	}
	if version != taskContextManifestStatsVersion ||
		manifestSHA256 != taskEpisodePayloadHash(manifestJSON) ||
		manifestEntryCount != 3 || includedCount != 2 || excludedCount != 1 ||
		frozenSourceContextCount != 1 {
		t.Fatalf(
			"backfilled manifest stats version=%q hash=%q entries=%d included=%d excluded=%d source=%d",
			version,
			manifestSHA256,
			manifestEntryCount,
			includedCount,
			excludedCount,
			frozenSourceContextCount,
		)
	}
	if _, err := store.db.Exec(`
		UPDATE task_context_manifest_stats_v1
		SET included_count = included_count + 1
		WHERE task_episode_id = ?`, episode.ID); err == nil ||
		!strings.Contains(err.Error(), "immutable") {
		t.Fatalf("manifest stats update error=%v", err)
	}
	if _, err := store.db.Exec(`
		DELETE FROM task_context_manifest_stats_v1
		WHERE task_episode_id = ?`, episode.ID); err == nil ||
		!strings.Contains(err.Error(), "immutable") {
		t.Fatalf("manifest stats delete error=%v", err)
	}
}
