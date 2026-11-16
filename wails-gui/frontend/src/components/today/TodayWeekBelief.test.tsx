import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import i18n from "@/i18n";
import { YourWeekCard } from "./YourWeekCard";
import { TodayWeekBeliefReceipt, useTodayWeekBelief } from "./useTodayWeekBelief";
import type { MobileBriefWeek } from "@/lib/wails-shim-app";

const mocks = vi.hoisted(() => ({
    save: vi.fn(), undo: vi.fn(), canUndo: vi.fn(), listeners: new Set<(event?: string, payload?: unknown) => void>(),
}));
vi.mock("@/lib/page-privacy-withdrawal", () => ({ onPagePrivacyWithdrawal: (fn: (event?: string, payload?: unknown) => void) => { mocks.listeners.add(fn); return () => mocks.listeners.delete(fn); } }));
vi.mock("@/lib/wails-shim-app", () => ({
    CorrectTodayWeekBelief: mocks.save, UndoTodayWeekBelief: mocks.undo, CanUndoTodayWeekBelief: mocks.canUndo,
    GetTodayYourWeekDeclined: vi.fn(async () => false), DeclineTodayYourWeek: vi.fn(),
    SetEngagementKindEnabled: vi.fn(), OpenDeliverableFile: vi.fn(), RecordTodayAction: vi.fn(async () => undefined),
}));

const week: MobileBriefWeek = { start: "2026-10-19", end: "2026-10-23", lead: "Your week", learned: [
    { id: "memory:m1", kind: "memory", text: "Prefers mornings", memoryId: "m1" },
] };
const handlers = { onStartChat: vi.fn(), onRunNow: vi.fn(), onOpenMemory: vi.fn(), onOpenAutomations: vi.fn(), onOpenActivity: vi.fn(), onOpenPeople: vi.fn() };

function Harness({ showWeek = true }: { showWeek?: boolean }) {
    const review = useTodayWeekBelief(true);
    return showWeek ? <YourWeekCard week={week} cards={[]} handlers={handlers} onCard={vi.fn()} briefId="brief" beliefReview={review} /> : <TodayWeekBeliefReceipt review={review} />;
}

beforeEach(async () => {
    await i18n.changeLanguage("en");
    vi.clearAllMocks();
    mocks.listeners.clear();
    mocks.save.mockImplementation(async (_brief: string, _id: string, _expected: string, _words: string, correlation: string) => {
        for (const listener of mocks.listeners) listener("memory:withdrawn", { correlation });
        return "opaque-token";
    });
    mocks.undo.mockResolvedValue(undefined);
    mocks.canUndo.mockResolvedValue(true);
});

describe("Today weekly belief correction", () => {
    it.each(["", "Prefer afternoons"])("saves %j in place and keeps Undo when the brief refreshes", async (words) => {
        const view = render(<Harness />);
        fireEvent.click(screen.getByRole("button", { name: "Not quite" }));
        const field = screen.getByRole("textbox", { name: "What’s closer? You can leave it blank." });
        fireEvent.change(field, { target: { value: words } });
        fireEvent.click(screen.getByRole("button", { name: "Save" }));
        expect(await screen.findByRole("button", { name: "Undo" })).toBeInTheDocument();
        expect(mocks.save).toHaveBeenCalledWith("brief", "m1", "Prefers mornings", words, expect.any(String));
        expect(handlers.onOpenMemory).not.toHaveBeenCalled();
        view.rerender(<Harness showWeek={false} />);
        fireEvent.click(screen.getByRole("button", { name: "Undo" }));
        await waitFor(() => expect(mocks.undo).toHaveBeenCalledWith("opaque-token", expect.any(String)));
        await waitFor(() => expect(screen.queryByRole("button", { name: "Undo" })).not.toBeInTheDocument());
    });

    it("drops a late correction receipt after an unrelated privacy withdrawal", async () => {
        let finish: (token: string) => void = () => {};
        mocks.save.mockReturnValue(new Promise<string>((resolve) => { finish = resolve; }));
        render(<Harness />);
        fireEvent.click(screen.getByRole("button", { name: "Not quite" }));
        fireEvent.click(screen.getByRole("button", { name: "Save" }));
        for (const listener of mocks.listeners) listener("sensitive-apps:updated");
        finish("late-token");
        await waitFor(() => expect(screen.queryByRole("button", { name: "Undo" })).not.toBeInTheDocument());
    });

    it("revalidates an Undo receipt after indexing and drops it when its source is gone", async () => {
        render(<Harness />);
        fireEvent.click(screen.getByRole("button", { name: "Not quite" }));
        fireEvent.click(screen.getByRole("button", { name: "Save" }));
        await screen.findByRole("button", { name: "Undo" });
        for (const listener of mocks.listeners) listener("memory:updated", { reason: "index" });
        await waitFor(() => expect(mocks.canUndo).toHaveBeenCalledWith("opaque-token"));
        await screen.findByRole("button", { name: "Undo" });
        mocks.canUndo.mockResolvedValueOnce(false);
        for (const listener of mocks.listeners) listener("memory:updated", { reason: "index" });
        await waitFor(() => expect(screen.queryByRole("button", { name: "Undo" })).not.toBeInTheDocument());
    });

    it("shows the field and label in Chinese", async () => {
        await i18n.changeLanguage("zh-CN");
        render(<Harness />);
        fireEvent.click(screen.getByRole("button", { name: "不太对" }));
        expect(screen.getByRole("textbox", { name: "怎样说更准确？也可以不填。" })).toBeInTheDocument();
    });
});


it.each(["en", "zh-CN"])("keeps mute Undo and belief correction on their own rows in %s", async (language) => {
    await i18n.changeLanguage(language);
    function BothRows() {
        const review = useTodayWeekBelief(true);
        return <YourWeekCard week={{ ...week, learned: [...week.learned!, { id: "mute:topic_finds", kind: "mute", text: "Muted topics", engagementKind: "topic_finds" }] }} cards={[]} handlers={handlers} onCard={vi.fn()} briefId="brief" beliefReview={review} />;
    }
    render(<BothRows />);
    const belief = screen.getByText("Prefers mornings").parentElement!;
    const mute = screen.getByText("Muted topics").parentElement!;
    const undo = language === "en" ? "Undo" : "撤销";
    const correction = language === "en" ? "Not quite" : "不太对";
    expect(within(mute).getByRole("button", { name: undo })).toBeInTheDocument();
    expect(within(mute).queryByRole("button", { name: correction })).not.toBeInTheDocument();
    expect(within(belief).getByRole("button", { name: correction })).toBeInTheDocument();
    expect(within(belief).queryByRole("button", { name: undo })).not.toBeInTheDocument();
});
