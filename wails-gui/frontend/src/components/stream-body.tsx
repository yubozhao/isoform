import { useRef, useState, useEffect, useLayoutEffect, useCallback, useMemo, startTransition } from 'react';
import { useTranslation } from 'react-i18next';
import { Virtuoso, type VirtuosoHandle } from 'react-virtuoso';
import { useReplyNavigation } from '@/hooks/useReplyNavigation';
import { JumpToNewest } from './jump-to-newest';
import { ScrollArea } from '@/components/ui/scroll-area';
import { StreamRowView, StreamRows, useStreamListModel, type StreamRow } from './grouped-stream-list';
import type { DeliverableActions } from './stream-card';
import { TypingIndicator } from './typing-indicator';
import { buildWelcomeContent, WelcomeSuggestions } from './first-chat-welcome';
import { Loader2 } from 'lucide-react';
import { useVimScroll } from '@/hooks/useVimScroll';
import { useUserProfile } from '@/hooks/useUserProfile';
import type { StreamItem } from '@/types';
import type { ExecutorType, MainChatReportTarget } from '@/lib/stream-helpers';
import { MainChatViewContext, type MainChatStreamView } from './main-chat/main-chat-view-context';
import { cn } from '@/lib/utils';

/** The most recent rows render fully so native find (Cmd+F) and
 *  cross-message copy keep working for recent history: the last three
 *  screens or the last RECENT_MIN_ROWS rows, whichever is more. Older
 *  history above them renders through the windowed list. */
const RECENT_MIN_ROWS = 150;
/** Streams longer than this stage their first screen (sync tail first, the
 *  rest in a transition) even when nothing windows; shorter streams render
 *  whole, exactly as before. */
const STAGED_MIN_ROWS = 24;
/** Estimated row height for the three-screen calculation. */
const ESTIMATED_ROW_HEIGHT = 240;
/** On the first commit only the tail of the recent section mounts (about one
 *  screenful — the part that is actually on screen); the rest of the recent
 *  section hydrates in a transition right after, so opening a long chat
 *  paints immediately and native find covers it a beat later. */
const SYNC_TAIL_ROWS = 4;
/** Windowed rows occupy the tail of a fixed index space; the index lowers
 *  only when real history prepends (load more), never on appends. */
const VIRTUAL_INDEX_BASE = 1_000_000;

interface StreamBodyProps {
    items: StreamItem[] | null;
    streaming: boolean;
    streamingStartedAt?: number;
    hasProject: boolean;
    executorType?: ExecutorType;
    /** The agent is still starting; a sent turn waits for it. */
    agentStarting?: boolean;
    vimMode?: boolean;
    onVimInsert?: () => void;
    hasMore?: boolean;
    loadingMore?: boolean;
    onLoadMore?: () => void;
    onEditMessage?: (itemId: string, sortOrder: number, newContent: string) => void;
    connectedProviders?: { slack?: string; feishu?: string };
    onFillComposer?: (prompt: string) => void;
    /** Show the welcome message only on the user's very first login (no prior chats). */
    isFirstLogin?: boolean;
    wideMode?: boolean;
    /**
     * Called when the user clicks a deliverable file chip in an assistant
     * message to open it. When undefined, chips render as non-interactive.
     */
    onOpenDeliverable?: (path: string) => void;
    /**
     * Rich deliverable actions (open / open-with / reveal + metadata). When
     * provided, deliverables render as Cola-style cards instead of plain chips.
     */
    deliverableActions?: DeliverableActions;
    /** Opens what a main-chat report line or a side-chat origin points at. */
    onOpenReportTarget?: (target: MainChatReportTarget) => void;
    /** "Use as my message" on a texted reply: puts its words in the composer. */
    onUseTextedReply?: (text: string) => void;
    onSendOpenAnswer?: (sessionId: string, text: string) => Promise<unknown>;
    /** Rendered above the conversation, inside the scroll area (the hub). */
    header?: React.ReactNode;
    /** Rendered after the conversation (the main chat's setup card); hidden
     *  while a reply is streaming. */
    footer?: React.ReactNode;
    /** Rendered above the scroll area, so it stays in place (the main chat's
     *  filter row). */
    toolbar?: React.ReactNode;
    /** Shown in the scroll area when there are no rows (an empty filter). */
    emptyState?: React.ReactNode;
    /** The active session, used to reset first-mount windowing on switch. */
    sessionLocalId?: string;
    /** The main chat's view (RC 2 D1): day dividers and folds, topic tags
     *  and prepared chips. Every other chat leaves it out. */
    mainChatView?: MainChatStreamView;
    /** A message to scroll to once it is rendered (Search opens a chat at a
     *  message); a new signal scrolls again. */
    focusItem?: { itemId: string; signal: number } | null;
    /** Called once the view scrolled to focusItem, so a later visit to the
     *  chat opens at the bottom again. */
    onFocusItemShown?: () => void;
}

