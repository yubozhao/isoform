import { useCallback, useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Bookmark, ChevronDown, ChevronUp, Film, Trash2 } from 'lucide-react';
import { ConfirmDialog } from '@/components/confirm-dialog';
import { ActivityInlineNotice } from '@/components/activity-inline-notice';
import { hostBridgeMediaURL } from '@/lib/host-bridge-media';
import {
    DeleteWatchRecording,
    GetWatchRecordings,
    GetWatchRecordingTranscript,
    type WatchRecording,
    type WatchRecordingLine,
    type WatchRecordingPart,
} from '../../wailsjs/go/main/App';

// "From your watch": the recordings this computer received from the watch
// (through the phone), by day. Each one shows its parts with the place and
// heart rate line, plays its audio while the computer keeps it, shows what it
// heard with who said it, opens the Screen timeline at its time, and deletes
// everywhere. It is a plain list on purpose (watch handoff v1.0.0, section 8);
// the designed page replaces it.

const REFRESH_MS = 60_000;

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

export function ActivityWatchRecordings({
    bridgePort,
    onOpenSession,
    onCount,
}: {
    bridgePort: number;
    onOpenSession: (sessionId: string) => void;
    /** Reports how many recordings there are, so the section can hide its door at zero. */
    onCount?: (count: number) => void;
}) {
    const { t, i18n } = useTranslation('activity');
    const [recordings, setRecordings] = useState<WatchRecording[] | null>(null);
    const [failed, setFailed] = useState(false);

    const load = useCallback(() => {
        GetWatchRecordings()
            .then((list) => {
                setRecordings(list ?? []);
                setFailed(false);
                onCount?.((list ?? []).length);
            })
            .catch(() => setFailed(true));
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

    const days = useMemo(() => {
        const groups: { key: string; at: number; items: WatchRecording[] }[] = [];
        for (const rec of recordings ?? []) {
            const at = Date.parse(rec.startedAt);
            const key = Number.isNaN(at) ? '' : dayKey(at);
            const last = groups[groups.length - 1];
            if (last && last.key === key) last.items.push(rec);
            else groups.push({ key, at, items: [rec] });
        }
        return groups;
    }, [recordings]);

    const dayLabel = (at: number, key: string) => {
        const now = Date.now();
        if (key === dayKey(now)) return t('groupHeaders.today');
        if (key === dayKey(now - 86_400_000)) return t('groupHeaders.yesterday');
        return new Date(at).toLocaleDateString(i18n.language, { weekday: 'long', month: 'long', day: 'numeric' });
    };

    return (
        <div className="flex-1 overflow-y-auto" data-testid="watch-recordings">
            <div className="mx-auto px-6 py-6 space-y-6 max-w-2xl">
                <header>
                    <h2 className="text-[17px] font-semibold text-ink">{t('watch.title')}</h2>
                    <p className="mt-1 text-[12.5px] text-muted">{t('watch.subtitle')}</p>
                </header>
                {failed && (
                    <ActivityInlineNotice notice={{ tone: 'error', text: t('watch.loadFailed'), onAction: load }} />
                )}
                {recordings !== null && recordings.length === 0 && !failed && (
                    <p className="text-[12.5px] text-muted">{t('watch.empty')}</p>
                )}
                {days.map((day) => (
                    <section key={day.key} className="space-y-2">
                        <h3 className="text-[11px] font-semibold uppercase tracking-[0.08em] text-muted">
                            {dayLabel(day.at, day.key)}
                        </h3>
                        <div className="space-y-2">
                            {day.items.map((rec) => (
                                <RecordingCard
                                    key={rec.id}
                                    rec={rec}
                                    bridgePort={bridgePort}
                                    onOpenSession={onOpenSession}
                                    onDeleted={load}
                                />
                            ))}
                        </div>
                    </section>
                ))}
            </div>
        </div>
    );
}

function RecordingCard({
    rec,
    bridgePort,
    onOpenSession,
    onDeleted,
}: {
    rec: WatchRecording;
    bridgePort: number;
    onOpenSession: (sessionId: string) => void;
    onDeleted: () => void;
}) {
    const { t, i18n } = useTranslation('activity');
    const [open, setOpen] = useState(false);
    const [lines, setLines] = useState<WatchRecordingLine[] | null>(null);
    const [confirmDelete, setConfirmDelete] = useState(false);
    const [deleteFailed, setDeleteFailed] = useState(false);
    const startedAt = Date.parse(rec.startedAt);
    const stateKey = watchRecordingStateKey(rec);

    useEffect(() => {
        if (!open || rec.sessionId === '') return;
        let live = true;
        GetWatchRecordingTranscript(rec.id)
            .then((got) => live && setLines(got ?? []))
            .catch(() => live && setLines([]));
        return () => {
            live = false;
        };
    }, [open, rec.id, rec.sessionId]);

    const remove = () => {
        setDeleteFailed(false);
        DeleteWatchRecording(rec.id)
            .then(() => {
                setConfirmDelete(false);
                onDeleted();
            })
            .catch(() => setDeleteFailed(true));
    };

    return (
        <section className="rounded-lg border border-border bg-panel" data-recording-id={rec.id}>
            <button
                type="button"
                onClick={() => setOpen((o) => !o)}
                aria-expanded={open}
                className="w-full flex items-start gap-3 px-4 py-3 text-left rounded-lg hover:bg-ink/[0.02] transition-colors"
            >
                <div className="flex-1 min-w-0">
                    <h4 className="text-[13px] font-medium text-ink truncate">
                        {Number.isNaN(startedAt) ? t('watch.untimed') : formatClock(startedAt, i18n.language)}
                        <span className="ml-2 font-normal text-muted">
                            {rec.source === 'phone' ? t('watch.sourcePhone') : t('watch.sourceWatch')}
                        </span>
                    </h4>
                    <p className="mt-0.5 text-[11px] text-muted tabular-nums">
                        {t('watch.meta', {
                            duration: formatRecordingLength(rec.durationSeconds),
                            state: t(`watch.state.${stateKey}`),
                        })}
                    </p>
                </div>
                {open ? (
                    <ChevronUp className="h-3.5 w-3.5 mt-1 shrink-0 text-muted" />
                ) : (
                    <ChevronDown className="h-3.5 w-3.5 mt-1 shrink-0 text-muted" />
                )}
            </button>
            {open && (
                <div className="px-4 pb-4 space-y-4 select-text">
                    <div className="space-y-3">
                        {rec.parts.map((part, index) => (
                            <PartRow
                                key={part.part}
                                recordingId={rec.id}
                                part={part}
                                index={index}
                                total={rec.parts.length}
                                bridgePort={bridgePort}
                            />
                        ))}
                        {!rec.hasAudio && rec.state === 'transcribed' && (
                            <p className="text-[11.5px] text-muted">{t('watch.audioGone')}</p>
                        )}
                    </div>
                    {rec.sessionId !== '' && (
                        <div className="space-y-1.5 border-t border-border/60 pt-3">
                            <h5 className="text-[11px] font-medium text-muted">{t('watch.heard')}</h5>
                            {lines === null ? null : lines.length === 0 ? (
                                <p className="text-[12px] text-muted">{t('watch.transcriptEmpty')}</p>
                            ) : (
                                <ul className="space-y-1.5">
                                    {lines.map((line) => (
                                        <li key={`${line.at}-${line.text}`} className="flex items-start gap-2 text-[12.5px] leading-relaxed text-ink">
                                            <span className="mt-[3px] w-[44px] shrink-0 text-[10.5px] text-muted tabular-nums">
                                                {formatClock(line.at, i18n.language)}
                                            </span>
                                            {line.speaker !== '' && (
                                                <span className="mt-[2px] shrink-0 rounded-full bg-panel-warm px-1.5 py-0.5 text-[10.5px] leading-none text-muted dark:bg-panel dark:text-muted">
                                                    {speakerName(line.speaker, t)}
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
                            )}
                            {rec.ownerVoice === 'not_enrolled' && (
                                <p className="text-[10.5px] text-muted/80">{t('watch.voiceNotEnrolled')}</p>
                            )}
                        </div>
                    )}
                    <div className="flex flex-wrap items-center justify-between gap-x-3 gap-y-1">
                        {rec.sessionId !== '' ? (
                            <button
                                type="button"
                                onClick={() => onOpenSession(rec.sessionId)}
                                className="inline-flex items-center gap-1 rounded-md px-1.5 py-0.5 text-[11px] text-muted hover:text-ink hover:bg-border/40 transition-colors"
                            >
                                <Film className="h-3 w-3" />
                                {t('watch.openTimeline')}
                            </button>
                        ) : (
                            <span />
                        )}
                        <button
                            type="button"
                            onClick={() => setConfirmDelete(true)}
                            className="inline-flex items-center gap-1 rounded-md px-1.5 py-0.5 text-[11px] text-muted hover:text-ink hover:bg-border/40 transition-colors"
                        >
                            <Trash2 className="h-3 w-3" />
                            {t('watch.delete')}
                        </button>
                    </div>
                </div>
            )}
            <ConfirmDialog
                open={confirmDelete}
                onOpenChange={setConfirmDelete}
                title={t('watch.deleteTitle')}
                description={t('watch.deleteBody')}
                confirmLabel={t('watch.deleteConfirm')}
                variant="danger"
                closeOnConfirm={false}
                notice={deleteFailed ? t('watch.deleteFailed') : undefined}
                onConfirm={remove}
            />
        </section>
    );
}

function speakerName(speaker: string, t: (key: never, options?: never) => unknown): string {
    if (speaker === 'you') return t('callTurns.you' as never) as string;
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
