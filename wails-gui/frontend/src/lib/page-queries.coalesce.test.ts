import { afterEach, describe, expect, it, vi } from "vitest";

const listeners = vi.hoisted(() => new Map<string, ((data?: unknown) => void)[]>());
const health = vi.hoisted(() => vi.fn(async () => ({ enabled: true })));
const week = vi.hoisted(() => vi.fn(async () => ({ available: true, days: 7, kinds: [] })));
vi.mock("@/lib/wails-shim-app", () => ({ GetMobileBriefHealth: health, GetEngagementWeekSummary: week }));
vi.mock("../../wailsjs/runtime/runtime", () => ({
    EventsOn: (event: string, fn: (data?: unknown) => void) => {
        listeners.set(event, [...(listeners.get(event) ?? []), fn]);
        return () => listeners.set(event, (listeners.get(event) ?? []).filter((other) => other !== fn));
    },
}));

import { COALESCE_EVENT_MS, GetEngagementWeekSummary, GetMobileBriefHealth, installPageQueryEvents, resetPageQueries } from "./page-queries";
import { pageQueryCache } from "./query-cache";

afterEach(() => {
    vi.useRealTimers();
    health.mockClear();
    week.mockClear();
    resetPageQueries();
});

// Today's status reads (the week summary and the brief health) re-read on
// mobile:updated. A burst of the event is one re-read of each, not one per
// event (2026-11-15: three of each within 40 ms).
describe("mobile:updated bursts", () => {
    it("re-read Today's status once per burst", async () => {
        vi.useFakeTimers({ shouldAdvanceTime: false });
        installPageQueryEvents();
        await Promise.all([GetMobileBriefHealth(), GetEngagementWeekSummary()]);
        const off = pageQueryCache.subscribe((name) => {
            if (name === "GetMobileBriefHealth") void GetMobileBriefHealth();
            if (name === "GetEngagementWeekSummary") void GetEngagementWeekSummary();
        });
        for (let i = 0; i < 3; i++) {
            for (const fn of listeners.get("mobile:updated") ?? []) fn();
            await vi.advanceTimersByTimeAsync(15);
        }
        await vi.advanceTimersByTimeAsync(COALESCE_EVENT_MS);
        expect(health).toHaveBeenCalledTimes(2);
        expect(week).toHaveBeenCalledTimes(2);
        off();
    });
});
