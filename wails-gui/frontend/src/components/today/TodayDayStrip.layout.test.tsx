import { cleanup, render, screen } from '@testing-library/react';
import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from 'vitest';
import i18n from '@/i18n';
import { TodayDayStrip } from './TodayDayStrip';
import { dayShapeFixture } from './today-next.fixture';
import { DAY_STRIP, DAY_STRIP_LABEL, fitBlockLabel, hourLabelLeft, pickHourLabels, placeBriefLabels, type DayStripLabelType, type MeasureLabel } from '@/lib/day-strip-layout';

// Group H of docs/today-failure-history.md: the strip's overlap and clip fixes
// recurred because labels were measured in one font and drawn in another.
// These guards check the computed layout, not class names.

// A canvas that records the font each text was measured in.
const measuredFont = new Map<string, string>();
const pxOf = (font: string) => Number(/(\d+(?:\.\d+)?)px/.exec(font)?.[1] ?? 0);
beforeAll(() => {
    document.documentElement.style.setProperty('--text-label', '11px');
    const context = {
        font: '',
        measureText(text: string) {
            measuredFont.set(text, this.font);
            return { width: text.length * pxOf(this.font) * 0.55 };
        },
    };
    vi.spyOn(HTMLCanvasElement.prototype, 'getContext').mockReturnValue(context as unknown as CanvasRenderingContext2D);
});
afterAll(() => vi.restoreAllMocks());
afterEach(cleanup);

const width: MeasureLabel = (type, text) => text.length * (type === 'block' ? 11.5 : 11) * 0.55;
const fontOf = (type: DayStripLabelType) => `${DAY_STRIP_LABEL[type].fontWeight} ${DAY_STRIP_LABEL[type].fontSize === 'var(--text-label)' ? '11px' : DAY_STRIP_LABEL[type].fontSize}`;

describe('day strip labels are measured in the font they are drawn in', () => {
    it.each([820, 1100])('at a %i px track', async (trackWidth) => {
        await i18n.changeLanguage('en');
        const rect = vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockReturnValue({ width: trackWidth } as DOMRect);
        try {
            measuredFont.clear();
            render(<TodayDayStrip requestedDate="2026-10-23" shape={dayShapeFixture} timeZone="America/Los_Angeles" />);
            const drawn = (element: HTMLElement) => `${element.style.fontWeight || getComputedStyle(element).fontWeight} ${element.style.fontSize || getComputedStyle(element).fontSize}`;
            const hours = screen.getByTestId('today-day-hours');
            expect(hours.children.length).toBeGreaterThan(0);
            for (const label of Array.from(hours.children) as HTMLElement[]) {
                expect(measuredFont.get(label.textContent!)?.startsWith(fontOf('hour'))).toBe(true);
                expect(`${hours.style.fontWeight} ${hours.style.fontSize}`).toBe(`${DAY_STRIP_LABEL.hour.fontWeight} ${DAY_STRIP_LABEL.hour.fontSize}`);
            }
            const names = screen.getAllByTestId('today-day-event-label').filter((label) => label.textContent);
            expect(names.length).toBeGreaterThan(0);
            for (const label of names) {
                expect(measuredFont.get(label.textContent!)?.startsWith(fontOf('block'))).toBe(true);
                expect(drawn(label)).toBe(`${DAY_STRIP_LABEL.block.fontWeight} ${DAY_STRIP_LABEL.block.fontSize}`);
            }
            for (const free of screen.queryAllByTestId('today-day-free')) {
                const text = free.textContent!;
                if (!text) continue;
                expect(measuredFont.get(text)?.startsWith(fontOf('free'))).toBe(true);
                expect(drawn(free)).toBe(`${DAY_STRIP_LABEL.free.fontWeight} ${DAY_STRIP_LABEL.free.fontSize}`);
                expect(free).toHaveStyle({ top: `${DAY_STRIP.blockTop}px`, height: `${DAY_STRIP.blockHeight}px` });
            }
            expect(screen.getByTestId('today-day-track')).toHaveStyle({ height: `${DAY_STRIP.trackHeight}px` });
        } finally { rect.mockRestore(); }
    });
});

describe('the computed strip layout', () => {
    const hourEntries = (from: number, to: number) => Array.from({ length: to - from + 1 }, (_, i) => {
        const hour = from + i;
        return { at: hour, hour, percent: (hour - from) / (to - from) * 100, text: `${hour % 12 || 12} ${hour < 12 ? 'AM' : 'PM'}` };
    });

    it.each([[320, 8, 19], [560, 8, 19], [820, 8, 19], [560, 0, 23], [820, 6, 22]])(
        'hour labels at %i px never touch and stay inside (%i:00 to %i:00)', (trackWidth, from, to) => {
            const entries = hourEntries(from, to);
            const shown = pickHourLabels(entries, trackWidth, width).map((at) => entries.find((entry) => entry.at === at)!);
            expect(shown.length).toBeGreaterThan(0);
            let edge = -Infinity;
            for (const entry of shown) {
                const w = width('hour', entry.text);
                const left = hourLabelLeft(entry.percent, entry.percent / 100 * trackWidth, w);
                if (shown.length > 1) {
                    expect(left).toBeGreaterThanOrEqual(0);
                    expect(left + w).toBeLessThanOrEqual(trackWidth);
                    expect(left).toBeGreaterThanOrEqual(edge + DAY_STRIP.hourGap);
                }
                edge = left + w;
            }
        });

    it('a block shows a whole name that fits inside it, or nothing, never a cut name', () => {
        for (const span of [20, 40, 60, 90, 140, 300]) {
            const label = fitBlockLabel(['1:1 with Priya on Halcyon Meet', 'Priya'], span, width);
            expect(['', '1:1 with Priya on Halcyon Meet', 'Priya']).toContain(label);
            if (label) expect(width('block', label)).toBeLessThanOrEqual(span - DAY_STRIP.labelPadding);
        }
        expect(fitBlockLabel(['P'], 300, width)).toBe('');
    });

    it('labels under the strip never overlap; a later one that would is on hover only', () => {
        const labels = [{ key: 'a', left: 0, text: 'Priya' }, { key: 'b', left: 20, text: 'Maya' }, { key: 'c', left: 120, text: 'Article' }];
        const placed = placeBriefLabels(labels, 560, width);
        expect(placed.map((entry) => entry.shown)).toEqual([true, false, true]);
        let edge = -Infinity;
        placed.forEach(({ shown }, index) => {
            if (!shown) return;
            expect(labels[index].left).toBeGreaterThanOrEqual(edge);
            edge = labels[index].left + width('brief', labels[index].text) + DAY_STRIP.briefGap;
        });
        expect(placeBriefLabels(labels, 0, width).every((entry) => !entry.shown)).toBe(true);
    });
});
