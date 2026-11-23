import { useCallback, useEffect, useRef, useState, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';
import { Bell, ExternalLink, MoreHorizontal, Unlink } from 'lucide-react';
import { ConfirmDialog } from '@/components/confirm-dialog';
import { ActivityInlineNotice, type ActivityNotice } from '@/components/activity-inline-notice';
import { dayEndMs, dayStartMs, formatDateStr, formatDurationMs } from '@/components/timeline/useCalendarData';
import { refreshActivitySessionData, useActivitySessionQueryState } from '@/components/timeline/activity-session-cache';
import { ReminderChoiceItems, ReminderTimePicker, useActivityReminder, useReminderWhen } from '@/components/activity-row-actions';
import {
    DropdownMenu,
    DropdownMenuContent,
    DropdownMenuItem,
    DropdownMenuLabel,
    DropdownMenuSub,
    DropdownMenuSubContent,
    DropdownMenuSubTrigger,
    DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu';
import type { TodayTopic } from '@/components/today/topics';
import { toTodayTopics } from '@/hooks/useTodayTopics';
import { GetTodayTopics } from '@/lib/page-queries';
import { pageQueryCache } from '@/lib/query-cache';
import {
    ForgetActivityFeedRow,
    ForgetWatchConversation,
    GetActivityFeed,
    GetActivitiesInRange,
    GetActivityForgetPreview,
    GetWatchConversationForgetPreview,
    GetActivityRowDetails,
    NotThisTopic,
    OpenActivityRow,
    type ActivityFeedKind,
    type ActivityFeedRow,
    type ActivityForgetPreview,
    type WatchConversationForgetPreview,
    type ActivityOpenTarget,
    type ActivityRowDetail,
    type ActivityView,
    type TodayTopicsView,
} from '@/lib/wails-shim-app';

// Activity's one timeline (computer-activity v1.0.0, rules 3 to 8): what
// Isoform saw in the day across the screen, the watch and your agents,
// newest first. A row is its time, a dot in its source's colour, the source,
// what it was and a line under it, and Forget, which first says what goes
// with it. Closed Eyes is a striped gap with nothing else about that time.
// The source is the dot and its name, never a bar at the row's left edge.

// Source dots (computer-theme v1.0.0 rule 3, conformance answers v1.1.0 P1):
// the screen is indigo, and an agent run the screen saw is a Screen row, so
// indigo too; the watch is the accent; Your agents (hand-offs) are plum;
// Closed Eyes is a striped warning dot. No green and no copper: green means
// only "working" or Done.
export const DOT_CLASS: Record<Exclude<ActivityFeedKind, 'closed' | 'trouble'>, string> = {
    screen: 'bg-app-indigo-text',
    watch: 'bg-accent',
    agent: 'bg-app-indigo-text',
    handoff: 'bg-app-plum-text',
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
            // What saw it: the screen (P1).
            return row.app ? t('feed.sourceScreenApp', { app: row.app }) : t('feed.sourceScreen');
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
            // A conversation by its name; untitled, and an hour, by its length.
            return (row.conversation && row.title) || t('feed.watchTitle', { length });
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

/** Who spoke in a conversation: "You" first, then the names the person
 *  gave, then the voices with no name as a count, never "Speaker 2". */
export function watchSpeakers(t: TFunction<'activity'>, row: ActivityFeedRow): string {
    const names = (row.speakers ?? []).map((speaker) => (speaker === 'you' ? t('feed.spokeYou') : speaker));
    if (row.otherVoices) names.push(t('feed.otherVoices', { count: row.otherVoices }));
    return names.join(', ');
}

/** A conversation's line (computer-activity v1.0.0 rule 4): its length, who
 *  spoke, its marks and whether it has a note. An untitled one already says
 *  its length in the title. */
function watchConversationLine(t: TFunction<'activity'>, row: ActivityFeedRow): string {
    return [
        row.title ? formatDurationMs(Math.max(0, row.endMs - row.startMs)) : '',
        watchSpeakers(t, row),
        row.marks ? t('feed.marks', { count: row.marks }) : '',
        row.hasNote ? t('feed.note') : '',
    ].filter((piece) => piece !== '').join(' · ');
}

/** Why Isoform has a row, from the consent it stands on. Only agent and
 *  learned rows say it (computer-activity v1.2.0 rule 1); the screen saw an
 *  agent at work on its own, which is not Learn from it (P1). */
function basisLine(t: TFunction<'activity'>, row: ActivityFeedRow): string {
    const agent = row.basis?.agent || row.app || '';
    switch (row.basis?.setting) {
        case 'screen':
            return agent ? t('act.agentOnScreen', { agent }) : t('act.agentOnScreenUnnamed');
        case 'learn':
            return t('act.readBecause', { agent });
        default:
            return '';
    }
}

export function feedLine(t: TFunction<'activity'>, row: ActivityFeedRow, lang = 'en'): string {
    switch (row.kind) {
        case 'handoff':
            return handoffLine(t, row, lang);
        case 'screen':
            return row.detail ?? '';
        case 'watch':
            if (row.conversation) return watchConversationLine(t, row);
            return row.words ? t('feed.watchWords', { count: row.words }) : t('feed.watchNoWords');
        case 'agent':
            return basisLine(t, row);
        default:
            return '';
    }
}

/** A trouble gap's line (computer-trouble v1.0.0 rule 7). */
export function troubleTitle(t: TFunction<'activity'>, row: ActivityFeedRow, lang: string): string {
    if (row.cause === 'restarted') return t('act.restarted', { from: clock(row.startMs, lang), to: clock(row.endMs, lang) });
    return row.ongoing ? t('act.gapNow') : t('act.gap', { time: clock(row.endMs, lang) });
}

export function troubleCause(t: TFunction<'activity'>, cause: ActivityFeedRow['cause']): string {
    switch (cause) {
        case 'screen':
            return t('act.cause.screen');
        case 'signed_out':
            return t('act.cause.signedOut');
        default:
            return t('act.cause.offline');
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
    /** Work an agent already has: named, not counted (15e rule 3). */
    staying: string[];
    count: number;
}

export function forgetPlan(t: TFunction<'activity'>, preview: ActivityForgetPreview, row: ActivityFeedRow, lang: string): ForgetPlan {
    if (row.kind === 'handoff') {
        // Isoform's record goes; what the agent made stays with it (v1.2.0 rule 3).
        const agent = preview.agent || row.app || '';
        return { title: t('act.forgetHandoff', { title: preview.title || feedTitle(t, row), agent }), lead: '', items: [], staying: [], count: 1 };
    }
    const what = row.kind === 'screen' ? (preview.title || feedTitle(t, row)) : feedTitle(t, row);
    const items: string[] = [];
    const staying: string[] = [];
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
            case 'conversation':
                items.push(t('act.forgetItem', {
                    what: item.title
                        ? t('feed.goes.conversation', { name: item.title })
                        : t('feed.goes.conversationAt', { start: clock(item.startMs ?? 0, lang), end: clock(item.endMs ?? 0, lang) }),
                    where: t('feed.where.watch'),
                }));
                break;
            case 'followup':
                items.push(t('act.forgetItem', { what: item.title || t('feed.goes.followupUntitled'), where: t('feed.where.work') }));
                break;
            case 'claim':
                items.push(t('act.forgetItem', { what: item.title ? t('feed.goes.claim', { claim: item.title }) : t('feed.goes.claimUntitled'), where: t('feed.where.aboutYou') }));
                break;
            case 'agent_work':
                staying.push(t('act.staysWith', { agent: item.title ?? '' }));
                break;
            default:
                break;
        }
    }
    const kind = t('act.forgetKind', { source: feedSource(t, row), time: clock(row.startMs, lang) });
    // Until Forget can cut one conversation out of its hours, a conversation
    // sharing them says the other goes too (conformance answers A8, interim);
    // each one that goes is also in the list.
    const shared = preview.goes.find((item) => item.kind === 'conversation');
    const other = shared
        ? shared.title || t('watch.conversationAt', { start: clock(shared.startMs ?? 0, lang), end: clock(shared.endMs ?? 0, lang) })
        : '';
    return {
        title: shared ? t('act.forgetShared', { name: what, other }) : t('act.forgetTitle', { what }),
        lead: items.length > 0 ? t('act.forgetLead', { what: kind }) : t('act.forgetLeadOnly', { what: kind }),
        items,
        staying,
        count: items.length + 1,
    };
}

/** Forget on a watch conversation cut out of its hours (computer-conformance
 *  answers A8 and A10): only that conversation's words, audio stretch, note
 *  and what was made from it go; a claim that could have come from either
 *  conversation in the hour is named and goes too; the hour's other
 *  conversations stay. */
export function cutForgetPlan(t: TFunction<'activity'>, cut: WatchConversationForgetPreview, row: ActivityFeedRow, lang: string): ForgetPlan {
    const what = cut.name || feedTitle(t, row);
    const items: string[] = [];
    if (cut.hasNote) items.push(t('act.forgetItem', { what: t('feed.goes.notes'), where: t('feed.where.watch') }));
    for (const item of cut.followups ?? []) {
        items.push(t('act.forgetItem', { what: item.title || t('feed.goes.followupUntitled'), where: t('feed.where.work') }));
    }
    for (const item of cut.claims ?? []) {
        items.push(t('act.forgetItem', { what: item.title ? t('feed.goes.claim', { claim: item.title }) : t('feed.goes.claimUntitled'), where: t('feed.where.aboutYou') }));
    }
    for (const item of cut.sameHourClaims ?? []) {
        items.push(t('act.forgetItem', { what: t('act.sameHourClaim', { claim: item.title || t('feed.goes.claimUntitled') }), where: t('feed.where.aboutYou') }));
    }
    const staying = (cut.stayingWithAgents ?? []).map((agent) => t('act.staysWith', { agent }));
    const kind = t('act.forgetKind', { source: feedSource(t, row), time: clock(row.startMs, lang) });
    return {
        title: t('act.forgetTitle', { what }),
        lead: items.length > 0 ? t('act.forgetLead', { what: kind }) : t('act.forgetLeadOnly', { what: kind }),
        items,
        staying,
        // The backend counts what goes, the conversation itself included.
        count: Math.max(cut.count, items.length + 1),
    };
}

const GONE = 'no longer kept';

/** Forget as Activity does it everywhere (15e): ask for the preview, show
 *  one confirmation, and on Forget remove the row at once, with no Undo. A
 *  failed read or a failed Forget says so; a row that changed under it says
 *  it is no longer kept, and the list is read again. */
export function useActivityForget(onForgotten: (row: ActivityFeedRow) => void, reload: () => void) {
    const { t, i18n } = useTranslation('activity');
    // A watch conversation is cut out of its hours when the cut can prove
    // its boundaries (cut); otherwise Forget takes the hours and names every
    // conversation that goes with it (preview, the A8 interim). The
    // confirmation and the Forget always use the same one.
    const [forgetting, setForgetting] = useState<{ row: ActivityFeedRow; preview?: ActivityForgetPreview; cut?: WatchConversationForgetPreview } | null>(null);
    const [failure, setFailure] = useState<'failed' | 'gone' | null>(null);
    const opener = useRef<HTMLElement | null>(null);

    const ask = useCallback((row: ActivityFeedRow, from?: HTMLElement | null) => {
        opener.current = from ?? null;
        setFailure(null);
        const whole = () => GetActivityForgetPreview(row.kind, row.id)
            .then((preview) => setForgetting({ row, preview }))
            .catch((err: unknown) => {
                if (String(err).includes(GONE)) {
                    setFailure('gone');
                    reload();
                } else setFailure('failed');
            });
        if (row.kind !== 'watch' || !row.conversation) {
            void whole();
            return;
        }
        GetWatchConversationForgetPreview(row.id)
            .then((cut) => setForgetting({ row, cut }))
            .catch((err: unknown) => {
                if (String(err).includes(GONE)) {
                    setFailure('gone');
                    reload();
                    return;
                }
                // The cut can't prove this conversation's boundaries (an old
                // recording, an earlier cut): take its hours, saying so.
                void whole();
            });
    }, [reload]);

    const confirm = () => {
        if (!forgetting) return;
        const { row, cut } = forgetting;
        (cut ? ForgetWatchConversation(row.id) : ForgetActivityFeedRow(row.kind, row.id))
            .then(() => {
                setForgetting(null);
                onForgotten(row);
                reload();
            })
            .catch((err: unknown) => {
                setForgetting(null);
                if (String(err).includes(GONE)) {
                    setFailure('gone');
                    reload();
                } else setFailure('failed');
            });
    };

    const plan = forgetting
        ? forgetting.cut
            ? cutForgetPlan(t, forgetting.cut, forgetting.row, i18n.language)
            : forgetting.preview ? forgetPlan(t, forgetting.preview, forgetting.row, i18n.language) : null
        : null;
    const notice: ReactNode = failure ? (
        <ActivityInlineNotice notice={{ tone: 'error', text: failure === 'gone' ? t('feed.forgetNoLonger') : t('feed.forgetFailed') }} />
    ) : null;
    const dialog = (
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
                    {plan.staying.length > 0 && (
                        <ul className="m-0 flex list-disc flex-col gap-1 pl-5" data-testid="forget-staying">
                            {plan.staying.map((item) => <li key={item}>{item}</li>)}
                        </ul>
                    )}
                    <p className="m-0">{t('act.forgetGone')}</p>
                </div>
            ) : ''}
            confirmLabel={plan && plan.count > 1 ? t('act.forgetAll', { n: plan.count }) : t('act.forgetOne')}
            variant="danger"
            focusCancel
            returnFocusTo={opener.current}
            onConfirm={confirm}
            closeOnConfirm={false}
        />
    );
    return { ask, dialog, notice };
}

