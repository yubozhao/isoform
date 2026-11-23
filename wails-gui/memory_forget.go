package main

import (
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
	rootID          string
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

// ForgetMemoryEntry checks the byte-bound confirmation under the writer
// barriers and uses the same prepared files and commit decision as a watch cut.
func (a *App) ForgetMemoryEntry(id, confirmationToken string) error {
	var committed *watchCutFiles
	defer func() {
		if committed != nil {
			a.publishForgottenMemory(committed.WithdrawnIDs, committed.notifyTopics)
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
	dir, err := memoryDir()
	if err != nil {
		return err
	}
	journal := &mobileRecordingStore{dir: dir, cutUses: a.store}
	if a.activity != nil && a.activity.store != nil {
		journal.cutDB = a.activity.store.db
		if _, err := journal.cutDB.Exec(watchCutReceiptSchema); err != nil {
			return err
		}
	}
	if err := journal.recoverWatchCutLocked(); err != nil {
		return err
	}
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
	stage := filepath.Join(dir, ".staging-conversation-cut")
	if err := os.Mkdir(stage, 0700); err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	f := watchCutFiles{dir: dir, stage: stage, watchCutJournal: watchCutJournal{Token: fmt.Sprint(time.Now().UnixNano())}}
	finish, err := a.prepareMemoryFiles(&f, scope, prepared.semantics, tx, index)
	if err != nil {
		return err
	}
	defer finish()
	if err := scope.commitDerivatives(tx); err != nil {
		return err
	}
	if err := a.commitForgetFiles(journal, &f, tx, index, nil); err != nil {
		return err
	}
	committed = &f
	for _, id := range scope.removedIDs {
		if err := os.RemoveAll(filepath.Join(dir, memoryHistoryDirName, id)); err != nil {
			return err
		}
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
	return memoryScopePreview(dir, preview, records, tx, index, historyVersionsOnly)
}
func memoryScopePreview(dir string, preview MemoryForgetPreview, records []*memoryForgetRecord, tx *sql.Tx, index *sql.DB, historyVersionsOnly bool) (MemoryForgetPreview, *memoryForgetScope, error) {
	id := preview.MemoryID
	var err error
	scope := &memoryForgetScope{rootID: id, records: records, preserveRoot: historyVersionsOnly}
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
