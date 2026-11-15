package main

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestBrowserPreviewBootstrapMethodsAreAllowlisted(t *testing.T) {
	for _, method := range []string{
		"GetUserProfile",
		"GetStorageStartupStatus",
	} {
		if !browserRPCAllowlist[method] {
			t.Errorf("%s must be browser RPC allowlisted for preview bootstrap", method)
		}
	}
}

// The Yansu sign-in left the product; its bindings must not come back through
// the browser RPC surface.
func TestRemovedSignInMethodsAreNotAllowlisted(t *testing.T) {
	for _, method := range []string{
		"CheckAuth",
		"Login",
		"CancelLogin",
		"Logout",
		"HasSavedToken",
	} {
		if browserRPCAllowlist[method] {
			t.Errorf("%s must not remain a browser RPC method: there is no sign-in", method)
		}
	}
}

func TestObsoleteOnboardingHandoffPreviewMethodsAreNotAllowlisted(t *testing.T) {
	for _, method := range []string{
		"StartOnboardingHandoffPreview",
		"CompleteOnboardingHandoffPreview",
		"ForceOnboardingHandoffPreview",
	} {
		if browserRPCAllowlist[method] {
			t.Errorf("%s must not remain a browser onboarding API", method)
		}
	}
}

// The Agency, the first-run journey and the Desk left the product on
// 2026-10-13; their bindings must not come back through the browser RPC
// surface.
func TestRemovedAgencyMethodsAreNotAllowlisted(t *testing.T) {
	for _, method := range []string{
		"GetAgencySnapshot",
		"DecideAgencyPermission",
		"GetAgencyInitiativeV2Detail",
		"GetAgencyOperatingEnvelopeSnapshot",
		"GetAgencyRuntimeDebugSnapshot",
		"GetAgencyInitiativeOutcomeSourceObservers",
		"SendAgencyContextMessage",
		"StartFirstRunJourney",
		"GetFirstRunDebugSnapshot",
		"ReplayFirstOutcomeDebug",
		"RecordAgencyLearningCorrection",
		"GetPurposeDebugAgencyManifest",
		"GetPurposeDebugOutcomeReceipt",
		"GetSelfDirectiveDebugSnapshot",
	} {
		if browserRPCAllowlist[method] {
			t.Errorf("%s must not remain a browser RPC method: the Agency is gone", method)
		}
	}
}

// Dev Tools in the browser preview may read background understanding; its
// settings, source grants and runs change only in the app window.
func TestBackgroundUnderstandingReadsAreAllowlistedButNotItsControls(t *testing.T) {
	for _, method := range []string{
		"GetBackgroundUnderstandingSnapshot",
		"GetBackgroundUnderstandingRunDetail",
	} {
		if !browserRPCAllowlist[method] {
			t.Errorf("%s must be browser RPC allowlisted for Dev Tools", method)
		}
	}
	for _, method := range []string{
		"SetBackgroundUnderstandingSettings",
		"GrantBackgroundUnderstandingSource",
		"RevokeBackgroundUnderstandingSource",
		"RunBackgroundUnderstandingNow",
	} {
		if browserRPCAllowlist[method] {
			t.Errorf("%s changes state and must stay off the browser allowlist", method)
		}
	}
}

func TestComputerUseAppAccessMethodsAreAllowlistedWithoutComposioOAuth(t *testing.T) {
	if !browserRPCAllowlist["GetComputerUseAppAccessSnapshot"] {
		t.Error("GetComputerUseAppAccessSnapshot must be browser RPC allowlisted")
	}
	for _, method := range []string{
		// Starts the cua-driver daemon.
		"ProbeComputerUseAppAccess",
		"GetComposioStatus",
		"ConnectComposioApp",
		"ActivateComposioApp",
		"CallComposioTool",
		"SaveComposioApiKey",
	} {
		if browserRPCAllowlist[method] {
			t.Errorf("%s must not remain a first-party app access RPC", method)
		}
	}
}

