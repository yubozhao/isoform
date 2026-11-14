import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';
import { GitBranch, MessageSquareText } from 'lucide-react';
import { cn } from '@/lib/utils';
import { LogoIcon } from '@/assets/icons/LogoIcon';
import { Button } from '@/components/ui/button';
import { MainChatTopicTags } from '@/components/main-chat/main-chat-topic-tags';
import { createScheduleTextLabels, cronToHumanReadable } from '@/components/automations/schedule-utils';
import { NativeClipboardSetText, OpenExternalURL, ResolveUpcomingConfirm } from '@/lib/wails-shim-app';
import {
    getMainChatReport,
    getSideChatOrigin,
    getTextedReply,
    getUpcomingConfirm,
    type MainChatReportPayload,
    type MainChatReportTarget,
    type UpcomingConfirmStatus,
    chatDraftMailto,
    getChatDraft,
} from '@/lib/stream-helpers';
import type { StreamItem, UpcomingApproval } from '@/types';
import { mainChatTargetOpens, statusEntryPlace, TODAY_BRIEF_STATUS_TARGET } from '@/lib/main-chat-navigation';
import { computerClock, computerHourOptions } from "@/lib/brief-time";

function reportPlaceLabel(place: string | null, t: TFunction<'chat'>): string | null {
    switch (place) {
        case 'tasks': return t('mainChat.proactive.source.tasks');
        case 'calendar': return t('mainChat.proactive.source.calendar');
        case 'mail': return t('mainChat.proactive.source.mail');
        case 'slack': return t('mainChat.proactive.source.slack');
        case 'brief': return t('mainChat.proactive.source.brief');
        case 'observing': return t('mainChat.proactive.source.observing');
        default: return null;
    }
}

// Quiet system cards for the chat model: a report line background work
// posted into the main chat (one sentence, one action) and the origin note
// at the top of a side chat (the main-chat message it branched from).

const engagementReportParams = {
    commitment_slipping: ['title', 'due'],
    meeting_ended: ['title', 'decisions', 'followups'],
    person_reappeared: ['name', 'where'],
    goal_check_in: ['title'],
    work_finished: ['title'],
    work_blocked: ['title', 'question'],
    brief_ready: ['slot'],
    brief_missed: ['slot'],
    schedule_missed: ['title'],
    suggestion: ['title', 'count'],
    topic_finds: ['topic', 'title'],
    learned_to_confirm: ['statement'],
    capture_paused: ['what', 'duration'],
    meeting_prep: ['title', 'minutes'],
    calendar_conflict: ['summary'],
    meeting_followup: ['title', 'followups', 'decisions'],
    waiting_on_reply: [],
    playbook_suggested: ['site', 'time'],
    playbook_result: ['title', 'result'],
    reminder: ['title'],
} as const;

type EngagementReportKind = keyof typeof engagementReportParams;

type ReportKey =
    | `engagement.${EngagementReportKind}.summary`
    | `engagement.${Exclude<EngagementReportKind, 'waiting_on_reply'>}.detail`
    | 'handoff.completed'
    | 'handoff.active'
    | 'handoff.in_progress'
    | 'handoff.stale'
    | 'handoff.needs_review'
    | 'handoff.aborted'
    | 'handoff.blocked'
    | 'schedule.ok'
    | 'schedule.error'
    | 'schedule.created'
    | 'schedule.changed'
    | 'schedule.changedWhen'
    | 'schedule.changedOnce'
    | 'schedule.paused'
    | 'schedule.resumed'
    | 'schedule.started'
    | 'schedule.removed'
    | 'meeting.ready'
    | 'side_chat.brought_back'
    | 'message_send.sent';

