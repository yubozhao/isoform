import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import i18n from "@/i18n";
import type { CLIToolStatus, DependencyStatus, MagpieInstallView } from "@/lib/wails-shim-app";
import en from "@/i18n/locales/en/settings.json";
import zh from "@/i18n/locales/zh-CN/settings.json";
import componentSource from "./tools-on-this-mac.tsx?raw";
import { ToolsOnThisMacCard } from "./tools-on-this-mac";

const mocks = vi.hoisted(() => ({
    check: vi.fn(),
    install: vi.fn(),
}));

vi.mock("@/lib/wails-shim-app", () => ({
    CheckDependencies: mocks.check,
    InstallCLITool: mocks.install,
}));

const bundled: MagpieInstallView = {
    kind: "bundled",
    version: "0.9.1",
    binaryPath: "",
    configDir: "",
    serverUrl: "http://127.0.0.1:3425",
    healthy: true,
    activeGroup: "isoform",
    groups: [],
    agentModels: {},
};

function cli(tool: string, overrides: Partial<CLIToolStatus> = {}): CLIToolStatus {
    return { tool, installed: true, version: "2.1.3", logged_in: true, message: "", ...overrides };
}

function deps(overrides: Partial<DependencyStatus> = {}): DependencyStatus {
    return {
        magpie: bundled,
        claude: cli("claude"),
        codex: cli("codex", { installed: false, logged_in: false, version: "" }),
        npm: true,
        ollama: false,
        git: { available: true, path: "/usr/bin/git", recoverable: false },
        ...overrides,
    };
}

async function renderCard(props: { gatewayInUse?: boolean } = {}) {
    const view = render(<ToolsOnThisMacCard {...props} />);
    await screen.findByTestId("tools-on-this-mac");
    return view;
}

