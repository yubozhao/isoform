package ksync

import (
	"context"

	"yansu-agent/internal/client"
	"yansu-agent/internal/config"
	"yansu-agent/internal/knowledge"
	internalsync "yansu-agent/internal/sync"
)

// KSyncer provides a public API for knowledge synchronization.
// It wraps the internal sync, client, config, and knowledge packages
// so that external modules (e.g. wails-gui) can use sync functionality
// without importing internal packages directly.
type KSyncer struct {
	syncer       *internalsync.Syncer
	somethingDir string
}

// NewKSyncer creates a syncer for a project directory with explicit credentials.
func NewKSyncer(baseURL, token, org, product, projectName, projectRoot string) *KSyncer {
	somethingDir := config.GetSomethingDir(projectRoot)
	return &KSyncer{
		syncer:       internalsync.NewSyncer(projectRoot, somethingDir, nil),
		somethingDir: somethingDir,
	}
}

// NewKSyncerFromProject reports retirement before loading any saved credentials.
func NewKSyncerFromProject(projectRoot string) (*KSyncer, error) {
	return nil, client.ErrRetired
}

// Pull pulls remote knowledge changes to local.
func (k *KSyncer) Pull(ctx context.Context) (*PullResult, error) {
	return nil, client.ErrRetired
}

// Push pushes local new-knowledge to cloud.
func (k *KSyncer) Push(ctx context.Context) (*PushResult, error) {
	return nil, client.ErrRetired
}

// SyncContext syncs the project context (PRD, scenarios, etc.) from cloud.
func (k *KSyncer) SyncContext(ctx context.Context) error {
	return k.syncer.SyncContext(ctx)
}

// SyncAll performs a full bidirectional sync (push + pull).
func (k *KSyncer) SyncAll(ctx context.Context) (*SyncResult, error) {
	return nil, client.ErrRetired
}

// GetSyncStatus returns the current sync status for the project.
func (k *KSyncer) GetSyncStatus() (*SyncStatus, error) {
	s, err := k.syncer.GetSyncStatus()
	if err != nil {
		return nil, err
	}
	return &SyncStatus{
		LastSyncedAt:        s.LastSyncedAt,
		LocalKnowledgeCount: s.LocalKnowledgeCount,
		PendingNewKnowledge: s.PendingNewKnowledge,
	}, nil
}

// EnsureDirectories creates the .something subdirectories if they don't exist.
func (k *KSyncer) EnsureDirectories() error {
	return knowledge.EnsureDirectories(k.somethingDir)
}

// UpdateIndex regenerates the knowledge index file.
func (k *KSyncer) UpdateIndex() error {
	return knowledge.UpdateIndex(k.somethingDir)
}
