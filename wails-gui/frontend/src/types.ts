export type AgentEventType =
    | 'run_created'
    | 'system'
    | 'thinking'
    | 'assistant'
    | 'tool_use_start'
    | 'tool_use'
    | 'tool_result'
    | 'result'
    | 'available_commands'
    | 'done'
    | 'error'
    | 'close'
    | 'plan_complete'
    | 'permission_request'
    | 'permission_resolved'
    | 'compact_start'
    | 'compact_end'
    | 'compact_error';

export type PermissionOptionKind = 'allow_once' | 'allow_always' | 'reject_once' | 'reject_always';

export interface PermissionOption {
    id: string;
    name: string;
    kind: PermissionOptionKind;
}

export interface AgentCommand {
    name: string;
    description?: string;
    input_hint?: string;
}

export interface AgentEvent {
    type: AgentEventType;
    run_id?: string;
    session_id?: string;
    tag?: string;
    content?: string;
    tool_name?: string;
    tool_input?: unknown;
    tool_use_id?: string;
    persisted?: boolean;
    available_commands?: AgentCommand[];
    error?: string;
    /** An error's proven cause (chat_cause.go); the error row reads its line from it. */
    code?: string;
    /** The AI the cause line names. */
    assistant?: string;
    permission_options?: PermissionOption[];
    permission_request_id?: string;
    upcoming_approval?: UpcomingApproval;
    /** Names one emit; the row built from this event takes it as its id. */
    event_id?: string;
    /** The assistant, thinking or tool row this event builds or grows,
     *  named by the backend so every renderer stores the same row. */
    item_id?: string;
    /** An assistant or thinking chunk's place in its row, from 1. */
    item_seq?: number;
}

/** The item an Upcoming tool's approval question is about: item is the
 *  renderer's own name for the brief or Daily News, title a schedule's
 *  name, and schedule a change's new schedule (cron, or at:<ms>). */
export interface UpcomingApproval {
    item?: string;
    title?: string;
    schedule?: string;
}

export interface AgentStatus {
    running: boolean;
    ready: boolean;
    port: number;
    message?: string;
}

export interface PermissionItem {
    key: string;
    label: string;
    description: string;
    granted: boolean;
    required: boolean;
    actionable: boolean;
    actionLabel: string;
    toggleable: boolean;
    enabled: boolean;
}

export interface PlatformPermissions {
    platform: string;
    items: PermissionItem[];
    supported: boolean;
}

export type OnboardingProviderId =
    | 'slack'
    | 'discord'
    | 'telegram'
    | 'wechat';

export interface OnboardingAboutYou {
    busyWith: string;
    repeatedWeekly: string;
    wishHandled: string;
}

export interface OnboardingState {
    version: number;
    completed: boolean;
    neverShowAgain: boolean;
    permissionsComplete: boolean;
    aboutYou: OnboardingAboutYou;
    selectedProviders: OnboardingProviderId[];
    completedProviders: OnboardingProviderId[];
    providerChannelSelections: Record<string, string[]>;
}

export interface FeishuStatus {
    connected: boolean;
    app_id?: string;
    feishu_user_name?: string;
    feishu_account_id?: string;
    daemon_online: boolean;
}

// Remote resources fetched from Isoform API
export interface RemoteOrg {
    name: string;
    display_name: string;
}

export interface RemoteProduct {
    name: string;
    display_name: string;
}

export interface RemoteProject {
    uuid: string;
    name: string;
    display_name: string;
    coding_branch: string;
    product_uuid: string;
}

export type StreamItemKind = 'user' | 'assistant' | 'system' | 'context_status' | 'thinking' | 'result' | 'tool' | 'error' | 'compact_boundary' | 'compact_summary';

export interface ImageAttachment {
    mediaType: string;   // e.g. "image/png"
    dataBase64: string;  // raw base64 (no data: prefix)
    fileName?: string;   // original name, used by the draft's remove control
}

export type FileCategory = 'image' | 'document' | 'spreadsheet' | 'presentation' | 'code' | 'text' | 'archive';

