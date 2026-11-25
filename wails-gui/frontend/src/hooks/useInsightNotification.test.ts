import { describe, expect, it, vi, beforeEach } from "vitest";
import { renderHook } from "@testing-library/react";

type Handler = (event: unknown) => void;
const handlers: Record<string, Handler> = {};

vi.mock("../../wailsjs/runtime/runtime", () => ({
    EventsOn: vi.fn((name: string, handler: Handler) => {
        handlers[name] = handler;
        return () => {
            delete handlers[name];
        };
    }),
}));
vi.mock("@/lib/notify", () => ({ notifyImportant: vi.fn() }));

import { notifyImportant } from "@/lib/notify";
import { useInsightNotification } from "./useInsightNotification";

// Speech is no longer the frontend's: notifyImportant goes through NotifyUser,
// which says the title when Read notifications aloud allows it
// (notification_speech.go). A notice that isn't shown is never said.
describe("useInsightNotification", () => {
    beforeEach(() => {
        vi.mocked(notifyImportant).mockClear();
    });

    it("stays silent for meeting notes from a recorded call", () => {
        renderHook(() => useInsightNotification());
        handlers["insight:saved"]({ kind: "knowledge", id: "meeting-1", title: "Meeting notes: Halcyon Meet, Oct 15 14:00", source: "meeting" });
        expect(notifyImportant).not.toHaveBeenCalled();
    });

    it("still announces other new knowledge", () => {
        renderHook(() => useInsightNotification());
        handlers["insight:saved"]({ kind: "knowledge", id: "k-1", title: "How the release works", source: "triage" });
        expect(notifyImportant).toHaveBeenCalledTimes(1);
        expect(vi.mocked(notifyImportant).mock.calls[0][1]).toBe("Open Isoform for the latest update.");
        expect(JSON.stringify(vi.mocked(notifyImportant).mock.calls)).not.toContain("How the release works");
    });
});
