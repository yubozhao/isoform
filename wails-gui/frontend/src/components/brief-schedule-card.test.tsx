import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import i18n from "@/i18n";
import { pageQueryCache } from "@/lib/query-cache";
import type {
    MobileBriefSchedule,
    MobileConnectionStatus,
} from "@/lib/wails-shim-app";
import en from "@/i18n/locales/en/settings.json";
import zh from "@/i18n/locales/zh-CN/settings.json";
import { BriefScheduleCard } from "./brief-schedule-card";

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

async function renderReady(props: Parameters<typeof BriefScheduleCard>[0] = {}) {
    const view = render(<BriefScheduleCard {...props} />);
    await waitFor(() => {
        expect(screen.queryAllByText(/Checking phone connections|Loading brief settings/)).toHaveLength(0);
    });
    return view;
}

// The brief's schedule moved from Your phone to Briefs and notifications
// (computer-settings v1.0.0 rule 6); these tests moved with it, unchanged
// except where the card now stands alone.
describe("BriefScheduleCard", () => {
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

    it("saves the brief switch and times as soon as they change, with no Save button", async () => {
        mocks.setSchedule.mockImplementation(async (next: MobileBriefSchedule) => next);
        await renderReady();
        expect(screen.queryByRole("button", { name: /Save/ })).not.toBeInTheDocument();
        fireEvent.click(screen.getByRole("switch", { name: "Send automatically" }));
        await waitFor(() => expect(mocks.setSchedule).toHaveBeenCalledWith({ ...schedule, enabled: true }));
        expect(screen.getByLabelText("Morning")).toBeEnabled();
        fireEvent.change(screen.getByLabelText("Morning"), { target: { value: "06:30" } });
        await waitFor(() => expect(mocks.setSchedule).toHaveBeenLastCalledWith({
            ...schedule,
            enabled: true,
            morning: "06:30",
        }));
        expect(screen.getByLabelText("Morning")).toHaveValue("06:30");
    });

    it("shows the podcast feed address with a copy button once the feed is on", async () => {
        mocks.getSchedule.mockResolvedValue({ ...schedule, feedEnabled: true, feedUrl: "http://127.0.0.1:47831/feed/abc/brief.xml" });
        await renderReady();
        expect(screen.getByRole("switch", { name: "Podcast feed on this computer" })).toBeChecked();
        expect(screen.getByTestId("brief-feed-url")).toHaveTextContent("http://127.0.0.1:47831/feed/abc/brief.xml");
        fireEvent.click(screen.getByRole("button", { name: /Copy feed address/ }));
        await waitFor(() => expect(mocks.clipboardCopy).toHaveBeenCalledWith("http://127.0.0.1:47831/feed/abc/brief.xml"));
        expect(await screen.findByRole("button", { name: /Copied/ })).toBeInTheDocument();
    });

    it("saves the feed switch with the rest of the schedule and hides the address while it starts", async () => {
        mocks.setSchedule.mockResolvedValue({ ...schedule, feedEnabled: true, feedUrl: "" });
        await renderReady();
        fireEvent.click(screen.getByRole("switch", { name: "Podcast feed on this computer" }));
        await waitFor(() => expect(mocks.setSchedule).toHaveBeenCalledWith({ ...schedule, feedEnabled: true }));
        expect(await screen.findByText("The feed is starting.")).toBeInTheDocument();
        expect(screen.queryByTestId("brief-feed-url")).not.toBeInTheDocument();
    });

    it("saves adaptive timing and preserves the choice when editing a fixed time", async () => {
        mocks.getSchedule.mockResolvedValue({ ...schedule, enabled: true });
        mocks.setSchedule.mockImplementation(async (next: MobileBriefSchedule) => next);
        await renderReady();
        const adaptive = screen.getByRole("switch", { name: "Adaptive timing" });
        expect(adaptive).not.toBeChecked();
        fireEvent.click(adaptive);
        await waitFor(() => expect(mocks.setSchedule).toHaveBeenLastCalledWith({
            ...schedule, enabled: true, adaptiveTiming: true,
        }));
        fireEvent.change(screen.getByLabelText("Morning"), { target: { value: "08:00" } });
        await waitFor(() => expect(mocks.setSchedule).toHaveBeenLastCalledWith({
            ...schedule, enabled: true, adaptiveTiming: true, morning: "08:00",
        }));
        fireEvent.click(adaptive);
        await waitFor(() => expect(mocks.setSchedule).toHaveBeenLastCalledWith({
            ...schedule, enabled: true, adaptiveTiming: false, morning: "08:00",
        }));
    });

    it.each(["en", "zh-CN"])("shows the prediction and privacy copy in %s without a duplicate fixed next time", async (locale) => {
        await i18n.changeLanguage(locale);
        const copy = locale === "en" ? en.mobileConnection : zh.mobileConnection;
        const predictedAt = "2026-10-18T07:40:00Z";
        mocks.getSchedule.mockResolvedValue({
            ...schedule, enabled: true, adaptiveTiming: true,
            nextPredictedAt: predictedAt, nextPredictedSlot: "morning", timingReason: "learned_opens",
        });
        mocks.getHealth.mockResolvedValue({
            enabled: true, lastRunAt: "", lastSlot: "", nextRunAt: "2026-10-18T08:00:00Z", nextSlot: "morning",
            missedRuns: 0, failingSlot: "", failedAttempts: 0, latestBriefId: "",
        });
        await renderReady();
        expect(await screen.findByRole("switch", { name: copy.briefAdaptive })).toBeChecked();
        expect(screen.getByText(copy.briefAdaptivePrivacy)).toBeInTheDocument();
        // settings-review ST67: the slot follows the time; a 12-hour time.
        const time = new Date(predictedAt).toLocaleTimeString(locale, { hour: "numeric", minute: "2-digit" });
        expect(screen.getByText((text) => /^(Next brief:|下次简报：)/.test(text)
            && text.includes(time) && text.includes(copy.briefTimingOpens))).toBeInTheDocument();
        expect(screen.getAllByText(locale === "en" ? /^Next brief:/ : /^下次简报：/)).toHaveLength(1);
    });

    it.each([
        // ST67: 7:40 is not the chosen 7:00, so it is not "your chosen time".
        ["fixed_slot", "timed to when you finish"],
        ["learned_presence", "before you usually open Isoform on your phone"],
        ["learned_activity", "based on your usual computer activity"],
        ["before_meeting", "before your first meeting"],
        ["work_settled", "timed to when you finish"],
        ["quiet_hours", "after quiet hours"],
        ["waiting_for_activity", "waiting for work activity to settle"],
        ["unrecognized_private_server_text", "estimated delivery time"],
    ])("translates the %s timing reason without displaying server text", async (reason, label) => {
        mocks.getSchedule.mockResolvedValue({
            ...schedule, enabled: true, adaptiveTiming: true,
            nextPredictedAt: "2026-10-18T07:40:00Z", nextPredictedSlot: "morning", timingReason: reason,
        });
        await renderReady();
        expect(screen.getByText(/^Next brief:/)).toHaveTextContent(label);
        expect(screen.queryByText(/unrecognized_private_server_text/)).not.toBeInTheDocument();
    });

    it("shows no eligible time instead of a fixed delivery when no window remains", async () => {
        mocks.getSchedule.mockResolvedValue({
            ...schedule, enabled: true, adaptiveTiming: true, timingReason: "no_window",
        });
        mocks.getHealth.mockResolvedValue({
            enabled: true, lastRunAt: "", lastSlot: "", nextRunAt: "2026-10-18T08:00:00Z", nextSlot: "morning",
            missedRuns: 0, failingSlot: "", failedAttempts: 0, latestBriefId: "",
        });
        await renderReady();
        expect(screen.getByText("No eligible brief time: quiet hours or the delay limit leave no delivery window.")).toBeInTheDocument();
        expect(screen.queryByText(/^Next brief:/)).not.toBeInTheDocument();
    });

    it("falls back to the health time when the prediction timestamp is invalid", async () => {
        mocks.getSchedule.mockResolvedValue({
            ...schedule, enabled: true, adaptiveTiming: true,
            nextPredictedAt: "invalid", nextPredictedSlot: "morning", timingReason: "learned_opens",
        });
        mocks.getHealth.mockResolvedValue({
            enabled: true, lastRunAt: "", lastSlot: "", nextRunAt: "2026-10-18T08:00:00Z", nextSlot: "morning",
            missedRuns: 0, failingSlot: "", failedAttempts: 0, latestBriefId: "",
        });
        await renderReady();
        expect(screen.getByText(/^Next brief:/)).not.toHaveTextContent("before you usually open a brief");
    });

    it("shows last, next and missed brief lines from the health view", async () => {
        mocks.getHealth.mockResolvedValue({
            enabled: true, lastRunAt: "", lastSlot: "", nextRunAt: "2026-10-14T18:00:00Z", nextSlot: "evening",
            missedRuns: 2, failingSlot: "morning", failedAttempts: 3, latestBriefId: "",
        });
        await renderReady();
        expect(await screen.findByText(/^Next brief: Evening, /)).toBeInTheDocument();
        expect(screen.getByText("Missed in the last 7 days: 2")).toBeInTheDocument();
        expect(screen.getByText("The Morning brief has failed 3 times and is being retried.")).toBeInTheDocument();
    });

    it("refreshes deferred and skipped predictions every minute without losing a half-typed time", async () => {
        vi.useFakeTimers();
        const focused = vi.spyOn(document, "hasFocus").mockReturnValue(true);
        const visible = vi.spyOn(document, "visibilityState", "get").mockReturnValue("visible");
        const current = {
            ...schedule, enabled: true, adaptiveTiming: true,
            nextPredictedAt: "2026-10-18T18:00:00Z", nextPredictedSlot: "evening", timingReason: "work_settled",
        };
        mocks.getSchedule.mockResolvedValue(current);
        try {
            await act(async () => { render(<BriefScheduleCard />); });
            fireEvent.change(screen.getByLabelText("Evening"), { target: { value: "" } });
            const deferredAt = "2026-10-18T18:30:00Z";
            mocks.getSchedule.mockResolvedValue({
                ...current, nextPredictedAt: deferredAt, timingReason: "waiting_for_activity",
            });
            await act(async () => { await vi.advanceTimersByTimeAsync(59_000); });
            expect(mocks.getSchedule).toHaveBeenCalledTimes(1);
            await act(async () => { await vi.advanceTimersByTimeAsync(1_000); });
            expect(mocks.getSchedule).toHaveBeenCalledTimes(2);
            const time = new Date(deferredAt).toLocaleTimeString("en", { hour: "numeric", minute: "2-digit" });
            expect(screen.getByText(/^Next brief:/)).toHaveTextContent(time);
            expect(screen.getByText(/^Next brief:/)).toHaveTextContent("waiting for work activity to settle");
            expect(screen.getByText("Enter a time such as 07:00.")).toBeInTheDocument();
            mocks.getSchedule.mockResolvedValue({ ...current, nextPredictedAt: "", timingReason: "no_window" });
            await act(async () => { await vi.advanceTimersByTimeAsync(60_000); });
            expect(screen.getByText(/^No eligible brief time:/)).toBeInTheDocument();
            expect(mocks.setSchedule).not.toHaveBeenCalled();
        } finally {
            focused.mockRestore();
            visible.mockRestore();
        }
    });

    it("polls the schedule only while focused and visible and stops on unmount", async () => {
        vi.useFakeTimers();
        const focused = vi.spyOn(document, "hasFocus").mockReturnValue(false);
        const visible = vi.spyOn(document, "visibilityState", "get").mockReturnValue("visible");
        try {
            const view = render(<BriefScheduleCard />);
            await act(async () => { await vi.advanceTimersByTimeAsync(60_000); });
            expect(mocks.getSchedule).toHaveBeenCalledTimes(1);
            focused.mockReturnValue(true);
            visible.mockReturnValue("hidden");
            await act(async () => { await vi.advanceTimersByTimeAsync(60_000); });
            expect(mocks.getSchedule).toHaveBeenCalledTimes(1);
            visible.mockReturnValue("visible");
            await act(async () => { await vi.advanceTimersByTimeAsync(60_000); });
            expect(mocks.getSchedule).toHaveBeenCalledTimes(2);
            view.unmount();
            await act(async () => { await vi.advanceTimersByTimeAsync(60_000); });
            expect(mocks.getSchedule).toHaveBeenCalledTimes(2);
        } finally {
            focused.mockRestore();
            visible.mockRestore();
        }
    });

    it("pauses polling during a save and ignores a poll response that predates the save", async () => {
        vi.useFakeTimers();
        const focused = vi.spyOn(document, "hasFocus").mockReturnValue(true);
        const visible = vi.spyOn(document, "visibilityState", "get").mockReturnValue("visible");
        const current = { ...schedule, enabled: true, adaptiveTiming: false };
        let resolvePoll!: (value: MobileBriefSchedule) => void;
        let resolveSave!: (value: MobileBriefSchedule) => void;
        mocks.getSchedule.mockResolvedValueOnce(current).mockImplementationOnce(
            () => new Promise<MobileBriefSchedule>((resolve) => { resolvePoll = resolve; }),
        );
        mocks.setSchedule.mockImplementationOnce(
            () => new Promise<MobileBriefSchedule>((resolve) => { resolveSave = resolve; }),
        );
        try {
            await act(async () => { render(<BriefScheduleCard />); });
            await act(async () => { await vi.advanceTimersByTimeAsync(60_000); });
            expect(mocks.getSchedule).toHaveBeenCalledTimes(2);
            fireEvent.click(screen.getByRole("switch", { name: "Adaptive timing" }));
            await act(async () => { await vi.advanceTimersByTimeAsync(60_000); });
            expect(mocks.getSchedule).toHaveBeenCalledTimes(2);
            await act(async () => { resolveSave({ ...current, adaptiveTiming: true }); });
            await act(async () => { resolvePoll(current); });
            expect(screen.getByRole("switch", { name: "Adaptive timing" })).toBeChecked();
            expect(mocks.setSchedule).toHaveBeenCalledTimes(1);
        } finally {
            focused.mockRestore();
            visible.mockRestore();
        }
    });

    it("keeps the brief times off, with the reason, until Send automatically is on", async () => {
        await renderReady();
        expect(screen.getByLabelText("Morning")).toBeDisabled();
        expect(screen.getByLabelText("Evening")).toBeDisabled();
        expect(screen.getByText("Turn on Send automatically to choose the times.")).toBeInTheDocument();
    });

    it("does not save a half-typed brief time", async () => {
        mocks.getSchedule.mockResolvedValue({ ...schedule, enabled: true });
        await renderReady();
        fireEvent.change(screen.getByLabelText("Evening"), { target: { value: "" } });
        expect(screen.getByText("Enter a time such as 07:00.")).toBeInTheDocument();
        expect(mocks.setSchedule).not.toHaveBeenCalled();
    });

    it("puts the brief switch back and says so when a save fails", async () => {
        await renderReady();
        fireEvent.click(screen.getByRole("switch", { name: "Send automatically" }));
        expect(await screen.findByRole("alert")).toHaveTextContent("Could not save the brief schedule. Try again.");
        expect(screen.getByRole("switch", { name: "Send automatically" })).not.toBeChecked();
    });

    it("explains under the brief when briefs come without a written summary, and links to Assistant", async () => {
        mocks.getSchedule.mockResolvedValue({ ...schedule, narrativeAvailable: false });
        const onOpenAssistant = vi.fn();
        await renderReady({ onOpenAssistant });
        expect(screen.getByRole("note")).toHaveTextContent(
            "When Isoform uses Claude Code or Codex as its assistant, briefs leave out the short written summary at the top.",
        );
        // Points at the switch by its name on the Assistant page.
        expect(screen.getByRole("note")).toHaveTextContent(
            "turn on Use your own API key or model under Advanced on the Assistant page.",
        );
        fireEvent.click(screen.getByRole("button", { name: "Open Assistant" }));
        expect(onOpenAssistant).toHaveBeenCalledTimes(1);
    });

    it("answers started, running and cooling-down brief requests under the button, not in toasts (ST25)", async () => {
        mocks.generateBrief
            .mockResolvedValueOnce({ status: "started", nextAllowedAt: "" })
            .mockResolvedValueOnce({ status: "running", nextAllowedAt: "" })
            .mockResolvedValueOnce({ status: "cooldown", nextAllowedAt: "2026-10-09T18:30:00Z" });
        await renderReady();
        const generate = screen.getByRole("button", { name: "Generate brief now" });

        fireEvent.click(generate);
        expect(await screen.findByText("Writing a fresh brief. It reaches your phone when it's ready.")).toBeInTheDocument();
        await waitFor(() => expect(generate).toBeEnabled());

        fireEvent.click(generate);
        expect(await screen.findByText("A brief is already being written.")).toBeInTheDocument();
        await waitFor(() => expect(generate).toBeEnabled());

        fireEvent.click(generate);
        const retryAt = new Date("2026-10-09T18:30:00Z").toLocaleTimeString("en", {
            hour: "numeric",
            minute: "2-digit",
        });
        expect(await screen.findByText(`You can ask for another brief at ${retryAt}.`)).toBeInTheDocument();
        expect(mocks.notifyToast).not.toHaveBeenCalled();
    });

    it("shows an error when a brief cannot be started", async () => {
        mocks.generateBrief.mockRejectedValueOnce(new Error("bridge offline"));
        await renderReady();
        fireEvent.click(screen.getByRole("button", { name: "Generate brief now" }));
        const alert = await screen.findByRole("alert");
        expect(alert).toHaveTextContent("Couldn't start a brief.");
        expect(alert.className).toContain("text-copper");
        expect(mocks.notifyToast).not.toHaveBeenCalled();
        mocks.generateBrief.mockResolvedValueOnce({ status: "started", nextAllowedAt: "" });
        fireEvent.click(within(alert).getByRole("button", { name: "Try again" }));
        expect(await screen.findByText("Writing a fresh brief. It reaches your phone when it's ready.")).toBeInTheDocument();
    });
});
