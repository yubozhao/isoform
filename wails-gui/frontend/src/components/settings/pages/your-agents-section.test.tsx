import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import i18n from "@/i18n";
import enApp from "@/i18n/locales/en/app.json";
import zhApp from "@/i18n/locales/zh-CN/app.json";
import en from "@/i18n/locales/en/settings.json";
import zh from "@/i18n/locales/zh-CN/settings.json";
import { agentRows } from "@/components/agents/agents-model";
import { READ_DEADLINE_MS } from "@/components/settings/settings-failure";
import type { ConnectedAgent, YourAgents } from "@/lib/wails-shim-app";
import { YourAgentsSection } from "./your-agents-section";

const mocks = vi.hoisted(() => ({
    agents: null as unknown as YourAgents,
    connected: [] as ConnectedAgent[],
    getYourAgents: vi.fn(),
    getConnected: vi.fn(),
    setYourAgent: vi.fn(async () => ({})),
    connectHere: vi.fn(async () => ({})),
    disconnect: vi.fn(async () => {}),
    disconnectHere: vi.fn(async () => ({})),
    setLearn: vi.fn(async () => {}),
    modelStatus: { mode: "advanced", accountOrder: [] as string[] },
    provider: "anthropic",
}));

vi.mock("@/lib/wails-shim-app", () => ({
    GetYourAgents: mocks.getYourAgents,
    GetConnectedAgents: mocks.getConnected,
    SetYourAgent: mocks.setYourAgent,
    ConnectAgentHere: mocks.connectHere,
    DisconnectAgent: mocks.disconnect,
    DisconnectAgentHere: mocks.disconnectHere,
    SetLearnFromIt: mocks.setLearn,
    GetModelAccountsStatus: vi.fn(async () => mocks.modelStatus),
    GetAIProviderConfig: vi.fn(async () => ({ provider: mocks.provider })),
}));
vi.mock("@/components/model-accounts-card", () => ({ ModelAccountsSettings: () => null }));
// The Connect sheet has its own suite; here it only has to open.
vi.mock("@/components/agents/ConnectAgentDialog", () => ({
    ConnectAgentDialog: ({ open }: { open: boolean }) => (open ? <div data-testid="connect-sheet" /> : null),
}));

const rowOf = async (name: string) => (await screen.findByText(name, { selector: ".settings-row-title" })).closest(".settings-row") as HTMLElement;

const openMenu = (trigger: Element) => {
    fireEvent.pointerDown(trigger, { button: 0, pointerType: "mouse" });
};

