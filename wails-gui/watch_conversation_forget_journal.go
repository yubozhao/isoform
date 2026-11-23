package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Stage and fsync every replacement before publishing the journal. Hard links
// retain the old files until SQLite commits. Recovery uses a transaction receipt
// instead of inferring commit from the presence or contents of an audio file.
const watchForgetJournal = ".conversation-cut"
const watchCutReceiptSchema = "CREATE TABLE IF NOT EXISTS watch_conversation_cut_receipts (token TEXT PRIMARY KEY)"

type watchCutJournal struct {
	Token                                 string
	Paths                                 []string
	Created                               []int
	IndexPaths, IndexHashes, WithdrawnIDs []string
	FileReceipt                           bool
	CleanContextUses                      bool
	Reindex                               []string
}
type watchCutFiles struct {
	dir, stage   string
	notifyTopics bool
	watchCutJournal
}

func (f *watchCutFiles) add(rel string, data []byte) error {
	path, err := cutFilePath(f.dir, rel)
	if err != nil {
		return err
	}
	n := strconv.Itoa(len(f.Paths))
	if err := os.Link(path, filepath.Join(f.stage, "old"+n)); err != nil {
		if !os.IsNotExist(err) || data == nil {
			return err
		}
		f.Created = append(f.Created, len(f.Paths))
	}
	if data != nil {
		if err := mobileRecordingWriteFile(filepath.Join(f.stage, "new"+n), data, 0600); err != nil {
			return err
		}
	}
	f.Paths = append(f.Paths, rel)
	return nil
}

func syncWatchCutDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
func (f *watchCutFiles) install() error {
	for i, rel := range f.Paths {
		if rel == "@topics" {
			continue
		} // Topic withdrawal follows the receipt.

		src := filepath.Join(f.stage, "new"+strconv.Itoa(i))
		dest, err := cutFilePath(f.dir, rel)
		if err != nil {
			return err
		}
		if _, err := os.Stat(src); errors.Is(err, os.ErrNotExist) {
			if err := os.Remove(dest); err != nil {
				return err
			}
		} else if err != nil {
			return err
		} else if err := os.Rename(src, dest); err != nil {
			return err
		}
		if err := syncWatchCutDir(filepath.Dir(dest)); err != nil {
			return err
		}
	}
	return nil
}

func (s *mobileRecordingStore) recoverWatchCutLocked() error { return s.resolveWatchCut(false) }
func (s *mobileRecordingStore) resolveWatchCut(memoryCompleted bool) error {
	stage := filepath.Join(s.dir, watchForgetJournal)
	// Probe the three possible crash states by name. A normal first load
	// does not enumerate recordings, remove files, or open the receipt DB.
	for _, name := range []string{".staging-conversation-cut", ".retired-conversation-cut"} {
		path := filepath.Join(s.dir, name)
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return err
		}
		if err := os.RemoveAll(path); err != nil {
			return err
		}
	}
	if _, err := os.Lstat(stage); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	data, err := os.ReadFile(filepath.Join(stage, "journal.json"))
	if errors.Is(err, os.ErrNotExist) {
		// The journal is durable before installation can move a live file.
		return os.RemoveAll(stage)
	}
	if err != nil {
		return err
	}
	var j watchCutJournal
	if err := json.Unmarshal(data, &j); err != nil {
		return err
	}
	var committed bool
	if j.FileReceipt {
		_, err := os.Stat(filepath.Join(stage, "committed"))
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		committed = err == nil
	} else {
		if s.cutDB == nil {
			return errors.New("conversation cut awaits activity storage")
		}
		if err := s.cutDB.QueryRow("SELECT EXISTS(SELECT 1 FROM watch_conversation_cut_receipts WHERE token=?)", j.Token).Scan(&committed); err != nil {
			return err
		}
	}
	if !committed {
		if err := restoreCutFiles(s.dir, stage, j); err != nil {
			return err
		}
	} else {
		if i := slices.Index(j.Paths, "@topics"); i >= 0 {
			dest, err := cutFilePath(s.dir, "@topics")
			if err != nil {
				return err
			}
			if err := installCutLink(stage, "new"+strconv.Itoa(i), dest); err != nil {
				return err
			}
		}
		if !memoryCompleted {
			if err := s.finishCutMemory(j); err != nil {
				return err
			}
		}
		if len(j.WithdrawnIDs) > 0 || len(j.Reindex) > 0 {
			keys := make([]briefWithdrawn, 0, len(j.WithdrawnIDs))
			for _, id := range j.WithdrawnIDs {
				keys = append(keys, briefWithdrawn{Kind: "memory", ID: id})
			}
			briefPrivacyChanged(keys...)
		}
	}
	// Atomically retire the marker before unlinking backups.
	retired := filepath.Join(s.dir, ".retired-conversation-cut")
	if err := os.RemoveAll(retired); err != nil {
		return err
	}
	if err := os.Rename(stage, retired); err != nil {
		return err
	}
	if err := syncWatchCutDir(s.dir); err != nil {
		return err
	}
	if err := os.RemoveAll(retired); err != nil {
		return err
	}
	if j.FileReceipt {
		return nil
	}
	_, err = s.cutDB.Exec("DELETE FROM watch_conversation_cut_receipts WHERE token=?", j.Token)
	return err
}

