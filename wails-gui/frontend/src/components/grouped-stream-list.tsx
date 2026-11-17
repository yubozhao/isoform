import { Fragment, memo, useCallback, useMemo, useRef } from 'react';
import { useTranslation } from 'react-i18next';
import {
    groupStreamItems,
    splitIntoTurns,
    turnHasContent,
    turnHasFinals,
    isTodayContextItem,
} from '@/lib/stream-helpers';
import { getMainChatProactive, getMainChatStatus } from '@/lib/stream-helpers';
import { buildMainChatDays, chatThreadUpdates, mainChatStreamRows } from '@/lib/main-chat-view';
import { StreamCard } from './stream-card';
import type { DeliverableActions } from './stream-card';
import { ProcessingBlock } from './processing-block';
import { MainChatStatusRow } from './main-chat-proactive-card';
import { MainChatAnswerChips } from './main-chat/main-chat-answer-chips';
import { MainChatDayDivider, MainChatDayFold, RemovedUpdatesLine } from './main-chat/main-chat-day';
import { useMainChatStreamView, type MainChatStreamView } from './main-chat/main-chat-view-context';
import type { StreamItem } from '@/types';
import type { ExecutorType, GroupedStreamItem, MainChatReportTarget, Turn } from '@/lib/stream-helpers';

export type ToolResultsMap = Map<string, StreamItem>;

/**
 * Which rows a day shows. With updates shown, a day with any row keeps its
 * divider, even when its updates are folded away or withdrawn (R63,
 * 2026-11-05). With them out of Chat, only a day with conversation has a
 * divider, and no fold or removed-updates line shows.
 */
export function dayRowsVisibility(
    day: { items: readonly unknown[]; routine: readonly unknown[]; withdrawn?: number; withheld?: number },
    hideRoutine: boolean,
    updatesShown: boolean = chatThreadUpdates.shown,
): { divider: boolean; fold: boolean; removed: boolean } {
    if (!updatesShown) return { divider: day.items.length > 0, fold: false, removed: false };
    return {
        divider: day.items.length > 0 || day.routine.length > 0 || (day.withdrawn ?? 0) > 0 || (day.withheld ?? 0) > 0,
        fold: day.routine.length > 0 && !hideRoutine,
        removed: (day.withdrawn ?? 0) > 0,
    };
}

function isTodayContextStatus(entry: GroupedStreamItem): boolean {
    return entry.type === 'single' && isTodayContextItem(entry.item);
}

interface GroupedStreamListProps {
    items: StreamItem[];
    streaming: boolean;
    streamingStartedAt?: number;
    executorType?: ExecutorType;
    onEditMessage?: (itemId: string, sortOrder: number, newContent: string) => void;
    /** Render all cards in compact mode (no avatars, no bubbles, left-aligned). */
    compact?: boolean;
    /**
     * Optional uppercase section labels rendered before the first turn's user
     * prompt and its output (processing + finals). Applied only to turn 0 so
     * follow-up turns (e.g. from "Continue in chat") do not repeat them.
     * Leave undefined to render without labels (default).
     */
    splitLabels?: { prompt?: string; output?: string };
    /**
     * Called when the user clicks a deliverable file chip in an assistant
     * message. When undefined, chips render as non-interactive spans.
     */
    onOpenDeliverable?: (path: string) => void;
    /**
     * Rich deliverable actions (open / open-with / reveal + metadata). When
     * provided, deliverables render as Cola-style cards instead of plain chips.
     */
    deliverableActions?: DeliverableActions;
    /**
     * When true, omit the user-prompt turn(s) from the rendered output. Used
     * by the cron run-history list where every run replays the same prompt —
     * surfacing it on each row is pure noise.
     */
    hideUserPrompt?: boolean;
    /** Opens what a main-chat report line or a side-chat origin points at. */
    onOpenReportTarget?: (target: MainChatReportTarget) => void;
    /** "Use as my message" on a texted reply: puts its words in the composer. */
    onUseTextedReply?: (text: string) => void;
    onSendOpenAnswer?: (sessionId: string, text: string) => Promise<unknown>;
}

function SectionLabel({ children }: { children: React.ReactNode }) {
    return (
        <h3 className="text-[10px] font-medium text-muted uppercase tracking-wider">
            {children}
        </h3>
    );
}

// ── Row model ───────────────────────────────────────────────────────
// The stream renders as a flat list of rows: a day divider per local day, the
// day's folded routine updates, then the day's turns. StreamBody windows this
// list once it grows past the small-chat threshold, so the row shape lives
// here, next to the grouping logic both paths share.

