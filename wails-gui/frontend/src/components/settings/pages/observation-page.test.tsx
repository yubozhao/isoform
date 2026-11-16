import { resetPageQueries } from '@/lib/page-queries';
import "@/i18n";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { PermissionItem, PlatformPermissions } from "@/types";
import { ObservationPage } from "./observation-page";

const mocks = vi.hoisted(() => ({
    handlers: new Map<string, Set<(...args: unknown[]) => void>>(),
    isPaused: vi.fn(),
    setPaused: vi.fn(),
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
    retentionDays: vi.fn(),
    calendarOverview: vi.fn(),
    holdSettings: vi.fn(),
    setOfferHolds: vi.fn(),
    setHoldCalendar: vi.fn(),
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
    emit: vi.fn(),
    toast: { error: vi.fn(), success: vi.fn(), warning: vi.fn() },
}));

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
    SetClosedEyesModeEnabled: mocks.setPaused,
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
    GetActivityRetentionDays: mocks.retentionDays,
    GetCalendarOverview: mocks.calendarOverview,
    GetHoldSettings: mocks.holdSettings,
    SetOfferHolds: mocks.setOfferHolds,
    SetHoldCalendar: mocks.setHoldCalendar,
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
        mocks.setPaused.mockResolvedValue(undefined);
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
        mocks.retentionDays.mockResolvedValue(30);
        mocks.calendarOverview.mockResolvedValue(calendarOverview());
        mocks.holdSettings.mockResolvedValue({ granted: true, offerHolds: true, calendarId: "work", calendars: [{ id: "work", title: "Work", sourceId: "account", sourceTitle: "Account", isDefault: true }, { id: "home", title: "Home", sourceId: "other", sourceTitle: "Personal", isDefault: false }] });
        mocks.setOfferHolds.mockResolvedValue(undefined);
        mocks.setHoldCalendar.mockResolvedValue(undefined);
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
    });

    afterEach(() => {
        localStorage.clear();
    });

    it("lists the sources in the agreed order, with Closed Eyes on its own page, and keeps the rest under Advanced", async () => {
        render(<ObservationPage />);

        // computer-settings v1.0.0 rule 2: Screen, Calls and meetings, Mail
        // and calendar, Messages.
        const anchors = [...document.querySelectorAll("[data-settings-anchor^=sees-]")].map((el) => el.getAttribute("data-settings-anchor"));
        expect(anchors).toEqual(["sees-screen", "sees-calls", "sees-mail", "sees-calendar", "sees-messages"]);
        expect(screen.queryByRole("switch", { name: "Pause observing" })).toBeNull();
        expect(screen.getByText("Watch my screen")).toBeInTheDocument();
        expect(screen.getByText("Listen")).toBeInTheDocument();
        expect(
            screen.getByText("Capture status, apps Isoform does not listen to and how long recordings are kept"),
        ).toBeInTheDocument();
        expect(screen.queryByText("Capture status")).not.toBeInTheDocument();

        fireEvent.click(screen.getByRole("button", { name: /Advanced/ }));

        expect(await screen.findByText("Capture status")).toBeInTheDocument();
        await waitFor(() => expect(screen.getByText("Kept for 30 days now.")).toBeInTheDocument());
    });

    it("says what Watch my screen keeps, typed text and Chromium history included (ST2 v1.0.1)", async () => {
        render(<ObservationPage />);
        await screen.findByText("Watch my screen");
        expect(
            screen.getByText(
                "Isoform looks at the apps and windows you use, and keeps the text you type in them, so it can learn how you work and help at the right moment. It also reads the history of Chrome and other Chromium browsers on this computer (not Safari, never private windows), so the sites you visit most and what you searched for can appear in your brief. Password fields, private windows and the apps you ignore stay out. Turn this off if you would rather it never sees your screen.",
            ),
        ).toBeInTheDocument();
    });

    it("shows the call transcription model and removes it", async () => {
        render(<ObservationPage />);
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
        render(<ObservationPage />);
        expect(
            await screen.findByText(
                "Qwen3-ASR 0.6B was not downloaded: not enough free disk space: 900 MB free, about 2848 MB needed. It tries again after a later call.",
            ),
        ).toBeInTheDocument();
        expect(screen.queryByRole("button", { name: "Remove" })).not.toBeInTheDocument();
    });

    it("offers Remove for a partial download left on disk", async () => {
        mocks.getSpeechModel.mockResolvedValue(speechModel({ status: "absent", enabled: false, removable: true }));
        render(<ObservationPage />);
        expect(await screen.findByRole("button", { name: "Remove" })).toBeInTheDocument();
    });

    it("keeps the computer's sound outside calls off until turned on", async () => {
        render(<ObservationPage />);
        const toggle = await screen.findByRole("switch", { name: "Hear the computer outside calls" });
        await waitFor(() => expect(toggle).not.toBeDisabled());
        expect(toggle).not.toBeChecked();

        fireEvent.click(toggle);
        await waitFor(() => expect(mocks.setSystemAudioOutsideCalls).toHaveBeenCalledWith(true));
        expect(toggle).toBeChecked();
    });

    it("follows Closed Eyes from any surface: the sources wait while eyes are closed", async () => {
        render(<ObservationPage />);
        await waitFor(() => expect(screen.getByRole("switch", { name: "Watch my screen" })).not.toBeDisabled());

        emit("closed-eyes:changed", true);
        await waitFor(() =>
            expect(screen.getByRole("switch", { name: "Watch my screen" })).toBeDisabled(),
        );
        expect(screen.getByRole("switch", { name: "Listen" })).toBeDisabled();
        expect(screen.getByText("Paused. Isoform is not watching or listening right now.")).toBeInTheDocument();

        emit("closed-eyes:changed", false);
        await waitFor(() =>
            expect(screen.getByRole("switch", { name: "Watch my screen" })).not.toBeDisabled(),
        );
    });

    it("writes every screen permission key so they always agree", async () => {
        render(<ObservationPage />);
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
        render(<ObservationPage />);

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

    it("changes hold offers and remembers a writable calendar", async () => {
        render(<ObservationPage />);
        const offer = await screen.findByRole("switch", { name: "Offer to hold time" });
        expect(offer).toBeChecked();
        fireEvent.click(offer);
        await waitFor(() => expect(mocks.setOfferHolds).toHaveBeenCalledWith(false));
        const calendar = screen.getByRole("combobox", { name: "Holds go to" });
        await waitFor(() => expect(calendar).not.toBeDisabled());
        fireEvent.change(calendar, { target: { value: "home" } });
        await waitFor(() => expect(mocks.setHoldCalendar).toHaveBeenCalledWith("home"));
        await waitFor(() => expect(calendar).toHaveValue("home"));
    });

    it("offers a calendar choice when no eligible default is selected", async () => {
        mocks.holdSettings.mockResolvedValue({ granted: true, offerHolds: true, calendarId: "", calendars: [{ id: "home", title: "Home", sourceId: "local", sourceTitle: "On My Mac", isDefault: false }] });
        render(<ObservationPage />);
        const calendar = await screen.findByRole("combobox", { name: "Holds go to" });
        expect(calendar).toHaveTextContent("Choose a calendar");
        expect(calendar).not.toHaveTextContent("No writable calendar");
        fireEvent.change(calendar, { target: { value: "home" } });
        await waitFor(() => expect(mocks.setHoldCalendar).toHaveBeenCalledWith("home"));
    });

    it("turns web calendar reading off and on", async () => {
        render(<ObservationPage />);
        const peek = await screen.findByRole("switch", { name: "Read web calendars when the computer is idle" });
        await waitFor(() => expect(peek).not.toBeDisabled());
        expect(peek).toBeChecked();

        fireEvent.click(peek);
        await waitFor(() => expect(mocks.setCalendarPeek).toHaveBeenCalledWith(false));
        await waitFor(() => expect(peek).not.toBeChecked());
    });

    it("keeps Watch my screen on and says so in copper with a working Try again when the change fails (ST9)", async () => {
        mocks.setPermission.mockRejectedValueOnce(new Error("denied"));
        render(<ObservationPage />);
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
        render(<ObservationPage />);
        const reading = await screen.findByRole("switch", { name: "Read my calendars" });
        await waitFor(() => expect(reading).not.toBeDisabled());
        expect(reading).toBeChecked();

        fireEvent.click(reading);
        await waitFor(() => expect(mocks.setPermission).toHaveBeenCalledWith("calendars", false));
    });

    it("asks for calendar access inline when the Mac has not granted it", async () => {
        mocks.getPermissions.mockResolvedValue(macPermissions({ calendars: { granted: false } }));
        mocks.calendarOverview.mockResolvedValue(calendarOverview({ granted: false, authorization: "not_determined", sources: [], peekProviders: [] }));
        render(<ObservationPage />);

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
        render(<ObservationPage />);

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
        render(<ObservationPage />);
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
        render(<ObservationPage />);

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
        render(<ObservationPage />);

        await waitFor(() => expect(mocks.remindersOverview).toHaveBeenCalled());
        await waitFor(() => expect(screen.queryByText("Reminders Isoform can see")).not.toBeInTheDocument());
    });

    it("switches noting copies off and keeps it out of Watch my screen", async () => {
        const base = macPermissions();
        mocks.getPermissions.mockResolvedValue({
            ...base,
            items: [
                ...base.items,
                permission("clipboard", { toggleable: true, enabled: true, actionable: false, actionLabel: "" }),
            ],
        });
        render(<ObservationPage />);

        const clipboard = await screen.findByRole("switch", { name: "Note what I copy" });
        await waitFor(() => expect(clipboard).not.toBeDisabled());
        expect(clipboard).toBeChecked();
        expect(screen.queryByRole("button", { name: "Allow" })).not.toBeInTheDocument();

        fireEvent.click(clipboard);
        await waitFor(() => expect(mocks.setPermission).toHaveBeenCalledWith("clipboard", false));
        expect(mocks.setPermission).not.toHaveBeenCalledWith("screen_recording", expect.anything());

        // Watch my screen never writes the clipboard key.
        mocks.setPermission.mockClear();
        fireEvent.click(screen.getByRole("switch", { name: "Watch my screen" }));
        await waitFor(() => expect(mocks.setPermission).toHaveBeenCalledTimes(2));
        expect(mocks.setPermission).not.toHaveBeenCalledWith("clipboard", expect.anything());
    });

    it("shows the folders Isoform can see and the documents worked on today", async () => {
        mocks.getPermissions.mockResolvedValue(withFiles(macPermissions()));
        render(<ObservationPage />);

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
        render(<ObservationPage />);

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
        render(<ObservationPage />);

        const card = (await screen.findByText("Recent files")).closest(".settings-card") as HTMLElement;
        expect(await within(card).findByText("Recent files are off and Isoform notes none of them.")).toBeInTheDocument();
        expect(within(card).queryByText("Documents")).not.toBeInTheDocument();
        expect(within(card).getByRole("button", { name: "Refresh" })).toBeDisabled();
    });

    it("adds and removes private folders", async () => {
        mocks.getPermissions.mockResolvedValue(withFiles(macPermissions()));
        render(<ObservationPage />);

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
        render(<ObservationPage />);

        const files = await screen.findByRole("switch", { name: "Note my recent files" });
        await waitFor(() => expect(files).toBeDisabled());
        const card = files.closest(".settings-card") as HTMLElement;
        expect(within(card).getByText("Observing is paused. Resume it to change this.")).toBeInTheDocument();
        expect(within(card).getByRole("button", { name: "Refresh" })).toBeDisabled();
    });

    it("hides the recent files card where the computer has no Spotlight", async () => {
        render(<ObservationPage />);
        await waitFor(() => expect(mocks.getPermissions).toHaveBeenCalled());
        await screen.findByText("Reminders Isoform can see");
        expect(screen.queryByText("Recent files")).not.toBeInTheDocument();
    });

    it("shows the mail Isoform sees and who is waiting on a reply", async () => {
        mocks.getPermissions.mockResolvedValue(withMail(macPermissions()));
        render(<ObservationPage />);

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
        render(<ObservationPage />);

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
        render(<ObservationPage />);

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
        const { unmount } = render(<ObservationPage />);

        let card = (await screen.findByText("Mail Isoform can see")).closest(".settings-card") as HTMLElement;
        expect(
            await within(card).findByText("Mail is not open. Open Mail to allow Isoform to read it; Isoform never opens it."),
        ).toBeInTheDocument();
        expect(within(card).queryByRole("button", { name: "Allow" })).not.toBeInTheDocument();
        expect(within(card).queryByRole("switch", { name: "Read my mail headers" })).not.toBeInTheDocument();
        unmount();

        mocks.mailOverview.mockResolvedValue(mailOverview({ granted: false, mailOpen: true, received: 0, waiting: 0 }));
        render(<ObservationPage />);
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
        render(<ObservationPage />);

        const reading = await screen.findByRole("switch", { name: "Read my mail headers" });
        await waitFor(() => expect(reading).not.toBeDisabled());
        fireEvent.click(reading);
        await waitFor(() => expect(mocks.setPermission).toHaveBeenCalledWith("automation_mail", false));

        mocks.setPermission.mockClear();
        fireEvent.click(screen.getByRole("switch", { name: "Watch my screen" }));
        await waitFor(() => expect(mocks.setPermission).toHaveBeenCalledTimes(2));
        expect(mocks.setPermission).not.toHaveBeenCalledWith("automation_mail", expect.anything());
    });

    it("pauses mail reading while observing is paused", async () => {
        mocks.isPaused.mockResolvedValue(true);
        mocks.getPermissions.mockResolvedValue(withMail(macPermissions()));
        render(<ObservationPage />);

        const reading = await screen.findByRole("switch", { name: "Read my mail headers" });
        await waitFor(() => expect(reading).toBeDisabled());
        const card = reading.closest(".settings-card") as HTMLElement;
        expect(within(card).getByText("Observing is paused. Resume it to change this.")).toBeInTheDocument();
        expect(within(card).getByRole("button", { name: "Refresh" })).toBeDisabled();
    });

    it("asks for Full Disk Access only from Grant and opens System Settings", async () => {
        mocks.getPermissions.mockResolvedValue(withMessages(macPermissions(), { granted: false }));
        mocks.messagesOverview.mockResolvedValue(messagesOverview({ granted: false, asks: 0, commitments: 0 }));
        render(<ObservationPage />);

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
        render(<ObservationPage />);

        const reading = await screen.findByRole("switch", { name: "Read my Messages" });
        const card = reading.closest(".settings-card") as HTMLElement;
        expect(await within(card).findByText(/3 asks/)).toBeInTheDocument();
        fireEvent.click(await within(card).findByRole("button", { name: "Stop leaving out Lee" }));
        await waitFor(() => expect(mocks.setLeftOut).toHaveBeenCalledWith([]));

        fireEvent.click(within(card).getByRole("button", { name: "Forget" }));
        await waitFor(() => expect(mocks.forgetMessages).toHaveBeenCalled());

        // The switch is its own; Watch my screen never turns it.
        mocks.setPermission.mockClear();
        fireEvent.click(screen.getByRole("switch", { name: "Watch my screen" }));
        await waitFor(() => expect(mocks.setPermission).toHaveBeenCalledTimes(2));
        expect(mocks.setPermission).not.toHaveBeenCalledWith("messages_history", expect.anything());
    });

    it("hides the mail card where the computer has no Mail", async () => {
        mocks.getPermissions.mockResolvedValue({
            platform: "windows",
            items: [permission("screen_capture", { required: true, toggleable: true, enabled: true })],
        } as PlatformPermissions);
        mocks.mailOverview.mockResolvedValue(mailOverview({ supported: false, granted: false }));
        render(<ObservationPage />);

        await waitFor(() => expect(mocks.mailOverview).toHaveBeenCalled());
        await waitFor(() => expect(screen.queryByText("Mail Isoform can see")).not.toBeInTheDocument());
    });

    it("pauses noting copies while observing is paused", async () => {
        mocks.isPaused.mockResolvedValue(true);
        const base = macPermissions();
        mocks.getPermissions.mockResolvedValue({
            ...base,
            items: [...base.items, permission("clipboard", { toggleable: true, enabled: true, actionable: false })],
        });
        render(<ObservationPage />);

        const clipboard = await screen.findByRole("switch", { name: "Note what I copy" });
        await waitFor(() => expect(clipboard).toBeDisabled());
        const card = clipboard.closest(".settings-card") as HTMLElement;
        expect(within(card).getByText("Observing is paused. Resume it to change this.")).toBeInTheDocument();
    });

    it("shows Watch my screen as off when only one key was turned off earlier", async () => {
        mocks.getPermissions.mockResolvedValue(macPermissions({ accessibility: { enabled: false } }));
        render(<ObservationPage />);
        const watch = screen.getByRole("switch", { name: "Watch my screen" });
        await waitFor(() => expect(watch).not.toBeDisabled());
        expect(watch).not.toBeChecked();

        fireEvent.click(watch);

        await waitFor(() => expect(mocks.setPermission).toHaveBeenCalledWith("accessibility", true));
        expect(mocks.setPermission).toHaveBeenCalledTimes(1);
    });

    it("asks for missing screen access inline and disables the switch until granted", async () => {
        mocks.getPermissions.mockResolvedValue(macPermissions({ accessibility: { granted: false } }));
        render(<ObservationPage />);

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
        render(<ObservationPage />);

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
        render(<ObservationPage />);

        const listenCard = (await screen.findByText("Microphone")).closest(".settings-card") as HTMLElement;
        expect(within(listenCard).getByText("Listen")).toBeInTheDocument();
        expect(within(listenCard).getByText("Used for hearing your voice and conversations.")).toBeInTheDocument();
    });

    it("switches the microphone source off without turning listening off", async () => {
        mocks.getPermissions.mockResolvedValue(macPermissions({ microphone: { toggleable: true, enabled: true } }));
        render(<ObservationPage />);

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
        render(<ObservationPage />);
        fireEvent.click(screen.getByRole("button", { name: /Advanced/ }));

        const row = (await screen.findByText("Turned off with Use the microphone. System audio can still be heard."))
            .closest(".settings-row") as HTMLElement;
        expect(within(row).getByText("Microphone")).toBeInTheDocument();
        expect(within(row).getByText("Off")).toBeInTheDocument();
    });

    it("shows no capture state when the read fails, says so in copper and reads again", async () => {
        mocks.audioStatus.mockRejectedValueOnce(new Error("bridge down"));
        render(<ObservationPage />);
        fireEvent.click(screen.getByRole("button", { name: /Advanced/ }));

        const line = await screen.findByText("Couldn't read what Isoform is capturing.");
        expect(line.closest("[data-settings-failure]")).toHaveClass("text-copper");
        expect(screen.queryByText("Stopped")).not.toBeInTheDocument();

        fireEvent.click(within(line.closest("[data-settings-failure]") as HTMLElement).getByRole("button", { name: "Try again" }));
        await waitFor(() => expect(screen.queryByText("Couldn't read what Isoform is capturing.")).not.toBeInTheDocument());
        expect(screen.getByText("Screen")).toBeInTheDocument();
    });

    it("shows a spinner on Refresh while capture status reads", async () => {
        let finish: (value: unknown) => void = () => {};
        mocks.audioStatus.mockReturnValueOnce(new Promise((resolve) => (finish = resolve)));
        render(<ObservationPage />);
        fireEvent.click(screen.getByRole("button", { name: /Advanced/ }));
        expect(await screen.findByTestId("capture-reading")).toBeInTheDocument();
        finish({ running: true, activeSources: ["microphone"], screenLocked: false });
        await waitFor(() => expect(screen.queryByTestId("capture-reading")).not.toBeInTheDocument());
    });

    it("shows no microphone switch until the Mac has given access", async () => {
        mocks.getPermissions.mockResolvedValue(
            macPermissions({ microphone: { toggleable: true, enabled: true, granted: false } }),
        );
        render(<ObservationPage />);
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

    it("jumps to Privacy for the per-app list", async () => {
        const onOpenPage = vi.fn();
        render(<ObservationPage onOpenPage={onOpenPage} />);
        fireEvent.click(screen.getByRole("button", { name: /Advanced/ }));

        const row = (await screen.findByText("Apps Isoform does not listen to")).closest(".settings-row") as HTMLElement;
        fireEvent.click(within(row).getByRole("button", { name: "Open Privacy" }));

        expect(onOpenPage).toHaveBeenCalledWith("security");
        expect(mocks.emit).not.toHaveBeenCalled();
    });
});
