import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import i18n from '@/i18n';
import { getMainChatProactive, MAIN_CHAT_PROACTIVE_TOOL } from '@/lib/stream-helpers';
import { FirstReadCard } from './first-read-card';

const answer = vi.hoisted(() => vi.fn());
vi.mock('@/lib/wails-shim-app', () => ({ AnswerFirstRead: answer }));

const item = { kind: 'system', toolName: MAIN_CHAT_PROACTIVE_TOOL, toolInput: {
    momentId: 'first-read:1', kind: 'first_read', headline: 'What I see so far', body: 'You build things.',
    sentences: ['You build things.'], helps: ['Help Sam.', 'Brief at 7:30.'], offerCount: 2,
    actionIds: [], actionLabels: {}, state: 'pending', target: { kind: 'none' }, evidence: [],
} } as const;

beforeEach(async () => { await i18n.changeLanguage('en'); answer.mockReset(); });
afterEach(cleanup);

describe('first read', () => {
    it('preserves the projected fields and confirms, then undoes', async () => {
        const message = getMainChatProactive(item)!;
        expect(message.sentences).toEqual(['You build things.']);
        expect(message.helps).toEqual(['Help Sam.', 'Brief at 7:30.']);
        expect(message.offerCount).toBe(2);
        answer.mockResolvedValueOnce({ state: 'acted', confirmedAt: Date.now() }).mockResolvedValueOnce({ state: 'pending' });
        const open = vi.fn();
        render(<FirstReadCard message={message} onOpenTarget={open} />);
        fireEvent.click(screen.getByRole('button', { name: "That's me" }));
        expect(screen.getByTestId('first-read')).toHaveTextContent("You said that's you");
        await waitFor(() => expect(answer).toHaveBeenCalledWith('first-read:1', 'confirm_portrait', ''));
        fireEvent.click(screen.getByRole('button', { name: 'Undo' }));
        await waitFor(() => expect(screen.getByRole('button', { name: "That's me" })).toBeInTheDocument());
        expect(answer).toHaveBeenCalledWith('first-read:1', 'undo_portrait', '');
        fireEvent.click(screen.getByRole('button', { name: 'See them' }));
        expect(open).toHaveBeenCalledWith({ kind: 'tasks' });
    });
    it('shows the sent words, then the portrait line once the computer has answered', async () => {
        let finish!: (result: { state: string }) => void;
        answer.mockImplementationOnce(() => new Promise((resolve) => { finish = resolve; }));
        render(<FirstReadCard message={getMainChatProactive(item)!} />);
        fireEvent.click(screen.getByRole('button', { name: 'Not quite' }));
        fireEvent.change(screen.getByLabelText("What's off? You can leave it blank."), { target: { value: 'I work later.' } });
        fireEvent.click(screen.getByRole('button', { name: 'Send' }));
        expect(answer).toHaveBeenCalledWith('first-read:1', 'correct_portrait', 'I work later.');
        expect(screen.getByText('I work later.')).toBeInTheDocument();
        expect(screen.getByTestId('first-read')).toHaveTextContent('You said: “I work later.”');
        expect(screen.getByRole('button', { name: 'Undo' })).toBeInTheDocument();
        expect(screen.queryByText('Your portrait in About you starts from your words.')).not.toBeInTheDocument();
        await act(async () => { finish({ state: 'corrected' }); });
        expect(screen.getByText('Your portrait in About you starts from your words.')).toBeInTheDocument();
        expect(screen.getByText('I work later.')).toBeInTheDocument();
    });
    it('keeps the words line break-for-break', async () => {
        answer.mockResolvedValue({ state: 'corrected' });
        render(<FirstReadCard message={getMainChatProactive(item)!} />);
        fireEvent.click(screen.getByRole('button', { name: 'Not quite' }));
        fireEvent.change(screen.getByLabelText("What's off? You can leave it blank."), { target: { value: 'Line one\nLine two' } });
        fireEvent.click(screen.getByRole('button', { name: 'Send' }));
        await waitFor(() => expect(screen.getByText('Your portrait in About you starts from your words.')).toBeInTheDocument());
        expect(screen.getByText(/Line one/).textContent).toBe('Line one\nLine two');
    });
    it('restores the words to the field on Undo', async () => {
        answer.mockResolvedValueOnce({ state: 'corrected' }).mockResolvedValueOnce({ state: 'pending' });
        render(<FirstReadCard message={getMainChatProactive(item)!} />);
        fireEvent.click(screen.getByRole('button', { name: 'Not quite' }));
        fireEvent.change(screen.getByLabelText("What's off? You can leave it blank."), { target: { value: 'I work later.' } });
        fireEvent.click(screen.getByRole('button', { name: 'Send' }));
        await waitFor(() => expect(screen.getByText('Your portrait in About you starts from your words.')).toBeInTheDocument());
        fireEvent.click(screen.getByRole('button', { name: 'Undo' }));
        await waitFor(() => expect(screen.getByLabelText("What's off? You can leave it blank.")).toHaveValue('I work later.'));
        expect(answer).toHaveBeenCalledWith('first-read:1', 'undo_portrait', '');
    });
    it('closes the Undo window 5 s after the computer answers', async () => {
        vi.useFakeTimers();
        try {
            answer.mockResolvedValue({ state: 'corrected' });
            render(<FirstReadCard message={getMainChatProactive(item)!} />);
            fireEvent.click(screen.getByRole('button', { name: 'Not quite' }));
            fireEvent.change(screen.getByLabelText("What's off? You can leave it blank."), { target: { value: 'I work later.' } });
            fireEvent.click(screen.getByRole('button', { name: 'Send' }));
            await act(async () => { await vi.advanceTimersByTimeAsync(0); });
            expect(screen.getByRole('button', { name: 'Undo' })).toBeInTheDocument();
            await act(async () => { await vi.advanceTimersByTimeAsync(5000); });
            expect(screen.queryByRole('button', { name: 'Undo' })).not.toBeInTheDocument();
            expect(screen.getByText('I work later.')).toBeInTheDocument();
        } finally { vi.useRealTimers(); }
    });
    it('keeps the words in the field and offers Try again when the send fails', async () => {
        answer.mockRejectedValueOnce(new Error('unavailable')).mockResolvedValueOnce({ state: 'corrected' });
        render(<FirstReadCard message={getMainChatProactive(item)!} />);
        fireEvent.click(screen.getByRole('button', { name: 'Not quite' }));
        fireEvent.change(screen.getByLabelText("What's off? You can leave it blank."), { target: { value: 'I work later.' } });
        fireEvent.click(screen.getByRole('button', { name: 'Send' }));
        await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent("Couldn't save your answer. Try again"));
        expect(screen.getByLabelText("What's off? You can leave it blank.")).toHaveValue('I work later.');
        fireEvent.click(screen.getByRole('button', { name: 'Try again' }));
        expect(answer).toHaveBeenNthCalledWith(2, 'first-read:1', 'correct_portrait', 'I work later.');
        await waitFor(() => expect(screen.getByText('Your portrait in About you starts from your words.')).toBeInTheDocument());
        expect(screen.queryByRole('alert')).not.toBeInTheDocument();
    });
    it('uses the same save failure line for That\'s me', async () => {
        answer.mockRejectedValueOnce(new Error('unavailable')).mockResolvedValueOnce({ state: 'acted', confirmedAt: Date.now() });
        render(<FirstReadCard message={getMainChatProactive(item)!} />);
        fireEvent.click(screen.getByRole('button', { name: "That's me" }));
        await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent("Couldn't save your answer. Try again"));
        fireEvent.click(screen.getByRole('button', { name: 'Try again' }));
        expect(answer).toHaveBeenNthCalledWith(2, 'first-read:1', 'confirm_portrait', '');
        await waitFor(() => expect(screen.queryByRole('alert')).not.toBeInTheDocument());
    });
    it('shows the blank copy when the sent words are empty', () => {
        render(<FirstReadCard message={getMainChatProactive(item)!} initialCorrection={{ words: '', answeredAt: Date.now() }} />);
        expect(screen.getByTestId('first-read')).toHaveTextContent("You said it's not quite you");
        expect(screen.getByText("Your portrait in About you won't start from this.")).toBeInTheDocument();
        expect(screen.getByRole('button', { name: 'Undo' })).toBeInTheDocument();
    });
    it('shows the Chinese sent copy', async () => {
        await act(async () => { await i18n.changeLanguage('zh-CN'); });
        try {
            render(<FirstReadCard message={getMainChatProactive(item)!} initialCorrection={{ words: '我晚上工作。', answeredAt: Date.now() }} />);
            expect(screen.getByText('我晚上工作。')).toBeInTheDocument();
            expect(screen.getByTestId('first-read')).toHaveTextContent('你说：“我晚上工作。”');
            expect(screen.getByText('“关于你”里的画像会从你的话开始。')).toBeInTheDocument();
            expect(screen.getByRole('button', { name: '撤销' })).toBeInTheDocument();
        } finally { await act(async () => { await i18n.changeLanguage('en'); }); }
    });
    it('retains confirmation and shows an error if a queued Undo fails', async () => {
        let finish!: (result: { state: string; confirmedAt: number }) => void;
        answer.mockImplementationOnce(() => new Promise((resolve) => { finish = resolve; }))
            .mockRejectedValueOnce(new Error('unavailable'));
        render(<FirstReadCard message={getMainChatProactive(item)!} />);
        fireEvent.click(screen.getByRole('button', { name: "That's me" }));
        fireEvent.click(screen.getByRole('button', { name: 'Undo' }));
        await act(async () => { finish({ state: 'acted', confirmedAt: Date.now() }); });
        await waitFor(() => expect(screen.getByRole('alert')).toBeInTheDocument());
        expect(screen.getByTestId('first-read')).toHaveTextContent("You said that's you");
    });
});

