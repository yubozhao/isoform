// The mark's state machine: every rule of mark motion v1.0.1 (handoff
// docs/handoff-2026-11-09/handoff-2026-11-09-1659-mark-motion-v1.0.1.md) about
// what plays and when lives here, and nowhere else. It owns one timer at most:
// a frame step while something plays, or the wait for the next blink at rest
// (rules 2 and 3). It never decides a state (rule 7): it is told the state the
// app's one status line shows.
import { FRAME_MS, pose, sequence, type HeldPose, type MarkState, type SequenceName } from "./mark-frames";

export type MarkHeld = "rest" | HeldPose;
export type MarkEvent = "needs" | "brief" | "retry" | "sent";
export interface MarkEventOptions {
    /** For the brief: the evening brief plays the sunset. */
    evening?: boolean;
}

/** Rule 12: when two wait, this is the order. */
const PRIORITY: Record<MarkEvent, number> = { needs: 0, brief: 1, retry: 2, sent: 3 };
/** Rule 12: a wait longer than this is dropped. */
export const WAIT_LIMIT_MS = 10_000;
/** Rule 13: each kind plays at most once a minute. */
export const ONCE_PER_MS = 60_000;
/** Rule 6: a page change blinks at most this often. */
export const PAGE_BLINK_MS = 2_000;
/** Rule 2: one blink every 20 to 40 seconds at rest. */
export const REST_BLINK_MIN_MS = 20_000;
export const REST_BLINK_SPAN_MS = 20_000;

export interface MarkMachineDeps {
    paint: (st: MarkState) => void;
    setTimer?: (fn: () => void, ms: number) => unknown;
    clearTimer?: (handle: unknown) => void;
    now?: () => number;
    random?: () => number;
    /** Told each sequence as it starts (tests and the harness). */
    onPlay?: (name: SequenceName) => void;
}

export interface MarkMachineStart {
    held?: MarkHeld;
    visible?: boolean;
    reducedMotion?: boolean;
}

interface Waiting {
    kind: MarkEvent;
    evening: boolean;
    at: number;
}

interface Run {
    name: SequenceName;
    frames: MarkState[];
    index: number;
    then: () => void;
}

const LOOP: Partial<Record<MarkHeld, SequenceName>> = { working: "working.loop", listening: "listening.loop" };

export class MarkMachine {
    private readonly paint: (st: MarkState) => void;
    private readonly setTimer: (fn: () => void, ms: number) => unknown;
    private readonly clearTimer: (handle: unknown) => void;
    private readonly now: () => number;
    private readonly random: () => number;
    private readonly onPlay?: (name: SequenceName) => void;

    /** The state the app's line shows. */
    private target: MarkHeld;
    /** The state the drawing is in: entered, or posed while hidden. */
    private shown: MarkHeld;
    private visible: boolean;
    private reduced: boolean;
    private run: Run | null = null;
    private timer: unknown = null;
    private timerKind: "frame" | "blink" | null = null;
    private waiting: Waiting[] = [];
    private readonly lastPlayed = new Map<MarkEvent, number>();
    private lastPageBlink = -Infinity;
    /** Rule 15: needs you and the brief, held for after the call. */
    private afterCall: Waiting[] = [];
    /** Rule 16: needs you and the brief, noticed while the window was away. */
    private whileAway: Waiting[] = [];
    private disposed = false;

    constructor(deps: MarkMachineDeps, start: MarkMachineStart = {}) {
        this.paint = deps.paint;
        this.setTimer = deps.setTimer ?? ((fn, ms) => setTimeout(fn, ms));
        this.clearTimer = deps.clearTimer ?? ((h) => clearTimeout(h as ReturnType<typeof setTimeout>));
        this.now = deps.now ?? (() => Date.now());
        this.random = deps.random ?? Math.random;
        this.onPlay = deps.onPlay;
        this.target = start.held ?? "rest";
        this.shown = this.target;
        this.visible = start.visible ?? true;
        this.reduced = start.reducedMotion ?? false;
        this.paint(pose(this.shown));
        this.advance();
    }

    /** What is playing now, for tests and the harness. */
    get playing(): SequenceName | null {
        return this.run?.name ?? null;
    }

    /** The held state the drawing is in. */
    get held(): MarkHeld {
        return this.shown;
    }

    /** Whether a timer is pending: a frame step or the next rest blink. */
    get timerPending(): "frame" | "blink" | null {
        return this.timerKind;
    }

