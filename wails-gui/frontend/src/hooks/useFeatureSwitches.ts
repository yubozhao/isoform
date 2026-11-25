import { useEffect, useState } from "react";
import { GetFeatureSwitches, type FeatureSwitches } from "@/lib/wails-shim-app";

/** Every turned-off feature is off until the Go side says otherwise, so an
 *  entry never flashes in before the read. */
export const FEATURES_OFF: FeatureSwitches = { automations: false, playbooks: false, proactiveVoice: false, calendarHolds: false, chat: false, briefExtras: false, activityExtras: false };

/** Tests answer every read with this (test-setup.ts turns every feature on;
 *  a switch's guard tests turn it off). Never set in the app. */
export const FEATURE_SWITCHES_TEST_KEY = "__isoformFeatureSwitchesForTest";

function testSwitches(): FeatureSwitches | null {
    const value = (globalThis as Record<string, unknown>)[FEATURE_SWITCHES_TEST_KEY];
    return value && typeof value === "object" ? { ...FEATURES_OFF, ...(value as Partial<FeatureSwitches>) } : null;
}

let cached: FeatureSwitches | null = null;
let pending: Promise<FeatureSwitches> | null = null;

function loadFeatureSwitches(): Promise<FeatureSwitches> {
    const forTest = testSwitches();
    if (forTest) return Promise.resolve(forTest);
    if (cached) return Promise.resolve(cached);
    if (!pending) {
        pending = Promise.resolve()
            .then(() => GetFeatureSwitches())
            .then((value) => {
                cached = { ...FEATURES_OFF, ...(value ?? {}) };
                return cached;
            })
            .catch(() => {
                pending = null;
                return FEATURES_OFF;
            });
    }
    return pending;
}

/** The switches are fixed for the run of the app (an environment setting),
 *  so they are read once and shared. */
export function useFeatureSwitches(): FeatureSwitches {
    const [switches, setSwitches] = useState<FeatureSwitches>(() => testSwitches() ?? cached ?? FEATURES_OFF);
    useEffect(() => {
        let alive = true;
        void loadFeatureSwitches().then((value) => {
            if (alive) setSwitches(value);
        });
        return () => {
            alive = false;
        };
    }, []);
    return switches;
}

/** Activity's extras (scope cut B3): the Screen and Agents tabs, the
 *  stitched list, agent runs, the topic line, Not this topic, Remind me and
 *  the 14-day bars. Off unless YANSU_FEATURES turns them on. */
export function useActivityExtras(): boolean {
    return useFeatureSwitches().activityExtras === true;
}

/** Test seam: forget the shared read. */
export function resetFeatureSwitchesForTest(): void {
    cached = null;
    pending = null;
}

/** Chat is hidden by the scope cut of November 2026 (plan section 6, switch
 *  `chat`, off by default). Until the switch reaches FeatureSwitches, a
 *  missing field reads as off, so nothing shows a way into Chat. */
export function useChatOn(): boolean {
    const switches = useFeatureSwitches() as FeatureSwitches & { chat?: boolean };
    return switches.chat === true;
}
