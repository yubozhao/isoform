import { useCallback, useEffect, useId, useRef, useState, type ReactNode } from 'react';
import type { TFunction } from 'i18next';
import { useTranslation } from 'react-i18next';
import { Check } from 'lucide-react';
import {
    ConfirmMemoryEntry,
    ForgetMemoryEntry,
    GetMemoryForgetPreview,
    NotMineMemoryEntry,
    NotQuiteMemoryEntry,
    RewordMemoryEntry,
    UndoConfirmMemoryEntry,
    UndoMemoryWording,
    UndoNotQuiteMemoryEntry,
    type AboutYouBelief,
} from '@/lib/wails-shim-app';
import type { main } from '../../../wailsjs/go/models';
import { memoryPoints } from '@/lib/memory-content';
import { Button } from '@/components/ui/button';
import { cn } from '@/lib/utils';
import { activeInterval } from '@/lib/app-activity';
import { slotStatus } from '@/components/about-you/belief-status';
import { WhyLine } from '@/components/why/why-line';

/** How long a one-click Forget waits for Undo before it runs. */
export const FORGET_UNDO_MS = 10_000;

export type ReviewResult =
    | { kind: 'confirmFailed' }
    /** wasNotQuite: That's me on a Not quite belief; Undo puts Not quite back. */
    | { kind: 'confirmed'; wasNotQuite?: boolean; undoFailed?: boolean }
    | { kind: 'asking' }
    | { kind: 'saving' }
    | { kind: 'answerFailed' }
    | { kind: 'corrected'; wording: string; undoFailed?: boolean }
    /** copyAt: when each copy of the idea was marked, for Undo (one card, every copy). */
    | { kind: 'notQuite'; confirmedAt: number; copyAt?: Record<string, number>; undoFailed?: boolean }
    /** notMine: Not mine waits for Undo the same way, then the claim goes as someone else's. */
    | { kind: 'pending'; until: number; notMine?: boolean }
    | { kind: 'forgetting'; notMine?: boolean }
    | { kind: 'forgotten'; days: number; notMine?: boolean }
    | { kind: 'forgetFailed' };

export interface ReviewItem {
    /** The card's head: the newest copy of the idea. */
    id: string;
    text: string;
    where: string;
    /** Every saved copy of this idea, head included. Actions apply to all of them. */
    copyIds?: string[];
    /** How many saved copies the card stands for, shown as "Seen N times" when above one. */
    copies?: number;
    communication?: boolean;
    /** One of the portrait's confirm rows: That's me says the portrait can use it. */
    portraitCandidate?: boolean;
    status?: AboutYouBelief['status'];
}

/** The belief's first point in the person's words, or a new point. */
export function rewordFirstPoint(content: string, text: string): string {
    const point = memoryPoints(content)[0];
    return point
        ? content.slice(0, point.start) + text + content.slice(point.end)
        : `${content.trimEnd()}\n- ${text}\n`;
}

/**
 * That's me, Not quite and Forget for a belief (answer feedback v1.0.0
 * section 6). Each answer becomes its line at once, with Undo until the
 * person leaves the page. Not quite asks in place: words reword the belief,
 * blank stops Isoform using it. Forget waits FORGET_UNDO_MS for Undo, then
 * reads the removal preview and forgets with its token, the same path as the
 * detail's reviewed Forget this. The page cancels it when it closes or goes
 * behind Activity before then.
 */
