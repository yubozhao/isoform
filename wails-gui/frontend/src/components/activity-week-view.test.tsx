import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import i18n from '@/i18n';
import { resetPageQueries } from '@/lib/page-queries';
import { toast } from 'sonner';
import { ActivityWeekView } from './activity-week-view';

// Every Wails binding resolves through the shim (vite aliases wailsjs/go/main/App
// to it), so one Proxy mock covers the whole import graph of the Week view.
const fixture = vi.hoisted(() => ({
    bindings: new Map<string, ReturnType<typeof vi.fn>>(),
    listeners: new Map<string, Set<(payload: unknown) => void>>(),
}));

vi.mock('@/lib/wails-shim-app', () => new Proxy({}, {
    has: () => true,
    get: (_target, name: string) => {
        if (name === 'then') return undefined;
        if (!fixture.bindings.has(name)) fixture.bindings.set(name, vi.fn().mockResolvedValue([]));
        return fixture.bindings.get(name);
    },
}));

vi.mock('../../wailsjs/runtime/runtime', () => ({
    EventsOn: (name: string, handler: (payload: unknown) => void) => {
        const listeners = fixture.listeners.get(name) ?? new Set();
        listeners.add(handler);
        fixture.listeners.set(name, listeners);
        return () => listeners.delete(handler);
    },
    ClipboardSetText: vi.fn(),
}));

vi.mock('sonner', () => ({ toast: { error: vi.fn() } }));

function binding(name: string) {
    if (!fixture.bindings.has(name)) fixture.bindings.set(name, vi.fn().mockResolvedValue([]));
    return fixture.bindings.get(name)!;
}

function yourWeekEntry(lead = 'A Halcyon week.') {
    return {
        briefId: '2026-10-23-evening',
        timeZone: 'America/Los_Angeles',
        week: { start: '2026-10-19', end: '2026-10-23', lead },
        cards: [],
    };
}

async function emitRuntimeEvent(name: string, payload?: unknown) {
    await act(async () => {
        for (const listener of fixture.listeners.get(name) ?? []) listener(payload);
    });
}

function deferred<T>() {
    let resolve!: (value: T) => void;
    let reject!: (reason?: unknown) => void;
    const promise = new Promise<T>((resolvePromise, rejectPromise) => {
        resolve = resolvePromise;
        reject = rejectPromise;
    });
    return { promise, resolve, reject };
}
// A day in the week of Oct 12-18, 2026 (last week relative to Oct 20).
const WEEK_DAY = new Date(2026, 9, 13);

function weeklyData(overrides: Record<string, unknown> = {}) {
    return {
        weekStart: '2026-10-12',
        weekEnd: '2026-10-18',
        aiSummary: 'A steady week of shipping the billing rework.',
        aiSummaryStatus: 'done',
        aiSummaryLanguage: 'en',
        dailyStats: [
            { date: '2026-10-12', weekday: 'Mon', activeMs: 3600_000, sessionCount: 3 },
            { date: '2026-10-13', weekday: 'Tue', activeMs: 0, sessionCount: 0 },
        ],
        totalActiveMs: 3600_000,
        dailyAvgMs: 1800_000,
        prevWeekMs: 7200_000,
        topApps: [],
        ...overrides,
        // eslint-disable-next-line @typescript-eslint/no-explicit-any
    } as any;
}

