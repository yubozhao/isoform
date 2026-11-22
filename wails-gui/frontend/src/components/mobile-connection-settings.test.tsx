import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import i18n from "@/i18n";
import { pageQueryCache } from "@/lib/query-cache";
import type {
    MobileBriefSchedule,
    MobileConnectionStatus,
    MobileDeviceView,
} from "@/lib/wails-shim-app";
import en from "@/i18n/locales/en/settings.json";
import zh from "@/i18n/locales/zh-CN/settings.json";
import componentSource from "./mobile-connection-settings.tsx?raw";
import { MobileConnectionSettings } from "./mobile-connection-settings";

const mocks = vi.hoisted(() => ({
    getStatus: vi.fn(),
    enableUsingRelay: vi.fn(),
    disable: vi.fn(),
    generateCode: vi.fn(),
    confirmPairing: vi.fn(),
    confirmScopeRequest: vi.fn(),
    revoke: vi.fn(),
    resetRemoteAccess: vi.fn(),
    getSchedule: vi.fn(),
    setSchedule: vi.fn(),
    getHealth: vi.fn(),
    generateBrief: vi.fn(),
    getKeepAwake: vi.fn(),
    setKeepAwake: vi.fn(),
    nativeCopy: vi.fn(),
    clipboardCopy: vi.fn(),
    eventsOn: vi.fn(),
    openURL: vi.fn(),
    notifyToast: vi.fn(),
}));

vi.mock("../../wailsjs/go/main/App", () => ({
    GetMobileConnectionStatus: mocks.getStatus,
    EnableMobileConnectionUsingRelay: mocks.enableUsingRelay,
    DisableMobileConnection: mocks.disable,
    GenerateMobileConnectionCode: mocks.generateCode,
    ConfirmMobilePairing: mocks.confirmPairing,
    ConfirmMobileScopeRequest: mocks.confirmScopeRequest,
    RevokeMobileDevice: mocks.revoke,
    ResetMobileRemoteAccess: mocks.resetRemoteAccess,
    GetMobileBriefSchedule: mocks.getSchedule,
    SetMobileBriefSchedule: mocks.setSchedule,
    GetMobileBriefHealth: mocks.getHealth,
    GenerateMobileBriefNow: mocks.generateBrief,
    GetMobileKeepAwake: mocks.getKeepAwake,
    SetMobileKeepAwake: mocks.setKeepAwake,
    NativeClipboardSetText: mocks.nativeCopy,
}));

vi.mock("../../wailsjs/runtime/runtime", () => ({
    BrowserOpenURL: mocks.openURL,
    ClipboardSetText: mocks.clipboardCopy,
    EventsOn: mocks.eventsOn,
}));

vi.mock("@/lib/notify", () => ({
    notifyToast: mocks.notifyToast,
}));

const QR_NAME = "Pairing code for Isoform on your phone";
const KEEP_AWAKE_NAME = "Keep awake while plugged in";
const INVITATION = "isoform://pair#component-test-invitation";

const phone: MobileDeviceView = {
    deviceId: "phone-1",
    deviceName: "My iPhone",
    createdAt: "2026-10-07T16:00:00Z",
    expiresAt: "2027-01-07T16:00:00Z",
    lastSeenAt: new Date(Date.now() - 2 * 60 * 60 * 1000).toISOString(),
    scopes: ["brief:read", "brief:refresh", "presence:read", "push"],
};

const idleStatus: MobileConnectionStatus = {
    enabled: false,
    relayUrl: "",
    savedRelayUrl: "",
    relayState: "disabled",
    relayLastSeenAt: "",
    hubId: "00000000-0000-4000-8000-000000000001",
    hubName: "Test Mac",
    connectionCode: "",
    expiresAt: "",
    pendingPairing: null,
    devices: [],
    instantPush: false,
};

const connectedStatus: MobileConnectionStatus = {
    ...idleStatus,
    enabled: true,
    relayUrl: "https://relay.example.com",
    savedRelayUrl: "https://relay.example.com",
    relayState: "connected",
    relayLastSeenAt: new Date(Date.now() - 60 * 1000).toISOString(),
    devices: [phone],
};

const noPhoneStatus: MobileConnectionStatus = { ...connectedStatus, devices: [] };

function withCode(
    seconds = 300,
    overrides: Partial<MobileConnectionStatus> = {},
): MobileConnectionStatus {
    return {
        ...connectedStatus,
        connectionCode: INVITATION,
        expiresAt: new Date(Date.now() + seconds * 1000).toISOString(),
        ...overrides,
    };
}

const schedule: MobileBriefSchedule = {
    enabled: false,
    morning: "07:00",
    evening: "18:00",
    lastGeneratedAt: "",
    lastSlot: "",
    narrativeAvailable: true,
    feedEnabled: false,
    feedUrl: "",
};

async function renderReady() {
    const view = render(<MobileConnectionSettings />);
    await waitFor(() => {
        expect(screen.queryAllByText(/Checking phone connections|Loading brief settings/)).toHaveLength(0);
    });
    return view;
}

