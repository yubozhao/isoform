import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import '@/i18n';
import enActivity from '@/i18n/locales/en/activity.json';
import { ActivityAnswers, sourceRates } from './activity-answers';
import type { JudgmentStats } from '@/lib/wails-shim-app';

const fixture = vi.hoisted(() => ({ bindings: new Map<string, ReturnType<typeof vi.fn>>() }));

vi.mock('@/lib/wails-shim-app', () => new Proxy({}, {
    has: () => true,
    get: (_target, name: string) => {
        if (name === 'then') return undefined;
        if (!fixture.bindings.has(name)) fixture.bindings.set(name, vi.fn().mockResolvedValue({ answers: [], days: [] }));
        return fixture.bindings.get(name);
    },
}));

function binding(name: string) {
    if (!fixture.bindings.has(name)) fixture.bindings.set(name, vi.fn().mockResolvedValue({ answers: [], days: [] }));
    return fixture.bindings.get(name)!;
}

const counts = (c: Partial<Record<string, number>>) => ({ shown: 0, answered: 0, accepted: 0, declined: 0, notMine: 0, useful: 0, notUseful: 0, undone: 0, ...c });
const now = new Date();
const today = (h: number) => new Date(now.getFullYear(), now.getMonth(), now.getDate(), h).toISOString();
const yesterday = new Date(now.getFullYear(), now.getMonth(), now.getDate() - 1, 9).toISOString();
const stats: JudgmentStats = {
    days: [
        { day: 'today', total: counts({ shown: 9, answered: 5, accepted: 3, useful: 1 }), bySource: { brief: counts({ shown: 4, answered: 3, accepted: 2 }), prep: counts({ shown: 2, answered: 2, accepted: 1, useful: 1 }) } },
        { day: 'yesterday', total: counts({ shown: 3, answered: 3, accepted: 3 }), bySource: { prep: counts({ answered: 2, accepted: 2 }), weird: counts({ answered: 1, accepted: 1 }) } },
    ],
};

