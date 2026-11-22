import { resetPageQueries } from '@/lib/page-queries';
import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { useState } from 'react';
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest';
import { chatThreadUpdates } from '@/lib/main-chat-view';
import i18n from '@/i18n';
import en from '@/i18n/locales/en/chat.json';
import zh from '@/i18n/locales/zh-CN/chat.json';
import { MAIN_CHAT_PROACTIVE_TOOL } from '@/lib/stream-helpers';
import { resetMainChatWaitingStore } from '@/lib/main-chat-waiting-store';
import { resetPagePrivacyWithdrawal } from '@/lib/page-privacy-withdrawal';
import { useMainChatView } from '@/hooks/useMainChatView';
import { useSidebarTopics } from '@/hooks/useTodayTopics';
import type { StreamItem } from '@/types';
import { MainChatStreamBody } from './main-chat-stream-body';

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

function Harness({ items = ITEMS, focusItem = null }: { items?: StreamItem[]; focusItem?: { itemId: string; signal: number } | null }) {
    // App owns both readers.
    useSidebarTopics();
    const view = useMainChatView({ enabled: true, items });
    const [box, setBox] = useState('');
    return (
        <>
            <MainChatStreamBody mainChat={view} onPrepare={setBox} items={items} streaming={false} hasProject focusItem={focusItem} />
            <output data-testid="plain-box">{box}</output>
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

function header() {
    return within(screen.getByTestId('main-chat-header'));
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
    it('is one thread: the title and what Chat is for, no filters, topic chips or update switch (computer-chat v1.0.0 rules 1 and 2)', async () => {
        render(<Harness />);
        await waitFor(() => expect(binding('GetTodayTopics')).toHaveBeenCalled());
        await screen.findByText('What should I focus on?');
        expect(header().getByRole('heading', { level: 1, name: en.mainChat.view.title })).toBeInTheDocument();
        expect(header().getByText(en.mainChat.view.sub)).toBeInTheDocument();
        expect(en.mainChat.view.sub).toBe('About your own day, and setting Isoform straight.');
        expect(en.mainChat.view.field).toBe('Ask about your day, or correct Isoform');
        expect(header().queryAllByRole('button')).toEqual([]);
        expect(screen.queryByTestId('main-chat-filter-row')).toBeNull();
        expect(screen.queryByTestId('main-chat-topic-view')).toBeNull();
        expect(screen.queryByRole('button', { name: 'Halcyon ERP sync' })).toBeNull();
        expect(screen.queryByRole('button', { name: 'All' })).toBeNull();
        expect(screen.queryByRole('button', { name: /Hide updates|Show updates/ })).toBeNull();
        expect(document.querySelector('[data-main-chat-filter]')).toBeNull();
        expect(screen.getByTestId('main-chat-header').closest('[data-radix-scroll-area-viewport]')).toBeNull();
        expect(screen.queryByTestId('main-chat-summary')).toBeNull();
        expect(writesCalled()).toEqual([]);
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
        expect(screen.queryByRole('button', { name: /Waiting/ })).toBeNull();
        expect(screen.getByRole('heading', { name: 'Someone is waiting on your reply.' })).toBeInTheDocument();
        expect(screen.queryByTestId('main-chat-waiting-empty')).toBeNull();
        expect(binding('ListMainChatWaitingItems')).not.toHaveBeenCalled();
        expect(writesCalled()).toEqual([]);
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
        expect(header().getByRole('heading', { level: 1, name: en.mainChat.view.title })).toBeInTheDocument();
        expect(screen.queryByTestId('main-chat-summary')).toBeNull();
    });

    it('reads in Chinese', async () => {
        await i18n.changeLanguage('zh-CN');
        render(<Harness />);
        expect(screen.queryByTestId('main-chat-summary')).toBeNull();
        expect(header().getByRole('heading', { level: 1, name: zh.mainChat.view.title })).toBeInTheDocument();
        expect(header().getByText(zh.mainChat.view.sub)).toBeInTheDocument();
        expect(screen.getByRole('separator', { name: zh.mainChat.view.today })).toBeInTheDocument();
        expect(screen.getByRole('button', { name: 'Isoform 的 1 条更新' })).toBeInTheDocument();
    });
});
