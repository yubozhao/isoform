package main

import (
	"context"
	"io/fs"
	"path/filepath"
	"time"

	"wails-gui/internal/pii"
	"wails-gui/storage"
)

// YourDataSummary is Settings › Your data's numbers (redesign-shell v1.0.0
// rules 7 to 11): how long what Isoform gathers is kept, how many apps it
// never sees, and what left this computer this week. Every number comes from
// an existing store; nothing here reads personal content.
type YourDataSummary struct {
	FindingLeaves *FindingLeaves `json:"findingLeaves,omitempty"`
	// RetentionDays is how long recordings (screen and calls) are kept; 0
	// means until you delete them.
	RetentionDays int `json:"retentionDays"`
	// IgnoredApps is how many apps Isoform never sees.
	IgnoredApps int `json:"ignoredApps"`
	// HandoffsThisWeek is how many tasks went to your agents in the last 7 days,
	// and HandoffsByAgent the same count per agent ("claude-code", "codex", or
	// "" for the app's own assistant), for "this week {n}: {agent} {k}, …"
	// (computer-data v1.0.0 rule 5).
	HandoffsThisWeek int            `json:"handoffsThisWeek"`
	HandoffsByAgent  map[string]int `json:"handoffsByAgent"`
	// PairedDevices is how many phones are paired through your relay.
	PairedDevices int `json:"pairedDevices"`
	// AssistantBriefs and AssistantAnswers are this week's briefs and Chat
	// answers the assistant was given the person's data for, from the call
	// record See them lists (assistant_call_record.go): the assistant row's
	// "this week {n} briefs, {m} answers". AssistantProposals and
	// AssistantDay count the proposals it wrote and the stretches of the day
	// it read. All four are omitted (nil) when the record can't be read: a
	// failed read is an error the row shows, never a zero.
	AssistantBriefs    *int `json:"assistantBriefs,omitempty"`
	AssistantAnswers   *int `json:"assistantAnswers,omitempty"`
	AssistantProposals *int `json:"assistantProposals,omitempty"`
	AssistantDay       *int `json:"assistantDay,omitempty"`
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
	// CallsOn, MessagesOn and AgentsHistoryOn are whether each source's own
	// switch is on now, so a source that's off shows Turn on and never a line
	// that says more than its switch (computer-data v1.0.0 rule 2). Each is
	// nil when its read failed: the page shows the failure with Try again,
	// never a guessed on or off.
	CallsOn         *bool `json:"callsOn,omitempty"`
	MessagesOn      *bool `json:"messagesOn,omitempty"`
	AgentsHistoryOn *bool `json:"agentsHistoryOn,omitempty"`
}

const yourDataWeek = 7 * 24 * time.Hour

// GetYourDataSummary reads Settings › Your data's numbers. A source that
// can't be read counts as zero rather than failing the page.
func (a *App) GetYourDataSummary() (perfValue YourDataSummary, perfErr error) {
	defer logPageBinding("GetYourDataSummary", time.Now(), &perfValue)
	out := YourDataSummary{RetentionDays: a.GetActivityRetentionDays(), IgnoredApps: len(pii.GetAllSensitiveApps()), HandoffsByAgent: map[string]int{}}
	if a.handoffStore != nil {
		if entries, err := a.handoffStore.List(); err == nil {
			since := time.Now().Add(-yourDataWeek).UnixMilli()
			for _, entry := range entries {
				if entry.CreatedAt >= since {
					out.HandoffsThisWeek++
					out.HandoffsByAgent[entry.Backend]++
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
		if counts, err := a.store.CountAssistantCalls(context.Background(), time.Now().Add(-yourDataWeek).UnixMilli()); err == nil {
			count := func(purpose string) *int { n := counts[purpose]; return &n }
			out.AssistantBriefs = count(storage.AssistantCallBrief)
			out.AssistantAnswers = count(storage.AssistantCallAnswer)
			out.AssistantProposals = count(storage.AssistantCallProposal)
			out.AssistantDay = count(storage.AssistantCallDay)
		}
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
	calls := a.GetBackgroundListening()
	out.CallsOn = &calls
	// Messages reads only with its switch on and Full Disk Access given. An unreadable switch stays unknown.
	if on, err := a.messagesSwitchRead(context.Background()); err == nil {
		reading := on && systemMessagesHistorySupported() && messagesHistoryAccess()
		out.MessagesOn = &reading
	}
	// The legacy summary field aggregates the saved per-agent choices.
	if on, err := a.GetReadAssistantHistory(); err == nil {
		out.AgentsHistoryOn = &on
	}
	dir, err := flavorDataDir()
	if err != nil {
		return out, err
	}
	cfg, err := loadMemoryStoreConfig(dir)
	if err != nil {
		return out, err
	}
	out.FindingLeaves, err = cfg.findingLeaves()
	if err != nil {
		return out, err
	}
	if out.FindingLeaves != nil {
		a.mu.Lock()
		store := a.memoryStore
		a.mu.Unlock()
		if store != nil {
			out.FindingLeaves.countCalls(store.db)
		}
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
