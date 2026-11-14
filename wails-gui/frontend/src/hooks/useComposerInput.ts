import { useState, useRef, useEffect, useCallback, useMemo, type SetStateAction } from 'react';
import { chatErrorCauseOf } from '@/lib/chat-error';
import {
    SendPrompt,
    SendPromptWithImages,
    SendPromptWithAttachments,
    SendPromptWithTodayContext,
    SendPromptWithOfferReferences,
    PrepareTodayChatTurn,
    CancelStream,
    CancelByTag,
    PersistStreamItem,
    SaveSessionState,
    EnsureProjectStructure,
    RenameWorkspaceBranch,
    SaveWorkspaceState,
} from '../../wailsjs/go/main/App';
import type { AgentStatus, FileAttachment, ImageAttachment, Project, RemoteEnv, Session, StreamItem, StreamItemData, Workspace } from '@/types';
import { type KeyCombo, eventMatchesCombo } from '@/hooks/useKeybindings';
import { type TodayChatContextRef, type TodayChatDraft, type TodayChatPreview, type TodayChatStartOptions } from '@/lib/today-chat-navigation';
import { prepareTodayDraft, type TodayDraftBox } from '@/lib/today-draft-preparation';
import { useTranslation } from 'react-i18next';
import type { TaskOfferReference } from '@/lib/wails-shim-app';

// newTraceID returns a fresh UUID used as the cross-process correlation
// id for one LLM call. Generated at the moment the user hits Send, then
// propagated through the Wails binding → yansu-agent loop → outbound
// x-yansu-trace-id HTTP header → backend Loki / PostHog so the full
// chain stitches together. Falls back to a timestamp+random hex when
// crypto.randomUUID is unavailable (very old WebView builds) — the
// downstream consumer treats any non-empty string as opaque.
function newTraceID(): string {
    if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') {
        return crypto.randomUUID();
    }
    return `${Date.now().toString(16)}-${Math.random().toString(16).slice(2, 10)}`;
}
import {
    buildId,
    toStreamItemData,
    toSessionData,
    toProjectData,
    toWorkspaceData,
    getExecutorType,
    splitModelEffort,
    logPersistError,
    getCurrentUILanguage,
    isMainChatSession,
} from '@/lib/stream-helpers';

type ComposerSourceContext = TodayChatContextRef & { offerReferences?: TaskOfferReference[] };

export interface QueuedMessage {
    prompt: string;
    images: ImageAttachment[];
    files: FileAttachment[];
    todayContext?: ComposerSourceContext;
    todayPreview?: TodayChatPreview;
    awaitingReview?: boolean;
}

interface TodayComposerDraft {
    sessionId: string;
    /** Absent when neither the item nor a fallback context could be prepared:
     *  the draft is then sent as an ordinary message, with a quiet note. */
    context?: ComposerSourceContext;
    preview?: TodayChatPreview;
}

// ── Shared send helper ──
// Extracted from executeSend so callers with explicit params (e.g. automation
// creation) bypass the React state closure race condition.

export interface SendFirstMessageParams {
    project: Project;
    workspace: Workspace;
    session: Session;
    promptText: string;
    model: string;
    updateProject: (id: string, fn: (p: Project) => Project) => void;
    remoteEnv?: RemoteEnv;
    /**
     * When set, this is shown and persisted as the user-bubble content instead
     * of `promptText`. `promptText` is still what gets sent to the agent, so
     * callers can hand the agent extra context (e.g. a scheduled run's output)
     * without dumping it into the visible/persisted bubble.
     */
    displayContent?: string;
    /**
     * Extra display-only fields merged into the user StreamItem (e.g.
     * `automationResultOutput` for the scheduled-run quote card). These are not
     * persisted unless `toStreamItemData` serializes them.
     */
    userItemFields?: Partial<StreamItem>;
    /** Skip first-message auto-rename when the caller already set a label. */
    skipAutoRename?: boolean;
    /** Override the filesystem work dir sent to the agent. */
    projectPathOverride?: string;
}

interface SendSessionPromptParams {
    session: Session;
    promptText: string;
    sessionModel: string;
    sessionId: string;
    tag: string;
    projectPath: string;
    images?: ImageAttachment[];
    files?: FileAttachment[];
    sessionPlanMode?: boolean;
    sessionThinkingEnabled?: boolean;
    sessionReasoningEffort?: string;
    sessionMode?: string;
    remoteEnv?: RemoteEnv;
    uiLanguage?: string;
    /** Cross-process correlation id; empty = auto-generate. Set by
     *  callers that have already emitted a chat.send_clicked event so
     *  the binding call carries the same id. */
    traceID?: string;
    todayContext?: ComposerSourceContext;
    userItem?: StreamItemData;
}

export async function sendSessionPrompt({
    promptText,
    sessionModel,
    sessionId,
    tag,
    projectPath,
    images = [],
    files = [],
    sessionPlanMode = false,
    sessionThinkingEnabled = false,
    sessionReasoningEffort = '',
    sessionMode = '',
    remoteEnv,
    uiLanguage = getCurrentUILanguage(),
    traceID,
    todayContext,
    userItem,
}: SendSessionPromptParams): Promise<number | void> {
    const remoteEnvJSON = remoteEnv ? JSON.stringify(remoteEnv) : '';
    const finalTraceID = traceID && traceID !== '' ? traceID : newTraceID();

    if (todayContext) {
        if (!userItem) throw new Error('TODAY_CHAT_CONTEXT_UNAVAILABLE');
        if (todayContext.offerReferences) {
            return SendPromptWithOfferReferences(
                promptText, projectPath, sessionModel, sessionId, tag,
                images.map((img) => ({ media_type: img.mediaType, data_base64: img.dataBase64 })),
                files.map((file) => ({ file_name: file.fileName, media_type: file.mediaType, data_base64: file.dataBase64 ?? '', file_size: file.fileSize })),
                sessionPlanMode, sessionThinkingEnabled, uiLanguage, finalTraceID, userItem, todayContext.offerReferences,
            );
        }
        return SendPromptWithTodayContext(
            promptText, projectPath, sessionModel, sessionId, tag,
            images.map((img) => ({ media_type: img.mediaType, data_base64: img.dataBase64 })),
            files.map((file) => ({ file_name: file.fileName, media_type: file.mediaType, data_base64: file.dataBase64 ?? '', file_size: file.fileSize })),
            sessionPlanMode, sessionThinkingEnabled, uiLanguage, finalTraceID, userItem, todayContext,
        );
    }

    if (files.length > 0) {
        await SendPromptWithAttachments(
            promptText,
            projectPath,
            sessionModel,
            sessionId,
            tag,
            images.map((img) => ({
                media_type: img.mediaType,
                data_base64: img.dataBase64,
            })),
            files.map((f) => ({
                file_name: f.fileName,
                media_type: f.mediaType,
                data_base64: f.dataBase64 ?? '',
                file_size: f.fileSize,
            })),
            sessionPlanMode,
            sessionThinkingEnabled,
            sessionReasoningEffort,
            sessionMode,
            remoteEnvJSON,
            uiLanguage,
            finalTraceID,
        );
        return;
    }

    if (images.length > 0) {
        await SendPromptWithImages(
            promptText,
            projectPath,
            sessionModel,
            sessionId,
            tag,
            images.map((img) => ({
                media_type: img.mediaType,
                data_base64: img.dataBase64,
            })),
            sessionPlanMode,
            sessionThinkingEnabled,
            sessionReasoningEffort,
            sessionMode,
            remoteEnvJSON,
            uiLanguage,
            finalTraceID,
        );
        return;
    }

    await SendPrompt(
        promptText,
        projectPath,
        sessionModel,
        sessionId,
        tag,
        sessionPlanMode,
        sessionThinkingEnabled,
        sessionReasoningEffort,
        sessionMode,
        remoteEnvJSON,
        uiLanguage,
        finalTraceID,
    );
}

