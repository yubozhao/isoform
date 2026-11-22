import { resetPageQueries } from '@/lib/page-queries';
import "@/i18n";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { PermissionItem, PlatformPermissions } from "@/types";
import { ObservationPage } from "./observation-page";

const mocks = vi.hoisted(() => ({
    handlers: new Map<string, Set<(...args: unknown[]) => void>>(),
    isPaused: vi.fn(),
    getPermissions: vi.fn(),
    setPermission: vi.fn(),
    requestPermission: vi.fn(),
    openPermissionSettings: vi.fn(),
    getListening: vi.fn(),
    setListening: vi.fn(),
    systemAudioSupported: vi.fn(),
    getMeetings: vi.fn(),
    setMeetings: vi.fn(),
    getSystemAudioOutsideCalls: vi.fn(),
    setSystemAudioOutsideCalls: vi.fn(),
    getSpeechModel: vi.fn(),
    setSpeechModel: vi.fn(),
    removeSpeechModel: vi.fn(),
    audioStatus: vi.fn(),
    monitorStatus: vi.fn(),
    calendarOverview: vi.fn(),
    setCalendarPeek: vi.fn(),
    syncCalendars: vi.fn(),
    remindersOverview: vi.fn(),
    syncReminders: vi.fn(),
    filesOverview: vi.fn(),
    syncFiles: vi.fn(),
    addPrivateFolder: vi.fn(),
    removePrivateFolder: vi.fn(),
    mailOverview: vi.fn(),
    syncMail: vi.fn(),
    messagesOverview: vi.fn(),
    syncMessages: vi.fn(),
    setLeftOut: vi.fn(),
    forgetMessages: vi.fn(),
    listPrincipals: vi.fn(),
    captureCounts: vi.fn(),
    getWatchListening: vi.fn(),
    turnOffWatchListening: vi.fn(),
    getReadHistory: vi.fn(),
    setReadHistory: vi.fn(),
    assistantChoice: vi.fn(),
    modelChoices: vi.fn(),
    getVision: vi.fn(),
    setVision: vi.fn(),
    planSpeech: vi.fn(),
    emit: vi.fn(),
    toast: { error: vi.fn(), success: vi.fn(), warning: vi.fn() },
}));

function speechPlan(over: Record<string, unknown> = {}) {
    return {
        planId: "p", choices: {}, requiredBytes: 0, modelsFolder: "",
        items: [{ id: "live", bytes: 1_500_000_000, fileCount: 1, source: { kind: "mirror", url: "" }, host: "", licence: { name: "", url: "", nonCommercial: false }, nonCommercial: false }],
        totalBytes: 1_500_000_000,
        freeBytes: 400_000_000,
        enoughSpace: false,
        ...over,
    };
}

function speechModel(over: Record<string, unknown> = {}) {
    return {
        supported: true,
        name: "Qwen3-ASR 0.6B",
        downloadBytes: 878_702_423,
        diskBytes: 970_000_000,
        enabled: true,
        status: "absent",
        downloaded: 0,
        total: 0,
        lastError: "",
        folder: "/Users/sam/.isoform-agent/sherpa/qwen3-asr",
        ...over,
    };
}

vi.mock("@/lib/wails-shim-app", () => ({
    GetDailyTopics: vi.fn(async () => ({ enabled: false, topics: [] })),
    SetDailyTopicsEnabled: vi.fn(async (enabled: boolean) => ({ enabled, topics: [] })),
    IsClosedEyesModeEnabled: mocks.isPaused,
    GetPlatformPermissions: mocks.getPermissions,
    SetPermissionEnabled: mocks.setPermission,
    RequestPermission: mocks.requestPermission,
    OpenPermissionSettings: mocks.openPermissionSettings,
    GetBackgroundListening: mocks.getListening,
    SetBackgroundListening: mocks.setListening,
    IsSystemAudioBackgroundListeningSupported: mocks.systemAudioSupported,
    GetAutomaticMeetingRecordingEnabled: mocks.getMeetings,
    SetAutomaticMeetingRecordingEnabled: mocks.setMeetings,
    GetSystemAudioOutsideCallsEnabled: mocks.getSystemAudioOutsideCalls,
    SetSystemAudioOutsideCallsEnabled: mocks.setSystemAudioOutsideCalls,
    GetAfterCallSpeechModel: mocks.getSpeechModel,
    SetAfterCallSpeechModelEnabled: mocks.setSpeechModel,
    RemoveAfterCallSpeechModel: mocks.removeSpeechModel,
    GetBackgroundAudioStatus: mocks.audioStatus,
    GetActivityMonitorStatus: mocks.monitorStatus,
    GetCalendarOverview: mocks.calendarOverview,
    SetCalendarPeekEnabled: mocks.setCalendarPeek,
    SyncCalendarsNow: mocks.syncCalendars,
    GetRemindersOverview: mocks.remindersOverview,
    SyncRemindersNow: mocks.syncReminders,
    GetFilesOverview: mocks.filesOverview,
    SyncFilesNow: mocks.syncFiles,
    AddFilesPrivateFolder: mocks.addPrivateFolder,
    RemoveFilesPrivateFolder: mocks.removePrivateFolder,
    GetMailOverview: mocks.mailOverview,
    SyncMailNow: mocks.syncMail,
    GetMessagesOverview: mocks.messagesOverview,
    SyncMessagesNow: mocks.syncMessages,
    SetPeopleLeftOut: mocks.setLeftOut,
    ForgetMessages: mocks.forgetMessages,
    ListPrincipals: mocks.listPrincipals,
    GetCaptureCounts: mocks.captureCounts,
    GetWatchListening: mocks.getWatchListening,
    TurnOffWatchListening: mocks.turnOffWatchListening,
    GetReadAssistantHistory: mocks.getReadHistory,
    SetReadAssistantHistory: mocks.setReadHistory,
    LoadUIState: vi.fn(async () => ""),
    SaveUIState: vi.fn(async () => undefined),
    GetAssistantChoice: mocks.assistantChoice,
    GetModelChoices: mocks.modelChoices,
    GetActivityVision: mocks.getVision,
    SetActivityVisionEnabled: mocks.setVision,
    PlanSetupModelDownload: mocks.planSpeech,
    GetSensitiveApps: vi.fn(async () => []),
    AddSensitiveApp: vi.fn(),
    RemoveSensitiveApp: vi.fn(),
    GetVoiceInputApps: vi.fn(async () => []),
    AddVoiceInputApp: vi.fn(),
    RemoveVoiceInputApp: vi.fn(),
    ListSystemApps: vi.fn(async () => []),
}));

vi.mock("../../../../wailsjs/runtime/runtime", () => ({
    EventsOn: (name: string, handler: (...args: unknown[]) => void) => {
        const set = mocks.handlers.get(name) ?? new Set();
        set.add(handler);
        mocks.handlers.set(name, set);
        return () => set.delete(handler);
    },
    EventsEmit: mocks.emit,
}));

vi.mock("@/lib/browser-bridge", async (importOriginal) => ({
    ...(await importOriginal<typeof import("@/lib/browser-bridge")>()),
    isWailsEnvironment: () => true,
}));

vi.mock("sonner", () => ({ toast: mocks.toast }));

function emit(name: string, ...args: unknown[]) {
    act(() => {
        for (const handler of mocks.handlers.get(name) ?? []) handler(...args);
    });
}

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

function macPermissions(overrides: Record<string, Partial<PermissionItem>> = {}): PlatformPermissions {
    return {
        platform: "darwin",
        items: [
            permission("screen_recording", { required: true, toggleable: true, enabled: true, ...overrides.screen_recording }),
            permission("accessibility", { required: true, toggleable: true, enabled: true, ...overrides.accessibility }),
            permission("microphone", overrides.microphone),
            permission("system_audio", overrides.system_audio),
            permission("calendars", { toggleable: true, enabled: true, ...overrides.calendars }),
            permission("reminders", { toggleable: true, enabled: true, ...overrides.reminders }),
        ],
    } as PlatformPermissions;
}