export interface FileAttachment {
    id: string;
    fileName: string;
    fileSize: number;
    mediaType: string;
    category: FileCategory;
    dataBase64?: string; // transient: held in memory for sending, not persisted
}

/** Source bindings checked when a phone's idea-derived message was admitted. */
export interface StreamOfferReference {
    id: string;
    version: string;
}

export interface StreamItem {
    id: string;
    kind: StreamItemKind;
    /** Local RPC failure; the original turn and attachments remain available for retry. */
    sendFailed?: boolean;
    /** Why a kept send never left the computer (AgentEvent.code), e.g. no_assistant. */
    sendFailedCode?: string;
    /** A person's Try again failed too (Chat K3): the line reads "Still didn't work." */
    sendRetryFailed?: boolean;
    content?: string;
    toolName?: string;
    toolInput?: unknown;
    toolUseId?: string;
    runId?: string;
    sessionId?: string;
    createdAt?: number;
    openAnswer?: import("@/lib/open-answers").OpenAnswer;
    cardType?: string;
    sortOrder?: number;
    images?: ImageAttachment[];
    files?: FileAttachment[];
    // Raw run output (markdown + agent <deliverables>/<delivery_target> tags)
    // of a scheduled automation, attached to the first user turn of a
    // "continue in chat" session so the bubble renders it as a collapsible
    // "run output" quote card (via AssistantMarkdown — markdown + file chips).
    // Persisted (with automationJobId) as JSON in the stream item's
    // automation_context column, so the card survives a session reload.
    automationResultOutput?: string;
    // Owning automation job id, paired with automationResultOutput so the
    // quote card's deliverable chips can open files via OpenAutomationDeliverable.
    // Persisted alongside automationResultOutput inside automation_context.
    automationJobId?: string;
    // Marks the first persisted user turn for an accepted handoff. The content
    // is the handoff document used to start the execution agent and should
    // render collapsed in chat.
    handoffContext?: boolean;
    handoffId?: string;
    // Preserve admission provenance when the renderer reloads and saves the row.
    // This is attribution metadata, not permission to execute the idea.
    offerReferences?: StreamOfferReference[];
    // Preserve the exact server-owned source envelope (including unknown or
    // malformed fields) so a renderer save cannot erase a read dependency.
    offerSourceContext?: string;
    /** Explicitly saved answer to a morning brief's week question. */
    weekAnswerBriefId?: string;
    // Main chat rows only (RC 2 C1), derived by the computer on every read and
    // never saved back: the row's class (conversation, waiting, finished,
    // routine, other), its topic tags, and its local day ("YYYY-MM-DD").
    rowClass?: MainChatRowClass;
    topicIds?: string[];
    localDay?: string;
}

/** The computer's one row-class rule for the main chat (main_chat_view.go). */
export type MainChatRowClass = 'conversation' | 'waiting' | 'finished' | 'routine' | 'other';

// Project context fetched from Isoform API
export interface ProjectContext {
    is_yansu_project: boolean;
    project_name?: string;
    organization?: string;
    product?: string;
    prd?: string;
    prd_status?: string;
    scenarios?: string;
    scenarios_status?: string;
    error?: string;
    fetchedAt?: number;      // Timestamp when context was fetched
}

// Parsed scenarios data (from JSON content of simulations stage)
export interface ScenarioSummary {
    total_scenarios: number;
    complexity: string;
    estimated_time: string;
}

export interface ScenarioStep {
    step_number: number;
    action: string;
    description: string;
    context: string;
}

export interface ScenarioTestCase {
    id: number;
    input: string;
    expected: string;
    type: string;
    status?: string;
}

export interface SimulationScenario {
    id: number;
    name: string;
    description: string;
    manual?: boolean;
    steps: ScenarioStep[];
    test_cases: ScenarioTestCase[];
    status: string;
}

export interface ScenarioData {
    project_name: string;
    scenarios: SimulationScenario[];
    summary: ScenarioSummary;
}

// Git repository information for a project directory
export interface GitInfo {
    isGitRepo: boolean;
    currentBranch: string;
    remoteUrl?: string;
    rootPath: string;
}

