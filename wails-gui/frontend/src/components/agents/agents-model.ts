// The Agents page's view model (computer-agents v1.0.0), built from what the
// computer keeps: Your agents (the two choices and who is connected), the
// hand-off log (each Yes, with exactly what went) and the results that came
// back. Pure, so it is tested without the page.

import type { ConnectedAgent, TaskLinkResult, WorkOnItHandoff, WorkOnItKind, YourAgents, YourAgentView } from "@/lib/wails-shim-app";

/** How a row says what an agent is for, and how it is reached. */
export type AgentJob = "forCodeHere" | "forCode" | "forElse" | "connected" | "foundHere" | "byText";

export interface AgentRow {
    id: string;
    name: string;
    kind: WorkOnItKind;
    job: AgentJob;
    connected: boolean;
    /** Connected on this computer with one click (C1), not by a join link. */
    connectedHere: boolean;
    /** The joined agent's own id, for Disconnect. */
    connectionId?: string;
    chosenFor?: WorkOnItKind;
    /** The title of a hand-off it has taken and not answered yet. */
    working?: string;
    lastWorkedAt?: number;
}

export type TimelineEntry =
    | { type: "handoff"; key: string; at: number; agentId: string; title: string; answered: boolean; text: string; taskState?: WorkOnItHandoff["taskState"]; taskResult?: string }
    | { type: "result"; key: string; at: number; agentId: string; linkId: string; status: TaskLinkResult["status"]; summary: string; files: string[] };

export interface MadeFile {
    key: string;
    linkId: string;
    name: string;
    agentId: string;
    at: number;
}

/** The task's title: its first line, as the person saw it before Send. */
export function handoffTitle(text: string): string {
    const first = text.split("\n").find((line) => line.trim() !== "") ?? "";
    return first.trim();
}

/** Hand-offs that came back: a result posted for them, or their task here ended. */
function answeredHandoffs(handoffs: WorkOnItHandoff[], results: TaskLinkResult[]): Set<string> {
    const answered = new Set(results.map((r) => r.linkId));
    for (const h of handoffs) if (h.taskState === "done" || h.taskState === "failed") answered.add(h.id);
    return answered;
}

/** The agent ids a result or hand-off may use for one row: its own and its catalog id. */
function rowKeys(row: AgentRow): string[] {
    return row.connectionId ? [row.id, row.connectionId] : [row.id];
}

/**
 * The rows of Your agents: the two chosen agents, every connected agent, and
 * coding agents found on this computer. Agents only known by name (not
 * chosen, not connected, not here) stay out of the list.
 */
export function agentRows(agents: YourAgents, connected: ConnectedAgent[], handoffs: WorkOnItHandoff[], results: TaskLinkResult[]): AgentRow[] {
    const answered = answeredHandoffs(handoffs, results);
    const chosen = new Map<string, WorkOnItKind>();
    (Object.keys(agents.choices) as WorkOnItKind[]).forEach((kind) => {
        if (agents.choices[kind]) chosen.set(agents.choices[kind], kind);
    });
    const rows: AgentRow[] = [];
    for (const view of agents.agents) {
        const chosenFor = chosen.get(view.id);
        if (!chosenFor && !view.connected && !(view.found && view.kind === "code")) continue;
        const connection = connected.find((c) => c.id === view.id || c.catalogId === view.id);
        rows.push({
            id: view.id,
            name: view.name,
            kind: view.kind,
            job: jobOf(view, chosenFor),
            connected: view.connected,
            connectedHere: !!view.connectedHere,
            connectionId: connection?.id,
            chosenFor,
            lastWorkedAt: view.lastWorkedAt,
        });
    }
    // Working: its newest hand-off that has no result yet.
    const newestFirst = [...handoffs].sort((a, b) => b.sentAt - a.sentAt);
    for (const row of rows) {
        const keys = rowKeys(row);
        const open = newestFirst.find((h) => keys.includes(h.agentId) && !answered.has(h.id));
        if (open) row.working = handoffTitle(open.text);
    }
    const rank = (row: AgentRow) => (row.chosenFor === "code" ? 0 : row.chosenFor === "else" ? 1 : row.connected ? 2 : 3);
    return rows.sort((a, b) => rank(a) - rank(b));
}

function jobOf(view: YourAgentView, chosenFor?: WorkOnItKind): AgentJob {
    if (chosenFor === "code") return view.found ? "forCodeHere" : "forCode";
    if (chosenFor === "else") return "forElse";
    if (view.connected) return "connected";
    if (view.reach === "by_text") return "byText";
    return "foundHere";
}

function startOfDay(now: Date): number {
    const d = new Date(now);
    d.setHours(0, 0, 0, 0);
    return d.getTime();
}

/** Today, newest first: each Yes and each result, optionally for one agent. */
export function todayTimeline(handoffs: WorkOnItHandoff[], results: TaskLinkResult[], now: Date, only?: AgentRow): TimelineEntry[] {
    const since = startOfDay(now);
    const answered = answeredHandoffs(handoffs, results);
    const keys = only ? rowKeys(only) : null;
    const entries: TimelineEntry[] = [];
    for (const h of handoffs) {
        if (h.sentAt < since || (keys && !keys.includes(h.agentId))) continue;
        entries.push({ type: "handoff", key: `h-${h.id}`, at: h.sentAt, agentId: h.agentId, title: handoffTitle(h.text), answered: answered.has(h.id), text: h.text, taskState: h.taskState, taskResult: h.taskResult });
    }
    for (const r of results) {
        if (r.resultAt < since || (keys && !keys.includes(r.agentId))) continue;
        entries.push({ type: "result", key: `r-${r.linkId}`, at: r.resultAt, agentId: r.agentId, linkId: r.linkId, status: r.status, summary: r.summary, files: r.files ?? [] });
    }
    return entries.sort((a, b) => b.at - a.at);
}

/** This week's files from every agent, newest first. */
export function madeThisWeek(results: TaskLinkResult[], now: Date): MadeFile[] {
    const since = startOfDay(now) - 6 * 24 * 60 * 60 * 1000;
    const out: MadeFile[] = [];
    for (const r of [...results].sort((a, b) => b.resultAt - a.resultAt)) {
        if (r.resultAt < since) continue;
        for (const name of r.files ?? []) out.push({ key: `${r.linkId}/${name}`, linkId: r.linkId, name, agentId: r.agentId, at: r.resultAt });
    }
    return out;
}

/** An agent's name for an id from a hand-off or result. */
export function agentName(id: string, agents: YourAgents, connected: ConnectedAgent[]): string {
    return agents.agents.find((a) => a.id === id)?.name ?? connected.find((c) => c.id === id)?.name ?? id;
}