export type StreamRow =
    | { type: 'day-divider'; key: string; day: string; today: string }
    | { type: 'day-fold'; key: string; day: string; routine: StreamItem[]; routineCount: number }
    | { type: 'day-removed'; key: string; day: string; count: number }
    | {
          type: 'turn';
          key: string;
          turn: Turn;
          /** Session-wide streaming flag: the App rejects every edit while a
           *  reply streams, so edit/retry stays locked on all turns. */
          sessionStreaming: boolean;
          streamingTurn: boolean;
          streamingStartedAt: number;
          isFirstTurn: boolean;
          isLastTurn: boolean;
      };

export interface StreamListModel {
    rows: StreamRow[];
    resultsByToolUseId: ToolResultsMap;
    contextStatusesByUser: Map<string, StreamItem[]>;
    /** User prompt and final-answer item id -> row index, for scroll-to-message. */
    rowIndexByItemId: Map<string, number>;
}

/** Reuses the previous map when every entry survived the rebuild unchanged
 *  (stream flushes replace only the items they touch), so rows depending on
 *  the map keep a stable prop across flushes. */
function useStabilizedMap<K>(next: Map<K, StreamItem>, ref: { current: Map<K, StreamItem> }): Map<K, StreamItem> {
    const prev = ref.current;
    if (prev.size === next.size) {
        let same = true;
        for (const [key, value] of next) {
            if (prev.get(key) !== value) { same = false; break; }
        }
        if (same) return prev;
    }
    ref.current = next;
    return next;
}

function useStabilizedMultiMap(next: Map<string, StreamItem[]>, ref: { current: Map<string, StreamItem[]> }): Map<string, StreamItem[]> {
    const prev = ref.current;
    if (prev.size === next.size) {
        let same = true;
        for (const [key, value] of next) {
            const before = prev.get(key);
            if (!before || before.length !== value.length || before.some((item, i) => item !== value[i])) { same = false; break; }
        }
        if (same) return prev;
    }
    ref.current = next;
    return next;
}

/** A row key that is unique across days and stable across prepends: the
 *  first item id in the turn, with a day-scoped index fallback only for a
 *  turn with no items at all. Duplicate keys let React reuse a stale row and
 *  keep withdrawn text on screen. */
function turnKey(turn: Turn, dayScope: string, turnIndex: number): string {
    if (turn.user && turn.user.type === 'single') return turn.user.item.id;
    for (const segment of turn.segments) {
        for (const entry of [...segment.processing, ...segment.finals]) {
            if (entry.type === 'single') return entry.item.id;
            if (entry.type === 'tool-group' && entry.items.length > 0) return entry.items[0].id;
            if (entry.type === 'thinking-group') return entry.thinkingItem.id;
        }
    }
    return `${dayScope}-turn-${turnIndex}`;
}

