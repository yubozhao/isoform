package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	telegramPollInterval  = cadenceMessageScan
	telegramForceInterval = 1 * time.Hour
)

// telegramChatState is persisted state for one Telegram chat.
type telegramChatState struct {
	LastProcessedTS string    `json:"last_processed_ts"`
	LastSeenTS      string    `json:"last_seen_ts,omitempty"` // latest TS observed in the most recent poll
	LastExtractedAt time.Time `json:"last_extracted_at"`
	FirstDirtyAt    time.Time `json:"first_dirty_at,omitempty"`
}

// telegramScanState is the full persisted state file for the Telegram scanner.
type telegramScanState struct {
	Chats         map[string]telegramChatState `json:"chats"`
	PendingRescan bool                         `json:"pending_rescan,omitempty"`
}

// TelegramIncrementalScanner stores incoming Telegram messages locally and
// triggers extraction when enough time has passed for conversations to settle.
//
// Unlike Slack/Discord/Feishu, the Telegram Bot API does not support fetching
// historical messages, so there is no cold-start scan. Messages are only
// collected in real-time via webhook events (see daemon.go). Because of this,
// no first-run DM notification is sent — there is nothing meaningful to report
// on initial startup.
type TelegramIncrementalScanner struct {
	app       *App
	mu        sync.Mutex
	cancel    context.CancelFunc
	triggerCh chan struct{}

	// pollRW protects the entire poll() scope.
	// poll() holds RLock; RescanTelegram() holds Lock.
	pollRW sync.RWMutex

	// extractionCancel cancels any in-flight extraction pipeline.
	extractionCancel context.CancelFunc

	historyMu sync.Mutex // protects history file reads/writes for concurrent stages
}

// NewTelegramIncrementalScanner creates a scanner bound to the given App.
func NewTelegramIncrementalScanner(app *App) *TelegramIncrementalScanner {
	return &TelegramIncrementalScanner{
		app:       app,
		triggerCh: make(chan struct{}, 1),
	}
}

// Trigger requests an immediate poll. Non-blocking; coalesces multiple triggers.
func (s *TelegramIncrementalScanner) Trigger() {
	if !furnaceScanEnabled {
		return
	}
	select {
	case s.triggerCh <- struct{}{}:
	default:
	}
}

// cleanupStaleRunning marks any "running" history records left from a previous
// app session as interrupted so the UI never shows phantom running entries.
func (s *TelegramIncrementalScanner) cleanupStaleRunning() {
	hist := s.loadHistory()
	filtered := hist[:0]
	for _, r := range hist {
		if r.Status != "running" {
			filtered = append(filtered, r)
		}
	}
	if len(filtered) != len(hist) {
		s.saveHistory(filtered)
		log.Printf("[telegram-dynamic] removed %d stale running record(s)", len(hist)-len(filtered))
	}
}

// Run starts the poll loop. Blocks until ctx is cancelled or Stop() is called.
func (s *TelegramIncrementalScanner) Run(ctx context.Context) {
	if !furnaceScanEnabled {
		return
	}
	ctx, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	s.cancel = cancel
	s.mu.Unlock()
	defer cancel()

	s.cleanupStaleRunning()

	log.Printf("[telegram-dynamic] incremental scanner started (poll every %v)", telegramPollInterval)

	// Wait for an explicit trigger (from StartTelegramDaemon or
	// tryAutoStartTelegramDaemon) before the first poll.
	select {
	case <-s.triggerCh:
	case <-ctx.Done():
		return
	}
	s.poll(ctx)

	ticker := time.NewTicker(telegramPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			s.poll(ctx)
		case <-s.triggerCh:
			s.poll(ctx)
		case <-ctx.Done():
			log.Printf("[telegram-dynamic] incremental scanner stopped")
			return
		}
	}
}

// Stop cancels the scanner's context.
func (s *TelegramIncrementalScanner) Stop() {
	s.mu.Lock()
	if s.cancel != nil {
		s.cancel()
	}
	s.mu.Unlock()
}

