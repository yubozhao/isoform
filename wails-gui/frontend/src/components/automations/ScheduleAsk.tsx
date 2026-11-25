import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/button';
import { AnimatedEllipsis } from '@/components/animated-ellipsis';
import { MarkdownContent } from '@/components/markdown-content';
import { AskMainChatWithContext, GetMainChat, LoadStreamItemsPage, SendMainChatWithContext } from '@/lib/wails-shim-app';
import type { TodayChatContextRef } from '@/lib/today-chat-navigation';
import type { CronJobState, CronRunState } from '@/hooks/useCronJobs';
import { EventsOn } from '../../../wailsjs/runtime/runtime';
import { extractAgentBlocks } from '@/lib/agent-deliverables';
import { useFeatureSwitches } from '@/hooks/useFeatureSwitches';

/** How long a schedule's detail waits for the main chat before it reads again. */
const EXCHANGE_WAIT_MS = 180_000;
const ENDS_A_RUN = new Set(['done', 'error', 'close']);

export type ExchangePhase = 'idle' | 'waiting' | 'answered' | 'failed';

export interface MainChatExchange {
    phase: ExchangePhase;
    question: string;
    sessionId: string;
    userItemId: string;
    answer: string;
    answerItemId: string;
}

const IDLE: MainChatExchange = { phase: 'idle', question: '', sessionId: '', userItemId: '', answer: '', answerItemId: '' };

interface PageItem { id?: string; kind?: string; content?: string }

/** One exchange with the main chat from a schedule's detail (the click rule's
 *  Ask and Do routes): the words go to the main chat with the schedule or run
 *  as their item, so the exchange is saved in Chat, and the reply is read back
 *  to show where it was asked. */
export function useMainChatExchange() {
    const [state, setState] = useState<MainChatExchange>(IDLE);
    const pending = useRef<{ tag: string; userItemId: string; ended: boolean; generation: number } | null>(null);
    const generation = useRef(0);

    const finish = useCallback(async () => {
        const current = pending.current;
        if (!current || !current.userItemId) return;
        pending.current = null;
        try {
            const page = await LoadStreamItemsPage(current.tag, -1, 50) as unknown as { items?: PageItem[] | null };
            if (current.generation !== generation.current) return;
            const items = page?.items ?? [];
            const start = items.findIndex((item) => item.id === current.userItemId);
            const replies: PageItem[] = [];
            for (const item of start >= 0 ? items.slice(start + 1) : []) {
                if (item.kind === 'user') break;
                if (item.kind === 'assistant' && item.content?.trim()) replies.push(item);
            }
            const answer = replies.map((item) => extractAgentBlocks(item.content ?? '').content.trim()).filter(Boolean).join('\n\n');
            setState((s) => ({ ...s, phase: answer ? 'answered' : 'failed', answer, answerItemId: replies[0]?.id ?? '' }));
        } catch {
            if (current.generation === generation.current) setState((s) => ({ ...s, phase: 'failed' }));
        }
    }, []);

    useEffect(() => EventsOn('agent:event', (event: { type?: string; tag?: string } | null) => {
        const current = pending.current;
        if (!current || !event?.type || event.tag !== current.tag || !ENDS_A_RUN.has(event.type)) return;
        current.ended = true;
        void finish();
    }), [finish]);

    useEffect(() => {
        if (state.phase !== 'waiting') return;
        const timer = window.setTimeout(() => {
            if (pending.current) pending.current.ended = true;
            void finish();
        }, EXCHANGE_WAIT_MS);
        return () => window.clearTimeout(timer);
    }, [state.phase, finish]);

    const send = useCallback(async (text: string, ref: TodayChatContextRef, ask: boolean): Promise<boolean> => {
        const words = text.trim();
        if (!words) return false;
        const mine = ++generation.current;
        setState({ ...IDLE, phase: 'waiting', question: words });
        try {
            const info = await GetMainChat() as unknown as { session?: { localId?: string } };
            const tag = info?.session?.localId ?? '';
            if (!tag) throw new Error('MAIN_CHAT_UNAVAILABLE');
            pending.current = { tag, userItemId: '', ended: false, generation: mine };
            const sent = await (ask ? AskMainChatWithContext(words, ref) : SendMainChatWithContext(words, ref)) as unknown as { sessionId?: string; userItemId?: string };
            if (mine !== generation.current) return false;
            const userItemId = sent?.userItemId ?? '';
            if (!userItemId) throw new Error('NOT_SAVED');
            setState((s) => ({ ...s, sessionId: sent?.sessionId || tag, userItemId }));
            if (pending.current) {
                pending.current.userItemId = userItemId;
                if (pending.current.ended) void finish();
            }
            return true;
        } catch {
            if (mine === generation.current) {
                pending.current = null;
                setState((s) => ({ ...s, phase: 'failed' }));
            }
            return false;
        }
    }, [finish]);

    const reset = useCallback(() => {
        generation.current++;
        pending.current = null;
        setState(IDLE);
    }, []);

    return { ...state, send, reset };
}

