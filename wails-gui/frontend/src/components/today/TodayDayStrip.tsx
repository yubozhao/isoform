import { measureToday } from "@/lib/today-telemetry";
import { memo, useEffect, useLayoutEffect, useRef, useState, type CSSProperties } from 'react';
import { useTranslation } from 'react-i18next';
import type { TodayDayShape } from '@/lib/wails-shim-app';
import { DAY_STRIP, dayStripLabelStyle, fitBlockLabel, fitFreeLabel, hourLabelLeft, measureDayStripLabel, pickHourLabels, placeBriefLabels } from '@/lib/day-strip-layout';
import { briefClockTime } from '@/lib/brief-time';
import { cn } from '@/lib/utils';
import { activeInterval } from '@/lib/app-activity';

export type HoldPreview = 'proposed' | 'held' | 'dismissed';

// R70: a held hold is a solid violet outline with a violet tint, never a solid fill.
// A proposed hold stays dashed with a lighter tint. Isoform's colour, the meetings' outline.
const HELD_HOLD = 'text-[10px] border-[1.5px] border-accent bg-accent-light text-accent';
const PROPOSED_HOLD = 'border-[1.5px] border-dashed border-accent bg-accent-light/50 text-accent';
interface Span { start: number; end: number }

/** The first whole word of a name: the most a narrow block shows, never cut mid-word (R30). */
function firstWholeWord(text: string): string {
    return text.trim().split(/\s+/).find((word) => /[\p{L}\p{N}]/u.test(word)) ?? '';
}

/** Older day shapes can carry the recurrence word as their short name. */
function meetingLabel(shortName: string, title: string): string {
    const source = /^weekly$/i.test(shortName.trim()) ? title : shortName;
    return source.replace(/\bweekly\b[\s,:-]*/gi, '').trim();
}

/** Union calendar intervals before finding free time; overlapping meetings
 * never create a false free stretch. A preview hold occupies drawing space only. */
export function freeDaySpans(start: number, end: number, occupied: Span[]): Span[] {
    let cursor = start;
    const free: Span[] = [];
    for (const span of [...occupied].sort((a, b) => a.start - b.start)) {
        if (span.end <= cursor || span.start >= end) continue;
        if (span.start > cursor) free.push({ start: cursor, end: Math.min(span.start, end) });
        cursor = Math.max(cursor, Math.min(span.end, end));
    }
    if (cursor < end) free.push({ start: cursor, end });
    return free;
}

interface DayBlock extends Span {
    event: Omit<TodayDayShape['events'][number], 'start' | 'end'> | null;
    originalStart: number;
    originalEnd: number;
}
interface DrawnBlock extends DayBlock {
    layer: number;
    inset: boolean;
    hidden: DayBlock[];
}

/** Sweep simultaneous intervals, retaining the two shortest at every instant.
 * Merge contiguous pieces with the same geometry. Labels are drawn once per
 * event, independently of inset changes. Hidden events retain tooltip evidence. */
function overlapBlocks(blocks: DayBlock[]): DrawnBlock[] {
    const ordered = [...blocks].sort((a, b) =>
        (b.originalEnd - b.originalStart) - (a.originalEnd - a.originalStart));
    const boundaries = [...new Set(blocks.flatMap((block) => [block.start, block.end]))].sort((a, b) => a - b);
    const pieces = new Map<DayBlock, DrawnBlock[]>();
    for (let i = 0; i < boundaries.length - 1; i++) {
        const start = boundaries[i], end = boundaries[i + 1];
        const active = ordered.filter((block) => block.start < end && block.end > start);
        const visible = active.slice(-2), hidden = active.slice(0, -2);
        visible.forEach((block, index) => {
            const spans = pieces.get(block) ?? [];
            const prior = spans.at(-1);
            const folded = index === 0 ? hidden : [];
            const inset = index === 1;
            if (prior?.end === start && prior.inset === inset) {
                prior.end = end;
                prior.hidden = [...new Set([...prior.hidden, ...folded])];
            } else spans.push({ ...block, start, end, layer: ordered.indexOf(block), inset, hidden: folded });
            pieces.set(block, spans);
        });
    }
    return ordered.flatMap((block) => pieces.get(block) ?? []).sort((a, b) => a.start - b.start || a.layer - b.layer);
}

