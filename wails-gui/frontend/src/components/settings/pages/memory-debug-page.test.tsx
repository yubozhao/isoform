import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import i18n from "@/i18n";
import en from "@/i18n/locales/en/settings.json";
import zh from "@/i18n/locales/zh-CN/settings.json";
import pageSource from "./memory-debug-page.tsx?raw";
import featuresSource from "./learning-features.tsx?raw";
import { MemoryDebugPage } from "./memory-debug-page";

const mocks = vi.hoisted(() => ({
    uiState: {} as Record<string, string>,
    loadUIState: vi.fn(),
    saveUIState: vi.fn(),
    getPermissions: vi.fn(),
    requestPermission: vi.fn(),
    openPermissionSettings: vi.fn(),
    getMemoryConfig: vi.fn(),
    getMemoryStatus: vi.fn(),
}));

// The wailsjs path and the shim resolve to the same module, so one mock covers both imports.
vi.mock("@/lib/wails-shim-app", () => ({
    LoadUIState: mocks.loadUIState,
    SaveUIState: mocks.saveUIState,
    GetPlatformPermissions: mocks.getPermissions,
    RequestPermission: mocks.requestPermission,
    OpenPermissionSettings: mocks.openPermissionSettings,
    GetMemoryConfig: mocks.getMemoryConfig,
    GetMemoryStatus: mocks.getMemoryStatus,
    SaveMemoryConfig: vi.fn(),
    TestEmbedding: vi.fn(),
}));

vi.mock("../../../../wailsjs/runtime/runtime", () => ({
    EventsOn: () => () => {},
}));

// Covered by its own tests; a marker keeps this one about the page layout.
vi.mock("@/components/memory-context-debug-settings", () => ({
    MemoryContextDebugSettings: () => <p>Chat context preview</p>,
}));
vi.mock("@/components/daily-topics-preview", () => ({
    DailyTopicsPreview: () => <p>Daily topics preview</p>,
}));

function renderPage() {
    render(<MemoryDebugPage />);
}

describe("Developer tools › Memory and search (from Learning and memory, settings batch D3)", () => {
    beforeEach(async () => {
        vi.clearAllMocks();
        window.localStorage.clear();
        await i18n.changeLanguage("en");
        mocks.uiState = {};
        mocks.loadUIState.mockImplementation((key: string) =>
            Promise.resolve(mocks.uiState[key] ?? ""),
        );
        mocks.saveUIState.mockResolvedValue(undefined);
        mocks.requestPermission.mockResolvedValue(false);
        mocks.openPermissionSettings.mockResolvedValue(undefined);
        mocks.getMemoryConfig.mockResolvedValue({ provider: "", apiKey: "", baseUrl: "", model: "" });
        mocks.getMemoryStatus.mockResolvedValue({
            files: 3,
            chunks: 12,
            dirty: false,
            syncReason: "",
            provider: "local-onnx",
            model: "all-MiniLM-L6-v2",
            vectorReady: true,
            cachedEmbeds: 0,
        });
    });

    it("shows a failed learning read and retries without guessing", async () => {
        mocks.loadUIState.mockRejectedValueOnce(new Error("offline"));
        renderPage();
        expect(await screen.findByText("Couldn't read this setting.")).toBeInTheDocument();
        fireEvent.click(screen.getByRole("button", { name: "Try again" }));
        await waitFor(() => expect(screen.queryByText("Couldn't read this setting.")).toBeNull());
    });

    it("saves a learning switch and puts it back when the save fails", async () => {
        renderPage();
        const aboutYou = await screen.findByRole("switch", { name: "About you" });
        await waitFor(() => expect(mocks.loadUIState).toHaveBeenCalledWith("triage_enable_memory"));

        fireEvent.click(aboutYou);
        expect(mocks.saveUIState).toHaveBeenCalledWith("triage_enable_memory", "false");
        await waitFor(() => expect(aboutYou).toHaveAttribute("aria-checked", "false"));

        mocks.uiState.triage_enable_memory = "false";
        mocks.saveUIState.mockRejectedValueOnce(new Error("disk full"));
        fireEvent.click(aboutYou);
        expect(mocks.saveUIState).toHaveBeenLastCalledWith("triage_enable_memory", "true");
        expect(aboutYou).toHaveAttribute("aria-checked", "true");
        await waitFor(() => expect(screen.getByRole("switch", { name: "About you" })).toHaveAttribute("aria-checked", "false"));
    });

    it("has no Learn by comparison switch, which nothing reads, and keeps Prediction confidence on its own (S16)", async () => {
        mocks.uiState = { triage_enable_shadow: "false" };
        renderPage();

        const confidence = await screen.findByRole("switch", { name: "Prediction confidence" });
        await waitFor(() => expect(confidence).toBeEnabled());
        expect(screen.queryByRole("switch", { name: "Learn by comparison" })).not.toBeInTheDocument();
        expect(screen.queryByText("Turn on Learn by comparison to use this.")).not.toBeInTheDocument();
        expect(mocks.loadUIState).not.toHaveBeenCalledWith("triage_enable_shadow");

        fireEvent.click(confidence);
        expect(mocks.saveUIState).toHaveBeenCalledWith("shadow_confidence_adjustment_enabled", "false");
    });

    it("holds what Isoform learns, Prediction confidence, both previews and the search index", async () => {
        renderPage();
        expect(screen.getByRole("heading", { name: "What Isoform learns" })).toBeInTheDocument();
        await waitFor(() => expect(mocks.loadUIState).toHaveBeenCalledWith("triage_enable_sop"));
        expect(await screen.findByRole("switch", { name: "Prediction confidence" })).toBeInTheDocument();
        expect(screen.getByText("Daily topics preview")).toBeInTheDocument();

        expect(screen.getByText("Chat context preview")).toBeInTheDocument();
        const preview = screen.getByRole("heading", { name: "Preview chat context" });
        expect(preview).toHaveClass("settings-card-title");
        expect(preview.closest(".settings-card")).toContainElement(screen.getByText("Chat context preview"));
        const card = screen.getByRole("heading", { name: "Search index" }).closest(".settings-card");
        expect(card).not.toBeNull();
        expect(await within(card as HTMLElement).findByText("On this computer")).toBeInTheDocument();
        expect(within(card as HTMLElement).getByText("3 files, 12 passages")).toBeInTheDocument();
        expect(within(card as HTMLElement).getByRole("button", { name: /Save and rebuild/ })).toBeInTheDocument();
    });

    it("keeps every page string in both languages and out of the source", () => {
        const leaves = (value: unknown, prefix = ""): string[] =>
            value && typeof value === "object"
                ? Object.entries(value).flatMap(([key, child]) =>
                      leaves(child, prefix ? `${prefix}.${key}` : key),
                  )
                : [prefix];
        expect(leaves(zh.learningMemory).sort()).toEqual(leaves(en.learningMemory).sort());
        expect(leaves(zh.embedding).sort()).toEqual(leaves(en.embedding).sort());
        for (const source of [pageSource, featuresSource]) {
            expect(source).not.toMatch(/\bt\(\s*["'`][^"'`]+["'`]\s*,\s*["'`]/);
            expect(source).not.toMatch(/defaultValue/);
        }
    });
});
