import { memo, useCallback, useEffect, useId, useLayoutEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';
import { useFirstDay } from '@/hooks/useFirstDay';
import { useDoingNow } from '@/hooks/useDoingNow';
import { ChevronDown, Film, Trash2, Zap } from 'lucide-react';
import {
    DeleteActivity,
    GetActivitiesInRange,
    GetActivityFeed,
    GetDayThreads,
    GetRecentMeetingNotes,
    type ActivityFeedRow,
    type ActivityOutcome,
    type ActivityRunFrame,
    type ActivityThreadView,
    type ActivityView,
    type MeetingNotesView,
} from '@/lib/wails-shim-app';
import { invalidateActivitySessionCache, lastActivityScroll, prefetchActivitySessionData, refreshActivitySessionData, rememberActivityScroll, updateActivitySessionData, useActivitySessionQuery, useActivitySessionQueryState } from './timeline/activity-session-cache';
import { activeInterval } from '@/lib/app-activity';
import { ThreadMoveControl, ThreadWorkRow } from './activity-thread-row';
import {
    ActivityStitchedView,
    clipDayActivity,
    StitchedFailure,
    isPickUpLater,
    pickUpNotice,
    setPickUpLater,
    stitchThreads,
    stitchedHeadline,
    stitchedRows,
    stitchedSubline,
    type PickUpNotice,
    type WorkedOnView,
} from './activity-stitched';
import type { main } from '../../wailsjs/go/models';
import { AppGlyph, FrameLightbox, RunFrameThumb, useAppIcons } from './activities-view';
import { ActivityRowActions } from './activity-row-actions';
import type { TodayChatContextRef } from '@/lib/today-chat-navigation';
import { warmTodayChatDrafts } from '@/lib/today-chat-prepared';
import { MeetingNotesCard } from './activity-meeting-notes';
import { ActivityCaptureLine } from './activity-capture-line';
import { ActivityFeed, ActivityFeedRowItem, useActivityForget } from './activity-feed';
import type { TodayTopic } from '@/components/today/topics';
import { cn } from '../lib/utils';
import { ACTION_LINK, ActivityInlineNotice } from './activity-inline-notice';
import {
    dayEndMs,
    dayStartMs,
    formatDateStr,
    formatDurationMs,
    formatTimeFromMs,
    formatTimeRangeFromMs,
    type CronRunWithJobName,
} from './timeline/useCalendarData';

// The Activity page's Day view: the day's feed ("Your day", the record only)
// and one merged list of what you worked on (activities, meetings, Isoform's
// scheduled runs). Everything technical (run counts, keyword
// chips, raw run output) stays out of this view. What you worked on shows the
// day "Stitched by work" (activity-stitched.tsx, the RC 1 timeline) or "As
// recorded", the merged list itself.

// Shared empty fallbacks: a fresh `?? []` per render would change identity
// and defeat the memoized selectors and rows below.
const NO_ACTIVITIES: ActivityView[] = [];
const NO_THREADS: ActivityThreadView[] = [];
const NO_MEETINGS: MeetingNotesView[] = [];

/** The pick-up notice is rebuilt on every stitched/clock change but its
 *  fields are all scalars; reuse the previous object while nothing changed
 *  so downstream memo boundaries (the stitched view, its card) hold. */
function useStablePickUp(next: PickUpNotice | null): PickUpNotice | null {
    const ref = useRef<PickUpNotice | null>(null);
    const prev = ref.current;
    if (prev && next &&
        prev.key === next.key && prev.threadId === next.threadId && prev.title === next.title &&
        prev.steppedAwayMs === next.steppedAwayMs && prev.label === next.label &&
        prev.momentId === next.momentId && prev.momentTitle === next.momentTitle &&
        prev.momentStartMs === next.momentStartMs && prev.activeMs === next.activeMs) {
        return prev;
    }
    ref.current = next;
    return next;
}

// ── Helpers ──

/** The one-line plain-text result shown for a scheduled run, or null when the
 *  output is markup/JSON or spans several lines — those runs show only their
 *  title plus an "Open result" link to the automation detail dialog, so raw
 *  tags never land on the day page. */

/** A read still running after this says so in place, with Try again
 *  (activity review v1.0.5 V1); placeholders show only before it. */
export const ACTIVITY_LOAD_DEADLINE_MS = 15_000;

export function useLoadDeadline(loading: boolean, attempt: number): boolean {
    const [late, setLate] = useState(false);
    useEffect(() => {
        setLate(false);
        if (!loading) return;
        const timer = setTimeout(() => setLate(true), ACTIVITY_LOAD_DEADLINE_MS);
        return () => clearTimeout(timer);
    }, [loading, attempt]);
    return late && loading;
}

export function displayableRunResult(summary: string | undefined): string | null {
    const text = (summary ?? '').trim();
    if (!text) return null;
    if (text.includes('\n')) return null;
    if (text.startsWith('<') || text.startsWith('{') || text.startsWith('[')) return null;
    if (text.includes('<news_json>')) return null;
    return text;
}

const sectionLabelClass = 'text-[11px] uppercase tracking-[0.08em] font-semibold text-muted/80 mb-2 px-1';

const WORKED_ON_VIEWS: WorkedOnView[] = ['stitched', 'recorded'];

// ── Day view ──

/** The Day view's two sections: Your day (the day's feed) and the Screen
 *  tab's What you worked on. The Activity page's tabs show one at a time; an
 *  undefined section renders both stacked. */
export type ActivityDaySection = 'yourDay' | 'workedOn';

export interface ActivityDayViewProps {
    date: Date;
    /** A thread to open in "What you worked on" once its rows are loaded. */
    initialThreadId?: string | null;
    onInitialThreadConsumed?: (threadId: string) => void;
    searchQuery: string;
    bridgePort: number;
    /** Day data from the page-level useCalendarData. */
    dailyData: main.DailySummaryData | null;
    automationRuns: CronRunWithJobName[];
    /** "Chat about this" on a moment: the prepared words go in the main chat
     *  box, unsent, with the moment as context (Today's route). */
    onStartChat?: (prompt: string, context: TodayChatContextRef) => void;
    /** Opens a scheduled run's detail dialog (the automation that ran). */
    onOpenRunDetail: (jobId: string) => void;
    /** When set, only this section renders (the Activity page's tabs show one
     *  section at a time). Undefined renders both stacked. */
    section?: ActivityDaySection;
    /** A suggestion's source link reveals its moment in What you worked on. */
    onSectionChange?: (section: ActivityDaySection) => void;
    /** Reports the day's row counts (unfiltered) so the section panel can
     *  label "What you worked on" with them. */
    onCountsChange?: (counts: { workedOn: number }) => void;
    /** What you worked on: "Stitched by work" or "As recorded". The Activity
     *  page keeps the pick; without it the view keeps its own, Stitched first. */
    workedOnView?: WorkedOnView;
    onWorkedOnViewChange?: (view: WorkedOnView) => void;
    /** The Screen tab's other view: scrub the day in the Screen timeline. */
    onOpenTimeline?: () => void;
    /** Your day's rows: a watch conversation opens on the Watch tab, on its day. */
    onOpenConversation?: (conversationId: string, startMs: number) => void;
    /** A row's topic opens beside Today. */
    onOpenTopic?: (topic: TodayTopic) => void;
    /** A call's recording opens in the Screen timeline. */
    onOpenRecording?: (sessionId: string) => void;
}

export function ActivityDayView({
    date,
    initialThreadId,
    onInitialThreadConsumed,
    searchQuery,
    bridgePort,
    dailyData,
    automationRuns,
    onStartChat: startChat,
    onOpenRunDetail,
    section,
    onSectionChange,
    onCountsChange,
    workedOnView,
    onWorkedOnViewChange,
    onOpenTimeline,
    onOpenConversation,
    onOpenTopic,
    onOpenRecording,
}: ActivityDayViewProps) {
    const { t, i18n } = useTranslation('activity');
    const dateStr = formatDateStr(date);
    const dayStart = dayStartMs(date);
    const dayEnd = dayEndMs(date);
    const isToday = dateStr === formatDateStr(new Date());
    const firstDay = useFirstDay();
    const onStartChat = useMemo(() => startChat ? (prompt: string, ref: TodayChatContextRef) =>
        startChat(prompt, ref.sourceKind === 'activity' ? { ...ref, dayStartMs: dayStart } : ref) : undefined, [startChat, dayStart]);

    // The page remounts on every open; restore the day's scroll offset (the
    // warm cache renders the content at full height on the first commit).
    const scrollRef = useRef<HTMLDivElement>(null);
    useLayoutEffect(() => {
        const saved = lastActivityScroll(`day:${dateStr}`);
        if (saved && scrollRef.current) scrollRef.current.scrollTop = saved;
    }, [dateStr]);

    // ── The day's composed activities (AI-titled sessions) ──
    const [loadAttempt, setLoadAttempt] = useState(0);
    const loadActivities = useCallback(() => GetActivitiesInRange(dayStart, dayEnd), [dayStart, dayEnd]);
    const activityState = useActivitySessionQueryState(`activities:${dateStr}`, loadActivities);
    const rawActivities = activityState.data ?? NO_ACTIVITIES;
    const activities = useMemo(() => rawActivities.filter((activity) => activity.endMs > dayStart && activity.startMs < dayEnd)
        .map((activity) => clipDayActivity(activity, dayStart, dayEnd, isToday)), [rawActivities, dayStart, dayEnd, isToday]);
    // A failed read says so; it never reads as an empty day.
    const activitiesLate = useLoadDeadline(activityState.loading, loadAttempt);
    const activitiesFailed = (activityState.error !== undefined && activityState.data === undefined) || activitiesLate;
    // A live session's duration counts up on the local minute clock below;
    // its rows follow capture events, applied at most every 5 minutes
    // (activity-session-cache), never by a refetch timer.
    const hasLiveActivity = activities.some((activity) => activity.inProgress);
    const loadThreads = useCallback(() => GetDayThreads(dayStart, dayEnd), [dayStart, dayEnd]);
    const threadState = useActivitySessionQueryState(`threads:${dateStr}`, loadThreads);
    const rawThreads = threadState.data?.threads ?? NO_THREADS;
    const threads = useMemo(() => rawThreads.filter((thread) => thread.endMs > dayStart && thread.startMs < dayEnd)
        .map((thread) => ({ ...thread, startMs: Math.max(dayStart, thread.startMs), endMs: Math.min(dayEnd, thread.endMs) })),
        [rawThreads, dayStart, dayEnd]);
    const threadsLoaded = !threadState.loading;
    const threadsLate = useLoadDeadline(threadState.loading, loadAttempt);
    const threadsFailed = (threadState.error !== undefined && threadState.data === undefined) || threadsLate;
    const handleActivityDeleted = useCallback((id: string) => {
        updateActivitySessionData<ActivityView[]>(`activities:${dateStr}`, (rows) => (rows ?? []).filter((row) => row.id !== id));
    }, [dateStr]);

    // "Chat about this" is prepared for every moment while the day loads, in
    // one read, so a click fills the chat box at once (the Today route).
    const chatWarmKey = useMemo(
        () =>
            onStartChat
                ? JSON.stringify(
                      activities
                          .filter((a) => !a.actionsWithheld)
                          .map((a) => ({
                              ref: { sourceKind: 'activity', sourceId: a.id, dayStartMs: dayStart },
                              signature: `${a.title}|${a.description}|${a.endMs}|${a.activeMs}|${dayStart}`,
                          })),
                  )
                : '',
        [activities, onStartChat, dayStart],
    );
    useEffect(() => {
        if (chatWarmKey) warmTodayChatDrafts('activity-day', JSON.parse(chatWarmKey));
    }, [chatWarmKey]);

    // A row action changed what Go prepares for a row (a follow-up, a
    // reminder, a correction): read the day again.
    const reloadActivities = useCallback(() => {
        void refreshActivitySessionData(`activities:${dateStr}`, loadActivities);
    }, [dateStr, loadActivities]);

    // Meetings retain the same lookback and local-day filtering.
    const loadMeetings = useCallback(async () => {
        const todayStart = dayStartMs(new Date());
        if (dayStart > todayStart) return [];
        const days = Math.round((todayStart - dayStart) / 86400000) + 1;
        const rows = await GetRecentMeetingNotes(days);
        return (rows ?? []).filter((meeting) => meeting.startTime >= dayStart && meeting.startTime < dayEnd);
    }, [dayStart, dayEnd]);
    const meetings = useActivitySessionQuery(`meetings:${dateStr}`, loadMeetings) ?? NO_MEETINGS;

    // ── The merged "What you worked on" list, newest first ──
    const workItems = useMemo<WorkItem[]>(() => {
        const out: WorkItem[] = [];
        // A thread with two or more moments on screen is one row; a one-moment
        // thread stays the plain moment row it was before threads.
        const byId = new Map(activities.map((a) => [a.id, a] as const));
        const grouped = new Set<string>();
        for (const thread of threads) {
            const moments = thread.activityIds
                .map((id) => byId.get(id))
                .filter((a): a is ActivityView => a !== undefined);
            if (moments.length < 2) continue;
            for (const moment of moments) grouped.add(moment.id);
            // Newest first by start, the time the row shows (activity review v1.0.6 AR40).
            const start = Math.min(...moments.map((m) => m.startMs));
            out.push({ kind: 'thread', id: `thread-${thread.id}`, startMs: start, thread, moments });
        }
        for (const activity of activities) {
            if (grouped.has(activity.id)) continue;
            out.push({ kind: 'activity', id: `activity-${activity.id}`, startMs: activity.startMs, activity });
        }
        for (const meeting of meetings) {
            out.push({ kind: 'meeting', id: `meeting-${meeting.meetingKey}`, startMs: meeting.startTime, meeting });
        }
        for (const run of automationRuns) {
            out.push({ kind: 'run', id: `run-${run.id}`, startMs: run.startedAt, run });
        }
        out.sort((a, b) => b.startMs - a.startMs);
        return out;
    }, [activities, threads, meetings, automationRuns]);

    const query = searchQuery.trim().toLowerCase();

    // ── Stitched by work ──
    const [ownView, setOwnView] = useState<WorkedOnView>('stitched');
    const view = workedOnView ?? ownView;
    const setView = useCallback(
        (next: WorkedOnView) => {
            setOwnView(next);
            onWorkedOnViewChange?.(next);
        },
        [onWorkedOnViewChange],
    );
    useEffect(() => {
        if (!isToday || activityState.loading || threadState.loading) return;
        const previous = new Date(date);
        previous.setDate(previous.getDate() - 1);
        const key = formatDateStr(previous);
        const start = dayStartMs(previous);
        const end = dayEndMs(previous);
        void prefetchActivitySessionData(`activities:${key}`, () => GetActivitiesInRange(start, end));
        void prefetchActivitySessionData(`threads:${key}`, () => GetDayThreads(start, end));
    }, [isToday, dateStr, activityState.loading, threadState.loading]);
    const [now, setNow] = useState(() => Date.now());
    useEffect(() => {
        // Recent work can still qualify just after midnight on the previous day.
        setNow(Date.now());
    }, [dateStr]);
    const stitched = useMemo(
        () => stitchThreads(rawThreads, rawActivities, now, isToday, dayStart, dayEnd),
        [rawThreads, rawActivities, now, isToday, dayStart, dayEnd],
    );
    const visibleStitched = useMemo(
        () =>
            query
                ? stitched.filter(
                      (item) =>
                          item.thread.title.toLowerCase().includes(query) ||
                          item.moments.some((moment) => activityMatchesQuery(moment, query)),
                  )
                : stitched,
        [stitched, query],
    );
    const stitchedStatus =
        threadsFailed || activitiesFailed
            ? 'failed'
            : threadState.loading || activityState.loading
              ? 'loading'
              : threadState.data?.grouped !== true && activities.length > 0
                ? 'linking'
                : 'ready';
    const [selectedThreadId, setSelectedThreadId] = useState<string | null>(null);
    const [scrollThreadId, setScrollThreadId] = useState<string | null>(null);
    const clearScrollThread = useCallback(() => setScrollThreadId(null), []);
    const [, setLaterRevision] = useState(0);
    const candidatePickUp = useMemo(() => pickUpNotice(stitched, dateStr, now), [stitched, dateStr, now]);
    const pickUp = useStablePickUp(candidatePickUp && !isPickUpLater(candidatePickUp.key) ? candidatePickUp : null);
    useEffect(() => {
        // The minute tick extends live stretches and expires the pick-up
        // notice. With neither live work nor a pick-up on screen, `now`
        // stays frozen and the stitched model keeps its cache.
        // It stops while the window is hidden and catches up when shown.
        if (!(isToday && hasLiveActivity) && !candidatePickUp) return;
        return activeInterval(() => setNow(Date.now()), 60_000);
    }, [isToday, hasLiveActivity, candidatePickUp]);
    const showPickUp = useCallback(
        (notice: PickUpNotice) => {
            setView('stitched');
            onSectionChange?.('workedOn');
            setSelectedThreadId(notice.threadId);
            setScrollThreadId(notice.threadId);
        },
        [setView, onSectionChange],
    );
    const pickUpLater = useCallback((notice: PickUpNotice) => {
        setPickUpLater(notice.key);
        setLaterRevision((n) => n + 1);
    }, []);
    // A card's "Open thread" lands here. Wait for the thread rows, then select
    // and scroll to the exact thread once, so a still-loading day never drops
    // the scroll request.
    useEffect(() => {
        if (!initialThreadId || !threadsLoaded) return;
        const thread = workItems.find((item) => item.kind === 'thread' && item.thread.id === initialThreadId);
        if (!thread) {
            onInitialThreadConsumed?.(initialThreadId);
            return;
        }
        setView('stitched');
        setSelectedThreadId(initialThreadId);
        setScrollThreadId(initialThreadId);
        onInitialThreadConsumed?.(initialThreadId);
    }, [initialThreadId, threadsLoaded, workItems, setView, onInitialThreadConsumed]);
    const retryDay = useCallback(() => {
        setLoadAttempt((n) => n + 1);
        invalidateActivitySessionCache();
    }, []);
    const viewIds = useId();

    const visibleWorkItems = useMemo(
        () => (query ? workItems.filter((item) => workItemMatchesQuery(item, query)) : workItems),
        [workItems, query],
    );

    // The section panel labels two sections with their row counts; report the
    // unfiltered totals so a search query never shrinks them.
    const stitchedRowModel = useMemo(() => stitchedRows(stitched, pickUp?.threadId), [stitched, pickUp?.threadId]);
    const stitchedHead = useMemo(
        () => (stitchedStatus === 'ready' ? stitchedHeadline(t, visibleStitched, pickUp?.threadId) : null),
        [t, stitchedStatus, visibleStitched, pickUp?.threadId],
    );
    const stitchedSub = useMemo(
        // One active time per day (activity review v1.0.0 #5): the Your day
        // number, unless a search narrows the rows.
        () => (stitchedHead ? stitchedSubline(t, visibleStitched, query || !dailyData?.totalActiveMs ? undefined : dailyData.totalActiveMs, i18n.resolvedLanguage ?? i18n.language) : null),
        [t, i18n, stitchedHead, visibleStitched, query, dailyData?.totalActiveMs],
    );
    const agentRuns = threadState.data?.agents?.runs ?? 0;
    const agentActiveMs = threadState.data?.agents?.activeMs ?? 0;
    const agentsLine =
        agentRuns > 0 && agentActiveMs > 0
            ? t('stitched.agentsLine', { count: agentRuns, duration: formatDurationMs(agentActiveMs) })
            : null;
    const workedOnCount = view === 'stitched' ? stitchedRowModel.threads.length : workItems.length;
    useEffect(() => {
        onCountsChange?.({ workedOn: workedOnCount });
    }, [onCountsChange, workedOnCount]);

    // One accordion row open at a time across both lists.
    const [openItemId, setOpenItemId] = useState<string | null>(null);
    const handleToggleItem = useCallback((itemId: string) => {
        setOpenItemId((current) => (current === itemId ? null : itemId));
    }, []);
    useEffect(() => {
        setOpenItemId(null);
        setSelectedThreadId(null);
        setScrollThreadId(null);
    }, [dateStr]);
    const [lightbox, setLightbox] = useState<{ frames: ActivityRunFrame[]; index: number } | null>(null);
    const openFrame = useCallback((frames: ActivityRunFrame[], index: number) => setLightbox({ frames, index }), []);
    const handleMeetingDeleted = useCallback((meetingKey: string) => {
        updateActivitySessionData<MeetingNotesView[]>(`meetings:${dateStr}`, (current) =>
            (current ?? []).filter((m) => m.meetingKey !== meetingKey),
        );
    }, [dateStr]);

    // Your day is the record only (computer-conformance-answers A4): the
    // day's feed, newest first, and nothing Isoform wrote about it.
    const yourDaySection = (
        <ActivityFeed
            date={date}
            onOpenConversation={onOpenConversation}
            onOpenTopic={onOpenTopic}
            onOpenRecording={onOpenRecording}
        />
    );

    // The Your day fallback (while the digest loads) shows the list as
    // recorded, as before, without the switch.
    const renderWorkedOn = (mode: WorkedOnView, withSwitch: boolean) => {
        const headline =
            mode === 'stitched'
                ? stitchedHead
                : workItems.length > 0
                  ? t('stitched.recordedHeader')
                  : null;
        const subline = headline ? (mode === 'stitched' ? stitchedSub : null) : null;
        return (
            <section>
                <div className={cn('mb-4 flex flex-wrap gap-x-6 gap-y-3', headline ? 'items-end' : 'items-center')}>
                    <div className="flex min-w-0 flex-[1_1_320px] flex-col gap-1">
                        <h3 className={cn(sectionLabelClass, 'mb-0')}>{t('day.workedOn')}</h3>
                        {headline && <p className="display px-1 text-ink">{headline}</p>}
                        {subline && <p className="px-1 text-[14px] text-muted">{subline}</p>}
                        {mode === 'stitched' && agentsLine && (
                            <p className="px-1 text-[14px] text-muted" data-testid="activity-agents-line">
                                {agentsLine}
                            </p>
                        )}
                        {isToday && <ActivityCaptureLine />}
                    </div>
                    {withSwitch && (
                    <div
                        role="tablist"
                        aria-label={t('stitched.viewLabel')}
                        className="flex shrink-0 gap-0.5 rounded-[10px] border border-border bg-panel-warm p-[3px]"
                    >
                        {WORKED_ON_VIEWS.map((tab) => (
                            <button
                                key={tab}
                                id={`${viewIds}-tab-${tab}`}
                                type="button"
                                role="tab"
                                data-worked-on-view={tab}
                                aria-selected={view === tab}
                                aria-controls={`${viewIds}-panel`}
                                tabIndex={view === tab ? 0 : -1}
                                onClick={() => setView(tab)}
                                onKeyDown={(e) => {
                                    // Arrow keys move between the two views.
                                    if (e.key !== 'ArrowLeft' && e.key !== 'ArrowRight') return;
                                    e.preventDefault();
                                    const next = tab === 'stitched' ? 'recorded' : 'stitched';
                                    setView(next);
                                    document.getElementById(`${viewIds}-tab-${next}`)?.focus();
                                }}
                                className={cn(
                                    'h-[30px] rounded-lg px-3 text-[13px] font-medium text-ink transition-colors duration-[120ms]',
                                    'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-border-focus',
                                    view === tab ? 'bg-surface-active' : 'hover:bg-surface-hover',
                                )}
                            >
                                {tab === 'stitched' ? t('stitched.stitchedTab') : t('stitched.recordedTab')}
                            </button>
                        ))}
                    </div>
                    )}
                    {withSwitch && onOpenTimeline && (
                        <button
                            type="button"
                            data-testid="activity-open-timeline"
                            onClick={onOpenTimeline}
                            className="flex h-[36px] shrink-0 items-center gap-1.5 rounded-[10px] border border-border px-3 text-[13px] font-medium text-ink hover:bg-surface-hover focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-border-focus transition-colors duration-[120ms]"
                        >
                            <Film className="size-3.5 shrink-0" />
                            {t('viewMode.timeline')}
                        </button>
                    )}
                </div>
                <div
                    role={withSwitch ? 'tabpanel' : undefined}
                    id={withSwitch ? `${viewIds}-panel` : undefined}
                    aria-labelledby={withSwitch ? `${viewIds}-tab-${view}` : undefined}
                >
                    {mode === 'stitched' ? (
                        <ActivityStitchedView
                            items={visibleStitched}
                            resumeThreadId={pickUp?.threadId}
                            threads={threads}
                            status={stitchedStatus}
                            query={searchQuery.trim()}
                            selectedThreadId={selectedThreadId}
                            onSelectThread={setSelectedThreadId}
                            scrollThreadId={scrollThreadId}
                            onScrolled={clearScrollThread}
                            pickUp={
                                pickUp && visibleStitched.some((item) => item.thread.id === pickUp.threadId)
                                    ? pickUp
                                    : null
                            }
                            onPickUpShow={showPickUp}
                            onPickUpLater={pickUpLater}
                            onRetry={retryDay}
                            empty={isToday && firstDay.firstDays ? <FirstDayNowRow /> : undefined}
                        />
                    ) : (
                        <>
                            {activitiesFailed && (
                                <div className="mb-3">
                                    <StitchedFailure onRetry={retryDay} />
                                </div>
                            )}
                            {visibleWorkItems.length > 0 ? (
                                <ul className="divide-y divide-border/60 rounded-xl border border-border bg-panel overflow-hidden">
                                    {visibleWorkItems.map((item) => (
                                        <WorkItemRow
                                            key={item.id}
                                            item={item}
                                            threads={threads}
                                            open={openItemId === item.id || (item.kind === 'thread' && !!query)}
                                            onToggleItem={handleToggleItem}
                                            bridgePort={bridgePort}
                                            onOpenFrame={openFrame}
                                            onOpenRunDetail={onOpenRunDetail}
                                            onMeetingDeleted={handleMeetingDeleted}
                                            onActivityDeleted={handleActivityDeleted}
                                            onActivityChanged={reloadActivities}
                                            onStartChat={onStartChat}
                                        />
                                    ))}
                                </ul>
                            ) : activitiesFailed ? null : (
                                !query && isToday ? <ActivityTodaySoon /> : (
                                <p className="text-[12px] text-muted/70 px-1">
                                    {/* Today, empty means "not yet" (first run 1.0): say when it fills. */}
                                    {query ? t('day.noMatches', { query: searchQuery.trim() }) : isToday ? t('day.workedOnSoon') : t('day.sectionEmpty')}
                                </p>
                                )
                            )}
                        </>
                    )}
                </div>
                {withSwitch && <ScreenAgentRuns date={date} />}
            </section>
        );
    };
    const workedOnSection = renderWorkedOn(view, true);


    return (
        // The page remounts on every open; the day's scroll offset survives in
        // the facade, and the warm cache makes the height right on the first
        // commit of a revisit.
        <div
            ref={scrollRef}
            className="flex-1 overflow-y-auto"
            onScroll={(event) => rememberActivityScroll(`day:${dateStr}`, event.currentTarget.scrollTop)}
        >
            {/* What you worked on is wider so the timeline holds at 1280 px. */}
            <div className={cn('mx-auto px-6 py-6 space-y-6', section === 'workedOn' ? 'max-w-4xl' : 'max-w-2xl')}>
                {section ? (
                    <>
                        {/* One section at a time (the Activity page's panel). */}
                        {section === 'yourDay' && yourDaySection}
                        {section === 'workedOn' && workedOnSection}
                    </>
                ) : (
                    <>
                        {yourDaySection}
                        {(workItems.length > 0 || query || activitiesFailed || threadsFailed) && workedOnSection}
                    </>
                )}
            </div>
            {lightbox && (
                <FrameLightbox
                    frames={lightbox.frames}
                    index={lightbox.index}
                    highlightKeywords={[]}
                    matchedPaths={[]}
                    bridgePort={bridgePort}
                    onClose={() => setLightbox(null)}
                />
            )}
        </div>
    );
}

// ── "What you worked on" rows ──

type WorkItem =
    | { kind: 'activity'; id: string; startMs: number; activity: ActivityView }
    | { kind: 'thread'; id: string; startMs: number; thread: ActivityThreadView; moments: ActivityView[] }
    | { kind: 'meeting'; id: string; startMs: number; meeting: MeetingNotesView }
    | { kind: 'run'; id: string; startMs: number; run: CronRunWithJobName };

function activityMatchesQuery(a: ActivityView, query: string): boolean {
    return (
        a.title.toLowerCase().includes(query) ||
        a.description.toLowerCase().includes(query) ||
        a.latestChange.toLowerCase().includes(query) ||
        a.apps.some((app) => app.appName.toLowerCase().includes(query)) ||
        a.entities.some((e) => e.name.toLowerCase().includes(query))
    );
}

function workItemMatchesQuery(item: WorkItem, query: string): boolean {
    switch (item.kind) {
        case 'activity':
            return activityMatchesQuery(item.activity, query);
        case 'thread':
            return (
                item.thread.title.toLowerCase().includes(query) ||
                item.moments.some((moment) => activityMatchesQuery(moment, query))
            );
        case 'meeting': {
            const m = item.meeting;
            return (
                m.title.toLowerCase().includes(query) ||
                m.meetingApp.toLowerCase().includes(query) ||
                m.summary.toLowerCase().includes(query)
            );
        }
        case 'run':
            return item.run.jobName.toLowerCase().includes(query);
    }
}

/** The day's agent runs the screen saw (conformance answers v1.1.0 P1, AC9):
 *  Screen rows, "Screen · {agent}", with the line saying they worked on their
 *  own. They come after your own rows and are not counted in your time or
 *  your work. They are the feed's agent rows, so Forget is the feed's. */
function ScreenAgentRuns({ date }: { date: Date }) {
    const { t } = useTranslation('activity');
    const [rows, setRows] = useState<ActivityFeedRow[] | null>(null);
    const [failed, setFailed] = useState(false);
    const start = dayStartMs(date);
    const end = dayEndMs(date);
    const load = useCallback(() => {
        GetActivityFeed(start, end)
            .then((list) => {
                setRows((list ?? []).filter((row) => row.kind === 'agent'));
                setFailed(false);
            })
            .catch(() => setFailed(true));
    }, [start, end]);
    useEffect(() => {
        setRows(null);
        load();
    }, [load]);
    const forget = useActivityForget(
        (row) => setRows((prev) => (prev ?? []).filter((r) => r.id !== row.id)),
        load,
    );
    if (!failed && (rows === null || rows.length === 0) && !forget.notice) return forget.dialog;
    return (
        <div className="mt-4" data-testid="worked-on-agent-runs">
            {failed && <ActivityInlineNotice notice={{ tone: 'error', text: t('day.agentRunsFailed'), onAction: load }} />}
            {forget.notice}
            {rows && rows.length > 0 && (
                <ol className="flex flex-col">
                    {rows.map((row) => (
                        <ActivityFeedRowItem key={row.id} row={row} onForget={forget.ask} />
                    ))}
                </ol>
            )}
            {forget.dialog}
        </div>
    );
}

interface WorkItemRowProps {
    item: WorkItem;
    threads: ActivityThreadView[];
    open: boolean;
    onToggleItem: (itemId: string) => void;
    bridgePort: number;
    onOpenFrame: (frames: ActivityRunFrame[], index: number) => void;
    onOpenRunDetail: (jobId: string) => void;
    onMeetingDeleted: (meetingKey: string) => void;
    onActivityDeleted: (activityId: string) => void;
    onActivityChanged: () => void;
    onStartChat?: (prompt: string, context: TodayChatContextRef) => void;
}

const WorkItemRow = memo(function WorkItemRow({
    item,
    threads,
    open,
    onToggleItem,
    bridgePort,
    onOpenFrame,
    onOpenRunDetail,
    onMeetingDeleted,
    onActivityDeleted,
    onActivityChanged,
    onStartChat,
}: WorkItemRowProps) {
    const onToggle = useCallback(() => onToggleItem(item.id), [onToggleItem, item.id]);
    const moveControl = (activityId: string, currentThreadId: string | null): ReactNode => (
        <ThreadMoveControl activityId={activityId} currentThreadId={currentThreadId} threads={threads} />
    );
    switch (item.kind) {
        case 'activity':
            return (
                <ActivityWorkRow
                    activity={item.activity}
                    open={open}
                    onToggle={onToggle}
                    bridgePort={bridgePort}
                    onOpenFrame={onOpenFrame}
                    onDeleted={onActivityDeleted}
                    onChanged={onActivityChanged}
                    onStartChat={onStartChat}
                    threadControl={moveControl(
                        item.activity.id,
                        threads.find((thread) => thread.activityIds.includes(item.activity.id))?.id ?? null,
                    )}
                />
            );
        case 'thread':
            return (
                <ThreadWorkRow
                    thread={item.thread}
                    moments={item.moments}
                    open={open}
                    onToggle={onToggle}
                    renderMoment={(moment, momentOpen, onMomentToggle) => (
                        <ActivityWorkRow
                            key={moment.id}
                            activity={moment}
                            open={momentOpen}
                            onToggle={onMomentToggle}
                            bridgePort={bridgePort}
                            onOpenFrame={onOpenFrame}
                            onDeleted={onActivityDeleted}
                            onChanged={onActivityChanged}
                            onStartChat={onStartChat}
                            threadControl={moveControl(moment.id, item.thread.id)}
                        />
                    )}
                />
            );
        case 'meeting':
            return (
                <MeetingWorkRow
                    meeting={item.meeting}
                    open={open}
                    onToggle={onToggle}
                    bridgePort={bridgePort}
                    onDeleted={() => onMeetingDeleted(item.meeting.meetingKey)}
                />
            );
        case 'run':
            return <RunWorkRow run={item.run} onOpenRunDetail={onOpenRunDetail} />;
    }
// Handler props are excluded from the comparison: they dispatch to stable
// setters or day-scoped callbacks, and the day they close over is also what
// `item`/`threads` identity tracks — a new day re-renders every row anyway.
}, (prev, next) =>
    prev.item === next.item &&
    prev.threads === next.threads &&
    prev.open === next.open &&
    prev.bridgePort === next.bridgePort);

const rowTimeClass = 'w-14 shrink-0 text-[11px] text-muted/80 tabular-nums pt-[2px]';

/** "10:02–10:30 AM · 28m active"; under a minute, the time alone. */
function rowTimeLine(t: TFunction<'activity'>, startMs: number, endMs: number, activeMs: number): string {
    const range = formatTimeRangeFromMs(startMs, endMs);
    return activeMs >= 60_000 ? `${range} · ${t('threads.timeSpent', { duration: formatDurationMs(activeMs) })}` : range;
}

// Go's topic finds job (topic_finds.go topicFindsJobName).
const TOPIC_FINDS_JOB_NAME = 'New on your topics';

/** A quiet marker for an activity with recent evidence still in progress. */
function LiveDot({ className }: { className?: string }) {
    return <span className={cn('h-1.5 w-1.5 shrink-0 rounded-full bg-accent-2', className)} />;
}

const OUTCOME_STATUS_KEYS = {
    reported: 'activityCard.outcome.reported',
    fixed: 'activityCard.outcome.fixed',
    done: 'activityCard.outcome.done',
    abandoned: 'activityCard.outcome.abandoned',
} as const;

const OUTCOME_REASON_KEYS = {
    message_sent: 'activityCard.outcomeReason.messageSent',
    target_changed: 'activityCard.outcomeReason.targetChanged',
    user_completed: 'activityCard.outcomeReason.userCompleted',
    completion_click: 'activityCard.outcomeReason.completionClick',
    quiet_timeout: 'activityCard.outcomeReason.quietTimeout',
} as const;

/** The outcome the old activity card showed: "Reported to X at 3:12 PM",
 *  Fixed, Done or Abandoned, with its evidence reason. Same data, same copy. */
function ActivityOutcomeLine({ outcome }: { outcome: ActivityOutcome }) {
    const { t, i18n } = useTranslation('activity');
    const time = new Date(outcome.occurredAt).toLocaleTimeString(i18n.language, { hour: 'numeric', minute: '2-digit' });
    const status = outcome.status === 'reported' && outcome.target
        ? t('activityCard.outcome.reportedTo', { target: outcome.target, time })
        : t(OUTCOME_STATUS_KEYS[outcome.status], { time });
    const reasonKey = OUTCOME_REASON_KEYS[outcome.reason as keyof typeof OUTCOME_REASON_KEYS]
        ?? 'activityCard.outcomeReason.recordedEvidence';
    return (
        <p className="text-[12px] leading-relaxed text-muted">
            {t('activityCard.statusWithReason', { status, reason: t(reasonKey) })}
        </p>
    );
}

function ActivityWorkRow({
    activity,
    open,
    onToggle,
    bridgePort,
    onOpenFrame,
    onDeleted,
    onChanged,
    threadControl,
    onStartChat,
}: {
    activity: ActivityView;
    open: boolean;
    onToggle: () => void;
    bridgePort: number;
    onOpenFrame: (frames: ActivityRunFrame[], index: number) => void;
    onDeleted: (activityId: string) => void;
    onChanged: () => void;
    /** Not right on a moment: move it to another thread or split it off. */
    threadControl?: ReactNode;
    onStartChat?: (prompt: string, context: TodayChatContextRef) => void;
}) {
    const { t } = useTranslation('activity');
    useAppIcons(activity.apps.map((a) => a.appKey));
    // The words Today would write for this moment; Go's prepared draft
    // replaces them while untouched, and Send reads the moment again.
    // Chat about this asks in place, with the moment as the chat's context.
    const chatContext: TodayChatContextRef | undefined = onStartChat
        ? { sourceKind: 'activity', sourceId: activity.id, dayStartMs: dayStartMs(new Date(activity.startMs)) }
        : undefined;
    // The same two-step confirm the old activity card had: the action turns
    // into Delete / Cancel in place.
    const [confirmingDelete, setConfirmingDelete] = useState(false);
    const [deleteFailed, setDeleteFailed] = useState(false);
    const doDelete = () => {
        setDeleteFailed(false);
        DeleteActivity(activity.id)
            .then(() => onDeleted(activity.id))
            .catch((err) => {
                console.error('[activity] delete activity failed:', err);
                setDeleteFailed(true);
            });
    };
    // Every row's second line is its time and active time; the summary shows
    // when the row is opened (activity review v1.0.6 AR40).
    const oneLine = rowTimeLine(t, activity.startMs, activity.endMs, activity.activeMs);
    // People and the real repos/projects/pull-requests/channels this moment
    // touched. Topics stay out: they were the keyword chips, which the Day
    // view drops.
    const people = activity.entities.filter((e) => e.type === 'person').map((e) => e.name);
    const links = activity.entities
        .filter((e) => e.type !== 'person' && e.type !== 'topic')
        .map((e) => e.name);
    return (
        <li id={`moment-${activity.id}`}>
            <button
                type="button"
                onClick={onToggle}
                aria-expanded={open}
                className="w-full flex items-start gap-3 px-4 py-3 text-left hover:bg-surface-hover transition-colors"
            >
                <time className={rowTimeClass}>{formatTimeFromMs(activity.startMs)}</time>
                <div className="flex-1 min-w-0">
                    <div className="flex items-center gap-2">
                        <span className="text-[13px] font-medium text-ink truncate">{activity.title}</span>
                        {activity.inProgress && (
                            <span className="inline-flex shrink-0 items-center gap-1.5 rounded-full border border-border px-2 py-0.5 text-[10px] font-medium text-muted">
                                <LiveDot />
                                {t('activityCard.live')}
                            </span>
                        )}
                    </div>
                    {oneLine && <p className="text-[12px] text-muted truncate mt-0.5">{oneLine}</p>}
                </div>
                {activity.apps.length > 0 && (
                    <span className="flex items-center shrink-0 pt-0.5">
                        {activity.apps.slice(0, 4).map((app, i) => (
                            <span
                                key={app.appKey || app.appName}
                                title={app.appName}
                                className={cn('rounded-[3px] bg-panel ring-1 ring-panel', i > 0 && '-ml-1')}
                            >
                                <AppGlyph appKey={app.appKey} appName={app.appName} className="h-4 w-4" />
                            </span>
                        ))}
                    </span>
                )}
                <ChevronDown
                    className={`h-3.5 w-3.5 shrink-0 mt-1 text-muted/60 transition-transform ${open ? 'rotate-180' : ''}`}
                />
            </button>
            {open && (
                <div className="px-4 pb-4 pt-1 space-y-3">
                    {activity.description && (
                        <p className="text-[13px] text-ink/80 leading-relaxed">{activity.description}</p>
                    )}
                    {activity.latestChange && activity.latestChange !== activity.description && (
                        <p className="text-[12px] text-muted leading-relaxed">{activity.latestChange}</p>
                    )}
                    {activity.latestOutcome && <ActivityOutcomeLine outcome={activity.latestOutcome} />}
                    {people.length > 0 && (
                        <p className="text-[12px] text-muted">{t('day.people', { names: people.join(', ') })}</p>
                    )}
                    {links.length > 0 && (
                        <p className="text-[12px] text-muted">{t('day.links', { names: links.join(', ') })}</p>
                    )}
                    {activity.thumbnails.length > 0 && bridgePort > 0 && (
                        <div className="flex flex-wrap gap-2">
                            {activity.thumbnails.map((frame, i) => (
                                <RunFrameThumb
                                    key={`${frame.path}-${i}`}
                                    frame={frame}
                                    bridgePort={bridgePort}
                                    alt={activity.title}
                                    onClick={() => onOpenFrame(activity.thumbnails, i)}
                                />
                            ))}
                        </div>
                    )}
                    <ActivityRowActions
                        activity={activity}
                        onChanged={onChanged}
                        threadControl={threadControl}
                        chatContext={chatContext}
                        deleteControl={
                            confirmingDelete ? (
                                <div className="flex items-center gap-1">
                                    <button
                                        type="button"
                                        onClick={doDelete}
                                        className="text-[11px] px-1.5 py-0.5 rounded bg-panel-warm text-error hover:bg-panel-warm"
                                    >
                                        {t('activityCard.confirmDelete')}
                                    </button>
                                    <button
                                        type="button"
                                        onClick={() => setConfirmingDelete(false)}
                                        className="text-[11px] px-1.5 py-0.5 rounded text-muted hover:text-ink"
                                    >
                                        {t('activityCard.cancel')}
                                    </button>
                                </div>
                            ) : (
                                <button
                                    type="button"
                                    onClick={() => setConfirmingDelete(true)}
                                    className="inline-flex items-center gap-1 text-[11px] text-muted hover:text-error transition-colors"
                                >
                                    <Trash2 className="h-3 w-3" />
                                    {t('day.deleteFromHistory')}
                                </button>
                            )
                        }
                    />
                    {deleteFailed && <p role="alert" className="text-[12px] text-copper">{t('day.deleteMomentFailed')} <button type="button" className={ACTION_LINK} onClick={doDelete}>{t('ask.tryAgain')}</button></p>}
                </div>
            )}
        </li>
    );
}

function MeetingWorkRow({
    meeting,
    open,
    onToggle,
    bridgePort,
    onDeleted,
}: {
    meeting: MeetingNotesView;
    open: boolean;
    onToggle: () => void;
    bridgePort: number;
    onDeleted: () => void;
}) {
    const { t } = useTranslation('activity');
    const title =
        meeting.title ||
        (meeting.meetingApp ? t('meetingNotes.callTitle', { app: meeting.meetingApp }) : t('meetingNotes.untitledCall'));
    const oneLine = rowTimeLine(t, meeting.startTime, meeting.endTime, meeting.endTime - meeting.startTime);
    return (
        <li>
            <button
                type="button"
                onClick={onToggle}
                aria-expanded={open}
                className="w-full flex items-start gap-3 px-4 py-3 text-left hover:bg-surface-hover transition-colors"
            >
                <time className={rowTimeClass}>{formatTimeFromMs(meeting.startTime)}</time>
                <div className="flex-1 min-w-0">
                    <div className="text-[13px] font-medium text-ink truncate">{title}</div>
                    {oneLine && <p className="text-[12px] text-muted truncate mt-0.5">{oneLine}</p>}
                </div>
                {meeting.meetingApp && (
                    <span className="shrink-0 pt-0.5" title={meeting.meetingApp}>
                        <AppGlyph appKey="" appName={meeting.meetingApp} className="h-4 w-4" />
                    </span>
                )}
                <ChevronDown
                    className={`h-3.5 w-3.5 shrink-0 mt-1 text-muted/60 transition-transform ${open ? 'rotate-180' : ''}`}
                />
            </button>
            {open && (
                <div className="px-4 pb-4 pt-1">
                    <MeetingNotesCard meeting={meeting} bridgePort={bridgePort} onDeleted={onDeleted} />
                </div>
            )}
        </li>
    );
}

function RunWorkRow({ run, onOpenRunDetail }: { run: CronRunWithJobName; onOpenRunDetail: (jobId: string) => void }) {
    const { t } = useTranslation('activity');
    const result = displayableRunResult(run.resultSummary);
    const errorText = displayableRunResult(run.error);
    return (
        <li>
            <button
                type="button"
                onClick={() => onOpenRunDetail(run.jobId)}
                className="w-full flex items-start gap-3 px-4 py-3 text-left hover:bg-surface-hover transition-colors"
            >
                <time className={rowTimeClass}>{formatTimeFromMs(run.startedAt)}</time>
                <Zap className="h-3.5 w-3.5 shrink-0 mt-[3px] text-automation" />
                <div className="flex-1 min-w-0">
                    <div className="text-[13px] font-medium text-ink truncate">
                        {run.jobName === TOPIC_FINDS_JOB_NAME
                            ? t('day.isoformUpdatedTopics')
                            : t('day.isoformRan', { name: run.jobName || t('day.unknownSchedule') })}
                    </div>
                    {result ? (
                        <p className="text-[12px] text-muted truncate mt-0.5">{result}</p>
                    ) : errorText ? (
                        <p className="text-[12px] text-error truncate mt-0.5">{errorText}</p>
                    ) : null}
                </div>
                {!result && (
                    <span className="shrink-0 text-[11px] text-accent-2 pt-[3px]">{t('day.openResult')}</span>
                )}
            </button>
        </li>
    );
}

/** Today with no sessions yet. On the first two days it says when sessions
 *  start and shows what Isoform sees right now as one live row (first-day
 *  handoff §5); otherwise the usual "after about 30 minutes" line. */
function ActivityTodaySoon() {
    const { t } = useTranslation('activity');
    const firstDay = useFirstDay();
    if (!firstDay.firstDays) return <p className="text-[12px] text-muted/70 px-1">{t('day.workedOnSoon')}</p>;
    return <FirstDayNowRow />;
}

function FirstDayNowRow() {
    const { t } = useTranslation('activity');
    const now = useDoingNow();
    const app = now && !now.frontAppHidden ? (now.app || now.frontApp || '') : '';
    if (!app) return <p className="text-[12px] text-muted/70 px-1">{t('day.workedOnSoon')}</p>;
    return (
        <div className="flex flex-col gap-2 px-1" data-testid="activity-first-day">
            <p className="text-[12px] text-muted">{t('firstDay.rightNow')}</p>
            <div className="flex items-center gap-2.5 rounded-xl border border-border bg-panel px-3.5 py-2.5" data-testid="activity-now-row">
                <span className="size-2 shrink-0 rounded-full bg-accent animate-breathe opacity-80 motion-reduce:animate-none" aria-hidden />
                <span className="min-w-0 flex-1 truncate text-[13px] text-ink">{[app, now?.title].filter(Boolean).join(' · ')}</span>
                <span className="shrink-0 text-[12px] font-medium text-accent">{t('firstDay.now')}</span>
            </div>
        </div>
    );
}
