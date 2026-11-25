import { act, renderHook } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import "@/i18n";

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
vi.mock("@/lib/notify", () => ({ notifyToast: vi.fn() }));
vi.mock("../lib/tts-player", () => ({ speakOnce: vi.fn(() => Promise.resolve()) }));

import { notifyToast } from "@/lib/notify";
import { speakOnce } from "../lib/tts-player";
import { useHandoffNotification } from "./useHandoffNotification";

// Read notifications aloud (answers v1.5.0 S22): Go decides (on, not quiet
// hours) and sends each notification's title; the hook only says it.
describe("useHandoffNotification", () => {
    beforeEach(() => {
        vi.clearAllMocks();
        for (const name of Object.keys(handlers)) delete handlers[name];
    });

    it("says each notification Go sends to be spoken, and nothing else", () => {
        const { unmount } = renderHook(() => useHandoffNotification());
        act(() => handlers["notification:speak"]({ title: "Your brief is ready" }));
        expect(speakOnce).toHaveBeenCalledWith("Your brief is ready");
        act(() => handlers["notification:speak"]({ title: "  " }));
        expect(speakOnce).toHaveBeenCalledTimes(1);
        unmount();
        expect(handlers["notification:speak"]).toBeUndefined();
    });

    it("no longer speaks a new hand-off, which is not a notification, and raises no toast", () => {
        renderHook(() => useHandoffNotification());
        expect(handlers["handoff:created"]).toBeUndefined();
        expect(notifyToast).not.toHaveBeenCalled();
    });
});
