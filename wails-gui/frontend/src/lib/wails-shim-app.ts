import { timePageBinding } from './page-switch-timing';
// wails-shim-app.ts
// Unified shim for wailsjs/go/main/App. We re-export the auto-generated
// bindings (which call `window.go.main.App[name](...)`) so every method
// stays in sync with the Go backend without a hand-maintained list.
//
// In browser mode there's no `window.go`, so before the real bindings
// execute we install a Proxy at `window.go.main.App` that routes any method
// access through the HTTP browser bridge via callAppMethod. This keeps
// native mode zero-overhead (direct property lookup) and browser mode
// transparent (any method defined on the Go App struct is callable).
//
// Importing this module for its side effect is enough to initialise the
// Proxy; explicit exports below cover App methods that may be missing from
// stale generated files, and `export *` provides the rest.

import { callAppMethod } from "./browser-bridge";
import type { TodayNewsData } from "@/components/today/types";
import type { SessionData, StreamItemData } from "@/types";
import type { TodayBriefOpened, TodayChatContextRef, TodayChatDraft, TodayChatDraftResult } from "@/lib/today-chat-navigation";
import type { OpenAnswer, OpenAnswerDelivery } from "@/lib/open-answers";

export interface WhatIsoformSees {
    sources: { job: string; on: boolean }[];
    assistant?: string;
    phonePaired?: boolean;
}

export const GetWhatIsoformSees = appBinding<[], WhatIsoformSees>("GetWhatIsoformSees");
export const CancelStream = appBinding<[string], void>("CancelStream");

// Shared waiting projection for Today and Tasks. Target IDs are routing data,
// while sentence and source are display-only.
export interface TodayNeed {
    id: string;
    kind: string;
    sentence: string;
    sentenceLocalized?: Record<string, string>;
    source?: string;
    createdAt: number;
    dueAt?: number;
    targetKind: string;
    targetId: string;
    chatId?: string;
    item?: StreamItemData;
    stopCause?: string;
    /** The second return of an item put off twice: ask Keep · Done · Forget. */
    asksKeep?: boolean;
    /** When a put-off item comes back (unix ms). */
    putOffUntil?: number;
}
export interface TodayNeedsPage { items: TodayNeed[]; total: number; putOff?: TodayNeed[]; nextReturnAt?: number }
export const GetNeedsYou = appBinding<[number], TodayNeedsPage>("GetNeedsYou");
/** Not now on Needs you (put_off), Bring back now (bring_back) and Keep (keep).
 *  id is the task's id for a task and the item's id otherwise. */
export const PutOffNeedsYou = appBinding<[string, "put_off" | "bring_back" | "keep" | "forget"], void>("PutOffNeedsYou");
export interface TodayNeedResolution { kind: string; person?: string; at?: number }
export const GetTodayNeedResolution = appBinding<[string], TodayNeedResolution>("GetTodayNeedResolution");
export const GetTodayNeedCheck = appBinding<[string, string, string, string], TodayNeedResolution>("GetTodayNeedCheck");

export interface TodayDone {
    id: string;
    kind: string;
    sentence: string;
    sentenceLocalized?: Record<string, string>;
    at: number;
    targetKind: string;
    targetId: string;
    runId?: string;
    chatId?: string;
}
export interface TodayDonePage { items: TodayDone[]; total: number }
export const GetDoneForYou = appBinding<[number], TodayDonePage>("GetDoneForYou");
export const MarkTodayDoneOpened = appBinding<[string], void>("MarkTodayDoneOpened");
export interface TodayDoneAnswer { id: string; chatId: string; content: string }
export const ReadTodayDoneAnswer = appBinding<[string], TodayDoneAnswer>("ReadTodayDoneAnswer");

export interface DesktopTaskEvidence { text: string; textLocalized?: Record<string, string>; source: string; at: string }
export interface BrainDumpItem { id: string; title: string; who: "draft" | "claude_code" | "codex" | "reminder"; when?: string; project?: string; person?: string; noRunner?: boolean }
export const ParseBrainDump = appBinding<[string], { items: BrainDumpItem[] }>("ParseBrainDump");
export const AddBrainDump = appBinding<[string, BrainDumpItem[]], string[]>("AddBrainDump");
export interface DesktopTaskThread {
    id: string; title: string; group: string; status: string; updatedAt: string; progress?: { done: number; total: number };
    /** A waiting task's plan message, built from the handoff at projection time. */
    plan?: { will: string; wont?: string; need?: string; unavailable?: boolean };
    files?: string[];
    project?: string;
    /** Present only when a check wasn't met or nothing could be checked. */
    checks?: { met: number; total: number; criteria: string[]; unmet: boolean[]; unverifiable?: boolean };
    source?: string;
    stopCause?: string; stoppedBy?: string; assistant?: string; runner?: string;
    origin?: { kind: string; at: string; slot?: string; label?: string };
    outcome?: { kind: string; by: string; at: string; params?: Record<string, string> };
    events: { id: string; kind: string; at: string; text?: string; chosenId?: string; choices?: { id: string; label: string }[]; file?: { name: string }; code?: string; params?: Record<string, string> }[];
    actions: string[]; steps: { text: string; state: string; at?: string }[];
    draft?: { channel: string; why?: DesktopTaskEvidence; to: string; subject?: string; body: string; state: string; sentAt?: string; recipients?: { to: string[]; cc?: string[]; notIncluded?: string[] } };
    /** Replies queued on the task (tasks-review v1.0.5–v1.0.7). */
    replies?: DesktopTaskReply[];
    /** The stopped run's steps whose effect left it (v1.0.2), and the ones the current run skipped. */
    effectSteps?: DesktopTaskEffectStep[];
    skipped?: DesktopTaskEffectStep[];
    stepsToCheck?: number;
    repliesToChoose?: number;
}
export interface DesktopTaskReply { id: string; text: string; sentAt: string; state: "waiting" | "picked_up" | "uncertain"; pickedUpAt?: string; leftOut?: boolean; choice?: "left_out" | "use_again"; leftOutOfRun?: boolean }
export interface DesktopTaskEffectStep { id: string; kind: string; name?: string; target?: string; at: string; app?: string; proof: "done" | "not_done" | "unknown"; answer?: "skip" | "redo" }
export type DesktopTaskAction = { action: string; title?: string; eventId?: string; choiceId?: string; text?: string };
export const GetDesktopTaskThread = appBinding<[string], DesktopTaskThread>("GetDesktopTaskThread");
export interface DesktopScheduledTask { id: string; kind: string; title?: string; at: string }
export const ListDesktopTaskThreads = appBinding<[], { tasks: DesktopTaskThread[]; scheduled?: DesktopScheduledTask[]; finishedDeliverables?: DeliverableEntry[] }>("ListDesktopTaskThreads");
export const ActOnDesktopTask = appBinding<[string, DesktopTaskAction], DesktopTaskThread>("ActOnDesktopTask");
// Opens Finder on a stopped task's file step with the file selected.
export const RevealTaskStep = appBinding<[string, string], void>("RevealTaskStep");
export const ReplyToDesktopTask = appBinding<[string, string], DesktopTaskThread>("ReplyToDesktopTask");
export const DesktopDraftTaskID = appBinding<[string], string>("DesktopDraftTaskID");
export const StartDesktopTaskOffer = appBinding<[string, string], HandoffAcceptResult>("StartDesktopTaskOffer");
export const StartHandoffAgain = appBinding<[string], HandoffAcceptResult>("StartHandoffAgain");

// Purpose Map and background understanding read models, used by the
// developer tools.
export type PurposeKind =
    | "commitment"
    | "responsibility"
    | "goal"
    | "concern"
    | "opportunity";

export type PurposeLifecycleState =
    | "candidate"
    | "active"
    | "waiting"
    | "resolved"
    | "contradicted"
    | "dismissed"
    | "superseded";

export type PurposeDueKind = "none" | "date" | "window" | "condition" | "unknown";

export type PurposeOwner = "user" | "other" | "shared" | "unknown";

export interface AgencyPurpose {
    id: string;
    canonicalKey: string;
    kind: PurposeKind;
    title: string;
    desiredOutcome: string;
    owner: PurposeOwner;
    counterparty?: string;
    scope?: string;
    dueKind: PurposeDueKind;
    dueValue?: string;
    waitingCondition?: string;
    lifecycleState: PurposeLifecycleState;
    supportState: "hypothesis" | "supported" | "contradicted" | "unknown";
    reasoningStatus: "explicit" | "unknown";
    rationale?: string;
    confidence: number;
    evidenceCount: number;
    relationCount: number;
    eligible: boolean;
    currentEventId: string;
    currentEventType: string;
    advancingInitiativeIds: string[];
    createdAt: string;
    updatedAt: string;
}

export interface AgencyPurposeEvent {
    id: string;
    sequence: number;
    eventType: string;
    predecessorEventId?: string;
    title: string;
    desiredOutcome: string;
    owner: PurposeOwner;
    counterparty?: string;
    scope?: string;
    dueKind: PurposeDueKind;
    dueValue?: string;
    waitingCondition?: string;
    lifecycleState: PurposeLifecycleState;
    supportState: string;
    reasoningStatus: "explicit" | "unknown";
    rationale?: string;
    confidence: number;
    evidenceIds: string[];
    evidenceSetHash: string;
    occurredAt: string;
    createdAt: string;
    payloadHash: string;
}

export interface AgencyPurposeEvidence {
    id: string;
    eventId: string;
    sourceKind: string;
    sourceId: string;
    sourceVersion: string;
    sourceObservedAt: string;
    sourceHash: string;
    evidenceClass: string;
    stance: string;
    independenceKey: string;
    excerptHash?: string;
    privacyDecision: string;
    createdAt: string;
    payloadHash: string;
}

export interface AgencyPurposeRelationEvent {
    id: string;
    sequence: number;
    eventType: string;
    evidenceEventId: string;
    reason?: string;
    occurredAt: string;
    createdAt: string;
    payloadHash: string;
}

export interface AgencyPurposeRelation {
    id: string;
    relationType: string;
    direction: "incoming" | "outgoing";
    relatedPurposeId: string;
    relatedTitle: string;
    relatedState: PurposeLifecycleState;
    active: boolean;
    currentEventId: string;
    createdAt: string;
    payloadHash: string;
    events: AgencyPurposeRelationEvent[];
}

export interface AgencyPurposeDetail {
    purpose: AgencyPurpose;
    events: AgencyPurposeEvent[];
    evidence: AgencyPurposeEvidence[];
    relations: AgencyPurposeRelation[];
}

export interface BackgroundUnderstandingSource {
    source: "local_activity" | "gmail";
    label: string;
    available: boolean;
    connected: boolean;
    allowed: boolean;
    grantId?: string;
    scopeLabel?: string;
    sourceQuery?: string;
    lookbackDays?: number;
    maxItemsPerRun?: number;
    lastRunAt?: string;
    lastRunState?: string;
    lastRunSummary?: string;
    unavailableReason?: string;
}

export interface BackgroundUnderstandingRun {
    id: string;
    missionKind: "retained_activity" | "gmail";
    source: "local_activity" | "gmail";
    state:
        | "selected"
        | "running"
        | "completed"
        | "skipped"
        | "failed"
        | "cancelled";
    triggerKind: "automatic" | "manual";
    triggerReason: string;
    startedAt: string;
    updatedAt: string;
    summary: string;
    sourceItemCount: number;
    materializationCount: number;
}

export interface BackgroundUnderstandingSnapshot {
    enabled: boolean;
    status:
        | "unavailable"
        | "paused"
        | "waiting"
        | "waiting_for_source"
        | "ready"
        | "working"
        | "limit_reached";
    statusReason: string;
    currentRunId?: string;
    schedulingPolicyVersion: string;
    dailyRunLimit: number;
    todayRunCount: number;
    todayAttemptCount: number;
    reservedRunCount: number;
    reservedSource?: "gmail";
    minIntervalMinutes: number;
    sources: BackgroundUnderstandingSource[];
    runs: BackgroundUnderstandingRun[];
}

function installBrowserProxy(): void {
    const w = globalThis as any;
    if (!w || !w.window) return;
    const win = w.window;
    if (win.go?.main?.App) return; // native Wails already provided it

    const handler: ProxyHandler<Record<string, any>> = {
        get(_t, prop: string) {
            if (prop === "then") return undefined; // not a thenable
            return (...args: any[]) => callAppMethod(prop, ...args);
        },
    };
    const appProxy = new Proxy({}, handler);
    win.__yansuBrowserProxy = true;
    win.go = win.go ?? {};
    win.go.main = win.go.main ?? {};
    win.go.main.App = appProxy;
}

installBrowserProxy();

type VoiceInputAppEntry = {
    bundleId: string;
    appName: string;
    isDefault: boolean;
};

function appBinding<TArgs extends unknown[], TResult>(
    name: string,
): (...args: TArgs) => Promise<TResult> {
    return (...args: TArgs) => {
        const app = (globalThis as any).window?.go?.main?.App;
        const fn = app?.[name];
        if (typeof fn !== "function") {
            return Promise.reject(
                new Error(`Wails binding ${name} is not available`),
            );
        }
        return timePageBinding(name, () => fn(...args) as Promise<TResult>);
    };
}

export interface MCPReadConnection {
    client: string;
    name: string;
    command: string;
    connected: boolean;
    available: boolean;
    authorized: boolean;
    availabilityReason?: "assistant_missing" | "isoform_missing";
    issue?: "name_conflict";
    conflictCommand?: string;
}

export const GetMCPReadConnections = appBinding<[], MCPReadConnection[]>("GetMCPReadConnections");
export const ConnectMCPReadAssistant = appBinding<[string], MCPReadConnection>("ConnectMCPReadAssistant");
export const RevokeMCPReadAssistant = appBinding<[string], MCPReadConnection>("RevokeMCPReadAssistant");

/** One assistant's health line in Settings. `code` names the state; the page words it. */
export interface AssistantHealthView {
    assistant: string;
    code: string;
    checking: boolean;
    retryAt?: string;
}

export const GetAssistantHealth = appBinding<[], AssistantHealthView[]>("GetAssistantHealth");

/** R75: the chosen assistant whose sign-in is confirmed gone; `signedOut` false when none. */
export interface AssistantSignOutView {
    assistant: string;
    signedOut: boolean;
}

export const GetAssistantSignOut = appBinding<[], AssistantSignOutView>("GetAssistantSignOut");
export const RetryAssistantHealth = appBinding<[string], AssistantHealthView[]>("RetryAssistantHealth");

/**
 * One agent's model and effort in Settings. An empty model is the agent's own
 * default; effort is what its runs use (High until changed, empty when the
 * model offers none). modelEfforts lists the steps magpie offers per model.
 */
export interface AgentChoiceView {
    agent: string;
    /** Whether the row can be picked; Codex and DeepSeek Harness cannot answer yet. */
    canAnswer: boolean;
    /** Set when an agent without a health line (DeepSeek Harness) has no CLI on this computer. */
    notInstalled?: boolean;
    model: string;
    effort: string;
    models: string[];
    modelEfforts: Record<string, string[]>;
    efforts: string[];
}

/** Who answers in Isoform, and each agent's model and effort. Reason identifies the model source when it needs an explanation. */
export interface AssistantChoiceView {
    answering: string;
    agents: AgentChoiceView[];
    backgroundUsesChoice: boolean;
    editable: boolean;
    reason?: string;
    isoformReady: boolean;
}

export const GetAssistantChoice = appBinding<[], AssistantChoiceView>("GetAssistantChoice");
export const SetAnsweringAgent = appBinding<[string], AssistantChoiceView>("SetAnsweringAgent");
export const SetAgentModel = appBinding<[string, string], AssistantChoiceView>("SetAgentModel");
export const SetAgentEffort = appBinding<[string, string], AssistantChoiceView>("SetAgentEffort");
export const SetBackgroundUsesChoice = appBinding<[boolean], AssistantChoiceView>("SetBackgroundUsesChoice");
export const OpenAssistantSignIn = appBinding<[string], void>("OpenAssistantSignIn");

export interface MagpieProfileMember { model: string; effort: string }
export interface MagpieProfileView { members: MagpieProfileMember[]; models: string[]; backgroundUsesChoice: boolean }
export const GetMagpieProfile = appBinding<[], MagpieProfileView>("GetMagpieProfile");
export const SetMagpieProfile = appBinding<[MagpieProfileMember[]], MagpieProfileView>("SetMagpieProfile");

export interface PluginInfo {
    id: string;
    nameKey: string;
    descriptionKey: string;
    version: string;
    kind: string;
    source: string;
    enabled: boolean;
    essential: boolean;
    lockedReasonKey: string;
    readErrorKey: string;
}

export const ListPlugins = appBinding<[], PluginInfo[]>("ListPlugins");
export const SetBuiltinOn = appBinding<[string, boolean], PluginInfo[]>("SetBuiltinOn");
export const SetActivityMonitorEnabled = appBinding<[boolean], void>("SetActivityMonitorEnabled");
export const CaptureAndSaveWindowState = appBinding<[], void>("CaptureAndSaveWindowState");

// iPhone companion contract: mobile-protocol/PROTOCOL.md section 12.
export interface MobileDeviceView {
    deviceId: string;
    deviceName: string;
    createdAt: string;
    expiresAt: string;
    lastSeenAt: string;
    scopes: string[];
}

export interface MobilePendingPairing {
    pairingId: string;
    deviceName: string;
    code: string;
}

export type MobileRelayState = "disabled" | "connecting" | "connected" | "offline";

export interface MobileRemoteAccessStatus {
    state: "off" | "starting" | "ready" | "restarting" | "failed";
    lastError?: string;
}

export type MobileRemoteAccessOffReason = "saved_relay" | "environment_relay" | "build_relay" | "disabled";

export interface MobileConnectionStatus {
    enabled: boolean;
    relayUrl: string;
    savedRelayUrl: string;
    relayState: MobileRelayState;
    relayLastSeenAt: string;
    warning?: string;
    warningCode?: string;
    warningHost?: string;
    remoteAccessOffReason?: MobileRemoteAccessOffReason;
    hubId: string;
    hubName: string;
    connectionCode: string;
    expiresAt: string;
    pendingPairing: MobilePendingPairing | null;
    devices: MobileDeviceView[];
    remoteAccess?: MobileRemoteAccessStatus;
    // true when this Mac's relay has a push gateway, so notices arrive at once.
    instantPush: boolean;
}

// "Keep this computer awake for your phone" (mobile_keep_awake.go).
export interface MobileKeepAwakeStatus {
    on: boolean;
    pairedPhones: number;
    onPower: boolean;
    hasBattery: boolean;
}

export interface MobileBriefSchedule {
    enabled: boolean;
    morning: string;
    evening: string;
    adaptiveTiming?: boolean;
    // Additive schedule estimates; older hubs omit these RFC 3339 time and reason fields.
    nextPredictedAt?: string;
    nextPredictedSlot?: string;
    timingReason?: string;
    lastGeneratedAt: string;
    lastSlot: string;
    narrativeAvailable: boolean;
    // The private podcast feed served from this Mac; feedUrl is set while it is on.
    feedEnabled: boolean;
    feedUrl: string;
}

export type MobileBriefRequestStatus = "started" | "running" | "cooldown";

export interface MobileBriefRequestResult {
    status: MobileBriefRequestStatus;
    nextAllowedAt: string;
}

export const GetMobileConnectionStatus = appBinding<[], MobileConnectionStatus>(
    "GetMobileConnectionStatus",
);
export const EnableMobileConnection = appBinding<[string], MobileConnectionStatus>(
    "EnableMobileConnection",
);
export const EnableMobileConnectionUsingRelay = appBinding<
    [string],
    MobileConnectionStatus
>("EnableMobileConnectionUsingRelay");
export const DisableMobileConnection = appBinding<[], MobileConnectionStatus>(
    "DisableMobileConnection",
);
export const GenerateMobileConnectionCode = appBinding<[], MobileConnectionStatus>(
    "GenerateMobileConnectionCode",
);
export const ConfirmMobilePairing = appBinding<
    [string, boolean],
    MobileConnectionStatus
>("ConfirmMobilePairing");
export const RevokeMobileDevice = appBinding<[string], MobileConnectionStatus>(
    "RevokeMobileDevice",
);
export const ResetMobileRemoteAccess = appBinding<[], MobileConnectionStatus>(
    "ResetMobileRemoteAccess",
);
export const GetMobileKeepAwake = appBinding<[], MobileKeepAwakeStatus>(
    "GetMobileKeepAwake",
);
export const SetMobileKeepAwake = appBinding<[boolean], MobileKeepAwakeStatus>(
    "SetMobileKeepAwake",
);
export const GetMobileBriefSchedule = appBinding<[], MobileBriefSchedule>(
    "GetMobileBriefSchedule",
);
export const SetMobileBriefSchedule = appBinding<
    [MobileBriefSchedule],
    MobileBriefSchedule
>("SetMobileBriefSchedule");
export const GetTodayYourWeekDeclined = appBinding<[string], boolean>("GetTodayYourWeekDeclined");
export const DeclineTodayYourWeek = appBinding<[string], void>("DeclineTodayYourWeek");
export const GenerateTodayMissedBrief = appBinding<[string], MobileBriefRequestResult>("GenerateTodayMissedBrief");
export const GenerateMobileBriefNow = appBinding<[], MobileBriefRequestResult>(
    "GenerateMobileBriefNow",
);
export const SetMenuBarBriefPlayback = appBinding<[string, string, number, number], void>(
    "SetMenuBarBriefPlayback",
);
export const GetAutomaticMeetingRecordingEnabled = appBinding<[], boolean>(
    "GetAutomaticMeetingRecordingEnabled",
);
export const SetAutomaticMeetingRecordingEnabled = appBinding<[boolean], void>(
    "SetAutomaticMeetingRecordingEnabled",
);
// The computer's own sound outside calls (videos, podcasts): off unless the
// user turns it on. Mirrors wails-gui/background_listening_outside_calls.go.
export const GetSystemAudioOutsideCallsEnabled = appBinding<[], boolean>(
    "GetSystemAudioOutsideCallsEnabled",
);
export const SetSystemAudioOutsideCallsEnabled = appBinding<[boolean], void>(
    "SetSystemAudioOutsideCallsEnabled",
);
// The model that transcribes a call again once it has ended. Mirrors
// wails-gui/after_call_speech_model.go.
export interface AfterCallSpeechModelView {
    supported: boolean;
    name: string;
    downloadBytes: number;
    diskBytes: number;
    enabled: boolean;
    status: "absent" | "downloading" | "ready" | "low_disk" | "failed";
    downloaded: number;
    total: number;
    lastError: string;
    folder: string;
    removable: boolean;
}
export const GetAfterCallSpeechModel = appBinding<[], AfterCallSpeechModelView>("GetAfterCallSpeechModel");
export const SetAfterCallSpeechModelEnabled = appBinding<[boolean], void>("SetAfterCallSpeechModelEnabled");
export const RemoveAfterCallSpeechModel = appBinding<[], void>("RemoveAfterCallSpeechModel");
// Settings > Privacy: ask before risky Bash commands in attended chats.
// Mirrors risky_commands_config.go.
export const GetAskBeforeRiskyCommands = appBinding<[], boolean>(
    "GetAskBeforeRiskyCommands",
);
export const SetAskBeforeRiskyCommands = appBinding<[boolean], void>(
    "SetAskBeforeRiskyCommands",
);
// Pause observing (closed-eyes mode), the same state as the menu bar's Pause
// Observation. Go emits "closed-eyes:changed" with the new paused state from
// every surface that changes it. Mirrors wails-gui/closed_eyes_mode.go.
export const IsClosedEyesModeEnabled = appBinding<[], boolean>(
    "IsClosedEyesModeEnabled",
);
export const SetClosedEyesModeEnabled = appBinding<[boolean], void>(
    "SetClosedEyesModeEnabled",
);

// Text channel: Isoform texts the user's own number through Messages when it
// cannot reach their iPhone. Mirrors wails-gui/text_channel.go.
export interface TextChannelStatus {
    supported: boolean;
    enabled: boolean;
    number: string;
    includeNames: boolean;
    consentAccepted: boolean;
    permissionGranted: boolean;
    sentToday: number;
    lastSentAt: string;
    lastError: string;
    verified: boolean;
    verifying: boolean;
}

export interface TextChannelInput {
    enabled: boolean;
    number: string;
    includeNames: boolean;
    acceptConsent: boolean;
}

export const GetTextChannelStatus = appBinding<[], TextChannelStatus>(
    "GetTextChannelStatus",
);
export const SetTextChannel = appBinding<[TextChannelInput], TextChannelStatus>(
    "SetTextChannel",
);
export const SendTestText = appBinding<[], TextChannelStatus>("SendTestText");
export const StartTextVerification = appBinding<[], TextChannelStatus>("StartTextVerification");
export const ConfirmTextVerification = appBinding<[string], TextChannelStatus>("ConfirmTextVerification");

// Text messages through the relay: the user subscribes their number with a
// code, and Isoform texts it when it cannot reach a paired iPhone. A failed
// call rejects with a short code (see SMS_ERROR_KEYS in
// sms-text-settings.tsx). Mirrors wails-gui/sms_channel.go.
export interface SMSChannelStatus {
    available: boolean;
    state: string;
    number: string;
    pendingNumber: string;
    codeExpiresAt: string;
    sentToday: number;
    sentThisMonth: number;
    dailyCap: number;
    monthlyCap: number;
    lastError: string;
}

export const GetSMSChannelStatus = appBinding<[], SMSChannelStatus>("GetSMSChannelStatus");
export const StartSMSVerification = appBinding<[string], SMSChannelStatus>("StartSMSVerification");
export const ConfirmSMSVerification = appBinding<[string], SMSChannelStatus>("ConfirmSMSVerification");
export const StopSMSChannel = appBinding<[], SMSChannelStatus>("StopSMSChannel");

// Email through the relay: the user verifies their own address with a code,
// and a playbook's result can go there. The relay mails only that address. A
// failed call rejects with a short code (see EMAIL_ERROR_KEYS in
// email-settings.tsx). Mirrors wails-gui/email_channel.go.
export interface EmailChannelStatus {
    available: boolean;
    state: string;
    address: string;
    pendingAddress: string;
    codeExpiresAt: string;
    sentToday: number;
    dailyCap: number;
    lastError: string;
}

export const GetEmailChannelStatus = appBinding<[], EmailChannelStatus>("GetEmailChannelStatus");
export const StartEmailVerification = appBinding<[string], EmailChannelStatus>("StartEmailVerification");
export const ConfirmEmailVerification = appBinding<[string], EmailChannelStatus>("ConfirmEmailVerification");
export const StopEmailChannel = appBinding<[], EmailChannelStatus>("StopEmailChannel");

