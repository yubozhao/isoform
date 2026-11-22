import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import i18n from "@/i18n";
import type { WorkItem, WorkList } from "@/lib/wails-shim-app";
import { TodayKeysLine, TodayYourWork, workWhen } from "./TodayYourWork";

vi.mock("@/lib/wails-shim-app", () => ({ RecordTodayAction: vi.fn().mockResolvedValue(undefined) }));
vi.mock("@/lib/today-telemetry", () => ({ measureToday: (_id: string, _kind: string, _outcome: string, run: () => unknown) => run() }));

const NOW = new Date(2026, 10, 15, 18, 30);

function item(id: string, order: number, overrides: Partial<WorkItem> = {}): WorkItem {
    return {
        id, kind: "offered", row: "offered", state: "open", source: "brief", ref: {},
        first: [{ text: "For your 10:00 with Lena" }, { text: "from the brief", ref: "brief:x" }],
        noticed: `Noticed ${id}`, work: { title: `Work ${id}`, text: `Text ${id}` },
        yes: { does: "mail_draft", line: "Yes puts it in your Mail drafts. You send it." },
        reasons: ["already_done", "not_now"], createdAt: NOW.toISOString(), order, ...overrides,
    };
}

function list(items: WorkItem[]): WorkList {
    return { v: 1, version: "1", generatedAt: NOW.toISOString(), toAnswer: items.filter((i) => i.state === "open").length, backFromAgents: 0, undoUntil: new Date(2026, 10, 16, 7).toISOString(), items };
}

function setup(items: WorkItem[]) {
    const onAnswer = vi.fn().mockResolvedValue(null);
    const onUndo = vi.fn().mockResolvedValue(null);
    const view = render(<TodayYourWork list={list(items)} active onAnswer={onAnswer} onUndo={onUndo} now={NOW} />);
    return { onAnswer, onUndo, rerender: (next: WorkItem[]) => view.rerender(<TodayYourWork list={list(next)} active onAnswer={onAnswer} onUndo={onUndo} now={NOW} />) };
}

// Keys answer the item that has focus: the open item unless something else has it.
const key = async (k: string, init: KeyboardEventInit = {}) => {
    const focused = document.activeElement && document.activeElement !== document.body ? document.activeElement as HTMLElement : null;
    const target = focused ?? screen.queryByTestId("your-work-open") ?? document.body;
    target.focus?.();
    await act(async () => { fireEvent.keyDown(target, { key: k, ...init }); });
};