describe('first read after a reload', () => {
    it('shows the answer, not the buttons, for a corrected card', () => {
        const corrected = { ...item, toolInput: { ...item.toolInput, state: 'corrected' } };
        render(<FirstReadCard message={getMainChatProactive(corrected)!} />);
        expect(screen.queryByRole('button', { name: 'Not quite' })).not.toBeInTheDocument();
        expect(screen.queryByRole('button', { name: "That's me" })).not.toBeInTheDocument();
        expect(screen.getByText('Your portrait in About you starts from your words.')).toBeInTheDocument();
        expect(screen.queryByRole('button', { name: 'Undo' })).not.toBeInTheDocument();
    });
    it('shows the confirmed answer, not the buttons, for a confirmed card', () => {
        const acted = { ...item, toolInput: { ...item.toolInput, state: 'acted', firstReadConfirmedAt: Date.now() - 60_000 } };
        render(<FirstReadCard message={getMainChatProactive(acted)!} />);
        expect(screen.queryByRole('button', { name: "That's me" })).not.toBeInTheDocument();
        expect(screen.getByTestId('first-read')).toHaveTextContent("You said that's you");
        expect(screen.queryByRole('button', { name: 'Undo' })).not.toBeInTheDocument();
    });
    it('takes back a correction within the window and puts the words back in the field', async () => {
        answer.mockResolvedValueOnce({ state: 'corrected' }).mockResolvedValueOnce({ state: 'pending' });
        render(<FirstReadCard message={getMainChatProactive(item)!} />);
        fireEvent.click(screen.getByRole('button', { name: 'Not quite' }));
        fireEvent.change(screen.getByLabelText("What's off? You can leave it blank."), { target: { value: 'I work later.' } });
        fireEvent.click(screen.getByRole('button', { name: 'Send' }));
        await waitFor(() => expect(screen.getByText('Your portrait in About you starts from your words.')).toBeInTheDocument());
        fireEvent.click(screen.getByRole('button', { name: 'Undo' }));
        await waitFor(() => expect(screen.getByLabelText("What's off? You can leave it blank.")).toHaveValue('I work later.'));
        expect(answer).toHaveBeenLastCalledWith('first-read:1', 'undo_portrait', '');
    });
});

describe('first read blank Not quite', () => {
    it('sends an empty answer through Send and shows the blank state', async () => {
        answer.mockResolvedValueOnce({ state: 'corrected' });
        render(<FirstReadCard message={getMainChatProactive(item)!} />);
        fireEvent.click(screen.getByRole('button', { name: 'Not quite' }));
        const send = screen.getByRole('button', { name: 'Send' });
        expect(send).toBeEnabled();
        fireEvent.click(send);
        await waitFor(() => expect(answer).toHaveBeenCalledWith('first-read:1', 'correct_portrait', ''));
        expect(screen.getByTestId('first-read')).toHaveTextContent("You said it's not quite you");
        expect(screen.getByText("Your portrait in About you won't start from this.")).toBeInTheDocument();
    });
});
