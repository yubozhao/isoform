import { resetMainChatHeldActions } from '@/lib/main-chat-held-action';
import { resetPageQueries } from '@/lib/page-queries';
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { useEffect } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { chatThreadUpdates } from "@/lib/main-chat-view";
import enChat from "@/i18n/locales/en/chat.json";
import { TodayPage, phoneQuietOnToday } from "./TodayPage";
import { FEATURE_SWITCHES_TEST_KEY, resetFeatureSwitchesForTest } from "@/hooks/useFeatureSwitches";

/** Run with the chat switch set, then put the suite's switches back
 *  (test-setup.ts turns chat on for every test). */
async function withChat<T>(on: boolean, run: () => Promise<T> | T): Promise<T> {
    const store = globalThis as Record<string, unknown>;
    const previous = store[FEATURE_SWITCHES_TEST_KEY];
    store[FEATURE_SWITCHES_TEST_KEY] = { ...(previous as Record<string, unknown> | undefined), chat: on };
    resetFeatureSwitchesForTest();
    try { return await run(); } finally {
        store[FEATURE_SWITCHES_TEST_KEY] = previous;
        resetFeatureSwitchesForTest();
    }
}
import { todayNextBrief, dayShapeFixture } from "./today-next.fixture";
import { useMainChatItemPanel } from "@/hooks/useMainChatItemPanel";
import { resetMainChatWaitingStore } from "@/lib/main-chat-waiting-store";
import { type TodayPanelAnswer } from "@/lib/wails-shim-app";

// These cover the rollback switch: Isoform's updates back in Chat's thread.
beforeEach(() => { chatThreadUpdates.shown = true; });
afterEach(() => { chatThreadUpdates.shown = false; });


vi.mock("@/hooks/useFirstBriefSeen", () => ({ useFirstBriefSeen: () => ({ current: null }) }));
const mockUseGoalCheckIn = vi.fn((): { checkIn: unknown; refresh: () => void } => ({ checkIn: null, refresh: () => undefined }));
vi.mock("@/hooks/useGoalCheckIn", () => ({ useGoalCheckIn: () => mockUseGoalCheckIn() }));
const firstDayMock = vi.hoisted(() => vi.fn());
vi.mock("@/hooks/useFirstDay", async (importActual) => ({
    ...await importActual<typeof import("@/hooks/useFirstDay")>(),
    useFirstDay: () => firstDayMock(),
}));

const mockUseTodayNews = vi.fn();

vi.mock("@/hooks/useTodayNews", () => ({
    useTodayNews: (enabled: boolean) => mockUseTodayNews(enabled),
}));


const mockUseTodayStatus = vi.fn();
vi.mock("@/hooks/useTodayStatus", () => ({
    useTodayStatus: (...args: unknown[]) => mockUseTodayStatus(...args),
}));

const mockUseCaptureCounts = vi.fn();
vi.mock("@/hooks/useCaptureCounts", () => ({
    useCaptureCounts: (...args: unknown[]) => mockUseCaptureCounts(...args),
}));

const mockUseTodaySuggestion = vi.fn();
vi.mock("@/hooks/useTodaySuggestion", () => ({
    useTodaySuggestion: (...args: unknown[]) => mockUseTodaySuggestion(...args),
}));

const mockUseLatestBrief = vi.fn();
vi.mock("@/hooks/useLatestBrief", () => ({
    useLatestBrief: (...args: unknown[]) => mockUseLatestBrief(...args),
}));

const mockUseTodayDone = vi.fn();
vi.mock("@/hooks/useTodayDone", () => ({ useTodayDone: () => mockUseTodayDone() }));
vi.mock("./TodayDoneAnswerPanel", () => ({ TodayDoneAnswerPanel: ({ item }: { item: { id: string } }) => <div data-testid="done-answer-panel">{item.id}</div> }));

const mockUseTodayNeeds = vi.fn();
vi.mock("@/hooks/useTodayNeeds", () => ({ useTodayNeeds: () => mockUseTodayNeeds() }));
vi.mock("@/components/tasks/TaskThreadPane", () => ({ TaskThreadPane: ({ id }: { id: string }) => <div data-testid="needs-task-panel">{id}</div> }));

const mockUseTodayTopics = vi.fn();
vi.mock("@/hooks/useTodayTopics", () => ({
    useTodayTopics: (...args: unknown[]) => mockUseTodayTopics(...args),
}));

const mockUseTodayBeforeYouTalk = vi.fn();
vi.mock("@/hooks/useTodayBeforeYouTalk", () => ({
    useTodayBeforeYouTalk: (...args: unknown[]) => mockUseTodayBeforeYouTalk(...args),
}));

const mockUseTopicFinds = vi.fn();
vi.mock("@/hooks/useTopicFinds", () => ({
    useTopicFinds: (...args: unknown[]) => mockUseTopicFinds(...args),
}));

