import { MainChatViewContext } from './main-chat/main-chat-view-context';
import { act, fireEvent, render as renderUI, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { isValidElement, useLayoutEffect, useState, type ComponentProps, type ReactElement } from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import i18n from '@/i18n';
import en from '@/i18n/locales/en/chat.json';
import zh from '@/i18n/locales/zh-CN/chat.json';
import enToday from '@/i18n/locales/en/today.json';
import enApp from '@/i18n/locales/en/app.json';
import zhToday from '@/i18n/locales/zh-CN/today.json';
import appSource from '../App.tsx?raw';
import { computerClock, setComputerClock } from '@/lib/brief-time';
import zhApp from '@/i18n/locales/zh-CN/app.json';
import { MAIN_CHAT_PROACTIVE_TOOL, MAIN_CHAT_STATUS_TOOL } from '@/lib/stream-helpers';
import { publishEngagementSettings } from '@/lib/engagement-settings-events';
import { beginOpenAnswerDelivery, registerDeliveredOpenAnswers } from '@/hooks/useOpenAnswers';
import type { StreamItem } from '@/types';
import { MainChatProactiveCard, MainChatStatusRow } from './main-chat-proactive-card';
import { TODAY_BRIEF_STATUS_TARGET } from '@/lib/main-chat-navigation';
import { MAIN_CHAT_HELD_ACTION_MS, resetMainChatHeldActions } from '@/lib/main-chat-held-action';
import { MAIN_CHAT_DRAFT_SEND_HOLD_MS, resetMainChatDraftSends } from '@/lib/main-chat-draft-send';
import { MainChatItemPanel } from './main-chat-item-panel';
import { useMainChatItemPanel } from '@/hooks/useMainChatItemPanel';
import { StreamCard } from './stream-card';
import { NotificationsSettings, useEngagementSettingsState } from './notifications-settings';

type MainChatProactiveCardProps = ComponentProps<typeof MainChatProactiveCard>;

// The Notifications page's settings, with the page's own shared state.
function NotificationSettingsPage() {
    const state = useEngagementSettingsState();
    return <NotificationsSettings state={state} />;
}
const resolveAction = vi.hoisted(() => vi.fn());
const sendDraftAction = vi.hoisted(() => vi.fn());
const correctFact = vi.hoisted(() => vi.fn());
const loadItem = vi.hoisted(() => vi.fn());
const recordReceipt = vi.hoisted(() => vi.fn());
const settingsMocks = vi.hoisted(() => ({ get: vi.fn(), setKind: vi.fn(), week: vi.fn() }));
const runtimeListeners = vi.hoisted(() => new Map<string, Set<(value: unknown) => void>>());
const copyDraft = vi.hoisted(() => vi.fn());
const nativeCopyDraft = vi.hoisted(() => vi.fn());
const openAnswerMocks = vi.hoisted(() => ({ list: vi.fn(), change: vi.fn(), cancel: vi.fn(), notForMe: vi.fn(), undo: vi.fn() }));
const briefByID = vi.hoisted(() => vi.fn());
const taskDetail = vi.hoisted(() => vi.fn());
const taskAction = vi.hoisted(() => vi.fn());
const draftTaskID = vi.hoisted(() => vi.fn());
vi.mock('../../wailsjs/go/main/App', () => ({
    GetDesktopTaskThread: (...args: unknown[]) => taskDetail(...args),
    ActOnDesktopTask: (...args: unknown[]) => taskAction(...args),
    DesktopDraftTaskID: (...args: unknown[]) => draftTaskID(...args),
    ReplyToDesktopTask: vi.fn(), AbortHandoff: vi.fn(),
    OpenAutomationDeliverable: vi.fn(),
    OpenExternalURL: vi.fn(),
    LoadMainChatProactiveItem: (...args: unknown[]) => loadItem(...args),
    GetTodayBriefByID: (...args: unknown[]) => briefByID(...args),
    ReturnToMainChatPlace: vi.fn().mockResolvedValue(false),
    NativeClipboardSetText: (...args: unknown[]) => nativeCopyDraft(...args),
    ResolveMainChatProactiveAction: (...args: unknown[]) => resolveAction(...args),
    SendMainChatProactiveDraft: (...args: unknown[]) => sendDraftAction(...args),
    CorrectMainChatProactiveFact: (...args: unknown[]) => correctFact(...args),
    RecordMainChatProactiveReceipt: (...args: unknown[]) => recordReceipt(...args),
    GetEngagementSettings: settingsMocks.get,
    SetEngagementKindEnabled: settingsMocks.setKind,
    GetEngagementWeekSummary: settingsMocks.week,
    SetEngagementLevel: vi.fn(),
    SetEngagementRules: vi.fn(),
    ListOpenAnswers: openAnswerMocks.list,
    ChangeOpenAnswer: openAnswerMocks.change,
    CancelOpenAnswer: openAnswerMocks.cancel,
    NotForMeAnswer: openAnswerMocks.notForMe,
    UndoNotForMeAnswer: openAnswerMocks.undo,
}));
vi.mock('../../wailsjs/runtime/runtime', () => ({
    EventsOn: vi.fn((name: string, callback: (value: unknown) => void) => {
        const listeners = runtimeListeners.get(name) ?? new Set();
        listeners.add(callback);
        runtimeListeners.set(name, listeners);
        return () => {
            listeners.delete(callback);
            if (listeners.size === 0) runtimeListeners.delete(name);
        };
    }),
    EventsEmit: vi.fn(),
    ClipboardSetText: (...args: unknown[]) => copyDraft(...args),
}));

function proactive(overrides: Record<string, unknown> = {}): StreamItem {
    return {
        id: 'proactive-1', kind: 'system', toolName: MAIN_CHAT_PROACTIVE_TOOL,
        content: 'Your brief is ready. Open your brief when you have a moment.',
        toolInput: {
            momentId: 'moment-1', kind: 'brief_ready', headline: 'Your brief is ready.', body: 'Open your brief when you have a moment.',
            draft: false, actionIds: ['open', 'dismiss'], actionLabels: { open: 'Play', dismiss: 'Dismiss' },
            target: { kind: 'today' }, state: 'pending', evidence: [{ kind: 'brief', id: 'brief-1', target: { kind: 'today' } }],
            ...(overrides.kind === 'meeting_prep' ? { event: { title: 'Design review', start: '2099-01-01T10:00:00Z' } } : {}), ...overrides,
        },
    };
}

/** A brief with its words and a recording, as the brief service reads it. */
function readyBrief(briefId: string) {
    return {
        briefId, slot: 'morning', asOf: '2026-10-28T13:50:00Z', timezone: 'UTC', narrativeStatus: 'ok',
        narrative: [{ text: 'Good morning. Three meetings are scheduled.', refs: [] }, { text: 'Your first call is at nine.', refs: [] }],
        audio: { mime: 'audio/wav', bytes: 10, durationSeconds: 3 },
    } as never;
}

function TodayCardPanelHarness({ item }: { item: StreamItem }) {
    const panel = useMainChatItemPanel("today");
    return (
        <>
            <MainChatProactiveCard item={item} compact panelSurface="today" />
            <MainChatItemPanel surface="today" />
            <button onClick={panel.close}>Reset panel</button>
        </>
    );
}

function TodayExpandableCardPanelHarness({ item }: { item: StreamItem }) {
    const panel = useMainChatItemPanel("today");
    return <>
        <MainChatProactiveCard item={item} compact panelSurface="today" />
        <MainChatItemPanel surface="today" />
        <button onClick={panel.close}>Reset panel</button>
    </>;
}

function CardPanelHarness({ item, ...props }: MainChatProactiveCardProps) {
    const surface = props.panelSurface ?? 'chat';
    return <>
        <MainChatProactiveCard item={item} {...props} />
        <MainChatItemPanel surface={surface} onOpenTarget={props.onOpenTarget} />
    </>;
}

function ClearItemPanels() {
    const chat = useMainChatItemPanel('chat');
    const today = useMainChatItemPanel('today');
    useLayoutEffect(() => {
        chat.close();
        today.close();
    }, [chat.close, today.close]);
    return null;
}

function PrivacyPanelProbe({ surface }: { surface: 'chat' | 'today' }) {
    const panel = useMainChatItemPanel(surface);
    return <>
        <button onClick={() => panel.openItem(proactive())}>Open</button>
        <button onClick={() => void panel.openMoment('late-card')}>Load</button>
        <output data-testid="panel-open-id">{panel.openId}</output>
        <MainChatItemPanel surface={surface} />
    </>;
}

function render(element: ReactElement) {
    if (isValidElement(element) && element.type === MainChatProactiveCard) {
        const props = element.props as MainChatProactiveCardProps;
        return renderUI(<CardPanelHarness {...props} />);
    }
    return renderUI(element);
}

async function openCardPanel(label: string) {
    fireEvent.click(screen.getByRole('button', { name: label }));
    await waitFor(() => expect(screen.getByTestId('main-chat-item-panel')).toHaveAttribute('data-panel-open'));
}

async function clickMenuAction(label: string, menuLabel = en.mainChat.proactive.moreActions) {
    const user = userEvent.setup();
    await user.click(screen.getByRole('button', { name: menuLabel }));
    await user.click(await screen.findByRole('menuitem', { name: label }));
}

function sendDraft(draftOverrides: Record<string, unknown> = {}, overrides: Record<string, unknown> = {}): StreamItem {
    return proactive({
        kind: 'waiting_on_reply', headline: 'Dana is waiting on your reply.', body: 'Review the draft before sending.',
        draft: true,
        draftContent: {
            to: 'dana@example.com', subject: 'Re: Planning', text: 'Dana, Thursday works for me.',
            channel: 'mail', recipient: 'Dana', principalId: 'person-dana', revision: 'draft-revision-1',
            ...draftOverrides,
        },
        actionIds: ['send', 'copy_draft', 'open', 'dismiss'],
        actionLabels: { send: 'Stored send label', copy_draft: 'Copy draft', open: 'Stored open label', dismiss: 'Dismiss' },
        target: { kind: 'mail' },
        ...overrides,
    });
}

function status(): StreamItem {
    return {
        id: 'status-1', kind: 'system', toolName: MAIN_CHAT_STATUS_TOOL,
        toolInput: { period: '2026-10-16:morning', entries: [
            { id: 'capture-1', kind: 'capture_paused', text: 'Screen capture has been off for 20 minutes.', target: { kind: 'settings', id: 'notifications' } },
            { id: 'digest-1', kind: 'digest_done', text: 'Your digest is ready.', target: { kind: 'today' } },
        ] },
    };
}

function work(overrides: Record<string, unknown> = {}): StreamItem {
    return proactive({
        kind: 'work_finished', headline: 'Your work is ready.', body: 'Review the result when you have a moment.',
        actionIds: ['review', 'stop', 'dismiss'], actionLabels: { review: 'Review', stop: 'Stop', dismiss: 'Dismiss' },
        target: { kind: 'handoff', id: 'work-1' }, evidence: [{ kind: 'handoff', id: 'work-1', target: { kind: 'handoff', id: 'work-1' } }],
        work: { handoffId: 'work-1', status: 'completed', summary: 'The comparison is ready.', result: 'The full comparison.', deliverables: [{ label: 'Comparison', url: '/tmp/comparison.md' }] },
        ...overrides,
    });
}

function completedWork(kind: 'work_finished' | 'playbook_result'): StreamItem {
    return proactive({
        kind,
        headline: kind === 'work_finished' ? 'Completed task' : 'Playbook result',
        body: 'A short summary.',
        actionIds: ['review', 'dismiss'],
        actionLabels: { review: 'Review', dismiss: 'Dismiss' },
        target: kind === 'work_finished' ? { kind: 'handoff', id: 'work-1' } : { kind: 'playbook', id: 'playbook-1' },
        work: {
            handoffId: 'work-1',
            status: 'completed',
            summary: 'Summary differs from the complete result.',
            result: 'Full privacy-projected result: all implementation checks completed.',
            deliverables: [
                { label: 'Implementation report', url: '/tmp/implementation.md' },
                { label: 'Test results', url: '/tmp/tests.txt' },
            ],
        },
    });
}

function permission(overrides: Record<string, unknown> = {}): StreamItem {
    return proactive({
        kind: 'decision_due', headline: 'Your work needs permission.', body: 'Review this request to continue.',
        actionIds: ['approve', 'decline'], actionLabels: { approve: 'Approve', decline: 'Decline' },
        target: { kind: 'handoff', id: 'work-1' }, evidence: [{ kind: 'handoff', id: 'work-1', target: { kind: 'handoff', id: 'work-1' } }],
        permission: { requestId: 'request-1', sessionId: 'agent-1', handoffId: 'work-1', toolName: 'Bash', details: 'Run the repository checks.' },
        ...overrides,
    });
}

function openFoldedLine() {
    const details = document.querySelector<HTMLDetailsElement>('[data-proactive-folded]');
    expect(details).not.toBeNull();
    details!.open = true;
    fireEvent(details!, new Event('toggle'));
}

/** Held card actions wait 5 seconds; real time still runs for findBy. */
function holdTimers() {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'], shouldAdvanceTime: true });
}

async function passCardHold() {
    await act(async () => { vi.advanceTimersByTime(MAIN_CHAT_HELD_ACTION_MS); });
}

async function openMutedLine() {
    await waitFor(() => expect(document.querySelector('[data-proactive-folded]') ?? screen.queryByRole('status')).not.toBeNull());
    if (document.querySelector('[data-proactive-folded]')) openFoldedLine();
}

beforeEach(() => {
    resetMainChatDraftSends();
    resetMainChatHeldActions();
    sendDraftAction.mockReset();
});

afterEach(() => {
    vi.useRealTimers();
});

describe('main chat proactive messages', () => {
    beforeEach(() => {
        loadItem.mockReset();
        const resetPanels = renderUI(<ClearItemPanels />);
        resetPanels.unmount();
    });

    const privacyEvents = [
        ['Forget', 'memory:withdrawn'],
        ['protected-app change', 'sensitive-apps:updated'],
        ['Closed Eyes', 'closed-eyes:changed'],
        ['model-account sign-out', 'model-accounts:updated'],
        ['session deletion', 'session:deleted'],
    ] as const;
    const emitRuntimeEvent = (event: string, payload: unknown) => {
        for (const listener of runtimeListeners.get(event) ?? []) listener(payload);
    };
    const emitWithdrawal = (event: string) => {
        const listeners = runtimeListeners.get(event);
        if (!listeners?.size) throw new Error(`No listener for ${event}`);
        if (event === 'model-accounts:updated') {
            emitRuntimeEvent(event, { accounts: [{ provider: 'test', kind: 'subscription', name: 'account', on: true }] });
            emitRuntimeEvent(event, { accounts: [] });
        } else {
            emitRuntimeEvent(event, undefined);
        }
    };

    it.each(privacyEvents.flatMap(([name, event]) => (['chat', 'today'] as const).map(surface => ({ name, event, surface }))))(
        'clears an open $name panel immediately on $surface', ({ event, surface }) => {
            renderUI(<PrivacyPanelProbe surface={surface} />);
            fireEvent.click(screen.getByRole('button', { name: 'Open' }));
            expect(screen.getByTestId('main-chat-item-panel')).toHaveAttribute('data-panel-open', 'moment-1');
            act(() => emitWithdrawal(event));
            expect(screen.queryByTestId('main-chat-item-panel')).toBeNull();
            expect(screen.getByTestId('panel-open-id')).toBeEmptyDOMElement();
        },
    );

    it.each(privacyEvents.flatMap(([name, event]) => (['chat', 'today'] as const).map(surface => ({ name, event, surface }))))(
        'ignores a deferred $name load on $surface after $event', async ({ event, surface }) => {
            let finish!: (value: unknown) => void;
            loadItem.mockReturnValue(new Promise(resolve => { finish = resolve; }));
            renderUI(<PrivacyPanelProbe surface={surface} />);
            fireEvent.click(screen.getByRole('button', { name: 'Load' }));
            expect(loadItem).toHaveBeenCalledWith('late-card');
            act(() => emitWithdrawal(event));
            await act(async () => finish({
                id: 'late-card-row',
                kind: 'system',
                content: 'Late result',
                toolName: MAIN_CHAT_PROACTIVE_TOOL,
                toolInput: JSON.stringify({
                    momentId: 'late-card',
                    kind: 'brief_ready',
                    headline: 'Late card',
                    body: 'Stale private content',
                    actionIds: ['open'],
                    actionLabels: { open: 'Play' },
                    target: { kind: 'today' },
                    state: 'pending',
                }),
            }));
            expect(screen.queryByTestId('main-chat-item-panel')).toBeNull();
            expect(screen.getByTestId('panel-open-id')).toBeEmptyDOMElement();
        },
    );

    it.each(['chat', 'today'] as const)(
        'keeps the open panel fresh after a live activity update on $surface',
        async (surface) => {
            loadItem.mockResolvedValue({
                id: 'refreshed-card-row',
                kind: 'system',
                content: 'Refreshed content',
                toolName: MAIN_CHAT_PROACTIVE_TOOL,
                toolInput: JSON.stringify({
                    momentId: 'moment-1',
                    kind: 'brief_ready',
                    headline: 'Refreshed headline',
                    body: 'Refreshed body',
                    actionIds: ['open'],
                    actionLabels: { open: 'Play' },
                    target: { kind: 'today' },
                    state: 'pending',
                }),
            });
            renderUI(<PrivacyPanelProbe surface={surface} />);
            fireEvent.click(screen.getByRole('button', { name: 'Open' }));
            expect(screen.getByTestId('main-chat-item-panel')).toHaveAttribute('data-panel-open', 'moment-1');

            await act(async () => {
                emitRuntimeEvent('activity:activities-updated', undefined);
            });

            expect(loadItem).toHaveBeenCalledWith('moment-1');
            expect(await screen.findByRole('heading', { level: 2, name: 'Refreshed headline' })).toBeInTheDocument();
            expect(screen.getByTestId('main-chat-item-panel')).toHaveAttribute('data-panel-open', 'moment-1');
        },
    );

    it.each(['chat', 'today'] as const)(
        'hides stale panel text while revalidating on $surface',
        async (surface) => {
            let finish!: (value: unknown) => void;
            loadItem.mockReturnValue(new Promise(resolve => { finish = resolve; }));
            renderUI(<PrivacyPanelProbe surface={surface} />);
            fireEvent.click(screen.getByRole('button', { name: 'Open' }));
            expect(screen.getByTestId('main-chat-item-panel')).toHaveTextContent('Brief');

            act(() => emitRuntimeEvent('activity:activities-updated', undefined));
            const panel = screen.getByTestId('main-chat-item-panel');
            expect(panel).toHaveAttribute('data-panel-open', 'moment-1');
            expect(within(panel).getByRole('status')).toHaveTextContent(en.mainChat.proactive.revalidating);
            expect(within(panel).queryByRole('heading', { name: 'Your brief is ready.' })).toBeNull();

            await act(async () => finish({
                id: 'fresh-card-row',
                kind: 'system',
                content: 'Fresh content',
                toolName: MAIN_CHAT_PROACTIVE_TOOL,
                toolInput: JSON.stringify({
                    momentId: 'moment-1', kind: 'brief_ready', headline: 'Fresh headline', body: 'Fresh body',
                    actionIds: ['open'], actionLabels: { open: 'Play' }, target: { kind: 'today' }, state: 'pending',
                }),
            }));
            expect(within(screen.getByTestId('main-chat-item-panel')).getAllByRole('heading', { name: 'Fresh headline' })[0]).toBeInTheDocument();
        },
    );

    it.each(['empty', 'rejection'] as const)(
        'clears a revalidating panel when the source is deleted by $surface response',
        async (response) => {
            if (response === 'empty') loadItem.mockResolvedValue(null);
            else loadItem.mockRejectedValue(new Error('deleted'));
            renderUI(<PrivacyPanelProbe surface="chat" />);
            fireEvent.click(screen.getByRole('button', { name: 'Open' }));
            act(() => emitRuntimeEvent('activity:activities-updated', undefined));
            await waitFor(() => expect(screen.queryByTestId('main-chat-item-panel')).toBeNull());
        },
    );

    it.each(['chat', 'today'] as const)(
        'clears the panel when an activity update can no longer load the item on $surface',
        async (surface) => {
            loadItem.mockResolvedValue(null);
            renderUI(<PrivacyPanelProbe surface={surface} />);
            fireEvent.click(screen.getByRole('button', { name: 'Open' }));
            expect(screen.getByTestId('main-chat-item-panel')).toHaveAttribute('data-panel-open', 'moment-1');

            await act(async () => {
                emitRuntimeEvent('activity:activities-updated', undefined);
            });

            await waitFor(() => {
                expect(screen.queryByTestId('main-chat-item-panel')).toBeNull();
                expect(screen.getByTestId('panel-open-id')).toBeEmptyDOMElement();
            });
        },
    );

    it.each(['close', 'privacy reset'] as const)(
        'ignores a late activity revalidation reply after $reset',
        async (reset) => {
            let finish!: (value: unknown) => void;
            loadItem.mockReturnValue(new Promise(resolve => { finish = resolve; }));
            renderUI(<PrivacyPanelProbe surface="chat" />);
            fireEvent.click(screen.getByRole('button', { name: 'Open' }));
            await act(async () => {
                emitRuntimeEvent('activity:activities-updated', undefined);
            });
            expect(loadItem).toHaveBeenCalledWith('moment-1');

            if (reset === 'close') {
                fireEvent.click(screen.getByRole('button', { name: en.mainChat.proactive.closePanel }));
            } else {
                act(() => emitRuntimeEvent('memory:withdrawn', undefined));
            }
            await act(async () => finish({
                id: 'late-refresh-row',
                kind: 'system',
                content: 'Late refreshed content',
                toolName: MAIN_CHAT_PROACTIVE_TOOL,
                toolInput: JSON.stringify({
                    momentId: 'moment-1',
                    kind: 'brief_ready',
                    headline: 'Late refreshed headline',
                    body: 'Late refreshed body',
                    actionIds: ['open'],
                    actionLabels: { open: 'Play' },
                    target: { kind: 'today' },
                    state: 'pending',
                }),
            }));

            expect(screen.queryByTestId('main-chat-item-panel')).toBeNull();
            expect(screen.getByTestId('panel-open-id')).toBeEmptyDOMElement();
        },
    );

    it.each([
        ['decision_due', permission({ kindMuted: true }), en.mainChat.card.button.review],
        ['work_blocked', proactive({ kind: 'work_blocked', kindMuted: true, actionIds: ['review', 'stop', 'dismiss'], actionLabels: { review: 'Review', stop: 'Stop', dismiss: 'Dismiss' } }), en.mainChat.card.button.answer],
        ['commitment_slipping', proactive({ kind: 'commitment_slipping', kindMuted: true }), en.mainChat.card.button.findTime],
        ['waiting_on_reply', proactive({ kind: 'waiting_on_reply', kindMuted: true }), en.mainChat.card.button.review],
        ['reminder', proactive({ kind: 'reminder', kindMuted: true, actionIds: ['done', 'not_now'], actionLabels: { done: 'Done', not_now: 'Not now' } }), en.mainChat.card.button.done],
        ['permission_fixture', permission({ kind: 'permission_fixture', kindMuted: true, actionIds: ['open'], actionLabels: { open: 'Open' } }), en.mainChat.card.button.review],
    ])('keeps muted %s controls visible without a fold', (_kind, item, expectedLabel) => {
        render(<MainChatProactiveCard item={item} onOpenTarget={vi.fn()} />);
        expect(document.querySelector('[data-proactive-folded]')).toBeNull();
        expect(screen.getByRole('button', { name: expectedLabel })).toBeEnabled();
    });

    it('renders retained muted cards as one collapsed line and keeps their controls inside', () => {
        render(<MainChatProactiveCard item={proactive({ kind: 'meeting_prep', kindMuted: true })} onOpenTarget={vi.fn()} />);
        const details = document.querySelector('[data-proactive-folded]')!;
        expect(details).not.toHaveAttribute('open');
        expect(details.closest('section')).not.toHaveClass('bg-panel-warm', 'border');
        expect(screen.queryByRole('button', { name: en.mainChat.card.button.openPrep })).toBeNull();
        openFoldedLine();
        expect(screen.getByRole('button', { name: en.mainChat.card.button.openPrep })).toBeEnabled();
    });

    it.each(['en', 'zh-CN'])('titles the first brief\'s card with its slot and time, not the first-week title, in %s (today-review v1.1.16 D16)', async (lang) => {
        await i18n.changeLanguage(lang);
        render(<MainChatProactiveCard item={proactive({
            kind: 'first_week', headline: 'Your first week with Isoform.', briefId: '2026-11-03-request',
            briefDisplay: { slot: 'on_request', asOf: '2026-11-04T01:28:00Z', timezone: 'America/Los_Angeles' },
            actionIds: ['open'], actionLabels: { open: 'Open' },
        })} />);
        expect(screen.getByRole('heading', { level: 3 })).toHaveTextContent(lang === 'en' ? 'Evening brief · 5:28 PM' : '晚间简报 · 17:28');
        expect(screen.queryByText('Your first week with Isoform.')).not.toBeInTheDocument();
    });

    it('keeps the first-week title on the first-week card itself', async () => {
        await i18n.changeLanguage('en');
        render(<MainChatProactiveCard item={proactive({ kind: 'first_week', headline: 'Your first week with Isoform.', actionIds: ['open'], actionLabels: { open: 'Open' } })} />);
        expect(screen.getByRole('heading', { level: 3 })).toHaveTextContent('Your first week with Isoform.');
    });

    it.each(['en', 'zh-CN'])('mutes a first-week check-in through Not for me in %s', async (lang) => {
        await i18n.changeLanguage(lang);
        resolveAction.mockResolvedValue('pending');
        render(<MainChatProactiveCard item={proactive({ kind: 'first_week', actionIds: ['open', 'not_for_me'], actionLabels: { open: 'Open', not_for_me: 'Not for me' } })} />);
        await clickMenuAction(lang === 'en' ? en.mainChat.proactive.notForMe : zh.mainChat.proactive.notForMe, lang === 'en' ? en.mainChat.proactive.moreActions : zh.mainChat.proactive.moreActions);
        await waitFor(() => expect(resolveAction).toHaveBeenCalledWith('moment-1', 'not_for_me'));
    });
    beforeEach(async () => {
        await i18n.changeLanguage('en');
        draftTaskID.mockReset().mockRejectedValue(new Error('no draft task'));
        taskDetail.mockReset().mockResolvedValue({ id: 'work-1', title: 'Completed task', group: 'done', status: 'completed', updatedAt: '2026-10-30T12:00:00Z', actions: [], steps: [], events: [
            { id: 'result', kind: 'isoform', text: 'Full privacy-projected result: all implementation checks completed.', at: '2026-10-30T12:00:00Z' },
            { id: 'file-1', kind: 'file', file: { name: 'Implementation report' }, at: '2026-10-30T12:00:00Z' },
            { id: 'file-2', kind: 'file', file: { name: 'Test results' }, at: '2026-10-30T12:00:00Z' },
        ] });
        taskAction.mockReset().mockResolvedValue({});
        resolveAction.mockReset().mockResolvedValue('opened');
        recordReceipt.mockReset().mockResolvedValue(undefined);
        settingsMocks.get.mockReset().mockResolvedValue({ available: false, kinds: [] });
        settingsMocks.setKind.mockReset();
        settingsMocks.week.mockReset();
        publishEngagementSettings({ kinds: [] });
        copyDraft.mockReset().mockResolvedValue(true);
        nativeCopyDraft.mockReset().mockResolvedValue(undefined);
        correctFact.mockReset().mockResolvedValue('acted');
    });

    // The brief card (design, In Chat): the brief's title and opening sentence,
    // Play only with a recording and its words, and Read all opens the same
    // brief view Today shows.
    it('renders the brief card: title, opening sentence, Play and Read it opens the brief in the card', async () => {
        briefByID.mockResolvedValue({ brief: readyBrief('brief-1'), building: false, enabled: true, nextAt: '', nextSlot: '', empty: false, missing: [] });
        const onOpen = vi.fn();
        render(<MainChatProactiveCard item={proactive({ briefId: 'brief-1' })} onOpenTarget={onOpen} />);
        expect(document.querySelector('[data-proactive-moment]')).not.toHaveClass('animate-rise-in');
        expect(await screen.findByRole('heading', { name: 'Evening brief · 1:50 PM' })).toBeInTheDocument();
        expect(screen.getByText('Good morning. Three meetings are scheduled.')).toBeInTheDocument();
        expect(screen.getByRole('button', { name: en.mainChat.card.button.hearIt })).toHaveClass('bg-solid-btn-bg');
        fireEvent.click(screen.getByRole('button', { name: en.mainChat.card.button.readIt }));
        expect(onOpen).not.toHaveBeenCalled();
        expect(screen.queryByRole('button', { name: en.mainChat.card.button.readIt })).toBeNull();
        expect(screen.getByText('From your brief')).toBeInTheDocument();
        expect(screen.queryByRole('button', { name: 'From your brief' })).toBeNull();
        expect(screen.queryByRole('button', { name: 'Dismiss' })).toBeNull();
        expect(resolveAction).not.toHaveBeenCalled();
    });

    it('does not lend the linked brief time to a different evidence record', async () => {
        briefByID.mockResolvedValue({ brief: readyBrief('brief-1') });
        render(<MainChatProactiveCard item={proactive({ briefId: 'brief-1', evidence: [{ kind: 'brief', id: 'brief-2' }] })} />);
        await screen.findByRole('heading', { name: 'Evening brief · 1:50 PM' });
        expect(screen.getByText('From your brief')).toBeInTheDocument();
        expect(screen.queryByText('From your brief · 1:50 PM')).toBeNull();
    });

    it.each([
        ['en', 'Stopped watching your screen at 12:21 PM: Screen Recording was turned off.', 'Turn it back on'],
        ['zh-CN', '停止查看你的屏幕：屏幕录制权限被关闭了。', '重新打开'],
    ])('states an unchosen stop with its cause and Turn it back on in %s (chat-review v1.0.10 H2)', async (language, text, action) => {
        await i18n.changeLanguage(language);
        const open = vi.fn();
        const start = new Date(2026, 10, 2, 12, 21).getTime();
        render(<MainChatViewContext.Provider value={{ today: '2026-11-02', hideRoutine: false, topicNames: new Map(), chips: {} }}><MainChatProactiveCard item={proactive({ kind: 'capture_paused', captureSource: 'screen', captureCause: 'permission', captureStartAt: start, target: { kind: 'settings', id: 'activity' } })} onOpenTarget={open} /></MainChatViewContext.Provider>);
        const line = document.querySelector('[data-main-chat-proactive="capture_paused"]') as HTMLElement;
        expect(line).toHaveTextContent(text);
        expect(line).not.toHaveTextContent(/From|来自/);
        expect(screen.queryByRole('button', { name: /Resume|恢复/ })).toBeNull();
        fireEvent.click(screen.getByRole('button', { name: action }));
        expect(open).toHaveBeenCalledWith({ kind: 'settings', id: 'activity' });
        await i18n.changeLanguage('en');
    });

    it('names the listening stop the capture loop made on its own', () => {
        const start = new Date(2026, 9, 30, 14, 10).getTime();
        render(<MainChatViewContext.Provider value={{ today: '2026-11-02', hideRoutine: false, topicNames: new Map(), chips: {} }}><MainChatProactiveCard item={proactive({ kind: 'capture_paused', captureSource: 'microphone', captureCause: 'stopped', captureStartAt: start, target: { kind: 'settings', id: 'activity' } })} onOpenTarget={vi.fn()} /></MainChatViewContext.Provider>);
        expect(document.querySelector('[data-main-chat-proactive="capture_paused"]')).toHaveTextContent('Stopped listening at 2:10 PM: listening stopped on its own.');
        expect(screen.getByRole('button', { name: 'Turn it back on' })).toBeInTheDocument();
    });

    it('uses the recorded card time while brief metadata is unavailable', () => {
        briefByID.mockReturnValue(new Promise(() => {}));
        const item = proactive({ briefId: '2026-10-30-evening', evidence: [{ kind: 'brief', id: '2026-10-30-evening' }] });
        item.createdAt = new Date(2026, 9, 30, 18, 0).getTime();
        render(<MainChatProactiveCard item={item} onOpenTarget={vi.fn()} />);
        expect(screen.getByRole('heading', { name: 'Evening brief · 6:00 PM' })).toBeInTheDocument();
    });

    it('keeps a source-less morning brief readable at its recorded time after a failed read, and Try again reads it (design v1.0.1)', async () => {
        briefByID.mockReset();
        briefByID.mockRejectedValueOnce(new Error('unavailable'));
        briefByID.mockResolvedValue({ brief: { ...(readyBrief('2026-11-02-morning') as object), evidence: [] }, building: false, enabled: true, nextAt: '', nextSlot: '', empty: false, missing: [] });
        const item = proactive({ briefId: '2026-11-02-morning', evidence: [] });
        item.createdAt = new Date(2026, 10, 2, 9, 23).getTime();
        render(<MainChatProactiveCard item={item} onOpenTarget={vi.fn()} />);
        expect(screen.getByRole('heading', { name: 'Morning brief · 9:23 AM' })).toBeInTheDocument();
        const alert = await screen.findByRole('alert');
        expect(alert).toHaveTextContent("Couldn't read this brief. Try again");
        expect(screen.getByText('From your brief')).toBeInTheDocument();
        expect(screen.queryByRole('button', { name: 'Read it' })).toBeNull();
        fireEvent.click(screen.getByRole('button', { name: 'Try again' }));
        expect(await screen.findByRole('button', { name: 'Read it' })).toBeInTheDocument();
        expect(briefByID).toHaveBeenCalledTimes(2);
    });
    it('uses the recorded source recovery and has no stale Resume action', () => {
        const open = vi.fn();
        const end = new Date(2026, 10, 1, 22, 5).getTime();
        render(<MainChatViewContext.Provider value={{ today: '2026-11-02', hideRoutine: false, topicNames: new Map(), chips: {} }}><MainChatProactiveCard item={proactive({ kind: 'capture_paused', captureSource: 'screen', captureCause: 'permission', captureStartAt: end - 35 * 60_000, captureEndAt: end, target: { kind: 'settings', id: 'activity' } })} onOpenTarget={open} /></MainChatViewContext.Provider>);
        expect(document.querySelector('[data-main-chat-proactive="capture_paused"]')).toHaveTextContent('Stopped watching your screen from 9:30 PM to 10:05 PM: Screen Recording was turned off.');
        expect(screen.queryByRole('button', { name: 'Resume' })).toBeNull();
        expect(screen.queryByRole('button', { name: 'Turn it back on' })).toBeNull();
    });

    // The shown recap is the catalog doorbell (headline only, body the same):
    // expanding it in place revealed nothing, so Read your week opens the
    // brief that holds the week on Today (polish sweep 2026-10-27).
    const weekDoorbell = () => proactive({
        kind: 'weekly_recap', headline: 'Your week is ready.', body: 'Your week is ready.',
        actionIds: ['open', 'mute_kind'], actionLabels: { open: 'Read your week', mute_kind: 'Mute this kind' },
        target: { kind: 'today', id: '2026-10-23-evening' }, evidence: [],
    });

    // Polish sweep 2026-10-27: folded rows showed the browser's raw "▶".
    it('folds a muted card behind Chat\'s chevron, not the browser triangle', () => {
        render(<MainChatProactiveCard item={proactive({ kind: 'meeting_prep', kindMuted: true, actionIds: ['open', 'mute_kind'] })} onOpenTarget={vi.fn()} />);
        const summary = document.querySelector('[data-proactive-folded] > summary');
        expect(summary).not.toBeNull();
        expect(summary!.className).toContain('list-none');
        expect(summary!.className).toContain('[&::-webkit-details-marker]:hidden');
        const chevron = summary!.querySelector('svg');
        expect(chevron).not.toBeNull();
        expect(chevron!.getAttribute('class')).not.toContain('rotate-90');
        openFoldedLine();
        expect(summary!.querySelector('svg')!.getAttribute('class')).toContain('rotate-90');
    });

    // The week's card is a brief card too: Play only with the brief's words.
    it('offers Play and Read it on the weekly recap card only when its brief has words or recap rows (design v1.0.1)', async () => {
        briefByID.mockReset();
        const week = { ...(readyBrief('week-1') as object), slot: 'evening', yourWeek: { start: '2026-10-26', end: '2026-11-01', lead: 'Your week' } };
        const noRows = { ...(readyBrief('week-1') as object), narrativeStatus: 'unavailable', narrative: [], audio: undefined, yourWeek: { start: '2026-10-26', end: '2026-11-01', lead: 'Your week' } };
        const withRows = { ...noRows, yourWeek: { ...noRows.yourWeek, time: [{ name: 'Brief work', hours: 2 }] } };
        briefByID.mockResolvedValueOnce({ brief: noRows, building: false, enabled: true, nextAt: '', nextSlot: '', empty: false, missing: [] });
        briefByID.mockResolvedValueOnce({ brief: withRows, building: false, enabled: true, nextAt: '', nextSlot: '', empty: false, missing: [] });
        briefByID.mockResolvedValue({ brief: week, building: false, enabled: true, nextAt: '', nextSlot: '', empty: false, missing: [] });
        const onOpen = vi.fn();
        const { unmount } = render(<MainChatProactiveCard item={proactive({ kind: 'weekly_recap', briefId: 'week-1' })} onOpenTarget={onOpen} />);
        expect(await screen.findByRole('heading', { name: 'Evening brief · 1:50 PM' })).toBeInTheDocument();
        expect(screen.queryByRole('button', { name: en.mainChat.card.button.hearIt })).toBeNull();
        expect(screen.queryByRole('button', { name: en.mainChat.card.button.readIt })).toBeNull();
        unmount();
        render(<MainChatProactiveCard item={proactive({ kind: 'weekly_recap', briefId: 'week-1' })} onOpenTarget={onOpen} />);
        expect(await screen.findByRole('button', { name: en.mainChat.card.button.readIt })).toBeInTheDocument();
        expect(screen.queryByRole('button', { name: en.mainChat.card.button.hearIt })).toBeNull();
        fireEvent.click(screen.getByRole('button', { name: en.mainChat.card.button.readIt }));
        expect(onOpen).toHaveBeenCalledWith({ kind: 'today', id: 'week-1' });
        act(() => emitRuntimeEvent('brief:stored', {}));
        expect(await screen.findByRole('button', { name: en.mainChat.card.button.hearIt })).toBeInTheDocument();
    });
    it('Read it on a recap appears only when the Your week view would draw a row (design PR #664)', async () => {
        const base = { ...(readyBrief('week-1') as object), narrativeStatus: 'unavailable', narrative: [], audio: undefined };
        const cases: [string, Record<string, unknown>, boolean][] = [
            ['zero-hour time bars', { time: [{ name: 'Brief work', hours: 0 }] }, false],
            ['a done row with no file or task', { done: [{ id: 'd1', kind: 'deliverable', title: 'Doc' }] }, false],
            ['a done row with a task Today can open', { done: [{ id: 'd2', kind: 'outcome', taskId: 't1', title: 'Task' }] }, true],
            ['an open row', { open: [{ id: 'o1', title: 'Follow up' }] }, true],
        ];
        for (const [, rows, shows] of cases) {
            briefByID.mockReset();
            briefByID.mockResolvedValue({ brief: { ...base, yourWeek: { start: '2026-10-26', end: '2026-11-01', lead: 'Your week', ...rows } }, building: false, enabled: true, nextAt: '', nextSlot: '', empty: false, missing: [] });
            const { unmount } = render(<MainChatProactiveCard item={proactive({ kind: 'weekly_recap', briefId: 'week-1' })} onOpenTarget={vi.fn()} />);
            await waitFor(() => expect(briefByID).toHaveBeenCalled());
            await act(async () => {});
            expect(screen.queryByRole('button', { name: en.mainChat.card.button.readIt }) !== null).toBe(shows);
            unmount();
        }
    });

    it('opens the source brief from the weekly recap card in Chat', () => {
        const onOpen = vi.fn();
        render(<MainChatProactiveCard item={weekDoorbell()} onOpenTarget={onOpen} />);
        fireEvent.click(screen.getByRole('button', { name: en.mainChat.card.button.readWeek }));
        expect(onOpen).toHaveBeenCalledWith({ kind: 'today', id: '2026-10-23-evening', runId: undefined });
        expect(resolveAction).not.toHaveBeenCalled();
    });

    it('opens the source brief from the weekly recap row in Today, not an empty Review panel', () => {
        const onOpen = vi.fn();
        render(<MainChatProactiveCard item={weekDoorbell()} compact panelSurface="today" onOpenTarget={onOpen} />);
        expect(screen.queryByRole('button', { name: en.mainChat.action.review })).toBeNull();
        fireEvent.click(screen.getByRole('button', { name: en.mainChat.card.button.readWeek }));
        expect(onOpen).toHaveBeenCalledWith({ kind: 'today', id: '2026-10-23-evening', runId: undefined });
        expect(resolveAction).not.toHaveBeenCalled();
    });

    it('switches stored card, digest children, actions and expanded status copy with the UI language', async () => {
        const localized = {
            en: { headline: 'Your brief is ready.', body: 'Your morning brief is ready.', actionLabels: { open: 'Read it' } },
            'zh-CN': { headline: '你的简报已经准备好了。', body: '你的早间简报已经准备好了。', actionLabels: { open: '阅读' } },
        };
        const child = proactive({ localized, actionIds: ['open'] }).toolInput;
        render(<MainChatProactiveCard item={proactive({
            kind: 'digest', actionIds: [], items: [child],
            localized: {
                en: { headline: 'One thing to catch up on.', body: '', actionLabels: {} },
                'zh-CN': { headline: '有一件事可以看看。', body: '', actionLabels: {} },
            },
        })} onOpenTarget={vi.fn()} />);
        render(<MainChatStatusRow item={{ ...status(), toolInput: { period: 'morning', entries: [
            { id: 'digest', kind: 'digest_done', text: 'Your updates are ready.', localized: { en: 'Your updates are ready.', 'zh-CN': '你的最新动态已准备好。' }, target: { kind: 'today' } },
        ] } }} />);
        fireEvent.click(screen.getByRole('button', { name: /1 background update/ }));
        expect(screen.queryByText('Your morning brief is ready.')).toBeNull();
        expect(screen.getByRole('heading', { name: 'Brief' })).toBeInTheDocument();
        await act(() => i18n.changeLanguage('zh-CN'));
        expect(screen.getByRole('heading', { name: '有一件事可以看看。' })).toBeInTheDocument();
        expect(screen.queryByText('你的早间简报已经准备好了。')).toBeNull();
        expect(screen.getByRole('heading', { name: '简报' })).toBeInTheDocument();
        expect(screen.getByRole('button', { name: zh.mainChat.card.button.readIt })).toBeInTheDocument();
        expect(screen.getByText('你的最新动态已准备好。')).toBeInTheDocument();
        expect(screen.queryByText('Your morning brief is ready.')).not.toBeInTheDocument();
        await act(() => i18n.changeLanguage('en'));
        expect(screen.getByRole('button', { name: en.mainChat.card.button.readIt })).toBeInTheDocument();
    });

    it.each(['en', 'zh-CN'])('renders a draft as plain text and copies only its text in %s', async (language) => {
        await i18n.changeLanguage(language);
        const locale = language === 'zh-CN' ? zh : en;
        const draftContent = { to: 'dana@example.com', subject: 'Re: Planning', text: '<b>Dana</b>, thank you for your patience.' };
        const onOpen = vi.fn();
        render(<MainChatProactiveCard item={proactive({ kind: 'waiting_on_reply', draft: true, draftContent, actionIds: ['copy_draft', 'open', 'dismiss'], actionLabels: { copy_draft: 'Copy draft', open: 'Open Mail', dismiss: 'Dismiss' }, target: { kind: 'mail' } })} onOpenTarget={onOpen} />);
        expect(screen.getByText(draftContent.to)).toBeInTheDocument();
        expect(screen.getByText(draftContent.subject)).toBeInTheDocument();
        expect(screen.getByText(draftContent.text)).toBeInTheDocument();
        expect(document.querySelector('[data-proactive-draft] b')).toBeNull();
        expect(screen.getByText(locale.mainChat.proactive.draftTo)).toBeInTheDocument();
        await openCardPanel(locale.mainChat.card.button.review);
        expect(screen.queryByRole('button', { name: locale.mainChat.proactive.copyDraft })).toBeNull();
        await clickMenuAction(locale.mainChat.proactive.copyDraft, locale.mainChat.proactive.moreActions);
        expect(await screen.findByRole('status')).toHaveTextContent(locale.mainChat.proactive.draftCopied);
        expect(copyDraft).toHaveBeenCalledWith(draftContent.text);
        expect(resolveAction).not.toHaveBeenCalled();
        expect(onOpen).not.toHaveBeenCalled();
        expect(screen.getByTestId('main-chat-item-panel')).toHaveAttribute('data-panel-open', 'moment-1');
    });

    it.each(['en', 'zh-CN'])('shows factual communication provenance and Change opens the first belief in %s', async (language) => {
        await i18n.changeLanguage(language);
        const onOpen = vi.fn();
        const draftContent = {
            to: 'jia@example.com', subject: '', text: '谢谢，周五前确认。',
            communication: { beliefIds: ['belief-language', 'belief-length'], recipientName: '加一', language: 'Chinese', sentences: 2, greeting: false },
        };
        render(<MainChatProactiveCard item={proactive({ kind: 'waiting_on_reply', draft: true, draftContent, actionIds: [] })} onOpenTarget={onOpen} />);
        const expected = language === 'en'
            ? 'Written the way you write to 加一: Chinese, 2 sentences, no greeting.'
            : '按你写给加一的方式撰写：中文，2 句话，无问候语。';
        expect(screen.getByText(expected)).toBeInTheDocument();
        fireEvent.click(screen.getByRole('button', { name: language === 'en' ? 'Change' : '更改' }));
        expect(onOpen).toHaveBeenCalledWith({ kind: 'memory', id: 'belief-language' });
        expect(resolveAction).not.toHaveBeenCalled();
    });

    it.each([
        undefined,
        { beliefIds: [], recipientName: '加一', language: 'Chinese', sentences: 2, greeting: false },
        { beliefIds: ['belief-1'], recipientName: '', language: 'Chinese', sentences: 2, greeting: false },
        { beliefIds: ['belief-1'], recipientName: '加一', language: 'Chinese', sentences: 0, greeting: false },
        { beliefIds: ['belief-1'], recipientName: '加一', language: 'Chinese', sentences: 2, greeting: 'no' },
    ])('keeps an old or malformed draft without communication provenance', (communication) => {
        const draftContent = { to: 'jia@example.com', subject: '', text: '谢谢。', communication };
        render(<MainChatProactiveCard item={proactive({ kind: 'waiting_on_reply', draft: true, draftContent, actionIds: [] })} onOpenTarget={vi.fn()} />);
        expect(screen.getByText('谢谢。')).toBeInTheDocument();
        expect(document.querySelector('[data-communication-provenance]')).toBeNull();
        expect(screen.queryByRole('button', { name: 'Change' })).toBeNull();
    });

    it.each(['en', 'zh-CN'])('keeps draft copying local after muting its reminder kind in %s', async (language) => {
        await i18n.changeLanguage(language);
        const locale = language === 'zh-CN' ? zh : en;
        const draftContent = { to: 'dana@example.com', subject: 'Re: Planning', text: 'Dana, thank you for waiting.' };
        resolveAction.mockResolvedValue('pending');
        render(<MainChatProactiveCard item={proactive({
            kind: 'waiting_on_reply', draft: true, draftContent,
            actionIds: ['copy_draft', 'open', 'mute_kind'],
            actionLabels: { copy_draft: 'Stored copy label', open: 'Stored open label', dismiss: 'Dismiss', mute_kind: 'Stored mute label' },
        })} />);
        await waitFor(() => expect(recordReceipt).toHaveBeenCalledWith('moment-1', 'delivered'));
        holdTimers();
        await clickMenuAction(locale.mainChat.proactive.muteKind, locale.mainChat.proactive.moreActions);
        await passCardHold();
        await openMutedLine();
        expect(await screen.findByRole('status')).toHaveTextContent(locale.mainChat.proactive.held.muted);
        expect(resolveAction).toHaveBeenCalledWith('moment-1', 'mute_kind');
        expect(screen.queryByRole('button', { name: locale.mainChat.proactive.muteKind })).toBeNull();
        resolveAction.mockClear();

        await openCardPanel(locale.mainChat.card.button.review);
        await clickMenuAction(locale.mainChat.proactive.copyDraft, locale.mainChat.proactive.moreActions);
        await waitFor(() => expect(screen.getAllByRole('status').map((node) => node.textContent)).toEqual([
            locale.mainChat.proactive.held.muted, locale.mainChat.proactive.draftCopied,
        ]));
        expect(copyDraft).toHaveBeenCalledWith(draftContent.text);
        expect(resolveAction).not.toHaveBeenCalled();
        expect(screen.queryByRole('button', { name: 'Dismiss' })).toBeNull();
        expect(screen.getByRole('button', { name: locale.mainChat.proactive.moreActions })).toBeEnabled();
    });

    it.each([true, false])('falls back after the native clipboard is unavailable, with runtime success %s', async (runtimeSuccess) => {
        const originalRuntime = (window as unknown as { runtime?: unknown }).runtime;
        (window as unknown as { runtime?: unknown }).runtime = { ClipboardSetText: vi.fn() };
        nativeCopyDraft.mockRejectedValue(new Error('private clipboard detail'));
        copyDraft.mockResolvedValue(runtimeSuccess);
        try {
            const draftContent = { to: 'dana@example.com', subject: 'Re: Planning', text: 'Dana, thank you for waiting.' };
            render(<MainChatProactiveCard item={proactive({ kind: 'waiting_on_reply', draft: true, draftContent, actionIds: ['copy_draft', 'open'], actionLabels: { copy_draft: 'Copy draft', open: 'Open Mail' } })} />);
            await openCardPanel(en.mainChat.card.button.review);
            await clickMenuAction(en.mainChat.proactive.copyDraft);
            if (runtimeSuccess) {
                expect(await screen.findByRole('status')).toHaveTextContent(en.mainChat.proactive.draftCopied);
                expect(screen.queryByRole('alert')).toBeNull();
            } else {
                expect(await screen.findByRole('alert')).toHaveTextContent(en.mainChat.proactive.failed);
                expect(screen.queryByRole('status')).toBeNull();
            }
            expect(nativeCopyDraft).toHaveBeenCalledWith(draftContent.text);
            expect(copyDraft).toHaveBeenCalledWith(draftContent.text);
            expect(resolveAction).not.toHaveBeenCalled();
            expect(screen.queryByText('private clipboard detail')).toBeNull();
            expect(screen.getByRole('button', { name: en.mainChat.proactive.moreActions })).toBeEnabled();
        } finally {
            (window as unknown as { runtime?: unknown }).runtime = originalRuntime;
        }
    });

    it.each([undefined, { to: 'dana@example.com', subject: 'Planning', text: '' }, { to: 2, subject: 'Planning', text: 'Thanks.' }])('hides draft actions without a complete draft', (draftContent) => {
        render(<MainChatProactiveCard item={proactive({ kind: 'waiting_on_reply', draft: true, draftContent, actionIds: ['copy_draft', 'send'], actionLabels: { copy_draft: 'Copy draft', send: 'Send' } })} />);
        expect(screen.queryByRole('button', { name: en.mainChat.proactive.copyDraft })).toBeNull();
        expect(screen.queryByRole('button', { name: 'Send' })).toBeNull();
        expect(document.querySelector('[data-proactive-draft]')).toBeNull();
    });

    it.each(['en', 'zh-CN'])('shows a complete sendable draft with one localized primary Send action in %s', async (language) => {
        await i18n.changeLanguage(language);
        const locale = language === 'en' ? en : zh;
        render(<MainChatProactiveCard item={sendDraft()} />);
        await openCardPanel(locale.mainChat.card.button.review);
        const panel = within(screen.getByTestId('main-chat-item-panel'));
        const send = panel.getByRole('button', {
            name: locale.mainChat.proactive.sendTo
                .replace('{{recipient}}', 'Dana')
                .replace('{{channel}}', locale.mainChat.channel.mail),
        });
        expect(send).toHaveClass('bg-solid-btn-bg');
        expect(panel.queryByRole('button', { name: 'Dismiss' })).toBeNull();
        expect(panel.queryByRole('button', { name: 'Stored send label' })).toBeNull();
        expect(panel.getByText('Dana, Thursday works for me.')).toBeInTheDocument();
    });

    it('allows a Slack draft without a subject', async () => {
        render(<MainChatProactiveCard item={sendDraft({ channel: 'slack', subject: '' })} />);
        await openCardPanel(en.mainChat.card.button.review);
        expect(screen.getByRole('button', { name: 'Send to Dana · Slack' })).toBeEnabled();
    });

    it.each([
        ['legacy', { channel: undefined, recipient: undefined, principalId: undefined, revision: undefined }],
        ['unsupported channel', { channel: 'teams' }],
        ['missing recipient', { recipient: ' ' }],
        ['missing principal', { principalId: '' }],
        ['missing revision', { revision: undefined }],
        ['empty Mail subject', { subject: ' ' }],
    ])('hides Send for an incomplete or %s draft', async (_case, metadata) => {
        render(<MainChatProactiveCard item={sendDraft(metadata)} />);
        await openCardPanel(en.mainChat.card.button.review);
        const panel = within(screen.getByTestId('main-chat-item-panel'));
        expect(panel.queryByText(/Send to|发送给/)).toBeNull();
        expect(panel.getByText('Dana, Thursday works for me.')).toBeInTheDocument();
        expect(panel.queryByRole('button', { name: 'Dismiss' })).toBeNull();
    });

    it('opens the shared panel from a compact draft row without sending', async () => {
        const item = sendDraft();
        render(<TodayCardPanelHarness item={item} />);
        const review = screen.getByRole('button', { name: en.mainChat.action.review });
        expect(review).toBeEnabled();
        expect(screen.queryByText(/Send to/)).toBeNull();
        fireEvent.click(review);
        const panel = await screen.findByTestId('main-chat-item-panel');
        expect(panel).toHaveAttribute('data-panel-open', 'moment-1');
        expect(within(panel).getByRole('button', { name: 'Send to Dana · Mail' })).toBeEnabled();
        expect(resolveAction).not.toHaveBeenCalled();
    });

    // launch-film v2.4.0: Not now is the quiet button in every state of the
    // waiting card and its draft panel; only Review and Send are filled, and a
    // mouse click leaves no focus ring.
    it('keeps Not now quiet on the waiting card and its draft panel; only Review and Send are filled', async () => {
        const item = sendDraft({ recipient: 'Maya' }, {
            headline: 'Maya is waiting on your notes.',
            actionIds: ['send', 'not_now', 'copy_draft', 'open'],
            actionLabels: { send: 'Stored send label', not_now: 'Not now', copy_draft: 'Copy draft', open: 'Stored open label' },
        });
        const filled = (button: HTMLElement) => button.className.includes('bg-solid-btn-bg');
        const noMouseFocusRing = (button: HTMLElement) => expect(button.className.split(/\s+/).filter((name) => /^focus:/.test(name))).toEqual([]);
        render(<TodayCardPanelHarness item={item} />);
        const review = screen.getByRole('button', { name: en.mainChat.action.review });
        const cardNotNow = () => screen.getAllByRole('button', { name: 'Not now' }).find((button) => !button.closest('[data-testid="main-chat-item-panel"]'))!;
        expect(filled(review)).toBe(true);
        expect(filled(cardNotNow())).toBe(false);
        fireEvent.mouseDown(review);
        fireEvent.click(review);
        const panel = await screen.findByTestId('main-chat-item-panel');
        await waitFor(() => expect(panel).toHaveAttribute('data-panel-open', 'moment-1'));
        const buttons = [...screen.getAllByRole('button', { name: 'Not now' }), ...screen.getAllByRole('button', { name: 'Send to Maya · Mail' })];
        for (const button of buttons) noMouseFocusRing(button);
        for (const notNow of screen.getAllByRole('button', { name: 'Not now' })) {
            expect(filled(notNow)).toBe(false);
            expect(notNow.className).toContain('bg-transparent');
        }
        expect(within(panel).getAllByRole('button', { name: 'Send to Maya · Mail' }).every(filled)).toBe(true);
        expect(cardNotNow()).toBeInTheDocument();
    });

    // chat-review v1.0.8: after Review the draft and its Send are in the panel
    // only; the card keeps its preview with Review and Not now and the open outline.
    it('keeps one Send on screen, in the panel, after Review while the card keeps its preview, Review and Not now', async () => {
        const item = sendDraft({ recipient: 'Maya' }, {
            headline: 'Maya is waiting on your notes.',
            actionIds: ['send', 'not_now', 'copy_draft', 'open'],
            actionLabels: { send: 'Stored send label', not_now: 'Not now', copy_draft: 'Copy draft', open: 'Stored open label' },
        });
        render(<TodayCardPanelHarness item={item} />);
        const card = () => [...document.querySelectorAll<HTMLElement>('article[data-main-chat-proactive="waiting_on_reply"]')].find((el) => !el.closest('[data-testid="main-chat-item-panel"]'))!;
        const send = () => screen.queryAllByRole('button', { name: 'Send to Maya · Mail' });
        expect(card().querySelector('[data-proactive-draft]')).not.toBeNull();
        expect(send()).toHaveLength(0);
        expect(card()).not.toHaveAttribute('data-panel-open');
        fireEvent.click(within(card()).getByRole('button', { name: en.mainChat.action.review }));
        const panel = await screen.findByTestId('main-chat-item-panel');
        await waitFor(() => expect(panel).toHaveAttribute('data-panel-open', 'moment-1'));
        expect(send()).toHaveLength(1);
        expect(panel).toContainElement(send()[0]);
        expect(card()).toHaveAttribute('data-panel-open', 'true');
        expect(card().className).toContain('ring-2');
        expect(card().querySelector('[data-proactive-draft]')).not.toBeNull();
        expect(within(card()).queryByRole('textbox')).toBeNull();
        expect(within(card()).getAllByRole('button').map((button) => button.textContent)).toEqual([en.mainChat.action.review, 'Not now']);
    });

    // Thursday polish C5: the panel's header is the card's title, so the card
    // under it starts at its lines.
    it('says the waiting card title once in its draft panel, in the header', async () => {
        const item = sendDraft({ recipient: 'Maya' }, {
            headline: 'Maya is waiting on your notes.',
            actionIds: ['send', 'not_now', 'copy_draft', 'open'],
            actionLabels: { send: 'Stored send label', not_now: 'Not now', copy_draft: 'Copy draft', open: 'Stored open label' },
        });
        render(<TodayCardPanelHarness item={item} />);
        fireEvent.click(screen.getByRole('button', { name: en.mainChat.action.review }));
        const panel = await screen.findByTestId('main-chat-item-panel');
        await waitFor(() => expect(panel).toHaveAttribute('data-panel-open', 'moment-1'));
        expect(within(panel).getAllByText('Maya is waiting on your notes.')).toHaveLength(1);
        expect(within(panel).getByRole('heading', { name: 'Maya is waiting on your notes.' }).tagName).toBe('H2');
        expect(within(panel).getByRole('button', { name: 'Send to Maya · Mail' })).toBeInTheDocument();
    });

    it('opens the draft task thread in the 420px panel from Review', async () => {
        draftTaskID.mockResolvedValue('draft.one');
        taskDetail.mockResolvedValue({ id: 'draft.one', title: 'Reply to Dana', group: 'needs_you', status: 'draft_ready', updatedAt: '2026-10-30T12:00:00Z', actions: ['send'], steps: [], events: [], draft: { channel: 'mail', to: 'Dana', body: 'A reply', state: 'ready', recipients: { to: ['Dana'] } } });
        const onOpen = vi.fn();
        render(<MainChatProactiveCard item={sendDraft()} onOpenTarget={onOpen} />);
        await openCardPanel(en.mainChat.card.button.review);
        const panel = screen.getByTestId('main-chat-item-panel');
        expect(await within(panel).findByText('A reply')).toBeInTheDocument();
        expect(panel).toHaveStyle({ width: '420px' });
        expect(draftTaskID).toHaveBeenCalledWith('moment-1');
        expect(onOpen).not.toHaveBeenCalled();
        expect(resolveAction).not.toHaveBeenCalled();
    });

    it('opens the shared item panel from a Today card with the Chat card\'s own button', () => {
        const item = proactive({
            momentId: 'meeting-1',
            kind: 'meeting_prep',
            headline: 'Design review starts at 2:05 PM.',
            body: '',
            actionIds: ['open'],
            actionLabels: { open: 'Open' },
            target: { kind: 'calendar' },
            event: { title: 'Design review', start: '2099-10-26T14:05:00Z' },
        });
        render(<TodayCardPanelHarness item={item} />);
        // Today's panel shows Chat's card: the same Open prep, no Review or More.
        expect(screen.queryByRole('button', { name: en.mainChat.action.review })).toBeNull();
        expect(screen.queryByRole('button', { name: enToday.mainChatPanel.showCard })).toBeNull();
        fireEvent.click(screen.getByRole('button', { name: en.mainChat.card.button.openPrep }));
        expect(screen.getByTestId('main-chat-item-panel')).toHaveAttribute('data-panel-open', 'meeting-1');
        expect(recordReceipt).toHaveBeenCalledWith('meeting-1', 'opened');
        expect(resolveAction).not.toHaveBeenCalled();
        fireEvent.click(screen.getByRole('button', { name: 'Reset panel' }));
    });

    it.each(['work_finished', 'playbook_result'] as const)(
        'shows the full %s result and every deliverable from the Chat card',
        async kind => {
            resolveAction.mockResolvedValue('pending');
            render(<CardPanelHarness item={completedWork(kind)} />);
            await openCardPanel(en.mainChat.card.button.review);
            const panel = within(screen.getByTestId('main-chat-item-panel'));
            await waitFor(() => expect(within(screen.getByTestId('main-chat-item-panel')).getByText('Full privacy-projected result: all implementation checks completed.')).toBeInTheDocument());
            expect(await panel.findByText('Implementation report')).toBeInTheDocument();
            expect(await panel.findByText('Test results')).toBeInTheDocument();
            expect(resolveAction).toHaveBeenCalledWith('moment-1', 'review');
        },
    );

    it.each(['work_finished', 'playbook_result'] as const)(
        'shows the full %s result and every deliverable from the Today row',
        async kind => {
            resolveAction.mockResolvedValue('pending');
            render(<TodayCardPanelHarness item={completedWork(kind)} />);
            fireEvent.click(screen.getByRole('button', { name: en.mainChat.action.review }));
            await waitFor(() => expect(within(screen.getByTestId('main-chat-item-panel')).getByText('Full privacy-projected result: all implementation checks completed.')).toBeInTheDocument());
            const panel = screen.getByTestId('main-chat-item-panel');
            expect(await within(panel).findByText('Implementation report')).toBeInTheDocument();
            expect(await within(panel).findByText('Test results')).toBeInTheDocument();
            expect(resolveAction).toHaveBeenCalledWith('moment-1', 'review');
        },
    );

    const sendLabel = (locale: typeof en) => locale.mainChat.proactive.sendTo
        .replace('{{recipient}}', 'Dana')
        .replace('{{channel}}', locale.mainChat.channel.mail);
    const fill = (template: string, values: Record<string, string>) =>
        Object.entries(values).reduce((text, [key, value]) => text.replace(`{{${key}}}`, value), template);
    const SENT_AT = new Date('2026-10-30T15:42:00');
    const clock = (language: string) => computerClock(SENT_AT, language);

    /** Opens the draft in the panel, then switches to fake timers for the 5 seconds. */
    async function openDraftPanel(locale: typeof en, item = sendDraft()) {
        render(<MainChatProactiveCard item={item} />);
        await openCardPanel(locale.mainChat.card.button.review);
        vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'Date'] });
        vi.setSystemTime(SENT_AT);
        return within(screen.getByTestId('main-chat-item-panel'));
    }

    async function passHold(ms = MAIN_CHAT_DRAFT_SEND_HOLD_MS) {
        await act(async () => { vi.advanceTimersByTime(ms); });
        await act(async () => { await Promise.resolve(); });
    }

    // K2 box 1: Send shows "Sent to {name} · Undo" for 5 seconds, and Undo
    // stops it before Mail gets it.
    it.each(['en', 'zh-CN'])('holds Send for 5 seconds with Undo, and Undo stops it before Mail gets it in %s', async (language) => {
        await i18n.changeLanguage(language);
        const locale = language === 'en' ? en : zh;
        sendDraftAction.mockResolvedValue('acted');
        const panel = await openDraftPanel(locale);
        fireEvent.click(panel.getByRole('button', { name: sendLabel(locale) }));
        expect(panel.getByRole('status')).toHaveTextContent(fill(locale.mainChat.proactive.draftSentHeld, { name: 'Dana' }));
        expect(panel.getByRole('button', { name: locale.mainChat.proactive.undoSend })).toBeInTheDocument();
        expect(panel.queryByRole('button', { name: sendLabel(locale) })).toBeNull();
        await passHold(MAIN_CHAT_DRAFT_SEND_HOLD_MS - 1);
        expect(sendDraftAction).not.toHaveBeenCalled();
        fireEvent.click(panel.getByRole('button', { name: locale.mainChat.proactive.undoSend }));
        await passHold(MAIN_CHAT_DRAFT_SEND_HOLD_MS * 2);
        expect(sendDraftAction).not.toHaveBeenCalled();
        expect(resolveAction).not.toHaveBeenCalledWith('moment-1', 'send');
        expect(panel.getByRole('button', { name: sendLabel(locale) })).toBeEnabled();
        expect(panel.queryByText(fill(locale.mainChat.proactive.draftSentHeld, { name: 'Dana' }))).toBeNull();
    });

    it.each(['en', 'zh-CN'])('hands the draft to Mail after 5 seconds, then reads "Sent to {name} · {time}" in %s', async (language) => {
        await i18n.changeLanguage(language);
        const locale = language === 'en' ? en : zh;
        sendDraftAction.mockResolvedValue('acted');
        const panel = await openDraftPanel(locale);
        fireEvent.click(panel.getByRole('button', { name: sendLabel(locale) }));
        await passHold();
        expect(sendDraftAction).toHaveBeenCalledTimes(1);
        expect(sendDraftAction).toHaveBeenCalledWith('moment-1', '');
        expect(panel.getByRole('status')).toHaveTextContent(fill(locale.mainChat.proactive.draftSentAt, { name: 'Dana', time: clock(language) }));
        expect(panel.queryByRole('button', { name: locale.mainChat.proactive.undoSend })).toBeNull();
        expect(panel.queryByRole('button', { name: sendLabel(locale) })).toBeNull();
        expect(panel.queryByRole('alert')).toBeNull();
        expect(panel.getByText('Dana, Thursday works for me.')).toBeInTheDocument();
    });

    // K2 box 2: a receipt that fails to save still reads "Sent to {name} ·
    // {time}", with no Try again. Go answers "acted" once Mail took it and
    // saves the receipt apart (outbound_send_lifecycle_test.go); a sent card
    // read back later carries its send time.
    it.each(['en', 'zh-CN'])('reads a sent draft as sent, never Try again, in %s', async (language) => {
        await i18n.changeLanguage(language);
        const locale = language === 'en' ? en : zh;
        render(<MainChatProactiveCard item={sendDraft({}, { state: 'acted', sentAt: SENT_AT.getTime() })} />);
        const line = screen.getByRole('status');
        expect(line).toHaveTextContent(fill(locale.mainChat.proactive.draftSentAt, { name: 'Dana', time: clock(language) }));
        expect(line).toHaveAttribute('data-draft-send', 'sent');
        expect(screen.queryByRole('alert')).toBeNull();
        expect(screen.queryByText(locale.mainChat.proactive.done)).toBeNull();
        expect(screen.queryByRole('button', { name: /Try again|重试/ })).toBeNull();
        expect(screen.queryByRole('button', { name: locale.mainChat.card.button.review })).toBeNull();
        expect(JSON.stringify(locale)).not.toMatch(/receipt is not saved|回执尚未保存/);
    });

    it('does not call a card sent from another action without a send time', () => {
        render(<MainChatProactiveCard item={sendDraft({}, { state: 'acted' })} />);
        expect(screen.queryByText(/Sent to Dana/)).toBeNull();
        expect(screen.getByRole('status')).toHaveTextContent(en.mainChat.proactive.done);
    });

    // K2 box 3: Mail that doesn't answer shows the check-first line with only Edit.
    it.each(['en', 'zh-CN'])('shows Mail that did not say in copper with only Edit in %s', async (language) => {
        await i18n.changeLanguage(language);
        const locale = language === 'en' ? en : zh;
        sendDraftAction.mockRejectedValue(new Error('send outcome is unknown; the message will not be sent again automatically'));
        const panel = await openDraftPanel(locale);
        fireEvent.click(panel.getByRole('button', { name: sendLabel(locale) }));
        await passHold();
        const line = panel.getByRole('alert');
        expect(line).toHaveTextContent(locale.mainChat.proactive.draftUnknown);
        expect(line).toHaveClass('text-warning');
        expect(line.className).not.toMatch(/error|red|destructive/);
        expect(panel.queryByRole('button', { name: sendLabel(locale) })).toBeNull();
        expect(panel.queryByRole('button', { name: /Try again|重试/ })).toBeNull();
        expect(panel.getByRole('button', { name: locale.mainChat.proactive.editDraft })).toBeEnabled();
        expect(panel.queryByRole('button', { name: 'Dismiss' })).toBeNull();
        expect(panel.queryByText(/send outcome is unknown/)).toBeNull();
        expect(panel.queryByRole('status')).toBeNull();

        // Send is in the editor: a second send is the person's choice.
        fireEvent.click(panel.getByRole('button', { name: locale.mainChat.proactive.editDraft }));
        const editor = panel.getByRole('textbox', { name: locale.mainChat.proactive.editLabel });
        expect(editor).toHaveValue('Dana, Thursday works for me.');
        fireEvent.change(editor, { target: { value: 'Dana, Thursday at 3 works for me.' } });
        sendDraftAction.mockReset().mockResolvedValue('acted');
        fireEvent.click(panel.getByRole('button', { name: sendLabel(locale) }));
        expect(panel.getByRole('button', { name: locale.mainChat.proactive.undoSend })).toBeInTheDocument();
        await passHold();
        expect(sendDraftAction).toHaveBeenCalledWith('moment-1', 'Dana, Thursday at 3 works for me.');
        expect(panel.getByText('Dana, Thursday at 3 works for me.')).toBeInTheDocument();
        expect(panel.getByRole('status')).toHaveTextContent(fill(locale.mainChat.proactive.draftSentAt, { name: 'Dana', time: clock(language) }));
    });

    it.each(['en', 'zh-CN'])('shows a refused send as "Didn\'t send." with Send and Edit in %s', async (language) => {
        await i18n.changeLanguage(language);
        const locale = language === 'en' ? en : zh;
        sendDraftAction.mockRejectedValue(new Error('transport detail'));
        const panel = await openDraftPanel(locale);
        fireEvent.click(panel.getByRole('button', { name: sendLabel(locale) }));
        await passHold();
        const line = panel.getByRole('alert');
        expect(line).toHaveTextContent(locale.mainChat.proactive.draftRefused);
        expect(line).toHaveClass('text-warning');
        expect(panel.getByRole('button', { name: sendLabel(locale) })).toBeEnabled();
        expect(panel.getByRole('button', { name: locale.mainChat.proactive.editDraft })).toBeEnabled();
        expect(panel.queryByRole('button', { name: 'Dismiss' })).toBeNull();
        expect(panel.queryByText('transport detail')).toBeNull();
        expect(panel.getByText('Dana, Thursday works for me.')).toBeInTheDocument();
        // Send is the try again, held the same way.
        sendDraftAction.mockReset().mockResolvedValue('acted');
        fireEvent.click(panel.getByRole('button', { name: sendLabel(locale) }));
        expect(panel.getByRole('status')).toHaveTextContent(fill(locale.mainChat.proactive.draftSentHeld, { name: 'Dana' }));
        await passHold();
        expect(sendDraftAction).toHaveBeenCalledTimes(1);
    });

    it('never starts a second send while one is held or going', async () => {
        let finish!: (state: string) => void;
        sendDraftAction.mockReturnValue(new Promise<string>((resolve) => { finish = resolve; }));
        const panel = await openDraftPanel(en);
        fireEvent.click(panel.getByRole('button', { name: sendLabel(en) }));
        await passHold();
        expect(panel.getByRole('status')).toHaveTextContent(fill(en.mainChat.proactive.draftSent, { name: 'Dana' }));
        expect(panel.queryByRole('button', { name: sendLabel(en) })).toBeNull();
        expect(panel.queryByRole('button', { name: en.mainChat.proactive.undoSend })).toBeNull();
        await passHold();
        expect(sendDraftAction).toHaveBeenCalledTimes(1);
        await act(async () => finish('acted'));
        expect(panel.getByRole('status')).toHaveTextContent(fill(en.mainChat.proactive.draftSentAt, { name: 'Dana', time: clock('en') }));
    });

    // K2 box 4: Copy draft is in the card's ···.
    it.each(['en', 'zh-CN'])('puts Copy draft in the card\'s ··· beside Send and Edit in %s', async (language) => {
        await i18n.changeLanguage(language);
        const locale = language === 'en' ? en : zh;
        copyDraft.mockResolvedValue(true);
        render(<MainChatProactiveCard item={sendDraft()} />);
        await openCardPanel(locale.mainChat.card.button.review);
        const panel = within(screen.getByTestId('main-chat-item-panel'));
        expect(panel.getByRole('button', { name: sendLabel(locale) })).toBeEnabled();
        expect(panel.getByRole('button', { name: locale.mainChat.proactive.editDraft })).toBeEnabled();
        expect(panel.queryByRole('button', { name: locale.mainChat.proactive.copyDraft })).toBeNull();
        await clickMenuAction(locale.mainChat.proactive.copyDraft, locale.mainChat.proactive.moreActions);
        expect(copyDraft).toHaveBeenCalledWith('Dana, Thursday works for me.');
        expect(await panel.findByRole('status')).toHaveTextContent(locale.mainChat.proactive.draftCopied);
        expect(resolveAction).not.toHaveBeenCalled();
        expect(sendDraftAction).not.toHaveBeenCalled();
    });

    it('shows Not now with Undo for 5 seconds, records it, then folds to title and result (K5)', async () => {
        holdTimers();
        resolveAction.mockResolvedValue('deferred');
        render(<MainChatProactiveCard item={proactive({ kind: 'suggestion', actionIds: ['do_once', 'not_now'], actionLabels: { do_once: 'Do it', not_now: 'Not now' } })} />);
        fireEvent.click(screen.getByRole('button', { name: en.mainChat.card.button.notNow }));
        expect(await screen.findByRole('status')).toHaveTextContent(en.mainChat.proactive.held.notNow);
        expect(screen.getByRole('button', { name: en.mainChat.proactive.held.undo })).toBeEnabled();
        expect(resolveAction).not.toHaveBeenCalled();
        expect(screen.queryByRole('button', { name: en.mainChat.card.button.notNow })).toBeNull();
        await passCardHold();
        await waitFor(() => expect(resolveAction).toHaveBeenCalledWith('moment-1', 'not_now'));
        await waitFor(() => expect(document.querySelector('[data-proactive-held-folded]')).not.toBeNull());
        expect(screen.getByRole('heading', { name: 'Your brief is ready.' })).toBeInTheDocument();
        expect(screen.queryByRole('button', { name: en.mainChat.proactive.held.undo })).toBeNull();
    });

    it.each(['en', 'zh-CN'])('Undo within 5 seconds puts the card back and never acts in %s (K5)', async (language) => {
        await i18n.changeLanguage(language);
        const locale = language === 'en' ? en : zh;
        holdTimers();
        render(<MainChatProactiveCard item={proactive({ kind: 'suggestion', actionIds: ['do_once', 'not_now'], actionLabels: { do_once: 'Do it', not_now: 'Not now' } })} />);
        fireEvent.click(screen.getByRole('button', { name: locale.mainChat.card.button.doIt }));
        expect(screen.getByRole('status')).toHaveTextContent(locale.mainChat.proactive.held.started);
        fireEvent.click(screen.getByRole('button', { name: locale.mainChat.proactive.held.undo }));
        expect(screen.getByRole('button', { name: locale.mainChat.card.button.doIt })).toBeEnabled();
        await passCardHold();
        expect(resolveAction).not.toHaveBeenCalled();
    });

    it('offers Edit beside Undo after a playbook offer is added to Scheduled (K5)', async () => {
        holdTimers();
        const onOpen = vi.fn();
        render(<MainChatProactiveCard item={proactive({ kind: 'suggestion', actionIds: ['add_to_upcoming', 'not_now'], actionLabels: { add_to_upcoming: 'Add', not_now: 'Not now' } })} onOpenTarget={onOpen} detailPanel />);
        fireEvent.click(document.querySelector('[data-proactive-action="add_to_upcoming"]') as HTMLElement);
        expect(screen.getByRole('status')).toHaveTextContent(en.mainChat.proactive.held.scheduled);
        expect(screen.getByRole('button', { name: en.mainChat.proactive.held.undo })).toBeInTheDocument();
        fireEvent.click(screen.getByRole('button', { name: en.mainChat.proactive.held.edit }));
        expect(onOpen).toHaveBeenCalledWith({ kind: 'upcoming' });
        expect(resolveAction).not.toHaveBeenCalled();
    });

    it.each([
        ['brief_ready', en.mainChat.card.button.readIt],
        ['work_finished', 'Review'],
        ['decision_due', 'Review'],
    ])('mutes %s from More without running its card action', async (kind, label) => {
        holdTimers();
        resolveAction.mockResolvedValue('pending');
        if (kind === 'brief_ready') briefByID.mockResolvedValue({ brief: readyBrief('brief-1') });
        const onOpen = vi.fn();
        const item = kind === 'work_finished' ? work() : kind === 'decision_due' ? permission() : proactive({ briefId: 'brief-1' });
        const payload = item.toolInput as { actionIds: string[]; actionLabels: Record<string, string> };
        render(<MainChatProactiveCard item={{ ...item, toolInput: {
            ...payload, actionIds: [...payload.actionIds, 'mute_kind'], actionLabels: { ...payload.actionLabels, mute_kind: 'Mute this kind' },
        } }} onOpenTarget={onOpen} />);
        if (kind === 'brief_ready') await screen.findByRole('heading', { name: 'Evening brief · 1:50 PM' });
        await clickMenuAction(en.mainChat.proactive.muteKind);
        expect(screen.getByRole('status')).toHaveTextContent(en.mainChat.proactive.held.muted);
        expect(screen.getByRole('button', { name: en.mainChat.proactive.held.undo })).toBeInTheDocument();
        expect(resolveAction).not.toHaveBeenCalled();
        await passCardHold();
        await openMutedLine();
        expect(await screen.findByRole('status')).toHaveTextContent(en.mainChat.proactive.held.muted);
        expect(resolveAction).toHaveBeenCalledWith('moment-1', 'mute_kind');
        expect(onOpen).not.toHaveBeenCalled();
        expect(screen.getByRole('button', { name: label })).toBeEnabled();
        if (kind === 'brief_ready') {
            expect(screen.queryByTestId('main-chat-item-panel')).toBeNull();
            expect(recordReceipt).not.toHaveBeenCalledWith('moment-1', 'opened');
            return;
        }
        await openCardPanel(label);
        expect(recordReceipt).toHaveBeenCalledWith('moment-1', 'opened');
        expect(resolveAction).toHaveBeenCalledTimes(kind === 'work_finished' ? 2 : 1);
        expect(onOpen).not.toHaveBeenCalled();
    });

    it('restores mute after Resume and keeps permission controls on a refreshed muted card', async () => {
        const item = permission({
            kindMuted: true,
            actionIds: ['approve', 'decline', 'mute_kind'],
            actionLabels: { approve: 'Approve', decline: 'Decline', mute_kind: 'Mute this kind' },
        });
        const { rerender } = render(<MainChatProactiveCard item={item} />);
        expect(screen.queryByRole('button', { name: en.mainChat.proactive.moreActions })).toBeNull();
        expect(document.querySelector('[data-proactive-folded]')).toBeNull();
        expect(screen.getByRole('button', { name: en.mainChat.card.button.review })).toBeEnabled();
        await openCardPanel(en.mainChat.card.button.review);
        const panel = within(screen.getByTestId('main-chat-item-panel'));
        expect(panel.getByRole('button', { name: 'Approve' })).toBeEnabled();
        expect(panel.getByRole('button', { name: 'Decline' })).toBeEnabled();
        expect(panel.getByRole('status')).toHaveTextContent(en.mainChat.proactive.held.muted);
        fireEvent.click(panel.getByRole('button', { name: en.mainChat.proactive.closePanel }));
        rerender(<MainChatProactiveCard item={{ ...item, toolInput: { ...item.toolInput as object, kindMuted: false } }} />);
        expect(screen.getByRole('button', { name: en.mainChat.proactive.moreActions })).toBeEnabled();
        expect(screen.queryByRole('status')).toBeNull();
    });

    it('updates the same mounted card after muting, opening Settings, and turning its kind on', async () => {
        const item = proactive({ actionIds: ['open', 'mute_kind'], actionLabels: { open: 'Play', mute_kind: 'Mute this kind' } });
        const disabled = {
            available: true, level: 'custom', quietStart: '22:00', quietEnd: '08:00',
            dailyInterruptBudget: 6, digestTimes: [], statsDays: 30,
            kinds: [{ kind: 'brief_ready', enabled: false, defaultOn: true, paused: false, sent: 1, opened: 0, acted: 0, dismissed: 0, muted: 1 }],
        };
        settingsMocks.get.mockResolvedValueOnce({
            ...disabled,
            kinds: [{ ...disabled.kinds[0], enabled: true, muted: 0 }],
        }).mockResolvedValue(disabled);
        settingsMocks.setKind.mockResolvedValue({ ...disabled, kinds: [{ ...disabled.kinds[0], enabled: true }] });
        settingsMocks.week.mockResolvedValue({ available: true, sent: 1, actedOn: 0, days: 7, kinds: [] });
        resolveAction.mockResolvedValue('pending');
        function Harness() {
            const [settingsOpen, setSettingsOpen] = useState(false);
            return <>
                <MainChatProactiveCard item={item} onOpenTarget={vi.fn()} />
                <button onClick={() => setSettingsOpen(true)}>Settings</button>
                {settingsOpen && <div role="dialog" data-state="open">
                    <NotificationSettingsPage />
                    <button onClick={() => setSettingsOpen(false)}>Close settings</button>
                </div>}
            </>;
        }
        holdTimers();
        render(<Harness />);
        const card = document.querySelector('[data-main-chat-proactive]');
        await clickMenuAction(en.mainChat.proactive.muteKind);
        await passCardHold();
        await openMutedLine();
        expect(await screen.findByRole('status')).toHaveTextContent(en.mainChat.proactive.held.muted);
        fireEvent.click(screen.getByRole('button', { name: 'Settings' }));
        const toggle = await screen.findByRole('switch', { name: 'Brief ready' });
        expect(toggle).not.toBeChecked();
        fireEvent.click(toggle);
        await waitFor(() => expect(toggle).toBeChecked());
        fireEvent.click(screen.getByRole('button', { name: 'Close settings' }));
        expect(document.querySelector('[data-main-chat-proactive]')).toBe(card);
        expect(screen.getByRole('button', { name: en.mainChat.proactive.moreActions })).toBeEnabled();
        expect(screen.queryByText(en.mainChat.proactive.held.muted)).toBeNull();
        expect(settingsMocks.setKind).toHaveBeenCalledWith('brief_ready', true);
    });

    it('updates mounted cards when the computer receives a phone preference change', async () => {
        render(<MainChatProactiveCard item={proactive({ actionIds: ['mute_kind'], actionLabels: { mute_kind: 'Mute this kind' } })} />);
        await act(async () => emitRuntimeEvent('engagement:settings-changed', { kinds: [{ kind: 'brief_ready', enabled: false }] }));
        openFoldedLine();
        expect(screen.getByRole('status')).toHaveTextContent(en.mainChat.proactive.held.muted);
        expect(screen.queryByRole('button', { name: en.mainChat.proactive.moreActions })).toBeNull();
        await act(async () => emitRuntimeEvent('engagement:settings-changed', { kinds: [{ kind: 'brief_ready', enabled: true }] }));
        expect(screen.getByRole('button', { name: en.mainChat.proactive.moreActions })).toBeEnabled();
        expect(screen.queryByRole('status')).toBeNull();
    });

    it.each([false, true])('keeps a cached muted card resumed after remount (Resume while chat absent: %s)', async (resumeWhileAbsent) => {
        const cachedItem = proactive({ kindMuted: true, actionIds: ['mute_kind'], actionLabels: { mute_kind: 'Mute this kind' } });
        const disabled = {
            available: true, level: 'custom', quietStart: '22:00', quietEnd: '08:00',
            dailyInterruptBudget: 6, digestTimes: [], statsDays: 30,
            kinds: [{ kind: 'brief_ready', enabled: false, defaultOn: true, paused: false, sent: 1, opened: 0, acted: 0, dismissed: 0, muted: 1 }],
        };
        let current = disabled;
        settingsMocks.get.mockImplementation(async () => current);
        settingsMocks.setKind.mockImplementation(async () => {
            current = { ...disabled, kinds: [{ ...disabled.kinds[0], enabled: true }] };
            return current;
        });
        settingsMocks.week.mockResolvedValue({ available: true, sent: 1, actedOn: 0, days: 7, kinds: [] });
        function Harness() {
            const [chatVisible, setChatVisible] = useState(true);
            const [settingsOpen, setSettingsOpen] = useState(false);
            return <>
                {chatVisible && <MainChatProactiveCard item={cachedItem} />}
                <button onClick={() => { setSettingsOpen(true); if (resumeWhileAbsent) setChatVisible(false); }}>Settings</button>
                <button onClick={() => setChatVisible(false)}>Today</button>
                <button onClick={() => setChatVisible(true)}>Main chat</button>
                {settingsOpen && <div role="dialog" data-state="open">
                    <NotificationSettingsPage />
                    <button onClick={() => setSettingsOpen(false)}>Close settings</button>
                </div>}
            </>;
        }
        render(<Harness />);
        await openMutedLine();
        expect(await screen.findByRole('status')).toHaveTextContent(en.mainChat.proactive.held.muted);
        fireEvent.click(screen.getByRole('button', { name: 'Settings' }));
        const toggle = await screen.findByRole('switch', { name: 'Brief ready' });
        fireEvent.click(toggle);
        await waitFor(() => expect(toggle).toBeChecked());
        fireEvent.click(screen.getByRole('button', { name: 'Close settings' }));
        fireEvent.click(screen.getByRole('button', { name: 'Today' }));
        fireEvent.click(screen.getByRole('button', { name: 'Main chat' }));
        await waitFor(() => expect(screen.getByRole('button', { name: en.mainChat.proactive.moreActions })).toBeEnabled());
        expect(screen.queryByText(en.mainChat.proactive.held.muted)).toBeNull();
        expect(cachedItem.toolInput).toMatchObject({ kindMuted: true });
    });

    it('reads a phone preference change missed while all cards were unmounted', async () => {
        const cachedItem = proactive({ kindMuted: true, actionIds: ['mute_kind'], actionLabels: { mute_kind: 'Mute this kind' } });
        settingsMocks.get.mockResolvedValue({ available: true, kinds: [{ kind: 'brief_ready', enabled: false }] });
        const { rerender } = render(<MainChatProactiveCard item={cachedItem} />);
        await act(async () => {});
        openFoldedLine();
        expect(screen.getByRole('status')).toHaveTextContent(en.mainChat.proactive.held.muted);
        rerender(<></>);
        expect(runtimeListeners.has('engagement:settings-changed')).toBe(false);
        settingsMocks.get.mockResolvedValue({ available: true, kinds: [{ kind: 'brief_ready', enabled: true }] });
        rerender(<MainChatProactiveCard item={cachedItem} />);
        await waitFor(() => expect(screen.getByRole('button', { name: en.mainChat.proactive.moreActions })).toBeEnabled());
        expect(screen.queryByRole('status')).toBeNull();
        expect(settingsMocks.get).toHaveBeenCalledTimes(2);
    });

    it('does not let a slow initial preference read replace a newer Resume event', async () => {
        let resolveRead!: (view: { available: boolean; kinds: { kind: string; enabled: boolean }[] }) => void;
        settingsMocks.get.mockImplementation(() => new Promise((resolve) => { resolveRead = resolve; }));
        render(<MainChatProactiveCard item={proactive({ kindMuted: true, actionIds: ['mute_kind'], actionLabels: { mute_kind: 'Mute this kind' } })} />);
        await act(async () => emitRuntimeEvent('engagement:settings-changed', { kinds: [{ kind: 'brief_ready', enabled: true }] }));
        await act(async () => resolveRead({ available: true, kinds: [{ kind: 'brief_ready', enabled: false }] }));
        expect(screen.getByRole('button', { name: en.mainChat.proactive.moreActions })).toBeEnabled();
        expect(screen.queryByRole('status')).toBeNull();
    });

    it('localizes mute and keeps it available after a failed write', async () => {
        await i18n.changeLanguage('zh-CN');
        resolveAction.mockRejectedValue(new Error('unavailable'));
        const { rerender } = render(<MainChatProactiveCard item={proactive({ actionIds: ['mute_kind'], actionLabels: { mute_kind: 'Mute this kind' } })} />);
        holdTimers();
        await clickMenuAction(zh.mainChat.proactive.muteKind, zh.mainChat.proactive.moreActions);
        await passCardHold();
        expect(await screen.findByRole('alert')).toHaveTextContent(zh.failure.start);
        fireEvent.click(screen.getByRole('button', { name: zh.errors.actions.tryAgain }));
        await passCardHold();
        expect(resolveAction).toHaveBeenCalledTimes(2);
        expect(await screen.findByRole('alert')).toHaveTextContent(zh.failure.start);
        expect(screen.getByRole('button', { name: zh.mainChat.proactive.moreActions })).toBeEnabled();
        rerender(<MainChatProactiveCard item={proactive({ kindMuted: true })} />);
        openFoldedLine();
        expect(screen.getByRole('status')).toHaveTextContent(zh.mainChat.proactive.held.muted);
    });

    it.each(['light', 'dark'])('uses the design system button tokens in %s mode', async (theme) => {
        document.documentElement.classList.toggle('dark', theme === 'dark');
        briefByID.mockResolvedValue({ brief: readyBrief('brief-1'), building: false, enabled: true, nextAt: '', nextSlot: '', empty: false, missing: [] });
        try {
            render(<MainChatProactiveCard item={proactive({ briefId: 'brief-1' })} onOpenTarget={vi.fn()} />);
            const button = await screen.findByRole('button', { name: en.mainChat.card.button.hearIt });
            expect(screen.getByRole('button', { name: en.mainChat.card.button.readIt })).toHaveClass('bg-transparent');
            expect(button.closest('[data-proactive-moment]')).toHaveClass('bg-panel-warm', 'border');
            expect(button).toHaveClass('bg-solid-btn-bg', 'text-solid-btn-fg', 'rounded-lg', 'focus-visible:ring-2');
            expect(button.className).not.toMatch(/(?:bg|text)-\[#[0-9a-f]+\]/i);
            expect(button).not.toHaveClass('rounded-full');
            expect(screen.queryByRole('button', { name: 'Dismiss' })).toBeNull();
        } finally {
            document.documentElement.classList.remove('dark');
        }
    });

    it.each(['en', 'zh-CN'])('shows the saved suggestion decision in %s', async (language) => {
        await i18n.changeLanguage(language);
        resolveAction.mockResolvedValue('deferred');
        const action = 'not_now';
        const locale = language === 'en' ? en : zh;
        const actionLabel = locale.mainChat.card.button.notNow;
        const card = proactive({ kind: 'suggestion', actionIds: [action], actionLabels: { [action]: actionLabel } });
        const { unmount } = render(<MainChatProactiveCard item={card} />);
        fireEvent.click(screen.getByRole('button', { name: actionLabel }));
        expect(await screen.findByRole('status')).toHaveTextContent(locale.mainChat.proactive.held.notNow);
        expect(screen.queryByRole('button', { name: actionLabel })).toBeNull();
        unmount();
        resetMainChatHeldActions();
        render(<MainChatProactiveCard item={proactive({ kind: 'suggestion', state: 'deferred', actionIds: [action], actionLabels: { [action]: actionLabel } })} />);
        expect(screen.getByRole('status')).toHaveTextContent(locale.mainChat.proactive.held.notNow);
        expect(screen.queryByRole('button', { name: actionLabel })).toBeNull();
    });

    it('reviews blocked work and opens its details', async () => {
        const onOpen = vi.fn();
        resolveAction.mockResolvedValue('acted');
        render(<MainChatProactiveCard item={proactive({
            kind: 'work_blocked', actionIds: ['review', 'stop', 'dismiss'], actionLabels: { review: 'Review', stop: 'Stop', dismiss: 'Dismiss' },
            target: { kind: 'handoff', id: 'work-1' },
            work: { handoffId: 'work-1', status: 'blocked', summary: 'The work is blocked.' },
        })} onOpenTarget={onOpen} />);
        expect(screen.queryByTestId('main-chat-item-panel')).toBeNull();
        await openCardPanel(en.mainChat.card.button.answer);
        expect(screen.getByTestId('main-chat-item-panel')).toHaveAttribute('data-panel-open', 'moment-1');
        expect(onOpen).not.toHaveBeenCalled();
        expect(resolveAction).toHaveBeenCalledWith('moment-1', 'review');
    });

    it('keeps blocked handoff stop reachable and never offers Dismiss', async () => {
        resolveAction.mockResolvedValue('acted');
        render(<MainChatProactiveCard item={proactive({
            kind: 'work_blocked',
            actionIds: ['review', 'stop', 'dismiss'],
            actionLabels: { review: 'Review', stop: 'Stop', dismiss: 'Dismiss' },
            target: { kind: 'handoff', id: 'work-1' },
            work: { handoffId: 'work-1', status: 'blocked', summary: 'The work is blocked.' },
        })} />);
        fireEvent.click(screen.getByRole('button', { name: en.mainChat.card.button.stop }));
        await waitFor(() => expect(resolveAction).toHaveBeenCalledWith('moment-1', 'stop'));
        // Dismiss is retired (§7): the card never offers it, even when emitted.
        expect(screen.queryByRole('button', { name: 'Dismiss' })).toBeNull();
        expect(screen.queryByRole('menuitem', { name: 'Dismiss' })).toBeNull();
    });

    it('keeps Today handoff review and stop bound to the emitted action IDs, without Dismiss', async () => {
        resolveAction.mockResolvedValue('pending');
        const item = proactive({
            kind: 'work_blocked',
            actionIds: ['review', 'stop', 'dismiss'],
            actionLabels: { review: 'Review', stop: 'Stop', dismiss: 'Dismiss' },
            target: { kind: 'handoff', id: 'work-1' },
            work: { handoffId: 'work-1', status: 'blocked', summary: 'The work is blocked.' },
        });
        const view = render(<TodayExpandableCardPanelHarness item={item} />);
        fireEvent.click(screen.getByRole('button', { name: en.mainChat.card.button.answer }));
        expect(await screen.findByTestId('main-chat-item-panel')).toHaveAttribute('data-panel-open', 'moment-1');
        expect(resolveAction).toHaveBeenNthCalledWith(1, 'moment-1', 'review');
        const expandedCard = within(document.querySelector('[data-main-chat-proactive="work_blocked"]') as HTMLElement);
        fireEvent.click(expandedCard.getByRole('button', { name: en.mainChat.card.button.stop }));
        await waitFor(() => expect(resolveAction).toHaveBeenNthCalledWith(2, 'moment-1', 'stop'));
        expect(expandedCard.queryByRole('button', { name: 'Dismiss' })).toBeNull();
        expect(screen.queryByRole('menuitem', { name: 'Dismiss' })).toBeNull();
        view.unmount();
    });

    it.each([
        ['work_finished', 'completed', en.mainChat.card.button.review],
        ['work_blocked', 'aborted', en.mainChat.card.button.answer],
    ] as const)('uses only review for %s %s work on Chat and Today', async (kind, status, label) => {
        resolveAction.mockResolvedValue('pending');
        const item = proactive({
            kind,
            actionIds: ['review', 'dismiss'],
            actionLabels: { review: 'Review', dismiss: 'Dismiss' },
            target: { kind: 'handoff', id: 'work-1' },
            work: { handoffId: 'work-1', status, summary: 'The work is complete.' },
        });
        const chat = render(<CardPanelHarness item={item} />);
        expect(screen.queryByRole('button', { name: en.mainChat.card.button.stop })).toBeNull();
        expect(screen.queryByRole('button', { name: 'Dismiss' })).toBeNull();
        fireEvent.click(screen.getByRole('button', { name: label }));
        await waitFor(() => expect(resolveAction).toHaveBeenCalledWith('moment-1', 'review'));
        chat.unmount();

        resolveAction.mockClear().mockResolvedValue('pending');
        render(<TodayExpandableCardPanelHarness item={item} />);
        expect(screen.queryByRole('button', { name: en.mainChat.card.button.stop })).toBeNull();
        expect(screen.queryByRole('button', { name: 'Dismiss' })).toBeNull();
        fireEvent.click(screen.getByRole('button', { name: label }));
        await waitFor(() => expect(resolveAction).toHaveBeenCalledWith('moment-1', 'review'));
    });

    it('keeps a blocked handoff pending and shows the localized error when review is rejected', async () => {
        resolveAction.mockRejectedValue(new Error('resolver unavailable'));
        render(<MainChatProactiveCard item={proactive({
            kind: 'work_blocked',
            actionIds: ['review', 'stop', 'dismiss'],
            actionLabels: { review: 'Review', stop: 'Stop', dismiss: 'Dismiss' },
            target: { kind: 'handoff', id: 'work-1' },
            work: { handoffId: 'work-1', status: 'blocked', summary: 'The work is blocked.' },
        })} />);
        fireEvent.click(screen.getByRole('button', { name: en.mainChat.card.button.answer }));
        expect(await screen.findByRole('alert')).toHaveTextContent(en.failure.start);
        expect(resolveAction).toHaveBeenCalledWith('moment-1', 'review');
        expect(screen.getByRole('button', { name: en.mainChat.card.button.answer })).toBeEnabled();
        expect(screen.getByRole('button', { name: en.mainChat.card.button.stop })).toBeEnabled();
        expect(screen.queryByRole('status')).toBeNull();
        expect(screen.queryByTestId('main-chat-item-panel')).toBeNull();
    });

    it('keeps the Today handoff row pending and visible when review is rejected', async () => {
        resolveAction.mockRejectedValue(new Error('resolver unavailable'));
        const item = proactive({
            kind: 'work_blocked',
            actionIds: ['review', 'stop', 'dismiss'],
            actionLabels: { review: 'Review', stop: 'Stop', dismiss: 'Dismiss' },
            target: { kind: 'handoff', id: 'work-1' },
            work: { handoffId: 'work-1', status: 'blocked', summary: 'The work is blocked.' },
        });
        render(<TodayCardPanelHarness item={item} />);
        fireEvent.click(screen.getByRole('button', { name: en.mainChat.card.button.answer }));
        expect(await screen.findByRole('alert')).toHaveTextContent(en.failure.start);
        expect(resolveAction).toHaveBeenCalledWith('moment-1', 'review');
        expect(screen.getByRole('button', { name: en.mainChat.card.button.answer })).toBeEnabled();
        expect(screen.queryByTestId('main-chat-item-panel')).toBeNull();
    });

    it('opens and saves a trimmed learned-fact correction', async () => {
        correctFact.mockResolvedValue('acted');
        const item = proactive({
            kind: 'learned_to_confirm',
            actionIds: ['keep', 'correct'],
            actionLabels: { keep: 'Looks right', correct: 'Not right' },
            target: { kind: 'memory', id: 'operation-1' },
        });
        render(<MainChatProactiveCard item={item} />);
        fireEvent.click(screen.getByRole('button', { name: en.mainChat.card.button.notQuite }));
        const editor = screen.getByRole('textbox', { name: en.mainChat.proactive.correction.label });
        expect(correctFact).not.toHaveBeenCalled();
        const save = screen.getByRole('button', { name: en.mainChat.proactive.correction.save });
        expect(save).toBeDisabled();
        fireEvent.change(editor, { target: { value: '   ' } });
        expect(save).toBeDisabled();
        fireEvent.change(editor, { target: { value: '  Correct fact.  ' } });
        expect(save).toBeEnabled();
        fireEvent.click(save);
        await waitFor(() => expect(correctFact).toHaveBeenCalledWith('moment-1', 'Correct fact.'));
        expect(correctFact).toHaveBeenCalledTimes(1);
        expect(screen.getByRole('status')).toHaveTextContent(en.mainChat.proactive.results.learned_to_confirm_correct);
    });

    it('cancels a learned-fact correction without persisting', () => {
        const item = proactive({
            kind: 'learned_to_confirm',
            actionIds: ['keep', 'correct'],
            actionLabels: { keep: 'Looks right', correct: 'Not right' },
        });
        render(<MainChatProactiveCard item={item} />);
        fireEvent.click(screen.getByRole('button', { name: en.mainChat.card.button.notQuite }));
        fireEvent.click(screen.getByRole('button', { name: en.mainChat.proactive.correction.cancel }));
        expect(correctFact).not.toHaveBeenCalled();
        expect(screen.queryByRole('textbox', { name: en.mainChat.proactive.correction.label })).toBeNull();
    });

    it('keeps a failed learned-fact correction open with the text intact', async () => {
        correctFact.mockRejectedValue(new Error('private correction error'));
        render(<MainChatProactiveCard item={proactive({
            kind: 'learned_to_confirm',
            actionIds: ['keep', 'correct'],
            actionLabels: { keep: 'Looks right', correct: 'Not right' },
        })} />);
        fireEvent.click(screen.getByRole('button', { name: en.mainChat.card.button.notQuite }));
        const editor = screen.getByRole('textbox', { name: en.mainChat.proactive.correction.label });
        fireEvent.change(editor, { target: { value: '  Keep the meeting short.  ' } });
        fireEvent.click(screen.getByRole('button', { name: en.mainChat.proactive.correction.save }));
        expect(await screen.findByRole('alert')).toHaveTextContent(en.failure.save);
        expect(screen.getByRole('textbox', { name: en.mainChat.proactive.correction.label })).toHaveValue('  Keep the meeting short.  ');
        expect(correctFact).toHaveBeenCalledWith('moment-1', 'Keep the meeting short.');
        expect(screen.queryByText('private correction error')).toBeNull();
    });


    it.each(['work_finished', 'work_blocked'])('opens the shared panel for %s without opening the work chat', async (kind) => {
        const onOpen = vi.fn();
        render(<MainChatProactiveCard item={work({
            kind,
            ...(kind === 'work_blocked' ? { actionIds: ['review', 'stop', 'dismiss'], actionLabels: { review: 'Review', stop: 'Stop', dismiss: 'Dismiss' } } : {}),
        })} onOpenTarget={onOpen} />);
        expect(screen.getByText('The comparison is ready.')).toBeInTheDocument();
        resolveAction.mockResolvedValue('acted');
        await openCardPanel(kind === 'work_blocked' ? en.mainChat.card.button.answer : en.mainChat.card.button.review);
        expect(screen.getByTestId('main-chat-item-panel')).toHaveAttribute('data-panel-open', 'moment-1');
        expect(recordReceipt).toHaveBeenCalledWith('moment-1', 'opened');
        expect(resolveAction).toHaveBeenCalledWith('moment-1', kind === 'work_blocked' ? 'review' : 'review');
        expect(onOpen).not.toHaveBeenCalled();
    });

    it.each(['en', 'zh-CN'])('renders each work lifecycle and only valid actions in %s', async (language) => {
        await i18n.changeLanguage(language);
        const copy = language === 'en' ? enApp : zhApp;
        for (const status of [
            'active', 'in_progress', 'needs_review', 'blocked', 'completed', 'aborted', 'stale',
        ] as const) {
            const view = render(<MainChatProactiveCard item={work({
                work: { handoffId: 'work-1', status },
                actionIds: ['review', 'stop', 'dismiss', 'approve', 'mute_kind'],
                actionLabels: { review: 'Old review label', stop: 'Old stop label', dismiss: 'Old dismiss label', approve: 'Approve', mute_kind: 'Old mute label' },
            })} />);
            expect(screen.getByText(copy.handoffsPage.status[status])).toBeInTheDocument();
            expect(screen.getByRole('button', { name: (language === 'en' ? en : zh).mainChat.card.button.review })).toBeEnabled();
            expect(screen.queryByRole('button', { name: copy.handoffsPage.stop })).toBeNull();
            expect(screen.queryByRole('button', { name: copy.handoffsPage.dismiss })).toBeNull();
            expect(screen.queryByRole('button', { name: 'Approve' })).toBeNull();
            expect(screen.queryByText('Old stop label')).toBeNull();
            const user = userEvent.setup();
            await user.click(screen.getByRole('button', { name: (language === 'en' ? en : zh).mainChat.proactive.moreActions }));
            expect(await screen.findByRole('menuitem', { name: (language === 'en' ? en : zh).mainChat.proactive.muteKind })).toBeInTheDocument();
            expect(resolveAction).not.toHaveBeenCalled();
            view.unmount();
        }
    });

    it('keeps review failures and interrupted work visible with localized verification', async () => {
        const { rerender } = render(<MainChatProactiveCard item={work({
            work: { handoffId: 'work-1', status: 'needs_review', verification: { met: 1, total: 2 } },
        })} />);
        expect(screen.getByText('Needs review')).toBeInTheDocument();
        expect(screen.getByText('1 of 2 checks met')).toBeInTheDocument();
        expect(screen.queryByText('Done')).toBeNull();
        await act(() => i18n.changeLanguage('zh-CN'));
        expect(screen.getByText('2 项检查中有 1 项通过')).toBeInTheDocument();
        rerender(<MainChatProactiveCard item={work({
            kind: 'work_blocked', work: { handoffId: 'work-1', status: 'blocked', blockedReason: 'interrupted' },
        })} />);
        expect(screen.getByText(zhApp.handoffsPage.status.blocked)).toBeInTheDocument();
        expect(screen.getByText(zhApp.handoffsPage.blockedReason.interrupted)).toBeInTheDocument();
        expect(screen.queryByText('2 项检查中有 1 项通过')).toBeNull();
    });

    it('does not show a malformed verification count as confirmed evidence', () => {
        render(<MainChatProactiveCard item={work({
            work: { handoffId: 'work-1', status: 'needs_review', verification: { met: 3, total: 2 } },
        })} />);
        expect(screen.getByText('Needs review')).toBeInTheDocument();
        expect(screen.queryByText(/checks met/)).toBeNull();
    });

    it('opens work details while keeping provenance as non-button text', async () => {
        const onOpen = vi.fn();
        render(<MainChatProactiveCard item={work()} onOpenTarget={onOpen} />);
        expect(screen.getByText('From Tasks')).toBeInTheDocument();
        expect(screen.queryByRole('button', { name: 'From Tasks' })).toBeNull();
        await openCardPanel(en.mainChat.card.button.review);
        expect(screen.getByTestId('main-chat-item-panel')).toHaveAttribute('data-panel-open', 'moment-1');
        expect(resolveAction).toHaveBeenCalledWith('moment-1', 'review');
        expect(onOpen).not.toHaveBeenCalled();
    });

    it('shows a summary already included in the catalog body only once', () => {
        render(<MainChatProactiveCard item={work({ body: 'Review your result.\nThe comparison is ready.' })} />);
        expect(screen.getAllByText(/The comparison is ready\./)).toHaveLength(1);
    });

    it('does not expose Stop or Dismiss on work cards', () => {
        render(<MainChatProactiveCard item={work({ work: { handoffId: 'work-1', status: 'in_progress' } })} />);
        expect(screen.queryByRole('button', { name: 'Stop' })).toBeNull();
        expect(screen.queryByRole('button', { name: 'Dismiss' })).toBeNull();
        expect(screen.getByRole('button', { name: en.mainChat.card.button.review })).toBeEnabled();
    });

    it.each([['approve', 'Approve'], ['decline', 'Decline']])('answers a running work permission with %s', async (action, label) => {
        resolveAction.mockResolvedValue('acted');
        const onOpen = vi.fn();
        render(<MainChatProactiveCard item={permission()} onOpenTarget={onOpen} />);
        expect(screen.getByText(en.mainChat.proactive.requestDetails)).toBeInTheDocument();
        expect(screen.getByText('Tool: Bash')).toBeInTheDocument();
        expect(screen.getByText('Run the repository checks.')).toBeInTheDocument();
        expect(screen.queryByRole('button', { name: 'From Tasks' })).toBeNull();
        await openCardPanel(en.mainChat.card.button.review);
        const panel = within(screen.getByTestId('main-chat-item-panel'));
        fireEvent.click(panel.getByRole('button', { name: label }));
        expect(await panel.findByRole('status')).toHaveTextContent(en.mainChat.proactive.permissionAnswered);
        expect(resolveAction).toHaveBeenCalledWith('moment-1', action);
        expect(panel.queryByRole('button', { name: 'Approve' })).toBeNull();
        expect(panel.queryByRole('button', { name: 'Decline' })).toBeNull();
        expect(onOpen).not.toHaveBeenCalled();
    });

    it.each(['acted', 'expired'])('reflects a permission %s on another surface and localizes request details', async (state) => {
        await i18n.changeLanguage('zh-CN');
        const { rerender } = render(<MainChatProactiveCard item={permission()} />);
        expect(screen.getByText(zh.mainChat.proactive.requestDetails)).toBeInTheDocument();
        expect(screen.getByText('工具：Bash')).toBeInTheDocument();
        rerender(<MainChatProactiveCard item={permission({ state })} />);
        expect(screen.getByRole('status')).toHaveTextContent(state === 'expired' ? zh.mainChat.proactive.permissionExpired : zh.mainChat.proactive.permissionAnswered);
        expect(screen.queryByRole('button', { name: 'Approve' })).toBeNull();
        expect(screen.queryByRole('button', { name: 'Decline' })).toBeNull();
        expect(resolveAction).not.toHaveBeenCalled();
    });

    it('keeps both permission actions available after a failed response', async () => {
        resolveAction.mockRejectedValue(new Error('provider disconnected'));
        render(<MainChatProactiveCard item={permission()} />);
        await openCardPanel(en.mainChat.card.button.review);
        const panel = within(screen.getByTestId('main-chat-item-panel'));
        fireEvent.click(panel.getByRole('button', { name: 'Approve' }));
        expect(await panel.findByRole('alert')).toHaveTextContent(en.failure.answer);
        expect(panel.getByRole('button', { name: 'Approve' })).toBeEnabled();
        expect(panel.getByRole('button', { name: 'Decline' })).toBeEnabled();
    });

    it('shows generic copy once when the body is the same as the headline', () => {
        render(<MainChatProactiveCard item={proactive({ headline: 'A reply is waiting.', body: 'A reply is waiting.' })} />);
        expect(screen.getAllByText('A reply is waiting.')).toHaveLength(1);
    });

    it('opens a person card, then reaches the named People destination', async () => {
        const onOpen = vi.fn();
        const item = proactive({ kind: 'person_reappeared', personName: 'Dana', actionIds: ['open_person'], actionLabels: { open_person: 'Stored label' }, target: { kind: 'person', id: 'person-1' }, evidence: [{ kind: 'person', id: 'person-1', target: { kind: 'person', id: 'person-1' } }] });
        render(<MainChatProactiveCard item={item} onOpenTarget={onOpen} />);
        await openCardPanel(en.mainChat.card.button.showName.replace('{{name}}', 'Dana'));
        expect(onOpen).not.toHaveBeenCalled();
        expect(resolveAction).not.toHaveBeenCalled();
        fireEvent.click(screen.getByRole('button', { name: en.mainChat.card.button.showName.replace('{{name}}', 'Dana') }));
        expect(onOpen).toHaveBeenCalledWith({ kind: 'person', id: 'person-1', runId: undefined });
    });

    it('labels app and list evidence links without implying an exact source opens', () => {
        render(<MainChatProactiveCard item={proactive({ evidence: [
            { kind: 'calendar', id: 'event-1', target: { kind: 'calendar', id: 'event-1' } },
            { kind: 'mail', id: 'person-1', target: { kind: 'mail' } },
        ] })} onOpenTarget={vi.fn()} />);
        expect(screen.getByText('From Calendar')).toBeInTheDocument();
        expect(screen.getByText('From Mail')).toBeInTheDocument();
        expect(screen.queryByRole('button', { name: 'From Calendar' })).toBeNull();
        expect(screen.queryByRole('button', { name: 'From Mail' })).toBeNull();
    });

    it('uses the actual suggestion handler through the validated binding', async () => {
        holdTimers();
        resolveAction.mockResolvedValue('acted');
        render(<MainChatProactiveCard item={proactive({ kind: 'suggestion', actionIds: ['do_once', 'not_now'], actionLabels: { do_once: 'Stored do-once label', not_now: 'Stored not-now label' }, target: { kind: 'suggestion', id: 'initiative-1' } })} />);
        expect(screen.getByRole('button', { name: en.mainChat.card.button.doIt })).toHaveClass('bg-solid-btn-bg');
        expect(screen.getByRole('button', { name: en.mainChat.card.button.notNow })).not.toHaveClass('bg-solid-btn-bg');
        fireEvent.click(screen.getByRole('button', { name: en.mainChat.card.button.doIt }));
        expect(await screen.findByRole('status')).toHaveTextContent(en.mainChat.proactive.held.started);
        await passCardHold();
        await waitFor(() => expect(resolveAction).toHaveBeenCalledWith('moment-1', 'do_once'));
    });

    it.each(['en', 'zh-CN'])('shows work results and current action words in a detail panel in %s', async (language) => {
        await i18n.changeLanguage(language);
        const { unmount } = render(<MainChatProactiveCard item={work()} detailPanel />);
        expect(screen.getByText('The full comparison.')).toBeInTheDocument();
        expect(screen.queryByRole('button', { name: /Review|查看/ })).toBeNull();
        unmount();
        render(<MainChatProactiveCard item={proactive({ kind: 'suggestion', actionIds: ['do_once', 'not_now'], actionLabels: { do_once: 'Do once', not_now: 'Later' }, target: { kind: 'suggestion', id: 'suggestion-1' } })} detailPanel />);
        expect(screen.getByRole('button', { name: language === 'en' ? 'Do it' : '就做这个' })).toBeInTheDocument();
        expect(screen.getByRole('button', { name: language === 'en' ? 'Not now' : '暂不' })).toBeInTheDocument();
    });

    it('never renders unsupported or unlabeled actions', () => {
        render(<MainChatProactiveCard item={proactive({ actionIds: ['snooze_day', 'not_mine', 'invented'], actionLabels: { snooze_day: 'Tomorrow', not_mine: 'Not mine', invented: 'Invented' } })} onOpenTarget={vi.fn()} />);
        expect(screen.queryByRole('button', { name: 'Tomorrow' })).toBeNull();
        expect(screen.queryByRole('button', { name: 'Not mine' })).toBeNull();
        expect(screen.queryByRole('button', { name: 'Invented' })).toBeNull();
        expect(document.querySelector('[data-proactive-action]')).toBeNull();
    });

    it('shows a localized retry message and keeps actions when the write fails', async () => {
        resolveAction.mockRejectedValue(new Error('internal detail'));
        const onOpen = vi.fn();
        render(<MainChatProactiveCard item={proactive({ kind: 'reminder', actionIds: ['done', 'not_now'], actionLabels: { done: 'Done', not_now: 'Not now' } })} onOpenTarget={onOpen} />);
        fireEvent.click(screen.getByRole('button', { name: en.mainChat.card.button.done }));
        expect(await screen.findByRole('alert')).toHaveTextContent(en.failure.done);
        expect(screen.getByRole('button', { name: en.mainChat.card.button.done })).toBeEnabled();
        expect(onOpen).not.toHaveBeenCalled();
        expect(screen.queryByText('internal detail')).toBeNull();
    });

    it('renders digest children in the same card with their own real actions', async () => {
        const child = proactive({
            kind: 'meeting_prep',
            headline: 'Design review at 14:05.',
            body: 'Starts in 15 min.',
            actionIds: ['open'],
            actionLabels: { open: 'Open Calendar' },
            target: { kind: 'calendar' },
            evidence: [{ kind: 'event', id: 'event-1', target: { kind: 'calendar' } }],
            event: { title: 'Design review', start: '2026-10-26T14:05:00Z' },
        }).toolInput;
        render(<MainChatProactiveCard item={proactive({ momentId: 'digest', kind: 'digest', headline: 'A few things for you.', body: '', actionIds: [], items: [child] })} onOpenTarget={vi.fn()} />);
        expect(document.querySelectorAll('[data-main-chat-proactive]')).toHaveLength(1);
        expect(screen.getByRole('heading', { name: 'Design review at 14:05.' })).toBeInTheDocument();
        await openCardPanel(en.mainChat.card.button.openPrep);
        expect(recordReceipt).toHaveBeenCalledWith('moment-1', 'opened');
        expect(resolveAction).not.toHaveBeenCalled();
        expect(recordReceipt.mock.calls.some(([id]) => id === 'digest')).toBe(false);
    });

    it('opens a digest child in the Today item panel', async () => {
        const child = sendDraft().toolInput;
        const item = proactive({ momentId: 'digest', kind: 'digest', headline: 'Updates', body: '', actionIds: [], items: [child] });
        render(<TodayCardPanelHarness item={item} />);
        fireEvent.click(screen.getByRole('button', { name: en.mainChat.action.review }));
        expect(await screen.findByTestId('main-chat-item-panel')).toHaveAttribute('data-panel-open', 'moment-1');
    });

    it('reflects a remotely resolved card after its payload is refreshed', () => {
        const { rerender } = render(<MainChatProactiveCard item={proactive()} onOpenTarget={vi.fn()} />);
        rerender(<MainChatProactiveCard item={proactive({ state: 'dismissed' })} onOpenTarget={vi.fn()} />);
        expect(screen.queryByRole('button', { name: 'Dismiss' })).toBeNull();
        expect(screen.getByRole('status')).toHaveTextContent(en.mainChat.proactive.held.notNow);
    });

    it('is rendered by StreamCard and malformed payloads keep the plain fallback', () => {
        const { unmount } = render(<StreamCard item={proactive()} onOpenReportTarget={vi.fn()} />);
        expect(document.querySelector('[data-main-chat-proactive]')).not.toBeNull();
        unmount();
        render(<StreamCard item={{ ...proactive(), toolInput: {} }} />);
        expect(screen.getByText(proactive().content!)).toBeInTheDocument();
    });

    it('routes promise messages and answer_ready posts through StreamCard', () => {
        const openAnswer = {
            id: 'answer-1', sessionId: 'session-1', messageId: 'question-1', question: 'Will it rain?',
            kind: 'weather', settles: 'result', delivery: 'settled_only', expiresAt: '2026-11-01T00:00:00Z',
            posts: 0, state: 'open', createdAt: '2026-10-26T09:00:00-07:00',
        } as const;
        const startedGeneration = beginOpenAnswerDelivery();
        registerDeliveredOpenAnswers([openAnswer], startedGeneration);
        render(<StreamCard item={{ id: 'promise-1', kind: 'assistant', content: 'Not yet.', openAnswer }} onSendOpenAnswer={vi.fn(async () => {})} />);
        expect(screen.getByText("I'll let you know when it's settled.")).toBeInTheDocument();
        render(<StreamCard item={{ id: 'answer-1', kind: 'assistant', cardType: 'answer_ready', content: 'It will be sunny.', openAnswer }} />);
        expect(screen.getByTestId('answer-ready-card')).toHaveTextContent('It will be sunny.');
    });
});

