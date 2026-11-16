import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { resetMainChatHeldActions } from '@/lib/main-chat-held-action';
import i18n from '@/i18n';
import en from '@/i18n/locales/en/chat.json';
import {
    MAIN_CHAT_PROACTIVE_TOOL,
    MAIN_CHAT_REPORT_TOOL,
    MAIN_CHAT_SETUP_TOOL,
    MAIN_CHAT_STATUS_TOOL,
    MAIN_CHAT_TEXT_REPLY_TOOL,
    SIDE_CHAT_ORIGIN_TOOL,
    TODAY_CONTEXT_TOOL,
    UPCOMING_CONFIRM_TOOL,
} from '@/lib/stream-helpers';
import type { StreamItem } from '@/types';
import { GroupedStreamList } from '../grouped-stream-list';
import { MainChatItemPanel } from '../main-chat-item-panel';
import { MainChatViewContext, type MainChatStreamView } from './main-chat-view-context';

const bindings = vi.hoisted(() => new Map<string, ReturnType<typeof vi.fn>>());
vi.mock('@/lib/wails-shim-app', () => new Proxy({}, {
    has: () => true,
    get: (_target, name: string) => {
        if (name === 'then') return undefined;
        if (!bindings.has(name)) bindings.set(name, vi.fn().mockResolvedValue({ available: false, kinds: [] }));
        return bindings.get(name);
    },
}));
vi.mock('@/lib/wails-shim-runtime', () => ({
    EventsOn: () => () => {},
    EventsEmit: vi.fn(),
    ClipboardSetText: vi.fn(),
    LogInfo: vi.fn(),
    LogWarning: vi.fn(),
}));

const TODAY = '2026-10-22';
const YESTERDAY = '2026-10-21';

function card(id: string, kind: string, rowClass: StreamItem['rowClass'], extra: Record<string, unknown> = {}, item: Partial<StreamItem> = {}): StreamItem {
    return {
        id, kind: 'system', toolName: MAIN_CHAT_PROACTIVE_TOOL, rowClass, localDay: TODAY, ...item,
        toolInput: {
            momentId: `moment-${id}`, kind, headline: `${id} headline`, body: `${id} sentence`, draft: false,
            actionIds: ['open', 'dismiss'], actionLabels: { open: `${id} open`, dismiss: 'Dismiss' },
            target: { kind: 'today' }, state: 'pending', evidence: [{ kind: 'brief', id: 'brief-1', target: { kind: 'today' } }],
            ...extra,
        },
    };
}

function report(id: string, rowClass: StreamItem['rowClass'], toolInput: Record<string, unknown>, item: Partial<StreamItem> = {}): StreamItem {
    return { id, kind: 'system', toolName: MAIN_CHAT_REPORT_TOOL, rowClass, localDay: TODAY, content: id, toolInput, ...item };
}

