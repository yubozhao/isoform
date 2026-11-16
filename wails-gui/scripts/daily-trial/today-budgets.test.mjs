// node --test today-budgets.test.mjs
import { test } from "node:test";
import assert from "node:assert/strict";
import { checkTodayBudgets } from "./today-budgets.mjs";

const at = (s) => `2026/11/15 10:${s}.000000`;
const budgets = { launchWindowMs: 90000, minSamples: 5, default: { steadyP95Ms: 100, launchMaxMs: 1500 }, bindings: { GetTodayDayShape: {}, GetLatestBrief: { steadyP95Ms: 300 } } };

test("a slow launch read and a slow steady p95 both fail; internal reads and other bindings are ignored", () => {
    const lines = [
        `${at("00:00")} [perf] launch stage=logging_ready ms_since_start=6`,
        `${at("00:12")} [perf] binding=GetTodayDayShape ms=12948 rows=1 origin=window`,
        `${at("00:13")} [perf] binding=GetTodayDayShape ms=9000 rows=1 origin=internal`,
        ...Array.from({ length: 10 }, (_, i) => `${at(`0${2 + Math.floor(i / 6)}:${String(10 + i).padStart(2, "0")}`)} [perf] binding=GetLatestBrief ms=${i < 9 ? 50 : 900} rows=1 origin=window`),
        `${at("03:00")} [perf] binding=GetDayNoticed ms=99999 rows=1 origin=window`,
    ];
    const { rows, failures } = checkTodayBudgets(lines, budgets);
    assert.deepEqual(failures, ["GetTodayDayShape at launch 12948 ms > 1500", "GetLatestBrief steady p95 900 ms > 300"]);
    assert.equal(rows.length, 2);
});

test("reads inside their budgets pass", () => {
    const lines = [
        `${at("00:00")} [perf] launch stage=logging_ready ms_since_start=6`,
        `${at("00:01")} [perf] binding=GetTodayDayShape ms=400 rows=1 origin=window`,
        `${at("05:00")} [perf] binding=GetTodayDayShape ms=0 rows=1 origin=window`,
    ];
    assert.deepEqual(checkTodayBudgets(lines, budgets).failures, []);
});

test("every budgeted binding names its page, and the table shows it", async () => {
    const { readFileSync } = await import("node:fs");
    const config = JSON.parse(readFileSync(new URL("./today-budgets.json", import.meta.url), "utf8"));
    const pages = new Set(Object.values(config.bindings).map((b) => b.page));
    assert.deepEqual([...pages].sort(), ["activity", "chat", "today", "you"]);
    const { todayBudgetTable } = await import("./today-budgets.mjs");
    const lines = [
        `${at("00:00")} [perf] launch stage=logging_ready ms_since_start=6`,
        `${at("00:05")} [perf] binding=GetDayThreads ms=2000 rows=13 origin=window`,
    ];
    const result = checkTodayBudgets(lines);
    assert.deepEqual(result.failures, ["GetDayThreads at launch 2000 ms > 1500"]);
    assert.match(todayBudgetTable(result), /\| activity \| GetDayThreads \| OVER \|/);
});

test("a slow first paint of a budgeted page fails; a hidden-window paint and other pages are ignored", () => {
    const lines = [
        `${at("00:00")} [perf] launch stage=logging_ready ms_since_start=6`,
        `${at("00:01")} [perf] first_paint page=today ms=537 source=network`,
        `${at("00:10")} [perf] first_paint page=people ms=1848 source=network`,
        `${at("00:11")} [perf] first_paint page=today ms=549772 source=network`,
        `${at("00:12")} [perf] first_paint page=settings ms=9000 source=network`,
    ];
    const result = checkTodayBudgets(lines);
    assert.deepEqual(result.failures, ["people first paint 1848 ms > 1500"]);
    assert.deepEqual(result.firstPaints.map((p) => [p.page, p.max, p.ok]), [["today", 537, true], ["people", 1848, false]]);
});
