import { useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/button';
import { AnswerFirstRead } from '@/lib/wails-shim-app';
import type { MainChatProactivePayload, MainChatReportTarget } from '@/lib/stream-helpers';

type FailedAction = 'confirm' | 'undo' | 'restore' | 'correct';
// The words sent with Not quite. answeredAt stays 0 until the computer has answered.
// words is null for a corrected card reloaded from the computer: the answer is known, the words are in the chat above.
export type FirstReadCorrection = { words: string | null; answeredAt: number };

export function FirstReadCard({ message, onOpenTarget, initialCorrection }: { message: MainChatProactivePayload; onOpenTarget?: (target: MainChatReportTarget) => void; initialCorrection?: FirstReadCorrection }) {
    const { t, i18n } = useTranslation('chat');
    const [confirmed, setConfirmed] = useState(message.state === 'acted');
    const [confirmedAt, setConfirmedAt] = useState(message.firstReadConfirmedAt ?? 0);
    const [undoVisible, setUndoVisible] = useState(false);
    const [correcting, setCorrecting] = useState(false);
    const [text, setText] = useState('');
    const [busy, setBusy] = useState(false);
    const [failedAction, setFailedAction] = useState<FailedAction | null>(null);
    const [correction, setCorrection] = useState<FirstReadCorrection | null>(initialCorrection ?? (message.state === 'corrected' ? { words: null, answeredAt: 0 } : null));
    const [correctionUndoOpen, setCorrectionUndoOpen] = useState(false);
    const undoRequested = useRef(false);
    const language = i18n.resolvedLanguage?.startsWith('zh') ? 'zh-CN' : 'en';
    const helps = message.localized?.[language]?.helps ?? message.helps ?? [];
    const correctionAnsweredAt = correction?.answeredAt ?? 0;
    const correctionUndoShown = correction !== null && correction.words !== null && (correctionAnsweredAt === 0 || correctionUndoOpen);

    useEffect(() => {
        setConfirmed(message.state === 'acted');
        setConfirmedAt(message.firstReadConfirmedAt ?? 0);
    }, [message.momentId, message.state, message.firstReadConfirmedAt]);
    useEffect(() => {
        const remaining = confirmedAt + 5000 - Date.now();
        setUndoVisible(confirmed && remaining > 0);
        if (!confirmed || remaining <= 0) return;
        const timer = window.setTimeout(() => setUndoVisible(false), remaining);
        return () => window.clearTimeout(timer);
    }, [confirmed, confirmedAt]);
    useEffect(() => {
        // The computer's state decides whether the answer shows; a local correction keeps its own words.
        if (message.state === 'corrected') setCorrection((previous) => previous ?? { words: null, answeredAt: 0 });
    }, [message.momentId, message.state]);
    useEffect(() => {
        if (!correctionAnsweredAt) return;
        const remaining = correctionAnsweredAt + 5000 - Date.now();
        setCorrectionUndoOpen(remaining > 0);
        if (remaining <= 0) return;
        const timer = window.setTimeout(() => setCorrectionUndoOpen(false), remaining);
        return () => window.clearTimeout(timer);
    }, [correctionAnsweredAt]);

    async function confirm() {
        setBusy(true); setFailedAction(null); undoRequested.current = false;
        setConfirmed(true); setConfirmedAt(Date.now());
        let recordedAt = 0;
        try {
            const result = await AnswerFirstRead(message.momentId, 'confirm_portrait', '');
            recordedAt = result.confirmedAt ?? Date.now();
            if (undoRequested.current) {
                try {
                    await AnswerFirstRead(message.momentId, 'undo_portrait', '');
                    setConfirmed(false); setConfirmedAt(0);
                } catch { setConfirmed(true); setConfirmedAt(recordedAt); setFailedAction('undo'); }
            } else setConfirmedAt(recordedAt);
        } catch {
            setConfirmed(recordedAt > 0); setConfirmedAt(recordedAt); setFailedAction('confirm');
        } finally { setBusy(false); }
    }
    async function undo() {
        if (busy) { undoRequested.current = true; setConfirmed(false); return; }
        setBusy(true); setFailedAction(null);
        try {
            await AnswerFirstRead(message.momentId, 'undo_portrait', '');
            setConfirmed(false); setConfirmedAt(0);
        } catch { setFailedAction('undo'); }
        finally { setBusy(false); }
    }
    async function correct() {
        // A blank Not quite is a valid answer (P163 v1.0.1): the computer saves nothing.
        const words = text.trim();
        undoRequested.current = false;
        setBusy(true); setFailedAction(null); setCorrecting(false); setCorrection({ words, answeredAt: 0 });
        try {
            await AnswerFirstRead(message.momentId, 'correct_portrait', words);
            if (undoRequested.current) {
                try { await AnswerFirstRead(message.momentId, 'undo_portrait', ''); }
                catch { setFailedAction('undo'); }
            } else setCorrection({ words, answeredAt: Date.now() });
        } catch {
            setCorrection(null); setCorrecting(true); setText(words); setFailedAction('correct');
        } finally { setBusy(false); }
    }
    async function restoreCorrection(words: string) {
        setBusy(true); setFailedAction(null);
        try {
            await AnswerFirstRead(message.momentId, 'undo_portrait', '');
            setCorrection(null); setCorrecting(true); setText(words);
        } catch { setFailedAction('restore'); }
        finally { setBusy(false); }
    }
    function undoCorrection() {
        if (!correction || correction.words === null) return;
        if (busy) {
            // The answer is still in flight: show the field now and undo once the computer answers.
            undoRequested.current = true;
            setCorrection(null); setCorrecting(true); setText(correction.words);
            return;
        }
        void restoreCorrection(correction.words);
    }
    function retry() {
        if (failedAction === 'confirm') void confirm();
        else if (failedAction === 'undo') void undo();
        else if (failedAction === 'restore' && correction && correction.words !== null) void restoreCorrection(correction.words);
        else if (failedAction === 'correct') void correct();
    }
    if (!message.sentences?.length) return null;
    return (
        <div className="flex flex-col gap-3" data-testid="first-read">
            <section className="rounded-xl border border-border bg-panel p-4">
                <p className="text-xs font-semibold uppercase text-muted">{t('firstRead.label')}</p>
                <p className="mt-2 text-[15px] leading-relaxed text-ink">{message.sentences.join(' ')}</p>
                {confirmed ? <div className="mt-3">
                    <p className="text-sm font-medium text-accent">{t('firstRead.confirmed')}{undoVisible && <> · <button type="button" onClick={() => void undo()}>{t('firstRead.undo')}</button></>}</p>
                    <p className="mt-1 text-sm text-muted">{t('firstRead.portrait')}</p>
                </div> : correction ? <div className="mt-3">
                    {correction.words !== null && <p className="text-sm font-medium text-accent">{correction.words
                        ? <>{t('firstRead.saidBefore')}<span className="whitespace-pre-wrap">{correction.words}</span>{t('firstRead.saidAfter')}</>
                        : t('firstRead.saidBlank')}{correctionUndoShown && <> · <button type="button" onClick={undoCorrection}>{t('firstRead.undo')}</button></>}</p>}
                    {(correction.words === null || correctionAnsweredAt > 0) && <p className="mt-1 text-sm text-muted">{correction.words === '' ? t('firstRead.saidBlankPortrait') : t('firstRead.saidPortrait')}</p>}
                </div> : correcting ? <div className="mt-3 flex flex-col items-start gap-2">
                    <textarea className="w-full rounded-lg border border-border bg-panel p-2 text-sm text-ink" placeholder={t('firstRead.off')} aria-label={t('firstRead.off')} maxLength={500} value={text} onChange={(event) => setText(event.target.value)} />
                    <Button size="sm" disabled={busy} onClick={() => void correct()}>{t('firstRead.send')}</Button>
                </div> : <div className="mt-3 flex gap-2">
                    <Button size="sm" variant="secondary" disabled={busy} onClick={() => void confirm()}>{t('firstRead.yes')}</Button>
                    <Button size="sm" variant="secondary" disabled={busy} onClick={() => setCorrecting(true)}>{t('firstRead.no')}</Button>
                </div>}
                {failedAction && <p role="alert" className="mt-2 text-sm text-error">{t('firstRead.failed')}<button type="button" onClick={retry}>{t('firstRead.retry')}</button></p>}
            </section>
            {helps.length >= 2 && <section className="rounded-xl border border-border bg-panel p-4"><p className="text-xs font-semibold uppercase text-muted">{t('firstRead.helps')}</p><ul className="mt-2 space-y-2 text-sm text-ink">{helps.map((line, index) => <li key={index}>{line}</li>)}</ul></section>}
            {(message.offerCount ?? 0) > 0 && <section className="rounded-xl border border-border bg-panel p-4"><p className="text-xs font-semibold uppercase text-muted">{t('firstRead.upNext')}</p><p className="mt-2 font-semibold text-ink">{message.offerCount === 1 ? t('firstRead.offerOne') : t('firstRead.offers', { count: message.offerCount })}</p><p className="mt-2 text-sm text-muted">{t('firstRead.offerDetail')}</p><Button className="mt-3" size="sm" onClick={() => onOpenTarget?.({ kind: 'tasks' })}>{t('firstRead.see')}</Button></section>}
        </div>
    );
}
