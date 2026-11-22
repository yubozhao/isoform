import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import i18n from "@/i18n";
import en from "@/i18n/locales/en/settings.json";
import zh from "@/i18n/locales/zh-CN/settings.json";
import type { FindingProviderView, YourAgents } from "@/lib/wails-shim-app";
import { YourAISection } from "./your-ai-section";

const mocks = vi.hoisted(() => ({
    agents: null as unknown as YourAgents,
    modelStatus: { mode: "managed", accountOrder: ["claude"] as string[] },
    provider: "",
    finding: { choice: "local", hasOpenAIKey: false, localModelDownloaded: true, localModelBytes: 0 } as FindingProviderView,
    setFinding: vi.fn(),
    plugins: [] as { id: string; enabled: boolean }[],
    setBuiltin: vi.fn(),
}));

vi.mock("@/lib/wails-shim-app", () => ({
    GetYourAgents: vi.fn(async () => mocks.agents),
    GetModelAccountsStatus: vi.fn(async () => mocks.modelStatus),
    GetAIProviderConfig: vi.fn(async () => ({ provider: mocks.provider })),
    GetFindingProvider: vi.fn(async () => mocks.finding),
    SetFindingProvider: mocks.setFinding,
    ListPlugins: vi.fn(async () => mocks.plugins),
    SetBuiltinOn: mocks.setBuiltin,
}));
vi.mock("@/components/model-accounts-card", () => ({ ModelAccountsSettings: () => <div data-testid="model-accounts" /> }));
vi.mock("./assistant-page", () => ({ AIProviderSettingsCard: () => null }));

