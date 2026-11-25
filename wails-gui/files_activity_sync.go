package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"wails-gui/internal/pii"
	"wails-gui/internal/platform/activitymon"
	"wails-gui/internal/platform/recentfiles"
)

// The recent files reader asks Spotlight, every fifteen minutes and on wake,
// which documents the user opened or edited lately in Documents, Desktop,
// Downloads and iCloud Drive, and keeps them in file_activity: a hash of the
// path, the scrubbed name, the folder, the kind, when it was last opened and
// edited, and the app when the window in front named it. No file is ever
// opened or listed and no content is read; only Spotlight's metadata is.
//
// What is never kept: hidden files, caches, node_modules, Library and the
// insides of packages; files whose name says they hold passwords, keys or a
// wallet; files under the folders the user keeps private; files used with
// an app the user protects, in a session that touched one, or in a private
// browser window; and anything done while observing was paused.
//
// Like the browser history it never reaches back before reading was last
// allowed: the first run, every switch back on and every launch start from
// that moment. Turning the switch off deletes what was kept. macOS asks
// before an app reads Documents, Desktop or Downloads; Isoform never causes
// that dialog. Spotlight is believed to leave out the folders macOS has not
// opened to Isoform (those show as not available), but that is not yet
// confirmed on a real Mac, so the switch starts off until the user turns it
// on (permissionsOffByDefault).
const (
	filesPermissionKey = "files"
	filesReadInterval  = 15 * time.Minute
	filesFirstRun      = time.Minute
	// filesRetention is how long a document is kept after it was last
	// worked on.
	filesRetention = 14 * 24 * time.Hour
	// filesLookback bounds how far back one read asks Spotlight. Work is
	// stored when it happens; older work that did not count then never
	// counts later (the pause log forgets after a week).
	filesLookback = 2 * 24 * time.Hour
	// filesQueryLimit bounds one folder's results. It is high because the
	// skip rules apply after Spotlight answers: a checkout or an install in
	// Documents touches thousands of files at once.
	filesQueryLimit = 10000
	filesNameMax    = 200
	// filesAppWindow is how near to a document's open or edit the window
	// naming it must be to tell which app was used.
	filesAppWindow = 10 * time.Minute
	// filesStemMinRunes keeps a short name from matching unrelated windows.
	filesStemMinRunes = 3

	filesSinceKey          = "files_since"
	filesLastReadKey       = "files_last_read_at"
	filesPrivateFoldersKey = "files_private_folders"
)

var errFilesStopped = errors.New("recent files reading stopped during the read")

// filesFolderStatus is what the last read learned about one folder.
type filesFolderStatus struct {
	Key string
	// Exists is false when the folder is not on this Mac (no iCloud Drive).
	Exists bool
	// Available is true when Spotlight showed Isoform anything in it.
	Available bool
}

