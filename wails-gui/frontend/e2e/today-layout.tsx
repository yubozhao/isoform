// Today on fixture data for e2e/today-layout.spec.ts: production layout and
// styles, a full day strip, twelve topics. In-memory reads only; it never
// contacts the running desktop, its browser bridge, or its persisted state.
import React from 'react';
import { createRoot } from 'react-dom/client';
import i18n from '@/i18n';
import '@/index.css';
import { todayNextBrief, dayShapeFixture } from '@/components/today/today-next.fixture';
import { TodayPage } from '@/components/today/TodayPage';
import type { TodayActionHandlers } from '@/components/today/types';

const params = new URLSearchParams(location.search);
const noop = () => {};
const reads: Record<string, unknown> = {
    GetLatestBrief: { brief: todayNextBrief, building: false, enabled: true, nextAt: '', nextSlot: '', empty: false, missing: [] },
    GetTodayDayShape: dayShapeFixture,
    GetMobileBriefHealth: { enabled: true, lastRunAt: todayNextBrief.asOf, nextRunAt: '2026-10-23T18:15:00-07:00', missedRuns: 0 },
    GetCaptureCounts: { sources: [{ source: 'screens', today: 110 }, { source: 'audioMinutes', today: 2 }] },
    GetTodayNews: { date: '', status: 'ready', items: [], updatedAt: 0 }, GetTodayNewsHistory: [],
    GetTodaySuggestion: { available: false }, GetGoalCheckIn: null, GetTopicFinds: { status: 'off', items: [], topics: [] },
    GetTodayTopics: Array.from({ length: 12 }, (_, i) => ({ id: `topic-${i}`, name: `Topic ${i}`, kind: 'work', status: i === 8 ? 'waiting' : 'moving', latest: { text: 'Updated', at: new Date(2026, 9, i + 1).toISOString() }, pinnedToSidebar: false })),
    GetTodayQuietLines: [], IsClosedEyesModeEnabled: false,
    GetMainChat: { session: { localId: 'main' } }, LoadStreamItemsPage: { items: [], hasMore: false },
    ListMainChatAnswerChips: {}, GetMainChatOverview: { conversations: 1, waiting: 2, finished: 3, routine: 4 },
};
(window as any).go = { main: { App: new Proxy({}, { get: (_, method: string) => async () => reads[method] ?? null }) } };
(window as any).runtime = { EventsOn: () => noop };
const handlers: TodayActionHandlers = {
    onStartChat: noop, onRunNow: noop, onOpenMemory: noop, onOpenAutomations: noop, onOpenActivity: noop, onOpenPeople: noop, onOpenSettings: noop, onOpenTopic: noop, onOpenCaptureCounts: noop, onOpenBriefConversation: async () => true,
};
await i18n.changeLanguage(params.get('lang') ?? 'en');
document.documentElement.dataset.theme = params.get('theme') ?? 'paper';
createRoot(document.getElementById('root')!).render(
    <div className="flex h-screen w-screen">
        <aside aria-hidden style={{ width: Number(params.get('sidebar') ?? 240), flexShrink: 0 }} />
        <main data-testid="page-area" className="flex min-w-0 flex-1"><TodayPage handlers={handlers} wideMode /></main>
    </div>,
);
