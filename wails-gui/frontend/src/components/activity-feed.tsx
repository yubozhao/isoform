import { useCallback, useEffect, useState } from 'react';
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

const DOT_CLASS: Record<Exclude<ActivityFeedKind, 'closed'>, string> = {
    screen: 'bg-info',
    watch: 'bg-copper',
    agent: 'bg-success',
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
        default:
            return '';
    }
}

export function feedLine(t: TFunction<'activity'>, row: ActivityFeedRow): string {
    switch (row.kind) {
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

/** The sentence Forget shows before the person confirms. */
export function forgetBody(t: TFunction<'activity'>, preview: ActivityForgetPreview, row: ActivityFeedRow, lang: string): string {
    const thing = row.kind === 'screen' ? (preview.title || feedTitle(t, row)) : feedTitle(t, row);
    const list = preview.goes.map((item) => {
        switch (item.kind) {
            case 'topic':
                return t('feed.goes.topic', { topic: item.title });
            case 'goal':
                return item.title ? t('feed.goes.goal', { goal: item.title }) : t('feed.goes.goalUntitled');
            case 'screen':
                return t('feed.goes.screen', { start: clock(item.startMs ?? 0, lang), end: clock(item.endMs ?? 0, lang) });
            case 'moment':
                return t('feed.goes.moment', { moment: item.title });
            case 'words':
                return t('feed.goes.words');
            case 'audio':
                return t('feed.goes.audio');
            case 'copies':
                return t('feed.goes.copies');
            default:
                return t('feed.goes.notes');
        }
    });
    if (list.length === 0) return t('feed.forgetBodyNothing', { thing });
    return t('feed.forgetBody', { thing, count: list.length, list: list.join(t('feed.listSeparator')) });
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
                                <p className="text-[13px] text-ink truncate">{feedTitle(t, row)}</p>
                                {feedLine(t, row) && <p className="text-[12px] text-muted line-clamp-2">{feedLine(t, row)}</p>}
                            </div>
                            <button
                                type="button"
                                onClick={() => askForget(row)}
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
                title={t('feed.forgetTitle')}
                description={forgetting ? forgetBody(t, forgetting.preview, forgetting.row, i18n.language) : ''}
                confirmLabel={t('feed.forgetConfirm')}
                variant="danger"
                onConfirm={confirmForget}
                closeOnConfirm={false}
            />
        </section>
    );
}
