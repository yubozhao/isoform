import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import '@/i18n';
import enActivity from '@/i18n/locales/en/activity.json';
import { ActivityFeed, FEED_FOCUS_RELOAD_MS } from './activity-feed';
import { resetActivitySessionCache } from './timeline/activity-session-cache';
import { resetPageQueries } from '@/lib/page-queries';

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
    beforeEach(() => {
        fixture.bindings.clear();
        resetActivitySessionCache();
        resetPageQueries();
        // The one-conversation cut refuses by default here, so the tests
        // below exercise the whole-hours Forget it falls back to (A8 interim).
        binding('GetWatchConversationForgetPreview').mockRejectedValue(new Error('recording does not keep exact speech boundaries'));
    });

    // computer-conformance-answers A8 and A10: Forget cuts one conversation
    // out of its hours when it can; a claim from the same hour is named and
    // goes; the hour's other conversation isn't listed, because it stays.
    it('cuts one conversation out of its hours, naming a claim from the same hour', async () => {
        binding('GetActivityFeed').mockResolvedValueOnce([
            { id: 'dev:1', kind: 'watch', startMs: at(10), endMs: at(10, 36), title: 'Pricing with Sam', conversation: true, basis: { setting: 'watch' } },
            { id: 'dev:2', kind: 'watch', startMs: at(10, 40), endMs: at(10, 55), title: 'Standup', conversation: true, basis: { setting: 'watch' } },
        ]).mockResolvedValue([
            { id: 'dev:2', kind: 'watch', startMs: at(10, 40), endMs: at(10, 55), title: 'Standup', conversation: true, basis: { setting: 'watch' } },
        ]);
        binding('GetWatchConversationForgetPreview').mockResolvedValue({
            id: 'dev:1', name: 'Pricing with Sam', lineCount: 40, audio: [], hasNote: true,
            followups: [{ id: 'f1', title: 'Reply to Sam' }],
            claims: [{ id: 'c1', title: 'Sam owns pricing' }],
            sameHourClaims: [{ id: 'c2', title: 'Bo prefers mornings' }],
            claimsLosingSupport: [], stayingWithAgents: ['Claude'], count: 5,
        });
        binding('ForgetWatchConversation').mockResolvedValue(undefined);
        await act(async () => {
            render(<ActivityFeed date={day} />);
        });
        fireEvent.click(await screen.findByRole('button', { name: 'Forget: Pricing with Sam' }));
        const dialog = await screen.findByRole('dialog');
        expect(within(dialog).getByText('Forget "Pricing with Sam"?')).toBeInTheDocument();
        expect(within(dialog).queryByText(/shares its hours/)).toBeNull();
        const plan = within(dialog).getByTestId('forget-plan');
        expect([...plan.querySelectorAll('ul:not([data-testid]) li')].map((li) => li.textContent)).toEqual([
            'the notes made from it · Watch',
            'Reply to Sam · Your work',
            '"Sam owns pricing" · You · About you',
            'A claim from the same hour: "Bo prefers mornings" · You · About you',
        ]);
        expect(within(plan).queryByText(/Standup/)).toBeNull();
        expect(within(plan).getByTestId('forget-staying')).toHaveTextContent('Sent to Claude · stays with Claude');
        fireEvent.click(within(dialog).getByRole('button', { name: 'Forget all 5' }));
        await waitFor(() => expect(binding('ForgetWatchConversation')).toHaveBeenCalledWith('dev:1'));
        expect(binding('ForgetActivityFeedRow')).not.toHaveBeenCalled();
        expect(binding('GetActivityForgetPreview')).not.toHaveBeenCalled();
        await waitFor(() => expect(screen.queryByText('Pricing with Sam')).toBeNull());
        expect(screen.getByText('Standup')).toBeInTheDocument();
    });

    it('falls back to the whole hours when the cut refuses, and confirms with that Forget', async () => {
        binding('GetActivityFeed').mockResolvedValue([
            { id: 'dev:1', kind: 'watch', startMs: at(10), endMs: at(10, 36), title: 'Pricing with Sam', conversation: true, basis: { setting: 'watch' } },
        ]);
        binding('GetActivityForgetPreview').mockResolvedValue({ kind: 'watch', id: 'dev:1', title: 'Pricing with Sam', goes: [{ kind: 'conversation', title: 'Standup' }, { kind: 'copies' }] });
        binding('ForgetActivityFeedRow').mockResolvedValue(undefined);
        await act(async () => {
            render(<ActivityFeed date={day} />);
        });
        fireEvent.click(await screen.findByRole('button', { name: 'Forget: Pricing with Sam' }));
        const dialog = await screen.findByRole('dialog');
        expect(within(dialog).getByText('Forget "Pricing with Sam"? It shares its hours with "Standup", which goes too.')).toBeInTheDocument();
        fireEvent.click(within(dialog).getByRole('button', { name: 'Forget all 2' }));
        await waitFor(() => expect(binding('ForgetActivityFeedRow')).toHaveBeenCalledWith('watch', 'dev:1'));
        expect(binding('ForgetWatchConversation')).not.toHaveBeenCalled();
    });

    it('says a conversation is no longer kept when the cut says so, without falling back', async () => {
        binding('GetActivityFeed').mockResolvedValue([
            { id: 'dev:1', kind: 'watch', startMs: at(10), endMs: at(10, 36), title: 'Pricing with Sam', conversation: true, basis: { setting: 'watch' } },
        ]);
        binding('GetWatchConversationForgetPreview').mockRejectedValue('this conversation is no longer kept');
        await act(async () => {
            render(<ActivityFeed date={day} />);
        });
        fireEvent.click(await screen.findByRole('button', { name: 'Forget: Pricing with Sam' }));
        expect(await screen.findByText(enActivity.feed.forgetNoLonger)).toBeInTheDocument();
        expect(binding('GetActivityForgetPreview')).not.toHaveBeenCalled();
        expect(screen.queryByRole('dialog')).toBeNull();
    });

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
        expect(screen.getByText('Screen · Codex')).toBeInTheDocument();
        expect(screen.getByText('Your watch')).toBeInTheDocument();
        expect(screen.getByText('Screen · Mail')).toBeInTheDocument();
        const gap = document.querySelector('[data-feed-kind="closed"]') as HTMLElement;
        expect(gap.textContent).toMatch(/nothing was kept$/);
        expect(gap.querySelector('button')).toBeNull();
        // Forget on each of the three rows, and the screen row's ••• (Remind me…, T12).
        expect(document.querySelectorAll('[data-feed-kind] button')).toHaveLength(4);
        // pass-access v1.0.0: each Forget says which row it forgets.
        expect(screen.getByRole('button', { name: 'Forget: Pricing page copy' })).toBeInTheDocument();
        // No coloured bar at a row's left edge: the source is the dot and its name.
        for (const row of document.querySelectorAll('[data-feed-kind]')) {
            expect(row.className).not.toMatch(/border-l|before:|after:/);
        }
    });

    // Conformance answers v1.1.0 P1, computer-theme v1.0.0 rule 3: an agent
    // run the screen saw is a Screen row, "Screen · {agent}", with the line
    // saying it worked on its own, and the screen's indigo dot; hand-offs keep
    // the plum of Your agents; Closed Eyes is a striped warning dot. No green
    // and no copper anywhere in the feed.
    it('shows screen-seen agent runs as Screen rows, with the theme\'s source dots', async () => {
        binding('GetActivityFeed').mockResolvedValue([
            { id: 'run-1', kind: 'agent', startMs: at(15), endMs: at(15, 1), activeMs: 60_000, app: 'Claude', basis: { setting: 'screen', agent: 'Claude' } },
            { id: 'run-2', kind: 'agent', startMs: at(14), endMs: at(14, 1), activeMs: 60_000, basis: { setting: 'screen' } },
            { id: 'h-1', kind: 'handoff', startMs: at(13), endMs: at(13), app: 'Codex', title: 'Write the test plan', outcome: 'sent', basis: { setting: 'yes' } },
            { id: '', kind: 'closed', startMs: at(12), endMs: at(12, 30), basis: { setting: 'closed_eyes' } },
            { id: 'rec-1', kind: 'watch', startMs: at(10), endMs: at(10, 36), words: 4, basis: { setting: 'watch' } },
            { id: 'm-1', kind: 'screen', startMs: at(9), endMs: at(9, 30), title: 'Pricing page copy', basis: { setting: 'screen' } },
        ]);
        await act(async () => {
            render(<ActivityFeed date={day} />);
        });
        await screen.findByText(enActivity.act.agentOnScreen.replace('{{agent}}', 'Claude'));
        expect(screen.getByText('Screen · Claude')).toBeInTheDocument();
        expect(screen.getByText(enActivity.act.agentOnScreenUnnamed)).toBeInTheDocument();
        expect(screen.queryByText(/Seen on your screen/)).toBeNull();
        const dot = (kind: string) => [...document.querySelectorAll(`[data-feed-kind="${kind}"] [data-feed-dot]`)].map((d) => d.className);
        for (const c of dot('agent')) expect(c).toMatch(/\bbg-app-indigo-text\b/);
        for (const c of dot('screen')) expect(c).toMatch(/\bbg-app-indigo-text\b/);
        expect(dot('handoff')[0]).toMatch(/\bbg-app-plum-text\b/);
        expect(dot('watch')[0]).toMatch(/\bbg-accent\b/);
        const closed = document.querySelector('[data-feed-kind="closed"] [data-closed-dot]') as HTMLElement;
        expect(closed.className).toMatch(/\bborder-warning\b/);
        expect(closed.getAttribute('style')).toMatch(/repeating-linear-gradient/);
        expect(closed.getAttribute('style')).toMatch(/--color-warning/);
        for (const el of document.querySelectorAll('[data-feed-kind] *')) {
            expect(el.getAttribute('class') ?? '').not.toMatch(/seaweed|success|green|copper/);
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
        await screen.findByText('Screen · Claude');
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

    // computer-activity v1.2.0, Activity › Agents: a hand-off says what Isoform
    // knows the agent did, and Forget removes only Isoform's record of it.
    it('shows a hand-off with what the agent did, and Forget keeps the agent\'s work', async () => {
        binding('GetActivityFeed').mockResolvedValue([
            { id: 'h-1', kind: 'handoff', startMs: at(9), endMs: at(9), app: 'Codex', title: 'Write the test plan', surface: 'computer', outcome: 'done', doneMs: at(11) },
            { id: 'h-2', kind: 'handoff', startMs: at(8), endMs: at(8), app: 'Claude', title: 'Reply to Sam', surface: 'phone', outcome: 'no_word' },
        ]);
        binding('GetActivityForgetPreview').mockResolvedValue({ kind: 'handoff', id: 'h-1', title: 'Write the test plan', goes: [], agent: 'Codex' });
        binding('ForgetActivityFeedRow').mockResolvedValue(undefined);
        await act(async () => {
            render(<ActivityFeed date={day} kinds={['handoff', 'agent']} />);
        });
        expect(await screen.findByText('Write the test plan')).toBeInTheDocument();
        expect(screen.getAllByText('Your agents · Codex')).toHaveLength(1);
        expect(screen.getByText(/^Handed over by your Yes · back at 11:00\s?AM$/)).toBeInTheDocument();
        expect(screen.getByText('Handed over by your Yes on your phone · no word yet')).toBeInTheDocument();
        const row = screen.getByText('Write the test plan').closest('li') as HTMLElement;
        fireEvent.click(within(row).getByRole('button', { name: 'Forget: Write the test plan' }));
        const dialog = await screen.findByRole('dialog');
        expect(within(dialog).getByText('Forget your record of "Write the test plan"? What Codex made stays with Codex.')).toBeInTheDocument();
        expect(within(dialog).queryByRole('listitem')).toBeNull();
        fireEvent.click(within(dialog).getByRole('button', { name: enActivity.act.forgetOne }));
        await waitFor(() => expect(binding('ForgetActivityFeedRow')).toHaveBeenCalledWith('handoff', 'h-1'));
    });

    // computer-trouble v1.0.0 rule 7: the hours trouble stopped Isoform are a
    // gap with a dashed dot and its cause, and no Forget.
    it('keeps the hours trouble stopped Isoform as a gap with its cause', async () => {
        binding('GetActivityFeed').mockResolvedValue([
            { id: '', kind: 'trouble', startMs: at(16), endMs: at(18, 40), cause: 'screen' },
            { id: '', kind: 'trouble', startMs: at(17, 3), endMs: at(17, 13), cause: 'restarted' },
            { id: '', kind: 'trouble', startMs: at(20), endMs: at(21), cause: 'offline', ongoing: true },
        ]);
        await act(async () => {
            render(<ActivityFeed date={day} />);
        });
        const gaps = await screen.findAllByText(/^Not seen|^Isoform restarted/);
        expect(gaps.map((g) => g.textContent)).toEqual([
            expect.stringMatching(/^Not seen, until 6:40\s?PM$/),
            expect.stringMatching(/^Isoform restarted · not seen, 5:03\s?PM to 5:13\s?PM$/),
            'Not seen, since then',
        ]);
        expect(screen.getByText('Screen Recording was off for Isoform. Nothing from these hours is in a brief.')).toBeInTheDocument();
        expect(screen.getByText('There was no internet. Nothing from these hours is in a brief.')).toBeInTheDocument();
        expect(document.querySelectorAll('[data-feed-kind="trouble"] button')).toHaveLength(0);
        expect(document.querySelectorAll('[data-trouble-dot]')).toHaveLength(3);
    });

    // Hub class rule, 2026-11-21: every row names its consent, from the
    // backend; the agent line is built from it, and a learned row says why.
    it('carries each row\'s consent on data-feed-basis and builds the agent line from it', async () => {
        binding('GetActivityFeed').mockResolvedValue([
            { id: 'run-1', kind: 'agent', startMs: at(15), endMs: at(15, 1), activeMs: 60_000, app: 'Codex', basis: { setting: 'screen', agent: 'Claude' } },
            { id: 'learn-1', kind: 'agent', startMs: at(14), endMs: at(14, 1), activeMs: 60_000, app: 'Codex', basis: { setting: 'learn', agent: 'Codex' } },
            { id: '', kind: 'closed', startMs: at(12), endMs: at(13), basis: { setting: 'closed_eyes' } },
            { id: '', kind: 'trouble', startMs: at(11), endMs: at(11, 30), cause: 'signed_out', basis: { setting: 'assistant' } },
            { id: 'rec-1', kind: 'watch', startMs: at(10), endMs: at(10, 36), words: 4, basis: { setting: 'watch' } },
            { id: 'm-1', kind: 'screen', startMs: at(9), endMs: at(9, 30), title: 'Pricing page copy', detail: 'Rewrote the plans table', basis: { setting: 'screen' } },
            { id: 'h-1', kind: 'handoff', startMs: at(8), endMs: at(8), app: 'Codex', title: 'Write the plan', basis: { setting: 'yes' } },
        ]);
        await act(async () => {
            render(<ActivityFeed date={day} />);
        });
        await screen.findByText('Pricing page copy');
        const bases = [...document.querySelectorAll('[data-feed-kind]')].map((row) => row.getAttribute('data-feed-basis'));
        expect(bases).toEqual(['screen', 'learn', 'closed_eyes', 'assistant', 'watch', 'screen', 'yes']);
        // The agent the consent names, not the row's app, is in the line.
        expect(screen.getByText('Claude working on its own · not counted as your work')).toBeInTheDocument();
        expect(screen.getByText('read because Learn from it is on for Codex')).toBeInTheDocument();
        // Screen and watch rows get no basis line: the design drew none.
        expect(screen.queryByText(/Learn from it/, { selector: '[data-feed-kind="screen"] *' })).toBeNull();
        expect(screen.getByText('Rewrote the plans table')).toBeInTheDocument();
    });

    // computer-activity v1.0.0 rule 4: a watch row is its conversation, by
    // name and length, who spoke, its marks and note; opening it opens the
    // conversation, and Forget on it does not.
    it('shows a watch conversation by name, length, who spoke, marks and note, and opens it', async () => {
        binding('GetActivityFeed').mockResolvedValue([
            { id: 'dev:1', kind: 'watch', startMs: at(10), endMs: at(10, 36), title: 'Pricing with Sam', conversation: true,
                speakers: ['you', 'Sam'], otherVoices: 0, marks: 1, hasNote: true, device: 'watch', basis: { setting: 'watch' } },
            { id: 'dev:2', kind: 'watch', startMs: at(9), endMs: at(9, 12), conversation: true,
                speakers: ['you'], otherVoices: 2, marks: 3, device: 'watch', basis: { setting: 'watch' } },
            { id: 'rec-3', kind: 'watch', startMs: at(8), endMs: at(9), words: 0, device: 'watch', basis: { setting: 'watch' } },
        ]);
        binding('GetActivityForgetPreview').mockResolvedValue({ kind: 'watch', id: 'dev:1', title: 'Pricing with Sam', goes: [] });
        const opened = vi.fn();
        await act(async () => {
            render(<ActivityFeed date={day} onOpenConversation={opened} />);
        });
        expect(await screen.findByText('Pricing with Sam')).toBeInTheDocument();
        expect(screen.getByText('36m · You, Sam · 1 mark · note')).toBeInTheDocument();
        // Untitled: the length is the title; unnamed voices are a count, never "Speaker 2".
        expect(screen.getByText('Conversation · 12m')).toBeInTheDocument();
        expect(screen.getByText('You, 2 others · 3 marks')).toBeInTheDocument();
        expect(screen.queryByText(/Speaker/)).toBeNull();
        // An hour where nothing was said keeps its line, and does not open.
        expect(screen.getByText(enActivity.feed.watchNoWords)).toBeInTheDocument();
        expect(document.querySelectorAll('[data-feed-open]')).toHaveLength(2);
        fireEvent.click(screen.getByRole('button', { name: 'Forget: Pricing with Sam' }));
        await screen.findByRole('dialog');
        expect(opened).not.toHaveBeenCalled();
        fireEvent.keyDown(screen.getByRole('dialog'), { key: 'Escape' });
        fireEvent.click(screen.getByRole('button', { name: 'Open: Pricing with Sam' }));
        expect(opened).toHaveBeenCalledWith('dev:1', expect.any(Number));
    });

    // computer-activity v1.1.0, 15e: Forget on a conversation lists its note,
    // the conversation sharing its hour, the follow-up and the claim made
    // from it with where each lives; work an agent has stays and isn't counted.
    it('lists everything a watch conversation takes with it, and what stays with an agent', async () => {
        binding('GetActivityFeed').mockResolvedValueOnce([
            { id: 'dev:1', kind: 'watch', startMs: at(10), endMs: at(10, 36), title: 'Pricing with Sam', conversation: true,
                speakers: ['you', 'Sam'], hasNote: true, device: 'watch', basis: { setting: 'watch' } },
        ]).mockResolvedValue([]);
        binding('GetActivityForgetPreview').mockResolvedValue({
            kind: 'watch', id: 'dev:1', title: 'Pricing with Sam',
            goes: [
                { kind: 'words' }, { kind: 'audio' }, { kind: 'notes' },
                { kind: 'conversation', title: 'Standup' },
                { kind: 'conversation', startMs: at(10, 40), endMs: at(10, 55) },
                { kind: 'followup', title: 'Reply to Sam' },
                { kind: 'claim', title: 'Sam owns pricing' },
                { kind: 'agent_work', title: 'Claude' },
                { kind: 'copies' },
            ],
        });
        binding('ForgetActivityFeedRow').mockResolvedValue(undefined);
        await act(async () => {
            render(<ActivityFeed date={day} />);
        });
        fireEvent.click(await screen.findByRole('button', { name: 'Forget: Pricing with Sam' }));
        const dialog = await screen.findByRole('dialog');
        // A8 interim: it shares its hours, so the title says the other goes too.
        expect(within(dialog).getByText('Forget "Pricing with Sam"? It shares its hours with "Standup", which goes too.')).toBeInTheDocument();
        const plan = within(dialog).getByTestId('forget-plan');
        expect([...plan.querySelectorAll('ul:not([data-testid]) li')].map((li) => li.textContent)).toEqual([
            'the notes made from it · Watch',
            'the conversation Standup, in the same hours · Watch',
            expect.stringMatching(/^the conversation from 10:40\s?AM to 10:55\s?AM, in the same hours · Watch$/),
            'Reply to Sam · Your work',
            '"Sam owns pricing" · You · About you',
        ]);
        expect(within(plan).getByTestId('forget-staying')).toHaveTextContent('Sent to Claude · stays with Claude');
        // Five made from it plus the conversation itself; the agent's work isn't counted.
        fireEvent.click(within(dialog).getByRole('button', { name: 'Forget all 6' }));
        await waitFor(() => expect(binding('ForgetActivityFeedRow')).toHaveBeenCalledWith('watch', 'dev:1'));
        await waitFor(() => expect(screen.queryByText('Pricing with Sam')).toBeNull());
    });

    // A failed read says so with Try again; a row that changed under Forget
    // says it is no longer kept and reads the list again.
    it('says a failed Forget read plainly, and a conversation no longer kept', async () => {
        binding('GetActivityFeed').mockResolvedValue([
            { id: 'dev:1', kind: 'watch', startMs: at(10), endMs: at(10, 36), title: 'Pricing with Sam', conversation: true, basis: { setting: 'watch' } },
        ]);
        binding('GetActivityForgetPreview')
            .mockRejectedValueOnce(new Error('store down'))
            .mockRejectedValueOnce('that conversation is no longer kept');
        await act(async () => {
            render(<ActivityFeed date={day} />);
        });
        fireEvent.click(await screen.findByRole('button', { name: 'Forget: Pricing with Sam' }));
        expect(await screen.findByText(enActivity.feed.forgetFailed)).toBeInTheDocument();
        const reads = binding('GetActivityFeed').mock.calls.length;
        fireEvent.click(screen.getByRole('button', { name: 'Forget: Pricing with Sam' }));
        expect(await screen.findByText(enActivity.feed.forgetNoLonger)).toBeInTheDocument();
        await waitFor(() => expect(binding('GetActivityFeed').mock.calls.length).toBeGreaterThan(reads));
        expect(screen.queryByRole('dialog')).toBeNull();
    });

    it('says when the day cannot be read, with Try again', async () => {
        binding('GetActivityFeed').mockRejectedValueOnce(new Error('db locked')).mockResolvedValue([]);
        await act(async () => {
            render(<ActivityFeed date={day} />);
        });
        expect(await screen.findByText(enActivity.feed.loadFailed)).toBeInTheDocument();
        expect(screen.queryByText(enActivity.feed.empty)).toBeNull();
    });

    // Conformance answers v1.1.0 A8, interim: the shared-hour title names the
    // other conversation (by its time when unnamed); with no sibling, the plain title.
    it('titles Forget of a conversation sharing its hours, and only then', async () => {
        binding('GetActivityFeed').mockResolvedValue([
            { id: 'dev:1', kind: 'watch', startMs: at(10), endMs: at(10, 36), title: 'Pricing with Sam', conversation: true, basis: { setting: 'watch' } },
            { id: 'dev:2', kind: 'watch', startMs: at(8), endMs: at(8, 20), title: 'Call with Lena', conversation: true, basis: { setting: 'watch' } },
        ]);
        binding('GetActivityForgetPreview')
            .mockResolvedValueOnce({ kind: 'watch', id: 'dev:1', title: 'Pricing with Sam', goes: [{ kind: 'conversation', startMs: at(10, 40), endMs: at(10, 55) }, { kind: 'copies' }] })
            .mockResolvedValueOnce({ kind: 'watch', id: 'dev:2', title: 'Call with Lena', goes: [{ kind: 'audio' }, { kind: 'copies' }] });
        await act(async () => {
            render(<ActivityFeed date={day} />);
        });
        fireEvent.click(await screen.findByRole('button', { name: 'Forget: Pricing with Sam' }));
        let dialog = await screen.findByRole('dialog');
        expect(within(dialog).getByText(/^Forget "Pricing with Sam"\? It shares its hours with "10:40\s?AM to 10:55\s?AM", which goes too\.$/)).toBeInTheDocument();
        expect(within(dialog).getByRole('button', { name: 'Forget all 2' })).toBeInTheDocument();
        fireEvent.keyDown(dialog, { key: 'Escape' });
        await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
        fireEvent.click(screen.getByRole('button', { name: 'Forget: Call with Lena' }));
        dialog = await screen.findByRole('dialog');
        expect(within(dialog).getByText('Forget "Call with Lena"?')).toBeInTheDocument();
        expect(within(dialog).queryByText(/shares its hours/)).toBeNull();
    });

    // B7: computer-conformance-answers v1.0.0 A1 and A2.
    describe('topics and the ••• menu', () => {
        const rows = [
            { id: 'm-1', kind: 'screen', startMs: at(11), endMs: at(11, 30), title: 'Import script', detail: '3 files in halcyon/import', basis: { setting: 'screen' } },
            { id: 'm-2', kind: 'screen', startMs: at(10), endMs: at(10, 30), title: 'Pricing page copy', detail: 'Rewrote the plans table', basis: { setting: 'screen' } },
            { id: 'm-3', kind: 'screen', startMs: at(9), endMs: at(9, 30), title: 'Docs', detail: 'Read the guide', basis: { setting: 'screen' } },
            { id: 'm-4', kind: 'screen', startMs: at(8), endMs: at(8, 30), title: 'Standup', basis: { setting: 'screen' } },
        ];
        const details = [
            { kind: 'screen', id: 'm-1', topicId: 't-1', topicName: 'Halcyon import', open: { kind: 'file', label: 'import.go' } },
            { kind: 'screen', id: 'm-2', topicId: 't-2', topicName: 'Pricing' },
            { kind: 'screen', id: 'm-3', open: { kind: 'page', label: 'docs.example.com' } },
            { kind: 'screen', id: 'm-4', open: { kind: 'call', label: 'Recording' } },
        ];
        const row = (title: string) => screen.getByText(title).closest('li') as HTMLElement;
        const openMenu = async (title: string) => {
            fireEvent.pointerDown(within(row(title)).getByRole('button', { name: `More for ${title}` }), { button: 0, pointerType: 'mouse' });
            return screen.findByRole('menu');
        };

        beforeEach(() => {
            binding('GetActivityFeed').mockResolvedValue(rows);
            binding('GetActivityRowDetails').mockResolvedValue(details);
        });

        it('starts the line under a row with its topic in grey, and opens the topic on click', async () => {
            const openTopic = vi.fn();
            await act(async () => {
                render(<ActivityFeed date={day} onOpenTopic={openTopic} />);
            });
            await screen.findByText('Halcyon import');
            const line = row('Import script').querySelector('[data-feed-line]') as HTMLElement;
            expect(line.textContent).toBe('Halcyon import · 3 files in halcyon/import');
            const topic = within(row('Import script')).getByRole('button', { name: 'Halcyon import' });
            expect(topic.className).toMatch(/\btext-muted\b/);
            expect(topic.className).not.toMatch(/accent/);
            // A row with no topic has no topic text.
            expect(row('Docs').querySelector('[data-feed-topic]')).toBeNull();
            expect(row('Docs').querySelector('[data-feed-line]')!.textContent).toBe('Read the guide');
            fireEvent.click(topic);
            await waitFor(() => expect(openTopic).toHaveBeenCalledWith({ id: 't-1', name: 'Halcyon import', kind: 'work', pinned: false }));
        });

        it('shows ••• only with something in it, Not this topic only with a topic, and keeps Forget its own button', async () => {
            await act(async () => {
                render(<ActivityFeed date={day} />);
            });
            await screen.findByText('Halcyon import');
            let menu = await openMenu('Import script');
            expect(within(menu).getAllByRole('menuitem').map((item) => item.textContent)).toEqual(['Not this topic', 'Open import.go', 'Remind me…']);
            fireEvent.keyDown(menu, { key: 'Escape' });
            await waitFor(() => expect(screen.queryByRole('menu')).toBeNull());
            menu = await openMenu('Pricing page copy');
            expect(within(menu).getAllByRole('menuitem').map((item) => item.textContent)).toEqual(['Not this topic', 'Remind me…']);
            fireEvent.keyDown(menu, { key: 'Escape' });
            await waitFor(() => expect(screen.queryByRole('menu')).toBeNull());
            menu = await openMenu('Docs');
            expect(within(menu).getAllByRole('menuitem').map((item) => item.textContent)).toEqual(['Open the page', 'Remind me…']);
            fireEvent.keyDown(menu, { key: 'Escape' });
            await waitFor(() => expect(screen.queryByRole('menu')).toBeNull());
            menu = await openMenu('Standup');
            expect(within(menu).getAllByRole('menuitem').map((item) => item.textContent)).toEqual(['Open the recording', 'Remind me…']);
            fireEvent.keyDown(menu, { key: 'Escape' });
            await waitFor(() => expect(screen.queryByRole('menu')).toBeNull());
            for (const title of ['Import script', 'Pricing page copy', 'Docs', 'Standup']) {
                expect(within(row(title)).getByRole('button', { name: `Forget: ${title}` })).toBeInTheDocument();
            }
        });

        it('shows no ••• on a row with nothing in it, and Remind me… alone on a bare screen row', async () => {
            binding('GetActivityFeed').mockResolvedValue([
                rows[3],
                { id: 'rec-1', kind: 'watch', startMs: at(7), endMs: at(8), words: 0, basis: { setting: 'watch' } },
            ]);
            binding('GetActivityRowDetails').mockResolvedValue([]);
            await act(async () => {
                render(<ActivityFeed date={day} />);
            });
            await screen.findByText('Standup');
            expect(document.querySelector('[data-feed-kind="watch"] [data-feed-more]')).toBeNull();
            const menu = await openMenu('Standup');
            expect(within(menu).getAllByRole('menuitem').map((item) => item.textContent)).toEqual(['Remind me…']);
        });

        it('takes a row out of its topic at once with Not this topic', async () => {
            binding('NotThisTopic').mockResolvedValue(undefined);
            await act(async () => {
                render(<ActivityFeed date={day} />);
            });
            await screen.findByText('Pricing');
            const menu = await openMenu('Pricing page copy');
            fireEvent.click(within(menu).getByRole('menuitem', { name: 'Not this topic' }));
            await waitFor(() => expect(row('Pricing page copy').querySelector('[data-feed-topic]')).toBeNull());
            expect(binding('NotThisTopic')).toHaveBeenCalledWith('screen', 'm-2', 't-2');
            // Without a topic the row keeps its ••• for Remind me… (T12).
            const after = await openMenu('Pricing page copy');
            expect(within(after).getAllByRole('menuitem').map((item) => item.textContent)).toEqual(['Remind me…']);
        });

        it('puts the topic back with Try again when Not this topic fails', async () => {
            binding('NotThisTopic').mockRejectedValueOnce(new Error('disk full')).mockResolvedValueOnce(undefined);
            await act(async () => {
                render(<ActivityFeed date={day} />);
            });
            await screen.findByText('Pricing');
            const menu = await openMenu('Pricing page copy');
            fireEvent.click(within(menu).getByRole('menuitem', { name: 'Not this topic' }));
            const alert = await screen.findByRole('alert');
            expect(alert).toHaveTextContent(enActivity.feed.notThisTopicFailed);
            expect(row('Pricing page copy').querySelector('[data-feed-topic]')).toHaveTextContent('Pricing');
            fireEvent.click(within(alert).getByRole('button', { name: 'Try again' }));
            await waitFor(() => expect(row('Pricing page copy').querySelector('[data-feed-topic]')).toBeNull());
            expect(binding('NotThisTopic')).toHaveBeenCalledTimes(2);
            expect(screen.queryByRole('alert')).toBeNull();
        });

        it('opens a page or file where it happened, and a call in the Screen timeline', async () => {
            const openRecording = vi.fn();
            binding('OpenActivityRow').mockImplementation((_kind: string, id: string) =>
                id === 'm-4' ? Promise.reject('activity_open_in_app:rec-42') : Promise.resolve(undefined));
            await act(async () => {
                render(<ActivityFeed date={day} onOpenRecording={openRecording} />);
            });
            await screen.findByText('Halcyon import');
            let menu = await openMenu('Docs');
            fireEvent.click(within(menu).getByRole('menuitem', { name: 'Open the page' }));
            await waitFor(() => expect(binding('OpenActivityRow')).toHaveBeenCalledWith('screen', 'm-3'));
            await waitFor(() => expect(screen.queryByRole('menu')).toBeNull());
            menu = await openMenu('Standup');
            fireEvent.click(within(menu).getByRole('menuitem', { name: 'Open the recording' }));
            await waitFor(() => expect(openRecording).toHaveBeenCalledWith('rec-42'));
            expect(screen.queryByRole('alert')).toBeNull();
        });

        it('says when a row could not be opened, with Try again', async () => {
            binding('OpenActivityRow').mockRejectedValueOnce(new Error('no such file')).mockResolvedValueOnce(undefined);
            await act(async () => {
                render(<ActivityFeed date={day} />);
            });
            await screen.findByText('Halcyon import');
            const menu = await openMenu('Import script');
            fireEvent.click(within(menu).getByRole('menuitem', { name: 'Open import.go' }));
            const alert = await screen.findByRole('alert');
            expect(alert).toHaveTextContent(enActivity.feed.openWhereFailed);
            fireEvent.click(within(alert).getByRole('button', { name: 'Try again' }));
            await waitFor(() => expect(binding('OpenActivityRow')).toHaveBeenCalledTimes(2));
        });

        it('says when the topics could not be read, never as rows without topics', async () => {
            binding('GetActivityRowDetails').mockRejectedValueOnce(new Error('timeout')).mockResolvedValueOnce(details);
            await act(async () => {
                render(<ActivityFeed date={day} />);
            });
            const alert = await screen.findByRole('alert');
            expect(alert).toHaveTextContent(enActivity.feed.detailsFailed);
            expect(screen.getByText('Import script')).toBeInTheDocument();
            fireEvent.click(within(alert).getByRole('button', { name: 'Try again' }));
            await screen.findByText('Halcyon import');
            expect(screen.queryByRole('alert')).toBeNull();
        });
    });

    // Design T12: Remind me… is the third ••• entry, on screen rows, with
    // the opened row's flow (GetActivityReminderChoices, SetActivityReminder,
    // CancelActivityReminder).
    describe('Remind me…', () => {
        const moment = { id: 'm-1', kind: 'screen', startMs: at(9), endMs: at(9, 30), title: 'Pricing page copy', basis: { setting: 'screen' } };
        const row = () => screen.getByText('Pricing page copy').closest('li') as HTMLElement;
        const openMenu = async () => {
            fireEvent.pointerDown(within(row()).getByRole('button', { name: 'More for Pricing page copy' }), { button: 0, pointerType: 'mouse' });
            return screen.findByRole('menu');
        };
        const openRemindMe = async () => {
            const menu = await openMenu();
            const trigger = within(menu).getByRole('menuitem', { name: 'Remind me…' });
            trigger.focus();
            fireEvent.keyDown(trigger, { key: 'ArrowRight' });
            return (await screen.findAllByRole('menu')).at(-1)!;
        };
        const tomorrow = new Date();
        tomorrow.setDate(tomorrow.getDate() + 1);
        tomorrow.setHours(9, 0, 0, 0);

        beforeEach(() => {
            binding('GetActivityFeed').mockResolvedValue([moment]);
            binding('GetActivityRowDetails').mockResolvedValue([]);
            binding('GetActivityReminderChoices').mockResolvedValue([{ id: 'tomorrow_morning', atMs: tomorrow.getTime() }]);
        });

        it('sets a reminder from the quick times', async () => {
            binding('SetActivityReminder').mockResolvedValue(tomorrow.getTime());
            await act(async () => {
                render(<ActivityFeed date={day} />);
            });
            await screen.findByText('Pricing page copy');
            const sub = await openRemindMe();
            const items = await within(sub).findAllByRole('menuitem');
            expect(items.map((item) => item.textContent)).toEqual([expect.stringMatching(/^Tomorrow morning, /), 'Pick a time']);
            fireEvent.click(items[0]);
            await waitFor(() => expect(binding('SetActivityReminder')).toHaveBeenCalledWith('m-1', tomorrow.getTime()));
            expect(await within(row()).findByText(/^Reminder set for /)).toBeInTheDocument();
        });

        it('shows a pending reminder\'s time and Cancel in the •••', async () => {
            binding('GetActivitiesInRange').mockResolvedValue([{ id: 'm-1', reminderAt: tomorrow.getTime() }]);
            binding('CancelActivityReminder').mockResolvedValue(undefined);
            await act(async () => {
                render(<ActivityFeed date={day} />);
            });
            await screen.findByText('Pricing page copy');
            await waitFor(() => expect(binding('GetActivitiesInRange')).toHaveBeenCalled());
            const menu = await openMenu();
            expect(within(menu).getByText(/^Reminder set for tomorrow at /)).toBeInTheDocument();
            expect(within(menu).queryByRole('menuitem', { name: 'Remind me…' })).toBeNull();
            fireEvent.click(within(menu).getByRole('menuitem', { name: 'Cancel reminder' }));
            await waitFor(() => expect(binding('CancelActivityReminder')).toHaveBeenCalledWith('m-1'));
            expect(await within(row()).findByText('Reminder cancelled.')).toBeInTheDocument();
        });

        it('says when the reminder could not be set, with Try again', async () => {
            binding('SetActivityReminder').mockRejectedValueOnce(new Error('off')).mockResolvedValueOnce(tomorrow.getTime());
            await act(async () => {
                render(<ActivityFeed date={day} />);
            });
            await screen.findByText('Pricing page copy');
            const sub = await openRemindMe();
            fireEvent.click((await within(sub).findAllByRole('menuitem'))[0]);
            const alert = await within(row()).findByRole('alert');
            expect(alert).toHaveTextContent(enActivity.rowActions.reminderFailed);
            fireEvent.click(within(alert).getByRole('button', { name: 'Try again' }));
            await waitFor(() => expect(binding('SetActivityReminder')).toHaveBeenCalledTimes(2));
        });

        it('leaves Remind me… off a private or paused moment', async () => {
            binding('GetActivitiesInRange').mockResolvedValue([{ id: 'm-1', actionsWithheld: 'private' }]);
            await act(async () => {
                render(<ActivityFeed date={day} />);
            });
            await screen.findByText('Pricing page copy');
            await waitFor(() => expect(document.querySelector('[data-feed-more]')).toBeNull());
        });

        it('says when the reminders could not be read, never as none pending', async () => {
            binding('GetActivitiesInRange').mockRejectedValue(new Error('timeout'));
            await act(async () => {
                render(<ActivityFeed date={day} />);
            });
            expect(await screen.findByText(enActivity.feed.remindersFailed)).toBeInTheDocument();
        });

        it('shows no Remind me… on watch, agent or hand-off rows', async () => {
            binding('GetActivityFeed').mockResolvedValue([
                { id: 'run-1', kind: 'agent', startMs: at(15), endMs: at(15, 1), activeMs: 60_000, app: 'Claude', basis: { setting: 'screen', agent: 'Claude' } },
                { id: 'h-1', kind: 'handoff', startMs: at(13), endMs: at(13), app: 'Codex', title: 'Write the test plan', outcome: 'sent', basis: { setting: 'yes' } },
                { id: 'rec-1', kind: 'watch', startMs: at(10), endMs: at(10, 36), words: 4, basis: { setting: 'watch' } },
            ]);
            await act(async () => {
                render(<ActivityFeed date={day} />);
            });
            await screen.findByText('Write the test plan');
            expect(document.querySelector('[data-feed-more]')).toBeNull();
        });
    });

    // Design A9 (#1457): with Closed Eyes on, past rows keep their topic and
    // Open; only the Closed Eyes stretch itself has nothing.
    it('keeps the ••• on a past row while Closed Eyes is on', async () => {
        binding('GetActivityFeed').mockResolvedValue([
            { id: '', kind: 'closed', startMs: at(12), endMs: at(13), ongoing: true, basis: { setting: 'closed_eyes' } },
            { id: 'm-1', kind: 'screen', startMs: at(9), endMs: at(9, 30), title: 'Import script', basis: { setting: 'screen' } },
        ]);
        binding('GetActivityRowDetails').mockResolvedValue([
            { kind: 'screen', id: 'm-1', topicId: 't-1', topicName: 'Halcyon import', open: { kind: 'file', label: 'import.go' } },
        ]);
        await act(async () => {
            render(<ActivityFeed date={day} />);
        });
        await screen.findByText('Halcyon import');
        expect(document.querySelector('[data-feed-kind="closed"] [data-feed-more]')).toBeNull();
        const past = screen.getByText('Import script').closest('li') as HTMLElement;
        fireEvent.pointerDown(within(past).getByRole('button', { name: 'More for Import script' }), { button: 0, pointerType: 'mouse' });
        const menu = await screen.findByRole('menu');
        expect(within(menu).getAllByRole('menuitem').map((item) => item.textContent)).toEqual(['Not this topic', 'Open import.go', 'Remind me…']);
    });

    it('reloads on window focus at most once a minute, keeping the rows on screen', async () => {
        let now = 1_000_000;
        const clock = vi.spyOn(Date, 'now').mockImplementation(() => now);
        binding('GetActivityFeed').mockResolvedValue([
            { id: 'm-1', kind: 'screen', startMs: at(9), endMs: at(9, 30), title: 'Pricing page copy', basis: { setting: 'screen' } },
        ]);
        await act(async () => {
            render(<ActivityFeed date={day} />);
        });
        await screen.findByText('Pricing page copy');
        expect(binding('GetActivityFeed')).toHaveBeenCalledTimes(1);
        now += FEED_FOCUS_RELOAD_MS - 1;
        await act(async () => { window.dispatchEvent(new Event('focus')); });
        expect(binding('GetActivityFeed')).toHaveBeenCalledTimes(1);
        now += 1;
        await act(async () => { window.dispatchEvent(new Event('focus')); });
        expect(binding('GetActivityFeed')).toHaveBeenCalledTimes(2);
        expect(screen.getByText('Pricing page copy')).toBeInTheDocument();
        clock.mockRestore();
    });
});
