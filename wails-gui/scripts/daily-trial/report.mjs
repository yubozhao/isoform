#!/usr/bin/env node
// report.mjs RUN DATE SHA BUILD_SECS STARTUP_SECS LOG_ERRORS: writes the daily
// real-app test's Markdown report to stdout and RUN/summary.txt (first line
// "PASS ..." or "FAIL ...") for the morning report.
import { readFileSync, writeFileSync, existsSync } from "node:fs";

const [run, date, sha, buildSecs, startupSecs, logErrors, branch = "feat/iphone-companion"] = process.argv.slice(2);
const read = (name) => (existsSync(`${run}/${name}`) ? readFileSync(`${run}/${name}`, "utf8") : "");
const pages = read("pages.jsonl").split("\n").filter(Boolean).map((line) => JSON.parse(line));
const paints = read("first-paint.txt").split("\n").filter(Boolean).map((line) => line.split(" "));
const refused = read("refused-methods.txt").split("\n").filter(Boolean);
const errors = read("log-errors.txt").split("\n").filter(Boolean);
const failed = pages.filter((p) => !p.ok);
const expected = 13;
const ok = pages.length >= expected && failed.length === 0 && Number(logErrors) === 0 && refused.length === 0;
const line = ok
    ? `PASS ${pages.length} pages on ${sha}, build ${buildSecs}s, start ${startupSecs}s`
    : `FAIL on ${sha}: ${[
        ...failed.map((p) => `${p.page} (${p.failures[0]})`),
        pages.length < expected ? `${expected - pages.length} page(s) not reached` : "",
        Number(logErrors) > 0 ? `${logErrors} log error(s)` : "",
        refused.length ? `bridge refused ${refused.join(", ")}` : "",
    ].filter(Boolean).join("; ")}`;
writeFileSync(`${run}/summary.txt`, line + "\n");

const rows = pages.map((p) => `| ${p.page} | ${p.ok ? "pass" : "FAIL"} | ${p.paintMs ?? "-"} / ${p.budgetMs} | ${p.screenshot} | ${[...p.checks, ...p.failures].join("; ") || ""} |`);
console.log(`# Daily real-app test ${date}

${line}

- Tip: \`${sha}\` (${branch}), built with build-wails.sh in ${buildSecs}s; the app answered in ${startupSecs}s.
- Data: a fresh folder seeded with a scripted day (TestTrialSeed). No one's real data.
- Screenshots and logs: \`${run}\`.

## Pages

| Page | Result | Paint ms / budget | Screenshot | Notes |
|---|---|---|---|---|
${rows.join("\n")}

## Brief reads

${read("brief-reads.txt").trim() ? "```\n" + read("brief-reads.txt").trim() + "\n```" : "No brief was read."}

## Calls the browser bridge refused

${refused.length ? refused.map((m) => `- ${m}`).join("\n") : "None."}

## First paint from the app's log

${paints.length ? paints.map(([page, ms]) => `- ${page}: ${ms} ms`).join("\n") : "- none logged"}

## Log errors (${errors.length})

${errors.length ? errors.slice(0, 20).map((e) => "- `" + e.slice(0, 200).replace(/`/g, "'") + "`").join("\n") : "- none"}

## How quick chat and the phone are checked

- quick-chat: one prompt through the quick chat helper's own route to the launched app (the panel window itself is not drawn).
- phone-relay: TestMobileEndToEndThroughRelayBinary, the relay binary with the hub code and a scripted phone, not the launched app.
`);
