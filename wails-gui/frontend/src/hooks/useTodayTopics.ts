import { GetTodayTopics, usePageQueryRefresh, PageEventsOn, TODAY_TOPICS_PENDING_RESYNC_MS, TODAY_TOPICS_TTL_MS } from '@/lib/page-queries';
import { pageQueryCache } from '@/lib/query-cache';
import { useBudgetedRefresh } from '@/lib/app-activity';
import { useCallback, useEffect, useRef, useState } from "react";
import { ListSidebarTopics, RefreshTodayTopics } from "@/lib/wails-shim-app";
import { EventsOn } from "../../wailsjs/runtime/runtime";
import { SIDEBAR_TOPICS_CHANGED, isDisplayTopic, type TodayTopic, type TopicMoment } from "@/components/today/topics";

export { SIDEBAR_TOPICS_CHANGED };

export type TodayTopicsStatus = "loading" | "ready" | "failed" | "unavailable";

/** Display publication and auxiliary completion invalidate the read, but a
 *  read never bypasses the backend's background collection interval. */
export const TOPICS_UPDATED_EVENT = "topics:updated";

/** One topic as the registry (RC 1 A1, topics_registry.go TopicView) sends
 *  it, read defensively. */
interface TopicViewWire {
    id?: string;
    kind?: string;
    name?: string;
    status?: string;
    latest?: { text?: string; at?: string } | null;
    latestFind?: { text?: string; at?: string } | null;
    next?: { text?: string; at?: string } | null;
    peopleLine?: { names?: string[]; count?: number } | null;
    pinnedToSidebar?: boolean;
    sourceCounts?: Record<string, number> | null;
    newStories?: number;
    why?: { lastSeenAt?: string; interestWhy?: string } | null;
    activityDays?: string[] | null;
}

const KINDS: Record<string, TodayTopic["kind"]> = { project: "work", work: "work", following: "followed", followed: "followed" };
const STATUSES: Record<string, NonNullable<TodayTopic["status"]>> = {
    waiting_on_you: "waiting", waiting: "waiting", waiting_on_them: "waitingOnThem", today: "today", moving: "moving", new_stories: "news", news: "news",
};

function moment(raw: TopicViewWire["latest"]): TopicMoment | undefined {
    const text = raw?.text?.trim();
    return text ? { text, at: raw?.at || undefined } : undefined;
}

/** Keeps only topics Today can show and open: an id, a name and a kind. */
export function toTodayTopics(raw: unknown): TodayTopic[] {
    const list = Array.isArray(raw) ? raw : Array.isArray((raw as { topics?: unknown })?.topics) ? (raw as { topics: unknown[] }).topics : [];
    const topics: TodayTopic[] = [];
    for (const entry of list as TopicViewWire[]) {
        const kind = KINDS[entry?.kind ?? ""];
        if (!entry?.id?.trim() || !isDisplayTopic(entry) || !kind) continue;
        const names = (entry.peopleLine?.names ?? []).filter((name) => name?.trim()).slice(0, 3);
        const count = Math.max(entry.peopleLine?.count ?? 0, names.length);
        const newCount = entry.newStories;
        const latestFind = moment(entry.latestFind);
        topics.push({
            id: entry.id,
            name: entry.name.trim(),
            kind,
            status: STATUSES[entry.status ?? ""],
            newCount: typeof newCount === "number" && newCount > 0 ? newCount : undefined,
            latest: moment(entry.latest),
            ...(latestFind ? { latestFind } : {}),
            next: moment(entry.next),
            people: names.length > 0 ? { names, count } : undefined,
            lastSeenAt: entry.why?.lastSeenAt || undefined,
            pinned: entry.pinnedToSidebar === true,
            ...(Array.isArray(entry.activityDays) ? { activityDays: entry.activityDays.filter((day) => typeof day === "string") } : {}),
        });
    }
    return topics;
}

/** Keep displayed positions and reuse every unchanged card object. */
export function reconcileTodayTopics(current: TodayTopic[], incoming: TodayTopic[]): TodayTopic[] {
    const byId = new Map(incoming.map((topic) => [topic.id, topic]));
    const next = current.flatMap((topic) => {
        const replacement = byId.get(topic.id);
        if (!replacement) return [];
        byId.delete(topic.id);
        return [JSON.stringify(topic) === JSON.stringify(replacement) ? topic : replacement];
    });
    next.push(...byId.values());
    return next.length === current.length && next.every((topic, index) => topic === current[index]) ? current : next;
}

function unavailable(error: unknown): boolean {
    return String(error).includes("is not available");
}

