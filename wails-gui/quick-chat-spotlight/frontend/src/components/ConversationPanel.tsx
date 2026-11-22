import React, { useEffect, useMemo, useReducer, useRef, useState } from "react";
import { useTranslation } from "@/i18n";
import { ArrowUp, Loader2, Mic, Square, X } from "lucide-react";
import { GroupedStreamList } from "@/components/grouped-stream-list";
import { PermissionQuestionCard } from "@/components/permission-question-card";
import { TypingIndicator } from "@/components/typing-indicator";
import { VoiceRecordingIndicator } from "@/components/voice-recording-indicator";
import { fromStreamItemData } from "@/lib/stream-helpers";
import { clearStreamEditDraft, getStreamEditDraft } from "@/lib/stream-edit-drafts";
import { useVoiceInput } from "@/hooks/useVoiceInput";
import { HideWindow } from "../lib/wails-app-stub";
import {
    subscribeStream,
    fetchSpotlightSnapshot,
    submitPrompt,
    newSpotlightRequestId,
    dismiss as dismissAPI,
    respondToPermission,
    cancel as cancelAPI,
    expand as expandAPI,
} from "../api/yansu";
import type { DetectionContext, DeliverableProposal, StreamEvent } from "../api/types";
import {
    appendUserMessage,
    clearPermission,
    editAndTruncate,
    initialStreamState,
    processAgentEvent,
    type StreamState,
} from "../chat/reducer";

interface Props {
    detection: DetectionContext | null;
    userPrompt: string;
    sessionLocalId: string | null;
    firstRequestId?: string | null;
    firstUserAck?: { clientId: string; itemId: string; sortOrder: number } | null;
    firstSendFailed?: boolean;
    firstSendPending?: boolean;
    onRetryFirst?: () => void;
    onClose: () => void;
}

type Action =
    | { type: "user_prompt"; prompt: string; clientId?: string }
    | { type: "ack_user"; clientId: string; itemId: string; sortOrder: number }
    | { type: "agent_event"; event: import("../api/types").AgentEvent }
    | { type: "replace_history"; items: import("@/types").StreamItem[]; streaming: boolean; runId?: string; sessionId?: string; permission?: import("../api/types").AgentEvent }
    | { type: "set_streaming"; streaming: boolean }
    | { type: "clear_permission"; eventId?: string }
    | { type: "edit_truncate"; itemId: string; newContent: string }
    | { type: "append_item"; item: import("@/types").StreamItem }
    | { type: "reset" };

function reduce(state: StreamState, action: Action): StreamState {
    switch (action.type) {
        case "user_prompt":
            return appendUserMessage(state, action.prompt, action.clientId);
        case "ack_user":
            return { ...state, items: state.items.map((item) => item.id === action.clientId ? { ...item, id: action.itemId, sortOrder: action.sortOrder } : item) };
        case "agent_event":
            return processAgentEvent(state, action.event);
        case "replace_history":
            {
                const restored = { ...state, items: action.items, streaming: action.streaming, runId: action.runId ?? "", sessionId: action.sessionId ?? "", permission: null, error: "" };
                return action.permission ? processAgentEvent(restored, action.permission) : restored;
            }
        case "set_streaming":
            return { ...state, streaming: action.streaming };
        case "clear_permission":
            return clearPermission(state, action.eventId);
        case "edit_truncate":
            return editAndTruncate(state, action.itemId, action.newContent);
        case "append_item":
            // A row the main chat wrote for this exchange (its sources line),
            // once, even when the snapshot already had it.
            if (state.items.some((item) => item.id === action.item.id)) return state;
            return { ...state, items: [...state.items, action.item] };
        case "reset":
            return initialStreamState;
    }
}

/**
 * ConversationPanel hosts the spotlight's chat surface (form 2 / 2.png).
 *
 * Layout (3 horizontal bands inside a 20px-radius card):
 *   ┌────────────────────────────────────────────────────────┐
 *   │ [×] App Name                                  [□]      │  53px header
 *   ├────────────────────────────────────────────────────────┤
 *   │   chat messages, scrollable                            │  flex: 1
 *   ├────────────────────────────────────────────────────────┤
 *   │ Refine or confirm…           [mic] [send]              │  57px input
 *   └────────────────────────────────────────────────────────┘
 *   shared panel ground (matches the pill), header/input separated by
 *   1px solid var(--color-border).
 *
 * The chat-rendering surface is still main UI's GroupedStreamList +
 * PermissionQuestionCard so message visuals match. The chrome (header
 * + input) is bespoke because the design is intentionally simpler than
 * main UI's full chat window.
 */
