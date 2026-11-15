import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import i18n from '@/i18n';
import en from '@/i18n/locales/en/chat.json';
import zh from '@/i18n/locales/zh-CN/chat.json';

const bindings = vi.hoisted(() => ({
    RequestPermission: vi.fn(),
    OpenPermissionSettings: vi.fn(),
}));
vi.mock('../../wailsjs/go/main/App', () => bindings);
vi.mock('@/components/tasks/TaskThreadPane', () => ({
    TaskThreadPane: ({ id }: { id: string }) => <div data-testid="task-thread">{id}</div>,
}));
const toast = vi.hoisted(() => ({ error: vi.fn(), success: vi.fn() }));
vi.mock('sonner', () => ({ toast }));

import { ChatFailureLine } from './chat-failure-line';
import { quietRetry } from '@/lib/chat-failure';
import { PermissionQuestionCard } from './permission-question-card';
import { ComposerSuggestions } from './composer-suggestions';
import { MainChatAnswerChips } from './main-chat/main-chat-answer-chips';
import { MainChatViewContext, type MainChatStreamView } from './main-chat/main-chat-view-context';
import { MainChatPageColumn } from './main-chat/main-chat-stream-body';
import { closeMainChatSidePanel, openMainChatReplyDraft, openMainChatTask } from '@/lib/main-chat-side-panel';

beforeEach(async () => {
    await i18n.changeLanguage('en');
    vi.clearAllMocks();
});
afterEach(() => {
    act(() => closeMainChatSidePanel());
    cleanup();
});

describe('Chat K3: one set of failure words', () => {
    it('says the failure in place, gray, never red, and no toast', () => {
        render(<ChatFailureLine text={i18n.t('failure.loadChat', { ns: 'chat' })} onRetry={() => true} />);
        const line = screen.getByRole('alert');
        expect(line).toHaveTextContent(`${en.failure.loadChat}${en.errors.actions.tryAgain}`);
        expect(line.className).toContain('text-subtle');
        expect(line.className).not.toMatch(/error|red|destructive/);
        expect(toast.error).not.toHaveBeenCalled();
    });

    it('can be copper', () => {
        render(<ChatFailureLine tone="copper" text="x" />);
        expect(screen.getByRole('alert').className).toContain('text-warning');
    });

    it('has every K3 line in en and zh-CN', () => {
        for (const key of Object.keys(en.failure)) {
            expect((zh.failure as Record<string, string>)[key], key).toBeTruthy();
        }
        expect(zh.failure.still).toBe('还是没成功。');
    });

    it('a second failed Try again reads "Still didn\'t work."', async () => {
        const retry = vi.fn().mockResolvedValue(false);
        render(<ChatFailureLine text={en.failure.save} onRetry={retry} />);
        fireEvent.click(screen.getByRole('button', { name: en.errors.actions.tryAgain }));
        await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent(en.failure.still));
        expect(retry).toHaveBeenCalledTimes(1);
    });

    it('an automatic load gets one quiet retry before it fails', async () => {
        const run = vi.fn().mockRejectedValueOnce(new Error('a')).mockResolvedValueOnce('ok');
        await expect(quietRetry(run)).resolves.toBe('ok');
        const fail = vi.fn().mockRejectedValue(new Error('b'));
        await expect(quietRetry(fail)).rejects.toThrow('b');
        expect(fail).toHaveBeenCalledTimes(2);
    });

    it('answering a question rings the chosen button and dims the others', async () => {
        let finish: (ok: boolean) => void = () => undefined;
        const onSelect = vi.fn(() => new Promise<boolean>((resolve) => { finish = resolve; }));
        render(
            <PermissionQuestionCard
                question="Which one?"
                options={[{ id: 'a', name: 'Alpha', kind: 'allow_once' }, { id: 'b', name: 'Beta', kind: 'allow_once' }] as never}
                onSelect={onSelect}
                onSkip={() => undefined}
            />,
        );
        fireEvent.click(screen.getByRole('button', { name: 'Alpha' }));
        const chosen = screen.getByRole('button', { name: 'Alpha' });
        expect(chosen).toHaveAttribute('aria-busy', 'true');
        expect(screen.getByTestId('answer-ring')).toBeInTheDocument();
        expect(screen.getByRole('button', { name: 'Beta' }).className).toContain('opacity-60');
        expect(chosen.className).not.toContain('opacity-60');
        await act(async () => finish(false));
        expect(await screen.findByText(en.failure.answer)).toBeInTheDocument();
        expect(toast.error).not.toHaveBeenCalled();
    });
});

