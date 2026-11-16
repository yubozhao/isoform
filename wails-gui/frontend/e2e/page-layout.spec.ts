import { test, expect } from "@playwright/test";
import { appendFileSync, mkdirSync } from "node:fs";
import { PAGE_LAYOUT_WIDTHS, pageLayoutFailures } from "./page-layout-checks";

// The layout check of today-layout.spec.ts for the other pages of the
// computer pass (computer-activity v1.0.0), on fixture data, at a narrow and
// a wide window. Runs in the daily trial with Today's (same config); also by
// hand: pnpm exec playwright test --config playwright.today-layout.config.ts
const out = process.env.DAILY_TRIAL_OUT;
if (out) mkdirSync(out, { recursive: true });

const PAGES = [
    { name: "activity", query: "page=activity&section=yourDay", area: '[data-testid="activity-feed"]', rows: "[data-feed-kind]" },
    { name: "activity-agents", query: "page=activity&section=agents", area: '[data-testid="activity-feed"]', rows: "[data-feed-kind]" },
] as const;

for (const target of PAGES) {
    for (const width of PAGE_LAYOUT_WIDTHS) {
        test(`${target.name} keeps its layout at ${width} px`, async ({ page }) => {
            await page.clock.setFixedTime(new Date("2026-10-23T10:00:00-07:00"));
            await page.setViewportSize({ width, height: 1000 });
            await page.goto(`/e2e/page-layout.html?${target.query}&sidebar=240&theme=paper&lang=en`);
            await expect(page.locator(target.rows).first()).toBeVisible();
            await page.evaluate(() => document.fonts.ready);
            const failures = await pageLayoutFailures(page, target.area, target.rows);
            let screenshot: "match" | "new" | "differs" = "match";
            try {
                await expect(page).toHaveScreenshot(`${target.name}-layout-${width}.png`, { fullPage: true });
            } catch (error) {
                screenshot = /writing actual|missing/i.test((error as Error).message) ? "new" : "differs";
                if (screenshot === "differs") failures.push("screenshot differs from the baseline");
            }
            if (out) appendFileSync(`${out}/pages.jsonl`, JSON.stringify({ page: `${target.name}-layout-${width}`, ok: failures.length === 0, paintMs: null, budgetMs: 0, checks: ["no sideways overflow", "sections never overlap", "rows never overlap"], failures, screenshot }) + "\n");
            expect(failures).toEqual([]);
        });
    }
}
