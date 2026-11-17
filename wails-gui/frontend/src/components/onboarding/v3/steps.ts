// Computer setup v1.0.0 (docs/handoff-2026-11-15/handoff-2026-11-15-2100-computer-setup-v1.0.0.md):
// four screens that only show, then five steps in a rail. Order lives here.

/** The four screens, phone-setup v1.1.0's ob.1 to ob.4. */
export const SETUP_V3_INTRO = [1, 2, 3, 4] as const;
export type SetupV3Intro = (typeof SETUP_V3_INTRO)[number];

/** The five steps of the rail. */
export const SETUP_V3_STEPS = ["sees", "right", "today", "agents", "phone"] as const;
export type SetupV3Step = (typeof SETUP_V3_STEPS)[number];

export type SetupV3Stage = { kind: "intro"; screen: SetupV3Intro } | { kind: "step"; step: SetupV3Step };

/** One row of step 1: the macOS prompts its Allow… asks, one at a time, in order. */
export interface SeesRow {
    id: "calendar" | "mail" | "microphone" | "notifications" | "screen";
    keys: string[];
}

// Screen comes last: granting Screen Recording makes macOS reopen Isoform.
export const SEES_V3_ROWS: SeesRow[] = [
    { id: "calendar", keys: ["calendars", "reminders", "contacts"] },
    { id: "mail", keys: ["automation_mail"] },
    { id: "microphone", keys: ["microphone", "system_audio"] },
    { id: "notifications", keys: ["notifications"] },
    { id: "screen", keys: ["accessibility", "screen_recording"] },
];

/** Where a saved stage resumes; anything unknown starts at the first screen. */
export function resumeStageV3(saved: string): SetupV3Stage | null {
    if ((SETUP_V3_STEPS as readonly string[]).includes(saved)) {
        return { kind: "step", step: saved as SetupV3Step };
    }
    // Setup v2's screens and the reopen after Screen Recording come back to step 1.
    if (saved === "sees") return { kind: "step", step: "sees" };
    if (saved === "reaches") return { kind: "step", step: "phone" };
    return null;
}
