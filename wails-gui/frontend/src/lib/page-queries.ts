import { useEffect, useRef } from 'react';
import * as app from '@/lib/wails-shim-app';
import { EventsOn as RuntimeEventsOn } from '../../wailsjs/runtime/runtime';
import { pageQueryCache } from './query-cache';
import { clearSavedPeopleView } from './people-saved-view';
import { onPagePrivacyCacheWithdrawal, onPagePrivacyWithdrawal, resetPagePrivacyWithdrawal } from './page-privacy-withdrawal';
import { briefSlotKey } from './brief-time';
import { CHAT_FIRST_PAGE_SIZE } from './chat-page-size';
import { appActive, onAppActivity, resetAppActivity, waitForAppActive } from './app-activity';
import {
    listMainChatWaitingItemsWithOpenAnswerDelivery,
    loadStreamItemsPageWithOpenAnswerDelivery,
} from './open-answer-delivery';

pageQueryCache.setActivityGate(waitForAppActive);

const today = ['GetNeedsYou', 'GetTodayDayShape', 'GetTodayBeforeYouTalk', 'GetTodayNews', 'GetEngagementWeekSummary', 'GetMobileBriefHealth', 'GetTodaySuggestion', 'GetTodayQuietLines', 'GetTodayTopics', 'GetLatestBrief', 'GetTopicFinds', 'GetGoalCheckIn', 'GetPortraitTodayCard', 'IsClosedEyesModeEnabled'];
const chat = ['LoadStreamItemsPage', 'GetMainChatOverview', 'ListMainChatWaitingItems', 'ListMainChatAnswerChips'];
// Every other page's primary reads. Activity's names are the ones its cache
// facade (timeline/activity-session-cache.ts) reads under; 'Activity:meetings'
// is the facade's synthetic name for the day's meeting notes.
// GetCaptureCounts ("Today Isoform saw N screens") is Activity's since 1.0; it
// is read after the page paints, never on Today's click path.
const activity = ['EnsureDailySummary', 'GetActivitiesInRange', 'GetDayThreads', 'GetCronRunsByDateRange', 'GetKnowledgeEntries', 'GetMemoryEntries', 'Activity:meetings', 'GetDayNoticed', 'GetCaptureCounts'];
const goals = ['ListGoals'];
const aboutYou = ['GetAboutYou', 'GetAboutYouPortrait', 'GetMemoryEntries', 'GetMemoryStatus', 'GetMemoryEvidenceReadable', 'GetEntryUses', 'ListMemoryVersions'];
const knowHow = ['ListKnowHow', 'GetKnowledgeEntries', 'GetKnowHowMatch'];
const people = ['ListPeopleOverview', 'ListPrincipalsWithStats', 'GetContactsAccessStatus', 'GetPersonDetail', 'ListPrincipalActivitySessions', 'GetPersonResearch', 'GetVoiceEnrollmentStatus', 'ListVoiceSamples'];
const settings = ['GetSettingsOverview', 'GetMobileBriefSchedule', 'GetPlatformPermissions', 'GetBackgroundListening', 'GetAutomaticMeetingRecordingEnabled', 'GetTextChannelStatus', 'GetSMSChannelStatus', 'LoadUIState'];
const search = ['GetTodayTopics', 'ListHandoffs'];
const firstDay = ['GetFirstWeekDay', 'GetAssistantHistory', 'GetCalendarFirstDay'];
const everyPage = [...today, ...chat, ...activity, ...goals, ...aboutYou, ...knowHow, ...people, ...settings, ...search, ...firstDay];
const eventReads: Record<string, string[]> = {};
function on(names: string[], reads: string[]) {
    for (const name of names) eventReads[name] = [...new Set([...(eventReads[name] ?? []), ...reads])];
}
// The brief is read again only when it is stored or amended, or a build starts
// or stops (R24). Source events never re-read it.
on(['brief:stored', 'brief:building'], ['GetLatestBrief']);
// The backend derives this event from real calendar/hold changes. Source
// notifications alone must not clear an unchanged strip or its local preview.
on(['today-dayshape:updated'], ['GetTodayDayShape', 'GetTodayBeforeYouTalk']);
on(['calendar:synced', 'activity:session-saved', 'activity:activities-updated', 'mail:synced', 'people:updated', 'session:items-appended'], ['GetTodayBeforeYouTalk']);
on(['mobile:updated'], ['GetEngagementWeekSummary', 'GetMobileBriefHealth', 'GetMobileBriefSchedule']);
on(['initiative:updated'], ['GetTodaySuggestion']);
// Quiet lines change with each brief and with the notice settings; Today
// otherwise reads them at most every 30 minutes (useTodayQuietLines).
on(['brief:stored', 'engagement:settings-changed'], ['GetTodayQuietLines']);
on(['engagement:settings-changed'], ['GetTodayBeforeYouTalk']);
on(['cron:run', 'model-accounts:updated', 'ai:config-changed'], ['GetTodayNews']);
on(['topics:finds', 'topics:updated', 'model-accounts:updated', 'ai:config-changed'], ['GetTopicFinds']);
on(['topics:updated', 'topics:withdrawn'], ['GetTodayTopics']);
on(['activity:session-saved', 'calendar:synced'], ['GetCaptureCounts']);
on(['agency:updated'], ['GetGoalCheckIn', 'ListGoals']);
// The Noticed strip re-reads when a summary lands or a follow-up changes.
// The Noticed strip re-reads only when its own set really changes: the backend
// emits noticed:updated only then. agency:updated and activity:activities-updated
// fire constantly at launch, and every read they caused returned the same rows.
on(['noticed:updated'], ['GetDayNoticed']);
on(['session:items-appended'], ['LoadStreamItemsPage', 'ListMainChatAnswerChips']);
on(['main-chat:chips'], ['ListMainChatAnswerChips']);
on(['main-chat:waiting-changed', 'main-chat:waiting-items-updated'], ['ListMainChatWaitingItems']);
on(['contacts:updated'], people);
on(['people:research'], ['GetPersonResearch']);
on(['insight:saved', 'knowledge:updated'], knowHow);
on(['handoff:created', 'handoff:updated'], ['ListHandoffs']);
// The first day: the history read moves through reading, pass 1 and pass 2.
on(['assistant-history:updated'], ['GetAssistantHistory', 'GetAboutYou', 'ListKnowHow']);
on(['calendar:synced', 'today-dayshape:updated'], ['GetCalendarFirstDay']);
// A sign-out or provider change re-reads the account-derived overview rows.
on(['model-accounts:updated', 'ai:config-changed'], ['GetSettingsOverview']);
on(['automatic-meeting-recording:changed'], ['GetAutomaticMeetingRecordingEnabled']);
on(['cron:state'], ['ListCronJobs']);
// A portrait written, failed or started in the background.
on(['portrait:updated'], ['GetAboutYouPortrait', 'GetPortraitTodayCard']);
// Scheduled snapshot builds that changed a page (Go derived_views.go). The
// re-read returns the new snapshot; it never builds one.
on(['about-you:updated'], ['GetAboutYou']);
on(['people:overview-updated'], ['ListPeopleOverview', 'ListPrincipalsWithStats']);
on(['today-before-you-talk:updated'], ['GetTodayBeforeYouTalk']);

