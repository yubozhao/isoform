import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import i18n from "@/i18n";
import type { MobileBriefDocument, TodayBriefView, WorkItem } from "@/lib/wails-shim-app";
import { TodayBriefFull, readingAt, sentenceWorkId } from "./TodayBriefFull";

const mocks = vi.hoisted(() => ({ toggle: vi.fn(), seek: vi.fn(), rate: vi.fn(), list: vi.fn() }));
vi.mock("@/lib/wails-shim-app", () => ({ RecordTodayAction: vi.fn().mockResolvedValue(undefined), ListPastBriefs: mocks.list, GetMobileBriefAudio: vi.fn() }));
vi.mock("@/lib/brief-audio", async (importOriginal) => ({
    ...(await importOriginal<typeof import("@/lib/brief-audio")>()),
    toggleBriefPlayback: mocks.toggle,
    seekBriefPlayback: mocks.seek,
    setBriefPlaybackRate: mocks.rate,
}));
vi.mock("@/lib/today-telemetry", () => ({ measureToday: (_id: string, _kind: string, _outcome: string, run: () => unknown) => run() }));

const brief = {
    briefId: "2026-11-15-evening", slot: "evening", asOf: "2026-11-15T18:00:00-08:00", timezone: "America/Los_Angeles", narrativeStatus: "ok",
    narrative: [{ text: "Halcyon's review is first." }, { text: "Lena moved the call to ten." }],
    sections: { moved: [], doing: [], watching: [], meetings: [], people: [], openLoops: [], learned: [], deliverables: [], schedules: [] },
    cards: [], audio: { status: "ok", bytes: 1200, mime: "audio/mp4", durationSeconds: 120 },
} as unknown as MobileBriefDocument;
const view = { brief, building: false, enabled: true, nextAt: "", nextSlot: "", empty: false, missing: [] } as TodayBriefView;
const offer = { id: "brief:2026-11-15-evening:offer:o1", kind: "offered", row: "draft", state: "open", source: "brief", ref: { briefId: "2026-11-15-evening", paragraph: "1" }, first: [], noticed: "", work: { title: "Reply to Lena" }, yes: { does: "mail_draft", line: "" }, reasons: [], createdAt: "", order: 0 } as unknown as WorkItem;

