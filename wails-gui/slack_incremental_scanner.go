package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	incrementalPollInterval  = cadenceMessageScan
	incrementalForceInterval = 1 * time.Hour
)

// channelScanState is persisted state for one channel.
type channelScanState struct {
	LastSeenTS      string    `json:"last_seen_ts"`
	LastExtractedAt time.Time `json:"last_extracted_at"`
	LastScannedTS   string    `json:"last_scanned_ts"`
	FirstDirtyAt    time.Time `json:"first_dirty_at,omitempty"`
}

// incrementalScanState is the full persisted state file.
type incrementalScanState struct {
	Channels         map[string]channelScanState `json:"channels"`
	FirstRunNotified bool                        `json:"first_run_notified,omitempty"`
	PendingRescan    bool                        `json:"pending_rescan,omitempty"`
}

// DynamicScanRecord is one history entry shown in the UI.
type DynamicScanRecord struct {
	ID                   string       `json:"id"`
	StartedAt            time.Time    `json:"started_at"`
	CompletedAt          *time.Time   `json:"completed_at"`
	Status               string       `json:"status"`           // "running" | "done" | "error"
	ScanType             string       `json:"scan_type"`        // "initial" | "incremental" | "rescan"
	Stage                string       `json:"stage,omitempty"`  // current stage while running (backward compat)
	Stages               []StageStats `json:"stages,omitempty"` // per-stage breakdown
	SelectedChannelNames []string     `json:"selected_channel_names,omitempty"`
	ChannelNames         []string     `json:"channel_names"`
	ConvCount            int          `json:"conv_count"`
	ConvsProcessed       int          `json:"convs_processed"`
	AutomationCount      int          `json:"automation_count"`
	Error                string       `json:"error,omitempty"`
}

// SlackIncrementalScanner polls Slack channels for new messages
// and triggers extraction when channels settle or become overdue.
type SlackIncrementalScanner struct {
	app       *App
	mu        sync.Mutex
	cancel    context.CancelFunc
	triggerCh chan struct{}

	// pollRW protects the entire poll() scope (including saveState + runExtraction).
	// poll() holds RLock; RescanSlack() holds Lock for exclusive access.
	pollRW sync.RWMutex

	// extractionCancel cancels the in-flight extraction's pipeline calls.
	// Protected by mu.
	extractionCancel context.CancelFunc

	historyMu sync.Mutex // protects history file reads/writes for concurrent stages
}

// NewSlackIncrementalScanner creates a scanner bound to the given App.
func NewSlackIncrementalScanner(app *App) *SlackIncrementalScanner {
	return &SlackIncrementalScanner{
		app:       app,
		triggerCh: make(chan struct{}, 1),
	}
}

// Trigger requests an immediate poll cycle. Non-blocking; coalesces multiple triggers.
func (s *SlackIncrementalScanner) Trigger() {
	if !furnaceScanEnabled {
		return
	}
	select {
	case s.triggerCh <- struct{}{}:
	default: // already pending
	}
}

// cleanupStaleRunning marks any "running" records left over from a previous
// app session as interrupted. Called once at scanner startup before the first
// poll so the UI never shows phantom running entries.
func (s *SlackIncrementalScanner) cleanupStaleRunning() {
	hist := s.loadHistory()
	filtered := hist[:0]
	for _, r := range hist {
		if r.Status != "running" {
			filtered = append(filtered, r)
		}
	}
	if len(filtered) != len(hist) {
		s.saveHistory(filtered)
		log.Printf("[slack-dynamic] removed %d stale running record(s)", len(hist)-len(filtered))
	}
}

// Run starts the poll loop. Blocks until ctx is cancelled or Stop() is called.
func (s *SlackIncrementalScanner) Run(ctx context.Context) {
	if !furnaceScanEnabled {
		return
	}
	ctx, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	s.cancel = cancel
	s.mu.Unlock()
	defer cancel()

	s.cleanupStaleRunning()

	log.Printf("[slack-dynamic] incremental scanner started (poll every %v)", incrementalPollInterval)

	// Wait for an explicit user-triggered rescan before the first poll.
	select {
	case <-s.triggerCh:
	case <-ctx.Done():
		return
	}
	s.poll(ctx)

	ticker := time.NewTicker(incrementalPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			s.poll(ctx)
		case <-s.triggerCh:
			s.poll(ctx)
		case <-ctx.Done():
			log.Printf("[slack-dynamic] incremental scanner stopped")
			return
		}
	}
}

// Stop cancels the scanner's context, causing Run to return.
func (s *SlackIncrementalScanner) Stop() {
	s.mu.Lock()
	if s.cancel != nil {
		s.cancel()
	}
	s.mu.Unlock()
}