/**
 * Today's topics from the topic registry. Before the registry exists on this
 * computer the read is "unavailable" and Today shows no topics, never an
 * error; a failed read (topic_registry_refresh_failed among them) keeps the
 * last topics and status on screen. Reads again on focus, after a pin, and
 * when the computer's background retry succeeds (topics:updated).
 */
export function useTodayTopics(enabled: boolean) {
    const [topics, setTopics] = useState<TodayTopic[]>(() => toTodayTopics(pageQueryCache.peek('GetTodayTopics')));
    const [hasTopics, setHasTopics] = useState(() => pageQueryCache.peek<{ hasTopics?: boolean }>('GetTodayTopics')?.hasTopics === true || toTodayTopics(pageQueryCache.peek('GetTodayTopics')).length > 0);
    const [firstRun, setFirstRun] = useState(() => pageQueryCache.peek<{ firstRun?: boolean }>('GetTodayTopics')?.firstRun === true);
    const [refreshing, setRefreshing] = useState(() => pageQueryCache.peek<{ refreshing?: boolean }>('GetTodayTopics')?.refreshing === true);
    const [refreshFailed, setRefreshFailed] = useState(false);
    const refreshFailedRef = useRef(false);
    const setRefreshFailure = (failed: boolean) => {
        if (refreshFailedRef.current === failed) return;
        refreshFailedRef.current = failed;
        setRefreshFailed(failed);
    };
    const lastSuccessfulAt = useRef<number | null>(pageQueryCache.state('GetTodayTopics')?.at || null);
    const [lastUpdatedAt, setLastUpdatedAt] = useState<number | null>(null);
    const [status, setStatus] = useState<TodayTopicsStatus>(() => pageQueryCache.peek('GetTodayTopics') === undefined ? 'loading' : 'ready');
    const [refreshInterval, setRefreshInterval] = useState<number | null>(() => pageQueryCache.peek<{ refreshIntervalMs?: number }>('GetTodayTopics')?.refreshIntervalMs ?? null);
    // Avoid dispatching even equal state after an unrelated parent/draft edit:
    // React may otherwise commit one bailout render before it can skip again.
    const displayed = useRef({ topics, hasTopics, firstRun, refreshing, status, refreshInterval });
    // The latest read wins when two overlap (focus and a retry's event).
    const request = useRef(0);

    const refresh = useCallback(async () => {
        if (!enabled) return;
        const id = ++request.current;
        try {
            // The registry keeps the why rule (item 61): followed topics come
            // only with a reason, which Closed Eyes withholds, so Today shows
            // what it sends.
            const response = await GetTodayTopics();
            const next = toTodayTopics(response);
            const nextFirstRun = !Array.isArray(response) && response?.firstRun === true;
            const pending = !Array.isArray(response) && response?.refreshing === true;
            const clear = !Array.isArray(response) && response?.clear === true;
            const known = next.length > 0 || (!Array.isArray(response) && response?.hasTopics === true);
            if (id !== request.current) return;
            setRefreshFailure(false);
            lastSuccessfulAt.current = pageQueryCache.state('GetTodayTopics')?.at || Date.now();
            const current = displayed.current;
            const nextTopics = pending && next.length === 0 && !clear ? current.topics : reconcileTodayTopics(current.topics, next);
            const nextHasTopics = pending && !clear ? current.hasTopics || known : known;
            const nextInterval = !Array.isArray(response) && response?.refreshIntervalMs > 0 ? response.refreshIntervalMs : current.refreshInterval;
            // A second read can complete before React renders the first one.
            // Compare against the latest queued display as well as committed state.
            displayed.current = { topics: nextTopics, hasTopics: nextHasTopics, firstRun: nextFirstRun, refreshing: pending, status: "ready", refreshInterval: nextInterval };
            if (nextTopics !== current.topics) setTopics(nextTopics);
            if (nextHasTopics !== current.hasTopics) setHasTopics(nextHasTopics);
            if (nextFirstRun !== current.firstRun) setFirstRun(nextFirstRun);
            if (pending !== current.refreshing) setRefreshing(pending);
            if (nextInterval !== current.refreshInterval) setRefreshInterval(nextInterval);
            if (current.status !== "ready") setStatus("ready");
        } catch (error) {
            if (id !== request.current) return;
            setRefreshFailure(!unavailable(error));
            setLastUpdatedAt(lastSuccessfulAt.current);
            const current = displayed.current.status;
            // "Unavailable" means the registry does not exist yet: it never
            // hides a grid that has shown topics (a read that fails with that
            // text during launch kept Today empty).
            const next = current === "ready" ? current : unavailable(error) ? "unavailable" : "failed";
            displayed.current = { ...displayed.current, status: next };
            if (next !== current) setStatus(next);
        }
    }, [enabled]);

    usePageQueryRefresh(['GetTodayTopics'], refresh, enabled);

    // "topics:updated" drives the grid; these are only safety resyncs, never
    // while the window is hidden or the Mac locked. Settled topics follow the
    // backend's collection interval (2 hours of work), never under 10 minutes.
    useBudgetedRefresh(() => pageQueryCache.invalidate(['GetTodayTopics'], false),
        Math.max(refreshInterval ?? TODAY_TOPICS_TTL_MS, TODAY_TOPICS_PENDING_RESYNC_MS), enabled);
    // A pending rebuild keeps the cards and the spinning icon; its publication
    // replaces them in place. The resync covers a publication that preceded
    // the listener or a bridge that reconnected.
    useBudgetedRefresh(() => void refresh(), TODAY_TOPICS_PENDING_RESYNC_MS, enabled && refreshing);

    useEffect(() => {
        // Withdraw local copies as well as the cache before a gated read
        // completes. Hidden pages must also forget pre-transition topics.
        const clear = () => {
            ++request.current;
            setRefreshFailure(false);
            lastSuccessfulAt.current = null;
            setLastUpdatedAt(null);
            displayed.current = { ...displayed.current, topics: [], hasTopics: false, refreshing: true };
            setTopics([]);
            setHasTopics(false);
            setRefreshing(true);
            void refresh();
        };
        const off = ['topics:withdrawn', 'memory:updated', 'closed-eyes:changed', 'background-listening:changed', 'permissions:changed', 'sensitive-apps:updated']
            .map((event) => PageEventsOn(event, clear));
        return () => off.forEach((stop) => stop());
    }, [refresh]);

    useEffect(() => {
        // Focus paints from the cache; the resyncs above catch up only what is
        // past its budget.
        void refresh();
        window.addEventListener(SIDEBAR_TOPICS_CHANGED, refresh);
        const offUpdated = PageEventsOn(TOPICS_UPDATED_EVENT, refresh);
        return () => {
            window.removeEventListener(SIDEBAR_TOPICS_CHANGED, refresh);
            offUpdated();
        };
    }, [refresh]);

    const refreshExplicitly = useCallback(async () => {
        if (!enabled) return;
        try {
            await RefreshTodayTopics();
            pageQueryCache.invalidate(['GetTodayTopics'], false);
            await refresh();
        } catch (error) {
            setRefreshFailure(!unavailable(error));
            setLastUpdatedAt(lastSuccessfulAt.current);
            const current = displayed.current.status;
            // "Unavailable" means the registry does not exist yet: it never
            // hides a grid that has shown topics (a read that fails with that
            // text during launch kept Today empty).
            const next = current === "ready" ? current : unavailable(error) ? "unavailable" : "failed";
            displayed.current = { ...displayed.current, status: next };
            if (next !== current) setStatus(next);
        }
    }, [enabled, refresh]);
    return { topics, firstRun, hasTopics, status, refreshing, refreshFailed, lastUpdatedAt, refresh: refreshExplicitly };
}

