import { act, cleanup, render } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import '@/i18n';
import { MeetingRecordingRow } from './settings/pages/observation-page';
import { MemoryPage } from './memory-page';
import { KnowledgePage } from './knowledge-page';
import { useCronJobs } from '@/hooks/useCronJobs';
import { useMainChatView } from '@/hooks/useMainChatView';
import { useMainChatWaitingCount } from '@/hooks/useMainChatWaitingCount';
import { GetAutomaticMeetingRecordingEnabled, GetBackgroundListening, resetPageQueries } from '@/lib/page-queries';

const fixture = vi.hoisted(() => ({
    bindings: new Map<string, ReturnType<typeof vi.fn>>(),
    events: new Map<string, Set<(...args: any[]) => unknown>>(),
}));

vi.mock('@/lib/wails-shim-app', () => new Proxy({}, {
    has: () => true,
    get: (_target, name: string) => {
        if (name === 'then') return undefined;
        if (!fixture.bindings.has(name)) fixture.bindings.set(name, vi.fn().mockResolvedValue({}));
        return fixture.bindings.get(name);
    },
}));
vi.mock('../../wailsjs/runtime/runtime', () => ({
    EventsOn: (name: string, handler: (...args: any[]) => unknown) => {
        const handlers = fixture.events.get(name) ?? new Set();
        handlers.add(handler);
        fixture.events.set(name, handlers);
        return () => handlers.delete(handler);
    },
}));

const watched = [
    'GetBackgroundListening',
    'GetAutomaticMeetingRecordingEnabled',
    'GetMemoryEntries',
    'GetKnowledgeEntries',
    'ListMainChatWaitingItems',
    'GetMainChatOverview',
    'ListCronJobs',
] as const;

function binding(name: string) {
    if (!fixture.bindings.has(name)) fixture.bindings.set(name, vi.fn().mockResolvedValue({}));
    return fixture.bindings.get(name)!;
}

async function emit(name: string, data?: unknown) {
    await act(async () => {
        await Promise.all([...(fixture.events.get(name) ?? [])].map((handler) => handler(data)));
    });
}

type Page = 'chat' | 'memory' | 'knowledge' | 'settings';

function BindingPollShell({ page }: { page: Page }) {
    useCronJobs();
    useMainChatWaitingCount();
    useMainChatView({ enabled: page === 'chat', items: [] });
    return <>
        {page === 'chat' ? <div data-testid="chat-page" /> : null}
        {page === 'memory' ? <MemoryPage /> : null}
        {page === 'knowledge' ? <KnowledgePage /> : null}
        {page === 'settings' ? <MeetingRecordingRow listeningOn /> : null}
    </>;
}

