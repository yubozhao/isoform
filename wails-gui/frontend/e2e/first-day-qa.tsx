// QA fixture for docs/first-day-qa/: real first-day pages over synthetic
// reads, no app or live store. A fresh install on day 0 with a calendar and
// Claude Code history. URL params: page=today|today-narrow|today-bare|chat|
// about-you|know-how|people|activity|your-data (privacy, the old name, opens it too), theme=paper|onedark, lang.
import React, { useState } from 'react';
import { createRoot } from 'react-dom/client';
import '@/index.css';
import i18n from '@/i18n';
import { ThemeProvider } from '@/hooks/useTheme';
import { TodayPage } from '@/components/today/TodayPage';
import { MemoryPage } from '@/components/memory-page';
import { KnowledgePage } from '@/components/knowledge-page';
import { PeoplePage } from '@/components/people-page';
import { ActivityPage, type ActivitySectionId } from '@/components/activity-page';
import { YourDataPage } from '@/components/settings/pages/your-data-page';
import { Composer } from '@/components/composer';
import { MainChatPageColumn, MainChatStreamBody } from '@/components/main-chat/main-chat-stream-body';
import { useMainChatView } from '@/hooks/useMainChatView';
import { localDayKey } from '@/lib/main-chat-view';
import type { CronJobsProp } from '@/components/timeline/useCalendarData';
import type { TodayActionHandlers } from '@/components/today/types';
import type { StreamItem } from '@/types';

const params = new URLSearchParams(location.search);
const theme = params.get('theme') ?? 'paper';
const page = params.get('page') ?? 'today';
const bare = page === 'today-bare';
const noop = () => {};
localStorage.setItem('yansu.theme', theme);
document.documentElement.dataset.theme = theme;

const now = Date.now();
const MIN = 60_000;
const HOUR = 60 * MIN;
const today = new Date(now);
const at = (h: number, m = 0) => new Date(today.getFullYear(), today.getMonth(), today.getDate(), h, m).getTime();
const iso = (ms: number) => new Date(ms).toISOString();
const dateStr = localDayKey(today);

