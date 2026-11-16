import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { TaskSuggestionDeck } from "./TaskSuggestionDeck";
import { resetPagePrivacyWithdrawal } from "@/lib/page-privacy-withdrawal";
import i18n from "@/i18n";
import { clearPendingTaskStarts } from "./pending-task-starts";

const mocks = vi.hoisted(() => ({
    list: vi.fn(), prepare: vi.fn(), decide: vi.fn(), start: vi.fn(),
    handlers: new Map<string, Set<() => void>>(),
}));
vi.mock("@/lib/wails-shim-app", () => ({
    ListTaskSuggestions: mocks.list, PrepareTaskSuggestion: mocks.prepare, DecideTaskSuggestion: mocks.decide, StartDesktopTaskOffer: mocks.start,
}));
vi.mock("../../../wailsjs/runtime/runtime", () => ({ EventsOn: (name: string, callback: () => void) => {
    const listeners = mocks.handlers.get(name) ?? new Set();
    listeners.add(callback);
    mocks.handlers.set(name, listeners);
    return () => listeners.delete(callback);
} }));
const offer = { id: "A", version: "1", title: "Private offer A", why: "Private rationale A", prompt: "Private prompt A", kind: "suggestion", evidence: { text: "Private rationale A", source: "mail", at: "2026-10-30T10:00:00Z" } };
const draft = { prompt: offer.prompt, reference: { id: offer.id, version: offer.version } };
function emit(name: string) { act(() => mocks.handlers.get(name)?.forEach((callback) => callback())); }
afterEach(() => { clearPendingTaskStarts(); vi.useRealTimers(); });
beforeEach(async () => {
    resetPagePrivacyWithdrawal();
    mocks.handlers.clear();
    vi.clearAllMocks();
    await i18n.changeLanguage("en");
    mocks.list.mockResolvedValueOnce([offer]).mockResolvedValue([]);
    mocks.prepare.mockResolvedValue(draft);
    mocks.start.mockResolvedValue({});
});
it.each([
    "memory:withdrawn", "closed-eyes:changed", "background-listening:changed", "permissions:changed",
    "sensitive-apps:updated", "model-accounts:updated", "topics:withdrawn",
])("withdraws every rendered offer immediately on %s", async (event) => {
    render(<TaskSuggestionDeck />);
    await screen.findByText(offer.title);
    emit(event);
    expect(screen.queryByText(offer.title)).toBeNull();
    expect(screen.queryByText(offer.why)).toBeNull();
    expect(document.body.textContent).not.toContain(offer.prompt);
    await waitFor(() => expect(mocks.list).toHaveBeenCalledTimes(2));
});
it("fences a read begun before withdrawal and restores only surviving offers", async () => {
    let finish!: (value: unknown) => void;
    mocks.list.mockReset().mockImplementationOnce(() => new Promise((resolve) => { finish = resolve; }))
        .mockResolvedValue([{ ...offer, id: "B", title: "Surviving B", why: "Still readable" }]);
    render(<TaskSuggestionDeck />);
    emit("memory:withdrawn");
    await screen.findByText("Surviving B");
    await act(async () => finish([offer]));
    expect(screen.queryByText(offer.title)).toBeNull();
    expect(screen.getByText("Surviving B")).toBeInTheDocument();
});
it("withdrawal cancels a pending start before dispatch", async () => {
    render(<TaskSuggestionDeck />);
    await screen.findByText(offer.title);
    vi.useFakeTimers();
    fireEvent.click(screen.getByRole("button", { name: "Do it" }));
    emit("closed-eyes:changed");
    await act(async () => vi.advanceTimersByTimeAsync(5000));
    expect(mocks.start).not.toHaveBeenCalled();
    expect(screen.queryByText(offer.title)).toBeNull();
});

it("starts only after the undo window", async () => {
    render(<TaskSuggestionDeck />);
    await screen.findByText(offer.title);
    vi.useFakeTimers();
    fireEvent.click(screen.getByRole("button", { name: "Do it" }));
    await act(async () => vi.advanceTimersByTimeAsync(4999));
    expect(mocks.start).not.toHaveBeenCalled();
    await act(async () => vi.advanceTimersByTimeAsync(1));
    expect(mocks.start).toHaveBeenCalledWith("A", "1");
});

