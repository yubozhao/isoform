import { test, expect, type Page } from '@playwright/test';

// The Tasks walk (hub item 6): give Isoform three tasks, start them, and
// follow each to its end: one finishes, one stops on something the person
// must do, one the person stops. Every button on the way must do work (call
// the backend), not only open a page.

type Call = { method: string; args: unknown[] };
const calls = (page: Page) => page.evaluate(() => ((window as any).fixtureCalls as Call[]).map((call) => call.method));
const count = async (page: Page, method: string) => (await calls(page)).filter((name) => name === method).length;
const row = (page: Page, title: string) => page.locator('div,li').filter({ hasText: title }).filter({ has: page.getByRole('button') }).last();

test.beforeEach(({ page }) => {
    page.on('pageerror', (error) => { console.error('Fixture error:', error.message); });
});

test('a task given in the box runs to its end, and every button on the way does work', async ({ page }) => {
    await page.goto('/e2e/tasks-walk.html');
    const box = page.getByPlaceholder('Give Isoform a task…');
    await expect(box).toBeVisible();

    // 1. Give three tasks. Make tasks reads them; nothing starts until Add.
    await box.fill('Draft the launch memo\nCheck the export, it will fail\nResearch pricing, a long one');
    await page.getByRole('button', { name: 'Make tasks' }).click();
    await expect(page.getByText('Check these. Nothing starts until you add them.')).toBeVisible();
    expect(await count(page, 'ParseBrainDump')).toBe(1);
    expect(await count(page, 'AcceptHandoff')).toBe(0);
    await page.getByRole('button', { name: 'Add 3 to Tasks' }).click();
    await expect.poll(() => count(page, 'AddBrainDump')).toBe(1);

    // 2. Do it starts the task after its Undo window; it runs, then finishes.
    await expect(row(page, 'Draft the launch memo').getByRole('button', { name: 'Do it' })).toBeVisible();
    await row(page, 'Draft the launch memo').getByRole('button', { name: 'Do it' }).click();
    await expect(page.getByRole('button', { name: 'Undo' }).first()).toBeVisible();
    await expect.poll(() => count(page, 'AcceptHandoff'), { timeout: 15_000 }).toBe(1);
    await expect(page.getByText('Done this week')).toBeVisible({ timeout: 15_000 });
    await expect(page.getByText('Draft the launch memo')).toBeVisible();

    // 3. A task that hits something only the person can do says what it needs.
    await row(page, 'Check the export, it will fail').getByRole('button', { name: 'Do it' }).click();
    await expect.poll(() => count(page, 'AcceptHandoff'), { timeout: 15_000 }).toBe(2);
    await expect(page.getByText('Needs your sign-in to the export site.')).toBeVisible({ timeout: 15_000 });

    // 4. A running task stops from its row, and the backend is told.
    await row(page, 'Research pricing, a long one').getByRole('button', { name: 'Do it' }).click();
    await expect.poll(() => count(page, 'AcceptHandoff'), { timeout: 15_000 }).toBe(3);
    const stop = row(page, 'Research pricing, a long one').getByRole('button', { name: 'Stop' });
    await expect(stop).toBeVisible({ timeout: 15_000 });
    await stop.click();
    await expect.poll(() => count(page, 'AbortHandoff')).toBe(1);
    await expect(stop).toBeHidden();

    // Every click above reached the backend: no button only opened a page.
    const methods = await calls(page);
    for (const method of ['ParseBrainDump', 'AddBrainDump', 'AcceptHandoff', 'AbortHandoff']) {
        expect(methods, `${method} was never called`).toContain(method);
    }
});
