// Draws one MarkState into the mark's SVG: a port of the render step of
// design's cut kit (docs/handoff-2026-11-09/mark-motion-1.0.0/mark-kit.js).
//
// Rule 24, the logo is the master drawing: whenever the frame is the logo (at
// rest, and during a blink, where only the eye changes) the logo's own paths
// are shown, so the mark at rest is today's logo to the pixel. Every other
// frame is drawn by the kit, whose S is fitted to the logo
// (scripts/mark-frames.mjs) and whose I is the logo's own I (I_OUTLINE,
// I_STEM below), so the change from one drawing to the other does not show.
import type { MarkState } from "./mark-frames";
import { REST } from "./mark-frames";

/** Kit units to the logo's 19-unit box: ink x 67..263, y 68..265 to 0..18.215. */
export const FIT = 18.215 / 196.5;
export const FIT_TRANSFORM = `scale(${FIT.toFixed(5)}) translate(-67 -68)`;
/** The logo's units inside kit units, for the I drawn from the logo. */
export const UNFIT_TRANSFORM = `translate(67 68) scale(${(1 / FIT).toFixed(5)})`;

/** The logo's eye centre, in kit units (the kit's ball sits at 226,159). */
const EYE_X = 14.7415 / FIT + 67;
const EYE_Y = 8.48523 / FIT + 68;
/** The logo's eye centre and the kit's closed lid, in logo units. */
export const LOGO_EYE_CENTER = [14.7415, 8.48523] as const;
export const LID_PATH = "M-21 -5 Q0 17 21 -5";

/** The I of the logo, in logo units: the stem, round-capped so it reads on
 *  its own when the serifs fade (the ! of ?!), and the serifs with the
 *  stem's fillets, taken from the logo's outline. */
export const I_STEM = "M6.858 1.854 L11.549 16.5";
export const I_STEM_WIDTH = 1.968;
export const I_OUTLINE =
    "M3.911 1.978 C5.075 1.978 6.105 2.729 6.460 3.838 L9.954 14.746 C10.191 15.485 9.640 16.239 8.865 16.239 L14.378 16.239 C13.259 16.239 12.268 15.516 11.927 14.451 L8.389 3.407 C8.163 2.700 8.690 1.978 9.432 1.978 Z " +
    "M3.911 0 H10.020 A0.989 0.989 0 0 1 10.020 1.978 H3.911 A0.989 0.989 0 0 1 3.911 0 Z " +
    "M8.269 16.239 H14.378 A0.989 0.989 0 0 1 14.378 18.216 H8.269 A0.989 0.989 0 0 1 8.269 16.239 Z";

const ANCH = { tail: [119.5, 186.5], hook: [207.5, 143.5], S: [165, 166], all: [165, 166] } as const;

const f1 = (v: number) => v.toFixed(1);

function seg(pts: number[], from: number, to: number): string {
    let d = `M${f1(pts[from])} ${f1(pts[from + 1])}`;
    for (let i = from + 2; i < to; i += 6) d += ` C${pts.slice(i, i + 6).map(f1).join(" ")}`;
    return d;
}

function tf4(a: number[], [ax, ay]: readonly number[]): string {
    return `translate(${f1(a[0])} ${f1(a[1])}) rotate(${f1(a[2])} ${ax} ${ay}) translate(${ax} ${ay}) scale(${a[3].toFixed(3)}) translate(${-ax} ${-ay})`;
}

const near = (a: number, b: number) => Math.abs(a - b) < 0.005;
const nearAll = (a: number[], b: number[]) => a.length === b.length && a.every((v, i) => near(v, b[i]));

/** Whether the frame is the logo, its eye open or shut: drawn with the logo's paths. */
export function isLogoFrame(st: MarkState): boolean {
    return (
        nearAll(st.S, REST.S) && near(st.sw, REST.sw) && near(st.wave, 0) && near(st.flip, 1) &&
        nearAll(st.Sg, REST.Sg) && nearAll(st.tail, REST.tail) && nearAll(st.hook, REST.hook) &&
        nearAll(st.all, REST.all) && nearAll(st.face, REST.face) && nearAll(st.I, REST.I) &&
        near(st.serif, 1) && nearAll(st.O, REST.O) && st.A <= 0.05 && !st.clip && st.th == null &&
        (st.B >= 0.995 || st.B <= 0.2)
    );
}

interface Parts {
    logo: SVGGElement;
    leye: SVGPathElement;
    llid: SVGPathElement;
    kit: SVGGElement;
    all: SVGGElement;
    front: SVGGElement;
    oclip: SVGGElement;
    face: SVGGElement;
    O: SVGGElement;
    ball: SVGEllipseElement;
    lid: SVGPathElement;
    A: SVGGElement;
    Aball: SVGEllipseElement;
    Sg: SVGGElement;
    tail: SVGGElement;
    hook: SVGGElement;
    tailP: SVGPathElement;
    hookP: SVGPathElement;
    I: SVGGElement;
    serifs: SVGGElement;
}

