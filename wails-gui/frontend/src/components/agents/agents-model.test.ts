import { describe, expect, it } from "vitest";
import type { ConnectedAgent, TaskLinkResult, WorkOnItHandoff, YourAgents } from "@/lib/wails-shim-app";
import { agentName, agentRows, handoffTitle, madeThisWeek, todayTimeline } from "./agents-model";

const now = new Date(2026, 10, 15, 18, 45);
const at = (h: number, m: number, dayOffset = 0) => new Date(2026, 10, 15 + dayOffset, h, m).getTime();

const agents: YourAgents = {
    choices: { code: "codex", else: "claude" },
    agents: [
        { id: "codex", name: "Codex", kind: "code", reach: "on_computer", found: true, connected: false },
        { id: "claude-code", name: "Claude Code", kind: "code", reach: "connected_computer", found: true, connected: true },
        { id: "cursor", name: "Cursor", kind: "code", reach: "on_computer", found: false, connected: false },
        { id: "claude", name: "Claude", kind: "else", reach: "its_app", found: true, connected: false },
        { id: "grok", name: "Grok", kind: "else", reach: "its_app", found: false, connected: false },
        { id: "ag_x", name: "Ana's Helper", kind: "else", reach: "connected", found: false, connected: true },
    ],
};
const connected: ConnectedAgent[] = [
    { id: "ag_cc", name: "Claude Code", device: "this Mac", mayOpen: "the repo", joinedAt: 1, catalogId: "claude-code" },
    { id: "ag_x", name: "Ana's Helper", device: "the cloud", mayOpen: "the web", joinedAt: 2 },
];
const handoff = (id: string, agentId: string, text: string, sentAt: number): WorkOnItHandoff => ({ id, sourceKind: "brief_item", kind: "else", agentId, method: "connected", text, sentAt, surface: "computer" });
const result = (linkId: string, agentId: string, resultAt: number, files?: string[]): TaskLinkResult => ({ linkId, agentId, status: "done", summary: "Done.", resultAt, files });

describe("agents model", () => {
    it("lists the chosen, the connected and coding agents found here; nothing else", () => {
        const rows = agentRows(agents, connected, [], []);
        expect(rows.map((r) => r.id)).toEqual(["codex", "claude", "claude-code", "ag_x"]);
        expect(rows.map((r) => r.job)).toEqual(["forCodeHere", "forElse", "connected", "connected"]);
        expect(rows.find((r) => r.id === "claude-code")?.connectionId).toBe("ag_cc");
    });

    it("says an agent is working on its newest hand-off with no result", () => {
        const handoffs = [handoff("h1", "codex", "Write the test plan\n\nDone looks like: 14 cases", at(18, 20)), handoff("h2", "claude", "Q4 plan outline", at(16, 10))];
        const rows = agentRows(agents, connected, handoffs, [result("h2", "claude", at(17, 0))]);
        expect(rows.find((r) => r.id === "codex")?.working).toBe("Write the test plan");
        expect(rows.find((r) => r.id === "claude")?.working).toBeUndefined();
    });

    it("builds today's timeline newest first, and narrows it to one agent", () => {
        const handoffs = [handoff("h1", "codex", "Write the test plan", at(18, 20)), handoff("old", "codex", "Yesterday", at(18, 20, -1)), handoff("h3", "ag_cc", "Fix the build", at(17, 0))];
        const results = [result("h1", "codex", at(18, 40), ["test-plan.md"])];
        const all = todayTimeline(handoffs, results, now);
        expect(all.map((e) => e.key)).toEqual(["r-h1", "h-h1", "h-h3"]);
        expect(all[1]).toMatchObject({ type: "handoff", answered: true, title: "Write the test plan" });
        const claudeCode = agentRows(agents, connected, [], []).find((r) => r.id === "claude-code")!;
        expect(todayTimeline(handoffs, results, now, claudeCode).map((e) => e.key)).toEqual(["h-h3"]);
    });

    it("counts a hand-off back when its task here ended", () => {
        const handoffs = [{ ...handoff("h9", "codex", "Fix the build", at(17, 0)), taskId: "hf_1", taskState: "done" as const, taskResult: "Fixed." }];
        expect(agentRows(agents, connected, handoffs, []).find((r) => r.id === "codex")?.working).toBeUndefined();
        expect(todayTimeline(handoffs, [], now)[0]).toMatchObject({ answered: true, taskState: "done", taskResult: "Fixed." });
    });

    it("lists this week's files from every agent", () => {
        const files = madeThisWeek([result("a", "codex", at(10, 0), ["one.md", "two.md"]), result("b", "claude", at(10, 0, -9), ["old.md"]), result("c", "claude", at(12, 0))], now);
        expect(files.map((f) => f.name)).toEqual(["one.md", "two.md"]);
    });

    it("names agents by catalog or joined id", () => {
        expect(agentName("codex", agents, connected)).toBe("Codex");
        expect(agentName("ag_cc", agents, connected)).toBe("Claude Code");
        expect(handoffTitle("\n  First line  \nSecond")).toBe("First line");
    });
});
