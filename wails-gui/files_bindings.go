package main

import (
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"wails-gui/internal/platform/recentfiles"
)

// filesPrivateFoldersMax bounds the private folders list.
const filesPrivateFoldersMax = 50

// FilesFolderView is one folder in Settings > Observation: whether Spotlight
// shows it to Isoform and how many of its documents are kept.
type FilesFolderView struct {
	Key string `json:"key"` // documents | desktop | downloads | icloud
	// Checked is false until the first read looked at the folder.
	Checked   bool `json:"checked"`
	Available bool `json:"available"`
	Recent    int  `json:"recent"`
}

// FilesPrivateFolderView is one folder the user keeps private. ID is an
// opaque handle for RemoveFilesPrivateFolder; the full path is not sent.
type FilesPrivateFolderView struct {
	ID          string `json:"id"`
	DisplayPath string `json:"displayPath"`
}

// FilesOverview is what Settings > Observation shows about recent files.
type FilesOverview struct {
	Supported      bool                     `json:"supported"`
	Enabled        bool                     `json:"enabled"`
	LastReadAt     string                   `json:"lastReadAt"`
	LastError      string                   `json:"lastError"`
	TodayCount     int                      `json:"todayCount"`
	RecentCount    int                      `json:"recentCount"`
	LinkedCount    int                      `json:"linkedCount"`
	Folders        []FilesFolderView        `json:"folders"`
	PrivateFolders []FilesPrivateFolderView `json:"privateFolders"`
}

// GetFilesOverview reports the switch, the last read, the folders and the
// documents kept. It reads nothing from disk.
func (a *App) GetFilesOverview() (FilesOverview, error) {
	view := FilesOverview{
		Supported:      recentfiles.Supported(),
		Folders:        []FilesFolderView{},
		PrivateFolders: []FilesPrivateFolderView{},
	}
	if a == nil || a.store == nil {
		return view, errors.New("the store is not ready")
	}
	// A switch that can't be read fails the read; the page shows it with Try
	// again, never as on.
	enabled, err := a.permissionSwitchRead(context.Background(), filesPermissionKey)
	if err != nil {
		return view, err
	}
	view.Enabled = enabled
	home, _ := os.UserHomeDir()
	for _, folder := range a.filesPrivateFolders() {
		view.PrivateFolders = append(view.PrivateFolders, FilesPrivateFolderView{
			ID: recentfiles.FolderHash(folder), DisplayPath: filesDisplayPath(folder, home),
		})
	}
	if a.filesActivity == nil {
		return view, nil
	}
	last, lastErr, folders := a.filesActivity.status()
	if !last.IsZero() {
		view.LastReadAt = last.UTC().Format(time.RFC3339)
	} else if stored, err := a.store.GetUIState(context.Background(), filesLastReadKey); err == nil {
		view.LastReadAt = stored
	}
	view.LastError = lastErr

	var rows []FileActivityRow
	if a.activity != nil && a.activity.store != nil && view.Enabled {
		now := time.Now()
		var err error
		rows, err = a.activity.store.ListFileActivity(now.Add(-filesRetention).UnixMilli(), now.Add(time.Minute).UnixMilli())
		if err != nil {
			return view, err
		}
	}
	view.Folders = filesFolderViews(a.filesActivity.roots, folders, rows)
	view.TodayCount, view.RecentCount = filesCounts(rows, time.Now(), time.Local)
	if len(rows) > 0 {
		if store, err := a.purposeProductStore(); err == nil {
			if details, err := store.ListDetailsAll(); err == nil {
				view.LinkedCount = len(linkFilesToCommitments(rows, details))
			}
		}
	}
	return view, nil
}

// filesFolderViews lists the folders on this Mac in display order: the ones
// the last read found, with how many documents each keeps.
func filesFolderViews(roots []recentfiles.Root, statuses []filesFolderStatus, rows []FileActivityRow) []FilesFolderView {
	byKey := map[string]filesFolderStatus{}
	for _, status := range statuses {
		byKey[status.Key] = status
	}
	recent := map[string]int{}
	for _, row := range rows {
		recent[row.Folder]++
	}
	views := []FilesFolderView{}
	for _, root := range roots {
		status, checked := byKey[root.Key]
		if checked && !status.Exists {
			continue
		}
		views = append(views, FilesFolderView{
			Key:       root.Key,
			Checked:   checked,
			Available: status.Available,
			Recent:    recent[root.Key],
		})
	}
	return views
}

