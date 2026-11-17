import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { chatThreadUpdates } from '@/lib/main-chat-view';
import i18n from '@/i18n';
import { ComposerVoiceContext } from '@/lib/composer-voice-context';
import { MainChatPanel, splitMeetingPrep } from './MainChatPanel';
import { TopicPanel } from './TopicPanel';
import type { StreamItem } from '@/types';
import { MAIN_CHAT_REPORT_TOOL } from '@/lib/stream-helpers';
import { GroupedStreamList } from '@/components/grouped-stream-list';
import { MainChatViewContext } from '@/components/main-chat/main-chat-view-context';

// These cover the rollback switch: Isoform's updates back in Chat's thread.
beforeEach(() => { chatThreadUpdates.shown = true; });
afterEach(() => { chatThreadUpdates.shown = false; });


const fixture = vi.hoisted(() => ({ chips: {} as Record<string, string[]>, items: [] as StreamItem[], waiting: false, stopped: false, lastUserItemId: '', pending: null as null | { text: string; state: 'failed'; context: { sourceKind: string; sourceId: string }; ask?: boolean }, send: vi.fn(async () => 'sent') }));
vi.mock('@/lib/wails-shim-app', () => ({
    IdeaMeaningGroups: () => Promise.resolve(null),
    ResolveMainChatProactiveAction: vi.fn(),
    ResolveUpcomingConfirm: vi.fn(),
}));
vi.mock('@/hooks/useMainChatPanel', () => ({
    useMainChatPanel: () => ({ sessionId: 'main', status: 'ready', items: fixture.items, pending: fixture.pending, waiting: fixture.waiting, stopped: fixture.stopped, lastUserItemId: fixture.lastUserItemId, send: fixture.send }),
    waitingCards: () => [],
}));
vi.mock('@/hooks/useMainChatView', () => ({ useMainChatAnswerChips: () => fixture.chips }));
vi.mock('@/hooks/useTopicChat', () => ({
    useTopicChat: () => ({ status: 'ready', items: fixture.items.map(item => ({ id: item.id, kind: 'message', at: NaN, message: item })), pending: null, waiting: fixture.waiting }),
}));
const handlers = { onStartChat: vi.fn(), onRunNow: vi.fn(), onOpenMemory: vi.fn(), onOpenAutomations: vi.fn(), onOpenActivity: vi.fn(), onOpenPeople: vi.fn() };
const topic = { id: 'release', name: 'Release', kind: 'work', pinned: false } as const;
function panel(kind: string, name: string = topic.name) {
    const common = { draft: '', prepared: false, onDraftChange: vi.fn(), handlers };
    return kind === 'main'
        ? <MainChatPanel {...common} topicsFailed={false} onRetryTopics={vi.fn()} />
        : <TopicPanel {...common} topic={{ ...topic, name }} variant="pinned" observationPaused={false} />;
}
beforeEach(async () => {
    await i18n.changeLanguage('en');
    fixture.send.mockClear();
    fixture.chips = {};
    fixture.waiting = false;
    fixture.stopped = false;
    fixture.lastUserItemId = '';
    fixture.pending = null;
    fixture.items = [{ id: 'first', kind: 'user', content: 'Review release' }];
});
afterEach(cleanup);
it('uses the page session and its pagination for the panel day fold', () => {
    const status = (id: string, count: number): StreamItem => ({
        id, kind: 'system', toolName: 'main_chat_status', localDay: '2026-10-29', rowClass: 'routine',
        toolInput: { period: id, entries: Array.from({ length: count }, (_, i) => ({ id: `${id}-${i}`, kind: 'observing', text: `Update ${i}`, target: { kind: 'none' } })) },
    });
    fixture.items = [status('partial', 7)];
    const shared = { localId: 'main', itemsLoaded: true, items: [status('complete', 9)], hasMore: true, loadingMore: false } as import('@/types').Session;
    const loadMore = vi.fn(async () => {});
    const props = { draft: '', prepared: false, onDraftChange: vi.fn(), handlers, topicsFailed: false, onRetryTopics: vi.fn(), sharedSession: shared, onLoadSharedMore: loadMore };
    const view = render(<MainChatPanel {...props} />);
    expect(screen.getByRole('button', { name: '9 updates from Isoform' })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: '7 updates from Isoform' })).toBeNull();
    fireEvent.click(screen.getByRole('button', { name: 'Show earlier' }));
    expect(loadMore).toHaveBeenCalledOnce();
    view.rerender(<MainChatPanel {...props} hideRoutine />);
    expect(screen.queryByRole('button', { name: '9 updates from Isoform' })).toBeNull();
});
it('renders the same nine Thursday updates and historical PT chips on page and panel', () => {
    const day = '2026-10-29';
    const entries = Array.from({ length: 9 }, (_, i) => ({ id: `entry-${i}`, kind: 'digest_done', text: `Update ${i}`, target: { kind: 'today' } }));
    const rows: StreamItem[] = [
        { id: 'pt', kind: 'assistant', content: 'Should I keep PT out of your brief?', localDay: day, rowClass: 'conversation' },
        { id: 'status-nine', kind: 'system', toolName: 'main_chat_status', localDay: day, rowClass: 'routine', toolInput: { period: day, entries } },
    ];
    fixture.chips = { pt: ['Keep it out', "Include it when it's due"] };
    const shared = { localId: 'main', itemsLoaded: true, items: rows, hasMore: false, loadingMore: false } as import('@/types').Session;
    render(<>
        <div data-testid="page"><MainChatViewContext.Provider value={{ today: '2026-11-02', hideRoutine: false, topicNames: new Map(), chips: fixture.chips, onFillChip: vi.fn() }}><GroupedStreamList items={rows} streaming={false} /></MainChatViewContext.Provider></div>
        <div data-testid="panel"><MainChatPanel draft="" prepared={false} onDraftChange={vi.fn()} handlers={handlers} topicsFailed={false} onRetryTopics={vi.fn()} sharedSession={shared} /></div>
    </>);
    for (const surface of [screen.getByTestId('page'), screen.getByTestId('panel')]) {
        expect(within(surface).getByRole('button', { name: '9 updates from Isoform' })).toBeInTheDocument();
        expect(within(surface).getByText('Should I keep PT out of your brief?')).toBeInTheDocument();
        expect(within(surface).getByRole('button', { name: 'Keep it out' })).toBeInTheDocument();
        expect(within(surface).getByRole('button', { name: "Include it when it's due" })).toBeInTheDocument();
    }
});
function pageAndPanel(rows: StreamItem[], pageOpen = vi.fn(), panelOpen = vi.fn()) {
    const shared = { localId: 'main', itemsLoaded: true, items: rows, hasMore: false, loadingMore: false } as import('@/types').Session;
    render(<>
        <div data-testid="page"><MainChatViewContext.Provider value={{ today: '2026-11-02', hideRoutine: false, topicNames: new Map(), chips: {}, onFillChip: vi.fn() }}><GroupedStreamList items={rows} streaming={false} onOpenReportTarget={pageOpen} /></MainChatViewContext.Provider></div>
        <div data-testid="panel"><MainChatPanel draft="" prepared={false} onDraftChange={vi.fn()} handlers={handlers} topicsFailed={false} onRetryTopics={vi.fn()} sharedSession={shared} onOpenReportTarget={panelOpen} /></div>
    </>);
    return [screen.getByTestId('page'), screen.getByTestId('panel')];
}
const foldLines = (surface: HTMLElement) => within(surface).getAllByRole('listitem')
    .filter((line) => line.closest('[data-main-chat-day-fold]') || line.closest('ul'))
    .map((line) => [line.querySelector('span')?.textContent ?? '', within(line).queryAllByRole('button', { name: 'Show' }).length]);
