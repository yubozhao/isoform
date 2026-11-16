import { defineConfig } from '@playwright/test';

// Today's layout at 920 and 1440 px on fixture data (e2e/today-layout.spec.ts).
// WebKit, as the app's WebView. The daily trial points the baselines at
// $DAILY_TRIAL_BASELINE; by hand they go to test-results/today-layout-baseline.
export default defineConfig({
    testDir: './e2e',
    testMatch: ['today-layout.spec.ts'],
    timeout: 60_000,
    workers: 1,
    outputDir: `${process.env.DAILY_TRIAL_OUT ?? 'test-results/today-layout'}/today-layout`,
    snapshotPathTemplate: `${process.env.DAILY_TRIAL_BASELINE ?? 'test-results/today-layout-baseline'}/{arg}{ext}`,
    expect: { toHaveScreenshot: { maxDiffPixelRatio: 0.01, animations: 'disabled' } },
    use: { baseURL: 'http://127.0.0.1:5179', browserName: 'webkit', headless: true, reducedMotion: 'reduce' },
    webServer: {
        command: 'pnpm exec vite --host 127.0.0.1 --port 5179 --strictPort',
        url: 'http://127.0.0.1:5179/e2e/today-layout.html',
        reuseExistingServer: false,
    },
});
