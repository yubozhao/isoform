import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { FRAME_MS, pose, sequence, type MarkState, type SequenceName } from "./mark-frames";
import { MarkMachine, heldForDoingNow, type MarkMachineStart } from "./mark-machine";
import { isLogoFrame } from "./mark-paint";

const len = (name: SequenceName) => sequence(name).length * FRAME_MS;

function setup(start: MarkMachineStart & { enter?: MarkMachineStart["held"] } = {}) {
    const painted: MarkState[] = [];
    const played: SequenceName[] = [];
    const machine = new MarkMachine(
        { paint: (st) => painted.push(st), now: () => Date.now(), random: () => 0, onPlay: (name) => played.push(name) },
        start,
    );
    // The app learns its line after the mark mounts: the state enters.
    if (start.enter) machine.setHeld(start.enter);
    return { machine, painted, played };
}

describe("MarkMachine", () => {
    beforeEach(() => { vi.useFakeTimers(); vi.setSystemTime(new Date("2026-11-10T09:00:00Z")); });
    afterEach(() => { vi.useRealTimers(); });

    it("rests as the logo with one blink timer and no frames between blinks (rules 1-3)", () => {
        const { machine, painted, played } = setup();
        expect(isLogoFrame(painted[0])).toBe(true);
        expect(machine.timerPending).toBe("blink");
        expect(vi.getTimerCount()).toBe(1);
        vi.advanceTimersByTime(19_999);
        expect(painted).toHaveLength(1);
        expect(played).toEqual([]);
        vi.advanceTimersByTime(1);
        expect(played).toEqual(["blink"]);
        vi.advanceTimersByTime(len("blink"));
        expect(isLogoFrame(painted[painted.length - 1])).toBe(true);
        expect(painted[painted.length - 1].B).toBe(1);
        expect(machine.timerPending).toBe("blink");
        expect(vi.getTimerCount()).toBe(1);
    });

    it("waits 20 to 40 seconds between rest blinks", () => {
        const painted: MarkState[] = [];
        const played: SequenceName[] = [];
        new MarkMachine({ paint: (st) => painted.push(st), random: () => 0.999, onPlay: (n) => played.push(n) });
        vi.advanceTimersByTime(39_900);
        expect(played).toEqual([]);
        vi.advanceTimersByTime(100);
        expect(played).toEqual(["blink"]);
    });

    it("runs nothing while the window is hidden or behind, and plays only the owed brief and needs you on return (rule 16)", () => {
        const { machine, played } = setup({ enter: "working" });
        expect(played).toEqual(["working.enter"]);
        machine.setVisible(false);
        expect(machine.timerPending).toBeNull();
        expect(vi.getTimerCount()).toBe(0);
        machine.event("sent");
        machine.event("retry");
        machine.event("brief", { evening: true });
        machine.event("needs");
        vi.advanceTimersByTime(120_000);
        expect(played).toEqual(["working.enter"]);
        expect(vi.getTimerCount()).toBe(0);
        machine.setVisible(true);
        vi.advanceTimersByTime(30_000);
        expect(played.slice(1, 6)).toEqual(["working.leave", "needs", "briefEvening", "working.enter", "working.loop"]);
        expect(played).not.toContain("sent");
        expect(played).not.toContain("retry");
    });

    it("pauses the rest blink while hidden", () => {
        const { machine, played } = setup({ visible: false });
        expect(vi.getTimerCount()).toBe(0);
        vi.advanceTimersByTime(60_000);
        expect(played).toEqual([]);
        machine.setVisible(true);
        expect(machine.timerPending).toBe("blink");
    });

    it("with reduced motion plays and blinks nothing, and shows a state's pose in one step (rule 18)", () => {
        const { machine, painted, played } = setup({ reducedMotion: true });
        expect(vi.getTimerCount()).toBe(0);
        machine.setHeld("working");
        expect(painted[painted.length - 1]).toEqual(pose("working"));
        machine.event("needs");
        machine.pageChange();
        machine.setHeld("paused");
        machine.hover();
        vi.advanceTimersByTime(120_000);
        expect(played).toEqual([]);
        expect(vi.getTimerCount()).toBe(0);
        expect(painted[painted.length - 1]).toEqual(pose("paused"));
        machine.setReducedMotion(false);
        expect(machine.held).toBe("paused");
        expect(vi.getTimerCount()).toBe(0);
    });

    it("turns reduced motion on mid-gesture into the pose at once", () => {
        const { machine, painted } = setup({ held: "working" });
        vi.advanceTimersByTime(500);
        machine.setReducedMotion(true);
        expect(painted[painted.length - 1]).toEqual(pose("working"));
        expect(vi.getTimerCount()).toBe(0);
    });

    it("blinks once on a page change, never more than once every two seconds (rule 6)", () => {
        const { machine, played } = setup();
        machine.pageChange();
        vi.advanceTimersByTime(500);
        machine.pageChange();
        vi.advanceTimersByTime(1_000);
        machine.pageChange();
        expect(played).toEqual(["blink"]);
        vi.advanceTimersByTime(600);
        machine.pageChange();
        expect(played).toEqual(["blink", "blink"]);
    });

    it("plays no page blink during a state", () => {
        const { machine, played } = setup({ enter: "paused" });
        vi.advanceTimersByTime(len("paused.enter"));
        machine.pageChange();
        expect(played).toEqual(["paused.enter"]);
    });

    it("plays one gesture at a time; one more waits, the higher kind keeps the place (rule 12)", () => {
        const { machine, played } = setup();
        machine.event("sent");
        machine.event("retry");
        machine.event("needs");
        vi.advanceTimersByTime(len("sent") + len("needs") + 100);
        expect(played).toEqual(["sent", "needs"]);
        vi.advanceTimersByTime(30_000);
        expect(played.filter((name) => name !== "blink")).toEqual(["sent", "needs"]);
    });

    it("plays each kind at most once a minute (rule 13) and each event once, ending on the logo (rule 5)", () => {
        const { machine, painted, played } = setup();
        machine.event("needs");
        machine.event("needs");
        vi.advanceTimersByTime(len("needs"));
        expect(isLogoFrame(painted[painted.length - 1])).toBe(true);
        machine.event("needs");
        vi.advanceTimersByTime(50_000);
        expect(played.filter((name) => name === "needs")).toHaveLength(1);
        vi.advanceTimersByTime(10_000);
        machine.event("needs");
        expect(played.filter((name) => name === "needs")).toHaveLength(2);
    });

    it("keeps every gesture under four seconds (rule 17)", () => {
        for (const name of ["needs", "briefMorning", "briefEvening", "sent", "retry", "blink"] as const) {
            expect(len(name)).toBeLessThanOrEqual(4_000);
            expect(isLogoFrame(sequence(name)[sequence(name).length - 1])).toBe(true);
        }
    });

    it("circles the whole time a task runs, then leaves to rest (rules 4 and Bo's decision 3)", () => {
        const { machine, played } = setup({ enter: "working" });
        vi.advanceTimersByTime(len("working.enter") + 3 * len("working.loop"));
        expect(played).toEqual(["working.enter", "working.loop", "working.loop", "working.loop", "working.loop"]);
        machine.setHeld("rest");
        vi.advanceTimersByTime(len("working.loop") + len("working.leave"));
        expect(played.slice(-1)).toEqual(["working.leave"]);
        expect(machine.held).toBe("rest");
        expect(machine.timerPending).toBe("blink");
    });

    it("lets a gesture interrupt a state's pose: out, the gesture, back in (rule 14)", () => {
        const { machine, played } = setup({ enter: "working" });
        vi.advanceTimersByTime(len("working.enter") + 200);
        machine.event("brief");
        vi.advanceTimersByTime(len("working.loop") + len("working.leave") + len("briefMorning") + len("working.enter") + 100);
        expect(played.slice(0, 6)).toEqual(["working.enter", "working.loop", "working.leave", "briefMorning", "working.enter", "working.loop"]);
    });

    it("only listens during a recorded call; needs you and the brief play once after it (rule 15)", () => {
        const { machine, played } = setup();
        machine.setHeld("listening");
        machine.event("sent");
        machine.event("retry");
        machine.event("needs");
        machine.event("needs");
        machine.event("brief");
        vi.advanceTimersByTime(60_000);
        expect(new Set(played)).toEqual(new Set(["listening.enter", "listening.loop"]));
        machine.setHeld("rest");
        vi.advanceTimersByTime(20_000);
        const after = played.slice(played.lastIndexOf("listening.loop") + 1);
        expect(after.slice(0, 3)).toEqual(["listening.leave", "needs", "briefMorning"]);
        expect(played).not.toContain("sent");
        expect(played).not.toContain("retry");
    });

    it("stops a gesture when a call starts", () => {
        const { machine, painted, played } = setup();
        machine.event("sent");
        vi.advanceTimersByTime(1_000);
        machine.setHeld("listening");
        expect(isLogoFrame(painted[painted.length - 1]) || played.includes("listening.enter")).toBe(true);
        vi.advanceTimersByTime(len("listening.enter"));
        expect(played).toEqual(["sent", "listening.enter", "listening.loop"]);
    });

    it("holds the paused cap still and peeks only when pointed at (rules 10, 4)", () => {
        const { machine, played } = setup({ enter: "paused" });
        vi.advanceTimersByTime(len("paused.enter"));
        expect(vi.getTimerCount()).toBe(0);
        machine.hover();
        vi.advanceTimersByTime(len("paused.peek"));
        expect(played).toEqual(["paused.enter", "paused.peek"]);
        expect(vi.getTimerCount()).toBe(0);
    });

    it("leaves pointing at rest to the wordmark's own double blink", () => {
        const { machine, played } = setup();
        machine.hover();
        expect(played).toEqual([]);
    });

    it("follows the one status line: paused, a call and work move the mark; the rest rest (rule 7)", () => {
        expect(heldForDoingNow("paused")).toBe("paused");
        expect(heldForDoingNow("meeting")).toBe("listening");
        expect(heldForDoingNow("working")).toBe("working");
        for (const kind of ["calendar", "watching", "listening", "idle", undefined]) expect(heldForDoingNow(kind)).toBe("rest");
    });

    it("starts in a state's pose when it mounts during one", () => {
        const { machine, painted, played } = setup({ held: "working" });
        expect(painted[0]).toEqual(pose("working"));
        expect(played).toEqual(["working.loop"]);
        expect(machine.held).toBe("working");
    });

    it("stops every timer on dispose", () => {
        const { machine } = setup({ held: "working" });
        machine.dispose();
        expect(vi.getTimerCount()).toBe(0);
    });
});
