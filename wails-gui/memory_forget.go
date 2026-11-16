package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

type MemoryForgetItem struct {
	Kind       string `json:"kind"`
	ID         string `json:"id"`
	Title      string `json:"title"`
	RecordType string `json:"recordType,omitempty"`
}

type MemoryForgetPreview struct {
	MemoryID          string             `json:"memoryId"`
	Title             string             `json:"title"`
	Items             []MemoryForgetItem `json:"items"`
	EmbeddingCount    int                `json:"embeddingCount"`
	HistoryCount      int                `json:"historyCount"`
	RetentionDays     int                `json:"retentionDays"`
	ConfirmationToken string             `json:"confirmationToken"`
}

type memoryForgetScope struct {
	records         []*memoryForgetRecord
	contents        []string
	ids             []string
	removedIDs      []string
	removedVersions []string
	paths           []string
	rejections      memoryForgetRejectionScope
	preserveRoot    bool
}

func (a *App) readMemoryForgetPreviewScoped(id string, historyVersionsOnly bool) (MemoryForgetPreview, *memoryForgetScope, error) {
	memoryMutationMu.RLock()
	defer memoryMutationMu.RUnlock()
	unlock := memoryFileLocks.Acquire(id)
	defer unlock()
	index, release, err := a.lockMemoryForgetIndex()
	if err != nil {
		return MemoryForgetPreview{}, nil, err
	}
	defer release()
	tx, err := a.beginMemoryForgetDerivatives()
	if err != nil {
		return MemoryForgetPreview{}, nil, err
	}
	if tx != nil {
		defer tx.Rollback()
	}
	return a.memoryForgetPreviewScoped(id, tx, index, historyVersionsOnly)
}

