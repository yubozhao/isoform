package main

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"hash"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Only hashes survive the admission read. They also detect edits made outside
// the app's writer locks while the publication waits for its database turn.
type todayChatAdmissionFileRevision [sha256.Size]byte

// Keep the mutable file services stable from the fresh source read through
// commit. Their ordinary getters use their own mutexes, never this barrier.
func (a *App) lockTodayChatFileServices(ctx context.Context, ref TodayChatContextRef) (func(), error) {
	type publicationLock struct {
		try     func() bool
		release func()
	}
	var locks []publicationLock
	if ref.SourceKind == "brief_item" || ref.SourceKind == "brief" || ref.SourceKind == todayChatBriefCardKind {
		locks = append(locks, publicationLock{mobileBriefPublicationMu.TryRLock, mobileBriefPublicationMu.RUnlock})
	}
	switch ref.SourceKind {
	case "topic", "find", "brief_item", "brief", todayChatBriefCardKind:
		store := a.topicsSvc().store
		locks = append(locks, publicationLock{store.publication.TryRLock, store.publication.RUnlock})
		interests := a.interestsSvc().store
		locks = append(locks, publicationLock{interests.publication.TryRLock, interests.publication.RUnlock})
	}
	switch ref.SourceKind {
	case "handoff", "history", "deliverable", "brief_item", "brief", todayChatBriefCardKind:
		if a.handoffStore != nil {
			locks = append(locks, publicationLock{a.handoffStore.TryLockForPublication, a.handoffStore.UnlockForPublication})
		}
	}
	var acquired []func()
	var once sync.Once
	release := func() {
		once.Do(func() {
			for i := len(acquired) - 1; i >= 0; i-- {
				acquired[i]()
			}
		})
	}
	for _, lock := range locks {
		if err := lockTodayChatPublication(ctx, lock.try); err != nil {
			release()
			return nil, err
		}
		acquired = append(acquired, lock.release)
	}
	return release, nil
}

func (a *App) readTodayChatAdmissionFiles(ctx context.Context, ref TodayChatContextRef) (todayChatAdmissionFileRevision, error) {
	return a.readTodayChatSourceFiles(ctx, ref, true)
}

// Collection fences the same source files without including generated brief
// outputs, which are not inputs to the frozen synthesis.
func (a *App) readTodayChatSourceFiles(ctx context.Context, ref TodayChatContextRef, includeBrief bool) (todayChatAdmissionFileRevision, error) {
	return a.readTodayChatSourceFilesWith(ctx, ref, includeBrief, true)
}

// readTodayChatSourceFilesWith is readTodayChatSourceFiles, leaving out the
// memory notes themselves when memoryNotes is false: the brief's collection
// fence checks those one by one (mobileBriefGatherChange).
func (a *App) readTodayChatSourceFilesWith(ctx context.Context, ref TodayChatContextRef, includeBrief, memoryNotes bool) (todayChatAdmissionFileRevision, error) {
	var empty todayChatAdmissionFileRevision
	digest := sha256.New()
	memory, err := memoryDir()
	if err != nil {
		return empty, err
	}
	knowledge, err := knowledgeDir()
	if err != nil {
		return empty, err
	}
	for _, source := range []struct{ key, dir string }{{"memory", memory}, {"knowledge", knowledge}, {"memory semantic controls", filepath.Join(memory, ".semantic")}} {
		var keep func(string) bool
		if !memoryNotes && source.key == "memory" {
			keep = func(name string) bool { return filepath.Ext(name) != ".md" }
		}
		if err := todayChatAdmissionDirectoryFiltered(ctx, digest, source.key, source.dir, keep); err != nil {
			return empty, err
		}
	}
	if includeBrief && (ref.SourceKind == "brief_item" || ref.SourceKind == "brief" || ref.SourceKind == todayChatBriefCardKind) {
		if !mobileBriefIDPattern.MatchString(ref.BriefID) {
			return empty, errTodayChatItemUnavailable
		}
		dir, err := mobileBriefStateDir()
		if err != nil {
			return empty, err
		}
		if err := todayChatAdmissionFile(ctx, digest, "selected brief", filepath.Join(dir, mobileBriefDirName, ref.BriefID+".json")); err != nil {
			return empty, err
		}
		if err := todayChatAdmissionFile(ctx, digest, "current Today brief", filepath.Join(dir, mobileBriefDirName, mobileBriefTodayFileName)); err != nil {
			return empty, err
		}
	}
	// Brief references and deliverable ancestry can point at either service.
	// These reads do not rank topics, refresh providers or access a database.
	switch ref.SourceKind {
	case "topic", "find", "brief_item", "brief", todayChatBriefCardKind:
		if err := todayChatAdmissionValue(ctx, digest, "interests", a.interestsSvc().store.load()); err != nil {
			return empty, err
		}
		state := a.topicsSvc().store.load()
		if err := todayChatAdmissionValue(ctx, digest, "topics", state); err != nil {
			return empty, err
		}
	}
	switch ref.SourceKind {
	case "handoff", "history", "deliverable", "brief_item", "brief", todayChatBriefCardKind:
		if a.handoffStore != nil {
			entries, err := a.handoffStore.List()
			if err != nil {
				return empty, err
			}
			if err := todayChatAdmissionValue(ctx, digest, "handoffs", entries); err != nil {
				return empty, err
			}
		}
	}

	if err := ctx.Err(); err != nil {
		return empty, err
	}
	var revision todayChatAdmissionFileRevision
	copy(revision[:], digest.Sum(nil))
	return revision, nil
}