/** Every row type the main chat shows today, one of each. */
function everyRow(): StreamItem[] {
    return [
        // Yesterday: a conversation and a routine brief card.
        { id: 'y-user', kind: 'user', content: 'Yesterday question', rowClass: 'conversation', localDay: YESTERDAY },
        { id: 'y-answer', kind: 'assistant', content: 'Yesterday answer', rowClass: 'conversation', localDay: YESTERDAY },
        card('y-brief', 'brief_ready', 'routine', {}, { localDay: YESTERDAY }),
        // Today's routine rows: a status row, a brief card, a capture-paused
        // card and a schedule-added report.
        { id: 'status', kind: 'system', toolName: MAIN_CHAT_STATUS_TOOL, rowClass: 'routine', localDay: TODAY, toolInput: { period: `${TODAY}:morning`, entries: [
            { id: 'entry-1', kind: 'brief_missed', text: 'Your morning brief was missed.', target: { kind: 'today' } },
            { id: 'entry-2', kind: 'playbook_result', text: 'A playbook finished.', target: { kind: 'none' } },
        ] } },
        card('brief', 'brief_ready', 'routine'),
        card('capture', 'capture_paused', 'routine', { actionIds: ['resume_capture'], actionLabels: { resume_capture: 'Resume' }, captureSource: 'screen', captureCause: 'permission', captureStartAt: new Date(2026, 9, 22, 12, 21).getTime(), target: { kind: 'settings', id: 'activity' } }),
        report('schedule-added', 'routine', { source: 'schedule', status: 'created', title: 'Daily News', action: 'open', target: { kind: 'upcoming' } }),
        // Today's other rows.
        card('waiting', 'waiting_on_reply', 'waiting', {
            actionIds: ['copy_draft', 'open', 'not_now', 'dismiss', 'mute_kind'],
            actionLabels: { copy_draft: 'Copy draft', open: 'Review draft', not_now: 'Not now', dismiss: 'Dismiss', mute_kind: 'Mute this kind' },
            draft: true, draftContent: { to: 'Halcyon ops lead', subject: 'Re: ERP sync timing', text: 'We would like to hold Nov 1.' },
            evidence: [{ kind: 'mail', id: 'mail-1', target: { kind: 'mail' } }], target: { kind: 'mail' },
        }, { topicIds: ['topic-halcyon', 'topic-unknown'] }),
        card('permission', 'decision_due', 'waiting', {
            actionIds: ['approve', 'decline'], actionLabels: { approve: 'Approve', decline: 'Decline' },
            permission: { requestId: 'request-1', sessionId: 'agent-1', handoffId: 'work-1', toolName: 'Bash', details: 'Run the checks.' },
            target: { kind: 'handoff', id: 'work-1' },
        }),
        card('work', 'work_finished', 'finished', {
            actionIds: ['review', 'dismiss'], actionLabels: { review: 'Review', dismiss: 'Dismiss' },
            work: { handoffId: 'work-2', status: 'completed', summary: 'The memo is drafted.', result: '', deliverables: [] },
            target: { kind: 'handoff', id: 'work-2' },
        }),
        card('suggestion', 'suggestion', 'waiting', {
            actionIds: ['add_to_upcoming', 'do_once', 'not_now', 'dismiss'],
            actionLabels: { add_to_upcoming: 'Add to Upcoming', do_once: 'Do once', not_now: 'Not now', dismiss: 'Dismiss' },
            target: { kind: 'suggestion', id: 'initiative-1' },
        }),
        card('dismissed', 'meeting_prep', 'other', { state: 'dismissed', event: { title: 'Design review', start: '2099-01-01T10:00:00Z' } }),
        report('handoff-done', 'finished', { source: 'handoff', status: 'completed', title: 'Halcyon launch call summary', action: 'review', target: { kind: 'handoff', id: 'h-1' } }, { topicIds: ['topic-halcyon-launch'] }),
        { id: 'origin', kind: 'system', toolName: SIDE_CHAT_ORIGIN_TOOL, content: 'Branched excerpt', rowClass: 'other', localDay: TODAY, toolInput: { parentId: 'main-1', itemId: 'x' } },
        { id: 'setup', kind: 'system', toolName: MAIN_CHAT_SETUP_TOOL, rowClass: 'other', localDay: TODAY, toolInput: { step: 'microphone', answer: 'granted' } },
        { id: 'texted', kind: 'system', toolName: MAIN_CHAT_TEXT_REPLY_TOOL, content: 'Texted words', rowClass: 'other', localDay: TODAY, toolInput: {} },
        { id: 'confirm', kind: 'system', toolName: UPCOMING_CONFIRM_TOOL, rowClass: 'other', localDay: TODAY, toolInput: { action: 'remove', jobId: 'job-1', title: 'Morning news', status: 'pending', expiresAt: 0 } },
        { id: 'notice', kind: 'system', content: 'A generic notice.', rowClass: 'other', localDay: TODAY },
        { id: 'user', kind: 'user', content: 'What should I focus on?', rowClass: 'conversation', localDay: TODAY, sortOrder: 40 },
        { id: 'receipt', kind: 'context_status', toolName: TODAY_CONTEXT_TOOL, content: '{"memories":3,"activities":2,"people":1}', rowClass: 'other', localDay: TODAY, toolInput: { userItemId: 'user', memories: 3, activities: 0, people: 0 } },
        { id: 'tool', kind: 'tool', toolName: 'Read', toolInput: { file_path: '/tmp/x' }, rowClass: 'other', localDay: TODAY },
        { id: 'answer', kind: 'assistant', content: 'Countersign the Northwind SOW first.', rowClass: 'conversation', localDay: TODAY },
        { id: 'error', kind: 'error', content: 'Something failed.', rowClass: 'other', localDay: TODAY },
    ];
}

