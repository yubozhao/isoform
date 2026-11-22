import { startTransition, useCallback, useEffect, useRef, useState } from "react";
import { EventsOn } from "../../wailsjs/runtime/runtime";
import {
    UpdateSessionClaudeID,
    PersistStreamItem,
    GrowStreamItemContent,
} from "@/lib/wails-shim-app";
import { loadStreamItemsPageWithOpenAnswerDelivery } from "@/lib/open-answer-delivery";
import type {
    AgentStatus,
    AgentEvent,
    StreamItem,
    StreamItemKind,
    Project,
    Session,
} from "@/types";
import { buildId, toStreamItemData } from "@/lib/stream-helpers";
import { emptyStatus, logPersistError } from "@/lib/stream-helpers";

/** The assistant or thinking row a renderer is streaming. `seq` is the last
 *  chunk's place in the row (AgentEvent.item_seq). `stale` marks a row this
 *  renderer does not hold whole (it joined mid-row, or a chunk went missing):
 *  it shows the row but never writes its text, which the renderer that saw the
 *  row whole and the backend write. */
interface StreamingRow {
    id: string;
    cachedContent: string;
    runId: string;
    seq: number;
    stale: boolean;
}

/** A row's text to write out when it ends. */
interface RowFlush {
    id: string;
    content: string;
    stale: boolean;
}

interface StreamTracker {
    runId: string;
    claudeSessionId: string;
    assistantItem: StreamingRow | null;
    thinkingItem: StreamingRow | null;
    lastItemKind: string;
    lastItemRunId: string;
    /**
     * Maps a `tool_use_id` emitted by `tool_use_start` to the placeholder
     * StreamItem.id that was inserted into React state for it. When the
     * follow-up `tool_use` event arrives with the same tool_use_id, we
     * reuse the same StreamItem.id so persistence creates a single DB
     * row and the placeholder chip upgrades in place instead of
     * duplicating.
     */
    pendingToolUseStartIds: Map<string, string>;
}

/** A processed event waiting to be flushed to React state. */
interface PendingEvent {
    tag: string;
    type: string;
    /** Standard path: appends to session.items AND persists to DB. */
    itemToPersist: StreamItem | null;
    /**
     * UI-only placeholder appended to session.items but never persisted.
     * Used by `tool_use_start` to render a "Running command..." chip
     * during the input-JSON streaming window. The companion `tool_use`
     * event upgrades this item via `toolUseUpgrade` below.
     */
    placeholderItem?: StreamItem | null;
    /**
     * Upgrade an existing tool placeholder in state (matched by toolUseId)
     * to a fully-formed tool item, and persist that item to DB. Set when
     * a `tool_use` arrives after its `tool_use_start` companion.
     */
    toolUseUpgrade?: StreamItem | null;
    mergedAssistantContent: string | null;
    mergedThinkingContent: string | null;
    /**
     * A chunk of a stale row (see StreamingRow): appended to the row in state
     * by id, or the row is added with it. A reloaded window already has the
     * row's stored start in state and keeps it.
     */
    staleChunk?: StreamItem | null;
    newSessionId: string | null;
    /** Extra session fields to set (e.g. streaming, currentRunId, plan). */
    sessionPatch: Partial<Session> | null;
    /** When true, plan content should be extracted from items during flush. */
    extractPlanFromItems?: boolean;
}

interface UseAgentStreamingDeps {
    setProjects: React.Dispatch<React.SetStateAction<Project[]>>;
    // localId of the chat session the user is currently viewing. Auto-read
    // only speaks replies for this session — background automation (cron)
    // runs stream through the same agent:event channel under their own hidden
    // sessions, and must never be read aloud.
    activeSessionLocalId?: string | null;
    onSessionUnread?: (sessionLocalId: string) => void;
}

/** Maximum number of items kept in memory per session. */
const MAX_INMEMORY_ITEMS = 500;

/** Minimum content length to consider a Write tool output as plan content. */
const MIN_PLAN_CONTENT_LENGTH = 50;

/**
 * Extract plan content from session items by searching backwards for a Write
 * tool with substantial content, falling back to the last 3 assistant messages.
 */
function extractPlanContentFromItems(items: StreamItem[]): string {
    for (let i = items.length - 1; i >= 0; i--) {
        const item = items[i];
        if (item.kind === "tool" && item.toolName === "Write" && item.toolInput) {
            const input = item.toolInput as Record<string, unknown>;
            if (
                typeof input.content === "string" &&
                input.content.length > MIN_PLAN_CONTENT_LENGTH
            ) {
                return input.content;
            }
        }
    }
    const assistantItems = items
        .filter((item) => item.kind === "assistant" && item.content)
        .slice(-3);
    return assistantItems.map((item) => item.content).join("\n\n");
}

/** How often (ms) pending events are flushed to React state. */
const FLUSH_INTERVAL_MS = 150;

/** When, after a stale row (see StreamingRow) ends, a renderer re-reads it
 *  from storage: past the backend's write of a finished row's whole text
 *  (agentSegmentWriteDelay, 2 s, in agent_event_items.go), and once more in
 *  case that write was late. */
const STALE_ROW_RELOAD_MS = [2500, 5000];

/** Rows per page when paging back for stale rows, and the most pages read:
 *  a row the store never holds (empty text, rewound away) must not make each
 *  re-read walk the whole chat. */
const STALE_ROW_PAGE_SIZE = 100;
const STALE_ROW_MAX_PAGES = 10;

const utf8 = new TextEncoder();

/** UTF-8 byte length, as storage measures a row's text. */
function byteLength(text: string): number {
    return utf8.encode(text).length;
}

