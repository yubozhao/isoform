import { afterEach, describe, expect, it } from 'vitest';
import i18n from '@/i18n';
import { formatDurationMs, formatRoundedDurationMs, formatTimeFromMs, formatTimeRangeFromMs } from './timeline/useCalendarData';
import { pointWhat, readableName, stitchedTitles, stripChipsFit } from './activity-stitched';
import type { ActivityThreadView } from '@/lib/wails-shim-app';

const at = (h: number, m: number) => new Date(2026, 10, 2, h, m).getTime();

afterEach(async () => {
    await i18n.changeLanguage('en');
});

describe('Activity path to 80 (activity review v1.0.6, v1.0.7)', () => {
    it('AR14: list durations are minutes only, rounded, and under a minute reads 1m', () => {
        expect(formatRoundedDurationMs(2 * 60_000 + 48_000)).toBe('3m');
        expect(formatRoundedDurationMs(19_000)).toBe('1m');
        expect(formatRoundedDurationMs(1_000)).toBe('1m');
        expect(formatRoundedDurationMs(70 * 60_000)).toBe('1h 10m');
    });

    it('AR42: Chinese times are 24-hour without 上午, ranges use a dash, durations are words', async () => {
        await i18n.changeLanguage('zh-CN');
        expect(formatTimeFromMs(at(9, 12))).toBe('9:12');
        expect(formatTimeFromMs(at(13, 5))).toBe('13:05');
        expect(formatTimeRangeFromMs(at(9, 12), at(10, 2))).toBe('9:12–10:02');
        expect(formatDurationMs(47 * 60_000)).toBe('47 分钟');
        expect(formatDurationMs(2 * 3600_000)).toBe('2 小时');
        expect(formatDurationMs(3 * 3600_000 + 38 * 60_000)).toBe('3 小时 38 分钟');
        expect(formatRoundedDurationMs(2 * 60_000 + 48_000)).toBe('3 分钟');
    });

    it('AR48: English ranges say the shared period once, and across midnight both', () => {
        expect(formatTimeRangeFromMs(at(12, 0), at(12, 55), 'en')).toBe('12:00–12:55 PM');
        expect(formatTimeRangeFromMs(at(11, 40), at(12, 28), 'en')).toBe('11:40 AM–12:28 PM');
        expect(formatTimeRangeFromMs(at(23, 0), at(24, 0), 'en')).toBe('11:00 PM–12:00 AM');
    });

    it('AR50: a name read off the screen that no one can read is not a place', () => {
        expect(readableName('wnocishiscaeronaarivaNzrvactvcrasEuncneelvsc.niod')).toBe(false);
        expect(readableName('apostla-ple-1-201g.par')).toBe(false);
        expect(readableName('invoice_mapping.go')).toBe(true);
        expect(readableName('Halcyon launch memo, section 3')).toBe(true);
        expect(readableName('ERP connector v0.3')).toBe(true);
        expect(pointWhat('Downloads/wnocishiscaeronaarivaNzrvactvcrasEuncneelvsc.niod', 'path')).toBe('');
        expect(pointWhat('~/Documents/apostla-ple-1-201g.par', 'path')).toBe('');
        expect(pointWhat('~/Documents/Halcyon/launch-memo.pdf', 'path')).toBe('launch-memo.pdf');
    });

    it('AR51: chips only when every stretch shows its full time', () => {
        const label = '1:00 PM · 12m';
        const seven = Array.from({ length: 7 }, (_, i) => [
            ...(i > 0 ? [{ kind: 'gap' as const, grow: 12, label: 'Away 12m' }] : []),
            { kind: 'stretch' as const, grow: 12, label },
        ]).flat();
        expect(stripChipsFit(seven, 760)).toBe(false);
        expect(stripChipsFit([{ kind: 'stretch', grow: 50, label: '9:12 AM · 50m' }, { kind: 'gap', grow: 28, label: 'Away 28m · Northwind call' }, { kind: 'stretch', grow: 70, label: '10:30 AM · 1h 10m' }], 760)).toBe(true);
        expect(stripChipsFit(seven, 0)).toBe(true);
    });

    it('AR55: a title of 60 characters or fewer is never cut', () => {
        const thread = (id: string, title: string) => ({ id, title, startMs: 0 } as unknown as ActivityThreadView);
        const t = i18n.getFixedT('en', 'activity');
        const long = 'Reviewing the Northwind invoice sync connector with Priya and Ana today';
        const titles = stitchedTitles([thread('a', 'Answering Northwind support tickets'), thread('b', long)], t);
        expect(titles.get('a')).toBe('Answering Northwind support tickets');
        expect([...titles.get('b')!].length).toBe(60);
        expect(titles.get('b')!.endsWith('…')).toBe(true);
    });
});
