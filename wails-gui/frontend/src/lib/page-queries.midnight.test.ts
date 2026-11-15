import { afterEach, describe, expect, it, vi } from "vitest";

const latest = vi.hoisted(() => vi.fn(async () => ({ brief: { briefId: "2026-11-15-evening" } })));
vi.mock("@/lib/wails-shim-app", () => ({ GetLatestBrief: latest }));
vi.mock("../../wailsjs/runtime/runtime", () => ({ EventsOn: () => () => {} }));

import { GetLatestBrief, onLocalDayChange, resetPageQueries } from "./page-queries";
import { pageQueryCache } from "./query-cache";

afterEach(() => {
    vi.useRealTimers();
    latest.mockClear();
    resetPageQueries();
});

// After midnight Today kept the brief it read the day before: the read is
// cached for 15 minutes and nothing re-read it when the date changed, so its
// day words ("today") and cards stayed on the old day. A new local day now
// re-reads every Today read, keeping what is shown until the answer arrives.
describe("Today across midnight", () => {
    it("re-reads the brief once at local midnight, keeping it shown meanwhile", async () => {
        vi.useFakeTimers({ shouldAdvanceTime: false });
        vi.setSystemTime(new Date(2026, 10, 15, 23, 50));
        await GetLatestBrief();
        expect(latest).toHaveBeenCalledTimes(1);
        const refreshed = vi.fn();
        const off = pageQueryCache.subscribe((name) => { if (name === "GetLatestBrief") refreshed(); });
        await vi.advanceTimersByTimeAsync(9 * 60_000);
        expect(refreshed).not.toHaveBeenCalled();
        await vi.advanceTimersByTimeAsync(2 * 60_000);
        expect(refreshed).toHaveBeenCalledTimes(1);
        expect(pageQueryCache.peek("GetLatestBrief")).toBeDefined();
        await GetLatestBrief();
        expect(latest).toHaveBeenCalledTimes(2);
        off();
    });

    it("tells a window shown again after the date changed", async () => {
        vi.useFakeTimers({ shouldAdvanceTime: false });
        vi.setSystemTime(new Date(2026, 10, 15, 22, 0));
        const changed = vi.fn();
        const off = onLocalDayChange(changed);
        Object.defineProperty(document, "visibilityState", { configurable: true, get: () => "hidden" });
        document.dispatchEvent(new Event("visibilitychange"));
        vi.setSystemTime(new Date(2026, 10, 16, 7, 30));
        await vi.advanceTimersByTimeAsync(0);
        expect(changed).not.toHaveBeenCalled();
        Object.defineProperty(document, "visibilityState", { configurable: true, get: () => "visible" });
        document.dispatchEvent(new Event("visibilitychange"));
        expect(changed).toHaveBeenCalledTimes(1);
        off();
    });
});
