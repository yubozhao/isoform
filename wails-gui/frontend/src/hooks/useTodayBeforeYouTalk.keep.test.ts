import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { TodayBeforeYouTalk } from "@/lib/wails-shim-app";

const read = vi.hoisted(() => vi.fn());
vi.mock("@/lib/wails-shim-app", () => ({
    GetTodayBeforeYouTalk: read,
    DismissTodayBeforeYouTalk: vi.fn(),
    SetEngagementKindEnabled: vi.fn(),
}));
vi.mock("../../wailsjs/runtime/runtime", () => ({ EventsOn: () => () => {} }));

import { pageQueryCache } from "@/lib/query-cache";
import { resetPageQueries } from "@/lib/page-queries";
import { useTodayBeforeYouTalk } from "./useTodayBeforeYouTalk";

const card = { meetingId: "coffee", meetingTitle: "Coffee" } as TodayBeforeYouTalk;

afterEach(() => {
    vi.useRealTimers();
    read.mockReset();
    resetPageQueries();
});

// Today's "Before you talk" card blinked out every 30 s: the poll cleared the
// cached card before re-reading, and a failed read removed it for good. A
// re-read now keeps the card shown until its answer, and a failed read keeps
// the last card.
describe("useTodayBeforeYouTalk", () => {
    it("keeps the card shown through the 30 s re-read", async () => {
        vi.useFakeTimers({ shouldAdvanceTime: true });
        let answer: (value: TodayBeforeYouTalk) => void = () => {};
        read.mockResolvedValueOnce(card).mockImplementationOnce(() => new Promise((resolve) => { answer = resolve; }));
        const { result } = renderHook(() => useTodayBeforeYouTalk(true));
        await waitFor(() => expect(result.current.card?.meetingId).toBe("coffee"));
        await act(async () => { await vi.advanceTimersByTimeAsync(30_000); });
        expect(read).toHaveBeenCalledTimes(2);
        expect(result.current.card?.meetingId).toBe("coffee");
        await act(async () => { answer({ ...card, meetingTitle: "Coffee, moved" }); });
        await waitFor(() => expect(result.current.card?.meetingTitle).toBe("Coffee, moved"));
    });

    it("keeps the last card when a read fails", async () => {
        read.mockResolvedValueOnce(card).mockRejectedValueOnce(new Error("store busy"));
        const { result } = renderHook(() => useTodayBeforeYouTalk(true));
        await waitFor(() => expect(result.current.card?.meetingId).toBe("coffee"));
        await act(async () => { pageQueryCache.invalidate(["GetTodayBeforeYouTalk"], false); });
        await waitFor(() => expect(read.mock.calls.length).toBeGreaterThanOrEqual(2));
        expect(result.current.card?.meetingId).toBe("coffee");
    });
});