describe('idle binding reads', () => {
    beforeEach(() => {
        resetPageQueries();
        fixture.bindings.clear();
        fixture.events.clear();
        binding('GetBackgroundListening').mockResolvedValue(false);
        binding('GetAutomaticMeetingRecordingEnabled').mockResolvedValue(false);
        binding('GetMemoryEntries').mockResolvedValue([]);
        binding('GetKnowledgeEntries').mockResolvedValue([]);
        binding('ListMainChatWaitingItems').mockResolvedValue({ items: [], hasMore: false, nextCursor: '' });
        binding('GetMainChatOverview').mockResolvedValue({ conversations: 0, waiting: 0, finished: 0, routine: 0 });
        binding('ListCronJobs').mockResolvedValue([]);
        binding('GetPlatformPermissions').mockResolvedValue({ items: [] });
        binding('GetDoingNow').mockResolvedValue({ kind: 'idle' });
        binding('GetAboutYou').mockResolvedValue({ beliefs: [], newThisWeek: [], totals: { beliefs: 0 }, storage: { files: 0, bytes: 0, chunks: 0 } });
        binding('GetMemoryStatus').mockResolvedValue({ files: 0, bytes: 0, chunks: 0 });
        binding('ListKnowHow').mockResolvedValue([]);
        binding('GetKnowHowMatch').mockResolvedValue({ available: false });
        binding('GetKnowHowCandidate').mockResolvedValue(null);
        binding('GetTodayTopics').mockResolvedValue([]);
        vi.useFakeTimers();
        vi.setSystemTime(new Date(2026, 9, 25, 15, 0, 0));
    });

    afterEach(() => {
        cleanup();
        resetPageQueries();
        vi.useRealTimers();
        vi.restoreAllMocks();
    });

    it('keeps a mounted shell and its pages to a handful of reads over five minutes', async () => {
        const shell = render(<BindingPollShell page="chat" />);
        await act(async () => { await vi.advanceTimersByTimeAsync(1_000); });
        const pages: Page[] = ['memory', 'knowledge', 'settings', 'chat'];
        let visible = true;
        vi.spyOn(document, 'visibilityState', 'get').mockImplementation(() => visible ? 'visible' : 'hidden');

        for (let elapsed = 5_000; elapsed <= 300_000; elapsed += 5_000) {
            visible = !visible;
            await act(async () => {
                window.dispatchEvent(new Event('focus'));
                document.dispatchEvent(new Event('visibilitychange'));
                if (elapsed % 15_000 === 0) shell.rerender(<BindingPollShell page={pages[(elapsed / 15_000) % pages.length]} />);
                await vi.advanceTimersByTimeAsync(5_000);
            });
        }

        const counts = Object.fromEntries(watched.map((name) => [name, binding(name).mock.calls.length]));
        console.log(`[binding-polls] five-minute wire calls ${JSON.stringify(counts)}`);
        // The sidebar's observing row (which read listening) is gone (design
        // S3/A5), so nothing in this shell needs to read it; at most once.
        const listeningReads = binding('GetBackgroundListening').mock.calls.length;
        expect(listeningReads).toBeLessThanOrEqual(1);
        expect(binding('GetAutomaticMeetingRecordingEnabled')).toHaveBeenCalledTimes(1);
        // Thirty show transitions may each refresh a mounted page once.
        for (const name of ['GetMemoryEntries', 'GetKnowledgeEntries']) expect(binding(name).mock.calls.length, name).toBeLessThanOrEqual(31);
        for (const name of watched.slice(4)) expect(binding(name).mock.calls.length, name).toBeLessThanOrEqual(6);

        await emit('background-listening:changed', true);
        await emit('automatic-meeting-recording:changed', true);
        await expect(GetBackgroundListening()).resolves.toBe(true);
        await expect(GetAutomaticMeetingRecordingEnabled()).resolves.toBe(true);
        expect(binding('GetBackgroundListening')).toHaveBeenCalledTimes(listeningReads);
        expect(binding('GetAutomaticMeetingRecordingEnabled')).toHaveBeenCalledTimes(1);

        shell.rerender(<BindingPollShell page="memory" />);
        await emit('memory:updated', { reason: 'test' });
        expect(binding('GetMemoryEntries').mock.calls.length).toBeGreaterThan(counts.GetMemoryEntries);
        shell.rerender(<BindingPollShell page="knowledge" />);
        await emit('knowledge:updated');
        expect(binding('GetKnowledgeEntries').mock.calls.length).toBeGreaterThan(counts.GetKnowledgeEntries);
        shell.rerender(<BindingPollShell page="chat" />);
        await act(async () => {});
        const overviewAfterOpen = binding('GetMainChatOverview').mock.calls.length;
        const waitingAfterOpen = binding('ListMainChatWaitingItems').mock.calls.length;
        await emit('session:items-appended');
        await act(async () => { await vi.advanceTimersByTimeAsync(500); });
        expect(binding('GetMainChatOverview').mock.calls.length).toBe(overviewAfterOpen);
        expect(binding('ListMainChatWaitingItems').mock.calls.length).toBe(waitingAfterOpen);
    });
});