it.each([
    "knowledge:updated", "activity:activities-updated", "activity:threads-updated", "activity:timeline-groups-updated",
    "activity:session-saved", "activity:status", "activity:summary-ready", "activity:moments-ready", "activity:summary-error",
    "meeting-notes:saved", "insight:saved", "cron:state", "app:ready", "ai:config-changed",
    "topics:finds", "topics:updated", "initiative:updated",
])("debounces %s and keeps surviving cards mounted during and after reload", async (event) => {
    let finish!: (value: unknown) => void;
    render(<TaskSuggestionDeck />);
    const title = await screen.findByText(offer.title);
    mocks.list.mockImplementationOnce(() => new Promise((resolve) => { finish = resolve; }));
    vi.useFakeTimers();
    emit(event);
    emit(event);
    expect(screen.getByText(offer.title)).toBe(title);
    await act(async () => vi.advanceTimersByTimeAsync(499));
    expect(mocks.list).toHaveBeenCalledTimes(1);
    await act(async () => vi.advanceTimersByTimeAsync(1));
    expect(mocks.list).toHaveBeenCalledTimes(2);
    expect(screen.getByText(offer.title)).toBe(title);
    expect(screen.queryByRole("status")).toBeNull();
    await act(async () => finish([{ ...offer, evidence: { ...offer.evidence, text: "Updated rationale" } }]));
    expect(screen.getByText(offer.title)).toBe(title);
    expect(screen.getByText("Updated rationale")).toBeInTheDocument();
});

it("cancels a pending start when its offer disappears during a refresh", async () => {
    render(<TaskSuggestionDeck />);
    await screen.findByText(offer.title);
    vi.useFakeTimers();
    fireEvent.click(screen.getByRole("button", { name: "Do it" }));
    mocks.list.mockResolvedValueOnce([{ ...offer, id: "B", title: "Surviving B" }]);
    emit("activity:status");
    await act(async () => vi.advanceTimersByTimeAsync(500));
    expect(screen.getByText("Surviving B")).toBeInTheDocument();
    await act(async () => vi.advanceTimersByTimeAsync(5000));
    expect(mocks.start).not.toHaveBeenCalled();
});

it("keeps a pending start when the same offer survives a refresh", async () => {
    render(<TaskSuggestionDeck />);
    await screen.findByText(offer.title);
    vi.useFakeTimers();
    fireEvent.click(screen.getByRole("button", { name: "Do it" }));
    mocks.list.mockResolvedValueOnce([{ ...offer, id: "B", title: "Other B" }, offer]);
    emit("activity:status");
    await act(async () => vi.advanceTimersByTimeAsync(5000));
    expect(mocks.start).toHaveBeenCalledWith("A", "1");
    expect(screen.getByText("Other B")).toBeInTheDocument();
});

it("keeps the deck on background failure and cancels a scheduled reload on withdrawal", async () => {
    render(<TaskSuggestionDeck />);
    await screen.findByText(offer.title);
    vi.useFakeTimers();
    mocks.list.mockRejectedValueOnce(new Error("offline"));
    emit("activity:status");
    await act(async () => vi.advanceTimersByTimeAsync(500));
    expect(screen.getByText(offer.title)).toBeInTheDocument();
    expect(screen.getByRole("alert")).toBeInTheDocument();
    emit("activity:status");
    emit("topics:withdrawn");
    expect(screen.queryByText(offer.title)).toBeNull();
    await act(async () => vi.advanceTimersByTimeAsync(500));
    expect(mocks.list).toHaveBeenCalledTimes(3);
});

it("keeps Up next with its empty line when no offers survive", async () => {
    render(<TaskSuggestionDeck />);
    await screen.findByText(offer.title);
    vi.useFakeTimers();
    emit("activity:status");
    await act(async () => vi.advanceTimersByTimeAsync(500));
    expect(screen.queryByText(offer.title)).toBeNull();
    expect(screen.queryByRole("status")).toBeNull();
    expect(screen.getByTestId("up-next-empty")).toHaveTextContent("Nothing up next.");
});

it("fences a background response that arrives after withdrawal", async () => {
    let finish!: (value: unknown) => void;
    render(<TaskSuggestionDeck />);
    await screen.findByText(offer.title);
    mocks.list.mockImplementationOnce(() => new Promise((resolve) => { finish = resolve; }))
        .mockResolvedValueOnce([{ ...offer, id: "B", title: "Surviving B" }]);
    vi.useFakeTimers();
    emit("activity:status");
    await act(async () => vi.advanceTimersByTimeAsync(500));
    emit("memory:withdrawn");
    expect(screen.queryByText(offer.title)).toBeNull();
    await act(async () => {});
    expect(screen.getByText("Surviving B")).toBeInTheDocument();
    await act(async () => finish([offer]));
    expect(screen.queryByText(offer.title)).toBeNull();
    expect(screen.getByText("Surviving B")).toBeInTheDocument();
});