func TestSelfDirectiveReadsAreAllowlistedWithoutWrites(t *testing.T) {
	if !browserRPCAllowlist["GetSelfDirectiveDetail"] {
		t.Error("GetSelfDirectiveDetail must be browser RPC allowlisted for Self")
	}
	for _, method := range []string{
		"CreateSelfDirective",
		"SaveAssistantText",
		"ResetAssistantText",
		"ReviseSelfDirective",
		"RetractSelfDirective",
		"AddSelfModelEntry",
		"PromoteSelfModelFromQualification",
		"AppendSelfDirectiveRevision",
	} {
		if browserRPCAllowlist[method] {
			t.Errorf("%s must not expose an automatic direct-Self write path", method)
		}
	}
}

func TestPurposeReadsAreAllowlistedWithoutMutation(t *testing.T) {
	if !browserRPCAllowlist["GetPurposeOpenLoopProductDetail"] {
		t.Error("GetPurposeOpenLoopProductDetail must be browser RPC allowlisted for Purpose")
	}
	for _, method := range []string{
		"CreatePurposeStatement",
		"RevisePurposeStatement",
		"DismissPurposeStatement",
		"ReopenPurposeStatement",
		"CreatePurposeOpenLoop",
		"AppendPurposeOpenLoopEvent",
		"CreatePurposeRelation",
		"RetractPurposeRelation",
	} {
		if browserRPCAllowlist[method] {
			t.Errorf("%s must not expose raw Purpose ledger mutation", method)
		}
	}
}

func TestActivityPipelineDebuggerExposesReadOnlyHistory(t *testing.T) {
	if !browserRPCAllowlist["GetActivityPipelineRuns"] {
		t.Fatal("activity pipeline history must be browser RPC allowlisted for read-only diagnostics")
	}
	for _, method := range []string{
		"RunActivityPipelineNow",
		"ClearActivityPipelineRuns",
	} {
		if browserRPCAllowlist[method] {
			t.Errorf("%s must remain native-only", method)
		}
	}
}

// A shorter retention deletes recorded activity, so only the app window may
// change it.
func TestActivityRetentionSettingsAreReadOnlyInTheBrowser(t *testing.T) {
	for _, method := range []string{
		"GetActivityRetentionDays",
		"GetActivityMaxStorageGB",
		"GetActivityStorageStats",
	} {
		if !browserRPCAllowlist[method] {
			t.Errorf("%s must be browser RPC allowlisted for Observation settings", method)
		}
	}
	for _, method := range []string{"SetActivityRetentionDays", "SetActivityMaxStorageGB"} {
		if browserRPCAllowlist[method] {
			t.Errorf("%s changes state and must stay off the browser allowlist", method)
		}
	}
}

func TestSettingsReadOnlyStateIsAllowlistedButNotItsSetters(t *testing.T) {
	for _, method := range []string{
		"IsClosedEyesModeEnabled", "GetBackgroundListening", "GetEngagementSettings",
		"GetSettingsOverview",
		"GetTelemetryTier", "GetVoiceInputApps", "GetRemindersOverview", "GetFilesOverview",
		"GetMailOverview",
	} {
		if !browserRPCAllowlist[method] {
			t.Errorf("%s should be allowlisted so the browser preview can show Settings", method)
		}
	}
	for _, method := range []string{
		"SetClosedEyesModeEnabled", "SetBackgroundListening", "SetEngagementRules",
		"PauseObservationFor", "ResumeObservation", "DismissOverviewIssue",
		"SetTelemetryTier", "ConfirmMobilePairing", "SendTestText",
		"SyncFilesNow", "AddFilesPrivateFolder", "RemoveFilesPrivateFolder", "SyncMailNow",
		// The main chat's setup posts lines and saves progress on every read.
		"GetMainChatSetup", "AnswerMainChatSetup", "CloseMainChatSetupSteps",
		"ReopenMainChatSetupSteps",
	} {
		if browserRPCAllowlist[method] {
			t.Errorf("%s changes state and must stay off the browser allowlist", method)
		}
	}
}

// A macOS permission prompt may only follow a tap in the app window, so the
// local browser bridge must never be able to start one.
func TestPermissionPromptsStayOffTheBrowserBridge(t *testing.T) {
	for _, method := range []string{"RequestPermission", "RequestCuaDriverPermissions"} {
		if browserRPCAllowlist[method] {
			t.Errorf("%s can show a macOS prompt and must stay off the browser allowlist", method)
		}
	}
	for _, method := range []string{"GetPlatformPermissions", "OpenPermissionSettings"} {
		if !browserRPCAllowlist[method] {
			t.Errorf("%s should stay allowlisted so the browser preview can show permission state", method)
		}
	}
}

