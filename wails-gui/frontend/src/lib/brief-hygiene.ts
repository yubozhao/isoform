import { displayModelCopy } from "./model-display";
import type { MobileBriefDocument, MobileBriefItem } from "./wails-shim-app";
import patterns from "./brief-copy-patterns.json";

// Last display guard, including documents cached before the assembly guard.
// Go embeds this same list at the assembly boundary.
const unfinished = new RegExp(patterns.unfinished.join("|"));
const unfinishedInsensitive = new RegExp(patterns.unfinishedInsensitive.join("|"), "i");
const privacyMask = new RegExp(patterns.privacyMask, "g");
const maskedSubject = new RegExp(patterns.privacyMask);
export function briefCopySafe(text: string): boolean {
    text = text.replace(privacyMask, "");
    return !unfinished.test(text) && !unfinishedInsensitive.test(text);
}

export function briefForDisplay(brief: MobileBriefDocument): MobileBriefDocument {
    const original = brief;
    brief = displayModelCopy(brief);
    const redactedCopy = JSON.stringify(original) !== JSON.stringify(brief);
    // Withhold invalid legacy units as a whole; never rewrite delivered prose.
    const safeCopy = (text: string) => text.split("\n").every(briefCopySafe);
    const safeItems = (items: MobileBriefItem[]) => items.filter((item) => [
        item.title, item.detail, item.nextStep ?? "", item.attentionReason ?? "",
        item.context?.preview ?? "", item.context?.sourceLabel ?? "",
    ].every(safeCopy));
    // "Your week": a row whose words fail the check leaves; a masked lead
    // takes the card with it.
    const safeWeek = (week: MobileBriefDocument["yourWeek"]): MobileBriefDocument["yourWeek"] => {
        if (!week || !safeCopy(week.lead)) return undefined;
        return {
            ...week,
            time: week.time?.filter((row) => row.other || safeCopy(row.name ?? "")),
            done: week.done?.filter((row) => safeCopy(row.title ?? "")),
            open: week.open?.filter((row) => safeCopy(row.title)).map((row) => (
                row.label && !(safeCopy(row.label) && !maskedSubject.test(row.label)) ? { ...row, label: undefined, cardId: undefined } : row
            )),
            learned: week.learned?.filter((row) => safeCopy(row.text)),
            ask: week.ask && safeCopy(week.ask.text) && safeCopy(week.ask.message) ? week.ask : undefined,
        };
    };
    const sections = { ...brief.sections };
    for (const name of Object.keys(sections) as Array<keyof typeof sections>) {
        const items = sections[name];
        if (items) sections[name] = safeItems(items);
    }
    const positions = new Map<number, number>();
    let narrative = (brief.narrative ?? []).flatMap((paragraph, index) => {
        const text = paragraph.text;
        if (!safeCopy(text)) return [];
        positions.set(index, positions.size);
        return [{ ...paragraph, text, ...(!briefCopySafe(paragraph.topicName ?? "") ? { topicId: undefined, topicName: undefined } : {}) }];
    });
    // The latest brief always shows its words (Bo, 2026-11-15): a check that
    // would leave none keeps them all. It may leave out a flagged paragraph
    // only while others remain.
    if (narrative.length === 0 && (brief.narrative?.length ?? 0) > 0) {
        positions.clear();
        narrative = brief.narrative.map((paragraph, index) => {
            positions.set(index, index);
            return paragraph;
        });
    }
    const changed = narrative.length !== brief.narrative?.length || narrative.some((para, i) => para.text !== brief.narrative[i]?.text);
    const closing = narrative.at(-1)?.text;
    const closingWithheld = closing !== brief.narrative?.at(-1)?.text;
    return {
        ...brief,
        narrative,
        sections,
        upcoming: brief.upcoming ? safeItems(brief.upcoming) : undefined,
        evidence: (brief.evidence ?? []).filter((ref) => safeCopy(ref.label)),
        weeklyRecap: brief.weeklyRecap?.filter((para) => safeCopy(para.text)),
        yourWeek: safeWeek(brief.yourWeek),
        weekQuestion: brief.weekQuestion && safeCopy(brief.weekQuestion.recommendation ?? "") && !maskedSubject.test(brief.weekQuestion.recommendation ?? "")
            ? brief.weekQuestion : undefined,
        // The recording stays with the brief: a left-out paragraph or a
        // display-only rewrite does not take the play button away. A brief
        // whose own words carry a privacy mask is not played.
        audio: maskedSubject.test(JSON.stringify(original.narrative ?? [])) ? undefined : brief.audio,
        cards: brief.cards?.flatMap((card, index) => {
            const source = original.cards?.[index] ?? card;
            if (maskedSubject.test(source.label ?? "") || maskedSubject.test(source.message)) return [];
            if (!briefCopySafe(card.label ?? "") || !briefCopySafe(card.message)
                || maskedSubject.test(card.label ?? "") || maskedSubject.test(card.message)) return [];
            if (card.kind === "action" && (closingWithheld || changed && !/[?？]/.test(closing ?? ""))) return [];
            if (card.paragraph === undefined) return [card];
            const paragraph = positions.get(card.paragraph);
            if (paragraph === undefined) {
                return card.kind === "item" ? [{ ...card, paragraph: undefined, label: undefined }] : [];
            }
            return [{ ...card, paragraph }];
        }),
    };
}
