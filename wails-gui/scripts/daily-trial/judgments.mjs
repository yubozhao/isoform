// judgments.mjs: the thesis's daily measure for the morning replay
// (docs/rework-2026-11.md): the person's judgments per day and source, read
// from the answers ledger (answers/ledger.jsonl). It mirrors the app's
// judgmentStatsAt (wails-gui/answers_ledger.go): an undo takes its entry out
// of Answered and counts it as Undone, Yes and Edit are accepted, and a
// brief's items acted on are the brief source's accepted. Only each entry's
// id, action, source, time and the entry an undo names are read; titles,
// reasons, notes and edits are never touched.
import { existsSync, readFileSync } from "node:fs";

const zero = () => ({ shown: 0, answered: 0, accepted: 0, declined: 0, notMine: 0, useful: 0, notUseful: 0, undone: 0 });
const localDay = (date) => `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, "0")}-${String(date.getDate()).padStart(2, "0")}`;

export function readLedger(path) {
    if (!path || !existsSync(path)) return null;
    const rows = [];
    for (const line of readFileSync(path, "utf8").split("\n")) {
        if (!line.trim()) continue;
        try {
            const { id, action, source, at, undoes } = JSON.parse(line);
            rows.push({ id, action, source, at, undoes });
        } catch { /* a torn last line */ }
    }
    return rows;
}

// judgmentDays counts the days local days ending on lastDay (YYYY-MM-DD),
// newest first.
export function judgmentDays(rows, lastDay, days = 7) {
    const undone = new Set(rows.filter((r) => r.action === "undo" && r.undoes).map((r) => r.undoes));
    const [y, m, d] = lastDay.split("-").map(Number);
    const order = Array.from({ length: days }, (_, i) => localDay(new Date(y, m - 1, d - i)));
    const byDay = Object.fromEntries(order.map((day) => [day, { day, total: zero(), bySource: {} }]));
    for (const r of rows) {
        if (r.action === "undo") continue;
        const at = new Date(r.at);
        if (Number.isNaN(at.getTime())) continue;
        const day = byDay[localDay(at)];
        if (!day) continue;
        const source = r.source || "other";
        day.bySource[source] ??= zero();
        for (const c of [day.bySource[source], day.total]) count(c, r, undone.has(r.id));
    }
    return order.map((day) => byDay[day]);
}

function count(c, r, isUndone) {
    if (r.action === "shown") { c.shown++; return; }
    if (isUndone) { c.undone++; return; }
    c.answered++;
    switch (r.action) {
    case "yes": case "edit": c.accepted++; break;
    case "no": c.declined++; break;
    case "not_mine": c.notMine++; break;
    case "useful": c.useful++; break;
    case "not_useful": c.notUseful++; break;
    }
}

const cols = ["shown", "answered", "accepted", "declined", "notMine", "useful", "notUseful", "undone"];
const head = "| Source | Shown | Answered | Accepted | Declined | Not mine | Useful | Not useful | Undone |\n|---|---|---|---|---|---|---|---|---|";
const row = (name, c) => `| ${name} | ${cols.map((k) => c[k]).join(" | ")} |`;

export function judgmentsSection(rows, day) {
    if (rows === null) return "- no answers ledger yet";
    const week = judgmentDays(rows, day, 7);
    const [yesterday] = week;
    const sources = Object.keys(yesterday.bySource).sort();
    const rate = (c) => (c.shown ? `${Math.round((c.answered / c.shown) * 100)}%` : "-");
    return [
        `Brief items acted on: ${yesterday.bySource.brief?.accepted ?? 0}. Answered of shown: ${rate(yesterday.total)}.`,
        "",
        head,
        ...sources.map((s) => row(s, yesterday.bySource[s])),
        row("**all**", yesterday.total),
        "",
        "Last 7 days, all sources (newest first):",
        "",
        "| Day | Shown | Answered | Accepted | Brief acted on | Answered of shown |\n|---|---|---|---|---|---|",
        ...week.map((d) => `| ${d.day} | ${d.total.shown} | ${d.total.answered} | ${d.total.accepted} | ${d.bySource.brief?.accepted ?? 0} | ${rate(d.total)} |`),
    ].join("\n");
}
