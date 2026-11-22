import { act, render, screen } from "@testing-library/react";
import { fireEvent } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import i18n from "@/i18n";
import type { ReactNode } from "react";
import type { AIProviderConfig, AIProviderPreset } from "@/lib/wails-shim-app";
import { AIProviderSettingsCard, AssistantPage } from "./assistant-page";

const mocks = vi.hoisted(() => ({
    getConfig: vi.fn(),
    listPresets: vi.fn(),
    getMCPConnections: vi.fn(),
    saveConfig: vi.fn(),
    eventsOn: vi.fn(),
    toastMessage: vi.fn(),
    toastSuccess: vi.fn(),
    toastError: vi.fn(),
}));

vi.mock("@/lib/wails-shim-app", () => ({
    GetAIProviderConfig: mocks.getConfig,
    GetFindingProvider: vi.fn(async () => ({ choice: "local", hasOpenAIKey: false, localModelDownloaded: true, localModelBytes: 0 })),
    SetFindingProvider: vi.fn(),
    ListPlugins: vi.fn(async () => []),
    SetBuiltinOn: vi.fn(),
    ListAIProviderPresets: mocks.listPresets,
    SaveAIProviderConfig: mocks.saveConfig,
    SetCachedAIProviderModels: vi.fn(),
    TestAIProvider: vi.fn(),
    BuildProviderTestCurl: vi.fn(),
    ListAIProviderModels: vi.fn(),
    GetMCPReadConnections: mocks.getMCPConnections,
    ConnectMCPReadAssistant: vi.fn(),
    RevokeMCPReadAssistant: vi.fn(),
    GetModelAccountsStatus: vi.fn(async () => ({ mode: "advanced", accountOrder: [] })),
    GetYourAgents: vi.fn(async () => ({ choices: { code: "", else: "" }, agents: [] })),
}));

vi.mock("../../../../wailsjs/runtime/runtime", () => ({
    BrowserOpenURL: vi.fn(),
    EventsOn: mocks.eventsOn,
}));

vi.mock("sonner", () => ({
    toast: { message: mocks.toastMessage, success: mocks.toastSuccess, error: mocks.toastError },
}));


// Your agents has its own suite; here it only has to come first.
vi.mock("./your-agents-section", () => ({
    YourAgentsSection: () => <div data-testid="your-agents" />,
}));

// The page's account cards have their own suite; here they only show the form they were given.
vi.mock("@/components/model-accounts-card", () => ({
    ModelAccountsSettings: ({ ownKeyForm }: { ownKeyForm: ReactNode }) => (
        <div data-testid="model-accounts">{ownKeyForm}</div>
    ),
}));

const openai: AIProviderPreset = {
    id: "openai",
    name: "OpenAI",
    apiFormat: "openai",
    baseUrl: "https://api.openai.com/v1",
    seedModels: ["gpt-4o"],
    apiKeyUrl: "",
    requiresKey: true,
    custom: false,
};

function config(overrides: Partial<AIProviderConfig> = {}): AIProviderConfig {
    return {
        provider: "openai",
        presetId: "openai",
        apiKey: "sk-test",
        baseUrl: "",
        model: "gpt-4o",
        lightModel: "",
        command: "",
        ...overrides,
    };
}

const devTools = vi.hoisted(() => ({ on: true }));
vi.mock("@/components/settings/settings-dev-tools", async (original) => ({
    ...(await original<typeof import("@/components/settings/settings-dev-tools")>()),
    useSettingsDevTools: () => devTools.on,
}));
beforeEach(() => {
    devTools.on = true;
});

