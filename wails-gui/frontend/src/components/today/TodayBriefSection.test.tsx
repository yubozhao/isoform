import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import i18n from "@/i18n";
import { briefDateTime } from "@/lib/brief-time";
import type { MobileBriefCard, MobileBriefDocument, MobileBriefItem, TodayBriefView } from "@/lib/wails-shim-app";
import { MAX_MEETINGS, MAX_SECTION_ITEMS, TodayBriefSection, briefItemLink, narrativeInPartOrder, needsYou, nextMeetings } from "./TodayBriefSection";
import { resetTodayChatDrafts } from "@/lib/today-chat-prepared";
import { todayBriefCardRef, type TodayChatContextRef } from "@/lib/today-chat-navigation";
import { buttonVariants } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import { dayShapeFixture } from "./today-next.fixture";
import { briefHoldSource } from "./useBriefHoldOffer";

describe("narrativeInPartOrder", () => {
    const done = { text: "You shipped the draft.", refs: ["e1"] };
    const next = { text: "Send it to Sam before the review.", refs: ["e2"] };
    const closing = { text: "Want me to book the room?", refs: ["e2"] };

    it("keeps done first, then next, with the closing question last", () => {
        const brief = { narrative: [done, next, closing], narrativeParts: [{ kind: "done", count: 1 }, { kind: "next", count: 2 }] };
        expect(narrativeInPartOrder(brief)).toEqual([done, next, closing]);
    });

    it("keeps the stored narrative when a brief has no parts", () => {
        const brief = { narrative: [next, closing] };
        expect(narrativeInPartOrder(brief)).toEqual([next, closing]);
    });

    it("reads a null narrative as empty", () => {
        expect(narrativeInPartOrder({ narrative: null, narrativeParts: null })).toEqual([]);
    });
});

const mocks = vi.hoisted(() => ({ signIn: vi.fn(async () => undefined), reopen: vi.fn(), openURL: vi.fn(), prepareDrafts: vi.fn(), postClosing: vi.fn(), addSchedule: vi.fn(), undoSchedule: vi.fn(), toast: Object.assign(vi.fn(), { error: vi.fn() }), moveGoal: vi.fn(), noteCheckIn: vi.fn(), setKindEnabled: vi.fn(), openDeliverable: vi.fn(), permissions: vi.fn(), requestPermission: vi.fn(), permissionSettings: vi.fn(), recordToday: vi.fn(), correctBelief: vi.fn(), undoBelief: vi.fn(), canUndoBelief: vi.fn(), holdSettings: vi.fn(), propose: vi.fn(), hold: vi.fn(), holdStatus: vi.fn(), undoHold: vi.fn(), dismissHold: vi.fn(), undoDismissHold: vi.fn(), removeHold: vi.fn(), undoRemove: vi.fn(), openCalendar: vi.fn(), prepForPerson: vi.fn(), draftRoute: vi.fn() }));

vi.mock("sonner", () => ({ toast: mocks.toast }));

vi.mock("@/hooks/useFirstBriefSeen", () => ({ useFirstBriefSeen: () => ({ current: null }) }));

vi.mock("@/lib/wails-shim-app", () => ({
    OpenAssistantSignIn: mocks.signIn,
    ReopenMainChatSetupSteps: mocks.reopen,
    PrepareTodayChatDrafts: mocks.prepareDrafts,
    PostTodayClosingAnswer: mocks.postClosing,
    AddTodayNoticedSchedule: mocks.addSchedule,
    UndoTodayNoticedSchedule: mocks.undoSchedule,
    MoveGoal: mocks.moveGoal,
    NoteGoalCheckInShown: mocks.noteCheckIn,
    SetEngagementKindEnabled: mocks.setKindEnabled,
    GetTodayYourWeekDeclined: vi.fn(async () => false),
    DeclineTodayYourWeek: vi.fn(async () => undefined),
    OpenDeliverableFile: mocks.openDeliverable,
    GetTodayPermissionStates: mocks.permissions, RequestTodayPermission: mocks.requestPermission,
    OpenPermissionSettings: mocks.permissionSettings, RecordTodayAction: mocks.recordToday, RecordTodaySectionSeen: vi.fn(async () => undefined),
    CorrectTodayWeekBelief: mocks.correctBelief, UndoTodayWeekBelief: mocks.undoBelief, CanUndoTodayWeekBelief: mocks.canUndoBelief,
    GetHoldSettings: mocks.holdSettings, ProposeHold: mocks.propose, HoldTime: mocks.hold, GetHoldStatus: mocks.holdStatus, UndoHold: mocks.undoHold,
    DismissHoldOffer: mocks.dismissHold, UndoDismissHoldOffer: mocks.undoDismissHold,
    RemoveHold: mocks.removeHold, UndoRemoveHold: mocks.undoRemove,
    OpenExternalURL: mocks.openCalendar,
    GetTodayPrepMeetingForPerson: mocks.prepForPerson,
    GetTodayBriefDraftRoute: mocks.draftRoute,
}));
vi.mock("../../../wailsjs/runtime/runtime", () => ({
    BrowserOpenURL: mocks.openURL,
    EventsOn: vi.fn(() => () => {}),
}));

const NOW = new Date(2026, 9, 15, 9, 30);

function handlers() {
    return {
        onStartChat: vi.fn(),
        onRunNow: vi.fn(),
        onOpenMemory: vi.fn(),
        onOpenAutomations: vi.fn(),
        onOpenActivity: vi.fn(),
        onOpenPeople: vi.fn(),
        onOpenSettings: vi.fn(),
        onOpenMainChat: vi.fn(),
        onOpenBriefConversation: vi.fn(async (_briefId: string) => true),
        onAskAboutParagraph: vi.fn(async (_briefId: string, _paragraph: number, _text: string, _question: string) => true),
    };
}

function item(overrides: Partial<MobileBriefItem>): MobileBriefItem {
    return { id: "x-1", key: `${overrides.id ?? "x-1"}.key`, title: "Title", detail: "", status: "", at: "", kind: "", refs: ["e1"], ...overrides };
}

/** A row's buttons by test id or name; a goal row's "..." is "goal-menu". */
function rowActions(row: HTMLElement): string[] {
    return within(row).queryAllByRole("button").map((button) => button.dataset.testid || button.textContent || "");
}

/** The question card Go writes for a row (mobile_brief_cards.go). */
const rowCardId = (row: MobileBriefItem) => `item:${row.key || row.id}`;

/** A card click puts the card's words in the message box, unsent, with the
 *  card itself as the context Go checks at Send (backlog item 64). */
function expectCardInBox(onStartChat: ReturnType<typeof vi.fn>, doc: MobileBriefDocument, cardId: string, last = false) {
    const card = withCards(doc).cards!.find((candidate) => candidate.id === cardId);
    expect(card).toBeDefined();
    const call = [card!.message, { sourceKind: "brief_card", sourceId: cardId, briefId: doc.briefId, generatedMessage: card!.message, ...(cardId.startsWith("week:ask:") ? { telemetryAction: "T84" } : {}) }];
    if (last) expect(onStartChat).toHaveBeenLastCalledWith(...call);
    else expect(onStartChat).toHaveBeenCalledWith(...call);
}

/** Like Go: one question card per row with sources, unless the test says. */
function withCards(doc: MobileBriefDocument): MobileBriefDocument {
    if (doc.cards) return doc;
    const rows = [...Object.values(doc.sections ?? {}).flatMap((list) => (Array.isArray(list) ? list : [])), ...(doc.upcoming ?? [])];
    const cards: MobileBriefCard[] = [];
    for (const row of rows) {
        const key = row.key || row.id;
        if (!(row.refs ?? []).length || cards.some((card) => card.itemKey === key)) continue;
        cards.push({ id: `item:${key}`, kind: "item", mode: "send", message: `Where do I stand on ${row.title}?`, refs: row.refs, itemKey: key });
    }
    return { ...doc, cards };
}

function brief(overrides: Partial<MobileBriefDocument> = {}): MobileBriefDocument {
    return {
        version: 1,
        briefId: "2026-10-15-morning",
        slot: "morning",
        localDate: "2026-10-15",
        asOf: new Date(2026, 9, 15, 7, 0).toISOString(),
        coldStart: false,
        narrativeStatus: "ok",
        narrative: [{ text: "You have two meetings and the release is close.", refs: ["e1"] }],
        sections: {
            moved: [], doing: [], watching: [], meetings: [], people: [], openLoops: [], learned: [], deliverables: [], schedules: [],
        },
        dayShape: null,
        evidence: [
            { ref: "e1", sourceKind: "calendar", sourceId: "ev1", label: "Design review", observedAt: "" },
            { ref: "e2", sourceKind: "memory", sourceId: "mem-7", label: "Prefers mornings", observedAt: "" },
            { ref: "e3", sourceKind: "web", sourceId: "site:wails.io", label: "wails.io", observedAt: "" },
        ],
        ...overrides,
    };
}

function view(overrides: Partial<TodayBriefView> = {}): TodayBriefView {
    return {
        brief: null,
        building: false,
        enabled: true,
        nextAt: new Date(2026, 9, 15, 18, 0).toISOString(),
        nextSlot: "evening",
        empty: false,
        missing: [],
        ...overrides,
    };
}

/** RC 1 folds Today's sections under "More from today"; opening every row
 *  shows each section as Today listed it before. */
function openMoreFromToday() {
    for (const row of document.querySelectorAll<HTMLButtonElement>("[data-more-row] > div > button[aria-expanded='false']")) {
        fireEvent.click(row);
    }
}

function renderSection(props: Partial<Parameters<typeof TodayBriefSection>[0]> = {}, { folded = false } = {}) {
    const h = handlers();
    const onMakeNow = vi.fn();
    const shown = props.view === undefined ? view() : props.view;
    render(
        <TodayBriefSection
            loading={false}
            make="idle"
            nextAllowedAt=""
            onMakeNow={onMakeNow}
            handlers={h}
            now={NOW}
            {...props}
            view={shown?.brief ? { ...shown, brief: withCards(shown.brief) } : shown}
        />,
    );
    if (!folded) openMoreFromToday();
    return { h, onMakeNow };
}

beforeEach(async () => {
    await i18n.changeLanguage("en");
    vi.clearAllMocks();
    resetTodayChatDrafts();
    mocks.reopen.mockResolvedValue(undefined);
    mocks.permissions.mockResolvedValue({ screenRecording: "not_determined", calendars: "not_determined" });
    mocks.requestPermission.mockResolvedValue(true);
    mocks.permissionSettings.mockResolvedValue(undefined);
    mocks.recordToday.mockResolvedValue(undefined);
    mocks.addSchedule.mockResolvedValue({ id: "job", revision: "v1", proof: "proof" });
    mocks.undoSchedule.mockResolvedValue(undefined);
    mocks.correctBelief.mockResolvedValue("opaque-receipt");
    mocks.undoBelief.mockResolvedValue(undefined);
    mocks.canUndoBelief.mockResolvedValue(true);
    mocks.prepareDrafts.mockImplementation(async (refs: TodayChatContextRef[]) => refs.map(() => ({ error: "failed" })));
    mocks.postClosing.mockImplementation(async (_briefID: string, cardID: string) => ({ cardId: cardID, label: cardID === "hold" ? "Draft the memo" : "Not this week", questionDigest: "closing-question-facts" }));
});

afterEach(() => vi.useRealTimers());

