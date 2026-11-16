import { useSyncExternalStore } from "react";
import { GetMobileBriefAudio, GetMobileBriefHealth } from "@/lib/wails-shim-app";
import { onPrivacyReset } from "./privacy-reset";

// Today and the menu bar share one player. A late file read cannot replace a
// newer brief or restart audio after a privacy pause.
type Playback = { briefId: string; state: "idle" | "loading" | "playing" | "failed" };
let snapshot: Playback = { briefId: "", state: "idle" };
let element: HTMLAudioElement | null = null;
let generation = 0;
// The newest brief's audio, read ahead of a click so Play starts at once. It
// is dropped with the playback it belongs to (any stop, privacy included).
let prepared: { briefId: string; audio: Promise<HTMLAudioElement> } | null = null;
const listeners = new Set<() => void>();
/** The player's state with its position, for the menu bar popover, which
 *  shows the same playing brief as Today. */
export type BriefPlaybackProgress = Playback & { position: number; duration: number };
const progressListeners = new Set<(progress: BriefPlaybackProgress) => void>();
function notifyProgress() {
    const duration = element?.duration ?? 0;
    const progress = { ...snapshot, position: element?.currentTime ?? 0, duration: Number.isFinite(duration) ? duration : 0 };
    progressListeners.forEach(listener => listener(progress));
}
export function onBriefPlaybackProgress(listener: (progress: BriefPlaybackProgress) => void) {
    progressListeners.add(listener);
    return () => { progressListeners.delete(listener); };
}
function publish(next: Playback) {
    snapshot = next;
    listeners.forEach(listener => listener());
    notifyProgress();
}
function subscribe(listener: () => void) {
    listeners.add(listener);
    return () => { listeners.delete(listener); };
}
/** The person's playback speed; it carries to the next brief that plays. */
let rate = 1;
/** Moves the brief playing (or paused) by seconds, within its length (the
 *  brief in full's ← and →, computer-brief v1.0.0 rule 2). */
export function seekBriefPlayback(briefId: string, seconds: number) {
    if (snapshot.briefId !== briefId || !element) return;
    const duration = Number.isFinite(element.duration) ? element.duration : Infinity;
    element.currentTime = Math.max(0, Math.min(duration, element.currentTime + seconds));
    notifyProgress();
}
export function setBriefPlaybackRate(next: number) {
    rate = next;
    if (element) element.playbackRate = next;
}
export function briefPlaybackRate() { return rate; }
export function useBriefPlayback(briefId: string) {
    const current = useSyncExternalStore(subscribe, () => snapshot);
    return current.briefId === briefId ? current.state : "idle";
}
export function stopBriefPlayback() {
    generation += 1;
    prepared = null;
    const stopped = element;
    element = null;
    stopped?.pause();
    stopped?.removeAttribute("src");
    stopped?.load();
    publish({ briefId: "", state: "idle" });
}
// The store outlives either control. Use the canonical identity-withdrawal
// reset, which deliberately leaves quota-only account refreshes alone.
onPrivacyReset(stopBriefPlayback);

// Always the existing source-gated audio read, never popover data.
async function loadAudio(briefId: string): Promise<HTMLAudioElement> {
    const started = performance.now();
    const data = await GetMobileBriefAudio(briefId);
    const audio = new Audio(`data:${data.mime};base64,${data.base64}`);
    audio.preload = "auto";
    audio.addEventListener("timeupdate", () => {
        if (element === audio) notifyProgress();
    });
    for (const event of ["ended", "pause", "error"] as const) {
        audio.addEventListener(event, () => {
            if (element === audio) publish({ briefId, state: event === "error" ? "failed" : "idle" });
        });
    }
    console.info(`[brief-audio] read brief=${briefId} ms=${Math.round(performance.now() - started)}`);
    return audio;
}

/** Reads a brief's audio before anyone presses Play. A failed read is not
 *  kept: the next Play reads again. */
export function prepareBriefAudio(briefId: string): void {
    if (!briefId || prepared?.briefId === briefId) return;
    if (element && snapshot.briefId === briefId) return;
    const pending = loadAudio(briefId);
    const entry = { briefId, audio: pending };
    prepared = entry;
    pending.catch(() => {
        if (prepared === entry) prepared = null;
    });
}

/** Prepares the current brief's audio (the one Today and the menu bar play).
 *  Launch and each new brief call it; it does nothing when no spoken brief exists. */
export async function prepareLatestBriefAudio(): Promise<void> {
    const epoch = generation;
    const health = await GetMobileBriefHealth();
    if (epoch !== generation || !health.audio || !health.currentBriefId) return;
    prepareBriefAudio(health.currentBriefId);
}

export async function toggleBriefPlayback(briefId: string): Promise<void> {
    const clickedAt = performance.now();
    if (snapshot.briefId === briefId && snapshot.state === "loading") return;
    if (snapshot.briefId === briefId && snapshot.state === "playing") {
        element?.pause();
        publish({ briefId, state: "idle" });
        return;
    }
    // Taken before any stop, which drops the prepared read along with the element.
    const ready = prepared?.briefId === briefId ? prepared.audio : null;
    if (snapshot.briefId !== briefId) stopBriefPlayback();
    const request = ++generation;
    publish({ briefId, state: "loading" });
    try {
        if (!element) {
            prepared = null;
            // A prepared read that failed is read again on this click.
            const audio = ready ? await ready.catch(() => loadAudio(briefId)) : await loadAudio(briefId);
            if (request !== generation) return;
            element = audio;
        }
        const playing = element;
        playing.playbackRate = rate;
        await playing.play();
        if (request !== generation) { playing.pause(); return; }
        console.info(`[brief-audio] playing brief=${briefId} ms_since_click=${Math.round(performance.now() - clickedAt)}`);
        publish({ briefId, state: "playing" });
    } catch (error) {
        if (request === generation) {
            element = null;
            publish({ briefId, state: "failed" });
            throw error;
        }
    }
}
