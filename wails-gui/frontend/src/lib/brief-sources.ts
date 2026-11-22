import type { MobileBriefDocument, MobileBriefRef } from "@/lib/wails-shim-app";

/** The kinds "In this brief" lists, by their names in What Isoform sees, in
 *  the design's order (2026-11-21 conformance answers B2, which change
 *  computer-brief v1.0.0 rule 6's five). */
export const BRIEF_SOURCE_KINDS = ["calendar", "mail", "messages", "reminders", "calls", "watch", "agents", "screen"] as const;
export type BriefSourceKind = (typeof BRIEF_SOURCE_KINDS)[number];

export interface BriefSourceGroup {
    kind: BriefSourceKind;
    /** The kind's sources, newest first. */
    sources: MobileBriefRef[];
}

/** The brief's sources by kind, only the kinds present. A source with no
 *  group, or a group this computer doesn't know, is not listed. */
export function briefSourcesByKind(brief: Pick<MobileBriefDocument, "evidence"> | null | undefined): BriefSourceGroup[] {
    const byKind = new Map<BriefSourceKind, MobileBriefRef[]>();
    for (const ref of brief?.evidence ?? []) {
        const kind = ref.group as BriefSourceKind | undefined;
        if (!kind || !(BRIEF_SOURCE_KINDS as readonly string[]).includes(kind)) continue;
        byKind.set(kind, [...(byKind.get(kind) ?? []), ref]);
    }
    const time = (ref: MobileBriefRef) => { const at = Date.parse(ref.observedAt); return Number.isNaN(at) ? 0 : at; };
    return BRIEF_SOURCE_KINDS.filter((kind) => byKind.has(kind))
        .map((kind) => ({ kind, sources: [...byKind.get(kind)!].sort((a, b) => time(b) - time(a)) }));
}

/** The watch conversations a brief drew on, for the band's "From your
 *  watch: {n} conversation" (computer-today v1.0.0 rule 2). */
export function watchConversations(brief: Pick<MobileBriefDocument, "evidence"> | null | undefined): number {
    return new Set((brief?.evidence ?? []).filter((ref) => ref.group === "watch").map((ref) => ref.sourceId)).size;
}