it('gives the panel the same fold lines with the same Show as the page (chat-review v1.0.9 #1)', () => {
    const day = '2026-10-29';
    const linked = [
        { id: 'pt', kind: 'observing', text: 'PT moved to Friday.', target: { kind: 'thread', id: 'thread-pt' } },
        { id: 'dinner', kind: 'meeting_upcoming', text: 'dinner at 6:00 PM', target: { kind: 'calendar', id: 'event-dinner' } },
        { id: 'tim-bo', kind: 'observing', text: 'Tim / Bo', target: { kind: 'thread', id: 'thread-tim-bo' } },
        { id: 'reservation', kind: 'mail_received', text: 'Your reservation is confirmed.', target: { kind: 'mail', id: 'mail-1' } },
        { id: 'calendar', kind: 'calendar_conflict', text: 'Calendar checked.', target: { kind: 'calendar' } },
    ];
    const unlinked = Array.from({ length: 4 }, (_, i) => ({ id: `plain-${i}`, kind: 'observing', text: `Update ${i}`, target: { kind: 'none' } }));
    const rows: StreamItem[] = [{ id: 'status', kind: 'system', toolName: 'main_chat_status', localDay: day, rowClass: 'routine', toolInput: { period: day, entries: [...linked, ...unlinked] } }];
    const panelOpen = vi.fn();
    const [page, panel] = pageAndPanel(rows, vi.fn(), panelOpen);
    for (const surface of [page, panel]) fireEvent.click(within(surface).getByRole('button', { name: '9 updates from Isoform' }));
    expect(within(page).getAllByRole('button', { name: 'Show' })).toHaveLength(5);
    expect(within(panel).getAllByRole('button', { name: 'Show' })).toHaveLength(5);
    expect(foldLines(panel)).toEqual(foldLines(page));
    const dinner = within(panel).getByText('dinner at 6:00 PM').closest('li')!;
    expect(dinner).toHaveTextContent('dinner at 6:00 PM · From CalendarShow');
    fireEvent.click(within(dinner).getByRole('button', { name: 'Show' }));
    expect(panelOpen).toHaveBeenCalledWith({ kind: 'calendar', id: 'event-dinner' });
});
it('turns a pause line into its from-to line in the same place once the pause ends (chat-review v1.0.9 #2)', () => {
    const day = '2026-10-29';
    const start = new Date(2026, 9, 29, 22, 5).getTime();
    const end = new Date(2026, 9, 29, 22, 40).getTime();
    const pause = { id: 'pause', momentId: 'moment-pause', kind: 'capture_paused', text: 'Paused', captureSource: 'screen', captureCause: 'permission', captureStartAt: start, target: { kind: 'settings', id: 'activity' } };
    const rows: StreamItem[] = [
        { id: 'evening', kind: 'system', toolName: 'main_chat_status', localDay: day, rowClass: 'routine', toolInput: { period: `${day}:evening`, entries: [
            { id: 'before', kind: 'observing', text: 'Before the pause', target: { kind: 'none' } },
            pause,
            { id: 'after', kind: 'observing', text: 'After the pause', target: { kind: 'none' } },
        ] } },
        { id: 'night', kind: 'system', toolName: 'main_chat_status', localDay: day, rowClass: 'routine', toolInput: { period: `${day}:night`, entries: [{ ...pause, id: 'pause-ended', captureEndAt: end }] } },
    ];
    for (const surface of pageAndPanel(rows)) {
        fireEvent.click(within(surface).getByRole('button', { name: '3 updates from Isoform' }));
        const stop = 'Stopped watching your screen from 10:05 PM to 10:40 PM: Screen Recording was turned off.';
        expect(within(surface).queryByText(/your screen at/)).toBeNull();
        expect(within(surface).getAllByText(stop)).toHaveLength(1);
        expect(foldLines(surface).map(([text]) => text)).toEqual(['Before the pause · From Observing', stop, 'After the pause · From Observing']);
    }
});
it('shows each stop once, counts what it shows, and drops sleep, lock and chosen pauses (chat-review v1.0.10 H1, H2)', () => {
    const day = '2026-10-29';
    const at = (hour: number, minute: number) => new Date(2026, 9, 29, hour, minute).getTime();
    const stop = (id: string, source: string, cause: string | undefined, startAt: number) => ({ id, momentId: id, kind: 'capture_paused', text: 'Paused', captureSource: source, captureCause: cause, captureStartAt: startAt, target: { kind: 'settings', id: 'activity' } });
    const rows: StreamItem[] = [
        { id: 'noon', kind: 'system', toolName: 'main_chat_status', localDay: day, rowClass: 'routine', toolInput: { period: `${day}:noon`, entries: [
            stop('screen', 'screen', 'permission', at(12, 21)),
            stop('screen-again', 'screen', 'permission', at(12, 21)),
            stop('listening', 'microphone', 'stopped', at(15, 2)),
            stop('listening-audio', 'system_audio', 'stopped', at(15, 2)),
            stop('chosen', 'screen', 'chosen', at(16, 0)),
            stop('old', 'screen', undefined, at(17, 0)),
        ] } },
        { id: 'card', kind: 'system', toolName: 'main_chat_proactive', localDay: day, rowClass: 'routine', toolInput: { momentId: 'screen', kind: 'capture_paused', headline: 'Pause', body: '', captureSource: 'screen', captureCause: 'permission', captureStartAt: at(12, 21) } },
        { id: 'own-pause', kind: 'system', toolName: 'main_chat_observation_pause', localDay: day, rowClass: 'routine', toolInput: { startAt: at(18, 0), endAt: at(18, 30) } },
    ];
    for (const surface of pageAndPanel(rows)) {
        fireEvent.click(within(surface).getByRole('button', { name: '2 updates from Isoform' }));
        expect(foldLines(surface).map(([text]) => text)).toEqual([
            'Stopped watching your screen at 12:21 PM: Screen Recording was turned off.',
            'Stopped listening at 3:02 PM: listening stopped on its own.',
        ]);
        expect(within(surface).getAllByRole('button', { name: 'Turn it back on' })).toHaveLength(2);
        expect(surface.textContent).not.toMatch(/Observing paused/);
    }
});
it('reads the day words of a past fold line from its own day on the page and in the panel (Thursday polish C2)', () => {
    // The clock is pinned: "Thursday" is the day name for a day within the week,
    // so the expectation must not depend on when the suite runs.
    vi.useFakeTimers({ toFake: ['Date'] });
    vi.setSystemTime(new Date(2026, 10, 2, 12));
    const rows: StreamItem[] = [{ id: 'status', kind: 'system', toolName: 'main_chat_status', localDay: '2026-10-29', rowClass: 'routine', toolInput: { period: '2026-10-29', entries: [
        { id: 'pt', kind: 'observing', text: 'PT is due Thursday.', target: { kind: 'none' } },
        { id: 'forms', kind: 'observing', text: 'The library forms are due tomorrow.', target: { kind: 'none' } },
    ] } }];
    for (const surface of pageAndPanel(rows)) {
        fireEvent.click(within(surface).getByRole('button', { name: '2 updates from Isoform' }));
        expect(foldLines(surface).map(([text]) => text)).toEqual(['PT was due Thursday. · From Observing', 'The library forms were due Friday. · From Observing']);
        for (const [text] of foldLines(surface)) expect(text).not.toMatch(/\bis due\b|\btoday\b|\btomorrow\b/i);
    }
    vi.useRealTimers();
});
it('lists background lines right under the day fold, with no fold inside it (Thursday polish C3)', () => {
    const day = '2026-10-29';
    const status = (id: string, count: number): StreamItem => ({ id, kind: 'system', toolName: 'main_chat_status', localDay: day, rowClass: 'routine', toolInput: { period: `${day}:${id}`, entries: Array.from({ length: count }, (_, i) => ({ id: `${id}-${i}`, kind: 'observing', text: `${id} line ${i}`, target: { kind: 'none' } })) } });
    for (const surface of pageAndPanel([status('morning', 5), status('evening', 4)])) {
        fireEvent.click(within(surface).getByRole('button', { name: '9 updates from Isoform' }));
        expect(within(surface).queryByRole('button', { name: /background update/ })).toBeNull();
        expect(within(surface).getAllByRole('listitem').filter((line) => /line \d/.test(line.textContent ?? ''))).toHaveLength(9);
        expect(within(surface).getByText('morning line 0')).toBeVisible();
    }
});
it('keeps the inner background group when the day fold also holds a card (Thursday polish C3)', () => {
    const day = '2026-10-29';
    const rows: StreamItem[] = [
        { id: 'card', kind: 'assistant', content: 'A card in the fold', localDay: day, rowClass: 'routine' },
        { id: 'status', kind: 'system', toolName: 'main_chat_status', localDay: day, rowClass: 'routine', toolInput: { period: day, entries: [
            { id: 'one', kind: 'observing', text: 'Line one', target: { kind: 'none' } },
            { id: 'two', kind: 'observing', text: 'Line two', target: { kind: 'none' } },
        ] } },
    ];
    const [page] = pageAndPanel(rows);
    fireEvent.click(within(page).getByRole('button', { name: '3 updates from Isoform' }));
    expect(within(page).getByRole('button', { name: '2 background updates' })).toBeInTheDocument();
});
it('keeps an in-flight reply visible when the shared session has not received it yet', () => {
    fixture.waiting = true;
    fixture.items = [{ id: 'question', kind: 'user', content: 'Question' }, { id: 'answer', kind: 'assistant', content: 'Reply in progress' }];
    const shared = { localId: 'main', itemsLoaded: true, items: [fixture.items[0]], hasMore: false, loadingMore: false } as import('@/types').Session;
    render(<MainChatPanel draft="" prepared={false} onDraftChange={vi.fn()} handlers={handlers} topicsFailed={false} onRetryTopics={vi.fn()} sharedSession={shared} />);
    expect(screen.getByText('Reply in progress')).toBeInTheDocument();
});
it('splits the four meeting sections as words arrive, in English and Chinese', () => {
    expect(splitMeetingPrep("## Who's coming\nMei\n## Last time\nReviewed staging\n## Still open\nSend the memo\n## What they asked\nConfirm dates"))
        .toEqual({ who: 'Mei', last: 'Reviewed staging', open: 'Send the memo', asked: 'Confirm dates' });
    expect(splitMeetingPrep('参会的人：梅\n上次：审查测试\n还没解决的：发备忘录\n他们问过的：确认日期'))
        .toEqual({ who: '梅', last: '审查测试', open: '发备忘录', asked: '确认日期' });
});
it('shows a saved meeting prep with its made time and Refresh', () => {
    const meeting = { meetingId: 'm', meetingTitle: 'Sync', startAt: new Date(2026, 9, 30, 9).getTime(), endAt: 1, personId: 'p', personName: 'Mei' };
    const refresh = vi.fn();
    render(<MainChatPanel draft="" prepared={false} onDraftChange={vi.fn()} handlers={handlers} topicsFailed={false} onRetryTopics={vi.fn()}
        meetingPrep={{ meeting, text: 'Prepare' }} onRefreshSaved={refresh}
        savedPanel={{ status: 'ready', answer: { userItemId: 'u', answerItemId: 'a', question: 'Prepare', answer: "## Who's coming\nMei\n## Still open\nSend the memo", madeAt: new Date(2026, 9, 30, 8, 42).getTime() } }} />);
    expect(screen.getByTestId('meeting-prep-who')).toHaveTextContent('Mei');
    expect(screen.getByTestId('meeting-prep-open')).toHaveTextContent('Send the memo');
    expect(screen.queryByTestId('meeting-prep-last')).toBeNull();
    expect(screen.getByText(/Made .* ·/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Refresh' }));
    expect(refresh).toHaveBeenCalledOnce();
});
it('shows meeting section frames while the current answer is streaming', () => {
    const meeting = { meetingId: 'm', meetingTitle: 'Sync', startAt: new Date(2026, 9, 30, 9).getTime(), endAt: 1, personId: 'p', personName: 'Mei' };
    fixture.waiting = true;
    fixture.lastUserItemId = 'u';
    fixture.items = [{ id: 'u', kind: 'user', content: 'Prepare' }, { id: 'a', kind: 'assistant', content: "## Who's coming\nMei" }];
    render(<MainChatPanel draft="" prepared={false} onDraftChange={vi.fn()} handlers={handlers} topicsFailed={false} onRetryTopics={vi.fn()} meetingPrep={{ meeting, text: 'Prepare' }} />);
    expect(screen.getByTestId('meeting-prep-who')).toHaveTextContent('Mei');
    expect(screen.getByTestId('meeting-prep-last')).toBeInTheDocument();
    expect(screen.getByTestId('meeting-prep-open')).toBeInTheDocument();
    expect(screen.getByTestId('meeting-prep-asked')).toBeInTheDocument();
});
it('sends a first brief starter once without changing the composer', () => {
    const onDraftChange = vi.fn();
    const onStarterConsumed = vi.fn();
    const props = { draft: 'My unfinished message', prepared: false, onDraftChange, handlers,
        topicsFailed: false, onRetryTopics: vi.fn(), starterRequest: { id: 1, text: 'Prepare me for the meeting' }, onStarterConsumed };
    const view = render(<MainChatPanel {...props} />);
    expect(fixture.send).toHaveBeenCalledWith('Prepare me for the meeting', undefined, true);
    expect(fixture.send).toHaveBeenCalledTimes(1);
    expect(onStarterConsumed).toHaveBeenCalledWith(1);
    expect(onDraftChange).not.toHaveBeenCalled();
    view.rerender(<MainChatPanel {...props} />);
    expect(fixture.send).toHaveBeenCalledTimes(1);
});
it('retries a failed source-bound Ask with the same source and keeps unrelated draft text', () => {
    const context = { sourceKind: 'activity', sourceId: 'moment' };
    fixture.pending = { text: 'Ask about the moment', state: 'failed', context, ask: true };
    const onDraftChange = vi.fn();
    render(<MainChatPanel draft="Other words" prepared={false} onDraftChange={onDraftChange} handlers={handlers} topicsFailed={false} onRetryTopics={vi.fn()} />);
    fireEvent.click(screen.getByRole('button', { name: 'Try again' }));
    expect(fixture.send).toHaveBeenCalledWith('Ask about the moment', context, true);
    expect(onDraftChange).not.toHaveBeenCalled();
});
it.each(['main', 'topic'])('keeps the %s panel scrolled up and uses a short counted jump', (kind) => {
    const view = render(panel(kind));
    const list = screen.getByTestId(kind === 'main' ? 'main-chat-panel-list' : 'topic-panel-list');
    Object.defineProperties(list, { scrollHeight: { configurable: true, value: 2000 }, clientHeight: { configurable: true, value: 300 } });
    list.scrollTop = 100;
    fireEvent.wheel(list);
    fireEvent.scroll(list);
    expect(screen.getByTestId('jump-to-newest').textContent).toBe('');
    fixture.items = [...fixture.items, { id: 'new1', kind: 'assistant', content: 'First answer' }, { id: 'new2', kind: 'assistant', content: 'Second answer' }];
    view.rerender(panel(kind));
    expect(list.scrollTop).toBe(100);
    const jump = screen.getByRole('button', { name: '2 new replies' });
    expect(jump).toHaveTextContent(/^2$/);
    fireEvent.click(jump);
    expect(screen.queryByTestId('jump-to-newest')).not.toBeInTheDocument();
    expect(list.scrollTop).toBe(2000);
    act(() => { fixture.items = [...fixture.items, { id: 'new3', kind: 'assistant', content: 'Third answer' }]; });
    view.rerender(panel(kind));
    expect(screen.queryByTestId('jump-to-newest')).not.toBeInTheDocument();
});
it.each(['[REDACTE…', '[masked]', '[...]', 'Release [...]', 'Isoform Main chat'])('withholds a topic panel with unsafe name %s', name => {
    const { container } = render(panel('topic', name));
    expect(container).toBeEmptyDOMElement();
});

it('renders sent report rows in the Today main chat panel', () => {
    const sentAt = new Date(2026, 9, 24, 14, 10).getTime();
    fixture.items = [{
        id: 'receipt', kind: 'system', toolName: MAIN_CHAT_REPORT_TOOL,
        toolInput: { source: 'message_send', action: '', target: { kind: 'none' }, channel: 'mail', sentAt },
        localDay: '2026-10-24',
    }];
    render(panel('main'));
    const time = new Date(sentAt).toLocaleTimeString('en', { hour: 'numeric', minute: '2-digit' });
    expect(screen.getByText(`Sent via Mail, ${time}`)).toBeInTheDocument();
    expect(document.querySelector('[data-main-chat-report="message_send"]')).not.toBeNull();
    expect(screen.queryByTestId('main-chat-panel-empty')).toBeNull();
});

it('shows the same report cards as the Chat page (chat-review v1.0.3 #2)', () => {
    fixture.items = [{
        id: 'handoff-report', kind: 'system', toolName: MAIN_CHAT_REPORT_TOOL, content: 'Work finished',
        toolInput: { source: 'handoff', action: 'review', target: { kind: 'session', id: 'work-1' }, status: 'completed' },
    }];
    render(panel('main'));
    expect(document.querySelector('[data-main-chat-report="handoff"]')).not.toBeNull();
});

it('keeps the Today main chat empty state when there are no rows', () => {
    fixture.items = [];
    render(panel('main'));
    expect(screen.getByTestId('main-chat-panel-empty')).toBeInTheDocument();
    expect(screen.queryByTestId('main-chat-panel-loading')).toBeNull();
    expect(screen.queryByTestId('main-chat-panel-failed')).toBeNull();
});

it.each(['main', 'topic'])('announces the current %s panel answer growing and finishing without a new row', kind => {
    fixture.items = [...fixture.items, { id: 'current', kind: 'assistant', content: 'Started' }];
    fixture.waiting = true;
    const view = render(panel(kind));
    const list = screen.getByTestId(kind === 'main' ? 'main-chat-panel-list' : 'topic-panel-list');
    Object.defineProperties(list, { scrollHeight: { configurable: true, value: 2000 }, clientHeight: { configurable: true, value: 400 } });
    list.scrollTop = 1500;
    fireEvent.wheel(list);
    fireEvent.scroll(list);
    fixture.items = [...fixture.items.slice(0, -1), { ...fixture.items.at(-1)!, content: 'Started and grew' }];
    view.rerender(panel(kind));
    expect(screen.getByRole('button', { name: '1 new reply' })).toHaveTextContent(/^1$/);
    fixture.waiting = false;
    view.rerender(panel(kind));
    expect(screen.getByRole('button', { name: '1 new reply' })).toBeInTheDocument();
    expect(list.scrollTop).toBe(1500);
    expect(screen.getByTestId('jump-to-newest').closest('[data-topic-composer-surface]')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: '1 new reply' }));
    expect(screen.queryByTestId('jump-to-newest')).not.toBeInTheDocument();
});

