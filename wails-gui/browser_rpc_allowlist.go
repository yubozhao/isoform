package main

import (
	"encoding/json"
	"fmt"
)

// browserRPCAllowlist defines the set of App methods that the /api/call
// reflection-based RPC endpoint is allowed to invoke. Any method not in this
// map is rejected with HTTP 403.
//
// The browser preview exists to look at and click through the interface
// during development. It keeps the reads, the chat and its sidebar (sending,
// streaming, saving and arranging chats) and the interface's own saved
// state. Everything that reaches past the window (installs, credentials,
// consent, billing, updates, git, terminals, connectors, scheduled runs,
// changing observation or privacy settings, deleting or rewriting what Isoform
// learned) stays in the app window. Every state changer below needs a
// one-line reason in browserRPCStateChangers in browser_rpc_allowlist_test.go;
// the test fails for one without. Settings Overview is a read-only summary;
// its pause, resume and dismissal controls remain native-only.
var browserRPCAllowlist = map[string]bool{
	// Search reads the same local content and gates as native navigation.
	"SearchEverything":     true,
	"FindStreamItemCursor": true,

	// Core
	"StartAgent":          true,
	"LoadUIState":         true,
	"SaveUIState":         true, // only the keys in browserUIStateKeys
	"LoadAllProjects":     true,
	"LoadActiveProjectID": true,
	"ListAvailableModels": true,
	"LoadStreamItemsPage": true,
	"GetGitInfo":          true, // runs git; see browserRPCStateChangers
	"GetVersion":          true,
	"GetUpdateStatus":     true,
	"LoadWindowState":     true,
	"GetSlackStatus":      true,
	"GetFeishuStatus":     true,
	"SaveWorkspaceState":  true,

	// Projects
	"SelectWorkDir":                true,
	"FetchProjectContext":          true,
	"DetectProject":                true,
	"CancelStream":                 true,
	"CancelByTag":                  true,
	"RespondToPermission":          true,
	"CancelPermission":             true,
	"SaveProjectState":             true,
	"SaveSessionState":             true,
	"ForkSession":                  true,
	"PersistStreamItem":            true,
	"SaveActiveProjectID":          true,
	"UpdateProjectActiveSession":   true,
	"UpdateProjectActiveWorkspace": true,
	"UpdateWorkspaceActiveSession": true,
	"DetectInstalledApps":          true,
	"UpdateStreamItemContent":      true,
	"GrowStreamItemContent":        true,
	"ReorderProjects":              true,
	"ReorderWorkspaces":            true,
	"NextProjectSortOrder":         true,
	"PrewarmSession":               true,

	// Prompt / Streaming
	"SendPrompt":             true,
	"SendPromptWithImages":   true,
	"EnsureProjectStructure": true,
	"UpdateSessionClaudeID":  true,
	"GetComposerSuggestions": true,

	// Identity and build (there is no sign-in)
	"GetUserProfile": true,
	"GetWebBaseURL":  true,
	"GetAuthSource":  true,
	"HasEnvOverride": true,

	// Remote browsing
	"ListOrganizations": true,
	"ListProducts":      true,
	"ListProjects":      true,

	// Setup reads. Confirmation, permission prompts and probe polling stay native.
	"GetModelChoices":      true,
	"GetModelDownloads":    true,
	"GetModelNetworkState": true,
	"GetModelEvaluation":   true,
	"GetSetupPermissions":  true,

	// Permissions
	"GetPlatformPermissions": true,
	"GetWhatIsoformSees":     true,
	"OpenPermissionSettings": true,
	// RequestPermission is deliberately absent: a macOS permission prompt may
	// only follow a tap in the app window, never a call from the local bridge.
	"GetComputerUseAppAccessSnapshot": true,
	// ProbeComputerUseAppAccess is absent: it starts the cua-driver daemon.

	// Calendars (Settings > Observation). Read-only: the hub's next meetings.
	"GetCalendarOverview":        true,
	"GetFindTimeDetails":         true,
	"GetFindTimeDetailsForRange": true,
	"GetUpcomingCalendarEvents":  true,

	// Reminders (Settings > Observation). Read-only.
	"GetRemindersOverview": true,

	// Recent files (Settings > Observation). Read-only; the private folders
	// and Refresh stay off.
	"GetFilesOverview": true,

	// Mail (Settings > Observation). Read-only; Refresh stays off.
	"GetMailOverview": true,

	// Environment / Settings
	"GetEnvironment":              true,
	"GetUseYansuGateway":          true,
	"GetBrowserBridgeEnabled":     true,
	"GetCuaCursorOverlayEnabled":  true,
	"GetStartAtLogin":             true,
	"ListSkillHubInstalledSkills": true,
	"SearchClawHubSkills":         true,
	"DiscoverAgentSkills":         true,
	"SearchAgentSkills":           true,

	// MCP server hub
	"ListMcpServers":          true,
	"GetMcpServer":            true,
	"GetMcpServerHealth":      true,
	"ListMcpServerTools":      true,
	"TailMcpServerLog":        true,
	"DiscoverLocalMcpServers": true,
	"SearchMcpRegistry":       true,

	// Voice. Read-only: transcribing may download the speech model.
	"GetVoiceInputStatus":      true,
	"GetAudioCaptureEvents":    true,
	"GetBackgroundAudioStatus": true,

	// Subscription
	"GetSubscriptionStatus": true,
	"GetQuotaUsage":         true,

	// PR. GetPRInfo is absent: it runs gh with the user's GitHub sign-in.
	"BuildPRCreateURL": true,

	// Cron and the hub's lists. Read-only: scheduled runs change only in the
	// app window.
	"ListCronJobs":    true,
	"GetCronRuns":     true,
	"LoadCronRunLogs": true,
	"GetUpcoming":     true,
	// Today: the brief, a read. The topic list and the finds stay in the app
	// window: ranking them records the browser journal in topics.json, and
	// finding, feedback and topic changes rewrite what Isoform learned.
	"GetLatestBrief":                     true,
	"GetTodayBriefByID":                  true,
	"PrepareTodayBriefParagraph":         true,
	"GetTodayPanelAnswer":                true,
	"GetTodayMeetingNote":                true,
	"GetTodayBriefDraft":                 true,
	"GetTodayBriefDraftRoute":            true,
	"SaveMorningBriefWeekIntent":         true,
	"PostMorningBriefWeekAnswer":         true,
	"PostMorningBriefWeekAnswerFromChip": true,
	"PostTodayClosingAnswer":             true,
	"OpenTodayReplyDraft":                true,
	"CancelTodayReplyDraft":              true,
	"GetPersonBrief":                     true,
	"OpenPersonReplyDraft":               true,
	"AddTodayNoticedSchedule":            true,
	"UndoTodayNoticedSchedule":           true,
	"EditTodayNoticedSchedule":           true,
	"ChangeMorningBriefWeekIntent":       true,
	"ListSelfModelEntries":               true,
	"GetSelfModelContextPreview":         true,
	"GetSelfDirectiveDetail":             true,
	"GetPurposeOpenLoopProductDetail":    true,
	"ListDeliverables":                   true,
	"RecordChatDeliverables":             true,
	"DeliverableFileInfo":                true,
	"ListDeliverableFileOpenWithApps":    true,
	"ListHandoffs":                       true,
	"GetActiveHandoffChips":              true,

	// Sidebar groups
	"LoadAllSidebarGroups":       true,
	"CreateSidebarGroup":         true,
	"RenameSidebarGroup":         true,
	"DeleteSidebarGroup":         true,
	"ReorderSidebarGroups":       true,
	"SetSidebarGroupCollapsed":   true,
	"AddItemToSidebarGroup":      true,
	"RemoveItemFromSidebarGroup": true,
	"RemoveGroupItemsByRef":      true,
	"ReorderSidebarGroupItems":   true,

	// Chat apps. Read-only: connecting, credentials and daemons stay in the
	// app window.
	"GetSlackChannels":            true,
	"GetSlackScanChannelFilter":   true,
	"TestSlackConnection":         true, // standalone: validate bot+app token
	"GetSlackArchived":            true, // standalone: reusable archived app info
	"GetFeishuChats":              true,
	"GetFeishuReceiverStatus":     true,
	"GetFeishuScanChatFilter":     true,
	"GetDiscordStatus":            true,
	"GetDiscordChannels":          true,
	"GetDiscordScanChannelFilter": true,
	"TestDiscordConnection":       true, // standalone: validate bot token + invite URL
	"GetDiscordInviteURL":         true, // standalone: invite URL for saved bot
	"GetDiscordArchived":          true, // standalone: reusable archived bot info
	"GetTelegramStatus":           true,
	"GetTelegramGroups":           true,
	"TestTelegramConnection":      true, // standalone: validate bot token
	"GetTelegramArchived":         true, // standalone: reusable archived bot info
	"GetWeChatStatus":             true,

	// Memory. Confirmation, like editing and Forget, remains app-window only.
	"GetAboutYou":               true,
	"GetMemoryEvidenceReadable": true,
	"GetEntryUses":              true,
	"GetEntryUseCounts":         true,
	"GetMemoryEvidence":         true,
	"GetMemoryEntries":          true,
	"SearchMemory":              true,
	"GetMemoryConfig":           true, // the API key comes back redacted
	"GetMemoryStatus":           true,
	"GetAIProviderConfig":       true, // the API key comes back redacted
	"ListAIProviderModels":      true,
	"ListAIProviderPresets":     true,
	"GetModelAccountsStatus":    true,
	// Read-only state the Settings pages display (browser preview).
	"GetAutoSpeakAutomationEnabled":             true,
	"GetAutoSpeakEnabled":                       true,
	"GetAutoSpeakNotificationsEnabled":          true,
	"GetAutomaticMeetingRecordingEnabled":       true,
	"GetBackgroundListening":                    true,
	"GetCaptureCounts":                          true,
	"GetCaptureByHour":                          true,
	"GetPrivateAppSuggestions":                  true,
	"GetDoingNow":                               true,
	"GetEngagementSettings":                     true,
	"GetSettingsOverview":                       true,
	"GetQuickChatHotkey":                        true,
	"GetTelemetryTier":                          true,
	"GetVoiceInputApps":                         true,
	"IsClosedEyesModeEnabled":                   true,
	"IsSystemAudioBackgroundListeningSupported": true,
	"CheckDependencies":                         true, // runs status commands; see browserRPCStateChangers
	"IsBYOAEntitled":                            true,
	"ListMemoryVersions":                        true,
	"GetMemoryVersion":                          true,

	// Knowledge
	"ListKnowHow":            true,
	"GetKnowHowMatch":        true,
	"DismissKnowHowMatch":    true,
	"GetKnowledgeEntries":    true,
	"GetKnowledgeEntry":      true,
	"GetKnowledgeForSession": true,
	"ListKnowledgeVersions":  true,
	"GetKnowledgeVersion":    true,

	// Activity
	"GetActivitySessions":            true,
	"GetActivitySessionDetail":       true,
	"ActivitySessionExists":          true,
	"GetActivityOCRMatches":          true,
	"GetSnapshotOCRText":             true,
	"GetOCRHighlightBoxes":           true,
	"GetHostBridgePort":              true,
	"GetHostBridgeMediaToken":        true, // opens /api/activity/* on the host bridge only
	"SemanticSearchActivitySessions": true,
	"GetActivitiesInRange":           true,
	"GetActivityRuns":                true,
	"GetActivityKeywordFrames":       true,
	"GetDailySummary":                true,
	"GetWeeklySummary":               true,
	"GetActivityRetentionDays":       true,
	"GetActivityMaxStorageGB":        true,
	"GetActivityStorageStats":        true,

	// The frontend checks storage before mounting any database-backed hooks.
	"GetStorageStartupStatus": true,

	// AI Memory import. Read-only scan; the import stays in the app window.
	"ScanAIMemories": true,

	// Worktree / Git. The two GetWorkspace* calls run git; see
	// browserRPCStateChangers.
	"GetWorktreeSettings":      true,
	"GetWorkspaceChangedFiles": true,
	"GetWorkspaceFileDiff":     true,
	"ListWorkspaces":           true,

	"GetTriageLogs": true,

	// Read-only activity-processing diagnostics. Manual evaluation and history
	// deletion remain native-only so browser preview cannot change pipeline state.
	"GetActivityPipelineRuns": true,

	// Triage. Read-only: the sensitive-app list changes only in the app window.
	"GetSensitiveApps": true,
	"ListSystemApps":   true,

	// Soul file
	"ReadSoulFile": true,

	// Misc
	"GetBunPath": true,

	// Diagnostics (DevTools → Perf Analysis). Read-only.
	"GetPerfSnapshot":                     true,
	"GetShadowAgentDebugSnapshot":         true,
	"GetBackgroundUnderstandingSnapshot":  true,
	"GetBackgroundUnderstandingRunDetail": true,
	"GetWorkPatternProfileDebugSnapshot":  true,
	"GetWorkPatternProfileDebugDetail":    true,
	"GetSelfDreamDebugSnapshot":           true,
	"GetSelfDreamDebugRun":                true,
	"ListSelfDreamUpdates":                true,
	"GetPurposeDebugSnapshot":             true,
	"GetPurposeDebugOpenLoop":             true,
	"GetPurposeDebugExtractionRun":        true,
	"GetShadowAgentDebugRun":              true,
}

