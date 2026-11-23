package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"time"

	"wails-gui/internal/privacy"
)

// Callers hold the memory mutation and embedding barriers until installation
// and both database operations finish. The journal contains cache hashes and
// paths so that index deletion can be repeated without the original Markdown.
func stageMemoryIndex(f *watchCutFiles, index *sql.DB, paths, contents []string) error {
	if index == nil {
		return nil
	}
	hashes, err := forgottenMemoryChunkHashes(index, paths[0], contents, paths[1:]...)
	if err != nil {
		return err
	}
	f.IndexPaths = append(f.IndexPaths, paths...)
	f.IndexHashes = append(f.IndexHashes, hashes...)
	return nil
}

func (a *App) prepareMemoryFiles(f *watchCutFiles, scope *memoryForgetScope, semantics memoryForgetSemantics, tx *sql.Tx, index *sql.DB) (func(), error) {
	dir, err := memoryDir()
	if err != nil {
		return nil, err
	}
	w, err := a.topicWithdrawalForSource("memory", scope.ids, tx, scope.contents...)
	if err != nil {
		return nil, err
	}
	f.notifyTopics = a.followedTopicWithdrawn(w)
	topics := a.topicsSvc().store
	topics.publication.Lock()
	topics.mu.Lock()
	portrait := a.portraitService()
	portrait.fileMu.Lock()
	memoryTombstoneMu.Lock()
	unlock := func() {
		memoryTombstoneMu.Unlock()
		portrait.fileMu.Unlock()
		topics.mu.Unlock()
		topics.publication.Unlock()
	}
	success := false
	defer func() {
		if !success {
			unlock()
		}
	}()
	if topics.path != "" {
		data, err := os.ReadFile(topics.path)
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		if err == nil {
			var state topicsStateFile
			if err := json.Unmarshal(data, &state); err != nil {
				return nil, err
			}
			if w.projection != topicWithdrawalProjection(state) {
				return nil, errTopicRegistryChanged
			}
			oldProjects := slices.Clone(state.Projects)
			oldChat := len(state.ChatTopics)
			before := proactiveVoiceHash(state)
			scrubTopicState(&state, w, false)
			f.notifyTopics = f.notifyTopics || len(state.ChatTopics) != oldChat || !reflect.DeepEqual(state.Projects, oldProjects)
			if proactiveVoiceHash(state) != before {
				raw, err := json.MarshalIndent(state, "", "  ")
				if err != nil {
					return nil, err
				}
				if err := f.add("@topics", raw); err != nil {
					return nil, err
				}
			}
		}
	}
	path, err := portraitPath()
	if err != nil {
		return nil, err
	}
	doc, err := readPortrait(path)
	if err != nil {
		return nil, err
	}
	if doc != nil && removePortraitVersions(doc, scope.removedIDs, scope.records) {
		raw, err := json.Marshal(doc)
		if err != nil {
			return nil, err
		}
		if err := f.add("@portrait", raw); err != nil {
			return nil, err
		}
	}
	stones, err := loadMemoryTombstones(dir)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	stones = slices.DeleteFunc(stones, func(t memoryTombstone) bool { return t.ExpiresAt <= now.UnixMilli() })
	semantics.RemovedIDs = scope.removedIDs
	semantics.RemovedVersions = scope.removedVersions
	for _, r := range scope.records {
		id := r.id
		if r.preserveLive && scope.rootID != "" {
			id = scope.rootID
		}
		var stone memoryTombstone
		if r.versionOnlySuppression {
			// Suppress the erased versions while allowing a new observation with
			// the same deterministic ID and wording from the surviving conversation.
			stone = memoryTombstone{Version: memoryTombstoneVersion, ExpiresAt: now.Add(memoryForgetRetentionDays * 24 * time.Hour).UnixMilli(), Keys: []string{memoryHistoricalSourceKey(id)}}
			for _, content := range r.contents {
				stone.Keys = append(stone.Keys, memoryForgetHash(memoryForgetHistoricalSelector(id, content)))
			}
		} else {
			stone, err = prepareMemoryTombstone(dir, id, r.contents, now, (scope.preserveRoot && id == scope.rootID) || (r.preserveLive && scope.rootID == ""), semantics)
			if err != nil {
				return nil, err
			}
		}
		stones = append(stones, stone)
		paths := append(slices.Clone(r.archivePaths), r.historyPaths...)
		if !r.preserveLive && r.livePath != "" {
			paths = append(paths, r.livePath)
		}
		for _, p := range paths {
			rel, err := filepath.Rel(dir, p)
			if err != nil {
				return nil, err
			}
			if err := f.add("@memory/"+rel, nil); err != nil {
				return nil, err
			}
		}
	}
	raw, err := json.Marshal(stones)
	if err != nil {
		return nil, err
	}
	if err := f.add("@memory/"+memoryTombstonesFile, raw); err != nil {
		return nil, err
	}
	if len(scope.rejections.items) > 0 {
		if err := f.add("@memory/"+memoryRejectionsLogName, append([]byte{}, scope.rejections.after...)); err != nil {
			return nil, err
		}
	}
	if err := stageMemoryIndex(f, index, scope.paths, scope.contents); err != nil {
		return nil, err
	}
	f.WithdrawnIDs = slices.Clone(scope.removedIDs)
	if scope.preserveRoot {
		f.WithdrawnIDs = append(f.WithdrawnIDs, scope.rootID)
	}
	f.CleanContextUses = a.store != nil
	success = true
	return unlock, nil
}

