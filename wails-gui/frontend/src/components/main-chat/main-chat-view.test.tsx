import { resetPageQueries } from '@/lib/page-queries';
import { act, cleanup, fireEvent, render, renderHook, screen, waitFor, within } from '@testing-library/react';
import { Profiler, useState } from 'react';
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest';
import { chatThreadUpdates } from '@/lib/main-chat-view';
import i18n from '@/i18n';
import en from '@/i18n/locales/en/chat.json';
import zh from '@/i18n/locales/zh-CN/chat.json';
import { MAIN_CHAT_PROACTIVE_TOOL } from '@/lib/stream-helpers';
import { resetMainChatWaitingStore } from '@/lib/main-chat-waiting-store';
import { resetPagePrivacyWithdrawal } from '@/lib/page-privacy-withdrawal';
import { useMainChatView } from '@/hooks/useMainChatView';
import { normalizeMainChatFilter } from '@/lib/main-chat-view';
import { useSidebarTopics } from '@/hooks/useTodayTopics';
import type { StreamItem } from '@/types';
import { MainChatStreamBody } from './main-chat-stream-body';
import { MainChatTopicBox } from './main-chat-topic-view';

// These cover the rollback switch: Isoform's updates back in Chat's thread.
beforeEach(() => { chatThreadUpdates.shown = true; });
afterEach(() => { chatThreadUpdates.shown = false; });


const fixture = vi.hoisted(() => ({
    bindings: new Map<string, ReturnType<typeof vi.fn>>(),
    listeners: new Map<string, Set<(payload: unknown) => void>>(),
}));
vi.mock('@/lib/wails-shim-app', () => new Proxy({}, {
    has: () => true,
    get: (_target, name: string) => {
        if (name === 'then') return undefined;
        if (!fixture.bindings.has(name)) fixture.bindings.set(name, vi.fn().mockResolvedValue({}));
        return fixture.bindings.get(name);
    },
}));
vi.mock('@/lib/wails-shim-runtime', () => ({
    EventsOn: (name: string, handler: (payload: unknown) => void) => {
        const set = fixture.listeners.get(name) ?? new Set();
        set.add(handler);
        fixture.listeners.set(name, set);
        return () => set.delete(handler);
    },
    EventsEmit: vi.fn(),
    ClipboardSetText: vi.fn(),
    LogInfo: vi.fn(),
    LogWarning: vi.fn(),
}));

function binding(name: string) {
    if (!fixture.bindings.has(name)) fixture.bindings.set(name, vi.fn().mockResolvedValue({}));
    return fixture.bindings.get(name)!;
}

async function emit(name: string, payload?: unknown) {
    await act(async () => {
        for (const handler of fixture.listeners.get(name) ?? []) handler(payload);
    });
}

const TODAY = (() => {
    const now = new Date();
    return `${now.getFullYear()}-${String(now.getMonth() + 1).padStart(2, '0')}-${String(now.getDate()).padStart(2, '0')}`;
})();

function waitingCard(id: string): StreamItem {
    return {
        id, kind: 'system', toolName: MAIN_CHAT_PROACTIVE_TOOL, rowClass: 'waiting', localDay: TODAY,
        toolInput: {
            momentId: `m-${id}`, kind: 'waiting_on_reply', headline: 'Someone is waiting on your reply.', body: `${id} sentence`, draft: false,
            actionIds: ['not_now', 'dismiss'], actionLabels: { not_now: 'Not now', dismiss: 'Dismiss' }, target: { kind: 'mail' }, state: 'pending', evidence: [],
        },
    };
}

const ITEMS: StreamItem[] = [
    { id: 'brief', kind: 'system', toolName: MAIN_CHAT_PROACTIVE_TOOL, rowClass: 'routine', localDay: TODAY, toolInput: {
        momentId: 'm-brief', kind: 'brief_ready', headline: 'Your brief is ready.', body: 'Your morning brief is ready.', draft: false,
        actionIds: [], actionLabels: {}, target: { kind: 'today' }, state: 'pending', evidence: [],
    } },
    waitingCard('card-1'),
    { id: 'user', kind: 'user', content: 'What should I focus on?', rowClass: 'conversation', localDay: TODAY },
    { id: 'answer', kind: 'assistant', content: 'The SOW first.', rowClass: 'conversation', localDay: TODAY },
];

const TOPICS = [
    { id: 'halcyon', kind: 'project', name: 'Halcyon ERP sync', status: 'waiting_on_you' },
    { id: 'northwind', kind: 'project', name: 'Northwind kickoff', status: 'moving' },
    { id: 'news', kind: 'following', name: 'AI news', status: 'new_stories' },
];