function BlockLabel({ label, style, className, eventId }: {
    label: string; style: CSSProperties; className: string; eventId?: string;
}) {
    return <span data-testid={eventId !== undefined ? 'today-day-event-label' : 'today-day-hold-label'} data-event-id={eventId}
        className={cn('today-day-block-label pointer-events-none absolute flex items-center justify-start overflow-hidden whitespace-nowrap px-1.5 text-left', className)}
        style={{ ...dayStripLabelStyle('block'), ...style }}>{label}</span>;
}

function NowLine({ initialNow, start, end, layer, clock, label }: {
    initialNow: number; start: number; end: number; layer: number;
    clock: (at: number) => string; label: (time: string) => string;
}) {
    const [now, setNow] = useState(initialNow);
    useEffect(() => {
        // The local one-minute clock: no data read, stopped while hidden.
        setNow(initialNow);
        return activeInterval(() => setNow(Date.now()), 60_000);
    }, [initialNow]);
    // Outside the strip's hours there is no now line (today-review v1.1.23 R53).
    if (now < start || now >= end) return null;
    const percent = (now - start) / (end - start) * 100;
    return <span data-testid="today-day-now" className="absolute -bottom-1 -top-1 z-10 w-0.5 rounded-sm bg-accent" style={{ zIndex: layer, left: now <= start ? '0px' : now >= end ? 'calc(100% - 2px)' : `clamp(0px, ${percent}%, calc(100% - 2px))` }} title={label(clock(now))}><span aria-hidden className="absolute -left-[3px] -top-[3px] size-2 rounded-full bg-accent" /></span>;
}

function FreeLabels({ spans, start, end, initialNow, isToday, width, tr }: {
    spans: Span[]; start: number; end: number; initialNow: number; isToday: boolean; width: number; language: string;
    tr: (key: string, options?: Record<string, unknown>) => string;
}) {
    const [now, setNow] = useState(initialNow);
    useEffect(() => {
        const received = Date.now();
        setNow(initialNow);
        if (!isToday || !Number.isFinite(initialNow)) return;
        return activeInterval(() => setNow(initialNow + Math.max(0, Date.now() - received)), 60_000);
    }, [initialNow, isToday]);
    return <>{spans.map((span) => {
        // Free time already past is plain, with no label (today-review v1.1.22 R21).
        const ahead = { start: Number.isFinite(now) ? Math.max(span.start, now) : span.start, end: span.end };
        if (ahead.end - ahead.start < DAY_STRIP.minFreeMs) return null;
        const minutes = Math.round((ahead.end - ahead.start) / 60_000);
        const hours = Math.floor(minutes / 60), remainder = minutes % 60;
        const duration = tr(hours && remainder ? 'dayStrip.hoursMinutes' : hours ? 'dayStrip.hours' : 'dayStrip.minutes', { hours, minutes: remainder });
        const full = tr('dayStrip.freeDuration', { duration });
        // Every free label reads "free {duration}" (today-review v1.1.13 D7).
        const label = fitFreeLabel(full, width * (ahead.end - ahead.start) / (end - start), measureDayStripLabel);
        return <span key={span.start} data-testid="today-day-free" className="pointer-events-none absolute z-[2] flex items-center justify-center whitespace-nowrap text-muted" style={{ ...dayStripLabelStyle('free'), top: DAY_STRIP.blockTop, height: DAY_STRIP.blockHeight, left: `${(ahead.start - start) / (end - start) * 100}%`, width: `calc(${(ahead.end - ahead.start) / (end - start) * 100}% - 2px)` }}>{label && <span className="bg-background">{label}</span>}</span>;
    })}</>;
}

