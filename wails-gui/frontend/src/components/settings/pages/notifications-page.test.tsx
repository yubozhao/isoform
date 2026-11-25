import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import i18n from "@/i18n";
import type { EngagementSettingsView, MobileConnectionStatus, TextChannelStatus } from "@/lib/wails-shim-app";
import en from "@/i18n/locales/en/settings.json";
import zh from "@/i18n/locales/zh-CN/settings.json";
import pageSource from "./notifications-page.tsx?raw";
import { NotificationsPage } from "./notifications-page";

const mocks = vi.hoisted(() => ({
    getSettings: vi.fn(),
    handlers: new Map<string, (...args: unknown[]) => void>(),
    getWeek: vi.fn(),
    setKind: vi.fn(),
    setRules: vi.fn(),
    getPermissions: vi.fn(),
    requestPermission: vi.fn(),
    openPermissionSettings: vi.fn(),
    getMobileStatus: vi.fn(),
    getTextStatus: vi.fn(),
    getSMSStatus: vi.fn(),
    getEmailStatus: vi.fn(),
}));

// The first channel row is named for the platform: "This Mac" on macOS,
// "This computer" elsewhere.
const platform = vi.hoisted(() => ({ isMac: true }));
vi.mock("@/lib/platform", async (importOriginal) => ({
    ...(await importOriginal<typeof import("@/lib/platform")>()),
    get isMac() {
        return platform.isMac;
    },
}));

// The proposals switch and the brief's schedule have their own tests
// (topic-proposals-card.test.tsx, brief-schedule-card.test.tsx); here they
// are stand-ins so the page's order can be checked.
vi.mock("./topic-proposals-card", () => ({
    TopicProposalsCard: () => <section data-testid="proposals-card" />,
}));
vi.mock("@/components/brief-schedule-card", () => ({
    BriefScheduleCard: () => <section data-testid="brief-schedule-card" />,
}));

// The wailsjs path and the shim resolve to the same module, so one mock covers both imports.
vi.mock("@/lib/wails-shim-app", () => ({
    GetEngagementSettings: mocks.getSettings,
    GetEngagementWeekSummary: mocks.getWeek,
    SetEngagementKindEnabled: mocks.setKind,
    SetEngagementRules: mocks.setRules,
    GetPlatformPermissions: mocks.getPermissions,
    RequestPermission: mocks.requestPermission,
    OpenPermissionSettings: mocks.openPermissionSettings,
    GetMobileConnectionStatus: mocks.getMobileStatus,
    GetTextChannelStatus: mocks.getTextStatus,
    GetSMSChannelStatus: mocks.getSMSStatus,
    GetEmailChannelStatus: mocks.getEmailStatus,
    GetAutoSpeakAutomationEnabled: vi.fn(async () => false),
    SetAutoSpeakAutomationEnabled: vi.fn(async () => undefined),
    GetAutoSpeakNotificationsEnabled: vi.fn(async () => false),
    SetAutoSpeakNotificationsEnabled: vi.fn(async () => undefined),
    GetTtsModelStatus: vi.fn(async () => ({ zh: false, en: true })),
}));

vi.mock("../../../../wailsjs/runtime/runtime", () => ({
    EventsOn: (event: string, handler: (...args: unknown[]) => void) => {
        mocks.handlers.set(event, handler);
        return () => mocks.handlers.delete(event);
    },
}));

const view: EngagementSettingsView = {
    available: true,
    level: "normal",
    kinds: [
        {
            kind: "work_blocked",
            enabled: true,
            defaultOn: true,
            paused: false,
            quietOnToday: false,
            deliveryMode: "normal",
            quietAllowed: true,
            quietSuggestionAllowed: false,
            sent: 0,
            opened: 0,
            acted: 0,
            dismissed: 0,
            muted: 0,
        },
    ],
    quietStart: "22:00",
    quietEnd: "08:00",
    dailyInterruptBudget: 6,
    digestTimes: ["08:30"],
    statsDays: 30,
};