// Notification controls: every proactive kind has a switch, says when it paused
// itself after too many dismissals, and shows what it earned over the stats
// window. The phone reads the same view through the hub.
export interface EngagementKindView {
    kind: string;
    enabled: boolean;
    defaultOn: boolean;
    paused: boolean;
    mutedUntil?: string;
    /** Set when the kind shows as a quiet line on Today instead of
     *  interrupting. */
    quietOnToday: boolean;
    deliveryMode: "normal" | "quiet_on_today";
    /** False for kinds that need the user's answer (a decision due, a
     *  permission request): they always interrupt. */
    quietAllowed: boolean;
    quietSuggestionAllowed: boolean;
    sent: number;
    opened: number;
    acted: number;
    dismissed: number;
    muted: number;
    /** RC 1 A5 fields are absent on older computers. Copy keys resolve the
     * engagement.NotificationSampleCopy's generic PushBody without personal params. */
    channels?: ("computer" | "phone" | "text" | "email" | "today_quiet")[];
    /** Interruption counts drive tags and suggestions; quiet history is separate. */
    notificationSent?: number;
    notificationOpenedOrActed?: number;
    notificationActed?: number;
    notificationUseful?: number;
    quietSent?: number;
    quietOpenedOrActed?: number;
    sampleCopyKey?: string;
    quiet?: boolean;
    canQuiet?: boolean;
    tag?: "" | "noisy" | "ignored" | "needs_you" | "rare" | "quiet";
}

// The dial position the switches and rules match. "custom" is reported, never
// set: it means a switch or rule was changed by hand after a level was picked.
export type EngagementLevel = "off" | "fewer" | "normal" | "more" | "custom";

export interface EngagementSettingsView {
    available: boolean;
    level: EngagementLevel;
    kinds: EngagementKindView[];
    quietStart: string;
    quietEnd: string;
    dailyInterruptBudget: number;
    digestTimes: string[];
    statsDays: number;
    summary?: { sent: number; useful?: number; openedOrActed: number; quietSent?: number; quietOpenedOrActed?: number };
}

export const GetEngagementSettings = appBinding<[], EngagementSettingsView>(
    "GetEngagementSettings",
);
export const SetEngagementKindEnabled = appBinding<[string, boolean], EngagementSettingsView>(
    "SetEngagementKindEnabled",
);
// Sets whether one kind shows as a quiet line on Today instead of
// interrupting; kinds that need the user's answer refuse with an error.
export const SetEngagementKindQuiet = appBinding<[string, boolean], EngagementSettingsView>(
    "SetEngagementKindQuiet",
);

// One stretch a quiet kind reports for a day, RFC3339 UTC. `end` is empty
// while the stretch is open; an unknown recovery has no end.
export interface QuietLineInterval {
    start: string;
    end?: string;
}

// One quiet kind's day on Today: when its moments happened and how many were
// delivered quietly. capture_paused also carries the day's real pause and
// outage intervals. The UI writes the sentence from these fields.
export interface QuietLine {
    kind: string;
    firstAt: string;
    lastAt: string;
    count: number;
    intervals: QuietLineInterval[];
}

// Today's quiet lines: one per kind showing on Today instead of interrupting
// that had something to say today.
export const GetTodayQuietLines = appBinding<[], QuietLine[]>(
    "GetTodayQuietLines",
);

// The editable global rules, times as HH:MM. The same start and end means no
// quiet hours; a zero budget means no cap.
export interface EngagementRulesInput {
    quietStart: string;
    quietEnd: string;
    dailyInterruptBudget: number;
}

export const SetEngagementRules = appBinding<[EngagementRulesInput], EngagementSettingsView>(
    "SetEngagementRules",
);

// The one dial: off, fewer, normal or more. It rewrites every kind switch, the
// budget, the digest times and the quiet hours in one step.
export const SetEngagementLevel = appBinding<[EngagementLevel], EngagementSettingsView>(
    "SetEngagementLevel",
);

// The weekly line on Today: moments sent in the last `days` days and how many
// of them completed their linked item within two hours.
export interface EngagementWeekKind {
    kind: string;
    created: number;
    scheduled: number;
    sent: number;
    delivered: number;
    opened: number;
    acted: number;
    dismissed: number;
    muted: number;
    actRate: number;
}

export interface EngagementWeekSummary {
    available: boolean;
    sent: number;
    actedOn: number;
    days: number;
    kinds: EngagementWeekKind[];
}

export const GetEngagementWeekSummary = appBinding<[], EngagementWeekSummary>(
    "GetEngagementWeekSummary",
);

// What Isoform captured, today and in the last seven days (today included):
// the card in Settings > Observation and the line on Today. While observing
// is paused the answer is { paused: true } with no sources; while activity.db
// is still opening (at launch) it is { notReady: true } with no sources, and
// no surface shows numbers then. `since` is local midnight in Unix ms; 0
// lets Go use its own.
export type CaptureCountSource =
    | "screens"
    | "textRead"
    | "audioMinutes"
    | "meetings"
    | "typing"
    | "calendarEvents"
    | "calendarPeeks";

export interface CaptureCountRow {
    source: CaptureCountSource;
    today: number;
    week: number;
}

export interface CaptureCounts {
    paused: boolean;
    notReady: boolean;
    todayStart: number;
    weekStart: number;
    sources: CaptureCountRow[] | null;
}

export interface CaptureHour {
    hourStart: number;
    screens: number;
    speechMinutes: number;
}

export interface CaptureByHour {
    paused: boolean;
    notReady: boolean;
    dayStart: number;
    hours: CaptureHour[] | null;
}

export interface AppSuggestion {
    name: string;
    bundleId: string;
    activeMs: number;
    category: "finance" | "health" | "messaging";
}

export const GetCaptureByHour = appBinding<[], CaptureByHour>("GetCaptureByHour");
export const GetPrivateAppSuggestions = appBinding<[], AppSuggestion[]>("GetPrivateAppSuggestions");

export const GetCaptureCounts = appBinding<[number], CaptureCounts>("GetCaptureCounts");

// What Isoform is doing right now: the line at the top of the menu-bar menu
// and in the sidebar's ObservingStatus tooltip. app names the meeting app of
// "meeting", title the task of "working"; either may be missing. Changes
// arrive as the doing-now:changed event.
export type DoingNowKind =
    | "paused"
    | "meeting"
    | "calendar"
    | "working"
    | "watching"
    | "listening"
    | "idle";

export interface DoingNow {
    kind: DoingNowKind;
    pausedUntil?: number;
    frontApp?: string;
    frontAppHidden?: boolean;
    microphone?: "listening" | "paused" | "off";
    inCall?: boolean;
    app?: string;
    title?: string;
}

export const GetDoingNow = appBinding<[], DoingNow>("GetDoingNow");

// The spoken version of a brief, stored beside it on the Mac. Attachment is
// set once a sealed copy reached the relay for paired phones.
export interface MobileBriefAudio {
    status?: string;
    mime: string;
    bytes: number;
    durationSeconds: number;
    attachment?: { id: string; key: string; nonce: string };
}

// Scheduled brief health: last run, next run, missed runs in the last seven
// days, a slot whose build is failing, and the newest brief with its spoken
// version when one was stored. Times are RFC 3339; empty means unknown.
export interface MobileBriefHealth {
    enabled: boolean;
    lastRunAt: string;
    lastSlot: string;
    nextRunAt: string;
    nextSlot: string;
    missedRuns: number;
    failingSlot: string;
    failedAttempts: number;
    /** Today's slot whose time has passed with no brief yet, its scheduled
     *  time, and the next try after a failure; empty when none. */
    pendingSlot?: string;
    pendingAt?: string;
    retryAt?: string;
    /** Failed tries of the pending slot, from its run record. */
    pendingAttempts?: number;
    /** Why the last try failed, when Isoform knows (e.g. "timeout"). */
    pendingCause?: string;
    pendingAssistant?: string;
    latestBriefId: string;
    /** The current brief (the one Today, the menu bar and Chat show). */
    currentBriefId: string;
    audio?: MobileBriefAudio;
}

export const GetMobileBriefHealth = appBinding<[], MobileBriefHealth>("GetMobileBriefHealth");

// The brief document (wails-gui/mobile_brief_types.go, PROTOCOL.md section 8)
// as Today reads it. Times are RFC 3339.
export interface MobileBriefScheduleSpec {
    kind: string;
    expression?: string;
    timezone?: string;
    everyMs?: number;
    atMs?: number;
    nextRunAt: number;
}

export interface MobileBriefItemContext {
    preview?: string;
    schedule?: string; // Legacy stored briefs.
    scheduleSpec?: MobileBriefScheduleSpec;
    jobId?: string;
    fileCount?: number;
    blockedReason?: string;
    sourceLabel?: string;
}

export interface MobileBriefItem {
    id: string;
    /** Stable source identity across briefs; absent in older documents. */
    key?: string;
    title: string;
    detail: string;
    status: string;
    at: string;
    /** Meeting end time; absent in older stored briefs. */
    endAt?: string;
    kind: string;
    refs: string[];
    context?: MobileBriefItemContext;
    /** An http or https link: a meeting's join link or a find's page. */
    url?: string;
    lastTouch?: string;
    nextStep?: string;
    dueAt?: string;
    /** Show only the purpose title, state and unavailable-evidence notice. */
    evidenceUnavailable?: boolean;
    /** Goal identity for rows without observation sources. */
    purposeId?: string;
    /** One line on why the item needs the user's attention now. Briefs that
     *  carry it list only such items under "What needs your attention". */
    attentionReason?: string;
    /** The attention tier, 1 (overdue) to 7 (stalled progress). */
    attentionRank?: number;
    /** When Isoform first recorded the item (RFC 3339). */
    since?: string;
    /** References the existing proactive moment; the brief never posts it again. */
    momentId?: string;
    chatSessionId?: string;
    chatMessageId?: string;
}

export interface MobileBriefRef {
    ref: string;
    sourceKind: string;
    sourceId: string;
    label: string;
    observedAt: string;
}

export interface MobileBriefSections {
    moved: MobileBriefItem[];
    doing: MobileBriefItem[];
    watching: MobileBriefItem[];
    meetings: MobileBriefItem[];
    people: MobileBriefItem[];
    openLoops: MobileBriefItem[];
    learned: MobileBriefItem[];
    deliverables: MobileBriefItem[];
    schedules: MobileBriefItem[];
    workingToward?: MobileBriefItem[];
    commitments?: MobileBriefItem[];
    waitingOnYou?: MobileBriefItem[];
    noticed?: MobileBriefItem[];
}

export interface MobileBriefDay extends Partial<TodayDayShape> {
    activeMinutes: number;
    hours: { hour: number; activeMinutes: number }[];
    topApps: { name: string; activeMinutes: number }[];
    firstActive?: string;
    lastActive?: string;
    breakMinutes?: number;
}

export interface MobileBriefOffer {
    id: string;
    /** "send" starts main chat; "prefill" is editable first. Older offer
     *  kind values also mean "prefill". "open_activity" navigates to its
     *  target and never enters chat. */
    action: string;
    target?: MobileBriefActivityTarget;
    /** What the offer does: plan, reply, follow_up, order, ... */
    kind?: string;
    /** Chip text; newer briefs name the offer's subject in it. */
    label: string;
    text: string;
    prompt: string;
    refs: string[];
}

/** Navigation to an editable suggestion on Activity; saving is separate. */
export interface MobileBriefActivityTarget {
    date: string;
    suggestionId: string;
    action: "follow_up" | "routine";
    schedule?: TodayNoticedSchedule;
}

/** One prepared click on the brief, made with it and checked again by the
 *  computer at the click. Today shows its words; it never writes or edits
 *  them. */
export interface MobileBriefCard {
    id: string;
    /** Present only on answers the computer bound to the closing question. */
    answerTo?: string;
    /** "action": a button; "item": a row's question; "opener": what Isoform
     *  says first for "Talk about this brief". */
    kind: "action" | "item" | "opener" | string;
    /** "send": the message is the user's and Isoform answers at once;
     *  "speak_first": Isoform says it, and the user answers;
     *  "open_activity": opens an editable suggestion on Activity. */
    mode: "send" | "speak_first" | "open_activity" | string;
    target?: MobileBriefActivityTarget;
    /** An action's button text: a verb and the thing. */
    label?: string;
    message: string;
    refs: string[];
    /** The row an item card belongs to: the item's key, or its id. */
    itemKey?: string;
    /** A "question" card's paragraph: its index in the narrative (RC 1 A2,
     *  provisional until the brief's backend lands). */
    paragraph?: number;
}

/** One paragraph of the brief. topicId and topicName name the topic it is
 *  about (RC 1 A2, provisional; omitted by older briefs). */
export interface MobileBriefParagraph {
    hold?: TodayDayShapeHold | null;
    text: string;
    refs: string[];
    topicId?: string;
    topicName?: string;
}

export interface MobileBriefDocument {
    displaySlot?: "morning" | "evening";
    conversationKey?: string;
    offers?: MobileBriefOffer[];
    /** Prepared clicks; absent on briefs read before cards existed. */
    cards?: MobileBriefCard[];
    upcoming?: MobileBriefItem[];
    version: number;
    briefId: string;
    slot: "morning" | "evening" | "on_request" | string;
    localDate: string;
    timezone?: string;
    asOf: string;
    coldStart: boolean;
    /** The first brief used the Claude Code or Codex history (first-day handoff §3). */
    fromHistory?: boolean;
    narrativeStatus: "ok" | "unavailable" | "skipped" | string;
    /** Present when the brief was made after its time: the computer was
     *  asleep then ("mac_asleep"), or Isoform was closed or its first tries
     *  failed ("late"). */
    generation?: { reason: string; scheduledAt: string; generatedAt: string; message: string };
    narrative: MobileBriefParagraph[];
    /** "Your week" as paragraphs (older phones, the recording). */
    weeklyRecap?: { text: string; refs: string[] }[];
    /** "Your week", the Friday evening look back (Go your_week.go). */
    yourWeek?: MobileBriefWeek;
    weekQuestion?: { weekStart: string; recommendation?: string };
    sections: MobileBriefSections;
    dayShape: MobileBriefDay | null;
    evidence: MobileBriefRef[];
    limits?: {
        itemsPerSection: number;
        evidenceRefs: number;
        truncatedSections: string[];
        excludedSensitiveItems?: number;
    };
    audio?: MobileBriefAudio;
}

// "Your week" (wails-gui/your_week.go, docs/your-week/handoff.md). Every part
// may be absent; Go never sends a zero or a system count.
export interface MobileBriefWeek {
    start: string;
    end: string;
    lead: string;
    time?: { topicId?: string; name?: string; other?: boolean; hours: number }[];
    done?: {
        id: string;
        kind: "outcome" | "deliverable" | "meetings" | string;
        taskId?: string;
        title?: string;
        at?: string;
        count?: number;
        source?: string;
        sourceId?: string;
        path?: string;
    }[];
    doneMore?: number;
    open?: { itemKey: string; cardId?: string; title: string; since?: string; label?: string }[];
    learned?: { id: string; kind: "memory" | "mute" | string; text: string; memoryId?: string; engagementKind?: string }[];
    ask?: { day?: string; text: string; message: string; cardId?: string; topicId?: string; hold?: TodayDayShapeHold };
}

export interface YourWeekEntry {
    briefId?: string;
    timeZone?: string;
    week?: MobileBriefWeek;
    cards: MobileBriefCard[];
}

// Today's brief (wails-gui/brief_today.go): today's newest brief, whether one
// is being built, the next slot, and what Isoform cannot see when it is empty.
export type TodayBriefMissing = "screen_recording" | "accessibility" | "calendars";

export interface TodayBriefView {
    weekAnswer?: { weekStart: string; answer: string };
    closingAnswer?: { cardId: string; label: string; questionDigest: string };
    quiet?: boolean;
    nothingNew?: boolean;
    quietSince?: string;
    quietScreens?: number;
    /** Local activity text; never part of the stored or paired-phone brief. */
    dailySummary?: { text: string; at: string };
    /** No brief has ever been made and a slot is due. */
    firstBrief?: boolean;
    brief: MobileBriefDocument | null;
    briefRun?: BriefScheduleRun;
    building: boolean;
    enabled: boolean;
    nextAt: string;
    nextSlot: string;
    /** Today's slot whose time has passed with no brief yet: Today says it is
     *  on its way. pendingAt is its time, retryAt the next try after a failure. */
    pendingSlot?: string;
    pendingAt?: string;
    retryAt?: string;
    /** Failed tries of the pending slot, only when its run record counts them. */
    pendingAttempts?: number;
    /** Why the last try failed, when Isoform knows (e.g. "timeout"). */
    pendingCause?: string;
    pendingAssistant?: string;
    /** The newest stored brief when it is not the one shown, to open it. */
    lastBriefId?: string;
    lastSlot?: string;
    lastAt?: string;
    empty: boolean;
    missing: TodayBriefMissing[];
}

export interface TodayDayShapeHold {
    start: string;
    end: string;
    label: string;
}

export interface TodayDayShapeEvent {
    id: string;
    start: string;
    end: string;
    title: string;
    shortName: string;
    shortestName: string;
    initial?: string;
    kind: "meeting" | "personal";
    /** Present only for a privacy-filtered event created by Isoform. */
    holdId?: string;
    /** A private event: no names, and the strip says only "Private". */
    private?: boolean;
}

export interface TodayDayShape {
    rangeStart: string;
    rangeEnd: string;
    now: string;
    events: TodayDayShapeEvent[];
    hold: TodayDayShapeHold | null;
    calendarConnected: boolean;
}

/** Local today/tomorrow only; rejects other dates with TODAY_DAY_SHAPE_DATE_INVALID. */
export const GetTodayDayShape = appBinding<[dateISO: string], TodayDayShape>("GetTodayDayShape");
/** Read the same privacy-filtered strip for a day within the hold proposal window. */
export const GetHoldDayShape = appBinding<[dateISO: string], TodayDayShape>("GetHoldDayShape");
export type FindTimeRange = { start: string; end: string };
export const GetFindTime = appBinding<[], FindTimeRange[]>("GetFindTime");
export interface FindTimeDetails { ranges: FindTimeRange[]; checkedCalendars: string[]; browserOnly: string[] }
export const GetFindTimeDetails = appBinding<[], FindTimeDetails>("GetFindTimeDetails");
export const GetFindTimeDetailsForRange = appBinding<[string, string, number], FindTimeDetails>("GetFindTimeDetailsForRange");

export interface TodayBeforeYouTalk {
    meetingId: string;
    meetingTitle: string;
    startAt: number;
    endAt: number;
    conferenceUrl?: string;
    personId: string;
    personName: string;
    youOwe?: string;
    theyAsked?: string;
    hasUnsentDraft?: boolean;
    lastTime?: string;
    lastTimeAt?: number;
    lastDurationMs?: number;
}

export const GetTodayBeforeYouTalk = appBinding<[], TodayBeforeYouTalk | null>("GetTodayBeforeYouTalk");
export const DismissTodayBeforeYouTalk = appBinding<[meetingID: string], void>("DismissTodayBeforeYouTalk");

export const GetLatestBrief = appBinding<[], TodayBriefView>("GetLatestBrief");
export const GetTodayBriefByID = appBinding<[briefID: string], TodayBriefView>("GetTodayBriefByID");
export const SaveMorningBriefWeekIntent = appBinding<[briefID: string, answer: string], void>("SaveMorningBriefWeekIntent");
export const PostMorningBriefWeekAnswer = appBinding<[briefID: string, previous: string, answer: string], void>("PostMorningBriefWeekAnswer");
export const CancelTodayReplyDraft = appBinding<[string], void>("CancelTodayReplyDraft");
export const OpenTodayReplyDraft = appBinding<[string, string, string, string, string], string>("OpenTodayReplyDraft");
/** Brief me before we talk, in a person's detail on Who you know. */
export interface PersonBrief {
    personId: string;
    youOwe?: string;
    theyAsked?: string;
    hasUnsentDraft?: boolean;
    lastTime?: string;
    lastTimeAt?: number;
    lastDurationMs?: number;
}
export const GetPersonBrief = appBinding<[string], PersonBrief | null>("GetPersonBrief");
/** Draft a reply in a person's detail: (personId, momentId, requestId). */
export const OpenPersonReplyDraft = appBinding<[string, string, string], string>("OpenPersonReplyDraft");
export const PostTodayClosingAnswer = appBinding<[briefID: string, cardID: string, shownMessage: string, previous: string], NonNullable<TodayBriefView["closingAnswer"]>>("PostTodayClosingAnswer");
export const ChangeMorningBriefWeekIntent = appBinding<[briefID: string, previous: string, answer: string], void>("ChangeMorningBriefWeekIntent");
export const GetYourWeekForDate = appBinding<[string], YourWeekEntry>("GetYourWeekForDate");

// New finds on the user's topics (wails-gui/topic_finds.go).
export interface TopicFind {
    id: string;
    topicId: string;
    topic: string;
    title: string;
    source: string;
    url: string;
    whatsNew: string;
    lookedAt?: string;
    publishedAt?: string;
    score: number;
    opened?: boolean;
    feedback?: "" | "more" | "less";
}

export type TopicFindsStatus =
    | "off"
    | "loading"
    | "unconfigured"
    | "no_topics"
    | "finding"
    | "ready"
    | "none"
    | "error"
    /** The last search could not reach the web. */
    | "unavailable";

export interface TopicFindsView {
    enabled: boolean;
    status: TopicFindsStatus;
    items: TopicFind[];
    topics: string[];
    ranAt?: string;
    /** When Find new works again after the last press (RFC 3339). */
    cooldownUntil?: string;
}

export type TopicFindFeedbackAction = "open" | "more" | "less" | "not_interested";

export const GetTopicFinds = appBinding<[], TopicFindsView>("GetTopicFinds");
export const FindNewOnTopics = appBinding<[], TopicFindsView>("FindNewOnTopics");
export const RecordTopicFindFeedback = appBinding<
    [{ findId: string; action: TopicFindFeedbackAction }],
    TopicFindsView
>("RecordTopicFindFeedback");

// The topics Isoform found the user looking at (wails-gui/topics_service.go):
// the interests the model named, or phrases until it has named any.
export type UserTopicKind = "project" | "research" | "theme" | "routine";

export interface UserTopic {
    id: string;
    label: string;
    original?: string;
    pinned: boolean;
    lastSeenAt?: string;
    sources: string[];
    /** Why the interest matters to the user; only on interests. */
    why?: string;
    kind?: UserTopicKind;
}

export interface UserTopicsView {
    enabled: boolean;
    /** The list is the interests the model named, not the phrase fallback. */
    interests: boolean;
    topics: UserTopic[];
    removed: UserTopic[];
}

export type UserTopicAction = "pin" | "unpin" | "rename" | "remove" | "restore";

export const GetUserTopics = appBinding<[], UserTopicsView>("GetUserTopics");
export const UpdateUserTopic = appBinding<
    [{ id: string; action: UserTopicAction; label: string }],
    UserTopicsView
>("UpdateUserTopic");
export const SetTopicFindsEnabled = appBinding<[boolean], UserTopicsView>("SetTopicFindsEnabled");
export type TopicWhy = main.TopicWhy;
export type TopicView = main.TopicView;
export type TodayTopicsView = main.TodayTopicsView & { firstRun?: boolean };
export const RefreshTodayTopics = appBinding<[], void>("RefreshTodayTopics");
export const GetTodayTopics = appBinding<[], TodayTopicsView>("GetTodayTopics");
export const SetTopicRefreshInterval = appBinding<[number], number>("SetTopicRefreshInterval");
export const PinTopicToSidebar = appBinding<[string, boolean], void>("PinTopicToSidebar");
export const ListSidebarTopics = appBinding<[], TopicView[]>("ListSidebarTopics");

// The Upcoming list (wails-gui/upcoming.go): the user's schedules, the morning
// and evening brief and the Daily News run, ordered by next run. Times are
// Unix milliseconds; zero means none.
export type UpcomingKind = "schedule" | "brief" | "news";
export type UpcomingResult = "" | "done" | "failed" | "skipped" | "missed" | "running";
export type UpcomingReason =
    | ""
    | "asleep"
    | "not_ready"
    | "timeout"
    | "no_model"
    | "busy"
    | "offline"
    | "empty"
    | "retrying"
    | "kept_failing"
    | "other";

export interface UpcomingItem {
    id: string;
    kind: UpcomingKind;
    jobId: string;
    slot: string;
    name: string;
    summary: string;
    enabled: boolean;
    scheduleKind: string;
    scheduleConfig: unknown;
    time: string;
    nextRunAt: number;
    lastRunAt: number;
    lastResult: UpcomingResult;
    lastReason: UpcomingReason;
    failedAttempts: number;
    missedRuns: number;
    system: boolean;
    /** A brief's latest scheduled time (Unix ms) when that run made no brief
     *  and none was made on request after it. */
    didntRunAt?: number;
    /** While Isoform is still trying that run: its failed tries and the cause, when known. */
    triedAttempts?: number;
    triedCause?: string;
    /** The assistant the failed run named, for a sign-in failure. */
    triedAssistant?: string;
}

export const GetUpcoming = appBinding<[], UpcomingItem[]>("GetUpcoming");

// A brief slot's own detail on Tasks › Scheduled: its schedule and the last
// three finished runs, newest first, today's included. reason is set only
// when the run record says why (asleep or closed): on a missed run, or on an
// ok run that came late. madeNow is a brief made with Make it now after the
// slot's time. Times are RFC 3339; nextRunAt is Unix milliseconds.
export interface BriefScheduleRun {
    briefId: string;
    /** A brief made before its slot (P157); `at` is when it was made. */
    early?: boolean;
    /** failed: today's slot whose tries failed while its window is open. */
    status: "ok" | "missed" | "failed";
    scheduledAt: string;
    at: string;
    reason?: "asleep" | "closed";
    late?: boolean;
    madeNow?: boolean;
    /** How many tries failed, when the run record counts them. */
    attempts?: number;
    failureCause?: string;
    failureAssistant?: string;
    waitingForSignIn?: boolean;
    /** A failed slot's next try (RFC 3339), while its window is open. */
    nextTryAt?: string;
}

export interface BriefScheduleDetail {
    slot: "morning" | "evening";
    enabled: boolean;
    time: string;
    nextRunAt: number;
    runs: BriefScheduleRun[];
}

export const GetBriefScheduleDetail = appBinding<[string], BriefScheduleDetail>("GetBriefScheduleDetail");

// The user's answer to the confirm card the main chat posts before removing a
// schedule from Upcoming: true removes it, false keeps it. Only a pending card
// in the main chat that has not expired is acted on.
export interface UpcomingConfirmResult {
    status: "pending" | "removed" | "kept" | "expired" | "gone";
}

export const ResolveUpcomingConfirm = appBinding<[string, boolean], UpcomingConfirmResult>("ResolveUpcomingConfirm");

// The spoken brief as base64 for Today's play control. An empty id means the
// newest brief; the call fails when the brief has no spoken version.
export interface MobileBriefAudioData {
    briefId: string;
    mime: string;
    base64: string;
    durationSeconds: number;
}

export const GetMobileBriefAudio = appBinding<[string], MobileBriefAudioData>("GetMobileBriefAudio");

// This week's suggestion on Today (docs/notifications-design.md section 8):
// a repeated pattern the extractor saw, with its evidence, and what accepting
// it creates. Available is false when there is no open suggestion.
export interface TodaySuggestionView {
    available: boolean;
    id: string;
    title: string;
    description: string;
    times: number;
    days: number;
    evidence: string[];
    schedule: "repeat" | "once" | "";
    scheduleDescription: string;
    suggestedAt: string;
}

export type TodaySuggestionDecisionKind = "add_to_upcoming" | "do_once" | "not_now" | "dismiss";

