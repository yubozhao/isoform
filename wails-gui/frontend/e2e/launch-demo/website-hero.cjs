// The isoform.ai hero capture (website v19.1.0): Today with the Chat panel, asked
// "What's my 9:00?", over the harness's sample content, light, 1280x578 at 2x.
// Serve the frontend first: node_modules/.bin/vite --host 127.0.0.1 --port 34291 --strictPort
// Then: node e2e/launch-demo/website-hero.cjs out.png   (crop the answer for phones:
// x 1782-2560, y 200-840; encode both with cwebp -q 86 into website/assets/home/).
const { webkit } = require('@playwright/test');

(async () => {
    const out = process.argv[2] ?? 'hero.png';
    const browser = await webkit.launch();
    const page = await browser.newPage({ viewport: { width: 1280, height: 578 }, deviceScaleFactor: 2, colorScheme: 'light' });
    await page.clock.install({ time: new Date('2026-11-03T08:41:00-08:00') });
    await page.clock.resume();
    await page.goto('http://127.0.0.1:34291/e2e/launch-demo/index.html?theme=paper&empty=1');
    const box = page.locator('textarea:visible').first();
    await box.waitFor();
    await box.fill("What's my 9:00?");
    await box.press('Enter');
    await page.getByText('Your 9:00 is the team standup').waitFor({ timeout: 15000 });
    await page.mouse.move(5, 5);
    await page.waitForTimeout(1500);
    await page.screenshot({ path: out });
    await browser.close();
})();
