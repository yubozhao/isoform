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

const (
	discordIncrementalPollInterval  = cadenceMessageScan
	discordIncrementalForceInterval = 1 * time.Hour
)

// discordChannelScanState is persisted state for one channel or thread.
type discordChannelScanState struct {
	LastSeenMessageID string    `json:"last_seen_message_id"`
	LastExtractedAt   time.Time `json:"last_extracted_at"`
	LastScannedID     string    `json:"last_scanned_id"`
	FirstDirtyAt      time.Time `json:"first_dirty_at,omitempty"`
	ParentChannelID   string    `json:"parent_channel_id,omitempty"` // thread only; empty for channels
}

// discordIncrementalScanState is the full persisted state file.
type discordIncrementalScanState struct {
	Channels         map[string]discordChannelScanState `json:"channels"`
	Threads          map[string]discordChannelScanState `json:"threads,omitempty"`
	PendingRescan    bool                               `json:"pending_rescan,omitempty"`
	FirstRunNotified bool                               `json:"first_run_notified,omitempty"`
}

// DiscordDynamicScanRecord is one history entry shown in the UI.
type DiscordDynamicScanRecord struct {
	ID                   string       `json:"id"`
	StartedAt            time.Time    `json:"started_at"`
	CompletedAt          *time.Time   `json:"completed_at"`
	Status               string       `json:"status"`                           // "running" | "done" | "error"
	ScanType             string       `json:"scan_type"`                        // "initial" | "incremental" | "rescan"
	Stage                string       `json:"stage,omitempty"`                  // current stage while running (backward compat)
	Stages               []StageStats `json:"stages,omitempty"`                 // per-stage breakdown
	SelectedChannelNames []string     `json:"selected_channel_names,omitempty"` // all selected channels (top-level only)
	ChannelNames         []string     `json:"channel_names"`                    // dirty top-level channels (no threads)
	ThreadNames          []string     `json:"thread_names,omitempty"`           // dirty threads (format: "channel/thread")
	ConvCount            int          `json:"conv_count"`
	ConvsProcessed       int          `json:"convs_processed"`
	AutomationCount      int          `json:"automation_count"`
	Error                string       `json:"error,omitempty"`
}

// DiscordIncrementalScanner polls Discord channels for new messages
// and triggers extraction when channels settle or become overdue.
// Mirrors SlackIncrementalScanner.
type DiscordIncrementalScanner struct {
	app              *App
	mu               sync.Mutex
	cancel           context.CancelFunc
	extractionCancel context.CancelFunc
	trigger          chan struct{}
	pollRW           sync.RWMutex // RLock for poll, Lock for RescanDiscord
	historyMu        sync.Mutex   // protects history file reads/writes for concurrent stages
}

// NewDiscordIncrementalScanner creates a new scanner.
func NewDiscordIncrementalScanner(app *App) *DiscordIncrementalScanner {
	return &DiscordIncrementalScanner{
		app:     app,
		trigger: make(chan struct{}, 1),
	}
}

// Trigger requests an immediate poll (non-blocking, coalesces multiple calls).
func (s *DiscordIncrementalScanner) Trigger() {
	if !furnaceScanEnabled {
		return
	}
	select {
	case s.trigger <- struct{}{}:
	default: // already triggered
	}
}

// Run starts the background poll loop. Blocks until ctx is cancelled.
func (s *DiscordIncrementalScanner) Run(ctx context.Context) {
	if !furnaceScanEnabled {
		return
	}
	ctx, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	s.cancel = cancel
	s.mu.Unlock()
	defer cancel()

	// Cleanup stale "running" records from previous crash.
	s.cleanupStaleRunning()

	log.Printf("[discord-dynamic] incremental scanner started (poll every %v)", discordIncrementalPollInterval)

	// Wait for an explicit trigger (from StartDiscordDaemon or
	// tryAutoStartDiscordDaemon) before the first poll.
	select {
	case <-s.trigger:
	case <-ctx.Done():
		return
	}
	s.poll(ctx)

	ticker := time.NewTicker(discordIncrementalPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			s.poll(ctx)
		case <-s.trigger:
			s.poll(ctx)
		case <-ctx.Done():
			log.Printf("[discord-dynamic] incremental scanner stopped")
			return
		}
	}
}

// Stop cancels the poll loop.
func (s *DiscordIncrementalScanner) Stop() {
	s.mu.Lock()
	if s.cancel != nil {
		s.cancel()
	}
	s.mu.Unlock()
}

