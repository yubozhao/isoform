import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { setComputerClock } from "@/lib/brief-time";

import i18n from "@/i18n";
import { TasksPage, resetPlaybookOfferFailures, startOfTaskWeek, taskTitleFrom } from "./TasksPage";
import { useTasksNeedsYouCount } from "@/hooks/useTasksNeedsYou";
import { useTodayNeeds } from "@/hooks/useTodayNeeds";
import { TodayNeedsSection } from "@/components/today/TodayNeedsSection";
import type { HandoffEntry, TaskSuggestionView, UpcomingItem } from "@/lib/wails-shim-app";
import type { OpenAnswer } from "@/lib/open-answers";
import { refreshOpenAnswers } from "@/hooks/useOpenAnswers";
import { resetPageQueries } from "@/lib/page-queries";
import { pageQueryCache } from "@/lib/query-cache";
import { clearPendingTaskStarts } from "./pending-task-starts";

const mocks = vi.hoisted(() => ({
    desktopTasks: vi.fn(), startOffer: vi.fn(), firstWeekDay: vi.fn(), deliverables: vi.fn(), findMore: vi.fn(), abort: vi.fn(), sops: vi.fn(), playbooks: vi.fn(), authorStatus: vi.fn(),
    putOff: vi.fn(), acceptPlaybook: vi.fn(), declinePlaybook: vi.fn(), snoozePlaybook: vi.fn(), openFile: vi.fn(), revealFile: vi.fn(),
    needs: vi.fn(), list: vi.fn(), answers: vi.fn(), change: vi.fn(), cancel: vi.fn(), offers: vi.fn(), prepare: vi.fn(), decide: vi.fn(), create: vi.fn(), accept: vi.fn(), picker: vi.fn(), parseDump: vi.fn(), addDump: vi.fn(),
    startAgain: vi.fn(), emit: vi.fn(), recordTasks: vi.fn(async () => undefined), recordTasksSeen: vi.fn(async () => undefined),
    updated: null as null | (() => void), events: new Map<string, Set<(payload?: unknown) => unknown>>(), toast: Object.assign(vi.fn(), { success: vi.fn(), error: vi.fn() }),
}));
vi.mock("@/lib/wails-shim-app", () => ({
    ListDesktopTaskThreads: mocks.desktopTasks, StartDesktopTaskOffer: mocks.startOffer, StartHandoffAgain: mocks.startAgain,
    GetNeedsYou: mocks.needs, PutOffNeedsYou: mocks.putOff, ActOnDesktopTask: vi.fn(async () => undefined), RecordTodaySectionSeen: vi.fn(async () => undefined), LoadMainChatProactiveItem: vi.fn(async () => null), RecordMainChatProactiveReceipt: vi.fn(async () => undefined),
    ListHandoffs: mocks.list, ListTaskSuggestions: mocks.offers, DecideTaskSuggestion: mocks.decide,
    ListOpenAnswers: mocks.answers, ChangeOpenAnswer: mocks.change, CancelOpenAnswer: mocks.cancel,
    PrepareTaskSuggestion: mocks.prepare,
    CreateTask: mocks.create, AcceptHandoff: mocks.accept, SelectWorkDir: mocks.picker,
    ParseBrainDump: mocks.parseDump, AddBrainDump: mocks.addDump,
    GetFirstWeekDay: mocks.firstWeekDay, ListDeliverables: mocks.deliverables, FindMoreTaskSuggestions: mocks.findMore, AbortHandoff: mocks.abort,
    ListSOPs: mocks.sops, GetPlaybooksState: mocks.playbooks, GetSOPAuthorStatus: mocks.authorStatus,
    AcceptPlaybookSuggestion: mocks.acceptPlaybook, DeclinePlaybookSuggestion: mocks.declinePlaybook, SnoozePlaybookSuggestion: mocks.snoozePlaybook,
    OpenDeliverableFile: mocks.openFile, RevealDeliverableFile: mocks.revealFile,
    RecordTasksAction: mocks.recordTasks, RecordTasksSectionSeen: mocks.recordTasksSeen,
}));
vi.mock("./BriefDetailPane", () => ({ BriefDetailPane: ({ slot, onClose, onChangeTimes }: { slot: string; onClose: () => void; onChangeTimes?: () => void }) => <div data-testid="brief-detail">{slot}<button onClick={onClose}>close brief</button><button onClick={onChangeTimes}>Change times</button></div> }));
vi.mock("./TaskThreadPane", () => ({ TaskThreadPane: ({ id, onGone }: { id: string; onGone?: () => void }) => <div data-testid="task-thread">{id}<button onClick={onGone}>gone</button></div> }));
vi.mock("../../../wailsjs/runtime/runtime", () => ({
    EventsEmit: mocks.emit,
    EventsOn: vi.fn((event: string, callback: (payload?: unknown) => unknown) => {
        if (event === "handoff:updated") mocks.updated = callback;
        const handlers = mocks.events.get(event) ?? new Set();
        handlers.add(callback);
        mocks.events.set(event, handlers);
        return () => handlers.delete(callback);
    }),
}));
vi.mock("sonner", () => ({ toast: mocks.toast }));


function handoff(id: string, status: string, extra: Partial<HandoffEntry> = {}): HandoffEntry {
    return { id, title: id, status, updatedAt: Date.now(), body: "", ...extra } as HandoffEntry;
}
const offer = (id: string): TaskSuggestionView => ({ id, kind: "suggestion", title: id, prompt: "Draft " + id, why: "A useful task", whyLocalized: { "zh-CN": "一个有用的任务" }, version: "1", evidence: { text: "A useful task", textLocalized: { "zh-CN": "一个有用的任务" }, source: "mail", at: "2026-10-30T10:00:00Z" } });
function mount(extra = {}) {
    const props = { onOpenChat: vi.fn(), onStartHandoff: vi.fn(async () => true), onStartInPlace: vi.fn(async () => true), upcoming: { items: [] as UpcomingItem[], loading: false }, ...extra };
    render(<TasksPage {...props} />);
    return props;
}
async function settleDeck() { await act(async () => { await new Promise((resolve) => setTimeout(resolve, 450)); }); }
function deferred<T>() {
    let resolve!: (value: T) => void;
    const promise = new Promise<T>((done) => { resolve = done; });
    return { promise, resolve };
}
async function emit(event: string, payload?: unknown) {
    await act(async () => {
        await Promise.all([...(mocks.events.get(event) ?? [])].map((handler) => handler(payload)));
        if (["handoff:created", "handoff:updated", "main-chat:waiting-changed", "session:items-appended", "open-answers:changed"].includes(event)) {
            await Promise.all([...(mocks.events.get("today:needs-changed") ?? [])].map((handler) => handler()));
        }
    });
}
async function emitWithdrawal(event: string) {
    if (event !== "model-accounts:updated") {
        await emit(event);
        return;
    }
    const account = { provider: "claude", kind: "subscription", name: "Bo", on: true };
    await emit(event, { accounts: [account] });
    await emit(event, { accounts: [] });
}
beforeEach(async () => {
    vi.clearAllMocks();
    resetPlaybookOfferFailures();
    // Tasks reads through the shared page cache (snappy 1.0, cache first).
    resetPageQueries();
    await i18n.changeLanguage("en");
    mocks.desktopTasks.mockResolvedValue({ tasks: [{ id: "done", group: "done", updatedAt: new Date().toISOString(), outcome: { kind: "completed", by: "isoform", at: new Date().toISOString(), params: { summary: "The task is complete." } } }] });
    mocks.startOffer.mockResolvedValue({});
    mocks.list.mockResolvedValue([]);
    mocks.needs.mockImplementation(async () => {
        const read = mocks.list.getMockImplementation();
        const rows = read ? await read() : [];
        const items = rows.filter((row: HandoffEntry) => ["active", "needs_review", "blocked"].includes(row.status)).map((row: HandoffEntry) => ({ id: row.id, kind: row.status === "blocked" ? "work_blocked" : "review", sentence: row.title, createdAt: 0, targetKind: "handoff", targetId: row.id }));
        return { items, total: items.length };
    });
    mocks.answers.mockResolvedValue([]);
    mocks.change.mockResolvedValue(null);
    mocks.cancel.mockResolvedValue(null);
    mocks.offers.mockResolvedValue([]);
    mocks.prepare.mockImplementation(async (id, version) => ({ prompt: "Draft " + id, reference: { id, version } }));
    mocks.decide.mockResolvedValue(undefined);
    mocks.putOff.mockResolvedValue(undefined);
    mocks.accept.mockResolvedValue({ handoffId: "active", sessionId: "chat" });
    mocks.picker.mockResolvedValue("/chosen");
    mocks.create.mockResolvedValue(handoff("new", "active"));
    mocks.parseDump.mockResolvedValue({ items: [
        { id: "draft", title: "Reply to Lena", who: "draft", person: "Lena" },
        { id: "reminder", title: "Book dentist", who: "reminder" },
        { id: "code", title: "Fix tests", who: "codex" },
    ] });
    mocks.addDump.mockResolvedValue(["one", "two"]);
    mocks.firstWeekDay.mockResolvedValue(5);
    mocks.deliverables.mockResolvedValue([]);
    mocks.findMore.mockResolvedValue({ offers: [] });
    mocks.abort.mockResolvedValue(undefined);
    mocks.sops.mockResolvedValue([]);
    mocks.playbooks.mockResolvedValue({ plans: [], channels: {}, defaultChannels: [] });
    mocks.authorStatus.mockResolvedValue(null);
    mocks.openFile.mockResolvedValue(undefined);
    mocks.revealFile.mockResolvedValue(undefined);
    mocks.snoozePlaybook.mockResolvedValue({});
});
afterEach(async () => {
    setComputerClock("12h");
    clearPendingTaskStarts();
    mocks.answers.mockResolvedValue([]);
    await act(async () => { await refreshOpenAnswers(); });
});

function openAnswer(overrides: Partial<OpenAnswer> = {}): OpenAnswer {
    return {
        id: "answer", sessionId: "session-answer", messageId: "message-answer", question: "Will it rain?",
        kind: "weather", settles: "result", delivery: "around", windowStart: "2026-10-27T10:00:00-07:00",
        expiresAt: "2026-11-01T00:00:00Z", posts: 0, state: "open", createdAt: "2026-10-26T09:00:00-07:00",
        ...overrides,
    };
}

