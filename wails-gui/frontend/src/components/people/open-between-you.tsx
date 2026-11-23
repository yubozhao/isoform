// Open between you (redesign-you v1.0.0 rule 16): the promises both ways,
// each under "Still open?" with Yes, No, Not mine and Edit on Y, N, M and E,
// the same claim line About you answers with. A promise is a Purpose map
// commitment; every answer moves it there and is recorded (people_promises.go).
import { useId, useState, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { AnswerPersonPromise, type PersonLoop, type PersonPromiseAnswer } from '@/lib/wails-shim-app';
import { Button } from '@/components/ui/button';
import { cn } from '@/lib/utils';
import { CLAIM_ROW_CLASS, claimKeyAction } from '@/components/about-you/new-this-week';
import { WhyLine } from '@/components/why/why-line';
import { dayWords, type Translate } from './people-format';

const DAY = 24 * 60 * 60 * 1000;
const WEEK = 7 * DAY;

/** A promise with who it is between. */
export interface PromiseEntry {
    loop: PersonLoop;
    personId: string;
    name: string;
}

/** Only Purpose promises can be answered; other open loops stay as they were. */
export function isPromise(loop: PersonLoop): boolean {
    return loop.source === 'purpose' && (loop.kind === 'you_owe' || loop.kind === 'waiting_on_them');
}

/** New this week first, then the rest; within each, dated ones by due date, then newest. */
export function sortPromises<T extends { loop: PersonLoop }>(entries: T[], now = Date.now()): T[] {
    const fresh = (loop: PersonLoop) => now - (loop.whereAt || loop.since) < WEEK;
    return [...entries].sort((x, y) => {
        const a = x.loop;
        const b = y.loop;
        if (fresh(a) !== fresh(b)) return fresh(a) ? -1 : 1;
        if ((a.dueAt > 0) !== (b.dueAt > 0)) return a.dueAt > 0 ? -1 : 1;
        if (a.dueAt !== b.dueAt) return a.dueAt - b.dueAt;
        if (a.since !== b.since) return b.since - a.since;
        return a.id.localeCompare(b.id);
    });
}

/** "Thursday" this week, else the day words the page uses. */
function heardDay(t: Translate, lang: string, ms: number, now: number): string {
    const ago = now - ms;
    if (ago >= 2 * DAY && ago < 6 * DAY) return new Date(ms).toLocaleDateString(lang, { weekday: 'long' });
    return dayWords(t, lang, ms, now);
}

/** "You owe · email, Tuesday · due Friday". */
export function promiseWhere(t: Translate, lang: string, loop: PersonLoop, now = Date.now()): string {
    const at = loop.whereAt || loop.since;
    const where = at > 0 ? t(`who.where.${loop.where || 'other'}`, { day: heardDay(t, lang, at, now) }) : '';
    const owe = loop.kind === 'you_owe';
    const head = where ? t(owe ? 'who.youOwe' : 'who.owedToYou', { where }) : t(owe ? 'who.youOweNoWhere' : 'who.owedToYouNoWhere');
    return loop.dueAt > 0 ? `${head} · ${t('who.due', { when: dayWords(t, lang, loop.dueAt, now) })}` : head;
}

/** Move the focus to the next or previous claim line on the page. */
function focusLine(from: HTMLElement, step: 1 | -1) {
    const rows = Array.from(document.querySelectorAll<HTMLElement>('[data-claim-row]'));
    rows[rows.indexOf(from) + step]?.focus();
}

type LineState =
    | { kind: 'open' }
    | { kind: 'reasons' }
    | { kind: 'editing' }
    | { kind: 'saving'; editing?: boolean }
    | { kind: 'failed'; message: 'answerFailed' | 'changed' | 'gone'; editing?: boolean }
    | { kind: 'done'; outcome: 'kept' | 'closed' | 'closedDone' | 'closedNot' | 'notMineDone' | 'edited' };

function dueInput(ms: number): string {
    if (!ms) return '';
    const d = new Date(ms);
    return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`;
}

let answerSeq = 0;
function answerKey(loopId: string): string {
    answerSeq += 1;
    return `${loopId}:${Date.now().toString(36)}:${answerSeq}`;
}

/**
 * One promise: where it was heard, "Still open?" and the promise, then the
 * four answers. No asks why (Already done, Not a promise, or Skip); Edit
 * fixes the words or the date in place. A failed answer says so with Try
 * again and keeps the buttons.
 */
export function PromiseLine({
    entry,
    showPerson = false,
    onOpenPerson,
    onAnswered,
}: {
    entry: PromiseEntry;
    /** On the page's list, the person it's with, as a link to them. */
    showPerson?: boolean;
    onOpenPerson?: (id: string) => void;
    /** An answer landed: the page re-reads who you know. */
    onAnswered: () => void;
}) {
    const { t, i18n } = useTranslation('people');
    const tr: Translate = (key, options) => t(key as never, options as never) as unknown as string;
    const { loop, name } = entry;
    const [state, setState] = useState<LineState>({ kind: 'open' });
    const [words, setWords] = useState(loop.title);
    const [due, setDue] = useState(() => dueInput(loop.dueAt));
    const [last, setLast] = useState<Omit<PersonPromiseAnswer, 'key' | 'loopId'> | null>(null);
    const fieldId = useId();
    const dueId = useId();
    const where = promiseWhere(tr, i18n.language, loop);
    const editing = state.kind === 'editing' || ((state.kind === 'saving' || state.kind === 'failed') && !!state.editing);
    const answerable = state.kind === 'open' || (state.kind === 'failed' && !state.editing);

    const send = async (answer: Omit<PersonPromiseAnswer, 'key' | 'loopId'>) => {
        const isEdit = answer.answer === 'edit';
        setLast(answer);
        setState({ kind: 'saving', editing: isEdit });
        try {
            const result = await AnswerPersonPromise({ ...answer, loopId: loop.id, expectedEventId: loop.eventId, personId: entry.personId, key: answerKey(loop.id) });
            const outcome =
                answer.answer === 'yes' ? 'kept'
                    : answer.answer === 'edit' ? 'edited'
                        : answer.answer === 'not_mine' ? 'notMineDone'
                            : answer.reason === 'already_done' ? 'closedDone'
                                : answer.reason === 'not_a_promise' ? 'closedNot'
                                    : result.open ? 'kept' : 'closed';
            setState({ kind: 'done', outcome });
            onAnswered();
        } catch (error) {
            const text = String((error as Error)?.message ?? error);
            const message = text.includes('promise_changed') ? 'changed' : text.includes('promise_unavailable') ? 'gone' : 'answerFailed';
            setState({ kind: 'failed', message, editing: isEdit });
            // A promise that moved or went is re-read, so the line shows it as it is.
            if (message !== 'answerFailed') onAnswered();
        }
    };

    const answer = (action: 'yes' | 'no' | 'notMine' | 'edit') => {
        if (action === 'yes') void send({ answer: 'yes' });
        else if (action === 'no') setState({ kind: 'reasons' });
        else if (action === 'notMine') void send({ answer: 'not_mine' });
        else setState({ kind: 'editing' });
    };
    const saveEdit = () => {
        if (!words.trim()) return;
        void send({ answer: 'edit', title: words.trim(), dueDate: due, setDue: due !== dueInput(loop.dueAt) });
    };
    const cancel = () => setState({ kind: 'open' });

    const label = tr('who.lineLabel', { where, text: loop.title });
    return (
        <article
            aria-label={label}
            tabIndex={0}
            data-claim-row
            aria-keyshortcuts="Y N M E"
            onKeyDown={(event) => {
                const action = claimKeyAction({ key: event.key, metaKey: event.metaKey, ctrlKey: event.ctrlKey, altKey: event.altKey, target: event.target, isComposing: event.nativeEvent.isComposing });
                if (!action) return;
                if (action === 'next' || action === 'previous') {
                    event.preventDefault();
                    focusLine(event.currentTarget, action === 'next' ? 1 : -1);
                    return;
                }
                if (event.target !== event.currentTarget || !answerable) return;
                event.preventDefault();
                answer(action);
            }}
            className={CLAIM_ROW_CLASS}
        >
            <span className="flex min-w-0 flex-wrap items-baseline gap-x-1.5">
                <WhyLine kind="claim" where={where} />
                {showPerson && (
                    <>
                        <span aria-hidden="true" className="text-xs text-muted">·</span>
                        <button
                            type="button"
                            tabIndex={-1}
                            onClick={() => onOpenPerson?.(entry.personId)}
                            aria-label={tr('who.openPerson', { name })}
                            className="cursor-pointer rounded-sm border-0 bg-transparent p-0 text-xs font-medium text-muted underline-offset-2 hover:text-ink hover:underline"
                        >
                            {tr('who.withPerson', { name })}
                        </button>
                    </>
                )}
            </span>
            <p className={cn('m-0 text-sm leading-[1.45] text-ink break-words', state.kind === 'done' && state.outcome !== 'kept' && state.outcome !== 'edited' && 'text-muted line-through')}>
                {state.kind !== 'done' && <span className="mr-1.5 text-muted">{tr('who.stillOpen')}</span>}
                <span>{state.kind === 'done' && state.outcome === 'edited' && last?.title ? last.title : loop.title}</span>
            </p>

            {editing ? (
                <div className="flex flex-col gap-2 rounded-[10px] bg-panel-warm p-3">
                    <label htmlFor={fieldId} className="text-[12.5px] text-muted">{tr('who.editWords')}</label>
                    <textarea
                        id={fieldId}
                        autoFocus
                        rows={1}
                        value={words}
                        disabled={state.kind === 'saving'}
                        onChange={(event) => setWords(event.target.value)}
                        onKeyDown={(event) => {
                            if (event.key === 'Enter' && !event.shiftKey && !event.nativeEvent.isComposing) {
                                event.preventDefault();
                                saveEdit();
                            } else if (event.key === 'Escape') {
                                event.preventDefault();
                                event.stopPropagation();
                                cancel();
                            }
                        }}
                        className="max-h-[calc(3*1.45em+18px)] min-h-[calc(1.45em+18px)] w-full resize-none rounded-lg border border-border bg-panel px-2.5 py-2 text-[13.5px] leading-[1.45] text-ink [field-sizing:content] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-border-focus"
                    />
                    <div className="flex items-center gap-2">
                        <label htmlFor={dueId} className="text-[12.5px] text-muted">{tr('who.editDue')}</label>
                        <input
                            id={dueId}
                            type="date"
                            value={due}
                            disabled={state.kind === 'saving'}
                            onChange={(event) => setDue(event.target.value)}
                            className="h-8 rounded-lg border border-border bg-panel px-2 text-[13px] text-ink focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-border-focus"
                        />
                    </div>
                    <div className="flex flex-wrap items-center gap-1.5">
                        <Button size="sm" disabled={state.kind === 'saving' || !words.trim()} aria-busy={state.kind === 'saving'} onClick={saveEdit}>{tr('who.editSave')}</Button>
                        <Button size="sm" variant="outline" disabled={state.kind === 'saving'} onClick={cancel}>{tr('who.editCancel')}</Button>
                        <span className="text-[12.5px] text-muted">{tr('who.editKept')}</span>
                    </div>
                    {state.kind === 'failed' && <FailedLine message={state.message} onRetry={state.message === 'answerFailed' ? saveEdit : undefined} />}
                </div>
            ) : state.kind === 'reasons' ? (
                <div className="flex flex-wrap items-center gap-1.5" role="group" aria-label={tr('who.reasonAsk')}>
                    <span className="mr-1 text-[12.5px] text-muted">{tr('who.reasonAsk')}</span>
                    <Button size="sm" variant="secondary" onClick={() => void send({ answer: 'no', reason: 'already_done' })}>{tr('who.reasonDone')}</Button>
                    <Button size="sm" variant="secondary" onClick={() => void send({ answer: 'no', reason: 'not_a_promise' })}>{tr('who.reasonNot')}</Button>
                    <Button size="sm" variant="outline" onClick={() => void send({ answer: 'no' })}>{tr('who.skip')}</Button>
                </div>
            ) : state.kind === 'done' ? (
                <p role="status" className="m-0 text-[12.5px] text-muted">{tr(`who.${state.outcome}`, { name })}</p>
            ) : (
                <>
                    {state.kind === 'failed' && <FailedLine message={state.message} onRetry={state.message === 'answerFailed' && last ? () => void send(last) : undefined} />}
                    <div className="flex flex-wrap items-center gap-1.5">
                        <Button size="sm" aria-keyshortcuts="Y" disabled={state.kind === 'saving'} onClick={() => answer('yes')}>{tr('who.yes')}</Button>
                        <Button size="sm" variant="secondary" aria-keyshortcuts="N" disabled={state.kind === 'saving'} onClick={() => answer('no')}>{tr('who.no')}</Button>
                        <Button size="sm" variant="secondary" aria-keyshortcuts="M" disabled={state.kind === 'saving'} onClick={() => answer('notMine')}>{tr('who.notMine')}</Button>
                        <Button size="sm" variant="outline" aria-keyshortcuts="E" disabled={state.kind === 'saving'} onClick={() => answer('edit')}>{tr('who.edit')}</Button>
                    </div>
                    <span className="text-[12.5px] text-muted">{tr('who.yesDoes')}</span>
                </>
            )}
        </article>
    );
}

function FailedLine({ message, onRetry }: { message: 'answerFailed' | 'changed' | 'gone'; onRetry?: () => void }) {
    const { t } = useTranslation('people');
    return (
        <p role="alert" className="m-0 flex flex-wrap items-center gap-2 text-[12.5px] text-copper">
            <span>{t(`who.${message}` as never) as unknown as string}</span>
            {onRetry && (
                <button type="button" className="cursor-pointer border-0 bg-transparent p-0 text-[12.5px] text-accent hover:underline" onClick={onRetry}>
                    {t('overview.retry')}
                </button>
            )}
        </p>
    );
}

/**
 * The page's first section: every promise both ways across people, new this
 * week first. Its count is the page's old headline ("1 person is waiting on
 * you").
 */
export function OpenBetweenYou({
    entries,
    count,
    onOpenPerson,
    onAnswered,
    children,
}: {
    entries: PromiseEntry[];
    /** "1 person is waiting on you", or nothing. */
    count?: string;
    onOpenPerson: (id: string) => void;
    onAnswered: () => void;
    /** The people with something open, below the promises. */
    children?: ReactNode;
}) {
    const { t } = useTranslation('people');
    const sorted = sortPromises(entries);
    return (
        <section aria-labelledby="people-open-h" className="flex flex-col gap-3">
            <div className="flex flex-wrap items-baseline gap-x-2.5 gap-y-1.5">
                <h2 id="people-open-h" className="m-0 text-[18px] font-semibold text-ink">{t('who.open')}</h2>
                {count && <span className="text-[13px] text-muted">{count}</span>}
            </div>
            {sorted.length > 0 && (
                <div className="-mx-3 flex flex-col">
                    {sorted.map((entry) => (
                        <PromiseLine key={`${entry.personId}:${entry.loop.id}`} entry={entry} showPerson onOpenPerson={onOpenPerson} onAnswered={onAnswered} />
                    ))}
                </div>
            )}
            {children}
        </section>
    );
}
