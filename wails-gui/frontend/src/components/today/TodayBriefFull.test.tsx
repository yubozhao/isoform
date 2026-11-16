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