export function useBeliefReview({
    onConfirmed,
    onForgotten,
    onChanged,
    contentOf,
    copiesOf,
    onForgetCopies,
}: {
    onConfirmed: (saved: main.MemoryEntry) => void;
    onForgotten: (id: string) => void;
    /** An answer or its Undo changed the belief: re-read at once. */
    onChanged: () => void;
    /** The belief's file, for Not quite with words. */
    contentOf: (id: string) => string | undefined;
    /** Every saved copy of the idea a card's head stands for; an action reaches each one. */
    copiesOf?: (id: string) => string[];
    /** Forget on a card for several copies opens the forget dialog, which lists them first. */
    onForgetCopies?: (id: string) => void;
}) {
    const [results, setResults] = useState<Record<string, ReviewResult>>({});
    const copiesRef = useRef(copiesOf);
    copiesRef.current = copiesOf;
    const idsOf = useCallback((id: string) => copiesRef.current?.(id) ?? [id], []);
    const [now, setNow] = useState(() => Date.now());
    const timers = useRef(new Map<string, number>());
    const set = useCallback((id: string, result: ReviewResult) => {
        setResults((prev) => ({ ...prev, [id]: result }));
    }, []);
    const clear = useCallback((id: string) => {
        setResults((prev) => {
            if (!(id in prev)) return prev;
            const next = { ...prev };
            delete next[id];
            return next;
        });
    }, []);

    const confirm = useCallback(async (id: string, wasNotQuite = false) => {
        // The answer shows at once (Y27); a failure brings the buttons back.
        set(id, { kind: 'confirmed', wasNotQuite });
        try {
            for (const copy of idsOf(id)) onConfirmed(await ConfirmMemoryEntry(copy));
        } catch {
            set(id, { kind: 'confirmFailed' });
        }
    }, [idsOf, onConfirmed, set]);

    const ask = useCallback((id: string) => set(id, { kind: 'asking' }), [set]);

    /** Save from the Not quite field: words reword it, blank stops using it. */
    const answerNotQuite = useCallback(async (id: string, words: string) => {
        const wording = words.trim().replace(/\n+/g, ' ');
        set(id, { kind: 'saving' });
        try {
            if (wording) {
                for (const copy of idsOf(id)) {
                    const content = contentOf(copy);
                    if (content === undefined) throw new Error('memory_unavailable');
                    onConfirmed(await RewordMemoryEntry(copy, rewordFirstPoint(content, wording), wording));
                }
                set(id, { kind: 'corrected', wording });
            } else {
                const copyAt: Record<string, number> = {};
                for (const copy of idsOf(id)) copyAt[copy] = await NotQuiteMemoryEntry(copy);
                set(id, { kind: 'notQuite', confirmedAt: copyAt[id] ?? 0, copyAt });
                onChanged();
            }
        } catch {
            set(id, { kind: 'answerFailed' });
        }
    }, [contentOf, idsOf, onChanged, onConfirmed, set]);

    const runForget = useCallback(async (id: string, notMine = false) => {
        timers.current.delete(id);
        set(id, { kind: 'forgetting', notMine });
        try {
            if (notMine) {
                // Not mine (design answer A6): every copy goes as someone else's.
                for (const copy of idsOf(id)) await NotMineMemoryEntry(copy);
                set(id, { kind: 'forgotten', days: 0, notMine: true });
                onForgotten(id);
                return;
            }
            // Every copy goes, so the idea cannot come back from a copy.
            let days = 0;
            for (const copy of idsOf(id)) {
                const preview = await GetMemoryForgetPreview(copy);
                await ForgetMemoryEntry(preview.memoryId, preview.confirmationToken);
                days = preview.retentionDays;
            }
            set(id, { kind: 'forgotten', days });
            onForgotten(id);
        } catch {
            set(id, { kind: 'forgetFailed' });
        }
    }, [idsOf, onForgotten, set]);
    const runForgetRef = useRef(runForget);
    runForgetRef.current = runForget;

    const forget = useCallback((id: string) => {
        // Several copies go only after the dialog has listed them; a one-click forget would not show them.
        if (idsOf(id).length > 1 && onForgetCopies) {
            onForgetCopies(id);
            return;
        }
        if (timers.current.has(id)) return;
        setNow(Date.now());
        set(id, { kind: 'pending', until: Date.now() + FORGET_UNDO_MS });
        timers.current.set(id, window.setTimeout(() => void runForgetRef.current(id), FORGET_UNDO_MS));
    }, [idsOf, onForgetCopies, set]);

    /** Not mine: the claim is about someone else. It waits FORGET_UNDO_MS for Undo, then goes. */
    const notMine = useCallback((id: string) => {
        if (timers.current.has(id)) return;
        setNow(Date.now());
        set(id, { kind: 'pending', until: Date.now() + FORGET_UNDO_MS, notMine: true });
        timers.current.set(id, window.setTimeout(() => void runForgetRef.current(id, true), FORGET_UNDO_MS));
    }, [set]);

    /** Drop a card's state: Cancel, a pending Forget's Undo, or its belief handled elsewhere. */
    const cancel = useCallback((id: string) => {
        const timer = timers.current.get(id);
        if (timer !== undefined) window.clearTimeout(timer);
        timers.current.delete(id);
        clear(id);
    }, [clear]);

    /** Undo an answer: the belief goes back to how it was, and the card opens again. */
    const undo = useCallback(async (id: string) => {
        const result = results[id];
        if (!result) return;
        try {
            switch (result.kind) {
                case 'confirmed':
                    for (const copy of idsOf(id)) {
                        await UndoConfirmMemoryEntry(copy);
                        if (result.wasNotQuite) await NotQuiteMemoryEntry(copy);
                    }
                    break;
                case 'notQuite':
                    for (const copy of idsOf(id)) {
                        await UndoNotQuiteMemoryEntry(copy, result.copyAt?.[copy] ?? result.confirmedAt);
                    }
                    break;
                case 'corrected':
                    for (const copy of idsOf(id)) onConfirmed(await UndoMemoryWording(copy, result.wording));
                    break;
                case 'pending':
                    cancel(id);
                    return;
                default:
                    return;
            }
            clear(id);
            onChanged();
        } catch {
            // The answer stays, and says Undo didn't save (Y15).
            if (result.kind === 'confirmed' || result.kind === 'notQuite' || result.kind === 'corrected') set(id, { ...result, undoFailed: true });
        }
    }, [cancel, clear, idsOf, onChanged, onConfirmed, results, set]);

    /** Every pending forget: the page closed or went behind Activity. */
    const cancelPending = useCallback(() => {
        const ids = [...timers.current.keys()];
        for (const id of ids) cancel(id);
    }, [cancel]);

    const counting = Object.values(results).some((result) => result.kind === 'pending');
    useEffect(() => {
        // Only during a Forget's 10-second undo countdown, the progress the
        // person is watching; stopped while the window is hidden.
        if (!counting) return;
        return activeInterval(() => setNow(Date.now()), 1000);
    }, [counting]);

    useEffect(() => () => {
        for (const timer of timers.current.values()) window.clearTimeout(timer);
        timers.current.clear();
    }, []);

    return { results, now, confirm, ask, answerNotQuite, forget, notMine, cancel, undo, cancelPending, retryForget: runForget };
}

