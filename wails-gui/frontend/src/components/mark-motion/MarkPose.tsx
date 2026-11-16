// One still pose of the mark, drawn from the same frames as MarkMotion
// (pass-questions v1.0.0 rule 4): the sidebar's watch line shows it at 16
// points, with no ripple and no idle moments. Words always sit beside it.
import { useId, useLayoutEffect, useRef } from "react";
import { MarkPainter } from "./mark-paint";
import { MarkSvg } from "./MarkSvg";
import { REST, pose, type MarkState } from "./mark-frames";

/** listening: the eye lifted, open. notListening: the logo, shut.
 *  closedEyes: the Closed Eyes pose, shut. */
export type MarkPoseName = "listening" | "notListening" | "closedEyes";

export function markPoseState(name: MarkPoseName): MarkState {
    switch (name) {
        case "listening":
            return pose("listening");
        case "closedEyes":
            return pose("paused");
        default:
            return { ...REST, B: 0 };
    }
}

/** label: the mark's words (pass-access v1.0.0 rule 12), so the pose never
 *  carries the state alone; without one the mark is decoration. */
export function MarkPose({ name, size = 16, className, label }: { name: MarkPoseName; size?: number; className?: string; label?: string }) {
    const svgRef = useRef<SVGSVGElement>(null);
    const clipId = `mark-pose-${useId().replace(/[^a-zA-Z0-9_-]/g, "")}`;
    useLayoutEffect(() => {
        const svg = svgRef.current;
        if (!svg) return;
        new MarkPainter(svg, clipId).paint(markPoseState(name));
    }, [clipId, name]);
    return <MarkSvg ref={svgRef} size={size} clipId={clipId} className={className} line={label ?? null} />;
}
