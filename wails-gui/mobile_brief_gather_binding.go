package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"wails-gui/internal/pii"
)

var errMobileBriefSourcesChanged = errors.New("brief sources changed during collection; regenerate the brief")

// This fence starts BEFORE the gather callback. A later source read can certify
// collected text only if nothing the user protected, deleted or paused, and
// none of the brief's file sources, changed throughout collection and capture.
// It watches what it is there for: privacy withdrawals (pause, protect, a
// sensitive app, a private folder, a deleted source, a sign-out), the model-read
// policy and the user's permission switches, the protected and existing
// activity sessions, and the memory, knowledge, handoff and topic files the
// brief reads. Ordinary writes (state database heartbeats, new activity rows)
// never reject it: on a working computer they land every few seconds, and a
// whole-database stamp failed most morning briefs (2026-10-27). Stored
// per-source content digests then protect composition and admission without
// invalidating briefs on every later background write. Keep this separate from
// the delivery filter and from the wire identity.
type mobileBriefGatherRevision struct {
	fileRevisions map[string]os.FileInfo
	files         todayChatAdmissionFileRevision
	activity      mobileBriefActivityPrivacyStamp
	startedMs     int64
	policy        string
	// withdrawn is set by any privacy withdrawal published after the fence
	// began; stopWatching ends that subscription (close).
	withdrawn    atomic.Bool
	stopWatching func()
	// memory is the memory folder as collection began. The brief is built
	// from it (mobileBriefPinnedMemoryKey), so the notes Isoform writes all
	// the time during a long collection change nothing the brief read.
	memory    *memoryExternalSnapshot
	memoryDir string
	// mu guards cited and forgotten. cited is the memory notes the brief
	// cites once it is built (cite), nil before: until then removing any
	// note counts. forgotten is the notes forgotten during collection.
	mu        sync.Mutex
	cited     map[string]bool
	forgotten map[string]bool
}

type mobileBriefPinnedMemoryKey struct{}

// mobileBriefPinnedMemory is the memory snapshot a collection is built from,
// or nil outside one.
func mobileBriefPinnedMemory(ctx context.Context) *memoryExternalSnapshot {
	snapshot, _ := ctx.Value(mobileBriefPinnedMemoryKey{}).(*memoryExternalSnapshot)
	return snapshot
}

// cite records the memory notes the built brief cites. From then on only
// removing or forgetting one of them rejects the brief.
func (r *mobileBriefGatherRevision) cite(brief MobileBriefV1) {
	if r == nil {
		return
	}
	cited := map[string]bool{}
	for _, ref := range brief.Evidence {
		if ref.SourceKind == "memory" || ref.SourceKind == taskContextSourceMemoryKind {
			id, _, _ := strings.Cut(ref.SourceID, ":")
			cited[id] = true
		}
	}
	r.mu.Lock()
	r.cited = cited
	r.mu.Unlock()
}

// noteCounts reports whether removing or forgetting the memory note id
// rejects the brief: any note before the brief is built, then a cited one.
func (r *mobileBriefGatherRevision) noteCounts(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cited == nil || r.cited[id]
}

// memoryNote is the id of the memory note at path, or "" for another file.
func (r *mobileBriefGatherRevision) memoryNote(path string) string {
	if r.memoryDir == "" || filepath.Dir(path) != r.memoryDir || filepath.Ext(path) != ".md" {
		return ""
	}
	return strings.TrimSuffix(filepath.Base(path), ".md")
}

func (r *mobileBriefGatherRevision) forgotCitedNote() bool {
	r.mu.Lock()
	ids := make([]string, 0, len(r.forgotten))
	for id := range r.forgotten {
		ids = append(ids, id)
	}
	r.mu.Unlock()
	for _, id := range ids {
		if r.noteCounts(id) {
			return true
		}
	}
	return false
}