describe("TodayYourWork", () => {
    beforeEach(async () => { await i18n.changeLanguage("en"); });
    afterEach(cleanup);

    it("shows the open item with the computer's words, its keys and where it is in the list", () => {
        setup([item("a", 0), item("b", 1), item("c", 2, { state: "with_agent", agent: { name: "Codex", since: new Date(2026, 10, 15, 17, 10).toISOString() } })]);
        const open = screen.getByTestId("your-work-open");
        expect(open).toHaveTextContent("For your 10:00 with Lena · from the brief");
        expect(open).toHaveTextContent("Noticed a");
        expect(screen.getByTestId("your-work-yes-line")).toHaveTextContent("Yes puts it in your Mail drafts. You send it.");
        expect(screen.getByTestId("your-work-move")).toHaveTextContent("1 of 2 · ↑↓ to move");
        expect(screen.getByTestId("your-work-up-next")).toHaveTextContent("Work b");
        expect(screen.getByTestId("your-work-with-agents")).toHaveTextContent(/Work c · Codex · since 5:10/);
    });

    it("moves with the arrows and answers with Y and M", async () => {
        const { onAnswer } = setup([item("a", 0), item("b", 1)]);
        await key("ArrowDown");
        expect(document.activeElement).toHaveAttribute("data-item-id", "b");
        await key("y");
        expect(onAnswer).toHaveBeenCalledWith("b", { action: "yes" });
        await key("ArrowUp");
        await key("m");
        expect(onAnswer).toHaveBeenLastCalledWith("a", { action: "not_mine" });
    });

    it("asks why on N, with the item's reasons and Skip", async () => {
        const { onAnswer } = setup([item("a", 0)]);
        await key("n");
        expect(onAnswer).not.toHaveBeenCalled();
        await act(async () => { fireEvent.click(screen.getByText("Not now")); });
        expect(onAnswer).toHaveBeenCalledWith("a", { action: "no", reason: "not_now" });
    });

    it("edits in place with E and sends with Command-Return", async () => {
        const { onAnswer } = setup([item("a", 0)]);
        await key("e");
        const field = screen.getByTestId("your-work-edit");
        fireEvent.change(field, { target: { value: "My words" } });
        // Keys typed in the field never answer.
        await act(async () => { fireEvent.keyDown(field, { key: "y" }); });
        expect(onAnswer).not.toHaveBeenCalled();
        await act(async () => { fireEvent.keyDown(field, { key: "Enter", metaKey: true }); });
        expect(onAnswer).toHaveBeenCalledWith("a", { action: "edit", before: "Text a", after: "My words" });
    });

    it("judges a result back with Useful and Not useful", async () => {
        const { onAnswer } = setup([item("r", 0, { state: "back", kind: "result", row: "result", reasons: [] })]);
        expect(screen.getByText("Useful")).toBeInTheDocument();
        expect(screen.queryByText("Not mine")).toBeNull();
        await key("n");
        expect(onAnswer).toHaveBeenCalledWith("r", { action: "not_useful" });
    });

    it("lists Done today with Undo, and Command-Z undoes the last answer", async () => {
        const answered = item("d", 3, { state: "answered", answer: { action: "yes", at: new Date(2026, 10, 15, 18, 0).toISOString(), surface: "phone", line: "Reply to Priya: in your Mail drafts" } });
        const { onUndo } = setup([item("a", 0), answered]);
        expect(screen.getByTestId("your-work-done")).toHaveTextContent("Reply to Priya: in your Mail drafts");
        await key("z", { metaKey: true });
        expect(onUndo).toHaveBeenCalledWith("d");
    });

    it("says why an answer didn't go through", async () => {
        const onAnswer = vi.fn().mockResolvedValue("changed");
        render(<TodayYourWork list={list([item("a", 0)])} active onAnswer={onAnswer} onUndo={vi.fn()} now={NOW} />);
        await key("y");
        expect(screen.getByTestId("your-work-error")).toHaveTextContent("This item was answered or changed.");
    });

    it("leaves the keys alone while Today is not in front", async () => {
        const onAnswer = vi.fn();
        render(<TodayYourWork list={list([item("a", 0)])} active={false} onAnswer={onAnswer} onUndo={vi.fn()} now={NOW} />);
        await key("y");
        expect(onAnswer).not.toHaveBeenCalled();
    });

    it("keeps Yes and Hand it over off on agent work until the hand-off sheet exists", async () => {
        const { onAnswer } = setup([item("a", 0, { yes: { does: "agent", line: "Yes hands it to Codex, your agent.", agent: "codex" } })]);
        expect(screen.getByRole("button", { name: /^Yes/ })).toBeDisabled();
        expect(screen.getByTestId("your-work-handoff-soon")).toHaveTextContent("Handing work to your agent comes next.");
        await key("y");
        expect(onAnswer).not.toHaveBeenCalled();
        await key("e");
        fireEvent.change(screen.getByTestId("your-work-edit"), { target: { value: "Mine" } });
        expect(screen.getByRole("button", { name: /Hand it over/ })).toBeDisabled();
        await act(async () => { fireEvent.keyDown(screen.getByTestId("your-work-edit"), { key: "Enter", metaKey: true }); });
        expect(onAnswer).not.toHaveBeenCalled();
        await act(async () => { fireEvent.keyDown(screen.getByTestId("your-work-edit"), { key: "Escape" }); });
        await key("m");
        expect(onAnswer).toHaveBeenCalledWith("a", { action: "not_mine" });
    });

    it("opens the hand-off sheet for a Yes on agent work, with the edit when there is one", async () => {
        const onAnswer = vi.fn();
        const onHandOff = vi.fn();
        const agentItem = item("a", 0, { yes: { does: "agent", line: "Yes hands it to Codex, your agent.", agent: "codex" } });
        render(<TodayYourWork list={list([agentItem])} active onAnswer={onAnswer} onUndo={vi.fn()} onHandOff={onHandOff} now={NOW} />);
        expect(screen.queryByTestId("your-work-handoff-soon")).toBeNull();
        await key("y");
        expect(onHandOff).toHaveBeenCalledWith(expect.objectContaining({ id: "a" }));
        await key("e");
        fireEvent.change(screen.getByTestId("your-work-edit"), { target: { value: "Mine" } });
        await act(async () => { fireEvent.keyDown(screen.getByTestId("your-work-edit"), { key: "Enter", metaKey: true }); });
        expect(onHandOff).toHaveBeenLastCalledWith(expect.objectContaining({ id: "a" }), "Mine");
        expect(onAnswer).not.toHaveBeenCalled();
    });

    it("offers Send on a Yes not sent", () => {
        const onHandOff = vi.fn();
        const notSent = item("s", 1, { state: "answered", yes: { does: "agent", line: "", agent: "codex" }, answer: { action: "yes", at: NOW.toISOString(), surface: "computer", line: "Test plan: Yes · not sent", notSent: true } });
        render(<TodayYourWork list={list([notSent])} active onAnswer={vi.fn()} onUndo={vi.fn()} onHandOff={onHandOff} now={NOW} />);
        expect(screen.getByTestId("your-work-done")).toHaveTextContent("Test plan: Yes · not sent");
        fireEvent.click(screen.getByTestId("your-work-send"));
        expect(onHandOff).toHaveBeenCalledWith(expect.objectContaining({ id: "s" }));
    });

    it("words a first-line part from its time when it has one", () => {
        setup([item("a", 0, { first: [{ text: "For your meeting" }, { text: "tomorrow", at: new Date(2026, 10, 16, 10).toISOString() }] })]);
        expect(screen.getByTestId("your-work-first")).toHaveTextContent(/For your meeting · tomorrow 10:00/);
    });

    it("heads a result back with who made it and asks if it was useful", () => {
        setup([item("r", 0, { state: "back", kind: "result", row: "result", agent: { name: "Codex" }, answer: { action: "yes", at: new Date(2026, 10, 15, 16, 10).toISOString(), surface: "computer" } })]);
        expect(screen.getByTestId("your-work-back-from")).toHaveTextContent(/Back from Codex · you said yes at 4:10/);
        expect(screen.getByText("Was it useful?")).toBeInTheDocument();
    });

    it("says when there has been no word from an agent for two hours", () => {
        setup([item("a", 0, { state: "with_agent", agent: { name: "Codex", since: new Date(2026, 10, 15, 16, 0).toISOString() } })]);
        expect(screen.getByTestId("your-work-no-word")).toHaveTextContent(/No word from Codex since 4:00/);
    });

    it("shows the answer as one line with Undo for five seconds", async () => {
        vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
        try {
            const answered = { ...item("a", 0), state: "answered", answer: { action: "yes", at: NOW.toISOString(), surface: "computer", line: "Prep for Lena: kept for 10:00" } };
            const onAnswer = vi.fn().mockResolvedValue(null);
            const view = render(<TodayYourWork list={list([item("a", 0)])} active onAnswer={onAnswer} onUndo={vi.fn()} now={NOW} />);
            await key("y");
            view.rerender(<TodayYourWork list={list([answered])} active onAnswer={onAnswer} onUndo={vi.fn()} now={NOW} />);
            expect(screen.getByTestId("your-work-undo-line")).toHaveTextContent("Prep for Lena: kept for 10:00");
            act(() => { vi.advanceTimersByTime(5_000); });
            expect(screen.queryByTestId("your-work-undo-line")).toBeNull();
        } finally {
            vi.useRealTimers();
        }
    });

    it("says no agent is connected yet, once", () => {
        render(<TodayYourWork list={list([item("a", 0)])} active onAnswer={vi.fn()} onUndo={vi.fn()} noAgents now={NOW} />);
        expect(screen.getByTestId("your-work-no-agents")).toHaveTextContent("None connected yet.");
    });

    it("opens what the source line names", () => {
        const onOpenRef = vi.fn().mockReturnValue(true);
        render(<TodayYourWork list={list([item("a", 0, { first: [{ text: "Halcyon import" }, { text: "from your topics", ref: "topic:halcyon" }] })])} active onAnswer={vi.fn()} onUndo={vi.fn()} onOpenRef={onOpenRef} now={NOW} />);
        expect(screen.getByTestId("your-work-first")).toHaveTextContent("Halcyon import · from your topics");
        fireEvent.click(screen.getByTestId("your-work-source"));
        expect(onOpenRef).toHaveBeenCalledWith("topic:halcyon");
    });

    it("answers only the focused item, never with a modifier or while composing", async () => {
        const { onAnswer } = setup([item("a", 0)]);
        await act(async () => { fireEvent.keyDown(document.body, { key: "y" }); });
        expect(onAnswer).not.toHaveBeenCalled();
        const open = screen.getByTestId("your-work-open");
        open.focus();
        await act(async () => { fireEvent.keyDown(open, { key: "y", ctrlKey: true }); });
        await act(async () => { fireEvent.keyDown(open, { key: "y", isComposing: true }); });
        expect(onAnswer).not.toHaveBeenCalled();
        expect(open).toHaveAttribute("role", "article");
        expect(open.getAttribute("aria-label")).toContain("Noticed a");
        expect(screen.getByRole("button", { name: "Yes" })).toBeInTheDocument();
    });

    it("says that's everything when nothing is open", () => {
        setup([]);
        expect(screen.getByTestId("your-work-empty")).toHaveTextContent("That's everything for now.");
    });

    it("words times in the reader's day", () => {
        const tr = (k: string, o?: Record<string, unknown>) => i18n.t(`today:${k}`, o as never) as unknown as string;
        expect(workWhen(new Date(2026, 10, 16, 10).toISOString(), NOW, "en", tr)).toMatch(/^tomorrow 10:00/);
    });
});

