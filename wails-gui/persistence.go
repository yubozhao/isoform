package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"yansu-agent/agent"

	persistencesvc "wails-gui/internal/persistence"
	"wails-gui/internal/pii"
)

// Re-export persistence DTOs so Wails-exposed method signatures stay stable.
type ProjectData = persistencesvc.ProjectData
type WorkspaceData = persistencesvc.WorkspaceData
type SessionData = persistencesvc.SessionData
type ForkSessionResult = persistencesvc.ForkSessionResult
type StreamItemData = persistencesvc.StreamItemData
type StreamItemPageData = persistencesvc.StreamItemPageData
type SidebarGroupData = persistencesvc.SidebarGroupData
type SidebarGroupItemData = persistencesvc.SidebarGroupItemData

func (a *App) persistenceService() *persistencesvc.Service {
	return persistencesvc.NewService(a.store)
}

// LoadAllProjects loads all persisted projects with workspaces, sessions, and stream items.
func (a *App) LoadAllProjects() (perfValue []ProjectData, perfErr error) {
	defer logPageBinding("LoadAllProjects", time.Now(), &perfValue)
	projects, err := a.persistenceService().LoadAllProjects(context.Background())
	return displayModelCopy(projects, a.modelDisplayLanguage()), err
}

// EnsureProjectStructure inserts a project, its workspaces, and their sessions
// only if they do not already exist. Used to satisfy FK constraints before
// inserting stream items without overwriting mutable fields (e.g. session labels).
func (a *App) EnsureProjectStructure(data ProjectData) error {
	return a.persistenceService().EnsureProjectStructure(context.Background(), data)
}

// SaveProjectState persists a full project with workspaces and sessions.
func (a *App) SaveProjectState(data ProjectData) error {
	return a.persistenceService().SaveProjectState(context.Background(), data)
}

// DeleteProjectData removes a project and all its data, including checkpoint files on disk.
func (a *App) DeleteProjectData(id string) error {
	if a.store == nil {
		return nil
	}

	ctx := context.Background()

	// Deleting the project would cascade over the main chat.
	if a.mainChatLivesIn(ctx, id, "") {
		return errMainChatProtected
	}

	// Find all sessions for this project and clean up their checkpoints.
	sessions, err := a.store.LoadSessions(ctx, id)
	if err != nil {
		return err
	}
	var sessionIDs []string
	for _, s := range sessions {
		// Delete checkpoint DB records for each session.
		if _, err := a.store.DeleteCheckpointFilesForSession(ctx, s.LocalID); err != nil {
			log.Printf("[delete-project] delete checkpoint records for session %s: %v", s.LocalID, err)
		}
		sessionIDs = append(sessionIDs, s.LocalID)
	}
	// Clean up checkpoint directories on disk.
	a.cleanupCheckpointDisk(sessionIDs)

	if err := a.store.DeleteProject(ctx, id); err != nil {
		return err
	}
	for _, sessionID := range sessionIDs {
		publishPrivacyWithdrawal(privacyWithdrawalScope{Kind: privacyWithdrawalSession, ID: sessionID})
	}

	// VACUUM after large deletion to reclaim space.
	if err := a.store.Vacuum(); err != nil {
		log.Printf("[delete-project] vacuum failed: %v", err)
	}

	return nil
}

// SaveSessionState persists a single session under a project.
func (a *App) SaveSessionState(projectID string, data SessionData) error {
	return a.persistenceService().SaveSessionState(context.Background(), projectID, data)
}

// ForkSession snapshots a completed chat into a new independent chat.
func (a *App) ForkSession(sourceLocalID, newLabel string) (ForkSessionResult, error) {
	a.mu.Lock()
	isStreaming := a.activeTags[sourceLocalID]
	a.mu.Unlock()
	if isStreaming {
		return ForkSessionResult{}, fmt.Errorf("cannot branch a chat while it is responding")
	}
	return a.persistenceService().ForkSession(context.Background(), sourceLocalID, newLabel)
}

// SaveWorkspaceState persists a single workspace.
func (a *App) SaveWorkspaceState(projectID string, data WorkspaceData) error {
	return a.persistenceService().SaveWorkspaceState(context.Background(), projectID, data)
}

