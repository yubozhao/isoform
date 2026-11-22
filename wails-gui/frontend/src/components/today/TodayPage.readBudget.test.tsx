import { act, cleanup, render } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { resetPageQueries } from '@/lib/page-queries';
import { resetMainChatWaitingStore } from '@/lib/main-chat-waiting-store';
import { TodayPage } from './TodayPage';

// Today's read budget. Bo's app showed 83 brief reads in one second when one
// memory:updated re-read every page, and each brief read walks the saved
// copies. This gate counts every binding read TodayPage makes, with the real
// hooks and page cache, on mount and after one event. Each binding is read at
// most once, and the totals are pinned: a new read on Today's load or on a
// privacy sweep has to raise its budget here, on purpose.
// 16: the header's brief times (GetMobileBriefSchedule, computer-today SH1).
// 17 and 14: Your work (GetWorkList, SH2), which holds the brief's words, so a
// privacy sweep reads it again too. 18: whether any agent is connected
// (GetYourAgents, Your work's day-one card, computer-today v1.1.0).
const MOUNT_READ_BUDGET = 18;
const MEMORY_UPDATED_READ_BUDGET = 14;
const total = (calls: string[]) => calls.filter(isRead).length;

vi.mock('@/lib/launch-perf', () => ({ markLaunchStage: vi.fn(), markLaunchPaint: vi.fn() }));
vi.mock('react-i18next', () => ({
    useTranslation: () => ({ t: (key: string) => key, i18n: { language: 'en', resolvedLanguage: 'en' } }),
    Trans: ({ i18nKey }: { i18nKey: string }) => i18nKey,
}));
vi.mock('@/i18n', () => ({ default: { language: 'en', resolvedLanguage: 'en', t: (key: string) => key } }));

const fixture = vi.hoisted(() => ({
    calls: [] as string[],
    bindings: new Map<string, (...args: unknown[]) => Promise<unknown>>(),
    values: {} as Record<string, unknown>,
    events: new Map<string, Set<(...args: any[]) => unknown>>(),
}));
vi.mock('@/lib/wails-shim-app', () => new Proxy({}, {
    has: () => true,
    get: (_target, name: string) => {
        if (name === 'then' || name === '__esModule') return undefined;
        if (!fixture.bindings.has(name)) {
            fixture.bindings.set(name, async () => {
                fixture.calls.push(name);
                return name in fixture.values ? structuredClone(fixture.values[name]) : null;
            });
        }
        return fixture.bindings.get(name);
    },
}));
const runtime = vi.hoisted(() => ({
    EventsOn: (name: string, handler: (...args: any[]) => unknown) => {
        const set = fixture.events.get(name) ?? new Set();
        set.add(handler); fixture.events.set(name, set);
        return () => set.delete(handler);
    },
    EventsEmit: () => undefined,
    BrowserOpenURL: () => undefined,
}));
vi.mock('../../../wailsjs/runtime/runtime', () => runtime);

const handlers = {
    onStartChat: vi.fn(), onRunNow: vi.fn(), onOpenMemory: vi.fn(), onOpenAutomations: vi.fn(),
    onOpenActivity: vi.fn(), onOpenPeople: vi.fn(), onOpenTasks: vi.fn(),
};

async function settle() {
    for (let i = 0; i < 20; i++) await act(async () => { await new Promise((resolve) => setTimeout(resolve, 0)); });
}
async function emit(name: string, data?: unknown) {
    await act(async () => { await Promise.all([...(fixture.events.get(name) ?? [])].map((handler) => handler(data))); });
    await settle();
}
// Reads only: writes such as RecordTodaySectionSeen fire once per section.
const isRead = (name: string) => /^(Get|List|Load|Is)/.test(name);
function counts(calls: string[]) {
    const out: Record<string, number> = {};
    for (const name of calls.filter(isRead)) out[name] = (out[name] ?? 0) + 1;
    return out;
}
function overBudget(calls: string[], perBinding: number) {
    return Object.fromEntries(Object.entries(counts(calls)).filter(([, n]) => n > perBinding));
}
async function mountToday() {
    render(<TodayPage handlers={handlers} />);
    await settle();
    const mount = [...fixture.calls];
    fixture.calls.length = 0;
    return mount;
}

beforeEach(() => {
    resetPageQueries();
    resetMainChatWaitingStore();
    fixture.calls.length = 0;
    fixture.bindings.clear();
    fixture.events.clear();
    fixture.values = {
        GetMainChat: { session: { localId: 'main' } },
        LoadStreamItemsPage: { items: [], hasMore: false, nextCursor: -1 },
        GetMainChatOverview: { conversations: 0, waiting: 0, finished: 0, routine: 0 },
        GetMainChatWaiting: { sessionId: 'main', count: 0, items: [], revision: 1, ready: true },
        ListMainChatWaitingItems: { items: [], hasMore: false, nextCursor: '' },
        ListMainChatAnswerChips: {},
        GetTodayTopics: [],
        GetTodayQuietLines: [],
        GetUserTopics: { topics: [], removed: [], enabled: true },
        GetEngagementSettings: { kinds: [] },
        GetAssistantSignOut: { assistant: '', signedOut: false },
        GetTodayYourWeekDeclined: false,
        IsClosedEyesModeEnabled: false,
        GetWatchedToday: 0,
    };
});
afterEach(() => { cleanup(); resetPageQueries(); resetMainChatWaitingStore(); });

describe('Today read budget', () => {
    it('reads each binding at most once on mount', async () => {
        const mount = await mountToday();
        expect(overBudget(mount, 1)).toEqual({});
        expect(total(mount)).toBeGreaterThan(0);
        expect(total(mount), JSON.stringify(counts(mount))).toBeLessThanOrEqual(MOUNT_READ_BUDGET);
    });

    it('re-reads each binding at most once after one memory:updated', async () => {
        await mountToday();
        await emit('memory:updated', { reason: 'index' });
        // A cleared cache used to orphan the read its own notice started, so
        // the brief was asked for twice per sweep.
        expect(overBudget(fixture.calls, 1)).toEqual({});
        expect(total(fixture.calls), JSON.stringify(counts(fixture.calls))).toBeLessThanOrEqual(MEMORY_UPDATED_READ_BUDGET);
    });

    it('reads nothing after agency:updated: the goal check-in left Today (computer-map rule 8)', async () => {
        await mountToday();
        await emit('agency:updated');
        expect(counts(fixture.calls)).toEqual({});
    });
});
