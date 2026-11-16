import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { installPageQueryEvents, pageReadNames, resetPageQueries } from './page-queries';
import { pageQueryCache } from './query-cache';
import { onPagePrivacyWithdrawal } from './page-privacy-withdrawal';

// The privacy contract: Forget, a protection change, Closed Eyes, background
// listening, a permission change — each clears EVERY page's cached reads, so
// no pre-transition value survives anywhere, mounted or not.
const fixture = vi.hoisted(() => ({ events: new Map<string, Set<(...args: any[]) => unknown>>() }));
vi.mock('@/lib/wails-shim-app', () => new Proxy({}, {
    has: () => true,
    get: (_target, name: string) => name === 'then' ? undefined : vi.fn().mockResolvedValue({}),
}));
vi.mock('../../wailsjs/runtime/runtime', () => ({ EventsOn: (name: string, handler: (...args: any[]) => unknown) => {
    const set = fixture.events.get(name) ?? new Set();
    set.add(handler);
    fixture.events.set(name, set);
    return () => set.delete(handler);
} }));

const PAGES = ['today', 'chat', 'activity', 'goals', 'about-you', 'know-how', 'people', 'settings', 'search'] as const;
const PRIVACY_EVENTS = ['memory:withdrawn', 'closed-eyes:changed', 'background-listening:changed', 'permissions:changed', 'sensitive-apps:updated'];

/** The argument shapes each read is cached under (pages pass real args). */
const ARG_VARIANTS: Record<string, unknown[][]> = {
    LoadStreamItemsPage: [['main', -1, 100], ['main', -1, 50]],
    GetCaptureCounts: [[1761264000000]],
    GetActivitiesInRange: [[1761264000000, 1761350400000]],
    GetDayThreads: [[1761264000000, 1761350400000]],
    GetCronRunsByDateRange: [[1761264000000, 1761350400000]],
    'Activity:meetings': [['2026-10-23']],
    EnsureDailySummary: [['2026-10-23']],
    GetDayNoticed: [['2026-10-23']],
    LoadUIState: [['triage_enable_events'], ['triage_enable_memory']],
    GetMemoryEvidenceReadable: [['e1']],
    GetEntryUses: [['e1']],
    ListMemoryVersions: [['e1']],
    GetPersonDetail: [['priya']],
    ListPrincipalActivitySessions: [['priya', 50]],
    GetPersonResearch: [['priya']],
    GetVoiceEnrollmentStatus: [['priya']],
    ListVoiceSamples: [['priya']],
};

function everyEntry(): Array<[string, unknown[]]> {
    const out: Array<[string, unknown[]]> = [];
    for (const page of PAGES) {
        for (const name of pageReadNames(page)) {
            for (const args of ARG_VARIANTS[name] ?? [[]]) out.push([name, args]);
        }
    }
    return out;
}

async function emit(name: string, data?: unknown) {
    await Promise.all([...(fixture.events.get(name) ?? [])].map((handler) => handler(data)));
}

beforeEach(() => {
    resetPageQueries();
    fixture.events.clear();
    installPageQueryEvents();
});
afterEach(() => resetPageQueries());

describe('privacy sweeps clear every page', () => {
    it('a plain memory:updated (index pass, background learning) clears nothing and reads nothing', async () => {
        const entries = everyEntry();
        for (const [name, args] of entries) pageQueryCache.set(name, args, { seeded: true });
        const withdrawn = vi.fn();
        const off = onPagePrivacyWithdrawal(withdrawn);
        await emit('memory:updated', { reason: 'index' });
        off();
        expect(withdrawn).not.toHaveBeenCalled();
        for (const [name, args] of entries) expect(pageQueryCache.peek(name, args), `${name} kept after an index pass`).toBeDefined();
        // The memory reads are only marked stale: their next read revalidates.
        expect(pageQueryCache.state('GetMemoryEntries')?.at).toBe(0);
        expect(pageQueryCache.state('GetTodayTopics')?.at).not.toBe(0);
    });

    it('a forget (memory:withdrawn, then memory:updated) still clears every page at once', async () => {
        const entries = everyEntry();
        for (const [name, args] of entries) pageQueryCache.set(name, args, { seeded: true });
        await emit('memory:withdrawn');
        for (const [name, args] of entries) expect(pageQueryCache.peek(name, args), `${name} cleared by a forget`).toBeUndefined();
        await emit('memory:updated');
        for (const [name, args] of entries) expect(pageQueryCache.peek(name, args), `${name} stays cleared`).toBeUndefined();
    });

    it.each(PRIVACY_EVENTS)('%s withdraws every page\'s cached reads', async (event) => {
        const entries = everyEntry();
        expect(entries.length).toBeGreaterThan(35);
        for (const [name, args] of entries) pageQueryCache.set(name, args, { seeded: true });
        for (const [name, args] of entries) expect(pageQueryCache.peek(name, args), `${name} seeded`).toBeDefined();
        await emit(event);
        for (const [name, args] of entries) {
            if (event === 'background-listening:changed' && name === 'GetBackgroundListening') {
                expect(pageQueryCache.peek(name, args), `${name} published by ${event}`).toBe(false);
            } else {
                expect(pageQueryCache.peek(name, args), `${name} cleared by ${event}`).toBeUndefined();
            }
        }
    });
});