// RescanTelegram clears all scan state and triggers a fresh full scan.
func (a *App) RescanTelegram() error {
	if !furnaceScanEnabled {
		return nil
	}
	s := a.telegramScanner
	if s == nil {
		return fmt.Errorf("telegram scanner not initialized")
	}

	// Cancel any in-flight extraction.
	s.mu.Lock()
	if s.extractionCancel != nil {
		s.extractionCancel()
	}
	s.mu.Unlock()

	// Acquire exclusive write lock — blocks until any in-flight poll finishes.
	s.pollRW.Lock()

	// Clear scan state, marking PendingRescan so next poll tags the run correctly.
	s.saveState(telegramScanState{
		Chats:         make(map[string]telegramChatState),
		PendingRescan: true,
	})

	// Clear telegramConvs from memory_processed.json via the generator,
	// which holds its own lock and writes atomically — no race with saveProcessedState.
	if mc := a.getMemoryGenerator(); mc != nil {
		mc.clearTelegramConvs()
	}

	// Clear known processed, automations, and conversations from state dir.
	stateDir := s.stateDir()
	_ = os.Remove(filepath.Join(stateDir, "known_processed.json"))
	_ = os.Remove(filepath.Join(stateDir, "known_automations.json"))
	_ = os.RemoveAll(filepath.Join(stateDir, "conversations"))

	s.pollRW.Unlock()

	s.Trigger()
	log.Printf("[telegram-dynamic] RescanTelegram: state cleared, triggered fresh scan")
	return nil
}

// appendTelegramMessage stores a new raw Telegram message to the local JSONL store.
// Called from daemon.go whenever a Telegram event is received.
func (a *App) appendTelegramMessage(msg TelegramRawMessage) {
	if !furnaceScanEnabled {
		return
	}
	if a.telegramScanner == nil {
		return
	}
	s := a.telegramScanner

	rawDir := filepath.Join(s.stateDir(), "raw")
	_ = os.MkdirAll(rawDir, 0700)

	chatFile := filepath.Join(rawDir, msg.ChatID+".jsonl")
	f, err := os.OpenFile(chatFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		log.Printf("[telegram-dynamic] appendTelegramMessage: open %s: %v", chatFile, err)
		return
	}
	defer f.Close()

	data, err := json.Marshal(msg)
	if err != nil {
		log.Printf("[telegram-dynamic] appendTelegramMessage: marshal: %v", err)
		return
	}
	if _, err := f.Write(append(data, '\n')); err != nil {
		log.Printf("[telegram-dynamic] appendTelegramMessage: write: %v", err)
	}
}

// ── State persistence ────────────────────────────────────────────────

func telegramDynamicStateDir() string {
	dataRoot, err := flavorDataDir()
	if err != nil {
		return filepath.Join(os.TempDir(), configDirName, "telegram-dynamic-scan")
	}
	return filepath.Join(dataRoot, "telegram-dynamic-scan")
}

func (s *TelegramIncrementalScanner) stateDir() string {
	return telegramDynamicStateDir()
}

func (s *TelegramIncrementalScanner) loadState() telegramScanState {
	path := filepath.Join(s.stateDir(), "state.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return telegramScanState{Chats: make(map[string]telegramChatState)}
	}
	var st telegramScanState
	if err := json.Unmarshal(data, &st); err != nil {
		return telegramScanState{Chats: make(map[string]telegramChatState)}
	}
	if st.Chats == nil {
		st.Chats = make(map[string]telegramChatState)
	}
	return st
}

func (s *TelegramIncrementalScanner) saveState(st telegramScanState) {
	dir := s.stateDir()
	_ = os.MkdirAll(dir, 0700)
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		log.Printf("[telegram-dynamic] save state: %v", err)
		return
	}
	writeFileAtomic(filepath.Join(dir, "state.json"), data, "[telegram-dynamic] save state")
}