describe("Your work's layout (computer-window v1.0.0 rules 5 and 8)", () => {
    afterEach(cleanup);
    it("goes two columns at 880 of content: 480-640 and 320-372, 24 between", () => {
        render(<TodayYourWork list={list([item("a", 0), item("b", 1)])} active onAnswer={vi.fn()} onUndo={vi.fn()} now={NOW} />);
        expect(screen.getByTestId("your-work-columns")).toHaveClass("gap-6", "@min-[880px]:grid-cols-[minmax(480px,640px)_minmax(320px,372px)]");
    });
    it("leaves its keys line to Today's strip when asked, and the strip drops Quick chat below 880", () => {
        const { rerender } = render(<TodayYourWork keys={false} list={list([item("a", 0)])} active onAnswer={vi.fn()} onUndo={vi.fn()} now={NOW} />);
        expect(screen.queryByTestId("your-work-keys")).not.toBeInTheDocument();
        rerender(<TodayKeysLine />);
        expect(screen.getByTestId("your-work-keys-quick-chat")).toHaveClass("hidden", "@min-[880px]:inline");
    });
});

describe("Your work's states (computer-today v1.1.0 rules 3, 5, 12, 13)", () => {
    beforeEach(async () => { await i18n.changeLanguage("en"); });
    afterEach(cleanup);
    const quiet = (id: string) => item(id, 5, { state: "with_agent", agent: { name: "Codex", since: new Date(NOW.getTime() - 3 * 60 * 60_000).toISOString() } });

    it("offers Ask again and Take it back, in amber, after two hours with no word", async () => {
        const { onAnswer } = setup([item("a", 0), quiet("q")]);
        const row = screen.getByTestId("your-work-no-word");
        expect(row.querySelector("p")).toHaveClass("text-warning");
        await act(async () => { fireEvent.click(screen.getByTestId("your-work-ask-again")); });
        expect(onAnswer).toHaveBeenLastCalledWith("q", { action: "ask_again" });
        expect(screen.queryByTestId("your-work-undo-line")).not.toBeInTheDocument();
        await act(async () => { fireEvent.click(screen.getByTestId("your-work-take-back")); });
        expect(onAnswer).toHaveBeenLastCalledWith("q", { action: "take_back" });
    });

    it("says what Take it back can't undo, and shows the taken-back work as offered again", async () => {
        const taken = { ...item("q", 0), takenBack: { agentName: "Codex", at: new Date(NOW.getTime() - 60_000).toISOString(), undoUntil: new Date(NOW.getTime() + 60_000).toISOString() } } as unknown as WorkItem;
        const { onAnswer, rerender } = setup([quiet("q")]);
        await act(async () => { fireEvent.click(screen.getByTestId("your-work-take-back")); });
        expect(onAnswer).toHaveBeenCalledWith("q", { action: "take_back" });
        rerender([taken]);
        expect(screen.getByTestId("your-work-undo-line")).toHaveTextContent("Taken back. Codex can't open it any more; anything it already made stays with Codex.");
        expect(screen.getByTestId("your-work-taken-back")).toHaveTextContent(/^Taken back from Codex at .+\. Hand it to someone else, or keep it for later\.$/);
    });

    it("asks a reason for Not useful on a result, when the result has reasons, and nothing is required", async () => {
        const result = item("r", 0, { state: "back", kind: "result", row: "result", reasons: ["wrong_facts", "missed_point", "myself"], agent: { name: "Codex" } });
        const { onAnswer } = setup([result]);
        fireEvent.keyDown(screen.getByTestId("your-work-open"), { key: "n" });
        const reasons = screen.getByTestId("your-work-reasons");
        expect(reasons).toHaveTextContent("Missed the point");
        await act(async () => { fireEvent.click(screen.getByRole("button", { name: "Missed the point" })); });
        expect(onAnswer).toHaveBeenCalledWith("r", { action: "not_useful", reason: "missed_point" });
    });

    it("judges a result with no reasons in one key", async () => {
        const result = item("r", 0, { state: "back", kind: "result", row: "result", reasons: [], agent: { name: "Codex" } });
        const { onAnswer } = setup([result]);
        await act(async () => { fireEvent.keyDown(screen.getByTestId("your-work-open"), { key: "n" }); });
        expect(onAnswer).toHaveBeenCalledWith("r", { action: "not_useful" });
    });

    it("counts day one's work as day one", () => {
        setup([item("d1", 0, { source: "day_one" }), item("d2", 1, { source: "day_one" })]);
        expect(screen.getByTestId("your-work-counts")).toHaveTextContent("2 to answer · day one");
    });

    it("links Connect an agent › when none is connected", () => {
        const onConnectAgent = vi.fn();
        render(<TodayYourWork list={list([item("a", 0)])} active noAgents onConnectAgent={onConnectAgent} onAnswer={vi.fn()} onUndo={vi.fn()} now={NOW} />);
        fireEvent.click(screen.getByRole("button", { name: "Connect an agent ›" }));
        expect(onConnectAgent).toHaveBeenCalled();
    });
});