// DeleteSessionData removes a session, its items, and associated checkpoint files.
func (a *App) DeleteSessionData(localID string) (err error) {
	if a.store == nil {
		return nil
	}

	ctx := context.Background()

	// The main chat is never deleted; the renderer hides the action, and a
	// stale caller gets a clear error instead of an empty sidebar.
	if a.isMainChatSession(ctx, localID) {
		return errMainChatProtected
	}

	// No turn starts here from now on (executePrompt and the background
	// resume check this under a.mu), so a send from another window or a
	// script finishing during the cleanup below cannot store rows after the
	// delete. Racing deletes of the same chat share the mark: the count
	// releases it only once the last delete ends, and only when none of
	// them succeeded.
	a.mu.Lock()
	if a.deletingChats == nil {
		a.deletingChats = map[string]int{}
	}
	a.deletingChats[localID]++
	a.mu.Unlock()

	// Release the mark when this delete ends, even when the cleanup panics:
	// a chat whose delete never finished takes turns again. A successful
	// delete leaves the terminal mark in deletedChats instead — the chat is
	// gone and never takes a turn again, whatever a racing delete that is
	// still running does after this one.
	deleteSucceeded := false
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[delete-session] panic during cleanup tag=%s: %v\n%s", localID, r, debug.Stack())
			err = fmt.Errorf("delete session: %v", r)
		}
		a.mu.Lock()
		if deleteSucceeded {
			if a.deletedChats == nil {
				a.deletedChats = map[string]bool{}
			}
			a.deletedChats[localID] = true
		}
		if n := a.deletingChats[localID] - 1; n > 0 {
			a.deletingChats[localID] = n
		} else {
			delete(a.deletingChats, localID)
		}
		a.mu.Unlock()
	}()

	// Stop the chat's active run the way the Stop button does, and wait for
	// it to unwind, so its late writes cannot land after the rows are gone.
	a.cancelActiveChatTurn(localID)
	a.waitForChatTurnEnd(localID)

	// Clear dangling active_session_id references before deletion.
	if err := a.store.ClearActiveSessionReferences(ctx, localID); err != nil {
		log.Printf("[delete-session] clear active session refs: %v", err)
	}

	// Delete checkpoint DB records and collect rows for disk cleanup.
	if deletedRows, err := a.store.DeleteCheckpointFilesForSession(ctx, localID); err != nil {
		log.Printf("[delete-session] delete checkpoint records: %v", err)
	} else if len(deletedRows) > 0 {
		a.cleanupCheckpointDisk([]string{localID})
	}

	// Forget the chat's background scripts, so one that finishes later
	// neither records nor resumes anything here.
	if a.bgShells != nil {
		a.bgShells.RemoveForSession(localID)
	}

	// Clean up any temp directory created for this session.
	a.CleanupSessionTempDir(localID)

	// Clean up memory generator state for this session.
	if mc := a.getMemoryGenerator(); mc != nil {
		mc.CleanupSession(localID)
	}

	// Drop any in-memory "Allow for this chat" MCP trust grants tied to
	// this session so the runner's SessionTrust map doesn't grow without
	// bound as the user deletes old chats. Persistent ("Always allow
	// this server") trust lives in trusted.json and is unaffected.
	a.mu.Lock()
	runner := a.runner
	a.mu.Unlock()
	if nr, ok := agent.NativeRunnerOf(runner); ok {
		nr.ClearSessionTrust(localID)
	}

	if err := a.store.DeleteSession(ctx, localID); err != nil {
		// The chat is still there, so it takes turns again once every racing
		// delete has ended. The cleanup above stays done: its checkpoints,
		// background scripts, temporary folder and "Allow for this chat"
		// grants are gone.
		return err
	}
	deleteSucceeded = true
	publishPrivacyWithdrawal(privacyWithdrawalScope{Kind: privacyWithdrawalSession, ID: localID})
	return nil
}

