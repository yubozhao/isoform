// today-budgets.mjs: checks the window reads of Today, Chat, Activity and You in an app log against
// today-budgets.json. Reads only timestamps, binding names, timings and
// origins out of a line; never its text. Used by replay.mjs and report.mjs.
import { readFileSync } from "node:fs";

const config = JSON.parse(readFileSync(new URL("./today-budgets.json", import.meta.url), "utf8"));

const stampMs = (line) => {
    const m = line.match(/^(\d{4})\/(\d\d)\/(\d\d) (\d\d):(\d\d):(\d\d)\.(\d{3})/);
    return m ? Date.UTC(+m[1], +m[2] - 1, +m[3], +m[4], +m[5], +m[6], +m[7]) : NaN;
};

/** checkTodayBudgets(lines) -> { rows, failures }: one row per budgeted
 * binding that was read, and a sentence per budget exceeded. */
export function checkTodayBudgets(lines, budgets = config) {
    const launches = [];
    const steady = {}, launch = {};
    for (const line of lines) {
        if (line.includes("[perf] launch stage=logging_ready")) {
            const at = stampMs(line);
            if (!Number.isNaN(at)) launches.push(at);
            continue;
        }
        const m = line.match(/\[perf\] binding=(\w+) ms=(\d+)(?: rows=\S+)? origin=window\b/);
        if (!m || !(m[1] in budgets.bindings)) continue;
        const at = stampMs(line);
        const since = launches.length ? at - launches[launches.length - 1] : Infinity;
        ((since < budgets.launchWindowMs ? launch : steady)[m[1]] ??= []).push(Number(m[2]));
    }
    const rows = [], failures = [];
    for (const name of Object.keys(budgets.bindings)) {
        const budget = { ...budgets.default, ...budgets.bindings[name] };
        const s = [...(steady[name] ?? [])].sort((a, b) => a - b);
        const l = launch[name] ?? [];
        if (!s.length && !l.length) continue;
        const p95 = s.length >= budgets.minSamples ? s[Math.min(s.length - 1, Math.floor(0.95 * s.length))] : s.length ? s[s.length - 1] : null;
        const launchMax = l.length ? Math.max(...l) : null;
        const row = { name, page: budget.page ?? "today", steadyN: s.length, steadyP95: p95, steadyBudget: budget.steadyP95Ms, launchN: l.length, launchMax, launchBudget: budget.launchMaxMs, ok: true };
        if (p95 !== null && p95 > budget.steadyP95Ms) {
            row.ok = false;
            failures.push(`${name} steady p95 ${p95} ms > ${budget.steadyP95Ms}`);
        }
        if (launchMax !== null && launchMax > budget.launchMaxMs) {
            row.ok = false;
            failures.push(`${name} at launch ${launchMax} ms > ${budget.launchMaxMs}`);
        }
        rows.push(row);
    }
    return { rows, failures };
}

export function todayBudgetTable({ rows }) {
    if (!rows.length) return "No page reads logged.";
    const cell = (v) => (v === null ? "-" : v);
    return "| Page | Binding | Result | Steady reads | Steady p95 / budget ms | Launch reads | Launch max / budget ms |\n|---|---|---|---|---|---|---|\n"
        + rows.map((r) => `| ${r.page} | ${r.name} | ${r.ok ? "pass" : "OVER"} | ${r.steadyN} | ${cell(r.steadyP95)} / ${r.steadyBudget} | ${r.launchN} | ${cell(r.launchMax)} / ${r.launchBudget} |`).join("\n");
}
