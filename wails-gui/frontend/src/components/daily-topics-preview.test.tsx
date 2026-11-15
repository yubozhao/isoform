import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import "@/i18n";
import { DailyTopicsPreview } from "./daily-topics-preview";

const mocks = vi.hoisted(() => ({
    get: vi.fn(),
    setEnabled: vi.fn(),
    refresh: vi.fn(),
    answer: vi.fn(),
    eventsOn: vi.fn(() => () => {}),
}));

vi.mock("@/lib/wails-shim-app", async (importOriginal) => {
    const original = await importOriginal<typeof import("@/lib/wails-shim-app")>();
    return {
        ...original,
        GetDailyTopics: mocks.get,
        SetDailyTopicsEnabled: mocks.setEnabled,
        RefreshDailyTopics: mocks.refresh,
        AnswerDailyTopicProposal: mocks.answer,
    };
});

vi.mock("../../wailsjs/runtime/runtime", () => ({
    EventsOn: mocks.eventsOn,
}));

const view = {
    enabled: true,
    day: "2026-11-14",
    topics: [
        { id: "t1", kind: "project", name: "Harbor launch", proposal: { id: "p1", title: "Draft the launch checklist", state: "proposed" } },
        { id: "t2", kind: "subject", name: "Tide tables", proposal: { id: "p2", title: "Summarize the research", state: "done", result: "Summary ready" } },
    ],
};

describe("DailyTopicsPreview", () => {
    beforeEach(() => {
        vi.clearAllMocks();
        mocks.get.mockResolvedValue(view);
        mocks.answer.mockResolvedValue(view.topics[0]);
    });

    it("shows the day's topics and answers a proposal", async () => {
        render(<DailyTopicsPreview />);
        expect(await screen.findByText("Harbor launch")).toBeTruthy();
        expect(screen.getByText("Draft the launch checklist")).toBeTruthy();
        expect(screen.getByText(/Summary ready/)).toBeTruthy();
        fireEvent.click(screen.getByRole("button", { name: "Do it" }));
        await waitFor(() => expect(mocks.answer).toHaveBeenCalledWith("t1", "p1", "yes"));
    });

    it("shows no topics while the switch is off", async () => {
        mocks.get.mockResolvedValue({ enabled: false, topics: [] });
        render(<DailyTopicsPreview />);
        await waitFor(() => expect(mocks.get).toHaveBeenCalled());
        expect(screen.queryByText("Harbor launch")).toBeNull();
        expect(screen.getByRole("switch")).toBeTruthy();
    });
});