it('offers source, Refresh and Chat for a completed brief answer', () => {
    const context = { sourceKind: 'brief_card', sourceId: 'chip', briefId: 'brief' };
    const onOpenMainChat = vi.fn();
    const props = { draft: '', prepared: false, onDraftChange: vi.fn(), handlers: { ...handlers, onOpenMainChat }, topicsFailed: false, onRetryTopics: vi.fn() };
    fixture.lastUserItemId = 'u';
    fixture.items = [{ id: 'u', kind: 'user', content: 'What remains?' }, { id: 'a', kind: 'assistant', content: 'The review.' }];
    const view = render(<MainChatPanel {...props} starterRequest={{ id: 9, text: 'What remains?', context }} />);
    view.rerender(<MainChatPanel {...props} starterRequest={null} />);
    expect(screen.getByTestId('brief-answer-actions')).toHaveTextContent('From your brief');
    expect(screen.queryByRole('button', { name: 'Ask a follow-up' })).not.toBeInTheDocument();
    expect(screen.queryByText('Sends to your chat.')).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Refresh' }));
    expect(fixture.send).toHaveBeenLastCalledWith('What remains?', context, true);
    fireEvent.click(screen.getByRole('button', { name: 'Open in Chat' }));
    expect(onOpenMainChat).toHaveBeenCalledOnce();
});