const pauseMocks = vi.hoisted(() => ({
    schedule: vi.fn(async (): Promise<unknown> => ({ enabled: false, morning: "07:00", evening: "18:00" })),
    trouble: vi.fn(async (): Promise<unknown> => ({ items: [] })),
    yourAgents: vi.fn(async (): Promise<unknown> => ({ agents: [] })),
    recordToday: vi.fn(async () => undefined),
    recordSection: vi.fn(async () => undefined),
    dayShape: vi.fn(async (_dateISO: string): Promise<unknown> => null),

    dayMeeting: vi.fn(async (_id: string): Promise<unknown> => null),
    needResolution: vi.fn(async (_id: string): Promise<unknown> => ({ kind: "" })),

    holdSettings: vi.fn(async (): Promise<unknown> => ({ granted: true, offerHolds: false, calendarId: "", calendars: [] })),
    proposeHold: vi.fn(),

    findTime: vi.fn(async () => [{ start: "2026-10-30T09:30:00Z", end: "2026-10-30T11:00:00Z" }]),
    todayTopics: vi.fn(),
    news: vi.fn(),
    generateNews: vi.fn(async () => undefined),
    noteCheckIn: vi.fn(async () => undefined),
    paused: vi.fn(async () => false),
    pauseSince: vi.fn(async () => 0),
    resumeObservation: vi.fn(async () => undefined),
    listeners: new Map<string, (value: unknown) => void>(),
    subscriptions: new Map<string, Set<(value: unknown) => void>>(),
    prepareDrafts: vi.fn(async (refs: unknown[]) => refs.map(() => ({ error: "failed" }))),
    paragraph: vi.fn(async () => ({ sourceKind: "brief", sourceId: "current-version", briefId: "b" })),
    savedAnswer: vi.fn(async (_context: unknown): Promise<null | TodayPanelAnswer> => null),
    topicRender: vi.fn(),
    listTopic: vi.fn(async (topicId: string) => ({ sessionId: "main", topicId, items: [], hasMore: false, nextCursor: "" })),
    sendTopic: vi.fn(),
    pin: vi.fn(async () => undefined),
    quiet: vi.fn(async () => [] as unknown[]),
    mainChat: vi.fn(async () => ({ projectId: "p", workspaceId: "w", session: { localId: "main-1" } })),
    mainPage: vi.fn(async (): Promise<unknown> => ({ items: [] as unknown[], hasMore: false, nextCursor: -1 })),
    overview: vi.fn(async (): Promise<unknown> => ({ waiting: 0 })),
    waiting: vi.fn(async (): Promise<unknown> => ({ sessionId: "main-1", count: 0, items: [], revision: 1, ready: true })),
    sendMain: vi.fn(async () => ({ sessionId: "main-1", userItemId: "u1", sortOrder: 1 })),
    sendMainCtx: vi.fn(async (_text: string, _ref: unknown) => ({ sessionId: "main-1", userItemId: "u2", sortOrder: 2 })),
    // The computer's current words for a card, as PrepareTodayChatDraft returns them.
    prepareDraft: vi.fn(async (ref: { sourceId?: string; generatedMessage?: string }): Promise<unknown> => ({
        prefill: { kind: "brief_card", title: `Card ${ref.sourceId ?? ""}` },
        counts: { memories: 1, activities: 2, people: 0 },
        fingerprint: `fp-${ref.sourceId ?? ""}`,
        message: ref.generatedMessage ?? "",
    })),
    chips: vi.fn(async () => ({})),
    resolveCard: vi.fn(async (_momentId: string, _action: string) => "acted"),
    loadProactive: vi.fn(async (_momentId: string): Promise<unknown> => null),
}));

vi.mock("@/lib/wails-shim-app", () => ({
    GetMobileBriefSchedule: pauseMocks.schedule,
    GetTrouble: pauseMocks.trouble,
    GetYourAgents: pauseMocks.yourAgents,
    ListPastBriefs: vi.fn().mockResolvedValue([{ briefId: "2026-10-21-evening", slot: "evening", localDate: "2026-10-21", asOf: "2026-10-21T18:00:00Z", narrativeStatus: "ok", lead: "Yesterday's lead." }]),
    GetAssistantSignOut: vi.fn().mockResolvedValue({ assistant: "", signedOut: false }),
    GetTodayYourWeekDeclined: vi.fn(async () => false),
    DeclineTodayYourWeek: vi.fn(async () => undefined),
    GetTodayTopics: pauseMocks.todayTopics,
    RefreshTodayTopics: vi.fn().mockResolvedValue(undefined),
    GetTodayDayShape: pauseMocks.dayShape,
    GetHoldDayShape: pauseMocks.dayShape,
    GetFindTimeDetails: async () => ({ ranges: await pauseMocks.findTime(), checkedCalendars: [], browserOnly: [] }),
    GetHoldSettings: pauseMocks.holdSettings,
    ProposeHold: pauseMocks.proposeHold,
    GetTodayNews: pauseMocks.news,
    GenerateTodayNews: pauseMocks.generateNews,
    NoteGoalCheckInShown: pauseMocks.noteCheckIn,
    ReopenMainChatSetupSteps: vi.fn(),
    GetUserTopics: vi.fn(async () => ({ topics: [], removed: [], enabled: true })),
    UpdateUserTopic: vi.fn(),
    SetTopicFindsEnabled: vi.fn(),
    IsClosedEyesModeEnabled: pauseMocks.paused,
    GetTodayObservationPauseSince: pauseMocks.pauseSince,
    ResumeObservation: pauseMocks.resumeObservation,
    GetTodayPermissionStates: vi.fn(async () => ({ screenRecording: "not_determined", calendars: "not_determined" })),
    GetTodayDayMeetingTarget: pauseMocks.dayMeeting,
    GetTodayNeedResolution: pauseMocks.needResolution,
    GetTodayNeedCheck: pauseMocks.needResolution,
    RequestTodayPermission: vi.fn(async () => true),
    OpenPermissionSettings: vi.fn(async () => undefined),
    PrepareTodayChatDrafts: pauseMocks.prepareDrafts,
    PrepareTodayBriefParagraph: pauseMocks.paragraph,
    GetTodayPanelAnswer: pauseMocks.savedAnswer,
    ListMainChatTopicItems: pauseMocks.listTopic,
    SendMainChatWithTopic: pauseMocks.sendTopic,
    PinTopicToSidebar: pauseMocks.pin,
    GetTodayQuietLines: pauseMocks.quiet,
    GetWatchedToday: vi.fn(async () => 0),
    GetMainChat: pauseMocks.mainChat,
    LoadStreamItemsPage: pauseMocks.mainPage,
    GetMainChatOverview: pauseMocks.overview,
    GetMainChatWaiting: pauseMocks.waiting,
    SendMainChat: pauseMocks.sendMain,
    SendMainChatFromTodayChip: pauseMocks.sendMain,
    SendMainChatWithAttachments: pauseMocks.sendMain,
    SendMainChatWithContext: pauseMocks.sendMainCtx,
    AskMainChat: pauseMocks.sendMain,
    AskMainChatFromTodayChip: pauseMocks.sendMain,
    AskMainChatWithContext: pauseMocks.sendMainCtx,
    PrepareTodayChatDraft: pauseMocks.prepareDraft,
    ResolveMainChatProactiveAction: pauseMocks.resolveCard,
    LoadMainChatProactiveItem: pauseMocks.loadProactive,
    RecordMainChatProactiveReceipt: vi.fn(async () => undefined),
    RecordTodayAction: pauseMocks.recordToday,
    RecordTodaySectionSeen: pauseMocks.recordSection,
    MarkTodayDoneOpened: vi.fn(async () => undefined),
    NativeClipboardSetText: vi.fn(async () => undefined),
    GetEngagementSettings: vi.fn(async () => ({ kinds: [] })),
    ListMainChatAnswerChips: pauseMocks.chips,
}));

vi.mock("@/hooks/useTopicChat", async (importActual) => {
    const actual = await importActual<typeof import("@/hooks/useTopicChat")>();
    return { useTopicChat: (topicId: string | null) => {
        pauseMocks.topicRender(topicId);
        return actual.useTopicChat(topicId);
    } };
});