describe("TodayBriefFull", () => {
    beforeEach(async () => {
        await i18n.changeLanguage("en");
        Object.values(mocks).forEach((mock) => mock.mockReset());
        mocks.toggle.mockResolvedValue(undefined);
        mocks.list.mockResolvedValue([]);
    });
    afterEach(cleanup);

    it("shows the brief in full with the player, its items where mentioned, and Past briefs", async () => {
        const onAnswer = vi.fn().mockResolvedValue(null);
        render(<TodayBriefFull brief={brief} view={view} active items={[offer]} onAnswer={onAnswer} onOpenBrief={vi.fn()} onBack={vi.fn()} />);
        await act(async () => {});
        expect(screen.getByText("Lena moved the call to ten.")).toBeInTheDocument();
        expect(screen.getByTestId("brief-full-player")).toBeInTheDocument();
        expect(screen.getByTestId("brief-full-item")).toHaveTextContent("Reply to Lena");
        fireEvent.click(screen.getByRole("button", { name: "Yes" }));
        expect(onAnswer).toHaveBeenCalledWith(offer.id, { action: "yes" });
        expect(screen.getByTestId("brief-full-past")).toHaveTextContent("Every brief stays on this computer; the phone keeps 30 days.");
    });

    it("lists In this brief by kind above Past briefs, each opening its Why", async () => {
        const withSources = { ...brief, evidence: [
            { ref: "e1", sourceKind: "activity", sourceId: "a1", label: "Worked on the import", observedAt: "2026-11-15T15:00:00-08:00", group: "screen" },
            { ref: "e2", sourceKind: "calendar", sourceId: "c1", label: "Halcyon review", observedAt: "2026-11-15T09:00:00-08:00", group: "calendar" },
            { ref: "e3", sourceKind: "purpose", sourceId: "p1", label: "Ship the import", observedAt: "2026-11-15T09:00:00-08:00" },
            { ref: "e4", sourceKind: "activity_session", sourceId: "s1", label: "A moment you marked on your watch", observedAt: "2026-11-15T12:00:00-08:00", group: "watch" },
            { ref: "e5", sourceKind: "meeting_transcript", sourceId: "m1", label: "Call with Lena", observedAt: "2026-11-15T11:00:00-08:00", group: "calls" },
            { ref: "e6", sourceKind: "meeting", sourceId: "m2", label: "Standup", observedAt: "2026-11-15T10:00:00-08:00", group: "calls" },
        ] } as unknown as MobileBriefDocument;
        render(<TodayBriefFull brief={withSources} view={view} active onOpenBrief={vi.fn()} onBack={vi.fn()} />);
        await act(async () => {});
        const column = screen.getByTestId("brief-full-past");
        const sources = screen.getByTestId("brief-full-sources");
        expect(column.firstElementChild).toBe(sources);
        expect(sources).toHaveTextContent("In this brief");
        expect(sources).toHaveTextContent("Each opens its Why.");
        expect(screen.getAllByTestId("brief-source-kind").map((row) => row.getAttribute("data-kind"))).toEqual(["calendar", "calls", "watch", "screen"]);
        expect(screen.getByRole("button", { name: /Calls and meetings/ })).toHaveTextContent("2");
        expect(sources).not.toHaveTextContent("Ship the import");
        expect(screen.queryByTestId("brief-source-why")).toBeNull();
        const calendar = screen.getByRole("button", { name: /Calendar/ });
        fireEvent.click(calendar);
        expect(calendar).toHaveAttribute("aria-expanded", "true");
        expect(screen.getByTestId("brief-source-why")).toHaveTextContent("Sun · Calendar · Halcyon review");
    });

    it("shows no In this brief when the brief has no sources of the eight kinds", async () => {
        render(<TodayBriefFull brief={brief} view={view} active onOpenBrief={vi.fn()} onBack={vi.fn()} />);
        await act(async () => {});
        expect(screen.queryByTestId("brief-full-sources")).toBeNull();
    });

    it("puts an agent's result under the Your agents paragraph, answered from there (brief-agents rules 5 and 6)", async () => {
        const withAgents = { ...brief, narrative: [...(brief.narrative ?? []), { text: "Your agents: Codex finished the test plan." }] } as MobileBriefDocument;
        const result = { id: "result:wo_plan", kind: "result", row: "result", state: "back", source: "agent", ref: { briefId: "2026-11-15-evening", paragraph: "2", handoffId: "wo_plan" }, first: [], noticed: "The test plan is ready: 14 cases.", work: { title: "The test plan", text: "The test plan is ready: 14 cases." }, yes: { does: "open", line: "Was it useful?" }, reasons: ["wrong_facts", "missed_point", "myself"], createdAt: "", order: 0 } as unknown as WorkItem;
        const onAnswer = vi.fn().mockResolvedValue(null);
        render(<TodayBriefFull brief={withAgents} view={view} active items={[result]} onAnswer={onAnswer} onOpenBrief={vi.fn()} onBack={vi.fn()} />);
        await act(async () => {});
        const paragraph = screen.getByText("Your agents: Codex finished the test plan.").parentElement!;
        expect(paragraph).toContainElement(screen.getByTestId("brief-full-item"));
        expect(screen.getByTestId("brief-full-item")).toHaveTextContent("The test plan");
        fireEvent.click(screen.getByRole("button", { name: "Useful" }));
        expect(onAnswer).toHaveBeenCalledWith(result.id, { action: "useful" });
    });

    it("plays with Space, moves 15 seconds with the arrows, and goes back with Escape", async () => {
        const onBack = vi.fn();
        render(<TodayBriefFull brief={brief} view={view} active onOpenBrief={vi.fn()} onBack={onBack} />);
        await act(async () => { fireEvent.keyDown(document.body, { key: " ", code: "Space" }); });
        expect(mocks.toggle).toHaveBeenCalledWith("2026-11-15-evening");
        fireEvent.keyDown(document.body, { key: "ArrowLeft" });
        fireEvent.keyDown(document.body, { key: "ArrowRight" });
        expect(mocks.seek).toHaveBeenNthCalledWith(1, "2026-11-15-evening", -15);
        expect(mocks.seek).toHaveBeenNthCalledWith(2, "2026-11-15-evening", 15);
        fireEvent.click(screen.getByTestId("brief-full-speed"));
        expect(mocks.rate).toHaveBeenCalledWith(1.25);
        fireEvent.keyDown(document.body, { key: "Escape" });
        expect(onBack).toHaveBeenCalled();
    });

    it("follows the voice: the sentence being read, from its mark", () => {
        const marks = [
            { source: "narrative" as const, paragraph: 0, sentence: 0, from: 0, to: 8, start: 0, end: 2 },
            { source: "narrative" as const, paragraph: 0, sentence: 1, from: 9, to: 20, start: 2, end: 4 },
            { source: "weeklyRecap" as const, paragraph: 0, sentence: 0, from: 0, to: 5, start: 4, end: 6 },
        ];
        expect(readingAt(1, marks)).toEqual({ paragraph: 0, from: 0, to: 8 });
        expect(readingAt(3, marks)).toEqual({ paragraph: 0, from: 9, to: 20 });
        expect(readingAt(5, marks)).toBeNull();
        expect(readingAt(1, undefined)).toBeNull();
    });

    it("lists past briefs with where you are with each", async () => {
        mocks.list.mockResolvedValue([
            { briefId: "2026-11-15-morning", slot: "morning", localDate: "2026-11-15", asOf: "2026-11-15T07:00:00-08:00", narrativeStatus: "ok", listened: "not_played", toAnswer: 2, answered: 0 },
            { briefId: "2026-11-14-evening", slot: "evening", localDate: "2026-11-14", asOf: "2026-11-14T18:00:00-08:00", narrativeStatus: "ok", listened: "played", played: true, toAnswer: 1, answered: 1 },
            { briefId: "2026-11-14-morning", slot: "morning", localDate: "2026-11-14", asOf: "2026-11-14T07:00:00-08:00", narrativeStatus: "ok", listened: "played", played: true, toAnswer: 0, answered: 3 },
        ]);
        render(<TodayBriefFull brief={brief} view={view} active onOpenBrief={vi.fn()} onBack={vi.fn()} />);
        const rows = await screen.findAllByTestId("past-brief-row");
        expect(rows[0]).toHaveTextContent("not played");
        expect(rows[1]).toHaveTextContent("1 left to answer");
        expect(rows[2]).toHaveTextContent("all answered");
    });

    it("marks a sentence Not right with a reason and the person's words", async () => {
        const onNotRight = vi.fn().mockResolvedValue(null);
        render(<TodayBriefFull brief={brief} view={view} active onNotRight={onNotRight} onOpenBrief={vi.fn()} onBack={vi.fn()} />);
        fireEvent.click(screen.getAllByTestId("brief-full-not-right")[1]);
        expect(screen.getByTestId("not-right-sheet")).toHaveTextContent("Lena moved the call to ten.");
        fireEvent.click(screen.getByRole("button", { name: "Already done" }));
        fireEvent.change(screen.getByTestId("not-right-words"), { target: { value: "It's at eleven." } });
        await act(async () => { fireEvent.click(screen.getByTestId("not-right-send")); });
        expect(onNotRight).toHaveBeenCalledWith(sentenceWorkId("2026-11-15-evening", 1), { action: "no", reason: "already_done", after: "It's at eleven." });
    });
});