// browserRPCStateChangers is every allowlisted method that changes state or
// acts outside the window, with the reason the browser preview needs it. A
// state changer without a reason comes off the allowlist.
var browserRPCStateChangers = map[string]string{
	// The chat.
	"StartAgent":              "App.tsx starts the agent on boot; the chat cannot run without it",
	"PrewarmSession":          "the chat warms the agent when a chat or model is picked",
	"SendPrompt":              "the composer sends a prompt",
	"SendPromptWithImages":    "the composer sends a prompt with pasted images",
	"CancelStream":            "the composer's stop button",
	"CancelByTag":             "the composer's stop button for a tagged run",
	"RespondToPermission":     "answering the agent's tool approval card in a chat",
	"CancelPermission":        "dismissing the agent's tool approval card in a chat",
	"PersistStreamItem":       "the chat saves each streamed turn from the frontend",
	"UpdateStreamItemContent": "the chat updates a saved message when the user edits it",
	"GrowStreamItemContent":   "the chat grows a saved reply as it streams",
	"UpdateSessionClaudeID":   "the chat keeps the agent session id so the next turn resumes it",
	"RecordChatDeliverables":  "the chat records the files a reply produced (best effort)",

	// The sidebar.
	"SaveProjectState":             "the sidebar saves projects and their chats",
	"SaveSessionState":             "the sidebar saves a chat's title, model and settings",
	"SaveWorkspaceState":           "the sidebar saves a workspace's chats",
	"EnsureProjectStructure":       "the first prompt of a new chat creates its project rows",
	"SaveActiveProjectID":          "the sidebar remembers the open project",
	"UpdateProjectActiveSession":   "the sidebar remembers the open chat",
	"UpdateProjectActiveWorkspace": "the sidebar remembers the open workspace",
	"UpdateWorkspaceActiveSession": "the sidebar remembers the open chat of a workspace",
	"ReorderProjects":              "dragging projects in the sidebar",
	"ReorderWorkspaces":            "dragging workspaces in the sidebar",
	"ForkSession":                  "Branch chat copies a chat into a new one",
	"SelectWorkDir":                "Add project opens the folder picker; nothing changes until a folder is picked",
	"CreateSidebarGroup":           "arranging chats into sidebar groups",
	"RenameSidebarGroup":           "arranging chats into sidebar groups",
	"DeleteSidebarGroup":           "arranging chats into sidebar groups (the chats stay)",
	"ReorderSidebarGroups":         "arranging chats into sidebar groups",
	"SetSidebarGroupCollapsed":     "collapsing a sidebar group",
	"AddItemToSidebarGroup":        "arranging chats into sidebar groups",
	"RemoveItemFromSidebarGroup":   "arranging chats into sidebar groups",
	"RemoveGroupItemsByRef":        "the sidebar drops group entries of a chat that is gone",
	"ReorderSidebarGroupItems":     "arranging chats into sidebar groups",

	// Reads with a side effect.
	"GetUserProfile":         "boot reads the local profile; with telemetry on it identifies the user as the app window does",
	"GetPlatformPermissions": "the permission screens show permission state; with every permission granted it starts the activity monitor as the app window does, within the observation switches",
	"GetWhatIsoformSees":     "the job-level permission sheet shows the same permission state as the phone",
	"GetFeishuStatus":        "Settings shows the Feishu status; it may refresh the stored bot name",
	"SearchMcpRegistry":      "the MCP page searches the registry; it caches the registry listing",
	"GetOCRHighlightBoxes":   "the timeline highlights OCR matches; it caches the computed boxes",

	// Reads that run a program on the Mac.
	"CheckDependencies":        "onboarding and Settings > Tools on this Mac show which tools are installed; it runs their status commands and a login shell to look for npm, and installs nothing",
	"GetGitInfo":               "the sidebar and Add project show a folder's branch and remote; it runs read-only git commands in that folder",
	"GetWorkspaceChangedFiles": "the changes panel lists a workspace's changed files; it runs git diff in the workspace folder and changes nothing",
	"GetWorkspaceFileDiff":     "the changes panel shows one file's diff; it runs git diff in the workspace folder and changes nothing",

	// The interface's own state.
	"SaveUIState":                        "language, onboarding progress, unread chats; checkBrowserRPCArgs refuses other keys",
	"SaveMorningBriefWeekIntent":         "the person's explicit answer to this week's brief question",
	"PostMorningBriefWeekAnswer":         "the person's explicit weekly answer and its chat row",
	"PostMorningBriefWeekAnswerFromChip": "the person's explicit weekly recommendation and its chat row",
	"PostTodayClosingAnswer":             "the person's explicit brief answer and its chat row",
	"OpenTodayReplyDraft":                "the person requests a source-checked unsent reply draft",
	"CancelTodayReplyDraft":              "the person stops a requested draft",
	"OpenPersonReplyDraft":               "the person requests a source-checked unsent reply draft from Who you know",
	"AddTodayNoticedSchedule":            "the person explicitly schedules a current suggestion",
	"UndoTodayNoticedSchedule":           "the person takes back that unchanged schedule",
	"EditTodayNoticedSchedule":           "the person edits that unchanged schedule",
	"ChangeMorningBriefWeekIntent":       "an explicit revision to the currently displayed week answer",
	"DismissKnowHowMatch":                "the Know-how Not the same action persists a dismissed entry/error pair on this computer",
	"OpenPermissionSettings":             "the permission screens' Open Settings button; it opens System Settings and grants nothing",
}

