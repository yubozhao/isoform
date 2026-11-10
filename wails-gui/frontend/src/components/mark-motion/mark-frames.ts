// The mark's motion as frames, from mark-frames.json (made by
// scripts/mark-frames.mjs from design's cut kit, mark motion v1.0.1). Each
// frame is the whole state of the mark, a twelfth of a second apart, so the
// player needs nothing that computes timing.
import data from "./mark-frames.json";

/** The whole state of the mark in one frame, in the kit's units. */
export interface MarkState {
    S: number[];
    wave: number;
    phase: number;
    flip: number;
    sw: number;
    Sg: number[];
    tail: number[];
    hook: number[];
    all: number[];
    face: number[];
    Fp: number[];
    I: number[];
    Ip: number[];
    serif: number;
    O: number[];
    B: number;
    A: number;
    Ap: number[];
    ride: number;
    clip: number;
    th: number | null;
}

export type HeldPose = "working" | "listening" | "paused";
export type SequenceName =
    | "working.enter" | "working.loop" | "working.leave"
    | "listening.enter" | "listening.loop" | "listening.loopLarge" | "listening.leave"
    | "paused.enter" | "paused.peek" | "paused.leave"
    | "blink" | "hello" | "needs" | "briefMorning" | "briefEvening" | "sent" | "retry";

interface FrameData {
    fps: number;
    rest: MarkState;
    poses: Record<HeldPose, Partial<MarkState>>;
    seq: Record<SequenceName, Partial<MarkState>[]>;
}

const FRAMES = data as unknown as FrameData;

export const FRAME_MS = 1000 / FRAMES.fps;
export const REST: MarkState = FRAMES.rest;

const expanded = new Map<SequenceName, MarkState[]>();

/** A sequence's frames, expanded once from their stored changes. */
export function sequence(name: SequenceName): MarkState[] {
    const known = expanded.get(name);
    if (known) return known;
    let prev = REST;
    const frames = FRAMES.seq[name].map((change) => (prev = { ...prev, ...change }));
    expanded.set(name, frames);
    return frames;
}

/** A held state's still pose; rest is the logo. */
export function pose(name: HeldPose | "rest"): MarkState {
    return name === "rest" ? REST : { ...REST, ...FRAMES.poses[name] };
}
