package main

import (
	"context"
	"io/fs"
	"path/filepath"
	"time"

	"wails-gui/internal/pii"
)

// YourDataSummary is Settings › Your data's numbers (redesign-shell v1.0.0
// rules 7 to 11): how long what Isoform gathers is kept, how many apps it
// never sees, and what left this computer this week. Every number comes from
// an existing store; nothing here reads personal content.
type YourDataSummary struct {
	// RetentionDays is how long recordings (screen and calls) are kept; 0
	// means until you delete them.
	RetentionDays int `json:"retentionDays"`
	// IgnoredApps is how many apps Isoform never sees.
	IgnoredApps int `json:"ignoredApps"`
	// HandoffsThisWeek is how many tasks went to your agents in the last 7 days.
	HandoffsThisWeek int `json:"handoffsThisWeek"`
	// PairedDevices is how many phones are paired through your relay.
	PairedDevices int `json:"pairedDevices"`
	// Answers is how many answers the ledger keeps (an Undo takes one back),
	// and AnswersSince is the first one's time (RFC 3339), or "".
	Answers      int    `json:"answers"`
	AnswersSince string `json:"answersSince,omitempty"`
	// ScreenBytes and AudioBytes are what the screen's pictures and recorded
	// audio take; UsedBytes is everything Isoform keeps for Activity, and
	// CapBytes the limit, past which the oldest goes first (computer-data
	// v1.0.0 rules 2 and 3).
	ScreenBytes int64 `json:"screenBytes"`
	AudioBytes  int64 `json:"audioBytes"`
	UsedBytes   int64 `json:"usedBytes"`
	CapBytes    int64 `json:"capBytes"`
	// MessagesNotes and MessagesBytes are the Messages notes kept (who asked
	// what, what you promised) and what their words take.
	MessagesNotes int   `json:"messagesNotes"`
	MessagesBytes int64 `json:"messagesBytes"`
	// WatchBytes is what the watch's recordings take on this computer.
	WatchBytes int64 `json:"watchBytes"`
	// AgentsHistoryItems and AgentsHistoryBytes are what was learned from your
	// agents' own history (beliefs and know-how) and the scan's saved view.
	AgentsHistoryItems int   `json:"agentsHistoryItems"`
	AgentsHistoryBytes int64 `json:"agentsHistoryBytes"`
}

const yourDataWeek = 7 * 24 * time.Hour

// GetYourDataSummary reads Settings › Your data's numbers. A source that
// can't be read counts as zero rather than failing the page.
func (a *App) GetYourDataSummary() (perfValue YourDataSummary, perfErr error) {
	defer logPageBinding("GetYourDataSummary", time.Now(), &perfValue)
	out := YourDataSummary{RetentionDays: a.GetActivityRetentionDays(), IgnoredApps: len(pii.GetAllSensitiveApps())}
	if a.handoffStore != nil {
		if entries, err := a.handoffStore.List(); err == nil {
			since := time.Now().Add(-yourDataWeek).UnixMilli()
			for _, entry := range entries {
				if entry.CreatedAt >= since {
					out.HandoffsThisWeek++
				}
			}
		}
	}
	entries := a.answersLedger().entries()
	undone := answersUndone(entries)
	for _, record := range entries {
		if record.Action == workActionUndo || undone[record.EntryID] {
			continue
		}
		out.Answers++
		if out.AnswersSince == "" || record.At < out.AnswersSince {
			out.AnswersSince = record.At
		}
	}
	if usage, err := a.GetActivityStorageUsage(); err == nil {
		out.ScreenBytes, out.AudioBytes, out.UsedBytes = usage.ScreenshotBytes, usage.AudioBytes, usage.TotalBytes
	}
	out.CapBytes = int64(a.GetActivityMaxStorageGB()) << 30
	if status, err := a.GetMobileConnectionStatus(); err == nil {
		out.PairedDevices = len(status.Devices)
	}
	if a.store != nil {
		if count, bytes, err := a.store.MessageAsksSize(context.Background()); err == nil {
			out.MessagesNotes, out.MessagesBytes = count, bytes
		}
	}
	if dir := mobileStateDirectoryOrEmpty(); dir != "" {
		out.WatchBytes = yourDataDirBytes(filepath.Join(dir, "recordings"))
	}
	beliefs, knowHow := a.assistantHistoryCounts()
	out.AgentsHistoryItems = beliefs + knowHow
	if path := assistantHistoryCachePath(); path != "" {
		out.AgentsHistoryBytes = yourDataDirBytes(filepath.Dir(path))
	}
	return out, nil
}

// yourDataDirBytes is what the files under dir take; a missing folder is 0.
func yourDataDirBytes(dir string) int64 {
	var total int64
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}