    /** The state from the app's one line (rule 7). */
    setHeld(next: MarkHeld): void {
        if (this.disposed || next === this.target) return;
        const wasCall = this.target === "listening";
        this.target = next;
        if (next === "listening") {
            // Rule 15: a call is when a screen is most likely shared. A
            // gesture stops; needs you and the brief wait for the call's end.
            for (const w of this.waiting) this.holdForCall(w);
            this.waiting = [];
            if (this.run && !this.run.name.includes(".")) {
                this.stopRun();
                this.shown = "rest";
                this.paint(pose("rest"));
            }
        } else if (wasCall && this.afterCall.length) {
            this.enqueueAll(this.afterCall);
            this.afterCall = [];
        }
        if (!this.canMove()) {
            this.shown = next;
            this.paint(pose(next));
            return;
        }
        if (!this.run) this.advance();
    }

    /** One of the four events (rules 5, 12, 13, 15, 16). */
    event(kind: MarkEvent, opts: MarkEventOptions = {}): void {
        if (this.disposed || this.reduced) return;
        const w: Waiting = { kind, evening: !!opts.evening, at: this.now() };
        if (!this.visible) {
            if (kind === "needs" || kind === "brief") this.keep(this.whileAway, w);
            return;
        }
        if (this.target === "listening") {
            this.holdForCall(w);
            return;
        }
        if (!this.enqueue(w)) return;
        if (!this.run || this.timerKind === "blink") this.kick();
    }

    /** Rule 6: a page change gets one blink, at most once every two seconds. */
    pageChange(): void {
        if (this.disposed || !this.canMove()) return;
        const now = this.now();
        if (now - this.lastPageBlink < PAGE_BLINK_MS) return;
        if (this.run || this.shown !== "rest" || this.target !== "rest" || this.ready()) return;
        this.lastPageBlink = now;
        this.cancelTimer();
        this.play("blink", () => this.advance());
    }

    /** Rule 10: pointing at the mark while paused plays the peek. At rest the
     *  double blink is the wordmark's own (CSS), as today. */
    hover(): void {
        if (this.disposed || !this.canMove() || this.run) return;
        if (this.shown === "paused" && this.target === "paused") this.play("paused.peek", () => this.advance());
    }

    /** Rule 16: the window in front, or hidden or behind. */
    setVisible(visible: boolean): void {
        if (this.disposed || visible === this.visible) return;
        this.visible = visible;
        if (!visible) {
            this.stopRun();
            this.cancelTimer();
            this.waiting = [];
            this.shown = this.target;
            this.paint(pose(this.target));
            return;
        }
        const away = this.whileAway;
        this.whileAway = [];
        if (this.reduced) return;
        if (this.target === "listening") for (const w of away) this.holdForCall({ ...w, at: this.now() });
        else this.enqueueAll(away);
        this.advance();
    }

    /** Rule 18: with reduced motion on, nothing plays and nothing blinks; a
     *  state's pose still shows, changed in one step. */
    setReducedMotion(reduced: boolean): void {
        if (this.disposed || reduced === this.reduced) return;
        this.reduced = reduced;
        if (reduced) {
            this.stopRun();
            this.cancelTimer();
            this.waiting = [];
            this.afterCall = [];
            this.whileAway = [];
            this.shown = this.target;
            this.paint(pose(this.target));
            return;
        }
        this.advance();
    }

    dispose(): void {
        this.disposed = true;
        this.stopRun();
        this.cancelTimer();
    }

    // ---- internals ----

    private canMove(): boolean {
        return this.visible && !this.reduced;
    }

    private holdForCall(w: Waiting): void {
        if (w.kind === "needs" || w.kind === "brief") this.keep(this.afterCall, w);
    }

    /** Several of a kind play once: keep one of each, the latest options. */
    private keep(list: Waiting[], w: Waiting): void {
        const i = list.findIndex((x) => x.kind === w.kind);
        if (i >= 0) list[i] = { ...w, at: list[i].at };
        else list.push(w);
        list.sort((a, b) => PRIORITY[a.kind] - PRIORITY[b.kind]);
    }

    /** Rule 12 and 13: one more can wait; the higher kind keeps the place. */
    private enqueue(w: Waiting): boolean {
        const last = this.lastPlayed.get(w.kind);
        if (last !== undefined && w.at - last < ONCE_PER_MS) return false;
        if (this.run?.name === sequenceFor(w)) return false;
        this.expire();
        const same = this.waiting.find((x) => x.kind === w.kind);
        if (same) {
            same.evening = w.evening;
            return true;
        }
        this.waiting.push(w);
        this.waiting.sort((a, b) => PRIORITY[a.kind] - PRIORITY[b.kind]);
        if (this.waiting.length > 1) this.waiting.length = 1;
        return this.waiting[0] === w;
    }

