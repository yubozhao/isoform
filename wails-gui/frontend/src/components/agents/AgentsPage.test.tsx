import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import i18n from "@/i18n";
import en from "@/i18n/locales/en/app.json";

const mocks = vi.hoisted(() => ({
    agents: vi.fn(), connected: vi.fn(), handoffs: vi.fn(), results: vi.fn(), openFile: vi.fn(), disconnect: vi.fn(), setAgent: vi.fn(),
    start: vi.fn(), join: vi.fn(), answer: vi.fn(), cancel: vi.fn(), copy: vi.fn(),
    address: vi.fn(), startForApp: vi.fn(),
    statuses: vi.fn(), askAgain: vi.fn(), takeBack: vi.fn(), useful: vi.fn(), openApp: vi.fn(), history: vi.fn(), setLearn: vi.fn(),
    deliverables: vi.fn(), openDeliverable: vi.fn(), revealDeliverable: vi.fn(),
}));
vi.mock("@/lib/wails-shim-app", () => ({
    GetYourAgents: mocks.agents, GetConnectedAgents: mocks.connected, GetWorkOnItHandoffs: mocks.handoffs, GetTaskLinkResults: mocks.results,
    OpenAgentFile: mocks.openFile, DisconnectAgent: mocks.disconnect, DisconnectAgentHere: mocks.disconnect, SetYourAgent: mocks.setAgent, ConnectAgentHere: vi.fn(), GetHandoffStatuses: mocks.statuses, AskAgentAgain: mocks.askAgain, TakeHandoffBack: mocks.takeBack, GetUsefulHandoffs: mocks.useful, OpenAgentApp: mocks.openApp, GetAssistantHistory: mocks.history, SetLearnFromIt: mocks.setLearn,
    StartAgentJoin: mocks.start, StartAgentJoinForApp: mocks.startForApp, GetAgentAddress: mocks.address, GetAgentJoin: mocks.join, AnswerAgentJoin: mocks.answer, CancelAgentJoin: mocks.cancel, NativeClipboardSetText: mocks.copy,
    ListDeliverables: mocks.deliverables, OpenDeliverableFile: mocks.openDeliverable, RevealDeliverableFile: mocks.revealDeliverable,
}));

import zh from "@/i18n/locales/zh-CN/app.json";
import { AgentsPage } from "./AgentsPage";

const now = new Date(2026, 10, 15, 18, 45);
const today = (h: number, m: number) => new Date(2026, 10, 15, h, m).getTime();

beforeEach(async () => {
    vi.resetAllMocks();
    await i18n.changeLanguage("en");
    mocks.address.mockResolvedValue({ open: false, hostname: "", service: "", apps: [], needsTerms: true, unexpectedCertificates: [] });
    mocks.agents.mockResolvedValue({
        choices: { code: "codex", else: "" },
        agents: [
            { id: "codex", name: "Codex", kind: "code", reach: "connected_computer", found: true, connected: true },
            { id: "claude", name: "Claude", kind: "else", reach: "its_app", found: true, connected: false },
        ],
    });
    mocks.connected.mockResolvedValue([{ id: "ag_co", name: "Codex", device: "this Mac", mayOpen: "the repo", joinedAt: 1, catalogId: "codex" }]);
    mocks.handoffs.mockResolvedValue([{ id: "h1", sourceKind: "brief_item", kind: "code", agentId: "codex", method: "connected", text: "Write the test plan\n\nDone looks like: 14 cases", sentAt: today(18, 20), surface: "computer" }]);
    mocks.results.mockResolvedValue([{ linkId: "h1", agentId: "codex", status: "done", summary: "The test plan is ready: 14 cases.", files: ["test-plan.md"], resultAt: today(18, 40), via: "inbox" }]);
    mocks.start.mockResolvedValue({ message: "Join my Isoform: read http://127.0.0.1:8792/j/abc and follow the steps there.", link: "http://127.0.0.1:8792/j/abc", expiresAt: 0, local: true, mcpAddress: "http://127.0.0.1:8792/mcp?join=k" });
    mocks.statuses.mockResolvedValue([{ id: "h1", agentId: "codex", agentName: "Codex", title: "Write the test plan", sentAt: today(18, 20), state: "back", line: "The test plan is ready: 14 cases." }]);
    mocks.join.mockResolvedValue({ state: "waiting", expiresAt: 0 });
    mocks.useful.mockResolvedValue([]);
    mocks.history.mockResolvedValue({ enabled: false, state: "off", assistants: [], projects: [], beliefCount: 0, knowHowCount: 0, scannedAt: 0, pass2Done: false });
});

