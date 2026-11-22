// The Agents page's view model (computer-agents v1.0.0), built from what the
// computer keeps: Your agents (the two choices and who is connected), the
// hand-off log (each Yes, with exactly what went) and the results that came
// back. Pure, so it is tested without the page.

import type { ConnectedAgent, HistoryProject, TaskLinkResult, WorkOnItHandoff, WorkOnItKind, YourAgents, YourAgentView } from "@/lib/wails-shim-app";

/** How a row says what an agent is for, and how it is reached. */
export type AgentJob = "forCodeHere" | "forCode" | "forElse" | "connected" | "foundHere" | "byText";

/**
 * The only states an agent has in Your agents (agents-join v1.0.0 §1 rule 3):
 * Connected (it has Isoform's tools), Not connected (Isoform writes the task
 * in for the person), and Disconnected right after Disconnect.
 */
export type AgentState = "connected" | "notConnected" | "disconnected";

/**
 * Where an agent runs: "here" for coding agents on this computer, "itsApp"
 * for chat apps, "byText" for agents reached by text message, and "device"
 * for an agent that joined from its own device (`device` names it).
 */
export type AgentWhere = "here" | "itsApp" | "byText" | "device";

export interface AgentRow {
    id: string;
    name: string;
    kind: WorkOnItKind;
    job: AgentJob;
    state: AgentState;
    where: AgentWhere;
    /** The device a joined agent named for itself, when `where` is "device". */
    device?: string;
    /** On this computer: its app or command was found here. */
    found: boolean;
    connected: boolean;
    /** Connected on this computer with one click (C1), not by a join link. */
    connectedHere: boolean;
    /** The joined agent's own id, for Disconnect. */
    connectionId?: string;
    chosenFor?: WorkOnItKind;
    /** The title of a hand-off it has taken and not answered yet. */
    working?: string;
    lastWorkedAt?: number;
    /** When it was connected, for its card's "Connected {date}". */
    connectedAt?: number;
    /** When it was disconnected, for a faded row (30 days, then it leaves). */
    disconnectedAt?: number;
    /** Open {agent} has somewhere to go (its app, its site, or its sign-in). */
    opens: boolean;
    /** What a joined agent said it may open, read-only on its card. */
    mayOpen?: string;
    /** Learn from it is on: Isoform reads this agent's own history here. */
    learnFromIt: boolean;
}

/**
 * Agents whose own history Isoform can read on this computer, and the name
 * that history carries (learn_from_it.go historySources). Learn from it is
 * only for these; any other agent is "Not available…".
 */
export const LEARN_SOURCES: Readonly<Record<string, string>> = { "claude-code": "claude", codex: "codex" };

export function canLearnFrom(row: Pick<AgentRow, "id">): boolean {
    return LEARN_SOURCES[row.id] !== undefined;
}

export interface LearnedLine {
    key: string;
    /** The on agents whose history this line comes from. */
    agents: string[];
    project: string;
    sessions: number;
}

/**
 * Learned from them (computer-agents v1.0.0 rule 10): what Isoform read this
 * week, per project, from agents whose Learn from it is on. A project's count
 * is for all the on agents it names, so the line names each of them.
 */
export function learnedLines(rows: AgentRow[], projects: HistoryProject[]): LearnedLine[] {
    const on = rows.filter((row) => row.learnFromIt && canLearnFrom(row));
    const out: LearnedLine[] = [];
    for (const project of projects) {
        if (project.sessionsThisWeek <= 0) continue;
        const agents = on.filter((row) => project.assistants.includes(LEARN_SOURCES[row.id])).map((row) => row.name);
        if (agents.length === 0) continue;
        out.push({ key: project.id, agents, project: project.name, sessions: project.sessionsThisWeek });
    }
    return out.sort((a, b) => b.sessions - a.sessions);
}