// These transitions change what the backend permits a reader to see. Never
// return a pre-transition value, even to an in-flight caller or hidden page.
const PRIVACY_SWEEP_EVENTS = ['memory:updated', 'closed-eyes:changed', 'background-listening:changed', 'permissions:changed', 'sensitive-apps:updated'];
// The brief is sealed by the backend for privacy changes (its own privacy class),
// so only those re-read it. memory:updated and permissions:changed are freshness
// events for the brief: the index emits memory:updated on every update, and a
// re-read for each one is the R24 loop (the withdrawal path still clears it).
const BRIEF_PRIVACY_EVENTS = ['closed-eyes:changed', 'background-listening:changed', 'sensitive-apps:updated'];
const everyPageButBrief = everyPage.filter((name) => name !== 'GetLatestBrief');
on(PRIVACY_SWEEP_EVENTS.filter((name) => !BRIEF_PRIVACY_EVENTS.includes(name)), everyPageButBrief);
on(BRIEF_PRIVACY_EVENTS, everyPage);

export { onPagePrivacyWithdrawal } from './page-privacy-withdrawal';

export function usePagePrivacyWithdrawal(withdraw: () => void) {
    const latest = useRef(withdraw);
    latest.current = withdraw;
    useEffect(() => {
        installPageQueryEvents();
        return onPagePrivacyWithdrawal(() => latest.current());
    }, []);
}