export interface TodaySuggestionDecision {
    id: string;
    decision: TodaySuggestionDecisionKind;
    reason: string;
}

export const GetTodaySuggestion = appBinding<[], TodaySuggestionView>("GetTodaySuggestion");
export const ResolveTodaySuggestion = appBinding<[TodaySuggestionDecision], TodaySuggestionView>(
    "ResolveTodaySuggestion",
);

// Model accounts: signed-in Claude/Codex subscriptions and keys routed through
// the local magpie sidecar. Refetch on the "model-accounts:updated" event.
export type ModelAccountsMode = "managed" | "advanced" | "external";

export interface ModelSidecarView {
    available: boolean;
    running: boolean;
    healthy: boolean;
    degraded: boolean;
    port: number;
    version: string;
    /** Omitted by Go (omitempty) when the gateway has no error. */
    lastError?: string;
    /** Omitted by Go (omitempty) when unknown. */
    binaryPath?: string;
}

export interface ModelAccountWindowView {
    name: string;
    used: number;
    remaining: number;
    resetsAt: string;
}

export interface ModelAccountView {
    provider: string;
    name: string;
    kind: string;
    plan: string;
    user: string;
    on: boolean;
    balance: string;
    windows: ModelAccountWindowView[];
    readAt: string;
}

export interface ModelGroupView {
    id: string;
    name: string;
    routing: string;
    members: string[];
}

export interface ModelConsentView {
    required: boolean;
    accepted: boolean;
    version: number;
    acceptedAt: string;
}

/**
 * Which magpie Isoform uses: "user" for a magpie the user installed (its
 * gateway and routing group are used as they are) or "bundled" for Isoform's
 * own copy (its groups are built from the models the user's agents use).
 */
export interface MagpieInstallView {
    kind: "user" | "bundled";
    version: string;
    binaryPath: string;
    configDir: string;
    serverUrl: string;
    healthy: boolean;
    // The user's routing group Isoform thinks with (user kind only).
    activeGroup: string;
    groups: ModelGroupView[];
    // The model each agent currently uses, by provider id ("claude", "codex").
    agentModels: Record<string, string>;
}

export interface CLIToolStatus {
    tool: string;
    installed: boolean;
    version: string;
    logged_in: boolean;
    message: string;
}

export interface GitStatus {
    available: boolean;
    path: string;
    recoverable: boolean;
}

export interface DependencyStatus {
    magpie: MagpieInstallView;
    claude: CLIToolStatus;
    codex: CLIToolStatus;
    npm: boolean;
    ollama: boolean;
    git: GitStatus;
}

export interface CLIInstallResult {
    tool: string;
    ok: boolean;
    output: string;
    status: CLIToolStatus;
}

export const CheckDependencies = appBinding<[], DependencyStatus>("CheckDependencies");
export const InstallCLITool = appBinding<[string], CLIInstallResult>("InstallCLITool");

/** Setup's install and re-check (assistant_setup.go). Code is "" when nothing is wrong. */
export type AssistantSetupCode =
    | ""
    | "needsNode"
    | "installDenied"
    | "installTimeout"
    | "installFailed"
    | "notFound"
    | "signedOut"
    | "testSignedOut"
    | "testLimit"
    | "testFailed"
    | "gatewayDown";
export interface AssistantSetupResult {
    id: string;
    installed: boolean;
    signedIn: boolean;
    tested: boolean;
    working: boolean;
    version: string;
    plan: string;
    code: AssistantSetupCode;
    command?: string;
    detail?: string;
}
export const SetupInstallAssistant = appBinding<[string], AssistantSetupResult>("SetupInstallAssistant");
export const SetupCheckAssistant = appBinding<[string, boolean], AssistantSetupResult>("SetupCheckAssistant");
export const OpenAssistantInstallTerminal = appBinding<[string], void>("OpenAssistantInstallTerminal");
export const OpenNodeDownload = appBinding<[], void>("OpenNodeDownload");

export interface ModelAccountsStatus {
    mode: ModelAccountsMode;
    sidecar: ModelSidecarView;
    accounts: ModelAccountView[];
    groups: ModelGroupView[];
    consent: ModelConsentView;
    // The assistant order the groups admit, primary first ("claude", "codex").
    accountOrder: string[];
    install: MagpieInstallView;
    ollama: boolean;
    byokRegistered: boolean;
    // Set when bootstrap or the quota poll failed; empty when the last read succeeded.
    error: string;
    // How Today's searches reach the web: the signed-in Claude Code's web
    // search, the assistant with opencli in Chrome, or nothing yet.
    researchVia?: "claude_code" | "opencli" | "none";
}

export const GetModelAccountsStatus = appBinding<[], ModelAccountsStatus>(
    "GetModelAccountsStatus",
);
export const SetManagedAccountOrder = appBinding<[string[]], ModelAccountsStatus>(
    "SetManagedAccountOrder",
);
export const AcceptManagedModelConsent = appBinding<[number], ModelAccountsStatus>(
    "AcceptManagedModelConsent",
);
export const RevokeManagedModelConsent = appBinding<[], ModelAccountsStatus>(
    "RevokeManagedModelConsent",
);
export const RefreshModelAccounts = appBinding<[], ModelAccountsStatus>(
    "RefreshModelAccounts",
);
export const SetAIProviderMode = appBinding<["managed" | "advanced"], void>(
    "SetAIProviderMode",
);
export type ForkSessionResult = {
    projectId: string;
    session: SessionData;
};
/** Where the main chat lives; the session is kind "main". */
export type MainChatInfo = {
    projectId: string;
    workspaceId: string;
    session: SessionData;
};
export const GetMainChat = appBinding<[], MainChatInfo>("GetMainChat");
export const NativeClipboardSetText = appBinding<[string], void>("NativeClipboardSetText");
export const ResolveMainChatProactiveAction = appBinding<[string, string], string>("ResolveMainChatProactiveAction");
export const SendMainChatProactiveDraft = appBinding<[string, string], string>("SendMainChatProactiveDraft");
export const CorrectMainChatProactiveFact = appBinding<[string, string], string>("CorrectMainChatProactiveFact");
export const RecordMainChatProactiveReceipt = appBinding<[string, string], void>("RecordMainChatProactiveReceipt");
export const LoadMainChatProactiveItem = appBinding<[string], StreamItemData | null>("LoadMainChatProactiveItem");
export const ListOpenAnswers = appBinding<[], OpenAnswer[]>("ListOpenAnswers");
export const ChangeOpenAnswer = appBinding<[string, OpenAnswerDelivery], OpenAnswer | null>("ChangeOpenAnswer");
export const CancelOpenAnswer = appBinding<[string], OpenAnswer | null>("CancelOpenAnswer");
export const SelectOpenAnswerAsk = appBinding<[string], void>("SelectOpenAnswerAsk");
export const NotForMeAnswer = appBinding<[string], void>("NotForMeAnswer");
export const UndoNotForMeAnswer = appBinding<[string], void>("UndoNotForMeAnswer");
export const ReturnToMainChatPlace = appBinding<[string], boolean>("ReturnToMainChatPlace");
export const OpenExternalURL = appBinding<[string], void>("OpenExternalURL");
/** One ask in the main chat's setup sequence ("Isoform speaks first"). */
export type MainChatSetupStep = {
    key: string;
    kind: "name" | "assistant" | "permission" | "first_task";
    status: "pending" | "granted" | "not_now" | "done";
    /** The name step's prefill: the stored name, else the Mac account's full name. */
    suggestion?: string;
    /**
     * Pending, or shown by the main chat before. A step closed before the
     * chat reached it (already granted, or answered in onboarding) is not.
     */
    asked: boolean;
};
export type MainChatSetupState = {
    steps: MainChatSetupStep[];
    /** The step to show now; "" when nothing is left. */
    next: string;
};
/** lang is the renderer's language; the first message is posted in it. */
export const GetMainChatSetup = appBinding<[string], MainChatSetupState>("GetMainChatSetup");
/**
 * Closes the main chat's asks the onboarding window already made (the
 * permissions it showed, Notifications when set to Off, the first task), so
 * the chat does not make them again. Unknown keys and the assistant are ignored.
 */
export const CloseMainChatSetupSteps = appBinding<[string[]], void>("CloseMainChatSetupSteps");
/** Reopens asks onboarding closed and the user then changed (Notifications Off, then another level). */
export const ReopenMainChatSetupSteps = appBinding<[string[]], void>("ReopenMainChatSetupSteps");
export const AnswerMainChatSetup = appBinding<
    [string, "granted" | "not_now" | "done"],
    MainChatSetupState
>("AnswerMainChatSetup");
/** needed: Screen Recording was turned on after this launch, so the app sees the screen only after it reopens; app: the product name. */
export type ScreenRecordingRelaunch = { needed: boolean; app: string };
export const GetScreenRecordingRelaunch = appBinding<[], ScreenRecordingRelaunch>("GetScreenRecordingRelaunch");
/** One draggable area of the title strip, in page points from the window's top-left corner. */
export type TitleStripRect = { x: number; y: number; width: number; height: number };
/** Tells macOS where the title strip drags the window, so the first click on an inactive window drags there. */
export const SetTitleStripDragRegions = appBinding<[TitleStripRect[]], void>("SetTitleStripDragRegions");
/** Quits Isoform and opens this same app bundle again once it has exited. */
export const RelaunchApp = appBinding<[], void>("RelaunchApp");
/** Posts a one-line summary of a side chat into the main chat; returns the line. */
export const ForkSession = appBinding<
    [string, string],
    ForkSessionResult
>("ForkSession");
// Today's news — cached daily brief + async web-search generation.
export const GetTodayNews = appBinding<[], TodayNewsData>("GetTodayNews");
export const GenerateTodayNews = appBinding<[], void>("GenerateTodayNews");
// Past days' briefs from the Daily News automation's run history.
export const GetTodayNewsHistory = appBinding<
    [string, number],
    TodayNewsData[]
>("GetTodayNewsHistory");

export interface MemoryContextDebugCounts {
    discovered: number;
    correctedExcluded: number;
    afterCorrection: number;
    belowRelevance: number;
    empty: number;
    relevant: number;
    selected: number;
    selectedMemory: number;
    selectedKnowledge: number;
    selectedOther: number;
    selectedChars: number;
    truncated: number;
    budgetOmitted: number;
    detailItemsOmitted: number;
}

export interface MemoryContextDebugItem {
    path: string;
    source: string;
    startLine: number;
    endLine: number;
    score: number;
    preview: string;
}

export interface MemoryContextDebugSnapshot {
    version: string;
    query: string;
    minScore: number;
    characterBudget: number;
    detailPayloadBudget: number;
    counts: MemoryContextDebugCounts;
    selected: MemoryContextDebugItem[];
    countsReconcile: boolean;
    readOnly: boolean;
    generatedAt: number;
    durationMs: number;
}

export const CancelMemoryDream = appBinding<[], void>("CancelMemoryDream");

export const GetMemoryContextDebugSnapshot = appBinding<
    [string],
    MemoryContextDebugSnapshot
>("GetMemoryContextDebugSnapshot");

export interface ShadowEvidenceReference {
    role: string;
    sourceKind: string;
    sourceId: string;
    content?: string;
    contentHash: string;
    summary?: string;
    observedAt?: number;
}

export interface ShadowReasoningHypothesis {
    statement: string;
    confidence: number;
    basis?: string;
    sourceQuote?: string;
    evidenceRefs?: string[];
    profileRefs?: string[];
    contradictionRefs?: string[];
}

export interface ShadowReasoningEvidence {
    kind: string;
    statement: string;
    confidence: number;
    sourceRefs?: string[];
}

export interface ShadowCandidateExecutionPlan {
    kind: string;
    name: string;
    description?: string;
    prompt: string;
    scheduleKind: string;
    scheduleConfig: unknown;
    scheduleEvidence?: string;
    rationale?: string;
    confidence: number;
}

export interface ShadowPrediction {
    goal: string;
    predictedAction: string;
    expectedResult: string;
    expectedTiming?: string;
    evidenceRationale?: string;
    reasoningHypotheses: ShadowReasoningHypothesis[];
    candidateExecutionPlan?: ShadowCandidateExecutionPlan;
    requiredCapabilities?: string[];
    plannedEffects?: Array<{
        capability: string;
        effect: string;
        resource?: string;
        recipient?: string;
    }>;
    goalConfidence: number;
    actionConfidence: number;
    resultConfidence: number;
    timingConfidence: number;
}

export interface ShadowRun {
    id: string;
    decisionKey: string;
    capabilityKey: string;
    sourceKind: string;
    sourceId: string;
    contextHash: string;
    context: {
        capturedAt: number;
        productionEventAt?: number;
        promptContext: string;
        selectionBudgetChars?: number;
        references: ShadowEvidenceReference[];
    };
    predictionHash: string;
    prediction: ShadowPrediction;
    model: string;
    promptVersion: string;
    runnerVersion: string;
    observationHorizonStart: number;
    observationHorizonEnd: number;
    createdAt: number;
}

export interface ShadowRunObservation {
    id: string;
    shadowRunId: string;
    sourceKind: string;
    sourceId: string;
    observedAction: string;
    observedResult: string;
    reasoningEvidence: ShadowReasoningEvidence[];
    evidence: ShadowEvidenceReference[];
    observedAt: number;
    createdAt: number;
}

export interface ShadowEvaluationDimension {
    assessment: string;
    score?: number;
    confidence: number;
    reason?: string;
    evidenceRefs?: string[];
}

export interface ShadowRunEvaluation {
    id: string;
    shadowRunId: string;
    observationId: string;
    idempotencyKey: string;
    comparisonVersion: string;
    evaluatorModel: string;
    comparatorPrompt?: string;
    comparatorHash?: string;
    evaluatorRaw?: string;
    action: ShadowEvaluationDimension;
    outcome: ShadowEvaluationDimension;
    timing: ShadowEvaluationDimension;
    reasoning: ShadowEvaluationDimension;
    safety: ShadowEvaluationDimension;
    summary?: string;
    createdAt: number;
}

export interface ShadowRunDebugSummary {
    id: string;
    capabilityKey: string;
    status: string;
    sourceKind: string;
    sourceId: string;
    predictedAction: string;
    expectedResult: string;
    actionConfidence: number;
    observationCount: number;
    evaluationCount: number;
    lastObservationAt?: number;
    observationHorizonEnd: number;
    createdAt: number;
}

export interface ShadowCorrelationCandidateAudit {
    shadowRunId: string;
    capabilityKey: string;
    relevance: number;
    payloadBytes: number;
    selected: boolean;
    reason?: string;
}

export interface ShadowCorrelationAttempt {
    id: string;
    sourceKind: string;
    sourceId: string;
    observedAt: number;
    observedSummary?: string;
    status: string;
    reason?: string;
    candidateCount: number;
    selectedCount: number;
    reportedMatches: number;
    persistedRunIds?: string[];
    candidates?: ShadowCorrelationCandidateAudit[];
    comparatorPrompt?: string;
    comparatorHash?: string;
    comparatorVersion: string;
    evaluatorModel?: string;
    evaluatorRaw?: string;
    error?: string;
    createdAt: number;
}

export interface ShadowCapabilityDimensionProfile {
    totalCount: number;
    knownCount: number;
    qualifiedCount: number;
    unknownCount: number;
    lowConfidenceCount: number;
    missingScoreCount: number;
    assessmentCounts: Record<string, number>;
    qualifiedAssessmentCounts: Record<string, number>;
    reliability?: number;
    averageConfidence?: number;
}

export interface ShadowCapabilityProfileEvidence {
    evaluationId: string;
    shadowRunId: string;
    observationId: string;
    predictionSourceKind: string;
    predictionSourceId: string;
    observationSourceKind: string;
    observationSourceId: string;
    contextHash: string;
    actionAssessment: string;
    outcomeAssessment: string;
    timingAssessment: string;
    reasoningAssessment: string;
    safetyAssessment: string;
    qualified: boolean;
    exclusionReason?: string;
    evaluationCreatedAt: number;
}

export interface ShadowCapabilityReadiness {
    status: string;
    ready: boolean;
    reasons: string[];
    minQualifiedEvaluations: number;
    minPredictionSources: number;
    minObservationSources: number;
    minContexts: number;
    minQualifiedSafetyAssessments: number;
    actionReliabilityThreshold: number;
    dimensionConfidenceThreshold: number;
}

export interface ShadowCapabilityProfile {
    id: string;
    capabilityKey: string;
    policyVersion: string;
    evidenceHash: string;
    evaluationCount: number;
    qualifiedEvaluationCount: number;
    excludedEvaluationCount: number;
    runCount: number;
    predictionSourceCount: number;
    observationSourceCount: number;
    sourcePairCount: number;
    sourceKindCount: number;
    contextCount: number;
    action: ShadowCapabilityDimensionProfile;
    outcome: ShadowCapabilityDimensionProfile;
    timing: ShadowCapabilityDimensionProfile;
    reasoning: ShadowCapabilityDimensionProfile;
    safety: ShadowCapabilityDimensionProfile;
    readiness: ShadowCapabilityReadiness;
    evidence: ShadowCapabilityProfileEvidence[];
    createdAt: number;
}

export interface ShadowCapabilityQualification {
    id: string;
    qualificationKey: string;
    policyVersion: string;
    profileId: string;
    profileEvidenceHash: string;
    capabilityKey: string;
    representativeShadowRunId?: string;
    candidatePlanHash?: string;
    runtimeEvidenceHash: string;
    runtimeAttemptIds: string[];
    riskTier: string;
    status: string;
    reasons: string[];
    thresholds: {
        minQualifiedEvaluations: number;
        minPredictionSources: number;
        minObservationSources: number;
        minContexts: number;
        minQualifiedSafetyAssessments: number;
        minActionReliability: number;
        minCompletedRuntimeAttempts: number;
    };
    actual: {
        qualifiedEvaluations: number;
        predictionSources: number;
        observationSources: number;
        contexts: number;
        qualifiedSafetyAssessments: number;
        unsafeAssessments: number;
        actionReliability: number;
        completedRuntimeAttempts: number;
    };
    effects: Array<{
        capability: string;
        effect: AuthorityAction["effect"];
        resource?: string;
        recipient?: string;
        reversible: boolean;
    }>;
    proposalInitiativeId?: string;
    createdAt: number;
}

export interface SelfModelEntry {
    id: string;
    entryKey: string;
    revision: number;
    kind: "principle" | "preference" | "decision_policy" | "boundary";
    scope?: string;
    statement: string;
    status: "provisional" | "active" | "retracted";
    confidence: number;
    reasoningStatus: string;
    evidence: Array<{
        sourceKind: string;
        sourceId: string;
        statement?: string;
        confidence: number;
        sourceRefs?: string[];
        occurredAt: number;
    }>;
    contradictions: Array<{
        sourceKind: string;
        sourceId: string;
        statement?: string;
        confidence: number;
        sourceRefs?: string[];
        occurredAt: number;
    }>;
    sourceHash: string;
    createdAt: number;
}

export interface ShadowAgentDebugSnapshot {
    generatedAt: number;
    runs: ShadowRunDebugSummary[];
    profiles: ShadowCapabilityProfile[];
    qualifications: ShadowCapabilityQualification[];
    selfModel: SelfModelEntry[];
    attempts: ShadowCorrelationAttempt[];
}

export interface ShadowRunDebugDetail {
    detail: {
        run: ShadowRun;
        status: string;
        evidence: Array<ShadowEvidenceReference & {
            id: string;
            shadowRunId: string;
            createdAt: number;
        }>;
        observations: ShadowRunObservation[];
        evaluations: ShadowRunEvaluation[];
    };
    executionAttempts: ShadowExecutionAttempt[];
    memoryProjection: {
        path?: string;
        exists: boolean;
        content?: string;
        error?: string;
    };
}

export interface ShadowExecutionAttempt {
    id: string;
    shadowRunId: string;
    idempotencyKey: string;
    runtimeVersion: string;
    status: "completed" | "error" | "unsupported_runner";
    policyVersion: string;
    agentRunId?: string;
    model?: string;
    planHash: string;
    prompt: string;
    plan: ShadowCandidateExecutionPlan;
    decisions: AuthorityEvaluation[];
    assistantOutput?: string;
    error?: string;
    startedAt: number;
    completedAt: number;
    createdAt: number;
}

export const GetShadowAgentDebugSnapshot = appBinding<
    [number],
    ShadowAgentDebugSnapshot
>("GetShadowAgentDebugSnapshot");

export const GetShadowAgentDebugRun = appBinding<
    [string],
    ShadowRunDebugDetail
>("GetShadowAgentDebugRun");

export interface TaskContextLearningProvenance {
    version: string;
    runId: string;
    revisionId: string;
    materializationReceiptId: string;
    targetKind: string;
    targetId: string;
    projectionKey: string;
    capabilityKey: string;
    learningKind: string;
    evidenceHash: string;
    availableAt: number;
    authorityEffect: "none";
}

export interface PurposeDebugCounts {
    openLoops: number;
    eligibleOpenLoops: number;
    events: number;
    evidence: number;
    relations: number;
    relationEvents: number;
    extractionRuns: number;
    extractionHypotheses: number;
    reconciliationReceipts: number;
}

export interface PurposeDebugTruncation {
    extractionRuns: boolean;
}

export interface PurposeDebugEligibility {
    eligible: boolean;
    decision: "included" | "excluded";
    reasons: string[];
    currentEvidenceCount: number;
    independentKeys: string[];
    hasExplicitSupport: boolean;
    duplicateOfIds: string[];
    projectionReference: string;
    projectionHash: string;
}

export interface PurposeDebugOpenLoopSummary {
    purpose: AgencyPurpose;
    eligibility: PurposeDebugEligibility;
    contractVersion: string;
    rootPayloadHash: string;
    eventCount: number;
    evidenceCount: number;
    relationCount: number;
}

export interface PurposeDebugExtractionRunSummary {
    id: string;
    status: string;
    model: string;
    cutoffAt: number;
    scannedCount: number;
    includedCount: number;
    excludedCount: number;
    hypothesisCount: number;
    receiptCount: number;
    error?: string;
    inputHash: string;
    manifestHash: string;
    promptHash: string;
    outputHash?: string;
    rawOutputHash?: string;
    payloadHash: string;
    startedAt: number;
    completedAt: number;
    createdAt: number;
}

export interface PurposeUnderstandingCycleStatus {
    state: string;
    triggerKinds: string[];
    model?: string;
    fingerprint?: string;
    runId?: string;
    cached: boolean;
    scannedCount: number;
    includedCount: number;
    excludedCount: number;
    currentPurposeCount: number;
    changedRootIds: string[];
    changedRelationIds: string[];
    recoveredRunIds: string[];
    startedAt?: number;
    completedAt?: number;
    durationMilliseconds?: number;
    error?: string;
}

export interface PurposeUnderstandingRuntimeSnapshot {
    started: boolean;
    running: boolean;
    pendingTriggers: string[];
    current?: PurposeUnderstandingCycleStatus;
    last?: PurposeUnderstandingCycleStatus;
}

export interface PurposeDebugSnapshot {
    version: string;
    generatedAt: number;
    integrityStatus: string;
    understanding: PurposeUnderstandingRuntimeSnapshot;
    counts: PurposeDebugCounts;
    truncated: PurposeDebugTruncation;
    openLoops: PurposeDebugOpenLoopSummary[];
    extractionRuns: PurposeDebugExtractionRunSummary[];
}

export interface PurposeDebugExtractionSource {
    ref: string;
    sourceKind: string;
    sourceId: string;
    sourceVersion: string;
    sourceObservedAt: number;
    sourceHash: string;
    promptContentHash: string;
    evidenceClass: string;
    privacyDecision: string;
    independenceKey: string;
    truncated: boolean;
}

export interface PurposeDebugManifestEntry {
    ref: string;
    sourceKind: string;
    sourceId: string;
    sourceObservedAt?: number;
    evidenceClass?: string;
    privacyDecision?: string;
    decision: string;
    reason?: string;
    sourceHash?: string;
    promptContentHash?: string;
    includedChars?: number;
    truncated?: boolean;
}

export interface PurposeDebugCurrentPurpose {
    ref: string;
    openLoopId: string;
    currentEventId: string;
    projectionHash: string;
    canonicalKey: string;
    kind: string;
    title: string;
    desiredOutcome: string;
    owner: string;
    counterparty?: string;
    scope?: string;
    dueKind: string;
    dueValue?: string;
    waitingCondition?: string;
    reasoningStatus: string;
    lifecycleState: string;
    supportState: string;
    eligible: boolean;
    duplicateOfIds: string[];
}

export interface PurposeDebugManifestSummary {
    version: string;
    cutoffAt: number;
    budgetChars: number;
    usedChars: number;
    scannedCount: number;
    includedCount: number;
    excludedCount: number;
    entries: PurposeDebugManifestEntry[];
    currentPurposeBudgetChars?: number;
    currentPurposeUsedChars?: number;
    currentPurposeCount?: number;
    currentPurposeHash?: string;
}

export interface PurposeDebugExtractionHypothesis {
    id: string;
    runId: string;
    localKey: string;
    canonicalKey: string;
    duplicateOfCanonicalKey?: string;
    kind: string;
    operation: string;
    title: string;
    desiredOutcome: string;
    owner: string;
    counterparty?: string;
    scope?: string;
    dueKind: string;
    dueValue?: string;
    waitingCondition?: string;
    reasoningStatus: string;
    rationale?: string;
    confidence: number;
    evidenceRefs: string[];
    payloadJson: string;
    payloadHash: string;
    createdAt: number;
}

export interface PurposeDebugReconciliationReceipt {
    id: string;
    idempotencyKey: string;
    runId: string;
    hypothesisId: string;
    disposition: string;
    openLoopId?: string;
    eventId?: string;
    relationId?: string;
    reason?: string;
    sourceSetHash: string;
    occurredAt: number;
    createdAt: number;
    payloadJson: string;
    payloadHash: string;
}

export interface PurposeDebugExtractionRunDetail {
    summary: PurposeDebugExtractionRunSummary;
    query?: string;
    sources: PurposeDebugExtractionSource[];
    currentPurpose?: PurposeDebugCurrentPurpose[];
    manifest: PurposeDebugManifestEntry[];
    manifestSummary?: PurposeDebugManifestSummary;
    hypotheses: PurposeDebugExtractionHypothesis[];
    receipts: PurposeDebugReconciliationReceipt[];
}

export interface PurposeDebugOpenLoopDetail {
    summary: PurposeDebugOpenLoopSummary;
    product: AgencyPurposeDetail;
    rootPayloadJson: string;
}

export const GetPurposeDebugSnapshot = appBinding<
    [number],
    PurposeDebugSnapshot
>("GetPurposeDebugSnapshot");

export const GetPurposeDebugOpenLoop = appBinding<
    [string],
    PurposeDebugOpenLoopDetail
>("GetPurposeDebugOpenLoop");

export const GetPurposeDebugExtractionRun = appBinding<
    [string],
    PurposeDebugExtractionRunDetail
>("GetPurposeDebugExtractionRun");

