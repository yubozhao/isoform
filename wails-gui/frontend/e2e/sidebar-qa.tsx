// QA fixture for docs/sidebar-qa/: the real Sidebar beside a real page over
// synthetic reads, no app or live store. URL params: page=tasks|about-you|
// know-how|people, day=first (the first day), menu=1 (account menu open),
// theme=paper|onedark.
import React, { useEffect } from 'react';
import { createRoot } from 'react-dom/client';
import '@/index.css';
import i18n from '@/i18n';
import { ThemeProvider } from '@/hooks/useTheme';
import { Sidebar } from '@/components/sidebar';
import { TasksPage } from '@/components/tasks/TasksPage';
import { MemoryPage } from '@/components/memory-page';
import { KnowledgePage } from '@/components/knowledge-page';
import { PeoplePage } from '@/components/people-page';
import type { AppMode } from '@/components/sidebar';

const params = new URLSearchParams(location.search);
const theme = params.get('theme') ?? 'paper';
const page = params.get('page') ?? 'tasks';
const firstDay = params.get('day') === 'first';
const noop = () => {};
const now = Date.now();
const HOUR = 3_600_000;
const DAY = 24 * HOUR;
localStorage.setItem('yansu.theme', theme);
document.documentElement.dataset.theme = theme;

const handoff = (id: string, title: string, status: string, body: string, extra: Record<string, unknown> = {}) => ({
    id, title, status, body, source: 'chat', sourceId: '', sourceLink: '', appName: '', workdir: '', keyFiles: null,
    chatSessionId: '', cronJobId: '', createdAt: now - DAY, updatedAt: now - HOUR, ...extra,
});
const upcoming = (id: string, kind: string, slot: string, name: string, summary: string, time: string, nextRunAt: number) => ({
    id, kind, jobId: id, slot, name, summary, enabled: true, scheduleKind: 'daily', scheduleConfig: null, time, nextRunAt,
    lastRunAt: 0, lastResult: '', lastReason: '', failedAttempts: 0, missedRuns: 0, system: kind === 'brief',
});
const briefs = [
    upcoming('morning', 'brief', 'morning', 'Morning brief', '', '08:00', now + 14 * HOUR),
    upcoming('evening', 'brief', 'evening', 'Evening brief', '', '17:00', now + 4 * HOUR),
];
const schedules = firstDay ? briefs : [...briefs, upcoming('invoice', 'schedule', '', 'Friday invoice run', 'Same five steps, ready for you to send', '16:00', now + 2 * DAY)];
const handoffs = firstDay ? [] : [
    handoff('priya', 'Reply to Priya about Thursday', 'needs_review', 'The draft says no and offers Friday. Nothing is sent until you send it.'),
    handoff('halcyon', 'Send the Halcyon deck to Joon', 'needs_review', 'Version 3 is ready, with your changes from this morning.'),
    handoff('pricing', 'Pricing page copy', 'in_progress', '', { runner: 'claude_code', progress: { done: 2, total: 4, label: 'Writing section 2' } }),
    handoff('notes', 'Clean up the onboarding notes', 'completed', '', { updatedAt: now - 3 * HOUR }),
];
const offers = firstDay ? [] : [
    { id: 'weekly', kind: 'suggestion', title: "Turn Monday's notes into the weekly update", why: "You've written it every Monday for a month, about 40 minutes each time.", prompt: 'Draft the weekly update', version: '1' },
];
const sop = (id: string, title: string, description: string, status: string, source: string) => ({
    id, title, description, trigger: '', status, source, sourceIds: [], version: 1, createdAt: now - 30 * DAY, updatedAt: now - DAY, scripts: [], sites: [], body: '# Steps\n\nRead the notes.',
});
const sops = firstDay ? [] : [
    sop('weekly-update', 'Weekly update', 'Monday notes into the update, in your words.', 'confirmed', 'manual'),
    sop('invoice', 'Friday invoice run', 'Same five steps, three Fridays in a row. Isoform would do it at 4 PM and show you before sending.', 'draft', 'observed'),
];
const plan = (id: string, proposal: unknown) => ({ id, proposal, schedule: null, channels: ['desktop'], sites: [], lastRun: null, nextRunAt: 0, suggestedTime: '', held: '' });
const playbooks = {
    plans: firstDay ? [] : [plan('weekly-update', null), plan('invoice', { host: '', siteName: '', time: '16:00', days: 3, weekdays: false, titles: [], proposedAt: now, answer: '', answeredAt: 0 })],
    channels: { desktop: true, push: false, text: false, email: false, textNames: false }, defaultChannels: ['desktop'],
};
const belief = (i: number, title: string) => ({ id: `belief-${i}`, title, content: `# ${title}`, dimension: 'user_insight', source: 'activity', modTime: now, updatedAt: now, sizeBytes: 20, apps: [], activitySessionIds: [] });
const beliefs = firstDay ? [] : [belief(0, 'Plans the whole thing, then builds the first version'), belief(1, 'Writes the weekly update on Mondays'), belief(2, 'Prefers short replies')];
const goal = (id: string, title: string, extra: Record<string, unknown> = {}) => ({ id, eventId: id, kind: 'goal', title, state: 'active', group: 'tracking', slipping: false, addedAt: new Date(now - 3 * DAY).toISOString(), updatedAt: new Date(now - DAY).toISOString(), lastEvent: 'discovered', origin: 'chat', ...extra });
const reads: Record<string, unknown> = {
    GetThemePreference: theme, GetTelemetryTier: 'detailed', LoadUIState: '',
    ListHandoffs: handoffs, ListTaskSuggestions: offers, GetFirstWeekDay: firstDay ? 0 : 5,
    ListDeliverables: firstDay ? [] : [{ id: 'd1', source: 'handoff', sourceId: 'notes', title: 'Clean up the onboarding notes', summary: '', status: 'completed', files: [{ path: '/Users/example/notes.md', name: 'notes.md' }], createdAt: now - 3 * HOUR, updatedAt: now - 3 * HOUR }],
    ListOpenAnswers: firstDay ? [] : [{ id: 'a1', sessionId: 's', messageId: 'm', question: 'Tell you when Priya replies', kind: 'reply', settles: 'result', delivery: 'around', windowStart: new Date(now - 2 * DAY).toISOString(), expiresAt: new Date(now + 5 * DAY).toISOString(), posts: 0, state: 'open', createdAt: new Date(now - 2 * DAY).toISOString() }],
    ListSOPs: sops, GetPlaybooksState: playbooks, GetSOPAuthorStatus: null,
    GetMemoryEntries: beliefs,
    GetAboutYou: { beliefs: beliefs.map((b) => ({ id: b.id, title: b.title, status: 'inferred', confidence: 2 })), newThisWeek: [], totals: { beliefs: beliefs.length, confirmed: 0 }, storage: { files: 1, bytes: 20, chunks: 1, vectorReady: false } },
    GetAboutYouPortrait: { state: 'none', segments: [] }, GetMemoryStatus: {},
    ListGoals: firstDay ? { tracking: [], done: [], held: [] } : { tracking: [goal('halcyon', 'Close the Halcyon raise'), goal('passport', 'Renew passport', { state: 'waiting', waitingOn: 'Ana' })], done: [], held: [] },
    ListKnowHow: firstDay ? [] : [
        { id: 'kh-pricing', title: 'Price the Pro plan per seat, not per project', kind: 'decision', takeaway: 'Seats track value better than projects.', points: [], apps: [], sessionCount: 3, savedAt: now - 2 * DAY, reuseCount: 2, topicId: '', topicName: '' },
        { id: 'kh-dmg', title: 'Notarization fails when the DMG is still mounted', kind: 'fix', takeaway: 'Eject the DMG before notarizing.', points: [], apps: [], sessionCount: 1, savedAt: now - 4 * DAY, reuseCount: 1, topicId: '', topicName: '' },
    ],
    GetKnowledgeEntries: [], GetKnowHowMatch: null,
    ListPeopleOverview: firstDay ? { open: [], everyone: [], duplicates: [], headline: { repliesOwed: 0, thingsOwed: 0, peopleWaitingOnYou: 0 } }
        : {
            headline: { repliesOwed: 1, thingsOwed: 0, peopleWaitingOnYou: 1 }, knownCount: 2, duplicates: [],
            open: [{ id: 'priya', kind: 'contact', name: 'Priya Raman', relationship: 'colleague', photo: '', lastSeenAt: now - HOUR, lastSeenApp: 'slack',
                loops: [{ id: 'l1', kind: 'reply_owed', title: 'Thursday', since: now - DAY, dueAt: 0 }] }],
            everyone: [{ id: 'joon', kind: 'contact', name: 'Joon Park', relationship: '', photo: '', loops: [], lastSeenAt: now - 3 * DAY, lastSeenApp: 'email' }],
        },
    GetPersonDetail: { id: 'priya', name: 'Priya Raman', relationship: 'colleague', notes: '', researchSummary: '', loops: [], together: [], sharedTopics: [], seenIn: [], messageCount: 12 },
    ListPrincipalsWithStats: [], GetContactsAccessStatus: { status: 'not_requested' },
    GetHostBridgePort: 18080, GetHostBridgeMediaToken: 'fixture-token', GetAppIconsForBundleIDs: {},
    GetUserProfile: { name: 'Bo' }, GetProfile: { name: 'Bo' },
};
const fixture = window as unknown as Record<string, any>;
fixture.go = { main: { App: new Proxy({}, { get: (_, method: string) => async () => {
    const value = reads[method];
    return value === undefined ? null : value;
} }) } };
fixture.runtime = new Proxy({}, { get: () => () => noop });