const handlers = new Map<string, Set<(...args: any[]) => void>>();
function dispatch(event: string, ...args: any[]) {
    return Promise.all([...(handlers.get(event) ?? [])].map((handler) => handler(...args)));
}
export function PageEventsOn(event: string, handler: (...args: any[]) => void) {
    installPageQueryEvents();
    const set = handlers.get(event) ?? new Set();
    set.add(handler);
    handlers.set(event, set);
    return () => { set.delete(handler); };
}
// Bursts of one event (noticed:updated for several days at once) invalidate
// their readers once per window, not once per event.
const COALESCED_EVENTS = new Set(['noticed:updated']);
export const COALESCE_EVENT_MS = 250;
function coalesceEvent(event: string, names: string[]): () => void {
    let timer: ReturnType<typeof setTimeout> | undefined;
    let last: unknown;
    const flush = () => {
        timer = undefined;
        pageQueryCache.invalidate(names, true);
        void dispatch(event, last);
    };
    const off = RuntimeEventsOn(event, (data?: unknown) => {
        last = data;
        if (timer === undefined) timer = setTimeout(flush, COALESCE_EVENT_MS);
    });
    return () => {
        if (timer !== undefined) clearTimeout(timer);
        off();
    };
}
let installed = false;
const cleanups: (() => void)[] = [];
// Publications and progress keep the last checked display while their
// replacement is read (a remount paints it at once); every other event,
// privacy above all, erases it.
const KEEP_SHOWN_EVENTS = new Set(['topics:updated', 'today-dayshape:updated', 'brief:building', 'portrait:updated', 'about-you:updated', 'people:overview-updated', 'main-chat:waiting-items-updated', 'today-before-you-talk:updated']);
export function installPageQueryEvents() {
    if (installed) return;
    installed = true;
    let wasAway = !appActive();
    cleanups.push(onAppActivity((active) => {
        // Back in view, re-read only what is past its budget: a fresh entry
        // keeps painting from the cache, and no page is told to read it again.
        if (active && wasAway) pageQueryCache.invalidateStale([...today, ...activity, ...aboutYou, ...knowHow, ...firstDay]);
        wasAway = !active;
    }));
    cleanups.push(onPagePrivacyCacheWithdrawal(() => {
        pageQueryCache.clear();
        clearSavedPeopleView();
    }));
    for (const [event, names] of Object.entries(eventReads)) {
        if (COALESCED_EVENTS.has(event)) {
            cleanups.push(coalesceEvent(event, names));
            continue;
        }
        cleanups.push(RuntimeEventsOn(event, (data?: { status?: string }) => {
            if (event === 'cron:run' && data?.status === 'missed') return;
            // Publications keep the last checked display available to a
            // remount while its replacement is read. Privacy events still erase.
            pageQueryCache.invalidate(names, !KEEP_SHOWN_EVENTS.has(event));
            return dispatch(event, data);
        }));
    }
    for (const [event, name] of Object.entries({
        'background-listening:changed': 'GetBackgroundListening',
        'automatic-meeting-recording:changed': 'GetAutomaticMeetingRecordingEnabled',
    })) {
        cleanups.push(RuntimeEventsOn(event, (enabled: boolean) => {
            pageQueryCache.set(name, [], Boolean(enabled));
        }));
    }
    cleanups.push(RuntimeEventsOn('agent:event', (event?: { type?: string }) => {
        if (['done', 'error', 'close'].includes(event?.type ?? '')) pageQueryCache.invalidate(chat);
        return dispatch('agent:event', event);
    }));
    for (const [event, names] of Object.entries({ 'isoform:sidebar-topics-changed': ['GetTodayTopics'], 'isoform:main-chat-view-changed': chat })) {
        const invalidate = () => pageQueryCache.invalidate(names, event !== 'isoform:sidebar-topics-changed');
        window.addEventListener(event, invalidate);
        cleanups.push(() => window.removeEventListener(event, invalidate));
    }
}

