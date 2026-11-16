import { render } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { MarkPose, markPoseState } from "./MarkPose";
import { REST, pose } from "./mark-frames";

describe("MarkPose", () => {
    it("draws listening open with the eye lifted, not listening and Closed Eyes shut", () => {
        expect(markPoseState("listening")).toEqual(pose("listening"));
        expect(markPoseState("listening").B).not.toBe(0);
        expect(markPoseState("notListening")).toEqual({ ...REST, B: 0 });
        expect(markPoseState("closedEyes")).toEqual(pose("paused"));
        expect(markPoseState("closedEyes").B).toBe(0);
    });

    it.each(["listening", "notListening", "closedEyes"] as const)("paints %s at 16 points", (name) => {
        const { container } = render(<MarkPose name={name} />);
        const svg = container.querySelector("svg")!;
        expect(svg).toHaveAttribute("width", "16");
        expect(svg).toHaveAttribute("aria-hidden", "true");
    });
});
