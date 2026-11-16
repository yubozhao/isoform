// node e2e/you-agent-shots.mjs <outDir>  (serve with: pnpm exec vite --host 127.0.0.1 --port 5193)
// You › Agent of You before and after the owner is approved, paper and onedark.
// The owner screens themselves are in Settings: e2e/aoy-owner.html.
import { chromium } from '@playwright/test';
const [out] = process.argv.slice(2);
const base = `http://127.0.0.1:${process.env.PORT ?? 5193}/e2e/you-agent.html`;
const tab = async (p) => { await p.getByRole('tab', { name: /Agent of You/ }).click(); await p.waitForTimeout(500); };
const shots = [
    ['soon', 'state=none', tab],
    ['open', 'state=on', tab],
];
const browser = await chromium.launch();
for (const theme of ['paper', 'onedark']) {
    for (const [name, query, action] of shots) {
        const page = await browser.newPage({ viewport: { width: 1180, height: 820 }, deviceScaleFactor: 2 });
        await page.goto(`${base}?${query}&theme=${theme}`);
        await page.waitForTimeout(600);
        await action(page);
        await page.screenshot({ path: `${out}/${name}-${theme}.png` });
        await page.close();
    }
}
await browser.close();
