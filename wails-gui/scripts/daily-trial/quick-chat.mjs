#!/usr/bin/env node
// quick-chat.mjs BUNDLE_PATH OUT: the daily real-app test's quick chat check.
// The quick chat panel is a separate helper the app starts at launch; it sends
// each prompt to the app's host bridge with its own IPC token. This check takes
// that token and port from the helper's arguments (same user, test machine
// only), sends one prompt the way the panel does, and waits for the answer.
// It appends one line to OUT/pages.jsonl in the spec's format.
import { execFileSync } from "node:child_process";
import { appendFileSync } from "node:fs";
import { randomUUID } from "node:crypto";

const [bundle, out] = process.argv.slice(2);
const result = { page: "quick-chat", ok: false, paintMs: null, budgetMs: 180000, checks: [], failures: [], screenshot: "skipped" };
const done = () => {
    result.ok = result.failures.length === 0;
    appendFileSync(`${out}/pages.jsonl`, JSON.stringify(result) + "\n");
    process.exit(0);
};

const helper = execFileSync("ps", ["-axww", "-o", "args="], { encoding: "utf8" })
    .split("\n")
    .find((line) => line.startsWith(bundle) && line.includes("--ipc-token="));
const token = /--ipc-token=(\S+)/.exec(helper ?? "")?.[1];
const port = /--yansu-port=(\d+)/.exec(helper ?? "")?.[1];
if (!token || !port) {
    result.failures.push("the quick chat helper is not running");
    done();
}
result.checks.push("helper running");

const base = `http://127.0.0.1:${port}/api/spotlight`;
const headers = { "Content-Type": "application/json", "X-Yansu-Spotlight-Token": token };
// The answer is a word the prompt does not contain, so finding it means the
// assistant replied.
const prompt = "Daily test: answer with the one word that is the opposite of no.";
const started = Date.now();
try {
    const res = await fetch(`${base}/submit`, { method: "POST", headers, body: JSON.stringify({ prompt, requestId: randomUUID() }) });
    const body = await res.json().catch(() => ({}));
    if (!res.ok || !body.sessionLocalId) throw new Error(`submit answered ${res.status}: ${body.error ?? "no session"}`);
    result.checks.push("prompt sent");
    const deadline = started + result.budgetMs;
    for (;;) {
        const snap = await fetch(`${base}/snapshot?sessionLocalId=${encodeURIComponent(body.sessionLocalId)}`, { headers });
        const text = await snap.text();
        if (snap.ok && /\byes\b/i.test(text)) break;
        if (Date.now() > deadline) throw new Error(`no answer within ${result.budgetMs / 1000}s`);
        await new Promise((r) => setTimeout(r, 2000));
    }
    result.paintMs = Date.now() - started;
    result.checks.push("answer arrived");
} catch (error) {
    result.failures.push(error.message);
}
done();
