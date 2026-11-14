/* eslint-disable */
// This file is auto-shaped: the named exports below mirror every method
// the main UI's frontend imports from `wailsjs/go/main/App`. The vite
// plugin in vite.config.ts redirects those imports to this module so
// reused main-UI components compile in the spotlight project.
//
// Methods the spotlight binary actually exposes (GetConfig,
// HideWindow, ResizeMode) re-export the real binding. Everything else
// is a logged no-op returning `Promise<undefined>` — sufficient to
// keep main-UI hooks (useSubscription, useUserProfile, etc.) running
// without throwing in a spotlight context.
//
// If main UI adds new bindings, the build will fail with
// `"X" is not exported by wails-app-stub.ts`. Append the new symbol
// here with `export const X = makeNoop("X");` to fix.

// Path is intentionally lowercase ("app", not "App"): the spotlight's
// Go binding struct is `type app struct` in main.go, so Wails generates
// `wailsjs/go/main/app.{d.ts,js}`. The main UI binds `type App struct`
// which generates uppercase `App.{d.ts,js}`. Importing "App" here used
// to resolve on macOS (case-insensitive FS) but broke the Linux build
// (case-sensitive FS) with "Could not resolve" during vite build.
import { transcribeAudio, initVoiceInput } from "../api/yansu";

// wailsBinding invokes a Wails-injected app binding (window.go.main.app.X)
// directly, so the macOS native build needs no generated wailsjs/ files.
// Only reached on the Windows/Linux (Wails) fallback path; on macOS the
// six real methods short-circuit to the native bridge before this runs.
function wailsBinding<T = void>(name: string, ...args: unknown[]): Promise<T> {
    const fn = (
        window as unknown as {
            go?: { main?: { app?: Record<string, (...a: unknown[]) => Promise<T>> } };
        }
    ).go?.main?.app?.[name];
    if (typeof fn === "function") return fn(...args);
    return Promise.resolve(undefined as T);
}

let warned = new Set<string>();
function makeNoop(name: string): (...args: unknown[]) => Promise<unknown> {
    return async (..._args: unknown[]) => {
        if (!warned.has(name)) {
            warned.add(name);
            console.debug(`[spotlight] noop call: ${name}() — main UI binding not available in spotlight binary`);
        }
        return undefined;
    };
}

export const ParseBrainDump = makeNoop("ParseBrainDump");
export const GetAssistantSignOut = makeNoop("GetAssistantSignOut");
export const AddBrainDump = makeNoop("AddBrainDump");

// Optimistic-value stubs for bindings that the reused main UI hooks
// branch on. Spotlight is spawned by an already-authenticated Yansu
// process; gating the chat surface on a "Free plan" upsell here is
// wrong. Returning the same shape the main UI hook expects but with
// is_active=true keeps useSubscription from rendering Composer's
// "Lite" / free-plan upsell inside the spotlight.
function stubSubscriptionActive(): Promise<{ is_active: boolean; subscription_plan: string }> {
    return Promise.resolve({ is_active: true, subscription_plan: "max" });
}

function stubAuthSource(): Promise<{ source: string }> {
    return Promise.resolve({ source: "" });
}

// Voice-input stubs that round-trip through Yansu's host bridge so
// the reused main-UI useVoiceInput hook (and the Composer's mic
// button) get the SAME offline sherpa-onnx engine the main composer
// uses. spotlight WebView2 doesn't have the cgo binding itself.
function bridgeTranscribeAudio(wavBase64: string): Promise<string> {
    return transcribeAudio(wavBase64);
}

function bridgeInitVoiceInput(): Promise<void> {
    return initVoiceInput();
}

// Optimistic ready status — matches the VoiceInputStatus shape the
// main UI's useVoiceInput expects ({available, ready, downloading,
// building, error}). We assume the main Yansu binary already loaded
// sherpa-onnx; that's where transcription actually runs (host bridge
// forwards). If we returned the wrong shape (e.g. {state, error}),
// `status.ready` reads undefined → useVoiceInput thinks the engine
// isn't ready and triggers `InitVoiceInput()` → state stays
// "initializing" → Composer's mic button renders a loading spinner
// instead of a mic glyph. That was the bug the user reported.
function stubVoiceInputStatus(): Promise<{
    available: boolean;
    ready: boolean;
    downloading: boolean;
    building: boolean;
    error: string;
}> {
    return Promise.resolve({
        available: true,
        ready: true,
        downloading: false,
        building: false,
        error: "",
    });
}

// Platform permissions (used by useVoiceInput to gate the mic). Main
// UI's PlatformPermissions is { platform, items: [{key, granted,
// actionable, ...}] }. Returning `undefined` (the makeNoop default)
// crashes the hook with "Cannot read properties of undefined
// (reading 'items')" — that's the error the user saw.
//
// Windows doesn't expose actionable mic-permission items (the OS-level
// gate is handled by the Edge WebView2 prompt that triggers on the
// first getUserMedia call), so we return an empty items list. The
// hook's branch `if (!microphoneItem || !microphoneItem.actionable)
// return true` then skips the explicit RequestPermission roundtrip.
function stubPlatformPermissions(): Promise<{
    platform: string;
    items: Array<{ key: string; granted: boolean; actionable: boolean }>;
}> {
    return Promise.resolve({ platform: "windows", items: [] });
}

// RequestPermission is only reached on macOS (where mic access is
// gated by TCC). On Windows the hook short-circuits before this is
// called, but we still need a non-undefined return so the
// downstream `await` doesn't crash if the codepath ever runs.
function stubRequestPermission(): Promise<boolean> {
    return Promise.resolve(true);
}

// Real spotlight bindings.
//
// Two transports depending on the host shell:
//   - macOS: the helper is a native NSPanel + WKWebView (no Wails). The
//     three window ops post to the `spotlight` WKScriptMessageHandler;
//     GetConfig reads the injected window.__spotlightConfig; the Windows-
//     only DWM ops are no-ops.
//   - Windows/Linux: the helper is still a Wails app, so we fall back to
//     the generated Wails bindings.
// Detection is runtime (same dist ships to both), per the macOS/Windows
// "separate implementations" decision.
interface SpotlightMessageHandler {
    postMessage(message: unknown): void;
}

function nativeSpotlightBridge(): SpotlightMessageHandler | null {
    const w = window as unknown as {
        webkit?: { messageHandlers?: { spotlight?: SpotlightMessageHandler } };
    };
    return w.webkit?.messageHandlers?.spotlight ?? null;
}

export async function GetConfig(): Promise<{ yansuPort: number; token: string }> {
    const cfg = (
        window as unknown as {
            __spotlightConfig?: { yansuPort?: number; token?: string };
        }
    ).__spotlightConfig;
    if (cfg) {
        return { yansuPort: Number(cfg.yansuPort ?? 0), token: String(cfg.token ?? "") };
    }
    return wailsBinding<{ yansuPort: number; token: string }>("GetConfig");
}

export async function HideWindow(): Promise<void> {
    const bridge = nativeSpotlightBridge();
    if (bridge) {
        bridge.postMessage({ type: "hide" });
        return;
    }
    return wailsBinding("HideWindow");
}

export async function ResizeMode(mode: string): Promise<void> {
    const bridge = nativeSpotlightBridge();
    if (bridge) {
        bridge.postMessage({ type: "resize", mode });
        return;
    }
    return wailsBinding("ResizeMode", mode);
}

export async function SetAutoHideOnBlur(enabled: boolean): Promise<void> {
    const bridge = nativeSpotlightBridge();
    if (bridge) {
        bridge.postMessage({ type: "autohide", enabled });
        return;
    }
    return wailsBinding("SetAutoHideOnBlur", enabled);
}

// macOS: no DWM frame to tint — the native window is transparent. Windows:
// forward to the Wails binding.
export async function SetDarkMode(dark: boolean): Promise<void> {
    if (nativeSpotlightBridge()) return;
    return wailsBinding("SetDarkMode", dark);
}

// macOS: the native window background is fully transparent, so painting a
// solid surface colour is both unnecessary and harmful. Windows: forward.
export async function SetSurfaceColor(r: number, g: number, b: number): Promise<void> {
    if (nativeSpotlightBridge()) return;
    return wailsBinding("SetSurfaceColor", r, g, b);
}

