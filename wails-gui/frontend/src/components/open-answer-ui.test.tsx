import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import i18n from "@/i18n";
import type { OpenAnswer } from "@/lib/open-answers";
import { parseOpenAnswer } from "@/lib/open-answers";
import {
    beginOpenAnswerDelivery,
    refreshOpenAnswers,
    registerDeliveredOpenAnswers,
} from "@/hooks/useOpenAnswers";
import {
    listMainChatTopicItemsWithOpenAnswerDelivery,
    loadStreamItemsPageWithOpenAnswerDelivery,
} from "@/lib/open-answer-delivery";
import type { StreamItem } from "@/types";
import { AnswerReadyCard, OpenAnswerPromiseLine } from "./open-answer-ui";

const bindings = vi.hoisted(() => ({
    list: vi.fn(), streamLoad: vi.fn(), topicLoad: vi.fn(), change: vi.fn(), cancel: vi.fn(), selectAsk: vi.fn(), notForMe: vi.fn(), undo: vi.fn(), send: vi.fn(),
}));
const listeners = vi.hoisted(() => new Map<string, Set<(value: unknown) => void>>());
vi.mock("@/lib/wails-shim-app", () => ({
    ListOpenAnswers: bindings.list, ChangeOpenAnswer: bindings.change, CancelOpenAnswer: bindings.cancel,
    SelectOpenAnswerAsk: bindings.selectAsk, NotForMeAnswer: bindings.notForMe, UndoNotForMeAnswer: bindings.undo, SendMainChat: bindings.send,
    LoadStreamItemsPage: bindings.streamLoad,
    ListMainChatTopicItems: bindings.topicLoad,
}));
vi.mock("../../wailsjs/runtime/runtime", () => ({
    EventsOn: vi.fn((name, callback) => {
        const handlers = listeners.get(name) ?? new Set();
        handlers.add(callback);
        listeners.set(name, handlers);
        return () => handlers.delete(callback);
    }),
}));

const answer = (overrides: Partial<OpenAnswer> = {}): OpenAnswer => ({
    id: "answer-1", sessionId: "session-1", messageId: "message-1", question: "Will it rain?",
    kind: "weather", settles: "result", delivery: "around", windowStart: "2026-10-27T10:00:00-07:00",
    expiresAt: "2026-11-01T00:00:00Z", posts: 0, state: "open", createdAt: "2026-10-26T09:00:00-07:00",
    ...overrides,
});

function deferred<T>() {
    let resolve!: (value: T) => void;
    const promise = new Promise<T>((done) => { resolve = done; });
    return { promise, resolve };
}