/** What a row went under and where it can open (activity_row_topic.go),
 *  joined to the row on (kind, id). Only screen rows have them for now. */
export function rowDetailKey(kind: ActivityFeedKind, id: string): string {
    return `${kind}:${id}`;
}

/** The words for Open where it happened, per kind (the Go label for a call
 *  is an English word; a page's host and a file's name are safe to show). */
export function openWhereLabel(t: TFunction<'activity'>, open: ActivityOpenTarget): string {
    switch (open.kind) {
        case 'page':
            return t('act.openPage');
        case 'file':
            return t('act.openFile', { label: open.label });
        case 'call':
            return t('act.openCall');
        default:
            return '';
    }
}

/** A topic as Today opens it: the registry's entry when it is cached or
 *  can be read, else the row's own id and name (as Find opens a topic). */
export function openTopicFromRow(openTopic: (topic: TodayTopic) => void, id: string, name: string) {
    const found: TodayTopic = { id, name, kind: 'work', pinned: false };
    const pick = (raw: unknown) => toTodayTopics(raw).find((topic) => topic.id === id) ?? found;
    const cached = pageQueryCache.peek<TodayTopicsView>('GetTodayTopics');
    if (cached !== undefined) {
        openTopic(pick(cached));
        return;
    }
    Promise.resolve()
        .then(() => GetTodayTopics())
        .then((raw) => openTopic(pick(raw)), () => openTopic(found));
}