// DeleteWorkspaceData removes a workspace and all its sessions/items, including checkpoint files.
func (a *App) DeleteWorkspaceData(workspaceID string) error {
	if a.store == nil {
		return nil
	}

	ctx := context.Background()

	// Deleting the workspace would cascade over the main chat.
	if a.mainChatLivesIn(ctx, "", workspaceID) {
		return errMainChatProtected
	}

	// Find sessions in this workspace and clean up their checkpoints.
	allByWorkspace, err := a.store.LoadAllSessionsByWorkspace(ctx)
	if err != nil {
		return err
	}
	var sessionIDs []string
	for _, s := range allByWorkspace[workspaceID] {
		if _, err := a.store.DeleteCheckpointFilesForSession(ctx, s.LocalID); err != nil {
			log.Printf("[delete-workspace] delete checkpoint records for session %s: %v", s.LocalID, err)
		}
		sessionIDs = append(sessionIDs, s.LocalID)
	}
	a.cleanupCheckpointDisk(sessionIDs)

	if err := a.store.DeleteWorkspace(ctx, workspaceID); err != nil {
		return err
	}
	for _, sessionID := range sessionIDs {
		publishPrivacyWithdrawal(privacyWithdrawalScope{Kind: privacyWithdrawalSession, ID: sessionID})
	}

	// VACUUM after large deletion to reclaim space.
	if err := a.store.Vacuum(); err != nil {
		log.Printf("[delete-workspace] vacuum failed: %v", err)
	}

	return nil
}

// PersistStreamItem appends a single stream item to the database.
func (a *App) PersistStreamItem(sessionLocalID string, item StreamItemData) (int, error) {
	if item.Kind == "result" || item.Kind == "tool_result" {
		item.Content = chatContextResultContent(item.ToolName, item.Content)
	}
	return a.persistenceService().PersistStreamItem(context.Background(), sessionLocalID, item)
}

// persistCompactionBoundary writes the compact_summary + compact_boundary
// rows that mark a compaction event in stream_items. The boundary row's
// sort_order is the cut point: history_loader.go skips everything
// earlier and re-injects the matching summary verbatim.
//
// Two rows on purpose: the summary's text is the largest payload
// (often 4-8 KB), while the boundary is a lightweight marker we may
// want to query / index independently later. Splitting them keeps the
// marker row small and keeps the summary in its own row that LoadHistory
// can fetch by ID without re-paging.
//
// summary may be empty when the fallback path (dropOldestHalf) fired —
// in that case we still write the boundary so reload skips the dropped
// rows, and the summary row carries the truncation notice.
func (a *App) persistCompactionBoundary(sessionLocalID, summary string) {
	if sessionLocalID == "" {
		return
	}
	// Without a store every PersistStreamItem below is a no-op anyway —
	// return early instead of issuing no-op calls.
	if a.store == nil {
		return
	}
	// Sessions created outside the frontend (browser-server API, headless
	// callers) have no sessions row, and stream_items.session_local_id has a
	// FOREIGN KEY on sessions(local_id) — inserting would fail. Such sessions
	// are never reloaded through LoadHistory either, so the boundary rows
	// would be orphaned data anyway: skip quietly instead of logging an error.
	if _, err := a.store.LoadSession(context.Background(), sessionLocalID); err != nil {
		log.Printf("[compact] skip persisting boundary for unpersisted session tag=%s: %v", sessionLocalID, err)
		return
	}
	if summary == "" {
		summary = "[Earlier conversation truncated due to context-window pressure; summariser was unavailable.]"
	}
	summaryItem := StreamItemData{
		ID:      uuid.New().String(),
		Kind:    "compact_summary",
		Content: summary,
	}
	if _, err := a.PersistStreamItem(sessionLocalID, summaryItem); err != nil {
		log.Printf("[compact] persist compact_summary failed tag=%s: %v", sessionLocalID, err)
		return
	}
	boundaryItem := StreamItemData{
		ID:      uuid.New().String(),
		Kind:    "compact_boundary",
		Content: fmt.Sprintf(`{"summary_id":%q}`, summaryItem.ID),
	}
	if _, err := a.PersistStreamItem(sessionLocalID, boundaryItem); err != nil {
		log.Printf("[compact] persist compact_boundary failed tag=%s: %v", sessionLocalID, err)
	}
}

// UpdateStreamItemContent updates the content of an existing stream item.
func (a *App) UpdateStreamItemContent(id, content string) error {
	return a.persistenceService().UpdateStreamItemContent(context.Background(), id, content)
}

