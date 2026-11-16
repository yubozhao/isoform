import { test, expect } from '@playwright/test';

test.beforeEach(({ page }) => {
    page.on('pageerror', (error) => { console.error('Fixture error:', error.message); });
});

for (const theme of ['paper', 'onedark']) {
    // The composer's topic scope control was removed (b126ce084, chat-review v1.0.2);
    // the text still sits above its controls, and a topic chip below the text.
    test(`Polish 5: text and controls occupy separate rows in ${theme}`, async ({ page }, testInfo) => {
        await page.setViewportSize({ width: 1400, height: 900 });
        await page.goto(`/e2e/polish-structure.html?page=chat&sidebar=56&theme=${theme}`);
        const text = page.locator('[data-composer-textarea]');
        const attach = page.getByRole('button', { name: 'Attach files' });
        await expect(attach).toBeVisible();
        const textBox = (await text.boundingBox())!;
        expect((await attach.boundingBox())!.y).toBeGreaterThanOrEqual(textBox.y + textBox.height);
        await expect(page.getByRole('button', { name: 'Choose chat scope' })).toHaveCount(0);
        await page.screenshot({ path: testInfo.outputPath(`polish-five-chat-${theme}.png`) });
        await page.goto(`/e2e/polish-structure.html?page=chat&sidebar=56&theme=${theme}&topic`);
        const chip = page.getByTestId('topic-composer-chip');
        await expect(chip).toBeVisible();
        const topicText = (await page.getByRole('textbox').boundingBox())!;
        expect((await chip.boundingBox())!.y).toBeGreaterThanOrEqual(topicText.y + topicText.height);
        expect((await page.getByTestId('topic-composer-toolbar').boundingBox())!.y).toBeGreaterThanOrEqual(topicText.y + topicText.height);
        await page.screenshot({ path: testInfo.outputPath(`polish-five-topic-${theme}.png`) });
        await chip.getByRole('button').click();
        await expect(page.locator('[data-composer-textarea]')).toBeVisible();
    });
}


for (const width of [477, 478, 696, 699, 700, 760]) {
    test(`Polish 6: topic grid at ${width}px of page width`, async ({ page }) => {
        await page.setViewportSize({ width, height: 1000 });
        await page.goto('/e2e/polish-structure.html?page=topics');
        const column = page.getByTestId('today-column');
        await expect(column).toBeVisible();
        expect((await column.boundingBox())!.width).toBe(width);
        const cards = column.getByTestId('today-topic-card');
        await expect(cards).toHaveCount(6);
        const boxes = await cards.evaluateAll((nodes) => nodes.map((node) => node.getBoundingClientRect().toJSON()));
        const columns = boxes.filter((box) => box.y === boxes[0].y).length;
        expect(columns).toBe(width < 700 ? 2 : 3);
        for (const box of boxes) expect(box.width).toBeGreaterThanOrEqual(200);
    });
}


for (const theme of ['paper', 'onedark']) {
    test(`Polish 6: Activity marks keep their roles in ${theme}`, async ({ page }, testInfo) => {
        await page.setViewportSize({ width: 1400, height: 1000 });
        await page.goto(`/e2e/polish-structure.html?page=activity&theme=${theme}`);
        const marks = page.getByTestId('stitched-legend').locator('[aria-hidden]');
        await expect(marks).toHaveCount(3);
        const colors = await marks.evaluateAll((nodes) => nodes.map((node, i) => {
            const style = getComputedStyle(node);
            return i === 2 ? style.borderTopColor : style.backgroundColor;
        }));
        expect(new Set(colors).size).toBe(3);
        const work = page.locator('[data-thread-lane]:not([data-interruption]) [data-testid="stitched-stretch"]').first();
        const interruption = page.locator('[data-interruption] [data-testid="stitched-stretch"]').first();
        await expect(work).toHaveCSS('background-color', colors[0]);
        await expect(interruption).toHaveCSS('background-color', colors[1]);
        await expect(page.getByTestId('stitched-gap-link').first()).toHaveCSS('border-top-color', colors[2]);
        await expect(page.locator('[data-work-stretch]').first()).toHaveCSS('background-color', colors[0]);
        await page.screenshot({ path: testInfo.outputPath(`polish-six-activity-${theme}.png`), fullPage: true });
    });
}