// Guard: the brief's schedule is not on Your phone any more
// (computer-settings v1.0.0 rule 6 puts it in Briefs and notifications).
function expectNoBriefSchedule() {
    expect(screen.queryByRole("heading", { name: "Daily brief" })).not.toBeInTheDocument();
    expect(mocks.getSchedule).not.toHaveBeenCalled();
}

function openAdvanced(label: RegExp = /Advanced/) {
    fireEvent.click(screen.getByRole("button", { name: label }));
}

describe("MobileConnectionSettings component", () => {
    beforeEach(async () => {
        vi.clearAllMocks();
        pageQueryCache.clear();
        window.localStorage.clear();
        await i18n.changeLanguage("en");
        mocks.getStatus.mockResolvedValue(idleStatus);
        mocks.getSchedule.mockResolvedValue(schedule);
        mocks.enableUsingRelay.mockRejectedValue(new Error("unexpected enable"));
        mocks.disable.mockRejectedValue(new Error("unexpected disable"));
        mocks.generateCode.mockRejectedValue(new Error("unexpected code"));
        mocks.confirmPairing.mockRejectedValue(new Error("unexpected confirm"));
        mocks.revoke.mockRejectedValue(new Error("unexpected revoke"));
        mocks.resetRemoteAccess.mockRejectedValue(new Error("unexpected remote access reset"));
        mocks.setSchedule.mockRejectedValue(new Error("unexpected schedule save"));
        mocks.generateBrief.mockRejectedValue(new Error("unexpected brief"));
        mocks.getKeepAwake.mockResolvedValue({ on: true, pairedPhones: 0, onPower: false, hasBattery: true });
        mocks.setKeepAwake.mockRejectedValue(new Error("unexpected keep-awake save"));
        mocks.clipboardCopy.mockResolvedValue(true);
        mocks.nativeCopy.mockResolvedValue(undefined);
        mocks.eventsOn.mockReturnValue(() => undefined);
    });

    afterEach(() => {
        vi.useRealTimers();
        delete (window as unknown as { runtime?: unknown }).runtime;
    });

    it.each([
        ["en", "A phone on another network cannot reach 127.0.0.1."],
        ["zh-CN", "其他网络上的手机无法访问 127.0.0.1。"],
    ])("translates loopback warnings in Settings and the invitation in %s", async (language, message) => {
        await i18n.changeLanguage(language);
        mocks.getStatus.mockResolvedValue(withCode(300, {
            relayUrl: "http://127.0.0.1:8791",
            ...{
                warning: "backend text must never be rendered",
                warningCode: "relay_loopback",
                warningHost: "127.0.0.1",
            },
        }));
        await renderReady();
        await waitFor(() => expect(screen.getAllByText(message)).toHaveLength(2));
        expect(screen.queryByText("backend text must never be rendered")).not.toBeInTheDocument();
        expect(screen.getByRole("img", { name: i18n.t("settings:mobileConnection.qrTitle") })).toBeInTheDocument();
    });

    it.each(["en", "zh-CN"])("opens the App Store from phone settings in %s", async (language) => {
        await i18n.changeLanguage(language);
        await renderReady();
        fireEvent.click(screen.getByRole("button", { name: i18n.t("settings:mobileConnection.getApp") }));
        expect(mocks.openURL).toHaveBeenCalledWith("https://apps.apple.com/app/isoform/id6827347350");
    });

    it("keeps Simulator pairing available without a loopback warning", async () => {
        mocks.getStatus.mockResolvedValue(withCode(300, { relayUrl: "http://127.0.0.1:8791" }));
        await renderReady();
        expect(screen.queryByText(/A phone on another network cannot reach/)).not.toBeInTheDocument();
        expect(screen.getByRole("img", { name: QR_NAME })).toBeInTheDocument();
    });

    it("shows the connection as off and turns it on with the server address as an origin", async () => {
        mocks.enableUsingRelay.mockResolvedValue(connectedStatus);
        await renderReady();
        expect(screen.getByText("Off")).toBeInTheDocument();
        expect(screen.queryByLabelText("Server address")).not.toBeInTheDocument();
        openAdvanced();
        fireEvent.change(screen.getByLabelText("Server address"), {
            target: { value: "https://relay.example.com/" },
        });
        fireEvent.click(screen.getByRole("button", { name: "Turn on" }));
        await waitFor(() => expect(mocks.enableUsingRelay).toHaveBeenCalledWith("https://relay.example.com"));
        expect(await screen.findByText("Connected")).toBeInTheDocument();
        expect(screen.getByRole("button", { name: "Turn off" })).toBeEnabled();
    });

    it("shows the hosted fallback as a placeholder without saving it when turning on", async () => {
        const status = { ...idleStatus, relayUrl: "https://relay.yansu.app" };
        mocks.getStatus.mockResolvedValue(status);
        mocks.enableUsingRelay.mockResolvedValue({ ...status, enabled: true });
        await renderReady();
        openAdvanced();

        const input = screen.getByLabelText("Server address");
        expect(input).toHaveValue("");
        expect(input).toHaveAttribute("placeholder", "https://relay.yansu.app");
        fireEvent.click(screen.getByRole("button", { name: "Turn on" }));

        await waitFor(() => expect(mocks.enableUsingRelay).toHaveBeenCalledWith(""));
        expect(mocks.enableUsingRelay).not.toHaveBeenCalledWith("https://relay.yansu.app");
    });

    it("turns on with the saved relay URL", async () => {
        const status = {
            ...idleStatus,
            relayUrl: "https://relay.example.com",
            savedRelayUrl: "https://relay.example.com",
        };
        mocks.getStatus.mockResolvedValue(status);
        mocks.enableUsingRelay.mockResolvedValue({ ...status, enabled: true });
        await renderReady();
        openAdvanced();

        expect(screen.getByLabelText("Server address")).toHaveValue("https://relay.example.com");
        fireEvent.click(screen.getByRole("button", { name: "Turn on" }));

        await waitFor(() => expect(mocks.enableUsingRelay).toHaveBeenCalledWith("https://relay.example.com"));
    });

    it("clears the saved relay when the user clears the field", async () => {
        const status = {
            ...idleStatus,
            relayUrl: "https://relay.example.com",
            savedRelayUrl: "https://relay.example.com",
        };
        mocks.getStatus.mockResolvedValue(status);
        mocks.enableUsingRelay.mockResolvedValue({ ...status, enabled: true, savedRelayUrl: "" });
        await renderReady();
        openAdvanced();

        fireEvent.change(screen.getByLabelText("Server address"), { target: { value: "" } });
        fireEvent.click(screen.getByRole("button", { name: "Turn on" }));

        await waitFor(() => expect(mocks.enableUsingRelay).toHaveBeenCalledWith(""));
    });

    it.each(["en", "zh-CN"])("describes ready remote access in %s without exposing the loopback relay", async (language) => {
        await i18n.changeLanguage(language);
        const copy = language === "en" ? en.mobileConnection : zh.mobileConnection;
        mocks.getStatus.mockResolvedValue({
            ...connectedStatus,
            relayUrl: "http://127.0.0.1:4100",
            savedRelayUrl: "",
            relayLastSeenAt: "",
            remoteAccess: { state: "ready" },
        });
        await renderReady();

        expect(screen.getByText(copy.statusConnectedRemoteNoTime)).toBeInTheDocument();
        expect(screen.queryByText(copy.statusConnectedLocalOnlyNoTime)).not.toBeInTheDocument();
        if (language === "en") {
            expect(screen.queryByText(/cannot reach this computer/i)).not.toBeInTheDocument();
        }
        openAdvanced(language === "en" ? /Advanced/ : /高级/);
        expect(screen.getByText(copy.serverRemoteAccess)).toBeInTheDocument();
        expect(screen.queryByText("http://127.0.0.1:4100")).not.toBeInTheDocument();
    });

    it.each(["en", "zh-CN"])("explains saved relay access and confirms switching in %s", async (language) => {
        await i18n.changeLanguage(language);
        const copy = language === "en" ? en.mobileConnection : zh.mobileConnection;
        const status = {
            ...connectedStatus,
            relayUrl: "https://relay.example.com",
            relayState: "offline" as const,
            remoteAccessOffReason: "saved_relay" as const,
        };
        mocks.getStatus.mockResolvedValue(status);
        mocks.enableUsingRelay.mockResolvedValue({
            ...status,
            savedRelayUrl: "",
            remoteAccess: { state: "starting" },
            remoteAccessOffReason: undefined,
        });
        await renderReady();

        expect(screen.getByText(copy.remoteAccessOff.saved_relay.replace("{{host}}", "relay.example.com"))).toBeInTheDocument();
        fireEvent.click(screen.getByRole("button", { name: copy.remoteAccessUseButton }));
        const dialog = await screen.findByRole("dialog");
        expect(dialog).toHaveTextContent(copy.remoteSwitchTitle);
        expect(dialog).toHaveTextContent(copy.remoteSwitchDescription.replaceAll("{{host}}", "relay.example.com"));
        expect(mocks.enableUsingRelay).not.toHaveBeenCalled();
        fireEvent.click(within(dialog).getByRole("button", { name: copy.remoteSwitchConfirm }));

        await waitFor(() => expect(mocks.enableUsingRelay).toHaveBeenCalledWith(""));
        await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    });

    it.each(["en", "zh-CN"])("does not offer remote access for a connected relay in %s", async (language) => {
        await i18n.changeLanguage(language);
        const copy = language === "en" ? en.mobileConnection : zh.mobileConnection;
        mocks.getStatus.mockResolvedValue({ ...connectedStatus, relayUrl: "https://relay.example.com", remoteAccessOffReason: "saved_relay" });
        await renderReady();
        expect(screen.queryByRole("button", { name: copy.remoteAccessUseButton })).not.toBeInTheDocument();
    });

    it.each(["en", "zh-CN"])("describes a private relay as local in %s", async (language) => {
        await i18n.changeLanguage(language);
        const copy = language === "en" ? en.mobileConnection : zh.mobileConnection;
        mocks.getStatus.mockResolvedValue({ ...connectedStatus, relayUrl: "https://192.168.1.4", relayLastSeenAt: "", remoteAccessOffReason: "saved_relay" });
        await renderReady();
        expect(screen.getByText(copy.statusConnectedLocalOnlyNoTime)).toBeInTheDocument();
        expect(screen.getByRole("button", { name: copy.remoteAccessUseButton })).toBeInTheDocument();
    });

    it.each(["en", "zh-CN"])("shows disabled remote access without a switch button in %s", async (language) => {
        await i18n.changeLanguage(language);
        const copy = language === "en" ? en.mobileConnection : zh.mobileConnection;
        mocks.getStatus.mockResolvedValue({
            ...idleStatus,
            relayUrl: "https://relay.example.com",
            remoteAccessOffReason: "disabled",
        });
        await renderReady();

        expect(screen.getByText(copy.remoteAccessOff.disabled)).toBeInTheDocument();
        expect(screen.queryByRole("button", { name: copy.remoteAccessUseButton })).not.toBeInTheDocument();
    });

    it("shows the saved server address read-only under Advanced while on, and turns the connection off", async () => {
        mocks.getStatus.mockResolvedValue(connectedStatus);
        mocks.disable.mockResolvedValue(idleStatus);
        await renderReady();
        openAdvanced();
        expect(screen.getByText("https://relay.example.com")).toBeInTheDocument();
        expect(screen.getByText("Turn off the connection to change the address.")).toBeInTheDocument();
        expect(screen.queryByLabelText("Server address")).not.toBeInTheDocument();
        fireEvent.click(screen.getByRole("button", { name: "Turn off" }));
        await waitFor(() => expect(mocks.disable).toHaveBeenCalledTimes(1));
        expect(await screen.findByText("Off")).toBeInTheDocument();
    });

    it("rejects server addresses that are not https before calling the desktop", async () => {
        await renderReady();
        openAdvanced();
        fireEvent.change(screen.getByLabelText("Server address"), {
            target: { value: "http://relay.example.com" },
        });
        fireEvent.click(screen.getByRole("button", { name: "Turn on" }));
        expect(await screen.findByRole("alert")).toHaveTextContent(
            "The server address under Advanced must start with https://",
        );
        expect(mocks.enableUsingRelay).not.toHaveBeenCalled();
    });

    it("names what Advanced holds and keeps it closed until opened", async () => {
        mocks.getStatus.mockResolvedValue(connectedStatus);
        await renderReady();
        const trigger = screen.getByRole("button", { name: /Advanced/ });
        expect(trigger).toHaveTextContent("Server address, what each phone can do");
        expect(trigger).toHaveAttribute("aria-expanded", "false");
        expect(screen.queryByText("What each phone can do")).not.toBeInTheDocument();
        openAdvanced();
        expect(screen.getByRole("heading", { name: "What each phone can do" })).toBeInTheDocument();
    });

    it("shows the QR code, instruction and copy link while an invitation is open", async () => {
        mocks.getStatus.mockResolvedValue(withCode());
        await renderReady();
        expect(screen.getByRole("img", { name: QR_NAME })).toBeInTheDocument();
        expect(screen.getByText("Open Isoform on your phone and scan this code.")).toBeInTheDocument();
        expect(screen.getByText(/works once/)).toBeInTheDocument();
        // The everyday card carries no developer-only hints.
        expect(screen.queryByText(/Simulator/)).not.toBeInTheDocument();
        fireEvent.click(screen.getByRole("button", { name: "Copy link" }));
        await screen.findByRole("button", { name: "Copied" });
        expect(mocks.clipboardCopy).toHaveBeenCalledWith(INVITATION);
        expect(mocks.nativeCopy).not.toHaveBeenCalled();
    });

    it("copies the invitation through the native clipboard inside the Wails window", async () => {
        mocks.getStatus.mockResolvedValue(withCode());
        (window as unknown as { runtime?: unknown }).runtime = { ClipboardSetText: vi.fn() };
        await renderReady();
        fireEvent.click(screen.getByRole("button", { name: "Copy link" }));
        await screen.findByRole("button", { name: "Copied" });
        expect(mocks.nativeCopy).toHaveBeenCalledWith(INVITATION);
        expect(mocks.clipboardCopy).not.toHaveBeenCalled();
    });

    it("reports a failed copy instead of claiming success", async () => {
        mocks.getStatus.mockResolvedValue(withCode());
        mocks.clipboardCopy.mockResolvedValue(false);
        await renderReady();
        fireEvent.click(screen.getByRole("button", { name: "Copy link" }));
        expect(await screen.findByRole("alert")).toHaveTextContent("Could not copy the link. Try again.");
        expect(screen.queryByRole("button", { name: "Copied" })).not.toBeInTheDocument();
    });

    it("opens with Pair an iPhone and creates a fresh invitation when no iPhone is paired", async () => {
        mocks.getStatus.mockResolvedValue(noPhoneStatus);
        mocks.generateCode.mockResolvedValue(withCode(300, { devices: [] }));
        await renderReady();
        expect(screen.getByRole("heading", { name: "Pair a phone" })).toBeInTheDocument();
        expect(screen.queryByRole("heading", { name: "Your phones" })).not.toBeInTheDocument();
        fireEvent.click(screen.getByRole("button", { name: "Show pairing code" }));
        expect(await screen.findByRole("img", { name: QR_NAME })).toBeInTheDocument();
        expect(mocks.generateCode).toHaveBeenCalledTimes(1);
    });

    it("lists paired iPhones and offers to pair another one", async () => {
        mocks.getStatus.mockResolvedValue(connectedStatus);
        mocks.generateCode.mockResolvedValue(withCode());
        await renderReady();
        expect(screen.getByRole("heading", { name: "Your phones" })).toBeInTheDocument();
        expect(screen.queryByRole("heading", { name: "Pair a phone" })).not.toBeInTheDocument();
        fireEvent.click(screen.getByRole("button", { name: "Pair another phone" }));
        expect(await screen.findByRole("img", { name: QR_NAME })).toBeInTheDocument();
        expect(screen.getByRole("button", { name: "Create new code" })).toBeInTheDocument();
    });

    it("explains why pairing waits while the connection is off", async () => {
        await renderReady();
        expect(screen.getByRole("button", { name: "Show pairing code" })).toBeDisabled();
        expect(screen.getByText("Turn on the connection above before you pair a phone.")).toBeInTheDocument();
    });

    it("does not let a status read started before a new code replace it", async () => {
        mocks.getStatus.mockResolvedValue(noPhoneStatus);
        mocks.generateCode.mockResolvedValue(withCode());
        await renderReady();
        let resolveOld!: (value: MobileConnectionStatus) => void;
        mocks.getStatus.mockImplementationOnce(
            () => new Promise<MobileConnectionStatus>((resolve) => { resolveOld = resolve; }),
        );
        fireEvent(window, new Event("focus"));
        fireEvent.click(screen.getByRole("button", { name: "Show pairing code" }));
        expect(await screen.findByRole("img", { name: QR_NAME })).toBeInTheDocument();
        await act(async () => {
            resolveOld(idleStatus);
        });
        expect(screen.getByRole("img", { name: QR_NAME })).toBeInTheDocument();
    });

    it("hides an expired invitation and offers no copy action", async () => {
        mocks.getStatus.mockResolvedValue(withCode(-5));
        await renderReady();
        expect(screen.queryByRole("img", { name: QR_NAME })).not.toBeInTheDocument();
        expect(screen.queryByRole("button", { name: "Copy link" })).not.toBeInTheDocument();
        expect(screen.getByText("This code expired. Create a new code to pair your phone.")).toBeInTheDocument();
    });

    it("shows the approval code and confirms the pairing with its id", async () => {
        mocks.getStatus.mockResolvedValue(withCode(300, {
            pendingPairing: { pairingId: "pairing-1", deviceName: "Alex's iPhone", code: "123456" },
        }));
        mocks.confirmPairing.mockResolvedValue(connectedStatus);
        await renderReady();
        const dialog = await screen.findByRole("dialog");
        expect(within(dialog).getByText("Alex's iPhone wants to pair with this computer.")).toBeInTheDocument();
        expect(within(dialog).getByText("123 456")).toBeInTheDocument();
        expect(within(dialog).getByText(
            "Check that this code matches the code on the phone. Approve only when the two codes are the same.",
        )).toBeInTheDocument();
        expect(within(dialog).getByText("Once your phone is paired, Isoform can keep this computer awake so Chat on your phone can connect. You can change this in Settings › Phone.")).toBeInTheDocument();
        expect(screen.queryByRole("img", { name: QR_NAME })).not.toBeInTheDocument();
        fireEvent.click(within(dialog).getByRole("button", { name: "Approve" }));
        await waitFor(() => expect(mocks.confirmPairing).toHaveBeenCalledWith("pairing-1", true));
        await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    });

    it("asks before a paired phone saves corrections from Chat, and answers with its device id", async () => {
        mocks.getStatus.mockResolvedValue({
            ...connectedStatus,
            devices: [{ ...phone, scopeRequest: { scopes: ["chat:correct"], state: "waiting", at: "2026-11-16T09:00:00Z" } }],
        });
        mocks.confirmScopeRequest.mockResolvedValue(connectedStatus);
        await renderReady();
        expect(await screen.findByText("My iPhone asks to save corrections from Chat on this computer.")).toBeInTheDocument();
        fireEvent.click(screen.getByRole("button", { name: "Allow" }));
        await waitFor(() => expect(mocks.confirmScopeRequest).toHaveBeenCalledWith("phone-1", true));
        await waitFor(() => expect(screen.queryByText("My iPhone asks to save corrections from Chat on this computer.")).not.toBeInTheDocument());
    });

    it("declines a pairing, and lets the user defer the decision and reopen it", async () => {
        mocks.getStatus.mockResolvedValue(withCode(300, {
            pendingPairing: { pairingId: "pairing-1", deviceName: "Alex's iPhone", code: "123456" },
        }));
        mocks.confirmPairing.mockResolvedValue(connectedStatus);
        await renderReady();
        fireEvent.click(within(await screen.findByRole("dialog")).getByRole("button", { name: "Decide later" }));
        await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
        expect(screen.getByText("Alex's iPhone is waiting for you to confirm the code.")).toBeInTheDocument();
        fireEvent.click(screen.getByRole("button", { name: "Review" }));
        const dialog = await screen.findByRole("dialog");
        fireEvent.click(within(dialog).getByRole("button", { name: "Decline" }));
        await waitFor(() => expect(mocks.confirmPairing).toHaveBeenCalledWith("pairing-1", false));
    });

    it("disconnects an iPhone only after confirmation", async () => {
        mocks.getStatus.mockResolvedValue(connectedStatus);
        mocks.revoke.mockResolvedValue({ ...connectedStatus, devices: [] });
        await renderReady();
        fireEvent.click(screen.getByRole("button", { name: "Disconnect My iPhone" }));
        const dialog = await screen.findByRole("dialog");
        expect(dialog).toHaveTextContent("My iPhone will lose access to this computer.");
        expect(mocks.revoke).not.toHaveBeenCalled();
        fireEvent.click(within(dialog).getByRole("button", { name: "Disconnect phone" }));
        await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
        expect(mocks.revoke).toHaveBeenCalledWith("phone-1");
        expect(screen.getByRole("heading", { name: "Pair a phone" })).toBeInTheDocument();
    });

    it.each(["failed", "restarting"] as const)(
        "shows a translated remote-access hint while %s",
        async (state) => {
            const diagnostic = "remote access startup failed: diagnostic";
            mocks.getStatus.mockResolvedValue({
                ...connectedStatus,
                remoteAccess: { state, lastError: diagnostic },
            });
            await renderReady();
            const alert = screen.getByRole("alert");
            expect(alert).toHaveTextContent("Remote access stopped. Isoform keeps trying.");
            expect(alert.className).toContain("text-copper");
            expect(within(alert).getByRole("button", { name: "Try again" })).toBeInTheDocument();
            expect(
                screen.getByText("Not running, so your phone can reach this computer only on the same Wi-Fi."),
            ).toBeInTheDocument();
            expect(screen.queryByText(diagnostic)).not.toBeInTheDocument();
        },
    );

    it("requires confirmation before rotating the remote key and disconnecting paired phones", async () => {
        mocks.getStatus.mockResolvedValue({
            ...connectedStatus,
            remoteAccess: { state: "ready" },
        });
        mocks.resetRemoteAccess.mockResolvedValue({
            ...connectedStatus,
            devices: [],
            remoteAccess: { state: "ready" },
        });
        await renderReady();
        fireEvent.click(screen.getByRole("button", { name: "Reset remote access" }));
        const dialog = await screen.findByRole("dialog");
        expect(dialog).toHaveTextContent("Each phone must be paired again.");
        expect(mocks.resetRemoteAccess).not.toHaveBeenCalled();
        fireEvent.click(within(dialog).getByRole("button", { name: "Reset and disconnect phones" }));
        await waitFor(() => expect(mocks.resetRemoteAccess).toHaveBeenCalledTimes(1));
        await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    });

    it("shows when each iPhone was last seen, and what it can do only in its details under Advanced", async () => {
        mocks.getStatus.mockResolvedValue({
            ...connectedStatus,
            devices: [{ ...phone, scopes: [...phone.scopes, "chat:read", "desk:read"] }],
        });
        await renderReady();
        expect(screen.getByText("Last seen 2 hours ago")).toBeInTheDocument();
        expect(screen.queryByText("Read your daily brief")).not.toBeInTheDocument();
        openAdvanced();
        expect(screen.getByText("Read your daily brief")).toBeInTheDocument();
        expect(screen.getByText("Ask for a fresh brief")).toBeInTheDocument();
        expect(screen.getByText("See whether this computer is online")).toBeInTheDocument();
        expect(screen.getByText("Get notifications")).toBeInTheDocument();
        expect(screen.getByText("Read your chats with Isoform")).toBeInTheDocument();
        expect(screen.getByText(/Paired on/)).toBeInTheDocument();
        expect(screen.getByText(/Access until/)).toBeInTheDocument();
        // desk:read is retired: older phones hold it, nothing accepts it, so it is not listed.
        expect(screen.queryByText(/desk/i)).not.toBeInTheDocument();
    });

    it("keeps a failed disconnection visible in the dialog across status refreshes", async () => {
        mocks.getStatus.mockResolvedValue(connectedStatus);
        mocks.revoke
            .mockRejectedValueOnce(new Error("component-test transport failure"))
            .mockResolvedValueOnce({ ...connectedStatus, devices: [] });
        await renderReady();
        fireEvent.click(screen.getByRole("button", { name: "Disconnect My iPhone" }));
        const dialog = await screen.findByRole("dialog");
        fireEvent.click(within(dialog).getByRole("button", { name: "Disconnect phone" }));
        expect(await within(dialog).findByRole("alert")).toHaveTextContent(
            "Could not disconnect this phone. Try again.",
        );
        fireEvent(window, new Event("focus"));
        await waitFor(() => expect(mocks.getStatus).toHaveBeenCalledTimes(2));
        expect(within(dialog).getByRole("alert")).toHaveTextContent(
            "Could not disconnect this phone. Try again.",
        );
        fireEvent.click(within(dialog).getByRole("button", { name: "Disconnect phone" }));
        await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
        expect(screen.queryByRole("alert")).not.toBeInTheDocument();
        expect(mocks.revoke).toHaveBeenCalledTimes(2);
        expect(screen.getByRole("heading", { name: "Pair a phone" })).toBeInTheDocument();
    });

    it("has no brief schedule on Your phone", async () => {
        await renderReady();
        expectNoBriefSchedule();
        openAdvanced();
        expectNoBriefSchedule();
    });

    it("reloads on the mobile:updated event and on focus, and unsubscribes on unmount", async () => {
        const unsubscribe = vi.fn();
        mocks.eventsOn.mockReturnValue(unsubscribe);
        const view = await renderReady();
        const listener = mocks.eventsOn.mock.calls.find(([name]) => name === "mobile:updated")?.[1] as
            | (() => void)
            | undefined;
        expect(listener).toBeDefined();
        await act(async () => {
            listener?.();
        });
        await waitFor(() => expect(mocks.getStatus).toHaveBeenCalledTimes(2));
        fireEvent(window, new Event("focus"));
        await waitFor(() => expect(mocks.getStatus).toHaveBeenCalledTimes(3));
        view.unmount();
        expect(unsubscribe).toHaveBeenCalledTimes(1);
    });

    it("polls status every 15 seconds only while an invitation is displayed", async () => {
        mocks.getStatus.mockResolvedValue(noPhoneStatus);
        mocks.generateCode.mockResolvedValue(withCode());
        await renderReady();
        vi.useFakeTimers();
        await act(async () => {
            await vi.advanceTimersByTimeAsync(30_000);
        });
        expect(mocks.getStatus).toHaveBeenCalledTimes(1);

        await act(async () => {
            fireEvent.click(screen.getByRole("button", { name: "Show pairing code" }));
        });
        await act(async () => {
            await vi.advanceTimersByTimeAsync(15_000);
        });
        expect(mocks.getStatus).toHaveBeenCalledTimes(2);
    });

    it("offers a retry when the connection status cannot be read", async () => {
        mocks.getStatus.mockRejectedValueOnce(new Error("bridge offline"));
        render(<MobileConnectionSettings />);
        const retry = (await screen.findAllByRole("button", { name: "Try again" }))[0];
        expect(screen.getAllByRole("alert")[0]).toHaveTextContent("Could not check phone connections. Try again.");
        fireEvent.click(retry);
        await waitFor(() => expect(screen.queryAllByRole("alert")).toHaveLength(0));
        expect(screen.getByRole("heading", { name: "Pair a phone" })).toBeInTheDocument();
    });

    it("renders the Chinese copy in the zh-CN locale", async () => {
        await act(async () => {
            await i18n.changeLanguage("zh-CN");
        });
        const view = await renderReady();
        try {
            // Pairing reads 配对; 连接 is kept for the connection switch.
            expect(screen.getByRole("heading", { name: "配对手机" })).toBeInTheDocument();
            expect(screen.getByRole("button", { name: "显示配对码" })).toBeInTheDocument();
            expect(screen.getByText("已关闭")).toBeInTheDocument();
        } finally {
            view.unmount();
            await act(async () => {
                await i18n.changeLanguage("en");
            });
        }
    });

    it("says the relay is offline in plain words with no server address or tunnel (ST68)", () => {
        expect(en.mobileConnection.statusOfflineDescription).toBe(
            "Your phone can't reach this computer right now: the connection server isn't answering. Isoform keeps trying.",
        );
        expect(zh.mobileConnection.statusOfflineDescription).toBe("你的手机现在连不上这台电脑：连接服务器没有响应。Isoform 会持续重试。");
        for (const copy of [en.mobileConnection, zh.mobileConnection]) {
            const remote = JSON.stringify([copy.remoteAccessOff, copy.remoteAccessDescription, copy.remoteAccessStatus,
                copy.remoteResetDescription, copy.remoteSwitchDescription, copy.serverRemoteAccess, copy.statusOfflineDescription]);
            expect(remote).not.toMatch(/\{\{host\}\}|tunnel|隧道/i);
        }
    });

    it("keeps every mobileConnection key used by the component in both locales", () => {
        const used = new Set(
            [...componentSource.matchAll(/["'`](mobileConnection\.[A-Za-z0-9]+)["'`]/g)].map(
                (match) => match[1].slice("mobileConnection.".length),
            ),
        );
        const enKeys = Object.keys(en.mobileConnection).sort();
        const zhKeys = Object.keys(zh.mobileConnection).sort();
        expect([...used].filter((key) => !enKeys.includes(key))).toEqual([]);
        expect(zhKeys).toEqual(enKeys);
    });

    it("shows the keep-awake switch under Your phones once a phone is paired, off until turned on", async () => {
        mocks.getStatus.mockResolvedValue(connectedStatus);
        mocks.getKeepAwake.mockResolvedValue({ on: false, pairedPhones: 1, onPower: true, hasBattery: true });
        await renderReady();
        expect(await screen.findByRole("switch", { name: KEEP_AWAKE_NAME })).not.toBeChecked();
    });

    it("reads the keep-awake switch after the phone list and Pair another phone", async () => {
        mocks.getStatus.mockResolvedValue(connectedStatus);
        mocks.getKeepAwake.mockResolvedValue({ on: true, pairedPhones: 1, onPower: true, hasBattery: true });
        await renderReady();
        const pairAnother = screen.getByRole("button", { name: "Pair another phone" });
        const keepAwake = await screen.findByRole("switch", { name: KEEP_AWAKE_NAME });
        expect(pairAnother.compareDocumentPosition(keepAwake) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    });

    it.each(["en", "zh-CN"])("chooses the laptop or desktop keep-awake copy from battery status in %s", async (language) => {
        await i18n.changeLanguage(language);
        const copy = language === "en" ? en.mobileConnection : zh.mobileConnection;
        mocks.getStatus.mockResolvedValue(connectedStatus);
        mocks.getKeepAwake.mockResolvedValue({ on: true, pairedPhones: 1, onPower: true, hasBattery: true });
        const laptop = await renderReady();
        expect(await screen.findByRole("switch", { name: copy.keepAwakeTitle })).toBeChecked();
        expect(screen.getByText(copy.keepAwakeDescription)).toBeInTheDocument();
        expect(screen.queryByText(copy.keepAwakeDescriptionDesktop)).not.toBeInTheDocument();
        laptop.unmount();

        mocks.getKeepAwake.mockResolvedValue({ on: true, pairedPhones: 1, onPower: true, hasBattery: false });
        await renderReady();
        expect(screen.getByText(copy.keepAwakeDescriptionDesktop)).toBeInTheDocument();
        expect(screen.queryByText(copy.keepAwakeDescription)).not.toBeInTheDocument();
    });

    it("hides the keep-awake switch before any phone is paired", async () => {
        mocks.getKeepAwake.mockResolvedValue({ on: true, pairedPhones: 0, onPower: false, hasBattery: true });
        await renderReady();
        await waitFor(() => expect(mocks.getKeepAwake).toHaveBeenCalled());
        expect(screen.queryByRole("switch", { name: KEEP_AWAKE_NAME })).not.toBeInTheDocument();
    });

    it("saves the keep-awake switch at once and puts it back when the save fails", async () => {
        mocks.getStatus.mockResolvedValue(connectedStatus);
        mocks.getKeepAwake.mockResolvedValue({ on: true, pairedPhones: 1, onPower: true, hasBattery: true });
        mocks.setKeepAwake.mockResolvedValueOnce({ on: false, pairedPhones: 1, onPower: true, hasBattery: true });
        await renderReady();
        fireEvent.click(await screen.findByRole("switch", { name: KEEP_AWAKE_NAME }));
        await waitFor(() => expect(mocks.setKeepAwake).toHaveBeenCalledWith(false));
        await waitFor(() => expect(screen.getByRole("switch", { name: KEEP_AWAKE_NAME })).not.toBeChecked());

        mocks.setKeepAwake.mockRejectedValueOnce(new Error("save failed"));
        fireEvent.click(screen.getByRole("switch", { name: KEEP_AWAKE_NAME }));
        expect(await screen.findByText("Could not change this setting. Try again.")).toBeInTheDocument();
        expect(screen.getByRole("switch", { name: KEEP_AWAKE_NAME })).not.toBeChecked();
    });

    it("never passes fallback strings to translation calls", () => {
        expect(componentSource).not.toMatch(/\bt\(\s*["'`]mobileConnection\.[A-Za-z0-9]+["'`]\s*,\s*["'`]/);
    });

    // computer-settings v1.0.0: the page is "Your phone".
    it("names the page Your phone in both locales", () => {
        expect(en.pages.iphone.label).toBe("Your phone");
        expect(zh.pages.iphone.label).toBe("你的手机");
    });
});

describe("brief time labels (ST69)", () => {
    it("reads 7:00 AM and 6:00 PM, keeps an off-grid saved time, and says 上午7:00 in Chinese", async () => {
        const { clockLabel, clockOptions } = await import("./mobile-connection-settings");
        expect(clockLabel("07:00", "en")).toBe("7:00 AM");
        expect(clockLabel("18:00", "en")).toBe("6:00 PM");
        expect(clockLabel("07:00", "zh-CN")).toBe("上午7:00");
        expect(clockOptions("07:40")).toContain("07:40");
        expect(clockOptions("07:00")).toHaveLength(96);
    });
});

it.each(["en", "zh-CN"])("shows why an unreachable relay cannot produce a code in %s", async (language) => {
    await i18n.changeLanguage(language);
    mocks.getStatus.mockResolvedValue(noPhoneStatus);
    mocks.generateCode.mockRejectedValue(new Error("remote access is not ready; try again when the relay is available"));
    await renderReady();
    fireEvent.click(screen.getByRole("button", { name: language === "en" ? en.mobileConnection.showCode : zh.mobileConnection.showCode }));
    expect(await screen.findByText(language === "en" ? en.mobileConnection.relayUnreachable : zh.mobileConnection.relayUnreachable)).toBeInTheDocument();
    expect(screen.queryByRole("img", { name: QR_NAME })).not.toBeInTheDocument();
});