// browserRPCReadOnly is every allowlisted method that only reads: it changes
// nothing on disk, in another app or in an account (in-memory status and
// outbound checks such as TestSlackConnection count as reads).
//
// Debug panels in other files add their reads to the allowlist in init().
var browserRPCReadOnly = []string{
	// Setup snapshots perform no probe, prompt or download.
	"GetModelChoices",
	"GetModelDownloads",
	"GetModelNetworkState",
	"GetModelEvaluation",
	"GetSetupPermissions",

	"ActivitySessionExists",
	"BuildPRCreateURL",
	"DeliverableFileInfo",
	"DetectInstalledApps",
	"DetectProject",
	"DiscoverAgentSkills",
	"DiscoverLocalMcpServers",
	"FetchProjectContext",
	"GetAIProviderConfig",
	"GetActiveHandoffChips",
	"GetActivitiesInRange",
	"GetActivityKeywordFrames",
	"GetActivityMaxStorageGB",
	"GetActivityOCRMatches",
	"GetActivityPipelineRuns",
	"GetActivityRetentionDays",
	"GetActivityRuns",
	"GetActivitySessionDetail",
	"GetActivitySessions",
	"GetActivityStorageStats",
	"GetAudioCaptureEvents",
	"GetAuthSource",
	"GetAutoSpeakAutomationEnabled",
	"GetAutoSpeakEnabled",
	"GetAutoSpeakNotificationsEnabled",
	"GetAutomaticMeetingRecordingEnabled",
	"GetBackgroundAudioStatus",
	"GetBackgroundListening",
	"GetBackgroundUnderstandingRunDetail",
	"GetBackgroundUnderstandingSnapshot",
	"GetBrowserBridgeEnabled",
	"GetBunPath",
	"GetCalendarOverview",
	"GetFindTimeDetails", "GetFindTimeDetailsForRange",
	"GetCaptureCounts",
	"GetCaptureByHour",
	"GetPrivateAppSuggestions",
	"GetComposerSuggestions",
	"GetComputerUseAppAccessSnapshot",
	"GetCronRuns",
	"GetCuaCursorOverlayEnabled",
	"GetDailySummary",
	"GetDiscordArchived",
	"GetDiscordChannels",
	"GetDiscordInviteURL",
	"GetDiscordScanChannelFilter",
	"GetDiscordStatus",
	"GetDoingNow",
	"GetEngagementSettings",
	"GetEnvironment",
	"GetFeishuChats",
	"GetFeishuReceiverStatus",
	"GetFeishuScanChatFilter",
	"GetFilesOverview",
	"GetHostBridgeMediaToken",
	"GetHostBridgePort",
	"ListKnowHow",
	"GetKnowHowMatch",
	"GetKnowledgeEntries",
	"GetKnowledgeEntry",
	"GetKnowledgeForSession",
	"GetKnowledgeVersion",
	"GetLatestBrief",
	"GetTodayBriefByID",
	"PrepareTodayBriefParagraph",
	"GetTodayPanelAnswer",
	"GetTodayMeetingNote",
	"GetTodayBriefDraft",
	// Who you know's Brief me: open loops, the waiting card and the last
	// meeting, with no model run and no write.
	"GetPersonBrief",
	"GetTodayBriefDraftRoute",
	"GetMailOverview",
	"GetMcpServer",
	"GetMcpServerHealth",
	"GetMemoryConfig",
	"GetMemoryContextDebugSnapshot",
	"GetMemoryEntries",
	"GetAboutYou", "GetMemoryEvidenceReadable", "GetEntryUses", "GetEntryUseCounts",
	"GetMemoryEvidence",
	"GetMemoryStatus",
	"GetMemoryVersion",
	"GetModelAccountsStatus",
	"GetPerfSnapshot",
	"GetPurposeDebugExtractionRun",
	"GetPurposeDebugOpenLoop",
	"GetPurposeDebugSnapshot",
	"GetPurposeOpenLoopProductDetail",
	"GetQuickChatHotkey",
	"GetQuotaUsage",
	"GetRemindersOverview",
	"GetSelfDirectiveDetail",
	"GetSelfDreamDebugRun",
	"GetSelfDreamDebugSnapshot",
	"GetSelfModelContextPreview",
	"GetSensitiveApps",
	"GetSettingsOverview",
	"GetShadowAgentDebugRun",
	"GetShadowAgentDebugSnapshot",
	"GetSlackArchived",
	"GetSlackChannels",
	"GetSlackScanChannelFilter",
	"GetSlackStatus",
	"GetSnapshotOCRText",
	"GetStartAtLogin",
	"GetStorageStartupStatus",
	"GetSubscriptionStatus",
	"GetTaskShadowDebugDetail",
	"GetTaskShadowDebugSnapshot",
	"GetTaskShadowObserverCoverageSnapshot",
	"GetTaskShadowObserverDebugDetail",
	"GetTaskShadowObserverDebugSnapshot",
	"GetTelegramArchived",
	"GetTelegramGroups",
	"GetTelegramStatus",
	"GetTelemetryTier",
	"GetTriageLogs",
	"GetUpcoming",
	"GetUpcomingCalendarEvents",
	"GetUpdateStatus",
	"GetUseYansuGateway",
	"GetVersion",
	"GetVoiceInputApps",
	"GetVoiceInputStatus",
	"GetWeChatStatus",
	"GetWebBaseURL",
	"GetWeeklySummary",
	"GetWorkPatternProfileDebugDetail",
	"GetWorkPatternProfileDebugSnapshot",
	"GetWorktreeSettings",
	"HasEnvOverride",
	"IsBYOAEntitled",
	"IsClosedEyesModeEnabled",
	"IsSystemAudioBackgroundListeningSupported",
	"ListAIProviderModels",
	"ListAIProviderPresets",
	"ListAvailableModels",
	"ListCronJobs",
	"ListDeliverableFileOpenWithApps",
	"ListDeliverables",
	"ListHandoffs",
	"ListKnowledgeVersions",
	"ListMcpServerTools",
	"ListMcpServers",
	"ListMemoryVersions",
	"ListOrganizations",
	"ListProducts",
	"ListProjects",
	"ListSelfDreamUpdates",
	"ListSelfModelEntries",
	"ListSkillHubInstalledSkills",
	"ListSystemApps",
	"ListWorkspaces",
	"LoadActiveProjectID",
	"LoadAllProjects",
	"LoadAllSidebarGroups",
	"LoadCronRunLogs",
	"LoadStreamItemsPage",
	"SearchEverything",
	"FindStreamItemCursor",
	"LoadUIState",
	"LoadWindowState",
	"NextProjectSortOrder",
	"ReadSoulFile",
	"ScanAIMemories",
	"SearchAgentSkills",
	"SearchClawHubSkills",
	"SearchMemory",
	"SemanticSearchActivitySessions",
	"TailMcpServerLog",
	"TestDiscordConnection",
	"TestSlackConnection",
	"TestTelegramConnection",
}

