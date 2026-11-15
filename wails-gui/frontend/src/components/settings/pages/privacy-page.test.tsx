import i18n from "@/i18n";
import { Children, isValidElement, type ReactElement, type ReactNode } from "react";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import en from "@/i18n/locales/en/settings.json";
import zh from "@/i18n/locales/zh-CN/settings.json";
import type { PermissionItem, PlatformPermissions } from "@/types";
import privacySource from "./privacy-page.tsx?raw";
import observationSource from "./observation-page.tsx?raw";
import sharedSource from "./capture-shared.tsx?raw";
import { PrivacyPage } from "./privacy-page";
import { resetPageQueries } from "@/lib/page-queries";

const mocks = vi.hoisted(() => ({
    invalidateActivity: vi.fn(),
    getPermissions: vi.fn(),
    setPermission: vi.fn(),
    requestPermission: vi.fn(),
    openPermissionSettings: vi.fn(),
    getListening: vi.fn(),
    systemAudioSupported: vi.fn(),
    getRetention: vi.fn(),
    setRetention: vi.fn(),
    getMaxGB: vi.fn(),
    setMaxGB: vi.fn(),
    storageStats: vi.fn(),
    getSensitive: vi.fn(),
    removeSensitive: vi.fn(),
    getVoice: vi.fn(),
    removeVoice: vi.fn(),
    listApps: vi.fn(),
    getTelemetry: vi.fn(),
    setTelemetry: vi.fn(),
    getRiskyCommands: vi.fn(),
    setRiskyCommands: vi.fn(),
    getReadHistory: vi.fn(),
    loadUIState: vi.fn((_key: string) => Promise.resolve("")),
    saveUIState: vi.fn(() => Promise.resolve()),
    setReadHistory: vi.fn(),
    emit: vi.fn(),
    listeners: new Map<string, Set<(...args: unknown[]) => void>>(),
    toast: Object.assign(vi.fn(), { error: vi.fn(), success: vi.fn(), warning: vi.fn(), dismiss: vi.fn() }),
}));

vi.mock("@/components/timeline/activity-session-cache", () => ({ invalidateActivitySessionCache: mocks.invalidateActivity }));

vi.mock("@/lib/wails-shim-app", () => ({
    GetPlatformPermissions: mocks.getPermissions,
    SetPermissionEnabled: mocks.setPermission,
    RequestPermission: mocks.requestPermission,
    OpenPermissionSettings: mocks.openPermissionSettings,
    GetBackgroundListening: mocks.getListening,
    SetBackgroundListening: vi.fn(),
    IsSystemAudioBackgroundListeningSupported: mocks.systemAudioSupported,
    GetActivityRetentionDays: mocks.getRetention,
    SetActivityRetentionDays: mocks.setRetention,
    GetActivityMaxStorageGB: mocks.getMaxGB,
    SetActivityMaxStorageGB: mocks.setMaxGB,
    GetActivityStorageStats: mocks.storageStats,
    GetSensitiveApps: mocks.getSensitive,
    AddSensitiveApp: vi.fn(),
    RemoveSensitiveApp: mocks.removeSensitive,
    GetVoiceInputApps: mocks.getVoice,
    AddVoiceInputApp: vi.fn(),
    RemoveVoiceInputApp: mocks.removeVoice,
    ListSystemApps: mocks.listApps,
    GetTelemetryTier: mocks.getTelemetry,
    SetTelemetryTier: mocks.setTelemetry,
    GetAskBeforeRiskyCommands: mocks.getRiskyCommands,
    SetAskBeforeRiskyCommands: mocks.setRiskyCommands,
    GetReadAssistantHistory: mocks.getReadHistory,
    SetReadAssistantHistory: mocks.setReadHistory,
    LoadUIState: mocks.loadUIState,
    SaveUIState: mocks.saveUIState,
}));

vi.mock("../../../../wailsjs/runtime/runtime", () => ({
    EventsOn: (event: string, listener: (...args: unknown[]) => void) => {
        const listeners = mocks.listeners.get(event) ?? new Set();
        listeners.add(listener);
        mocks.listeners.set(event, listeners);
        return () => listeners.delete(listener);
    },
    EventsEmit: mocks.emit,
}));

vi.mock("sonner", () => ({ toast: mocks.toast }));

