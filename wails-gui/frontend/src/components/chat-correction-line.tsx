import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { cn } from '@/lib/utils';
import * as appBindings from '@/lib/wails-shim-app';
import { getChatCorrection, getChatCorrectionUndo } from '@/lib/stream-helpers';
import type { StreamItem } from '@/types';

function undoBinding(): (id: string) => Promise<string> {
    try {
        const fn = (appBindings as Pick<typeof appBindings, 'UndoChatCorrection'>).UndoChatCorrection;
        if (typeof fn === 'function') return fn;
    } catch {
        // A mocked module without this export.
    }
    return () => Promise.reject(new Error('unavailable'));
}

/**
 * Under a correction (phone-chat v1.0.0 rule 8): "Changed: {where} · Undo".
 * Undo takes the correction back; the line then says it was undone.
 */
export function ChatCorrectionLine({ item, compact = false }: { item: StreamItem; compact?: boolean }) {
    const { t } = useTranslation('chat');
    const correction = getChatCorrection(item);
    const [status, setStatus] = useState(correction?.status ?? '');
    const [busy, setBusy] = useState(false);
    const [failed, setFailed] = useState(false);
    useEffect(() => { setStatus(correction?.status ?? ''); }, [correction?.status]);
    if (!correction) return null;

    const undo = () => {
        setBusy(true);
        setFailed(false);
        undoBinding()(item.id)
            .then((saved) => { if (saved === 'undone') setStatus('undone'); })
            .catch(() => setFailed(true))
            .finally(() => setBusy(false));
    };

    return (
        <div data-chat-correction={status || 'changed'} className={cn('animate-rise-in text-[12.5px] text-muted', compact ? '' : 'max-w-[90%] mx-auto')}>
            {status === 'undone' ? (
                <p role="status">{t('mainChat.correction.changedUndone', { where: correction.where })}</p>
            ) : (
                <p className="flex flex-wrap items-center gap-x-1.5">
                    <span>{t('mainChat.correction.changed', { where: correction.where })}</span>
                    <span aria-hidden="true">·</span>
                    <button type="button" disabled={busy} className="font-medium text-ink transition-colors hover:text-accent disabled:opacity-60" onClick={undo}>
                        {t('mainChat.correction.undo')}
                    </button>
                </p>
            )}
            {failed ? <p role="alert" className="text-subtle">{t('mainChat.correction.failed')}</p> : null}
        </div>
    );
}

/**
 * What an Undo answers in the thread (computer-chat v1.1.0 rule 5):
 * "Undone. {what} is as it was." with "Changed back: {what}".
 */
export function ChatCorrectionUndoReply({ item, compact = false }: { item: StreamItem; compact?: boolean }) {
    const { t } = useTranslation('chat');
    const where = getChatCorrectionUndo(item);
    if (!where) return null;
    return (
        <div data-chat-correction-undo className={cn('animate-rise-in text-[13px] leading-relaxed', compact ? '' : 'max-w-[90%] mx-auto')}>
            <p className="text-ink">{t('mainChat.correction.undoneReply', { where })}</p>
            <p className="text-[12.5px] text-muted">{t('mainChat.correction.changedBack', { where })}</p>
        </div>
    );
}