export function ConversationPanel({ detection, userPrompt, sessionLocalId, firstRequestId, firstUserAck, firstSendFailed = false, firstSendPending = false, onRetryFirst, onClose }: Props) {
    const { t } = useTranslation(["chat", "common"]);
    const [state, dispatch] = useReducer(reduce, initialStreamState);
    const [followup, setFollowup] = useState("");
    const [followupFailed, setFollowupFailed] = useState(false);
    const followupPendingRef = useRef<{ prompt: string; requestId: string } | null>(null);
    const followupInFlightRef = useRef(false);
    const [editFailed, setEditFailed] = useState(false);
    const editPendingRef = useRef<{ itemId: string; sortOrder: number; content: string; requestId: string; events: StreamEvent[] } | null>(null);
    const editInFlightRef = useRef(false);
    const [submitting, setSubmitting] = useState(false);
    const [streamFailure, setStreamFailure] = useState<"disconnected" | "history" | null>(null);
    const [streamAttempt, setStreamAttempt] = useState(0);
    const streamSequenceRef = useRef(0);
    const streamingRef = useRef(state.streaming);
    streamingRef.current = state.streaming;
    const resumeStreamingRef = useRef(false);
    const permissionPending = useRef(false);
    const [deliverable, setDeliverable] = useState<DeliverableProposal | null>(null);
    const closingRef = useRef(false);
    const bodyRef = useRef<HTMLDivElement>(null);
    const endRef = useRef<HTMLDivElement>(null);

    const appName = detection?.appName?.trim() || t("quickChat.title");

    /*
     * Seed the user bubble for the first turn from the App-level
     * `userPrompt` prop. The panel is permanently mounted in the stage,
     * so its mount-time effect runs with an empty userPrompt; the prop
     * watcher catches the first-prompt value when App sets it after
     * pill submission. ref-dedup prevents a re-dispatch if userPrompt
     * happens to revert. Follow-ups go through sendFollowup directly.
     */
    const seededPromptRef = useRef<string | null>(null);
    useEffect(() => {
        if (!userPrompt) return;
        if (seededPromptRef.current === userPrompt) return;
        seededPromptRef.current = userPrompt;
        dispatch({ type: "user_prompt", prompt: userPrompt, clientId: firstRequestId ?? undefined });
    }, [userPrompt, firstRequestId]);

    useEffect(() => {
        if (firstUserAck) dispatch({ type: "ack_user", ...firstUserAck });
    }, [firstUserAck]);

    useEffect(() => {
        if (!userPrompt || sessionLocalId) return;
        if (firstSendFailed || firstSendPending) dispatch({ type: "set_streaming", streaming: firstSendPending });
    }, [firstSendFailed, firstSendPending, sessionLocalId, userPrompt]);

    useEffect(() => {
        endRef.current?.scrollIntoView({ behavior: "smooth", block: "end" });
    }, [state.items.length, state.streaming]);

    const applyStreamEvent = (ev: StreamEvent) => {
        switch (ev.kind) {
            case "agent_event":
                if (ev.data.type === "run_created") streamingRef.current = true;
                if (["done", "close", "error", "auth_error"].includes(ev.data.type)) streamingRef.current = false;
                dispatch({ type: "agent_event", event: ev.data });
                break;
            case "error":
                streamingRef.current = false;
                dispatch({ type: "agent_event", event: { type: "error" } });
                break;
            case "done":
                streamingRef.current = false;
                dispatch({ type: "agent_event", event: { type: "done" } });
                break;
            case "permission_cleared":
                dispatch({ type: "clear_permission", eventId: ev.data.eventId });
                break;
            case "deliverable_proposed":
                setDeliverable(ev.data);
                break;
            case "item":
                dispatch({ type: "append_item", item: fromStreamItemData(ev.data) });
                break;
            case "user_message":
            case "assistant_started":
                break;
        }
    };

    useEffect(() => { streamSequenceRef.current = 0; setStreamFailure(null); }, [sessionLocalId]);

    useEffect(() => {
        if (!sessionLocalId) return;
        return subscribeStream(sessionLocalId, streamSequenceRef.current, (ev: StreamEvent, sequence) => {
            if (editPendingRef.current) editPendingRef.current.events.push(ev);
            else applyStreamEvent(ev);
            streamSequenceRef.current = sequence;
        }, (historyUnavailable) => {
            if (closingRef.current) return;
            resumeStreamingRef.current = streamingRef.current;
            dispatch({ type: "set_streaming", streaming: false });
            setStreamFailure(historyUnavailable ? "history" : "disconnected");
        });
    }, [sessionLocalId, streamAttempt]); // eslint-disable-line react-hooks/exhaustive-deps

    const retryStream = async () => {
        if (!sessionLocalId || submitting || followupPendingRef.current || editPendingRef.current) return;
        if (streamFailure === "history") {
            setSubmitting(true);
            try {
                const snapshot = await fetchSpotlightSnapshot(sessionLocalId);
                if (!Number.isSafeInteger(snapshot.sequence) || snapshot.sequence < 0 || !Array.isArray(snapshot.items)) throw new Error();
                if (typeof snapshot.streaming !== "boolean") throw new Error();
                dispatch({ type: "replace_history", items: snapshot.items.map(fromStreamItemData), streaming: snapshot.streaming, runId: snapshot.runId, sessionId: snapshot.sessionId, permission: snapshot.permission });
                streamSequenceRef.current = snapshot.sequence;
                streamingRef.current = snapshot.streaming;
                resumeStreamingRef.current = false;
            } catch {
                return;
            } finally {
                setSubmitting(false);
            }
        }
        setStreamFailure(null);
        if (resumeStreamingRef.current) dispatch({ type: "set_streaming", streaming: true });
        setStreamAttempt((attempt) => attempt + 1);
    };

    const attemptFollowup = async (pending: { prompt: string; requestId: string }) => {
        if (!sessionLocalId || followupInFlightRef.current) return;
        followupInFlightRef.current = true;
        setSubmitting(true);
        setFollowupFailed(false);
        try {
            const result = await submitPrompt(pending.prompt, detection?.bundleID ?? "", sessionLocalId, undefined, pending.requestId);
            if (result.userItemId && typeof result.sortOrder === "number") {
                dispatch({ type: "ack_user", clientId: pending.requestId, itemId: result.userItemId, sortOrder: result.sortOrder });
            }
            followupPendingRef.current = null;
        } catch {
            dispatch({ type: "set_streaming", streaming: false });
            setFollowupFailed(true);
        } finally {
            followupInFlightRef.current = false;
            setSubmitting(false);
        }
    };

    const sendFollowup = async () => {
        const trimmed = followup.trim();
        if (!trimmed || !sessionLocalId || state.streaming || submitting || streamFailure || followupPendingRef.current || editPendingRef.current) return;
        const pending = { prompt: trimmed, requestId: newSpotlightRequestId() };
        followupPendingRef.current = pending;
        setFollowup("");
        dispatch({ type: "user_prompt", prompt: trimmed, clientId: pending.requestId });
        await attemptFollowup(pending);
    };

    const handleClose = async () => {
        closingRef.current = true;
        await dismissAPI();
        try {
            await HideWindow();
        } catch {
            /* ignore */
        }
        onClose();
    };

    const handleMaximize = async () => {
        // Surface the main Yansu window and ask it to switch to this
        // spotlight chat session. The expand endpoint shows the main
        // window + emits a router event the main UI picks up; we then
        // hide the spotlight panel so focus lands on the main window.
        if (sessionLocalId) {
            await expandAPI(sessionLocalId);
        }
        closingRef.current = true;
        try {
            await HideWindow();
        } catch {
            /* ignore */
        }
        onClose();
    };

    const handlePermissionSelect = async (optionId: string) => {
        const sid = state.permission?.sessionId;
        const eventId = state.permission?.eventId;
        if (!sid || permissionPending.current) return false;
        permissionPending.current = true;
        try {
            await respondToPermission(sid, optionId, sessionLocalId ?? undefined);
            dispatch({ type: "clear_permission", eventId });
            return true;
        } catch {
            return false;
        } finally {
            permissionPending.current = false;
        }
    };

    const handlePermissionSkip = () => {
        dispatch({ type: "clear_permission" });
    };

    const handleCancel = async () => {
        if (!sessionLocalId) return;
        await cancelAPI(sessionLocalId);
    };

    const attemptEdit = async (pending: NonNullable<typeof editPendingRef.current>): Promise<boolean> => {
        if (!sessionLocalId || editInFlightRef.current) return false;
        editInFlightRef.current = true;
        setSubmitting(true);
        setEditFailed(false);
        try {
            await submitPrompt(pending.content, detection?.bundleID ?? "", sessionLocalId,
                { itemId: pending.itemId, sortOrder: pending.sortOrder }, pending.requestId);
            editPendingRef.current = null;
            dispatch({ type: "edit_truncate", itemId: pending.itemId, newContent: pending.content });
            dispatch({ type: "set_streaming", streaming: true });
            // Events from the canceled prior run may arrive while the edit is
            // pending. Only replay the new turn after the host's start marker.
            let start = pending.events.length - 1;
            while (start >= 0 && pending.events[start].kind !== "assistant_started") start--;
            if (start >= 0) for (const event of pending.events.slice(start + 1)) applyStreamEvent(event);
            if (getStreamEditDraft(pending.itemId)?.trim() === pending.content.trim()) clearStreamEditDraft(pending.itemId);
            return true;
        } catch {
            setEditFailed(true);
            return false;
        } finally {
            editInFlightRef.current = false;
            setSubmitting(false);
        }
    };

    const handleEditMessage = async (itemId: string, sortOrder: number, newContent: string): Promise<boolean> => {
        if (!sessionLocalId || streamFailure || followupPendingRef.current || submitting) return false;
        let pending = editPendingRef.current;
        if (pending && (pending.itemId !== itemId || pending.sortOrder !== sortOrder || pending.content !== newContent)) {
            setEditFailed(true);
            return false;
        }
        if (!pending) {
            pending = { itemId, sortOrder, content: newContent, requestId: newSpotlightRequestId(), events: [] };
            editPendingRef.current = pending;
        }
        return attemptEdit(pending);
    };

    /*
     * Voice input. Reuses main UI's useVoiceInput hook; spotlight's
     * wails-app-stub redirects TranscribeAudio + InitVoiceInput Wails
     * bindings through the host bridge to the main Yansu binary's
     * sherpa-onnx engine. Same engine, same UX as main composer.
     */
    const voice = useVoiceInput({
        onTranscriptUpdate: (text) => setFollowup(text),
    });

    const onInputKeyDown = (e: React.KeyboardEvent<HTMLTextAreaElement>) => {
        if (
            e.key === "Enter" &&
            !e.shiftKey &&
            !e.metaKey &&
            !e.ctrlKey &&
            !e.altKey
        ) {
            e.preventDefault();
            void sendFollowup();
        }
    };

    const handleVoiceClick = () => {
        if (voice.state === "recording") {
            void voice.stopAndTranscribe();
        } else if (voice.state === "idle") {
            void voice.startRecording();
        } else if (voice.error && voice.needsMicrophonePermission) {
            void voice.openMicrophoneSettings();
        } else if (voice.error) {
            void voice.retryInitialization();
        }
    };

    const drag = { "--wails-draggable": "drag" } as React.CSSProperties;
    const noDrag = { "--wails-draggable": "no-drag" } as React.CSSProperties;

    const canSend = followup.trim().length > 0 && !state.streaming && !submitting && !streamFailure && !followupPendingRef.current && !editPendingRef.current;

    // Mirrors main UI's stream-body.tsx condition: render the three-dot
    // typing indicator only in the gap between the user's message landing
    // and the agent producing its first content. As soon as any
    // assistant/thinking/tool item appears for the current turn, the
    // processing block itself takes over as the loading affordance.
    const showTypingIndicator = useMemo(() => {
        if (!state.streaming || state.items.length === 0) return false;
        let lastUserIndex = -1;
        for (let i = state.items.length - 1; i >= 0; i--) {
            if (state.items[i].kind === "user") {
                lastUserIndex = i;
                break;
            }
        }
        if (lastUserIndex < 0) return false;
        for (let i = lastUserIndex + 1; i < state.items.length; i++) {
            const k = state.items[i].kind;
            if (k === "assistant" || k === "thinking" || k === "tool") {
                return false;
            }
        }
        return true;
    }, [state.streaming, state.items]);

    return (
        <div className="conv">
            <header className="conv__header" style={drag}>
                <div className="conv__header-left" style={noDrag}>
                    <button
                        type="button"
                        className="conv__icon-btn conv__icon-btn--close"
                        aria-label={t("common:close")}
                        onClick={handleClose}
                    >
                        <X className="conv__icon conv__icon--close" />
                    </button>
                    <span className="conv__title">{appName}</span>
                </div>
                <div className="conv__header-right" style={noDrag}>
                    <button
                        type="button"
                        className="conv__icon-btn conv__icon-btn--chrome"
                        aria-label={t("quickChat.openInApp")}
                        title={t("quickChat.openInApp")}
                        onClick={handleMaximize}
                    >
                        <Square className="conv__icon conv__icon--chrome" />
                    </button>
                </div>
            </header>

            <div ref={bodyRef} className="conv__body">
                <GroupedStreamList
                    items={state.items}
                    streaming={state.streaming}
                    streamingStartedAt={state.streamingStartedAt}
                    onEditMessage={sessionLocalId ? handleEditMessage : undefined}
                />
                {showTypingIndicator && <TypingIndicator />}
                {(followupFailed || editFailed) && (
                    <div role="alert" className="flex items-center gap-2 text-xs text-subtle">
                        <span>{t("failure.confirmSent")}</span><span aria-hidden="true">·</span>
                        <button type="button" className="font-medium underline-offset-2 hover:underline" disabled={submitting} onClick={() => {
                            if (followupPendingRef.current) void attemptFollowup(followupPendingRef.current);
                            else if (editPendingRef.current) void attemptEdit(editPendingRef.current);
                        }}>{t("errors.actions.tryAgain")}</button>
                    </div>
                )}
                {streamFailure && (
                    <div role="alert" className="flex items-center gap-2 text-xs text-subtle">
                        <span>{t(streamFailure === "history" ? "failure.loadSpotlight" : "failure.streamStopped")}</span>
                        <button type="button" className="font-medium underline-offset-2 hover:underline" disabled={submitting || !!followupPendingRef.current || !!editPendingRef.current} onClick={() => void retryStream()}>{t("errors.actions.tryAgain")}</button>
                    </div>
                )}
                {firstSendFailed && onRetryFirst && (
                    <div role="alert" className="flex items-center gap-2 text-xs text-subtle">
                        <span>{t("failure.confirmSent")}</span><span aria-hidden="true">·</span>
                        <button type="button" className="font-medium underline-offset-2 hover:underline" onClick={onRetryFirst}>{t("errors.actions.tryAgain")}</button>
                    </div>
                )}
                {state.permission && (
                    <>
                    <PermissionQuestionCard
                        question={state.permission.toolName
                            ? t("quickChat.runTool", { tool: state.permission.toolName })
                            : t("quickChat.permissionRequested")}
                        options={state.permission.options}
                        onSelect={handlePermissionSelect}
                        onSkip={handlePermissionSkip}
                    />
                    </>
                )}
                {deliverable && (
                    <DeliverableCard
                        proposal={deliverable}
                        sessionLocalId={sessionLocalId}
                    />
                )}
                <div ref={endRef} />
            </div>

            <footer className="conv__input" style={noDrag}>
                <textarea
                    className="conv__textarea"
                    rows={1}
                    placeholder={t("quickChat.followupPlaceholder")}
                    value={followup}
                    onChange={(e) => setFollowup(e.target.value)}
                    onKeyDown={onInputKeyDown}
                />
                {voice.state === "recording" ? (
                    <VoiceRecordingIndicator
                        audioLevel={voice.audioLevel ?? 0}
                        onClick={handleVoiceClick}
                    />
                ) : (
                <button
                    type="button"
                    className="conv__icon-btn conv__icon-btn--input"
                    onClick={handleVoiceClick}
                    disabled={
                        voice.state === "transcribing" ||
                        voice.state === "initializing"
                    }
                    title={t("quickChat.voiceInput")}
                >
                    {voice.state === "transcribing" ||
                    voice.state === "initializing" ? (
                        <Loader2 className="conv__icon conv__icon--spin" />
                    ) : (
                        <Mic className="conv__icon" />
                    )}
                </button>
                )}
                {state.streaming ? (
                    <button
                        type="button"
                        className="conv__send"
                        onClick={handleCancel}
                        title={t("messageActions.stop")}
                    >
                        <Square className="conv__icon-light conv__icon-light--filled" />
                    </button>
                ) : (
                    <button
                        type="button"
                        className="conv__send"
                        onClick={() => void sendFollowup()}
                        disabled={!canSend}
                        title={t("quickChat.send")}
                    >
                        <ArrowUp className="conv__icon-light" />
                    </button>
                )}
            </footer>
            <style>{convStyles}</style>
        </div>
    );
}