/** Picks the localized sentence for a report; null falls back to the stored text. */
function reportKey(report: MainChatReportPayload): ReportKey | null {
    switch (report.source) {
        case 'handoff':
            switch (report.status) {
                case 'active':
                    return 'handoff.active';
                case 'in_progress':
                    return 'handoff.in_progress';
                case 'stale':
                    return 'handoff.stale';
                case 'completed':
                    return 'handoff.completed';
                case 'needs_review':
                    return 'handoff.needs_review';
                case 'aborted':
                    return 'handoff.aborted';
                case 'blocked':
                    return 'handoff.blocked';
                default:
                    return null;
            }
        case 'schedule':
            switch (report.status) {
                case 'error':
                    return 'schedule.error';
                case 'created':
                    return 'schedule.created';
                case 'changed':
                    if (report.schedule?.startsWith('at:')) return 'schedule.changedOnce';
                    return report.schedule ? 'schedule.changedWhen' : 'schedule.changed';
                case 'paused':
                    return 'schedule.paused';
                case 'resumed':
                    return 'schedule.resumed';
                case 'started':
                    return 'schedule.started';
                case 'removed':
                    return 'schedule.removed';
                default:
                    return 'schedule.ok';
            }
        case 'engagement': {
            const kind = report.status as EngagementReportKind;
            if (!Object.prototype.hasOwnProperty.call(engagementReportParams, kind)) return null;
            if (kind !== 'waiting_on_reply' && engagementReportParams[kind].every((param) => report.params?.[param]?.trim())) {
                return `engagement.${kind}.detail`;
            }
            return `engagement.${kind}.summary`;
        }
        case 'meeting':
            return 'meeting.ready';
        case 'side_chat':
            return 'side_chat.brought_back';
        case 'message_send':
            return report.channel && report.sentAt !== undefined && !Number.isNaN(new Date(report.sentAt).getTime())
                ? 'message_send.sent'
                : null;
        default:
            return null;
    }
}

/** Puts a changed schedule into words: a cron expression as the schedule
 *  page says it, at:<ms> as the date and time of a one-time run. */
