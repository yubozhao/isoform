#!/usr/bin/env node
// replay.mjs DAY OUT BRIEFS LOG...: reduces one day of the app's log and its
// stored briefs to numbers and tags, saves them as OUT/DAY.json, compares them
// with the previous saved day and writes OUT/DAY.md and OUT/latest.txt.
// Only counts, timings and log tags are read out of a line; never its text.
import { readFileSync, writeFileSync, existsSync, readdirSync } from "node:fs";
import { checkTodayBudgets, todayBudgetTable } from "./today-budgets.mjs";

const [day, out, briefsFile, ...logs] = process.argv.slice(2);
const stamp = day.replaceAll("-", "/");
const paints = {}, bindings = {}, errors = {}, turns = {};
// One [brief-tokens-shadow] line per brief written with tokens while the old
// writer runs beside it (docs/brief-writer-tokens.md): counts only.
const shadows = [];
let lines = 0;
// A paint that started before the screen was unlocked waited for the unlock
// (the app reads nothing while the Mac is locked); it is counted apart, not
// as a slow paint. The lock state comes from [listening-privacy] lines.
let locked = false, lastUnlock = 0, waitedForUnlock = 0;
const seconds = (line) => {
    const m = line.match(/^\S+ (\d+):(\d+):(\d+(?:\.\d+)?)/);
    return m ? Number(m[1]) * 3600 + Number(m[2]) * 60 + Number(m[3]) : 0;
};
const perfLines = [];
const push = (map, key, value) => { (map[key] ??= []).push(value); };
for (const file of logs) {
    for (const line of readFileSync(file, "utf8").split("\n")) {
        if (!line.startsWith(stamp)) continue;
        lines++;
        if (line.includes("[perf] ")) perfLines.push(line);
        let m;
        if (/\[listening-privacy\] paused reason=lock/.test(line)) locked = true;
        if (/\[listening-privacy\] resumed reason=lock/.test(line)) { locked = false; lastUnlock = seconds(line); }
        if ((m = line.match(/\[perf\] first_paint page=([\w-]+) ms=(\d+)/))) {
            const ms = Number(m[2]);
            if (locked || seconds(line) - ms / 1000 < lastUnlock) waitedForUnlock++;
            else push(paints, m[1], ms);
        }
        else if ((m = line.match(/\[perf\] binding=(\w+) ms=(\d+)/))) push(bindings, m[1], Number(m[2]));
        if (line.includes("[brief-tokens-shadow] done=")) {
            const kv = Object.fromEntries([...line.matchAll(/(\w+)=(\d+)(?:\/(\d+))?/g)].map(([, k, a, b]) => [k, b === undefined ? Number(a) : [Number(a), Number(b)]]));
            shadows.push(kv);
        }
        if ((m = line.match(/\[chat-turn\] kind=(\w+) .*outcome=(\w+)/))) turns[`${m[1]}:${m[2]}`] = (turns[`${m[1]}:${m[2]}`] ?? 0) + 1;
        if (/panic|fatal|\berror\b|failed/i.test(line) && !/\[perf\]/.test(line)) {
            const tag = (line.match(/^\S+ \S+ (\[[^\]]+\])/) ?? [])[1] ?? "[untagged]";
            errors[tag] = (errors[tag] ?? 0) + 1;
        }
    }
}
const stat = (values) => {
    const s = [...values].sort((a, b) => a - b);
    const q = (p) => s[Math.min(s.length - 1, Math.floor(p * s.length))];
    return { n: s.length, p50: q(0.5), p95: q(0.95) };
};
const briefs = existsSync(briefsFile) ? readFileSync(briefsFile, "utf8").split("\n").filter(Boolean).map((line) => {
    const [id, decision] = line.split(" ");
    const kv = Object.fromEntries([...line.matchAll(/(\w+)=(\S+)/g)].map(([, k, v]) => [k, v]));
    return { id, decision, narrative: Number(kv.narrative ?? 0), cards: Number(kv.cards ?? 0), evidence: Number(kv.evidence ?? 0), audio: kv.audio === "true" };
}) : [];
const metrics = {
    day, logLines: lines,
    paints: Object.fromEntries(Object.entries(paints).map(([k, v]) => [k, stat(v)])),
    bindings: Object.fromEntries(Object.entries(bindings).map(([k, v]) => [k, stat(v)])),
    errors, turns, briefs, shadows, waitedForUnlock,
};
writeFileSync(`${out}/${day}.json`, JSON.stringify(metrics, null, 2));