export interface TaskShadowDebugEpisodeSummary {
    id: string;
    status: string;
    taskHypothesis: string;
    desiredResultHypothesis: string;
    taskConfidence: number;
    boundaryConfidence: number;
    desiredResultConfidence: number;
    inferredStartAt: number;
    detectedAt: number;
    contextSnapshotId: string;
    contextHash: string;
    contextIncludedCount: number;
    contextExcludedCount: number;
    linkedShadowRunIds: string[];
    linkedShadowRunCount: number;
    taskAgentAttemptCount: number;
    persistedEvaluationCount: number;
    latestPairWorkflow?: string;
    latestPairState?: string;
    latestPairReasons?: string[];
    createdAt: number;
}

export interface TaskShadowReasoningCurrentVersions {
    onsetPrompt: string;
    lifecyclePrompt: string;
    shadowPrompt: string;
    resultDestination?: string;
    evaluationInput: string;
    evaluationOutput: string;
    comparator: string;
    evaluator: string;
}

export interface TaskShadowReasoningVersionCount {
    version: string;
    count: number;
    current: boolean;
}

export interface TaskShadowReasoningHypothesisDebug {
    statement: string;
    confidence: number;
    basis: string;
    sourceQuote?: string;
    evidenceRefs: string[];
    profileRefs: string[];
    contradictionRefs: string[];
    quoteValidation: string;
    provenanceResolved: boolean;
    validationReason?: string;
}

export interface TaskShadowReasoningPredictionDebug {
    shadowRunId: string;
    promptVersion: string;
    current: boolean;
    hypotheses: TaskShadowReasoningHypothesisDebug[];
}

export interface TaskShadowObservedReasoningDebug {
    evidenceId: string;
    kind: string;
    version?: string;
    sourceKind: string;
    sourceId: string;
    sourceQuote?: string;
    evidenceRefs: string[];
    observedAt: number;
    valid: boolean;
    validationReason: string;
}

export interface TaskShadowReasoningComparisonDebug {
    evaluationId: string;
    shadowRunId: string;
    comparatorVersion: string;
    inputVersion?: string;
    outputVersion?: string;
    evaluatorVersion?: string;
    evaluatorModel: string;
    current: boolean;
    predictionBound: boolean;
    predictionBindingReason: string;
    predictedHypotheses: TaskShadowReasoningHypothesisDebug[];
    supportedEvidenceRefs: string[];
    assessment: string;
    score: number | null;
    confidence: number;
    reason: string;
    evidenceRefs: string[];
    citationsValid: boolean;
    unknown: boolean;
}

export interface TaskShadowReasoningDebug {
    version: string;
    versions: {
        current: TaskShadowReasoningCurrentVersions;
        onsetPrompts: TaskShadowReasoningVersionCount[];
        lifecyclePrompts: TaskShadowReasoningVersionCount[];
        shadowPrompts: TaskShadowReasoningVersionCount[];
        evaluationInputs: TaskShadowReasoningVersionCount[];
        evaluationOutputs: TaskShadowReasoningVersionCount[];
        comparators: TaskShadowReasoningVersionCount[];
        evaluators?: TaskShadowReasoningVersionCount[];
    };
    onsetHypotheses: TaskShadowReasoningHypothesisDebug[];
    predictions: TaskShadowReasoningPredictionDebug[];
    observed: TaskShadowObservedReasoningDebug[];
    comparisons: TaskShadowReasoningComparisonDebug[];
    counts: {
        onsetHypotheses: number;
        shadowHypotheses: number;
        observedReasoning: number;
        invalidObservedReasoning: number;
        comparisons: number;
        knownComparisons: number;
        unknownComparisons: number;
    };
    coverage: string;
}

export interface ShadowTaskPairReadiness {
    workflow: string;
    state: string;
    ready: boolean;
    shadowPromptVersion?: string;
    currentGeneration?: boolean;
    taskEpisodeStatus: string;
    shadowAttemptStatus?: string;
    userResultMaterialized: boolean;
    userResultSha256?: string;
    userResultDestinationSha256?: string;
    userResultTruncated: boolean;
    userResultEvidenceIds: string[];
    invalidUserResultEvidenceIds?: string[];
    shadowResultMaterialized: boolean;
    shadowResultSha256?: string;
    shadowArtifactDigests: string[];
    evaluationIds: string[];
    currentEvaluationIds?: string[];
    historicalEvaluationIds?: string[];
    reasons: string[];
}

export interface TaskShadowContinuationItem {
    taskEpisodeId: string;
    shadowRunId: string;
    workflow: string;
    state: string;
    action: string;
    reason?: string;
    episodeStatus?: string;
    attemptId?: string;
    attemptStatus?: string;
    pairState?: string;
    currentEvaluationIds: string[];
    learningRunId?: string;
    learningMaterialization?: ShadowTaskLearningMaterializationResolution;
    createdAt: number;
}

export interface ShadowTaskLearningMaterializationResolution {
    version: string;
    runId: string;
    complete: boolean;
    candidateCount: number;
    terminalCount: number;
    materializedCount: number;
    skippedCount: number;
    deniedCount: number;
    errorCount: number;
    invalidCount: number;
    missingCount: number;
    pendingRevisionIds: string[];
    errorRevisionIds: string[];
    terminalRevisionIds: string[];
}

export interface TaskShadowContinuationSnapshot {
    version: string;
    generatedAt: number;
    scannedRunCount: number;
    currentRunCount: number;
    executionPendingCount: number;
    evaluationPendingCount: number;
    learningPendingCount: number;
    materializationPendingCount: number;
    awaitingTerminalCount: number;
    awaitingResultCount: number;
    completeCount: number;
    blockedCount: number;
    historicalCount: number;
    firstRunExcludedCount: number;
    items: TaskShadowContinuationItem[];
}

export interface ShadowTaskLearningQualification {
    decision: "qualified" | "collecting" | "rejected";
    reasons: string[];
    observedEvaluationCount: number;
    qualifiedEvaluationCount: number;
    observedTaskEpisodeCount: number;
    observedContextCount: number;
    requiredEvaluationCount: number;
    requiredTaskEpisodeCount: number;
    requiredContextCount: number;
    requiredSafetyCount?: number;
    averageTaskReliability?: number;
    averageResultQuality?: number;
    averageEvidenceConfidence?: number;
}

export interface TaskShadowCapabilityReleaseEvidence {
    evaluationIds: string[];
    taskEpisodeIds: string[];
    shadowRunIds: string[];
    contextHashes: string[];
}

export interface TaskShadowCapabilityReleaseEvent {
    id: string;
    eventKey: string;
    version: string;
    capabilityKey: string;
    promptVersion: string;
    campaignVersion: string;
    learningPolicyVersion: string;
    decision: "qualified" | "suspended";
    evidenceHash: string;
    previousEventId?: string;
    qualification: ShadowTaskLearningQualification;
    evidence: TaskShadowCapabilityReleaseEvidence;
    authorityEffect: "none";
    createdAt: number;
}

export interface TaskShadowCapabilityReleaseSnapshot {
    version: string;
    capabilityKey: string;
    promptVersion: string;
    state: string;
    active: boolean;
    pendingDecision?: string;
    authorityEffect: "none";
    currentPromptEventCount: number;
    historicalEventCount: number;
    latestCurrentPromptEvent?: TaskShadowCapabilityReleaseEvent;
    events: TaskShadowCapabilityReleaseEvent[];
}

export interface TaskShadowQualityCampaign {
    version: string;
    capabilityKey: string;
    promptVersion: string;
    evidenceHash?: string;
    stage: string;
    complete: boolean;
    existingEvidenceExhausted: boolean;
    requiredAction: string;
    counts: {
        detectedEpisodes: number;
        eligibleEpisodes: number;
        ineligibleEpisodes: number;
        shadowRuns: number;
        currentPromptRuns: number;
        historicalRuns: number;
        completedShadowAttempts: number;
        independentResults: number;
        observedResults: number;
        awaitingNaturalOutcomes: number;
        pairReady: number;
        evaluated: number;
        currentPromptEvaluations: number;
        historicalEvaluations: number;
        safeEvaluations: number;
        safetyConcerns: number;
        shadowFailures: number;
        terminalWithoutShadowRun: number;
        processablePairs: number;
    };
    qualification: ShadowTaskLearningQualification;
    pendingExecutionRunIds: string[];
    readyPairRunIds: string[];
    blockers: Array<{
        taskEpisodeId: string;
        shadowRunId?: string;
        state: string;
        reasons: string[];
        occurredAt: number;
    }>;
    release?: TaskShadowCapabilityReleaseSnapshot;
    generatedAt: number;
}

export interface TaskShadowQualityCampaignRunResult {
    version: string;
    capabilityKey: string;
    inspectedRunIds: string[];
    executedRunIds: string[];
    evaluatedRunIds: string[];
    existingEvaluationIds: string[];
    deferredRunIds: string[];
    errors: string[];
    releaseEventId?: string;
    releaseEventAdded: boolean;
    startedAt: number;
    completedAt: number;
    campaign: TaskShadowQualityCampaign;
}

export interface TaskShadowDebugSnapshot {
    generatedAt: number;
    limit: number;
    enabledWorkflows?: string[];
    reasoningVersions?: TaskShadowReasoningCurrentVersions;
    lifecycleHealth?: {
        policyVersion: string;
        promptVersion: string;
        shadowPromptVersion: string;
        candidateHorizonMs: number;
        activeHorizonMs: number;
        pausedHorizonMs: number;
        evidenceRenews: boolean;
        activitySourcePolicy: string;
        episodeCount: number;
        openCount: number;
        overdueOpenCount: number;
        materializedResultEpisodeCount: number;
        statusCounts: Record<string, number>;
        pairStateCounts: Record<string, number>;
        duplicateOnsetClusterCount: number;
        duplicateOnsetEpisodeCount: number;
        duplicateOnsetClusters: Array<{
            sourceKind: string;
            sourceId: string;
            evidenceRef: string;
            observedAt: number;
            inferredStartAt: number;
            episodeIds: string[];
        }>;
        duplicateOnsetClustersTruncated: boolean;
        latestAttempt?: {
            id: string;
            status: string;
            model: string;
            promptVersion: string;
            startedAt: number;
            completedAt: number;
        };
        generatedAt: number;
    };
    releaseGate?: {
        version: string;
        stage: string;
        complete: boolean;
        canObserveNaturalTask: boolean;
        monitoring: boolean;
        foregroundApp?: string;
        foregroundBundleId?: string;
        latestTaskEpisodeId?: string;
        latestShadowRunId?: string;
        latestPairState?: string;
        selectedWorkflow?: string;
        requiredShadowPrompt?: string;
        selectedShadowPrompt?: string;
        currentGeneration?: boolean;
        historicalShadowRunIds?: string[];
        observerProvenance?: {
            version: string;
            state: string;
            reason: string;
            sourceComplete: boolean;
            originComplete: boolean;
            pipelineRunId?: number;
            traceId?: string;
            traceContractVersion?: string;
            coverageState?: string;
            admission?: string;
            flow?: string;
            terminalStatus?: string;
            productionOnsetAttemptId?: string;
            productionOnsetPrompt?: string;
            productionOnsetStatus?: string;
            taskEpisodeId?: string;
            workflow?: string;
            workflowDecisionState?: string;
            frozenContextHash?: string;
            shadowRunId?: string;
            claimTraceCount: number;
            candidateTraceCount: number;
            candidateDecisionCount: number;
            integrityValid: boolean;
        };
        learningResolution?: ShadowTaskLearningMaterializationResolution;
        requiredAction: string;
        reasons: string[];
        steps: Array<{
            key: string;
            label: string;
            complete: boolean;
            evidenceId?: string;
        }>;
    };
    continuation?: TaskShadowContinuationSnapshot;
    qualityCampaign?: TaskShadowQualityCampaign;
    episodes: TaskShadowDebugEpisodeSummary[];
}

export interface TaskContextManifestEntry {
    ref: string;
    section: string;
    sourceKind: string;
    sourceId: string;
    observedAt: number;
    evidenceClass: string;
    privacyDecision: string;
    included: boolean;
    reason?: string;
    contentHash?: string;
    includedChars?: number;
}

export interface TaskAgentAttempt {
    id: string;
    phase: string;
    taskEpisodeId?: string;
    shadowRunId?: string;
    shadowEvaluationId?: string;
    modelName: string;
    modelVersion: string;
    promptVersion: string;
    promptText: string;
    promptHash: string;
    normalizedInputJson: string;
    inputHash: string;
    status: string;
    rawOutput?: string;
    parsedOutputJson?: string;
    error?: string;
    startedAt: number;
    completedAt?: number;
    createdAt: number;
}

export interface TaskShadowDebugResultDestination {
    status: string;
    validationReason: string;
    current: boolean;
    valid: boolean;
    workflow?: string;
    resultSha256?: string;
    destinationSha256?: string;
    version?: string;
    appBundleId?: string;
    appName?: string;
    windowTitle?: string;
    semanticRole?: string;
    captureMethod?: string;
    evidenceIds: string[];
    sourceEvidenceRefs: string[];
    latestObservedAt: number;
}

export interface TaskShadowDebugDetail {
    generatedAt: number;
    limit: number;
    selectedShadowRunId?: string;
    productionRollout: {
        workflow: string;
        enabledWorkflows: string[];
        state: string;
        eligible: boolean;
        reason: string;
    };
    episode: {
        episode: {
            id: string;
            inferredStartAt: number;
            detectedAt: number;
            currentStatus: string;
            observationLease: {
                policyVersion: string;
                status: string;
                open: boolean;
                anchorKind: string;
                anchorTransitionId?: string;
                anchorAt: number;
                horizonMs: number;
                deadlineAt: number;
                due: boolean;
                evidenceRenews: boolean;
            };
            createPayloadHash: string;
            frozenContext: {
                id: string;
                taskEpisodeId: string;
                cutoffAt: number;
                sha256: string;
                manifest: TaskContextManifestEntry[];
                manifestEntryCount: number;
                manifestIncludedCount: number;
                manifestExcludedCount: number;
                manifestTruncated: boolean;
                canonicalJsonRedacted: boolean;
                createdAt: number;
            };
            latestHypothesis: {
                id?: string;
                taskEpisodeId?: string;
                taskHypothesis: string;
                desiredResultHypothesis: string;
                taskConfidence: number;
                boundaryConfidence: number;
                desiredResultConfidence: number;
                revision: number;
                profileInfluenceRefs?: string[];
                profileConflict?: boolean;
                reasoningHypotheses?: Array<{
                    statement: string;
                    confidence: number;
                    basis: string;
                    sourceQuote?: string;
                    evidenceIds?: string[];
                    profileRefs?: string[];
                    contradictionEvidenceIds?: string[];
                }>;
            };
            createdAt: number;
        };
        hypotheses: unknown[];
        evidence: unknown[];
        resultDestinations?: TaskShadowDebugResultDestination[];
        transitions: unknown[];
        links: unknown[];
    };
    runs: Array<{
        run: ShadowRun & {
            mode?: string;
            taskEpisodeId?: string;
            taskContextHash?: string;
        };
        status: string;
        pairReadiness?: ShadowTaskPairReadiness;
        evidence: unknown[];
        observations: unknown[];
        executionAttempts: unknown[];
        evaluations: unknown[];
        contextPromptRedacted: boolean;
        executionPlanRedacted: boolean;
    }>;
    reasoning?: TaskShadowReasoningDebug;
    taskAgentAttempts: TaskAgentAttempt[];
    learning: {
        runs: unknown[];
        revisions: unknown[];
        materializations: unknown[];
    };
    correlations: Record<string, unknown>;
    exclusions: Array<{
        stage: string;
        correlationId: string;
        sourceKind?: string;
        sourceId?: string;
        reason: string;
    }>;
}

export const GetTaskShadowDebugSnapshot = appBinding<
    [number],
    TaskShadowDebugSnapshot
>("GetTaskShadowDebugSnapshot");

export const GetTaskShadowDebugDetail = appBinding<
    [string, string, number],
    TaskShadowDebugDetail
>("GetTaskShadowDebugDetail");

export interface TaskShadowWorkflowReadinessDecision {
    source: string;
    taskEpisodeId: string;
    workflow: string;
    state: string;
    reason?: string;
    frozenContextHash?: string;
    frozenManifestCount: number;
    frozenIncludedCount: number;
    hasExactActivitySnapshot: boolean;
    shadowRunId?: string;
    executionDispatched: boolean;
}

export interface TaskShadowObserverDuplicateReceipt {
    policyVersion: string;
    originalTraceId: string;
    originalRootHash: string;
    originalEventHash: string;
    originalStatus: string;
    inputIdentityHash: string;
}

export interface TaskShadowObserverTraceEventPayload {
    reason?: string;
    shadowEnabledAtExecution?: boolean;
    firstRunDecision?: string;
    firstRunReason?: string;
    onsetWindowCount: number;
    retainedOnsetEvidenceCount: number;
    productionOnsetAttemptIds: string[];
    diagnosticOnsetAttemptIds: string[];
    lifecycleAttemptIds: string[];
    taskEpisodeIds: string[];
    expiredTaskEpisodeIds: string[];
    shadowRunIds: string[];
    workflowDecisions: TaskShadowWorkflowReadinessDecision[];
    continuation: {
        version?: string;
        trigger?: string;
        scannedRunCount: number;
        executionDispatchedIds: string[];
        evaluationDispatchedIds: string[];
        materializationRecoveredRunIds?: string[];
        errorCount: number;
    };
    duplicate?: TaskShadowObserverDuplicateReceipt;
    diagnostics: string[];
    errors: string[];
}

export interface TaskShadowObserverTraceEvent {
    id: string;
    traceId: string;
    sequence: number;
    status: string;
    payload: TaskShadowObserverTraceEventPayload;
    payloadHash: string;
    previousHash: string;
    occurredAt: number;
    createdAt: number;
}

export interface TaskShadowObserverTrace {
    id: string;
    contractVersion: string;
    runtimeInstanceId: string;
    groupId: string;
    sessionIds: string[];
    sessionManifestHash: string;
    source: string;
    flow: string;
    admission: string;
    admissionReason?: string;
    windowStart: number;
    windowEnd: number;
    sessionCount: number;
    declaredEventCount: number;
    retainedEventCount: number;
    retainedEventManifestHash: string;
    inputIdentityHash?: string;
    shadowEnabledAtAdmission: boolean;
    sourcePipelineRunId: number;
    rootHash: string;
    createdAt: number;
    latestEvent: TaskShadowObserverTraceEvent;
}

export interface TaskShadowObserverDebugSummary {
    id: string;
    state: string;
    integrityValid: boolean;
    integrityReason?: string;
    groupId: string;
    flow: string;
    admission: string;
    admissionReason?: string;
    shadowEnabledAtAdmission: boolean;
    sourcePipelineRunId: number;
    windowStart: number;
    windowEnd: number;
    sessionCount: number;
    declaredEventCount: number;
    retainedEventCount: number;
    inputIdentityHash?: string;
    duplicateOfTraceId?: string;
    duplicatePolicyVersion?: string;
    firstRunDecision?: string;
    onsetWindowCount: number;
    retainedOnsetEvidenceCount: number;
    productionAttemptCount: number;
    diagnosticAttemptCount: number;
    taskEpisodeCount: number;
    shadowRunCount: number;
    workflowDecisionCount: number;
    terminalReason?: string;
    latestEventAt: number;
    createdAt: number;
}

export interface TaskShadowObserverDebugSnapshot {
    version: string;
    generatedAt: number;
    limit: number;
    counts: {
        total: number;
        running: number;
        interrupted: number;
        completed: number;
        completedWithErrors: number;
        skipped: number;
        cancelled: number;
        failed: number;
        corrupt: number;
    };
    traces: TaskShadowObserverDebugSummary[];
}

export interface TaskShadowObserverDebugAttemptLink {
    id: string;
    role: string;
    found: boolean;
    phase?: string;
    status?: string;
    model?: string;
    promptVersion?: string;
    promptHash?: string;
    inputHash?: string;
    startedAt?: number;
    completedAt?: number;
    missingReason?: string;
}

export interface TaskShadowObserverDebugDetail {
    version: string;
    generatedAt: number;
    summary: TaskShadowObserverDebugSummary;
    trace: TaskShadowObserverTrace;
    events: TaskShadowObserverTraceEvent[];
    attemptLinks: TaskShadowObserverDebugAttemptLink[];
}

export const GetTaskShadowObserverDebugSnapshot = appBinding<
    [number],
    TaskShadowObserverDebugSnapshot
>("GetTaskShadowObserverDebugSnapshot");

export const GetTaskShadowObserverDebugDetail = appBinding<
    [string],
    TaskShadowObserverDebugDetail
>("GetTaskShadowObserverDebugDetail");

export interface TaskShadowObserverCoverageCounts {
    instrumentedSources: number;
    legacySources: number;
    currentTraces: number;
    legacyTraces: number;
    covered: number;
    coveredRootOnly: number;
    pendingDispatch: number;
    dispatchFailed: number;
    traceMissing: number;
    linkMismatch: number;
    traceCorrupt: number;
    duplicateTrace: number;
    sourcePruned: number;
    unlinkedTrace: number;
    scanTruncated: number;
}

export interface TaskShadowObserverCoverageItem {
    id: string;
    kind: string;
    state: string;
    reason: string;
    pipelineRunId?: number;
    traceId?: string;
    groupId?: string;
    admission?: string;
    expectedFlow?: string;
    traceFlow?: string;
    dispatchStatus?: string;
    dispatchReason?: string;
    traceState?: string;
    integrityValid: boolean;
    integrityReason?: string;
    sourceStartedAt?: number;
    sourceCompletedAt?: number;
    traceCreatedAt?: number;
    observerRecordedAt?: number;
}

export interface TaskShadowObserverCoverageSnapshot {
    version: string;
    generatedAt: number;
    limit: number;
    health: string;
    healthReason: string;
    counts: TaskShadowObserverCoverageCounts;
    items: TaskShadowObserverCoverageItem[];
    loadedItems: number;
    scannedTraces: number;
}

export const GetTaskShadowObserverCoverageSnapshot = appBinding<
    [number],
    TaskShadowObserverCoverageSnapshot
>("GetTaskShadowObserverCoverageSnapshot");

export const RunTaskShadowQualityCampaign = appBinding<
    [],
    TaskShadowQualityCampaignRunResult
>("RunTaskShadowQualityCampaign");

export interface WorkPatternMetric {
    name: string;
    value: number;
    unit: string;
    sampleCount: number;
    confidence: number;
}

export interface WorkPatternProjection {
    scopeKind: string;
    scopeKey: string;
    label: string;
    evidenceKind: string;
    status: string;
    confidence: number;
    sampleCount: number;
    sessionCount: number;
    apps?: string[];
    metrics: WorkPatternMetric[];
}

export interface WorkPatternProfileSnapshot {
    id: string;
    policyVersion: string;
    sourceWindowStart: number;
    sourceCutoffAt: number;
    sourceMaxEventId: number;
    sourceHash: string;
    status: string;
    sampleCount: number;
    sessionCount: number;
    confidence: number;
    projections: WorkPatternProjection[];
    exclusions: Array<{
        sourceKind: string;
        sourceId: string;
        observedAt?: number;
        reason: string;
    }>;
    canonicalJson: string;
    sha256: string;
    createdAt: number;
}

export interface WorkPatternProfileRolloutStatus {
    influenceEnabled: boolean;
    ablationRunCount: number;
    evaluationCount: number;
    scoredPairCount: number;
    profileWins: number;
    neutralWins: number;
    ties: number;
    unknown: number;
    correctionCount: number;
    integrityErrorCount: number;
    averageImprovement: number;
    profileWinRate: number;
    unknownRate: number;
    correctionRate: number;
    minimumScoredPairs: number;
    minimumImprovement: number;
    minimumProfileWinRate: number;
    maximumUnknownRate: number;
    maximumCorrectionRate: number;
    meetsEvidenceGate: boolean;
    recommendedDecision: string;
    reason: string;
    policyVersion: string;
    collectionAlwaysEnabled: boolean;
}

export interface WorkPatternProfileDebugSummary {
    id: string;
    sha256: string;
    policyVersion: string;
    status: string;
    sourceWindowStart: number;
    sourceCutoffAt: number;
    sampleCount: number;
    sessionCount: number;
    confidence: number;
    projectionCount: number;
    evidenceCount: number;
    exclusionCount: number;
    correctionCount: number;
    consumingEpisodeCount: number;
    consumingRunCount: number;
    ablationRunCount: number;
    createdAt: number;
}

export interface ShadowLearningInfluenceProofStep {
    key: string;
    label: string;
    complete: boolean;
    reason?: string;
}

export interface ShadowLearningInfluenceProof {
    version: string;
    state: string;
    complete: boolean;
    improved: boolean;
    summary: string;
    requiredAction: string;
    authorityEffect: string;
    generatedAt: number;
    sourceLearningCount: number;
    laterTaskCount: number;
    measuredChainCount: number;
    learningRevisionId?: string;
    learningReceiptId?: string;
    learningTargetKind?: string;
    learningKind?: string;
    learningCapability?: string;
    learningAvailableAt?: number;
    sourceTaskEpisodeIds: string[];
    sourceShadowRunIds: string[];
    sourceEvaluationIds: string[];
    laterTaskEpisodeId?: string;
    laterTaskStatus?: string;
    laterTaskStartedAt?: number;
    contextSnapshotId?: string;
    contextSha256?: string;
    profileSnapshotId?: string;
    profileSha256?: string;
    onsetProfilePresent: boolean;
    onsetPromptIncluded?: boolean;
    onsetPromptReason?: string;
    onsetPromptSha256?: string;
    shadowContextIncluded?: boolean;
    shadowContextReason?: string;
    shadowContextSha256?: string;
    shadowRunId?: string;
    shadowExecutionAttemptId?: string;
    shadowEvaluationId?: string;
    ablationRunId?: string;
    ablationEvaluationId?: string;
    neutralScore?: number;
    profileScore?: number;
    lift?: number;
    winner?: string;
    steps: ShadowLearningInfluenceProofStep[];
    reasons: string[];
}

export interface ShadowLearningInfluenceCohortPolicy {
    version: string;
    minimumScoredTasks: number;
    minimumSourceTasks: number;
    minimumProfileWinRate: number;
    minimumMeanLift: number;
    maximumUnknownRate: number;
    maximumCorrectionRate: number;
}

export interface ShadowLearningInfluenceChain {
    id: string;
    learningRevisionId: string;
    laterTaskEpisodeId: string;
    laterTaskStatus: string;
    laterTaskStartedAt: number;
    state: string;
    profileSnapshotId?: string;
    onsetIncluded: boolean;
    shadowIncluded: boolean;
    shadowRunId?: string;
    shadowEvaluationId?: string;
    ablationRunId?: string;
    ablationEvaluationId?: string;
    neutralScore?: number;
    profileScore?: number;
    lift?: number;
    winner?: string;
    historicalPolicyExcluded: boolean;
    corrected: boolean;
    correctionIds: string[];
    integrityFailure: boolean;
    reasons: string[];
    authorityEffect: string;
}