// No-op stubs for every main UI method. Generated from
// `wails-gui/frontend/wailsjs/go/main/App.d.ts`.
// Returns false so markdown-content's "is this a clickable
// activity-session ref?" probe resolves cleanly to "missing" — the
// spotlight has no activity-session jump UI anyway.
export const ActivitySessionExists = async (_id: string): Promise<boolean> => false;
export const AddItemToSidebarGroup = makeNoop("AddItemToSidebarGroup");
export const AddSensitiveApp = makeNoop("AddSensitiveApp");
export const AddTelegramGroup = makeNoop("AddTelegramGroup");
export const AttachQuickChatContext = makeNoop("AttachQuickChatContext");
export const BuildPRCreateURL = makeNoop("BuildPRCreateURL");
export const CancelByTag = makeNoop("CancelByTag");
export const CancelLogin = makeNoop("CancelLogin");
export const CancelPendingDowngrade = makeNoop("CancelPendingDowngrade");
export const CancelPermission = makeNoop("CancelPermission");
export const CancelStream = makeNoop("CancelStream");
export const ChangePlan = makeNoop("ChangePlan");
export const CheckAuth = makeNoop("CheckAuth");
export const CheckForUpdates = makeNoop("CheckForUpdates");
export const CleanupSessionTempDir = makeNoop("CleanupSessionTempDir");
export const ClearActivityPipelineRuns = makeNoop("ClearActivityPipelineRuns");
export const CloneFromURL = makeNoop("CloneFromURL");
export const CloneProject = makeNoop("CloneProject");
export const CloseQuickChatWindow = makeNoop("CloseQuickChatWindow");
export const CloseTerminal = makeNoop("CloseTerminal");
export const ConfirmActivityThreadLink = makeNoop("ConfirmActivityThreadLink");
export const ConnectDiscord = makeNoop("ConnectDiscord");
export const ConnectMCPReadAssistant = makeNoop("ConnectMCPReadAssistant");
export const ConnectSlack = makeNoop("ConnectSlack");
export const ConnectTelegram = makeNoop("ConnectTelegram");
export const ConnectWeChat = makeNoop("ConnectWeChat");
export const CreateBillingPortalSession = makeNoop("CreateBillingPortalSession");
export const CreateCronJob = makeNoop("CreateCronJob");
export const CreateSidebarGroup = makeNoop("CreateSidebarGroup");
export const CreateTerminal = makeNoop("CreateTerminal");
export const CreateWorkspace = makeNoop("CreateWorkspace");
export const DeleteCronJob = makeNoop("DeleteCronJob");
export const DeleteDiscordPipelineRun = makeNoop("DeleteDiscordPipelineRun");
export const DeleteKnowledgeEntry = makeNoop("DeleteKnowledgeEntry");
export const DeleteMemoryEntry = makeNoop("DeleteMemoryEntry");
export const DeletePipelineRun = makeNoop("DeletePipelineRun");
export const DeleteProjectData = makeNoop("DeleteProjectData");
export const DeleteSessionData = makeNoop("DeleteSessionData");
export const DeleteSidebarGroup = makeNoop("DeleteSidebarGroup");
export const DeleteTelegramGroup = makeNoop("DeleteTelegramGroup");
export const DeleteTelegramPipelineRun = makeNoop("DeleteTelegramPipelineRun");
export const DeleteWorkspaceData = makeNoop("DeleteWorkspaceData");
export const DetectInstalledApps = makeNoop("DetectInstalledApps");
export const DetectProject = makeNoop("DetectProject");
export const DisconnectDiscordGuild = makeNoop("DisconnectDiscordGuild");
export const DisconnectFeishu = makeNoop("DisconnectFeishu");
export const DisconnectSlack = makeNoop("DisconnectSlack");
export const DisconnectTelegram = makeNoop("DisconnectTelegram");
export const DisconnectWeChat = makeNoop("DisconnectWeChat");
export const DiscoverAgentSkills = makeNoop("DiscoverAgentSkills");
export const DownloadUpdate = makeNoop("DownloadUpdate");
export const EnsureDailySummary = makeNoop("EnsureDailySummary");
export const EnsureProjectStructure = makeNoop("EnsureProjectStructure");
export const EnsureTranscriptChatMessage = makeNoop("EnsureTranscriptChatMessage");
export const EnsureWeeklySummary = makeNoop("EnsureWeeklySummary");
export const FetchProjectContext = makeNoop("FetchProjectContext");
export const GenerateAutomationName = makeNoop("GenerateAutomationName");
export const GenerateDailySummary = makeNoop("GenerateDailySummary");
export const GenerateTelegramBindToken = makeNoop("GenerateTelegramBindToken");
export const GenerateWeeklySummary = makeNoop("GenerateWeeklySummary");
export const GetActivityMaxStorageGB = makeNoop("GetActivityMaxStorageGB");
export const GetActivityMonitorStatus = makeNoop("GetActivityMonitorStatus");
export const GetActivityOCRMatches = makeNoop("GetActivityOCRMatches");
export const GetActivityPipelineRuns = makeNoop("GetActivityPipelineRuns");
export const GetActivityRetentionDays = makeNoop("GetActivityRetentionDays");
export const GetActivitySessionDetail = makeNoop("GetActivitySessionDetail");
export const GetActivitySessions = makeNoop("GetActivitySessions");
export const GetActivityStorageStats = makeNoop("GetActivityStorageStats");
export const GetAudioCaptureEvents = makeNoop("GetAudioCaptureEvents");
export const GetAuthSource = stubAuthSource;
// TTS read-aloud is a main-UI chat affordance (sherpa MeloTTS). The
// spotlight binary has no local TTS engine, so report auto-speak off and
// synthesize nothing — the reused stream-card's read-aloud button degrades
// to a no-op rather than crashing the build/runtime.
export const GetAutoSpeakEnabled = async (): Promise<boolean> => false;
export const GetAutoSpeakNotificationsEnabled = async (): Promise<boolean> => false;
export const GetBackgroundAudioStatus = makeNoop("GetBackgroundAudioStatus");
export const GetBackgroundListening = makeNoop("GetBackgroundListening");
export const GetBrowserBridgeEnabled = makeNoop("GetBrowserBridgeEnabled");
export const GetBunPath = makeNoop("GetBunPath");
export const GetComposerSuggestions = makeNoop("GetComposerSuggestions");
export const GetCronJob = makeNoop("GetCronJob");
export const GetCronRuns = makeNoop("GetCronRuns");
export const GetCronRunsByDateRange = makeNoop("GetCronRunsByDateRange");
export const GetDailySummary = makeNoop("GetDailySummary");
export const GetDayThreads = makeNoop("GetDayThreads");
export const GetDiscordChannels = makeNoop("GetDiscordChannels");
export const GetDiscordDynamicScanHistory = makeNoop("GetDiscordDynamicScanHistory");
export const GetDiscordPipelineConfig = makeNoop("GetDiscordPipelineConfig");
export const GetDiscordPipelineHistory = makeNoop("GetDiscordPipelineHistory");
export const GetDiscordScanChannelFilter = makeNoop("GetDiscordScanChannelFilter");
export const GetDiscordScanStatus = makeNoop("GetDiscordScanStatus");
export const GetDiscordStatus = makeNoop("GetDiscordStatus");
export const GetDynamicScanHistory = makeNoop("GetDynamicScanHistory");
export const GetEnvironment = makeNoop("GetEnvironment");
export const GetExtractorFullOutputByRunID = makeNoop("GetExtractorFullOutputByRunID");
export const GetExtractorRecordCountsByDateRange = makeNoop("GetExtractorRecordCountsByDateRange");
export const GetExtractorRecordDetail = makeNoop("GetExtractorRecordDetail");
export const GetExtractorRecordsByDate = makeNoop("GetExtractorRecordsByDate");
export const GetExtractorRunLog = makeNoop("GetExtractorRunLog");
export const GetFeishuChats = makeNoop("GetFeishuChats");
export const GetFeishuDynamicScanHistory = makeNoop("GetFeishuDynamicScanHistory");
export const GetFeishuReceiverStatus = makeNoop("GetFeishuReceiverStatus");
export const GetFeishuScanChatFilter = makeNoop("GetFeishuScanChatFilter");
export const GetFeishuStatus = makeNoop("GetFeishuStatus");
export const GetGitInfo = makeNoop("GetGitInfo");
export const GetHostBridgeMediaToken = makeNoop("GetHostBridgeMediaToken");
export const GetHostBridgePort = makeNoop("GetHostBridgePort");
export const GetKnowledgeEntries = makeNoop("GetKnowledgeEntries");
export const GetKnowledgeForSession = makeNoop("GetKnowledgeForSession");
export const GetMeetingTranscript = makeNoop("GetMeetingTranscript");
export const GetMemoryConfig = makeNoop("GetMemoryConfig");
export const GetMemoryEntries = makeNoop("GetMemoryEntries");
export const GetMemoryEvidence = makeNoop("GetMemoryEvidence");
export const GetMemoryEntry = makeNoop("GetMemoryEntry");
export const GetMemoryStatus = makeNoop("GetMemoryStatus");
export const GetMemoryVersion = makeNoop("GetMemoryVersion");
export const GetOCRHighlightBoxes = makeNoop("GetOCRHighlightBoxes");
export const GetPRInfo = makeNoop("GetPRInfo");
export const GetPerfSnapshot = makeNoop("GetPerfSnapshot");
export const GetPipelineConfig = makeNoop("GetPipelineConfig");
export const GetPipelineHistory = makeNoop("GetPipelineHistory");
// Override: return an empty permissions list so useVoiceInput's
// `permissions.items.find(...)` doesn't crash. See
// stubPlatformPermissions for the why.
export const GetPlatformPermissions = stubPlatformPermissions;
export const GetProjectSetupCommand = makeNoop("GetProjectSetupCommand");
export const GetQuickChatSuggestionsForApp = makeNoop("GetQuickChatSuggestionsForApp");
export const GetQuotaUsage = makeNoop("GetQuotaUsage");
export const GetRecommendedQuestions = makeNoop("GetRecommendedQuestions");
export const GetSensitiveApps = makeNoop("GetSensitiveApps");
export const GetSlackChannels = makeNoop("GetSlackChannels");
export const GetSlackKnownMemberChannels = makeNoop("GetSlackKnownMemberChannels");
export const GetSlackScanChannelFilter = makeNoop("GetSlackScanChannelFilter");
export const GetSlackStatus = makeNoop("GetSlackStatus");
export const GetStartAtLogin = makeNoop("GetStartAtLogin");
export const GetStatus = makeNoop("GetStatus");
// Override: optimistic Max-plan response so Composer doesn't render
// the free-plan "Lite" upsell badge inside the spotlight. See
// stubSubscriptionActive above for the why.
export const GetSubscriptionStatus = stubSubscriptionActive;
export const GetTelegramDynamicScanHistory = makeNoop("GetTelegramDynamicScanHistory");
export const GetTelegramGroups = makeNoop("GetTelegramGroups");
export const GetTelegramPipelineConfig = makeNoop("GetTelegramPipelineConfig");
export const GetTelegramPipelineHistory = makeNoop("GetTelegramPipelineHistory");
export const GetTelegramStatus = makeNoop("GetTelegramStatus");
export const GetTriageLogs = makeNoop("GetTriageLogs");
export const GetUpdateStatus = makeNoop("GetUpdateStatus");
export const GetUseYansuGateway = makeNoop("GetUseYansuGateway");
export const GetUserProfile = makeNoop("GetUserProfile");
export const GetVersion = makeNoop("GetVersion");
export const GetVoiceInputStatus = stubVoiceInputStatus;
export const GetWeChatStatus = makeNoop("GetWeChatStatus");
export const GetWebBaseURL = makeNoop("GetWebBaseURL");
export const GetWeeklySummary = makeNoop("GetWeeklySummary");
export const GetWorkspaceChangedFiles = makeNoop("GetWorkspaceChangedFiles");
export const GetWorkspaceFileDiff = makeNoop("GetWorkspaceFileDiff");
export const GetWorktreeSettings = makeNoop("GetWorktreeSettings");
export const GitCommit = makeNoop("GitCommit");
export const GitDiscardFiles = makeNoop("GitDiscardFiles");
export const GitStageFiles = makeNoop("GitStageFiles");
export const GitUnstageFiles = makeNoop("GitUnstageFiles");
export const HasEnvOverride = makeNoop("HasEnvOverride");
export const HasLocalCredentials = makeNoop("HasLocalCredentials");
export const ImportAIMemories = makeNoop("ImportAIMemories");
export const ImportAIMemoryText = makeNoop("ImportAIMemoryText");
// Override: route to host bridge.
export const InitVoiceInput = bridgeInitVoiceInput;
export const InstallClawHubSkill = makeNoop("InstallClawHubSkill");
export const InstallDiscordBot = makeNoop("InstallDiscordBot");
export const InstallSkillFromAgentDir = makeNoop("InstallSkillFromAgentDir");
export const InstallSkillFromGitHub = makeNoop("InstallSkillFromGitHub");
export const InstallSkillFromLocal = makeNoop("InstallSkillFromLocal");
export const IsGitRepo = makeNoop("IsGitRepo");
export const IsSystemAudioBackgroundListeningSupported = makeNoop("IsSystemAudioBackgroundListeningSupported");
export const JoinSlackChannel = makeNoop("JoinSlackChannel");
export const ListAvailableModels = makeNoop("ListAvailableModels");
export const ListCronJobs = makeNoop("ListCronJobs");
export const ListMemoryVersions = makeNoop("ListMemoryVersions");
export const ListOrganizations = makeNoop("ListOrganizations");
export const ListProducts = makeNoop("ListProducts");
export const ListProjects = makeNoop("ListProjects");
export const ListSkillHubInstalledSkills = makeNoop("ListSkillHubInstalledSkills");
export const ListSystemApps = makeNoop("ListSystemApps");
export const ListTerminals = makeNoop("ListTerminals");
export const ListWorkspaces = makeNoop("ListWorkspaces");
export const LoadActiveProjectID = makeNoop("LoadActiveProjectID");
export const LoadAllProjects = makeNoop("LoadAllProjects");
export const LoadAllSidebarGroups = makeNoop("LoadAllSidebarGroups");
export const LoadCronRunLogs = makeNoop("LoadCronRunLogs");
export const LoadStreamItemsPage = makeNoop("LoadStreamItemsPage");
export const LoadUIState = makeNoop("LoadUIState");
export const LoadWindowState = makeNoop("LoadWindowState");
export const Login = makeNoop("Login");
export const Logout = makeNoop("Logout");
export const MergeWorkspace = makeNoop("MergeWorkspace");
export const MoveActivityToThread = makeNoop("MoveActivityToThread");
export const NativeClipboardSetText = makeNoop("NativeClipboardSetText");
export const NextProjectSortOrder = makeNoop("NextProjectSortOrder");
export const NotifyUser = makeNoop("NotifyUser");
export const OpenAutomationDeliverable = makeNoop("OpenAutomationDeliverable");
export const OpenCheckout = makeNoop("OpenCheckout");
export const OpenExternalURL = makeNoop("OpenExternalURL");
export const OpenExtractorDeliverable = makeNoop("OpenExtractorDeliverable");
export const OpenInApp = makeNoop("OpenInApp");
export const OpenPermissionSettings = makeNoop("OpenPermissionSettings");
export const OpenPermissionSettingsFromRect = makeNoop("OpenPermissionSettingsFromRect");
export const OpenProjectFolder = makeNoop("OpenProjectFolder");
export const OpenProjectURL = makeNoop("OpenProjectURL");
export const OpenScanStageFolder = makeNoop("OpenScanStageFolder");
export const OpenSkillHubSkillFolder = makeNoop("OpenSkillHubSkillFolder");
export const PatchSkillHubSkill = makeNoop("PatchSkillHubSkill");
export const PersistStreamItem = makeNoop("PersistStreamItem");
export const PollWeChatLogin = makeNoop("PollWeChatLogin");
export const PrewarmSession = makeNoop("PrewarmSession");
export const PullKnowledge = makeNoop("PullKnowledge");
export const PushKnowledge = makeNoop("PushKnowledge");
export const QuitAndInstall = makeNoop("QuitAndInstall");
export async function ReadCuaScreenshotBase64(_path: string): Promise<string> {
    return "";
}
export const ReadSoulFile = makeNoop("ReadSoulFile");
export const RebuildApprovalChat = makeNoop("RebuildApprovalChat");
export const RegenerateComposerSuggestions = makeNoop("RegenerateComposerSuggestions");
export const RemoveGroupItemsByRef = makeNoop("RemoveGroupItemsByRef");
export const RemoveItemFromSidebarGroup = makeNoop("RemoveItemFromSidebarGroup");
export const RemoveSensitiveApp = makeNoop("RemoveSensitiveApp");
export const RemoveWorkspace = makeNoop("RemoveWorkspace");
export const RenameActivityThread = makeNoop("RenameActivityThread");
export const RenameSidebarGroup = makeNoop("RenameSidebarGroup");
export const RenameWorkspaceBranch = makeNoop("RenameWorkspaceBranch");
export const ReorderCronJobs = makeNoop("ReorderCronJobs");
export const ReorderProjects = makeNoop("ReorderProjects");
export const ReorderSidebarGroupItems = makeNoop("ReorderSidebarGroupItems");
export const ReorderSidebarGroups = makeNoop("ReorderSidebarGroups");
export const ReorderWorkspaces = makeNoop("ReorderWorkspaces");
export const RequestPermission = stubRequestPermission;
export const RescanDiscord = makeNoop("RescanDiscord");
export const RescanFeishu = makeNoop("RescanFeishu");
export const RescanSlack = makeNoop("RescanSlack");
export const RescanTelegram = makeNoop("RescanTelegram");
export const ResizeTerminal = makeNoop("ResizeTerminal");
export const ResolveUpcomingConfirm = makeNoop("ResolveUpcomingConfirm");
export const RespondToPermission = makeNoop("RespondToPermission");
export const RestartAgent = makeNoop("RestartAgent");
export const RestartFeishuReceiver = makeNoop("RestartFeishuReceiver");
export const RestoreApprovalChat = makeNoop("RestoreApprovalChat");
export const RetryDiscordPipelineRun = makeNoop("RetryDiscordPipelineRun");
export const RetryPipelineRun = makeNoop("RetryPipelineRun");
export const RetryTelegramPipelineRun = makeNoop("RetryTelegramPipelineRun");
export const RewindSession = makeNoop("RewindSession");
export const RunActivityPipelineNow = makeNoop("RunActivityPipelineNow");
export const RunCronJobNow = makeNoop("RunCronJobNow");
export const RunDiscordScanPipeline = makeNoop("RunDiscordScanPipeline");
export const RunFeishuScanPipeline = makeNoop("RunFeishuScanPipeline");
export const SaveActiveProjectID = makeNoop("SaveActiveProjectID");
export const SaveMemoryConfig = makeNoop("SaveMemoryConfig");
export const SaveMemoryDiary = makeNoop("SaveMemoryDiary");
export const SaveMemoryEntry = makeNoop("SaveMemoryEntry");
export const SaveProjectSetupCommand = makeNoop("SaveProjectSetupCommand");
export const SaveProjectState = makeNoop("SaveProjectState");
export const SaveSessionState = makeNoop("SaveSessionState");
export const SaveUIState = makeNoop("SaveUIState");
export const SaveWindowState = makeNoop("SaveWindowState");
export const CaptureAndSaveWindowState = makeNoop("CaptureAndSaveWindowState");
export const SaveWorkspaceState = makeNoop("SaveWorkspaceState");
export const ScanAIMemories = makeNoop("ScanAIMemories");
export const SearchAgentSkills = makeNoop("SearchAgentSkills");
export const SearchClawHubSkills = makeNoop("SearchClawHubSkills");
export const SearchMemory = makeNoop("SearchMemory");
export const SelectLocalSkillDir = makeNoop("SelectLocalSkillDir");
export const SelectWorkDir = makeNoop("SelectWorkDir");
export const SemanticSearchActivitySessions = makeNoop("SemanticSearchActivitySessions");
export const SendDiscordDM = makeNoop("SendDiscordDM");
export const SendDiscordMessage = makeNoop("SendDiscordMessage");
export const SendFeishuMessage = makeNoop("SendFeishuMessage");
export const SendPrompt = makeNoop("SendPrompt");
export const SendPromptWithImages = makeNoop("SendPromptWithImages");
export const SendSlackMessage = makeNoop("SendSlackMessage");
export const SetActivityMaxStorageGB = makeNoop("SetActivityMaxStorageGB");
export const SetActivityRetentionDays = makeNoop("SetActivityRetentionDays");
export const SetBackgroundListening = makeNoop("SetBackgroundListening");
export const SetBackgroundListeningSource = makeNoop("SetBackgroundListeningSource");
export const SetBrowserBridgeEnabled = makeNoop("SetBrowserBridgeEnabled");
export const SetDiscordPipelineConfig = makeNoop("SetDiscordPipelineConfig");
export const SetDiscordScanChannelFilter = makeNoop("SetDiscordScanChannelFilter");
export const SetFeishuScanChatFilter = makeNoop("SetFeishuScanChatFilter");
export const SetPermissionEnabled = makeNoop("SetPermissionEnabled");
export const SetPipelineConfig = makeNoop("SetPipelineConfig");
export const SetSidebarGroupCollapsed = makeNoop("SetSidebarGroupCollapsed");
export const SetSkillHubSkillDomains = makeNoop("SetSkillHubSkillDomains");
export const SetSkillHubSkillEnabled = makeNoop("SetSkillHubSkillEnabled");
export const SetSlackKnownMemberChannels = makeNoop("SetSlackKnownMemberChannels");
export const SetSlackScanChannelFilter = makeNoop("SetSlackScanChannelFilter");
export const SetStartAtLogin = makeNoop("SetStartAtLogin");
export const SetTelegramPipelineConfig = makeNoop("SetTelegramPipelineConfig");
export const SetUseYansuGateway = makeNoop("SetUseYansuGateway");
export const ShowQuickChatWindow = makeNoop("ShowQuickChatWindow");
export const ShowWindow = makeNoop("ShowWindow");
export const StartAgent = makeNoop("StartAgent");
export const StartDaemon = makeNoop("StartDaemon");
export const StartDiscordDaemon = makeNoop("StartDiscordDaemon");
export const StartFeishuDaemon = makeNoop("StartFeishuDaemon");
export const StartQuickChatHotkey = makeNoop("StartQuickChatHotkey");
export const StartTelegramDaemon = makeNoop("StartTelegramDaemon");
export const StartWeChatLogin = makeNoop("StartWeChatLogin");
export const StartWeChatMonitor = makeNoop("StartWeChatMonitor");
export const StopAgent = makeNoop("StopAgent");
export const StopDaemon = makeNoop("StopDaemon");
export const StopDiscordDaemon = makeNoop("StopDiscordDaemon");
export const StopFeishuDaemon = makeNoop("StopFeishuDaemon");
export const StopQuickChatHotkey = makeNoop("StopQuickChatHotkey");
export const StopTelegramDaemon = makeNoop("StopTelegramDaemon");
export const StopWeChatMonitor = makeNoop("StopWeChatMonitor");
export const SwitchEnvironment = makeNoop("SwitchEnvironment");
export const SyncMemoryIndex = makeNoop("SyncMemoryIndex");
// See GetAutoSpeakEnabled — no local TTS engine in the spotlight binary.
export const SynthesizeSpeech = async (_text: string): Promise<string> => "";
export const SplitSpeechText = async (_text: string): Promise<string[]> => [];
export const PrewarmTts = makeNoop("PrewarmTts");
export const TestDesktopNotification = makeNoop("TestDesktopNotification");
export const TestEmbedding = makeNoop("TestEmbedding");
// Override: route to host bridge.
export const TranscribeAudio = bridgeTranscribeAudio;
export const TriggerMemory = makeNoop("TriggerMemory");
export const UnbindDiscordAccount = makeNoop("UnbindDiscordAccount");
export const UninstallSkillHubSkill = makeNoop("UninstallSkillHubSkill");
export const UpdateCronJob = makeNoop("UpdateCronJob");
export const UpdateProjectActiveSession = makeNoop("UpdateProjectActiveSession");
export const UpdateProjectActiveWorkspace = makeNoop("UpdateProjectActiveWorkspace");
export const UpdateSessionClaudeID = makeNoop("UpdateSessionClaudeID");
export const UpdateStreamItemContent = makeNoop("UpdateStreamItemContent");
export const UpdateWorkspaceActiveSession = makeNoop("UpdateWorkspaceActiveSession");
export const GetAssistantHealth = makeNoop("GetAssistantHealth");
export const RetryAssistantHealth = makeNoop("RetryAssistantHealth");
export const GetAssistantChoice = makeNoop("GetAssistantChoice");
export const SetAnsweringAgent = makeNoop("SetAnsweringAgent");
export const SetAgentModel = makeNoop("SetAgentModel");
export const SetAgentEffort = makeNoop("SetAgentEffort");
export const GetMagpieProfile = makeNoop("GetMagpieProfile");
export const SetMagpieProfile = makeNoop("SetMagpieProfile");
export const SetBackgroundUsesChoice = makeNoop("SetBackgroundUsesChoice");
export const OpenAssistantSignIn = makeNoop("OpenAssistantSignIn");
export const AIReady = makeNoop("AIReady");
export const AbortHandoff = makeNoop("AbortHandoff");
export const AcceptHandoff = makeNoop("AcceptHandoff");
export const AcceptManagedModelConsent = makeNoop("AcceptManagedModelConsent");
export const AcceptPlaybookSuggestion = makeNoop("AcceptPlaybookSuggestion");
export const AddFilesPrivateFolder = makeNoop("AddFilesPrivateFolder");
export const AddVoiceInputApp = makeNoop("AddVoiceInputApp");
export const AnswerMainChatSetup = makeNoop("AnswerMainChatSetup");
export const BindAppHandle = makeNoop("BindAppHandle");
export const BringBackToMainChat = makeNoop("BringBackToMainChat");
export const BuildProviderTestCurl = makeNoop("BuildProviderTestCurl");
export const CancelMemoryDream = makeNoop("CancelMemoryDream");
export const ChatDeliverableInfo = makeNoop("ChatDeliverableInfo");
export const CheckDependencies = makeNoop("CheckDependencies");
export const ClassifyActivityEvents = makeNoop("ClassifyActivityEvents");
export const CleanupAnonymousPrincipals = makeNoop("CleanupAnonymousPrincipals");
export const ClearComposioApiKey = makeNoop("ClearComposioApiKey");
export const CloseMainChatSetupSteps = makeNoop("CloseMainChatSetupSteps");
export const ConfirmActivityStorageLimit = makeNoop("ConfirmActivityStorageLimit");
export const ConfirmEmailVerification = makeNoop("ConfirmEmailVerification");
export const ConfirmMobilePairing = makeNoop("ConfirmMobilePairing");
export const ConfirmSMSVerification = makeNoop("ConfirmSMSVerification");
export const CreateContact = makeNoop("CreateContact");
export const DeclinePlaybookSuggestion = makeNoop("DeclinePlaybookSuggestion");
export const SnoozePlaybookSuggestion = makeNoop("SnoozePlaybookSuggestion");
export const DeleteActivity = makeNoop("DeleteActivity");
export const AddActivityFollowUp = makeNoop("AddActivityFollowUp");
export const UndoActivityFollowUp = makeNoop("UndoActivityFollowUp");
export const SetActivityReminder = makeNoop("SetActivityReminder");
export const CancelActivityReminder = makeNoop("CancelActivityReminder");
export const CorrectActivity = makeNoop("CorrectActivity");
export const GetActivityReminderChoices = makeNoop("GetActivityReminderChoices");
export const GetDayNoticed = makeNoop("GetDayNoticed");
export const DismissNoticed = makeNoop("DismissNoticed");
export const UndoDismissNoticed = makeNoop("UndoDismissNoticed");
export const SaveNoticedRoutine = makeNoop("SaveNoticedRoutine");
export const DeleteHandoff = makeNoop("DeleteHandoff");
export const DeletePersonResearch = makeNoop("DeletePersonResearch");
export const DeletePrincipal = makeNoop("DeletePrincipal");
export const DeleteSOP = makeNoop("DeleteSOP");
export const DeleteVoiceSample = makeNoop("DeleteVoiceSample");
export const DeliverableFileInfo = makeNoop("DeliverableFileInfo");
export const DisableMobileConnection = makeNoop("DisableMobileConnection");
export const DiscardDiscordArchived = makeNoop("DiscardDiscordArchived");
export const DiscardSlackArchived = makeNoop("DiscardSlackArchived");
export const DiscardTelegramArchived = makeNoop("DiscardTelegramArchived");
export const EnableMobileConnection = makeNoop("EnableMobileConnection");
export const EnableMobileConnectionUsingRelay = makeNoop("EnableMobileConnectionUsingRelay");
export const EnrollVoiceSample = makeNoop("EnrollVoiceSample");
export const EnsureSelfProfile = makeNoop("EnsureSelfProfile");
export const ExportSOP = makeNoop("ExportSOP");
export const FindNewOnTopics = makeNoop("FindNewOnTopics");
export const FireDelayedDesktopNotification = makeNoop("FireDelayedDesktopNotification");
export const ForgetMemoryEntry = makeNoop("ForgetMemoryEntry");
export const ForkSession = makeNoop("ForkSession");
export const GenerateActivityTimelineGroupSummary = makeNoop("GenerateActivityTimelineGroupSummary");
export const GetTodayYourWeekDeclined = makeNoop("GetTodayYourWeekDeclined");
export const DeclineTodayYourWeek = makeNoop("DeclineTodayYourWeek");
export const GenerateTodayMissedBrief = makeNoop("GenerateTodayMissedBrief");
export const GenerateMobileBriefNow = makeNoop("GenerateMobileBriefNow");
export const SetMenuBarBriefPlayback = makeNoop("SetMenuBarBriefPlayback");
export const GenerateMobileConnectionCode = makeNoop("GenerateMobileConnectionCode");
export const GenerateSOPFromDescription = makeNoop("GenerateSOPFromDescription");
export const GenerateSOPFromRange = makeNoop("GenerateSOPFromRange");
export const GenerateTodayNews = makeNoop("GenerateTodayNews");
export const GetAIProviderConfig = makeNoop("GetAIProviderConfig");
export const GetActiveHandoffChips = makeNoop("GetActiveHandoffChips");
export const GetActivitiesInRange = makeNoop("GetActivitiesInRange");
export const GetActivityKeywordFrames = makeNoop("GetActivityKeywordFrames");
export const GetActivityRuns = makeNoop("GetActivityRuns");
export const GetActivityStorageBudget = makeNoop("GetActivityStorageBudget");
export const GetActivityStorageCleanupStatus = makeNoop("GetActivityStorageCleanupStatus");
export const GetActivityStorageUsage = makeNoop("GetActivityStorageUsage");
export const GetActivityTimelineGroups = makeNoop("GetActivityTimelineGroups");
export const GetActivityTimelineGroupsForSessions = makeNoop("GetActivityTimelineGroupsForSessions");
export const GetAfterCallSpeechModel = makeNoop("GetAfterCallSpeechModel");
export const GetAppIconsForBundleIDs = makeNoop("GetAppIconsForBundleIDs");
export const GetArchivedFeishuApp = makeNoop("GetArchivedFeishuApp");
export const GetAskBeforeRiskyCommands = makeNoop("GetAskBeforeRiskyCommands");
export const GetAutoSpeakAutomationEnabled = makeNoop("GetAutoSpeakAutomationEnabled");
export const GetAutomaticMeetingRecordingEnabled = makeNoop("GetAutomaticMeetingRecordingEnabled");
export const GetBackgroundShells = makeNoop("GetBackgroundShells");
export const GetBackgroundUnderstandingRunDetail = makeNoop("GetBackgroundUnderstandingRunDetail");
export const GetBackgroundUnderstandingSnapshot = makeNoop("GetBackgroundUnderstandingSnapshot");
export const GetCalendarOverview = makeNoop("GetCalendarOverview");
export const GetCaptureByHour = makeNoop("GetCaptureByHour");
export const GetPrivateAppSuggestions = makeNoop("GetPrivateAppSuggestions");
export const GetCaptureCounts = makeNoop("GetCaptureCounts");
export const GetComposioApiKeyStatus = makeNoop("GetComposioApiKeyStatus");
export const GetComputerUseAppAccessSnapshot = makeNoop("GetComputerUseAppAccessSnapshot");
export const GetContactsAccessStatus = makeNoop("GetContactsAccessStatus");
export const AcceptContactsConsent = makeNoop("AcceptContactsConsent");
export const GetCuaCursorOverlayEnabled = makeNoop("GetCuaCursorOverlayEnabled");
export const GetDiscordArchived = makeNoop("GetDiscordArchived");
export const GetDiscordInviteURL = makeNoop("GetDiscordInviteURL");
export const GetDoingNow = makeNoop("GetDoingNow");
export const GetEmailChannelStatus = makeNoop("GetEmailChannelStatus");
export const GetEngagementSettings = makeNoop("GetEngagementSettings");
export const GetEngagementWeekSummary = makeNoop("GetEngagementWeekSummary");
export const GetFilesOverview = makeNoop("GetFilesOverview");
export const GetLarkAccountStatus = makeNoop("GetLarkAccountStatus");
export const GetLarkCliStatus = makeNoop("GetLarkCliStatus");
export const GetLatestBrief = makeNoop("GetLatestBrief");
export const GetTodayBriefByID = makeNoop("GetTodayBriefByID");
export const GetYourWeekForDate = makeNoop("GetYourWeekForDate");
export const GetMailOverview = makeNoop("GetMailOverview");
export const GetMainChat = makeNoop("GetMainChat");
export const GetMainChatSetup = makeNoop("GetMainChatSetup");
export const GetMCPReadConnections = makeNoop("GetMCPReadConnections");
export const GetMemoryContextDebugSnapshot = makeNoop("GetMemoryContextDebugSnapshot");
export const GetMemoryForgetPreview = makeNoop("GetMemoryForgetPreview");
export const GetMobileBriefAudio = makeNoop("GetMobileBriefAudio");
export const GetMobileBriefHealth = makeNoop("GetMobileBriefHealth");
export const GetMobileBriefSchedule = makeNoop("GetMobileBriefSchedule");
export const GetMobileConnectionStatus = makeNoop("GetMobileConnectionStatus");
export const GetModelAccountsStatus = makeNoop("GetModelAccountsStatus");
export const GetPersonResearch = makeNoop("GetPersonResearch");
export const GetPlaybooksState = makeNoop("GetPlaybooksState");
export const GetPrincipalStats = makeNoop("GetPrincipalStats");
export const GetPurposeDebugExtractionRun = makeNoop("GetPurposeDebugExtractionRun");
export const GetPurposeDebugOpenLoop = makeNoop("GetPurposeDebugOpenLoop");
export const GetPurposeDebugSnapshot = makeNoop("GetPurposeDebugSnapshot");
export const GetRemindersOverview = makeNoop("GetRemindersOverview");
export const GetSMSChannelStatus = makeNoop("GetSMSChannelStatus");
export const GetSOPAuthorStatus = makeNoop("GetSOPAuthorStatus");
export const GetSOPScript = makeNoop("GetSOPScript");
export const GetScreenRecordingRelaunch = makeNoop("GetScreenRecordingRelaunch");
export const GetScreenshotRetentionDays = makeNoop("GetScreenshotRetentionDays");
export const GetSelfDreamDebugRun = makeNoop("GetSelfDreamDebugRun");
export const GetSelfDreamDebugSnapshot = makeNoop("GetSelfDreamDebugSnapshot");
export const GetShadowAgentDebugRun = makeNoop("GetShadowAgentDebugRun");
export const GetShadowAgentDebugSnapshot = makeNoop("GetShadowAgentDebugSnapshot");
export const GetSlackArchived = makeNoop("GetSlackArchived");
export const GetStorageStartupStatus = makeNoop("GetStorageStartupStatus");
export const GetSystemAudioOutsideCallsEnabled = makeNoop("GetSystemAudioOutsideCallsEnabled");
export const GetTaskShadowDebugDetail = makeNoop("GetTaskShadowDebugDetail");
export const GetTaskShadowDebugSnapshot = makeNoop("GetTaskShadowDebugSnapshot");
export const GetTaskShadowObserverCoverageSnapshot = makeNoop("GetTaskShadowObserverCoverageSnapshot");
export const GetTaskShadowObserverDebugDetail = makeNoop("GetTaskShadowObserverDebugDetail");
export const GetTaskShadowObserverDebugSnapshot = makeNoop("GetTaskShadowObserverDebugSnapshot");
export const GetTelegramArchived = makeNoop("GetTelegramArchived");
export const GetTelemetryTier = makeNoop("GetTelemetryTier");
export const GetTextChannelStatus = makeNoop("GetTextChannelStatus");
export const GetTodayNews = makeNoop("GetTodayNews");
export const GetTodayNewsHistory = makeNoop("GetTodayNewsHistory");
export const GetTodayQuietLines = makeNoop("GetTodayQuietLines");
export const GetTodaySuggestion = makeNoop("GetTodaySuggestion");
export const RefreshTodayTopics = makeNoop("RefreshTodayTopics");
export const GetTodayTopics = makeNoop("GetTodayTopics");
export const GetTopicFinds = makeNoop("GetTopicFinds");
export const GetTtsModelStatus = makeNoop("GetTtsModelStatus");
export const GetUpcoming = makeNoop("GetUpcoming");
export const GetBriefScheduleDetail = makeNoop("GetBriefScheduleDetail");
export const GetUpcomingCalendarEvents = makeNoop("GetUpcomingCalendarEvents");
export const GetUserTopics = makeNoop("GetUserTopics");
export const GetVoiceEnrollmentStatus = makeNoop("GetVoiceEnrollmentStatus");
export const GetVoiceInputApps = makeNoop("GetVoiceInputApps");
export const GetWorkPatternProfileDebugDetail = makeNoop("GetWorkPatternProfileDebugDetail");
export const GetWorkPatternProfileDebugSnapshot = makeNoop("GetWorkPatternProfileDebugSnapshot");
export const GrowStreamItemContent = makeNoop("GrowStreamItemContent");
export const ImportContactsFromVCard = makeNoop("ImportContactsFromVCard");
export const ImportSOP = makeNoop("ImportSOP");
export const ImportSystemContacts = makeNoop("ImportSystemContacts");
export const InstallCLITool = makeNoop("InstallCLITool");
export const SetupInstallAssistant = makeNoop("SetupInstallAssistant");
export const SetupCheckAssistant = makeNoop("SetupCheckAssistant");
export const OpenAssistantInstallTerminal = makeNoop("OpenAssistantInstallTerminal");
export const OpenNodeDownload = makeNoop("OpenNodeDownload");
export const IsBYOAEntitled = makeNoop("IsBYOAEntitled");
export const IsClosedEyesModeEnabled = makeNoop("IsClosedEyesModeEnabled");
export const LarkCliUserLoginPoll = makeNoop("LarkCliUserLoginPoll");
export const LarkCliUserLoginStart = makeNoop("LarkCliUserLoginStart");
export const ListAIProviderModels = makeNoop("ListAIProviderModels");
export const ListAIProviderPresets = makeNoop("ListAIProviderPresets");
export const ListAXParserCatalog = makeNoop("ListAXParserCatalog");
export const ListAXParserUnsupportedApps = makeNoop("ListAXParserUnsupportedApps");
export const ListAXWindowsByBundle = makeNoop("ListAXWindowsByBundle");
export const ListChatDeliverableOpenWithApps = makeNoop("ListChatDeliverableOpenWithApps");
export const ListDeliverableFileOpenWithApps = makeNoop("ListDeliverableFileOpenWithApps");
export const ListDeliverables = makeNoop("ListDeliverables");
export const ListHandoffs = makeNoop("ListHandoffs");
export const GetNeedsYou = makeNoop("GetNeedsYou");
export const PutOffNeedsYou = makeNoop("PutOffNeedsYou");
export const GetTodayNeedResolution = makeNoop("GetTodayNeedResolution");
export const GetTodayNeedCheck = makeNoop("GetTodayNeedCheck");
export const GetDoneForYou = makeNoop("GetDoneForYou");
export const MarkTodayDoneOpened = makeNoop("MarkTodayDoneOpened");
export const ReadTodayDoneAnswer = makeNoop("ReadTodayDoneAnswer");
export const ListPlugins = makeNoop("ListPlugins");
export const ListPrincipalActivitySessions = makeNoop("ListPrincipalActivitySessions");
export const ListPrincipals = makeNoop("ListPrincipals");
export const ListPrincipalsByKind = makeNoop("ListPrincipalsByKind");
export const ListPrincipalsWithStats = makeNoop("ListPrincipalsWithStats");
export const GetSavedCopyStamp = makeNoop("GetSavedCopyStamp");
export const GetSavedPeopleView = makeNoop("GetSavedPeopleView");
export const SavePeopleSavedView = makeNoop("SavePeopleSavedView");
export const ForgetSavedPeopleView = makeNoop("ForgetSavedPeopleView");
export const ListSidebarTopics = makeNoop("ListSidebarTopics");
export const ListSOPs = makeNoop("ListSOPs");
export const ListSelfDreamUpdates = makeNoop("ListSelfDreamUpdates");
export const ListSessionPrincipals = makeNoop("ListSessionPrincipals");
export const ListSessionSpeakerClusters = makeNoop("ListSessionSpeakerClusters");
export const ListSystemContacts = makeNoop("ListSystemContacts");
export const ListVoiceSamples = makeNoop("ListVoiceSamples");
export const MergePrincipals = makeNoop("MergePrincipals");
export const ListPeopleOverview = makeNoop("ListPeopleOverview");
export const MergePeople = makeNoop("MergePeople");
export const UndoMerge = makeNoop("UndoMerge");
export const NotSamePeople = makeNoop("NotSamePeople");
export const UndoNotSamePeople = makeNoop("UndoNotSamePeople");
export const GetPersonDetail = makeNoop("GetPersonDetail");
export const GetPersonBrief = makeNoop("GetPersonBrief");
export const OpenPersonReplyDraft = makeNoop("OpenPersonReplyDraft");
export const OpenChatDeliverable = makeNoop("OpenChatDeliverable");
export const OpenChatDeliverableWithApp = makeNoop("OpenChatDeliverableWithApp");
export const OpenDeliverableFile = makeNoop("OpenDeliverableFile");
export const OpenDeliverableFileWithApp = makeNoop("OpenDeliverableFileWithApp");
export const PickVCardFile = makeNoop("PickVCardFile");
export const PlanActivityStorageCleanup = makeNoop("PlanActivityStorageCleanup");
export const PollFeishuRegistration = makeNoop("PollFeishuRegistration");
export const ProbeAXParser = makeNoop("ProbeAXParser");
export const ProbeComputerUseAppAccess = makeNoop("ProbeComputerUseAppAccess");
export const PromoteAnonymousToContact = makeNoop("PromoteAnonymousToContact");
export const ReassignMemoryAttribution = makeNoop("ReassignMemoryAttribution");
export const RecordChatDeliverables = makeNoop("RecordChatDeliverables");
export const RecordFirstBriefSeen = makeNoop("RecordFirstBriefSeen");
export const RecordProductActivity = makeNoop("RecordProductActivity");
export const RecordTopicFindFeedback = makeNoop("RecordTopicFindFeedback");
export const RefreshModelAccounts = makeNoop("RefreshModelAccounts");
export const RejectMemoryAttribution = makeNoop("RejectMemoryAttribution");
export const RejectMemoryAttributionWithPreview = makeNoop("RejectMemoryAttributionWithPreview");
export const RelaunchApp = makeNoop("RelaunchApp");
export const RemoveAfterCallSpeechModel = makeNoop("RemoveAfterCallSpeechModel");
export const RemoveFilesPrivateFolder = makeNoop("RemoveFilesPrivateFolder");
export const RemoveImportedContacts = makeNoop("RemoveImportedContacts");
export const RemoveVoiceInputApp = makeNoop("RemoveVoiceInputApp");
export const ReopenMainChatSetupSteps = makeNoop("ReopenMainChatSetupSteps");
export const ReplaySelfDreamDebug = makeNoop("ReplaySelfDreamDebug");
export const RequestContactsAccess = makeNoop("RequestContactsAccess");
export const ResolveAgencyHandoffExecution = makeNoop("ResolveAgencyHandoffExecution");
export const ResolveChatSender = makeNoop("ResolveChatSender");
export const ResolveMainChatProactiveAction = makeNoop("ResolveMainChatProactiveAction");
export const SendMainChatProactiveDraft = makeNoop("SendMainChatProactiveDraft");
export const CorrectMainChatProactiveFact = makeNoop("CorrectMainChatProactiveFact");
export const RecordMainChatProactiveReceipt = makeNoop("RecordMainChatProactiveReceipt");
export const LoadMainChatProactiveItem = makeNoop("LoadMainChatProactiveItem");
export const ListOpenAnswers = makeNoop("ListOpenAnswers");
export const ChangeOpenAnswer = makeNoop("ChangeOpenAnswer");
export const CancelOpenAnswer = makeNoop("CancelOpenAnswer");
export const SelectOpenAnswerAsk = makeNoop("SelectOpenAnswerAsk");
export const NotForMeAnswer = makeNoop("NotForMeAnswer");
export const UndoNotForMeAnswer = makeNoop("UndoNotForMeAnswer");
export const ReturnToMainChatPlace = makeNoop("ReturnToMainChatPlace");
export const ResolveTodaySuggestion = makeNoop("ResolveTodaySuggestion");
export const RecordTodayAction = makeNoop("RecordTodayAction");
export const RecordTodaySectionSeen = makeNoop("RecordTodaySectionSeen");
export const RecordTasksAction = makeNoop("RecordTasksAction");
export const RecordTasksSectionSeen = makeNoop("RecordTasksSectionSeen");
export const ReuseArchivedFeishuApp = makeNoop("ReuseArchivedFeishuApp");
export const ReuseDiscordLocal = makeNoop("ReuseDiscordLocal");
export const ReuseSlackLocal = makeNoop("ReuseSlackLocal");
export const ReuseTelegramLocal = makeNoop("ReuseTelegramLocal");
export const RevealChatDeliverable = makeNoop("RevealChatDeliverable");
export const RevealDeliverableFile = makeNoop("RevealDeliverableFile");
export const ReviewSelfDreamOperation = makeNoop("ReviewSelfDreamOperation");
export const RevokeManagedModelConsent = makeNoop("RevokeManagedModelConsent");
export const RevokeMCPReadAssistant = makeNoop("RevokeMCPReadAssistant");
export const RevokeMobileDevice = makeNoop("RevokeMobileDevice");
export const ResetMobileRemoteAccess = makeNoop("ResetMobileRemoteAccess");
export const RunPlaybookNow = makeNoop("RunPlaybookNow");
export const RunSOP = makeNoop("RunSOP");
export const RunTaskShadowQualityCampaign = makeNoop("RunTaskShadowQualityCampaign");
export const SaveAIProviderConfig = makeNoop("SaveAIProviderConfig");
export const SaveComposioApiKey = makeNoop("SaveComposioApiKey");
export const SaveDiscordLocalCredentials = makeNoop("SaveDiscordLocalCredentials");
export const SaveSOP = makeNoop("SaveSOP");
export const SaveSlackLocalCredentials = makeNoop("SaveSlackLocalCredentials");
export const SaveTelegramLocalCredentials = makeNoop("SaveTelegramLocalCredentials");
export const SendPromptWithAttachments = makeNoop("SendPromptWithAttachments");
export const SendMainChatWithTopic = makeNoop("SendMainChatWithTopic");
export const SendMainChat = makeNoop("SendMainChat");
export const AskMainChat = makeNoop("AskMainChat");
export const AskMainChatWithContext = makeNoop("AskMainChatWithContext");
export const GetTodayPanelAnswer = makeNoop("GetTodayPanelAnswer");
export const GetTodayBriefDraftRoute = makeNoop("GetTodayBriefDraftRoute");
export const GetTodayBriefDraft = makeNoop("GetTodayBriefDraft");
export const SendTodayBriefDraft = makeNoop("SendTodayBriefDraft");
export const GetTodayMeetingNote = makeNoop("GetTodayMeetingNote");
export const SendTodayMeetingNote = makeNoop("SendTodayMeetingNote");
export const SendMainChatWithContext = makeNoop("SendMainChatWithContext");
export const ListMainChatTopicItems = makeNoop("ListMainChatTopicItems");
export const SendPromptWithTodayContext = makeNoop("SendPromptWithTodayContext");
export const PrepareTodayChatTurn = makeNoop("PrepareTodayChatTurn");
export const PrepareTodayChatDraft = makeNoop("PrepareTodayChatDraft");
export const PrepareTodayChatDrafts = makeNoop("PrepareTodayChatDrafts");
export const OpenTodayBriefConversation = makeNoop("OpenTodayBriefConversation");
export const OpenTodayBriefParagraph: (briefID: string, paragraph: number, text: string) => Promise<unknown> = makeNoop("OpenTodayBriefParagraph");
export const PrepareTodayBriefParagraph = makeNoop("PrepareTodayBriefParagraph");
export const SendTestText = makeNoop("SendTestText");
export const StartTextVerification = makeNoop("StartTextVerification");
export const ConfirmTextVerification = makeNoop("ConfirmTextVerification");
export const SetAIProviderMode = makeNoop("SetAIProviderMode");
export const SetActivityMonitorEnabled = makeNoop("SetActivityMonitorEnabled");
export const SetAfterCallSpeechModelEnabled = makeNoop("SetAfterCallSpeechModelEnabled");
export const SetAskBeforeRiskyCommands = makeNoop("SetAskBeforeRiskyCommands");
export const SetAutoSpeakAutomationEnabled = makeNoop("SetAutoSpeakAutomationEnabled");
export const SetAutoSpeakEnabled = makeNoop("SetAutoSpeakEnabled");
export const SetAutoSpeakNotificationsEnabled = makeNoop("SetAutoSpeakNotificationsEnabled");
export const SetAutomaticMeetingRecordingEnabled = makeNoop("SetAutomaticMeetingRecordingEnabled");
export const SetBuiltinOn = makeNoop("SetBuiltinOn");
export const SetCachedAIProviderModels = makeNoop("SetCachedAIProviderModels");
export const SetCalendarPeekEnabled = makeNoop("SetCalendarPeekEnabled");
export const SetClosedEyesModeEnabled = makeNoop("SetClosedEyesModeEnabled");
export const SetCuaCursorOverlayEnabled = makeNoop("SetCuaCursorOverlayEnabled");
export const SetEngagementKindEnabled = makeNoop("SetEngagementKindEnabled");
export const SetEngagementKindQuiet = makeNoop("SetEngagementKindQuiet");
export const SetEngagementLevel = makeNoop("SetEngagementLevel");
export const SetEngagementRules = makeNoop("SetEngagementRules");
export const SetManagedAccountOrder = makeNoop("SetManagedAccountOrder");
export const SetMcpServerTrusted = makeNoop("SetMcpServerTrusted");
export const SetMobileBriefSchedule = makeNoop("SetMobileBriefSchedule");
export const SetPlaybookSchedule = makeNoop("SetPlaybookSchedule");
export const SetScreenshotRetentionDays = makeNoop("SetScreenshotRetentionDays");
export const SetSystemAudioOutsideCallsEnabled = makeNoop("SetSystemAudioOutsideCallsEnabled");
export const SetTelemetryTier = makeNoop("SetTelemetryTier");
export const SetTextChannel = makeNoop("SetTextChannel");
export const SetTitleStripDragRegions = makeNoop("SetTitleStripDragRegions");
export const SetTopicFindsEnabled = makeNoop("SetTopicFindsEnabled");
export const StartActivityStorageCleanup = makeNoop("StartActivityStorageCleanup");
export const StartEmailVerification = makeNoop("StartEmailVerification");
export const StartFeishuRegistration = makeNoop("StartFeishuRegistration");
export const StartPersonResearch = makeNoop("StartPersonResearch");
export const StartSMSVerification = makeNoop("StartSMSVerification");
export const StopEmailChannel = makeNoop("StopEmailChannel");
export const StopSMSChannel = makeNoop("StopSMSChannel");
export const SyncCalendarsNow = makeNoop("SyncCalendarsNow");
export const SyncFilesNow = makeNoop("SyncFilesNow");
export const SyncMailNow = makeNoop("SyncMailNow");
export const SyncRemindersNow = makeNoop("SyncRemindersNow");
export const TestAIProvider = makeNoop("TestAIProvider");
export const TestDiscordConnection = makeNoop("TestDiscordConnection");
export const TestSlackConnection = makeNoop("TestSlackConnection");
export const TestTelegramConnection = makeNoop("TestTelegramConnection");
export const TestVoiceMatch = makeNoop("TestVoiceMatch");
export const UnbindAppHandle = makeNoop("UnbindAppHandle");
export const UnlinkActivityRun = makeNoop("UnlinkActivityRun");
export const UpdatePrincipal = makeNoop("UpdatePrincipal");
export const UpdateUserTopic = makeNoop("UpdateUserTopic");
export const PinTopicToSidebar = makeNoop("PinTopicToSidebar");