export function useStreamListModel(
    items: StreamItem[],
    streaming: boolean,
    streamingStartedAt: number,
    view: MainChatStreamView | undefined,
    clipToFirstTurn: boolean,
): StreamListModel {
    const filteredItems = useMemo(() => mainChatStreamRows(items), [items]);

    // tool_result items are filtered out of the rendered stream above; debug
    // mode resurfaces them by pairing each one with its originating tool call
    // via tool_use_id. Built unconditionally so the debug toggle is a pure
    // render-time decision downstream.
    const resultsRef = useRef<ToolResultsMap>(new Map());
    const resultsByToolUseId = useStabilizedMap(useMemo(() => {
        const map = new Map<string, StreamItem>();
        for (const item of items) {
            if (item.kind === 'result' && item.toolUseId) {
                map.set(item.toolUseId, item);
            }
        }
        return map;
    }, [items]), resultsRef);

    // Receipts can finish loading after a newer turn has already appeared.
    // Their originating user id, rather than arrival order, owns the status.
    const { streamItems, contextStatusesByUser } = useMemo(() => {
        const streamItems: StreamItem[] = [];
        const contextStatusesByUser = new Map<string, StreamItem[]>();
        for (const item of filteredItems) {
            const userItemId = isTodayContextItem(item) &&
                item.toolInput && typeof item.toolInput === 'object'
                ? (item.toolInput as { userItemId?: unknown }).userItemId : undefined;
            if (typeof userItemId === 'string' && userItemId) {
                const receipts = contextStatusesByUser.get(userItemId) ?? [];
                receipts.push(item);
                contextStatusesByUser.set(userItemId, receipts);
            } else {
                streamItems.push(item);
            }
        }
        return { streamItems, contextStatusesByUser };
    }, [filteredItems]);
    const statusesRef = useRef<Map<string, StreamItem[]>>(new Map());
    const stableStatuses = useStabilizedMultiMap(contextStatusesByUser, statusesRef);

    const today = view?.today;
    const days = useMemo(
        () => (today ? buildMainChatDays(streamItems, today) : null),
        [today, streamItems],
    );

    return useMemo(() => {
        const rowIndexByItemId = new Map<string, number>();
        const rows: StreamRow[] = [];
        const pushTurns = (dayItems: StreamItem[], dayStreaming: boolean, dayScope: string) => {
            const grouped = groupStreamItems(dayItems);
            const turns = splitIntoTurns(grouped, dayStreaming);
            // Cron's "featured run output" (splitLabels) clips to turn 0: run
            // logs are sliced from the shared session by sort_order range,
            // which can pull in orphaned user items from other runs that share
            // the session_tag — surfacing those as a second turn renders a bare
            // prompt card under the assistant output. Each cron execution
            // persists exactly one user prompt, and "Continue in chat" starts a
            // new session instead of appending to the run's log.
            const renderedTurns = clipToFirstTurn ? turns.slice(0, 1) : turns;
            // Streaming indicators compare against renderedTurns.length - 1,
            // not turns.length - 1: orphaned turns are always dead runs (the
            // current execution's assistant streams into turn 0).
            renderedTurns.forEach((turn, turnIndex) => {
                const key = turnKey(turn, dayScope, turnIndex);
                const row: StreamRow = {
                    type: 'turn',
                    key,
                    turn,
                    sessionStreaming: streaming,
                    streamingTurn: dayStreaming && turnIndex === renderedTurns.length - 1,
                    streamingStartedAt,
                    isFirstTurn: turnIndex === 0,
                    isLastTurn: turnIndex === renderedTurns.length - 1,
                };
                const index = rows.length;
                if (turn.user && turn.user.type === 'single') rowIndexByItemId.set(turn.user.item.id, index);
                for (const segment of turn.segments) {
                    for (const entry of segment.finals) {
                        if (entry.type === 'single') rowIndexByItemId.set(entry.item.id, index);
                    }
                }
                rows.push(row);
            });
        };
        if (!days) {
            pushTurns(streamItems, streaming, 'stream');
        } else {
            let lastDayWithRows = -1;
            days.forEach((day, index) => {
                if (day.items.length > 0) lastDayWithRows = index;
            });
            days.forEach((day, index) => {
                const { divider, fold, removed } = dayRowsVisibility(day, view?.hideRoutine === true);
                if (!divider) return;
                rows.push({ type: 'day-divider', key: `day-${day.day}`, day: day.day, today: today ?? '' });
                if (removed) rows.push({ type: 'day-removed', key: `removed-${day.day}`, day: day.day, count: day.withdrawn });
                if (fold) {
                    rows.push({ type: 'day-fold', key: `fold-${day.day}`, day: day.day, routine: day.routine, routineCount: day.routineCount });
                }
                if (day.items.length > 0) pushTurns(day.items, streaming && index === lastDayWithRows, day.day);
            });
        }
        return { rows, resultsByToolUseId, contextStatusesByUser: stableStatuses, rowIndexByItemId };
    }, [days, today, streamItems, streaming, streamingStartedAt, view?.hideRoutine, clipToFirstTurn, resultsByToolUseId, stableStatuses]);
}

// ── Row views ───────────────────────────────────────────────────────

interface RowViewProps {
    row: StreamRow;
    executorType: ExecutorType;
    compact: boolean;
    splitLabels?: { prompt?: string; output?: string };
    hideUserPrompt: boolean;
    onEditMessage?: (itemId: string, sortOrder: number, newContent: string) => void;
    onOpenDeliverable?: (path: string) => void;
    deliverableActions?: DeliverableActions;
    onOpenReportTarget?: (target: MainChatReportTarget) => void;
    onUseTextedReply?: (text: string) => void;
    onSendOpenAnswer?: (sessionId: string, text: string) => Promise<unknown>;
    resultsByToolUseId: ToolResultsMap;
    contextStatusesByUser: Map<string, StreamItem[]>;
    showChips: boolean;
}

