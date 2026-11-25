import "@testing-library/jest-dom/vitest";
import { beforeEach } from "vitest";

// The rework's turned-off features stay testable: every test starts with them
// on, and a switch's guard tests turn it off (hooks/useFeatureSwitches.ts,
// feature_switches.go). The key matches FEATURE_SWITCHES_TEST_KEY.
beforeEach(() => {
    (globalThis as Record<string, unknown>).__isoformFeatureSwitchesForTest = { automations: true, playbooks: true, proactiveVoice: true, calendarHolds: true, chat: true, briefExtras: true, activityExtras: true };
});