// The previous saved day.
const previousDay = readdirSync(out).filter((f) => /^\d{4}-\d{2}-\d{2}\.json$/.test(f) && f < `${day}.json`).sort().pop();
const prev = previousDay ? JSON.parse(readFileSync(`${out}/${previousDay}`, "utf8")) : null;
const worse = [];
if (prev) {
    for (const [page, s] of Object.entries(metrics.paints)) {
        const p = prev.paints[page];
        if (p && s.p95 > p.p95 * 1.25 && s.p95 - p.p95 > 100) worse.push(`first paint ${page} p95 ${p.p95} -> ${s.p95} ms`);
    }
    for (const [name, s] of Object.entries(metrics.bindings)) {
        const p = prev.bindings[name];
        if (p && s.n >= 5 && s.p95 > p.p95 * 1.5 && s.p95 - p.p95 > 200) worse.push(`${name} p95 ${p.p95} -> ${s.p95} ms`);
    }
    for (const [tag, n] of Object.entries(metrics.errors)) {
        const p = prev.errors[tag] ?? 0;
        if (n > p * 2 && n - p >= 10) worse.push(`${tag} errors ${p} -> ${n}`);
    }
    const prevBriefs = prev.briefs.length, briefsNow = briefs.length;
    if (briefsNow < prevBriefs) worse.push(`briefs ${prevBriefs} -> ${briefsNow}`);
    const noAudio = briefs.filter((b) => !b.audio).length;
    if (noAudio > prev.briefs.filter((b) => !b.audio).length) worse.push(`${noAudio} brief(s) without audio`);
}
// Today's absolute budgets (today-budgets.json) fail the day on their own,
// whatever the day before was.
const budget = checkTodayBudgets(perfLines);
metrics.todayBudgetFailures = budget.failures;
writeFileSync(`${out}/${day}.json`, JSON.stringify(metrics, null, 2));
const overBudget = budget.failures.length ? `${day} OVER BUDGET on Today: ${budget.failures.slice(0, 4).join("; ")}${budget.failures.length > 4 ? `; ${budget.failures.length - 4} more` : ""}. ` : "";
const comparison = !prev ? `${day} baseline saved (${lines} log lines, ${briefs.length} briefs); nothing to compare yet`
    : worse.length ? `${day} WORSE than ${previousDay.slice(0, 10)}: ${worse.slice(0, 4).join("; ")}`
        : `${day} no worse than ${previousDay.slice(0, 10)} (${lines} log lines, ${briefs.length} briefs)`;
const line = overBudget + comparison;
const shadowNote = shadows.length ? `; tokens vs old writer: ${shadows.length} briefs, ${shadows.filter((r) => (r.items_only_old ?? 0) > 0).length} left out an old item, ${shadows.reduce((n, r) => n + (r.fallbacks ?? 0), 0)} fallbacks` : "";
writeFileSync(`${out}/latest.txt`, line + shadowNote + "\n");

// Side-by-side of the writer's tokens and the old writer, per brief: same
// paragraph counts, cited items shared or only on one side, templates kept,
// fallbacks, and the old writer's day repairs. Counts only.
function tokensShadow(rows) {
    if (!rows.length) return "- no side-by-side runs logged";
    const sum = (key) => rows.reduce((n, r) => n + (Array.isArray(r[key]) ? 0 : r[key] ?? 0), 0);
    const sameParts = rows.filter((r) => r.done?.[0] === r.done?.[1] && r.next?.[0] === r.next?.[1]).length;
    const lost = rows.filter((r) => (r.items_only_old ?? 0) > 0).length;
    return [
        `- briefs compared: ${rows.length}; same paragraph counts: ${sameParts}`,
        `- cited items shared: ${sum("items_shared")}, only with tokens: ${sum("items_only_tokens")}, only in the old writer: ${sum("items_only_old")} (briefs that left out an old item: ${lost})`,
        `- token paragraphs kept: ${sum("templates")}, fell back: ${sum("fallbacks")}; old writer day repairs: ${sum("old_day_repairs")}`,
    ].join("\n");
}

const table = (map, label) => {
    const rows = Object.entries(map).sort((a, b) => b[1].p95 - a[1].p95).slice(0, 15)
        .map(([k, s]) => `| ${k} | ${s.n} | ${s.p50} | ${s.p95} | ${prev?.[label]?.[k] ? prev[label][k].p95 : "-"} |`);
    return rows.length ? `| Name | Count | p50 ms | p95 ms | p95 day before |\n|---|---|---|---|---|\n${rows.join("\n")}` : "None logged.";
};
writeFileSync(`${out}/${day}.md`, `# Replay of ${day} (read-only, counts only)

${line}

## Briefs
${briefs.length ? briefs.map((b) => `- ${b.id}: ${b.decision}, ${b.narrative} paragraphs, ${b.cards} cards, ${b.evidence} sources, audio ${b.audio ? "yes" : "no"}`).join("\n") : "- none stored for this day"}

## Today read budgets (today-budgets.json)
${todayBudgetTable(budget)}

## First paint by page
${table(metrics.paints, "paints")}
${waitedForUnlock ? `\n${waitedForUnlock} paint(s) started while the Mac was locked and waited for the unlock; not counted above.` : ""}

## Slowest bindings
${table(metrics.bindings, "bindings")}

## Errors by log tag
${Object.keys(errors).length ? Object.entries(errors).sort((a, b) => b[1] - a[1]).map(([t, n]) => `- ${t}: ${n}${prev ? ` (day before ${prev.errors[t] ?? 0})` : ""}`).join("\n") : "- none"}

## Brief writer: tokens against the old writer
${tokensShadow(shadows)}

## Chat and Task turns
${Object.keys(turns).length ? Object.entries(turns).map(([k, n]) => `- ${k}: ${n}`).join("\n") : "- none logged"}
`);
console.log(line);