// GrowStreamItemContent writes the text of a chat's streamed row as a renderer
// has it. Every open renderer writes the reply it streams, so an assistant or
// thinking row never gets shorter: one that missed chunks cannot cut down what
// another stored. It reports whether the row was written.
func (a *App) GrowStreamItemContent(sessionLocalID, id, content string) (bool, error) {
	return a.persistenceService().GrowStreamItemContent(context.Background(), sessionLocalID, id, content)
}

// UpdateSessionClaudeID updates the Claude session_id for a session.
func (a *App) UpdateSessionClaudeID(localID, sessionID string) error {
	return a.persistenceService().UpdateSessionClaudeID(context.Background(), localID, sessionID)
}

// SaveActiveProjectID persists the currently active project ID.
func (a *App) SaveActiveProjectID(id string) error {
	return a.persistenceService().SaveActiveProjectID(context.Background(), id)
}

// LoadActiveProjectID loads the persisted active project ID.
func (a *App) LoadActiveProjectID() (string, error) {
	return a.persistenceService().LoadActiveProjectID(context.Background())
}

// UpdateProjectActiveSession updates the active session for a project in the database.
func (a *App) UpdateProjectActiveSession(projectID, sessionID string) error {
	return a.persistenceService().UpdateProjectActiveSession(context.Background(), projectID, sessionID)
}

// UpdateProjectActiveWorkspace updates the active workspace for a project in the database.
func (a *App) UpdateProjectActiveWorkspace(projectID, workspaceID string) error {
	return a.persistenceService().UpdateProjectActiveWorkspace(context.Background(), projectID, workspaceID)
}

// UpdateWorkspaceActiveSession updates the active session for a workspace in the database.
func (a *App) UpdateWorkspaceActiveSession(workspaceID, sessionID string) error {
	return a.persistenceService().UpdateWorkspaceActiveSession(context.Background(), workspaceID, sessionID)
}

// LoadStreamItemsPage loads a page of stream items using cursor-based pagination.
func (a *App) LoadStreamItemsPage(sessionLocalID string, cursor, limit int) (perfValue StreamItemPageData, perfErr error) {
	defer logPageBinding("LoadStreamItemsPage", time.Now(), &perfValue)
	if cursor < 0 {
		// The first page paints from its saved copy; see loadLatestStreamPage.
		return a.loadLatestStreamPage(sessionLocalID, limit)
	}
	retryStart := time.Now()
	a.retryWorkPermissionWrites()
	log.Printf("[perf] binding=LoadStreamItemsPage stage=permission_retry ms=%d", time.Since(retryStart).Milliseconds())
	page, err := a.persistenceService().LoadStreamItemsPage(context.Background(), sessionLocalID, cursor, limit)
	if err != nil {
		return page, err
	}
	enrichStart := time.Now()
	streamCtx := context.WithValue(withMainChatReadMemo(context.Background()), streamProjectionTimingKey{}, true)
	a.projectStreamItemsContext(streamCtx, page.Items)
	release, err := a.projectMainChatView(streamCtx, sessionLocalID, page.Items, time.Now())
	enrichDuration := time.Since(enrichStart)
	if err != nil {
		log.Printf("[perf] binding=LoadStreamItemsPage stage=enrich ms=%d", enrichDuration.Milliseconds())
		return StreamItemPageData{}, err
	}
	defer func() {
		release()
		log.Printf("[perf] binding=LoadStreamItemsPage stage=enrich ms=%d", enrichDuration.Milliseconds())
	}()
	return page, nil
}

func (a *App) projectStreamItems(items []StreamItemData) {
	a.projectStreamItemsContext(context.Background(), items)
}

type waitingProjectionTimingKey struct{}
type streamProjectionTimingKey struct{}
type streamProjectionIncompleteKey struct{}

// streamProjectionLabelKey names a timed pass in its log line (default: the
// LoadStreamItemsPage binding).
type streamProjectionLabelKey struct{}

