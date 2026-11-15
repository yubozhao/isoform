import { describe, expect, it } from "vitest";
import { briefCopySafe, briefForDisplay } from "./brief-hygiene";
import type { MobileBriefDocument } from "./wails-shim-app";

describe("brief display hygiene", () => {
    it.each(["Review [RFC] retry policy", "Reply to Dana <dana@example.com>", "The API returns [1, 2, 3]", "Review [MAX_RETRIES]", "Reply to Dana <person@example.com>"])("preserves real bracketed copy: %s", (text) => {
        expect(briefCopySafe(text)).toBe(true);
        const brief = { narrative: [{ text, refs: ["e1"] }], sections: { moved: [{ title: text, detail: text }] }, evidence: [{ label: text }] } as MobileBriefDocument;
        const shown = briefForDisplay(brief);
        expect(shown.narrative[0].text).toBe(text);
        expect(shown.sections.moved[0].title).toBe(text);
        expect(shown.sections.moved[0].detail).toBe(text);
        expect(shown.evidence[0].label).toBe(text);
    });
    it.each(["Name", "{{person}}", "[CONTACT_NAME]", "[project]", "%s", "__name__", "$person", "<subject>", "unidentified contacts", "?"])("drops an unfinished subject %s", (subject) => {
        expect(briefCopySafe(`Want me to write a reply to ${subject}?`)).toBe(false);
    });
    it.each(["[REDACTED:code]", "[REDACTED:account_number]", "[masked]", "[...]"])("preserves the privacy marker %s", (mask) => {
        expect(briefCopySafe(`The order ${mask} is ready.`)).toBe(true);
        expect(briefCopySafe(`The order ${mask} is ready. Reply to Name?`)).toBe(false);
    });
    it.each(["[REDACTED:code]", "[REDACTED:account_number]", "[masked]", "[...]"])("withholds cached question subjects containing %s", (mask) => {
        const brief = { narrative: [{ text: "The Atlas release plan is ready.", refs: ["e1"] }], sections: {},
            cards: [
                { id: "masked-label", kind: "question", paragraph: 0, label: `Ask about ${mask}`, message: "Ask about Atlas", refs: ["e1"] },
                { id: "masked-message", kind: "question", paragraph: 0, label: "Ask about Atlas", message: `Ask about ${mask}`, refs: ["e1"] },
                { id: "safe", kind: "question", paragraph: 0, label: "Review [RFC]", message: "Review [RFC] retry policy", refs: ["e1"] },
            ] } as MobileBriefDocument;
        expect(briefForDisplay(brief).cards?.map((card) => card.id)).toEqual(["safe"]);
        expect(brief.cards).toHaveLength(3);
    });
    it("keeps named copy and the recording, and removes cached placeholder paragraphs and chips", () => {
        const brief = { narrative: [{ text: "The Halcyon launch proposal is ready.", refs: ["e1"] }, { text: "Want me to draft a reply to Name?", refs: ["e1"] }],
            cards: [{ id: "q", kind: "question", paragraph: 1, label: "Draft a reply", message: "Draft a reply", refs: ["e1"] }], audio: { bytes: 123 } } as MobileBriefDocument;
        const got = briefForDisplay(brief);
        expect(got.narrative.map((para) => para.text)).toEqual(["The Halcyon launch proposal is ready."]);
        expect(got.cards).toEqual([]);
        expect(got.audio).toEqual({ bytes: 123 });
        expect(brief.narrative).toHaveLength(2);
        expect(briefCopySafe("Want me to draft a reply to Dana?")).toBe(true);
        expect(briefCopySafe("The NameError is fixed.")).toBe(true);
    });
    it("keeps linked item cards when their paragraph is withheld", () => {
        const linked = { id: "item:halcyon", kind: "item", paragraph: 1, label: "Open Halcyon launch proposal",
            message: "Review the Halcyon launch proposal?", refs: ["e1"], itemKey: "halcyon" };
        const question = { id: "question:halcyon", kind: "question", paragraph: 1, label: "Review Halcyon launch proposal",
            message: "Review the Halcyon launch proposal?", refs: ["e1"] };
        const brief = { narrative: [{ text: "The Halcyon launch proposal is ready.", refs: ["e1"] },
                { text: "Want me to draft a reply to Name?", refs: ["e1"] }],
            sections: {}, cards: [linked, question] } as MobileBriefDocument;
        const shown = briefForDisplay(brief);
        expect(shown.cards).toEqual([{ ...linked, paragraph: undefined, label: undefined }]);
        expect(linked.paragraph).toBe(1);
        expect(linked.label).toBe("Open Halcyon launch proposal");
    });
    it("withholds unsafe cached items and evidence without rewriting them", () => {
        const row = { id: "row", title: "Halcyon launch proposal", detail: "A draft is ready.\nReply to Name?", status: "", at: "", kind: "", refs: ["e1"] };
        const brief: MobileBriefDocument = {
            version: 1, briefId: "legacy", slot: "morning", localDate: "2026-10-23", asOf: "2026-10-23T07:00:00Z",
            coldStart: false, narrativeStatus: "ok", narrative: [], dayShape: null,
            sections: { moved: [row, { ...row, id: "placeholder", title: "Name" }], doing: [], watching: [], meetings: [], people: [], openLoops: [], learned: [], deliverables: [], schedules: [] },
            evidence: [{ ref: "e1", label: "Name", sourceKind: "history", sourceId: "h1", observedAt: "" }],
        };
        const shown = briefForDisplay(brief);
        expect(shown.sections.moved).toEqual([]);
        expect(shown.evidence).toEqual([]);
        expect(brief.sections.moved).toHaveLength(2);
    });
});

