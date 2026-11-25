import { act, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import i18n from '@/i18n';
import type { CronJobState } from '@/hooks/useCronJobs';
import { AskAboutRun, AskForChange } from './ScheduleAsk';
import { AutomationDetailPane } from './AutomationDetailPane';

const mocks = vi.hoisted(() => ({
    send: vi.fn(), ask: vi.fn(), page: vi.fn(), main: vi.fn(),
    events: new Map<string, (payload?: unknown) => void>(),
}));
vi.mock('@/lib/wails-shim-app', () => ({
    IdeaMeaningGroups: () => Promise.resolve(null),
    SendMainChatWithContext: mocks.send, AskMainChatWithContext: mocks.ask, LoadStreamItemsPage: mocks.page, GetMainChat: mocks.main,
}));
vi.mock('../../../wailsjs/runtime/runtime', () => ({
    EventsOn: vi.fn((name: string, fn: (payload?: unknown) => void) => { mocks.events.set(name, fn); return () => mocks.events.delete(name); }),
}));
vi.mock('@/components/markdown-content', () => ({ MarkdownContent: ({ content }: { content: string }) => <div>{content}</div> }));
vi.mock('./CronJobDetail', () => ({ CronJobDetail: () => <div data-testid="cron-detail" /> }));

const job = (expr: string) => ({
    id: 'weekly', name: 'Weekly update', description: '', enabled: true,
    scheduleKind: 'cron', scheduleConfig: { expr }, scheduleDescription: '',
    targetKind: 'agent', targetConfig: { prompt: 'Send the update' },
    notifyDm: false, notifyPlatform: 'none', sourceSessionId: '', surfaceKind: '',
} as unknown as CronJobState);
const describeJob = (j: CronJobState) => `cron ${(j.scheduleConfig as { expr: string }).expr}`;
const runEnds = () => act(() => { mocks.events.get('agent:event')?.({ type: 'done', tag: 'main' }); });

beforeEach(async () => {
    await i18n.changeLanguage('en');
    mocks.main.mockReset().mockResolvedValue({ session: { localId: 'main' } });
    mocks.send.mockReset().mockResolvedValue({ sessionId: 'main', userItemId: 'u1' });
    mocks.ask.mockReset().mockResolvedValue({ sessionId: 'main', userItemId: 'q1' });
    mocks.page.mockReset().mockResolvedValue({ items: [] });
});
afterEach(async () => { await i18n.changeLanguage('en'); });

describe('Ask for a change (K7, C10)', () => {
    it('sends the words through the main chat for this schedule and says Changed · Undo in place', async () => {
        const onUpdate = vi.fn(async () => undefined);
        const onClose = vi.fn();
        const { rerender } = render(<AskForChange job={job('0 16 * * 5')} describe={describeJob} onUpdate={onUpdate} onClose={onClose} />);
        fireEvent.change(screen.getByRole('textbox', { name: 'What should change?' }), { target: { value: 'Run it at 9 AM on weekdays instead' } });
        fireEvent.click(screen.getByRole('button', { name: 'Change it' }));
        await waitFor(() => expect(mocks.send).toHaveBeenCalledWith('Run it at 9 AM on weekdays instead', { sourceKind: 'schedule', sourceId: 'weekly' }));
        expect(mocks.ask).not.toHaveBeenCalled();
        rerender(<AskForChange job={job('0 9 * * 1-5')} describe={describeJob} onUpdate={onUpdate} onClose={onClose} />);
        expect(await screen.findByRole('status')).toHaveTextContent('Changed: cron 0 9 * * 1-5 · Undo');
        fireEvent.click(screen.getByRole('button', { name: 'Undo' }));
        await waitFor(() => expect(onUpdate).toHaveBeenCalledWith('weekly', { scheduleKind: 'cron', scheduleConfig: { expr: '0 16 * * 5' } }));
        expect(onClose).toHaveBeenCalled();
    });

    it('says the failure in place when the turn ends with nothing changed, keeping the words', async () => {
        vi.useFakeTimers({ shouldAdvanceTime: true });
        try {
            render(<AskForChange job={job('0 16 * * 5')} describe={describeJob} onUpdate={vi.fn()} onClose={vi.fn()} />);
            fireEvent.change(screen.getByRole('textbox', { name: 'What should change?' }), { target: { value: 'Make it monthly' } });
            fireEvent.click(screen.getByRole('button', { name: 'Change it' }));
            await waitFor(() => expect(mocks.send).toHaveBeenCalled());
            await waitFor(() => expect(mocks.events.get('agent:event')).toBeDefined());
            await waitFor(() => expect(screen.getByRole('textbox', { name: 'What should change?' })).toBeDisabled());
            runEnds();
            await waitFor(() => expect(mocks.page).toHaveBeenCalledWith('main', -1, 50));
            await act(async () => { await vi.advanceTimersByTimeAsync(2000); });
            expect(screen.getByRole('alert')).toHaveTextContent("Couldn't change it. Try again");
            expect(screen.getByRole('textbox', { name: 'What should change?' })).toHaveValue('Make it monthly');
        } finally {
            vi.useRealTimers();
        }
    });

    it('says a send that fails in place', async () => {
        mocks.send.mockRejectedValue(new Error('offline'));
        render(<AskForChange job={job('0 16 * * 5')} describe={describeJob} onUpdate={vi.fn()} onClose={vi.fn()} />);
        fireEvent.change(screen.getByRole('textbox', { name: 'What should change?' }), { target: { value: 'Later' } });
        fireEvent.click(screen.getByRole('button', { name: 'Change it' }));
        expect(await screen.findByRole('alert')).toHaveTextContent("Couldn't change it. Try again");
    });

    it('is the detail header action, in place of Edit in Chat, and Tasks stays', async () => {
        render(<AutomationDetailPane job={job('0 16 * * 5')} onClose={vi.fn()} onToggleEnabled={vi.fn()} onDelete={vi.fn()} onRunNow={vi.fn()}
            onUpdate={vi.fn(async () => undefined)} loadRuns={vi.fn(async () => [])} loadRunLogs={vi.fn(async () => [])} />);
        fireEvent.click(screen.getByRole('button', { name: 'Ask for a change' }));
        expect(screen.getByTestId('schedule-ask-change')).toBeInTheDocument();
        expect(screen.getByTestId('cron-detail')).toBeInTheDocument();
        expect(screen.queryByText('Edit in Chat')).toBeNull();
    });

    it('says it in zh-CN', async () => {
        await i18n.changeLanguage('zh-CN');
        const { rerender } = render(<AskForChange job={job('0 16 * * 5')} describe={describeJob} onUpdate={vi.fn()} onClose={vi.fn()} />);
        fireEvent.change(screen.getByRole('textbox', { name: '要改什么？' }), { target: { value: '改到工作日上午 9 点' } });
        fireEvent.click(screen.getByRole('button', { name: '修改' }));
        await waitFor(() => expect(mocks.send).toHaveBeenCalled());
        rerender(<AskForChange job={job('0 9 * * 1-5')} describe={describeJob} onUpdate={vi.fn()} onClose={vi.fn()} />);
        expect(await screen.findByRole('status')).toHaveTextContent('已改为：cron 0 9 * * 1-5 · 撤销');
    });
});

describe('Ask about this run (K7, C16)', () => {
    it('answers under the run, with Open in Chat and Ask a follow-up', async () => {
        const onOpenChatMessage = vi.fn();
        mocks.page.mockResolvedValue({ items: [
            { id: 'old', kind: 'assistant', content: 'Earlier answer' },
            { id: 'q1', kind: 'user', content: 'Did Lena get it?' },
            { id: 'a1', kind: 'assistant', content: 'No. She joined the team list on Monday, after this run.' },
        ] });
        render(<AskAboutRun run={{ id: 'run-23' }} onOpenChatMessage={onOpenChatMessage} />);
        fireEvent.click(screen.getByRole('button', { name: 'Ask about this run' }));
        fireEvent.change(screen.getByRole('textbox', { name: 'Ask about this run' }), { target: { value: 'Did Lena get it?' } });
        fireEvent.submit(screen.getByRole('textbox', { name: 'Ask about this run' }));
        await waitFor(() => expect(mocks.ask).toHaveBeenCalledWith('Did Lena get it?', { sourceKind: 'schedule_run', sourceId: 'run-23' }));
        expect(mocks.send).not.toHaveBeenCalled();
        runEnds();
        expect(await screen.findByTestId('schedule-run-answer')).toHaveTextContent('No. She joined the team list on Monday, after this run.');
        expect(screen.getByText('Did Lena get it?')).toBeInTheDocument();
        expect(screen.queryByText('Earlier answer')).toBeNull();
        fireEvent.click(screen.getByRole('button', { name: 'Open in Chat' }));
        expect(onOpenChatMessage).toHaveBeenCalledWith('main', 'a1');
        expect(screen.getByRole('textbox', { name: 'Ask a follow-up' })).toBeInTheDocument();
    });

    it('says a failed ask in place', async () => {
        mocks.ask.mockRejectedValue(new Error('offline'));
        render(<AskAboutRun run={{ id: 'run-23' }} />);
        fireEvent.click(screen.getByRole('button', { name: 'Ask about this run' }));
        fireEvent.change(screen.getByRole('textbox', { name: 'Ask about this run' }), { target: { value: 'Why?' } });
        fireEvent.submit(screen.getByRole('textbox', { name: 'Ask about this run' }));
        expect(await screen.findByRole('alert')).toHaveTextContent("Didn't send. Try again.");
    });
});

describe('while Chat is off (scope cut 2026-11)', () => {
    const key = '__isoformFeatureSwitchesForTest';
    let saved: unknown;
    beforeEach(() => {
        const g = globalThis as Record<string, unknown>;
        saved = g[key];
        g[key] = { ...(saved as object), chat: false };
    });
    afterEach(() => { (globalThis as Record<string, unknown>)[key] = saved; });

    it('offers no Ask for a change and no Ask about this run, and never reaches the main chat', async () => {
        render(<AutomationDetailPane job={job('0 16 * * 5')} onClose={vi.fn()} onToggleEnabled={vi.fn()} onDelete={vi.fn()} onRunNow={vi.fn()}
            onUpdate={vi.fn(async () => undefined)} loadRuns={vi.fn(async () => [])} loadRunLogs={vi.fn(async () => [])} />);
        render(<AskAboutRun run={{ id: 'run-23' }} />);
        expect(screen.getByTestId('cron-detail')).toBeInTheDocument();
        expect(screen.queryByRole('button', { name: 'Ask for a change' })).toBeNull();
        expect(screen.queryByTestId('schedule-ask-for-change')).toBeNull();
        expect(screen.queryByRole('button', { name: 'Ask about this run' })).toBeNull();
        expect(mocks.main).not.toHaveBeenCalled();
    });
});