export const SearchImportedContacts = makeNoop("SearchImportedContacts");
export const LinkPersonToContact = makeNoop("LinkPersonToContact");
export const UnlinkPersonContact = makeNoop("UnlinkPersonContact");
export const CancelContactsSync = makeNoop("CancelContactsSync");
export const GetMeetingNotesForSessions = makeNoop("GetMeetingNotesForSessions");
export const RevealMeetingRecording = makeNoop("RevealMeetingRecording");
export const RevealTaskStep = makeNoop("RevealTaskStep");
export const GetMeetingNotesTranscript = makeNoop("GetMeetingNotesTranscript");
export const DeleteMeetingNotes = makeNoop("DeleteMeetingNotes");
export const GetMeetingCallTurns = makeNoop("GetMeetingCallTurns");
export const RenameMeetingSpeaker = makeNoop("RenameMeetingSpeaker");
export const ForgetVoices = makeNoop("ForgetVoices");
export const GetRecentMeetingNotes = makeNoop("GetRecentMeetingNotes");
export const MoveGoal = makeNoop("MoveGoal");
export const CreateGoal = makeNoop("CreateGoal");
export const GetGoalCheckIn = makeNoop("GetGoalCheckIn");
export const NoteGoalCheckInShown = makeNoop("NoteGoalCheckInShown");
export const ListGoals = makeNoop("ListGoals");
export const GetGoal = makeNoop("GetGoal");
export const GetActivityMonthStats = makeNoop("GetActivityMonthStats");

