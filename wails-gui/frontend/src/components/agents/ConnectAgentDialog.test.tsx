import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import i18n from "@/i18n";
import en from "@/i18n/locales/en/app.json";

const mocks = vi.hoisted(() => ({ start: vi.fn(), startForApp: vi.fn(), address: vi.fn(), join: vi.fn(), answer: vi.fn(), cancel: vi.fn(), copy: vi.fn(), connectHere: vi.fn(), open: vi.fn() }));
vi.mock("@/lib/wails-shim-app", () => ({
    StartAgentJoin: mocks.start, StartAgentJoinForApp: mocks.startForApp, GetAgentAddress: mocks.address,
    GetAgentJoin: mocks.join, AnswerAgentJoin: mocks.answer, CancelAgentJoin: mocks.cancel, NativeClipboardSetText: mocks.copy,
    ConnectAgentHere: mocks.connectHere,
}));
vi.mock("../../../wailsjs/runtime/runtime", () => ({ BrowserOpenURL: mocks.open }));

import { ConnectAgentDialog, JOIN_POLL_MS } from "./ConnectAgentDialog";

const message = "Join my Isoform: read https://abc.agents.isoform.ai/j/code and follow the steps there.";
const agents = {
    choices: { code: "", else: "" },
    agents: [
        { id: "claude-code", name: "Claude Code", kind: "code" as const, reach: "on_computer" as const, found: true, connected: false },
        { id: "codex", name: "Codex", kind: "code" as const, reach: "connected_computer" as const, found: true, connected: true },
        { id: "cursor", name: "Cursor", kind: "code" as const, reach: "on_computer" as const, found: true, connected: false },
        { id: "other", name: "Other", kind: "code" as const, reach: "on_computer" as const, found: false, connected: false },
    ],
};

beforeEach(async () => {
    vi.resetAllMocks();
    vi.useFakeTimers({ shouldAdvanceTime: true });
    await i18n.changeLanguage("en");
    mocks.start.mockResolvedValue({ message, link: "https://abc.agents.isoform.ai/j/code", expiresAt: 0, local: false, mcpAddress: "https://abc.agents.isoform.ai/mcp?join=key" });
    mocks.startForApp.mockResolvedValue({ message: "Join my Isoform: read https://abc.agents.isoform.ai/j/addr and follow the steps there.", link: "https://abc.agents.isoform.ai/j/addr", expiresAt: 0, local: false, mcpAddress: "https://abc.agents.isoform.ai/mcp?join=key" });
    mocks.address.mockResolvedValue({ open: false, hostname: "", service: "", apps: [], needsTerms: true, unexpectedCertificates: [] });
    mocks.join.mockResolvedValue({ state: "waiting", expiresAt: 0 });
    mocks.copy.mockResolvedValue(undefined);
    mocks.answer.mockResolvedValue(undefined);
});
afterEach(() => vi.useRealTimers());