function permissions(granted: boolean | null) {
    const items =
        granted === null
            ? [{ key: "microphone", label: "Microphone", granted: true }]
            : [
                  { key: "microphone", label: "Microphone", granted: true },
                  { key: "notifications", label: "Notifications", granted },
              ];
    return { platform: "darwin", supported: true, items };
}

function mobileStatus(overrides: Partial<MobileConnectionStatus> = {}): MobileConnectionStatus {
    return {
        enabled: false,
        relayUrl: "",
        savedRelayUrl: "",
        relayState: "disabled",
        relayLastSeenAt: "",
        hubId: "",
        hubName: "",
        connectionCode: "",
        expiresAt: "",
        pendingPairing: null,
        devices: [],
        instantPush: false,
        ...overrides,
    };
}

function textStatus(overrides: Partial<TextChannelStatus> = {}): TextChannelStatus {
    return {
        verified: false,
        verifying: false,
        supported: true,
        enabled: false,
        number: "",
        includeNames: false,
        consentAccepted: false,
        permissionGranted: false,
        sentToday: 0,
        lastSentAt: "",
        lastError: "",
        ...overrides,
    };
}

async function renderPage() {
    render(<NotificationsPage />);
    await waitFor(() => {
        expect(screen.getByRole("heading", { name: "The last 30 days" })).toBeInTheDocument();
    });
    await waitFor(() => {
        expect(mocks.getPermissions).toHaveBeenCalled();
    });
}

function openAdvanced() {
    fireEvent.click(screen.getByRole("button", { name: /Advanced/ }));
}

function channelRow(title: string): HTMLElement {
    const row = screen.getByText(title).closest(".settings-row");
    if (!(row instanceof HTMLElement)) throw new Error(`no row for ${title}`);
    return row;
}

const LATE_NOTE = en.proactiveNotifications.channels.iphoneLateNote;