export const GetAboutYouPortrait = makeNoop("GetAboutYouPortrait");
export const RetryAboutYouPortrait = makeNoop("RetryAboutYouPortrait");
export const ConfirmAboutYouPortrait = makeNoop("ConfirmAboutYouPortrait");
export const UndoConfirmAboutYouPortrait = makeNoop("UndoConfirmAboutYouPortrait");
export const NotQuiteAboutYouPortrait = makeNoop("NotQuiteAboutYouPortrait");
export const AnswerFirstRead = makeNoop("AnswerFirstRead");
export const MarkAboutYouPortraitSeen = makeNoop("MarkAboutYouPortraitSeen");
export const AnswerAboutYouPortraitBelief = makeNoop("AnswerAboutYouPortraitBelief");
export const UndoAboutYouPortraitBelief = makeNoop("UndoAboutYouPortraitBelief");
export const ShowAboutYouPortraitPending = makeNoop("ShowAboutYouPortraitPending");
export const GetPortraitTodayCard = makeNoop("GetPortraitTodayCard");
export const DismissPortraitTodayCard = makeNoop("DismissPortraitTodayCard");
export const ListKnowHow = makeNoop("ListKnowHow");
export const GetKnowHowMatch = makeNoop("GetKnowHowMatch");
export const IdeaMeaningGroups = makeNoop("IdeaMeaningGroups");
export const DismissKnowHowMatch = makeNoop("DismissKnowHowMatch");
export const GetKnowHowCandidate = makeNoop("GetKnowHowCandidate");
export const ConfirmKnowHowCandidate = makeNoop("ConfirmKnowHowCandidate");
export const DeclineKnowHowCandidate = makeNoop("DeclineKnowHowCandidate");
export const UndoDeclineKnowHowCandidate = makeNoop("UndoDeclineKnowHowCandidate");
export const UndoConfirmKnowHowCandidate = makeNoop("UndoConfirmKnowHowCandidate");
export const SaveKnowHowCandidate = makeNoop("SaveKnowHowCandidate");
export const ConfirmMemoryEntry = makeNoop("ConfirmMemoryEntry");
export const UndoConfirmMemoryEntry = makeNoop("UndoConfirmMemoryEntry");
export const NotQuiteMemoryEntry = makeNoop("NotQuiteMemoryEntry");
export const UndoNotQuiteMemoryEntry = makeNoop("UndoNotQuiteMemoryEntry");
export const UndoMemoryWording = makeNoop("UndoMemoryWording");
export const GetAboutYou = makeNoop("GetAboutYou");
export const GetCommunicationDraftExamples = makeNoop("GetCommunicationDraftExamples");
export const GetMemoryEvidenceReadable = makeNoop("GetMemoryEvidenceReadable");
export const GetEntryUses = makeNoop("GetEntryUses");
export const GetEntryUseCounts = makeNoop("GetEntryUseCounts");
export const GetSettingsOverview = makeNoop("GetSettingsOverview");
export const DismissOverviewIssue = makeNoop("DismissOverviewIssue");
export const PauseObservationFor = makeNoop("PauseObservationFor");
export const ResumeObservation = makeNoop("ResumeObservation");
export const GetTodayObservationPauseSince = makeNoop("GetTodayObservationPauseSince");
export const GetTodayDayMeetingTarget = makeNoop("GetTodayDayMeetingTarget");
export const GetTodayPrepMeetingForPerson = makeNoop("GetTodayPrepMeetingForPerson");
export const GetTodayPermissionStates = makeNoop("GetTodayPermissionStates");
export const RequestTodayPermission = makeNoop("RequestTodayPermission");
export const SearchEverything = makeNoop("SearchEverything");
export const FindStreamItemCursor = makeNoop("FindStreamItemCursor");