describe("TodayBriefSection with a brief", () => {
    it("removes a saved hold from the strip and can undo it", async () => {
        const event = { ...dayShapeFixture.events[0], start: "2026-10-23T13:00:00-07:00", end: "2026-10-23T14:00:00-07:00", holdId: "hold-1" };
        mocks.removeHold.mockResolvedValue(undefined);
        mocks.undoRemove.mockResolvedValue(undefined);
        renderSection({ view: view({ brief: brief() }), dayShape: { ...dayShapeFixture, hold: null, events: [event] }, dayShapeDate: "2026-10-23" });
        fireEvent.click(screen.getByRole("button", { name: "Held for 1:1 with Priya on Halcyon Meet" }));
        fireEvent.click(screen.getByRole("button", { name: "Remove the hold" }));
        expect(await screen.findByText(/The hold is off your calendar/)).toBeInTheDocument();
        expect(screen.queryByRole("button", { name: "Held for 1:1 with Priya on Halcyon Meet" })).not.toBeInTheDocument();
        fireEvent.click(screen.getByRole("button", { name: "Undo" }));
        await waitFor(() => expect(mocks.undoRemove).toHaveBeenCalledWith("hold-1"));
        expect(mocks.removeHold).toHaveBeenCalledWith("hold-1");
    });
    it("explains revoked access after a strip removal fails", async () => {
        const event = { ...dayShapeFixture.events[0], start: "2026-10-23T13:00:00-07:00", end: "2026-10-23T14:00:00-07:00", holdId: "hold-1" };
        mocks.removeHold.mockRejectedValueOnce(new Error("denied"));
        mocks.holdSettings.mockResolvedValueOnce({ granted: false });
        renderSection({ view: view({ brief: brief() }), dayShape: { ...dayShapeFixture, hold: null, events: [event] }, dayShapeDate: "2026-10-23" });
        fireEvent.click(screen.getByRole("button", { name: "Held for 1:1 with Priya on Halcyon Meet" }));
        fireEvent.click(screen.getByRole("button", { name: "Remove the hold" }));
        expect(await screen.findByText(/can't reach your calendar/)).toBeInTheDocument();
        fireEvent.click(screen.getByRole("button", { name: "Open System Settings" }));
        expect(mocks.permissionSettings).toHaveBeenCalledWith("calendars");
    });
    it("offers the referenced commitment on the strip and opens its checked card", async () => {
        const entry = item({ id: "q4", key: "commitments.1234567890abcdef", title: "Q4 plan", refs: ["e1"], attentionReason: "Due Monday." });
        const doc = brief({ timezone: "America/Los_Angeles", sections: { ...brief().sections, commitments: [entry] }, narrative: [{ text: "Want me to find time for Q4 plan?", refs: ["e1"] }] });
        const actions = { ...handlers(), onFindTime: vi.fn() };
        const slot = { start: "2026-10-23T13:00:00-07:00", end: "2026-10-23T14:00:00-07:00" };
        mocks.holdSettings.mockResolvedValue({ granted: true, offerHolds: true, calendarId: "local" });
        mocks.propose.mockResolvedValue({ slots: [slot] });
        renderSection({ view: view({ brief: doc }), handlers: actions, observationPaused: false, dayShape: { ...dayShapeFixture, hold: null }, dayShapeDate: "2026-10-23" });
        fireEvent.click(await screen.findByRole("button", { name: "Open hold offer for Q4 plan" }));
        expect(actions.onFindTime).toHaveBeenCalledWith({ thing: { title: "Q4 plan", sourceKind: "brief", sourceId: entry.key }, due: undefined, reason: "Due Monday.", preferred: slot, telemetryAction: "T31" });
        expect(mocks.propose).toHaveBeenCalledWith({ title: "Q4 plan", sourceKind: "brief", sourceId: entry.key }, "", 0);
    });

    it.each(["en", "zh-CN"])("holds the displayed brief slot in one press and can undo in %s", async (language) => {
        await i18n.changeLanguage(language);
        const entry = item({ key: "commitments.1234567890abcdef", title: "Q4 plan", refs: ["e1"] });
        const doc = brief({ timezone: "America/Los_Angeles", sections: { ...brief().sections, commitments: [entry] }, narrative: [{ text: "Want me to find time for Q4 plan?", refs: ["e1"] }] });
        const slot = { start: "2026-10-23T13:00:00-07:00", end: "2026-10-23T14:00:00-07:00" };
        const actions = { ...handlers(), onFindTime: vi.fn() };
        mocks.holdSettings.mockResolvedValue({ granted: true, offerHolds: true, calendarId: "local", calendars: [{ id: "local", meetingsAccount: true }] });
        mocks.propose.mockResolvedValue({ slots: [slot] });
        mocks.hold.mockResolvedValue({ id: "h", state: "pending_write", writeAfter: new Date(Date.now() + 5000).toISOString() });
        renderSection({ view: view({ brief: doc }), handlers: actions, observationPaused: false, dayShape: { ...dayShapeFixture, hold: null }, dayShapeDate: "2026-10-23" });
        const closing = await screen.findByTestId("brief-closing");
        expect(closing).toHaveTextContent(language === "en" ? "Others see you as busy once it's held." : "留出后，别人会看到你这段时间忙。");
        const label = language === "en" ? /^Hold 1:00/ : /^留出/;
        fireEvent.click(await within(closing).findByRole("button", { name: label }));
        await waitFor(() => expect(mocks.hold).toHaveBeenCalledOnce());
        expect(actions.onFindTime).not.toHaveBeenCalled();
        expect(closing).toHaveTextContent(language === "en" ? "Held" : "已留出");
        fireEvent.click(within(closing).getByRole("button", { name: language === "en" ? "Undo" : "撤销" }));
        await waitFor(() => expect(mocks.undoHold).toHaveBeenCalledWith("h"));
    });

    it("keeps a saved closing answer instead of replacing it with a hold offer", async () => {
        const entry = item({ id: "q4", key: "commitments.1234567890abcdef", title: "Q4 plan", refs: ["e1"] });
        const question = "Want me to find time for Q4 plan?";
        const doc = brief({ timezone: "America/Los_Angeles", sections: { ...brief().sections, commitments: [entry] }, narrative: [{ text: question, refs: ["e1"] }], cards: [{ id: "later", kind: "action", mode: "send", label: "Not this week", message: "Not this week.", answerTo: question, refs: ["e1"] }] });
        const actions = { ...handlers(), onFindTime: vi.fn() };
        mocks.holdSettings.mockResolvedValue({ granted: true, offerHolds: true, calendarId: "local" });
        mocks.propose.mockResolvedValue({ slots: [{ start: "2026-10-23T13:00:00-07:00", end: "2026-10-23T14:00:00-07:00" }] });
        renderSection({ view: view({ brief: doc, closingAnswer: { cardId: "later", label: "Not this week", questionDigest: "digest" } }), handlers: actions, observationPaused: false, dayShape: { ...dayShapeFixture, hold: null }, dayShapeDate: "2026-10-23" });
        expect(await screen.findByText("You answered: Not this week ·")).toBeInTheDocument();
        expect(screen.queryByRole("button", { name: "Open hold offer for Q4 plan" })).not.toBeInTheDocument();
        expect(mocks.propose).not.toHaveBeenCalled();
    });

    it("does not replace an answer being posted when a hold check finishes later", async () => {
        const entry = item({ id: "q4", key: "commitments.1234567890abcdef", title: "Q4 plan", refs: ["e1"] });
        const question = "Want me to find time for Q4 plan?";
        const doc = brief({ timezone: "America/Los_Angeles", sections: { ...brief().sections, commitments: [entry] }, narrative: [{ text: question, refs: ["e1"] }], cards: [{ id: "later", kind: "action", mode: "send", label: "Not this week", message: "Not this week.", answerTo: question, refs: ["e1"] }] });
        let finishProposal!: (result: { slots: { start: string; end: string }[] }) => void;
        mocks.holdSettings.mockResolvedValue({ granted: true, offerHolds: true, calendarId: "local" });
        mocks.propose.mockReturnValue(new Promise((resolve) => { finishProposal = resolve; }));
        renderSection({ view: view({ brief: doc }), handlers: { ...handlers(), onFindTime: vi.fn() }, observationPaused: false, dayShape: { ...dayShapeFixture, hold: null }, dayShapeDate: "2026-10-23" });
        await waitFor(() => expect(mocks.propose).toHaveBeenCalled());
        fireEvent.click(screen.getByRole("button", { name: "Not this week" }));
        expect(await screen.findByText("You answered: Not this week ·")).toBeInTheDocument();
        await act(async () => finishProposal({ slots: [{ start: "2026-10-23T13:00:00-07:00", end: "2026-10-23T14:00:00-07:00" }] }));
        expect(screen.getByText("You answered: Not this week ·")).toBeInTheDocument();
        expect(screen.queryByRole("button", { name: "Open hold offer for Q4 plan" })).not.toBeInTheDocument();
    });

    it("does not infer a hold source from an unrelated or ambiguous closing", () => {
        const entry = item({ key: "commitments.1234567890abcdef", refs: ["e1"] });
        const doc = brief({ sections: { ...brief().sections, commitments: [entry] }, narrative: [{ text: "Want me to draft the reply?", refs: ["e1"] }] });
        expect(briefHoldSource(doc)).toBeNull();
        doc.narrative = [{ text: "Want me to find time?", refs: ["e1"] }];
        doc.sections.commitments = [entry, { ...entry, id: "other", key: "commitments.abcdefabcdefabcd" }];
        expect(briefHoldSource(doc)).toBeNull();
    });

    it("dismisses the brief offer without a write and can undo", async () => {
        const entry = item({ id: "q4", key: "commitments.1234567890abcdef", title: "Q4 plan", refs: ["e1"] });
        const doc = brief({ timezone: "America/Los_Angeles", sections: { ...brief().sections, commitments: [entry] }, narrative: [{ text: "要找个时间处理 Q4 plan 吗？", refs: ["e1"] }] });
        const actions = { ...handlers(), onFindTime: vi.fn() };
        mocks.holdSettings.mockResolvedValue({ granted: true, offerHolds: true, calendarId: "local" });
        mocks.propose.mockResolvedValue({ slots: [{ start: "2026-10-23T13:00:00-07:00", end: "2026-10-23T14:00:00-07:00" }] });
        mocks.dismissHold.mockResolvedValue("dismissal");
        mocks.undoDismissHold.mockResolvedValue(undefined);
        renderSection({ view: view({ brief: doc }), handlers: actions, observationPaused: false, dayShape: { ...dayShapeFixture, hold: null }, dayShapeDate: "2026-10-23" });
        await screen.findByRole("button", { name: "Open hold offer for Q4 plan" });
        fireEvent.click(within(screen.getByTestId("brief-closing")).getByRole("button", { name: "Not now" }));
        fireEvent.click(within(await within(screen.getByTestId("brief-closing")).findByRole("status")).getByRole("button", { name: "Undo" }));
        await waitFor(() => expect(mocks.undoDismissHold).toHaveBeenCalledWith("dismissal"));
        expect(mocks.dismissHold).toHaveBeenCalledWith({ title: "Q4 plan", sourceKind: "brief", sourceId: entry.key });
        expect(actions.onFindTime).not.toHaveBeenCalled();
    });

    it.each([["en", "Couldn't read your calendar. Try again", "Try again"], ["zh-CN", "没能读取你的日历。 重试", "重试"]])("puts the calendar read failure in place of the strip and retries in %s (v1.1.5 #3)", async (language, line, label) => {
        await i18n.changeLanguage(language);
        const retry = vi.fn();
        renderSection({ view: view({ brief: brief() }), dayShape: null, dayShapeFailure: { lastUpdatedAt: new Date(2026, 9, 30, 10, 12).getTime(), retry } });
        expect(screen.getByTestId("day-strip-read-failed")).toHaveTextContent(line);
        expect(screen.getByTestId("brief-card").querySelector('[data-testid="today-day-strip"], [data-testid="day-strip"]')).toBeNull();
        fireEvent.click(screen.getByRole("button", { name: label }));
        await i18n.changeLanguage("en");
        expect(retry).toHaveBeenCalledOnce();
        expect(mocks.recordToday).toHaveBeenCalledWith("today", "T32", "do", "done", expect.any(Number));
    });

    it("keeps the privacy note at the foot of an empty brief's card (v1.1.5 #4)", () => {
        renderSection({ view: view({ brief: brief({ limits: { itemsPerSection: 8, evidenceRefs: 60, truncatedSections: [], excludedSensitiveItems: 1 } }), empty: true }) });
        const note = screen.getByTestId("brief-sensitive-excluded");
        expect(screen.queryByTestId("today-quiet-footer")).not.toBeInTheDocument();
        expect(note.parentElement?.lastElementChild).toBe(note);
    });

    it("omits the privacy note when no activity was excluded", () => {
        renderSection({ view: view({ brief: brief({ limits: { itemsPerSection: 8, evidenceRefs: 60, truncatedSections: [], excludedSensitiveItems: 0 } }) }) });
        expect(screen.queryByTestId("brief-sensitive-excluded")).not.toBeInTheDocument();
    });

    it.each(["en", "zh-CN"])("never shows the paragraphs-only weekly reading, which older phones and the recording read, in %s", async (lang) => {
        await i18n.changeLanguage(lang);
        const text = lang === "en" ? "I sent 4 messages this week." : "本周我发出了 4 条消息。";
        renderSection({ view: view({ brief: brief({ weeklyRecap: [{ text, refs: [] }] }) }) });
        expect(screen.queryByText(text)).not.toBeInTheDocument();
        expect(screen.queryByTestId("brief-your-week")).not.toBeInTheDocument();
        expect(screen.getByTestId("brief-narrative")).toBeInTheDocument();
    });

    it("shows the narrative, the shape of the day, the next meetings with Join, and one line to Activity for the rest", () => {
        const doc = brief({
            dayShape: { activeMinutes: 200, hours: [], topApps: [{ name: "Figma", activeMinutes: 90 }], breakMinutes: 35 },
            sections: {
                ...brief().sections,
                meetings: [
                    item({ id: "meetings-1", title: "Design review", detail: "10:00-10:30; Halcyon Meet", status: "upcoming", kind: "meeting", at: new Date(2026, 9, 15, 10, 0).toISOString(), url: "https://meet.halcyon.example/room-123" }),
                    item({ id: "meetings-2", title: "Standup", status: "done", kind: "meeting", at: new Date(2026, 9, 15, 9, 0).toISOString(), url: "https://meet.google.com/abc" }),
                    item({ id: "meetings-3", title: "The shape of today", detail: "Two meetings, a free afternoon.", kind: "day_shape" }),
                ],
                learned: [item({ id: "learned-1", title: "Prefers mornings", refs: ["e2"] })],
                watching: [
                    item({ id: "watching-find-1", title: "Wails 2.12.1 released", kind: "find", url: "https://github.com/wailsapp/wails" }),
                    item({ id: "watching-1", title: "wails.io", detail: "4 visits", refs: ["e3"] }),
                ],
                schedules: [item({ id: "schedules-1", title: "Daily digest ran" })],
            },
        });
        const { h } = renderSection({ view: view({ brief: doc }) });

        expect(screen.getByTestId("brief-narrative")).toHaveTextContent("You have two meetings and the release is close.");
        expect(screen.queryByText("The shape of today")).not.toBeInTheDocument();
        expect(screen.queryByText("Two meetings, a free afternoon.")).not.toBeInTheDocument();
        // Only time still ahead: no active minutes, away time or app list.
        expect(screen.queryByText(/min active|min away/)).not.toBeInTheDocument();
        expect(screen.queryByText(/Figma/)).not.toBeInTheDocument();

        expect(screen.queryByTestId("today-more")).not.toBeInTheDocument();
        expect(screen.queryByTestId("brief-section-meetings")).not.toBeInTheDocument();
    });

    it("has no line to Activity when the brief holds nothing beyond what Today shows", () => {
        const doc = brief({ sections: { ...brief().sections, moved: [item({ id: "moved-1", title: "Launch plan" })] } });
        renderSection({ view: view({ brief: doc }) });
        expect(screen.queryByTestId("brief-more-on-activity")).not.toBeInTheDocument();
    });

    it("hides every section that has nothing", () => {
        const doc = brief({ sections: { ...brief().sections } });
        renderSection({ view: view({ brief: doc }) });
        expect(document.querySelectorAll("[data-testid^='brief-section-']")).toHaveLength(0);
    });

    it.each([["purpose"], ["purpose", "session"]])("keeps narrative purpose references out of source controls (%j)", (...refs) => {
        const doc = brief({
            narrative: [{ text: "Work continues toward the launch.", refs }],
            evidence: [
                { ref: "purpose", sourceKind: "purpose", sourceId: "launch", label: "Ship the beta", observedAt: "" },
                { ref: "session", sourceKind: "activity_session", sourceId: "session-1", label: "Release draft", observedAt: "" },
            ],
        });
        renderSection({ view: view({ brief: doc }) });
        // Ask remains independent of the paragraph's source identity.
        const narrative = screen.getByTestId("brief-narrative");
        expect(narrative.querySelector("p")?.textContent).toBe("Work continues toward the launch.");
        expect(within(narrative).queryByRole("button", { name: "Ship the beta" })).not.toBeInTheDocument();
        expect(within(narrative).getByTestId("brief-ask-about-paragraph")).toBeInTheDocument();
        expect(within(narrative).queryByRole("list")).not.toBeInTheDocument();
    });

    it.each([["en", "Goal"], ["zh-CN", "目标"]])("uses the same goal term in %s", async (language, goalTerm) => {
        await i18n.changeLanguage(language);
        expect(i18n.t("todayBrief.sourceKinds.purpose", { ns: "today" })).toBe(goalTerm);
    });

    it("keeps older briefs readable when all additive fields are absent", () => {
        renderSection({ view: view({ brief: brief() }) });
        expect(screen.getByTestId("brief-narrative")).toHaveTextContent("You have two meetings");
        expect(screen.queryByTestId("brief-section-workingToward")).not.toBeInTheDocument();
        expect(screen.queryByTestId("brief-section-commitments")).not.toBeInTheDocument();
        expect(screen.queryByTestId("brief-section-waitingOnYou")).not.toBeInTheDocument();
        expect(screen.queryByTestId("brief-sensitive-excluded")).not.toBeInTheDocument();
    });

    it("still marks excluded sensitive activity when the remaining brief is empty", () => {
        renderSection({ view: view({
            empty: true,
            brief: brief({ narrative: [], limits: { itemsPerSection: 20, evidenceRefs: 100, truncatedSections: [], excludedSensitiveItems: 1 } }),
        }) });
        expect(screen.getByTestId("brief-sensitive-excluded").textContent).toBe("1 private item was left out of this brief.");
    });
});

describe("a brief made after its time", () => {
    // Design section 1: the title names the time it was made, and a Late line
    // names when the Mac was asleep.
    it.each([
        ["en", "your computer was asleep", "Evening brief ·"],
        ["zh-CN", "电脑在睡眠", "晚间简报 ·"],
    ])("titles it by the time it was made and says so in a Late line when the computer slept, in %s", async (language, late, onTime) => {
        await i18n.changeLanguage(language);
        const asleep = { reason: "asleep", scheduledAt: "2026-10-15T18:00:00Z", generatedAt: "2026-10-15T21:00:00Z", message: "Your evening brief, a bit late." };
        renderSection({ view: view({ brief: brief({ slot: "evening", generation: asleep }), briefRun: { briefId: "2026-10-15-morning", status: "ok", scheduledAt: asleep.scheduledAt, at: asleep.generatedAt, late: true, reason: "asleep" } }) });

        expect(screen.getByTestId("brief-card")).toHaveTextContent(onTime);
        expect(screen.getByTestId("brief-late-line")).toHaveTextContent(late);
        cleanup();
        renderSection({ view: view({ brief: brief({ slot: "evening" }) }) });
        expect(screen.getByTestId("brief-card")).toHaveTextContent(onTime);
        expect(screen.queryByTestId("brief-late-line")).toBeNull();
    });

    it('closes an empty paragraph question on Escape or when focus leaves', () => {
        const doc = brief({ narrative: [{ text: 'The plan is ready.', refs: ['e1'] }] });
        renderSection({ view: view({ brief: doc }), observationPaused: false });
        const ask = screen.getByTestId('brief-ask-about-paragraph');
        fireEvent.click(ask);
        const input = screen.getByRole('textbox', { name: 'Ask anything about this paragraph' });
        expect(input).toHaveFocus();
        fireEvent.keyDown(input, { key: 'Escape' });
        expect(screen.queryByRole('textbox', { name: 'Ask anything about this paragraph' })).not.toBeInTheDocument();
        fireEvent.click(ask);
        fireEvent.blur(screen.getByRole('textbox', { name: 'Ask anything about this paragraph' }), { relatedTarget: document.body });
        expect(screen.queryByRole('textbox', { name: 'Ask anything about this paragraph' })).not.toBeInTheDocument();
    });
});

describe("Today brief review regressions", () => {

    it("provides Chinese resources for every reviewed row label without English fallback", () => {
        for (const [ns, keys] of Object.entries({
            today: ["todayBrief.dailySummary", "todayBrief.dueAt", "todayBrief.overdueAt", "todayBrief.scheduleWithTimezone", "todayBrief.open", "actions.runNow", "actions.retry", "actions.openActivity"],
            app: ["handoffsPage.blockedReason.interrupted", "automationsPage.when.everyHours_other", "automationsPage.when.once", "automations.scheduleText.daily"],
        })) {
            for (const key of keys) {
                const zh = i18n.getResource("zh-CN", ns, key);
                expect(zh, `${ns}:${key}`).toBeTypeOf("string");
                if (key !== "todayBrief.scheduleWithTimezone") expect(zh, `${ns}:${key}`).toMatch(/[\u3400-\u9fff]/);
            }
        }
    });
});

describe("TodayBriefSection when the brief cannot be read", () => {
    it("says so with a retry, not that briefs are off", () => {
        const onRetry = vi.fn();
        renderSection({ view: null, loadFailed: true, onRetry });
        expect(screen.getByRole("alert")).toHaveTextContent("Couldn't load your brief.");
        expect(screen.queryByText(/Scheduled briefs are off/)).not.toBeInTheDocument();
        fireEvent.click(screen.getByRole("button", { name: "Try again" }));
        expect(onRetry).toHaveBeenCalledTimes(1);
        expect(mocks.recordToday).toHaveBeenCalledWith("today", "T03", "do", "done", expect.any(Number));
    });
});

// 2026-10-27: Today showed yesterday's evening brief at 10 AM while the
// morning brief was failing. A brief on its way is said, never hidden.
it.each([["en", "Open About you"], ["zh-CN", "打开「关于你」"]])("links a learned paragraph to About you in %s", async (language, label) => {
    await i18n.changeLanguage(language);
    const actions = handlers();
    renderSection({ handlers: actions, view: view({ brief: brief({
        narrative: [{ text: "Prefers mornings.", refs: ["learned-ref"] }, { text: "Other work.", refs: ["other-ref"] }],
        sections: { ...brief().sections, learned: [item({ kind: "memory", refs: ["learned-ref"] })] },
    }) }) });
    const links = screen.getAllByRole("button", { name: label });
    expect(links).toHaveLength(1);
    fireEvent.click(links[0]);
    expect(actions.onOpenMemory).toHaveBeenCalledOnce();
});

describe("TodayBriefSection while today's brief is on its way", () => {
    const lastAt = new Date(2026, 9, 14, 19, 30).toISOString();
    const snapshot = () => brief({ briefId: "2026-10-15-on_request-061052", slot: "on_request", displaySlot: "morning", narrative: [], narrativeStatus: "unavailable",
        sections: { moved: [item({ id: "m1", title: "Release notes" })], doing: [], watching: [], meetings: [], people: [], openLoops: [], learned: [], deliverables: [], schedules: [] } });

    it("keeps the last written brief visible while the next slot is pending", () => {
        const earlier = brief({ briefId: "2026-10-14-evening", slot: "evening", localDate: "2026-10-14", asOf: lastAt,
            narrative: [{ text: "A written evening brief.", refs: [] }] });
        renderSection({ view: view({ brief: earlier, pendingSlot: "morning", pendingAt: new Date(2026, 9, 15, 7, 0).toISOString() }) });
        expect(screen.getByTestId("brief-card")).toHaveTextContent("A written evening brief.");
        expect(screen.queryByTestId("brief-pending")).not.toBeInTheDocument();
    });

    it.each(["en", "zh-CN"])("keeps yesterday's written brief visible after midnight in %s", async (language) => {
        await i18n.changeLanguage(language);
        const yesterday = brief({ briefId: "2026-10-14-evening", slot: "evening", localDate: "2026-10-14", asOf: new Date(2026, 9, 14, 18, 2).toISOString(),
            narrative: [{ text: "Maya Chen moved the review to Friday.", refs: [] }] });
        renderSection({ view: view({ brief: yesterday }), now: new Date(2026, 9, 15, 0, 30) });
        expect(screen.getByTestId("brief-card")).toHaveTextContent("Maya Chen moved the review to Friday.");
        expect(screen.queryByTestId("brief-pending")).not.toBeInTheDocument();
    });

    it("keeps the morning brief in full through the afternoon until the next one replaces it (v1.1.23 R52)", () => {
        renderSection({
            view: view({ brief: brief({ narrative: [{ text: "Priya shared the launch notes.", refs: [] }] }) }),
            now: new Date(2026, 9, 15, 16, 45),
        });
        expect(screen.getByTestId("brief-card")).toBeInTheDocument();
        expect(screen.getByText("Priya shared the launch notes.")).toBeInTheDocument();
        expect(screen.queryByTestId("brief-pending")).not.toBeInTheDocument();
    });

    it.each([
        ["en", 6, "Tried 6 times without finishing. Isoform tries again at 9:40 AM."],
        ["en", 0, "Isoform tries again at 9:40 AM."],
        ["zh-CN", 6, "试了 6 次都没完成。Isoform 会在9:40再试。"],
        ["zh-CN", 0, "Isoform 会在9:40再试。"],
    ])("says only what the record proves about the tries in %s (%i tries, v1.1.5 #2)", async (language, attempts, line) => {
        await i18n.changeLanguage(language);
        const onOpenBrief = vi.fn();
        const { onMakeNow } = renderSection({
            view: view({ pendingSlot: "morning", pendingAt: new Date(2026, 9, 15, 7, 0).toISOString(), pendingAttempts: attempts,
                retryAt: new Date(2026, 9, 15, 9, 40).toISOString(), lastBriefId: "2026-10-14-evening", lastSlot: "evening", lastAt }),
            onOpenBrief,
        });
        const card = screen.getByTestId("brief-pending");
        expect(within(card).getByTestId("brief-pending-line").textContent).toBe(line);
        const buttons = within(card).getAllByRole("button", { name: language === "en" ? "Make my brief now" : "现在生成简报" });
        expect(buttons).toHaveLength(1);
        fireEvent.click(buttons[0]);
        expect(onMakeNow).toHaveBeenCalledTimes(1);
        if (language === "zh-CN") expect(within(card).getByTestId("brief-pending-last")).toHaveTextContent("上一份：昨天 19:30 · 阅读");
        await i18n.changeLanguage("en");
    });

    it.each([["en", "Tried 3 times. Your AI is signed out. Isoform tries again at 9:40 AM."], ["zh-CN", "已尝试 3 次。你的 AI 已退出登录。Isoform 会在 9:40 再试一次。"]])("shows the proven signed-out cause in %s", async (language, line) => {
        await i18n.changeLanguage(language);
        renderSection({ view: view({ pendingSlot: "morning", pendingAt: new Date(2026, 9, 15, 7).toISOString(), pendingAttempts: 3, pendingCause: "signed_out", retryAt: new Date(2026, 9, 15, 9, 40).toISOString() }) });
        expect(screen.getByTestId("brief-pending-line")).toHaveTextContent(line);
        await i18n.changeLanguage("en");
    });

    it.each([["en", "Tried 3 times. Codex is signed out.", "Sign in", "Make my brief now"], ["zh-CN", "已尝试 3 次。Codex 已退出登录。", "登录", "现在生成简报"]])("offers sign in without a retry promise in %s", async (language, line, action, make) => {
        await i18n.changeLanguage(language);
        mocks.signIn.mockClear();
        renderSection({ view: view({ pendingSlot: "morning", pendingAt: new Date(2026, 9, 15, 7).toISOString(), pendingAttempts: 3, pendingCause: "signed_out", pendingAssistant: "codex", retryAt: "" }) });
        const card = screen.getByTestId("brief-pending");
        expect(within(card).getByTestId("brief-pending-line")).toHaveTextContent(line);
        expect(within(card).queryByRole("button", { name: make })).toBeNull();
        fireEvent.click(within(card).getByRole("button", { name: action }));
        expect(mocks.signIn).toHaveBeenCalledWith("codex");
        await i18n.changeLanguage("en");
    });

    it("has no line without a scheduled retry: just the button", () => {
        renderSection({ view: view({ pendingSlot: "morning", pendingAt: new Date(2026, 9, 15, 7, 0).toISOString(), pendingAttempts: 6 }) });
        const card = screen.getByTestId("brief-pending");
        expect(within(card).queryByTestId("brief-pending-line")).not.toBeInTheDocument();
        expect(within(card).getByRole("button", { name: "Make my brief now" })).toBeInTheDocument();
    });

    it("says the cause when Isoform knows it (today-review v1.1.13 D3)", () => {
        renderSection({ view: view({ pendingSlot: "morning", pendingAt: new Date(2026, 9, 15, 7, 0).toISOString(), pendingAttempts: 9, pendingCause: "timeout", retryAt: new Date(2026, 9, 15, 9, 40).toISOString() }) });
        expect(within(screen.getByTestId("brief-pending")).getByTestId("brief-pending-line").textContent).toBe("Tried 9 times. It timed out. Isoform tries again at 9:40 AM.");
    });

    it("puts the calendar read failure in place of the pending card's strip", () => {
        renderSection({ view: view({ pendingSlot: "morning" }), dayShape: null, dayShapeFailure: { lastUpdatedAt: null, retry: vi.fn() } });
        expect(within(screen.getByTestId("brief-pending")).getByTestId("day-strip-read-failed")).toHaveTextContent("Couldn't read your calendar. Try again");
    });

    it("says it is making the brief now, without the button", () => {
        renderSection({ view: view({ building: true, pendingSlot: "morning", pendingAt: new Date(2026, 9, 15, 7, 0).toISOString() }) });
        expect(screen.getByTestId("brief-pending")).toHaveTextContent("Making your morning brief now.");
        expect(screen.queryByRole("button", { name: "Make my brief now" })).not.toBeInTheDocument();
        // One line says it, not two.
        expect(screen.queryByText("Making your brief...")).not.toBeInTheDocument();
        expect(screen.queryByText(/Your evening brief comes/)).not.toBeInTheDocument();
    });

    it("rings the brief's card when a missed brief's Show opens Today", () => {
        renderSection({ view: view({ pendingSlot: "evening" }), locateNonce: 1 });
        expect(screen.getByTestId("today-brief-located")).toHaveClass("activity-card-locate");
        expect(screen.getByTestId("today-brief-located")).toContainElement(screen.getByTestId("brief-pending"));
    });

    it("has Chinese words for every pending line", () => {
        for (const key of ["makingMorning", "makingEvening", "retry", "tried_other", "lastBrief", "readIt"]) {
            expect(i18n.getResource("zh-CN", "today", `todayBrief.pending.${key}`), key).toMatch(/[\u3400-\u9fff]/);
        }
    });
});

describe("TodayBriefSection without a brief", () => {
    it("says when the next brief comes and offers to make one now", () => {
        const { onMakeNow } = renderSection();
        expect(screen.getByText(/Your evening brief comes today at/)).toBeInTheDocument();
        expect(screen.getByRole("button", { name: "Make my brief now" })).toHaveClass("bg-solid-btn-bg", "text-solid-btn-fg");
        fireEvent.click(screen.getByRole("button", { name: "Make my brief now" }));
        expect(onMakeNow).toHaveBeenCalledTimes(1);
        expect(screen.queryByTestId("today-brief-blind")).not.toBeInTheDocument();
    });

    it("says the schedule is off when it is", () => {
        renderSection({ view: view({ enabled: false }) });
        expect(screen.getByText("Scheduled briefs are off. You can make one now.")).toBeInTheDocument();
    });

    it("keeps a stored brief on screen when the schedule is off, with a note under it", () => {
        const { onMakeNow } = renderSection({ view: view({ enabled: false, brief: brief() }) });
        expect(screen.getByText("You have two meetings and the release is close.")).toBeInTheDocument();
        expect(screen.getByTestId("today-brief-schedule-off")).toHaveTextContent("Scheduled briefs are off.");
        expect(screen.getByRole("button", { name: "Make my brief now" })).toHaveClass("bg-solid-btn-bg", "text-solid-btn-fg");
        fireEvent.click(screen.getByRole("button", { name: "Make my brief now" }));
        expect(onMakeNow).toHaveBeenCalledTimes(1);
    });

    it("says why a brief could not be made under a stored brief too", () => {
        renderSection({ view: view({ enabled: false, brief: brief() }), make: "failed" });
        expect(screen.getByTestId("today-brief-schedule-off")).toContainElement(screen.getByRole("alert"));
        cleanup();
        renderSection({
            view: view({ enabled: false, brief: brief() }),
            make: "cooldown",
            nextAllowedAt: new Date(2026, 9, 15, 9, 40).toISOString(),
        });
        // RC1-States: during the cooldown its line replaces the button.
        expect(screen.queryByRole("button", { name: "Make my brief now" })).not.toBeInTheDocument();
        expect(screen.getByTestId("today-brief-schedule-off")).toHaveTextContent(/9:40/);
    });

    // With no brief yet, the one being made is the first one (Isoform design,
    // first run 1.0: made right after setup, from today's calendar).
    it("says Making your brief for a later one", () => {
        renderSection({ view: view({ brief: brief(), empty: true }), make: "making" });
        expect(screen.getByRole("status")).toHaveTextContent("Making your brief...");
        expect(screen.getByRole("status")).not.toHaveTextContent("first");
    });

    it("shows that the first brief is being made, without the button", () => {
        renderSection({ make: "making" });
        expect(screen.getByRole("status")).toHaveTextContent("Making your first brief from today's calendar…");
        expect(screen.queryByRole("button", { name: "Make my brief now" })).not.toBeInTheDocument();
    });

    it("says when the next brief can be made after a cooldown, in place of the button", () => {
        renderSection({ make: "cooldown", nextAllowedAt: new Date(2026, 9, 15, 9, 40).toISOString() });
        expect(screen.queryByRole("button", { name: "Make my brief now" })).not.toBeInTheDocument();
        expect(screen.getByText(/You can make another brief today at/)).toBeInTheDocument();
    });
});

describe("TodayBriefSection when Isoform cannot see", () => {
    it("asks macOS directly from the blind brief", async () => {
        const { h } = renderSection({ view: view({ brief: brief({ narrative: [] }), empty: true, missing: ["screen_recording"] }) });
        const blind = screen.getByTestId("today-brief-blind");
        expect(blind).toHaveTextContent("This brief only knows your calendar.");
        const turnOn = await screen.findByRole("button", { name: "Turn on" });
        await waitFor(() => expect(turnOn).toBeEnabled());
        fireEvent.click(turnOn);
        await waitFor(() => expect(mocks.requestPermission).toHaveBeenCalledWith("screen_recording"));
        expect(h.onOpenMainChat).not.toHaveBeenCalled();
        expect(within(blind).getAllByRole("button")).toHaveLength(1);
    });

    it("names System Settings after macOS denied screen recording", async () => {
        mocks.permissions.mockResolvedValue({ screenRecording: "denied", calendars: "granted" });
        renderSection({ view: view({ missing: ["screen_recording"] }) });
        const button = await screen.findByRole("button", { name: "Open System Settings" });
        expect(screen.getByTestId("today-brief-blind")).toHaveTextContent("macOS said no to screen recording.");
        fireEvent.click(button);
        await waitFor(() => expect(mocks.permissionSettings).toHaveBeenCalledWith("screen_recording"));
    });

    it("does not claim the calendar is available when both sources are missing", () => {
        renderSection({ view: view({ missing: ["screen_recording", "calendars"] }) });
        const blind = screen.getByTestId("today-brief-blind");
        expect(blind).toHaveTextContent("Let Isoform see your screen and read your calendar");
        expect(blind).not.toHaveTextContent("only knows your calendar");
        expect(within(blind).getAllByRole("button")).toHaveLength(1);
    });

    it("reads in Chinese", async () => {
        await i18n.changeLanguage("zh-CN");
        renderSection({ view: view({ missing: ["accessibility"] }) });
        expect(screen.getByTestId("today-brief-blind")).toHaveTextContent("Isoform 还看不到你的一天");
        expect(screen.getByRole("button", { name: "现在生成简报" })).toBeInTheDocument();
    });
});

describe("briefItemLink", () => {
    const evidence = new Map(brief().evidence.map((ref) => [ref.ref, ref]));

    it("opens only web links and the pages behind the record", () => {
        expect(briefItemLink(item({ url: "javascript:alert(1)", status: "upcoming" }), "meetings", evidence)).toBeNull();
        expect(briefItemLink(item({ url: "https://meet.halcyon.example/room-1", status: "now" }), "meetings", evidence)).toEqual({ kind: "join", url: "https://meet.halcyon.example/room-1" });
        expect(briefItemLink(item({ refs: ["e2"] }), "learned", evidence)).toEqual({ kind: "memory", id: "mem-7" });
        expect(briefItemLink(item({ refs: ["e3"] }), "watching", evidence)).toEqual({ kind: "url", url: "https://wails.io/" });
        expect(briefItemLink(item({ refs: ["missing"] }), "moved", evidence)).toBeNull();
        // A producer moment can exist before it has ever appeared in chat.
        expect(briefItemLink(item({ momentId: "pending-moment", refs: [] }), "commitments", evidence)).toBeNull();
    });

});

describe("needsYou and nextMeetings", () => {
    it("picks blocked or reviewable one-time work and commitments, nothing else", () => {
        expect(needsYou(item({ kind: "one_time_work", status: "blocked" }))).toBe(true);
        expect(needsYou(item({ kind: "one_time_work", status: "needs_review" }))).toBe(true);
        expect(needsYou(item({ kind: "one_time_work", status: "in_progress" }))).toBe(false);
        expect(needsYou(item({ kind: "commitment", status: "active" }))).toBe(true);
        for (const kind of ["goal", "responsibility", "concern", "opportunity", ""]) {
            expect(needsYou(item({ kind, status: "active" }))).toBe(false);
        }
    });

    it("ignores invalid deadline zones and uses the reader's calendar as a fallback", () => {
        const purpose = item({ kind: "goal", dueAt: new Date(2026, 9, 15, 23).toISOString() });
        expect(needsYou(purpose, NOW, true, "invalid/timezone")).toBe(true);
        expect(needsYou({ ...purpose, dueAt: new Date(2026, 9, 16, 0).toISOString() }, NOW, true, "invalid/timezone")).toBe(false);
    });

    it("keeps the meetings still ahead, soonest first, three at most", () => {
        const at = (hour: number) => new Date(2026, 9, 15, hour, 0).toISOString();
        const items = [
            item({ id: "late", title: "Late", kind: "meeting", status: "upcoming", at: at(16) }),
            item({ id: "done", title: "Done", kind: "meeting", status: "done", at: at(8) }),
            item({ id: "shape", title: "Shape", kind: "day_shape" }),
            item({ id: "now", title: "Now", kind: "meeting", status: "now", at: at(9), endAt: at(10) }),
            item({ id: "soon", title: "Soon", kind: "meeting", status: "upcoming", at: at(11) }),
            item({ id: "later", title: "Later", kind: "meeting", status: "upcoming", at: at(14) }),
        ];
        expect(nextMeetings(items, NOW).map((m) => m.id)).toEqual(["now", "soon", "later"]);
    });

    it("expires old stored meetings without end times when their start passes", () => {
        const items = [
            item({ id: "old", kind: "meeting", status: "upcoming", at: new Date(2026, 9, 15, 8).toISOString() }),
            item({ id: "boundary", kind: "meeting", status: "now", at: NOW.toISOString() }),
            item({ id: "next", kind: "meeting", status: "upcoming", at: new Date(2026, 9, 15, 10).toISOString() }),
        ];
        expect(nextMeetings(items, NOW).map((m) => m.id)).toEqual(["next"]);
    });
});

describe("brief cards", () => {
    const action = (id: string, label: string): MobileBriefCard => ({ id, kind: "action", mode: "send", label, message: `${label}: the full request.`, refs: ["e1"] });
    const opener: MobileBriefCard = { id: "opener", kind: "opener", mode: "speak_first", message: "I'd start with the Citi payment. Want to begin there?", refs: ["e1"] };

    it.each([
        ["en", "Talk about this brief"],
        ["zh-CN", "聊聊这份简报"],
    ])("shows at most two answer buttons without Talk in %s", async (language, talk) => {
        await i18n.changeLanguage(language);
        const actions = handlers();
        const doc = brief({ conversationKey: "revision-1", cards: [action("offer-1", "Draft reply to Dana"), action("offer-2", "Set up Citi payment"), action("offer-3", "Plan Discovery"), opener] });
        renderSection({ view: view({ brief: doc }), handlers: actions, observationPaused: false });
        const block = within(screen.getByTestId("brief-conversation-actions"));
        expect(block.getAllByRole("button").map((button) => button.textContent)).toEqual(["Set up Citi payment", "Plan Discovery"]);
        fireEvent.click(block.getByRole("button", { name: "Set up Citi payment" }));
        // The card's own words go in the box, unsent; Today writes none.
        expectCardInBox(actions.onStartChat, doc, "offer-2", true);
        expect(block.queryByRole("button", { name: talk })).not.toBeInTheDocument();
        expect(actions.onOpenBriefConversation).not.toHaveBeenCalled();
        expect(actions.onStartChat).toHaveBeenCalledTimes(1);
    });

    it("shows no button for a card that is not complete, and no Talk button without the brief's text", () => {
        const doc = brief({ narrative: [], cards: [{ ...action("offer-1", ""), label: "" }, opener] });
        renderSection({ view: view({ brief: doc }), observationPaused: false });
        expect(screen.queryByTestId("brief-conversation-actions")).not.toBeInTheDocument();
    });

    it("ignores an older Ask completion after Today is reopened", async () => {
        const actions = handlers();
        let finishOld!: (opened: boolean) => void;
        let finishNew!: (opened: boolean) => void;
        actions.onAskAboutParagraph.mockImplementationOnce(() => new Promise((resolve) => { finishOld = resolve; }));
        actions.onAskAboutParagraph.mockImplementationOnce(() => new Promise((resolve) => { finishNew = resolve; }));
        const doc = brief({ narrative: [{ text: "The Atlas release is ready.", refs: ["e1"] }] });
        const props = { view: view({ brief: doc }), loading: false, make: "idle" as const, nextAllowedAt: "", onMakeNow: vi.fn(), handlers: actions, now: NOW, observationPaused: false };
        const rendered = render(<TodayBriefSection {...props} active />);
        fireEvent.click(screen.getByTestId("brief-ask-about-paragraph"));
        fireEvent.change(screen.getByPlaceholderText("Ask anything about this paragraph"), { target: { value: "What changed?" } });
        fireEvent.submit(screen.getByPlaceholderText("Ask anything about this paragraph").closest("form")!);
        rendered.rerender(<TodayBriefSection {...props} active={false} />);
        rendered.rerender(<TodayBriefSection {...props} active />);
        fireEvent.click(screen.getByTestId("brief-ask-about-paragraph"));
        fireEvent.change(screen.getByPlaceholderText("Ask anything about this paragraph"), { target: { value: "What changed?" } });
        fireEvent.submit(screen.getByPlaceholderText("Ask anything about this paragraph").closest("form")!);
        expect(actions.onAskAboutParagraph).toHaveBeenCalledTimes(2);
        await act(async () => finishOld(false));
        expect(screen.queryByText("Couldn't open the chat. Try again")).not.toBeInTheDocument();
        await act(async () => finishNew(true));
    });

    it("offers an inline retry when opening the chat fails", async () => {
        const actions = handlers();
        actions.onAskAboutParagraph.mockResolvedValueOnce(false).mockResolvedValueOnce(true);
        const doc = brief({ narrative: [{ text: "The Atlas release is ready.", refs: ["e1"] }] });
        renderSection({ view: view({ brief: doc }), handlers: actions, observationPaused: false });
        fireEvent.click(screen.getByTestId("brief-ask-about-paragraph"));
        const question = screen.getByPlaceholderText("Ask anything about this paragraph");
        fireEvent.change(question, { target: { value: "What changed?" } });
        fireEvent.submit(question.closest("form")!);
        const retry = await screen.findByRole("alert");
        expect(retry).toHaveTextContent("Couldn't open the chat. Try again");
        fireEvent.click(retry);
        await waitFor(() => expect(actions.onAskAboutParagraph).toHaveBeenCalledTimes(2));
        expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    });

    it("prepares nothing when Today opens: the click checks the card", async () => {
        renderSection({ view: view({ brief: brief({ cards: [action("offer-1", "Draft reply to Dana"), opener] }) }), observationPaused: false });
        await act(async () => {});
        expect(mocks.prepareDrafts).not.toHaveBeenCalled();
    });
});

describe("Today sections after the attention list", () => {

    it("leaves the age to the reason line, and says nothing for a card first seen today", () => {
        renderSection({ view: view({ brief: brief({ sections: { ...brief().sections,
            openLoops: [
                item({ id: "reason", kind: "commitment", title: "Run payroll", attentionReason: "No progress recorded since last Thursday.", since: new Date(2026, 9, 8).toISOString() }),
                item({ id: "fresh", kind: "goal", title: "New today", since: new Date(2026, 9, 15, 8).toISOString() }),
            ],
        } }) }) });
        expect(screen.queryByTestId("brief-open-since")).not.toBeInTheDocument();
    });
});

describe("Today brief while observation is paused", () => {
    it("keeps a closing answer without proposing a hold while paused", async () => {
        const entry = item({ id: "q4", key: "commitments.1234567890abcdef", title: "Q4 plan", refs: ["e1"] });
        const question = "Want me to find time for Q4 plan?";
        const doc = brief({ sections: { ...brief().sections, commitments: [entry] }, narrative: [{ text: question, refs: ["e1"] }], cards: [{ id: "later", kind: "action", mode: "send", label: "Not this week", message: "Not this week.", answerTo: question, refs: ["e1"] }] });
        renderSection({ view: view({ brief: doc }), handlers: { ...handlers(), onFindTime: vi.fn() }, observationPaused: true, dayShape: { ...dayShapeFixture, hold: null }, dayShapeDate: "2026-10-23" });
        expect(screen.getByRole("button", { name: "Not this week" })).toBeInTheDocument();
        await act(async () => {});
        expect(mocks.propose).not.toHaveBeenCalled();
    });

    it.each([
        ["en", "Talk about this brief", "Chat about this"],
        ["zh-CN", "聊聊这份简报", "聊聊这个"],
    ])("keeps brief answers available while observing is paused in %s", async (language, talk) => {
        await i18n.changeLanguage(language);
        const actions = handlers();
        renderSection({
            view: view({ brief: brief({
                cards: [{ id: "offer-1", kind: "action", mode: "send", label: "Plan the work", message: "Plan my work.", refs: ["e1"] },
                    { id: "opener", kind: "opener", mode: "speak_first", message: "I'd start with Dana. Want to begin there?", refs: ["e1"] },
                    { id: "item:loop-1.key", kind: "item", mode: "send", message: "Where do I stand on Run payroll?", refs: ["e1"], itemKey: "loop-1.key" }],
                sections: { ...brief().sections, openLoops: [item({ id: "loop-1", kind: "commitment", title: "Run payroll" })] },
            }), dailySummary: { text: "Local morning recap", at: NOW.toISOString() } }),
            handlers: actions,
            observationPaused: true,
        });
        await act(async () => {});
        expect(mocks.prepareDrafts).not.toHaveBeenCalled();
        fireEvent.click(screen.getByRole("button", { name: "Plan the work" }));
        expect(actions.onStartChat.mock.calls.at(-1)?.[2]).toEqual({ paused: true });
        expect(screen.queryByRole("button", { name: talk })).not.toBeInTheDocument();
        expect(screen.queryByTestId("brief-daily-summary")).not.toBeInTheDocument();
    });

    it("waits to show cards until it is known whether observation is paused", async () => {
        renderSection({ view: view({ brief: brief({ cards: [{ id: "offer-1", kind: "action", mode: "send", label: "Draft reply to Dana", message: "Draft my reply.", refs: ["e1"] }] }) }), observationPaused: null });
        await act(async () => {});
        expect(screen.queryByRole("button", { name: "Draft reply to Dana" })).not.toBeInTheDocument();
    });
});

describe("the goal check-in on Today (item 66 A3)", () => {
    const checkIn = { goalId: "discovery", eventId: "ev-1", title: "Discovery", dueAt: "2026-08-01T23:59:59Z" };
    const doc = () => brief({
        sections: {
            ...brief().sections,
            workingToward: [
                item({ id: "w-discovery", purposeId: "discovery", title: "Discovery", kind: "goal", refs: [] }),
                item({ id: "w-garden", purposeId: "garden", title: "Plant the garden", kind: "goal", refs: [] }),
            ],
        },
    });
    beforeEach(() => mocks.noteCheckIn.mockResolvedValue(undefined));

    // RC 1: the check-in keeps its own card between the brief and Your topics.
    it("asks once in its own card after the brief instead of listing the goal", () => {
        renderSection({ view: view({ brief: doc() }), goalCheckIn: checkIn });
        const card = screen.getByTestId("goal-checkin");
        expect(card).toHaveAccessibleName("Goal check-in");
        expect(card.textContent).toContain("Still working on Discovery?");
        expect(card.textContent).toContain("It was due Aug 1, 2026, and nothing has moved on it in over a month.");
        expect(within(card).getAllByRole("button").map((button) => button.textContent)).toEqual(["Keep", "Done", "Forget"]);
        expect(screen.getByTestId("brief-card").compareDocumentPosition(card) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
        expect(screen.queryByTestId("today-more")).not.toBeInTheDocument();
        expect(mocks.noteCheckIn).toHaveBeenCalledWith("discovery", "ev-1");
    });

    it("shows the check-in even when no goal is listed", () => {
        renderSection({ view: view({ brief: brief() }), goalCheckIn: checkIn });
        expect(screen.getByTestId("goal-checkin")).toBeTruthy();
        expect(screen.queryByTestId("brief-section-workingToward")).not.toBeInTheDocument();
    });

    it("Keep keeps the goal and asks Today to read the check-in again", async () => {
        mocks.moveGoal.mockResolvedValueOnce({ id: "discovery", eventId: "ev-2", state: "active", title: "Discovery" });
        const onAnswered = vi.fn();
        renderSection({ view: view({ brief: doc() }), goalCheckIn: checkIn, onGoalCheckInAnswered: onAnswered });
        await act(async () => { fireEvent.click(screen.getByRole("button", { name: "Keep" })); });
        expect(mocks.moveGoal).toHaveBeenCalledWith(expect.objectContaining({ goalId: "discovery", action: "keep", expectedEventId: "ev-1" }));
        expect(screen.getByText("Kept Discovery. Isoform won't ask about it again.")).toBeInTheDocument();
        expect(onAnswered).not.toHaveBeenCalled();
    });

    it("Forget hides the check-in with Undo", async () => {
        mocks.moveGoal.mockResolvedValueOnce({ id: "discovery", eventId: "ev-2", state: "dismissed", title: "Discovery" });
        renderSection({ view: view({ brief: doc() }), goalCheckIn: checkIn, onGoalCheckInAnswered: vi.fn() });
        await act(async () => { fireEvent.click(screen.getByRole("button", { name: "Forget" })); });
        expect(mocks.moveGoal).toHaveBeenCalledWith(expect.objectContaining({ goalId: "discovery", action: "forget", expectedEventId: "ev-1" }));
        expect(screen.getByText("Forgot Discovery. It won't come back on its own.")).toBeInTheDocument();
        expect(screen.getByRole("button", { name: "Undo" })).toBeInTheDocument();
    });
    it("keeps goal actions available when the move fails", async () => {
        mocks.moveGoal.mockRejectedValueOnce(new Error("offline"));
        renderSection({ view: view({ brief: doc() }), goalCheckIn: checkIn });
        fireEvent.click(screen.getByRole("button", { name: "Done" }));
        expect(await screen.findByText("Isoform couldn't change this goal right now. Try again in a moment.")).toBeInTheDocument();
        expect(screen.getByRole("button", { name: "Done" })).toBeEnabled();
    });

    it("undoes a completed goal from its inline receipt", async () => {
        mocks.moveGoal.mockResolvedValueOnce({ id: "discovery", eventId: "ev-2" }).mockResolvedValueOnce({ id: "discovery", eventId: "ev-3" });
        renderSection({ view: view({ brief: doc() }), goalCheckIn: checkIn, onGoalCheckInAnswered: vi.fn() });
        fireEvent.click(screen.getByRole("button", { name: "Done" }));
        expect(await screen.findByText("Marked done")).toBeInTheDocument();
        fireEvent.click(screen.getByRole("button", { name: "Undo" }));
        await waitFor(() => expect(mocks.moveGoal).toHaveBeenLastCalledWith(expect.objectContaining({ goalId: "discovery", expectedEventId: "ev-2", action: "restore" })));
    });

});

describe('noticed Activity navigation cards', () => {
    it.each([
        ['en', 'follow_up', 'Add follow-up'],
        ['en', 'routine', 'Make a routine'],
        ['zh-CN', 'follow_up', '添加跟进'],
        ['zh-CN', 'routine', '设为例行事务'],
    ] as const)('adds the suggestion in place and edits it in Activity: %s %s', async (lang, action, label) => {
        await i18n.changeLanguage(lang);
        const h = { ...handlers(), onOpenActivitySuggestion: vi.fn() };
        const target = { date: '2026-10-14', suggestionId: 'suggestion-1', action };
        const doc = brief({ cards: [{ id: 'offer-1', kind: 'action', mode: 'open_activity', label: 'Server label', message: '', refs: ['e1'], target }] });
        renderSection({ view: view({ brief: doc }), handlers: h, observationPaused: false });
        fireEvent.click(screen.getByRole('button', { name: label }));
        await waitFor(() => expect(mocks.addSchedule).toHaveBeenCalledWith(target));
        const edit = await screen.findByRole('button', { name: lang === 'en' ? 'Edit' : '编辑' });
        expect(h.onOpenActivitySuggestion).not.toHaveBeenCalled();
        fireEvent.click(edit);
        expect(h.onOpenActivitySuggestion).toHaveBeenCalledWith({ ...target, schedule: { id: 'job', revision: 'v1', proof: 'proof' } });
        expect(h.onStartChat).not.toHaveBeenCalled();
        expect(h.onOpenBriefConversation).not.toHaveBeenCalled();
        expect(screen.queryByRole('heading', { name: 'Isoform noticed' })).not.toBeInTheDocument();
    });

    it.each([
        ["follow_up", "Add follow-up"],
        ["routine", "Make a routine"],
    ] as const)("restores %s after observation resumes", (action, label) => {
        const target = { date: "2026-10-14", suggestionId: "suggestion-1", action };
        const doc = brief({ cards: [{ id: "activity", kind: "action", mode: "open_activity", label, message: "", refs: ["e1"], target }] });
        const props = { view: view({ brief: doc }), loading: false, make: "idle" as const, nextAllowedAt: "", onMakeNow: vi.fn(), handlers: { ...handlers(), onOpenActivitySuggestion: vi.fn() }, now: NOW };
        const { rerender } = render(<TodayBriefSection {...props} observationPaused={true} />);
        expect(screen.queryByRole("button", { name: label })).not.toBeInTheDocument();
        rerender(<TodayBriefSection {...props} observationPaused={false} />);
        expect(screen.getByRole("button", { name: label })).toBeInTheDocument();
    });

    it('hides navigation when observation is paused or the target is absent', () => {
        const card: MobileBriefCard = { id: 'offer-1', kind: 'action', mode: 'open_activity', label: 'Add follow-up', message: '', refs: ['e1'] };
        const { unmount } = render(<TodayBriefSection view={view({ brief: brief({ cards: [card] }) })} loading={false} make="idle" nextAllowedAt="" onMakeNow={vi.fn()} handlers={handlers()} observationPaused={false} />);
        expect(screen.queryByRole('button', { name: 'Add follow-up' })).not.toBeInTheDocument();
        unmount();
        renderSection({ view: view({ brief: brief({ cards: [{ ...card, target: { date: '2026-10-14', suggestionId: 'gone', action: 'follow_up' } }] }) }), observationPaused: true });
        expect(screen.queryByRole('button', { name: 'Add follow-up' })).not.toBeInTheDocument();
    });
});

describe("More from today (RC 1)", () => {
    const rowLabels = () => Array.from(document.querySelectorAll<HTMLElement>("[data-more-row]")).map((row) => row.dataset.moreRow);
    const fullBrief = () => brief({
        upcoming: [item({ id: "auto-1", title: "Inbox check", kind: "automation", at: new Date(2026, 9, 15, 11).toISOString() })],
        sections: {
            ...brief().sections,
            openLoops: [item({ id: "loop-1", title: "Approve the launch mail", kind: "one_time_work", status: "needs_review" })],
            meetings: [item({ id: "meet-1", title: "Design review", kind: "meeting", status: "upcoming", at: new Date(2026, 9, 15, 10).toISOString() })],
            workingToward: [item({ id: "goal-1", title: "Ship the companion", refs: [] })],
            commitments: [item({ id: "promise-1", title: "Send the deck", refs: [] })],
            moved: [item({ id: "moved-1", title: "Launch plan" })],
            people: [item({ id: "person-1", title: "Mira" })],
            learned: [item({ id: "learned-1", title: "Prefers mornings", refs: ["e2"] })],
        },
    });

    it("hides More from today when every section is empty", () => {
        renderSection({ view: view({ brief: brief() }) });
        expect(screen.queryByTestId("today-more")).not.toBeInTheDocument();
    });
});

describe("the brief's card (RC 1)", () => {
    it("gives lines within a stored paragraph the same gap as separate paragraphs", () => {
        const doc = brief({ narrative: [{ text: "First item.\nSecond item.", refs: ["e1"] }, { text: "Third item.", refs: ["e2"] }] });
        renderSection({ view: view({ brief: doc }), observationPaused: false });
        const narrative = screen.getByTestId("brief-narrative");
        const paragraphs = within(narrative).getAllByText(/^(First|Second|Third) item\.$/);
        expect(paragraphs).toHaveLength(3);
        expect(paragraphs[0].parentElement).toHaveClass("gap-4");
        expect(narrative).toHaveClass("gap-4");
    });
    it("shows the paragraphs, chip, audio and closing of a stored Monday morning brief", () => {
        const doc = brief({
            briefId: "2026-11-02-morning", localDate: "2026-11-02",
            narrative: [
                { text: "Answer the weekly invitation so the team knows your plans.", refs: ["e1"] },
                { text: "The launch plan is ready for review.", refs: ["e2"] },
                { text: "Would you like to review the invitation?", refs: ["e1"] },
            ],
            cards: [
                { id: "plan", kind: "question", mode: "send", paragraph: 1, label: "Review launch", message: "Review the launch plan.", refs: ["e2"] },
                { id: "review", kind: "action", mode: "send", label: "Review invitation", message: "Review the invitation.", answerTo: "closing-question-facts", refs: ["e1"] },
                { id: "later", kind: "action", mode: "send", label: "Not now", message: "Not now.", answerTo: "closing-question-facts", refs: ["e1"] },
            ],
            audio: { mime: "audio/mp4", bytes: 1200, durationSeconds: 70 },
            weekQuestion: { weekStart: "2026-11-02" },
        });
        renderSection({ view: view({ brief: doc }), observationPaused: false });
        expect(screen.getByTestId("brief-narrative")).toHaveTextContent("Answer the weekly invitation");
        expect(screen.getByTestId("brief-narrative")).toHaveTextContent("The launch plan is ready for review.");
        expect(screen.getByRole("button", { name: "Review launch" })).toBeVisible();
        expect(screen.getByTestId("brief-closing-question")).toHaveTextContent("Would you like to review the invitation?");
        expect(screen.getByTestId("brief-audio-toggle")).toBeVisible();
        expect(screen.getByLabelText("What is this week about?")).toBeVisible();
    });
    it("keeps a saved weekly answer visible when the one-shot question is no longer delivered", () => {
        const doc = brief({ narrative: [{ text: "The week is underway.", refs: ["e1"] }], weekQuestion: undefined });
        renderSection({ view: view({ brief: doc, weekAnswer: { weekStart: "2026-10-26", answer: "Launch" } }), observationPaused: false });
        expect(screen.getByText("You answered: Launch ·")).toBeInTheDocument();
        expect(screen.getByRole("button", { name: "Change" })).toBeInTheDocument();
    });
    const audio = { mime: "audio/mp4", bytes: 1200, durationSeconds: 70 };
    const answer = (id: string, label: string, message: string): MobileBriefCard => ({ id, kind: "action", mode: "send", label, message, refs: ["e1"], answerTo: "closing-question-facts" });
    const opener: MobileBriefCard = { id: "opener", kind: "opener", mode: "speak_first", message: "Want to start with Northwind?", refs: ["e1"] };
    const closingBrief = (overrides: Partial<MobileBriefDocument> = {}) => brief({
        narrative: [
            { text: "Northwind's countersignature is due before noon.", refs: ["e1"] },
            { text: "One question before you start: should I draft the Halcyon launch memo?", refs: ["e1"] },
        ],
        cards: [
            answer("hold", "Draft the memo", "Draft the Halcyon launch memo."),
            answer("leave", "Not this week", "Leave Thursday open this week. I will find time for the memo myself."),
            answer("third", "A third answer", "Never shown."),
            opener,
        ],
        ...overrides,
    });

    it.each(['en', 'zh-CN'])('shows no unrelated answer under an unanswered closing in %s', async (language) => {
        await i18n.changeLanguage(language);
        const doc = closingBrief({ cards: [
            { ...answer('prep', 'Prep for Deep work', 'Prepare Deep work.'), answerTo: undefined },
            { id: 'activity', kind: 'action', mode: 'open_activity', label: 'Unrelated activity', message: '', refs: ['e1'], target: { date: '2026-10-14', suggestionId: 's', action: 'follow_up' } },
            opener,
        ] });
        const { h } = renderSection({ view: view({ brief: doc }), observationPaused: false });
        expect(screen.queryByTestId('brief-closing-question')).not.toBeInTheDocument();
        expect(screen.queryByRole('button', { name: 'Prep for Deep work' })).not.toBeInTheDocument();
        expect(h.onStartChat).not.toHaveBeenCalled();
    });

    it("plays the card's own brief, with its time and length", () => {
        renderSection({ view: view({ brief: brief({ audio }) }) });
        const card = screen.getByTestId("brief-card");
        const play = within(card).getByTestId("brief-audio-toggle");
        expect(play).toHaveAccessibleName("Hear your brief");
        expect(play).toHaveAccessibleDescription("Morning brief · 7:00 AM · 1:10");
        expect(play).toHaveTextContent("Morning brief · 7:00 AM · 1:10");
        // The header no longer plays anything; the card is the only player.
        expect(screen.getAllByTestId("brief-audio-toggle")).toHaveLength(1);
    });

    it("offers no player for a brief without a stored recording", () => {
        renderSection({ view: view({ brief: brief({ audio: { mime: "audio/mp4", bytes: 0, durationSeconds: 0 } }) }) });
        expect(screen.queryByTestId("brief-audio-toggle")).not.toBeInTheDocument();
        expect(screen.getByTestId("brief-card")).toHaveTextContent("Morning brief · 7:00 AM");
    });

    it("offers no player for audio that is not ready", () => {
        renderSection({ view: view({ brief: brief({ audio: { ...audio, status: "downloading" } }) }) });
        expect(screen.queryByTestId("brief-audio-toggle")).not.toBeInTheDocument();
    });

    it("keeps the day's work below the first brief's card while it is made (today-review v1.1.11 #1)", () => {
        const snapshot = brief({ asOf: new Date(2026, 9, 15, 9, 0).toISOString(), narrative: [{ text: "You have two meetings and the release is close.", refs: ["e1"] }] });
        const first = view({ brief: snapshot, firstBrief: true, building: true, pendingSlot: "morning", pendingAt: new Date(2026, 9, 15, 7, 0).toISOString() });
        renderSection({ view: first });
        const card = screen.getByTestId("brief-pending");
        expect(card).toHaveTextContent("Your first brief");
        expect(card).toHaveTextContent("Making your morning brief now.");
        expect(within(card).queryByRole("button", { name: "Make my brief now" })).not.toBeInTheDocument();
        expect(screen.queryByTestId("brief-pending-last")).not.toBeInTheDocument();
        expect(screen.getByTestId("brief-narrative")).toHaveTextContent("You have two meetings and the release is close.");
        expect(card.compareDocumentPosition(screen.getByTestId("brief-narrative")) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    });

    it("offers Make my brief now on day one only when the first brief couldn't be made", () => {
        renderSection({ view: view({ firstBrief: true, pendingSlot: "morning", pendingAt: new Date(2026, 9, 15, 7, 0).toISOString() }) });
        const card = screen.getByTestId("brief-pending");
        expect(card).toHaveTextContent("Your first brief");
        expect(within(card).getByRole("button", { name: "Make my brief now" })).toBeInTheDocument();
    });

    it("shows one answer per offer, then Not now (today-review v1.1.8 #3)", () => {
        const doc = closingBrief({ cards: [
            answer("prep", "Prepare for it", "Help me prepare for the 1:1 with Jenny."),
            answer("reply", "Draft my reply", "Help me reply to Dana."),
            answer("not-now", "Not now", "Not now"),
            opener,
        ] });
        renderSection({ view: view({ brief: doc }), observationPaused: false });
        const closing = screen.getByTestId("brief-closing");
        expect(within(closing).getAllByRole("button").map((button) => button.textContent)).toEqual(["Prepare for it", "Draft my reply", "Not now"]);
    });

    it("ends the card on the closing question and at most two answers", () => {
        const doc = closingBrief();
        renderSection({ view: view({ brief: doc }), observationPaused: false });
        expect(screen.getByTestId("brief-narrative")).not.toHaveTextContent("should I draft the Halcyon launch memo");
        const closing = screen.getByTestId("brief-closing");
        expect(within(closing).getByTestId("brief-closing-question")).toHaveTextContent("should I draft the Halcyon launch memo");
        expect(screen.getByTestId("brief-narrative").compareDocumentPosition(closing) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
        expect(within(closing).getAllByRole("button").map((button) => button.textContent)).toEqual(["Draft the memo", "Not this week"]);
        expect(within(closing).queryByTestId("brief-closing-note")).not.toBeInTheDocument();
        fireEvent.click(within(closing).getByRole("button", { name: "Draft the memo" }));
        expect(within(closing).queryByTestId("brief-closing-note")).not.toBeInTheDocument();
    });

    it.each([["hold", "Draft the memo"], ["leave", "Not this week"]])("posts and folds the %s answer without filling the composer", async (id, label) => {
        const doc = closingBrief();
        const { h } = renderSection({ view: view({ brief: doc }), observationPaused: false });
        fireEvent.click(screen.getByRole("button", { name: label }));
        await waitFor(() => expect(mocks.postClosing).toHaveBeenCalledWith(doc.briefId, id, expect.any(String), ""));
        expect(await screen.findByText(`You answered: ${label} ·`)).toBeInTheDocument();
        expect(h.onStartChat).not.toHaveBeenCalled();
        expect(h.onOpenBriefConversation).not.toHaveBeenCalled();
        expect(h.onRunNow).not.toHaveBeenCalled();
    });

    it("opens the draft after posting a draft closing answer", async () => {
        const doc = closingBrief();
        const actions = { ...handlers(), onDraftBriefCard: vi.fn() };
        renderSection({ view: view({ brief: doc }), handlers: actions, observationPaused: false });
        fireEvent.click(screen.getByRole("button", { name: "Draft the memo" }));
        await waitFor(() => expect(actions.onDraftBriefCard).toHaveBeenCalledWith(doc.briefId, expect.objectContaining({ id: "hold" })));
        expect(await screen.findByText("You answered: Draft the memo ·")).toBeInTheDocument();
        expect(actions.onStartChat).not.toHaveBeenCalled();
    });

    it("changes a saved closing answer and reports a failed save", async () => {
        const doc = closingBrief();
        mocks.postClosing.mockRejectedValueOnce(new Error("offline")).mockResolvedValueOnce({ cardId: "leave", label: "Not this week", questionDigest: "closing-question-facts" });
        renderSection({ view: view({ brief: doc, closingAnswer: { cardId: "hold", label: "Draft the memo", questionDigest: "closing-question-facts" } }), observationPaused: false });
        fireEvent.click(screen.getByRole("button", { name: "Change" }));
        fireEvent.click(screen.getByRole("button", { name: "Not this week" }));
        expect(await screen.findByRole("alert")).toBeInTheDocument();
        fireEvent.click(screen.getByRole("button", { name: "Not this week" }));
        await waitFor(() => expect(mocks.postClosing).toHaveBeenLastCalledWith(doc.briefId, "leave", "Leave Thursday open this week. I will find time for the memo myself.", "hold"));
        expect(await screen.findByText("You answered: Not this week ·")).toBeInTheDocument();
    });

    describe("Your week", () => {
        const week = (): MobileBriefDocument => {
            const q4 = item({ id: "q4", key: "commitments:q4", kind: "commitment", title: "the Q4 plan", attentionReason: "Due Monday.", refs: ["e1"] });
            const doc = closingBrief({
                briefId: "2026-10-23-evening", slot: "evening", localDate: "2026-10-23",
                sections: { ...brief().sections, commitments: [q4] },
                weeklyRecap: [{ text: "A Halcyon week: most of your time went there.", refs: [] }],
                yourWeek: {
                    start: "2026-10-19", end: "2026-10-23", lead: "A Halcyon week: most of your time went there.",
                    time: [{ topicId: "t1", name: "Halcyon", hours: 9 }, { other: true, hours: 3 }],
                    open: [{ itemKey: "commitments:q4", cardId: "item:commitments:q4", title: "the Q4 plan", label: "Find time for the Q4 plan" }],
                    ask: { day: "2026-10-26", text: "Start next week on the Q4 plan?", message: "Help me plan how to start Monday on the Q4 plan. Don't write to my calendar.", cardId: "week:ask:commitments:q4" },
                },
            });
            return { ...doc, cards: [...(doc.cards ?? []),
                { id: "item:commitments:q4", kind: "item", mode: "send", message: "Where do I stand on the Q4 plan?", refs: ["e1"], itemKey: "commitments:q4" },
                { id: "week:ask:commitments:q4", kind: "item", mode: "send", message: "Help me plan how to start Monday on the Q4 plan. Don't write to my calendar.", refs: ["e1"] }] };
        };

        it("is the brief that evening: open, in place of the daily text and its closing question", () => {
            renderSection({ view: view({ brief: week() }), observationPaused: false });
            const card = screen.getByTestId("brief-card");
            const yourWeek = within(card).getByTestId("brief-your-week");
            expect(yourWeek.tagName).not.toBe("DETAILS");
            expect(within(yourWeek).getByRole("heading", { name: "Your week · Oct 19 to 23" })).toBeInTheDocument();
            expect(within(yourWeek).getByTestId("your-week-lead")).toHaveTextContent("A Halcyon week: most of your time went there.");
            expect(screen.queryByTestId("brief-narrative")).not.toBeInTheDocument();
            expect(screen.queryByTestId("brief-closing-question")).not.toBeInTheDocument();
            expect(screen.queryByText("Your week with Isoform")).not.toBeInTheDocument();
            expect(screen.queryByRole("button", { name: "Draft the memo" })).not.toBeInTheDocument();
        });

        it("keeps the correction receipt after the brief's weekly row is withdrawn by refresh", async () => {
            const doc = week();
            doc.yourWeek!.learned = [{ id: "memory:m1", kind: "memory", text: "Prefers mornings", memoryId: "m1" }];
            const props = { loading: false, make: "idle" as const, nextAllowedAt: "", onMakeNow: vi.fn(), handlers: handlers(), now: NOW, observationPaused: false };
            const rendered = render(<TodayBriefSection {...props} view={view({ brief: doc })} />);
            fireEvent.click(screen.getByRole("button", { name: "Not quite" }));
            fireEvent.click(screen.getByRole("button", { name: "Save" }));
            await screen.findByRole("button", { name: "Undo" });
            rendered.rerender(<TodayBriefSection {...props} view={view({ brief: { ...doc, yourWeek: undefined } })} />);
            fireEvent.click(screen.getByRole("button", { name: "Undo" }));
            await waitFor(() => expect(mocks.undoBelief).toHaveBeenCalledWith("opaque-receipt", expect.any(String)));
        });

        it("puts a chip's and the Monday answer's prepared words in the box through their cards", async () => {
            const doc = week();
            const { h } = renderSection({ view: view({ brief: doc }), observationPaused: false });
            fireEvent.click(screen.getByRole("button", { name: "Find time for the Q4 plan" }));
            expectCardInBox(h.onStartChat, doc, "item:commitments:q4", true);
            fireEvent.click(await screen.findByRole("button", { name: "Plan Mon" }));
            expectCardInBox(h.onStartChat, doc, "week:ask:commitments:q4", true);
            expect(h.onStartChat).toHaveBeenCalledTimes(2);
            expect(within(screen.getByTestId("your-week-ask")).queryByText("Ready in the box on the right.")).not.toBeInTheDocument();
        });

        it("turns off from its menu with an in-card undo", async () => {
            mocks.setKindEnabled.mockResolvedValue({});
            renderSection({ view: view({ brief: week() }), observationPaused: false });
            const trigger = screen.getByTestId("your-week-menu");
            fireEvent.pointerDown(trigger, { button: 0, pointerType: "mouse" });
            fireEvent.click(await screen.findByRole("menuitem", { name: "Turn off Your week" }));
            expect(await screen.findByText("Your week is off")).toBeInTheDocument();
            expect(mocks.setKindEnabled).toHaveBeenCalledWith("weekly_recap", false);
            fireEvent.click(screen.getByRole("button", { name: "Undo" }));
            await waitFor(() => expect(mocks.setKindEnabled).toHaveBeenCalledWith("weekly_recap", true));
        });

        it("is not shown while the brief is quiet", () => {
            renderSection({ view: view({ brief: week(), quiet: true }), observationPaused: false });
            expect(screen.queryByTestId("brief-your-week")).not.toBeInTheDocument();
        });
    });

    it("removes the legacy shape box and keeps privacy at the foot of the brief", () => {
        const doc = closingBrief({
            sections: { ...brief().sections, meetings: [item({ id: "shape", kind: "day_shape", title: "", detail: "Meetings at 10:30 AM and 2:00 PM." })] },
            limits: { itemsPerSection: 8, evidenceRefs: 60, truncatedSections: [], excludedSensitiveItems: 1 },
        });
        renderSection({ view: view({ brief: doc }), observationPaused: false });
        const card = screen.getByTestId("brief-card");
        expect(within(card).queryByText("The shape of today")).not.toBeInTheDocument();
        // The private-items line belongs to the brief it counts (v1.1.5 #4).
        expect(card.lastElementChild).toBe(within(card).getByTestId("brief-sensitive-excluded"));
        expect(screen.queryByTestId("today-quiet-footer")).not.toBeInTheDocument();
    });

    it.each([
        ["en", "Meetings at 9am, 12pm and 1:50pm; noon stays free."],
        ["en", "Meetings at 9:00 AM, 12:00 PM and 1:50 PM; noon stays free."],
        ["zh-CN", "Meetings at 9am, 12pm and 1:50pm; noon stays free."],
        ["zh-CN", "会议在9:00、12:00和13:50；中午有空。"],
    ])("renders supplied shape prose byte-for-byte when written in the brief in %s: %s", async (language, detail) => {
        await i18n.changeLanguage(language);
        // Feat replaced the legacy shape box with TodayDayStrip. Retained
        // shape fields stay intact; generated prose renders in the narrative.
        const doc = brief({
            narrative: [{ text: detail, refs: ['e1'] }],
            sections: { ...brief().sections, meetings: [item({ id: "shape", kind: "day_shape", title: "", detail })] },
        });
        renderSection({ view: view({ brief: doc }), observationPaused: false });
        const rendered = within(screen.getByTestId("brief-card")).getByText(detail);
        expect(rendered.textContent).toBe(doc.sections.meetings[0].detail);
        expect(doc.narrative[0].text).toBe(detail);
    });

    it("keeps the answers and omits the hidden-while-paused line", () => {
        renderSection({ view: view({ brief: closingBrief() }), observationPaused: true });
        expect(screen.getByRole("button", { name: "Draft the memo" })).toBeInTheDocument();
        expect(screen.getByTestId("brief-closing-question")).toBeInTheDocument();
        expect(screen.queryByTestId("brief-paused-cards")).not.toBeInTheDocument();
    });

    it.each(['en', 'zh-CN'])('shows one subject-specific chip per paragraph and prepares its full message in %s', async (language) => {
        await i18n.changeLanguage(language);
        const message = "Review the Atlas release notes.";
        const doc = brief({ narrative: [{ text: 'The Atlas release notes are ready.', refs: ['e1'] }],
            cards: [
                { id: 'q1', kind: 'question', mode: 'send', label: 'Review Atlas', message, refs: ['e1'], paragraph: 0 },
                { id: 'q2', kind: 'question', mode: 'send', label: ' review   atlas ', message, refs: ['e1'], paragraph: 0 },
                { id: 'q3', kind: 'question', mode: 'send', label: '', message, refs: ['e1'], paragraph: 0 },
            ] });
        const actions = handlers();
        renderSection({ view: view({ brief: doc }), handlers: actions, observationPaused: false });
        const extras = screen.getByTestId('brief-paragraph-extras');
        const chips = within(extras).getAllByTestId('brief-question-chip');
        expect(chips).toHaveLength(1);
        expect(chips[0]).toHaveTextContent('Review Atlas');
        fireEvent.click(chips[0]);
        expect(actions.onStartChat).toHaveBeenCalledWith(message, todayBriefCardRef(doc.briefId, 'q1', message));
    });

    it.each(["en", "zh-CN"])("hides a reply chip without a reply source in %s", async (language) => {
        await i18n.changeLanguage(language);
        const label = language === "en" ? "Draft a reply to Priya" : "起草给 Priya 的回复";
        const card = { id: "reply", kind: "item", mode: "send", label, message: "Reply to Priya", refs: ["e1"], paragraph: 0 };
        renderSection({ view: view({ brief: brief({ narrative: [{ text: "The memo is ready.", refs: ["e1"] }], cards: [card] }) }), handlers: { ...handlers(), onDraftBriefCard: vi.fn() }, observationPaused: false });
        expect(screen.queryByRole("button", { name: label })).not.toBeInTheDocument();
    });

    it.each(["en", "zh-CN"])("routes a sourced meeting prep chip to the meeting in %s", async (language) => {
        await i18n.changeLanguage(language);
        const label = language === "en" ? "Prep me for the Priya 1:1" : "准备 Priya 的 1:1 会议";
        const card = { id: "prep", kind: "question", mode: "send", label, message: "Prepare for Priya", refs: ["e1"], paragraph: 0 };
        const actions = { ...handlers(), onOpenDayMeeting: vi.fn(), onAskBriefCard: vi.fn() };
        renderSection({ view: view({ brief: brief({ narrative: [{ text: "Priya's meeting is today.", refs: ["e1"] }], cards: [card] }) }), handlers: actions, observationPaused: false });
        fireEvent.click(screen.getByRole("button", { name: label }));
        expect(actions.onOpenDayMeeting).toHaveBeenCalledWith("ev1");
        expect(actions.onAskBriefCard).not.toHaveBeenCalled();
    });

    it.each(["en", "zh-CN"])("opens prep through a person id with exactly one meeting still to come today in %s", async (language) => {
        await i18n.changeLanguage(language);
        mocks.prepForPerson.mockImplementation(async (person: string) => {
            if (person === "p-mei") return "ev-mei";
            throw new Error("meeting is no longer available");
        });
        const label = language === "en" ? "Prep me for 2 PM with Mei" : "准备下午 2 点和 Mei 的会议";
        const card = { id: "prep", kind: "question", mode: "send", label, message: "Prepare for Mei", refs: ["p1"], paragraph: 0 };
        const evidence = [{ ref: "p1", sourceKind: "principal", sourceId: "p-mei", label: "Mei", observedAt: "" }];
        const actions = { ...handlers(), onOpenDayMeeting: vi.fn(), onAskBriefCard: vi.fn() };
        renderSection({ view: view({ brief: brief({ narrative: [{ text: "Mei is on your calendar.", refs: ["p1"] }], evidence, cards: [card] }) }), handlers: actions, observationPaused: false });
        fireEvent.click(await screen.findByRole("button", { name: label }));
        expect(mocks.prepForPerson).toHaveBeenCalledWith("p-mei");
        expect(actions.onOpenDayMeeting).toHaveBeenCalledWith("ev-mei");
        expect(actions.onAskBriefCard).not.toHaveBeenCalled();
    });

    it.each([
        ["two meetings today", [{ ref: "p1", sourceKind: "principal", sourceId: "p-ana", label: "Ana", observedAt: "" }]],
        ["a name only", []],
        ["two people", [{ ref: "p1", sourceKind: "principal", sourceId: "p-mei", label: "Mei", observedAt: "" }, { ref: "p2", sourceKind: "principal", sourceId: "p-ana", label: "Ana", observedAt: "" }]],
    ])("withholds a Prep me chip with %s", async (_name, evidence) => {
        mocks.prepForPerson.mockImplementation(async (person: string) => {
            if (person === "p-mei") return "ev-mei";
            throw new Error("meeting is no longer available");
        });
        const card = { id: "prep", kind: "question", mode: "send", label: "Prep me for 2 PM with Ana", message: "Prepare for Ana", refs: evidence.map((ref) => ref.ref), paragraph: 0 };
        const actions = { ...handlers(), onOpenDayMeeting: vi.fn(), onAskBriefCard: vi.fn() };
        renderSection({ view: view({ brief: brief({ narrative: [{ text: "Ana is on your calendar.", refs: card.refs }], evidence, cards: [card] }) }), handlers: actions, observationPaused: false });
        await waitFor(() => expect(mocks.prepForPerson.mock.calls.length).toBe(evidence.length === 1 ? 1 : 0));
        await act(async () => { await Promise.resolve(); });
        expect(screen.queryByRole("button", { name: "Prep me for 2 PM with Ana" })).not.toBeInTheDocument();
        expect(actions.onOpenDayMeeting).not.toHaveBeenCalled();
    });

    it.each([
        ["en", "Find time for the memo on Friday", { first: "2026-10-16", last: "2026-10-16", minutes: 60 }],
        ["en", "Find time for the memo next week", { first: "2026-10-19", last: "2026-10-23", minutes: 60, nextWeek: true }],
        ["zh-CN", "为备忘录在周五留出时间", { first: "2026-10-16", last: "2026-10-16", minutes: 60 }],
    ])("asks the date-range availability query for a Find time chip naming a day or week in %s: %s", async (language, label, range) => {
        await i18n.changeLanguage(language);
        const card = { id: "find", kind: "question", mode: "send", label, message: label, refs: ["e1"], paragraph: 0, itemKey: "commitments:memo" };
        const actions = { ...handlers(), onFindTime: vi.fn(), onFindTimeRange: vi.fn(), onAskBriefCard: vi.fn() };
        renderSection({ view: view({ brief: brief({ narrative: [{ text: "The memo is due.", refs: ["e1"] }], cards: [card] }) }), handlers: actions, observationPaused: false });
        fireEvent.click(screen.getByRole("button", { name: label }));
        expect(actions.onFindTimeRange).toHaveBeenCalledWith(range);
        expect(actions.onFindTime).not.toHaveBeenCalled();
        expect(actions.onAskBriefCard).not.toHaveBeenCalled();
    });

    it.each(["en", "zh-CN"])("offers a new-message draft only when Go proves the address in %s", async (language) => {
        await i18n.changeLanguage(language);
        mocks.draftRoute.mockImplementation(async (ref: { sourceId: string }) => {
            if (ref.sourceId === "proven") return { personId: "p-priya", name: "Priya Raman", address: "priya@example.com", source: "mail", subject: "Q4 plan" };
            throw new Error("unavailable");
        });
        const proven = { id: "proven", kind: "item", mode: "send", label: language === "en" ? "Draft the update to Priya" : "起草给 Priya 的更新", message: "Draft the update for Priya.", refs: ["p1"], paragraph: 0 };
        const unproven = { id: "unproven", kind: "item", mode: "send", label: language === "en" ? "Draft the note to Sam" : "起草给 Sam 的说明", message: "Draft the note for Sam.", refs: ["p2"], paragraph: 1 };
        const evidence = [{ ref: "p1", sourceKind: "principal", sourceId: "p-priya", label: "Priya", observedAt: "" }, { ref: "p2", sourceKind: "principal", sourceId: "p-sam", label: "Sam", observedAt: "" }];
        const doc = brief({ narrative: [{ text: "Priya asked for the plan.", refs: ["p1"] }, { text: "Sam needs the note.", refs: ["p2"] }], evidence, cards: [proven, unproven] });
        const actions = { ...handlers(), onDraftBriefCard: vi.fn(), onAskBriefCard: vi.fn() };
        renderSection({ view: view({ brief: doc }), handlers: actions, observationPaused: false });
        fireEvent.click(await screen.findByRole("button", { name: proven.label }));
        expect(mocks.draftRoute).toHaveBeenCalledWith(expect.objectContaining({ sourceId: "proven", briefDraft: true }));
        expect(actions.onDraftBriefCard).toHaveBeenCalledWith(doc.briefId, proven, true);
        expect(screen.queryByRole("button", { name: unproven.label })).not.toBeInTheDocument();
        expect(actions.onAskBriefCard).not.toHaveBeenCalled();
    });

    it("opens a reply draft from a paragraph chip without posting a chat request", () => {
        const card = { id: "reply", kind: "item", mode: "send", label: "Draft the reply to Dana", message: "Dana is waiting on my reply. What do they need?", refs: ["e1"], paragraph: 0 };
        const doc = brief({ narrative: [{ text: "Dana is waiting.", refs: ["e1"] }], evidence: [{ ref: "e1", sourceKind: "brief_waiting_reply", sourceId: "dana", label: "Dana", observedAt: "" }], cards: [card] });
        const actions = { ...handlers(), onDraftBriefCard: vi.fn(), onAskBriefCard: vi.fn() };
        renderSection({ view: view({ brief: doc }), handlers: actions, observationPaused: false });
        fireEvent.click(screen.getByTestId("brief-question-chip"));
        expect(actions.onDraftBriefCard).toHaveBeenCalledWith(doc.briefId, card);
        expect(actions.onAskBriefCard).not.toHaveBeenCalled();
        expect(actions.onStartChat).not.toHaveBeenCalled();
    });

    it.each([
        ['en', 'Ask about this'],
        ['zh-CN', '问问这段'],
    ])('shows the localized Ask control only after body paragraphs in %s', async (language, label) => {
        await i18n.changeLanguage(language);
        const actions = handlers();
        const doc = brief({ narrative: [
            { text: 'The Atlas release is ready.', refs: ['e1'] },
            { text: 'Would you like to review it?', refs: ['e1'] },
        ] });
        renderSection({ view: view({ brief: doc }), handlers: actions, observationPaused: false });
        const ask = screen.getByTestId('brief-ask-about-paragraph');
        expect(ask).toHaveAccessibleName(label);
        expect(ask).toHaveAttribute('title', label);
        expect(ask).toHaveClass('size-7', 'rounded-full', 'right-0', 'top-0');
        expect(ask).toHaveClass('opacity-0', 'group-focus-within:opacity-100');
        expect(screen.getAllByTestId('brief-ask-about-paragraph')).toHaveLength(1);
        fireEvent.click(ask);
        const input = screen.getByRole('textbox');
        expect(input).toHaveFocus();
        expect(actions.onAskAboutParagraph).not.toHaveBeenCalled();
        fireEvent.change(input, { target: { value: 'What changed?' } });
        fireEvent.submit(input.closest('form')!);
        expect(actions.onAskAboutParagraph).toHaveBeenCalledWith(doc.briefId, 0, doc.narrative[0].text, 'What changed?');
    });

    it('hides Ask controls while observation is paused or its state is unknown', () => {
        const doc = brief({ narrative: [{ text: 'The Atlas release is ready.', refs: ['e1'] }] });
        renderSection({ view: view({ brief: doc }), observationPaused: true });
        expect(screen.queryByTestId('brief-ask-about-paragraph')).not.toBeInTheDocument();
        cleanup();
        renderSection({ view: view({ brief: doc }), observationPaused: null });
        expect(screen.queryByTestId('brief-ask-about-paragraph')).not.toBeInTheDocument();
    });

    it.each(['en', 'zh-CN'])('withholds masked prepared question chips from retained briefs in %s', async (language) => {
        await i18n.changeLanguage(language);
        const masks = ['[REDACTED:code]', '[masked]', '[...]'];
        const doc = brief({ narrative: [{ text: 'The Atlas release plan is ready.', refs: ['e1'] }],
            cards: masks.map((mask, index) => ({ id: `masked-${index}`, kind: 'question', mode: 'send',
                label: `Ask about ${mask}`, message: `Ask about ${mask}`, refs: ['e1'], paragraph: 0 })) });
        renderSection({ view: view({ brief: doc }), observationPaused: false });
        expect(screen.queryAllByTestId('brief-question-chip')).toHaveLength(0);
        for (const mask of masks) expect(screen.queryByText(`Ask about ${mask}`)).not.toBeInTheDocument();
    });

    it.each(['en', 'zh-CN'])('integrates verbatim delivery and whole-unit withholding with masked chips in %s', async (language) => {
        await i18n.changeLanguage(language);
        const normal = language === 'en' ? '  Atlas review starts at 9:00 AM.\nKeep the spacing.  ' : '  Atlas 评审在9:00开始。\n保留原来的空格。  ';
        const unsafe = 'A complete sentence.\nReply to Name?';
        const doc = brief({ narrative: [{ text: normal, refs: ['e1'] }, { text: unsafe, refs: ['e1'] }],
            cards: [
                { id: 'safe', kind: 'question', mode: 'send', label: 'Review Atlas', message: 'Review Atlas.', refs: ['e1'], paragraph: 0 },
                { id: 'mask', kind: 'question', mode: 'send', label: 'Ask about [masked]', message: 'Ask about [masked]', refs: ['e1'], paragraph: 0 },
                { id: 'unsafe-para', kind: 'question', mode: 'send', label: 'Review the notes', message: 'Review the notes.', refs: ['e1'], paragraph: 1 },
            ] });
        renderSection({ view: view({ brief: doc }), observationPaused: false });
        const narrative = screen.getByTestId('brief-narrative');
        expect(narrative.querySelector('p')?.textContent).toBe(doc.narrative[0].text);
        expect(narrative).not.toHaveTextContent('A complete sentence.');
        expect(screen.getAllByTestId('brief-question-chip')).toHaveLength(1);
        expect(screen.getByTestId('brief-question-chip')).toHaveTextContent('Review Atlas');
        expect(doc.narrative[1].text).toBe(unsafe);
        expect(doc.cards).toHaveLength(3);
    });

    it('keeps the shared 8px small Button radius on topic and question buttons', () => {
        const topic = 'Release checklist';
        const doc = brief({ narrative: [{ text: 'Ready to review.', refs: ['e1'], topicId: 'release', topicName: topic }],
            cards: [{ id: 'q1', kind: 'question', mode: 'send', label: 'Draft a reply', message: 'Draft a reply to Dana.', refs: ['e1'], paragraph: 0 }] });
        const actions = { ...handlers(), onOpenTopic: vi.fn() };
        renderSection({ view: view({ brief: doc }), handlers: actions, observationPaused: false });
        const extras = screen.getByTestId('brief-paragraph-extras');
        for (const button of within(extras).getAllByRole('button')) {
            expect(button).toHaveClass('rounded-lg');
            expect(button).not.toHaveClass('rounded-full');
            expect(button).not.toHaveClass('rounded-sm');
        }
        fireEvent.click(screen.getByRole('button', { name: topic }));
        expect(actions.onOpenTopic).toHaveBeenCalledWith('release');
    });

    it.each(['en', 'zh-CN'])('names a missing evening recording without a play slot in %s', async (language) => {
        await i18n.changeLanguage(language);
        renderSection({ view: view({ brief: brief({ slot: 'evening', asOf: '2026-10-22T20:05:00-07:00', timezone: 'America/Los_Angeles', audio: undefined }) }) });
        const caption = screen.getByTestId('brief-no-recording');
        expect(caption).toHaveTextContent(language === 'en' ? 'Evening brief · 8:05 PM' : '晚间简报 · 20:05');
        expect(caption).toHaveClass('text-muted');
        expect(screen.queryByTestId('brief-audio-toggle')).not.toBeInTheDocument();
        expect(caption.children).toHaveLength(0);
    });

    it("shows a paragraph's topic and questions only when the brief carries them", () => {
        const question = (id: string, paragraph: number, label: string): MobileBriefCard => ({ id, kind: "question", mode: "send", label, message: `${label} (asked)`, refs: ["e1"], paragraph });
        const doc = brief({
            narrative: [
                { text: "Halcyon wants the sync moved up to October 30.", refs: ["e1"], topicId: "topic-halcyon", topicName: "Halcyon ERP sync" },
                { text: "A plain paragraph.", refs: ["e1"] },
            ],
            cards: [question("q1", 0, "Review Halcyon"), question("q2", 0, "Make the reply warmer"), question("q3", 0, "A third question")],
        });
        const actions = { ...handlers(), onOpenTopic: vi.fn(), onPrepareInTopic: vi.fn(() => true) };
        renderSection({ view: view({ brief: doc }), handlers: actions, observationPaused: false });
        const extras = screen.getAllByTestId("brief-paragraph-extras");
        expect(extras).toHaveLength(1);
        expect(within(extras[0]).getAllByRole("button").map((button) => button.textContent)).toEqual(["Halcyon ERP sync", "Review Halcyon"]);
        fireEvent.click(screen.getByRole("button", { name: "Halcyon ERP sync" }));
        expect(actions.onOpenTopic).toHaveBeenCalledWith("topic-halcyon");
        fireEvent.click(within(extras[0]).getByRole("button", { name: "Review Halcyon" }));
        expect(actions.onPrepareInTopic).toHaveBeenCalledWith("topic-halcyon", "Review Halcyon (asked)");
        expect(actions.onStartChat).not.toHaveBeenCalled();
        expect(actions.onOpenBriefConversation).not.toHaveBeenCalled();
    });

    it("sends item chips from topic paragraphs through the regular card action", () => {
        const row = item({ id: "deliverable", key: "deliverable-key", title: "Atlas report", kind: "deliverable" });
        const linkedMessage = "Open the Atlas report and walk me through it.";
        const doc = brief({
            narrative: [{ text: "The Atlas report is ready.", refs: ["e1"], topicId: "topic-atlas", topicName: "Atlas project" }],
            sections: { ...brief().sections, deliverables: [row] },
            cards: [{
                id: "item:deliverable-key",
                kind: "item",
                mode: "send",
                label: "Open Atlas report",
                message: linkedMessage,
                refs: ["e1"],
                itemKey: "deliverable-key",
                paragraph: 0,
            }],
        });
        const actions = { ...handlers(), onPrepareInTopic: vi.fn(() => true), onOpenTopic: vi.fn() };
        renderSection({ view: view({ brief: doc }), handlers: actions, observationPaused: false });
        fireEvent.click(screen.getByRole("button", { name: "Open Atlas report" }));
        expect(actions.onStartChat).toHaveBeenCalledWith(
            linkedMessage,
            todayBriefCardRef(doc.briefId, "item:deliverable-key", linkedMessage),
        );
        expect(actions.onPrepareInTopic).not.toHaveBeenCalled();
    });

    it("caps and deduplicates paragraph chips, and linked items retain their card identity", () => {
        const row = item({ id: "deliverable", key: "deliverable-key", title: "Atlas report", kind: "deliverable" });
        const linkedMessage = "Open the Atlas report and walk me through it.";
        const doc = brief({
            narrative: ["First", "Second", "Third", "Fourth", "Fifth"].map((text) => ({ text, refs: ["e1"] })),
            sections: { moved: [], doing: [], watching: [], meetings: [], people: [], openLoops: [], learned: [], deliverables: [row], schedules: [] },
            cards: [
                { id: "q1", kind: "question", mode: "send", label: "Review Atlas", message: "Review Atlas.", refs: ["e1"], paragraph: 0 },
                { id: "q2", kind: "question", mode: "send", label: " review   atlas ", message: "Another request.", refs: ["e1"], paragraph: 0 },
                { id: "no-label", kind: "question", mode: "send", label: "", message: "No visible label.", refs: ["e1"], paragraph: 1 },
                { id: "item:deliverable-key", kind: "item", mode: "send", label: "Open Atlas report", message: linkedMessage, refs: ["e1"], itemKey: "deliverable-key", paragraph: 1 },
                { id: "q3", kind: "question", mode: "send", label: "open atlas report", message: "Open the same report.", refs: ["e1"], paragraph: 2 },
                { id: "q4", kind: "question", mode: "send", label: "Draft notes", message: "Draft the notes.", refs: ["e1"], paragraph: 3 },
                { id: "q5", kind: "question", mode: "send", label: "Prep tomorrow", message: "Prep for tomorrow.", refs: ["e1"], paragraph: 4 },
            ],
        });
        const actions = handlers();
        renderSection({ view: view({ brief: doc }), handlers: actions, observationPaused: false });
        expect(screen.getAllByTestId("brief-question-chip").map((chip) => chip.textContent)).toEqual([
            "Review Atlas", "Open Atlas report", "Draft notes",
        ]);
        fireEvent.click(screen.getByRole("button", { name: "Open Atlas report" }));
        expect(actions.onStartChat).toHaveBeenCalledWith(
            linkedMessage,
            todayBriefCardRef(doc.briefId, "item:deliverable-key", linkedMessage),
        );
    });

    it("shows no topic button without a way to open topics", () => {
        renderSection({ view: view({ brief: brief({ narrative: [{ text: "About Halcyon.", refs: [], topicId: "t", topicName: "Halcyon" }] }) }), observationPaused: false });
        expect(screen.queryByTestId("brief-paragraph-extras")).not.toBeInTheDocument();
    });
});

describe("the brief card's states (RC1-States)", () => {
    it("draws grey lines while the brief is read", () => {
        renderSection({ view: null, loading: true });
        const card = screen.getByTestId("brief-card-loading");
        expect(card).toHaveAttribute("aria-busy", "true");
        expect(card.children.length).toBeGreaterThanOrEqual(3);
    });

    it("shows grey lines under Making your brief, without questions or answers", () => {
        renderSection({ make: "making" });
        expect(screen.getByRole("status")).toHaveTextContent("Making your first brief from today's calendar…");
        expect(screen.queryByTestId("brief-closing")).not.toBeInTheDocument();
    });

    it("says it could not load the brief with an icon and Try again", () => {
        const onRetry = vi.fn();
        renderSection({ view: null, loadFailed: true, onRetry });
        const alert = screen.getByRole("alert");
        expect(alert).toHaveClass("text-copper");
        expect(alert.querySelector("svg")).toBeNull();
        expect(alert).toContainElement(screen.getByRole("button", { name: "Try again" }));
        fireEvent.click(screen.getByRole("button", { name: "Try again" }));
        expect(onRetry).toHaveBeenCalledTimes(1);
    });

    it("replaces the card's words, not the page, when Isoform cannot see", () => {
        renderSection({ view: view({ missing: ["calendars"], dailySummary: { text: "Recap", at: NOW.toISOString() } }) });
        expect(screen.getByTestId("today-brief-blind")).toBeInTheDocument();
        expect(screen.getByText(/Your evening brief comes/)).toBeInTheDocument();
        expect(screen.queryByRole("button", { name: "Today so far" })).not.toBeInTheDocument();
    });
});

// Desk round 1: the quiet actions are the design-system Button's "quiet"
// variant, never a Today-only class.
describe("Today's quiet actions are the design-system Button", () => {
    const button = (variant: "quiet" | "secondary") => cn(buttonVariants({ variant, size: "sm" })).split(" ");

    it("does not render Talk about this brief on the computer", () => {
        const opener: MobileBriefCard = { id: "opener", kind: "opener", mode: "speak_first", message: "Start with Dana?", refs: ["e1"] };
        renderSection({ view: view({ brief: brief({ cards: [opener] }) }), observationPaused: false });
        expect(screen.queryByTestId("brief-talk-button")).not.toBeInTheDocument();
    });

    it("renders the blind brief's single action as a secondary Button", () => {
        renderSection({ view: view({ missing: ["calendars"] }) });
        expect(screen.getByRole("button", { name: "Turn on" })).toHaveClass("text-accent");
        expect(screen.queryByRole("button", { name: "Set up in the chat" })).toBeNull();
    });

    it("renders the check-in's Keep and Done as secondary Buttons and Forget as the quiet one", () => {
        renderSection({ view: view({ brief: brief() }), goalCheckIn: { goalId: "g", eventId: "e", title: "Discovery", dueAt: "2026-08-01T23:59:59Z" } });
        const card = within(screen.getByTestId("goal-checkin"));
        expect(card.getByRole("button", { name: "Keep" })).toHaveClass(...button("secondary"));
        expect(card.getByRole("button", { name: "Done" })).toHaveClass(...button("secondary"));
        expect(card.getByRole("button", { name: "Forget" })).toHaveClass(...button("quiet"));
    });
});

// Desk round 1: what Isoform cannot see never hides another reason.
describe("Isoform cannot see, with other states", () => {
    it("still says scheduled briefs are off", () => {
        renderSection({ view: view({ enabled: false, missing: ["calendars"] }) });
        expect(screen.getByTestId("today-brief-blind")).toBeInTheDocument();
        expect(screen.getByText(/Scheduled briefs are off/)).toBeInTheDocument();
    });

    it("keeps what is missing on the card while a brief is being made, as before", () => {
        renderSection({ view: view({ missing: ["calendars"] }), make: "making" });
        expect(screen.getByRole("status")).toHaveTextContent("Making your first brief from today's calendar…");
        expect(screen.getByTestId("today-brief-blind")).toHaveTextContent("This brief only knows your screen.");
        expect(screen.queryByRole("button", { name: "Make my brief now" })).not.toBeInTheDocument();
    });
});

describe("brief hygiene quiet state", () => {
    it.each(["en", "zh-CN"])("renders real quiet counts and next clock without brief controls in %s", async (language) => {
        await i18n.changeLanguage(language);
        const quietView = { ...view({ brief: brief({ narrativeStatus: "quiet" }), empty: true }), quiet: true,
            quietSince: new Date(2026, 9, 14, 18).toISOString(), quietScreens: 24,
            nextAt: new Date(2026, 9, 16, 7).toISOString() } as TodayBriefView;
        renderSection({ view: quietView });
        const line = screen.getByTestId("brief-quiet");
        expect(line.textContent).toBe(language === "en"
            ? "Quiet since yesterday's brief: 24 screens, nothing new on your topics. Next brief at 7:00 AM."
            : "自昨天的简报以来很安静：24 个屏幕，关注的话题没有新动态。下次简报在7:00。");
        expect(within(line).queryByRole("button")).toBeNull();
        expect(screen.queryByTestId("brief-audio")).toBeNull();
        expect(screen.queryByText("You have two meetings and the release is close.")).toBeNull();
        expect(screen.queryByRole("button", { name: /Talk about|聊聊|Make my|立即生成/ })).toBeNull();
    });
});

it("does not show More from today during a quiet slot", () => {
    const doc = brief({ narrativeStatus: "quiet", sections: { ...brief().sections, moved: [item({ id: "quiet-work", title: "Halcyon launch proposal" })] } });
    renderSection({ view: { ...view({ brief: doc, empty: true }), quiet: true, quietScreens: 24 } });
    expect(screen.getByTestId("brief-quiet")).toBeInTheDocument();
    expect(screen.queryByTestId("today-more")).not.toBeInTheDocument();
    expect(screen.queryByText("Halcyon launch proposal")).not.toBeInTheDocument();
    expect(within(screen.getByTestId("brief-quiet")).queryByRole("button")).toBeNull();
});

it.each(['[REDACTE…', '[masked]', '[...]', 'Release [...]', 'Isoform Main chat'])('omits the unsafe topic chip %s from a brief paragraph', name => {
    const actions = { ...handlers(), onOpenTopic: vi.fn() };
    renderSection({ view: view({ brief: brief({ narrative: [{ text: 'Release work is ready.', refs: [], topicId: 'topic-release', topicName: name }] }) }), handlers: actions, observationPaused: false });
    expect(screen.queryByRole('button', { name })).not.toBeInTheDocument();
    expect(screen.getByText('Release work is ready.')).toBeInTheDocument();
});

describe('Today review F1 brief states', () => {
    it.each(['en', 'zh-CN'])('shows the counted privacy note only for exclusions in %s', async (language) => {
        await i18n.changeLanguage(language);
        const limits = { itemsPerSection: 8, evidenceRefs: 60, truncatedSections: [], excludedSensitiveItems: 2 };
        renderSection({ view: view({ brief: brief({ limits }) }) });
        expect(screen.getByTestId('brief-sensitive-excluded')).toHaveTextContent(language === 'en' ? '2 private items were left out of this brief.' : '有 2 项私人内容没有放进这份简报。');
    });
    it.each(['en', 'zh-CN'])('uses one solid permission action with content and a link when pending in %s', async (language) => {
        await i18n.changeLanguage(language);
        const turnOn = language === 'en' ? 'Turn on' : '开启';
        renderSection({ view: view({ brief: brief(), missing: ['screen_recording'] }) });
        expect(await screen.findByRole('button', { name: turnOn })).toHaveClass('bg-solid-btn-bg');
        expect(screen.getByTestId('brief-card')).toContainElement(screen.getByTestId('today-brief-blind'));
        cleanup();
        renderSection({ view: view({ missing: ['screen_recording'], pendingSlot: 'morning' }) });
        const pending = screen.getByTestId('brief-pending');
        expect(within(pending).getByRole('button', { name: turnOn })).toHaveClass('text-accent');
        expect(within(pending).getByRole('button', { name: language === 'en' ? 'Make my brief now' : '现在生成简报' })).toHaveClass('bg-solid-btn-bg');
    });
    it.each([['en', 'Not today', 'Not now'], ['zh-CN', '今天不做', '暂不']])('normalizes a legacy closing decline in %s', async (language, oldLabel, label) => {
        await i18n.changeLanguage(language);
        const doc = brief({ narrative: [{ text: 'Proceed?', refs: ['e1'] }], cards: [{ id: 'decline', kind: 'action', mode: 'send', label: oldLabel, message: 'No.', answerTo: 'Proceed?', refs: ['e1'] }] });
        renderSection({ view: view({ brief: doc }), observationPaused: false });
        expect(screen.getByRole('button', { name: label })).toBeInTheDocument();
        expect(screen.queryByRole('button', { name: oldLabel })).not.toBeInTheDocument();
    });
    it('keeps Ask in the right gutter without reserving another row', () => {
        const doc = brief({ cards: [{ id: 'q', kind: 'question', mode: 'send', label: 'Explain', message: 'Explain', paragraph: 0, refs: ['e1'] }] });
        renderSection({ view: view({ brief: doc }), observationPaused: false });
        const row = screen.getByTestId('brief-paragraph-extras');
        expect(row).not.toContainElement(screen.getByTestId('brief-ask-about-paragraph'));
        expect(row.parentElement).toHaveClass('pr-10');
        expect(screen.getByTestId('brief-ask-about-paragraph')).toHaveClass('absolute', 'opacity-0');
        expect(screen.getByTestId('brief-narrative')).toHaveClass('gap-4');
        fireEvent.click(screen.getByTestId('brief-ask-about-paragraph'));
        expect(mocks.recordToday).toHaveBeenCalledWith('today', 'T11', 'open', 'opened', expect.any(Number));
    });
    it('uses the same tokens for first-day and paragraph chips', () => {
        const doc = brief({ cards: [{ id: 'q', kind: 'question', mode: 'send', label: 'Explain', message: 'Explain', paragraph: 0, refs: ['e1'] }] });
        renderSection({ view: view({ brief: doc }), firstBrief: { chips: ['First'] }, handlers: { ...handlers(), onPrefillChat: vi.fn() }, observationPaused: false });
        for (const button of [screen.getByRole('button', { name: 'First' }), screen.getByTestId('brief-question-chip')]) expect(button).toHaveClass('border-chip-border', 'bg-chip-bg', 'text-ink');
        fireEvent.click(screen.getByRole('button', { name: 'First' }));
        expect(mocks.recordToday).toHaveBeenCalledWith('today', 'T15', 'ask', 'opened', expect.any(Number));
    });
});

it('separates a Latin person name from Chinese copy', () => {
    expect(i18n.getResource('zh-CN', 'today', 'beforeYouTalk.openPerson')).toBe('在「你认识的人」中打开 {{name}}');
});

it("withholds a weekly reply label when its card has no waiting-reply record", () => {
    const card = { id: "item:dan", kind: "item", mode: "send", message: "What should I do?", refs: ["e1"], itemKey: "dan" };
    const doc = brief({ yourWeek: { start: "2026-10-19", end: "2026-10-23", lead: "Your week.", open: [{ itemKey: "dan", cardId: card.id, title: "Dan", label: "Draft the reply to Dan" }] }, cards: [card] });
    renderSection({ view: view({ brief: doc }), observationPaused: false });
    expect(screen.queryByRole("button", { name: "Draft the reply to Dan" })).not.toBeInTheDocument();
});


it.each(["Write to Priya about the memo", "给 Priya 写邮件"])('withholds a new-message chip without a reply source: %s', (label) => {
    const card = { id: "new-message", kind: "item", mode: "send", label, message: "Write a message", refs: ["e1"], paragraph: 0 };
    renderSection({ view: view({ brief: brief({ narrative: [{ text: "The memo is ready.", refs: ["e1"] }], cards: [card] }) }), observationPaused: false });
    expect(screen.queryByRole("button", { name: label })).not.toBeInTheDocument();
});


it.each([
    ["en", "Nothing new since your 9:12 AM brief."],
    ["zh-CN", "自你 9:12 的简报以来没有新内容。"],
])("shows the last written brief and the empty request notice in %s", async (language, expected) => {
    await i18n.changeLanguage(language);
    const doc = { ...brief(), asOf: new Date(2026, 9, 15, 9, 12).toISOString(), narrativeStatus: "ok", narrative: [{ text: "A written brief.", refs: [] }] };
    renderSection({ view: view({ brief: doc, nothingNew: true }) });
    expect(screen.getByText(expected)).toBeInTheDocument();
    expect(screen.getByText("A written brief.")).toBeInTheDocument();
});