const pageReads: Record<string, readonly string[]> = { today, chat, activity, goals, 'about-you': aboutYou, 'know-how': knowHow, people, settings, search };
export function revalidatePageQueries(page: string) {
    const names = pageReads[page] ?? [];
    pageQueryCache.revalidate(names);
    pageQueryCache.refreshSubscribers(names);
}
/** The reads a page's first paint consults, for the privacy sweep tests. */
export function pageReadNames(page: string): readonly string[] { return pageReads[page] ?? []; }

export function resetPageQueries() {
    cleanups.splice(0).forEach((off) => off?.());
    resetAppActivity();
    resetPagePrivacyWithdrawal();
    handlers.clear();
    installed = false;
    pageQueryCache.clear();
    clearSavedPeopleView();
}

/** Observe successful revalidation and invalidation as well as direct reads.
 * Existing hook event handlers still run; single-flight prevents duplicate IO. */
export function usePageQueryRefresh(names: readonly string[], refresh: () => unknown, enabled = true, accepts?: (args: readonly unknown[]) => boolean) {
    const latest = useRef(refresh);
    latest.current = refresh;
    const filter = useRef(accepts);
    filter.current = accepts;
    const key = names.join('|');
    useEffect(() => {
        if (!enabled) return;
        installPageQueryEvents();
        const release = pageQueryCache.retainNames(key.split('|'));
        const off = pageQueryCache.subscribe((name, args) => {
            if (key.split('|').includes(name) && (!args || !filter.current || filter.current(args))) void latest.current();
        });
        return () => { off(); release(); };
    }, [key, enabled]);
}

function cached<K extends keyof typeof app>(name: K, ttl = 60_000): typeof app[K] {
    return ((...args: unknown[]) => {
        installPageQueryEvents();
        return pageQueryCache.read(name, args, () => (app[name] as (...params: unknown[]) => Promise<unknown>)(...args), ttl);
    }) as typeof app[K];
}

