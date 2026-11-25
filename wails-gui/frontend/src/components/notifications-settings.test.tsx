import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import i18n from "@/i18n";
import type { EngagementKindView, EngagementSettingsView } from "@/lib/wails-shim-app";
import en from "@/i18n/locales/en/settings.json";
import componentSource from "./notifications-settings.tsx?raw";
import {
    NotificationsSettings,
    QuietHoursCard,
    useEngagementSettingsState,
} from "./notifications-settings";

const mocks = vi.hoisted(() => ({
    getSettings: vi.fn(),
    getWeek: vi.fn(),
    setKind: vi.fn(),
    setQuiet: vi.fn(),
    setRules: vi.fn(),
    setLevel: vi.fn(),
    emit: vi.fn(),
    toast: { error: vi.fn(), success: vi.fn() },
}));

vi.mock("@/lib/wails-shim-app", () => ({
    GetEngagementSettings: mocks.getSettings,
    GetEngagementWeekSummary: mocks.getWeek,
    SetEngagementKindEnabled: mocks.setKind,
    SetEngagementKindQuiet: mocks.setQuiet,
    SetEngagementRules: mocks.setRules,
    SetEngagementLevel: mocks.setLevel,
}));

vi.mock("../../wailsjs/runtime/runtime", () => ({
    EventsOn: () => () => {},
    EventsEmit: mocks.emit,
}));

vi.mock("sonner", () => ({ toast: mocks.toast }));

// The 21 active kinds in catalog order: engagement.AllKinds() in
// wails-gui/internal/engagement/moment.go without the two silenced meeting
// kinds (engagementSilentKinds), as GetEngagementSettings returns them.
const KINDS = [
    "first_week",
    "weekly_recap",
    "work_blocked",
    "decision_due",
    "work_finished",
    "playbook_result",
    "reminder",
    "commitment_slipping",
    "meeting_prep",
    "capture_paused",
    "brief_ready",
    "calendar_conflict",
    "waiting_on_reply",
    "brief_missed",
    "schedule_missed",
    "suggestion",
    "topic_finds",
    "playbook_suggested",
    "goal_check_in",
    "person_reappeared",
    "learned_to_confirm",
    "answer_ready",
];
const NEEDS_YOU = new Set(["decision_due", "commitment_slipping", "waiting_on_reply", "work_blocked"]);

type Stats = Partial<EngagementKindView>;

// Receipts like the hub's 30-day stats: interruption counts, where they went
// and the tag the thresholds give (engagement.NotificationStatsTag).
const SENT: Record<string, Stats> = {
    capture_paused: { notificationSent: 36, notificationActed: 0, dismissed: 9, channels: ["computer", "phone"], tag: "noisy" },
    brief_ready: { notificationSent: 13, notificationActed: 0, notificationUseful: 4, channels: ["computer", "phone"], tag: "ignored" },
    meeting_prep: { notificationSent: 6, notificationActed: 0, channels: ["computer"], tag: "ignored" },
    brief_missed: { notificationSent: 2, notificationActed: 0, channels: ["computer", "phone"], tag: "rare" },
    decision_due: { notificationSent: 1, notificationActed: 0, channels: ["computer", "phone"], tag: "needs_you" },
    commitment_slipping: { notificationSent: 1, notificationActed: 0, channels: ["computer"], tag: "needs_you" },
    calendar_conflict: { notificationSent: 1, notificationActed: 1, channels: ["computer"], tag: "rare" },
};

function kindView(kind: string, stats: Stats = {}): EngagementKindView {
    const offByDefault = new Set(["goal_check_in", "person_reappeared", "learned_to_confirm"]);
    const sent = stats.notificationSent ?? 0;
    return {
        kind,
        enabled: !offByDefault.has(kind),
        defaultOn: !offByDefault.has(kind),
        paused: false,
        quietOnToday: false,
        quiet: false,
        deliveryMode: "normal",
        quietAllowed: kind !== "decision_due",
        canQuiet: kind !== "decision_due",
        quietSuggestionAllowed: !NEEDS_YOU.has(kind) && kind !== "reminder",
        sent,
        opened: 0,
        acted: stats.notificationActed ?? 0,
        dismissed: 0,
        muted: 0,
        channels: [],
        notificationSent: 0,
        notificationOpenedOrActed: stats.notificationActed ?? 0,
        notificationActed: 0,
        quietSent: 0,
        quietOpenedOrActed: 0,
        sampleCopyKey: sent > 0 ? kind : "",
        tag: "",
        ...stats,
    };
}

