import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { installPageQueryEvents, resetPageQueries } from "@/lib/page-queries";
import { resetRenderTierStores } from "@/lib/privacy-reset";
import { pageQueryCache } from "@/lib/query-cache";
import type { TodayBeforeYouTalk } from "@/lib/wails-shim-app";
import { useTodayBeforeYouTalk } from "./useTodayBeforeYouTalk";

// Guards for the privacy blocker in desk-out/review-pr227-b1532902.md: Forget
// (`memory:withdrawn`), app protection (`sensitive-apps:updated`), Closed Eyes
// (`closed-eyes:changed`) and identity withdrawal (`model-accounts:updated`)
// must withdraw the rendered card in the event's own turn, retire in-flight
// reads and dismiss completions, and leave recovery to the gated reread. An
// ordinary quota poll carries the same signed-in identity and keeps the card.
const fixture = vi.hoisted(() => ({
    events: new Map<string, Set<(payload?: unknown) => unknown>>(),
    get: vi.fn(),
    dismiss: vi.fn(),
    status: vi.fn(),
}));
vi.mock("../../wailsjs/runtime/runtime", () => ({
    EventsOn: (name: string, handler: (payload?: unknown) => unknown) => {
        const set = fixture.events.get(name) ?? new Set();
        set.add(handler);
        fixture.events.set(name, set);
        return () => set.delete(handler);
    },
}));

const originalGo = (window as any).go;
const meeting: TodayBeforeYouTalk = {
    meetingId: "next", meetingTitle: "Coffee", startAt: Date.now() + 60_000, endAt: Date.now() + 120_000,
    personId: "mei", personName: "Mei Chen", youOwe: "The Halcyon launch memo outline.",
};
const ACCOUNT_A = { provider: "claude", kind: "subscription", name: "Bo", on: true, windows: [{ used: 10 }] };
const ACCOUNT_B = { provider: "kimi", kind: "api", name: "Work", on: true };
const privacyEvents = ["permissions:changed", "closed-eyes:changed", "background-listening:changed", "sensitive-apps:updated", "memory:withdrawn"];

function deferred<T>() {
    let resolve: (value: T) => void = () => {};
    const promise = new Promise<T>((done) => { resolve = done; });
    return { promise, resolve };
}
async function emit(name: string, payload?: unknown) {
    await act(async () => { await Promise.all([...(fixture.events.get(name) ?? [])].map((handler) => handler(payload))); });
}

beforeEach(async () => {
    resetPageQueries();
    fixture.get.mockReset().mockResolvedValue(null);
    fixture.dismiss.mockReset().mockResolvedValue(undefined);
    fixture.status.mockReset().mockResolvedValue({ accounts: [ACCOUNT_A, ACCOUNT_B] });
    (window as any).go = { main: { App: { GetTodayBeforeYouTalk: fixture.get, DismissTodayBeforeYouTalk: fixture.dismiss, GetModelAccountsStatus: fixture.status } } };
    resetRenderTierStores();
    // Let both account gates establish their baselines, so only a real withdrawal resets.
    installPageQueryEvents();
    await emit("model-accounts:updated", { accounts: [ACCOUNT_A, ACCOUNT_B] });
    await act(async () => { await new Promise((resolve) => setTimeout(resolve, 30)); });
});
afterEach(() => {
    cleanup();
    resetPageQueries();
    (window as any).go = originalGo;
});