// Application detected on the user's machine
export interface InstalledApp {
    name: string;
    app_bundle: string;
    category: string;
}

// Project identified by a local directory (optionally a Isoform project)
export interface Project {
    id: string;              // Local unique ID
    name: string;            // Project name (from project.json or directory)
    path: string;            // Local directory path
    organization?: string;   // From .something/project.json
    product?: string;        // From .something/project.json
    isYansuProject: boolean; // Whether .something/project.json exists
    context?: ProjectContext; // Fetched PRD/scenarios (from FetchProjectContext)
    gitInfo?: GitInfo;                // Git repository info (populated on open)
    workspaces: Workspace[];          // Workspaces under this project
    activeWorkspaceId: string | null; // Currently active workspace
    activeSessionId: string | null;   // Currently active session (kept for backward compat)
    sortOrder?: number;               // Manual sort order for sidebar drag-and-drop
}

/** Existing terminal/persistence remote environment wire fields. */
export interface RemoteEnv {
    type: string;
    distro?: string;
    sshConfigId?: string;
}

// Workspace — belongs to a Project
export interface Workspace {
    id: string;
    label: string;
    isDefault: boolean;
    worktreePath?: string;       // Git worktree path (non-default workspaces only)
    worktreeBranch?: string;     // Git branch name (non-default workspaces only)
    baseBranch?: string;         // Base branch for the worktree
    activeSessionId: string | null;
    remoteEnv?: RemoteEnv;
    sortOrder?: number;           // Sort order for drag-and-drop reordering
    sessions: Session[];
    createdAt: number;
    updatedAt: number;
}

// Session management — belongs to a Workspace
/** The session's place in the chat model: 'main' is the one Isoform-owned
 *  chat the user keeps talking in, 'side' is a side chat split off from it,
 *  and '' (or undefined) is every other session. */
export type SessionKind = 'main' | 'side' | '';

export interface Session {
    localId: string;         // Local unique ID (generated on creation)
    workspaceId: string;     // Parent workspace ID
    sessionId: string;       // Claude's session_id (populated after first response)
    label: string;           // Display name
    items: StreamItem[];     // Conversation items for this session
    thinkingLevel?: string;  // Cached executor effort on older sessions
    sessionMode?: string;    // Cached executor session mode
    model: string;           // Model used
    createdAt: number;       // Creation timestamp
    currentRunId: string | null; // Current run ID for this session
    streaming: boolean;          // Whether this session is currently streaming
    streamingStartedAt: number;  // Epoch ms when streaming began (runtime only, survives session switches)
    thinkingEnabled: boolean;    // Claude: enable extended thinking (alwaysThinkingEnabled)
    planMode: boolean;           // Claude: use plan mode (read-only planning phase)
    planContent?: string;        // Plan text from exit_plan_mode tool (runtime only)
    planSessionId?: string;      // Claude session ID for --resume after plan approval (runtime only)
    awaitingPlanApproval: boolean; // Whether a plan is pending user review (runtime only)
    permissionOptions?: PermissionOption[];  // Options for a pending permission request (runtime only)
    permissionRequestId?: string; // Exact running-work request identity (runtime only)
    permissionToolName?: string; // Tool name from the permission request (runtime only)
    permissionUpcoming?: UpcomingApproval; // The Upcoming item the permission request is about (runtime only)
    availableCommands?: AgentCommand[]; // Slash commands advertised by the agent runner (runtime only)
    slashCommandsLoading?: boolean; // Whether slash command list is currently loading (runtime only)
    slashCommandsLoaded?: boolean; // Whether slash command list has been fetched at least once (runtime only)
    automationResult?: boolean;  // True when session originated from automation output (skip triage)
    /** Set when the session was produced by a meeting (transcript slice or
     *  handoff) — every chat session sharing this id was generated by the
     *  same call. Empty for regular chats. Sidebar Recent collapses
     *  sessions that share a non-empty meetingId under one parent row. */
    meetingId?: string;
    kind?: SessionKind;          // 'main' | 'side' | '' (see SessionKind)
    parentId?: string;           // The main chat a side chat was split off from
    pinned: boolean;             // Whether this session is pinned to the top
    pinnedAt: number;            // Timestamp when pinned (for ordering)
    updatedAt: number;           // Timestamp of last activity (for sorting)
    streamItemCount: number;     // Persisted item count reported by the backend
    itemsLoaded: boolean;        // Whether items contains the persisted page for this session
    itemsLoading: boolean;       // Whether initial item hydration is in progress
    // Pagination state (runtime only, not persisted)
    hasMore: boolean;            // Whether older messages exist in the database
    nextCursor: number;          // sort_order cursor for loading the next page of older items
    loadingMore: boolean;        // Whether a page load is in progress
    // Auto-compaction lifecycle (runtime only). Drives CompactionBanner;
    // no UI surfaces the underlying token usage — the user shouldn't have
    // to manage context as a resource.
    compactionState?: 'idle' | 'compacting' | 'error';
    compactionError?: string;
}