func stageWatchCutIndex[T any](files *watchCutFiles, name, id string) error {
	entries, err := readWatchForgetIndex[T](files.dir, name)
	if err != nil {
		return err
	}
	if _, ok := entries[id]; !ok {
		return nil
	}
	delete(entries, id)
	var data []byte
	if len(entries) > 0 {
		data, err = json.Marshal(entries)
		if err != nil {
			return err
		}
	}
	return files.add(name, data)
}

func stageWatchHour(f *watchCutFiles, h watchForgetHour, cuts []WatchConversationAudioCut) (mobileRecording, error) {
	rec := h.recording
	rec.Received = append([]mobileRecordingPart(nil), rec.Received...)
	start, err := time.Parse(time.RFC3339, rec.view().StartedAt)
	if err != nil {
		return rec, err
	}
	for i := range rec.Received {
		part := &rec.Received[i]
		if !filepath.IsLocal(part.File) || part.File == "." || part.File != filepath.Base(part.File) || !mobileValidUUID(rec.ID) {
			return rec, errors.New("invalid recording part path")
		}
		rel := filepath.Join(rec.ID, part.File)
		if !rec.AudioRemoved && !part.Paused {
			from, err := time.Parse(time.RFC3339, part.StartedAt)
			if err != nil {
				return rec, err
			}
			length := int64(math.Round(part.DurationSeconds * 1000))
			offset := from.UnixMilli() - start.UnixMilli()
			for _, cut := range cuts {
				if cut.RecordingID != rec.ID {
					continue
				}
				wallLo, wallHi := max(int64(0), cut.StartMs-offset), min(part.watchOriginalDurationMs(), cut.EndMs-offset)
				lo, hi := part.watchFileOffset(wallLo), part.watchFileOffset(wallHi)
				if hi <= lo {
					continue
				}
				marks := make([]float64, 0, len(part.Marks))
				for _, mark := range part.Marks {
					ms := int64(math.Round(mark * 1000))
					if ms < wallLo || ms >= wallHi {
						marks = append(marks, mark)
					}
				}
				part.Marks = marks
				if h.whole || hi-lo == length {
					if err := f.add(rel, nil); err != nil {
						return rec, err
					}
					part.DurationSeconds = 0
					part.Paused = true
				} else {
					bin := mobileRecordingFFmpegPath()
					if bin == "" {
						return rec, errors.New("ffmpeg is unavailable")
					}
					ctx, cancel := context.WithTimeout(context.Background(), meetingAudioEncodeTimeout+time.Duration(length)*time.Millisecond)
					defer cancel()
					path := filepath.Join(f.dir, rel)
					secs, err := decodedAudioSeconds(ctx, bin, path)
					if err != nil {
						return rec, err
					}
					if math.Abs(secs-part.DurationSeconds) > .08 {
						return rec, errors.New("recording duration disagrees with its audio")
					}
					var spans []mobileBriefAudioCut
					if lo > 0 {
						spans = append(spans, mobileBriefAudioCut{start: 0, end: float64(lo) / 1000})
					}
					if hi < length {
						spans = append(spans, mobileBriefAudioCut{start: float64(hi) / 1000, end: part.DurationSeconds})
					}
					data, err := cutAudioWithFFmpeg(ctx, bin, path, rec.Mime, spans)
					if err != nil {
						return rec, err
					}
					if err := f.add(rel, data); err != nil {
						return rec, err
					}
					staged := filepath.Join(f.stage, "new"+strconv.Itoa(len(f.Paths)-1))
					secs, err = decodedAudioSeconds(ctx, bin, staged)
					if err != nil {
						return rec, err
					}
					part.DurationSeconds = float64(length-hi+lo) / 1000
					if math.Abs(secs-part.DurationSeconds) > .08 {
						return rec, errors.New("cut audio length could not be verified")
					}
					// Bytes and SHA256 identify the original upload on a retry.
					// Old upload identity still identifies retransmissions. Keep
					// a wall-clock map independently of the shortened media.
					part.ConversationCuts = append(append([]watchPartInterval(nil), part.ConversationCuts...), watchPartInterval{wallLo, wallHi})
				}
				break
			}
		}
		if h.whole {
			part.Context = nil
			part.Marks = nil
		}
	}
	voicePath := filepath.Join(rec.ID, mobileRecordingVoicesFile)
	if _, err := os.Stat(filepath.Join(f.dir, voicePath)); err == nil {
		if !h.whole {
			return rec, errors.New("the shared recording has a combined speaker fingerprint")
		}
		if err := f.add(voicePath, nil); err != nil {
			return rec, err
		}
	} else if !os.IsNotExist(err) {
		return rec, err
	}
	rec.Words -= h.words
	if h.whole {
		rec.State = mobileRecordingDeleted
		rec.AudioRemoved = true
		rec.Words = 0
		rec.Title = ""
		rec.SessionID = ""
		rec.OwnerVoice = ""
		rec.Error = ""
	}
	rec.UpdatedAt = mobileTimestamp(time.Now())
	return rec, nil
}