export const ListMainChatAnswerChips = makeNoop("ListMainChatAnswerChips");

export const GetClockFormat = makeNoop("GetClockFormat");
export const GetMainChatOverview = makeNoop("GetMainChatOverview");
export const GetMainChatWaiting = makeNoop("GetMainChatWaiting");
export const ListMainChatWaitingItems = makeNoop("ListMainChatWaitingItems");
export const SetMainChatHideRoutine = makeNoop("SetMainChatHideRoutine");

export const GetAboutInfo = makeNoop("GetAboutInfo");
export const SetBackgroundUpdates = makeNoop("SetBackgroundUpdates");
export const RestartToUpdate = makeNoop("RestartToUpdate");
export const SaveSupportLogs = makeNoop("SaveSupportLogs");
export const OpenDataFolder = makeNoop("OpenDataFolder");
export const DismissUpdateUntilNextLaunch = makeNoop("DismissUpdateUntilNextLaunch");

export const ReportLaunchStage = makeNoop("ReportLaunchStage");
export const ReportPageFirstPaint = makeNoop("ReportPageFirstPaint");
export const GetModelChoices = makeNoop("GetModelChoices");

export const SetModelChoice = makeNoop("SetModelChoice");

export const PlanModelDownload = makeNoop("PlanModelDownload");

