import { useEffect } from "react";
import { useTranslation } from "react-i18next";
import { ChevronRight, Clock } from "lucide-react";
import type { MakeBriefState } from "@/hooks/useLatestBrief";
import { assistantLabel } from "@/lib/brief-assistant";
import { toggleBriefPlayback } from "@/lib/brief-audio";
import { briefForDisplay } from "@/lib/brief-hygiene";
import { briefClockTime, briefSlotKey } from "@/lib/brief-time";
import { briefHasWords, briefTitle } from "@/lib/brief-title";
import { measureToday } from "@/lib/today-telemetry";
import type { MobileBriefDocument, TodayBriefView } from "@/lib/wails-shim-app";
import { BriefAudioPlayer } from "./BriefAudioPlayer";

type Translate = (key: string, options?: Record<string, unknown>) => string;

/** How many sentences of the brief the band reads before Read all. */
export const BAND_SENTENCES = 3;

/** The brief's first sentences, in order, across its paragraphs. */
export function firstSentences(paragraphs: readonly string[], count = BAND_SENTENCES, language = "en"): string {
    const out: string[] = [];
    for (const paragraph of paragraphs) {
        const text = paragraph.trim();
        if (!text) continue;
        for (const sentence of splitSentences(text, language)) {
            out.push(sentence);
            if (out.length >= count) return out.join(" ");
        }
    }
    return out.join(" ");
}