export async function sendFirstMessage(params: SendFirstMessageParams): Promise<void> {
    const { project, workspace, session, promptText, model, updateProject, remoteEnv, displayContent, userItemFields, skipAutoRename, projectPathOverride } = params;
    const tag = session.localId;
    // What the user actually sees / what we persist for the bubble. May differ
    // from promptText, which is the (possibly context-heavy) payload sent to
    // the agent.
    const displayText = displayContent ?? promptText;

    const mapSess = (
        p: Project,
        sessionId: string,
        updater: (s: Session) => Session,
    ): Project => ({
        ...p,
        workspaces: p.workspaces.map((w) => ({
            ...w,
            sessions: w.sessions.map((s) =>
                s.localId === sessionId ? updater(s) : s,
            ),
        })),
    });

    // 1. Build user StreamItem (display content; extra display-only fields)
    const userItem: StreamItem = {
        id: buildId(),
        kind: 'user',
        content: displayText,
        ...userItemFields,
    };

    // 2. Auto-rename: first line of message, truncated to 40 chars
    const isFirstMessage =
        session.items.length === 0 && session.streamItemCount === 0;
    // The main chat keeps its name; every other chat takes its first line.
    let newLabel: string | null = null;
    if (isFirstMessage && !skipAutoRename && !isMainChatSession(session)) {
        const firstLine = displayText.split('\n')[0];
        newLabel = firstLine.length > 40
            ? firstLine.slice(0, 40) + '\u2026'
            : firstLine;
    }

    // 3. Update state: insert user item + streaming + label
    updateProject(project.id, (p) =>
        mapSess(p, tag, (s) => ({
            ...s,
            streaming: true,
            streamingStartedAt: Date.now(),
            currentRunId: null,
            items: [...s.items, userItem],
            streamItemCount: Math.max(s.streamItemCount, s.items.length + 1),
            itemsLoaded: true,
            itemsLoading: false,
            updatedAt: Date.now(),
            ...(newLabel ? { label: newLabel } : {}),
        })),
    );

    if (newLabel) {
        SaveSessionState(
            project.id,
            toSessionData({ ...session, label: newLabel }) as any,
        ).catch(logPersistError);
    }

    // 4. Persist user StreamItem (caller must ensure session already exists in DB)
    const assignedSortOrder = await PersistStreamItem(
        tag,
        toStreamItemData(userItem),
    );
    updateProject(project.id, (p) =>
        mapSess(p, tag, (s) => ({
            ...s,
            items: s.items.map((i) =>
                i.id !== userItem.id ? i : { ...i, sortOrder: assignedSortOrder },
            ),
        })),
    );

    // 5. Model normalization (codex composite models). Settings › Assistant
    // owns the model and effort; a chat's stored model never overrides it.
    let sessionModel = model;
    const sessionId = session.sessionId || '';
    const projectPath = projectPathOverride || workspace?.worktreePath || project.path;

    let sessionReasoningEffort = '';
    if (getExecutorType(sessionModel) === 'codex') {
        const [baseModel, effortFromModel] = splitModelEffort(sessionModel);
        sessionModel = baseModel;
        sessionReasoningEffort = effortFromModel;
    }

    await sendSessionPrompt({
        session,
        promptText,
        sessionModel,
        sessionId,
        tag,
        projectPath,
        sessionPlanMode: false,
        sessionThinkingEnabled: false,
        sessionReasoningEffort,
        sessionMode: session.sessionMode ?? '',
        remoteEnv,
    });
}

interface UseComposerInputDeps {
    activeProject: Project | null;
    activeWorkspace: Workspace | null;
    activeSession: Session | null;
    updateProject: (
        projectId: string,
        updater: (p: Project) => Project,
    ) => void;
    status: AgentStatus;
    model: string;
    setError: (e: string | null) => void;
    awaitingPlanApproval?: boolean;
    hasPendingPermission?: boolean;
    onDismissPlanReview?: () => Promise<void>;
    onRespondToPermission?: (optionId: string) => Promise<boolean>;
    sendPromptCombo: KeyCombo;
    /** Called when the user types in the composer. */
    onTyping?: () => void;
    /** Connected provider names for first-chat context injection. */
    connectedProviders?: { slack?: string; feishu?: string };
    /** User display name for first-chat context injection. */
    userName?: string;
    /** True only on the user's very first login (no prior chats ever). */
    isFirstLogin?: boolean;
}