describe("NotificationsPage", () => {
    beforeEach(async () => {
        vi.clearAllMocks();
        mocks.handlers.clear();
        window.localStorage.clear();
        platform.isMac = true;
        await i18n.changeLanguage("en");
        mocks.getSettings.mockResolvedValue(view);
        mocks.getWeek.mockResolvedValue({ available: true, sent: 0, actedOn: 0, days: 7, kinds: [] });
        mocks.getSMSStatus.mockRejectedValue(new Error("offline"));
        mocks.getEmailStatus.mockResolvedValue({
            available: true,
            state: "none",
            address: "",
            pendingAddress: "",
            codeExpiresAt: "",
            sentToday: 0,
            dailyCap: 20,
            lastError: "",
        });
        mocks.getPermissions.mockResolvedValue(permissions(true));
        mocks.getMobileStatus.mockResolvedValue(mobileStatus());
        mocks.getTextStatus.mockResolvedValue(textStatus());
        mocks.openPermissionSettings.mockResolvedValue(undefined);
    });

    it("says a phone notice can come late only when this Mac's relay has no instant push", async () => {
        mocks.getMobileStatus.mockResolvedValue(mobileStatus({ instantPush: false }));
        await renderPage();
        openAdvanced();
        await waitFor(() => {
            expect(within(channelRow("Your phone")).getByText("Not set up")).toBeInTheDocument();
        });
        expect(within(channelRow("Your phone")).getByText(LATE_NOTE)).toBeInTheDocument();
    });

    it("hides the late-notice line when this Mac's relay has instant push", async () => {
        mocks.getMobileStatus.mockResolvedValue(mobileStatus({ instantPush: true }));
        await renderPage();
        openAdvanced();
        await waitFor(() => {
            expect(within(channelRow("Your phone")).getByText("Not set up")).toBeInTheDocument();
        });
        expect(screen.queryByText(LATE_NOTE)).not.toBeInTheDocument();
    });

    it("puts Proposals from your topics first, then the brief's schedule, then notifications (computer-settings v1.3.0 rule 1)", async () => {
        const { container } = render(<NotificationsPage />);
        await waitFor(() => expect(mocks.getSettings).toHaveBeenCalled());
        const sections = Array.from(container.querySelectorAll("[data-testid]")).map((node) => node.getAttribute("data-testid"));
        expect(sections.slice(0, 2)).toEqual(["proposals-card", "brief-schedule-card"]);
        expect(pageSource.indexOf("<TopicProposalsCard")).toBeLessThan(pageSource.indexOf("<BriefScheduleCard"));
        expect(pageSource.indexOf("<BriefScheduleCard")).toBeLessThan(pageSource.indexOf("<NotificationsSettings"));
        expect(en.pages.notifications.subtitle).toBe(
            "What Isoform brings you on its own: the brief, proposals from your topics, and three kinds of notification.",
        );
    });

    it("opens with the cards, no second introduction, and keeps channel details under Advanced", async () => {
        await renderPage();
        // The page header's subtitle is the one explanation; the page adds no intro of its own.
        expect(screen.queryByText(/while you are using it, on your phone when you are away/)).not.toBeInTheDocument();
        expect(en.proactiveNotifications).not.toHaveProperty("intro");
        expect(zh.proactiveNotifications).not.toHaveProperty("intro");
        expect(screen.queryByText("Allow notifications on this computer")).not.toBeInTheDocument();
        expect(
            screen.getByRole("button", { name: /Where each notification is sent/ }),
        ).toBeInTheDocument();
        expect(screen.queryByText("Where notifications go")).not.toBeInTheDocument();
        expect(mocks.getMobileStatus).not.toHaveBeenCalled();
    });

    it("shows quiet hours and limits with the rest, out of Advanced, sharing the page's one read (answers v1.0.0 T2)", async () => {
        await renderPage();
        expect(await screen.findByRole("heading", { name: "Quiet hours and limits" })).toBeInTheDocument();
        expect(screen.getByLabelText("Quiet hours start")).toHaveValue("22:00");
        expect(screen.getByLabelText("Interruptions a day")).toHaveValue(6);
        expect(mocks.getSettings).toHaveBeenCalledTimes(1);
    });

    it("holds the voice for Hear it and no switch for reading Chat's answers aloud (answers v1.0.0 T2)", async () => {
        await renderPage();
        expect(await screen.findByRole("heading", { name: "Read aloud" })).toBeInTheDocument();
        expect(await screen.findByText("English voice")).toBeInTheDocument();
        expect(screen.getByText("Read notifications aloud")).toBeInTheDocument();
        // Off until turned on, in design's words (answers v1.5.0 S22).
        expect(screen.getByText("Says each notification out loud on this computer. Anyone nearby hears it.")).toBeInTheDocument();
        await waitFor(() => expect(screen.getByRole("switch", { name: "Read notifications aloud" })).not.toBeChecked());
        expect(screen.queryByText("Read chat replies aloud")).not.toBeInTheDocument();
        // Automations are behind their switch (answers v1.2.0 AG12): kept, not shown.
        expect(screen.queryByText("Announce finished automations")).not.toBeInTheDocument();
    });

    it("rechecks a preloaded permission view when its background refresh publishes", async () => {
        mocks.getPermissions.mockResolvedValue(permissions(false));
        await renderPage();
        expect(screen.getByRole("button", { name: /Allow notifications/ })).toBeInTheDocument();
        mocks.getPermissions.mockResolvedValue(permissions(true));
        act(() => mocks.handlers.get("permissions:changed")?.());
        await waitFor(() => expect(screen.queryByRole("button", { name: /Allow notifications/ })).not.toBeInTheDocument());
    });

    it("asks for the macOS permission only while it is missing", async () => {
        mocks.getPermissions.mockResolvedValue(permissions(false));
        await renderPage();
        expect(await screen.findByText("Allow notifications on this computer")).toBeInTheDocument();

        mocks.requestPermission.mockResolvedValueOnce(true);
        mocks.getPermissions.mockResolvedValue(permissions(true));
        fireEvent.click(screen.getByRole("button", { name: /Allow notifications/ }));
        await waitFor(() => {
            expect(screen.queryByText("Allow notifications on this computer")).not.toBeInTheDocument();
        });
        expect(mocks.requestPermission).toHaveBeenCalledWith("notifications");
        expect(mocks.openPermissionSettings).not.toHaveBeenCalled();
    });

    it("opens System Settings and says so when macOS still blocks notifications", async () => {
        mocks.getPermissions.mockResolvedValue(permissions(false));
        mocks.requestPermission.mockResolvedValueOnce(false);
        await renderPage();
        fireEvent.click(await screen.findByRole("button", { name: /Allow notifications/ }));
        await waitFor(() => {
            expect(screen.getByRole("status")).toHaveTextContent(
                "Notifications are still off. Turn them on for Isoform in System Settings",
            );
        });
        expect(mocks.openPermissionSettings).toHaveBeenCalledWith("notifications");
        expect(screen.getByText("Allow notifications on this computer")).toBeInTheDocument();
    });

    it("says so when the permission request fails", async () => {
        mocks.getPermissions.mockResolvedValue(permissions(false));
        mocks.requestPermission.mockRejectedValueOnce(new Error("boom"));
        await renderPage();
        fireEvent.click(await screen.findByRole("button", { name: /Allow notifications/ }));
        await waitFor(() => {
            expect(screen.getByRole("alert")).toHaveTextContent("Could not ask macOS for permission. Try again.");
        });
    });

    it("shows where each notification goes and whether each channel can be used", async () => {
        mocks.getMobileStatus.mockResolvedValue(
            mobileStatus({
                enabled: true,
                devices: [
                    {
                        deviceId: "d1",
                        deviceName: "Phone",
                        createdAt: "",
                        expiresAt: "",
                        lastSeenAt: "",
                        scopes: ["brief", "push"],
                    },
                ],
            }),
        );
        mocks.getTextStatus.mockResolvedValue(textStatus({ enabled: true, number: "+14155550123" }));
        await renderPage();
        openAdvanced();
        expect(await screen.findByText("Where notifications go")).toBeInTheDocument();
        await waitFor(() => {
            expect(within(channelRow("Your phone")).getByText("Ready")).toBeInTheDocument();
        });
        expect(within(channelRow("This computer")).getByText("Ready")).toBeInTheDocument();
        // New text setup is paused, while existing channel readiness stays visible.
        await waitFor(() => {
            expect(within(channelRow("Text message")).getByText("Coming soon")).toBeInTheDocument();
        });
        // No address is verified for playbook results yet.
        await waitFor(() => {
            expect(within(channelRow("Email")).getByText("Not set up")).toBeInTheDocument();
        });
        expect(screen.getByRole("heading", { name: "Results by email" })).toBeInTheDocument();
    });

    it("marks a blocked Mac, an unpaired iPhone and an unsupported text channel", async () => {
        mocks.getPermissions.mockResolvedValue(permissions(false));
        mocks.getMobileStatus.mockResolvedValue(
            mobileStatus({
                enabled: true,
                devices: [
                    {
                        deviceId: "d1",
                        deviceName: "Phone",
                        createdAt: "",
                        expiresAt: "",
                        lastSeenAt: "",
                        scopes: ["brief"],
                    },
                ],
            }),
        );
        mocks.getTextStatus.mockResolvedValue(textStatus({ supported: false }));
        await renderPage();
        openAdvanced();
        await waitFor(() => {
            expect(within(channelRow("Text message")).getByText("Not available here")).toBeInTheDocument();
        });
        expect(within(channelRow("This computer")).getByText("Not allowed")).toBeInTheDocument();
        expect(within(channelRow("Your phone")).getByText("Not set up")).toBeInTheDocument();
    });

    it("counts an active text subscription as a ready text channel", async () => {
        mocks.getTextStatus.mockResolvedValue(textStatus({ supported: false }));
        mocks.getSMSStatus.mockResolvedValue({
            available: true,
            state: "active",
            number: "+14155550123",
            pendingNumber: "",
            codeExpiresAt: "",
            sentToday: 0,
            sentThisMonth: 0,
            dailyCap: 10,
            monthlyCap: 200,
            lastError: "",
        });
        await renderPage();
        openAdvanced();
        await waitFor(() => {
            expect(within(channelRow("Text message")).getByText("Ready")).toBeInTheDocument();
        });
    });

    it("does not count a subscription the relay connection cannot reach", async () => {
        mocks.getTextStatus.mockResolvedValue(textStatus({ supported: false }));
        mocks.getSMSStatus.mockResolvedValue({
            available: false,
            state: "active",
            number: "+14155550123",
            pendingNumber: "",
            codeExpiresAt: "",
            sentToday: 0,
            sentThisMonth: 0,
            dailyCap: 10,
            monthlyCap: 200,
            lastError: "offline",
        });
        await renderPage();
        openAdvanced();
        await waitFor(() => {
            expect(within(channelRow("Text message")).getByText("Not available here")).toBeInTheDocument();
        });
    });

    it("links a channel that is not set up to the Phone page", async () => {
        const onOpenPage = vi.fn();
        mocks.getTextStatus.mockResolvedValue(textStatus({ supported: false }));
        render(<NotificationsPage onOpenPage={onOpenPage} />);
        await screen.findByRole("heading", { name: "The last 30 days" });
        openAdvanced();
        await waitFor(() => {
            expect(within(channelRow("Your phone")).getByText("Not set up")).toBeInTheDocument();
        });
        // Text messages are not available here, so only the iPhone row links.
        expect(within(channelRow("Text message")).queryByRole("button")).not.toBeInTheDocument();
        fireEvent.click(within(channelRow("Your phone")).getByRole("button", { name: "Open Phone" }));
        expect(onOpenPage).toHaveBeenCalledWith("iphone");
    });

    it("shows no Mac chip or permission card where there is no notifications permission", async () => {
        mocks.getPermissions.mockResolvedValue(permissions(null));
        mocks.getMobileStatus.mockRejectedValue(new Error("offline"));
        await renderPage();
        expect(screen.queryByText("Allow notifications on this computer")).not.toBeInTheDocument();
        openAdvanced();
        await screen.findByText("Where notifications go");
        await waitFor(() => {
            expect(within(channelRow("Text message")).getByText("Coming soon")).toBeInTheDocument();
        });
        expect(within(channelRow("This computer")).queryByText(/Ready|Not allowed/)).not.toBeInTheDocument();
        expect(within(channelRow("Your phone")).queryByText(/Ready|Not set up/)).not.toBeInTheDocument();
    });

    it("names the first channel This computer outside macOS", async () => {
        platform.isMac = false;
        mocks.getPermissions.mockResolvedValue({ platform: "windows", supported: true, items: [] });
        await renderPage();
        openAdvanced();
        await screen.findByText("Where notifications go");
        expect(screen.getByText("This computer")).toBeInTheDocument();
        expect(screen.queryByText("This Mac")).not.toBeInTheDocument();
        expect(screen.queryByText(/this Mac/)).not.toBeInTheDocument();
    });

    it("has every key it uses in English and Chinese", () => {
        const keys = new Set<string>();
        for (const match of pageSource.matchAll(/t\(\s*"proactiveNotifications\.([^"]+)"/g)) {
            keys.add(match[1]);
        }
        expect(keys.size).toBeGreaterThan(15);
        const lookup = (tree: Record<string, unknown>, path: string) =>
            path.split(".").reduce<unknown>((node, part) => (node as Record<string, unknown> | undefined)?.[part], tree);
        for (const key of keys) {
            expect(lookup(en.proactiveNotifications, key), `en ${key}`).toBeTypeOf("string");
            expect(lookup(zh.proactiveNotifications, key), `zh ${key}`).toBeTypeOf("string");
        }
    });
});