function splitSentences(text: string, language: string): string[] {
    try {
        const segmenter = new Intl.Segmenter(language, { granularity: "sentence" });
        return [...segmenter.segment(text)].map((part) => part.segment.trim()).filter(Boolean);
    } catch {
        return text.match(/[^.!?。！？]+[.!?。！？]+["'”’)\]]*|[^.!?。！？]+$/g)?.map((part) => part.trim()).filter(Boolean) ?? [text];
    }
}

/** Space plays and pauses only when no field, control or sheet has the key. */
export function spaceIsFree(event: KeyboardEvent): boolean {
    if (event.code !== "Space" && event.key !== " ") return false;
    if (event.repeat || event.defaultPrevented || event.metaKey || event.ctrlKey || event.altKey || event.shiftKey) return false;
    const target = event.target instanceof Element ? event.target : null;
    if (target?.closest('input, textarea, select, button, a[href], [contenteditable]:not([contenteditable="false"]), [role="button"], [role="textbox"], [role="combobox"], [role="menuitem"], [role="option"]')) return false;
    return !document.querySelector('[role="dialog"], [role="alertdialog"]');
}

/**
 * Today's brief band (computer-today v1.0.0, SH1): the latest brief across
 * the top. On the left Hear it, the brief's kind and time, its first three
 * sentences and Read all; on the right what is in it and Past briefs. Space
 * plays and pauses anywhere on Today. The next brief being made, or a try
 * that failed, is one line above the brief, which stays (phone-today rule 7).
 */
export function TodayBriefBand({ brief, view, make, onMakeNow, showLead = true, readAll = false, onReadAll, onOpenPastBriefs, active, firstCaption }: {
    brief: MobileBriefDocument;
    view: TodayBriefView;
    make: MakeBriefState;
    onMakeNow: () => void;
    /** The first sentences and Read all. Off while today's full card is shown
     *  under the band, so the brief's words are never on screen twice. */
    showLead?: boolean;
    readAll?: boolean;
    onReadAll?: () => void;
    onOpenPastBriefs?: () => void;
    /** Today is the page in front: only then does Space play. */
    active: boolean;
    /** The first day's caption, which names the brief in place of its time. */
    firstCaption?: string;
}) {
    const { t, i18n } = useTranslation("today");
    const tr: Translate = (key, options) => t(key as never, options as never) as unknown as string;
    const language = i18n.language;
    const displayed = briefForDisplay(brief);
    const title = firstCaption ?? briefTitle(displayed, language, tr, view.briefRun);
    const audio = displayed.audio && (!displayed.audio.status || displayed.audio.status === "ok") && displayed.audio.bytes > 0 && briefHasWords(displayed) ? displayed.audio : null;
    const preparing = !audio && displayed.audio?.status === "rendering" && briefHasWords(displayed);
    const lead = firstSentences((displayed.narrative ?? []).map((paragraph) => paragraph.text), BAND_SENTENCES, language);
    const slot = briefSlotKey(displayed.slot, displayed.asOf, displayed.timezone);
    const meetings = displayed.sections?.meetings?.length ?? 0;
    const toAnswer = (displayed.cards ?? []).filter((card) => card.kind === "item" && !!card.label?.trim()).length;

    // The next brief: being made, or a try that failed. The brief on screen
    // stays under its own time either way.
    const pending = view.pendingSlot === "morning" || view.pendingSlot === "evening" ? view.pendingSlot : null;
    const building = make === "making" || view.building;
    const nextSlot = pending ?? (slot === "morning" ? "evening" : "morning");
    const failedTry = !building && (make === "failed" || (!!pending && (view.pendingAttempts ?? 0) > 0));
    const ai = assistantLabel(view.pendingAssistant, { claudeCode: tr("status.assistantClaudeCode"), codex: tr("status.assistantCodex"), yourAI: tr("todayBrief.pending.yourAI") });
    const reason = make !== "failed" && view.pendingCause === "timeout" ? tr("ctoday.reason.timeout")
        : make !== "failed" && view.pendingCause === "signed_out" ? tr("ctoday.reason.signedOut", { ai })
        : tr("ctoday.reason.other");
    const failedKind = make === "failed" && !pending ? "on_request" : nextSlot;

    useEffect(() => {
        if (!active || !audio) return;
        const onKey = (event: KeyboardEvent) => {
            if (!spaceIsFree(event)) return;
            event.preventDefault();
            void measureToday("T08", "do", "done", () => toggleBriefPlayback(brief.briefId).catch(() => {}));
        };
        document.addEventListener("keydown", onKey);
        return () => document.removeEventListener("keydown", onKey);
    }, [active, audio, brief.briefId]);

    return (
        <section
            aria-label={title}
            aria-keyshortcuts={audio ? "Space" : undefined}
            data-testid="today-brief-band"
            className="grid grid-cols-1 gap-6 rounded-xl border border-border bg-panel px-[26px] py-6 shadow-card md:grid-cols-[minmax(0,1fr)_220px]"
        >
            <div className="flex min-w-0 flex-col gap-3.5">
                {building ? (
                    <p role="status" className="text-[12.5px] text-muted" data-testid="brief-band-making">{tr(`ctoday.making.${nextSlot}`)}</p>
                ) : failedTry ? (
                    <p role="status" className="text-[12.5px] text-copper" data-testid="brief-band-couldnt">
                        {tr("ctoday.couldnt", { kind: tr(`ctoday.kind.${failedKind}`), reason })}{" "}
                        <button type="button" className="font-medium text-accent" onClick={() => measureToday("T04", "do", "done", onMakeNow)}>{tr("ctoday.tryAgain")}</button>
                    </p>
                ) : null}
                {audio ? (
                    <BriefAudioPlayer key={brief.briefId} briefId={brief.briefId} audio={audio} caption={title} />
                ) : preparing ? (
                    <div role="status" className="text-[12px] text-muted" data-testid="brief-audio-preparing">{tr(displayed.audio?.updating ? "todayBrief.updatingAudio" : "todayBrief.preparingAudio", { brief: title })}</div>
                ) : (
                    <div className="text-[12px] text-muted" data-testid="brief-band-title">{title}</div>
                )}
                {showLead && !readAll && lead && (
                    <p className="text-[15px] leading-[1.6] text-ink" data-testid="brief-band-lead">{lead}</p>
                )}
                {showLead && onReadAll && <button
                    type="button"
                    aria-expanded={readAll}
                    data-testid="brief-band-read-all"
                    className="flex items-center gap-0.5 self-start rounded-sm text-[13px] font-medium text-accent outline-none focus-visible:ring-2 focus-visible:ring-border-focus"
                    onClick={() => measureToday("T05", "open", "opened", onReadAll)}
                >
                    {tr(readAll ? "ctoday.showLess" : "ctoday.readAll")}
                    <ChevronRight aria-hidden className={readAll ? "size-3.5 -rotate-90" : "size-3.5"} />
                </button>}
            </div>
            <div className="flex flex-col gap-2.5 border-t border-border-divider pt-4 md:border-l md:border-t-0 md:pl-6 md:pt-0" data-testid="brief-band-contents">
                <h2 className="text-[11px] font-semibold uppercase tracking-wider text-muted">{tr("ctoday.inBrief")}</h2>
                {meetings > 0 && <p className="text-[13px] text-ink">{tr(slot === "evening" ? "ctoday.meetings" : "ctoday.meetingsToday", { count: meetings })}</p>}
                {toAnswer > 0 && <p className="text-[13px] text-ink">{tr("ctoday.toAnswer", { count: toAnswer })}</p>}
                {onOpenPastBriefs && (
                    <button
                        type="button"
                        data-testid="brief-band-past"
                        className="mt-auto flex items-center gap-0.5 self-start rounded-sm text-[13px] font-medium text-accent outline-none focus-visible:ring-2 focus-visible:ring-border-focus"
                        onClick={() => measureToday("T05", "open", "opened", onOpenPastBriefs)}
                    >
                        {tr("ctoday.pastBriefs")}
                        <ChevronRight aria-hidden className="size-3.5" />
                    </button>
                )}
            </div>
        </section>
    );
}

/**
 * The band before the first brief (computer-today v1.1.0, 12a): a clock in
 * place of play, "Your first brief" at its time, and what comes meanwhile.
 * The brief takes its place when it is made (B1), so the band never goes
 * empty.
 */
export function TodayFirstBriefBand({ nextAt, timeZone }: { nextAt: string; timeZone?: string }) {
    const { t, i18n } = useTranslation("today");
    const tr: Translate = (key, options) => t(key as never, options as never) as unknown as string;
    const time = briefClockTime(nextAt, i18n.language, tr, timeZone);
    if (!time) return null;
    return (
        <section data-testid="today-first-brief-band" className="flex items-start gap-3.5 rounded-xl border border-border bg-panel px-[26px] py-6 shadow-card">
            <span aria-hidden className="flex size-9 shrink-0 items-center justify-center rounded-full bg-surface-active text-muted">
                <Clock className="size-4" />
            </span>
            <div className="flex min-w-0 flex-col gap-1">
                <p className="text-[13px] font-semibold text-ink">{tr("ctoday.firstTitle")} <span className="font-normal text-muted">{tr("ctoday.firstAt", { time })}</span></p>
                <p className="text-[14px] leading-[1.55] text-ink">{tr("ctoday.firstBody", { time })}</p>
            </div>
        </section>
    );
}