function calendarOverview(overrides: Record<string, unknown> = {}) {
    return {
        supported: true,
        authorization: "granted",
        granted: true,
        enabled: true,
        lastSyncAt: new Date(Date.now() - 5 * 60_000).toISOString(),
        lastError: "",
        upcomingCount: 3,
        sources: [
            {
                id: "eventkit:g",
                kind: "eventkit",
                provider: "google",
                providerLabel: "Google",
                title: "Google",
                detectedBy: "eventkit",
                calendars: 2,
                lastSeenAt: new Date(Date.now() - 5 * 60_000).toISOString(),
                lastReadAt: new Date(Date.now() - 5 * 60_000).toISOString(),
            },
            {
                id: "web:outlook",
                kind: "web",
                provider: "outlook",
                providerLabel: "Outlook",
                title: "outlook.office.com",
                detectedBy: "history",
                calendars: 0,
                lastSeenAt: new Date(Date.now() - 2 * 3_600_000).toISOString(),
                lastReadAt: "",
            },
        ],
        peekEnabled: true,
        peekLastAt: "",
        peekProviders: ["outlook"],
        ...overrides,
    };
}

function remindersOverview(overrides: Record<string, unknown> = {}) {
    return {
        supported: true,
        authorization: "granted",
        granted: true,
        enabled: true,
        lastSyncAt: new Date(Date.now() - 10 * 60_000).toISOString(),
        lastError: "",
        openCount: 5,
        overdueCount: 2,
        lists: [
            { title: "Work", open: 3, overdue: 2 },
            { title: "", open: 2, overdue: 0 },
        ],
        ...overrides,
    };
}

function filesOverview(overrides: Record<string, unknown> = {}) {
    return {
        supported: true,
        enabled: true,
        lastReadAt: new Date(Date.now() - 3 * 60_000).toISOString(),
        lastError: "",
        todayCount: 4,
        recentCount: 12,
        linkedCount: 2,
        folders: [
            { key: "documents", checked: true, available: true, recent: 9 },
            { key: "desktop", checked: true, available: false, recent: 0 },
            { key: "downloads", checked: true, available: true, recent: 1 },
            { key: "icloud", checked: false, available: false, recent: 0 },
        ],
        privateFolders: [{ id: "f1a2", displayPath: "~/Documents/Taxes" }],
        ...overrides,
    };
}

function withFiles(base: PlatformPermissions, overrides: Partial<PermissionItem> = {}): PlatformPermissions {
    return {
        ...base,
        items: [
            ...base.items,
            permission("files", { toggleable: true, enabled: true, actionable: false, actionLabel: "", ...overrides }),
        ],
    };
}

function mailOverview(overrides: Record<string, unknown> = {}) {
    return {
        supported: true,
        granted: true,
        enabled: true,
        mailOpen: true,
        lastReadAt: new Date(Date.now() - 4 * 60_000).toISOString(),
        lastError: "",
        stalled: false,
        received: 42,
        waiting: 2,
        ...overrides,
    };
}

function messagesOverview(overrides: Record<string, unknown> = {}) {
    return {
        supported: true,
        granted: true,
        enabled: true,
        lastReadAt: new Date(Date.now() - 4 * 60_000).toISOString(),
        lastError: "",
        asks: 3,
        commitments: 1,
        leftOut: [],
        ...overrides,
    };
}

function withMessages(base: PlatformPermissions, overrides: Partial<PermissionItem> = {}): PlatformPermissions {
    return {
        ...base,
        items: [...base.items, permission("messages_history", { toggleable: true, enabled: true, ...overrides })],
    };
}

function withMail(base: PlatformPermissions, overrides: Partial<PermissionItem> = {}): PlatformPermissions {
    return {
        ...base,
        items: [...base.items, permission("automation_mail", { toggleable: true, enabled: true, ...overrides })],
    };
}

