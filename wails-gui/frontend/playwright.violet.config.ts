import { defineConfig } from '@playwright/test';

export default defineConfig({
    testDir: './e2e',
    testMatch: ['brand-violet.spec.ts', 'brand-violet-layout.spec.ts'],
    timeout: 120_000,
    expect: { timeout: 30_000 },
    workers: 1,
    outputDir: process.env.VIOLET_BROWSER_OUTPUT ?? '/tmp/brand-violet-browser',
    use: { baseURL: 'http://127.0.0.1:34117', headless: true, browserName: 'webkit', reducedMotion: 'reduce' },
    webServer: {
        command: 'pnpm exec vite --config vite.violet.config.ts --host 127.0.0.1 --port 34117 --strictPort',
        url: 'http://127.0.0.1:34117/e2e/brand-violet.html',
        reuseExistingServer: false,
        timeout: 120_000,
    },
});
