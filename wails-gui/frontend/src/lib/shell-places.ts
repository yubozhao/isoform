import type { FeatureSwitches } from "@/lib/wails-shim-app";

// The shell's places after the scope cut (docs/plans/scope-cut-2026-11.md,
// sections 1 and 6, A2): Today, Agents, Activity and You. Chat comes back
// only while the `chat` switch is on (YANSU_FEATURES=chat, the rollback),
// after You. One table decides the sidebar, the place keys, Find's Go to and
// where a Chat destination lands, so they can never disagree.

export type Place = "today" | "chat" | "agents" | "activity" | "you";

/** Every place, in sidebar order. Chat is last: with the switch on it comes
 *  back without moving the other places or their keys. */
export const ALL_PLACES: readonly Place[] = ["today", "agents", "activity", "you", "chat"];

/** The places a person can open: Chat only while its switch is on. */
export function shellPlaces(chatOn: boolean): Place[] {
    return ALL_PLACES.filter((place) => place !== "chat" || chatOn);
}

/** Whether the rollback switch for Chat is on. Reads the `chat` switch that
 *  scope-cut A1 adds; off when the build does not report it. */
export function chatFeatureOn(switches: FeatureSwitches): boolean {
    return (switches as FeatureSwitches & { chat?: boolean }).chat === true;
}

/** Where a request for a view lands: Chat is Today while its switch is off,
 *  so an old deep link, a saved mode or a leftover caller never opens a page
 *  that is gone. Every other view lands where it asked. */
export function landingView<M extends string>(view: M, chatOn: boolean): M | "today" {
    if (view === "chat" && !chatOn) return "today";
    return view;
}
