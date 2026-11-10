// The mark's SVG: the logo's own paths, shown at rest, and the kit's drawing
// (design's cut kit) for every other frame. MarkPainter draws frames into it.
import { forwardRef, type PointerEventHandler } from "react";
import { cn } from "@/lib/utils";
import { LOGO_BODY_PATH, LOGO_EYE_PATH } from "@/assets/icons/LogoIcon";
import { FIT, FIT_TRANSFORM, I_OUTLINE, I_STEM, I_STEM_WIDTH, LID_PATH, LOGO_EYE_CENTER, UNFIT_TRANSFORM } from "./mark-paint";

interface MarkSvgProps {
    size: number;
    clipId: string;
    /** The wordmark's hover double blink, as today. */
    hoverBlink?: boolean;
    /** The words for what the mark shows, as its tooltip and name. */
    line?: string | null;
    className?: string;
    onPointerEnter?: PointerEventHandler<SVGSVGElement>;
}

export const MarkSvg = forwardRef<SVGSVGElement, MarkSvgProps>(function MarkSvg({ size, clipId, hoverBlink, line, className, onPointerEnter }, ref) {
    const [ex, ey] = LOGO_EYE_CENTER;
    return (
        <svg
            ref={ref}
            width={size}
            height={size}
            viewBox="0 0 19 19"
            fill="none"
            xmlns="http://www.w3.org/2000/svg"
            className={cn(className, "text-accent")}
            aria-hidden={line ? undefined : true}
            role={line ? "img" : undefined}
            aria-label={line ?? undefined}
            data-mark-motion=""
            onPointerEnter={onPointerEnter}
        >
            {line ? <title>{line}</title> : null}
            <defs>
                <clipPath id={clipId} clipPathUnits="userSpaceOnUse">
                    <rect x="-400" y="-400" width="1200" height="614" />
                </clipPath>
            </defs>
            {/* The logo, the master drawing: shown at rest and while it blinks. */}
            <g data-k="logo">
                <path data-k="leye" className={hoverBlink ? "logo-eye-hover" : undefined} fillRule="evenodd" clipRule="evenodd" d={LOGO_EYE_PATH} fill="var(--color-accent)" />
                <path data-k="llid" d={LID_PATH} transform={`translate(${ex} ${ey}) scale(${FIT.toFixed(5)})`} fill="none" stroke="var(--color-accent)" strokeWidth="14" strokeLinecap="round" style={{ display: "none" }} />
                <path fillRule="evenodd" clipRule="evenodd" d={LOGO_BODY_PATH} fill="var(--color-accent)" />
            </g>
            {/* The kit's drawing, for every frame that is not the logo. */}
            <g data-k="kit" transform={FIT_TRANSFORM} style={{ display: "none" }}>
                <g data-k="all">
                    <g data-k="oclip">
                        <g data-k="O">
                            <ellipse data-k="ball" rx="21" ry="21" fill="var(--color-accent)" />
                            <path data-k="lid" d={LID_PATH} fill="none" stroke="var(--color-accent)" strokeWidth="14" strokeLinecap="round" />
                        </g>
                        <g data-k="A">
                            <ellipse data-k="Aball" rx="21" ry="21" fill="var(--color-accent)" />
                        </g>
                    </g>
                    <g data-k="face">
                        <g data-k="Sg" fill="none" stroke="var(--color-accent)" strokeWidth="21" strokeLinecap="round" strokeLinejoin="round">
                            <g data-k="tail"><path data-k="tailP" /></g>
                            <g data-k="hook"><path data-k="hookP" /></g>
                        </g>
                        <g data-k="I">
                            <g transform={UNFIT_TRANSFORM}>
                                <path d={I_STEM} stroke="var(--color-accent)" strokeWidth={I_STEM_WIDTH} strokeLinecap="round" />
                                <g data-k="serifs">
                                    <path d={I_OUTLINE} fill="var(--color-accent)" />
                                </g>
                            </g>
                        </g>
                    </g>
                    <g data-k="front" />
                </g>
            </g>
        </svg>
    );
});
