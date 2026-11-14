import { act, renderHook, waitFor } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { resetAppActivity } from '@/lib/app-activity';
import { resetPageQueries } from '@/lib/page-queries';
import { useTodayStatus } from './useTodayStatus';
import { useMainChatSetup } from './useMainChatSetup';

const fixture = vi.hoisted(() => ({
    listeners: new Map<string, Set<(data?: unknown) => void>>(),
    getHealth: null as unknown as ReturnType<typeof vi.fn>,
    getWeek: null as unknown as ReturnType<typeof vi.fn>,
    getSetup: null as unknown as ReturnType<typeof vi.fn>,
}));

vi.mock('../../wailsjs/runtime/runtime', () => ({
    EventsOn: (name: string, handler: (data?: unknown) => void) => {
        const set = fixture.listeners.get(name) ?? new Set();
        set.add(handler);
        fixture.listeners.set(name, set);
        return () => set.delete(handler);
    },
}));
vi.mock('@/lib/wails-shim-app', () => new Proxy({}, {
    has: () => true,
    get: (_t, name: string) => {
        if (name === 'then') return undefined;
        if (name === 'GetMobileBriefHealth') return fixture.getHealth;
        if (name === 'GetEngagementWeekSummary') return fixture.getWeek;
        if (name === 'GetMainChatSetup') return fixture.getSetup;
        return vi.fn().mockResolvedValue(null);
    },
}));
vi.mock('../../wailsjs/go/main/App', () => new Proxy({}, {
    has: () => true,
    get: (_t, name: string) => {
        if (name === 'then') return undefined;
        if (name === 'GetMainChatSetup') return fixture.getSetup;
        return vi.fn().mockResolvedValue(null);
    },
}));

function emit(name: string, data?: unknown) {
    for (const handler of [...(fixture.listeners.get(name) ?? [])]) handler(data);
}

beforeEach(() => {
    resetPageQueries();
    resetAppActivity();
    fixture.listeners.clear();
    fixture.getHealth = vi.fn().mockResolvedValue({ enabled: true });
    fixture.getWeek = vi.fn().mockResolvedValue({ spoke: 1 });
    fixture.getSetup = vi.fn().mockResolvedValue({ steps: [] });
});

// Snappy and the lock rule: a locked Mac is not looked at. A Today refresh
// that arrives while it is locked waits; it runs once when the Mac is unlocked.
describe('held while the Mac is locked', () => {
    it('Today status makes no brief-health read while locked, and one on unlock', async () => {
        const { unmount } = renderHook(() => useTodayStatus(true));
        // A loaded test machine can take over a second to mount; the lock rule
        // is what this test guards, not how fast the first read starts.
        await waitFor(() => expect(fixture.getHealth).toHaveBeenCalledTimes(1), { timeout: 10_000 });
        act(() => emit('system:state', 'locked'));
        act(() => emit('mobile:updated'));
        await act(async () => { await new Promise((r) => setTimeout(r, 20)); });
        expect(fixture.getHealth).toHaveBeenCalledTimes(1);
        act(() => emit('system:state', 'unlocked'));
        await waitFor(() => expect(fixture.getHealth).toHaveBeenCalledTimes(2), { timeout: 10_000 });
        await act(async () => { await new Promise((r) => setTimeout(r, 20)); });
        expect(fixture.getHealth).toHaveBeenCalledTimes(2);
        unmount();
    });

    it('main chat setup makes no read while locked, and one on unlock', async () => {
        const { unmount } = renderHook(() => useMainChatSetup(true));
        await act(async () => {});
        const before = fixture.getSetup.mock.calls.length;
        act(() => emit('system:state', 'locked'));
        act(() => emit('permissions:changed'));
        act(() => emit('background-listening:changed'));
        await act(async () => { await new Promise((r) => setTimeout(r, 400)); });
        expect(fixture.getSetup.mock.calls.length).toBe(before);
        act(() => emit('system:state', 'unlocked'));
        await waitFor(() => expect(fixture.getSetup.mock.calls.length).toBeGreaterThan(before), { timeout: 10_000 });
        unmount();
    });
});
