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
    const steady = {}, launch = {}, paints = {};
    for (const line of lines) {
        if (line.includes("[perf] launch stage=logging_ready")) {
            const at = stampMs(line);
            if (!Number.isNaN(at)) launches.push(at);
            continue;
        }
        const paint = line.match(/\[perf\] first_paint page=([\w-]+) ms=(\d+)/);
        if (paint) {
            (paints[paint[1]] ??= []).push(Number(paint[2]));
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
    // First paint of each budgeted page (Bo, 2026-11-15: under 1.5 s). A paint
    // longer than the launch window was a window opened hidden (a login item):
    // it measures how long nobody looked, not the page.
    const paintBudget = budgets.firstPaintMs ?? budgets.default.launchMaxMs;
    const firstPaints = [];
    for (const [page, all] of Object.entries(paints)) {
        if (budgets.pages && !budgets.pages.includes(page)) continue;
        const looked = all.filter((ms) => ms < budgets.launchWindowMs);
        if (!looked.length) continue;
        const max = Math.max(...looked);
        const ok = max <= paintBudget;
        firstPaints.push({ page, n: looked.length, max, budget: paintBudget, ok });
        if (!ok) failures.push(`${page} first paint ${max} ms > ${paintBudget}`);
    }
    return { rows, failures, firstPaints };
}

export function todayBudgetTable({ rows, firstPaints = [] }) {
    const paintTable = firstPaints.length
        ? "\n\n| Page | First paints | Slowest / budget ms | Result |\n|---|---|---|---|\n" + firstPaints.map((p) => `| ${p.page} | ${p.n} | ${p.max} / ${p.budget} | ${p.ok ? "pass" : "OVER"} |`).join("\n")
        : "";
    if (!rows.length) return "No page reads logged." + paintTable;
    const cell = (v) => (v === null ? "-" : v);
    return "| Page | Binding | Result | Steady reads | Steady p95 / budget ms | Launch reads | Launch max / budget ms |\n|---|---|---|---|---|---|---|\n"
        + rows.map((r) => `| ${r.page} | ${r.name} | ${r.ok ? "pass" : "OVER"} | ${r.steadyN} | ${cell(r.steadyP95)} / ${r.steadyBudget} | ${r.launchN} | ${cell(r.launchMax)} / ${r.launchBudget} |`).join("\n") + paintTable;
}
