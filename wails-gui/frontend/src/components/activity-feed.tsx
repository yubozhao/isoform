import { useCallback, useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';
import { ConfirmDialog } from '@/components/confirm-dialog';
import { ActivityInlineNotice } from '@/components/activity-inline-notice';
import { formatDurationMs } from '@/components/timeline/useCalendarData';
import {
    ForgetActivityFeedRow,
    GetActivityFeed,
    GetActivityForgetPreview,
    type ActivityFeedKind,
    type ActivityFeedRow,
    type ActivityForgetPreview,
} from '@/lib/wails-shim-app';

// Activity's one timeline (computer-activity v1.0.0, rules 3 to 8): what
// Isoform saw in the day across the screen, the watch and your agents,
// newest first. A row is its time, a dot in its source's colour, the source,
// what it was and a line under it, and Forget, which first says what goes
// with it. Closed Eyes is a striped gap with nothing else about that time.
// The source is the dot and its name, never a bar at the row's left edge.

// Your agents' hand-offs carry the purple-blue of Your agents; what an agent
// did on this computer is screen, so it takes the screen's colour
// (computer-activity v1.2.0 rule 4).
const DOT_CLASS: Record<Exclude<ActivityFeedKind, 'closed'>, string> = {
    screen: 'bg-seaweed',
    watch: 'bg-copper',
    agent: 'bg-seaweed',
    handoff: 'bg-accent',
};

function clock(ms: number, lang: string): string {
    return new Date(ms).toLocaleTimeString(lang, { hour: 'numeric', minute: '2-digit' });
}

function dayBounds(date: Date): [number, number] {
    const start = new Date(date.getFullYear(), date.getMonth(), date.getDate()).getTime();
    const end = new Date(date.getFullYear(), date.getMonth(), date.getDate() + 1).getTime();
    return [start, end];
}

export function feedSource(t: TFunction<'activity'>, row: ActivityFeedRow): string {
    switch (row.kind) {
        case 'screen':
            return row.app ? t('feed.sourceScreenApp', { app: row.app }) : t('feed.sourceScreen');
        case 'watch':
            return row.device === 'phone' ? t('feed.sourcePhone') : t('feed.sourceWatch');
        case 'agent':
        case 'handoff':
            return row.app ? t('feed.sourceAgentApp', { app: row.app }) : t('feed.sourceAgent');
        default:
            return '';
    }
}

export function feedTitle(t: TFunction<'activity'>, row: ActivityFeedRow): string {
    const length = formatDurationMs(Math.max(0, row.endMs - row.startMs));
    switch (row.kind) {
        case 'screen':
            return row.title || t('feed.untitledMoment');
        case 'watch':
            return t('feed.watchTitle', { length });
        case 'agent':
            return t('feed.agentTitle', { length: formatDurationMs(row.activeMs || row.endMs - row.startMs) });
        case 'handoff':
            return row.title || t('feed.untitledHandoff');
        default:
            return '';
    }
}

function handoffLine(t: TFunction<'activity'>, row: ActivityFeedRow, lang: string): string {
    const given = row.surface === 'phone' ? t('act.handedOverPhone') : t('act.handedOverYes');
    switch (row.outcome) {
        case 'done':
            return row.doneMs ? t('act.handedOver', { given, time: clock(row.doneMs, lang) }) : t('act.outcomeLine', { given, outcome: t('act.outcome.done') });
        case 'taken':
            return t('act.outcomeLine', { given, outcome: t('act.outcome.taken', { agent: row.app }) });
        case 'not_done':
            return t('act.outcomeLine', { given, outcome: t('act.outcome.notDone', { agent: row.app }) });
        case 'no_word':
            return t('act.outcomeLine', { given, outcome: t('act.outcome.noWord') });
        default:
            return given;
    }
}

export function feedLine(t: TFunction<'activity'>, row: ActivityFeedRow, lang = 'en'): string {
    switch (row.kind) {
        case 'handoff':
            return handoffLine(t, row, lang);
        case 'screen':
            return row.detail ?? '';
        case 'watch':
            return row.words ? t('feed.watchWords', { count: row.words }) : t('feed.watchNoWords');
        case 'agent':
            return t('feed.agentLine');
        default:
            return '';
    }
}

/** What Forget's one confirmation says (computer-activity v1.1.0, 15e):
 *  the title, what the row is with its source and time, everything made
 *  from it with where it lives, and the count on the button. The row's own
 *  words, audio and copies are the thing itself, said by the closing line. */
export interface ForgetPlan {
    title: string;
    lead: string;
    items: string[];
    count: number;
}

export function forgetPlan(t: TFunction<'activity'>, preview: ActivityForgetPreview, row: ActivityFeedRow, lang: string): ForgetPlan {
    if (row.kind === 'handoff') {
        // Isoform's record goes; what the agent made stays with it (v1.2.0 rule 3).
        const agent = preview.agent || row.app || '';
        return { title: t('act.forgetHandoff', { title: preview.title || feedTitle(t, row), agent }), lead: '', items: [], count: 1 };
    }
    const what = row.kind === 'screen' ? (preview.title || feedTitle(t, row)) : feedTitle(t, row);
    const items: string[] = [];
    for (const item of preview.goes) {
        switch (item.kind) {
            case 'topic':
                items.push(t('act.forgetItem', { what: t('feed.goes.topic', { topic: item.title }), where: t('feed.where.topics') }));
                break;
            case 'goal':
                items.push(t('act.forgetItem', { what: item.title || t('feed.goes.goalUntitled'), where: t('feed.where.goals') }));
                break;
            case 'screen':
                items.push(t('act.forgetItem', { what: t('feed.goes.screen', { start: clock(item.startMs ?? 0, lang), end: clock(item.endMs ?? 0, lang) }), where: t('feed.where.screen') }));
                break;
            case 'moment':
                items.push(t('act.forgetItem', { what: t('feed.goes.moment', { moment: item.title }), where: t('feed.where.activity') }));
                break;
            case 'notes':
                items.push(t('act.forgetItem', { what: t('feed.goes.notes'), where: t('feed.where.watch') }));
                break;
            default:
                break;
        }
    }
    const kind = t('act.forgetKind', { source: feedSource(t, row), time: clock(row.startMs, lang) });
    return {
        title: t('act.forgetTitle', { what }),
        lead: items.length > 0 ? t('act.forgetLead', { what: kind }) : t('act.forgetLeadOnly', { what: kind }),
        items,
        count: items.length + 1,
    };
}

export function ActivityFeed({ date, kinds, emptyText }: {
    date: Date;
    /** The sources this tab shows; all of them when left out. */
    kinds?: ActivityFeedKind[];
    emptyText?: string;
}) {
    const { t, i18n } = useTranslation('activity');
    const [rows, setRows] = useState<ActivityFeedRow[] | null>(null);
    const [failed, setFailed] = useState(false);
    const [forgetting, setForgetting] = useState<{ row: ActivityFeedRow; preview: ActivityForgetPreview } | null>(null);
    const [forgetFailed, setForgetFailed] = useState(false);
    // The row's Forget that opened the confirm gets focus back when it closes.
    const forgetOpener = useRef<HTMLButtonElement | null>(null);
    const [start, end] = dayBounds(date);
    const kindsKey = (kinds ?? []).join(',');

    const load = useCallback(() => {
        GetActivityFeed(start, end)
            .then((list) => {
                const wanted = kindsKey ? new Set(kindsKey.split(',')) : null;
                setRows((list ?? []).filter((row) => !wanted || wanted.has(row.kind)));
                setFailed(false);
            })
            .catch(() => setFailed(true));
    }, [start, end, kindsKey]);

    useEffect(() => {
        setRows(null);
        load();
        window.addEventListener('focus', load);
        return () => window.removeEventListener('focus', load);
    }, [load]);

    const askForget = (row: ActivityFeedRow) => {
        setForgetFailed(false);
        GetActivityForgetPreview(row.kind, row.id)
            .then((preview) => setForgetting({ row, preview }))
            .catch(() => setForgetFailed(true));
    };

    const confirmForget = () => {
        if (!forgetting) return;
        const { row } = forgetting;
        ForgetActivityFeedRow(row.kind, row.id)
            .then(() => {
                setRows((prev) => (prev ?? []).filter((r) => !(r.kind === row.kind && r.id === row.id)));
                setForgetting(null);
                load();
            })
            .catch(() => {
                setForgetting(null);
                setForgetFailed(true);
            });
    };

    const plan = forgetting ? forgetPlan(t, forgetting.preview, forgetting.row, i18n.language) : null;

    return (
        <section data-testid="activity-feed" aria-label={t('feed.label') as string}>
            {failed && <ActivityInlineNotice notice={{ tone: 'error', text: t('feed.loadFailed'), onAction: load }} />}
            {forgetFailed && <ActivityInlineNotice notice={{ tone: 'error', text: t('feed.forgetFailed') }} />}
            {rows !== null && rows.length === 0 && !failed && (
                <p className="px-1 text-[12.5px] text-muted">{emptyText ?? t('feed.empty')}</p>
            )}
            <ol className="flex flex-col">
                {(rows ?? []).map((row) =>
                    row.kind === 'closed' ? (
                        <li
                            key={`closed-${row.startMs}`}
                            data-feed-kind="closed"
                            className="my-1 rounded-md px-3 py-2 text-[12px] text-muted"
                            style={{ backgroundImage: 'repeating-linear-gradient(135deg, var(--color-border) 0 1px, transparent 1px 7px)' }}
                        >
                            {t('act.closedGap', { start: clock(row.startMs, i18n.language), end: clock(row.endMs, i18n.language) })}
                        </li>
                    ) : (
                        <li
                            key={`${row.kind}-${row.id}`}
                            data-feed-kind={row.kind}
                            className="group flex items-start gap-3 rounded-md px-3 py-2 hover:bg-surface-hover transition-colors"
                        >
                            <span className="w-[62px] shrink-0 pt-px text-[12px] tabular-nums text-muted">{clock(row.startMs, i18n.language)}</span>
                            <span aria-hidden className={`mt-[6px] size-[7px] shrink-0 rounded-full ${DOT_CLASS[row.kind]}`} />
                            <div className="min-w-0 flex-1">
                                <p className="text-[11px] text-muted">{feedSource(t, row)}</p>
                                <p className="text-[13px] text-ink line-clamp-2 break-words">{feedTitle(t, row)}</p>
                                {feedLine(t, row, i18n.language) && <p className="text-[12px] text-muted line-clamp-2">{feedLine(t, row, i18n.language)}</p>}
                            </div>
                            <button
                                type="button"
                                onClick={(event) => { forgetOpener.current = event.currentTarget; askForget(row); }}
                                aria-label={t('feed.forgetRow', { title: feedTitle(t, row) })}
                                className="shrink-0 rounded-md px-2 py-1 text-[12px] text-muted hover:text-ink hover:bg-surface-active focus-visible:ring-2 focus-visible:ring-border-focus transition-colors"
                            >
                                {t('feed.forget')}
                            </button>
                        </li>
                    ),
                )}
            </ol>
            <ConfirmDialog
                open={forgetting !== null}
                onOpenChange={(open) => { if (!open) setForgetting(null); }}
                title={plan?.title ?? ''}
                description={plan ? (
                    <div className="flex flex-col gap-2" data-testid="forget-plan">
                        {plan.lead && <p className="m-0">{plan.lead}</p>}
                        {plan.items.length > 0 && (
                            <ul className="m-0 flex list-disc flex-col gap-1 pl-5 text-ink">
                                {plan.items.map((item) => <li key={item}>{item}</li>)}
                            </ul>
                        )}
                        <p className="m-0">{t('act.forgetGone')}</p>
                    </div>
                ) : ''}
                confirmLabel={plan && plan.count > 1 ? t('act.forgetAll', { n: plan.count }) : t('act.forgetOne')}
                variant="danger"
                focusCancel
                returnFocusTo={forgetOpener.current}
                onConfirm={confirmForget}
                closeOnConfirm={false}
            />
        </section>
    );
}
