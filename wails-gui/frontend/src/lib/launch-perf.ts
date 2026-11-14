import { ReportLaunchStage } from './wails-shim-app';

const reported = new Set<string>();
export function markLaunchStage(stage: string) {
    if (reported.has(stage)) return;
    reported.add(stage);
    try { void ReportLaunchStage(stage).catch(() => {}); } catch { /* Older browser bridge. */ }
    if (!reported.has('first_screen_rendered') && ['first_data', 'chat_rendered', 'counts_rendered', 'overview_rendered'].every((name) => reported.has(name))) {
        markLaunchPaint('first_screen_rendered');
    }
}

/** The longest a paint milestone waits for animation frames. A window that
 *  launches hidden (login launch, menu bar only, behind other windows) gets
 *  none, so a frame-only milestone never fired and everything waiting on it
 *  (page snapshots, first reads) waited until the window was shown. */
export const PAINT_FALLBACK_MS = 1000;

/** afterPaint runs callback once, after two animation frames or after
 *  PAINT_FALLBACK_MS, whichever comes first. It returns a cancel function. */
export function afterPaint(callback: () => void): () => void {
    let done = false;
    let second = 0;
    const fire = () => {
        if (done) return;
        done = true;
        clearTimeout(timer);
        cancelAnimationFrame(first);
        cancelAnimationFrame(second);
        callback();
    };
    const first = requestAnimationFrame(() => {
        second = requestAnimationFrame(fire);
    });
    const timer = setTimeout(fire, PAINT_FALLBACK_MS);
    return () => {
        done = true;
        clearTimeout(timer);
        cancelAnimationFrame(first);
        cancelAnimationFrame(second);
    };
}

export function markLaunchPaint(stage: string) {
    afterPaint(() => markLaunchStage(stage));
}