it('closes a saved answer with Escape and keeps the composer for follow-up', () => {
    const onLeaveSaved = vi.fn();
    render(<MainChatPanel draft="" prepared={false} onDraftChange={vi.fn()} handlers={handlers} topicsFailed={false} onRetryTopics={vi.fn()}
        onLeaveSaved={onLeaveSaved} savedPanel={{ status: 'ready', answer: { userItemId: 'u', answerItemId: 'a', question: 'What remains?', answer: 'The review.' } }} />);
    expect(screen.queryByRole('button', { name: 'Ask a follow-up' })).not.toBeInTheDocument();
    expect(screen.getByRole('textbox')).toBeInTheDocument();
    fireEvent.keyDown(document, { key: 'Escape' });
    expect(onLeaveSaved).toHaveBeenCalledOnce();
});

it('returns a streaming item to Chat when the sheet requests a close', () => {
    const onItemOpenChange = vi.fn();
    const props = { draft: '', prepared: false, onDraftChange: vi.fn(), handlers, topicsFailed: false, onRetryTopics: vi.fn(), onItemOpenChange };
    const view = render(<MainChatPanel {...props} closeItemRequest={0} starterRequest={{ id: 31, text: 'What remains?', context: { sourceKind: 'brief_card', sourceId: 'c', briefId: 'b' } }} />);
    view.rerender(<MainChatPanel {...props} closeItemRequest={0} starterRequest={null} />);
    expect(screen.getByRole('heading', { name: 'What remains?' })).toBeInTheDocument();
    view.rerender(<MainChatPanel {...props} closeItemRequest={1} starterRequest={null} />);
    expect(screen.getByRole('heading', { name: 'Chat' })).toBeInTheDocument();
    expect(onItemOpenChange).toHaveBeenLastCalledWith(false);
});