describe('main chat status row', () => {
    beforeEach(async () => {
        await i18n.changeLanguage('en');
        recordReceipt.mockReset().mockResolvedValue(undefined);
        settingsMocks.get.mockReset().mockResolvedValue({ available: false, kinds: [] });
        publishEngagementSettings({ kinds: [] });
    });
    afterEach(() => {
        vi.restoreAllMocks();
        vi.unstubAllGlobals();
    });

    it('shows a reservation email with its source and a working Show action', () => {
        const open = vi.fn();
        render(<MainChatStatusRow item={{ ...status(), toolInput: { period: '2026-10-30:evening', entries: [
            { id: 'reservation', kind: 'mail_update', text: 'Dinner reservation', target: { kind: 'mail' } },
        ] } }} onOpenTarget={open} defaultExpanded />);
        expect(screen.getByText(/From Mail/)).toHaveTextContent('Dinner reservation');
        fireEvent.click(screen.getByRole('button', { name: 'Show' }));
        expect(open).toHaveBeenCalledWith({ kind: 'mail', id: undefined, runId: undefined });
    });

    // chat-review v1.0.8: a source names the app or place the line is about,
    // from its kind; with no known place the line ends after its title.
    it('names the place each fold line is about and never a storage word', () => {
        render(<MainChatStatusRow item={{ ...status(), toolInput: { period: '2026-10-29:evening', entries: [
            { id: 'dinner', kind: 'meeting_prep', text: 'dinner at 6:00 PM', target: { kind: 'calendar', id: 'event-1' } },
            { id: 'reservation', kind: 'mail_update', text: 'Your reservation is confirmed.', target: { kind: 'mail' } },
            { id: 'lease', kind: 'schedule_done', text: 'Lease summary check finished. No changes found.', target: { kind: 'automation', id: 'job-1' } },
            { id: 'brief', kind: 'brief_ready', text: "Thursday's brief was added.", target: { kind: 'today' } },
            { id: 'thread', kind: 'slack_reply', text: 'Dana replied in #launch.', target: { kind: 'none' } },
            { id: 'unknown', kind: 'digest_done', text: 'Your updates were gathered.', target: { kind: 'none' } },
        ] } }} defaultExpanded />);
        const line = (text: string) => screen.getByText(text).closest('li')!;
        expect(line('dinner at 6:00 PM')).toHaveTextContent(/^dinner at 6:00 PM · From Calendar$/);
        expect(line('Your reservation is confirmed.')).toHaveTextContent(/^Your reservation is confirmed\. · From Mail$/);
        expect(line('Lease summary check finished. No changes found.')).toHaveTextContent(/^Lease summary check finished\. No changes found\. · From Tasks$/);
        expect(line("Thursday's brief was added.")).toHaveTextContent(/^Thursday's brief was added\. · From your brief$/);
        expect(line('Dana replied in #launch.')).toHaveTextContent(/^Dana replied in #launch\. · From Slack$/);
        expect(line('Your updates were gathered.')).toHaveTextContent(/^Your updates were gathered\.$/);
        expect(document.body.textContent).not.toMatch(/saved record|record|store|database/i);
    });

    // chat-review v1.0.8: a past pause states what happened with its times,
    // never "It's on again".
    it('states a past pause with its start and end, or only its start when the end is unknown', () => {
        const start = new Date(2026, 9, 29, 22, 5).getTime();
        const end = new Date(2026, 9, 29, 22, 40).getTime();
        render(<MainChatStatusRow item={{ ...status(), toolInput: { period: '2026-10-29:evening', entries: [
            { id: 'pause-range', kind: 'capture_paused', text: '', captureSource: 'screen', captureCause: 'permission', captureStartAt: start, captureEndAt: end, target: { kind: 'none' } },
            { id: 'pause-start', kind: 'capture_paused', text: '', captureSource: 'screen', captureCause: 'permission', captureStartAt: start, target: { kind: 'none' } },
        ] } }} onOpenTarget={vi.fn()} defaultExpanded />);
        expect(screen.getByText(/ from /).closest('li')).toHaveTextContent(/^Stopped watching your screen from 10:05 PM to 10:40 PM: Screen Recording was turned off\.$/);
        expect(screen.getByText(/ at /).closest('li')).toHaveTextContent(/^Stopped watching your screen at 10:05 PM: Screen Recording was turned off\.Turn it back on$/);
        expect(screen.getAllByRole('button', { name: 'Turn it back on' })).toHaveLength(1);
        expect(document.body.textContent).not.toMatch(/on again|Resume|From Observing/);
    });

    // chat-review v1.0.7: no fallback. Show only for a real saved destination;
    // otherwise the line is its title and source, with nothing where Show would sit.
    it('shows Show only on fold lines with a saved destination and leaves nothing in its place on the others', () => {
        const open = vi.fn();
        render(<MainChatStatusRow item={{ ...status(), toolInput: { period: '2026-10-29:evening', entries: [
            { id: 'done', kind: 'work_finished', text: 'Lease summary is ready.', target: { kind: 'handoff', id: 'work-7' } },
            { id: 'generic', kind: 'observing', text: 'Isoform noted a long focus block.', target: { kind: 'none' } },
            { id: 'no-id', kind: 'work_finished', text: 'Older result.', target: { kind: 'handoff' } },
            { id: 'legacy', kind: 'observing', text: 'Older note.' },
        ] } }} onOpenTarget={open} defaultExpanded />);
        const shows = screen.getAllByRole('button', { name: en.mainChat.card.button.show });
        expect(shows).toHaveLength(1);
        expect(shows[0].closest('li')).toHaveTextContent('Lease summary is ready.');
        fireEvent.click(shows[0]);
        expect(open).toHaveBeenCalledWith({ kind: 'handoff', id: 'work-7', runId: undefined });
        for (const [text, source] of [['Isoform noted a long focus block.', 'Observing'], ['Older result.', 'Tasks'], ['Older note.', 'Observing']]) {
            const line = screen.getByText(text).closest('li')!;
            expect(line).toHaveTextContent(`${text} · From ${source}`);
            expect(line.children).toHaveLength(1);
            expect(within(line).queryByRole('button')).toBeNull();
        }
    });

    it('does not replay retired brief-ready lines inside a folded status', () => {
        render(<MainChatStatusRow item={{ ...status(), toolInput: { period: '2026-10-30:evening', entries: [
            { id: 'brief', kind: 'brief_ready', text: 'Your brief is ready.', target: { kind: 'today' } },
            { id: 'week', kind: 'weekly_recap', text: 'Your week is ready.', target: { kind: 'today' } },
        ] } }} onOpenTarget={vi.fn()} defaultExpanded />);
        expect(screen.queryByText(/Your (?:brief|week) is ready/)).toBeNull();
        expect(screen.getByText('Brief')).toBeInTheDocument();
        expect(screen.getByText('Your week')).toBeInTheDocument();
    });

    it('delivers linked status entries when folded and opens them only after expansion in view', async () => {
        let intersect!: (entries: Partial<IntersectionObserverEntry>[]) => void;
        vi.spyOn(document, 'hasFocus').mockReturnValue(true);
        vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible');
        vi.stubGlobal('IntersectionObserver', class {
            constructor(callback: typeof intersect) { intersect = callback; }
            observe() {}
            disconnect() {}
        });
        const item = status();
        const payload = item.toolInput as { period: string; entries: Record<string, unknown>[] };
        render(<MainChatStatusRow item={{ ...item, toolInput: { ...payload, entries: payload.entries.map((entry, index) => index === 0 ? { ...entry, momentId: 'capture-moment' } : entry) } }} />);
        await waitFor(() => expect(recordReceipt.mock.calls).toEqual([['capture-1', 'delivered']]));
        await act(async () => intersect([{ isIntersecting: true }]));
        expect(recordReceipt).toHaveBeenCalledTimes(1);
        expect(screen.queryByText('Screen capture has been off for 20 minutes.')).toBeNull();

        const toggle = screen.getByRole('button', { name: '2 background updates' });
        fireEvent.click(toggle);
        await act(async () => intersect([{ isIntersecting: false }]));
        expect(recordReceipt).toHaveBeenCalledTimes(1);
        await act(async () => intersect([{ isIntersecting: true }]));
        expect(recordReceipt.mock.calls).toEqual([['capture-1', 'delivered'], ['capture-1', 'opened']]);
        fireEvent.click(toggle);
        fireEvent.click(toggle);
        await act(async () => intersect([{ isIntersecting: true }]));
        expect(recordReceipt).toHaveBeenCalledTimes(2);
        expect(recordReceipt.mock.calls.some(([id]) => id === 'digest-1')).toBe(false);
    });

    it('folds routine entries into one tertiary line and opens their saved targets', () => {
        const onOpen = vi.fn();
        render(<MainChatStatusRow item={status()} onOpenTarget={onOpen} />);
        expect(screen.queryByText('Your digest is ready.')).toBeNull();
        const summary = screen.getByRole('button', { name: '2 background updates' });
        expect(summary).toHaveAttribute('aria-expanded', 'false');
        expect(summary).not.toHaveAttribute('aria-controls');
        expect(document.querySelector('[data-main-chat-status]')).not.toHaveClass('border', 'bg-panel-warm');
        fireEvent.click(summary);
        expect(summary).toHaveAttribute('aria-expanded', 'true');
        expect(document.getElementById(summary.getAttribute('aria-controls')!)).toBeInTheDocument();
        expect(screen.getByText('Your digest is ready.')).toBeInTheDocument();
        fireEvent.click(screen.getByRole('button', { name: en.mainChat.observationPause.turnBackOn }));
        expect(onOpen).toHaveBeenCalledWith({ kind: 'settings', id: 'activity' });
        fireEvent.click(summary);
        expect(summary).not.toHaveAttribute('aria-controls');
        expect(screen.queryByText('Your digest is ready.')).toBeNull();
    });

    // 2026-10-27: Show on "Your morning brief was missed" opened plain Today,
    // already beside the chat, so the click did nothing.
    it("sends a missed brief's Show to Today's brief state, not plain Today", () => {
        const onOpen = vi.fn();
        render(<MainChatStatusRow item={{ ...status(), toolInput: { period: '2026-10-27:morning', entries: [
            { id: 'missed-1', kind: 'brief_missed', text: 'Your morning brief was missed.', target: { kind: 'today' } },
            { id: 'digest-1', kind: 'digest_done', text: 'Your digest is ready.', target: { kind: 'today' } },
        ] } }} onOpenTarget={onOpen} defaultExpanded />);
        const shows = screen.getAllByRole('button', { name: en.mainChat.card.button.show });
        fireEvent.click(shows[0]);
        expect(onOpen).toHaveBeenLastCalledWith({ kind: 'today', id: TODAY_BRIEF_STATUS_TARGET });
        fireEvent.click(shows[1]);
        expect(onOpen).toHaveBeenLastCalledWith({ kind: 'today' });
    });

    // chat-review v1.0.10 H9: the line sat under Today but was about last
    // night's 6 PM brief, with no time, no cause, and a source it didn't need.
    it("words a missed brief from the brief's own day with its time and cause, and names no source", async () => {
        const item = { ...status(), localDay: '2026-11-02', toolInput: { period: '2026-11-02:morning', entries: [
            { id: 'missed-1', kind: 'brief_missed', text: "Your evening brief didn't run.", target: { kind: 'today' }, briefDay: '2026-11-01', briefSlot: 'evening', briefTime: '18:00', briefCause: 'asleep' },
        ] } };
        const view = { today: '2026-11-02', hideRoutine: false, topicNames: new Map(), chips: {} };
        const { unmount } = render(<MainChatViewContext.Provider value={view}><MainChatStatusRow item={item} onOpenTarget={vi.fn()} defaultExpanded /></MainChatViewContext.Provider>);
        const line = screen.getByText("Last night's evening brief didn't come at 6:00 PM: your computer was asleep.").closest('li') as HTMLElement;
        expect(line.textContent).not.toMatch(/From |didn't run/);
        unmount();
        await i18n.changeLanguage('zh-CN');
        render(<MainChatViewContext.Provider value={view}><MainChatStatusRow item={item} onOpenTarget={vi.fn()} defaultExpanded /></MainChatViewContext.Provider>);
        const zhLine = screen.getByText(/^昨晚的晚间简报没有在.+送达：电脑在睡眠。$/).closest('li') as HTMLElement;
        expect(zhLine.textContent).not.toContain('来自');
        await i18n.changeLanguage('en');
    });

    it('renders through StreamCard and translates chrome in both languages', async () => {
        await i18n.changeLanguage('zh-CN');
        render(<StreamCard item={status()} />);
        expect(screen.getByRole('button', { name: '2 条后台更新' })).toBeInTheDocument();
        expect(Object.keys(zh.mainChat.proactive)).toEqual(Object.keys(en.mainChat.proactive));
        expect(Object.keys(zh.mainChat.proactive.source)).toEqual(Object.keys(en.mainChat.proactive.source));
        expect(Object.keys(zh.mainChat.status)).toEqual(Object.keys(en.mainChat.status));
        expect(zh.mainChat.proactive.source.tasks).toBe('任务');
        expect(zh.mainChat.proactive.source.playbook).toBe('操作手册');
    });
});

// Chat review v1.0.2 (design's live walk-through of ab2d86ee).
describe('chat walk-through v1.0.2 cards', () => {
    afterEach(async () => {
        vi.useRealTimers();
        setComputerClock('12h');
        await i18n.changeLanguage('en');
    });
    const leftOff = (at: string) => proactive({
        kind: 'decision_due', headline: 'You were going through what Isoform learned about you.',
        body: 'Next: mark the beliefs in Learned today.', actionIds: ['open'], actionLabels: { open: 'Open' },
        target: { kind: 'thread', id: 'thread-1' },
        place: { app: 'Isoform', bundleId: 'ai.isoform.app', at },
        evidence: [{ kind: 'thread_outcome', id: 'thread-outcome:thread-1', at: Date.parse(at), target: { kind: 'thread', id: 'thread-1' } }],
    });

    it.each([
        ['en', 'Left off in Isoform · Yesterday, 7:01 PM'],
        ['zh-CN', '停在Isoform · 昨天 19:01'],
    ])('#2 reads a left-off card as two sentences and one source line with one time in %s', async (language, line) => {
        vi.useFakeTimers({ toFake: ['Date'] });
        vi.setSystemTime(new Date(2026, 9, 30, 9));
        await i18n.changeLanguage(language);
        render(<MainChatProactiveCard item={leftOff(new Date(2026, 9, 29, 19, 1).toISOString())} onOpenTarget={vi.fn()} />);
        expect(screen.getByText(language === 'en' ? 'What Isoform learned about you' : 'Isoform 对你的了解')).toBeInTheDocument();
        expect(screen.getByText(language === 'en' ? "Next: say That's me or Not quite to this week's new beliefs." : '下一步：对本周的新认识选择“说得对”或“不太对”。')).toBeInTheDocument();
        expect(document.querySelector('[data-left-off]')).toHaveTextContent(line);
        const text = document.body.textContent ?? '';
        expect(text).not.toMatch(/has a next step|saved record|已保存的记录/);
        expect(text.match(language === 'en' ? /7:01/g : /19:01/g)).toHaveLength(1);
        expect(screen.getByRole('button', { name: (language === 'en' ? en : zh).mainChat.card.button.showThread })).toBeInTheDocument();
    });

    it('shows a left-off title only once when its body repeats it with punctuation', () => {
        render(<MainChatProactiveCard item={proactive({
            kind: 'decision_due', headline: 'Review the launch plan', body: 'Review the launch plan.',
            target: { kind: 'thread', id: 'thread-1' }, actionIds: [], evidence: [],
        })} onOpenTarget={vi.fn()} />);
        expect(screen.getAllByText(/^Review the launch plan\.?$/)).toHaveLength(1);
        expect(screen.getByRole('heading', { name: 'Review the launch plan' })).toBeInTheDocument();
    });

    it('#2 keeps a left-off card folded after the next morning brief without its buttons', () => {
        render(<MainChatProactiveCard item={{ ...leftOff(new Date(2026, 9, 29, 19, 1).toISOString()), rowClass: 'routine' }} onOpenTarget={vi.fn()} />);
        expect(screen.queryByRole('button', { name: en.mainChat.card.button.showThread })).toBeNull();
        expect(screen.queryByRole('button', { name: en.mainChat.card.button.takeMeThere })).toBeNull();
    });

    it.each([
        [new Date(2026, 9, 29, 19, 1), 'From your brief · Yesterday, 7:01 PM'],
        [new Date(2026, 9, 22, 22, 58), 'From your brief · Thu, Oct 22, 10:58 PM'],
        [new Date(2026, 9, 27, 22, 58), 'From your brief · Tue, 10:58 PM'],
    ])('#3 writes every source line one way: %s', (at, line) => {
        vi.useFakeTimers({ toFake: ['Date'] });
        vi.setSystemTime(new Date(2026, 9, 30, 9));
        render(<MainChatProactiveCard item={proactive({ kind: 'suggestion', evidence: [{ kind: 'brief', id: 'b', at: at.getTime(), target: { kind: 'today' } }] })} />);
        expect(screen.getByText(line)).toBeInTheDocument();
    });

    it('#4 follows the computer\'s 24-hour clock', () => {
        vi.useFakeTimers({ toFake: ['Date'] });
        vi.setSystemTime(new Date(2026, 9, 30, 9));
        setComputerClock('24h');
        render(<MainChatProactiveCard item={proactive({ kind: 'suggestion', evidence: [{ kind: 'brief', id: 'b', at: new Date(2026, 9, 29, 19, 1).getTime(), target: { kind: 'today' } }] })} />);
        expect(screen.getByText('From your brief · Yesterday, 19:01')).toBeInTheDocument();
    });

    it.each(['en', 'zh-CN'])('#1 folds a started meeting into one "{title} at {time}" line in %s', async (language) => {
        await i18n.changeLanguage(language);
        const start = new Date(Date.now() - 60_000);
        render(<MainChatProactiveCard item={proactive({ kind: 'meeting_prep', headline: 'Design review starts at 2:05 PM.', body: '', actionIds: ['open'], event: { title: 'Design review', start: start.toISOString() } })} />);
        const time = computerClock(start, language);
        expect(document.querySelector('[data-meeting-started]')).toHaveTextContent(language === 'en' ? `Design review at ${time}` : `${time}的Design review`);
        expect(screen.queryByRole('button')).toBeNull();
    });

    it('#1 shows no meeting card when the title is unknown', () => {
        const { container } = render(<MainChatProactiveCard item={proactive({ kind: 'meeting_prep', event: { start: '2099-01-01T10:00:00Z', private: true } })} />);
        expect(container).toBeEmptyDOMElement();
    });

    it('gives a folded calendar line its source and a working Show action', () => {
        const open = vi.fn();
        const start = new Date(Date.now() - 60_000);
        render(<MainChatProactiveCard item={proactive({ kind: 'meeting_prep', event: { title: 'dinner', start: start.toISOString() }, target: { kind: 'calendar', id: 'event-1' } })} onOpenTarget={open} />);
        expect(document.querySelector('[data-meeting-started]')).toHaveTextContent('From Calendar');
        fireEvent.click(screen.getByRole('button', { name: 'Show' }));
        expect(open).toHaveBeenCalledWith({ kind: 'calendar', id: 'event-1' });
    });

    it('#5 puts the brief card\'s ··· on its button row', async () => {
        briefByID.mockResolvedValue({ brief: readyBrief('brief-1'), building: false, enabled: true, nextAt: '', nextSlot: '', empty: false, missing: [] });
        render(<MainChatProactiveCard item={proactive({ briefId: 'brief-1', actionIds: ['open', 'dismiss', 'mute_kind'], actionLabels: { open: 'Play', dismiss: 'Dismiss', mute_kind: 'Mute' } })} onOpenTarget={vi.fn()} />);
        await screen.findByRole('heading', { name: 'Evening brief · 1:50 PM' });
        const more = screen.getByRole('button', { name: en.mainChat.proactive.moreActions });
        const row = screen.getByRole('button', { name: en.mainChat.card.button.readIt }).parentElement!;
        expect(row).toContainElement(more);
    });

    it('#6 #7 keeps the composer to field, attach, voice and Send with "Ask anything…"', () => {
        expect(enApp.composer.placeholder.default).toBe('Ask anything…');
        expect(zhApp.composer.placeholder.default).toBe('问点什么…');
        expect(enToday.mainChatPanel.placeholder).toBe('Ask anything…');
        expect(zhToday.mainChatPanel.placeholder).toBe('问点什么…');
        expect(appSource).not.toMatch(/MainChatScopeControl|scopeControl=/);
    });

    it('#8 names the switch Hide updates / Show updates in both languages', () => {
        expect([en.mainChat.view.hideUpdates, en.mainChat.view.showUpdates]).toEqual(['Hide updates', 'Show updates']);
        expect([zh.mainChat.view.hideUpdates, zh.mainChat.view.showUpdates]).toEqual(['隐藏更新', '显示更新']);
        expect(JSON.stringify(en)).not.toMatch(/folded update/i);
    });
});
