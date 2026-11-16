import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import i18n from "@/i18n";
import type { WorkItem, YourAgents } from "@/lib/wails-shim-app";
import { TodayHandOffSheet, handOffAgent, handOffKind } from "./TodayHandOffSheet";

const mocks = vi.hoisted(() => ({ prepare: vi.fn(), sendWorkOnIt: vi.fn() }));
vi.mock("@/lib/wails-shim-app", () => ({
    RecordTodayAction: vi.fn().mockResolvedValue(undefined),
    PrepareWorkOnIt: mocks.prepare,
    SendWorkOnIt: mocks.sendWorkOnIt,
    SetWorkOnItHandle: vi.fn(),
    GetWorkOnItEnabled: vi.fn().mockResolvedValue(true),
}));
vi.mock("@/lib/today-telemetry", () => ({ measureToday: (_id: string, _kind: string, _outcome: string, run: () => unknown) => run() }));

const agents: YourAgents = {
    choices: { code: "codex", else: "claude" },
    agents: [
        { id: "codex", name: "Codex", kind: "code", reach: "connected_computer", found: true, connected: true },
        { id: "claude-code", name: "Claude Code", kind: "code", reach: "on_computer", found: true, connected: false },
        { id: "claude", name: "Claude", kind: "else", reach: "its_app", found: false, connected: false },
    ],
};

const item = (overrides: Partial<WorkItem> = {}): WorkItem => ({
    id: "brief:b1:offer:o1", kind: "offered", row: "offered", state: "open", source: "brief", ref: {},
    first: [{ text: "From Atlas" }], noticed: "The test plan is still open.",
    work: { title: "Test plan", text: "Write the test plan for Atlas. Don't change any code." },
    yes: { does: "agent", line: "Yes hands it to Codex, your agent.", agent: "codex" },
    gets: [{ id: "g1", label: "Atlas README", removable: true }, { id: "g2", label: "The open issue", removable: false }],
    reasons: [], createdAt: "", order: 0, ...overrides,
});