describe('Your answers', () => {
    beforeEach(() => fixture.bindings.clear());

    it('sums each source over the days, unknown sources as Other, only sources with answers', () => {
        expect(sourceRates(stats)).toEqual([
            { source: 'prep', yes: 4, answered: 4 },
            { source: 'brief', yes: 2, answered: 3 },
            { source: 'other', yes: 1, answered: 1 },
        ]);
    });

    // redesign-activity v1.0.0 rules 8 and 9.
    it("shows today's four numbers and where yes comes from over 14 days", async () => {
        binding('GetJudgmentStats').mockResolvedValue(stats);
        await act(async () => {
            render(<ActivityAnswers />);
        });
        await waitFor(() => expect(binding('GetJudgmentStats')).toHaveBeenCalledWith(14));
        const number = (key: string) => document.querySelector(`[data-answer-number="${key}"]`)!.textContent;
        expect(number('made')).toBe(`9${enActivity.ans.made}`);
        expect(number('answered')).toBe(`5${enActivity.ans.answered}`);
        expect(number('yes')).toBe(`4${enActivity.ans.yes}`);
        expect(number('brief')).toBe(`2 of 4${enActivity.ans.brief}`);
        const sources = screen.getByTestId('answers-sources');
        expect(within(sources).getByText(enActivity.ans.sourcesMeta)).toBeInTheDocument();
        expect(within(sources).getByText(enActivity.ans.source.prep)).toBeInTheDocument();
        expect(within(sources).getByText('2 of 3 yes')).toBeInTheDocument();
    });

    // Scope cut B3: without Activity's extras the 14-day bars go; today's
    // numbers, Undo and Forget stay.
    it('hides where yes comes from without the Activity extras, and keeps Undo and Forget', async () => {
        (globalThis as Record<string, unknown>).__isoformFeatureSwitchesForTest = { automations: true, playbooks: true, proactiveVoice: true, calendarHolds: true, activityExtras: false };
        binding('GetJudgmentStats').mockResolvedValue(stats);
        binding('GetAnswers').mockResolvedValue({ answers: [
            { id: 'a1', item: 'w1', kind: 'work', title: 'Reply to Sam', action: 'yes', at: today(9), surface: 'computer', canUndo: true },
        ] });
        await act(async () => {
            render(<ActivityAnswers />);
        });
        expect(await screen.findByText('Reply to Sam')).toBeInTheDocument();
        await waitFor(() => expect(document.querySelector('[data-answer-number="made"]')!.textContent).toBe(`9${enActivity.ans.made}`));
        expect(screen.queryByTestId('answers-sources')).toBeNull();
        expect(screen.getByRole('button', { name: 'Undo: Reply to Sam' })).toBeInTheDocument();
        expect(screen.getByRole('button', { name: 'Forget: Reply to Sam' })).toBeInTheDocument();
    });

    // Rules 10 and 11.
    it('lists every answer newest first with its reason or edit and where it was given, pages on, and forgets one', async () => {
        binding('GetAnswers')
            .mockResolvedValueOnce({ answers: [
                { id: 'e2', item: 'w2', kind: 'work', title: 'Reply to Lena, with the slides', action: 'edit', edited: { before: 'Hi Lena', after: 'Hi Lena, slides attached' }, at: today(10), surface: 'computer' },
                { id: 'e1', item: 'w1', kind: 'work', title: 'Book the venue', action: 'no', reason: 'Already done', at: today(9), surface: 'phone', undone: true },
            ], next: 'e1' })
            .mockResolvedValueOnce({ answers: [
                { id: 'e0', item: 'belief:x', kind: 'belief', title: 'You keep calls short', action: 'yes', at: yesterday, surface: 'computer' },
            ] });
        binding('ForgetAnswer').mockResolvedValue(undefined);
        await act(async () => {
            render(<ActivityAnswers />);
        });
        expect(await screen.findByText('Reply to Lena, with the slides')).toBeInTheDocument();
        expect(screen.getByText(enActivity.ans.kept)).toBeInTheDocument();
        expect(screen.getByText('Hi Lena, slides attached')).toBeInTheDocument();
        expect(screen.getByText(`${enActivity.ans.surface.phone} · ${enActivity.ans.undone}`)).toBeInTheDocument();
        expect(screen.getByText('Book the venue')).toHaveClass('line-through');
        expect(binding('GetAnswers')).toHaveBeenLastCalledWith(50, '');
        fireEvent.click(screen.getByRole('button', { name: enActivity.ans.more }));
        expect(await screen.findByText('You keep calls short')).toBeInTheDocument();
        expect(binding('GetAnswers')).toHaveBeenLastCalledWith(50, 'e1');

        const row = screen.getByText('Book the venue').closest('[data-answer-row]') as HTMLElement;
        // pass-access v1.0.0: each Forget says which answer it forgets.
        fireEvent.click(within(row).getByRole('button', { name: 'Forget: Book the venue' }));
        expect(await screen.findByText('Your answer to "Book the venue" is deleted from this computer. Isoform may ask about it again.')).toBeInTheDocument();
        expect(binding('ForgetAnswer')).not.toHaveBeenCalled();
        const dialog = screen.getByRole('dialog');
        fireEvent.click([...dialog.querySelectorAll('button')].find((b) => b.textContent === enActivity.ans.forget)!);
        await waitFor(() => expect(binding('ForgetAnswer')).toHaveBeenCalledWith('e1'));
        await waitFor(() => expect(screen.queryByText('Book the venue')).toBeNull());
    });

    // redesign-activity v1.0.0 rule 10 and redesign v1.0.0 rule 28: a No shows
    // its reason in words, never the ledger's code; free words show as typed.
    it('says a reason in words, not its code', async () => {
        binding('GetAnswers').mockResolvedValue({ answers: [
            { id: 'a1', item: 'w1', kind: 'work', title: 'Reply to Sam', action: 'no', reason: 'not_useful', at: today(9), surface: 'computer' },
            { id: 'a2', item: 'w2', kind: 'work', title: 'Prep for Lena', action: 'no', reason: 'wrong_facts', at: today(8), surface: 'phone' },
            { id: 'a3', item: 'portrait', kind: 'portrait', title: 'In one line', action: 'no', note: 'Not a runner', at: yesterday, surface: 'computer' },
        ] });
        await act(async () => {
            render(<ActivityAnswers />);
        });
        await screen.findByText('Reply to Sam');
        expect(screen.getByText(enActivity.ans.reason.not_useful)).toBeInTheDocument();
        expect(screen.getByText(enActivity.ans.reason.wrong_facts)).toBeInTheDocument();
        expect(screen.getByText('Not a runner')).toBeInTheDocument();
        expect(screen.queryByText('not_useful')).toBeNull();
        expect(screen.queryByText('wrong_facts')).toBeNull();
    });

    // redesign-activity v1.0.0 rule 10: Undo on today's answers, until the
    // next brief, only where the ledger says it can still be taken back.
    it("offers Undo only where it can be taken back, and takes it back as Today's Undo does", async () => {
        binding('GetAnswers').mockResolvedValue({ answers: [
            { id: 'a1', item: 'w1', kind: 'work', title: 'Reply to Sam', action: 'yes', at: today(9), surface: 'computer', canUndo: true },
            { id: 'a2', item: 'belief:m1', kind: 'belief', title: 'Prefers mornings', action: 'yes', at: today(8), surface: 'computer' },
        ] });
        binding('UndoWork').mockResolvedValue({});
        await act(async () => {
            render(<ActivityAnswers />);
        });
        await screen.findByText('Reply to Sam');
        expect(screen.queryByRole('button', { name: 'Undo: Prefers mornings' })).toBeNull();
        await act(async () => {
            fireEvent.click(screen.getByRole('button', { name: 'Undo: Reply to Sam' }));
        });
        expect(binding('UndoWork')).toHaveBeenCalledWith('w1');
        await waitFor(() => expect(binding('GetAnswers').mock.calls.length).toBeGreaterThanOrEqual(2));
    });

    it('says so when Undo fails, and keeps the answer', async () => {
        binding('GetAnswers').mockResolvedValue({ answers: [
            { id: 'a1', item: 'w1', kind: 'work', title: 'Reply to Sam', action: 'yes', at: today(9), surface: 'computer', canUndo: true },
        ] });
        binding('UndoWork').mockRejectedValue(new Error('closed'));
        await act(async () => {
            render(<ActivityAnswers />);
        });
        await screen.findByText('Reply to Sam');
        await act(async () => {
            fireEvent.click(screen.getByRole('button', { name: 'Undo: Reply to Sam' }));
        });
        expect(await screen.findByText(enActivity.ans.undoFailed)).toBeInTheDocument();
        expect(screen.getByText('Reply to Sam')).toBeInTheDocument();
    });
});