it('withholds whole unsafe cached paragraphs and items without rewriting their safe lines', () => {
    const good = '  Review at 9:00 AM.\nKeep the spacing.  ';
    const bad = 'A complete sentence.\nReply to Name?\nAnother complete sentence.';
    const brief = { narrative: [{ text: good, refs: [] }, { text: bad, refs: [] }],
        sections: { moved: [{ id: 'good', title: good, detail: good }, { id: 'bad-detail', title: 'Review', detail: bad }, { id: 'bad-title', title: bad, detail: good }] },
        weeklyRecap: [{ text: good, refs: [] }, { text: bad, refs: [] }], evidence: [], audio: { bytes: 123 },
    } as unknown as MobileBriefDocument;
    const shown = briefForDisplay(brief);
    expect(shown.narrative.map((p) => p.text)).toEqual([good]);
    expect(shown.sections.moved).toEqual([brief.sections.moved[0]]);
    expect(shown.weeklyRecap).toEqual([brief.weeklyRecap![0]]);
    expect(shown.audio).toEqual({ bytes: 123 });
    expect(brief.narrative).toHaveLength(2);
});

it('withholds answers bound to an unsafe closing even if the previous paragraph is a question', () => {
    const brief = { narrative: [{ text: 'What remains for the review?', refs: [] }, { text: 'Hold the afternoon?\nReply to Name?', refs: [] }],
        sections: {}, evidence: [], cards: [{ id: 'answer', kind: 'action', mode: 'send', answerTo: 'unsafe-closing', label: 'Hold it', message: 'Hold the afternoon.', refs: [] }],
    } as unknown as MobileBriefDocument;
    const shown = briefForDisplay(brief);
    expect(shown.narrative).toEqual([brief.narrative[0]]);
    expect(shown.cards).toEqual([]);
});

it("sanitizes cached prose, withholds masked questions and old audio, and preserves safe-card proof", () => {
    const mask = "[REDACTED:account_number]";
    const brief = {
        narrative: [{ text: `The contract ${mask}sed out.`, topicName: mask, refs: [] }],
        sections: {}, evidence: [],
        cards: [
            { id: "masked", kind: "question", paragraph: 0, label: mask, message: `Review ${mask}`, facts: "masked-proof", refs: [] },
            { id: "q", kind: "question", paragraph: 0, label: "Review the contract", message: "Review the contract", facts: "original-proof", refs: [] },
        ],
        offers: [{ label: mask, prompt: `Review ${mask}`, text: mask }],
        audio: { bytes: 123 },
    } as unknown as MobileBriefDocument;
    const shown = briefForDisplay(brief);
    expect(JSON.stringify(shown)).not.toContain("[REDACTED");
    expect(shown.narrative[0].text).not.toContain("sed");
    expect(shown.cards?.[0].facts).toBe("original-proof");
    expect(shown.cards?.map((card) => card.id)).toEqual(["q"]);
    expect(brief.cards?.[0].facts).toBe("masked-proof");
    expect(shown.audio).toBeUndefined();
    expect(brief.narrative[0].text).toContain(mask);
});

// Bo, 2026-11-15: the latest brief shows its words and recording. The display
// guard used to drop every paragraph it flagged, which could leave no words,
// and took the recording away whenever it changed anything.
it("never leaves the brief without words or its recording", () => {
    const brief = { narrative: [{ text: "Reply to Name?", refs: [] }], sections: {}, evidence: [], audio: { bytes: 123 } } as unknown as MobileBriefDocument;
    const shown = briefForDisplay(brief);
    expect(shown.narrative.map((p) => p.text)).toEqual(["Reply to Name?"]);
    expect(shown.audio).toEqual({ bytes: 123 });
});
