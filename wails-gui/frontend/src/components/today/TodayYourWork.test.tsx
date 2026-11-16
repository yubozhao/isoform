import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import i18n from "@/i18n";
import type { WorkItem, WorkList } from "@/lib/wails-shim-app";
import { TodayYourWork, workWhen } from "./TodayYourWork";

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
        setup([item("a", 0), item("b", 1), item("c", 2, { state: "with_agent", agent: { name: "Codex", since: new Date(2026, 10, 15, 16, 10).toISOString() } })]);
        const open = screen.getByTestId("your-work-open");
        expect(open).toHaveTextContent("For your 10:00 with Lena · from the brief");
        expect(open).toHaveTextContent("Noticed a");
        expect(screen.getByTestId("your-work-yes-line")).toHaveTextContent("Yes puts it in your Mail drafts. You send it.");
        expect(screen.getByTestId("your-work-move")).toHaveTextContent("1 of 2 · ↑↓ to move");
        expect(screen.getByTestId("your-work-up-next")).toHaveTextContent("Work b");
        expect(screen.getByTestId("your-work-with-agents")).toHaveTextContent(/Work c · Codex · since 4:10/);
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
        const { onAnswer } = setup([item("r", 0, { state: "back", kind: "result", row: "result" })]);
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

    it("words a first-line part from its time when it has one", () => {
        setup([item("a", 0, { first: [{ text: "For your meeting" }, { text: "tomorrow", at: new Date(2026, 10, 16, 10).toISOString() }] })]);
        expect(screen.getByTestId("your-work-first")).toHaveTextContent(/For your meeting · tomorrow 10:00/);
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