type ScheduleShape = Pick<CronJobState, 'scheduleKind' | 'scheduleConfig'>;
const scheduleKey = (job: ScheduleShape) => JSON.stringify([job.scheduleKind, job.scheduleConfig ?? null]);

/** Ask for a change (C10), in place: "What should change?" and Change it.
 *  Isoform makes the change through the main chat, so the exchange is saved
 *  in Chat, and the detail says "Changed: {new schedule} · Undo". */
export function AskForChange({ job, describe, onUpdate, onClose }: {
    job: CronJobState;
    /** The schedule in words, as the detail's header says it. */
    describe: (job: CronJobState) => string;
    onUpdate: (id: string, patch: Record<string, unknown>) => Promise<void>;
    onClose: () => void;
}) {
    const { t } = useTranslation('app');
    const exchange = useMainChatExchange();
    const [text, setText] = useState('');
    const [changed, setChanged] = useState<{ before: ScheduleShape } | null>(null);
    const [failed, setFailed] = useState(false);
    const [undoing, setUndoing] = useState(false);
    const before = useRef<(ScheduleShape & { key: string }) | null>(null);
    const key = scheduleKey(job);

    useEffect(() => {
        const was = before.current;
        if (!was || key === was.key) return;
        before.current = null;
        setChanged({ before: { scheduleKind: was.scheduleKind, scheduleConfig: was.scheduleConfig } });
        setText('');
    }, [key]);

    useEffect(() => {
        if (exchange.phase !== 'answered' && exchange.phase !== 'failed') return;
        if (!before.current) return;
        // The schedule's own update can land just after the turn ends.
        const timer = window.setTimeout(() => {
            if (!before.current) return;
            before.current = null;
            setFailed(true);
        }, 1500);
        return () => window.clearTimeout(timer);
    }, [exchange.phase]);

    const submit = async () => {
        const words = text.trim();
        if (!words || before.current) return;
        before.current = { scheduleKind: job.scheduleKind, scheduleConfig: job.scheduleConfig, key };
        setFailed(false);
        setChanged(null);
        const sent = await exchange.send(words, { sourceKind: 'schedule', sourceId: job.id }, false);
        if (!sent && before.current) {
            before.current = null;
            setFailed(true);
        }
    };

    const undo = async () => {
        if (!changed || undoing) return;
        setUndoing(true);
        try {
            await onUpdate(job.id, { scheduleKind: changed.before.scheduleKind, scheduleConfig: changed.before.scheduleConfig });
            setChanged(null);
            onClose();
        } catch {
            setFailed(true);
        } finally {
            setUndoing(false);
        }
    };

    const working = !!before.current && exchange.phase === 'waiting';
    return (
        <div data-testid="schedule-ask-change" className="border-b border-border px-6 py-3">
            {changed ? (
                <p role="status" className="text-[13px] text-accent">
                    {t('scheduleAsk.changed', { schedule: describe(job) })}{' · '}
                    <Button variant="link" size="sm" className="h-auto p-0 text-[13px] font-medium hover:no-underline" disabled={undoing} onClick={() => void undo()}>{t('scheduleAsk.undo')}</Button>
                </p>
            ) : (
                <form className="flex items-center gap-2" onSubmit={(event) => { event.preventDefault(); void submit(); }}>
                    <input autoFocus value={text} onChange={(event) => setText(event.target.value)} disabled={working}
                        aria-label={t('scheduleAsk.whatShouldChange')} placeholder={t('scheduleAsk.whatShouldChange')}
                        className="h-8 min-w-0 flex-1 rounded-lg border border-border bg-panel px-3 text-[13px] text-ink outline-none placeholder:text-muted focus:border-border-emphasis" />
                    <Button type="submit" size="sm" disabled={!text.trim() || working}>{working ? <AnimatedEllipsis /> : t('scheduleAsk.changeIt')}</Button>
                </form>
            )}
            {failed && <p role="alert" className="mt-2 text-[12px] text-error">{t('scheduleAsk.changeFailed')}</p>}
        </div>
    );
}