function Harness({ items = ITEMS, focusItem = null, streaming = false, onStop }: { items?: StreamItem[]; focusItem?: { itemId: string; signal: number } | null; streaming?: boolean; onStop?: () => void }) {
    // App owns both readers; a sidebar assignment also renders the topic page.
    useSidebarTopics();
    const view = useMainChatView({ enabled: true, items });
    const [box, setBox] = useState('');
    return (
        <>
            <output data-testid="selected-filter">{JSON.stringify(view.filter)}</output>
            <MainChatStreamBody mainChat={view} onPrepare={setBox} items={items} streaming={false} hasProject focusItem={focusItem} />
            {view.attached
                ? <MainChatTopicBox view={view} onUsePlainBox={setBox} streaming={streaming} onStop={onStop} />
                : <output data-testid="plain-box">{box}</output>}
        </>
    );
}

function SummaryHarness({ items = [] }: { items?: StreamItem[] }) {
    const view = useMainChatView({ enabled: true, items });
    return <MainChatStreamBody mainChat={view} onPrepare={() => {}} items={items} streaming={false} hasProject focusItem={null} />;
}

/** Bindings that change something on the computer and were called. */
function writesCalled(): string[] {
    return [...fixture.bindings.entries()]
        .filter(([name, fn]) => /^(Resolve|Send|Set|Persist|Delete|Create|Update|Start)/.test(name) && fn.mock.calls.length > 0)
        .map(([name]) => name);
}

function filterRow() {
    return within(screen.getByTestId('main-chat-filter-row'));
}

beforeAll(() => {
    globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
    Element.prototype.scrollIntoView ??= () => {};
});

beforeEach(async () => {
    resetPageQueries();
    await i18n.changeLanguage('en');
    fixture.bindings.clear();
    fixture.listeners.clear();
    binding('GetMainChatOverview').mockResolvedValue({ sessionId: 'main', day: TODAY, conversations: 1, waiting: 2, finished: 1, routine: 3, waitingItemIds: ['card-1'], hideRoutine: false });
    binding('GetMainChatWaiting').mockResolvedValue({ sessionId: 'main', count: 2, items: [], revision: 1, ready: true });
    binding('GetTodayTopics').mockResolvedValue(TOPICS);
    binding('ListMainChatAnswerChips').mockResolvedValue({});
    binding('IsClosedEyesModeEnabled').mockResolvedValue(false);
    binding('ListMainChatWaitingItems').mockResolvedValue({ sessionId: 'main', items: [], hasMore: false, nextCursor: '' });
    binding('ListMainChatTopicItems').mockResolvedValue({ sessionId: 'main', topicId: 'halcyon', items: [], hasMore: false, nextCursor: '' });
    binding('SetMainChatHideRoutine').mockResolvedValue(undefined);
});
afterEach(() => {
    cleanup();
    resetMainChatWaitingStore();
    resetPagePrivacyWithdrawal();
    vi.useRealTimers();
});