export interface ShadowLearningInfluenceCohortSummary {
    id: string;
    manifestSha256: string;
    state: string;
    summary: string;
    learningRevisionId: string;
    learningReceiptId: string;
    learningTargetKind: string;
    learningKind: string;
    learningCapability: string;
    learningAvailableAt: number;
    sourceTaskEpisodeCount: number;
    sourceShadowRunCount: number;
    sourceEvaluationCount: number;
    sourceTaskEpisodeIds: string[];
    sourceShadowRunIds: string[];
    sourceEvaluationIds: string[];
    relevantLaterTaskCount: number;
    onsetOmittedCount: number;
    shadowOmittedCount: number;
    awaitingResultCount: number;
    awaitingAblationCount: number;
    historicalAblationCount: number;
    scoredChainCount: number;
    unknownChainCount: number;
    profileWins: number;
    neutralWins: number;
    ties: number;
    positiveLiftTotal: number;
    negativeLiftTotal: number;
    meanLift: number;
    profileWinRate: number;
    unknownRate: number;
    correctionCount: number;
    correctedChainCount: number;
    correctionRate: number;
    integrityExclusionCount: number;
    blockers: string[];
    chainCount: number;
    authorityEffect: string;
    productionInfluenceEffect: string;
    chains: ShadowLearningInfluenceChain[];
}

export interface ShadowLearningInfluenceCohortSnapshot {
    id: string;
    version: string;
    policyVersion: string;
    generatedAt: number;
    readCutoffAt: number;
    manifestSha256: string;
    authorityEffect: string;
    productionInfluenceEffect: string;
    policy: ShadowLearningInfluenceCohortPolicy;
    cohortCount: number;
    awaitingMeasurementCount: number;
    earlyCount: number;
    reliableCount: number;
    insufficientQualityCount: number;
    integrityErrorCount: number;
    cohorts: ShadowLearningInfluenceCohortSummary[];
}

export interface WorkPatternProfileDebugSnapshot {
    generatedAt: number;
    limit: number;
    profiles: WorkPatternProfileDebugSummary[];
    rollout: WorkPatternProfileRolloutStatus;
    learningInfluence: ShadowLearningInfluenceProof;
    learningCohorts: ShadowLearningInfluenceCohortSnapshot;
}

export interface WorkPatternProfileAblationRun {
    id: string;
    sourceKind: string;
    sourceId: string;
    windowStart: number;
    cutoffAt: number;
    profileSnapshotId: string;
    profileSha256: string;
    primaryMode: string;
    neutralAttemptId?: string;
    profileAttemptId?: string;
    neutralStatus: string;
    profileStatus: string;
    neutralResponse: unknown;
    profileResponse: unknown;
    model: string;
    promptVersion: string;
    startedAt: number;
    completedAt: number;
    createdAt: number;
}

export interface WorkPatternProfileAblationEvaluation {
    id: string;
    ablationRunId: string;
    taskEpisodeId: string;
    shadowEvaluationId?: string;
    policyVersion: string;
    neutralScore?: number;
    profileScore?: number;
    neutralBoundaryErrorMs?: number;
    profileBoundaryErrorMs?: number;
    winner: string;
    evidenceJson: string;
    metrics?: {
        version: string;
        observedReferenceVersion?: string;
        observedEvidenceStrength?: string;
        observedEvidenceSha256?: string;
        observedEvidence?: Array<{
            id: string;
            role: string;
            sourceKind: string;
            observedAt: number;
            contentHash: string;
            semanticTextHash: string;
        }>;
        observedTaskHash: string;
        observedResultHash: string;
        neutral: WorkPatternProfileAblationBranchMetrics;
        profile: WorkPatternProfileAblationBranchMetrics;
        mergeScope: string;
    };
    createdAt: number;
}

export interface WorkPatternProfileAblationBranchMetrics {
    predictionCount: number;
    matchedPredictionCount: number;
    onsetPrecision: number;
    onsetRecall: number;
    taskMatch: number;
    actionMatch?: number;
    resultMatch?: number;
    boundaryErrorMs?: number;
    splitErrorCount: number;
    mergeErrorCount: number;
    calibrationError?: number;
    compositeScore: number;
}

export interface WorkPatternProfileSelectionEntry {
    ref: string;
    kind: string;
    evidenceClass: string;
    included: boolean;
    reason: string;
    truncated?: boolean;
    learning?: TaskContextLearningProvenance;
    onsetPromptIncluded?: boolean;
    onsetPromptReason?: string;
    shadowContextIncluded?: boolean;
    shadowContextReason?: string;
}

export interface WorkPatternProfileDebugDetail {
    generatedAt: number;
    snapshot: WorkPatternProfileSnapshot;
    promptDiagnostic?: {
        version: string;
        influenceMode: string;
        sourceSnapshotBytes: number;
        sourceContextBytes: number;
        promptProjectionBytes: number;
        promptBudgetBytes: number;
        rawReferenceCount: number;
        selectedProjectionCount: number;
        capabilitySignalCount: number;
        contradictionCount: number;
        manifestEntryCount: number;
        promptCandidateCount: number;
        promptIncludedCount: number;
        promptOmittedCount: number;
        sourceContextSha256: string;
        promptProjectionSha256: string;
        selectionManifestSha256: string;
        fits: boolean;
        error?: string;
    };
    evidence: Array<{
        id: string;
        profileSnapshotId: string;
        sourceKind: string;
        sourceId: string;
        observedAt: number;
        contentHash: string;
        included: boolean;
        reason: string;
        createdAt: number;
    }>;
    corrections: WorkPatternProfileCorrection[];
    episodes: Array<{
        id: string;
        status: string;
        task: string;
        desiredResult: string;
        inferredStartAt: number;
        contextSnapshotId: string;
        contextSha256: string;
        workProfileSha256: string;
        profileSelectionVersion: string;
        profileOnsetPresent: boolean;
        profileOnsetSourceContextSha256: string;
        profileOnsetPromptSha256: string;
        profileOnsetSelectionSha256: string;
        onsetProfileSelectionManifest: WorkPatternProfileSelectionEntry[];
        onsetProfileSelectionReason: string;
        profileSourceContextSha256: string;
        profilePromptSha256: string;
        profileSelectionSha256: string;
        profileInfluenceRefs: string[];
        profileConflict: boolean;
        profileSelectionManifest: WorkPatternProfileSelectionEntry[];
        profileSelectionReason: string;
        includedContextCount: number;
        excludedContextCount: number;
    }>;
    shadowRuns: Array<{
        id: string;
        taskEpisodeId: string;
        capabilityKey: string;
        status: string;
        model: string;
        promptVersion: string;
        contextSha256: string;
        profileSha256: string;
        profileInfluenceRefs: string[];
        profileConflict: boolean;
        createdAt: number;
    }>;
    ablations: WorkPatternProfileAblationRun[];
    evaluations: WorkPatternProfileAblationEvaluation[];
    modelAttempts: TaskAgentAttempt[];
    replay: {
        profileSnapshotId: string;
        profileSha256: string;
        sourceCutoffAt: number;
        onsetAttemptIds: string[];
        taskEpisodeIds: string[];
        shadowRunIds: string[];
    };
    rollout: WorkPatternProfileRolloutStatus;
}

export const GetWorkPatternProfileDebugSnapshot = appBinding<
    [number],
    WorkPatternProfileDebugSnapshot
>("GetWorkPatternProfileDebugSnapshot");
export const GetWorkPatternProfileDebugDetail = appBinding<
    [string, number],
    WorkPatternProfileDebugDetail
>("GetWorkPatternProfileDebugDetail");
export const GetSelfDreamDebugSnapshot = appBinding<[number], unknown>(
    "GetSelfDreamDebugSnapshot",
);
export const GetSelfDreamDebugRun = appBinding<[string], unknown>(
    "GetSelfDreamDebugRun",
);
export const ReplaySelfDreamDebug = appBinding<
    [{ runId: string; prompt: string; model: string }],
    unknown
>("ReplaySelfDreamDebug");
// Mirrors SelfDreamUpdate in wails-gui/self_dream_product.go.
export interface SelfDreamUpdate {
    id: string;
    runId: string;
    operation: "merge" | "split" | "contextualize" | "contradict" | "supersede";
    before: string[];
    after: string[];
    scope: { kind: string; key?: string };
    evidenceCount: number;
    uncertainty?: string;
    createdAt: number;
}
export type SelfDreamDecision =
    | "accept"
    | "correct"
    | "keep_separate"
    | "not_enough_evidence";
export interface ReviewSelfDreamOperationInput {
    operationId: string;
    idempotencyKey: string;
    decision: SelfDreamDecision;
    note?: string;
}
export const ListSelfDreamUpdates = appBinding<[], SelfDreamUpdate[]>(
    "ListSelfDreamUpdates",
);
export const ReviewSelfDreamOperation = appBinding<
    [ReviewSelfDreamOperationInput],
    unknown
>("ReviewSelfDreamOperation");
export const GetBackgroundUnderstandingSnapshot = appBinding<[], unknown>(
    "GetBackgroundUnderstandingSnapshot",
);
export interface ComputerUseAppDefinition {
    id: string;
    label: string;
    category: string;
    entryUrl?: string;
    nativeBundle?: string;
}
export interface ComputerUseAppAccessSnapshot {
    policyVersion: string;
    mode: "computer_use_only";
    oauthFallback: false;
    available: boolean;
    accessibility: boolean;
    screenRecording: boolean;
    screenLocked: boolean;
    status: string;
    statusReason: string;
    apps: ComputerUseAppDefinition[];
}
export interface ComputerUseBrowserProbe {
    bundle: string;
    name: string;
    pid?: number;
    active: boolean;
    running: boolean;
    useCount?: number;
    windowTitles?: string[];
}
export interface ComputerUseAppAccessProbe {
    snapshot: ComputerUseAppAccessSnapshot;
    probedAt: number;
    browsers: ComputerUseBrowserProbe[];
    receiptHash: string;
}
export const GetComputerUseAppAccessSnapshot = appBinding<
    [],
    ComputerUseAppAccessSnapshot
>("GetComputerUseAppAccessSnapshot");
export const ProbeComputerUseAppAccess = appBinding<
    [],
    ComputerUseAppAccessProbe
>("ProbeComputerUseAppAccess");
export const GetBackgroundUnderstandingRunDetail = appBinding<
    [string],
    unknown
>("GetBackgroundUnderstandingRunDetail");
export interface WorkPatternProfileCorrection {
    id: string;
    idempotencyKey: string;
    profileSnapshotId: string;
    scopeKind: string;
    scopeKey: string;
    revision: number;
    originalStatement: string;
    correction: string;
    createdAt: number;
}

export interface AuthorityAction {
    capability: string;
    tool_name: string;
    operation?: string;
    effect: "read" | "local_write" | "external_write" | "privileged" | "unknown";
    resource?: string;
    recipient?: string;
    reversible: boolean;
}

export interface AuthorityEvaluation {
    policy_version: string;
    tool_use_id?: string;
    decision: "allow" | "require_approval" | "deny";
    reason: string;
    grant_id?: string;
    context: {
        run_id?: string;
        session_id?: string;
        initiative_id?: string;
        shadow_run_id?: string;
        execution_mode?: "normal" | "shadow";
        unattended: boolean;
    };
    action: AuthorityAction;
}

export const GetComposioApiKeyStatus = appBinding<
    [],
    { configured: boolean; entity_id?: string }
>("GetComposioApiKeyStatus");
export const SaveComposioApiKey = appBinding<[string], void>(
    "SaveComposioApiKey",
);
export const ClearComposioApiKey = appBinding<[], void>("ClearComposioApiKey");
export const TestTelegramConnection = appBinding<
    [string],
    { success: boolean; bot_id?: number; bot_username?: string; error?: string }
>("TestTelegramConnection");
export const SaveTelegramLocalCredentials = appBinding<[string], void>(
    "SaveTelegramLocalCredentials",
);
export const TestSlackConnection = appBinding<
    [string, string],
    { success: boolean; team_name?: string; bot_user_id?: string; error?: string }
>("TestSlackConnection");
export const SaveSlackLocalCredentials = appBinding<[string, string], void>(
    "SaveSlackLocalCredentials",
);
export const TestDiscordConnection = appBinding<
    [string],
    {
        success: boolean;
        bot_user_id?: string;
        bot_username?: string;
        invite_url?: string;
        error?: string;
    }
>("TestDiscordConnection");
export const SaveDiscordLocalCredentials = appBinding<[string], void>(
    "SaveDiscordLocalCredentials",
);
export const GetDiscordInviteURL = appBinding<[], string>("GetDiscordInviteURL");
// Standalone IM connector reuse: archive on disconnect → reconnect without
// re-pasting the token (mirrors Feishu). { has, label } describes the archived app.
type ArchivedConnectorInfo = { has: boolean; label: string };
export const GetTelegramArchived = appBinding<[], ArchivedConnectorInfo>(
    "GetTelegramArchived",
);
export const ReuseTelegramLocal = appBinding<[], void>("ReuseTelegramLocal");
export const DiscardTelegramArchived = appBinding<[], void>(
    "DiscardTelegramArchived",
);
export const GetSlackArchived = appBinding<[], ArchivedConnectorInfo>(
    "GetSlackArchived",
);
export const ReuseSlackLocal = appBinding<[], void>("ReuseSlackLocal");
export const DiscardSlackArchived = appBinding<[], void>("DiscardSlackArchived");
export const GetDiscordArchived = appBinding<[], ArchivedConnectorInfo>(
    "GetDiscordArchived",
);
export const ReuseDiscordLocal = appBinding<[], void>("ReuseDiscordLocal");
export const DiscardDiscordArchived = appBinding<[], void>(
    "DiscardDiscordArchived",
);
export const GetVoiceInputApps = appBinding<[], VoiceInputAppEntry[]>(
    "GetVoiceInputApps",
);

// Text-to-speech (sherpa MeloTTS). SynthesizeSpeech returns base64-encoded WAV
// bytes for the given text; the frontend wraps it in a data: URL and plays it.
export const SynthesizeSpeech = appBinding<[string], string>("SynthesizeSpeech");
export const SplitSpeechText = appBinding<[string], string[]>("SplitSpeechText");
export const PrewarmTts = appBinding<[string], void>("PrewarmTts");
export const GetTtsModelStatus = appBinding<
    [],
    { zh: boolean; en: boolean }
>("GetTtsModelStatus");
export const GetAutoSpeakEnabled =
    appBinding<[], boolean>("GetAutoSpeakEnabled");
export const SetAutoSpeakEnabled = appBinding<[boolean], void>(
    "SetAutoSpeakEnabled",
);
export const GetAutoSpeakAutomationEnabled = appBinding<[], boolean>(
    "GetAutoSpeakAutomationEnabled",
);
export const SetAutoSpeakAutomationEnabled = appBinding<[boolean], void>(
    "SetAutoSpeakAutomationEnabled",
);
export const GetAutoSpeakNotificationsEnabled = appBinding<[], boolean>(
    "GetAutoSpeakNotificationsEnabled",
);
export const SetAutoSpeakNotificationsEnabled = appBinding<[boolean], void>(
    "SetAutoSpeakNotificationsEnabled",
);

// A single entity (person, project, doc, ...) referenced by a semantic event.
export interface SemanticEventEntity {
    type: string;
    name: string;
}

// ── Activities (layer 3: composed from per-app runs) ──────────────────────

// One activity bucket: a coherent piece of work composed from per-app runs.
// Mirrors Go ActivityView in activity_compose_bindings.go.
export interface ActivityView {
    id: string;
    status: string; // active | idle | paused | settled | abandoned
    title: string;
    description: string;
    latestChange: string;
    category: string;
    categoryColor: string;
    entities: SemanticEventEntity[];
    apps: ActivityAppUsage[]; // bundle id + name + time, ranked by dwell desc
    activeMs: number;
    startMs: number;
    endMs: number;
    runCount: number;
    continuesActivityId?: string;
    thumbnails: ActivityRunFrame[];
    inProgress: boolean;
    latestOutcome?: ActivityOutcome;
    latestLink?: ActivityRunLink;
    /** Row actions, prepared by Go (activity_row_actions.go). */
    followUpText?: string;
    followUp?: ActivityFollowUp;
    reminderAt?: number;
    corrected?: boolean;
    actionsWithheld?: "private" | "paused";
}

/** The open follow-up a moment has under Today's "What needs your attention". */
export interface ActivityFollowUp {
    openLoopId: string;
    eventId: string;
    text: string;
}

export interface ActivityOutcome {
    id: string;
    activityId: string;
    runId: string;
    status: 'reported' | 'fixed' | 'done' | 'abandoned';
    reason: string;
    occurredAt: number;
    target: string;
    evidence: {
        sourceKind: string;
        sourceId: string;
        runId?: string;
        observedAt: number;
        tokens: { kind: string; value: string; lastSeenMs: number }[];
    }[];
}

export interface ActivityRunLink {
    runId: string;
    reasonKind: string;
    token?: string;
}

// One app's contribution to an activity: bundle id (for native-icon
// resolution), display name, and active time. Mirrors Go ActivityAppUsage.
export interface ActivityAppUsage {
    appKey: string;
    appName: string;
    activeMs: number;
}

// One step within a run's summary (verb + target, drilling into raw events).
export interface ActivityRunStep {
    eventIds: string[];
    action: string;
    target?: string;
    details?: string[];
}

// The cached summary of one per-app run.
export interface ActivityRunSummary {
    groupId: string;
    title: string;
    description: string;
    activityType: string;
    entities: string[];
    timeline: ActivityRunStep[];
}

// One representative screenshot of a run, with its focus rect (normalized 0..1)
// so the renderer can crop to the focused app region. Mirrors Go ActivityRunFrame.
export interface ActivityRunFrame {
    timestamp: number;
    path: string;
    focusX: number;
    focusY: number;
    focusW: number;
    focusH: number;
}

export interface ActivityKeywordFrame extends ActivityRunFrame {
    visual: boolean;
}

export interface OCRHighlightBox {
    x: number;
    y: number;
    width: number;
    height: number;
    text: string;
}

// One member run in an activity's drill-down (Activity -> runs -> steps), with
// its screenshots grouped under it. Mirrors Go ActivityRunView.
export interface ActivityRunView {
    id: string;
    appKey: string;
    appName: string;
    startMs: number;
    endMs: number;
    activeMs: number;
    summary?: ActivityRunSummary;
    frames: ActivityRunFrame[];
    link?: ActivityRunLink;
}

export const GetActivitiesInRange = appBinding<[number, number], ActivityView[]>(
    "GetActivitiesInRange",
);
export const GetCronRunsByDateRange = appBinding<[number, number], main.CronRunWithJobName[]>(
    "GetCronRunsByDateRange",
);
export const GetActivityRuns = appBinding<[string], ActivityRunView[]>(
    "GetActivityRuns",
);
export const GetActivityKeywordFrames = appBinding<
    [string, string[]],
    ActivityKeywordFrame[]
>("GetActivityKeywordFrames");
export const DeleteActivity = appBinding<[string], void>("DeleteActivity");
export const AddActivityFollowUp = appBinding<[string, string, string], ActivityFollowUp>("AddActivityFollowUp");
export const UndoActivityFollowUp = appBinding<[string, string], void>("UndoActivityFollowUp");
export const SetActivityReminder = appBinding<[string, number], number>("SetActivityReminder");
export const CancelActivityReminder = appBinding<[string], void>("CancelActivityReminder");
export const CorrectActivity = appBinding<[string, string, string], void>("CorrectActivity");
export interface ActivityReminderChoice {
    id: "later_today" | "tomorrow_morning";
    atMs: number;
}
export const GetActivityReminderChoices = appBinding<[], ActivityReminderChoice[]>("GetActivityReminderChoices");

/** "Isoform noticed" (activity_noticed.go). Mirrors Go NoticedRoutineDraft:
 *  when is "" (Know-how) or "daily HH:MM", "weekdays HH:MM" or
 *  "weekly <mon..sun> HH:MM" (a Schedule). */
export interface NoticedRoutineDraft {
    name: string;
    when: string;
    steps: string[];
    will: string[];
    wont: string[];
}
export interface NoticedSuggestion {
    id: string;
    kind: "follow_up" | "routine";
    momentId: string;
    momentTitle: string;
    momentStartMs: number;
    text: string;
    why: string;
    routine?: NoticedRoutineDraft;
}
export interface NoticedRoutineSaved {
    home: "schedule" | "knowhow";
    id: string;
}
export const GetDayNoticed = appBinding<[string], NoticedSuggestion[]>("GetDayNoticed");
export const DismissNoticed = appBinding<[string, string], void>("DismissNoticed");
export const UndoDismissNoticed = appBinding<[string, string], void>("UndoDismissNoticed");
export interface TodayNoticedSchedule { id: string; revision: string; proof: string }
export const AddTodayNoticedSchedule = appBinding<[MobileBriefActivityTarget], TodayNoticedSchedule>("AddTodayNoticedSchedule");
export const UndoTodayNoticedSchedule = appBinding<[MobileBriefActivityTarget, TodayNoticedSchedule], void>("UndoTodayNoticedSchedule");
export const CorrectTodayWeekBelief = appBinding<[string, string, string, string, string], string>("CorrectTodayWeekBelief");
export const UndoTodayWeekBelief = appBinding<[string, string], void>("UndoTodayWeekBelief");
export const CanUndoTodayWeekBelief = appBinding<[string], boolean>("CanUndoTodayWeekBelief");
export const EditTodayNoticedSchedule = appBinding<[MobileBriefActivityTarget, TodayNoticedSchedule, string, NoticedRoutineDraft | null], TodayNoticedSchedule>("EditTodayNoticedSchedule");
export const SaveNoticedRoutine = appBinding<[string, string, NoticedRoutineDraft], NoticedRoutineSaved>("SaveNoticedRoutine");
export interface ActivityThreadResumePoint {
    kind: "path" | "pr" | "repo" | "figure";
    label: string;
}
export interface ActivityThreadStretch {
    start: number;
    end: number;
    activeMs: number;
    activityIds: string[];
    resumePoint: ActivityThreadResumePoint | null;
}
export interface ActivityThreadGap {
    start: number;
    end: number;
    minutes: number;
    label: string;
    threadId: string;
    /** Time without input inside the same work: splits a bar, never an interruption. */
    idle?: boolean;
}
export interface ActivityThreadLinkReason {
    kind: "rule" | "model" | "user";
    keys: string[];
    text: string;
    resumedWithinMinutes?: number;
}
// One thread of a day: moments grouped by Go (activity_threads.go). The screen
// only places the moments Go lists here. Mirrors Go ActivityThreadView.
export interface ActivityThreadView {
    id: string;
    title: string;
    kind: string;
    userTitled: boolean;
    startMs: number;
    endMs: number;
    timeSpentMs: number;
    activityIds: string[];
    stretches: ActivityThreadStretch[];
    gaps: ActivityThreadGap[];
    activeMs: number;
    linkReason: ActivityThreadLinkReason;
    interruption: boolean;
    reviewed: boolean;
    unfinished: boolean;
}

// A day's threads. grouped is false while a past day waits for its first
// grouping; its moments show ungrouped until then. Mirrors Go ActivityDayThreads.
export interface ActivityDayThreads {
    grouped: boolean;
    threads: ActivityThreadView[];
}

export const GetDayThreads = appBinding<[number, number], ActivityDayThreads>("GetDayThreads");
export const ConfirmActivityThreadLink = appBinding<[string], void>("ConfirmActivityThreadLink");
export const MoveActivityToThread = appBinding<[string, string], string>("MoveActivityToThread");
export const RenameActivityThread = appBinding<[string, string], void>("RenameActivityThread");
export const UnlinkActivityRun = appBinding<[string, string], void>("UnlinkActivityRun");
export const GetOCRHighlightBoxes = appBinding<
    [string, string],
    OCRHighlightBox[]
>("GetOCRHighlightBoxes");

export interface ActivityTimelineEvent {
    timestamp: number;
    kind: string;
    appBundleId: string;
    appName: string;
    windowTitle: string;
    clickX?: number;
    clickY?: number;
    clickRole?: string;
    clickLabel?: string;
    keyCode?: number;
    modifiers?: number;
    keyChar?: string;
    keyChars?: string[];
    focusRole?: string;
    focusLabel?: string;
    speechText?: string;
    speechSource?: string;
    speechLikelyEcho?: boolean;
    speechSpeakerVerified?: boolean;
    meetingCandidate?: boolean;
    meetingCandidateApp?: string;
    meetingCandidateReason?: string;
    eventId: string;
    sessionId: string;
    sessionStartTime: number;
    sessionEndTime: number;
    sessionTrigger: string;
    eventIndexInSession: number;
}

export interface ActivityTimelineSemanticItem {
    eventIds: string[];
    action: string;
    target?: string;
    details?: string[];
}

export interface ActivityTimelineGroupSummary {
    groupId: string;
    title: string;
    description: string;
    activityType: string;
    entities: string[];
    timeline: ActivityTimelineSemanticItem[];
}

export interface ActivityTimelineGroup {
    id: string;
    appKey: string;
    appName: string;
    startTime: number;
    endTime: number;
    activeMs: number;
    eventCount: number;
    sessionIds: string[];
    triggerKinds: string[];
    primarySessionId: string;
    events?: ActivityTimelineEvent[];
    summary?: ActivityTimelineGroupSummary;
    identityAnchors?: string[];
    /** The run's first kept screenshot; absent when none was kept. */
    firstFrame?: ActivityRunFrame;
    // Persisted summary generation state:
    // none|generating|done|skipped|failed_transient|failed_permanent
    summaryStatus: string;
}

export const GetActivityTimelineGroups = appBinding<
    [number, number, number, number, string],
    ActivityTimelineGroup[]
>("GetActivityTimelineGroups");
export const GetActivityTimelineGroupsForSessions = appBinding<
    [string[]],
    ActivityTimelineGroup[]
>("GetActivityTimelineGroupsForSessions");
export const GetAppIconsForBundleIDs = appBinding<
    [string[]],
    Record<string, string>
>("GetAppIconsForBundleIDs");
export const GenerateActivityTimelineGroupSummary = appBinding<
    [string],
    ActivityTimelineGroupSummary