// RescanSlack clears all scan state and triggers a fresh full scan.
// It cancels any in-flight extraction, waits for the current poll to finish,
// clears four stores, and triggers an immediate poll (which will behave as
// a first run for all channels).
func (a *App) RescanSlack() error {
	if !furnaceScanEnabled {
		return nil
	}
	s := a.slackScanner
	if s == nil {
		return fmt.Errorf("scanner not initialized")
	}

	// Step 1: Cancel in-flight extraction so poll() exits quickly.
	s.mu.Lock()
	if s.extractionCancel != nil {
		s.extractionCancel()
	}
	s.mu.Unlock()

	// Acquire exclusive write lock — blocks until any in-flight poll finishes.
	s.pollRW.Lock()

	// Step 2: Clear scan state, but mark PendingRescan so the next poll
	// knows to tag the scan as "rescan" rather than "initial".
	s.saveState(incrementalScanState{
		Channels:      make(map[string]channelScanState),
		PendingRescan: true,
	})

	// Step 3: Clear processed Slack conversations (per-source file).
	if mc := a.getMemoryGenerator(); mc != nil {
		mc.clearSlackConvs()
	}

	// Step 4: Clear known processed conversations from shared scan dir.
	scanDir := a.scanDir()
	_ = os.Remove(filepath.Join(scanDir, "known_processed.json"))
	_ = os.Remove(filepath.Join(scanDir, "known_automations.json"))
	_ = os.RemoveAll(filepath.Join(scanDir, "conversations"))

	// Step 5: Clear legacy config fields (upgrade compat).
	_ = updateConfig(func(c *yansuConfig) {
		c.SlackScanCompleted = false
		c.SlackScanTime = ""
	})

	s.pollRW.Unlock()

	// Step 6: Trigger immediate poll — will behave as first run (all channels
	// have no cursors, so 7-day lookback kicks in).
	s.Trigger()

	log.Printf("[slack-dynamic] RescanSlack: state cleared, triggered fresh scan")
	return nil
}

// ── State persistence ────────────────────────────────────────────────

// slackDynamicStateDir returns the directory used for incremental-scanner state.
// Extracted as a package-level helper so App methods can use it when the
// scanner instance is not yet available (e.g. early GetDynamicScanHistory call).
func slackDynamicStateDir() string {
	dataRoot, err := flavorDataDir()
	if err != nil {
		return filepath.Join(os.TempDir(), configDirName, "slack-dynamic-scan")
	}
	return filepath.Join(dataRoot, "slack-dynamic-scan")
}

func (s *SlackIncrementalScanner) stateDir() string {
	return slackDynamicStateDir()
}

func (s *SlackIncrementalScanner) loadState() incrementalScanState {
	path := filepath.Join(s.stateDir(), "state.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return incrementalScanState{Channels: make(map[string]channelScanState)}
	}
	var st incrementalScanState
	if err := json.Unmarshal(data, &st); err != nil {
		return incrementalScanState{Channels: make(map[string]channelScanState)}
	}
	if st.Channels == nil {
		st.Channels = make(map[string]channelScanState)
	}
	return st
}

func (s *SlackIncrementalScanner) saveState(st incrementalScanState) {
	dir := s.stateDir()
	_ = os.MkdirAll(dir, 0700)
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		log.Printf("[slack-dynamic] save state: %v", err)
		return
	}
	writeFileAtomic(filepath.Join(dir, "state.json"), data, "[slack-dynamic] save state")
}

