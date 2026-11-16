import { act, renderHook, waitFor } from "@testing-library/react";
import { beforeEach, expect, it, vi } from "vitest";
import { useDirectHold } from "./useDirectHold";

const mocks = vi.hoisted(() => ({ settings: vi.fn(), propose: vi.fn(), hold: vi.fn(), status: vi.fn(), undo: vi.fn(), remove: vi.fn(), restore: vi.fn() }));
vi.mock("@/lib/wails-shim-app", () => ({ GetHoldSettings: mocks.settings, ProposeHold: mocks.propose, HoldTime: mocks.hold, GetHoldStatus: mocks.status, UndoHold: mocks.undo, RemoveHold: mocks.remove, UndoRemoveHold: mocks.restore }));
const slot = { start: "2026-11-02T09:00:00-08:00", end: "2026-11-02T10:00:00-08:00" };
const source = { thing: { title: "Plan", sourceKind: "brief", sourceId: "commitments.1234567890abcdef" }, preferred: slot };

beforeEach(() => {
    vi.resetAllMocks();
    mocks.settings.mockResolvedValue({ granted: true, offerHolds: true, calendarId: "calendar" });
    mocks.propose.mockResolvedValue({ id: "proposal", thing: source.thing, slots: [slot] });
});

it("does not silently hold a different time when the offer has changed", async () => {
    mocks.propose.mockResolvedValue({ id: "proposal", slots: [{ ...slot, start: "2026-11-02T10:00:00-08:00" }] });
    const { result } = renderHook(() => useDirectHold(source));
    await act(() => result.current.hold());
    expect(mocks.hold).not.toHaveBeenCalled();
    expect(result.current.failed).toBe(true);
});

it("does not submit an offer withdrawn during the availability check", async () => {
    let finish!: (value: unknown) => void;
    mocks.propose.mockReturnValue(new Promise((resolve) => { finish = resolve; }));
    const { result, rerender } = renderHook(({ offer }) => useDirectHold(offer), { initialProps: { offer: source as typeof source | null } });
    let pending!: Promise<boolean>;
    act(() => { pending = result.current.hold(); });
    await waitFor(() => expect(mocks.propose).toHaveBeenCalled());
    rerender({ offer: null });
    await act(async () => { finish({ id: "proposal", slots: [slot] }); await pending; });
    expect(mocks.hold).not.toHaveBeenCalled();
});