function isBackgroundLineItem(item: StreamItem): boolean {
    const card = getMainChatProactive(item);
    if (card?.kind === 'capture_paused') return true;
    if (card?.kind !== 'meeting_prep' || !card.event?.start) return false;
    const start = new Date(card.event.start).getTime();
    return !Number.isNaN(start) && (item.rowClass === 'routine' || start <= Date.now());
}

function sameItems(a: readonly StreamItem[], b: readonly StreamItem[]): boolean {
    return a.length === b.length && a.every((item, i) => item === b[i]);
}

function sameGroupedItems(a: readonly GroupedStreamItem[], b: readonly GroupedStreamItem[]): boolean {
    return a.length === b.length && a.every((entry, i) => {
        const other = b[i];
        if (entry.type !== other.type) return false;
        if (entry.type === 'single' && other.type === 'single') return entry.item === other.item;
        if (entry.type === 'tool-group' && other.type === 'tool-group') return entry.toolName === other.toolName && sameItems(entry.items, other.items);
        if (entry.type === 'thinking-group' && other.type === 'thinking-group') return entry.thinkingItem === other.thinkingItem && sameItems(entry.tools, other.tools);
        return false;
    });
}

function sameTurn(a: Turn, b: Turn): boolean {
    const userEqual = a.user === b.user ||
        (a.user && b.user && sameGroupedItems([a.user], [b.user]));
    return Boolean(userEqual) &&
        a.segments.length === b.segments.length &&
        a.segments.every((segment, i) =>
            sameGroupedItems(segment.processing, b.segments[i].processing) &&
            sameGroupedItems(segment.finals, b.segments[i].finals));
}

function rowViewPropsEqual(prev: RowViewProps, next: RowViewProps): boolean {
    const a = prev.row;
    const b = next.row;
    if (a.type !== b.type) return false;
    if (a.type === 'day-divider' && b.type === 'day-divider') {
        if (a.day !== b.day || a.today !== b.today) return false;
    } else if (a.type === 'day-removed' && b.type === 'day-removed') {
        if (a.day !== b.day || a.count !== b.count) return false;
    } else if (a.type === 'day-fold' && b.type === 'day-fold') {
        if (a.day !== b.day || a.routineCount !== b.routineCount || !sameItems(a.routine, b.routine)) return false;
    } else if (a.type === 'turn' && b.type === 'turn') {
        if (
            a.key !== b.key ||
            a.sessionStreaming !== b.sessionStreaming ||
            a.streamingTurn !== b.streamingTurn ||
            a.isFirstTurn !== b.isFirstTurn ||
            a.isLastTurn !== b.isLastTurn ||
            (a.streamingTurn && a.streamingStartedAt !== b.streamingStartedAt) ||
            !sameTurn(a.turn, b.turn)
        ) return false;
    }
    return (
        prev.executorType === next.executorType &&
        prev.compact === next.compact &&
        prev.splitLabels === next.splitLabels &&
        prev.hideUserPrompt === next.hideUserPrompt &&
        prev.onEditMessage === next.onEditMessage &&
        prev.onOpenDeliverable === next.onOpenDeliverable &&
        prev.deliverableActions === next.deliverableActions &&
        prev.onOpenReportTarget === next.onOpenReportTarget &&
        prev.onUseTextedReply === next.onUseTextedReply &&
        prev.onSendOpenAnswer === next.onSendOpenAnswer &&
        prev.resultsByToolUseId === next.resultsByToolUseId &&
        prev.contextStatusesByUser === next.contextStatusesByUser &&
        prev.showChips === next.showChips
    );
}

/** One stream row. Memoized on item identity: a streamed token replaces only
 *  its own items, so every other row keeps an equal model and skips render. */
