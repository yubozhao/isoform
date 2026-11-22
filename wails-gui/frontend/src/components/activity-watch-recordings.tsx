import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Bookmark, ChevronLeft, Film } from 'lucide-react';
import { ActivityInlineNotice } from '@/components/activity-inline-notice';
import { hostBridgeMediaURL } from '@/lib/host-bridge-media';
import { ActivityWatchLane } from '@/components/activity-watch-lane';
import { ActivityFeedRowItem, feedLine, feedTitle, useActivityForget } from '@/components/activity-feed';
import {
    GetActivityWatchRows,
    GetWatchConversations,
    GetWatchRecordings,
    type ActivityFeedRow,
    type WatchConversation,
    type WatchRecording,
    type WatchRecordingLine,
    type WatchRecordingPart,
} from '@/lib/wails-shim-app';

// The Watch tab (computer-activity v1.0.0 rule 4): the same rows as the
// day's timeline, for the days the watch keeps, newest first. A row is a
// conversation by name and length, with who spoke and its marks and note,
// or an hour in which nothing was said. Each has Forget, the one Forget of
// Activity (15e). Opening a row shows its page: the notes, what was said
// with the marks and the names given to its speakers, the audio of the hours
// it draws on, and the Screen timeline at its time. The hours lane stays on
// top.

const REFRESH_MS = 60_000;
// The days the watch and the phone list (watch_conversations.go).
const WATCH_DAYS = 7;

function formatClock(ms: number, lang: string): string {
    return new Date(ms).toLocaleTimeString(lang, { hour: 'numeric', minute: '2-digit' });
}

/** 75 -> "1:15", 3725 -> "1:02:05". */
export function formatRecordingLength(seconds: number): string {
    const total = Math.max(0, Math.round(seconds));
    const pad = (n: number) => String(n).padStart(2, '0');
    const h = Math.floor(total / 3600);
    const m = Math.floor((total % 3600) / 60);
    return h > 0 ? `${h}:${pad(m)}:${pad(total % 60)}` : `${m}:${pad(total % 60)}`;
}