// Radix Select needs pointer APIs jsdom lacks; a native <select> keeps the
// same value / onValueChange contract and the trigger's accessible name.
vi.mock("@/components/ui/select", () => {
    const SelectTrigger = (_: { "aria-label"?: string; className?: string; children?: ReactNode }) => null;
    const SelectValue = () => null;
    const SelectContent = ({ children }: { children?: ReactNode }) => <>{children}</>;
    const SelectItem = ({ value, children }: { value: string; children?: ReactNode }) => (
        <option value={value}>{children}</option>
    );
    const Select = ({
        value,
        onValueChange,
        disabled,
        children,
    }: {
        value: string;
        onValueChange: (value: string) => void;
        disabled?: boolean;
        children?: ReactNode;
    }) => {
        const trigger = Children.toArray(children).find(
            (child): child is ReactElement<{ "aria-label"?: string }> =>
                isValidElement(child) && child.type === SelectTrigger,
        );
        return (
            <select
                aria-label={trigger?.props["aria-label"]}
                value={value}
                disabled={disabled}
                onChange={(event) => onValueChange(event.target.value)}
            >
                {children}
            </select>
        );
    };
    return { Select, SelectTrigger, SelectValue, SelectContent, SelectItem };
});

const GIB = 1024 * 1024 * 1024;

function permission(key: string, overrides: Partial<PermissionItem> = {}): PermissionItem {
    return {
        key,
        label: key,
        description: "",
        granted: true,
        required: false,
        actionable: true,
        actionLabel: "Grant",
        toggleable: false,
        enabled: false,
        ...overrides,
    };
}

const MAC_PERMISSIONS: PlatformPermissions = {
    platform: "darwin",
    items: [
        permission("screen_recording", { required: true, toggleable: true, enabled: true }),
        permission("accessibility", { required: true, toggleable: true, enabled: false }),
        permission("microphone"),
        permission("system_audio"),
        permission("notifications"),
        permission("automation_messages", { granted: false }),
        permission("contacts"),
    ],
} as PlatformPermissions;

// Renders the page and waits for every load to land, so no state update
// arrives after a test has finished.
async function renderPage(props: { onOpenPage?: (page: string) => void; onOpenPeople?: () => void } = {}) {
    render(<PrivacyPage {...props} />);
    await screen.findByRole("combobox", { name: "Keep recordings for" });
    await waitFor(() => expect(screen.queryByText("Loading...")).not.toBeInTheDocument());
    await waitFor(() =>
        expect(screen.getByRole("switch", { name: "Share diagnostic data" })).not.toBeDisabled(),
    );
    await waitFor(() => expect(mocks.listApps).toHaveBeenCalled());
    await waitFor(() => expect(mocks.getPermissions).toHaveBeenCalled());
}

async function openAdvanced() {
    fireEvent.click(screen.getByRole("button", { name: /Advanced/ }));
}

function leafKeys(value: unknown, prefix = ""): string[] {
    if (value === null || typeof value !== "object") return [prefix];
    return Object.entries(value as Record<string, unknown>).flatMap(([key, child]) =>
        leafKeys(child, prefix ? `${prefix}.${key}` : key),
    );
}