export const GetCaptureCounts: typeof app.GetCaptureCounts = (...args) => {
    installPageQueryEvents();
    const prior = pageQueryCache.peek<{ notReady?: boolean }>('GetCaptureCounts', args);
    return pageQueryCache.read('GetCaptureCounts', args, () => app.GetCaptureCounts(...args), prior?.notReady ? 2500 : 60_000);
};
export const GetMainChat = cached('GetMainChat');
export const GetFirstWeekDay = cached('GetFirstWeekDay');
export const GetAssistantHistory = cached('GetAssistantHistory');
export const GetCalendarFirstDay = cached('GetCalendarFirstDay');
export const GetTodayDayShape = cached('GetTodayDayShape');
export const GetTodayBeforeYouTalk = cached('GetTodayBeforeYouTalk');
export const GetTodayNews = cached('GetTodayNews');
export const GetEngagementWeekSummary = cached('GetEngagementWeekSummary');
export const GetMobileBriefHealth = cached('GetMobileBriefHealth');
export const GetTodaySuggestion = cached('GetTodaySuggestion');
export const GetTodayQuietLines = cached('GetTodayQuietLines');
// Topics change by "topics:updated" (each rebuild publishes it), never by a
// poll (snappy-and-first-run-1.0.md, section 1). A pending projection keeps a
// shorter lifetime, the safety resync for a publication that preceded its
// listener or a bridge that reconnected; a settled one may be 2 hours old.
export const TODAY_TOPICS_PENDING_RESYNC_MS = 10 * 60_000;
export const TODAY_TOPICS_TTL_MS = 2 * 60 * 60_000;
export const GetTodayTopics: typeof app.GetTodayTopics = (...args) => {
    installPageQueryEvents();
    const prior = pageQueryCache.peek<{ refreshing?: boolean }>('GetTodayTopics', args);
    return pageQueryCache.read('GetTodayTopics', args, () => app.GetTodayTopics(...args), prior?.refreshing ? TODAY_TOPICS_PENDING_RESYNC_MS : TODAY_TOPICS_TTL_MS);
};
// The brief's budget is BRIEF_RESYNC_MS (hooks/useLatestBrief): a show or
// focus reads it again only past that age; brief events re-read it at once.
export const GetLatestBrief = cached('GetLatestBrief', 15 * 60_000);
export const GetTopicFinds = cached('GetTopicFinds');
export const GetGoalCheckIn = cached('GetGoalCheckIn');
export const LoadStreamItemsPage: typeof app.LoadStreamItemsPage = (...args) => {
    installPageQueryEvents();
    return pageQueryCache.read(
        'LoadStreamItemsPage',
        args,
        () => loadStreamItemsPageWithOpenAnswerDelivery(...args),
    ) as ReturnType<typeof app.LoadStreamItemsPage>;
};
export const GetMainChatOverview = cached('GetMainChatOverview');
export const ListMainChatWaitingItems: typeof app.ListMainChatWaitingItems = (...args) => {
    installPageQueryEvents();
    return pageQueryCache.read(
        'ListMainChatWaitingItems',
        args,
        () => listMainChatWaitingItemsWithOpenAnswerDelivery(...args),
    ) as ReturnType<typeof app.ListMainChatWaitingItems>;
};
export const ListMainChatAnswerChips = cached('ListMainChatAnswerChips');
export const ListGoals = cached('ListGoals');
export const GetAboutYou = cached('GetAboutYou');
export const GetAboutYouPortrait = cached('GetAboutYouPortrait');
export const GetPortraitTodayCard = cached('GetPortraitTodayCard');
export const GetMemoryEntries = cached('GetMemoryEntries');
export const GetMemoryStatus = cached('GetMemoryStatus');
export const ListKnowHow = cached('ListKnowHow');
export const GetKnowledgeEntries = cached('GetKnowledgeEntries');
export const GetKnowHowMatch = cached('GetKnowHowMatch');
export const ListPeopleOverview = cached('ListPeopleOverview');
export const ListPrincipalsWithStats = cached('ListPrincipalsWithStats');
export const GetContactsAccessStatus = cached('GetContactsAccessStatus');
export const GetSettingsOverview = cached('GetSettingsOverview');
export const GetMobileBriefSchedule = cached('GetMobileBriefSchedule');
export const GetPlatformPermissions = cached('GetPlatformPermissions');
export const GetBackgroundListening = cached('GetBackgroundListening', Infinity);
export const GetAutomaticMeetingRecordingEnabled = cached('GetAutomaticMeetingRecordingEnabled', Infinity);
export const ListCronJobs = cached('ListCronJobs');
export const GetTextChannelStatus = cached('GetTextChannelStatus');
export const GetSMSChannelStatus = cached('GetSMSChannelStatus');
export const LoadUIState = cached('LoadUIState');
export const ListHandoffs = cached('ListHandoffs');
export const GetNeedsYou = cached('GetNeedsYou');
export const GetDayNoticed = cached('GetDayNoticed');
// The observation gate itself: read on many pages' mounts, cleared by the
// privacy sweep like everything it gates.
export const IsClosedEyesModeEnabled = cached('IsClosedEyesModeEnabled');
// Detail-pane reads: they render when a dialog opens with a selection (a
// retained dialog keeps one, a deep link brings one), so they cache like the
// lists do.
export const GetMemoryEvidenceReadable = cached('GetMemoryEvidenceReadable');
export const GetEntryUses = cached('GetEntryUses');
export const ListMemoryVersions = cached('ListMemoryVersions');
export const GetPersonDetail = cached('GetPersonDetail');
export const ListPrincipalActivitySessions = cached('ListPrincipalActivitySessions');
export const GetPersonResearch = cached('GetPersonResearch');
export const GetVoiceEnrollmentStatus = cached('GetVoiceEnrollmentStatus');
export const ListVoiceSamples = cached('ListVoiceSamples');

export async function prefetchMainChat() {
    const main = await GetMainChat();
    const id = main?.session?.localId;
    // The chat and Today's panel open on the same last-50 page; the waiting
    // count is the in-memory state (GetMainChatWaiting), not a read here.
    if (id) await LoadStreamItemsPage(id, -1, CHAT_FIRST_PAGE_SIZE);
}
/** The strip shows the day the brief talks about: its own day while that is
 *  today, an evening brief included, otherwise today (today-review v1.1.20
 *  R44). `evening` is kept for callers; it no longer moves the day. */
