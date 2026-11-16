import { defineConfig } from '@playwright/test';

export default defineConfig({
    testDir: './e2e',
    testMatch: ['polish-structure.spec.ts', 'stream-window.spec.ts'],
    timeout: 30_000,
    use: { baseURL: 'http://127.0.0.1:5178', browserName: 'webkit', headless: true, reducedMotion: 'reduce' },
    webServer: {
        command: 'pnpm exec vite --host 127.0.0.1 --port 5178 --strictPort',
        url: 'http://127.0.0.1:5178/e2e/polish-structure.html',
        reuseExistingServer: false,
    },
});