// Snappy 1.0 (section 2): Tasks paints all four groups from the last read at
// once; before any read it shows static rows in their shape, never
// "Loading tasks…" and never a group that wrongly says it is empty.
describe("Tasks first paint", () => {
    it("uses the same translated waiting sentence as Today", async () => {
        mocks.needs.mockResolvedValue({ items: [{ id: "one", kind: "review", sentence: "Review it", sentenceLocalized: { en: "Review it", "zh-CN": "查看它" }, createdAt: 0, targetKind: "handoff", targetId: "one" }], total: 1 });
        mount();
        expect(await screen.findByText("Review it")).toBeInTheDocument();
        await act(async () => { await i18n.changeLanguage("zh-CN"); });
        expect(screen.getByText("查看它")).toBeInTheDocument();
    });

    it.each([["needs", "needs-you"], ["done", "done"]] as const)("opens the %s section requested by Today", async (section, target) => {
        const scroll = vi.fn();
        Object.defineProperty(HTMLElement.prototype, "scrollIntoView", { configurable: true, value: scroll });
        try {
            mocks.list.mockResolvedValue([handoff("waiting", "blocked"), handoff("done", "completed")]);
            mount({ focus: { section, nonce: 1 } });
            await waitFor(() => expect(scroll.mock.contexts.some((element) => (element as HTMLElement).dataset.tasksSection === target)).toBe(true));
        } finally {
            delete (HTMLElement.prototype as { scrollIntoView?: unknown }).scrollIntoView;
        }
    });

    it("paints the groups from the cache on the first frame, then updates in place", async () => {
        pageQueryCache.set("ListHandoffs", [], [handoff("Cached review", "needs_review")]);
        pageQueryCache.set("GetNeedsYou", [0], { items: [{ id: "Cached review", kind: "review", sentence: "Cached review", createdAt: 0, targetKind: "handoff", targetId: "Cached review" }], total: 1 });
        mocks.list.mockResolvedValue([handoff("Cached review", "needs_review"), handoff("Fresh work", "in_progress")]);
        mount();
        expect(screen.getByText("Cached review")).toBeInTheDocument();
        expect(screen.queryByText("Loading tasks…")).not.toBeInTheDocument();
        expect(screen.queryByTestId("task-row-placeholder")).not.toBeInTheDocument();
        expect(mocks.list).not.toHaveBeenCalled();
    });

    it("shows row-shaped placeholders, not empty groups, until the first read lands", async () => {
        const read = deferred<HandoffEntry[]>();
        mocks.list.mockReturnValue(read.promise);
        mount();
        expect(screen.getAllByTestId("task-row-placeholder")).toHaveLength(1);
        expect(screen.queryByText("Loading tasks…")).not.toBeInTheDocument();
        await act(async () => { read.resolve([handoff("Ready", "needs_review")]); });
        expect(screen.queryByTestId("task-row-placeholder")).not.toBeInTheDocument();
        expect(screen.getByText("Ready")).toBeInTheDocument();
        expect(screen.queryByRole("heading", { name: "Running" })).toBeNull();
        expect(screen.queryByRole("heading", { name: "Done this week" })).toBeNull();
    });

    it("shows loading shapes in section order, then hides empty groups but keeps Up next and Scheduled", async () => {
        const handoffs = deferred<HandoffEntry[]>();
        const offers = deferred<TaskSuggestionView[]>();
        mocks.list.mockReturnValue(handoffs.promise);
        mocks.offers.mockReturnValue(offers.promise);
        mount({ upcoming: { items: [], loading: true } });
        expect(screen.getAllByTestId("task-row-placeholder")).toHaveLength(2);
        expect(screen.getByTestId("task-deck-placeholder")).toBeInTheDocument();
        expect(screen.getAllByRole("heading", { level: 2 }).map((heading) => heading.textContent)).toEqual(["Needs you", "Up next", "Scheduled"]);
        expect(screen.queryByText("0")).toBeNull();
        expect(screen.queryByText("Loading tasks…")).toBeNull();
        await act(async () => { handoffs.resolve([]); });
        expect(screen.getAllByRole("heading", { level: 2 }).map((heading) => heading.textContent)).toEqual(["Up next", "Scheduled"]);
        expect(screen.getByTestId("task-deck-placeholder")).toBeInTheDocument();
        await act(async () => { offers.resolve([]); });
        expect(screen.queryByTestId("task-deck-placeholder")).not.toBeInTheDocument();
        expect(screen.getByText("Nothing up next.")).toBeInTheDocument();
        expect(screen.queryByText("0")).toBeNull();
    });

    it("reads fresh after a change event, never the cached rows", async () => {
        pageQueryCache.set("ListHandoffs", [], [handoff("Before", "needs_review")]);
        mount();
        mocks.list.mockResolvedValue([handoff("After", "needs_review")]);
        await emit("handoff:updated");
        await waitFor(() => expect(screen.getByText("After")).toBeInTheDocument());
    });
});