// PR information fetched via gh CLI
export interface PRInfo {
    number: number;
    title: string;
    state: 'open' | 'merged' | 'closed';
    url: string;
    draft: boolean;
}

// ── Persistence types (matching Go structs in persistence.go) ──

export interface ProjectData {
    id: string;
    name: string;
    path: string;
    organization?: string;
    product?: string;
    isYansuProject: boolean;
    activeSessionId: string;
    activeWorkspaceId: string;
    sortOrder: number;
    workspaces: WorkspaceData[];
}

export interface WorkspaceData {
    id: string;
    label: string;
    isDefault: boolean;
    worktreePath?: string;
    worktreeBranch?: string;
    baseBranch?: string;
    activeSessionId: string;
    remoteEnv?: string;
    sortOrder?: number;
    sessions: SessionData[];
    createdAt: number;
    updatedAt: number;
}

export interface SessionData {
    localId: string;
    workspaceId: string;
    sessionId: string;
    label: string;
    model: string;
    createdAt: number;
    prdEnabled: boolean;
    thinkingLevel: string;
    thinkingEnabled: boolean;
    planMode: boolean;
    automationResult?: boolean;
    meetingId?: string;
    kind?: string;
    parentId?: string;
    pinned: boolean;
    pinnedAt: number;
    updatedAt: number;
    streamItemCount?: number;
    items?: StreamItemData[] | null;
}

export interface StreamItemData {
    weekAnswerBriefId?: string;
    id: string;
    kind: string;
    content?: string;
    toolName?: string;
    toolInput?: string;  // JSON string
    toolUseId?: string;  // Anthropic-assigned tool_use_id for pairing tool_use/tool_result
    runId?: string;
    sessionId?: string;
    createdAt?: number;
    openAnswer?: unknown;
    cardType?: string;
    sortOrder: number;   // DB-assigned sort order; 0 for new items (computed by backend)
    // JSON-encoded array of {mediaType, dataBase64, fileName?} attachments, matching
    // ImageAttachment above. Kept as a string over the RPC boundary so the
    // Go StreamItemData stays a flat record; stream-helpers (de)serializes.
    images?: string;
    // JSON blob for per-message metadata. Scheduled automation follow-ups use
    // {output, jobId}; accepted handoffs use {handoff, handoffId}.
    // Flat string over the RPC boundary like images; stream-helpers
    // (de)serializes it onto StreamItem fields.
    automationContext?: string;
    // JSON-encoded array of {fileName, mediaType, fileSize, category} for
    // non-image file attachments. Same flat-string pattern as images.
    files?: string;
}

// --- Sidebar Custom Groups ---

export interface SidebarGroup {
    id: string;
    name: string;
    sortOrder: number;
    collapsed: boolean;
    items: SidebarGroupItem[];
    createdAt: number;
    updatedAt: number;
}

export interface SidebarGroupItem {
    id: string;
    groupId: string;
    itemType: 'chat' | 'automation';
    refId: string;
    sortOrder: number;
    addedAt: number;
}