type watchPartInterval struct{ StartMs, EndMs int64 }

func (p mobileRecordingPart) watchOriginalDurationMs() int64 {
	length := int64(math.Round(p.DurationSeconds * 1000))
	for _, cut := range p.ConversationCuts {
		length += cut.EndMs - cut.StartMs
	}
	return length
}
func (p mobileRecordingPart) watchFileOffset(wallMs int64) int64 {
	offset := wallMs
	for _, cut := range p.ConversationCuts {
		offset -= max(int64(0), min(wallMs, cut.EndMs)-cut.StartMs)
	}
	return offset
}

var watchCutMu sync.RWMutex

func (a *App) ForgetWatchConversation(conversationID string) error {
	watchCutMu.Lock()
	defer watchCutMu.Unlock()
	p, err := a.applyWatchForget(conversationID)
	if err != nil {
		return err
	}
	store := a.watchRecordingStore()
	delete(store.forgetPreviews, conversationID)
	for _, hour := range p.hours {
		if hour.whole {
			publishPrivacyWithdrawal(privacyWithdrawalScope{Kind: privacyWithdrawalSession, ID: hour.recording.SessionID})
		}
	}
	if p.claimScope != nil || len(p.supportChanges) > 0 {
		a.publishForgottenMemory(p.withdrawnIDs, p.notifyTopics)
	}
	briefPrivacyChanged()
	a.touchDerivedViews(derivedWorkList)
	_, err = a.watchConversationsOf(store)
	return err
}

func (a *App) applyWatchForget(conversationID string) (*watchForgetPlan, error) {
	p, err := a.watchForgetPlan(conversationID)
	if err != nil {
		return nil, err
	}

	store := a.watchRecordingStore()
	if expected, ok := store.forgetPreviews[conversationID]; ok && expected != p.signature {
		return nil, errors.New("conversation changed; preview it again")
	}
	err = a.commitWatchCut(store, p, nil)
	if err != nil {
		return nil, err
	}
	return p, nil
}