function dayKey(ms: number): string {
    const d = new Date(ms);
    return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`;
}

type StateKey = 'receiving' | 'onComputer' | 'transcribed' | 'nothingSaid' | 'failed';

export function watchRecordingStateKey(rec: WatchRecording): StateKey {
    switch (rec.state) {
        case 'transcribed':
            return rec.words > 0 ? 'transcribed' : 'nothingSaid';
        case 'failed':
            return 'failed';
        case 'received':
            return rec.parts.length > 0 && rec.hasAudio && rec.sessionId === '' ? 'onComputer' : 'receiving';
        default:
            return 'onComputer';
    }
}

function watchWindow(): [number, number] {
    const now = new Date();
    const start = new Date(now.getFullYear(), now.getMonth(), now.getDate() - WATCH_DAYS).getTime();
    const end = new Date(now.getFullYear(), now.getMonth(), now.getDate() + 1).getTime();
    return [start, end];
}

export function ActivityWatchRecordings({
    bridgePort,
    onOpenSession,
    onCount,
    openConversationId,
}: {
    bridgePort: number;
    onOpenSession: (sessionId: string) => void;
    /** Reports how many recordings there are, so the section can hide its door at zero. */
    onCount?: (count: number) => void;
    /** A conversation to open on arrival, from a row of the day's timeline
     *  or a link elsewhere (Chat's watch source). One that no longer
     *  resolves leaves the list with a quiet line, never a blank page. */
    openConversationId?: string | null;
}) {
    const { t, i18n } = useTranslation('activity');
    const [rows, setRows] = useState<ActivityFeedRow[] | null>(null);
    const [rowsFailed, setRowsFailed] = useState(false);
    const [conversations, setConversations] = useState<WatchConversation[] | null>(null);
    const [conversationsFailed, setConversationsFailed] = useState(false);
    const [recordings, setRecordings] = useState<WatchRecording[] | null>(null);
    const [recordingsFailed, setRecordingsFailed] = useState(false);
    const [openId, setOpenId] = useState<string | null>(openConversationId ?? null);
    // The conversation a link asked for, until the rows say whether it is kept.
    const [arriving, setArriving] = useState<string | null>(openConversationId ?? null);
    const [arrivalGone, setArrivalGone] = useState(false);
    const pageRef = useRef<HTMLDivElement>(null);

    useEffect(() => {
        if (!openConversationId) return;
        setOpenId(openConversationId);
        setArriving(openConversationId);
        setArrivalGone(false);
    }, [openConversationId]);

    useEffect(() => {
        if (!arriving || rows === null) return;
        if (rows.some((row) => row.id === arriving)) {
            pageRef.current?.scrollIntoView?.({ block: 'start' });
        } else {
            setArrivalGone(true);
            setOpenId((current) => (current === arriving ? null : current));
        }
        setArriving(null);
    }, [arriving, rows]);

    const load = useCallback(() => {
        const [start, end] = watchWindow();
        GetActivityWatchRows(start, end)
            .then((list) => {
                setRows(list ?? []);
                setRowsFailed(false);
            })
            .catch(() => setRowsFailed(true));
        GetWatchConversations()
            .then((list) => {
                setConversations(list ?? []);
                setConversationsFailed(false);
            })
            .catch(() => setConversationsFailed(true));
        GetWatchRecordings()
            .then((list) => {
                setRecordings(list ?? []);
                setRecordingsFailed(false);
                onCount?.((list ?? []).length);
            })
            .catch(() => setRecordingsFailed(true));
    }, [onCount]);

    useEffect(() => {
        load();
        const timer = window.setInterval(load, REFRESH_MS);
        window.addEventListener('focus', load);
        return () => {
            window.clearInterval(timer);
            window.removeEventListener('focus', load);
        };
    }, [load]);

    const forget = useActivityForget((row) => {
        // No Undo: the row leaves at once, and its page with it (15e rule 4).
        setRows((prev) => (prev ?? []).filter((r) => r.id !== row.id));
        setOpenId((current) => (current === row.id ? null : current));
    }, load);

    const days = useMemo(() => {
        const groups: { key: string; at: number; items: ActivityFeedRow[] }[] = [];
        for (const row of rows ?? []) {
            const key = dayKey(row.startMs);
            const last = groups[groups.length - 1];
            if (last && last.key === key) last.items.push(row);
            else groups.push({ key, at: row.startMs, items: [row] });
        }
        return groups;
    }, [rows]);

    const dayLabel = (at: number, key: string) => {
        const now = Date.now();
        if (key === dayKey(now)) return t('groupHeaders.today');
        if (key === dayKey(now - 86_400_000)) return t('groupHeaders.yesterday');
        return new Date(at).toLocaleDateString(i18n.language, { weekday: 'long', month: 'long', day: 'numeric' });
    };

    const openRow = openId ? (rows ?? []).find((row) => row.id === openId) ?? null : null;

    return (
        <div className="flex-1 overflow-y-auto" data-testid="watch-recordings">
            <div ref={pageRef} className="mx-auto px-6 py-6 space-y-6 max-w-2xl">
                {forget.notice}
                {openId ? (
                    <WatchRowPage
                        id={openId}
                        row={openRow}
                        rowsLoaded={rows !== null}
                        rowsFailed={rowsFailed}
                        conversations={conversations}
                        conversationsFailed={conversationsFailed}
                        recordings={recordings}
                        recordingsFailed={recordingsFailed}
                        bridgePort={bridgePort}
                        onOpenSession={onOpenSession}
                        onBack={() => {
                            setOpenId(null);
                            setArriving(null);
                        }}
                        onForget={forget.ask}
                        onRetry={load}
                    />
                ) : (
                    <>
                        <header>
                            <h2 className="text-[17px] font-semibold text-ink">{t('watch.title')}</h2>
                            <p className="mt-1 text-[12.5px] text-muted">{t('watch.subtitle')}</p>
                        </header>
                        <ActivityWatchLane />
                        {arrivalGone && (
                            <p className="text-[12.5px] text-muted" data-testid="watch-conversation-gone">{t('act.conversationGone')}</p>
                        )}
                        {rowsFailed && (
                            <ActivityInlineNotice notice={{ tone: 'error', text: t('watch.rowsFailed'), onAction: load }} />
                        )}
                        {rows !== null && rows.length === 0 && !rowsFailed && (
                            <p className="text-[12.5px] text-muted">{t('watch.empty')}</p>
                        )}
                        {days.map((day) => (
                            <section key={day.key} className="space-y-1">
                                <h3 className="px-3 text-[11px] font-semibold uppercase tracking-[0.08em] text-muted">
                                    {dayLabel(day.at, day.key)}
                                </h3>
                                <ol className="flex flex-col" data-testid="watch-rows">
                                    {day.items.map((row) => (
                                        <ActivityFeedRowItem
                                            key={row.id}
                                            row={row}
                                            onForget={forget.ask}
                                            onOpen={(opened) => setOpenId(opened.id)}
                                        />
                                    ))}
                                </ol>
                            </section>
                        ))}
                    </>
                )}
            </div>
            {forget.dialog}
        </div>
    );
}

/** One watch row's page: a conversation's notes and words, or an hour; the
 *  audio of the hours it draws on; the Screen timeline; and Forget. */
function WatchRowPage({
    id,
    row,
    rowsLoaded,
    rowsFailed,
    conversations,
    conversationsFailed,
    recordings,
    recordingsFailed,
    bridgePort,
    onOpenSession,
    onBack,
    onForget,
    onRetry,
}: {
    id: string;
    row: ActivityFeedRow | null;
    rowsLoaded: boolean;
    rowsFailed: boolean;
    conversations: WatchConversation[] | null;
    conversationsFailed: boolean;
    recordings: WatchRecording[] | null;
    recordingsFailed: boolean;
    bridgePort: number;
    onOpenSession: (sessionId: string) => void;
    onBack: () => void;
    onForget: (row: ActivityFeedRow, from: HTMLElement) => void;
    onRetry: () => void;
}) {
    const { t, i18n } = useTranslation('activity');
    const conversation = row?.conversation ? (conversations ?? []).find((c) => c.id === id) ?? null : null;
    const recordingIds = row?.conversation ? conversation?.recordingIds ?? [] : row?.recordingId ? [row.recordingId] : [];
    const hours = (recordings ?? []).filter((rec) => recordingIds.includes(rec.id));
    const timelineSession = hours.find((rec) => rec.sessionId !== '')?.sessionId ?? '';
    const back = (
        <button
            type="button"
            onClick={onBack}
            className="inline-flex items-center gap-1 rounded-md px-1.5 py-0.5 text-[12px] text-muted hover:text-ink hover:bg-surface-hover transition-colors"
        >
            <ChevronLeft className="h-3.5 w-3.5" aria-hidden />
            {t('watch.back')}
        </button>
    );
    if (!row) {
        return (
            <section className="space-y-4" data-testid="watch-row-page">
                {back}
                {rowsFailed ? (
                    <ActivityInlineNotice notice={{ tone: 'error', text: t('watch.pageFailed'), onAction: onRetry }} />
                ) : rowsLoaded ? (
                    <p className="text-[12.5px] text-muted">{t('watch.pageGone')}</p>
                ) : null}
            </section>
        );
    }
    const line = feedLine(t, row, i18n.language);
    return (
        <section className="space-y-5" data-testid="watch-row-page" data-conversation-id={row.conversation ? row.id : undefined}>
            {back}
            <header className="space-y-1">
                <h2 className="text-[17px] font-semibold text-ink break-words">{feedTitle(t, row)}</h2>
                <p className="text-[12.5px] text-muted tabular-nums">
                    {formatClock(row.startMs, i18n.language)}
                    {line ? ` · ${line}` : ''}
                </p>
            </header>
            {row.conversation && (
                conversationsFailed ? (
                    <ActivityInlineNotice notice={{ tone: 'error', text: t('watch.pageFailed'), onAction: onRetry }} />
                ) : conversations !== null && conversation === null ? (
                    <p className="text-[12.5px] text-muted">{t('watch.pageGone')}</p>
                ) : conversation ? (
                    <div className="space-y-4 select-text">
                        <ConversationNotes conversation={conversation} />
                        <div className="space-y-1.5">
                            <h3 className="text-[11px] font-medium text-muted">{t('watch.heard')}</h3>
                            {conversation.lines.length === 0 ? (
                                <p className="text-[12px] text-muted">{t('watch.transcriptEmpty')}</p>
                            ) : (
                                <TranscriptLines lines={conversation.lines} names={conversation.speakerNames ?? {}} />
                            )}
                        </div>
                    </div>
                ) : null
            )}
            {!row.conversation && (row.words ?? 0) === 0 && <p className="text-[12.5px] text-muted">{t('watch.noTalk')}</p>}
            {recordingsFailed ? (
                <ActivityInlineNotice notice={{ tone: 'error', text: t('watch.loadFailed'), onAction: onRetry }} />
            ) : (
                hours.map((rec) => {
                    const startedAt = Date.parse(rec.startedAt);
                    return (
                        <div key={rec.id} className="space-y-2 border-t border-border/60 pt-3" data-recording-id={rec.id}>
                            <p className="text-[11px] font-medium text-muted tabular-nums">
                                {Number.isNaN(startedAt) ? t('watch.untimed') : formatClock(startedAt, i18n.language)}
                                <span className="ml-2 font-normal">
                                    {t('watch.meta', { duration: formatRecordingLength(rec.durationSeconds), state: t(`watch.state.${watchRecordingStateKey(rec)}`) })}
                                </span>
                            </p>
                            {rec.parts.map((part, index) => (
                                <PartRow key={part.part} recordingId={rec.id} part={part} index={index} total={rec.parts.length} bridgePort={bridgePort} />
                            ))}
                            {!rec.hasAudio && rec.state === 'transcribed' && <p className="text-[11.5px] text-muted">{t('watch.audioGone')}</p>}
                            {rec.ownerVoice === 'not_enrolled' && <p className="text-[10.5px] text-muted/80">{t('watch.voiceNotEnrolled')}</p>}
                        </div>
                    );
                })
            )}
            <div className="flex flex-wrap items-center justify-between gap-x-3 gap-y-1 border-t border-border/60 pt-3">
                {timelineSession !== '' ? (
                    <button
                        type="button"
                        onClick={() => onOpenSession(timelineSession)}
                        className="inline-flex items-center gap-1 rounded-md px-1.5 py-0.5 text-[11px] text-muted hover:text-ink hover:bg-surface-hover transition-colors"
                    >
                        <Film className="h-3 w-3" aria-hidden />
                        {t('watch.openTimeline')}
                    </button>
                ) : (
                    <span />
                )}
                <button
                    type="button"
                    onClick={(event) => onForget(row, event.currentTarget)}
                    aria-label={t('feed.forgetRow', { title: feedTitle(t, row) })}
                    className="rounded-md px-2 py-1 text-[12px] text-muted hover:text-ink hover:bg-surface-active focus-visible:ring-2 focus-visible:ring-border-focus transition-colors"
                >
                    {t('feed.forget')}
                </button>
            </div>
        </section>
    );
}

/** What a longer conversation was about, what was decided and what has to happen next. Nothing when it has no notes. */
function ConversationNotes({ conversation }: { conversation: WatchConversation }) {
    const { t } = useTranslation('activity');
    const summary = conversation.summary ?? '';
    const decisions = conversation.decisions ?? [];
    const followups = conversation.followups ?? [];
    if (summary === '' && decisions.length === 0 && followups.length === 0) return null;
    return (
        <div className="space-y-2 border-b border-border/60 pb-3" data-testid="watch-conversation-notes">
            {summary !== '' && <p className="text-[12.5px] leading-relaxed text-ink">{summary}</p>}
            {decisions.length > 0 && (
                <div className="space-y-1">
                    <h6 className="text-[11px] font-medium text-muted">{t('meetingNotes.decisions')}</h6>
                    <ul className="space-y-0.5 text-[12.5px] leading-relaxed text-ink">
                        {decisions.map((decision) => (
                            <li key={decision}>{decision}</li>
                        ))}
                    </ul>
                </div>
            )}
            {followups.length > 0 && (
                <div className="space-y-1">
                    <h6 className="text-[11px] font-medium text-muted">{t('meetingNotes.followups')}</h6>
                    <ul className="space-y-0.5 text-[12.5px] leading-relaxed text-ink">
                        {followups.map((followup) => (
                            <li key={`${followup.owner}-${followup.text}`}>
                                <span className="text-muted">
                                    {followup.owner === 'me'
                                        ? t('meetingNotes.ownerMe')
                                        : followup.person !== ''
                                          ? followup.person
                                          : t('meetingNotes.ownerSomeone')}
                                    {': '}
                                </span>
                                {followup.text}
                                {followup.due !== '' && (
                                    <span className="text-muted"> {t('meetingNotes.dueSuffix', { due: followup.due })}</span>
                                )}
                            </li>
                        ))}
                    </ul>
                </div>
            )}
        </div>
    );
}

function TranscriptLines({ lines, names }: { lines: WatchRecordingLine[]; names: Record<string, string> }) {
    const { t, i18n } = useTranslation('activity');
    return (
        <ul className="space-y-1.5">
            {lines.map((line) => (
                <li key={`${line.at}-${line.text}`} className="flex items-start gap-2 text-[12.5px] leading-relaxed text-ink" data-marked={line.marked ? 'true' : undefined}>
                    <span className="mt-[3px] w-[44px] shrink-0 text-[10.5px] text-muted tabular-nums">
                        {formatClock(line.at, i18n.language)}
                    </span>
                    {line.speaker !== '' && (
                        <span className="mt-[2px] shrink-0 rounded-full bg-panel-warm px-1.5 py-0.5 text-[10.5px] leading-none text-muted dark:bg-panel dark:text-muted">
                            {speakerName(line.speaker, t, names)}
                        </span>
                    )}
                    <span className="min-w-0">
                        {line.text}
                        {line.marked && (
                            <span className="ml-2 inline-flex items-center gap-0.5 align-baseline text-[10.5px] text-muted">
                                <Bookmark className="h-2.5 w-2.5" aria-hidden />
                                {t('watch.marked')}
                            </span>
                        )}
                    </span>
                </li>
            ))}
        </ul>
    );
}

function speakerName(speaker: string, t: (key: never, options?: never) => unknown, names: Record<string, string> = {}): string {
    if (speaker === 'you') return t('callTurns.you' as never) as string;
    if ((names[speaker] ?? '').trim() !== '') return names[speaker].trim();
    const number = speaker.startsWith('speaker_') ? speaker.slice('speaker_'.length) : speaker;
    return t('callTurns.speaker' as never, { number } as never) as string;
}

function PartRow({
    recordingId,
    part,
    index,
    total,
    bridgePort,
}: {
    recordingId: string;
    part: WatchRecordingPart;
    index: number;
    total: number;
    bridgePort: number;
}) {
    const { t, i18n } = useTranslation('activity');
    const [audioFailed, setAudioFailed] = useState(false);
    const startedAt = Date.parse(part.startedAt);
    const context = [
        part.place !== '' ? t('watch.place', { place: part.place }) : '',
        part.heartRate !== ''
            ? t(`watch.heartRate.${part.heartRate as 'calm' | 'raised' | 'high'}`, {
                  avg: part.heartRateAvg,
                  peak: part.heartRateMax,
              })
            : '',
    ].filter((piece) => piece !== '');
    return (
        <div className="space-y-1.5" data-recording-part={part.part}>
            <p className="text-[11px] font-medium text-muted tabular-nums">
                {total > 1
                    ? t('watch.part', {
                          index: index + 1,
                          time: Number.isNaN(startedAt) ? '' : formatClock(startedAt, i18n.language),
                          duration: formatRecordingLength(part.durationSeconds),
                      })
                    : null}
                {context.length > 0 && (
                    <span className={total > 1 ? 'ml-2 font-normal' : 'font-normal'}>{context.join(' · ')}</span>
                )}
            </p>
            {part.hasAudio && bridgePort > 0 && !audioFailed && (
                <audio
                    controls
                    preload="none"
                    src={hostBridgeMediaURL(bridgePort, 'recording', { id: recordingId, part: part.part })}
                    onError={() => setAudioFailed(true)}
                    aria-label={t('watch.audioLabel', { index: index + 1 }) as string}
                    className="w-full h-8"
                />
            )}
            {part.hasAudio && audioFailed && <p className="text-[11.5px] text-muted">{t('watch.audioUnavailable')}</p>}
        </div>
    );
}