describe("AIProviderSettingsCard", () => {
    beforeEach(() => {
        vi.clearAllMocks();
        mocks.eventsOn.mockReturnValue(() => undefined);
        mocks.listPresets.mockResolvedValue([openai]);
        mocks.getConfig.mockResolvedValue(config());
        mocks.saveConfig.mockResolvedValue(undefined);
    });

    it("renders only the form when flat, with no card or header of its own", async () => {
        const { container } = render(<AIProviderSettingsCard flat />);
        expect(await screen.findByText("API key")).toBeInTheDocument();
        expect(screen.queryByRole("heading")).not.toBeInTheDocument();
        expect(container.querySelector(".settings-card")).toBeNull();
        expect(screen.getByRole("button", { name: "Save and apply" })).toBeInTheDocument();
    });

    it("keeps Copy curl command under More options", async () => {
        render(<AIProviderSettingsCard flat />);
        await screen.findByText("API key");
        expect(screen.queryByRole("button", { name: "Copy curl command" })).not.toBeInTheDocument();
        fireEvent.click(screen.getByRole("button", { name: "More options" }));
        expect(screen.getByRole("button", { name: "Copy curl command" })).toBeInTheDocument();
    });

    it("opens More options by itself when the saved CLI runs without the sandbox", async () => {
        mocks.getConfig.mockResolvedValue(
            config({ provider: "codex", presetId: "", apiKey: "", model: "", disableSandbox: true }),
        );
        render(<AIProviderSettingsCard flat />);
        expect(await screen.findByText("Run commands without the sandbox")).toBeInTheDocument();
        expect(screen.getByRole("checkbox", { name: /Run commands without the sandbox/ })).toBeChecked();
        expect(screen.getByText("CLI command")).toBeInTheDocument();
    });

    it("names the models in plain words in the zh-CN locale", async () => {
        await act(async () => {
            await i18n.changeLanguage("zh-CN");
        });
        try {
            const { container } = render(<AIProviderSettingsCard flat />);
            expect(await screen.findByText("API 密钥")).toBeInTheDocument();
            expect(screen.getByText("轻量模型")).toBeInTheDocument();
            expect(screen.getByText("主模型，用于对话和大部分思考。")).toBeInTheDocument();
            expect(container).not.toHaveTextContent(/大脑|小脑/);
        } finally {
            await act(async () => {
                await i18n.changeLanguage("en");
            });
        }
    });

    it("saves an API provider and reports success", async () => {
        render(<AIProviderSettingsCard flat />);
        await screen.findByText("API key");
        fireEvent.click(screen.getByRole("button", { name: "Save and apply" }));
        await vi.waitFor(() => expect(mocks.saveConfig).toHaveBeenCalledWith(expect.objectContaining({ provider: "openai" })));
        await vi.waitFor(() => expect(screen.getByRole("button", { name: "Save and apply" })).toBeEnabled());
        expect(mocks.toastSuccess).not.toHaveBeenCalled();
        expect(mocks.toastError).not.toHaveBeenCalled();
    });

    it("shows a failed save in place in copper with Try again, never a toast (ST9, ST11)", async () => {
        mocks.saveConfig.mockRejectedValueOnce(new Error("Error: raw failure"));
        render(<AIProviderSettingsCard flat />);
        await screen.findByText("API key");
        fireEvent.click(screen.getByRole("button", { name: "Save and apply" }));
        const line = await screen.findByText("Couldn't change this.");
        expect(line.closest("[data-settings-failure]")?.className).toContain("text-copper");
        expect(screen.queryByText(/raw failure/)).toBeNull();
        expect(mocks.toastError).not.toHaveBeenCalled();
        fireEvent.click(screen.getByRole("button", { name: "Try again" }));
        await vi.waitFor(() => expect(mocks.saveConfig).toHaveBeenCalledTimes(2));
        await vi.waitFor(() => expect(screen.queryByText("Couldn't change this.")).toBeNull());
    });

    it.each(["claude-code", "codex"])("saves the %s assistant with its command settings", async (provider) => {
        mocks.getConfig.mockResolvedValue(config({ provider, presetId: "", apiKey: "" }));
        render(<AIProviderSettingsCard flat />);
        const save = await screen.findByRole("button", { name: "Save and apply" });
        await vi.waitFor(() => expect(save).toBeEnabled());
        fireEvent.click(save);
        await vi.waitFor(() => expect(mocks.saveConfig).toHaveBeenCalledWith(expect.objectContaining({ provider })));
        expect(mocks.toastSuccess).not.toHaveBeenCalled();
        expect(mocks.toastError).not.toHaveBeenCalled();
    });
});

describe("AssistantPage", () => {
    beforeEach(() => {
        vi.clearAllMocks();
        mocks.eventsOn.mockReturnValue(() => undefined);
        mocks.listPresets.mockResolvedValue([openai]);
        mocks.getConfig.mockResolvedValue(config());
        mocks.getMCPConnections.mockResolvedValue([]);
    });

    it("hands the page the flat key form", async () => {
        const { container } = render(<AssistantPage />);
        expect(await screen.findByText("API key")).toBeInTheDocument();
        expect(screen.getByTestId("model-accounts")).toBeInTheDocument();
        // Your agents leads the page (computer-settings v1.0.0 rule 5).
        expect(screen.getByTestId("your-agents").compareDocumentPosition(screen.getByTestId("model-accounts")) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
        // Until the Settings registry's Your AI entry lands, the model
        // accounts sit under their own "Your AI" heading (conformance answers G1).
        expect(screen.getByRole("heading", { name: "Your AI" }).compareDocumentPosition(screen.getByTestId("model-accounts")) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
        // Isoform's own text stays internal: no prompt editors in Settings.
        expect(screen.queryByText("How Isoform works with you")).not.toBeInTheDocument();
        expect(screen.queryByRole("textbox", { name: /talk to you/i })).not.toBeInTheDocument();
        expect(screen.getByTestId("model-accounts").querySelector(".settings-card")).toBeNull();
        expect(container.querySelectorAll(".settings-card")).toHaveLength(0);
        // No pointer to Other AI apps: the design's Settings has no such section (computer-settings v1.1.0 rule 1).
        expect(screen.queryByText(/Other AI apps/)).not.toBeInTheDocument();
        expect(screen.queryByRole("heading", { name: /Claude Code/ })).not.toBeInTheDocument();
    });

});

// ST13: Copy curl command is developer-only, even under More options.
describe("AIProviderSettingsCard without Developer tools", () => {
    it("never offers Copy curl command", async () => {
        devTools.on = false;
        render(<AIProviderSettingsCard flat />);
        await screen.findByText("API key");
        fireEvent.click(screen.getByRole("button", { name: "More options" }));
        expect(screen.queryByRole("button", { name: "Copy curl command" })).not.toBeInTheDocument();
    });
});