const project = (id: string, name: string, assistants: string[], sessionsThisWeek: number) => ({ id, name, path: `/p/${id}`, assistants, sessionsThisWeek, sessions14d: sessionsThisWeek, lastAt: 1, status: "", openProblem: "", openProblemAt: 0, openQuestion: "", openChip: "" });

const row = async (id: string) => {
    await waitFor(() => expect(document.querySelector(`[data-agent-row="${id}"]`)).not.toBeNull());
    return document.querySelector(`[data-agent-row="${id}"]`) as HTMLElement;
};
const cardOf = (id: string) => document.querySelector(`[data-agent-card="${id}"]`) as HTMLElement | null;

describe("Agents page", () => {
    it("shows your agents, today's hand-off and result, and what they made", async () => {
        render(<AgentsPage now={() => now} />);
        expect(await screen.findByRole("heading", { name: en.agents.title })).toBeInTheDocument();
        const yours = screen.getByRole("region", { name: en.agents.yours });
        await within(yours).findByText("Codex");
        expect(within(yours).getByText("For code · Connected")).toBeInTheDocument();
        expect(within(yours).queryByText("Claude")).not.toBeInTheDocument();

        const between = screen.getByRole("region", { name: en.agents.between });
        expect(within(between).getByText("You → Codex")).toBeInTheDocument();
        expect(within(between).getByText("Codex is back")).toBeInTheDocument();
        expect(within(between).getByText("The test plan is ready: 14 cases.")).toBeInTheDocument();
        fireEvent.click(within(between).getByRole("button", { name: en.agents.whatWent }));
        expect(within(between).getByText(/Done looks like: 14 cases/)).toBeInTheDocument();

        const made = screen.getByRole("region", { name: en.agents.made });
        fireEvent.click(within(made).getByRole("button", { name: /test-plan\.md/ }));
        await waitFor(() => expect(mocks.openFile).toHaveBeenCalledWith("h1", "test-plan.md"));
    });

    it("disconnects a connected agent from its row", async () => {
        mocks.disconnect.mockResolvedValue(undefined);
        render(<AgentsPage now={() => now} />);
        fireEvent.click(await screen.findByRole("button", { name: /Codex/, pressed: false }));
        // Disconnect lives on the card now, not under the list.
        expect(document.querySelector("[data-agents-selected-actions]")).toBeNull();
        fireEvent.click(within(cardOf("codex")!).getByRole("button", { name: en.agents.disconnect }));
        // Disconnect asks once.
        expect(screen.getByText(/Disconnect Codex\? It stops getting hand-offs at once/)).toBeInTheDocument();
        expect(mocks.disconnect).not.toHaveBeenCalled();
        fireEvent.click(document.querySelector("[data-agents-disconnect-yes]")!);
        await waitFor(() => expect(mocks.disconnect).toHaveBeenCalledWith("ag_co"));
    });

    it("has no way to the old Tasks page (computer-today v1.0.0 rule 9, computer-agents v1.0.0 rule 16)", async () => {
        render(<AgentsPage now={() => now} />);
        await screen.findByRole("button", { name: /Codex/, pressed: false });
        expect(screen.queryByRole("button", { name: /All tasks/i })).not.toBeInTheDocument();
        expect(document.querySelector("[data-agents-open-tasks]")).toBeNull();
    });

    it("says plainly when there's nothing yet", async () => {
        mocks.agents.mockResolvedValue({ choices: { code: "", else: "" }, agents: [] });
        mocks.connected.mockResolvedValue([]);
        mocks.handoffs.mockResolvedValue([]);
        mocks.results.mockResolvedValue([]);
        mocks.statuses.mockResolvedValue([]);
        render(<AgentsPage now={() => now} />);
        // With no agent connected, the page is the connect sheet (13a).
        expect(await screen.findByRole("heading", { name: en.agents.emptyTitle })).toBeInTheDocument();
        expect(screen.getByText(en.agents.untilThen)).toBeInTheDocument();
        expect(screen.queryByRole("region", { name: en.agents.between })).not.toBeInTheDocument();
    });

    it("says only what Isoform knows: no word with Ask again and Take it back, and no dot for an app", async () => {
        mocks.agents.mockResolvedValue({
            choices: { code: "codex", else: "claude" },
            agents: [
                { id: "codex", name: "Codex", kind: "code", reach: "connected_computer", found: true, connected: true },
                { id: "claude", name: "Claude", kind: "else", reach: "its_app", found: true, connected: false },
            ],
        });
        mocks.handoffs.mockResolvedValue([
            { id: "h1", sourceKind: "brief_item", kind: "code", agentId: "codex", method: "connected", text: "Write the test plan", sentAt: today(14, 0), surface: "computer" },
            { id: "h2", sourceKind: "brief_item", kind: "else", agentId: "claude", method: "deep_link", text: "Q4 plan outline", sentAt: today(16, 10), surface: "computer" },
        ]);
        mocks.results.mockResolvedValue([]);
        mocks.statuses.mockResolvedValue([
            { id: "h1", agentId: "codex", agentName: "Codex", title: "Write the test plan", sentAt: today(14, 0), state: "no_word", since: today(14, 0) },
            { id: "h2", agentId: "claude", agentName: "Claude", title: "Q4 plan outline", sentAt: today(16, 10), state: "opened" },
        ]);
        mocks.askAgain.mockResolvedValue({});
        render(<AgentsPage now={() => now} />);
        await waitFor(() => expect(document.querySelector('[data-agent-row="codex"]')).not.toBeNull());
        const codexRow = document.querySelector('[data-agent-row="codex"]') as HTMLElement;
        expect(within(codexRow).getByText(/No word since/)).toBeInTheDocument();
        const claudeRow = document.querySelector('[data-agent-row="claude"]') as HTMLElement;
        expect(within(claudeRow).getByText("Has it: Q4 plan outline")).toBeInTheDocument();
        expect(within(claudeRow).queryByLabelText(en.agents.workingDot)).not.toBeInTheDocument();
        expect(screen.getByText(/Opened in Claude/)).toBeInTheDocument();
        fireEvent.click(screen.getByRole("button", { name: en.agents.status.askAgain }));
        await waitFor(() => expect(mocks.askAgain).toHaveBeenCalledWith("h1"));
        fireEvent.click(screen.getByRole("button", { name: en.agents.status.takeBack }));
        await waitFor(() => expect(mocks.takeBack).toHaveBeenCalledWith("h1"));
    });

    it("never says Blocked or Declined on a result (agents-join v1.0.0 rule 3)", async () => {
        mocks.results.mockResolvedValue([{ linkId: "h1", agentId: "codex", status: "blocked", summary: "Needs a token to read the repo.", files: [], resultAt: today(18, 40), via: "inbox" }]);
        render(<AgentsPage now={() => now} />);
        expect(await screen.findByText("Needs a token to read the repo.")).toBeInTheDocument();
        expect(screen.queryByText(/Blocked|Declined/)).not.toBeInTheDocument();
        expect((en.agents.status as Record<string, string>).blocked).toBeUndefined();
        expect((en.agents.status as Record<string, string>).declined).toBeUndefined();
    });

    it("says it couldn't read, with Try again", async () => {
        mocks.agents.mockRejectedValue(new Error("down"));
        render(<AgentsPage now={() => now} />);
        expect(await screen.findByText(en.agents.failed)).toBeInTheDocument();
        mocks.agents.mockResolvedValue({ choices: { code: "", else: "" }, agents: [] });
        fireEvent.click(screen.getByRole("button", { name: en.agents.retry }));
        await waitFor(() => expect(screen.queryByText(en.agents.failed)).not.toBeInTheDocument());
    });

    it("opens an agent's card beside the list; clicking again or Escape closes it (13d)", async () => {
        mocks.agents.mockResolvedValue({
            choices: { code: "codex", else: "" },
            agents: [{ id: "codex", name: "Codex", kind: "code", reach: "connected_computer", found: true, connected: true, connectedHere: true, connectedAt: new Date(2026, 10, 3).getTime() }],
        });
        mocks.connected.mockResolvedValue([]);
        render(<AgentsPage now={() => now} />);
        fireEvent.click(await row("codex"));
        const card = cardOf("codex")!;
        expect(within(card).getByRole("heading", { name: "Codex" })).toBeInTheDocument();
        expect(within(card).getByText("Connected Nov 3 · on this computer · runs when Isoform starts it")).toBeInTheDocument();
        expect(within(card).getByText(en.agents.card.jobCode)).toBeInTheDocument();
        expect(document.activeElement).toBe(card);
        fireEvent.click(await row("codex"));
        expect(cardOf("codex")).toBeNull();
        fireEvent.click(await row("codex"));
        fireEvent.keyDown(window, { key: "Escape" });
        expect(cardOf("codex")).toBeNull();
        fireEvent.click(await row("codex"));
        fireEvent.click(document.querySelector("[data-agent-card-close]")!);
        expect(cardOf("codex")).toBeNull();
    });

    it("says a chat app runs in its app and has no green dot", async () => {
        mocks.agents.mockResolvedValue({ choices: { code: "", else: "claude" }, agents: [{ id: "claude", name: "Claude", kind: "else", reach: "its_app", found: true, connected: false }] });
        mocks.statuses.mockResolvedValue([{ id: "h9", agentId: "claude", agentName: "Claude", title: "Q4", sentAt: today(16, 0), state: "working" }]);
        render(<AgentsPage now={() => now} />);
        const claude = await row("claude");
        expect(within(claude).getByText("For everything else · Not connected")).toBeInTheDocument();
        fireEvent.click(claude);
        expect(within(cardOf("claude")!).getByText("Not connected · in its app; you open it")).toBeInTheDocument();
    });

    it("has no All agents button and no Make it your agent button", async () => {
        render(<AgentsPage now={() => now} />);
        fireEvent.click(await row("codex"));
        expect(screen.queryByRole("button", { name: /All agents/ })).not.toBeInTheDocument();
        expect(screen.queryByRole("button", { name: /Make it your agent/ })).not.toBeInTheDocument();
        expect(document.querySelector("[data-agents-show-all]")).toBeNull();
        expect((en.agents as Record<string, unknown>).makeCode).toBeUndefined();
        expect((en.agents as Record<string, unknown>).showAll).toBeUndefined();
    });

    it("changes its job with SetYourAgent and says who takes over", async () => {
        mocks.agents.mockResolvedValue({
            choices: { code: "codex", else: "" },
            agents: [
                { id: "codex", name: "Codex", kind: "code", reach: "connected_computer", found: true, connected: true, connectedHere: true },
                { id: "claude-code", name: "Claude Code", kind: "code", reach: "connected_computer", found: true, connected: true, connectedHere: true },
            ],
        });
        mocks.connected.mockResolvedValue([]);
        mocks.setAgent.mockResolvedValue({});
        render(<AgentsPage now={() => now} />);
        fireEvent.click(await row("claude-code"));
        fireEvent.pointerDown(within(cardOf("claude-code")!).getByRole("button", { name: en.agents.card.changeJob }), { button: 0, pointerType: "mouse" });
        fireEvent.click(await screen.findByRole("menuitem", { name: en.agents.card.forCode }));
        await waitFor(() => expect(mocks.setAgent).toHaveBeenCalledWith("code", "claude-code"));
        expect(await screen.findByText("Codex does your code work now. Claude Code takes over; Codex stays connected.")).toBeInTheDocument();
    });

    it("Not now clears the job only for the agent that holds it", async () => {
        mocks.setAgent.mockResolvedValue({});
        render(<AgentsPage now={() => now} />);
        fireEvent.click(await row("codex"));
        fireEvent.pointerDown(within(cardOf("codex")!).getByRole("button", { name: en.agents.card.changeJob }), { button: 0, pointerType: "mouse" });
        fireEvent.click(await screen.findByRole("menuitem", { name: en.agents.card.notNow }));
        await waitFor(() => expect(mocks.setAgent).toHaveBeenCalledWith("code", ""));
    });

    it("lists the files a done local hand-off's task made, with Open and Show in Finder (Tasks removal keeps them)", async () => {
        mocks.handoffs.mockResolvedValue([{ id: "h2", sourceKind: "brief_item", kind: "code", agentId: "codex", method: "connected", text: "Draft the memo", sentAt: today(17, 0), surface: "computer", taskId: "t9", taskState: "done", taskResult: "The memo is drafted." }]);
        mocks.results.mockResolvedValue([]);
        mocks.statuses.mockResolvedValue([]);
        mocks.deliverables.mockResolvedValue([
            { id: "handoff:t9", source: "handoff", sourceId: "t9", title: "Draft the memo", summary: "", status: "completed", files: [{ path: "/w/memo.md", name: "memo.md" }], createdAt: 1, updatedAt: 2 },
            { id: "chat:x", source: "chat", sourceId: "t9", title: "Other", summary: "", status: "completed", files: [{ path: "/w/chat.md", name: "chat.md" }], createdAt: 1, updatedAt: 2 },
        ]);
        mocks.openDeliverable.mockResolvedValue(undefined);
        mocks.revealDeliverable.mockResolvedValue(undefined);
        render(<AgentsPage now={() => now} />);
        await screen.findByText("The memo is drafted.");
        const file = await waitFor(() => {
            const el = document.querySelector('[data-agents-task-file="memo.md"]') as HTMLElement | null;
            expect(el).not.toBeNull();
            return el!;
        });
        expect(document.querySelector('[data-agents-task-file="chat.md"]')).toBeNull();
        fireEvent.click(file);
        await waitFor(() => expect(mocks.openDeliverable).toHaveBeenCalledWith("handoff", "t9", "/w/memo.md"));
        fireEvent.contextMenu(file);
        fireEvent.click(await screen.findByRole("menuitem", { name: en.agents.showInFinder }));
        await waitFor(() => expect(mocks.revealDeliverable).toHaveBeenCalledWith("handoff", "t9", "/w/memo.md"));
    });

    it("shows no task files while the hand-off's task is still running", async () => {
        mocks.handoffs.mockResolvedValue([{ id: "h2", sourceKind: "brief_item", kind: "code", agentId: "codex", method: "connected", text: "Draft the memo", sentAt: today(17, 0), surface: "computer", taskId: "t9", taskState: "running" }]);
        mocks.results.mockResolvedValue([]);
        mocks.statuses.mockResolvedValue([]);
        mocks.deliverables.mockResolvedValue([{ id: "handoff:t9", source: "handoff", sourceId: "t9", title: "Draft the memo", summary: "", status: "in_progress", files: [{ path: "/w/memo.md", name: "memo.md" }], createdAt: 1, updatedAt: 2 }]);
        render(<AgentsPage now={() => now} />);
        await screen.findByText(/Draft the memo/);
        await waitFor(() => expect(mocks.deliverables).toHaveBeenCalled());
        expect(document.querySelector("[data-agents-task-file]")).toBeNull();
    });

    it("counts this week's hand-offs, what came back, and what you judged useful", async () => {
        const day = (d: number) => new Date(2026, 10, d, 10).getTime();
        mocks.handoffs.mockResolvedValue([
            { id: "a", sourceKind: "brief_item", kind: "code", agentId: "codex", method: "connected", text: "A", sentAt: day(15), surface: "computer" },
            { id: "b", sourceKind: "brief_item", kind: "code", agentId: "ag_co", method: "connected", text: "B", sentAt: day(12), surface: "computer" },
            { id: "c", sourceKind: "brief_item", kind: "code", agentId: "codex", method: "connected", text: "C", sentAt: day(10), surface: "computer", taskState: "done" },
            { id: "old", sourceKind: "brief_item", kind: "code", agentId: "codex", method: "connected", text: "Old", sentAt: day(2), surface: "computer" },
            { id: "other", sourceKind: "brief_item", kind: "else", agentId: "claude", method: "deep_link", text: "Other", sentAt: day(15), surface: "computer" },
        ]);
        mocks.results.mockResolvedValue([
            { linkId: "a", agentId: "codex", status: "done", summary: "", files: [], resultAt: day(15) },
            { linkId: "b", agentId: "ag_co", status: "blocked", summary: "", files: [], resultAt: day(12) },
        ]);
        mocks.useful.mockResolvedValue(["a", "old"]);
        render(<AgentsPage now={() => now} />);
        fireEvent.click(await row("codex"));
        expect(within(cardOf("codex")!).getByText("3 hand-offs · 2 back · 1 useful")).toBeInTheDocument();
    });

    it("keeps a disconnected agent as a faded row with Connect again", async () => {
        mocks.agents.mockResolvedValue({
            choices: { code: "", else: "" },
            agents: [
                { id: "codex", name: "Codex", kind: "code", reach: "connected_computer", found: true, connected: true, connectedHere: true },
                { id: "claude-code", name: "Claude Code", kind: "code", reach: "on_computer", found: false, connected: false, disconnectedAt: new Date(2026, 10, 12).getTime() },
            ],
        });
        mocks.connected.mockResolvedValue([]);
        render(<AgentsPage now={() => now} />);
        const gone = await row("claude-code");
        expect(within(gone).getByText("Disconnected · Nov 12")).toBeInTheDocument();
        expect(within(gone).getByText("Claude Code")).toHaveClass("truncate");
        expect(gone.querySelector(".text-ink")).toBeNull();
        fireEvent.click(document.querySelector('[data-agents-connect-again="claude-code"]')!);
        expect(await screen.findByRole("dialog")).toBeInTheDocument();
    });

    it("says Nothing was sent with Open {agent} when a hand-off couldn't start; amber only there", async () => {
        mocks.agents.mockResolvedValue({ choices: { code: "codex", else: "" }, agents: [{ id: "codex", name: "Codex", kind: "code", reach: "connected_computer", found: true, connected: true, connectedHere: true, opens: true }] });
        mocks.connected.mockResolvedValue([]);
        mocks.results.mockResolvedValue([]);
        mocks.statuses.mockResolvedValue([{ id: "h1", agentId: "codex", agentName: "Codex", title: "Write the test plan", sentAt: today(18, 20), state: "couldnt_start", line: "not logged in." }]);
        mocks.openApp.mockResolvedValue(undefined);
        render(<AgentsPage now={() => now} />);
        await waitFor(() => expect(document.querySelector('[data-handoff-state="couldnt_start"]')).not.toBeNull());
        const state = document.querySelector('[data-handoff-state="couldnt_start"]') as HTMLElement;
        expect(within(state).getByText("Codex said: not logged in.")).toBeInTheDocument();
        expect(within(state).getByText(en.agents.status.nothingSent)).toBeInTheDocument();
        fireEvent.click(within(state).getByRole("button", { name: "Open Codex" }));
        await waitFor(() => expect(mocks.openApp).toHaveBeenCalledWith("codex"));
        expect(within(state).getByRole("button", { name: en.agents.status.sendAgain })).toBeInTheDocument();
        // Amber is only for something to act on: the couldn't-start lines.
        const amber = [...document.querySelectorAll(".text-warning")].map((el) => el.textContent);
        expect(amber).toEqual(["Couldn't start · 6:20 PM", "Codex couldn't start"]);
    });

    it("opens one hand-off from elsewhere with What went open and Back (G4)", async () => {
        const onBack = vi.fn();
        mocks.handoffs.mockResolvedValue([
            { id: "h1", sourceKind: "brief_item", kind: "code", agentId: "codex", method: "connected", text: "Write the test plan\n\nDone looks like: 14 cases", sentAt: today(18, 20), surface: "computer" },
            { id: "h0", sourceKind: "brief_item", kind: "code", agentId: "codex", method: "connected", text: "Older plan\n\nLast week", sentAt: new Date(2026, 10, 9, 9).getTime(), surface: "computer" },
        ]);
        const { rerender } = render(<AgentsPage now={() => now} openHandoff={{ id: "h0", nonce: 1, backLabel: "Find", onBack }} />);
        await waitFor(() => expect(cardOf("codex")).not.toBeNull());
        const between = screen.getByRole("region", { name: en.agents.between });
        expect(within(between).getByText(/Older plan/, { selector: "pre" })).toBeInTheDocument();
        expect(within(between).queryByText(/Write the test plan/)).not.toBeInTheDocument();
        fireEvent.click(screen.getByRole("button", { name: "‹ Find" }));
        expect(onBack).toHaveBeenCalled();
        // Closing the card returns to every agent; a new nonce opens it again.
        fireEvent.click(document.querySelector("[data-agent-card-close]")!);
        expect(cardOf("codex")).toBeNull();
        expect(within(between).getByText(/Write the test plan/)).toBeInTheDocument();
        rerender(<AgentsPage now={() => now} openHandoff={{ id: "h0", nonce: 2, backLabel: "Find", onBack }} />);
        await waitFor(() => expect(cardOf("codex")).not.toBeNull());
        expect(within(between).queryByText(/Write the test plan/)).not.toBeInTheDocument();
        expect(within(between).getByText(/Last week/)).toBeInTheDocument();
    });

    it("has every new word in Chinese too", () => {
        const keys = (o: Record<string, unknown>, pre = ""): string[] => Object.entries(o).flatMap(([k, v]) => (v && typeof v === "object" ? keys(v as Record<string, unknown>, `${pre}${k}.`) : [`${pre}${k}`]));
        const zhKeys = new Set(keys(zh.agents));
        const missing = keys(en.agents).filter((k) => !zhKeys.has(k));
        expect(missing).toEqual([]);
        for (const k of ["card.connectedOn", "card.runsWhenStarted", "disconnectedOn", "connectAgain", "status.nothingSent", "status.openAgent", "back"]) expect(zhKeys.has(k)).toBe(true);
    });

    describe("Learn from it", () => {
        beforeEach(() => {
            mocks.agents.mockResolvedValue({
                choices: { code: "codex", else: "" },
                agents: [
                    { id: "codex", name: "Codex", kind: "code", reach: "connected_computer", found: true, connected: true, connectedHere: true, learnFromIt: false },
                    { id: "claude-code", name: "Claude Code", kind: "code", reach: "connected_computer", found: true, connected: true, connectedHere: true, learnFromIt: false },
                    { id: "claude", name: "Claude", kind: "else", reach: "its_app", found: true, connected: false },
                ],
            });
            mocks.connected.mockResolvedValue([]);
            mocks.setLearn.mockResolvedValue(undefined);
        });

        it("the card's switch turns it on, and off asks once", async () => {
            render(<AgentsPage now={() => now} />);
            fireEvent.click(await row("codex"));
            const card = cardOf("codex")!;
            expect(within(card).getByText(/Read Codex's own history on this computer/)).toBeInTheDocument();
            fireEvent.click(card.querySelector('[data-learn-switch="codex"]')!);
            await waitFor(() => expect(mocks.setLearn).toHaveBeenCalledWith("codex", true));
        });

        it("turning it off asks once before it forgets", async () => {
            mocks.agents.mockResolvedValue({ choices: { code: "codex", else: "" }, agents: [{ id: "codex", name: "Codex", kind: "code", reach: "connected_computer", found: true, connected: true, connectedHere: true, learnFromIt: true }] });
            render(<AgentsPage now={() => now} />);
            fireEvent.click(await row("codex"));
            fireEvent.click(cardOf("codex")!.querySelector('[data-learn-switch="codex"]')!);
            expect(await screen.findByText(/forgets what it learned/)).toBeInTheDocument();
            expect(mocks.setLearn).not.toHaveBeenCalled();
            fireEvent.click(screen.getByRole("button", { name: en.agents.learn.offConfirm }));
            await waitFor(() => expect(mocks.setLearn).toHaveBeenCalledWith("codex", false));
        });

        it("an agent in its own app says it isn't available", async () => {
            mocks.agents.mockResolvedValue({ choices: { code: "", else: "claude" }, agents: [{ id: "claude", name: "Claude", kind: "else", reach: "its_app", found: true, connected: false }] });
            mocks.handoffs.mockResolvedValue([{ id: "h2", sourceKind: "brief_item", kind: "else", agentId: "claude", method: "deep_link", text: "Q4", sentAt: today(16, 10), surface: "computer" }]);
            render(<AgentsPage now={() => now} />);
            fireEvent.click(await row("claude"));
            expect(within(cardOf("claude")!).getByText(en.agents.learnNotHere)).toBeInTheDocument();
            expect(cardOf("claude")!.querySelector("[data-learn-switch]")).toBeNull();
        });

        it("Learned from them: quiet line with Choose › when every agent is off", async () => {
            mocks.history.mockResolvedValue({ enabled: false, state: "off", assistants: ["claude"], projects: [project("p1", "isoform", ["claude"], 4)], beliefCount: 0, knowHowCount: 0, scannedAt: 0, pass2Done: false });
            render(<AgentsPage now={() => now} />);
            const learned = await waitFor(() => {
                const el = document.querySelector("[data-agents-learned]") as HTMLElement | null;
                expect(el).not.toBeNull();
                return el!;
            });
            expect(within(learned).getByText(en.agents.learnOffAll)).toBeInTheDocument();
            expect(within(learned).getByText(en.agents.learnedFoot)).toBeInTheDocument();
            expect(learned.querySelector("[data-agents-learned-line]")).toBeNull();
            fireEvent.click(within(learned).getByRole("button", { name: en.agents.learnChoose }));
            expect(cardOf("codex")).not.toBeNull();
        });

        it("Learned from them shows only what came from agents turned on, and each line opens Activity › Agents", async () => {
            mocks.agents.mockResolvedValue({
                choices: { code: "codex", else: "" },
                agents: [
                    { id: "codex", name: "Codex", kind: "code", reach: "connected_computer", found: true, connected: true, connectedHere: true, learnFromIt: true },
                    { id: "claude-code", name: "Claude Code", kind: "code", reach: "connected_computer", found: true, connected: true, connectedHere: true, learnFromIt: false },
                ],
            });
            mocks.history.mockResolvedValue({ enabled: true, state: "ready", assistants: ["codex", "claude"], projects: [project("p1", "isoform", ["codex"], 3), project("p2", "notes", ["claude"], 9), project("p3", "old", ["codex"], 0)], beliefCount: 0, knowHowCount: 0, scannedAt: 1, pass2Done: true });
            const onOpenActivityAgents = vi.fn();
            render(<AgentsPage now={() => now} onOpenActivityAgents={onOpenActivityAgents} />);
            const line = await screen.findByRole("button", { name: "Codex: 3 sessions on isoform this week" });
            expect(screen.queryByText(/notes this week/)).not.toBeInTheDocument();
            expect(screen.queryByText(/on old this week/)).not.toBeInTheDocument();
            expect(screen.queryByText(en.agents.learnOffAll)).not.toBeInTheDocument();
            fireEvent.click(line);
            expect(onOpenActivityAgents).toHaveBeenCalled();
        });

        it("a failed read of what it learned says so, with Try again", async () => {
            mocks.history.mockRejectedValue(new Error("down"));
            render(<AgentsPage now={() => now} />);
            expect(await screen.findByText(en.agents.learn.failed)).toBeInTheDocument();
            mocks.history.mockResolvedValue({ enabled: false, state: "off", assistants: [], projects: [], beliefCount: 0, knowHowCount: 0, scannedAt: 0, pass2Done: false });
            fireEvent.click(within(document.querySelector("[data-agents-learned-failed]") as HTMLElement).getByRole("button", { name: en.agents.retry }));
            await waitFor(() => expect(screen.queryByText(en.agents.learn.failed)).not.toBeInTheDocument());
        });
    });
});
