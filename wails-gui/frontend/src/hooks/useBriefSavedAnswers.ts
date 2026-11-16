import { useEffect, useState } from "react";
import { PageEventsOn } from "@/lib/page-queries";
import { GetTodayPanelAnswer, type MobileBriefDocument } from "@/lib/wails-shim-app";
import { todayBriefCardRef } from "@/lib/today-chat-navigation";

/** Checks only the brief's displayed questions. Saved text stays in Chat. */
export function useBriefSavedAnswers(brief: MobileBriefDocument | null | undefined, active: boolean): ReadonlySet<string> {
    const [answered, setAnswered] = useState<ReadonlySet<string>>(new Set());
    useEffect(() => {
        let current = true;
        let generation = 0;
        const refresh = () => {
            const request = ++generation;
            if (!active || !brief) { setAnswered(new Set()); return; }
            const cards = (brief.cards ?? []).filter((card) => card.mode === "send" && card.message?.trim() && (card.kind === "question" || card.kind === "item"));
            void Promise.all(cards.map(async (card) => {
                try { return await GetTodayPanelAnswer(todayBriefCardRef(brief.briefId, card.id, card.message)) ? card.id : null; }
                catch { return null; }
            })).then((ids) => { if (current && request === generation) setAnswered(new Set(ids.filter((id): id is string => id !== null))); });
        };
        refresh();
        const offs = ["today:answer-saved", "memory:withdrawn", "sensitive-apps:updated", "closed-eyes:changed", "brief:stored", "handoff:updated", "calendar:synced", "topics:withdrawn"].map((event) => PageEventsOn(event, refresh));
        return () => { current = false; offs.forEach((off) => off()); };
    }, [brief, active]);
    return answered;
}