    /** After the call or on return both kinds may be owed: the first plays at
     *  once, the second is the one that waits. */
    private enqueueAll(list: Waiting[]): void {
        const now = this.now();
        for (const w of list) {
            const last = this.lastPlayed.get(w.kind);
            if (last !== undefined && now - last < ONCE_PER_MS) continue;
            if (!this.waiting.some((x) => x.kind === w.kind)) this.waiting.push({ ...w, at: now });
        }
        this.waiting.sort((a, b) => PRIORITY[a.kind] - PRIORITY[b.kind]);
        this.waiting.length = Math.min(this.waiting.length, 2);
    }

    private expire(): void {
        const now = this.now();
        this.waiting = this.waiting.filter((w) => now - w.at <= WAIT_LIMIT_MS);
    }

    private ready(): Waiting | null {
        this.expire();
        return this.waiting[0] ?? null;
    }

    private kick(): void {
        if (this.timerKind === "blink") this.cancelTimer();
        if (!this.run) this.advance();
    }

    /** Decides what plays next when nothing is playing. */
    private advance(): void {
        if (this.disposed || this.run) return;
        this.cancelTimer();
        if (!this.canMove()) {
            if (this.shown !== this.target) {
                this.shown = this.target;
                this.paint(pose(this.target));
            }
            return;
        }
        const next = this.ready();
        // Rule 14: a gesture interrupts a state's pose: out, the gesture, back in.
        if (next && this.target !== "listening") {
            if (this.shown !== "rest") {
                const from = this.shown;
                this.play(`${from}.leave` as SequenceName, () => { this.shown = "rest"; this.advance(); });
                return;
            }
            this.waiting.shift();
            this.lastPlayed.set(next.kind, this.now());
            this.play(sequenceFor(next), () => this.advance());
            return;
        }
        if (this.shown !== this.target) {
            if (this.shown !== "rest") {
                const from = this.shown;
                this.play(`${from}.leave` as SequenceName, () => { this.shown = "rest"; this.advance(); });
            } else {
                const to = this.target as HeldPose;
                this.play(`${to}.enter` as SequenceName, () => { this.shown = to; this.advance(); });
            }
            return;
        }
        const loop = LOOP[this.shown];
        if (loop) {
            this.play(loop, () => this.advance());
            return;
        }
        if (this.shown === "rest") {
            // Rules 2 and 3: still between blinks; the one timer is the next blink.
            const wait = REST_BLINK_MIN_MS + this.random() * REST_BLINK_SPAN_MS;
            this.timerKind = "blink";
            this.timer = this.setTimer(() => {
                this.timer = null;
                this.timerKind = null;
                this.play("blink", () => this.advance());
            }, wait);
        }
        // Paused: the cap stays down, nothing runs (rule 10 peeks on pointing).
    }

    private play(name: SequenceName, then: () => void): void {
        this.cancelTimer();
        const frames = sequence(name);
        this.run = { name, frames, index: 0, then };
        this.onPlay?.(name);
        this.paint(frames[0]);
        this.scheduleStep();
    }

    private scheduleStep(): void {
        this.timerKind = "frame";
        this.timer = this.setTimer(() => {
            this.timer = null;
            this.timerKind = null;
            const run = this.run;
            if (!run) return;
            run.index += 1;
            if (run.index < run.frames.length) {
                this.paint(run.frames[run.index]);
                this.scheduleStep();
                return;
            }
            this.run = null;
            run.then();
        }, FRAME_MS);
    }

    private stopRun(): void {
        this.run = null;
        if (this.timerKind === "frame") this.cancelTimer();
    }

    private cancelTimer(): void {
        if (this.timer !== null) this.clearTimer(this.timer);
        this.timer = null;
        this.timerKind = null;
    }
}

function sequenceFor(w: Waiting): SequenceName {
    switch (w.kind) {
        case "needs":
            return "needs";
        case "brief":
            return w.evening ? "briefEvening" : "briefMorning";
        case "retry":
            return "retry";
        case "sent":
            return "sent";
    }
}

/** The mark's state for the app's one line (handoff section 1). The app's
 *  "Listening quietly" is the microphone in the background: the mark rests. */
export function heldForDoingNow(kind: string | undefined): MarkHeld {
    switch (kind) {
        case "paused":
            return "paused";
        case "meeting":
            return "listening";
        case "working":
            return "working";
        default:
            return "rest";
    }
}