func TestBrowserRPCAllowlistNamesAppMethods(t *testing.T) {
	appType := reflect.TypeOf(&App{})
	for method := range browserRPCAllowlist {
		if _, ok := appType.MethodByName(method); !ok {
			t.Errorf("%s is allowlisted but is not an App method", method)
		}
	}
}

// Every allowlisted method is either a read or a state changer with a reason
// the browser preview needs it.
func TestBrowserRPCStateChangersHaveAReason(t *testing.T) {
	reads := make(map[string]bool, len(browserRPCReadOnly))
	for _, method := range browserRPCReadOnly {
		if reads[method] {
			t.Errorf("%s is listed twice as a read", method)
		}
		reads[method] = true
		if !browserRPCAllowlist[method] {
			t.Errorf("%s is listed as a read but is not allowlisted", method)
		}
		if _, ok := browserRPCStateChangers[method]; ok {
			t.Errorf("%s is listed both as a read and as a state changer", method)
		}
	}
	for method, reason := range browserRPCStateChangers {
		if !browserRPCAllowlist[method] {
			t.Errorf("%s has a reason but is not allowlisted", method)
		}
		if reason == "" {
			t.Errorf("%s changes state without a reason; take it off the allowlist", method)
		}
	}
	for method := range browserRPCAllowlist {
		if !reads[method] && browserRPCStateChangers[method] == "" {
			t.Errorf("%s is allowlisted without being classified: add it to browserRPCReadOnly if it only reads, or to browserRPCStateChangers with the reason the browser preview needs it", method)
		}
	}
}

