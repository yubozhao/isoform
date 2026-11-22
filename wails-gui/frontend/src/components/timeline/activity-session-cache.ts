import { useEffect, useRef, useState } from 'react';
import { EventsOn } from '../../../wailsjs/runtime/runtime';
import { pageQueryCache } from '@/lib/query-cache';
import { onPagePrivacyWithdrawal } from '@/lib/page-privacy-withdrawal';
import { activeInterval, appActive, onAppShown } from '@/lib/app-activity';

// This facade keeps Activity's existing callers and date/remount contract;
// read values share the renderer's 64-entry LRU with Today and main chat.
const names = new Set<string>();
const queries = new Map<string, [string, unknown[]]>();
function pastDay(key: string): boolean {
    const [kind, date] = key.split(':');
    return ['activities', 'threads'].includes(kind) && /^\d{4}-\d{2}-\d{2}$/.test(date ?? '')
        && new Date(date + 'T00:00:00').getTime() < new Date().setHours(0, 0, 0, 0);
}
function invalidateLiveReads() {
    for (const [key, [name, args]] of queries) {
        if (!pastDay(key)) pageQueryCache.invalidate([name], false, args);
    }
}

/** Today's Activity can be this old (snappy-and-first-run-1.0.md, section 1):
 *  capture events land every few minutes while the person works, and are
 *  applied at most this often, only while the window is looked at. Past days
 *  never change and stay cached for good. */
export const ACTIVITY_LIVE_BUDGET_MS = 5 * 60_000;
// The capture events that only say "today moved on". Summaries, insights,
// schedule state and edits (activities-updated: an edit, Forget or protection
// that also drops past days) still apply at once.
const BUDGETED_LIVE_EVENTS = ['activity:session-saved', 'activity:status', 'activity:timeline-groups-updated', 'activity:moments-ready'];
let liveAppliedAt = 0;
let livePending = false;
// When the oldest event not yet applied arrived: a read that completed or
// started after it (the page's own revalidation on show) already has it.
let livePendingSince = 0;
let liveTimer: ReturnType<typeof setTimeout> | undefined;
function invalidateLiveReadsOnBudget() {
    if (!livePending) livePendingSince = Date.now();
    livePending = true;
    scheduleLiveReads();
}
function scheduleLiveReads() {
    if (liveTimer !== undefined) return;
    liveTimer = setTimeout(applyLiveReads, Math.max(0, liveAppliedAt + ACTIVITY_LIVE_BUDGET_MS - Date.now()));
}
function applyLiveReads() {
    liveTimer = undefined;
    // Hidden or locked: the update waits for the window to be shown again.
    if (!livePending || !appActive()) return;
    livePending = false;
    liveAppliedAt = Date.now();
    for (const [key, [name, args]] of queries) {
        // A read that started after the oldest pending event already has it
        // (the page's own revalidation on show); one that started before is
        // superseded, so it can never refill the cache with older rows.
        const entry = pageQueryCache.state(name, args);
        if (pastDay(key) || (entry?.startedAt ?? 0) > livePendingSince) continue;
        pageQueryCache.invalidate([name], false, args);
    }
}
function invalidateDates(dates: string[]) {
    for (const [key, [name, args]] of queries) {
        const [kind, date] = key.split(':');
        if (['activities', 'threads'].includes(kind) && dates.includes(date)) pageQueryCache.invalidate([name], true, args);
    }
}
let today = new Date().toDateString();
let cleanups: Array<() => void> = [];
export let lastActivityDate: Date | undefined;
export function rememberActivityDate(date: Date) { lastActivityDate = date; }

// The page remounts on every open (App's activityNonce), so its UI state —
// timeline filters, scroll offsets — lives here instead and a
// plain revisit restores it. Deep links (a session/date/highlight jump) start
// fresh: the page reads nothing from here when one arrives. Privacy sweeps
// (the clear=true invalidations) drop it with the data.
export interface ActivityTimelineFilters { searchText: string; timePreset: string; customStart: string; customEnd: string; selectedApps: string[] | null }
let rememberedFilters: ActivityTimelineFilters | undefined;
const rememberedScroll = new Map<string, number>();
export function rememberTimelineFilters(filters: ActivityTimelineFilters) { rememberedFilters = filters; }
export function lastTimelineFilters(): ActivityTimelineFilters | undefined { return rememberedFilters; }
export function rememberActivityScroll(key: string, top: number) { rememberedScroll.set(key, top); }
export function lastActivityScroll(key: string): number | undefined { return rememberedScroll.get(key); }
function clearRememberedActivityUi() {
    rememberedFilters = undefined;
    rememberedScroll.clear();
}