export function useAgentStreaming({
    setProjects,
    activeSessionLocalId,
    onSessionUnread,
}: UseAgentStreamingDeps) {
    const [status, setStatus] = useState<AgentStatus>(emptyStatus);

    // Read the latest active session id at event time without resubscribing.
    const activeSessionLocalIdRef = useRef<string | null>(
        activeSessionLocalId ?? null,
    );
    useEffect(() => {
        activeSessionLocalIdRef.current = activeSessionLocalId ?? null;
    }, [activeSessionLocalId]);
    const onSessionUnreadRef = useRef(onSessionUnread);
    useEffect(() => {
        onSessionUnreadRef.current = onSessionUnread;
    }, [onSessionUnread]);

    const streamTrackerRef = useRef<Map<string, StreamTracker>>(new Map());
    const flushTimerRef = useRef<Map<string, ReturnType<typeof setTimeout>>>(
        new Map(),
    );
    const pendingEventsRef = useRef<Map<string, PendingEvent[]>>(new Map());
    const flushScheduledRef = useRef<ReturnType<typeof setTimeout> | null>(
        null,
    );
    const persistenceQueueRef = useRef<Map<string, Promise<void>>>(new Map());
    // Stale rows per chat, re-read from storage once the turn ends.
    const staleRowsRef = useRef<Map<string, Set<string>>>(new Map());
    const persistenceErrorsRef = useRef<Map<string, unknown>>(new Map());

    const enqueueSessionPersistence = useCallback(
        (tag: string, operation: () => Promise<unknown>) => {
            const previous =
                persistenceQueueRef.current.get(tag) ?? Promise.resolve();
            const next = previous
                .catch(() => undefined)
                .then(operation)
                .then(() => undefined)
                .catch((error: unknown) => {
                    // Keep failures sticky for the current run. A later
                    // successful write may concern a different stream item
                    // and does not prove the history is complete enough to
                    // fork safely. run_created clears this only when the user
                    // explicitly retries with a new response.
                    persistenceErrorsRef.current.set(tag, error);
                    logPersistError(error);
                });
            persistenceQueueRef.current.set(tag, next);
            void next.finally(() => {
                if (persistenceQueueRef.current.get(tag) === next) {
                    persistenceQueueRef.current.delete(tag);
                }
            });
        },
        [],
    );

    const waitForSessionPersistence = useCallback(async (tag: string) => {
        while (true) {
            const pending = persistenceQueueRef.current.get(tag);
            if (!pending) break;
            await pending;
            if (persistenceQueueRef.current.get(tag) === pending) break;
        }
        const error = persistenceErrorsRef.current.get(tag);
        if (error) throw error;
    }, []);

    const getTracker = (tag: string): StreamTracker => {
        let t = streamTrackerRef.current.get(tag);
        if (!t) {
            t = {
                runId: "",
                claudeSessionId: "",
                assistantItem: null,
                thinkingItem: null,
                lastItemKind: "",
                lastItemRunId: "",
                pendingToolUseStartIds: new Map(),
            };
            streamTrackerRef.current.set(tag, t);
        }
        return t;
    };

    const initializeTracker = useCallback(
        (localId: string, sessionId: string) => {
            const t = getTracker(localId);
            t.claudeSessionId = sessionId;
        },
        [],
    );

    /** Clean up all refs associated with a session (call on session close). */
    const cleanupSession = useCallback((localId: string) => {
        const t = streamTrackerRef.current.get(localId);
        t?.pendingToolUseStartIds.clear();
        streamTrackerRef.current.delete(localId);
        pendingEventsRef.current.delete(localId);
        persistenceQueueRef.current.delete(localId);
        persistenceErrorsRef.current.delete(localId);
        staleRowsRef.current.delete(localId);

        // Clear only this session's pending flush timers.
        // Timer keys are prefixed with the session localId.
        const prefix = `${localId}:`;
        for (const [key, timer] of flushTimerRef.current) {
            if (key.startsWith(prefix)) {
                clearTimeout(timer);
                flushTimerRef.current.delete(key);
            }
        }
    }, []);

    /** Flush all pending assistant content to the database immediately.
     *  Call this before the app closes to prevent data loss from the 3s debounce. */
    const flushAllPendingContent = useCallback(() => {
        // Fire all pending debounced content writes immediately.
        for (const [key, timer] of flushTimerRef.current) {
            clearTimeout(timer);
            flushTimerRef.current.delete(key);
        }
        // Flush any in-flight content from stream trackers.
        for (const [tag, tracker] of streamTrackerRef.current) {
            if (
                tracker.assistantItem &&
                tracker.assistantItem.cachedContent &&
                !tracker.assistantItem.stale
            ) {
                const itemId = tracker.assistantItem.id;
                const content = tracker.assistantItem.cachedContent;
                enqueueSessionPersistence(tag, () =>
                    GrowStreamItemContent(tag, itemId, content),
                );
            }
            if (
                tracker.thinkingItem &&
                tracker.thinkingItem.cachedContent &&
                !tracker.thinkingItem.stale
            ) {
                const itemId = tracker.thinkingItem.id;
                const content = tracker.thinkingItem.cachedContent;
                enqueueSessionPersistence(tag, () =>
                    GrowStreamItemContent(tag, itemId, content),
                );
            }
        }
    }, [enqueueSessionPersistence]);

    const handleStatus = useCallback((next: AgentStatus) => {
        setStatus(next);
    }, []);

    // ── Apply pending events to React state in a single batch ──
    const flushPendingEvents = useCallback(() => {
        flushScheduledRef.current = null;
        const pending = pendingEventsRef.current;
        if (pending.size === 0) return;

        // Snapshot and clear the queue.
        const snapshot = new Map(pending);
        pending.clear();

        // Mark the flush as a transition. React 18's concurrent scheduler
        // can then interrupt the (potentially large) tree re-render between
        // component boundaries to yield for browser paint, so streaming
        // never visibly stalls on a heavy render. Claude.ai wraps its
        // stream-event dispatches in p.startTransition(...) for the same
        // reason — it's what lets the token stream stay smooth even when a
        // long assistant message causes the reconciler to do a lot of work.
        startTransition(() => {
        setProjects((prev) => {
            let anyProjectChanged = false;
            const next = prev.map((p) => {
                let projectChanged = false;
                const newWorkspaces = p.workspaces.map((w) => {
                    let workspaceChanged = false;
                    const newSessions = w.sessions.map((s) => {
                        const events = snapshot.get(s.localId);
                        if (!events || events.length === 0) return s;

                        // Single copy of the session — mutate items array in place.
                        const updated: Session = { ...s, items: [...s.items] };

                        for (const pe of events) {
                            if (pe.newSessionId) {
                                updated.sessionId = pe.newSessionId;
                            }

                            if (pe.sessionPatch) {
                                Object.assign(updated, pe.sessionPatch);
                                // Only show plan review when planMode is actually enabled.
                                // permission_request events with acceptEdits/plan options
                                // can arrive even without planMode (e.g. normal tool
                                // permission prompts), so guard against false positives.
                                if (
                                    pe.sessionPatch.awaitingPlanApproval &&
                                    !updated.planMode
                                ) {
                                    updated.awaitingPlanApproval = false;
                                }
                                // Extract plan content from items when flagged.
                                if (pe.extractPlanFromItems) {
                                    updated.planContent =
                                        extractPlanContentFromItems(
                                            updated.items,
                                        );
                                }
                            }

                            if (
                                pe.itemToPersist &&
                                !updated.items.some(
                                    (it) => it.id === pe.itemToPersist!.id,
                                )
                            ) {
                                updated.items.push(pe.itemToPersist);
                            }

                            if (pe.staleChunk) {
                                const chunk = pe.staleChunk;
                                const k = updated.items.findIndex(
                                    (it) => it.id === chunk.id,
                                );
                                if (k >= 0) {
                                    updated.items[k] = {
                                        ...updated.items[k],
                                        content:
                                            (updated.items[k].content ?? "") +
                                            (chunk.content ?? ""),
                                    };
                                } else {
                                    updated.items.push(chunk);
                                }
                            }

                            // tool_use_start: append placeholder chip into
                            // state so the UI renders "Running..." while
                            // the model streams the input JSON. The
                            // placeholder is not persisted to DB; the
                            // upgrade below replaces it once tool_use lands.
                            if (pe.placeholderItem) {
                                updated.items.push(pe.placeholderItem);
                            }

                            // tool_use after tool_use_start: find the
                            // existing placeholder by tool_use_id and
                            // replace it with the fully-formed tool item.
                            // Walk back-to-front because the placeholder
                            // is almost always the most recent tool item.
                            if (pe.toolUseUpgrade) {
                                const target = pe.toolUseUpgrade;
                                let upgraded = false;
                                for (
                                    let k = updated.items.length - 1;
                                    k >= 0;
                                    k--
                                ) {
                                    const it = updated.items[k];
                                    if (
                                        it.kind === "tool" &&
                                        it.toolUseId === target.toolUseId
                                    ) {
                                        updated.items[k] = target;
                                        upgraded = true;
                                        break;
                                    }
                                }
                                // Defensive: if the placeholder was already
                                // pruned (e.g. by MAX_INMEMORY_ITEMS), still
                                // append the upgrade so the call surfaces.
                                if (!upgraded) {
                                    updated.items.push(target);
                                }
                            }

                            if (pe.mergedAssistantContent !== null) {
                                for (
                                    let k = updated.items.length - 1;
                                    k >= 0;
                                    k--
                                ) {
                                    if (
                                        updated.items[k].kind === "assistant"
                                    ) {
                                        updated.items[k] = {
                                            ...updated.items[k],
                                            content:
                                                pe.mergedAssistantContent,
                                        };
                                        break;
                                    }
                                }
                            }

                            if (pe.mergedThinkingContent !== null) {
                                for (
                                    let k = updated.items.length - 1;
                                    k >= 0;
                                    k--
                                ) {
                                    if (
                                        updated.items[k].kind === "thinking"
                                    ) {
                                        updated.items[k] = {
                                            ...updated.items[k],
                                            content:
                                                pe.mergedThinkingContent,
                                        };
                                        break;
                                    }
                                }
                            }
                        }

                        // Auto-show plan review when turn ends in plan mode without ExitPlanMode
                        if (
                            updated.planMode &&
                            !updated.awaitingPlanApproval &&
                            !updated.streaming
                        ) {
                            const hasDoneEvent = events.some(
                                (e) => e.type === "done",
                            );
                            if (hasDoneEvent) {
                                const planText =
                                    extractPlanContentFromItems(updated.items);
                                if (planText) {
                                    updated.awaitingPlanApproval = true;
                                    updated.planContent = planText;
                                }
                            }
                        }

                        // Cap in-memory items to avoid unbounded growth.
                        // Preserve user messages so queries remain visible.
                        if (updated.items.length > MAX_INMEMORY_ITEMS) {
                            const cutIndex =
                                updated.items.length - MAX_INMEMORY_ITEMS;
                            const trimmed = updated.items.slice(0, cutIndex);
                            const kept = updated.items.slice(cutIndex);
                            const preservedUserMsgs = trimmed.filter(
                                (i) => i.kind === 'user',
                            );
                            updated.items = [
                                ...preservedUserMsgs,
                                ...kept,
                            ];
                            updated.hasMore = true;
                        }

                        updated.itemsLoaded = true;
                        updated.itemsLoading = false;
                        updated.streamItemCount = Math.max(
                            updated.streamItemCount,
                            updated.items.length,
                        );

                        workspaceChanged = true;
                        return updated;
                    });

                    if (!workspaceChanged) return w;
                    projectChanged = true;
                    return { ...w, sessions: newSessions };
                });

                if (!projectChanged) return p;
                anyProjectChanged = true;
                return { ...p, workspaces: newWorkspaces };
            });

            return anyProjectChanged ? next : prev;
        });
        });
    }, [setProjects]);

    /** Schedule a flush (or execute immediately for terminal events). */
    const scheduleFlush = useCallback(
        (immediate: boolean) => {
            if (immediate) {
                if (flushScheduledRef.current !== null) {
                    clearTimeout(flushScheduledRef.current);
                    flushScheduledRef.current = null;
                }
                flushPendingEvents();
                return;
            }
            if (flushScheduledRef.current === null) {
                flushScheduledRef.current = setTimeout(
                    flushPendingEvents,
                    FLUSH_INTERVAL_MS,
                );
            }
        },
        [flushPendingEvents],
    );

    /** Replaces the text of a chat's stale rows with what storage holds: a
     *  renderer that joined a row mid-way (a chat reopened, or a preview
     *  opened during a reply) shows only the chunks it saw, and the backend
     *  stores the row's whole text after the row ends. */
    const reloadStaleRows = useCallback(
        async (tag: string, ids: Set<string>) => {
            const stored = new Map<string, string>();
            try {
                // Page back from the newest rows until every stale row is
                // found, the chat has no older rows, or the page cap.
                let cursor = -1;
                for (let pages = 1; ; pages++) {
                    const page = await loadStreamItemsPageWithOpenAnswerDelivery(
                        tag,
                        cursor,
                        STALE_ROW_PAGE_SIZE,
                    );
                    for (const item of page.items ?? []) {
                        if (ids.has(item.id)) {
                            stored.set(item.id, item.content ?? "");
                        }
                    }
                    if (
                        stored.size >= ids.size ||
                        pages >= STALE_ROW_MAX_PAGES ||
                        !page.hasMore ||
                        (page.items ?? []).length === 0 ||
                        (cursor >= 0 && page.nextCursor >= cursor)
                    ) {
                        break;
                    }
                    cursor = page.nextCursor;
                }
            } catch (error) {
                logPersistError(error);
                return;
            }
            if (stored.size === 0) return;
            setProjects((prev) => {
                let changed = false;
                const next = prev.map((p) => ({
                    ...p,
                    workspaces: p.workspaces.map((w) => ({
                        ...w,
                        sessions: w.sessions.map((sess) => {
                            if (sess.localId !== tag) return sess;
                            let sessionChanged = false;
                            const items = sess.items.map((item) => {
                                const content = stored.get(item.id);
                                // A read before the backend wrote the whole
                                // text holds less than the screen; keep it.
                                // Measured in bytes, as storage grows rows.
                                if (
                                    content === undefined ||
                                    content === item.content ||
                                    byteLength(content) <
                                        byteLength(item.content ?? "")
                                ) {
                                    return item;
                                }
                                sessionChanged = true;
                                return { ...item, content };
                            });
                            if (!sessionChanged) return sess;
                            changed = true;
                            return { ...sess, items };
                        }),
                    })),
                }));
                return changed ? next : prev;
            });
        },
        [setProjects],
    );

    const handleEvent = useCallback(
        (evt: AgentEvent) => {
            const tag = evt.tag;
            if (!tag) return;

            const tracker = getTracker(tag);
            const runId = evt.run_id || tracker.runId || undefined;
            // The rows open before this event, to tell which it ends.
            const openRows = [tracker.assistantItem, tracker.thinkingItem];
            // A row built from this event is named after the event, so every
            // renderer showing the turn (the app window and a browser preview
            // over the bridge) writes the same id and storage keeps one row.
            const eventItemId = () => evt.event_id || buildId();
            const noteStaleRow = (id: string) => {
                let ids = staleRowsRef.current.get(tag);
                if (!ids) {
                    ids = new Set();
                    staleRowsRef.current.set(tag, ids);
                }
                ids.add(id);
            };
            // Grows the assistant or thinking row this chunk belongs to. The
            // backend names the row (item_id) and the chunk's place in it
            // (item_seq); without them a chunk continues the run's open row.
            // A row this renderer did not see from its first chunk on, or
            // that skipped a place, turns stale: shown, never written.
            const streamChunk = (
                kind: StreamItemKind,
                open: StreamingRow | null,
                text: string,
            ): {
                row: StreamingRow;
                created: StreamItem | null;
                merged: string | null;
                stale: StreamItem | null;
            } => {
                const seq = evt.item_seq ?? 0;
                const chunkItem = (id: string): StreamItem => ({
                    id,
                    kind,
                    content: text,
                    runId,
                    sessionId: evt.session_id,
                });
                const continues =
                    open !== null &&
                    (evt.item_id
                        ? open.id === evt.item_id
                        : open.runId === (runId ?? ""));
                if (open && continues) {
                    if (seq && seq !== open.seq + 1) open.stale = true;
                    open.seq = seq;
                    // Incremental append avoids O(N²) from repeated .join('').
                    open.cachedContent += text;
                    if (open.stale) {
                        return {
                            row: open,
                            created: null,
                            merged: null,
                            stale: chunkItem(open.id),
                        };
                    }
                    return {
                        row: open,
                        created: null,
                        merged: open.cachedContent,
                        stale: null,
                    };
                }
                const item = chunkItem(evt.item_id || eventItemId());
                const row: StreamingRow = {
                    id: item.id,
                    cachedContent: text,
                    runId: runId ?? "",
                    seq,
                    stale: seq > 1,
                };
                if (row.stale) {
                    return { row, created: null, merged: null, stale: item };
                }
                return { row, created: item, merged: null, stale: null };
            };

            // Always update tracker when a new session_id arrives (e.g. after
            // agent restart or resume that returns a different session ID).
            let newSessionId: string | null = null;
            if (evt.session_id && evt.session_id !== tracker.claudeSessionId) {
                tracker.claudeSessionId = evt.session_id;
                newSessionId = evt.session_id;
            }

            let itemToPersist: StreamItem | null = null;
            let placeholderItem: StreamItem | null = null;
            let toolUseUpgrade: StreamItem | null = null;
            let assistantItemToFlush: RowFlush | null = null;
            let thinkingItemToFlush: RowFlush | null = null;
            let mergedAssistantContent: string | null = null;
            let mergedThinkingContent: string | null = null;
            let staleChunk: StreamItem | null = null;
            let staleRowStart: StreamItem | null = null;
            let sessionPatch: Partial<Session> | null = null;
            let shouldExtractPlan = false;

            switch (evt.type) {
                case "run_created": {
                    if (evt.run_id) {
                        persistenceErrorsRef.current.delete(tag);
                        tracker.runId = evt.run_id;
                        const item: StreamItem = {
                            id: eventItemId(),
                            kind: "system" as StreamItemKind,
                            content: `Run created: ${evt.run_id.slice(0, 8)}`,
                            runId: evt.run_id,
                            sessionId: evt.session_id,
                        };
                        itemToPersist = item;
                        sessionPatch = {
                            streaming: true,
                            streamingStartedAt: Date.now(),
                            currentRunId: evt.run_id,
                            updatedAt: Date.now(),
                            slashCommandsLoading: true,
                            permissionRequestId: undefined,
                            permissionOptions: undefined,
                            permissionToolName: undefined,
                            permissionUpcoming: undefined,
                            awaitingPlanApproval: false,
                        };
                        tracker.lastItemKind = "system";
                        tracker.lastItemRunId = evt.run_id;
                    }
                    break;
                }
                case "assistant": {
                    // Assistant chunks end the thinking phase.
                    if (tracker.thinkingItem) {
                        thinkingItemToFlush = {
                            id: tracker.thinkingItem.id,
                            content: tracker.thinkingItem.cachedContent,
                            stale: tracker.thinkingItem.stale,
                        };
                        tracker.thinkingItem = null;
                    }
                    const chunk = streamChunk(
                        "assistant" as StreamItemKind,
                        tracker.assistantItem,
                        evt.content ?? "",
                    );
                    if (chunk.row !== tracker.assistantItem) {
                        tracker.assistantItem = chunk.row;
                        tracker.lastItemKind = "assistant";
                        tracker.lastItemRunId = runId ?? "";
                        // A stale row still gets stored if no renderer
                        // stored it; storage keeps the first write of an id.
                        if (chunk.stale) staleRowStart = chunk.stale;
                    }
                    itemToPersist = chunk.created;
                    mergedAssistantContent = chunk.merged;
                    staleChunk = chunk.stale;
                    if (chunk.row.stale) noteStaleRow(chunk.row.id);
                    break;
                }
                case "tool_use_start": {
                    // UI-only placeholder so the user sees a chip immediately
                    // when the model starts a tool block, instead of waiting
                    // for the entire input-JSON to finish streaming. The
                    // companion `tool_use` event upgrades this item in place
                    // (matched by tool_use_id) and is the one that gets
                    // persisted. Never persist this placeholder — empty
                    // ToolInput rows in the DB break history reload.
                    if (evt.tool_use_id) {
                        const placeholderId = evt.item_id || eventItemId();
                        tracker.pendingToolUseStartIds.set(
                            evt.tool_use_id,
                            placeholderId,
                        );
                        placeholderItem = {
                            id: placeholderId,
                            kind: "tool" as StreamItemKind,
                            toolName: evt.tool_name ?? "tool",
                            // Intentionally undefined; chip renders a
                            // "preparing" placeholder until tool_use arrives.
                            toolInput: undefined,
                            toolUseId: evt.tool_use_id,
                            runId,
                            sessionId: evt.session_id,
                        };
                        // Tool calls break assistant text continuity — the
                        // next assistant chunk must start a new item.
                        tracker.assistantItem = null;
                        tracker.thinkingItem = null;
                        tracker.lastItemKind = "tool";
                        tracker.lastItemRunId = runId ?? "";
                    }
                    break;
                }
                case "tool_use": {
                    // If we previously saw a tool_use_start for this id, the
                    // placeholder is already in state — reuse its StreamItem.id
                    // so we upgrade that row rather than duplicate it.
                    const placeholderId = evt.tool_use_id
                        ? tracker.pendingToolUseStartIds.get(evt.tool_use_id)
                        : undefined;
                    const id = placeholderId ?? (evt.item_id || eventItemId());
                    if (evt.tool_use_id && placeholderId) {
                        tracker.pendingToolUseStartIds.delete(evt.tool_use_id);
                    }
                    const item: StreamItem = {
                        id,
                        kind: "tool" as StreamItemKind,
                        toolName: evt.tool_name ?? "tool",
                        toolInput: evt.tool_input,
                        toolUseId: evt.tool_use_id,
                        runId,
                        sessionId: evt.session_id,
                    };
                    if (placeholderId) {
                        toolUseUpgrade = item;
                    } else {
                        itemToPersist = item;
                    }
                    // Tool calls break assistant text continuity — the next
                    // assistant chunk must start a new item.
                    tracker.assistantItem = null;
                    tracker.thinkingItem = null;
                    tracker.lastItemKind = "tool";
                    tracker.lastItemRunId = runId ?? "";
                    break;
                }
                case "thinking": {
                    if (evt.content) {
                        const chunk = streamChunk(
                            "thinking" as StreamItemKind,
                            tracker.thinkingItem,
                            evt.content,
                        );
                        if (chunk.row !== tracker.thinkingItem) {
                            tracker.thinkingItem = chunk.row;
                            tracker.lastItemKind = "thinking";
                            tracker.lastItemRunId = runId ?? "";
                            if (chunk.stale) staleRowStart = chunk.stale;
                        }
                        itemToPersist = chunk.created;
                        mergedThinkingContent = chunk.merged;
                        staleChunk = chunk.stale;
                        if (chunk.row.stale) noteStaleRow(chunk.row.id);
                    }
                    break;
                }
                case "system": {
                    if (evt.content) {
                        const item: StreamItem = {
                            id: eventItemId(),
                            kind: "system" as StreamItemKind,
                            content: evt.content,
                            runId,
                            sessionId: evt.session_id,
                        };
                        itemToPersist = item;
                        tracker.lastItemKind = "system";
                        tracker.lastItemRunId = runId ?? "";
                    }
                    break;
                }
                case "tool_result": {
                    const item: StreamItem = {
                        id: eventItemId(),
                        kind: "result" as StreamItemKind,
                        content: evt.content ?? "Result received.",
                        toolName: evt.tool_name,
                        toolUseId: evt.tool_use_id,
                        runId,
                        sessionId: evt.session_id,
                    };
                    itemToPersist = item;
                    tracker.lastItemKind = "result";
                    tracker.lastItemRunId = runId ?? "";
                    break;
                }
                case "error": {
                    // A send kept for want of an AI is shown under the kept
                    // message itself (KeptSendLine), not as an error row.
                    if (evt.code === "no_assistant") break;
                    const message = evt.error || evt.content || "";
                    const item: StreamItem = {
                        id: eventItemId(),
                        kind: "error" as StreamItemKind,
                        content: message,
                        runId,
                        sessionId: evt.session_id,
                        ...(evt.code || evt.assistant
                            ? { toolInput: { ...(evt.code ? { code: evt.code } : {}), ...(evt.assistant ? { assistant: evt.assistant } : {}) } }
                            : {}),
                    };
                    itemToPersist = item;
                    if (tracker.assistantItem) {
                        assistantItemToFlush = {
                            id: tracker.assistantItem.id,
                            content: tracker.assistantItem.cachedContent,
                            stale: tracker.assistantItem.stale,
                        };
                        tracker.assistantItem = null;
                    }
                    if (tracker.thinkingItem) {
                        thinkingItemToFlush = {
                            id: tracker.thinkingItem.id,
                            content: tracker.thinkingItem.cachedContent,
                            stale: tracker.thinkingItem.stale,
                        };
                        tracker.thinkingItem = null;
                    }
                    sessionPatch = {
                        streaming: false,
                        slashCommandsLoading: false,
                    };
                    tracker.lastItemKind = "error";
                    tracker.lastItemRunId = runId ?? "";
                    break;
                }
                case "done": {
                    let doneContent = "Run completed.";
                    if (evt.content?.startsWith("run_paused:wall_time")) {
                        const hasBgShells = evt.content.includes("bg_shells=");
                        doneContent = hasBgShells
                            ? "Run paused (wall-time limit). Background script still running."
                            : "Run paused (wall-time limit).";
                    }
                    const item: StreamItem = {
                        id: eventItemId(),
                        kind: "system" as StreamItemKind,
                        content: doneContent,
                        runId,
                        sessionId: evt.session_id,
                    };
                    itemToPersist = item;
                    if (tracker.assistantItem) {
                        assistantItemToFlush = {
                            id: tracker.assistantItem.id,
                            content: tracker.assistantItem.cachedContent,
                            stale: tracker.assistantItem.stale,
                        };
                        tracker.assistantItem = null;
                    }
                    if (tracker.thinkingItem) {
                        thinkingItemToFlush = {
                            id: tracker.thinkingItem.id,
                            content: tracker.thinkingItem.cachedContent,
                            stale: tracker.thinkingItem.stale,
                        };
                        tracker.thinkingItem = null;
                    }
                    sessionPatch = {
                        streaming: false,
                        slashCommandsLoading: false,
                    };
                    tracker.lastItemKind = "system";
                    tracker.lastItemRunId = runId ?? "";
                    break;
                }
                case "compact_start": {
                    sessionPatch = {
                        compactionState: "compacting",
                        compactionError: undefined,
                    };
                    break;
                }
                case "compact_end": {
                    sessionPatch = {
                        compactionState: "idle",
                        compactionError: undefined,
                    };
                    break;
                }
                case "compact_error": {
                    sessionPatch = {
                        compactionState: "error",
                        compactionError: evt.error ?? "compaction failed",
                    };
                    break;
                }
                case "plan_complete": {
                    const planText =
                        typeof evt.tool_input === "object" &&
                        evt.tool_input !== null
                            ? (((evt.tool_input as Record<string, unknown>)
                                  .plan as string) ?? "")
                            : "";
                    const item: StreamItem = {
                        id: eventItemId(),
                        kind: "system" as StreamItemKind,
                        content: planText || "Plan complete.",
                        runId,
                        sessionId: evt.session_id,
                    };
                    itemToPersist = item;
                    if (tracker.assistantItem) {
                        assistantItemToFlush = {
                            id: tracker.assistantItem.id,
                            content: tracker.assistantItem.cachedContent,
                            stale: tracker.assistantItem.stale,
                        };
                        tracker.assistantItem = null;
                    }
                    if (tracker.thinkingItem) {
                        thinkingItemToFlush = {
                            id: tracker.thinkingItem.id,
                            content: tracker.thinkingItem.cachedContent,
                            stale: tracker.thinkingItem.stale,
                        };
                        tracker.thinkingItem = null;
                    }
                    const planInput = evt.tool_input as Record<
                        string,
                        unknown
                    > | null;
                    sessionPatch = {
                        planContent: (planInput?.plan as string) ?? "",
                        planSessionId:
                            tracker.claudeSessionId || evt.session_id || "",
                        awaitingPlanApproval: true,
                    };
                    tracker.lastItemKind = "system";
                    tracker.lastItemRunId = runId ?? "";
                    break;
                }
                case "permission_request": {
                    // Interactive permission (ExitPlanMode / AskUserQuestion).
                    // No stream item to persist — handled purely via session state.
                    if (tracker.assistantItem) {
                        assistantItemToFlush = {
                            id: tracker.assistantItem.id,
                            content: tracker.assistantItem.cachedContent,
                            stale: tracker.assistantItem.stale,
                        };
                        tracker.assistantItem = null;
                    }
                    if (tracker.thinkingItem) {
                        thinkingItemToFlush = {
                            id: tracker.thinkingItem.id,
                            content: tracker.thinkingItem.cachedContent,
                            stale: tracker.thinkingItem.stale,
                        };
                        tracker.thinkingItem = null;
                    }

                    const options = evt.permission_options ?? [];
                    const isExitPlanMode = options.some(
                        (o) => o.id === "acceptEdits" || o.id === "plan",
                    );
                    const permPatch: Partial<Session> = {
                        permissionOptions: options,
                        permissionRequestId: evt.permission_request_id,
                        permissionToolName: evt.tool_name,
                        permissionUpcoming: evt.upcoming_approval,
                        streaming: false,
                        slashCommandsLoading: false,
                    };
                    if (isExitPlanMode) {
                        permPatch.awaitingPlanApproval = true;
                        permPatch.planSessionId =
                            tracker.claudeSessionId || evt.session_id || "";
                        shouldExtractPlan = true;
                    }
                    sessionPatch = permPatch;
                    break;
                }
                case "permission_resolved": {
                    // Another surface answered the same running-work request.
                    sessionPatch = {
                        permissionOptions: undefined,
                        permissionRequestId: undefined,
                        permissionToolName: undefined,
                        permissionUpcoming: undefined,
                        awaitingPlanApproval: false,
                    };
                    break;
                }
                case "available_commands": {
                    const commands = evt.available_commands ?? [];
                    const preview = commands
                        .slice(0, 8)
                        .map((c) => c.name)
                        .join(",");
                    // Debug hook for slash-command candidate pipeline.
                    console.debug(
                        "[slash-debug] received available_commands",
                        {
                            tag,
                            sessionId: evt.session_id ?? tracker.claudeSessionId,
                            count: commands.length,
                            names: preview,
                        },
                    );
                    sessionPatch = {
                        availableCommands: commands,
                        slashCommandsLoading: false,
                        slashCommandsLoaded: true,
                    };
                    break;
                }
                case "close": {
                    if (tracker.assistantItem) {
                        assistantItemToFlush = {
                            id: tracker.assistantItem.id,
                            content: tracker.assistantItem.cachedContent,
                            stale: tracker.assistantItem.stale,
                        };
                        tracker.assistantItem = null;
                    }
                    if (tracker.thinkingItem) {
                        thinkingItemToFlush = {
                            id: tracker.thinkingItem.id,
                            content: tracker.thinkingItem.cachedContent,
                            stale: tracker.thinkingItem.stale,
                        };
                        tracker.thinkingItem = null;
                    }
                    sessionPatch = {
                        streaming: false,
                        slashCommandsLoading: false,
                    };
                    break;
                }
            }

            // ── Enqueue the processed event for batched React state update ──
            const pe: PendingEvent = {
                tag,
                type: evt.type,
                itemToPersist,
                placeholderItem,
                toolUseUpgrade,
                mergedAssistantContent,
                mergedThinkingContent,
                staleChunk,
                newSessionId,
                sessionPatch,
                extractPlanFromItems: shouldExtractPlan || undefined,
            };

            let queue = pendingEventsRef.current.get(tag);
            if (!queue) {
                queue = [];
                pendingEventsRef.current.set(tag, queue);
            }
            queue.push(pe);

            // Terminal events flush immediately; streaming events are batched.
            const isTerminal =
                evt.type === "done" ||
                evt.type === "error" ||
                evt.type === "close" ||
                evt.type === "plan_complete" ||
                evt.type === "permission_request" ||
                evt.type === "permission_resolved" ||
                evt.type === "available_commands";
            scheduleFlush(isTerminal);

            // Spotlight-originated sessions are persisted server-side
            // (see spotlight_app.go:flushSpotlightItems) so the chat —
            // including tool_use, tool_result, and thinking rows the
            // main UI's processing-block needs to render step buttons —
            // survives even when the main UI is closed during the turn.
            // Skip the renderer's own write to avoid duplicate
            // stream_items rows.
            const isSpotlightTag = tag.startsWith("spotlight_");
            const shouldPersist = evt.persisted !== true && !isSpotlightTag;

            // ── Side effects (persistence, toasts) — fire immediately ──
            if (shouldPersist && newSessionId) {
                enqueueSessionPersistence(tag, () =>
                    UpdateSessionClaudeID(tag, newSessionId),
                );
            }
            if (shouldPersist && itemToPersist) {
                const item = toStreamItemData(itemToPersist);
                enqueueSessionPersistence(tag, () =>
                    PersistStreamItem(tag, item),
                );
            }
            if (shouldPersist && staleRowStart) {
                const item = toStreamItemData(staleRowStart);
                enqueueSessionPersistence(tag, () =>
                    PersistStreamItem(tag, item),
                );
            }
            // A tool_use that upgrades a prior tool_use_start placeholder is
            // the first time this tool call enters the DB. The placeholder
            // itself is never persisted (its ToolInput is empty and would
            // 400 on history reload). Reuses the placeholder's StreamItem.id
            // so subsequent GrowStreamItemContent calls target the right
            // row.
            if (shouldPersist && toolUseUpgrade) {
                const item = toStreamItemData(toolUseUpgrade);
                enqueueSessionPersistence(tag, () =>
                    PersistStreamItem(tag, item),
                );
            }
            if (
                shouldPersist &&
                assistantItemToFlush &&
                !assistantItemToFlush.stale
            ) {
                const flushKey = `${tag}:${assistantItemToFlush.id}`;
                const pending = flushTimerRef.current.get(flushKey);
                if (pending) {
                    clearTimeout(pending);
                    flushTimerRef.current.delete(flushKey);
                }
                const itemId = assistantItemToFlush.id;
                const content = assistantItemToFlush.content;
                enqueueSessionPersistence(tag, () =>
                    GrowStreamItemContent(tag, itemId, content),
                );
            }
            // Show the whole stored text of rows this renderer joined
            // mid-way: each when it ends (a tool call or a new row ends it),
            // and all of the turn's when the turn ends.
            const reloadLater = (ids: Set<string>) => {
                for (const ms of STALE_ROW_RELOAD_MS) {
                    setTimeout(() => void reloadStaleRows(tag, ids), ms);
                }
            };
            if (
                evt.type === "done" ||
                evt.type === "error" ||
                evt.type === "close"
            ) {
                const staleIds = staleRowsRef.current.get(tag);
                if (staleIds) {
                    staleRowsRef.current.delete(tag);
                    reloadLater(staleIds);
                }
            } else {
                const ended = openRows.filter(
                    (row): row is StreamingRow =>
                        row !== null &&
                        row.stale &&
                        row !== tracker.assistantItem &&
                        row !== tracker.thinkingItem,
                );
                if (ended.length > 0) {
                    reloadLater(new Set(ended.map((row) => row.id)));
                }
            }
            // Read the finished assistant reply aloud when auto-read is on.
            // A renderer that joined the reply mid-way holds only part of
            // it and does not read it.
            // Keyed by the stream item id so auto-play and the manual speaker
            // button share controls for this message without conflating two
            // different replies that happen to have identical content.
            if (
                evt.type === "done" &&
                assistantItemToFlush?.content &&
                tag !== activeSessionLocalIdRef.current
            ) {
                onSessionUnreadRef.current?.(tag);
            }
            if (
                shouldPersist &&
                thinkingItemToFlush &&
                !thinkingItemToFlush.stale
            ) {
                const flushKey = `${tag}:${thinkingItemToFlush.id}`;
                const pending = flushTimerRef.current.get(flushKey);
                if (pending) {
                    clearTimeout(pending);
                    flushTimerRef.current.delete(flushKey);
                }
                const itemId = thinkingItemToFlush.id;
                const content = thinkingItemToFlush.content;
                enqueueSessionPersistence(tag, () =>
                    GrowStreamItemContent(tag, itemId, content),
                );
            }

            // Debounced flush: persist accumulated content every 3s
            if (
                shouldPersist &&
                mergedAssistantContent !== null &&
                tracker.assistantItem
            ) {
                const itemId = tracker.assistantItem.id;
                const timerKey = `${tag}:${itemId}`;
                const content = mergedAssistantContent;
                const existing = flushTimerRef.current.get(timerKey);
                if (existing) clearTimeout(existing);
                flushTimerRef.current.set(
                    timerKey,
                    setTimeout(() => {
                        enqueueSessionPersistence(tag, () =>
                            GrowStreamItemContent(tag, itemId, content),
                        );
                        flushTimerRef.current.delete(timerKey);
                    }, 3000),
                );
            }
            if (
                shouldPersist &&
                mergedThinkingContent !== null &&
                tracker.thinkingItem
            ) {
                const itemId = tracker.thinkingItem.id;
                const timerKey = `${tag}:${itemId}`;
                const content = mergedThinkingContent;
                const existing = flushTimerRef.current.get(timerKey);
                if (existing) clearTimeout(existing);
                flushTimerRef.current.set(
                    timerKey,
                    setTimeout(() => {
                        enqueueSessionPersistence(tag, () =>
                            GrowStreamItemContent(tag, itemId, content),
                        );
                        flushTimerRef.current.delete(timerKey);
                    }, 3000),
                );
            }

        },
        [enqueueSessionPersistence, scheduleFlush, reloadStaleRows],
    );

    // Keep refs in sync so the stable subscription always calls the latest handlers.
    const handleEventRef = useRef(handleEvent);
    const handleStatusRef = useRef(handleStatus);
    useEffect(() => {
        handleEventRef.current = handleEvent;
    }, [handleEvent]);
    useEffect(() => {
        handleStatusRef.current = handleStatus;
    }, [handleStatus]);

    // Subscribe to Wails events — stable subscription that never re-creates.
    // Using refs avoids the unsubscribe/resubscribe gap that drops events
    // when the dependency chain (setProjects → flushPendingEvents →
    // scheduleFlush → handleEvent) causes handler recreation.
    useEffect(() => {
        const unsubStatus = EventsOn("agent:status", (s: AgentStatus) =>
            handleStatusRef.current(s),
        );
        const unsubEvent = EventsOn("agent:event", (evt: AgentEvent) =>
            handleEventRef.current(evt),
        );
        return () => {
            unsubStatus();
            unsubEvent();
            // Cancel any pending flush on unmount.
            if (flushScheduledRef.current !== null) {
                clearTimeout(flushScheduledRef.current);
            }
        };
        // eslint-disable-next-line react-hooks/exhaustive-deps
    }, []);

    return {
        status,
        setStatus,
        initializeTracker,
        cleanupSession,
        flushAllPendingContent,
        waitForSessionPersistence,
    };
}
