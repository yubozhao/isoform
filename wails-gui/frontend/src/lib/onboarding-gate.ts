import type { OnboardingState } from "@/types";

/**
 * onboardingFinished reports whether onboarding is over for good: finished,
 * or turned off with "never show again". Escape hides onboarding for the session
 * only and does not count, because onboarding comes back on the next launch.
 * The main chat's setup waits for this, so its first message carries what
 * onboarding asked instead of asking it again.
 */
export function onboardingFinished(
    state: Pick<OnboardingState, "completed" | "neverShowAgain">,
): boolean {
    return state.completed || state.neverShowAgain;
}

/**
 * loadOnboardingStateRaw reads the saved onboarding state, retrying a failed
 * read a few times while the "preparing" screen shows. It rejects only when
 * every attempt failed. A failed read must never count as "no saved state":
 * that showed onboarding again to people who had finished it.
 */
export async function loadOnboardingStateRaw(
    load: () => Promise<string>,
    { attempts = 6, delayMs = 500 }: { attempts?: number; delayMs?: number } = {},
): Promise<string> {
    let lastError: unknown;
    for (let attempt = 0; attempt < attempts; attempt++) {
        if (attempt > 0) await new Promise((resolve) => setTimeout(resolve, delayMs));
        try {
            return await load();
        } catch (err) {
            lastError = err;
        }
    }
    throw lastError;
}