export type BeliefReview = ReturnType<typeof useBeliefReview>;

/** Whether a belief's buttons show: nothing answered yet, or it failed. */
export function reviewOpen(result: ReviewResult | undefined) {
    return !result || result.kind === 'confirmFailed';
}

/** The chip an answer leaves: Confirmed, Not quite, else the belief's own. */
export function answeredStatus(result: ReviewResult | undefined, status: AboutYouBelief['status'] = 'inferred'): AboutYouBelief['status'] {
    switch (result?.kind) {
        case 'confirmed':
        case 'corrected':
            return 'confirmed';
        case 'notQuite':
            return 'not_quite';
        default:
            return status;
    }
}

/** "today", "yesterday" or "on Oct 29" (zh: 今天, 昨天, 在 10月29日). */
export function answerWhen(t: TFunction<'memory'>, language: string, at: number) {
    const start = (time: number) => new Date(new Date(time).toDateString()).getTime();
    const days = Math.round((start(Date.now()) - start(at)) / 86_400_000);
    if (days <= 0) return t('aboutYou.portrait.when.today');
    if (days === 1) return t('aboutYou.portrait.when.yesterday');
    return t('aboutYou.portrait.when.date', { date: new Intl.DateTimeFormat(language, { month: 'short', day: 'numeric' }).format(new Date(at)) });
}

