import { describe, expect, it } from "vitest";
import type { ConnectedAgent, TaskLinkResult, WorkOnItHandoff, YourAgents } from "@/lib/wails-shim-app";
import { agentName, agentRows, assistantAgentId, whereOf, handoffTitle, handoffTimeline, learnedLines, madeThisWeek, rowForAgent, todayTimeline, weekCounts } from "./agents-model";

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

    it("gives every row one of the two states and where it runs", () => {
        const rows = agentRows(agents, connected, [], []);
        const by = (id: string) => rows.find((r) => r.id === id);
        expect(rows.map((r) => r.state)).toEqual(["notConnected", "notConnected", "connected", "connected"]);
        expect(by("codex")).toMatchObject({ where: "here", found: true });
        // Claude Code joined by link from "this Mac": it says the device it named.
        expect(by("claude-code")).toMatchObject({ where: "device", device: "this Mac", state: "connected" });
        expect(by("claude")).toMatchObject({ where: "itsApp" });
        expect(by("ag_x")).toMatchObject({ where: "device", device: "the cloud" });
    });

    it("says a joined cloud coding agent runs on its own device, not on this computer", () => {
        const view = { id: "ag_dv", name: "Devin", kind: "code" as const, reach: "connected_computer" as const, found: false, connected: true };
        const devin: ConnectedAgent = { id: "ag_dv", name: "Devin", device: "the cloud", mayOpen: "the repo", joinedAt: 3 };
        expect(whereOf(view, devin)).toEqual({ where: "device", device: "the cloud" });
        // A catalog coding agent joined from a cloud device runs there, not here.
        const codexCloud: ConnectedAgent = { id: "ag_cx", name: "Codex", device: "Codex Cloud", mayOpen: "the repo", joinedAt: 4, catalogId: "codex" };
        const codex = { ...view, id: "codex", name: "Codex" };
        expect(whereOf(codex, codexCloud)).toEqual({ where: "device", device: "Codex Cloud" });
        expect(agentRows({ choices: { code: "codex", else: "" }, agents: [codex] }, [codexCloud], [], []).find((r) => r.id === "codex")).toMatchObject({ where: "device", device: "Codex Cloud" });
        // "here" only when connected with one click here, or found here and not connected.
        expect(whereOf({ ...codex, connectedHere: true }, codexCloud)).toEqual({ where: "here" });
        expect(whereOf({ ...codex, connected: false, found: true, reach: "on_computer" })).toEqual({ where: "here" });
    });

    it("finds the coding agent that is also Isoform's own assistant", () => {
        expect(assistantAgentId("managed", ["claude", "codex"], "yansu")).toBe("claude-code");
        expect(assistantAgentId("managed", ["codex"], "")).toBe("codex");
        expect(assistantAgentId("external", [], "codex")).toBe("codex");
        expect(assistantAgentId("advanced", ["claude"], "anthropic")).toBe("");
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

    it("keeps disconnected agents as faded rows on the Agents page only, last", () => {
        const left: YourAgents = {
            choices: { code: "", else: "" },
            agents: [
                { id: "codex", name: "Codex", kind: "code", reach: "on_computer", found: false, connected: false, disconnectedAt: at(9, 0, -3) },
                { id: "claude-code", name: "Claude Code", kind: "code", reach: "connected_computer", found: true, connected: true, connectedAt: at(8, 0, -10) },
                { id: "ag_h", name: "Ana's Helper", kind: "else", reach: "disconnected", found: false, connected: false, disconnectedAt: at(9, 0, -1) },
                // The computer drops one after 30 days; with no time it is not disconnected.
                { id: "cursor", name: "Cursor", kind: "code", reach: "on_computer", found: false, connected: false },
            ],
        };
        const page = agentRows(left, [], [], [], { disconnected: true });
        expect(page.map((r) => [r.id, r.state])).toEqual([["claude-code", "connected"], ["codex", "disconnected"], ["ag_h", "disconnected"]]);
        expect(page.find((r) => r.id === "codex")?.disconnectedAt).toBe(at(9, 0, -3));
        expect(page.find((r) => r.id === "claude-code")?.connectedAt).toBe(at(8, 0, -10));
        // Settings lists only who it can reach.
        expect(agentRows(left, [], [], []).map((r) => r.id)).toEqual(["claude-code"]);
    });

    it("counts one agent's week: hand-offs, done results back, and Useful", () => {
        const rows = agentRows(agents, connected, [], []);
        const cc = rowForAgent(rows, "ag_cc")!;
        expect(cc.id).toBe("claude-code");
        const handoffs = [
            handoff("a", "claude-code", "A", at(9, 0)),
            handoff("b", "ag_cc", "B", at(9, 0, -6)),
            { ...handoff("c", "claude-code", "C", at(9, 0, -2)), taskState: "done" as const },
            handoff("old", "claude-code", "Old", at(9, 0, -7)),
            handoff("x", "codex", "X", at(9, 0)),
        ];
        const results = [result("a", "claude-code", at(10, 0)), { ...result("b", "ag_cc", at(10, 0, -6)), status: "blocked" as const }];
        expect(weekCounts(cc, handoffs, results, new Set(["a", "old", "x"]), now)).toEqual({ handoffs: 3, back: 2, useful: 1 });
    });

    it("narrows to one hand-off and its result, whatever day it was", () => {
        const handoffs = [handoff("a", "codex", "A", at(9, 0)), handoff("b", "codex", "B", at(9, 0, -9))];
        const entries = handoffTimeline(handoffs, [result("b", "codex", at(10, 0, -9)), result("a", "codex", at(10, 0))], "b");
        expect(entries.map((e) => e.key)).toEqual(["r-b", "h-b"]);
        expect(handoffTimeline(handoffs, [], "nope")).toEqual([]);
    });

    it("learned lines come only from agents with Learn from it on, naming each", () => {
        const base = { path: "", sessions14d: 0, lastAt: 0, status: "", openProblem: "", openProblemAt: 0, openQuestion: "", openChip: "" };
        const rows = agentRows({ choices: { code: "", else: "" }, agents: [
            { id: "codex", name: "Codex", kind: "code", reach: "connected_computer", found: true, connected: true, learnFromIt: true },
            { id: "claude-code", name: "Claude Code", kind: "code", reach: "connected_computer", found: true, connected: true, learnFromIt: true },
            { id: "cursor", name: "Cursor", kind: "code", reach: "connected_computer", found: true, connected: true, learnFromIt: true },
        ] }, [], [], []);
        const lines = learnedLines(rows, [
            { ...base, id: "a", name: "isoform", assistants: ["codex", "claude"], sessionsThisWeek: 5 },
            { ...base, id: "b", name: "notes", assistants: ["claude"], sessionsThisWeek: 8 },
            { ...base, id: "c", name: "quiet", assistants: ["codex"], sessionsThisWeek: 0 },
        ]);
        expect(lines).toEqual([
            { key: "b", agents: ["Claude Code"], project: "notes", sessions: 8 },
            { key: "a", agents: ["Codex", "Claude Code"], project: "isoform", sessions: 5 },
        ]);
        expect(learnedLines(rows.map((r) => ({ ...r, learnFromIt: false })), [{ ...base, id: "a", name: "isoform", assistants: ["codex"], sessionsThisWeek: 5 }])).toEqual([]);
    });
});
