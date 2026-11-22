import { useState, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { Check } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { UndoNotQuiteMemoryEntry, type AboutYouBelief } from '@/lib/wails-shim-app';
import { WhyLine } from '@/components/why/why-line';
import { CLAIM_ROW_CLASS, UndoLink, answerWhen } from '@/components/about-you/new-this-week';

/** A claim the page shows: its id, its sentence and where it came from. */
export interface ClaimRow {
    id: string;
    text: string;
    where?: string;
    belief?: AboutYouBelief;
}

/** "You confirmed · Nov 20", "You edited" or "You said" (computer-activity v1.1.0 rule 5). */
export function ConfirmedHowLine({ belief }: { belief?: AboutYouBelief }) {
    const { t, i18n } = useTranslation('memory');
    let words = '';
    switch (belief?.confirmedHow) {
        case 'edited':
            words = t('aboutYou.youEdited');
            break;
        case 'said':
            words = t('aboutYou.youSaid');
            break;
        default: {
            const at = belief?.confirmedHowAt || belief?.confirmedAt || 0;
            words = at > 0
                ? t('aboutYou.youConfirmedOn', { date: new Intl.DateTimeFormat(i18n.language, { month: 'short', day: 'numeric' }).format(new Date(at)) })
                : t('aboutYou.youConfirmed');
        }
    }
    return (
        <span data-testid="confirmed-how" className="inline-flex items-center gap-1 text-xs text-success">
            <Check className="h-3 w-3 flex-none" aria-hidden="true" />
            {words}
        </span>
    );
}

function SectionHead({ id, title, meta }: { id: string; title: string; meta?: ReactNode }) {
    return (
        <div className="flex flex-wrap items-baseline gap-x-2.5 gap-y-1.5">
            <h2 id={id} className="m-0 text-lg font-semibold text-ink">{title}</h2>
            {meta !== undefined && <span className="text-[13px] text-muted">{meta}</span>}
        </div>
    );
}

/**
 * Confirmed (redesign-you v1.0.0 rule 5): what the person said Yes to,
 * edited or said themselves, each with how, and no answer buttons. How to
 * work with me is a group inside it (rule 8).
 */
export function ConfirmedSection({
    rows,
    workWithMe,
    onWhy,
    onOpen,
}: {
    rows: ClaimRow[];
    /** How to work with me: confirmed claims about working with the person. */
    workWithMe?: { title: string; rows: ClaimRow[] };
    onWhy?: (id: string) => void;
    onOpen?: (id: string) => void;
}) {
    const { t } = useTranslation('memory');
    const total = rows.length + (workWithMe?.rows.length ?? 0);
    if (total === 0) return null;
    const line = (row: ClaimRow) => (
        <div key={row.id} data-testid="confirmed-row" className={CLAIM_ROW_CLASS}>
            <span className="flex flex-wrap items-baseline gap-x-1.5">
                <ConfirmedHowLine belief={row.belief} />
                {(row.where || onWhy) && <span aria-hidden="true" className="text-xs text-muted">·</span>}
                <WhyLine kind="claim" where={row.where} onWhy={onWhy ? () => onWhy(row.id) : undefined} />
            </span>
            {onOpen ? (
                <button type="button" onClick={() => onOpen(row.id)} className="m-0 cursor-pointer border-0 bg-transparent p-0 text-left text-sm leading-[1.45] text-ink break-words hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-border-focus">
                    {row.text}
                </button>
            ) : (
                <p className="m-0 text-sm leading-[1.45] text-ink break-words">{row.text}</p>
            )}
        </div>
    );
    return (
        <section aria-labelledby="about-you-confirmed" data-testid="about-you-confirmed" className="flex flex-col gap-3">
            <SectionHead id="about-you-confirmed" title={t('aboutYou.confirmed')} meta={total} />
            {workWithMe && workWithMe.rows.length > 0 && (
                <div data-testid="work-with-me" className="flex flex-col gap-0.5">
                    <h3 className="m-0 pb-1 text-[11px] font-semibold uppercase tracking-[0.08em] text-muted/80">{workWithMe.title}</h3>
                    <div className="-mx-3 flex flex-col gap-0.5">{workWithMe.rows.map(line)}</div>
                </div>
            )}
            {rows.length > 0 && <div className="-mx-3 flex flex-col gap-0.5">{rows.map(line)}</div>}
        </section>
    );
}

/**
 * Not right (redesign-you v1.0.0 rule 6): every No, kept so the claim isn't
 * guessed again, folded to one line with Show. Undo moves one back to be
 * checked again.
 */
export function NotRightSection({ rows, onUndone }: { rows: ClaimRow[]; onUndone: () => void }) {
    const { t, i18n } = useTranslation('memory');
    const [open, setOpen] = useState(false);
    const [undoing, setUndoing] = useState<Record<string, 'busy' | 'failed'>>({});
    if (rows.length === 0) return null;
    const folded = rows.slice(0, 2).map((row) => row.text);
    const rest = rows.length - folded.length;
    const undo = (id: string) => {
        setUndoing((prev) => ({ ...prev, [id]: 'busy' }));
        UndoNotQuiteMemoryEntry(id, 0)
            .then(() => {
                setUndoing((prev) => {
                    const next = { ...prev };
                    delete next[id];
                    return next;
                });
                onUndone();
            })
            .catch(() => setUndoing((prev) => ({ ...prev, [id]: 'failed' })));
    };
    return (
        <section aria-labelledby="about-you-not-right" data-testid="about-you-not-right" className="flex flex-col gap-2">
            <SectionHead id="about-you-not-right" title={t('aboutYou.notRight')} meta={t('aboutYou.notRightMeta', { count: rows.length })} />
            {!open ? (
                <p data-testid="not-right-folded" className="m-0 flex flex-wrap items-baseline gap-x-1.5 text-[13px] text-muted">
                    <span className="min-w-0">
                        {folded.join(' · ')}
                        {rest > 0 && ` · ${t('aboutYou.notRightMore', { count: rest })}`}
                    </span>
                    <Button size="sm" variant="link" className="h-auto px-0" onClick={() => setOpen(true)}>{t('aboutYou.show')}</Button>
                </p>
            ) : (
                <>
                    <div className="-mx-3 flex flex-col gap-0.5">
                        {rows.map((row) => (
                            <div key={row.id} data-testid="not-right-row" className={CLAIM_ROW_CLASS}>
                                <p className="m-0 text-sm leading-[1.45] text-muted break-words">{row.text}</p>
                                <span className="flex flex-wrap items-center gap-x-1.5 text-xs text-muted">
                                    {(row.belief?.notQuiteAt ?? 0) > 0 && <span>{answerWhen(t, i18n.language, row.belief!.notQuiteAt!)}</span>}
                                    {(row.belief?.notQuiteAt ?? 0) > 0 && <span aria-hidden="true">·</span>}
                                    <UndoLink onClick={() => undo(row.id)} />
                                    {undoing[row.id] === 'failed' && <span role="alert" className="text-copper">{t('aboutYou.undoFailed')}</span>}
                                </span>
                            </div>
                        ))}
                    </div>
                    <Button size="sm" variant="link" className="h-auto self-start px-0" onClick={() => setOpen(false)}>{t('aboutYou.hide')}</Button>
                </>
            )}
        </section>
    );
}
