import { test, expect, type Page, type Locator } from "@playwright/test";
import { appendFileSync, mkdirSync } from "node:fs";
import { TODAY_LAYOUT_WIDTHS, todayLayoutFailures } from "./today-layout-checks";
import { PAGE_LAYOUT_WIDTHS, pageLayoutFailures } from "./page-layout-checks";

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
// The bridge token the app page sends, for the checks that read a binding directly.
let bridgeToken = "";
const refused = new Set<string>();
// Calls the bridge keeps native-only on purpose (browser_rpc_allowlist.go):
// telemetry, voice input, the menu bar, the SMS status check, the chat's
// setup steps, saved views and the phone's brief schedule.
const nativeOnly = new Set(["RecordProductActivity", "RecordTodaySectionSeen", "InitVoiceInput", "RecordFirstBriefSeen", "CaptureAndSaveWindowState", "SetMenuBarBriefPlayback", "GetSMSChannelStatus", "RecordTasksSectionSeen",
    // Kept off by mobile_allowlist_test.go and the Settings guard test.
    "GetMainChatSetup", "GetTodayTopics", "GetMobileBriefSchedule", "SavePeopleSavedView", "RecordTodayAction"]);

test.describe.configure({ mode: "serial" });

test.beforeAll(async ({ browser }) => {
    page = await browser.newPage();
    page.on("pageerror", (error) => pageErrors.push(error.message));
    page.on("request", (request) => {
        if (!bridgeToken && request.url().endsWith("/api/call")) bridgeToken = request.headers()["x-yansu-bridge-token"] ?? "";
    });
    page.on("console", (message) => {
        // A refused bridge call is listed by method name below, not as a page error.
        if (message.type() === "error" && !/status of 403/.test(message.text())) pageErrors.push(message.text());
    });
    // The bridge answers 403 for a method it does not serve to a browser.
    // Each one is a page that cannot load in this test: list it by name.
    page.on("response", async (response) => {
        if (response.status() !== 403 || !response.url().endsWith("/api/call")) return;
        // The method comes from the request: a response body the page dropped
        // cannot be read, and listed the refusal as "unknown".
        const body = await response.json().catch(() => null);
        const sent = (() => { try { return response.request().postDataJSON()?.method; } catch { return undefined; } })();
        const method = sent ?? /method not allowed: (\w+)/.exec(body?.error ?? "")?.[1] ?? "unknown";
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

// A row's name may carry its count or state in words ("Tasks 4 waiting on
// you", "Chat Unread"; pass-access v1.0.0 rule 5), so match the label at the
// start.
const rowName = (name: string) => new RegExp(`^${name.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}(\\b|,|$)`);
const nav = (name: string) => async () => {
    await page.getByRole("navigation").getByRole("button", { name: rowName(name) }).first().click();
};

test("Today", async () => {
    await visit("today", 3000, async () => { await page.goto("/"); }, () => page.locator("main h1").filter({ visible: true }), async (r) => {
        // Your work (computer-today SH2) under the brief, once the list is read.
        const work = page.getByTestId("today-your-work").filter({ visible: true }).first();
        if (await work.waitFor({ state: "visible", timeout: 5000 }).then(() => true, () => false)) {
            const open = await page.getByTestId("your-work-open").count();
            r.checks.push(open > 0 ? "your work: open item shown" : "your work: shown, nothing to answer");
        } else {
            r.checks.push("your work: no list read, not checked");
        }
    });
});

// Today's layout in a narrow and a wide window (group H of
// docs/today-failure-history.md); today-layout.spec.ts checks the same rules
// on fixture data with a full day strip.
for (const width of TODAY_LAYOUT_WIDTHS) {
    test(`Today at ${width} px`, async () => {
        try {
            await visit(`today-${width}`, 3000, async () => {
                await page.setViewportSize({ width, height: 900 });
                await page.goto("/");
            }, () => page.locator("main h1").filter({ visible: true }), async (result) => {
                await page.waitForTimeout(800);
                result.checks.push("no sideways overflow", "sections never overlap", "strip labels fit");
                result.failures.push(...await todayLayoutFailures(page));
            });
        } finally {
            await page.setViewportSize({ width: 1280, height: 820 });
        }
    });
}

// trialSeedBriefWords in wails-gui/trial_seed_test.go.
// The seeded words say "today"; read a day later they say "yesterday" (the
// app renders day words for the reader's day), so match the day-free lead.
const SEEDED_BRIEF_WORDS = /Daily test brief: the Halcyon review is the first thing/;

test("A built brief a later pass stripped still shows its words", async () => {
    // The seed saved yesterday's evening brief again without its words and
    // recording, as the live refresh did on 2026-11-15. Once built, a brief
    // always shows with its words (mobile_brief_show_built.go). Today leads
    // with the newest brief, which by now may be today's scheduled one (it is
    // made ahead at launch), so the seeded brief is then opened through its
    // "Read it" link.
    const words = () => page.getByText(SEEDED_BRIEF_WORDS).filter({ visible: true });
    await visit("stripped-brief", 15_000, async () => {
        if (await words().first().isVisible().catch(() => false)) return;
        const readIt = page.getByRole("button", { name: "Read it" }).filter({ visible: true }).first();
        if (await readIt.isVisible().catch(() => false)) await readIt.click();
    }, words, async (r) => {
        r.checks.push("the built words show");
    });
});

test("The scheduled brief arrives with its recording", async () => {
    // The seed set the morning brief three minutes after launch: the app
    // writes, seals and records it on its own schedule. Wait for it on Today,
    // then check the recording the way the player reads it: the audio read
    // returns a file of the stated length, and an audio element reads that
    // length from it. Play itself is not counted here: in this test browser
    // a new audio element inside the app page stalls before it plays (hub,
    // 2026-11-15), and Bo's player is WebKit, which the app runner checks on
    // his computer after a merge. The click's outcome is noted, not failed.
    // The seeded earlier brief must give way: the newest brief wins.
    await visit("brief-audio", 420_000, async () => {}, () => page.locator("main").filter({ hasNot: page.getByText(SEEDED_BRIEF_WORDS) }).getByTestId("brief-audio-toggle").filter({ visible: true }), async (r) => {
        r.checks.push("scheduled brief arrived with a recording");
        const call = async (method: string, ...args: unknown[]) => {
            const res = await page.request.post("/api/call", { headers: { "X-Yansu-Bridge-Token": bridgeToken }, data: { method, args } });
            const body = await res.json();
            if (!body.ok) throw new Error(`${method}: ${body.error ?? res.status()}`);
            return body.data;
        };
        const health = await call("GetMobileBriefHealth");
        const audio = await call("GetMobileBriefAudio", health.currentBriefId);
        const bytes = Buffer.from(audio.base64 ?? "", "base64").length;
        if (!/^audio\//.test(audio.mime) || bytes < 1000 || !(audio.durationSeconds > 1)) throw new Error(`the audio read returned ${audio.mime} ${bytes} bytes ${audio.durationSeconds}s`);
        r.checks.push(`the audio read returned ${Math.round(bytes / 1024)} KB, ${Math.round(audio.durationSeconds)} s`);
        const read = await page.evaluate(({ mime, base64 }) => new Promise<number>((resolve) => {
            const element = document.createElement("audio");
            element.preload = "metadata";
            element.addEventListener("loadedmetadata", () => { resolve(element.duration); element.removeAttribute("src"); element.load(); }, { once: true });
            element.addEventListener("error", () => resolve(-1), { once: true });
            element.src = `data:${mime};base64,${base64}`;
            setTimeout(() => resolve(-2), 15_000);
        }), audio);
        if (!(Math.abs(read - audio.durationSeconds) < 1.5)) throw new Error(`an audio element read ${read}s, the brief says ${audio.durationSeconds}s`);
        r.checks.push("an audio element reads the same length");
        await page.getByTestId("brief-audio-toggle").filter({ visible: true }).first().click();
        const playing = await page.locator('[data-testid="brief-audio-toggle"][aria-pressed="true"]').filter({ visible: true }).first()
            .waitFor({ state: "visible", timeout: 10_000 }).then(() => true, () => false);
        r.checks.push(playing ? "Play started in the test browser" : "note: Play did not start in the test browser (known stall, not counted)");
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

// Tasks is moving: its work goes to Your work on Today and to Agents
// (computer-map v1.0.0). The check opens Agents when the sidebar has it,
// else Tasks, and waits for the page's heading rather than the task field.
test("Agents", async () => {
    const sidebar = page.getByRole("navigation");
    const agents = sidebar.getByRole("button", { name: rowName("Agents") });
    const label = await agents.count() > 0 ? "Agents" : "Tasks";
    await visit("agents", 1500, nav(label), () => page.locator("main").getByRole("heading").first());
});

test("Activity", async () => {
    await visit("activity", 2000, nav("Activity"), () => page.locator("main").getByRole("heading").first());
});

// Activity's one timeline in a narrow and a wide window
// (computer-activity v1.0.0); page-layout.spec.ts checks the same rules on
// fixture data.
for (const width of PAGE_LAYOUT_WIDTHS) {
    test(`Activity at ${width} px`, async () => {
        try {
            await visit(`activity-${width}`, 2000, async () => {
                await page.setViewportSize({ width, height: 900 });
                await page.locator('[data-activity-section="yourDay"]').click();
            }, () => page.getByTestId("activity-feed"), async (result) => {
                await page.waitForTimeout(800);
                result.checks.push("no sideways overflow", "sections never overlap", "rows never overlap");
                result.failures.push(...await pageLayoutFailures(page, '[data-testid="activity-feed"]', "[data-feed-kind]", false));
            });
        } finally {
            await page.setViewportSize({ width: 1280, height: 820 });
        }
    });
}

test("Activity, from your watch", async () => {
    await visit("watch-recordings", 2000, async () => {
        await page.locator('[data-activity-section="watch"]').click();
    }, () => page.getByTestId("watch-recordings"), async (r) => {
        await expect(page.getByTestId("watch-recordings").getByText(/watch/i).first()).toBeVisible();
        r.checks.push("seeded watch recording listed");
    });
});

// You is one sidebar row with tabs (computer-you v1.0.0); each tab is the
// page that was there.
test("You", async () => {
    await visit("about-you", 1500, nav("You"), () => page.getByTestId("you-page"));
});

for (const [name, tab] of [["how-you-do-things", "howYouDo"], ["who-you-know", "people"]] as const) {
    test(`You, ${tab}`, async () => {
        await visit(name, 1500, async () => {
            await page.locator(`[data-you-tab="${tab}"]`).first().click();
        }, () => page.locator(`[data-you-tab="${tab}"][aria-selected="true"]`));
    });
}

// About you's claims in a narrow and a wide window; page-layout.spec.ts
// checks the same rules on fixture claims.
for (const width of PAGE_LAYOUT_WIDTHS) {
    test(`You at ${width} px`, async () => {
        try {
            await visit(`you-${width}`, 1500, async () => {
                await page.setViewportSize({ width, height: 900 });
                await page.locator('[data-you-tab="about"]').first().click();
            }, () => page.locator('[data-you-tab="about"][aria-selected="true"]'), async (result) => {
                await page.waitForTimeout(800);
                result.checks.push("no sideways overflow", "sections never overlap", "claims never overlap");
                result.failures.push(...await pageLayoutFailures(page, '[data-testid="you-page"]', "[data-claim-row]", false));
            });
        } finally {
            await page.setViewportSize({ width: 1280, height: 820 });
        }
    });
}

test("Settings", async () => {
    await visit("settings", 1500, async () => {
        await page.keyboard.press(process.platform === "darwin" ? "Meta+Comma" : "Control+Comma");
    }, () => page.getByRole("heading", { name: /settings/i }));
});

// Keyboard only (pass-access v1.0.0, proof 1 and rules 1 to 3): every page
// opens from its key, focus is always shown with the 2 px ring, and Esc
// closes Search with focus back where it was. Recorded as one line,
// "keyboard", with no screenshot.
test("Keyboard only", async () => {
    const result: PageResult = { page: "keyboard", ok: true, paintMs: null, budgetMs: 0, checks: [], failures: [], screenshot: "skipped" };
    const mod = process.platform === "darwin" ? "Meta" : "Control";
    const errorsBefore = pageErrors.length;
    const step = async (name: string, run: () => Promise<void>) => {
        try { await run(); result.checks.push(name); } catch (error) { result.failures.push(`${name}: ${(error as Error).message.split("\n")[0]}`); }
    };
    await page.goto("/");
    await page.locator("main h1").filter({ visible: true }).first().waitFor({ timeout: 30_000 });
    const current = page.getByRole("navigation").locator('[aria-current="page"]');
    const agentsLabel = await page.getByRole("navigation").getByRole("button", { name: rowName("Agents") }).count() > 0 ? "Agents" : "Tasks";
    for (const [key, label] of [["2", "Chat"], ["3", agentsLabel], ["4", "Activity"], ["5", "You"], ["1", "Today"]] as const) {
        await step(`${mod}+${key} opens ${label}`, async () => {
            await page.keyboard.press(`${mod}+${key}`);
            await expect(current.filter({ hasText: label }).first()).toBeVisible({ timeout: 5_000 });
        });
    }
    await step("Tab shows a 2 px focus ring", async () => {
        await page.locator("body").click({ position: { x: 1, y: 1 } });
        for (let i = 0; i < 3; i++) await page.keyboard.press("Tab");
        const ring = await page.evaluate(() => {
            const el = document.activeElement as HTMLElement | null;
            if (!el || el === document.body) return "nothing focused";
            const style = getComputedStyle(el);
            return style.outlineStyle !== "none" && parseFloat(style.outlineWidth) >= 2 ? "ok" : `${el.tagName} outline ${style.outlineStyle} ${style.outlineWidth}`;
        });
        expect(ring).toBe("ok");
    });
    await step("Esc closes Search and gives focus back", async () => {
        const before = await page.evaluate(() => document.activeElement?.outerHTML.slice(0, 80) ?? "");
        await page.keyboard.press(`${mod}+k`);
        await expect(page.getByRole("dialog")).toBeVisible({ timeout: 5_000 });
        await page.keyboard.press("Escape");
        await expect(page.getByRole("dialog")).toHaveCount(0, { timeout: 5_000 });
        const after = await page.evaluate(() => document.activeElement?.outerHTML.slice(0, 80) ?? "");
        expect(after).toBe(before);
    });
    const newErrors = pageErrors.slice(errorsBefore);
    if (newErrors.length) result.failures.push(`${newErrors.length} page error(s): ${newErrors[0].slice(0, 160)}`);
    result.ok = result.failures.length === 0;
    record(result);
});
