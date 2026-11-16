import { useCallback, useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { GetWatchLane, type WatchLane, type WatchLaneHour } from '@/lib/wails-shim-app';

// "Your watch today": one column for each clock hour since the watch first
// recorded today, as tall as the talk heard in it. An hour observing was
// paused in has a dashed outline; a quiet hour is an empty track; the hour
// that is not over yet is lighter. There are no numbers on it and no running
// clock (design's watch v1.1.0, rule 42); each column says its hour and its
// talk to assistive technology and on hover. It shows nothing until the watch
// has recorded something today. A plain strip on purpose; the designed lane
// replaces it.

const REFRESH_MS = 60_000;

type HourKind = 'talk' | 'quiet' | 'paused';

export function watchLaneHourKind(hour: WatchLaneHour): HourKind {
    if (hour.talkSeconds >= 60) return 'talk';
    return hour.pausedSeconds >= 60 ? 'paused' : 'quiet';
}

/** How tall an hour's column is, 0 to 100: its talk out of the whole hour. */
export function watchLaneHourHeight(hour: WatchLaneHour): number {
    return Math.max(0, Math.min(100, Math.round((hour.talkSeconds / 3600) * 100)));
}

export function ActivityWatchLane() {
    const { t, i18n } = useTranslation('activity');
    const [lane, setLane] = useState<WatchLane | null>(null);

    const load = useCallback(() => {
        // The lane is an extra over the list below: when it cannot be read it is simply not shown.
        GetWatchLane()
            .then((got) => setLane(got ?? null))
            .catch(() => setLane(null));
    }, []);

    useEffect(() => {
        load();
        const timer = window.setInterval(load, REFRESH_MS);
        window.addEventListener('focus', load);
        return () => {
            window.clearInterval(timer);
            window.removeEventListener('focus', load);
        };
    }, [load]);

    const hours = lane?.hours ?? [];
    if (hours.length === 0) return null;

    const hourLabel = (ms: number) => new Date(ms).toLocaleTimeString(i18n.language, { hour: 'numeric' });
    const describe = (hour: WatchLaneHour): string => {
        const label = hourLabel(hour.start);
        const kind = watchLaneHourKind(hour);
        if (kind === 'talk') {
            const talk = Math.round(hour.talkSeconds / 60);
            return hour.pausedSeconds >= 60
                ? t('watch.lane.hourTalkPaused', { hour: label, talk, paused: Math.round(hour.pausedSeconds / 60) })
                : t('watch.lane.hourTalk', { hour: label, count: talk });
        }
        if (kind === 'paused') {
            return t('watch.lane.hourPaused', { hour: label, count: Math.round(hour.pausedSeconds / 60) });
        }
        return t('watch.lane.hourQuiet', { hour: label });
    };

    return (
        <section className="space-y-2" data-testid="watch-lane" aria-label={t('watch.lane.title') as string}>
            <h3 className="text-[11px] font-semibold uppercase tracking-[0.08em] text-muted">{t('watch.lane.title')}</h3>
            <ol className="flex h-14 items-end gap-1">
                {hours.map((hour) => {
                    const paused = hour.pausedSeconds >= 60;
                    const text = describe(hour);
                    return (
                        <li
                            key={hour.start}
                            title={text}
                            aria-label={text}
                            data-lane-hour={watchLaneHourKind(hour)}
                            className={`relative h-full min-w-[6px] flex-1 max-w-[28px] overflow-hidden rounded-sm bg-border/50 ${
                                paused ? 'border border-dashed border-muted' : ''
                            }`}
                        >
                            <span
                                className={`absolute inset-x-0 bottom-0 rounded-sm bg-accent ${hour.current ? 'opacity-50' : ''}`}
                                style={{ height: `${watchLaneHourHeight(hour)}%` }}
                            />
                        </li>
                    );
                })}
            </ol>
            <div className="flex justify-between text-[10.5px] text-muted tabular-nums">
                <span>{hourLabel(hours[0].start)}</span>
                <span>{t('watch.lane.now')}</span>
            </div>
        </section>
    );
}