func (s *SlackIncrementalScanner) loadHistory() []DynamicScanRecord {
	path := filepath.Join(s.stateDir(), "history.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var history []DynamicScanRecord
	if err := json.Unmarshal(data, &history); err != nil {
		return nil
	}
	return history
}

func (s *SlackIncrementalScanner) saveHistory(history []DynamicScanRecord) {
	dir := s.stateDir()
	_ = os.MkdirAll(dir, 0700)
	data, err := json.MarshalIndent(history, "", "  ")
	if err != nil {
		log.Printf("[slack-dynamic] save history: %v", err)
		return
	}
	writeFileAtomic(filepath.Join(dir, "history.json"), data, "[slack-dynamic] save history")
}

// emitStage transitions to a new stage in the running record and emits the history event.
func (s *SlackIncrementalScanner) emitStage(recordID, stage string) {
	s.historyMu.Lock()
	defer s.historyMu.Unlock()
	hist := s.loadHistory()
	for i := len(hist) - 1; i >= 0; i-- {
		if hist[i].ID == recordID {
			hist[i].Stage = stage
			hist[i].Stages = transitionStage(hist[i].Stages, stage)
			break
		}
	}
	s.saveHistory(hist)
	s.app.emitBrowserEvent("slack-dynamic-scan:history", hist)
}

// emitProgress updates the progress counter on the current running stage.
func (s *SlackIncrementalScanner) emitProgress(recordID string, current, total int) {
	s.historyMu.Lock()
	defer s.historyMu.Unlock()
	hist := s.loadHistory()
	for i := len(hist) - 1; i >= 0; i-- {
		if hist[i].ID == recordID {
			hist[i].Stages = updateStageProgress(hist[i].Stages, current, total)
			break
		}
	}
	s.saveHistory(hist)
	s.app.emitBrowserEvent("slack-dynamic-scan:history", hist)
}

// emitStageDetails sets the detail items on a named stage.
func (s *SlackIncrementalScanner) emitStageDetails(recordID, stageName string, details []StageDetailItem) {
	s.historyMu.Lock()
	defer s.historyMu.Unlock()
	hist := s.loadHistory()
	for i := len(hist) - 1; i >= 0; i-- {
		if hist[i].ID == recordID {
			hist[i].Stages = setStageDetails(hist[i].Stages, stageName, details)
			break
		}
	}
	s.saveHistory(hist)
	s.app.emitBrowserEvent("slack-dynamic-scan:history", hist)
}

// emitStageSummary sets the summary text on a named stage.
func (s *SlackIncrementalScanner) emitStageSummary(recordID, stageName, summary string) {
	s.historyMu.Lock()
	defer s.historyMu.Unlock()
	hist := s.loadHistory()
	for i := len(hist) - 1; i >= 0; i-- {
		if hist[i].ID == recordID {
			hist[i].Stages = setStageSummary(hist[i].Stages, stageName, summary)
			break
		}
	}
	s.saveHistory(hist)
	s.app.emitBrowserEvent("slack-dynamic-scan:history", hist)
}

func (s *SlackIncrementalScanner) appendHistory(record DynamicScanRecord) []DynamicScanRecord {
	s.historyMu.Lock()
	defer s.historyMu.Unlock()
	history := s.loadHistory()
	history = append(history, record)
	// Cap at 20 entries, keeping newest.
	if len(history) > 20 {
		history = history[len(history)-20:]
	}
	s.saveHistory(history)
	return history
}

// ── Poll ─────────────────────────────────────────────────────────────

func (s *SlackIncrementalScanner) poll(ctx context.Context) {
	s.pollRW.RLock()
	defer s.pollRW.RUnlock()

	if ctx.Err() != nil {
		return
	}

	// D6: if Slack is disconnected, skip silently.
	cfg := loadConfig()
	if cfg.Slack == nil {
		return
	}

	token, err := loadYansuToken()
	if err != nil || token == "" {
		return // not authenticated, skip silently
	}
	baseURL := loadAPIBaseURL()

	// D10: Upgrade migration — two independent actions:
	// 1. Remove old cold-start manifest.json if it exists.
	manifestPath := filepath.Join(s.app.scanDir(), "manifest.json")
	if _, err := os.Stat(manifestPath); err == nil {
		log.Printf("[slack-dynamic] upgrade: removing old manifest.json")
		_ = os.Remove(manifestPath)
	}
	// 2. Clear legacy config fields regardless of manifest existence
	//    (manifest may have been deleted manually but config still has stale fields).
	if cfg.SlackScanCompleted || cfg.SlackScanTime != "" {
		_ = updateConfig(func(c *yansuConfig) {
			c.SlackScanCompleted = false
			c.SlackScanTime = ""
		})
	}

	// Ensure state directory exists before fetchAllChannels tries to write channels.json.
	_ = os.MkdirAll(s.stateDir(), 0700)

	// Resolve channels using the same logic as cold-start.
	channels, err := s.app.fetchAllChannels(ctx, s.stateDir())
	if err != nil {
		log.Printf("[slack-dynamic] poll: fetch channels: %v", err)
		return
	}
	if len(channels) == 0 {
		return
	}

	selectedChannelNames := make([]string, 0, len(channels))
	for _, ch := range channels {
		selectedChannelNames = append(selectedChannelNames, ch.Name)
	}
	sort.Strings(selectedChannelNames)

	state := s.loadState()
	now := time.Now()

	// D9: Prune stale channels no longer in the fetched list.
	activeIDs := make(map[string]bool, len(channels))
	for _, ch := range channels {
		activeIDs[ch.ID] = true
	}
	for chID := range state.Channels {
		if !activeIDs[chID] {
			delete(state.Channels, chID)
		}
	}

	// channelID -> latest message ts seen this poll
	type dirtyInfo struct {
		latestTS    string
		channelName string
	}
	triggerChannels := make(map[string]dirtyInfo)

	for _, ch := range channels {
		if ctx.Err() != nil {
			return
		}

		cs := state.Channels[ch.ID]
		isFirstRun := cs.LastScannedTS == ""

		// Determine oldest for the poll query.
		oldest := cs.LastScannedTS
		if oldest == "" {
			// First run for this channel: look back 7 days.
			oldest = fmt.Sprintf("%d.000000", now.Add(-7*24*time.Hour).Unix())
		}

		// Fetch just the first page to check for new messages.
		requestPath := fmt.Sprintf("/api/slack/channels/%s/history?oldest=%s&skip_user_names=1",
			url.PathEscape(ch.ID), url.QueryEscape(oldest))

		var resp SlackScanHistoryResponse
		if err := apiGet(baseURL, token, requestPath, &resp); err != nil {
			log.Printf("[slack-dynamic] poll channel %s: %v", ch.ID, err)
			continue
		}

		if len(resp.Messages) == 0 {
			// No new messages — not dirty. Leave last_seen_ts unchanged.
			continue
		}

		// Find the newest message ts in this page.
		latestTS := resp.Messages[0].TS
		for _, msg := range resp.Messages[1:] {
			if msg.TS > latestTS {
				latestTS = msg.TS
			}
		}

		// Check dirty: latest ts differs from last scanned.
		dirty := latestTS != cs.LastScannedTS
		if !dirty {
			continue
		}

		// First-run channels skip debounce: trigger immediately.
		if isFirstRun && cs.LastExtractedAt.IsZero() && cs.LastSeenTS == "" {
			cs.LastSeenTS = latestTS
			state.Channels[ch.ID] = cs
			triggerChannels[ch.ID] = dirtyInfo{latestTS: latestTS, channelName: ch.Name}
			continue
		}

		// Track when this channel first appeared dirty, used as the
		// overdue baseline for channels that have never been extracted.
		if cs.FirstDirtyAt.IsZero() {
			cs.FirstDirtyAt = now
		}

		// Settling check: compare latestTS against what we observed on
		// the PREVIOUS poll (cs.LastSeenTS). If they match, no new
		// messages arrived since last poll — the channel has "settled."
		settled := cs.LastSeenTS != "" && latestTS == cs.LastSeenTS

		// Overdue: >1h since last extraction, or >1h since first
		// observed dirty for channels that have never been extracted.
		baseline := cs.LastExtractedAt
		if baseline.IsZero() {
			baseline = cs.FirstDirtyAt
		}
		overdue := !baseline.IsZero() && now.Sub(baseline) > incrementalForceInterval

		// Now update LastSeenTS for the next poll's comparison.
		cs.LastSeenTS = latestTS
		state.Channels[ch.ID] = cs

		if settled || overdue {
			triggerChannels[ch.ID] = dirtyInfo{latestTS: latestTS, channelName: ch.Name}
		}
	}

	s.saveState(state)

	if len(triggerChannels) == 0 {
		return
	}

	// Detect scan type.
	scanType := "incremental"
	if state.PendingRescan {
		scanType = "rescan"
		// Clear the flag now — subsequent polls are incremental.
		state.PendingRescan = false
		s.saveState(state)
	} else {
		allInitial := true
		for chID := range triggerChannels {
			if cs, ok := state.Channels[chID]; ok && !cs.LastExtractedAt.IsZero() {
				allInitial = false
				break
			}
		}
		if allInitial {
			scanType = "initial"
		}
	}

	// Build dirty map: channelID -> oldest, and ID→name lookup.
	dirtyMap := make(map[string]string, len(triggerChannels))
	idToName := make(map[string]string, len(triggerChannels))
	channelNames := make([]string, 0, len(triggerChannels))
	for chID, info := range triggerChannels {
		cs := state.Channels[chID]
		oldest := cs.LastScannedTS
		if oldest == "" {
			oldest = fmt.Sprintf("%d.000000", now.Add(-7*24*time.Hour).Unix())
		}
		dirtyMap[chID] = oldest
		channelNames = append(channelNames, info.channelName)
		idToName[chID] = info.channelName
	}
	sort.Strings(channelNames)

	s.runExtraction(ctx, dirtyMap, selectedChannelNames, channelNames, idToName, scanType)
}

// ── Extraction ───────────────────────────────────────────────────────

func (s *SlackIncrementalScanner) runExtraction(
	ctx context.Context,
	dirtyMap map[string]string, // channelID -> oldest ts
	selectedChannelNames []string,
	channelNames []string,
	idToName map[string]string, // channelID → human-readable name
	scanType string, // "initial" | "incremental" | "rescan"
) {
	// Try pipeline run mutex — skip if another extraction is active.
	if !s.app.slackPipelineRunMu.TryLock() {
		log.Printf("[slack-dynamic] extraction skipped: another Slack pipeline run is active")
		return
	}
	defer s.app.slackPipelineRunMu.Unlock()

	// Derive a child context so RescanSlack can cancel this extraction
	// without killing the scanner's main Run() loop.
	extractCtx, extractCancel := context.WithCancel(ctx)
	defer extractCancel()
	s.mu.Lock()
	s.extractionCancel = extractCancel
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.extractionCancel = nil; s.mu.Unlock() }()

	startedAt := time.Now()
	recordID := fmt.Sprintf("dyn-%d", startedAt.UnixMilli())

	record := DynamicScanRecord{
		ID:                   recordID,
		StartedAt:            startedAt,
		Status:               "running",
		ScanType:             scanType,
		SelectedChannelNames: selectedChannelNames,
		ChannelNames:         channelNames,
		Stages:               initStages([]string{"fetching", "segmenting", "triaging", "extracting", "analyzing", "automations"}),
	}
	history := s.appendHistory(record)
	s.app.emitBrowserEvent("slack-dynamic-scan:history", history)

	// Prepare work directory.
	workDir := filepath.Join(s.stateDir(), "work")
	_ = os.RemoveAll(workDir)
	for _, sub := range []string{"", "raw", "threads", "conversations", "triage", "analysis"} {
		_ = os.MkdirAll(filepath.Join(workDir, sub), 0700)
	}

	var extractionErr error
	var convCount, convsProcessed, automationCount int
	var automationNames []string
	// Track the latest consumed ts per channel for cursor advancement.
	consumedTS := make(map[string]string) // channelID -> latest msg ts consumed

	defer func() {
		completedAt := time.Now()

		// Only advance cursors on full success — a partial failure means
		// some channels' messages were never segmented/triaged/analyzed,
		// so we must NOT advance their cursors or those messages are lost.
		if extractionErr == nil {
			state := s.loadState()
			for chID, ts := range consumedTS {
				cs := state.Channels[chID]
				cs.LastScannedTS = ts
				cs.LastExtractedAt = completedAt
				cs.FirstDirtyAt = time.Time{} // reset; will be set again if new dirty detected
				state.Channels[chID] = cs
			}
			s.saveState(state)
		}

		// Read the latest history entry (which has accumulated stage
		// summaries/timestamps from emitStage/emitStageSummary calls)
		// and only patch the completion fields on top of it.
		hist := s.loadHistory()
		for i := len(hist) - 1; i >= 0; i-- {
			if hist[i].ID == recordID {
				hist[i].CompletedAt = &completedAt
				hist[i].ConvCount = convCount
				hist[i].ConvsProcessed = convsProcessed
				hist[i].AutomationCount = automationCount
				hist[i].Stage = "" // clear backward-compat "current stage" field
				hist[i].Stages = finishStages(hist[i].Stages)
				if extractionErr != nil {
					hist[i].Status = "error"
					hist[i].Error = extractionErr.Error()
				} else {
					hist[i].Status = "done"
				}
				break
			}
		}
		s.saveHistory(hist)
		s.app.emitBrowserEvent("slack-dynamic-scan:history", hist)

		if extractionErr != nil {
			log.Printf("[slack-dynamic] extraction failed: %v", extractionErr)
		} else {
			log.Printf("[slack-dynamic] extraction done: %d convs, %d processed",
				convCount, convsProcessed)
		}
	}()

	// Step 1: Fetch channel history + thread replies for each dirty channel.
	s.emitStage(recordID, "fetching")
	allThreadRoots := make(map[string][]string) // channelID -> thread root ts list
	var fetchDetails []StageDetailItem
	fetchIdx := 0
	for chID, oldest := range dirtyMap {
		fetchIdx++
		s.emitProgress(recordID, fetchIdx, len(dirtyMap))
		if extractCtx.Err() != nil {
			extractionErr = extractCtx.Err()
			return
		}

		msgCount, err := s.app.fetchChannelHistory(extractCtx, workDir, chID, oldest)
		if err != nil {
			log.Printf("[slack-dynamic] fetch history %s: %v", chID, err)
			extractionErr = fmt.Errorf("fetch history %s: %w", chID, err)
			return
		}
		log.Printf("[slack-dynamic] fetched %d messages for channel %s", msgCount, chID)

		if msgCount == 0 {
			continue
		}
		chLabel := chID
		if name, ok := idToName[chID]; ok && name != "" {
			chLabel = name
		}
		fetchDetails = append(fetchDetails, StageDetailItem{Label: chLabel, Value: fmt.Sprintf("%d messages", msgCount)})

		// Discover active thread roots from the fetched messages.
		threadRoots := discoverActiveThreadRoots(filepath.Join(workDir, "raw", chID+".jsonl"))
		if len(threadRoots) > 0 {
			allThreadRoots[chID] = threadRoots
			threadCount, err := s.app.fetchThreadReplies(extractCtx, workDir, chID, threadRoots)
			if err != nil {
				log.Printf("[slack-dynamic] fetch thread replies %s: %v", chID, err)
				// Non-fatal: continue without threads.
			} else {
				log.Printf("[slack-dynamic] fetched %d threads for channel %s", threadCount, chID)
			}
		}
		if slackConfig := loadConfig(); slackConfig.Slack != nil {
			s.app.observeSlackCommunicationMessages(workDir, chID, slackConfig.Slack.SlackUserID)
		}

		// Track the latest message ts consumed for this channel.
		latestTS := findLatestTS(filepath.Join(workDir, "raw", chID+".jsonl"))
		if latestTS != "" {
			consumedTS[chID] = latestTS
		}
	}

	s.emitStageDetails(recordID, "fetching", fetchDetails)
	s.emitStageSummary(recordID, "fetching", fmt.Sprintf("%d channels", len(dirtyMap)))

	// Step 2: Segment conversations (uses versioned file IDs for threads).
	s.emitStage(recordID, "segmenting")
	conversations := segmentConversationsVersioned(workDir)
	convCount = len(conversations)
	log.Printf("[slack-dynamic] segmented %d conversations", convCount)
	threadConvs, gapConvs := 0, 0
	for _, c := range conversations {
		switch c.Type {
		case "thread":
			threadConvs++
		default:
			gapConvs++
		}
	}
	segDetails := []StageDetailItem{
		{Label: "thread convs", Value: fmt.Sprintf("%d", threadConvs)},
		{Label: "time_gap convs", Value: fmt.Sprintf("%d", gapConvs)},
	}
	for _, c := range conversations {
		segDetails = append(segDetails, StageDetailItem{Label: c.Type, Value: c.ID})
	}
	s.emitStageDetails(recordID, "segmenting", segDetails)
	s.emitStageSummary(recordID, "segmenting", fmt.Sprintf("%d conversations", convCount))

	if convCount == 0 {
		// Successful run with no output is still valid.
		return
	}

	// Step 3: Copy new conversation files to shared conversations dir.
	sharedConvDir := filepath.Join(s.app.scanDir(), "conversations")
	_ = os.MkdirAll(sharedConvDir, 0700)
	workConvDir := filepath.Join(workDir, "conversations")
	entries, err := os.ReadDir(workConvDir)
	if err == nil {
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			src := filepath.Join(workConvDir, e.Name())
			dst := filepath.Join(sharedConvDir, e.Name())
			data, err := os.ReadFile(src)
			if err != nil {
				continue
			}
			_ = os.WriteFile(dst, data, 0600)
		}
	}

	// Step 4: Triage conversations.
	// Pass s.app.scanDir() as processedDir so already-processed convs are correctly
	// skipped — known_processed.json lives in scanDir, not the ephemeral workDir.
	s.emitStage(recordID, "triaging")
	triageResults, triageErrors, _, skippedLowMsg, skippedKnown := s.app.triageConversations(extractCtx, workDir, s.app.scanDir(), conversations)
	if triageErrors > 0 {
		log.Printf("[slack-dynamic] %d triage errors", triageErrors)
	}
	worthCount := 0
	var triageDetails []StageDetailItem
	for _, tr := range triageResults {
		if tr.Worth {
			worthCount++
		}
		status := "skip"
		if tr.Worth {
			status = "worth"
		}
		triageDetails = append(triageDetails, StageDetailItem{
			Label: fmt.Sprintf("[%s] %s", tr.ConvID, tr.AppIdea),
			Value: fmt.Sprintf("score:%d %s", tr.Score, status),
		})
	}
	if skippedLowMsg > 0 {
		triageDetails = append(triageDetails, StageDetailItem{Label: "skipped (<3 msgs)", Value: fmt.Sprintf("%d", skippedLowMsg)})
	}
	if skippedKnown > 0 {
		triageDetails = append(triageDetails, StageDetailItem{Label: "skipped (already processed)", Value: fmt.Sprintf("%d", skippedKnown)})
	}
	s.emitStageDetails(recordID, "triaging", triageDetails)
	s.emitStageSummary(recordID, "triaging", fmt.Sprintf("%d worth / %d total", worthCount, len(triageResults)))

	// Step 5: Memory extraction via evaluateSlackNow.
	s.emitStage(recordID, "extracting")
	// Snapshot memory dir before so we can count new files for the DM.
	memDirPath, _ := memoryDir()
	beforeMemFiles := listFileNames(memDirPath)
	var failedIDCount int

	mc := s.app.getMemoryGenerator()
	if mc == nil {
		log.Printf("[slack-dynamic] memory extraction skipped: generator not yet initialized")
	} else {
		var convIDs []string
		for _, c := range conversations {
			convIDs = append(convIDs, c.ID)
		}
		convIDs = append(convIDs, loadMemoryRetryIDs(slackDynamicStateDir())...)
		processed, failedIDs, memErr := mc.evaluateSlackNow(extractCtx, convIDs)
		if memErr != nil {
			log.Printf("[slack-dynamic] memory extraction: %v", memErr)
		}
		if memErr == nil {
			updateMemoryRetryQueue(slackDynamicStateDir(), failedIDs)
		}
		convsProcessed = processed
		failedIDCount = len(failedIDs)
	}

	afterMemFiles := listFileNames(memDirPath)
	newMemoryCount := countNewEntries(beforeMemFiles, afterMemFiles)
	extractDetails := []StageDetailItem{
		{Label: "processed", Value: fmt.Sprintf("%d", convsProcessed)},
		{Label: "new memories", Value: fmt.Sprintf("%d", newMemoryCount)},
		{Label: "failed", Value: fmt.Sprintf("%d", failedIDCount)},
	}
	retryIDs := loadMemoryRetryIDs(slackDynamicStateDir())
	if len(retryIDs) > 0 {
		extractDetails = append(extractDetails, StageDetailItem{Label: "retries queued", Value: fmt.Sprintf("%d", len(retryIDs))})
	}
	s.emitStageDetails(recordID, "extracting", extractDetails)
	s.emitStageSummary(recordID, "extracting", fmt.Sprintf("%d processed", convsProcessed))

	// Step 6: Analysis.
	s.emitStage(recordID, "analyzing")
	specs := s.app.analyzeTopConversations(extractCtx, workDir, triageResults)
	var analyzeDetails []StageDetailItem
	for _, spec := range specs {
		analyzeDetails = append(analyzeDetails, StageDetailItem{Label: spec.AppName, Value: spec.Description})
	}
	s.emitStageDetails(recordID, "analyzing", analyzeDetails)
	s.emitStageSummary(recordID, "analyzing", fmt.Sprintf("%d analyzed", len(specs)))

	// Step 7: Legacy message automation candidates stay quarantined until this
	// extractor emits the full immutable ShadowRun contract. Converting these
	// partial automation specs into Initiatives would expose unmeasured model
	// output as user-facing work.
	var automationsCreated int
	var automationCandidates int

	s.emitStage(recordID, "automations")
	knownAutomations := loadKnownAutomations(s.app.scanDir())
	for i := range triageResults {
		tr := &triageResults[i]
		if !tr.WorthAutomation {
			continue
		}
		automationCandidates++
		if knownAutomations[tr.ConvKey] {
			continue
		}
		log.Printf("[slack-dynamic] deferred legacy automation candidate %s until immutable shadow prediction support", tr.ConvKey)
	}
	saveKnownAutomations(s.app.scanDir(), knownAutomations)

	automationDetails := []StageDetailItem{
		{Label: "candidates", Value: fmt.Sprintf("%d", automationCandidates)},
		{Label: "shadowed", Value: fmt.Sprintf("%d", automationsCreated)},
		{Label: "deferred", Value: fmt.Sprintf("%d", automationCandidates-automationsCreated)},
	}
	for _, name := range automationNames {
		automationDetails = append(automationDetails, StageDetailItem{Label: "automation", Value: name})
	}
	s.emitStageDetails(recordID, "automations", automationDetails)
	s.emitStageSummary(recordID, "automations", formatAutomationSummary(automationsCreated, automationNames))
	automationCount = automationsCreated

	// Step 8: First-run / rescan DM notification.
	// D4: RescanSlack clears FirstRunNotified, so the DM fires again after rescan.
	// Skip DM when nothing was extracted (0 memories).
	if (scanType == "initial" || scanType == "rescan") && extractionErr == nil && newMemoryCount > 0 {
		state := s.loadState()
		if !state.FirstRunNotified {
			dmText := generateScanSummary(convCount, newMemoryCount, automationNames)
			cfg := loadConfig()
			if cfg.Slack != nil && cfg.Slack.SlackUserID != "" {
				if err := s.app.SendSlackMessage(cfg.Slack.SlackUserID, "", dmText); err != nil {
					log.Printf("[slack-dynamic] first-run DM failed (will retry next run): %v", err)
				} else {
					log.Printf("[slack-dynamic] first-run DM sent")
					state.FirstRunNotified = true
					s.saveState(state)
				}
			}
		}
	}
}