>("GenerateActivityTimelineGroupSummary");
export const AddVoiceInputApp = appBinding<[string, string], void>(
    "AddVoiceInputApp",
);
export const RemoveVoiceInputApp = appBinding<[string], void>(
    "RemoveVoiceInputApp",
);
// First-class file-based handoffs (~/.yansu-agent/handoff). Explicit exports
// per CLAUDE.md rule 14 — the static imports in composer-suggestions/
// Tasks resolve against the shim, not the generated wailsjs file.
export type HandoffEntry = {
    id: string;
    title: string;
    status: string; // active | in_progress | needs_review | blocked | completed | aborted | stale
    schemaVersion?: number;
    blockedReason?: string;
    // Why a Needs you task stopped without finishing, and who stopped an aborted one.
    stopCause?: string;
    stoppedBy?: string;
    stopAssistant?: string;
    stoppedAt?: number;
    /** A stopped row says Review while a step needs checking or a reply needs a choice. */
    stepsToCheck?: number;
    repliesToChoose?: number;
    group?: string;
    progress?: { done: number; total: number; label: string };
    runner?: "claude_code" | "codex" | "isoform";
    criteria?: string[] | null;
    verification?: { met: number; total: number; note?: string; reason?: string } | null;
    source: string;
    sourceId: string;
    sourceLink: string;
    appName: string;
    workdir: string;
    keyFiles: string[] | null;
    chatSessionId: string;
    cronJobId: string;
    createdAt: number;
    updatedAt: number;
    body: string;
    plan?: {
        purpose: string;
        desiredResult: string;
        scopeSummary: string;
        includes: string[];
        excludes: string[];
        access: string[];
        risk: string;
        riskSummary: string;
        reversibility: string;
        expectedEffort: string;
        whyNow: string;
    };
};
export type HandoffAcceptResult = {
    handoffId: string;
    sessionId: string;
    title: string;
    prompt: string;
    workdir: string;
    model: string;
};
export const ListHandoffs = appBinding<[], HandoffEntry[]>("ListHandoffs");
export type CreateTaskInput = { title: string; brief: string; workDir: string };
export type TaskSuggestionView = {
    id: string;
    kind: string;
    title: string;
    why: string;
    whyLocalized?: Record<string, string>;
    prompt: string;
    version: string;
    deckKind?: string;
    evidence?: { text: string; textLocalized?: Record<string, string>; source: string; at: string };
};
export const SelectWorkDir = appBinding<[], string>("SelectWorkDir");
export const CreateTask = appBinding<[CreateTaskInput], HandoffEntry>("CreateTask");
export const ListTaskSuggestions = appBinding<[], TaskSuggestionView[]>("ListTaskSuggestions");
// Find more in Tasks › Up next: the phone's search and cooldown.
export type TaskSuggestionsFindMore = { offers: TaskSuggestionView[]; nextAllowedAt?: string };
export const FindMoreTaskSuggestions = appBinding<[], TaskSuggestionsFindMore>("FindMoreTaskSuggestions");
// The day of the first week, 0 on the day setup finished; -1 when unknown.
export const GetFirstWeekDay = appBinding<[], number>("GetFirstWeekDay");
// The first day (handoff 2026-10-28 first-day v1.0.0): the Claude Code and
// Codex history read on this computer, and today's calendar people.
export type HistoryProject = {
    id: string; name: string; path: string; assistants: string[];
    sessionsThisWeek: number; sessions14d: number; lastAt: number;
    status: string; openProblem: string; openProblemAt: number;
    /** Pass 2, in the app's language: the question to ask about the open problem, and a shorter "what's left" one. */
    openQuestion: string; openChip: string;
};
export type AssistantHistoryView = {
    enabled: boolean; state: "off" | "none" | "reading" | "ready"; assistants: string[];
    projects: HistoryProject[]; beliefCount: number; knowHowCount: number; scannedAt: number; pass2Done: boolean;
};
export const GetAssistantHistory = appBinding<[], AssistantHistoryView>("GetAssistantHistory");
export const RefreshAssistantHistory = appBinding<[], void>("RefreshAssistantHistory");
export const GetReadAssistantHistory = appBinding<[], boolean>("GetReadAssistantHistory");
export const SetReadAssistantHistory = appBinding<[boolean], void>("SetReadAssistantHistory");
export type CalendarWeekPerson = {
    key: string; name: string; email: string; principalId: string;
    nextMeetingTitle: string; nextMeetingAt: number; recurring: boolean; meetingsThisWeek: number;
};
export type CalendarFirstDayMeeting = { title: string; start: number; attendeeNames: string[] };
export type CalendarFreeRange = { start: number; end: number };
export type CalendarFirstDay = {
    paused: boolean; readsOff: boolean;
    connected: boolean; meetingsLeftToday: number; peopleLeftToday: number; peopleThisWeek?: number; people: CalendarWeekPerson[];
    nextMeeting: CalendarFirstDayMeeting | null; freeRanges: CalendarFreeRange[];
};
export const GetCalendarFirstDay = appBinding<[], CalendarFirstDay>("GetCalendarFirstDay");
export type WatchedToday = { startedAt: number; minutes: number; screenRecording: boolean; paused: boolean };
export const GetWatchedToday = appBinding<[], WatchedToday>("GetWatchedToday");
export type TaskOfferReference = { id: string; version: string };
export type TaskSuggestionDraft = { prompt: string; reference: TaskOfferReference };
export const PrepareTaskSuggestion = appBinding<[string, string], TaskSuggestionDraft>("PrepareTaskSuggestion");
export const SendPromptWithOfferReferences = appBinding<
    [string, string, string, string, string, Array<{media_type: string; data_base64: string}>, Array<{file_name: string; media_type: string; data_base64: string; file_size: number}>, boolean, boolean, string, string, StreamItemData, TaskOfferReference[]],
    number
>("SendPromptWithOfferReferences");
export const DecideTaskSuggestion = appBinding<[string, "not_now" | "dismiss"], void>("DecideTaskSuggestion");
export const GetActiveHandoffChips = appBinding<[number], HandoffEntry[]>(
    "GetActiveHandoffChips",
);
export const AcceptHandoff = appBinding<[string], HandoffAcceptResult>(
    "AcceptHandoff",
);
// Revalidates an in_progress handoff and its chat session and returns what
// handleStartHandoffExecution needs to start it (for example work approved on
// the iPhone while this window was closed).
export type AgencyHandoffContinuationInput = {
    handoffId: string;
    sessionId: string;
};
export const ResolveAgencyHandoffExecution = appBinding<
    [AgencyHandoffContinuationInput],
    HandoffAcceptResult
>("ResolveAgencyHandoffExecution");
export const AbortHandoff = appBinding<[string], void>("AbortHandoff");
export const DeleteHandoff = appBinding<[string], void>("DeleteHandoff");

// First-class SOP bundles (~/.yansu-agent/sops/<slug>/ with SOP.md + optional
// scripts/). Explicit exports per CLAUDE.md rule 14 — the static imports in
// Playbook bindings resolve against the shim, not the generated wailsjs file.
export type SopEntry = {
    id: string;
    title: string;
    description: string;
    trigger: string;
    status: string; // draft | confirmed
    source: string; // triage | manual
    sourceIds: string[] | null;
    version: number;
    createdAt: number;
    updatedAt: number;
    scripts: string[] | null;
    // Web addresses the steps open; a run may open only the ones approved
    // with its schedule (PlaybookView.sites).
    sites?: string[] | null;
    body: string;
};
export type SopAuthorEvent = {
    timeMs: number;
    kind: string;
    message: string;
    tool: string;
    error: string;
};
export type SopAuthorStatus = {
    running: boolean;
    title: string;
    source: string;
    sourceId: string;
    startedAt: number;
    updatedAt: number;
    lastEvent: string;
    lastError: string;
    events: SopAuthorEvent[] | null;
};
export type SopRunResult = {
    sopId: string;
    sessionId: string;
    title: string;
    prompt: string;
    workdir: string;
    model: string;
};
export const ListSOPs = appBinding<[], SopEntry[]>("ListSOPs");
export const SaveSOP = appBinding<
    [string, string, string, string, string],
    SopEntry
>("SaveSOP");
export const DeleteSOP = appBinding<[string], void>("DeleteSOP");
export const RunSOP = appBinding<[string], SopRunResult>("RunSOP");
export const GenerateSOPFromRange = appBinding<[number, number], void>(
    "GenerateSOPFromRange",
);
export const GenerateSOPFromDescription = appBinding<[string], void>(
    "GenerateSOPFromDescription",
);
export const GetSOPAuthorStatus = appBinding<[], SopAuthorStatus>(
    "GetSOPAuthorStatus",
);
export const GetSOPScript = appBinding<[string, string], string>(
    "GetSOPScript",
);
export const ExportSOP = appBinding<[string], string>("ExportSOP");
export const ImportSOP = appBinding<[], SopEntry>("ImportSOP");

// Playbooks that run by themselves and send their result where the user
// chose. Mirrors wails-gui/playbook_run.go and internal/sop/plans.go.
export type PlaybookProposal = {
    host: string;
    siteName: string;
    time: string; // usual first visit, "9:05"
    days: number;
    weekdays: boolean;
    titles: string[] | null;
    proposedAt: number;
    answer: string; // "" | accepted | declined
    answeredAt: number;
    /** Unix ms; an unanswered offer stays hidden until then. */
    snoozedUntil?: number;
};
export type PlaybookSchedule = {
    jobId: string;
    time: string; // "08:55"
    weekdays: boolean;
    enabled: boolean;
};
export type PlaybookRunRecord = {
    runId: string;
    at: number;
    ok: boolean;
    result: string;
};
export type PlaybookView = {
    id: string;
    proposal: PlaybookProposal | null;
    schedule: PlaybookSchedule | null;
    channels: string[];
    sites: string[];
    lastRun: PlaybookRunRecord | null;
    nextRunAt: number;
    suggestedTime: string;
    /** Why the schedule does not run: "unapproved" until the user saves it again. */
    held: string;
};
export type PlaybookChannels = {
    desktop: boolean;
    push: boolean;
    text: boolean;
    email: boolean;
    // Whether texts may name the playbook and its numbers.
    textNames: boolean;
};
export type PlaybooksState = {
    plans: PlaybookView[];
    channels: PlaybookChannels;
    defaultChannels: string[];
};
export type PlaybookScheduleInput = {
    id: string;
    enabled: boolean;
    time: string;
    weekdays: boolean;
    channels: string[];
    // The pages the panel showed; saving approves exactly these.
    sites: string[];
};
export const GetPlaybooksState = appBinding<[], PlaybooksState>("GetPlaybooksState");
export const AcceptPlaybookSuggestion = appBinding<[string], PlaybookView>("AcceptPlaybookSuggestion");
export const DeclinePlaybookSuggestion = appBinding<[string], PlaybookView>("DeclinePlaybookSuggestion");
// Not now on a playbook offer in Tasks › Up next: hidden for two weeks, still unanswered.
export const SnoozePlaybookSuggestion = appBinding<[string], PlaybookView>("SnoozePlaybookSuggestion");
export const SetPlaybookSchedule = appBinding<[PlaybookScheduleInput], PlaybookView>("SetPlaybookSchedule");
// The second argument is the pages shown next to the button; running approves them.
export const RunPlaybookNow = appBinding<[string, string[]], PlaybookView>("RunPlaybookNow");

// Reads a cua-driver window screenshot from disk and returns a
// "data:<mediaType>;base64,..." string the chat can render inline. The
// webview can't read host files itself, so this binding is the only
// bridge (see cua_screenshot_read.go). Explicit export per CLAUDE.md
// rule 14 — the static import in cua-screenshot.tsx resolves against the
// shim, not the generated wailsjs file.
export const ReadCuaScreenshotBase64 = appBinding<[string], string>(
    "ReadCuaScreenshotBase64",
);

// Computer-use virtual-cursor overlay toggle. Explicit exports per
// CLAUDE.md rule 14 — the static imports in settings-page.tsx resolve
// against the shim, not the generated wailsjs file. See
// cua_cursor_overlay_config.go for the Go side.
export const GetCuaCursorOverlayEnabled = appBinding<[], boolean>(
    "GetCuaCursorOverlayEnabled",
);
export const SetCuaCursorOverlayEnabled = appBinding<[boolean], void>(
    "SetCuaCursorOverlayEnabled",
);

// AX-parser probe (DevTools). Mirrors activity_monitor.go AXProbeOptions /
// AXProbeResult / AXParserDispatch and appparser.ParserCatalogEntry.
export type AXParserDispatch = {
    matchedApp?: string;
    contentType?: string;
    messagesCount: number;
    title?: string;
    channel?: string;
    parsedNonEmpty: boolean;
};

export type AXProbeOptions = {
    bundleId: string;
    windowIndex: number;
    delaySeconds: number;
    includeTree: boolean;
    includeOCR: boolean;
    upscaleOCR: boolean;
    label: string;
};

export type ParsedConversationMessage = {
    sender?: string;
    content: string;
    timestamp?: string;
    threadParent?: string;
};

export type ParsedContent = {
    contentType?: string;
    title?: string;
    body?: string;
    url?: string;
    channel?: string;
    messages?: ParsedConversationMessage[];
    metadata?: Record<string, string>;
    dedupOfHash?: string;
};

export type AXProbeResult = {
    timestamp: number;
    bundleId: string;
    appName: string;
    windowTitle: string;
    browserURL?: string;
    axTreeBytes: number;
    axTextBytes: number;
    axTree?: string;
    axText?: string;
    parsed?: ParsedContent;
    dispatch: AXParserDispatch;
    ocrText?: string;
    ocrDurationMs?: number;
    ocrError?: string;
    fusedBody?: string;
    fusedSpliced: boolean;
    fusedReason?: string;
    fusedWithOCRPreview?: string;
    dumpPath?: string;
};

export type AXParserCatalogEntry = {
    name: string;
    label: string;
    contentType?: string;
    bundles?: string[];
    urlHosts?: string[];
    notes?: string;
};

// Mirrors appparser.UnsupportedApp — apps we investigated and could
// NOT parse via macOS Accessibility. Surfaced in the DevTools panel so
// future readers don't repeat investigations that already concluded.
export type AXUnsupportedApp = {
    label: string;
    bundles: string[];
    reason: string;
    probedAt: string;
};

export const ProbeAXParser = appBinding<[AXProbeOptions], AXProbeResult>(
    "ProbeAXParser",
);
export const ListAXParserCatalog = appBinding<[], AXParserCatalogEntry[]>(
    "ListAXParserCatalog",
);
export const ListAXParserUnsupportedApps = appBinding<[], AXUnsupportedApp[]>(
    "ListAXParserUnsupportedApps",
);
export const ListAXWindowsByBundle = appBinding<[string], string>(
    "ListAXWindowsByBundle",
);

// SetMcpServerTrusted: explicit binding so the static ESM import in
// mcp-hub-settings.tsx resolves against the shim (rather than relying
// on `export *`, which can drop newly-added names when generated types
// lag). See CLAUDE.md rule #14.
import type { main } from "../../wailsjs/go/models";
export const SetMcpServerTrusted = appBinding<
    [string, boolean],
    main.McpServerView
>("SetMcpServerTrusted");

// Identity / Principal management bindings. The generated wailsjs file
// regenerates on build, but until then these explicit exports keep
// frontend code that imports them by name from crashing at module load
// (see CLAUDE.md rule 14).
export type AppHandleDTO = {
    id: number;
    principalId: string;
    app: string;
    handleId: string;
    displayHandle: string;
    createdAt: number;
};

export type PrincipalDTO = {
    mergeId?: string;
    contactName?: string;
    contactPhoto?: string;
    contactLinked?: boolean;
    id: string;
    kind: string;
    displayName: string;
    relationship: string;
    notes: string;
    createdFrom: string;
    confidence: number;
    aliases: string[];
    createdAt: number;
    updatedAt: number;
    appHandles?: AppHandleDTO[];
    // Computed display label: for anonymous principals with a single
    // bound app this is "{App} · {displayName}" (e.g. "WeChat · 妈");
    // for self/contact/multi-app-anonymous it equals displayName.
    qualifiedDisplayName: string;
};

export const ListPrincipals = appBinding<[], PrincipalDTO[]>("ListPrincipals");
export const ListPrincipalsByKind = appBinding<[string], PrincipalDTO[]>(
    "ListPrincipalsByKind",
);
export const EnsureSelfProfile = appBinding<[string, string[]], PrincipalDTO>(
    "EnsureSelfProfile",
);
export const CreateContact = appBinding<
    [string, string, string[], AppHandleDTO[]],
    PrincipalDTO
>("CreateContact");
export const UpdatePrincipal = appBinding<[PrincipalDTO], PrincipalDTO>(
    "UpdatePrincipal",
);
export const DeletePrincipal = appBinding<[string], void>("DeletePrincipal");
export const BindAppHandle = appBinding<
    [string, string, string, string],
    AppHandleDTO
>("BindAppHandle");
export const UnbindAppHandle = appBinding<[number], void>("UnbindAppHandle");
export const PromoteAnonymousToContact = appBinding<
    [string, string, string, string[]],
    PrincipalDTO
>("PromoteAnonymousToContact");

// Fold srcID into dstID. Transfers all aliases / app handles / sightings /
// recent messages with conflict resolution (see storage.MergePrincipals).
export const MergePrincipals = appBinding<[string, string], PrincipalDTO>(
    "MergePrincipals",
);

// People projections contain structured data; presentation copy stays in i18n.
export type PersonLoop = {
    id: string;
    kind: "reply_owed" | "you_owe" | "waiting_on_them" | "reappeared";
    since: number;
    title: string;
    dueAt: number;
};
export type PersonCard = {
    id: string;
    kind: string;
    name: string;
    relationship: string;
    photo: string;
    loops: PersonLoop[];
    lastSeenAt: number;
    lastSeenApp: string;
};
export type DuplicateGroup = {
    id: string;
    memberIds: string[];
    suggestedTargetId: string;
    reason: "same_handle" | "same_email";
    app: string;
};
export type PeopleOverview = {
    headline: { repliesOwed: number; thingsOwed: number; peopleWaitingOnYou: number };
    open: PersonCard[];
    everyone: PersonCard[];
    duplicates: DuplicateGroup[];
    knownCount: number;
    selfGuesses?: { selfId: string; personId: string; name: string; app: string }[];
};
export type MergeResult = { mergeId: string; target: PersonCard };
export type PersonDetail = {
    id: string;
    name: string;
    relationship: string;
    notes: string;
    researchSummary: string;
    loops: PersonLoop[];
    togetherFailed?: boolean;
    together: { at: number; kind: "meeting" | "activity" | "message"; title: string; app: string; durationMs: number; direction?: "in" | "out" | "" }[];
    sharedTopics: { id: string; name: string }[];
    seenIn: string[];
    messageCount: number;
};
export const ListPeopleOverview = appBinding<[], PeopleOverview>("ListPeopleOverview");
export const MergePeople = appBinding<[string, string[]], MergeResult>("MergePeople");
export const UndoMerge = appBinding<[string], void>("UndoMerge");
export const NotSamePeople = appBinding<[string[]], void>("NotSamePeople");
export const UndoNotSamePeople = appBinding<[string[]], void>("UndoNotSamePeople");
export const GetPersonDetail = appBinding<[string], PersonDetail>("GetPersonDetail");

// Normalize every Anonymous principal's display name (strip trailing
// timestamps / role badges / message bleed) and fold duplicates that
// collapse to the same cleaned name. Tidies up People → Unidentified
// after upstream parser bugs polluted the bucket list.
export type CleanupReportDTO = {
    scanned: number;
    renamed: number;
    merged: number;
    crossAppMerged: number;
    mergedIntoNamed: number;
    prunedNonChat: number;
    prunedImplausible: number;
    mergedIds?: string[];
};
export const CleanupAnonymousPrincipals = appBinding<[], CleanupReportDTO>(
    "CleanupAnonymousPrincipals",
);

// Drill from a Principal to the Activity sessions they appeared in. Each
// row carries enough metadata for the Who you know page to render a clickable
// summary that jumps straight to the Activity detail page.
export type PrincipalActivitySessionDTO = {
    sessionId: string;
    startTime: number;
    endTime: number;
    appNames: string[];
    isMeeting?: boolean;
    firstSeenAt: number;
    lastSeenAt: number;
};
export const ListPrincipalActivitySessions = appBinding<
    [string, number],
    PrincipalActivitySessionDTO[]
>("ListPrincipalActivitySessions");

// AI research dossier for a person (Contact / Who They Are / Your
// Relationship, with [n] citations). Generated in the background by a
// tool-using agent; the Who you know page subscribes to the "people:research"
// Wails event to refresh as status changes.
export type ResearchSourceDTO = {
    n: number;
    title: string;
    url: string;
};
export type PersonResearchDTO = {
    principalId: string;
    status: string; // idle | researching | ready | failed
    summary: string;
    markdown: string;
    sources: ResearchSourceDTO[];
    model: string;
    error: string;
    researchedAt: number;
    updatedAt: number;
};
export const GetPersonResearch = appBinding<[string], PersonResearchDTO>(
    "GetPersonResearch",
);
// (principalId, additionalContext) — additionalContext may be "".
export const StartPersonResearch = appBinding<[string, string], void>(
    "StartPersonResearch",
);
export const DeletePersonResearch = appBinding<[string], void>(
    "DeletePersonResearch",
);

// Inverse drill: which principals appeared in a given Activity session.
// Surfaced on the Activity detail page so the user can see who Isoform
// identified and click to highlight a specific person's messages.
export type SessionPrincipalDTO = {
    principalId: string;
    displayName: string;
    kind: string;
    firstSeenAt: number;
    lastSeenAt: number;
    // See PrincipalDTO.qualifiedDisplayName.
    qualifiedDisplayName: string;
};
export const ListSessionPrincipals = appBinding<
    [string, number],
    SessionPrincipalDTO[]
>("ListSessionPrincipals");

// Voice fingerprint enrollment (R19). One enrolled sample per row; the
// backend keeps the embedding bytes and only exposes metadata.
export type VoiceSampleDTO = {
    id: number;
    principalId: string;
    dim: number;
    sampleRate: number;
    audioSeconds: number;
    label: string;
    capturedAt: number;
};
export type VoiceEnrollmentStatusDTO = {
    principalId: string;
    sampleCount: number;
    lastCapturedAt: number;
    modelReady: boolean;
    modelMissingHint: string;
};
export const GetVoiceEnrollmentStatus = appBinding<
    [string],
    VoiceEnrollmentStatusDTO
>("GetVoiceEnrollmentStatus");
export const ListVoiceSamples = appBinding<[string], VoiceSampleDTO[]>(
    "ListVoiceSamples",
);
export const EnrollVoiceSample = appBinding<[string, number], VoiceSampleDTO>(
    "EnrollVoiceSample",
);
export const DeleteVoiceSample = appBinding<[number], void>(
    "DeleteVoiceSample",
);

// In-app "is my enrollment actually working" check. Records a fresh
// sample on click, runs cosine vs each enrolled embedding, returns
// the score breakdown + a verdict the UI can render plainly.
export type VoiceMatchSampleScore = {
    sampleId: number;
    label: string;
    capturedAt: number;
    score: number;
};
export type VoiceMatchResultDTO = {
    bestScore: number;
    bestSampleId: number;
    threshold: number;
    verdict: "match" | "no_match" | "no_enrollment";
    audioSeconds: number;
    enrolledSampleHits?: VoiceMatchSampleScore[];
};
export const TestVoiceMatch = appBinding<[string, number], VoiceMatchResultDTO>(
    "TestVoiceMatch",
);

// Per-session speaker diarization. Greedy clustering of each mic-source
// speech event's cached embedding. Returns "You" + "Speaker 1" / "Speaker 2"
// chips. Returns [] when no mic speech / no enrollment / pre-R20 session.
export type SessionSpeakerClusterDTO = {
    label: string;
    isSelf: boolean;
    utteranceCount: number;
    firstSeenAt: number;
    lastSeenAt: number;
    eventTimestamps: number[];
};
export const ListSessionSpeakerClusters = appBinding<
    [string],
    SessionSpeakerClusterDTO[]
>("ListSessionSpeakerClusters");

// Identity debug bindings (Round 6). Read-only helpers the Activity page
// calls when the "Show identity verdicts" toggle is on so the developer
// can see how each event was classified and how each chat sender resolves.
export type EventIdentityVerdictDTO = {
    index: number;
    contentMode: "performed" | "received" | "observed" | "ambient" | "unknown";
    reasoning: string;
    speechHints: {
        source?: string;
        likelyEcho?: boolean;
        nonUserSpeech: boolean;
        mediaApp?: boolean;
        browserMedia?: boolean;
        meetingContext?: boolean;
    };
};

export type ChatSenderVerdictDTO = {
    rawSender: string;
    resolved: boolean;
    kind: "self" | "contact" | "anonymous" | "unknown";
    principalId?: string;
    displayName?: string;
    relationship?: string;
    attributionMethod?: string;
    confidence: number;
    label: string;
};

// Take a batch of captured events plus the session's in-meeting flag and
// return one verdict per event (same length, same order). Pure compute —
// no side effects.
export const ClassifyActivityEvents = appBinding<
    // The Go binding accepts []activitymon.ActivityEvent which on the wire is
    // an array of objects — TypeScript callers pass the same DetailEventRow
    // shape the timeline already renders.
    [unknown[], boolean],
    EventIdentityVerdictDTO[]
>("ClassifyActivityEvents");

// Resolve a single chat sender against the identity store. autoBucket is
// always off in this binding (see Go side) so hovering a popup message
// never creates an Anonymous principal as a side effect.
export const ResolveChatSender = appBinding<
    [string, string],
    ChatSenderVerdictDTO
>("ResolveChatSender");

// Subject corrections share the forget cascade. The desktop uses a reviewed
// scope token; the legacy binding remains available for other clients.
export interface MemoryEvidence {
    day: number;
    apps: string[];
    snippet: string;
}
export const GetMemoryEvidence = appBinding<[string], MemoryEvidence[]>("GetMemoryEvidence");

export const RejectMemoryAttribution = appBinding<[string, string], void>(
    "RejectMemoryAttribution",
);
export const RejectMemoryAttributionWithPreview = appBinding<[string, string, string], void>(
    "RejectMemoryAttributionWithPreview",
);

export interface MemoryForgetItem {
    kind:
        | "memory"
        | "memory_archive"
        | "memory_history"
        | "memory_correction"
        | "self_dream"
        | "purpose_map"
        | "work_profile"
        | "shadow_learning"
        | "background_learning";
    id: string;
    title: string;
    recordType?:
        | "analysis"
        | "attempt"
        | "evaluation"
        | "decision"
        | "compilation"
        | "event"
        | "evidence"
        | "snapshot"
        | "revision"
        | "receipt"
        | "relation"
        | "hypothesis"
        | "entry";
}

export interface MemoryForgetPreview {
    memoryId: string;
    title: string;
    items: MemoryForgetItem[];
    embeddingCount: number;
    historyCount: number;
    retentionDays: number;
    confirmationToken: string;
}

export const GetMemoryForgetPreview = appBinding<[string], MemoryForgetPreview>(
    "GetMemoryForgetPreview",
);
export const ForgetMemoryEntry = appBinding<[string, string], void>("ForgetMemoryEntry");

// Opens a file an interactive chat agent declared via its <deliverables>
// block in the system default app. Explicit shim export (not just `export *`)
// because the generated App.js has not been regenerated for this new binding
// yet — static named imports resolve against the shim, so without this line
// the consuming component crashes at module load (see CLAUDE.md rule 14).
// args: [sessionTag (localId), absolutePath]
export const OpenChatDeliverable = appBinding<[string, string], void>(
    "OpenChatDeliverable",
);

// Rich deliverable actions (Cola-style "Open with" / "Reveal" / subtitle).
// All take [sessionTag (localId), absolutePath]; OpenWith also takes appPath.
export interface DeliverableInfo {
    name: string;
    ext: string;
    sizeBytes: number;
}

export interface DeliverableOpenWithApp {
    name: string;
    path: string;
    icon: string;
    isDefault: boolean;
}

export const ChatDeliverableInfo = appBinding<
    [string, string],
    DeliverableInfo
>("ChatDeliverableInfo");

export const ListChatDeliverableOpenWithApps = appBinding<
    [string, string],
    DeliverableOpenWithApp[]
>("ListChatDeliverableOpenWithApps");

export const OpenChatDeliverableWithApp = appBinding<
    [string, string, string],
    void
>("OpenChatDeliverableWithApp");

export const RevealChatDeliverable = appBinding<[string, string], void>(
    "RevealChatDeliverable",
);