it("says the agents couldn't be read in the Your agents card, with Try again, instead of leaving it out", () => {
    const onRetryAgents = vi.fn();
    render(<TodayYourWork list={list([item("a", 0)])} active agentsFailed onRetryAgents={onRetryAgents} onAnswer={vi.fn()} onUndo={vi.fn()} now={NOW} />);
    const card = screen.getByTestId("your-work-agents-failed");
    expect(card).toHaveAttribute("role", "alert");
    expect(card).toHaveTextContent("Couldn't read your agents.");
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(onRetryAgents).toHaveBeenCalled();
    cleanup();
});

describe("opening an item asked for from elsewhere (Quick chat's Open)", () => {
    beforeEach(async () => { await i18n.changeLanguage("en"); });
    afterEach(cleanup);
    it("makes an open item the open item with focus, and asks again on a new nonce", async () => {
        const items = [item("a", 0), item("b", 1), item("c", 2)];
        const { rerender } = render(<TodayYourWork list={list(items)} active onAnswer={vi.fn()} onUndo={vi.fn()} now={NOW} focusRequest={{ itemId: "c", nonce: 1 }} />);
        await act(async () => { await new Promise((resolve) => requestAnimationFrame(() => resolve(null))); });
        const open = screen.getByTestId("your-work-open");
        expect(open).toHaveAttribute("data-item-id", "c");
        expect(document.activeElement).toBe(open);
        rerender(<TodayYourWork list={list(items)} active onAnswer={vi.fn()} onUndo={vi.fn()} now={NOW} focusRequest={{ itemId: "a", nonce: 2 }} />);
        expect(screen.getByTestId("your-work-open")).toHaveAttribute("data-item-id", "a");
    });
    it("brings an answered item into view in Done today", async () => {
        const done = item("d", 1, { state: "answered", answer: { action: "yes", at: NOW.toISOString(), surface: "computer", line: "Work d · in your Mail drafts" } });
        render(<TodayYourWork list={list([item("a", 0), done])} active onAnswer={vi.fn()} onUndo={vi.fn()} now={NOW} focusRequest={{ itemId: "d", nonce: 1 }} />);
        await act(async () => {});
        expect(document.activeElement).toHaveAttribute("data-item-id", "d");
        expect(screen.getByTestId("your-work-open")).toHaveAttribute("data-item-id", "a");
    });
});