func (s *TelegramIncrementalScanner) loadHistory() []DynamicScanRecord {
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

func (s *TelegramIncrementalScanner) saveHistory(history []DynamicScanRecord) {
	dir := s.stateDir()
	_ = os.MkdirAll(dir, 0700)
	data, err := json.MarshalIndent(history, "", "  ")
	if err != nil {
		log.Printf("[telegram-dynamic] save history: %v", err)
		return
	}
	writeFileAtomic(filepath.Join(dir, "history.json"), data, "[telegram-dynamic] save history")
}

func (s *TelegramIncrementalScanner) emitStage(recordID, stage string) {
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
	s.app.emitBrowserEvent("telegram-dynamic-scan:history", hist)
}

func (s *TelegramIncrementalScanner) emitProgress(recordID string, current, total int) {
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
	s.app.emitBrowserEvent("telegram-dynamic-scan:history", hist)
}

func (s *TelegramIncrementalScanner) emitStageDetails(recordID, stageName string, details []StageDetailItem) {
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
	s.app.emitBrowserEvent("telegram-dynamic-scan:history", hist)
}

func (s *TelegramIncrementalScanner) emitStageSummary(recordID, stageName, summary string) {
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
	s.app.emitBrowserEvent("telegram-dynamic-scan:history", hist)
}

func (s *TelegramIncrementalScanner) appendHistory(record DynamicScanRecord) []DynamicScanRecord {
	s.historyMu.Lock()
	defer s.historyMu.Unlock()
	history := s.loadHistory()
	history = append(history, record)
	if len(history) > 20 {
		history = history[len(history)-20:]
	}
	s.saveHistory(history)
	return history
}

// ── Poll ─────────────────────────────────────────────────────────────

func (s *TelegramIncrementalScanner) poll(ctx context.Context) {
	s.pollRW.RLock()
	defer s.pollRW.RUnlock()

	if ctx.Err() != nil {
		return
	}

	rawDir := filepath.Join(s.stateDir(), "raw")
	entries, err := os.ReadDir(rawDir)
	if err != nil {
		return // no messages yet
	}

	state := s.loadState()
	now := time.Now()

	type dirtyInfo struct {
		chatID string
	}
	triggerChats := make(map[string]dirtyInfo)

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		chatID := strings.TrimSuffix(entry.Name(), ".jsonl")

		cs := state.Chats[chatID]
		isFirstRun := cs.LastProcessedTS == ""

		latestTS := s.findLatestTSInJSONL(filepath.Join(rawDir, entry.Name()))
		if latestTS == "" {
			continue
		}

		dirty := latestTS != cs.LastProcessedTS
		if !dirty {
			// Nothing unprocessed; keep LastSeenTS current so the next dirty poll
			// can correctly compute settled = (latestTS == cs.LastSeenTS).
			cs.LastSeenTS = latestTS
			state.Chats[chatID] = cs
			continue
		}

		if isFirstRun {
			// Record that we've seen this chat, but do NOT advance LastProcessedTS.
			// If extraction fails or the app crashes before completion, the empty
			// LastProcessedTS ensures the next poll re-triggers extraction for this
			// chat (dirty check: latestTS != LastProcessedTS is always true when
			// LastProcessedTS is empty). LastProcessedTS is only advanced by
			// runExtraction on success.
			cs.LastSeenTS = latestTS
			cs.FirstDirtyAt = now
			state.Chats[chatID] = cs
			triggerChats[chatID] = dirtyInfo{chatID: chatID}
			continue
		}

		if cs.FirstDirtyAt.IsZero() {
			cs.FirstDirtyAt = now
		}

		// settled: TS hasn't changed since the previous poll — conversations have
		// quieted down.  LastProcessedTS is NOT updated here; it is advanced only
		// by runExtraction on success, ensuring we never silently skip messages.
		settled := cs.LastSeenTS != "" && latestTS == cs.LastSeenTS
		baseline := cs.LastExtractedAt
		if baseline.IsZero() {
			baseline = cs.FirstDirtyAt
		}
		overdue := !baseline.IsZero() && now.Sub(baseline) > telegramForceInterval

		cs.LastSeenTS = latestTS
		state.Chats[chatID] = cs

		if settled || overdue {
			triggerChats[chatID] = dirtyInfo{chatID: chatID}
		}
	}

	s.saveState(state)

	if len(triggerChats) == 0 {
		return
	}

	scanType := "incremental"
	if state.PendingRescan {
		scanType = "rescan"
		state.PendingRescan = false
		s.saveState(state)
	} else {
		allInitial := true
		for chatID := range triggerChats {
			if cs, ok := state.Chats[chatID]; ok && !cs.LastExtractedAt.IsZero() {
				allInitial = false
				break
			}
		}
		if allInitial {
			scanType = "initial"
		}
	}

	// Build afterTS map: chatID -> LastProcessedTS (cursor).
	// readTelegramJSONL will only return messages with EventTS > afterTS,
	// preventing re-processing of already-consumed messages.
	afterTS := make(map[string]string, len(triggerChats))
	for chatID := range triggerChats {
		if cs, ok := state.Chats[chatID]; ok {
			afterTS[chatID] = cs.LastProcessedTS // "" for first run → reads all
		}
	}

	chatIDs := make([]string, 0, len(triggerChats))
	for chatID := range triggerChats {
		chatIDs = append(chatIDs, chatID)
	}
	sort.Strings(chatIDs)

	s.runExtraction(ctx, chatIDs, afterTS, scanType)
}