func (s *DiscordIncrementalScanner) poll(ctx context.Context) {
	s.pollRW.RLock()
	defer s.pollRW.RUnlock()

	if ctx.Err() != nil {
		return
	}

	token, err := loadYansuToken()
	if err != nil || token == "" {
		return
	}
	baseURL := loadAPIBaseURL()

	// Check Discord binding exists (mirrors Slack/Feishu: poll uses REST API,
	// not the daemon WebSocket, so daemon connectivity is not required).
	var binding discordBindingResponse
	if err := apiGet(baseURL, token, "/api/discord/bindings", &binding); err != nil || !binding.Bound {
		return
	}

	// Load persisted state (preserve PendingRescan for scanType tagging below).
	state := s.loadState()
	if state.PendingRescan {
		state = discordIncrementalScanState{
			Channels:      make(map[string]discordChannelScanState),
			Threads:       make(map[string]discordChannelScanState),
			PendingRescan: true,
		}
	}

	// Get authorized guilds.
	var guilds []DiscordGuild
	if err := apiGet(baseURL, token, "/api/discord/guilds", &guilds); err != nil {
		log.Printf("[discord-scan] poll: failed to fetch guilds: %v", err)
		return
	}

	type dirtyInfo struct {
		latestID    string
		channelName string
	}
	dirtyMap := make(map[string]dirtyInfo)
	threadDirtyMap := make(map[string]dirtyInfo) // threadID → dirtyInfo
	selectedChannelNameSet := make(map[string]struct{})

	// Load channel filter once for all guilds.
	channelFilter := loadConfig().DiscordScanChannelFilter

	for _, guild := range guilds {
		var channels []DiscordScanChannel
		if err := apiGet(baseURL, token, "/api/discord/guilds/"+guild.GuildID+"/channels", &channels); err != nil {
			continue
		}

		channels = filterDiscordChannels(guild.GuildID, channels, channelFilter)

		for _, ch := range channels {
			if ctx.Err() != nil {
				return
			}
			qualifiedName := guild.GuildName + "/" + ch.Name
			selectedChannelNameSet[qualifiedName] = struct{}{}

			// -- Channel dirty check --
			var messages []DiscordScanMessage
			endpoint := "/api/discord/channels/" + ch.ID + "/history?limit=1"
			if err := apiGet(baseURL, token, endpoint, &messages); err != nil || len(messages) == 0 {
				continue
			}

			latestID := messages[0].ID
			chState, exists := state.Channels[ch.ID]

			if !exists {
				// First time seeing this channel — trigger immediately, skip debounce.
				state.Channels[ch.ID] = discordChannelScanState{
					LastSeenMessageID: latestID,
					FirstDirtyAt:      time.Now(),
				}
				dirtyMap[ch.ID] = dirtyInfo{latestID: latestID, channelName: qualifiedName}
			} else {
				dirty := latestID != chState.LastScannedID
				settled := latestID == chState.LastSeenMessageID
				overdue := !chState.LastExtractedAt.IsZero() && time.Since(chState.LastExtractedAt) > discordIncrementalForceInterval

				chState.LastSeenMessageID = latestID
				if dirty && chState.FirstDirtyAt.IsZero() {
					chState.FirstDirtyAt = time.Now()
				}
				state.Channels[ch.ID] = chState

				if dirty && (settled || overdue) {
					dirtyMap[ch.ID] = dirtyInfo{latestID: latestID, channelName: qualifiedName}
				}
			}

			// -- Thread discovery for this channel --
			threads, err := s.app.fetchDiscordChannelThreads(ctx, baseURL, token, ch.ID, guild.GuildID)
			if err != nil {
				log.Printf("[discord-scan] poll: failed to fetch threads for channel %s: %v", ch.ID, err)
				continue
			}

			for _, thread := range threads {
				if ctx.Err() != nil {
					return
				}

				var threadMsgs []DiscordScanMessage
				threadEndpoint := "/api/discord/channels/" + thread.ID + "/history?limit=1"
				if err := apiGet(baseURL, token, threadEndpoint, &threadMsgs); err != nil || len(threadMsgs) == 0 {
					continue
				}

				threadLatestID := threadMsgs[0].ID
				thState, thExists := state.Threads[thread.ID]

				if !thExists {
					state.Threads[thread.ID] = discordChannelScanState{
						LastSeenMessageID: threadLatestID,
						FirstDirtyAt:      time.Now(),
						ParentChannelID:   ch.ID,
					}
					threadDirtyMap[thread.ID] = dirtyInfo{latestID: threadLatestID, channelName: ch.Name + "/" + thread.Name}
					continue
				}

				// Ensure ParentChannelID is always populated (backfill for old state).
				if thState.ParentChannelID == "" {
					thState.ParentChannelID = ch.ID
				}

				dirty := threadLatestID != thState.LastScannedID
				settled := threadLatestID == thState.LastSeenMessageID
				overdue := !thState.LastExtractedAt.IsZero() && time.Since(thState.LastExtractedAt) > discordIncrementalForceInterval

				thState.LastSeenMessageID = threadLatestID
				if dirty && thState.FirstDirtyAt.IsZero() {
					thState.FirstDirtyAt = time.Now()
				}
				state.Threads[thread.ID] = thState

				if dirty && (settled || overdue) {
					threadDirtyMap[thread.ID] = dirtyInfo{latestID: threadLatestID, channelName: ch.Name + "/" + thread.Name}
				}
			}
		}
	}

	if len(dirtyMap) == 0 && len(threadDirtyMap) == 0 {
		s.saveState(state)
		return
	}

	log.Printf("[discord-scan] %d dirty channels, %d dirty threads ready for extraction", len(dirtyMap), len(threadDirtyMap))

	// Determine scan type.
	scanType := "incremental"
	if state.PendingRescan {
		scanType = "rescan"
		state.PendingRescan = false
		s.saveState(state)
	} else {
		allInitial := true
		for chID := range dirtyMap {
			if cs, ok := state.Channels[chID]; ok && !cs.LastExtractedAt.IsZero() {
				allInitial = false
				break
			}
		}
		if allInitial {
			for thID := range threadDirtyMap {
				if ts, ok := state.Threads[thID]; ok && !ts.LastExtractedAt.IsZero() {
					allInitial = false
					break
				}
			}
		}
		if allInitial {
			scanType = "initial"
		}
	}

	// Build selected channel names (all filtered channels, for summary display).
	selectedChannelNames := make([]string, 0, len(selectedChannelNameSet))
	for name := range selectedChannelNameSet {
		selectedChannelNames = append(selectedChannelNames, name)
	}
	sort.Strings(selectedChannelNames)

	// Build per-channel afterID map, poll latest ID map, sorted channel names, and ID→name lookup.
	afterMap := make(map[string]string, len(dirtyMap))
	pollLatestMap := make(map[string]string, len(dirtyMap)+len(threadDirtyMap))
	idToName := make(map[string]string, len(dirtyMap)+len(threadDirtyMap))
	channelNames := make([]string, 0, len(dirtyMap))
	for chID, info := range dirtyMap {
		chState := state.Channels[chID]
		afterID := chState.LastScannedID
		if afterID == "" {
			afterID = discordSnowflakeFromTime(time.Now().Add(-7 * 24 * time.Hour))
		}
		afterMap[chID] = afterID
		pollLatestMap[chID] = info.latestID
		channelNames = append(channelNames, info.channelName)
		idToName[chID] = info.channelName
	}

	// Build per-thread afterID map and collect dirty thread names.
	threadAfterMap := make(map[string]string, len(threadDirtyMap))
	threadNames := make([]string, 0, len(threadDirtyMap))
	for thID, info := range threadDirtyMap {
		thState := state.Threads[thID]
		afterID := thState.LastScannedID
		if afterID == "" {
			afterID = discordSnowflakeFromTime(time.Now().Add(-7 * 24 * time.Hour))
		}
		threadAfterMap[thID] = afterID
		pollLatestMap[thID] = info.latestID
		threadNames = append(threadNames, info.channelName)
		idToName[thID] = info.channelName
	}
	sort.Strings(channelNames)
	sort.Strings(threadNames)

	s.saveState(state)
	s.runDiscordExtraction(ctx, afterMap, threadAfterMap, pollLatestMap, selectedChannelNames, channelNames, threadNames, idToName, scanType)
}