// ForgetMemoryEntry fails closed: suppression is durable before anything is
// removed. Storage spans Markdown and two SQLite databases, so failures are
// returned for retry rather than claiming a cross-store atomic deletion.
func (a *App) ForgetMemoryEntry(id, confirmationToken string) (resultErr error) {
	var withdrawnIDs []string
	defer func() {
		for _, withdrawnID := range withdrawnIDs {
			publishPrivacyWithdrawal(privacyWithdrawalScope{Kind: privacyWithdrawalSource, SourceKind: "memory", ID: withdrawnID})
		}
	}()
	a.clearPageSnapshots()
	defer a.clearPageSnapshots()
	prepared, err := loadMemoryForgetPreparedPlan(id, confirmationToken)
	if err != nil {
		return err
	}
	memoryMutationMu.Lock()
	defer memoryMutationMu.Unlock()
	unlock := memoryFileLocks.Acquire(id)
	defer unlock()
	index, release, err := a.lockMemoryForgetIndex()
	if err != nil {
		return err
	}
	defer release()
	tx, err := a.beginMemoryForgetDerivatives()
	if err != nil {
		return err
	}
	if tx != nil {
		defer tx.Rollback()
	}
	preview, scope, err := a.memoryForgetPreviewScoped(id, tx, index, prepared.historyVersionsOnly)
	if err != nil {
		return err
	}
	if prepared.baseToken != preview.ConfirmationToken {
		return fmt.Errorf("memory forget preview changed; review it again")
	}
	dir, err := memoryDir()
	if err != nil {
		return err
	}
	withdrawal, err := a.topicWithdrawalForSource("memory", scope.ids, tx, scope.contents...)
	if err != nil {
		return err
	}
	finish, err := a.beginTopicWithdrawal(withdrawal)
	if err != nil {
		return err
	}
	suppressed := false
	defer func() { resultErr = errors.Join(resultErr, finish(suppressed)) }()

	if err := a.forgetAboutYouPortraitVersions(scope.removedIDs, scope.records); err != nil {
		return fmt.Errorf("remove memory portrait: %w", err)
	}
	prepared.semantics.RemovedIDs = scope.removedIDs
	prepared.semantics.RemovedVersions = scope.removedVersions
	for _, record := range scope.records {
		tombstoneID := record.id
		if record.preserveLive {
			tombstoneID = id
		}
		preserveRoot := scope.preserveRoot && tombstoneID == id
		if err := recordMemoryTombstoneScoped(dir, tombstoneID, record.contents, time.Now(), preserveRoot, prepared.semantics); err != nil {
			return fmt.Errorf("save memory suppression: %w", err)
		}
		suppressed = true
		withdrawnIDs = append([]string(nil), scope.removedIDs...)
		if scope.preserveRoot {
			withdrawnIDs = append(withdrawnIDs, id)
		}
	}
	if a.store != nil {
		if err := a.store.DeleteContextUses(context.Background(), "memory", withdrawnIDs...); err != nil {
			return fmt.Errorf("remove memory uses: %w", err)
		}
	}
	if tx != nil {
		if err := deleteMemoryForgetDerivatives(tx, scope.ids[0], scope.ids[1:]...); err != nil {
			return fmt.Errorf("remove derived memories: %w", err)
		}
	}
	if index != nil {
		if err := removeForgottenMemoryIndex(index, scope.paths[0], scope.contents, scope.paths[1:]...); err != nil {
			return fmt.Errorf("remove memory embeddings: %w", err)
		}
	}
	if tx != nil {
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit derived memory removal: %w", err)
		}
	}
	if err := writeMemoryForgetRejections(dir, scope.rejections); err != nil {
		return fmt.Errorf("remove saved attribution corrections: %w", err)
	}
	for _, record := range scope.records {
		for _, path := range record.archivePaths {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("remove archived memory: %w", err)
			}
		}
		if record.preserveLive {
			for _, path := range record.historyPaths {
				if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
					return fmt.Errorf("remove prior memory version: %w", err)
				}
			}
		} else if err := os.RemoveAll(filepath.Join(dir, memoryHistoryDirName, record.id)); err != nil {
			return fmt.Errorf("remove memory history: %w", err)
		}
		if record.livePath != "" && record.id != id {
			if err := os.Remove(record.livePath); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("remove memory file: %w", err)
			}
		}
	}
	// Keep the requested record available for a fresh preview until all of its
	// descendants and saved copies have been removed successfully.
	if !scope.preserveRoot {
		if err := os.Remove(filepath.Join(dir, id+".md")); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove memory file: %w", err)
		}
	}

	// Close the unlink window for reads that began after the initial erasure.
	if a.store != nil {
		if err := a.store.DeleteContextUses(context.Background(), "memory", withdrawnIDs...); err != nil {
			return fmt.Errorf("remove memory uses: %w", err)
		}
	}

	a.markMemoryDirty("forget")
	if a.ctx != nil {
		a.emitMemoryUpdated(true)
	}
	return nil
}

func (a *App) beginMemoryForgetDerivatives() (*sql.Tx, error) {
	if a.activity == nil || a.activity.store == nil {
		dir, err := memoryDir()
		if err != nil {
			return nil, err
		}
		if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "activity.db")); !os.IsNotExist(err) {
			return nil, fmt.Errorf("activity storage must be available to inspect derived memories")
		}
		return nil, nil
	}
	return a.activity.store.db.Begin()
}

// Synchronize with the complete embedding pipeline, including cache writes that
// can finish after a slow external embed request. Do not depend on Search to
// eventually clean up deleted vectors.
func (a *App) lockMemoryForgetIndex() (*sql.DB, func(), error) {
	a.mu.Lock()
	store := a.memoryStore
	a.mu.Unlock()
	if store != nil {
		store.syncMu.Lock()
		store.mu.Lock()
		closed := store.closed
		db := store.db
		store.mu.Unlock()
		if closed {
			store.syncMu.Unlock()
			return nil, nil, fmt.Errorf("memory index is closed")
		}
		return db, store.syncMu.Unlock, nil
	}
	dir, err := memoryDir()
	if err != nil {
		return nil, nil, err
	}
	path := filepath.Join(filepath.Dir(dir), "memory.db")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil, func() {}, nil
	} else if err != nil {
		return nil, nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, nil, err
	}
	return db, func() { db.Close() }, nil
}

