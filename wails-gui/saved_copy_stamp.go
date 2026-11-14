package main

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"wails-gui/internal/pii"
)

// Saved copies (the finds view and the first chat page) are served only under
// the privacy state they were made under. The check has two parts:
//
//   - savedCopyStamp is read on every read. It compares only values already in
//     memory: the durable privacy epoch (#588, every Isoform action that
//     withdraws text advances it synchronously before it returns), Closed Eyes,
//     and the sensitive-app version. No disk, no database, no memory parse.
//   - savedCopyDeepStamp is the full check: the memory folder, the observation
//     pauses and the protected activity count. It reads the disk and the
//     database, so it runs only in the background. It records the copy's deep
//     state after the copy is saved, and a background pass drops the copy when
//     the deep state no longer matches.
//
// The one gap: a memory file changed outside Isoform is caught when the memory
// watcher fires (seconds later), or by the next background check, whichever
// comes first. A change made while the app is closed is caught by the first
// background check after launch.
func (a *App) savedCopyStamp(ctx context.Context) string {
	savedCopyEpochLoad.Do(loadSavedCopyEpoch)
	return fmt.Sprintf("read|%d|%t|%d", mobileBriefPrivacyEpoch.Load(), a.IsClosedEyesModeEnabled(), pii.UserSensitiveAppsVersion())
}

// savedCopyEpochLoad loads the durable privacy epoch once, before the first
// read compares against it. The epoch file lives beside the briefs.
var savedCopyEpochLoad sync.Once

func loadSavedCopyEpoch() {
	dir, err := mobileBriefStateDir()
	if err != nil || dir == "" {
		return
	}
	adoptMobileBriefPrivacyEpoch(filepath.Join(dir, mobileBriefDirName))
	// Copies made under an older epoch (a withdrawal saved while their chat was
	// closed) never hold again; they leave the disk now.
	dropStaleStreamPageCopies()
}

// savedCopyDeepStamp is the full check. It runs in the background only.
func (a *App) savedCopyDeepStamp(ctx context.Context) string {
	// A fresh folder signal, not the cached snapshot: the background check must see
	// a change made outside Isoform at once.
	dir, err := memoryDir()
	if err != nil {
		return ""
	}
	signal, _, err := memoryFolderSignal(dir)
	if err != nil {
		return ""
	}
	pauses, err := a.observationPausesContext(ctx)
	if err != nil && a.store != nil {
		return ""
	}
	activity, err := a.mobileBriefActivityPrivacyStamp(ctx, time.Now().UnixMilli())
	if err != nil {
		return ""
	}
	return mobileBriefModelDigest(struct {
		PrivacyEpoch  uint64
		ClosedEyes    bool
		SensitiveApps uint64
		Pauses        []observationPause
		Protected     int64
		Memory        string
	}{
		PrivacyEpoch:  mobileBriefPrivacyEpoch.Load(),
		ClosedEyes:    a.IsClosedEyesModeEnabled(),
		SensitiveApps: pii.UserSensitiveAppsVersion(),
		Pauses:        pauses,
		Protected:     activity.protected,
		Memory:        fmt.Sprintf("%s|%x", dir, signal),
	})
}