// browserUIStateKeys are the ui_state keys SaveUIState may write from the
// browser: the interface's language, onboarding progress, unread chats.
// The same table holds observation and privacy settings (calendar peek,
// private folders, observation pauses, learning switches), which change only
// in the app window.
var browserUIStateKeys = map[string]bool{
	uiLanguageStateKey:          true, // App.tsx
	onboardingStateKey:          true, // App.tsx
	"yansu.onboarding.step":     true, // onboarding-screen.tsx
	"yansu.hasChattedBefore":    true, // App.tsx
	"yansu.unreadSessionIds.v1": true, // useProjects.ts
}

// browserRedactedSecret stands in for a stored credential in a browser
// response, so the Settings pages can show that a key is set without the key.
const browserRedactedSecret = "********"

// redactBrowserRPCResult removes stored credentials from what an allowlisted
// read returns to the browser.
func redactBrowserRPCResult(method string, result any) any {
	switch method {
	case "GetAIProviderConfig":
		if cfg, ok := result.(AIProviderConfig); ok && cfg.APIKey != "" {
			cfg.APIKey = browserRedactedSecret
			return cfg
		}
	case "GetMemoryConfig":
		if cfg, ok := result.(MemoryStoreConfig); ok && cfg.APIKey != "" {
			cfg.APIKey = browserRedactedSecret
			return cfg
		}
	default:
	}
	return result
}

// checkBrowserRPCArgs narrows an allowlisted method to the arguments the
// browser preview needs.
func checkBrowserRPCArgs(method string, args []json.RawMessage) error {
	switch method {
	case "SaveUIState":
		var key string
		if len(args) == 0 || json.Unmarshal(args[0], &key) != nil || !browserUIStateKeys[key] {
			return fmt.Errorf("SaveUIState: key not writable from the browser")
		}
	default:
	}
	return nil
}
