import { measureToday } from "@/lib/today-telemetry";
import { useProactiveSentence } from "@/hooks/use-proactive-sentence";
import { useEffect, useId, useState } from 'react';
import { ChatFailureLine } from '@/components/chat-failure-line';
import { useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';
import { ChevronDown, ChevronRight, CircleAlert, CircleCheck, MoreHorizontal } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { WorkOnItButton } from '@/components/work-on-it/work-on-it';
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from '@/components/ui/dropdown-menu';
import { cn } from '@/lib/utils';
import { MAIN_CHAT_HELD_ACTIONS, holdMainChatCardAction, undoMainChatCardAction, useMainChatHeldAction } from '@/lib/main-chat-held-action';
import { CorrectMainChatProactiveFact, NativeClipboardSetText, OpenDeliverableFile, OpenExternalURL, ResolveMainChatProactiveAction, ReturnToMainChatPlace } from '@/lib/wails-shim-app';
import { ClipboardSetText } from '@/lib/wails-shim-runtime';
import { useMainChatProactiveReceipts } from '@/hooks/useMainChatProactiveReceipts';
import { holdMainChatDraftSend, undoMainChatDraftSend, useMainChatDraftSend } from '@/lib/main-chat-draft-send';
import { onEngagementSettingsChanged } from '@/lib/engagement-settings-events';
import {
    canSendMainChatProactiveDraft,
    getMainChatProactive,
    getMainChatStatus,
    type MainChatProactivePayload,
    type MainChatReportTarget,
    type MainChatStatusPayload,
} from '@/lib/stream-helpers';
import type { StreamItem } from '@/types';
import { handoffBlockedReasonKey, handoffStatusKey, handoffVerificationLine } from '@/components/handoffs/handoffStatus';
import { localDayKey, notifyMainChatViewChanged } from '@/lib/main-chat-view';
import { mainChatBriefMissedText, mainChatPastLineText } from '@/lib/main-chat-brief-relative';
import { MainChatTopicTags } from '@/components/main-chat/main-chat-topic-tags';
import { communicationDraftDetails } from '@/lib/communication';
import { cardButtons } from '@/lib/main-chat-card-spec';
import { useMainChatItemPanel, type MainChatItemPanelSurface } from '@/hooks/useMainChatItemPanel';
import { MainChatBriefCard } from '@/components/main-chat-brief-card';
import { briefTitle } from '@/lib/brief-title';
import { computerClock, computerDayClock } from '@/lib/brief-time';
import { toggleBriefPlayback } from '@/lib/brief-audio';
import { mainChatTargetOpens, statusEntryPlace, TODAY_BRIEF_STATUS_TARGET } from '@/lib/main-chat-navigation';
import { FirstReadCard } from '@/components/main-chat/first-read-card';
import { FirstDayChatStarters } from '@/components/main-chat/first-day-starters';
import { useMainChatStreamView } from '@/components/main-chat/main-chat-view-context';

interface MainChatProactiveProps {
    item: StreamItem;
    onOpenTarget?: (target: MainChatReportTarget) => void;
    compact?: boolean;
    panelSurface?: MainChatItemPanelSurface;
    detailPanel?: boolean;
    /** The card drawn inside the item panel, where the draft and its Send live. */
    inPanel?: boolean;
    /** The panel's header already reads the card's title (C5), so the card starts at its lines. */
    titleInHeader?: boolean;
}

const openActions = new Set(['open', 'open_notes', 'open_person', 'open_settings', 'open_event', 'review', 'see', 'update', 'answer']);
const copyLanguage = (language: string | undefined) => language?.toLowerCase().startsWith('zh') ? 'zh-CN' : 'en';
export function retiredBriefCopy(text: string): boolean {
    return /^(?:your (?:morning |evening )?(?:brief|week) is ready[.!]?|你的(?:早间|晚间)?(?:简报|一周)已经准备好了[。.!]?)$/i.test(text.trim());
}

const suggestionActions = new Set(['add_to_upcoming', 'do_once', 'not_now']);
const workActions = new Set(['review', 'stop']);
const stoppableWorkStatuses = new Set(['active', 'in_progress', 'needs_review', 'blocked']);
const permissionActions = new Set(['approve', 'decline']);
const quietActions = new Set(['not_now', 'defer', 'mute_kind']);

/** What a held action reads as while Undo is offered and after (§5). */
const heldResultKeys: Record<string, string> = {
    do_once: 'mainChat.proactive.held.started',
    not_now: 'mainChat.proactive.held.notNow',
    mute_kind: 'mainChat.proactive.held.muted',
    add_to_upcoming: 'mainChat.proactive.held.scheduled',
};


/** A source names the app or place a line is about, never storage
 *  (chat-review v1.0.8); null means no place is known. */
function sourceLabel(kind: string, t: TFunction<'chat'>): string | null {
    switch (kind) {
        case 'mail': return t('mainChat.proactive.source.mail');
        case 'slack': return t('mainChat.proactive.source.slack');
        case 'calendar': return t('mainChat.proactive.source.calendar');
        case 'commitment': return t('mainChat.proactive.source.commitment');
        case 'capture':
        case 'observing': return t('mainChat.proactive.source.observing');
        case 'brief': return t('mainChat.proactive.source.brief');
        case 'schedule':
        case 'handoff':
        case 'tasks': return t('mainChat.proactive.source.tasks');
        case 'suggestion': return t('mainChat.proactive.source.suggestion');
        case 'topic': return t('mainChat.proactive.source.topic');
        case 'playbook': return t('mainChat.proactive.source.playbook');
        case 'person': return t('mainChat.proactive.source.person');
        default: return null;
    }
}

/** A stop nobody chose, with its cause (chat-review v1.0.10 H2): "Stopped
 *  watching your screen at 12:21 PM: Screen Recording was turned off." A
 *  recorded end gives "from {start} to {end}". */
function pauseLine(t: TFunction<'chat'>, source: string | undefined, cause: string | undefined, startAt: number | undefined, endAt: number | undefined, lang: string): string {
    const screen = source === 'screen';
    const clock = (at: number) => computerClock(new Date(at), lang);
    const why = t(cause === 'permission'
        ? (screen ? 'mainChat.observationPause.cause.screenPermission' : source === 'system_audio' ? 'mainChat.observationPause.cause.systemAudioPermission' : 'mainChat.observationPause.cause.microphonePermission')
        : (screen ? 'mainChat.observationPause.cause.screenStopped' : 'mainChat.observationPause.cause.listeningStopped'));
    if (startAt && endAt) return t(screen ? 'mainChat.observationPause.stoppedWatchingFromTo' : 'mainChat.observationPause.stoppedListeningFromTo', { start: clock(startAt), end: clock(endAt), cause: why });
    return t(screen ? 'mainChat.observationPause.stoppedWatchingAt' : 'mainChat.observationPause.stoppedListeningAt', { time: clock(startAt ?? Date.now()), cause: why });
}

/** One style for every background line in a fold (chat-review v1.0.10 H3). */
export const BACKGROUND_LINE_CLASS = 'flex items-start gap-3 text-[12.5px] leading-relaxed text-muted';

/** Actions that step back from a card rather than act on it. */
// Needs-you cards and explicitly requested reminders remain in the scan path.
const needsUserKinds = new Set(['decision_due', 'work_blocked', 'commitment_slipping', 'waiting_on_reply', 'reminder']);

function ProactiveMessage({ item, message, onOpenTarget, nested = false, topicIds, panelSurface = 'chat', compact = false, detailPanel = false, inPanel = false, titleInHeader = false }: { item: StreamItem; message: MainChatProactivePayload; onOpenTarget?: MainChatProactiveProps['onOpenTarget']; nested?: boolean; topicIds?: string[]; panelSurface?: MainChatItemPanelSurface; compact?: boolean; detailPanel?: boolean; inPanel?: boolean; titleInHeader?: boolean }) {
    const { t, i18n } = useTranslation('chat');
    const { t: tToday } = useTranslation('today');
    const track = <T,>(id: string, run: () => T): T => panelSurface === "today" ? measureToday(id, "do", "done", run) : run();
    const { t: tApp } = useTranslation('app');
    const panel = useMainChatItemPanel(panelSurface);
    const copy = message.localized?.[copyLanguage(i18n.resolvedLanguage)] ?? message;
    const originalSentence = useProactiveSentence(message);
    const [state, setState] = useState(message.state);
    const [kindMuted, setKindMuted] = useState(message.kindMuted === true);
    const [busy, setBusy] = useState(false);
    const [failed, setFailed] = useState<false | { text: string; retry?: () => unknown }>(false);
    const draftSend = useMainChatDraftSend(message.momentId);
    const [editing, setEditing] = useState(false);
    const [editText, setEditText] = useState('');
    const [copied, setCopied] = useState(false);
    const [lastAction, setLastAction] = useState<string | null>(null);
    const [stopped, setStopped] = useState(false);
    useEffect(() => setStopped(false), [message.momentId, message.work?.status]);
    const [expanded, setExpanded] = useState(false);
    const [placeFailed, setPlaceFailed] = useState(false);
    const [calendarFailed, setCalendarFailed] = useState(false);
    const heldAction = useMainChatHeldAction(message.momentId);
    useEffect(() => {
        if (heldAction?.status === 'failed') setFailed({ text: t('failure.start'), retry: () => {
            setFailed(false);
            holdMainChatCardAction(message.momentId, heldAction.action);
            return true;
        } });
        if (heldAction?.status !== 'done') return;
        if (heldAction.state) setState(heldAction.state);
        setLastAction(heldAction.action);
        if (heldAction.action === 'mute_kind') setKindMuted(true);
    }, [heldAction?.status, heldAction?.state, heldAction?.action, message.momentId, t]);
    // A muted kind folds behind its own line once the computer has it.
    const heldMuteDone = heldAction?.status === 'done' && heldAction.action === 'mute_kind';
    const heldShowing = Boolean(heldAction) && heldAction?.status !== 'failed' && !heldMuteDone;
    // After its 5 seconds a held card folds to its title and result where it was posted.
    const heldFolded = heldAction?.status === 'done' && !heldMuteDone && !detailPanel;
    const [correctionOpen, setCorrectionOpen] = useState(false);
    const [correctionText, setCorrectionText] = useState('');
    // Brief and week cards open Today instead of expanding; a real sentence
    // of theirs shows inline.
    const expandable = message.kind === 'first_week';
    const panelOpen = panel.openId === message.momentId;
    const canFoldMuted = !message.permission && !needsUserKinds.has(message.kind);
    const folded = (canFoldMuted && (kindMuted || state === 'muted')) || message.kind === 'thread_update_unavailable';
    useEffect(() => setExpanded(false), [folded, message.momentId]);
    const receiptRef = useMainChatProactiveReceipts(message.kind === 'digest' ? null : message.momentId, !folded || expanded);
    useEffect(() => {
        setState(message.state);
        setKindMuted(message.kindMuted === true);
    }, [message.state, message.kindMuted]);
    useEffect(() => onEngagementSettingsChanged((view) => {
        const kind = view.kinds.find((entry) => entry.kind === message.kind);
        if (kind) setKindMuted(!kind.enabled);
    }), [message.kind]);

    // A sent draft reads "Sent to {name}" from the Undo seconds on, and from
    // the stored send time after that. It never offers Try again (§2).
    const draftGone = Boolean(message.draftContent) && (draftSend
        ? draftSend.status === 'waiting' || draftSend.status === 'sending' || draftSend.status === 'sent'
        : state === 'acted' && Boolean(message.sentAt));
    const draftSentAt = draftSend ? draftSend.sentAt : message.sentAt;
    const draftTrouble = draftSend?.status === 'refused' || draftSend?.status === 'unknown' ? draftSend.status : null;
    const draftName = message.draftContent?.recipient?.trim() || message.draftContent?.to || '';
    const resolved = heldShowing || draftGone || state === 'acted' || state === 'dismissed' || state === 'deferred' || state === 'expired';
    const hasChildren = Boolean(message.items?.length);
    const isWork = Boolean(message.work) && (message.kind === 'work_finished' || message.kind === 'work_blocked' || message.kind === 'playbook_result');
    const isPermission = Boolean(message.permission) && message.kind === 'decision_due';
    const workStatus = stopped ? 'aborted' : message.work?.status;
    const statusKey = isWork && workStatus ? handoffStatusKey({ status: workStatus }) : null;
    const blockedReasonKey = isWork && message.work ? handoffBlockedReasonKey({ status: workStatus ?? '', blockedReason: message.work.blockedReason }) : null;
    const verificationLine = isWork ? handoffVerificationLine(message.work?.verification ?? null) : null;
    // A brief card names its brief and renders its own Play and Read it, so a
    // recording is offered only with its words. The week's card is one too.
    // The first brief's card is a brief card too, so Read it opens the saved
    // brief in place (today-review v1.1.19 R27).
    const briefCard = (message.kind === 'brief_ready' || message.kind === 'weekly_recap' || message.kind === 'first_week') && Boolean(message.briefId);
    const slots = cardButtons(message);
    // A left-off card folded after the next morning brief no longer offers to go back.
    const leftOff = message.kind === 'decision_due' && message.target.kind === 'thread';
    // Repair this persisted legacy card in either UI language, without changing
    // other cards or inventing a next step for different work.
    const aboutYouLeftOff = leftOff && [message.headline, message.localized?.en?.headline].some(
        (headline) => headline?.trim() === 'You were going through what Isoform learned about you.',
    );
    const displayHeadline = aboutYouLeftOff ? t('mainChat.proactive.leftOffAboutYouTitle') : leftOff ? copy.headline.replace(/[.。]$/, '') : copy.headline;
    const sentence = aboutYouLeftOff && [message.body, message.localized?.en?.body].some(
        (body) => body?.trim() === 'Next: mark the beliefs in Learned today.',
    ) ? t('mainChat.proactive.leftOffAboutYouNext') : originalSentence;
    // A repeated body belongs in the title only, including punctuation variants.
    const sameLeftOffText = (text: string) => leftOff && text.trim().replace(/[.。]+$/, '').trim() === displayHeadline.trim().replace(/[.。]+$/, '').trim();
    // After Review the draft and its Send live in the panel only; the card keeps
    // its preview with Review and Not now (chat-review v1.0.8).
    const draftInPanel = panelOpen && !detailPanel && !inPanel && message.kind === 'waiting_on_reply';
    const cardActions = briefCard || resolved || (leftOff && item.rowClass === 'routine') || (panelOpen && !compact && !draftInPanel) ? [] : [slots.main, slots.second].filter((button): button is NonNullable<typeof button> => {
        if (!button) return false;
        if (button.does === 'panel') return !panelOpen || draftInPanel;
        if (button.does === 'expand') return true;
        if (button.does === 'answer' || button.does === 'correct') return Boolean(button.action && message.actionIds.includes(button.action));
        if (button.does === 'activity') return message.target.kind === 'thread' && Boolean(onOpenTarget);
        if (button.does === 'target') return message.target.kind !== 'none' && Boolean(onOpenTarget);
        if (button.does === 'place') return Boolean(message.place?.bundleId);
        if (button.does === 'calendar') return message.actionIds.includes('open');
        if (button.does === 'play') return Boolean(message.briefId);
        if (button.does === 'file') return Boolean(message.work?.deliverables[0]);
        return false;
    });
    const detailActions = resolved ? [] : message.actionIds.filter((action) => {
        // After a refused or unanswered send the draft keeps only Send and Edit (chat-06).
        if (draftTrouble && action !== 'send') return false;
        if (action === 'mute_kind') return !kindMuted;
        if (isPermission) return permissionActions.has(action);
        if (detailPanel && isWork && action === 'review') return false;
        if (isWork) return workActions.has(action) && (action !== 'stop' || stoppableWorkStatuses.has(workStatus ?? ''));
        if (action === 'send') return !editing && draftTrouble !== 'unknown' && canSendMainChatProactiveDraft(message.draftContent);
        if (action === 'copy_draft') return false;
        // The computer defers a waiting reply too (RC 2 C1); it leaves "Waiting on you".
        if (action === 'not_now' && message.kind === 'waiting_on_reply') return true;
        if (openActions.has(action)) return message.target.kind !== 'none' && Boolean(onOpenTarget);
        return message.kind === 'suggestion' && suggestionActions.has(action);
    });
    const draftSurface = detailPanel || (panelOpen && compact && !draftInPanel);
    // Copy draft sits in the card's ··· (C081).
    const draftMenuActions = !resolved && draftSurface && message.kind === 'waiting_on_reply' && message.draftContent && message.actionIds.includes('copy_draft') ? ['copy_draft'] : [];
    const menuActions = [...draftMenuActions, ...(detailPanel || resolved || (panelOpen && !compact && !draftInPanel) ? [] : message.actionIds.filter((action) =>
        (action === 'mute_kind' && !kindMuted) || (message.kind === 'first_week' && action === 'not_for_me')))];
    const editable = !resolved && !editing && draftSurface && message.kind === 'waiting_on_reply' && message.actionIds.includes('send') && canSendMainChatProactiveDraft(message.draftContent);
    const startEdit = () => {
        setEditText(draftSend?.text || message.draftContent?.text || '');
        setEditing(true);
    };
    const sendEdit = () => {
        const text = editText.trim();
        if (!text) return;
        setEditing(false);
        holdMainChatDraftSend(message.momentId, text === message.draftContent?.text.trim() ? '' : text);
    };

    const act = async (action: string): Promise<boolean> => {
        if (busy) return false;
        if (MAIN_CHAT_HELD_ACTIONS.has(action)) {
            setFailed(false);
            holdMainChatCardAction(message.momentId, action);
            return true;
        }
        if (action === 'send') {
            // Mail gets it only after the 5 seconds of Undo.
            setFailed(false);
            setCopied(false);
            holdMainChatDraftSend(message.momentId);
            return true;
        }
        setBusy(true);
        setFailed(false);
        setCopied(false);
        try {
            if (action === 'copy_draft' && message.draftContent) {
                const nativeRuntime = (window as unknown as { runtime?: { ClipboardSetText?: unknown } }).runtime?.ClipboardSetText;
                if (nativeRuntime) {
                    try {
                        await NativeClipboardSetText(message.draftContent.text);
                        setCopied(true);
                        return true;
                    } catch { /* Other platforms use the Wails runtime clipboard. */ }
                }
                if (!(await ClipboardSetText(message.draftContent.text))) {
                    setFailed({ text: t('mainChat.proactive.failed') });
                    return false;
                }
                setCopied(true);
                return true;
            }
            const nextState = await ResolveMainChatProactiveAction(message.momentId, action);
            setState(nextState);
            setLastAction(action);
            // What waits on the user may have changed; the main chat's
            // numbers read again.
            notifyMainChatViewChanged();
            if (isWork && action === 'stop' && nextState === 'acted') setStopped(true);
            if (action === 'mute_kind') setKindMuted(true);
            if (isWork && action === 'review') {
                void panel.openItem({ ...item, toolInput: { ...message, state: nextState } });
            }
            if (openActions.has(action) && !isWork && !isPermission) onOpenTarget?.(message.target);
            return true;
        } catch {
            setFailed({ text: t(action === 'done' ? 'failure.done' : isPermission ? 'failure.answer' : 'failure.start'), retry: () => act(action) });
            return false;
        } finally {
            setBusy(false);
        }
    };

    const saveCorrection = async () => {
        const correction = correctionText.trim();
        if (!correction || busy) return;
        setBusy(true);
        setFailed(false);
        try {
            const nextState = await CorrectMainChatProactiveFact(message.momentId, correction);
            setState(nextState);
            setLastAction('correct');
            setCorrectionOpen(false);
            setCorrectionText('');
            notifyMainChatViewChanged();
        } catch {
            setFailed({ text: t('failure.save'), retry: saveCorrection });
        } finally {
            setBusy(false);
        }
    };

    const runButton = async (button: NonNullable<typeof slots.main>) => {
        if (button.does === 'correct') {
            setCorrectionOpen(true);
            setCorrectionText('');
            setFailed(false);
            return;
        }
        if (button.does === 'answer' && button.action) {
            await act(button.action);
            return;
        }
        if (button.does === 'panel') {
            void panel.openItem(item);
            return;
        }
        if (button.does === 'expand') {
            setExpanded(true);
            return;
        }
        if (button.does === 'calendar') {
            setCalendarFailed(false);
            void OpenExternalURL('/System/Applications/Calendar.app').catch(() => setCalendarFailed(true));
            return;
        }
        if (button.does === 'activity' || button.does === 'target') {
            // A brief card names its brief; Today opens at it.
            const target = message.target.kind === 'today' && !message.target.id && message.briefId
                ? { ...message.target, id: message.briefId }
                : message.target;
            onOpenTarget?.(target);
            return;
        }
        if (button.does === 'place') {
            try {
                if (await ReturnToMainChatPlace(message.momentId)) return;
            } catch { /* The card can still open its thread panel. */ }
            setPlaceFailed(true);
            void panel.openMoment(message.momentId, true);
            return;
        }
        if (button.does === 'play' && message.briefId) {
            void toggleBriefPlayback(message.briefId).catch(() => setFailed({ text: t('failure.brief'), retry: () => toggleBriefPlayback(message.briefId!).then(() => { setFailed(false); return true; }, () => false) }));
            return;
        }
        if (button.does === 'file') {
            const deliverable = message.work?.deliverables[0];
            if (deliverable && message.work) {
                const open = /^https?:\/\//i.test(deliverable.url)
                    ? OpenExternalURL(deliverable.url)
                    : OpenDeliverableFile('handoff', message.work.handoffId, deliverable.url);
                void open.catch(() => setFailed({ text: t('failure.openFile', { file: deliverable.label }), retry: () => (/^https?:\/\//i.test(deliverable.url) ? OpenExternalURL(deliverable.url) : OpenDeliverableFile('handoff', message.work!.handoffId, deliverable.url)).then(() => { setFailed(false); return true; }, () => false) }));
            }
        }
    };

    const actionLabel = (action: string) => {
        if (action === 'send' && canSendMainChatProactiveDraft(message.draftContent)) {
            return t('mainChat.proactive.sendTo', {
                recipient: message.draftContent.recipient,
                channel: t(`mainChat.channel.${message.draftContent.channel}`),
            });
        }
        if (action === 'copy_draft') return t('mainChat.proactive.copyDraft');
        if (action === 'mute_kind') return t(message.kind === 'first_week' ? 'mainChat.proactive.notForMe' : 'mainChat.proactive.muteKind');
        if (action === 'not_for_me') return t('mainChat.proactive.notForMe');
        if (detailPanel && action === 'do_once') return tApp('tasks.doIt');
        if (detailPanel && action === 'not_now') return tApp('tasks.notNow');
        if (isWork && action === 'stop') return tApp('handoffsPage.stop');
        if (isWork && action === 'review') return t('mainChat.action.review');
        return copy.actionLabels[action] ?? message.actionLabels[action];
    };
    const panelActions = !detailPanel && panelOpen && compact && !resolved
        ? message.kind === 'calendar_conflict'
            ? ['keep_both'].filter((action) => message.actionIds.includes(action))
            : message.kind === 'waiting_on_reply'
            ? ['send'].filter((action) => !draftInPanel && message.actionIds.includes(action) && !editing && draftTrouble !== 'unknown' && canSendMainChatProactiveDraft(message.draftContent))
            : isPermission
              ? ['approve', 'decline'].filter((action) => message.actionIds.includes(action))
              : isWork && message.kind === 'work_blocked' && message.actionIds.includes('review')
                ? ['review']
                : []
        : [];
    const detailPrimaryIndex = detailActions.findIndex((action) => !quietActions.has(action) && action !== 'copy_draft');
    // Not now stays the quiet button in every state; only the action is filled (launch-film v2.4.0).
    const cardPrimaryIndex = cardActions.findIndex((button) => !quietActions.has(button.action ?? ''));
    const panelPrimaryIndex = cardPrimaryIndex >= 0 ? -1 : panelActions.findIndex((action) => !quietActions.has(action));
    const resultKey = lastAction ? `mainChat.proactive.results.${message.kind}_${lastAction}` : '';

    const editButton = (
        <Button size="sm" variant="quiet" className="text-[12.5px]" onClick={startEdit}>
            {t('mainChat.proactive.editDraft')}
        </Button>
    );
    const workOnIt = (
        <WorkOnItButton look="button" input={{ sourceKind: 'chat_card', sourceId: message.momentId, title: displayHeadline, detail: message.body ?? '', done: '' }} />
    );
    const moreMenu = menuActions.length > 0 && (
        <DropdownMenu>
            <DropdownMenuTrigger asChild>
                <Button size="icon" variant="quiet" aria-label={t('mainChat.proactive.moreActions')} className="ml-auto">
                    <MoreHorizontal className="size-4" />
                </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end">
                {menuActions.map((action) => (
                    <DropdownMenuItem key={action} disabled={busy} onSelect={() => void track("T71", () => act(action))}>
                        {actionLabel(action)}
                    </DropdownMenuItem>
                ))}
            </DropdownMenuContent>
        </DropdownMenu>
    );
    const heldLine = heldShowing && heldAction && (
        <p role="status" data-held-action={heldAction.action} data-held-status={heldAction.status} className="flex flex-wrap items-center gap-1.5 text-[12.5px] text-muted">
            <span>{t((heldResultKeys[heldAction.action] ?? 'mainChat.proactive.done') as never)}</span>
            {heldAction.status === 'waiting' && <>
                <span aria-hidden>·</span>
                <Button size="sm" variant="link" className="h-auto px-0 text-[12.5px] font-medium" onClick={() => track("T71", () => undoMainChatCardAction(message.momentId))}>
                    {t('mainChat.proactive.held.undo')}
                </Button>
            </>}
            {heldAction.action === 'add_to_upcoming' && onOpenTarget && <>
                <span aria-hidden>·</span>
                <Button size="sm" variant="link" className="h-auto px-0 text-[12.5px] font-medium" onClick={() => track("T70", () => onOpenTarget({ kind: 'upcoming' }))}>
                    {t('mainChat.proactive.held.edit')}
                </Button>
            </>}
        </p>
    );
    const draftTime = draftSentAt ? computerClock(new Date(draftSentAt), i18n.resolvedLanguage ?? 'en') : '';

    const briefDisplay = message.briefDisplay ?? (message.briefId && item.createdAt && Number.isFinite(new Date(item.createdAt).getTime()) ? {
        slot: /-morning$/.test(message.briefId) ? 'morning' : /-evening$/.test(message.briefId) ? 'evening' : 'on_request',
        asOf: new Date(item.createdAt).toISOString(), timezone: '',
    } : undefined);

    // RC 2 order: the real summary as the headline, the specific sentence,
    // the draft (To, Subject, text), the source line, the buttons, then the
    // card's topic tags.
    const content = (
        <>
            {statusKey && <p data-work-status={workStatus} className="-mb-1.5 text-[11px] font-medium text-muted">{tApp(statusKey)}</p>}
            {briefCard && message.briefId ? (
                <MainChatBriefCard
                    briefId={message.briefId}
                    weeklyRecap={message.kind === 'weekly_recap'}
                    display={briefDisplay}
                    lead={message.briefLead}
                    onOpenBrief={onOpenTarget ? () => onOpenTarget({ kind: 'today', id: message.briefId }) : undefined}
                    trailing={moreMenu}
                    meta={() => <>
                        {message.evidence.length > 0 ? (
                            <div className="flex flex-wrap gap-x-3 gap-y-1 text-[12px] leading-relaxed text-muted">
                                {message.evidence.map((ref, index) => {
                                    const at = ref.at ? new Date(ref.at) : null;
                                    const time = ref.kind === 'brief'
                                        ? ''
                                        : at && !Number.isNaN(at.getTime()) ? computerDayClock(at, i18n.resolvedLanguage ?? 'en') : '';
                                    const source = sourceLabel(ref.kind, t);
                                    if (!source) return null;
                                    return <span key={`${ref.kind}:${ref.id}:${index}`}>{time ? t('mainChat.proactive.sourceAt', { source, time }) : t('mainChat.proactive.sourceFrom', { source })}</span>;
                                })}
                            </div>
                        ) : <p className="text-[12px] leading-relaxed text-muted">{t('mainChat.proactive.sourceFrom', { source: t('mainChat.proactive.source.brief') })}</p>}
                        <MainChatTopicTags topicIds={topicIds} />
                    </>}
                />
            ) : !titleInHeader && (
                <h3 className="break-words text-[15px] font-semibold leading-snug text-ink">{message.kind === 'first_week' && message.briefId && briefDisplay
                    ? briefTitle({ ...briefDisplay, yourWeek: undefined, briefId: message.briefId } as Parameters<typeof briefTitle>[0], i18n.resolvedLanguage ?? i18n.language, (key, options) => String(tToday(key as never, options as never)))
                    : retiredBriefCopy(copy.headline) && message.kind === 'brief_ready' ? tToday('todayBrief.slot.on_request') : retiredBriefCopy(copy.headline) && message.kind === 'weekly_recap' ? tToday('todayBrief.kind.week') : displayHeadline}</h3>
            )}
            {blockedReasonKey && <p className="text-[12px] leading-relaxed text-muted">{tApp(blockedReasonKey)}</p>}
            {verificationLine && <p className="text-[12px] leading-relaxed text-muted">{verificationLine.kind === 'checks' ? tApp(verificationLine.key, { met: verificationLine.met, total: verificationLine.total }) : tApp(verificationLine.key)}</p>}
            {message.updatedAt ? <p data-main-chat-updated className="text-[12px] leading-relaxed text-muted">{message.state === 'done'
                ? t('mainChat.proactive.doneAt', { time: computerClock(new Date(message.updatedAt), i18n.resolvedLanguage ?? 'en') })
                : t('mainChat.proactive.updatedAt', { time: computerClock(new Date(message.updatedAt), i18n.resolvedLanguage ?? 'en') })}</p> : null}
            {sentence && !sameLeftOffText(sentence) && !briefCard && !retiredBriefCopy(sentence) && (detailPanel || !expandable || expanded || panelOpen) &&<p className="whitespace-pre-wrap break-words text-[14px] leading-normal text-ink">{sentence}</p>}
            {!briefCard && !aboutYouLeftOff && !retiredBriefCopy(message.body) && !detailPanel && (expanded || panelOpen) && message.body && !sameLeftOffText(message.body) && message.body !== sentence && message.body !== copy.headline && (
                <p className="whitespace-pre-wrap break-words text-[14px] leading-normal text-ink">{message.body}</p>
            )}
            {isWork && message.work?.summary && !sentence.includes(message.work.summary) && message.work.summary !== copy.headline && <p className="whitespace-pre-wrap break-words text-[14px] leading-normal text-ink">{message.work.summary}</p>}
            {detailPanel && isWork && message.work?.result && message.work.result !== sentence && <p className="whitespace-pre-wrap break-words text-[14px] leading-normal text-ink">{message.work.result}</p>}
            {isPermission && message.permission && (
                <details open className="group rounded-lg border border-border px-3 py-2 text-[12px] leading-relaxed">
                    <summary className="flex cursor-pointer list-none items-center gap-1.5 font-medium text-ink [&::-webkit-details-marker]:hidden">
                        <ChevronRight aria-hidden className="size-3.5 shrink-0 transition-transform group-open:rotate-90" />
                        {t('mainChat.proactive.requestDetails')}
                    </summary>
                    {message.permission.toolName && <p className="mt-2 break-words text-muted">{t('mainChat.proactive.permissionTool', { tool: message.permission.toolName })}</p>}
                    <p className="mt-1 whitespace-pre-wrap break-words text-ink/85">{message.permission.details}</p>
                </details>
            )}
            {correctionOpen && message.kind === 'learned_to_confirm' && (
                <div className="flex flex-col gap-2 rounded-lg border border-border bg-panel px-3 py-2.5">
                    <label htmlFor={`${message.momentId}-correction`} className="text-[12px] font-medium text-ink">
                        {t('mainChat.proactive.correction.label')}
                    </label>
                    <textarea
                        id={`${message.momentId}-correction`}
                        aria-label={t('mainChat.proactive.correction.label')}
                        placeholder={t('mainChat.proactive.correction.placeholder')}
                        value={correctionText}
                        onChange={(event) => setCorrectionText(event.target.value)}
                        disabled={busy}
                        rows={3}
                        className="min-h-20 resize-y rounded-md border border-border bg-background px-2.5 py-2 text-[13px] text-ink outline-none focus-visible:ring-2 focus-visible:ring-accent-2/40"
                    />
                    <div className="flex justify-end gap-2">
                        <Button type="button" size="sm" variant="quiet" disabled={busy} onClick={() => track("T71", () => {
                            setCorrectionOpen(false);
                            setCorrectionText('');
                        })}>
                            {t('mainChat.proactive.correction.cancel')}
                        </Button>
                        <Button type="button" size="sm" disabled={busy || correctionText.trim().length === 0} onClick={() => void track("T71", saveCorrection)}>
                            {t('mainChat.proactive.correction.save')}
                        </Button>
                    </div>
                </div>
            )}
            {message.draftContent && (
                <div data-proactive-draft className="flex flex-col gap-1 rounded-lg border border-border bg-panel px-3 py-2.5 text-[13px]">
                    <dl className="flex flex-col gap-1">
                        <div className="flex gap-2"><dt className="text-muted">{t('mainChat.proactive.draftTo')}</dt><dd className="min-w-0 break-words text-ink">{message.draftContent.to}</dd></div>
                        <div className="flex gap-2"><dt className="text-muted">{t('mainChat.proactive.draftSubject')}</dt><dd className="min-w-0 break-words text-ink">{message.draftContent.subject}</dd></div>
                    </dl>
                    {editing ? (
                        <div className="flex flex-col gap-2">
                            <textarea
                                aria-label={t('mainChat.proactive.editLabel')}
                                value={editText}
                                onChange={(event) => setEditText(event.target.value)}
                                rows={4}
                                className="min-h-24 resize-y rounded-md border border-border bg-background px-2.5 py-2 text-[13px] leading-normal text-ink outline-none focus-visible:ring-2 focus-visible:ring-accent-2/40"
                            />
                            <div className="flex flex-wrap items-center gap-2">
                                <Button type="button" size="sm" className="text-[12.5px]" disabled={editText.trim().length === 0} onClick={sendEdit}>
                                    {actionLabel('send')}
                                </Button>
                                <Button type="button" size="sm" variant="quiet" className="text-[12.5px]" onClick={() => setEditing(false)}>
                                    {t('mainChat.proactive.editCancel')}
                                </Button>
                            </div>
                        </div>
                    ) : (
                        <p className="whitespace-pre-wrap break-words leading-normal text-ink">{draftSend?.text || message.draftContent.text}</p>
                    )}
                    {message.draftContent.communication && (
                        <div className="mt-1 flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-muted" data-communication-provenance>
                            <span>
                                {t('mainChat.proactive.communication.written', {
                                    name: message.draftContent.communication.recipientName,
                                    details: communicationDraftDetails(t, 'mainChat.proactive.communication', message.draftContent.communication),
                                })}
                            </span>
                            {onOpenTarget && (
                                <Button
                                    size="sm"
                                    variant="link"
                                    className="ml-auto h-auto px-0"
                                    onClick={() => track("T70", () => onOpenTarget({ kind: 'memory', id: message.draftContent!.communication!.beliefIds[0] }))}
                                >
                                    {t('mainChat.proactive.communication.change')}
                                </Button>
                            )}
                        </div>
                    )}
                </div>
            )}
            {leftOff && message.place?.app && message.place.at && !Number.isNaN(new Date(message.place.at).getTime()) ? (
                // One source line with one time: where and when they left off.
                <p data-left-off className="text-[12px] leading-relaxed text-muted">
                    {t('mainChat.proactive.leftOffIn', { app: message.place.app, when: computerDayClock(new Date(message.place.at), i18n.resolvedLanguage ?? 'en') })}
                </p>
            ) : !briefCard && !hasChildren && !leftOff && message.evidence.length > 0 && (
                <div className="flex flex-wrap gap-x-3 gap-y-1 text-[12px] leading-relaxed text-muted">
                    {message.evidence.map((ref, index) => {
                        const at = ref.at ? new Date(ref.at) : null;
                        const time = at && !Number.isNaN(at.getTime()) ? computerDayClock(at, i18n.resolvedLanguage ?? 'en') : '';
                        const source = sourceLabel(ref.kind, t);
                        if (!source) return null;
                        const label = time ? t('mainChat.proactive.sourceAt', { source, time }) : t('mainChat.proactive.sourceFrom', { source });
                        return <span key={`${ref.kind}:${ref.id}:${index}`}>{label}</span>;
                    })}
                </div>
            )}
            {!briefCard && <MainChatTopicTags topicIds={topicIds} />}
            {detailPanel ? (detailActions.length > 0 || editable || menuActions.length > 0) && (
                <div className="flex flex-wrap items-center gap-2">
                    {detailActions.map((action, index) => (
                        <Button
                            key={action}
                            data-proactive-action={action}
                            size="sm"
                            variant={index === detailPrimaryIndex ? 'default' : action === 'copy_draft' ? 'secondary' : 'quiet'}
                            className={cn('text-[12.5px]', action === 'mute_kind' && 'ml-auto')}
                            disabled={busy}
                            onClick={() => void track("T71", () => act(action))}
                        >
                            {actionLabel(action)}
                        </Button>
                    ))}
                    {editable && editButton}
                    {workOnIt}
                    {moreMenu}
                </div>
            ) : !briefCard && (cardActions.length > 0 || panelActions.length > 0 || menuActions.length > 0 || editable) && (
                <div className="flex flex-wrap items-center gap-2">
                    {cardActions.map((button, index) => (
                        <Button
                            key={button.id}
                            data-proactive-action={button.action ?? button.id}
                            size="sm"
                            variant={index === cardPrimaryIndex ? 'default' : 'quiet'}
                            className="text-[12.5px]"
                            disabled={busy}
                            aria-expanded={button.does === 'expand' ? expanded : undefined}
                            onClick={() => void track("T70", () => runButton(button))}
                        >
                            {button.does === 'answer' && message.kind === 'goal_check_in' && button.action
                                ? actionLabel(button.action) ?? t(button.labelKey as never, button.labelParams)
                                : t((placeFailed && button.does === 'place' ? 'mainChat.card.button.showThread' : button.labelKey) as never, button.labelParams)}
                        </Button>
                    ))}
                    {panelActions.map((action, index) => (
                        <Button
                            key={action}
                            data-proactive-action={action}
                            size="sm"
                            variant={index === panelPrimaryIndex ? 'default' : 'quiet'}
                            className="text-[12.5px]"
                            disabled={busy}
                            onClick={() => void track("T71", () => act(action))}
                        >
                            {actionLabel(action) ?? t('mainChat.card.button.answer')}
                        </Button>
                    ))}
                    {editable && editButton}
                    {workOnIt}
                    {moreMenu}
                </div>
            )}
            {draftGone && (
                <p role="status" data-draft-send={draftSend?.status ?? 'sent'} className="flex flex-wrap items-center gap-1.5 text-[12.5px] text-muted">
                    <CircleCheck aria-hidden className="size-4 shrink-0 text-accent" />
                    <span>{draftTime
                        ? t('mainChat.proactive.draftSentAt', { name: draftName, time: draftTime })
                        : draftSend?.status === 'waiting'
                          ? t('mainChat.proactive.draftSentHeld', { name: draftName })
                          : t('mainChat.proactive.draftSent', { name: draftName })}</span>
                    {draftSend?.status === 'waiting' && (
                        <Button size="sm" variant="link" className="h-auto px-0 text-[12.5px] font-medium" onClick={() => undoMainChatDraftSend(message.momentId)}>
                            {t('mainChat.proactive.undoSend')}
                        </Button>
                    )}
                </p>
            )}
            {draftTrouble && !editing && (
                <p role="alert" data-draft-send={draftTrouble} className="flex items-center gap-1.5 text-[12.5px] text-warning">
                    <CircleAlert aria-hidden className="size-4 shrink-0" />
                    <span>{t(draftTrouble === 'unknown' ? 'mainChat.proactive.draftUnknown' : 'mainChat.proactive.draftRefused')}</span>
                </p>
            )}
            {heldLine}
            {calendarFailed && <p role="alert" className="text-[12px] text-muted">{t('mainChat.proactive.calendarFailed')}</p>}
            {resolved && !draftGone && !heldShowing && <p role="status" className="text-[11px] text-muted">{lastAction && i18n.exists(resultKey, { ns: 'chat' }) ? t(resultKey as never) : state === 'dismissed' || state === 'deferred' ? t('mainChat.proactive.held.notNow') : state === 'expired' ? t('mainChat.proactive.permissionExpired') : isPermission ? t('mainChat.proactive.permissionAnswered') : isWork ? workStatus === 'aborted' ? t('mainChat.proactive.workStopped') : t('mainChat.proactive.workUpdated') : t('mainChat.proactive.done')}</p>}
            {!resolved && kindMuted && <p role="status" className="text-[11px] text-muted">{t('mainChat.proactive.held.muted')}</p>}
            {copied && <p role="status" className="text-[11px] text-muted">{t('mainChat.proactive.draftCopied')}</p>}
            {failed && <ChatFailureLine testId="proactive" text={failed.text} onRetry={failed.retry} />}
            {message.items && message.items.length > 0 && (
                <div className="divide-y divide-border space-y-3">
                {message.items.filter((child) => child.kind !== 'thread_update_unavailable').map((child) => {
                    const childItem: StreamItem = { ...item, id: child.momentId, content: child.headline, toolInput: child };
                    return <div key={child.momentId} className="pt-3"><ProactiveMessage item={childItem} message={child} onOpenTarget={onOpenTarget} nested panelSurface={panelSurface} compact={compact} /></div>;
                })}
                </div>
            )}
        </>
    );
    return (
        <section ref={receiptRef} data-proactive-moment={message.momentId} className={cn('flex min-w-0 flex-col gap-2.5', !nested && !folded && 'rounded-xl border border-border bg-panel-warm px-[18px] py-4')}>
            {heldFolded ? (
                <div data-proactive-held-folded className="flex flex-col gap-1">
                    <h3 className="break-words text-[15px] font-semibold leading-snug text-ink">{displayHeadline}</h3>
                    {heldLine}
                </div>
            ) : folded ? (
                <details onToggle={(event) => setExpanded(event.currentTarget.open)} data-proactive-folded>
                    {/* Chat's own disclosure (as the day fold), not the browser's triangle. */}
                    <summary className="flex cursor-pointer list-none items-center gap-1.5 rounded-sm text-[12.5px] text-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-border-focus [&::-webkit-details-marker]:hidden">
                        <ChevronRight aria-hidden className={cn('size-3.5 shrink-0 transition-transform', expanded && 'rotate-90')} />
                        <span className="min-w-0 break-words">{displayHeadline}</span>
                    </summary>
                    <div hidden={!expanded} className={cn(expanded && 'flex flex-col gap-2.5 pt-2')}>{content}</div>
                </details>
            ) : content}
        </section>
    );
}

/** A brief's card: the brief ready, the week's recap, or the first week's brief. */
export function isChatBriefCard(message: { kind: string; briefId?: string }): boolean {
    return (message.kind === 'brief_ready' || message.kind === 'weekly_recap' || message.kind === 'first_week') && Boolean(message.briefId);
}

export function MainChatProactiveCard({ item, onOpenTarget, compact = false, panelSurface = 'chat', detailPanel = false, inPanel = false, titleInHeader = false }: MainChatProactiveProps) {
    const panel = useMainChatItemPanel(panelSurface);
    const streamView = useMainChatStreamView();
    const { t, i18n } = useTranslation('chat');
    const message = getMainChatProactive(item);
    if (!message) return null;
    // The brief lives on Today, and Chat never speaks first (pass-questions
    // v1.0.0 rule 8): its card leaves Chat, with no row kept for it.
    if (streamView && isChatBriefCard(message)) return null;
    if (streamView && message.kind === 'capture_paused') {
        const text = pauseLine(t, message.captureSource, message.captureCause, message.captureStartAt, message.captureEndAt, i18n.resolvedLanguage ?? 'en');
        return <div data-main-chat-proactive="capture_paused" data-background-line className={BACKGROUND_LINE_CLASS}>
            <span className="min-w-0 flex-1 break-words">{text}</span>
            {onOpenTarget && !message.captureEndAt && <button type="button" className="shrink-0 text-accent hover:underline" onClick={() => onOpenTarget({ kind: 'settings', id: 'activity' })}>{t('mainChat.observationPause.turnBackOn')}</button>}
        </div>;
    }
    // A card says what happened: no meeting card without the meeting's title.
    if (message.kind === 'meeting_prep' && (!message.event?.title || message.event.private)) return null;
    const meetingStart = message.kind === 'meeting_prep' && message.event?.start ? new Date(message.event.start) : null;
    if (meetingStart && !Number.isNaN(meetingStart.getTime()) && (item.rowClass === 'routine' || meetingStart.getTime() <= Date.now())) {
        // Once it has started it is no longer an action: one line in the day's updates.
        return (
            <div data-main-chat-proactive="meeting_prep" data-meeting-started data-background-line className={cn(BACKGROUND_LINE_CLASS, !compact && 'mx-auto w-full')}>
                <span className="min-w-0 flex-1">{t('mainChat.proactive.meetingStarted', { title: message.event!.title, time: computerClock(meetingStart, i18n.resolvedLanguage ?? 'en') })} · {t('mainChat.proactive.sourceFrom', { source: t('mainChat.proactive.source.calendar') })}</span>
                {onOpenTarget && <button type="button" className="shrink-0 text-accent hover:underline" onClick={() => onOpenTarget({ kind: 'calendar', id: message.target.id })}>{t('mainChat.card.button.show')}</button>}
            </div>
        );
    }
    const panelOpen = panel.openId === message.momentId;
    return (
        <>
            {streamView?.firstReadStarter?.itemId === item.id && <FirstDayChatStarters items={streamView.firstReadStarter.items} onPrepare={streamView.onFillChip} />}
            <article data-main-chat-proactive={message.kind} data-panel-open={panelOpen || undefined} className={cn('flex min-w-0 flex-col', !detailPanel && 'rounded-xl', !detailPanel && !inPanel && panelOpen && 'bg-panel-warm ring-2 ring-accent-2/25', !compact && 'mx-auto w-full')}>
                {message.kind === 'first_read' ? <FirstReadCard message={message} onOpenTarget={onOpenTarget} /> : <ProactiveMessage item={item} message={message} onOpenTarget={onOpenTarget} topicIds={item.topicIds} panelSurface={panelSurface} compact={compact} detailPanel={detailPanel} inPanel={inPanel} titleInHeader={titleInHeader} />}
            </article>
        </>
    );
}

/** Where a status line's Show goes. A missed brief opens Today on the brief's
 *  state and starts the brief that is on its way: plain Today is already
 *  beside the chat there, so it would do nothing. */
function statusEntryTarget(entry: MainChatStatusPayload['entries'][number]): MainChatStatusPayload['entries'][number]['target'] {
    if (entry.kind === 'brief_missed') return { ...entry.target, kind: 'today', id: TODAY_BRIEF_STATUS_TARGET };
    if (entry.kind === 'capture_paused') return { kind: 'settings', id: 'activity' };
    return entry.target;
}

function MainChatStatusEntry({ entry, expanded, onOpenTarget, today, day }: {
    today: boolean;
    /** The day the line was written ("YYYY-MM-DD"); its day words are read from it. */
    day?: string;
    entry: MainChatStatusPayload['entries'][number];
    expanded: boolean;
    onOpenTarget?: MainChatProactiveProps['onOpenTarget'];
}) {
    const { t, i18n } = useTranslation('chat');
    const { t: tToday } = useTranslation('today');
    const place = statusEntryPlace(entry);
    const source = place ? sourceLabel(place, t) : null;
    // The entry is delivered with its folded row, but opening it requires
    // the actual text to be expanded into the focused viewport.
    const receiptRef = useMainChatProactiveReceipts<HTMLLIElement>(entry.momentId ? entry.id : null, expanded);
    const view = useMainChatStreamView();
    const rawText = entry.localized?.[copyLanguage(i18n.resolvedLanguage)] ?? entry.text;
    const pause = entry.kind === 'capture_paused';
    const lang = i18n.resolvedLanguage ?? 'en';
    // A missed brief's line is about the brief, so it names no source (H9).
    const missed = entry.kind === 'brief_missed';
    const missedText = missed ? mainChatBriefMissedText(entry, view?.today ?? localDayKey(new Date()), lang, t as (key: string, options?: Record<string, unknown>) => string, tToday as (key: string, options?: Record<string, unknown>) => string) : null;
    const text = pause
        ? pauseLine(t, entry.captureSource, entry.captureCause, entry.captureStartAt, entry.captureEndAt, i18n.resolvedLanguage ?? 'en')
        : missedText ? missedText
        : (entry.kind === 'brief_ready' || entry.kind === 'weekly_recap') && retiredBriefCopy(rawText)
            ? tToday(entry.kind === 'brief_ready' ? 'todayBrief.slot.on_request' : 'todayBrief.kind.week')
            : mainChatPastLineText(rawText, day, view?.today ?? localDayKey(new Date()), i18n.resolvedLanguage ?? 'en', t as (key: string, options?: Record<string, unknown>) => string);
    return (
        <li ref={receiptRef} data-background-line className={BACKGROUND_LINE_CLASS}>
            {expanded && <>
                <span className="min-w-0 flex-1 break-words"><span>{text}</span>{source && !pause && !missed && <> · {t('mainChat.proactive.sourceFrom', { source })}</>}</span>
                {onOpenTarget && mainChatTargetOpens(statusEntryTarget(entry)) && !(pause && entry.captureEndAt) && <button type="button" className="shrink-0 cursor-pointer rounded-sm text-accent underline-offset-2 hover:text-accent hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent-2/40" onClick={() => today ? measureToday("T70", "open", "opened", () => onOpenTarget(statusEntryTarget(entry))) : onOpenTarget(statusEntryTarget(entry))}>{pause ? t('mainChat.observationPause.turnBackOn') : t('mainChat.card.button.show')}</button>}
            </>}
        </li>
    );
}

export function MainChatStatusRow({ item, onOpenTarget, compact = false, defaultExpanded = false, flat = false, panelSurface = "chat" }: MainChatProactiveProps & {
    /** Inside the main chat's day fold the entries show at once: the fold
     *  was the click that opened them. */
    defaultExpanded?: boolean;
    /** A day fold that holds only background lines lists them right under
     *  its own summary, with no inner "{n} background updates" (C3). */
    flat?: boolean;
}) {
    const { t } = useTranslation('chat');
    const [expanded, setExpanded] = useState(defaultExpanded);
    const detailsId = useId();
    const status = getMainChatStatus(item);
    if (!status || status.entries.length === 0) return null;
    if (flat) return (
        <ul data-main-chat-status={status.period} className="flex flex-col gap-3">
            {status.entries.map((entry) => <MainChatStatusEntry key={entry.id} entry={entry} expanded onOpenTarget={onOpenTarget} today={panelSurface === "today"} day={item.localDay} />)}
        </ul>
    );
    return (
        <div data-main-chat-status={status.period} className={cn('text-[11px] leading-relaxed text-muted', !compact && 'mx-auto w-full max-w-[90%]')}>
            <button type="button" aria-expanded={expanded} aria-controls={expanded ? detailsId : undefined} className="mx-auto flex max-w-full cursor-pointer items-center justify-center gap-1 rounded-md px-2 py-1 text-muted hover:bg-ink/[0.03] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent-2/40" onClick={() => panelSurface === "today" ? measureToday("T69", "open", "opened", () => setExpanded(!expanded)) : setExpanded(!expanded)}>
                <span className="truncate">{t('mainChat.status.summary', { count: status.entries.length })}</span>
                <ChevronDown aria-hidden="true" className={cn('size-3 shrink-0 transition-transform', expanded && 'rotate-180')} />
            </button>
            <ul id={detailsId} hidden={!expanded} className="mt-1 flex flex-col gap-3">
                {status.entries.map((entry) => <MainChatStatusEntry key={entry.id} entry={entry} expanded={expanded} onOpenTarget={onOpenTarget} today={panelSurface === "today"} day={item.localDay} />)}
            </ul>
        </div>
    );
}
