import { act, fireEvent, render, screen, waitFor } from '@testing-library/react';
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

    // Rule 2 of the ranked goals: Forget says what goes with it, then goes.
    it('names what goes with a row before forgetting it, and drops the row after', async () => {
        binding('GetActivityFeed').mockResolvedValueOnce([
            { id: 'm-1', kind: 'screen', startMs: at(9), endMs: at(9, 30), title: 'Call with Lena', app: 'Zoom' },
        ]).mockResolvedValue([]);
        binding('GetActivityForgetPreview').mockResolvedValue({
            kind: 'screen', id: 'm-1', title: 'Call with Lena',
            goes: [{ kind: 'topic', title: 'Halcyon import' }, { kind: 'goal', title: 'Reply to Lena, with the slides' }],
        });
        binding('ForgetActivityFeedRow').mockResolvedValue(undefined);
        await act(async () => {
            render(<ActivityFeed date={day} />);
        });
        fireEvent.click(await screen.findByRole('button', { name: enActivity.feed.forget }));
        expect(await screen.findByText(
            'Forget Call with Lena, and 2 things made from it: its place in Halcyon import; the goal Reply to Lena, with the slides.',
        )).toBeInTheDocument();
        expect(binding('ForgetActivityFeedRow')).not.toHaveBeenCalled();
        const dialog = screen.getByRole('dialog');
        fireEvent.click([...dialog.querySelectorAll('button')].find((b) => b.textContent === enActivity.feed.forgetConfirm)!);
        await waitFor(() => expect(binding('ForgetActivityFeedRow')).toHaveBeenCalledWith('screen', 'm-1'));
        await waitFor(() => expect(screen.queryByText('Call with Lena')).toBeNull());
    });

    it('says when nothing else was made from a row', async () => {
        binding('GetActivityFeed').mockResolvedValue([
            { id: 'rec-1', kind: 'watch', startMs: at(10), endMs: at(10, 36), words: 0, device: 'phone' },
        ]);
        binding('GetActivityForgetPreview').mockResolvedValue({ kind: 'watch', id: 'rec-1', title: '', goes: [] });
        await act(async () => {
            render(<ActivityFeed date={day} />);
        });
        expect(await screen.findByText('Your phone')).toBeInTheDocument();
        fireEvent.click(screen.getByRole('button', { name: enActivity.feed.forget }));
        expect(await screen.findByText(/Nothing else was made from it\.$/)).toBeInTheDocument();
    });
});
