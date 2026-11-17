import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import i18n from "@/i18n";
import en from "@/i18n/locales/en/settings.json";
import zh from "@/i18n/locales/zh-CN/settings.json";
import type { ConnectedAgent, YourAgents } from "@/lib/wails-shim-app";
import { YourAgentsSection } from "./your-agents-section";

const mocks = vi.hoisted(() => ({
    agents: null as unknown as YourAgents,
    connected: [] as ConnectedAgent[],
    setYourAgent: vi.fn(async () => ({})),
    disconnect: vi.fn(async () => {}),
    disconnectHere: vi.fn(async () => ({})),
}));

vi.mock("@/lib/wails-shim-app", () => ({
    GetYourAgents: vi.fn(async () => mocks.agents),
    GetConnectedAgents: vi.fn(async () => mocks.connected),
    SetYourAgent: mocks.setYourAgent,
    DisconnectAgent: mocks.disconnect,
    DisconnectAgentHere: mocks.disconnectHere,
}));
// The Connect sheet has its own suite; here it only has to open.
vi.mock("@/components/agents/ConnectAgentDialog", () => ({
    ConnectAgentDialog: ({ open }: { open: boolean }) => (open ? <div data-testid="connect-sheet" /> : null),
}));

describe("Settings › Your agents", () => {
    beforeEach(async () => {
        await i18n.changeLanguage("en");
        mocks.agents = {
            choices: { code: "claude_code", else: "" },
            agents: [
                { id: "claude_code", name: "Claude Code", kind: "code", reach: "connected_computer", found: true, connected: true, connectedHere: true },
                { id: "codex", name: "Codex", kind: "code", reach: "on_computer", found: true, connected: false },
                { id: "chatgpt", name: "ChatGPT", kind: "else", reach: "its_app", found: false, connected: false },
            ],
        };
        mocks.connected = [];
        mocks.setYourAgent.mockClear();
        mocks.disconnect.mockClear();
        mocks.disconnectHere.mockClear();
    });

    it("shows the agent for each job, and an unchosen job is asked at the first Yes", async () => {
        render(<YourAgentsSection />);
        const code = await screen.findByRole("combobox", { name: en.yourAgents.forCode });
        await waitFor(() => expect(code).toHaveTextContent("Claude Code"));
        expect(screen.getByRole("combobox", { name: en.yourAgents.forElse })).toHaveTextContent(en.yourAgents.notChosen);
        expect(screen.getByText(en.yourAgents.note)).toBeInTheDocument();
    });

    it("lists connected agents only, and Disconnect asks once before it goes", async () => {
        render(<YourAgentsSection />);
        const row = await screen.findByText("Claude Code", { selector: ".settings-row-title" });
        expect(screen.queryByText("Codex", { selector: ".settings-row-title" })).not.toBeInTheDocument();
        fireEvent.click(row.closest(".settings-row")!.querySelector("[data-your-agents-disconnect]")!);
        const dialog = await screen.findByRole("dialog");
        expect(within(dialog).getByText("Disconnect Claude Code?")).toBeInTheDocument();
        expect(within(dialog).getByText(en.yourAgents.disconnectBody)).toBeInTheDocument();
        expect(mocks.disconnectHere).not.toHaveBeenCalled();
        fireEvent.click(within(dialog).getByRole("button", { name: en.yourAgents.disconnect }));
        await waitFor(() => expect(mocks.disconnectHere).toHaveBeenCalledWith("claude_code"));
        expect(mocks.disconnect).not.toHaveBeenCalled();
    });

    it("disconnects an agent that joined by link through its connection", async () => {
        mocks.connected = [{ id: "ag_1", name: "Muse", device: "phone", mayOpen: "", joinedAt: 1 } as ConnectedAgent];
        mocks.agents.agents.push({ id: "muse", name: "Muse", kind: "else", reach: "connected", found: false, connected: true });
        mocks.connected[0].catalogId = "muse";
        render(<YourAgentsSection />);
        const row = await screen.findByText("Muse", { selector: ".settings-row-title" });
        fireEvent.click(row.closest(".settings-row")!.querySelector("[data-your-agents-disconnect]")!);
        fireEvent.click(within(await screen.findByRole("dialog")).getByRole("button", { name: en.yourAgents.disconnect }));
        await waitFor(() => expect(mocks.disconnect).toHaveBeenCalledWith("ag_1"));
    });

    it("opens Connect an agent", async () => {
        render(<YourAgentsSection />);
        fireEvent.click(await screen.findByRole("button", { name: en.yourAgents.connect }));
        expect(screen.getByTestId("connect-sheet")).toBeInTheDocument();
    });

    it("keeps both locales in step", () => {
        expect(Object.keys(zh.yourAgents).sort()).toEqual(Object.keys(en.yourAgents).sort());
    });
});
