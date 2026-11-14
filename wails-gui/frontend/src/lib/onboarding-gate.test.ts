import { describe, expect, it, vi } from "vitest";
import { loadOnboardingStateRaw, onboardingFinished } from "./onboarding-gate";

describe("onboardingFinished", () => {
    it("is false while onboarding was only hidden with Escape", () => {
        // Escape sets a session flag and leaves the saved state untouched.
        expect(onboardingFinished({ completed: false, neverShowAgain: false })).toBe(false);
    });

    it("is true once onboarding was finished or turned off for good", () => {
        expect(onboardingFinished({ completed: true, neverShowAgain: false })).toBe(true);
        expect(onboardingFinished({ completed: false, neverShowAgain: true })).toBe(true);
    });
});

describe("loadOnboardingStateRaw", () => {
    it("retries a failed read and returns the saved state", async () => {
        const load = vi
            .fn<() => Promise<string>>()
            .mockRejectedValueOnce(new Error("UI state storage unavailable"))
            .mockResolvedValueOnce('{"completed":true}');
        await expect(loadOnboardingStateRaw(load, { delayMs: 0 })).resolves.toBe('{"completed":true}');
        expect(load).toHaveBeenCalledTimes(2);
    });

    it("rejects after every attempt failed instead of reporting no saved state", async () => {
        const load = vi.fn<() => Promise<string>>().mockRejectedValue(new Error("locked"));
        await expect(loadOnboardingStateRaw(load, { attempts: 3, delayMs: 0 })).rejects.toThrow("locked");
        expect(load).toHaveBeenCalledTimes(3);
    });
});
