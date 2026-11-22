import { useCallback, useEffect, useId, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Check } from 'lucide-react';
import { GetAboutYouPortrait, usePagePrivacyWithdrawal, usePageQueryRefresh } from '@/lib/page-queries';
import { pageQueryCache } from '@/lib/query-cache';
import {
    ConfirmAboutYouPortrait,
    NotQuiteAboutYouPortrait,
    UndoConfirmAboutYouPortrait,
    type AboutYouPortrait,
} from '@/lib/wails-shim-app';
import { Button } from '@/components/ui/button';
import { CLAIM_ROW_CLASS, UndoLink, claimKeyAction } from '@/components/about-you/new-this-week';
import { WhyLine } from '@/components/why/why-line';
import { cn } from '@/lib/utils';

/** The portrait's one sentence: its first, as written. */
export function portraitOneLine(portrait: AboutYouPortrait | null | undefined): string {
    if (portrait?.state !== 'ready') return '';
    const first = portrait.sentences?.[0];
    return (first?.parts ?? []).map((part) => part.text).join('').replace(/\s+/g, ' ').trim();
}

type Answer =
    | { kind: 'open' }
    | { kind: 'saving' }
    | { kind: 'yes'; ids: string[] }
    | { kind: 'no' }
    | { kind: 'editing' }
    | { kind: 'edited' }
    | { kind: 'failed'; retry: () => void };

/**
 * In one line (redesign-you v1.0.0 rule 3): the portrait as one claim, its
 * sentence, Why, "Right?" with Yes, No and Edit (no Not mine: it's about the
 * person by definition). The portrait's name goes: a sentence can be judged,
 * a title can't. Its Edit is the most valuable answer on the page.
 */
