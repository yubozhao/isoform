import { fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import '@/i18n';
import { WhySheet } from './WhySheet';
import { WhyLine } from './why-line';
import { whySummaryText } from './why-copy';
import i18n from '@/i18n';

const fixture = vi.hoisted(() => ({ bindings: new Map<string, ReturnType<typeof vi.fn>>() }));

vi.mock('@/lib/wails-shim-app', () => new Proxy({}, {
    has: () => true,
    get: (_target, name: string) => {
        if (name === 'then') return undefined;
        if (!fixture.bindings.has(name)) fixture.bindings.set(name, vi.fn().mockResolvedValue(undefined));
        return fixture.bindings.get(name);
    },
}));

function binding(name: string) {
    if (!fixture.bindings.has(name)) fixture.bindings.set(name, vi.fn().mockResolvedValue(undefined));
    return fixture.bindings.get(name)!;
}

const now = new Date();
const today = (h: number) => new Date(now.getFullYear(), now.getMonth(), now.getDate(), h).getTime();
const dayKey = (at: number) => {
    const d = new Date(at);
    return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`;
};
const earlier = today(9) - 3 * 86_400_000;

const view = {
    kind: 'claim',
    id: 'belief-1',
    summary: { total: 3, bySource: { screen: 2, agent: 1 }, thisWeek: 3 },
    days: [
        { day: dayKey(today(10)), moments: [
            { id: 's1', at: today(10), source: 'screen', app: 'Editor', title: 'Reviewed the plan', activitySessionId: 's1', canNotThis: true },
            { id: 's2', at: today(9), source: 'screen', app: 'Editor', title: 'Wrote the spec', activitySessionId: 's2', canNotThis: true },
        ] },
        { day: dayKey(earlier), moments: [
            { id: 'history:x', at: earlier, source: 'agent', app: 'Claude Code', canNotThis: false },
        ] },
    ],
};

describe('WhySheet', () => {
    beforeEach(() => fixture.bindings.clear());

    it('shows the moments grouped by day and source, and opens one where it happened', async () => {
        binding('GetWhy').mockResolvedValue(view);
        const onOpenMoment = vi.fn();
        render(<WhySheet kind="claim" id="belief-1" open onOpenChange={() => {}} onOpenMoment={onOpenMoment} />);
        const days = await screen.findAllByTestId('why-day');
        expect(binding('GetWhy')).toHaveBeenCalledWith('claim', 'belief-1');
        expect(days).toHaveLength(2);
        expect(within(days[0]).getByText('Today')).toBeTruthy();
        expect(within(days[0]).getAllByTestId('why-moment')).toHaveLength(2);
        expect(within(days[1]).getByText('A session in Claude Code')).toBeTruthy();
        // The agent session can't be taken off; the screen moments can.
        expect(within(days[1]).queryByText('Not this')).toBeNull();
        expect(screen.getByText('From 2 moments on your screen and a session with an agent this week')).toBeTruthy();
        const dots = document.querySelectorAll('[data-why-dot]');
        expect(dots[0].className).toMatch(/bg-app-indigo-text/);
        expect(dots[2].className).toMatch(/bg-app-plum-text/);
        fireEvent.click(screen.getByText('Reviewed the plan'));
        expect(onOpenMoment).toHaveBeenCalledWith(expect.objectContaining({ id: 's1', activitySessionId: 's1' }));
    });

    it('Not this calls NotThis and takes the moment off', async () => {
        binding('GetWhy').mockResolvedValue(view);
        binding('NotThis').mockResolvedValue(undefined);
        const onChanged = vi.fn();
        render(<WhySheet kind="claim" id="belief-1" open onOpenChange={() => {}} onChanged={onChanged} />);
        await screen.findByText('Wrote the spec');
        fireEvent.click(screen.getByRole('button', { name: 'Not this: Wrote the spec' }));
        await waitFor(() => expect(screen.queryByText('Wrote the spec')).toBeNull());
        expect(binding('NotThis')).toHaveBeenCalledWith('claim', 'belief-1', 's2');
        expect(onChanged).toHaveBeenCalled();
        expect(screen.getByText('Reviewed the plan')).toBeTruthy();
    });

    it('says when Not this fails and keeps the moment', async () => {
        binding('GetWhy').mockResolvedValue(view);
        binding('NotThis').mockRejectedValue(new Error('nope'));
        render(<WhySheet kind="claim" id="belief-1" open onOpenChange={() => {}} />);
        await screen.findByText('Wrote the spec');
        fireEvent.click(screen.getByRole('button', { name: 'Not this: Wrote the spec' }));
        expect(await screen.findByRole('alert')).toBeTruthy();
        expect(screen.getByText('Wrote the spec')).toBeTruthy();
    });

    it('says so when nothing is linked', async () => {
        binding('GetWhy').mockResolvedValue({ kind: 'claim', id: 'b', summary: { total: 0, bySource: {}, thisWeek: 0 }, days: [] });
        render(<WhySheet kind="claim" id="b" open onOpenChange={() => {}} />);
        expect((await screen.findByTestId('why-empty')).textContent).toBe('Nothing Isoform saw is linked to this yet.');
    });

    it('a failed read shows an error with Try again, never an empty Why', async () => {
        binding('GetWhy').mockRejectedValueOnce(new Error('timeout')).mockResolvedValueOnce(view);
        render(<WhySheet kind="work" id="w1" open onOpenChange={() => {}} />);
        const alert = await screen.findByRole('alert');
        expect(alert.textContent).toContain("Couldn't load where this came from.");
        expect(screen.queryByTestId('why-empty')).toBeNull();
        fireEvent.click(within(alert).getByRole('button', { name: 'Try again' }));
        expect(await screen.findByText('Reviewed the plan')).toBeTruthy();
        expect(binding('GetWhy')).toHaveBeenCalledTimes(2);
    });
});

describe('WhyLine', () => {
    it('a claim has its source line and Why', () => {
        const onWhy = vi.fn();
        render(<WhyLine kind="claim" where="Seen 3 times" onWhy={onWhy} />);
        fireEvent.click(screen.getByRole('button', { name: 'Why' }));
        expect(onWhy).toHaveBeenCalled();
        expect(screen.getByText('Seen 3 times')).toBeTruthy();
    });

    it('a result that is itself a moment has no Why', () => {
        render(<WhyLine kind="moment" where="Mail · 9:40 AM" onWhy={() => {}} />);
        expect(screen.getByText('Mail · 9:40 AM')).toBeTruthy();
        expect(screen.queryByRole('button', { name: 'Why' })).toBeNull();
    });

    it('writes the source line from the counts', () => {
        const t = i18n.getFixedT('en', 'common');
        expect(whySummaryText(t, 'en', { total: 7, bySource: { mail: 6, call: 1 }, thisWeek: 7 })).toBe('From 6 emails and a call this week');
        expect(whySummaryText(t, 'en', { total: 7, bySource: { mail: 6, call: 1 }, thisWeek: 2 })).toBe('From 6 emails and a call');
        expect(whySummaryText(t, 'en', { total: 0, bySource: {}, thisWeek: 0, importedFrom: 'ChatGPT' })).toBe('Brought in from ChatGPT');
    });
});