export const StreamRowView = memo(function StreamRowView({
    row,
    executorType,
    compact,
    splitLabels,
    hideUserPrompt,
    onEditMessage,
    onOpenDeliverable,
    deliverableActions,
    onOpenReportTarget,
    onUseTextedReply,
    onSendOpenAnswer,
    resultsByToolUseId,
    contextStatusesByUser,
    showChips,
}: RowViewProps) {
    const surface = useMainChatStreamView()?.surface ?? 'chat';
    if (row.type === 'day-divider') {
        return <MainChatDayDivider day={row.day} today={row.today} />;
    }
    if (row.type === 'day-removed') {
        return <RemovedUpdatesLine day={row.day} count={row.count} />;
    }
    if (row.type === 'day-fold') {
        // Status lines, stop lines and started meetings are background lines;
        // a fold of only those lists them at one level (C3, H3).
        const onlyBackground = row.routine.every((item) => getMainChatStatus(item) || isBackgroundLineItem(item));
        return (
            <MainChatDayFold day={row.day} count={row.routineCount}>
                {row.routine.map((item) =>
                    getMainChatStatus(item) ? (
                        <MainChatStatusRow key={item.id} item={item} onOpenTarget={onOpenReportTarget} compact defaultExpanded flat={onlyBackground} panelSurface={surface} />
                    ) : (
                        <StreamCard key={item.id} item={item} executorType={executorType} compact onOpenReportTarget={onOpenReportTarget} onUseTextedReply={onUseTextedReply} onSendOpenAnswer={onSendOpenAnswer} />
                    ),
                )}
            </MainChatDayFold>
        );
    }
    return (
        <TurnView
            row={row}
            executorType={executorType}
            compact={compact}
            splitLabels={splitLabels}
            hideUserPrompt={hideUserPrompt}
            onEditMessage={onEditMessage}
            onOpenDeliverable={onOpenDeliverable}
            deliverableActions={deliverableActions}
            onOpenReportTarget={onOpenReportTarget}
            onUseTextedReply={onUseTextedReply}
            onSendOpenAnswer={onSendOpenAnswer}
            resultsByToolUseId={resultsByToolUseId}
            contextStatuses={contextStatusesByUser.get(row.key) ?? EMPTY_STATUSES}
            showChips={showChips}
        />
    );
}, rowViewPropsEqual);

const EMPTY_STATUSES: StreamItem[] = [];

function TurnView({
    row,
    executorType,
    compact,
    splitLabels,
    hideUserPrompt,
    onEditMessage,
    onOpenDeliverable,
    deliverableActions,
    onOpenReportTarget,
    onUseTextedReply,
    onSendOpenAnswer,
    resultsByToolUseId,
    contextStatuses,
    showChips,
}: Omit<RowViewProps, 'row' | 'contextStatusesByUser'> & { row: Extract<StreamRow, { type: 'turn' }>; contextStatuses: StreamItem[] }) {
    const { t } = useTranslation('chat');
    const { turn, sessionStreaming, streamingTurn, streamingStartedAt, isFirstTurn, isLastTurn } = row;
    const hasResponse = turnHasFinals(turn);
    const showPromptLabel = !hideUserPrompt && isFirstTurn && splitLabels?.prompt && turn.user;
    const showOutputLabel = isFirstTurn && splitLabels?.output && turnHasContent(turn);
    const userItem = !hideUserPrompt && turn.user && turn.user.type === 'single' ? turn.user.item : null;
    // The App rejects every edit while a reply streams; the lock is
    // session-wide, not just the streaming turn.
    const canEdit = Boolean(userItem && !userItem.sendFailed && onEditMessage && !sessionStreaming && userItem.sortOrder !== undefined);
    const onEdit = useCallback(
        (newContent: string) => {
            if (userItem?.sortOrder !== undefined) return onEditMessage?.(userItem.id, userItem.sortOrder, newContent);
            return undefined;
        },
        [onEditMessage, userItem],
    );
    const onReset = useCallback(() => {
        if (userItem?.sortOrder !== undefined) onEditMessage?.(userItem.id, userItem.sortOrder, userItem.content ?? '');
    }, [onEditMessage, userItem]);

    return (
        <div className="flex flex-col gap-3" data-stream-turn="">
            {showPromptLabel && <SectionLabel>{splitLabels!.prompt}</SectionLabel>}
            {userItem && (
                <div className="contents" data-stream-item-id={userItem.id}>
                    <StreamCard
                        item={userItem}
                        executorType={executorType}
                        compact={compact}
                        onEdit={canEdit ? onEdit : undefined}
                        onReset={canEdit && hasResponse ? onReset : undefined}
                        onOpenDeliverable={onOpenDeliverable}
                        deliverableActions={deliverableActions}
                    />
                </div>
            )}

            {showOutputLabel && <SectionLabel>{splitLabels!.output}</SectionLabel>}

            {turn.segments.map((segment, segIdx) => {
                const isLastSegment = segIdx === turn.segments.length - 1;
                // Context receipts are always visible, even when they
                // arrived before tool work that later collapses.
                const contextStatusesHere = [...segment.processing, ...segment.finals].filter(isTodayContextStatus);
                const processing = segment.processing.filter((entry) => !isTodayContextStatus(entry));
                const finals = segment.finals.filter((entry) => !isTodayContextStatus(entry));
                const segHasFinals = finals.length > 0;
                // The live timer/spinner only belongs on the segment currently
                // being streamed, and only while no assistant tokens have
                // arrived for it yet — after that, the assistant card itself
                // signals progress. Earlier segments are frozen records.
                const segStreaming = streamingTurn && isLastSegment && !segHasFinals;
                // Auto-collapse the thinking/tool block as soon as assistant
                // tokens start arriving — that's the visual cue that the agent
                // has moved from reasoning to answering, so the steps recede.
                const defaultCollapsed = segHasFinals;
                const processingBlock = processing.length > 0 ? (
                    <ProcessingBlock
                        items={processing}
                        defaultCollapsed={defaultCollapsed}
                        answerDetails={showChips && segHasFinals}
                        executorType={executorType}
                        streaming={segStreaming}
                        streamingStartedAt={isLastTurn && isLastSegment ? streamingStartedAt : 0}
                        resultsByToolUseId={resultsByToolUseId}
                        onOpenReportTarget={onOpenReportTarget}
                        onUseTextedReply={onUseTextedReply}
                    />
                ) : null;
                return (
                    <Fragment key={`seg-${segIdx}`}>
                        {!(showChips && segHasFinals) && processingBlock}
                        {finals.map((final) =>
                            final.type === 'single' ? (
                                <Fragment key={final.item.id}>
                                    {/* `contents` keeps the card a direct flex item; the
                                        id lets the chat scroll to a message Search found. */}
                                    <div className="contents" data-stream-item-id={final.item.id}>
                                        <div className={showChips && final.item.kind === 'assistant' ? 'flex flex-col gap-1.5' : 'contents'}>
                                            {showChips && final.item.kind === 'assistant' && <span className="text-[12px] text-accent">{t('mainChat.view.isoform')}</span>}
                                            <StreamCard
                                                item={final.item}
                                                executorType={executorType}
                                                compact={compact}
                                                onOpenDeliverable={onOpenDeliverable}
                                                deliverableActions={deliverableActions}
                                                resultsByToolUseId={resultsByToolUseId}
                                                onOpenReportTarget={onOpenReportTarget}
                                                onUseTextedReply={onUseTextedReply}
                                                onSendOpenAnswer={onSendOpenAnswer}
                                            />
                                        </div>
                                    </div>
                                    {showChips && !segStreaming && final.item.kind === 'assistant' && (
                                        <MainChatAnswerChips itemId={final.item.id} />
                                    )}
                                </Fragment>
                            ) : null,
                        )}
                        {showChips && segHasFinals && processingBlock}
                        {contextStatusesHere.map((entry) => entry.type === 'single' ? (
                            <StreamCard key={entry.item.id} item={entry.item} />
                        ) : null)}
                    </Fragment>
                );
            })}
            {contextStatuses.map((item) => (
                <StreamCard key={item.id} item={item} />
            ))}
        </div>
    );
}

