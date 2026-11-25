import { describe, expect, it } from "vitest";
import { ALL_PLACES, chatFeatureOn, landingView, shellPlaces } from "./shell-places";
import type { FeatureSwitches } from "@/lib/wails-shim-app";

// Scope cut A2: one table decides the places and where a Chat request lands.
describe("shell places", () => {
    it("has four places with Chat off and Chat last with it on", () => {
        expect(shellPlaces(false)).toEqual(["today", "agents", "activity", "you"]);
        expect(shellPlaces(true)).toEqual(["today", "agents", "activity", "you", "chat"]);
        expect(ALL_PLACES).toEqual(shellPlaces(true));
    });

    it.each([
        ["chat", false, "today"],
        ["chat", true, "chat"],
        ["today", false, "today"],
        ["agents", false, "agents"],
        ["activity", false, "activity"],
        ["memory", false, "memory"],
        ["knowledge", false, "knowledge"],
        ["people", false, "people"],
    ] as const)("lands %s with chat %s on %s", (view, chatOn, landing) => {
        expect(landingView(view, chatOn)).toBe(landing);
    });

    it("reads the chat switch only when the build reports it on", () => {
        const base = { automations: true, playbooks: true, proactiveVoice: true, calendarHolds: true } as FeatureSwitches;
        expect(chatFeatureOn(base)).toBe(false);
        expect(chatFeatureOn({ ...base, chat: false } as FeatureSwitches)).toBe(false);
        expect(chatFeatureOn({ ...base, chat: true } as FeatureSwitches)).toBe(true);
    });
});