// findLatestTSInJSONL returns the latest EventTS found in a Telegram JSONL file.
// Uses full fractional precision so sub-second messages are correctly detected.
func (s *TelegramIncrementalScanner) findLatestTSInJSONL(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	var latest float64
	var latestStr string
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024) // 1 MB max line
	for scanner.Scan() {
		var msg TelegramRawMessage
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			continue
		}
		ts := parseTelegramEventTSFloat(msg.EventTS)
		if ts > latest {
			latest = ts
			latestStr = msg.EventTS
		}
	}
	if err := scanner.Err(); err != nil {
		log.Printf("[telegram-scanner] findLatestTSInJSONL %s: scan error: %v", path, err)
	}
	return latestStr
}

// readTelegramJSONL reads messages from a JSONL file, returning only those
// with EventTS strictly greater than afterTS. Pass "" to read all messages.
// Comparison uses full fractional precision to avoid dropping messages that
// share the same integer-second as the cursor.
func readTelegramJSONL(path string, afterTS string) []TelegramRawMessage {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	afterF := parseTelegramEventTSFloat(afterTS) // 0 when afterTS is ""

	var msgs []TelegramRawMessage
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024) // 1 MB max line
	for scanner.Scan() {
		var msg TelegramRawMessage
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			continue
		}
		if afterF > 0 && parseTelegramEventTSFloat(msg.EventTS) <= afterF {
			continue
		}
		msgs = append(msgs, msg)
	}
	if err := scanner.Err(); err != nil {
		log.Printf("[telegram-scanner] readTelegramJSONL %s: scan error: %v", path, err)
	}
	return msgs
}