func (a *App) validateTodayChatAdmissionFiles(ctx context.Context, ref TodayChatContextRef, expected todayChatAdmissionFileRevision) error {
	current, err := a.readTodayChatAdmissionFiles(ctx, ref)
	if err != nil {
		return err
	}
	if current != expected {
		return errTodayChatItemUnavailable
	}
	return nil
}

// todayChatAdmissionDirectoryFiltered hashes the files keep accepts (all
// when keep is nil).
func todayChatAdmissionDirectoryFiltered(ctx context.Context, digest hash.Hash, key, dir string, keep func(string) bool) error {
	info, err := os.Lstat(dir)
	if os.IsNotExist(err) {
		todayChatAdmissionHash(digest, key, nil)
		return ctx.Err()
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errTodayChatItemUnavailable
	}
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		todayChatAdmissionHash(digest, key, nil)
		return ctx.Err()
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		name := entry.Name()
		if entry.IsDir() || keep != nil && !keep(name) {
			continue
		}
		switch filepath.Ext(name) {
		case ".md", ".json", ".jsonl":
			if err := todayChatAdmissionFile(ctx, digest, key+"/"+name, filepath.Join(dir, name)); err != nil {
				return err
			}
		}
	}
	return nil
}

func todayChatAdmissionFile(ctx context.Context, digest hash.Hash, key, path string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		todayChatAdmissionHash(digest, key+"/missing", nil)
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errTodayChatItemUnavailable
	}
	if sum, ok := cachedAdmissionFileSum(path, info); ok {
		todayChatAdmissionHash(digest, key, sum)
		return nil
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	content := sha256.New()
	if _, err := io.Copy(content, todayChatAdmissionReader{ctx: ctx, reader: file}); err != nil {
		return err
	}
	sum := content.Sum(nil)
	rememberAdmissionFileSum(path, info, sum)
	todayChatAdmissionHash(digest, key, sum)
	return nil
}

type todayChatAdmissionReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r todayChatAdmissionReader) Read(bytes []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(bytes)
}

func todayChatAdmissionValue(ctx context.Context, digest hash.Hash, key string, value any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	todayChatAdmissionHash(digest, key, data)
	return ctx.Err()
}

func todayChatAdmissionHash(digest hash.Hash, key string, data []byte) {
	// Length framing prevents a renamed file from colliding with adjacent data.
	_ = binary.Write(digest, binary.LittleEndian, uint64(len(key)))
	_, _ = io.Copy(digest, strings.NewReader(key))
	_ = binary.Write(digest, binary.LittleEndian, uint64(len(data)))
	_, _ = digest.Write(data)
}
