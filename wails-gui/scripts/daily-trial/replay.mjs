#!/usr/bin/env node
// replay.mjs DAY OUT BRIEFS LOG...: reduces one day of the app's log and its
// stored briefs to numbers and tags, saves them as OUT/DAY.json, compares them
// with the previous saved day and writes OUT/DAY.md and OUT/latest.txt.
// Only counts, timings and log tags are read out of a line; never its text.
import { readFileSync, writeFileSync, existsSync, readdirSync } from "node:fs";

const [day, out, briefsFile, ...logs] = process.argv.slice(2);
const stamp = day.replaceAll("-", "/");
const paints = {}, bindings = {}, errors = {}, turns = {};
let lines = 0;
const push = (map, key, value) => { (map[key] ??= []).push(value); };
for (const file of logs) {
    for (const line of readFileSync(file, "utf8").split("\n")) {
        if (!line.startsWith(stamp)) continue;
        lines++;
        let m;
        if ((m = line.match(/\[perf\] first_paint page=([\w-]+) ms=(\d+)/))) push(paints, m[1], Number(m[2]));
        else if ((m = line.match(/\[perf\] binding=(\w+) ms=(\d+)/))) push(bindings, m[1], Number(m[2]));
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
    errors, turns, briefs,
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
const line = !prev ? `${day} baseline saved (${lines} log lines, ${briefs.length} briefs); nothing to compare yet`
    : worse.length ? `${day} WORSE than ${previousDay.slice(0, 10)}: ${worse.slice(0, 4).join("; ")}`
        : `${day} no worse than ${previousDay.slice(0, 10)} (${lines} log lines, ${briefs.length} briefs)`;
writeFileSync(`${out}/latest.txt`, line + "\n");

const table = (map, label) => {
    const rows = Object.entries(map).sort((a, b) => b[1].p95 - a[1].p95).slice(0, 15)
        .map(([k, s]) => `| ${k} | ${s.n} | ${s.p50} | ${s.p95} | ${prev?.[label]?.[k] ? prev[label][k].p95 : "-"} |`);
    return rows.length ? `| Name | Count | p50 ms | p95 ms | p95 day before |\n|---|---|---|---|---|\n${rows.join("\n")}` : "None logged.";
};
writeFileSync(`${out}/${day}.md`, `# Replay of ${day} (read-only, counts only)

${line}

## Briefs
${briefs.length ? briefs.map((b) => `- ${b.id}: ${b.decision}, ${b.narrative} paragraphs, ${b.cards} cards, ${b.evidence} sources, audio ${b.audio ? "yes" : "no"}`).join("\n") : "- none stored for this day"}

## First paint by page
${table(metrics.paints, "paints")}

## Slowest bindings
${table(metrics.bindings, "bindings")}

## Errors by log tag
${Object.keys(errors).length ? Object.entries(errors).sort((a, b) => b[1] - a[1]).map(([t, n]) => `- ${t}: ${n}${prev ? ` (day before ${prev.errors[t] ?? 0})` : ""}`).join("\n") : "- none"}

## Chat and Task turns
${Object.keys(turns).length ? Object.entries(turns).map(([k, n]) => `- ${k}: ${n}`).join("\n") : "- none logged"}
`);
console.log(line);