/** The plain (unwindowed) renderer: every row in order. StreamBody switches
 *  to the windowed renderer only for long streams. */
export function StreamRows({ rows, ...props }: Omit<RowViewProps, 'row'> & { rows: StreamRow[] }) {
    return (
        <>
            {rows.map((row) => (
                <StreamRowView key={row.key} row={row} {...props} />
            ))}
        </>
    );
}

export function GroupedStreamList({
    items,
    streaming,
    streamingStartedAt = 0,
    executorType = 'claude',
    onEditMessage,
    compact = false,
    splitLabels,
    onOpenDeliverable,
    deliverableActions,
    hideUserPrompt = false,
    onOpenReportTarget,
    onUseTextedReply,
    onSendOpenAnswer,
}: GroupedStreamListProps) {
    const view = useMainChatStreamView();
    const model = useStreamListModel(items, streaming, streamingStartedAt, view ?? undefined, Boolean(splitLabels));
    return (
        <StreamRows
            rows={model.rows}
            executorType={executorType}
            compact={compact}
            splitLabels={splitLabels}
            hideUserPrompt={hideUserPrompt}
            onEditMessage={onEditMessage}
            onOpenDeliverable={onOpenDeliverable}
            deliverableActions={deliverableActions}
            onOpenReportTarget={onOpenReportTarget}
            onUseTextedReply={onUseTextedReply}
            onSendOpenAnswer={onSendOpenAnswer}
            resultsByToolUseId={model.resultsByToolUseId}
            contextStatusesByUser={model.contextStatusesByUser}
            showChips={Boolean(view)}
        />
    );
}