export function todayDayShapeDate(briefDate?: string, _evening = false): string {
    const iso = (day: Date) => `${day.getFullYear()}-${String(day.getMonth() + 1).padStart(2, '0')}-${String(day.getDate()).padStart(2, '0')}`;
    const today = iso(new Date());
    return briefDate && /^\d{4}-\d{2}-\d{2}$/.test(briefDate) && briefDate > today ? briefDate : today;
}

export async function prefetchToday() {
    const brief = GetLatestBrief();
    const shape = brief.then((view) => {
        const document = view.brief;
        const evening = !!document && briefSlotKey(document.slot, document.asOf, document.timezone) === 'evening';
        return GetTodayDayShape(todayDayShapeDate(document?.localDate, evening));
    }, () => GetTodayDayShape(todayDayShapeDate()));
    await Promise.all([GetEngagementWeekSummary(), GetMobileBriefHealth(), GetTodayQuietLines(), GetTodayTopics(), brief, shape, GetTopicFinds(), GetGoalCheckIn()]);
}

// ── Prefetch every page ──
// One step is one read. The idle walker (lib/idle-prefetch.ts) runs them one
// at a time in the order below; a hover or focus on a navigation row runs the
// page's steps together (prefetchPage). Every step lands in pageQueryCache,
// so a visit paints the cached value and revalidates in the background.

/** A single warm read for one page. */
export interface PagePrefetchStep { page: string; name: string; run: () => Promise<unknown> }

function dayArgs(date: Date): { dateStr: string; start: number; end: number } {
    const start = new Date(date.getFullYear(), date.getMonth(), date.getDate()).getTime();
    return {
        dateStr: `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, '0')}-${String(date.getDate()).padStart(2, '0')}`,
        start,
        end: start + 86_400_000,
    };
}

async function mainChatStreamPages(size: number) {
    const main = await GetMainChat();
    const id = main?.session?.localId;
    if (id) await LoadStreamItemsPage(id, -1, size);
}

// The self card's detail and voice fold open by default; warm them with the
// lists so Who you know completes from cache.
async function prefetchSelfPerson() {
    const principals = await ListPrincipalsWithStats();
    const selfId = (principals ?? []).find((entry) => entry.principal.kind === 'self')?.principal.id;
    if (!selfId) return;
    await GetPersonDetail(selfId);
    await GetVoiceEnrollmentStatus(selfId);
    await ListVoiceSamples(selfId);
}

// Activity (today), keyed exactly like the page's cache facade so a visit
// finds the entries warm. The meetings read mirrors activity-day-view's
// same-day filter over GetRecentMeetingNotes.
function activityTodaySteps(date: Date): PagePrefetchStep[] {
    const { dateStr, start, end } = dayArgs(date);
    return [
        { page: 'activity', name: 'EnsureDailySummary', run: () => pageQueryCache.read('EnsureDailySummary', [dateStr], () => app.EnsureDailySummary(dateStr)) },
        { page: 'activity', name: 'GetActivitiesInRange', run: () => pageQueryCache.read('GetActivitiesInRange', [start, end], () => app.GetActivitiesInRange(start, end)) },
        { page: 'activity', name: 'GetDayThreads', run: () => pageQueryCache.read('GetDayThreads', [start, end], () => app.GetDayThreads(start, end)) },
        { page: 'activity', name: 'GetCronRunsByDateRange', run: () => pageQueryCache.read('GetCronRunsByDateRange', [start, end], () => app.GetCronRunsByDateRange(start, end)) },
        { page: 'activity', name: 'GetKnowledgeEntries', run: () => GetKnowledgeEntries() },
        { page: 'activity', name: 'GetMemoryEntries', run: () => GetMemoryEntries() },
        {
            page: 'activity', name: 'Activity:meetings', run: () => pageQueryCache.read('Activity:meetings', [dateStr], async () => {
                const todayStart = dayArgs(new Date()).start;
                const days = start > todayStart ? 0 : Math.round((todayStart - start) / 86_400_000) + 1;
                const rows = days > 0 ? await app.GetRecentMeetingNotes(days) : [];
                return (rows ?? []).filter((meeting) => meeting.startTime >= start && meeting.startTime < end);
            }),
        },
        { page: 'activity', name: 'GetDayNoticed', run: () => GetDayNoticed(dateStr) },
    ];
}