describe("PrivacyPage", () => {
    beforeEach(() => {
        resetPageQueries();
        vi.clearAllMocks();
        mocks.listeners.clear();
        localStorage.clear();
        mocks.getPermissions.mockResolvedValue(MAC_PERMISSIONS);
        mocks.requestPermission.mockResolvedValue(true);
        mocks.openPermissionSettings.mockResolvedValue(undefined);
        mocks.getListening.mockResolvedValue(true);
        mocks.systemAudioSupported.mockResolvedValue(true);
        mocks.getRetention.mockResolvedValue(30);
        mocks.setRetention.mockResolvedValue(undefined);
        mocks.getMaxGB.mockResolvedValue(10);
        mocks.setMaxGB.mockResolvedValue(undefined);
        mocks.storageStats.mockResolvedValue({
            retainedBytes: 3 * GIB,
            retainedSessionCount: 12,
            earliestRetainedMs: 0,
        });
        mocks.getSensitive.mockResolvedValue([]);
        mocks.removeSensitive.mockResolvedValue(undefined);
        mocks.getVoice.mockResolvedValue([]);
        mocks.removeVoice.mockResolvedValue(undefined);
        mocks.listApps.mockResolvedValue([]);
        mocks.getTelemetry.mockResolvedValue("detailed");
        mocks.setTelemetry.mockResolvedValue(undefined);
        mocks.getRiskyCommands.mockResolvedValue(false);
        mocks.setRiskyCommands.mockResolvedValue(undefined);
        mocks.getReadHistory.mockResolvedValue(true);
        mocks.setReadHistory.mockResolvedValue(undefined);
    });

    it("routes Contacts to Who you know without requesting permission", async () => {
        const onOpenPeople = vi.fn();
        await renderPage({ onOpenPeople });
        await openAdvanced();
        fireEvent.click(await screen.findByRole("button", { name: "Open Who you know" }));
        expect(onOpenPeople).toHaveBeenCalledTimes(1);
        expect(mocks.requestPermission).not.toHaveBeenCalled();
        expect(mocks.openPermissionSettings).not.toHaveBeenCalled();
    });

    it.each(["windows", "linux"])("offers vCard import through Who you know on %s", async (platform) => {
        mocks.getPermissions.mockResolvedValue({ ...MAC_PERMISSIONS, platform });
        const onOpenPeople = vi.fn();
        await renderPage({ onOpenPeople });
        await openAdvanced();
        expect(await screen.findByText("Import a vCard")).toBeInTheDocument();
        fireEvent.click(screen.getByRole("button", { name: "Open Who you know" }));
        expect(onOpenPeople).toHaveBeenCalledTimes(1);
        expect(mocks.requestPermission).not.toHaveBeenCalled();
    });

    it("opens with the four lines of On this computer, as setup shows them (ST1)", async () => {
        await renderPage();
        const first = document.querySelector(".settings-card") as HTMLElement;
        expect(within(first).getByRole("heading", { name: "On this computer" })).toBeInTheDocument();
        const lines = within(first).getAllByRole("listitem");
        expect(lines.map((li) => li.textContent)).toEqual([
            "Memory, screenshots and audio are stored on this computer, and models here read them.",
            "Your AI gets only what a task needs, on your own plan: the text, or for WeChat, a cropped screenshot.",
            "Your phone gets only what it shows, encrypted end to end.",
            "Usage counts, if you share them, carry a random number, never your name or your content.",
        ]);
    });

    it("says usage counts carry a random number and the phone line, in Chinese (ST60)", async () => {
        await i18n.changeLanguage("zh-CN");
        try {
            render(<PrivacyPage />);
            await waitFor(() => expect(document.querySelector(".settings-card li")).not.toBeNull());
            const first = document.querySelector(".settings-card") as HTMLElement;
            const lines = within(first).getAllByRole("listitem").map((li) => li.textContent);
            expect(lines).toContain("你的手机只拿到它要显示的内容，端到端加密。");
            expect(lines).toContain("使用统计（如果你选择分享）只带一个随机编号，不含你的名字和内容。");
            expect(first).not.toHaveTextContent("匿名");
        } finally {
            await i18n.changeLanguage("en");
        }
    });

    it("keeps both per-app lists together in Apps Isoform ignores", async () => {
        await renderPage();

        const card = screen.getByText("Apps Isoform ignores").closest(".settings-card") as HTMLElement;
        expect(within(card).getByText("Hidden from screen recordings")).toBeInTheDocument();
        expect(within(card).getByText("Apps Isoform does not listen to")).toBeInTheDocument();
        await waitFor(() => expect(within(card).getAllByRole("button", { name: "Add app" })).toHaveLength(2));
    });

    it("replaces the not-listened list with a reason while listening is off", async () => {
        mocks.getListening.mockResolvedValue(false);
        const onOpenPage = vi.fn();
        await renderPage({ onOpenPage });

        expect(
            await screen.findByText(
                "Listening is off, so this list has no effect. Turn on Listen on the Observation page to choose apps here.",
            ),
        ).toBeInTheDocument();
        expect(screen.getAllByRole("button", { name: "Add app" })).toHaveLength(1);

        fireEvent.click(screen.getByRole("button", { name: "Open Observation" }));
        expect(onOpenPage).toHaveBeenCalledWith("activity");
    });

    it("asks before an app stops being hidden from screen recordings", async () => {
        mocks.getSensitive.mockResolvedValue([
            { bundlePrefix: "com.example.Diary", category: "Diary", isDefault: false },
        ]);
        await renderPage();

        const row = (await screen.findByText("Diary")).parentElement?.parentElement as HTMLElement;
        expect(within(row).getByText("Diary")).toHaveClass("settings-row-title");
        fireEvent.click(within(row).getByRole("button", { name: "Remove" }));
        expect(await screen.findByText("Stop hiding Diary?")).toBeInTheDocument();
        expect(screen.getByText("Its windows will appear in Isoform's recordings again.")).toBeInTheDocument();

        fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
        expect(mocks.removeSensitive).not.toHaveBeenCalled();
        expect(screen.getByText("Diary")).toBeInTheDocument();

        fireEvent.click(within(row).getByRole("button", { name: "Remove" }));
        fireEvent.click(await screen.findByRole("button", { name: "Stop hiding" }));
        await waitFor(() => expect(mocks.removeSensitive).toHaveBeenCalledWith("com.example.Diary"));
        await waitFor(() => expect(mocks.invalidateActivity).toHaveBeenCalledWith(true));
        await waitFor(() => expect(screen.queryByText("Diary")).not.toBeInTheDocument());
    });

    it("asks before Isoform listens in an app again", async () => {
        mocks.getVoice.mockResolvedValue([
            { bundleId: "com.example.Dictate", appName: "Dictate", isDefault: false },
        ]);
        await renderPage();

        const row = (await screen.findByText("Dictate")).parentElement?.parentElement as HTMLElement;
        fireEvent.click(within(row).getByRole("button", { name: "Remove" }));
        expect(await screen.findByText("Listen while Dictate is in front?")).toBeInTheDocument();
        expect(mocks.removeVoice).not.toHaveBeenCalled();

        fireEvent.click(screen.getByRole("button", { name: "Listen again" }));
        await waitFor(() => expect(mocks.removeVoice).toHaveBeenCalledWith("com.example.Dictate"));
    });

    it("asks before a shorter time limit deletes recordings", async () => {
        await renderPage();
        const select = await screen.findByRole("combobox", { name: "Keep recordings for" });
        expect(
            await screen.findByText("Using 3.0 GB of your 10 GB storage limit for 12 sessions."),
        ).toBeInTheDocument();

        fireEvent.change(select, { target: { value: "7" } });
        expect(await screen.findByText("Keep recordings for 7 days?")).toBeInTheDocument();
        expect(mocks.setRetention).not.toHaveBeenCalled();

        fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
        expect(mocks.setRetention).not.toHaveBeenCalled();

        fireEvent.change(select, { target: { value: "7" } });
        fireEvent.click(await screen.findByRole("button", { name: "Delete older recordings" }));
        await waitFor(() => expect(mocks.setRetention).toHaveBeenCalledWith(7));
    });

    it("saves a longer time limit without asking", async () => {
        await renderPage();
        const select = await screen.findByRole("combobox", { name: "Keep recordings for" });

        fireEvent.change(select, { target: { value: "90" } });

        await waitFor(() => expect(mocks.setRetention).toHaveBeenCalledWith(90));
        expect(screen.queryByText("Keep recordings for 90 days?")).not.toBeInTheDocument();
    });

    it("puts the time limit back and shows a copper line with Try again when saving fails (ST9)", async () => {
        mocks.setRetention.mockRejectedValueOnce(new Error("disk busy"));
        await renderPage();
        const select = await screen.findByRole("combobox", { name: "Keep recordings for" });
        const before = (select as HTMLSelectElement).value;

        fireEvent.change(select, { target: { value: "90" } });

        const line = await screen.findByText("Couldn't change this.", { exact: false });
        expect(line.closest("[data-settings-failure]")).toHaveClass("text-copper");
        expect(line.textContent).not.toContain("disk busy");
        await waitFor(() => expect((select as HTMLSelectElement).value).toBe(before));
        expect(mocks.toast.error).not.toHaveBeenCalled();

        fireEvent.click(screen.getByRole("button", { name: "Try again" }));
        await waitFor(() => expect(mocks.setRetention).toHaveBeenCalledTimes(2));
        await waitFor(() => expect(screen.queryByText("Couldn't change this.", { exact: false })).not.toBeInTheDocument());
    });

    it("asks before a shorter time limit while usage is unknown", async () => {
        mocks.storageStats.mockRejectedValue(new Error("busy"));
        await renderPage();
        const select = screen.getByRole("combobox", { name: "Keep recordings for" });
        fireEvent.change(select, { target: { value: "7" } });
        expect(await screen.findByText("Keep recordings for 7 days?")).toBeInTheDocument();
        expect(mocks.setRetention).not.toHaveBeenCalled();
    });

    it("leaves the storage limit to the Storage page", async () => {
        await renderPage();
        await openAdvanced();
        expect(screen.queryByRole("combobox", { name: "Storage limit" })).not.toBeInTheDocument();
    });

    it("lists every permission with what it is used for and no switches", async () => {
        await renderPage();
        await openAdvanced();

        const card = (await screen.findByText("System permissions")).closest(".settings-card") as HTMLElement;
        await waitFor(() => expect(within(card).getByText("Messages")).toBeInTheDocument());
        expect(within(card).getByText("Contacts")).toBeInTheDocument();
        expect(
            within(card).getByText("Review consent and choose contacts on the Who you know page."),
        ).toBeInTheDocument();
        expect(
            within(card).getByText(
                "Used for reading text in app windows and using your apps when you ask. Turned off on the Observation page.",
            ),
        ).toBeInTheDocument();
        expect(within(card).queryByRole("switch")).not.toBeInTheDocument();
        expect(card.textContent).not.toContain("permissions.items");

        // Messages access is requested only on the text-message Settings page.
        expect(within(within(card).getByText("Messages").closest(".settings-row") as HTMLElement).queryByRole("button")).not.toBeInTheDocument();
        expect(mocks.requestPermission).not.toHaveBeenCalled();
    });

    it("turns diagnostic data sharing off from its card", async () => {
        await renderPage();
        const share = screen.getByRole("switch", { name: "Share diagnostic data" });
        await waitFor(() => expect(share).not.toBeDisabled());

        fireEvent.click(share);

        await waitFor(() => expect(mocks.setTelemetry).toHaveBeenCalledWith("off"));
        await waitFor(() => expect(share).not.toBeChecked());
    });

    it("preserves a diagnostic data opt-out saved during onboarding", async () => {
        mocks.getTelemetry.mockResolvedValue("off");
        await renderPage();

        expect(screen.getByRole("switch", { name: "Share diagnostic data" })).not.toBeChecked();
        expect(mocks.setTelemetry).not.toHaveBeenCalled();
    });

    it("keeps the mounted diagnostic data switch in sync with saved tier changes", async () => {
        await renderPage();
        const share = screen.getByRole("switch", { name: "Share diagnostic data" });
        expect(share).toBeChecked();

        act(() => {
            mocks.listeners.get("telemetry:tier-changed")?.forEach((listener) => listener("off"));
        });
        expect(share).not.toBeChecked();

        act(() => {
            mocks.listeners.get("telemetry:tier-changed")?.forEach((listener) => listener("detailed"));
        });
        expect(share).toBeChecked();
        expect(mocks.setTelemetry).not.toHaveBeenCalled();
    });

    it.each(["off", "detailed"])("preserves the saved %s tier when a diagnostic data change fails", async (tier) => {
        mocks.getTelemetry.mockResolvedValue(tier);
        mocks.setTelemetry.mockRejectedValue(new Error("Could not save"));
        await renderPage();
        const share = screen.getByRole("switch", { name: "Share diagnostic data" });

        fireEvent.click(share);

        await waitFor(() => expect(mocks.setTelemetry).toHaveBeenCalledWith(tier === "off" ? "detailed" : "off"));
        const alert = await screen.findByRole("alert");
        expect(alert).toHaveTextContent(en.privacy.diagnostics.saveFailed);
        expect(alert.className).toContain("text-copper");
        expect(mocks.toast.error).not.toHaveBeenCalled();
        expect(share).toHaveAttribute("aria-checked", String(tier !== "off"));
        expect(share).not.toBeDisabled();
    });

    it("waits for the diagnostic data choice to save before changing its switch", async () => {
        let finishSave!: () => void;
        mocks.setTelemetry.mockImplementation(() => new Promise<void>((resolve) => {
            finishSave = resolve;
        }));
        await renderPage();
        const share = screen.getByRole("switch", { name: "Share diagnostic data" });

        fireEvent.click(share);

        expect(share).toBeChecked();
        expect(share).toBeDisabled();
        fireEvent.click(share);
        expect(mocks.setTelemetry).toHaveBeenCalledTimes(1);

        await act(async () => finishSave());
        expect(share).not.toBeChecked();
        expect(share).not.toBeDisabled();
    });

    it.each([
        ["risky", "Ask before running risky commands"],
        ["history", "Read my Claude Code and Codex history"],
    ])("does not guess the %s setting after a failed read", async (kind, title) => {
        const getter = kind === "risky" ? mocks.getRiskyCommands : mocks.getReadHistory;
        getter.mockRejectedValueOnce(new Error("offline"));
        await renderPage();
        expect(await screen.findByText("Couldn't read this setting.")).toBeInTheDocument();
        expect(screen.queryByRole("switch", { name: title })).toBeNull();
        getter.mockResolvedValue(true);
        fireEvent.click(screen.getByRole("button", { name: "Try again" }));
        expect(await screen.findByRole("switch", { name: title })).toBeChecked();
    });

    it("keeps a failed forget visible when Settings is left during Undo", async () => {
        await renderPage();
        const read = screen.getByRole("switch", { name: "Read my Claude Code and Codex history" });
        mocks.setReadHistory.mockRejectedValueOnce(new Error("forget failed"));
        fireEvent.click(read);
        mocks.getReadHistory.mockResolvedValue(false);
        // Unmounting runs the pending change at once; the failure is kept for next time.
        const { cleanup } = await import("@testing-library/react");
        cleanup();
        await waitFor(() => expect(mocks.saveUIState).toHaveBeenCalledWith("settings.assistantHistory.failure", "forget"));
        expect(mocks.toast.error).not.toHaveBeenCalled();
        expect(mocks.toast).not.toHaveBeenCalled();

        // S032: the next time Privacy opens, the card says what failed, and Try again repeats the forget.
        mocks.loadUIState.mockImplementation((key: string) =>
            Promise.resolve(key === "settings.assistantHistory.failure" ? "forget" : ""));
        mocks.setReadHistory.mockResolvedValue(undefined);
        await renderPage();
        const alert = await screen.findByText(en.privacy.assistantHistory.forgetFailed);
        expect(alert.className).toContain("text-copper");
        fireEvent.click(within(alert).getByRole("button", { name: "Try again" }));
        await waitFor(() => expect(mocks.setReadHistory).toHaveBeenLastCalledWith(false));
        await waitFor(() => expect(mocks.saveUIState).toHaveBeenLastCalledWith("settings.assistantHistory.failure", ""));
        expect(screen.queryByText(en.privacy.assistantHistory.forgetFailed)).toBeNull();
    });

    it("keeps risky command questions off by default and turns them on from its card", async () => {
        await renderPage();
        const ask = screen.getByRole("switch", { name: "Ask before running risky commands" });
        await waitFor(() => expect(ask).not.toBeDisabled());
        expect(ask).not.toBeChecked();

        fireEvent.click(ask);

        await waitFor(() => expect(mocks.setRiskyCommands).toHaveBeenCalledWith(true));
        expect(ask).toBeChecked();
    });

    it("shows risky command questions on when they are saved on, and turns them off", async () => {
        mocks.getRiskyCommands.mockResolvedValue(true);
        await renderPage();
        const ask = screen.getByRole("switch", { name: "Ask before running risky commands" });
        await waitFor(() => expect(ask).not.toBeDisabled());
        expect(ask).toBeChecked();

        fireEvent.click(ask);

        await waitFor(() => expect(mocks.setRiskyCommands).toHaveBeenCalledWith(false));
        expect(ask).not.toBeChecked();
    });

    it("forgets the Claude Code and Codex history only after the Undo window", async () => {
        await renderPage();
        const read = screen.getByRole("switch", { name: "Read my Claude Code and Codex history" });
        await waitFor(() => expect(read).not.toBeDisabled());
        expect(read).toBeChecked();

        vi.useFakeTimers();
        try {
            fireEvent.click(read);
            expect(read).not.toBeChecked();
            expect(screen.getByText(/Stopping in 5 seconds\. Isoform will forget what came from this history\./)).toBeInTheDocument();
            expect(mocks.toast).not.toHaveBeenCalled();
            expect(mocks.setReadHistory).not.toHaveBeenCalled();
            act(() => vi.advanceTimersByTime(5000));
            expect(mocks.setReadHistory).toHaveBeenCalledWith(false);
        } finally {
            vi.useRealTimers();
        }
    });

    it("keeps the history when Undo is pressed, and reads it again at once when turned on", async () => {
        await renderPage();
        const read = screen.getByRole("switch", { name: "Read my Claude Code and Codex history" });
        await waitFor(() => expect(read).not.toBeDisabled());

        vi.useFakeTimers();
        try {
            fireEvent.click(read);
            fireEvent.click(screen.getByRole("button", { name: "Undo" }));
            act(() => vi.advanceTimersByTime(6000));
            expect(screen.queryByText(/Stopping in 5 seconds/)).toBeNull();
            expect(mocks.setReadHistory).not.toHaveBeenCalled();
            expect(read).toBeChecked();
        } finally {
            vi.useRealTimers();
        }
    });

    it("puts the history switch back on and says so when stopping fails (S032)", async () => {
        await renderPage();
        const read = screen.getByRole("switch", { name: "Read my Claude Code and Codex history" });
        await waitFor(() => expect(read).toBeChecked());
        mocks.setReadHistory.mockRejectedValueOnce(new Error("offline"));
        mocks.getReadHistory.mockResolvedValue(true);

        vi.useFakeTimers();
        try {
            fireEvent.click(read);
            await act(async () => {
                vi.advanceTimersByTime(5000);
                await Promise.resolve();
                await Promise.resolve();
            });
        } finally {
            vi.useRealTimers();
        }
        expect(await screen.findByText(en.privacy.assistantHistory.stopFailed)).toBeInTheDocument();
        expect(read).toBeChecked();
    });

    it("turns the history read on at once when it was off", async () => {
        mocks.getReadHistory.mockResolvedValue(false);
        await renderPage();
        const read = screen.getByRole("switch", { name: "Read my Claude Code and Codex history" });
        await waitFor(() => expect(read).not.toBeChecked());

        fireEvent.click(read);

        await waitFor(() => expect(mocks.setReadHistory).toHaveBeenCalledWith(true));
        expect(mocks.toast).not.toHaveBeenCalled();
    });

    it("shows the saved off state when forgetting fails after the preference was saved", async () => {
        await renderPage();
        const read = screen.getByRole("switch", { name: "Read my Claude Code and Codex history" });
        await waitFor(() => expect(read).not.toBeDisabled());
        mocks.setReadHistory.mockRejectedValueOnce(new Error("forget failed"));
        mocks.getReadHistory.mockResolvedValue(false);

        vi.useFakeTimers();
        try {
            fireEvent.click(read);
            await act(async () => {
                vi.advanceTimersByTime(5000);
                await Promise.resolve();
                await Promise.resolve();
            });
            expect(read).not.toBeChecked();
            expect(screen.getByText(en.privacy.assistantHistory.forgetFailed).className).toContain("text-copper");
            expect(mocks.toast.error).not.toHaveBeenCalled();
        } finally {
            vi.useRealTimers();
        }
    });
});