func (a *App) stageWatchMemory(f *watchCutFiles, p *watchForgetPlan, tx *sql.Tx, index *sql.DB) (func(), error) {
	dir, err := memoryDir()
	if err != nil {
		return nil, err
	}
	for _, change := range p.supportChanges {
		rel, err := filepath.Rel(dir, change.path)
		if err != nil {
			return nil, err
		}
		if err := f.add("@memory/"+rel, []byte(change.after)); err != nil {
			return nil, err
		}
	}
	if len(p.supportChanges) > 0 && index != nil {
		var paths, contents []string
		for _, c := range p.supportChanges {
			contents = append(contents, c.before)
		}
		for _, claim := range p.preview.ClaimsLosingSupport {
			path := claim.ID + ".md"
			if !slices.ContainsFunc(p.supportChanges, func(c watchClaimChange) bool { return c.path == filepath.Join(dir, path) }) {
				continue
			}
			paths = append(paths, path)
			var indexed bool
			if err := index.QueryRow("SELECT EXISTS(SELECT 1 FROM memory_files WHERE path=? AND source='memory')", path).Scan(&indexed); err != nil {
				return nil, err
			}
			if indexed {
				f.Reindex = append(f.Reindex, path)
			}
		}
		if len(paths) == 0 {
			paths = []string{""}
		}
		if err := stageMemoryIndex(f, index, paths, contents); err != nil {
			return nil, err
		}
	}
	if p.claimScope == nil {
		return func() {}, nil
	}
	scope := p.claimScope
	var suppressed []string
	for _, r := range scope.records {
		if !r.versionOnlySuppression {
			suppressed = append(suppressed, r.contents...)
		}
	}
	sem, err := a.prepareMemoryForgetSemantics(suppressed)
	if err != nil {
		return nil, err
	}
	unlock, err := a.prepareMemoryFiles(f, scope, sem, tx, index)
	if err != nil {
		return nil, err
	}
	if err := scope.commitDerivatives(tx); err != nil {
		unlock()
		return nil, err
	}
	p.notifyTopics = f.notifyTopics
	p.withdrawnIDs = f.WithdrawnIDs
	return unlock, nil
}
func (s *memoryForgetScope) commitDerivatives(tx *sql.Tx) error {
	if tx == nil {
		return nil
	}
	return deleteMemoryForgetDerivatives(tx, s.ids[0], s.ids[1:]...)
}
func (a *App) publishForgottenMemory(ids []string, notifyTopics bool) {
	a.clearPageSnapshots()
	memoryTombstoneGeneration.Add(1)
	derivedDiskDropAll()
	if len(ids) > 0 || notifyTopics {
		a.topicsSvc().invalidateChatSources()
		if notifyTopics {
			a.emitBrowserEvent("topics:withdrawn")
		}
		a.portraitService().poke()
	}
	for _, id := range ids {
		briefPrivacyChanged(briefWithdrawn{Kind: string(MemorySourceMemory), ID: id})
		publishPrivacyWithdrawal(privacy.Scope{Kind: privacy.Source, SourceKind: "memory", ID: id})
	}
	briefPrivacyAmendSoon()
	a.markMemoryDirty("forget")
	if a.ctx != nil {
		a.emitMemoryUpdated(true)
	}
}