function DeliverableCard({
    proposal,
    sessionLocalId,
}: {
    proposal: DeliverableProposal;
    sessionLocalId: string | null;
}) {
    const { t } = useTranslation("chat");
    return (
        <div className="mt-3 p-2.5 px-3 border border-accent bg-accent/10 rounded-[10px]">
            <div className="font-semibold mb-1 text-ink">{proposal.title}</div>
            <ol className="my-1 mb-2 ml-[18px] text-[12.5px] text-muted">
                {proposal.steps.map((s, i) => (
                    <li key={i}>{s}</li>
                ))}
            </ol>
            <button
                type="button"
                className="px-3 py-1 rounded-md bg-accent text-white text-[12.5px] disabled:opacity-50"
                disabled={!sessionLocalId}
                onClick={() => {
                    /* wired by buildSpotlightDeliverable in a follow-up commit */
                }}
            >
                {t("quickChat.build")}
            </button>
        </div>
    );
}

const convStyles = `
.conv {
    width: 100%;
    /* height: 100% (not 100vh) so the panel fills its layer
       container — the layer has a fixed 650px height in App.tsx so
       the panel layout stays at its intrinsic size even while the OS
       window is still pill-sized (cross-fade pre-roll). Using 100vh
       here would squash the panel down to ~88px during that moment
       and reflow it as the window grew, which was the source of the
       earlier mid-transition flicker. */
    height: 100%;
    box-sizing: border-box;
    background: var(--spotlight-surface);
    border-radius: 20px;
    display: flex;
    flex-direction: column;
    overflow: hidden;
    color: var(--color-ink);
}
.conv__header {
    height: 53px;
    flex-shrink: 0;
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 0 12px;
    border-bottom: 1px solid var(--color-border);
}
.conv__header-left {
    display: flex;
    align-items: center;
    gap: 8px;
    min-width: 0;
}
.conv__header-right {
    display: flex;
    align-items: center;
    gap: 4px;
    flex-shrink: 0;
}
.conv__title {
    font-size: 15px;
    font-weight: 500;
    color: var(--color-heading-strong);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
}
.conv__icon-btn {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 28px;
    height: 28px;
    border: none;
    background: transparent;
    border-radius: 6px;
    color: var(--color-ink);
    cursor: pointer;
    padding: 0;
    flex-shrink: 0;
}
.conv__icon-btn:hover:not(:disabled) {
    background: var(--color-surface-hover);
}
.conv__icon-btn:disabled {
    opacity: 0.5;
    cursor: default;
}
.conv__icon-btn--input {
    width: 32px;
    height: 32px;
}
.conv__icon-btn--chrome {
    width: 24px;
    height: 24px;
}
.conv__icon-btn--close {
    color: var(--color-heading-strong);
}
.conv__icon {
    width: 16px;
    height: 16px;
}
.conv__icon--chrome {
    width: 13px;
    height: 13px;
    color: var(--color-icon-subtle);
}
.conv__icon--close {
    width: 18px;
    height: 18px;
    color: var(--color-heading-strong);
}
.conv__icon--spin {
    animation: conv-spin 1s linear infinite;
}
@keyframes conv-spin {
    to { transform: rotate(360deg); }
}
.conv__body {
    flex: 1;
    overflow-y: auto;
    padding: 14px 16px;
    min-height: 0;
}
.conv__input {
    height: 57px;
    flex-shrink: 0;
    display: flex;
    align-items: center;
    gap: 6px;
    padding: 0 14px;
    border-top: 1px solid var(--color-border);
}
.conv__textarea {
    flex: 1;
    min-width: 0;
    height: 32px;
    border: none;
    outline: none;
    background: transparent;
    color: var(--color-ink);
    font-size: 14px;
    font-family: inherit;
    resize: none;
    padding: 0;
    line-height: 32px;
    overflow: hidden;
}
.conv__textarea::placeholder {
    color: var(--color-muted);
}
.conv__send {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 32px;
    height: 32px;
    border: none;
    background: var(--color-solid-btn-bg);
    border-radius: 8px;
    color: var(--color-solid-btn-fg);
    cursor: pointer;
    flex-shrink: 0;
    padding: 0;
    transition: filter 120ms ease;
}
.conv__send:hover:not(:disabled) {
    filter: brightness(0.92);
}
.conv__send:disabled {
    opacity: 0.4;
    cursor: default;
}
.conv__icon-light {
    width: 16px;
    height: 16px;
    color: var(--color-solid-btn-fg);
}
.conv__icon-light--filled {
    fill: currentColor;
}
`;