func (a *App) memoryForgetPreviewScoped(id string, tx *sql.Tx, index *sql.DB, historyVersionsOnly bool) (MemoryForgetPreview, *memoryForgetScope, error) {
	entry, err := a.GetMemoryEntry(id)
	if errors.Is(err, os.ErrNotExist) {
		// Dream may have archived the source while keeping descendants live.
		// The ordinary cascade must still be able to erase that source.
		dir, dirErr := memoryDir()
		if dirErr != nil {
			return MemoryForgetPreview{}, nil, dirErr
		}
		records, readErr := loadMemoryForgetRecords(dir)
		if readErr != nil {
			return MemoryForgetPreview{}, nil, readErr
		}
		if record := records[id]; record != nil && len(record.contents) > 0 {
			entry = MemoryEntry{ID: id, Title: extractTitle(record.contents[0])}
			err = nil
		}
	}
	if err != nil {
		return MemoryForgetPreview{}, nil, err
	}
	preview := MemoryForgetPreview{MemoryID: id, Title: entry.Title, RetentionDays: memoryForgetRetentionDays}
	dir, err := memoryDir()
	if err != nil {
		return preview, nil, err
	}
	records, err := collectMemoryForgetRecordsScoped(dir, id, historyVersionsOnly)
	if err != nil {
		return preview, nil, err
	}
	scope := &memoryForgetScope{records: records, preserveRoot: historyVersionsOnly}
	ids := map[string]bool{}
	var archived []string
	for _, record := range records {
		if !record.preserveLive {
			scope.ids = append(scope.ids, record.id)
			ids[record.id] = true
			scope.removedIDs = append(scope.removedIDs, record.id)
			scope.paths = append(scope.paths, record.id+".md")
		} else {
			for _, content := range record.contents {
				selector := memoryForgetHistoricalSelector(record.id, content)
				scope.ids = append(scope.ids, selector)
				scope.removedVersions = append(scope.removedVersions, selector)
			}
		}
		scope.contents = append(scope.contents, record.contents...)
		preview.Items = append(preview.Items, record.items...)
		preview.HistoryCount += record.historyCount
		archived = append(archived, record.archivePaths...)
		archived = append(archived, record.archiveContents...)
	}
	if len(scope.ids) == 0 {
		return preview, nil, fmt.Errorf("memory record is no longer available")
	}
	// No live file is removed when only old versions were selected. An empty
	// index path leaves the corrected file's chunks intact while clearing old
	// content hashes from the embedding cache.
	if len(scope.paths) == 0 {
		scope.paths = []string{""}
	}
	if historyVersionsOnly {
		// A rejection keyed only by the shared ID cannot identify which
		// version it described; remove that ambiguous saved correction.
		ids[id] = true
	}
	scope.rejections, err = collectMemoryForgetRejections(dir, ids)
	if err != nil {
		return preview, nil, err
	}
	preview.Items = append(preview.Items, scope.rejections.items...)
	if tx != nil {
		items, err := collectMemoryForgetDerivatives(tx, scope.ids[0], scope.ids[1:]...)
		if err != nil {
			return preview, nil, err
		}
		preview.Items = append(preview.Items, items...)
	}
	sort.Slice(preview.Items, func(i, j int) bool {
		left, right := preview.Items[i], preview.Items[j]
		return left.Kind+":"+left.ID < right.Kind+":"+right.ID
	})
	if index != nil {
		preview.EmbeddingCount, err = countForgottenMemoryEmbeddings(index, scope.paths[0], scope.contents, scope.paths[1:]...)
		if err != nil {
			return preview, nil, err
		}
	}
	// Bind confirmation to all descendants and copies, even when their file
	// timestamps are unchanged. Correction rows can contain the erased fact too.
	manifest := fmt.Sprintf("%s\x00%#v\x00%#v\x00%#v\x00%d\x00%d\x00%#v\x00%s",
		id, scope.ids, scope.contents, preview.Items, preview.EmbeddingCount, preview.HistoryCount, archived, scope.rejections.before)
	preview.ConfirmationToken = memoryForgetHash(manifest)
	return preview, scope, nil
}