describe("Observation and Privacy copy", () => {
    it("has every key in both languages, with label and purpose for each permission Go reports", () => {
        for (const namespace of ["observation", "privacy", "permissions"] as const) {
            expect(leafKeys(zh[namespace]).sort()).toEqual(leafKeys(en[namespace]).sort());
        }
        const goPermissionKeys = [
            "screen_recording",
            "accessibility",
            "microphone",
            "system_audio",
            "notifications",
            "automation_messages",
            "automation_mail",
            "messages_history",
            "contacts",
            "calendars",
            "reminders",
            "clipboard",
            "files",
            "screen_capture",
            "input_automation",
            "pipewire",
            "portal",
            "uinput",
        ];
        for (const locale of [en, zh]) {
            const items = locale.permissions.items as Record<string, { label?: string; usedFor?: string }>;
            for (const key of goPermissionKeys) {
                expect(items[key]?.label, key).toBeTruthy();
                expect(items[key]?.usedFor, key).toBeTruthy();
            }
        }
    });

    it("never falls back to inline English", () => {
        for (const source of [privacySource, observationSource, sharedSource]) {
            expect(source).not.toContain("defaultValue");
            expect(source).not.toMatch(/\bt\(\s*["'`][^"'`]+["'`]\s*,\s*["'`]/);
        }
    });
});
