import { GetMainChatOverview, ListMainChatAnswerChips, PageEventsOn as EventsOn, usePageQueryRefresh } from '@/lib/page-queries';
import { quietRetry } from '@/lib/chat-failure';
import { pageQueryCache } from '@/lib/query-cache';
import { activeInterval } from '@/lib/app-activity';
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
    answerChipIds,
    localDayKey,
    toMainChatOverview,
    type MainChatOverview,
} from '@/lib/main-chat-view';
import { useTodayTopics } from '@/hooks/useTodayTopics';
import { clockContextKey } from '@/lib/brief-time';
import type { StreamItem } from '@/types';
import { useMainChatWaiting as useMainChatWaitingState } from '@/lib/main-chat-waiting-store';

export type MainChatReadStatus = 'loading' | 'ready' | 'failed';

/** Prepared chips are read for at most this many answers at once. */
const CHIP_READ_LIMIT = 100;
const CHIPS_EVENT = 'main-chat:chips';
const CLOSED_EYES_CHANGED_EVENT = 'closed-eyes:changed';

/** Today's numbers for the main chat and the "Hide folded updates" switch.
 *  A failed read keeps what is on screen; the first failure says so. */
function useMainChatOverview(enabled: boolean, today: string) {
    const [overview, commitOverview] = useState<MainChatOverview | null>(() => { const value = pageQueryCache.peek('GetMainChatOverview'); return value === undefined ? null : toMainChatOverview(value); });
    const [status, setStatus] = useState<MainChatReadStatus>(() => pageQueryCache.peek('GetMainChatOverview') === undefined ? 'loading' : 'ready');
    const displayed = useRef({ overview, status });
    // The switch also writes the overview locally; keep those queued values in
    // the same comparison as background reads, before React renders either one.
    const setOverview = useCallback<typeof commitOverview>((update) => {
        const current = displayed.current.overview;
        const next = typeof update === 'function' ? update(current) : update;
        if (JSON.stringify(current) === JSON.stringify(next)) return;
        displayed.current = { ...displayed.current, overview: next };
        commitOverview(next);
    }, []);
    const request = useRef(0);

    const load = useCallback(async () => {
        if (!enabled) return;
        const id = ++request.current;
        try {
            const raw = await quietRetry(() => GetMainChatOverview());
            const next = toMainChatOverview(raw);
            if (id !== request.current) return;
            setOverview(next);
            if (displayed.current.status !== 'ready') {
                displayed.current = { ...displayed.current, status: 'ready' };
                setStatus('ready');
            }
        } catch {
            if (id !== request.current) return;
            const current = displayed.current.status;
            if (current !== 'ready' && current !== 'failed') {
                displayed.current = { ...displayed.current, status: 'failed' };
                setStatus('failed');
            }
        }
    }, [enabled, setOverview]);

    useEffect(() => {
        if (!enabled) return;
        void load();
    }, [enabled, load, today]);

    const reload = useCallback(() => {
        pageQueryCache.invalidate(['GetMainChatOverview']);
        return load();
    }, [load]);

    return { overview, status, setOverview, reload };
}

/** Prepared requests under the main chat's latest answers
 *  (ListMainChatAnswerChips), read when an answer appears and again when the
 *  computer says it kept chips for one (main-chat:chips). None while
 *  observation is paused: the computer returns none then. */
