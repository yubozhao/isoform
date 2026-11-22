import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import i18n from "@/i18n";
import type {
    MobileBriefSchedule,
    MobileConnectionStatus,
    SMSChannelStatus,
    TextChannelStatus,
} from "@/lib/wails-shim-app";
import { IPhonePage } from "./iphone-page";

const mocks = vi.hoisted(() => ({
    getMobileStatus: vi.fn(),
    getSchedule: vi.fn(),
    getTextStatus: vi.fn(),
    getSMSStatus: vi.fn(),
    eventsOn: vi.fn(),
}));

// The wailsjs path and the shim resolve to the same module, so one mock covers both imports.
vi.mock("@/lib/wails-shim-app", () => ({
    GetMobileConnectionStatus: mocks.getMobileStatus,
    GetMobileBriefSchedule: mocks.getSchedule,
    GetTextChannelStatus: mocks.getTextStatus,
    GetSMSChannelStatus: mocks.getSMSStatus,
}));

vi.mock("../../../../wailsjs/runtime/runtime", () => ({
    ClipboardSetText: vi.fn(),
    EventsOn: mocks.eventsOn,
}));

const status: MobileConnectionStatus = {
    enabled: true,
    relayUrl: "https://relay.example.com",
    savedRelayUrl: "https://relay.example.com",
    relayState: "connected",
    relayLastSeenAt: "",
    hubId: "00000000-0000-4000-8000-000000000001",
    hubName: "Test Mac",
    connectionCode: "",
    expiresAt: "",
    pendingPairing: null,
    instantPush: false,
    devices: [
        {
            deviceId: "phone-1",
            deviceName: "My iPhone",
            createdAt: "2026-10-07T16:00:00Z",
            expiresAt: "2027-01-07T16:00:00Z",
            lastSeenAt: "",
            scopes: ["brief:read", "push"],
        },
    ],
};

const smsStatus: SMSChannelStatus = {
    available: true,
    state: "none",
    number: "",
    pendingNumber: "",
    codeExpiresAt: "",
    sentToday: 0,
    sentThisMonth: 0,
    dailyCap: 10,
    monthlyCap: 200,
    lastError: "",
};

const schedule: MobileBriefSchedule = {
    feedEnabled: false,
    feedUrl: "",
    enabled: true,
    morning: "07:00",
    evening: "18:00",
    lastGeneratedAt: "",
    lastSlot: "",
    narrativeAvailable: true,
};

const texts: TextChannelStatus = {
    verified: true,
    verifying: false,
    supported: true,
    enabled: true,
    number: "+15550100000",
    includeNames: false,
    consentAccepted: true,
    permissionGranted: true,
    sentToday: 1,
    lastSentAt: "",
    lastError: "",
};

async function renderPage() {
    render(<IPhonePage />);
    await waitFor(() => {
        expect(screen.queryByText("Checking text messages...")).not.toBeInTheDocument();
    });
}

describe("IPhonePage", () => {
    beforeEach(async () => {
        vi.clearAllMocks();
        window.localStorage.clear();
        await i18n.changeLanguage("en");
        mocks.getMobileStatus.mockResolvedValue(status);
        mocks.getSchedule.mockResolvedValue(schedule);
        mocks.getTextStatus.mockResolvedValue(texts);
        mocks.getSMSStatus.mockResolvedValue(smsStatus);
        mocks.eventsOn.mockReturnValue(() => undefined);
    });

    it("opens with phone pairing only, without the brief or text-message setup", async () => {
        await renderPage();
        expect(screen.getByText("Connected")).toBeInTheDocument();
        expect(screen.getByRole("button", { name: "Turn off" })).toBeInTheDocument();
        // The brief's schedule lives on Briefs and notifications
        // (computer-settings v1.0.0 rule 6), not here.
        expect(screen.getAllByRole("heading").map((heading) => heading.textContent)).toEqual([
            "Your phones",
        ]);
        expect(mocks.getSchedule).not.toHaveBeenCalled();
        expect(screen.queryByRole("switch", { name: "Turn on text messages" })).not.toBeInTheDocument();
        expect(mocks.getTextStatus).not.toHaveBeenCalled();
        expect(mocks.getSMSStatus).not.toHaveBeenCalled();
        expect(screen.queryByRole("switch", { name: "Include names in texts" })).not.toBeInTheDocument();
        expect(screen.queryByRole("button", { name: "Send a test text" })).not.toBeInTheDocument();
        expect(screen.queryByText("Server address")).not.toBeInTheDocument();
    });

    it("keeps the server address and phone access under Advanced", async () => {
        await renderPage();
        fireEvent.click(screen.getByRole("button", { name: /Advanced/ }));
        expect(screen.getAllByRole("heading").map((heading) => heading.textContent)).toEqual([
            "Your phones",
            "Server address",
            "What each phone can do",
        ]);
        expect(screen.queryByRole("switch", { name: "Include names in texts" })).not.toBeInTheDocument();
        expect(screen.queryByRole("button", { name: "Send a test text" })).not.toBeInTheDocument();
        expect(mocks.getTextStatus).not.toHaveBeenCalled();
    });
});
