// Proof for mark motion v1.0.1 from e2e/mark-motion.tsx: every state and
// event as a strip at the corner's size, the real sidebar corner in each,
// rest identical to today's logo, reduced motion, and the cost at rest and
// in orbit. Screenshots go to MARK_MOTION_OUT.
import fs from 'node:fs';
import path from 'node:path';
import { expect, test, type Page } from '@playwright/test';

const OUT = process.env.MARK_MOTION_OUT ?? '/tmp/mark-motion-shots';
fs.mkdirSync(OUT, { recursive: true });
const THEMES = [['paper', 'light'], ['onedark', 'dark']] as const;
const STRIPS = [
    'pose:working', 'working.enter', 'working.loop', 'working.leave',
    'pose:listening', 'listening.enter', 'listening.loop', 'listening.leave',
    'pose:paused', 'paused.enter', 'paused.peek', 'paused.leave',
    'needs', 'briefMorning', 'briefEvening', 'sent', 'retry', 'blink',
];
const FRAME = 1000 / 12;

for (const [theme, name] of THEMES) {
    test(`strips ${name}`, async ({ page }) => {
        for (const seq of STRIPS) {
            await page.goto(`/e2e/mark-motion.html?mode=strip&seq=${seq}&theme=${theme}`);
            const strip = page.locator('[data-strip]');
            await expect(strip).toBeVisible();
            await strip.screenshot({ path: path.join(OUT, 'strips', `${seq.replace(':', '-')}-${name}.png`) });
        }
    });
}

async function pixels(page: Page, png: Buffer): Promise<number[]> {
    return page.evaluate(async (b64) => {
        const img = new Image();
        img.src = `data:image/png;base64,${b64}`;
        await img.decode();
        const c = document.createElement('canvas');
        c.width = img.width; c.height = img.height;
        const x = c.getContext('2d')!;
        x.drawImage(img, 0, 0);
        return Array.from(x.getImageData(0, 0, c.width, c.height).data);
    }, png.toString('base64'));
}

function differing(a: number[], b: number[], over = 6): number {
    let n = 0;
    for (let i = 0; i < a.length; i += 4) if (Math.abs(a[i] - b[i]) + Math.abs(a[i + 1] - b[i + 1]) + Math.abs(a[i + 2] - b[i + 2]) > over) n++;
    return n;
}

const report: Record<string, unknown> = {};

for (const [theme, name] of THEMES) {
    test(`rest is today's logo ${name}`, async ({ page }) => {
        await page.goto(`/e2e/mark-motion.html?mode=identity&theme=${theme}`);
        await page.waitForTimeout(300);
        {
            const x = await pixels(page, await page.locator('[data-id="open-before"]').screenshot());
            const y = await pixels(page, await page.locator('[data-id="open-control"]').screenshot());
            report[`control-${name}`] = differing(x, y);
        }
        for (const where of ['open', 'collapsed']) {
            const before = await page.locator(`[data-id="${where}-before"]`).screenshot();
            const after = await page.locator(`[data-id="${where}-after"]`).screenshot({ path: path.join(OUT, `rest-${where}-${name}.png`) });
            fs.writeFileSync(path.join(OUT, `rest-${where}-before-${name}.png`), before);
            const pa = await pixels(page, before);
            const pb = await pixels(page, after);
            const where_: number[] = [];
            for (let i = 0; i < pa.length; i += 4) if (Math.abs(pa[i] - pb[i]) + Math.abs(pa[i + 1] - pb[i + 1]) + Math.abs(pa[i + 2] - pb[i + 2]) > 6) where_.push(i / 4);
            report[`${where}-${name}-diff`] = where_.slice(0, 20).map((i) => [i % 256, Math.floor(i / 256)]);
            fs.writeFileSync(path.join(OUT, `identity-${name}.json`), JSON.stringify(report, null, 2));
            expect(differing(pa, pb)).toBe(0);
        }
        // How close the kit's drawing is to the logo at rest: the change
        // from one to the other when a gesture starts.
        const logo20 = await page.locator('[data-id="collapsed-before"]').screenshot();
        const kit20 = await page.locator('[data-id="kit-20"]').screenshot({ path: path.join(OUT, `rest-kit-19-${name}.png`) });
        await page.locator('[data-id="kit-160"]').screenshot({ path: path.join(OUT, `rest-kit-160-${name}.png`) });
        await page.locator('[data-id="logo-160"]').screenshot({ path: path.join(OUT, `rest-logo-160-${name}.png`) });
        const a = await pixels(page, logo20);
        const b = await pixels(page, kit20);
        // Device pixels at 2x that differ at all, and by more than a fifth of
        // the way from background to ink.
        report[`kitVsLogo19px-${name}`] = a.length === b.length ? { any: differing(a, b), visible: differing(a, b, 120) } : 'size differs';
        fs.writeFileSync(path.join(OUT, `identity-${name}.json`), JSON.stringify(report, null, 2));
    });
}