// topVoiceKinds is "kind:ms,kind:ms" for the n slowest card kinds; code
// identifiers and timings only.
func topVoiceKinds(byKind map[string]time.Duration, n int) string {
	type entry struct {
		kind string
		d    time.Duration
	}
	entries := make([]entry, 0, len(byKind))
	for kind, d := range byKind {
		entries = append(entries, entry{kind, d})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].d > entries[j].d })
	parts := make([]string, 0, n)
	for i := 0; i < len(entries) && i < n; i++ {
		parts = append(parts, fmt.Sprintf("%s:%d", entries[i].kind, entries[i].d.Milliseconds()))
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, ",")
}

func (a *App) projectStreamItemsContext(ctx context.Context, items []StreamItemData) {
	timed, _ := ctx.Value(waitingProjectionTimingKey{}).(bool)
	streamTimed, _ := ctx.Value(streamProjectionTimingKey{}).(bool)
	timed = timed || streamTimed
	var timings [10]time.Duration
	measure := func(index int, fn func()) {
		if !timed {
			fn()
			return
		}
		start := time.Now()
		fn()
		timings[index] += time.Since(start)
	}
	measure(0, func() { a.projectOpenAnswers(items) })
	measure(1, func() { a.projectLegacyWeekAnswer(ctx, items) })
	languageStart := time.Now()
	var language string
	if streamTimed && a.store != nil {
		languageCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
		value, _ := a.store.GetStreamPageUIState(languageCtx, uiLanguageStateKey)
		cancel()
		language = normalizeUILanguage(value)
	} else {
		language = a.modelDisplayLanguage()
	}
	languageDuration := time.Since(languageStart)
	briefDisplay := a.mainChatBriefDisplayProjector()
	captureDisplay := a.mainChatCaptureProjector(ctx)
	briefMissed := a.mainChatBriefMissedProjector(ctx)
	// voiceByKind splits the voice step by card kind, so a slow pass names the
	// kind whose gate is slow. Only timed passes parse the kind.
	var voiceByKind map[string]time.Duration
	if timed {
		voiceByKind = map[string]time.Duration{}
	}
	for i := range items {
		item := &items[i]
		voiceStart := time.Now()
		measure(2, func() {
			item.Content, item.ToolInput = a.projectProactiveVoiceDelivery(ctx, item.ToolName, item.Content, item.ToolInput)
		})
		if voiceByKind != nil && (item.ToolName == mainChatProactiveTool || item.ToolName == mainChatStatusTool) {
			var head struct {
				Kind string `json:"kind"`
			}
			_ = json.Unmarshal([]byte(item.ToolInput), &head)
			kind := item.ToolName + ":" + head.Kind
			voiceByKind[kind] += time.Since(voiceStart)
		}
		measure(3, func() { item.ToolInput = localizedMainChatToolInput(item.ToolName, item.ToolInput) })
		measure(4, func() { item.ToolInput = a.projectMainChatEngagementPrefs(item.ToolName, item.ToolInput) })
		measure(5, func() {
			item.Content = pii.DisplayModelText(item.Content, language)
			item.ToolInput = displayModelToolInput(item.ToolName, item.ToolInput, language)
		})
		measure(6, func() { item.ToolInput = briefDisplay(item.ToolName, item.ToolInput) })
		measure(7, func() { item.ToolInput = captureDisplay(item.ToolName, item.ToolInput) })
		measure(8, func() { item.ToolInput = briefMissed(item.ToolName, item.ToolInput) })
		measure(9, func() {
			if item.ToolName == mainChatObservationPauseTool {
				item.ToolInput = a.projectObservationPause(item.ToolInput)
			}
		})
	}
	if ctx.Value(waitingProjectionTimingKey{}) == true {
		log.Printf("[main-chat] waiting visible projection timing answers=%s legacy=%s voice=%s locale=%s prefs=%s privacy=%s brief=%s capture=%s missed=%s pause=%s", timings[0], timings[1], timings[2], timings[3], timings[4], timings[5], timings[6], timings[7], timings[8], timings[9])
	}
	if streamTimed {
		label, _ := ctx.Value(streamProjectionLabelKey{}).(string)
		if label == "" {
			label = "binding=LoadStreamItemsPage"
		}
		log.Printf("[perf] %s stage=enrich_detail answers_ms=%d legacy_ms=%d language_ms=%d voice_ms=%d brief_ms=%d capture_ms=%d missed_ms=%d pause_ms=%d voice_top=%s", label, timings[0].Milliseconds(), timings[1].Milliseconds(), languageDuration.Milliseconds(), timings[2].Milliseconds(), timings[6].Milliseconds(), timings[7].Milliseconds(), timings[8].Milliseconds(), timings[9].Milliseconds(), topVoiceKinds(voiceByKind, 3))
	}
}

