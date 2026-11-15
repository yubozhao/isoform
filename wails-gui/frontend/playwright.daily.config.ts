import { defineConfig } from "@playwright/test";

// The daily real-app test (scripts/daily-trial/daily-trial.sh): drives the
// built app through its browser bridge on the test machine. Never run against
// the app on someone's own data.
const out = process.env.DAILY_TRIAL_OUT ?? "test-results/daily";
export default defineConfig({
  testDir: "./e2e",
  testMatch: "daily-real-app.spec.ts",
  timeout: 540_000,
  workers: 1,
  outputDir: `${out}/playwright`,
  snapshotPathTemplate: `${process.env.DAILY_TRIAL_BASELINE ?? "test-results/daily-baseline"}/{arg}{ext}`,
  expect: { timeout: 30_000, toHaveScreenshot: { maxDiffPixelRatio: 0.04, animations: "disabled" } },
  use: {
    baseURL: process.env.DAILY_TRIAL_BRIDGE ?? "http://127.0.0.1:34116",
    headless: true,
    actionTimeout: 30_000,
    navigationTimeout: 30_000,
    viewport: { width: 1280, height: 820 },
  },
  projects: [{ name: "chromium", use: { browserName: "chromium" } }],
});