const project = (id: string, name: string, sessionsThisWeek: number, sessions14d: number, lastAt: number, status: string, extra: Record<string, unknown> = {}) => ({
    id, name, path: `/Users/alex/src/${id}`, assistants: ['claude'], sessionsThisWeek, sessions14d, lastAt, status,
    openProblem: '', openProblemAt: 0, openQuestion: '', openChip: '', ...extra,
});
const history = {
    enabled: true, state: 'ready', assistants: ['claude'], beliefCount: 6, knowHowCount: 3, scannedAt: now - 20 * MIN, pass2Done: true,
    projects: [
        project('isoform', 'isoform', 6, 14, now - 2 * HOUR, 'The onboarding tests fail on the new setup screen.', {
            openProblem: 'onboarding tests failing', openProblemAt: now - 3 * 24 * HOUR,
            openQuestion: 'Why are the onboarding tests failing?', openChip: 'Look at the failing onboarding tests',
        }),
        project('pricing-site', 'pricing-site', 3, 7, now - 26 * HOUR, 'Pricing table moved to per-seat plans.'),
        project('notes-cli', 'notes-cli', 0, 2, now - 6 * 24 * HOUR, ''),
    ],
};
const calendarPeople = [
    { key: 'maya', name: 'Maya Chen', email: 'maya@example.com', principalId: 'maya', nextMeetingTitle: 'Design sync', nextMeetingAt: at(14, 30), recurring: true, meetingsThisWeek: 2 },
    { key: 'joon', name: 'Joon Park', email: 'joon@example.com', principalId: 'joon', nextMeetingTitle: 'Halcyon check-in', nextMeetingAt: at(16), recurring: false, meetingsThisWeek: 1 },
    { key: 'priya', name: 'Priya Raman', email: 'priya@example.com', principalId: '', nextMeetingTitle: 'Planning', nextMeetingAt: at(10) + 2 * 24 * HOUR, recurring: true, meetingsThisWeek: 1 },
];
const calendar = {
    connected: true, meetingsLeftToday: 2, peopleLeftToday: 3, people: calendarPeople,
    nextMeeting: { title: 'Design sync', start: at(14, 30), attendeeNames: ['Maya Chen'] },
    freeRanges: [{ start: at(15), end: at(16) }, { start: at(16, 30), end: at(18, 30) }],
};
const brief = {
    version: 1, briefId: `${dateStr}-morning`, slot: 'afternoon', localDate: dateStr, asOf: iso(now - 4 * MIN), timezone: Intl.DateTimeFormat().resolvedOptions().timeZone,
    coldStart: true, fromHistory: true, narrativeStatus: 'ok',
    narrative: [
        { text: 'Your afternoon has two meetings left: the design sync with Maya at 2:30 and the Halcyon check-in with Joon at 4:00.', refs: [], topicId: '', topicName: '' },
        { text: 'In Claude Code you spent most of this week on isoform. The last session ended on the failing onboarding tests, so that is likely where you pick up.', refs: [], topicId: '', topicName: '' },
        { text: 'You have an hour free at 3:00 and a longer stretch from 4:30.', refs: [], topicId: '', topicName: '' },
    ],
    cards: [], sections: { moved: [], doing: [], watching: [], meetings: [], people: [], openLoops: [], learned: [], deliverables: [], schedules: [] }, evidence: [],
};
const dayEvent = (id: string, title: string, start: number, end: number) => ({
    id, title, shortName: title, shortestName: title, start: iso(start), end: iso(end), allDay: false, kind: 'meeting', attendees: 2, location: '', joinUrl: '',
});
const dayShape = {
    date: dateStr, now: iso(now), rangeStart: iso(at(8)), rangeEnd: iso(at(19)), calendarConnected: true, hold: null,
    events: [dayEvent('standup', 'Standup', at(9, 30), at(10)), dayEvent('design', 'Design sync', at(14, 30), at(15)), dayEvent('halcyon', 'Halcyon check-in', at(16), at(16, 30))],
};
const belief = (i: number, title: string, confidence: number) => ({
    id: `h-${i}`, title, status: 'inferred', confidence, source: 'assistant-history', sourceAssistant: 'claude', sourceAt: now - (i + 1) * 24 * HOUR,
});
const beliefs = [
    belief(0, 'Writes the tests before the fix', 3),
    belief(1, 'Keeps pull requests small, one change each', 3),
    belief(2, 'Prefers Go for backend services', 2),
    belief(3, 'Reviews the diff before committing', 2),
    belief(4, 'Works on isoform most mornings', 1),
    belief(5, 'Asks for plain-language commit messages', 1),
];
const knowHow = (id: string, title: string, kind: string, takeaway: string, source: string, sourceAt: number) => ({
    id, title, kind, takeaway, points: [], apps: [], sessionCount: 1, savedAt: sourceAt, reuseCount: 0, topicId: '', topicName: '', source, sourceAssistant: 'claude', sourceAt,
});
const reads: Record<string, unknown> = {
    GetThemePreference: theme, GetTelemetryTier: 'detailed', LoadUIState: '',
    GetFirstWeekDay: 0, GetAssistantHistory: bare ? { ...history, enabled: false, state: 'off', projects: [] } : history,
    GetCalendarFirstDay: bare ? { connected: false, meetingsLeftToday: 0, peopleLeftToday: 0, people: [], nextMeeting: null, freeRanges: [] } : calendar,
    GetWatchedToday: { startedAt: now - 25 * MIN, minutes: 25, screenRecording: true, paused: false },
    GetReadAssistantHistory: true,
    GetLatestBrief: { brief: bare ? { ...brief, fromHistory: false, narrative: [brief.narrative[0]] } : brief, building: false, enabled: true, nextAt: iso(at(18)), nextSlot: 'evening', empty: false, missing: [] },
    GetTodayDayShape: bare ? null : dayShape,
    GetMobileBriefHealth: { enabled: true, lastRunAt: iso(now - 4 * MIN), nextRunAt: iso(at(18)), missedRuns: 0 },
    GetCaptureCounts: { paused: false, notReady: false, sources: [{ source: 'screens', today: 14 }] },
    GetEngagementWeekSummary: { available: false, sent: 0, actedOn: 0, days: 7, kinds: [] },
    GetTodayNews: { date: dateStr, status: 'ready', items: [], updatedAt: now }, GetTodayNewsHistory: [],
    GetTodaySuggestion: { available: false }, GetGoalCheckIn: null, GetTopicFinds: { status: 'off', items: [], topics: [] },
    GetTodayTopics: [], GetTodayQuietLines: [], IsClosedEyesModeEnabled: false,
    GetMainChat: { session: { localId: 'main' } }, LoadStreamItemsPage: { items: [], hasMore: false },
    ListMainChatAnswerChips: {}, GetMainChatOverview: { conversations: 0, waiting: 0, finished: 0, routine: 0 },
    ListMainChatWaitingItems: { items: [], hasMore: false, nextCursor: '' },
    GetDoingNow: { kind: 'working', app: 'Cursor', title: 'onboarding.test.ts', frontApp: 'Cursor', frontAppHidden: false },
    GetActivitiesInRange: [], GetDayThreads: { grouped: true, threads: [] },
    EnsureDailySummary: { autoStatus: 'none', date: dateStr, summary: null },
    GetCronRunsByDateRange: [], GetKnowledgeEntries: [], GetRecentMeetingNotes: [], GetDayNoticed: { items: [] },
    GetMemoryEntries: beliefs.map((b) => ({ id: b.id, title: b.title, content: `# ${b.title}`, dimension: 'user_insight', source: 'assistant-history', modTime: now, updatedAt: now, sizeBytes: 20, apps: [], activitySessionIds: [] })),
    GetAboutYou: { beliefs, newThisWeek: [], totals: { beliefs: beliefs.length, confirmed: 0 }, storage: { files: 1, bytes: 20, chunks: 1, vectorReady: false } },
    GetAboutYouPortrait: { state: 'none', segments: [] }, GetMemoryStatus: {},
    ListGoals: { tracking: [], done: [], held: [] },
    ListKnowHow: [
        knowHow('kh-tests', 'Run the onboarding tests with the fake clock', 'fix', 'The setup screens time out without vi.useFakeTimers.', 'assistant-history', now - 3 * 24 * HOUR),
        knowHow('kh-release', 'Release builds go through build-wails.sh', 'reference', 'Never raw wails build; the script signs and embeds the bundles.', 'claude-md', now - 5 * 24 * HOUR),
        knowHow('kh-seats', 'Price per seat, not per project', 'decision', 'Seats track value better than projects.', 'assistant-history', now - 2 * 24 * HOUR),
    ],
    GetKnowHowMatch: null, ListSOPs: [], GetPlaybooksState: { plans: [], channels: { desktop: true }, defaultChannels: ['desktop'] },
    ListPeopleOverview: {
        headline: { repliesOwed: 0, thingsOwed: 0, peopleWaitingOnYou: 0 }, knownCount: 2, duplicates: [], open: [],
        everyone: [
            { id: 'maya', kind: 'contact', name: 'Maya Chen', relationship: '', photo: '', loops: [], lastSeenAt: 0, lastSeenApp: 'calendar' },
            { id: 'joon', kind: 'contact', name: 'Joon Park', relationship: '', photo: '', loops: [], lastSeenAt: 0, lastSeenApp: 'calendar' },
        ],
    },
    ListPrincipalsWithStats: [], GetContactsAccessStatus: { status: 'not_requested' },
    GetPlatformPermissions: { platform: 'darwin', items: [], supported: true },
    GetBackgroundListening: false, GetActivityRetentionDays: 30, GetActivityMaxStorageGB: 0,
    GetActivityStorageStats: { totalBytes: 0 }, GetSensitiveApps: [], GetVoiceInputApps: [], ListSystemApps: [],
    GetAskBeforeRiskyCommands: false, IsSystemAudioBackgroundListeningSupported: false,
    GetHostBridgePort: 18080, GetHostBridgeMediaToken: 'fixture-token', GetAppIconsForBundleIDs: {},
    GetUserProfile: { name: 'Alex' }, GetProfile: { name: 'Alex' }, ListHandoffs: [], ListTaskSuggestions: [],
};
const fixture = window as unknown as Record<string, any>;
fixture.go = { main: { App: new Proxy({}, { get: (_, method: string) => async () => structuredClone(reads[method] ?? null) }) } };
fixture.runtime = new Proxy({}, { get: () => () => noop });

