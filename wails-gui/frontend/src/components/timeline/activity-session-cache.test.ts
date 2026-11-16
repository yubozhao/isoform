import { act, renderHook, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import {
    invalidateActivitySessionCache,
    lastActivityScope,
    lastActivityScroll,
    lastTimelineFilters,
    rememberActivityScope,
    rememberActivityScroll,
    rememberTimelineFilters,
    resetActivitySessionCache,
    useActivitySessionQuery,
} from './activity-session-cache';
import { resetPageQueries } from '@/lib/page-queries';

// The Activity page remounts on every open; its scope, timeline filters and
// scroll offsets live in this module so a plain revisit restores them, and a
// privacy sweep (clear=true invalidation) drops them with the data.
const events = vi.hoisted(() => new Map<string, Set<(data?: unknown) => void>>());
vi.mock('@/lib/wails-shim-app', () => new Proxy({}, {
    has: () => true,
    get: (_target, name: string) => name === 'then' ? undefined : vi.fn().mockResolvedValue({}),
}));
vi.mock('../../../wailsjs/runtime/runtime', () => ({
    EventsOn: (name: string, fn: (data?: unknown) => void) => {
        const set = events.get(name) ?? new Set();
        set.add(fn);
        events.set(name, set);
        return () => set.delete(fn);
    },
}));

beforeEach(() => {
    resetPageQueries();
    resetActivitySessionCache();
});
afterEach(() => {
    resetPageQueries();
    resetActivitySessionCache();
});

describe('remembered Activity page state', () => {
    it('keeps scope, filters and scroll offsets across remounts', () => {
        expect(lastActivityScope()).toBeUndefined();
        expect(lastTimelineFilters()).toBeUndefined();
        expect(lastActivityScroll('day:2026-10-23')).toBeUndefined();

        rememberActivityScope('week');
        rememberTimelineFilters({ searchText: 'halcyon', timePreset: 'custom', customStart: '2026-10-01', customEnd: '2026-10-23', selectedApps: ['com.app.Terminal'] });
        rememberActivityScroll('day:2026-10-23', 420);
        rememberActivityScroll('timeline-sessions', 88);

        expect(lastActivityScope()).toBe('week');
        expect(lastTimelineFilters()).toEqual({ searchText: 'halcyon', timePreset: 'custom', customStart: '2026-10-01', customEnd: '2026-10-23', selectedApps: ['com.app.Terminal'] });
        expect(lastActivityScroll('day:2026-10-23')).toBe(420);
        expect(lastActivityScroll('timeline-sessions')).toBe(88);
    });

    it('a privacy (clearing) invalidation drops the remembered UI state with the data', () => {
        rememberActivityScope('week');
        rememberTimelineFilters({ searchText: 'halcyon', timePreset: 'today', customStart: '', customEnd: '', selectedApps: null });
        rememberActivityScroll('day:2026-10-23', 420);

        // A plain invalidation (data refresh) keeps the UI state.
        invalidateActivitySessionCache(false);
        expect(lastActivityScope()).toBe('week');
        expect(lastActivityScroll('day:2026-10-23')).toBe(420);

        invalidateActivitySessionCache(true);
        expect(lastActivityScope()).toBeUndefined();
        expect(lastTimelineFilters()).toBeUndefined();
        expect(lastActivityScroll('day:2026-10-23')).toBeUndefined();
    });
});

describe('privacy sweep events and the remembered UI state', () => {
    // The facade's clearing list: every privacy transition drops the
    // remembered scope, filters and scroll with the data.
    const CLEARING_EVENTS = ['closed-eyes:changed', 'memory:withdrawn', 'knowledge:updated', 'background-listening:changed', 'permissions:changed', 'sensitive-apps:updated'];
    const FILTERS = { searchText: 'halcyon', timePreset: 'custom', customStart: '2026-10-01', customEnd: '2026-10-23', selectedApps: ['com.app.Terminal'] };

    // Mounting the page's query hook subscribes the facade to the runtime
    // events (its start()); the remembered UI state is written as the page
    // would on unmount. The hook unmounts before the afterEach resets so a
    // reset's invalidation never redraws a mounted hook outside act.
    async function mountWithRememberedUi() {
        const load = vi.fn().mockResolvedValue('rows');
        const hook = renderHook(() => useActivitySessionQuery('activities:day', load));
        await waitFor(() => expect(hook.result.current).toBe('rows'));
        rememberActivityScope('week');
        rememberTimelineFilters(FILTERS);
        rememberActivityScroll('day:2026-10-23', 420);
        return hook;
    }

    function expectRememberedUiKept() {
        expect(lastActivityScope()).toBe('week');
        expect(lastTimelineFilters()).toEqual(FILTERS);
        expect(lastActivityScroll('day:2026-10-23')).toBe(420);
    }

    function expectRememberedUiDropped() {
        expect(lastActivityScope()).toBeUndefined();
        expect(lastTimelineFilters()).toBeUndefined();
        expect(lastActivityScroll('day:2026-10-23')).toBeUndefined();
    }

    it.each(CLEARING_EVENTS)('%s drops the remembered scope, filters and scroll', async (event) => {
        const hook = await mountWithRememberedUi();
        await act(async () => {
            events.get(event)?.forEach((fn) => fn());
        });
        expectRememberedUiDropped();
        hook.unmount();
    });

    it('a first event after a two-account sign-out drops remembered filters', async () => {
        const hook = await mountWithRememberedUi();
        await act(async () => {
            events.get('model-accounts:updated')?.forEach((fn) => fn({
                accounts: [{ provider: 'claude', kind: 'subscription', name: 'work', on: true }],
            }));
        });
        expectRememberedUiDropped();
        hook.unmount();
    });

    it('an ordinary event keeps the remembered scope, filters and scroll', async () => {
        const hook = await mountWithRememberedUi();
        await act(async () => {
            events.get('activity:session-saved')?.forEach((fn) => fn());
        });
        expectRememberedUiKept();
        hook.unmount();
    });
});