function view(overrides: Partial<MainChatStreamView> = {}): MainChatStreamView {
    return {
        today: TODAY,
        hideRoutine: false,
        topicNames: new Map([['topic-halcyon', 'Halcyon ERP sync'], ['topic-halcyon-launch', 'Halcyon launch memo']]),
        chips: {},
        ...overrides,
    };
}

function renderStream(items: StreamItem[], value: MainChatStreamView = view()) {
    return render(
        <MainChatViewContext.Provider value={value}>
            <div data-testid="stream">
                <GroupedStreamList items={items} streaming={false} onOpenReportTarget={vi.fn()} onUseTextedReply={vi.fn()} />
            </div>
            <MainChatItemPanel surface="chat" />
        </MainChatViewContext.Provider>,
    );
}

it.each(['en', 'zh-CN'])('labels the main chat answer in %s', async (language) => {
    await i18n.changeLanguage(language);
    renderStream([
        { id: 'question', kind: 'user', content: 'Question', localDay: TODAY },
        { id: 'answer', kind: 'assistant', content: 'Answer', localDay: TODAY },
    ]);
    expect(screen.getByText('Isoform')).toBeInTheDocument();
    expect(screen.getByText('Question')).toBeInTheDocument();
    expect(screen.getByText('Answer')).toBeInTheDocument();
});

