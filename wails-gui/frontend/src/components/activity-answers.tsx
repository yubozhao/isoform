import { useCallback, useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';
import { ActivityInlineNotice } from '@/components/activity-inline-notice';
import { ConfirmDialog } from '@/components/confirm-dialog';
import { cn } from '@/lib/utils';
import {
    ForgetAnswer,
    GetAnswers,
    GetJudgmentStats,
    type AnswerView,
    type JudgmentCounts,
    type JudgmentStats,
} from '@/lib/wails-shim-app';

// Your answers (redesign-activity v1.0.0 rules 8 to 13; computer-activity
// v1.0.0 tab 5), read from the answers ledger (answers_ledger.go):
// today's four numbers, where yes comes from over 14 days, and every answer
// newest first with Forget. A mirror, not a target: no streaks, no goals, no
// score, and none of it on Today. Counted on this computer.

const PAGE = 50;
const SOURCE_DAYS = 14;
/** The ledger's sources, in the order Where yes comes from lists them. */
export const ANSWER_SOURCES = ['prep', 'brief', 'topic', 'agent', 'about_you', 'chat', 'other'] as const;

const EMPTY: JudgmentCounts = { shown: 0, answered: 0, accepted: 0, declined: 0, notMine: 0, useful: 0, notUseful: 0, undone: 0 };

/** Each source's yes rate over the days given: said yes (or useful) of the answers that stand. */
export function sourceRates(stats: JudgmentStats | null): { source: string; yes: number; answered: number }[] {
    const sums = new Map<string, { yes: number; answered: number }>();
    for (const day of stats?.days ?? []) {
        for (const [source, counts] of Object.entries(day.bySource ?? {}) as [string, JudgmentCounts][]) {
            const key = (ANSWER_SOURCES as readonly string[]).includes(source) ? source : 'other';
            const sum = sums.get(key) ?? { yes: 0, answered: 0 };
            sum.yes += counts.accepted + counts.useful;
            sum.answered += counts.answered;
            sums.set(key, sum);
        }
    }
    return ANSWER_SOURCES.filter((source) => (sums.get(source)?.answered ?? 0) > 0).map((source) => ({ source, ...sums.get(source)! }));
}

function sourceLabel(t: TFunction<'activity'>, source: string): string {
    switch (source) {
        case 'prep':
            return t('ans.source.prep');
        case 'brief':
            return t('ans.source.brief');
        case 'topic':
            return t('ans.source.topic');
        case 'agent':
            return t('ans.source.agent');
        case 'about_you':
            return t('ans.source.about_you');
        case 'chat':
            return t('ans.source.chat');
        default:
            return t('ans.source.other');
    }
}

function dayKey(at: Date): string {
    return `${at.getFullYear()}-${at.getMonth()}-${at.getDate()}`;
}

export function ActivityAnswers() {
    const { t, i18n } = useTranslation('activity');
    const [answers, setAnswers] = useState<AnswerView[] | null>(null);
    const [stats, setStats] = useState<JudgmentStats | null>(null);
    const [next, setNext] = useState<string | undefined>();
    const [failed, setFailed] = useState(false);
    const [forgetting, setForgetting] = useState<AnswerView | null>(null);
    const [forgetFailed, setForgetFailed] = useState(false);

    const load = useCallback((before = '') => {
        GetAnswers(PAGE, before)
            .then((page) => {
                setAnswers((prev) => (before ? [...(prev ?? []), ...(page?.answers ?? [])] : page?.answers ?? []));
                setNext(page?.next || undefined);
                setFailed(false);
            })
            .catch(() => setFailed(true));
        if (!before) GetJudgmentStats(SOURCE_DAYS).then(setStats).catch(() => setFailed(true));
    }, []);

    useEffect(() => {
        load();
        const refresh = () => load();
        window.addEventListener('focus', refresh);
        return () => window.removeEventListener('focus', refresh);
    }, [load]);

    const today = stats?.days?.[0];
    const total = today?.total ?? EMPTY;
    const brief = today?.bySource?.brief ?? EMPTY;
    const numbers = [
        { key: 'made', value: String(total.shown), label: t('ans.made') },
        { key: 'answered', value: String(total.answered), label: t('ans.answered') },
        { key: 'yes', value: String(total.accepted + total.useful), label: t('ans.yes') },
        { key: 'brief', value: t('ans.ofCount', { n: brief.accepted, m: brief.shown }), label: t('ans.brief') },
    ];
    const rates = sourceRates(stats);

    const clock = (at: string) => {
        const date = new Date(at);
        if (Number.isNaN(date.getTime())) return '';
        return dayKey(date) === dayKey(new Date())
            ? date.toLocaleTimeString(i18n.language, { hour: 'numeric', minute: '2-digit' })
            : date.toLocaleDateString(i18n.language, { month: 'short', day: 'numeric' });
    };

    const confirmForget = () => {
        if (!forgetting) return;
        const { id } = forgetting;
        setForgetting(null);
        ForgetAnswer(id)
            .then(() => {
                setForgetFailed(false);
                setAnswers((prev) => (prev ?? []).filter((answer) => answer.id !== id));
                GetJudgmentStats(SOURCE_DAYS).then(setStats).catch(() => {});
            })
            .catch(() => setForgetFailed(true));
    };

    return (
        <div className="flex-1 overflow-y-auto" data-testid="activity-answers">
            <div className="mx-auto flex max-w-2xl flex-col gap-6 px-6 py-6">
                <header className="flex flex-wrap items-baseline justify-between gap-2">
                    <h2 className="m-0 text-[17px] font-semibold text-ink">{t('ans.title')}</h2>
                    <span className="text-[12.5px] text-muted">{t('ans.local')}</span>
                </header>
                {failed && <ActivityInlineNotice notice={{ tone: 'error', text: t('ans.loadFailed'), onAction: () => load() }} />}
                {forgetFailed && <ActivityInlineNotice notice={{ tone: 'error', text: t('ans.forgetFailed') }} />}
                <dl className="grid grid-cols-2 gap-3 sm:grid-cols-4" data-testid="answers-today">
                    {numbers.map(({ key, value, label }) => (
                        <div key={key} data-answer-number={key} className="rounded-xl border border-border bg-panel px-4 py-3">
                            <dd className="m-0 text-[22px] font-normal tabular-nums text-ink">{value}</dd>
                            <dt className="text-[12.5px] text-muted">{label}</dt>
                        </div>
                    ))}
                </dl>
                {rates.length > 0 && (
                    <section aria-labelledby="answers-sources" className="flex flex-col gap-2" data-testid="answers-sources">
                        <div className="flex items-baseline justify-between px-1">
                            <h3 id="answers-sources" className="m-0 text-[11px] font-semibold uppercase tracking-[0.08em] text-muted/80">{t('ans.sources')}</h3>
                            <span className="text-[12px] text-muted">{t('ans.sourcesMeta')}</span>
                        </div>
                        <ul className="flex flex-col gap-1.5">
                            {rates.map(({ source, yes, answered }) => (
                                <li key={source} data-answer-source={source} className="flex items-center gap-3 px-1 text-[13px]">
                                    <span className="w-[140px] shrink-0 truncate text-ink">{sourceLabel(t, source)}</span>
                                    <span aria-hidden className="h-1.5 flex-1 overflow-hidden rounded-full bg-surface-active">
                                        <span className="block h-full rounded-full bg-accent" style={{ width: `${Math.round((yes / answered) * 100)}%` }} />
                                    </span>
                                    <span className="w-[96px] shrink-0 text-right tabular-nums text-muted">{t('ans.rate', { n: yes, m: answered })}</span>
                                </li>
                            ))}
                        </ul>
                    </section>
                )}
                <section aria-labelledby="answers-every" className="flex flex-col">
                    <div className="mb-2 flex items-baseline justify-between px-1">
                        <h3 id="answers-every" className="m-0 text-[11px] font-semibold uppercase tracking-[0.08em] text-muted/80">{t('ans.every')}</h3>
                        <span className="text-[12px] text-muted">{t('ans.kept')}</span>
                    </div>
                    {answers !== null && answers.length === 0 && !failed && <p className="px-1 text-[12.5px] text-muted">{t('ans.empty')}</p>}
                    <ol className="flex flex-col">
                        {(answers ?? []).map((answer) => {
                            const under = answer.edited?.after || answer.reason || answer.note;
                            const accent = answer.action === 'yes' || answer.action === 'edit';
                            return (
                                <li key={answer.id} data-answer-row className="group flex items-start gap-3 rounded-md px-3 py-2 hover:bg-surface-hover">
                                    <span className="w-[62px] shrink-0 pt-px text-[12px] tabular-nums text-muted">{clock(answer.at)}</span>
                                    <span className={cn('w-[72px] shrink-0 text-[13px]', accent ? 'text-accent' : 'text-muted', answer.undone && 'line-through')}>
                                        {t(`ans.action.${answer.action}` as 'ans.action.yes')}
                                    </span>
                                    <div className="min-w-0 flex-1">
                                        <p className={cn('m-0 text-[13px] text-ink', answer.undone && 'text-muted line-through')}>{answer.title || t('ans.untitled')}</p>
                                        {under && <p className="m-0 text-[12px] text-muted line-clamp-2">{under}</p>}
                                        <p className="m-0 text-[11px] text-muted">
                                            {t(`ans.surface.${answer.surface}` as 'ans.surface.computer')}
                                            {answer.undone ? ` · ${t('ans.undone')}` : ''}
                                        </p>
                                    </div>
                                    <button
                                        type="button"
                                        onClick={() => setForgetting(answer)}
                                        aria-label={t('ans.forgetRow', { title: answer.title || t('ans.untitled') })}
                                        className="shrink-0 rounded-md px-2 py-1 text-[12px] text-muted hover:bg-surface-active hover:text-ink focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-border-focus"
                                    >
                                        {t('ans.forget')}
                                    </button>
                                </li>
                            );
                        })}
                    </ol>
                    {next && (
                        <button
                            type="button"
                            onClick={() => load(next)}
                            className="mt-2 self-start rounded-md px-3 py-1 text-[12.5px] text-muted hover:bg-surface-hover hover:text-ink focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-border-focus"
                        >
                            {t('ans.more')}
                        </button>
                    )}
                </section>
            </div>
            <ConfirmDialog
                open={forgetting !== null}
                onOpenChange={(open) => { if (!open) setForgetting(null); }}
                title={t('ans.forgetTitle')}
                description={forgetting ? t('ans.forgetBody', { title: forgetting.title || t('ans.untitled') }) : ''}
                confirmLabel={t('ans.forget')}
                variant="danger"
                onConfirm={confirmForget}
            />
        </div>
    );
}