it('does not show completed-answer controls for a stopped partial reply', () => {
    fixture.stopped = true;
    fixture.lastUserItemId = 'u';
    fixture.items = [{ id: 'u', kind: 'user', content: 'What remains?' }, { id: 'a', kind: 'assistant', content: 'A partial answer' }];
    const props = { draft: '', prepared: false, onDraftChange: vi.fn(), handlers, topicsFailed: false, onRetryTopics: vi.fn() };
    const view = render(<MainChatPanel {...props} starterRequest={{ id: 7, text: 'What remains?', context: { sourceKind: 'brief_card', sourceId: 'c', briefId: 'b' } }} />);
    view.rerender(<MainChatPanel {...props} starterRequest={null} />);
    expect(screen.queryByTestId('brief-answer-actions')).toBeNull();
});


it('posts an answer chip at once and folds it to You answered · Change', async () => {
    fixture.items = [{ id: 'answer', kind: 'assistant', content: 'Should I leave PT out of your briefs?' }, { id: 'later', kind: 'user', content: 'Another topic' }];
    fixture.chips = { answer: ['Keep it out', "Include it when it's due"] };
    const onDraftChange = vi.fn();
    render(<MainChatPanel draft="" prepared={false} onDraftChange={onDraftChange} handlers={handlers} topicsFailed={false} onRetryTopics={vi.fn()} />);
    fireEvent.click(screen.getByRole('button', { name: 'Keep it out' }));
    await waitFor(() => expect(fixture.send).toHaveBeenLastCalledWith('Keep it out', null, false, false, true));
    expect(onDraftChange).not.toHaveBeenCalled();
    expect(screen.getByRole('status')).toHaveTextContent('You answered: Keep it out');
    expect(screen.queryByRole('button', { name: "Include it when it's due" })).toBeNull();
    fireEvent.click(screen.getByRole('button', { name: 'Change' }));
    expect(screen.getByRole('button', { name: "Include it when it's due" })).toBeInTheDocument();
});