interface TodayDayStripProps {
    shape: TodayDayShape | null;
    holdPreview?: HoldPreview;
    timeZone?: string;
    requestedDate: string;
    /** The first days: "Your afternoon" with "From your calendar" above the strip. */
    heading?: { title: string; source: string };
    onOpenMeeting?: (id: string) => void;
    holdTitle?: string;
    onRemoveHold?: (id?: string) => void;
    onOpenHold?: () => void;
    /** The brief's first sentence: an event it names is named on the strip too (R30). */
    briefText?: string;
}

export const TodayDayStrip = memo(function TodayDayStrip({ shape, holdPreview = 'proposed', timeZone, requestedDate, heading, onOpenMeeting, holdTitle, onRemoveHold, onOpenHold, briefText }: TodayDayStripProps) {
    const { t, i18n } = useTranslation('today');
    const tr = (key: string, options?: Record<string, unknown>) => String(t(key as never, options as never));
    const track = useRef<HTMLDivElement>(null);
    const [width, setWidth] = useState(0);
    const [holdMenu, setHoldMenu] = useState<{ id?: string; title: string } | null>(null);
    const [holdNow, setHoldNow] = useState(() => Date.parse(shape?.now ?? ''));
    const shapeClock = useRef({ value: shape?.now, received: Date.now() });
    if (shapeClock.current.value !== shape?.now) shapeClock.current = { value: shape?.now, received: Date.now() };
    // What is past moves on with the clock, not with the time the strip was
    // read (today-review v1.1.22 R21). It redraws when the next block ends,
    // so the now line's minute ticks never redraw the events.
    const [, setEnded] = useState(0);
    useEffect(() => {
        const now = Date.parse(shape?.now ?? '') + Math.max(0, Date.now() - shapeClock.current.received);
        if (!shape || !Number.isFinite(now)) return;
        const ends = [...shape.events.map((event) => Date.parse(event.end)), Date.parse(shape.hold?.end ?? '')].filter((at) => Number.isFinite(at) && at > now);
        if (!ends.length) return;
        const timer = window.setTimeout(() => setEnded((count) => count + 1), Math.min(...ends) - now + 1);
        return () => window.clearTimeout(timer);
    });
    useEffect(() => {
        const now = Date.parse(shape?.now ?? '') + Math.max(0, Date.now() - shapeClock.current.received);
        setHoldNow(now);
        if (!shape || !Number.isFinite(now)) return;
        const timers = shape.events.filter((event) => event.holdId).map((event) => Date.parse(event.start))
            .filter((start) => Number.isFinite(start) && start > now)
            .map((start) => window.setTimeout(() => setHoldNow((current) => Math.max(current, start)), start - now));
        return () => timers.forEach((timer) => window.clearTimeout(timer));
    }, [shape?.now, shape?.events]);
    // A font that finishes loading changes every label's width: fit them again.
    const [, setFontsLoaded] = useState(0);
    useEffect(() => {
        const fonts = typeof document === 'undefined' ? undefined : document.fonts;
        if (!fonts?.addEventListener) return;
        const refit = () => setFontsLoaded((count) => count + 1);
        fonts.addEventListener('loadingdone', refit);
        return () => fonts.removeEventListener('loadingdone', refit);
    }, []);
    useLayoutEffect(() => {
        const element = track.current;
        if (!element) return;
        const measure = () => setWidth(element.getBoundingClientRect().width);
        measure();
        if (typeof ResizeObserver === 'undefined') return;
        const observer = new ResizeObserver(measure);
        observer.observe(element);
        return () => observer.disconnect();
    }, [shape?.calendarConnected, shape?.rangeStart, shape?.rangeEnd, shape?.events, shape?.hold, holdPreview]);
    if (!shape) return null;
    if (!shape.calendarConnected) return null;
    const start = Date.parse(shape.rangeStart), end = Date.parse(shape.rangeEnd);
    if (!Number.isFinite(start) || !Number.isFinite(end) || end <= start) return null;
    const percent = (at: number) => (at - start) / (end - start) * 100;
    const placement = (span: Span, gap = 2) => ({ left: `${percent(span.start)}%`, width: `calc(${percent(span.end) - percent(span.start)}% - ${gap}px)` });
    const clock = (at: number) => briefClockTime(new Date(at).toISOString(), i18n.language, tr, timeZone) ?? '';
    const events = (shape.events ?? []).map((event) => ({ ...event, holdId: event.holdId && Date.parse(event.start) > holdNow ? event.holdId : undefined, start: Math.max(start, Date.parse(event.start)), end: Math.min(end, Date.parse(event.end)), originalStart: Date.parse(event.start), originalEnd: Date.parse(event.end) }))
        .filter((event) => Number.isFinite(event.start) && Number.isFinite(event.end) && event.end > event.start)
        .sort((a, b) => a.start - b.start);
    const holdStart = Date.parse(shape.hold?.start ?? ''), holdEnd = Date.parse(shape.hold?.end ?? '');
    const hold = shape.hold && holdPreview !== 'dismissed' && holdEnd > start && holdStart < end && holdEnd > holdStart
        ? { start: Math.max(start, holdStart), end: Math.min(end, holdEnd) } : null;
    if (events.length === 0 && !hold) return null;
    const currentMenu = holdMenu && (holdMenu.id ? events.some((event) => event.holdId === holdMenu.id && event.title === holdMenu.title) : hold && holdPreview === 'held' && holdMenu.title === (holdTitle ?? shape.hold?.label ?? '')) ? holdMenu : null;
    const entries: DayBlock[] = [
        ...events.map((event) => ({ ...event, event })),
        ...(hold ? [{ ...hold, event: null, originalStart: holdStart, originalEnd: holdEnd }] : []),
    ];
    const blocks = overlapBlocks(entries);
    const markerLayer = 0;
    const free = freeDaySpans(start, end, [...events, ...(hold ? [hold] : [])]).filter((span) => span.end - span.start >= DAY_STRIP.minFreeMs);
    const hours: number[] = [];
    const firstHour = new Date(start);
    firstHour.setMinutes(0, 0, 0);
    for (let at = firstHour.getTime(); at <= end; at += 3_600_000) if (at >= start) hours.push(at);
    // R69: hour labels never touch. Every 2 hours when they fit, else every 4, else every 6; each stays inside the strip.
    // R69: labels sit on the hours that are multiples of the step, so noon always has one (design, 2026-11-06).
    const hourOfDay = (at: number) => {
        try {
            return Number(new Intl.DateTimeFormat('en-US', { hour: 'numeric', hour12: false, timeZone }).formatToParts(at).find((part) => part.type === 'hour')?.value) % 24;
        } catch {
            return new Date(at).getHours();
        }
    };
    const hourLabels = pickHourLabels(hours.map((at) => ({ at, hour: hourOfDay(at), percent: percent(at), text: clock(at) })), width, measureDayStripLabel);
    const now = Date.parse(shape.now) + Math.max(0, Date.now() - shapeClock.current.received);
    const civilDay = (at: number) => {
        let parts: Intl.DateTimeFormatPart[];
        try { parts = new Intl.DateTimeFormat('en-CA', { year: 'numeric', month: '2-digit', day: '2-digit', timeZone }).formatToParts(at); }
        catch { parts = new Intl.DateTimeFormat('en-CA', { year: 'numeric', month: '2-digit', day: '2-digit' }).formatToParts(at); }
        const value = (type: string) => Number(parts.find((part) => part.type === type)?.value);
        return Date.UTC(value('year'), value('month') - 1, value('day'));
    };
    const day = /^\d{4}-\d{2}-\d{2}$/.test(requestedDate) ? Date.parse(`${requestedDate}T00:00:00Z`) : NaN;
    const dayDifference = Number.isFinite(now) && Number.isFinite(day) ? Math.round((day - civilDay(now)) / 86_400_000) : null;
    const dayLabel = dayDifference === null || dayDifference === 0 ? '' : dayDifference === 1 ? t('dayStrip.tomorrow')
        : new Date(day).toLocaleDateString(i18n.language, { weekday: 'long', timeZone: 'UTC' });
    // Hover and the accessible name: the time, then the full title (R30). Busy and Private read as before.
    const eventTitle = (item: DayBlock) => item.event?.private ? t('dayStrip.private') : item.event && !item.event.holdId && !item.event.title?.trim() ? t('dayStrip.busy') : t('dayStrip.eventTimeTitle', {
        title: item.event?.title ?? shape.hold!.label, start: clock(item.originalStart), end: clock(item.originalEnd),
    });
    // A block the brief names gets its short name under the strip: the person for a one-on-one, else the title's first whole word.
    const briefShortName = (event: NonNullable<DayBlock['event']>): string | null => {
        if (!briefText?.trim() || event.holdId || event.private || !event.title?.trim()) return null;
        const brief = briefText.toLowerCase();
        const found = [event.shortName, event.shortestName, firstWholeWord(event.title)]
            .find((name) => { const text = name?.trim().toLowerCase() ?? ''; return text.length >= 2 && brief.includes(text); });
        return found?.trim() || null;
    };
    // One block's name inside it: the short name, then its first word, then nothing (R30).
    const layoutLabel = (entry: DayBlock) => {
        const pieces = blocks.filter((block) => block.event === entry.event);
        const covers = pieces.length ? blocks.filter((other) => other.layer > pieces[0].layer && other.start < entry.end && other.end > entry.start) : [];
        const labelSpan = freeDaySpans(entry.start, entry.end, covers).sort((a, b) => (b.end - b.start) - (a.end - a.start))[0];
        const { event } = entry;
        const labelWidth = labelSpan ? width * (labelSpan.end - labelSpan.start) / (end - start) - 2 : 0;
        // A private event reads Private; an event with no title reads Busy (R30 v3).
        const untitled = !!event && !event.holdId && !event.private && !event.title?.trim();
        const rawLabel = event?.private ? t('dayStrip.private') : untitled ? t('dayStrip.busy') : event ? event.holdId ? t('dayStrip.held') : event.kind === 'meeting' ? meetingLabel(event.shortName, event.title) : event.shortName || event.title
            : holdPreview === 'held' ? t('dayStrip.held') : t('dayStrip.proposed');
        // The short name, then the first word of it (shortestName), never a cut name.
        const names = event && !event.holdId && !event.private && !untitled ? [rawLabel, event.shortestName] : [rawLabel];
        const label = fitBlockLabel(names, labelWidth, measureDayStripLabel);
        return { pieces, labelSpan, label, untitled, rawLabel };
    };
    const named: { key: DayBlock; left: number; text: string }[] = [];
    for (const entry of entries) {
        if (!entry.event) continue;
        // R30 handoff: the label under the strip is only for a block the brief names that shows no name inside.
        if (layoutLabel(entry).label) continue;
        const text = briefShortName(entry.event);
        // Two labels that would overlap: the earlier shows, the later is on hover only.
        if (text) named.push({ key: entry, left: percent(entry.start) / 100 * width, text });
    }
    const briefLabels = placeBriefLabels(named, width, measureDayStripLabel).map(({ key, shown }, index) => ({ entry: key, text: named[index].text, shown }));
    return (
        <div className="today-day-strip flex flex-col gap-1.5" data-testid="today-day-strip" role="group" aria-label={heading?.title ?? t('dayStrip.label')}>
            {heading && (
                <div className="flex items-baseline justify-between gap-3" data-testid="today-day-strip-heading">
                    <span className="text-[13px] font-semibold text-ink">{heading.title}</span>
                    <span className="text-[12px] text-muted">{heading.source}</span>
                </div>
            )}
            {/* A strip for another day says which day, so its free time is
                never read as today's (today-review v1.1.17 R21). */}
            {dayLabel && <span className="text-[12px] font-medium text-muted" data-testid="today-day-strip-day">{dayLabel}</span>}
            <div className="relative h-3.5 leading-[14px] text-muted" style={dayStripLabelStyle('hour')} data-testid="today-day-hours">
                {hourLabels.map((at) => <span key={at} className="today-day-hour absolute whitespace-nowrap" style={{ left: `${percent(at)}%`, transform: hourLabelLeft(percent(at), 0, 100) === 0 ? undefined : `translateX(${hourLabelLeft(percent(at), 0, 100)}%)` }}>{clock(at)}</span>)}
            </div>
            <div ref={track} data-testid="today-day-track" data-rows="1" className="patina-ground relative isolate rounded-lg border border-border bg-background" style={{ height: DAY_STRIP.trackHeight }}>
                {hours.map((at) => <span key={at} aria-hidden className="absolute inset-y-0 w-px bg-border" style={{ left: `${percent(at)}%` }} />)}
                {events.length === 0 && !hold ? <span className="absolute inset-0 flex items-center justify-center text-[12px] text-muted">{dayLabel ? t('dayStrip.emptyOnDay', { day: dayLabel }) : t('dayStrip.empty')}</span> : <FreeLabels spans={dayDifference !== null && dayDifference < 0 ? [] : free} start={start} end={end} initialNow={now} isToday={dayDifference === 0} width={width} language={i18n.language} tr={tr} />}
                {entries.map((entry) => {
                    const { pieces, labelSpan, label } = layoutLabel(entry);
                    if (!pieces.length) return null;
                    const { event } = entry;
                    return <span key={event?.id ?? 'hold'} data-testid={event ? 'today-day-event-group' : 'today-day-hold-group'}
                        className="pointer-events-none absolute inset-0" style={{ zIndex: pieces[0].layer + 1, opacity: !event && entry.originalEnd <= now ? 0.45 : 1 }}>
                        {pieces.map((block, pieceIndex) => {
                            // Hover and the accessible name carry the word or the full name, even where the block is too narrow to show it.
                            const title = [eventTitle(block), ...(block.hidden.length ? [t('dayStrip.more', { count: block.hidden.length }), ...block.hidden.map(eventTitle)] : [])].join('\n');
                            // Every calendar block is an ink outline with a light tint, never a solid fill; an ended one dims its tint (R30).
                            const className = cn('pointer-events-auto absolute flex items-center justify-center overflow-hidden whitespace-nowrap rounded-md', event ? event.holdId ? HELD_HOLD : cn('border-[1.5px] border-ink', entry.originalEnd <= now ? 'bg-panel-warm/50' : 'bg-panel-warm') : holdPreview === 'held' ? HELD_HOLD : PROPOSED_HOLD);
                            const style = { ...placement(block, block.end === Math.min(end, block.originalEnd) ? 2 : 0), zIndex: 1, top: block.inset ? DAY_STRIP.insetTop : DAY_STRIP.blockTop, height: block.inset ? DAY_STRIP.insetHeight : DAY_STRIP.blockHeight };
                            return event?.kind === 'meeting' && !event.holdId && !event.private && onOpenMeeting
                                ? <button key={block.start} type="button" data-testid="today-day-event" data-inset={block.inset} data-kind={event.kind} title={title} aria-label={t('dayStrip.openPrep', { event: eventTitle(block) })} tabIndex={pieceIndex === 0 ? 0 : -1} className={cn(className, 'cursor-pointer focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent')} style={style} onClick={() => onOpenMeeting(event.id)} />
                                : <span key={block.start} data-testid={event ? 'today-day-event' : 'today-day-hold'} data-inset={block.inset} data-kind={event?.kind} data-state={event ? undefined : holdPreview} title={title} aria-label={title} className={className} style={style} />;
                        })}
                        {labelSpan && <BlockLabel eventId={event?.id} label={label}
                            className={event ? event.holdId ? 'text-accent' : entry.originalEnd <= now ? 'text-muted' : 'text-ink' : 'text-accent'}
                            style={{ ...placement(labelSpan), top: DAY_STRIP.blockTop, height: DAY_STRIP.blockHeight, zIndex: 2 }} />}
                        {!event && labelSpan && holdPreview === 'held' && onRemoveHold && <button type="button"
                            aria-label={t('holds.heldFor', { title: holdTitle ?? shape.hold?.label })}
                            aria-expanded={!!holdMenu && !holdMenu.id} onClick={() => measureToday("T31", "open", "opened", () => setHoldMenu((open) => open && !open.id ? null : { title: holdTitle ?? shape.hold?.label ?? '' }))}
                            className="pointer-events-auto absolute rounded-md focus-visible:outline focus-visible:outline-2 focus-visible:outline-accent"
                            style={{ ...placement(labelSpan), top: DAY_STRIP.blockTop, height: DAY_STRIP.blockHeight, zIndex: 3 }} />}
                        {event?.holdId && labelSpan && onRemoveHold && <button type="button"
                            aria-label={t('holds.heldFor', { title: event.title })} aria-expanded={holdMenu?.id === event.holdId}
                            onClick={() => measureToday("T31", "open", "opened", () => setHoldMenu((open) => open?.id === event.holdId ? null : { id: event.holdId, title: event.title }))}
                            className="pointer-events-auto absolute rounded-md focus-visible:outline focus-visible:outline-2 focus-visible:outline-accent"
                            style={{ ...placement(labelSpan), top: DAY_STRIP.blockTop, height: DAY_STRIP.blockHeight, zIndex: 3 }} />}
                        {!event && labelSpan && holdPreview === 'proposed' && onOpenHold && <button type="button"
                            aria-label={t('holds.openOffer', { title: holdTitle ?? shape.hold?.label })} onClick={onOpenHold}
                            className="pointer-events-auto absolute rounded-md focus-visible:outline focus-visible:outline-2 focus-visible:outline-accent"
                            style={{ ...placement(labelSpan), top: DAY_STRIP.blockTop, height: DAY_STRIP.blockHeight, zIndex: 3 }} />}
                    </span>;
                })}
                {Number.isFinite(now) && dayDifference === 0 && <NowLine initialNow={now} start={start} end={end} layer={markerLayer} clock={clock} label={(time) => String(t('dayStrip.now', { time }))} />}
            </div>
            {briefLabels.length > 0 && <div className="relative h-4" data-testid="today-day-brief-labels">
                {briefLabels.map(({ entry, text, shown }) => shown
                    ? <span key={entry.event?.id} data-testid="today-day-brief-label" className="absolute top-0 whitespace-nowrap text-muted" style={{ ...dayStripLabelStyle('brief'), left: `${percent(entry.start)}%` }}>{text}</span>
                    : <span key={entry.event?.id} data-testid="today-day-brief-label-hover" aria-label={eventTitle(entry)} title={eventTitle(entry)} className="absolute top-0 h-4" style={{ left: `${percent(entry.start)}%`, width: `${percent(entry.end) - percent(entry.start)}%` }} />)}
            </div>}
            {currentMenu && onRemoveHold && <div className="self-start rounded-lg border border-border bg-background p-2 text-[13px] shadow-sm">
                <p className="text-muted">{t('holds.heldFor', { title: currentMenu.title })}</p>
                <button type="button" className="mt-1 text-accent" onClick={() => { setHoldMenu(null); onRemoveHold(currentMenu.id); }}>{t('holds.remove')}</button>
            </div>}

        </div>
    );
}, (prior, next) => prior.holdPreview === next.holdPreview && prior.timeZone === next.timeZone
    && prior.requestedDate === next.requestedDate && prior.onOpenMeeting === next.onOpenMeeting && prior.holdTitle === next.holdTitle && prior.onRemoveHold === next.onRemoveHold && prior.onOpenHold === next.onOpenHold && JSON.stringify(prior.shape) === JSON.stringify(next.shape));