describe("Tasks board", () => {
    it("hides every empty section and shows Running when an open answer arrives", async () => {
        mocks.answers.mockResolvedValue([]);
        mocks.list.mockResolvedValue([]);
        mount();
        await waitFor(() => {
            expect(screen.queryByTestId("task-row-placeholder")).toBeNull();
            expect(screen.queryByTestId("task-deck-placeholder")).toBeNull();
        });
        expect(screen.getAllByRole("heading", { level: 2 }).map((heading) => heading.textContent)).toEqual(["Up next", "Scheduled"]);
        expect(screen.queryByText("0")).toBeNull();
        expect(screen.getByTestId("tasks-line")).toHaveTextContent("Nothing needs you.");
        mocks.answers.mockResolvedValue([openAnswer({ question: "An open answer" })]);
        await act(async () => { await refreshOpenAnswers(); });
        expect(await screen.findByText("An open answer")).toBeInTheDocument();
        expect(screen.getAllByRole("heading", { level: 2 }).map((heading) => heading.textContent)).toEqual(["Up next", "Running", "Scheduled"]);
    });

    it("shows open answers after handoffs, counts them, and navigates to their source message", async () => {
        mocks.list.mockResolvedValue([handoff("running task", "in_progress")]);
        mocks.answers.mockResolvedValue([
            openAnswer({ question: "Rain?" }),
            openAnswer({ id: "reply", question: "Mina reply?", settles: "reply", delivery: "settled_only", watchedEvent: { who: "Mina", label: "reply" } }),
            openAnswer({ id: "settled", question: "Settled?", delivery: "settled_only" }),
            openAnswer({ id: "brief", question: "Brief?", delivery: "brief" }),
        ]);
        const onOpenChatMessage = vi.fn();
        mount({ onOpenChatMessage });
        const running = await screen.findByRole("region", { name: "Running" });
        const question = await within(running).findByText("Rain?");
        expect(within(running).getByText(/Lets you know around/)).toBeInTheDocument();
        expect(within(running).getByText("When Mina replies")).toBeInTheDocument();
        expect(within(running).getByText("When it's settled")).toBeInTheDocument();
        expect(within(running).getByText("In tomorrow's brief")).toBeInTheDocument();
        expect(within(running).getByRole("progressbar")).toBeInTheDocument();
        expect(within(running).getByText("running task").compareDocumentPosition(question) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
        expect(question.closest("div.flex.w-full")?.querySelector("svg.lucide-clock")).toBeInTheDocument();
        expect(within(running).getAllByRole("progressbar")).toHaveLength(1);
        expect(screen.getByText("5 running")).toBeInTheDocument();
        const answerRow = question.closest("div.flex.w-full")!;
        fireEvent.click(answerRow.querySelector("button")!);
        expect(onOpenChatMessage).toHaveBeenCalledWith("session-answer", "message-answer");
        // No count beside a section heading (tasks-review v1.0.3 #5).
        expect(running.querySelector("header")).toHaveTextContent(/^Running$/);
    });

    it("applies open-answer delivery changes and cancellation from the row menu", async () => {
        const user = userEvent.setup();
        mocks.answers.mockResolvedValue([openAnswer({ question: "Menu question" })]);
        mocks.change.mockResolvedValue(null);
        mocks.cancel.mockResolvedValue(null);
        mount();
        await screen.findByText("Menu question");
        const row = (): HTMLElement => screen.getByText("Menu question").closest("div.flex.w-full") as HTMLElement;
        await user.click(within(row()).getByRole("button", { name: "More options" }));
        expect(await screen.findByText("Change")).toBeInTheDocument();
        await user.click(await screen.findByRole("menuitem", { name: /Around/ }));
        await waitFor(() => expect(mocks.change).toHaveBeenCalledWith("answer", "around"));
        await user.click(within(row()).getByRole("button", { name: "More options" }));
        await user.click(await screen.findByRole("menuitem", { name: /Only when it's settled/ }));
        await waitFor(() => expect(mocks.change).toHaveBeenCalledWith("answer", "settled_only"));
        await user.click(within(row()).getByRole("button", { name: "More options" }));
        await user.click(await screen.findByRole("menuitem", { name: "In tomorrow's brief" }));
        await waitFor(() => expect(mocks.change).toHaveBeenCalledWith("answer", "brief"));
        await user.click(within(row()).getByRole("button", { name: "More options" }));
        await user.click(await screen.findByRole("menuitem", { name: "No need" }));
        await waitFor(() => expect(mocks.cancel).toHaveBeenCalledWith("answer"));
    });

    it("makes No need single-flight", async () => {
        let resolveCancel!: (value: null) => void;
        mocks.answers.mockResolvedValue([openAnswer()]);
        mocks.cancel.mockImplementation(() => new Promise((resolve) => { resolveCancel = resolve; }));
        mount();
        const question = await screen.findByText("Will it rain?");
        const row = question.closest("div.flex.w-full") as HTMLElement;
        const user = userEvent.setup();
        await user.click(within(row).getByRole("button", { name: "More options" }));
        const cancel = await screen.findByRole("menuitem", { name: "No need" });
        fireEvent.click(cancel);
        fireEvent.click(cancel);
        expect(mocks.cancel).toHaveBeenCalledTimes(1);
        await act(async () => { resolveCancel(null); });
    });

    it.each(["memory:withdrawn", "sensitive-apps:updated", "closed-eyes:changed", "permissions:changed", "background-listening:changed", "model-accounts:updated", "session:deleted"])("clears Running open-answer rows synchronously on %s", async (event) => {
        const record = openAnswer();
        mocks.answers.mockResolvedValue([record]);
        mount();
        expect(await screen.findByText(record.question)).toBeInTheDocument();

        const reread = deferred<OpenAnswer[]>();
        mocks.answers.mockImplementation(() => reread.promise);
        await emitWithdrawal(event);
        expect(screen.queryByText(record.question)).toBeNull();
        await act(async () => { reread.resolve([]); await reread.promise; });
    });
    it("groups real statuses, progress and calendar-week completions", async () => {
        mocks.offers.mockResolvedValue([offer("next suggestion")]);
        mocks.list.mockResolvedValue([
            handoff("active", "active"), handoff("review", "needs_review"), handoff("blocked", "blocked", { blockedReason: "Needs access" }),
            handoff("running", "in_progress", { progress: { done: 2, total: 3, label: "Tests" }, runner: "claude_code" }),
            handoff("unknown", "in_progress"), handoff("done", "completed"),
            handoff("last week", "completed", { updatedAt: startOfTaskWeek(new Date()) - 1 }), handoff("aborted", "aborted"),
        ]);
        mount();
        const needs = await screen.findByRole("region", { name: "Needs you" });
        await screen.findByRole("heading", { name: "Up next" });
        expect(screen.getAllByRole("heading", { level: 2 }).map((heading) => heading.textContent)).toEqual(["Needs you", "Up next", "Running", "Scheduled", "Done this week"]);
        expect(screen.getByTestId("tasks-line")).toHaveTextContent("3 need you · 2 running");
        expect(await within(needs).findByText("active")).toBeInTheDocument();
        expect(within(needs).getByText("review")).toBeInTheDocument();
        expect(within(needs).getByText("Needs access")).toBeInTheDocument();
        expect(screen.getByText("Claude Code on this computer · step 3 of 3 · Tests")).toBeInTheDocument();
        expect(screen.getAllByRole("progressbar")[0]).toHaveAttribute("aria-valuenow", "2");
        expect(screen.getAllByRole("progressbar")[1]).not.toHaveAttribute("aria-valuenow");
        expect(within(screen.getByRole("region", { name: "Done this week" })).getByText("done")).toBeInTheDocument();
        expect(screen.queryByText("last week")).toBeNull();
        // K1: a legacy aborted row with no stopper goes to Done.
        expect(within(screen.getByRole("region", { name: "Done this week" })).getByText("aborted")).toBeInTheDocument();
        expect(screen.getByText(/^Stopped at /)).toBeInTheDocument();
    });
    it("starts active work through the existing acceptance and execution callbacks", async () => {
        mocks.list.mockResolvedValue([handoff("active", "active")]);
        const props = mount();
        fireEvent.click(await within(await screen.findByRole("region", { name: "Needs you" })).findByRole("button", { name: "Do it" }));
        expect(await screen.findByText("Started")).toBeInTheDocument();
        expect(screen.getByRole("button", { name: "Undo" })).toBeInTheDocument();
        expect(mocks.accept).not.toHaveBeenCalled();
        await waitFor(() => expect(props.onStartHandoff).toHaveBeenCalledWith({ handoffId: "active", sessionId: "chat" }), { timeout: 7000 });
        expect(mocks.accept).toHaveBeenCalledWith("active");
    }, 10000);
    it("opens Review in the reused reader and returns to the board", async () => {
        mocks.list.mockResolvedValue([handoff("review", "needs_review")]);
        mount();
        fireEvent.click(await screen.findByRole("button", { name: "Review" }));
        expect(screen.getByTestId("task-thread")).toHaveTextContent("review");
        fireEvent.click(screen.getByRole("button", { name: "Back to Tasks" }));
        expect(screen.getByRole("heading", { name: "What Isoform is doing for you" })).toBeInTheDocument();
    });
    it("opens a supplied handoff ID and retains the full list", async () => {
        mocks.list.mockResolvedValue([handoff("done", "completed")]);
        mount({ openHandoffId: "external" });
        expect(screen.getByTestId("task-thread")).toHaveTextContent("external");
        fireEvent.click(screen.getByRole("button", { name: "Back to Tasks" }));
        fireEvent.click(await screen.findByRole("button", { name: "Show all" }));
        expect(screen.getByTestId("all-done")).toBeInTheDocument();
    });
    it("closes the thread when the window's back control is used", async () => {
        mocks.list.mockResolvedValue([handoff("review", "needs_review")]);
        const props = { onOpenChat: vi.fn(), onStartHandoff: vi.fn(async () => true), backSignal: 0 };
        const view = render(<TasksPage {...props} />);
        fireEvent.click(await screen.findByRole("button", { name: "Review" }));
        expect(screen.getByTestId("task-thread")).toBeInTheDocument();
        view.rerender(<TasksPage {...props} backSignal={1} />);
        await waitFor(() => expect(screen.queryByTestId("task-thread")).toBeNull());
    });
    it("refreshes on handoff events and reports load failures", async () => {
        mocks.list.mockRejectedValueOnce(new Error("offline"));
        mount();
        expect(await screen.findByTestId("tasks-refresh-failed")).toHaveTextContent("Couldn't refresh · Try again");
        expect(screen.queryByRole("heading", { name: "Needs you" })).toBeNull();
        mocks.list.mockResolvedValue([handoff("updated", "active")]);
        await emit("handoff:updated");
        expect(await screen.findByText("updated")).toBeInTheDocument();
    });
    it("uses Monday midnight in local time", () => {
        expect(startOfTaskWeek(new Date(2026, 10, 1, 16))).toBe(new Date(2026, 9, 26).getTime());
    });
});

describe("Up next", () => {
    it("reports a failed read and still shows the section, since Find more lives there", async () => {
        mocks.offers.mockRejectedValue(new Error("offline"));
        mount();
        expect(await screen.findByRole("alert")).toHaveTextContent("Could not update suggestions");
        expect(screen.getByRole("region", { name: "Up next" })).toBeInTheDocument();
        expect(screen.queryByText("0")).toBeNull();
    });

    it("starts the offer after five seconds and lets Undo cancel it", async () => {
        mocks.offers.mockResolvedValue([offer("first"), offer("second")]);
        const props = mount();
        await screen.findByText("first");
        vi.useFakeTimers();
        try {
            fireEvent.click(within(screen.getByText("first").closest("[data-task-offer]") as HTMLElement).getByRole("button", { name: "Do it" }));
            expect(screen.getByText("Started")).toBeInTheDocument();
            expect(mocks.startOffer).not.toHaveBeenCalled();
            fireEvent.click(screen.getByRole("button", { name: "Undo" }));
            await act(async () => vi.advanceTimersByTimeAsync(5000));
            expect(mocks.startOffer).not.toHaveBeenCalled();
            fireEvent.click(within(screen.getByText("first").closest("[data-task-offer]") as HTMLElement).getByRole("button", { name: "Do it" }));
            await act(async () => vi.advanceTimersByTimeAsync(5000));
            expect(mocks.startOffer).toHaveBeenCalledWith("first", "1");
            expect(props.onOpenChat).not.toHaveBeenCalled();
        } finally { vi.useRealTimers(); }
    });

    it("Not now hides the card at once, offers Undo, and sends only after the Undo window", async () => {
        vi.useFakeTimers({ shouldAdvanceTime: true });
        try {
            mocks.offers.mockResolvedValue([offer("first")]);
            mount();
            await screen.findByText("first");
            fireEvent.click(screen.getByRole("button", { name: "Not now" }));
            expect(screen.queryByText("first")).toBeNull();
            expect(mocks.decide).not.toHaveBeenCalled();
            expect(screen.getByText("Not now · first ·")).toBeInTheDocument();
            expect(mocks.toast).not.toHaveBeenCalled();
            await act(async () => { await vi.advanceTimersByTimeAsync(5000); });
            expect(mocks.decide).toHaveBeenCalledWith("first", "not_now");
            expect(screen.getByRole("region", { name: "Up next" })).toBeInTheDocument();
            expect(screen.getByText("Nothing up next.")).toBeInTheDocument();
        } finally {
            vi.useRealTimers();
        }
    });

    it("Undo brings the card back and sends nothing", async () => {
        vi.useFakeTimers({ shouldAdvanceTime: true });
        try {
            mocks.offers.mockResolvedValue([offer("first")]);
            mount();
            await screen.findByText("first");
            fireEvent.click(screen.getByRole("button", { name: "Not now" }));
            fireEvent.click(screen.getByRole("button", { name: "Undo" }));
            expect(screen.getByText("first")).toBeInTheDocument();
            await act(async () => { await vi.advanceTimersByTimeAsync(6000); });
            expect(mocks.decide).not.toHaveBeenCalled();
        } finally {
            vi.useRealTimers();
        }
    });

    it("dismisses from More, and Find more runs the phone's search with its lines", async () => {
        mocks.offers.mockResolvedValue([offer("first"), offer("second")]);
        mount();
        await screen.findByText("first");
        const card = screen.getByText("first").closest("[data-task-offer]") as HTMLElement;
        fireEvent.pointerDown(within(card).getByRole("button", { name: "More options" }), { button: 0, ctrlKey: false });
        fireEvent.click(await screen.findByRole("menuitem", { name: "Not for me" }));
        expect(mocks.decide).toHaveBeenCalledWith("first", "dismiss");
        await settleDeck();
        mocks.findMore.mockResolvedValueOnce({ offers: [offer("second")] });
        fireEvent.click(screen.getByRole("button", { name: "Find more" }));
        expect(await screen.findByText("Nothing new right now.")).toBeInTheDocument();
        mocks.findMore.mockResolvedValueOnce({ offers: [offer("second"), offer("third")], nextAllowedAt: new Date(Date.now() + 3600_000).toISOString() });
        fireEvent.click(screen.getByRole("button", { name: "Find more" }));
        expect(await screen.findByText("third")).toBeInTheDocument();
        expect(screen.getByText(/^More arrive after /)).toBeInTheDocument();
        mocks.findMore.mockRejectedValueOnce(new Error("the search is still running"));
        fireEvent.click(screen.getByRole("button", { name: "Find more" }));
        expect(await screen.findByText("Couldn't find more tasks. Try again.")).toBeInTheDocument();
    });

    it("keeps cards on decision errors", async () => {
        mocks.offers.mockResolvedValue([offer("first")]);
        mount();
        await screen.findByText("first");
        mocks.decide.mockRejectedValue(new Error("offline"));
        const card = screen.getByText("first").closest("[data-task-offer]") as HTMLElement;
        fireEvent.pointerDown(within(card).getByRole("button", { name: "More options" }), { button: 0, ctrlKey: false });
        fireEvent.click(await screen.findByRole("menuitem", { name: "Not for me" }));
        expect(await screen.findByRole("alert")).toHaveTextContent("Could not update suggestions");
        expect(screen.getByText("first")).toBeInTheDocument();
    });

    it("localizes rationale and falls back to why", async () => {
        await i18n.changeLanguage("zh-CN");
        mocks.offers.mockResolvedValue([offer("first")]);
        mount();
        expect(await screen.findByText("一个有用的任务")).toBeInTheDocument();
    });

    it("does not show offers without evidence", async () => {
        mocks.offers.mockResolvedValue([{ ...offer("ungrounded"), evidence: undefined }, offer("grounded")]);
        mount();
        await screen.findByText("grounded");
        expect(screen.queryByText("ungrounded")).toBeNull();
        expect(screen.getByText(/From Mail/)).toBeInTheDocument();
    });

    it("offers a playbook Isoform drafted, and never lists it as a draft", async () => {
        mocks.sops.mockResolvedValue([{ id: "pb", title: "Friday invoice run", status: "draft", sites: [] }]);
        mocks.playbooks.mockResolvedValue({
            plans: [{ id: "pb", proposal: { host: "", siteName: "Stripe", time: "4:00 PM", days: 3, weekdays: false, titles: [], proposedAt: 1, answer: "", answeredAt: 0 }, schedule: null, channels: [], sites: [], lastRun: null, nextRunAt: 0, suggestedTime: "16:00", held: "" }],
            channels: {}, defaultChannels: [],
        });
        mocks.acceptPlaybook.mockResolvedValue({ schedule: { time: "16:00" } });
        mount();
        const card = await screen.findByText("Make your Friday invoice run a playbook");
        expect(within(card.closest("[data-playbook-offer]") as HTMLElement).getByText(/From Stripe/)).toBeInTheDocument();
        expect(screen.queryByText(/Draft/)).toBeNull();
        fireEvent.click(within(card.closest("[data-playbook-offer]") as HTMLElement).getByRole("button", { name: "Do it" }));
        expect(screen.getByTestId("playbook-added")).toHaveTextContent("Added to Scheduled ·Undo·Edit");
        expect(mocks.acceptPlaybook).not.toHaveBeenCalled();
        await waitFor(() => expect(mocks.acceptPlaybook).toHaveBeenCalledWith("pb"), { timeout: 7000 });
    }, 10000);

    it("explains a playbook offer with its own description, after the task offers", async () => {
        mocks.offers.mockResolvedValue([offer("weekly")]);
        mocks.sops.mockResolvedValue([{ id: "pb", title: "Friday invoice run", description: "Same five steps, three Fridays in a row.", status: "draft", sites: [] }]);
        mocks.playbooks.mockResolvedValue({
            plans: [{ id: "pb", proposal: { host: "", siteName: "Stripe", time: "4:00 PM", days: 3, weekdays: false, titles: [], proposedAt: 1, answer: "", answeredAt: 0 }, schedule: null, channels: [], sites: [], lastRun: null, nextRunAt: 0, suggestedTime: "16:00", held: "" }],
            channels: {}, defaultChannels: [],
        });
        mount();
        expect(await screen.findByText("Same five steps, three Fridays in a row.")).toBeInTheDocument();
        await screen.findByText("weekly");
        const cards = [...document.querySelectorAll("[data-task-offer], [data-playbook-offer]")].map((card) => card.getAttribute("data-task-offer") ?? card.getAttribute("data-playbook-offer"));
        expect(cards).toEqual(["weekly", "pb"]);
    });

    it("keeps a playbook offer when its source time is missing", async () => {
        mocks.sops.mockResolvedValue([{ id: "pb", title: "Friday invoice run", status: "draft", sites: [] }]);
        mocks.playbooks.mockResolvedValue({ plans: [{ id: "pb", proposal: { host: "", siteName: "Stripe", time: "4:00 PM", days: 3, weekdays: false, titles: [], proposedAt: 0, answer: "", answeredAt: 0 }, schedule: null, channels: [], sites: [], lastRun: null, nextRunAt: 0, suggestedTime: "16:00", held: "" }], channels: {}, defaultChannels: [] });
        mount();
        const card = await screen.findByText("Make your Friday invoice run a playbook");
        expect(within(card.closest("[data-playbook-offer]") as HTMLElement).queryByText(/From Stripe/)).toBeNull();
    });
});

describe("the first day", () => {
    it("says the briefs are scheduled, explains when offers start, and hides Find more", async () => {
        mocks.firstWeekDay.mockResolvedValue(0);
        mount({ upcoming: { items: [brief("morning", "08:00"), brief("evening", "18:00")], loading: false } });
        expect(await screen.findByText("Isoform offers work here once it has seen a day of yours, usually by tomorrow.")).toBeInTheDocument();
        expect(screen.getByTestId("tasks-line")).toHaveTextContent("Nothing needs you. Your briefs are scheduled.");
        expect(screen.queryByRole("button", { name: "Find more" })).toBeNull();
        const scheduled = screen.getByRole("region", { name: "Scheduled" });
        expect(within(scheduled).getByText("Morning brief")).toBeInTheDocument();
        expect(within(scheduled).getByText("Evening brief")).toBeInTheDocument();
        expect(screen.getByPlaceholderText("Give Isoform a task…")).toBeInTheDocument();
    });

    it("is never blank: a fresh install still shows the box, Up next and Scheduled", async () => {
        mocks.firstWeekDay.mockResolvedValue(0);
        mount();
        await waitFor(() => expect(screen.queryByTestId("task-deck-placeholder")).toBeNull());
        expect(screen.getByPlaceholderText("Give Isoform a task…")).toBeInTheDocument();
        expect(screen.getByRole("region", { name: "Up next" })).toBeInTheDocument();
        expect(screen.getByRole("region", { name: "Scheduled" })).toBeInTheDocument();
    });
});

function brief(slot: string, time: string, extra: Partial<UpcomingItem> = {}): UpcomingItem {
    return {
        id: `brief:${slot}`, kind: "brief", jobId: "", slot, name: "", summary: "", enabled: true, scheduleKind: "cron", scheduleConfig: null,
        time, nextRunAt: Date.now() + 3600_000, lastRunAt: 0, lastResult: "", lastReason: "", failedAttempts: 0, missedRuns: 0, system: true, ...extra,
    } as UpcomingItem;
}
function schedule(id: string, extra: Partial<UpcomingItem> = {}): UpcomingItem {
    return brief(id, "09:00", { id, kind: "schedule", jobId: id, name: id, summary: "Does " + id, system: false, scheduleKind: "every", scheduleConfig: { every_ms: 86_400_000 }, ...extra });
}

describe("Scheduled", () => {
    it("lists schedules, says Paused, flags two failed runs, hides finished ones and opens the detail", async () => {
        const onSelectSchedule = vi.fn();
        mount({
            onSelectSchedule,
            upcoming: {
                loading: false,
                items: [
                    schedule("weekly update"),
                    schedule("paused one", { enabled: false, nextRunAt: 0 }),
                    schedule("failing one", { failedAttempts: 2, lastResult: "failed" }),
                    schedule("finished one", { enabled: false, scheduleKind: "at", lastRunAt: Date.now() - 1000, nextRunAt: 0 }),
                ],
            },
        });
        const scheduled = await screen.findByRole("region", { name: "Scheduled" });
        expect(within(scheduled).getByText("weekly update")).toBeInTheDocument();
        expect(within(scheduled).getByText("Paused")).toBeInTheDocument();
        expect(within(scheduled).queryByText("finished one")).toBeNull();
        const failing = scheduled.querySelector('[data-scheduled-row="failing one"]') as HTMLElement;
        expect(within(failing).getByText("Didn't run · See why")).toBeInTheDocument();
        expect(scheduled.querySelectorAll("[data-schedule-failing]")).toHaveLength(1);
        fireEvent.click(within(failing).getByText("failing one"));
        expect(onSelectSchedule).toHaveBeenCalledWith("failing one");
    });

    // Upcoming's search, sort and hover delete carry over to Scheduled
    // (hub review of #350, regression guard).
    it("searches and sorts a long list, and a hover delete asks first", async () => {
        const onDeleteSchedule = vi.fn();
        const onSelectSchedule = vi.fn();
        mount({
            onDeleteSchedule, onSelectSchedule,
            upcoming: { loading: false, items: [brief("morning", "08:00"), schedule("zeta report", { nextRunAt: Date.now() + 1000 }), schedule("alpha sync", { nextRunAt: Date.now() + 5000 }), schedule("invoice run")] },
        });
        const scheduled = await screen.findByRole("region", { name: "Scheduled" });
        const names = () => Array.from(scheduled.querySelectorAll("[data-scheduled-row]")).map((row) => row.getAttribute("data-scheduled-row"));
        expect(names()).toEqual(["brief:morning", "zeta report", "alpha sync", "invoice run"]);

        await userEvent.type(within(scheduled).getByRole("textbox", { name: "Search Scheduled..." }), "invoice");
        expect(names()).toEqual(["invoice run"]);
        await userEvent.clear(within(scheduled).getByRole("textbox", { name: "Search Scheduled..." }));
        await userEvent.type(within(scheduled).getByRole("textbox", { name: "Search Scheduled..." }), "nothing like it");
        expect(within(scheduled).getByText("Nothing scheduled matches")).toBeInTheDocument();
        await userEvent.clear(within(scheduled).getByRole("textbox", { name: "Search Scheduled..." }));

        await userEvent.click(within(scheduled).getByRole("button", { name: "Sort by: Next run" }));
        await userEvent.click(await screen.findByRole("menuitem", { name: "Name" }));
        expect(names()).toEqual(["alpha sync", "invoice run", "brief:morning", "zeta report"]);

        // The brief is the system's: no delete on it.
        expect(scheduled.querySelector('[data-scheduled-delete="brief:morning"]')).toBeNull();
        await userEvent.click(within(scheduled).getByRole("button", { name: "Delete alpha sync" }));
        expect(onSelectSchedule).not.toHaveBeenCalled();
        expect(onDeleteSchedule).not.toHaveBeenCalled();
        const dialog = await screen.findByRole("dialog");
        expect(within(dialog).getByText("alpha sync")).toBeInTheDocument();
        await userEvent.click(within(dialog).getByRole("button", { name: "Delete" }));
        expect(onDeleteSchedule).toHaveBeenCalledWith("alpha sync");
    });

    it("keeps the first day's briefs free of search and sort", async () => {
        mount({ upcoming: { items: [brief("morning", "08:00"), brief("evening", "18:00")], loading: false } });
        const scheduled = await screen.findByRole("region", { name: "Scheduled" });
        expect(within(scheduled).queryByRole("textbox")).toBeNull();
        expect(within(scheduled).queryByRole("button", { name: /Sort by/ })).toBeNull();
    });

    it("opens the brief's own detail beside the list from a brief row, never iPhone settings (K3)", async () => {
        const onChangeBriefTimes = vi.fn();
        const onSelectSchedule = vi.fn();
        mount({ onChangeBriefTimes, onSelectSchedule, upcoming: { items: [brief("morning", "08:00"), brief("evening", "18:00")], loading: false } });
        fireEvent.click(await screen.findByText("Morning brief"));
        expect(screen.getByTestId("brief-detail")).toHaveTextContent("morning");
        expect(document.querySelector('[data-scheduled-row="brief:morning"]')).toHaveAttribute("data-selected", "true");
        // The list stays beside it.
        expect(screen.getByRole("region", { name: "Scheduled" })).toBeInTheDocument();
        expect(onChangeBriefTimes).not.toHaveBeenCalled();
        expect(onSelectSchedule).not.toHaveBeenCalled();
        fireEvent.click(screen.getByText("Evening brief"));
        expect(screen.getByTestId("brief-detail")).toHaveTextContent("evening");
        fireEvent.click(screen.getByText("close brief"));
        expect(screen.queryByTestId("brief-detail")).toBeNull();
    });

    it("opens a brief's detail when asked from elsewhere (Today's missed-brief Why)", async () => {
        const props = { onStartHandoff: vi.fn(async () => true), upcoming: { items: [brief("morning", "08:00")], loading: false } };
        const { rerender } = render(<TasksPage {...props} />);
        await screen.findByText("Morning brief");
        expect(screen.queryByTestId("brief-detail")).toBeNull();
        rerender(<TasksPage {...props} focus={{ section: "scheduled", brief: "morning", nonce: 1 }} />);
        expect(await screen.findByTestId("brief-detail")).toHaveTextContent("morning");
    });
});

describe("Done this week", () => {
    it("counts chat work that made a file, shows file chips that open, and Show all opens All done", async () => {
        mocks.deliverables.mockResolvedValue([
            { id: "d1", source: "chat", sourceId: "s1", title: "Pricing summary", summary: "Pricing summary is ready.", status: "completed", files: [{ path: "/tmp/pricing.md", name: "pricing.md" }], createdAt: Date.now(), updatedAt: Date.now() },
            { id: "d2", source: "chat", sourceId: "s2", title: "No file chat", summary: "", status: "completed", files: [], createdAt: Date.now(), updatedAt: Date.now() },
        ]);
        mount();
        const done = await screen.findByRole("region", { name: "Done this week" });
        expect(within(done).getByText("Pricing summary")).toBeInTheDocument();
        expect(within(done).queryByText("No file chat")).toBeNull();
        fireEvent.click(within(done).getByText("pricing.md"));
        expect(mocks.openFile).toHaveBeenCalledWith("chat", "s1", "/tmp/pricing.md");
        fireEvent.click(within(done).getByRole("button", { name: "Show all" }));
        expect(screen.getByTestId("all-done")).toBeInTheDocument();
    });
});

describe("Running", () => {
    it("shows list drafts in Running and list reminders in Scheduled", async () => {
        mocks.list.mockResolvedValue([handoff("draft-list", "active"), handoff("reminder-list", "active")]);
        mocks.desktopTasks.mockResolvedValue({
            tasks: [{ id: "draft-list", title: "Reply to Lena", group: "running", status: "drafting", source: "list", updatedAt: new Date().toISOString() }],
            scheduled: [{ id: "reminder-list", kind: "reminder", title: "Book dentist", at: "" }],
        });
        mount();
        const running = await screen.findByRole("region", { name: "Running" });
        expect(within(running).getByText("Reply to Lena")).toBeInTheDocument();
        expect(within(running).getByText("Isoform is drafting it on your computer")).toBeInTheDocument();
        const scheduled = screen.getByRole("region", { name: "Scheduled" });
        expect(within(scheduled).getByText("Book dentist")).toBeInTheDocument();
        expect(screen.queryByRole("region", { name: "Needs you" })).not.toBeInTheDocument();
        fireEvent.click(within(running).getByRole("button", { name: /Reply to Lena/ }));
        expect(await screen.findByTestId("task-thread")).toHaveTextContent("draft-list");
    });

    it("stops a running task from its row", async () => {
        mocks.list.mockResolvedValue([handoff("running", "in_progress")]);
        mount();
        const running = await screen.findByRole("region", { name: "Running" });
        fireEvent.click(within(running).getByRole("button", { name: "Stop" }));
        await waitFor(() => expect(mocks.abort).toHaveBeenCalledWith("running"));
    });
});

describe("give Isoform a task", () => {
    it("checks a multiline list and adds only checked tasks", async () => {
        mount();
        const box = screen.getByPlaceholderText("Give Isoform a task…");
        expect(screen.getByRole("button", { name: "Make tasks" })).toBeDisabled();
        fireEvent.change(box, { target: { value: "Reply to Lena\nBook dentist\nFix tests" } });
        fireEvent.submit(box.closest("form")!);
        expect(await screen.findByText("Check these. Nothing starts until you add them.")).toBeInTheDocument();
        expect(screen.getByText("Isoform drafts it. You send it.")).toBeInTheDocument();
        expect(screen.getByText("A reminder for you")).toBeInTheDocument();
        fireEvent.click(screen.getByRole("checkbox", { name: /Fix tests/ }));
        fireEvent.click(screen.getByRole("button", { name: "Add 2 to Tasks" }));
        await waitFor(() => expect(mocks.addDump).toHaveBeenCalledWith(expect.any(String), [
            expect.objectContaining({ id: "draft" }), expect.objectContaining({ id: "reminder" }),
        ]));
        expect(await screen.findByPlaceholderText("Give Isoform a task…")).toHaveValue("");
    });

    it("keeps the words and shows an inline error without a toast when Make tasks fails", async () => {
        mocks.parseDump.mockRejectedValueOnce(new Error("offline"));
        mount();
        const box = screen.getByPlaceholderText("Give Isoform a task…");
        fireEvent.change(box, { target: { value: "Do the thing" } });
        fireEvent.submit(box.closest("form")!);
        expect(await screen.findByRole("alert")).toHaveTextContent("Couldn't make tasks from your list. Try again");
        expect(box).toHaveValue("Do the thing");
        expect(mocks.toast.error).not.toHaveBeenCalled();
        fireEvent.submit(box.closest("form")!);
        fireEvent.click(await screen.findByRole("button", { name: "Edit the list" }));
        expect(screen.getByPlaceholderText("Give Isoform a task…")).toHaveValue("Do the thing");
    });

    it("keeps the check step and retries the same batch without a toast when Add fails", async () => {
        mocks.parseDump.mockResolvedValueOnce({ items: [{ id: "one", title: "Fix tests", who: "reminder", noRunner: true }] });
        mocks.addDump.mockRejectedValueOnce(new Error("offline"));
        mount();
        const box = screen.getByPlaceholderText("Give Isoform a task…");
        fireEvent.change(box, { target: { value: "Fix tests" } });
        fireEvent.submit(box.closest("form")!);
        expect(await screen.findByText("No AI is signed in on your computer, so this is a reminder.")).toBeInTheDocument();
        fireEvent.click(screen.getByRole("button", { name: "Add 1 to Tasks" }));
        expect(await screen.findByRole("alert")).toHaveTextContent("Couldn't add them. Try again");
        expect(screen.getByText("Check these. Nothing starts until you add them.")).toBeInTheDocument();
        expect(mocks.toast.error).not.toHaveBeenCalled();
        fireEvent.click(screen.getByRole("button", { name: "Add 1 to Tasks" }));
        await waitFor(() => expect(mocks.addDump).toHaveBeenCalledTimes(2));
        expect(mocks.addDump.mock.calls[0]).toEqual(mocks.addDump.mock.calls[1]);
    });

    it("shows reply sources and project lines only when the check data supplies them", async () => {
        mocks.parseDump.mockResolvedValueOnce({ items: [
            { id: "reply", title: "Reply to Lena", who: "draft", person: "Lena Chen" },
            { id: "reply-reminder", title: "Reply to Lena", who: "reminder", person: "Lena Chen" },
            { id: "claude", title: "Fix desktop tests", who: "claude_code", project: "isoform" },
            { id: "codex", title: "Fix website tests", who: "codex", project: "website" },
            { id: "unknown", title: "Fix unnamed project", who: "claude_code" },
            { id: "reminder", title: "Book dentist", who: "reminder" },
        ] });
        mount();
        const box = screen.getByPlaceholderText("Give Isoform a task…");
        fireEvent.change(box, { target: { value: "Reply to Lena\nFix tests" } });
        fireEvent.submit(box.closest("form")!);
        expect(await screen.findByText("From Lena Chen")).toBeInTheDocument();
        expect(screen.getByText("No email to reply to yet")).toBeInTheDocument();
        expect(screen.getByText("In isoform, from your Claude Code history")).toBeInTheDocument();
        expect(screen.getByText("In website, from your Codex history")).toBeInTheDocument();
        const unknown = screen.getByRole("checkbox", { name: /Fix unnamed project/ });
        expect(within(unknown.closest("label")!).queryByText(/from your .* history/)).toBeNull();
        const reminder = screen.getByRole("checkbox", { name: /Book dentist/ });
        expect(within(reminder.closest("label")!).queryByText("No email to reply to yet")).toBeNull();
    });

    it("titles a task from its first line, within 200 bytes", () => {
        expect(taskTitleFrom("  First line \nsecond")).toBe("First line");
        const long = taskTitleFrom("字".repeat(120));
        expect(new TextEncoder().encode(long).length).toBeLessThanOrEqual(200);
        expect(long.endsWith("…")).toBe(true);
    });
});

// The sidebar's Tasks count reads the same list as Needs you.
describe("the Tasks count", () => {
    function Count() {
        return <output data-testid="tasks-count">{useTasksNeedsYouCount()}</output>;
    }

    it("keeps Today, Tasks and the badge in the same order as items change", async () => {
        const make = (id: string) => ({ id, kind: "work_blocked", sentence: id, createdAt: 0, targetKind: "chat_item", targetId: `row-${id}`, chatId: "main" });
        mocks.needs.mockResolvedValue({ items: [make("first"), make("second"), make("third"), make("fourth")], total: 4 });
        function Preview() {
            const { items } = useTodayNeeds();
            return <TodayNeedsSection items={items} onOpen={() => {}} onOpenTasks={() => {}} />;
        }
        render(<><Count /><Preview /></>);
        mount();
        const today = await screen.findByTestId("today-needs-you");
        const tasks = await screen.findByTestId("tasks-count");
        await waitFor(() => expect(tasks).toHaveTextContent("4"));
        const taskRegion = document.querySelector<HTMLElement>('[data-tasks-section="needs-you"]')!;
        expect([...taskRegion.querySelectorAll("[data-needs-you-card]")].map((row) => row.getAttribute("data-needs-you-card"))).toEqual(["first", "second", "third", "fourth"]);
        expect(within(today).getByText("1 more in Tasks")).toBeInTheDocument();
        expect(within(today).queryByText("fourth")).toBeNull();
        mocks.needs.mockResolvedValue({ items: [make("second"), make("third")], total: 2 });
        await emit("main-chat:waiting-changed");
        await waitFor(() => expect(tasks).toHaveTextContent("2"));
        expect([...taskRegion.querySelectorAll("[data-needs-you-card]")].map((row) => row.getAttribute("data-needs-you-card"))).toEqual(["second", "third"]);
        expect(within(today).queryByText("first")).toBeNull();
        expect(within(today).queryByText(/more in Tasks/)).toBeNull();
    });

    it("counts exactly what Needs you lists, and drops when one is set aside", async () => {
        mocks.list.mockResolvedValue([
            handoff("start", "active"), handoff("review", "needs_review"), handoff("stuck", "blocked"),
            handoff("busy", "in_progress"), handoff("done", "completed"), handoff("gone", "aborted"),
        ]);
        render(<Count />);
        mount();
        const needs = await screen.findByRole("region", { name: "Needs you" });
        await within(needs).findByText("stuck");
        const listed = needs.querySelectorAll("[data-needs-you-card]").length;
        expect(listed).toBe(3);
        await waitFor(() => expect(screen.getByTestId("tasks-count")).toHaveTextContent(String(listed)));
        const page = await mocks.needs.getMockImplementation()!();
        mocks.needs.mockResolvedValue({ items: page.items.filter((item: { id: string }) => item.id !== "start"), putOff: page.items.filter((item: { id: string }) => item.id === "start"), total: 2 });
        fireEvent.click(within(needs).getByRole("button", { name: "Not now" }));
        expect(mocks.putOff).toHaveBeenCalledWith("start", "put_off");
        await waitFor(() => expect(screen.getByTestId("tasks-count")).toHaveTextContent("2"));
        expect(needs.querySelectorAll("[data-needs-you-card]")).toHaveLength(2);
    });
});

describe("Not now on a playbook offer", () => {
    const proposal = { host: "", siteName: "Stripe", time: "4:00 PM", days: 3, weekdays: false, titles: [], proposedAt: 1, answer: "", answeredAt: 0 };
    function offerPlaybook(extra = {}) {
        mocks.sops.mockResolvedValue([{ id: "pb", title: "Friday invoice run", status: "draft", sites: [] }]);
        mocks.playbooks.mockResolvedValue({
            plans: [{ id: "pb", proposal: { ...proposal, ...extra }, schedule: null, channels: [], sites: [], lastRun: null, nextRunAt: 0, suggestedTime: "16:00", held: "" }],
            channels: {}, defaultChannels: [],
        });
    }

    it("hides it with Undo, then snoozes it for two weeks and never declines it", async () => {
        offerPlaybook();
        mount();
        const title = await screen.findByText("Make your Friday invoice run a playbook");
        vi.useFakeTimers();
        try {
            fireEvent.click(within(title.closest("[data-playbook-offer]") as HTMLElement).getByRole("button", { name: "Not now" }));
            expect(screen.queryByText("Make your Friday invoice run a playbook")).toBeNull();
            expect(screen.getByText("Not now · Friday invoice run ·")).toBeInTheDocument();
            expect(mocks.toast).not.toHaveBeenCalled();
            fireEvent.click(screen.getByRole("button", { name: "Undo" }));
            expect(screen.getByText("Make your Friday invoice run a playbook")).toBeInTheDocument();
            await act(async () => { vi.advanceTimersByTime(6000); });
            expect(mocks.snoozePlaybook).not.toHaveBeenCalled();
            fireEvent.click(within(screen.getByText("Make your Friday invoice run a playbook").closest("[data-playbook-offer]") as HTMLElement).getByRole("button", { name: "Not now" }));
            await act(async () => { vi.advanceTimersByTime(5000); });
            expect(mocks.snoozePlaybook).toHaveBeenCalledWith("pb");
            expect(mocks.declinePlaybook).not.toHaveBeenCalled();
        } finally {
            vi.useRealTimers();
        }
    });

    it("W22: a Not now that didn't save stays after leaving Tasks, with Try again", async () => {
        offerPlaybook();
        mocks.snoozePlaybook.mockRejectedValueOnce(new Error("disk full")).mockResolvedValue(undefined);
        mount();
        const title = await screen.findByText("Make your Friday invoice run a playbook");
        vi.useFakeTimers();
        try {
            fireEvent.click(within(title.closest("[data-playbook-offer]") as HTMLElement).getByRole("button", { name: "Not now" }));
            await act(async () => { vi.advanceTimersByTime(5000); });
        } finally {
            vi.useRealTimers();
        }
        expect(await screen.findByText("Your Not now didn't save, so it's back.")).toBeInTheDocument();
        cleanup();
        mount();
        expect(await screen.findByText("Your Not now didn't save, so it's back.")).toBeInTheDocument();
        expect(document.body.textContent).not.toContain("disk full");
        vi.useFakeTimers();
        try {
            fireEvent.click(screen.getByRole("button", { name: "Try again" }));
            await act(async () => { vi.advanceTimersByTime(5000); });
        } finally {
            vi.useRealTimers();
        }
        expect(mocks.snoozePlaybook).toHaveBeenCalledTimes(2);
        await waitFor(() => expect(screen.queryByText("Your Not now didn't save, so it's back.")).toBeNull());
    });

    it("W22: a Do it that didn't save says so in place and outlives Tasks, with Try again", async () => {
        offerPlaybook();
        mocks.acceptPlaybook.mockRejectedValueOnce(new Error("offline")).mockResolvedValue({ schedule: { time: "16:00" } });
        mount();
        const title = await screen.findByText("Make your Friday invoice run a playbook");
        vi.useFakeTimers();
        try {
            fireEvent.click(within(title.closest("[data-playbook-offer]") as HTMLElement).getByRole("button", { name: "Do it" }));
            await act(async () => { vi.advanceTimersByTime(5000); });
        } finally {
            vi.useRealTimers();
        }
        expect(await screen.findByText("Didn’t add it to Scheduled.")).toBeInTheDocument();
        expect(mocks.toast).not.toHaveBeenCalled();
        cleanup();
        mount();
        expect(await screen.findByText("Didn’t add it to Scheduled.")).toBeInTheDocument();
        vi.useFakeTimers();
        try {
            fireEvent.click(screen.getByRole("button", { name: "Try again" }));
            await act(async () => { vi.advanceTimersByTime(5000); });
        } finally {
            vi.useRealTimers();
        }
        expect(mocks.acceptPlaybook).toHaveBeenCalledTimes(2);
    });

    it("W22: reads in Chinese", async () => {
        await i18n.changeLanguage("zh-CN");
        offerPlaybook();
        mocks.acceptPlaybook.mockRejectedValueOnce(new Error("offline"));
        mount();
        const card = (await screen.findAllByRole("article")).find((el) => el.hasAttribute("data-playbook-offer"))!;
        vi.useFakeTimers();
        try {
            fireEvent.click(within(card).getAllByRole("button")[0]);
            await act(async () => { vi.advanceTimersByTime(5000); });
        } finally {
            vi.useRealTimers();
        }
        expect(await screen.findByText("没有添加到定时任务。")).toBeInTheDocument();
    });

    it("keeps a snoozed offer out of Up next until the snooze ends", async () => {
        offerPlaybook({ snoozedUntil: Date.now() + 86_400_000 });
        mocks.offers.mockResolvedValue([offer("weekly")]);
        mount();
        await screen.findByText("weekly");
        expect(screen.queryByText("Make your Friday invoice run a playbook")).toBeNull();
    });
});

describe("Up next keys", () => {
    it("Right arrow starts the first offer and Left arrow puts it away, unless typing", async () => {
        mocks.offers.mockResolvedValue([offer("first"), offer("second")]);
        mount();
        await screen.findByText("first");
        vi.useFakeTimers();
        try {
            fireEvent.keyDown(screen.getByPlaceholderText("Give Isoform a task…"), { key: "ArrowRight" });
            expect(mocks.startOffer).not.toHaveBeenCalled();
            fireEvent.keyDown(document.body, { key: "ArrowLeft" });
            fireEvent.keyDown(document.body, { key: "ArrowRight" });
            await act(async () => vi.advanceTimersByTimeAsync(5000));
            expect(mocks.startOffer).toHaveBeenCalledWith("second", "1");
        } finally { vi.useRealTimers(); }
    });
});

describe("A task never disappears (K1)", () => {
    const stopped = [
        handoff("no-ai", "aborted", { title: "Draft the memo", stoppedBy: "system", stopCause: "no_assistant", group: "needs_you" }),
        handoff("signed-out", "aborted", { title: "Sort the files", stoppedBy: "system", stopCause: "signed_out", stopAssistant: "claude_code", group: "needs_you" }),
        handoff("restarted", "blocked", { title: "Plan the trip", blockedReason: "interrupted", stopCause: "restarted", group: "needs_you" }),
        handoff("timeout", "blocked", { title: "Clean the list", blockedReason: "timeout", stopCause: "timeout", group: "needs_you" }),
        handoff("error", "aborted", { title: "Write the notes", stoppedBy: "system", stopCause: "assistant_error", stopAssistant: "codex", group: "needs_you" }),
        handoff("loop", "aborted", { title: "Prepare the report", stoppedBy: "system", stopCause: "loop_guard", group: "needs_you" }),
        handoff("review", "needs_review", { title: "Check the report", verification: { met: 2, total: 3 }, group: "needs_you" }),
    ];
    const mine = handoff("mine", "aborted", { title: "Stopped by me", stoppedBy: "person", group: "done", stoppedAt: new Date(2026, 9, 31, 15, 4).getTime() });
    const needItems = stopped.map((h) => ({ id: h.id, kind: h.status === "needs_review" ? "review" : "work_blocked", sentence: h.title, createdAt: 0, targetKind: "handoff", targetId: h.id, stopCause: h.stopCause }));
    beforeEach(() => {
        mocks.list.mockResolvedValue([...stopped, mine]);
        mocks.needs.mockResolvedValue({ items: needItems, total: needItems.length });
        mocks.desktopTasks.mockResolvedValue({ tasks: [] });
    });
    const row = async (id: string) => {
        await waitFor(() => expect(document.querySelector(`[data-needs-you-card="${id}"]`)).not.toBeNull());
        return within(document.querySelector(`[data-needs-you-card="${id}"]`) as HTMLElement);
    };

    it("shows each stopped task in Needs you with its line and button", async () => {
        mount();
        const cases: [string, string, string][] = [
            ["no-ai", "Couldn't finish: no AI is signed in.", "Set up"],
            ["signed-out", "Couldn't finish: sign in to Claude Code again.", "Sign in"],
            ["restarted", "Stopped when Isoform restarted.", "Start again"],
            ["timeout", "Stopped: no activity for two hours.", "Start again"],
            ["error", "Couldn't finish: Codex stopped with an error.", "Start again"],
            ["loop", "Stopped after too many attempts.", "Review"],
            ["review", "Finished · 2 of 3 checks met", "Review"],
        ];
        for (const [id, line, button] of cases) {
            const card = await row(id);
            expect(card.getByText(line)).toBeInTheDocument();
            expect(card.getByRole("button", { name: button })).toBeInTheDocument();
        }
        expect(screen.getByRole("region", { name: "Needs you" }).querySelector("header")).toHaveTextContent(/^Needs you$/);
    });

    it("says Review with one more sentence when a step needs checking or a reply needs a choice (v1.0.2, v1.0.7)", async () => {
        const checking = [
            handoff("step", "blocked", { title: "Post the FAQ", blockedReason: "interrupted", stopCause: "restarted", group: "needs_you", stepsToCheck: 1 }),
            handoff("steps", "aborted", { title: "Push the site", stoppedBy: "system", stopCause: "timeout", group: "needs_you", stepsToCheck: 2 }),
            handoff("reply", "aborted", { title: "Send the deck", stoppedBy: "system", stopCause: "assistant_error", stopAssistant: "codex", group: "needs_you", repliesToChoose: 1 }),
        ];
        mocks.list.mockResolvedValue(checking);
        const items = checking.map((h) => ({ id: h.id, kind: "work_blocked", sentence: h.title, createdAt: 0, targetKind: "handoff", targetId: h.id, stopCause: h.stopCause }));
        mocks.needs.mockResolvedValue({ items, total: items.length });
        mount();
        for (const [id, line] of [
            ["step", "Stopped when Isoform restarted. One step needs checking."],
            ["steps", "Stopped: no activity for two hours. 2 steps need checking."],
            ["reply", "Couldn't finish: Codex stopped with an error. One reply needs a choice."],
        ]) {
            const card = await row(id);
            expect(card.getByText(line)).toBeInTheDocument();
            expect(card.getByRole("button", { name: "Review" })).toBeInTheDocument();
            expect(card.queryByRole("button", { name: "Start again" })).toBeNull();
        }
        cleanup();
        await i18n.changeLanguage("zh-CN");
        mount();
        expect((await row("step")).getByText("Isoform 重启时中断了。有一步需要你确认。")).toBeInTheDocument();
        expect((await row("reply")).getByText("没能完成：Codex 出错停止了。有一条回复需要你决定。")).toBeInTheDocument();
        expect((await row("reply")).getByRole("button", { name: "查看" })).toBeInTheDocument();
    });

    it("words the rows in Chinese from the copy table", async () => {
        await i18n.changeLanguage("zh-CN");
        mount();
        const cases: [string, string, string][] = [
            ["no-ai", "没能完成：还没有登录 AI。", "去设置"],
            ["signed-out", "没能完成：需要重新登录 Claude Code。", "登录"],
            ["restarted", "Isoform 重启时中断了。", "重新开始"],
            ["timeout", "中断了：两小时没有进展。", "重新开始"],
            ["error", "没能完成：Codex 出错停止了。", "重新开始"],
            ["loop", "尝试太多次，中断了。", "查看"],
            ["review", "已完成 · 3 项检查通过了 2 项", "查看"],
        ];
        for (const [id, line, button] of cases) {
            const card = await row(id);
            expect(card.getByText(line)).toBeInTheDocument();
            expect(card.getByRole("button", { name: button })).toBeInTheDocument();
        }
    });

    it("Set up and Sign in open Your AI in Settings", async () => {
        mount();
        fireEvent.click((await row("no-ai")).getByRole("button", { name: "Set up" }));
        fireEvent.click((await row("signed-out")).getByRole("button", { name: "Sign in" }));
        expect(mocks.emit).toHaveBeenNthCalledWith(1, "settings:open", "ai-provider");
        expect(mocks.emit).toHaveBeenNthCalledWith(2, "settings:open", "ai-provider");
    });

    it("Start again waits five seconds with Started · Undo, then restarts the same task", async () => {
        mount();
        const card = await row("restarted");
        vi.useFakeTimers({ shouldAdvanceTime: true });
        try {
            fireEvent.click(card.getByRole("button", { name: "Start again" }));
            expect(card.getByText("Started")).toBeInTheDocument();
            fireEvent.click(card.getByRole("button", { name: "Undo" }));
            await act(async () => { await vi.advanceTimersByTimeAsync(5000); });
            expect(mocks.startAgain).not.toHaveBeenCalled();
            mocks.startAgain.mockResolvedValue({ handoffId: "restarted", sessionId: "s" });
            fireEvent.click(card.getByRole("button", { name: "Start again" }));
            await act(async () => { await vi.advanceTimersByTimeAsync(5000); });
            expect(mocks.startAgain).toHaveBeenCalledWith("restarted");
        } finally { vi.useRealTimers(); }
    });

    it("puts a task you stopped in Done this week with a gray check and the time", async () => {
        mount();
        const done = await screen.findByRole("region", { name: "Done this week" });
        expect(within(done).getByText("Stopped by me")).toBeInTheDocument();
        const time = new Date(mine.stoppedAt!).toLocaleTimeString("en", { hour: "numeric", minute: "2-digit" });
        expect(within(done).getByText(`You stopped it at ${time}.`)).toBeInTheDocument();
        expect(done.querySelector('[data-done-check="you"]')?.getAttribute("class")).toContain("text-muted");
        expect(document.querySelector('[data-needs-you-card="mine"]')).toBeNull();
    });

    it("says how many steps were done when you stopped it", async () => {
        setComputerClock("24h");
        const at = new Date(2026, 9, 31, 15, 4).toISOString();
        mocks.desktopTasks.mockResolvedValue({ tasks: [{ id: "mine", title: "Stopped by me", group: "done", status: "aborted", updatedAt: at, stoppedBy: "person", outcome: { kind: "stopped", by: "you", at, params: { n: "2", m: "5" } }, events: [], actions: [], steps: [] }] });
        await i18n.changeLanguage("zh-CN");
        mount();
        const time = new Date(at).toLocaleTimeString("zh-CN", { hour: "numeric", minute: "2-digit" });
        expect(await screen.findByText(`你在${time}停止了它，当时完成了 5 步中的 2 步。`)).toBeInTheDocument();
    });
});

describe("Needs you Not now (K5)", () => {
    const need = (id: string, extra = {}) => ({ id, kind: "review", sentence: id, createdAt: 0, targetKind: "handoff", targetId: id, ...extra });
    it("stores Not now, then lists it under Put off with Bring back now", async () => {
        mocks.list.mockResolvedValue([handoff("review", "active")]);
        mocks.needs.mockResolvedValue({ items: [need("review")], total: 1 });
        mount();
        const needs = await screen.findByRole("region", { name: "Needs you" });
        mocks.needs.mockResolvedValue({ items: [], putOff: [need("review")], total: 0 });
        fireEvent.click(await within(needs).findByRole("button", { name: "Not now" }));
        expect(mocks.putOff).toHaveBeenCalledWith("review", "put_off");
        expect(mocks.toast).not.toHaveBeenCalled();
        expect(await screen.findByText("Put off: 1 ·")).toBeInTheDocument();
        fireEvent.click(screen.getByRole("button", { name: "Show" }));
        fireEvent.click(screen.getByRole("button", { name: "Bring back now" }));
        expect(mocks.putOff).toHaveBeenCalledWith("review", "bring_back");
    });
    it("puts the card back when Not now doesn't save (T09)", async () => {
        mocks.list.mockResolvedValue([handoff("review", "active")]);
        mocks.needs.mockResolvedValue({ items: [need("review")], total: 1 });
        mocks.putOff.mockRejectedValueOnce(new Error("disk"));
        mount();
        const needs = await screen.findByRole("region", { name: "Needs you" });
        fireEvent.click(await within(needs).findByRole("button", { name: "Not now" }));
        expect(await screen.findByText("Your Not now didn't save, so it's back.")).toBeInTheDocument();
        expect(needs.querySelector('[data-needs-you-card="review"]')).not.toBeNull();
    });
    it("asks Keep · Done · Forget on the second return", async () => {
        mocks.list.mockResolvedValue([handoff("review", "needs_review")]);
        mocks.needs.mockResolvedValue({ items: [need("review", { asksKeep: true })], total: 1 });
        mount();
        const asks = await screen.findByTestId("needs-asks-keep");
        expect(within(asks).getAllByRole("button").map((button) => button.textContent)).toEqual(["Keep", "Done", "Forget"]);
        fireEvent.click(within(asks).getByRole("button", { name: "Keep" }));
        expect(mocks.putOff).toHaveBeenCalledWith("review", "keep");
        fireEvent.click(within(asks).getByRole("button", { name: "Forget" }));
        expect(mocks.putOff).toHaveBeenCalledWith("review", "forget");
    });
    it("puts a failed Do it back with Couldn't start it. Try again", async () => {
        mocks.list.mockResolvedValue([handoff("active", "active")]);
        mocks.accept.mockRejectedValueOnce(new Error("no"));
        mount();
        fireEvent.click(await within(await screen.findByRole("region", { name: "Needs you" })).findByRole("button", { name: "Do it" }));
        expect(await screen.findByText("Couldn't start it.", {}, { timeout: 7000 })).toBeInTheDocument();
        expect(screen.getByRole("button", { name: "Try again" })).toBeInTheDocument();
        expect(mocks.toast.error).not.toHaveBeenCalled();
    }, 10000);
});

describe("One task, one view (K2)", () => {
    it("says a linked task was deleted on the board", async () => {
        mount({ openHandoffId: "missing" });
        fireEvent.click(screen.getByRole("button", { name: "gone" }));
        expect(await screen.findByText("That task isn't in Tasks anymore.")).toBeInTheDocument();
        expect(screen.queryByTestId("task-thread")).toBeNull();
    });
    it("lists every finished task in All done, with or without files", async () => {
        mocks.list.mockResolvedValue([handoff("no files", "completed")]);
        mount();
        fireEvent.click(await within(await screen.findByRole("region", { name: "Done this week" })).findByRole("button", { name: "Show all" }));
        expect(within(screen.getByTestId("all-done")).getByText("no files")).toBeInTheDocument();
    });
});

describe("Up next failures (T07, T09, T11)", () => {
    it("puts a failed start back on its card, and a Not now that didn't save", async () => {
        vi.useFakeTimers({ shouldAdvanceTime: true });
        try {
            mocks.offers.mockResolvedValue([offer("first")]);
            mocks.decide.mockRejectedValue(new Error("disk"));
            mount();
            await screen.findByText("first");
            fireEvent.click(screen.getByRole("button", { name: "Not now" }));
            await act(async () => { await vi.advanceTimersByTimeAsync(5000); });
            expect(await screen.findByText("Your Not now didn't save, so it's back.")).toBeInTheDocument();
            expect(screen.getByText("first")).toBeInTheDocument();
            expect(mocks.toast).not.toHaveBeenCalled();
        } finally { vi.useRealTimers(); }
    });
    it("uses the table's copy in Chinese", async () => {
        await i18n.changeLanguage("zh-CN");
        expect(i18n.t("app:tasks.findMoreNothing")).toBe("暂时没有新建议。");
        expect(`${i18n.t("app:tasks.addedToScheduled")} · ${i18n.t("app:tasks.undo")} · ${i18n.t("app:tasks.edit")}`).toBe("已添加到定时任务 · 撤销 · 编辑");
    });
});

// K6: every failure on Tasks says so in place, gray or copper, never a toast.
describe("failures in place (K6)", () => {
    const noToast = () => {
        expect(mocks.toast).not.toHaveBeenCalled();
        expect(mocks.toast.error).not.toHaveBeenCalled();
        expect(mocks.toast.success).not.toHaveBeenCalled();
    };
    const tone = (el: HTMLElement) => {
        expect(el.className).not.toMatch(/error|red/);
        return el.getAttribute("data-failure-line");
    };

    it("says Scheduled couldn't load under its heading, never empty", async () => {
        const refresh = vi.fn();
        mount({ upcoming: { items: [], loading: false, failed: true, refresh } });
        const scheduled = await screen.findByRole("region", { name: "Scheduled" });
        const line = within(scheduled).getByTestId("scheduled-failed");
        expect(line).toHaveTextContent("Couldn't load scheduled work. Try again");
        expect(tone(line)).toBe("gray");
        expect(within(scheduled).queryByText(/Nothing scheduled/)).toBeNull();
        fireEvent.click(within(line).getByRole("button", { name: "Try again" }));
        expect(refresh).toHaveBeenCalled();
        noToast();
    });

    it("says Done this week couldn't load, and All done couldn't either", async () => {
        mocks.deliverables.mockRejectedValue(new Error("offline"));
        mount();
        const line = await screen.findByTestId("done-week-failed");
        expect(line).toHaveTextContent("Couldn't load this week's finished work. Try again");
        expect(tone(line)).toBe("gray");
        fireEvent.click(await screen.findByTestId("all-done-link"));
        const all = await screen.findByTestId("all-done-failed");
        expect(all).toHaveTextContent("Couldn't load your finished work. Try again");
        expect(screen.queryByText("Nothing finished yet")).toBeNull();
        mocks.deliverables.mockResolvedValue([]);
        fireEvent.click(within(all).getByRole("button", { name: "Try again" }));
        await waitFor(() => expect(screen.queryByTestId("all-done-failed")).toBeNull());
        noToast();
    });

    it("keeps the list on a failed refresh and says when it was read, in gray", async () => {
        mocks.list.mockResolvedValue([handoff("kept", "in_progress")]);
        mount();
        expect(await screen.findByText("kept")).toBeInTheDocument();
        mocks.list.mockRejectedValue(new Error("offline"));
        await emit("handoff:updated");
        const line = await screen.findByTestId("tasks-refresh-failed");
        expect(line).toHaveTextContent(/^Couldn't refresh · showing \d{1,2}:\d{2}\s?[AP]M · Try again$/);
        expect(tone(line)).toBe("gray");
        expect(screen.getByText("kept")).toBeInTheDocument();
        noToast();
    });

    it("says a failed Stop on the row in copper, with no toast", async () => {
        mocks.list.mockResolvedValue([handoff("running", "in_progress")]);
        mocks.abort.mockRejectedValue(new Error("busy"));
        mount();
        const running = await screen.findByRole("region", { name: "Running" });
        fireEvent.click(within(running).getByRole("button", { name: "Stop" }));
        const line = await within(running).findByTestId("task-stop-failed");
        expect(line).toHaveTextContent("Couldn't stop it. Try again");
        expect(tone(line)).toBe("copper");
        noToast();
    });

    it("says a file that won't open under its chip", async () => {
        mocks.deliverables.mockResolvedValue([{ id: "d1", source: "chat", sourceId: "s1", title: "Pricing summary", summary: "Pricing summary is ready.", status: "completed", files: [{ path: "/tmp/pricing.md", name: "pricing.md" }], createdAt: Date.now(), updatedAt: Date.now() }]);
        mocks.openFile.mockRejectedValue(new Error("missing"));
        mount();
        const done = await screen.findByRole("region", { name: "Done this week" });
        fireEvent.click(within(done).getByText("pricing.md"));
        const line = await within(done).findByTestId("done-file-failed");
        expect(line).toHaveTextContent("Couldn't open pricing.md. Try again");
        expect(tone(line)).toBe("gray");
        noToast();
    });

    it("keeps the delete dialog open with its line when a delete fails", async () => {
        const onDeleteSchedule = vi.fn(async () => false);
        mount({ onDeleteSchedule, upcoming: { loading: false, items: [schedule("alpha sync")] } });
        const scheduled = await screen.findByRole("region", { name: "Scheduled" });
        await userEvent.click(within(scheduled).getByRole("button", { name: "Delete alpha sync" }));
        const dialog = await screen.findByRole("dialog");
        await userEvent.click(within(dialog).getByRole("button", { name: "Delete" }));
        expect(onDeleteSchedule).toHaveBeenCalledWith("alpha sync");
        const line = await within(dialog).findByTestId("scheduled-delete-failed");
        expect(line).toHaveTextContent("Didn't delete. Try again");
        expect(tone(line)).toBe("copper");
        expect(screen.getByRole("dialog")).toBeInTheDocument();
        onDeleteSchedule.mockResolvedValueOnce(true);
        await userEvent.click(within(line).getByRole("button", { name: "Try again" }));
        await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
        noToast();
    });

    it("says each line in Chinese", async () => {
        await i18n.changeLanguage("zh-CN");
        mocks.deliverables.mockRejectedValue(new Error("offline"));
        mount({ upcoming: { items: [], loading: false, failed: true } });
        expect(await screen.findByTestId("scheduled-failed")).toHaveTextContent("定时任务没有加载出来。重试");
        expect(await screen.findByTestId("done-week-failed")).toHaveTextContent("本周完成的工作没有加载出来。重试");
    });
});

// K8: tasks.action and tasks.section_seen carry map ids and counts only.
describe("measuring Tasks (K8)", () => {
    it("records row ids, kinds and outcomes, and sections with row counts, never content", async () => {
        mocks.list.mockResolvedValue([handoff("Private running title", "in_progress")]);
        mocks.abort.mockRejectedValueOnce(new Error("private error words"));
        mount({ upcoming: { loading: false, items: [schedule("Private schedule name")] } });
        const running = await screen.findByRole("region", { name: "Running" });
        fireEvent.click(within(running).getByRole("button", { name: "Stop" }));
        await within(running).findByTestId("task-stop-failed");
        fireEvent.click(within(running).getByRole("button", { name: /Private running title/ }));
        await screen.findByTestId("task-thread");
        expect(mocks.recordTasks).toHaveBeenCalledWith("T16", "do", "failed", expect.any(Number));
        expect(mocks.recordTasks).toHaveBeenCalledWith("T15", "open", "opened", expect.any(Number));
        await waitFor(() => expect(mocks.recordTasksSeen).toHaveBeenCalledWith("running", 1));
        expect(mocks.recordTasksSeen).toHaveBeenCalledWith("scheduled", 1);
        const sent = JSON.stringify([mocks.recordTasks.mock.calls, mocks.recordTasksSeen.mock.calls]);
        expect(sent).not.toMatch(/Private|private/);
        for (const [id] of mocks.recordTasks.mock.calls as unknown as [string][]) expect(id).toMatch(/^[THDCE]\d{2}$/);
    });
});

// tasks-review v1.0.3: design's live walk-through of build ab2d86ee.
describe("tasks-review v1.0.11 page regression gate", () => {
    it("keeps the header and empty needs-you sentence", async () => {
        mount();
        expect(screen.getByRole("heading", { name: "What Isoform is doing for you" })).toBeInTheDocument();
        await waitFor(() => expect(screen.getByTestId("tasks-line")).toHaveTextContent("Nothing needs you."));
    });

    it("keeps the task box and Make tasks button", async () => {
        mount();
        expect(await screen.findByPlaceholderText("Give Isoform a task…")).toBeInTheDocument();
        expect(screen.getByRole("button", { name: "Make tasks" })).toBeInTheDocument();
    });

    it("keeps the empty Up next message and Find more", async () => {
        mount();
        const section = await screen.findByRole("region", { name: "Up next" });
        expect(await within(section).findByText("Nothing up next.")).toBeInTheDocument();
        expect(within(section).getByRole("button", { name: "Find more" })).toBeInTheDocument();
    });

    it("keeps both brief rows without descriptions or a Scheduled count", async () => {
        mount({ upcoming: { loading: false, items: [brief("morning", "07:00", { summary: "hidden morning description" }), brief("evening", "18:00", { summary: "hidden evening description" })] } });
        const section = await screen.findByRole("region", { name: "Scheduled" });
        expect(section.querySelector("header")).toHaveTextContent(/^Scheduled$/);
        for (const slot of ["morning", "evening"]) {
            const row = section.querySelector(`[data-scheduled-row="brief:${slot}"]`) as HTMLElement;
            expect(row).toBeInTheDocument();
            expect(row).not.toHaveTextContent(`hidden ${slot} description`);
        }
    });
});

describe("tasks-review v1.0.3", () => {
    const tomorrowAt = (hour: number) => { const d = new Date(); d.setDate(d.getDate() + 1); d.setHours(hour, 0, 0, 0); return d.getTime(); };
    const todayAt = (hour: number) => { const d = new Date(); d.setHours(hour, 0, 0, 0); return d.getTime(); };

    it("reads a brief row as its title and when, with the scheduler's next run and no count beside Scheduled", async () => {
        mount({ upcoming: { loading: false, items: [brief("morning", "07:00", { nextRunAt: tomorrowAt(7), summary: "What moved" }), schedule("weekly update")] } });
        const scheduled = await screen.findByRole("region", { name: "Scheduled" });
        const row = scheduled.querySelector('[data-scheduled-row="brief:morning"]') as HTMLElement;
        expect(row).toHaveTextContent("Every day, 7:00 AM");
        expect(row).toHaveTextContent("Next tomorrow");
        expect(row).not.toHaveTextContent("07:00");
        expect(row).not.toHaveTextContent("What moved, what is next");
        expect(within(row).queryByText(i18n.t("app:automationsPage.summaries.brief"))).toBeNull();
        // A schedule still says what it does.
        expect(within(scheduled.querySelector('[data-scheduled-row="weekly update"]') as HTMLElement).getByText("Does weekly update")).toBeInTheDocument();
        expect(scheduled.querySelector("header")).toHaveTextContent(/^Scheduled$/);
    });

    it("says a run still due today in words, with its true time", async () => {
        const due = new Date(); due.setHours(23, 59, 0, 0);
        mount({ upcoming: { loading: false, items: [brief("evening", "18:00", { nextRunAt: Math.max(due.getTime(), Date.now() + 1000) })] } });
        const row = (await screen.findByRole("region", { name: "Scheduled" })).querySelector('[data-scheduled-row="brief:evening"]') as HTMLElement;
        expect(row).toHaveTextContent("Every day, 6:00 PM");
        expect(row).toHaveTextContent(/Next today, \d{1,2}:\d{2} [AP]M/);
    });

    it("warns in gray only when no brief was made after its time, and never says as planned", async () => {
        mount({ upcoming: { loading: false, items: [
            brief("morning", "07:00", { nextRunAt: tomorrowAt(7), didntRunAt: todayAt(7), missedRuns: 3, lastReason: "kept_failing" }),
            brief("evening", "18:00", { nextRunAt: tomorrowAt(18), missedRuns: 3, failedAttempts: 3, lastReason: "kept_failing" }),
        ] } });
        const scheduled = await screen.findByRole("region", { name: "Scheduled" });
        const warning = scheduled.querySelector('[data-scheduled-row="brief:morning"] [data-brief-didnt-run]') as HTMLElement;
        expect(warning).toHaveTextContent("Didn't run at 7:00 AM · See why");
        expect(warning.className).toContain("text-muted");
        expect(warning.className).not.toContain("copper");
        // Today's evening brief exists (no didntRunAt): the row says nothing extra.
        const evening = scheduled.querySelector('[data-scheduled-row="brief:evening"]') as HTMLElement;
        expect(evening.querySelector("[data-brief-didnt-run], [data-schedule-failing]")).toBeNull();
        expect(scheduled).not.toHaveTextContent("as planned");
        fireEvent.click(within(scheduled).getByText("Morning brief"));
        expect(screen.getByTestId("brief-detail")).toHaveTextContent("morning");
    });

    it("tints the selected row with the accent, not a gray band", async () => {
        mount({ upcoming: { loading: false, items: [brief("morning", "07:00")] } });
        fireEvent.click(await screen.findByText("Morning brief"));
        const row = document.querySelector('[data-scheduled-row="brief:morning"]') as HTMLElement;
        expect(row).toHaveAttribute("data-selected", "true");
        expect(row.className).toContain("data-[selected]:bg-accent-light");
        expect(row.className).not.toContain("data-[selected]:bg-surface-active");
    });

    it("has no resize grip on the task box, which grows with its text", async () => {
        mount();
        const box = await screen.findByTestId("task-box") as HTMLTextAreaElement;
        expect(box.className).toContain("resize-none");
        expect(box.className).not.toMatch(/resize-(y|x|both)\b/);
        Object.defineProperty(box, "scrollHeight", { configurable: true, get: () => 20 * box.value.split("\n").length });
        fireEvent.change(box, { target: { value: "one\ntwo\nthree\nfour\nfive" } });
        expect(box.style.height).toBe("100px");
    });

    it("shows All done › whenever anything has ever finished, a scheduled run included", async () => {
        const old = new Date(); old.setMonth(old.getMonth() - 2);
        mocks.deliverables.mockResolvedValue([{ id: "automation:weekly:r1", source: "automation", sourceId: "weekly:r1", title: "Weekly update", summary: "Your team has it.", status: "completed", files: [{ path: "/tmp/weekly-update.md", name: "weekly-update.md" }], createdAt: old.getTime(), updatedAt: old.getTime() }]);
        mount();
        fireEvent.click(await screen.findByTestId("all-done-link"));
        const all = await screen.findByTestId("all-done");
        expect(within(all).getByText("Weekly update")).toBeInTheDocument();
        expect(within(all).getByText("weekly-update.md")).toBeInTheDocument();
        expect(all.querySelector("header")).toHaveTextContent(/^All done$/);
    });

    it("shows All done for a draft sent before this week", async () => {
        const old = new Date(); old.setMonth(old.getMonth() - 2);
        mocks.desktopTasks.mockResolvedValue({ tasks: [{ id: "draft.old", title: "An older reply", group: "done", status: "completed", updatedAt: old.toISOString(), outcome: { kind: "replied", by: "isoform", at: old.toISOString(), params: { person: "Dana" } } }] });
        mount();
        fireEvent.click(await screen.findByTestId("all-done-link"));
        expect(within(await screen.findByTestId("all-done")).getByText("An older reply")).toBeInTheDocument();
    });

    it("shows an old scheduled run from the retained history in All done", async () => {
        const old = new Date(); old.setMonth(old.getMonth() - 3);
        const run = { id: "automation:weekly:old", source: "automation", sourceId: "weekly:old", title: "Weekly update", summary: "Report ready", status: "completed", files: [], createdAt: old.getTime(), updatedAt: old.getTime() };
        mocks.desktopTasks.mockResolvedValue({ tasks: [], finishedDeliverables: [run] });
        mocks.deliverables.mockResolvedValue([]);
        mount();
        fireEvent.click(await screen.findByTestId("all-done-link"));
        expect(within(await screen.findByTestId("all-done")).getByText("Weekly update")).toBeInTheDocument();
    });

    it("offers All done and reports a failed task-history read", async () => {
        mocks.desktopTasks.mockRejectedValue(new Error("history unavailable"));
        mocks.deliverables.mockResolvedValue([]);
        mount();
        fireEvent.click(await screen.findByTestId("all-done-link"));
        expect(await screen.findByTestId("all-done-failed")).toHaveTextContent("Couldn't load your finished work.");
        expect(screen.queryByText("Nothing finished yet")).toBeNull();
    });

    it("shows a finished chat file in All done even when it has no summary", async () => {
        const old = new Date(); old.setMonth(old.getMonth() - 2);
        mocks.deliverables.mockResolvedValue([{ id: "chat:old", source: "chat", sourceId: "old", title: "Saved work", summary: "", status: "completed", files: [{ path: "/tmp/work.md", name: "work.md" }], createdAt: old.getTime(), updatedAt: old.getTime() }]);
        mount();
        fireEvent.click(await screen.findByTestId("all-done-link"));
        expect(within(await screen.findByTestId("all-done")).getByText("Saved work")).toBeInTheDocument();
    });

    it("lists a scheduled run that finished this week in Done this week", async () => {
        mocks.deliverables.mockResolvedValue([{ id: "automation:weekly:r2", source: "automation", sourceId: "weekly:r2", title: "Weekly update", summary: "Your team has it.", status: "completed", files: [{ path: "/tmp/weekly-update.md", name: "weekly-update.md" }], createdAt: Date.now(), updatedAt: Date.now() }]);
        mount();
        const done = await screen.findByRole("region", { name: "Done this week" });
        expect(within(done).getByText("Weekly update")).toBeInTheDocument();
        expect(done.querySelector("header h2")?.nextElementSibling?.textContent ?? "").not.toMatch(/^\d+$/);
        expect(screen.getByTestId("all-done-link")).toHaveTextContent("All done ›");
    });

    it("says the tries and the cause while Isoform is still trying, as Today does (today-review v1.1.13 D3)", async () => {
        mount({ upcoming: { loading: false, items: [brief("morning", "07:00", { nextRunAt: tomorrowAt(7), didntRunAt: todayAt(7), triedAttempts: 9, triedCause: "timeout" })] } });
        const row = (await screen.findByRole("region", { name: "Scheduled" })).querySelector('[data-scheduled-row="brief:morning"]') as HTMLElement;
        expect(row.querySelector("[data-brief-tries]")).toHaveTextContent("Tried 9 times. It timed out. Isoform tries again at 7:00 AM.");
        expect(row).not.toHaveTextContent("See why");
    });

    it("says it in zh-CN", async () => {
        await i18n.changeLanguage("zh-CN");
        mount({ upcoming: { loading: false, items: [brief("morning", "07:00", { nextRunAt: tomorrowAt(7), didntRunAt: todayAt(7) })] } });
        const row = (await screen.findByRole("region", { name: "定时任务" })).querySelector('[data-scheduled-row="brief:morning"]') as HTMLElement;
        expect(row).toHaveTextContent("每天7:00");
        expect(row).toHaveTextContent("下次：明天");
        expect(row).toHaveTextContent("没有在7:00运行 · 查看原因");
        expect(i18n.t("app:tasks.didntRun")).toBe("没有运行 · 查看原因");
        expect(i18n.t("app:tasks.allDoneLink")).toBe("全部已完成 ›");
    });
});


describe("Up next on a refresh (tasks-review v1.0.14 K2)", () => {
    it("keeps what's on screen while it refreshes: placeholders only on the first load", async () => {
        mocks.offers.mockResolvedValue([offer("Draft the launch note")]);
        mount();
        expect(await screen.findByText("Draft the launch note")).toBeInTheDocument();
        mocks.offers.mockImplementation(() => new Promise(() => undefined));
        act(() => { mocks.events.get("app:ready")?.forEach((handler: (payload?: unknown) => unknown) => handler()); });
        await new Promise((resolve) => setTimeout(resolve, 650));
        expect(mocks.offers.mock.calls.length).toBeGreaterThan(1);
        expect(screen.queryByTestId("task-deck-placeholder")).toBeNull();
        expect(screen.getByText("Draft the launch note")).toBeInTheDocument();
    });
});
