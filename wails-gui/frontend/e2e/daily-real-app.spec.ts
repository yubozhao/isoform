import { test, expect, type Page, type Locator } from "@playwright/test";
import { appendFileSync, mkdirSync } from "node:fs";

// The daily real-app test (docs/rework-2026-11.md, step 4): the built app on
// a seeded test data folder, driven through its browser bridge. Each page must
// paint within its budget, show what it is for, raise no page errors, and
// match yesterday's screenshot. Results go to $DAILY_TRIAL_OUT/pages.jsonl for
// scripts/daily-trial/report.mjs. Run only by daily-trial.sh on the test
// machine, never against an app on someone's own data.

const out = process.env.DAILY_TRIAL_OUT ?? "test-results/daily";
mkdirSync(out, { recursive: true });

type PageResult = { page: string; ok: boolean; paintMs: number | null; budgetMs: number; checks: string[]; failures: string[]; screenshot: "match" | "new" | "differs" | "skipped" };

function record(result: PageResult) {
    appendFileSync(`${out}/pages.jsonl`, JSON.stringify(result) + "\n");
}

let page: Page;
const pageErrors: string[] = [];
const refused = new Set<string>();
// Calls the bridge keeps native-only on purpose (browser_rpc_allowlist.go):
// telemetry, voice input, the menu bar, the SMS status check, the chat's
// setup steps, saved views and the phone's brief schedule.
const nativeOnly = new Set(["RecordProductActivity", "RecordTodaySectionSeen", "InitVoiceInput", "SetMenuBarBriefPlayback", "GetSMSChannelStatus", "RecordTasksSectionSeen",
    // Kept off by mobile_allowlist_test.go and the Settings guard test.
    "GetMainChatSetup", "GetTodayTopics", "GetMobileBriefSchedule", "SavePeopleSavedView", "RecordTodayAction"]);

test.describe.configure({ mode: "serial" });

test.beforeAll(async ({ browser }) => {
    page = await browser.newPage();
    page.on("pageerror", (error) => pageErrors.push(error.message));
    page.on("console", (message) => {
        // A refused bridge call is listed by method name below, not as a page error.
        if (message.type() === "error" && !/status of 403/.test(message.text())) pageErrors.push(message.text());
    });
    // The bridge answers 403 for a method it does not serve to a browser.
    // Each one is a page that cannot load in this test: list it by name.
    page.on("response", async (response) => {
        if (response.status() !== 403 || !response.url().endsWith("/api/call")) return;
        const body = await response.json().catch(() => null);
        const method = /method not allowed: (\w+)/.exec(body?.error ?? "")?.[1] ?? "unknown";
        if (!refused.has(method) && !nativeOnly.has(method)) {
            refused.add(method);
            appendFileSync(`${out}/refused-methods.txt`, method + "\n");
            pageErrors.push(`bridge refused ${method}`);
        }
    });
});

test.afterAll(async () => { await page?.close(); });

/** Opens a page from the sidebar (or with open), times it until marker shows, then checks and screenshots it. */
async function visit(name: string, budgetMs: number, open: () => Promise<void>, marker: () => Locator, checks: (r: PageResult) => Promise<void> = async () => {}) {
    const result: PageResult = { page: name, ok: true, paintMs: null, budgetMs, checks: [], failures: [], screenshot: "skipped" };
    const errorsBefore = pageErrors.length;
    try {
        const started = Date.now();
        await open();
        await marker().first().waitFor({ state: "visible", timeout: Math.max(30_000, budgetMs) });
        result.paintMs = Date.now() - started;
        if (result.paintMs > budgetMs) result.failures.push(`painted in ${result.paintMs} ms, budget ${budgetMs} ms`);
        await checks(result);
    } catch (error) {
        result.failures.push(`did not show: ${(error as Error).message.split("\n")[0]}`);
    }
    const newErrors = pageErrors.slice(errorsBefore);
    if (newErrors.length) result.failures.push(`${newErrors.length} page error(s): ${newErrors[0].slice(0, 160)}`);
    try {
        await page.waitForTimeout(800);
        await expect(page).toHaveScreenshot(`${name}.png`, { fullPage: false });
        result.screenshot = "match";
    } catch (error) {
        const message = (error as Error).message;
        result.screenshot = /writing actual|missing/i.test(message) ? "new" : "differs";
        if (result.screenshot === "differs") result.failures.push("screenshot differs from the baseline");
    }
    result.ok = result.failures.length === 0;
    // The result is the line in pages.jsonl; the test itself passes so the
    // serial run goes on to the next page.
    record(result);
}