// model-accounts:updated also carries the 60-second quota poll, so only an
// identity withdrawal — a signed-in account gone or switched off — sweeps
// every page. Every first payload withdraws because a multi-account sign-out
// may predate this renderer's attach.
const ACCOUNT = { provider: 'claude', kind: 'subscription', name: 'work', on: true };

describe('sign-out withdraws every page', () => {
    it('a seeded value of any page and argument shape is gone after accounts empty out', async () => {
        await emit('model-accounts:updated', { accounts: [ACCOUNT] });
        const entries = everyEntry();
        for (const [name, args] of entries) pageQueryCache.set(name, args, { private: 'withdraw me' });
        await emit('model-accounts:updated', { accounts: [] });
        for (const [name, args] of entries) expect(pageQueryCache.peek(name, args), `${name} cleared by sign-out`).toBeUndefined();
    });

    it('a first event after a two-account sign-out withdraws pages warmed before the event', async () => {
        const personal = { provider: 'codex', kind: 'subscription', name: 'personal', on: true };
        const withdrawDom = vi.fn();
        const off = onPagePrivacyWithdrawal(withdrawDom);
        for (const [name, args] of everyEntry()) pageQueryCache.set(name, args, { private: 'two-account data' });
        await emit('model-accounts:updated', { accounts: [ACCOUNT] });
        expect(withdrawDom).toHaveBeenCalledOnce();
        for (const [name, args] of everyEntry()) expect(pageQueryCache.peek(name, args), `${name} cleared after ${personal.name} signed out`).toBeUndefined();
        off();
    });

    it('an empty first payload withdraws by privacy bias', async () => {
        for (const [name, args] of everyEntry()) pageQueryCache.set(name, args, { private: 'withdraw me' });
        await emit('model-accounts:updated', { accounts: [] });
        expect(pageQueryCache.peek('GetAboutYou')).toBeUndefined();
        expect(pageQueryCache.peek('ListPeopleOverview')).toBeUndefined();
    });

    it('an unreadable first payload withdraws by privacy bias', async () => {
        pageQueryCache.set('GetAboutYou', [], { private: 'withdraw me' });
        await emit('model-accounts:updated', { error: 'status unavailable' });
        expect(pageQueryCache.peek('GetAboutYou')).toBeUndefined();
    });

    it('an ordinary quota refresh keeps every cached page value', async () => {
        await emit('model-accounts:updated', { accounts: [ACCOUNT] });
        // GetSettingsOverview, GetTodayNews and GetTopicFinds re-read on EVERY
        // model-accounts:updated by design (account-derived rows); the quota
        // control asserts every other page's value survives.
        const accountDerived = new Set(['GetSettingsOverview', 'GetTodayNews', 'GetTopicFinds']);
        const entries = everyEntry().filter(([name]) => !accountDerived.has(name));
        for (const [name, args] of entries) pageQueryCache.set(name, args, { seeded: true });
        // Quota numbers and read timestamps change; the identities do not.
        await emit('model-accounts:updated', { accounts: [{ ...ACCOUNT, balance: '41%', readAt: '2026-10-23T10:00:00Z' }] });
        for (const [name, args] of entries) expect(pageQueryCache.peek(name, args), `${name} kept on quota refresh`).toBeDefined();
    });

    it('an added account is not a withdrawal', async () => {
        await emit('model-accounts:updated', { accounts: [ACCOUNT] });
        pageQueryCache.set('GetAboutYou', [], { seeded: true });
        await emit('model-accounts:updated', { accounts: [ACCOUNT, { provider: 'codex', kind: 'subscription', name: 'oss', on: true }] });
        expect(pageQueryCache.peek('GetAboutYou')).toBeDefined();
    });

    it('a switched-off account withdraws like a removed one', async () => {
        await emit('model-accounts:updated', { accounts: [ACCOUNT] });
        pageQueryCache.set('GetAboutYou', [], { seeded: true });
        await emit('model-accounts:updated', { accounts: [{ ...ACCOUNT, on: false }] });
        expect(pageQueryCache.peek('GetAboutYou')).toBeUndefined();
    });

    it('a read in flight at sign-out never lands the pre-sign-out value', async () => {
        await emit('model-accounts:updated', { accounts: [ACCOUNT] });
        let resolveOld!: (value: unknown) => void;
        let calls = 0;
        const loader = () => ++calls === 1
            ? new Promise((resolve) => { resolveOld = resolve; })
            : Promise.resolve({ fresh: 'permitted' });
        const oldRead = pageQueryCache.read('GetAboutYou', [], loader);
        void oldRead.catch(() => {});
        // Let the loader actually start: a read whose loader has not started
        // yet simply re-checks supersession on its scheduling microtask.
        await Promise.resolve();
        expect(calls).toBe(1);
        await emit('model-accounts:updated', { accounts: [] });
        // The pre-sign-out read resolves late; the superseded caller joins a
        // fresh read instead of receiving the withdrawn value.
        resolveOld({ private: 'stale' });
        await expect(oldRead).resolves.toEqual({ fresh: 'permitted' });
        expect(calls).toBe(2);
        expect(pageQueryCache.peek('GetAboutYou')).toEqual({ fresh: 'permitted' });
    });
});