const mockUseTranslation = vi.fn();
vi.mock("react-i18next", () => ({
    useTranslation: (...args: unknown[]) => mockUseTranslation(...args),
}));
// The main chat panel's row reader (stream-helpers) imports the app's i18n
// instance, which would set react-i18next up; the tests above mock that.
vi.mock("@/i18n", () => ({ default: { language: "en", resolvedLanguage: "en", t: (key: string) => key } }));

vi.mock("../../../wailsjs/runtime/runtime", () => ({
    BrowserOpenURL: vi.fn(),
    EventsOn: (name: string, callback: (value: unknown) => void) => {
        const set = pauseMocks.subscriptions.get(name) ?? new Set();
        set.add(callback);
        pauseMocks.subscriptions.set(name, set);
        pauseMocks.listeners.set(name, (value) => { for (const listener of set) listener(value); });
        return () => { set.delete(callback); if (!set.size) pauseMocks.listeners.delete(name); };
    },
}));

const cachedStory = {
    title: "Cached headline",
    summary: "Cached summary",
    source: "Example",
    url: "https://example.com/story",
};

const handlers = {
    onStartChat: vi.fn(),
    onRunNow: vi.fn(),
    onOpenMemory: vi.fn(),
    onOpenAutomations: vi.fn(),
    onOpenActivity: vi.fn(),
    onOpenPeople: vi.fn(),
    onOpenTasks: vi.fn(),
};

function ClearMainChatItemPanel() {
    const chat = useMainChatItemPanel("chat");
    const today = useMainChatItemPanel("today");
    useEffect(() => {
        chat.close();
        today.close();
    }, [chat.close, today.close]);
    return null;
}

/** RC 1 folds Daily news and the brief's sections under "More from today";
 *  these checks open every row first. The folded state has its own tests. */
function renderToday(
    actions: typeof handlers & Record<string, unknown> = handlers,
    props: Partial<Parameters<typeof TodayPage>[0]> = {},
) {
    const result = render(<TodayPage handlers={actions} {...props} />);
    for (const row of document.querySelectorAll<HTMLButtonElement>("[data-more-row] > div > button[aria-expanded='false']")) {
        fireEvent.click(row);
    }
    return result;
}

beforeEach(() => {
    // A brief shows in full only on its own day (R52): read the fixtures on it.
    vi.useFakeTimers({ toFake: ["Date"] });
    vi.setSystemTime(new Date(todayNextBrief.asOf));
    resetMainChatHeldActions();
    firstDayMock.mockReturnValue({ day: null, firstDays: false, history: null, calendar: null });
    resetMainChatWaitingStore();
    const resetPanel = render(<ClearMainChatItemPanel />);
    resetPanel.unmount();
    mockUseGoalCheckIn.mockReturnValue({ checkIn: null, refresh: () => undefined });
    pauseMocks.noteCheckIn.mockClear();
    resetPageQueries();
    vi.clearAllMocks();
    pauseMocks.savedAnswer.mockResolvedValue(null);
    pauseMocks.waiting.mockResolvedValue({ sessionId: "main-1", count: 0, items: [], revision: 1, ready: true });
    mockUseTranslation.mockImplementation((namespace: string) => ({
        t: (key: string, options?: Record<string, unknown>) => {
            if (namespace !== "chat") return key;
            const value = key.split(".").reduce<unknown>(
                (current, part) => current && typeof current === "object"
                    ? (current as Record<string, unknown>)[part]
                    : undefined,
                enChat as Record<string, unknown>,
            );
            return typeof value === "string"
                ? value.replace(/\{\{(\w+)\}\}/g, (_, name: string) => String(options?.[name] ?? `{{${name}}}`))
                : key;
        },
        i18n: { language: "en", resolvedLanguage: "en" },
    }));
    mockUseTodayStatus.mockReturnValue({
        week: null,
        brief: null,
        loading: false,
        refresh: vi.fn(),
    });
    mockUseTodaySuggestion.mockReturnValue({
        suggestion: null,
        busy: false,
        failed: false,
        resolve: vi.fn(),
        refresh: vi.fn(),
    });
    mockUseCaptureCounts.mockReturnValue({ counts: null, loadFailed: false, refresh: vi.fn() });
    mockUseLatestBrief.mockReturnValue({
        view: { brief: null, building: false, enabled: true, nextAt: "", nextSlot: "", empty: false, missing: [] },
        loading: false,
        loadFailed: false,
        make: "idle",
        nextAllowedAt: "",
        makeNow: vi.fn(),
        refresh: vi.fn(),
    });
    mockUseTodayNeeds.mockReturnValue({ items: [], loading: false, failed: false });
    mockUseTodayDone.mockReturnValue({ items: [], loading: false, failed: false });
    mockUseTodayTopics.mockReturnValue({ topics: [], status: "unavailable", refresh: vi.fn() });
    mockUseTodayBeforeYouTalk.mockReturnValue({ card: null, dismiss: vi.fn() });
    mockUseTopicFinds.mockReturnValue({
        view: { enabled: true, status: "none", items: [], topics: [] },
        loading: false,
        loadFailed: false,
        failure: null,
        findNew: vi.fn(),
        feedback: vi.fn(),
        refresh: vi.fn(),
    });
});

afterEach(() => { vi.useRealTimers(); pauseMocks.dayShape.mockResolvedValue(null); pauseMocks.dayMeeting.mockResolvedValue(null); });