const ROW_ACTION_CLASS =
    'shrink-0 rounded-md px-2 py-1 text-[12px] text-muted hover:text-ink hover:bg-surface-active focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-border-focus transition-colors';

/** One row of a thing Isoform saw (computer-activity v1.0.0 rule 3): time,
 *  a dot in its source's colour, the source, what it was, a line, and
 *  Forget. A row that opens (a watch conversation) opens on click or Enter
 *  on the row, not on Forget. Its consent is on data-feed-basis.
 *
 *  Its topic (conformance answers A1) starts the line under it, in grey:
 *  the dot stays the row's one colour. ••• (A2) holds Not this topic, on a
 *  row with a topic, and Open where it happened, on a row Isoform can open;
 *  a row with neither has no •••. Forget stays its own button. */
/** Remind me on a screen row (design T12): the moment's pending reminder,
 *  from the day's activities, and a reload after a change. */
export interface FeedRowReminder {
    pendingAt?: number;
    onChanged: () => void;
}

export function ActivityFeedRowItem({ row, onForget, onOpen, showSource = true, detail, onOpenTopic, onNotThisTopic, onOpenWhere, reminder }: {
    row: ActivityFeedRow;
    onForget: (row: ActivityFeedRow, from: HTMLElement) => void;
    onOpen?: (row: ActivityFeedRow) => void;
    showSource?: boolean;
    detail?: ActivityRowDetail;
    onOpenTopic?: (topicId: string, topicName: string) => void;
    onNotThisTopic?: (row: ActivityFeedRow, topicId: string) => void;
    onOpenWhere?: (row: ActivityFeedRow) => void;
    /** Screen rows: Remind me…, the third ••• entry. */
    reminder?: FeedRowReminder;
}) {
    const { t, i18n } = useTranslation('activity');
    const reminderWhen = useReminderWhen();
    const [notice, setNotice] = useState<ActivityNotice | null>(null);
    const [picking, setPicking] = useState(false);
    const remind = useActivityReminder({
        activityId: row.id,
        pendingAt: reminder?.pendingAt,
        onChanged: () => reminder?.onChanged(),
        onNotice: setNotice,
        onSettled: () => setPicking(false),
    });
    if (row.kind === 'trouble' || row.kind === 'closed') return null;
    const line = feedLine(t, row, i18n.language);
    const topicId = detail?.topicId ?? '';
    const topicName = detail?.topicName ?? '';
    const hasTopic = topicId !== '' && topicName !== '';
    const open = detail?.open;
    const canNotThisTopic = hasTopic && Boolean(onNotThisTopic);
    const canOpenWhere = Boolean(open && onOpenWhere && openWhereLabel(t, open));
    const canRemind = Boolean(reminder);
    // A topic inside a row that opens is words only: a button can't hold a button.
    const topic = !hasTopic ? null : onOpenTopic && !onOpen ? (
        <button
            type="button"
            data-feed-topic
            onClick={() => onOpenTopic(topicId, topicName)}
            className="rounded-sm text-muted hover:text-ink hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-border-focus"
        >
            {topicName}
        </button>
    ) : (
        <span data-feed-topic>{topicName}</span>
    );
    const body = (
        <>
            <span className="w-[62px] shrink-0 pt-px text-[12px] tabular-nums text-muted">{clock(row.startMs, i18n.language)}</span>
            <span aria-hidden data-feed-dot className={`mt-[6px] size-[7px] shrink-0 rounded-full ${DOT_CLASS[row.kind]}`} />
            <div className="min-w-0 flex-1">
                {showSource && <p className="text-[11px] text-muted">{feedSource(t, row)}</p>}
                <p className="text-[13px] text-ink line-clamp-2 break-words">{feedTitle(t, row)}</p>
                {(topic || line) && (
                    <p className="text-[12px] text-muted line-clamp-2" data-feed-line>
                        {topic}
                        {topic && line ? ' · ' : ''}
                        {line}
                    </p>
                )}
            </div>
        </>
    );
    return (
        <li
            data-feed-kind={row.kind}
            data-feed-basis={row.basis?.setting ?? ''}
            className="group flex flex-wrap items-start gap-x-3 rounded-md px-3 py-2 hover:bg-surface-hover transition-colors"
        >
            {onOpen ? (
                <button
                    type="button"
                    data-feed-open
                    onClick={() => onOpen(row)}
                    aria-label={t('feed.openRow', { title: feedTitle(t, row) })}
                    className="flex min-w-0 flex-1 items-start gap-3 rounded-md text-left focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-border-focus"
                >
                    {body}
                </button>
            ) : (
                <div className="flex min-w-0 flex-1 items-start gap-3">{body}</div>
            )}
            {(canNotThisTopic || canOpenWhere || canRemind) && (
                <DropdownMenu onOpenChange={canRemind ? remind.loadChoices : undefined}>
                    <DropdownMenuTrigger asChild>
                        <button
                            type="button"
                            data-feed-more
                            aria-label={t('feed.moreRow', { title: feedTitle(t, row) })}
                            className={ROW_ACTION_CLASS}
                        >
                            <MoreHorizontal className="size-3.5" aria-hidden />
                        </button>
                    </DropdownMenuTrigger>
                    <DropdownMenuContent align="end">
                        {canNotThisTopic && (
                            <DropdownMenuItem data-feed-not-this-topic onSelect={() => onNotThisTopic?.(row, topicId)}>
                                <Unlink />
                                {t('act.notThisTopic')}
                            </DropdownMenuItem>
                        )}
                        {canOpenWhere && open && (
                            <DropdownMenuItem data-feed-open-where onSelect={() => onOpenWhere?.(row)}>
                                <ExternalLink />
                                {openWhereLabel(t, open)}
                            </DropdownMenuItem>
                        )}
                        {canRemind && remind.reminderAt ? (
                            <>
                                <DropdownMenuLabel data-feed-reminder-at>{t('rowActions.reminderSetFor', { when: reminderWhen(remind.reminderAt) })}</DropdownMenuLabel>
                                <DropdownMenuItem data-feed-cancel-reminder disabled={remind.busy} onSelect={() => void remind.cancelReminder()}>
                                    <Bell />
                                    {t('rowActions.cancelReminder')}
                                </DropdownMenuItem>
                            </>
                        ) : canRemind ? (
                            <DropdownMenuSub>
                                <DropdownMenuSubTrigger data-feed-remind-me disabled={remind.busy}>
                                    <Bell />
                                    {t('act.remindMe')}
                                </DropdownMenuSubTrigger>
                                <DropdownMenuSubContent>
                                    <ReminderChoiceItems
                                        reminder={remind}
                                        onPickTime={() => {
                                            remind.startPick();
                                            setPicking(true);
                                        }}
                                    />
                                </DropdownMenuSubContent>
                            </DropdownMenuSub>
                        ) : null}
                    </DropdownMenuContent>
                </DropdownMenu>
            )}
            <button
                type="button"
                onClick={(event) => onForget(row, event.currentTarget)}
                aria-label={t('feed.forgetRow', { title: feedTitle(t, row) })}
                className={ROW_ACTION_CLASS}
            >
                {t('feed.forget')}
            </button>
            {(picking || notice) && (
                <div className="basis-full space-y-2 pl-[93px] pt-1.5" data-feed-reminder>
                    {picking && <ReminderTimePicker reminder={remind} onClose={() => setPicking(false)} />}
                    <ActivityInlineNotice notice={notice} />
                </div>
            )}
        </li>
    );
}

