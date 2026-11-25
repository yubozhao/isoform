import { renderHook, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const read = vi.hoisted(() => vi.fn());
vi.mock("@/lib/wails-shim-app", () => ({ GetFeatureSwitches: read }));

import { FEATURE_SWITCHES_TEST_KEY, FEATURES_OFF, resetFeatureSwitchesForTest, useFeatureSwitches } from "./useFeatureSwitches";

describe("useFeatureSwitches", () => {
    beforeEach(() => {
        delete (globalThis as Record<string, unknown>)[FEATURE_SWITCHES_TEST_KEY];
        resetFeatureSwitchesForTest();
        read.mockReset();
    });

    it("starts with every turned-off feature off, then follows the Go side", async () => {
        read.mockResolvedValue({ automations: true, playbooks: false, proactiveVoice: false, calendarHolds: false, chat: false });
        const { result } = renderHook(() => useFeatureSwitches());
        expect(result.current).toEqual(FEATURES_OFF);
        await waitFor(() => expect(result.current.automations).toBe(true));
        expect(result.current.playbooks).toBe(false);
        expect(result.current.chat).toBe(false);
    });

    it("keeps everything off when the read fails", async () => {
        read.mockRejectedValue(new Error("bridge unavailable"));
        const { result } = renderHook(() => useFeatureSwitches());
        await waitFor(() => expect(read).toHaveBeenCalled());
        expect(result.current).toEqual(FEATURES_OFF);
    });
});
