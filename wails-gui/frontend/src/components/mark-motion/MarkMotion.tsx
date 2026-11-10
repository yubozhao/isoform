// The sidebar's mark, the one mark that moves (mark motion v1.0.1, rules
// 25-26): at rest today's logo, still; the three states and four events drawn
// from design's frames. MarkMachine decides what plays; this component only
// feeds it the app's existing signals and draws its frames.
import { useEffect, useId, useLayoutEffect, useRef } from "react";
import { useTranslation } from "react-i18next";
import { doingNowKey, useDoingNow } from "@/hooks/useDoingNow";
import { MarkPainter } from "./mark-paint";
import { MarkSvg } from "./MarkSvg";
import { MarkMachine, heldForDoingNow } from "./mark-machine";
import { useBriefArrivals, useReducedMotion, useWindowInFront } from "./mark-signals";

interface MarkMotionProps {
    /** 20 beside the wordmark, 19 when the sidebar is collapsed (rule 25). */
    size: number;
    /** The count of things that need you: a rise plays ?! (needs you). */
    needsCount?: number;
    /** Changes on each page change: one blink (rule 6). */
    pageKey?: string;
    /** The wordmark's hover double blink, as today (rule 2). */
    hoverBlink?: boolean;
    className?: string;
}

declare global {
    interface Window {
        /** Test hook (development builds only): play an event or state that
         *  has no signal in the app yet. */
        __isoformMark?: MarkMachine;
    }
}

export function MarkMotion({ size, needsCount = 0, pageKey, hoverBlink, className }: MarkMotionProps) {
    const { t } = useTranslation("app");
    const svgRef = useRef<SVGSVGElement>(null);
    const machineRef = useRef<MarkMachine | null>(null);
    const clipId = `mark-${useId().replace(/[^a-zA-Z0-9_-]/g, "")}`;
    const doingNow = useDoingNow();
    const held = heldForDoingNow(doingNow?.kind);
    const inFront = useWindowInFront();
    const reduced = useReducedMotion();

    // The machine follows the props; start values are read once.
    const start = useRef({ held, inFront, reduced });
    useLayoutEffect(() => {
        const svg = svgRef.current;
        if (!svg) return;
        const painter = new MarkPainter(svg, clipId);
        const machine = new MarkMachine(
            { paint: (st) => painter.paint(st) },
            { held: start.current.held, visible: start.current.inFront, reducedMotion: start.current.reduced },
        );
        machineRef.current = machine;
        if (import.meta.env.DEV) window.__isoformMark = machine;
        return () => {
            machine.dispose();
            machineRef.current = null;
            if (import.meta.env.DEV && window.__isoformMark === machine) delete window.__isoformMark;
        };
    }, [clipId]);

    useEffect(() => { machineRef.current?.setHeld(held); }, [held]);
    useEffect(() => { machineRef.current?.setVisible(inFront); }, [inFront]);
    useEffect(() => { machineRef.current?.setReducedMotion(reduced); }, [reduced]);

    // Needs you: the count of things that need you goes up.
    const lastNeeds = useRef(needsCount);
    useEffect(() => {
        if (needsCount > lastNeeds.current) machineRef.current?.event("needs");
        lastNeeds.current = needsCount;
    }, [needsCount]);

    // The brief: a new brief arrives; the evening one plays the sunset.
    useBriefArrivals((evening) => machineRef.current?.event("brief", { evening }));

    const lastPage = useRef(pageKey);
    useEffect(() => {
        if (pageKey === lastPage.current) return;
        lastPage.current = pageKey;
        machineRef.current?.pageChange();
    }, [pageKey]);

    // Rule 9: during a state, pointing shows the same line the menu bar shows.
    const line = held !== "rest" && doingNow
        ? (t(`observingStatus.doingNow.${doingNowKey(doingNow)}` as never, { app: doingNow.app ?? "", title: doingNow.title ?? "" } as never) as unknown as string)
        : null;

    return (
        <MarkSvg
            ref={svgRef}
            size={size}
            clipId={clipId}
            hoverBlink={hoverBlink}
            line={line}
            className={className}
            onPointerEnter={() => machineRef.current?.hover()}
        />
    );
}