// runDiscordExtraction is the full 6-stage pipeline mirroring SlackIncrementalScanner.runExtraction:
// fetching → segmenting → triaging → extracting → analyzing → generating
// Cursor advancement is atomic: only on full pipeline success.
func (s *DiscordIncrementalScanner) runDiscordExtraction(
	ctx context.Context,
	afterMap map[string]string, // channelID → afterID (snowflake)
	threadAfterMap map[string]string, // threadID → afterID (snowflake)
	pollLatestMap map[string]string, // channelID/threadID → latest message ID seen during poll
	selectedChannelNames []string, // all selected channel names (top-level only)
	channelNames []string, // dirty top-level channel names
	threadNames []string, // dirty thread names (format: "channel/thread")
	idToName map[string]string, // channelID/threadID → human-readable name
	scanType string,
) {
	// Acquire pipeline mutex — skip if another pipeline (cold-start) is running.
	if !s.app.discordPipelineRunMu.TryLock() {
		log.Printf("[discord-scan] extraction skipped: another pipeline is running")
		return
	}
	defer s.app.discordPipelineRunMu.Unlock()

	scanDir, err := s.app.discordScanDir()
	if err != nil {
		log.Printf("[discord-scan] resolve scan data: %v", err)
		return
	}
	stateDir, err := discordDynamicStateDir()
	if err != nil {
		log.Printf("[discord-scan] resolve scan state: %v", err)
		return
	}

	// Child context so RescanDiscord can cancel extraction without stopping Run().
	extractCtx, extractCancel := context.WithCancel(ctx)
	defer extractCancel()
	s.mu.Lock()
	s.extractionCancel = extractCancel
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.extractionCancel = nil
		s.mu.Unlock()
	}()

	startedAt := time.Now()
	recordID := fmt.Sprintf("dyn-%d", startedAt.UnixMilli())
	record := DiscordDynamicScanRecord{
		ID:                   recordID,
		StartedAt:            startedAt,
		Status:               "running",
		ScanType:             scanType,
		SelectedChannelNames: cloneStringSlice(selectedChannelNames),
		ChannelNames:         cloneStringSlice(channelNames),
		ThreadNames:          cloneStringSlice(threadNames),
		Stages:               initStages([]string{"fetching", "segmenting", "triaging", "extracting", "analyzing", "automations"}),
	}
	history := s.appendHistory(record)
	s.emitHistorySlice(history)

	token, err := loadYansuToken()
	if err != nil || token == "" {
		s.finishRecord(recordID, 0, 0, 0, fmt.Errorf("not authenticated"))
		return
	}
	baseURL := loadAPIBaseURL()

	// Work directory — ephemeral, cleaned up at the end.
	workDir := filepath.Join(stateDir, "work")
	_ = os.RemoveAll(workDir)
	for _, sub := range []string{"", "conversations", "triage", "analysis"} {
		_ = os.MkdirAll(filepath.Join(workDir, sub), 0o700)
	}
	defer os.RemoveAll(workDir)

	var extractionErr error
	var convCount, convsProcessed, automationCount int
	var automationNames []string
	// consumedIDs tracks the latest scanned snowflake ID per channel.
	// consumedThreadIDs tracks the latest scanned snowflake ID per thread.
	// Cursors are only advanced on full pipeline success (see defer below).
	consumedIDs := make(map[string]string)
	consumedThreadIDs := make(map[string]string)

	defer func() {
		completedAt := time.Now()

		// Advance cursors only on full success.
		if extractionErr == nil {
			state := s.loadState()
			for chID, latestID := range consumedIDs {
				cs := state.Channels[chID]
				cs.LastScannedID = latestID
				cs.LastExtractedAt = completedAt
				cs.FirstDirtyAt = time.Time{}
				state.Channels[chID] = cs
			}
			for thID, latestID := range consumedThreadIDs {
				ts := state.Threads[thID]
				ts.LastScannedID = latestID
				ts.LastExtractedAt = completedAt
				ts.FirstDirtyAt = time.Time{}
				state.Threads[thID] = ts
			}
			s.saveState(state)
		}

		s.finishRecord(recordID, convCount, convsProcessed, automationCount, extractionErr)
	}()

	// ── Stage 1: Fetch ───────────────────────────────────────────────
	s.emitStage(recordID, "fetching")

	// fetchedGroup holds raw messages for a single channel or thread.
	type fetchedGroup struct {
		sourceID        string // channel or thread ID
		parentChannelID string // for threads: the parent channel; for channels: same as sourceID
		newestID        string // newest message ID (for cursor advancement)
		messages        []DiscordScanMessage
		isThread        bool
	}
	var fetched []fetchedGroup
	var fetchDetails []StageDetailItem
	totalFetchItems := len(afterMap) + len(threadAfterMap)

	fetchIdx := 0
	for chID, afterID := range afterMap {
		fetchIdx++
		s.emitProgress(recordID, fetchIdx, totalFetchItems)
		if extractCtx.Err() != nil {
			extractionErr = extractCtx.Err()
			return
		}

		messages, err := s.app.fetchDiscordChannelHistory(extractCtx, baseURL, token, chID, afterID)
		if err != nil {
			log.Printf("[discord-scan] fetch channel %s: %v", chID, err)
			extractionErr = fmt.Errorf("fetch channel %s: %w", chID, err)
			return
		}
		if len(messages) == 0 {
			// No new messages since afterID. Advance the cursor to the latest
			// message ID seen during poll so the channel is not re-dirtied.
			if pid := pollLatestMap[chID]; pid != "" {
				consumedIDs[chID] = pid
			}
			continue
		}
		consumedIDs[chID] = messages[0].ID // messages are newest-first
		log.Printf("[discord-scan] fetched %d messages for channel %s", len(messages), chID)
		chLabel := chID
		if name, ok := idToName[chID]; ok && name != "" {
			chLabel = name
		}
		fetchDetails = append(fetchDetails, StageDetailItem{Label: chLabel, Value: fmt.Sprintf("%d messages", len(messages))})
		fetched = append(fetched, fetchedGroup{
			sourceID:        chID,
			parentChannelID: chID,
			newestID:        messages[0].ID,
			messages:        messages,
			isThread:        false,
		})
	}

	// Fetch dirty threads (independent of channel fetches above).
	// Load state once to look up ParentChannelID for each thread (set during poll).
	threadState := s.loadState()
	for thID, afterID := range threadAfterMap {
		fetchIdx++
		s.emitProgress(recordID, fetchIdx, totalFetchItems)
		if extractCtx.Err() != nil {
			extractionErr = extractCtx.Err()
			return
		}

		messages, err := s.app.fetchDiscordChannelHistory(extractCtx, baseURL, token, thID, afterID)
		if err != nil {
			log.Printf("[discord-scan] fetch thread %s: %v", thID, err)
			extractionErr = fmt.Errorf("fetch thread %s: %w", thID, err)
			return
		}
		if len(messages) == 0 {
			if pid := pollLatestMap[thID]; pid != "" {
				consumedThreadIDs[thID] = pid
			}
			continue
		}
		consumedThreadIDs[thID] = messages[0].ID // messages are newest-first
		thLabel := thID
		if name, ok := idToName[thID]; ok && name != "" {
			thLabel = name + " (thread)"
		} else {
			thLabel = thID + " (thread)"
		}
		fetchDetails = append(fetchDetails, StageDetailItem{Label: thLabel, Value: fmt.Sprintf("%d messages", len(messages))})

		// Stamp ThreadID on all messages so segmenter groups them as a thread conversation.
		for i := range messages {
			messages[i].ThreadID = thID
		}

		fetched = append(fetched, fetchedGroup{
			sourceID:        thID,
			parentChannelID: threadState.Threads[thID].ParentChannelID,
			newestID:        messages[0].ID,
			messages:        messages,
			isThread:        true,
		})
	}

	s.emitStageDetails(recordID, "fetching", fetchDetails)
	s.emitStageSummary(recordID, "fetching", fmt.Sprintf("%d sources from %d channels + %d threads", len(fetched), len(afterMap), len(threadAfterMap)))

	// ── Stage 2: Segment ────────────────────────────────────────────
	s.emitStage(recordID, "segmenting")
	allConvs := make([]Conversation, 0)
	workConvDir := filepath.Join(workDir, "conversations")

	for _, fg := range fetched {
		convs := segmentDiscordConversations(fg.messages, fg.parentChannelID)

		// Only thread convs use versioned IDs; time_gap convs keep stable IDs.
		for i := range convs {
			if convs[i].Type == "thread" {
				convs[i].ID = discordConvFileIDVersioned(convs[i].ConvKey, fg.newestID)
			}
		}

		if err := writeDiscordConversations(convs, workConvDir); err != nil {
			kind := "channel"
			if fg.isThread {
				kind = "thread"
			}
			log.Printf("[discord-scan] write %s conversations: %v", kind, err)
			extractionErr = err
			return
		}
		allConvs = append(allConvs, convs...)
		log.Printf("[discord-scan] segmented %d conversations from %s", len(convs), fg.sourceID)
	}

	convCount = len(allConvs)
	segDetails := []StageDetailItem{
		{Label: "conversations", Value: fmt.Sprintf("%d", convCount)},
	}
	for _, c := range allConvs {
		segDetails = append(segDetails, StageDetailItem{Label: c.Type, Value: c.ID})
	}
	s.emitStageDetails(recordID, "segmenting", segDetails)
	s.emitStageSummary(recordID, "segmenting", fmt.Sprintf("%d conversations", convCount))
	if convCount == 0 {
		return
	}

	// Copy work conversations to shared dir (same pattern as Slack).
	sharedConvDir := filepath.Join(scanDir, "conversations")
	_ = os.MkdirAll(sharedConvDir, 0o700)
	if entries, err := os.ReadDir(filepath.Join(workDir, "conversations")); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			src := filepath.Join(workDir, "conversations", e.Name())
			dst := filepath.Join(sharedConvDir, e.Name())
			if data, err := os.ReadFile(src); err == nil {
				_ = os.WriteFile(dst, data, 0o600)
			}
		}
	}

	// ── Stage 3: Triage ──────────────────────────────────────────────
	s.emitStage(recordID, "triaging")
	triageResults, triageErrors, _, skippedLowMsg, skippedKnown := s.app.triageConversations(extractCtx, workDir, scanDir, allConvs)
	if triageErrors > 0 {
		log.Printf("[discord-scan] %d triage errors", triageErrors)
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

	// ── Stage 4: Memory extraction ───────────────────────────────────
	s.emitStage(recordID, "extracting")
	memDirPath, _ := memoryDir()
	beforeMemFiles := listFileNames(memDirPath)
	var failedIDCount int

	mc := s.app.getMemoryGenerator()
	if mc == nil {
		log.Printf("[discord-scan] memory extraction skipped: generator not ready")
	} else {
		var convIDs []string
		for _, c := range allConvs {
			convIDs = append(convIDs, c.ID)
		}
		retryIDs := loadMemoryRetryIDs(stateDir)
		convIDs = append(convIDs, retryIDs...)
		processed, _, failedIDs, mcErr := mc.evaluateDiscordConversations(extractCtx, convIDs)
		if mcErr != nil && extractCtx.Err() == nil {
			log.Printf("[discord-scan] memory extraction: %v", mcErr)
		}
		if mcErr == nil {
			updateMemoryRetryQueue(stateDir, failedIDs)
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
	retryIDs := loadMemoryRetryIDs(stateDir)
	if len(retryIDs) > 0 {
		extractDetails = append(extractDetails, StageDetailItem{Label: "retries queued", Value: fmt.Sprintf("%d", len(retryIDs))})
	}
	s.emitStageDetails(recordID, "extracting", extractDetails)
	s.emitStageSummary(recordID, "extracting", fmt.Sprintf("%d processed", convsProcessed))

	// ── Stage 5: Analyze ─────────────────────────────────────────────
	s.emitStage(recordID, "analyzing")
	specs := s.app.analyzeTopConversations(extractCtx, workDir, triageResults)
	var analyzeDetails []StageDetailItem
	for _, spec := range specs {
		analyzeDetails = append(analyzeDetails, StageDetailItem{Label: spec.AppName, Value: spec.Description})
	}
	s.emitStageDetails(recordID, "analyzing", analyzeDetails)
	s.emitStageSummary(recordID, "analyzing", fmt.Sprintf("%d analyzed", len(specs)))

	// Finish analyzing before entering parallel stages so it doesn't stay
	// "running" alongside generating/automations (which would also cause
	// updateStageProgress to target the wrong stage).
	s.emitFinishStage(recordID, "analyzing")

	// ── Stage 6: Legacy message candidates ───────────────────────────
	// Keep these candidates quarantined until this extractor emits the full
	// immutable ShadowRun contract.
	var automationsCreated int
	var automationCandidates int
	s.emitStage(recordID, "automations")
	knownAutomations := loadKnownAutomations(scanDir)
	for i := range triageResults {
		tr := &triageResults[i]
		if !tr.WorthAutomation {
			continue
		}
		automationCandidates++
		if knownAutomations[tr.ConvKey] {
			continue
		}
		log.Printf("[discord-dynamic] deferred legacy automation candidate %s until immutable shadow prediction support", tr.ConvKey)
	}
	saveKnownAutomations(scanDir, knownAutomations)
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
	s.emitFinishStage(recordID, "automations")
	automationCount = automationsCreated

	// ── DM notification (initial / rescan only) ──────────────────────
	// Skip DM when nothing was extracted (0 memories).
	if (scanType == "initial" || scanType == "rescan") && extractionErr == nil && newMemoryCount > 0 {
		state := s.loadState()
		if !state.FirstRunNotified {
			dmText := generateDiscordScanSummary(convCount, newMemoryCount, automationNames)
			cfg := loadConfig()
			if cfg.Discord != nil && cfg.Discord.DiscordUserID != "" {
				if err := s.app.SendDiscordDM(cfg.Discord.DiscordUserID, dmText); err != nil {
					log.Printf("[discord-scan] DM notification failed (will retry next run): %v", err)
				} else {
					log.Printf("[discord-scan] DM notification sent")
					state.FirstRunNotified = true
					s.saveState(state)
				}
			}
		}
	}

	log.Printf("[discord-scan] done: %d convs, %d processed", convCount, convsProcessed)
}

// generateDiscordScanSummary builds the DM text for the first-run/rescan notification.
func generateDiscordScanSummary(convCount, newMemoryCount int, automationNames []string) string {
	msg := "Based on analysis of your past 7 days of Discord messages:\n"
	msg += fmt.Sprintf("- %d conversations scanned\n", convCount)
	msg += fmt.Sprintf("- %d memory files extracted\n", newMemoryCount)
	if len(automationNames) > 0 {
		msg += fmt.Sprintf("- %d automations discovered: %s", len(automationNames), strings.Join(automationNames, ", "))
	}
	return strings.TrimRight(msg, "\n")
}

// finishRecord updates a history record to "done"/"error" and persists it.
func (s *DiscordIncrementalScanner) finishRecord(recordID string, convCount, convsProcessed, automationCount int, err error) {
	now := time.Now()
	hist := s.loadHistory()
	for i := range hist {
		if hist[i].ID == recordID {
			hist[i].CompletedAt = &now
			hist[i].ConvCount = convCount
			hist[i].ConvsProcessed = convsProcessed
			hist[i].AutomationCount = automationCount
			hist[i].Stage = ""
			hist[i].Stages = finishStages(hist[i].Stages)
			if err != nil {
				hist[i].Status = "error"
				hist[i].Error = err.Error()
			} else {
				hist[i].Status = "done"
			}
			break
		}
	}
	s.saveHistory(hist)
	s.emitHistorySlice(hist)
}

// RescanDiscord clears all state and triggers a fresh full scan.
func (s *DiscordIncrementalScanner) RescanDiscord() {
	scanDir, err := s.app.discordScanDir()
	if err != nil {
		log.Printf("[discord-scan] resolve scan data: %v", err)
		return
	}
	// Cancel any in-flight extraction first.
	s.mu.Lock()
	if s.extractionCancel != nil {
		s.extractionCancel()
	}
	s.mu.Unlock()

	// Wait for any in-flight poll to finish before mutating state.
	s.pollRW.Lock()
	defer s.pollRW.Unlock()

	// Write state with PendingRescan so the next poll tags itself as "rescan".
	// Clear FirstRunNotified so the DM fires again after rescan (mirrors Slack).
	s.saveState(discordIncrementalScanState{
		Channels:      make(map[string]discordChannelScanState),
		Threads:       make(map[string]discordChannelScanState),
		PendingRescan: true,
	})

	// Clear known processed conversations.
	os.Remove(filepath.Join(scanDir, "known_processed.json"))
	os.Remove(filepath.Join(scanDir, "known_automations.json"))
	os.RemoveAll(filepath.Join(scanDir, "conversations"))

	s.Trigger()
}

// ── History persistence ──────────────────────────────────────────────

func (s *DiscordIncrementalScanner) loadHistory() []DiscordDynamicScanRecord {
	stateDir, err := discordDynamicStateDir()
	if err != nil {
		log.Printf("[discord-scan] resolve history folder: %v", err)
		return nil
	}
	data, err := os.ReadFile(filepath.Join(stateDir, "history.json"))
	if err != nil {
		return nil
	}
	var history []DiscordDynamicScanRecord
	if err := json.Unmarshal(data, &history); err != nil {
		return nil
	}
	return normalizeDiscordDynamicScanHistory(history)
}

func (s *DiscordIncrementalScanner) saveHistory(history []DiscordDynamicScanRecord) {
	stateDir, err := discordDynamicStateDir()
	if err != nil {
		log.Printf("[discord-scan] save history: %v", err)
		return
	}
	os.MkdirAll(stateDir, 0o755)
	history = normalizeDiscordDynamicScanHistory(history)
	data, err := json.MarshalIndent(history, "", "  ")
	if err != nil {
		return
	}
	tmp := filepath.Join(stateDir, "history.json.tmp")
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return
	}
	os.Rename(tmp, filepath.Join(stateDir, "history.json"))
}

func (s *DiscordIncrementalScanner) appendHistory(record DiscordDynamicScanRecord) []DiscordDynamicScanRecord {
	history := s.loadHistory()
	history = append(history, record)
	// Cap at 20 entries, keeping newest.
	if len(history) > 20 {
		history = history[len(history)-20:]
	}
	s.saveHistory(history)
	return history
}

func normalizeDiscordDynamicScanHistory(history []DiscordDynamicScanRecord) []DiscordDynamicScanRecord {
	if history == nil {
		return []DiscordDynamicScanRecord{}
	}
	for i := range history {
		if history[i].ChannelNames == nil {
			history[i].ChannelNames = []string{}
		}
		if history[i].SelectedChannelNames == nil {
			history[i].SelectedChannelNames = []string{}
		}
		if history[i].ThreadNames == nil {
			history[i].ThreadNames = []string{}
		}
		if history[i].Stages == nil {
			history[i].Stages = []StageStats{}
		}
	}
	return history
}

func cloneStringSlice(values []string) []string {
	if len(values) == 0 {
		return []string{}
	}
	return append([]string(nil), values...)
}

func (s *DiscordIncrementalScanner) emitHistorySlice(h []DiscordDynamicScanRecord) {
	if s.app.ctx == nil {
		return
	}
	s.app.emitBrowserEvent("discord-dynamic-scan:history", h)
}

// emitStage transitions to a new stage in the running record and emits the history event.
func (s *DiscordIncrementalScanner) emitStage(recordID, stage string) {
	s.historyMu.Lock()
	defer s.historyMu.Unlock()
	hist := s.loadHistory()
	for i := range hist {
		if hist[i].ID == recordID {
			hist[i].Stage = stage
			hist[i].Stages = transitionStage(hist[i].Stages, stage)
			break
		}
	}
	s.saveHistory(hist)
	s.emitHistorySlice(hist)
}

// emitStageParallel marks a stage as "running" without finishing other running stages.
// It also updates the backward-compat Stage field so legacy consumers see a current value.
func (s *DiscordIncrementalScanner) emitStageParallel(recordID, stage string) {
	s.historyMu.Lock()
	defer s.historyMu.Unlock()
	hist := s.loadHistory()
	for i := range hist {
		if hist[i].ID == recordID {
			hist[i].Stage = stage
			hist[i].Stages = startStageParallel(hist[i].Stages, stage)
			break
		}
	}
	s.saveHistory(hist)
	s.emitHistorySlice(hist)
}

// emitFinishStage marks a single named stage as "done".
// The backward-compat Stage field is set to whichever stage is still running
// (if any), so legacy consumers always see "current stage while running".
func (s *DiscordIncrementalScanner) emitFinishStage(recordID, stage string) {
	s.historyMu.Lock()
	defer s.historyMu.Unlock()
	hist := s.loadHistory()
	for i := range hist {
		if hist[i].ID == recordID {
			hist[i].Stages = finishStage(hist[i].Stages, stage)
			// Point Stage to a still-running stage if one exists.
			hist[i].Stage = stage // fallback: the one we just finished
			for _, st := range hist[i].Stages {
				if st.Status == "running" {
					hist[i].Stage = st.Name
					break
				}
			}
			break
		}
	}
	s.saveHistory(hist)
	s.emitHistorySlice(hist)
}

// emitProgress updates the progress counter on the current running stage.
func (s *DiscordIncrementalScanner) emitProgress(recordID string, current, total int) {
	s.historyMu.Lock()
	defer s.historyMu.Unlock()
	hist := s.loadHistory()
	for i := range hist {
		if hist[i].ID == recordID {
			hist[i].Stages = updateStageProgress(hist[i].Stages, current, total)
			break
		}
	}
	s.saveHistory(hist)
	s.emitHistorySlice(hist)
}

// emitStageDetails sets the detail items on a named stage.
func (s *DiscordIncrementalScanner) emitStageDetails(recordID, stageName string, details []StageDetailItem) {
	s.historyMu.Lock()
	defer s.historyMu.Unlock()
	hist := s.loadHistory()
	for i := range hist {
		if hist[i].ID == recordID {
			hist[i].Stages = setStageDetails(hist[i].Stages, stageName, details)
			break
		}
	}
	s.saveHistory(hist)
	s.emitHistorySlice(hist)
}

// emitStageSummary sets the summary text on a named stage.
func (s *DiscordIncrementalScanner) emitStageSummary(recordID, stageName, summary string) {
	s.historyMu.Lock()
	defer s.historyMu.Unlock()
	hist := s.loadHistory()
	for i := range hist {
		if hist[i].ID == recordID {
			hist[i].Stages = setStageSummary(hist[i].Stages, stageName, summary)
			break
		}
	}
	s.saveHistory(hist)
	s.emitHistorySlice(hist)
}

// ── App-level bindings ───────────────────────────────────────────────

// GetDiscordDynamicScanHistory returns the scan history records.
func (a *App) GetDiscordDynamicScanHistory() []DiscordDynamicScanRecord {
	if a.discordScanner != nil {
		return a.discordScanner.loadHistory()
	}
	// Scanner not ready yet — read from disk directly so the UI shows
	// persisted history on first mount (mirrors GetDynamicScanHistory for Slack).
	stateDir, err := discordDynamicStateDir()
	if err != nil {
		log.Printf("[discord-scan] resolve history folder: %v", err)
		return nil
	}
	data, err := os.ReadFile(filepath.Join(stateDir, "history.json"))
	if err != nil {
		return nil
	}
	var history []DiscordDynamicScanRecord
	if err := json.Unmarshal(data, &history); err != nil {
		return nil
	}
	return history
}

// GetDiscordScanChannelFilter returns the per-guild channel filter.
func (a *App) GetDiscordScanChannelFilter() map[string][]string {
	cfg := loadConfig()
	if cfg.DiscordScanChannelFilter == nil {
		return map[string][]string{}
	}
	return cfg.DiscordScanChannelFilter
}

// SetDiscordScanChannelFilter sets the per-guild channel filter.
func (a *App) SetDiscordScanChannelFilter(filter map[string][]string) error {
	return updateConfig(func(c *yansuConfig) {
		c.DiscordScanChannelFilter = filter
	})
}

// DiscordChannelEntry is a channel entry returned by GetDiscordChannels.
type DiscordChannelEntry struct {
	GuildID     string `json:"guild_id"`
	GuildName   string `json:"guild_name"`
	ChannelID   string `json:"channel_id"`
	ChannelName string `json:"channel_name"`
	Type        int    `json:"type"`
}

// GetDiscordChannels fetches all text channels across all authorized guilds.
func (a *App) GetDiscordChannels() ([]DiscordChannelEntry, error) {
	token, err := loadYansuToken()
	if err != nil || token == "" {
		return nil, fmt.Errorf("not authenticated")
	}
	baseURL := loadAPIBaseURL()

	var guilds []DiscordGuild
	if err := apiGet(baseURL, token, "/api/discord/guilds", &guilds); err != nil {
		return nil, fmt.Errorf("failed to fetch guilds: %w", err)
	}

	var result []DiscordChannelEntry
	for _, guild := range guilds {
		var channels []DiscordScanChannel
		if err := apiGet(baseURL, token, "/api/discord/guilds/"+guild.GuildID+"/channels", &channels); err != nil {
			continue
		}
		for _, ch := range channels {
			result = append(result, DiscordChannelEntry{
				GuildID:     guild.GuildID,
				GuildName:   guild.GuildName,
				ChannelID:   ch.ID,
				ChannelName: ch.Name,
				Type:        ch.Type,
			})
		}
	}
	return result, nil
}

// RescanDiscord clears all Discord scan state and triggers a fresh full scan.
func (a *App) RescanDiscord() error {
	if !furnaceScanEnabled {
		return nil
	}
	if a.discordScanner == nil {
		return fmt.Errorf("discord scanner not initialized")
	}
	a.discordScanner.RescanDiscord()
	return nil
}

// cleanupStaleRunning removes any "running" records left over from a previous
// app session. Called once at scanner startup.
func (s *DiscordIncrementalScanner) cleanupStaleRunning() {
	hist := s.loadHistory()
	cleaned := hist[:0]
	for _, r := range hist {
		if r.Status != "running" {
			cleaned = append(cleaned, r)
		}
	}
	if len(cleaned) != len(hist) {
		s.saveHistory(cleaned)
	}
}

func discordDynamicStateDir() (string, error) {
	dataRoot, err := flavorDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dataRoot, "discord-dynamic-scan"), nil
}

