import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import i18n from "@/i18n";
import en from "@/i18n/locales/en/settings.json";
import zh from "@/i18n/locales/zh-CN/settings.json";
import { OtherAIAppsPage } from "@/components/settings/pages/other-ai-apps-page";

const mocks = vi.hoisted(() => ({
    openURL: vi.fn(),
    list: vi.fn(),
}));

vi.mock("@/lib/wails-shim-app", () => ({
    GetMCPReadConnections: mocks.list,
    ConnectMCPReadAssistant: vi.fn(),
    RevokeMCPReadAssistant: vi.fn(),
    OpenExternalURL: mocks.openURL,
    GetFrontStatus: vi.fn().mockResolvedValue({ enabled: false, running: false, hostname: "" }),
    SetFrontEnabled: vi.fn(),
}));

const connections = [
    { client: "claude-code", name: "Claude Code", command: "claude mcp add isoform", connected: false, authorized: false, available: true },
    { client: "codex", name: "Codex", command: "codex mcp add isoform", connected: true, authorized: true, available: true },
];

// Settings review v1.0.0 ST7 / S052.
describe("Other AI apps", () => {
    beforeEach(async () => {
        vi.clearAllMocks();
        window.localStorage.clear();
        mocks.list.mockResolvedValue(connections);
        mocks.openURL.mockResolvedValue(undefined);
        await i18n.changeLanguage("en");
    });

    it("holds the real Connect rows and the by-hand setup, with no install prompt", async () => {
        render(<OtherAIAppsPage />);

        expect(screen.getByRole("heading", { name: "Claude Code and Codex" })).toBeInTheDocument();
        const claude = await screen.findByRole("article", { name: "Claude Code" });
        expect(within(claude).getByRole("button", { name: "Connect" })).toBeInTheDocument();
        expect(within(screen.getByRole("article", { name: "Codex" })).getByRole("button", { name: "Revoke" })).toBeInTheDocument();

        expect(screen.getByText("Set it up by hand")).toBeInTheDocument();
        expect(screen.getByText("The steps and commands are in Isoform's public repository.")).toBeInTheDocument();
        fireEvent.click(screen.getByRole("button", { name: "Open on GitHub" }));
        expect(mocks.openURL).toHaveBeenCalledWith("https://github.com/yubozhao/isoform/blob/main/docs/isoform-mcp-server.md");

        expect(screen.queryByRole("button", { name: "Copy install prompt" })).not.toBeInTheDocument();
        expect(document.body.textContent).not.toMatch(/yansu|Cursor/i);

        fireEvent.click(screen.getByRole("button", { name: /Advanced/ }));
        expect(screen.getByRole("heading", { name: "What other assistants can recall" })).toBeInTheDocument();
        expect(screen.queryByText("Examples")).not.toBeInTheDocument();
        expect(screen.queryByText("What the install prompt does")).not.toBeInTheDocument();
        expect(document.body.textContent).not.toMatch(/yansu|Cursor/i);
    });

    it("opens the integration instructions file on the public repo, never the repo root or isoform-dev (S052)", async () => {
        render(<OtherAIAppsPage />);

        fireEvent.click(screen.getByRole("button", { name: "Open on GitHub" }));
        await waitFor(() => expect(mocks.openURL).toHaveBeenCalledTimes(1));
        const [url] = mocks.openURL.mock.calls[0] as [string];
        expect(url).toBe("https://github.com/yubozhao/isoform/blob/main/docs/isoform-mcp-server.md");
        expect(url).not.toMatch(/isoform-dev/);
        expect(url).toMatch(/\/blob\/main\/docs\/isoform-mcp-server\.md$/);
    });

    it("shows a browser that didn't open as a copper line with Try again", async () => {
        mocks.openURL.mockRejectedValueOnce(new Error("no browser")).mockResolvedValueOnce(undefined);
        render(<OtherAIAppsPage />);
        fireEvent.click(screen.getByRole("button", { name: "Open on GitHub" }));
        const alert = await screen.findByRole("alert");
        expect(alert).toHaveTextContent("Couldn't open your browser.");
        expect(alert).toHaveClass("text-copper");
        fireEvent.click(within(alert).getByRole("button", { name: "Try again" }));
        await waitFor(() => expect(mocks.openURL).toHaveBeenCalledTimes(2));
        await waitFor(() => expect(screen.queryByRole("alert")).not.toBeInTheDocument());
    });

    it("renders in Chinese", async () => {
        await i18n.changeLanguage("zh-CN");
        window.localStorage.setItem("yansu.settings.advanced.use-yansu-anywhere", "1");
        render(<OtherAIAppsPage />);
        expect(screen.getByRole("heading", { name: "Claude Code 和 Codex" })).toBeInTheDocument();
        expect(screen.getByRole("button", { name: "在 GitHub 上打开" })).toBeInTheDocument();
        expect(screen.getByRole("heading", { name: "其他助手能调用哪些内容" })).toBeInTheDocument();
        expect(document.body.textContent).not.toMatch(/yansu|Cursor/i);
    });

    it("words the page subtitle as decided, and no visible string says yansu", () => {
        expect(en.pages["use-yansu-anywhere"].subtitle).toBe("Let Claude Code and Codex recall what Isoform has learned about your work.");
        expect(zh.pages["use-yansu-anywhere"].subtitle).toBe("让 Claude Code 和 Codex 也能调用 Isoform 对你工作的了解。");
        for (const tree of [en.assistantMcp, zh.assistantMcp])
            expect(JSON.stringify(tree)).not.toMatch(/yansu|Cursor/i);
    });
});