/** Inferred, Learned today, Confirmed or Not quite (outlined, muted). */
export function BeliefChip({ status: given, className }: { status: AboutYouBelief['status']; className?: string }) {
    const { t } = useTranslation('memory');
    const status = slotStatus(given);
    return (
        <span
            data-status={status}
            className={cn(
                'inline-flex h-5 flex-none items-center rounded-md px-2 text-[11px] font-semibold transition-colors duration-[160ms] motion-reduce:transition-none',
                status === 'confirmed' && 'bg-success-bg text-success',
                status === 'learned_today' && 'bg-accent-light text-accent',
                status === 'inferred' && 'bg-surface-active text-muted',
                status === 'not_quite' && 'border border-border text-muted',
                className,
            )}
        >
            {t(`aboutYou.status.${status}`)}
        </span>
    );
}

export function UndoLink({ onClick, buttonRef }: { onClick: () => void; buttonRef?: React.Ref<HTMLButtonElement> }) {
    const { t } = useTranslation('memory');
    return (
        <button
            ref={buttonRef}
            type="button"
            className="cursor-pointer rounded-sm border-0 bg-transparent p-0 font-semibold text-accent hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-border-focus"
            onClick={onClick}
        >
            {t('aboutYou.review.undo')}
        </button>
    );
}

/** "What's closer? You can leave it blank." in place, one line growing to three. */
export function NotQuiteField({ item, review }: { item: ReviewItem; review: BeliefReview }) {
    const { t } = useTranslation('memory');
    const fieldId = useId();
    const [words, setWords] = useState('');
    const saving = review.results[item.id]?.kind === 'saving';
    const failed = review.results[item.id]?.kind === 'answerFailed';
    const save = () => void review.answerNotQuite(item.id, words);
    return (
        <div className="rounded-[10px] bg-panel-warm p-3">
            <label htmlFor={fieldId} className="mb-1.5 block text-[12.5px] text-muted">{t('aboutYou.review.notQuiteLabel')}</label>
            <textarea
                id={fieldId}
                autoFocus
                rows={1}
                value={words}
                disabled={saving}
                placeholder={item.text}
                onChange={(event) => setWords(event.target.value)}
                onKeyDown={(event) => {
                    if (event.key === 'Enter' && !event.shiftKey && !event.nativeEvent.isComposing) {
                        event.preventDefault();
                        save();
                    } else if (event.key === 'Escape') {
                        // Esc cancels the field, not the page.
                        event.preventDefault();
                        event.stopPropagation();
                        review.cancel(item.id);
                    }
                }}
                className="max-h-[calc(3*1.45em+18px)] min-h-[calc(1.45em+18px)] w-full resize-none rounded-lg border border-border bg-panel px-2.5 py-2 text-[13.5px] leading-[1.45] text-ink [field-sizing:content] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-border-focus"
            />
            <div className="mt-2 flex gap-1.5">
                <Button size="sm" disabled={saving} aria-busy={saving} onClick={save}>{t('aboutYou.review.save')}</Button>
                <Button size="sm" variant="outline" disabled={saving} onClick={() => review.cancel(item.id)}>{t('aboutYou.review.cancel')}</Button>
            </div>
            {failed && (
                <p role="alert" className="m-0 mt-2 flex flex-wrap items-center gap-2 text-[12.5px] text-copper">
                    <span>{t('aboutYou.review.wordsFailed')}</span>
                    <button type="button" className="cursor-pointer border-0 bg-transparent p-0 text-[12.5px] text-accent hover:underline" onClick={save}>{t('aboutYou.retry')}</button>
                </p>
            )}
        </div>
    );
}