// generateScanSummary builds the DM text for the first-run notification.
func generateScanSummary(convCount, newMemoryCount int, automationNames []string) string {
	msg := "Based on analysis of your past 7 days of Slack messages:\n"
	msg += fmt.Sprintf("- %d conversations scanned\n", convCount)
	msg += fmt.Sprintf("- %d memory files extracted\n", newMemoryCount)
	if len(automationNames) > 0 {
		msg += fmt.Sprintf("- %d automations discovered: %s", len(automationNames), strings.Join(automationNames, ", "))
	}
	return strings.TrimRight(msg, "\n")
}

// listFileNames returns the set of filenames in a directory.
func listFileNames(dir string) map[string]bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	names := make(map[string]bool, len(entries))
	for _, e := range entries {
		names[e.Name()] = true
	}
	return names
}

// countNewEntries returns the number of entries in after that are not in before.
func countNewEntries(before, after map[string]bool) int {
	count := 0
	for name := range after {
		if !before[name] {
			count++
		}
	}
	return count
}

// ── Versioned segmentation ──────────────────────────────────────────

// segmentConversationsVersioned is like segmentConversations but uses
// versioned file IDs for thread conversations. The versioned ID includes
// the latest reply ts, so updated threads produce new snapshot files that
// memory_processed.json treats as new input.
func segmentConversationsVersioned(scanDir string) []Conversation {
	rawDir := filepath.Join(scanDir, "raw")
	entries, err := os.ReadDir(rawDir)
	if err != nil {
		log.Printf("[slack-dynamic] failed to read raw dir: %v", err)
		return nil
	}

	var allConvs []Conversation

	for _, entry := range entries {
		if matched, _ := filepath.Match("*.jsonl", entry.Name()); !matched {
			continue
		}
		channelID := entry.Name()[:len(entry.Name())-len(".jsonl")]

		threadConvs := segmentThreads(scanDir, channelID)
		allConvs = append(allConvs, threadConvs...)

		threadRootSet := make(map[string]bool)
		for _, tc := range threadConvs {
			threadRootSet[tc.ThreadTS] = true
		}

		channelMsgs := readJSONL(filepath.Join(rawDir, entry.Name()))
		var nonThreaded []SlackScanMessage
		for _, msg := range channelMsgs {
			if noiseSubtypesLocal[msg.SubType] {
				continue
			}
			if msg.ThreadTS != "" && msg.ThreadTS != msg.TS {
				continue
			}
			if msg.ThreadTS != "" && threadRootSet[msg.ThreadTS] {
				continue
			}
			nonThreaded = append(nonThreaded, msg)
		}

		gapConvs := segmentByTimeGap(channelID, nonThreaded)
		allConvs = append(allConvs, gapConvs...)
	}

	// Write each conversation to disk with versioned IDs for threads.
	convDir := filepath.Join(scanDir, "conversations")
	_ = os.MkdirAll(convDir, 0700)
	for i := range allConvs {
		conv := &allConvs[i]
		key := conversationKey(*conv)
		conv.ConvKey = key

		// Versioned file ID for threads: include latest reply ts.
		// Use max(TS) across all messages rather than the last element because
		// thread JSONL is not guaranteed to be in chronological order.
		var fid string
		if conv.Type == "thread" && len(conv.Messages) > 0 {
			latestReplyTS := conv.Messages[0].TS
			for _, m := range conv.Messages[1:] {
				if m.TS > latestReplyTS {
					latestReplyTS = m.TS
				}
			}
			fid = convFileIDVersioned(key, latestReplyTS)
		} else {
			fid = convFileID(key)
		}
		conv.ID = fid

		slim := SlimConversation{
			ID:           fid,
			Type:         conv.Type,
			ChannelID:    conv.ChannelID,
			Topic:        conv.Topic,
			Participants: conv.Participants,
			MessageCount: conv.MessageCount,
		}
		for _, m := range conv.Messages {
			slim.Messages = append(slim.Messages, SlimMessage{
				UserID:   m.User,
				UserName: m.UserName,
				Text:     m.Text,
			})
		}

		path := filepath.Join(convDir, fid+".json")
		data, err := json.MarshalIndent(slim, "", "  ")
		if err != nil {
			log.Printf("[slack-dynamic] marshal conversation %s: %v", fid, err)
			continue
		}
		_ = os.WriteFile(path, data, 0600)

		conv.Messages = nil
	}

	log.Printf("[slack-dynamic] segmented %d conversations (versioned)", len(allConvs))
	return allConvs
}