describe("TodayPage brief and finds", () => {
    it("does not load the retired news or suggestion surfaces", () => {
        mockUseTodayNews.mockReturnValue({ data: { date: "2026-10-16", status: "ready", items: [cachedStory], updatedAt: 100 }, loading: false, regenerate: vi.fn() });
        mockUseTodaySuggestion.mockReturnValue({ suggestion: { id: "old", title: "Legacy suggestion" }, busy: false, failed: false, resolve: vi.fn(), refresh: vi.fn() });
        renderToday();
        expect(mockUseTodayNews).not.toHaveBeenCalled();
        expect(mockUseTodaySuggestion).not.toHaveBeenCalled();
        expect(screen.queryByTestId("today-more-news")).not.toBeInTheDocument();
        expect(screen.queryByText("Cached headline")).not.toBeInTheDocument();
        expect(screen.queryByText("Legacy suggestion")).not.toBeInTheDocument();
    });

    it("opens the requested source brief and scrolls Your Week into view", () => {
        const sourceBriefId = "2026-10-23-evening";
        const scrollIntoView = vi.fn();
        const originalScrollIntoView = Element.prototype.scrollIntoView;
        Object.defineProperty(Element.prototype, "scrollIntoView", { configurable: true, value: scrollIntoView });
        mockUseTodayNews.mockReturnValue({
            data: { date: "2026-10-23", status: "none", items: [], updatedAt: 0 },
            loading: false,
            regenerate: vi.fn(),
        });
        mockUseLatestBrief.mockReturnValue({
            view: {
                brief: {
                    ...todayNextBrief,
                    briefId: sourceBriefId,
                    localDate: "2026-10-23",
                    slot: "evening",
                    displaySlot: "evening",
                    yourWeek: { start: "2026-10-19", end: "2026-10-23", lead: "Your week in review." },
                },
                building: false,
                enabled: true,
                nextAt: "",
                nextSlot: "",
                empty: false,
                missing: [],
            },
            loading: false,
            loadFailed: false,
            make: "idle",
            nextAllowedAt: "",
            makeNow: vi.fn(),
            refresh: vi.fn(),
        });
        try {
            renderToday(handlers, { active: true, briefRequest: { briefId: sourceBriefId, nonce: 1 } });
            expect(mockUseLatestBrief).toHaveBeenCalledWith(true, sourceBriefId, 1);
            const yourWeek = screen.getByTestId("your-week-section");
            expect(yourWeek).toBeVisible();
            expect(within(yourWeek).getByText("Your week in review.")).toBeVisible();
            expect(scrollIntoView).toHaveBeenCalledWith({ behavior: "smooth", block: "start" });
        } finally {
            if (originalScrollIntoView) {
                Object.defineProperty(Element.prototype, "scrollIntoView", { configurable: true, value: originalScrollIntoView });
            } else {
                Reflect.deleteProperty(Element.prototype, "scrollIntoView");
            }
        }
    });

    // 2026-10-27: at 10 AM the header read "Evening brief" from a snapshot
    // made at 6:10 AM while the morning brief was still on its way.
    it("names the brief on its way in the header, not the older brief below it", () => {
        mockUseTodayNews.mockReturnValue({ data: { date: "2026-10-27", status: "none", items: [], updatedAt: 0 }, loading: false, regenerate: vi.fn() });
        mockUseLatestBrief.mockReturnValue({
            view: {
                brief: { ...todayNextBrief, briefId: "2026-10-27-on_request-061052", slot: "on_request", displaySlot: undefined, narrative: [], narrativeStatus: "unavailable", asOf: new Date(2026, 9, 27, 6, 10).toISOString() },
                building: false, enabled: true, nextAt: "", nextSlot: "", empty: false, missing: [],
                pendingSlot: "morning", pendingAt: new Date(2026, 9, 27, 7, 0).toISOString(),
            },
            loading: false, loadFailed: false, make: "idle", nextAllowedAt: "", makeNow: vi.fn(), refresh: vi.fn(),
        });
        renderToday(handlers, { active: true });
        expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent("ctoday.title");
        expect(screen.getByTestId("brief-pending")).toBeInTheDocument();
        // v1.1.5 #1: the earlier brief is never shown as the brief.
        expect(screen.queryByTestId("brief-card")).not.toBeInTheDocument();
    });

    it("acts at once on a missed brief's Show: locates the brief and starts the one on its way", () => {
        mockUseTodayNews.mockReturnValue({ data: { date: "2026-10-27", status: "none", items: [], updatedAt: 0 }, loading: false, regenerate: vi.fn() });
        const scrollIntoView = vi.fn();
        const originalScrollIntoView = Element.prototype.scrollIntoView;
        Object.defineProperty(Element.prototype, "scrollIntoView", { configurable: true, value: scrollIntoView });
        const makeNow = vi.fn(async () => undefined);
        mockUseLatestBrief.mockReturnValue({
            view: { brief: null, building: false, enabled: true, nextAt: "", nextSlot: "", empty: false, missing: [], pendingSlot: "morning" },
            loading: false, loadFailed: false, make: "idle", nextAllowedAt: "", makeNow, refresh: vi.fn(),
        });
        try {
            const { rerender } = renderToday(handlers, { active: true, briefRequest: { focus: "status", nonce: 1 } });
            expect(makeNow).toHaveBeenCalledTimes(1);
            expect(scrollIntoView).toHaveBeenCalledWith({ behavior: "smooth", block: "start" });
            expect(screen.getByTestId("today-brief-located")).toHaveClass("activity-card-locate");
            // The same request never starts a second brief.
            rerender(<TodayPage handlers={handlers} active briefRequest={{ focus: "status", nonce: 1 }} />);
            expect(makeNow).toHaveBeenCalledTimes(1);
        } finally {
            if (originalScrollIntoView) {
                Object.defineProperty(Element.prototype, "scrollIntoView", { configurable: true, value: originalScrollIntoView });
            } else {
                Reflect.deleteProperty(Element.prototype, "scrollIntoView");
            }
        }
    });

    it("only locates the brief on a missed brief's Show when nothing is on its way", () => {
        mockUseTodayNews.mockReturnValue({ data: { date: "2026-10-27", status: "none", items: [], updatedAt: 0 }, loading: false, regenerate: vi.fn() });
        const makeNow = vi.fn(async () => undefined);
        mockUseLatestBrief.mockReturnValue({
            view: { brief: null, building: false, enabled: true, nextAt: "", nextSlot: "", empty: false, missing: [] },
            loading: false, loadFailed: false, make: "idle", nextAllowedAt: "", makeNow, refresh: vi.fn(),
        });
        const originalScrollIntoView = Element.prototype.scrollIntoView;
        Object.defineProperty(Element.prototype, "scrollIntoView", { configurable: true, value: vi.fn() });
        try {
            renderToday(handlers, { active: true, briefRequest: { focus: "status", nonce: 1 } });
            expect(makeNow).not.toHaveBeenCalled();
            expect(screen.getByTestId("today-brief-located")).toBeInTheDocument();
        } finally {
            if (originalScrollIntoView) {
                Object.defineProperty(Element.prototype, "scrollIntoView", { configurable: true, value: originalScrollIntoView });
            } else {
                Reflect.deleteProperty(Element.prototype, "scrollIntoView");
            }
        }
    });


});