describe('Activity Week view', () => {
    beforeEach(async () => {
        resetPageQueries();
        fixture.bindings.forEach((mock) => mock.mockReset().mockResolvedValue([]));
        fixture.listeners.clear();
        vi.mocked(toast.error).mockClear();
        binding('EnsureWeeklySummary').mockResolvedValue(weeklyData({ autoStatus: 'ready' }));
        binding('GetDailySummary').mockImplementation(async (date: string) =>
            date === '2026-10-12' ? { aiSummary: 'Fixed the deploy pipeline. Then wrote docs.' } : null,
        );
        await i18n.changeLanguage('en');
    });
    afterEach(() => {
        cleanup();
        resetPageQueries();
    });

    it('renders the weekly digest and one row per day', async () => {
        const onOpenDay = vi.fn();
        render(<ActivityWeekView date={WEEK_DAY} onOpenDay={onOpenDay} />);
        expect(await screen.findByText(/Week of Oct 12/)).toBeInTheDocument();
        expect(screen.getByText(/A steady week of shipping the billing rework/)).toBeInTheDocument();
        expect(screen.getByText('1h')).toBeInTheDocument();
        expect(await screen.findByText('Fixed the deploy pipeline.')).toBeInTheDocument();
        fireEvent.click(screen.getByText('Mon, Oct 12').closest('button')!);
        expect(onOpenDay).toHaveBeenCalledWith('2026-10-12');
    });

    it('starts with Your week, then one row per day, with one active-time measure', async () => {
        binding('GetYourWeekForDate').mockResolvedValue(yourWeekEntry());
        render(<ActivityWeekView date={WEEK_DAY} onOpenDay={vi.fn()} />);
        const days = await screen.findByTestId('activity-week-days');
        const yourWeek = screen.getByTestId('activity-your-week');
        expect(yourWeek.compareDocumentPosition(days) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
        expect(screen.queryByText(/per day/i)).not.toBeInTheDocument();
        expect(screen.queryByText(/A steady week of shipping the billing rework/)).not.toBeInTheDocument();
    });

    it("puts Your week isn't written yet with Write it above the days when there is no look-back", async () => {
        binding('EnsureWeeklySummary').mockResolvedValue(weeklyData({ aiSummary: '', aiSummaryStatus: 'none' }));
        render(<ActivityWeekView date={WEEK_DAY} onOpenDay={vi.fn()} />);
        const days = await screen.findByTestId('activity-week-days');
        const empty = screen.getByText("Your week isn't written yet.");
        expect(empty.compareDocumentPosition(days) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
        const write = screen.getByRole('button', { name: 'Write it' });
        expect(write.compareDocumentPosition(days) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    });

    it('says in place when Your week or the recap fails, with Try again', async () => {
        binding('GetYourWeekForDate').mockRejectedValueOnce(new Error('offline'));
        binding('GenerateWeeklySummary').mockRejectedValueOnce(new Error('model down'));
        render(<ActivityWeekView date={WEEK_DAY} onOpenDay={vi.fn()} />);
        expect(await screen.findByText("Couldn't load Your week.")).toBeInTheDocument();
        fireEvent.click(within(screen.getByText("Couldn't load Your week.")).getByRole('button', { name: 'Try again' }));
        await waitFor(() => expect(binding('GetYourWeekForDate')).toHaveBeenCalledTimes(2));
        await waitFor(() => expect(screen.queryByText("Couldn't load Your week.")).not.toBeInTheDocument());
        fireEvent.click(await screen.findByRole('button', { name: /Write it again|Regenerate|Write summary/ }));
        expect(await screen.findByText("Couldn't write the weekly recap.")).toBeInTheDocument();
        expect(toast.error).not.toHaveBeenCalled();
    });

    // Activity review v1.0.3 #6: copper line, accent Try again, header above.
    it('keeps the week header above a copper load failure with an accent Try again', async () => {
        binding('EnsureWeeklySummary').mockRejectedValueOnce(new Error('database is locked'));
        render(<ActivityWeekView date={WEEK_DAY} onOpenDay={vi.fn()} />);
        const failure = await screen.findByText("Couldn't load this week.", { exact: false });
        expect(failure).toHaveClass('text-copper');
        expect(within(failure).getByRole('button', { name: 'Try again' })).toHaveClass('text-accent');
        const header = screen.getByRole('heading', { level: 2 });
        expect(header).toHaveTextContent('Week of Oct 12 – Oct 18');
        expect(header.compareDocumentPosition(failure) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    });

    it('puts each day headline beside its time and keeps a quiet dash for a day with no activity', async () => {
        render(<ActivityWeekView date={WEEK_DAY} onOpenDay={vi.fn()} />);
        const headline = await screen.findByText('Fixed the deploy pipeline.');
        const mon = screen.getByText('Mon, Oct 12').closest('button')!;
        const time = within(mon).getByText('1h');
        expect(time.compareDocumentPosition(headline) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
        const tue = screen.getByText('Tue, Oct 13').closest('button')!;
        expect(within(tue).getByText('\u2014')).toBeInTheDocument();
    });

    it('shows no week-over-week comparison', async () => {
        render(<ActivityWeekView date={WEEK_DAY} onOpenDay={vi.fn()} />);
        await screen.findByText(/Week of Oct 12/);
        expect(screen.queryByText(/last week/i)).not.toBeInTheDocument();
        expect(screen.queryByText(/%/)).not.toBeInTheDocument();
    });

    it('opens Chat about this week in place, with the field focused', async () => {
        render(<ActivityWeekView date={WEEK_DAY} onOpenDay={vi.fn()} />);
        fireEvent.click(await screen.findByRole('button', { name: 'Chat about this week' }));
        const field = screen.getByRole('textbox', { name: 'Ask anything about this' });
        expect(field).toHaveFocus();
        expect(screen.getByRole('button', { name: 'Ask' })).toBeInTheDocument();
    });

    it('reads the week through EnsureWeeklySummary and never starts a summary itself', async () => {
        binding('EnsureWeeklySummary').mockResolvedValue(
            weeklyData({ aiSummary: '', aiSummaryStatus: 'none', autoStatus: 'refreshing' }),
        );
        render(<ActivityWeekView date={WEEK_DAY} onOpenDay={vi.fn()} />);
        expect(await screen.findByText('Writing your week...')).toBeInTheDocument();
        expect(binding('EnsureWeeklySummary')).toHaveBeenCalledWith('2026-10-13');
        expect(binding('GenerateWeeklySummary')).not.toHaveBeenCalled();
        expect(screen.queryByRole('button', { name: /Write it/ })).not.toBeInTheDocument();
    });

    it.each([
        ['page privacy withdrawal', 'memory:withdrawn'],
        ['Closed Eyes', 'closed-eyes:changed'],
        ['sign-out', 'model-accounts:updated'],
    ])('%s clears the expanded week before showing the backend re-read', async (name, event) => {
        const initialEntry = yourWeekEntry();
        binding('GetYourWeekForDate').mockResolvedValue(initialEntry);
        render(<ActivityWeekView
            date={new Date(2026, 9, 20)}
            onOpenDay={vi.fn()}
            yourWeekHandlers={{
                onStartChat: vi.fn(),
                onRunNow: vi.fn(),
                onOpenMemory: vi.fn(),
                onOpenAutomations: vi.fn(),
                onOpenActivity: vi.fn(),
                onOpenPeople: vi.fn(),
            }}
        />);
        let row = await screen.findByTestId('activity-your-week');
        fireEvent.click(row.querySelector('button')!);
        expect(await screen.findByTestId('brief-your-week')).toBeInTheDocument();

        if (event === 'model-accounts:updated') {
            await emitRuntimeEvent(event, { accounts: [{ provider: 'claude', kind: 'subscription', name: 'work', on: true }] });
            row = await screen.findByTestId('activity-your-week');
            if (!screen.queryByTestId('brief-your-week')) fireEvent.click(row.querySelector('button')!);
            expect(await screen.findByTestId('brief-your-week')).toBeInTheDocument();
        }

        const reread = deferred<ReturnType<typeof yourWeekEntry>>();
        binding('GetYourWeekForDate').mockReturnValueOnce(reread.promise);
        await emitRuntimeEvent(event, event === 'model-accounts:updated' ? { accounts: [] } : undefined);
        expect(screen.queryByTestId('activity-your-week')).not.toBeInTheDocument();
        expect(screen.queryByTestId('brief-your-week')).not.toBeInTheDocument();

        if (name === 'page privacy withdrawal') {
            reread.resolve(yourWeekEntry('A fresh backend week.'));
            expect(await screen.findByText('A fresh backend week.')).toBeInTheDocument();
            expect(screen.queryByText('A Halcyon week.')).not.toBeInTheDocument();
        } else {
            reread.resolve({ cards: [] } as ReturnType<typeof yourWeekEntry>);
            await act(async () => {});
            expect(screen.queryByTestId('activity-your-week')).not.toBeInTheDocument();
        }
    });

    it('discards an in-flight Your Week response after privacy withdrawal', async () => {
        const stale = deferred<ReturnType<typeof yourWeekEntry>>();
        const reread = deferred<ReturnType<typeof yourWeekEntry>>();
        binding('GetYourWeekForDate').mockReturnValueOnce(stale.promise).mockReturnValueOnce(reread.promise);
        render(<ActivityWeekView date={new Date(2026, 9, 20)} onOpenDay={vi.fn()} />);
        await screen.findByText(/Week of Oct 12/);

        await emitRuntimeEvent('memory:withdrawn');
        expect(binding('GetYourWeekForDate')).toHaveBeenCalledTimes(2);
        expect(screen.queryByTestId('activity-your-week')).not.toBeInTheDocument();
        stale.resolve(yourWeekEntry('Stale private week.'));
        await act(async () => {});
        expect(screen.queryByText('Stale private week.')).not.toBeInTheDocument();

        reread.resolve({ cards: [] } as ReturnType<typeof yourWeekEntry>);
        await act(async () => {});
        expect(screen.queryByTestId('activity-your-week')).not.toBeInTheDocument();
    });

    it('shows a date-addressable Your Week entry and expands its card', async () => {
        binding('GetYourWeekForDate').mockResolvedValue({
            briefId: '2026-10-23-evening',
            timeZone: 'America/Los_Angeles',
            week: { start: '2026-10-19', end: '2026-10-22', lead: 'A Halcyon week.' },
            cards: [],
        });
        const weekDate = new Date(2026, 9, 20);
        render(<ActivityWeekView
            date={weekDate}
            onOpenDay={vi.fn()}
            yourWeekHandlers={{
                onStartChat: vi.fn(),
                onRunNow: vi.fn(),
                onOpenMemory: vi.fn(),
                onOpenAutomations: vi.fn(),
                onOpenActivity: vi.fn(),
                onOpenPeople: vi.fn(),
            }}
        />);
        const row = await screen.findByTestId('activity-your-week');
        expect(row).toHaveTextContent('Your week');
        expect(row).toHaveTextContent('A Halcyon week.');
        const toggle = row.querySelector('button');
        expect(toggle).not.toBeNull();
        fireEvent.click(toggle!);
        expect(await screen.findByTestId('brief-your-week')).toBeInTheDocument();
        expect(screen.queryByRole('button', { name: 'Turn off Your week' })).not.toBeInTheDocument();
    });

    it('hides the date-addressable Your Week entry when none is available', async () => {
        binding('GetYourWeekForDate').mockResolvedValue({ cards: [] });
        render(<ActivityWeekView date={WEEK_DAY} onOpenDay={vi.fn()} />);
        await screen.findByText(/Week of Oct 12/);
        expect(screen.queryByTestId('activity-your-week')).not.toBeInTheDocument();
    });

    it('reads the week again when its summary is ready or failed', async () => {
        render(<ActivityWeekView date={WEEK_DAY} onOpenDay={vi.fn()} />);
        await screen.findByText(/Week of Oct 12/);
        const calls = binding('EnsureWeeklySummary').mock.calls.length;
        await emitRuntimeEvent('activity:summary-error', '2026-10-12');
        await waitFor(() => expect(binding('EnsureWeeklySummary').mock.calls.length).toBeGreaterThan(calls));
    });

    it('offers Write it only when no summary exists and none is being made', async () => {
        binding('EnsureWeeklySummary').mockResolvedValue(
            weeklyData({ aiSummary: '', aiSummaryStatus: 'none', autoStatus: 'unavailable' }),
        );
        render(<ActivityWeekView date={WEEK_DAY} onOpenDay={vi.fn()} />);
        expect(await screen.findByRole('button', { name: 'Write it' })).toBeInTheDocument();
        expect(screen.getByText("Your week isn't written yet.")).toBeInTheDocument();
        expect(screen.queryByText(/No weekly digest|Generate weekly digest/)).not.toBeInTheDocument();
        expect(screen.queryByText('Writing your week...')).not.toBeInTheDocument();
    });

    it.each([
        ['memory:withdrawn', undefined],
        ['closed-eyes:changed', true],
        ['model-accounts:updated', { accounts: [] }],
    ])('clears weekly and daily summaries synchronously on %s, then rereads', async (eventName, payload) => {
        let withdrawn = false;
        binding('EnsureWeeklySummary')
            .mockResolvedValueOnce(weeklyData({ aiSummary: 'Before the privacy transition.' }))
            .mockImplementation(async () => weeklyData({ aiSummary: 'After the privacy transition.' }));
        binding('GetDailySummary').mockImplementation(async (date: string) => {
            if (date !== '2026-10-12') return null;
            return {
                aiSummary: withdrawn ? 'After privacy, the day is still readable.' : 'Before privacy, the day was readable.',
            };
        });
        render(<ActivityWeekView date={WEEK_DAY} onOpenDay={vi.fn()} />);
        expect(await screen.findByText('Before the privacy transition.')).toBeInTheDocument();
        expect(await screen.findByText('Before privacy, the day was readable.')).toBeInTheDocument();

        withdrawn = true;
        await emitRuntimeEvent(eventName, payload);
        expect(screen.queryByText('Before the privacy transition.')).not.toBeInTheDocument();
        expect(screen.queryByText('Before privacy, the day was readable.')).not.toBeInTheDocument();

        expect(await screen.findByText('After the privacy transition.')).toBeInTheDocument();
        expect(await screen.findByText('After privacy, the day is still readable.')).toBeInTheDocument();
    });

    it('drops a weekly summary read that resolves after a privacy withdrawal', async () => {
        const stale = deferred<ReturnType<typeof weeklyData>>();
        const reread = new Promise<ReturnType<typeof weeklyData>>(() => undefined);
        binding('EnsureWeeklySummary')
            .mockImplementationOnce(() => stale.promise)
            .mockImplementationOnce(() => reread);
        render(<ActivityWeekView date={WEEK_DAY} onOpenDay={vi.fn()} />);
        await waitFor(() => expect(binding('EnsureWeeklySummary')).toHaveBeenCalledTimes(1));

        await emitRuntimeEvent('memory:withdrawn');
        stale.resolve(weeklyData({ aiSummary: 'Stale weekly summary.' }));
        await act(async () => {
            await Promise.resolve();
        });
        expect(screen.queryByText('Stale weekly summary.')).not.toBeInTheDocument();
    });

    it('drops a daily summary read that resolves after a privacy withdrawal', async () => {
        const stale = deferred<{ aiSummary: string }>();
        let dailyCalls = 0;
        binding('EnsureWeeklySummary').mockResolvedValue(weeklyData());
        binding('GetDailySummary').mockImplementation((date: string) => {
            dailyCalls += 1;
            if (dailyCalls === 1 && date === '2026-10-12') return stale.promise;
            if (dailyCalls === 2) return Promise.resolve(null);
            return new Promise(() => undefined);
        });
        render(<ActivityWeekView date={WEEK_DAY} onOpenDay={vi.fn()} />);
        await screen.findByText(/Week of Oct 12/);
        await waitFor(() => expect(dailyCalls).toBe(2));

        await emitRuntimeEvent('memory:withdrawn');
        stale.resolve({ aiSummary: 'Stale daily sentence.' });
        await act(async () => {
            await Promise.resolve();
        });
        expect(screen.queryByText('Stale daily sentence.')).not.toBeInTheDocument();
    });

    it('keeps Write it again disabled through a privacy withdrawal until manual generation settles', async () => {
        const generation = deferred<void>();
        const reread = deferred<ReturnType<typeof weeklyData>>();
        const currentWeek = weeklyData();
        binding('EnsureWeeklySummary')
            .mockResolvedValueOnce(currentWeek)
            .mockReturnValueOnce(reread.promise);
        binding('GenerateWeeklySummary').mockReturnValue(generation.promise);
        render(<ActivityWeekView date={WEEK_DAY} onOpenDay={vi.fn()} />);
        fireEvent.click(await screen.findByRole('button', { name: 'Write it again' }));
        expect(binding('GenerateWeeklySummary')).toHaveBeenCalledWith('2026-10-13');
        await waitFor(() => expect(screen.getByRole('button', { name: 'Write it again' })).toBeDisabled());

        await emitRuntimeEvent('memory:withdrawn');
        await waitFor(() => expect(binding('EnsureWeeklySummary')).toHaveBeenCalledTimes(2));
        reread.resolve(currentWeek);
        await screen.findByText(/A steady week of shipping the billing rework/);
        const busyButton = screen.queryByRole('button', { name: 'Write it again' }) as HTMLButtonElement | null;
        const remainedDisabled = busyButton?.disabled ?? false;

        generation.resolve(undefined);
        await waitFor(() => expect(screen.getByRole('button', { name: 'Write it again' })).toBeEnabled());
        expect(remainedDisabled).toBe(true);
    });

    it('clears Generate after a rejected request from a previous date without toasting', async () => {
        const generation = deferred<void>();
        binding('EnsureWeeklySummary').mockImplementation(async (date: string) => weeklyData({
            weekStart: date === '2026-10-20' ? '2026-10-19' : '2026-10-12',
            weekEnd: date === '2026-10-20' ? '2026-10-25' : '2026-10-18',
            aiSummary: '',
            aiSummaryStatus: 'none',
            autoStatus: 'unavailable',
        }));
        binding('GenerateWeeklySummary').mockReturnValue(generation.promise);
        const view = render(<ActivityWeekView date={WEEK_DAY} onOpenDay={vi.fn()} />);
        fireEvent.click(await screen.findByRole('button', { name: 'Write it' }));
        expect(binding('GenerateWeeklySummary')).toHaveBeenCalledWith('2026-10-13');

        view.rerender(<ActivityWeekView date={new Date(2026, 9, 20)} onOpenDay={vi.fn()} />);
        expect(await screen.findByText(/Week of Oct 19/)).toBeInTheDocument();

        await act(async () => {
            generation.reject(new Error('Old week generation failed.'));
            await Promise.resolve();
        });
        await waitFor(() => expect(screen.getByRole('button', { name: 'Write it' })).toBeEnabled());
        expect(toast.error).not.toHaveBeenCalled();
    });
});