export function useMainChatAnswerChips(enabled: boolean, answerIds: readonly string[]) {
    const [chips, setChips] = useState<Record<string, string[]>>({});
    const displayed = useRef(chips);
    const asked = useRef(new Set<string>());
    // The latest read per answer wins, whatever order the replies come in.
    const latestRead = useRef(new Map<string, number>());
    const reads = useRef(0);
    const paused = useRef(false);
    const answerKey = answerIds.join('|');

    const read = useCallback(async (ids: string[]) => {
        if (ids.length === 0 || paused.current) return;
        const readId = ++reads.current;
        for (const id of ids) latestRead.current.set(id, readId);
        try {
            const batches = await Promise.all(Array.from({ length: Math.ceil(ids.length / CHIP_READ_LIMIT) }, (_, index) =>
                ListMainChatAnswerChips(ids.slice(index * CHIP_READ_LIMIT, (index + 1) * CHIP_READ_LIMIT)),
            ));
            const found = Object.assign({}, ...batches) as Record<string, unknown>;
            const current = displayed.current;
            const next = { ...current };
            for (const id of ids) {
                if (latestRead.current.get(id) !== readId) continue;
                const list = Array.isArray(found[id])
                    ? (found[id] as unknown[]).filter((chip): chip is string => typeof chip === 'string' && chip.trim() !== '').slice(0, 2)
                    : [];
                if (list.length > 0) next[id] = list;
                else delete next[id];
            }
            if (JSON.stringify(current) !== JSON.stringify(next)) {
                displayed.current = next;
                setChips(next);
            }
        } catch {
            // Read again the next time the answer is looked for.
            for (const id of ids) asked.current.delete(id);
        }
    }, []);

    usePageQueryRefresh(['ListMainChatAnswerChips'], () => read([...answerIds]), enabled);

    useEffect(() => {
        if (!enabled) {
            paused.current = false;
            asked.current.clear();
            return;
        }
        const fresh = answerIds.filter((id) => !asked.current.has(id));
        for (const id of fresh) asked.current.add(id);
        void read(fresh);
        // answerKey stands for answerIds.
        // eslint-disable-next-line react-hooks/exhaustive-deps
    }, [enabled, answerKey, read]);

    useEffect(() => {
        if (!enabled) return;
        const offChips = EventsOn(CHIPS_EVENT, (data: { itemId?: unknown } | null) => {
            const itemId = typeof data?.itemId === 'string' ? data.itemId : '';
            if (!itemId) return;
            asked.current.add(itemId);
            void read([itemId]);
        });
        const offPause = EventsOn(CLOSED_EYES_CHANGED_EVENT, (isPaused: unknown) => {
            paused.current = Boolean(isPaused);
            asked.current.clear();
            if (isPaused) {
                latestRead.current.clear();
                displayed.current = {};
                setChips(displayed.current);
            } else void read([...answerIds]);
        });
        return () => {
            offChips();
            offPause();
        };
        // answerKey stands for answerIds.
        // eslint-disable-next-line react-hooks/exhaustive-deps
    }, [enabled, answerKey, read]);

    return chips;
}

/** Today's local day, kept current across midnight and when the window
 *  comes back. */
function useToday(): string {
    const [today, setToday] = useState(() => ({ day: localDayKey(new Date()), context: clockContextKey() }));
    const clock = useRef(today.context);
    useEffect(() => {
        const check = () => {
            const now = new Date();
            const context = clockContextKey(now);
            if (context === clock.current) return;
            clock.current = context;
            setToday({ day: localDayKey(now), context });
        };
        // A local clock with no read: stopped while the window is hidden or
        // the Mac locked, and checked again the moment it is shown.
        return activeInterval(check, 60_000);
    }, []);
    return today.day;
}

/**
 * The main chat's view: today's numbers, prepared chips under the latest
 * answer, and the topic names its rows are tagged with. Chat is one thread
 * (computer-chat v1.0.0 rule 2): there is no filter or topic view. Nothing
 * here sends, creates or changes a chat row.
 */
export function useMainChatView({ enabled, items }: { enabled: boolean; items: readonly StreamItem[] | null }) {
    const today = useToday();
    // Today's numbers read again after midnight too.
    const overview = useMainChatOverview(enabled, today);
    const waitingState = useMainChatWaitingState();
    const summaryOverview = useMemo(() => {
        const current = overview.overview;
        if (!current || !waitingState.ready || !waitingState.day || waitingState.day !== current.day) return current;
        if (waitingState.sessionId && current.sessionId && waitingState.sessionId !== current.sessionId) return current;
        return {
            ...current,
            conversations: waitingState.conversations,
            finished: waitingState.finished,
            routine: waitingState.routine,
        };
    }, [overview.overview, waitingState]);
    const answerIds = useMemo(() => answerChipIds(items ?? []), [items]);
    const allChips = useMainChatAnswerChips(enabled, answerIds);
    // Keep older questions visible alongside the latest turn's suggestions.
    const answerKey = answerIds.join('|');
    const chips = useMemo(() => {
        const shown: Record<string, string[]> = {};
        for (const id of answerKey.split('|')) if (id && allChips[id]) shown[id] = allChips[id];
        return shown;
    }, [allChips, answerKey]);

    const { topics: allTopics } = useTodayTopics(enabled);
    const topicNames = useMemo(() => new Map(allTopics.map((topic) => [topic.id, topic.name])), [allTopics]);

    return {
        today,
        topicNames,
        overview: summaryOverview,
        overviewStatus: overview.status,
        reloadOverview: overview.reload,
        hideRoutine: overview.overview?.hideRoutine ?? false,
        chips,
    };
}

export type MainChatView = ReturnType<typeof useMainChatView>;