// filesActivityReader owns the read loop.
type filesActivityReader struct {
	home  string
	roots []recentfiles.Root
	now   func() time.Time
	store func() *ActivityStore
	// enabled is the files switch; paused is observing paused.
	enabled captureSwitch
	paused  func() bool
	pauses  func() ([]observationPause, error)
	// sensitive reports an app the user protects (or a password manager).
	sensitive      func(bundleID, name string) bool
	privateFolders func() []string
	// since and setSince keep the filesSinceKey marker; lastRead and
	// setLastRead the filesLastReadKey time.
	since          func() (int64, bool)
	setSince       func(int64)
	setLastRead    func(time.Time)
	unknownThrough int64 // last tick with an unreadable switch
	started        time.Time
	query          func(text, scope string, limit int) ([]recentfiles.Item, error)
	exists         func(path string) bool
	// onRead runs after a read that stored something.
	onRead func()

	// readMu keeps one read or clear at a time.
	readMu sync.Mutex

	statusMu sync.Mutex
	lastRead time.Time
	lastErr  string
	folders  []filesFolderStatus

	kicks  chan string
	mu     sync.Mutex
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// fileSensitiveApp reports whether documents used with this app are never
// kept: a password manager or an app the user protects.
func fileSensitiveApp(bundleID, name string) bool {
	if bundleID == "" && name == "" {
		return false
	}
	if _, sensitive := pii.IsSensitiveApp(bundleID); sensitive {
		return true
	}
	if pii.IsSensitiveAppName(name) {
		return true
	}
	lower := strings.ToLower(name)
	return strings.Contains(lower, "password") || strings.Contains(lower, "keychain")
}

func newFilesActivityReader(a *App) *filesActivityReader {
	home, _ := os.UserHomeDir()
	r := &filesActivityReader{
		home:  home,
		roots: recentfiles.Roots(home),
		now:   time.Now,
		store: func() *ActivityStore {
			if a.activity == nil {
				return nil
			}
			return a.activity.store
		},
		enabled: func() (bool, bool) {
			return a.captureState(context.Background(), "files_activity_sync", filesPermissionKey)
		},
		paused:    a.IsClosedEyesModeEnabled,
		pauses:    a.observationPauses,
		sensitive: fileSensitiveApp,
		privateFolders: func() []string {
			return a.filesPrivateFolders()
		},
		since: func() (int64, bool) {
			if a.store == nil {
				return 0, false
			}
			raw, err := a.store.GetUIState(context.Background(), filesSinceKey)
			if err != nil || raw == "" {
				return 0, false
			}
			ms, err := strconv.ParseInt(raw, 10, 64)
			return ms, err == nil
		},
		setSince: func(ms int64) {
			if a.store == nil {
				return
			}
			if err := a.store.SetUIState(context.Background(), filesSinceKey, strconv.FormatInt(ms, 10)); err != nil {
				log.Printf("[files] save reading start: %v", err)
			}
		},
		setLastRead: func(at time.Time) {
			if a.store == nil {
				return
			}
			if err := a.store.SetUIState(context.Background(), filesLastReadKey, at.UTC().Format(time.RFC3339)); err != nil {
				log.Printf("[files] record last read: %v", err)
			}
		},
		started: time.Now(),
		query:   recentfiles.Query,
		exists: func(path string) bool {
			// Lstat reads the folder's own entry in its parent, never what
			// is inside it, so it causes no privacy dialog.
			info, err := os.Lstat(path)
			return err == nil && info.IsDir()
		},
		onRead: func() { a.emitBrowserEvent("files:read") },
		kicks:  make(chan string, 4),
	}
	return r
}

// startFilesActivityReader starts the loop once from startup on platforms
// with Spotlight.
func (a *App) startFilesActivityReader(ctx context.Context) {
	if a == nil || !recentfiles.Supported() {
		return
	}
	r := newFilesActivityReader(a)
	if len(r.roots) == 0 {
		return
	}
	runCtx, cancel := context.WithCancel(ctx)
	r.mu.Lock()
	r.cancel = cancel
	r.mu.Unlock()
	a.filesActivity = r
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		r.loop(runCtx)
	}()
}

// stopFilesActivityReader ends the loop at shutdown.
func (a *App) stopFilesActivityReader() {
	if a == nil || a.filesActivity == nil {
		return
	}
	r := a.filesActivity
	r.mu.Lock()
	cancel := r.cancel
	r.cancel = nil
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	r.wg.Wait()
}

// RequestSync asks for a read soon. It never blocks.
func (r *filesActivityReader) RequestSync(reason string) {
	if r == nil {
		return
	}
	select {
	case r.kicks <- reason:
	default:
	}
}

// filesSwitchChanged applies the Settings switch: on, reading starts from
// this moment; off, saved activity stays while future collection stops.
func (a *App) filesSwitchChanged(enabled bool) {
	if a == nil || a.filesActivity == nil {
		return
	}
	if enabled {
		a.filesActivity.setSince(time.Now().UnixMilli())
		a.filesActivity.RequestSync("switched on")
		return
	}
}

// filesProtectionChanged runs after an app is protected on the Privacy
// page: the next pass deletes the documents used with it.
func (a *App) filesProtectionChanged() {
	if a == nil || a.filesActivity == nil {
		return
	}
	a.filesActivity.RequestSync("protected apps changed")
}

// filesProtectionLifted runs just before an app stops being protected: the
// documents are read up to now under the old rules, then reading starts
// again from this moment, so nothing used with the app while it was
// protected is ever kept.
func (a *App) filesProtectionLifted() {
	if a == nil || a.filesActivity == nil {
		return
	}
	if _, err := a.filesActivity.readOnce(context.Background()); err != nil && !errors.Is(err, errFilesStopped) {
		log.Printf("[files] read before an app stops being protected: %v", err)
	}
	a.filesActivity.setSince(time.Now().UnixMilli())
}