/** The call's recording opens in the Screen timeline: OpenActivityRow
 *  answers a call with this prefix and the session to open. */
const OPEN_IN_APP = 'activity_open_in_app:';

/** Focus reloads at most this often; Forget and Try again reload at once.
 *  A feed read covers about 28 hours of transcripts. */
export const FEED_FOCUS_RELOAD_MS = 60_000;

/** The row actions every feed list shares: its topic, Not this topic and
 *  Open where it happened. Not this topic drops the topic at once and puts
 *  it back with Try again when the answer could not be kept. */
export function useActivityRowDetails(start: number, end: number, onOpenRecording?: (sessionId: string) => void) {
    const { t } = useTranslation('activity');
    const [details, setDetails] = useState<Map<string, ActivityRowDetail> | null>(null);
    const [detailsFailed, setDetailsFailed] = useState(false);
    const [notice, setNotice] = useState<ActivityNotice | null>(null);

    const loadDetails = useCallback(() => {
        GetActivityRowDetails(start, end)
            .then((list) => {
                setDetails(new Map((list ?? []).map((detail) => [rowDetailKey(detail.kind, detail.id), detail])));
                setDetailsFailed(false);
            })
            .catch(() => setDetailsFailed(true));
    }, [start, end]);

    const setTopic = (row: ActivityFeedRow, detail: ActivityRowDetail) =>
        setDetails((prev) => new Map(prev ?? []).set(rowDetailKey(row.kind, row.id), detail));

    const notThisTopic = (row: ActivityFeedRow, topicId: string) => {
        const before = details?.get(rowDetailKey(row.kind, row.id));
        if (!before) return;
        setNotice(null);
        setTopic(row, { ...before, topicId: undefined, topicName: undefined });
        NotThisTopic(row.kind, row.id, topicId)
            .catch(() => {
                setTopic(row, before);
                setNotice({ tone: 'error', text: t('feed.notThisTopicFailed'), onAction: () => notThisTopic(row, topicId) });
            });
    };

    const openWhere = (row: ActivityFeedRow) => {
        setNotice(null);
        OpenActivityRow(row.kind, row.id).catch((err: unknown) => {
            const message = String(err instanceof Error ? err.message : err);
            const at = message.indexOf(OPEN_IN_APP);
            if (at >= 0 && onOpenRecording) {
                onOpenRecording(message.slice(at + OPEN_IN_APP.length).trim());
                return;
            }
            setNotice({ tone: 'error', text: t('feed.openWhereFailed'), onAction: () => openWhere(row) });
        });
    };

    const failedNotice: ReactNode = detailsFailed ? (
        <ActivityInlineNotice notice={{ tone: 'error', text: t('feed.detailsFailed'), onAction: loadDetails }} />
    ) : null;
    return {
        details,
        loadDetails,
        notThisTopic,
        openWhere,
        notice: (
            <>
                {failedNotice}
                <ActivityInlineNotice notice={notice} />
            </>
        ),
    };
}