async function corner(page: Page, theme: string, collapsed = false) {
    await page.clock.install();
    await page.goto(`/e2e/mark-motion.html?mode=corner&theme=${theme}${collapsed ? '&collapsed=1' : ''}`);
    await page.bringToFront();
    await expect(page.locator('[data-mark-motion]')).toBeVisible();
    await page.clock.runFor(500);
}
const emit = (page: Page, event: string, payload?: unknown) => page.evaluate(([e, p]) => (window as any).__mark.emit(e, p), [event, payload] as const);
const clip = { x: 0, y: 0, width: 240, height: 96 };
const shot = (page: Page, file: string) => page.screenshot({ path: path.join(OUT, 'corner', file), clip });

for (const [theme, name] of THEMES) {
    test(`corner states and events ${name}`, async ({ page }) => {
        await corner(page, theme);
        await shot(page, `00-rest-${name}.png`);
        // Working: the line says so; the moon circles.
        await emit(page, 'doing-now:changed', { kind: 'working', title: 'Pricing page copy' });
        await page.clock.runFor(1_200);
        await shot(page, `01-working-a-${name}.png`);
        await page.clock.runFor(700);
        await shot(page, `01-working-b-${name}.png`);
        await expect(page.locator('[data-mark-motion] title')).toHaveText('Working on: Pricing page copy');
        // A recorded call.
        await emit(page, 'doing-now:changed', { kind: 'meeting', app: 'Zoom' });
        await page.clock.runFor(4_000);
        await shot(page, `02-listening-${name}.png`);
        // Paused, and the peek when pointed at.
        await emit(page, 'doing-now:changed', { kind: 'paused' });
        await page.clock.runFor(4_000);
        await shot(page, `03-paused-${name}.png`);
        await page.locator('[data-mark-motion]').hover();
        await page.clock.runFor(700);
        await shot(page, `03-paused-peek-${name}.png`);
        await page.mouse.move(600, 400);
        // Back to rest.
        await emit(page, 'doing-now:changed', { kind: 'watching' });
        await page.clock.runFor(4_000);
        await shot(page, `04-rest-again-${name}.png`);
        // Needs you: the count rises.
        await page.evaluate(() => (window as any).__mark.setWaiting(2));
        await page.clock.runFor(1_400);
        await shot(page, `05-needs-${name}.png`);
        await page.clock.runFor(3_000);
        // The brief: a new morning brief is stored.
        await page.evaluate(() => (window as any).__mark.setBrief({ brief: { briefId: '2026-11-10-morning', slot: 'morning', asOf: '2026-11-10T07:00:00-08:00', localDate: '2026-11-10', version: 1, coldStart: false }, building: false, enabled: true, nextAt: '', nextSlot: '' }));
        await emit(page, 'brief:stored', '2026-11-10-morning');
        await page.clock.runFor(1_900);
        await shot(page, `06-brief-sunrise-${name}.png`);
        await page.clock.runFor(3_000);
        // No signal in the app yet: the test hook plays them.
        await page.evaluate(() => (window as any).__isoformMark.event('sent'));
        await page.clock.runFor(1_000);
        await shot(page, `07-sent-${name}.png`);
        await page.clock.runFor(3_000);
        await page.evaluate(() => (window as any).__isoformMark.event('retry'));
        await page.clock.runFor(1_300);
        await shot(page, `08-retry-${name}.png`);
        await page.clock.runFor(3_000);
        await page.evaluate(() => (window as any).__isoformMark.event('brief', { evening: true }));
        await page.clock.runFor(1_600);
        await shot(page, `09-brief-sunset-${name}.png`);
    });

    test(`collapsed corner ${name}`, async ({ page }) => {
        await corner(page, theme, true);
        await page.screenshot({ path: path.join(OUT, 'corner', `10-collapsed-rest-${name}.png`), clip: { x: 0, y: 0, width: 72, height: 96 } });
        await emit(page, 'doing-now:changed', { kind: 'working', title: 'Pricing page copy' });
        await page.clock.runFor(1_200);
        await page.screenshot({ path: path.join(OUT, 'corner', `11-collapsed-working-${name}.png`), clip: { x: 0, y: 0, width: 72, height: 96 } });
    });
}

