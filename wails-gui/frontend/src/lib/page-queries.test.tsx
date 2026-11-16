import { act, cleanup, renderHook, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { useTodayTopics } from '@/hooks/useTodayTopics';
import { useMainChatPanel } from '@/hooks/useMainChatPanel';
import { useMainChatAnswerChips, useMainChatView } from '@/hooks/useMainChatView';
import { useOpenAnswers } from '@/hooks/useOpenAnswers';
import { useTodayNews } from '@/hooks/useTodayNews';
import { useTodayStatus } from '@/hooks/useTodayStatus';
import { useTodayQuietLines } from '@/hooks/useTodayQuietLines';
import { BRIEF_RESYNC_MS, useLatestBrief } from '@/hooks/useLatestBrief';
import { useTopicFinds } from '@/hooks/useTopicFinds';
import { useGoalCheckIn } from '@/hooks/useGoalCheckIn';
import { useCaptureCounts } from '@/hooks/useCaptureCounts';
import { useTodayDayShape } from '@/hooks/useTodayDayShape';
import { dayShapeFixture } from '@/components/today/today-next.fixture';
import * as reads from './page-queries';
import { pageQueryCache } from './query-cache';
import { resetMainChatWaitingStore } from './main-chat-waiting-store';
import type { StreamItem } from '@/types';

vi.mock('@/lib/launch-perf', () => ({ markLaunchStage: vi.fn(), markLaunchPaint: vi.fn() }));

const fixture = vi.hoisted(() => ({ bindings: new Map<string, ReturnType<typeof vi.fn>>(), events: new Map<string, Set<(...args: any[]) => unknown>>() }));
vi.mock('@/lib/wails-shim-app', () => new Proxy({}, {
    has: () => true,
    get: (_target, name: string) => {
        if (name === 'then') return undefined;
        if (!fixture.bindings.has(name)) fixture.bindings.set(name, vi.fn().mockResolvedValue({}));
        return fixture.bindings.get(name);
    },
}));
vi.mock('../../wailsjs/runtime/runtime', () => ({ EventsOn: (name: string, handler: (...args: any[]) => unknown) => {
    const set = fixture.events.get(name) ?? new Set();
    set.add(handler); fixture.events.set(name, set);
    return () => set.delete(handler);
} }));
const binding = (name: string) => fixture.bindings.get(name)!;
const topics = [{ id: 'one', kind: 'project', name: 'Fixture project' }];
const page = { items: [{ id: 'answer', kind: 'assistant', content: 'Fixture answer', createdAt: 1 }], hasMore: false, nextCursor: 0 };
async function emit(name: string, data?: unknown) {
    await act(async () => { await Promise.all([...(fixture.events.get(name) ?? [])].map((handler) => handler(data))); });
}
beforeEach(() => {
    reads.resetPageQueries();
    resetMainChatWaitingStore();
    fixture.bindings.clear();
    fixture.events.clear();
    fixture.bindings.set('GetMainChatWaiting', vi.fn().mockResolvedValue({
        sessionId: 'main', count: 0, items: [], revision: 1, ready: true,
    }));
    // Resolve getters once so the fixture keeps stable spies like the shim.
    for (const name of ['GetTodayTopics', 'GetMainChat', 'LoadStreamItemsPage', 'GetMainChatOverview', 'ListMainChatWaitingItems', 'ListMainChatAnswerChips']) {
        fixture.bindings.set(name, vi.fn());
    }
    binding('GetTodayTopics').mockResolvedValue(topics);
    binding('GetMainChat').mockResolvedValue({ session: { localId: 'main' } });
    binding('LoadStreamItemsPage').mockResolvedValue(page);
    binding('GetMainChatOverview').mockResolvedValue({ conversations: 1, waiting: 0, finished: 0, routine: 0 });
    binding('ListMainChatWaitingItems').mockResolvedValue({ items: [], hasMore: false, nextCursor: '' });
    binding('ListMainChatAnswerChips').mockResolvedValue({ answer: ['Prepared request'] });
});
afterEach(() => { cleanup(); reads.resetPageQueries(); resetMainChatWaitingStore(); vi.useRealTimers(); vi.restoreAllMocks(); });

function useTodayReads() {
    const status = useTodayStatus(true);
    const quiet = useTodayQuietLines(true);
    const brief = useLatestBrief(true);
    const finds = useTopicFinds(true);
    const goal = useGoalCheckIn(true);
    // Snappy 1.0: Today no longer reads the capture counts (Activity does).
    const topics = useTodayTopics(true);
    const day = useTodayDayShape(true);
    return { status, quiet, brief, finds, goal, topics, day };
}
function seedToday() {
    for (const [name, value] of Object.entries({
        GetTodayNews: { date: '2026-10-22', status: 'ready', items: [], updatedAt: 1 },
        GetEngagementWeekSummary: { available: true, sent: 1, actedOn: 1 },
        GetMobileBriefHealth: { enabled: true }, GetTodaySuggestion: { available: false },
        GetTodayQuietLines: [], GetLatestBrief: { brief: null, building: false },
        GetTopicFinds: { items: [], status: 'off' }, GetGoalCheckIn: null,
        GetCaptureCounts: { sources: [], notReady: false, paused: false },
        GetTodayDayShape: dayShapeFixture,
    })) fixture.bindings.set(name, vi.fn().mockResolvedValue(value));
}

describe('cached page hooks through shim spies', () => {
    it('defers the six reported bindings while hidden and reads each once on return', async () => {
        seedToday();
        for (const name of ['GetMemoryEntries', 'GetAboutYouPortrait', 'GetKnowledgeEntries']) fixture.bindings.set(name, vi.fn().mockResolvedValue([]));
        const names = ['GetMemoryEntries', 'GetAboutYouPortrait', 'GetKnowledgeEntries', 'GetMobileBriefHealth', 'GetLatestBrief', 'GetTodayQuietLines'] as const;
        const visibility = vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('hidden');
        const pending = names.map((name) => reads[name]());
        await act(async () => { await Promise.resolve(); });
        for (const name of names) expect(binding(name), name).not.toHaveBeenCalled();
        visibility.mockReturnValue('visible');
        await act(async () => { document.dispatchEvent(new Event('visibilitychange')); await Promise.all(pending); });
        for (const name of names) expect(binding(name), name).toHaveBeenCalledTimes(1);
    });
    it('on show re-reads only what is past its budget, never a cached read still inside it', async () => {
        seedToday();
        await reads.GetTodayQuietLines();
        binding('GetTodayQuietLines').mockClear();
        const visibility = vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('hidden');
        await act(async () => document.dispatchEvent(new Event('visibilitychange')));
        visibility.mockReturnValue('visible');
        await act(async () => document.dispatchEvent(new Event('visibilitychange')));
        await reads.GetTodayQuietLines();
        expect(binding('GetTodayQuietLines')).not.toHaveBeenCalled();
        // Past its 60-second budget, the same show does read it once.
        vi.spyOn(Date, 'now').mockReturnValue(Date.now() + 61_000);
        visibility.mockReturnValue('hidden');
        await act(async () => document.dispatchEvent(new Event('visibilitychange')));
        visibility.mockReturnValue('visible');
        await act(async () => document.dispatchEvent(new Event('visibilitychange')));
        await act(async () => { await reads.GetTodayQuietLines(); });
        expect(binding('GetTodayQuietLines')).toHaveBeenCalledTimes(1);
    });
    // Snappy 1.0: no 2-second poll. A missed publication is recovered by the
    // 10-minute safety resync, in one shared read, and never sooner.
    it('recovers two first-run bootstrap readers without an event in one shared read', async () => {
        vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval', 'Date'] });
        binding('GetTodayTopics').mockResolvedValue({ topics: [], refreshing: true, firstRun: true });
        const readers = renderHook(() => [useTodayTopics(true), useTodayTopics(true)]);
        await act(async () => {});
        expect(readers.result.current.every((reader) => reader.refreshing)).toBe(true);
        expect(readers.result.current.every((reader) => reader.firstRun)).toBe(true);
        expect(binding('GetTodayTopics')).toHaveBeenCalledTimes(1);
        const published = { topics, refreshing: false, firstRun: false };
        binding('GetTodayTopics').mockResolvedValue(published);
        await act(async () => { await vi.advanceTimersByTimeAsync(60_000); });
        expect(binding('GetTodayTopics')).toHaveBeenCalledTimes(1);
        await act(async () => { await vi.advanceTimersByTimeAsync(reads.TODAY_TOPICS_PENDING_RESYNC_MS); });
        expect(readers.result.current.every((reader) => reader.topics.length === 1 && !reader.refreshing && !reader.firstRun)).toBe(true);
        expect(binding('GetTodayTopics')).toHaveBeenCalledTimes(2);
        expect(pageQueryCache.peek('GetTodayTopics')).toEqual(published);
        expect(pageQueryCache.state('GetTodayTopics')?.ttl).toBe(reads.TODAY_TOPICS_TTL_MS);
        readers.unmount();
        const later = renderHook(() => useTodayTopics(true));
        expect(later.result.current.topics[0].name).toBe('Fixture project');
        await act(async () => {});
        expect(binding('GetTodayTopics')).toHaveBeenCalledTimes(2);
    }, 10_000);

    it.each([false, true])('mounts all Today reads from cache; stale=%s revalidates each binding once', async (stale) => {
        seedToday();
        await reads.prefetchToday();
        for (const mock of fixture.bindings.values()) mock.mockClear();
        // Past every Today budget, the longest being the topics' 2 hours.
        if (stale) vi.spyOn(Date, 'now').mockReturnValue(Date.now() + reads.TODAY_TOPICS_TTL_MS + 1);
        const hook = renderHook(useTodayReads);
        expect(hook.result.current.status.loading).toBe(false);
        expect(hook.result.current.brief.loading).toBe(false);
        expect(hook.result.current.topics.topics[0].name).toBe('Fixture project');
        expect(hook.result.current.day.shape).toEqual(dayShapeFixture);
        await act(async () => {});
        for (const [name, mock] of fixture.bindings) {
            // Chat's reads, and the capture counts (Activity's since 1.0).
            const chatOnly = ['GetMainChat', 'LoadStreamItemsPage', 'GetMainChatOverview', 'ListMainChatWaitingItems', 'ListMainChatAnswerChips', 'GetMainChatWaiting', 'GetCaptureCounts', 'GetTodayNews', 'GetTodaySuggestion'].includes(name);
            expect(mock, name).toHaveBeenCalledTimes(stale && !chatOnly ? 1 : 0);
        }
    });

    it('does not prefetch retired news or suggestions', async () => {
        seedToday();
        await reads.prefetchToday();
        expect(binding('GetTodayNews')).not.toHaveBeenCalled();
        expect(binding('GetTodaySuggestion')).not.toHaveBeenCalled();
    });

    it('preloads the evening brief\'s own day, the rest of today (today-review v1.1.20 R44)', async () => {
        vi.useFakeTimers({ toFake: ['Date'] });
        vi.setSystemTime(new Date(2026, 9, 23, 9));
        seedToday();
        binding('GetLatestBrief').mockResolvedValue({ brief: { localDate: '2026-10-23', slot: 'evening', asOf: '2026-10-23T18:00:00-07:00' } });
        await reads.prefetchToday();
        expect(binding('GetTodayDayShape')).toHaveBeenCalledWith('2026-10-23');
        expect(pageQueryCache.peek('GetTodayDayShape', ['2026-10-23'])).toEqual(dayShapeFixture);
    });

    // Snappy 1.0: focus answers from what Today has. Topics, the quiet lines,
    // the brief and quiet lines stay cached; the day strip changes on its
    // backend event or when the window is shown again.
    it('keeps event-driven reads on focus and resyncs topics past their budget', async () => {
        seedToday();
        await reads.prefetchToday();
        renderHook(useTodayReads);
        await act(async () => {});
        for (const mock of fixture.bindings.values()) mock.mockClear();
        await act(async () => window.dispatchEvent(new Event('focus')));
        await waitFor(() => expect(binding('GetGoalCheckIn')).toHaveBeenCalledTimes(1));
        expect(binding('GetEngagementWeekSummary')).not.toHaveBeenCalled();
        // Brief health is cached-first: focus inside its minute reads nothing.
        const budgeted = ['GetMobileBriefHealth', 'GetTodayTopics', 'GetTodayQuietLines', 'GetLatestBrief', 'GetTodayDayShape'];
        for (const name of budgeted) expect(binding(name), name).not.toHaveBeenCalled();
        vi.spyOn(Date, 'now').mockReturnValue(Date.now() + reads.TODAY_TOPICS_TTL_MS + 1);
        await act(async () => window.dispatchEvent(new Event('focus')));
        await waitFor(() => expect(binding('GetTodayTopics')).toHaveBeenCalledTimes(1));
        expect(binding('GetTodayDayShape')).not.toHaveBeenCalled();
        for (const name of ['GetMobileBriefHealth', 'GetTodayQuietLines', 'GetLatestBrief']) expect(binding(name), name).not.toHaveBeenCalled();
        // Snappy 1.0: Today's click path never reads the capture counts.
        expect(binding('GetCaptureCounts')).not.toHaveBeenCalled();
    });

    it('reads the brief again on show only once it is past its budget, never while hidden', async () => {
        seedToday();
        await reads.GetLatestBrief();
        renderHook(() => useLatestBrief(true));
        await act(async () => {});
        binding('GetLatestBrief').mockClear();
        vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('hidden');
        await act(async () => document.dispatchEvent(new Event('visibilitychange')));
        expect(binding('GetLatestBrief')).not.toHaveBeenCalled();
        vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible');
        await act(async () => document.dispatchEvent(new Event('visibilitychange')));
        expect(binding('GetLatestBrief')).not.toHaveBeenCalled();
        vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('hidden');
        await act(async () => document.dispatchEvent(new Event('visibilitychange')));
        vi.spyOn(Date, 'now').mockReturnValue(Date.now() + BRIEF_RESYNC_MS + 1);
        vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible');
        await act(async () => document.dispatchEvent(new Event('visibilitychange')));
        expect(binding('GetLatestBrief')).toHaveBeenCalledTimes(1);
    });

    it('refreshes the stale panel on focus without re-reading the shared waiting count', async () => {
        // This test checks focus, not the separate midnight refresh. Keep its
        // 61-second advance on the same local day even when the suite runs late.
        vi.useFakeTimers({ toFake: ['Date'] });
        vi.setSystemTime(new Date(2026, 9, 30, 12));
        await reads.prefetchMainChat();
        const items = [page.items[0]] as unknown as StreamItem[];
        renderHook(() => ({ panel: useMainChatPanel(true), view: useMainChatView({ enabled: true, items }) }));
        await act(async () => {});
        for (const mock of fixture.bindings.values()) mock.mockClear();
        await act(async () => window.dispatchEvent(new Event('focus')));
        for (const name of ['GetMainChatOverview', 'ListMainChatWaitingItems']) expect(binding(name), name).not.toHaveBeenCalled();
        vi.setSystemTime(Date.now() + 61_000);
        await act(async () => window.dispatchEvent(new Event('focus')));
        expect(binding('LoadStreamItemsPage')).toHaveBeenCalled();
        for (const name of ['GetMainChatOverview', 'ListMainChatWaitingItems']) expect(binding(name), name).not.toHaveBeenCalled();
    });

    it('withdraws all cached Today derivatives on Forget and publishes the new authoritative reads', async () => {
        seedToday();
        await reads.prefetchToday();
        const hook = renderHook(useTodayReads);
        await act(async () => {});
        const derivatives = ['GetTodayTopics', 'GetTopicFinds', 'GetGoalCheckIn'];
        let release!: () => void;
        const held = new Promise<void>((resolve) => { release = resolve; });
        for (const name of derivatives) {
            const value = name === 'GetTodayTopics' ? [] : name === 'GetGoalCheckIn' ? null : name === 'GetTodaySuggestion' ? { available: false } : name === 'GetTodayNews' ? { status: 'ready', items: [] } : { status: 'off', items: [] };
            binding(name).mockClear().mockImplementation(async () => { await held; return value; });
        }
        await emit('memory:updated');
        for (const name of derivatives) {
            expect(pageQueryCache.peek(name), name).toBeUndefined();
            expect(binding(name), name).toHaveBeenCalledTimes(1);
        }
        await act(async () => release());
        expect(hook.result.current.topics.topics).toEqual([]);
        expect(hook.result.current.goal.checkIn).toBeNull();
    });

    it('withdraws protected-app topics and chat values on the protection event, including cached hidden readers', async () => {
        seedToday();
        await reads.prefetchToday();
        await reads.prefetchMainChat();
        await reads.ListMainChatWaitingItems('', 50);
        await reads.ListMainChatAnswerChips(['answer']);
        const hook = renderHook(() => ({ topics: useTodayTopics(true), panel: useMainChatPanel(true) }));
        await act(async () => {});
        expect(hook.result.current.topics.topics[0].name).toBe('Fixture project');
        let finish!: () => void;
        const pending = new Promise<void>((resolve) => { finish = resolve; });
        binding('GetTodayTopics').mockClear().mockImplementation(async () => { await pending; return []; });
        binding('LoadStreamItemsPage').mockClear().mockImplementation(async () => { await pending; return { items: [], hasMore: false, nextCursor: -1 }; });
        // AddSensitiveApp emits this event after the Go policy change. Reads
        // not mounted here must also lose the pre-protection cache immediately.
        await emit('sensitive-apps:updated');
        for (const name of ['GetTodayTopics', 'GetTodayNews', 'GetTopicFinds', 'GetTodaySuggestion', 'GetGoalCheckIn', 'GetTodayQuietLines', 'GetLatestBrief', 'GetMainChatOverview']) {
            expect(pageQueryCache.peek(name), name).toBeUndefined();
        }
        expect(pageQueryCache.peek('ListMainChatWaitingItems', ['', 50])).toBeUndefined();
        expect(pageQueryCache.peek('ListMainChatAnswerChips', [['answer']])).toBeUndefined();
        expect(pageQueryCache.peek('LoadStreamItemsPage', ['main', -1, 100])).toBeUndefined();
        expect(binding('GetTodayTopics')).toHaveBeenCalledTimes(1);
        expect(binding('LoadStreamItemsPage')).toHaveBeenCalledTimes(1);
        await act(async () => finish());
        expect(hook.result.current.topics.topics).toEqual([]);
        expect(hook.result.current.panel.items).toEqual([]);
        expect(await reads.GetTodayTopics()).toEqual([]);
        expect(binding('GetTodayTopics')).toHaveBeenCalledTimes(1);
    });

    it('debounces capture invalidations for 500 ms after the last event', async () => {
        seedToday();
        await reads.prefetchToday();
        renderHook(() => useCaptureCounts(true));
        await act(async () => {});
        binding('GetCaptureCounts').mockClear();
        vi.useFakeTimers();
        await emit('activity:session-saved');
        await act(async () => { await vi.advanceTimersByTimeAsync(250); });
        await emit('calendar:synced');
        await act(async () => { await vi.advanceTimersByTimeAsync(499); });
        expect(binding('GetCaptureCounts')).not.toHaveBeenCalled();
        await act(async () => { await vi.advanceTimersByTimeAsync(1); });
        expect(binding('GetCaptureCounts')).toHaveBeenCalledTimes(1);
    });

    it.each([
        ['ready', false], ['none', false], ['none', true], ['error', false],
    ] as const)('waits for news revalidation before automatic generation: next=%s hidden=%s', async (nextStatus, hidden) => {
        seedToday();
        binding('GetTodayNews').mockResolvedValue({ date: '2026-10-22', status: 'none', items: [], updatedAt: 0 });
        await reads.GetTodayNews();
        let finish!: (value: unknown) => void;
        let fail!: (error: Error) => void;
        binding('GetTodayNews').mockClear().mockImplementation(() => new Promise((resolve, reject) => { finish = resolve; fail = reject; }));
        fixture.bindings.set('GenerateTodayNews', vi.fn().mockResolvedValue(undefined));
        vi.spyOn(Date, 'now').mockReturnValue(Date.now() + 60_001);
        const hook = renderHook(({ active }) => useTodayNews(active), { initialProps: { active: true } });
        await act(async () => {});
        expect(binding('GenerateTodayNews')).not.toHaveBeenCalled();
        if (hidden) hook.rerender({ active: false });
        await act(async () => {
            if (nextStatus === 'error') fail(new Error('read unavailable'));
            else finish({ date: '2026-10-22', status: nextStatus, items: [], updatedAt: 1 });
        });
        expect(binding('GetTodayNews')).toHaveBeenCalledTimes(1);
        expect(binding('GenerateTodayNews')).toHaveBeenCalledTimes(!hidden && nextStatus === 'none' ? 1 : 0);
        if (nextStatus === 'ready' || nextStatus === 'error') expect(hook.result.current.data.status).toBe(nextStatus);
    });

    it.each([false, true])('keeps news generating until its event, and restores authoritative state on dispatch failure; failed=%s', async (failed) => {
        seedToday();
        let finish!: () => void;
        let reject!: (error: Error) => void;
        const dispatch = new Promise<void>((resolve, no) => { finish = resolve; reject = no; });
        fixture.bindings.set('GenerateTodayNews', vi.fn(() => dispatch));
        const hook = renderHook(() => useTodayNews(true));
        await waitFor(() => expect(hook.result.current.loading).toBe(false));
        await act(async () => hook.result.current.regenerate());
        expect(hook.result.current.data.status).toBe('generating');
        expect(binding('GetTodayNews')).toHaveBeenCalledTimes(1);
        await act(async () => { if (failed) reject(new Error('dispatch failed')); else finish(); });
        if (failed) {
            expect(hook.result.current.data.status).toBe('ready');
            expect(binding('GetTodayNews')).toHaveBeenCalledTimes(2);
        } else {
            expect(hook.result.current.data.status).toBe('generating');
            expect(binding('GetTodayNews')).toHaveBeenCalledTimes(1);
            await emit('cron:run', { status: 'completed' });
            expect(hook.result.current.data.status).toBe('ready');
            expect(binding('GetTodayNews')).toHaveBeenCalledTimes(2);
        }
    });

    it.each([false, true])('publishes a cold bootstrap into the shared cache; empty=%s', async (empty) => {
        const partial = { topics: empty ? [] : topics, refreshing: true };
        binding('GetTodayTopics').mockResolvedValueOnce(partial);
        await reads.GetTodayTopics();
        const hook = renderHook(() => useTodayTopics(true));
        expect(hook.result.current.status).toBe('ready');
        expect(hook.result.current.refreshing).toBe(true);
        expect(hook.result.current.topics.map((topic) => topic.name)).toEqual(empty ? [] : ['Fixture project']);
        expect(binding('GetTodayTopics')).toHaveBeenCalledTimes(1);

        const published = { topics: [{ ...topics[0], name: 'Published registry' }], refreshing: false };
        binding('GetTodayTopics').mockResolvedValue(published);
        await emit('topics:updated');
        await waitFor(() => expect(hook.result.current.topics[0]?.name).toBe('Published registry'));
        expect(hook.result.current.refreshing).toBe(false);
        expect(pageQueryCache.peek('GetTodayTopics')).toEqual(published);
        expect(binding('GetTodayTopics')).toHaveBeenCalledTimes(2);
        hook.unmount();
        const reopened = renderHook(() => useTodayTopics(true));
        expect(reopened.result.current.topics[0].name).toBe('Published registry');
        expect(reopened.result.current.refreshing).toBe(false);
        await act(async () => {});
        expect(binding('GetTodayTopics')).toHaveBeenCalledTimes(2);
    });

    it.each(['sensitive-apps:updated', 'topics:withdrawn'].flatMap(event => [true, false].map(active => ({ event, active }))))('clears retained bootstrap topics on $event while active=$active, before the replacement resolves', async ({ event, active: enabled }) => {
        await reads.GetTodayTopics();
        const hook = renderHook(({ active }) => useTodayTopics(active), { initialProps: { active: true } });
        await act(async () => {});
        hook.rerender({ active: enabled });
        let finish!: (value: unknown) => void;
        binding('GetTodayTopics').mockImplementation(() => new Promise((resolve) => { finish = resolve; }));
        await emit(event);
        expect(hook.result.current.topics).toEqual([]);
        expect(pageQueryCache.peek('GetTodayTopics')).toBeUndefined();
        if (!enabled) hook.rerender({ active: true });
        await waitFor(() => expect(finish).toBeTypeOf('function'));
        await act(async () => finish({ topics: [], refreshing: true }));
        expect(hook.result.current.topics).toEqual([]);
        expect(hook.result.current.refreshing).toBe(true);
    });

    // Snappy 1.0: focus paints the topics Today has; "topics:updated" brings
    // new ones, and a read on show happens only once they are past budget.
    it('mounts Today topics without reads and keeps fresh topics on focus', async () => {
        await reads.GetTodayTopics();
        binding('GetTodayTopics').mockClear();
        const hook = renderHook(() => useTodayTopics(true));
        expect(hook.result.current.status).toBe('ready');
        expect(hook.result.current.topics[0].name).toBe('Fixture project');
        await act(async () => window.dispatchEvent(new Event('focus')));
        expect(binding('GetTodayTopics')).not.toHaveBeenCalled();
        vi.spyOn(Date, 'now').mockReturnValue(Date.now() + reads.TODAY_TOPICS_TTL_MS + 1);
        await act(async () => window.dispatchEvent(new Event('focus')));
        expect(binding('GetTodayTopics')).toHaveBeenCalledTimes(1);
    });

    it('shows stale Today topics immediately, revalidates once and publishes the new result', async () => {
        await reads.GetTodayTopics();
        binding('GetTodayTopics').mockClear().mockResolvedValue([{ ...topics[0], name: 'Updated project' }]);
        vi.spyOn(Date, 'now').mockReturnValue(Date.now() + reads.TODAY_TOPICS_TTL_MS + 1);
        const hook = renderHook(() => useTodayTopics(true));
        expect(hook.result.current.topics[0].name).toBe('Fixture project');
        await waitFor(() => expect(hook.result.current.topics[0].name).toBe('Updated project'));
        expect(binding('GetTodayTopics')).toHaveBeenCalledTimes(1);
    });

    it('invalidates topics while unmounted and refreshes once when reopened', async () => {
        await reads.GetTodayTopics();
        binding('GetTodayTopics').mockClear().mockResolvedValue([{ ...topics[0], name: 'After pin' }]);
        await emit('topics:updated');
        expect(pageQueryCache.peek('GetTodayTopics')).toEqual(topics);
        const hook = renderHook(() => useTodayTopics(true));
        await waitFor(() => expect(hook.result.current.topics[0].name).toBe('After pin'));
        expect(binding('GetTodayTopics')).toHaveBeenCalledTimes(1);
    });

    it('mounts the main chat panel from a warm first page with zero binding calls', async () => {
        await reads.GetMainChat();
        await reads.LoadStreamItemsPage('main', -1, 50);
        binding('GetMainChat').mockClear(); binding('LoadStreamItemsPage').mockClear();
        const hook = renderHook(() => useMainChatPanel(true));
        expect(hook.result.current.status).toBe('ready');
        expect(hook.result.current.items[0].content).toBe('Fixture answer');
        await act(async () => {});
        expect(binding('GetMainChat')).not.toHaveBeenCalled();
        expect(binding('LoadStreamItemsPage')).not.toHaveBeenCalled();
    });

    it('keeps the stored status row intact for hydration into the shared page session', async () => {
        const status = { id: 'mixed', kind: 'system', toolName: 'main_chat_status', rowClass: 'routine', localDay: '2026-11-02', createdAt: 1,
            toolInput: JSON.stringify({ period: 'morning', entries: [
                { id: 'pause', kind: 'capture_paused', text: 'Capture', momentId: 'moment', captureStartAt: new Date(2026, 10, 1, 22, 5).getTime() },
                { id: 'brief', kind: 'brief_missed', text: 'Brief' },
            ] }) };
        binding('LoadStreamItemsPage').mockResolvedValue({ items: [status], hasMore: false, nextCursor: 0 });
        const hook = renderHook(() => useMainChatPanel(true));
        await waitFor(() => expect(hook.result.current.status).toBe('ready'));
        expect(hook.result.current.items).toHaveLength(1);
        expect(hook.result.current.items[0].id).toBe('mixed');
        expect((hook.result.current.items[0].toolInput as { entries: unknown[] }).entries).toHaveLength(2);
    });

    it('forces an explicit panel reload even when its page is fresh', async () => {
        const hook = renderHook(() => useMainChatPanel(true));
        await waitFor(() => expect(hook.result.current.status).toBe('ready'));
        binding('LoadStreamItemsPage').mockClear().mockResolvedValue({ ...page, items: [{ ...page.items[0], content: 'Authoritative retry' }] });
        await act(async () => hook.result.current.reload());
        expect(binding('LoadStreamItemsPage')).toHaveBeenCalledTimes(1);
        expect(hook.result.current.items[0].content).toBe('Authoritative retry');
    });

    it('forces the reply-timeout read even if another read just warmed the panel', async () => {
        const hook = renderHook(() => useMainChatPanel(true));
        await waitFor(() => expect(hook.result.current.status).toBe('ready'));
        fixture.bindings.set('SendMainChat', vi.fn().mockResolvedValue({ sessionId: 'main' }));
        vi.useFakeTimers();
        await act(async () => hook.result.current.send('Fixture request'));
        expect(hook.result.current.waiting).toBe(true);
        await act(async () => { await vi.advanceTimersByTimeAsync(179_000); });
        await act(async () => { pageQueryCache.set('LoadStreamItemsPage', ['main', -1, 50], page); });
        binding('LoadStreamItemsPage').mockClear().mockResolvedValue({ ...page, items: [{ ...page.items[0], content: 'Reply without event' }] });
        await act(async () => { await vi.advanceTimersByTimeAsync(1_000); });
        expect(hook.result.current.waiting).toBe(false);
        expect(binding('LoadStreamItemsPage')).toHaveBeenCalledTimes(1);
        expect(hook.result.current.items[0].content).toBe('Reply without event');
    });

    it('shows the current run as it streams and stops only its run id', async () => {
        const hook = renderHook(() => useMainChatPanel(true));
        await waitFor(() => expect(hook.result.current.status).toBe('ready'));
        fixture.bindings.set('SendMainChat', vi.fn().mockResolvedValue({ sessionId: 'main' }));
        fixture.bindings.set('CancelStream', vi.fn().mockResolvedValue(undefined));
        await act(async () => hook.result.current.send('Ask'));
        await emit('agent:event', { type: 'run_created', tag: 'main', run_id: 'run-1' });
        await emit('agent:event', { type: 'assistant', tag: 'main', run_id: 'run-1', item_id: 'live', item_seq: 1, content: 'First ' });
        await emit('agent:event', { type: 'assistant', tag: 'main', run_id: 'run-1', item_id: 'live', item_seq: 2, content: 'words' });
        await emit('agent:event', { type: 'assistant', tag: 'main', run_id: 'other', item_id: 'other', item_seq: 1, content: 'Wrong' });
        expect(hook.result.current.items.find((item) => item.id === 'live')?.content).toBe('First words');
        expect(hook.result.current.items.find((item) => item.id === 'other')).toBeUndefined();
        await act(async () => hook.result.current.stop());
        expect(binding('CancelStream')).toHaveBeenCalledWith('run-1');
        expect(hook.result.current.stopped).toBe(true);
    });

    it('uses the answer-only binding for a clicked Ask and preserves it on start over', async () => {
        const hook = renderHook(() => useMainChatPanel(true));
        await waitFor(() => expect(hook.result.current.status).toBe('ready'));
        const ask = vi.fn().mockResolvedValue({ sessionId: 'main' });
        const ordinary = vi.fn().mockResolvedValue({ sessionId: 'main' });
        fixture.bindings.set('AskMainChatWithContext', ask);
        fixture.bindings.set('SendMainChatWithContext', ordinary);
        const ref = { sourceKind: 'activity', sourceId: 'moment' };
        await act(async () => hook.result.current.send('What happened?', ref, true));
        expect(ask).toHaveBeenCalledWith('What happened?', ref);
        expect(ordinary).not.toHaveBeenCalled();
        await emit('agent:event', { type: 'done', tag: 'main' });
        await act(async () => hook.result.current.startOver());
        expect(ask).toHaveBeenCalledTimes(2);
    });

    it('revalidates the stale main chat once and refreshes on append and terminal events', async () => {
        await reads.GetMainChat();
        await reads.LoadStreamItemsPage('main', -1, 50);
        binding('LoadStreamItemsPage').mockClear().mockResolvedValue({ ...page, items: [{ ...page.items[0], content: 'New answer' }] });
        vi.spyOn(Date, 'now').mockReturnValue(Date.now() + 60_001);
        const hook = renderHook(() => useMainChatPanel(true));
        expect(hook.result.current.items[0].content).toBe('Fixture answer');
        await waitFor(() => expect(hook.result.current.items[0].content).toBe('New answer'));
        expect(binding('LoadStreamItemsPage')).toHaveBeenCalledTimes(1);
        await emit('session:items-appended', { sessionId: 'main' });
        expect(binding('LoadStreamItemsPage')).toHaveBeenCalledTimes(2);
        await emit('agent:event', { type: 'done', tag: 'main' });
        expect(binding('LoadStreamItemsPage')).toHaveBeenCalledTimes(3);
        expect(fixture.events.get('session:items-appended')?.size).toBe(1);
    });

    it('invalidates stream rows on append and waiting items only on the waiting-state event', async () => {
        await Promise.all([
            reads.GetMainChatOverview(),
            reads.ListMainChatWaitingItems('', 50),
            reads.LoadStreamItemsPage('main', -1, 50),
        ]);
        const invalidated: string[] = [];
        const unsubscribe = pageQueryCache.subscribe((name) => invalidated.push(name));
        await emit('session:items-appended', { sessionId: 'main' });
        expect(invalidated).toContain('LoadStreamItemsPage');
        expect(invalidated).not.toContain('GetMainChatOverview');
        expect(invalidated).not.toContain('ListMainChatWaitingItems');

        invalidated.length = 0;
        await emit('main-chat:waiting-changed', { count: 2, revision: 1 });
        expect(invalidated).toContain('ListMainChatWaitingItems');
        expect(invalidated).not.toContain('GetMainChatOverview');
        unsubscribe();
    });

    it('mounts the overview and chips without reads when fresh, never reads a Chat waiting list (K10), and refreshes on invalidation', async () => {
        await Promise.all([reads.GetMainChatOverview(), reads.ListMainChatWaitingItems('', 50), reads.ListMainChatAnswerChips(['answer']), reads.GetTodayTopics()]);
        for (const mock of fixture.bindings.values()) mock.mockClear();
        const items = [page.items[0]] as unknown as StreamItem[];
        renderHook(() => useMainChatView({ enabled: true, items }));
        await act(async () => {});
        for (const [name, mock] of fixture.bindings) {
            if (name !== 'GetMainChatWaiting') expect(mock, name).not.toHaveBeenCalled();
        }
        await emit('main-chat:chips', { itemId: 'answer', sessionId: 'main' });
        expect(binding('GetMainChatOverview')).not.toHaveBeenCalled();
        expect(binding('ListMainChatWaitingItems')).not.toHaveBeenCalled();
        expect(binding('ListMainChatAnswerChips')).toHaveBeenCalledTimes(1);
    });

    it('registers open-answer records from backend waiting-list deliveries', async () => {
        const openAnswer = {
            id: 'waiting-answer',
            sessionId: 'main',
            messageId: 'waiting-answer',
            question: 'Waiting question?',
            kind: 'weather',
            settles: 'result',
            delivery: 'settled_only',
            expiresAt: '2026-11-01T00:00:00Z',
            posts: 0,
            state: 'open',
            createdAt: '2026-10-26T09:00:00Z',
        };
        binding('ListMainChatWaitingItems').mockResolvedValue({
            items: [{ id: 'waiting-answer', openAnswer }],
            hasMore: false,
            nextCursor: '',
        });
        const store = renderHook(() => useOpenAnswers());

        await act(async () => {
            await reads.ListMainChatWaitingItems('', 50);
        });

        await waitFor(() => {
            expect(store.result.current.delivered.get(openAnswer.id)).toBe(
                store.result.current.generation,
            );
        });
        store.unmount();
    });

    it('does not re-read overview counts when streamed rows arrive', async () => {
        await reads.GetTodayTopics();
        binding('GetMainChatOverview').mockClear();
        const hook = renderHook(({ items }) => useMainChatView({ enabled: true, items }), { initialProps: { items: [] as StreamItem[] } });
        await waitFor(() => expect(binding('GetMainChatOverview')).toHaveBeenCalledTimes(1));
        binding('GetMainChatOverview').mockClear();
        for (let i = 0; i < 3; i++) {
            hook.rerender({ items: [{ id: `user-${i}`, kind: 'user', content: 'Fixture words' }] as StreamItem[] });
        }
        expect(binding('GetMainChatOverview')).not.toHaveBeenCalled();
    });

    it('reads chips authoritatively after a disabled page missed the resume event', async () => {
        const hook = renderHook(({ enabled }) => useMainChatAnswerChips(enabled, ['answer']), { initialProps: { enabled: true } });
        await waitFor(() => expect(hook.result.current.answer).toEqual(['Prepared request']));
        await emit('closed-eyes:changed', true);
        expect(hook.result.current).toEqual({});
        hook.rerender({ enabled: false });
        await emit('closed-eyes:changed', false);
        hook.rerender({ enabled: true });
        await waitFor(() => expect(hook.result.current.answer).toEqual(['Prepared request']));
    });

    it('does not replace a paged panel when a different page enters the cache', async () => {
        const hook = renderHook(() => useMainChatPanel(true));
        await waitFor(() => expect(hook.result.current.status).toBe('ready'));
        binding('LoadStreamItemsPage').mockResolvedValue({ items: [{ ...page.items[0], id: 'older', content: 'Older answer' }] });
        await act(async () => { await reads.LoadStreamItemsPage('main', 12, 50); });
        expect(hook.result.current.items.map((item) => item.content)).toEqual(['Fixture answer']);
    });
});


it('retains every local date on day updates and erases it on privacy changes', async () => {
    const binding = fixture.bindings.get('GetTodayDayShape') ?? vi.fn();
    fixture.bindings.set('GetTodayDayShape', binding);
    binding.mockResolvedValue({ events: [], hold: null, calendarConnected: true });
    for (const event of ['today-dayshape:updated', 'permissions:changed', 'closed-eyes:changed']) {
        await reads.GetTodayDayShape('2026-10-23');
        await reads.GetTodayDayShape('2026-10-24');
        expect(pageQueryCache.peek('GetTodayDayShape', ['2026-10-23'])).toBeDefined();
        await emit(event);
        for (const date of ['2026-10-23', '2026-10-24']) {
            if (event === 'today-dayshape:updated') expect(pageQueryCache.peek('GetTodayDayShape', [date])).toBeDefined();
            else expect(pageQueryCache.peek('GetTodayDayShape', [date])).toBeUndefined();
        }
    }
});


it('invalidates the day strip once for a source change and its derived event', async () => {
    const spy = vi.fn().mockResolvedValue({ events: [], hold: null, calendarConnected: true });
    fixture.bindings.set('GetTodayDayShape', spy);
    await reads.GetTodayDayShape('2026-10-23');
    await emit('calendar:synced');
    await reads.GetTodayDayShape('2026-10-23');
    await emit('brief:stored');
    await reads.GetTodayDayShape('2026-10-23');
    expect(spy).toHaveBeenCalledTimes(1);
    await emit('today-dayshape:updated');
    await reads.GetTodayDayShape('2026-10-23');
    expect(spy).toHaveBeenCalledTimes(2);
});
