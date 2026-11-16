import type { CSSProperties } from 'react';
import { fitChipLabel } from './day-strip-chip-label';

/** The day strip's fixed geometry, in px. Every block, label and row is placed
 * from these numbers, so a label never sits outside the block it names. */
export const DAY_STRIP = {
    trackHeight: 40,
    blockTop: 6,
    blockHeight: 28,
    insetTop: 10,
    insetHeight: 20,
    /** Room a label leaves inside its block (px-1.5 on both sides). */
    labelPadding: 12,
    /** Space kept between two hour labels (R69). */
    hourGap: 6,
    /** Space kept after a label under the strip. */
    briefGap: 8,
    /** The shortest free stretch that gets a label. */
    minFreeMs: 45 * 60_000,
} as const;

export type DayStripLabelType = 'block' | 'hour' | 'free' | 'brief';

/** One table for every label on the strip: what is drawn and what is measured
 * come from the same entry. The strip's overlap and clip fixes kept recurring
 * because labels were measured in one font (11.5px, weight 500) and drawn in
 * another (11px hour, free and brief labels). Sizes use the type tokens where
 * one exists (`--text-label`); the block label's 11.5px has no token. */
export const DAY_STRIP_LABEL: Record<DayStripLabelType, { fontSize: string; fontWeight: number }> = {
    block: { fontSize: '11.5px', fontWeight: 500 },
    hour: { fontSize: 'var(--text-label)', fontWeight: 400 },
    free: { fontSize: 'var(--text-label)', fontWeight: 400 },
    brief: { fontSize: 'var(--text-label)', fontWeight: 500 },
};

export function dayStripLabelStyle(type: DayStripLabelType): CSSProperties {
    return { fontSize: DAY_STRIP_LABEL[type].fontSize, fontWeight: DAY_STRIP_LABEL[type].fontWeight };
}

export type MeasureLabel = (type: DayStripLabelType, text: string) => number;

/** The size a label type is drawn at, with tokens resolved from the document. */
export function resolvedLabelSize(type: DayStripLabelType): string {
    const size = DAY_STRIP_LABEL[type].fontSize;
    const token = /^var\((--[\w-]+)\)$/.exec(size)?.[1];
    if (!token) return size;
    const value = typeof document === 'undefined' ? '' : window.getComputedStyle(document.documentElement).getPropertyValue(token).trim();
    return value || '11px';
}

let context: CanvasRenderingContext2D | null | undefined;
let family = '';
const fonts = new Map<DayStripLabelType, string>();

if (typeof document !== 'undefined') {
    // A font that finishes loading changes every width: measure again (the strip refits on the same event).
    document.fonts?.addEventListener?.('loadingdone', () => { fonts.clear(); family = ''; });
}

/** Text width in the font the label type is drawn in. */
export const measureDayStripLabel: MeasureLabel = (type, text) => {
    if (typeof document === 'undefined') return text.length * 6;
    if (context === undefined) context = document.createElement('canvas').getContext('2d');
    if (!context) return text.length * 6;
    if (!family) family = window.getComputedStyle(document.body).fontFamily || 'sans-serif';
    let font = fonts.get(type);
    if (!font) {
        font = `${DAY_STRIP_LABEL[type].fontWeight} ${resolvedLabelSize(type)} ${family}`;
        fonts.set(type, font);
    }
    context.font = font;
    return context.measureText(text).width;
};

/** Where a label anchored on `x` starts: the first hour sits on the left
 * edge, the last ones end on the right edge, the rest are centred. */
export function hourLabelLeft(percent: number, x: number, w: number): number {
    return percent === 0 ? x : percent > 90 ? x - w : x - w / 2;
}

/** R69: hour labels never touch. Every 2 hours when they fit, else every 4,
 * else every 6, on the hours that are multiples of the step so noon always
 * has one; each stays inside the strip. Before the strip is measured the
 * 2-hour step is used. */
export function pickHourLabels<T>(hours: { at: T; hour: number; percent: number; text: string }[], width: number, measure: MeasureLabel): T[] {
    for (const step of [2, 4, 6]) {
        const shown = hours.filter((entry) => entry.hour % step === 0);
        if (width <= 0) { if (step === 2) return shown.map((entry) => entry.at); continue; }
        let edge = -Infinity, ok = true;
        for (const entry of shown) {
            const w = measure('hour', entry.text);
            const left = hourLabelLeft(entry.percent, entry.percent / 100 * width, w);
            if (left < 0 || left + w > width || left < edge + DAY_STRIP.hourGap) { ok = false; break; }
            edge = left + w;
        }
        if (ok) return shown.map((entry) => entry.at);
    }
    return hours.slice(0, 1).map((entry) => entry.at);
}

/** A name shown inside a block: the whole name or nothing, never cut (R30).
 * A single Latin letter is never a name. */
export function fitBlockLabel(names: (string | undefined)[], spanWidth: number, measure: MeasureLabel): string {
    for (const name of names) {
        const label = fitChipLabel(name ?? '', spanWidth - DAY_STRIP.labelPadding, (text) => measure('block', text));
        if (label) return label;
    }
    return '';
}

/** "free 1h 30m" when it fits the free stretch, else nothing. */
export function fitFreeLabel(text: string, spanWidth: number, measure: MeasureLabel): string {
    return measure('free', text) <= spanWidth - 8 ? text : '';
}

/** Labels under the strip, left to right: one that would overlap the one
 * before it is on hover only. */
export function placeBriefLabels<T>(labels: { key: T; left: number; text: string }[], width: number, measure: MeasureLabel): { key: T; shown: boolean }[] {
    let edge = -Infinity;
    return labels.map(({ key, left, text }) => {
        const shown = width > 0 && left >= edge;
        if (shown) edge = left + measure('brief', text) + DAY_STRIP.briefGap;
        return { key, shown };
    });
}