describe("Connect an agent", () => {
    it("names no agent that can't connect, and no Settings section the design dropped (agents-join v1.1.0, settings v1.1.0 rule 1)", async () => {
        const zh = (await import("@/i18n/locales/zh-CN/app.json")).default;
        for (const sheet of [en.agents.connectSheet, zh.agents.connectSheet]) {
            expect(sheet.anyOtherHelp).not.toMatch(/Muse/);
            expect(sheet.localOnly).not.toMatch(/Other AI apps|其他 AI 应用/);
        }
        mocks.start.mockResolvedValue({ message, link: "http://127.0.0.1/j/code", expiresAt: 0, local: true, mcpAddress: "http://127.0.0.1/mcp?join=key" });
        render(<ConnectAgentDialog open onOpenChange={() => undefined} agents={agents} onConnected={() => undefined} />);
        expect(await screen.findByText(en.agents.connectSheet.localOnly)).toBeInTheDocument();
        expect(screen.getByText(en.agents.connectSheet.anyOtherHelp)).toBeInTheDocument();
    });

    it("shows the agents found here and the one message, and copies it", async () => {
        render(<ConnectAgentDialog open onOpenChange={() => undefined} agents={agents} onConnected={() => undefined} />);
        expect(await screen.findByText(message)).toBeInTheDocument();
        expect(screen.getByText("Claude Code")).toBeInTheDocument();
        expect(screen.queryByText("Other")).not.toBeInTheDocument();
        expect(screen.getByText(en.agents.job.connected)).toBeInTheDocument();
        // Cursor has no one-click connection: its Connect points the message at it.
        const cursorRow = document.querySelector('[data-connect-here="cursor"]') as HTMLElement;
        fireEvent.click(cursorRow.querySelector("button")!);
        expect(screen.getByText(/Paste this into Cursor/)).toBeInTheDocument();
        fireEvent.click(screen.getByRole("button", { name: en.agents.connectSheet.copy }));
        await waitFor(() => expect(mocks.copy).toHaveBeenCalledWith(message));
        expect(screen.getByRole("button", { name: en.agents.connectSheet.copied })).toBeInTheDocument();
        expect(screen.getByText(en.agents.connectSheet.waiting)).toBeInTheDocument();
    });

    it("asks the person to Allow with the agent's check number, then says it's connected", async () => {
        const onConnected = vi.fn();
        render(<ConnectAgentDialog open onOpenChange={() => undefined} agents={agents} onConnected={onConnected} />);
        await screen.findByText(message);
        mocks.join.mockResolvedValue({ state: "requested", expiresAt: 0, name: "Grok", device: "the cloud", mayOpen: "the web only", check: "4821" });
        await act(async () => { vi.advanceTimersByTime(JOIN_POLL_MS + 10); });
        expect(await screen.findByText("Grok is asking to join")).toBeInTheDocument();
        expect(screen.getByText("4821")).toBeInTheDocument();
        expect(screen.getByText("From the cloud · to work in the web only")).toBeInTheDocument();
        fireEvent.click(screen.getByRole("button", { name: "Yes, it shows 4821" }));
        await waitFor(() => expect(mocks.answer).toHaveBeenCalledWith(true));
        expect(await screen.findByText("Grok is connected")).toBeInTheDocument();
        // The agent's token read spends the join; the sheet keeps saying it's connected.
        mocks.join.mockResolvedValue(null);
        await act(async () => { vi.advanceTimersByTime(JOIN_POLL_MS * 2); });
        expect(screen.getByText("Grok is connected")).toBeInTheDocument();
    });

    it("connects Claude Code here with one click after asking once", async () => {
        const onConnected = vi.fn();
        mocks.connectHere.mockResolvedValueOnce(agents).mockRejectedValueOnce(new Error("signed out"));
        render(<ConnectAgentDialog open onOpenChange={() => undefined} agents={agents} onConnected={onConnected} />);
        await screen.findByText(message);
        const row = () => document.querySelector('[data-connect-here="claude-code"]') as HTMLElement;
        fireEvent.click(row().querySelector("button")!);
        expect(screen.getByText(/Connect Claude Code\? It will receive the work you say yes to/)).toBeInTheDocument();
        fireEvent.click(row().querySelector("[data-connect-here-yes]")!);
        await waitFor(() => expect(mocks.connectHere).toHaveBeenCalledWith("claude-code"));
        expect(onConnected).toHaveBeenCalled();
        await waitFor(() => expect(row().textContent).toContain(en.agents.job.connected));
    });

    it("shows each app's own steps, and says plainly when an app can't connect", async () => {
        render(<ConnectAgentDialog open onOpenChange={() => undefined} agents={agents} onConnected={() => undefined} />);
        await screen.findByText(message);
        fireEvent.click(screen.getByRole("button", { name: "Claude" }));
        // Nothing opens until Connect (v1.5.0 G10).
        expect(screen.queryByText("https://abc.agents.isoform.ai/mcp?join=key")).not.toBeInTheDocument();
        expect(mocks.startForApp).not.toHaveBeenCalled();
        fireEvent.click(document.querySelector("[data-connect-address-open]")!);
        await waitFor(() => expect(mocks.startForApp).toHaveBeenCalledWith("claude", true));
        expect(await screen.findByText("https://abc.agents.isoform.ai/mcp?join=key")).toBeInTheDocument();
        expect(screen.getByText(en.agents.connectSheet.steps.claude)).toBeInTheDocument();
        fireEvent.click(screen.getByRole("button", { name: en.agents.connectSheet.copy }));
        await waitFor(() => expect(mocks.copy).toHaveBeenCalledWith("https://abc.agents.isoform.ai/mcp?join=key"));
        fireEvent.click(screen.getByRole("button", { name: "ChatGPT" }));
        expect(screen.getByText(en.agents.connectSheet.chatgptDev)).toBeInTheDocument();
        // The open address serves ChatGPT too, without a second open.
        expect(screen.getByText("https://abc.agents.isoform.ai/mcp?join=key")).toBeInTheDocument();
        expect(mocks.startForApp).toHaveBeenCalledTimes(1);
        fireEvent.click(screen.getByRole("button", { name: "Muse" }));
        expect(screen.getByText("Muse can't connect to Isoform yet. It gets each task written in, and sends results back through a link.")).toBeInTheDocument();
        expect(screen.queryByText("https://abc.agents.isoform.ai/mcp?join=key")).not.toBeInTheDocument();
    });

    it("shows a plain retry line when a link can't be made, and closes an open link on close", async () => {
        mocks.start.mockRejectedValueOnce(new Error("task link listener: address already in use"));
        const onOpenChange = vi.fn();
        const { rerender } = render(<ConnectAgentDialog open onOpenChange={onOpenChange} agents={agents} onConnected={() => undefined} />);
        expect(await screen.findByText(en.agents.actionFailed)).toBeInTheDocument();
        // Agents G2: no switch and not Closed Eyes stop joining, so the old sentence never shows.
        expect(screen.queryByText(/task links/i)).not.toBeInTheDocument();
        expect(document.querySelector("[data-connect-off]")).toBeNull();
        fireEvent.click(screen.getByRole("button", { name: en.agents.retry }));
        expect(await screen.findByText(message)).toBeInTheDocument();
        fireEvent.keyDown(document.activeElement ?? document.body, { key: "Escape" });
        await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false));
        // Closing the sheet closes the link still waiting.
        rerender(<ConnectAgentDialog open={false} onOpenChange={onOpenChange} agents={agents} onConnected={() => undefined} />);
        await waitFor(() => expect(mocks.cancel).toHaveBeenCalled());
    });

    it("shows the Let's Encrypt agreement above Connect only before the first acceptance (v1.5.0 G10)", async () => {
        const { unmount } = render(<ConnectAgentDialog open onOpenChange={() => undefined} agents={agents} onConnected={() => undefined} />);
        await screen.findByText(message);
        fireEvent.click(screen.getByRole("button", { name: "Gemini" }));
        const line = await waitFor(() => {
            const el = document.querySelector('[data-connect-address-line="terms"]') as HTMLElement;
            expect(el).not.toBeNull();
            return el;
        });
        expect(line.textContent).toBe("This opens your agent address. What passes through it is listed in What leaves. The address gets a certificate from Let's Encrypt, and connecting means you accept their Subscriber Agreement ›.");
        const agreement = line.querySelector("button")!;
        expect(agreement.textContent).toBe("Subscriber Agreement ›");
        fireEvent.click(agreement);
        expect(mocks.open).toHaveBeenCalledWith("https://letsencrypt.org/repository/");
        expect(screen.queryByRole("checkbox")).toBeNull();
        unmount();

        mocks.address.mockResolvedValue({ open: false, hostname: "", service: "", apps: [], needsTerms: false, unexpectedCertificates: [] });
        render(<ConnectAgentDialog open onOpenChange={() => undefined} agents={agents} onConnected={() => undefined} />);
        await screen.findByText(message);
        fireEvent.click(screen.getByRole("button", { name: "Gemini" }));
        await waitFor(() => expect(document.querySelector('[data-connect-address-line="plain"]')?.textContent).toBe(en.agents.connectSheet.addressLine));
        expect(screen.queryByText(/Let's Encrypt/)).toBeNull();
        fireEvent.click(document.querySelector("[data-connect-address-open]")!);
        await waitFor(() => expect(mocks.startForApp).toHaveBeenCalledWith("gemini", false));
    });

    it("asks where any other agent runs; on this computer opens no address, somewhere else does on Connect (v1.5.0 G11)", async () => {
        render(<ConnectAgentDialog open onOpenChange={() => undefined} agents={agents} onConnected={() => undefined} />);
        await screen.findByText(message);
        expect(screen.getByText(en.agents.connectSheet.whereRuns)).toBeInTheDocument();
        const here = document.querySelector('[data-connect-where-choice="here"]') as HTMLElement;
        expect(here.getAttribute("aria-pressed")).toBe("true");
        expect(document.querySelector("[data-connect-address-open]")).toBeNull();
        fireEvent.click(document.querySelector('[data-connect-where-choice="elsewhere"]')!);
        expect(screen.getByText(en.agents.connectSheet.runsElsewhereLine)).toBeInTheDocument();
        expect(screen.queryByText(message)).toBeNull();
        expect(mocks.startForApp).not.toHaveBeenCalled();
        fireEvent.click(document.querySelector("[data-connect-address-open]")!);
        await waitFor(() => expect(mocks.startForApp).toHaveBeenCalledWith("other", true));
        expect(await screen.findByText(/abc\.agents\.isoform\.ai\/j\/addr/)).toBeInTheDocument();
        // Back to this computer: a new private link, and no address asked for.
        mocks.start.mockClear();
        fireEvent.click(here);
        await waitFor(() => expect(mocks.start).toHaveBeenCalledTimes(1));
        expect(mocks.startForApp).toHaveBeenCalledTimes(1);
    });
});