describe("ObservationPage", () => {
    beforeEach(() => {
        resetPageQueries();
        vi.clearAllMocks();
        mocks.handlers.clear();
        localStorage.clear();
        mocks.isPaused.mockResolvedValue(false);
        mocks.captureCounts.mockResolvedValue({ paused: false, notReady: false, todayStart: 0, weekStart: 0, sources: [] });
        mocks.getPermissions.mockResolvedValue(macPermissions());
        mocks.setPermission.mockResolvedValue(undefined);
        mocks.requestPermission.mockResolvedValue(false);
        mocks.openPermissionSettings.mockResolvedValue(undefined);
        mocks.getListening.mockResolvedValue(true);
        mocks.setListening.mockResolvedValue(undefined);
        mocks.systemAudioSupported.mockResolvedValue(true);
        mocks.getMeetings.mockResolvedValue(true);
        mocks.setMeetings.mockResolvedValue(undefined);
        mocks.getSystemAudioOutsideCalls.mockResolvedValue(false);
        mocks.setSystemAudioOutsideCalls.mockResolvedValue(undefined);
        mocks.getSpeechModel.mockResolvedValue(speechModel({ status: "ready", removable: true }));
        mocks.setSpeechModel.mockResolvedValue(undefined);
        mocks.removeSpeechModel.mockResolvedValue(undefined);
        mocks.audioStatus.mockResolvedValue({ running: true, activeSources: ["microphone"], screenLocked: false });
        mocks.monitorStatus.mockResolvedValue({ monitoring: true, snapshotCount: 0, storageMB: 0 });
        mocks.calendarOverview.mockResolvedValue(calendarOverview());
        mocks.setCalendarPeek.mockResolvedValue(undefined);
        mocks.syncCalendars.mockResolvedValue(undefined);
        mocks.remindersOverview.mockResolvedValue(remindersOverview());
        mocks.syncReminders.mockResolvedValue(undefined);
        mocks.filesOverview.mockResolvedValue(filesOverview());
        mocks.syncFiles.mockResolvedValue(undefined);
        mocks.addPrivateFolder.mockResolvedValue("/Users/sam/Documents/Health");
        mocks.removePrivateFolder.mockResolvedValue(undefined);
        mocks.mailOverview.mockResolvedValue(mailOverview());
        mocks.syncMail.mockResolvedValue(undefined);
        mocks.messagesOverview.mockResolvedValue(messagesOverview());
        mocks.syncMessages.mockResolvedValue(undefined);
        mocks.setLeftOut.mockResolvedValue(undefined);
        mocks.forgetMessages.mockResolvedValue(undefined);
        mocks.listPrincipals.mockResolvedValue([
            { id: "p1", kind: "contact", displayName: "Priya" },
            { id: "me", kind: "self", displayName: "Sam" },
        ]);
        mocks.getWatchListening.mockResolvedValue({ off: false, hasWatch: true });
        mocks.turnOffWatchListening.mockResolvedValue({ off: true, since: "2026-11-21T10:00:00Z", hasWatch: true });
        mocks.getReadHistory.mockResolvedValue(true);
        mocks.setReadHistory.mockResolvedValue(undefined);
        mocks.assistantChoice.mockResolvedValue({ answering: "claude-code" });
        mocks.modelChoices.mockResolvedValue({ jobs: [] });
        mocks.getVision.mockResolvedValue({ enabled: true, privacyModelReady: true });
        mocks.setVision.mockImplementation(async (enabled: boolean) => ({ enabled, privacyModelReady: true }));
        mocks.planSpeech.mockResolvedValue(speechPlan({ items: [] }));
    });

    afterEach(() => {
        localStorage.clear();
    });

    it("lists one row per source in the design's order, each with its line and one switch (S2, S2a-S2f)", async () => {
        mocks.getPermissions.mockResolvedValue(withMessages(withMail(macPermissions()), { granted: false, enabled: false }));
        render(<ObservationPage />);

        await waitFor(() => expect(screen.getByRole("switch", { name: "Screen" })).toBeInTheDocument());
        await screen.findByRole("switch", { name: "Your watch" });
        const rows = [...document.querySelectorAll("[data-testid^=sees-row-]")].map((el) => el.getAttribute("data-testid"));
        expect(rows).toEqual(["sees-row-screen", "sees-row-calls", "sees-row-mail", "sees-row-messages", "sees-row-watch", "sees-row-agents"]);
        const words = [
            ["Screen", "What's on screen and your history in Chrome and browsers like it, with the apps you exclude left out"],
            ["Calls and meetings", "What you say and your recorded calls, transcribed on this computer"],
            ["Mail, calendar and reminders", "Your accounts on this computer, read where they are"],
            ["Messages", "Your iMessage and text conversations with people you know, on this computer"],
            ["Your watch", "Talk it hears while on your wrist, by the hour"],
            ["Your agents' history", "What Claude Code and Codex did on this computer"],
        ];
        for (const [title, line] of words) {
            expect(screen.getByText(title)).toBeInTheDocument();
            expect(screen.getByText(line)).toBeInTheDocument();
        }
        for (const id of ["screen", "calls", "mail", "messages", "watch", "agents"]) {
            const row = screen.getByTestId(`sees-row-${id}`);
            await waitFor(() => expect(within(row).getAllByRole("switch")).toHaveLength(1));
        }
        // The cards live in the details now.
        expect(screen.queryByText("Watch my screen")).toBeNull();
        expect(screen.queryByText("Calendars Isoform can see")).toBeNull();
        // computer-settings v1.3.0 rule 1: Proposals live on Briefs and notifications.
        expect(screen.queryByText("Proposals from your topics")).toBeNull();
    });

    it("has no Copy, no calendar holds, no Privacy links and no Pause (S26)", async () => {
        for (const source of [undefined, "sees-mail", "sees-screen", "sees-calls"]) {
            const { unmount } = render(<ObservationPage anchor={source} />);
            await waitFor(() => expect(mocks.getPermissions).toHaveBeenCalled());
            expect(screen.queryByText("Note what I copy")).toBeNull();
            expect(screen.queryByRole("switch", { name: "Offer to hold time" })).toBeNull();
            expect(screen.queryByText("On the Privacy page")).toBeNull();
            expect(screen.queryByRole("button", { name: "Open Privacy" })).toBeNull();
            expect(screen.queryByText(/Pause observing/)).toBeNull();
            unmount();
        }
    });

    it("opens each source's details in place and comes back to the rows (S3)", async () => {
        mocks.getPermissions.mockResolvedValue(withFiles(withMessages(withMail(macPermissions()))));
        render(<ObservationPage />);
        const details: [string, string, string][] = [
            ["Screen", "screen", "Watch my screen"],
            ["Calls and meetings", "calls", "Listen"],
            ["Mail, calendar and reminders", "mail", "Calendars Isoform can see"],
            ["Messages", "messages", "Messages Isoform can see"],
            ["Your agents' history", "agents", "Read my Claude Code and Codex history"],
        ];
        for (const [title, id, card] of details) {
            fireEvent.click(await screen.findByRole("button", { name: `Open ${title}` }));
            expect(await screen.findByTestId(`sees-details-${id}`)).toBeInTheDocument();
            expect(await screen.findByText(card)).toBeInTheDocument();
            fireEvent.click(screen.getByRole("button", { name: "What Isoform sees" }));
            await screen.findByTestId("sees-rows");
        }
        // Screen holds the apps Isoform never sees and Files (T2); Calls the apps it doesn't listen to.
        fireEvent.click(screen.getByRole("button", { name: "Open Screen" }));
        expect(await screen.findByText("Hidden from screen recordings")).toBeInTheDocument();
        expect(await screen.findByText("Recent files")).toBeInTheDocument();
        fireEvent.click(screen.getByRole("button", { name: "What Isoform sees" }));
        fireEvent.click(await screen.findByRole("button", { name: "Open Calls and meetings" }));
        expect(await screen.findByText("Apps Isoform does not listen to")).toBeInTheDocument();
        fireEvent.click(screen.getByRole("button", { name: "What Isoform sees" }));
        fireEvent.click(await screen.findByRole("button", { name: "Open Mail, calendar and reminders" }));
        expect(await screen.findByText("Reminders Isoform can see")).toBeInTheDocument();
        expect(screen.getByText("Mail Isoform can see")).toBeInTheDocument();
        // Your watch has no details.
        fireEvent.click(screen.getByRole("button", { name: "What Isoform sees" }));
        await screen.findByTestId("sees-rows");
        expect(screen.queryByRole("button", { name: "Open Your watch" })).toBeNull();
    }, 20_000);

    it("lands deep links on their source's details (sees-screen, sees-calls, sees-mail, sees-calendar, sees-messages)", async () => {
        const cases: [string, string][] = [
            ["sees-screen", "screen"],
            ["sees-calls", "calls"],
            ["sees-mail", "mail"],
            ["sees-calendar", "mail"],
            ["sees-messages", "messages"],
            ["sees-assistant", "agents"],
        ];
        for (const [anchor, id] of cases) {
            const { container, unmount } = render(<ObservationPage anchor={anchor} />);
            expect(await screen.findByTestId(`sees-details-${id}`)).toBeInTheDocument();
            expect(container.querySelector(`[data-settings-anchor="sees-${id}"]`)).not.toBeNull();
            if (anchor === "sees-calendar") expect(container.querySelector('[data-settings-anchor="sees-calendar"]')).not.toBeNull();
            unmount();
        }
        // A new link with no anchor, as from the sidebar, goes back to the rows.
        const { rerender } = render(<ObservationPage anchor="sees-calls" anchorKey={1} />);
        expect(await screen.findByTestId("sees-details-calls")).toBeInTheDocument();
        rerender(<ObservationPage anchorKey={2} />);
        expect(await screen.findByTestId("sees-rows")).toBeInTheDocument();
    });

    it("says it couldn't read a switch, with Try again, never Off", async () => {
        mocks.getPermissions.mockRejectedValueOnce(new Error("no"));
        mocks.getWatchListening.mockRejectedValueOnce(new Error("no"));
        mocks.getReadHistory.mockRejectedValueOnce(new Error("no"));
        mocks.getListening.mockRejectedValueOnce(new Error("no"));
        render(<ObservationPage />);
        for (const id of ["screen", "calls", "mail", "messages", "watch", "agents"]) {
            const row = screen.getByTestId(`sees-row-${id}`);
            await waitFor(() => expect(within(row).getByText("Couldn't read whether this is on")).toBeInTheDocument());
            expect(within(row).queryByRole("switch")).toBeNull();
            expect(within(row).getByRole("button", { name: "Try again" })).toBeInTheDocument();
        }
        fireEvent.click(within(screen.getByTestId("sees-row-watch")).getByRole("button", { name: "Try again" }));
        await waitFor(() => expect(within(screen.getByTestId("sees-row-watch")).getByRole("switch")).toBeChecked());
    });

    it("turns mail, calendar and reminders off together, and shows on while any one reads", async () => {
        mocks.getPermissions.mockResolvedValue(
            withMail(macPermissions({ calendars: { enabled: false }, reminders: { enabled: false } }), { enabled: true }),
        );
        render(<ObservationPage />);
        const row = await screen.findByRole("switch", { name: "Mail, calendar and reminders" });
        await waitFor(() => expect(row).toBeChecked());
        fireEvent.click(row);
        await waitFor(() => expect(mocks.setPermission).toHaveBeenCalledWith("automation_mail", false));
        expect(mocks.setPermission).not.toHaveBeenCalledWith("calendars", expect.anything());
        expect(mocks.setPermission).not.toHaveBeenCalledWith("screen_recording", expect.anything());
    });

    it("hints Messages' Full Disk Access: plain while off, amber while on without it (S4)", async () => {
        mocks.getPermissions.mockResolvedValue(withMessages(macPermissions(), { granted: false, enabled: false }));
        const { unmount } = render(<ObservationPage />);
        const off = await screen.findByTestId("sees-hint-messages");
        expect(off).toHaveTextContent("Needs Full Disk Access · asked when you turn it on");
        expect(off.className).not.toContain("text-warning");
        unmount();

        mocks.getPermissions.mockResolvedValue(withMessages(macPermissions(), { granted: false, enabled: true }));
        render(<ObservationPage />);
        await waitFor(() => expect(screen.getByTestId("sees-hint-messages")).toHaveTextContent(/^Needs Full Disk Access$/));
        expect(screen.getByTestId("sees-hint-messages").className).toContain("text-warning");
        expect(screen.getByRole("switch", { name: "Messages" })).toBeChecked();
    });

    it("asks once when Messages is turned on without access, with Open System Settings and Not now (S4)", async () => {
        mocks.getPermissions.mockResolvedValue(withMessages(macPermissions(), { granted: false, enabled: false }));
        render(<ObservationPage />);
        const messages = await screen.findByRole("switch", { name: "Messages" });
        await waitFor(() => expect(messages).not.toBeDisabled());
        fireEvent.click(messages);
        await waitFor(() => expect(mocks.setPermission).toHaveBeenCalledWith("messages_history", true));
        expect(
            await screen.findByText(
                "Isoform reads your conversations with people you know, to know who's waiting on you and what you promised. It keeps them on this computer, and Claude Code reads them as it reads your screen. It needs Full Disk Access, which you give in System Settings.",
            ),
        ).toBeInTheDocument();
        fireEvent.click(screen.getByRole("button", { name: "Not now" }));
        await waitFor(() => expect(screen.queryByRole("button", { name: "Open System Settings" })).toBeNull());
        expect(mocks.requestPermission).not.toHaveBeenCalled();

        mocks.setPermission.mockClear();
        mocks.getPermissions.mockResolvedValue(withMessages(macPermissions(), { granted: false, enabled: false }));
        fireEvent.click(await screen.findByRole("switch", { name: "Messages" }));
        fireEvent.click(await screen.findByRole("button", { name: "Open System Settings" }));
        await waitFor(() => expect(mocks.requestPermission).toHaveBeenCalledWith("messages_history"));
    });

    it("turns Messages on without asking once Full Disk Access is given", async () => {
        mocks.getPermissions.mockResolvedValue(withMessages(macPermissions(), { granted: true, enabled: false }));
        render(<ObservationPage />);
        const messages = await screen.findByRole("switch", { name: "Messages" });
        await waitFor(() => expect(messages).not.toBeDisabled());
        fireEvent.click(messages);
        await waitFor(() => expect(mocks.setPermission).toHaveBeenCalledWith("messages_history", true));
        expect(screen.queryByRole("button", { name: "Open System Settings" })).toBeNull();
    });

    it("turns listening on the watch off as the phone does, and never on (S2e)", async () => {
        render(<ObservationPage />);
        const watch = await screen.findByRole("switch", { name: "Your watch" });
        await waitFor(() => expect(watch).toBeChecked());
        fireEvent.click(watch);
        await waitFor(() => expect(mocks.turnOffWatchListening).toHaveBeenCalledTimes(1));
        await waitFor(() => expect(screen.getByRole("switch", { name: "Your watch" })).not.toBeChecked());
        expect(screen.getByRole("switch", { name: "Your watch" })).toBeDisabled();
        expect(screen.getByTestId("sees-hint-watch")).toHaveTextContent("Off. Tap the mark on your watch to start listening.");
    });

    it("says there is no watch yet, with no switch", async () => {
        mocks.getWatchListening.mockResolvedValue({ off: false, hasWatch: false });
        render(<ObservationPage />);
        expect(await screen.findByText("No watch with Isoform yet. Put Isoform on your watch from your phone.")).toBeInTheDocument();
        expect(within(screen.getByTestId("sees-row-watch")).queryByRole("switch")).toBeNull();
    });

    it("shows agents' history on unless turned off, with Undo in the row", async () => {
        render(<ObservationPage />);
        const history = await screen.findByRole("switch", { name: "Your agents' history" }, { timeout: 3000 });
        expect(history).toBeChecked();
        fireEvent.click(history);
        expect(await screen.findByText(/Stopping in 5 seconds/)).toBeInTheDocument();
        fireEvent.click(screen.getByRole("button", { name: "Undo" }));
        await waitFor(() => expect(screen.getByRole("switch", { name: "Your agents' history" })).toBeChecked());
        expect(mocks.setReadHistory).not.toHaveBeenCalled();
    });

    it("says what Watch my screen keeps, typed text and Chromium history included (ST2 v1.0.1)", async () => {
        render(<ObservationPage anchor="sees-screen" />);
        await screen.findByText("Watch my screen");
        expect(
            screen.getByText(
                "Isoform looks at the apps and windows you use, and keeps the text you type in them, so it can learn how you work and help at the right moment. It also reads the history of Chrome and other Chromium browsers on this computer (not Safari, never private windows), so the sites you visit most and what you searched for can appear in your brief. Password fields, private windows and the apps you ignore stay out. Turn this off if you would rather it never sees your screen.",
            ),
        ).toBeInTheDocument();
    });

    it("shows the call transcription model and removes it", async () => {
        render(<ObservationPage anchor="sees-calls" />);
        expect(
            await screen.findByText(/Qwen3-ASR 0.6B, .* on this computer\. A call that ends while it is ready is transcribed again with it\./),
        ).toBeInTheDocument();
        mocks.getSpeechModel.mockResolvedValue(speechModel({ status: "absent", enabled: false }));
        fireEvent.click(screen.getByRole("button", { name: "Remove" }));
        await waitFor(() => expect(mocks.removeSpeechModel).toHaveBeenCalled());
        expect(
            await screen.findByText("Off. Calls are transcribed with the built-in model, and Qwen3-ASR 0.6B is not downloaded."),
        ).toBeInTheDocument();
        expect(screen.getByRole("switch", { name: "Call transcription model" })).not.toBeChecked();
    });

    it("says why the call transcription model is missing", async () => {
        mocks.getSpeechModel.mockResolvedValue(
            speechModel({ status: "low_disk", lastError: "not enough free disk space: 900 MB free, about 2848 MB needed" }),
        );
        render(<ObservationPage anchor="sees-calls" />);
        expect(
            await screen.findByText(
                "Qwen3-ASR 0.6B was not downloaded: not enough free disk space: 900 MB free, about 2848 MB needed. It tries again after a later call.",
            ),
        ).toBeInTheDocument();
        expect(screen.queryByRole("button", { name: "Remove" })).not.toBeInTheDocument();
    });

    it("offers Remove for a partial download left on disk", async () => {
        mocks.getSpeechModel.mockResolvedValue(speechModel({ status: "absent", enabled: false, removable: true }));
        render(<ObservationPage anchor="sees-calls" />);
        expect(await screen.findByRole("button", { name: "Remove" })).toBeInTheDocument();
    });

    it("keeps the computer's sound outside calls off until turned on", async () => {
        render(<ObservationPage anchor="sees-calls" />);
        const toggle = await screen.findByRole("switch", { name: "Hear the computer outside calls" });
        await waitFor(() => expect(toggle).not.toBeDisabled());
        expect(toggle).not.toBeChecked();

        fireEvent.click(toggle);
        await waitFor(() => expect(mocks.setSystemAudioOutsideCalls).toHaveBeenCalledWith(true));
        expect(toggle).toBeChecked();
    });

    it("says on Calls and meetings that speech recognition needs room, in amber, until the model is here (S9)", async () => {
        mocks.planSpeech.mockResolvedValue(speechPlan());
        render(<ObservationPage />);
        const hint = await screen.findByTestId("sees-speech-space");
        const need = within(hint).getByText(/^Speech recognition needs .+ free · .+ free now$/);
        expect(need.className).toContain("text-warning");
        expect(within(hint).getByText("Calls and your watch's talk are kept, and transcribed once it's here.").className).not.toContain("text-warning");
        expect(within(screen.getByTestId("sees-row-calls")).getByTestId("sees-speech-space")).toBe(hint);
        expect(mocks.planSpeech).toHaveBeenCalledWith(expect.any(Object), ["privacy", "memory"]);
    });

    it("has no speech hint once the model is here, or while there is room for it", async () => {
        render(<ObservationPage />);
        await waitFor(() => expect(mocks.planSpeech).toHaveBeenCalled());
        expect(screen.queryByTestId("sees-speech-space")).toBeNull();
        mocks.planSpeech.mockResolvedValue(speechPlan({ enoughSpace: true }));
        act(() => { window.dispatchEvent(new Event("focus")); });
        await waitFor(() => expect(mocks.planSpeech).toHaveBeenCalledTimes(2));
        expect(screen.queryByTestId("sees-speech-space")).toBeNull();
        expect(screen.queryByText(/Speech recognition needs/)).toBeNull();
    });

    it("says it couldn't read the speech model, with Try again, never a size", async () => {
        mocks.planSpeech.mockRejectedValueOnce(new Error("no"));
        render(<ObservationPage />);
        const row = screen.getByTestId("sees-row-calls");
        expect(await within(row).findByText("Couldn't read whether speech recognition is on this computer.")).toBeInTheDocument();
        expect(within(row).queryByText(/Speech recognition needs/)).toBeNull();
        mocks.planSpeech.mockResolvedValue(speechPlan());
        fireEvent.click(within(row).getByRole("button", { name: "Try again" }));
        expect(await within(row).findByTestId("sees-speech-space")).toBeInTheDocument();
    });

    it("shows no speech hint while listening is off", async () => {
        mocks.getListening.mockResolvedValue(false);
        mocks.planSpeech.mockResolvedValue(speechPlan());
        render(<ObservationPage />);
        await screen.findByRole("switch", { name: "Calls and meetings" });
        expect(screen.queryByTestId("sees-speech-space")).toBeNull();
        expect(mocks.planSpeech).not.toHaveBeenCalled();
    });

    it("puts WeChat pictures in Screen's details with their own switch, naming the assistant (S18)", async () => {
        render(<ObservationPage anchor="sees-screen" />);
        const words = "WeChat windows as pictures, when their text can't be read · sent to Claude Code";
        const toggle = await screen.findByRole("switch", { name: words });
        expect(within(screen.getByTestId("sees-details-screen")).getByTestId("sees-wechat-pictures")).toBeInTheDocument();
        expect(toggle).toBeChecked();
        expect(screen.queryByText(/Pictures wait until the privacy model/)).toBeNull();
        fireEvent.click(toggle);
        await waitFor(() => expect(mocks.setVision).toHaveBeenCalledWith(false));
        await waitFor(() => expect(screen.getByRole("switch", { name: words })).not.toBeChecked());
        expect(mocks.setPermission).not.toHaveBeenCalled();
    });

    it("says WeChat pictures wait while the privacy model is missing (S13)", async () => {
        mocks.getVision.mockResolvedValue({ enabled: true, privacyModelReady: false });
        render(<ObservationPage anchor="sees-screen" />);
        expect(
            await screen.findByText("Pictures wait until the privacy model, which masks pictures of your screen, is on this computer."),
        ).toBeInTheDocument();
    });

    it("says it couldn't read the WeChat pictures switch, with Try again and no switch", async () => {
        mocks.getVision.mockRejectedValueOnce(new Error("no"));
        render(<ObservationPage anchor="sees-screen" />);
        const card = await screen.findByTestId("sees-wechat-pictures");
        expect(await within(card).findByText("Couldn't read whether this is on")).toBeInTheDocument();
        expect(within(card).queryByRole("switch")).toBeNull();
        fireEvent.click(within(card).getByRole("button", { name: "Try again" }));
        expect(await within(card).findByRole("switch")).toBeChecked();
    });

    it("follows Closed Eyes from any surface: the sources wait while eyes are closed", async () => {
        render(<ObservationPage />);
        await waitFor(() => expect(screen.getByRole("switch", { name: "Screen" })).not.toBeDisabled());

        emit("closed-eyes:changed", true);
        await waitFor(() => expect(screen.getByRole("switch", { name: "Screen" })).toBeDisabled());
        expect(screen.getByRole("switch", { name: "Calls and meetings" })).toBeDisabled();
        expect(screen.getByText("Closed Eyes is on. Nothing is seen or kept until you open them.")).toBeInTheDocument();

        emit("closed-eyes:changed", false);
        await waitFor(() => expect(screen.getByRole("switch", { name: "Screen" })).not.toBeDisabled());
    });

    it("writes every screen permission key so they always agree", async () => {
        render(<ObservationPage anchor="sees-screen" />);
        const watch = screen.getByRole("switch", { name: "Watch my screen" });
        await waitFor(() => expect(watch).toBeChecked());
        await waitFor(() => expect(watch).not.toBeDisabled());

        fireEvent.click(watch);

        await waitFor(() => expect(mocks.setPermission).toHaveBeenCalledTimes(2));
        expect(mocks.setPermission).toHaveBeenCalledWith("screen_recording", false);
        expect(mocks.setPermission).toHaveBeenCalledWith("accessibility", false);
        expect(mocks.setPermission).not.toHaveBeenCalledWith("microphone", expect.anything());
        expect(mocks.setPermission).not.toHaveBeenCalledWith("calendars", expect.anything());
        expect(mocks.setPermission).not.toHaveBeenCalledWith("reminders", expect.anything());
    });

    it("lists the calendars Isoform can see with their source and last read time", async () => {
        render(<ObservationPage anchor="sees-mail" />);

        expect(await screen.findByText("Calendars Isoform can see")).toBeInTheDocument();
        expect(await screen.findByText("Google")).toBeInTheDocument();
        expect(screen.getByText(/2 calendars/)).toBeInTheDocument();
        expect(screen.getByText(/from this computer's accounts/)).toBeInTheDocument();
        expect(screen.getAllByText(/Last read 5 minutes ago/)).toHaveLength(2);
        expect(screen.getByText(/Outlook \u00b7 outlook.office.com/)).toBeInTheDocument();
        expect(screen.getByText(/seen in your browser history/)).toBeInTheDocument();
        expect(screen.getByText(/Last used 2 hours ago/)).toBeInTheDocument();
        expect(screen.getByText("On this computer")).toBeInTheDocument();
        expect(screen.getByText("In the browser")).toBeInTheDocument();
        expect(screen.getByText(/3 events in the next two weeks/)).toBeInTheDocument();
        expect(screen.getByText(/Will read: Outlook/)).toBeInTheDocument();
        expect(screen.queryByText("Allow")).not.toBeInTheDocument();
    });

    it("turns web calendar reading off and on", async () => {
        render(<ObservationPage anchor="sees-mail" />);
        const peek = await screen.findByRole("switch", { name: "Read web calendars when the computer is idle" });
        await waitFor(() => expect(peek).not.toBeDisabled());
        expect(peek).toBeChecked();

        fireEvent.click(peek);
        await waitFor(() => expect(mocks.setCalendarPeek).toHaveBeenCalledWith(false));
        await waitFor(() => expect(peek).not.toBeChecked());
    });

    it("keeps Watch my screen on and says so in copper with a working Try again when the change fails (ST9)", async () => {
        mocks.setPermission.mockRejectedValueOnce(new Error("denied"));
        render(<ObservationPage anchor="sees-screen" />);
        const watch = screen.getByRole("switch", { name: "Watch my screen" });
        await waitFor(() => expect(watch).not.toBeDisabled());

        fireEvent.click(watch);

        const alert = await screen.findByRole("alert");
        expect(alert).toHaveTextContent("Couldn't change this.");
        expect(alert.className).toContain("text-copper");
        expect(alert.className).not.toContain("text-error");
        expect(mocks.toast.error).not.toHaveBeenCalled();
        await waitFor(() => expect(watch).toBeChecked());

        const calls = mocks.setPermission.mock.calls.length;
        fireEvent.click(within(alert).getByRole("button", { name: "Try again" }));
        await waitFor(() => expect(mocks.setPermission.mock.calls.length).toBeGreaterThan(calls));
        expect(mocks.setPermission).toHaveBeenLastCalledWith(expect.any(String), false);
        await waitFor(() => expect(screen.queryByText("Couldn't change this.")).not.toBeInTheDocument());
    });

    it("switches calendar reading off through the permission toggle", async () => {
        render(<ObservationPage anchor="sees-mail" />);
        const reading = await screen.findByRole("switch", { name: "Read my calendars" });
        await waitFor(() => expect(reading).not.toBeDisabled());
        expect(reading).toBeChecked();

        fireEvent.click(reading);
        await waitFor(() => expect(mocks.setPermission).toHaveBeenCalledWith("calendars", false));
    });

    it("asks for calendar access inline when the Mac has not granted it", async () => {
        mocks.getPermissions.mockResolvedValue(macPermissions({ calendars: { granted: false } }));
        mocks.calendarOverview.mockResolvedValue(calendarOverview({ granted: false, authorization: "not_determined", sources: [], peekProviders: [] }));
        render(<ObservationPage anchor="sees-mail" />);

        expect(
            await screen.findByText("Your computer has not given Isoform access to your calendars yet. Allow it below."),
        ).toBeInTheDocument();
        expect(screen.getByText("No calendars seen yet. They appear here once Isoform has access, or after you open a web calendar in your browser.")).toBeInTheDocument();
        expect(screen.queryByRole("switch", { name: "Read my calendars" })).not.toBeInTheDocument();

        const allow = screen.getAllByRole("button", { name: "Allow" });
        fireEvent.click(allow[allow.length - 1]);
        await waitFor(() => expect(mocks.requestPermission).toHaveBeenCalledWith("calendars"));
    });

    it("shows the open reminders per list next to the calendars", async () => {
        render(<ObservationPage anchor="sees-mail" />);

        const card = (await screen.findByText("Reminders Isoform can see")).closest(".settings-card") as HTMLElement;
        expect(await within(card).findByText("Work")).toBeInTheDocument();
        expect(within(card).getByText("3 open \u00b7 2 overdue")).toBeInTheDocument();
        expect(within(card).getByText("Untitled list")).toBeInTheDocument();
        expect(within(card).getByText("2 open")).toBeInTheDocument();
        expect(within(card).getByText("5 open reminders \u00b7 2 overdue \u00b7 Last read 10 minutes ago")).toBeInTheDocument();

        fireEvent.click(within(card).getByRole("button", { name: "Refresh" }));
        await waitFor(() => expect(mocks.syncReminders).toHaveBeenCalledTimes(1));

        // A read finished elsewhere refreshes the card.
        mocks.remindersOverview.mockResolvedValue(remindersOverview({ openCount: 1, overdueCount: 0, lists: [] }));
        emit("reminders:synced");
        expect(await within(card).findByText("No open reminders.")).toBeInTheDocument();
    });

    it("switches reminders reading off through the permission toggle", async () => {
        render(<ObservationPage anchor="sees-mail" />);
        const reading = await screen.findByRole("switch", { name: "Read my reminders" });
        await waitFor(() => expect(reading).not.toBeDisabled());
        expect(reading).toBeChecked();

        fireEvent.click(reading);
        await waitFor(() => expect(mocks.setPermission).toHaveBeenCalledWith("reminders", false));
        expect(mocks.setPermission).not.toHaveBeenCalledWith("screen_recording", expect.anything());
    });

    it("asks for reminders access inline only from the Allow button", async () => {
        mocks.getPermissions.mockResolvedValue(macPermissions({ reminders: { granted: false } }));
        mocks.remindersOverview.mockResolvedValue(
            remindersOverview({ granted: false, authorization: "not_determined", openCount: 0, overdueCount: 0, lists: [] }),
        );
        render(<ObservationPage anchor="sees-mail" />);

        const card = (await screen.findByText("Reminders Isoform can see")).closest(".settings-card") as HTMLElement;
        expect(
            await within(card).findByText("Your computer has not given Isoform access to your reminders yet. Allow it below."),
        ).toBeInTheDocument();
        expect(within(card).queryByRole("switch", { name: "Read my reminders" })).not.toBeInTheDocument();
        expect(within(card).getByRole("button", { name: "Refresh" })).toBeDisabled();
        expect(mocks.requestPermission).not.toHaveBeenCalled();

        fireEvent.click(within(card).getByRole("button", { name: "Allow" }));
        await waitFor(() => expect(mocks.requestPermission).toHaveBeenCalledWith("reminders"));
    });

    it("hides the reminders card where the computer has no reminders", async () => {
        mocks.getPermissions.mockResolvedValue({
            platform: "windows",
            items: [permission("screen_capture", { required: true, toggleable: true, enabled: true })],
        } as PlatformPermissions);
        mocks.remindersOverview.mockResolvedValue(remindersOverview({ supported: false, granted: false }));
        render(<ObservationPage anchor="sees-mail" />);

        await waitFor(() => expect(mocks.remindersOverview).toHaveBeenCalled());
        await waitFor(() => expect(screen.queryByText("Reminders Isoform can see")).not.toBeInTheDocument());
    });

    it("shows the folders Isoform can see and the documents worked on today", async () => {
        mocks.getPermissions.mockResolvedValue(withFiles(macPermissions()));
        render(<ObservationPage anchor="sees-screen" />);

        const card = (await screen.findByText("Recent files")).closest(".settings-card") as HTMLElement;
        expect(
            await within(card).findByText("4 documents today \u00b7 2 linked to your commitments \u00b7 Last read 3 minutes ago"),
        ).toBeInTheDocument();
        expect(within(card).getByText("Documents")).toBeInTheDocument();
        expect(within(card).getByText("9 documents in the last two weeks")).toBeInTheDocument();
        expect(within(card).getByText("1 document in the last two weeks")).toBeInTheDocument();
        expect(
            within(card).getByText("Not available. macOS has not opened this folder to Isoform, and Isoform does not ask."),
        ).toBeInTheDocument();
        expect(within(card).getByText("Not read yet")).toBeInTheDocument();
        // No permission sits behind recent files: nothing to allow.
        expect(within(card).queryByRole("button", { name: "Allow" })).not.toBeInTheDocument();

        fireEvent.click(within(card).getByRole("button", { name: "Refresh" }));
        await waitFor(() => expect(mocks.syncFiles).toHaveBeenCalledTimes(1));

        mocks.filesOverview.mockResolvedValue(filesOverview({ todayCount: 5, linkedCount: 0 }));
        emit("files:read");
        expect(await within(card).findByText("5 documents today \u00b7 Last read 3 minutes ago")).toBeInTheDocument();
    });

    it("switches recent files off and keeps them out of Watch my screen", async () => {
        mocks.getPermissions.mockResolvedValue(withFiles(macPermissions()));
        render(<ObservationPage anchor="sees-screen" />);

        const files = await screen.findByRole("switch", { name: "Note my recent files" });
        await waitFor(() => expect(files).not.toBeDisabled());
        expect(files).toBeChecked();
        fireEvent.click(files);
        await waitFor(() => expect(mocks.setPermission).toHaveBeenCalledWith("files", false));

        mocks.setPermission.mockClear();
        fireEvent.click(screen.getByRole("switch", { name: "Watch my screen" }));
        await waitFor(() => expect(mocks.setPermission).toHaveBeenCalledTimes(2));
        expect(mocks.setPermission).not.toHaveBeenCalledWith("files", expect.anything());
    });

    it("hides the folders while recent files are off", async () => {
        mocks.getPermissions.mockResolvedValue(withFiles(macPermissions(), { enabled: false }));
        mocks.filesOverview.mockResolvedValue(filesOverview({ enabled: false }));
        render(<ObservationPage anchor="sees-screen" />);

        const card = (await screen.findByText("Recent files")).closest(".settings-card") as HTMLElement;
        expect(await within(card).findByText("Recent files are off and Isoform notes none of them.")).toBeInTheDocument();
        expect(within(card).queryByText("Documents")).not.toBeInTheDocument();
        expect(within(card).getByRole("button", { name: "Refresh" })).toBeDisabled();
    });

    it("adds and removes private folders", async () => {
        mocks.getPermissions.mockResolvedValue(withFiles(macPermissions()));
        render(<ObservationPage anchor="sees-screen" />);

        const card = (await screen.findByText("Recent files")).closest(".settings-card") as HTMLElement;
        expect(await within(card).findByText("~/Documents/Taxes")).toBeInTheDocument();

        fireEvent.click(within(card).getByRole("button", { name: "Add a folder" }));
        await waitFor(() => expect(mocks.addPrivateFolder).toHaveBeenCalledWith("Choose a folder to keep private"));

        fireEvent.click(await within(card).findByRole("button", { name: "Stop keeping ~/Documents/Taxes private" }));
        await waitFor(() => expect(mocks.removePrivateFolder).toHaveBeenCalledWith("f1a2"));
    });

    it("pauses recent files while observing is paused", async () => {
        mocks.isPaused.mockResolvedValue(true);
        mocks.getPermissions.mockResolvedValue(withFiles(macPermissions()));
        render(<ObservationPage anchor="sees-screen" />);

        const files = await screen.findByRole("switch", { name: "Note my recent files" });
        await waitFor(() => expect(files).toBeDisabled());
        const card = files.closest(".settings-card") as HTMLElement;
        expect(within(card).getByText("Observing is paused. Resume it to change this.")).toBeInTheDocument();
        expect(within(card).getByRole("button", { name: "Refresh" })).toBeDisabled();
    });

    it("hides the recent files card where the computer has no Spotlight", async () => {
        render(<ObservationPage anchor="sees-screen" />);
        await waitFor(() => expect(mocks.getPermissions).toHaveBeenCalled());
        await screen.findByText("Watch my screen");
        expect(screen.queryByText("Recent files")).not.toBeInTheDocument();
    });

    it("shows the mail Isoform sees and who is waiting on a reply", async () => {
        mocks.getPermissions.mockResolvedValue(withMail(macPermissions()));
        render(<ObservationPage anchor="sees-mail" />);

        const card = (await screen.findByText("Mail Isoform can see")).closest(".settings-card") as HTMLElement;
        expect(
            await within(card).findByText("42 messages this week \u00b7 2 waiting on your reply \u00b7 Last read 4 minutes ago"),
        ).toBeInTheDocument();
        fireEvent.click(within(card).getByRole("button", { name: "Refresh" }));
        await waitFor(() => expect(mocks.syncMail).toHaveBeenCalledTimes(1));

        mocks.mailOverview.mockResolvedValue(mailOverview({ received: 43, waiting: 0 }));
        emit("mail:synced");
        expect(await within(card).findByText("43 messages this week \u00b7 Last read 4 minutes ago")).toBeInTheDocument();
    });

    it("says reading stopped after Mail was slow, and that Refresh tries again", async () => {
        mocks.getPermissions.mockResolvedValue(withMail(macPermissions()));
        mocks.mailOverview.mockResolvedValue(mailOverview({ stalled: true }));
        render(<ObservationPage anchor="sees-mail" />);

        const card = (await screen.findByText("Mail Isoform can see")).closest(".settings-card") as HTMLElement;
        expect(
            await within(card).findByText(
                "Mail was slow to answer several times in a row, so Isoform stopped reading it to keep Mail responsive. Tap Refresh to try again.",
            ),
        ).toBeInTheDocument();
        expect(within(card).getByRole("button", { name: "Refresh" })).not.toBeDisabled();
    });

    it("says Mail is closed and never offers to open it", async () => {
        mocks.getPermissions.mockResolvedValue(withMail(macPermissions()));
        mocks.mailOverview.mockResolvedValue(mailOverview({ mailOpen: false }));
        render(<ObservationPage anchor="sees-mail" />);

        const card = (await screen.findByText("Mail Isoform can see")).closest(".settings-card") as HTMLElement;
        expect(
            await within(card).findByText(
                "Mail is not open. Isoform reads only while Mail is open and never opens it. \u00b7 42 messages this week \u00b7 2 waiting on your reply \u00b7 Last read 4 minutes ago",
            ),
        ).toBeInTheDocument();
        expect(within(card).getByRole("button", { name: "Refresh" })).toBeDisabled();
    });

    it("asks for Mail access only from the Allow button and only while Mail is open", async () => {
        mocks.getPermissions.mockResolvedValue(withMail(macPermissions(), { granted: false }));
        mocks.mailOverview.mockResolvedValue(mailOverview({ granted: false, mailOpen: false, received: 0, waiting: 0 }));
        const { unmount } = render(<ObservationPage anchor="sees-mail" />);

        let card = (await screen.findByText("Mail Isoform can see")).closest(".settings-card") as HTMLElement;
        expect(
            await within(card).findByText("Mail is not open. Open Mail to allow Isoform to read it; Isoform never opens it."),
        ).toBeInTheDocument();
        expect(within(card).queryByRole("button", { name: "Allow" })).not.toBeInTheDocument();
        expect(within(card).queryByRole("switch", { name: "Read my mail headers" })).not.toBeInTheDocument();
        unmount();

        mocks.mailOverview.mockResolvedValue(mailOverview({ granted: false, mailOpen: true, received: 0, waiting: 0 }));
        render(<ObservationPage anchor="sees-mail" />);
        card = (await screen.findByText("Mail Isoform can see")).closest(".settings-card") as HTMLElement;
        expect(
            await within(card).findByText("Your computer has not given Isoform access to Mail yet. Open Mail, then allow it below."),
        ).toBeInTheDocument();
        expect(mocks.requestPermission).not.toHaveBeenCalled();
        fireEvent.click(within(card).getByRole("button", { name: "Allow" }));
        await waitFor(() => expect(mocks.requestPermission).toHaveBeenCalledWith("automation_mail"));
    });

    it("switches mail reading off and keeps it out of Watch my screen", async () => {
        mocks.getPermissions.mockResolvedValue(withMail(macPermissions()));
        render(<ObservationPage anchor="sees-mail" />);

        const reading = await screen.findByRole("switch", { name: "Read my mail headers" });
        await waitFor(() => expect(reading).not.toBeDisabled());
        fireEvent.click(reading);
        await waitFor(() => expect(mocks.setPermission).toHaveBeenCalledWith("automation_mail", false));

        mocks.setPermission.mockClear();
        fireEvent.click(screen.getByRole("button", { name: "What Isoform sees" }));
        const screenSwitch = await screen.findByRole("switch", { name: "Screen" });
        await waitFor(() => expect(screenSwitch).not.toBeDisabled());
        fireEvent.click(screenSwitch);
        await waitFor(() => expect(mocks.setPermission).toHaveBeenCalledTimes(2));
        expect(mocks.setPermission).not.toHaveBeenCalledWith("automation_mail", expect.anything());
    });

    it("pauses mail reading while observing is paused", async () => {
        mocks.isPaused.mockResolvedValue(true);
        mocks.getPermissions.mockResolvedValue(withMail(macPermissions()));
        render(<ObservationPage anchor="sees-mail" />);

        const reading = await screen.findByRole("switch", { name: "Read my mail headers" });
        await waitFor(() => expect(reading).toBeDisabled());
        const card = reading.closest(".settings-card") as HTMLElement;
        expect(within(card).getByText("Observing is paused. Resume it to change this.")).toBeInTheDocument();
        expect(within(card).getByRole("button", { name: "Refresh" })).toBeDisabled();
    });

    it("asks for Full Disk Access only from Grant and opens System Settings", async () => {
        mocks.getPermissions.mockResolvedValue(withMessages(macPermissions(), { granted: false }));
        mocks.messagesOverview.mockResolvedValue(messagesOverview({ granted: false, asks: 0, commitments: 0 }));
        render(<ObservationPage anchor="sees-messages" />);

        const card = (await screen.findByText("Messages Isoform can see")).closest(".settings-card") as HTMLElement;
        expect(await within(card).findByText(/Isoform needs Full Disk Access to read Messages/)).toBeInTheDocument();
        expect(within(card).queryByRole("switch", { name: "Read my Messages" })).not.toBeInTheDocument();
        expect(mocks.requestPermission).not.toHaveBeenCalled();
        expect(mocks.openPermissionSettings).not.toHaveBeenCalled();
        expect(mocks.listPrincipals).not.toHaveBeenCalled();
        fireEvent.click(within(card).getByRole("button", { name: "Allow" }));
        await waitFor(() => expect(mocks.requestPermission).toHaveBeenCalledWith("messages_history"));
        await waitFor(() => expect(mocks.openPermissionSettings).toHaveBeenCalledWith("messages_history"));
    });

    it("leaves a person out, takes them back and forgets what was noted", async () => {
        mocks.getPermissions.mockResolvedValue(withMessages(macPermissions()));
        mocks.messagesOverview.mockResolvedValue(messagesOverview({ leftOut: [{ id: "p2", name: "Lee" }] }));
        render(<ObservationPage anchor="sees-messages" />);

        const reading = await screen.findByRole("switch", { name: "Read my Messages" });
        const card = reading.closest(".settings-card") as HTMLElement;
        expect(await within(card).findByText(/3 asks/)).toBeInTheDocument();
        fireEvent.click(await within(card).findByRole("button", { name: "Stop leaving out Lee" }));
        await waitFor(() => expect(mocks.setLeftOut).toHaveBeenCalledWith([]));

        fireEvent.click(within(card).getByRole("button", { name: "Forget" }));
        await waitFor(() => expect(mocks.forgetMessages).toHaveBeenCalled());

        // The switch is its own; Screen never turns it.
        mocks.setPermission.mockClear();
        fireEvent.click(screen.getByRole("button", { name: "What Isoform sees" }));
        const screenSwitch = await screen.findByRole("switch", { name: "Screen" });
        await waitFor(() => expect(screenSwitch).not.toBeDisabled());
        fireEvent.click(screenSwitch);
        await waitFor(() => expect(mocks.setPermission).toHaveBeenCalledTimes(2));
        expect(mocks.setPermission).not.toHaveBeenCalledWith("messages_history", expect.anything());
    });

    it("shows the Contacts row in Messages' details while Contacts is missing, opening Who you know (settings batch D3)", async () => {
        const withContacts = withMessages(macPermissions());
        withContacts.items.push(permission("contacts", { granted: false, actionable: true }));
        mocks.getPermissions.mockResolvedValue(withContacts);
        const onOpenPeople = vi.fn();
        render(<ObservationPage anchor="sees-messages" onOpenPeople={onOpenPeople} />);
        await screen.findByTestId("sees-details-messages");
        fireEvent.click(await screen.findByRole("button", { name: "Choose in Who you know" }));
        expect(onOpenPeople).toHaveBeenCalledTimes(1);
        expect(screen.getByText("Recognize people from your Contacts")).toBeInTheDocument();
    });

    it("hides the mail card where the computer has no Mail", async () => {
        mocks.getPermissions.mockResolvedValue({
            platform: "windows",
            items: [permission("screen_capture", { required: true, toggleable: true, enabled: true })],
        } as PlatformPermissions);
        mocks.mailOverview.mockResolvedValue(mailOverview({ supported: false, granted: false }));
        render(<ObservationPage anchor="sees-mail" />);

        await waitFor(() => expect(mocks.mailOverview).toHaveBeenCalled());
        await waitFor(() => expect(screen.queryByText("Mail Isoform can see")).not.toBeInTheDocument());
    });

    it("shows Watch my screen as off when only one key was turned off earlier", async () => {
        mocks.getPermissions.mockResolvedValue(macPermissions({ accessibility: { enabled: false } }));
        render(<ObservationPage anchor="sees-screen" />);
        const watch = screen.getByRole("switch", { name: "Watch my screen" });
        await waitFor(() => expect(watch).not.toBeDisabled());
        expect(watch).not.toBeChecked();

        fireEvent.click(watch);

        await waitFor(() => expect(mocks.setPermission).toHaveBeenCalledWith("accessibility", true));
        expect(mocks.setPermission).toHaveBeenCalledTimes(1);
    });

    it("asks for missing screen access inline and disables the switch until granted", async () => {
        mocks.getPermissions.mockResolvedValue(macPermissions({ accessibility: { granted: false } }));
        render(<ObservationPage anchor="sees-screen" />);

        expect(
            await screen.findByText("Your computer has not given Isoform access yet. Allow it below."),
        ).toBeInTheDocument();
        expect(screen.getByRole("switch", { name: "Watch my screen" })).toBeDisabled();
        expect(screen.getByText("Accessibility")).toBeInTheDocument();

        fireEvent.click(screen.getByRole("button", { name: "Allow" }));
        await waitFor(() => expect(mocks.requestPermission).toHaveBeenCalledWith("accessibility"));
    });

    it("disables Record meetings with a reason while listening is off", async () => {
        mocks.getListening.mockResolvedValue(false);
        render(<ObservationPage anchor="sees-calls" />);

        const meetings = screen.getByRole("switch", { name: "Record meetings automatically" });
        await waitFor(() => expect(screen.getByText("Turn on Listen to use this.")).toBeInTheDocument());
        expect(meetings).toBeDisabled();

        const listen = screen.getByRole("switch", { name: "Listen" });
        await waitFor(() => expect(listen).not.toBeDisabled());
        fireEvent.click(listen);

        await waitFor(() => expect(mocks.setListening).toHaveBeenCalledWith(true));
        await waitFor(() => expect(meetings).not.toBeDisabled());
    });

    it("shows microphone access inline only while listening needs it", async () => {
        mocks.getPermissions.mockResolvedValue(macPermissions({ microphone: { granted: false } }));
        render(<ObservationPage anchor="sees-calls" />);

        const listenCard = (await screen.findByText("Used for hearing your voice and conversations.")).closest(".settings-card") as HTMLElement;
        expect(within(listenCard).getByText("Listen")).toBeInTheDocument();
        expect(within(listenCard).getByText("Used for hearing your voice and conversations.")).toBeInTheDocument();
    });

    it("switches the microphone source off without turning listening off", async () => {
        mocks.getPermissions.mockResolvedValue(macPermissions({ microphone: { toggleable: true, enabled: true } }));
        render(<ObservationPage anchor="sees-calls" />);

        const mic = await screen.findByRole("switch", { name: "Use the microphone" });
        await waitFor(() => expect(mic).not.toBeDisabled());
        expect(mic).toBeChecked();
        const card = mic.closest(".settings-card") as HTMLElement;
        expect(within(card).getByText("Listen")).toBeInTheDocument();

        fireEvent.click(mic);
        await waitFor(() => expect(mocks.setPermission).toHaveBeenCalledWith("microphone", false));
        expect(mocks.setListening).not.toHaveBeenCalled();
    });

    it("shows a microphone switched off as off in Capture status, not as a fault", async () => {
        mocks.audioStatus.mockResolvedValue({
            running: true,
            activeSources: ["system_audio"],
            screenLocked: false,
            microphonePaused: true,
        });
        render(<ObservationPage anchor="sees-calls" />);

        const row = (await screen.findByText("Turned off with Use the microphone. System audio can still be heard."))
            .closest(".settings-row") as HTMLElement;
        expect(within(row).getByText("Microphone")).toBeInTheDocument();
        expect(within(row).getByText("Off")).toBeInTheDocument();
    });

    it("shows no capture state when the read fails, says so in copper and reads again", async () => {
        mocks.audioStatus.mockRejectedValueOnce(new Error("bridge down"));
        render(<ObservationPage anchor="sees-calls" />);

        const line = await screen.findByText("Couldn't read what Isoform is capturing.");
        expect(line.closest("[data-settings-failure]")).toHaveClass("text-copper");
        expect(screen.queryByText("Stopped")).not.toBeInTheDocument();

        fireEvent.click(within(line.closest("[data-settings-failure]") as HTMLElement).getByRole("button", { name: "Try again" }));
        await waitFor(() => expect(screen.queryByText("Couldn't read what Isoform is capturing.")).not.toBeInTheDocument());
        expect(screen.getByText("Microphone")).toBeInTheDocument();
    });

    it("shows a spinner on Refresh while capture status reads", async () => {
        let finish: (value: unknown) => void = () => {};
        mocks.audioStatus.mockReturnValueOnce(new Promise((resolve) => (finish = resolve)));
        render(<ObservationPage anchor="sees-calls" />);
                expect(await screen.findByTestId("capture-reading")).toBeInTheDocument();
        finish({ running: true, activeSources: ["microphone"], screenLocked: false });
        await waitFor(() => expect(screen.queryByTestId("capture-reading")).not.toBeInTheDocument());
    });

    it("shows no microphone switch until the Mac has given access", async () => {
        mocks.getPermissions.mockResolvedValue(
            macPermissions({ microphone: { toggleable: true, enabled: true, granted: false } }),
        );
        render(<ObservationPage anchor="sees-calls" />);
        expect(await screen.findByText("Microphone")).toBeInTheDocument();
        expect(screen.queryByRole("switch", { name: "Use the microphone" })).not.toBeInTheDocument();
    });

    it("lists what Isoform captured under an anchor Today links to", async () => {
        mocks.captureCounts.mockResolvedValue({
            paused: false,
            notReady: false,
            todayStart: 0,
            weekStart: 0,
            sources: [{ source: "screens", today: 412, week: 2903 }],
        });
        const { container } = render(<ObservationPage />);

        const row = await screen.findByRole("row", { name: /Screens seen/ });
        expect(within(row).getByText("412")).toBeInTheDocument();
        const anchor = container.querySelector('[data-settings-anchor="captured"]');
        expect(anchor).toHaveTextContent("What Isoform captured");
        expect(mocks.captureCounts).toHaveBeenCalledTimes(1);
    });

});