func (r *filesActivityReader) loop(ctx context.Context) {
	timer := time.NewTimer(filesFirstRun)
	defer timer.Stop()
	for {
		reason := "interval"
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		case reason = <-r.kicks:
		}
		if added, err := r.readOnce(ctx); err != nil && ctx.Err() == nil && !errors.Is(err, errFilesStopped) {
			log.Printf("[files] read (%s): %v", reason, err)
		} else if added > 0 {
			log.Printf("[files] read (%s): %d documents new or worked on again", reason, added)
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(filesReadInterval)
	}
}

// readingAllowed is the switch on and observing not paused.
func (r *filesActivityReader) readingAllowed() bool {
	return r.enabled.on() && (r.paused == nil || !r.paused())
}

// readOnce runs one pass: with the switch off it keeps saved activity;
// while paused it reads nothing; otherwise it asks Spotlight about each
// folder and stores the documents worked on. It returns how many documents
// were new or worked on again.
func (r *filesActivityReader) readOnce(ctx context.Context) (int, error) {
	r.readMu.Lock()
	defer r.readMu.Unlock()
	store := r.store()
	if store == nil {
		return 0, nil
	}
	now := r.now()
	on, known := r.enabled()
	if !known {
		r.unknownThrough = now.UnixMilli()
		return 0, nil
	}
	if !on {
		// Reading resumes from the moment it is switched on again.
		r.setSince(now.UnixMilli())
		// Privacy exclusions still apply to retained observations.
		return 0, r.forgetHidden(store, r.privateFolders())
	}
	if r.paused != nil && r.paused() {
		// The pause log keeps this time out of later reads.
		return 0, nil
	}
	floor, ok := r.since()
	if !ok {
		// First run: start now, never reach back.
		floor = now.UnixMilli()
		r.setSince(floor)
	}
	// Keep the outage boundary in memory while preference writes may also fail.
	if r.unknownThrough > floor {
		floor = r.unknownThrough
	}
	// Nothing from before this launch: Isoform was not running then.
	if started := r.started.UnixMilli(); !r.started.IsZero() && started > floor {
		floor = started
	}
	start := time.UnixMilli(floor)
	if lookback := now.Add(-filesLookback); lookback.After(start) {
		start = lookback
	}

	private := r.privateFolders()
	var (
		items    []fileCandidate
		folders  []filesFolderStatus
		firstErr error
	)
	seen := map[string]bool{}
	for _, root := range r.roots {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		status := filesFolderStatus{Key: root.Key, Exists: r.exists(root.Path)}
		if !status.Exists {
			folders = append(folders, status)
			continue
		}
		found, err := r.query(recentfiles.RecentQuery(start), root.Path, filesQueryLimit)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			folders = append(folders, status)
			continue
		}
		if len(found) >= filesQueryLimit {
			log.Printf("[files] %s: Spotlight returned its limit of %d files; documents beyond it are not read", root.Key, filesQueryLimit)
		}
		status.Available = len(found) > 0
		if !status.Available {
			probe, err := r.query(recentfiles.ProbeQuery, root.Path, 1)
			status.Available = err == nil && len(probe) > 0
		}
		folders = append(folders, status)
		for _, item := range found {
			candidate, ok := fileCandidateFor(item, r.home, r.roots, private)
			if !ok || seen[candidate.ID] {
				continue
			}
			seen[candidate.ID] = true
			items = append(items, candidate)
		}
	}
	// Read the pause log after Spotlight answered, so a pause that began
	// meanwhile still counts; a log that cannot be read stores nothing.
	pauses, err := r.pauses()
	if err != nil {
		r.noteRead(now, folders, err)
		return 0, err
	}
	rows, drop := r.fileRows(store, items, start, pauses)

	if !r.readingAllowed() {
		return 0, errFilesStopped
	}
	added, err := store.UpsertFileActivity(rows, now.UnixMilli())
	if err != nil && firstErr == nil {
		firstErr = err
	}
	if len(drop) > 0 {
		if _, err := store.DeleteFileActivityIDs(drop); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	// The private folders again: one added during this read counts too.
	if err := r.forgetHidden(store, r.privateFolders()); err != nil && firstErr == nil {
		firstErr = err
	}
	if _, err := store.PruneFileActivity(now.Add(-filesRetention).UnixMilli()); err != nil && firstErr == nil {
		firstErr = err
	}
	r.noteRead(now, folders, firstErr)
	if r.setLastRead != nil {
		r.setLastRead(now)
	}
	if r.onRead != nil {
		r.onRead()
	}
	return added, firstErr
}