describe("Settings › Your AI", () => {
    beforeEach(async () => {
        await i18n.changeLanguage("en");
        mocks.agents = {
            choices: { code: "claude-code", else: "" },
            agents: [
                { id: "claude-code", name: "Claude Code", kind: "code", reach: "connected_computer", found: true, connected: true },
                { id: "codex", name: "Codex", kind: "code", reach: "on_computer", found: true, connected: false },
            ],
        };
        mocks.modelStatus = { mode: "managed", accountOrder: ["claude"] };
        mocks.provider = "";
        mocks.finding = { choice: "local", hasOpenAIKey: false, localModelDownloaded: true, localModelBytes: 0 };
        mocks.setFinding.mockReset();
        mocks.plugins = [
            { id: "memory-dream", enabled: true },
            { id: "memory-dream-llm", enabled: true },
            { id: "topic-finds", enabled: true },
        ];
        mocks.setBuiltin.mockReset();
        mocks.setBuiltin.mockImplementation(async (id: string, on: boolean) => {
            mocks.plugins = mocks.plugins.map((p) => (p.id === id ? { ...p, enabled: on } : p));
            return mocks.plugins;
        });
        mocks.setFinding.mockImplementation(async (choice: FindingProviderView["choice"], _key: string, address: string) => ({
            ...mocks.finding,
            choice,
            address: address || undefined,
            hasOpenAIKey: choice === "openai" || mocks.finding.hasOpenAIKey,
        }));
    });

    const openFindingMenu = () => {
        const trigger = document.querySelector("[data-your-ai-finding-change]") as HTMLElement;
        fireEvent.pointerDown(trigger, { button: 0, pointerType: "mouse" });
    };
    const pickFinding = (choice: string) => fireEvent.click(document.querySelector(`[data-your-ai-finding-pick=${choice}]`)!);

    it("shows Finding things in what Isoform keeps, on this computer by default, with no key field (T8)", async () => {
        render(<YourAISection />);
        expect(await screen.findByText(en.yourAI.finding.here)).toBeInTheDocument();
        expect(screen.getByText(en.yourAI.finding.title)).toBeInTheDocument();
        expect(document.querySelector("[data-your-ai-finding-openai]")).toBeNull();
        expect(screen.queryByText(en.yourAI.finding.leaves)).not.toBeInTheDocument();
        // "Automatic" is gone: never a choice.
        await waitFor(() => expect(document.querySelector("[data-your-ai-finding-change]")).not.toBeDisabled());
        openFindingMenu();
        expect(await screen.findByText(en.yourAI.finding.choice.ollama)).toBeInTheDocument();
        expect(screen.queryByText(/automatic/i)).not.toBeInTheDocument();
    });

    it("shows the key field and the What leaves line only once OpenAI is picked, and saves the key", async () => {
        render(<YourAISection />);
        await waitFor(() => expect(document.querySelector("[data-your-ai-finding-change]")).not.toBeDisabled());
        openFindingMenu();
        pickFinding("openai");
        expect(await screen.findByText(en.yourAI.finding.leaves)).toBeInTheDocument();
        expect(mocks.setFinding).not.toHaveBeenCalled();
        fireEvent.change(screen.getByLabelText(en.yourAI.finding.keyLabel), { target: { value: " sk-test " } });
        fireEvent.click(screen.getByRole("button", { name: en.yourAI.finding.save }));
        await waitFor(() => expect(mocks.setFinding).toHaveBeenCalledWith("openai", "sk-test", ""));
        expect(await screen.findByText(en.yourAI.finding.openai)).toBeInTheDocument();
        expect(document.querySelector("[data-your-ai-finding-openai]")).toBeNull();
    });

    it("saves Ollama with its address and On this computer straight away", async () => {
        render(<YourAISection />);
        await waitFor(() => expect(document.querySelector("[data-your-ai-finding-change]")).not.toBeDisabled());
        openFindingMenu();
        pickFinding("ollama");
        const field = (await screen.findByLabelText(en.yourAI.finding.addressLabel)) as HTMLInputElement;
        expect(field.value).toBe("http://localhost:11434");
        fireEvent.change(field, { target: { value: "http://studio.local:11434" } });
        fireEvent.click(screen.getByRole("button", { name: en.yourAI.finding.save }));
        await waitFor(() => expect(mocks.setFinding).toHaveBeenCalledWith("ollama", "", "http://studio.local:11434"));
        expect(await screen.findByText("Ollama at http://studio.local:11434")).toBeInTheDocument();
        openFindingMenu();
        pickFinding("local");
        await waitFor(() => expect(mocks.setFinding).toHaveBeenLastCalledWith("local", "", ""));
    });

    it("says it couldn't change, and keeps the choice", async () => {
        mocks.setFinding.mockRejectedValue(new Error("no"));
        render(<YourAISection />);
        await waitFor(() => expect(document.querySelector("[data-your-ai-finding-change]")).not.toBeDisabled());
        openFindingMenu();
        pickFinding("local");
        expect(await screen.findByText(en.yourAI.finding.failed)).toBeInTheDocument();
        expect(screen.getByText(en.yourAI.finding.here)).toBeInTheDocument();
    });

    it("holds the model accounts and names the assistant, also your agent for its job", async () => {
        render(<YourAISection />);
        expect(screen.getByTestId("model-accounts")).toBeInTheDocument();
        expect(await screen.findByText("Claude Code reads your day and writes your briefs and answers.")).toBeInTheDocument();
        expect(screen.getByText("Claude Code · also your agent for code")).toBeInTheDocument();
    });

    it("uses the assistant's own CLI outside subscriptions, and says nothing more when it holds no job", async () => {
        mocks.modelStatus = { mode: "external", accountOrder: [] };
        mocks.provider = "codex";
        render(<YourAISection />);
        expect(await screen.findByText("Codex reads your day and writes your briefs and answers.")).toBeInTheDocument();
        expect(document.querySelector("[data-your-ai-also]")).toBeNull();
    });

    it("shows Overnight review with Your AI's name, and one switch turns both builtins off, then on (S17)", async () => {
        render(<YourAISection />);
        expect(await screen.findByText(en.yourAI.overnight.title)).toBeInTheDocument();
        expect(await screen.findByText("Reads your saved memories with Claude Code and proposes changes for you to check.")).toBeInTheDocument();
        const toggle = document.querySelector("[data-your-ai-overnight]") as HTMLElement;
        await waitFor(() => expect(toggle).not.toBeDisabled());
        expect(toggle).toHaveAttribute("aria-checked", "true");
        fireEvent.click(toggle);
        await waitFor(() => expect(mocks.setBuiltin).toHaveBeenCalledTimes(2));
        // Off: the Your AI pass first, then the review it depends on.
        expect(mocks.setBuiltin.mock.calls).toEqual([["memory-dream-llm", false], ["memory-dream", false]]);
        await waitFor(() => expect(toggle).toHaveAttribute("aria-checked", "false"));
        mocks.setBuiltin.mockClear();
        fireEvent.click(toggle);
        await waitFor(() => expect(mocks.setBuiltin).toHaveBeenCalledTimes(2));
        expect(mocks.setBuiltin.mock.calls).toEqual([["memory-dream", true], ["memory-dream-llm", true]]);
        // topic-finds is not this row's.
        expect(mocks.setBuiltin.mock.calls.some(([id]) => id === "topic-finds")).toBe(false);
    });

    it("shows on while either builtin is on, and Off turns both off", async () => {
        mocks.plugins = [
            { id: "memory-dream", enabled: true },
            { id: "memory-dream-llm", enabled: false },
        ];
        render(<YourAISection />);
        const toggle = (await screen.findByLabelText(en.yourAI.overnight.title)) as HTMLElement;
        await waitFor(() => expect(toggle).toHaveAttribute("aria-checked", "true"));
        fireEvent.click(toggle);
        await waitFor(() => expect(mocks.setBuiltin.mock.calls).toEqual([["memory-dream-llm", false], ["memory-dream", false]]));
    });

    it("never shows an unread state as off", async () => {
        const { ListPlugins } = await import("@/lib/wails-shim-app");
        vi.mocked(ListPlugins).mockRejectedValueOnce(new Error("no"));
        render(<YourAISection />);
        expect(await screen.findByText(en.yourAI.overnight.readFailed)).toBeInTheDocument();
        expect(document.querySelector("[data-your-ai-overnight]")).toBeNull();
    });

    it("says it couldn't change Overnight review, and reads the saved state again", async () => {
        mocks.setBuiltin.mockRejectedValue(new Error("no"));
        render(<YourAISection />);
        const toggle = (await screen.findByLabelText(en.yourAI.overnight.title)) as HTMLElement;
        await waitFor(() => expect(toggle).not.toBeDisabled());
        fireEvent.click(toggle);
        expect(await screen.findByText(en.yourAI.overnight.failed)).toBeInTheDocument();
    });

    it("keeps both locales in step", () => {
        expect(Object.keys(zh.yourAI).sort()).toEqual(Object.keys(en.yourAI).sort());
        expect(Object.keys(zh.yourAI.finding).sort()).toEqual(Object.keys(en.yourAI.finding).sort());
        expect(Object.keys(zh.yourAI.overnight).sort()).toEqual(Object.keys(en.yourAI.overnight).sort());
        expect(en.yourAI.title).toBe("Your AI");
    });
});