describe('chat view (RC 2 D1)', () => {
    it('omits the redundant summary and keeps filters outside the scroll flow', async () => {
        render(<Harness />);
        expect(screen.queryByTestId('main-chat-summary')).toBeNull();
        expect(filterRow().queryByRole('button', { name: /Waiting on you/ })).toBeNull();
        expect(filterRow().getByRole('button', { name: 'All' })).toHaveAttribute('aria-pressed', 'true');
        expect(filterRow().getByRole('button', { name: 'All' })).toHaveClass('bg-surface-active', 'rounded-lg');
        expect(screen.getByTestId('main-chat-filter-row').closest('[data-radix-scroll-area-viewport]')).toBeNull();
        // Work topics only; followed topics are not filters.
        await waitFor(() => expect(filterRow().getByRole('button', { name: 'Halcyon ERP sync' })).toBeInTheDocument());
        expect(filterRow().getByRole('button', { name: 'Hide updates' })).toBeInTheDocument();
        expect(filterRow().queryByRole('button', { name: 'AI news' })).toBeNull();
    });

    it('omits the summary even when counts are unavailable', async () => {
        binding('GetMainChatOverview').mockResolvedValue({ day: TODAY, waiting: 2 });
        binding('GetMainChatWaiting').mockResolvedValue({ sessionId: 'main', count: 0, items: [], revision: 1, ready: false });
        render(<Harness />);
        expect(screen.queryByTestId('main-chat-summary')).toBeNull();
    });

    it('has no Waiting filter or list; waiting cards stay in the stream (K10)', async () => {
        render(<Harness />);
        await screen.findByRole('heading', { name: 'Someone is waiting on your reply.' });
        expect(filterRow().queryByRole('button', { name: /Waiting/ })).toBeNull();
        expect(screen.getByRole('heading', { name: 'Someone is waiting on your reply.' })).toBeInTheDocument();
        expect(screen.queryByTestId('main-chat-waiting-empty')).toBeNull();
        expect(binding('ListMainChatWaitingItems')).not.toHaveBeenCalled();
        expect(writesCalled()).toEqual([]);
    });

    it('opens a saved Waiting filter as All (K10)', () => {
        expect(normalizeMainChatFilter({ kind: 'waiting' })).toEqual({ kind: 'all' });
        expect(normalizeMainChatFilter(undefined)).toEqual({ kind: 'all' });
        expect(normalizeMainChatFilter({ kind: 'topic', topicId: 'halcyon' })).toEqual({ kind: 'topic', topicId: 'halcyon' });
        const { result } = renderHook(() => useMainChatView({ enabled: true, items: [] }));
        act(() => result.current.setFilter({ kind: 'waiting' } as never));
        expect(result.current.filter).toEqual({ kind: 'all' });
    });

    it('uses waiting events for summary counts without reads on focus or session appends', async () => {
        binding('GetMainChatOverview').mockResolvedValue({
            sessionId: 'main', day: TODAY, conversations: 1, waiting: 99, finished: 1, routine: 3, waitingItemIds: [], hideRoutine: false,
        });
        render(<SummaryHarness />);
        await waitFor(() => expect(binding('GetMainChatOverview')).toHaveBeenCalled());
        expect(screen.queryByTestId('main-chat-summary')).toBeNull();
        const bindingNames = [...fixture.bindings.keys()];
        const readsBeforeAppend = new Map(bindingNames.map((name) => [name, binding(name).mock.calls.length]));

        await emit('session:items-appended', { sessionId: 'main' });
        await act(async () => { await new Promise((resolve) => setTimeout(resolve, 300)); });
        expect([...fixture.bindings.keys()]).toEqual(bindingNames);
        for (const [name, calls] of readsBeforeAppend) expect(binding(name).mock.calls.length, name).toBe(calls);

        await emit('main-chat:waiting-changed', {
            sessionId: 'main', day: TODAY, count: 2, conversations: 3, finished: 4, routine: 5,
            items: [], revision: 2, ready: true,
        });
        expect(screen.queryByTestId('main-chat-summary')).toBeNull();
        for (const [name, calls] of readsBeforeAppend) expect(binding(name).mock.calls.length, name).toBe(calls);

        const readsBeforeFocus = new Map([...fixture.bindings.entries()].map(([name, fn]) => [name, fn.mock.calls.length]));
        await act(async () => { window.dispatchEvent(new Event('focus')); });
        for (const [name, calls] of readsBeforeFocus) expect(binding(name).mock.calls.length, name).toBe(calls);
    });


    it('hides and shows the folded updates as a view switch that changes no row', async () => {
        render(<Harness />);
        expect(await screen.findByRole('button', { name: '1 update from Isoform' })).toBeInTheDocument();
        fireEvent.click(filterRow().getByRole('button', { name: en.mainChat.view.hideUpdates }));
        await waitFor(() => expect(binding('SetMainChatHideRoutine')).toHaveBeenCalledWith(true));
        await waitFor(() => expect(document.querySelector('[data-main-chat-fold]')).toBeNull());
        expect(filterRow().getByRole('button', { name: en.mainChat.view.showUpdates })).toBeInTheDocument();
        expect(screen.queryByTestId('main-chat-summary')).toBeNull();
        expect(screen.getByText('card-1 sentence')).toBeInTheDocument();
        expect(writesCalled()).toEqual(['SetMainChatHideRoutine']);
    });

    it('keeps folds showing and says so when the switch cannot be saved', async () => {
        binding('SetMainChatHideRoutine').mockRejectedValue(new Error('locked'));
        render(<Harness />);
        await screen.findByRole('button', { name: '1 update from Isoform' });
        fireEvent.click(filterRow().getByRole('button', { name: en.mainChat.view.hideUpdates }));
        expect(await filterRow().findByRole('alert')).toHaveTextContent(en.failure.save);
        expect(document.querySelector('[data-main-chat-fold]')).not.toBeNull();
    });

    it('hides the folded updates even when the first read of today’s numbers failed', async () => {
        binding('GetMainChatOverview').mockRejectedValue(new Error('down'));
        render(<Harness />);
        await screen.findByRole('button', { name: '1 update from Isoform' });
        fireEvent.click(filterRow().getByRole('button', { name: en.mainChat.view.hideUpdates }));
        await waitFor(() => expect(binding('SetMainChatHideRoutine')).toHaveBeenCalledWith(true));
        await waitFor(() => expect(document.querySelector('[data-main-chat-fold]')).toBeNull());
        expect(filterRow().getByRole('button', { name: en.mainChat.view.showUpdates })).toBeInTheDocument();
    });

    it('starts with routine updates hidden when this computer remembers that', async () => {
        binding('GetMainChatOverview').mockResolvedValue({ conversations: 0, waiting: 0, finished: 0, routine: 1, hideRoutine: true });
        render(<Harness />);
        expect(await filterRow().findByRole('button', { name: en.mainChat.view.showUpdates })).toBeInTheDocument();
        expect(document.querySelector('[data-main-chat-fold]')).toBeNull();
    });

    it('shows a topic’s rows at full width and sends with the topic attached', async () => {
        binding('ListMainChatTopicItems').mockResolvedValue({ sessionId: 'main', topicId: 'halcyon', hasMore: false, nextCursor: '', items: [
            { id: 'event:1', kind: 'event', at: Date.now(), text: 'reviewed the ERP sync plan' },
            { id: 'message:u', kind: 'message', at: Date.now(), message: { id: 'u', kind: 'user', content: 'Can we hold Nov 1?', sortOrder: 1 } },
            { id: 'message:a', kind: 'message', at: Date.now(), message: { id: 'a', kind: 'assistant', content: 'Yes, with a preview on Oct 28.', sortOrder: 2 } },
        ] });
        binding('SendMainChatWithTopic').mockResolvedValue({ sessionId: 'main' });
        render(<Harness />);
        fireEvent.click(await filterRow().findByRole('button', { name: 'Halcyon ERP sync' }));
        expect(await screen.findByText('Yes, with a preview on Oct 28.')).toBeInTheDocument();
        expect(binding('ListMainChatTopicItems')).toHaveBeenCalledWith('halcyon', '', 50);
        const topicView = screen.getByTestId('main-chat-topic-view');
        expect(topicView).toHaveTextContent('Your chat, showing only Halcyon ERP sync.');
        expect(within(topicView).queryByTestId('main-chat-summary')).toBeNull();
        expect(within(topicView).getByText('Can we hold Nov 1?')).toBeInTheDocument();
        expect(within(topicView).getByText(/reviewed the ERP sync plan/)).toBeInTheDocument();
        expect(screen.queryByText('What should I focus on?')).toBeNull();
        // The box carries the topic as a removable chip.
        expect(screen.getByTestId('topic-composer-chip')).toHaveTextContent('Halcyon ERP sync');
        const box = screen.getByRole('textbox', { name: 'Message Isoform about this topic' });
        fireEvent.change(box, { target: { value: 'Draft the reply' } });
        expect(writesCalled()).toEqual([]);
        fireEvent.keyDown(box, { key: 'Enter' });
        await waitFor(() => expect(binding('SendMainChatWithTopic')).toHaveBeenCalledWith('Draft the reply', 'halcyon'));
        // "Show everything" goes back to the whole chat.
        fireEvent.click(within(topicView).getByRole('button', { name: 'Show everything' }));
        expect(await screen.findByText('What should I focus on?')).toBeInTheDocument();
    });

    it('keeps loading a selected topic by id after the published view refreshes and goes empty', async () => {
        binding('ListMainChatTopicItems').mockResolvedValue({ topicId: 'halcyon', items: [
            { id: 'private-row', kind: 'event', at: Date.now(), text: 'Observed topic detail' },
        ], hasMore: false, nextCursor: '' });
        render(<Harness />);
        fireEvent.click(await filterRow().findByRole('button', { name: 'Halcyon ERP sync' }));
        await screen.findByText('Observed topic detail');
        const editor = screen.getByRole('textbox', { name: 'Message Isoform about this topic' });
        fireEvent.change(editor, { target: { value: 'My unsent draft' } });
        editor.focus();

        binding('GetTodayTopics').mockResolvedValue({ topics: TOPICS.filter((topic) => topic.id !== 'halcyon'), refreshing: false });
        await emit('topics:updated');
        await waitFor(() => expect(binding('ListMainChatTopicItems')).toHaveBeenCalledTimes(2));
        expect(filterRow().queryByRole('button', { name: 'Halcyon ERP sync' })).toBeNull();
        expect(filterRow().getByRole('button', { name: 'Northwind kickoff' })).toBeInTheDocument();
        expect(JSON.parse(screen.getByTestId('selected-filter').textContent!)).toEqual({ kind: 'topic', topicId: 'halcyon' });
        expect(screen.getByRole('textbox', { name: 'Message Isoform about this topic' })).toBe(editor);
        expect(editor).toHaveValue('My unsent draft');
        expect(editor).toHaveFocus();
        expect(screen.getByText('Observed topic detail')).toBeInTheDocument();
        expect(binding('ListMainChatTopicItems')).toHaveBeenLastCalledWith('halcyon', '', 50);

        binding('GetTodayTopics').mockResolvedValue({ topics: [], refreshing: false, clear: true });
        await emit('topics:updated');
        await waitFor(() => expect(binding('ListMainChatTopicItems')).toHaveBeenCalledTimes(3));
        expect(screen.getByTestId('main-chat-topic-view')).toBeInTheDocument();
        expect(screen.getByRole('textbox', { name: 'Message Isoform about this topic' })).toBe(editor);
        expect(editor).toHaveValue('My unsent draft');
        expect(binding('ListMainChatTopicItems')).toHaveBeenLastCalledWith('halcyon', '', 50);
        expect(writesCalled()).toEqual([]);
    });

    it('shows a withdrawn topic without its old rows or a way to send', async () => {
        binding('ListMainChatTopicItems').mockResolvedValue({ topicId: 'halcyon', items: [
            { id: 'private-row', kind: 'event', at: Date.now(), text: 'Observed topic detail' },
        ], hasMore: false, nextCursor: '' });
        render(<Harness />);
        fireEvent.click(await filterRow().findByRole('button', { name: 'Halcyon ERP sync' }));
        await screen.findByText('Observed topic detail');
        binding('GetTodayTopics').mockResolvedValue({ topics: [], refreshing: false, clear: true });
        binding('ListMainChatTopicItems').mockRejectedValue(new Error('TODAY_CHAT_ITEM_UNAVAILABLE'));
        await emit('topics:withdrawn');
        expect(await screen.findByTestId('main-chat-topic-withdrawn')).toHaveTextContent('This topic was withdrawn for privacy. Your chat still has everything.');
        expect(screen.queryByText('Observed topic detail')).toBeNull();
        expect(screen.queryByTestId('main-chat-topic-box')).toBeNull();
        expect(binding('SendMainChatWithTopic')).not.toHaveBeenCalled();
        fireEvent.click(within(screen.getByTestId('main-chat-topic-withdrawn')).getByRole('button', { name: 'Show everything' }));
        expect(filterRow().getByRole('button', { name: 'All' })).toHaveAttribute('aria-pressed', 'true');
        expect(screen.getByText('What should I focus on?')).toBeInTheDocument();
    });

    it('keeps the topic editor after a failed proof read and lets the user leave the filter', async () => {
        render(<Harness />);
        fireEvent.click(await filterRow().findByRole('button', { name: 'Halcyon ERP sync' }));
        const editor = screen.getByRole('textbox', { name: 'Message Isoform about this topic' });
        fireEvent.change(editor, { target: { value: 'Draft kept through read failure' } });
        binding('GetTodayTopics').mockRejectedValue(new Error('proof unavailable'));
        await emit('memory:updated');
        expect(JSON.parse(screen.getByTestId('selected-filter').textContent!)).toEqual({ kind: 'topic', topicId: 'halcyon' });
        expect(editor).toHaveValue('Draft kept through read failure');
        expect(screen.getByTestId('main-chat-topic-view')).toHaveTextContent('Halcyon ERP sync');
        fireEvent.click(screen.getByRole('button', { name: 'Show everything' }));
        expect(filterRow().getByRole('button', { name: 'All' })).toHaveAttribute('aria-pressed', 'true');
        expect(screen.getByTestId('plain-box')).toBeInTheDocument();
    });

    it('keeps Stop reachable while topic proof is pending', async () => {
        const stop = vi.fn();
        render(<Harness streaming onStop={stop} />);
        fireEvent.click(await filterRow().findByRole('button', { name: 'Halcyon ERP sync' }));
        binding('GetTodayTopics').mockResolvedValue({ topics: [], refreshing: true, clear: true });
        await emit('sensitive-apps:updated');
        fireEvent.click(screen.getByTestId('topic-composer-stop'));
        expect(stop).toHaveBeenCalledTimes(1);
        expect(screen.getByRole('textbox', { name: 'Message Isoform about this topic' })).toBeInTheDocument();
        expect(binding('SendMainChatWithTopic')).not.toHaveBeenCalled();
    });

    it('a Search hit opens the chat at that message, back on All, with the view intact (RC 2 D3)', async () => {
        const scrolled: Element[] = [];
        const original = Element.prototype.scrollIntoView;
        Element.prototype.scrollIntoView = function scrollIntoView(this: Element) {
            scrolled.push(this);
        };
        try {
            const { rerender } = render(<Harness />);
            // The person was looking at one topic.
            fireEvent.click(await filterRow().findByRole('button', { name: 'Halcyon ERP sync' }));
            expect(await screen.findByTestId('main-chat-topic-view')).toBeInTheDocument();
            rerender(<Harness focusItem={{ itemId: 'answer', signal: 1 }} />);
            // Everything again, so the message is in the stream, and it is
            // scrolled into view.
            expect(await screen.findByText('What should I focus on?')).toBeInTheDocument();
            expect(screen.queryByTestId('main-chat-topic-view')).toBeNull();
            await waitFor(() => expect(scrolled.some((el) => el.textContent?.includes('The SOW first.'))).toBe(true));
            // The filter row and the day's fold remain available.
            expect(screen.getByTestId('main-chat-filter-row')).toBeInTheDocument();
            expect(screen.queryByText(/In your chat today/)).toBeNull();
            expect(writesCalled()).toEqual([]);
        } finally {
            Element.prototype.scrollIntoView = original;
        }
    });

    it('keeps a draft per topic and drops the topic from the box with its chip', async () => {
        render(<Harness />);
        fireEvent.click(await filterRow().findByRole('button', { name: 'Halcyon ERP sync' }));
        fireEvent.change(await screen.findByRole('textbox', { name: 'Message Isoform about this topic' }), { target: { value: 'Halcyon words' } });
        fireEvent.click(filterRow().getByRole('button', { name: 'Northwind kickoff' }));
        expect(await screen.findByRole('textbox', { name: 'Message Isoform about this topic' })).toHaveValue('');
        fireEvent.click(filterRow().getByRole('button', { name: 'Halcyon ERP sync' }));
        expect(await screen.findByRole('textbox', { name: 'Message Isoform about this topic' })).toHaveValue('Halcyon words');
        // Removing the chip keeps the filter but the box is the plain one again.
        fireEvent.click(screen.getByRole('button', { name: 'Remove Halcyon ERP sync from the box' }));
        expect(screen.queryByTestId('topic-composer-chip')).toBeNull();
        expect(screen.getByTestId('plain-box')).toBeInTheDocument();
        expect(screen.getByTestId('main-chat-topic-view')).toBeInTheDocument();
        expect(filterRow().getByRole('button', { name: 'Halcyon ERP sync' })).toHaveAttribute('aria-pressed', 'true');
        // Picking the topic again attaches it, with its words.
        fireEvent.click(filterRow().getByRole('button', { name: 'Halcyon ERP sync' }));
        expect(await screen.findByRole('textbox', { name: 'Message Isoform about this topic' })).toHaveValue('Halcyon words');
        expect(writesCalled()).toEqual([]);
    });

    it('while observing is paused, a topic send puts the words in the plain box, unsent', async () => {
        binding('IsClosedEyesModeEnabled').mockResolvedValue(true);
        render(<Harness />);
        fireEvent.click(await filterRow().findByRole('button', { name: 'Halcyon ERP sync' }));
        const box = await screen.findByRole('textbox', { name: 'Message Isoform about this topic' });
        await screen.findByRole('button', { name: 'Continue in the chat' });
        fireEvent.change(box, { target: { value: 'Paused words' } });
        fireEvent.keyDown(box, { key: 'Enter' });
        expect(await screen.findByTestId('plain-box')).toHaveTextContent('Paused words');
        expect(binding('SendMainChatWithTopic')).not.toHaveBeenCalled();
    });

    it('shows prepared chips under the latest answer, reads again on main-chat:chips, and a chip only fills the box', async () => {
        render(<Harness />);
        await waitFor(() => expect(binding('ListMainChatAnswerChips')).toHaveBeenCalledWith(['answer']));
        expect(document.querySelector('[data-main-chat-chips]')).toBeNull();
        binding('ListMainChatAnswerChips').mockResolvedValue({ answer: ['Hold 3:00 to 4:30 for the memo', 'Open the Northwind SOW'] });
        await emit('main-chat:chips', { sessionId: 'main', itemId: 'answer' });
        const chip = await screen.findByRole('button', { name: 'Hold 3:00 to 4:30 for the memo' });
        fireEvent.click(chip);
        expect(screen.getByTestId('plain-box')).toHaveTextContent('Hold 3:00 to 4:30 for the memo');
        expect(writesCalled()).toEqual([]);
        // Pausing observation takes the chips away.
        await emit('closed-eyes:changed', true);
        expect(screen.queryByRole('button', { name: 'Open the Northwind SOW' })).toBeNull();
    });

    it('shows and sends chips under a historical question after a later turn', async () => {
        const items: StreamItem[] = [
            { id: 'old-user', kind: 'user', content: 'My brief', rowClass: 'conversation', localDay: TODAY },
            { id: 'old-question', kind: 'assistant', content: 'Should I leave PT out?', rowClass: 'conversation', localDay: TODAY },
            { id: 'later-user', kind: 'user', content: 'Another topic', rowClass: 'conversation', localDay: TODAY },
            { id: 'later-answer', kind: 'assistant', content: 'Done.', rowClass: 'conversation', localDay: TODAY },
        ];
        binding('ListMainChatAnswerChips').mockResolvedValue({ 'old-question': ['Keep it out', "Include it when it's due"] });
        const view = render(<Harness items={items} />);
        fireEvent.click(await screen.findByRole('button', { name: 'Keep it out' }));
        expect(screen.getByTestId('plain-box')).toHaveTextContent('Keep it out');
        expect(screen.getByText('You answered: Keep it out')).toBeInTheDocument();
        view.rerender(<Harness items={[...items, { id: 'new-user', kind: 'user', content: 'Keep it out', rowClass: 'conversation', localDay: TODAY }]} />);
        expect(screen.getByText('You answered: Keep it out')).toBeInTheDocument();
        fireEvent.click(screen.getByRole('button', { name: 'Change' }));
        expect(screen.getByRole('button', { name: "Include it when it's due" })).toBeInTheDocument();
    });

    it('one failing read never hides the rest of the chat', async () => {
        binding('GetMainChatOverview').mockRejectedValue(new Error('down'));
        binding('GetTodayTopics').mockRejectedValue(new Error('down'));
        binding('ListMainChatAnswerChips').mockRejectedValue(new Error('down'));
        render(<Harness />);
        await screen.findByText('What should I focus on?');
        expect(screen.getByText('What should I focus on?')).toBeInTheDocument();
        expect(screen.getByText('card-1 sentence')).toBeInTheDocument();
        expect(screen.getByRole('button', { name: '1 update from Isoform' })).toBeInTheDocument();
        expect(filterRow().getByRole('button', { name: 'All' })).toBeInTheDocument();
        expect(filterRow().queryByRole('button', { name: /Waiting on you/ })).toBeNull();
        expect(screen.queryByTestId('main-chat-summary')).toBeNull();
    });

    it('reads in Chinese', async () => {
        await i18n.changeLanguage('zh-CN');
        render(<Harness />);
        expect(screen.queryByTestId('main-chat-summary')).toBeNull();
        expect(filterRow().getByRole('button', { name: zh.mainChat.view.everything })).toBeInTheDocument();
        expect(screen.getByRole('separator', { name: zh.mainChat.view.today })).toBeInTheDocument();
        expect(screen.getByRole('button', { name: 'Isoform 的 1 条更新' })).toBeInTheDocument();
    });
});