describe("TodayPage status lines", () => {
    it("shows brief health under the greeting, never the weekly line (it lives in Settings)", () => {
        mockUseTodayNews.mockReturnValue({
            data: { date: "2026-07-20", status: "none", items: [], updatedAt: 0 },
            loading: false,
            regenerate: vi.fn(),
        });
        mockUseTodayStatus.mockReturnValue({
            week: { available: true, sent: 9, actedOn: 6, days: 7, kinds: [] },
            brief: {
                enabled: true,
                lastRunAt: new Date().toISOString(),
                lastSlot: "morning",
                nextRunAt: "",
                nextSlot: "",
                missedRuns: 1,
                failingSlot: "",
                failedAttempts: 0,
            },
            loading: false,
            refresh: vi.fn(),
        });

        renderToday();
        const status = screen.getByTestId("today-status");
        expect(status).not.toHaveTextContent("status.spokeUp");
        expect(status).not.toHaveTextContent("status.lastBrief");
        expect(status).not.toHaveTextContent("status.missedThisWeek");
    });

    it("keeps only the date in the meta line while status is unknown", () => {
        mockUseTodayNews.mockReturnValue({
            data: { date: "2026-07-20", status: "none", items: [], updatedAt: 0 },
            loading: false,
            regenerate: vi.fn(),
        });
        renderToday();
        expect(screen.getByTestId("today-status").children).toHaveLength(1);
    });
});

describe("TodayPage quiet lines (RC 1 A4)", () => {
    it("adds one muted line per quiet kind under the status lines, with no buttons", async () => {
        mockUseTodayNews.mockReturnValue({ data: { date: "2026-07-20", status: "none", items: [], updatedAt: 0 }, loading: false, regenerate: vi.fn() });
        pauseMocks.quiet.mockResolvedValueOnce([
            { kind: "capture_paused", firstAt: "2026-10-21T15:40:00Z", lastAt: "2026-10-21T15:55:00Z", count: 0, intervals: [{ start: "2026-10-21T15:40:00Z", end: "2026-10-21T15:55:00Z" }] },
        ]);
        renderToday();
        const line = await screen.findByTestId("today-quiet-line");
        expect(screen.getByTestId("today-quiet-footer")).toContainElement(line);
        expect(line).toHaveTextContent("quiet.withIntervals");
        expect(within(line).queryByRole("button")).not.toBeInTheDocument();
    });

    it("shows none when nothing was quieted today", async () => {
        mockUseTodayNews.mockReturnValue({ data: { date: "2026-07-20", status: "none", items: [], updatedAt: 0 }, loading: false, regenerate: vi.fn() });
        renderToday();
        await act(async () => {});
        expect(screen.queryByTestId("today-quiet-line")).not.toBeInTheDocument();
    });
});

// Snappy 1.0 (section 4): "Today Isoform saw N screens" moved to Activity.
// Today neither shows nor reads the counts; the header keeps the date and
// the brief times.
describe("TodayPage capture line", () => {
    it("neither shows nor reads what Isoform captured today", () => {
        mockUseTodayNews.mockReturnValue({
            data: { date: "2026-07-20", status: "none", items: [], updatedAt: 0 },
            loading: false,
            regenerate: vi.fn(),
        });
        mockUseCaptureCounts.mockReturnValue({
            counts: {
                paused: false,
                notReady: false,
                todayStart: 0,
                weekStart: 0,
                sources: [
                    { source: "screens", today: 412, week: 900 },
                    { source: "audioMinutes", today: 38, week: 90 },
                ],
            },
            loadFailed: false,
            refresh: vi.fn(),
        });
        mockUseCaptureCounts.mockClear();
        const onOpenCaptureCounts = vi.fn();
        renderToday({ ...handlers, onOpenCaptureCounts });

        expect(screen.queryByTestId("today-capture")).not.toBeInTheDocument();
        expect(screen.queryByText("captured.screensAndAudio")).not.toBeInTheDocument();
        expect(mockUseCaptureCounts).not.toHaveBeenCalled();
        expect(screen.getByTestId("today-status")).toBeInTheDocument();
    });
});

describe("TodayPage topic panel (RC 1 part 2)", () => {
    // Today review v1.1.7 requires a sourced line on every shown card.
    const topics = [
        { id: "pipeline", name: "Pipeline", kind: "work", status: "moving", pinned: false },
        { id: "halcyon", name: "Halcyon ERP sync", kind: "work", status: "waiting", people: { names: ["Dana"], count: 1 }, pinned: false },
        { id: "halcyon-launch", name: "Halcyon launch memo", kind: "work", status: "today", pinned: false },
    ].map((topic) => ({ ...topic, latest: { text: "Active", at: "2026-10-21T15:00:00Z" } }));
    const briefWithTopics = () => ({
        version: 1, briefId: "2026-10-21-morning", slot: "morning", localDate: "2026-10-21",
        asOf: "2026-10-21T07:42:00Z", coldStart: false, narrativeStatus: "ok", dayShape: null, evidence: [],
        narrative: [{ text: "Halcyon's launch memo is due Tuesday.", refs: [], topicId: "halcyon-launch", topicName: "Halcyon launch memo" }],
        cards: [{ id: "q1", kind: "question", mode: "send", label: "What can move off Tuesday?", message: "What can move off Tuesday?", refs: [], paragraph: 0 }],
        sections: { moved: [], doing: [], watching: [], meetings: [], people: [], openLoops: [], learned: [], deliverables: [], schedules: [] },
    });
    let measured: { mockRestore: () => void } | null = null;
    const width = (px: number) => {
        measured = vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockReturnValue({ width: px, height: 800, top: 0, left: 0, right: px, bottom: 800, x: 0, y: 0, toJSON: () => ({}) } as DOMRect);
    };
    afterEach(() => {
        measured?.mockRestore();
        measured = null;
    });

    beforeEach(() => {
        vi.setSystemTime(new Date(briefWithTopics().asOf));
        mockUseTodayNews.mockReturnValue({ data: { date: "2026-10-21", status: "none", items: [], updatedAt: 0 }, loading: false, regenerate: vi.fn() });
        mockUseTodayTopics.mockReturnValue({ topics, status: "ready", refresh: vi.fn() });
        mockUseLatestBrief.mockReturnValue({
            view: { brief: briefWithTopics(), building: false, enabled: true, nextAt: "", nextSlot: "", empty: false, missing: [] },
            loading: false, loadFailed: false, make: "idle", nextAllowedAt: "", makeNow: vi.fn(), refresh: vi.fn(),
        });
    });

    it("keeps the rest of Today when Before you talk is empty, and draws no Your topics on Today (computer-map rule 10)", async () => {
        width(1200);
        render(<TodayPage handlers={handlers} />);
        await act(async () => {});
        expect(screen.queryByTestId("before-you-talk")).not.toBeInTheDocument();
        expect(screen.queryByTestId("today-topics")).not.toBeInTheDocument();
        expect(screen.queryByRole("button", { name: "yourTopics.edit" })).not.toBeInTheDocument();
        expect(screen.queryByTestId("today-topic-card")).not.toBeInTheDocument();
        expect(screen.getByTestId("today-brief")).toBeInTheDocument();
    });

    it("opens a requested topic on its own page in Today's place, once, and Esc goes back", async () => {
        await withChat(false, async () => {
            width(1200);
            const elsewhere = { id: "atlas", name: "Atlas migration", kind: "work" as const, pinned: true };
            const done = vi.fn();
            render(<TodayPage handlers={handlers} topicRequest={{ topic: elsewhere, nonce: 1 }} onTopicRequestDone={done} />);
            await act(async () => {});
            // A topic opens its own page in Today's place (computer-today v1.2.0 rule 3).
            const page = screen.getByTestId("today-topic-page");
            expect(screen.getByTestId("today-column")).toContainElement(page);
            expect(page).toHaveAccessibleName("Atlas migration");
            // App clears the request, so Today does not open it again on its next visit.
            expect(done).toHaveBeenCalledTimes(1);
            // With the chat switch off (the default), the topic page has no Ask link (scope cut A3).
            expect(screen.queryByTestId("topic-page-ask")).not.toBeInTheDocument();
            // Esc goes back to Today.
            fireEvent.keyDown(document, { key: "Escape" });
            await waitFor(() => expect(screen.queryByTestId("today-topic-page")).not.toBeInTheDocument());
        });
    });

});