// The crash hook can interrupt immediately before or after the receipt.
func (a *App) commitWatchCut(store *mobileRecordingStore, p *watchForgetPlan, interrupted func() bool) (result error) {
	watchConversationNotesMu.Lock()
	defer watchConversationNotesMu.Unlock()
	watchConversationNamesMu.Lock()
	defer watchConversationNamesMu.Unlock()
	watchSpeakerNamesMu.Lock()
	defer watchSpeakerNamesMu.Unlock()
	memoryMutationMu.Lock()
	defer memoryMutationMu.Unlock()
	var claimIDs []string
	if p.claimScope != nil {
		for _, r := range p.claimScope.records {
			claimIDs = append(claimIDs, r.id)
		}
	}
	for _, item := range p.preview.ClaimsLosingSupport {
		claimIDs = append(claimIDs, item.ID)
	}
	slices.Sort(claimIDs)
	for _, id := range slices.Compact(claimIDs) {
		unlock := memoryFileLocks.Acquire(id)
		defer unlock()
	}
	index, release, err := a.lockMemoryForgetIndex()
	if err != nil {
		return err
	}
	defer release()
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.loadLocked(); err != nil {
		return err
	}
	activity := a.activity.store
	activity.readPrivacyMu.Lock()
	defer activity.readPrivacyMu.Unlock()
	activity.readPrivacyRevision.Add(1)
	defer activity.readPrivacyRevision.Add(1)
	db := activity.db
	store.cutDB = db
	store.cutUses = a.store
	if _, err := db.Exec(watchCutReceiptSchema); err != nil {
		return err
	}
	staging := filepath.Join(store.dir, ".staging-conversation-cut")
	if err := os.RemoveAll(staging); err != nil {
		return err
	}
	if err := os.Mkdir(staging, 0700); err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	f := watchCutFiles{dir: store.dir, stage: staging, watchCutJournal: watchCutJournal{Token: fmt.Sprintf("%d", time.Now().UnixNano())}}
	replacements := map[string]mobileRecording{}
	for _, h := range p.hours {
		current := store.recordings[h.recording.ID]
		if !reflect.DeepEqual(current, &h.recording) {
			return errors.New("recording changed; preview it again")
		}
		rec, err := stageWatchHour(&f, h, p.preview.Audio)
		if err != nil {
			return err
		}
		replacements[rec.ID] = rec
	}
	if err := stageWatchCutIndex[watchConversationNotesEntry](&f, watchConversationNotesFile, p.preview.ID); err != nil {
		return err
	}
	if err := stageWatchCutIndex[watchConversationNameEntry](&f, watchConversationNamesFile, p.preview.ID); err != nil {
		return err
	}
	if err := stageWatchCutIndex[watchSpeakerNamesEntry](&f, watchSpeakerNamesFile, p.preview.ID); err != nil {
		return err
	}
	list := make([]mobileRecording, 0, len(store.recordings))
	for id, rec := range store.recordings {
		if r, ok := replacements[id]; ok {
			list = append(list, r)
		} else {
			list = append(list, *rec)
		}
	}
	data, err := json.Marshal(map[string]any{"version": 1, "recordings": list})
	if err != nil {
		return err
	}
	if err := f.add("index.json", data); err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	check := watchForgetPlan{hours: p.hours, speech: p.speech}
	if err := check.readClaims(tx, index); err != nil {
		return err
	}
	if check.claimSignature != p.claimSignature {
		return errors.New("claim evidence changed; preview it again")
	}
	if p.claimScope != nil || len(p.supportChanges) > 0 {
		release, err := a.stageWatchMemory(&f, p, tx, index)
		if err != nil {
			return err
		}
		defer release()
	}
	var recs []mobileRecording
	for _, h := range p.hours {
		recs = append(recs, h.recording)
	}
	current, err := readWatchSpeech(context.Background(), tx, recs, time.Time{}, nil)
	if err != nil {
		return err
	}
	// The planner enumerates recordings newest first. Keep comparison
	// independent of which hour supplied the conversation's first line.
	byRow := map[int64]watchForgetSpeech{}
	for _, line := range current {
		byRow[line.rowID] = line
	}
	if len(current) != len(p.speech) {
		return errors.New("recording changed; preview it again")
	}
	for _, line := range p.speech {
		if byRow[line.rowID] != line {
			return errors.New("recording changed; preview it again")
		}
	}
	for _, h := range p.hours {
		for _, event := range h.events {
			r, err := tx.Exec("DELETE FROM activity_events WHERE id=? AND session_id=?", event, h.recording.SessionID)
			if err != nil {
				return err
			}
			n, err := r.RowsAffected()
			if err != nil {
				return err
			}
			if n != 1 {
				return errWatchConversationGone
			}
		}
		if h.whole {
			if err := a.activity.store.deleteActivitySessionTx(tx, h.recording.SessionID, false); err != nil {
				return err
			}
		} else {
			if _, err := tx.Exec("UPDATE activity_sessions SET event_count=(SELECT COUNT(*) FROM activity_events WHERE session_id=?) WHERE id=?", h.recording.SessionID, h.recording.SessionID); err != nil {
				return err
			}
		}
	}
	if len(p.loops) > 0 {
		if err := deleteMemoryForgetDerivatives(tx, p.loops[0], p.loops[1:]...); err != nil {
			return err
		}
	}
	if err := a.commitForgetFiles(store, &f, tx, index, interrupted); err != nil {
		return err
	}
	if activity.readPrivacySessionRevisions == nil {
		activity.readPrivacySessionRevisions = map[string]uint64{}
	}
	for _, h := range p.hours {
		activity.readPrivacySessionRevisions[h.recording.SessionID] = activity.readPrivacyRevision.Load() + 1
	}
	return nil
}

var errWatchCutInterrupted = errors.New("conversation cut interrupted")