// Learning switches the Settings overview reads (activity-learning-settings).
const LEARNING_UI_KEYS = ['triage_enable_events', 'triage_enable_memory', 'triage_enable_knowledge', 'triage_enable_sop'];

/** The canonical idle order: Chat, Activity (today), then the four
 * account-menu dialogs (the slowest to complete: About you, How you do
 * things, Who you know, Settings overview — addendum 2026-10-23), then Goals
 * and Search recents. */
export function pagePrefetchSteps(date = new Date()): PagePrefetchStep[] {
    return [
        { page: 'chat', name: 'GetMainChat', run: () => GetMainChat() },
        { page: 'chat', name: 'LoadStreamItemsPage', run: () => mainChatStreamPages(CHAT_FIRST_PAGE_SIZE) },
        ...activityTodaySteps(date),
        { page: 'about-you', name: 'GetAboutYou', run: () => GetAboutYou() },
        { page: 'about-you', name: 'GetMemoryEntries', run: () => GetMemoryEntries() },
        { page: 'about-you', name: 'GetMemoryStatus', run: () => GetMemoryStatus() },
        { page: 'about-you', name: 'GetAboutYouPortrait', run: () => GetAboutYouPortrait() },
        { page: 'know-how', name: 'ListKnowHow', run: () => ListKnowHow() },
        { page: 'know-how', name: 'GetKnowledgeEntries', run: () => GetKnowledgeEntries() },
        { page: 'know-how', name: 'GetKnowHowMatch', run: () => GetKnowHowMatch() },
        { page: 'people', name: 'ListPeopleOverview', run: () => ListPeopleOverview() },
        { page: 'people', name: 'ListPrincipalsWithStats', run: () => ListPrincipalsWithStats() },
        { page: 'people', name: 'GetContactsAccessStatus', run: () => GetContactsAccessStatus() },
        { page: 'people', name: 'GetPersonDetail', run: () => prefetchSelfPerson() },
        { page: 'settings', name: 'GetSettingsOverview', run: () => GetSettingsOverview() },
        { page: 'settings', name: 'GetMobileBriefSchedule', run: () => GetMobileBriefSchedule() },
        { page: 'settings', name: 'GetPlatformPermissions', run: () => GetPlatformPermissions() },
        { page: 'settings', name: 'GetBackgroundListening', run: () => GetBackgroundListening() },
        { page: 'settings', name: 'GetAutomaticMeetingRecordingEnabled', run: () => GetAutomaticMeetingRecordingEnabled() },
        { page: 'settings', name: 'GetTextChannelStatus', run: () => GetTextChannelStatus() },
        { page: 'settings', name: 'GetSMSChannelStatus', run: () => GetSMSChannelStatus() },
        ...LEARNING_UI_KEYS.map((key) => ({ page: 'settings', name: 'LoadUIState', run: () => LoadUIState(key) })),
        { page: 'goals', name: 'ListGoals', run: () => ListGoals() },
        { page: 'search', name: 'GetTodayTopics', run: () => GetTodayTopics() },
        { page: 'search', name: 'ListHandoffs', run: () => ListHandoffs() },
    ];
}

/** Starts a page's primary reads (hover/focus on its navigation row).
 * Fire-and-forget; nothing is cancelled. Fresh cache entries skip the wire. */
export function prefetchPage(page: string) {
    // Today has no idle-prefetch steps (the queue runs after its first
    // paint); its hover warm is the page's own read set.
    if (page === 'today') {
        void prefetchToday().catch(() => undefined);
        return;
    }
    const steps = pagePrefetchSteps().filter((step) => step.page === page);
    void Promise.all(steps.map((step) => step.run().catch(() => undefined))).catch(() => undefined);
}

/** Warms a pinned chat session's first stream page (hover on a session row). */
export function prefetchSessionStream(sessionLocalId: string) {
    void LoadStreamItemsPage(sessionLocalId, -1, CHAT_FIRST_PAGE_SIZE).catch(() => undefined);
}