// Bo, Nov 13: the window on screen while another app is active. A WKWebView
// then reports document.hasFocus() false, and the mark used to stay still.
test('moves on screen while another app has focus', async ({ page }) => {
    await page.addInitScript(() => { Document.prototype.hasFocus = () => false; });
    await corner(page, 'paper');
    expect(await page.evaluate(() => document.hasFocus())).toBe(false);
    const mark = page.locator('[data-mark-motion]');
    const frames: string[] = [];
    const grab = async (label: string) => {
        frames.push(await mark.innerHTML());
        await shot(page, `20-unfocused-${label}.png`);
    };
    await grab('00-rest');
    await emit(page, 'doing-now:changed', { kind: 'working', title: 'Pricing page copy' });
    for (const ms of [250, 500, 750, 1000, 1250, 1500]) {
        await page.clock.runFor(250);
        await grab(`01-working-${ms}ms`);
    }
    await emit(page, 'doing-now:changed', { kind: 'watching' });
    await page.clock.runFor(4_000);
    await page.evaluate(() => (window as any).__mark.setWaiting(2));
    await page.clock.runFor(700);
    await grab('02-needs');
    expect(new Set(frames).size).toBeGreaterThanOrEqual(6);
    // Rest blinks on its own, 20 to 40 seconds apart.
    await page.clock.runFor(5_000);
    expect(await page.evaluate(() => (window as any).__isoformMark.timerPending)).toBe('blink');
});

test('reduced motion shows the pose in one step and plays nothing', async ({ browser }) => {
    const context = await browser.newContext({ reducedMotion: 'reduce', deviceScaleFactor: 2, viewport: { width: 1180, height: 700 } });
    const page = await context.newPage();
    await corner(page, 'paper');
    await emit(page, 'doing-now:changed', { kind: 'working', title: 'Pricing page copy' });
    await page.clock.runFor(10);
    await shot(page, `12-reduced-working-light.png`);
    const a = await page.locator('[data-mark-motion]').innerHTML();
    await page.clock.runFor(1_500);
    expect(await page.locator('[data-mark-motion]').innerHTML()).toBe(a);
    await page.evaluate(() => (window as any).__mark.setWaiting(3));
    await page.clock.runFor(1_000);
    expect(await page.locator('[data-mark-motion]').innerHTML()).toBe(a);
    await context.close();
});

test('cost at rest and in orbit', async ({ page }) => {
    await page.goto('/e2e/mark-motion.html?mode=corner&theme=paper');
    await page.bringToFront();
    await expect(page.locator('[data-mark-motion]')).toBeVisible();
    await page.waitForTimeout(1_500);
    const cdp = await page.context().newCDPSession(page);
    await cdp.send('Performance.enable');
    const metric = async () => Object.fromEntries((await cdp.send('Performance.getMetrics')).metrics.map((m) => [m.name, m.value]));
    const measure = async (ms: number) => {
        // Count the mark's paints and timers by watching its DOM.
        await page.evaluate(() => {
            const w = window as any;
            w.__mutations = 0;
            w.__observer?.disconnect();
            w.__observer = new MutationObserver((list) => { w.__mutations += list.length; });
            w.__observer.observe(document.querySelector('[data-mark-motion]')!, { attributes: true, subtree: true, childList: true });
        });
        const a = await metric();
        await page.waitForTimeout(ms);
        const b = await metric();
        const mutations = await page.evaluate(() => (window as any).__mutations);
        return {
            seconds: ms / 1000,
            mainThreadBusyPercent: Math.round(((b.TaskDuration - a.TaskDuration) / (ms / 1000)) * 1000) / 10,
            scriptMsPerSecond: Math.round(((b.ScriptDuration - a.ScriptDuration) * 1000) / (ms / 1000) * 10) / 10,
            layoutsPerSecond: Math.round(((b.LayoutCount - a.LayoutCount) / (ms / 1000)) * 10) / 10,
            markMutations: mutations,
        };
    };
    const rest = await measure(15_000);
    await emit(page, 'doing-now:changed', { kind: 'working', title: 'Pricing page copy' });
    await page.waitForTimeout(1_500);
    const orbit = await measure(15_000);
    const result = { rest, orbit, note: 'Chromium headless, M-series Mac, the harness page with the real Sidebar; main-thread task time per second of wall time.' };
    fs.writeFileSync(path.join(OUT, 'cost.json'), JSON.stringify(result, null, 2));
    console.log(JSON.stringify(result));
    expect(rest.markMutations).toBeLessThanOrEqual(6);
});