// mobileBriefActivityPrivacyStamp is the privacy state of the activity
// sessions that existed when collection began: how many there are (a deleted
// one lowers it) and how many are protected. Sessions created later and new
// events in them never change it.
type mobileBriefActivityPrivacyStamp struct {
	sessions, protected int64
}

// mobileBriefGatherPermissions are the user's permission switches that decide
// what a brief may read.
var mobileBriefGatherPermissions = []string{"screen_recording", "accessibility", calendarPermissionKey, remindersPermissionKey, mailPermissionKey, filesPermissionKey, clipboardPermissionKey}

type mobileBriefGatherContextKey struct{}

func (a *App) readMobileBriefGatherRevision(ctx context.Context) (*mobileBriefGatherRevision, error) {
	if a == nil || a.store == nil || a.IsClosedEyesModeEnabled() {
		return nil, errMobileBriefSourcesChanged
	}
	revision := &mobileBriefGatherRevision{startedMs: time.Now().UnixMilli(), forgotten: map[string]bool{}}
	// Watch withdrawals first: one published during any later read rejects,
	// except a forgotten memory note, which rejects only a brief citing it.
	revision.stopWatching = subscribePrivacyWithdrawals(func(withdrawal privacyWithdrawal) {
		if scope := withdrawal.Scope; scope.Kind == privacyWithdrawalSource && scope.SourceKind == "memory" && scope.ID != "" {
			id, _, _ := strings.Cut(scope.ID, ":")
			revision.mu.Lock()
			revision.forgotten[id] = true
			revision.mu.Unlock()
			return
		}
		revision.withdrawn.Store(true)
	})
	var err error
	defer func() {
		if err != nil {
			revision.close()
		}
	}()
	revision.activity, err = a.mobileBriefActivityPrivacyStamp(ctx, revision.startedMs)
	if err != nil {
		return nil, err
	}
	revision.memoryDir, err = memoryDir()
	if err != nil {
		return nil, err
	}
	// Pinned after the file stamps: a note written between the two is in
	// the snapshot and is a change the fence sees.
	revision.fileRevisions, err = a.readMobileBriefGatherFiles(ctx)
	if err != nil {
		return nil, err
	}
	revision.memory, err = a.memorySnapshot()
	if err != nil {
		return nil, err
	}
	revision.files, err = a.readTodayChatSourceFilesWith(ctx, TodayChatContextRef{SourceKind: "brief"}, false, false)
	if err != nil {
		return nil, err
	}
	revision.policy = a.mobileBriefGatherPolicy(ctx)
	err = ctx.Err()
	if err != nil {
		return nil, err
	}
	return revision, nil
}

// close ends the withdrawal subscription. A closed fence still reads as
// changed if a withdrawal arrived while it was open.
func (r *mobileBriefGatherRevision) close() {
	if r != nil && r.stopWatching != nil {
		r.stopWatching()
		r.stopWatching = nil
	}
}