describe('Chat K4: a click finishes in Chat', () => {
    it('a suggestion is sent at once and leaves the row', () => {
        const onSelect = vi.fn();
        render(<ComposerSuggestions suggestions={['Plan my day', 'Read mail']} isLoading={false} isRefreshing={false} onSelect={onSelect} />);
        fireEvent.click(screen.getByRole('button', { name: 'Plan my day' }));
        expect(onSelect).toHaveBeenCalledWith('Plan my day');
        expect(screen.queryByRole('button', { name: 'Plan my day' })).toBeNull();
        expect(screen.getByRole('button', { name: 'Read mail' })).toBeInTheDocument();
    });

    it('an answer chip posts at once and folds to "You answered: … · Change"', () => {
        const send = vi.fn();
        const view: MainChatStreamView = { today: '2026-10-31', hideRoutine: false, topicNames: new Map(), chips: { q: ['Yes, today', 'Not now'] }, onFillChip: send, chipsSend: true };
        render(<MainChatViewContext.Provider value={view}><MainChatAnswerChips itemId="q" /></MainChatViewContext.Provider>);
        fireEvent.click(screen.getByRole('button', { name: 'Yes, today' }));
        expect(send).toHaveBeenCalledWith('Yes, today');
        expect(screen.getByText(/You answered/).parentElement).toHaveTextContent('You answered: Yes, today · Change');
        fireEvent.click(screen.getByRole('button', { name: en.mainChat.change }));
        expect(screen.getByRole('button', { name: 'Not now' })).toBeInTheDocument();
    });

    it("a report's reply opens a draft card with Send and Edit in Chat's panel", async () => {
        const send = vi.fn();
        render(<MainChatPageColumn enabled><div>chat</div></MainChatPageColumn>);
        act(() => openMainChatReplyDraft('See you at 3', send));
        const panel = screen.getByTestId('main-chat-side-panel');
        expect(panel).toHaveAttribute('data-side-panel', 'draft');
        expect(panel.style.width).toBe('420px');
        expect(screen.getByText('chat')).toBeInTheDocument();
        fireEvent.click(screen.getByRole('button', { name: en.mainChat.replyDraft.edit }));
        fireEvent.change(screen.getByRole('textbox'), { target: { value: 'See you at 4' } });
        fireEvent.click(screen.getByRole('button', { name: en.mainChat.replyDraft.send }));
        expect(send).toHaveBeenCalledWith('See you at 4');
        await waitFor(() => expect(screen.queryByTestId('main-chat-side-panel')).toBeNull());
    });

    it('keeps the reply draft open when Send cannot start', async () => {
        const send = vi.fn(async () => false);
        render(<MainChatPageColumn enabled><div>chat</div></MainChatPageColumn>);
        act(() => openMainChatReplyDraft('See you at 3', send));
        fireEvent.click(screen.getByRole('button', { name: en.mainChat.replyDraft.send }));
        await waitFor(() => expect(send).toHaveBeenCalledWith('See you at 3'));
        expect(screen.getByTestId('main-chat-reply-draft')).toHaveTextContent('See you at 3');
        act(() => closeMainChatSidePanel());
    });

    it("the running-task chip opens the task's thread in the panel, and Chat stays", () => {
        render(<MainChatPageColumn enabled><div>chat</div></MainChatPageColumn>);
        act(() => openMainChatTask('task-7'));
        expect(screen.getByTestId('task-thread')).toHaveTextContent('task-7');
        expect(screen.getByText('chat')).toBeInTheDocument();
    });

    it('restores choices when posting an answer fails', async () => {
        const view: MainChatStreamView = { today: '2026-10-31', hideRoutine: false, topicNames: new Map(), chips: { q: ['Yes, today', 'Not now'] }, onFillChip: async () => false, chipsSend: true };
        render(<MainChatViewContext.Provider value={view}><MainChatAnswerChips itemId="q" /></MainChatViewContext.Provider>);
        fireEvent.click(screen.getByRole('button', { name: 'Yes, today' }));
        expect(await screen.findByRole('button', { name: 'Not now' })).toBeInTheDocument();
    });

});
