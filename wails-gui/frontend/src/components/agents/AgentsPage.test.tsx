import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import i18n from "@/i18n";
import en from "@/i18n/locales/en/app.json";

const mocks = vi.hoisted(() => ({
    agents: vi.fn(), connected: vi.fn(), handoffs: vi.fn(), results: vi.fn(), openFile: vi.fn(), disconnect: vi.fn(), setAgent: vi.fn(),
    start: vi.fn(), join: vi.fn(), answer: vi.fn(), cancel: vi.fn(), copy: vi.fn(),
}));
vi.mock("@/lib/wails-shim-app", () => ({
    GetYourAgents: mocks.agents, GetConnectedAgents: mocks.connected, GetWorkOnItHandoffs: mocks.handoffs, GetTaskLinkResults: mocks.results,
    OpenAgentFile: mocks.openFile, DisconnectAgent: mocks.disconnect, DisconnectAgentHere: mocks.disconnect, SetYourAgent: mocks.setAgent, ConnectAgentHere: vi.fn(),
    StartAgentJoin: mocks.start, GetAgentJoin: mocks.join, AnswerAgentJoin: mocks.answer, CancelAgentJoin: mocks.cancel, NativeClipboardSetText: mocks.copy,
}));

import { AgentsPage } from "./AgentsPage";

const now = new Date(2026, 10, 15, 18, 45);
const today = (h: number, m: number) => new Date(2026, 10, 15, h, m).getTime();

beforeEach(async () => {
    vi.resetAllMocks();
    await i18n.changeLanguage("en");
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
    mocks.start.mockResolvedValue({ message: "Join my Isoform: read http://127.0.0.1:8792/j/abc and follow the steps there.", link: "http://127.0.0.1:8792/j/abc", expiresAt: 0, local: true });
    mocks.join.mockResolvedValue({ state: "waiting", expiresAt: 0 });
});

describe("Agents page", () => {
    it("shows your agents, today's hand-off and result, and what they made", async () => {
        render(<AgentsPage now={() => now} />);
        expect(await screen.findByRole("heading", { name: en.agents.title })).toBeInTheDocument();
        const yours = screen.getByRole("region", { name: en.agents.yours });
        await within(yours).findByText("Codex");
        expect(within(yours).getByText(en.agents.job.forCodeHere)).toBeInTheDocument();
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
        fireEvent.click(screen.getByRole("button", { name: en.agents.disconnect }));
        await waitFor(() => expect(mocks.disconnect).toHaveBeenCalledWith("ag_co"));
    });

    it("opens Tasks from the page", async () => {
        const onOpenTasks = vi.fn();
        render(<AgentsPage now={() => now} onOpenTasks={onOpenTasks} />);
        fireEvent.click(await screen.findByRole("button", { name: en.agents.allTasks }));
        expect(onOpenTasks).toHaveBeenCalled();
    });

    it("says plainly when there's nothing yet", async () => {
        mocks.agents.mockResolvedValue({ choices: { code: "", else: "" }, agents: [] });
        mocks.connected.mockResolvedValue([]);
        mocks.handoffs.mockResolvedValue([]);
        mocks.results.mockResolvedValue([]);
        render(<AgentsPage now={() => now} />);
        expect(await screen.findByText(en.agents.empty.agents)).toBeInTheDocument();
        expect(screen.getByText(en.agents.empty.between)).toBeInTheDocument();
        expect(screen.getByText(en.agents.empty.made)).toBeInTheDocument();
    });

    it("says it couldn't read, with Try again", async () => {
        mocks.agents.mockRejectedValue(new Error("down"));
        render(<AgentsPage now={() => now} />);
        expect(await screen.findByText(en.agents.failed)).toBeInTheDocument();
        mocks.agents.mockResolvedValue({ choices: { code: "", else: "" }, agents: [] });
        fireEvent.click(screen.getByRole("button", { name: en.agents.retry }));
        await waitFor(() => expect(screen.queryByText(en.agents.failed)).not.toBeInTheDocument());
    });
});