func (a *App) mobileBriefActivityPrivacyStamp(ctx context.Context, startedMs int64) (mobileBriefActivityPrivacyStamp, error) {
	var stamp mobileBriefActivityPrivacyStamp
	if a.activity == nil || a.activity.store == nil {
		return stamp, nil
	}
	err := a.activity.store.db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(CASE WHEN triage_status='protected' THEN 1 ELSE 0 END),0)
		FROM activity_sessions WHERE created_at <= ?`, startedMs).Scan(&stamp.sessions, &stamp.protected)
	return stamp, err
}

func (a *App) mobileBriefGatherPolicy(ctx context.Context) string {
	switches := map[string]bool{}
	for _, key := range mobileBriefGatherPermissions {
		switches[key] = a.isPermissionEnabledContext(ctx, key)
	}
	return mobileBriefModelDigest(struct {
		ClosedEyes, Calendar, Reminders bool
		SensitiveApps                   map[string]string
		Switches                        map[string]bool
	}{a.IsClosedEyesModeEnabled(), calendarAccessGranted(), remindersAccessGranted(), pii.GetUserSensitiveApps(), switches})
}

func (a *App) validateMobileBriefGatherRevision(ctx context.Context, revision *mobileBriefGatherRevision) bool {
	return a.mobileBriefGatherChange(ctx, revision) == ""
}

// mobileBriefGatherChange names what changed since the fence began, for the
// log (closed_eyes, privacy_withdrawal, policy, source_files, memory_files,
// activity_privacy, canceled), or "" when nothing did. It never names a
// record or its content.
func (a *App) mobileBriefGatherChange(ctx context.Context, revision *mobileBriefGatherRevision) string {
	if a == nil || a.store == nil || revision == nil {
		return "unavailable"
	}
	if a.IsClosedEyesModeEnabled() {
		return "closed_eyes"
	}
	if ctx.Err() != nil {
		return "canceled"
	}
	if revision.withdrawn.Load() || revision.forgotCitedNote() {
		return "privacy_withdrawal"
	}
	if revision.policy != a.mobileBriefGatherPolicy(ctx) {
		return "policy"
	}
	files, err := a.readTodayChatSourceFilesWith(ctx, TodayChatContextRef{SourceKind: "brief"}, false, false)
	if err != nil || files != revision.files {
		return "source_files"
	}
	currentFiles, err := a.readMobileBriefGatherFiles(ctx)
	if err != nil {
		return "memory_files"
	}
	// The pinned snapshot keeps unrelated new or edited notes out of this
	// collection. An edit to a cited note, like its removal, must reject the
	// old text and retry with a new snapshot.
	for path, old := range revision.fileRevisions {
		current, ok := currentFiles[path]
		if ok && os.SameFile(old, current) && old.ModTime() == current.ModTime() && old.Size() == current.Size() && old.Mode() == current.Mode() {
			continue
		}
		if id := revision.memoryNote(path); id != "" {
			if revision.noteCounts(id) {
				return "memory_files"
			}
			continue
		}
		return "memory_files"
	}
	for path := range currentFiles {
		if _, existed := revision.fileRevisions[path]; !existed && revision.memoryNote(path) == "" {
			return "memory_files"
		}
	}
	activity, err := a.mobileBriefActivityPrivacyStamp(ctx, revision.startedMs)
	if err != nil || activity != revision.activity {
		return "activity_privacy"
	}
	// Checked last too: a withdrawal published during the reads above.
	if revision.withdrawn.Load() || revision.forgotCitedNote() {
		return "privacy_withdrawal"
	}
	if ctx.Err() != nil {
		return "canceled"
	}
	return ""
}

// Content hashes alone miss an edit followed by restoration to the old bytes.
// Retain identity and nanosecond modification stamps for the whole collection
// interval, including atomic replacements that preserve the old timestamp.
// These process-local stamps never enter the stored proof or the wire format.
func (a *App) readMobileBriefGatherFiles(ctx context.Context) (map[string]os.FileInfo, error) {
	memory, err := memoryDir()
	if err != nil {
		return nil, err
	}
	knowledge, err := knowledgeDir()
	if err != nil {
		return nil, err
	}
	dirs := []string{memory, knowledge, filepath.Join(memory, ".semantic")}
	if a.handoffStore != nil {
		dirs = append(dirs, a.handoffStore.Dir())
	}
	files := map[string]os.FileInfo{}
	remember := func(path string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errMobileBriefSourcesChanged
		}
		files[path] = info
		return nil
	}
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			switch filepath.Ext(entry.Name()) {
			case ".md", ".json", ".jsonl":
				if err := remember(filepath.Join(dir, entry.Name())); err != nil {
					return nil, err
				}
			}
		}
	}
	if path := a.topicsSvc().store.path; path != "" {
		if err := remember(path); err != nil {
			return nil, err
		}
	}
	return files, ctx.Err()
}