const modes: Record<string, AppMode> = { tasks: 'tasks', 'about-you': 'memory', 'know-how': 'knowledge', people: 'people' };

function OpenMenu() {
    useEffect(() => {
        if (!params.has('menu')) return;
        const timer = setTimeout(() => {
            const trigger = document.querySelector<HTMLElement>('[aria-label$="account menu"]');
            trigger?.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true, button: 0 }));
            trigger?.click();
        }, 300);
        return () => clearTimeout(timer);
    }, []);
    return null;
}

function Fixture() {
    const mode = modes[page] ?? 'tasks';
    return (
        <div className="h-screen flex flex-row overflow-hidden">
            <Sidebar
                projects={[]} activeProjectId={null} collapsed={false} width={232}
                onToggleCollapse={noop} onOpenSearch={noop} onOpenSettings={noop}
                waitingCount={firstDay ? 0 : 2}
                onOpenActivity={noop} onOpenKnowledge={noop} onOpenMemory={noop} onOpenPeople={noop}
                onOpenToday={noop} onOpenTasks={noop} tasksOpen={mode === 'tasks'} appMode={mode}
                onOpenMainChat={noop} onSwitchSession={noop} onCloseSession={noop} onTogglePin={noop}
                onRenameSession={noop} onMarkSessionRead={noop} onMarkSessionUnread={noop} onForkSession={noop}
                pinnedTopics={firstDay ? [] : [{ id: 'halcyon', name: 'Halcyon raise' }]} onOpenTopic={noop}
            />
            <main className="patina-ground flex-1 min-w-0 flex flex-col h-full bg-background">
                <div className="h-[44px] shrink-0" />
                {mode === 'tasks' && (
                    <TasksPage onOpenDraft={async () => {}} onOpenChat={noop} onStartHandoff={async () => true} onStartInPlace={async () => true}
                        upcoming={{ items: schedules as never, loading: false }} />
                )}
                {mode === 'memory' && <MemoryPage active />}
                {mode === 'knowledge' && <KnowledgePage active onRunPlaybook={async () => {}} />}
                {mode === 'people' && <PeoplePage active />}
            </main>
            <OpenMenu />
        </div>
    );
}

await i18n.changeLanguage(params.get('lang') ?? 'en');
createRoot(document.getElementById('root')!).render(<ThemeProvider><Fixture /></ThemeProvider>);