/** Ask about this run (C16): the question and Isoform's answer show under the
 *  run, with Open in Chat and Ask a follow-up. */
export function AskAboutRun({ run, onOpenChatMessage }: {
    run: Pick<CronRunState, 'id'>;
    onOpenChatMessage?: (sessionId: string, itemId: string) => void;
}) {
    const { t } = useTranslation('app');
    const { t: tToday } = useTranslation('today');
    const exchange = useMainChatExchange();
    const [open, setOpen] = useState(false);
    const [text, setText] = useState('');
    const [history, setHistory] = useState<MainChatExchange[]>([]);
    const ref = useMemo<TodayChatContextRef>(() => ({ sourceKind: 'schedule_run', sourceId: run.id }), [run.id]);
    const chatOn = useFeatureSwitches().chat;

    useEffect(() => {
        if (exchange.phase !== 'answered') return;
        setHistory((rows) => rows.some((row) => row.userItemId === exchange.userItemId) ? rows : [...rows, exchange]);
        // Keep the shown answer and wait for the next question.
        // eslint-disable-next-line react-hooks/exhaustive-deps
    }, [exchange.phase, exchange.userItemId]);

    const ask = async () => {
        const words = text.trim();
        if (!words || exchange.phase === 'waiting') return;
        if (await exchange.send(words, ref, true)) setText('');
    };

    // The answer comes from the main chat: no ask while Chat is off (scope cut 2026-11).
    if (!chatOn) return null;
    if (!open && history.length === 0) {
        return (
            <div className="flex justify-end">
                <Button variant="quiet" size="sm" className="text-muted" onClick={() => setOpen(true)}>{t('scheduleAsk.askAboutRun')}</Button>
            </div>
        );
    }
    const asking = exchange.phase === 'waiting' ? exchange.question : '';
    return (
        <div data-testid="schedule-run-ask" className="space-y-2.5">
            {history.map((row) => (
                <div key={row.userItemId} className="space-y-2">
                    <p className="ml-auto w-fit max-w-[85%] rounded-2xl bg-accent px-3.5 py-2 text-[13px] text-solid-btn-fg">{row.question}</p>
                    <div className="rounded-xl border border-border bg-panel px-3.5 py-3 text-[13px] text-ink" data-testid="schedule-run-answer">
                        <MarkdownContent content={row.answer} />
                        {onOpenChatMessage && <Button variant="link" size="sm" className="mt-2 h-auto p-0 text-[13px] font-medium hover:no-underline" onClick={() => onOpenChatMessage(row.sessionId, row.answerItemId || row.userItemId)}>{tToday('mainChatPanel.openInChat')}</Button>}
                    </div>
                </div>
            ))}
            {asking && (
                <div className="space-y-2">
                    <p className="ml-auto w-fit max-w-[85%] rounded-2xl bg-accent px-3.5 py-2 text-[13px] text-solid-btn-fg">{asking}</p>
                    <p role="status" className="text-[13px] text-muted"><AnimatedEllipsis /></p>
                </div>
            )}
            <form onSubmit={(event) => { event.preventDefault(); void ask(); }}>
                <input autoFocus value={text} onChange={(event) => setText(event.target.value)} disabled={exchange.phase === 'waiting'}
                    aria-label={history.length > 0 ? tToday('mainChatPanel.askFollowUp') : t('scheduleAsk.askAboutRun')}
                    placeholder={history.length > 0 ? tToday('mainChatPanel.askFollowUp') : t('scheduleAsk.askAboutRun')}
                    className="h-9 w-full rounded-lg border border-border bg-panel px-3 text-[13px] text-ink outline-none placeholder:text-muted focus:border-border-emphasis" />
            </form>
            {exchange.phase === 'failed' && <p role="alert" className="text-[12px] text-error">{t('scheduleAsk.askFailed')}</p>}
        </div>
    );
}