const handlers: TodayActionHandlers = {
    onStartChat: noop, onRunNow: noop, onOpenMemory: noop, onOpenAutomations: noop, onOpenActivity: noop, onOpenPeople: noop,
    onOpenSettings: noop, onOpenTopic: noop, onOpenCaptureCounts: noop, onOpenBriefConversation: async () => true, onPrefillChat: noop,
};
const cron = { jobs: [], loading: false } as unknown as CronJobsProp;

const welcome: StreamItem[] = [{
    id: 'welcome', kind: 'assistant', rowClass: 'conversation', localDay: dateStr,
    // The welcome itself is Go copy (the engagement catalog); fixture text here.
    content: "Hi Alex. Here's your first brief, made from today's calendar and your Claude Code history.",
}];

function Chat() {
    const [prompt, setPrompt] = useState('');
    const view = useMainChatView({ enabled: true, items: welcome });
    return <MainChatPageColumn enabled>
        <MainChatStreamBody mainChat={view} items={welcome} hasProject streaming={false} wideMode onOpenReportTarget={noop} onPrepare={setPrompt} />
        <Composer prompt={prompt} onPromptChange={setPrompt} onSend={noop} onCancel={noop} onKeyDown={noop}
            model="sonnet" onModelChange={noop} executorGroups={[]} streaming={false} ready hasProject
            images={[]} onImagesChange={noop} files={[]} onFilesChange={noop} executorType="claude"
            thinkingLevel="" onThinkingLevelChange={noop} thinkingEnabled={false} onToggleThinking={noop}
            planMode={false} onTogglePlanMode={noop} queuedMessages={[]} onRemoveQueuedMessage={noop} onEditQueuedMessage={noop}
            availableCommands={[]} showSuggestions={false} wideMode voiceState="idle" placeholder={i18n.t('app:firstDay.placeholder')} />
    </MainChatPageColumn>;
}

function Activity() {
    const [section, setSection] = useState<ActivitySectionId>('workedOn');
    return <ActivityPage section={section} onSectionChange={setSection} cronJobs={cron} />;
}

function Page() {
    switch (page) {
        case 'chat': return <Chat />;
        case 'about-you': return <MemoryPage active />;
        case 'know-how': return <KnowledgePage active onRunPlaybook={async () => {}} />;
        case 'people': return <PeoplePage active />;
        case 'activity': return <Activity />;
        case 'privacy':
        case 'your-data': return <div className="settings-page mx-auto w-full max-w-[720px] overflow-y-auto p-8"><YourDataPage /></div>;
        default: return <TodayPage handlers={handlers} wideMode />;
    }
}

await i18n.changeLanguage(params.get('lang') ?? 'en');
createRoot(document.getElementById('root')!).render(
    <ThemeProvider>
        <div className="flex h-screen w-screen overflow-hidden">
            <aside aria-hidden style={{ width: 232, flexShrink: 0 }} className="border-r border-border bg-sidebar" />
            <main data-testid="page-area" className="patina-ground flex min-w-0 flex-1 flex-col bg-background"><Page /></main>
        </div>
    </ThemeProvider>,
);