describe("TodayBriefFull items answered with the keys (computer-brief v1.0.0 rule 4)", () => {
    const second = { ...offer, id: "brief:2026-11-15-evening:offer:o2", work: { title: "Prep for Halcyon" }, ref: { briefId: "2026-11-15-evening", paragraph: "0" } } as unknown as WorkItem;
    const result = { ...offer, id: "brief:2026-11-15-evening:result:r1", state: "back", work: { title: "Test plan" } } as unknown as WorkItem;
    beforeEach(async () => {
        await i18n.changeLanguage("en");
        mocks.list.mockResolvedValue([]);
    });
    afterEach(cleanup);

    it("answers the focused item with Y, N and M, and moves between items with the arrows", async () => {
        const onAnswer = vi.fn().mockResolvedValue(null);
        render(<TodayBriefFull brief={brief} view={view} active items={[offer, second]} onAnswer={onAnswer} onOpenBrief={vi.fn()} onBack={vi.fn()} />);
        await act(async () => {});
        const [first, next] = screen.getAllByTestId("brief-full-item");
        expect(first).toHaveTextContent("Prep for Halcyon");
        first.focus();
        fireEvent.keyDown(first, { key: "ArrowDown" });
        expect(document.activeElement).toBe(next);
        await act(async () => { fireEvent.keyDown(next, { key: "n" }); });
        expect(onAnswer).toHaveBeenLastCalledWith(offer.id, { action: "no" });
        await act(async () => { fireEvent.keyDown(next, { key: "m" }); });
        expect(onAnswer).toHaveBeenLastCalledWith(offer.id, { action: "not_mine" });
        fireEvent.keyDown(next, { key: "ArrowUp" });
        await act(async () => { fireEvent.keyDown(first, { key: "y" }); });
        expect(onAnswer).toHaveBeenLastCalledWith(second.id, { action: "yes" });
        expect(screen.getAllByRole("button", { name: "Yes" })[0]).toHaveAttribute("aria-keyshortcuts", "Y");
    });

    it("judges a result with Useful (Y) and Not useful (N), with no Not mine", async () => {
        const onAnswer = vi.fn().mockResolvedValue(null);
        render(<TodayBriefFull brief={brief} view={view} active items={[result]} onAnswer={onAnswer} onOpenBrief={vi.fn()} onBack={vi.fn()} />);
        await act(async () => {});
        const row = screen.getByTestId("brief-full-item");
        await act(async () => { fireEvent.keyDown(row, { key: "y" }); });
        expect(onAnswer).toHaveBeenLastCalledWith(result.id, { action: "useful" });
        await act(async () => { fireEvent.keyDown(row, { key: "m" }); });
        expect(onAnswer).toHaveBeenCalledTimes(1);
        expect(screen.queryByRole("button", { name: "Not mine" })).not.toBeInTheDocument();
    });

    it("opens the hand-off sheet on a Yes to agent work", async () => {
        const onAnswer = vi.fn().mockResolvedValue(null);
        const onHandOff = vi.fn();
        const agent = { ...offer, yes: { does: "agent", line: "" } } as unknown as WorkItem;
        render(<TodayBriefFull brief={brief} view={view} active items={[agent]} onAnswer={onAnswer} onHandOff={onHandOff} onOpenBrief={vi.fn()} onBack={vi.fn()} />);
        await act(async () => {});
        fireEvent.keyDown(screen.getByTestId("brief-full-item"), { key: "y" });
        expect(onHandOff).toHaveBeenCalledWith(agent);
        expect(onAnswer).not.toHaveBeenCalled();
    });

    it("folds an answered item to one line with Undo while it can be undone", async () => {
        const onUndo = vi.fn().mockResolvedValue(null);
        const answered = { ...offer, state: "answered", answer: { action: "yes", at: "", surface: "computer", line: "Reply to Lena · in your Mail drafts", undoUntil: new Date(Date.now() + 60_000).toISOString() } } as unknown as WorkItem;
        render(<TodayBriefFull brief={brief} view={view} active items={[answered]} onUndo={onUndo} onOpenBrief={vi.fn()} onBack={vi.fn()} />);
        await act(async () => {});
        expect(screen.getByTestId("brief-full-item")).toHaveTextContent("Reply to Lena · in your Mail drafts");
        await act(async () => { fireEvent.click(screen.getByTestId("brief-full-item-undo")); });
        expect(onUndo).toHaveBeenCalledWith(offer.id);
    });

    it("never answers while typing in a field inside the page", async () => {
        const onAnswer = vi.fn().mockResolvedValue(null);
        render(<TodayBriefFull brief={brief} view={view} active items={[offer]} onAnswer={onAnswer} onOpenBrief={vi.fn()} onBack={vi.fn()} />);
        await act(async () => {});
        fireEvent.keyDown(screen.getByRole("button", { name: "Yes" }), { key: "n" });
        expect(onAnswer).not.toHaveBeenCalled();
    });
});