export function useComposerInput({
    activeProject,
    activeWorkspace,
    activeSession,
    updateProject,
    status,
    model,
    setError,
    awaitingPlanApproval,
    hasPendingPermission,
    onDismissPlanReview,
    onRespondToPermission,
    sendPromptCombo,
    onTyping,
    connectedProviders = {},
    userName,
    isFirstLogin = false,
}: UseComposerInputDeps) {
    const { t, i18n } = useTranslation('today');
    const composerRevisionRef = useRef(0);
    const promptRevisionRef = useRef(0);
    const getDraftRevision = useCallback(() => composerRevisionRef.current, []);
    const composerSessionRef = useRef(activeSession?.localId);
    composerSessionRef.current = activeSession?.localId;
    const [prompt, setPromptValue] = useState("");
    const promptValueRef = useRef('');
    const setPromptState = useCallback((value: string) => {
        promptValueRef.current = value;
        setPromptValue(value);
    }, []);
    const todayDraftRef = useRef<TodayComposerDraft | null>(null);
    const pendingBriefRef = useRef<{ isCurrent: () => boolean; finish: (opened?: import('@/lib/today-chat-navigation').TodayBriefOpened) => void } | null>(null);
    // Native Undo/Redo emits ordinary value changes, including values from
    // before a clear, replacement, or send. Keep their provenance for this
    // composer's lifetime; forgetting a value could bypass its source gates.
    const todayDraftHistoryRef = useRef(new Map<string, TodayComposerDraft | null>());
    const rememberTodayDraft = (value: string, draft: TodayComposerDraft | null) => {
        if (!value.trim() || draft?.preview?.status === 'loading') return;
        if (!draft && todayDraftHistoryRef.current.get(value)) return;
        if (draft || todayDraftHistoryRef.current.size > 0) todayDraftHistoryRef.current.set(value, draft);
    };
    const [todayPreview, setTodayPreview] = useState<TodayChatPreview>();
    const setPrompt = useCallback((value: string) => {
        composerRevisionRef.current += 1;
        promptRevisionRef.current += 1;
        const draft = (todayDraftRef.current?.context?.offerReferences || (todayDraftRef.current?.preview?.status === 'loading' && !todayDraftRef.current.context)) ? todayDraftRef.current : !value.trim() ? null : todayDraftHistoryRef.current.has(value)
            ? todayDraftHistoryRef.current.get(value)! : todayDraftRef.current;
        todayDraftRef.current = draft;
        rememberTodayDraft(value, draft);
        setTodayPreview(draft?.preview);
        setPromptState(value);
    }, []);
    const replacePrompt = useCallback((value: string) => {
        composerRevisionRef.current += 1;
        promptRevisionRef.current += 1;
        const draft = (todayDraftRef.current?.context?.offerReferences || (todayDraftRef.current?.preview?.status === 'loading' && !todayDraftRef.current.context)) ? todayDraftRef.current : todayDraftHistoryRef.current.get(value) ?? null;
        todayDraftRef.current = draft;
        rememberTodayDraft(value, draft);
        setTodayPreview(draft?.preview);
        setPromptState(value);
    }, []);
    const prefillTodayPrompt = useCallback((sessionId: string, text: string, context: ComposerSourceContext | undefined, preview?: TodayChatPreview) => {
        composerRevisionRef.current += 1;
        promptRevisionRef.current += 1;
        todayDraftRef.current = { sessionId, context, preview };
        rememberTodayDraft(text, todayDraftRef.current);
        setTodayPreview(preview);
        setPromptState(text);
        setImages([]);
        setFiles([]);
    }, []);
    const prefillOfferPrompt = useCallback((sessionId: string, text: string, reference: TaskOfferReference) => {
        prefillTodayPrompt(sessionId, text, { sourceKind: 'task_offer', sourceId: reference.id, offerReferences: [{ ...reference }] });
    }, [prefillTodayPrompt]);
    useEffect(() => {
        const pending = pendingBriefRef.current;
        if (pending?.isCurrent()) return;
        if (pending) pending.finish();
        const draft = todayDraftRef.current;
        if (draft && draft.sessionId !== activeSession?.localId) {
            todayDraftRef.current = null;
            setTodayPreview(undefined);
            setPromptState('');
        }
    }, [activeSession?.localId]);
    const [images, setImagesState] = useState<ImageAttachment[]>([]);
    const [files, setFilesState] = useState<FileAttachment[]>([]);
    const setImages = useCallback((value: SetStateAction<ImageAttachment[]>) => {
        composerRevisionRef.current += 1;
        setImagesState(value);
    }, []);
    const setFiles = useCallback((value: SetStateAction<FileAttachment[]>) => {
        composerRevisionRef.current += 1;
        setFilesState(value);
    }, []);
    /**
     * Put a Today action's text in the composer at once and attach its
     * context. The draft Today prepared before the click is used directly
     * when fresh; an older one fills the composer at once and is checked
     * again before Send. Without a prepared draft the card's own prompt fills
     * the composer while the item is prepared. If the item cannot be
     * prepared, the card's prompt stays without context and with a quiet
     * note, never an empty composer. A brief card (backlog item 64) works the
     * same way: its prepared words fill the box at once and nothing is sent;
     * the computer's current words replace them while untouched, and Send
     * reads the card's sources again.
     */
    const prepareTodayPrompt = useCallback(async (sessionId: string, cardPrompt: string, context: TodayChatContextRef, isCurrent: () => boolean = () => true, options: TodayChatStartOptions = {}) => {
        // The preparation itself is shared with the panel's box on Today
        // (prepareTodayDraft); this box adds its own rules for a draft that
        // moved chats or was edited while the item loaded.
        const originSessionId = composerSessionRef.current;
        const language = i18n.resolvedLanguage ?? i18n.language;
        let draft: TodayComposerDraft | null = null;
        let revision = composerRevisionRef.current;
        let promptRevision = promptRevisionRef.current;
        const box: TodayDraftBox = {
            fill: (text, itemContext, preview) => {
                prefillTodayPrompt(sessionId, text, itemContext, preview);
                draft = todayDraftRef.current;
                revision = composerRevisionRef.current;
                promptRevision = promptRevisionRef.current;
            },
            update: (patch) => {
                const live = draft;
                if (!live) return;
                if ('context' in patch) live.context = patch.context;
                if (patch.preview) {
                    live.preview = patch.preview;
                    setTodayPreview(patch.preview);
                }
                if (patch.text !== undefined) setPromptState(patch.text);
                // The prepared context also belongs to any edits retained
                // while it loaded, so clearing and undoing them cannot drop
                // its gates.
                rememberTodayDraft(promptValueRef.current, live);
            },
            clear: () => {
                todayDraftRef.current = null;
                setTodayPreview(undefined);
                setPromptState('');
            },
            stillApplies: () => {
                if (!draft || todayDraftRef.current !== draft) return false;
                if (!isCurrent()) {
                    if (context.sourceKind === 'brief_card' && composerRevisionRef.current !== revision) {
                        // Card words the user edited keep their card, so Send
                        // still asks Go and never sends them unchecked.
                        draft.preview = { status: 'unavailable' };
                        setTodayPreview(draft.preview);
                        rememberTodayDraft(promptValueRef.current, draft);
                        return false;
                    }
                    todayDraftRef.current = null;
                    setTodayPreview(undefined);
                    if (composerRevisionRef.current === revision) setPromptState('');
                    return false;
                }
                // Main-chat selection may not have rendered when its navigation
                // promise resolves. An actual later session change clears draft.
                return composerSessionRef.current === sessionId || composerSessionRef.current === originSessionId;
            },
            unedited: () => promptRevisionRef.current === promptRevision,
            onCardGone: () => setError(t('todayBrief.cardGone')),
        };
        await prepareTodayDraft(box, cardPrompt, context, options, t, language);
    }, [prefillTodayPrompt, t, i18n]);

    /** The reference Go checks again at Send: the prepared facts it was read
     *  with travel with it, so a change in between is refused. */
    const preparedTodayContext = (context: TodayChatContextRef, prepared: TodayChatDraft): TodayChatContextRef => ({
        ...context,
        generatedTitle: prepared.prefill.title,
        generatedFingerprint: prepared.fingerprint,
        ...(prepared.prefill.people?.length ? { generatedPeople: prepared.prefill.people } : {}),
    });

    /**
     * "Talk about this brief": Isoform has already spoken first in the main
     * chat, so the composer stays empty for the user's answer, with the brief
     * attached to it.
     */
    const attachTodayBrief = useCallback((sessionId: string, context: TodayChatContextRef, prepared: TodayChatDraft) => {
        prefillTodayPrompt(sessionId, '', preparedTodayContext(context, prepared), { status: 'ready', counts: prepared.counts });
    }, [prefillTodayPrompt]);

    /** The guard starts before Chat is visible, including while its main
     * session loads. It belongs to one request and never clears user input. */
    const beginTodayBrief = useCallback((isCurrent: () => boolean) => {
        let draft: TodayComposerDraft | null = null;
        const pending = {
            isCurrent,
            finish: (opened?: import('@/lib/today-chat-navigation').TodayBriefOpened) => {
                if (pendingBriefRef.current !== pending) return;
                pendingBriefRef.current = null;
                if (!draft || todayDraftRef.current !== draft) return;
                if (!isCurrent()) {
                    todayDraftRef.current = null;
                    setTodayPreview(undefined);
                    return;
                }
                draft.context = opened ? preparedTodayContext(opened.ref, opened.draft) : undefined;
                draft.preview = opened ? { status: 'ready', counts: opened.draft.counts } : { status: 'unavailable' };
                rememberTodayDraft(promptValueRef.current, draft);
                setTodayPreview(draft.preview);
            },
        };
        pendingBriefRef.current = pending;
        return {
            select: (sessionId: string) => {
                if (pendingBriefRef.current !== pending || !isCurrent()) return;
                draft = { sessionId, preview: { status: 'loading' } };
                todayDraftRef.current = draft;
                setTodayPreview(draft.preview);
            },
            finish: pending.finish,
        };
    }, []);
    const isTodayBriefOpening = useCallback(() => {
        const pending = pendingBriefRef.current;
        if (!pending) return false;
        if (pending.isCurrent()) return true;
        pending.finish();
        return false;
    }, []);

    const [queuedMessages, setQueuedMessages] = useState<QueuedMessage[]>([]);
    /** Tokens keep a canceled Today turn from sending after any pending RPC. */
    const preparingTodayRef = useRef<Map<string, symbol>>(new Map());

    /** Per-session message queues keyed by session localId. */
    const messageQueueRef = useRef<Map<string, QueuedMessage[]>>(new Map());
    /** Per-session previous busy state, including permissions and preflight. */
    const prevStreamingRef = useRef<Map<string, boolean>>(new Map());
    /** Tracks the original queue index when editing a queued message. */
    const editingQueueIndexRef = useRef<number | null>(null);

    /** Get the queue array for a given session (creates if missing). */
    const getQueue = (sessionId: string): QueuedMessage[] => {
        let q = messageQueueRef.current.get(sessionId);
        if (!q) {
            q = [];
            messageQueueRef.current.set(sessionId, q);
        }
        return q;
    };

    /** Map a session within the workspace hierarchy. */
    const mapSession = (
        p: Project,
        sessionId: string,
        updater: (s: Session) => Session,
    ): Project => ({
        ...p,
        workspaces: p.workspaces.map((w) => ({
            ...w,
            sessions: w.sessions.map((s) =>
                s.localId === sessionId ? updater(s) : s,
            ),
        })),
    });

    /** Map a workspace within a project. */
    const mapWorkspace = (
        p: Project,
        workspaceId: string,
        updater: (w: Workspace) => Workspace,
    ): Project => ({
        ...p,
        workspaces: p.workspaces.map((w) =>
            w.id === workspaceId ? updater(w) : w,
        ),
    });

    /** Sync ref → state for UI rendering (shows active session's queue). */
    const syncQueueState = () => {
        const sid = activeSession?.localId;
        const q = sid ? (messageQueueRef.current.get(sid) ?? []) : [];
        setQueuedMessages([...q]);
    };

    const restoreFailedTodayDraft = (sessionId: string, draft: QueuedMessage, clearedRevision?: number) => {
        // A failed preflight is not a completed turn: retain it for review
        // without automatically retrying it or sending the next queued turn.
        prevStreamingRef.current.set(sessionId, false);
        if (composerSessionRef.current === sessionId && clearedRevision === composerRevisionRef.current) {
            composerRevisionRef.current += 1;
            todayDraftRef.current = draft.todayContext ? { sessionId, context: draft.todayContext, preview: draft.todayPreview } : null;
            rememberTodayDraft(draft.prompt, todayDraftRef.current);
            setTodayPreview(draft.todayPreview);
            setPromptState(draft.prompt);
            setImagesState(draft.images);
            setFilesState(draft.files);
            return;
        }
        // A dequeued draft returns to the front. The same recovery preserves
        // any text or attachments authored while a direct send was preparing.
        const queue = getQueue(sessionId);
        queue.unshift({ ...draft, awaitingReview: true });
        if (composerSessionRef.current === sessionId) setQueuedMessages([...queue]);
    };

    const failedSendsRef = useRef(new Map<string, { tag: string; prompt: string; inFlight: boolean; retry: () => Promise<void> }>());
    const forgottenSessionsRef = useRef(new Set<string>());
    // R75: the most recent normal send in a chat. A signed-out answer can send
    // it again with one tap, with no retyping (the message row is not resent).
    const lastSendRef = useRef<{ tag: string; retry: () => Promise<void> } | null>(null);
    // The one row that offers Send again: the chat's last item, when it is a
    // signed-out answer. An older row never offers it (it would resend a message
    // a later turn already sent).
    const resendItemId = useMemo(() => {
        const items = activeSession?.items ?? [];
        const latest = items[items.length - 1];
        if (!latest || latest.kind !== 'error') return null;
        const code = chatErrorCauseOf(latest).code;
        return code === 'signed_out' || code === 'no_reply' ? latest.id : null;
    }, [activeSession]);
    const retryFailedSend = useCallback((itemId: string) => {
        const failed = failedSendsRef.current.get(itemId);
        if (!failed || failed.inFlight) return;
        failed.inFlight = true;
        void failed.retry();
    }, []);

    // Edit takes a kept message back into the box; nothing is resent.
    const editFailedSend = useCallback((itemId: string) => {
        const failed = failedSendsRef.current.get(itemId);
        if (!failed || failed.inFlight || !activeProject) return;
        failedSendsRef.current.delete(itemId);
        updateProject(activeProject.id, (p) => mapSession(p, failed.tag, (s) => ({
            ...s, items: s.items.filter((item) => item.id !== itemId),
        })));
        setPrompt(failed.prompt);
    }, [activeProject, updateProject, setPrompt]);

    const forgetFailedSends = useCallback((tag: string) => {
        forgottenSessionsRef.current.add(tag);
        for (const [id, failed] of failedSendsRef.current) {
            if (failed.tag === tag) failedSendsRef.current.delete(id);
        }
    }, []);

    /** Internal: execute the actual send RPC. */
    const executeSend = useCallback(async (
        sendPromptText: string,
        sendImages: ImageAttachment[],
        sendFiles: FileAttachment[] = [],
        todayContext?: ComposerSourceContext,
        restoreComposer?: { prompt: string; revision: number },
        draftPreview?: TodayChatPreview,
        retryTurn?: { item: StreamItem; traceID: string },
        // The words the chat shows when they differ from what the AI is sent.
        displayText?: string,
    ): Promise<void> => {
        if (!activeProject || !activeSession) return;

        const tag = activeSession.localId;
        const preparation = todayContext ? Symbol(tag) : undefined;
        const todayTurnCanceled = () => preparation !== undefined && preparingTodayRef.current.get(tag) !== preparation;
        const originalTodayDraft = todayContext
            ? { prompt: restoreComposer?.prompt ?? sendPromptText, images: sendImages, files: sendFiles, todayContext, ...(draftPreview ? { todayPreview: draftPreview } : {}) }
            : undefined;
        if (todayContext) {
            preparingTodayRef.current.set(tag, preparation!);
            // Reserve this session before the read-time check so a second
            // message queues behind this turn while its sources are read.
            updateProject(activeProject.id, (p) =>
                mapSession(p, tag, (s) => ({ ...s, streaming: true, streamingStartedAt: Date.now(), currentRunId: null })),
            );
            try {
                if (!todayContext.offerReferences) sendPromptText = await PrepareTodayChatTurn(tag, sendPromptText, todayContext);
                if (todayTurnCanceled()) return;
            } catch {
                if (todayTurnCanceled()) return;
                preparingTodayRef.current.delete(tag);
                restoreFailedTodayDraft(tag, originalTodayDraft!, restoreComposer?.revision);
                setError(t('actions.contextUnavailable'));
                updateProject(activeProject.id, (p) =>
                    mapSession(p, tag, (s) => ({ ...s, streaming: false })),
                );
                return;
            }
        }
        // Generate the cross-process correlation id at the moment of
        // user intent. Everything downstream — Wails binding, agent
        // loop telemetry, outbound HTTP request to backend, backend
        // sub2api_proxy events — receives the same id so the trace
        // stitches end-to-end in PostHog + Loki.
        //
        // The agentloop.user.send_clicked PostHog event used to be
        // captured here, but emitting it before the Go-side
        // executePrompt let phantom kickoffs land in PostHog whenever
        // the subscription pre-flight or the activeTags-collision
        // guard short-circuited the run. The Go side now captures the
        // event inside executePrompt after both gates pass, so this
        // hook only generates the trace_id and forwards it.
        const traceID = retryTurn?.traceID ?? newTraceID();

        const filesForPersist = sendFiles.length > 0
            ? sendFiles.map(({ dataBase64: _, ...rest }) => rest)
            : undefined;
        const userItem: StreamItem = retryTurn?.item ?? {
            id: buildId(),
            kind: 'user',
            content: displayText ?? sendPromptText,
            images: sendImages.length > 0 ? sendImages : undefined,
            files: filesForPersist,
        };
        let todaySendStarted = false;
        const assignUserSortOrder = (sortOrder: number) => updateProject(activeProject.id, (p) =>
            mapSession(p, tag, (s) => ({
                ...s,
                items: s.items.map((item) => item.id !== userItem.id ? item : { ...item, sortOrder }),
            })),
        );
        const removeOptimisticTodayItem = () => {
            updateProject(activeProject.id, (p) => mapSession(p, tag, (s) => {
                const items = s.items.filter((item) => item.id !== userItem.id);
                if (items.length === s.items.length) return s;
                const optimisticCount = Math.max(activeSession.streamItemCount, activeSession.items.length + 1) - activeSession.streamItemCount;
                return {
                    ...s,
                    items,
                    streamItemCount: Math.max(items.length, s.streamItemCount - optimisticCount),
                };
            }));
        };

        // Auto-rename session on first user input, or if the label is still
        // the default timestamp format (defensive: covers cases where a prior
        // rename was overwritten by a concurrent SaveProjectState).
        const isFirstMessage =
            activeSession.items.length === 0 &&
            activeSession.streamItemCount === 0;
        const isDefaultLabel = /^Session \d{1,2}:\d{2}\s[AP]M$/.test(activeSession.label);
        let newLabel: string | null = null;
        if (!retryTurn && (isFirstMessage || isDefaultLabel) && !isMainChatSession(activeSession)) {
            const firstLine = (displayText ?? sendPromptText).split('\n')[0];
            newLabel =
                firstLine.length > 40
                    ? firstLine.slice(0, 40) + "\u2026"
                    : firstLine;
        }

        updateProject(activeProject.id, (p) =>
            mapSession(p, tag, (s) => ({
                ...s,
                streaming: retryTurn ? s.streaming : true,
                streamingStartedAt: retryTurn ? s.streamingStartedAt : Date.now(),
                currentRunId: retryTurn ? s.currentRunId : null,
                items: retryTurn
                    ? s.items.map((item) => item.id === userItem.id ? { ...item, sendFailed: false, sendFailedCode: undefined } : item)
                    : [...s.items, userItem],
                streamItemCount: retryTurn ? s.streamItemCount : Math.max(s.streamItemCount, s.items.length + 1),
                itemsLoaded: true,
                itemsLoading: false,
                updatedAt: Date.now(),
                ...(newLabel ? { label: newLabel } : {}),
            })),
        );

        // NOTE: label persistence is deferred until after EnsureProjectStructure
        // (below) to avoid the old SaveProjectState overwriting the new label.

        // Auto-rename non-default workspace on first message (label + git branch).
        // Only rename once: skip if branch was already renamed (suffix no longer matches workspace ID).
        // IMPORTANT: Must await so the new worktreePath is available before SendPrompt,
        // otherwise SendPrompt uses the old (now-moved) directory path → chdir error.
        let renamedWorktreePath: string | null = null;
        if (!retryTurn && isFirstMessage && activeWorkspace && !activeWorkspace.isDefault && activeWorkspace.worktreeBranch) {
            const branchSuffix = activeWorkspace.worktreeBranch.split('/').pop();
            const isStillAutoNamed = branchSuffix === activeWorkspace.id;
            const wsLabel = (displayText ?? sendPromptText).split('\n')[0].slice(0, 40).trim();
            if (isStillAutoNamed && wsLabel) {
                try {
                    const result = await RenameWorkspaceBranch(activeProject.path, activeWorkspace.id, wsLabel);
                    renamedWorktreePath = result.path;
                    updateProject(activeProject.id, (p) =>
                        mapWorkspace(p, activeWorkspace.id, (w) => ({
                            ...w,
                            label: wsLabel,
                            worktreeBranch: result.branch,
                            worktreePath: result.path,
                        }))
                    );
                    SaveWorkspaceState(
                        activeProject.id,
                        toWorkspaceData({ ...activeWorkspace, label: wsLabel, worktreeBranch: result.branch, worktreePath: result.path }) as any
                    ).catch(logPersistError);
                } catch (err: unknown) {
                    // Non-fatal: workspace keeps its auto-generated name.
                    const msg = err instanceof Error ? err.message : String(err);
                    console.warn('[workspace rename]', msg);
                }
            }
        }

        try {
            if (todayTurnCanceled()) return;
            // Ensure the project/workspace/session records exist in SQLite
            // before inserting stream items (FK constraint). Uses INSERT ...
            // ON CONFLICT DO NOTHING so it never overwrites mutable fields
            // like session labels that may have been updated concurrently.
            await EnsureProjectStructure(toProjectData(activeProject) as any);
            if (todayTurnCanceled()) return;

            // Persist the renamed label AFTER the structure is guaranteed to
            // exist, so the upsert writes the correct label without being
            // overwritten by a stale full-project save.
            if (newLabel) {
                await SaveSessionState(
                    activeProject.id,
                    toSessionData({ ...activeSession, label: newLabel }) as any,
                );
            }
            if (todayTurnCanceled()) return;

            // Today sends remain local until Go admits the turn after its
            // final source checks. Ordinary messages keep their usual path.
            if (!todayContext) {
                const assignedSortOrder = await PersistStreamItem(tag, toStreamItemData(userItem));
                assignUserSortOrder(assignedSortOrder);
            }

            let sessionModel = model;
            const sessionId = activeSession.sessionId || '';
            const projectPath = renamedWorktreePath || activeWorkspace?.worktreePath || activeProject.path;
            const sessionPlanMode = false;
            const sessionThinkingEnabled = false;
            const sessionMode = activeSession.sessionMode ?? '';

            let sessionReasoningEffort = '';
            if (getExecutorType(sessionModel) === 'codex') {
                const [baseModel, effortFromModel] = splitModelEffort(sessionModel);
                sessionModel = baseModel;
                sessionReasoningEffort = effortFromModel;
            }

            // Onboarding context: only injected on the user's very first
            // request (isFirstLogin flag), usually the one they typed at the
            // end of onboarding. Asks for a direct, helpful answer. NOT
            // injected on every new session — regular chats just get the
            // server-side yansu context.
            let promptToSend = sendPromptText;

            // Transcript context: the transcript lives on screen as the
            // first assistant stream item for the session. The runner doesn't
            // replay prior assistant items as context, so on the user's
            // first turn we wrap the transcript body as a grounding
            // preamble the model can answer against. `isFirstMessage` is
            // false here because the transcript item already occupies the
            // session, so gate on "no user messages yet" instead.
            const hasUserTurn = activeSession.items.some((item) => item.kind === 'user');
            if (
                !hasUserTurn &&
                activeSession.label?.startsWith('Transcript: ')
            ) {
                const parts: string[] = [];
                for (const item of activeSession.items) {
                    if (item.kind === 'assistant' && item.content) {
                        parts.push(item.content);
                    }
                }
                const transcriptBody = parts.join('\n\n').trim();
                if (transcriptBody) {
                    const header = [
                        `<meeting-transcript>`,
                        `The body below is the meeting transcript the user is viewing in this session.`,
                        `Answer the user's question using this transcript as the primary source of context.`,
                        ``,
                        transcriptBody,
                        `</meeting-transcript>`,
                        ``,
                    ].join('\n');
                    promptToSend = header + sendPromptText;
                }
            }

            // Automation result context: sessions created by the cron
            // scheduler carry automationResult=true and a label of the
            // form "Automation: <name>". The assistant's first item is
            // the latest automation run output — follow-up user messages
            // are questions about that output or requests to adjust the
            // automation. Without context the model treats the chat as a
            // fresh generic conversation and loses the thread.
            if (!hasUserTurn && activeSession.automationResult) {
                const label = activeSession.label ?? '';
                const automationName = label.startsWith('Scheduled: ')
                    ? label.slice('Scheduled: '.length).trim()
                    : label.startsWith('Automation: ')
                        ? label.slice('Automation: '.length).trim()
                        : '';
                const lines: string[] = [
                    '<automation-context>',
                    `This chat session is the output of a scheduled Isoform automation.`,
                ];
                if (automationName) {
                    lines.push(`Automation name: ${automationName}`);
                }
                lines.push(
                    `The assistant message immediately above is the most recent run output for this automation — treat it as the source the user is referring to when they ask follow-up questions.`,
                    `When the user asks about the result, summarizes, or asks for changes, they mean this automation and its output.`,
                    '</automation-context>',
                    '',
                );
                promptToSend = lines.join('\n') + promptToSend;
            }

            if (isFirstLogin && isFirstMessage) {
                const providerList = Object.entries(connectedProviders)
                    .filter(([, v]) => !!v)
                    .map(([k, v]) => k === 'slack' ? `Slack (${v})` : v)
                    .join(', ');
                const parts: string[] = [];
                parts.push('<onboarding-context>');
                parts.push(`This is your very first conversation with this user.${userName ? ` Their name is ${userName}.` : ''}`);
                parts.push('They just finished setting up Isoform and chose you as their assistant. The message below is their first request.');
                if (providerList) {
                    parts.push(`They have connected: ${providerList}. You are already learning from their message history there.`);
                }
                parts.push('How to handle it:');
                parts.push('1. Answer the request directly and helpfully. Doing what they asked, and doing it well, is the best welcome.');
                parts.push('2. Be warm and natural, but do not open with an introduction of yourself or of Isoform.');
                parts.push('3. If something essential is missing, ask one short clarifying question; otherwise get on with the work.');
                parts.push('4. If you learn something lasting about them (name, role, preferences), note it in your SOUL.md.');
                parts.push('5. Stay on what they asked. Do not steer them toward building an app or toward features they did not ask about.');
                parts.push('</onboarding-context>');
                parts.push('');
                parts.push(sendPromptText);
                promptToSend = parts.join('\n');
            }

            if (todayTurnCanceled()) return;
            todaySendStarted = !!todayContext;
            const assignedSortOrder = await sendSessionPrompt({
                session: activeSession,
                // Go owns the Today context and admission of its visible text.
                promptText: todayContext ? sendPromptText : promptToSend,
                sessionModel,
                sessionId,
                tag,
                projectPath,
                images: sendImages,
                files: sendFiles,
                sessionPlanMode,
                sessionThinkingEnabled,
                sessionReasoningEffort,
                sessionMode,
                remoteEnv: activeWorkspace?.remoteEnv,
                traceID,
                todayContext,
                userItem: toStreamItemData(userItem),
            });
            if (retryTurn) failedSendsRef.current.delete(userItem.id);
            if (!todayContext) {
                lastSendRef.current = { tag, retry: () => executeSend(sendPromptText, sendImages, sendFiles, undefined, undefined, undefined, { item: userItem, traceID }, displayText) };
            }
            // An admitted Today row stays in history even if Stop arrives
            // while Go returns its sort order, just like any sent message.
            if (todayContext && typeof assignedSortOrder === 'number') assignUserSortOrder(assignedSortOrder);
        } catch (err) {
            const message =
                err instanceof Error ? err.message : String(err);
            const offerUnavailable = !!todayContext?.offerReferences;
            const todayContextUnavailable = todayContext && (offerUnavailable || message === 'TODAY_CHAT_CONTEXT_UNAVAILABLE');
            if (todayContextUnavailable) {
                // Go never published this rejected turn. Drop the local row
                // so later history state cannot mistake it for an admitted send.
                removeOptimisticTodayItem();
            }
            if (todayTurnCanceled()) return;
            if (todayContextUnavailable) restoreFailedTodayDraft(tag, originalTodayDraft!, restoreComposer?.revision);
            if (!todayContext && !forgottenSessionsRef.current.has(tag)) {
                failedSendsRef.current.set(userItem.id, { tag, prompt: sendPromptText, inFlight: false, retry: () => executeSend(sendPromptText, sendImages, sendFiles, undefined, undefined, undefined, { item: userItem, traceID }, displayText) });
                prevStreamingRef.current.set(tag, false);
                updateProject(activeProject.id, (p) => mapSession(p, tag, (s) => ({
                    // no_assistant is Go's own code for a send it kept because no AI is configured.
                    ...s, items: s.items.map((item) => item.id === userItem.id
                        ? { ...item, sendFailed: true, sendFailedCode: message === 'no_assistant' ? 'no_assistant' : undefined, sendRetryFailed: retryTurn ? true : undefined }
                        : item),
                })));
            }
            // The kept message's own line says what happened; Chat shows no toast.
            if (offerUnavailable) setError(t('actions.offerUnavailable'));
            else if (todayContextUnavailable) setError(t('actions.contextUnavailable'));
            updateProject(activeProject.id, (p) =>
                mapSession(p, tag, (s) => ({ ...s, streaming: false })),
            );
        } finally {
            if (todayTurnCanceled() && !todaySendStarted) {
                // Stop before the final RPC only has local state to remove.
                removeOptimisticTodayItem();
            }
            if (preparation !== undefined && preparingTodayRef.current.get(tag) === preparation) {
                preparingTodayRef.current.delete(tag);
            }
        }
    // eslint-disable-next-line react-hooks/exhaustive-deps
    }, [activeProject, activeWorkspace, activeSession, updateProject, model, t]);

    const resendLastSend = useCallback(() => {
        if (!activeSession || activeSession.streaming || !resendItemId) return;
        const last = lastSendRef.current;
        if (last && activeSession.localId === last.tag) {
            void last.retry();
            return;
        }
        // A no-reply row the hub stored (the window was closed, or the app quit
        // mid-turn) has no send in memory: send the message it answers again,
        // reusing its row. Its words are what the chat shows; attachments are not kept.
        const items = activeSession.items;
        const latest = items[items.length - 1];
        if (!latest || chatErrorCauseOf(latest).code !== 'no_reply') return;
        const asked = [...items].reverse().find((item) => item.kind === 'user');
        const text = asked?.content?.trim();
        if (!asked || !text) return;
        void executeSend(text, [], [], undefined, undefined, undefined, { item: asked, traceID: newTraceID() });
    }, [activeSession, resendItemId, executeSend]);

    /** Public: enqueue or send immediately. */
    const sendPromptAction = useCallback(async () => {
        if (isTodayBriefOpening()) return;
        setError(null);
        if (!prompt.trim() && images.length === 0 && files.length === 0) return;
        if (!activeProject) {
            setError('Please open a project first.');
            return;
        }
        if (!activeSession) {
            setError('No active session. Try creating a new session.');
            return;
        }

        // Main-chat navigation can resolve before React renders its selection.
        // Keep the generated text attached to that session until it is active.
        if (todayDraftRef.current && todayDraftRef.current.sessionId !== activeSession.localId) return;
        const todayDraft = todayDraftRef.current;
        if (todayDraft?.preview?.status === 'loading') return;
        const todayContext = todayDraft?.context;
        const draftPreview = todayDraft?.preview;
        const previewFields = draftPreview ? { todayPreview: draftPreview } : {};

        // A Today request is a new turn, never an answer or a dismissal of
        // the permission currently holding the main chat's existing turn,
        // even when it goes without context after its item became unavailable.
        if (todayDraft && hasPendingPermission) {
            const queued = { prompt: prompt.trim(), images: [...images], files: [...files], todayContext, ...previewFields };
            const q = getQueue(activeSession.localId);
            const editIdx = editingQueueIndexRef.current;
            if (editIdx !== null) {
                q.splice(Math.min(editIdx, q.length), 0, queued);
                editingQueueIndexRef.current = null;
            } else {
                q.push(queued);
            }
            syncQueueState();
            setPrompt('');
            todayDraftRef.current = null;
            setImages([]);
            setFiles([]);
            return;
        }

        // Intercept: if an AskUserQuestion is pending (not plan approval),
        // treat the typed text as an "Other" free-text response.
        //
        // AskUserQuestion permission options use the ID pattern "q<N>_other"
        // where N is the question index (0-3). The full protocol for free-text
        // responses is "<optionId>:<user text>" — e.g. "q0_other:my answer".
        // See also: handleRespondToPermission in useProjects.ts which parses
        // this format, and the backend permission handler which validates it.
        if (!todayDraft && hasPendingPermission && !awaitingPlanApproval && onRespondToPermission) {
            const otherOption = activeSession.permissionOptions?.find(
                (o) => o.id.endsWith('_other'),
            );
            if (otherOption) {
                const text = prompt.trim();
                // Only clear the composer if the response was actually
                // accepted. Returns false when a response is already
                // in-flight (permissionRespondingRef guard in useProjects).
                const accepted = await onRespondToPermission(`${otherOption.id}:${text}`);
                if (accepted) {
                    setPrompt('');
                    setImages([]);
                    setFiles([]);
                }
                return;
            }
        }

        // Intercept: if plan review card is showing, dismiss it first.
        if (awaitingPlanApproval && onDismissPlanReview) {
            await onDismissPlanReview();
            if (hasPendingPermission) {
                // Agent turn resumes after dismiss — queue feedback for when it ends.
                const newMsg: QueuedMessage = { prompt: prompt.trim(), images: [...images], files: [...files], todayContext, ...previewFields };
                getQueue(activeSession!.localId).push(newMsg);
                syncQueueState();
                setPrompt("");
                todayDraftRef.current = null;
                setImages([]);
                setFiles([]);
                return;
            }
            // Auto-triggered: turn already ended, proceed to send immediately.
        }

        const trimmedPrompt = prompt.trim();
        const pendingImages = [...images];
        const pendingFiles = [...files];

        // Clear composer immediately
        setPrompt('');
        todayDraftRef.current = null;
        setImages([]);
        setFiles([]);

        if (activeSession.streaming || preparingTodayRef.current.has(activeSession.localId) || [...failedSendsRef.current.values()].some((failed) => failed.tag === activeSession.localId)) {
            // Enqueue for later — message stays in pending area until sent
            const newMsg: QueuedMessage = { prompt: trimmedPrompt, images: pendingImages, files: pendingFiles, todayContext, ...previewFields };
            const q = getQueue(activeSession.localId);
            const editIdx = editingQueueIndexRef.current;
            if (editIdx !== null) {
                // Insert back at original position (clamped to current length)
                const insertAt = Math.min(editIdx, q.length);
                q.splice(insertAt, 0, newMsg);
                editingQueueIndexRef.current = null;
            } else {
                q.push(newMsg);
            }
            syncQueueState();
        } else {
            // Send directly
            await executeSend(trimmedPrompt, pendingImages, pendingFiles, todayContext,
                todayContext ? { prompt, revision: composerRevisionRef.current } : undefined, draftPreview);
        }
    }, [prompt, images, files, status, activeProject, activeSession, setError, executeSend, awaitingPlanApproval, hasPendingPermission, onDismissPlanReview, onRespondToPermission]);

    /** Dequeue next message when streaming ends. */
    useEffect(() => {
        const sid = activeSession?.localId;
        if (!sid) return;

        const wasStreaming = prevStreamingRef.current.get(sid) ?? false;
        const isStreaming = !!activeSession?.streaming || !!activeSession?.awaitingPlanApproval || !!hasPendingPermission || preparingTodayRef.current.has(sid);
        prevStreamingRef.current.set(sid, isStreaming);

        // Transition from streaming → not streaming
        if (wasStreaming && !isStreaming) {
            const q = getQueue(sid);
            // A rejected Today draft needs an explicit edit/send decision,
            // even after a later ordinary message finishes streaming.
            if (q[0]?.awaitingReview) return;
            const next = q.shift();
            if (next) {
                syncQueueState();
                const restore = next.todayContext?.offerReferences && !promptValueRef.current.trim() && images.length === 0 && files.length === 0
                    ? { prompt: next.prompt, revision: composerRevisionRef.current } : undefined;
                void executeSend(next.prompt, next.images, next.files, next.todayContext, restore, next.todayPreview);
            }
            if (q.length === 0) {
                messageQueueRef.current.delete(sid);
                prevStreamingRef.current.delete(sid);
            }
        }
    }, [activeSession?.localId, activeSession?.streaming, activeSession?.awaitingPlanApproval, hasPendingPermission, executeSend, images.length, files.length]);

    /** Sync displayed queue when switching sessions (preserve other sessions' queues). */
    useEffect(() => {
        editingQueueIndexRef.current = null;
        syncQueueState();
    }, [activeSession?.localId]);

    const handleCancel = () => {
        if (activeSession) {
            preparingTodayRef.current.delete(activeSession.localId);
            // Keep queued turns for review after stopping the current run.
            prevStreamingRef.current.delete(activeSession.localId);
        }
        editingQueueIndexRef.current = null;
        syncQueueState();

        if (activeSession?.currentRunId) {
            CancelStream(activeSession.currentRunId);
        } else if (activeSession) {
            // Run ID not yet available (run_created event hasn't flushed to
            // React state). Fall back to cancelling by session tag so the
            // backend context is cancelled immediately.
            CancelByTag(activeSession.localId);
        }
        if (activeProject && activeSession) {
            updateProject(activeProject.id, (p) =>
                mapSession(p, activeSession.localId, (s) => ({
                    ...s,
                    streaming: false,
                })),
            );
        }
    };

    const handleKeyDown = (
        event: React.KeyboardEvent<HTMLTextAreaElement>
    ) => {
        // Skip IME composition (e.g. Pinyin candidate selection). Some IMEs
        // (notably when typing Latin letters under a Pinyin layout) deliver a
        // plain-looking Enter without setting isComposing, so we also guard
        // against the legacy keyCode 229 signal.
        if (
            event.nativeEvent.isComposing ||
            event.nativeEvent.keyCode === 229
        ) {
            return;
        }
        // Send prompt keybinding (customizable, default: Enter)
        if (eventMatchesCombo(event.nativeEvent, sendPromptCombo)) {
            event.preventDefault();
            void sendPromptAction();
        }
    };

    /**
     * Send `text` as a new message without relying on the `prompt` React state.
     * Used by the empty-state suggestion chips: a click must fire the send on
     * the same tick, but `sendPromptAction` reads from the stale `prompt`
     * closure. If the session is streaming, the text is queued; otherwise it
     * is sent immediately via the same `executeSend` path as the normal flow.
     */
    const sendImmediate = useCallback(async (text: string, display?: string): Promise<boolean> => {
        if (isTodayBriefOpening()) return false;
        const trimmed = text.trim();
        if (!trimmed) return false;
        setError(null);
        if (!activeProject) {
            setError('Please open a project first.');
            return false;
        }
        if (!activeSession) {
            setError('No active session. Try creating a new session.');
            return false;
        }
        if (activeSession.streaming || preparingTodayRef.current.has(activeSession.localId) || [...failedSendsRef.current.values()].some((failed) => failed.tag === activeSession.localId)) {
            const q = getQueue(activeSession.localId);
            q.push({ prompt: trimmed, images: [], files: [] });
            syncQueueState();
            return true;
        }
        await executeSend(trimmed, [], [], undefined, undefined, undefined, undefined, display);
        return true;
    }, [activeProject, activeSession, executeSend, setError]);

    const removeQueuedMessage = useCallback((index: number) => {
        if (!activeSession) return;
        const q = getQueue(activeSession.localId);
        q.splice(index, 1);
        syncQueueState();
    }, [activeSession?.localId]);

    const editQueuedMessage = useCallback((index: number) => {
        if (!activeSession) return;
        const q = getQueue(activeSession.localId);
        const msg = q[index];
        if (!msg) return;
        // A queue edit replaces the composer. Resolve its own provenance
        // before writing text, without inheriting the draft currently open.
        const draft = msg.todayContext ? { sessionId: activeSession.localId, context: msg.todayContext, preview: msg.todayPreview }
            : todayDraftHistoryRef.current.get(msg.prompt) ?? null;
        editingQueueIndexRef.current = index;
        q.splice(index, 1);
        syncQueueState();
        composerRevisionRef.current += 1;
        promptRevisionRef.current += 1;
        todayDraftRef.current = draft;
        rememberTodayDraft(msg.prompt, draft);
        setTodayPreview(draft?.preview);
        setPromptState(msg.prompt);
        setImages(msg.images);
        setFiles(msg.files);
    }, [activeSession?.localId]);

    return {
        prompt,
        setPrompt,
        replacePrompt,
        prefillTodayPrompt,
        prefillOfferPrompt,
        prepareTodayPrompt,
        attachTodayBrief,
        getDraftRevision,
        beginTodayBrief,
        isTodayBriefOpening,
        todayPreview: todayDraftRef.current?.sessionId === activeSession?.localId ? todayPreview : undefined,
        images,
        setImages,
        files,
        setFiles,
        sendPrompt: sendPromptAction,
        sendImmediate,
        handleCancel,
        handleKeyDown,
        onTyping,
        queuedMessages,
        removeQueuedMessage,
        editQueuedMessage,
        retryFailedSend,
        editFailedSend,
        resendLastSend,
        resendItemId,
        forgetFailedSends,
    };
}
