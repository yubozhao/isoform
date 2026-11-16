import { act, cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import i18n from "@/i18n";
import { resetPageQueries } from "@/lib/page-queries";
import { pageQueryCache } from "@/lib/query-cache";
import type { GoalItemView, GoalsPageView, GoalStoryView } from "@/lib/wails-shim-app";
import { GoalsPage, goalStatus } from "./GoalsPage";

const mocks = vi.hoisted(() => ({
    handlers: new Map<string, Set<(...args: unknown[]) => void>>(),
    listGoals: vi.fn(),
    getGoal: vi.fn(),
    moveGoal: vi.fn(),
    toast: Object.assign(vi.fn(), { error: vi.fn() }),
}));

vi.mock("sonner", () => ({ toast: mocks.toast }));
vi.mock("@/lib/wails-shim-app", () => ({
    ListGoals: mocks.listGoals,
    GetGoal: mocks.getGoal,
    MoveGoal: mocks.moveGoal,
}));
vi.mock("../../../wailsjs/runtime/runtime", () => ({
    EventsOn: (name: string, handler: (...args: unknown[]) => void) => {
        const set = mocks.handlers.get(name) ?? new Set();
        set.add(handler);
        mocks.handlers.set(name, set);
        return () => set.delete(handler);
    },
}));

// Thursday, October 22, 2026.
const NOW = new Date(2026, 9, 22, 10, 0);
const TUESDAY = new Date(2026, 9, 20, 9, 0).toISOString();

function goal(overrides: Partial<GoalItemView>): GoalItemView {
    return {
        id: "g", eventId: "e1", kind: "goal", title: "Goal", state: "active", group: "tracking", slipping: false,
        addedAt: TUESDAY, updatedAt: TUESDAY, lastEvent: "discovered", origin: "chat", ...overrides,
    };
}

const PAGE: GoalsPageView = {
    tracking: [
        goal({ id: "passport", title: "Renew passport", state: "waiting", waitingOn: "Ana", lastEvent: "waiting", dueAt: new Date(2026, 10, 2, 23, 59).toISOString(), dueDate: "2026-11-02" }),
        goal({ id: "follow", title: "Send Dana the notes", kind: "commitment", origin: "activity" }),
    ],
    done: [goal({ id: "dentist", title: "Book the dentist", state: "resolved", group: "done", lastEvent: "resolved", updatedAt: new Date(2026, 9, 21, 9).toISOString() })],
    held: [goal({ id: "held", title: "Answer Dana about the renewal", group: "held", origin: "noticed" })],
};

async function renderPage(page: GoalsPageView = PAGE) {
    mocks.listGoals.mockResolvedValue(page);
    const onTalkAbout = vi.fn();
    const onCreateGoal = vi.fn();
    await act(async () => {
        render(<GoalsPage now={NOW} onTalkAbout={onTalkAbout} onCreateGoal={onCreateGoal} />);
    });
    return { onTalkAbout, onCreateGoal };
}

const openMenu = (title: string) => fireEvent.keyDown(screen.getByRole("button", { name: `More for ${title}` }), { key: "Enter" });

/** Emits a runtime event through the handlers the page-query installer and
 *  the page registered, flushing the synchronous withdrawal and the re-reads. */
function emitRuntime(event: string) {
    return act(async () => {
        for (const handler of mocks.handlers.get(event) ?? []) handler(undefined);
    });
}

beforeEach(async () => {
    resetPageQueries();
    mocks.handlers.clear();
    await i18n.changeLanguage("en");
    vi.clearAllMocks();
});
afterEach(cleanup);

describe("the Goals page (item 66)", () => {
    it("lists tracking, done and kept-here goals with sentences in the user's language", async () => {
        await renderPage();
        const tracking = within(screen.getByTestId("goals-tracking"));
        expect(tracking.getByText("Tracking")).toBeTruthy();
        expect(tracking.getByText("Waiting on Ana since Tuesday. Due Nov 2.")).toBeTruthy();
        expect(tracking.getByText("You added it from Activity on Tuesday.")).toBeTruthy();
        const done = within(screen.getByTestId("goals-done"));
        expect(done.getByText("Done (1)")).toBeTruthy();
        expect(done.getByText("Done yesterday.")).toBeTruthy();
        const held = within(screen.getByTestId("goals-held"));
        expect(held.getByText("Only on this computer (1)")).toBeTruthy();
        expect(held.getByText("Kept here because its words repeat a mail subject.")).toBeTruthy();
    });

    it("checks a goal off with Undo and forgets one from its menu", async () => {
        await renderPage();
        mocks.moveGoal.mockResolvedValueOnce({ id: "follow", eventId: "e2", state: "resolved", title: "Send Dana the notes" });
        await act(async () => { fireEvent.click(screen.getByRole("button", { name: "Mark done, Send Dana the notes" })); });
        expect(mocks.moveGoal).toHaveBeenCalledWith(expect.objectContaining({ goalId: "follow", action: "resolve", expectedEventId: "e1" }));
        const doneRow = screen.getByText("Send Dana the notes").closest("li")!;
        expect(within(doneRow).getByText("Marked done")).toBeInTheDocument();
        expect(mocks.toast).not.toHaveBeenCalled();
        mocks.moveGoal.mockResolvedValueOnce({ id: "follow", eventId: "e3", state: "active", title: "Send Dana the notes" });
        await act(async () => { fireEvent.click(within(doneRow).getByRole("button", { name: "Undo" })); });
        expect(mocks.moveGoal).toHaveBeenLastCalledWith(expect.objectContaining({ goalId: "follow", action: "restore", expectedEventId: "e2" }));
        expect(screen.getByRole("button", { name: "Mark done, Send Dana the notes" })).toBeInTheDocument();

        mocks.moveGoal.mockResolvedValueOnce({ id: "passport", eventId: "e3", state: "dismissed", title: "Renew passport" });
        openMenu("Renew passport");
        await act(async () => { fireEvent.click(await screen.findByText("Forget this goal")); });
        expect(mocks.moveGoal).toHaveBeenLastCalledWith(expect.objectContaining({ goalId: "passport", action: "forget" }));
        const forgottenRow = screen.getByText("Renew passport").closest("li")!;
        expect(within(forgottenRow).getByText("Forgotten")).toBeInTheDocument();
        expect(within(forgottenRow).getByRole("button", { name: "Undo" })).toBeEnabled();
    });

    it("keeps an Undo failure in the row and allows another attempt", async () => {
        await renderPage();
        mocks.moveGoal.mockResolvedValueOnce({ id: "follow", eventId: "e2", state: "resolved" });
        await act(async () => { fireEvent.click(screen.getByRole("button", { name: "Mark done, Send Dana the notes" })); });
        const row = screen.getByText("Send Dana the notes").closest("li")!;
        mocks.moveGoal.mockRejectedValueOnce(new Error("offline"));
        await act(async () => { fireEvent.click(within(row).getByRole("button", { name: "Undo" })); });
        expect(within(row).getByRole("alert")).toHaveTextContent("Couldn't undo. Try again.");
        mocks.moveGoal.mockResolvedValueOnce({ id: "follow", eventId: "e3", state: "active" });
        await act(async () => { fireEvent.click(within(row).getByRole("button", { name: "Undo" })); });
        expect(screen.getByRole("button", { name: "Mark done, Send Dana the notes" })).toBeInTheDocument();
    });

    it("ends the inline Undo window after five seconds", async () => {
        await renderPage();
        vi.useFakeTimers();
        try {
            mocks.moveGoal.mockResolvedValueOnce({ id: "follow", eventId: "e2", state: "resolved" });
            await act(async () => { fireEvent.click(screen.getByRole("button", { name: "Mark done, Send Dana the notes" })); });
            expect(screen.getByText("Marked done")).toBeInTheDocument();
            await act(async () => { vi.advanceTimersByTime(5_000); });
            expect(screen.queryByText("Marked done")).not.toBeInTheDocument();
        } finally {
            vi.useRealTimers();
        }
    });

    it("edits a goal's title and due date", async () => {
        await renderPage();
        mocks.moveGoal.mockResolvedValueOnce({ id: "passport", eventId: "e4", state: "waiting", title: "Renew my passport" });
        openMenu("Renew passport");
        await act(async () => { fireEvent.click(await screen.findByText("Edit")); });
        fireEvent.change(screen.getByLabelText("Goal"), { target: { value: "Renew my passport" } });
        fireEvent.change(screen.getByLabelText("Due date"), { target: { value: "2026-11-09" } });
        await act(async () => { fireEvent.click(screen.getByRole("button", { name: "Save" })); });
        expect(mocks.moveGoal).toHaveBeenCalledWith(expect.objectContaining({
            goalId: "passport", action: "revise", title: "Renew my passport", dueDate: "2026-11-09", setDue: true, expectedEventId: "e1",
        }));
    });

    it("keeps a due the form does not show when only the title changes", async () => {
        await renderPage();
        mocks.moveGoal.mockResolvedValueOnce({ id: "follow", eventId: "e5", state: "active", title: "Send Dana the signed notes" });
        openMenu("Send Dana the notes");
        await act(async () => { fireEvent.click(await screen.findByText("Edit")); });
        fireEvent.change(screen.getByLabelText("Goal"), { target: { value: "Send Dana the signed notes" } });
        await act(async () => { fireEvent.click(screen.getByRole("button", { name: "Save" })); });
        const sent = mocks.moveGoal.mock.calls.at(-1)![0];
        expect(sent).toMatchObject({ action: "revise", title: "Send Dana the signed notes" });
        expect(sent).not.toHaveProperty("setDue");
        expect(sent).not.toHaveProperty("dueDate");
    });

    it("keeps the Waiting on text when the save is refused", async () => {
        await renderPage();
        mocks.moveGoal.mockRejectedValueOnce("goal_changed");
        openMenu("Send Dana the notes");
        await act(async () => { fireEvent.click(await screen.findByText("Waiting on someone")); });
        fireEvent.change(screen.getByLabelText("Who or what"), { target: { value: "Dana" } });
        await act(async () => { fireEvent.click(screen.getByRole("button", { name: "Save" })); });
        expect(mocks.toast.error).toHaveBeenCalledWith("This goal changed. The page shows the latest now.");
        expect((screen.getByLabelText("Who or what") as HTMLInputElement).value).toBe("Dana");
    });

    it("says so when a goal's story cannot be read", async () => {
        await renderPage();
        mocks.getGoal.mockRejectedValueOnce("goal_unavailable");
        await act(async () => { fireEvent.click(screen.getByText("Renew passport")); });
        expect(screen.getByTestId("goal-story-failed").textContent).toBe("Isoform couldn't read what happened to this goal. It shows here once your computer answers.");
    });

    it("opens a goal's story and talks about it in the main chat", async () => {
        const { onTalkAbout } = await renderPage();
        const story: GoalStoryView = {
            goal: PAGE.tracking[0],
            history: [{ event: "waiting", at: new Date(2026, 9, 21, 9).toISOString() }, { event: "discovered", at: TUESDAY }],
            sources: [{ kind: "you", count: 1 }, { kind: "activity", count: 2 }],
        };
        mocks.getGoal.mockResolvedValueOnce(story);
        await act(async () => { fireEvent.click(screen.getByText("Renew passport")); });
        expect(mocks.getGoal).toHaveBeenCalledWith("passport");
        expect(screen.getByTestId("goal-timeline").textContent).toBe("Added Oct 20 · Waiting Oct 21");
        expect(screen.getByTestId("goal-sources").textContent).toBe("Rests on what you said (1), Activity (2)");
        fireEvent.click(screen.getByRole("button", { name: "Talk about this goal" }));
        expect(onTalkAbout).toHaveBeenCalledWith(PAGE.tracking[0], "Let's talk about my goal: Renew passport");
        fireEvent.click(screen.getByRole("button", { name: "About you" }));
        expect(screen.getByTestId("goals-tracking")).toBeTruthy();
    });

    it("opens on the goal Search asked for and hands the request back", async () => {
        mocks.listGoals.mockResolvedValue(PAGE);
        mocks.getGoal.mockResolvedValue({ goal: PAGE.tracking[0], history: [], sources: [] });
        const onOpenGoalHandled = vi.fn();
        await act(async () => {
            render(<GoalsPage now={NOW} onTalkAbout={vi.fn()} onCreateGoal={vi.fn()} openGoalId="passport" openGoalSignal={1} onOpenGoalHandled={onOpenGoalHandled} />);
        });
        expect(mocks.getGoal).toHaveBeenCalledWith("passport");
        expect(screen.getByRole("button", { name: "Talk about this goal" })).toBeInTheDocument();
        expect(onOpenGoalHandled).toHaveBeenCalledTimes(1);
    });

    it("hands Create a goal to the main chat and says so when there are none", async () => {
        const { onCreateGoal } = await renderPage({ tracking: [], done: [], held: [] });
        expect(screen.getByTestId("goals-empty").textContent).toBe("No goals yet. Tell Isoform what you're working toward and it keeps track here.");
        fireEvent.click(screen.getByTestId("goals-create"));
        expect(onCreateGoal).toHaveBeenCalledWith("I want to set a new goal: ");
    });

    it("writes the sentences in Chinese too", async () => {
        await i18n.changeLanguage("zh-CN");
        const t = (key: string, options?: Record<string, unknown>) => i18n.t(key as never, { ns: "app", ...options }) as string;
        expect(goalStatus(PAGE.tracking[1], NOW, "zh-CN", t)).toBe("你周二从“活动”添加的。");
    });
});

describe("the Goals page with the page query cache", () => {
    it("paints a warm cache at once, without the loading spinner", async () => {
        pageQueryCache.set("ListGoals", [], PAGE);
        mocks.listGoals.mockResolvedValue(PAGE);
        render(<GoalsPage now={NOW} onTalkAbout={vi.fn()} onCreateGoal={vi.fn()} />);
        expect(screen.getByText("Renew passport")).toBeInTheDocument();
        expect(screen.queryByLabelText("Loading your goals")).toBeNull();
        await act(async () => {});
    });

    it("shows the loading spinner while a cold cache reads", () => {
        mocks.listGoals.mockReturnValue(new Promise(() => undefined));
        render(<GoalsPage now={NOW} onTalkAbout={vi.fn()} onCreateGoal={vi.fn()} />);
        expect(screen.getByLabelText("Loading your goals")).toBeInTheDocument();
        expect(screen.queryByText("Renew passport")).toBeNull();
    });

    it("paints the stale cached goals, then the revalidation's", async () => {
        const clock = vi.spyOn(Date, "now");
        try {
            clock.mockReturnValue(1_000_000);
            pageQueryCache.set("ListGoals", [], { ...PAGE, tracking: [goal({ id: "stale", title: "Earlier passport errand" })] });
            clock.mockReturnValue(1_000_000 + 61_000);
            mocks.listGoals.mockResolvedValue(PAGE);
            render(<GoalsPage now={NOW} onTalkAbout={vi.fn()} onCreateGoal={vi.fn()} />);
            expect(screen.getByText("Earlier passport errand")).toBeInTheDocument();
            expect(screen.queryByLabelText("Loading your goals")).toBeNull();
            expect(await screen.findByText("Renew passport")).toBeInTheDocument();
            expect(screen.queryByText("Earlier passport errand")).toBeNull();
        } finally {
            clock.mockRestore();
        }
    });
});

describe("the Goals page privacy withdrawal", () => {
    it("drops the lists and the open story on a privacy event, before the re-read lands", async () => {
        await renderPage();
        const story: GoalStoryView = {
            goal: PAGE.tracking[0],
            history: [{ event: "waiting", at: new Date(2026, 9, 21, 9).toISOString() }, { event: "discovered", at: TUESDAY }],
            sources: [{ kind: "you", count: 1 }],
        };
        mocks.getGoal.mockResolvedValueOnce(story);
        await act(async () => { fireEvent.click(screen.getByText("Renew passport")); });
        expect(screen.getByTestId("goal-timeline")).toBeInTheDocument();

        // The sweep's re-read never answers; the withdrawal must not wait on it.
        mocks.listGoals.mockImplementation(() => new Promise(() => undefined));
        await emitRuntime("memory:withdrawn");
        expect(screen.queryByTestId("goal-timeline")).toBeNull();
        expect(screen.queryByText("Renew passport")).toBeNull();
        expect(screen.getByLabelText("Loading your goals")).toBeInTheDocument();

        // Permitted recovery: an ordinary invalidation re-reads what is still
        // permitted and the lists paint again.
        mocks.listGoals.mockResolvedValue(PAGE);
        await emitRuntime("agency:updated");
        expect(await screen.findByText("Renew passport")).toBeInTheDocument();
        expect(screen.queryByLabelText("Loading your goals")).toBeNull();
    });

    it("closes the waiting and edit dialogs on a privacy event", async () => {
        await renderPage();
        openMenu("Send Dana the notes");
        await act(async () => { fireEvent.click(await screen.findByText("Waiting on someone")); });
        expect(screen.getByLabelText("Who or what")).toBeInTheDocument();
        mocks.listGoals.mockImplementation(() => new Promise(() => undefined));
        await emitRuntime("memory:withdrawn");
        expect(screen.queryByLabelText("Who or what")).toBeNull();
        expect(screen.getByLabelText("Loading your goals")).toBeInTheDocument();

        mocks.listGoals.mockResolvedValue(PAGE);
        await emitRuntime("agency:updated");
        expect(await screen.findByText("Send Dana the notes")).toBeInTheDocument();

        openMenu("Send Dana the notes");
        await act(async () => { fireEvent.click(await screen.findByText("Edit")); });
        expect(screen.getByLabelText("Goal")).toBeInTheDocument();
        mocks.listGoals.mockImplementation(() => new Promise(() => undefined));
        await emitRuntime("closed-eyes:changed");
        expect(screen.queryByLabelText("Goal")).toBeNull();
        expect(screen.queryByText("Send Dana the notes")).toBeNull();
        expect(screen.getByLabelText("Loading your goals")).toBeInTheDocument();
    });

    it("keeps the painted goals on an ordinary invalidation", async () => {
        await renderPage();
        mocks.listGoals.mockImplementation(() => new Promise(() => undefined));
        await emitRuntime("agency:updated");
        expect(screen.getByText("Renew passport")).toBeInTheDocument();
        expect(screen.queryByLabelText("Loading your goals")).toBeNull();
    });
});