/**
 * The line an answer leaves, in the spot the buttons were, with Undo next
 * (answer feedback sections 1 and 6). Focus moves to it when the pressed
 * button goes; Undo is the next tab stop.
 */
export function ReviewOutcome({ item, review, className }: { item: ReviewItem; review: BeliefReview; className?: string }) {
    const { t } = useTranslation(['memory', 'common']);
    const result = review.results[item.id];
    const outcome = useRef<HTMLDivElement>(null);
    const undoButton = useRef<HTMLButtonElement>(null);
    const kind = result?.kind;
    useEffect(() => {
        if (kind === 'pending') undoButton.current?.focus();
        else if (kind && kind !== 'asking' && kind !== 'saving') outcome.current?.focus();
    }, [kind]);
    if (!result || reviewOpen(result) || result.kind === 'asking') return null;

    let message: ReactNode = '';
    let tone = 'text-muted';
    switch (result.kind) {
        case 'confirmed':
            message = t(item.communication
                ? 'aboutYou.review.communicationConfirmed'
                : item.portraitCandidate ? 'aboutYou.review.portraitConfirmed' : 'aboutYou.review.confirmed');
            tone = 'text-success';
            break;
        case 'corrected':
            message = t('aboutYou.review.corrected');
            tone = 'text-success';
            break;
        case 'notQuite':
            message = t('aboutYou.review.stoppedUsing');
            break;
        case 'saving':
            message = '';
            break;
        case 'answerFailed':
            message = t('aboutYou.review.answerFailed');
            tone = 'text-copper';
            break;
        case 'pending':
            message = result.notMine ? t('aboutYou.review.notMineDoneClaimOnly') : t('aboutYou.review.forgetPending', { count: Math.round(FORGET_UNDO_MS / 1000) });
            break;
        case 'forgetting':
            message = result.notMine ? t('aboutYou.review.notMineDoneClaimOnly') : t('forget.forgetting');
            break;
        case 'forgotten':
            message = result.notMine ? t('aboutYou.review.notMineDoneClaimOnly') : t('aboutYou.review.forgotten', { count: result.days });
            break;
        case 'forgetFailed':
            message = t('aboutYou.review.forgetFailed');
            tone = 'text-copper';
            break;
    }
    const failed = result.kind === 'forgetFailed' || result.kind === 'answerFailed';
    const undoable = result.kind === 'confirmed' || result.kind === 'corrected' || result.kind === 'notQuite';
    const undoFailed = undoable && !!result.undoFailed;
    return (
        <div className={cn('answer-line flex flex-wrap items-center gap-x-1.5 gap-y-1 text-[13px]', tone, className)}>
            {(result.kind === 'confirmed' || result.kind === 'corrected') && <Check className="h-3.5 w-3.5 flex-none" aria-hidden="true" />}
            {/* Announced once per change; the countdown below stays silent. */}
            <div
                ref={outcome}
                tabIndex={-1}
                role={failed ? 'alert' : 'status'}
                aria-busy={result.kind === 'forgetting' || result.kind === 'saving'}
                className={cn('outline-none', result.kind === 'pending' && !result.notMine && 'sr-only')}
            >
                {message}
            </div>
            {result.kind === 'pending' && !result.notMine && (
                <span aria-hidden="true">
                    {t('aboutYou.review.forgetPending', { count: Math.max(1, Math.ceil((result.until - review.now) / 1000)) })}
                </span>
            )}
            {((undoable && !undoFailed) || result.kind === 'pending') && (
                <>
                    <span aria-hidden="true" className="text-muted">·</span>
                    <UndoLink buttonRef={undoButton} onClick={() => void review.undo(item.id)} />
                </>
            )}
            {undoFailed && (
                <span role="alert" className="inline-flex flex-wrap items-center gap-1.5 text-copper">
                    <span aria-hidden="true" className="text-muted">·</span>
                    <span>{t('aboutYou.review.undoFailed')}</span>
                    <button type="button" className="cursor-pointer border-0 bg-transparent p-0 text-[13px] text-accent hover:underline" onClick={() => void review.undo(item.id)}>{t('aboutYou.retry')}</button>
                </span>
            )}
            {result.kind === 'forgetFailed' && (
                <Button size="sm" variant="secondary" onClick={() => void review.retryForget(item.id, false)}>
                    {t('aboutYou.retry')}
                </Button>
            )}
            {result.kind === 'answerFailed' && (
                <Button size="sm" variant="secondary" onClick={() => review.cancel(item.id)}>
                    {t('aboutYou.retry')}
                </Button>
            )}
        </div>
    );
}

