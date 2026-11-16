// One page on fixture data for e2e/page-layout.spec.ts (?page=activity):
// production layout and styles, in-memory reads only. It never contacts the
// running desktop, its browser bridge, or its persisted state.
import React from 'react';
import { createRoot } from 'react-dom/client';
import i18n from '@/i18n';
import '@/index.css';
import { ActivityPage } from '@/components/activity-page';

const params = new URLSearchParams(location.search);
const noop = () => {};
const at = (h: number, m = 0) => new Date(2026, 9, 23, h, m).getTime();
const reads: Record<string, unknown> = {
    GetActivityFeed: [
        { id: 'run-2', kind: 'agent', startMs: at(9, 40), endMs: at(9, 58), activeMs: 1_080_000, app: 'Codex' },
        { id: 'm-3', kind: 'screen', startMs: at(9, 10), endMs: at(9, 38), app: 'Mail', title: 'Replied to Lena about the Halcyon import test plan and the slides for Thursday', detail: 'Drafted the reply, attached the plan, and moved the review to Thursday afternoon after the standup.' },
        { id: 'rec-1', kind: 'watch', startMs: at(8, 20), endMs: at(8, 56), words: 4200, device: 'watch' },
        { id: '', kind: 'closed', startMs: at(7, 4), endMs: at(8, 4) },
        { id: 'm-2', kind: 'screen', startMs: at(6, 30), endMs: at(7, 0), app: 'Visual Studio Code', title: 'Date parser fix', detail: 'Tests for the time-zone edge cases.' },
        { id: 'run-1', kind: 'agent', startMs: at(6, 0), endMs: at(6, 25), activeMs: 1_500_000, app: 'Claude' },
    ],
    GetActivitiesInRange: [], GetWatchRecordings: [], GetDayThreads: { threads: [], gaps: [], agents: { runs: 0, activeMs: 0 } },
    GetDayNoticed: [], EnsureDailySummary: { aiSummary: '', hourlyActive: [], appBreakdown: [] },
};
(window as any).go = { main: { App: new Proxy({}, { get: (_, method: string) => async () => reads[method] ?? null }) } };
(window as any).runtime = { EventsOn: () => noop };
const cronJobs = { jobs: [], loading: false, toggleEnabled: noop, deleteJob: noop, runNow: noop, updateJob: noop, loadRuns: noop, loadRunLogs: noop } as never;
await i18n.changeLanguage(params.get('lang') ?? 'en');
document.documentElement.dataset.theme = params.get('theme') ?? 'paper';
const section = (params.get('section') ?? 'yourDay') as never;
createRoot(document.getElementById('root')!).render(
    <div className="flex h-screen w-screen">
        <aside aria-hidden style={{ width: Number(params.get('sidebar') ?? 240), flexShrink: 0 }} />
        <main data-testid="page-area" className="flex min-w-0 flex-1">
            <ActivityPage initialDate="2026-10-23" cronJobs={cronJobs} section={section} onSectionChange={noop} />
        </main>
    </div>,
);