function makeView(
    overrides: Partial<EngagementSettingsView> = {},
    stats: Record<string, Stats> = SENT,
): EngagementSettingsView {
    const kinds = KINDS.map((kind) => kindView(kind, stats[kind]));
    return {
        available: true,
        level: "normal",
        kinds,
        quietStart: "22:00",
        quietEnd: "08:00",
        dailyInterruptBudget: 6,
        digestTimes: ["08:30", "13:00", "18:00"],
        statsDays: 30,
        summary: { sent: 60, useful: 7, openedOrActed: 1, quietSent: 0, quietOpenedOrActed: 0 },
        ...overrides,
    };
}

function withKind(view: EngagementSettingsView, kind: string, change: Stats): EngagementSettingsView {
    return { ...view, kinds: view.kinds.map((entry) => (entry.kind === kind ? { ...entry, ...change } : entry)) };
}

function Page({ onOpenPage }: { onOpenPage?: (page: string, options?: unknown) => void }) {
    const state = useEngagementSettingsState();
    return (
        <>
            <NotificationsSettings state={state} onOpenPage={onOpenPage as never} />
            <QuietHoursCard state={state} />
        </>
    );
}

async function renderReady(view: EngagementSettingsView = makeView(), onOpenPage = vi.fn()) {
    mocks.getSettings.mockResolvedValue(view);
    const result = render(<Page onOpenPage={onOpenPage} />);
    await waitFor(() => {
        expect(screen.queryByText("Checking notifications...")).not.toBeInTheDocument();
    });
    return { ...result, onOpenPage };
}

function rankedRow(kind: string): HTMLElement {
    const row = document.querySelector(`[data-kind="${kind}"]`);
    if (!row) throw new Error(`no ranked row for ${kind}`);
    return row as HTMLElement;
}

function rankedOrder(): string[] {
    return Array.from(document.querySelectorAll("[data-kind]")).map((row) => row.getAttribute("data-kind") ?? "");
}

function openFold() {
    fireEvent.click(screen.getByRole("button", { name: /kinds that sent nothing in 30 days/ }));
}

const devTools = vi.hoisted(() => ({ on: true }));
vi.mock("@/components/settings/settings-dev-tools", async (original) => ({
    ...(await original<typeof import("@/components/settings/settings-dev-tools")>()),
    useSettingsDevTools: () => devTools.on,
}));
beforeEach(() => {
    devTools.on = true;
});