export function InOneLine({ onWhy }: { onWhy?: (memoryId: string) => void }) {
    const { t, i18n } = useTranslation('memory');
    const [portrait, setPortrait] = useState<AboutYouPortrait | null>(() => pageQueryCache.peek<AboutYouPortrait>('GetAboutYouPortrait') ?? null);
    const [failed, setFailed] = useState(false);
    const [answer, setAnswer] = useState<Answer>({ kind: 'open' });
    const [words, setWords] = useState('');
    const read = useRef(0);
    const fieldId = useId();

    const load = useCallback(() => {
        const current = ++read.current;
        GetAboutYouPortrait()
            .then((next: AboutYouPortrait) => {
                if (current !== read.current) return;
                setPortrait(next);
                setFailed(false);
            })
            .catch(() => {
                if (current === read.current) setFailed(true);
            });
    }, []);
    useEffect(() => load(), [load]);
    usePageQueryRefresh(['GetAboutYouPortrait'], load);
    usePagePrivacyWithdrawal(useCallback(() => {
        read.current++;
        setPortrait(null);
        setAnswer({ kind: 'open' });
        load();
    }, [load]));

    const sentence = portraitOneLine(portrait);
    if (failed && !portrait) {
        return (
            <div role="alert" data-testid="in-one-line-failed" className="flex items-center gap-3 rounded-xl border border-border bg-panel px-4 py-3.5 text-[13px] text-copper">
                <span className="flex-1">{t('aboutYou.oneLineFailed')}</span>
                <Button size="sm" variant="secondary" onClick={load}>{t('aboutYou.retry')}</Button>
            </div>
        );
    }
    if (!sentence || !portrait) return null;

    const memoryIds = [...new Set((portrait.sentences?.[0]?.parts ?? []).flatMap((part) => part.memoryIds ?? []))];
    const said = (portrait.saidAt ?? 0) > 0;
    const open = !said && (answer.kind === 'open' || answer.kind === 'failed');

    const yes = async () => {
        setAnswer({ kind: 'saving' });
        try {
            setAnswer({ kind: 'yes', ids: await ConfirmAboutYouPortrait() });
        } catch {
            setAnswer({ kind: 'failed', retry: () => void yes() });
        }
    };
    const no = async () => {
        setAnswer({ kind: 'saving' });
        try {
            await NotQuiteAboutYouPortrait('');
            setAnswer({ kind: 'no' });
        } catch {
            setAnswer({ kind: 'failed', retry: () => void no() });
        }
    };
    const saveEdit = async () => {
        const text = words.trim().replace(/\n+/g, ' ');
        if (!text) return;
        setAnswer({ kind: 'saving' });
        try {
            await NotQuiteAboutYouPortrait(text);
            setAnswer({ kind: 'edited' });
        } catch {
            setAnswer({ kind: 'failed', retry: () => void saveEdit() });
        }
    };
    const undoYes = async (ids: string[]) => {
        try {
            await UndoConfirmAboutYouPortrait(ids);
            setAnswer({ kind: 'open' });
            pageQueryCache.invalidate(['GetAboutYouPortrait']);
        } catch {
            setAnswer({ kind: 'failed', retry: () => void undoYes(ids) });
        }
    };
    const edit = () => {
        setWords(sentence);
        setAnswer({ kind: 'editing' });
    };

    return (
        <section aria-labelledby="about-you-one-line" data-testid="in-one-line" className="flex flex-col gap-2">
            <h2 id="about-you-one-line" className="m-0 text-lg font-semibold text-ink">{t('aboutYou.oneLine')}</h2>
            <article
                tabIndex={0}
                data-claim-row
                aria-keyshortcuts="Y N E"
                aria-label={sentence}
                onKeyDown={(event) => {
                    const action = claimKeyAction({ key: event.key, metaKey: event.metaKey, ctrlKey: event.ctrlKey, altKey: event.altKey, target: event.target, isComposing: event.nativeEvent.isComposing });
                    // No Not mine here: the portrait is about you by definition (design answer A6).
                    if (!action || action === 'next' || action === 'previous' || action === 'notMine') return;
                    if (event.target !== event.currentTarget || !open) return;
                    event.preventDefault();
                    if (action === 'yes') void yes();
                    else if (action === 'no') void no();
                    else edit();
                }}
                className={cn(CLAIM_ROW_CLASS, '-mx-3')}
            >
                {said ? (
                    <span className="inline-flex items-center gap-1 text-xs text-success">
                        <Check className="h-3 w-3 flex-none" aria-hidden="true" />
                        {t('aboutYou.youConfirmedOn', { date: new Intl.DateTimeFormat(i18n.language, { month: 'short', day: 'numeric' }).format(new Date(portrait.saidAt ?? 0)) })}
                    </span>
                ) : (
                    <WhyLine kind="claim" onWhy={onWhy && memoryIds.length > 0 ? () => onWhy(memoryIds[0]) : undefined} />
                )}
                <p className="m-0 text-[15px] leading-[1.5] text-ink break-words">
                    {open && <span className="mr-1.5 text-muted">{t('aboutYou.review.right')}</span>}
                    {sentence}
                </p>
                {open && (
                    <>
                        {answer.kind === 'failed' && <p role="alert" className="m-0 text-[12.5px] text-copper">{t('aboutYou.review.answerFailed')}</p>}
                        <div className="flex flex-wrap items-center gap-1.5">
                            <Button size="sm" aria-keyshortcuts="Y" onClick={() => void yes()}>{t('aboutYou.review.yes')}</Button>
                            <Button size="sm" variant="secondary" aria-keyshortcuts="N" onClick={() => void no()}>{t('aboutYou.review.no')}</Button>
                            <Button size="sm" variant="outline" aria-keyshortcuts="E" onClick={edit}>{t('aboutYou.review.edit')}</Button>
                        </div>
                    </>
                )}
                {(answer.kind === 'editing' || answer.kind === 'saving') && !said && (
                    <div className="flex flex-col gap-2 rounded-[10px] bg-panel-warm p-3">
                        <label htmlFor={fieldId} className="text-[12.5px] text-muted">{t('aboutYou.edit.label')}</label>
                        <textarea
                            id={fieldId}
                            autoFocus
                            rows={2}
                            value={words}
                            disabled={answer.kind === 'saving'}
                            onChange={(event) => setWords(event.target.value)}
                            onKeyDown={(event) => {
                                if (event.key === 'Escape') {
                                    event.preventDefault();
                                    event.stopPropagation();
                                    setAnswer({ kind: 'open' });
                                }
                            }}
                            className="w-full resize-y rounded-lg border border-border bg-panel px-2.5 py-2 text-[13.5px] leading-[1.45] text-ink focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-border-focus"
                        />
                        <div className="flex flex-wrap items-center gap-1.5">
                            <Button size="sm" disabled={answer.kind === 'saving' || !words.trim()} onClick={() => void saveEdit()}>{t('aboutYou.review.save')}</Button>
                            <Button size="sm" variant="outline" disabled={answer.kind === 'saving'} onClick={() => setAnswer({ kind: 'open' })}>{t('aboutYou.review.cancel')}</Button>
                            <span className="text-[12.5px] text-muted">{t('aboutYou.edit.kept')}</span>
                        </div>
                    </div>
                )}
                {answer.kind === 'yes' && (
                    <p role="status" className="m-0 flex flex-wrap items-center gap-1.5 text-[13px] text-success">
                        <Check className="h-3.5 w-3.5 flex-none" aria-hidden="true" />
                        <span>{t('aboutYou.review.confirmed')}</span>
                        <span aria-hidden="true" className="text-muted">·</span>
                        <UndoLink onClick={() => void undoYes(answer.ids)} />
                    </p>
                )}
                {answer.kind === 'no' && <p role="status" className="m-0 text-[13px] text-muted">{t('aboutYou.portrait.rewriting')}</p>}
                {answer.kind === 'edited' && <p role="status" className="m-0 text-[13px] text-muted">{t('aboutYou.portrait.rewriting')}</p>}
            </article>
        </section>
    );
}