export const StartModelDownloads = makeNoop("StartModelDownloads");

export const PlanSetupModelDownload = makeNoop("PlanSetupModelDownload");

export const DeferModelDownloads = makeNoop("DeferModelDownloads");

export const GetModelDownloads = makeNoop("GetModelDownloads");

export const RetryModelDownload = makeNoop("RetryModelDownload");

export const RemoveModel = makeNoop("RemoveModel");

export const GetModelEvaluation = makeNoop("GetModelEvaluation");

export const GetAgentProbe = makeNoop("GetAgentProbe");

export const RefreshAgentProbe = makeNoop("RefreshAgentProbe");

export const SetSetupWelcomeVisible = makeNoop("SetSetupWelcomeVisible");

export const RequestAllPermissions = makeNoop("RequestAllPermissions");

export const GetSetupPermissions = makeNoop("GetSetupPermissions");
export const GetTodayDayShape = makeNoop("GetTodayDayShape");
export const GetHoldDayShape = makeNoop("GetHoldDayShape");
export const GetFindTime = makeNoop("GetFindTime");
export const GetFindTimeDetails = makeNoop("GetFindTimeDetails");
export const GetFindTimeDetailsForRange = makeNoop("GetFindTimeDetailsForRange");

export const PlanToolDownload = makeNoop("PlanToolDownload");
export const StartToolDownload = makeNoop("StartToolDownload");
export const GetTodayBeforeYouTalk = makeNoop("GetTodayBeforeYouTalk");
export const DismissTodayBeforeYouTalk = makeNoop("DismissTodayBeforeYouTalk");