/** The rendered element for a stream item, or for its turn's prompt when the
 *  item itself sits inside a folded block. */
function findRenderedItem(root: HTMLElement, items: StreamItem[], itemId: string): Element | null {
    const byId = (id: string) => {
        const wrapper = root.querySelector(`[data-stream-item-id="${id.replace(/["\\]/g, '\\$&')}"]`);
        // The wrapper is `display: contents`; scroll its card.
        return wrapper ? (wrapper.firstElementChild ?? wrapper) : null;
    };
    const found = byId(itemId);
    if (found) return found;
    const index = items.findIndex((item) => item.id === itemId);
    for (let i = index; i >= 0; i--) {
        if (items[i].kind === 'user') return byId(items[i].id);
    }
    return null;
}

export function StreamBody({
    items,
    streaming,
    streamingStartedAt = 0,
    hasProject,
    executorType = 'claude',
    agentStarting = false,
    vimMode = false,
    onVimInsert,
    hasMore = false,
    loadingMore = false,
    onLoadMore,
    onEditMessage,
    connectedProviders = {},
    onFillComposer,
    isFirstLogin = false,
    wideMode = false,
    onOpenDeliverable,
    deliverableActions,
    onOpenReportTarget,
    onUseTextedReply,
    onSendOpenAnswer,
    header,
    footer,
    toolbar,
    emptyState,
    mainChatView,
    sessionLocalId,
    focusItem,
    onFocusItemShown,
}: StreamBodyProps) {
    const { t } = useTranslation('chat');
    const hasItems = items !== null && items.length > 0;
    const bottomRef = useRef<HTMLDivElement>(null);
    const scrollAreaRef = useRef<HTMLDivElement | null>(null);
    const virtuosoRef = useRef<VirtuosoHandle>(null);
    const [viewportEl, setViewportEl] = useState<HTMLElement | null>(null);
    const scrollAreaCallbackRef = useCallback((node: HTMLDivElement | null) => {
        scrollAreaRef.current = node;
        setViewportEl(node?.querySelector<HTMLElement>('[data-radix-scroll-area-viewport]') ?? null);
    }, []);
    const { count, visible, jumpToLatest, userScrolledRef } = useReplyNavigation(items, scrollAreaRef, {
        enabled: hasProject,
        streaming,
        viewportSelector: '[data-radix-scroll-area-viewport]',
        bottom: bottomRef,
    });
    const { profile } = useUserProfile();
    const firstName = (profile?.display_name || profile?.name || '').split(' ')[0];

    // Synthetic welcome message shown when the session has no items yet.
    const welcomeItem = useMemo<StreamItem>(() => ({
        id: '__welcome__',
        kind: 'assistant',
        content: buildWelcomeContent(connectedProviders, firstName),
    }), [connectedProviders, firstName]);

    const showWelcome = isFirstLogin && hasProject && !hasItems;
    const effectiveItems = useMemo(() => {
        if (hasItems) {
            // Hide compaction bookkeeping rows from the conversation view.
            // They exist purely so the agent loop's history loader can rebuild
            // a compacted message slice on resume; the user already sees
            // compaction lifecycle via CompactionBanner. Rendering them as
            // generic system rows would dump a 5-8 KB summary blob into the
            // chat for no value.
            return items!.filter(
                (it) => it.kind !== 'compact_boundary' && it.kind !== 'compact_summary'
            );
        }
        if (showWelcome) return [welcomeItem];
        return [];
    }, [hasItems, items, showWelcome, welcomeItem]);

    const model = useStreamListModel(effectiveItems, streaming, streamingStartedAt, mainChatView, false);
    // Two independent stages: long streams stage their first screen (sync
    // tail first, the rest in a transition); streams longer than the recent
    // section additionally window the older history above it, keeping the
    // recent rows fully rendered (native find and cross-message copy).
    const staged = model.rows.length > STAGED_MIN_ROWS;
    const recentTarget = useMemo(() => {
        const screen = viewportEl?.clientHeight || 800;
        return Math.max(RECENT_MIN_ROWS, Math.ceil((screen * 3) / ESTIMATED_ROW_HEIGHT));
    }, [viewportEl]);
    const windowed = model.rows.length > RECENT_MIN_ROWS;
    const oldRows = windowed ? model.rows.slice(0, Math.max(0, model.rows.length - recentTarget)) : [];
    const recentRows = windowed ? model.rows.slice(oldRows.length) : model.rows;
    // The recent section hydrates in two phases: the on-screen tail mounts
    // with the first commit, the rest right after, inside a transition.
    const [findRowsMounted, setFindRowsMounted] = useState(false);
    const openedAtBottomRef = useRef(false);
    useEffect(() => {
        openedAtBottomRef.current = false;
        setFindRowsMounted(false);
    }, [sessionLocalId]);
    useEffect(() => {
        if (!staged || findRowsMounted) return;
        const timer = setTimeout(() => startTransition(() => setFindRowsMounted(true)), 0);
        return () => clearTimeout(timer);
    }, [staged, findRowsMounted]);
    const visibleRecentRows = findRowsMounted ? recentRows : recentRows.slice(Math.max(0, recentRows.length - SYNC_TAIL_ROWS));
    // Scroll anchor for the windowed old section: the row that was first when
    // the list opened. Its index shifts only when history prepends above it.
    const anchorRef = useRef<string | undefined>(undefined);
    let anchorIndex = anchorRef.current === undefined ? -1 : oldRows.findIndex((row) => row.key === anchorRef.current);
    if (anchorRef.current === undefined || anchorIndex < 0) {
        anchorRef.current = oldRows[0]?.key;
        anchorIndex = 0;
    }
    const firstItemIndex = VIRTUAL_INDEX_BASE - anchorIndex;
    const rowProps = useMemo(() => ({
        executorType,
        compact: false,
        splitLabels: undefined,
        hideUserPrompt: false,
        onEditMessage,
        onOpenDeliverable,
        deliverableActions,
        onOpenReportTarget,
        onUseTextedReply,
        onSendOpenAnswer,
        resultsByToolUseId: model.resultsByToolUseId,
        contextStatusesByUser: model.contextStatusesByUser,
        showChips: Boolean(mainChatView),
    }), [executorType, onEditMessage, onOpenDeliverable, deliverableActions, onOpenReportTarget, onUseTextedReply, onSendOpenAnswer, model.resultsByToolUseId, model.contextStatusesByUser, mainChatView]);

    // Track scrollHeight before DOM update for scroll-position preservation on prepend.
    const prevScrollHeightRef = useRef<number>(0);
    const pendingPrependRef = useRef(false);
    const prevItemCountRef = useRef(0);

    // Scroll to the message Search opened, after the session-switch reset
    // above and the prepend adjustment below, and stop following the bottom.
    const focusItemId = focusItem?.itemId;
    const focusSignal = focusItem?.signal;
    useEffect(() => {
        if (!focusItemId) return;
        // The windowed list mounts one commit after the scroll area (it needs
        // the viewport), and the recent section hydrates a beat later; wait
        // for both so the target row exists to scroll to.
        if (staged && (!viewportEl || !findRowsMounted)) return;
        userScrolledRef.current = true;
        if (windowed) {
            const index = model.rowIndexByItemId.get(focusItemId);
            if (index !== undefined && index < oldRows.length) {
                virtuosoRef.current?.scrollToIndex({ index, align: 'center', behavior: 'auto' });
            }
        }
        // The windowed list mounts the target row on the next frame, so the
        // fine scroll runs one frame later there than in the plain list.
        const fineScroll = () => {
            const root = scrollAreaRef.current;
            if (!root) return;
            findRenderedItem(root, items ?? [], focusItemId)?.scrollIntoView({ block: 'center', behavior: 'instant' as ScrollBehavior });
            onFocusItemShown?.();
        };
        const frame = staged
            ? requestAnimationFrame(() => requestAnimationFrame(fineScroll))
            : requestAnimationFrame(fineScroll);
        return () => cancelAnimationFrame(frame);
        // Only a new focus request scrolls; later item updates must not.
        // eslint-disable-next-line react-hooks/exhaustive-deps
    }, [focusItemId, focusSignal, staged, windowed, viewportEl, findRowsMounted]);

    useVimScroll(vimMode, scrollAreaRef, onVimInsert);

    // Listen to scroll events on the Radix viewport
    useEffect(() => {
        const viewport = scrollAreaRef.current?.querySelector(
            '[data-radix-scroll-area-viewport]'
        );
        if (!viewport) return;

        const handleScroll = () => {
            // Trigger loading older messages when scrolled near the top
            if (
                viewport.scrollTop < 200 &&
                hasMore &&
                !loadingMore &&
                onLoadMore
            ) {
                onLoadMore();
            }
        };

        viewport.addEventListener('scroll', handleScroll, { passive: true });
        return () => viewport.removeEventListener('scroll', handleScroll);
    }, [hasMore, loadingMore, onLoadMore]);

    // Capture scrollHeight before a prepend render (items grew at the beginning).
    // We detect a prepend when item count increases but the first item ID changed.
    // The windowed old section keeps the prepend anchor itself (VIRTUAL_INDEX_BASE).
    useEffect(() => {
        const currentCount = items?.length ?? 0;
        if (!windowed && currentCount > prevItemCountRef.current && prevItemCountRef.current > 0) {
            // Items were added — check if it's a prepend (first item ID differs).
            // We set the flag; the layout effect below will adjust scrollTop.
            const viewport = scrollAreaRef.current?.querySelector(
                '[data-radix-scroll-area-viewport]'
            );
            if (viewport) {
                prevScrollHeightRef.current = viewport.scrollHeight;
                pendingPrependRef.current = true;
            }
        }
        prevItemCountRef.current = currentCount;
    }, [items, windowed]);

    // After DOM update: restore scroll position if items were prepended.
    useLayoutEffect(() => {
        if (!pendingPrependRef.current) return;
        pendingPrependRef.current = false;

        const viewport = scrollAreaRef.current?.querySelector(
            '[data-radix-scroll-area-viewport]'
        );
        if (!viewport) return;

        const newScrollHeight = viewport.scrollHeight;
        const delta = newScrollHeight - prevScrollHeightRef.current;
        if (delta > 0) {
            viewport.scrollTop += delta;
        }
    });

    // Auto-scroll to bottom when items change (unless user scrolled up).
    //
    // Streaming note: items[] gets a new reference on every ~150ms flush, so
    // this effect fires constantly during a response. Using
    // `behavior: 'smooth'` here starts a ~300ms CSS-driven scroll animation
    // each time — overlapping animations cancel/restart each other and each
    // call forces a synchronous layout to find the target. That pileup burns
    // the main thread and starves the rAF-driven streaming pacer, which is
    // what shows up as visible token stalls during streaming. Coalesce to
    // one scroll per frame and use instant behavior so the browser paints
    // right away instead of queuing animations.
    const scrollRafRef = useRef<number | null>(null);
    const snapToBottom = useCallback(() => {
        if (userScrolledRef.current) return;
        if (scrollRafRef.current != null) return;
        scrollRafRef.current = requestAnimationFrame(() => {
            scrollRafRef.current = null;
            if (userScrolledRef.current) return;
            bottomRef.current?.scrollIntoView({ block: 'end', behavior: 'instant' as ScrollBehavior });
        });
    }, []);
    useEffect(() => {
        snapToBottom();
    }, [items, streaming, snapToBottom]);

    // Track height changes inside the scroll viewport. The items effect only
    // fires on ~150ms flushes, but the paced markdown tail in MarkdownContent
    // grows the DOM height every rAF as more text is revealed — without a
    // height-driven trigger the bottom anchor drifts out of view between
    // flushes and the next flush has to snap it back, which reads as the
    // visible "上下跳动". ResizeObserver fires synchronously after layout so
    // we follow growth at the same cadence the content produces it.
    useEffect(() => {
        const viewport = scrollAreaRef.current?.querySelector(
            '[data-radix-scroll-area-viewport]'
        );
        if (!viewport) return;
        const inner = viewport.firstElementChild;
        if (!inner) return;
        const ro = new ResizeObserver(() => snapToBottom());
        ro.observe(inner);
        return () => ro.disconnect();
    }, [snapToBottom]);

    // Open at the bottom once the recent section has hydrated. The staged
    // list's own initial position is not the bottom, and the programmatic
    // scroll events getting there must not read as the user scrolling away
    // (which would silence the bottom anchor from then on).
    useEffect(() => {
        if (!staged || !findRowsMounted || openedAtBottomRef.current) return;
        openedAtBottomRef.current = true;
        userScrolledRef.current = false;
        snapToBottom();
    }, [staged, findRowsMounted, snapToBottom, userScrolledRef]);

    useEffect(
        () => () => {
            if (scrollRafRef.current != null) {
                cancelAnimationFrame(scrollRafRef.current);
                scrollRafRef.current = null;
            }
        },
        [],
    );

    // Typing indicator shows only in the brief gap between user sending a message
    // and the first agent activity arriving for the current turn. Once any
    // thinking/tool/assistant item exists after the last user message, the
    // processing block itself serves as the loading state.
    const showTypingIndicator = useMemo(() => {
        if (!streaming || !items || items.length === 0) return false;
        let lastUserIndex = -1;
        for (let i = items.length - 1; i >= 0; i--) {
            if (items[i].kind === 'user') { lastUserIndex = i; break; }
        }
        if (lastUserIndex < 0) return false;
        for (let i = lastUserIndex + 1; i < items.length; i++) {
            if (items[i].kind === 'assistant' || items[i].kind === 'thinking' || items[i].kind === 'tool') {
                return false;
            }
        }
        return true;
    }, [streaming, items]);

    const tail = (
        <>
            {footer && !streaming ? <div className="mt-2">{footer}</div> : null}
            {/* Suggestion chips shown below the welcome message */}
            {showWelcome && (
                <WelcomeSuggestions onFillComposer={onFillComposer} />
            )}
            {showTypingIndicator && <TypingIndicator executorType={executorType} startingUp={agentStarting} />}
            <div ref={bottomRef} className="h-px" />
        </>
    );
    const itemContent = useCallback((_index: number, row: StreamRow) => (
        <div className="pb-4">
            <StreamRowView row={row} {...rowProps} />
        </div>
    ), [rowProps]);

    // Long streams stage: the on-screen tail of the recent section mounts
    // first, the rest hydrates in a transition; streams longer than the
    // recent section also window their older history. Short streams render
    // whole, exactly as before.
    const list = staged ? (
        viewportEl && (
            <div className={cn('px-8 pb-6 w-full', !wideMode && 'max-w-3xl mx-auto')}>
                {(header || loadingMore) && (
                    <div className="pb-4">
                        {header ? <div className={loadingMore ? 'mb-6' : 'mb-2'}>{header}</div> : null}
                        {/* Loading spinner for older messages */}
                        {loadingMore && (
                            <div className="flex justify-center py-2">
                                <Loader2 className="size-4 animate-spin text-muted" />
                            </div>
                        )}
                    </div>
                )}
                {windowed && (
                    <Virtuoso
                        ref={virtuosoRef}
                        customScrollParent={viewportEl}
                        data={oldRows}
                        firstItemIndex={firstItemIndex}
                        initialTopMostItemIndex={oldRows.length - 1}
                        computeItemKey={(_index: number, row: StreamRow) => row.key}
                        increaseViewportBy={{ top: 300, bottom: 300 }}
                        defaultItemHeight={ESTIMATED_ROW_HEIGHT}
                        itemContent={itemContent}
                    />
                )}
                {visibleRecentRows.map((row) => (
                    <div key={row.key} className="pb-4">
                        <StreamRowView row={row} {...rowProps} />
                    </div>
                ))}
                {tail}
            </div>
        )
    ) : (
        <div className={`px-8 pb-6 flex flex-col gap-4 w-full${wideMode ? "" : " max-w-3xl mx-auto"}`}>
            {header ? <div className="mb-2">{header}</div> : null}
            {/* Loading spinner for older messages */}
            {loadingMore && (
                <div className="flex justify-center py-2">
                    <Loader2 className="size-4 animate-spin text-muted" />
                </div>
            )}
            <StreamRows rows={model.rows} {...rowProps} />
            {effectiveItems.length === 0 && !loadingMore && emptyState}
            {tail}
        </div>
    );

    return (
        <div className="flex-1 min-h-0 flex flex-col">
            {toolbar}
            <div className="flex-1 min-h-0 relative">
                {!hasProject ? (
                    <div className="h-full flex items-center justify-center text-muted text-center">
                        <p>{t('stream.openProject')}</p>
                    </div>
                ) : (
                <ScrollArea ref={scrollAreaCallbackRef} className="h-full">
                    {/* The top padding is empty space under the title strip, so
                        it drags the window across the full width. */}
                    <div aria-hidden className="window-drag h-6" />
                    {mainChatView ? (
                        <MainChatViewContext.Provider value={mainChatView}>{list}</MainChatViewContext.Provider>
                    ) : list}
                </ScrollArea>
                )}
                <JumpToNewest count={count} visible={visible} onJump={jumpToLatest} />
            </div>
        </div>
    );
}