func restoreCutFiles(dir, stage string, j watchCutJournal) error {
	for i, rel := range j.Paths {
		dest, err := cutFilePath(dir, rel)
		if err != nil {
			return err
		}
		if slices.Contains(j.Created, i) {
			if err := os.Remove(dest); err != nil && !os.IsNotExist(err) {
				return err
			}
			if err := syncWatchCutDir(filepath.Dir(dest)); err != nil {
				return err
			}
			continue
		}
		if err := installCutLink(stage, "old"+strconv.Itoa(i), dest); err != nil {
			return err
		}
	}
	return nil
}

func cutFilePath(dir, rel string) (string, error) {
	if !filepath.IsLocal(rel) {
		return "", errors.New("invalid recovery path")
	}
	switch rel {
	case "@portrait":
		return portraitPath()
	case "@topics":
		root, err := topicsStateDir()
		return filepath.Join(root, topicsStateFileName), err
	}
	if name, ok := strings.CutPrefix(rel, "@memory/"); ok {
		if !filepath.IsLocal(name) {
			return "", errors.New("invalid memory recovery path")
		}
		root, err := memoryDir()
		return filepath.Join(root, name), err
	}
	return filepath.Join(dir, rel), nil
}

func (s *mobileRecordingStore) finishCutMemory(j watchCutJournal) error {
	if len(j.IndexPaths) > 0 {
		dir, err := memoryDir()
		if err != nil {
			return err
		}
		db, err := openCutDatabase(filepath.Join(filepath.Dir(dir), "memory.db"))
		if err != nil {
			return err
		}
		defer db.Close()
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if err := j.applyIndex(tx); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	if j.CleanContextUses {
		if s.cutUses == nil {
			return errors.New("conversation recovery awaits context use storage")
		}
		return s.cutUses.DeleteContextUses(context.Background(), "memory", j.WithdrawnIDs...)
	}
	return nil
}

func openCutDatabase(path string) (*sql.DB, error) {
	uri := activityDatabaseURI(path)
	uri.RawQuery = "mode=rw&_pragma=busy_timeout(5000)"
	return sql.Open("sqlite", uri.String())
}

func (a *App) commitForgetFiles(store *mobileRecordingStore, f *watchCutFiles, tx *sql.Tx, index *sql.DB, interrupted func() bool) (result error) {
	if tx != nil {
		if _, err := tx.Exec("INSERT INTO watch_conversation_cut_receipts VALUES (?)", f.Token); err != nil {
			return err
		}
	} else {
		f.FileReceipt = true
	}
	staging := f.stage
	marker, err := json.Marshal(f.watchCutJournal)
	if err != nil {
		return err
	}
	if err := mobileRecordingWriteFile(filepath.Join(staging, "journal.json"), marker, 0600); err != nil {
		return err
	}
	if err := syncWatchCutDir(staging); err != nil {
		return err
	}
	f.stage = filepath.Join(store.dir, watchForgetJournal)
	if err := os.Rename(staging, f.stage); err != nil {
		return err
	}
	store.loaded = false
	memoryCompleted := false
	defer func() {
		if tx != nil {
			tx.Rollback()
		}
		if interrupted != nil && errors.Is(result, errWatchCutInterrupted) {
			return
		}
		result = errors.Join(result, store.resolveWatchCut(memoryCompleted))
	}()
	if err := syncWatchCutDir(store.dir); err != nil {
		return err
	}
	if err := f.install(); err != nil {
		return err
	}
	secondary, err := a.prepareMemoryDatabases(f, index)
	if err != nil {
		return err
	}
	defer secondary.rollback()
	if interrupted != nil && interrupted() {
		return errWatchCutInterrupted
	}
	if tx != nil {
		if err := tx.Commit(); err != nil {
			return err
		}
	} else {
		if err := mobileRecordingWriteFile(filepath.Join(f.stage, "committed"), []byte("1"), 0600); err != nil {
			return err
		}
		if err := syncWatchCutDir(f.stage); err != nil {
			return err
		}
	}
	if interrupted != nil && interrupted() {
		return errWatchCutInterrupted
	}
	if err := secondary.commit(); err != nil {
		return err
	}
	memoryCompleted = true
	return nil
}

// Retain the journal copy until retirement so recovery itself can restart.
func installCutLink(stage, source, dest string) error {
	link := filepath.Join(stage, "install")
	if err := os.Remove(link); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.Link(filepath.Join(stage, source), link); err != nil {
		return err
	}
	if err := os.Rename(link, dest); err != nil {
		return err
	}
	return syncWatchCutDir(filepath.Dir(dest))
}