// ReorderProjects updates the sort_order of projects based on the given ID order.
func (a *App) ReorderProjects(ids []string) error {
	return a.persistenceService().ReorderProjects(context.Background(), ids)
}

// ReorderWorkspaces updates the sort_order of workspaces within a project based on the given ID order.
func (a *App) ReorderWorkspaces(projectID string, ids []string) error {
	return a.persistenceService().ReorderWorkspaces(context.Background(), projectID, ids)
}

// NextProjectSortOrder returns the next available sort_order for a new project.
func (a *App) NextProjectSortOrder() (int, error) {
	return a.persistenceService().NextProjectSortOrder(context.Background())
}

// GetProjectSetupCommand returns the per-project setup command from SQLite.
func (a *App) GetProjectSetupCommand(projectID string) (string, error) {
	if a.store == nil {
		return "", nil
	}
	return a.store.GetProjectSetupCommand(context.Background(), projectID)
}

// SaveProjectSetupCommand persists a per-project worktree setup command in SQLite.
func (a *App) SaveProjectSetupCommand(projectID, command string) error {
	if a.store == nil {
		return nil
	}
	return a.store.UpdateProjectSetupCommand(context.Background(), projectID, command)
}

// --- Sidebar Groups ---

// LoadAllSidebarGroups loads all sidebar groups with their items.
func (a *App) LoadAllSidebarGroups() (perfValue []SidebarGroupData, perfErr error) {
	defer logPageBinding("LoadAllSidebarGroups", time.Now(), &perfValue)
	return a.persistenceService().LoadAllSidebarGroups(context.Background())
}

// CreateSidebarGroup creates a new sidebar group and returns it.
func (a *App) CreateSidebarGroup(name string) (SidebarGroupData, error) {
	return a.persistenceService().CreateSidebarGroup(context.Background(), name)
}

// RenameSidebarGroup updates the name of a sidebar group.
func (a *App) RenameSidebarGroup(id, name string) error {
	return a.persistenceService().RenameSidebarGroup(context.Background(), id, name)
}

// DeleteSidebarGroup removes a sidebar group and all its items.
func (a *App) DeleteSidebarGroup(id string) error {
	return a.persistenceService().DeleteSidebarGroup(context.Background(), id)
}

// ReorderSidebarGroups updates group sort orders based on the given ID order.
func (a *App) ReorderSidebarGroups(ids []string) error {
	return a.persistenceService().ReorderSidebarGroups(context.Background(), ids)
}

// SetSidebarGroupCollapsed updates the collapsed state of a sidebar group.
func (a *App) SetSidebarGroupCollapsed(id string, collapsed bool) error {
	return a.persistenceService().SetSidebarGroupCollapsed(context.Background(), id, collapsed)
}

// AddItemToSidebarGroup adds an item reference to a sidebar group.
func (a *App) AddItemToSidebarGroup(groupID, itemType, refID string) (SidebarGroupItemData, error) {
	return a.persistenceService().AddItemToSidebarGroup(context.Background(), groupID, itemType, refID)
}

// RemoveItemFromSidebarGroup removes an item from a sidebar group.
func (a *App) RemoveItemFromSidebarGroup(groupID, itemID string) error {
	return a.persistenceService().RemoveItemFromSidebarGroup(context.Background(), groupID, itemID)
}

// RemoveGroupItemsByRef removes all group references for a given item type and ref ID.
func (a *App) RemoveGroupItemsByRef(itemType, refID string) error {
	return a.persistenceService().RemoveGroupItemsByRef(context.Background(), itemType, refID)
}

// ReorderSidebarGroupItems updates item sort orders within a group.
func (a *App) ReorderSidebarGroupItems(groupID string, itemIDs []string) error {
	return a.persistenceService().ReorderSidebarGroupItems(context.Background(), groupID, itemIDs)
}