// Unified Deliverables hub. Aggregates chat/automation/handoff
// outputs into one status-sorted list (a read-time projection; each source
// stays the single source of truth for its own data). File operations route
// back to the owning source via [source, sourceId, path].
export interface DeliverableFileRef {
    path: string;
    name: string;
    /** Agent-written one-sentence description of what the file is. Absent for
     *  rows recorded before the "path | description" protocol. */
    description?: string;
}

export interface DeliverableEntry {
    id: string;
    source: string; // chat | automation | handoff
    sourceId: string;
    title: string;
    summary: string;
    status: string; // completed | in_progress | failed | active | stale | aborted | skipped
    files: DeliverableFileRef[];
    createdAt: number;
    updatedAt: number;
}

export const ListDeliverables = appBinding<[], DeliverableEntry[]>(
    "ListDeliverables",
);

// args: [tag (localId), sessionId, title, summary, files]
// Each file is {path, description?}; the backend re-derives name from path.
export const RecordChatDeliverables = appBinding<
    [
        string,
        string,
        string,
        string,
        Array<Pick<DeliverableFileRef, "path" | "description">>,
    ],
    void
>("RecordChatDeliverables");

// File operations take [source, sourceId, absolutePath]; OpenWith adds appPath.
export const OpenDeliverableFile = appBinding<[string, string, string], void>(
    "OpenDeliverableFile",
);

export const RevealDeliverableFile = appBinding<[string, string, string], void>(
    "RevealDeliverableFile",
);

export const DeliverableFileInfo = appBinding<
    [string, string, string],
    DeliverableInfo
>("DeliverableFileInfo");

export const ListDeliverableFileOpenWithApps = appBinding<
    [string, string, string],
    DeliverableOpenWithApp[]
>("ListDeliverableFileOpenWithApps");

export const OpenDeliverableFileWithApp = appBinding<
    [string, string, string, string],
    void
>("OpenDeliverableFileWithApp");

// Memory subject-reassignment binding (Round 2). Rewrites a memory entry
// so the named Principal becomes the speaker, caps dimension to
// topic_highlight, and appends a record to ~/.yansu-agent/memory/.reassignments.jsonl
// for future learning passes. Pass the correct principal ID + a free-text
// reason describing why it was mis-attributed.
export const ReassignMemoryAttribution = appBinding<
    [string, string, string],
    void
>("ReassignMemoryAttribution");

// Who you know page bindings + aggregate DTOs. These power the standalone
// "People" page (overview of everyone Isoform has identified, with per-app
// breakdown and recent message previews).
export type PrincipalSightingDTO = {
    app: string;
    messageCount: number;
    firstSeenAt: number;
    lastSeenAt: number;
};

export type PrincipalRecentMessageDTO = {
    app: string;
    seenAt: number;
    preview: string;
};

export type PrincipalStatsDTO = {
    principalId: string;
    totalMessages: number;
    firstSeenAt: number;
    lastSeenAt: number;
    byApp: PrincipalSightingDTO[];
    recentMessages: PrincipalRecentMessageDTO[];
};

export type PrincipalWithStatsDTO = {
    principal: PrincipalDTO;
    stats: PrincipalStatsDTO;
};

export const GetPrincipalStats = appBinding<[string], PrincipalStatsDTO>(
    "GetPrincipalStats",
);
export const ListPrincipalsWithStats = appBinding<[], PrincipalWithStatsDTO[]>(
    "ListPrincipalsWithStats",
);

/** Who you know's saved directory, served only under the privacy stamp it was made under. */
export type PeopleSavedView = {
  overview: PeopleOverview;
  entries: PrincipalWithStatsDTO[];
  savedAt: string;
};
export const GetSavedCopyStamp = appBinding<[], string>("GetSavedCopyStamp");
export const GetSavedPeopleView = appBinding<[], PeopleSavedView | null>("GetSavedPeopleView");
export const SavePeopleSavedView = appBinding<[string, PeopleOverview, PrincipalWithStatsDTO[]], void>("SavePeopleSavedView");
export const ForgetSavedPeopleView = appBinding<[], void>("ForgetSavedPeopleView");

// Phase 1.5 — telemetry tier controls (Privacy settings). Explicit
// exports because the auto-generated wailsjs/go/main/App may lag
// behind the Go side until `wails generate` runs, and static ESM
// imports against the shim itself need these names to resolve at
// module load. See wails-gui/telemetry_tier_pref.go for the Go side.
export const GetTelemetryTier = appBinding<[], string>("GetTelemetryTier");
export const RecordProductActivity = appBinding<[number, string], void>("RecordProductActivity");
export const RecordFirstBriefSeen = appBinding<[], void>("RecordFirstBriefSeen");
// SetTelemetryTier returns a void Promise from the JS side but the
// Go signature returns an error — Wails translates any non-nil error
// into a rejected Promise here, so callers should .catch() to surface
// disk-full / permission-denied failures to the user.
export const SetTelemetryTier = appBinding<[string], void>("SetTelemetryTier");

// Fires the in-house native desktop toast (rejects when the user has turned
// Isoform notifications off in System Settings — see notificationExplicitlyDenied
// on the Go side). Statically imported by the settings page, so it must be
// re-exported explicitly here per the shim binding rule.
export const TestDesktopNotification = appBinding<[], void>(
    "TestDesktopNotification",
);

// Fires a native desktop toast after a 5s in-Go delay — diagnostic for the
// window-pop bug. Statically imported by the settings page, so re-export here.
export const FireDelayedDesktopNotification = appBinding<
    [string, string],
    void
>("FireDelayedDesktopNotification");

export const SendPrompt = appBinding<
    [string, string, string, string, string, boolean, boolean, string, string, string, string, string],
    void
>("SendPrompt");

export const SendPromptWithAttachments = appBinding<
    [string, string, string, string, string, Array<{media_type: string; data_base64: string}>, Array<{file_name: string; media_type: string; data_base64: string; file_size: number}>, boolean, boolean, string, string, string, string, string],
    void
>("SendPromptWithAttachments");

export const SendPromptWithTodayContext = appBinding<
    [string, string, string, string, string, Array<{media_type: string; data_base64: string}>, Array<{file_name: string; media_type: string; data_base64: string; file_size: number}>, boolean, boolean, string, string, StreamItemData, TodayChatContextRef],
    number
>("SendPromptWithTodayContext");

export const SendMainChatWithTopic = appBinding<[string, string], import("../../wailsjs/go/models").main.MainChatTopicSendResult>("SendMainChatWithTopic");
export const SendMainChat = appBinding<[string], import("../../wailsjs/go/models").main.MainChatTopicSendResult>("SendMainChat");
export const AskMainChat = appBinding<[string], import("../../wailsjs/go/models").main.MainChatTopicSendResult>("AskMainChat");
export const AskMainChatWithContext = appBinding<[string, import("./today-chat-navigation").TodayChatContextRef], import("../../wailsjs/go/models").main.MainChatTopicSendResult>("AskMainChatWithContext");
export interface TodayPanelAnswer { question: string; answer: string; userItemId: string; answerItemId: string; madeAt?: number }
export interface TodayBriefDraftRoute { personId: string; name: string; address: string; source: "mail" | "contacts"; sourceAt?: number; subject: string }
export interface TodayBriefDraft { answer: TodayPanelAnswer; state: "ready" | "sent" | "unknown"; route: TodayBriefDraftRoute; subject: string; destination?: string }
export const GetTodayBriefDraftRoute = appBinding<[import("./today-chat-navigation").TodayChatContextRef], TodayBriefDraftRoute>("GetTodayBriefDraftRoute");
export const GetTodayBriefDraft = appBinding<[import("./today-chat-navigation").TodayChatContextRef], TodayBriefDraft | null>("GetTodayBriefDraft");
export const SendTodayBriefDraft = appBinding<[import("./today-chat-navigation").TodayChatContextRef, string, string, string], void>("SendTodayBriefDraft");
export interface TodayMeetingNote { answer: TodayPanelAnswer; name: string; address: string; subject: string; state: "ready" | "sent" | "unknown" }
export const GetTodayMeetingNote = appBinding<[import("./today-chat-navigation").TodayChatContextRef], TodayMeetingNote | null>("GetTodayMeetingNote");
export const SendTodayMeetingNote = appBinding<[import("./today-chat-navigation").TodayChatContextRef, string, string], void>("SendTodayMeetingNote");
export const GetTodayPanelAnswer = appBinding<[import("./today-chat-navigation").TodayChatContextRef], TodayPanelAnswer | null>("GetTodayPanelAnswer");
export const SendMainChatWithContext = appBinding<[string, import("./today-chat-navigation").TodayChatContextRef], import("../../wailsjs/go/models").main.MainChatTopicSendResult>("SendMainChatWithContext");
export const LoadStreamItemsPage = appBinding<[sessionLocalID: string, cursor: number, limit: number], import("../../wailsjs/go/models").persistence.StreamItemPageData>("LoadStreamItemsPage");
export const ListMainChatTopicItems = appBinding<[string, string, number], import("../../wailsjs/go/models").main.MainChatTopicPage>("ListMainChatTopicItems");
export const PrepareTodayChatTurn = appBinding<[string, string, TodayChatContextRef], string>("PrepareTodayChatTurn");
export const PrepareTodayChatDraft = appBinding<[TodayChatContextRef], TodayChatDraft>("PrepareTodayChatDraft");
export const PrepareTodayChatDrafts = appBinding<[TodayChatContextRef[]], TodayChatDraftResult[]>("PrepareTodayChatDrafts");
export const OpenTodayBriefConversation = appBinding<[string], TodayBriefOpened>("OpenTodayBriefConversation");
export const OpenTodayBriefParagraph = appBinding<[string, number, string], TodayBriefOpened>("OpenTodayBriefParagraph");
export const PrepareTodayBriefParagraph = appBinding<[string, number, string], TodayChatContextRef>("PrepareTodayBriefParagraph");

// Feishu scan-to-create-app (registration) bindings. Statically imported by the
// settings page, so re-export here per the shim binding rule.
export type FeishuRegistration = {
    session_id: string;
    verification_uri_complete: string;
    verification_uri: string;
    user_code: string;
    expires_in: number;
    interval: number;
};
export const StartFeishuRegistration = appBinding<[brand: string], FeishuRegistration>(
    "StartFeishuRegistration",
);
export const PollFeishuRegistration = appBinding<[string], string>(
    "PollFeishuRegistration",
);

// Reuse-vs-create-new bindings for reconnecting Feishu after a disconnect.
export type FeishuArchivedApp = {
    app_id: string;
    feishu_user_name?: string;
    feishu_account_id?: string;
    brand?: string;
    connected_at?: string;
};
export const GetArchivedFeishuApp = appBinding<[brand: string], FeishuArchivedApp | null>(
    "GetArchivedFeishuApp",
);
export const ReuseArchivedFeishuApp = appBinding<[brand: string], void>(
    "ReuseArchivedFeishuApp",
);

// lark-cli capability (composer chip) + per-brand status + user-identity login.
export type LarkCliStatus = {
    runnable: boolean;
    configured: boolean;
    enabled: boolean;
    scopes_ready: boolean;
    operable: boolean;
};
export const GetLarkCliStatus = appBinding<[], LarkCliStatus>("GetLarkCliStatus");
export type LarkAccountStatus = {
    runnable: boolean;
    configured: boolean;
    scopes_ready: boolean;
    user_authed: boolean;
};
export type LarkUserLogin = {
    verification_url: string;
    user_code: string;
    device_code: string;
    expires_in: number;
};
export const GetLarkAccountStatus = appBinding<[brand: string], LarkAccountStatus>(
    "GetLarkAccountStatus",
);
export const LarkCliUserLoginStart = appBinding<[brand: string], LarkUserLogin>(
    "LarkCliUserLoginStart",
);
export const LarkCliUserLoginPoll = appBinding<[brand: string, deviceCode: string], string>(
    "LarkCliUserLoginPoll",
);

export interface BgShellStatus {
    shell_id: string;
    tag: string;
    command: string;
    started_at: number;
    running: boolean;
}
export const GetBackgroundShells = appBinding<[string], BgShellStatus[]>(
    "GetBackgroundShells",
);

// ── BYOA: bring-your-own AI provider ──────────────────────────────────────
// Lets the user point the in-process agent at their own Anthropic- or
// OpenAI-compatible endpoint with their own key, bypassing the Isoform
// gateway. Statically imported by the settings page, so the bindings (and
// their config/result types) are declared here per the shim binding rule —
// the auto-generated models do not exist until the next wails regen.
export interface AIProviderConfig {
    // "yansu" (gateway, default) | "anthropic" | "openai" |
    // "claude-code" | "codex" — the wire format the runner speaks.
    provider: string;
    // Catalog entry the user picked (e.g. "deepseek", "qwen", "custom"). UI/
    // listing convenience only; empty for legacy configs and external agents.
    presetId: string;
    apiKey: string;
    baseUrl: string;
    // Primary ("big-brain") model for chat/code roles.
    model: string;
    // Optional "small-brain" model for cheap background tasks (summaries,
    // titles, memory extraction). Empty falls back to `model`.
    lightModel: string;
    // External-agent CLI binary path/name (claude-code / codex only).
    command: string;
    // Lift the CLI's OS-level Bash sandbox (claude-code / codex only).
    disableSandbox?: boolean;
    // Last model list fetched from the provider ("Fetch models"). UI-only
    // cache so the dropdowns repopulate on reopen without re-fetching.
    cachedModels?: string[];
}

// A curated bring-your-own API provider entry (see ai_provider_presets.go).
// Prefills the endpoint + seed models so a user only pastes a key.
export interface AIProviderPreset {
    id: string;
    name: string;
    // "openai" | "anthropic" — becomes AIProviderConfig.provider on save.
    apiFormat: string;
    baseUrl: string;
    seedModels: string[];
    apiKeyUrl: string;
    requiresKey: boolean;
    custom: boolean;
}

export interface TestAIProviderResult {
    success: boolean;
    provider: string;
    model: string;
    latencyMs: number;
    error?: string;
}

export const GetAIProviderConfig = appBinding<[], AIProviderConfig>(
    "GetAIProviderConfig",
);
export const SaveAIProviderConfig = appBinding<[AIProviderConfig], void>(
    "SaveAIProviderConfig",
);
// Persists the fetched model list (UI cache) without validation or an agent
// restart, so the dropdowns survive a reopen the moment a fetch succeeds.
export const SetCachedAIProviderModels = appBinding<[string[]], void>(
    "SetCachedAIProviderModels",
);
export const TestAIProvider = appBinding<
    [AIProviderConfig],
    TestAIProviderResult
>("TestAIProvider");
// Returns a copy-paste curl command reproducing the exact probe request, for
// manual debugging when the in-app test surfaces only an opaque status.
export const BuildProviderTestCurl = appBinding<[AIProviderConfig], string>(
    "BuildProviderTestCurl",
);
// Fetches available model ids from an API provider's listing endpoint
// (OpenAI /models, Anthropic /v1/models). Rejects when unavailable; the
// settings UI then falls back to manual model entry.
export const ListAIProviderModels = appBinding<[AIProviderConfig], string[]>(
    "ListAIProviderModels",
);
// The curated BYOA provider catalog: prefilled endpoints, seed models, and
// key links that drive the provider picker.
export const ListAIProviderPresets = appBinding<[], AIProviderPreset[]>(
    "ListAIProviderPresets",
);
// True when the current user holds a paid plan that unlocks BYOA / BYOK
// configuration. The AI Provider settings surface gates on this.
export const IsBYOAEntitled = appBinding<[], boolean>("IsBYOAEntitled");
// True when the user has a usable AI path configured (an installed external
// agent OR an API-key BYOK provider with key + model). The retirement banner
// hides as soon as this returns true.
export const AIReady = appBinding<[], boolean>("AIReady");
// Probes PATH for the Claude Code and Codex CLIs. LookPath only — no
// subprocess — so it is cheap to call on startup. The retirement banner uses
// it to recommend an installed agent.

export type ContactsPermission =
    | "granted"
    | "denied"
    | "restricted"
    | "not_determined"
    | "not_applicable";

export interface ContactsAccessStatus {
    syncState?: string;
    syncPaused?: boolean;
    supported: boolean;
    platform: string;
    permission: ContactsPermission;
    consentAccepted: boolean;
    consentAcceptedAt: string;
    importedCount: number;
    lastImportAt: string;
    vcardSupported: boolean;
}

export interface SystemContactView {
    id: string;
    displayName: string;
    phones: string[];
    emails: string[];
    imported: boolean;
}

export interface ContactsImportResult {
    imported: number;
    merged: number;
    skipped: number;
    removed: number;
    errors: string[];
}

export const GetContactsAccessStatus = appBinding<[], ContactsAccessStatus>(
    "GetContactsAccessStatus",
);
export const OpenPermissionSettings = appBinding<[string], void>("OpenPermissionSettings");
export const AcceptContactsConsent = appBinding<[number], ContactsAccessStatus>(
    "AcceptContactsConsent",
);
export const RequestContactsAccess = appBinding<[], ContactsAccessStatus>(
    "RequestContactsAccess",
);
export const ListSystemContacts = appBinding<[], SystemContactView[]>(
    "ListSystemContacts",
);
export const ImportSystemContacts = appBinding<
    [string[], number],
    ContactsImportResult
>("ImportSystemContacts");
export const ImportContactsFromVCard = appBinding<
    [string, number],
    ContactsImportResult
>("ImportContactsFromVCard");
export const PickVCardFile = appBinding<[], string>("PickVCardFile");
export const RemoveImportedContacts = appBinding<[], ContactsImportResult>(
    "RemoveImportedContacts",
);

// Calendars: what Isoform can see (Settings > Observation) and the web
// calendar reading switch. See wails-gui/calendar_bindings.go.
export interface CalendarSourceView {
    id: string;
    kind: "eventkit" | "web";
    provider: string;
    providerLabel: string;
    title: string;
    detectedBy: "eventkit" | "history" | "screen" | "peek";
    calendars: number;
    lastSeenAt: string;
    lastReadAt: string;
}
export interface CalendarOverview {
    supported: boolean;
    authorization: string;
    granted: boolean;
    enabled: boolean;
    lastSyncAt: string;
    lastError: string;
    upcomingCount: number;
    sources: CalendarSourceView[];
    peekEnabled: boolean;
    peekLastAt: string;
    peekProviders: string[];
}
export const GetCalendarOverview = appBinding<[], CalendarOverview>("GetCalendarOverview");
export const SetCalendarPeekEnabled = appBinding<[boolean], void>("SetCalendarPeekEnabled");
export const SyncCalendarsNow = appBinding<[], void>("SyncCalendarsNow");
/** One upcoming event for the main chat's hub (times in unix ms). */
export interface UpcomingCalendarEvent {
    id: string;
    title: string;
    start: number;
    end: number;
    location: string;
    calendarTitle: string;
    allDay: boolean;
    conferenceUrl: string;
}
export const GetUpcomingCalendarEvents = appBinding<[number], UpcomingCalendarEvent[]>(
    "GetUpcomingCalendarEvents",
);

// Reminders: what Isoform can see (Settings > Observation). See
// wails-gui/reminders_bindings.go.
export interface ReminderListView {
    title: string;
    open: number;
    overdue: number;
}
export interface RemindersOverview {
    supported: boolean;
    authorization: string;
    granted: boolean;
    enabled: boolean;
    lastSyncAt: string;
    lastError: string;
    openCount: number;
    overdueCount: number;
    lists: ReminderListView[];
}
export const GetRemindersOverview = appBinding<[], RemindersOverview>("GetRemindersOverview");
export const SyncRemindersNow = appBinding<[], void>("SyncRemindersNow");

// Recent files: what Isoform can see (Settings > Observation). See
// wails-gui/files_bindings.go.
export interface FilesFolderView {
    key: "documents" | "desktop" | "downloads" | "icloud";
    checked: boolean;
    available: boolean;
    recent: number;
}
export interface FilesPrivateFolderView {
    /** Opaque handle for RemoveFilesPrivateFolder; the full path stays in Go. */
    id: string;
    displayPath: string;
}
export interface FilesOverview {
    supported: boolean;
    enabled: boolean;
    lastReadAt: string;
    lastError: string;
    todayCount: number;
    recentCount: number;
    linkedCount: number;
    folders: FilesFolderView[];
    privateFolders: FilesPrivateFolderView[];
}
export const GetFilesOverview = appBinding<[], FilesOverview>("GetFilesOverview");
export const SyncFilesNow = appBinding<[], void>("SyncFilesNow");
export const AddFilesPrivateFolder = appBinding<[string], string>("AddFilesPrivateFolder");
export const RemoveFilesPrivateFolder = appBinding<[string], void>("RemoveFilesPrivateFolder");

// Mail: what Isoform can see (Settings > Observation). See
// wails-gui/mail_bindings.go.
export interface MailOverview {
    supported: boolean;
    granted: boolean;
    enabled: boolean;
    mailOpen: boolean;
    lastReadAt: string;
    lastError: string;
    // Reading stopped after Mail was slow to answer too often; only Refresh
    // reads again.
    stalled: boolean;
    received: number;
    waiting: number;
}
export const GetMailOverview = appBinding<[], MailOverview>("GetMailOverview");
export const SyncMailNow = appBinding<[], void>("SyncMailNow");

// The token the activity page adds to its host-bridge screenshot and
// recording URLs. See wails-gui/activity_video.go.
export const GetHostBridgeMediaToken = appBinding<[], string>("GetHostBridgeMediaToken");

// Settings > Storage: what activity data takes on disk, how long screenshots
// are kept, and the cleanup that reports before it changes anything. See
// wails-gui/activity_storage_usage.go and activity_storage_cleanup.go.
export interface ActivityStorageUsage {
    screenshotBytes: number;
    screenshotFiles: number;
    screenshotDays: number;
    oldestScreenshotMs: number;
    audioBytes: number;
    audioFiles: number;
    databaseBytes: number;
    databaseFreeBytes: number;
    otherBytes: number;
    totalBytes: number;
    measuredAtMs: number;
}
export interface StorageCleanupItem {
    count: number;
    bytes: number;
    afterBytes: number;
    oldestMs: number;
    newestMs: number;
}
export interface StorageCleanupPlan {
    createdAtMs: number;
    limitBytes: number;
    screenshotDays: number;
    usage: ActivityStorageUsage;
    unlinkedScreenshots: StorageCleanupItem;
    unlinkedRecordings: StorageCleanupItem;
    expiredSessions: StorageCleanupItem;
    retentionDays: number;
    expiredScreenshots: StorageCleanupItem;
    overLimitScreenshots: StorageCleanupItem;
    overLimitRecordings: StorageCleanupItem;
    shrinkScreenshots: StorageCleanupItem;
    compressRecordings: StorageCleanupItem;
    databaseSpace: StorageCleanupItem;
    recordingsNeedFFmpeg: boolean;
    limitUnreachable: boolean;
    keptBytes: number;
    freedBytes: number;
    afterBytes: number;
}
export interface StorageBudgetStatus {
    state: "ok" | "confirm" | "unreachable";
    limitBytes: number;
    projectedBytes: number;
    keptBytes: number;
    screenshots: StorageCleanupItem;
    recordings: StorageCleanupItem;
    checkedAtMs: number;
}
export interface StorageCleanupStatus {
    running: boolean;
    phase: "" | "unlinked" | "database" | "expired" | "limit" | "shrink" | "compress" | "done";
    done: number;
    total: number;
    startedAtMs: number;
    finishedAtMs: number;
    beforeBytes: number;
    afterBytes: number;
    freedBytes: number;
    databasePending: boolean;
    error?: string;
}
export const GetActivityStorageUsage = appBinding<[], ActivityStorageUsage>("GetActivityStorageUsage");
export const GetScreenshotRetentionDays = appBinding<[], number>("GetScreenshotRetentionDays");
export const SetScreenshotRetentionDays = appBinding<[number], void>("SetScreenshotRetentionDays");
export const PlanActivityStorageCleanup = appBinding<[], StorageCleanupPlan>("PlanActivityStorageCleanup");
export const StartActivityStorageCleanup = appBinding<[], void>("StartActivityStorageCleanup");
export const GetActivityStorageCleanupStatus = appBinding<[], StorageCleanupStatus>(
    "GetActivityStorageCleanupStatus",
);
export const GetActivityStorageBudget = appBinding<[], StorageBudgetStatus>("GetActivityStorageBudget");
export const ConfirmActivityStorageLimit = appBinding<[], void>("ConfirmActivityStorageLimit");

// Grows a chat's streamed reply row; assistant and thinking rows never get
// shorter. Reports whether the row was written. See wails-gui/persistence.go.
export const GrowStreamItemContent = appBinding<[string, string, string], boolean>(
    "GrowStreamItemContent",
);

export interface StorageStartupStatus {
    code: "" | "checking" | "schema_too_new" | "storage_unavailable";
    storedVersion?: number;
    supportedVersion?: number;
}
export const GetStorageStartupStatus = appBinding<[], StorageStartupStatus>(
    "GetStorageStartupStatus",
);

// EnsureDailySummary / EnsureWeeklySummary: explicit bindings so the static
// ESM imports in the activity screens resolve against the shim even while the
// generated wailsjs file lags the Go App struct (see CLAUDE.md rule 14). Each
// returns the cached data immediately and starts at most one background
// generation; AutoStatus on the payload reports "ready" | "refreshing" |
// "unavailable".
export const EnsureDailySummary = appBinding<
    [string],
    main.DailySummaryData
>("EnsureDailySummary");
export const EnsureWeeklySummary = appBinding<
    [string],
    main.WeeklySummaryData
>("EnsureWeeklySummary");

export * from "@wails-generated-app";

export interface ContactMatchDTO {
    contactId: string;
    displayName: string;
    photo: string;
    phones: string[];
    emails: string[];
    suggestedBy: '' | 'phone' | 'email' | 'name';
    available: boolean;
}
export const SearchImportedContacts = appBinding<[string, string], ContactMatchDTO[]>('SearchImportedContacts');
export const LinkPersonToContact = appBinding<[string, string], void>('LinkPersonToContact');
export const UnlinkPersonContact = appBinding<[string], void>('UnlinkPersonContact');
export const CancelContactsSync = appBinding<[], void>('CancelContactsSync');

// Meeting notes on Activity (meeting_notes_activity.go, meeting_notes.go).
// A meeting's notes live with its activity sessions; the transcript and the
// recording of each part are read through the meeting's privacy gates.
export interface MeetingNotesFollowup {
    text: string;
    owner: 'me' | 'other' | 'unknown';
    person?: string;
    due?: string;
}
export interface MeetingNotesPart {
    sessionId: string;
    startTime: number;
    endTime: number;
    wordCount: number;
    hasTranscript: boolean;
    hasRecording: boolean;
}
export interface MeetingNotesView {
    meetingKey: string;
    meetingApp: string;
    title: string;
    startTime: number;
    endTime: number;
    summary: string;
    decisions: string[];
    followups: MeetingNotesFollowup[];
    userNotes: string;
    parts: MeetingNotesPart[];
}
export const GetMeetingNotesForSessions = appBinding<[string[]], MeetingNotesView[]>('GetMeetingNotesForSessions');
export const GetRecentMeetingNotes = appBinding<[number], MeetingNotesView[]>('GetRecentMeetingNotes');
export const GetMeetingNotesTranscript = appBinding<[string], string>('GetMeetingNotesTranscript');
export const RevealMeetingRecording = appBinding<[string], void>('RevealMeetingRecording');
export const DeleteMeetingNotes = appBinding<[string], void>('DeleteMeetingNotes');