it('gives the panel box the Chat page controls: attach, voice and Send (chat-review v1.0.3 #4)', async () => {
    fixture.items = [];
    const voice = { state: 'idle' as const, error: null, needsPermission: false, start: vi.fn(), stop: vi.fn(async () => 'heard words') };
    const onDraftChange = vi.fn();
    const view = render(
        <ComposerVoiceContext.Provider value={voice}>
            <MainChatPanel draft="Read this" prepared={false} onDraftChange={onDraftChange} handlers={handlers} topicsFailed={false} onRetryTopics={vi.fn()} />
        </ComposerVoiceContext.Provider>,
    );
    expect(screen.getByPlaceholderText('Ask anything…')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Attach files' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Send' })).toBeEnabled();
    fireEvent.click(screen.getByRole('button', { name: 'Voice input' }));
    expect(voice.start).toHaveBeenCalledTimes(1);
    view.rerender(
        <ComposerVoiceContext.Provider value={{ ...voice, state: 'recording' }}>
            <MainChatPanel draft="Read this" prepared={false} onDraftChange={onDraftChange} handlers={handlers} topicsFailed={false} onRetryTopics={vi.fn()} />
        </ComposerVoiceContext.Provider>,
    );
    fireEvent.click(screen.getByRole('button', { name: 'Stop recording' }));
    await waitFor(() => expect(onDraftChange).toHaveBeenLastCalledWith('Read this heard words', false));
    const input = screen.getByTestId('topic-composer-file-input') as HTMLInputElement;
    fireEvent.change(input, { target: { files: [new File(['Stretch twice a day'], 'pt-notes.txt', { type: 'text/plain' })] } });
    expect(await screen.findByText('pt-notes.txt')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Send' }));
    await waitFor(() => expect(fixture.send).toHaveBeenLastCalledWith('Read this', null, false, false, false, expect.objectContaining({
        images: [], files: [expect.objectContaining({ fileName: 'pt-notes.txt', mediaType: 'text/plain', dataBase64: expect.any(String) })],
    })));
    expect(screen.queryByText('pt-notes.txt')).toBeNull();
});