it('keeps the topic bar unchanged through repeated pending empty publications', async () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date(`${TODAY}T12:00:00`));
    binding('GetTodayTopics').mockResolvedValue({ topics: TOPICS, refreshing: true, firstRun: false });
    binding('ListSidebarTopics').mockResolvedValue([]);
    let commits = 0;
    render(<Profiler id="topic-bar" onRender={() => commits++}><Harness /></Profiler>);
    await act(async () => { await vi.advanceTimersByTimeAsync(500); });
    const topic = filterRow().getByRole('button', { name: 'Halcyon ERP sync' });
    const initialCommits = commits;
    const mutations: MutationRecord[] = [];
    const observer = new MutationObserver((entries) => mutations.push(...entries));
    observer.observe(screen.getByTestId('main-chat-filter-row'), { attributes: true, childList: true, characterData: true, subtree: true });
    try {
        for (let publication = 0; publication < 120; publication++) {
            binding('GetTodayTopics').mockResolvedValue(publication % 2 === 0
                ? { topics: [], refreshing: true, firstRun: false }
                : { topics: structuredClone(TOPICS), refreshing: true, firstRun: false });
            await act(async () => { await vi.advanceTimersByTimeAsync(5000); });
            await emit('topics:updated');
        }
        expect(commits).toBe(initialCommits);
        expect(mutations).toHaveLength(0);
        expect(filterRow().getByRole('button', { name: 'Halcyon ERP sync' })).toBe(topic);
    } finally { observer.disconnect(); }
    binding('GetTodayTopics').mockResolvedValue({ topics: TOPICS.map(item => item.id === 'halcyon' ? { ...item, name: 'Updated topic' } : item), refreshing: false });
    await emit('topics:updated');
    expect(filterRow().getByRole('button', { name: 'Updated topic' })).toBe(topic);
    expect(commits).toBeGreaterThan(initialCommits);
});