describe("TodayHandOffSheet", () => {
    beforeEach(async () => { await i18n.changeLanguage("en"); });
    afterEach(cleanup);

    it("picks the agent by the work", () => {
        expect(handOffKind(item(), agents)).toBe("code");
        expect(handOffKind(item({ yes: { does: "agent", line: "", agent: "claude" } }), agents)).toBe("else");
        expect(handOffAgent("code", agents)?.name).toBe("Codex");
        expect(handOffAgent("code", agents, "claude-code")?.name).toBe("Claude Code");
    });

    it("shows exactly what goes and sends with what was removed and the agent", async () => {
        const onSend = vi.fn().mockResolvedValue(null);
        const onClose = vi.fn();
        render(<TodayHandOffSheet item={item()} agents={agents} onSend={onSend} onClose={onClose} />);
        expect(screen.getByText("Hand it to Codex")).toBeInTheDocument();
        expect(screen.getByTestId("handoff-subtitle")).toHaveTextContent("Your agent for code · connected on this computer");
        expect(screen.getByTestId("handoff-text")).toHaveTextContent("Write the test plan for Atlas. Don't change any code.");
        expect(screen.getAllByTestId("handoff-get")).toHaveLength(2);
        fireEvent.click(screen.getByRole("button", { name: "Remove Atlas README" }));
        expect(screen.getAllByTestId("handoff-get")).toHaveLength(1);
        expect(screen.queryByRole("button", { name: "Remove The open issue" })).toBeNull();
        expect(screen.getByTestId("handoff-foot")).toHaveTextContent("Codex works in its own account. The result comes back to Your work.");
        await act(async () => { fireEvent.keyDown(document.body, { key: "Enter", metaKey: true }); });
        expect(onSend).toHaveBeenCalledWith({ action: "yes", removed: ["g1"], agentId: "codex" });
        expect(onClose).toHaveBeenCalled();
    });

    it("keeps a topic's proposal as proposed: nothing removable", () => {
        render(<TodayHandOffSheet item={item({ source: "topic", id: "topic:atlas:p1" })} agents={agents} onSend={vi.fn()} onClose={vi.fn()} />);
        expect(screen.queryByRole("button", { name: /Remove/ })).toBeNull();
    });

    it("hands work to an agent that hasn't joined through Work on it, then records it", async () => {
        const onSend = vi.fn().mockResolvedValueOnce("agentAway").mockResolvedValueOnce(null);
        const onClose = vi.fn();
        mocks.prepare.mockResolvedValue({ taskId: "t1", text: "Write the test plan.", agents: [{ id: "claude", name: "Claude", methods: ["open_copy"], default: true }] });
        mocks.sendWorkOnIt.mockResolvedValue({ recordId: "h1", method: "open_copy", copied: true });
        render(<TodayHandOffSheet item={item({ yes: { does: "agent", line: "", kind: "else" } })} twoStep agents={agents} onSend={onSend} onClose={onClose} />);
        expect(screen.getByText("Hand it to Claude")).toBeInTheDocument();
        await act(async () => { fireEvent.click(screen.getByTestId("handoff-send")); });
        expect(onSend).toHaveBeenNthCalledWith(1, { action: "send", removed: [], agentId: "claude" });
        expect(mocks.prepare).toHaveBeenCalledWith(expect.objectContaining({ sourceKind: "work_item", sourceId: "brief:b1:offer:o1", kind: "else" }));
        await act(async () => { fireEvent.click(await screen.findByRole("button", { name: /Send to Claude/ })); });
        expect(mocks.sendWorkOnIt).toHaveBeenCalledWith("t1", "claude", "open_copy");
        expect(onSend).toHaveBeenNthCalledWith(2, { action: "send", recordId: "h1" });
        expect(onClose).toHaveBeenCalled();
    });

    it("says the agent couldn't start, quotes it, and keeps the work for Send again", async () => {
        const onSend = vi.fn().mockResolvedValueOnce("failed").mockResolvedValueOnce(null);
        const onClose = vi.fn();
        render(<TodayHandOffSheet item={item()} twoStep failedText="codex: not signed in" agents={agents} onSend={onSend} onClose={onClose} />);
        await act(async () => { fireEvent.click(screen.getByTestId("handoff-send")); });
        expect(screen.getByTestId("handoff-couldnt")).toHaveTextContent("Codex couldn't start");
        expect(screen.getByTestId("handoff-couldnt")).toHaveTextContent("Nothing was sent; the work is still in Your work.");
        expect(screen.getByTestId("handoff-agent-said")).toHaveTextContent("codex: not signed in");
        expect(onClose).not.toHaveBeenCalled();
        expect(screen.getByTestId("handoff-send")).toHaveTextContent("Send again");
        await act(async () => { fireEvent.click(screen.getByTestId("handoff-send")); });
        expect(onClose).toHaveBeenCalled();
    });

    it("only sends a Yes already recorded", async () => {
        const onSend = vi.fn().mockResolvedValue(null);
        render(<TodayHandOffSheet item={item()} twoStep agents={agents} onSend={onSend} onClose={vi.fn()} />);
        await act(async () => { fireEvent.click(screen.getByTestId("handoff-send")); });
        expect(onSend).toHaveBeenCalledWith({ action: "send", removed: [], agentId: "codex" });
    });

    it("sends the person's edit to another agent picked for this hand-off", async () => {
        const onSend = vi.fn().mockResolvedValue(null);
        render(<TodayHandOffSheet item={item()} edited="My version." agents={agents} onSend={onSend} onClose={vi.fn()} />);
        fireEvent.click(screen.getByTestId("handoff-change-agent"));
        fireEvent.click(screen.getByRole("button", { name: "Claude Code" }));
        expect(screen.getByText("Hand it to Claude Code")).toBeInTheDocument();
        expect(screen.getByTestId("handoff-text")).toHaveTextContent("My version.");
        await act(async () => { fireEvent.click(screen.getByTestId("handoff-send")); });
        expect(onSend).toHaveBeenCalledWith({ action: "edit", before: "Write the test plan for Atlas. Don't change any code.", after: "My version.", removed: [], agentId: "claude-code" });
    });

    it("stays open and says why when the answer didn't go through", async () => {
        const onClose = vi.fn();
        render(<TodayHandOffSheet item={item()} agents={agents} onSend={vi.fn().mockResolvedValue("changed")} onClose={onClose} />);
        await act(async () => { fireEvent.click(screen.getByTestId("handoff-send")); });
        expect(screen.getByTestId("handoff-error")).toHaveTextContent("This item was answered or changed.");
        expect(onClose).not.toHaveBeenCalled();
    });

    it("uses the connected wording only for a connected agent", () => {
        render(<TodayHandOffSheet item={item()} agents={{ ...agents, choices: { code: "claude-code", else: "claude" } }} onSend={vi.fn()} onClose={vi.fn()} />);
        expect(screen.getByTestId("handoff-subtitle")).toHaveTextContent("Your agent for code");
        expect(screen.getByTestId("handoff-subtitle")).not.toHaveTextContent("connected on this computer");
    });

    it("says the agents couldn't be read, and Try again reads them again", () => {
        const onRetry = vi.fn();
        render(<TodayHandOffSheet item={item()} agents={null} agentsFailed onRetryAgents={onRetry} onSend={vi.fn()} onClose={vi.fn()} />);
        expect(screen.getByTestId("handoff-agents-failed")).toHaveTextContent("Couldn't read your agents.");
        fireEvent.click(screen.getByText("Try again"));
        expect(onRetry).toHaveBeenCalled();
    });

    it("sends nothing until the agents are known", async () => {
        const onSend = vi.fn();
        render(<TodayHandOffSheet item={item()} agents={null} onSend={onSend} onClose={vi.fn()} />);
        expect(screen.getByTestId("handoff-send")).toBeDisabled();
        await act(async () => { fireEvent.keyDown(document.body, { key: "Enter", metaKey: true }); });
        expect(onSend).not.toHaveBeenCalled();
    });
});
