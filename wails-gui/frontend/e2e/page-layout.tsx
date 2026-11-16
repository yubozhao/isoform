// One page on fixture data for e2e/page-layout.spec.ts (?page=activity or
// ?page=you):
// production layout and styles, in-memory reads only. It never contacts the
// running desktop, its browser bridge, or its persisted state.
import React from 'react';
import { createRoot } from 'react-dom/client';
import i18n from '@/i18n';
import '@/index.css';
import { ActivityPage } from '@/components/activity-page';
import { YouPage } from '@/components/you/you-page';
import { NewThisWeek, useBeliefReview, type ReviewItem } from '@/components/about-you/new-this-week';

const params = new URLSearchParams(location.search);
const noop = () => {};
const at = (h: number, m = 0) => new Date(2026, 9, 23, h, m).getTime();
const ZERO = { shown: 0, answered: 0, accepted: 0, declined: 0, notMine: 0, useful: 0, notUseful: 0, undone: 0 };
const reads: Record<string, unknown> = {
    GetActivityFeed: [
        { id: 'h-2', kind: 'handoff', startMs: at(9, 59), endMs: at(9, 59), app: 'Codex', title: 'Write the test plan for the Halcyon import, with the edge cases from the call', surface: 'phone', outcome: 'done', doneMs: at(10, 40) },
        { id: 'run-2', kind: 'agent', startMs: at(9, 40), endMs: at(9, 58), activeMs: 1_080_000, app: 'Codex' },
        { id: 'm-3', kind: 'screen', startMs: at(9, 10), endMs: at(9, 38), app: 'Mail', title: 'Replied to Lena about the Halcyon import test plan and the slides for Thursday', detail: 'Drafted the reply, attached the plan, and moved the review to Thursday afternoon after the standup.' },
        { id: 'rec-1', kind: 'watch', startMs: at(8, 20), endMs: at(8, 56), words: 4200, device: 'watch' },
        { id: '', kind: 'closed', startMs: at(7, 4), endMs: at(8, 4) },
        { id: 'm-2', kind: 'screen', startMs: at(6, 30), endMs: at(7, 0), app: 'Visual Studio Code', title: 'Date parser fix', detail: 'Tests for the time-zone edge cases.' },
        { id: 'run-1', kind: 'agent', startMs: at(6, 0), endMs: at(6, 25), activeMs: 1_500_000, app: 'Claude' },
    ],
    GetActivitiesInRange: [], GetWatchRecordings: [], GetDayThreads: { threads: [], gaps: [], agents: { runs: 0, activeMs: 0 } },
    GetJudgmentStats: { days: [{ day: '2026-10-23', total: { ...ZERO, shown: 9, answered: 5, accepted: 4 }, bySource: { prep: { ...ZERO, answered: 3, accepted: 3 }, brief: { ...ZERO, shown: 4, answered: 2, accepted: 1 } } }] },
    GetAnswers: { answers: [
        { id: 'a3', item: 'w3', kind: 'work', title: 'Reply to Lena about the Halcyon import test plan, with the slides for Thursday and the notes from the call', action: 'edit', edited: { before: 'Hi Lena', after: 'Hi Lena, the plan and the slides are attached; the review moves to Thursday afternoon.' }, at: new Date(2026, 9, 23, 9, 40).toISOString(), surface: 'computer' },
        { id: 'a2', item: 'w2', kind: 'work', title: 'Book the venue', action: 'no', reason: 'Already done', at: new Date(2026, 9, 23, 9, 10).toISOString(), surface: 'phone', undone: true },
        { id: 'a1', item: 'belief:x', kind: 'belief', title: 'You keep calls short before 11 AM.', action: 'yes', at: new Date(2026, 9, 22, 17, 0).toISOString(), surface: 'computer' },
    ] },
    GetDayNoticed: [], EnsureDailySummary: { aiSummary: '', hourlyActive: [], appBreakdown: [] },
};
(window as any).go = { main: { App: new Proxy({}, { get: (_, method: string) => async () => reads[method] ?? null }) } };
(window as any).runtime = { EventsOn: () => noop };
const cronJobs = { jobs: [], loading: false, toggleEnabled: noop, deleteJob: noop, runNow: noop, updateJob: noop, loadRuns: noop, loadRunLogs: noop } as never;
await i18n.changeLanguage(params.get('lang') ?? 'en');
document.documentElement.dataset.theme = params.get('theme') ?? 'paper';
const section = (params.get('section') ?? 'yourDay') as never;
const claims: ReviewItem[] = [
    { id: 'c1', text: 'You build the system first, then the product on top of it.', where: 'Seen 6 times · Mail, Zoom', status: 'inferred' },
    { id: 'c2', text: 'You keep calls short before 11 AM, and move longer reviews to the afternoon when the team is in the same time zone.', where: 'Seen 3 times · Calendar', status: 'learned_today' },
    { id: 'c3', text: 'You write to Jia in Chinese.', where: 'Messages · 12 sent', status: 'inferred', communication: true },
    { id: 'c4', text: 'You release after the tests pass, never on a Friday.', where: 'Seen twice · Terminal', status: 'inferred' },
];
function YouFixture() {
    const review = useBeliefReview({ onConfirmed: noop, onForgotten: noop, onChanged: noop, contentOf: () => '' });
    return (
        <YouPage tab="about" onTabChange={noop}>
            <div className="flex-1 overflow-y-auto px-10 py-6"><NewThisWeek items={claims} review={review} /></div>
        </YouPage>
    );
}
createRoot(document.getElementById('root')!).render(
    <div className="flex h-screen w-screen">
        <aside aria-hidden style={{ width: Number(params.get('sidebar') ?? 240), flexShrink: 0 }} />
        <main data-testid="page-area" className="flex min-w-0 flex-1">
            {params.get('page') === 'you'
                ? <YouFixture />
                : <ActivityPage initialDate="2026-10-23" cronJobs={cronJobs} section={section} onSectionChange={noop} />}
        </main>
    </div>,
);
