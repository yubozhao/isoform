import { resetMainChatHeldActions } from '@/lib/main-chat-held-action';
import { pageQueryCache } from '@/lib/query-cache';
import { resetPageQueries, TODAY_TOPICS_PENDING_RESYNC_MS } from '@/lib/page-queries';
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { useEffect } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createInstance } from "i18next";
import en from "@/i18n/locales/en/today.json";
import enChat from "@/i18n/locales/en/chat.json";
import { TodayPage } from "./TodayPage";
import { todayNextBrief, dayShapeFixture } from "./today-next.fixture";
import { useMainChatItemPanel } from "@/hooks/useMainChatItemPanel";
import { resetMainChatWaitingStore } from "@/lib/main-chat-waiting-store";
import { MarkTodayDoneOpened, type TodayPanelAnswer } from "@/lib/wails-shim-app";

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

    it("opens the computer hold card for a commitment when offers are on", async () => {
        pauseMocks.holdSettings.mockResolvedValueOnce({ granted: true, offerHolds: true, calendarId: "local", calendars: [{ id: "local", title: "Home", sourceId: "local", sourceTitle: "On My Mac", sourceType: "local", isDefault: true }] });
        pauseMocks.proposeHold.mockResolvedValueOnce({ id: "h", thing: { title: "Q4 plan", sourceKind: "week", sourceId: "commitments.79bc12" }, calendarId: "local", slots: [{ start: "2026-10-30T09:30:00Z", end: "2026-10-30T10:30:00Z" }] });
        const brief = { ...todayNextBrief, slot: "evening", yourWeek: { start: "2026-10-19", end: "2026-10-23", lead: "Your week.", open: [{ itemKey: "commitments.79bc12", cardId: "item:commitments.79bc12", title: "Q4 plan", label: "Find time for Q4 plan" }] }, cards: [{ id: "item:commitments.79bc12", kind: "item", mode: "send", label: "Find time for Q4 plan", itemKey: "commitments.79bc12", message: "Where do I stand?", refs: [] }] };
        mockUseLatestBrief.mockReturnValue({ view: { brief, building: false, enabled: true, empty: false, missing: [] }, loading: false, loadFailed: false, make: "idle", nextAllowedAt: "", makeNow: vi.fn(), refresh: vi.fn() });
        renderToday(handlers, { active: true });
        fireEvent.click(await screen.findByRole("button", { name: "Find time for Q4 plan" }));
        expect(await screen.findByTestId("hold-card")).toBeInTheDocument();
        expect(pauseMocks.proposeHold).toHaveBeenCalledWith({ title: "Q4 plan", sourceKind: "week", sourceId: "commitments.79bc12" }, "", 0);
        expect(pauseMocks.findTime).not.toHaveBeenCalled();
    });

    it("posts a typed paragraph question in the panel with its validated brief reference", async () => {
        mockUseTodayNews.mockReturnValue({ data: { date: "2026-10-30", status: "none", items: [], updatedAt: 0 }, loading: false, regenerate: vi.fn() });
        const brief = { ...todayNextBrief, briefId: "b", narrative: [{ text: "Release is ready.", refs: ["e1"] }] };
        mockUseLatestBrief.mockReturnValue({ view: { brief, building: false, enabled: true, missing: [] }, loading: false, loadFailed: false, make: "idle", makeNow: vi.fn(), refresh: vi.fn() });
        renderToday();
        fireEvent.click(await screen.findByTestId("brief-ask-about-paragraph"));
        const input = screen.getByRole("textbox");
        fireEvent.change(input, { target: { value: "What remains?" } });
        fireEvent.submit(input.closest("form")!);
        await waitFor(() => expect(pauseMocks.sendMainCtx).toHaveBeenCalledWith("What remains?", { sourceKind: "brief", sourceId: "current-version", briefId: "b" }));
        expect(pauseMocks.paragraph).toHaveBeenCalledWith("b", 0, "Release is ready.");
        expect(handlers.onStartChat).not.toHaveBeenCalled();
    });
    it("reopens the matching saved paragraph question without posting it again", async () => {
        mockUseTodayNews.mockReturnValue({ data: { date: "2026-10-30", status: "none", items: [], updatedAt: 0 }, loading: false, regenerate: vi.fn() });
        const brief = { ...todayNextBrief, briefId: "b", narrative: [{ text: "Release is ready.", refs: ["e1"] }] };
        mockUseLatestBrief.mockReturnValue({ view: { brief, building: false, enabled: true, missing: [] }, loading: false, loadFailed: false, make: "idle", makeNow: vi.fn(), refresh: vi.fn() });
        pauseMocks.savedAnswer.mockImplementation(async (ref: unknown) => (ref as { sourceKind?: string }).sourceKind === "brief" ? { question: "What remains?", answer: "The review.", userItemId: "u", answerItemId: "a" } : null);
        renderToday();
        fireEvent.click(await screen.findByTestId("brief-ask-about-paragraph"));
        const input = screen.getByRole("textbox");
        fireEvent.change(input, { target: { value: "What remains?" } });
        fireEvent.submit(input.closest("form")!);
        expect(await screen.findByText("The review.")).toBeInTheDocument();
        expect(pauseMocks.sendMainCtx).not.toHaveBeenCalled();
    });
    it("does not send a paragraph question after Today has been left", async () => {
        let finish!: (value: { sourceKind: string; sourceId: string; briefId: string }) => void;
        pauseMocks.paragraph.mockReturnValueOnce(new Promise((resolve) => { finish = resolve; }));
        mockUseTodayNews.mockReturnValue({ data: { date: "2026-10-30", status: "none", items: [], updatedAt: 0 }, loading: false, regenerate: vi.fn() });
        mockUseLatestBrief.mockReturnValue({ view: { brief: { ...todayNextBrief, briefId: "b", narrative: [{ text: "Release is ready.", refs: ["e1"] }] }, building: false, enabled: true, missing: [] }, loading: false, loadFailed: false, make: "idle", makeNow: vi.fn(), refresh: vi.fn() });
        const page = renderToday();
        fireEvent.click(await screen.findByTestId("brief-ask-about-paragraph"));
        const input = screen.getByRole("textbox");
        fireEvent.change(input, { target: { value: "What remains?" } });
        fireEvent.submit(input.closest("form")!);
        page.rerender(<TodayPage handlers={handlers} active={false} />);
        await act(async () => finish({ sourceKind: "brief", sourceId: "current-version", briefId: "b" }));
        expect(pauseMocks.sendMainCtx).not.toHaveBeenCalled();
    });
    it("sends a first brief starter in Today's panel without opening Chat", async () => {
        firstDayMock.mockReturnValue({ day: 0, firstDays: true, history: null,
            calendar: { connected: true, nextMeeting: { start: Date.now() + 3600000, title: "Planning", attendeeNames: ["Maya"] }, freeRanges: [] } });
        mockUseTodayNews.mockReturnValue({ data: { date: "2026-10-30", status: "none", items: [], updatedAt: 0 }, loading: false, regenerate: vi.fn() });
        mockUseLatestBrief.mockReturnValue({ view: { brief: { ...todayNextBrief, coldStart: true, slot: "morning", displaySlot: "morning" }, building: false, enabled: true, missing: [] },
            loading: false, loadFailed: false, make: "idle", makeNow: vi.fn(), refresh: vi.fn() });
        const prefill = vi.fn();
        renderToday({ ...handlers, onPrefillChat: prefill });
        const chips = await screen.findByTestId("first-brief-chips");
        fireEvent.click(within(chips).getAllByRole("button")[0]);
        await waitFor(() => expect(pauseMocks.sendMain).toHaveBeenCalledTimes(1));
        expect(prefill).not.toHaveBeenCalled();
        expect(screen.getByTestId("main-chat-panel")).toBeInTheDocument();
    });
    it("answers a commitment's Find time chip without preparing a chat draft", async () => {
        const brief = {
            ...todayNextBrief,
            slot: "evening",
            yourWeek: { start: "2026-10-19", end: "2026-10-23", lead: "Your week.",
                open: [{ itemKey: "commitments.79bc12", cardId: "item:commitments.79bc12", title: "Q4 plan", label: "Find time for Q4 plan" }] },
            cards: [{ id: "item:commitments.79bc12", kind: "item", mode: "send", label: "Find time for Q4 plan",
                itemKey: "commitments.79bc12", message: "Where do I stand on Q4 plan?", refs: [] }],
        };
        mockUseLatestBrief.mockReturnValue({ view: { brief, building: false, enabled: true, empty: false, missing: [] },
            loading: false, loadFailed: false, make: "idle", nextAllowedAt: "", makeNow: vi.fn(), refresh: vi.fn() });
        renderToday(handlers, { active: true });
        fireEvent.click(await screen.findByRole("button", { name: "Find time for Q4 plan" }));
        expect(await screen.findByText(/You're free/)).toBeInTheDocument();
        expect(pauseMocks.findTime).toHaveBeenCalledOnce();
        expect(pauseMocks.prepareDraft).not.toHaveBeenCalled();
    });

    it("switches from Find time to a weekly Ask in the panel", async () => {
        const measured = vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockReturnValue({ width: 1400, height: 800, top: 0, left: 0, right: 1400, bottom: 800, x: 0, y: 0, toJSON: () => ({}) } as DOMRect);
        const brief = { ...todayNextBrief, slot: "evening", yourWeek: { start: "2026-10-19", end: "2026-10-23", lead: "Your week.", open: [
            { itemKey: "commitments.79bc12", cardId: "item:commitments.79bc12", title: "Q4 plan", label: "Find time for Q4 plan" },
            { itemKey: "goal.other", cardId: "item:goal.other", title: "Other work", label: "What remains?" },
        ] }, cards: [
            { id: "item:commitments.79bc12", kind: "item", mode: "send", itemKey: "commitments.79bc12", message: "Find time", refs: [] },
            { id: "item:goal.other", kind: "item", mode: "send", itemKey: "goal.other", message: "What remains?", refs: [] },
        ] };
        mockUseLatestBrief.mockReturnValue({ view: { brief, building: false, enabled: true, empty: false, missing: [] },
            loading: false, loadFailed: false, make: "idle", nextAllowedAt: "", makeNow: vi.fn(), refresh: vi.fn() });
        renderToday(handlers, { active: true });
        fireEvent.click(await screen.findByRole("button", { name: "Find time for Q4 plan" }));
        expect(await screen.findByTestId("today-find-time-panel")).toBeInTheDocument();
        fireEvent.click(screen.getByRole("button", { name: "What remains?" }));
        await waitFor(() => expect(pauseMocks.sendMainCtx).toHaveBeenCalledWith("What remains?", expect.objectContaining({ sourceId: "item:goal.other" })));
        expect(screen.queryByTestId("today-find-time-panel")).not.toBeInTheDocument();
        expect(screen.getByTestId("main-chat-panel")).toBeInTheDocument();
        measured.mockRestore();
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

    it('notes a check-in arriving on hidden Today only on its first visible visit', async () => {
        width(1200);
        const { rerender } = render(<TodayPage handlers={handlers} active={false} />);
        await act(async () => {});
        mockUseGoalCheckIn.mockReturnValue({ checkIn: { goalId: 'g', eventId: 'e', title: 'Discovery', dueAt: '2026-08-01T23:59:59Z' }, refresh: () => undefined });
        rerender(<TodayPage handlers={handlers} active={false} />);
        expect(screen.getByTestId('goal-checkin')).toBeInTheDocument();
        expect(pauseMocks.noteCheckIn).not.toHaveBeenCalled();
        rerender(<TodayPage handlers={handlers} active />);
        await waitFor(() => expect(pauseMocks.noteCheckIn).toHaveBeenCalledWith('g', 'e'));
        rerender(<TodayPage handlers={handlers} active={false} />);
        rerender(<TodayPage handlers={handlers} active />);
        expect(pauseMocks.noteCheckIn).toHaveBeenCalledTimes(1);
    });

    it('closes Your topics on hide without reopening it on return', async () => {
        width(1200);
        const { rerender } = render(<TodayPage handlers={handlers} active />);
        await act(async () => {});
        fireEvent.click(screen.getByRole('button', { name: 'yourTopics.edit' }));
        expect(await screen.findByRole('dialog')).toBeInTheDocument();
        rerender(<TodayPage handlers={handlers} active={false} />);
        rerender(<TodayPage handlers={handlers} active />);
        expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
    });

    it('closes the topic sheet on hide and retains the topic draft without reopening', async () => {
        width(800);
        const { rerender } = render(<TodayPage handlers={handlers} active />);
        await act(async () => {});
        fireEvent.click(within(screen.getByTestId('today-topics')).getByRole('button', { name: /Halcyon launch memo/ }));
        const sheet = await screen.findByRole('dialog');
        fireEvent.change(within(sheet).getByRole('textbox'), { target: { value: 'Unsaved topic draft' } });
        rerender(<TodayPage handlers={handlers} active={false} />);
        rerender(<TodayPage handlers={handlers} active />);
        expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
        fireEvent.click(within(screen.getByTestId('today-topics')).getByRole('button', { name: /Halcyon launch memo/ }));
        expect(within(await screen.findByRole('dialog')).getByRole('textbox')).toHaveValue('Unsaved topic draft');
    });

    it("reports the visible topic so the sidebar highlights the active circle", async () => {
        width(1200);
        const onActiveTopicChange = vi.fn();
        render(<TodayPage handlers={handlers} onActiveTopicChange={onActiveTopicChange} />);
        await act(async () => {});
        expect(onActiveTopicChange).toHaveBeenLastCalledWith("halcyon");
        fireEvent.click(within(screen.getByTestId("today-topics")).getByRole("button", { name: /Halcyon launch memo/ }));
        expect(onActiveTopicChange).toHaveBeenLastCalledWith("halcyon-launch");
    });

    it("keeps the panel open beside a wide page on the first topic waiting on you, with no click", async () => {
        width(1200);
        render(<TodayPage handlers={handlers} />);
        const pinned = screen.getByTestId("topic-panel-pinned");
        expect(within(pinned).getByRole("heading", { name: "Halcyon ERP sync" })).toBeInTheDocument();
        expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
        await act(async () => {});
        expect(pauseMocks.listTopic).toHaveBeenCalledWith("halcyon", "", 50);
    });

    it("answers a paragraph chip in the main panel without filling the composer", async () => {
        width(1200);
        render(<TodayPage handlers={handlers} />);
        await act(async () => {});
        // Questions show once it is known that observing is not paused.
        pauseMocks.recordToday.mockClear();
        fireEvent.click(await screen.findByRole("button", { name: "What can move off Tuesday?" }));
        expect(pauseMocks.recordToday).toHaveBeenCalledTimes(1);
        expect(pauseMocks.recordToday).toHaveBeenCalledWith("today", "T10", "ask", "opened", expect.any(Number));
        await waitFor(() => expect(pauseMocks.sendMainCtx).toHaveBeenCalledWith("What can move off Tuesday?", expect.objectContaining({ sourceKind: "brief_card", generatedMessage: "What can move off Tuesday?", telemetryAction: "T10" })));
        const panel = screen.getByTestId("main-chat-panel");
        expect(within(panel).getByRole("textbox")).toHaveValue("");
        expect(pauseMocks.sendTopic).not.toHaveBeenCalled();
        expect(handlers.onStartChat).not.toHaveBeenCalled();
    });

    it("reopens a saved chip answer without a second request and refreshes on demand", async () => {
        width(1200);
        const saved = { question: "What can move off Tuesday?", answer: "Move the review.", userItemId: "user-1", answerItemId: "answer-1" };
        pauseMocks.savedAnswer.mockResolvedValue(saved);
        render(<TodayPage handlers={handlers} />);
        const chip = await screen.findByRole("button", { name: "What can move off Tuesday?" });
        await waitFor(() => expect(chip.querySelector(".lucide-check")).not.toBeNull());
        fireEvent.click(chip);
        expect(await screen.findByText("Move the review.")).toBeInTheDocument();
        expect(pauseMocks.sendMainCtx).not.toHaveBeenCalled();
        fireEvent.click(screen.getByRole("button", { name: "mainChatPanel.refresh" }));
        await waitFor(() => expect(pauseMocks.sendMainCtx).toHaveBeenCalledWith("What can move off Tuesday?", expect.objectContaining({ sourceKind: "brief_card", sourceId: "q1" })));
    });

    it("offers retry without posting when saved-answer lookup fails", async () => {
        width(1200);
        pauseMocks.savedAnswer.mockRejectedValue(new Error("offline"));
        render(<TodayPage handlers={handlers} />);
        fireEvent.click(await screen.findByRole("button", { name: "What can move off Tuesday?" }));
        expect(await screen.findByRole("alert")).toHaveTextContent("mainChatPanel.loadFailed");
        expect(pauseMocks.sendMainCtx).not.toHaveBeenCalled();
    });

    it("keeps an opened topic chat after the topics view refreshes and goes empty", async () => {
        width(1200);
        const { rerender } = render(<TodayPage handlers={handlers} />);
        await act(async () => {});
        fireEvent.click(await within(screen.getByTestId("today-topics")).findByRole("button", { name: /Halcyon launch memo/ }));
        expect(within(screen.getByTestId("topic-panel-pinned")).getByRole("textbox")).toHaveValue("");
        pauseMocks.listTopic.mockClear();

        mockUseTodayTopics.mockReturnValue({ topics: topics.filter((topic) => topic.id !== "halcyon-launch"), status: "ready", refresh: vi.fn() });
        rerender(<TodayPage handlers={handlers} />);
        await act(async () => {});
        expect(within(screen.getByTestId("topic-panel-pinned")).getByRole("heading", { name: "Halcyon launch memo" })).toBeInTheDocument();
        expect(within(screen.getByTestId("topic-panel-pinned")).getByRole("textbox")).toHaveValue("");
        expect(pauseMocks.listTopic).not.toHaveBeenCalled();

        mockUseTodayTopics.mockReturnValue({ topics: [], status: "ready", refresh: vi.fn() });
        rerender(<TodayPage handlers={handlers} />);
        await act(async () => {});
        expect(within(screen.getByTestId("topic-panel-pinned")).getByRole("heading", { name: "Halcyon launch memo" })).toBeInTheDocument();
        expect(within(screen.getByTestId("topic-panel-pinned")).getByRole("textbox")).toHaveValue("");
        expect(pauseMocks.sendTopic).not.toHaveBeenCalled();
    });

    it("below the width shows no panel until a topic is opened, then a Sheet from the right edge", async () => {
        width(800);
        render(<TodayPage handlers={handlers} />);
        await act(async () => {});
        expect(screen.queryByTestId("topic-panel")).not.toBeInTheDocument();
        fireEvent.click(within(screen.getByTestId("today-topics")).getByRole("button", { name: /Halcyon launch memo/ }));
        const sheet = await screen.findByRole("dialog");
        expect(within(sheet).getByRole("heading", { name: "Halcyon launch memo" })).toBeInTheDocument();
        expect(within(sheet).getByTestId("topic-panel")).toBeInTheDocument();
        fireEvent.click(within(sheet).getByRole("button", { name: "topicPanel.close" }));
        await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    });

    it("answers a question in the main panel when its topic is not on Today", async () => {
        width(1200);
        mockUseTodayTopics.mockReturnValue({ topics: topics.filter((topic) => topic.id !== "halcyon-launch"), status: "ready", refresh: vi.fn() });
        render(<TodayPage handlers={handlers} />);
        await act(async () => {});
        fireEvent.click(await screen.findByRole("button", { name: "What can move off Tuesday?" }));
        const panel = within(screen.getByTestId("topic-panel-pinned")).getByTestId("main-chat-panel");
        expect(within(panel).getByRole("textbox")).toHaveValue("");
        expect(handlers.onStartChat).not.toHaveBeenCalled();
        await waitFor(() => expect(pauseMocks.sendMainCtx).toHaveBeenCalledWith("What can move off Tuesday?", expect.objectContaining({ sourceKind: "brief_card", sourceId: "q1", generatedMessage: "What can move off Tuesday?" })));
    });

    it("never brings the Sheet back by itself after the window widens and narrows again", async () => {
        const observers: Array<() => void> = [];
        const original = globalThis.ResizeObserver;
        globalThis.ResizeObserver = class { constructor(callback: () => void) { observers.push(callback); } observe() {} disconnect() {} } as unknown as typeof ResizeObserver;
        try {
            width(800);
            render(<TodayPage handlers={handlers} />);
            await act(async () => {});
            fireEvent.click(within(screen.getByTestId("today-topics")).getByRole("button", { name: /Halcyon launch memo/ }));
            expect(await screen.findByRole("dialog")).toBeInTheDocument();
            measured?.mockRestore();
            width(1200);
            await act(async () => observers.forEach((notify) => notify()));
            expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
            expect(within(screen.getByTestId("topic-panel-pinned")).getByRole("heading", { name: "Halcyon launch memo" })).toBeInTheDocument();
            measured?.mockRestore();
            width(800);
            await act(async () => observers.forEach((notify) => notify()));
            expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
        } finally {
            globalThis.ResizeObserver = original;
        }
    });

    it("does not render the topic detail when unchanged sibling Today data refreshes", async () => {
        width(1200);
        const host = render(<TodayPage handlers={handlers} />);
        await act(async () => {});
        const panel = within(screen.getByTestId("topic-panel-pinned")).getByTestId("topic-panel");
        const editor = within(panel).getByRole("textbox");
        fireEvent.change(editor, { target: { value: "Unsent detail draft" } });
        const renders = pauseMocks.topicRender.mock.calls.length;
        const markup = panel.outerHTML;
        for (let refresh = 0; refresh < 10; refresh++) {
            // A fresh sibling result commits Today even when the topic's
            // own checked data did not change.
            mockUseTopicFinds.mockReturnValue({ ...mockUseTopicFinds() });
            host.rerender(<TodayPage handlers={handlers} />);
        }
        expect(pauseMocks.topicRender.mock.calls.length).toBe(renders);
        expect(panel.outerHTML).toBe(markup);
        expect(editor).toHaveValue("Unsent detail draft");
    });

    it("updates memoized detail timestamps on a genuine clock context change", async () => {
        vi.useFakeTimers();
        const noon = new Date(2026, 9, 23, 12);
        vi.setSystemTime(noon);
        width(1200);
        pauseMocks.listTopic.mockResolvedValue({ sessionId: "main", topicId: "halcyon",
            items: [{ id: "event:clock", kind: "event", at: noon.getTime() - 60_000, text: "Clock context" }] as never[],
            hasMore: false, nextCursor: "" });
        const host = render(<TodayPage handlers={handlers} />);
        try {
            await act(async () => {});
            const row = screen.getByTestId("topic-panel-event");
            const original = row.textContent;
            const tomorrow = new Date(noon);
            tomorrow.setDate(tomorrow.getDate() + 1);
            vi.setSystemTime(tomorrow);
            host.rerender(<TodayPage handlers={handlers} />);
            expect(screen.getByTestId("topic-panel-event")).toBe(row);
            expect(row.textContent).not.toBe(original);
            const renders = pauseMocks.topicRender.mock.calls.length;
            const options = new Intl.DateTimeFormat().resolvedOptions();
            const zone = vi.spyOn(Intl.DateTimeFormat.prototype, "resolvedOptions").mockReturnValue({ ...options,
                timeZone: options.timeZone === "UTC" ? "America/New_York" : "UTC" });
            try {
                host.rerender(<TodayPage handlers={handlers} />);
                expect(pauseMocks.topicRender.mock.calls.length).toBeGreaterThan(renders);
                expect(pauseMocks.listTopic).toHaveBeenCalledTimes(1);
            } finally { zone.mockRestore(); }
        } finally {
            host.unmount();
            vi.useRealTimers();
        }
    });

    it("keeps each topic's own words when another topic opens", async () => {
        width(1200);
        render(<TodayPage handlers={handlers} />);
        await act(async () => {});
        const box = () => within(screen.getByTestId("topic-panel-pinned")).getByRole("textbox");
        fireEvent.change(box(), { target: { value: "Draft for Halcyon" } });
        fireEvent.click(within(screen.getByTestId("today-topics")).getByRole("button", { name: /Halcyon launch memo/ }));
        fireEvent.change(box(), { target: { value: "Draft for Halcyon launch" } });
        fireEvent.click(screen.getByRole("button", { name: /Halcyon ERP sync/ }));
        expect(box()).toHaveValue("Draft for Halcyon");
        fireEvent.click(within(screen.getByTestId("today-topics")).getByRole("button", { name: /Halcyon launch memo/ }));
        expect(box()).toHaveValue("Draft for Halcyon launch");
    });

    it("places Before you talk above Your topics and asks for prep in the panel", async () => {
        width(1200);
        mockUseTodayBeforeYouTalk.mockReturnValue({
            card: {
                meetingId: "coffee", meetingTitle: "Coffee", startAt: Date.now() + 20 * 60_000, endAt: Date.now() + 50 * 60_000,
                personId: "mei", personName: "Mei Chen", youOwe: "The memo outline",
            },
            dismiss: vi.fn(),
        });
        render(<TodayPage handlers={handlers} />);
        await act(async () => {});
        const card = screen.getByTestId("before-you-talk");
        const grid = screen.getByTestId("today-topics");
        expect(card.compareDocumentPosition(grid) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
        expect(screen.getByTestId("today-brief")).toContainElement(card);
        fireEvent.click(within(card).getByRole("button", { name: "beforeYouTalk.openPerson" }));
        expect(handlers.onOpenPeople).toHaveBeenCalledWith("mei");
        fireEvent.click(within(card).getByRole("button", { name: "beforeYouTalk.briefMe" }));
        const panel = within(screen.getByTestId("topic-panel-pinned")).getByTestId("main-chat-panel");
        expect(within(panel).getByRole("textbox")).toHaveValue("");
        expect(pauseMocks.sendMain).not.toHaveBeenCalled();
        await waitFor(() => expect(pauseMocks.sendMainCtx).toHaveBeenCalledWith("beforeYouTalk.briefPrompt", expect.objectContaining({ sourceKind: "calendar", sourceId: "coffee", meetingPrep: true, generatedPeople: ["Mei Chen"] })));
        expect(grid).toBeInTheDocument();
    });

    it("keeps Your topics and the rest of Today when Before you talk is empty", async () => {
        width(1200);
        render(<TodayPage handlers={handlers} />);
        await act(async () => {});
        expect(screen.queryByTestId("before-you-talk")).not.toBeInTheDocument();
        expect(screen.getByTestId("today-topics")).toBeInTheDocument();
        expect(screen.getByTestId("today-brief")).toBeInTheDocument();
    });

    it("lists Your topics as one grid, each card opening its topic beside Today", async () => {
        width(1200);
        render(<TodayPage handlers={handlers} />);
        await act(async () => {});
        const grid = screen.getByTestId("today-topics");
        expect(within(grid).getByRole("heading", { name: "yourTopics.title" })).toBeInTheDocument();
        const cards = within(grid).getAllByTestId("today-topic-card");
        expect(cards.map((card) => card.textContent)).toEqual([
            expect.stringContaining("Halcyon ERP sync"), expect.stringContaining("Halcyon launch memo"), expect.stringContaining("Pipeline"),
        ]);
        expect(cards[0]).toHaveAttribute("aria-pressed", "true");
        fireEvent.click(cards[2]);
        expect(cards[2]).toHaveAttribute("aria-pressed", "true");
        expect(within(screen.getByTestId("topic-panel-pinned")).getByRole("heading", { name: "Pipeline" })).toBeInTheDocument();
        expect(pauseMocks.sendTopic).not.toHaveBeenCalled();
        // "New on your topics" folds into the Daily news row once Your topics is the grid.
        expect(screen.queryByTestId("topic-finds")).not.toBeInTheDocument();
    });

    it("pins the open topic to the sidebar from its panel and says when it could not", async () => {
        width(1200);
        const changed = vi.fn();
        window.addEventListener("isoform:sidebar-topics-changed", changed);
        render(<TodayPage handlers={handlers} />);
        await act(async () => {});
        const pin = within(screen.getByTestId("topic-panel-pinned")).getByTestId("topic-panel-pin");
        expect(pin).toHaveTextContent(/^topicPanel\.pin$/);
        await act(async () => { fireEvent.click(pin); });
        expect(pauseMocks.pin).toHaveBeenCalledWith("halcyon", true);
        expect(changed).toHaveBeenCalledTimes(1);
        expect(within(screen.getByTestId("topic-panel-pinned")).getByTestId("topic-panel-pin")).toHaveTextContent(/^topicPanel\.pinned$/);
        pauseMocks.pin.mockRejectedValueOnce(new Error("failed"));
        await act(async () => { fireEvent.click(within(screen.getByTestId("topic-panel-pinned")).getByTestId("topic-panel-pin")); });
        expect(within(screen.getByTestId("topic-panel-pinned")).getByRole("alert")).toHaveTextContent("topicPanel.pinFailed");
        window.removeEventListener("isoform:sidebar-topics-changed", changed);
    });

    it("opens a pinned topic from the sidebar even when it is not among today's topics, once", async () => {
        width(1200);
        const elsewhere = { id: "atlas", name: "Atlas migration", kind: "work" as const, pinned: true };
        const done = vi.fn();
        render(<TodayPage handlers={handlers} topicRequest={{ topic: elsewhere, nonce: 1 }} onTopicRequestDone={done} />);
        await act(async () => {});
        expect(within(screen.getByTestId("topic-panel-pinned")).getByRole("heading", { name: "Atlas migration" })).toBeInTheDocument();
        // App clears the request, so Today does not open it again on its next visit.
        expect(done).toHaveBeenCalledTimes(1);
    });

    it("lets a later registry read decide whether a topic is pinned", async () => {
        width(1200);
        const refresh = vi.fn();
        const { rerender } = render(<TodayPage handlers={handlers} />);
        await act(async () => {});
        await act(async () => { fireEvent.click(within(screen.getByTestId("topic-panel-pinned")).getByTestId("topic-panel-pin")); });
        expect(within(screen.getByTestId("topic-panel-pinned")).getByTestId("topic-panel-pin")).toHaveTextContent(/^topicPanel\.pinned$/);
        // Unpinned from the sidebar: the registry now says Halcyon is not pinned.
        mockUseTodayTopics.mockReturnValue({ topics: topics.map((topic) => ({ ...topic })), status: "ready", refresh });
        rerender(<TodayPage handlers={handlers} />);
        await act(async () => {});
        expect(within(screen.getByTestId("topic-panel-pinned")).getByTestId("topic-panel-pin")).toHaveTextContent(/^topicPanel\.pin$/);
    });

    it("keeps Your topics as the heading while the registry is first read", () => {
        mockUseTodayTopics.mockReturnValue({ topics: [], status: "loading", refresh: vi.fn() });
        render(<TodayPage handlers={handlers} />);
        expect(within(screen.getByTestId("today-topics")).getByRole("heading", { name: "yourTopics.title" })).toBeInTheDocument();
        expect(screen.getByTestId("today-topics-loading")).toHaveAttribute("aria-busy", "true");
        expect(screen.queryByTestId("topic-finds")).not.toBeInTheDocument();
    });

    it("shows the main chat in the panel and no topic buttons while there are no topics", async () => {
        width(1200);
        mockUseTodayTopics.mockReturnValue({ topics: [], status: "unavailable", refresh: vi.fn() });
        render(<TodayPage handlers={handlers} />);
        await act(async () => {});
        expect(screen.queryByTestId("topic-panel")).not.toBeInTheDocument();
        expect(screen.getByTestId("main-chat-panel")).toBeInTheDocument();
        expect(screen.queryByRole("button", { name: /^Halcyon launch memo/ })).not.toBeInTheDocument();
    });
});

describe("TodayPage main chat panel (Claude Design, 2026-10-22: never a drawer that starts closed)", () => {
    const waiting = [{ id: "halcyon", name: "Halcyon ERP sync", kind: "work", status: "waiting", pinned: false, latest: { text: "Active", at: "2026-10-21T15:00:00Z" } }];
    const mainRows = [
        { id: "u-1", kind: "user", content: "Where is the Atlas memo?", sortOrder: 1 },
        { id: "t-1", kind: "tool", toolName: "Read", content: "{}", sortOrder: 2 },
        { id: "a-1", kind: "assistant", content: "It is in Drafts, updated this morning.", sortOrder: 3 },
    ];
    let measured: { mockRestore: () => void } | null = null;
    const width = (px: number) => {
        measured = vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockReturnValue({ width: px, height: 800, top: 0, left: 0, right: px, bottom: 800, x: 0, y: 0, toJSON: () => ({}) } as DOMRect);
    };
    afterEach(() => {
        measured?.mockRestore();
        measured = null;
    });
    beforeEach(() => {
        mockUseTodayNews.mockReturnValue({ data: { date: "2026-10-21", status: "none", items: [], updatedAt: 0 }, loading: false, regenerate: vi.fn() });
        pauseMocks.mainPage.mockResolvedValue({ items: mainRows, hasMore: false, nextCursor: -1 });
    });
    const panel = () => within(screen.getByTestId("topic-panel-pinned")).getByTestId("main-chat-panel");

    it("shows authoritative emptiness in the page and panel after removing the last topic", async () => {
        width(1200);
        const actual = await vi.importActual<typeof import("@/hooks/useTodayTopics")>("@/hooks/useTodayTopics");
        mockUseTodayTopics.mockImplementation(actual.useTodayTopics);
        pauseMocks.todayTopics.mockResolvedValue({ topics: [{ id: "last-followed", kind: "following", name: "Optical camera modules", latest: { text: "Active", at: "2026-10-21T15:00:00Z" } }], refreshing: false, firstRun: false });
        render(<TodayPage handlers={handlers} />);
        expect(await screen.findByTestId("today-topic-card")).toHaveTextContent("Optical camera modules");
        const editor = within(panel()).getByRole("textbox");
        fireEvent.change(editor, { target: { value: "Keep my draft after removal" } });
        pauseMocks.todayTopics.mockResolvedValue({ topics: [], refreshing: false, firstRun: false, clear: true });
        await act(async () => { pauseMocks.listeners.get("topics:updated")?.({}); });
        expect(await screen.findByTestId("today-topics-empty")).toBeInTheDocument();
        expect(screen.queryByTestId("today-topics-gathering")).not.toBeInTheDocument();
        expect(within(panel()).queryByTestId("main-chat-panel-topics-gathering")).not.toBeInTheDocument();
        expect(within(panel()).queryByTestId("main-chat-panel-topics-refreshing")).not.toBeInTheDocument();
        expect(within(panel()).getByRole("textbox")).toBe(editor);
        expect(editor).toHaveValue("Keep my draft after removal");
    });

    it("shows the published empty state after a snapshot filters to zero topics", async () => {
        width(1200);
        const actual = await vi.importActual<typeof import("@/hooks/useTodayTopics")>("@/hooks/useTodayTopics");
        mockUseTodayTopics.mockImplementation(actual.useTodayTopics);
        pauseMocks.todayTopics.mockResolvedValue({ topics: [], refreshing: false, firstRun: false, clear: true });
        const mounted = render(<TodayPage handlers={handlers} />);
        expect(await screen.findByTestId("today-topics-empty")).toBeInTheDocument();
        expect(screen.queryByTestId("today-topics-gathering")).not.toBeInTheDocument();
        expect(within(panel()).queryByTestId("main-chat-panel-topics-gathering")).not.toBeInTheDocument();
        expect(screen.queryByText(/not available/i)).not.toBeInTheDocument();
        expect(within(panel()).getByRole("textbox")).toBeInTheDocument();
        mounted.unmount();
        render(<TodayPage handlers={handlers} />);
        expect(screen.getByTestId("today-topics-empty")).toBeInTheDocument();
        await act(async () => {});
        expect(pauseMocks.todayTopics).toHaveBeenCalledTimes(1);
    });

    it("shows the first day's topics state in the grid and gathering in the chat panel", async () => {
        width(1200);
        const actual = await vi.importActual<typeof import("@/hooks/useTodayTopics")>("@/hooks/useTodayTopics");
        mockUseTodayTopics.mockImplementation(actual.useTodayTopics);
        pauseMocks.todayTopics.mockResolvedValue({ topics: [], refreshing: false, firstRun: true });
        render(<TodayPage handlers={handlers} />);
        // Topics v1.0.0 rules 29 and 30: when topics come, and Add, never an error.
        expect(await screen.findByTestId("today-topics-first-day")).toHaveTextContent("yourTopics.firstBody");
        expect(screen.getByTestId("today-topics-first-field")).toBeInTheDocument();
        expect(screen.queryByText("yourTopics.loadFailed")).not.toBeInTheDocument();
        expect(within(panel()).getByTestId("main-chat-panel-topics-gathering")).toHaveTextContent("yourTopics.gathering");
        expect(screen.queryByText(/not available/i)).not.toBeInTheDocument();
    });

    it("withdraws one topic from Today while keeping its sibling", async () => {
        width(1200);
        const actual = await vi.importActual<typeof import("@/hooks/useTodayTopics")>("@/hooks/useTodayTopics");
        mockUseTodayTopics.mockImplementation(actual.useTodayTopics);
        const atlas = { id: "topic-a", kind: "project", name: "Atlas", status: "moving", latest: { text: "Active", at: "2026-10-21T15:00:00Z" } };
        const beacon = { id: "topic-b", kind: "project", name: "Beacon", status: "moving", latest: { text: "Active", at: "2026-10-21T15:00:00Z" } };
        pauseMocks.todayTopics.mockResolvedValue({ topics: [atlas, beacon], refreshing: false, firstRun: false });
        render(<TodayPage handlers={handlers} />);
        expect(await screen.findAllByTestId("today-topic-card")).toHaveLength(2);
        pauseMocks.todayTopics.mockResolvedValue({ topics: [beacon], refreshing: false, firstRun: false, clear: false });
        await act(async () => { pauseMocks.listeners.get("topics:updated")?.({}); });
        await waitFor(() => expect(screen.getAllByTestId("today-topic-card")).toHaveLength(1));
        expect(screen.getByTestId("today-topic-card")).toHaveTextContent("Beacon");
        expect(screen.queryByText("Atlas")).not.toBeInTheDocument();
    });

    it.each(["event", "missed event"])("publishes first-run topics into the page, panel and cache after %s", async (publication) => {
        width(1200);
        const actual = await vi.importActual<typeof import("@/hooks/useTodayTopics")>("@/hooks/useTodayTopics");
        mockUseTodayTopics.mockImplementation(actual.useTodayTopics);
        pauseMocks.todayTopics.mockResolvedValue({ topics: [], refreshing: true, firstRun: true });
        render(<TodayPage handlers={handlers} />);
        await act(async () => {});
        expect(screen.queryByTestId("today-topics-empty")).not.toBeInTheDocument();
        expect(screen.getByTestId("today-topics-first-day")).toBeInTheDocument();
        expect(within(panel()).queryByTestId("main-chat-panel-topics-refreshing")).not.toBeInTheDocument();
        await waitFor(() => expect(within(panel()).getByTestId("main-chat-panel-topics-refreshing")).toBeInTheDocument(), { timeout: 1500 });
        const editor = within(panel()).getByRole("textbox");
        fireEvent.change(editor, { target: { value: "Keep my unsent words" } });
        const projects = Array.from({ length: 47 }, (_, i) => ({ id: `fixture-project-${i}`, name: `Fixture Atlas ${i}`, kind: "project", status: "moving", latest: { text: "Active", at: "2026-10-21T15:00:00Z" } }));
        const interests = Array.from({ length: 15 }, (_, i) => ({ id: `fixture-interest-${i}`, name: `Fixture research ${i}`, kind: "following", latest: { text: "Active", at: "2026-10-21T15:00:00Z" } }));
        pauseMocks.todayTopics.mockResolvedValue({ topics: projects.slice(0, 6), refreshing: true, firstRun: false });
        await act(async () => { pauseMocks.listeners.get("topics:updated")?.({}); });
        await waitFor(() => expect(screen.getAllByTestId("today-topic-card")).toHaveLength(6));
        expect(screen.getByTestId("today-topics-refreshing")).toBeInTheDocument();
        expect(within(panel()).getByTestId("main-chat-panel-topics-refreshing")).toBeInTheDocument();
        const published = { topics: [...projects, ...interests].slice(0, 12), refreshing: false, firstRun: false };
        pauseMocks.todayTopics.mockResolvedValue(published);
        let clock: { mockRestore(): void } | undefined;
        if (publication === "event") {
            await act(async () => { pauseMocks.listeners.get("topics:updated")?.({}); });
        } else {
            // Snappy 1.0: no 2-second poll. A publication that happened before
            // subscription is recovered by the 10-minute resync, on show.
            clock = vi.spyOn(Date, "now").mockReturnValue(Date.now() + TODAY_TOPICS_PENDING_RESYNC_MS + 1);
            await act(async () => { window.dispatchEvent(new Event("focus")); });
        }
        // At most six cards (today-review v1.1.4 #8).
        await waitFor(() => expect(screen.getAllByTestId("today-topic-card").map((card) => card.textContent)).toEqual(published.topics.slice(0, 6).map((topic) => expect.stringContaining(topic.name))));
        clock?.mockRestore();
        expect(pageQueryCache.peek("GetTodayTopics")).toEqual(published);
        expect(screen.queryByTestId("today-topics-refreshing")).not.toBeInTheDocument();
        expect(screen.queryByTestId("today-topics-loading")).not.toBeInTheDocument();
        expect(within(panel()).queryByTestId("main-chat-panel-topics-refreshing")).not.toBeInTheDocument();
        expect(within(panel()).getByRole("textbox")).toBe(editor);
        expect(editor).toHaveValue("Keep my unsent words");
        fireEvent.click(screen.getAllByTestId("today-topic-card")[0]);
        expect(await screen.findByTestId("topic-panel")).toHaveTextContent("Fixture Atlas 0");
        expect(screen.queryByTestId("today-topics-empty")).not.toBeInTheDocument();
        pauseMocks.todayTopics.mockResolvedValue({ topics: [], refreshing: false, firstRun: false, clear: true });
        await act(async () => { pauseMocks.listeners.get("topics:updated")?.({}); });
        await waitFor(() => expect(screen.getByTestId("today-topics-empty")).toBeInTheDocument());
        expect(screen.getByTestId("topic-panel")).toHaveTextContent("Fixture Atlas 0");
        expect(within(screen.getByTestId("topic-panel")).getByRole("textbox")).toHaveValue("");
    }, 10_000);

    it("shows the whole main chat with its box, headed Main chat, with no Pin, while there is no topic", async () => {
        width(1200);
        mockUseTodayTopics.mockReturnValue({ topics: [], status: "ready", refresh: vi.fn() });
        render(<TodayPage handlers={handlers} />);
        await act(async () => {});
        expect(within(panel()).getByRole("heading", { name: "mainChatPanel.title" })).toBeInTheDocument();
        expect(pauseMocks.mainPage).toHaveBeenCalledWith("main-1", -1, 50);
        expect(within(panel()).getByText("Where is the Atlas memo?")).toBeInTheDocument();
        expect(within(panel()).getByText("It is in Drafts, updated this morning.")).toBeInTheDocument();
        // Tool rows stay in the main chat page.
        expect(within(panel()).queryByText("{}")).not.toBeInTheDocument();
        expect(within(panel()).getByRole("textbox")).toBeInTheDocument();
        expect(within(panel()).queryByTestId("topic-panel-pin")).not.toBeInTheDocument();
        expect(screen.queryByTestId("topic-panel")).not.toBeInTheDocument();
        expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    });

    it("keeps the main chat while the topics are read, and shows its own skeleton only while the chat loads", async () => {
        width(1200);
        let resolvePage: (page: unknown) => void = () => undefined;
        pauseMocks.mainPage.mockReturnValueOnce(new Promise<unknown>((resolve) => { resolvePage = resolve; }));
        mockUseTodayTopics.mockReturnValue({ topics: [], status: "loading", refresh: vi.fn() });
        render(<TodayPage handlers={handlers} />);
        await act(async () => {});
        expect(within(panel()).getByTestId("main-chat-panel-loading")).toBeInTheDocument();
        expect(within(panel()).getByRole("textbox")).toBeInTheDocument();
        await act(async () => { resolvePage({ items: mainRows, hasMore: false, nextCursor: -1 }); });
        expect(within(panel()).queryByTestId("main-chat-panel-loading")).not.toBeInTheDocument();
        expect(within(panel()).getByText("It is in Drafts, updated this morning.")).toBeInTheDocument();
    });

    it("says when the topics read failed, with the grid's own retry, and keeps the chat under it", async () => {
        width(1200);
        const refresh = vi.fn();
        mockUseTodayTopics.mockReturnValue({ topics: [], status: "failed", refresh });
        render(<TodayPage handlers={handlers} />);
        await act(async () => {});
        const line = within(panel()).getByTestId("main-chat-panel-topics-failed");
        expect(line).toHaveTextContent("yourTopics.loadFailed");
        fireEvent.click(within(line).getByRole("button", { name: "todayBrief.retry" }));
        expect(refresh).toHaveBeenCalledTimes(1);
        expect(within(panel()).getByText("It is in Drafts, updated this morning.")).toBeInTheDocument();
        expect(within(panel()).getByRole("textbox")).toBeInTheDocument();
        // The grid keeps its own failed state and retry.
        expect(within(screen.getByTestId("today-topics")).getByRole("button", { name: "todayBrief.retry" })).toBeInTheDocument();
    });

    it("sends from the panel to the main chat with no topic attached, and only on Send", async () => {
        width(1200);
        mockUseTodayTopics.mockReturnValue({ topics: [], status: "ready", refresh: vi.fn() });
        render(<TodayPage handlers={handlers} />);
        await act(async () => {});
        fireEvent.change(within(panel()).getByRole("textbox"), { target: { value: "Plan my afternoon" } });
        expect(pauseMocks.sendMain).not.toHaveBeenCalled();
        await act(async () => { fireEvent.click(within(panel()).getByRole("button", { name: "topicPanel.send" })); });
        expect(pauseMocks.sendMain).toHaveBeenCalledWith("Plan my afternoon");
        expect(pauseMocks.sendTopic).not.toHaveBeenCalled();
        expect(handlers.onStartChat).not.toHaveBeenCalled();
        expect(within(panel()).getByRole("textbox")).toHaveValue("");
        expect(within(panel()).getByRole("status")).toHaveTextContent("topicPanel.working");
    });

    it("puts the words back in the box when the send failed", async () => {
        width(1200);
        mockUseTodayTopics.mockReturnValue({ topics: [], status: "ready", refresh: vi.fn() });
        pauseMocks.sendMain.mockRejectedValueOnce(new Error("offline"));
        render(<TodayPage handlers={handlers} />);
        await act(async () => {});
        fireEvent.change(within(panel()).getByRole("textbox"), { target: { value: "Plan my afternoon" } });
        await act(async () => { fireEvent.click(within(panel()).getByRole("button", { name: "topicPanel.send" })); });
        expect(within(panel()).getByRole("alert")).toHaveTextContent("topicPanel.notSent");
        expect(within(panel()).getByRole("textbox")).toHaveValue("Plan my afternoon");
    });

    it("keeps showing the main chat once topics load, even with one waiting; only a click switches", async () => {
        width(1200);
        const refresh = vi.fn();
        mockUseTodayTopics.mockReturnValue({ topics: [], status: "loading", refresh });
        const { rerender } = render(<TodayPage handlers={handlers} />);
        await act(async () => {});
        fireEvent.change(within(panel()).getByRole("textbox"), { target: { value: "half a thought" } });
        mockUseTodayTopics.mockReturnValue({ topics: waiting, status: "ready", refresh });
        rerender(<TodayPage handlers={handlers} />);
        await act(async () => {});
        expect(within(panel()).getByRole("textbox")).toHaveValue("half a thought");
        expect(screen.queryByTestId("topic-panel")).not.toBeInTheDocument();
        fireEvent.click(screen.getByRole("button", { name: /Halcyon ERP sync/ }));
        await act(async () => {});
        expect(screen.queryByTestId("main-chat-panel")).not.toBeInTheDocument();
        expect(within(screen.getByTestId("topic-panel-pinned")).getByRole("heading", { name: "Halcyon ERP sync" })).toBeInTheDocument();
    });

    it("opens the first topic waiting on you with no click only when the topics are already known", async () => {
        width(1200);
        mockUseTodayTopics.mockReturnValue({ topics: waiting, status: "ready", refresh: vi.fn() });
        render(<TodayPage handlers={handlers} />);
        await act(async () => {});
        expect(screen.queryByTestId("main-chat-panel")).not.toBeInTheDocument();
        expect(within(screen.getByTestId("topic-panel-pinned")).getByRole("heading", { name: "Halcyon ERP sync" })).toBeInTheDocument();
    });

    it("posts a chip under the latest answer at once, as the Chat page does (chat-review v1.0.3 #3)", async () => {
        width(1200);
        mockUseTodayTopics.mockReturnValue({ topics: [], status: "ready", refresh: vi.fn() });
        pauseMocks.chips.mockResolvedValue({ "a-1": ["Open the memo", "Who edited it?"] });
        render(<TodayPage handlers={handlers} />);
        await act(async () => {});
        expect(pauseMocks.chips).toHaveBeenCalledWith(["a-1"]);
        fireEvent.click(within(panel()).getByRole("button", { name: "Open the memo" }));
        await waitFor(() => expect(pauseMocks.sendMain).toHaveBeenCalledWith("Open the memo"));
        expect(within(panel()).getByRole("textbox")).toHaveValue("");
    });

    it("says so when the main chat could not be read and offers the main chat page", async () => {
        width(1200);
        const onOpenMainChat = vi.fn();
        mockUseTodayTopics.mockReturnValue({ topics: [], status: "ready", refresh: vi.fn() });
        pauseMocks.mainPage.mockRejectedValue(new Error("db"));
        render(<TodayPage handlers={{ ...handlers, onOpenMainChat }} />);
        await act(async () => {});
        expect(within(panel()).getByTestId("main-chat-panel-failed")).toHaveTextContent("mainChatPanel.loadFailed");
        fireEvent.click(within(panel()).getByRole("button", { name: "mainChatPanel.openMainChat" }));
        expect(onOpenMainChat).toHaveBeenCalledTimes(1);
        expect(within(panel()).getByRole("textbox")).toBeInTheDocument();
    });

    it("keeps narrow windows as they were: no panel until a click, then the Sheet", async () => {
        width(800);
        mockUseTodayTopics.mockReturnValue({ topics: waiting, status: "ready", refresh: vi.fn() });
        render(<TodayPage handlers={handlers} />);
        await act(async () => {});
        expect(screen.queryByTestId("main-chat-panel")).not.toBeInTheDocument();
        expect(screen.queryByTestId("topic-panel-pinned")).not.toBeInTheDocument();
        expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
        fireEvent.click(screen.getByRole("button", { name: /Halcyon ERP sync/ }));
        expect(await screen.findByRole("dialog")).toBeInTheDocument();
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
    const slot = () => screen.getByTestId("topic-panel-pinned");

    it("leads with the brief band over today's full card, and opens Past briefs beside Today (SH1)", async () => {
        width(1400);
        withBrief(closingBrief());
        render(<TodayPage handlers={handlers} />);
        await act(async () => {});
        const band = screen.getByTestId("today-brief-band");
        expect(band).toHaveTextContent("ctoday.inBrief");
        expect(screen.getByTestId("brief-card")).toBeInTheDocument();
        // The brief's words show once: the card keeps them, the band has none.
        expect(screen.getAllByText("Halcyon's launch memo is due Tuesday.")).toHaveLength(1);
        expect(within(band).queryByTestId("brief-band-lead")).toBeNull();
        fireEvent.click(within(band).getByTestId("brief-band-past"));
        expect(await within(slot()).findByTestId("past-brief-row")).toHaveTextContent("Yesterday's lead.");
    });

    it("caps and centers Today even when the saved reading preference is full width", async () => {
        width(1400);
        render(<TodayPage handlers={handlers} wideMode />);
        await act(async () => {});
        expect(screen.getByTestId("today-column")).toHaveClass("max-w-[760px]", "mx-auto", "w-full");
        expect(slot().style.width).toBe("420px");
    });


    it.each([
        [920, 360],
        [1010, 379],
        [1400, 420],
    ])("pins the panel at %i px of space, %i px wide, with no Chat button", async (space, panel) => {
        width(space);
        render(<TodayPage handlers={handlers} />);
        await act(async () => {});
        expect(slot().style.width).toBe(`${panel}px`);
        expect(within(slot()).getByTestId("main-chat-panel")).toBeInTheDocument();
        expect(screen.queryByTestId("today-open-chat")).not.toBeInTheDocument();
        expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    });

    it("below 920 px shows no panel until the Chat button opens it as a Sheet", async () => {
        width(900);
        render(<TodayPage handlers={handlers} />);
        await act(async () => {});
        expect(screen.queryByTestId("topic-panel-pinned")).not.toBeInTheDocument();
        expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
        fireEvent.click(screen.getByTestId("today-open-chat"));
        const sheet = await screen.findByRole("dialog");
        expect(within(sheet).getByTestId("main-chat-panel")).toBeInTheDocument();
        expect(within(sheet).getByRole("heading", { name: "mainChatPanel.title" })).toBeInTheDocument();
        expect(within(sheet).getByRole("textbox")).toBeInTheDocument();
        fireEvent.click(within(sheet).getByRole("button", { name: "topicPanel.close" }));
        await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    });

    it("measures the space next to the sidebar, so a folded sidebar pins the panel on a narrower window", async () => {
        const innerWidth = Object.getOwnPropertyDescriptor(window, "innerWidth");
        Object.defineProperty(window, "innerWidth", { configurable: true, value: 1100 });
        try {
            width(1010);
            render(<TodayPage handlers={handlers} />);
            await act(async () => {});
            expect(slot().style.width).toBe("379px");
        } finally {
            if (innerWidth) Object.defineProperty(window, "innerWidth", innerWidth);
        }
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
                expect(screen.getByTestId("brief-card")).toBeInTheDocument();
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

    it("keeps a prepared card and its context across three retained visits, then sends only on Send", async () => {
        width(1200);
        withBrief(closingBrief());
        let settle: (value: unknown) => void = () => undefined;
        pauseMocks.prepareDraft.mockReturnValueOnce(new Promise((resolve) => { settle = resolve; }));
        const { rerender } = render(<TodayPage handlers={handlers} active />);
        await act(async () => {});
        expect(screen.queryByTestId("brief-closing-note")).not.toBeInTheDocument();
        fireEvent.click(await screen.findByRole("button", { name: "Draft the memo" }));
        expect(screen.queryByTestId("brief-closing-note")).not.toBeInTheDocument();
        const panel = within(slot()).getByTestId("main-chat-panel");
        // The card's words at once, with the context line while the item is read; Send waits.
        expect(within(panel).getByRole("textbox")).toHaveValue("Draft the Halcyon launch memo.");
        expect(panel.querySelector("[data-today-context-preview]")).toHaveTextContent(enChat.todayContext.loading);
        expect(within(panel).getByRole("button", { name: "topicPanel.send" })).toBeDisabled();
        expect(pauseMocks.prepareDraft).toHaveBeenCalledWith(expect.objectContaining({ sourceKind: "brief_card", sourceId: "hold", briefId: "2026-10-22-morning", generatedMessage: "Draft the Halcyon launch memo." }));
        // Then the computer's current words and what Isoform adds.
        await act(async () => { settle({ prefill: { kind: "brief_card", title: "Draft memo" }, counts: { memories: 1, activities: 2, people: 0 }, fingerprint: "fp-hold", message: "Draft the Halcyon launch memo (as it stands now)." }); });
        expect(within(panel).getByRole("textbox")).toHaveValue("Draft the Halcyon launch memo (as it stands now).");
        expect(panel.querySelector("[data-today-context-preview]")).toHaveTextContent(enChat.todayContext.preview.split("{{")[0]);
        expect(within(panel).getByTestId("topic-panel-note")).toHaveTextContent("topicPanel.notePrepared");
        expect(handlers.onStartChat).not.toHaveBeenCalled();
        expect(pauseMocks.sendMainCtx).not.toHaveBeenCalled();
        expect(pauseMocks.sendMain).not.toHaveBeenCalled();
        for (let i = 0; i < 3; i++) {
            rerender(<TodayPage handlers={handlers} active={false} />);
            rerender(<TodayPage handlers={handlers} active />);
        }
        expect(within(slot()).getByTestId("main-chat-panel")).toBe(panel);
        expect(within(panel).getByRole("textbox")).toHaveValue("Draft the Halcyon launch memo (as it stands now).");
        await act(async () => { fireEvent.click(within(panel).getByRole("button", { name: "topicPanel.send" })); });
        // The reference Go checks again at Send carries the prepared facts.
        expect(pauseMocks.sendMainCtx).toHaveBeenCalledWith("Draft the Halcyon launch memo (as it stands now).", expect.objectContaining({
            sourceKind: "brief_card", sourceId: "hold", briefId: "2026-10-22-morning",
            generatedTitle: "Draft memo", generatedFingerprint: "fp-hold",
            generatedMessage: "Draft the Halcyon launch memo (as it stands now).",
        }));
        expect(within(panel).getByRole("textbox")).toHaveValue("");
        expect(panel.querySelector("[data-today-context-preview]")).toBeNull();
    });

    it("keeps card words the user edited while the item loaded, with the words they were shown, so Send asks Go", async () => {
        width(1200);
        withBrief(closingBrief());
        let settle: (value: unknown) => void = () => undefined;
        pauseMocks.prepareDraft.mockReturnValueOnce(new Promise((resolve) => { settle = resolve; }));
        render(<TodayPage handlers={handlers} />);
        await act(async () => {});
        fireEvent.click(await screen.findByRole("button", { name: "Draft the memo" }));
        const panel = within(slot()).getByTestId("main-chat-panel");
        fireEvent.change(within(panel).getByRole("textbox"), { target: { value: "Draft the Halcyon launch memo. Keep it short." } });
        await act(async () => { settle({ prefill: { kind: "brief_card", title: "Draft memo" }, counts: { memories: 1, activities: 2, people: 0 }, fingerprint: "fp-hold", message: "Something else now." }); });
        expect(within(panel).getByRole("textbox")).toHaveValue("Draft the Halcyon launch memo. Keep it short.");
        await act(async () => { fireEvent.click(within(panel).getByRole("button", { name: "topicPanel.send" })); });
        expect(pauseMocks.sendMainCtx).toHaveBeenCalledWith("Draft the Halcyon launch memo. Keep it short.", expect.objectContaining({
            generatedFingerprint: "fp-hold", generatedMessage: "Draft the Halcyon launch memo.",
        }));
    });

    it("fills the paused draft when observation pauses after Today rendered, and sends it with no sources", async () => {
        width(1200);
        withBrief(closingBrief());
        pauseMocks.prepareDraft.mockRejectedValueOnce(new Error("TODAY_CHAT_OBSERVATION_PAUSED"));
        render(<TodayPage handlers={handlers} />);
        await act(async () => {});
        fireEvent.click(await screen.findByRole("button", { name: "Draft the memo" }));
        await act(async () => {});
        const panel = within(slot()).getByTestId("main-chat-panel");
        expect(within(panel).getByRole("textbox")).toHaveValue("Draft the Halcyon launch memo.");
        expect(panel.querySelector("[data-today-context-preview]")).toHaveTextContent("contextNote.paused");
        await act(async () => { fireEvent.click(within(panel).getByRole("button", { name: "topicPanel.send" })); });
        expect(pauseMocks.sendMainCtx).toHaveBeenCalledWith("Draft the Halcyon launch memo.", expect.objectContaining({ sourceKind: "brief_card", sourceId: "hold" }));
    });

    it("clears a card's untouched words when the card left the brief", async () => {
        width(1200);
        withBrief(closingBrief());
        pauseMocks.prepareDraft.mockRejectedValueOnce(new Error("TODAY_CHAT_CONTEXT_UNAVAILABLE"));
        render(<TodayPage handlers={handlers} />);
        await act(async () => {});
        fireEvent.click(await screen.findByRole("button", { name: "Draft the memo" }));
        await act(async () => {});
        const panel = within(slot()).getByTestId("main-chat-panel");
        expect(within(panel).getByRole("textbox")).toHaveValue("");
        expect(panel.querySelector("[data-today-context-preview]")).toBeNull();
    });

    it("on a narrow window an answer opens the Sheet with the words in its box", async () => {
        width(900);
        withBrief(closingBrief());
        render(<TodayPage handlers={handlers} />);
        await act(async () => {});
        fireEvent.click(await screen.findByRole("button", { name: "Draft the memo" }));
        const sheet = await screen.findByRole("dialog");
        expect(within(sheet).getByRole("textbox")).toHaveValue("Draft the Halcyon launch memo.");
        expect(handlers.onStartChat).not.toHaveBeenCalled();
    });
});

describe("TodayPage main chat panel: Isoform's cards and status rows (Claude Design, 2026-10-22)", () => {
    const localDay = (() => {
        const now = new Date();
        return `${now.getFullYear()}-${String(now.getMonth() + 1).padStart(2, "0")}-${String(now.getDate()).padStart(2, "0")}`;
    })();
    const at = new Date(new Date().setHours(7, 42, 0, 0)).toISOString();
    const card = (id: string, momentId: string, overrides: Record<string, unknown> = {}) => ({
        id, kind: "system", toolName: "main_chat_proactive", rowClass: "waiting", localDay, topicIds: ["halcyon"], sortOrder: 0,
        toolInput: JSON.stringify({
            momentId, kind: "waiting_on_reply", headline: "Someone is waiting on your reply.",
            body: "Halcyon's ops lead asked to move the ERP sync up to Oct 30. Oct 30 would skip the staging preview, so the draft keeps Nov 1.",
            draft: false, actionIds: ["open", "not_now", "mute_kind"], actionLabels: { open: "Open Mail", not_now: "Not now", mute_kind: "Mute this kind" },
            target: { kind: "mail" }, state: "pending", evidence: [{ kind: "mail", id: "m1", at, target: { kind: "mail" } }], ...overrides,
        }),
    });
    const draftCard = card("c-2", "moment-2", {
        draft: true, draftContent: {
            to: "Halcyon ops lead", subject: "Re: ERP sync timing", text: "Thanks for moving fast on this. We'd like to hold Nov 1.",
            communication: { beliefIds: ["belief-communication"], recipientName: "Halcyon ops lead", language: "English", sentences: 2, greeting: true },
        },
        actionIds: ["open", "copy_draft", "not_now", "mute_kind"], actionLabels: { open: "Review draft", copy_draft: "Copy draft", not_now: "Not now", mute_kind: "Mute this kind" },
    });
    const statusRow = {
        id: "s-1", kind: "system", toolName: "main_chat_status", rowClass: "routine", localDay, sortOrder: 0,
        toolInput: JSON.stringify({ period: `${localDay}:morning`, entries: [
            { id: "e1", kind: "brief_ready", text: "Your brief is ready.", target: { kind: "today" } },
            { id: "e2", kind: "capture_paused", text: "Isoform stopped watching for a while.", captureSource: "screen", captureCause: "stopped", captureStartAt: new Date(`${localDay}T12:21:00`).getTime(), target: { kind: "settings", id: "activity" } },
        ] }),
    };
    const rows = [
        statusRow,
        card("c-1", "moment-1"),
        { id: "u-1", kind: "user", content: "Where is Northwind on the SOW?", localDay, sortOrder: 2 },
        { id: "a-1", kind: "assistant", content: "They signed the redlines. Your countersignature is the last open item.", localDay, sortOrder: 3 },
        draftCard,
    ];
    let measured: { mockRestore: () => void } | null = null;
    const width = (px: number) => {
        measured = vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockReturnValue({ width: px, height: 800, top: 0, left: 0, right: px, bottom: 800, x: 0, y: 0, toJSON: () => ({}) } as DOMRect);
    };
    afterEach(() => {
        measured?.mockRestore();
        measured = null;
    });
    beforeEach(() => {
        mockUseTodayNews.mockReturnValue({ data: { date: localDay, status: "none", items: [], updatedAt: 0 }, loading: false, regenerate: vi.fn() });
        mockUseTodayTopics.mockReturnValue({ topics: [{ id: "halcyon", name: "Halcyon ERP sync", kind: "work", status: "today", pinned: false }], status: "ready", refresh: vi.fn() });
        pauseMocks.mainPage.mockResolvedValue({ items: rows, hasMore: false, nextCursor: -1 });
        pauseMocks.overview.mockResolvedValue({ waiting: 0 });
        pauseMocks.resolveCard.mockClear();
    });
    const panel = () => within(screen.getByTestId("topic-panel-pinned")).getByTestId("main-chat-panel");
    const cards = () => Array.from(panel().querySelectorAll<HTMLElement>('[data-main-chat-proactive]')).map((card) => card.parentElement as HTMLElement);
    const mount = async () => {
        width(1200);
        render(<TodayPage handlers={handlers} />);
        await act(async () => {});
    };

    it('keeps the selected card in the shared panel after three retained visits', async () => {
        width(1200);
        const { rerender } = render(<TodayPage handlers={handlers} active />);
        await act(async () => {});
        fireEvent.click(within(cards()[0]).getByRole('button', { name: enChat.mainChat.action.review }));
        const itemPanel = await screen.findByTestId('main-chat-item-panel');
        expect(itemPanel).toHaveAttribute('data-panel-open', 'moment-1');
        const subscriptions = [...pauseMocks.subscriptions].map(([name, handlers]) => [name, handlers.size]);
        for (let i = 0; i < 3; i++) {
            rerender(<TodayPage handlers={handlers} active={false} />);
            rerender(<TodayPage handlers={handlers} active />);
        }
        expect(screen.getByTestId('main-chat-item-panel')).toBe(itemPanel);
        expect(itemPanel).toHaveAttribute('data-panel-open', 'moment-1');
        expect([...pauseMocks.subscriptions].map(([name, handlers]) => [name, handlers.size])).toEqual(subscriptions);
    });

    it("shows each waiting card as Chat's card: the same headline and buttons, no Review row or More (chat-review v1.0.2 #8)", async () => {
        await mount();
        const [first] = cards();
        expect(first.querySelector("[data-main-chat-proactive]")).not.toBeNull();
        expect(within(first).getByText("Someone is waiting on your reply.")).toBeInTheDocument();
        expect(within(first).getByRole("button", { name: enChat.mainChat.action.review })).toBeInTheDocument();
        expect(within(first).queryByRole("button", { name: "mainChatPanel.showCard" })).toBeNull();
        expect(first).not.toHaveTextContent("mainChatPanel.more");
        expect(within(panel()).queryByText("mainChatPanel.subtitle")).toBeNull();
        // The chat's own rows keep their place, in order.
        expect(within(panel()).getByText("Where is Northwind on the SOW?")).toBeInTheDocument();
        expect(within(panel()).getByText("They signed the redlines. Your countersignature is the last open item.")).toBeInTheDocument();
        expect(within(panel()).getByText(enChat.mainChat.view.isoform)).toBeInTheDocument();
    });

    it("shows Review instead of a first action that would send, and sends nothing from the row", async () => {
        await mount();
        const [, draft] = cards();
        expect(within(draft).queryByRole("button", { name: "Review draft" })).not.toBeInTheDocument();
        const review = within(draft).getByRole("button", { name: enChat.mainChat.action.review });
        fireEvent.click(review);
        expect(pauseMocks.resolveCard).not.toHaveBeenCalled();
        expect(pauseMocks.sendMain).not.toHaveBeenCalled();
        expect(pauseMocks.sendMainCtx).not.toHaveBeenCalled();
        const open = await screen.findByTestId("main-chat-item-panel");
        expect(open).toHaveAttribute("data-panel-open", "moment-2");
        const draftBox = open.querySelector("[data-proactive-draft]");
        expect(draftBox).toHaveTextContent("Halcyon ops lead");
        expect(draftBox).toHaveTextContent("Re: ERP sync timing");
        // Copy draft is in the card's ··· (Chat review C081).
        expect(open.querySelector("[data-proactive-action='copy_draft']")).not.toBeInTheDocument();
        expect(within(open as HTMLElement).getByRole("button", { name: enChat.mainChat.proactive.moreActions })).toBeInTheDocument();
        expect(open.querySelector("[data-proactive-action='not_now']")).toBeInTheDocument();
        expect(open).not.toHaveTextContent("mainChat.proactive.sourceAt");
        expect(open).toHaveTextContent(enChat.mainChat.proactive.sourceFrom.replace("{{source}}", "Mail"));
    });

    it("shows the draft card as the Chat page does, and Change opens its belief without an action", async () => {
        const onOpenReportTarget = vi.fn();
        width(1200);
        render(<TodayPage handlers={handlers} onOpenReportTarget={onOpenReportTarget} />);
        await act(async () => {});
        fireEvent.click(within(cards()[1]).getByRole("button", { name: enChat.mainChat.action.review }));
        const open = await screen.findByTestId("main-chat-item-panel");
        expect(open.querySelector("[data-communication-provenance]")).not.toBeNull();
        fireEvent.click(within(open).getByRole("button", { name: enChat.mainChat.proactive.communication.change }));
        expect(onOpenReportTarget).toHaveBeenCalledWith({ kind: "memory", id: "belief-communication" }, expect.any(Function));
        expect(pauseMocks.resolveCard).not.toHaveBeenCalled();
        expect(pauseMocks.sendMain).not.toHaveBeenCalled();
        expect(pauseMocks.sendMainCtx).not.toHaveBeenCalled();
    });

    it("opens only one card at a time", async () => {
        await mount();
        fireEvent.click(within(cards()[0]).getByRole("button", { name: enChat.mainChat.action.review }));
        expect(await screen.findByTestId("main-chat-item-panel")).toHaveAttribute("data-panel-open", "moment-1");
        fireEvent.click(screen.getByRole("button", { name: enChat.mainChat.proactive.closePanel }));
        fireEvent.click(within(cards()[1]).getByRole("button", { name: enChat.mainChat.action.review }));
        expect(await screen.findByTestId("main-chat-item-panel")).toHaveAttribute("data-panel-open", "moment-2");
    });

    it("opens Tasks from the shared Needs you count", async () => {
        mockUseTodayNeeds.mockReturnValue({ items: [{ id: "a" }, { id: "b" }], loading: false, failed: false });
        await mount();
        fireEvent.click(await within(panel()).findByTestId("main-chat-panel-waiting"));
        expect(handlers.onOpenTasks).toHaveBeenCalled();
    });

    it("R60: the header counts Tasks needs, even when Chat has a different waiting count", async () => {
        const translations = createInstance();
        await translations.init({ lng: "en", resources: { en: { today: en, chat: enChat } } });
        mockUseTranslation.mockImplementation((namespace: string) => ({
            t: translations.getFixedT("en", namespace as "today"), i18n: translations,
        }));
        pauseMocks.waiting.mockResolvedValue({ sessionId: "main-1", count: 1, items: [], revision: 1, ready: true });
        mockUseTodayNeeds.mockReturnValue({ items: [], loading: false, failed: false });
        width(1200);
        const view = render(<TodayPage handlers={handlers} />);
        await act(async () => {});
        expect(within(panel()).queryByTestId("main-chat-panel-waiting")).not.toBeInTheDocument();
        mockUseTodayNeeds.mockReturnValue({ items: [{ id: "a" }, { id: "b" }], loading: false, failed: false });
        view.rerender(<TodayPage handlers={handlers} />);
        expect(within(panel()).getByTestId("main-chat-panel-waiting")).toHaveTextContent("2 questions for you");
    });

    it("draws a thread outcome's card as the Chat page does, without resolving it", async () => {
        const threadCard = {
            id: "t-1", kind: "system", toolName: "main_chat_proactive", rowClass: "waiting", localDay, topicIds: [], sortOrder: 0,
            toolInput: JSON.stringify({
                momentId: "thread-moment-1", kind: "decision_due", headline: "An open thread has a next step.",
                body: "Finish the checkout before the confirmation window closes.", draft: false,
                actionIds: ["open"], actionLabels: { open: "Open" },
                target: { kind: "thread", id: "thread-1" }, state: "pending",
                evidence: [{ kind: "thread_outcome", id: "thread-outcome:thread-1", target: { kind: "thread", id: "thread-1" } }],
            }),
        };
        pauseMocks.mainPage.mockResolvedValue({ items: [threadCard], hasMore: false, nextCursor: -1 });
        const onOpenReportTarget = vi.fn();
        width(1200);
        render(<TodayPage handlers={handlers} onOpenReportTarget={onOpenReportTarget} />);
        await act(async () => {});
        fireEvent.click(within(cards()[0]).getByRole("button", { name: enChat.mainChat.card.button.showThread }));
        const itemPanel = await screen.findByTestId("main-chat-item-panel");
        fireEvent.click(within(itemPanel).getByRole("button", { name: enChat.mainChat.card.button.openInActivity }));
        expect(pauseMocks.resolveCard).not.toHaveBeenCalled();
        expect(pauseMocks.sendMain).not.toHaveBeenCalled();
        expect(pauseMocks.sendMainCtx).not.toHaveBeenCalled();
    });

    it("folds the day's status rows into one row that opens in place; nothing disappears", async () => {
        await mount();
        const fold = panel().querySelector(`[data-main-chat-fold='${localDay}']`) as HTMLElement;
        expect(fold).not.toBeNull();
        const toggle = within(fold).getByRole("button", { name: /mainChat\.view\.foldSummary/ });
        expect(toggle).toHaveAttribute("aria-expanded", "false");
        expect(within(panel()).queryByText("Your brief is ready.")).not.toBeInTheDocument();
        fireEvent.click(toggle);
        expect(toggle).toHaveAttribute("aria-expanded", "true");
        // Retired ready copy never shows (chat-review v1.0.6 gate); the brief
        // line keeps its title and source, and the pause says Observing.
        expect(within(fold).queryByText("Your brief is ready.")).not.toBeInTheDocument();
        expect(within(fold).getByText("todayBrief.slot.on_request")).toBeInTheDocument();
        expect(within(fold).queryByText("Isoform stopped watching for a while.")).not.toBeInTheDocument();
        expect(within(fold).getByText(/Stopped watching your screen at .+: screen capture stopped on its own\./)).toBeInTheDocument();
        expect(within(fold).getAllByRole("button", { name: "Show" })).toHaveLength(1);
        expect(within(fold).getAllByRole("button", { name: "Turn it back on" })).toHaveLength(1);
        expect(cards()).toHaveLength(2);
        expect(panel().querySelector(`[data-main-chat-day='${localDay}']`)).not.toBeNull();
    });

    it("shows unsupported row actions as Chat does, without running them", async () => {
        const unknown = card("c-3", "moment-3", { actionIds: ["ship_it", "dismiss"], actionLabels: { ship_it: "Ship it", dismiss: "Dismiss" }, target: { kind: "none" } });
        pauseMocks.mainPage.mockResolvedValue({ items: [unknown], hasMore: false, nextCursor: -1 });
        await mount();
        const row = cards()[0];
        expect(within(row).queryByRole("button", { name: "Ship it" })).not.toBeInTheDocument();
        // Chat's card offers no button for an action it can't run; Today shows the same card.
        expect(within(row).queryByRole("button", { name: "Dismiss" })).not.toBeInTheDocument();
        expect(pauseMocks.resolveCard).not.toHaveBeenCalled();
        expect(pauseMocks.sendMain).not.toHaveBeenCalled();
        expect(pauseMocks.sendMainCtx).not.toHaveBeenCalled();
    });

    it("opens the shared panel instead of resolving or navigating from a compact row", async () => {
        const onOpenReportTarget = vi.fn();
        width(1200);
        render(<TodayPage handlers={handlers} onOpenReportTarget={onOpenReportTarget} />);
        await act(async () => {});
        fireEvent.click(within(cards()[0]).getByRole("button", { name: enChat.mainChat.action.review }));
        expect(await screen.findByTestId("main-chat-item-panel")).toHaveAttribute("data-panel-open", "moment-1");
        expect(pauseMocks.resolveCard).not.toHaveBeenCalled();
        expect(onOpenReportTarget).not.toHaveBeenCalled();
        expect(pauseMocks.sendMain).not.toHaveBeenCalled();
        expect(pauseMocks.sendMainCtx).not.toHaveBeenCalled();
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

describe("TodayPage brief slot selects the strip date without changing the read contract", () => {
    afterEach(() => { vi.useRealTimers(); pauseMocks.dayShape.mockResolvedValue(null); });
    it.each(["morning", "evening"])("draws the %s brief's own day when read in the evening, with no now line after its hours (R44, R53)", async (slot) => {
        vi.useFakeTimers({ toFake: ["Date"] });
        vi.setSystemTime(new Date("2026-10-23T22:00:00-07:00"));
        const translations = createInstance();
        await translations.init({ lng: "en", resources: { en: { today: en, chat: enChat } } });
        mockUseTranslation.mockImplementation((namespace: string) => ({ t: translations.getFixedT("en", namespace as "today"), i18n: translations }));
        const brief = { ...todayNextBrief, slot };
        const requestedDate = "2026-10-23";
        const shape = dayShapeFixture;
        pauseMocks.dayShape.mockResolvedValue({ ...shape, now: "2026-10-23T22:00:00-07:00" });
        mockUseLatestBrief.mockReturnValue({ view: { brief, building: false, enabled: true, nextAt: "", nextSlot: "", empty: false, missing: [] }, loading: false, make: "idle", nextAllowedAt: "", makeNow: vi.fn(), refresh: vi.fn() });
        renderToday();
        await screen.findByTestId("today-day-track");
        expect(pauseMocks.dayShape).toHaveBeenCalledWith(requestedDate);
        expect(pauseMocks.dayShape.mock.calls.every((args) => args.length === 1)).toBe(true);
        {
            expect(screen.queryByTestId("today-day-now")).not.toBeInTheDocument();
            // R30: an ended calendar block keeps its outline and dims its tint, not the whole group.
            for (const group of screen.getAllByTestId("today-day-event-group")) expect(group).not.toHaveStyle({ opacity: "0.45" });
            expect(screen.queryByTestId("today-day-hold-group")).not.toBeInTheDocument();
            expect(screen.queryByTestId("today-day-caption")).not.toBeInTheDocument();
        }
    });
});


it('pauses topic refreshes while the retained Today page is hidden', () => {
    const page = render(<TodayPage handlers={handlers} active={false} />);
    expect(mockUseTodayTopics).toHaveBeenLastCalledWith(false);
    page.rerender(<TodayPage handlers={handlers} active />);
    expect(mockUseTodayTopics).toHaveBeenLastCalledWith(true);
});

it("opens a waiting handoff itself in the Today panel", async () => {
    mockUseTodayNeeds.mockReturnValue({ items: [{ id: "handoff-1", kind: "review", sentence: "Review the result", source: "Isoform", createdAt: 0, targetKind: "handoff", targetId: "handoff-1" }], loading: false, failed: false });
    renderToday();
    fireEvent.click(screen.getByRole("button", { name: "needsYou.review" }));
    expect(await screen.findByTestId("needs-task-panel")).toHaveTextContent("handoff-1");
});

it("answers Find time for a slipping commitment in the panel", async () => {
    const measured = vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockReturnValue({ width: 1400, height: 800, top: 0, left: 0, right: 1400, bottom: 800, x: 0, y: 0, toJSON: () => ({}) } as DOMRect);
    mockUseTodayNeeds.mockReturnValue({ items: [{ id: "slipping", kind: "commitment_slipping", sentence: "The memo is slipping", createdAt: 0, targetKind: "chat_item", targetId: "row" }], loading: false, failed: false });
    renderToday();
    fireEvent.click(screen.getByRole("button", { name: "holds.findTime" }));
    expect(await screen.findByTestId("today-find-time-panel")).toBeInTheDocument();
    await waitFor(() => expect(pauseMocks.findTime).toHaveBeenCalled());
    expect(pauseMocks.loadProactive).not.toHaveBeenCalled();
    measured.mockRestore();
});

it("places review lists before topics and one-time cards", () => {
    pageQueryCache.set("GetPortraitTodayCard", [], { renamed: false, name: ["blueprint", "builder"], sides: [1, 2, 3, 4], sentence: [{ text: "A portrait" }] });
    mockUseTodayNeeds.mockReturnValue({ items: [{ id: "waiting", kind: "review", sentence: "Waiting", createdAt: 0, targetKind: "handoff", targetId: "waiting" }], loading: false, failed: false });
    mockUseTodayDone.mockReturnValue({ items: [{ id: "done", kind: "work_finished", sentence: "Finished", at: 0, targetKind: "handoff", targetId: "done" }], loading: false, failed: false });
    mockUseTodayTopics.mockReturnValue({ topics: [], status: "ready", refresh: vi.fn() });
    renderToday();
    const needs = screen.getByTestId("today-needs-you");
    const done = screen.getByTestId("today-done-for-you");
    const topics = screen.getByTestId("today-topics");
    const portrait = screen.getByTestId("today-portrait-card");
    expect(needs.compareDocumentPosition(done) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(done.compareDocumentPosition(topics) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(topics.compareDocumentPosition(portrait) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(pauseMocks.recordSection).toHaveBeenCalledWith("needs_you", 1);
    expect(pauseMocks.recordSection).toHaveBeenCalledWith("done_for_you", 1);
    expect(pauseMocks.recordSection).toHaveBeenCalledWith("your_topics", 0);
    expect(JSON.stringify(pauseMocks.recordSection.mock.calls)).not.toContain("Waiting");
});

it("does not link first-day rows to sections that are absent", () => {
    firstDayMock.mockReturnValue({ day: 0, firstDays: true, history: null, calendar: null });
    mockUseTodayTopics.mockReturnValue({ topics: [{ id: "topic", name: "Topic" }], status: "unavailable", refresh: vi.fn() });
    const brief = { ...todayNextBrief, slot: "evening", asOf: new Date(2026, 9, 30, 18).toISOString() };
    mockUseLatestBrief.mockReturnValue({ view: { brief, building: false, enabled: true, nextAt: "", nextSlot: "", empty: true, missing: [] }, loading: false, make: "idle", nextAllowedAt: "", makeNow: vi.fn(), refresh: vi.fn() });
    renderToday();
    const card = screen.getByTestId("getting-to-know-you");
    const topics = card.querySelector("[data-know-row=topics]") as HTMLElement;
    const evening = card.querySelector("[data-know-row=evening]") as HTMLElement;
    expect(within(topics).queryByRole("button")).toBeNull();
    expect(within(evening).queryByRole("button")).toBeNull();
    expect(screen.queryByTestId("today-topics")).toBeNull();
    expect(screen.queryByTestId("brief-card")).toBeNull();
});

it("resolves a strip meeting and opens its exact readable past notes", async () => {
    pauseMocks.dayShape.mockResolvedValue(dayShapeFixture);
    pauseMocks.dayMeeting.mockResolvedValue({ kind: "notes", sessionId: "session-priya" });
    mockUseLatestBrief.mockReturnValue({ view: { brief: todayNextBrief, building: false, enabled: true, nextAt: "", nextSlot: "", empty: false, missing: [] }, loading: false, make: "idle", nextAllowedAt: "", makeNow: vi.fn(), refresh: vi.fn() });
    const onOpenMeetingNotes = vi.fn();
    renderToday({ ...handlers, onOpenMeetingNotes });
    const buttons = await screen.findAllByRole("button", { name: "dayStrip.openPrep" });
    fireEvent.click(buttons[0]);
    await waitFor(() => expect(pauseMocks.dayMeeting).toHaveBeenCalledWith("priya"));
    expect(onOpenMeetingNotes).toHaveBeenCalledWith("session-priya");
});

it("reopens a meeting's saved prep even when another entry point used different words", async () => {
    const meeting = { meetingId: "priya", meetingTitle: "1:1", startAt: Date.now() + 60_000, endAt: Date.now() + 600_000, personId: "p", personName: "Priya" };
    pauseMocks.dayShape.mockResolvedValue(dayShapeFixture);
    pauseMocks.dayMeeting.mockResolvedValue({ kind: "prep", meeting });
    pauseMocks.savedAnswer.mockResolvedValue({ question: "Earlier prep request", answer: "## Who's coming\nPriya", userItemId: "u", answerItemId: "a", madeAt: Date.now() });
    mockUseLatestBrief.mockReturnValue({ view: { brief: todayNextBrief, building: false, enabled: true, nextAt: "", nextSlot: "", empty: false, missing: [] }, loading: false, make: "idle", nextAllowedAt: "", makeNow: vi.fn(), refresh: vi.fn() });
    renderToday();
    fireEvent.click((await screen.findAllByRole("button", { name: "dayStrip.openPrep" }))[0]);
    expect(await screen.findByTestId("meeting-prep-who")).toHaveTextContent("Priya");
    expect(pauseMocks.sendMainCtx).not.toHaveBeenCalled();
});

it("passes an exact future meeting to the saved prep host", async () => {
    const meeting = { meetingId: "priya", meetingTitle: "1:1", startAt: Date.now() + 60_000, endAt: Date.now() + 600_000, personId: "p", personName: "Priya" };
    pauseMocks.dayShape.mockResolvedValue(dayShapeFixture);
    pauseMocks.dayMeeting.mockResolvedValue({ kind: "prep", meeting });
    mockUseLatestBrief.mockReturnValue({ view: { brief: todayNextBrief, building: false, enabled: true, nextAt: "", nextSlot: "", empty: false, missing: [] }, loading: false, make: "idle", nextAllowedAt: "", makeNow: vi.fn(), refresh: vi.fn() });
    renderToday();
    fireEvent.click((await screen.findAllByRole("button", { name: "dayStrip.openPrep" }))[0]);
    await waitFor(() => expect(pauseMocks.sendMainCtx).toHaveBeenCalledWith("beforeYouTalk.briefPrompt", expect.objectContaining({ sourceKind: "calendar", sourceId: "priya", meetingPrep: true, meetingStartMs: meeting.startAt })));
    expect(screen.getByTestId("main-chat-panel")).toBeInTheDocument();
});

it("revalidates a waiting chat item by its exact moment in the Today panel", async () => {
    mockUseTodayNeeds.mockReturnValue({ items: [{ id: "moment-1", kind: "work_blocked", sentence: "Answer the question", source: "Isoform", createdAt: 0, targetKind: "chat_item", targetId: "row-1", chatId: "main" }], loading: false, failed: false });
    renderToday();
    fireEvent.click(screen.getByRole("button", { name: "needsYou.answer" }));
    await waitFor(() => expect(pauseMocks.loadProactive).toHaveBeenCalledWith("moment-1"));
});

it("opens a finished answer in the Today panel and sends the overflow to Done this week", async () => {
    const rows = ["one", "two", "three", "four"].map((id) => ({ id: `answer:${id}`, kind: "answer_ready", sentence: `Result ${id}`, at: 0, targetKind: "chat_item", targetId: id, chatId: "main" }));
    mockUseTodayDone.mockReturnValue({ items: rows, loading: false, failed: false });
    const onOpenDoneTasks = vi.fn();
    renderToday({ ...handlers, onOpenDoneTasks });
    fireEvent.click(screen.getByRole("button", { name: "doneForYou.more" }));
    expect(onOpenDoneTasks).toHaveBeenCalledOnce();
    fireEvent.click(screen.getByRole("button", { name: "Result one" }));
    expect(await screen.findByTestId("done-answer-panel")).toHaveTextContent("answer:one");
});

it("opens the finished run's status in the panel and carries its exact destination", async () => {
    mockUseTodayDone.mockReturnValue({ items: [{ id: "status:one", kind: "playbook_result", sentence: "The routine finished.", at: 0, targetKind: "automation", targetId: "job-1", runId: "run-2" }], loading: false, failed: false });
    const onOpenReportTarget = vi.fn();
    renderToday(handlers, { onOpenReportTarget });
    fireEvent.click(screen.getByRole("button", { name: "doneForYou.review" }));
    expect(await screen.findByTestId("today-done-result-panel")).toHaveTextContent("The routine finished.");
    await waitFor(() => expect(MarkTodayDoneOpened).toHaveBeenCalledWith("status:one"));
    fireEvent.click(screen.getByRole("button", { name: "doneForYou.openAutomation" }));
    expect(onOpenReportTarget).toHaveBeenCalledWith({ kind: "automation", id: "job-1", runId: "run-2" }, expect.any(Function));
    act(() => onOpenReportTarget.mock.calls[0][1]());
    fireEvent.click(screen.getByRole("alert"));
    expect(onOpenReportTarget).toHaveBeenCalledTimes(2);
});


it("shows a proven reply in place when a waiting row was handled elsewhere", async () => {
    const row = { id: "reply-moment", kind: "waiting_on_reply", sentence: "Review the reply", source: "Mail", createdAt: 0, targetKind: "chat_item", targetId: "row-1" };
    mockUseTodayNeeds.mockReturnValue({ items: [row], loading: false, failed: false });
    pauseMocks.needResolution.mockResolvedValueOnce({ kind: "you_replied", person: "Dana", at: 1793380320000 });
    const page = renderToday();
    fireEvent.click(screen.getByRole("button", { name: "needsYou.review" }));
    expect(await screen.findByText("needsYou.alreadyReplied")).toBeInTheDocument();
    expect(pauseMocks.loadProactive).not.toHaveBeenCalled();
    mockUseTodayNeeds.mockReturnValue({ items: [], loading: false, failed: false });
    page.rerender(<TodayPage handlers={handlers} active={false} />);
    page.rerender(<TodayPage handlers={handlers} active />);
    expect(screen.getByText("needsYou.alreadyReplied")).toBeInTheDocument();
    mockUseLatestBrief.mockReturnValue({ view: { brief: { ...todayNextBrief, asOf: new Date(Date.now() + 60_000).toISOString() }, empty: false, enabled: true, missing: [] }, loading: false, make: "idle", nextAllowedAt: "", makeNow: vi.fn(), refresh: vi.fn() });
    page.rerender(<TodayPage handlers={handlers} active />);
    await waitFor(() => expect(screen.queryByText("needsYou.alreadyReplied")).not.toBeInTheDocument());
});


it("keeps a proven reply with no usable name in place as your reply", async () => {
    const row = { id: "reply-moment", kind: "waiting_on_reply", sentence: "Review the reply", source: "Mail", createdAt: 0, targetKind: "chat_item", targetId: "row-1" };
    mockUseTodayNeeds.mockReturnValue({ items: [row], loading: false, failed: false });
    pauseMocks.needResolution.mockResolvedValueOnce({ kind: "you_replied", at: 1793380320000 });
    renderToday();
    fireEvent.click(screen.getByRole("button", { name: "needsYou.review" }));
    expect(await screen.findByText("needsYou.alreadyRepliedNoName")).toBeInTheDocument();
    expect(pauseMocks.loadProactive).not.toHaveBeenCalled();
});

it("opens the exact task thread from a source-bound weekly result", async () => {
    const brief = { ...todayNextBrief, slot: "evening", yourWeek: { start: "2026-10-19", end: "2026-10-23", lead: "Your week.", done: [{ id: "result", kind: "outcome", title: "Prepared the file", taskId: "task-42" }] } };
    mockUseLatestBrief.mockReturnValue({ view: { brief, building: false, enabled: true, missing: [] }, loading: false, make: "idle", makeNow: vi.fn(), refresh: vi.fn() });
    renderToday();
    fireEvent.click(screen.getByRole("button", { name: "Prepared the file" }));
    expect(await screen.findByTestId("needs-task-panel")).toHaveTextContent("task-42");
});

it("offers an inline retry when a waiting chat item cannot open", async () => {
    mockUseTodayNeeds.mockReturnValue({ items: [{ id: "moment-failed", kind: "work_blocked", sentence: "Answer", source: "Isoform", createdAt: 0, targetKind: "chat_item", targetId: "row" }], loading: false, failed: false });
    pauseMocks.loadProactive.mockRejectedValueOnce(new Error("unavailable"));
    renderToday();
    fireEvent.click(screen.getByRole("button", { name: "needsYou.answer" }));
    const failure = await screen.findByRole("alert");
    expect(failure).toHaveTextContent("actions.chatOpenFailed");
    fireEvent.click(failure);
    await waitFor(() => expect(pauseMocks.loadProactive).toHaveBeenCalledTimes(2));
});


it("keeps a weekly task open when an earlier meeting lookup finishes", async () => {
    let finish!: (value: unknown) => void;
    pauseMocks.dayMeeting.mockReturnValueOnce(new Promise((resolve) => { finish = resolve; }));
    pauseMocks.dayShape.mockResolvedValue(dayShapeFixture);
    const brief = { ...todayNextBrief, slot: "evening", yourWeek: { start: "2026-10-19", end: "2026-10-23", lead: "Your week.", done: [{ id: "result", kind: "outcome", title: "Prepared the file", taskId: "task-42" }] } };
    mockUseLatestBrief.mockReturnValue({ view: { brief, building: false, enabled: true, missing: [] }, loading: false, make: "idle", makeNow: vi.fn(), refresh: vi.fn() });
    renderToday();
    fireEvent.click((await screen.findAllByRole("button", { name: "dayStrip.openPrep" }))[0]);
    fireEvent.click(screen.getByRole("button", { name: "Prepared the file" }));
    await act(async () => finish({ kind: "prep", meeting: { meetingId: "priya", meetingTitle: "1:1", startAt: Date.now() + 60000, endAt: Date.now() + 600000, personId: "p", personName: "Priya" } }));
    expect(screen.getByTestId("needs-task-panel")).toHaveTextContent("task-42");
    expect(pauseMocks.sendMainCtx).not.toHaveBeenCalled();
});

it("ignores a late waiting-item check after a meeting is selected", async () => {
    let finish!: (result: unknown) => void;
    pauseMocks.needResolution.mockReturnValueOnce(new Promise((resolve) => { finish = resolve; }));
    mockUseTodayNeeds.mockReturnValue({ items: [{ id: "reply-moment", kind: "waiting_on_reply", sentence: "Review", source: "Mail", createdAt: 0, targetKind: "chat_item", targetId: "row" }], loading: false, failed: false });
    pauseMocks.dayShape.mockResolvedValue(dayShapeFixture);
    pauseMocks.dayMeeting.mockResolvedValueOnce({ kind: "notes", sessionId: "session-priya" });
    mockUseLatestBrief.mockReturnValue({ view: { brief: todayNextBrief, building: false, enabled: true, missing: [] }, loading: false, make: "idle", makeNow: vi.fn(), refresh: vi.fn() });
    const onOpenMeetingNotes = vi.fn();
    renderToday({ ...handlers, onOpenMeetingNotes });
    fireEvent.click(screen.getByRole("button", { name: "needsYou.review" }));
    fireEvent.click((await screen.findAllByRole("button", { name: "dayStrip.openPrep" }))[0]);
    await waitFor(() => expect(onOpenMeetingNotes).toHaveBeenCalledWith("session-priya"));
    await act(async () => finish({ kind: "" }));
    expect(pauseMocks.loadProactive).not.toHaveBeenCalled();
});


it("ignores a late waiting-item check after opening a pinned topic", async () => {
    let finish!: (result: unknown) => void;
    pauseMocks.needResolution.mockReturnValueOnce(new Promise((resolve) => { finish = resolve; }));
    mockUseTodayNeeds.mockReturnValue({ items: [{ id: "reply-moment", kind: "waiting_on_reply", sentence: "Review", source: "Mail", createdAt: 0, targetKind: "chat_item", targetId: "row" }], loading: false, failed: false });
    const view = render(<TodayPage handlers={handlers} />);
    fireEvent.click(screen.getByRole("button", { name: "needsYou.review" }));
    const topic = { id: "atlas", name: "Atlas migration", kind: "work" as const, pinned: true };
    view.rerender(<TodayPage handlers={handlers} topicRequest={{ topic, nonce: 1 }} />);
    await act(async () => finish({ kind: "" }));
    expect(pauseMocks.loadProactive).not.toHaveBeenCalled();
});


it.each([
    { id: "review-work", kind: "review", sentence: "Review", source: "Isoform", createdAt: 0, targetKind: "handoff", targetId: "review-work" },
    { id: "decision", kind: "decision_due", sentence: "Review", source: "Mail", createdAt: 0, targetKind: "chat_item", targetId: "row" },
])("checks the source of a $kind row: resolved shows no button, a failure offers Try again", async (row) => {
    mockUseTodayNeeds.mockReturnValue({ items: [row], loading: false, failed: false });
    pauseMocks.needResolution.mockRejectedValueOnce(new Error("read failed")).mockResolvedValueOnce({ kind: "resolved" });
    renderToday();
    fireEvent.click(screen.getByRole("button", { name: "needsYou.review" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("needsYou.couldNotCheck");
    expect(pauseMocks.needResolution).toHaveBeenLastCalledWith(row.id, row.kind, row.targetKind, row.targetId);
    expect(pauseMocks.loadProactive).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "needsYou.tryAgain" }));
    expect(await screen.findByText("needsYou.noLongerNeeded")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "needsYou.tryAgain" })).not.toBeInTheDocument();
    expect(pauseMocks.loadProactive).not.toHaveBeenCalled();
});

it("keeps a failed waiting-source check in its row and retries without opening the target", async () => {
    const row = { id: "reply-failure", kind: "waiting_on_reply", sentence: "Review", source: "Mail", createdAt: 0, targetKind: "chat_item", targetId: "row" };
    mockUseTodayNeeds.mockReturnValue({ items: [row], loading: false, failed: false });
    pauseMocks.needResolution.mockRejectedValueOnce(new Error("read failed")).mockRejectedValueOnce(new Error("read failed again"));
    renderToday();
    fireEvent.click(screen.getByRole("button", { name: "needsYou.review" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("needsYou.couldNotCheck");
    expect(pauseMocks.loadProactive).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "needsYou.tryAgain" }));
    await waitFor(() => expect(screen.getByRole("alert")).toHaveTextContent("needsYou.stillFailed"));
    expect(pauseMocks.loadProactive).not.toHaveBeenCalled();
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