/** The topics pinned to the sidebar (A1 `ListSidebarTopics`); none before
 *  the registry exists, and a failed read keeps what the sidebar shows. */
export function useSidebarTopics() {
    const [topics, setTopics] = useState<TodayTopic[]>([]);
    const displayed = useRef(topics);
    const request = useRef(0);
    const refresh = useCallback(async () => {
        const id = ++request.current;
        try {
            const next = toTodayTopics(await ListSidebarTopics());
            if (id !== request.current || JSON.stringify(displayed.current) === JSON.stringify(next)) return;
            displayed.current = next;
            setTopics(next);
        } catch {
            // The sidebar keeps what it shows; before A1 it shows none.
        }
    }, []);
    useEffect(() => {
        void refresh();
        const onChange = () => void refresh();
        window.addEventListener("focus", onChange);
        window.addEventListener(SIDEBAR_TOPICS_CHANGED, onChange);
        const offUpdated = EventsOn(TOPICS_UPDATED_EVENT, onChange);
        const offWithdrawn = EventsOn('topics:withdrawn', () => { displayed.current = []; setTopics(displayed.current); void refresh(); });
        return () => {
            window.removeEventListener("focus", onChange);
            window.removeEventListener(SIDEBAR_TOPICS_CHANGED, onChange);
            offUpdated();
            offWithdrawn();
        };
    }, [refresh]);
    return { topics, refresh };
}