export interface AgentRowsOptions {
    /**
     * Keep agents disconnected in the last 30 days, as faded rows with Connect
     * again (computer-agents v1.1.0 13c rule 9). The Agents page does;
     * Settings › Your agents lists only who it can reach.
     */
    disconnected?: boolean;
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
export function agentRows(agents: YourAgents, connected: ConnectedAgent[], handoffs: WorkOnItHandoff[], results: TaskLinkResult[], options: AgentRowsOptions = {}): AgentRow[] {
    const answered = answeredHandoffs(handoffs, results);
    const chosen = new Map<string, WorkOnItKind>();
    (Object.keys(agents.choices) as WorkOnItKind[]).forEach((kind) => {
        if (agents.choices[kind]) chosen.set(agents.choices[kind], kind);
    });
    const rows: AgentRow[] = [];
    for (const view of agents.agents) {
        const gone = isDisconnected(view);
        if (gone && !options.disconnected) {
            // Settings lists only who it can reach; a disconnected joined agent fades there on the phone only.
            if (view.reach === "disconnected") continue;
        }
        const chosenFor = chosen.get(view.id);
        if (!chosenFor && !view.connected && !(view.found && view.kind === "code") && !(gone && options.disconnected)) continue;
        const connection = connected.find((c) => c.id === view.id || c.catalogId === view.id);
        rows.push({
            id: view.id,
            name: view.name,
            kind: view.kind,
            job: jobOf(view, chosenFor),
            state: gone && options.disconnected ? "disconnected" : stateOf(view),
            ...whereOf(view, connection),
            found: view.found,
            connected: view.connected,
            connectedHere: !!view.connectedHere,
            connectionId: connection?.id,
            chosenFor,
            lastWorkedAt: view.lastWorkedAt,
            connectedAt: view.connectedAt,
            disconnectedAt: gone ? view.disconnectedAt : undefined,
            opens: !!view.opens,
            mayOpen: connection?.mayOpen || undefined,
            learnFromIt: !!view.learnFromIt,
        });
    }
    // Working: its newest hand-off that has no result yet.
    const newestFirst = [...handoffs].sort((a, b) => b.sentAt - a.sentAt);
    for (const row of rows) {
        const keys = rowKeys(row);
        const open = newestFirst.find((h) => keys.includes(h.agentId) && !answered.has(h.id));
        if (open) row.working = handoffTitle(open.text);
    }
    const rank = (row: AgentRow) => (row.state === "disconnected" ? 4 : row.chosenFor === "code" ? 0 : row.chosenFor === "else" ? 1 : row.connected ? 2 : 3);
    return rows.sort((a, b) => rank(a) - rank(b));
}

/** An agent's state, from how Isoform reaches it. */
export function stateOf(view: YourAgentView): AgentState {
    if (view.reach === "disconnected") return "disconnected";
    return view.connected ? "connected" : "notConnected";
}

/** Disconnected in the last 30 days and not connected since (the computer drops it after 30 days). */
function isDisconnected(view: YourAgentView): boolean {
    return view.reach === "disconnected" || (!view.connected && !!view.disconnectedAt);
}

/** Where an agent runs. A catalog agent's place is fixed by its kind; an agent that joined under its own name runs on the device it named. */
export function whereOf(view: YourAgentView, connection?: ConnectedAgent): { where: AgentWhere; device?: string } {
    if (view.reach === "by_text") return { where: "byText" };
    // A joined agent runs where it said, catalog coding agents too (a Codex
    // joined from a cloud device isn't here). "here" is only for one connected
    // with one click on this computer, or a coding agent found here.
    if (connection?.device && !view.connectedHere) return { where: "device", device: connection.device };
    if (view.kind === "code") return { where: "here" };
    return { where: "itsApp" };
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

/** One hand-off and what came back for it, whatever day it was (G4: Agents, narrowed to that hand-off). */
export function handoffTimeline(handoffs: WorkOnItHandoff[], results: TaskLinkResult[], id: string): TimelineEntry[] {
    const h = handoffs.find((x) => x.id === id);
    if (!h) return [];
    const answered = answeredHandoffs(handoffs, results);
    const entries: TimelineEntry[] = [{ type: "handoff", key: `h-${h.id}`, at: h.sentAt, agentId: h.agentId, title: handoffTitle(h.text), answered: answered.has(h.id), text: h.text, taskState: h.taskState, taskResult: h.taskResult }];
    for (const r of results) {
        if (r.linkId === id) entries.push({ type: "result", key: `r-${r.linkId}`, at: r.resultAt, agentId: r.agentId, linkId: r.linkId, status: r.status, summary: r.summary, files: r.files ?? [] });
    }
    return entries.sort((a, b) => b.at - a.at);
}

/** The row a hand-off's agent has, by its own id or its catalog id. */
export function rowForAgent(rows: AgentRow[], agentId: string): AgentRow | undefined {
    return rows.find((row) => rowKeys(row).includes(agentId));
}

export interface WeekCounts {
    handoffs: number;
    back: number;
    useful: number;
}

/**
 * This week for one agent (computer-agents v1.1.0 rule 12): hand-offs sent
 * in the last seven days, how many came back done, and how many of those the
 * person judged Useful (from their own answers, never a guess).
 */
export function weekCounts(row: AgentRow, handoffs: WorkOnItHandoff[], results: TaskLinkResult[], useful: ReadonlySet<string>, now: Date): WeekCounts {
    const since = startOfDay(now) - 6 * 24 * 60 * 60 * 1000;
    const keys = rowKeys(row);
    const doneLinks = new Set(results.filter((r) => r.status === "done").map((r) => r.linkId));
    const out: WeekCounts = { handoffs: 0, back: 0, useful: 0 };
    for (const h of handoffs) {
        if (h.sentAt < since || !keys.includes(h.agentId)) continue;
        out.handoffs++;
        if (doneLinks.has(h.id) || h.taskState === "done") out.back++;
        if (useful.has(h.id)) out.useful++;
    }
    return out;
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

/**
 * The coding agent that is also Isoform's own assistant (computer conformance
 * answers v1.0.0 G1): with signed-in subscriptions, the first in the order;
 * with an assistant's own CLI, that CLI. An API key or local model is no agent.
 */
export function assistantAgentId(mode: string | undefined, accountOrder: string[] | undefined, provider: string | undefined): string {
    if (mode === "managed") {
        const first = accountOrder?.[0];
        return first === "claude" ? "claude-code" : first === "codex" ? "codex" : "";
    }
    return provider === "claude-code" || provider === "codex" ? provider : "";
}