func (s *DiscordIncrementalScanner) loadState() discordIncrementalScanState {
	stateDir, err := discordDynamicStateDir()
	if err != nil {
		log.Printf("[discord-scan] load state: %v", err)
		return discordIncrementalScanState{Channels: make(map[string]discordChannelScanState), Threads: make(map[string]discordChannelScanState)}
	}
	data, err := os.ReadFile(filepath.Join(stateDir, "state.json"))
	if err != nil {
		return discordIncrementalScanState{
			Channels: make(map[string]discordChannelScanState),
			Threads:  make(map[string]discordChannelScanState),
		}
	}
	var state discordIncrementalScanState
	if err := json.Unmarshal(data, &state); err != nil {
		return discordIncrementalScanState{
			Channels: make(map[string]discordChannelScanState),
			Threads:  make(map[string]discordChannelScanState),
		}
	}
	if state.Channels == nil {
		state.Channels = make(map[string]discordChannelScanState)
	}
	if state.Threads == nil {
		state.Threads = make(map[string]discordChannelScanState)
	}
	return state
}

func (s *DiscordIncrementalScanner) saveState(state discordIncrementalScanState) {
	stateDir, err := discordDynamicStateDir()
	if err != nil {
		log.Printf("[discord-scan] save state: %v", err)
		return
	}
	os.MkdirAll(stateDir, 0o755)

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return
	}
	os.WriteFile(filepath.Join(stateDir, "state.json"), data, 0o644)
}