// State changers that reached past the window and came off on 2026-10-15.
func TestBrowserRPCKeepsStateChangersThatReachPastTheWindowOff(t *testing.T) {
	for _, method := range []string{
		// Setup actions: confirmation, files, probes and native permission prompts.
		"SetModelChoice", "PlanModelDownload", "StartModelDownloads", "RetryModelDownload", "ResumeModelDownload", "RemoveModel",
		"GetAgentProbe", "RefreshAgentProbe", "SetSetupWelcomeVisible", "RequestAllPermissions",
		// Scheduled runs, handoffs and approvals.
		"CreateCronJob", "UpdateCronJob", "DeleteCronJob", "RunCronJobNow",
		"AcceptHandoff", "AbortHandoff", "DeleteHandoff", "ResolveAgencyHandoffExecution",
		// A proactive suggestion can approve a schedule or start one-time work.
		"ResolveMainChatProactiveAction", "ResolveTodaySuggestion", "SendTodayMeetingNote", "SendTodayBriefDraft",
		// Installs, terminals, git and updates.
		"InstallCLITool", "InstallSkillFromGitHub", "AddMcpServerManual", "InstallMcpFromRegistry",
		"CreateTerminal", "GitCommit", "GitDiscardFiles", "MergeWorkspace", "CreateWorkspace",
		"RewindSession", "DownloadUpdate", "QuitAndInstall", "ApplyUpdate", "RelaunchApp",
		// Credentials, consent and settings.
		"SaveAIProviderConfig", "AcceptManagedModelConsent", "SaveSlackLocalCredentials",
		"PollWeChatLogin", "SaveWorktreeSettings", "SetBrowserBridgeEnabled", "SetStartAtLogin",
		"AddSensitiveApp", "RemoveSensitiveApp", "SetPermissionEnabled", "WriteSoulFile",
		// Deleting or rewriting chats and what Isoform learned.
		"DeleteSessionData", "DeleteProjectData", "DeleteMemoryEntry", "DeleteKnowledgeEntry",
		"DeleteActivity", "SaveMemoryEntry", "ConfirmMemoryEntry",
		// Turning history reading on starts a scan of local sessions; off forgets
		// what it stored.
		"SetReadAssistantHistory", "RefreshAssistantHistory",
		// Acting on the Mac outside the window.
		"NativeClipboardSetText", "NotifyUser", "OpenDeliverableFileWithApp", "SaveWindowState",
		"CaptureAndSaveWindowState",
		"DumpFocusedWindowAXTree",
		// TestAIProvider runs the command it is given; transcribing may
		// download the speech model.
		"TestAIProvider", "TranscribeAudio", "InitVoiceInput",
		// Run programs or reach the network without the preview needing
		// them: CLI sign-in checks, gh with the user's GitHub sign-in, the
		// update check (it also tells the app window an update is
		// available), and the computer-use probe (it starts the cua-driver
		// daemon).
		"CheckLocalCLIStatus", "GetPRInfo", "CheckForUpdates", "ProbeComputerUseAppAccess",
		// Playbooks: approving one schedules unattended runs in the signed-in
		// browser; email mails a code to an address and sends results.
		"AcceptPlaybookSuggestion", "DeclinePlaybookSuggestion", "SnoozePlaybookSuggestion", "SetPlaybookSchedule", "RunPlaybookNow",
		"SaveSOP", "DeleteSOP", "RunSOP",
		"StartEmailVerification", "ConfirmEmailVerification", "StopEmailChannel",
	} {
		if browserRPCAllowlist[method] {
			t.Errorf("%s reaches past the window and must stay off the browser allowlist", method)
		}
	}
}