/** Move the focus to the next or previous claim on the page. */
function focusClaim(from: HTMLElement, step: 1 | -1) {
    const rows = Array.from(document.querySelectorAll<HTMLElement>('[data-claim-row]'));
    const next = rows[rows.indexOf(from) + step];
    next?.focus();
}

/**
 * Y, N and E answer the focused claim (computer-you v1.0.0 rule 3, as on
 * Today); the arrows move between claims. Keys typed into a field, or with a
 * modifier, are left alone.
 */
export function claimKeyAction(event: Pick<KeyboardEvent, 'key' | 'metaKey' | 'ctrlKey' | 'altKey' | 'target'> & { isComposing?: boolean }): 'yes' | 'no' | 'notMine' | 'edit' | 'next' | 'previous' | null {
    // pass-access v1.0.0 rule 2: never while typing, an input method composing included.
    if (event.metaKey || event.ctrlKey || event.altKey || event.isComposing) return null;
    const target = event.target as HTMLElement | null;
    if (target && (target.tagName === 'TEXTAREA' || target.tagName === 'INPUT' || target.tagName === 'SELECT' || target.isContentEditable)) return null;
    switch (event.key) {
        case 'y':
        case 'Y':
            return 'yes';
        case 'n':
        case 'N':
            return 'no';
        case 'm':
        case 'M':
            return 'notMine';
        case 'e':
        case 'E':
            return 'edit';
        case 'ArrowDown':
            return 'next';
        case 'ArrowUp':
            return 'previous';
        default:
            return null;
    }
}

/**
 * Edit on a claim, say it my way (redesign v1.0.0, edit.*): the claim in the
 * person's words, Save and Cancel, and what Isoform does with it. Blank words
 * don't save: No is the answer for a claim that isn't right.
 */
