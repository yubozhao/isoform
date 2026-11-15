import { useEffect, useState } from "react";
import { GetFeatureSwitches, type FeatureSwitches } from "@/lib/wails-shim-app";

/** Every turned-off feature is off until the Go side says otherwise, so an
 *  entry never flashes in before the read. */
export const FEATURES_OFF: FeatureSwitches = { automations: false, playbooks: false, proactiveVoice: false, calendarHolds: false };

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

/** Test seam: forget the shared read. */
export function resetFeatureSwitchesForTest(): void {
    cached = null;
    pending = null;
}