export interface MeetingCallTurn {
    speakerId: string;
    name: string;
    number: number;
    source: string;
    startTime: number;
    endTime: number;
    text: string;
}
export interface MeetingCallTurnsView { turns: MeetingCallTurn[]; status: string; }
export const GetMeetingCallTurns = appBinding<[string], MeetingCallTurnsView>('GetMeetingCallTurns');
export const RenameMeetingSpeaker = appBinding<[string, string, string], void>('RenameMeetingSpeaker');
export const ForgetVoices = appBinding<[], void>('ForgetVoices');

// Goals: what the user does by hand to a goal on the computer (Today, the
// Goals page). MoveGoal rejects with "goal_changed", "goal_unavailable" or
// "goal_invalid", which the page words in the user's language.
export type GoalMoveAction = "resolve" | "reopen" | "waiting" | "forget" | "restore" | "keep" | "revise";

export interface GoalMoveInput {
    goalId: string;
    /** The event the user saw; a goal that moved since answers goal_changed. */
    expectedEventId?: string;
    action: GoalMoveAction;
    /** One key per click, so a retried call never moves the goal twice. */
    key: string;
    waitingOn?: string;
    /** An edit (revise): the new title, and with setDue the due day,
     *  YYYY-MM-DD, or "" for none; without setDue the due is kept. */
    title?: string;
    dueDate?: string;
    setDue?: boolean;
}

export interface GoalMoveResult {
    id: string;
    /** The goal's event after the move; Undo names it. */
    eventId: string;
    state: PurposeLifecycleState;
    title: string;
}

export const MoveGoal = appBinding<[GoalMoveInput], GoalMoveResult>('MoveGoal');

/** A goal the user adds on About you; Key makes a retry add it once. */
export interface GoalCreateInput {
    text: string;
    key: string;
}
export const CreateGoal = appBinding<[GoalCreateInput], GoalMoveResult>('CreateGoal');

/** The goal Today asks "Still working on ...?" about (goal_checkin.go). */
export interface GoalCheckInView {
    goalId: string;
    eventId: string;
    title: string;
    /** RFC 3339. */
    dueAt: string;
}

export const GetGoalCheckIn = appBinding<[], GoalCheckInView | null>('GetGoalCheckIn');
export const NoteGoalCheckInShown = appBinding<[string, string], void>('NoteGoalCheckInShown');

/** Who added a goal (goals_page.go). */
export type GoalOrigin = "chat" | "phone" | "activity" | "reminder" | "meeting" | "noticed";

/** One goal on the computer's Goals page; the page writes its sentences. */
export interface GoalItemView {
    id: string;
    eventId: string;
    kind: PurposeKind;
    title: string;
    state: "active" | "waiting" | "resolved";
    group: "tracking" | "done" | "held";
    slipping: boolean;
    /** RFC 3339, the end of the due day. */
    dueAt?: string;
    /** YYYY-MM-DD, for the edit form. */
    dueDate?: string;
    addedAt: string;
    updatedAt: string;
    lastEvent: string;
    origin: GoalOrigin;
    waitingOn?: string;
    /** The aim, when it says more than the title. */
    outcome?: string;
}

export interface GoalsPageView {
    tracking: GoalItemView[];
    done: GoalItemView[];
    held: GoalItemView[];
}

export interface GoalStoryView {
    goal: GoalItemView;
    /** Newest first. */
    history: { event: string; at: string }[];
    sources: { kind: "you" | "activity" | "reminder" | "meeting" | "memory" | "other"; count: number }[];
}

export const ListGoals = appBinding<[], GoalsPageView>('ListGoals');
export const GetGoal = appBinding<[string], GoalStoryView>('GetGoal');

export const GetActivityMonthStats = appBinding<[string], main.DayStatBrief[]>("GetActivityMonthStats");

// The saved portrait (handoff portrait v1.0.0, v2.0.0). Reads never call a
// model; each phrase carries the beliefs it rests on.
export interface AboutYouPortraitPart {
    text: string;
    memoryIds?: string[] | null;
    confirmed: boolean;
}
/** Why a changed sentence changed: the beliefs confirmed, or Not quite with its note. */
export interface AboutYouPortraitCause {
    confirm?: string[] | null;
    note: string;
    notQuite: boolean;
}
export interface AboutYouPortraitSentence {
    role: "connect" | "insight" | "life" | "action";
    parts: AboutYouPortraitPart[];
    changed: boolean;
    cause?: AboutYouPortraitCause | null;
}
export interface AboutYouPortraitLine {
    text: string;
    memoryIds?: string[] | null;
    area: string;
    source?: string;
    confirmed: boolean;
}
export interface AboutYouPortraitLately {
    parts: AboutYouPortraitPart[];
    memoryId: string;
    thisMonth: number;
    usual: number;
}
export interface AboutYouPortraitProgress {
    beliefs: number;
    beliefsNeeded: number;
    days: number;
    daysNeeded: number;
    groups: number;
    groupsNeeded: number;
    moreDays: number;
    sidesReady: boolean;
    met: boolean;
}
export interface AboutYouPortraitFailure {
    reason: "limit" | "no_ai" | "no_answer" | "unusable";
    at: number;
    retryAt: number;
    assistant: string;
}
/** A confirm row: a belief whose confirmation would let dropped sentences pass. */
export interface AboutYouPortraitCandidate {
    memoryId: string;
    text: string;
    answer: "" | "confirmed" | "noted";
}
export interface AboutYouPortrait {
    /** shape: the name and the shape with no sentence passed yet (v2.0.0). */
    state: "learning" | "writing" | "ready" | "shape" | "failed";
    /** Word keys: the first from planning and reasoning, the second from focus and words. */
    name?: string[] | null;
    nameAreas?: string[][] | null;
    /** Focus, reasoning, words, planning; 0 leans to the first pole. */
    sides?: number[] | null;
    sentences?: AboutYouPortraitSentence[] | null;
    lately?: AboutYouPortraitLately | null;
    workWithMe?: AboutYouPortraitLine[] | null;
    beliefCount: number;
    generatedAt: number;
    progress?: AboutYouPortraitProgress | null;
    failure?: AboutYouPortraitFailure | null;
    writing: boolean;
    rewriting: boolean;
    forgotten: boolean;
    assistant: string;
    candidates?: AboutYouPortraitCandidate[] | null;
    /** True from That's me on a row until the rewrite lands. */
    confirming: boolean;
    /** That's me on the version shown (answer feedback section 3), or 0. */
    saidAt?: number;
    /** A newer version waits behind "Show it". */
    pending?: boolean;
}
export const GetAboutYouPortrait = appBinding<[], AboutYouPortrait>("GetAboutYouPortrait");
export const RetryAboutYouPortrait = appBinding<[], void>("RetryAboutYouPortrait");
export const ConfirmAboutYouPortrait = appBinding<[], string[]>("ConfirmAboutYouPortrait");
export const UndoConfirmAboutYouPortrait = appBinding<[string[]], void>("UndoConfirmAboutYouPortrait");
export const NotQuiteAboutYouPortrait = appBinding<[string], void>("NotQuiteAboutYouPortrait");
export const AnswerFirstRead = appBinding<[string, string, string], { state: string; confirmedAt?: number }>("AnswerFirstRead");
export const MarkAboutYouPortraitSeen = appBinding<[], void>("MarkAboutYouPortraitSeen");
export const AnswerAboutYouPortraitBelief = appBinding<[string, boolean], void>("AnswerAboutYouPortraitBelief");
export const UndoAboutYouPortraitBelief = appBinding<[string, boolean], void>("UndoAboutYouPortraitBelief");
export const ShowAboutYouPortraitPending = appBinding<[], void>("ShowAboutYouPortraitPending");
// The Today card: once when the first portrait is ready, again for a new name.
export interface PortraitTodayCard {
    renamed: boolean;
    name: string[];
    sides: number[];
    sentence: AboutYouPortraitPart[];
}
export const GetPortraitTodayCard = appBinding<[], PortraitTodayCard | null>("GetPortraitTodayCard");
export const DismissPortraitTodayCard = appBinding<[], void>("DismissPortraitTodayCard");
// Structured know-how reads and the local previous-fix dismissal.
export const ListKnowHow = appBinding<[], main.KnowHowCard[]>("ListKnowHow");
export const IdeaMeaningGroups = appBinding<[string[], number], { groups: number[] | null; pending: boolean } | null>("IdeaMeaningGroups");
export const GetKnowHowMatch = appBinding<[], main.KnowHowMatch | null>("GetKnowHowMatch");
export const DismissKnowHowMatch = appBinding<[string, string], void>("DismissKnowHowMatch");
// The confirm moment: one observed-repetition proposal at a time. Confirm
// writes the knowledge entry; decline suppresses that exact step sequence.
export const GetKnowHowCandidate = appBinding<[], main.KnowHowCandidate | null>("GetKnowHowCandidate");
export const ConfirmKnowHowCandidate = appBinding<[string], main.KnowHowCandidateConfirm>("ConfirmKnowHowCandidate");
export const DeclineKnowHowCandidate = appBinding<[string], void>("DeclineKnowHowCandidate");
export const UndoDeclineKnowHowCandidate = appBinding<[string], void>("UndoDeclineKnowHowCandidate");
export const UndoConfirmKnowHowCandidate = appBinding<[string], void>("UndoConfirmKnowHowCandidate");
export const SaveKnowHowCandidate = appBinding<[string, string, string, string[]], main.KnowHowCandidate | null>("SaveKnowHowCandidate");
// About you read models and explicit confirmation; reuse statistics are shared
// with the knowledge list. Presentation and localized sentences stay in the UI.
export interface AboutYouBelief {
    id: string;
    title: string;
    area?: string;
    topicIds?: string[];
    status: "confirmed" | "learned_today" | "inferred" | "not_quite";
    confidence: number;
    firstLearnedAt: number;
    updatedAt: number;
    sessionCount: number;
    dayCount: number;
    confirmedAt: number;
    /** When the person said Not quite; Isoform isn't using it. */
    notQuiteAt?: number;
    /** One of the portrait's confirm rows. */
    portraitCandidate?: boolean;
    sentMessageCount?: number;
    evidenceFirstAt?: number;
    evidenceLastAt?: number;
    /** The memory's source ("assistant-history" for the Claude Code or Codex
     *  history read on the first day), its assistant and the session's time. */
    source?: string;
    sourceAssistant?: string;
    sourceAt?: number;
}
export interface CommunicationDraftExample {
    id: string;
    to: string;
    subject: string;
    text: string;
    app: string;
    recipientName: string;
    language: string;
    sentences: number;
    greeting: boolean;
    beliefIds: string[];
    createdAt: number;
}
export interface AboutYouView {
    beliefs: AboutYouBelief[];
    newThisWeek: string[];
    totals: { beliefs: number; confirmed: number };
    storage: { files: number; bytes: number; chunks: number; vectorReady: boolean };
}
export interface ReadableEvidence {
    at: number;
    durationMs: number;
    appName: string;
    text: string;
    activitySessionId: string;
}
export interface EntryUses {
    total: number;
    lastAt: number;
    surfaces: { surface: "chat" | "today_chat" | "brief" | "assistant_tool"; count: number; lastAt: number }[];
}
export const ConfirmMemoryEntry = appBinding<[string], main.MemoryEntry>("ConfirmMemoryEntry");
// The answers' Undo (answer feedback section 11): Not quite left blank
// returns the confirmation it took back, for its Undo.
export const UndoConfirmMemoryEntry = appBinding<[string], void>("UndoConfirmMemoryEntry");
export const NotQuiteMemoryEntry = appBinding<[string], number>("NotQuiteMemoryEntry");
export const UndoNotQuiteMemoryEntry = appBinding<[string, number], void>("UndoNotQuiteMemoryEntry");
export const UndoMemoryWording = appBinding<[string, string], main.MemoryEntry>("UndoMemoryWording");
export const GetAboutYou = appBinding<[], AboutYouView>("GetAboutYou");
// Explicit re-exports so typed callers (and the page query cache) see these
// reads without relying on the vite-only '@wails-generated-app' alias.
export const GetMemoryEntries = appBinding<[], main.MemoryEntry[]>("GetMemoryEntries");
export const GetMemoryStatus = appBinding<[], main.MemoryStoreStatus>("GetMemoryStatus");
export const GetKnowledgeEntries = appBinding<[], main.KnowledgeEntry[]>("GetKnowledgeEntries");
export const GetPlatformPermissions = appBinding<[], main.PlatformPermissions>("GetPlatformPermissions");
export const GetBackgroundListening = appBinding<[], boolean>("GetBackgroundListening");
export const LoadUIState = appBinding<[string], string>("LoadUIState");
export const GetCommunicationDraftExamples = appBinding<[string], CommunicationDraftExample[]>("GetCommunicationDraftExamples");
export const GetMemoryEvidenceReadable = appBinding<[string], ReadableEvidence[]>("GetMemoryEvidenceReadable");
export const GetEntryUses = appBinding<[string, string], EntryUses>("GetEntryUses");
export const GetEntryUseCounts = appBinding<[string, string[]], Record<string, number>>("GetEntryUseCounts");
// Settings Overview (RC 1 A7, settings_overview.go): one structured section
// per area and up to three "Worth a look" issues. `unavailable` names only the
// sections that failed (speaksUp, thinksWith, remembers, keeps, briefs); their
// zero values are not real readings.
export interface SettingsOverviewAssistant {
    assistant: string;
    /** Percent left in the window that runs out first; null when unknown. */
    percentLeft: number | null;
    windowLabel: string;
    resetsAt: string;
}
export type SettingsOverviewIssueKind = "noisy_kind" | "phone_same_network_only" | "phone_remote_access_unavailable" | "assistant_out_of_usage";
export interface SettingsOverviewIssue {
    id: string;
    kind: SettingsOverviewIssueKind;
    params: { kind?: string; sent?: number; assistant?: string; resetsAt?: string; host?: string; remoteAccessOff?: MobileRemoteAccessOffReason };
    signature: string;
}
export interface SettingsOverviewView {
    unavailable: string[];
    sees: { watching: boolean; pausedUntil: string | null; ignoredAppsCount: number; passwordManagersHidden: boolean };
    speaksUp: { level: string; dailyCap: number; quietStart: string; quietEnd: string; channels: string[] };
    thinksWith: { order: SettingsOverviewAssistant[] };
    remembers: { awaitingConfirmCount: number };
    keeps: { retention: number; limitBytes: number; usedBytes: number };
    briefs: {
        morningAt: string;
        eveningAt: string;
        adaptive: boolean;
        phone: { name: string; lastSeenAt: string } | null;
        connection: Pick<MobileConnectionStatus, "enabled" | "relayUrl" | "relayState" | "relayLastSeenAt" | "warningCode" | "remoteAccess"> | null;
    };
    ignores: { apps: { name: string; bundleId: string }[] };
    worthALook: SettingsOverviewIssue[];
}
// Settings Overview reads structured values; issue kinds and params are translated by the UI.
export const GetSettingsOverview = appBinding<[], SettingsOverviewView>("GetSettingsOverview");
// Persists the current issue signature for Leave it.
export const DismissOverviewIssue = appBinding<[string], void>("DismissOverviewIssue");
// Minutes is 1-1440. Manual observation controls cancel this persisted timer.
export const PauseObservationFor = appBinding<[number], void>("PauseObservationFor");
export const ResumeObservation = appBinding<[], void>("ResumeObservation");
export const GetTodayObservationPauseSince = appBinding<[], number>("GetTodayObservationPauseSince");
export interface TodayDayMeetingTarget { kind: "prep" | "notes"; meeting?: TodayBeforeYouTalk; sessionId?: string }
export const GetTodayDayMeetingTarget = appBinding<[string], TodayDayMeetingTarget>("GetTodayDayMeetingTarget");
export const GetTodayPrepMeetingForPerson = appBinding<[string], string>("GetTodayPrepMeetingForPerson");
export interface TodayPermissionStates { screenRecording: "not_determined" | "denied" | "granted"; calendars: "not_determined" | "denied" | "granted" }
export const GetTodayPermissionStates = appBinding<[], TodayPermissionStates>("GetTodayPermissionStates");
export const RequestTodayPermission = appBinding<[string], boolean>("RequestTodayPermission");

export type SearchDestination =
    | { kind: "chat_item"; sessionId: string; itemId: string }
    | { kind: "chat"; sessionId: string }
    | { kind: "activity_moment"; activityId: string; sessionId: string; at: number }
    | { kind: "activity_thread"; threadId: string; startMs: number; endMs: number }
    | { kind: "person" | "goal" | "knowledge" | "memory" | "topic"; id: string };

export type SearchPlace = "topic" | "chat" | "activity" | "person" | "goal" | "know_how" | "about_you";

export interface SearchResult {
    id: string;
    place: SearchPlace;
    title: string;
    snippet: string;
    at: number;
    topicId?: string;
    topicName?: string;
    target: SearchDestination;
}

export interface SearchResults {
    query: string;
    groups: Array<{ place: SearchPlace; results: SearchResult[] }>;
    failedPlaces: SearchPlace[];
}

export const SearchEverything = appBinding<[query: string, limit: number], SearchResults>("SearchEverything");
export const FindStreamItemCursor = appBinding<[sessionLocalID: string, itemID: string], number>("FindStreamItemCursor");

export const ListMainChatAnswerChips = appBinding<[string[]], Record<string, string[]>>("ListMainChatAnswerChips");

export const GetClockFormat = appBinding<[], string>("GetClockFormat");
export const GetMainChatOverview = appBinding<[], import("../../wailsjs/go/models").main.MainChatOverview>("GetMainChatOverview");
export const GetMainChatWaiting = appBinding<[], import("../../wailsjs/go/models").main.MainChatWaitingState>("GetMainChatWaiting");
export const ListMainChatWaitingItems = appBinding<[string, number], import("../../wailsjs/go/models").main.MainChatWaitingPage>("ListMainChatWaitingItems");
export const SetMainChatHideRoutine = appBinding<[boolean], void>("SetMainChatHideRoutine");

export interface AboutInfo {
    version: string;
    build: string;
    buildTime: string;
    /** Short commit, set only for a staging build (ST85). */
    commit?: string;
    computer: string;
    os: string;
    phones: string[];
    dataBytes: number;
    backgroundUpdates: boolean;
    lastChecked: number;
    later: boolean;
    rollbackNotice?: string;
    releaseNotesVersion: string;
    releaseNotes: string;
    releaseNotesUrl: string;
    update: { status: string; version?: string; percent?: number; detail?: string; restart_needed?: boolean };
}
export const GetAboutInfo = appBinding<[], AboutInfo>('GetAboutInfo');
export const SetBackgroundUpdates = appBinding<[boolean], void>('SetBackgroundUpdates');
export const RestartToUpdate = appBinding<[], void>('RestartToUpdate');
export const SaveSupportLogs = appBinding<[], void>('SaveSupportLogs');
export const OpenDataFolder = appBinding<[], void>('OpenDataFolder');
export const DismissUpdateUntilNextLaunch = appBinding<[], void>('DismissUpdateUntilNextLaunch');
export const AcknowledgeRollbackNotice = appBinding<[], void>('AcknowledgeRollbackNotice');
export const CheckForUpdates = appBinding<[], {
    available: boolean;
    current: string;
    latest: string;
    download_url?: string;
    release_notes?: string;
    error?: string;
}>('CheckForUpdates');

// Shipped tool manifest and one-use confirmation plans for the download sheet.
export interface ToolDownloadEntry {
    id: string;
    displayName: string;
    version: string;
    sizeBytes: number;
    source: string;
    licence: string;
    licenceUrl: string;
    sha256: string;
}
export interface ToolDownloadPlan {
    entry: ToolDownloadEntry;
    folder: string;
    token: string;
    expiresAt: number;
}
export const PlanToolDownload = appBinding<[string], ToolDownloadPlan>('PlanToolDownload');
export const StartToolDownload = appBinding<[ToolDownloadPlan], void>('StartToolDownload');
export const ReportLaunchStage = appBinding<[string], void>("ReportLaunchStage");
export const ReportPageFirstPaint = appBinding<[page: string, ms: number, source: string], void>("ReportPageFirstPaint");
// Setup backend bindings are explicit so static imports work with stale generated bindings.
export const GetModelChoices = appBinding<[], import('./setup-backend').ModelChoices>('GetModelChoices');
export const SetModelChoice = appBinding<[job: import('./setup-backend').ModelJob, id: string], void>('SetModelChoice');
export const PlanModelDownload = appBinding<[choices: Partial<Record<import('./setup-backend').ModelJob, string>>, sourceOverrides: Record<string, string>], import('./setup-backend').ModelDownloadPlan>('PlanModelDownload');
export const StartModelDownloads = appBinding<[plan: import('./setup-backend').ModelDownloadPlan], void>('StartModelDownloads');
export const PlanSetupModelDownload = appBinding<[choices: Partial<Record<import('./setup-backend').ModelJob, string>>, skipJobs: import('./setup-backend').ModelJob[]], import('./setup-backend').ModelDownloadPlan>('PlanSetupModelDownload');
export const DeferModelDownloads = appBinding<[plan: import('./setup-backend').ModelDownloadPlan], void>('DeferModelDownloads');
export const GetModelDownloads = appBinding<[], import('./setup-backend').ModelDownload[]>('GetModelDownloads');
export const RetryModelDownload = appBinding<[id: string], void>('RetryModelDownload');
export const RemoveModel = appBinding<[id: string], void>('RemoveModel');
export const GetModelEvaluation = appBinding<[], string>('GetModelEvaluation');
export const GetAgentProbe = appBinding<[], import('./setup-backend').SetupAgentProbe>('GetAgentProbe');
export const RefreshAgentProbe = appBinding<[], void>('RefreshAgentProbe');
export const SetSetupWelcomeVisible = appBinding<[visible: boolean], void>('SetSetupWelcomeVisible');
export const RequestAllPermissions = appBinding<[], void>('RequestAllPermissions');
export const GetSetupPermissions = appBinding<[], import('./setup-backend').SetupPermissions>('GetSetupPermissions');

export const ResumeModelDownload = appBinding<[id: string], void>('ResumeModelDownload');
export const GetModelNetworkState = appBinding<[], import('./setup-backend').ModelNetworkState>('GetModelNetworkState');

// Your agent (coming soon): Settings page bindings. Each needs an explicit export here (CLAUDE.md rule 14).
export type YourAgentPageData = main.YourAgentPage;
export type YourAgentReservation = main.YourAgentReservation;
export type YourAgentCheckResult = main.YourAgentCheckResult;
export type YourAgentReserveResult = main.YourAgentReserveResult;
export type YourAgentReservationResult = main.YourAgentReservationResult;
export const GetYourAgentPage = appBinding<[], main.YourAgentPage>('GetYourAgentPage');
export const GetYourAgentReservation = appBinding<[], main.YourAgentReservationResult>('GetYourAgentReservation');
export const CheckYourAgentName = appBinding<[name: string], main.YourAgentCheckResult>('CheckYourAgentName');
export const ReserveYourAgent = appBinding<[name: string, email: string], main.YourAgentReserveResult>('ReserveYourAgent');
export const ReleaseYourAgent = appBinding<[], main.YourAgentReserveResult>('ReleaseYourAgent');
export const VerifyYourAgentCode = appBinding<[code: string], main.YourAgentReserveResult>('VerifyYourAgentCode');
export const ResendYourAgentCode = appBinding<[], main.YourAgentReserveResult>('ResendYourAgentCode');
export const ChooseYourAgentPage = appBinding<[keep: boolean], main.YourAgentReserveResult>('ChooseYourAgentPage');
export const CancelYourAgentRequest = appBinding<[], main.YourAgentReserveResult>('CancelYourAgentRequest');

// First-run setup v2 (setup_window.go): the setup window, the menu bar mark and the finish.
export const SetSetupWindowMode = appBinding<[boolean], void>("SetSetupWindowMode");
export const SetMenuBarMarkVisible = appBinding<[boolean, boolean], void>("SetMenuBarMarkVisible");
export const PlaySetupFinish = appBinding<[import("../../wailsjs/go/models").main.SetupFinishRequest], import("../../wailsjs/go/models").main.SetupFinishResult>("PlaySetupFinish");
export const RevealMainWindow = appBinding<[], void>("RevealMainWindow");

export interface HoldCalendar { id: string; title: string; sourceId: string; sourceTitle: string; sourceType: string; supportsBusy: boolean; isDefault: boolean; meetingsAccount: boolean }
export interface HoldSettings { granted: boolean; offerHolds: boolean; calendarId: string; calendars: HoldCalendar[]; browserOnly?: string[] }
export const GetHoldSettings = appBinding<[], HoldSettings>("GetHoldSettings");
export const SetOfferHolds = appBinding<[boolean], void>("SetOfferHolds");
export const SetHoldCalendar = appBinding<[string], void>("SetHoldCalendar");

export interface HoldThing { title: string; sourceKind: string; sourceId: string }
export interface HoldProposal { existing?: HoldStatus; dismissed?: boolean; id: string; thing: HoldThing; calendarId: string; slots: FindTimeRange[] }
export interface HoldStatus { id: string; state: string; calendarId: string; title: string; start: string; end: string; writeAfter: string; removedAt: string; failureReason?: string }
export const ProposeHold = appBinding<[HoldThing, string, number], HoldProposal>("ProposeHold");
export const HoldTime = appBinding<[HoldProposal, FindTimeRange, string], HoldStatus>("HoldTime");
export const GetHoldStatus = appBinding<[string], HoldStatus>("GetHoldStatus");
export const UndoHold = appBinding<[string], void>("UndoHold");
export const RemoveHold = appBinding<[string], void>("RemoveHold");
export const UndoRemoveHold = appBinding<[string], void>("UndoRemoveHold");
export const DismissHoldOffer = appBinding<[HoldThing], string>("DismissHoldOffer");
export const UndoDismissHoldOffer = appBinding<[string], void>("UndoDismissHoldOffer");
export const SendMainChatFromTodayChip = appBinding<[string], import("../../wailsjs/go/models").main.MainChatTopicSendResult>("SendMainChatFromTodayChip");
export const SendMainChatWithAttachments = appBinding<[string, import("../../wailsjs/go/models").main.ImageData[], import("../../wailsjs/go/models").main.FileData[]], import("../../wailsjs/go/models").main.MainChatTopicSendResult>("SendMainChatWithAttachments");
export const PostMorningBriefWeekAnswerFromChip = appBinding<[string, string, string], void>("PostMorningBriefWeekAnswerFromChip");
export const AskMainChatFromTodayChip = appBinding<[string], import("../../wailsjs/go/models").main.MainChatTopicSendResult>("AskMainChatFromTodayChip");
export const RecordTodayAction = appBinding<[string, string, string, string, number], void>("RecordTodayAction");
export const RecordTodaySectionSeen = appBinding<[string, number], void>("RecordTodaySectionSeen");
export const RecordTasksAction = appBinding<[string, string, string, number], void>("RecordTasksAction");
export const RecordTasksSectionSeen = appBinding<[string, number], void>("RecordTasksSectionSeen");
