import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import '@/i18n';
import enActivity from '@/i18n/locales/en/activity.json';
import { ActivityFeed } from './activity-feed';

const fixture = vi.hoisted(() => ({ bindings: new Map<string, ReturnType<typeof vi.fn>>() }));

vi.mock('@/lib/wails-shim-app', () => new Proxy({}, {
    has: () => true,
    get: (_target, name: string) => {
        if (name === 'then') return undefined;
        if (!fixture.bindings.has(name)) fixture.bindings.set(name, vi.fn().mockResolvedValue([]));
        return fixture.bindings.get(name);
    },
}));

function binding(name: string) {
    if (!fixture.bindings.has(name)) fixture.bindings.set(name, vi.fn().mockResolvedValue([]));
    return fixture.bindings.get(name)!;
}

const day = new Date(2026, 10, 15);
const at = (h: number, m = 0) => new Date(2026, 10, 15, h, m).getTime();

describe('ActivityFeed', () => {
    beforeEach(() => fixture.bindings.clear());

    // computer-activity v1.0.0 rules 3, 7 and 8.
    it('shows each source in its rows as the backend orders them, with Closed Eyes as a bare gap', async () => {
        binding('GetActivityFeed').mockResolvedValue([
            { id: 'run-1', kind: 'agent', startMs: at(15), endMs: at(15, 20), activeMs: 1_200_000, app: 'Codex' },
            { id: '', kind: 'closed', startMs: at(11, 4), endMs: at(12, 4) },
            { id: 'rec-1', kind: 'watch', startMs: at(10), endMs: at(10, 36), words: 420, device: 'watch' },
            { id: 'm-1', kind: 'screen', startMs: at(9), endMs: at(9, 30), title: 'Pricing page copy', detail: 'Rewrote the plans table', app: 'Mail' },
        ]);
        await act(async () => {
            render(<ActivityFeed date={day} />);
        });
        await screen.findByText('Pricing page copy');
        const kinds = [...document.querySelectorAll('[data-feed-kind]')].map((row) => row.getAttribute('data-feed-kind'));
        expect(kinds).toEqual(['agent', 'closed', 'watch', 'screen']);
        expect(screen.getByText('Your agents · Codex')).toBeInTheDocument();
        expect(screen.getByText('Your watch')).toBeInTheDocument();
        expect(screen.getByText('Screen · Mail')).toBeInTheDocument();
        const gap = document.querySelector('[data-feed-kind="closed"]') as HTMLElement;
        expect(gap.textContent).toMatch(/nothing was kept$/);
        expect(gap.querySelector('button')).toBeNull();
        expect(document.querySelectorAll('[data-feed-kind] button')).toHaveLength(3);
        // pass-access v1.0.0: each Forget says which row it forgets.
        expect(screen.getByRole('button', { name: 'Forget: Pricing page copy' })).toBeInTheDocument();
        // No coloured bar at a row's left edge: the source is the dot and its name.
        for (const row of document.querySelectorAll('[data-feed-kind]')) {
            expect(row.className).not.toMatch(/border-l|before:|after:/);
        }
    });

    it('asks the backend for the day it shows and keeps only the tab kinds', async () => {
        binding('GetActivityFeed').mockResolvedValue([
            { id: 'run-1', kind: 'agent', startMs: at(15), endMs: at(15, 20), app: 'Claude' },
            { id: 'm-1', kind: 'screen', startMs: at(9), endMs: at(9, 30), title: 'Pricing page copy' },
        ]);
        await act(async () => {
            render(<ActivityFeed date={day} kinds={['agent']} />);
        });
        await screen.findByText('Your agents · Claude');
        expect(screen.queryByText('Pricing page copy')).toBeNull();
        expect(binding('GetActivityFeed')).toHaveBeenCalledWith(at(0), new Date(2026, 10, 16).getTime());
    });

    // computer-activity v1.1.0, 15e: one confirmation names everything that
    // goes and where it lives, says it can't be undone, and counts it on the
    // button, the row itself included.
    it('names everything that goes and where it lives, counts it on the button, then goes', async () => {
        binding('GetActivityFeed').mockResolvedValueOnce([
            { id: 'm-1', kind: 'screen', startMs: at(9), endMs: at(9, 30), title: 'Call with Lena', app: 'Zoom' },
        ]).mockResolvedValue([]);
        binding('GetActivityForgetPreview').mockResolvedValue({
            kind: 'screen', id: 'm-1', title: 'Call with Lena',
            goes: [{ kind: 'screen', startMs: at(9), endMs: at(9, 30) }, { kind: 'topic', title: 'Halcyon import' }, { kind: 'goal', title: 'Reply to Lena, with the slides' }],
        });
        binding('ForgetActivityFeedRow').mockResolvedValue(undefined);
        await act(async () => {
            render(<ActivityFeed date={day} />);
        });
        const forget = await screen.findByRole('button', { name: 'Forget: Call with Lena' });
        forget.focus();
        fireEvent.click(forget);
        const dialog = await screen.findByRole('dialog');
        expect(within(dialog).getByText('Forget "Call with Lena"?')).toBeInTheDocument();
        // pass-access v1.0.0: a confirm that can't be undone opens on Cancel.
        await waitFor(() => expect(within(dialog).getByRole('button', { name: 'Cancel' })).toHaveFocus());
        const plan = within(dialog).getByTestId('forget-plan');
        expect(plan).toHaveTextContent(/^Screen · Zoom, 9:00\s?AM, and what was made from it:/);
        expect([...plan.querySelectorAll('li')].map((li) => li.textContent)).toEqual([
            expect.stringMatching(/^the screen from 9:00\s?AM to 9:30\s?AM · Screen$/),
            'its place in Halcyon import · Your topics',
            'Reply to Lena, with the slides · Goals',
        ]);
        expect(plan).toHaveTextContent(enActivity.act.forgetGone);
        expect(binding('ForgetActivityFeedRow')).not.toHaveBeenCalled();
        fireEvent.click(within(dialog).getByRole('button', { name: 'Forget all 4' }));
        await waitFor(() => expect(binding('ForgetActivityFeedRow')).toHaveBeenCalledWith('screen', 'm-1'));
        await waitFor(() => expect(screen.queryByText('Call with Lena')).toBeNull());
    });

    it('reads Forget, with no list, when nothing was made from a row', async () => {
        binding('GetActivityFeed').mockResolvedValue([
            { id: 'rec-1', kind: 'watch', startMs: at(10), endMs: at(10, 36), words: 0, device: 'phone' },
        ]);
        binding('GetActivityForgetPreview').mockResolvedValue({ kind: 'watch', id: 'rec-1', title: '', goes: [{ kind: 'words' }, { kind: 'audio' }, { kind: 'copies' }] });
        await act(async () => {
            render(<ActivityFeed date={day} />);
        });
        expect(await screen.findByText('Your phone')).toBeInTheDocument();
        const forget = screen.getByRole('button', { name: /^Forget: Conversation/ });
        forget.focus();
        fireEvent.click(forget);
        const dialog = await screen.findByRole('dialog');
        expect(within(dialog).queryByRole('listitem')).toBeNull();
        expect(within(dialog).getByTestId('forget-plan')).toHaveTextContent(enActivity.act.forgetGone);
        expect(within(dialog).getByRole('button', { name: enActivity.act.forgetOne })).toBeInTheDocument();
        // Esc closes it and gives focus back to that row's Forget.
        fireEvent.keyDown(dialog, { key: 'Escape' });
        await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
        await waitFor(() => expect(forget).toHaveFocus());
    });
});