export function ActivityFeed({ date, kinds, emptyText, onOpenConversation, onOpenTopic, onOpenRecording }: {
    date: Date;
    /** The sources this tab shows; all of them when left out. */
    kinds?: ActivityFeedKind[];
    emptyText?: string;
    /** Opens a watch conversation on the Watch tab, on its own day; rows don't open without it. */
    onOpenConversation?: (conversationId: string, startMs: number) => void;
    /** Opens a row's topic (Today's topic, beside Today). */
    onOpenTopic?: (topic: TodayTopic) => void;
    /** Opens a call's recording in the Screen timeline. */
    onOpenRecording?: (sessionId: string) => void;
}) {
    const { t, i18n } = useTranslation('activity');
    const [rows, setRows] = useState<ActivityFeedRow[] | null>(null);
    const [failed, setFailed] = useState(false);
    const [start, end] = dayBounds(date);
    const kindsKey = (kinds ?? []).join(',');
    const lastRead = useRef(0);
    const rowDetails = useActivityRowDetails(start, end, onOpenRecording);
    const { loadDetails } = rowDetails;
    // Remind me's pending times and withheld moments: the day's activities,
    // the same cached read the Screen tab makes.
    const wantsScreen = !kindsKey || kindsKey.split(',').includes('screen');
    const activitiesKey = `activities:${formatDateStr(date)}`;
    const activityStart = dayStartMs(date);
    const activityEnd = dayEndMs(date);
    const loadActivities = useCallback(() => GetActivitiesInRange(activityStart, activityEnd), [activityStart, activityEnd]);
    const activityState = useActivitySessionQueryState<ActivityView[]>(activitiesKey, loadActivities, wantsScreen);
    const reloadActivities = useCallback(() => {
        void refreshActivitySessionData(activitiesKey, loadActivities).catch(() => {});
    }, [activitiesKey, loadActivities]);
    const activitiesById = new Map((activityState.data ?? []).map((activity) => [activity.id, activity]));
    const activitiesFailed = wantsScreen && activityState.error !== undefined && activityState.data === undefined;
    const reminderFor = (row: ActivityFeedRow): FeedRowReminder | undefined => {
        if (row.kind !== 'screen') return undefined;
        const activity = activitiesById.get(row.id);
        // A private or paused moment is kept out of reminders (as in the opened row).
        if (activity?.actionsWithheld) return undefined;
        return { pendingAt: activity?.reminderAt, onChanged: reloadActivities };
    };

    const load = useCallback(() => {
        lastRead.current = Date.now();
        GetActivityFeed(start, end)
            .then((list) => {
                const wanted = kindsKey ? new Set(kindsKey.split(',')) : null;
                setRows((list ?? []).filter((row) => !wanted || wanted.has(row.kind)));
                setFailed(false);
            })
            .catch(() => setFailed(true));
        loadDetails();
    }, [start, end, kindsKey, loadDetails]);

    useEffect(() => {
        setRows(null);
        load();
        // A window focus refreshes in the background, at most once a minute;
        // the rows on screen stay until the new ones arrive.
        const onFocus = () => {
            if (Date.now() - lastRead.current >= FEED_FOCUS_RELOAD_MS) load();
        };
        window.addEventListener('focus', onFocus);
        return () => window.removeEventListener('focus', onFocus);
    }, [load]);

    const forget = useActivityForget(
        (row) => setRows((prev) => (prev ?? []).filter((r) => !(r.kind === row.kind && r.id === row.id))),
        load,
    );
    const open = onOpenConversation
        ? (row: ActivityFeedRow) => { if (row.kind === 'watch' && row.conversation) onOpenConversation(row.id, row.startMs); }
        : undefined;
    const openTopic = onOpenTopic ? (id: string, name: string) => openTopicFromRow(onOpenTopic, id, name) : undefined;

    return (
        <section data-testid="activity-feed" aria-label={t('feed.label') as string}>
            {failed && <ActivityInlineNotice notice={{ tone: 'error', text: t('feed.loadFailed'), onAction: load }} />}
            {forget.notice}
            {rowDetails.notice}
            {activitiesFailed && <ActivityInlineNotice notice={{ tone: 'error', text: t('feed.remindersFailed'), onAction: reloadActivities }} />}
            {rows !== null && rows.length === 0 && !failed && (
                <p className="px-1 text-[12.5px] text-muted">{emptyText ?? t('feed.empty')}</p>
            )}
            <ol className="flex flex-col">
                {(rows ?? []).map((row) =>
                    row.kind === 'trouble' ? (
                        <li
                            key={`trouble-${row.startMs}`}
                            data-feed-kind="trouble"
                            data-feed-basis={row.basis?.setting ?? ''}
                            className="flex items-start gap-3 rounded-md px-3 py-2"
                        >
                            <span className="w-[62px] shrink-0 pt-px text-[12px] tabular-nums text-muted">{clock(row.startMs, i18n.language)}</span>
                            <span aria-hidden data-trouble-dot className="mt-[5px] size-[9px] shrink-0 rounded-full border border-dashed border-muted" />
                            <div className="min-w-0 flex-1">
                                <p className="text-[13px] text-ink">{troubleTitle(t, row, i18n.language)}</p>
                                {row.cause !== 'restarted' && <p className="text-[12px] text-muted">{t('act.gapWhy', { cause: troubleCause(t, row.cause) })}</p>}
                            </div>
                        </li>
                    ) : row.kind === 'closed' ? (
                        <li
                            key={`closed-${row.startMs}`}
                            data-feed-kind="closed"
                            data-feed-basis={row.basis?.setting ?? ''}
                            className="my-1 flex items-center gap-2 rounded-md px-3 py-2 text-[12px] text-muted"
                            style={{ backgroundImage: 'repeating-linear-gradient(135deg, var(--color-border) 0 1px, transparent 1px 7px)' }}
                        >
                            <span
                                aria-hidden
                                data-closed-dot
                                className="size-[7px] shrink-0 rounded-full border border-warning"
                                style={{ backgroundImage: 'repeating-linear-gradient(135deg, var(--color-warning) 0 1px, transparent 1px 3px)' }}
                            />
                            {t('act.closedGap', { start: clock(row.startMs, i18n.language), end: clock(row.endMs, i18n.language) })}
                        </li>
                    ) : (
                        <ActivityFeedRowItem
                            key={`${row.kind}-${row.id}`}
                            row={row}
                            onForget={forget.ask}
                            onOpen={open && row.kind === 'watch' && row.conversation ? open : undefined}
                            detail={rowDetails.details?.get(rowDetailKey(row.kind, row.id))}
                            onOpenTopic={openTopic}
                            onNotThisTopic={rowDetails.notThisTopic}
                            onOpenWhere={rowDetails.openWhere}
                            reminder={reminderFor(row)}
                        />
                    ),
                )}
            </ol>
            {forget.dialog}
        </section>
    );
}