describe("Settings › Your agents", () => {
    beforeEach(async () => {
        await i18n.changeLanguage("en");
        // The walk's shape (2026-11-21): Codex chosen for code, found here and
        // not connected; Claude Code connected here.
        mocks.agents = {
            choices: { code: "codex", else: "" },
            agents: [
                { id: "codex", name: "Codex", kind: "code", reach: "on_computer", found: true, connected: false },
                { id: "claude-code", name: "Claude Code", kind: "code", reach: "connected_computer", found: true, connected: true, connectedHere: true },
                { id: "cursor", name: "Cursor", kind: "code", reach: "on_computer", found: false, connected: false },
                { id: "chatgpt", name: "ChatGPT", kind: "else", reach: "its_app", found: false, connected: false },
                { id: "instinct", name: "Instinct", kind: "else", reach: "by_text", found: false, connected: false },
            ],
        };
        mocks.connected = [];
        mocks.modelStatus = { mode: "advanced", accountOrder: [] };
        mocks.provider = "anthropic";
        mocks.getYourAgents.mockReset().mockImplementation(async () => mocks.agents);
        mocks.getConnected.mockReset().mockImplementation(async () => mocks.connected);
        for (const fn of [mocks.setYourAgent, mocks.connectHere, mocks.disconnect, mocks.disconnectHere, mocks.setLearn]) fn.mockClear();
    });

    afterEach(() => {
        vi.useRealTimers();
    });

    it("has Your jobs and Other agents, and the note at the foot", async () => {
        render(<YourAgentsSection />);
        expect(await screen.findByText(en.yourAgents.jobsTitle)).toBeInTheDocument();
        expect(screen.getByText(en.yourAgents.otherTitle)).toBeInTheDocument();
        expect(en.yourAgents.jobsTitle).toBe("Your jobs");
        expect(en.yourAgents.otherTitle).toBe("Other agents");
        const note = screen.getByText(en.yourAgents.note);
        const connect = screen.getByRole("button", { name: en.yourAgents.connect });
        // The note follows Connect an agent.
        expect(connect.compareDocumentPosition(note) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    });

    it("shows every row the Agents page shows, each Connected or Not connected with where it runs", async () => {
        render(<YourAgentsSection />);
        const pageRows = agentRows(mocks.agents, mocks.connected, [], []);
        expect(pageRows.map((r) => r.name)).toEqual(["Codex", "Claude Code"]);
        const codex = await rowOf("Codex");
        expect(codex).toHaveTextContent("Not connected · on this computer");
        expect(within(codex).getByRole("button", { name: enApp.agents.connectSheet.connectThis })).toBeInTheDocument();
        const claudeCode = await rowOf("Claude Code");
        expect(claudeCode).toHaveTextContent("Connected · on this computer");
        expect(within(claudeCode).getByRole("button", { name: en.yourAgents.disconnect })).toBeInTheDocument();
        expect(screen.queryByText("Cursor", { selector: ".settings-row-title" })).not.toBeInTheDocument();
    });

    it("shows each job's agent with Change, and an unchosen job says none is connected", async () => {
        render(<YourAgentsSection />);
        expect(await screen.findByText("Codex", { selector: "[data-your-agents-chosen=code]" })).toBeInTheDocument();
        expect(screen.getByText(en.yourAgents.noneChosen)).toBeInTheDocument();
        expect(document.querySelector("[data-your-agents-change=code]")).toHaveTextContent(en.yourAgents.change);
    });

    it("puts a coding agent found here and not connected under Found on this computer, with Connect and no Remove", async () => {
        render(<YourAgentsSection />);
        const codex = await rowOf("Codex");
        expect(codex.closest("[data-your-agents-found]")).not.toBeNull();
        expect(screen.getByText(enApp.agents.connectSheet.found)).toBeInTheDocument();
        expect((await rowOf("Claude Code")).closest("[data-your-agents-found]")).toBeNull();
        expect(within(codex).queryByRole("button", { name: "Remove" })).toBeNull();
    });

    it("says when an agent is also Isoform's own assistant", async () => {
        mocks.agents.choices.code = "claude-code";
        mocks.modelStatus = { mode: "managed", accountOrder: ["claude", "codex"] };
        render(<YourAgentsSection />);
        expect(await screen.findByText("Claude Code · also your agent for code")).toBeInTheDocument();
        expect(document.querySelectorAll("[data-your-agents-also]")).toHaveLength(1);
    });

    it("Change calls SetYourAgent with the picked agent and says who takes over", async () => {
        render(<YourAgentsSection />);
        await screen.findByText("Codex", { selector: "[data-your-agents-chosen=code]" });
        openMenu(document.querySelector("[data-your-agents-change=code]")!);
        fireEvent.click(await screen.findByRole("menuitem", { name: "Claude Code" }));
        await waitFor(() => expect(mocks.setYourAgent).toHaveBeenCalledWith("code", "claude-code"));
        expect(await screen.findByText("Codex does your code work now. Claude Code takes over; Codex stays connected.")).toBeInTheDocument();
    });

    it("Change with nobody chosen says nothing about taking over", async () => {
        mocks.agents.agents.push({ id: "claude", name: "Claude", kind: "else", reach: "its_app", found: true, connected: false });
        render(<YourAgentsSection />);
        await screen.findByText(en.yourAgents.noneChosen);
        openMenu(document.querySelector("[data-your-agents-change=else]")!);
        fireEvent.click(await screen.findByRole("menuitem", { name: "Claude" }));
        await waitFor(() => expect(mocks.setYourAgent).toHaveBeenCalledWith("else", "claude"));
        expect(document.querySelector("[data-your-agents-takes-over]")).toBeNull();
    });

    it("Connect on a coding agent here asks once, then connects it", async () => {
        render(<YourAgentsSection />);
        fireEvent.click(within(await rowOf("Codex")).getByRole("button", { name: enApp.agents.connectSheet.connectThis }));
        const dialog = await screen.findByRole("dialog");
        expect(mocks.connectHere).not.toHaveBeenCalled();
        fireEvent.click(within(dialog).getByRole("button", { name: enApp.agents.connectSheet.connectThis }));
        await waitFor(() => expect(mocks.connectHere).toHaveBeenCalledWith("codex"));
    });

    it("Disconnect asks once before it goes", async () => {
        render(<YourAgentsSection />);
        fireEvent.click((await rowOf("Claude Code")).querySelector("[data-your-agents-disconnect]")!);
        const dialog = await screen.findByRole("dialog");
        expect(within(dialog).getByText("Disconnect Claude Code?")).toBeInTheDocument();
        expect(within(dialog).getByText(en.yourAgents.disconnectBody)).toBeInTheDocument();
        expect(mocks.disconnectHere).not.toHaveBeenCalled();
        fireEvent.click(within(dialog).getByRole("button", { name: en.yourAgents.disconnect }));
        await waitFor(() => expect(mocks.disconnectHere).toHaveBeenCalledWith("claude-code"));
        expect(mocks.disconnect).not.toHaveBeenCalled();
    });

    it("disconnects an agent that joined by link through its connection, and says where it runs", async () => {
        mocks.connected = [{ id: "ag_1", name: "Muse", device: "phone", mayOpen: "", joinedAt: 1 }];
        mocks.agents.agents.push({ id: "ag_1", name: "Muse", kind: "else", reach: "connected", found: false, connected: true });
        render(<YourAgentsSection />);
        const row = await rowOf("Muse");
        expect(row).toHaveTextContent("Connected · phone");
        fireEvent.click(row.querySelector("[data-your-agents-disconnect]")!);
        fireEvent.click(within(await screen.findByRole("dialog")).getByRole("button", { name: en.yourAgents.disconnect }));
        await waitFor(() => expect(mocks.disconnect).toHaveBeenCalledWith("ag_1"));
    });

    it("a failing read shows the failure line, and Try again reloads", async () => {
        mocks.getYourAgents.mockRejectedValueOnce(new Error("store busy"));
        render(<YourAgentsSection />);
        const line = await screen.findByRole("alert");
        expect(line).toHaveTextContent(en.yourAgents.loadFailed);
        fireEvent.click(within(line).getByRole("button", { name: en.settingRead.retry }));
        expect(await rowOf("Codex")).toBeInTheDocument();
        expect(screen.queryByRole("alert")).not.toBeInTheDocument();
        expect(mocks.getYourAgents).toHaveBeenCalledTimes(2);
    });

    it("a read that never answers shows the failure line after the deadline, not an empty card", async () => {
        vi.useFakeTimers({ shouldAdvanceTime: true });
        mocks.getYourAgents.mockImplementationOnce(() => new Promise(() => {}));
        render(<YourAgentsSection />);
        expect(document.querySelector("[data-settings-placeholder]")).not.toBeNull();
        expect(screen.queryByRole("alert")).not.toBeInTheDocument();
        await act(async () => {
            vi.advanceTimersByTime(READ_DEADLINE_MS);
        });
        const line = await screen.findByRole("alert");
        expect(line).toHaveTextContent(en.yourAgents.loadFailed);
        fireEvent.click(within(line).getByRole("button", { name: en.settingRead.retry }));
        expect(await rowOf("Codex")).toBeInTheDocument();
    });

    it("shows Your agents even when the connected list fails", async () => {
        mocks.getConnected.mockRejectedValueOnce(new Error("no"));
        render(<YourAgentsSection />);
        expect(await rowOf("Claude Code")).toBeInTheDocument();
        expect(screen.getByRole("alert")).toHaveTextContent(en.yourAgents.loadFailed);
    });

    it("opens Connect an agent", async () => {
        render(<YourAgentsSection />);
        await rowOf("Codex");
        fireEvent.click(await screen.findByRole("button", { name: en.yourAgents.connect }));
        expect(screen.getByTestId("connect-sheet")).toBeInTheDocument();
    });

    it("keeps both locales in step, without Their jobs or Asked at your first Yes", () => {
        expect(Object.keys(zh.yourAgents).sort()).toEqual(Object.keys(en.yourAgents).sort());
        expect(Object.keys(zhApp.agents.state).sort()).toEqual(Object.keys(enApp.agents.state).sort());
        expect(Object.keys(zhApp.agents.where).sort()).toEqual(Object.keys(enApp.agents.where).sort());
        for (const locale of [en, zh]) {
            const text = JSON.stringify(locale.yourAgents);
            expect(text).not.toContain("Their jobs");
            expect(text).not.toContain("Asked at your first Yes");
            expect(locale.yourAgents).not.toHaveProperty("notChosen");
        }
        expect(JSON.stringify(zh.yourAgents)).not.toContain("它们的分工");
    });

    it("says Learn from it on or off for agents on this computer, with the same switch, and only on this computer for others", async () => {
        mocks.agents = {
            choices: { code: "", else: "" },
            agents: [
                { id: "claude-code", name: "Claude Code", kind: "code", reach: "connected_computer", found: true, connected: true, connectedHere: true, learnFromIt: true },
                { id: "codex", name: "Codex", kind: "code", reach: "connected_computer", found: true, connected: true, connectedHere: true, learnFromIt: false },
                { id: "ag_x", name: "Ana's Helper", kind: "else", reach: "connected", found: false, connected: true },
            ],
        };
        mocks.connected = [{ id: "ag_x", name: "Ana's Helper", device: "the cloud", mayOpen: "the web", joinedAt: 1 }];
        render(<YourAgentsSection />);
        const cc = await rowOf("Claude Code");
        expect(within(cc).getByText(en.yourAgents.learnOn)).toBeInTheDocument();
        const codex = await rowOf("Codex");
        expect(within(codex).getByText(en.yourAgents.learnOff)).toBeInTheDocument();
        const helper = await rowOf("Ana's Helper");
        expect(within(helper).getByText(en.yourAgents.learnOnlyHere)).toBeInTheDocument();
        expect(helper.querySelector("[data-learn-switch]")).toBeNull();
        fireEvent.click(codex.querySelector('[data-learn-switch="codex"]')!);
        await waitFor(() => expect(mocks.setLearn).toHaveBeenCalledWith("codex", true));
        // Off asks once first.
        fireEvent.click(cc.querySelector('[data-learn-switch="claude-code"]')!);
        expect(mocks.setLearn).not.toHaveBeenCalledWith("claude-code", false);
        fireEvent.click(await screen.findByRole("button", { name: enApp.agents.learn.offConfirm }));
        await waitFor(() => expect(mocks.setLearn).toHaveBeenCalledWith("claude-code", false));
        for (const locale of [en, zh]) for (const k of ["learnOn", "learnOff", "learnOnlyHere"]) expect(locale.yourAgents).toHaveProperty(k);
    });
});
