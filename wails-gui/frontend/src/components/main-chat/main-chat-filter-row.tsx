import { useLayoutEffect, useMemo, useRef, useState } from 'react';
import { ChevronDown } from 'lucide-react';
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from '@/components/ui/dropdown-menu';
import { useTranslation } from 'react-i18next';
import { Button, buttonVariants } from '@/components/ui/button';
import { cn } from '@/lib/utils';
import { chatThreadUpdates, type MainChatFilter } from '@/lib/main-chat-view';
import { orderedDisplayTopics, topicCards, type TodayTopic } from '@/components/today/topics';

/**
 * The main chat's filter row (RC 2 D1), above the scroll area: All, then the
 * work topics, and "Hide folded
 * updates" on the right. A filter changes only what is shown.
 */
export function MainChatFilterRow({
    filter,
    onFilter,
    topics,
    hideRoutine,
    routineCount,
    hideBusy,
    hideFailed,
    onToggleHide,
}: {
    filter: MainChatFilter;
    onFilter: (filter: MainChatFilter) => void;
    topics: readonly TodayTopic[];
    hideRoutine: boolean;
    /** Today's routine updates, for "Show N folded updates". */
    routineCount: number | null;
    hideBusy: boolean;
    hideFailed: boolean;
    onToggleHide: (hide: boolean) => void;
}) {
    const { t } = useTranslation('chat');
    // The same topics, with the same names, as Today's cards (today-review v1.1.4 #8).
    const ordered = useMemo(() => topicCards(orderedDisplayTopics(topics), new Date()), [topics]);
    const rowRef = useRef<HTMLDivElement>(null);
    const fixedRef = useRef<HTMLDivElement>(null);
    const hideRef = useRef<HTMLDivElement>(null);
    const measureRef = useRef<HTMLDivElement>(null);
    const [visibleCount, setVisibleCount] = useState(ordered.length);
    useLayoutEffect(() => {
        const row = rowRef.current;
        if (!row) return;
        const measure = () => {
            const width = row.getBoundingClientRect().width;
            // Non-layout renderers keep every topic accessible. WebView
            // measures after layout and again on font or column changes.
            if (!width) { setVisibleCount(ordered.length); return; }
            const style = getComputedStyle(row);
            const gap = parseFloat(style.columnGap) || 6;
            const padding = (parseFloat(style.paddingLeft) || 0) + (parseFloat(style.paddingRight) || 0);
            const available = width - padding - (fixedRef.current?.getBoundingClientRect().width ?? 0) - (hideRef.current?.getBoundingClientRect().width ?? 0) - gap * 2;
            const samples = Array.from(measureRef.current?.children ?? []);
            const widths = samples.slice(1).map(el => el.getBoundingClientRect().width + gap);
            if (Math.max(0, widths.reduce((sum, value) => sum + value, 0) - gap) <= available) { setVisibleCount(ordered.length); return; }
            let left = available - (samples[0]?.getBoundingClientRect().width ?? 0);
            let count = 0;
            for (const width of widths) {
                if (width > left) break;
                left -= width;
                count++;
            }
            setVisibleCount(count);
        };
        measure();
        if (typeof ResizeObserver === 'undefined') return;
        const observer = new ResizeObserver(measure);
        [row, fixedRef.current, hideRef.current, measureRef.current].forEach(el => { if (el) observer.observe(el); });
        return () => observer.disconnect();
    }, [ordered, t, hideRoutine, routineCount, hideFailed, filter.kind]);
    const pill = (key: string, label: string, on: boolean, next: MainChatFilter) => (
        <Button
            key={key}
            variant="ghost"
            size="sm"
            aria-pressed={on}
            title={label}
            onClick={() => onFilter(next)}
            className={cn('min-w-0 max-w-[220px] font-medium', on && 'bg-surface-active')}
            data-main-chat-filter={key}
        >
            <span className="truncate">{label}</span>
        </Button>
    );
    const isTopic = (id: string) => filter.kind === 'topic' && filter.topicId === id;
    return (
        <div className="window-drag sticky top-0 shrink-0 border-b border-border-subtle" data-testid="main-chat-filter-row">
            <div ref={rowRef} data-header-row className="relative flex flex-nowrap items-center gap-1.5 px-4 py-3">
                <div ref={fixedRef} data-header-fixed className="flex min-w-0 max-w-[55%] items-center gap-1.5">
                    <h2 className="mr-1 shrink-0 text-[14px] font-semibold text-ink">{t('mainChat.view.title')}</h2>
                    {pill('all', t('mainChat.view.everything'), filter.kind === 'all', { kind: 'all' })}
                </div>
                <div role="group" aria-label={t('mainChat.view.filterLabel')} className={cn('flex flex-1 flex-nowrap items-center gap-1.5', visibleCount < ordered.length ? 'min-w-max' : 'min-w-0')}>
                    {ordered.slice(0, visibleCount).map((topic) => pill(`topic:${topic.id}`, topic.name, isTopic(topic.id), { kind: 'topic', topicId: topic.id }))}
                    {visibleCount < ordered.length && (
                        <DropdownMenu>
                            <DropdownMenuTrigger asChild>
                                <Button size="sm" variant="ghost" className="shrink-0" aria-label={t('mainChat.view.more')}>
                                    {t('mainChat.view.more')}<ChevronDown className="size-3" aria-hidden />
                                </Button>
                            </DropdownMenuTrigger>
                            <DropdownMenuContent align="start">
                                {ordered.slice(visibleCount).map(topic => (
                                    <DropdownMenuItem key={topic.id} data-topic-id={topic.id} aria-current={isTopic(topic.id) ? "true" : undefined} className={cn(isTopic(topic.id) && 'bg-surface-active')} onSelect={() => onFilter({ kind: 'topic', topicId: topic.id })}>
                                        {topic.name}
                                    </DropdownMenuItem>
                                ))}
                            </DropdownMenuContent>
                        </DropdownMenu>
                    )}
                </div>
                {chatThreadUpdates.shown && <div ref={hideRef} data-header-hide className="ml-auto flex min-w-0 max-w-[25%] items-center gap-2">
                    {hideFailed && <span role="alert" className="truncate text-[12px] text-subtle" title={t('failure.save')}>{t('failure.save')}</span>}
                    {filter.kind === 'all' ? (
                        <Button
                            variant="quiet"
                            size="sm"
                            disabled={hideBusy}
                            onClick={() => onToggleHide(!hideRoutine)}
                            className="min-w-0"
                            data-testid="main-chat-hide-routine"
                        >
                            <span className="truncate">{hideRoutine ? t('mainChat.view.showUpdates') : t('mainChat.view.hideUpdates')}</span>
                        </Button>
                    ) : (
                        <span className="truncate text-[12.5px] text-muted" title={t('mainChat.view.hiddenWhileFiltered')}>{t('mainChat.view.hiddenWhileFiltered')}</span>
                    )}
                </div>}
                <div ref={measureRef} aria-hidden="true" className="pointer-events-none invisible absolute flex h-0 gap-1.5 overflow-hidden">
                    <span className={buttonVariants({ variant: 'ghost', size: 'sm', className: 'shrink-0' })}>{t('mainChat.view.more')}<ChevronDown className="size-3" /></span>
                    {ordered.map(topic => <span key={topic.id} data-topic-measure className={buttonVariants({ variant: 'ghost', size: 'sm', className: 'max-w-[220px] shrink-0 font-medium' })}><span className="truncate">{topic.name}</span></span>)}
                </div>
            </div>
        </div>
    );
}

