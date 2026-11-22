import { describe, expect, it } from "vitest";
import type { MobileBriefRef } from "@/lib/wails-shim-app";
import { briefSourcesByKind, watchConversations } from "./brief-sources";

const ref = (r: string, group: string | undefined, sourceId = r, observedAt = "2026-11-20T10:00:00Z"): MobileBriefRef =>
    ({ ref: r, sourceKind: "x", sourceId, label: r, observedAt, ...(group ? { group } : {}) }) as MobileBriefRef;

describe("briefSourcesByKind", () => {
    it("lists the kinds present in the design's order, newest first within each", () => {
        const groups = briefSourcesByKind({ evidence: [
            ref("e1", "screen"), ref("e2", "calendar", "c1", "2026-11-19T10:00:00Z"), ref("e3", "watch"),
            ref("e4", "calendar", "c2", "2026-11-21T10:00:00Z"), ref("e5", "messages"), ref("e6", "agents"), ref("e7", "mail"),
            ref("e8", "calls"), ref("e9", "reminders"),
        ] });
        expect(groups.map((g) => g.kind)).toEqual(["calendar", "mail", "messages", "reminders", "calls", "watch", "agents", "screen"]);
        expect(groups[0].sources.map((s) => s.ref)).toEqual(["e4", "e2"]);
    });

    it("drops a source with no group or an unknown one, never shown raw", () => {
        const groups = briefSourcesByKind({ evidence: [ref("e1", undefined), ref("e2", "news"), ref("e3", "mail")] });
        expect(groups).toEqual([{ kind: "mail", sources: [expect.objectContaining({ ref: "e3" })] }]);
        expect(briefSourcesByKind(null)).toEqual([]);
    });

    it("counts each watch conversation once", () => {
        expect(watchConversations({ evidence: [ref("e1", "watch", "s1"), ref("e2", "watch", "s1"), ref("e3", "watch", "s2"), ref("e4", "screen", "s3")] })).toBe(2);
        expect(watchConversations(undefined)).toBe(0);
    });
});
