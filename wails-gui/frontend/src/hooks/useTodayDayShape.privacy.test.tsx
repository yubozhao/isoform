import { act, cleanup, render, renderHook, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { TodayDayStrip } from '@/components/today/TodayDayStrip';
import { useTodayDayShape } from './useTodayDayShape';
import '@/i18n';
import { resetPageQueries } from '@/lib/page-queries';
import { pageQueryCache } from '@/lib/query-cache';
import { dayShapeFixture } from '@/components/today/today-next.fixture';
import type { TodayDayShape } from '@/lib/wails-shim-app';

const fixture = vi.hoisted(() => ({ read: vi.fn(), events: new Map<string, Set<(...args: unknown[]) => unknown>>() }));
vi.mock('@/lib/wails-shim-app', () => ({ GetTodayDayShape: fixture.read }));
vi.mock('../../wailsjs/runtime/runtime', () => ({ EventsOn: (name: string, handler: (...args: unknown[]) => unknown) => {
    const listeners = fixture.events.get(name) ?? new Set();
    listeners.add(handler);
    fixture.events.set(name, listeners);
    return () => listeners.delete(handler);
} }));
const blocked: TodayDayShape = { ...dayShapeFixture, events: [], hold: null, calendarConnected: false };
const privacyEvents = ['permissions:changed', 'closed-eyes:changed', 'background-listening:changed', 'sensitive-apps:updated', 'memory:withdrawn'];
function deferred() {
    let resolve: (value: TodayDayShape) => void = () => {};
    const promise = new Promise<TodayDayShape>((done) => { resolve = done; });
    return { promise, resolve };
}
async function emit(name: string) {
    await act(async () => { await Promise.all([...(fixture.events.get(name) ?? [])].map((handler) => handler(true))); });
}
beforeEach(() => { resetPageQueries(); fixture.events.clear(); fixture.read.mockReset().mockResolvedValue(dayShapeFixture); });
afterEach(() => { cleanup(); resetPageQueries(); vi.useRealTimers(); });

it.each(privacyEvents)('clears rendered calendar titles immediately on %s while the gated reread waits', async (event) => {
    const hook = renderHook(() => useTodayDayShape(true, '2026-10-23'));
    await waitFor(() => expect(hook.result.current.shape?.events[0].title).toBe(dayShapeFixture.events[0].title));
    const reread = deferred();
    fixture.read.mockReturnValueOnce(reread.promise);
    await emit(event);
    expect(hook.result.current.shape).toBeNull();
    expect(pageQueryCache.peek('GetTodayDayShape', ['2026-10-23'])).toBeUndefined();
    await act(async () => reread.resolve(blocked));
    await waitFor(() => expect(hook.result.current.shape).toEqual(blocked));
});
it.each(privacyEvents)('cannot publish an in-flight pre-privacy response after %s', async (event) => {
    const oldRead = deferred(), gatedRead = deferred();
    fixture.read.mockReturnValueOnce(oldRead.promise).mockReturnValueOnce(gatedRead.promise);
    const hook = renderHook(() => useTodayDayShape(true, '2026-10-23'));
    await waitFor(() => expect(fixture.read).toHaveBeenCalledTimes(1));
    await emit(event);
    await act(async () => oldRead.resolve(dayShapeFixture));
    expect(hook.result.current.shape).toBeNull();
    await act(async () => gatedRead.resolve(blocked));
    await waitFor(() => expect(hook.result.current.shape).toEqual(blocked));
});

it('removes the actual strip from the DOM while a permission reread is pending', async () => {
    function Strip() {
        const { shape, dateISO } = useTodayDayShape(true, '2026-10-23');
        return <TodayDayStrip shape={shape} requestedDate={dateISO} timeZone="America/Los_Angeles" />;
    }
    render(<Strip />);
    await screen.findByTitle(/1:1 with Priya on Halcyon Meet/);
    const reread = deferred();
    fixture.read.mockReturnValueOnce(reread.promise);
    await emit('permissions:changed');
    expect(screen.queryByTestId('today-day-strip')).not.toBeInTheDocument();
    expect(screen.queryByTitle(/1:1 with Priya on Halcyon Meet/)).not.toBeInTheDocument();
    await act(async () => reread.resolve(blocked));
    expect(screen.queryByTestId('today-day-strip')).not.toBeInTheDocument();
});
it('shares the cached native read across simultaneous strip readers', async () => {
    const hooks = renderHook(() => [useTodayDayShape(true, '2026-10-23'), useTodayDayShape(true, '2026-10-23')]);
    await waitFor(() => expect(hooks.result.current.every((view) => view.shape?.calendarConnected)).toBe(true));
    expect(fixture.read).toHaveBeenCalledTimes(1);
    await emit('today-dayshape:updated');
    await waitFor(() => expect(fixture.read).toHaveBeenCalledTimes(2));
});

it.each(['calendar:synced', 'brief:stored'])('keeps the strip cache and preview stable on unrelated source event %s', async (event) => {
    // The strip reads the brief's own day while that is today (today-review v1.1.20 R44).
    vi.useFakeTimers({ toFake: ['Date'] });
    vi.setSystemTime(new Date(2026, 9, 23, 9));
    const hook = renderHook(() => useTodayDayShape(true, '2026-10-23'));
    await waitFor(() => expect(hook.result.current.shape).toEqual(dayShapeFixture));
    const snapshot = hook.result.current.shape;
    await emit(event);
    expect(fixture.read).toHaveBeenCalledTimes(1);
    expect(hook.result.current.shape).toBe(snapshot);
    expect(pageQueryCache.peek('GetTodayDayShape', ['2026-10-23'])).toBe(snapshot);
});

it.each([new Error('fixture date rejection'), { code: 'fixture-date-rejection' }, 'fixture date rejection'])(
    'keeps the populated strip for a rejected date reread (%j)', async (rejection) => {
        function Strip() {
            const { shape, dateISO } = useTodayDayShape(true, '2026-10-21');
            return <TodayDayStrip shape={shape} requestedDate={dateISO} timeZone="America/Los_Angeles" />;
        }
        const hook = renderHook(() => useTodayDayShape(true, '2026-10-21'));
        await waitFor(() => expect(hook.result.current.shape).toEqual(dayShapeFixture));
        render(<Strip />);
        await screen.findByTestId('today-day-strip');
        const populated = hook.result.current.shape;
        fixture.read.mockRejectedValueOnce(rejection);
        await emit('today-dayshape:updated');
        await waitFor(() => expect(fixture.read).toHaveBeenCalledTimes(2));
        expect(hook.result.current.shape).toBe(populated);
        expect(screen.getByTestId('today-day-strip')).toBeInTheDocument();
    },
);