const nav = (name: string) => async () => {
    await page.getByRole("navigation").getByRole("button", { name, exact: true }).first().click();
};

test("Today", async () => {
    await visit("today", 3000, async () => { await page.goto("/"); }, () => page.locator("main h1").filter({ visible: true }));
});

// trialSeedBriefWords in wails-gui/trial_seed_test.go.
const SEEDED_BRIEF_WORDS = "Daily test brief: the Halcyon review is the first thing today.";

test("A built brief a later pass stripped still shows and plays", async () => {
    // The seed saved an earlier brief again without its words and recording,
    // as the live refresh did on 2026-11-15. Once built, the latest brief
    // always shows with both (mobile_brief_show_built.go).
    await visit("stripped-brief", 3000, async () => {}, () => page.getByText(SEEDED_BRIEF_WORDS).filter({ visible: true }), async (r) => {
        r.checks.push("the built words show");
        await page.getByTestId("brief-audio-toggle").filter({ visible: true }).first().click();
        await expect(page.locator('[data-testid="brief-audio-toggle"][aria-pressed="true"]').filter({ visible: true }).first()).toBeVisible({ timeout: 15_000 });
        r.checks.push("its recording plays");
        await page.getByTestId("brief-audio-toggle").filter({ visible: true }).first().click();
    });
});

test("The scheduled brief arrives and plays", async () => {
    // The seed set the morning brief three minutes after launch: the app
    // writes, seals and records it on its own schedule. Wait for it on Today,
    // then press play.
    // The seeded earlier brief must give way: the newest brief wins.
    await visit("brief-audio", 420_000, async () => {}, () => page.locator("main").filter({ hasNot: page.getByText(SEEDED_BRIEF_WORDS) }).getByTestId("brief-audio-toggle").filter({ visible: true }), async (r) => {
        r.checks.push("scheduled brief arrived with a recording");
        await page.getByTestId("brief-audio-toggle").filter({ visible: true }).first().click();
        await expect(page.locator('[data-testid="brief-audio-toggle"][aria-pressed="true"]').filter({ visible: true }).first()).toBeVisible({ timeout: 15_000 });
        r.checks.push("the play button switched to playing");
    });
});

test("Chat sends and gets an answer", async () => {
    await visit("chat", 1500, nav("Chat"), () => page.getByRole("textbox").last(), async (r) => {
        const box = page.getByRole("textbox").last();
        // The answer is a word the prompt does not contain.
        await box.fill("Daily test: answer with the one word that is the opposite of no.");
        await box.press("Enter");
        await expect(page.getByText("Daily test: answer with the one word that is the opposite of no.").filter({ visible: true }).first()).toBeVisible({ timeout: 15_000 });
        r.checks.push("message sent");
        await expect.poll(() => page.evaluate(() => [...document.querySelectorAll("p, span, div")].some((el) =>
            el.children.length === 0 && /^\s*yes[.!]?\s*$/i.test(el.textContent ?? "") && el.getClientRects().length > 0)),
            { timeout: 180_000, intervals: [1000] }).toBe(true);
        r.checks.push("answer arrived");
    });
});

test("Tasks", async () => {
    await visit("tasks", 1500, nav("Tasks"), () => page.getByPlaceholder("Give Isoform a task…"));
});

test("Activity", async () => {
    await visit("activity", 2000, nav("Activity"), () => page.locator("main").getByRole("heading").first());
});

test("Activity, from your watch", async () => {
    await visit("watch-recordings", 2000, async () => {
        await page.locator('[data-activity-section="watch"]').click();
    }, () => page.getByTestId("watch-recordings"), async (r) => {
        await expect(page.getByTestId("watch-recordings").getByText(/watch/i).first()).toBeVisible();
        r.checks.push("seeded watch recording listed");
    });
});

for (const [name, label] of [["about-you", "About you"], ["how-you-do-things", "How you do things"], ["who-you-know", "Who you know"]] as const) {
    test(label, async () => {
        await visit(name, 1500, nav(label), () => page.locator("main").getByRole("heading").first());
    });
}

test("Settings", async () => {
    await visit("settings", 1500, async () => {
        await page.keyboard.press(process.platform === "darwin" ? "Meta+Comma" : "Control+Comma");
    }, () => page.getByRole("heading", { name: /settings/i }));
});