// convFileIDVersioned returns a file ID that includes a version suffix
// derived from the latest reply ts. This ensures updated threads get
// new file names for memory dedup purposes.
func convFileIDVersioned(key, latestReplyTS string) string {
	versionedKey := key + ":" + latestReplyTS
	h := sha256.Sum256([]byte(versionedKey))
	return fmt.Sprintf("conv_%x", h[:6])
}

// ── Helpers ──────────────────────────────────────────────────────────

// discoverActiveThreadRoots reads a raw JSONL file and returns unique
// thread_ts values, representing threads that received activity.
func discoverActiveThreadRoots(rawPath string) []string {
	msgs := readJSONL(rawPath)
	seen := make(map[string]bool)
	var roots []string
	for _, msg := range msgs {
		if msg.ThreadTS != "" && !seen[msg.ThreadTS] {
			seen[msg.ThreadTS] = true
			roots = append(roots, msg.ThreadTS)
		}
	}
	return roots
}

// findLatestTS reads a raw JSONL file and returns the latest message ts.
func findLatestTS(rawPath string) string {
	msgs := readJSONL(rawPath)
	var latest string
	for _, msg := range msgs {
		if msg.TS > latest {
			latest = msg.TS
		}
	}
	return latest
}

// GetDynamicScanHistory returns the persisted dynamic scan history for the UI.
// Falls back to reading history.json directly when the scanner has not yet
// been initialised (frontend can outrace startup()).
func (a *App) GetDynamicScanHistory() []DynamicScanRecord {
	if a.slackScanner != nil {
		return a.slackScanner.loadHistory()
	}
	// Scanner not ready yet — read the file directly so the UI still shows
	// persisted history on first mount.
	data, err := os.ReadFile(filepath.Join(slackDynamicStateDir(), "history.json"))
	if err != nil {
		return nil
	}
	var history []DynamicScanRecord
	if err := json.Unmarshal(data, &history); err != nil {
		return nil
	}
	return history
}

// writeFileAtomic writes data to path using a write-to-temp-then-rename pattern
// so a crash mid-write cannot leave a truncated file. logPrefix is used for
// error log messages.
func writeFileAtomic(path string, data []byte, logPrefix string) {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		log.Printf("%s: write tmp: %v", logPrefix, err)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		log.Printf("%s: rename: %v", logPrefix, err)
		_ = os.Remove(tmp)
	}
}
