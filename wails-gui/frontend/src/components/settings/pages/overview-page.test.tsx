import i18n from "@/i18n";
import { Children, isValidElement, type ReactElement, type ReactNode } from "react";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { MobileBriefSchedule, SettingsOverviewView } from "@/lib/wails-shim-app";
import type { PermissionItem, PlatformPermissions } from "@/types";
import { pageQueryCache } from "@/lib/query-cache";
import { resetPageQueries } from "@/lib/page-queries";
import { keepOverviewRead, keepRead, OverviewPage } from "./overview-page";

vi.mock("./existing-text-channels", () => ({ ExistingTextChannels: () => null }));

const mocks = vi.hoisted(() => ({
    handlers: new Map<string, Set<(...args: unknown[]) => void>>(),
    overview: vi.fn(),
    permissions: vi.fn(),
    listening: vi.fn(),
    meetings: vi.fn(),
    uiState: vi.fn(),
    schedule: vi.fn(),
    pauseFor: vi.fn(),
    resume: vi.fn(),
    setQuiet: vi.fn(),
    setLevel: vi.fn(),
    setRetention: vi.fn(),
    dismiss: vi.fn(),
    emit: vi.fn(),
    toast: { error: vi.fn(), success: vi.fn() },
}));

vi.mock("@/lib/wails-shim-app", () => ({
    GetSettingsOverview: mocks.overview,
    GetPlatformPermissions: mocks.permissions,
    GetBackgroundListening: mocks.listening,
    GetAutomaticMeetingRecordingEnabled: mocks.meetings,
    LoadUIState: mocks.uiState,
    GetMobileBriefSchedule: mocks.schedule,
    PauseObservationFor: mocks.pauseFor,
    ResumeObservation: mocks.resume,
    SetEngagementKindQuiet: mocks.setQuiet,
    SetEngagementLevel: mocks.setLevel,
    SetActivityRetentionDays: mocks.setRetention,
    DismissOverviewIssue: mocks.dismiss,
    GetEngagementSettings: vi.fn(() => Promise.resolve({ available: false, kinds: [] })),
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

vi.mock("sonner", () => ({ toast: mocks.toast }));

// Radix Select needs pointer APIs jsdom lacks; a native <select> keeps the
// same value / onValueChange contract and the trigger's accessible name.
vi.mock("@/components/ui/select", () => {
    const SelectTrigger = (_: { "aria-label"?: string; className?: string; children?: ReactNode }) => null;
    const SelectValue = () => null;
    const SelectContent = ({ children }: { children?: ReactNode }) => <>{children}</>;
    const SelectItem = ({ value, children }: { value: string; disabled?: boolean; children?: ReactNode }) => (
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
const NOW = new Date("2026-10-22T15:00:00Z").getTime();

function permission(key: string, overrides: Partial<PermissionItem> = {}): PermissionItem {
    return {
        key,
        label: key,
        description: "",
        granted: true,
        required: false,
        actionable: true,
        actionLabel: "Grant",
        toggleable: true,
        enabled: true,
        ...overrides,
    };
}

function macPermissions(overrides: Record<string, Partial<PermissionItem>> = {}): PlatformPermissions {
    return {
        platform: "darwin",
        supported: true,
        items: [
            permission("screen_recording", overrides.screen_recording),
            permission("accessibility", overrides.accessibility),
            permission("microphone", overrides.microphone),
            permission("clipboard", overrides.clipboard),
        ],
    };
}

function makeOverview(overrides: Partial<SettingsOverviewView> = {}): SettingsOverviewView {
    return {
        unavailable: [],
        sees: { watching: true, pausedUntil: null, ignoredAppsCount: 0, passwordManagersHidden: true },
        speaksUp: { level: "normal", dailyCap: 6, quietStart: "22:00", quietEnd: "08:00", channels: ["desktop", "push"] },
        thinksWith: {
            order: [
                { assistant: "claude", percentLeft: 95.4, windowLabel: "5 hours", resetsAt: "2026-10-22T18:00:00Z" },
                { assistant: "codex", percentLeft: 40, windowLabel: "7 days", resetsAt: "2026-10-25T18:00:00Z" },
            ],
        },
        remembers: { awaitingConfirmCount: 3 },
        keeps: { retention: 0, limitBytes: 10 * GIB, usedBytes: 2.4 * GIB },
        briefs: {
            morningAt: "07:00",
            eveningAt: "18:00",
            adaptive: true,
            phone: { name: "iPhone 17", lastSeenAt: "2026-10-22T14:59:00Z" },
            connection: { enabled: true, relayState: "connected", relayLastSeenAt: "", relayUrl: "https://relay.example.com", warningCode: "" } as NonNullable<SettingsOverviewView["briefs"]["connection"]>,
        },
        ignores: { apps: [] },
        worthALook: [],
        ...overrides,
    };
}

function makeSchedule(overrides: Partial<MobileBriefSchedule> = {}): MobileBriefSchedule {
    return {
        enabled: true,
        morning: "07:00",
        evening: "18:00",
        adaptiveTiming: true,
        lastGeneratedAt: "",
        lastSlot: "",
        narrativeAvailable: true,
        feedEnabled: false,
        feedUrl: "",
        ...overrides,
    };
}

const ISSUES: SettingsOverviewView["worthALook"] = [
    { id: "assistant_out_of_usage:codex", kind: "assistant_out_of_usage", params: { assistant: "codex", resetsAt: "2026-10-23T07:00:00Z" }, signature: "codex|x" },
    { id: "phone_same_network_only", kind: "phone_same_network_only", params: { host: "192.168.1.4", remoteAccessOff: "saved_relay" }, signature: "192.168.1.4" },
    { id: "noisy_kind:capture_paused", kind: "noisy_kind", params: { kind: "capture_paused", sent: 36 }, signature: "capture_paused|20" },
];

function row(label: string): HTMLElement {
    const labelNode = screen.getByText(label, { selector: "span" });
    return labelNode.parentElement as HTMLElement;
}

async function renderPage(onOpenPage = vi.fn()) {
    const result = render(<OverviewPage onOpenPage={onOpenPage} />);
    await screen.findByText("Sees");
    await waitFor(() => expect(within(row("Briefs")).queryByText(/Your brief/)).not.toBeNull());
    return { ...result, onOpenPage };
}

describe("OverviewPage", () => {
    beforeEach(async () => {
        vi.useFakeTimers({ toFake: ["Date"] });
        vi.setSystemTime(NOW);
        await i18n.changeLanguage("en");
        // The page reads through the shared query cache; each test starts cold.
        resetPageQueries();
        mocks.handlers.clear();
        for (const fn of [mocks.overview, mocks.permissions, mocks.listening, mocks.meetings, mocks.uiState, mocks.schedule, mocks.pauseFor, mocks.resume, mocks.setQuiet, mocks.setLevel, mocks.setRetention, mocks.dismiss, mocks.toast.error]) {
            fn.mockReset();
        }
        mocks.overview.mockResolvedValue(makeOverview());
        mocks.permissions.mockResolvedValue(macPermissions());
        mocks.listening.mockResolvedValue(true);
        mocks.meetings.mockResolvedValue(true);
        mocks.uiState.mockResolvedValue("");
        mocks.schedule.mockResolvedValue(makeSchedule());
        mocks.pauseFor.mockResolvedValue(undefined);
        mocks.resume.mockResolvedValue(undefined);
        mocks.setLevel.mockResolvedValue({ available: true, kinds: [] });
        mocks.setQuiet.mockResolvedValue({ available: true, kinds: [] });
        mocks.setRetention.mockResolvedValue(undefined);
        mocks.dismiss.mockResolvedValue(undefined);
    });

    afterEach(() => {
        vi.useRealTimers();
    });

    it('does not promise channels that are on the way (ST14)', async () => {
        await renderPage();
        expect(screen.queryByText(/on the way/)).toBeNull();
        expect(screen.queryByRole('button', { name: /Text messages|Phone calls/ })).not.toBeInTheDocument();
    });

    it("writes one sentence per area from the real settings", async () => {
        await renderPage();
        expect(row("Sees")).toHaveTextContent(
            "Isoform is watching your screen, your microphone and your calls. Private browser windows and ignored apps stay out.",
        );
        expect(within(row("Sees")).getByRole("button", { name: "Pause for 1 hour" })).toBeInTheDocument();
        expect(screen.getByRole("combobox", { name: "How much Isoform speaks up" })).toHaveValue("normal");
        expect(row("Speaks up")).toHaveTextContent(
            /^Speaks upIt speaks up.*— up to 6 times a day, quiet 10:00 PM to 8:00 AM, and can reach you on this computer and your phone\.Notifications$/,
        );
        expect(row("Thinks with")).toHaveTextContent(
            "Claude Code answers first, then Codex. Claude Code has 95% of its 5-hour limit left.",
        );
        expect(row("Remembers")).toHaveTextContent(
            "It learns your activity, things about you, reusable know-how and playbooks. Nothing starts work on its own. 3 things it learned are waiting for you to confirm.",
        );
        expect(within(row("Remembers")).getByRole("button", { name: "Review" })).toBeInTheDocument();
        expect(screen.getByRole("combobox", { name: "Keep recordings for" })).toHaveValue("0");
        expect(row("Keeps")).toHaveTextContent(", up to 10 GB in all, 2.4 GB used.");
        expect(within(row("Keeps")).getByRole("progressbar", { name: "2.4 GB of 10 GB used" })).toHaveAttribute("aria-valuenow", "24");
        expect(row("Briefs")).toHaveTextContent(
            /Your brief comes around 7:00\sAM and 6:00\sPM, timed to when you start and finish\. Your phone can reach this computer wherever you are\./,
        );
        expect(row("Ignores")).toHaveTextContent("No apps are ignored yet. Password managers are always hidden.");
    });

    it.each(["en", "zh-CN"])("uses the Phone connection status for a paired phone in %s (ST81)", async (language) => {
        await i18n.changeLanguage(language);
        try {
            const view = makeOverview();
            view.briefs.connection = { ...view.briefs.connection!, relayState: "offline" };
            mocks.overview.mockResolvedValue(view);
            render(<OverviewPage onOpenPage={vi.fn()} />);
            await waitFor(() => expect(document.body).toHaveTextContent(i18n.t("settings:mobileConnection.statusOfflineDescription")));
            expect(document.body).not.toHaveTextContent(/It reaches iPhone 17/);
        } finally {
            await i18n.changeLanguage("en");
        }
    });

    it("names only the sources and learning switches that are on", async () => {
        mocks.permissions.mockResolvedValue(macPermissions({ microphone: { enabled: false } }));
        mocks.meetings.mockResolvedValue(false);
        mocks.uiState.mockImplementation((key: string) =>
            Promise.resolve(key === "triage_enable_memory" || key === "triage_enable_sop" ? "false" : ""),
        );
        mocks.overview.mockResolvedValue(
            makeOverview({
                remembers: { awaitingConfirmCount: 0 },
                ignores: { apps: ["Slack", "Notes", "Signal", "WeChat", "Mail"].map((name) => ({ name, bundleId: name })) },
            }),
        );
        await renderPage();
        expect(row("Sees")).toHaveTextContent("Isoform is watching your screen. Private browser windows");
        expect(row("Remembers")).toHaveTextContent(
            "It learns your activity and reusable know-how. Nothing starts work on its own. Nothing it learned is waiting for you to confirm.",
        );
        expect(within(row("Remembers")).getByRole("button", { name: "Learning and memory" })).toBeInTheDocument();
        expect(row("Ignores")).toHaveTextContent("Isoform ignores Slack, Notes, Signal and 2 more. Password managers are always hidden.");
    });

    it("counts the screen only when it's on and allowed, with Allow to Observation (ST14)", async () => {
        mocks.permissions.mockResolvedValue(macPermissions({ screen_recording: { granted: false } }));
        mocks.listening.mockResolvedValue(false);
        const { onOpenPage } = await renderPage();
        expect(row("Sees")).toHaveTextContent("Isoform can't see your screen until your computer allows it.");
        expect(row("Sees")).not.toHaveTextContent("watching your screen");
        fireEvent.click(within(row("Sees")).getByRole("button", { name: "Allow" }));
        expect(onOpenPage).toHaveBeenCalledWith("activity", { anchor: "sees-screen" });
    });

    it("says so when nothing is watched, the brief is off and the level has no limits", async () => {
        mocks.permissions.mockResolvedValue(macPermissions({ screen_recording: { enabled: false } }));
        mocks.listening.mockResolvedValue(false);
        mocks.schedule.mockResolvedValue(makeSchedule({ enabled: false }));
        mocks.overview.mockResolvedValue(
            makeOverview({ speaksUp: { level: "custom", dailyCap: 0, quietStart: "00:00", quietEnd: "00:00", channels: ["desktop"] } }),
        );
        const { onOpenPage } = await renderPage();
        expect(row("Sees")).toHaveTextContent("Isoform is not watching or listening: Watch my screen and Listen are off.");
        fireEvent.click(within(row("Sees")).getByRole("button", { name: "Observation" }));
        expect(onOpenPage).toHaveBeenCalledWith("activity");
        expect(row("Speaks up")).toHaveTextContent("— with no daily limit, with no quiet hours, and can reach you on this computer.");
        expect(screen.getByRole("combobox", { name: "How much Isoform speaks up" })).toHaveValue("custom");
        expect(row("Briefs")).toHaveTextContent("Your brief is turned off.");
    });

    it("pauses for one hour and offers Resume while paused", async () => {
        await renderPage();
        fireEvent.click(within(row("Sees")).getByRole("button", { name: "Pause for 1 hour" }));
        await waitFor(() => expect(mocks.pauseFor).toHaveBeenCalledWith(60));

        mocks.overview.mockResolvedValue(
            makeOverview({ sees: { watching: false, pausedUntil: "2026-10-22T16:00:00Z", ignoredAppsCount: 0, passwordManagersHidden: true } }),
        );
        act(() => {
            for (const handler of mocks.handlers.get("closed-eyes:changed") ?? []) handler(true);
        });
        const until = new Intl.DateTimeFormat("en", { hour: "numeric", minute: "2-digit" }).format(Date.parse("2026-10-22T16:00:00Z"));
        await waitFor(() => expect(row("Sees")).toHaveTextContent(`Isoform is paused until ${until}, not watching or listening.`));
        fireEvent.click(within(row("Sees")).getByRole("button", { name: "Resume observing" }));
        await waitFor(() => expect(mocks.resume).toHaveBeenCalledTimes(1));
    });

    it("changes the level inline and reads the sentence again", async () => {
        await renderPage();
        const reads = mocks.overview.mock.calls.length;
        mocks.overview.mockResolvedValue(
            makeOverview({ speaksUp: { level: "more", dailyCap: 10, quietStart: "23:00", quietEnd: "07:00", channels: ["desktop", "push"] } }),
        );
        fireEvent.change(screen.getByRole("combobox", { name: "How much Isoform speaks up" }), { target: { value: "more" } });
        await waitFor(() => expect(mocks.setLevel).toHaveBeenCalledWith("more"));
        await waitFor(() => expect(row("Speaks up")).toHaveTextContent("up to 10 times a day, quiet 11:00 PM to 7:00 AM"));
        expect(mocks.overview.mock.calls.length).toBeGreaterThan(reads);
    });

    it("keeps the old value and says so when a change fails", async () => {
        mocks.setLevel.mockRejectedValue(new Error("boom"));
        await renderPage();
        fireEvent.change(screen.getByRole("combobox", { name: "How much Isoform speaks up" }), { target: { value: "off" } });
        expect(await screen.findByText("Couldn't change this.")).toBeInTheDocument();
        expect(mocks.toast.error).not.toHaveBeenCalled();
        expect(screen.getByRole("combobox", { name: "How much Isoform speaks up" })).toHaveValue("normal");
    });

    it("asks before a shorter recording limit deletes recordings", async () => {
        mocks.overview.mockResolvedValue(makeOverview({ keeps: { retention: 30, limitBytes: 10 * GIB, usedBytes: GIB } }));
        await renderPage();
        fireEvent.change(screen.getByRole("combobox", { name: "Keep recordings for" }), { target: { value: "7" } });
        expect(mocks.setRetention).not.toHaveBeenCalled();
        fireEvent.click(await screen.findByRole("button", { name: "Delete older recordings" }));
        await waitFor(() => expect(mocks.setRetention).toHaveBeenCalledWith(7));

        // A longer limit deletes nothing and saves at once.
        fireEvent.change(screen.getByRole("combobox", { name: "Keep recordings for" }), { target: { value: "90" } });
        await waitFor(() => expect(mocks.setRetention).toHaveBeenCalledWith(90));
    });

    it("asks before a shorter limit even when the recordings take no space", async () => {
        mocks.overview.mockResolvedValue(makeOverview({ keeps: { retention: 0, limitBytes: 10 * GIB, usedBytes: 0 } }));
        await renderPage();
        fireEvent.change(screen.getByRole("combobox", { name: "Keep recordings for" }), { target: { value: "30" } });
        expect(await screen.findByRole("button", { name: "Delete older recordings" })).toBeInTheDocument();
        expect(mocks.setRetention).not.toHaveBeenCalled();
    });

    it("shows the chosen level while it saves", async () => {
        let resolve!: () => void;
        mocks.setLevel.mockImplementation(() => new Promise<unknown>((done) => { resolve = () => done({ available: true, kinds: [] }); }));
        await renderPage();
        const select = screen.getByRole("combobox", { name: "How much Isoform speaks up" });
        fireEvent.change(select, { target: { value: "fewer" } });
        await waitFor(() => expect(select).toBeDisabled());
        expect(select).toHaveValue("fewer");
        mocks.overview.mockResolvedValue(
            makeOverview({ speaksUp: { level: "fewer", dailyCap: 3, quietStart: "21:00", quietEnd: "09:00", channels: ["desktop"] } }),
        );
        resolve();
        await waitFor(() => expect(select).not.toBeDisabled());
        expect(select).toHaveValue("fewer");
    });

    it("names the day of a pause that ends on another day", async () => {
        mocks.overview.mockResolvedValue(
            makeOverview({ sees: { watching: false, pausedUntil: "2026-10-23T14:00:00Z", ignoredAppsCount: 0, passwordManagersHidden: true } }),
        );
        await renderPage();
        const until = new Intl.DateTimeFormat("en", { weekday: "short", hour: "numeric", minute: "2-digit" }).format(
            Date.parse("2026-10-23T14:00:00Z"),
        );
        expect(row("Sees")).toHaveTextContent(`Isoform is paused until ${until}, not watching or listening.`);
    });

    it("hides Worth a look when nothing needs attention", async () => {
        await renderPage();
        expect(screen.queryByText(/Worth a look/)).toBeNull();
    });

    it("lists up to three fixes, each with its fix and Leave it", async () => {
        mocks.overview.mockResolvedValue(makeOverview({ worthALook: ISSUES }));
        const { onOpenPage } = await renderPage();
        expect(screen.getByRole("heading", { name: "Worth a look" })).toBeInTheDocument();
        const noisy = document.querySelector('[data-overview-issue="noisy_kind"]') as HTMLElement;
        expect(noisy).toHaveTextContent("“Observing paused” was sent 36 times in 30 days, and you acted on none");
        expect(noisy).toHaveTextContent("Show it as a quiet line on Today instead of a notification.");
        const assistant = document.querySelector('[data-overview-issue="assistant_out_of_usage"]') as HTMLElement;
        expect(assistant).toHaveTextContent("Codex has used all of its weekly limit");
        expect(assistant).toHaveTextContent("It resets in 16 hours. Claude Code keeps answering until then.");

        fireEvent.click(within(noisy).getByRole("button", { name: "Make it quiet" }));
        await waitFor(() => expect(mocks.setQuiet).toHaveBeenCalledWith("capture_paused", true));
        // The issue no longer qualifies once quiet; its result stays on screen.
        mocks.overview.mockResolvedValue(makeOverview({ worthALook: ISSUES.slice(0, 2) }));
        await waitFor(() => expect(noisy).toHaveTextContent("Moved to a quiet line on Today."));
        await waitFor(() => expect(screen.getByRole("heading", { name: "Worth a look" })).toBeInTheDocument());

        const phone = document.querySelector('[data-overview-issue="phone_same_network_only"]') as HTMLElement;
        expect(phone).toHaveTextContent(
            "Remote access is off, so your phone can only reach this computer on the same network",
        );
        expect(phone).toHaveTextContent(
            "Open Phone settings and choose Use remote access.",
        );
        expect(phone).not.toHaveTextContent("192.168.1.4"
        );
        fireEvent.click(within(phone).getByRole("button", { name: "Fix the connection" }));
        expect(onOpenPage).toHaveBeenCalledWith("iphone");

        fireEvent.click(within(assistant).getByRole("button", { name: "Leave it" }));
        await waitFor(() => expect(mocks.dismiss).toHaveBeenCalledWith("assistant_out_of_usage:codex"));
        await waitFor(() => expect(assistant).toHaveTextContent("Left as it is."));
    });

    it("shows an unavailable remote-access issue and opens Phone settings", async () => {
        mocks.overview.mockResolvedValue(makeOverview({
            worthALook: [{
                id: "phone_remote_access_unavailable",
                kind: "phone_remote_access_unavailable",
                params: {},
                signature: "failed",
            }],
        }));
        const { onOpenPage } = await renderPage();
        const issue = document.querySelector('[data-overview-issue="phone_remote_access_unavailable"]') as HTMLElement;
        expect(issue).toHaveTextContent("Remote access is not running");
        expect(issue).toHaveTextContent(
            "Your phone cannot reach this computer from other networks until it starts again. Isoform keeps retrying.",
        );
        fireEvent.click(within(issue).getByRole("button", { name: "Open Phone settings" }));
        expect(onOpenPage).toHaveBeenCalledWith("iphone");
        expect(issue).toHaveTextContent("Opened Phone settings.");
    });

    it("opens an issue again when it comes back with a new signature", async () => {
        const noisy = ISSUES[2];
        mocks.overview.mockResolvedValue(makeOverview({ worthALook: [noisy] }));
        await renderPage();
        fireEvent.click(screen.getByRole("button", { name: "Leave it" }));
        await waitFor(() => expect(screen.getByText("Left as it is.")).toBeInTheDocument());
        expect(screen.getByRole("heading", { name: "Worth a look" })).toBeInTheDocument();

        // The count doubled: the hub returns the row with a new signature.
        mocks.overview.mockResolvedValue(
            makeOverview({ worthALook: [{ ...noisy, params: { kind: "capture_paused", sent: 41 }, signature: "capture_paused|40" }] }),
        );
        pageQueryCache.invalidate(["GetSettingsOverview"]);
        fireEvent(window, new Event("focus"));
        await waitFor(() => expect(screen.getByRole("button", { name: "Make it quiet" })).toBeInTheDocument());
        expect(screen.queryByText("Left as it is.")).toBeNull();
        expect(screen.getByRole("heading", { name: "Worth a look" })).toBeInTheDocument();
        expect(screen.getByText("“Observing paused” was sent 41 times in 30 days, and you acted on none")).toBeInTheDocument();
    });

    it("never says another assistant keeps answering when its usage is unknown", async () => {
        mocks.overview.mockResolvedValue(
            makeOverview({
                thinksWith: {
                    order: [
                        { assistant: "claude", percentLeft: 0, windowLabel: "5 hours", resetsAt: "2026-10-22T18:00:00Z" },
                        { assistant: "codex", percentLeft: null, windowLabel: "", resetsAt: "" },
                    ],
                },
                worthALook: [
                    { id: "assistant_out_of_usage:claude", kind: "assistant_out_of_usage", params: { assistant: "claude", resetsAt: "2026-10-22T18:00:00Z" }, signature: "claude|x" },
                ],
            }),
        );
        await renderPage();
        const issue = document.querySelector('[data-overview-issue="assistant_out_of_usage"]') as HTMLElement;
        expect(issue).toHaveTextContent("Claude Code has used all of its 5-hour limit");
        expect(issue).toHaveTextContent("It resets in 3 hours.");
        expect(issue).not.toHaveTextContent(/keeps? answering/);
    });

    it("drops an issue fixed elsewhere without calling Leave it a failure", async () => {
        mocks.overview.mockResolvedValue(makeOverview({ worthALook: ISSUES.slice(1, 2) }));
        mocks.dismiss.mockRejectedValue(new Error("overview_issue_not_current"));
        await renderPage();
        mocks.overview.mockResolvedValue(makeOverview({ worthALook: [] }));
        fireEvent.click(screen.getByRole("button", { name: "Leave it" }));
        await waitFor(() => expect(screen.queryByText(/Worth a look/)).toBeNull());
        expect(mocks.toast.error).not.toHaveBeenCalled();
    });

    it("keeps an issue open when Leave it fails", async () => {
        mocks.overview.mockResolvedValue(makeOverview({ worthALook: ISSUES.slice(1, 2) }));
        mocks.dismiss.mockRejectedValue(new Error("overview_store_unavailable"));
        await renderPage();
        fireEvent.click(screen.getByRole("button", { name: "Leave it" }));
        expect(await screen.findByText("Couldn't change this.")).toBeInTheDocument();
        expect(mocks.toast.error).not.toHaveBeenCalled();
        expect(screen.getByRole("button", { name: "Fix the connection" })).toBeInTheDocument();
    });

    // ST87: the Overview never shows a read error. A line whose read never
    // succeeded is left out; every other line still shows.
    it("leaves out a line that was never read and shows no read error", async () => {
        mocks.overview.mockResolvedValue(makeOverview({ unavailable: ["thinksWith"] }));
        mocks.schedule.mockRejectedValue(new Error("brief unavailable"));
        render(<OverviewPage />);
        await screen.findByText("Sees");
        await act(async () => {});
        expect(screen.queryByText("Thinks with", { selector: "span" })).toBeNull();
        expect(screen.queryByText("Briefs", { selector: "span" })).toBeNull();
        expect(screen.queryByText("Couldn't read this setting.")).toBeNull();
        expect(row("Sees")).toHaveTextContent("Isoform is watching your screen");
        expect(row("Speaks up")).toHaveTextContent("up to 6 times a day");
        expect(row("Remembers")).toHaveTextContent("3 things it learned");
        expect(row("Keeps")).toHaveTextContent("2.4 GB used");
    });

    it("shows no read error and leaves out every line when the overview cannot be read", async () => {
        mocks.overview.mockRejectedValue(new Error("down"));
        render(<OverviewPage />);
        await act(async () => {});
        await waitFor(() => expect(mocks.overview).toHaveBeenCalled());
        expect(screen.queryByText("Couldn't read this setting.")).toBeNull();
        for (const label of ["Speaks up", "Thinks with", "Remembers", "Keeps", "Briefs", "Ignores"]) {
            expect(screen.queryByText(label, { selector: "span" })).toBeNull();
        }
        expect(screen.queryByText(/Worth a look/)).toBeNull();
    });

    it("keeps the last known Thinks with line when a later read cannot read it", async () => {
        mocks.overview.mockResolvedValueOnce(makeOverview());
        const { unmount } = await renderPage();
        expect(row("Thinks with")).toHaveTextContent("Assistant");
        mocks.overview.mockResolvedValue(makeOverview({ unavailable: ["thinksWith"] }));
        const before = mocks.overview.mock.calls.length;
        pageQueryCache.invalidate(["GetSettingsOverview"]);
        fireEvent(window, new Event("focus"));
        await waitFor(() => expect(mocks.overview.mock.calls.length).toBeGreaterThan(before));
        await act(async () => {});
        expect(row("Thinks with")).toHaveTextContent("Assistant");
        expect(screen.queryByText("Couldn't read this setting.")).toBeNull();
        unmount();
    });

    it("shows the busy state while the overview is read", async () => {
        mocks.overview.mockReturnValue(new Promise(() => {}));
        render(<OverviewPage />);
        expect(document.querySelector('[aria-busy="true"]')).not.toBeNull();
        expect(screen.queryByText("Checking your setup...")).toBeNull();
    });

    it("shows the aria-busy skeleton on a true first load", async () => {
        render(<OverviewPage />);
        expect(document.querySelector('[aria-busy="true"]')).toBeInTheDocument();
        await act(async () => {});
    });

    it("says the usage window as people say it, never the raw label or the model (ST61)", async () => {
        mocks.overview.mockResolvedValue(makeOverview({
            thinksWith: { order: [{ assistant: "claude", percentLeft: 98, windowLabel: "7 days · Fable", resetsAt: "" }] },
        }));
        await renderPage();
        expect(row("Thinks with")).toHaveTextContent("Claude Code has 98% of its weekly limit left.");
        expect(row("Thinks with")).not.toHaveTextContent(/Fable|7 days/);
        cleanup();
        resetPageQueries();
        await i18n.changeLanguage("zh-CN");
        try {
            mocks.overview.mockResolvedValue(makeOverview({
                thinksWith: { order: [{ assistant: "claude", percentLeft: 98, windowLabel: "1 day · Fable", resetsAt: "" }] },
            }));
            render(<OverviewPage />);
            await screen.findByText("观察");
            await waitFor(() => expect(row("思考")).toHaveTextContent("98%"));
            expect(row("思考")).toHaveTextContent("Claude Code 的每日额度还剩 98%。");
            expect(row("思考")).not.toHaveTextContent(/Fable/);
        } finally {
            await i18n.changeLanguage("en");
        }
    });

    it("paints a cached overview at once, without the skeleton", async () => {
        pageQueryCache.set("GetSettingsOverview", [], makeOverview({
            sees: { watching: true, pausedUntil: null, ignoredAppsCount: 0, passwordManagersHidden: false },
            speaksUp: { level: "normal", dailyCap: 12, quietStart: "", quietEnd: "", channels: ["desktop"] },
            thinksWith: { order: [{ assistant: "claude", percentLeft: 82, windowLabel: "5 hours", resetsAt: "" }] },
            remembers: { awaitingConfirmCount: 0 },
            keeps: { retention: 30, limitBytes: 0, usedBytes: 123456 },
            briefs: { morningAt: "08:00", eveningAt: "17:00", adaptive: false, phone: null, connection: null },
            ignores: { apps: [] },
            worthALook: [],
        }));
        render(<OverviewPage />);
        // Synchronous, before the revalidating reads settle.
        expect(screen.getByText("Thinks with")).toBeInTheDocument();
        expect(document.querySelector('[aria-busy="true"]')).toBeNull();
        expect(screen.queryByText("Checking your setup...")).toBeNull();
        await act(async () => {});
    });

    it("a privacy transition withdraws the sentences and the open issues before the re-read lands", async () => {
        mocks.overview.mockResolvedValue(makeOverview({ worthALook: ISSUES }));
        await renderPage();
        expect(row("Sees")).toHaveTextContent("Isoform is watching your screen, your microphone and your calls.");
        expect(row("Briefs")).toHaveTextContent(/Your brief comes around 7:00/);
        expect(screen.getByRole("heading", { name: "Worth a look" })).toBeInTheDocument();

        // The sweep's re-reads never answer; the withdrawal must not wait on them.
        for (const fn of [mocks.overview, mocks.permissions, mocks.listening, mocks.meetings, mocks.uiState, mocks.schedule]) {
            fn.mockImplementation(() => new Promise(() => {}));
        }
        act(() => {
            for (const handler of mocks.handlers.get("closed-eyes:changed") ?? []) handler(true);
        });
        expect(document.querySelector('[aria-busy="true"]')).not.toBeNull();
        expect(screen.queryByText("Checking your setup...")).toBeNull();
        expect(document.querySelector('[aria-busy="true"]')).toBeInTheDocument();
        expect(screen.queryByText(/Isoform is watching your screen/)).toBeNull();
        expect(screen.queryByText(/Your brief comes/)).toBeNull();
        expect(screen.queryByText(/Worth a look/)).toBeNull();

        // Permitted recovery: the re-reads answer and fresh sentences paint.
        mocks.overview.mockResolvedValue(makeOverview());
        mocks.permissions.mockResolvedValue(macPermissions());
        mocks.listening.mockResolvedValue(true);
        mocks.meetings.mockResolvedValue(true);
        mocks.uiState.mockResolvedValue("");
        mocks.schedule.mockResolvedValue(makeSchedule());
        act(() => {
            for (const handler of mocks.handlers.get("closed-eyes:changed") ?? []) handler(false);
        });
        await waitFor(() => expect(row("Sees")).toHaveTextContent("Isoform is watching your screen, your microphone and your calls."));
        expect(row("Briefs")).toHaveTextContent(/Your brief comes around 7:00/);
        expect(screen.queryByText(/Worth a look/)).toBeNull();
    });

    it("an ordinary invalidation keeps the painted sentences while they re-read", async () => {
        await renderPage();
        expect(row("Sees")).toHaveTextContent("Isoform is watching your screen, your microphone and your calls.");
        for (const fn of [mocks.overview, mocks.permissions, mocks.listening, mocks.meetings, mocks.uiState, mocks.schedule]) {
            fn.mockImplementation(() => new Promise(() => {}));
        }
        act(() => {
            for (const handler of mocks.handlers.get("ai:config-changed") ?? []) handler(undefined);
        });
        expect(row("Sees")).toHaveTextContent("Isoform is watching your screen, your microphone and your calls.");
        expect(row("Briefs")).toHaveTextContent(/Your brief comes/);
        expect(screen.queryByText("Checking your setup...")).toBeNull();
    });

    it("writes the sentences in Chinese", async () => {
        await i18n.changeLanguage("zh-CN");
        render(<OverviewPage />);
        await screen.findByText("观察");
        await waitFor(() => expect(row("简报")).toHaveTextContent("你的简报大约在"));
        expect(row("观察")).toHaveTextContent("Isoform 正在观察你的屏幕、你的麦克风和你的通话。");
        expect(row("提醒")).toHaveTextContent("——每天最多 6 次，22:00 至 8:00 保持安静，目前可以发到这台电脑和你的手机。");
        expect(row("保留")).toHaveTextContent("录制内容存储在这台电脑上，保留");
        expect(row("保留")).toHaveTextContent("，总共最多 10 GB，已使用 2.4 GB。");
        expect(row("思考")).toHaveTextContent("先由 Claude Code 回答，然后是 Codex。Claude Code 的5 小时额度还剩 95%。");
    });
});

describe("Overview keeps the last known reading (ST87)", () => {
    const ready = (value: SettingsOverviewView) => ({ status: "ready" as const, value });

    it("keeps the last known section when the next read names it unavailable", () => {
        const known = makeOverview();
        const next = makeOverview({ unavailable: ["thinksWith"], thinksWith: { order: [] } as unknown as SettingsOverviewView["thinksWith"] });
        const merged = keepOverviewRead(ready(next), ready(known));
        expect(merged.status).toBe("ready");
        if (merged.status !== "ready") return;
        expect(merged.value.thinksWith).toEqual(known.thinksWith);
        expect(merged.value.unavailable).toEqual([]);
    });

    it("leaves a section that was never read unavailable", () => {
        const known = makeOverview({ unavailable: ["thinksWith"] });
        const next = makeOverview({ unavailable: ["thinksWith"] });
        const merged = keepOverviewRead(ready(next), ready(known));
        expect(merged.status === "ready" && merged.value.unavailable).toEqual(["thinksWith"]);
    });

    it("keeps the last known value when a read fails, and stays failed when none was known", () => {
        const known = ready(makeOverview());
        expect(keepRead({ status: "failed" as const }, known)).toBe(known);
        expect(keepRead({ status: "failed" as const }, { status: "loading" as const })).toEqual({ status: "failed" });
        const fresh = ready(makeOverview());
        expect(keepRead(fresh, known)).toBe(fresh);
    });
});
