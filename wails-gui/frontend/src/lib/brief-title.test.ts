import { describe, expect, it } from "vitest";
import i18n from "@/i18n";
import type { MobileBriefDocument } from "@/lib/wails-shim-app";
import { briefHasWords, briefKind, briefLateLine, briefMadeAheadRunLine, briefTitle } from "./brief-title";

const brief = (over: Partial<MobileBriefDocument>) => ({
    briefId: "2026-10-28-morning", slot: "morning", asOf: "2026-10-28T13:50:00Z", timezone: "UTC",
    narrativeStatus: "ok", narrative: [], ...over,
}) as unknown as MobileBriefDocument;

describe.each(["en", "zh-CN"])("brief titles in %s", (language) => {
    const tr = (key: string, options?: Record<string, unknown>) => String(i18n.t(key as never, { ...options, ns: "today", lng: language }));
    const en = language === "en";

    it("names the kind and the time the brief was made, as design section 1 does", () => {
        expect(briefTitle(brief({ slot: "morning", asOf: "2026-10-28T13:50:00Z" }), language, tr))
            .toBe(en ? "Morning brief · 1:50 PM" : "早间简报 · 13:50");
        expect(briefTitle(brief({ slot: "evening", asOf: "2026-10-28T18:00:00Z" }), language, tr))
            .toBe(en ? "Evening brief · 6:00 PM" : "晚间简报 · 18:00");
        expect(briefTitle(brief({ slot: "on_request", asOf: "2026-10-28T15:41:00Z" }), language, tr))
            .toBe(en ? "Evening brief · 3:41 PM" : "晚间简报 · 15:41");
        expect(briefTitle(brief({ slot: "evening", asOf: "2026-10-28T18:00:00Z", yourWeek: {} as never }), language, tr))
            .toBe(en ? "Your week · 6:00 PM" : "你的一周 · 18:00");
    });

    it("names a made-ahead brief by when it was made, with its made-at time (P157 R64, v1.0.1)", () => {
        const night = brief({ slot: "morning", localDate: "2026-11-08", timezone: "America/Los_Angeles", asOf: "2026-11-08T07:20:00Z", generation: { reason: "made_ahead", scheduledAt: "2026-11-08T14:50:00Z", generatedAt: "2026-11-08T07:20:00Z", message: "" } } as never);
        expect(briefTitle(night, language, tr)).toBe(en ? "Morning brief · made last night at 11:20 PM" : "早间简报 · 昨晚 23:20 生成");
        const sameDay = brief({ slot: "morning", localDate: "2026-11-08", timezone: "America/Los_Angeles", asOf: "2026-11-08T09:20:00Z", generation: { reason: "made_ahead", scheduledAt: "2026-11-08T14:50:00Z", generatedAt: "2026-11-08T09:20:00Z", message: "" } } as never);
        expect(briefTitle(sameDay, language, tr)).toBe(en ? "Morning brief · made at 1:20 AM" : "早间简报 · 1:20 生成");
        const yesterday = brief({ slot: "morning", localDate: "2026-11-08", timezone: "America/Los_Angeles", asOf: "2026-11-08T01:00:00Z", generation: { reason: "made_ahead", scheduledAt: "2026-11-08T14:50:00Z", generatedAt: "2026-11-08T01:00:00Z", message: "" } } as never);
        expect(briefTitle(yesterday, language, tr)).toBe(en ? "Morning brief · made yesterday at 5:00 PM" : "早间简报 · 昨天 17:00 生成");
    });

    it("names a made-ahead brief from an older day by its date, and never shows an empty time (v1.0.2)", () => {
        const older = brief({ slot: "morning", localDate: "2026-11-08", timezone: "America/Los_Angeles", asOf: "2026-11-06T23:10:00Z", generation: { reason: "made_ahead", scheduledAt: "2026-11-08T14:50:00Z", generatedAt: "2026-11-06T23:10:00Z", message: "" } } as never);
        expect(briefTitle(older, language, tr)).toBe(en ? "Morning brief · made Nov 6 at 3:10 PM" : "早间简报 · 11月6日 15:10 生成");
        const noTime = brief({ slot: "morning", localDate: "2026-11-08", timezone: "America/Los_Angeles", asOf: undefined, generation: { reason: "made_ahead", scheduledAt: "2026-11-08T14:50:00Z", generatedAt: "", message: "" } } as never);
        const title = briefTitle(noTime, language, tr);
        expect(title).not.toMatch(/made|生成|·/);
        expect(title.length).toBeGreaterThan(0);
    });

    it.runIf(en)("keeps 'at 11:20 PM' together on the Brief page run row in English, with non-breaking spaces (v1.0.2)", () => {
        const line = briefMadeAheadRunLine({ briefId: "2026-11-08-morning", at: "2026-11-08T07:20:00Z", timezone: "America/Los_Angeles" }, (day) => day, "en", tr, new Date("2026-11-08T14:10:00Z"));
        expect(line).toBe("Today · made last night\u00A0at\u00A011:20\u00A0PM");
        expect(line).toContain("\u00A0at\u00A011:20\u00A0PM");
    });

    it("names a brief asked for by its time: before noon morning, from noon evening (today-review v1.1.16 D14)", () => {
        const en = language === "en";
        expect(briefTitle(brief({ slot: "on_request", displaySlot: "morning", asOf: "2026-10-28T17:24:00Z" } as never), language, tr))
            .toBe(en ? "Evening brief · 5:24 PM" : "晚间简报 · 17:24");
        expect(briefTitle(brief({ slot: "on_request", displaySlot: "evening", asOf: "2026-10-28T11:59:00Z" } as never), language, tr))
            .toBe(en ? "Morning brief · 11:59 AM" : "早间简报 · 11:59");
        expect(briefKind({ slot: "on_request", asOf: "2026-10-28T12:00:00Z", timezone: "UTC" } as never)).toBe("evening");
        expect(briefKind({ slot: "on_request", displaySlot: "evening" } as never)).toBe("evening");
        expect(briefKind({ slot: "on_request" } as never)).toBe("on_request");
    });

    it("titles a brief made late with its own made time, and adds the Late line", () => {
        const late = brief({
            slot: "morning", asOf: "2026-10-28T16:12:00Z",
            generation: { reason: "asleep", scheduledAt: "2026-10-28T13:50:00Z", generatedAt: "2026-10-28T16:12:00Z", message: "" },
        });
        expect(briefTitle(late, language, tr)).toBe(en ? "Morning brief · 4:12 PM" : "早间简报 · 16:12");
        const run = { briefId: late.briefId, status: "ok" as const, scheduledAt: "2026-10-28T13:50:00Z", at: late.asOf, late: true, reason: "asleep" as const };
        expect(briefLateLine(late, language, tr, run))
            .toBe(en ? "Came at 4:12 PM, not 1:50 PM: your computer was asleep." : "16:12才生成，不是13:50：电脑在睡眠。");
        expect(briefLateLine(late, language, tr)).toBeNull();
        expect(briefLateLine(late, language, tr, { ...run, briefId: "other" })).toBeNull();
        expect(briefLateLine(late, language, tr, { ...run, late: false })).toBeNull();
        expect(briefLateLine(late, language, tr, { ...run, reason: "closed" }))
            .toBe(en ? "Came at 4:12 PM, not 1:50 PM: Isoform wasn't open." : "16:12才生成，不是13:50：Isoform 没有打开。");
        expect(briefLateLine(late, language, tr, { ...run, reason: undefined }))
            .toBe(en ? "Came at 4:12 PM, not 1:50 PM." : "16:12才生成，不是13:50。");

    });

    it("titles the brief with its matching completed run's ready time", () => {
        const doc = brief({ asOf: "2026-10-28T13:00:00Z" });
        const run = { briefId: doc.briefId, status: "ok" as const, scheduledAt: doc.asOf, at: "2026-10-28T16:23:00Z" };
        expect(briefTitle(doc, language, tr, run)).toBe(en ? "Morning brief · 4:23 PM" : "早间简报 · 16:23");
        expect(briefTitle(doc, language, tr, { ...run, briefId: "other" })).toBe(en ? "Morning brief · 1:00 PM" : "早间简报 · 13:00");
    });

    it("has no Late line for a brief made on time", () => {
        expect(briefLateLine(brief({}), language, tr)).toBeNull();
    });
});

describe("brief kind and words", () => {
    it("treats only morning and evening as scheduled kinds", () => {
        expect(briefKind(brief({ slot: "morning" }))).toBe("morning");
        expect(briefKind(brief({ slot: "evening" }))).toBe("evening");
        expect(briefKind(brief({ slot: "on_request" }))).toBe("evening");
        expect(briefKind(brief({ slot: "on_request", asOf: "" }))).toBe("on_request");
        expect(briefKind(brief({ slot: "morning", yourWeek: {} as never }))).toBe("week");
    });

    it("counts a brief as having words only with a paragraph that says something", () => {
        expect(briefHasWords(brief({ narrative: [] }))).toBe(false);
        expect(briefHasWords(brief({ narrative: [{ text: "   " } as never] }))).toBe(false);
        expect(briefHasWords(brief({ narrative: [{ text: "Three meetings today." } as never] }))).toBe(true);
        expect(briefHasWords(null)).toBe(false);
    });
});
