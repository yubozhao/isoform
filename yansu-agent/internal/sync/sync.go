package sync

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"yansu-agent/internal/client"
	"yansu-agent/internal/config"
	"yansu-agent/internal/knowledge"
)

// Syncer handles synchronization between local files and cloud
type Syncer struct {
	somethingDir string
}

// SyncResult contains the result of a sync operation
type SyncResult struct {
	Pushed PushResult
	Pulled PullResult
}

// PushResult contains details about pushed changes
type PushResult struct {
	Created []string
	Updated []string
	Deleted []string
	Errors  []string
}

// PullResult contains details about pulled changes
type PullResult struct {
	Created []string
	Updated []string
	Deleted []string
	Errors  []string
}

// HasChanges returns true if there were any changes
func (r *PullResult) HasChanges() bool {
	return len(r.Created) > 0 || len(r.Updated) > 0 || len(r.Deleted) > 0
}

// TotalChanges returns the total number of changes
func (r *PullResult) TotalChanges() int {
	return len(r.Created) + len(r.Updated) + len(r.Deleted)
}

// NewSyncer creates a new syncer
func NewSyncer(projectRoot, somethingDir string, apiClient *client.Client) *Syncer {
	return &Syncer{
		somethingDir: somethingDir,
	}
}

func (s *Syncer) SyncAll(ctx context.Context) (*SyncResult, error) {
	return nil, client.ErrRetired
}

func (s *Syncer) Push(ctx context.Context) (*PushResult, error) {
	return nil, client.ErrRetired
}

func (s *Syncer) Pull(ctx context.Context) (*PullResult, error) {
	return nil, client.ErrRetired
}

func (s *Syncer) PushSingleKnowledge(ctx context.Context, filePath string) error {
	return client.ErrRetired
}

func (s *Syncer) SyncContext(ctx context.Context) error {
	return client.ErrRetired
}

// GetSyncStatus returns the current sync status
func (s *Syncer) GetSyncStatus() (*SyncStatus, error) {
	status := &SyncStatus{}

	// Load project config for last sync time
	projectCfg, err := config.LoadProject(s.somethingDir)
	if err == nil {
		status.LastSyncedAt = projectCfg.LastSyncedAt
	}

	// Count local knowledge
	knowledgeDir := filepath.Join(s.somethingDir, "knowledge")
	localList, _ := knowledge.LoadAll(knowledgeDir)
	status.LocalKnowledgeCount = len(localList)

	// Count pending new knowledge
	newKnowledgeDir := filepath.Join(s.somethingDir, "new-knowledge")
	entries, _ := os.ReadDir(newKnowledgeDir)
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
			status.PendingNewKnowledge++
		}
	}

	return status, nil
}

// SyncStatus represents the current sync status
type SyncStatus struct {
	LastSyncedAt        time.Time
	LocalKnowledgeCount int
	PendingNewKnowledge int
}
