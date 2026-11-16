import { test, expect } from "@playwright/test";
import { appendFileSync, mkdirSync } from "node:fs";
import { TODAY_LAYOUT_WIDTHS, todayLayoutFailures } from "./today-layout-checks";

// Today's layout at a narrow and a wide window, on fixture data with a full
// day strip (group H of docs/today-failure-history.md). Runs in the daily
// trial (scripts/daily-trial/daily-trial.sh), which keeps the baselines and
// reads the results from $DAILY_TRIAL_OUT/pages.jsonl; also runs by hand:
//   pnpm exec playwright test --config playwright.today-layout.config.ts
const out = process.env.DAILY_TRIAL_OUT;
if (out) mkdirSync(out, { recursive: true });

for (const width of TODAY_LAYOUT_WIDTHS) {
    test(`Today keeps its layout at ${width} px`, async ({ page }) => {
        await page.clock.setFixedTime(new Date("2026-10-23T10:00:00-07:00"));
        await page.setViewportSize({ width, height: 1000 });
        await page.goto("/e2e/today-layout.html?sidebar=240&theme=paper&lang=en");
        await expect(page.getByTestId("today-day-track")).toBeVisible();
        await expect(page.getByTestId("today-topics")).toBeVisible();
        await page.evaluate(() => document.fonts.ready);
        const failures = await todayLayoutFailures(page);
        let screenshot: "match" | "new" | "differs" = "match";
        try {
            await expect(page).toHaveScreenshot(`today-layout-${width}.png`, { fullPage: true });
        } catch (error) {
            screenshot = /writing actual|missing/i.test((error as Error).message) ? "new" : "differs";
            if (screenshot === "differs") failures.push("screenshot differs from the baseline");
        }
        if (out) appendFileSync(`${out}/pages.jsonl`, JSON.stringify({ page: `today-layout-${width}`, ok: failures.length === 0, paintMs: null, budgetMs: 0, checks: ["strip labels fit", "no sideways overflow", "sections never overlap"], failures, screenshot }) + "\n");
        expect(failures).toEqual([]);
    });
}
