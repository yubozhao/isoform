// node --test judgments.test.mjs
import { test } from "node:test";
import assert from "node:assert/strict";
import { mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { judgmentDays, judgmentsSection, readLedger } from "./judgments.mjs";

const at = (day, hour) => new Date(`${day}T${String(hour).padStart(2, "0")}:00:00`).toISOString();

test("counts like the app: undo moves an answer to undone, yes and edit accept, shown is not an answer", () => {
    const rows = [
        { id: "s1", action: "shown", source: "brief", at: at("2026-11-15", 8) },
        { id: "s2", action: "shown", source: "topic", at: at("2026-11-15", 8) },
        { id: "a1", action: "yes", source: "brief", at: at("2026-11-15", 9) },
        { id: "a2", action: "edit", source: "brief", at: at("2026-11-15", 9) },
        { id: "a3", action: "no", source: "topic", at: at("2026-11-15", 10) },
        { id: "a4", action: "not_mine", source: "topic", at: at("2026-11-15", 10) },
        { id: "a5", action: "useful", source: "agent", at: at("2026-11-15", 11) },
        { id: "a6", action: "yes", at: at("2026-11-15", 12) },
        { id: "u1", action: "undo", undoes: "a6", at: at("2026-11-15", 12) },
        { id: "old", action: "yes", source: "brief", at: at("2026-11-01", 9) },
    ];
    const [day, before] = judgmentDays(rows, "2026-11-15", 7);
    assert.equal(day.day, "2026-11-15");
    assert.deepEqual(day.total, { shown: 2, answered: 5, accepted: 2, declined: 1, notMine: 1, useful: 1, notUseful: 0, undone: 1 });
    assert.equal(day.bySource.brief.accepted, 2);
    assert.equal(day.bySource.other.undone, 1);
    assert.equal(before.day, "2026-11-14");
    assert.equal(before.total.answered, 0);
});

test("the section reads only counts and says when there is no ledger", () => {
    const dir = mkdtempSync(join(tmpdir(), "judgments-"));
    const path = join(dir, "ledger.jsonl");
    writeFileSync(path, [
        JSON.stringify({ id: "s1", action: "shown", source: "brief", at: at("2026-11-15", 8), title: "Private title" }),
        JSON.stringify({ id: "a1", action: "yes", source: "brief", at: at("2026-11-15", 9), title: "Private title", reason: "private reason" }),
        "{torn",
    ].join("\n"));
    const text = judgmentsSection(readLedger(path), "2026-11-15");
    assert.match(text, /Brief items acted on: 1\. Answered of shown: 100%\./);
    assert.doesNotMatch(text, /Private|private/);
    assert.equal(judgmentsSection(readLedger(join(dir, "missing.jsonl")), "2026-11-15"), "- no answers ledger yet");
});
