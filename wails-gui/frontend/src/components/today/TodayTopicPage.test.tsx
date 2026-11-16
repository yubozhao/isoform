import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import i18n from "@/i18n";
import type { WorkItem, WorkList } from "@/lib/wails-shim-app";
import { TodayTopicPage, seenDays, topicWork } from "./TodayTopicPage";

vi.mock("@/lib/wails-shim-app", () => ({ RecordTodayAction: vi.fn().mockResolvedValue(undefined) }));
vi.mock("@/lib/today-telemetry", () => ({ measureToday: (_id: string, _kind: string, _outcome: string, run: () => unknown) => run() }));

const NOW = new Date(2026, 10, 15, 18, 30);
const item = (id: string, topicId: string, state = "open"): WorkItem => ({
    id, kind: "offered", row: "offered", state, source: "topic", ref: { topicId, proposalId: id }, first: [], noticed: "", work: { title: `Work ${id}` },
    yes: { does: "mail_draft", line: "" }, reasons: [], createdAt: "", order: 0,
    ...(state === "answered" ? { answer: { action: "yes", at: NOW.toISOString(), surface: "computer", line: `Done ${id}` } } : {}),
} as WorkItem);
const list: WorkList = { v: 1, version: "1", generatedAt: "", toAnswer: 2, backFromAgents: 0, items: [item("p1", "halcyon"), item("p2", "other"), item("p3", "halcyon", "answered")] };
const topic = { id: "halcyon", name: "Halcyon import", kind: "work" as const, pinned: false, activityDays: ["2026-11-15", "2026-11-13", "2026-11-01"], latest: { text: "The import failed on row 40." } };

describe("TodayTopicPage", () => {
    beforeEach(async () => { await i18n.changeLanguage("en"); });
    afterEach(cleanup);

    it("counts the days seen this week and keeps the topic's own work", () => {
        expect(seenDays(topic.activityDays, NOW)).toBe(2);
        expect(topicWork(list, "halcyon")?.items.map((i) => i.id)).toEqual(["p1", "p3"]);
    });

    it("shows the topic with Next answered by the keys, Done, what Isoform saw and Ask about it", async () => {
        const onAnswer = vi.fn().mockResolvedValue(null);
        const onAsk = vi.fn();
        const onBack = vi.fn();
        render(<TodayTopicPage topic={topic} list={list} active onAnswer={onAnswer} onUndo={vi.fn()} onAsk={onAsk} onBack={onBack} now={NOW} />);
        expect(screen.getByText("Project")).toBeInTheDocument();
        expect(screen.getByTestId("topic-page-seen")).toHaveTextContent("Seen 2 of the last 7 days");
        expect(screen.getByText("Next")).toBeInTheDocument();
        expect(screen.getByTestId("your-work-open")).toHaveTextContent("Work p1");
        expect(screen.queryByText("Work p2")).toBeNull();
        expect(screen.getByTestId("your-work-done")).toHaveTextContent("Done p3");
        expect(screen.getByTestId("topic-page-saw")).toHaveTextContent("The import failed on row 40.");
        const open = screen.getByTestId("your-work-open");
        open.focus();
        await act(async () => { fireEvent.keyDown(open, { key: "y" }); });
        expect(onAnswer).toHaveBeenCalledWith("p1", { action: "yes" });
        fireEvent.click(screen.getByTestId("topic-page-ask"));
        expect(onAsk).toHaveBeenCalled();
        fireEvent.click(screen.getByTestId("topic-page-back"));
        expect(onBack).toHaveBeenCalled();
    });
});
