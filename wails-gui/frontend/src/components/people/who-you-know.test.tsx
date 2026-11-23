import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import i18n from '@/i18n';
import type { PersonLoop } from '@/lib/wails-shim-app';
import { PersonDetailPanel, type PersonActions } from './person-detail';
import { OpenBetweenYou, promiseWhere, sortPromises, type PromiseEntry } from './open-between-you';
import { resetPageQueries } from '@/lib/page-queries';

// Who you know as promises and claims you judge (redesign-you v1.0.0 rules
// 16 to 18): Open between you with Y/N/M/E, a person's voice with Forget,
// and Chat corrections shown read-only in Notes.
const fixture = vi.hoisted(() => ({ bindings: new Map<string, ReturnType<typeof vi.fn>>() }));
vi.mock('@/lib/wails-shim-app', () => new Proxy({}, {
    has: () => true,
    get: (_target, name: string) => {
        if (name === 'then') return undefined;
        if (!fixture.bindings.has(name)) fixture.bindings.set(name, vi.fn().mockResolvedValue(null));
        return fixture.bindings.get(name);
    },
}));
vi.mock('../../wailsjs/runtime/runtime', () => ({ EventsOn: () => () => {} }));
vi.mock('../../../wailsjs/runtime/runtime', () => ({ EventsOn: () => () => {} }));
vi.mock('@/lib/notify', () => ({ notifyToast: vi.fn() }));
vi.mock('sonner', () => ({ toast: Object.assign(vi.fn(), { error: vi.fn(), success: vi.fn() }) }));

const DAY = 24 * 60 * 60 * 1000;
const NOW = Date.now();
const actions = new Proxy({} as PersonActions, { get: () => vi.fn() });

function promise(id: string, overrides: Partial<PersonLoop> = {}): PersonLoop {
    return { id, kind: 'you_owe', since: NOW - DAY, title: `Promise ${id}`, dueAt: 0, source: 'purpose', eventId: `event-${id}`, where: 'mail', whereAt: NOW - DAY, ...overrides };
}
function entry(loop: PersonLoop, name = 'Lena'): PromiseEntry {
    return { loop, personId: 'lena', name };
}
function detail(overrides: Record<string, unknown> = {}) {
    return { id: 'lena', name: 'Lena', relationship: '', notes: '', researchSummary: '', loops: [], together: [], sharedTopics: [], seenIn: [], messageCount: 0, chatNotes: [], ...overrides };
}
function bind(name: string, fn: ReturnType<typeof vi.fn>) {
    fixture.bindings.set(name, fn);
    return fn;
}

beforeEach(async () => {
    resetPageQueries();
    fixture.bindings.clear();
    await i18n.changeLanguage('en');
});
afterEach(() => {
    cleanup();
    resetPageQueries();
});