describe("NotificationsSettings", () => {
    beforeEach(async () => {
        vi.clearAllMocks();
        await i18n.changeLanguage("en");
        mocks.getWeek.mockResolvedValue({ available: true, sent: 0, actedOn: 0, days: 7, kinds: [] });
        mocks.setKind.mockRejectedValue(new Error("unexpected save"));
        mocks.setQuiet.mockRejectedValue(new Error("unexpected quiet save"));
        mocks.setRules.mockRejectedValue(new Error("unexpected rules save"));
        mocks.setLevel.mockRejectedValue(new Error("unexpected level save"));
    });

    // ── The dial ──

    it("shows the dial with the current level checked and its real level text", async () => {
        await renderReady(makeView({ level: "normal" }));
        const dial = screen.getByRole("radiogroup", { name: en.proactiveNotifications.level.title });
        expect(within(dial).getAllByRole("radio").map((radio) => radio.textContent)).toEqual(["Off", "Fewer", "Normal", "More"]);
        expect(within(dial).getByRole("radio", { name: "Normal" })).toHaveAttribute("aria-checked", "true");
        expect(
            screen.getByText("The usual mix, with one suggestion a week. Up to 6 interruptions a day, quiet 10:00 PM to 8:00 AM."),
        ).toBeInTheDocument();
        // The explanation of the dial and Custom stays on the page.
        expect(screen.getByText(en.proactiveNotifications.level.description)).toBeInTheDocument();
        expect(screen.queryByText(en.proactiveNotifications.level.custom)).not.toBeInTheDocument();
    });

    it("turns the dial through the binding and shows the returned view", async () => {
        await renderReady(makeView({ level: "normal" }));
        const next = makeView({ level: "more", dailyInterruptBudget: 10 });
        next.kinds = next.kinds.map((kind) => ({ ...kind, enabled: true }));
        mocks.setLevel.mockResolvedValue(next);
        fireEvent.click(screen.getByRole("radio", { name: "More" }));
        await waitFor(() => expect(screen.getByRole("radio", { name: "More" })).toHaveAttribute("aria-checked", "true"));
        expect(mocks.setLevel).toHaveBeenCalledWith("more");
        expect(screen.getByDisplayValue("10")).toBeInTheDocument();
    });

    it("draws the dial with Settings' one raised segmented control", async () => {
        await renderReady(makeView({ level: "fewer" }));
        const dial = screen.getByRole("radiogroup", { name: en.proactiveNotifications.level.title });
        expect(dial).toHaveClass("settings-segment");
        for (const radio of within(dial).getAllByRole("radio")) {
            const selected = radio.textContent === "Fewer";
            expect(radio).toHaveAttribute("data-active", String(selected));
            expect(radio).toHaveAttribute("aria-checked", String(selected));
        }
        // No hand-rolled selected look beside the shared one.
        expect(componentSource).not.toMatch(/--s-shadow-seg|rounded-\[10px\] bg-panel-warm/);
    });

    it("keeps every level's text readable without choosing it", async () => {
        await renderReady(makeView({ level: "normal" }));
        for (const level of ["off", "fewer", "normal", "more"] as const) {
            const option = en.proactiveNotifications.level.options[level];
            expect(screen.getByRole("radio", { name: option.label })).toHaveAttribute("title", option.description);
        }
    });

    it("does not call the binding for the level already selected", async () => {
        await renderReady(makeView({ level: "normal" }));
        fireEvent.click(screen.getByRole("radio", { name: "Normal" }));
        expect(mocks.setLevel).not.toHaveBeenCalled();
    });

    it("marks a hand-tuned view as custom, and keeps the level and says so when a change fails", async () => {
        await renderReady(makeView({ level: "custom" }));
        expect(screen.getByText(en.proactiveNotifications.level.custom)).toBeInTheDocument();
        // ST72: one line under the dial, not a second hint saying the same.
        expect(screen.getByText("Pick a level, then fine-tune any kind below.")).toBeInTheDocument();
        expect(screen.queryByText(en.proactiveNotifications.level.customHint)).not.toBeInTheDocument();
        for (const radio of screen.getAllByRole("radio")) expect(radio).toHaveAttribute("aria-checked", "false");
        fireEvent.click(screen.getByRole("radio", { name: "Off" }));
        await waitFor(() => expect(mocks.toast.error).toHaveBeenCalledWith(en.proactiveNotifications.level.saveFailed));
        expect(screen.getByText(en.proactiveNotifications.level.custom)).toBeInTheDocument();
    });

    // ── The last 30 days ──

    it("ranks the kinds that sent something by interruptions, with the real push line, counts, where and tag", async () => {
        await renderReady();
        expect(screen.getByRole("heading", { name: "The last 30 days" })).toBeInTheDocument();
        expect(
            screen.getByText(
                "60 sent · 7 useful",
            ),
        ).toBeInTheDocument();
        expect(rankedOrder()).toEqual([
            "capture_paused",
            "brief_ready",
            "meeting_prep",
            "brief_missed",
            "decision_due",
            "commitment_slipping",
            "calendar_conflict",
        ]);
        const capture = rankedRow("capture_paused");
        expect(within(capture).getByText("Observing paused")).toBeInTheDocument();
        expect(within(capture).getByText("Noisy")).toBeInTheDocument();
        expect(within(capture).getByText("“Isoform stopped watching for a while.”")).toBeInTheDocument();
        expect(within(capture).getByText("36 sent · 0 useful · 9 closed")).toBeInTheDocument();
        expect(within(capture).getByRole("img", { name: "36 sent, 0 useful" })).toBeInTheDocument();
        expect(within(capture).getByText("Computer and phone")).toBeInTheDocument();
        // What the kind is stays on the row.
        expect(within(capture).getByText(en.proactiveNotifications.kinds.capture_paused.description)).toBeInTheDocument();

        expect(within(rankedRow("brief_ready")).getByText("Ignored")).toBeInTheDocument();
        expect(within(rankedRow("brief_ready")).getByText("13 sent · 4 useful")).toBeInTheDocument();
        expect(within(rankedRow("brief_ready")).getByRole("img", { name: "13 sent, 4 useful" })).toBeInTheDocument();
        expect(within(rankedRow("decision_due")).getByText("Needs you")).toBeInTheDocument();
        expect(within(rankedRow("decision_due")).getByText("“Work needs your permission.”")).toBeInTheDocument();
        expect(within(rankedRow("meeting_prep")).getByText("Computer")).toBeInTheDocument();
        expect(within(rankedRow("calendar_conflict")).getByText("Rare")).toBeInTheDocument();
        expect(within(rankedRow("calendar_conflict")).getByText("1 sent · 1 useful")).toBeInTheDocument();
    });

    it("keeps an explicit zero useful count instead of falling back to acted counts", async () => {
        const view = withKind(makeView({ summary: { sent: 60, useful: 0, openedOrActed: 12 } }), "calendar_conflict", { notificationUseful: 0 });
        await renderReady(view);
        expect(screen.getByText("60 sent · 0 useful")).toBeInTheDocument();
        expect(within(rankedRow("calendar_conflict")).getByText("1 sent · 0 useful")).toBeInTheDocument();
    });

    it("sums per-kind useful counts when an older response has no useful summary", async () => {
        await renderReady(makeView({ summary: undefined }));
        expect(screen.getByText("60 sent · 5 useful")).toBeInTheDocument();
    });

    it("shows zero useful for an empty stats window", async () => {
        await renderReady(makeView({ summary: { sent: 0, useful: 0, openedOrActed: 0 } }, {}));
        expect(screen.getByText("0 sent · 0 useful")).toBeInTheDocument();
        expect(rankedOrder()).toEqual([]);
    });

    it("localizes aggregate and per-kind useful statistics in Chinese", async () => {
        await i18n.changeLanguage("zh-CN");
        await renderReady();
        expect(await screen.findByText("发送 60 条 · 有用 7 条")).toBeInTheDocument();
        expect(within(rankedRow("brief_ready")).getByText("发送 13 · 有用 4")).toBeInTheDocument();
    });

    it("uses the Activity-thread push line for an unfinished-thread notice", async () => {
        await renderReady(withKind(makeView(), "decision_due", { sampleCopyKey: "decision_due.activity_thread" }));
        expect(within(rankedRow("decision_due")).getByText("“An open thread has a next step.”")).toBeInTheDocument();
    });

    it("folds the kinds with no sends into one line, each keeping its switch", async () => {
        await renderReady();
        const folded = KINDS.length - Object.keys(SENT).length;
        const fold = screen.getByRole("button", { name: `${folded} kinds that sent nothing in 30 days` });
        expect(fold).toHaveAttribute("aria-expanded", "false");
        expect(screen.getAllByRole("switch")).toHaveLength(Object.keys(SENT).length);
        openFold();
        expect(screen.getAllByRole("switch")).toHaveLength(KINDS.length);
        expect(screen.getByRole("switch", { name: "Needs you" })).toBeChecked();
        expect(screen.getByRole("switch", { name: "Goal check-ins" })).not.toBeChecked();
        expect(screen.getByText(en.proactiveNotifications.kinds.goal_check_in.description)).toBeInTheDocument();
    });

    it("lists every kind in the list when each one sent something", async () => {
        const stats = Object.fromEntries(KINDS.map((kind) => [kind, { notificationSent: 1, channels: ["computer"], tag: "rare" } as Stats]));
        await renderReady(makeView({}, stats));
        expect(rankedOrder()).toEqual(KINDS);
        expect(screen.queryByRole("button", { name: /more kinds?, nothing sent/ })).toBeNull();
    });

    it("drops the answer kind, which only Chat sends, while Chat is off (scope cut A5)", async () => {
        (globalThis as Record<string, unknown>).__isoformFeatureSwitchesForTest = { automations: true, playbooks: true, proactiveVoice: true, calendarHolds: true, chat: false };
        const stats = Object.fromEntries(KINDS.map((kind) => [kind, { notificationSent: 1, channels: ["computer"], tag: "rare" } as Stats]));
        await renderReady(makeView({}, stats));
        expect(rankedOrder()).toEqual(KINDS.filter((kind) => kind !== "answer_ready"));
        expect(screen.queryByText(en.proactiveNotifications.kinds.answer_ready.label)).toBeNull();
    });

    it("turns a kind on from the fold and shows the returned state", async () => {
        await renderReady();
        openFold();
        mocks.setKind.mockResolvedValueOnce(withKind(makeView(), "goal_check_in", { enabled: true }));
        fireEvent.click(screen.getByRole("switch", { name: "Goal check-ins" }));
        await waitFor(() => expect(screen.getByRole("switch", { name: "Goal check-ins" })).toBeChecked());
        expect(mocks.setKind).toHaveBeenCalledWith("goal_check_in", true);
    });

    it("shows the answer-ready switch and updates it through the binding", async () => {
        await renderReady();
        openFold();
        expect(screen.getByText(en.proactiveNotifications.kinds.answer_ready.label)).toBeInTheDocument();
        expect(screen.getByText(en.proactiveNotifications.kinds.answer_ready.description)).toBeInTheDocument();
        mocks.setKind.mockResolvedValueOnce(withKind(makeView(), "answer_ready", { enabled: false }));
        fireEvent.click(screen.getByRole("switch", { name: en.proactiveNotifications.kinds.answer_ready.label }));
        await waitFor(() => expect(screen.getByRole("switch", { name: en.proactiveNotifications.kinds.answer_ready.label })).not.toBeChecked());
        expect(mocks.setKind).toHaveBeenCalledWith("answer_ready", false);
    });

    it("keeps the old state and says so when a switch does not save", async () => {
        await renderReady();
        fireEvent.click(screen.getByRole("switch", { name: "Brief ready" }));
        await waitFor(() => expect(mocks.toast.error).toHaveBeenCalledWith("Could not change this setting. Try again."));
        expect(screen.getByRole("switch", { name: "Brief ready" })).toBeChecked();
    });

    it("offers Show on Today instead only for noisy or ignored kinds Isoform may suggest quieting", async () => {
        // Needs-you kinds stay loud even when nobody acts on them.
        let view = withKind(makeView(), "commitment_slipping", { notificationSent: 25, tag: "noisy" });
        view = withKind(view, "decision_due", { notificationSent: 22, tag: "needs_you" });
        view = withKind(view, "reminder", { notificationSent: 8, tag: "ignored", channels: ["computer"] });
        await renderReady(view);
        const offered = Array.from(document.querySelectorAll("[data-kind]"))
            .filter((row) => within(row as HTMLElement).queryByRole("button", { name: "Show on Today instead" }))
            .map((row) => row.getAttribute("data-kind"));
        expect(offered).toEqual(["capture_paused", "brief_ready", "meeting_prep"]);
    });

    it("quiets a kind to a line on Today and can bring its notices back", async () => {
        await renderReady();
        const quieted = withKind(makeView(), "capture_paused", {
            quiet: true,
            quietOnToday: true,
            deliveryMode: "quiet_on_today",
            tag: "quiet",
        });
        mocks.setQuiet.mockResolvedValueOnce(quieted);
        fireEvent.click(within(rankedRow("capture_paused")).getByRole("button", { name: "Show on Today instead" }));
        await waitFor(() => expect(within(rankedRow("capture_paused")).getByText("Quiet")).toBeInTheDocument());
        expect(mocks.setQuiet).toHaveBeenCalledWith("capture_paused", true);
        const row = rankedRow("capture_paused");
        expect(within(row).getByText("Quiet line on Today")).toBeInTheDocument();
        expect(
            within(row).getByText("Shows as a quiet line on Today. No notices on your computer or phone."),
        ).toBeInTheDocument();
        expect(within(row).queryByRole("button", { name: "Show on Today instead" })).toBeNull();

        mocks.setQuiet.mockResolvedValueOnce(makeView());
        fireEvent.click(within(row).getByRole("button", { name: "Notify me again" }));
        await waitFor(() => expect(within(rankedRow("capture_paused")).getByText("Noisy")).toBeInTheDocument());
        expect(mocks.setQuiet).toHaveBeenLastCalledWith("capture_paused", false);
    });

    it("names the kind each row's action is for", async () => {
        await renderReady();
        expect(
            within(rankedRow("brief_ready")).getByRole("button", { name: "Show on Today instead" }),
        ).toHaveAccessibleDescription("Brief ready");
    });

    it("shows dismissed and muted only beside interruptions", async () => {
        const view = makeView(
            {},
            { ...SENT, capture_paused: { quiet: true, quietOnToday: true, tag: "quiet", quietSent: 3, dismissed: 2, muted: 1, channels: ["today_quiet"] } },
        );
        await renderReady(view);
        const row = rankedRow("capture_paused");
        expect(within(row).getByText("0 sent · 0 useful")).toBeInTheDocument();
        expect(within(row).queryByText(/dismissed|muted/)).toBeNull();
    });

    it("keeps a quieted kind with no new sends in the list, with its quiet lines counted", async () => {
        const view = makeView(
            { summary: { sent: 24, openedOrActed: 1, quietSent: 4, quietOpenedOrActed: 0 } },
            { ...SENT, capture_paused: { quiet: true, quietOnToday: true, tag: "quiet", quietSent: 4, channels: ["today_quiet"] } },
        );
        await renderReady(view);
        expect(rankedOrder()).toContain("capture_paused");
        expect(within(rankedRow("capture_paused")).getByText("4 quiet lines on Today")).toBeInTheDocument();
        expect(screen.getByText(/4 quiet lines on Today\.$/)).toBeInTheDocument();
    });

    it("shows a paused chip, the reason and Resume for a kind that paused itself", async () => {
        await renderReady(withKind(makeView(), "brief_ready", { paused: true, dismissed: 11 }));
        const row = rankedRow("brief_ready");
        expect(within(row).getByText("Paused")).toBeInTheDocument();
        expect(within(row).getByText("Isoform paused Brief ready because you closed most of them without acting.")).toBeInTheDocument();
        expect(within(row).getByRole("switch", { name: "Resume Brief ready" })).not.toBeChecked();
        mocks.setKind.mockResolvedValueOnce(makeView());
        fireEvent.click(within(row).getByRole("button", { name: "Resume" }));
        await waitFor(() => expect(within(rankedRow("brief_ready")).getByRole("switch", { name: "Brief ready" })).toBeChecked());
        expect(mocks.setKind).toHaveBeenCalledWith("brief_ready", true);
    });

    it("refreshes on focus without overwriting an in-flight change", async () => {
        await renderReady();
        const paused = withKind(makeView(), "brief_ready", { enabled: false, paused: true });
        mocks.getSettings.mockResolvedValue(paused);
        fireEvent(window, new Event("focus"));
        const resume = await screen.findByRole("switch", { name: "Resume Brief ready" });
        expect(mocks.getSettings).toHaveBeenCalledTimes(2);

        let resolveSave!: (view: EngagementSettingsView) => void;
        mocks.setKind.mockImplementationOnce(() => new Promise((resolve) => { resolveSave = resolve; }));
        fireEvent.click(resume);
        expect(resume).toBeDisabled();
        fireEvent(window, new Event("focus"));
        expect(mocks.getSettings).toHaveBeenCalledTimes(2);
        resolveSave(makeView());
        await waitFor(() => expect(screen.getByRole("switch", { name: "Brief ready" })).toBeChecked());
    });

    it("shows the week's line beside the 30 days, without opening anything", async () => {
        mocks.getWeek.mockResolvedValue({ available: true, sent: 9, actedOn: 6, days: 7, kinds: [] });
        await renderReady();
        expect(await screen.findByText("Isoform spoke up 9 times this week, you acted on 6.")).toBeInTheDocument();
        // One week read feeds the line and the folded table.
        fireEvent.click(screen.getByRole("button", { name: "The last 7 days in detail" }));
        await screen.findByRole("table", { name: en.proactiveNotifications.engagement.title });
        expect(mocks.getWeek).toHaveBeenCalledTimes(1);
    });

    it("keeps the week's counts, folded under the list", async () => {
        mocks.getWeek.mockResolvedValue({
            available: true, sent: 9, actedOn: 6, days: 7,
            kinds: [{ kind: "brief_ready", created: 9, scheduled: 0, sent: 9, delivered: 9, opened: 7, acted: 6, dismissed: 1, muted: 0, actRate: 6 / 9 }],
        });
        await renderReady();
        fireEvent.click(screen.getByRole("button", { name: "The last 7 days in detail" }));
        const table = await screen.findByRole("table", { name: en.proactiveNotifications.engagement.title });
        expect(within(table).getByRole("rowheader", { name: "Brief ready" })).toBeInTheDocument();
        expect(within(table).getByText("67%")).toBeInTheDocument();
        expect(screen.getByText(en.proactiveNotifications.engagement.measurement)).toBeInTheDocument();
    });

    // ── Check-ins and where notices go ──

    it("has no check-ins card, and the reply kind says it can be answered from the notice (ST17)", async () => {
        await renderReady();
        expect(screen.queryByRole("region", { name: "Check-ins that start a conversation" })).toBeNull();
        expect(screen.queryByText(/After-meeting check-ins come later/)).toBeNull();
        expect(en.proactiveNotifications.kinds.waiting_on_reply.description).toBe(
            "Someone you know wrote to you directly more than a day ago and has no answer yet. You can answer from the notice.",
        );
    });

    it("says where notices go and opens quiet hours and limits under Advanced", async () => {
        const { onOpenPage } = await renderReady();
        expect(
            screen.getByText("This computer while Isoform is in front, then your phone. Email only for playbook results."),
        ).toBeInTheDocument();
        fireEvent.click(screen.getByRole("button", { name: "Quiet hours and limits" }));
        expect(onOpenPage).toHaveBeenCalledWith("notifications", { advanced: true, anchor: "quiet-hours" });
    });

    // ── Quiet hours and limits ──

    it("explains quiet hours and the daily limit on their own rows, with the digest times", async () => {
        await renderReady();
        expect(screen.getByRole("heading", { name: "Quiet hours and limits" })).toBeInTheDocument();
        expect(document.querySelector('[data-settings-anchor="quiet-hours"]')).not.toBeNull();
        expect(screen.getByLabelText("Quiet hours start")).toHaveValue("22:00");
        expect(screen.getByLabelText("Quiet hours end")).toHaveValue("08:00");
        expect(screen.getByLabelText("Interruptions a day")).toHaveValue(6);
        expect(
            screen.getByText(
                "During quiet hours only work that needs you and has a deadline gets through, when that kind is on, at most once every two hours.",
            ),
        ).toBeInTheDocument();
        expect(screen.getByText("Things that can wait are sent together at 08:30, 13:00, 18:00.")).toBeInTheDocument();
    });

    it("says quiet hours are off when the start and end match, and drops the digest line without times", async () => {
        await renderReady(makeView({ quietStart: "09:00", quietEnd: "09:00", digestTimes: [] }));
        expect(screen.getByText("Quiet hours are off because the start and end are the same.")).toBeInTheDocument();
        expect(screen.queryByText(/sent together at/)).not.toBeInTheDocument();
    });

    it("saves quiet hours on change and the budget on blur, keeping the other rules", async () => {
        await renderReady();
        mocks.setRules.mockResolvedValueOnce(makeView({ quietStart: "23:00" }));
        fireEvent.change(screen.getByLabelText("Quiet hours start"), { target: { value: "23:00" } });
        await waitFor(() => expect(screen.getByLabelText("Quiet hours start")).toHaveValue("23:00"));
        expect(mocks.setRules).toHaveBeenLastCalledWith({ quietStart: "23:00", quietEnd: "08:00", dailyInterruptBudget: 6 });

        mocks.setRules.mockResolvedValueOnce(makeView({ quietStart: "23:00", dailyInterruptBudget: 3 }));
        const budget = screen.getByLabelText("Interruptions a day");
        fireEvent.change(budget, { target: { value: "3" } });
        expect(mocks.setRules).toHaveBeenCalledTimes(1);
        fireEvent.blur(budget);
        await waitFor(() => expect(mocks.setRules).toHaveBeenCalledTimes(2));
        expect(mocks.setRules).toHaveBeenLastCalledWith({ quietStart: "23:00", quietEnd: "08:00", dailyInterruptBudget: 3 });
        await waitFor(() => expect(budget).not.toBeDisabled());
        expect(budget).toHaveValue(3);

        // An out-of-range budget is dropped without a call.
        fireEvent.change(budget, { target: { value: "99" } });
        fireEvent.blur(budget);
        expect(mocks.setRules).toHaveBeenCalledTimes(2);
        expect(budget).toHaveValue(3);
    });

    it("hides quiet hours rather than edit them on a view that could not be read again", async () => {
        await renderReady();
        expect(screen.getByLabelText("Quiet hours start")).toBeInTheDocument();
        mocks.getSettings.mockRejectedValueOnce(new Error("offline"));
        fireEvent(window, new Event("focus"));
        await waitFor(() => expect(screen.queryByLabelText("Quiet hours start")).toBeNull());
        expect(screen.getByRole("alert")).toHaveTextContent("Could not check notifications. Try again.");
    });

    it("says when the rules do not save and keeps the old values", async () => {
        await renderReady();
        fireEvent.change(screen.getByLabelText("Quiet hours end"), { target: { value: "09:00" } });
        await waitFor(() => expect(mocks.toast.error).toHaveBeenCalledWith("Could not change the rules. Try again."));
        expect(screen.getByLabelText("Quiet hours end")).toHaveValue("08:00");
        const budget = screen.getByLabelText("Interruptions a day");
        fireEvent.change(budget, { target: { value: "2" } });
        fireEvent.keyDown(budget, { key: "Enter" });
        await waitFor(() => expect(mocks.toast.error).toHaveBeenCalledTimes(2));
        expect(budget).toHaveValue(6);
    });

    // ── Busy, unavailable and failed ──

    it("shows the busy state while the settings are read", () => {
        mocks.getSettings.mockReturnValue(new Promise(() => {}));
        render(<Page />);
        expect(screen.getByText("Checking notifications...")).toBeInTheDocument();
    });

    it("says when notifications are not ready and offers a retry after a failed read", async () => {
        await renderReady(makeView({ available: false, kinds: [] }));
        expect(screen.getByText(/Notifications are not ready yet/)).toBeInTheDocument();
        expect(screen.queryByRole("switch")).not.toBeInTheDocument();
        expect(screen.queryByLabelText("Quiet hours start")).not.toBeInTheDocument();
    });

    it("offers Try again after a failed read and loads on retry", async () => {
        mocks.getSettings.mockRejectedValueOnce(new Error("offline"));
        render(<Page />);
        expect(await screen.findByRole("alert")).toHaveTextContent("Could not check notifications. Try again.");
        mocks.getSettings.mockResolvedValueOnce(makeView());
        fireEvent.click(screen.getByRole("button", { name: "Try again" }));
        expect(await screen.findByRole("heading", { name: "The last 30 days" })).toBeInTheDocument();
    });

    it("writes the ranked list in Chinese with the Chinese push lines", async () => {
        await i18n.changeLanguage("zh-CN");
        await renderReady();
        const capture = rankedRow("capture_paused");
        expect(within(capture).getByText("观察已暂停")).toBeInTheDocument();
        expect(within(capture).getByText("“Isoform 暂停观察了一会儿。”")).toBeInTheDocument();
        expect(within(capture).getByText("太频繁")).toBeInTheDocument();
        expect(within(capture).getByText("电脑和手机")).toBeInTheDocument();
        expect(screen.getByRole("heading", { name: "过去 30 天" })).toBeInTheDocument();
        await i18n.changeLanguage("en");
    });
});

// ST13: the per-kind week table is developer-only; the week's line stays.
describe("NotificationsSettings without Developer tools", () => {
    it("keeps the week's line and hides the last 7 days in detail", async () => {
        devTools.on = false;
        mocks.getWeek.mockResolvedValue({ available: true, sent: 9, actedOn: 6, days: 7, kinds: [] });
        await renderReady();
        expect(await screen.findByText("Isoform spoke up 9 times this week, you acted on 6.")).toBeInTheDocument();
        expect(screen.queryByRole("button", { name: "The last 7 days in detail" })).not.toBeInTheDocument();
    });
});
