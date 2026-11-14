import { FailedSendContext } from '@/lib/failed-send-context';
import { quietRetry, reportChatPlaceFailure } from "@/lib/chat-failure";
import { openMainChatReplyDraft, openMainChatTask } from "@/lib/main-chat-side-panel";
import { useAIReady } from '@/hooks/useAIReady';
import { NoAILine } from '@/components/no-ai-line';
import { onBriefPlaybackProgress, prepareLatestBriefAudio, stopBriefPlayback, toggleBriefPlayback } from "@/lib/brief-audio";
import { Button } from "@/components/ui/button";
import { pageQueryCache } from '@/lib/query-cache';
import { RetainedPage } from '@/components/retained-page';
import { beginPageSwitch, markPageContent } from '@/lib/page-switch-timing';
import { LoadStreamItemsPage as ReadStreamItemsPage, revalidatePageQueries, usePageQueryRefresh } from '@/lib/page-queries';
import { usePagePrefetch } from '@/hooks/usePagePrefetch';
import { useIdlePagePrefetch } from '@/hooks/useIdlePagePrefetch';
import { setPrefetchStreaming } from '@/lib/idle-prefetch';
import { CHAT_FIRST_PAGE_SIZE } from '@/lib/chat-page-size';
import type { TodayBriefFocus } from "@/lib/main-chat-navigation";
import { useReadingWidth } from "@/hooks/use-reading-width";
import { lastActivityDate } from '@/components/timeline/activity-session-cache';
import { Suspense, useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import type { TFunction } from "i18next";
import {
    StartAgent,
    OpenChatDeliverable,
    ChatDeliverableInfo,
    ListChatDeliverableOpenWithApps,
    OpenChatDeliverableWithApp,
    RevealChatDeliverable,
    LoadUIState,
    SaveUIState,
    LoadAllProjects,
    LoadActiveProjectID,
    ListAvailableModels,
    EnsureTranscriptChatMessage,
    GetGitInfo,
    CaptureAndSaveWindowState,
    GetSlackStatus,
    GetFeishuStatus,
    AttachQuickChatContext,
    OpenTodayBriefConversation,
    OpenTodayBriefParagraph,
} from "../wailsjs/go/main/App";
import { loadStreamItemsPageWithOpenAnswerDelivery } from "@/lib/open-answer-delivery";
import {
    isMainChatSession,
    findMainChatSession,
    getMainChatProactive,
    isSideChatSession,
} from "@/lib/stream-helpers";
import { useMainChatReports } from "@/hooks/useMainChatReports";
import {
    RelaunchNotice,
    useScreenRecordingRelaunch,
} from "@/components/relaunch-notice";
import { upcomingApprovalQuestion } from "@/components/main-chat-report-card";
import { useMainChatSetup } from "@/hooks/useMainChatSetup";
import { onPrivacyReset } from "@/lib/privacy-reset";
import { MainChatProactiveCard } from "@/components/main-chat-proactive-card";
import { WhatIsoformSeesSheet } from "@/components/what-isoform-sees";
import { MainChatSetupCard } from "@/components/main-chat-setup-card";
import { AgentChoice } from "@/components/onboarding/components/AgentChoice";
// Import from the shim directly: it's the real module Vite aliases to, and its
// hand-written arg types (files as {path, description?}, no redundant `name`)
// stay correct even when the generated wailsjs bindings are stale.
import {
    GetPlaybooksState,
    PinTopicToSidebar,
    RecordChatDeliverables,
    SetMenuBarBriefPlayback,
    type MobileBriefActivityTarget,
} from "@/lib/wails-shim-app";
import { useSidebarTopics } from "@/hooks/useTodayTopics";
import { SIDEBAR_TOPICS_CHANGED, type TodayTopic } from "@/components/today/topics";
import type { DeliverableActions } from "@/components/stream-card";
import { completedChatDeliverables } from "@/lib/chat-deliverable-index";
import {
    convertFromStorage,
    createSession,
    fromStreamItemData,
    mergeAppendedItems,
    getExecutorType,
    DEFAULT_MODEL,
    logPersistError,
    STANDALONE_CHAT_PROJECT_ID,
} from "@/lib/stream-helpers";
import type { ExecutorGroup } from "@/lib/stream-helpers";

import {
    ArrowLeft,
    X,
} from "lucide-react";

import { Sidebar, type AppMode } from "@/components/sidebar";
import { MainChatPageColumn, MainChatStreamBody } from "@/components/main-chat/main-chat-stream-body";
import { useMainChatItemPanel } from "@/hooks/useMainChatItemPanel";
import { MainChatTopicBox } from "@/components/main-chat/main-chat-topic-view";
import { useMainChatView } from "@/hooks/useMainChatView";
import { ChatErrorPreview } from "@/components/chat-error-preview";
import { NewChatWelcome } from "@/components/new-chat-welcome";
import { Composer } from "@/components/composer";
import { ProjectThreadStatus } from "@/components/project-thread-status";

// import { PRButton } from '@/components/pr-button'; // TODO: re-enable after PR bug fix
import { LazyActivityPage, LazySettingsPage, useLazyPagesIdleWarm, LazyTerminalPanel } from "@/lib/lazy-pages";
import { SettingsLoadingShell } from "@/components/settings-loading-shell";
import type { ActivitySectionId } from "@/components/activity-page";
import { RetainedDialogContent } from "@/components/retained-dialog-content";
import { ActivityOpenChatContext } from "@/components/activity-ask-inline";
import { DevToolsContext } from "@/lib/dev-tools-context";
import { KnowledgePage } from "@/components/knowledge-page";
import { MemoryPage } from "@/components/memory-page";
import { PeoplePage, type PeopleSelection } from "@/components/people-page";
import { AutomationDetailPane } from "@/components/automations/AutomationDetailPane";
import { TodayPage } from "@/components/today/TodayPage";
import { CAPTURED_COUNTS_ANCHOR } from "@/components/settings/pages/captured-counts-card";
import type { TodayActionHandlers } from "@/components/today/types";
import { todayBriefCardRef } from "@/lib/today-chat-navigation";
import {
    Dialog,
    DialogDescription,
    DialogTitle,
    DialogClose,
} from "@/components/ui/dialog";
import type {
    SettingsOpenOptions,
    SettingsTopic,
} from "@/components/settings-page";
import { isSettingsPageId, readSettingsOpenEvent } from "@/components/settings/settings-registry";
import { debugDetailOwnsEscape } from "@/components/debug-master-detail";
import { PlanReviewCard } from "@/components/plan-review-card";
import { PermissionQuestionCard } from "@/components/permission-question-card";
import { CompactionBanner } from "@/components/compaction-banner";
import { SearchDialog, type SearchDialogActions } from "@/components/search-dialog";
import { waitInWords } from "@/lib/brief-wait";
import { TtsDownloadNotifications } from "@/components/tts-download-notifications";
import { toast } from "sonner";
import {
    EventsOn,
    LogInfo,
    LogWarning,
} from "../wailsjs/runtime/runtime";

import { useComposerCapabilities } from "@/hooks/useComposerCapabilities";
import { useSettings } from "@/hooks/useSettings";
import { comboToDisplayString, useKeybindings } from "@/hooks/useKeybindings";
import { useChatMessageJump } from "@/hooks/useChatMessageJump";
import { useMainChatWaitingCount } from "@/hooks/useMainChatWaitingCount";
import { usePageWindowTitle } from "@/hooks/usePageWindowTitle";
import { useSidebarResize } from "@/hooks/useSidebarResize";
import { useTerminal } from "@/hooks/useTerminal";
import { useProductTelemetry } from "@/hooks/useProductTelemetry";
import { useEnvironmentSwitch } from "@/hooks/useEnvironmentSwitch";
import { useDevToolsUnlock } from "@/hooks/useDevToolsUnlock";
import { useSidebarGroups } from "@/hooks/useSidebarGroups";
import { useInsightNotification } from "@/hooks/useInsightNotification";
import { useCronJobs } from "@/hooks/useCronJobs";
import type { CronJobState } from "@/hooks/useCronJobs";
import { useUpcoming } from "@/hooks/useUpcoming";
import { recordTasksAction } from "@/lib/tasks-telemetry";
import { offerShowsNow } from "@/components/playbooks/playbook-lists";
import { TasksPage, type TasksFocus } from "@/components/tasks/TasksPage";
// import { usePRInfo } from '@/hooks/usePRInfo'; // TODO: re-enable after PR bug fix
import { useProjects } from "@/hooks/useProjects";
import { useAgentStreaming } from "@/hooks/useAgentStreaming";
import { useComposerInput } from "@/hooks/useComposerInput";
import { openTodayMainChat, openTodayBriefChat, TodayBriefRequestGate, type TodayBriefOpened, type TodayChatContextRef, type TodayChatStartOptions } from "@/lib/today-chat-navigation";
import { useComposerDockSlide } from "@/hooks/useComposerDockSlide";
import { useUserProfile } from "@/hooks/useUserProfile";
import { useVoiceInput } from "@/hooks/useVoiceInput";
import { ComposerVoiceContext, type ComposerVoice } from "@/lib/composer-voice-context";
import { useTheme } from "@/hooks/useTheme";
import type {
    OnboardingProviderId,
    OnboardingState,
    StreamItem,
    UpcomingApproval,
} from "@/types";
import { OnboardingScreen } from "@/components/onboarding-screen";
import { AppToaster } from "@/components/app-toaster";
import { loadOnboardingStateRaw, onboardingFinished } from "@/lib/onboarding-gate";
import { ConfirmDialog } from "@/components/confirm-dialog";
import { useHandoffNotification } from "@/hooks/useHandoffNotification";
import { useFirstDay } from "@/hooks/useFirstDay";
import { useTasksNeedsYouCount } from "@/hooks/useTasksNeedsYou";
import { useTtsAnnounce } from "@/hooks/useTtsAnnounce";

const ONBOARDING_STORAGE_KEY = "yansu.onboarding.state";
const ONBOARDING_STATE_VERSION = 2;

// Labels for the "Back to X" button in the activity dialog live in
// `app.activityBack.*` translations.
const KNOWN_PROVIDER_IDS: OnboardingProviderId[] = [
    "slack",
    "discord",
    "telegram",
    "wechat",
];

function normalizeProviderList(value: unknown): OnboardingProviderId[] {
    if (!Array.isArray(value)) return [];
    const out: OnboardingProviderId[] = [];
    for (const item of value) {
        if (
            typeof item === "string" &&
            KNOWN_PROVIDER_IDS.includes(item as OnboardingProviderId) &&
            !out.includes(item as OnboardingProviderId)
        ) {
            out.push(item as OnboardingProviderId);
        }
    }
    return out;
}

function sanitizeOnboardingText(value: unknown): string {
    return typeof value === "string" ? value : "";
}

function sanitizeOnboardingStateValue(value: unknown): OnboardingState {
    const base: OnboardingState = {
        version: ONBOARDING_STATE_VERSION,
        completed: false,
        neverShowAgain: false,
        permissionsComplete: false,
        aboutYou: {
            busyWith: "",
            repeatedWeekly: "",
            wishHandled: "",
        },
        selectedProviders: ["slack"],
        completedProviders: [],
        providerChannelSelections: {},
    };

    if (!value || typeof value !== "object") {
        return base;
    }

    const candidate = value as Record<string, unknown>;
    const sanitizedSelectedProviders = normalizeProviderList(
        candidate.selectedProviders,
    );
    const selectedProviders =
        sanitizedSelectedProviders.length > 0
            ? sanitizedSelectedProviders
            : base.selectedProviders;
    const completedProviders = normalizeProviderList(
        candidate.completedProviders,
    ).filter((id) => selectedProviders.includes(id));

    const rawAboutYou = candidate.aboutYou;
    const aboutYouCandidate =
        rawAboutYou && typeof rawAboutYou === "object"
            ? (rawAboutYou as Record<string, unknown>)
            : {};

    const rawSelections = candidate.providerChannelSelections;
    const providerChannelSelections: Record<string, string[]> = {};
    if (rawSelections && typeof rawSelections === "object") {
        for (const [provider, channels] of Object.entries(
            rawSelections as Record<string, unknown>,
        )) {
            if (
                KNOWN_PROVIDER_IDS.includes(provider as OnboardingProviderId) &&
                Array.isArray(channels)
            ) {
                providerChannelSelections[provider] = channels.filter(
                    (item): item is string => typeof item === "string",
                );
            }
        }
    }

    return {
        ...base,
        completed: candidate.completed === true,
        neverShowAgain: candidate.neverShowAgain === true,
        permissionsComplete: candidate.permissionsComplete === true,
        aboutYou: {
            busyWith: sanitizeOnboardingText(aboutYouCandidate.busyWith),
            repeatedWeekly: sanitizeOnboardingText(
                aboutYouCandidate.repeatedWeekly,
            ),
            wishHandled: sanitizeOnboardingText(aboutYouCandidate.wishHandled),
        },
        selectedProviders,
        completedProviders,
        providerChannelSelections,
    };
}

function parseOnboardingState(raw: string): OnboardingState {
    if (!raw) return sanitizeOnboardingStateValue(null);
    try {
        return sanitizeOnboardingStateValue(JSON.parse(raw));
    } catch {
        return sanitizeOnboardingStateValue(null);
    }
}

function isCJKChar(char: string): boolean {
    if (!char) {
        return false;
    }

    const code = char.charCodeAt(0);
    return (
        (code >= 0x3400 && code <= 0x4dbf) ||
        (code >= 0x4e00 && code <= 0x9fff) ||
        (code >= 0xf900 && code <= 0xfaff)
    );
}

function appendVoiceTranscriptToPrompt(
    base: string,
    transcript: string,
): string {
    const left = base.trimEnd();
    const right = transcript.trim();
    if (!left) {
        return right;
    }
    if (!right) {
        return left;
    }

    const trailing = left[left.length - 1];
    const leading = right[0];
    const shouldInsertSpace =
        !/\s/.test(trailing) &&
        !/[\s,.;:!?，。！？、]/.test(leading) &&
        !isCJKChar(trailing) &&
        !isCJKChar(leading);

    return shouldInsertSpace ? `${left} ${right}` : `${left}${right}`;
}

function EnvironmentSwitchDialog({
    show,
    currentEnv,
    loading,
    error,
    onConfirm,
    onCancel,
}: {
    show: boolean;
    currentEnv: string;
    loading: boolean;
    error: string | null;
    onConfirm: () => void;
    onCancel: () => void;
}) {
    if (!show) return null;
    const targetEnv = currentEnv === "production" ? "Staging" : "Production";
    return (
        <div className="fixed inset-0 z-[9999] flex items-center justify-center bg-code-bg/50">
            <div className="w-[360px] rounded-xl border border-border bg-panel p-6 shadow-[var(--shadow-card)] animate-fade-in">
                <h3 className="text-sm font-semibold text-ink mb-2">
                    Switch to {targetEnv}?
                </h3>
                <p className="text-xs text-muted mb-4 leading-relaxed">
                    You are currently on{" "}
                    <span className="font-medium text-ink">{currentEnv}</span>.
                    Switching to{" "}
                    <span className="font-medium text-ink">
                        {targetEnv.toLowerCase()}
                    </span>{" "}
                    will log you out and require re-authentication against the{" "}
                    {targetEnv.toLowerCase()} server.
                </p>
                {error && (
                    <p className="text-xs text-error mb-3 p-2 rounded-md bg-error-bg border border-error-border">
                        Failed to switch: {error}
                    </p>
                )}
                <div className="flex justify-end gap-2">
                    <button
                        className="px-3 py-1.5 text-xs rounded-lg bg-border text-ink hover:bg-border/80 transition-colors"
                        onClick={onCancel}
                        disabled={loading}
                    >
                        Cancel
                    </button>
                    <button
                        className="px-3 py-1.5 text-xs rounded-lg patina-verdigris bg-solid-btn-bg text-solid-btn-fg shadow-[var(--shadow-solid-btn)] hover:opacity-90 transition-colors disabled:opacity-50"
                        onClick={onConfirm}
                        disabled={loading}
                    >
                        {loading ? "Switching..." : `Switch to ${targetEnv}`}
                    </button>
                </div>
            </div>
        </div>
    );
}

/** The question the approval card asks. The Upcoming tools that start
 *  unattended work ask in words and name the item; any other tool shows its
 *  name. */
function permissionQuestionFor(
    toolName: string,
    upcoming: UpcomingApproval | undefined,
    t: TFunction<"app">,
    tChat: TFunction<"chat">,
): string {
    return upcomingApprovalQuestion(toolName, upcoming, t, tChat) ?? toolName;
}

export default function App() {
    const mainChatItemPanel = useMainChatItemPanel("chat");
    // Dev-only escape hatch: visiting `#dev=chat-errors` renders a preview
    // gallery of all ChatErrorCard variants instead of the regular app.
    // Lets us verify error copy/styling without provoking real upstream
    // failures. Safe to leave in — it is a no-op without the fragment.
    if (
        typeof window !== "undefined" &&
        window.location.hash.includes("dev=chat-errors")
    ) {
        return <ChatErrorPreview />;
    }
    const { t, i18n } = useTranslation("app");
    const { t: tChat } = useTranslation("chat");
    const { t: tCommon } = useTranslation("common");
    const { t: tToday } = useTranslation("today");
    // ── Shared state ──
    const [model, setModel] = useState<string>(DEFAULT_MODEL);
    const [executorGroups, setExecutorGroups] = useState<ExecutorGroup[]>([]);
    const [error, setError] = useState<string | null>(null);
    const layoutRef = useRef<HTMLDivElement>(null);

    // ── Hook composition ──
    const { profile: userProfile } = useUserProfile();
    // Product telemetry uses the backend's anonymous install identity and
    // consent gate. No browser SDK inspects the page or the local profile.
    useProductTelemetry();
    const envSwitch = useEnvironmentSwitch();
    // Developer tools start on in staging and dev builds; the hidden
    // shortcut turns them on or off from there.
    const devToolsUnlocked = useDevToolsUnlock(
        envSwitch.isStaging || envSwitch.isDev,
    );
    const settings = useSettings();
    const { terminalTheme } = useTheme();
    const keybindings = useKeybindings();
    const sidebar = useSidebarResize({ layoutRef });

    const projects = useProjects({ model, setModel, setError });

    useEffect(() => {
        const normalizeLanguage = (value: string | undefined) =>
            value?.startsWith("zh") ? "zh-CN" : "en";
        const persistLanguage = (value: string | undefined) => {
            void SaveUIState("ui.language", normalizeLanguage(value)).catch(
                logPersistError,
            );
        };

        persistLanguage(i18n.resolvedLanguage ?? i18n.language);
        i18n.on("languageChanged", persistLanguage);
        return () => {
            i18n.off("languageChanged", persistLanguage);
        };
    }, [i18n]);

    // Stable refs for projects state — used inside Wails event handlers to
    // avoid re-subscribing EventsOn on every projects.projects change.
    const projectsRef = useRef(projects.projects);
    projectsRef.current = projects.projects;
    const updateProjectRef = useRef(projects.updateProject);
    updateProjectRef.current = projects.updateProject;
    const activeProjectRef = useRef(projects.activeProject);
    activeProjectRef.current = projects.activeProject;
    const activeWorkspaceRef = useRef(projects.activeWorkspace);
    activeWorkspaceRef.current = projects.activeWorkspace;
    // Handoff proposals queued while the window is hidden (AFK). Drained on
    // visibilitychange/focus into a single digest toast or normal toast.
    const pendingHandoffNotificationsRef = useRef<
        { proposalId: string; chatSessionId: string; label: string }[]
    >([]);

    const sidebarGroupsHook = useSidebarGroups();

    const streaming = useAgentStreaming({
        setProjects: projects.setProjects,
        activeSessionLocalId: projects.activeSession?.localId ?? null,
        onSessionUnread: projects.markSessionUnread,
    });

    const [onboardingState, setOnboardingState] = useState<OnboardingState>(
        sanitizeOnboardingStateValue(null),
    );
    const onboardingStateRef = useRef(onboardingState);
    const [onboardingLoading, setOnboardingLoading] = useState(true);
    const [skipOnboardingSession, setSkipOnboardingSession] = useState(false);
    const [connectedProviders, setConnectedProviders] = useState<{
        slack?: string;
        feishu?: string;
    }>({});
    // First-login welcome: only shown when the user has never chatted before.
    const [isFirstLogin, setIsFirstLogin] = useState(false);
    const firstLoginLoadedRef = useRef(false);

    // Check if any messenger provider is already connected
    useEffect(() => {
        Promise.all([
            GetSlackStatus().catch(() => ({ connected: false, team_name: "" })),
            GetFeishuStatus("").catch(() => ({ connected: false })),
        ]).then(([slack, feishu]) => {
            const providers: {
                slack?: string;
                feishu?: string;
            } = {};
            if (slack.connected)
                providers.slack = (slack as any).team_name || "Slack";
            if (feishu.connected) providers.feishu = "Feishu";
            setConnectedProviders(providers);
        });
    }, []);

    // Refs for insight notification callbacks — setters are defined later but
    // stable, so we assign them after useState and the arrow closures always
    // read the latest ref value.
    const openKnowledgeRef = useRef<() => void>(() => {});
    // How you do things steps back first (history, edit, the open entry).
    const knowledgeEscapeRef = useRef<(() => boolean) | null>(null);
    const openMemoryRef = useRef<() => void>(() => {});
    const openHandoffRef = useRef<(id: string) => void>(() => {});
    const openMainChatRef = useRef<() => void>(() => {});
    const openChatMessageRef = useRef<(sessionId: string, itemId: string) => void>(() => {});
    const openMainChatItemRef = useRef<(id: string) => void>(() => {});
    // Set once handleSetAppMode exists: a clicked brief or suggestion toast
    // opens Today (engagementClickData in wails-gui/engagement_app.go).
    const openTodayRef = useRef<(briefId?: string) => void>(() => {});
    const [todayBriefRequest, setTodayBriefRequest] = useState<{ briefId?: string; focus?: TodayBriefFocus; nonce: number } | null>(null);
    // Set alongside openTodayRef: a clicked missed-schedule toast opens the
    // schedule on Upcoming.
    const openUpcomingRef = useRef<(jobId: string) => void>(() => {});
    // Set alongside openTodayRef: a clicked playbook suggestion or result
    // opens that playbook on the Playbooks page.
    const openPlaybookRef = useRef<(id: string) => void>(() => {});
    // Set with the Activity open helpers: a clicked reminder opens Activity
    // on its moment's day.
    const openActivityDayRef = useRef<(day: string) => void>(() => {});
    useInsightNotification({
        onOpenKnowledge: (id: string) => {
            setPendingInsightKnowledgeId(id);
            openKnowledgeRef.current();
        },
        onOpenMemory: (id: string) => {
            setPendingInsightMemoryId(id);
            openMemoryRef.current();
        },
    });
    // The Tasks count is exactly what Tasks › Needs you lists.
    const tasksNeedsYouCount = useTasksNeedsYouCount();
    const firstDay = useFirstDay();
    useHandoffNotification();
    useTtsAnnounce();

    const composer = useComposerInput({
        activeProject: projects.activeProject,
        activeWorkspace: projects.activeWorkspace,
        activeSession: projects.activeSession,
        updateProject: projects.updateProject,
        status: streaming.status,
        model,
        setError,
        awaitingPlanApproval:
            projects.activeSession?.awaitingPlanApproval ?? false,
        hasPendingPermission:
            (projects.activeSession?.permissionOptions?.length ?? 0) > 0,
        onDismissPlanReview: projects.handleDismissPlanReview,
        onRespondToPermission: projects.handleRespondToPermission,
        sendPromptCombo: keybindings.getCombo("send-prompt")!,
        connectedProviders,
        userName: userProfile?.display_name || userProfile?.name,
        isFirstLogin,
    });
    const aiReady = useAIReady();
    const failedSendActions = useMemo(
        () => ({ retry: composer.retryFailedSend, edit: composer.editFailedSend, resendLast: composer.resendLastSend, resendItemId: composer.resendItemId, aiReady }),
        [composer.retryFailedSend, composer.editFailedSend, composer.resendLastSend, composer.resendItemId, aiReady],
    );

    // Drive the composer's welcome -> chat slide animation. `isEmptySession`
    // is the same condition used below to pick the layout; keep the logic
    // in one place.
    const activeSessionForLayout = projects.activeSession;
    const isSessionHydrating =
        !!activeSessionForLayout &&
        !activeSessionForLayout.itemsLoaded &&
        activeSessionForLayout.streamItemCount > 0;
    const isEmptySession =
        !isSessionHydrating &&
        (activeSessionForLayout?.items?.length ?? 0) === 0 &&
        !activeSessionForLayout?.streaming;
    const composerDockSlide = useComposerDockSlide(isEmptySession);

    const voicePromptBaseRef = useRef<string | null>(null);
    // Which box the mic is filling: the Chat page's live transcript stays out
    // of Today's panel box, which takes the words when recording stops.
    const voiceOwnerRef = useRef<"composer" | "panel">("composer");
    const handleVoiceRecordingStart = useCallback(() => {
        if (voiceOwnerRef.current === "panel") return;
        voicePromptBaseRef.current = composer.prompt;
    }, [composer.prompt]);

    const handleVoiceTranscriptUpdate = useCallback(
        (transcript: string) => {
            if (voiceOwnerRef.current === "panel") return;
            if (voicePromptBaseRef.current === null) {
                voicePromptBaseRef.current = composer.prompt;
            }

            composer.setPrompt(
                appendVoiceTranscriptToPrompt(
                    voicePromptBaseRef.current ?? "",
                    transcript,
                ),
            );
        },
        [composer.prompt, composer.setPrompt],
    );

    const handleVoiceRecordingCancel = useCallback(() => {
        if (voiceOwnerRef.current === "panel") return;
        if (voicePromptBaseRef.current !== null) {
            composer.setPrompt(voicePromptBaseRef.current);
        }
        voicePromptBaseRef.current = null;
    }, [composer.setPrompt]);

    const voice = useVoiceInput({
        onRecordingStart: handleVoiceRecordingStart,
        onTranscriptUpdate: handleVoiceTranscriptUpdate,
        onRecordingCancel: handleVoiceRecordingCancel,
    });

    const lastVoiceErrorRef = useRef<string | null>(null);

    useEffect(() => {
        if (!voice.error || voice.error === lastVoiceErrorRef.current) {
            return;
        }

        lastVoiceErrorRef.current = voice.error;
        toast.error(voice.error);
    }, [voice.error]);

    const handleOpenVoiceSettings = useCallback(async () => {
        try {
            await voice.openMicrophoneSettings();
        } catch (err) {
            const message = err instanceof Error ? err.message : String(err);
            toast.error(`Failed to open microphone settings: ${message}`);
        }
    }, [voice.openMicrophoneSettings]);

    const voiceStartRecording = voice.startRecording;
    const startComposerVoice = useCallback(() => {
        voiceOwnerRef.current = "composer";
        return voiceStartRecording();
    }, [voiceStartRecording]);
    const panelVoice = useMemo<ComposerVoice>(() => ({
        state: voice.state,
        error: voice.error,
        needsPermission: voice.needsMicrophonePermission,
        start: () => {
            voiceOwnerRef.current = "panel";
            return voiceStartRecording();
        },
        stop: voice.stopAndTranscribe,
        openSettings: handleOpenVoiceSettings,
    }), [voice.state, voice.error, voice.needsMicrophonePermission, voiceStartRecording, voice.stopAndTranscribe, handleOpenVoiceSettings]);

    const activeWorkDir =
        projects.activeWorkspace &&
        !projects.activeWorkspace.isDefault &&
        projects.activeWorkspace.worktreePath
            ? projects.activeWorkspace.worktreePath
            : projects.activeProject?.path;

    const terminal = useTerminal({
        activeProjectId: projects.activeProjectId,
        activeWorkspaceId: projects.activeWorkspace?.id ?? null,
        activeWorkDir,
        layoutRef,
        terminalFg: terminalTheme.foreground,
        terminalBg: terminalTheme.background,
    });

    // Core close-session logic (without linked-automation check)
    const doCloseSession = useCallback(
        (projectId: string, sessionId: string) => {
            streaming.cleanupSession(sessionId);
            composer.forgetFailedSends?.(sessionId);
            projects.handleCloseSession(projectId, sessionId);
            sidebarGroupsHook.removeItemsByRef("chat", sessionId);
        },
        [
            streaming.cleanupSession,
            composer.forgetFailedSends,
            projects.handleCloseSession,
            sidebarGroupsHook.removeItemsByRef,
        ],
    );

    // Deep-link target for the next Settings open. Undefined opens Overview;
    // legacy ids (account, subscription, appearance, slack, ...) are resolved
    // by the settings registry.
    const [settingsInitialTopic, setSettingsInitialTopic] = useState<
        SettingsTopic | undefined
    >(undefined);
    // How that deep link opens the page: { advanced: true } expands its
    // Advanced section, { anchor } scrolls to a marked element. A new object
    // per call, so asking again for the page already open still navigates.
    const [settingsInitialOptions, setSettingsInitialOptions] =
        useState<SettingsOpenOptions>({});

    const openSettings = useCallback(
        (topic?: SettingsTopic, options?: SettingsOpenOptions) => {
            setSettingsInitialTopic(topic);
            setSettingsInitialOptions({ ...options });
            settings.setSettingsOpen(true);
        },
        [
            setSettingsInitialTopic,
            setSettingsInitialOptions,
            settings.setSettingsOpen,
        ],
    );

    // Tool capabilities surfaced under the welcome-screen composer.
    const {
        capabilities: composerCapabilities,
        refresh: refreshComposerCapabilities,
    } = useComposerCapabilities();

    // ── Activity page ──
    // Activity is a main view (appMode === "activity"), not an overlay. The
    // state below is the open request the page mounts with; activityNonce
    // bumps on every open so a fresh request remounts the page even when it
    // was already showing (same remount the old dialog got on reopen).
    const [activityNonce, setActivityNonce] = useState(0);
    // The picked panel section, remembered for the app session while the
    // page unmounts across main-view switches.
    const [activitySection, setActivitySection] = useState<ActivitySectionId>(
        "yourDay",
    );
    const [activityNoticedFocus, setActivityNoticedFocus] = useState<MobileBriefActivityTarget | null>(null);
    // A thread the Activity page's "What you worked on" opens and scrolls to
    // (an "Open thread" card target).
    const [activityInitialThreadId, setActivityInitialThreadId] = useState<string | null>(null);
    const [activityInitialSessionId, setActivityInitialSessionId] = useState<
        string | null
    >(null);
    // The day the Activity page opens on (YYYY-MM-DD). The old Calendar
    // callers land here now; they always meant "today".
    const [activityInitialDate, setActivityInitialDate] = useState<
        string | null
    >(null);
    // When set alongside activityInitialSessionId, the Activity page jumps
    // to the snapshot closest to this timestamp after loading the detail.
    // Used by the Who you know page when the user clicks an Activity sessions
    // row to land on the actual frame where the person was last seen
    // rather than the session's default first snapshot.
    const [activityInitialEventTimestamp, setActivityInitialEventTimestamp] =
        useState<number | null>(null);
    // When set, the Activity page highlights every chat message whose
    // sender name contains this string. People → Activity jump uses it
    // so the user lands on the exact message that drove the jump.
    const [activityHighlightedSenderName, setActivityHighlightedSenderName] =
        useState<string | null>(null);
    // When non-null, the Activity page was opened by a session-jump from
    // another surface (memory / knowledge / pipeline). Drives the
    // "Back to X" button at the top-left of the page so the user has an
    // explicit affordance to return to what the jump came from.
    const [activityOpenedFrom, setActivityOpenedFrom] = useState<
        "memory" | "knowledge" | "pipeline" | "people" | null
    >(null);
    // When set, the Activity page received a "used-frames" highlight
    // payload from the Activity Pipeline settings page. Each entry's
    // (sessionId, snapshotId) is rendered as a soft tick on the snapshot
    // timeline + a badge on the currently-visible snapshot when it matches.
    // pipelineRunId labels the badge so the user knows which run drove it.
    const [activityHighlightedFrames, setActivityHighlightedFrames] = useState<
        {
            kind: string;
            sessionId: string;
            snapshotId: number;
            promptText?: string;
            skipReason?: string;
            ocrConfidence?: number;
            parsedSiblingCount?: number;
            browserUrl?: string;
            dedupChainLen?: number;
        }[]
    >([]);
    const [
        activityHighlightedPipelineRunId,
        setActivityHighlightedPipelineRunId,
    ] = useState<number | null>(null);
    // Restricts the Activity sidebar to a specific set of session IDs — used
    // when jumping from a pipeline run so the operator sees ONLY the merged
    // group's sessions (including ones that contributed 0 frames) instead of
    // every session within the current date filter.
    const [activityRestrictToSessionIds, setActivityRestrictToSessionIds] =
        useState<string[]>([]);

    // Reset every piece of state a previous Activity session might have left
    // behind. Pipeline jumps stash highlight + restrict state; without this
    // any subsequent non-pipeline entry (sidebar, top-bar menu, command
    // palette) would inherit it and silently filter the sidebar to last
    // jump's triage group.
    const resetActivityState = useCallback(() => {
        setActivityInitialSessionId(null);
        setActivityInitialDate(null);
        setActivityNoticedFocus(null);
        setActivityInitialThreadId(null);
        setActivityInitialEventTimestamp(null);
        setActivityHighlightedSenderName(null);
        setActivityOpenedFrom(null);
        setActivityHighlightedFrames([]);
        setActivityHighlightedPipelineRunId(null);
        setActivityRestrictToSessionIds([]);
    }, []);

    // The open helpers live below handleSetAppMode: a page open is a mode
    // switch, and the helpers need it.

    // Launches open Today, except legacy Handoffs state migrates to Tasks.
    // About you, How you do things and Who you know are main views, opened
    // from the sidebar's You rows (sidebar handoff 1.0.0, section 2).
    const [appMode, setAppMode] = useState<AppMode>(() => {
        if (localStorage.getItem("yansu.appMode") === "handoffs") {
            return "tasks";
        }
        return "today";
    });
    const appModeRef = useRef(appMode);
    appModeRef.current = appMode;
    // Set once handleSetAppMode exists (below); the You page setters use it.
    const handleSetAppModeRef = useRef<(mode: AppMode) => void>(() => {});
    // Where closing a You page returns: the last view that was not one.
    const youPageReturnRef = useRef<AppMode>("today");
    const youPageSetter = useCallback((mode: AppMode) => (open: boolean) => {
        if (open) handleSetAppModeRef.current(mode);
        else if (appModeRef.current === mode) handleSetAppModeRef.current(youPageReturnRef.current);
    }, []);

    // ── How you do things ──
    const knowledgeOpen = appMode === "knowledge";
    const setKnowledgeOpen = useMemo(() => youPageSetter("knowledge"), [youPageSetter]);
    const [pendingInsightKnowledgeId, setPendingInsightKnowledgeId] = useState<
        string | null
    >(null);
    openKnowledgeRef.current = () => setKnowledgeOpen(true);

    // ── About you ──
    const memoryOpen = appMode === "memory";
    const setMemoryOpen = useMemo(() => youPageSetter("memory"), [youPageSetter]);
    // About you steps back first (an open editor, the open belief).
    const memoryEscapeRef = useRef<(() => boolean) | null>(null);
    const [pendingInsightMemoryId, setPendingInsightMemoryId] = useState<
        string | null
    >(null);
    openMemoryRef.current = () => setMemoryOpen(true);

    // An entry opened from Search, a notification or a report opens once:
    // closing the page forgets it, so the next plain open shows the list.
    // A page kept mounted behind Activity keeps its own selection.
    useEffect(() => {
        if (!knowledgeOpen) setPendingInsightKnowledgeId(null);
    }, [knowledgeOpen]);
    useEffect(() => {
        if (!memoryOpen) setPendingInsightMemoryId(null);
    }, [memoryOpen]);

    // ── Who you know ──
    const peopleOpen = appMode === "people";
    const setPeopleOpen = useMemo(() => youPageSetter("people"), [youPageSetter]);
    // "self" when People opens from Settings > Learning and memory > Your
    // profile: the page selects your own card (or offers to name you).
    // { personId } when Search opens a person.
    const [peopleInitialSelection, setPeopleInitialSelection] = useState<
        PeopleSelection | undefined
    >(undefined);
    const closePeople = useCallback(() => {
        setPeopleOpen(false);
        setPeopleInitialSelection(undefined);
    }, []);
    // People steps back first (clears its search), then the dialog closes.
    const peopleEscapeRef = useRef<(() => boolean) | null>(null);
    // The You pages were dialogs; as main pages, Escape still steps back
    // inside them (an open entry, an edit, history). It never leaves the page.
    useEffect(() => {
        const onKey = (event: KeyboardEvent) => {
            if (event.key !== "Escape" || event.defaultPrevented) return;
            const escape = appModeRef.current === "memory" ? memoryEscapeRef
                : appModeRef.current === "knowledge" ? knowledgeEscapeRef
                    : appModeRef.current === "people" ? peopleEscapeRef : null;
            if (!escape?.current) return;
            if (document.querySelector('[role="dialog"][data-state="open"], [role="menu"][data-state="open"], [role="listbox"][data-state="open"]')) return;
            if (escape.current()) event.preventDefault();
        };
        document.addEventListener("keydown", onKey);
        return () => document.removeEventListener("keydown", onKey);
    }, []);

    // Route OS notification clicks for insight kinds (knowledge / memory) to the
    // corresponding page with the entry pre-selected.
    useEffect(() => {
        const unsub = EventsOn("notification:clicked", (clickData: string) => {
            if (!clickData) return;
            const idx = clickData.indexOf(":");
            if (idx < 0) return;
            const kind = clickData.slice(0, idx);
            const id = clickData.slice(idx + 1);
            // Tasks' notification rows (E02, E03): which route, never the item.
            if (kind === "automation") recordTasksAction("E03", "open", "opened");
            else if (kind === "handoff" || kind === "upcoming" || kind === "playbook" || kind === "activity-day") recordTasksAction("E02", "open", "opened");
            if (kind === "knowledge") {
                setPendingInsightKnowledgeId(id);
                setKnowledgeOpen(true);
            } else if (kind === "memory") {
                setPendingInsightMemoryId(id);
                setMemoryOpen(true);
            } else if (kind === "handoff") {
                openHandoffRef.current(id);
            } else if (kind === "main-chat") {
                openMainChatRef.current();
            } else if (kind === "chat-message") {
                const targetSeparator = id.indexOf(":");
                if (targetSeparator > 0) {
                    openChatMessageRef.current(id.slice(0, targetSeparator), id.slice(targetSeparator + 1));
                }
            } else if (kind === "main-chat-item") {
                openMainChatItemRef.current(id);
            } else if (kind === "transcript") {
                handleClickGroupItemRef.current?.({
                    itemType: "chat",
                    refId: id,
                });
            } else if (kind === "automation") {
                handleClickGroupItemRef.current?.({
                    itemType: "chat",
                    refId: id,
                });
            } else if (kind === "upcoming") {
                openUpcomingRef.current(id);
            } else if (kind === "today") {
                openTodayRef.current(id.startsWith("brief:") ? id.slice("brief:".length) : undefined);
            } else if (kind === "playbook") {
                openPlaybookRef.current(id);
            } else if (kind === "activity-day" && /^\d{4}-\d{2}-\d{2}$/.test(id)) {
                openActivityDayRef.current(id);
            } else if (kind === "settings") {
                openSettings(isSettingsPageId(id) ? id : undefined);
            }
        });
        return unsub;
    }, [openSettings]);

    // ── Automations overlay ──
    // Non-null when an automation's detail is shown in the right main content
    // column (mutually exclusive with the chat view). Mirrors how the active
    // chat session drives the main panel.
    const [automationsSelectedJobId, setAutomationsSelectedJobId] = useState<
        string | null
    >(null);
    const cronJobs = useCronJobs();

    usePageWindowTitle(appMode, projects.activeSession);
    const mainChatWaitingCount = useMainChatWaitingCount();
    // Chat's copper dot: an unread reply in the main chat (a question waiting
    // there also counts on Tasks, and shows the dot through the count).
    const mainChatUnread = useMemo(() => {
        const standalone = projects.projects.find((p) => p.id === STANDALONE_CHAT_PROJECT_ID);
        return Boolean(standalone?.workspaces.some((w) => w.sessions.some((session) => isMainChatSession(session) && projects.unreadSessionIds.has(session.localId))));
    }, [projects.projects, projects.unreadSessionIds]);
    // Tasks › Scheduled (was Upcoming), loaded while Tasks is open.
    const upcoming = useUpcoming(appMode === "tasks");
    // True while viewing a chat that was opened from the Handoffs page, so the
    // main top bar can show a "back to Handoffs" button. Cleared on any explicit
    // app-mode switch (see handleSetAppMode).
    const [chatBackToHandoffs, setChatBackToHandoffs] = useState(false);

    // Session deletion with linked automation confirmation
    const [pendingDeleteSession, setPendingDeleteSession] = useState<{
        projectId: string;
        sessionId: string;
        automationNames: string[];
    } | null>(null);

    const handleCloseSession = useCallback(
        (projectId: string, sessionId: string) => {
            const automationNames = cronJobs.jobs
                .filter((j) => j.sourceSessionId === sessionId)
                .map((j) => j.name || "Untitled scheduled job");

            if (automationNames.length > 0) {
                setPendingDeleteSession({
                    projectId,
                    sessionId,
                    automationNames,
                });
            } else {
                doCloseSession(projectId, sessionId);
            }
        },
        [cronJobs.jobs, doCloseSession],
    );

    // If selected automation is deleted while panel is open, clear selection.
    useEffect(() => {
        if (
            automationsSelectedJobId &&
            !cronJobs.jobs.find((j) => j.id === automationsSelectedJobId)
        ) {
            setAutomationsSelectedJobId(null);
        }
    }, [cronJobs.jobs, automationsSelectedJobId]);

    const resolveAutomationSourceSessionLabel = useCallback(
        (job: CronJobState) => {
            if (!job.sourceSessionId) return undefined;
            for (const p of projects.projects) {
                for (const w of p.workspaces) {
                    const s = w.sessions.find(
                        (s: { localId: string }) =>
                            s.localId === job.sourceSessionId,
                    );
                    if (s) return s.label || "Source Chat";
                }
            }
            return undefined;
        },
        [projects.projects],
    );

    const handleNavigateToAutomationSourceSession = useCallback(
        (job: CronJobState) => {
            if (!job.sourceSessionId) return;
            for (const p of projects.projects) {
                for (const w of p.workspaces) {
                    const s = w.sessions.find(
                        (s: { localId: string }) =>
                            s.localId === job.sourceSessionId,
                    );
                    if (s) {
                        setAutomationsSelectedJobId(null);
                        setAppMode("chat");
                        localStorage.setItem("yansu.appMode", "chat");
                        projects.handleSwitchSession(p.id, s.localId);
                        return;
                    }
                }
            }
        },
        [projects.projects, projects.handleSwitchSession],
    );

    // Handoff opened from the composer chip — auto-expands its reader on the
    // Handoffs page (consumed on mount). Null when the page is opened via the
    // sidebar nav, which shows the list.
    const [handoffOpenId, setHandoffOpenId] = useState<string | null>(null);
    const [handoffReaderBackTarget, setHandoffReaderBackTarget] = useState<
        "list" | "chat"
    >("list");
    const [handoffReaderOpen, setHandoffReaderOpen] = useState(false);
    const [handoffBackSignal, setHandoffBackSignal] = useState(0);
    const [sopReaderOpen, setSopReaderOpen] = useState(false);
    const [sopBackSignal, setSopBackSignal] = useState(0);
    // A goal Search opened; the Goals page clears the id once it shows it.
    const [goalOpenRequest, setGoalOpenRequest] = useState<{ id: string; signal: number }>({
        id: "",
        signal: 0,
    });
    const [sopOpenRequest, setSopOpenRequest] = useState<{ id: string; signal: number }>({
        id: "",
        signal: 0,
    });
    // Tasks scrolls to a section (Scheduled) or opens All done when a link
    // asks for it; the nonce makes a repeat request count.
    const [tasksFocus, setTasksFocus] = useState<TasksFocus | null>(null);

    const todayChatRequestRef = useRef(0);
    const todayBriefGate = useRef(new TodayBriefRequestGate());
    const handleSetAppMode = useCallback((mode: AppMode | "handoffs") => {
        const destination = mode === "handoffs" ? "tasks" : mode;
        if (destination !== "memory" && destination !== "knowledge" && destination !== "people") {
            youPageReturnRef.current = destination;
        }
        todayBriefGate.current.cancel();
        todayChatRequestRef.current += 1;
        setTodayBriefRequest(null);
        setAppMode(destination);
        localStorage.setItem("yansu.appMode", destination);
        // Any explicit mode switch ends the "opened from Handoffs" context; the
        // handoff onOpen re-sets it after calling this.
        setChatBackToHandoffs(false);
        // Every explicit mode switch resets the inline automation detail so the
        // main panel shows the destination view (chat / automation grid /
        // handoffs list), never a stale detail pane on top of it.
        setAutomationsSelectedJobId(null);
        if (destination !== "tasks") {
            setHandoffReaderOpen(false);
        }
        if (destination !== "knowledge") {
            setSopReaderOpen(false);
        }
    }, []);
    handleSetAppModeRef.current = handleSetAppMode;
    const openTodayBrief = useCallback((briefId?: string, focus?: TodayBriefFocus) => {
        handleSetAppMode("today");
        setTodayBriefRequest((current) => ({ briefId, focus, nonce: (current?.nonce ?? 0) + 1 }));
    }, [handleSetAppMode]);
    openHandoffRef.current = (id: string) => {
        setHandoffReaderBackTarget(appMode === "chat" ? "chat" : "list");
        handleSetAppMode("tasks");
        setHandoffOpenId(id);
    };
    openMainChatRef.current = () => {
        settings.setSettingsOpen(false);
        setSettingsInitialTopic(undefined);
        setKnowledgeOpen(false);
        setMemoryOpen(false);
        closePeople();
        handleSetAppMode("chat");
        const open = () =>
            quietRetry(() => Promise.resolve(projects.handleOpenMainChat())).then(
                () => { reportChatPlaceFailure(null); return true; },
                () => { reportChatPlaceFailure({ key: "failure.loadChat", retry: open }); return false; },
            );
        void open();
    };
    openMainChatItemRef.current = (id: string) => {
        openMainChatRef.current();
        void mainChatItemPanel.openMoment(id);
    };
    openTodayRef.current = openTodayBrief;
    // A schedule opens in Tasks (Upcoming is Tasks › Scheduled now), and a
    // playbook in How you do things (sidebar handoff, section 3).
    openUpcomingRef.current = (jobId: string) => {
        settings.setSettingsOpen(false);
        handleSetAppMode("tasks");
        setTasksFocus((current) => ({ section: "scheduled", nonce: (current?.nonce ?? 0) + 1 }));
        setAutomationsSelectedJobId(jobId || null);
    };
    openPlaybookRef.current = (id: string) => {
        settings.setSettingsOpen(false);
        // A playbook Isoform drafted and still offers lives in Tasks › Up
        // next until you answer; a confirmed one opens in How you do things.
        void GetPlaybooksState()
            .then((state) => state?.plans?.find((plan) => plan.id === id) ?? null)
            .catch(() => null)
            .then((plan) => {
                if (offerShowsNow(plan)) {
                    handleSetAppModeRef.current("tasks");
                    return;
                }
                handleSetAppModeRef.current("knowledge");
                setSopOpenRequest((prev) => ({ id, signal: prev.signal + 1 }));
            });
    };

    // ── Activity page open helpers ──
    // Activity is a main view (appMode === "activity"). Every open bumps
    // activityNonce so the page remounts on a fresh request even while it is
    // already showing, and closes the overlays a page can't stack over. A
    // jumped-from surface reopens from the page's "Back to X" button.
    const closeOverlaysForActivityPage = useCallback((preservePeople = false) => {
        settings.setSettingsOpen(false);
        setKnowledgeOpen(false);
        setMemoryOpen(false);
        if (preservePeople) setPeopleOpen(false);
        else closePeople();
    }, [settings.setSettingsOpen, closePeople]);

    // Non-pipeline open path. Used by the sidebar, top-bar user menu, command
    // palette, menu bar, etc. — anywhere that doesn't carry highlight/restrict
    // context. Returns to the last viewed day; explicit Today links still pick today.
    const openActivityFresh = useCallback((date?: Date) => {
        resetActivityState();
        const now = date ?? lastActivityDate ?? new Date();
        setActivityInitialDate(
            `${now.getFullYear()}-${String(now.getMonth() + 1).padStart(2, "0")}-${String(now.getDate()).padStart(2, "0")}`,
        );
        // A day caller means a day section: the remembered Screen timeline
        // pick would not show the day it asked for.
        setActivitySection((current) =>
            current === "timeline" ? "yourDay" : current,
        );
        setActivityNonce((n) => n + 1);
        closeOverlaysForActivityPage();
        handleSetAppMode("activity");
    }, [resetActivityState, closeOverlaysForActivityPage, handleSetAppMode]);
    openActivityDayRef.current = (day: string) => {
        resetActivityState();
        setActivityInitialDate(day);
        setActivitySection((current) =>
            current === "timeline" ? "yourDay" : current,
        );
        setActivityNonce((n) => n + 1);
        closeOverlaysForActivityPage();
        handleSetAppMode("activity");
    };

    const openActivityWithSession = useCallback(
        (
            sessionId: string,
            from:
                | "memory"
                | "knowledge"
                | "pipeline"
                | "people"
                | null = null,
            anchorTimestamp: number | null = null,
            highlightedSenderName: string | null = null,
        ) => {
            // Clear pipeline state first so opening from memory/knowledge
            // doesn't inherit a stale triage restriction.
            setActivityHighlightedFrames([]);
            setActivityHighlightedPipelineRunId(null);
            setActivityRestrictToSessionIds([]);
            setActivityInitialSessionId(sessionId);
            setActivityInitialDate(null);
            setActivityNoticedFocus(null);
            setActivityInitialEventTimestamp(anchorTimestamp);
            setActivityHighlightedSenderName(highlightedSenderName);
            setActivityOpenedFrom(from);
            // A session jump lands on the Screen timeline section, as before.
            setActivitySection("timeline");
            setActivityNonce((n) => n + 1);
            closeOverlaysForActivityPage(from === "people");
            handleSetAppMode("activity");
        },
        [closeOverlaysForActivityPage, handleSetAppMode],
    );

    const openActivityWithHighlightedFrames = useCallback(
        (
            sessionId: string,
            frames: {
                kind: string;
                sessionId: string;
                snapshotId: number;
                promptText?: string;
            }[],
            pipelineRunId: number,
            groupSessionIds: string[],
        ) => {
            setActivityHighlightedFrames(frames);
            setActivityHighlightedPipelineRunId(pipelineRunId);
            setActivityRestrictToSessionIds(groupSessionIds);
            setActivityInitialSessionId(sessionId);
            setActivityInitialDate(null);
            setActivityNoticedFocus(null);
            setActivityInitialEventTimestamp(null);
            setActivityHighlightedSenderName(null);
            setActivityOpenedFrom("pipeline");
            setActivitySection("timeline");
            setActivityNonce((n) => n + 1);
            closeOverlaysForActivityPage();
            handleSetAppMode("activity");
        },
        [closeOverlaysForActivityPage, handleSetAppMode],
    );

    // The Activity page's "Back to X" button: reopen the surface the jump
    // came from. It overlays the page; closing it lands back on Activity.
    const handleActivityBack = useCallback(() => {
        const from = activityOpenedFrom;
        resetActivityState();
        switch (from) {
            case "memory":
                setMemoryOpen(true);
                break;
            case "knowledge":
                setKnowledgeOpen(true);
                break;
            case "people":
                setPeopleOpen(true);
                break;
            case "pipeline":
                settings.setSettingsOpen(true);
                break;
            default:
                break;
        }
    }, [activityOpenedFrom, resetActivityState, settings.setSettingsOpen]);

    const focusComposer = useCallback(() => {
        const el = document.querySelector<HTMLTextAreaElement>(
            "[data-composer-textarea]",
        );
        if (el && !el.disabled) el.focus();
    }, []);

    // The main chat is the one conversation; Today and the sidebar open it,
    // and nothing splits a side chat off it any more.
    const activeIsMainChat = projects.activeSession
        ? isMainChatSession(projects.activeSession)
        : false;
    // RC 2 D1: the main chat's filters, numbers, day folds and chips. They
    // change only what is shown; the chat stays one conversation.
    const [chatVisited, setChatVisited] = useState(false);
    useEffect(() => { if (appMode === 'chat') setChatVisited(true); }, [appMode]);
    const mainChatView = useMainChatView({
        enabled: activeIsMainChat && (chatVisited || appMode === 'chat'),
        items: activeIsMainChat ? projects.activeSession?.items ?? null : null,
    });
    usePageQueryRefresh(['LoadStreamItemsPage'], () => {
        const sessionId = projects.activeSession?.localId;
        const projectId = projects.activeProject?.id;
        if (!sessionId || !projectId) return;
        const page = pageQueryCache.peek<Awaited<ReturnType<typeof ReadStreamItemsPage>>>('LoadStreamItemsPage', [sessionId, -1, CHAT_FIRST_PAGE_SIZE]);
        if (!page) {
            if (!pageQueryCache.state('LoadStreamItemsPage', [sessionId, -1, CHAT_FIRST_PAGE_SIZE])) return;
            void ReadStreamItemsPage(sessionId, -1, CHAT_FIRST_PAGE_SIZE).catch(logPersistError);
            return;
        }
        const fresh = (page.items ?? []).map(fromStreamItemData);
        const byId = new Map(fresh.map((item) => [item.id, item]));
        projects.updateProject(projectId, (project) => ({
            ...project,
            workspaces: project.workspaces.map((workspace) => ({
                ...workspace,
                sessions: workspace.sessions.map((session) => {
                    if (session.localId !== sessionId || !session.itemsLoaded || session.streaming) return session;
                    // A revalidated page also finishes ordinary assistant,
                    // thinking and tool rows, not just changed report cards.
                    let rowsChanged = false;
                    const refreshed = session.items.map((item) => {
                        const stored = byId.get(item.id);
                        if (!stored || (stored.sortOrder !== undefined && item.sortOrder !== undefined && stored.sortOrder < item.sortOrder)) return item;
                        if (JSON.stringify(stored) === JSON.stringify(item)) return item;
                        rowsChanged = true;
                        return stored;
                    });
                    const items = mergeAppendedItems(rowsChanged ? refreshed : session.items, fresh);
                    return items === session.items ? session : { ...session, items };
                }),
            })),
        }));
    }, activeIsMainChat, (args) => args[0] === projects.activeSession?.localId && args[1] === -1 && args[2] === CHAT_FIRST_PAGE_SIZE);
    const [todayContentReady, setTodayContentReady] = useState(false);
    const chatContentReady = Boolean(projects.activeSession?.itemsLoaded || projects.activeSession?.items.length || projects.activeSession?.streamItemCount === 0);
    usePagePrefetch(appMode, appMode === 'today' ? todayContentReady : chatContentReady);
    // After Today's first paint, warm every other page's primary reads during
    // idle time; the walker parks while the user types or a reply streams.
    useIdlePagePrefetch(todayContentReady);
    const activeStreaming = Boolean(projects.activeSession?.streaming);
    useEffect(() => setPrefetchStreaming(activeStreaming), [activeStreaming]);
    useLazyPagesIdleWarm(todayContentReady || chatContentReady);
    const measuredPage = memoryOpen ? 'about-you' : knowledgeOpen ? 'know-how' : peopleOpen ? 'people' : settings.settingsOpen ? 'settings' : appMode;
    useLayoutEffect(() => beginPageSwitch(measuredPage), [measuredPage]);
    useEffect(() => revalidatePageQueries(appMode), [appMode]);
    useEffect(() => {
        if (chatContentReady) markPageContent('chat');
        if (todayContentReady) markPageContent('today');
    }, [chatContentReady, todayContentReady, appMode]);

    // Words a click put in the box, unsent, until the user edits or sends
    // them; "answer" when they came from a chip under an answer.
    const [preparedPrompt, setPreparedPrompt] = useState<{ text: string; from: "answer" | "suggestion" } | null>(null);
    const composerReplacePrompt = composer.replacePrompt;
    const prepareComposerPrompt = useCallback(
        (text: string, from: "answer" | "suggestion") => {
            composerReplacePrompt(text);
            setPreparedPrompt({ text, from });
            requestAnimationFrame(focusComposer);
        },
        [composerReplacePrompt, focusComposer],
    );
    // Chat K4: a suggestion, starter or answer chip is sent as the person's
    // message at once; nothing waits in the box.
    const composerSendImmediate = composer.sendImmediate;
    const askNow = useCallback(
        (text: string) => composerSendImmediate(text),
        [composerSendImmediate],
    );
    const composerNote =
        preparedPrompt && preparedPrompt.text === composer.prompt
            ? tChat(
                  preparedPrompt.from === "answer"
                      ? "mainChat.view.notePreparedAnswer"
                      : "mainChat.view.notePrepared",
              )
            : undefined;

    // Whether the onboarding screen (or its loading gate) is in front. It is
    // the same test as the early returns further down, computed here so the
    // main chat's setup stays idle behind onboarding. The main chat holds no
    // hub strip: a glance at the day is Today's job (docs/sidebar-study.md).
    const onboardingInFront =
        onboardingLoading ||
        (!skipOnboardingSession &&
              !onboardingState.completed &&
              !onboardingState.neverShowAgain);
    // Isoform speaks first: one setup ask at a time in the main chat. It
    // waits for onboarding to be over for good, not only out of sight: after
    // Escape onboarding comes back on the next launch, and the first message
    // must carry its answers.
    const mainChatSetup = useMainChatSetup(
        activeIsMainChat &&
            !onboardingInFront &&
            onboardingFinished(onboardingState),
    );

    const handleForkSessionFromSidebar = useCallback(
        async (projectId: string, sessionId: string) => {
            handleSetAppMode("chat");
            try {
                await streaming.waitForSessionPersistence(sessionId);
            } catch {
                toast.error(t("sessionItem.branchPersistFailed"));
                return;
            }
            const forkedSessionId = await projects.handleForkSession(
                projectId,
                sessionId,
            );
            if (forkedSessionId) {
                requestAnimationFrame(() => focusComposer());
            }
        },
        [
            handleSetAppMode,
            streaming.waitForSessionPersistence,
            projects.handleForkSession,
            focusComposer,
            t,
        ],
    );

    // ── Today panel action handlers ──
    // Today drafts land in the main chat's composer at once, prepared before
    // the click; an action that asks Isoform to do something sends itself.
    // The source reference travels separately so Go resolves current,
    // filtered context.
    const handleStartChatFromToday = useCallback(
        (prompt: string, context: TodayChatContextRef, options?: TodayChatStartOptions) => {
            if (!prompt.trim()) return;
            handleSetAppMode("chat");
            const request = todayChatRequestRef.current;
            const revision = composer.getDraftRevision();
            let preparing = false;
            const isCurrent = () => request === todayChatRequestRef.current
                && (preparing || composer.getDraftRevision() === revision);
            void openTodayMainChat(prompt, context, {
                isCurrent,
                openMainChat: projects.handleOpenMainChat,
                prefill: (sessionId, cardPrompt, itemContext, startOptions) => {
                    preparing = true;
                    return composer.prepareTodayPrompt(sessionId, cardPrompt, itemContext, isCurrent, startOptions);
                },
                focus: () => requestAnimationFrame(() => focusComposer()),
            }, options).then((opened) => {
                if (!opened) toast.error(tToday("actions.chatOpenFailed"));
            }).catch(() => toast.error(tToday("actions.chatOpenFailed")));
        },
        [
            handleSetAppMode,
            projects.handleOpenMainChat,
            composer.prepareTodayPrompt,
            composer.getDraftRevision,
            focusComposer,
            tToday,
        ],
    );

    // Navigate first; the composer holds a loading context until the brief
    // has been checked, so an answer cannot escape without its context.
    const openTodayCardChat = useCallback(
        async (prepare: () => Promise<TodayBriefOpened>): Promise<boolean> => {
            if (todayBriefGate.current.pending) return true;
            const request = todayChatRequestRef.current + 1;
            let selection = projects.getSelectionRevision();
            const isCurrent = () => request === todayChatRequestRef.current && selection === projects.getSelectionRevision();
            // Guard Send and permission answers before the prior chat is visible.
            const draft = composer.beginTodayBrief(isCurrent);
            handleSetAppMode("chat");
            const requestController = todayBriefGate.current.begin()!;
            try {
                const opened = await openTodayBriefChat(prepare, {
                    openMainChat: (current) => {
                        const opening = projects.handleOpenMainChat(current);
                        selection = projects.getSelectionRevision();
                        return opening;
                    },
                    draft,
                    signal: requestController.signal,
                    focus: () => requestAnimationFrame(() => focusComposer()),
                    isCurrent,
                });
                return !isCurrent() || opened;
            } finally {
                todayBriefGate.current.finish(requestController);
            }
        },
        [handleSetAppMode, projects.handleOpenMainChat, projects.getSelectionRevision, composer.beginTodayBrief, focusComposer],
    );
    const handleOpenBriefConversation = useCallback(
        (briefId: string) => openTodayCardChat(() => OpenTodayBriefConversation(briefId)),
        [openTodayCardChat],
    );
    const handleAskAboutParagraph = useCallback(
        (briefId: string, paragraph: number, text: string) =>
            openTodayCardChat(() => OpenTodayBriefParagraph(briefId, paragraph, text)),
        [openTodayCardChat],
    );

    // Create a goal on the Goals page: the main chat opens with a sentence to
    // finish in the composer, unsent; the chat's goal tools add the goal.
    const handleCreateGoalInChat = useCallback(
        (prompt: string) => {
            handleSetAppMode("chat");
            void openTodayMainChat(prompt, { sourceKind: "", sourceId: "" }, {
                openMainChat: projects.handleOpenMainChat,
                prefill: (sessionId, text) => composer.prefillTodayPrompt(sessionId, text, undefined),
                focus: () => requestAnimationFrame(() => focusComposer()),
            }).then((opened) => {
                if (!opened) toast.error(tToday("actions.chatOpenFailed"));
            }).catch(() => toast.error(tToday("actions.chatOpenFailed")));
        },
        [handleSetAppMode, projects.handleOpenMainChat, composer.prefillTodayPrompt, focusComposer, tToday],
    );

    // Topics pinned to the sidebar (RC 1): a click opens the topic beside
    // Today; Unpin takes it off the sidebar.
    const sidebarTopics = useSidebarTopics();
    const [activeTodayTopicId, setActiveTodayTopicId] = useState<string | null>(null);
    const [todayTopicRequest, setTodayTopicRequest] = useState<{ topic: TodayTopic; nonce: number } | null>(null);
    const handleOpenSidebarTopic = useCallback((topicId: string) => {
        const topic = sidebarTopics.topics.find((candidate) => candidate.id === topicId);
        if (!topic) return;
        handleSetAppMode("today");
        setTodayTopicRequest((current) => ({ topic, nonce: (current?.nonce ?? 0) + 1 }));
    }, [sidebarTopics.topics, handleSetAppMode]);
    const handleUnpinSidebarTopic = useCallback((topicId: string) => {
        void PinTopicToSidebar(topicId, false).then(
            () => window.dispatchEvent(new Event(SIDEBAR_TOPICS_CHANGED)),
            () => toast.error(tToday("topicPanel.pinFailed")),
        );
    }, [tToday]);

    const briefHandlers = useMemo<TodayActionHandlers>(
        () => ({
            onStartChat: handleStartChatFromToday,
            onOpenBriefConversation: handleOpenBriefConversation,
            onAskAboutParagraph: handleAskAboutParagraph,
            onRunNow: (jobId: string) => {
                void cronJobs.runNow(jobId).catch(() => {});
            },
            onOpenMemory: (entryId?: string) => {
                if (entryId) setPendingInsightMemoryId(entryId);
                setMemoryOpen(true);
            },
            onOpenAutomations: () => {
                handleSetAppMode("tasks");
                setHandoffOpenId(null);
                setTasksFocus((current) => ({ section: "scheduled", nonce: (current?.nonce ?? 0) + 1 }));
            },
            onOpenBriefDetail: (slot: "morning" | "evening") => {
                handleSetAppMode("tasks");
                setHandoffOpenId(null);
                setTasksFocus((current) => ({ section: "scheduled", brief: slot, nonce: (current?.nonce ?? 0) + 1 }));
            },
            // Goals are a group in About you now (sidebar handoff, section 5).
            onOpenGoals: () => setMemoryOpen(true),
            onOpenTasks: () => {
                handleSetAppMode("tasks");
                setHandoffOpenId(null);
                setTasksFocus((current) => ({ section: "needs", nonce: (current?.nonce ?? 0) + 1 }));
            },
            onOpenDoneTasks: () => {
                handleSetAppMode("tasks");
                setHandoffOpenId(null);
                setTasksFocus((current) => ({ section: "done", nonce: (current?.nonce ?? 0) + 1 }));
            },
            onOpenActivity: () => openActivityFresh(new Date()),
            onOpenMeetingNotes: (sessionId: string) => openActivityWithSession(sessionId),
            onOpenActivitySuggestion: (target) => {
                openActivityDayRef.current(target.date);
                setActivitySection("yourDay");
                setActivityNoticedFocus(target);
            },
            onOpenPeople: (personId?: string) => {
                setPeopleInitialSelection(personId ? { personId } : undefined);
                setPeopleOpen(true);
            },
            onOpenCaptureCounts: () =>
                openSettings("activity", { anchor: CAPTURED_COUNTS_ANCHOR }),
            onOpenSettings: (topic: string) => openSettings(topic as SettingsTopic),
            onOpenMainChat: () => {
                handleSetAppMode("chat");
                void Promise.resolve(projects.handleOpenMainChat()).catch(() => {});
            },
            onPrefillChat: handleCreateGoalInChat,
        }),
        [
            handleCreateGoalInChat,
            handleStartChatFromToday,
            handleOpenBriefConversation,
            handleAskAboutParagraph,
            cronJobs.runNow,
            setPendingInsightMemoryId,
            setMemoryOpen,
            handleSetAppMode,
            openActivityFresh,
            openActivityWithSession,
            setPeopleOpen,
            openSettings,
            projects.handleOpenMainChat,
        ],
    );

    // Auto-focus composer when active session changes (new session or switch)
    const activeSessionId = projects.activeSession?.localId ?? null;

    // Rich deliverable actions for chat file cards, bound to the active
    // session's localId (the SendPrompt tag). Memoized on the session id so the
    // object identity is stable across renders — RichDeliverableCard keys its
    // metadata fetch on the actions reference.
    const chatDeliverableActions = useMemo<DeliverableActions>(() => {
        const fail = (err: unknown) =>
            toast.error(err instanceof Error ? err.message : String(err));
        return {
            open: (path: string) => {
                if (activeSessionId)
                    OpenChatDeliverable(activeSessionId, path).catch(fail);
            },
            reveal: (path: string) => {
                if (activeSessionId)
                    RevealChatDeliverable(activeSessionId, path).catch(fail);
            },
            openWith: (path: string, appPath: string) => {
                if (activeSessionId)
                    OpenChatDeliverableWithApp(
                        activeSessionId,
                        path,
                        appPath,
                    ).catch(fail);
            },
            listOpenWith: (path: string) =>
                activeSessionId
                    ? ListChatDeliverableOpenWithApps(activeSessionId, path)
                    : Promise.resolve([]),
            getInfo: (path: string) =>
                activeSessionId
                    ? ChatDeliverableInfo(activeSessionId, path)
                    : Promise.reject(new Error("no active session")),
        };
    }, [activeSessionId]);

    // Observe every session, including runs that finish while another is selected.
    const prevStreamingBySessionRef = useRef(new Map<string, boolean>());
    useEffect(() => {
        for (const result of completedChatDeliverables(projects.projects, prevStreamingBySessionRef.current)) {
            void RecordChatDeliverables(result.sessionId, result.sessionId, result.title, result.summary, result.files).catch(() => {});
        }
    }, [projects.projects]);

    const prevSessionIdRef = useRef<string | null>(activeSessionId);
    useEffect(() => {
        if (activeSessionId && activeSessionId !== prevSessionIdRef.current) {
            prevSessionIdRef.current = activeSessionId;
            focusComposer();
        }
    }, [activeSessionId, focusComposer]);

    // Also focus composer when entering chat mode (e.g. clicking "New Chat"
    // often reuses an existing empty session, so activeSessionId doesn't
    // change — but the composer just mounted and still needs focus).
    const prevAppModeRef = useRef(appMode);
    useEffect(() => {
        if (
            prevAppModeRef.current !== "chat" &&
            appMode === "chat" &&
            activeSessionId
        ) {
            // Wait one frame so the composer textarea is in the DOM.
            requestAnimationFrame(() => focusComposer());
        }
        prevAppModeRef.current = appMode;
    }, [appMode, activeSessionId, focusComposer]);

    // Transcript sessions carry their body as the first assistant item.
    // On every switch to a transcript session, ask the backend to ensure
    // the first message is present and in the current format — handles
    // both sessions created before transcripts were chat-persisted AND
    // sessions whose first message was persisted with an older layout
    // (e.g. timestamped lines collapsed into one paragraph).
    //
    // This effect owns hydration for transcript sessions (the lazy-load
    // effect below skips them): because stream items are no longer part of
    // the startup payload, we must always load the page after ensuring the
    // first message, regardless of whether the backend changed anything.
    const transcriptBackfillAttemptedRef = useRef<Set<string>>(new Set());
    useEffect(() => {
        const session = projects.activeSession;
        if (!session) return;
        if (!session.label?.startsWith("Transcript: ")) return;
        if (transcriptBackfillAttemptedRef.current.has(session.localId)) return;
        transcriptBackfillAttemptedRef.current.add(session.localId);
        const sessionId = session.localId;
        const projectId = projects.activeProject?.id;
        const workspaceId = projects.activeWorkspace?.id;
        if (!projectId || !workspaceId) return;
        (async () => {
            try {
                await EnsureTranscriptChatMessage(sessionId);
                const page = await loadStreamItemsPageWithOpenAnswerDelivery(sessionId, -1, CHAT_FIRST_PAGE_SIZE);
                const items = (page.items ?? []).map(fromStreamItemData);
                projects.updateProject(projectId, (p: any) => ({
                    ...p,
                    workspaces: p.workspaces.map((w: any) =>
                        w.id !== workspaceId
                            ? w
                            : {
                                  ...w,
                                  sessions: w.sessions.map((s: any) =>
                                      s.localId === sessionId
                                          ? {
                                                ...s,
                                                items,
                                                streamItemCount: Math.max(
                                                    s.streamItemCount ?? 0,
                                                    items.length,
                                                ),
                                                itemsLoaded: true,
                                                itemsLoading: false,
                                                hasMore: page.hasMore,
                                                nextCursor: page.nextCursor,
                                            }
                                          : s,
                                  ),
                              },
                    ),
                }));
            } catch (err) {
                console.warn("[transcript] ensure first message failed:", err);
                // Clear the hydrating state so the chat does not get stuck on a
                // loading spinner when backfill fails.
                projects.updateProject(projectId, (p: any) => ({
                    ...p,
                    workspaces: p.workspaces.map((w: any) =>
                        w.id !== workspaceId
                            ? w
                            : {
                                  ...w,
                                  sessions: w.sessions.map((s: any) =>
                                      s.localId === sessionId
                                          ? {
                                                ...s,
                                                itemsLoaded: true,
                                                itemsLoading: false,
                                            }
                                          : s,
                                  ),
                              },
                    ),
                }));
            }
        })();
    }, [
        projects.activeSession,
        projects.activeProject?.id,
        projects.activeWorkspace?.id,
        projects.updateProject,
    ]);

    // Hydrate the selected chat lazily. Project/session metadata is loaded at
    // startup, but stream items stay out of the initial payload so switching
    // chats does not force every recent conversation through the Wails bridge.
    useEffect(() => {
        const session = projects.activeSession;
        const projectId = projects.activeProject?.id;
        const workspaceId = projects.activeWorkspace?.id;
        if (!session || !projectId || !workspaceId) return;
        // Transcript sessions are hydrated by the backfill effect above, which
        // ensures the first message exists before loading. Skip them here to
        // avoid a redundant LoadStreamItemsPage call.
        if (session.label?.startsWith("Transcript: ")) return;
        if (session.itemsLoaded || session.itemsLoading) return;
        if (session.streamItemCount <= 0) {
            projects.updateProject(projectId, (p: any) => ({
                ...p,
                workspaces: p.workspaces.map((w: any) =>
                    w.id !== workspaceId
                        ? w
                        : {
                              ...w,
                              sessions: w.sessions.map((s: any) =>
                                  s.localId === session.localId
                                      ? {
                                            ...s,
                                            itemsLoaded: true,
                                            itemsLoading: false,
                                            hasMore: false,
                                            nextCursor: 0,
                                        }
                                      : s,
                              ),
                          },
                ),
            }));
            return;
        }

        const sessionId = session.localId;
        projects.updateProject(projectId, (p: any) => ({
            ...p,
            workspaces: p.workspaces.map((w: any) =>
                w.id !== workspaceId
                    ? w
                    : {
                          ...w,
                          sessions: w.sessions.map((s: any) =>
                              s.localId === sessionId
                                  ? { ...s, itemsLoading: true }
                                  : s,
                          ),
                      },
            ),
        }));

        let cancelled = false;
        let settled = false;
        (async () => {
            try {
                const page = await ReadStreamItemsPage(sessionId, -1, CHAT_FIRST_PAGE_SIZE);
                if (cancelled) return;
                const loadedItems: StreamItem[] = (page.items ?? []).map(
                    fromStreamItemData,
                );
                projects.updateProject(projectId, (p: any) => ({
                    ...p,
                    workspaces: p.workspaces.map((w: any) =>
                        w.id !== workspaceId
                            ? w
                            : {
                                  ...w,
                                  sessions: w.sessions.map((s: any) => {
                                      if (s.localId !== sessionId) return s;
                                      const loadedIds = new Set(
                                          loadedItems.map((item) => item.id),
                                      );
                                      const liveItems = (s.items ?? []).filter(
                                          (item: any) =>
                                              !loadedIds.has(item.id),
                                      );
                                      const items = [
                                          ...loadedItems,
                                          ...liveItems,
                                      ];
                                      const firstSortOrder =
                                          items.length > 0
                                              ? (items[0].sortOrder ?? 0)
                                              : 0;
                                      return {
                                          ...s,
                                          items,
                                          streamItemCount: Math.max(
                                              s.streamItemCount ?? 0,
                                              items.length,
                                          ),
                                          itemsLoaded: true,
                                          itemsLoading: false,
                                          hasMore: page.hasMore,
                                          nextCursor:
                                              page.nextCursor ?? firstSortOrder,
                                      };
                                  }),
                              },
                    ),
                }));
                settled = true;
            } catch (err) {
                if (cancelled) return;
                logPersistError(err);
                projects.updateProject(projectId, (p: any) => ({
                    ...p,
                    workspaces: p.workspaces.map((w: any) =>
                        w.id !== workspaceId
                            ? w
                            : {
                                  ...w,
                                  sessions: w.sessions.map((s: any) =>
                                      s.localId === sessionId
                                          ? { ...s, itemsLoading: false }
                                          : s,
                                  ),
                              },
                    ),
                }));
                settled = true;
            }
        })();

        return () => {
            cancelled = true;
            // If hydration was still in flight when we switched away, clear the
            // loading flag so a later switch back can retry. Otherwise the
            // top-of-effect guard (itemsLoaded || itemsLoading) would block
            // re-hydration and leave the session stuck on a spinner.
            if (settled) return;
            projects.updateProject(projectId, (p: any) => ({
                ...p,
                workspaces: p.workspaces.map((w: any) =>
                    w.id !== workspaceId
                        ? w
                        : {
                              ...w,
                              sessions: w.sessions.map((s: any) =>
                                  s.localId === sessionId && s.itemsLoading
                                      ? { ...s, itemsLoading: false }
                                      : s,
                              ),
                          },
                ),
            }));
        };
    }, [
        projects.activeSession?.localId,
        // Re-run when the loaded flag flips back to false. Re-login rebuilds the
        // project tree from storage (items omitted, itemsLoaded reset), but the
        // active session's localId is unchanged — without this dependency the
        // hydrate effect would not fire and the chat stays blank until the user
        // switches sessions.
        projects.activeSession?.itemsLoaded,
        projects.activeProject?.id,
        projects.activeWorkspace?.id,
        projects.updateProject,
    ]);

    // ── PR info (disabled — PRButton has bugs) ──
    // const isNonDefaultWs = !!(projects.activeWorkspace && !projects.activeWorkspace.isDefault);
    // const prInfo = usePRInfo({
    //     projectPath: projects.activeProject?.path,
    //     remoteUrl: projects.activeProject?.gitInfo?.remoteUrl,
    //     branch: projects.activeWorkspace?.worktreeBranch,
    //     baseBranch: projects.activeWorkspace?.baseBranch,
    //     enabled: isNonDefaultWs,
    // });

    // ── Persist window geometry ──
    // Size, position, and maximised state are restored by the Go side before
    // the storage gate mounts this UI; no restore work is needed
    // here. This effect only writes updates back to the store so the next
    // launch opens at the last-used geometry.
    //
    // We save on a debounced resize/move signal because `beforeunload` is
    // unreliable on Windows: X-to-tray hides the window without unloading the
    // webview, and a later quit from the tray menu also skips beforeunload.
    // A Go OnBeforeClose hook catches the quit path; this effect covers the
    // case where the user resizes and then hides-to-tray without quitting.
    useEffect(() => {
        let timeout: ReturnType<typeof setTimeout> | null = null;
        const schedule = () => {
            if (timeout) clearTimeout(timeout);
            timeout = setTimeout(() => {
                CaptureAndSaveWindowState().catch(() => {});
            }, 500);
        };

        const handleBeforeUnload = () => {
            // Flush pending assistant content to DB before the webview tears down.
            streaming.flushAllPendingContent();
            schedule();
        };

        window.addEventListener("resize", schedule);
        window.addEventListener("beforeunload", handleBeforeUnload);
        return () => {
            window.removeEventListener("resize", schedule);
            window.removeEventListener("beforeunload", handleBeforeUnload);
            if (timeout) clearTimeout(timeout);
        };
    }, [streaming.flushAllPendingContent]);

    // ── Onboarding state load/save ──
    useEffect(() => {
        let mounted = true;
        setOnboardingLoading(true);
        loadOnboardingStateRaw(() => LoadUIState(ONBOARDING_STORAGE_KEY))
            .then((raw) => {
                if (!mounted) return;
                const nextState = parseOnboardingState(raw);
                onboardingStateRef.current = nextState;
                setOnboardingState(nextState);
            })
            .catch((err: unknown) => {
                if (!mounted) return;
                // The saved state could not be read. Do not treat that as a new
                // person: skip onboarding for this session only and save
                // nothing, so the next launch reads the real state again.
                const message = err instanceof Error ? err.message : String(err);
                LogWarning(`[onboarding] state read failed, skipping this session: ${message}`);
                setSkipOnboardingSession(true);
            })
            .finally(() => {
                if (mounted) setOnboardingLoading(false);
            });

        return () => {
            mounted = false;
        };
    }, []);

    // Load first-login flag: if "yansu.hasChattedBefore" is not set, check
    // whether existing sessions have items. Only show welcome for truly new users.
    useEffect(() => {
        LoadUIState("yansu.hasChattedBefore")
            .then((raw) => {
                if (raw === "true") {
                    setIsFirstLogin(false);
                } else {
                    // Flag not set — check if any session already has items
                    // (existing user who upgraded before this flag existed).
                    const hasExistingChats = projects.projects.some((p) =>
                        p.workspaces.some((w) =>
                            w.sessions.some(
                                (s) =>
                                    s.streamItemCount > 0 ||
                                    (s.items && s.items.length > 0),
                            ),
                        ),
                    );
                    if (hasExistingChats) {
                        setIsFirstLogin(false);
                        void SaveUIState(
                            "yansu.hasChattedBefore",
                            "true",
                        ).catch(() => {});
                    } else {
                        setIsFirstLogin(true);
                    }
                }
                firstLoginLoadedRef.current = true;
            })
            .catch(() => {
                setIsFirstLogin(false);
                firstLoginLoadedRef.current = true;
            });
    }, [projects.projects]);

    // Mark first-login as done when the active session gets its first item.
    useEffect(() => {
        if (!isFirstLogin || !firstLoginLoadedRef.current) return;
        if (
            projects.activeSession?.items &&
            projects.activeSession.items.length > 0
        ) {
            setIsFirstLogin(false);
            void SaveUIState("yansu.hasChattedBefore", "true").catch(() => {});
        }
    }, [isFirstLogin, projects.activeSession?.items?.length]);

    const saveOnboardingState = useCallback((next: OnboardingState) => {
        const normalized = sanitizeOnboardingStateValue(next);
        onboardingStateRef.current = normalized;
        setOnboardingState(normalized);
        return SaveUIState(ONBOARDING_STORAGE_KEY, JSON.stringify(normalized));
    }, []);

    const persistOnboardingState = useCallback(
        (next: OnboardingState) => {
            void saveOnboardingState(next).catch(logPersistError);
        },
        [saveOnboardingState],
    );

    // Once per launch: open the main chat instead of a blank plain chat, so
    // the first thing a new user sees is Isoform's own message.
    const landedOnMainChatRef = useRef(false);
    useEffect(() => {
        if (landedOnMainChatRef.current || onboardingInFront || !projects.projectsHydrated) return;
        const active = projects.activeSession;
        if (!active) return;
        landedOnMainChatRef.current = true;
        const blankPlain =
            projects.activeProject?.id === STANDALONE_CHAT_PROJECT_ID &&
            !isMainChatSession(active) &&
            !isSideChatSession(active) &&
            active.items.length === 0 &&
            active.streamItemCount === 0;
        if (blankPlain) {
            void Promise.resolve(projects.handleOpenMainChat()).catch((err) =>
                console.error("[main-chat] landing failed:", err),
            );
        }
    }, [
        onboardingInFront,
        projects.projectsHydrated,
        projects.activeSession,
        projects.activeProject?.id,
        projects.handleOpenMainChat,
    ]);

    const handleOnboardingFinish = useCallback((startedFirstTask: boolean) => {
        // The landing below must not pull the user away from what onboarding
        // opens next.
        landedOnMainChatRef.current = true;
        // The main window opens on Chat in the sidebar (handoff §4, "The finish"),
        // not on Today with Chat as the side pane.
        handleSetAppMode("chat");
        // Onboarding's own first task runs in its chat. Onboarding has
        // already closed the main chat's asks it made (first task included),
        // so the main chat does not ask for them again.
        if (!startedFirstTask) {
            // Isoform's first message waits in the main chat; land there.
            void Promise.resolve(projects.handleOpenMainChat()).catch((err) =>
                console.error("[onboarding] open main chat failed:", err),
            );
        }
        void saveOnboardingState({
            ...onboardingStateRef.current,
            completed: true,
        })
            .then(() => {
                LogInfo("[onboarding] completed");
            })
            .catch((err: unknown) => {
                const message =
                    err instanceof Error ? err.message : String(err);
                LogWarning(`[onboarding] completion failed: ${message}`);
                logPersistError(err);
            });
    }, [saveOnboardingState, projects.handleOpenMainChat, handleSetAppMode]);

    const handleOnboardingSkipSession = useCallback(() => {
        setSkipOnboardingSession(true);
    }, []);

    const handleReplayOnboarding = useCallback(() => {
        persistOnboardingState(sanitizeOnboardingStateValue(null));
        setSkipOnboardingSession(false);
        settings.setSettingsOpen(false);
    }, [persistOnboardingState, settings]);

    const applyModelGroups = useCallback((groups: ExecutorGroup[]) => {
        if (!groups || groups.length === 0) {
            console.warn(
                "[models] no local model CLI detected, using fallback models",
            );
            toast.warning(
                "No local model CLI detected. Using fallback models.",
            );
            return;
        }

        setExecutorGroups(groups);
        const firstDefault = groups[0]?.default;
        const availableIds = new Set(
            groups.flatMap((group) =>
                group.models.map((candidate) => candidate.id),
            ),
        );
        if (firstDefault) {
            setModel((prev) => {
                if (!availableIds.has(prev)) return firstDefault;
                if (prev === DEFAULT_MODEL) return firstDefault;
                return prev;
            });
        }
    }, []);

    const loadModelOptions = useCallback(() => {
        ListAvailableModels()
            .then(applyModelGroups)
            .catch((err: unknown) => {
                const message =
                    err instanceof Error ? err.message : String(err);
                console.error(
                    "[models] failed to load available models:",
                    message,
                );
                toast.error(
                    "Failed to detect local models. Using fallback models.",
                );
            });
    }, [applyModelGroups]);

    // ── Model list fetch ──
    useEffect(() => {
        void loadModelOptions();

        const onModelsUpdated = (event: Event) => {
            const ce = event as CustomEvent<ExecutorGroup[]>;
            if (ce.detail && Array.isArray(ce.detail)) {
                applyModelGroups(ce.detail);
            } else {
                void loadModelOptions();
            }
        };

        window.addEventListener("yansu:claude-models-updated", onModelsUpdated);
        return () => {
            window.removeEventListener(
                "yansu:claude-models-updated",
                onModelsUpdated,
            );
        };
    }, [loadModelOptions, applyModelGroups]);

    // ── Startup ──
    useEffect(() => {
        let mounted = true;

        StartAgent()
            .then((nextStatus) => {
                if (mounted) streaming.setStatus(nextStatus);
            })
            .catch((err: unknown) => {
                const message =
                    err instanceof Error ? err.message : String(err);
                if (mounted) toast.error(`Failed to start agent: ${message}`);
            });

        LoadAllProjects()
            .then((saved) => {
                if (!mounted || !saved || saved.length === 0) return;
                const hydrated = convertFromStorage(saved);
                projects.setProjects(hydrated);
                for (const p of hydrated) {
                    for (const w of p.workspaces) {
                        for (const s of w.sessions) {
                            if (s.sessionId) streaming.initializeTracker(s.localId, s.sessionId);
                        }
                    }
                }

                // Disk state can paint before git runs. Enrich one repository
                // at a time and retain edits made while its status was loading.
                const enrich = async () => {
                    for (const p of hydrated) {
                        if (!mounted) return;
                        try {
                            const info = await GetGitInfo(p.path);
                            if (!mounted) return;
                            if (info.isGitRepo) projects.setProjects((current) => current.map((project) =>
                                project.id === p.id && project.path === p.path
                                    ? { ...project, gitInfo: info as import("@/types").GitInfo }
                                    : project));
                        } catch { /* Keep the persisted sidebar on a failed probe. */ }
                    }
                };
                requestAnimationFrame(() => requestAnimationFrame(() => { void enrich(); }));
            })
            .catch(logPersistError)
            .finally(() => {
                if (mounted) {
                    projects.markProjectsHydrated();
                }
            });

        LoadActiveProjectID()
            .then((savedId) => {
                if (mounted && savedId) projects.setActiveProjectId(savedId);
            })
            .catch(logPersistError);

        return () => {
            mounted = false;
        };
    }, []);

    // ── Spotlight session sync ──
    // Spotlight conversations are persisted on the Go side (see
    // spotlight_app.go::persistSpotlightAssistant), then this event
    // fires to tell us a new session row + stream items showed up
    // in the DB while the main UI was running. Re-load the project
    // tree so the standalone-chat ("General Chat") group renders the
    // new session in the sidebar. Debounced because a multi-turn
    // spotlight run emits one event per turn.
    useEffect(() => {
        let pending: ReturnType<typeof setTimeout> | null = null;
        const refresh = () => {
            LoadAllProjects()
                .then((saved) => {
                    if (!saved) return;
                    projects.setProjects(convertFromStorage(saved));
                })
                .catch(logPersistError);
        };
        const unsub = EventsOn("spotlight:session-changed", () => {
            if (pending) clearTimeout(pending);
            pending = setTimeout(refresh, 300);
        });
        return () => {
            if (pending) clearTimeout(pending);
            unsub();
        };
    }, [projects.setProjects]);

    // ── Spotlight maximize → switch to standalone chat session ──
    // Spotlight's expand button calls App.expandSpotlightToMain on the
    // Go side, which surfaces the main window and emits this event.
    // We refresh the project tree (so a fresh spotlight session shows
    // up in the sidebar), switch to the standalone chat project, and
    // activate the matching session by its localId (= the spotlight
    // tag, by construction).
    useEffect(() => {
        const unsub = EventsOn(
            "spotlight:expand-to-chat",
            async (data: { sessionLocalId?: string }) => {
                const sid = (data?.sessionLocalId ?? "").trim();
                if (!sid) return;
                try {
                    const saved = await LoadAllProjects();
                    if (saved) projects.setProjects(convertFromStorage(saved));
                } catch (err) {
                    logPersistError(err);
                }
                projects.handleSwitchProject(STANDALONE_CHAT_PROJECT_ID);
                projects.handleSwitchSession(STANDALONE_CHAT_PROJECT_ID, sid);
                handleSetAppMode("chat");
            },
        );
        return () => unsub();
    }, [projects, handleSetAppMode]);

    // ── Credential probe greeting toast ──
    useEffect(() => {
        const unsub = EventsOn("auth:greeting", (data: { message: string }) => {
            if (data?.message) toast(data.message, { duration: 4000 });
        });
        return () => {
            unsub();
        };
    }, []);

    // ── Auth recovery toasts ──
    useEffect(() => {
        const unsubRecovering = EventsOn("auth:recovering", () => {
            toast.loading("Reconnecting...", {
                id: "auth-recovery",
                duration: 15000,
            });
        });
        const unsubFallback = EventsOn(
            "auth:fallback",
            (data: { message: string }) => {
                toast.dismiss("auth-recovery");
                if (data?.message)
                    toast.success(data.message, { duration: 5000 });
            },
        );
        const unsubAuthError = EventsOn(
            "auth:error",
            (data: { message: string }) => {
                toast.dismiss("auth-recovery");
                if (data?.message)
                    toast.error(data.message, { duration: 10000 });
            },
        );
        return () => {
            unsubRecovering();
            unsubFallback();
            unsubAuthError();
        };
    }, []);

    // ── Open settings from anywhere via event ──
    // EventsEmit("settings:open", topic) opens a page;
    // EventsEmit("settings:open", topic, { advanced: true, anchor }) also
    // expands its Advanced section or scrolls to a marked element.
    useEffect(() => {
        const unsub = EventsOn(
            "settings:open",
            (payload?: unknown, extra?: unknown) => {
                const { topic, options } = readSettingsOpenEvent(
                    payload,
                    extra,
                );
                openSettings(topic, options);
            },
        );
        return () => unsub();
    }, [openSettings]);

    // ── Open the Activity log from the menu bar ──
    // The menu bar's "doing now" line shows the window and emits this.
    useEffect(() => {
        const unsub = EventsOn("activity:open", () => openActivityFresh());
        return () => unsub();
    }, [openActivityFresh]);

    // ── Handle proactive:notification events ──
    // Fires for automation proposals and handoff requests.
    // Uses refs for projects state to keep the subscription stable and avoid
    // re-subscribing EventsOn on every projects.projects change.
    useEffect(() => {
        const unsub = EventsOn("proactive:notification", async (data: any) => {
            const { chatSessionId, projectId, workspaceId, label, silent } =
                data;

            let items: StreamItem[] = [];
            try {
                const page = await loadStreamItemsPageWithOpenAnswerDelivery(chatSessionId, -1, CHAT_FIRST_PAGE_SIZE);
                items = (page.items ?? []).map(fromStreamItemData);
            } catch (err) {
                console.error("[proactive] failed to load items:", err);
            }

            const newSession = {
                ...createSession(
                    "claude-haiku-4-5-20251001",
                    workspaceId,
                    label,
                ),
                localId: chatSessionId,
                items,
                streamItemCount: items.length,
                itemsLoaded: true,
                itemsLoading: false,
            };

            const targetProj =
                projectsRef.current.find((p: any) => p.id === projectId) ??
                activeProjectRef.current;
            if (targetProj) {
                const targetWsId =
                    targetProj.id === projectId
                        ? workspaceId
                        : targetProj.workspaces?.[0]?.id;
                const updatedWorkspaces = targetProj.workspaces.map(
                    (w: any) => {
                        if (w.id === targetWsId) {
                            return {
                                ...w,
                                sessions: [...(w.sessions || []), newSession],
                            };
                        }
                        return w;
                    },
                );
                updateProjectRef.current(targetProj.id, (p: any) => ({
                    ...p,
                    workspaces: updatedWorkspaces,
                }));
            }

            const isTranscript =
                typeof label === "string" && label.startsWith("Transcript:");
            const isHandoff =
                typeof label === "string" && label.startsWith("Handoff:");

            // Silent restores reuse this pipeline to re-insert a soft-deleted
            // session into projects state but should not pop a "new proposal"
            // toast — the user already knows.
            if (silent) {
                return;
            }

            if (isTranscript) {
                // A meeting transcript announces nothing.
            } else if (isHandoff) {
                // Handoff toast: queue if user is away (visibility=hidden), else show now.
                // The queue drains on visibility/focus return — see the digest effect below.
                const entry = {
                    proposalId: data?.proposalId ?? "",
                    chatSessionId,
                    label,
                };
                if (
                    typeof document !== "undefined" &&
                    document.visibilityState === "hidden"
                ) {
                    pendingHandoffNotificationsRef.current.push(entry);
                } else {
                    const niceLabel = label.replace(/^Handoff:\s*/, "");
                    toast.success(
                        t("handoffToast.single", { name: niceLabel }),
                        {
                            duration: 8000,
                            action: {
                                label: t("handoffToast.open"),
                                onClick: () =>
                                    handleClickGroupItemRef.current?.({
                                        itemType: "chat",
                                        refId: chatSessionId,
                                    }),
                            },
                        },
                    );
                }
            } else {
                toast.success("New scheduled automation set up for you", {
                    duration: 6000,
                    action: {
                        label: "Open",
                        onClick: () =>
                            handleClickGroupItemRef.current?.({
                                itemType: "chat",
                                refId: chatSessionId,
                            }),
                    },
                });
            }
        });
        return () => unsub();
    }, []); // stable — reads from refs

    // Subscribe to report updates and share the same routing for report,
    // proactive action, evidence and side-chat origin links.
    const handleOpenReportTarget = useMainChatReports({
        projects: projects.projects,
        updateProject: projects.updateProject,
        activeSessionId: projects.activeSession?.localId ?? null,
        markSessionUnread: projects.markSessionUnread,
        openSession: (id) => handleClickGroupItemRef.current?.({ itemType: "chat", refId: id }),
        openHandoff: (id) => openHandoffRef.current(id),
        openPlaybook: (id) => openPlaybookRef.current(id),
        setPendingInsightKnowledgeId,
        setKnowledgeOpen,
        setPendingInsightMemoryId,
        setMemoryOpen,
        openScheduled: () => {
            handleSetAppMode("tasks");
            setTasksFocus((current) => ({ section: "scheduled", nonce: (current?.nonce ?? 0) + 1 }));
        },
        openTasksUpNext: () => {
            handleSetAppMode("tasks");
            setTasksFocus((current) => ({ section: "up-next", nonce: (current?.nonce ?? 0) + 1 }));
        },
        openToday: openTodayBrief,
        setAutomationsSelectedJobId,
        openSettings,
        openActivity: openActivityFresh,
        setPeopleInitialSelection,
        setPeopleOpen,
        openThread: (id: string) => {
            // The thread outcome was observed on an activity thread; its next
            // step lives in the Activity page's "What you worked on".
            openActivityFresh(new Date());
            setActivitySection("workedOn");
            setActivityInitialThreadId(id);
        },
    });

    // "Use as my message" on a texted reply: its sender is unverified, so the
    // words only become the user's message once the user presses Send on the
    // draft card in Chat's panel (Chat K4).
    const handleUseTextedReply = useCallback(
        (text: string) => {
            if (!text.trim()) return;
            mainChatItemPanel.close();
            openMainChatReplyDraft(text, composerSendImmediate);
        },
        [mainChatItemPanel.close, composerSendImmediate],
    );

    // ── Search and commands: where each result opens ──
    const closeOverlaysForSearch = () => {
        settings.setSettingsOpen(false);
        setSettingsInitialTopic(undefined);
        setKnowledgeOpen(false);
        setMemoryOpen(false);
        closePeople();
    };
    const openSessionFromSearch = (projectId: string, sessionId: string) => {
        closeOverlaysForSearch();
        handleSetAppMode("chat");
        projects.handleSwitchSession(projectId, sessionId);
        projects.markSessionRead(sessionId);
    };
    // A chat message opens its chat scrolled to it (FindStreamItemCursor).
    const chatJump = useChatMessageJump({
        projects: projects.projects,
        activeSession: projects.activeSession,
        updateProject: projects.updateProject,
        openSession: openSessionFromSearch,
        onFailed: () => toast.error(t("searchDialog.openFailed")),
    });
    openChatMessageRef.current = (sessionId, itemId) => {
        void chatJump.jump(sessionId, itemId);
    };
    useEffect(() => () => stopBriefPlayback(), []);
    // The menu bar popover shows the same player: its state at once, its
    // position about once a second.
    useEffect(() => {
        let sentAt = 0;
        let sentKey = "";
        return onBriefPlaybackProgress((progress) => {
            const key = `${progress.briefId}:${progress.state}`;
            const now = Date.now();
            if (key === sentKey && now - sentAt < 1000) return;
            sentKey = key;
            sentAt = now;
            void Promise.resolve()
                .then(() => SetMenuBarBriefPlayback(progress.briefId, progress.state, progress.position, progress.duration))
                .catch(() => {});
        });
    }, []);

    // Read the newest brief's audio ahead of Play, so the menu bar starts it
    // at once. Launch waits a moment for the first paint; a stored brief waits
    // for the stop that its own event triggers (menu bar 1.0).
    useEffect(() => {
        const launch = window.setTimeout(() => { void prepareLatestBriefAudio().catch(() => {}); }, 2000);
        const offStored = EventsOn("brief:stored", () => {
            window.setTimeout(() => { void prepareLatestBriefAudio().catch(() => {}); }, 0);
        });
        return () => { window.clearTimeout(launch); offStored(); };
    }, []);

    const [menuBarItem, setMenuBarItem] = useState<{ sessionId: string; itemId: string } | null>(null);
    const [seesOpen, setSeesOpen] = useState(false);
    useEffect(() => EventsOn("menubar:sees", () => setSeesOpen(true)), []);
    const menuBarSelectedItem = menuBarItem && projects.activeSession?.localId === menuBarItem.sessionId
        ? projects.activeSession.items.find((item) => item.id === menuBarItem.itemId && getMainChatProactive(item))
        : undefined;
    const menuBarItemSeen = useRef(false);
    useEffect(() => {
        if (!menuBarItem) { menuBarItemSeen.current = false; return; }
        if (appMode === "chat" && projects.activeSession?.localId === menuBarItem.sessionId) {
            menuBarItemSeen.current = true;
        } else if (menuBarItemSeen.current) {
            setMenuBarItem(null);
        }
    }, [menuBarItem, appMode, projects.activeSession?.localId]);
    useEffect(() => {
        if (!menuBarItem) return;
        const onKeyDown = (event: KeyboardEvent) => {
            if (event.key === "Escape") setMenuBarItem(null);
        };
        window.addEventListener("keydown", onKeyDown);
        const offPrivacy = onPrivacyReset(() => setMenuBarItem(null));
        const offPermissions = EventsOn("permissions:changed", () => setMenuBarItem(null));
        const offListening = EventsOn("background-listening:changed", () => setMenuBarItem(null));
        return () => { window.removeEventListener("keydown", onKeyDown); offPrivacy(); offPermissions(); offListening(); };
    }, [menuBarItem]);

    // Native menu bar actions use the same navigation and audio controller as Today.
    useEffect(() => {
        const offWaiting = EventsOn("menubar:waiting", (sessionId: string, itemId: string) => {
            closeOverlaysForSearch();
            menuBarItemSeen.current = false;
            setMenuBarItem(sessionId && itemId ? { sessionId, itemId } : null);
            handleSetAppMode("chat");
            const open = () => quietRetry(() => Promise.resolve(projects.handleOpenMainChat())).then(() => {
                reportChatPlaceFailure(null);
                if (sessionId && itemId) void chatJump.jump(sessionId, itemId);
                return true;
            }, () => { reportChatPlaceFailure({ key: "failure.loadChat", retry: open }); return false; });
            void open();
        });
        // A Needs you item with no chat card opens Tasks › Needs you (evening walk B7).
        const offNeeds = EventsOn("menubar:needs", () => {
            closeOverlaysForSearch();
            handleSetAppMode("tasks");
            setHandoffOpenId(null);
            setTasksFocus((current) => ({ section: "needs", nonce: (current?.nonce ?? 0) + 1 }));
        });
        // Plays in the shared player without bringing the app forward; the
        // popover shows the progress (menu bar 1.0).
        const offBrief = EventsOn("menubar:brief-play", (briefId: string) => {
            void toggleBriefPlayback(briefId).catch(() => toast.error(tToday("audio.failed")));
        });
        // Make my brief now answers like Search's: the wait during cooldown,
        // or Today while it is being made (it was silent before).
        const offMake = EventsOn("menubar:make-brief", (status: string, nextAllowedAt: string) => {
            closeOverlaysForSearch();
            if (status === "cooldown") {
                toast(waitInWords(nextAllowedAt, t));
                return;
            }
            handleSetAppMode("today");
            if (status === "running") toast(t("searchDialog.brief.running"));
            else if (status === "failed") toast.error(t("searchDialog.brief.failed"));
            else toast.success(t("searchDialog.brief.started"));
        });
        const offPause = EventsOn("closed-eyes:changed", (paused: boolean) => {
            if (paused) stopBriefPlayback();
        });
        const offForget = EventsOn("memory:updated", stopBriefPlayback);
        const offStored = EventsOn("brief:stored", stopBriefPlayback);
        const offSources = ["permissions:changed", "sensitive-apps:updated", "background-listening:changed"].map(event => EventsOn(event, stopBriefPlayback));
        return () => { offWaiting(); offNeeds(); offBrief(); offMake(); offPause(); offForget(); offStored(); offSources.forEach(off => off()); };
    }, [chatJump.jump, closeOverlaysForSearch, handleSetAppMode, projects.handleOpenMainChat, t, tChat, tToday]);

    const searchShortcutFor = useCallback(
        (actionId: string) => {
            const combo = keybindings.getCombo(actionId);
            return combo ? comboToDisplayString(combo) : undefined;
        },
        [keybindings.getCombo],
    );
    const searchActions: SearchDialogActions = {
        goToMainChat: () => {
            closeOverlaysForSearch();
            handleSetAppMode("chat");
            void Promise.resolve(projects.handleOpenMainChat()).finally(
                () => requestAnimationFrame(() => focusComposer()),
            );
        },
        openSession: openSessionFromSearch,
        openChatMessage: (sessionId, itemId) => void chatJump.jump(sessionId, itemId),
        openProject: projects.handleSwitchProject,
        openAutomation: (jobId) => {
            closeOverlaysForSearch();
            openUpcomingRef.current(jobId);
        },
        openHandoff: (id) => {
            closeOverlaysForSearch();
            setHandoffReaderBackTarget(appMode === "chat" ? "chat" : "list");
            handleSetAppMode("tasks");
            setHandoffOpenId(id);
        },
        // Library is gone (sidebar handoff, section 3): each page id opens
        // its new home.
        openLibraryPage: (page) => {
            closeOverlaysForSearch();
            switch (page) {
                case "automations":
                    handleSetAppMode("tasks");
                    setHandoffOpenId(null);
                    setTasksFocus((current) => ({ section: "scheduled", nonce: (current?.nonce ?? 0) + 1 }));
                    break;
                case "tasks":
                    setHandoffReaderBackTarget("list");
                    handleSetAppMode("tasks");
                    setHandoffOpenId(null);
                    setHandoffReaderOpen(false);
                    break;
                case "sops":
                    // The list, not a reader a notification asked for earlier.
                    setSopOpenRequest((prev) => ({ id: "", signal: prev.signal }));
                    handleSetAppMode("knowledge");
                    break;
                case "deliverables":
                    handleSetAppMode("tasks");
                    setHandoffOpenId(null);
                    setTasksFocus((current) => ({ section: "all", nonce: (current?.nonce ?? 0) + 1 }));
                    break;
            }
        },
        openSettings: (topic, options) => {
            setKnowledgeOpen(false);
            setMemoryOpen(false);
            closePeople();
            openSettings(topic, options);
        },
        toggleSidebar: () => sidebar.setSidebarCollapsed((v) => !v),
        openToday: () => {
            closeOverlaysForSearch();
            handleSetAppMode("today");
        },
        openActivity: (at) => openActivityFresh(at ? new Date(at) : undefined),
        openKnowHow: (id) => {
            closeOverlaysForSearch();
            if (id) setPendingInsightKnowledgeId(id);
            setKnowledgeOpen(true);
        },
        openAboutYou: (id) => {
            closeOverlaysForSearch();
            if (id) setPendingInsightMemoryId(id);
            setMemoryOpen(true);
        },
        openPerson: (id) => {
            closeOverlaysForSearch();
            setPeopleInitialSelection({ personId: id });
            setPeopleOpen(true);
        },
        openGoal: (id) => {
            closeOverlaysForSearch();
            setGoalOpenRequest((prev) => ({ id, signal: prev.signal + 1 }));
            setMemoryOpen(true);
        },
        // The topic opens beside Today, as a pinned topic does.
        openTopic: (topic) => {
            closeOverlaysForSearch();
            handleSetAppMode("today");
            setTodayTopicRequest((current) => ({ topic, nonce: (current?.nonce ?? 0) + 1 }));
        },
    };

    const setupStep = activeIsMainChat ? mainChatSetup.current : null;
    // Screen Recording turned on during this run needs a reopen; the notice
    // sits above the setup card (or alone once the sequence is finished).
    const screenRecordingRelaunchApp = useScreenRecordingRelaunch(
        activeIsMainChat && !onboardingInFront,
    );
    const mainChatSetupCardEl = setupStep ? (
        <MainChatSetupCard
            step={setupStep}
            position={mainChatSetup.position}
            total={mainChatSetup.total}
            busy={mainChatSetup.busyKey === setupStep.key}
            waiting={mainChatSetup.waitingKey === setupStep.key}
            assistantPicker={
                setupStep.kind === "assistant" ? (
                    <AgentChoice
                        onReady={() => {
                            void mainChatSetup.grant(setupStep);
                        }}
                    />
                ) : undefined
            }
            onGrant={() => {
                void mainChatSetup.grant(setupStep);
            }}
            onNotNow={() => {
                void mainChatSetup.notNow(setupStep);
            }}
            onOpenSettings={() => {
                if (setupStep.kind === "assistant") {
                    openSettings("ai-provider");
                    return;
                }
                void mainChatSetup.openSettings(setupStep);
            }}
            onSaveName={(name, replace) =>
                mainChatSetup.saveName(setupStep, name, replace)
            }
        />
    ) : null;
    const mainChatSetupEl =
        screenRecordingRelaunchApp && activeIsMainChat ? (
            <div className="flex flex-col gap-3">
                <RelaunchNotice app={screenRecordingRelaunchApp} />
                {mainChatSetupCardEl}
            </div>
        ) : (
            mainChatSetupCardEl
        );

    // ── Parser dispatch trace (devtools only) ──
    // The backend emits one parser:dispatch event per snapshot capture
    // describing which app-parser fired (or none) and what it produced.
    // Gated on a localStorage flag so non-developers see no console
    // noise. Toggle from devtools console:
    //   localStorage.setItem('yansu.debug.parsers', '1') // enable
    //   localStorage.removeItem('yansu.debug.parsers')   // disable
    useEffect(() => {
        if (localStorage.getItem("yansu.debug.parsers") !== "1") return;
        const unsub = EventsOn("parser:dispatch", (payload: any) => {
            const tag =
                payload?.parserApp || (payload?.url ? "web-fallback" : "none");
            // eslint-disable-next-line no-console
            console.log(
                `%c[parser]%c ${tag} %c(${payload?.contentType || "–"}) ${payload?.bodyLen ?? 0}b ${payload?.elapsedMs ?? 0}ms`,
                "color:var(--color-ink);font-weight:600",
                "color:inherit;font-weight:600",
                "color:var(--color-muted)",
                payload,
            );
        });
        return () => unsub();
    }, []);

    // ── Handle internal navigation events from chat links ──
    useEffect(() => {
        const unsubAutomation = EventsOn(
            "nav:open-automation",
            (jobId: string) => {
                if (!jobId) return;
                // Only open if the automation exists; otherwise show a toast.
                const found = cronJobs.jobs.some((j) => j.id === jobId);
                if (found) {
                    setAutomationsSelectedJobId(jobId);
                } else {
                    toast.error(
                        "Scheduled automation not found - it may have been removed",
                        { duration: 3000 },
                    );
                }
            },
        );
        return () => unsubAutomation();
    }, [cronJobs.jobs]);

    // ── Handle activity session UUID clicks from chat ──
    // MarkdownContent emits this when a user clicks an activity-session UUID
    // that has been verified to exist (missing UUIDs are silently non-clickable
    // upstream, so we trust the id here without re-checking).
    //
    // `from` is null because chat is the main view, not a surface the page
    // sits on top of: the "Back to X" affordance only makes sense when
    // ActivityPage replaced a memory / knowledge / people / pipeline surface.
    // Hardcoding a from-source here would render a misleading back pill that
    // has nothing to return to.
    useEffect(() => {
        const unsub = EventsOn(
            "nav:open-activity-session",
            (sessionId: string) => {
                if (!sessionId) return;
                openActivityWithSession(sessionId, null);
            },
        );
        return () => unsub();
    }, [openActivityWithSession]);

    // ── AFK handoff digest ──
    // While the window is hidden, handoff proposals accumulate in
    // pendingHandoffNotificationsRef. On return to visible, drain into a
    // single digest toast (≥2 items) or a normal toast (1 item).
    useEffect(() => {
        const drain = () => {
            const queue = pendingHandoffNotificationsRef.current;
            if (queue.length === 0) return;
            if (queue.length === 1) {
                const { chatSessionId, label } = queue[0];
                const niceLabel = label.replace(/^Handoff:\s*/, "");
                toast.success(t("handoffToast.single", { name: niceLabel }), {
                    duration: 8000,
                    action: {
                        label: t("handoffToast.open"),
                        onClick: () =>
                            handleClickGroupItemRef.current?.({
                                itemType: "chat",
                                refId: chatSessionId,
                            }),
                    },
                });
            } else {
                const first = queue[0];
                toast.success(
                    t("handoffToast.digest", { count: queue.length }),
                    {
                        duration: 10000,
                        action: {
                            label: t("handoffToast.view"),
                            onClick: () =>
                                handleClickGroupItemRef.current?.({
                                    itemType: "chat",
                                    refId: first.chatSessionId,
                                }),
                        },
                    },
                );
            }
            pendingHandoffNotificationsRef.current = [];
        };
        const onVis = () => {
            if (
                typeof document !== "undefined" &&
                document.visibilityState === "visible"
            )
                drain();
        };
        if (typeof document !== "undefined") {
            document.addEventListener("visibilitychange", onVis);
        }
        if (typeof window !== "undefined") {
            window.addEventListener("focus", drain);
        }
        return () => {
            if (typeof document !== "undefined") {
                document.removeEventListener("visibilitychange", onVis);
            }
            if (typeof window !== "undefined") {
                window.removeEventListener("focus", drain);
            }
        };
    }, []);


    const [chatWideMode] = useReadingWidth();

    // ── Load older messages (reverse infinite scroll) ──
    const mapSessionInProject = useCallback(
        (
            p: import("@/types").Project,
            sessionId: string,
            updater: (
                s: import("@/types").Session,
            ) => import("@/types").Session,
        ) => ({
            ...p,
            workspaces: p.workspaces.map((w) => ({
                ...w,
                sessions: w.sessions.map((s) =>
                    s.localId === sessionId ? updater(s) : s,
                ),
            })),
        }),
        [],
    );

    const loadSessionMore = useCallback(async (session: import('@/types').Session | null, project: import('@/types').Project | null) => {
        if (!session || !project || session.loadingMore || !session.hasMore)
            return;

        // Mark loading
        projects.updateProject(project.id, (p) =>
            mapSessionInProject(p, session.localId, (s) => ({
                ...s,
                loadingMore: true,
            })),
        );

        try {
            const page = await loadStreamItemsPageWithOpenAnswerDelivery(
                session.localId,
                session.nextCursor,
                50,
            );
            const olderItems: StreamItem[] = (page.items ?? []).map(
                fromStreamItemData,
            );

            projects.updateProject(project.id, (p) =>
                mapSessionInProject(p, session.localId, (s) => {
                    // Deduplicate: trimming preserves user messages that
                    // may also appear in the older page loaded from DB.
                    const existingIds = new Set(s.items.map((i) => i.id));
                    const unique = olderItems.filter(
                        (i) => !existingIds.has(i.id),
                    );
                    return {
                        ...s,
                        items: [...unique, ...s.items],
                        streamItemCount: Math.max(
                            s.streamItemCount ?? 0,
                            unique.length + s.items.length,
                        ),
                        itemsLoaded: true,
                        itemsLoading: false,
                        hasMore: page.hasMore,
                        nextCursor: page.nextCursor,
                        loadingMore: false,
                    };
                }),
            );
        } catch (err) {
            logPersistError(err);
            projects.updateProject(project.id, (p) =>
                mapSessionInProject(p, session.localId, (s) => ({
                    ...s,
                    loadingMore: false,
                })),
            );
        }
    }, [projects.updateProject, mapSessionInProject]);
    const handleLoadMore = useCallback(() => loadSessionMore(projects.activeSession, projects.activeProject), [loadSessionMore, projects.activeSession, projects.activeProject]);
    const sharedMainChat = useMemo(() => findMainChatSession(projects.projects), [projects.projects]);
    const loadMainChatMore = useCallback(() => loadSessionMore(sharedMainChat?.session ?? null, sharedMainChat?.project ?? null), [loadSessionMore, sharedMainChat]);
    const hydrateMainChat = useCallback((sessionId: string, items: StreamItem[], hasMore: boolean, cursor: number) => {
        const match = findMainChatSession(projectsRef.current);
        if (!match || match.session.localId !== sessionId) return;
        projects.updateProject(match.project.id, (project) => mapSessionInProject(project, sessionId, (session) => session.itemsLoaded ? session : {
            ...session, items, itemsLoaded: true, itemsLoading: false, hasMore, nextCursor: cursor,
        }));
    }, [projects.updateProject, mapSessionInProject]);

    // ── Search and commands (Command K and the sidebar's Search) ──
    const [searchOpen, setSearchOpen] = useState(false);
    useEffect(() => { if (searchOpen) beginPageSwitch('search'); }, [searchOpen]);

    // ── QuickChat ──
    // Holds the prompt produced by the QuickChat floating panel (macOS
    // NSPanel fallback when the Spotlight helper isn't running) until
    // the standalone-chat session is settled and ready to receive it.
    // Component state (not a ref) so dropping a prompt always triggers
    // a render — the watcher effect below needs to re-run even when
    // handleNewSession reuses an existing empty session.
    const [pendingQuickChatPrompt, setPendingQuickChatPrompt] = useState<{
        prompt: string;
        bundleId: string;
        display?: string;
    } | null>(null);
    // Stable refs for the latest projects/composer/appMode callbacks, so
    // the EventsOn handler (registered once at mount) always calls the
    // current hook closures instead of the ones captured on first render.
    const composerSendImmediateRef = useRef(composer.sendImmediate);
    composerSendImmediateRef.current = composer.sendImmediate;
    const composerSetPromptRef = useRef(composer.replacePrompt);
    composerSetPromptRef.current = composer.replacePrompt;
    const projectsRefForQuickChat = useRef(projects);
    projectsRefForQuickChat.current = projects;

    useEffect(() => {
        // quickChat:invoke fires from the Go side when a Quick Chat
        // trigger has to be handled in the renderer rather than by the
        // Spotlight helper. In practice this only happens on Windows
        // when the helper isn't running (typical in dev): the Go side
        // brings the main window forward and emits this event with
        // nativeWindow=false so we can drop the user into a fresh
        // standalone chat. macOS sets nativeWindow=true because the
        // native NSPanel is handling its own UI — we just ignore.
        const offInvoke = EventsOn(
            "quickChat:invoke",
            (payload: { nativeWindow?: boolean }) => {
                if (payload?.nativeWindow) return;
                const proj = projectsRefForQuickChat.current;
                proj.handleSwitchProject(STANDALONE_CHAT_PROJECT_ID);
                handleSetAppModeRef.current("chat");
                void proj
                    .handleNewSession(STANDALONE_CHAT_PROJECT_ID)
                    .catch(() => {});
            },
        );
        const offSubmit = EventsOn(
            "quickChat:submit",
            async (
                payload:
                    | {
                          prompt?: string;
                          frontmostBundleId?: string;
                          frontmostAppName?: string;
                      }
                    | string,
            ) => {
                // Tolerate the older string payload shape so a stale
                // renderer doesn't completely break after a backend update.
                const data =
                    typeof payload === "string"
                        ? { prompt: payload }
                        : (payload ?? {});
                const prompt = (data.prompt ?? "").trim();
                if (!prompt) return;
                const bundleId = (data.frontmostBundleId ?? "").trim();
                const proj = projectsRefForQuickChat.current;
                proj.handleSwitchProject(STANDALONE_CHAT_PROJECT_ID);
                handleSetAppModeRef.current("chat");
                try {
                    // handleNewSession either creates a brand-new session
                    // or reuses an existing empty one; awaiting it ensures
                    // the project state has stabilised before we queue the
                    // prompt for send.
                    await proj.handleNewSession(STANDALONE_CHAT_PROJECT_ID);
                } catch {
                    // fall through — the watcher will retry on any
                    // subsequent render that satisfies its conditions.
                }
                // The waiter effect below will pick this up and, once the
                // standalone-chat session is active and idle, capture fresh
                // focused-app context for the LIVE session.localId before
                // sending. Doing the capture there (not here) lets us key
                // the stash by the actual sessionLocalId React commits, not
                // the stale value visible in this closure.
                setPendingQuickChatPrompt({ prompt, bundleId });
            },
        );
        return () => {
            if (typeof offInvoke === "function") offInvoke();
            if (typeof offSubmit === "function") offSubmit();
        };
        // eslint-disable-next-line react-hooks/exhaustive-deps
    }, []);

    // Listen for the "Install via AI" affordance from Settings → MCP
    // Servers → Registry. The Settings dialog dispatches a window
    // CustomEvent when the user clicks Install via AI on a third-party
    // row; we close Settings, switch to a fresh general-chat session,
    // and queue the prompt for send. Reuses pendingQuickChatPrompt so
    // the same waiter effect below sends it once the session settles.
    useEffect(() => {
        const onInstall = (event: Event) => {
            const detail = (event as CustomEvent<{ prompt?: string; display?: string }>).detail;
            const prompt = (detail?.prompt ?? "").trim();
            const display = detail?.display?.trim() || undefined;
            if (!prompt) return;
            settings.setSettingsOpen(false);
            const proj = projectsRefForQuickChat.current;
            proj.handleSwitchProject(STANDALONE_CHAT_PROJECT_ID);
            handleSetAppModeRef.current("chat");
            void (async () => {
                try {
                    await proj.handleNewSession(STANDALONE_CHAT_PROJECT_ID);
                } catch {
                    // fall through — the watcher will retry on later renders.
                }
                setPendingQuickChatPrompt({ prompt, bundleId: "", display });
            })();
        };
        window.addEventListener("mcp:install-via-ai", onInstall);
        return () =>
            window.removeEventListener("mcp:install-via-ai", onInstall);
    }, [settings.setSettingsOpen]);

    // Onboarding can end with the user's first request. Open it in a fresh
    // General Chat session and queue it on pendingQuickChatPrompt, so the
    // same waiter effect below sends it once that session is active and idle
    // (by then the classic layout has replaced the onboarding screen). When
    // no session can be opened, the request goes into the composer instead
    // of being dropped, so the user can send it themselves.
    const startOnboardingFirstTask = useCallback(
        (prompt: string) => {
            const proj = projectsRefForQuickChat.current;
            proj.handleSwitchProject(STANDALONE_CHAT_PROJECT_ID);
            handleSetAppModeRef.current("chat");
            void (async () => {
                let sessionId: string | null = null;
                try {
                    sessionId = await proj.handleNewSession(
                        STANDALONE_CHAT_PROJECT_ID,
                        true,
                    );
                } catch {
                    sessionId = null;
                }
                if (!sessionId) {
                    composerSetPromptRef.current(prompt);
                    requestAnimationFrame(() => focusComposer());
                    toast.error(t("onboardingFirstTask.startFailed"));
                    return;
                }
                setPendingQuickChatPrompt({ prompt, bundleId: "" });
            })();
        },
        [t, focusComposer],
    );

    // Send any pending QuickChat prompt the moment the standalone-chat
    // session is active and idle. We rely on handleNewSession having
    // guaranteed the resulting session is either brand-new or empty, so we
    // just need to wait for !streaming on the right project.
    //
    // Right before sending, attach focused-app context for THIS session
    // (Go side captures fresh AX/OCR for the bundleID and stashes it under
    // sess.localId; the composePrompt focused-app producer pops it on the
    // first prompt). The await blocks the send by ~hundreds of ms — this is
    // intentional and masked by the chat transition the user just saw.
    useEffect(() => {
        if (!pendingQuickChatPrompt) return;
        if (projects.activeProject?.id !== STANDALONE_CHAT_PROJECT_ID) return;
        const sess = projects.activeSession;
        if (!sess) return;
        if (sess.streaming) return;
        const { prompt, bundleId, display } = pendingQuickChatPrompt;
        setPendingQuickChatPrompt(null);
        const sessionLocalId = sess.localId;
        void (async () => {
            if (bundleId && sessionLocalId) {
                try {
                    await AttachQuickChatContext(sessionLocalId, bundleId);
                } catch {
                    // Non-fatal — prompt still sends, just without context.
                }
            }
            await composerSendImmediateRef.current(prompt, display);
        })();
    }, [
        pendingQuickChatPrompt,
        projects.activeProject?.id,
        projects.activeSession?.localId,
        projects.activeSession?.streaming,
    ]);

    // ── Keyboard shortcuts (dynamic via keybindings) ──
    useEffect(() => {
        const onKeyDown = (e: KeyboardEvent) => {
            const actionId = keybindings.findMatchingAction(e);
            if (!actionId) return;

            switch (actionId) {
                case "command-palette":
                    e.preventDefault();
                    setSearchOpen((v) => !v);
                    break;
                case "toggle-sidebar":
                    e.preventDefault();
                    sidebar.setSidebarCollapsed((v) => !v);
                    break;
                case "open-settings":
                    e.preventDefault();
                    openSettings();
                    break;
                case "keyboard-shortcuts":
                    e.preventDefault();
                    openSettings("keybindings");
                    break;
                case "new-chat": {
                    // Don't create a new chat while settings is open.
                    if (settings.settingsOpen) break;
                    e.preventDefault();
                    const projectId =
                        projects.activeProject?.id ??
                        STANDALONE_CHAT_PROJECT_ID;
                    void projects.handleNewSession(projectId);
                    // Switch to chat view if currently showing an automation.
                    handleSetAppMode("chat");
                    break;
                }
                case "replay-onboarding":
                    // A developer shortcut: bound only while Developer tools
                    // are on. Users rerun setup from About.
                    if (!devToolsUnlocked) break;
                    e.preventDefault();
                    handleReplayOnboarding();
                    break;
            }
        };
        window.addEventListener("keydown", onKeyDown);
        return () => window.removeEventListener("keydown", onKeyDown);
    }, [
        keybindings.findMatchingAction,
        openSettings,
        projects.activeProject?.id,
        projects.handleNewSession,
        handleReplayOnboarding,
        handleSetAppMode,
        devToolsUnlocked,
    ]);

    const hasActiveConversation = !!projects.activeSession;

    // The automation whose detail fills the right main content column, when one
    // is selected from the sidebar. Mutually exclusive with the chat view.
    const selectedAutomationJob = useMemo(
        () =>
            automationsSelectedJobId
                ? (cronJobs.jobs.find(
                      (j) => j.id === automationsSelectedJobId,
                  ) ?? null)
                : null,
        [automationsSelectedJobId, cronJobs.jobs],
    );

    // Resolve the effective model: if the session saved a model that is no
    // longer in the available list (e.g. env var changed the Claude model),
    // snap to the current default so the dropdown never shows blank.
    const effectiveModel = useMemo(() => {
        // Sends use the Settings choice (K9), never a chat's saved model.
        const m = model;
        if (executorGroups.length === 0) return m;
        const allIds = new Set(
            executorGroups.flatMap((g) => g.models.map((x) => x.id)),
        );
        if (allIds.has(m)) return m;
        return executorGroups[0]?.default || model;
    }, [model, executorGroups]);

    // Sync stale session model: if the session saved a model that is no longer
    // available (e.g. env vars changed), persist the snapped default back so
    // the send paths (useComposerInput, handleEditItem) use the correct model.
    useEffect(() => {
        const raw = model;
        if (effectiveModel !== raw && projects.activeSession) {
            projects.handleModelChange(effectiveModel);
        }
        // eslint-disable-next-line react-hooks/exhaustive-deps
    }, [effectiveModel, projects.activeSession?.localId]);


    // --- Sidebar group helpers ---
    const handleClickGroupItem = useCallback(
        async (item: { itemType: string; refId: string }) => {
            if (item.itemType === "chat") {
                // Find the project that owns this session and switch to it.
                for (const p of projects.projects) {
                    for (const w of p.workspaces) {
                        const s = w.sessions.find(
                            (s: { localId: string }) =>
                                s.localId === item.refId,
                        );
                        if (s) {
                            handleSetAppMode("chat");
                            projects.handleSwitchSession(p.id, s.localId);
                            return;
                        }
                    }
                }
                // Session not in frontend state — hydrate it from DB into the
                // active project so the user can view it (e.g., transcript
                // sessions created asynchronously after a meeting).
                const activeProj = projects.activeProject;
                if (!activeProj || !activeProj.workspaces?.[0]) {
                    pendingNavRef.current = item;
                    return;
                }
                try {
                    const page = await loadStreamItemsPageWithOpenAnswerDelivery(item.refId, -1, CHAT_FIRST_PAGE_SIZE);
                    const items = (page.items ?? []).map(fromStreamItemData);
                    const ws = activeProj.workspaces[0];
                    const hydratedSession = {
                        ...createSession(
                            "claude-haiku-4-5-20251001",
                            ws.id,
                            "Transcript",
                        ),
                        localId: item.refId,
                        items,
                        streamItemCount: items.length,
                        itemsLoaded: true,
                        itemsLoading: false,
                    };
                    projects.updateProject(activeProj.id, (p: any) => ({
                        ...p,
                        workspaces: p.workspaces.map((w: any) =>
                            w.id === ws.id
                                ? {
                                      ...w,
                                      sessions: [
                                          ...(w.sessions || []),
                                          hydratedSession,
                                      ],
                                  }
                                : w,
                        ),
                    }));
                    handleSetAppMode("chat");
                    projects.handleSwitchSession(activeProj.id, item.refId);
                } catch (err) {
                    console.error(
                        "[group-item] failed to hydrate session:",
                        err,
                    );
                }
            }
            if (item.itemType === "automation") {
                setAutomationsSelectedJobId(item.refId);
            }
        },
        [
            projects.projects,
            projects.activeProject,
            projects.updateProject,
            projects.handleSwitchSession,
            handleSetAppMode,
        ],
    );

    // Pending navigation: if a notification click arrives before projects load,
    // store the target here and consume it when projects become available.
    const pendingNavRef = useRef<{ itemType: string; refId: string } | null>(
        null,
    );
    useEffect(() => {
        if (!pendingNavRef.current) return;
        if (!projects.activeProject || !projects.activeProject.workspaces?.[0])
            return;
        const item = pendingNavRef.current;
        pendingNavRef.current = null;
        handleClickGroupItem(item);
    }, [projects.activeProject, handleClickGroupItem]);

    // Stable ref so early useEffects (notification:clicked, report lines)
    // can call handleClickGroupItem without stale closures.
    const handleClickGroupItemRef = useRef(handleClickGroupItem);
    handleClickGroupItemRef.current = handleClickGroupItem;

    if (onboardingLoading) {
        return (
            <div className="patina-ground min-h-screen bg-background">
                <div className="min-h-screen flex items-center justify-center text-muted text-sm animate-pulse-slow">
                    {t("onboarding.preparing")}
                </div>
            </div>
        );
    }

    const showOnboarding = !skipOnboardingSession &&
          !onboardingState.completed &&
          !onboardingState.neverShowAgain;

    if (showOnboarding) {
        return (
            <OnboardingScreen
                state={onboardingState}
                onStateChange={persistOnboardingState}
                onFinish={(firstTaskPrompt?: string) => {
                    const prompt = (firstTaskPrompt ?? "").trim();
                    handleOnboardingFinish(Boolean(prompt));
                    if (prompt) startOnboardingFirstTask(prompt);
                }}
                onSkip={handleOnboardingSkipSession}
            />
        );
    }

    return (
        <div ref={layoutRef} className="h-screen flex flex-row overflow-hidden">
            <WhatIsoformSeesSheet open={seesOpen} onOpenChange={setSeesOpen} />
            {/* Settings dialog — rendered on top of the main view so terminals stay mounted */}
            <Dialog
                open={settings.settingsOpen}
                onOpenChange={(open) => {
                    if (!open) {
                        settings.setSettingsOpen(false);
                        setSettingsInitialTopic(undefined);
                        refreshComposerCapabilities();
                    }
                }}
            >
                <RetainedDialogContent
                    open={settings.settingsOpen}
                    retain={activityOpenedFrom === "pipeline"}
                    page="settings"
                    className="p-0 w-[calc(100vw-16px)] sm:w-[90vw] max-w-[1040px] h-[88vh] max-h-none overflow-hidden settings-overlay"
                    onEscapeKeyDown={(event) => {
                        if (debugDetailOwnsEscape()) {
                            event.preventDefault();
                        }
                    }}
                >
                    <DialogTitle className="sr-only">
                        {t("settingsDialog.title")}
                    </DialogTitle>
                    <DialogDescription className="sr-only">
                        {t("settingsDialog.description")}
                    </DialogDescription>
                    <DialogClose asChild>
                        <button
                            type="button"
                            aria-label={t("settingsDialog.close")}
                            className="absolute top-4 right-4 z-50 inline-flex size-8 items-center justify-center rounded-md text-muted leading-none cursor-pointer hover:bg-ink/[0.08] transition-colors [&>svg]:block"
                        >
                            <X className="h-4 w-4" />
                        </button>
                    </DialogClose>
                    <Suspense fallback={<SettingsLoadingShell showDevTools={devToolsUnlocked} />}>
                        <LazySettingsPage
                            active={settings.settingsOpen}
                            onClose={() => {
                                settings.setSettingsOpen(false);
                                setSettingsInitialTopic(undefined);
                                refreshComposerCapabilities();
                            }}
                            onJumpToActivity={openActivityWithHighlightedFrames}
                            onReplayOnboarding={handleReplayOnboarding}
                            vimMode={settings.vimMode}
                            onVimModeChange={settings.setVimMode}
                            initialTopic={settingsInitialTopic}
                            initialOptions={settingsInitialOptions}
                            keybindings={keybindings}
                            devToolsUnlocked={devToolsUnlocked}
                            onOpenMemory={() => {
                                settings.setSettingsOpen(false);
                                setSettingsInitialTopic(undefined);
                                refreshComposerCapabilities();
                                setMemoryOpen(true);
                            }}
                            onOpenPeople={() => {
                                settings.setSettingsOpen(false);
                                setSettingsInitialTopic(undefined);
                                refreshComposerCapabilities();
                                setPeopleInitialSelection(undefined);
                                setPeopleOpen(true);
                            }}
                            onOpenProfile={() => {
                                settings.setSettingsOpen(false);
                                setSettingsInitialTopic(undefined);
                                refreshComposerCapabilities();
                                setPeopleInitialSelection("self");
                                setPeopleOpen(true);
                            }}
                        />
                    </Suspense>
                </RetainedDialogContent>
            </Dialog>
            {/* Confirm dialog for deleting session linked to automations */}
            <ConfirmDialog
                open={pendingDeleteSession !== null}
                onOpenChange={(open) => {
                    if (!open) setPendingDeleteSession(null);
                }}
                title="Delete Chat"
                description={
                    pendingDeleteSession
                        ? (() => {
                              const totalLinked =
                                  pendingDeleteSession.automationNames.length;
                              return (
                                  <span>
                                      <span className="block">
                                          {t("deleteChatDialog.linkedTo")}
                                      </span>
                                      <span className="block mt-2 mb-3 pl-1 space-y-1">
                                          {pendingDeleteSession.automationNames.map(
                                              (name) => (
                                                  <span
                                                      key={`automation-${name}`}
                                                      className="block"
                                                  >
                                                      <span className="text-muted">
                                                          •
                                                      </span>{" "}
                                                      <strong>{name}</strong>{" "}
                                                      <span className="text-muted">
                                                          {t(
                                                              "deleteChatDialog.automation",
                                                          )}
                                                      </span>
                                                  </span>
                                              ),
                                          )}
                                      </span>
                                      <span className="block">
                                          {t(
                                              "deleteChatDialog.removeAbilityWarning",
                                              { count: totalLinked },
                                          )}
                                      </span>
                                  </span>
                              );
                          })()
                        : ""
                }
                confirmLabel="Delete"
                variant="danger"
                onConfirm={() => {
                    if (pendingDeleteSession) {
                        doCloseSession(
                            pendingDeleteSession.projectId,
                            pendingDeleteSession.sessionId,
                        );
                    }
                }}
            />
            <Sidebar
                projects={projects.projects}
                activeProjectId={projects.activeProjectId}
                collapsed={sidebar.sidebarCollapsed}
                width={sidebar.sidebarWidth}
                onToggleCollapse={() => sidebar.setSidebarCollapsed((v) => !v)}
                onLogoClick={envSwitch.handleLogoClick}
                isStaging={envSwitch.showStagingBadge}
                onOpenSearch={() => setSearchOpen(true)}
                shortcutFor={searchShortcutFor}
                onOpenSettings={() => openSettings()}
                onOpenProfile={() => openSettings("profile")}
                onOpenShortcuts={() => openSettings("keybindings")}
                onOpenHelp={() => openSettings("about")}
                onOpenObservationSettings={() => openSettings("activity")}
                waitingCount={tasksNeedsYouCount}
                chatUnread={mainChatUnread || (mainChatWaitingCount ?? 0) > 0}
                onOpenActivity={() => {
                    // The sidebar row is pure navigation: already on the page,
                    // it does nothing (unlike the deep links, which re-open on
                    // a specific day).
                    if (appMode !== "activity") openActivityFresh();
                }}
                activityOpen={appMode === "activity"}
                onOpenKnowledge={() => setKnowledgeOpen(true)}
                onOpenMemory={() => setMemoryOpen(true)}
                onOpenPeople={() => setPeopleOpen(true)}
                onOpenToday={() => handleSetAppMode("today")}
                todayOpen={appMode === "today"}
                pinnedTopics={sidebarTopics.topics}
                activeTopicId={appMode === "today" ? activeTodayTopicId : null}
                onOpenTopic={handleOpenSidebarTopic}
                onUnpinTopic={handleUnpinSidebarTopic}
                onOpenTasks={() => {
                    handleSetAppMode("tasks");
                    setHandoffOpenId(null);
                }}
                tasksOpen={appMode === "tasks"}
                appMode={appMode}
                onNewChat={() => {
                    void projects.handleNewSession(projects.activeProject?.id ?? STANDALONE_CHAT_PROJECT_ID);
                    handleSetAppMode("chat");
                }}
                onOpenMainChat={() => {
                    handleSetAppMode("chat");
                    void Promise.resolve(projects.handleOpenMainChat()).finally(
                        () => requestAnimationFrame(() => focusComposer()),
                    );
                }}
                onSwitchSession={(projectId, sessionId) => {
                    handleSetAppMode("chat");
                    projects.handleSwitchSession(projectId, sessionId);
                    projects.markSessionRead(sessionId);
                }}
                onCloseSession={handleCloseSession}
                onTogglePin={projects.handleTogglePin}
                onRenameSession={projects.handleRenameSession}
                onMarkSessionRead={projects.markSessionRead}
                onMarkSessionUnread={projects.markSessionUnread}
                onForkSession={handleForkSessionFromSidebar}
                unreadSessionIds={projects.unreadSessionIds}
            />

            <main className="patina-ground flex-1 min-w-0 flex flex-col h-full bg-background">
                {/* Drag region for window dragging. Matches sidebar's macOS
                    traffic-light strip (h-[44px]) so the header-row bottom
                    border aligns with the sidebar's header border. */}
                <div
                    className="h-[44px] shrink-0 flex items-center"
                    style={
                        { "--wails-draggable": "drag" } as React.CSSProperties
                    }
                >
                    {appMode === "tasks" && handoffReaderOpen && (
                        <button
                            className={`inline-flex items-center gap-1 px-2 py-1 rounded-md text-[13px] text-muted hover:text-ink hover:bg-border/50 transition-colors cursor-pointer ${sidebar.sidebarCollapsed ? "ml-1" : "ml-2"}`}
                            style={
                                {
                                    "--wails-draggable": "no-drag",
                                } as React.CSSProperties
                            }
                            onClick={() => setHandoffBackSignal((v) => v + 1)}
                            title={t("handoffsPage.back")}
                        >
                            <ArrowLeft className="h-4 w-4" />
                            {t("handoffsPage.back")}
                        </button>
                    )}
                    {appMode === "knowledge" && sopReaderOpen && (
                        <button
                            className={`inline-flex items-center gap-1 px-2 py-1 rounded-md text-[13px] text-muted hover:text-ink hover:bg-border/50 transition-colors cursor-pointer ${sidebar.sidebarCollapsed ? "ml-1" : "ml-2"}`}
                            style={
                                {
                                    "--wails-draggable": "no-drag",
                                } as React.CSSProperties
                            }
                            onClick={() => setSopBackSignal((v) => v + 1)}
                            title={t("sopsPage.back")}
                        >
                            <ArrowLeft className="h-4 w-4" />
                            {t("sopsPage.back")}
                        </button>
                    )}
                    {/* Terminal toggle button hidden — feature not yet ready for users */}
                </div>

                <ComposerVoiceContext.Provider value={panelVoice}>
                    <RetainedPage page="today" active={appMode === 'today' && !selectedAutomationJob}>
                        <TodayPage
                            active={appMode === "today" && !selectedAutomationJob}
                            onContentReady={() => setTodayContentReady(true)}
                            handlers={briefHandlers}
                            mainChatSession={sharedMainChat?.session}
                            onLoadMainChatMore={loadMainChatMore}
                            onHydrateMainChat={hydrateMainChat}
                            hideMainChatRoutine={mainChatView.hideRoutine}
                            onOpenReportTarget={handleOpenReportTarget}
                            wideMode={chatWideMode}
                            topicRequest={todayTopicRequest}
                            briefRequest={todayBriefRequest}
                            onActiveTopicChange={setActiveTodayTopicId}
                            onTopicRequestDone={() => setTodayTopicRequest(null)}
                        />
                    </RetainedPage>
                </ComposerVoiceContext.Provider>

                {/* The You pages stay mounted after their first visit (like
                    Today and Chat), so a jump to Activity and back keeps
                    their lists, scroll and open entry. */}
                <RetainedPage page="about-you" active={appMode === 'memory' && !selectedAutomationJob}>
                    <MemoryPage
                        onOpenActivitySession={(id) =>
                            openActivityWithSession(id, "memory")
                        }
                        initialSelectedId={pendingInsightMemoryId}
                        escapeRef={memoryEscapeRef}
                        active={memoryOpen}
                        devToolsUnlocked={devToolsUnlocked}
                        onTalkAboutGoal={(goal, prompt) =>
                            briefHandlers.onStartChat(prompt, { sourceKind: "purpose", sourceId: goal.id })
                        }
                        openGoalId={goalOpenRequest.id || null}
                        openGoalSignal={goalOpenRequest.signal}
                        onOpenGoalHandled={() =>
                            setGoalOpenRequest((prev) => ({ id: "", signal: prev.signal }))
                        }
                    />
                </RetainedPage>
                <RetainedPage page="know-how" active={appMode === 'knowledge' && !selectedAutomationJob}>
                    <DevToolsContext.Provider value={devToolsUnlocked}>
                    <ActivityOpenChatContext.Provider value={() => { setKnowledgeOpen(false); openMainChatRef.current(); }}>
                    <KnowledgePage
                        active={knowledgeOpen}
                        onOpenActivitySession={(id) =>
                            openActivityWithSession(id, "knowledge")
                        }
                        initialSelectedId={pendingInsightKnowledgeId}
                        escapeRef={knowledgeEscapeRef}
                        backSignal={sopBackSignal}
                        onReaderOpenChange={setSopReaderOpen}
                        openPlaybookId={sopOpenRequest.id || null}
                        openPlaybookSignal={sopOpenRequest.signal}
                        onOpenPlaybookHandled={() =>
                            setSopOpenRequest((prev) => ({ id: "", signal: prev.signal }))
                        }
                        onRunPlaybook={async (result) => {
                            setSopReaderOpen(false);
                            handleSetAppMode("chat");
                            await projects.handleStartSopExecution(result);
                            projects.markSessionRead(result.sessionId);
                        }}
                    />
                    </ActivityOpenChatContext.Provider>
                    </DevToolsContext.Provider>
                </RetainedPage>
                <RetainedPage page="people" active={appMode === 'people' && !selectedAutomationJob}>
                    <PeoplePage
                        active={peopleOpen}
                        escapeRef={peopleEscapeRef}
                        initialSelection={peopleInitialSelection}
                        devToolsUnlocked={devToolsUnlocked}
                        onOpenActivitySession={(
                            id,
                            anchorTimestamp,
                            principalName,
                        ) => {
                            // The People dialog closes so the Activity page
                            // shows; its "Back to People" button reopens
                            // People. Same pattern as Memory / Knowledge.
                            openActivityWithSession(
                                id,
                                "people",
                                anchorTimestamp,
                                principalName,
                            );
                        }}
                    />
                </RetainedPage>

                {selectedAutomationJob ? (
                    <AutomationDetailPane
                        key={selectedAutomationJob.id}
                        job={selectedAutomationJob}
                        wideMode={chatWideMode}
                        onClose={() => setAutomationsSelectedJobId(null)}
                        onToggleEnabled={cronJobs.toggleEnabled}
                        onDelete={(id, opts) =>
                            cronJobs.deleteJob(id, opts).then((deleted) => {
                                if (!deleted) return false;
                                sidebarGroupsHook.removeItemsByRef(
                                    "automation",
                                    id,
                                );
                                setAutomationsSelectedJobId(null);
                                return true;
                            })
                        }
                        onRunNow={cronJobs.runNow}
                        onUpdate={cronJobs.updateJob}
                        loadRuns={cronJobs.loadRuns}
                        loadRunLogs={cronJobs.loadRunLogs}
                        sourceSessionLabel={resolveAutomationSourceSessionLabel(
                            selectedAutomationJob,
                        )}
                        onNavigateToSourceSession={() =>
                            handleNavigateToAutomationSourceSession(
                                selectedAutomationJob,
                            )
                        }
                        onOpenChatMessage={(sessionId, itemId) => {
                            void chatJump.jump(sessionId, itemId);
                            handleSetAppMode("chat");
                        }}
                    />
                ) : appMode === "activity" ? (
                    // A full main-view page (backlog item 67), remounted on
                    // every open request so a fresh day/session applies even
                    // while the page is already showing.
                    <div className="flex min-h-0 min-w-0 flex-1">
                    <Suspense fallback={null}>
                        <LazyActivityPage
                            key={activityNonce}
                        initialSessionId={activityInitialSessionId}
                        initialDate={activityInitialDate}
                        noticedFocus={activityNoticedFocus}
                        onNoticedFocusConsumed={(focus) => setActivityNoticedFocus((current) => current === focus ? null : current)}
                        initialThreadId={activityInitialThreadId}
                        onInitialThreadConsumed={(threadId) => setActivityInitialThreadId((current) => current === threadId ? null : current)}
                        cronJobs={cronJobs}
                        section={activitySection}
                        onSectionChange={setActivitySection}
                        backButton={
                            activityOpenedFrom
                                ? {
                                      label: t("activityBack.label", {
                                          target: t(
                                              `activityBack.${activityOpenedFrom}`,
                                          ),
                                      }),
                                      onClick: handleActivityBack,
                                  }
                                : undefined
                        }
                        highlightedFrames={activityHighlightedFrames}
                        highlightedPipelineRunId={
                            activityHighlightedPipelineRunId
                        }
                        restrictToSessionIds={activityRestrictToSessionIds}
                        initialEventTimestamp={activityInitialEventTimestamp}
                        highlightedSenderName={activityHighlightedSenderName}
                        onStartChat={(prompt, context) => briefHandlers.onStartChat(prompt, context)}
                        onOpenChat={() => openMainChatRef.current()}
                        yourWeekHandlers={briefHandlers}
                        onYourWeekCard={(briefId, card) => briefHandlers.onStartChat(card.message, todayBriefCardRef(briefId, card.id, card.message))}
                    />
                    </Suspense>
                    </div>
                ) : appMode === "tasks" ? (
                    <TasksPage
                        openHandoffId={handoffOpenId}
                        focus={tasksFocus}
                        upcoming={upcoming}
                        onSelectSchedule={(jobId) => setAutomationsSelectedJobId(jobId)}
                        onDeleteSchedule={(id) =>
                            cronJobs.deleteJob(id, { onRefused: () => undefined }).then((deleted) => {
                                if (deleted) sidebarGroupsHook.removeItemsByRef("automation", id);
                                return deleted;
                            })
                        }
                        onOpenTodayBrief={(briefId) => openTodayBrief(briefId)}
                        onChangeBriefTimes={() => openSettings("iphone")}
                        onStartInPlace={async (result) => {
                            const started = await projects.handleStartHandoffExecution(result);
                            if (started) projects.markSessionRead(result.sessionId);
                            return started;
                        }}
                        backSignal={handoffBackSignal}
                        onReaderOpenChange={setHandoffReaderOpen}
                        wideMode={chatWideMode}
                        onStartHandoff={async (result, options) => {
                            const started =
                                await projects.handleStartHandoffExecution(
                                    result,
                                    options,
                                );
                            if (!started) {
                                return false;
                            }
                            setHandoffReaderBackTarget("list");
                            setHandoffReaderOpen(false);
                            handleSetAppMode("chat");
                            projects.markSessionRead(result.sessionId);
                            setChatBackToHandoffs(true);
                            return true;
                        }}
                        onOpenPlaybook={(id) => openPlaybookRef.current(id)}
                        onOpenChatMessage={(sessionId, itemId) => {
                            void chatJump.jump(sessionId, itemId);
                            setHandoffReaderBackTarget("list");
                            setHandoffReaderOpen(false);
                            handleSetAppMode("chat");
                        }}
                    />
                ) : null}
                <RetainedPage page="chat" active={appMode === 'chat' && !selectedAutomationJob}>
                    <>


                        {error && (
                            <div
                                className={`mx-8 mt-4 px-4 py-3 rounded-xl bg-error-bg text-error border border-error-border shrink-0 self-center w-full${chatWideMode ? "" : " max-w-3xl"}`}
                            >
                                {error}
                            </div>
                        )}

                        {/* Chat + Context panel — horizontal split */}
                        <div className="flex-1 min-h-0 flex flex-row">
                            {(() => {
                                const composerEl = (
                                    <>
                                    {aiReady === false && <NoAILine />}
                                    <Composer
                                        placeholder={activeIsMainChat && firstDay.firstDays ? t("firstDay.placeholder") : undefined}
                                        prompt={composer.prompt}
                                        todayPreview={composer.todayPreview}
                                        onPromptChange={composer.setPrompt}
                                        onSend={composer.sendPrompt}
                                        onCancel={composer.handleCancel}
                                        onKeyDown={composer.handleKeyDown}
                                        executorGroups={executorGroups}
                                        streaming={
                                            projects.activeSession?.streaming ??
                                            false
                                        }
                                        ready={streaming.status.ready}
                                        hasProject={hasActiveConversation}
                                        contextError={
                                            projects.activeProject?.context
                                                ?.error
                                        }
                                        images={composer.images}
                                        onImagesChange={composer.setImages}
                                        files={composer.files}
                                        onFilesChange={composer.setFiles}
                                        executorType={getExecutorType(
                                            effectiveModel,
                                        )}
                                        awaitingPlanApproval={
                                            projects.activeSession
                                                ?.awaitingPlanApproval ?? false
                                        }
                                        queuedMessages={composer.queuedMessages}
                                        onRemoveQueuedMessage={
                                            composer.removeQueuedMessage
                                        }
                                        onEditQueuedMessage={
                                            composer.editQueuedMessage
                                        }
                                        availableCommands={
                                            projects.activeSession
                                                ?.availableCommands ?? []
                                        }
                                        slashCommandsLoading={
                                            projects.activeSession
                                                ?.slashCommandsLoading ?? false
                                        }
                                        onTyping={composer.onTyping}
                                        voiceState={voice.state}
                                        voiceAudioLevel={voice.audioLevel}
                                        voiceStatusMessage={voice.statusMessage}
                                        voiceError={voice.error}
                                        voiceDownloadProgress={
                                            voice.downloadProgress
                                        }
                                        voiceNeedsPermission={
                                            voice.needsMicrophonePermission
                                        }
                                        onVoiceStart={startComposerVoice}
                                        onVoiceStop={voice.stopAndTranscribe}
                                        onVoiceCancel={voice.cancelRecording}
                                        onVoiceRetry={voice.retryInitialization}
                                        onVoiceOpenSettings={
                                            handleOpenVoiceSettings
                                        }
                                        onRequestSession={() => {
                                            // Auto-create a session when the user clicks the composer
                                            // and no session exists — removes the need to click "+" first.
                                            if (!projects.activeSession) {
                                                const projectId =
                                                    projects.activeProject
                                                        ?.id ??
                                                    STANDALONE_CHAT_PROJECT_ID;
                                                void projects.handleNewSession(
                                                    projectId,
                                                );
                                            }
                                        }}
                                        welcomeMode={isEmptySession}
                                        capabilities={composerCapabilities}
                                        onCapabilityConnect={openSettings}
                                        showSuggestions={
                                            isEmptySession &&
                                            hasActiveConversation
                                        }
                                        onSelectSuggestion={askNow}
                                        note={composerNote}
                                        onOpenHandoff={(id) => {
                                            // Chat K4: the task's thread opens in Chat's panel; Chat stays.
                                            mainChatItemPanel.close();
                                            openMainChatTask(id);
                                        }}
                                        wideMode={chatWideMode}
                                    />
                                    </>
                                );
                                return (
                                    <MainChatPageColumn enabled={activeIsMainChat} onOpenTarget={handleOpenReportTarget}>
                                        {chatBackToHandoffs && (
                                            <Button size="sm" variant="quiet" className="self-start mx-6" onClick={() => handleSetAppMode("tasks")}>
                                                {t("handoffsPage.back")}
                                            </Button>
                                        )}
                                        {isEmptySession && !activeIsMainChat ? (
                                            <NewChatWelcome
                                                aside={mainChatSetupEl}
                                            >
                                                <div
                                                    ref={
                                                        composerDockSlide.welcomeRef
                                                    }
                                                    style={{
                                                        willChange: "transform",
                                                    }}
                                                >
                                                    {composerEl}
                                                </div>
                                            </NewChatWelcome>
                                        ) : (
                                            <>
                                                <FailedSendContext.Provider value={failedSendActions}>
                                                <MainChatStreamBody
                                                    // A different session is a
                                                    // different stream: mount
                                                    // its first screen staged,
                                                    // never the previous
                                                    // chat's hydrated 150.
                                                    key={
                                                        projects.activeSession
                                                            ?.localId ?? ""
                                                    }
                                                    sessionLocalId={
                                                        projects.activeSession
                                                            ?.localId ?? ""
                                                    }
                                                    mainChat={
                                                        activeIsMainChat
                                                            ? mainChatView
                                                            : undefined
                                                    }
                                                    onPrepare={askNow}
                                                    items={
                                                        projects.activeSession
                                                            ?.items ?? null
                                                    }
                                                    streaming={
                                                        projects.activeSession
                                                            ?.streaming ?? false
                                                    }
                                                    streamingStartedAt={
                                                        projects.activeSession
                                                            ?.streamingStartedAt ??
                                                        0
                                                    }
                                                    hasProject={
                                                        hasActiveConversation
                                                    }
                                                    executorType={getExecutorType(
                                                        effectiveModel,
                                                    )}
                                                    onOpenReportTarget={
                                                        handleOpenReportTarget
                                                    }
                                                    onUseTextedReply={
                                                        handleUseTextedReply
                                                    }
                                                    onSendOpenAnswer={async (sessionId, text) => {
                                                        if (projects.activeSession?.localId !== sessionId) throw new Error();
                                                        await composer.sendImmediate(text);
                                                    }}
                                                    footer={mainChatSetupEl}
                                                    focusItem={
                                                        chatJump.focus &&
                                                        chatJump.focus.sessionId ===
                                                            projects.activeSession?.localId
                                                            ? chatJump.focus
                                                            : null
                                                    }
                                                    onFocusItemShown={
                                                        chatJump.clearFocus
                                                    }
                                                    vimMode={settings.vimMode}
                                                    onVimInsert={focusComposer}
                                                    hasMore={
                                                        projects.activeSession
                                                            ?.hasMore ?? false
                                                    }
                                                    loadingMore={
                                                        (projects.activeSession
                                                            ?.loadingMore ||
                                                            projects
                                                                .activeSession
                                                                ?.itemsLoading) ??
                                                        false
                                                    }
                                                    onLoadMore={handleLoadMore}
                                                    onEditMessage={
                                                        projects.handleEditMessage
                                                    }
                                                    connectedProviders={
                                                        connectedProviders
                                                    }
                                                    onFillComposer={(
                                                        text: string,
                                                    ) => {
                                                        composer.replacePrompt(
                                                            text,
                                                        );
                                                        requestAnimationFrame(
                                                            focusComposer,
                                                        );
                                                    }}
                                                    isFirstLogin={isFirstLogin}
                                                    wideMode={chatWideMode}
                                                    deliverableActions={
                                                        chatDeliverableActions
                                                    }
                                                />

                                                </FailedSendContext.Provider>

                                                <CompactionBanner
                                                    state={
                                                        projects.activeSession
                                                            ?.compactionState
                                                    }
                                                    error={
                                                        projects.activeSession
                                                            ?.compactionError
                                                    }
                                                />

                                                {projects.activeProject &&
                                                    projects.activeProject.id !== STANDALONE_CHAT_PROJECT_ID && (
                                                        <ProjectThreadStatus
                                                            key={projects.activeSession?.localId ?? ""}
                                                            sessionTag={projects.activeSession?.localId ?? ""}
                                                            folder={activeWorkDir}
                                                            branch={projects.activeWorkspace?.baseBranch}
                                                            revision={projects.activeSession?.streaming}
                                                        />
                                                    )}


                                                {projects.activeSession
                                                    ?.awaitingPlanApproval &&
                                                    projects.activeSession
                                                        .planContent && (
                                                        <PlanReviewCard
                                                            planContent={
                                                                projects
                                                                    .activeSession
                                                                    .planContent
                                                            }
                                                            onApprove={() => {
                                                                if (!composer.isTodayBriefOpening()) void projects.handleApprovePlan();
                                                            }}
                                                            onReject={() => {
                                                                if (!composer.isTodayBriefOpening()) void projects.handleRejectPlan();
                                                            }}
                                                            wideMode={
                                                                chatWideMode
                                                            }
                                                        />
                                                    )}

                                                {!projects.activeSession
                                                    ?.awaitingPlanApproval &&
                                                    projects.activeSession
                                                        ?.permissionOptions &&
                                                    projects.activeSession
                                                        .permissionOptions
                                                        .length > 0 &&
                                                    projects.activeSession
                                                        .permissionToolName && (
                                                        <PermissionQuestionCard
                                                            question={permissionQuestionFor(
                                                                projects
                                                                    .activeSession
                                                                    .permissionToolName,
                                                                projects
                                                                    .activeSession
                                                                    .permissionUpcoming,
                                                                t,
                                                                tChat,
                                                            )}
                                                            options={
                                                                projects
                                                                    .activeSession
                                                                    .permissionOptions
                                                            }
                                                            onSelect={(optionId) =>
                                                                composer.isTodayBriefOpening() ? false : projects.handleRespondToPermission(optionId)
                                                            }
                                                            onSkip={() => {
                                                                if (!composer.isTodayBriefOpening()) void projects.handleSkipPermission();
                                                            }}
                                                            wideMode={
                                                                chatWideMode
                                                            }
                                                        />
                                                    )}

                                                <div
                                                    ref={
                                                        composerDockSlide.chatRef
                                                    }
                                                    style={{
                                                        willChange: "transform",
                                                    }}
                                                >
                                                    {activeIsMainChat &&
                                                    mainChatView.attached ? (
                                                        <MainChatTopicBox
                                                            view={mainChatView}
                                                            streaming={
                                                                projects
                                                                    .activeSession
                                                                    ?.streaming ??
                                                                false
                                                            }
                                                            onStop={
                                                                composer.handleCancel
                                                            }
                                                            queuedMessages={
                                                                composer.queuedMessages
                                                            }
                                                            onRemoveQueued={
                                                                composer.removeQueuedMessage
                                                            }
                                                            onEditQueued={
                                                                composer.editQueuedMessage
                                                            }
                                                            onUsePlainBox={(
                                                                text,
                                                            ) =>
                                                                prepareComposerPrompt(
                                                                    text,
                                                                    "suggestion",
                                                                )
                                                            }
                                                            wideMode={
                                                                chatWideMode
                                                            }
                                                        />
                                                    ) : (
                                                        composerEl
                                                    )}
                                                </div>
                                            </>
                                        )}
                                    </MainChatPageColumn>
                                );
                            })()}

                            {appMode === "chat" && menuBarSelectedItem && (
                                <aside aria-label={tChat("mainChat.view.itemPanel")} className="flex w-[420px] max-w-[45%] min-w-0 shrink-0 flex-col border-l border-border bg-panel">
                                    <div className="flex items-center justify-between border-b border-border px-4 py-3">
                                        <h2 className="text-sm font-semibold">{tChat("mainChat.view.itemPanel")}</h2>
                                        <Button size="icon" variant="quiet" aria-label={tCommon("close")} onClick={() => setMenuBarItem(null)}><X className="size-4" /></Button>
                                    </div>
                                    <div className="min-h-0 overflow-y-auto p-4">
                                        <MainChatProactiveCard key={menuBarSelectedItem.id} item={menuBarSelectedItem} detailPanel />
                                    </div>
                                </aside>
                            )}

                        </div>
                    </>
                </RetainedPage>

                {/* Terminal panel — bottom resizable section */}
                {/* Render both divider + panel together when any terminals exist (keeps instances mounted).
                    The divider is only interactive when the active workspace has tabs and isn't collapsed. */}
                {terminal.allTerminalTabs.length > 0 && (
                    <>
                        {terminal.terminalTabs.length > 0 &&
                            !terminal.terminalCollapsed && (
                                <div
                                    className="relative shrink-0 h-0"
                                    style={
                                        {
                                            "--wails-draggable": "no-drag",
                                        } as React.CSSProperties
                                    }
                                >
                                    <div
                                        className="absolute left-0 right-0 -top-1.5 h-3 z-10 cursor-row-resize"
                                        onMouseDown={(e) => {
                                            e.preventDefault();
                                            terminal.startTerminalResize();
                                        }}
                                    />
                                </div>
                            )}
                        <Suspense fallback={<div className="shrink-0 bg-background border-t border-border" style={{ height: 0 }} />}>
                        <LazyTerminalPanel
                            tabs={terminal.terminalTabs}
                            allTabs={terminal.allTerminalTabs}
                            activeTabId={terminal.activeTerminalId}
                            height={terminal.terminalHeight}
                            collapsed={terminal.terminalCollapsed}
                            onCreateTab={terminal.handleCreateTerminal}
                            onCloseTab={terminal.handleCloseTerminal}
                            onSelectTab={terminal.setActiveTerminalId}
                            onToggleCollapse={() =>
                                terminal.setTerminalCollapsed((v) => !v)
                            }
                            onRenameTab={terminal.handleRenameTerminal}
                            onRegisterApi={terminal.registerTerminalApi}
                            terminalTheme={terminalTheme}
                        />
                        </Suspense>
                    </>
                )}
            </main>

            <EnvironmentSwitchDialog
                show={envSwitch.showConfirmDialog}
                currentEnv={envSwitch.environment}
                loading={envSwitch.loading}
                error={envSwitch.error}
                onConfirm={envSwitch.confirmSwitch}
                onCancel={envSwitch.cancelSwitch}
            />

            <SearchDialog
                open={searchOpen}
                onOpenChange={setSearchOpen}
                projects={projects.projects}
                automations={cronJobs.jobs}
                shortcutFor={searchShortcutFor}
                actions={searchActions}
            />

            <TtsDownloadNotifications />
            <AppToaster />
        </div>
    );
}
