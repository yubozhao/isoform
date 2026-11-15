import { afterEach, describe, expect, it, vi } from "vitest";

const latest = vi.hoisted(() => vi.fn());
vi.mock("@/lib/wails-shim-app", () => ({ GetLatestBrief: latest }));
vi.mock("../../wailsjs/runtime/runtime", () => ({ EventsOn: () => () => {} }));

import { GetLatestBrief, resetPageQueries, TODAY_READ_RETRY_MS } from "./page-queries";
import { pageQueryCache } from "./query-cache";

afterEach(() => {
    vi.useRealTimers();
    latest.mockReset();
    resetPageQueries();
});

// A Today read that failed stayed failed until some unrelated event re-read
// it (the brief retried once, quietly; Done for you, the status line and the
// check-in never). A failed Today read is now read again on its own, backing
// off, until it succeeds.
describe("Today read retry", () => {
    it("re-reads a failed brief read after a backoff, and stops once it succeeds", async () => {
        vi.useFakeTimers();
        latest.mockRejectedValueOnce(new Error("store busy")).mockRejectedValueOnce(new Error("store busy")).mockResolvedValue({ brief: { briefId: "b1" } });
        const told = vi.fn();
        const off = pageQueryCache.subscribe((name) => { if (name === "GetLatestBrief") told(); });
        await expect(GetLatestBrief()).rejects.toThrow("store busy");
        await vi.advanceTimersByTimeAsync(TODAY_READ_RETRY_MS[0] - 1);
        expect(told).not.toHaveBeenCalled();
        await vi.advanceTimersByTimeAsync(1);
        expect(told).toHaveBeenCalledTimes(1);
        await expect(GetLatestBrief()).rejects.toThrow("store busy");
        await vi.advanceTimersByTimeAsync(TODAY_READ_RETRY_MS[0]);
        expect(told).toHaveBeenCalledTimes(1);
        await vi.advanceTimersByTimeAsync(TODAY_READ_RETRY_MS[1] - TODAY_READ_RETRY_MS[0]);
        expect(told).toHaveBeenCalledTimes(2);
        await expect(GetLatestBrief()).resolves.toEqual({ brief: { briefId: "b1" } });
        const afterSuccess = told.mock.calls.length;
        await vi.advanceTimersByTimeAsync(10 * 60_000);
        expect(told).toHaveBeenCalledTimes(afterSuccess);
        expect(latest).toHaveBeenCalledTimes(3);
        off();
    });
});