// filesCounts counts the documents worked on today and in all.
func filesCounts(rows []FileActivityRow, now time.Time, loc *time.Location) (int, int) {
	local := now.In(loc)
	dayStart := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc).UnixMilli()
	today := 0
	for _, row := range rows {
		if row.ActiveAt >= dayStart {
			today++
		}
	}
	return today, len(rows)
}

// filesDisplayPath shows a folder under the home folder as "~/...".
func filesDisplayPath(path, home string) string {
	if home != "" {
		if rel, err := filepath.Rel(home, path); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
			return filepath.Join("~", rel)
		}
	}
	return path
}

// SyncFilesNow reads the recent files right away. It is the Refresh button.
func (a *App) SyncFilesNow() error {
	if a == nil || a.filesActivity == nil {
		return errors.New("recent files are not available on this computer")
	}
	if !a.captureAllowed(context.Background(), "files_activity_sync", filesPermissionKey) {
		return errors.New("recent files reading is switched off")
	}
	if a.IsClosedEyesModeEnabled() {
		return errors.New("observing is paused")
	}
	_, err := a.filesActivity.readOnce(context.Background())
	return err
}

// AddFilesPrivateFolder asks the user for a folder to keep private, with the
// panel's title in the user's language, and forgets what was kept from it.
// It returns the folder, or "" when the user cancelled.
func (a *App) AddFilesPrivateFolder(title string) (string, error) {
	if a == nil || a.ctx == nil {
		return "", errors.New("the app is not ready")
	}
	dir, err := runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{
		Title:                title,
		CanCreateDirectories: false,
	})
	if err != nil || dir == "" {
		return "", err
	}
	return dir, a.addFilesPrivateFolder(dir)
}

// addFilesPrivateFolder records a private folder and deletes what was kept
// from inside it at once. It waits for a read in progress, so that read
// cannot store documents from the folder after they were deleted.
func (a *App) addFilesPrivateFolder(dir string) error {
	dir = filepath.Clean(strings.TrimSpace(dir))
	if dir == "" || !filepath.IsAbs(dir) {
		return errors.New("choose a folder on this computer")
	}
	add := func() error {
		folders := a.filesPrivateFolders()
		for _, folder := range folders {
			if strings.EqualFold(folder, dir) {
				return nil
			}
		}
		if len(folders) >= filesPrivateFoldersMax {
			return errors.New("too many private folders")
		}
		if err := a.saveFilesPrivateFolders(append(folders, dir)); err != nil {
			return err
		}
		if a.activity != nil && a.activity.store != nil {
			home, _ := os.UserHomeDir()
			if _, err := a.activity.store.DeleteFileActivityUnder(recentfiles.FolderHash(recentfiles.Canonical(dir, home))); err != nil {
				return err
			}
		}
		return nil
	}
	var err error
	if a.filesActivity != nil {
		err = a.filesActivity.locked(add)
	} else {
		err = add()
	}
	if err != nil {
		return err
	}
	a.emitBrowserEvent("files:read")
	return nil
}

// RemoveFilesPrivateFolder stops keeping a folder private, by the id
// GetFilesOverview gave it. Its documents are read again only from now on.
func (a *App) RemoveFilesPrivateFolder(id string) error {
	if a == nil {
		return errors.New("the app is not ready")
	}
	folders := a.filesPrivateFolders()
	kept := make([]string, 0, len(folders))
	for _, folder := range folders {
		if recentfiles.FolderHash(folder) != id {
			kept = append(kept, folder)
		}
	}
	if len(kept) == len(folders) {
		return nil
	}
	// A document worked on while its folder was private is never read:
	// the other folders are read up to now first, then reading starts again
	// from this moment.
	if a.filesActivity != nil {
		if _, err := a.filesActivity.readOnce(context.Background()); err != nil && !errors.Is(err, errFilesStopped) {
			log.Printf("[files] read before a folder stops being private: %v", err)
		}
		a.filesActivity.setSince(time.Now().UnixMilli())
	}
	if err := a.saveFilesPrivateFolders(kept); err != nil {
		return err
	}
	a.emitBrowserEvent("files:read")
	return nil
}