it('shows a Today week answer below its question with a working Change action', async () => {
    bindings.set('GetTodayBriefByID', vi.fn().mockResolvedValue({ brief: { briefId: '2026-10-26-morning' }, weekAnswer: { answer: 'release isoform 1.0' } }));
    bindings.set('ChangeMorningBriefWeekIntent', vi.fn().mockResolvedValue(undefined));
    renderStream([{ id: 'week', kind: 'user', content: 'release isoform 1.0', weekAnswerBriefId: '2026-10-26-morning', localDay: TODAY }]);
    expect(screen.getByText('What is this week about?')).toBeInTheDocument();
    expect(screen.getByText(/You answered: release isoform 1.0/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Change' }));
    fireEvent.change(screen.getByRole('textbox', { name: 'What is this week about?' }), { target: { value: 'Ship the update' } });
    fireEvent.click(screen.getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(bindings.get('ChangeMorningBriefWeekIntent')).toHaveBeenCalledWith('2026-10-26-morning', 'release isoform 1.0', 'Ship the update'));
    expect(await screen.findByText(/You answered: Ship the update/)).toBeInTheDocument();
});

it('keeps the October 29 PT question and its answer chips visible in loaded history', () => {
    const onFillChip = vi.fn();
    renderStream([
        { id: 'pt', kind: 'assistant', content: "You said earlier to keep PT out of your briefs. Should I leave this one out as well, even when it's due today?", localDay: '2026-10-29', rowClass: 'conversation' },
        { id: 'later', kind: 'user', content: 'Another question', localDay: '2026-11-02', rowClass: 'conversation' },
    ], view({ today: '2026-11-02', chips: { pt: ['Keep it out', "Include it when it's due"] }, onFillChip }));
    expect(screen.getByText(/keep PT out of your briefs/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Keep it out' }));
    expect(onFillChip).toHaveBeenCalledWith('Keep it out');
    expect(screen.getByRole('button', { name: "Include it when it's due" })).toBeInTheDocument();
});

/** Bindings that change something on the computer and were called. */
function writesCalled(): string[] {
    return [...bindings.entries()]
        .filter(([name, fn]) => /^(Resolve|Send|Set|Persist|Delete|Create|Update|Start)/.test(name) && fn.mock.calls.length > 0)
        .map(([name]) => name);
}

function openFold(day: string) {
    const fold = document.querySelector(`[data-main-chat-fold="${day}"]`) as HTMLElement;
    fireEvent.click(within(fold).getByRole('button', { expanded: false }));
    return fold;
}

beforeEach(async () => {
    await i18n.changeLanguage('en');
    bindings.clear();
    window.localStorage.clear();
});
afterEach(() => {
    cleanup();
    resetMainChatHeldActions();
});

describe('main chat stream (RC 2 D1)', () => {
    it.each(['en', 'zh-CN'])('keeps completed work collapsed after the answer in %s', async (language) => {
        await i18n.changeLanguage(language);
        renderStream([
            { id: 'u', kind: 'user', content: 'Check it' },
            { id: 'bash', kind: 'tool', toolName: 'Bash', toolInput: { command: 'pwd' } },
            { id: 'a', kind: 'assistant', content: 'The answer' },
        ]);
        const details = screen.getByRole('button', { name: i18n.t('chat:tools.answerDetails') });
        expect(screen.getByText('The answer').closest('.text-ink')).not.toBeNull();
        if (language === 'en') expect(details).toHaveTextContent('How Isoform worked this out');
        expect(details).toHaveAttribute('aria-expanded', 'false');
        expect(details).not.toHaveTextContent('Bash');
        expect(screen.getByText('The answer').compareDocumentPosition(details) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
        fireEvent.click(details);
        expect(details).toHaveAttribute('aria-expanded', 'true');
        expect(screen.getByText(/Bash/, { selector: 'p' })).toBeVisible();
    });

    it('keeps muted permission cards visible and opens their answers in the shared panel', async () => {
        renderStream([card('permission', 'decision_due', 'waiting', {
            kindMuted: true,
            permission: { requestId: 'request', sessionId: 'runner', handoffId: 'work', toolName: 'Bash', details: 'Run checks.' },
            actionIds: ['approve', 'decline'], actionLabels: { approve: 'Approve', decline: 'Decline' },
        })], view({ hideRoutine: true }));
        const permission = document.querySelector('[data-main-chat-proactive="decision_due"]') as HTMLElement;
        fireEvent.click(within(permission).getByRole('button', { name: en.mainChat.action.review }));
        const panel = await screen.findByTestId('main-chat-item-panel');
        expect(panel.querySelector('[data-proactive-action="approve"]')).not.toBeNull();
        expect(panel.querySelector('[data-proactive-action="decline"]')).not.toBeNull();
        expect(document.querySelector('[data-main-chat-fold], [data-proactive-folded]')).toBeNull();
    });

    it('keeps every row type under Everything and opens card actions in the shared panel', async () => {
        renderStream(everyRow());
        // Conversation rows and the turn's processing block.
        expect(screen.getByText('Yesterday question')).toBeInTheDocument();
        expect(screen.getByText('Yesterday answer')).toBeInTheDocument();
        expect(screen.getByText('What should I focus on?')).toBeInTheDocument();
        expect(screen.getByText('Countersign the Northwind SOW first.')).toBeInTheDocument();
        expect(screen.getAllByRole('button', { name: en.tools.answerDetails }).length).toBeGreaterThan(0);
        expect(document.querySelector('[data-today-context-status]')).toHaveTextContent('3 memories');
        // Waiting cards with every action.
        const waiting = document.querySelector('[data-main-chat-proactive="waiting_on_reply"]') as HTMLElement;
        fireEvent.click(within(waiting).getByRole('button', { name: en.mainChat.action.review }));
        const itemPanel = await screen.findByTestId('main-chat-item-panel');
        // Copy draft is in the card's ··· (Chat review C081).
        expect(itemPanel.querySelector('[data-proactive-action="copy_draft"]')).toBeNull();
        expect(within(itemPanel).getByRole('button', { name: en.mainChat.proactive.moreActions })).toBeInTheDocument();
        const permission = document.querySelector('[data-main-chat-proactive="decision_due"]') as HTMLElement;
        fireEvent.click(within(permission).getByRole('button', { name: en.mainChat.action.review }));
        await waitFor(() => expect(itemPanel).toHaveAttribute('data-panel-open', 'moment-permission'));
        expect(itemPanel.querySelector('[data-proactive-action="approve"]')).not.toBeNull();
        expect(itemPanel.querySelector('[data-proactive-action="decline"]')).not.toBeNull();
        expect(within(permission).getByText('Run the checks.')).toBeInTheDocument();
        const suggestion = document.querySelector('[data-main-chat-proactive="suggestion"]') as HTMLElement;
        expect(within(suggestion).getByRole('button', { name: en.mainChat.card.button.doIt })).toBeInTheDocument();
        // Finished work, a resolved card and every other system row.
        expect(within(document.querySelector('[data-main-chat-proactive="work_finished"]') as HTMLElement).getByRole('button', { name: 'Review' })).toBeInTheDocument();
        expect(within(document.querySelector('[data-main-chat-proactive="meeting_prep"]') as HTMLElement).getByRole('status')).toHaveTextContent(en.mainChat.proactive.held.notNow);
        expect(screen.getByText('“Halcyon launch call summary” is done. The result is ready to review.')).toBeInTheDocument();
        expect(within(document.querySelector('[data-main-chat-report="handoff"]') as HTMLElement).getByRole('button', { name: en.mainChat.action.review })).toBeInTheDocument();
        expect(within(document.querySelector('[data-side-chat-origin]') as HTMLElement).getByText(en.mainChat.origin.title)).toBeInTheDocument();
        const texted = document.querySelector('[data-texted-reply]') as HTMLElement;
        expect(within(texted).getByText('Texted words')).toBeInTheDocument();
        expect(within(texted).getByRole('button', { name: en.mainChat.texted.use })).toBeInTheDocument();
        const confirm = document.querySelector('[data-upcoming-confirm]') as HTMLElement;
        expect(within(confirm).getByText(en.mainChat.confirm.question.replace('{{title}}', 'Morning news'))).toBeInTheDocument();
        expect(within(confirm).getByRole('button', { name: en.mainChat.confirm.remove })).toBeInTheDocument();
        expect(within(confirm).getByRole('button', { name: en.mainChat.confirm.keep })).toBeInTheDocument();
        expect(screen.getByText('A generic notice.')).toBeInTheDocument();
        expect(screen.getAllByText(en.errors.cause.assistantErrorUnknown).length).toBeGreaterThan(0);
        expect(document.querySelector('[data-main-chat-proactive="waiting_on_reply"]')?.closest('[data-testid="stream"]')).not.toBeNull();
    });

    it('folds each day’s routine updates into one row after that day’s divider, and opens it in place', () => {
        renderStream(everyRow());
        const stream = screen.getByTestId('stream');
        const markers = [...stream.querySelectorAll('[data-main-chat-day], [data-main-chat-fold]')].map((node) =>
            node.getAttribute('data-main-chat-day') ? `day:${node.getAttribute('data-main-chat-day')}` : `fold:${node.getAttribute('data-main-chat-fold')}`);
        expect(markers).toEqual([`day:${YESTERDAY}`, `fold:${YESTERDAY}`, `day:${TODAY}`, `fold:${TODAY}`]);
        expect(screen.getByRole('separator', { name: 'Yesterday' })).toBeInTheDocument();
        expect(screen.getByRole('separator', { name: 'Today' })).toBeInTheDocument();
        // Two status entries, the brief card, the capture card and the schedule report.
        expect(screen.getByRole('button', { name: '5 updates from Isoform' })).toHaveAttribute('aria-expanded', 'false');
        expect(screen.getByRole('button', { name: '1 update from Isoform' })).toBeInTheDocument();
        // Folded rows are not in the conversation until the fold opens.
        expect(screen.queryByText('capture headline')).toBeNull();
        expect(screen.queryByText('Your morning brief was missed.')).toBeNull();

        const fold = openFold(TODAY);
        expect(within(fold).getByText('brief headline')).toBeInTheDocument();
        expect(within(fold).getByText(/Stopped watching your screen at 12:21 PM: Screen Recording was turned off\./)).not.toHaveTextContent('From');
        expect(within(fold).getByRole('button', { name: en.mainChat.observationPause.turnBackOn })).toBeInTheDocument();
        expect(within(fold).queryByRole('button', { name: en.mainChat.card.button.resume })).toBeNull();
        expect(within(fold).getByText('Your morning brief was missed.')).toBeInTheDocument();
        expect(within(fold).getByText('A playbook finished.')).toBeInTheDocument();
        expect(within(fold).getByText('Added “Daily News” to your schedules.')).toBeInTheDocument();
        expect(within(fold.querySelector('[data-main-chat-report="schedule"]') as HTMLElement).getByRole('button', { name: en.mainChat.action.open })).toBeInTheDocument();
        expect(within(fold.querySelector('[data-main-chat-proactive="brief_ready"]') as HTMLElement).getByRole('button', { name: en.mainChat.card.button.readIt })).toBeInTheDocument();
        expect(within(fold).getAllByRole('button', { name: en.mainChat.action.open }).length).toBeGreaterThan(0);
        expect(within(fold).getByText(en.mainChat.view.foldNote)).toBeInTheDocument();
        expect(en.mainChat.view.foldNote).toContain('Settings › Notifications');
        // The note shows on the first fold open only (chat-review v1.0.10 H8).
        const toggle = within(fold).getByRole('button', { name: '5 updates from Isoform' });
        fireEvent.click(toggle);
        fireEvent.click(toggle);
        expect(within(fold).queryByText(en.mainChat.view.foldNote)).toBeNull();
        expect(within(openFold(YESTERDAY)).queryByText(en.mainChat.view.foldNote)).toBeNull();
        // Opening a fold calls nothing on the computer.
        expect(writesCalled()).toEqual([]);
    });

    it('hides the folds, and only the folds, when routine updates are hidden', () => {
        renderStream(everyRow(), view({ hideRoutine: true }));
        expect(document.querySelector('[data-main-chat-fold]')).toBeNull();
        // Yesterday still has its conversation, so its divider stays.
        expect(screen.getByRole('separator', { name: 'Yesterday' })).toBeInTheDocument();
        expect(screen.getByText('waiting headline')).toBeInTheDocument();
        expect(screen.getByText('What should I focus on?')).toBeInTheDocument();
    });

    it('keeps a day that only had routine updates when they are hidden, without its fold (R63)', () => {
        renderStream([card('old-brief', 'brief_ready', 'routine', {}, { localDay: YESTERDAY }), { id: 'u', kind: 'user', content: 'Hi', localDay: TODAY }], view({ hideRoutine: true }));
        expect(screen.getByRole('separator', { name: 'Yesterday' })).toBeInTheDocument();
        expect(screen.getByRole('separator', { name: 'Today' })).toBeInTheDocument();
        expect(document.querySelector('[data-main-chat-fold]')).toBeNull();
    });

    it('puts each card in review order: headline, sentence, draft, source, topic tag, buttons', () => {
        renderStream(everyRow());
        const waiting = document.querySelector('[data-main-chat-proactive="waiting_on_reply"]') as HTMLElement;
        const order = [
            within(waiting).getByRole('heading', { name: 'waiting headline' }),
            within(waiting).getByText('waiting sentence'),
            waiting.querySelector('[data-proactive-draft]')!,
            within(waiting).getByText('From Mail'),
            waiting.querySelector('[data-main-chat-topic-tags]')!,
            within(waiting).getByRole('button', { name: 'Review' }),
        ];
        for (let index = 1; index < order.length; index++) {
            expect(order[index - 1].compareDocumentPosition(order[index]) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
        }
        const draft = waiting.querySelector('[data-proactive-draft]') as HTMLElement;
        expect(draft).toHaveTextContent('ToHalcyon ops lead');
        expect(draft).toHaveTextContent('SubjectRe: ERP sync timing');
        expect(draft).toHaveTextContent('We would like to hold Nov 1.');
        // A tag shows only a topic the registry names; never an id.
        const tags = within(waiting.querySelector('[data-main-chat-topic-tags]') as HTMLElement).getAllByRole('listitem');
        expect(tags.map((tag) => tag.textContent)).toEqual(['Halcyon ERP sync']);
        expect(screen.queryByText('topic-unknown')).toBeNull();
        // Report lines carry their tag too.
        const handoff = document.querySelector('[data-main-chat-report="handoff"]') as HTMLElement;
        expect(within(handoff).getByText('Halcyon launch memo')).toBeInTheDocument();
    });

    it('shows no brief card: the brief lives on Today (pass-questions v1.0.0 rule 8)', () => {
        const { container } = renderStream([card('brief', 'brief_ready', 'waiting', { briefId: 'brief-1' }, { topicIds: ['topic-halcyon'] })]);
        expect(container.querySelector('[data-main-chat-proactive="brief_ready"]')).toBeNull();
        expect(screen.queryByRole('button', { name: en.mainChat.card.button.hearIt })).toBeNull();
    });

    it('shows prepared chips under the latest answer; a chip fills the box and sends nothing', () => {
        const onFillChip = vi.fn();
        renderStream(everyRow(), view({ chips: { answer: ['Hold 3:00 to 4:30 for the memo', 'Open the Northwind SOW'] }, onFillChip }));
        const chips = within(document.querySelector('[data-main-chat-chips="answer"]') as HTMLElement).getAllByRole('button');
        expect(chips.map((chip) => chip.textContent)).toEqual(['Hold 3:00 to 4:30 for the memo', 'Open the Northwind SOW']);
        expect(chips[0]).toHaveClass('rounded-lg');
        expect(chips[0]).not.toHaveClass('rounded-full');
        fireEvent.click(chips[0]);
        expect(onFillChip).toHaveBeenCalledWith('Hold 3:00 to 4:30 for the memo');
        expect(writesCalled()).toEqual([]);
    });

    it('shows no chips while the answer is still streaming', () => {
        render(
            <MainChatViewContext.Provider value={view({ chips: { live: ['Next step'] }, onFillChip: vi.fn() })}>
                <GroupedStreamList streaming items={[{ id: 'u', kind: 'user', content: 'Q' }, { id: 'live', kind: 'assistant', content: 'Partial' }]} />
            </MainChatViewContext.Provider>,
        );
        expect(screen.queryByRole('button', { name: 'Next step' })).toBeNull();
    });

    it('leaves every other chat as it was: no dividers, folds, tags or chips', () => {
        render(<GroupedStreamList streaming={false} items={everyRow()} />);
        expect(document.querySelector('[data-main-chat-day], [data-main-chat-fold], [data-main-chat-topic-tags], [data-main-chat-chips]')).toBeNull();
        // Routine rows stay in the flow there.
        expect(screen.getByText('capture headline')).toBeInTheDocument();
    });
});

it.each(['[REDACTE…', '[masked]', '[...]', 'Release [...]', 'Isoform Main chat'])('withholds unsafe topic tags in the stream and panel: %s', name => {
    renderStream(everyRow(), view({ topicNames: new Map([['topic-halcyon', name], ['topic-halcyon-launch', 'Halcyon launch memo']]) }));
    expect(screen.queryByText(name)).not.toBeInTheDocument();
    expect(screen.getByText('Halcyon launch memo')).toBeInTheDocument();
});