/** Today's sourced counts: conversations, finished, routine. The summary
 *  scrolls with the cards. */
export function MainChatSummary({
    counts,
    failed,
    onRetry,
}: {
    counts: { conversations: number | null; finished: number | null; routine: number | null } | null;
    failed: boolean;
    onRetry: () => void;
}) {
    const { t } = useTranslation('chat');
    if (!counts) {
        if (!failed) return null;
        return (
            <div className="flex flex-nowrap items-center gap-2 overflow-x-auto whitespace-nowrap rounded-lg bg-panel-warm px-3.5 py-2.5 text-[12.5px] text-muted" data-testid="main-chat-summary">
                <span role="alert" className="shrink-0">{t('mainChat.view.summaryFailed')}</span>
                <Button variant="link" className="h-auto shrink-0 p-0 text-[12.5px] font-medium" onClick={onRetry}>
                    {t('mainChat.view.retry')}
                </Button>
            </div>
        );
    }
    const items = [
        { key: 'conversations', text: counts.conversations !== null && counts.conversations > 0 ? t('mainChat.view.summaryConversations', { count: counts.conversations }) : null },
        { key: 'finished', text: counts.finished !== null && counts.finished > 0 ? t('mainChat.view.summaryFinished', { count: counts.finished }) : null },
        { key: 'routine', text: counts.routine !== null && counts.routine > 0 ? t('mainChat.view.summaryRoutine', { count: counts.routine }) : null },
    ].filter((item) => item.text !== null);
    return (
        <div
            className="overflow-x-auto whitespace-nowrap rounded-lg bg-panel-warm px-3.5 py-2.5 text-[12.5px] text-muted"
            data-testid="main-chat-summary"
        >
            <span className="font-semibold text-ink">{t('mainChat.view.summaryTitle')}</span>
            <span data-testid="main-chat-summary-counts">
                {items.map((item) => (
                    <span key={item.key}>
                        {t('mainChat.view.summarySeparator')}
                        <span>{item.text}</span>
                    </span>
                ))}
            </span>
        </div>
    );
}
