import { defineConfig } from '@playwright/test';

// Mark motion v1.0.1 harness: strips, the corner and its cost, from
// e2e/mark-motion.tsx. Run with
// `pnpm exec playwright test -c playwright.mark-motion.config.ts`;
// MARK_MOTION_OUT sets where the screenshots go.
export default defineConfig({
    testDir: './e2e',
    testMatch: ['mark-motion.spec.ts'],
    timeout: 180_000,
    workers: 1,
    outputDir: process.env.MARK_MOTION_OUTPUT ?? '/tmp/mark-motion-browser',
    use: { baseURL: 'http://127.0.0.1:34131', browserName: 'chromium', headless: true, viewport: { width: 1180, height: 700 }, deviceScaleFactor: 2 },
    webServer: {
        command: 'pnpm exec vite --host 127.0.0.1 --port 34131 --strictPort',
        url: 'http://127.0.0.1:34131/e2e/mark-motion.html',
        reuseExistingServer: false,
        timeout: 120_000,
    },
});