export function ClaimEditField({ item, review }: { item: ReviewItem; review: BeliefReview }) {
    const { t } = useTranslation('memory');
    const fieldId = useId();
    const [words, setWords] = useState(item.text);
    const saving = review.results[item.id]?.kind === 'saving';
    const failed = review.results[item.id]?.kind === 'answerFailed';
    const empty = !words.trim();
    const save = () => {
        if (!empty) void review.answerNotQuite(item.id, words);
    };
    return (
        <div className="flex flex-col gap-2 rounded-[10px] bg-panel-warm p-3">
            <label htmlFor={fieldId} className="text-[12.5px] text-muted">{t('aboutYou.edit.label')}</label>
            <textarea
                id={fieldId}
                autoFocus
                rows={1}
                value={words}
                disabled={saving}
                onChange={(event) => setWords(event.target.value)}
                onKeyDown={(event) => {
                    if (event.key === 'Enter' && !event.shiftKey && !event.nativeEvent.isComposing) {
                        event.preventDefault();
                        save();
                    } else if (event.key === 'Escape') {
                        // Esc cancels the field, not the page.
                        event.preventDefault();
                        event.stopPropagation();
                        review.cancel(item.id);
                    }
                }}
                className="max-h-[calc(3*1.45em+18px)] min-h-[calc(1.45em+18px)] w-full resize-none rounded-lg border border-border bg-panel px-2.5 py-2 text-[13.5px] leading-[1.45] text-ink [field-sizing:content] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-border-focus"
            />
            <div className="flex flex-wrap items-center gap-1.5">
                <Button size="sm" disabled={saving || empty} aria-busy={saving} onClick={save}>{t('aboutYou.review.save')}</Button>
                <Button size="sm" variant="outline" disabled={saving} onClick={() => review.cancel(item.id)}>{t('aboutYou.review.cancel')}</Button>
                <span className="text-[12.5px] text-muted">{t('aboutYou.edit.kept')}</span>
            </div>
            {failed && (
                <p role="alert" className="m-0 flex flex-wrap items-center gap-2 text-[12.5px] text-copper">
                    <span>{t('aboutYou.review.wordsFailed')}</span>
                    <button type="button" className="cursor-pointer border-0 bg-transparent p-0 text-[12.5px] text-accent hover:underline" onClick={save}>{t('aboutYou.retry')}</button>
                </p>
            )}
        </div>
    );
}

/** The focus ring and rounded highlight every claim line shares (no left bar). */
export const CLAIM_ROW_CLASS = 'flex flex-col gap-1.5 rounded-xl px-3 py-2.5 transition-colors duration-[160ms] hover:bg-surface-hover focus-visible:bg-surface-active focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-border-focus motion-reduce:transition-none';

/**
 * One claim: where it came from with Why, "Right?" and the claim, then Yes,
 * No and Edit on Y, N and E (computer-you v1.0.0 rule 3; redesign v1.0.0
 * section 1). Yes confirms it; No stops Isoform using it and keeps it under
 * Not right; Edit says it the person's way. Forget stays, quieter, beside
 * the answers. The same line answers everywhere on About you.
 */