it('keeps the selected topic detail page unchanged across redundant publications', async () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date(`${TODAY}T12:00:00`));
    binding('GetTodayTopics').mockImplementation(async () => structuredClone(TOPICS));
    binding('ListSidebarTopics').mockImplementation(async () => structuredClone([{ ...TOPICS[0], pinnedToSidebar: true }]));
    const detailAt = Date.now();
    binding('ListMainChatTopicItems').mockImplementation(async () => ({ sessionId: 'main', topicId: 'halcyon',
        items: [{ id: 'event:thread', kind: 'event', at: detailAt, text: 'reviewed "salesrabbit-isoform"' }], hasMore: false, nextCursor: '' }));
    let commits = 0;
    const host = render(<Profiler id="topic-page" onRender={() => commits++}><Harness /></Profiler>);
    await act(async () => { await vi.advanceTimersByTimeAsync(500); });
    fireEvent.click(filterRow().getByRole('button', { name: 'Halcyon ERP sync' }));
    await act(async () => {});
    expect(screen.getByTestId('topic-panel-event')).toBeInTheDocument();
    const page = screen.getByTestId('main-chat-topic-view');
    const box = screen.getByRole('textbox');
    fireEvent.change(box, { target: { value: 'Unsent topic draft' } });
    page.scrollTop = 37;
    const initialCommits = commits, initialReads = binding('GetTodayTopics').mock.calls.length;
    const markup = host.container.innerHTML;
    const mutations: MutationRecord[] = [];
    const observer = new MutationObserver((entries) => mutations.push(...entries));
    observer.observe(host.container, { attributes: true, childList: true, characterData: true, subtree: true });
    try {
        for (let refresh = 0; refresh < 120; refresh++) {
            await act(async () => { await vi.advanceTimersByTimeAsync(5000); });
            await emit('topics:updated');
        }
        expect(binding('GetTodayTopics').mock.calls.length - initialReads).toBe(120);
        for (let focus = 0; focus < 10; focus++) await act(async () => { window.dispatchEvent(new Event('focus')); });
        await emit('agent:event', { type: 'done', tag: 'background-topic-aux' });
        expect(binding('ListMainChatTopicItems')).toHaveBeenCalledTimes(131);
        expect(binding('ListSidebarTopics')).toHaveBeenCalledTimes(131);
        expect(commits).toBe(initialCommits);
        expect(mutations).toHaveLength(0);
        expect(host.container.innerHTML).toBe(markup);
        expect(screen.getByTestId('main-chat-topic-view')).toBe(page);
        expect(page.scrollTop).toBe(37);
        expect(box).toHaveValue('Unsent topic draft');
    } finally { observer.disconnect(); }
    binding('GetTodayTopics').mockResolvedValue(TOPICS.map(topic => topic.id === 'halcyon' ? { ...topic, name: 'Updated thread name' } : topic));
    await emit('topics:updated');
    expect(screen.getByTestId('main-chat-topic-view')).toBe(page);
    expect(page).toHaveTextContent('Updated thread name');
    expect(commits).toBeGreaterThan(initialCommits);
    // A real time-zone change still renders local timestamps on focus.
    const beforeZone = commits;
    const options = new Intl.DateTimeFormat().resolvedOptions();
    const zone = vi.spyOn(Intl.DateTimeFormat.prototype, 'resolvedOptions').mockReturnValue({ ...options,
        timeZone: options.timeZone === 'UTC' ? 'America/New_York' : 'UTC' });
    try {
        await act(async () => { window.dispatchEvent(new Event('focus')); });
        expect(commits).toBeGreaterThan(beforeZone);
    } finally { zone.mockRestore(); }
    // The quiet minute clock must still re-read the numbers on a real new day.
    const overviewReads = binding('GetMainChatOverview').mock.calls.length;
    const tomorrow = new Date(`${TODAY}T12:00:00`);
    tomorrow.setDate(tomorrow.getDate() + 1);
    vi.setSystemTime(tomorrow);
    await act(async () => { await vi.advanceTimersByTimeAsync(60_000); });
    await act(async () => { await vi.advanceTimersByTimeAsync(1000); });
    expect(binding('GetMainChatOverview').mock.calls.length).toBeGreaterThan(overviewReads);
});
