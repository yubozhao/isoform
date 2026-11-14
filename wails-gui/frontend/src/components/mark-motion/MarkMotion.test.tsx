import { act, render } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { MarkMachine } from "./mark-machine";

const mocks = vi.hoisted(() => ({
    handlers: new Map<string, Set<(...args: unknown[]) => void>>(),
    latest: { brief: { briefId: "2026-11-10-morning", slot: "morning", asOf: "2026-11-10T07:00:00-08:00" }, building: false, enabled: true, nextAt: "", nextSlot: "" } as Record<string, unknown>,
    doingNow: { kind: "watching" } as Record<string, unknown>,
}));

vi.mock("@/lib/wails-shim-app", () => ({
    GetDoingNow: vi.fn(async () => mocks.doingNow),
    GetLatestBrief: vi.fn(async () => mocks.latest),
}));
vi.mock("../../../wailsjs/runtime/runtime", () => ({
    EventsOn: vi.fn((event: string, cb: (...args: unknown[]) => void) => {
        const set = mocks.handlers.get(event) ?? new Set();
        set.add(cb);
        mocks.handlers.set(event, set);
        return () => set.delete(cb);
    }),
}));

import { MarkMotion } from "./MarkMotion";
import { pageQueryCache } from "@/lib/query-cache";

const emit = (event: string, payload?: unknown) => act(async () => {
    mocks.handlers.get(event)?.forEach((cb) => cb(payload));
    await new Promise((r) => setTimeout(r, 0));
});
const flush = () => act(async () => { await new Promise((r) => setTimeout(r, 0)); });

describe("MarkMotion", () => {
    let events: ReturnType<typeof vi.spyOn>;
    beforeEach(() => {
        pageQueryCache.clear();
        events = vi.spyOn(MarkMachine.prototype, "event");
    });
    afterEach(() => {
        events.mockRestore();
    });

    it("rests as the logo's own paths, the moving drawing hidden", async () => {
        const { container } = render(<MarkMotion size={19} />);
        await flush();
        const svg = container.querySelector("svg[data-mark-motion]")!;
        expect(svg.querySelector('[data-k="logo"]')).not.toHaveStyle({ display: "none" });
        expect(svg.querySelector('[data-k="kit"]')).toHaveStyle({ display: "none" });
        expect(svg.getAttribute("aria-hidden")).toBe("true");
    });

    it("plays needs you when the count of things that need you goes up, never when it goes down", async () => {
        const { rerender } = render(<MarkMotion size={19} needsCount={1} />);
        await flush();
        expect(events).not.toHaveBeenCalled();
        rerender(<MarkMotion size={19} needsCount={2} />);
        expect(events).toHaveBeenCalledWith("needs");
        events.mockClear();
        rerender(<MarkMotion size={19} needsCount={0} />);
        expect(events).not.toHaveBeenCalled();
    });

    it("plays the brief once for a newly stored brief, the sunset for the evening one", async () => {
        render(<MarkMotion size={19} />);
        await flush();
        // Stored again under the id already there: an amend, not an arrival.
        await emit("brief:stored", "2026-11-10-morning");
        expect(events).not.toHaveBeenCalled();
        mocks.latest = { ...mocks.latest, brief: { briefId: "2026-11-10-evening", slot: "evening", asOf: "2026-11-10T18:00:00-08:00" } };
        await emit("brief:stored", "2026-11-10-evening");
        await flush();
        expect(events).toHaveBeenCalledWith("brief", { evening: true });
        events.mockClear();
        await emit("brief:stored", "2026-11-10-evening");
        await flush();
        expect(events).not.toHaveBeenCalled();
    });

    it("plays nothing for a quiet brief", async () => {
        mocks.latest = { brief: { briefId: "2026-11-10-morning", slot: "morning", asOf: "2026-11-10T07:00:00-08:00" }, building: false, enabled: true, nextAt: "", nextSlot: "" };
        render(<MarkMotion size={19} />);
        await flush();
        mocks.latest = { ...mocks.latest, quiet: true, brief: { briefId: "2026-11-11-morning", slot: "morning", asOf: "2026-11-11T07:00:00-08:00" } };
        await emit("brief:stored", "2026-11-11-morning");
        await flush();
        expect(events).not.toHaveBeenCalled();
    });

    it("names the state with the status line's own words while it shows (rule 9)", async () => {
        mocks.doingNow = { kind: "working", title: "Pricing page copy" };
        const { container } = render(<MarkMotion size={19} />);
        await flush();
        const svg = container.querySelector("svg[data-mark-motion]")!;
        expect(svg.querySelector("title")?.textContent).toBe("Working on: Pricing page copy");
        expect(svg.getAttribute("aria-label")).toBe("Working on: Pricing page copy");
        mocks.doingNow = { kind: "watching" };
    });

    // Bo, Nov 13: "I don't see the little icon animation." A WKWebView reports
    // document.hasFocus() false whenever another app is active, though the
    // window is on screen; the mark asked for focus and so never moved.
    it("moves while the window is on screen though another app has focus, and stops when it is hidden (rule 16)", async () => {
        const focus = vi.spyOn(document, "hasFocus").mockReturnValue(false);
        const visibility = vi.spyOn(document, "visibilityState", "get").mockReturnValue("visible");
        try {
            mocks.doingNow = { kind: "working", title: "Pricing page copy" };
            render(<MarkMotion size={19} />);
            await flush();
            expect(window.__isoformMark?.playing).toBe("working.enter");
            visibility.mockReturnValue("hidden");
            await act(async () => { document.dispatchEvent(new Event("visibilitychange")); });
            expect(window.__isoformMark?.playing).toBeNull();
            expect(window.__isoformMark?.timerPending).toBeNull();
        } finally {
            focus.mockRestore();
            visibility.mockRestore();
            document.dispatchEvent(new Event("visibilitychange"));
            mocks.doingNow = { kind: "watching" };
        }
    });
});