describe("useTodayBeforeYouTalk privacy withdrawal", () => {
    it.each(privacyEvents)("clears the rendered card synchronously on %s while the gated reread is held", async (event) => {
        fixture.get.mockResolvedValueOnce(meeting);
        const hook = renderHook(() => useTodayBeforeYouTalk(true));
        await waitFor(() => expect(hook.result.current.card?.meetingId).toBe("next"));
        const reread = deferred<TodayBeforeYouTalk | null>();
        fixture.get.mockReturnValueOnce(reread.promise);
        await emit(event);
        expect(hook.result.current.card).toBeNull();
        expect(pageQueryCache.peek("GetTodayBeforeYouTalk")).toBeUndefined();
        // The reread stays behind the backend publication gate and withholds.
        await act(async () => { reread.resolve(null); });
        expect(hook.result.current.card).toBeNull();
    });

    it.each(privacyEvents)("cannot publish an in-flight pre-withdrawal read after %s", async (event) => {
        const held = deferred<TodayBeforeYouTalk | null>();
        fixture.get.mockReturnValueOnce(held.promise);
        const hook = renderHook(() => useTodayBeforeYouTalk(true));
        await waitFor(() => expect(fixture.get).toHaveBeenCalledTimes(1));
        await emit(event);
        await act(async () => { held.resolve(meeting); });
        expect(hook.result.current.card).toBeNull();
        await waitFor(() => expect(fixture.get.mock.calls.length).toBeGreaterThan(1));
        expect(hook.result.current.card).toBeNull();
    });

    it("stays empty when the gated reread fails after a withdrawal", async () => {
        fixture.get.mockResolvedValueOnce(meeting);
        const hook = renderHook(() => useTodayBeforeYouTalk(true));
        await waitFor(() => expect(hook.result.current.card?.meetingId).toBe("next"));
        fixture.get.mockRejectedValueOnce(new Error("gated read failed"));
        await emit("closed-eyes:changed");
        expect(hook.result.current.card).toBeNull();
        await waitFor(() => expect(fixture.get.mock.calls.length).toBeGreaterThan(1));
        expect(hook.result.current.card).toBeNull();
    });

    it("discards the retained cache while hidden without reading, and re-reads on reopen", async () => {
        fixture.get.mockResolvedValueOnce(meeting);
        const hook = renderHook(({ enabled }) => useTodayBeforeYouTalk(enabled), { initialProps: { enabled: true } });
        await waitFor(() => expect(hook.result.current.card?.meetingId).toBe("next"));
        act(() => hook.rerender({ enabled: false }));
        expect(hook.result.current.card).toBeNull();
        expect(fixture.get).toHaveBeenCalledTimes(1);
        await emit("closed-eyes:changed");
        expect(pageQueryCache.peek("GetTodayBeforeYouTalk")).toBeUndefined();
        expect(fixture.get).toHaveBeenCalledTimes(1);
        // Reopen shows only the gated reread, never the retained value.
        const reopened = { ...meeting, meetingId: "reopened", personName: "Dana" };
        fixture.get.mockResolvedValueOnce(reopened);
        act(() => hook.rerender({ enabled: true }));
        await waitFor(() => expect(hook.result.current.card?.meetingId).toBe("reopened"));
    });

    it.each([
        ["removed", { accounts: [ACCOUNT_A] }],
        ["disabled", { accounts: [ACCOUNT_A, { ...ACCOUNT_B, on: false }] }],
    ])("clears the card synchronously when an account is %s", async (_kind, payload) => {
        fixture.get.mockResolvedValueOnce(meeting);
        const hook = renderHook(() => useTodayBeforeYouTalk(true));
        await waitFor(() => expect(hook.result.current.card?.meetingId).toBe("next"));
        const reread = deferred<TodayBeforeYouTalk | null>();
        fixture.get.mockReturnValueOnce(reread.promise);
        await emit("model-accounts:updated", payload);
        expect(hook.result.current.card).toBeNull();
        expect(pageQueryCache.peek("GetTodayBeforeYouTalk")).toBeUndefined();
        // The gated reread withholds while the identity is gone.
        await act(async () => { reread.resolve(null); });
        expect(hook.result.current.card).toBeNull();
    });

    it("keeps the card through an ordinary quota refresh", async () => {
        fixture.get.mockResolvedValueOnce(meeting);
        const hook = renderHook(() => useTodayBeforeYouTalk(true));
        await waitFor(() => expect(hook.result.current.card?.meetingId).toBe("next"));
        const shown = hook.result.current.card;
        await emit("model-accounts:updated", { accounts: [{ ...ACCOUNT_A, windows: [{ used: 99 }] }, ACCOUNT_B] });
        expect(hook.result.current.card).toBe(shown);
        expect(fixture.get).toHaveBeenCalledTimes(1);
    });

    it("repopulates only through the gated reread when observation resumes", async () => {
        fixture.get.mockResolvedValueOnce(meeting);
        const hook = renderHook(() => useTodayBeforeYouTalk(true));
        await waitFor(() => expect(hook.result.current.card?.meetingId).toBe("next"));
        await emit("closed-eyes:changed");
        expect(hook.result.current.card).toBeNull();
        fixture.get.mockResolvedValue(meeting);
        await emit("closed-eyes:changed");
        await waitFor(() => expect(hook.result.current.card?.meetingId).toBe("next"));
    });

    it("a late dismiss completion cannot restore the card after a withdrawal", async () => {
        fixture.get.mockResolvedValueOnce(meeting);
        const hook = renderHook(() => useTodayBeforeYouTalk(true));
        await waitFor(() => expect(hook.result.current.card?.meetingId).toBe("next"));
        const heldDismiss = deferred<void>();
        fixture.dismiss.mockReturnValueOnce(heldDismiss.promise);
        let dismissal = Promise.resolve();
        await act(async () => {
            dismissal = hook.result.current.dismiss("next");
            await Promise.resolve();
        });
        await emit("closed-eyes:changed");
        expect(hook.result.current.card).toBeNull();
        await act(async () => {
            heldDismiss.resolve();
            await dismissal;
        });
        expect(fixture.dismiss).toHaveBeenCalledWith("next");
        expect(hook.result.current.card).toBeNull();
    });
});