function describeReportSchedule(schedule: string | undefined, tApp: TFunction<'app'>): string {
    if (!schedule) return '';
    if (schedule.startsWith('at:')) {
        const at = Number(schedule.slice(3));
        if (!Number.isFinite(at) || at <= 0) return '';
        return new Date(at).toLocaleString([], { weekday: 'short', month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit', ...computerHourOptions() });
    }
    return cronToHumanReadable(schedule, createScheduleTextLabels(tApp));
}

/** The renderer's own name for an Upcoming item it names itself. */
function upcomingItemTitle(item: string | undefined, t: TFunction<'chat'>): string {
    switch (item) {
        case 'brief':
            return t('mainChat.item.brief');
        case 'brief_morning':
            return t('mainChat.item.briefMorning');
        case 'brief_evening':
            return t('mainChat.item.briefEvening');
        case 'news':
            return t('mainChat.item.news');
        default:
            return '';
    }
}

/** The question an Upcoming tool's approval card asks, naming the item, or
 *  null for any other tool. A change that brings a run sooner says the new
 *  schedule. */
export function upcomingApprovalQuestion(
    toolName: string,
    upcoming: UpcomingApproval | undefined,
    tApp: TFunction<'app'>,
    t: TFunction<'chat'>,
): string | null {
    const title = upcomingItemTitle(upcoming?.item, t) || upcoming?.title || tApp('automations.untitled');
    switch (toolName) {
        case 'UpcomingRunNow':
            return tApp('automationsPage.approval.runNow', { title });
        case 'UpcomingResume':
            return tApp('automationsPage.approval.resume', { title });
        case 'UpcomingChange': {
            const when = describeReportSchedule(upcoming?.schedule, tApp);
            if (!when) return tApp('automationsPage.approval.change', { title });
            return upcoming?.schedule?.startsWith('at:')
                ? tApp('automationsPage.approval.changeOnce', { title, when })
                : tApp('automationsPage.approval.changeWhen', { title, when });
        }
        default:
            return null;
    }
}

/** The renderer's own words for a report whose subject has no name. */
function fallbackTitle(report: MainChatReportPayload, t: TFunction<'chat'>): string {
    switch (report.source) {
        case 'handoff':
            return t('mainChat.fallback.handoff');
        case 'schedule':
            return t('mainChat.fallback.schedule');
        case 'meeting':
            return t('mainChat.fallback.meeting');
        case 'side_chat':
            return t('mainChat.fallback.sideChat');
        default:
            return '';
    }
}

interface MainChatReportCardProps {
    item: StreamItem;
    /** Opens the report's target; when undefined the action is hidden. */
    onOpenTarget?: (target: MainChatReportTarget) => void;
    compact?: boolean;
}

export function MainChatReportCard({ item, onOpenTarget, compact = false }: MainChatReportCardProps) {
    const { t, i18n } = useTranslation('chat');
    const { t: tApp } = useTranslation('app');
    const { t: tToday } = useTranslation('today');
    const report = getMainChatReport(item);
    if (!report) return null;
    const key = reportKey(report);
    const title = upcomingItemTitle(report.item, t) || report.title || fallbackTitle(report, t);
    const when = describeReportSchedule(report.schedule, tApp);
    const channel = report.channel ? t(`mainChat.channel.${report.channel}`) : '';
    const time = report.sentAt !== undefined
        ? computerClock(new Date(report.sentAt), i18n.resolvedLanguage)
        : '';
    const params: Record<string, string> = { place: '', people: '', ...report.params };
    if (report.source === 'engagement' && (report.status === 'brief_ready' || report.status === 'brief_missed')) {
        switch (params.slot) {
            case 'morning':
                params.slot = t('mainChat.engagementSlot.morning');
                break;
            case 'evening':
                params.slot = t('mainChat.engagementSlot.evening');
                break;
            case 'on-request':
                params.slot = t('mainChat.engagementSlot.onRequest');
                break;
        }
    }
    const legacyBrief = report.source === 'engagement' && (report.status === 'brief_ready' || report.status === 'weekly_recap');
    const recordedTime = item.createdAt && Number.isFinite(new Date(item.createdAt).getTime()) ? computerClock(new Date(item.createdAt), i18n.resolvedLanguage) : '';
    const briefSlot = report.params?.slot === 'morning' || report.params?.slot === 'evening' ? report.params.slot
        : report.target.id?.endsWith('-evening') ? 'evening' : 'on_request';
    const sentence = legacyBrief
        ? `${report.status === 'weekly_recap' && briefSlot === 'on_request' ? tToday('todayBrief.kind.week') : tToday(`todayBrief.slot.${briefSlot}`)}${recordedTime ? ` · ${recordedTime}` : ''}`
        : key
        ? t(`mainChat.report.${key}`, report.source === 'engagement'
            ? { replace: params }
            : report.source === 'message_send' ? { channel, time } : { title, when })
        : (item.content ?? '');
    // Older reports have only a rendered sentence. Keep that historical
    // detail under the localized heading instead of losing its subject.
    const legacyDetail = report.source === 'engagement' && !report.params && key?.endsWith('.summary')
        && report.status !== 'waiting_on_reply' && item.content !== sentence ? item.content : '';
    const summary = legacyBrief ? '' : report.summary || legacyDetail;
    // The app or place the report is about; without one it ends after its sentence (chat-review v1.0.8).
    const place = report.source === 'schedule' || report.source === 'handoff' ? 'tasks'
        : report.source === 'meeting' ? 'calendar'
        : report.source === 'message_send' ? report.channel ?? null
        : report.source === 'engagement' ? statusEntryPlace({ kind: report.status ?? '', target: report.target })
        : null;
    const source = reportPlaceLabel(place, t);
    const actionLabel =
        report.action === 'review'
            ? t('mainChat.action.review')
            : report.action === 'needs_you'
              ? t('mainChat.action.needsYou')
              : report.action === 'open'
                ? t('mainChat.action.open')
                : '';
    const showAction = report.source !== 'message_send' && Boolean(actionLabel) && mainChatTargetOpens(report.target) && Boolean(onOpenTarget);

    return (
        <div
            data-main-chat-report={report.source}
            className={cn('animate-rise-in', compact ? '' : 'mx-auto w-full')}
        >
            <div className="flex items-start gap-2.5 px-3.5 py-2.5 rounded-lg border border-border bg-panel-warm text-[13px] leading-relaxed text-ink/85">
                <LogoIcon size={14} className="mt-[3px] shrink-0 text-accent" />
                <div className="min-w-0 flex-1">
                    <p className="break-words">{sentence}</p>
                    {summary ? (
                        <p className="mt-0.5 text-muted break-words">{summary}</p>
                    ) : null}
                    {source && <p className="mt-1 text-[12px] text-muted">{t('mainChat.proactive.sourceFrom', { source })}</p>}
                    <MainChatTopicTags topicIds={item.topicIds} className="mt-1.5" />
                </div>
                {showAction ? (
                    <Button
                        variant="secondary"
                        size="sm"
                        className="shrink-0 h-7 px-2.5 text-[12px]"
                        onClick={() => onOpenTarget?.(report.source === 'engagement' && report.status === 'brief_missed'
                            ? { ...report.target, kind: 'today', id: TODAY_BRIEF_STATUS_TARGET }
                            : report.target)}
                    >
                        {actionLabel}
                    </Button>
                ) : null}
            </div>
        </div>
    );
}

interface SideChatOriginCardProps {
    item: StreamItem;
    onOpenTarget?: (target: MainChatReportTarget) => void;
    compact?: boolean;
}

export function SideChatOriginCard({ item, onOpenTarget, compact = false }: SideChatOriginCardProps) {
    const { t } = useTranslation('chat');
    const origin = getSideChatOrigin(item);
    if (!origin) return null;
    return (
        <div data-side-chat-origin className={cn('animate-rise-in', compact ? '' : 'max-w-[90%] mx-auto')}>
            <div className="px-3.5 py-2.5 rounded-lg border border-border bg-panel-warm text-[13px] leading-relaxed">
                <div className="flex items-center gap-2 text-[12px] text-muted">
                    <GitBranch className="size-3.5 shrink-0" />
                    <span className="flex-1">{t('mainChat.origin.title')}</span>
                </div>
                {origin.excerpt ? (
                    <blockquote className="mt-1.5 rounded-md bg-ink/[0.03] px-3 py-2 text-ink/80 whitespace-pre-wrap break-words line-clamp-6">
                        {origin.excerpt}
                    </blockquote>
                ) : null}
                {onOpenTarget && origin.parentId ? (
                    <Button
                        variant="ghost"
                        size="sm"
                        className="mt-2 h-6 px-2 text-[12px] text-accent hover:text-accent"
                        onClick={() => onOpenTarget({ kind: 'session', id: origin.parentId })}
                    >
                        {t('mainChat.origin.openMain')}
                    </Button>
                ) : null}
            </div>
        </div>
    );
}

interface UpcomingConfirmCardProps {
    item: StreamItem;
    compact?: boolean;
}

/**
 * The confirm card the main chat posts before removing a schedule from
 * Upcoming. Nothing is removed until the user presses Remove here. The same
 * quiet card as a report line, with the two choices as its actions.
 */
export function UpcomingConfirmCard({ item, compact = false }: UpcomingConfirmCardProps) {
    const { t } = useTranslation('chat');
    const confirm = getUpcomingConfirm(item);
    const [answered, setAnswered] = useState<UpcomingConfirmStatus | null>(null);
    const [busy, setBusy] = useState(false);
    const [failed, setFailed] = useState(false);
    if (!confirm) return null;

    const title = confirm.title || t('mainChat.fallback.schedule');
    const expired = confirm.status === 'pending' && confirm.expiresAt > 0 && Date.now() > confirm.expiresAt;
    const status: UpcomingConfirmStatus = answered ?? (expired ? 'expired' : confirm.status);

    const answer = (remove: boolean) => {
        if (busy) return;
        setBusy(true);
        setFailed(false);
        ResolveUpcomingConfirm(item.id, remove)
            .then((result) => setAnswered(result.status))
            .catch(() => setFailed(true))
            .finally(() => setBusy(false));
    };

    let sentence: string;
    switch (status) {
        case 'removed':
            sentence = t('mainChat.confirm.removed', { title });
            break;
        case 'kept':
            sentence = t('mainChat.confirm.kept', { title });
            break;
        case 'expired':
            sentence = t('mainChat.confirm.expired', { title });
            break;
        case 'gone':
            sentence = t('mainChat.confirm.gone', { title });
            break;
        default:
            sentence = t('mainChat.confirm.question', { title });
    }

    return (
        <div data-upcoming-confirm={status} className={cn('animate-rise-in', compact ? '' : 'max-w-[90%] mx-auto')}>
            <div className="flex items-start gap-2.5 px-3.5 py-2.5 rounded-lg border border-border bg-panel-warm text-[13px] leading-relaxed text-ink/85">
                <LogoIcon size={14} className="mt-[3px] shrink-0 text-accent" />
                <div className="min-w-0 flex-1">
                    <p className="break-words">{sentence}</p>
                    {failed ? <p role="alert" className="mt-0.5 text-subtle break-words">{t('failure.save')}</p> : null}
                </div>
                {status === 'pending' ? (
                    <div className="flex shrink-0 items-center gap-1">
                        <Button
                            variant="ghost"
                            size="sm"
                            className="h-7 px-2 text-[12px] text-muted"
                            disabled={busy}
                            onClick={() => answer(false)}
                        >
                            {t('mainChat.confirm.keep')}
                        </Button>
                        <Button
                            variant="ghost"
                            size="sm"
                            className="h-7 px-2 text-[12px] text-error hover:text-error"
                            disabled={busy}
                            onClick={() => answer(true)}
                        >
                            {t('mainChat.confirm.remove')}
                        </Button>
                    </div>
                ) : null}
            </div>
        </div>
    );
}

interface ChatDraftCardProps {
    item: StreamItem;
    compact?: boolean;
}

/**
 * A message the chat drafted because the person asked to send one. Nothing is
 * sent from here: Copy puts it on the clipboard, and Open in mail app opens a
 * filled-in message in their mail app, where only their Send sends it.
 */
export function ChatDraftCard({ item, compact = false }: ChatDraftCardProps) {
    const { t } = useTranslation('chat');
    const draft = getChatDraft(item);
    const [copied, setCopied] = useState(false);
    const [failed, setFailed] = useState<'copy' | 'mail' | null>(null);
    if (!draft) return null;

    const copy = () => {
        setFailed(null);
        NativeClipboardSetText(draft.subject ? `${draft.subject}\n\n${draft.text}` : draft.text)
            .then(() => setCopied(true))
            .catch(() => setFailed('copy'));
    };
    const openMail = () => {
        setFailed(null);
        OpenExternalURL(chatDraftMailto(draft)).catch(() => setFailed('mail'));
    };

    return (
        <div data-chat-draft={draft.channel} className={cn('animate-rise-in', compact ? '' : 'max-w-[90%] mx-auto')}>
            <div className="flex flex-col gap-2 px-3.5 py-3 rounded-lg border border-border bg-panel-warm text-[13px] leading-relaxed text-ink/85">
                <div className="flex items-start gap-2.5">
                    <LogoIcon size={14} className="mt-[3px] shrink-0 text-accent" />
                    <p className="min-w-0 flex-1 break-words">
                        <span className="font-medium text-ink">{t('mainChat.draft.to', { to: draft.to })}</span>
                        <span className="text-muted">{t('mainChat.draft.separator')}{t('mainChat.draft.notSent')}</span>
                    </p>
                </div>
                {draft.subject ? <p className="break-words text-muted">{t('mainChat.draft.subject', { subject: draft.subject })}</p> : null}
                <p className="whitespace-pre-wrap break-words rounded-md bg-panel px-3 py-2 text-ink">{draft.text}</p>
                {failed ? <p role="alert" className="text-subtle">{t(failed === 'copy' ? 'mainChat.draft.copyFailed' : 'mainChat.draft.mailFailed')}</p> : null}
                <div className="flex flex-wrap items-center gap-1.5">
                    <Button variant="secondary" size="sm" onClick={copy}>{t(copied ? 'mainChat.draft.copied' : 'mainChat.draft.copy')}</Button>
                    {draft.channel === 'mail' ? <Button variant="secondary" size="sm" onClick={openMail}>{t('mainChat.draft.openInMail')}</Button> : null}
                </div>
            </div>
        </div>
    );
}

interface TextedReplyCardProps {
    item: StreamItem;
    /** "Use as my message": puts the words in the composer; hidden when undefined. */
    onUse?: (text: string) => void;
    compact?: boolean;
}

/** A texted reply: marked "From text", never shown as the user's message,
 *  because anyone can make a text look as if it came from the user's number. */
export function TextedReplyCard({ item, onUse, compact = false }: TextedReplyCardProps) {
    const { t, i18n } = useTranslation('chat');
    const reply = getTextedReply(item);
    if (!reply) return null;
    const at = reply.receivedAt ? new Date(reply.receivedAt) : null;
    const time =
        at && !Number.isNaN(at.getTime())
            ? computerClock(at, i18n.resolvedLanguage)
            : '';
    return (
        <div data-texted-reply className={cn('animate-rise-in', compact ? '' : 'max-w-[90%] mx-auto')}>
            <div className="px-3.5 py-2.5 rounded-lg border border-border bg-panel-warm text-[13px] leading-relaxed">
                <blockquote className="rounded-md bg-ink/[0.03] px-3 py-2 text-ink/80 whitespace-pre-wrap break-words">
                    {reply.text}
                </blockquote>
                <div className="mt-1.5 flex items-center gap-2 text-[12px] text-muted">
                    <MessageSquareText className="size-3.5 shrink-0" />
                    <span className="flex-1">
                        {time ? t('mainChat.texted.labelAt', { time }) : t('mainChat.texted.label')}
                    </span>
                </div>
                <p className="mt-1.5 text-[12px] text-muted">{t('mainChat.texted.note')}</p>
                {onUse ? (
                    <Button
                        variant="ghost"
                        size="sm"
                        className="mt-2 h-6 px-2 text-[12px] text-accent hover:text-accent"
                        onClick={() => onUse(reply.text)}
                    >
                        {t('mainChat.texted.use')}
                    </Button>
                ) : null}
            </div>
        </div>
    );
}