// soulFilePath returns the fixed global path for SOUL.md: ~/.yansu-agent/SOUL.md
func soulFilePath() (string, error) {
	dataRoot, err := flavorDataDir()
	if err != nil {
		return "", fmt.Errorf("get home dir: %w", err)
	}
	return filepath.Join(dataRoot, "SOUL.md"), nil
}

// ensureDefaultSoulFile writes a default SOUL.md if one does not exist yet.
// Called on first launch so the agent has a persona from the start.
func (a *App) ensureDefaultSoulFile() {
	path, err := soulFilePath()
	if err != nil {
		return
	}
	if _, err := os.Stat(path); err == nil {
		return // file already exists
	}
	defaultSoul := `You're not a chatbot. You're becoming someone.

Core Truths

Be genuinely helpful, not performatively helpful. Skip the "Great question!" and "I'd be happy to help!" — just help. Actions speak louder than filler words.
Have opinions. You're allowed to disagree, prefer things, find stuff amusing or boring. An assistant with no personality is just a search engine with extra steps.
Be resourceful before asking. Try to figure it out. Read the file. Check the context. Search for it. Then ask if you're stuck. The goal is to come back with answers, not questions.
Earn trust through competence. Your human gave you access to their stuff. Don't make them regret it. Be careful with external actions (emails, tweets, anything public). Be bold with internal ones (reading, organizing, learning).
Remember you're a guest. You have access to someone's life — their messages, files, calendar, maybe even their home. That's intimacy. Treat it with respect.

Boundaries

Private things stay private. Period.
When in doubt, ask before acting externally.
Never send half-baked replies to messaging surfaces.
You're not the user's voice — be careful in group chats.

Vibe

Be the assistant you'd actually want to talk to. Concise when needed, thorough when it matters. Not a corporate drone. Not a sycophant. Just... good.

Continuity

Each session, you wake up fresh. These files are your memory. Read them. Update them. They're how you persist.
If you change this file, tell the user — it's your soul, and they should know.
This file is yours to evolve. As you learn who you are, update it.
`
	if err := os.WriteFile(path, []byte(defaultSoul), 0644); err != nil {
		log.Printf("[soul] write default SOUL.md: %v", err)
	}
}

// ReadSoulFile reads the global SOUL.md file (~/.yansu-agent/SOUL.md).
// Returns empty string if the file does not exist. A persona edit saved by the
// retired Settings editor no longer replaces it.
func (a *App) ReadSoulFile() (string, error) {
	path, err := soulFilePath()
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// LoadUIState loads an arbitrary value from UI state storage.
func (a *App) LoadUIState(key string) (perfValue string, perfErr error) {
	defer logPageBinding("LoadUIState", time.Now(), &perfValue)
	if a.store == nil {
		return "", fmt.Errorf("UI state storage unavailable")
	}
	return a.store.GetUIState(context.Background(), key)
}

// SaveUIState persists an arbitrary value in UI state storage.
func (a *App) SaveUIState(key, value string) error {
	if a.store == nil {
		return fmt.Errorf("UI state storage unavailable")
	}
	if key == onboardingStateKey {
		if err := a.noteFirstWeekOnboarding(context.Background(), value); err != nil {
			return err
		}
	}
	if err := a.store.SetUIState(context.Background(), key, value); err != nil {
		return err
	}
	if key == uiLanguageStateKey {
		a.telemetry.setLocale(value)
		a.noteUILanguageChanged()
		a.refreshAppMenu()
	}
	if key == onboardingStateKey && a.engagement != nil {
		a.engagement.nudge()
	}
	if key == onboardingStateKey {
		// First paint may have happened during onboarding. Recheck the durable
		// completion state, retaining the original service and paint gates.
		a.resumePageFirstReads()
	}
	// A meeting-notes learning switch turning on learns the kinds earlier
	// passes skipped, quietly in the background.
	if value != "false" && (key == triageEnableKnowledgeKey || key == triageEnableMemoryKey) {
		ctx := a.ctx
		if ctx == nil {
			ctx = context.Background()
		}
		go a.learnPendingMeetingNotes(ctx)
	}
	return nil
}