export const ResumeModelDownload = makeNoop("ResumeModelDownload");
export const GetModelNetworkState = makeNoop("GetModelNetworkState");
export const SetTopicRefreshInterval = makeNoop("SetTopicRefreshInterval");

export const CreateTask = makeNoop("CreateTask");
export const GetWhatIsoformSees = makeNoop("GetWhatIsoformSees");
export const GetDesktopTaskThread = makeNoop("GetDesktopTaskThread");
export const StartHandoffAgain = makeNoop("StartHandoffAgain");
export const ListDesktopTaskThreads = makeNoop("ListDesktopTaskThreads");
export const ActOnDesktopTask = makeNoop("ActOnDesktopTask");
export const ReplyToDesktopTask = makeNoop("ReplyToDesktopTask");
export const DesktopDraftTaskID = makeNoop("DesktopDraftTaskID");
export const StartDesktopTaskOffer = makeNoop("StartDesktopTaskOffer");
export const ListTaskSuggestions = makeNoop("ListTaskSuggestions");
export const FindMoreTaskSuggestions = makeNoop("FindMoreTaskSuggestions");
export const GetFirstWeekDay = makeNoop("GetFirstWeekDay");
export const GetAssistantHistory = makeNoop("GetAssistantHistory");
export const RefreshAssistantHistory = makeNoop("RefreshAssistantHistory");
export const GetReadAssistantHistory = makeNoop("GetReadAssistantHistory");
export const SetReadAssistantHistory = makeNoop("SetReadAssistantHistory");
export const GetCalendarFirstDay = makeNoop("GetCalendarFirstDay");
export const GetWatchedToday = makeNoop("GetWatchedToday");
export const PrepareTaskSuggestion = makeNoop("PrepareTaskSuggestion");
export const SendPromptWithOfferReferences = makeNoop("SendPromptWithOfferReferences");
export const DecideTaskSuggestion = makeNoop("DecideTaskSuggestion");
export const GetMobileKeepAwake = makeNoop("GetMobileKeepAwake");
export const SetMobileKeepAwake = makeNoop("SetMobileKeepAwake");
export const GetYourAgentPage = makeNoop("GetYourAgentPage");
export const GetYourAgentReservation = makeNoop("GetYourAgentReservation");
export const CheckYourAgentName = makeNoop("CheckYourAgentName");
export const ReserveYourAgent = makeNoop("ReserveYourAgent");
export const ReleaseYourAgent = makeNoop("ReleaseYourAgent");
export const VerifyYourAgentCode = makeNoop("VerifyYourAgentCode");
export const ResendYourAgentCode = makeNoop("ResendYourAgentCode");
export const ChooseYourAgentPage = makeNoop("ChooseYourAgentPage");
export const CancelYourAgentRequest = makeNoop("CancelYourAgentRequest");
export const SetSetupWindowMode = makeNoop("SetSetupWindowMode");
export const SetMenuBarMarkVisible = makeNoop("SetMenuBarMarkVisible");
export const PlaySetupFinish = makeNoop("PlaySetupFinish");
export const RevealMainWindow = makeNoop("RevealMainWindow");