describe('Open between you', () => {
    it('lists each promise both ways under Still open? with Yes, No, Not mine and Edit', () => {
        render(<OpenBetweenYou entries={[entry(promise('a')), entry(promise('b', { kind: 'waiting_on_them', where: 'meeting' }), 'Sam')]} count="1 person is waiting on you" onOpenPerson={vi.fn()} onAnswered={vi.fn()} />);
        const section = screen.getByRole('region', { name: 'Open between you' });
        expect(within(section).getByText('1 person is waiting on you')).toBeInTheDocument();
        const lines = within(section).getAllByRole('article');
        expect(lines).toHaveLength(2);
        for (const line of lines) {
            expect(within(line).getByText('Still open?')).toBeInTheDocument();
            for (const name of ['Yes', 'No', 'Not mine', 'Edit']) expect(within(line).getByRole('button', { name })).toBeInTheDocument();
        }
        expect(section).toHaveTextContent(/You owe · email/);
        expect(section).toHaveTextContent(/Owed to you · a meeting/);
        expect(within(section).getByRole('button', { name: 'Open Sam' })).toBeInTheDocument();
    });

    it('puts this week first, then the rest', () => {
        const old = entry(promise('old', { since: NOW - 30 * DAY, whereAt: NOW - 30 * DAY, dueAt: NOW + DAY }));
        const fresh = entry(promise('fresh'));
        expect(sortPromises([old, fresh], NOW).map((e) => e.loop.id)).toEqual(['fresh', 'old']);
    });

    it('says where it was heard and when it is due', () => {
        const t = (key: string, options?: Record<string, unknown>) => i18n.t(`people:${key}`, options as never) as unknown as string;
        expect(promiseWhere(t, 'en', promise('a', { where: 'mail', whereAt: NOW, dueAt: NOW }), NOW)).toBe('You owe · email, Today · due Today');
        expect(promiseWhere(t, 'en', promise('b', { kind: 'waiting_on_them', where: undefined, whereAt: 0, since: 0 }), NOW)).toBe('Owed to you');
    });

    it('records Yes and tells the page to read again', async () => {
        const answer = bind('AnswerPersonPromise', vi.fn().mockResolvedValue({ loopId: 'a', eventId: 'e2', title: 'Promise a', open: true, followUpDrafted: false }));
        const answered = vi.fn();
        render(<OpenBetweenYou entries={[entry(promise('a'))]} onOpenPerson={vi.fn()} onAnswered={answered} />);
        fireEvent.click(screen.getByRole('button', { name: 'Yes' }));
        expect(await screen.findByText('Still open.')).toBeInTheDocument();
        expect(answer).toHaveBeenCalledWith(expect.objectContaining({ loopId: 'a', expectedEventId: 'event-a', personId: 'lena', answer: 'yes' }));
        expect(answered).toHaveBeenCalled();
    });

    it('asks why on No: Already done, Not a promise, or Skip', async () => {
        const answer = bind('AnswerPersonPromise', vi.fn().mockResolvedValue({ loopId: 'a', eventId: 'e2', title: 'Promise a', open: false, followUpDrafted: false }));
        render(<OpenBetweenYou entries={[entry(promise('a')), entry(promise('b')), entry(promise('c'))]} onOpenPerson={vi.fn()} onAnswered={vi.fn()} />);
        const [a, b, c] = screen.getAllByRole('article');
        fireEvent.click(within(a).getByRole('button', { name: 'No' }));
        fireEvent.click(within(a).getByRole('button', { name: 'Already done' }));
        expect(await within(a).findByText('Closed: already done.')).toBeInTheDocument();
        fireEvent.click(within(b).getByRole('button', { name: 'No' }));
        fireEvent.click(within(b).getByRole('button', { name: 'Not a promise' }));
        expect(await within(b).findByText('Closed: not a promise.')).toBeInTheDocument();
        fireEvent.click(within(c).getByRole('button', { name: 'No' }));
        fireEvent.click(within(c).getByRole('button', { name: 'Skip' }));
        expect(await within(c).findByText('Closed.')).toBeInTheDocument();
        expect(answer.mock.calls.map(([input]) => [input.answer, input.reason])).toEqual([['no', 'already_done'], ['no', 'not_a_promise'], ['no', undefined]]);
    });

    it('answers on the keys: M is Not mine, E opens the edit, arrows move between lines', async () => {
        const answer = bind('AnswerPersonPromise', vi.fn().mockResolvedValue({ loopId: 'a', eventId: 'e2', title: 'Promise a', open: false, followUpDrafted: false }));
        render(<OpenBetweenYou entries={[entry(promise('a')), entry(promise('b'))]} onOpenPerson={vi.fn()} onAnswered={vi.fn()} />);
        const [a, b] = screen.getAllByRole('article');
        a.focus();
        fireEvent.keyDown(a, { key: 'ArrowDown' });
        expect(document.activeElement).toBe(b);
        fireEvent.keyDown(b, { key: 'e' });
        expect(within(b).getByLabelText('The promise')).toBeInTheDocument();
        fireEvent.keyDown(a, { key: 'm' });
        expect(await within(a).findByText("Someone else's, so it's off Lena.")).toBeInTheDocument();
        expect(answer).toHaveBeenCalledWith(expect.objectContaining({ loopId: 'a', answer: 'not_mine' }));
    });

    it('edits the words and the date', async () => {
        const answer = bind('AnswerPersonPromise', vi.fn().mockResolvedValue({ loopId: 'a', eventId: 'e2', title: 'Send the signed contract', open: true, followUpDrafted: false }));
        render(<OpenBetweenYou entries={[entry(promise('a'))]} onOpenPerson={vi.fn()} onAnswered={vi.fn()} />);
        fireEvent.click(screen.getByRole('button', { name: 'Edit' }));
        fireEvent.change(screen.getByLabelText('The promise'), { target: { value: 'Send the signed contract' } });
        fireEvent.change(screen.getByLabelText('Due'), { target: { value: '2030-01-15' } });
        fireEvent.click(screen.getByRole('button', { name: 'Save' }));
        expect(await screen.findByText('Saved in your words.')).toBeInTheDocument();
        expect(answer).toHaveBeenCalledWith(expect.objectContaining({ answer: 'edit', title: 'Send the signed contract', dueDate: '2030-01-15', setDue: true }));
    });

    it('a failed answer says so with Try again and keeps the buttons', async () => {
        const answer = bind('AnswerPersonPromise', vi.fn().mockRejectedValueOnce(new Error('boom')).mockResolvedValue({ loopId: 'a', eventId: 'e2', title: 'Promise a', open: true, followUpDrafted: false }));
        render(<OpenBetweenYou entries={[entry(promise('a'))]} onOpenPerson={vi.fn()} onAnswered={vi.fn()} />);
        fireEvent.click(screen.getByRole('button', { name: 'Yes' }));
        expect(await screen.findByRole('alert')).toHaveTextContent("Couldn't save that answer.");
        expect(screen.getByRole('button', { name: 'Yes' })).toBeInTheDocument();
        fireEvent.click(screen.getByRole('button', { name: 'Try again' }));
        expect(await screen.findByText('Still open.')).toBeInTheDocument();
        expect(answer).toHaveBeenCalledTimes(2);
    });

    it('has every new word in Chinese', async () => {
        await i18n.changeLanguage('zh-CN');
        render(<OpenBetweenYou entries={[entry(promise('a'))]} onOpenPerson={vi.fn()} onAnswered={vi.fn()} />);
        expect(screen.getByRole('region', { name: '你们之间未完成的事' })).toBeInTheDocument();
        expect(screen.getByText('还没完成？')).toBeInTheDocument();
        expect(screen.getByRole('button', { name: '不是我的' })).toBeInTheDocument();
    });
});

