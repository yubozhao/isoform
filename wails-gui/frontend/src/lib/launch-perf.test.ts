import { beforeEach, describe, expect, it, vi } from 'vitest';

const report = vi.hoisted(() => vi.fn(async (_stage: string) => {}));
vi.mock('./wails-shim-app', () => ({ ReportLaunchStage: report }));

beforeEach(() => { vi.resetModules(); report.mockClear(); });

describe('launch paint milestones', () => {
    it('releases optional startup only after all first-screen reads painted', async () => {
        const frames: FrameRequestCallback[] = [];
        vi.stubGlobal('requestAnimationFrame', (callback: FrameRequestCallback) => frames.push(callback));
        const { markLaunchStage, markLaunchPaint } = await import('./launch-perf');
        markLaunchStage('first_data');
        markLaunchPaint('chat_rendered');
        markLaunchPaint('counts_rendered');
        markLaunchPaint('overview_rendered');
        expect(report.mock.calls.flat()).not.toContain('first_screen_rendered');
        while (frames.length) frames.shift()!(0);
        expect(report.mock.calls.filter(([name]) => name === 'first_screen_rendered')).toHaveLength(1);
        markLaunchStage('first_data');
        expect(report.mock.calls.filter(([name]) => name === 'first_data')).toHaveLength(1);
        vi.unstubAllGlobals();
    });

    it('reports a paint milestone without animation frames, as in a hidden window', async () => {
        vi.useFakeTimers();
        vi.stubGlobal('requestAnimationFrame', () => 0);
        vi.stubGlobal('cancelAnimationFrame', () => {});
        const { markLaunchPaint, PAINT_FALLBACK_MS } = await import('./launch-perf');
        markLaunchPaint('first_paint');
        expect(report.mock.calls.flat()).not.toContain('first_paint');
        vi.advanceTimersByTime(PAINT_FALLBACK_MS);
        expect(report.mock.calls.filter(([name]) => name === 'first_paint')).toHaveLength(1);
        vi.unstubAllGlobals();
        vi.useRealTimers();
    });

    it('reports a painted milestone once, not again when the fallback fires', async () => {
        vi.useFakeTimers();
        const frames: FrameRequestCallback[] = [];
        vi.stubGlobal('requestAnimationFrame', (callback: FrameRequestCallback) => frames.push(callback));
        vi.stubGlobal('cancelAnimationFrame', () => {});
        const { afterPaint, PAINT_FALLBACK_MS } = await import('./launch-perf');
        const done = vi.fn();
        afterPaint(done);
        while (frames.length) frames.shift()!(0);
        vi.advanceTimersByTime(PAINT_FALLBACK_MS);
        expect(done).toHaveBeenCalledTimes(1);
        vi.unstubAllGlobals();
        vi.useRealTimers();
    });

    it('diagnostic bridge errors never reject a successful page read', async () => {
        report.mockRejectedValueOnce(new Error('bridge unavailable'));
        const { markLaunchStage } = await import('./launch-perf');
        expect(() => markLaunchStage('first_data')).not.toThrow();
        await Promise.resolve();
    });
});