// parseTelegramEventTSFloat parses an EventTS string (e.g. "1734567890.123456"
// or "1734567890") into a float64 with full fractional precision. Returns 0 on
// failure or empty input.
func parseTelegramEventTSFloat(s string) float64 {
	if s == "" {
		return 0
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return v
}

// ── Extraction ───────────────────────────────────────────────────────

func (s *TelegramIncrementalScanner) runExtraction(
	ctx context.Context,
	chatIDs []string,
	afterTS map[string]string, // chatID -> LastProcessedTS cursor ("" for first run)
	scanType string,
) {
	if !s.app.telegramPipelineRunMu.TryLock() {
		log.Printf("[telegram-dynamic] extraction skipped: another Telegram pipeline run is active")
		return
	}
	defer s.app.telegramPipelineRunMu.Unlock()

	extractCtx, extractCancel := context.WithCancel(ctx)
	defer extractCancel()
	s.mu.Lock()
	s.extractionCancel = extractCancel
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.extractionCancel = nil; s.mu.Unlock() }()

	startedAt := time.Now()
	recordID := fmt.Sprintf("tg-dyn-%d", startedAt.UnixMilli())

	record := DynamicScanRecord{
		ID:           recordID,
		StartedAt:    startedAt,
		Status:       "running",
		ScanType:     scanType,
		ChannelNames: chatIDs,
		Stages:       initStages([]string{"fetching", "segmenting", "triaging", "extracting", "analyzing", "automations"}),
	}
	history := s.appendHistory(record)
	s.app.emitBrowserEvent("telegram-dynamic-scan:history", history)

	// Prepare work directory (ephemeral — cleaned up after extraction regardless of outcome).
	workDir := filepath.Join(s.stateDir(), "work")
	defer os.RemoveAll(workDir)
	for _, sub := range []string{"", "conversations", "triage", "analysis"} {
		_ = os.MkdirAll(filepath.Join(workDir, sub), 0700)
	}

	var extractionErr error
	var convCount, convsProcessed, automationCount int
	var automationNames []string
	// Track the latest consumed TS per chat for cursor advancement on success only.
	consumedTS := make(map[string]string)

	defer func() {
		completedAt := time.Now()

		// Advance cursors only on full success.
		if extractionErr == nil {
			state := s.loadState()
			for chatID, ts := range consumedTS {
				cs := state.Chats[chatID]
				cs.LastProcessedTS = ts
				cs.LastExtractedAt = completedAt
				cs.FirstDirtyAt = time.Time{}
				state.Chats[chatID] = cs
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
		s.app.emitBrowserEvent("telegram-dynamic-scan:history", hist)

		if extractionErr != nil {
			log.Printf("[telegram-dynamic] extraction failed: %v", extractionErr)
		} else {
			log.Printf("[telegram-dynamic] extraction done: %d convs, %d processed",
				convCount, convsProcessed)
		}
	}()

	// Stage 1: Fetch — read messages from local JSONL files.
	s.emitStage(recordID, "fetching")
	rawDir := filepath.Join(s.stateDir(), "raw")
	var allMsgs []TelegramRawMessage
	var fetchDetails []StageDetailItem
	for i, chatID := range chatIDs {
		s.emitProgress(recordID, i+1, len(chatIDs))
		if extractCtx.Err() != nil {
			extractionErr = extractCtx.Err()
			return
		}
		msgs := readTelegramJSONL(filepath.Join(rawDir, chatID+".jsonl"), afterTS[chatID])
		if len(msgs) == 0 {
			continue
		}
		allMsgs = append(allMsgs, msgs...)
		fetchDetails = append(fetchDetails, StageDetailItem{Label: chatID, Value: fmt.Sprintf("%d messages", len(msgs))})
		// Track latest TS per chat.
		for _, msg := range msgs {
			if msg.EventTS > consumedTS[chatID] {
				consumedTS[chatID] = msg.EventTS
			}
		}
	}
	log.Printf("[telegram-dynamic] fetched %d raw messages", len(allMsgs))
	s.emitStageDetails(recordID, "fetching", fetchDetails)
	s.emitStageSummary(recordID, "fetching", fmt.Sprintf("%d msgs from %d chats", len(allMsgs), len(chatIDs)))

	// Stage 2: Segment into conversations.
	s.emitStage(recordID, "segmenting")
	telegramConvs := segmentTelegramConversations(allMsgs)
	convCount = len(telegramConvs)
	log.Printf("[telegram-dynamic] segmented %d conversations", convCount)
	segDetails := []StageDetailItem{
		{Label: "conversations", Value: fmt.Sprintf("%d", convCount)},
	}
	for _, tc := range telegramConvs {
		segDetails = append(segDetails, StageDetailItem{Label: "time_gap", Value: telegramConvFileID(tc.ChatID, tc.ThreadID)})
	}
	s.emitStageDetails(recordID, "segmenting", segDetails)
	s.emitStageSummary(recordID, "segmenting", fmt.Sprintf("%d conversations", convCount))

	if convCount == 0 {
		return
	}

	// Write TelegramSlimConversations to stateDir/conversations/ for evaluateTelegramNow,
	// and write SlimConversation versions to workDir/conversations/ for the triage pipeline.
	statConvDir := filepath.Join(s.stateDir(), "conversations")
	_ = os.MkdirAll(statConvDir, 0700)
	workConvDir := filepath.Join(workDir, "conversations")

	var pipelineConvs []Conversation
	for _, tc := range telegramConvs {
		convID := telegramConvFileID(tc.ChatID, tc.ThreadID)

		// Write TelegramSlimConversation to stateDir for the memory generator.
		if data, err := json.MarshalIndent(tc, "", "  "); err == nil {
			_ = os.WriteFile(filepath.Join(statConvDir, convID+".json"), data, 0600)
		}

		// Build SlimConversation for triage/analyze pipeline.
		slim := SlimConversation{
			ID:           convID,
			Type:         "time_gap",
			ChannelID:    tc.ChatID,
			Topic:        "",
			MessageCount: len(tc.Messages),
		}
		for _, m := range tc.Messages {
			slim.Messages = append(slim.Messages, SlimMessage{
				UserID:   m.SenderID,
				UserName: m.SenderID,
				Text:     m.Text,
			})
		}
		if data, err := json.MarshalIndent(slim, "", "  "); err == nil {
			_ = os.WriteFile(filepath.Join(workConvDir, convID+".json"), data, 0600)
		}

		pipelineConvs = append(pipelineConvs, Conversation{
			ID:           convID,
			ConvKey:      convID,
			Type:         "time_gap",
			ChannelID:    tc.ChatID,
			MessageCount: len(tc.Messages),
		})
	}

	// Stage 3: Triage.
	s.emitStage(recordID, "triaging")
	triageResults, triageErrors, _, skippedLowMsg, skippedKnown := s.app.triageConversations(extractCtx, workDir, s.stateDir(), pipelineConvs)
	if triageErrors > 0 {
		log.Printf("[telegram-dynamic] %d triage errors", triageErrors)
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

	// Stage 4: Memory extraction.
	s.emitStage(recordID, "extracting")
	memDirPath, _ := memoryDir()
	beforeMemFiles := listFileNames(memDirPath)
	var failedIDCount int

	mc := s.app.getMemoryGenerator()
	if mc == nil {
		log.Printf("[telegram-dynamic] memory extraction skipped: generator not yet initialized")
	} else {
		var convIDs []string
		for _, c := range pipelineConvs {
			convIDs = append(convIDs, c.ID)
		}
		convIDs = append(convIDs, loadMemoryRetryIDs(s.stateDir())...)
		processed, failedIDs, memErr := mc.evaluateTelegramNow(extractCtx, s.stateDir(), convIDs)
		if memErr != nil {
			log.Printf("[telegram-dynamic] memory extraction: %v", memErr)
		}
		if memErr == nil {
			updateMemoryRetryQueue(s.stateDir(), failedIDs)
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
	retryIDs := loadMemoryRetryIDs(s.stateDir())
	if len(retryIDs) > 0 {
		extractDetails = append(extractDetails, StageDetailItem{Label: "retries queued", Value: fmt.Sprintf("%d", len(retryIDs))})
	}
	s.emitStageDetails(recordID, "extracting", extractDetails)
	s.emitStageSummary(recordID, "extracting", fmt.Sprintf("%d processed", convsProcessed))

	// Stage 5: Analysis.
	s.emitStage(recordID, "analyzing")
	specs := s.app.analyzeTopConversations(extractCtx, workDir, triageResults)
	var analyzeDetails []StageDetailItem
	for _, spec := range specs {
		analyzeDetails = append(analyzeDetails, StageDetailItem{Label: spec.AppName, Value: spec.Description})
	}
	s.emitStageDetails(recordID, "analyzing", analyzeDetails)
	s.emitStageSummary(recordID, "analyzing", fmt.Sprintf("%d analyzed", len(specs)))

	// Stage 6: Legacy message automation candidates stay quarantined until this
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
		log.Printf("[telegram-dynamic] deferred legacy automation candidate %s until immutable shadow prediction support", tr.ConvKey)
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
}

// telegramConvFileID returns a stable file ID from chat ID and thread ID.
func telegramConvFileID(chatID, threadID string) string {
	h := sha256.Sum256([]byte(chatID + ":" + threadID))
	return fmt.Sprintf("tgconv_%x", h[:6])
}

// GetTelegramDynamicScanHistory returns the persisted Telegram dynamic scan history.
func (a *App) GetTelegramDynamicScanHistory() []DynamicScanRecord {
	if a.telegramScanner != nil {
		return a.telegramScanner.loadHistory()
	}
	data, err := os.ReadFile(filepath.Join(telegramDynamicStateDir(), "history.json"))
	if err != nil {
		return nil
	}
	var history []DynamicScanRecord
	if err := json.Unmarshal(data, &history); err != nil {
		return nil
	}
	return history
}