describe('A person on Who you know', () => {
    it('shows their promises as lines and keeps a reply you owe with Draft a reply', async () => {
        bind('GetPersonDetail', vi.fn().mockResolvedValue(detail({ loops: [promise('a'), { id: 'm1', kind: 'reply_owed', since: NOW - DAY, title: '', dueAt: 0 }] })));
        render(<PersonDetailPanel personId="lena" revision={0} actions={actions} />);
        const line = await screen.findByRole('article', { name: /Still open\? Promise a/ });
        expect(within(line).getByRole('button', { name: 'Not mine' })).toBeInTheDocument();
        expect(screen.getByRole('button', { name: 'Draft a reply' })).toBeInTheDocument();
    });

    it('shows a Chat correction read-only in Notes, and nothing once Undo removed it', async () => {
        const read = bind('GetPersonDetail', vi.fn().mockResolvedValue(detail({ notes: 'Prefers mornings', chatNotes: [{ id: 'n1', text: 'Lena leads the import team' }] })));
        const entryRow = { principal: { id: 'lena', kind: 'contact', displayName: 'Lena', notes: 'Prefers mornings', aliases: [] }, stats: { totalMessages: 0, byApp: [], recentMessages: [] } } as never;
        const page = render(<PersonDetailPanel personId="lena" entry={entryRow} revision={0} actions={actions} />);
        expect(await screen.findByText('Lena leads the import team')).toBeInTheDocument();
        expect(screen.getByText('Said in Chat')).toBeInTheDocument();
        expect(screen.getByText('Prefers mornings')).toBeInTheDocument();
        // Read-only: no field holds it.
        expect(document.querySelector('[data-chat-note] textarea, [data-chat-note] input')).toBeNull();
        read.mockResolvedValue(detail({ notes: 'Prefers mornings', chatNotes: [] }));
        page.rerender(<PersonDetailPanel personId="lena" entry={entryRow} revision={1} actions={actions} />);
        await waitFor(() => expect(screen.queryByText('Lena leads the import team')).not.toBeInTheDocument());
        expect(screen.getByText('Prefers mornings')).toBeInTheDocument();
    });

    it('counts the conversations you named them in, lists them, and forgets their voice', async () => {
        bind('GetPersonDetail', vi.fn().mockResolvedValue(detail()));
        bind('GetPersonVoice', vi.fn().mockResolvedValue({ conversations: [{ id: 'c1', startAt: NOW }, { id: 'c2', startAt: NOW - 30 * DAY }], samples: 0 }));
        const forget = bind('ForgetPersonVoice', vi.fn().mockResolvedValue(2));
        render(<PersonDetailPanel personId="lena" revision={0} actions={actions} />);
        expect(await screen.findByText("Isoform knows Lena's voice from 2 conversations you named.")).toBeInTheDocument();
        expect(screen.getByRole('heading', { name: 'Their voice' })).toBeInTheDocument();
        expect(screen.getByText('Voices stay on this computer.')).toBeInTheDocument();
        fireEvent.click(screen.getByRole('button', { name: 'Which conversations' }));
        expect(screen.getByText('Conversation, Today')).toBeInTheDocument();
        fireEvent.click(screen.getByRole('button', { name: 'Forget their voice' }));
        expect(await screen.findByText('Forgot their voice in 2 conversations. They show as a speaker again.')).toBeInTheDocument();
        expect(forget).toHaveBeenCalledWith('lena');
    });

    it('says nothing about a voice no conversation named', async () => {
        bind('GetPersonDetail', vi.fn().mockResolvedValue(detail()));
        bind('GetPersonVoice', vi.fn().mockResolvedValue({ conversations: [], samples: 0 }));
        render(<PersonDetailPanel personId="lena" revision={0} actions={actions} />);
        await screen.findByRole('heading', { name: 'Lena' });
        await waitFor(() => expect(fixture.bindings.get('GetPersonVoice')).toHaveBeenCalled());
        expect(screen.queryByRole('heading', { name: 'Their voice' })).not.toBeInTheDocument();
    });

    it('a failed voice read is an error with Try again, never empty', async () => {
        bind('GetPersonDetail', vi.fn().mockResolvedValue(detail()));
        const read = bind('GetPersonVoice', vi.fn().mockRejectedValue(new Error('down')));
        render(<PersonDetailPanel personId="lena" revision={0} actions={actions} />);
        expect(await screen.findByText("Couldn't read what Isoform knows of their voice.")).toBeInTheDocument();
        read.mockResolvedValue({ conversations: [{ id: 'c1', startAt: NOW }], samples: 0 });
        fireEvent.click(screen.getByRole('button', { name: 'Try again' }));
        expect(await screen.findByText("Isoform knows Lena's voice from 1 conversation you named.")).toBeInTheDocument();
    });

    it('a failed Forget says so with Try again', async () => {
        bind('GetPersonDetail', vi.fn().mockResolvedValue(detail()));
        bind('GetPersonVoice', vi.fn().mockResolvedValue({ conversations: [{ id: 'c1', startAt: NOW }], samples: 0 }));
        bind('ForgetPersonVoice', vi.fn().mockRejectedValue(new Error('down')));
        render(<PersonDetailPanel personId="lena" revision={0} actions={actions} />);
        fireEvent.click(await screen.findByRole('button', { name: 'Forget their voice' }));
        expect(await screen.findByText("Couldn't forget their voice.")).toBeInTheDocument();
        expect(screen.getByText("Isoform knows Lena's voice from 1 conversation you named.")).toBeInTheDocument();
    });

    it("shows Next with Today's prep when their meeting is next", async () => {
        bind('GetPersonDetail', vi.fn().mockResolvedValue(detail()));
        bind('GetTodayBeforeYouTalk', vi.fn().mockResolvedValue({ meetingId: 'm', meetingTitle: 'Import sync', startAt: NOW + 3600_000, endAt: NOW + 7200_000, personId: 'lena', personName: 'Lena', youOwe: 'the test plan' }));
        render(<PersonDetailPanel personId="lena" revision={0} actions={actions} />);
        expect(await screen.findByRole('heading', { name: 'Next · Import sync' })).toBeInTheDocument();
        expect(screen.getByText('You owe: the test plan')).toBeInTheDocument();
    });
});
