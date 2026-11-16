import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import i18n from "@/i18n";
import { TodayPastBriefsPanel } from "./TodayPastBriefsPanel";

const mocks = vi.hoisted(() => ({ list: vi.fn() }));
vi.mock("@/lib/wails-shim-app", () => ({ ListPastBriefs: mocks.list, RecordTodayAction: vi.fn().mockResolvedValue(undefined) }));
vi.mock("@/lib/today-telemetry", () => ({ measureToday: (_id: string, _kind: string, _outcome: string, run: () => unknown) => run() }));

const rows = [
    { briefId: "2026-11-15-evening", slot: "evening", localDate: "2026-11-15", asOf: "2026-11-15T18:00:00-08:00", narrativeStatus: "ok", lead: "The brief on Today." },
    { briefId: "2026-11-15-morning", slot: "morning", localDate: "2026-11-15", asOf: "2026-11-15T07:00:00-08:00", narrativeStatus: "ok", lead: "Priya answered about the budget." },
];

describe("TodayPastBriefsPanel", () => {
    beforeEach(async () => {
        await i18n.changeLanguage("en");
        mocks.list.mockReset();
    });
    afterEach(cleanup);

    it("lists earlier briefs, leaves out the one on Today, and opens a row", async () => {
        mocks.list.mockResolvedValue(rows);
        const onOpen = vi.fn();
        render(<TodayPastBriefsPanel currentBriefId="2026-11-15-evening" onOpen={onOpen} onClose={vi.fn()} />);
        const row = await screen.findByTestId("past-brief-row");
        expect(screen.getAllByTestId("past-brief-row")).toHaveLength(1);
        expect(row).toHaveTextContent("Morning brief");
        expect(row).toHaveTextContent("Priya answered about the budget.");
        fireEvent.click(row);
        expect(onOpen).toHaveBeenCalledWith("2026-11-15-morning", expect.stringContaining("Morning brief"));
    });

    it("says there are none yet", async () => {
        mocks.list.mockResolvedValue([rows[0]]);
        render(<TodayPastBriefsPanel currentBriefId="2026-11-15-evening" onOpen={vi.fn()} onClose={vi.fn()} />);
        expect(await screen.findByTestId("past-briefs-empty")).toHaveTextContent("No earlier briefs yet.");
    });

    it("says a failed read, and Try again reads again", async () => {
        mocks.list.mockRejectedValueOnce(new Error("down")).mockResolvedValue(rows);
        render(<TodayPastBriefsPanel onOpen={vi.fn()} onClose={vi.fn()} />);
        fireEvent.click(await screen.findByText("Try again"));
        await waitFor(() => expect(screen.getAllByTestId("past-brief-row")).toHaveLength(2));
        expect(mocks.list).toHaveBeenCalledTimes(2);
    });
});