describe("TodayPage panel width, the Chat button and the header (Claude Design, 2026-10-22)", () => {
    let measured: { mockRestore: () => void } | null = null;
    const width = (px: number) => {
        measured = vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockReturnValue({ width: px, height: 800, top: 0, left: 0, right: px, bottom: 800, x: 0, y: 0, toJSON: () => ({}) } as DOMRect);
    };
    const closingBrief = () => ({
        version: 1, briefId: "2026-10-22-morning", slot: "morning", localDate: "2026-10-22",
        asOf: "2026-10-22T07:00:00Z", coldStart: false, narrativeStatus: "ok", dayShape: null, evidence: [],
        narrative: [{ text: "Halcyon's launch memo is due Tuesday.", refs: [] }],
        cards: [{ id: "hold", kind: "action", mode: "send", label: "Draft the memo", message: "Draft the Halcyon launch memo.", refs: [] }],
        sections: { moved: [], doing: [], watching: [], meetings: [], people: [], openLoops: [], learned: [], deliverables: [], schedules: [] },
    });
    const withBrief = (brief: unknown) => mockUseLatestBrief.mockReturnValue({
        view: { brief, building: false, enabled: true, nextAt: "", nextSlot: "", empty: false, missing: [] },
        loading: false, loadFailed: false, make: "idle", nextAllowedAt: "", makeNow: vi.fn(), refresh: vi.fn(),
    });
    afterEach(() => {
        measured?.mockRestore();
        measured = null;
    });
    beforeEach(() => {
        vi.setSystemTime(new Date(closingBrief().asOf));
        mockUseTodayNews.mockReturnValue({ data: { date: "2026-10-22", status: "none", items: [], updatedAt: 0 }, loading: false, regenerate: vi.fn() });
        mockUseTodayTopics.mockReturnValue({ topics: [], status: "ready", refresh: vi.fn() });
    });
    it("leads with the brief band and its first sentences, with no full card under it, and opens Past briefs in Today's place (SH1, computer-today rules 10; v1.2.0 rule 1)", async () => {
        width(1400);
        withBrief(closingBrief());
        render(<TodayPage handlers={handlers} />);
        await act(async () => {});
        const band = screen.getByTestId("today-brief-band");
        expect(band).toHaveTextContent("ctoday.inBrief");
        // The old card with its chips and day strip is gone from Today; the
        // band reads the brief's first sentences, once.
        expect(screen.queryByTestId("brief-card")).toBeNull();
        expect(within(band).getByTestId("brief-band-lead")).toHaveTextContent("Halcyon's launch memo is due Tuesday.");
        expect(screen.getAllByText(/Halcyon's launch memo is due Tuesday\./)).toHaveLength(1);
        // Past briefs opens the brief in full in Today's place, with its
        // Past briefs column; ‹ Today comes back (computer-today v1.2.0 rule 1).
        fireEvent.click(within(band).getByTestId("brief-band-past"));
        const full = await screen.findByTestId("today-brief-full");
        expect(await within(full).findAllByTestId("past-brief-row")).toHaveLength(1);
        expect(screen.queryByTestId("today-keys-strip")).not.toBeInTheDocument();
        fireEvent.click(screen.getByTestId("brief-full-back"));
        expect(await screen.findByTestId("today-brief-band")).toBeInTheDocument();
    });

    it("caps and centers Today even when the saved reading preference is full width", async () => {
        width(1400);
        render(<TodayPage handlers={handlers} wideMode />);
        await act(async () => {});
        // Content stops at 1180 and centres (computer-window v1.0.0 rule 9).
        expect(screen.getByTestId("today-column")).toHaveClass("max-w-[1180px]", "mx-auto", "w-full");
    });


    it("names the brief on the card in the display header and follows the clock without one", async () => {
        width(1200);
        vi.useFakeTimers({ toFake: ["Date"] });
        vi.setSystemTime(new Date(closingBrief().asOf));
        withBrief(closingBrief());
        const { unmount } = render(<TodayPage handlers={handlers} />);
        await act(async () => {});
        expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent("ctoday.title");
        unmount();
        withBrief(null);
        render(<TodayPage handlers={handlers} />);
        await act(async () => {});
        expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent("ctoday.title");
        expect(screen.getByRole("heading", { level: 1 })).toHaveClass("display");
        vi.useRealTimers();
    });

    it("names the morning brief on screen from noon to 6 PM (today-review v1.1.24 R54)", async () => {
        width(1200);
        const brief = closingBrief();
        const made = new Date(brief.asOf);
        vi.useFakeTimers({ toFake: ["Date"] });
        try {
            for (const [hour, minute] of [[12, 0], [15, 0], [17, 59]]) {
                vi.setSystemTime(new Date(made.getFullYear(), made.getMonth(), made.getDate(), hour, minute));
                withBrief(brief);
                const { unmount } = render(<TodayPage handlers={handlers} />);
                await act(async () => {});
                expect(screen.getByTestId("today-brief-band")).toBeInTheDocument();
                expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent("ctoday.title");
                unmount();
            }
        } finally {
            vi.useRealTimers();
        }
    });

    it("follows the slot once no brief from today is on screen: the next morning's after midnight (today-review v1.1.22 R51)", async () => {
        width(1200);
        const brief = closingBrief();
        const made = new Date(brief.asOf);
        vi.useFakeTimers({ toFake: ["Date"] });
        try {
            vi.setSystemTime(new Date(made.getFullYear(), made.getMonth(), made.getDate() + 1, 0, 30));
            withBrief({ ...brief, slot: "evening", asOf: new Date(made.getFullYear(), made.getMonth(), made.getDate(), 18, 0).toISOString() });
            render(<TodayPage handlers={handlers} />);
            await act(async () => {});
            expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent("ctoday.title");
        } finally {
            vi.useRealTimers();
        }
    });

});


it('resumes observation in place from the quiet footer', async () => {
    mockUseTodayNews.mockReturnValue({ data: { date: '2026-10-23', status: 'none', items: [], updatedAt: 0 }, loading: false, regenerate: vi.fn() });
    // The paused line comes from the observation state, not the counts.
    pauseMocks.paused.mockResolvedValue(true);
    renderToday();
    const footer = await screen.findByTestId('today-quiet-footer');
    const link = within(footer).getByRole('button', { name: 'captured.resume' });
    fireEvent.click(link);
    await waitFor(() => expect(pauseMocks.resumeObservation).toHaveBeenCalledOnce());
    expect(screen.getByTestId('today-status')).not.toContainElement(link);
    pauseMocks.paused.mockResolvedValue(false);
});

it('pauses topic refreshes while the retained Today page is hidden', () => {
    const page = render(<TodayPage handlers={handlers} active={false} />);
    expect(mockUseTodayTopics).toHaveBeenLastCalledWith(false);
    page.rerender(<TodayPage handlers={handlers} active />);
    expect(mockUseTodayTopics).toHaveBeenLastCalledWith(true);
});


describe("Today is never blank (today-review v1.1.14 D8)", () => {
    const hanging = { view: null, loading: true, loadFailed: false, make: "idle", nextAllowedAt: "", makeNow: vi.fn(), refresh: vi.fn() };

    it("paints the header, status line and placeholders while the brief read hangs", () => {
        mockUseLatestBrief.mockReturnValue(hanging);
        mockUseTodayStatus.mockReturnValue({ week: null, brief: null, loading: true, refresh: vi.fn() });
        renderToday();
        const header = screen.getByTestId("today-header");
        expect(within(header).getByRole("heading", { level: 1 })).toHaveTextContent("ctoday.title");
        expect(within(header).getByTestId("today-status")).not.toBeEmptyDOMElement();
        expect(screen.getByTestId("brief-card-loading")).toHaveAttribute("aria-busy", "true");
        // Nothing on the page waits on an entrance animation to become visible.
        expect(screen.getByTestId("today-column").className).not.toMatch(/\banimate-/);
    });

    it("says a failed brief read in copper with Try again", () => {
        const refresh = vi.fn();
        mockUseLatestBrief.mockReturnValue({ ...hanging, loading: false, loadFailed: true, refresh });
        renderToday();
        expect(within(screen.getByTestId("today-header")).getByRole("heading", { level: 1 })).toBeInTheDocument();
        const failed = screen.getByTestId("brief-load-failed");
        expect(failed).toHaveClass("text-copper");
        expect(failed).toHaveTextContent("todayBrief.loadFailed");
        fireEvent.click(within(failed).getByRole("button", { name: "todayBrief.retry" }));
        expect(refresh).toHaveBeenCalledTimes(1);
    });
});

describe("nothing opens beside Today (computer-today v1.2.0)", () => {
    const written = {
        version: 1, briefId: "2026-10-22-morning", slot: "morning", localDate: "2026-10-22",
        asOf: "2026-10-22T07:00:00Z", coldStart: false, narrativeStatus: "ok", dayShape: null, evidence: [],
        narrative: [{ text: "Halcyon's launch memo is due Tuesday.", refs: [] }],
        cards: [], sections: { moved: [], doing: [], watching: [], meetings: [], people: [], openLoops: [], learned: [], deliverables: [], schedules: [] },
    };
    beforeEach(() => {
        vi.setSystemTime(new Date(written.asOf));
        mockUseLatestBrief.mockReturnValue({ view: { brief: written, building: false, enabled: true, nextAt: "", nextSlot: "", empty: false, missing: [] }, loading: false, loadFailed: false, make: "idle", nextAllowedAt: "", makeNow: vi.fn(), refresh: vi.fn() });
    });

    it("has no docked panel, no Chat button and no Find time on Today, at any width", async () => {
        for (const px of [700, 1400]) {
            const measured = vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockReturnValue({ width: px, height: 800, top: 0, left: 0, right: px, bottom: 800, x: 0, y: 0, toJSON: () => ({}) } as DOMRect);
            const { unmount } = renderToday();
            await act(async () => {});
            expect(screen.queryByTestId("topic-panel-pinned")).not.toBeInTheDocument();
            expect(screen.queryByTestId("main-chat-panel")).not.toBeInTheDocument();
            expect(screen.queryByTestId("today-open-chat")).not.toBeInTheDocument();
            expect(screen.queryByTestId("today-find-time-panel")).not.toBeInTheDocument();
            expect(screen.queryByRole("button", { name: /findTime/i })).not.toBeInTheDocument();
            expect(screen.queryByTestId("before-you-talk")).not.toBeInTheDocument();
            expect(screen.queryByTestId("today-needs-you")).not.toBeInTheDocument();
            expect(screen.queryByTestId("today-done-for-you")).not.toBeInTheDocument();
            expect(screen.queryByTestId("today-portrait-card")).not.toBeInTheDocument();
            expect(screen.queryByTestId("goal-checkin")).not.toBeInTheDocument();
            unmount();
            measured.mockRestore();
        }
    });

    it("offers no Reply about this in the brief in full while Chat is off, and Not right stays (scope cut A3)", async () => {
        await withChat(false, async () => {
            const onAskAboutParagraph = vi.fn().mockResolvedValue(true);
            renderToday({ ...handlers, onAskAboutParagraph });
            await act(async () => {});
            fireEvent.click(screen.getByTestId("brief-band-read-all"));
            await screen.findByTestId("today-brief-full");
            expect(screen.queryByRole("button", { name: "cbrief.reply" })).not.toBeInTheDocument();
            expect(screen.getAllByTestId("brief-full-not-right").length).toBeGreaterThan(0);
            expect(onAskAboutParagraph).not.toHaveBeenCalled();
        });
    });

    it("keeps the brief band and Your work, with no Quick chat hint in the keys line, while Chat is off (A3)", async () => {
        await withChat(false, async () => {
            renderToday();
            await act(async () => {});
            expect(screen.getByTestId("today-brief-band")).toBeInTheDocument();
            expect(screen.queryByTestId("your-work-keys-quick-chat")).not.toBeInTheDocument();
        });
    });

    it("brings Ask about a topic and Reply about this back with the chat switch on", async () => {
        await withChat(true, async () => {
            const onAskAboutParagraph = vi.fn().mockResolvedValue(true);
            renderToday({ ...handlers, onAskAboutParagraph });
            await act(async () => {});
            fireEvent.click(screen.getByTestId("brief-band-read-all"));
            fireEvent.click(await screen.findByRole("button", { name: "cbrief.reply" }));
            expect(onAskAboutParagraph).toHaveBeenCalledWith("2026-10-22-morning", 0, "Halcyon's launch memo is due Tuesday.", "");
        });
    });
});

it("says when a meeting on the day strip couldn't be opened, with Try again (a failed read is never silent)", async () => {
    pauseMocks.dayShape.mockResolvedValue(dayShapeFixture);
    pauseMocks.dayMeeting.mockRejectedValueOnce(new Error("read failed")).mockResolvedValueOnce({ kind: "notes", sessionId: "s1" });
    mockUseLatestBrief.mockReturnValue({ view: { brief: null, building: false, enabled: true, nextAt: "", nextSlot: "morning", pendingSlot: "morning", empty: false, missing: [] }, loading: false, loadFailed: false, make: "idle", nextAllowedAt: "", makeNow: vi.fn(), refresh: vi.fn() });
    const onOpenMeetingNotes = vi.fn();
    renderToday({ ...handlers, onOpenMeetingNotes });
    fireEvent.click((await screen.findAllByTestId("today-day-event"))[0]);
    const failed = await screen.findByTestId("today-day-meeting-failed");
    expect(failed).toHaveAttribute("role", "alert");
    fireEvent.click(within(failed).getByRole("button"));
    await waitFor(() => expect(onOpenMeetingNotes).toHaveBeenCalledWith("s1"));
    expect(screen.queryByTestId("today-day-meeting-failed")).not.toBeInTheDocument();
});


describe("Today says when a read failed (hub rule, 2026-11-21)", () => {
    afterEach(() => { resetPageQueries(); });

    it("says it couldn't read the brief times, and Try again reads them", async () => {
        resetPageQueries();
        pauseMocks.schedule.mockRejectedValueOnce(new Error("timeout")).mockResolvedValueOnce({ enabled: true, morning: "07:00", evening: "18:00" });
        renderToday();
        const failed = await screen.findByTestId("today-brief-times-failed");
        expect(failed).toHaveAttribute("role", "alert");
        expect(failed).toHaveTextContent("ctoday.timesFailed");
        fireEvent.click(within(failed).getByRole("button", { name: "ctoday.tryAgain" }));
        expect(await screen.findByTestId("today-brief-times")).toBeInTheDocument();
        expect(screen.queryByTestId("today-brief-times-failed")).not.toBeInTheDocument();
    });
});

it("comes back from a page in Today's place when an item is asked for (Quick chat's Open)", async () => {
    const written = { version: 1, briefId: "2026-10-22-morning", slot: "morning", localDate: "2026-10-22", asOf: "2026-10-22T07:00:00Z", coldStart: false, narrativeStatus: "ok", dayShape: null, evidence: [], narrative: [{ text: "Halcyon's launch memo is due Tuesday.", refs: [] }], cards: [], sections: { moved: [], doing: [], watching: [], meetings: [], people: [], openLoops: [], learned: [], deliverables: [], schedules: [] } };
    vi.setSystemTime(new Date(written.asOf));
    mockUseLatestBrief.mockReturnValue({ view: { brief: written, building: false, enabled: true, nextAt: "", nextSlot: "", empty: false, missing: [] }, loading: false, loadFailed: false, make: "idle", nextAllowedAt: "", makeNow: vi.fn(), refresh: vi.fn() });
    const { rerender } = render(<TodayPage handlers={handlers} />);
    await act(async () => {});
    fireEvent.click(screen.getByTestId("brief-band-read-all"));
    expect(await screen.findByTestId("today-brief-full")).toBeInTheDocument();
    rerender(<TodayPage handlers={handlers} workRequest={{ itemId: "prep:m1", nonce: 1 }} />);
    expect(await screen.findByTestId("today-brief-band")).toBeInTheDocument();
    expect(screen.queryByTestId("today-brief-full")).not.toBeInTheDocument();
});


it("says under the date that the phone went quiet while the watch listened, past 3 hours (computer-trouble v1.0.0, W3)", async () => {
    resetPageQueries();
    pauseMocks.trouble.mockResolvedValue({ items: [{ kind: "phone", since: new Date(Date.now() - 4 * 60 * 60_000).toISOString(), watchListening: true }] });
    renderToday();
    const line = await screen.findByTestId("today-trouble");
    expect(screen.getByTestId("today-header")).toContainElement(line);
    expect(line).toHaveTextContent(/trouble\.phone|hasn't checked in/);
    pauseMocks.trouble.mockResolvedValue({ items: [] });
    resetPageQueries();
});


it("keeps a quiet phone off Today when the watch wasn't listening, or before 3 hours (W3, W5)", () => {
    const now = Date.parse("2026-11-22T12:00:00Z");
    const hoursAgo = (h: number) => new Date(now - h * 60 * 60_000).toISOString();
    expect(phoneQuietOnToday({ since: hoursAgo(4), watchListening: true }, now)).toBe(true);
    expect(phoneQuietOnToday({ since: hoursAgo(2), watchListening: true }, now)).toBe(false);
    expect(phoneQuietOnToday({ since: hoursAgo(15) }, now)).toBe(false);
    expect(phoneQuietOnToday({ since: hoursAgo(15), watchListening: false }, now)).toBe(false);
});