// The first-day reads come from this computer's sessions, calendar and
// capture state. They stay native-only until the browser preview needs them.
func TestFirstDayReadsStayOffTheBrowserBridge(t *testing.T) {
	for _, method := range []string{"GetAssistantHistory", "GetReadAssistantHistory", "GetCalendarFirstDay", "GetWatchedToday"} {
		if browserRPCAllowlist[method] {
			t.Errorf("%s reads this computer's history or activity and must stay off the browser allowlist", method)
		}
	}
}

func TestSaveUIStateFromTheBrowserIsLimitedToInterfaceKeys(t *testing.T) {
	args := func(key string) []json.RawMessage {
		raw, _ := json.Marshal(key)
		return []json.RawMessage{raw, json.RawMessage(`"v"`)}
	}
	if browserUIStateKeys["yansu.appearance.texture"] {
		t.Fatal("Retired Texture preference must not be writable from the browser")
	}
	for key := range browserUIStateKeys {
		if err := checkBrowserRPCArgs("SaveUIState", args(key)); err != nil {
			t.Errorf("%s: %v", key, err)
		}
	}
	for _, key := range []string{
		calendarPeekEnabledKey, observationPausesKey, todayObservationPauseStartedKey, filesPrivateFoldersKey,
		mainChatSetupStateKey, semanticEventsEnabledKey, "", "ui.language ",
	} {
		if err := checkBrowserRPCArgs("SaveUIState", args(key)); err == nil {
			t.Errorf("SaveUIState(%q) must be refused from the browser", key)
		}
	}
	if err := checkBrowserRPCArgs("SaveUIState", nil); err == nil {
		t.Error("SaveUIState without arguments must be refused")
	}
	if err := checkBrowserRPCArgs("SaveUIState", []json.RawMessage{json.RawMessage(`42`)}); err == nil {
		t.Error("SaveUIState with a non-string key must be refused")
	}
	if err := checkBrowserRPCArgs("GetVersion", nil); err != nil {
		t.Errorf("other methods are not narrowed: %v", err)
	}
}

func TestBrowserRPCRedactsStoredAPIKeys(t *testing.T) {
	ai := redactBrowserRPCResult("GetAIProviderConfig", AIProviderConfig{Provider: "openai", APIKey: "sk-secret"}).(AIProviderConfig)
	if ai.APIKey != browserRedactedSecret || ai.Provider != "openai" {
		t.Fatalf("GetAIProviderConfig = %+v", ai)
	}
	if empty := redactBrowserRPCResult("GetAIProviderConfig", AIProviderConfig{}).(AIProviderConfig); empty.APIKey != "" {
		t.Fatalf("an unset key must stay empty, got %q", empty.APIKey)
	}
	mem := redactBrowserRPCResult("GetMemoryConfig", MemoryStoreConfig{APIKey: "sk-secret"}).(MemoryStoreConfig)
	if mem.APIKey != browserRedactedSecret {
		t.Fatalf("GetMemoryConfig = %+v", mem)
	}
	if got := redactBrowserRPCResult("GetVersion", "v1"); got != "v1" {
		t.Fatalf("other results pass through, got %v", got)
	}
}