function query(key: string): [string, unknown[]] {
    const [kind, date] = key.split(':');
    const start = date ? new Date(date + 'T00:00:00') : null;
    const validDay = start && Number.isFinite(start.getTime());
    const end = validDay ? new Date(start.getFullYear(), start.getMonth(), start.getDate() + 1).getTime() : 0;
    const bindings: Record<string, string> = { summary: 'EnsureDailySummary', activities: 'GetActivitiesInRange', threads: 'GetDayThreads', runs: 'GetCronRunsByDateRange', knowledge: 'GetKnowledgeEntries', memory: 'GetMemoryEntries' };
    const binding = bindings[kind];
    // Live binding-wide revisions must not supersede a pending historical
    // prefetch. Privacy events still clear every registered read family.
    const name = pastDay(key) ? `Activity:past:${binding}:${date}` : binding ?? `Activity:${kind}`;
    names.add(name);
    const result: [string, unknown[]] = ['activities', 'threads', 'runs'].includes(kind) && validDay
        ? [name, [start.getTime(), end]] : [name, date ? [date] : []];
    queries.set(key, result);
    return result;
}

export function invalidateActivitySessionCache(clear = false) {
    if (clear) clearRememberedActivityUi();
    pageQueryCache.invalidate([...names], clear);
}
function checkDay() {
    const next = new Date().toDateString();
    if (next === today) return;
    const previousStart = new Date(today).getTime();
    const nextStart = new Date(next).getTime();
    today = next;
    if (nextStart < previousStart) { invalidateActivitySessionCache(true); return; }
    // Only the just-finished live day needs a fresh, finalized projection.
    // Earlier completed days retain their cache entries across midnight.
    for (const [key, [name, args]] of queries) {
        const date = key.split(':')[1];
        const start = /^\d{4}-\d{2}-\d{2}$/.test(date ?? '') ? new Date(date + 'T00:00:00').getTime() : NaN;
        if (!Number.isFinite(start) || start >= previousStart) pageQueryCache.invalidate([name], true, args);
    }
}
function start() {
    if (cleanups.length) return;
    // Account and privacy withdrawals arrive through page-queries. Activity's
    // additional knowledge withdrawal drops source text the same way; ordinary
    // events keep the remembered UI and last values while they revalidate.
    const clearing = ['knowledge:updated'];
    cleanups = [
        'activity:session-saved', 'activity:status', 'activity:activities-updated', 'activity:threads-updated',
        'activity:timeline-groups-updated', 'activity:summary-ready', 'activity:moments-ready',
        'activity:summary-error', 'memory:updated', 'insight:saved', 'cron:state',
        'closed-eyes:changed', 'knowledge:updated', 'meeting-notes:saved', 'app:ready',
        'background-listening:changed', 'permissions:changed', 'sensitive-apps:updated',
    ].map((name) => EventsOn(name, (data?: { live?: boolean; dates?: string[] }) => {
        if (clearing.includes(name)) {
            invalidateActivitySessionCache(true);
        } else if (Array.isArray(data?.dates)) {
            invalidateDates(data.dates);
        } else if (BUDGETED_LIVE_EVENTS.includes(name)) {
            invalidateLiveReadsOnBudget();
        } else if (data?.live || ['activity:summary-ready', 'activity:summary-error', 'cron:state', 'insight:saved'].includes(name)) {
            invalidateLiveReads();
        } else {
            // Unscoped edits/Forget/protection must discard hidden past days
            // too; the remembered UI (scope, filters, scroll) stays.
            pageQueryCache.invalidate([...names], true);
        }
    }));
    cleanups.push(onPagePrivacyWithdrawal(() => invalidateActivitySessionCache(true)));
    cleanups.push(onAppShown(() => { if (livePending) scheduleLiveReads(); }));
    // The local day check: stopped while the window is hidden or the Mac
    // locked, and run again the moment it is shown.
    cleanups.push(activeInterval(checkDay, 60_000));
}
export function resetActivitySessionCache() {
    cleanups.forEach((off) => off());
    cleanups = [];
    if (liveTimer !== undefined) clearTimeout(liveTimer);
    liveTimer = undefined;
    livePending = false;
    liveAppliedAt = 0;
    today = new Date().toDateString();
    lastActivityDate = undefined;
    invalidateActivitySessionCache(true);
    queries.clear();
}
export function updateActivitySessionData<T>(key: string, update: (data: T | undefined) => T) {
    const [name, args] = query(key);
    pageQueryCache.set(name, args, update(pageQueryCache.peek<T>(name, args)));
}
export async function refreshActivitySessionData<T>(key: string, load: () => Promise<T>) {
    const [name, args] = query(key);
    pageQueryCache.invalidate([name], false, args);
    try { await pageQueryCache.read(name, args, load); } catch { /* Keep the last successful rows. */ }
}
export async function prefetchActivitySessionData<T>(key: string, load: () => Promise<T>) {
    const [name, args] = query(key);
    start();
    const pendingGrouping = key.startsWith('threads:') && pageQueryCache.peek<{ grouped?: boolean }>(name, args)?.grouped === false;
    try { await pageQueryCache.read(name, args, load, pastDay(key) && !pendingGrouping ? Infinity : ACTIVITY_LIVE_BUDGET_MS); } catch { /* Navigation can retry. */ }
}
export function useActivitySessionQueryState<T>(key: string, load: () => Promise<T>, enabled = true, revalidate = true) {
    const [version, redraw] = useState(0);
    const [name, args] = query(key);
    // A first grouping can fail in the background. Keep its normal retry TTL
    // until completion instead of freezing a pending historical projection.
    const pendingGrouping = key.startsWith('threads:') && pageQueryCache.peek<{ grouped?: boolean }>(name, args)?.grouped === false;
    revalidate = revalidate && (!pastDay(key) || pendingGrouping);
    const latest = useRef({ load, enabled, revalidate });
    latest.current = { load, enabled, revalidate };
    useEffect(() => {
        checkDay();
        start();
        const notify = () => redraw((n) => n + 1);
        const off = pageQueryCache.subscribe((changed) => { if (changed === name) notify(); });
        const release = pageQueryCache.retain(name, args);
        const onFocus = () => {
            checkDay();
            const current = latest.current;
            if (current.enabled) void pageQueryCache.read(name, args, current.load, current.revalidate ? ACTIVITY_LIVE_BUDGET_MS : Infinity).catch(notify);
        };
        const onVisible = () => { if (document.visibilityState === 'visible') onFocus(); };
        window.addEventListener('focus', onFocus);
        document.addEventListener('visibilitychange', onVisible);
        notify();
        return () => { off(); release(); window.removeEventListener('focus', onFocus); document.removeEventListener('visibilitychange', onVisible); };
    }, [key, name]);
    useEffect(() => {
        if (!enabled) return;
        let active = true;
        void pageQueryCache.read(name, args, load, revalidate ? ACTIVITY_LIVE_BUDGET_MS : Infinity).catch(() => {
            if (active) redraw((n) => n + 1);
        });
        return () => { active = false; };
        // Success notifications re-render but fresh entries never read again.
        // A failed read retries on a new mount, focus, or invalidation.
        // eslint-disable-next-line react-hooks/exhaustive-deps
    }, [key, load, enabled, revalidate, pageQueryCache.state(name, args)?.generation, version === 0]);
    const entry = today === new Date().toDateString() ? pageQueryCache.state(name, args) : undefined;
    return { data: entry?.ready ? entry.value as T : undefined, loading: enabled && !entry?.ready && !entry?.settled, error: entry?.error };
}
export function useActivitySessionQuery<T>(key: string, load: () => Promise<T>, enabled = true, revalidate = true) {
    return useActivitySessionQueryState(key, load, enabled, revalidate).data;
}
