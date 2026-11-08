import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";
import type { MobileConnectionStatus } from "@/lib/wails-shim-app";
import type { SetupAgentProbe, SetupPermissions } from "@/lib/setup-backend";
import { blocksFromDayShape } from "./scenes/NoticesScene";
import { cardStates } from "./scenes/YourAIScene";
import { rowStates, sequenceDone, SEES_ROWS } from "./scenes/SeesScene";
import { cardFor } from "./scenes/ReachesScene";
import { Run } from "./motion";

const perms = (answers: SetupPermissions["answers"], running = false): SetupPermissions => ({
    answers,
    running,
    resumeStep: "sees",
    next: answers.length,
});

describe("Notices day strip", () => {
    it("places the day's timed events on the 7:00 to 19:00 strip", () => {
        const at = (h: number, m = 0) => new Date(2026, 9, 28, h, m).toISOString();
        expect(
            blocksFromDayShape([
                { start: at(9), end: at(10) },
                { start: at(6), end: at(7, 30) },
                { start: at(12), end: at(12, 5) },
                { start: at(8), end: at(17), allDay: true },
                { start: at(20), end: at(21) },
            ]),
        ).toEqual([
            { left: 60, width: 30 },
            { left: 0, width: 15 },
            { left: 150, width: 8 },
        ]);
    });
});

describe("Your AI cards", () => {
    it("reads signed in, installed and missing assistants, with local progress", () => {
        const probe = {
            agents: [
                { id: "claude", installed: true, signedIn: false },
                { id: "codex", installed: false, signedIn: false },
            ],
        } as unknown as SetupAgentProbe;
        expect(cardStates(probe, {})).toEqual({ claude: "signin", codex: "install" });
        expect(cardStates(probe, { claude: "waiting", codex: "installing" })).toEqual({
            claude: "waiting",
            codex: "installing",
        });
        const signedIn = {
            agents: [{ id: "claude", installed: true, signedIn: true }],
        } as unknown as SetupAgentProbe;
        // A sign-in finished in Terminal is tested before the card turns on.
        expect(cardStates(signedIn, { claude: "waiting" }).claude).toBe("testing");
        expect(cardStates(signedIn, {}).claude).toBe("on");
        expect(cardStates(signedIn, { claude: "testFailed" }).claude).toBe("testFailed");
        expect(cardStates(probe, {}, { claude: true }).claude).toBe("on");
        expect(cardStates(null, {})).toEqual({ claude: "install", codex: "install" });
    });
});

describe("Sees rows", () => {
    it("keeps unsupported Windows integrations out of the available rows", () => {
        const p = perms(SEES_ROWS.flatMap((row) => row.keys.map((key) => ({
            key, state: row.id === "day" ? "unsupported" as const : "on" as const,
        }))));
        p.platform = "windows";
        expect(rowStates(p)).toEqual({ screen: "on", calls: "on", day: "unsupported", apps: "on" });
        expect(sequenceDone(p)).toBe(true);
    });

    it("groups the prompts into four rows and asks Screen Recording last", () => {
        expect(SEES_ROWS.map((r) => r.id)).toEqual(["screen", "calls", "day", "apps"]);
        expect(SEES_ROWS.flatMap((r) => r.keys)).toHaveLength(8);
    });

    it("turns a row On only when every prompt in it is answered", () => {
        const p = perms([
            { key: "calendars", state: "on" },
            { key: "reminders", state: "on" },
            { key: "contacts", state: "on" },
            { key: "automation_mail", state: "unavailable" },
            { key: "microphone", state: "on" },
            { key: "system_audio", state: "denied" },
            { key: "accessibility", state: "off" },
            { key: "screen_recording", state: "onAfterReopen" },
        ]);
        expect(rowStates(p)).toEqual({ screen: "on", calls: "denied", day: "on", apps: "off" });
        expect(rowStates(null)).toEqual({ screen: "off", calls: "off", day: "off", apps: "off" });
    });

    it("finishes the sequence once nothing is left unanswered", () => {
        const answered = SEES_ROWS.flatMap((r) => r.keys).map((key) => ({ key, state: "on" as const }));
        expect(sequenceDone(perms(answered))).toBe(true);
        expect(sequenceDone(perms(answered, true))).toBe(false);
        expect(sequenceDone(perms([...answered.slice(1), { key: "calendars", state: "off" }]))).toBe(false);
        expect(sequenceDone(perms([]))).toBe(false);
    });
});

describe("Reaches you card", () => {
    const base = { connectionCode: "isoform://pair#invite", pendingPairing: null, devices: [] };
    it("shows the code, a phone asking to pair, or the paired phone", () => {
        expect(cardFor(base as unknown as MobileConnectionStatus)).toEqual({
            kind: "code",
            code: "isoform://pair#invite",
        });
        expect(
            cardFor({
                ...base,
                pendingPairing: { pairingId: "p1", deviceName: "Maya's iPhone", code: "123456" },
            } as unknown as MobileConnectionStatus),
        ).toEqual({ kind: "pending", pairingId: "p1", phone: "Maya's iPhone", code: "123456" });
        expect(
            cardFor({ ...base, devices: [{ deviceName: "Alex's iPhone" }] } as unknown as MobileConnectionStatus),
        ).toEqual({ kind: "paired", phone: "Alex's iPhone" });
        expect(cardFor(null)).toEqual({ kind: "code", code: "" });
    });
});

describe("Run", () => {
    it("finishes tweens at once under Reduce Motion and stops after cancel", async () => {
        const run = new Run(true);
        const seen: number[] = [];
        await run.tween(500, (p) => seen.push(p));
        expect(seen).toEqual([1]);
        run.cancel();
        expect(run.alive).toBe(false);
    });
});

describe("Native QA guards (2026-10-28)", () => {
    const src = (file: string) => readFileSync(resolve(process.cwd(), "src", file), "utf8");

    it("keeps the Your AI line under the cards on one line, so the zone chip clears the subtitle", () => {
        // With no assistant signed in, a two-line line made the screen 17 px taller than
        // the 600 x 560 window and the "On this computer" chip ran into the subtitle.
        expect(src("components/onboarding/v2/scenes/YourAIScene.tsx")).toContain("fine wide");
        const rule = src("components/onboarding/v2/setup-v2.css").match(/\.sv2 \.fine\.wide \{([^}]*)\}/);
        expect(rule).not.toBeNull();
        expect(rule![1]).toMatch(/width:\s*440px/);
        expect(rule![1]).toMatch(/margin-inline:\s*-60px/);
    });

    it("opens the main window on Chat in the sidebar when setup finishes", () => {
        const app = src("App.tsx");
        const start = app.indexOf("const handleOnboardingFinish");
        const body = app.slice(start, app.indexOf("const handleOnboardingSkipSession"));
        expect(body).toContain('handleSetAppMode("chat")');
    });
});
