package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// chatScanState is persisted state for one Feishu chat.
// All timestamps are Unix seconds (int64). Compare with > not string comparison.
type chatScanState struct {
	LastSeenTS      int64     `json:"last_seen_ts"`
	LastExtractedAt time.Time `json:"last_extracted_at"`
	LastScannedTS   int64     `json:"last_scanned_ts"`
	FirstDirtyAt    time.Time `json:"first_dirty_at,omitempty"`
}

// feishuIncrementalScanState is the full persisted state file.
type feishuIncrementalScanState struct {
	Chats            map[string]*chatScanState `json:"chats"`
	FirstRunNotified bool                      `json:"first_run_notified,omitempty"`
	PendingRescan    bool                      `json:"pending_rescan,omitempty"`
	BindingAppID     string                    `json:"binding_app_id,omitempty"`
	BindingOpenID    string                    `json:"binding_open_id,omitempty"`
}

// FeishuIncrementalScanner polls Feishu chats for new messages
// and triggers extraction when chats settle or become overdue.
type FeishuIncrementalScanner struct {
	app       *App
	mu        sync.Mutex
	cancel    context.CancelFunc
	triggerCh chan struct{}

	// pollRW protects the entire poll() scope (including saveState + runExtraction).
	// poll() holds RLock; RescanFeishu() holds Lock for exclusive access.
	pollRW sync.RWMutex

	// extractionCancel cancels the in-flight extraction pipeline.
	// Protected by mu.
	extractionCancel context.CancelFunc

	historyMu sync.Mutex // protects history file reads/writes for concurrent stages

	// bg tracks fire-and-forget goroutines (e.g. the first-run DM) so
	// Stop() can wait for them. yansuHTTPClient's 30s timeout bounds any
	// individual call, but without the WaitGroup the goroutine can still
	// run past shutdown.
	bg sync.WaitGroup
}

// Trigger requests an immediate poll cycle. Non-blocking; coalesces multiple triggers.
func (s *FeishuIncrementalScanner) Trigger() {
	if !furnaceScanEnabled {
		return
	}
	select {
	case s.triggerCh <- struct{}{}:
	default: // already pending
	}
}

// Stop cancels the scanner's context, causing Run to return, and waits
// for any background goroutines (e.g. the first-run DM) to finish.
func (s *FeishuIncrementalScanner) Stop() {
	s.mu.Lock()
	if s.cancel != nil {
		s.cancel()
	}
	s.mu.Unlock()
	s.bg.Wait()
}

// cleanupStaleRunning marks any "running" records left over from a previous
// app session as interrupted. Called once at scanner startup before the first poll.
func (s *FeishuIncrementalScanner) cleanupStaleRunning() {
	hist := s.loadHistory()
	filtered := hist[:0]
	for _, r := range hist {
		if r.Status != "running" {
			filtered = append(filtered, r)
		}
	}
	if len(filtered) != len(hist) {
		s.saveHistory(filtered)
		log.Printf("[feishu-dynamic] removed %d stale running record(s)", len(hist)-len(filtered))
	}
}

// Run starts the poll loop. Blocks until ctx is cancelled or Stop() is called.
func (s *FeishuIncrementalScanner) Run(ctx context.Context) {
	if !furnaceScanEnabled {
		return
	}
	ctx, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	s.cancel = cancel
	s.mu.Unlock()
	defer cancel()

	s.cleanupStaleRunning()

	log.Printf("[feishu-dynamic] incremental scanner started (poll every %v)", incrementalPollInterval)

	// Wait for an explicit trigger (from StartFeishuDaemon or
	// tryAutoStartFeishuDaemon) before the first poll.
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
			log.Printf("[feishu-dynamic] incremental scanner stopped")
			return
		}
	}
}

// ── State persistence ────────────────────────────────────────────────

func feishuDynamicStateDir() string {
	dataRoot, err := flavorDataDir()
	if err != nil {
		return filepath.Join(os.TempDir(), configDirName, "feishu-dynamic-scan")
	}
	return filepath.Join(dataRoot, "feishu-dynamic-scan")
}

func (s *FeishuIncrementalScanner) stateDir() string {
	return feishuDynamicStateDir()
}

