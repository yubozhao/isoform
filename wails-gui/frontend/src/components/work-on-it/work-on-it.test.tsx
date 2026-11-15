import { beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import i18n from '@/i18n';
import { resetWorkOnItEnabled, WorkOnItButton } from './work-on-it';

const mocks = vi.hoisted(() => ({
    enabled: vi.fn(),
    prepare: vi.fn(),
    send: vi.fn(),
    setHandle: vi.fn(),
}));

vi.mock('@/lib/wails-shim-app', () => ({
    GetWorkOnItEnabled: mocks.enabled,
    PrepareWorkOnIt: mocks.prepare,
    SendWorkOnIt: mocks.send,
    SetWorkOnItHandle: mocks.setHandle,
}));

const input = { sourceKind: 'brief_item' as const, sourceId: 'b1', title: 'Draft the launch note', detail: 'For Thursday.', done: 'One page' };
const text = 'Draft the launch note\n\nFor Thursday.\n\nDone looks like: One page';

beforeEach(async () => {
    vi.resetAllMocks();
    resetWorkOnItEnabled();
    await i18n.changeLanguage('en');
});

describe('Work on it', () => {
    it('shows nothing while the switch is off', async () => {
        mocks.enabled.mockResolvedValue(false);
        render(<WorkOnItButton input={input} />);
        await waitFor(() => expect(mocks.enabled).toHaveBeenCalled());
        expect(screen.queryByRole('button', { name: 'Work on it' })).toBeNull();
    });

    it('shows exactly what goes, and sends it to the default agent only on the tap', async () => {
        mocks.enabled.mockResolvedValue(true);
        mocks.prepare.mockResolvedValue({
            taskId: 't1', text,
            agents: [
                { id: 'claude', name: 'Claude', methods: ['deep_link', 'open_copy'], default: true },
                { id: 'chatgpt', name: 'ChatGPT', methods: ['open_copy'], default: false },
            ],
        });
        mocks.send.mockResolvedValue({ recordId: 'r1', method: 'deep_link', copied: false });
        render(<WorkOnItButton input={input} />);
        fireEvent.click(await screen.findByRole('button', { name: 'Work on it' }));
        expect(mocks.prepare).toHaveBeenCalledWith(input);
        const shown = await screen.findByText((_, node) => node?.getAttribute('data-work-on-it-text') !== null && node?.textContent === text);
        expect(shown).toBeInTheDocument();
        expect(screen.getByText('This is exactly what goes to Claude:')).toBeInTheDocument();
        expect(mocks.send).not.toHaveBeenCalled();
        fireEvent.click(screen.getByRole('button', { name: 'Send to Claude' }));
        await waitFor(() => expect(mocks.send).toHaveBeenCalledWith('t1', 'claude', 'deep_link'));
        expect(await screen.findByText('Opened in Claude. Review it there and send.')).toBeInTheDocument();
    });

    it('sends a task link first when the agent can take one, and says the result comes back', async () => {
        mocks.enabled.mockResolvedValue(true);
        mocks.prepare.mockResolvedValue({
            taskId: 't1', text,
            agents: [{ id: 'chatgpt', name: 'ChatGPT', methods: ['task_link', 'open_copy'], default: true }],
        });
        mocks.send.mockResolvedValue({ recordId: 'r1', method: 'task_link', copied: true, via: 'open_copy' });
        render(<WorkOnItButton input={input} />);
        fireEvent.click(await screen.findByRole('button', { name: 'Work on it' }));
        fireEvent.click(await screen.findByRole('button', { name: 'Send to ChatGPT' }));
        await waitFor(() => expect(mocks.send).toHaveBeenCalledWith('t1', 'chatgpt', 'task_link'));
        expect(await screen.findByText('Copied a one-time link to this task. Paste it in ChatGPT and send, and its result comes back here.')).toBeInTheDocument();
    });

    it('shows the terms note and asks for the number before a text agent can be used', async () => {
        mocks.enabled.mockResolvedValue(true);
        mocks.prepare.mockResolvedValue({
            taskId: 't2', text,
            agents: [{ id: 'instinct', name: 'Instinct', methods: [], default: false, needsHandle: true, termsNote: 'workOnIt.terms.instinct' }],
        });
        mocks.setHandle.mockResolvedValue(undefined);
        render(<WorkOnItButton input={input} />);
        fireEvent.click(await screen.findByRole('button', { name: 'Work on it' }));
        expect(await screen.findByText("Instinct's terms allow it to train on what it receives.")).toBeInTheDocument();
        expect(screen.getByRole('button', { name: 'Send to Instinct' })).toBeDisabled();
        fireEvent.change(screen.getByLabelText("Instinct's phone number or Apple ID"), { target: { value: '+15550100' } });
        fireEvent.click(screen.getByRole('button', { name: 'Save number' }));
        await waitFor(() => expect(mocks.setHandle).toHaveBeenCalledWith('instinct', '+15550100'));
    });

    it('says plainly that nothing is sent under Closed Eyes', async () => {
        mocks.enabled.mockResolvedValue(true);
        mocks.prepare.mockRejectedValue(new Error('closed eyes is on'));
        render(<WorkOnItButton input={input} />);
        fireEvent.click(await screen.findByRole('button', { name: 'Work on it' }));
        expect(await screen.findByText('Closed Eyes is on, so nothing is sent.')).toBeInTheDocument();
        expect(mocks.send).not.toHaveBeenCalled();
    });
});