/** Paints frames into one mark's SVG (MarkSvg). */
export class MarkPainter {
    private readonly p: Parts;
    private readonly clipId: string;
    private last: MarkState | null = null;
    private readonly kitOnly: boolean;
    /** The mark's SVG and the wordmark's around it: motion may leave the
     *  19-unit box (rule 22), so they stop clipping while something moves.
     *  At rest they clip as today, which keeps the logo's pixels identical. */
    private readonly boxes: SVGSVGElement[];

    /** kitOnly draws every frame with the kit, the logo too: the harness
     *  uses it to show how close the two drawings are at rest. */
    constructor(svg: SVGSVGElement, clipId: string, opts: { kitOnly?: boolean } = {}) {
        this.kitOnly = !!opts.kitOnly;
        this.boxes = svg.ownerSVGElement ? [svg, svg.ownerSVGElement] : [svg];
        const q = <T extends Element>(k: string) => {
            const el = svg.querySelector<T>(`[data-k="${k}"]`);
            if (!el) throw new Error(`mark: missing ${k}`);
            return el;
        };
        this.clipId = clipId;
        this.p = {
            logo: q("logo"), leye: q("leye"), llid: q("llid"), kit: q("kit"), all: q("all"), front: q("front"),
            oclip: q("oclip"), face: q("face"), O: q("O"), ball: q("ball"), lid: q("lid"), A: q("A"), Aball: q("Aball"),
            Sg: q("Sg"), tail: q("tail"), hook: q("hook"), tailP: q("tailP"), hookP: q("hookP"), I: q("I"), serifs: q("serifs"),
        };
    }

    paint(st: MarkState): void {
        if (this.last === st) return;
        this.last = st;
        const p = this.p;
        if (!this.kitOnly && isLogoFrame(st)) {
            for (const box of this.boxes) box.style.overflow = "";
            p.kit.style.display = "none";
            p.logo.style.display = "";
            const open = st.B > 0.2;
            p.leye.style.display = open ? "" : "none";
            p.llid.style.display = open ? "none" : "";
            return;
        }
        for (const box of this.boxes) box.style.overflow = "visible";
        p.logo.style.display = "none";
        p.kit.style.display = "";
        this.paintKit(st);
    }

    private paintKit(s: MarkState): void {
        const p = this.p;
        const pts: number[] = [];
        for (let i = 0; i < s.S.length; i += 2) {
            pts.push(165 + (s.S[i] - 165) * s.flip, s.S[i + 1] + s.wave * Math.sin(s.phase + i * 0.45));
        }
        p.tailP.setAttribute("d", seg(pts, 0, 14));
        p.hookP.setAttribute("d", seg(pts, 12, 26));
        p.Sg.setAttribute("stroke-width", f1(s.sw));
        p.tail.setAttribute("transform", tf4(s.tail, ANCH.tail));
        p.hook.setAttribute("transform", tf4(s.hook, ANCH.hook));
        p.Sg.setAttribute("transform", tf4(s.Sg, ANCH.S));
        p.all.setAttribute("transform", tf4(s.all, ANCH.all));
        p.face.setAttribute("transform", tf4(s.face, s.Fp));
        const [ix, iy, ir, is, isy] = s.I;
        const [px, py] = s.Ip;
        p.I.setAttribute("transform", `translate(${f1(ix)} ${f1(iy)}) rotate(${f1(ir)} ${px} ${py}) translate(${px} ${py}) scale(${is.toFixed(3)} ${(is * isy).toFixed(3)}) translate(${-px} ${-py})`);
        p.serifs.style.opacity = s.serif.toFixed(2);
        const ox = EYE_X + s.O[0];
        const oy = EYE_Y + s.O[1];
        p.O.setAttribute("transform", `translate(${f1(ox)} ${f1(oy)}) scale(${s.O[2].toFixed(3)})`);
        if (s.B > 0.2) {
            p.ball.setAttribute("ry", f1(21 * s.B));
            p.ball.setAttribute("rx", f1(21 * (1 + 0.18 * (1 - s.B))));
            p.ball.style.display = "";
            p.lid.style.display = "none";
        } else {
            p.ball.style.display = "none";
            p.lid.style.display = "";
        }
        if (s.A > 0.05) {
            p.A.style.display = "";
            p.A.setAttribute("transform", `translate(${f1(s.Ap[0])} ${f1(s.Ap[1])})`);
            p.Aball.setAttribute("ry", f1(21 * s.A));
        } else {
            p.A.style.display = "none";
        }
        // The horizon hides what is under it by clipping, never by painting a field.
        if (s.clip) p.oclip.setAttribute("clip-path", `url(#${this.clipId})`);
        else p.oclip.removeAttribute("clip-path");
        // The orbit: behind the nose on the far half, in front on the near half.
        if (s.th != null && Math.sin(s.th) >= 0) {
            if (p.oclip.nextElementSibling !== p.front) p.front.before(p.oclip);
        } else if (p.oclip.nextElementSibling !== p.face) {
            p.face.before(p.oclip);
        }
    }
}