describe("ToolsOnThisMacCard", () => {
    beforeEach(async () => {
        vi.resetAllMocks();
        await i18n.changeLanguage("en");
        mocks.check.mockResolvedValue(deps());
    });

    it("shows the gateway in use and each assistant's install and sign-in state", async () => {
        await renderCard();

        const gateway = screen.getByTestId("tools-gateway");
        expect(within(gateway).getByText("Assistant connection")).toBeInTheDocument();
        expect(within(gateway).getByText("Connects Isoform to your assistants on this computer.")).toBeInTheDocument();
        expect(within(gateway).getByText("Running")).toBeInTheDocument();
        // The everyday card keeps the technical words for Advanced.
        expect(screen.getByTestId("tools-on-this-mac")).not.toHaveTextContent(/magpie|gateway/i);

        const claude = screen.getByTestId("tools-claude");
        expect(within(claude).getByText("Claude Code")).toBeInTheDocument();
        expect(within(claude).getByText("Version 2.1.3")).toBeInTheDocument();
        expect(within(claude).getByText("Signed in")).toBeInTheDocument();

        const codex = screen.getByTestId("tools-codex");
        expect(within(codex).getByText("Isoform can install it for you.")).toBeInTheDocument();
        expect(within(codex).getByRole("button", { name: "Install" })).toBeEnabled();
    });

    it("installs a missing assistant and then asks to sign in", async () => {
        mocks.install.mockResolvedValue({
            tool: "codex",
            ok: true,
            output: "added 1 package",
            status: cli("codex", { logged_in: false, version: "0.40.0" }),
        });
        await renderCard();

        fireEvent.click(within(screen.getByTestId("tools-codex")).getByRole("button", { name: "Install" }));
        await waitFor(() => expect(mocks.install).toHaveBeenCalledWith("codex"));

        const codex = screen.getByTestId("tools-codex");
        expect(await within(codex).findByText("Not signed in")).toBeInTheDocument();
        expect(within(codex).getByText("Open Terminal and run codex login to sign in, then check again.")).toBeInTheDocument();
        expect(within(codex).queryByRole("button", { name: "Install" })).not.toBeInTheDocument();
    });

    it("shows the install failure on the row", async () => {
        mocks.install.mockResolvedValue({
            tool: "codex",
            ok: false,
            output: "npm ERR! code EACCES\nnpm ERR! permission denied",
            status: cli("codex", { installed: false, logged_in: false, version: "" }),
        });
        await renderCard();

        fireEvent.click(within(screen.getByTestId("tools-codex")).getByRole("button", { name: "Install" }));
        expect(await screen.findByRole("alert")).toHaveTextContent(
            "Could not install Codex: npm ERR! code EACCES npm ERR! permission denied",
        );
    });

    it("explains that installing needs Node.js when npm is missing", async () => {
        mocks.check.mockResolvedValue(deps({ npm: false }));
        await renderCard();

        const codex = screen.getByTestId("tools-codex");
        expect(within(codex).getByText(/Installing needs Node.js/)).toHaveTextContent("npm install -g @openai/codex");
        expect(within(codex).getByText("Not installed")).toBeInTheDocument();
        expect(within(codex).queryByRole("button", { name: "Install" })).not.toBeInTheDocument();
    });

    it("says when the user's own magpie is not responding", async () => {
        mocks.check.mockResolvedValue(deps({ magpie: { ...bundled, kind: "user", healthy: false } }));
        await renderCard();

        const gateway = screen.getByTestId("tools-gateway");
        expect(within(gateway).getByText("Assistant connection")).toBeInTheDocument();
        expect(within(gateway).getByText("Connects Isoform to your assistants on this computer.")).toBeInTheDocument();
        expect(within(gateway).queryByText(/magpie/i)).not.toBeInTheDocument();
        expect(within(gateway).getByText("Not responding")).toBeInTheDocument();
    });

    it("marks the gateway not in use while the own key is on", async () => {
        await renderCard({ gatewayInUse: false });

        const gateway = screen.getByTestId("tools-gateway");
        expect(within(gateway).getByText("Not in use")).toBeInTheDocument();
        expect(within(gateway).getByText("Not used while your own API key or model is on.")).toBeInTheDocument();
    });

    it("shows a failed check and checks again", async () => {
        mocks.check.mockRejectedValueOnce(new Error("shell timeout")).mockResolvedValueOnce(deps());
        render(<ToolsOnThisMacCard />);

        expect(await screen.findByRole("alert")).toHaveTextContent("Could not check this computer. Try again.");
        fireEvent.click(screen.getByRole("button", { name: "Check again" }));
        expect(await screen.findByTestId("tools-on-this-mac")).toBeInTheDocument();
        expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    });

    it("renders the Chinese copy in the zh-CN locale", async () => {
        await act(async () => {
            await i18n.changeLanguage("zh-CN");
        });
        const view = await renderCard();
        try {
            expect(screen.getByRole("heading", { name: "这台电脑上的工具" })).toBeInTheDocument();
            expect(screen.getByText("助手连接")).toBeInTheDocument();
            expect(screen.getByRole("button", { name: "安装" })).toBeInTheDocument();
        } finally {
            view.unmount();
            await act(async () => {
                await i18n.changeLanguage("en");
            });
        }
    });

    it("keeps every toolsOnThisMac key used by the component in both locales", () => {
        const used = new Set(
            [...componentSource.matchAll(/["'`](toolsOnThisMac\.[A-Za-z0-9]+)["'`]/g)].map(
                (match) => match[1].slice("toolsOnThisMac.".length),
            ),
        );
        const enKeys = Object.keys(en.toolsOnThisMac).sort();
        const zhKeys = Object.keys(zh.toolsOnThisMac).sort();
        expect([...used].sort()).toEqual(enKeys);
        expect(zhKeys).toEqual(enKeys);
        expect(componentSource).not.toMatch(/defaultValue/);
    });
});

it("offers the missing gateway download from Settings after onboarding", async () => {
    await i18n.changeLanguage("en");
    mocks.check.mockResolvedValue(deps({
        magpie: { ...bundled, healthy: false },
        gatewaySetup: { ...bundled, healthy: false, installed: false, required: true, downloadable: true, downloadBytes: 74073600, signedIn: false, plan: "", notFoundYet: false, probedAt: "" },
    }));
    render(<ToolsOnThisMacCard />);
    expect(await screen.findByRole("button", { name: "Download and connect (74 MB)" })).toBeInTheDocument();
    expect(screen.getByTestId("setup-gateway")).toHaveTextContent("separate from voice and memory models");
});