// forgetHidden deletes what is now hidden from Isoform: documents under the
// private folders, documents used with an app the user protects (by bundle
// id or name), and documents worked on during a session that touched a
// protected app, which may have been saved only after the document was
// read.
func (r *filesActivityReader) forgetHidden(store *ActivityStore, private []string) error {
	var firstErr error
	for _, folder := range private {
		if _, err := store.DeleteFileActivityUnder(recentfiles.FolderHash(recentfiles.Canonical(folder, r.home))); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if _, err := store.DeleteFileActivityInProtectedSessions(); err != nil && firstErr == nil {
		firstErr = err
	}
	apps, err := store.FileActivityApps()
	if err != nil {
		return err
	}
	for _, app := range apps {
		if r.sensitive != nil && r.sensitive(app.BundleID, app.Name) {
			if _, err := store.DeleteFileActivityOfApp(app.BundleID, app.Name); err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// locked runs fn while no read is running, so a change to what is hidden
// cannot race a read that already took the old rules.
func (r *filesActivityReader) locked(fn func() error) error {
	r.readMu.Lock()
	defer r.readMu.Unlock()
	return fn()
}

func (r *filesActivityReader) noteRead(at time.Time, folders []filesFolderStatus, err error) {
	r.statusMu.Lock()
	defer r.statusMu.Unlock()
	r.lastRead = at
	r.folders = folders
	r.lastErr = ""
	if err != nil {
		r.lastErr = err.Error()
	}
}

// status reports the last read, its error and what it learned per folder.
func (r *filesActivityReader) status() (time.Time, string, []filesFolderStatus) {
	if r == nil {
		return time.Time{}, "", nil
	}
	r.statusMu.Lock()
	defer r.statusMu.Unlock()
	return r.lastRead, r.lastErr, append([]filesFolderStatus(nil), r.folders...)
}

// fileCandidate is one document Spotlight reported that passed the path
// rules, before its times are checked.
type fileCandidate struct {
	ID        string
	Name      string
	Stem      string
	Folder    string
	DirHashes string
	Kind      string
	Item      recentfiles.Item
}

// fileCandidateFor applies the path rules: inside one of the folders, not
// hidden or skipped, no secret in its name, not under a private folder. A
// path inside iCloud Drive's Desktop or Documents counts as the same path
// under the home folder.
func fileCandidateFor(item recentfiles.Item, home string, roots []recentfiles.Root, private []string) (fileCandidate, bool) {
	path := recentfiles.Canonical(item.Path, home)
	root, rel, ok := recentfiles.RootOf(roots, path)
	if !ok {
		return fileCandidate{}, false
	}
	if recentfiles.Skipped(rel, item.TypeTree) || recentfiles.SecretName(item.Name) {
		return fileCandidate{}, false
	}
	for _, folder := range private {
		if recentfiles.UnderFolder(path, recentfiles.Canonical(folder, home)) {
			return fileCandidate{}, false
		}
	}
	name := fileDisplayName(item.Name)
	if name == "" {
		return fileCandidate{}, false
	}
	return fileCandidate{
		ID:        recentfiles.PathHash(path),
		Name:      name,
		Stem:      strings.TrimSpace(strings.TrimSuffix(item.Name, filepath.Ext(item.Name))),
		Folder:    root.Key,
		DirHashes: recentfiles.AncestorHashes(path),
		Kind:      recentfiles.Kind(item.ContentType, item.TypeTree),
		Item:      item,
	}, true
}

// fileDisplayName redacts personal details and recognizable secrets, then
// bounds the name. Labeled codes accept filename separators; ordinary numeric
// document IDs stay intact.
func fileDisplayName(name string) string {
	name = strings.Join(strings.Fields(strings.ToValidUTF8(name, "")), " ")
	if name == "" {
		return ""
	}
	name, _ = pii.RedactText(name)
	if utf8.RuneCountInString(name) > filesNameMax {
		name = string([]rune(name)[:filesNameMax])
	}
	return name
}

// fileTimes is what of a document's open and edit counts: each one counts
// when it happened at or after start and not while observing was paused.
// The later of the two that count is the document's activity; an edit that
// counts names the action, since editing says more than opening.
func fileTimes(item recentfiles.Item, rootKey string, start time.Time, pauses []observationPause) (opened, edited time.Time, at time.Time, action string, ok bool) {
	counts := func(t time.Time) bool {
		return !t.IsZero() && !t.Before(start) && !pausedAt(pauses, t)
	}
	if editAt, editAction, has := recentfiles.Activity(recentfiles.Item{
		Modified: item.Modified, Created: item.Created, Added: item.Added,
	}, rootKey); has && editAction == recentfiles.ActionEdited && counts(editAt) {
		edited = editAt
	}
	if counts(item.LastUsed) {
		opened = item.LastUsed
	}
	switch {
	case !edited.IsZero():
		at := edited
		if opened.After(at) {
			at = opened
		}
		return opened, edited, at, recentfiles.ActionEdited, true
	case !opened.IsZero():
		return opened, edited, opened, recentfiles.ActionOpened, true
	default:
		return time.Time{}, time.Time{}, time.Time{}, "", false
	}
}

// fileRows turns candidates into rows. For a document new or worked on
// again it looks for the window that named it near that moment: a window
// of a protected app, of a session that touched one, or of a private
// browser window means the document is not kept at all (drop lists the ids
// to delete); any other window names the app.
func (r *filesActivityReader) fileRows(store *ActivityStore, candidates []fileCandidate, start time.Time, pauses []observationPause) ([]FileActivityRow, []string) {
	ids := make([]string, 0, len(candidates))
	for _, c := range candidates {
		ids = append(ids, c.ID)
	}
	stored, err := store.FileActiveAt(ids)
	if err != nil {
		log.Printf("[files] stored times: %v", err)
		stored = map[string]int64{}
	}
	var rows []FileActivityRow
	var drop []string
	for _, c := range candidates {
		opened, edited, at, action, ok := fileTimes(c.Item, c.Folder, start, pauses)
		if !ok {
			continue
		}
		row := FileActivityRow{
			ID: c.ID, Name: c.Name, Folder: c.Folder, DirHashes: c.DirHashes, Kind: c.Kind,
			ActiveAt: at.UnixMilli(), Action: action,
		}
		if !opened.IsZero() {
			row.LastUsedAt = opened.UnixMilli()
		}
		if !edited.IsZero() {
			row.ModifiedAt = edited.UnixMilli()
		}
		if prev, ok := stored[c.ID]; ok && prev >= row.ActiveAt {
			// Nothing new; keep the name and folders current.
			rows = append(rows, row)
			continue
		}
		// Worked on during a session that touched a protected app: not kept.
		if protected, err := store.ProtectedSessionAt(row.ActiveAt); err != nil || protected {
			if err != nil {
				log.Printf("[files] protected session lookup: %v", err)
			}
			drop = append(drop, c.ID)
			continue
		}
		if utf8.RuneCountInString(c.Stem) >= filesStemMinRunes {
			match, found, err := store.FileWindowNear(c.Stem, row.ActiveAt, filesAppWindow.Milliseconds())
			if err != nil {
				log.Printf("[files] window lookup: %v", err)
			}
			if found {
				if match.Protected || activitymon.IsPrivateBrowsing(match.WindowTitle) ||
					(r.sensitive != nil && r.sensitive(match.BundleID, match.AppName)) {
					drop = append(drop, c.ID)
					continue
				}
				row.AppBundleID, row.AppName = match.BundleID, match.AppName
			}
		}
		rows = append(rows, row)
	}
	return rows, drop
}

// filesPrivateFolders reads the folders the user keeps private.
func (a *App) filesPrivateFolders() []string {
	return a.filesPrivateFoldersContext(context.Background())
}

func (a *App) filesPrivateFoldersContext(ctx context.Context) []string {
	if a == nil || a.store == nil {
		return nil
	}
	raw, err := a.store.GetUIState(ctx, filesPrivateFoldersKey)
	if err != nil || raw == "" {
		return nil
	}
	var folders []string
	if json.Unmarshal([]byte(raw), &folders) != nil {
		return nil
	}
	return folders
}

func (a *App) saveFilesPrivateFolders(folders []string) error {
	if a == nil || a.store == nil {
		return errors.New("the store is not ready")
	}
	if folders == nil {
		folders = []string{}
	}
	raw, err := json.Marshal(folders)
	if err != nil {
		return err
	}
	return a.store.SetUIState(context.Background(), filesPrivateFoldersKey, string(raw))
}