func (s *FeishuIncrementalScanner) loadState() feishuIncrementalScanState {
	path := filepath.Join(s.stateDir(), "state.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return feishuIncrementalScanState{Chats: make(map[string]*chatScanState)}
	}
	var st feishuIncrementalScanState
	if err := json.Unmarshal(data, &st); err != nil {
		return feishuIncrementalScanState{Chats: make(map[string]*chatScanState)}
	}
	if st.Chats == nil {
		st.Chats = make(map[string]*chatScanState)
	}
	return st
}

func (s *FeishuIncrementalScanner) saveState(st feishuIncrementalScanState) {
	dir := s.stateDir()
	_ = os.MkdirAll(dir, 0700)
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		log.Printf("[feishu-dynamic] save state: %v", err)
		return
	}
	writeFileAtomic(filepath.Join(dir, "state.json"), data, "[feishu-dynamic] save state")
}

func (s *FeishuIncrementalScanner) loadHistory() []DynamicScanRecord {
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

func (s *FeishuIncrementalScanner) saveHistory(history []DynamicScanRecord) {
	dir := s.stateDir()
	_ = os.MkdirAll(dir, 0700)
	data, err := json.MarshalIndent(history, "", "  ")
	if err != nil {
		log.Printf("[feishu-dynamic] save history: %v", err)
		return
	}
	writeFileAtomic(filepath.Join(dir, "history.json"), data, "[feishu-dynamic] save history")
}

// emitStage transitions to a new stage in the running record and emits the history event.
func (s *FeishuIncrementalScanner) emitStage(recordID, stage string) {
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
	s.app.emitBrowserEvent("feishu-dynamic-scan:history", hist)
}

func (s *FeishuIncrementalScanner) emitProgress(recordID string, current, total int) {
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
	s.app.emitBrowserEvent("feishu-dynamic-scan:history", hist)
}

func (s *FeishuIncrementalScanner) emitStageDetails(recordID, stageName string, details []StageDetailItem) {
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
	s.app.emitBrowserEvent("feishu-dynamic-scan:history", hist)
}

func (s *FeishuIncrementalScanner) emitStageSummary(recordID, stageName, summary string) {
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
	s.app.emitBrowserEvent("feishu-dynamic-scan:history", hist)
}

func (s *FeishuIncrementalScanner) appendHistory(record DynamicScanRecord) []DynamicScanRecord {
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

func (s *FeishuIncrementalScanner) poll(ctx context.Context) {
	s.pollRW.RLock()
	defer s.pollRW.RUnlock()

	if ctx.Err() != nil {
		return
	}

	token, err := loadYansuToken()
	if err != nil || token == "" {
		return // not authenticated, skip silently
	}
	baseURL := loadAPIBaseURL()

	// Upgrade migration: remove old cold-start manifest.json and clear conv dir.
	dataRoot, homeErr := flavorDataDir()
	if homeErr == nil {
		feishuScanDir := filepath.Join(dataRoot, feishuScanDirName)
		manifestPath := filepath.Join(feishuScanDir, "manifest.json")
		if _, err := os.Stat(manifestPath); err == nil {
			log.Printf("[feishu-dynamic] upgrade: removing old manifest.json")
			_ = os.Remove(manifestPath)
		}
		// Always clear old non-versioned conversations so evaluateFeishuNow
		// only sees versioned output from the new scanner (D12).
		feishuScanConvDir := filepath.Join(feishuScanDir, "conversations")
		_ = os.RemoveAll(feishuScanConvDir)
		_ = os.MkdirAll(feishuScanConvDir, 0o700)
	}

	// Ensure state directory exists.
	_ = os.MkdirAll(s.stateDir(), 0700)

	// Binding reconciliation (D2/D3).
	binding, err := getActiveFeishuBinding(baseURL, token)
	if err != nil {
		log.Printf("[feishu-dynamic] poll: binding check failed: %v", err)
		return // network/server error — don't clear state
	}
	if binding == nil {
		// No active Feishu binding found. This is expected when Feishu has not been
		// configured, but can also indicate the Feishu app became inactive while the
		// daemon still shows as connected. Log at INFO so the skip is diagnosable.
		if cfg := loadConfig(); cfg.Feishu != nil && cfg.Feishu.AppID != "" {
			log.Printf("[feishu-dynamic] poll: no active binding (app_id=%s); scanning paused until binding is active again", cfg.Feishu.AppID)
		}
		return
	}

	state := s.loadState()

	// Detect binding change — clear all cursors if account switched.
	if state.BindingAppID != "" &&
		(state.BindingAppID != binding.AppID || state.BindingOpenID != binding.FeishuOpenID) {
		log.Printf("[feishu-dynamic] binding changed (%s→%s), clearing cursors",
			state.BindingAppID, binding.AppID)
		state.Chats = make(map[string]*chatScanState)
		state.FirstRunNotified = false
		// Also clear processed and automation dedup state — old account's data is not relevant.
		_ = os.Remove(filepath.Join(s.stateDir(), "known_processed.json"))
		_ = os.Remove(filepath.Join(s.stateDir(), "known_automations.json"))
	}
	state.BindingAppID = binding.AppID
	state.BindingOpenID = binding.FeishuOpenID

	// Fetch chat list.
	chats, err := s.app.fetchFeishuChats(ctx, baseURL, token, binding.AppID)
	if err != nil {
		log.Printf("[feishu-dynamic] poll: fetch chats: %v", err)
		return
	}
	if len(chats) == 0 {
		return
	}

	now := time.Now()

	// D9: Prune stale chats no longer in the fetched list.
	// Uses the full (unfiltered) chat list so that temporarily-unchecked
	// chats keep their cursors and resume from where they left off.
	activeIDs := make(map[string]bool, len(chats))
	for _, ch := range chats {
		activeIDs[ch.ChatID] = true
	}
	for chatID := range state.Chats {
		if !activeIDs[chatID] {
			delete(state.Chats, chatID)
		}
	}

	// Apply chat filter from config (after pruning to preserve cursors).
	// With an explicit filter: scan only those chat IDs.
	// Without a filter (nil/empty): scan all chats.
	if filter := loadConfig().FeishuScanChatFilter; len(filter) > 0 {
		allowed := make(map[string]bool, len(filter))
		for _, id := range filter {
			allowed[id] = true
		}
		filtered := make([]FeishuChatInfo, 0, len(filter))
		for _, ch := range chats {
			if allowed[ch.ChatID] {
				filtered = append(filtered, ch)
			}
		}
		chats = filtered
		if len(chats) == 0 {
			return
		}
	}

	// chatID -> info for dirty chats that should trigger extraction.
	type dirtyInfo struct {
		latestTS int64
		chatName string
	}
	triggerChats := make(map[string]dirtyInfo)

	// chatID -> chatName for all fetched chats.
	chatNames := make(map[string]string, len(chats))
	for _, ch := range chats {
		chatNames[ch.ChatID] = ch.Name
	}

	for _, ch := range chats {
		if ctx.Err() != nil {
			return
		}

		cs := state.Chats[ch.ChatID]
		if cs == nil {
			cs = &chatScanState{}
			state.Chats[ch.ChatID] = cs
		}
		isFirstRun := cs.LastScannedTS == 0

		// For dirty check: fetch one page of messages since cursor.
		startTime := cs.LastScannedTS
		if startTime == 0 {
			// First run: look back 7 days.
			startTime = now.Add(-7 * 24 * time.Hour).Unix()
		}

		// Fetch first page to find the latest create_time.
		requestPath := fmt.Sprintf("/api/feishu/chats/%s/messages?start_time=%d",
			ch.ChatID, startTime)

		var resp feishuChatMessagesResponse
		if err := apiGet(baseURL, token, requestPath, &resp); err != nil {
			log.Printf("[feishu-dynamic] poll chat %s: %v", ch.ChatID, err)
			continue
		}

		if len(resp.Messages) == 0 {
			// No messages since cursor — not dirty.
			continue
		}

		// Find the max create_time across this page (in seconds).
		var maxCreateTimeSeconds int64
		for _, item := range resp.Messages {
			ts := parseCreateTimeMs(item.CreateTime)
			if ts > maxCreateTimeSeconds {
				maxCreateTimeSeconds = ts
			}
		}

		// Dirty check: latest ts differs from last scanned cursor.
		dirty := maxCreateTimeSeconds != cs.LastScannedTS
		if !dirty {
			continue
		}

		// First-run channels skip debounce: trigger immediately.
		if isFirstRun && cs.LastExtractedAt.IsZero() && cs.LastSeenTS == 0 {
			cs.LastSeenTS = maxCreateTimeSeconds
			triggerChats[ch.ChatID] = dirtyInfo{latestTS: maxCreateTimeSeconds, chatName: ch.Name}
			continue
		}

		// Track when this chat first appeared dirty (overdue baseline).
		if cs.FirstDirtyAt.IsZero() {
			cs.FirstDirtyAt = now
		}

		// Settling: latestTS same as last poll's observation.
		settled := cs.LastSeenTS != 0 && maxCreateTimeSeconds == cs.LastSeenTS

		// Overdue: >1h since last extraction, or >1h since first dirty.
		baseline := cs.LastExtractedAt
		if baseline.IsZero() {
			baseline = cs.FirstDirtyAt
		}
		overdue := !baseline.IsZero() && now.Sub(baseline) > incrementalForceInterval

		// Update LastSeenTS for next poll's comparison.
		cs.LastSeenTS = maxCreateTimeSeconds

		if settled || overdue {
			triggerChats[ch.ChatID] = dirtyInfo{latestTS: maxCreateTimeSeconds, chatName: ch.Name}
		}
	}

	s.saveState(state)

	if len(triggerChats) == 0 {
		return
	}

	// Detect scan type.
	scanType := "incremental"
	if state.PendingRescan {
		scanType = "rescan"
		state.PendingRescan = false
		s.saveState(state)
	} else {
		allInitial := true
		for chatID := range triggerChats {
			if cs := state.Chats[chatID]; cs != nil && !cs.LastExtractedAt.IsZero() {
				allInitial = false
				break
			}
		}
		if allInitial {
			scanType = "initial"
		}
	}

	// Build dirty map: chatID -> startTime (cursor).
	dirtyMap := make(map[string]int64, len(triggerChats))
	var chatNameList []string
	for chatID, info := range triggerChats {
		cs := state.Chats[chatID]
		start := cs.LastScannedTS
		if start == 0 {
			start = time.Now().Add(-7 * 24 * time.Hour).Unix()
		}
		dirtyMap[chatID] = start
		chatNameList = append(chatNameList, info.chatName)
	}
	sort.Strings(chatNameList)

	s.runExtraction(ctx, dirtyMap, chatNames, chatNameList, scanType, binding.AppID, binding.FeishuOpenID, baseURL, token)
}

// ── Extraction ───────────────────────────────────────────────────────

func (s *FeishuIncrementalScanner) runExtraction(
	ctx context.Context,
	dirtyMap map[string]int64, // chatID -> startTime (Unix seconds)
	chatNames map[string]string, // chatID -> chatName for ALL chats
	chatNameList []string, // sorted list of dirty chat names for UI
	scanType string, // "initial" | "incremental" | "rescan"
	appID, openID string,
	baseURL, token string,
) {
	// Try pipeline run mutex — skip if another extraction is active.
	if !s.app.feishuPipelineRunMu.TryLock() {
		log.Printf("[feishu-dynamic] extraction skipped: another Feishu pipeline run is active")
		return
	}
	defer s.app.feishuPipelineRunMu.Unlock()

	// Derive a child context so RescanFeishu can cancel this extraction
	// without killing the scanner's main Run() loop.
	extractCtx, extractCancel := context.WithCancel(ctx)
	defer extractCancel()
	s.mu.Lock()
	s.extractionCancel = extractCancel
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.extractionCancel = nil; s.mu.Unlock() }()

	startedAt := time.Now()
	recordID := fmt.Sprintf("fdyn-%d", startedAt.UnixMilli())

	record := DynamicScanRecord{
		ID:           recordID,
		StartedAt:    startedAt,
		Status:       "running",
		ScanType:     scanType,
		ChannelNames: chatNameList,
		Stages:       initStages([]string{"fetching", "segmenting", "triaging", "extracting", "analyzing", "automations"}),
	}
	history := s.appendHistory(record)
	s.app.emitBrowserEvent("feishu-dynamic-scan:history", history)

	// Prepare work directory.
	workDir := filepath.Join(s.stateDir(), "work")
	_ = os.RemoveAll(workDir)
	workRawDir := filepath.Join(workDir, "raw")
	workConvDir := filepath.Join(workDir, "conversations")
	workTriageDir := filepath.Join(workDir, "triage")
	workAnalysisDir := filepath.Join(workDir, "analysis")
	for _, sub := range []string{workDir, workRawDir, workConvDir, workTriageDir, workAnalysisDir} {
		_ = os.MkdirAll(sub, 0700)
	}

	var extractionErr error
	var convCount, convsProcessed, automationCount int
	var newMemoryCount int
	var automationNames []string

	// Track the max consumed create_time per chat for cursor advancement.
	consumedTS := make(map[string]int64) // chatID -> maxCreateTimeSeconds consumed

	defer func() {
		completedAt := time.Now()

		// Advance cursors only on full success.
		if extractionErr == nil {
			state := s.loadState()
			for chatID, ts := range consumedTS {
				cs := state.Chats[chatID]
				if cs == nil {
					cs = &chatScanState{}
					state.Chats[chatID] = cs
				}
				cs.LastScannedTS = ts
				cs.LastExtractedAt = completedAt
				cs.FirstDirtyAt = time.Time{} // reset overdue baseline
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
		s.app.emitBrowserEvent("feishu-dynamic-scan:history", hist)

		if extractionErr != nil {
			log.Printf("[feishu-dynamic] extraction failed: %v", extractionErr)
		} else {
			log.Printf("[feishu-dynamic] extraction done: %d convs, %d processed",
				convCount, convsProcessed)
		}
	}()

	// Stage 1: Fetch messages for each dirty chat.
	s.emitStage(recordID, "fetching")
	var fetchDetails []StageDetailItem
	fetchIdx := 0
	for chatID, startTime := range dirtyMap {
		fetchIdx++
		s.emitProgress(recordID, fetchIdx, len(dirtyMap))
		if extractCtx.Err() != nil {
			extractionErr = extractCtx.Err()
			return
		}

		chatName := chatNames[chatID]
		msgCount, maxSeenTS, err := s.app.fetchFeishuChatMessages(
			extractCtx, baseURL, token, chatID, chatName, startTime, workRawDir, appID)
		if err != nil {
			log.Printf("[feishu-dynamic] fetch chat %s: %v", chatID, err)
			extractionErr = fmt.Errorf("fetch chat %s: %w", chatID, err)
			return
		}
		log.Printf("[feishu-dynamic] fetched %d messages for chat %s", msgCount, chatID)

		// Advance cursor using max seen timestamp from ALL messages (including
		// non-text). This ensures the cursor moves forward even when a batch
		// contains only images/files/system messages with no extractable text.
		if maxSeenTS > 0 {
			consumedTS[chatID] = maxSeenTS
		}

		if msgCount == 0 {
			continue
		}
		label := chatID
		if chatName != "" {
			label = chatName
		}
		fetchDetails = append(fetchDetails, StageDetailItem{Label: label, Value: fmt.Sprintf("%d messages", msgCount)})
	}

	s.emitStageDetails(recordID, "fetching", fetchDetails)
	s.emitStageSummary(recordID, "fetching", fmt.Sprintf("%d chats", len(dirtyMap)))

	// Stage 2: Segment conversations (versioned IDs).
	s.emitStage(recordID, "segmenting")
	written, err := segmentFeishuMessagesVersioned(workRawDir, workConvDir)
	if err != nil {
		extractionErr = fmt.Errorf("segment: %w", err)
		return
	}
	convCount = len(written)
	log.Printf("[feishu-dynamic] segmented %d conversations", convCount)
	segDetails := []StageDetailItem{
		{Label: "conversations", Value: fmt.Sprintf("%d", convCount)},
	}
	for _, convID := range written {
		segDetails = append(segDetails, StageDetailItem{Label: "conv", Value: convID})
	}
	s.emitStageDetails(recordID, "segmenting", segDetails)
	s.emitStageSummary(recordID, "segmenting", fmt.Sprintf("%d conversations", convCount))

	if convCount == 0 {
		return // successful run with no output is valid
	}

	// Stage 3: Dual-format conversion + copy to shared dir.
	// FeishuSlimConversation → sharedConvDir (memory pipeline, unchanged)
	// SlimConversation → workConvDir (triage/analyze pipeline)
	// Also build []Conversation for the pipeline.
	dataRoot, homeErr := flavorDataDir()
	if homeErr != nil {
		extractionErr = fmt.Errorf("UserHomeDir: %w", homeErr)
		return
	}
	feishuScanDir := filepath.Join(dataRoot, feishuScanDirName)
	sharedConvDir := filepath.Join(feishuScanDir, "conversations")
	_ = os.MkdirAll(sharedConvDir, 0700)

	var pipelineConvs []Conversation
	entries, err := os.ReadDir(workConvDir)
	if err == nil {
		for _, e := range entries {
			if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
				continue
			}
			src := filepath.Join(workConvDir, e.Name())
			rawData, err := os.ReadFile(src)
			if err != nil {
				continue
			}

			var conv FeishuSlimConversation
			if err := json.Unmarshal(rawData, &conv); err != nil {
				continue
			}

			// Copy raw FeishuSlimConversation to sharedConvDir for memory pipeline.
			_ = os.WriteFile(filepath.Join(sharedConvDir, e.Name()), rawData, 0600)

			convID := strings.TrimSuffix(e.Name(), ".json")

			// Determine conversation type.
			convType := "time_gap"
			for _, msg := range conv.Messages {
				if msg.RootID != "" {
					convType = "thread"
					break
				}
			}

			// Build stable dedup key (NOT the versioned file ID).
			// Thread: "thread:<chatID>:<rootID>" — rootID is stable across updates.
			// Time-gap: "gap:<chatID>:<startTime>" — startTime is stable min across messages.
			var stableKey string
			switch convType {
			case "thread":
				stableKey = fmt.Sprintf("thread:%s:%s", conv.ChatID, conv.ThreadID)
			default:
				stableKey = fmt.Sprintf("gap:%s:%d", conv.ChatID, conv.StartTime)
			}

			// Build SlimConversation for triage pipeline.
			participants := make(map[string]bool)
			var slimMsgs []SlimMessage
			for _, m := range conv.Messages {
				slimMsgs = append(slimMsgs, SlimMessage{
					UserID:   m.SenderID,
					UserName: m.SenderID,
					Text:     m.TextBody,
				})
				if m.SenderID != "" {
					participants[m.SenderID] = true
				}
			}
			partList := make([]string, 0, len(participants))
			for p := range participants {
				partList = append(partList, p)
			}
			sort.Strings(partList)

			slim := SlimConversation{
				ID:           convID,
				Type:         convType,
				ChannelID:    conv.ChatID,
				Topic:        "",
				Participants: partList,
				MessageCount: len(conv.Messages),
				Messages:     slimMsgs,
			}
			if slimData, err := json.MarshalIndent(slim, "", "  "); err == nil {
				_ = os.WriteFile(src, slimData, 0600)
			}

			pipelineConvs = append(pipelineConvs, Conversation{
				ID:           convID,
				ConvKey:      stableKey,
				Type:         convType,
				ChannelID:    conv.ChatID,
				MessageCount: len(conv.Messages),
			})
		}
	}

	// Stage 4: Triage.
	s.emitStage(recordID, "triaging")
	triageResults, triageErrors, _, skippedLowMsg, skippedKnown := s.app.triageConversations(extractCtx, workDir, s.stateDir(), pipelineConvs)
	if triageErrors > 0 {
		log.Printf("[feishu-dynamic] %d triage errors", triageErrors)
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

	// Stage 5: Memory extraction via evaluateFeishuNow.
	s.emitStage(recordID, "extracting")
	memDirPath, _ := memoryDir()
	beforeMemFiles := listFileNames(memDirPath)
	var failedIDCount int

	mc := s.app.getMemoryGenerator()
	if mc == nil {
		log.Printf("[feishu-dynamic] memory extraction skipped: generator not yet initialized")
	} else {
		var convIDs []string
		for _, c := range pipelineConvs {
			convIDs = append(convIDs, c.ID)
		}
		convIDs = append(convIDs, loadMemoryRetryIDs(s.stateDir())...)
		processed, failedIDs, memErr := mc.evaluateFeishuNow(extractCtx, feishuScanDir, convIDs)
		if memErr != nil {
			log.Printf("[feishu-dynamic] memory extraction: %v", memErr)
			// Non-fatal: log and continue so cursors can still advance.
		}
		if memErr == nil {
			updateMemoryRetryQueue(s.stateDir(), failedIDs)
		}
		convsProcessed = processed
		failedIDCount = len(failedIDs)
	}

	afterMemFiles := listFileNames(memDirPath)
	newMemoryCount = countNewEntries(beforeMemFiles, afterMemFiles)
	extractDetails := []StageDetailItem{
		{Label: "processed", Value: fmt.Sprintf("%d", convsProcessed)},
		{Label: "new memories", Value: fmt.Sprintf("%d", newMemoryCount)},
		{Label: "failed", Value: fmt.Sprintf("%d", failedIDCount)},
	}
	retryIDs := loadMemoryRetryIDs(s.stateDir())
	if len(retryIDs) > 0 {
		extractDetails = append(extractDetails, StageDetailItem{Label: "retries queued", Value: fmt.Sprintf("%d", len(retryIDs))})
	}
	s.emitStageDetails(recordID, "extracting", extractDetails)
	s.emitStageSummary(recordID, "extracting", fmt.Sprintf("%d processed", convsProcessed))

	// Stage 6: Analyze.
	s.emitStage(recordID, "analyzing")
	specs := s.app.analyzeTopConversations(extractCtx, workDir, triageResults)
	var analyzeDetails []StageDetailItem
	for _, spec := range specs {
		analyzeDetails = append(analyzeDetails, StageDetailItem{Label: spec.AppName, Value: spec.Description})
	}
	s.emitStageDetails(recordID, "analyzing", analyzeDetails)
	s.emitStageSummary(recordID, "analyzing", fmt.Sprintf("%d analyzed", len(specs)))

	// Stage 7: Legacy message automation candidates stay quarantined until this
	// extractor emits the full immutable ShadowRun contract.
	var automationsCreated int
	var automationCandidates int

	s.emitStage(recordID, "automations")
	knownAutomations := loadKnownAutomations(s.stateDir())
	for i := range triageResults {
		tr := &triageResults[i]
		if !tr.WorthAutomation {
			continue
		}
		automationCandidates++
		if knownAutomations[tr.ConvKey] {
			continue
		}
		log.Printf("[feishu-dynamic] deferred legacy automation candidate %s until immutable shadow prediction support", tr.ConvKey)
	}
	saveKnownAutomations(s.stateDir(), knownAutomations)

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

	// Stage 8: First-run / rescan DM notification.
	// Mirror the Slack pattern: only mark FirstRunNotified=true on successful send
	// so a DM failure retries on the next initial/rescan cycle.
	// Skip DM when nothing was extracted (0 memories).
	if (scanType == "initial" || scanType == "rescan") && extractionErr == nil && newMemoryCount > 0 {
		state := s.loadState()
		if !state.FirstRunNotified {
			if openID != "" {
				s.bg.Add(1)
				go func() {
					defer s.bg.Done()
					dmText := generateFeishuScanSummary(convCount, newMemoryCount, automationNames)
					if err := s.app.sendFeishuDirectMessage(appID, openID, dmText); err != nil {
						log.Printf("[feishu-dynamic] first-run DM failed (will retry next scan): %v", err)
						return
					}
					log.Printf("[feishu-dynamic] first-run DM sent")
					// Save flag only on success to allow retry on failure.
					st := s.loadState()
					st.FirstRunNotified = true
					s.saveState(st)
				}()
			}
		}
	}
}

// generateFeishuScanSummary builds the DM text for the first-run notification.
func generateFeishuScanSummary(convCount, newMemoryCount int, automationNames []string) string {
	msg := "Based on analysis of your recent Feishu messages:\n"
	msg += fmt.Sprintf("- %d conversations scanned\n", convCount)
	msg += fmt.Sprintf("- %d memory files extracted\n", newMemoryCount)
	if len(automationNames) > 0 {
		msg += fmt.Sprintf("- %d automations discovered: %s", len(automationNames), strings.Join(automationNames, ", "))
	}
	return strings.TrimRight(msg, "\n")
}

// GetFeishuDynamicScanHistory returns the persisted dynamic scan history for the UI.
func (a *App) GetFeishuDynamicScanHistory() []DynamicScanRecord {
	if a.feishuScanner != nil {
		return a.feishuScanner.loadHistory()
	}
	// Scanner not ready yet — read the file directly.
	data, err := os.ReadFile(filepath.Join(feishuDynamicStateDir(), "history.json"))
	if err != nil {
		return nil
	}
	var history []DynamicScanRecord
	if err := json.Unmarshal(data, &history); err != nil {
		return nil
	}
	return history
}