export const GetHoldSettings = makeNoop("GetHoldSettings");
export const SetOfferHolds = makeNoop("SetOfferHolds");
export const SetHoldCalendar = makeNoop("SetHoldCalendar");

export const ProposeHold = makeNoop("ProposeHold");
export const HoldTime = makeNoop("HoldTime");
export const GetHoldStatus = makeNoop("GetHoldStatus");
export const UndoHold = makeNoop("UndoHold");
export const RemoveHold = makeNoop("RemoveHold");
export const UndoRemoveHold = makeNoop("UndoRemoveHold");
export const DismissHoldOffer = makeNoop("DismissHoldOffer");
export const UndoDismissHoldOffer = makeNoop("UndoDismissHoldOffer");
export const SaveMorningBriefWeekIntent = makeNoop("SaveMorningBriefWeekIntent");
export const PostMorningBriefWeekAnswer = makeNoop("PostMorningBriefWeekAnswer");
export const PostTodayClosingAnswer = makeNoop("PostTodayClosingAnswer");
export const ChangeMorningBriefWeekIntent = makeNoop("ChangeMorningBriefWeekIntent");

export const OpenTodayReplyDraft = makeNoop("OpenTodayReplyDraft");
export const CancelTodayReplyDraft = makeNoop("CancelTodayReplyDraft");

export const AddTodayNoticedSchedule = makeNoop("AddTodayNoticedSchedule");
export const UndoTodayNoticedSchedule = makeNoop("UndoTodayNoticedSchedule");
export const CorrectTodayWeekBelief = makeNoop("CorrectTodayWeekBelief");
export const UndoTodayWeekBelief = makeNoop("UndoTodayWeekBelief");
export const CanUndoTodayWeekBelief = makeNoop("CanUndoTodayWeekBelief");
export const EditTodayNoticedSchedule = makeNoop("EditTodayNoticedSchedule");

export const AskMainChatFromTodayChip = makeNoop("AskMainChatFromTodayChip");
export const SendMainChatFromTodayChip = makeNoop("SendMainChatFromTodayChip");
export const SendMainChatWithAttachments = makeNoop("SendMainChatWithAttachments");
export const PostMorningBriefWeekAnswerFromChip = makeNoop("PostMorningBriefWeekAnswerFromChip");
export const RefreshDerivedView = makeNoop("RefreshDerivedView");
export const AcknowledgeRollbackNotice = makeNoop("AcknowledgeRollbackNotice");
export const GetAgentOwnerSnapshot = makeNoop("GetAgentOwnerSnapshot");
export const AgentOwnerAction = makeNoop("AgentOwnerAction");
export const AgentOwnerDecision = makeNoop("AgentOwnerDecision");
export const OpenAgentOwnerPayoutOnboarding = makeNoop("OpenAgentOwnerPayoutOnboarding");
export const GetAgentOwnerNeeds = makeNoop("GetAgentOwnerNeeds");
export const GetAgentOwnerNeedsWithDraft = makeNoop("GetAgentOwnerNeedsWithDraft");
export const MarkAgentOwnerPayoutNeedShown = makeNoop("MarkAgentOwnerPayoutNeedShown");
export const DownloadAgentOwnerReviewFile = makeNoop("DownloadAgentOwnerReviewFile");
export const GetWorkProfile = makeNoop("GetWorkProfile");
export const EditWorkProfileLine = makeNoop("EditWorkProfileLine");
export const SetWorkProfileLineRemoved = makeNoop("SetWorkProfileLineRemoved");
export const AddWorkProfileLine = makeNoop("AddWorkProfileLine");
export const SetWorkProfileSpecialties = makeNoop("SetWorkProfileSpecialties");
export const RenameWorkProfileSpecialty = makeNoop("RenameWorkProfileSpecialty");
export const KeepWorkProfileLine = makeNoop("KeepWorkProfileLine");
export const DiscardWorkProfileDraft = makeNoop("DiscardWorkProfileDraft");
export const PreviewWorkProfileUpload = makeNoop("PreviewWorkProfileUpload");
export const ApproveWorkProfile = makeNoop("ApproveWorkProfile");
export const BuildWorkProfileDraft = makeNoop("BuildWorkProfileDraft");