// Rebuild the local searchable rows of an already indexed surviving claim
// from its installed document. Keep the hash stale so normal sync can refill
// its optional embeddings without invoking an external provider during Forget.
func (j watchCutJournal) applyIndex(tx *sql.Tx) error {
	if err := deleteForgottenMemoryIndex(tx, j.IndexHashes, j.IndexPaths); err != nil {
		return err
	}
	for _, path := range j.Reindex {
		if !filepath.IsLocal(path) || filepath.Base(path) != path {
			return errors.New("invalid memory index path")
		}
		dir, err := memoryDir()
		if err != nil {
			return err
		}
		raw, err := os.ReadFile(filepath.Join(dir, path))
		if err != nil {
			return err
		}
		if _, err := tx.Exec("INSERT INTO memory_files(path,source,hash,mtime,size) VALUES(?,'memory',?,0,?)", path, fmt.Sprintf("reindex:%x", sha256.Sum256(raw)), len(raw)); err != nil {
			return err
		}
		for i, c := range chunkMarkdown(string(raw), memoryChunkTokens, memoryChunkOverlap) {
			id := fmt.Sprintf("%s:%d", path, i)
			if _, err := tx.Exec("INSERT INTO memory_chunks(id,path,source,start_line,end_line,hash,text) VALUES(?,?,'memory',?,?,?,?)", id, path, c.StartLine, c.EndLine, c.Hash, c.Text); err != nil {
				return err
			}
			if _, err := tx.Exec("INSERT INTO memory_chunks_fts(id,path,source,start_line,end_line,text,model) VALUES(?,?,'memory',?,?,?,'')", id, path, c.StartLine, c.EndLine, c.Text); err != nil {
				return err
			}
		}
	}
	return nil
}

type memoryForgetDatabases struct {
	index       *sql.Tx
	contextUses func(bool) error
}

func (a *App) prepareMemoryDatabases(f *watchCutFiles, index *sql.DB) (memoryForgetDatabases, error) {
	var dbs memoryForgetDatabases
	if len(f.IndexPaths) > 0 {
		tx, err := index.Begin()
		if err != nil {
			return dbs, err
		}
		if err := f.applyIndex(tx); err != nil {
			tx.Rollback()
			return dbs, err
		}
		dbs.index = tx
	}
	if f.CleanContextUses {
		finish, err := a.store.PrepareContextUseDeletion(context.Background(), "memory", f.WithdrawnIDs...)
		if err != nil {
			dbs.rollback()
			return dbs, err
		}
		dbs.contextUses = finish
	}
	return dbs, nil
}
func (d *memoryForgetDatabases) rollback() {
	if d.index != nil {
		d.index.Rollback()
	}
	if d.contextUses != nil {
		d.contextUses(false)
		d.contextUses = nil
	}
}
func (d *memoryForgetDatabases) commit() error {
	if d.index != nil {
		if err := d.index.Commit(); err != nil {
			return err
		}
	}
	if d.contextUses != nil {
		finish := d.contextUses
		d.contextUses = nil
		return finish(true)
	}
	return nil
}
