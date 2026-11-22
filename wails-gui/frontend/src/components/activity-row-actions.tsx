import { useEffect, useRef, useState, type KeyboardEvent, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { Bell, Check, ListPlus, MessageSquare, PencilLine } from 'lucide-react';
import { ActivityAskInline } from './activity-ask-inline';
import { ActivityInlineNotice, errorNotice, type ActivityNotice } from './activity-inline-notice';
import type { TodayChatContextRef } from '@/lib/today-chat-navigation';
import {
    AddActivityFollowUp,
    CancelActivityReminder,
    CorrectActivity,
    GetActivityReminderChoices,
    SetActivityReminder,
    UndoActivityFollowUp,
    type ActivityFollowUp,
    type ActivityReminderChoice,
    type ActivityView,
} from '@/lib/wails-shim-app';
import { Button } from '@/components/ui/button';
import {
    DropdownMenu,
    DropdownMenuContent,
    DropdownMenuItem,
    DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu';

// The actions in an opened "What you worked on" row (backlog item 62, step 3):
// Add follow-up, Remind me and Not right. Go prepares the follow-up line and
// the quick reminder times and keeps every rule; this only shows them.

const actionClass =
    'inline-flex items-center gap-1 text-[11px] text-muted hover:text-ink transition-colors disabled:pointer-events-none';
const inputClass =
    'w-full px-3 py-1.5 text-xs rounded-md border border-border bg-panel text-ink placeholder:text-muted focus:outline-none focus:ring-1 focus:ring-accent';

type Editor = 'none' | 'followUp' | 'pickTime' | 'correct' | 'ask';

function newKey(): string {
    return typeof crypto !== 'undefined' && 'randomUUID' in crypto
        ? crypto.randomUUID()
        : `${Date.now()}-${Math.random().toString(36).slice(2)}`;
}

/** "9:00 AM" today, "tomorrow at 9:00 AM", or "Oct 23 at 9:00 AM". */
export function useReminderWhen() {
    const { t, i18n } = useTranslation('activity');
    return (atMs: number) => {
        const at = new Date(atMs);
        const time = at.toLocaleTimeString(i18n.language, { hour: 'numeric', minute: '2-digit' });
        const today = new Date();
        const tomorrow = new Date(today.getFullYear(), today.getMonth(), today.getDate() + 1);
        if (at.toDateString() === today.toDateString()) return t('rowActions.whenToday', { time });
        if (at.toDateString() === tomorrow.toDateString()) return t('rowActions.whenTomorrow', { time });
        const date = at.toLocaleDateString(i18n.language, { month: 'short', day: 'numeric' });
        return t('rowActions.whenOther', { date, time });
    };
}

/** A datetime-local value for a time, in local time. */
function toLocalInputValue(ms: number): string {
    const d = new Date(ms);
    const pad = (n: number) => String(n).padStart(2, '0');
    return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

/** Remind me on one moment (design T12): Go's quick times when the menu
 *  opens, or any picked time, and Cancel while one is pending. A reminder
 *  you set yourself is your own alarm. The one flow, shared by the opened
 *  "What you worked on" row and the feed row's •••. */
export function useActivityReminder({ activityId, pendingAt, onChanged, onNotice, onSettled }: {
    activityId: string;
    /** The pending reminder's time, from Go; none when 0 or undefined. */
    pendingAt?: number;
    onChanged: () => void;
    onNotice: (notice: ActivityNotice | null) => void;
    /** Called when a reminder was set from the picker, to close it. */
    onSettled?: () => void;
}) {
    const { t } = useTranslation('activity');
    const reminderWhen = useReminderWhen();
    const [busy, setBusy] = useState(false);
    const [reminderAt, setReminderAt] = useState<number | undefined>(pendingAt || undefined);
    useEffect(() => setReminderAt(pendingAt || undefined), [pendingAt]);
    const [choices, setChoices] = useState<ActivityReminderChoice[]>([]);
    const [pickedTime, setPickedTime] = useState('');
    const loadChoices = (open: boolean) => {
        if (!open) return;
        GetActivityReminderChoices()
            .then((rows) => setChoices(rows ?? []))
            .catch(() => {
                setChoices([]);
                onNotice(errorNotice(t('rowActions.choicesFailed'), () => loadChoices(true)));
            });
    };
    const setReminder = async (atMs: number) => {
        if (busy) return;
        setBusy(true);
        try {
            const at = await SetActivityReminder(activityId, atMs);
            setReminderAt(at);
            onSettled?.();
            onChanged();
            onNotice({ tone: 'done', text: t('rowActions.reminderSetToast', { when: reminderWhen(at) }) });
        } catch {
            onNotice(errorNotice(t('rowActions.reminderFailed'), () => void setReminder(atMs)));
        } finally {
            setBusy(false);
        }
    };
    /** Starts the picker at tomorrow morning, else an hour from now. */
    const startPick = () => {
        const tomorrow = choices.find((c) => c.id === 'tomorrow_morning');
        setPickedTime(toLocalInputValue(tomorrow?.atMs ?? Date.now() + 3_600_000));
    };
    const pickedMs = pickedTime ? new Date(pickedTime).getTime() : NaN;
    const pickedValid = Number.isFinite(pickedMs) && pickedMs > Date.now() + 60_000;
    const cancelReminder = async () => {
        if (busy) return;
        setBusy(true);
        try {
            await CancelActivityReminder(activityId);
            setReminderAt(undefined);
            onChanged();
            onNotice({ tone: 'done', text: t('rowActions.reminderCancelledToast') });
        } catch {
            onNotice(errorNotice(t('rowActions.cancelReminderFailed'), () => void cancelReminder()));
        } finally {
            setBusy(false);
        }
    };
    return { busy, reminderAt, choices, loadChoices, setReminder, startPick, pickedTime, setPickedTime, pickedMs, pickedValid, cancelReminder };
}

export type ActivityReminder = ReturnType<typeof useActivityReminder>;

/** The quick times and Pick a time, as menu items. */
export function ReminderChoiceItems({ reminder, onPickTime }: { reminder: ActivityReminder; onPickTime: () => void }) {
    const { t } = useTranslation('activity');
    const reminderWhen = useReminderWhen();
    return (
        <>
            {reminder.choices.map((choice) => (
                <DropdownMenuItem key={choice.id} onSelect={() => void reminder.setReminder(choice.atMs)}>
                    {choice.id === 'later_today'
                        ? t('rowActions.laterToday', { when: reminderWhen(choice.atMs) })
                        : t('rowActions.tomorrowMorning', { when: reminderWhen(choice.atMs) })}
                </DropdownMenuItem>
            ))}
            <DropdownMenuItem onSelect={onPickTime}>{t('rowActions.pickTime')}</DropdownMenuItem>
        </>
    );
}

/** Pick a time: any time from a minute ahead. */
export function ReminderTimePicker({ reminder, onClose, disabled }: { reminder: ActivityReminder; onClose: () => void; disabled?: boolean }) {
    const { t } = useTranslation('activity');
    const set = () => {
        if (reminder.pickedValid) void reminder.setReminder(reminder.pickedMs);
    };
    return (
        <div className="flex items-center gap-2">
            <input
                type="datetime-local"
                autoFocus
                aria-label={t('rowActions.pickTime')}
                className={inputClass}
                value={reminder.pickedTime}
                min={toLocalInputValue(Date.now())}
                onChange={(e) => reminder.setPickedTime(e.target.value)}
                onKeyDown={(e) => {
                    if (e.key === 'Enter' && !e.nativeEvent.isComposing) {
                        e.preventDefault();
                        set();
                    } else if (e.key === 'Escape') {
                        e.preventDefault();
                        onClose();
                    }
                }}
            />
            <Button size="sm" onClick={set} disabled={disabled || reminder.busy || !reminder.pickedValid}>
                {t('rowActions.setReminder')}
            </Button>
            <Button size="sm" variant="ghost" onClick={onClose}>
                {t('activityCard.cancel')}
            </Button>
        </div>
    );
}

export function ActivityRowActions({
    activity,
    onChanged,
    chatContext,
    deleteControl,
    threadControl,
}: {
    activity: ActivityView;
    /** Reloads the day's activities after a change Go made. */
    onChanged: () => void;
    /** Chat about this, asked in place with the moment as the chat's
     *  context. Absent where the page has no main chat. */
    chatContext?: TodayChatContextRef;
    /** The existing Delete from history control, kept as it was. */
    deleteControl: ReactNode;
    /** Not right also moves the moment to another thread or into its own. */
    threadControl?: ReactNode;
}) {
    const { t } = useTranslation('activity');
    const reminderWhen = useReminderWhen();
    const [editor, setEditor] = useState<Editor>('none');
    const [notice, setNotice] = useState<ActivityNotice | null>(null);
    const [busy, setBusy] = useState(false);
    const [followUp, setFollowUp] = useState<ActivityFollowUp | undefined>(activity.followUp);
    useEffect(() => setFollowUp(activity.followUp), [activity.followUp?.openLoopId, activity.followUp?.eventId]);
    const reminder = useActivityReminder({
        activityId: activity.id,
        pendingAt: activity.reminderAt,
        onChanged,
        onNotice: setNotice,
        onSettled: () => setEditor('none'),
    });
    const reminderAt = reminder.reminderAt;
    const openPickTime = () => {
        reminder.startPick();
        setEditor('pickTime');
    };

    // Follow-up editor: one line, prefilled with Go's text, editable.
    const [followUpDraft, setFollowUpDraft] = useState('');
    const followUpKey = useRef('');
    const openFollowUp = () => {
        setFollowUpDraft(activity.followUpText ?? '');
        followUpKey.current = newKey();
        setEditor('followUp');
    };
    const addFollowUp = async () => {
        const text = followUpDraft.trim();
        if (!text || busy) return;
        setBusy(true);
        try {
            const added = await AddActivityFollowUp(activity.id, text, followUpKey.current);
            setFollowUp(added);
            setEditor('none');
            onChanged();
            const undo = () => {
                UndoActivityFollowUp(added.openLoopId, added.eventId)
                    .then(() => {
                        setFollowUp(undefined);
                        onChanged();
                        setNotice({ tone: 'done', text: t('rowActions.followUpRemovedToast') });
                    })
                    .catch(() => setNotice(errorNotice(t('rowActions.undoFailed'), undo)));
            };
            setNotice({ tone: 'done', text: t('rowActions.followUpAddedToast'), actionLabel: t('rowActions.undo'), onAction: undo });
        } catch {
            setNotice(errorNotice(t('rowActions.followUpFailed'), () => void addFollowUp()));
        } finally {
            setBusy(false);
        }
    };

    // Not right: the title and summary, edited in place.
    const [titleDraft, setTitleDraft] = useState('');
    const [summaryDraft, setSummaryDraft] = useState('');
    const openCorrect = () => {
        setTitleDraft(activity.title);
        setSummaryDraft(activity.description);
        setEditor('correct');
    };
    const saveCorrection = async () => {
        if (!titleDraft.trim() || busy) return;
        setBusy(true);
        try {
            await CorrectActivity(activity.id, titleDraft, summaryDraft);
            setEditor('none');
            onChanged();
            setNotice({ tone: 'done', text: t('rowActions.correctionSavedToast') });
        } catch {
            setNotice(errorNotice(t('rowActions.correctionFailed'), () => void saveCorrection()));
        } finally {
            setBusy(false);
        }
    };

    const keys = (onEnter: () => void) => (e: KeyboardEvent<HTMLInputElement>) => {
        if (e.key === 'Enter' && !e.nativeEvent.isComposing) {
            e.preventDefault();
            onEnter();
        } else if (e.key === 'Escape') {
            e.preventDefault();
            setEditor('none');
        }
    };

    const withheld = activity.actionsWithheld;
    return (
        <div className="pt-1 space-y-2">
            {withheld && (
                <p className="text-[12px] text-muted">
                    {withheld === 'private' ? t('rowActions.withheldPrivate') : t('rowActions.withheldPaused')}
                </p>
            )}
            {reminderAt && !withheld && (
                <p className="flex items-center gap-2 text-[12px] text-muted">
                    <Bell className="h-3 w-3 shrink-0" />
                    <span>{t('rowActions.reminderSetFor', { when: reminderWhen(reminderAt) })}</span>
                    <button type="button" onClick={() => void reminder.cancelReminder()} disabled={busy || reminder.busy} className={actionClass}>
                        {t('rowActions.cancelReminder')}
                    </button>
                </p>
            )}
            <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
                {!withheld &&
                    (followUp ? (
                        <span className="inline-flex items-center gap-1 text-[11px] text-muted" title={followUp.text}>
                            <Check className="h-3 w-3" />
                            {t('rowActions.followUpAdded')}
                        </span>
                    ) : (
                        <button type="button" onClick={openFollowUp} disabled={busy} className={actionClass}>
                            <ListPlus className="h-3 w-3" />
                            {t('rowActions.addFollowUp')}
                        </button>
                    ))}
                {!withheld && (
                    <DropdownMenu onOpenChange={reminder.loadChoices}>
                        <DropdownMenuTrigger asChild>
                            <button type="button" disabled={busy || reminder.busy} className={actionClass}>
                                <Bell className="h-3 w-3" />
                                {t('rowActions.remindMe')}
                            </button>
                        </DropdownMenuTrigger>
                        <DropdownMenuContent align="start">
                            <ReminderChoiceItems reminder={reminder} onPickTime={openPickTime} />
                        </DropdownMenuContent>
                    </DropdownMenu>
                )}
                {!withheld && chatContext && (
                    <button
                        type="button"
                        onClick={() => setEditor((current) => (current === 'ask' ? 'none' : 'ask'))}
                        aria-expanded={editor === 'ask'}
                        className={actionClass}
                    >
                        <MessageSquare className="h-3 w-3" />
                        {t('rowActions.chatAboutThis')}
                    </button>
                )}
                <button type="button" onClick={openCorrect} disabled={busy} className={actionClass}>
                    <PencilLine className="h-3 w-3" />
                    {t('rowActions.notRight')}
                </button>
                <span className="ml-auto">{deleteControl}</span>
            </div>
            <ActivityInlineNotice notice={notice} />

            {editor === 'ask' && chatContext && <ActivityAskInline context={chatContext} onClose={() => setEditor('none')} />}

            {editor === 'followUp' && (
                <div className="flex items-center gap-2">
                    <input
                        type="text"
                        autoFocus
                        aria-label={t('rowActions.followUpLabel')}
                        className={inputClass}
                        value={followUpDraft}
                        placeholder={t('rowActions.followUpPlaceholder')}
                        onChange={(e) => setFollowUpDraft(e.target.value)}
                        onKeyDown={keys(() => void addFollowUp())}
                    />
                    <Button size="sm" onClick={() => void addFollowUp()} disabled={busy || !followUpDraft.trim()}>
                        {t('rowActions.add')}
                    </Button>
                    <Button size="sm" variant="ghost" onClick={() => setEditor('none')}>
                        {t('activityCard.cancel')}
                    </Button>
                </div>
            )}

            {editor === 'pickTime' && <ReminderTimePicker reminder={reminder} onClose={() => setEditor('none')} disabled={busy} />}

            {editor === 'correct' && (
                <div className="space-y-2">
                    {threadControl && <div>{threadControl}</div>}
                    <label className="block space-y-1">
                        <span className="text-[11px] text-muted">{t('rowActions.titleLabel')}</span>
                        <input
                            type="text"
                            autoFocus
                            className={inputClass}
                            value={titleDraft}
                            onChange={(e) => setTitleDraft(e.target.value)}
                            onKeyDown={keys(() => void saveCorrection())}
                        />
                    </label>
                    <label className="block space-y-1">
                        <span className="text-[11px] text-muted">{t('rowActions.summaryLabel')}</span>
                        <input
                            type="text"
                            className={inputClass}
                            value={summaryDraft}
                            onChange={(e) => setSummaryDraft(e.target.value)}
                            onKeyDown={keys(() => void saveCorrection())}
                        />
                    </label>
                    <div className="flex items-center gap-2">
                        <Button size="sm" onClick={() => void saveCorrection()} disabled={busy || !titleDraft.trim()}>
                            {t('rowActions.save')}
                        </Button>
                        <Button size="sm" variant="ghost" onClick={() => setEditor('none')}>
                            {t('activityCard.cancel')}
                        </Button>
                        <span className="text-[11px] text-muted">{t('rowActions.correctionHint')}</span>
                    </div>
                </div>
            )}
        </div>
    );
}
