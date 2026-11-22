import { useCallback, useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Sheet, SheetCloseButton, SheetContent, SheetDescription, SheetHeader, SheetTitle } from '@/components/ui/sheet';
import { ScrollArea } from '@/components/ui/scroll-area';
import { Button } from '@/components/ui/button';
import { cn } from '@/lib/utils';
import { GetWhy, NotThis, type WhyKind, type WhyMoment, type WhyView } from '@/lib/wails-shim-app';
import { WHY_DOT_CLASS, whySummaryText } from '@/components/why/why-copy';

type Load = { state: 'loading' } | { state: 'failed' } | { state: 'ready'; view: WhyView };

/**
 * Why (redesign v1.0.0 rule 16): the moments a claim or a piece of work came
 * from, by day and source, each with Not this. The one Why every page uses
 * (About you, Find, Chat, Today), as a right-edge Sheet. Titles only, never
 * captured text; the backend leaves out Closed Eyes and protected apps.
 */
export function WhySheet({
    kind,
    id,
    open,
    onOpenChange,
    onOpenMoment,
    onChanged,
}: {
    kind: WhyKind;
    id: string;
    open: boolean;
    onOpenChange: (open: boolean) => void;
    /** Opens the moment where it happened (Activity). Absent, moments don't open. */
    onOpenMoment?: (moment: WhyMoment) => void;
    /** A Not this changed what the item stands on: re-read the page. */
    onChanged?: () => void;
}) {
    const { t, i18n } = useTranslation('common');
    const [load, setLoad] = useState<Load>({ state: 'loading' });
    const [removing, setRemoving] = useState<Record<string, 'busy' | 'failed'>>({});
    const read = useRef(0);

    const fetchWhy = useCallback(() => {
        const current = ++read.current;
        setLoad({ state: 'loading' });
        GetWhy(kind, id)
            .then((view) => { if (current === read.current) setLoad({ state: 'ready', view }); })
            .catch(() => { if (current === read.current) setLoad({ state: 'failed' }); });
    }, [id, kind]);

    useEffect(() => {
        if (!open) return;
        setRemoving({});
        fetchWhy();
        return () => { read.current++; };
    }, [fetchWhy, open]);

    const notThis = (moment: WhyMoment) => {
        setRemoving((prev) => ({ ...prev, [moment.id]: 'busy' }));
        NotThis(kind, id, moment.id)
            .then(() => {
                setRemoving((prev) => {
                    const next = { ...prev };
                    delete next[moment.id];
                    return next;
                });
                setLoad((prev) => {
                    if (prev.state !== 'ready') return prev;
                    const days = prev.view.days
                        .map((day) => ({ ...day, moments: day.moments.filter((row) => row.id !== moment.id) }))
                        .filter((day) => day.moments.length > 0);
                    const bySource = { ...prev.view.summary.bySource };
                    bySource[moment.source] = Math.max(0, (bySource[moment.source] ?? 1) - 1);
                    return { state: 'ready', view: { ...prev.view, days, summary: { ...prev.view.summary, total: Math.max(0, prev.view.summary.total - 1), bySource } } };
                });
                onChanged?.();
            })
            .catch(() => setRemoving((prev) => ({ ...prev, [moment.id]: 'failed' })));
    };

    const dayLabel = (day: string) => {
        if (!day) return t('why.day.undated');
        const [y, m, d] = day.split('-').map(Number);
        const date = new Date(y, m - 1, d);
        const today = new Date();
        const start = new Date(today.getFullYear(), today.getMonth(), today.getDate()).getTime();
        const days = Math.round((start - date.getTime()) / 86_400_000);
        if (days === 0) return t('why.day.today');
        if (days === 1) return t('why.day.yesterday');
        return new Intl.DateTimeFormat(i18n.language, { weekday: 'long', month: 'short', day: 'numeric' }).format(date);
    };
    const clock = (at: number) => new Date(at).toLocaleTimeString(i18n.language, { hour: 'numeric', minute: '2-digit' });

    const summary = load.state === 'ready' ? whySummaryText(t, i18n.language, load.view.summary) : '';
    return (
        <Sheet open={open} onOpenChange={onOpenChange}>
            <SheetContent data-testid="why-sheet" aria-describedby={undefined}>
                <SheetHeader>
                    <div className="flex min-w-0 flex-col gap-1">
                        <SheetTitle>{t('why.title')}</SheetTitle>
                        {summary && <SheetDescription>{summary}</SheetDescription>}
                    </div>
                    <SheetCloseButton />
                </SheetHeader>
                <ScrollArea className="min-h-0 flex-1">
                    <div className="flex flex-col gap-5 px-6 pb-6">
                        {load.state === 'loading' && (
                            <div aria-busy="true" className="flex flex-col gap-2.5">
                                {['70%', '85%', '60%'].map((width) => (
                                    <span key={width} className="block h-3 rounded-md bg-surface-active motion-safe:animate-pulse" style={{ width }} />
                                ))}
                            </div>
                        )}
                        {load.state === 'failed' && (
                            <div role="alert" className="flex items-center gap-3 rounded-xl border border-border bg-panel px-4 py-3.5 text-[13px] text-copper">
                                <span className="flex-1">{t('why.failed')}</span>
                                <Button size="sm" variant="secondary" onClick={fetchWhy}>{t('why.retry')}</Button>
                            </div>
                        )}
                        {load.state === 'ready' && load.view.days.length === 0 && (
                            <p data-testid="why-empty" className="m-0 text-[13px] text-muted">
                                {load.view.summary.importedFrom ? t('why.importedEmpty', { label: load.view.summary.importedFrom }) : t('why.empty')}
                            </p>
                        )}
                        {load.state === 'ready' && load.view.days.map((day) => (
                            <section key={day.day || 'undated'} data-testid="why-day" className="flex flex-col gap-1">
                                <h3 className="m-0 pb-1 text-[11px] font-semibold uppercase tracking-[0.08em] text-muted/80">{dayLabel(day.day)}</h3>
                                {day.moments.map((moment) => {
                                    const state = removing[moment.id];
                                    const opens = !!onOpenMoment && !!(moment.activitySessionId || moment.feedRowId);
                                    const what = moment.title || t('why.untitled', { app: moment.app || t(`why.source.${moment.source}`) });
                                    const body = (
                                        <>
                                            <span aria-hidden data-why-dot className={cn('mt-[6px] size-[7px] shrink-0 rounded-full', WHY_DOT_CLASS[moment.source])} />
                                            <span className="flex min-w-0 flex-1 flex-col">
                                                <span className="text-[13.5px] leading-[1.45] text-ink break-words">{what}</span>
                                                <span className="text-xs text-muted">
                                                    {[moment.at > 0 ? clock(moment.at) : '', t(`why.source.${moment.source}`), moment.app && moment.title ? moment.app : ''].filter(Boolean).join(' · ')}
                                                </span>
                                            </span>
                                        </>
                                    );
                                    return (
                                        <div key={moment.id} data-testid="why-moment" className="flex items-start gap-2 rounded-lg px-2 py-1.5 hover:bg-surface-hover">
                                            {opens ? (
                                                <button
                                                    type="button"
                                                    onClick={() => onOpenMoment?.(moment)}
                                                    className="flex min-w-0 flex-1 cursor-pointer items-start gap-2.5 rounded-md border-0 bg-transparent p-0 text-left focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-border-focus"
                                                >
                                                    {body}
                                                </button>
                                            ) : (
                                                <span className="flex min-w-0 flex-1 items-start gap-2.5">{body}</span>
                                            )}
                                            {moment.canNotThis && (
                                                <span className="flex flex-none flex-col items-end gap-0.5">
                                                    <button
                                                        type="button"
                                                        disabled={state === 'busy'}
                                                        aria-busy={state === 'busy'}
                                                        aria-label={t('why.notThisLabel', { what })}
                                                        onClick={() => notThis(moment)}
                                                        className="cursor-pointer rounded-sm border-0 bg-transparent px-1 text-xs text-muted hover:text-ink focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-border-focus disabled:cursor-default"
                                                    >
                                                        {t('why.notThis')}
                                                    </button>
                                                    {state === 'failed' && <span role="alert" className="text-xs text-copper">{t('why.notThisFailed')}</span>}
                                                </span>
                                            )}
                                        </div>
                                    );
                                })}
                            </section>
                        ))}
                    </div>
                </ScrollArea>
            </SheetContent>
        </Sheet>
    );
}