export function ClaimLine({ item, review, onWhy, onOpen }: { item: ReviewItem; review: BeliefReview; onWhy?: (id: string) => void; onOpen?: (id: string) => void }) {
    const { t } = useTranslation(['memory', 'common']);
    const result = review.results[item.id];
    const open = reviewOpen(result);
    const forgetting = result?.kind === 'pending' || result?.kind === 'forgetting' || result?.kind === 'forgotten';
    const answer = (action: 'yes' | 'no' | 'notMine' | 'edit') => {
        if (action === 'yes') void review.confirm(item.id);
        else if (action === 'no') void review.answerNotQuite(item.id, '');
        else if (action === 'notMine') review.notMine(item.id);
        else review.ask(item.id);
    };
    const where = [item.where, (item.copies ?? 0) > 1 ? t('aboutYou.review.copies', { count: item.copies }) : ''].filter(Boolean).join(' · ');

    return (
        <article
            // pass-access v1.0.0 rule 4: one element, read in order: source, sentence, what Yes does.
            aria-label={item.where ? t('aboutYou.review.cardLabel', { where: item.where, text: item.text }) : t('aboutYou.review.cardLabelNoSource', { text: item.text })}
            tabIndex={0}
            // No data-claim-id: a memory's ID stays out of the DOM (it is copied only from the detail's More menu).
            data-claim-row
            aria-keyshortcuts="Y N M E"
            onKeyDown={(event) => {
                const action = claimKeyAction({ key: event.key, metaKey: event.metaKey, ctrlKey: event.ctrlKey, altKey: event.altKey, target: event.target, isComposing: event.nativeEvent.isComposing });
                if (!action) return;
                if (action === 'next' || action === 'previous') {
                    event.preventDefault();
                    focusClaim(event.currentTarget, action === 'next' ? 1 : -1);
                    return;
                }
                if (event.target !== event.currentTarget || !open) return;
                event.preventDefault();
                answer(action);
            }}
            className={CLAIM_ROW_CLASS}
        >
            <WhyLine kind="claim" where={where} onWhy={onWhy ? () => onWhy(item.id) : undefined} />
            <p className={cn('m-0 text-sm leading-[1.45] text-ink break-words', forgetting && 'text-muted line-through')}>
                {!forgetting && open && <span className="mr-1.5 text-muted">{t('aboutYou.review.right')}</span>}
                {onOpen ? (
                    <button type="button" tabIndex={-1} onClick={() => onOpen(item.id)} className="cursor-pointer border-0 bg-transparent p-0 text-left text-ink hover:underline">
                        {t('aboutYou.review.quote', { text: item.text })}
                    </button>
                ) : (
                    <span>{t('aboutYou.review.quote', { text: item.text })}</span>
                )}
            </p>
            {open ? (
                <>
                    {result?.kind === 'confirmFailed' && (
                        <p role="alert" className="m-0 text-[12.5px] text-copper">{t('aboutYou.review.confirmFailed')}</p>
                    )}
                    <div className="flex flex-wrap items-center gap-1.5">
                        <Button size="sm" aria-keyshortcuts="Y" onClick={() => answer('yes')}>
                            {t('aboutYou.review.yes')}
                        </Button>
                        <Button size="sm" variant="secondary" aria-keyshortcuts="N" onClick={() => answer('no')}>
                            {t('aboutYou.review.no')}
                        </Button>
                        <Button size="sm" variant="secondary" aria-keyshortcuts="M" onClick={() => answer('notMine')}>
                            {t('aboutYou.review.notMine')}
                        </Button>
                        <Button size="sm" variant="outline" aria-keyshortcuts="E" onClick={() => answer('edit')}>
                            {t('aboutYou.review.edit')}
                        </Button>
                        <button
                            type="button"
                            onClick={() => review.forget(item.id)}
                            aria-label={t('aboutYou.review.forgetClaim', { claim: item.text })}
                            className="ml-auto cursor-pointer rounded-sm border-0 bg-transparent px-1 text-xs text-muted hover:text-ink"
                        >
                            {t('aboutYou.review.forget')}
                        </button>
                    </div>
                </>
            ) : result?.kind === 'asking' || result?.kind === 'saving' || result?.kind === 'answerFailed' ? (
                <ClaimEditField item={item} review={review} />
            ) : (
                <ReviewOutcome item={item} review={review} />
            )}
        </article>
    );
}

export function NewThisWeek({
    items,
    review,
    headingId = 'about-you-review',
    title,
    hint,
    onWhy,
    onOpen,
    footer,
}: {
    items: ReviewItem[];
    review: BeliefReview;
    headingId?: string;
    /** The first days' "Check these first", in place of "To check". */
    title?: string;
    /** The line beside that title, in place of the count left. */
    hint?: string;
    /** Why on a claim: the moments it came from. */
    onWhy?: (id: string) => void;
    /** The claim's words open its detail. */
    onOpen?: (id: string) => void;
    /** Below the claims (the import link). */
    footer?: ReactNode;
}) {
    const { t } = useTranslation(['memory', 'common']);
    if (items.length === 0 && !footer) return null;

    return (
        <section aria-labelledby={headingId} className="flex flex-col gap-3">
            <div className="flex flex-wrap items-baseline gap-x-2.5 gap-y-1.5">
                <h2 id={headingId} className="m-0 text-lg font-semibold text-ink">{title ?? t('aboutYou.toCheck')}</h2>
                {hint && <span className="text-[13px] text-muted">{hint}</span>}
            </div>
            <div data-testid="review-cards" className="-mx-3 flex flex-col gap-0.5">
                {items.map((item) => (
                    <ClaimLine key={item.id} item={item} review={review} onWhy={onWhy} onOpen={onOpen} />
                ))}
            </div>
            {footer}
        </section>
    );
}