async function emit(event: string, payload?: unknown) {
    await act(async () => {
        await Promise.all([...(listeners.get(event) ?? [])].map((handler) => handler(payload)));
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

function deliverMessageAnswer(record: OpenAnswer) {
    const startedGeneration = beginOpenAnswerDelivery();
    registerDeliveredOpenAnswers([record], startedGeneration);
}

function renderPromise(record: OpenAnswer, onSend?: (sessionId: string, message: string) => Promise<void>) {
    deliverMessageAnswer(record);
    return render(<OpenAnswerPromiseLine answer={record} onSend={onSend} />);
}

async function deliverThroughStream(record: OpenAnswer) {
    bindings.streamLoad.mockResolvedValue({ items: [{ openAnswer: record }] });
    await act(async () => {
        await loadStreamItemsPageWithOpenAnswerDelivery(record.sessionId, -1, 100);
    });
}

beforeEach(async () => {
    vi.clearAllMocks();
    await i18n.changeLanguage("en");
    bindings.list.mockResolvedValue([]);
    bindings.streamLoad.mockResolvedValue({ items: [] });
    bindings.topicLoad.mockResolvedValue({ items: [] });
    bindings.change.mockResolvedValue(null);
    bindings.cancel.mockResolvedValue(null);
    bindings.selectAsk.mockResolvedValue(undefined);
    bindings.notForMe.mockResolvedValue(undefined);
    bindings.undo.mockResolvedValue(undefined);
    bindings.send.mockResolvedValue(undefined);
    await refreshOpenAnswers();
});

describe("open-answer promise line", () => {
    it.each([
        [answer(), "I'll let you know when it's settled, around 10:00 AM.", "有结果我告诉你，大约10:00。"],
        [answer({ settles: "status" }), "I'll check again around 10:00 AM.", "我大约10:00再查一次。"],
        [answer({ settles: "reply", watchedEvent: { who: "Mina", label: "reply" } }), "Not yet. I'll let you know when Mina replies.", "还没有。Mina 回复后我告诉你。"],
        [answer({ delivery: "settled_only" }), "I'll let you know when it's settled.", "有定论再告诉你。"],
        [answer({ delivery: "brief" }), "I'll put it in tomorrow's brief.", "我会放进明天的简报。"],
        [answer({ state: "saw_it", sawAt: "2026-10-26T10:00:00-07:00" }), "You saw it at 10:00 AM.", "你在10:00已经看到了。"],
        [answer({ state: "gave_up", watchedEvent: { who: "Mina", label: "reply" } }), "Mina hasn't replied in a week, so I've stopped checking.", "Mina 一周都没回复，我先不查了。"],
        [answer({ state: "gave_up" }), "I've stopped checking.", "我先不查了。"],
    ])("renders exact English and Chinese promise copy", async (record, en, zh) => {
        const { rerender } = renderPromise(record);
        expect(await screen.findByText(en)).toBeInTheDocument();
        await i18n.changeLanguage("zh-CN");
        rerender(<OpenAnswerPromiseLine answer={record} />);
        const line = screen.getByTestId("open-answer-promise").querySelector("span");
        expect(line).toHaveTextContent(zh);
    });

    it.each([
        ["around", "Around 10:00 AM"],
        ["settled_only", "Only when it's settled"],
        ["brief", "In tomorrow's brief"],
    ] as const)("changes delivery to %s", async (delivery, label) => {
        const user = userEvent.setup();
        const record = answer();
        bindings.change.mockResolvedValue({ ...record, delivery });
        renderPromise(record);
        await user.click(screen.getByRole("button", { name: "Change" }));
        await user.click(await screen.findByRole("menuitem", { name: label }));
        await waitFor(() => expect(bindings.change).toHaveBeenCalledWith(record.id, delivery));
    });

    it("offers delivery changes, omits Around without a window, and cancels No need", async () => {
        const user = userEvent.setup();
        const record = answer({ windowStart: undefined });
        renderPromise(record);
        await user.click(screen.getByRole("button", { name: "Change" }));
        expect(await screen.findByText("Only when it's settled")).toBeInTheDocument();
        expect(screen.queryByText(/Around/)).toBeNull();
        await user.click(screen.getByRole("menuitem", { name: "Only when it's settled" }));
        await waitFor(() => expect(bindings.change).toHaveBeenCalledWith(record.id, "settled_only"));
        await user.click(screen.getByRole("button", { name: "No need" }));
        await waitFor(() => expect(bindings.cancel).toHaveBeenCalledWith(record.id));
    });

    it("keeps the line unchanged on errors and makes delivery writes single-flight", async () => {
        const user = userEvent.setup();
        const record = answer();
        let rejectChange!: (error: Error) => void;
        bindings.change.mockImplementation(() => new Promise((_, reject) => { rejectChange = reject; }));
        renderPromise(record);
        await user.click(screen.getByRole("button", { name: "Change" }));
        await user.click(await screen.findByRole("menuitem", { name: "Only when it's settled" }));
        expect(bindings.change).toHaveBeenCalledTimes(1);
        rejectChange(new Error("write failed"));
        expect(await screen.findByRole("alert")).toBeInTheDocument();
        expect(screen.getByText("I'll let you know when it's settled, around 10:00 AM.")).toBeInTheDocument();
    });

    it("makes No need single-flight", async () => {
        let resolveCancel!: (value: null) => void;
        bindings.cancel.mockImplementation(() => new Promise((resolve) => { resolveCancel = resolve; }));
        renderPromise(answer());
        const noNeed = screen.getByRole("button", { name: "No need" });
        fireEvent.click(noNeed);
        fireEvent.click(noNeed);
        expect(bindings.cancel).toHaveBeenCalledTimes(1);
        await act(async () => { resolveCancel(null); });
    });

    it("refreshes its shared record when open-answers:changed fires", async () => {
        const record = answer();
        bindings.list.mockResolvedValue([{ ...record, delivery: "brief" }]);
        renderPromise(record);
        await waitFor(() => expect(listeners.has("open-answers:changed")).toBe(true));
        listeners.get("open-answers:changed")?.forEach((handler) => handler({}));
        expect(await screen.findByText("I'll put it in tomorrow's brief.")).toBeInTheDocument();
    });

    it("does not render settled or cancelled records", () => {
        const cancelled = answer({ state: "cancelled" });
        const { rerender } = renderPromise(cancelled);
        expect(screen.queryByTestId("open-answer-promise")).toBeNull();
        const settled = answer({ state: "settled" });
        deliverMessageAnswer(settled);
        rerender(<OpenAnswerPromiseLine answer={settled} />);
        expect(screen.queryByTestId("open-answer-promise")).toBeNull();
    });

    it("sends the localized ask once through its session callback", async () => {
        const user = userEvent.setup();
        const onSend = vi.fn(async () => {});
        const ask = answer({ state: "ask" });
        const { rerender } = renderPromise(ask, onSend);
        await user.click(screen.getByRole("button", { name: "Let me know" }));
        await waitFor(() => expect(onSend).toHaveBeenCalledTimes(1));
        expect(onSend).toHaveBeenCalledWith("session-1", "Let me know when it's settled.");
        await i18n.changeLanguage("zh-CN");
        const nextAsk = answer({ state: "ask" });
        deliverMessageAnswer(nextAsk);
        rerender(<OpenAnswerPromiseLine answer={nextAsk} onSend={onSend} />);
        await user.click(screen.getByRole("button", { name: "告诉我" }));
        await waitFor(() => expect(onSend).toHaveBeenCalledTimes(2));
        expect(onSend).toHaveBeenLastCalledWith("session-1", "有结果时告诉我。");
    });

    it("selects the tapped ask before sending its request", async () => {
        const user = userEvent.setup();
        const onSend = vi.fn(async () => {});
        const older = answer({ id: "older-ask", messageId: "older-message", state: "ask" });
        const newer = answer({ id: "newer-ask", messageId: "newer-message", state: "ask" });
        bindings.list.mockResolvedValue([older, newer]);
        await act(async () => { await refreshOpenAnswers(); });
        render(
            <>
                <OpenAnswerPromiseLine answer={older} onSend={onSend} />
                <OpenAnswerPromiseLine answer={newer} onSend={onSend} />
            </>,
        );

        await user.click(screen.getAllByRole("button", { name: "Let me know" })[0]);
        await waitFor(() => expect(onSend).toHaveBeenCalledTimes(1));
        expect(bindings.selectAsk).toHaveBeenCalledWith(older.id);
        expect(bindings.selectAsk.mock.invocationCallOrder[0]).toBeLessThan(onSend.mock.invocationCallOrder[0]);
    });

    it("does not send when selecting the ask fails", async () => {
        const user = userEvent.setup();
        const onSend = vi.fn(async () => {});
        const ask = answer({ state: "ask" });
        bindings.selectAsk.mockRejectedValueOnce(new Error("selection failed"));
        renderPromise(ask, onSend);

        await user.click(screen.getByRole("button", { name: "Let me know" }));

        expect(await screen.findByRole("alert")).toBeInTheDocument();
        expect(onSend).not.toHaveBeenCalled();
    });

    it.each(["memory:withdrawn", "sensitive-apps:updated", "closed-eyes:changed", "permissions:changed", "background-listening:changed", "model-accounts:updated", "session:deleted"])("clears the promise line synchronously on %s", async (event) => {
        const record = answer();
        bindings.list.mockResolvedValue([record]);
        await act(async () => { await refreshOpenAnswers(); });
        renderPromise(record);
        expect(screen.getByTestId("open-answer-promise")).toBeInTheDocument();

        const reread = deferred<OpenAnswer[]>();
        bindings.list.mockImplementation(() => reread.promise);
        await emitWithdrawal(event);
        expect(screen.queryByTestId("open-answer-promise")).toBeNull();
        await act(async () => { reread.resolve([]); await reread.promise; });
    });

    it("shows a newly delivered ask after withdrawal and sends it once", async () => {
        const before = answer();
        const after = answer({ id: "answer-2", messageId: "message-2", state: "ask" });
        const send = deferred<void>();
        const onSend = vi.fn(() => send.promise);
        const { rerender } = renderPromise(before, onSend);
        await emit("memory:withdrawn");
        rerender(<OpenAnswerPromiseLine answer={after} onSend={onSend} />);
        expect(screen.queryByTestId("open-answer-ask")).toBeNull();
        await deliverThroughStream(after);
        const ask = screen.getByRole("button", { name: "Let me know" });
        expect(ask).toBeInTheDocument();
        fireEvent.click(ask);
        fireEvent.click(ask);
        await waitFor(() => expect(onSend).toHaveBeenCalledTimes(1));
        expect(bindings.selectAsk).toHaveBeenCalledTimes(1);
        expect(bindings.selectAsk).toHaveBeenCalledWith(after.id);
        expect(onSend).toHaveBeenCalledTimes(1);
        expect(onSend).toHaveBeenCalledWith(after.sessionId, "Let me know when it's settled.");
        await act(async () => { send.resolve(); await send.promise; });
        expect(onSend).toHaveBeenCalledTimes(1);
    });

    it("registers topic-page stream records only when delivered after withdrawal", async () => {
        const record = answer({ settles: "reply", watchedEvent: { who: "Mina", label: "reply" } });
        const { rerender } = renderPromise(answer());
        await emit("memory:withdrawn");
        rerender(<OpenAnswerPromiseLine answer={record} />);
        expect(screen.queryByTestId("open-answer-promise")).toBeNull();
        bindings.topicLoad.mockResolvedValue({ items: [{ message: { openAnswer: record } }] });
        await act(async () => {
            await listMainChatTopicItemsWithOpenAnswerDelivery("topic-1", "", 50);
        });
        expect(screen.getByText("Not yet. I'll let you know when Mina replies.")).toBeInTheDocument();
    });

    it("shows a new message-backed open answer and its actions after withdrawal", async () => {
        const user = userEvent.setup();
        const before = answer();
        const after = answer({ id: "answer-2", messageId: "message-2" });
        const { rerender } = renderPromise(before);
        await emit("memory:withdrawn");
        rerender(<OpenAnswerPromiseLine answer={after} />);
        expect(screen.queryByTestId("open-answer-promise")).toBeNull();
        await deliverThroughStream(after);
        expect(screen.getByTestId("open-answer-promise")).toBeInTheDocument();
        expect(screen.getByRole("button", { name: "No need" })).toBeInTheDocument();
        await user.click(screen.getByRole("button", { name: "Change" }));
        await user.click(await screen.findByRole("menuitem", { name: "Only when it's settled" }));
        await waitFor(() => expect(bindings.change).toHaveBeenCalledWith(after.id, "settled_only"));
        await user.click(screen.getByRole("button", { name: "No need" }));
        await waitFor(() => expect(bindings.cancel).toHaveBeenCalledWith(after.id));
    });

    it("does not revive the same message object after withdrawal and remount", async () => {
        const record = answer({ settles: "reply", watchedEvent: { who: "Mina", label: "reply" } });
        const { unmount } = renderPromise(record);
        expect(screen.getByTestId("open-answer-promise")).toBeInTheDocument();
        await emit("memory:withdrawn");
        unmount();
        render(<OpenAnswerPromiseLine answer={record} />);
        expect(screen.queryByTestId("open-answer-promise")).toBeNull();
        expect(screen.queryByText(/Mina/)).toBeNull();
    });

    it("does not authorize reparsed stale message data after withdrawal", async () => {
        const record = answer({ settles: "reply", watchedEvent: { who: "Mina", label: "reply" } });
        const { unmount } = renderPromise(record);
        await emit("memory:withdrawn");
        unmount();
        const reparsed = { ...record, watchedEvent: { ...record.watchedEvent! } };
        render(<OpenAnswerPromiseLine answer={reparsed} />);
        expect(screen.queryByTestId("open-answer-promise")).toBeNull();
        expect(screen.queryByText(/Mina/)).toBeNull();
    });

    it("does not authorize a stream reply that resolves after withdrawal", async () => {
        const record = answer({ settles: "reply", watchedEvent: { who: "Mina", label: "reply" } });
        const response = deferred<{ items: Array<{ openAnswer: OpenAnswer }> }>();
        bindings.streamLoad.mockReturnValue(response.promise);
        const pending = loadStreamItemsPageWithOpenAnswerDelivery(record.sessionId, -1, 100);
        await waitFor(() => expect(bindings.streamLoad).toHaveBeenCalledTimes(1));
        await emit("memory:withdrawn");
        await act(async () => {
            response.resolve({ items: [{ openAnswer: record }] });
            await pending;
        });
        render(<OpenAnswerPromiseLine answer={record} />);
        expect(screen.queryByTestId("open-answer-promise")).toBeNull();
        expect(screen.queryByText(/Mina/)).toBeNull();
    });

    it.each([
        [answer({ state: "saw_it", sawAt: "2026-10-26T10:00:00-07:00" }), "You saw it at 10:00 AM."],
        [answer({ state: "gave_up", watchedEvent: { who: "Mina", label: "reply" } }), "Mina hasn't replied in a week, so I've stopped checking."],
        [answer({ state: "ask" }), "ask"],
        [answer({ state: "cancelled" }), "cancelled"],
    ])("renders a fresh message-backed %s state after withdrawal", async (record, expected) => {
        const { rerender } = renderPromise(answer());
        await emit("memory:withdrawn");
        await deliverThroughStream(record);
        rerender(<OpenAnswerPromiseLine answer={record} onSend={vi.fn(async () => {})} />);
        if (expected === "ask") expect(screen.getByTestId("open-answer-ask")).toBeInTheDocument();
        else if (expected === "cancelled") expect(screen.queryByTestId("open-answer-promise")).toBeNull();
        else expect(screen.getByText(expected)).toBeInTheDocument();
    });

    it.each([
        [answer({ state: "saw_it", sawAt: "2026-10-26T10:00:00-07:00" }), "You saw it at 10:00 AM."],
        [answer({ state: "gave_up", watchedEvent: { who: "Mina", label: "reply" } }), "Mina hasn't replied in a week, so I've stopped checking."],
        [answer({ state: "ask" }), "ask"],
        [answer({ state: "cancelled" }), "cancelled"],
    ])("renders a list-backed post-withdrawal state %s", async (record, expected) => {
        bindings.list.mockResolvedValue([]);
        renderPromise(answer());
        await waitFor(() => expect(bindings.list).toHaveBeenCalled());
        bindings.list.mockResolvedValue([record]);
        await emit("memory:withdrawn");
        if (expected === "ask") expect(await screen.findByTestId("open-answer-ask")).toBeInTheDocument();
        else if (expected === "cancelled") await waitFor(() => expect(screen.queryByTestId("open-answer-promise")).toBeNull());
        else expect(await screen.findByText(expected)).toBeInTheDocument();
    });

    it("does not revive a record from a list reply started before privacy withdrawal", async () => {
        const record = answer();
        bindings.list.mockResolvedValue([record]);
        await act(async () => { await refreshOpenAnswers(); });
        renderPromise(record);
        expect(screen.getByTestId("open-answer-promise")).toBeInTheDocument();

        const stale = deferred<OpenAnswer[]>();
        const fresh = deferred<OpenAnswer[]>();
        bindings.list.mockReset().mockReturnValueOnce(stale.promise).mockReturnValueOnce(fresh.promise);
        const pending = refreshOpenAnswers();
        await waitFor(() => expect(bindings.list).toHaveBeenCalledTimes(1));
        await emit("memory:withdrawn");
        await waitFor(() => expect(bindings.list).toHaveBeenCalledTimes(2));
        await act(async () => { stale.resolve([record]); await pending; });
        expect(screen.queryByTestId("open-answer-promise")).toBeNull();
        await act(async () => { fresh.resolve([]); await fresh.promise; });
    });

    it("does not publish a Change reply received after privacy withdrawal", async () => {
        const record = answer();
        const updated = answer({ delivery: "brief" });
        bindings.list.mockResolvedValue([record]);
        await act(async () => { await refreshOpenAnswers(); });
        const user = userEvent.setup();
        renderPromise(record);
        let resolveChange!: (value: OpenAnswer) => void;
        bindings.change.mockImplementation(() => new Promise((resolve) => { resolveChange = resolve; }));
        await user.click(screen.getByRole("button", { name: "Change" }));
        await user.click(await screen.findByRole("menuitem", { name: "In tomorrow's brief" }));
        const reread = deferred<OpenAnswer[]>();
        bindings.list.mockReset().mockReturnValueOnce(reread.promise);
        await emit("memory:withdrawn");
        expect(screen.queryByTestId("open-answer-promise")).toBeNull();
        await act(async () => { resolveChange(updated); });
        expect(screen.queryByTestId("open-answer-promise")).toBeNull();
        await act(async () => { reread.resolve([]); await reread.promise; });
    });

    it("does not start a post-Change list refresh with a pre-withdrawal generation", async () => {
        const record = answer();
        bindings.list.mockResolvedValue([record]);
        await act(async () => { await refreshOpenAnswers(); });
        const user = userEvent.setup();
        renderPromise(record);
        let resolveChange!: (value: null) => void;
        bindings.change.mockImplementation(() => new Promise((resolve) => { resolveChange = resolve; }));
        await user.click(screen.getByRole("button", { name: "Change" }));
        await user.click(await screen.findByRole("menuitem", { name: "In tomorrow's brief" }));
        const reread = deferred<OpenAnswer[]>();
        bindings.list.mockReset().mockReturnValueOnce(reread.promise);
        await emit("memory:withdrawn");
        await act(async () => { resolveChange(null); });
        expect(bindings.list).toHaveBeenCalledTimes(1);
        expect(screen.queryByTestId("open-answer-promise")).toBeNull();
        await act(async () => { reread.resolve([]); await reread.promise; });
        await waitFor(() => expect(bindings.list).toHaveBeenCalledTimes(1));
    });

    it("reruns a list that started before Change instead of overwriting its record", async () => {
        const record = answer();
        const updated = answer({ delivery: "brief" });
        bindings.list.mockResolvedValue([record]);
        await act(async () => { await refreshOpenAnswers(); });
        const user = userEvent.setup();
        renderPromise(record);
        const stale = deferred<OpenAnswer[]>();
        bindings.list.mockReset().mockReturnValueOnce(stale.promise).mockResolvedValue([updated]);
        const pending = refreshOpenAnswers();
        await waitFor(() => expect(bindings.list).toHaveBeenCalledTimes(1));
        bindings.change.mockResolvedValue(updated);
        await user.click(screen.getByRole("button", { name: "Change" }));
        await user.click(await screen.findByRole("menuitem", { name: "In tomorrow's brief" }));
        expect(await screen.findByText("I'll put it in tomorrow's brief.")).toBeInTheDocument();
        await act(async () => { stale.resolve([record]); await pending; });
        await waitFor(() => expect(bindings.list).toHaveBeenCalledTimes(2));
        expect(await screen.findByText("I'll put it in tomorrow's brief.")).toBeInTheDocument();
    });

    it("drops malformed open-answer payloads", () => {
        expect(parseOpenAnswer("{bad")).toBeUndefined();
        expect(parseOpenAnswer({ ...answer(), delivery: "later" })).toBeUndefined();
    });
});

describe("answer_ready card", () => {
    it("starts in the learned state when the backend marks the kind learned", () => {
        const record = answer({ learned: true, topic: "weather" });
        const item = { id: "message-1", kind: "assistant", cardType: "answer_ready", openAnswer: record, content: "It will be sunny." } as StreamItem;
        render(<AnswerReadyCard item={item} />);
        expect(screen.getByText(/stop offering these on weather/i)).toBeInTheDocument();
        expect(screen.getByRole("button", { name: "Undo" })).toBeInTheDocument();
        expect(within(screen.getByTestId("answer-ready-card")).getAllByRole("button")).toHaveLength(2);
    });

    it("shows question, source and answer, then Not for me and Undo", async () => {
        const record = answer({ source: "Weather service", topic: "weather" });
        const item = { id: "message-1", kind: "assistant", cardType: "answer_ready", openAnswer: record, content: "It will be sunny.", createdAt: Date.parse("2026-10-26T12:00:00-07:00") } as StreamItem;
        render(<AnswerReadyCard item={item} />);
        expect(screen.getByText("You asked · 9:00 AM")).toBeInTheDocument();
        expect(screen.getByText(/Will it rain/)).toBeInTheDocument();
        expect(screen.getByText("From Weather service · 12:00 PM")).toBeInTheDocument();
        expect(screen.getByText("It will be sunny.")).toBeInTheDocument();
        expect(within(screen.getByTestId("answer-ready-card")).getAllByRole("button")).toHaveLength(1);
        const menu = screen.getByRole("button", { name: /more/i });
        const user = userEvent.setup();
        await user.click(menu);
        await user.click(await screen.findByRole("menuitem", { name: "Not for me" }));
        await waitFor(() => expect(bindings.notForMe).toHaveBeenCalledWith(item.id));
        expect(await screen.findByText(/stop offering these on weather/i)).toBeInTheDocument();
        await user.click(screen.getByRole("button", { name: "Undo" }));
        await waitFor(() => expect(bindings.undo).toHaveBeenCalledWith(record.kind));
        expect(within(screen.getByTestId("answer-ready-card")).queryByRole("button", { name: /let me know/i })).toBeNull();
        await i18n.changeLanguage("zh-CN");
        expect(screen.getByText("你问过 · 9:00")).toBeInTheDocument();
        expect(screen.getByText("来自Weather service · 12:00")).toBeInTheDocument();
    });
});
