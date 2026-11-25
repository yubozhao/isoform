import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { ChevronLeft, RotateCcw, RotateCw } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Textarea } from "@/components/ui/textarea";
import { briefPlaybackRate, onBriefPlaybackProgress, seekBriefPlayback, setBriefPlaybackRate, toggleBriefPlayback } from "@/lib/brief-audio";
import { briefForDisplay } from "@/lib/brief-hygiene";
import { briefHasWords, briefTitle } from "@/lib/brief-title";
import { measureToday } from "@/lib/today-telemetry";
import { ListPastBriefs, type MobileBriefAudioMark, type MobileBriefDocument, type MobileBriefSummary, type TodayBriefView, type WorkAnswerInput, type WorkItem } from "@/lib/wails-shim-app";
import { onPagePrivacyWithdrawal } from "@/lib/page-privacy-withdrawal";
import { briefKind } from "@/lib/brief-title";
import { clockTime } from "@/lib/brief-time";
import type { WorkAnswerError } from "@/hooks/useWorkList";
import { briefSourcesByKind, briefSourcesLine } from "@/lib/brief-sources";
import { BriefAudioPlayer } from "./BriefAudioPlayer";
import { BriefSourceWhy } from "./BriefSourceWhy";
import { spaceIsFree } from "./TodayBriefBand";

type Translate = (key: string, options?: Record<string, unknown>) => string;

/** Seconds ← and → move the brief (computer-brief v1.0.0 rule 2). */
export const SEEK_SECONDS = 15;
export const SPEEDS = [1, 1.25, 1.5, 2] as const;
/** The reasons a sentence isn't right (phone-brief v1.0.0, rule 7 here). */
export const NOT_RIGHT_REASONS = ["didnt_happen", "wrong_person", "already_done", "not_mine"] as const;

/** The Your work id of one sentence of a brief (B2: brief:<briefId>:p<index>). */
export function sentenceWorkId(briefId: string, index: number): string {
    return `brief:${briefId}:p${index}`;
}

function arrowIsFree(event: KeyboardEvent): boolean {
    if (event.key !== "ArrowLeft" && event.key !== "ArrowRight") return false;
    if (event.defaultPrevented || event.metaKey || event.ctrlKey || event.altKey || event.shiftKey) return false;
    const target = event.target instanceof Element ? event.target : null;
    if (target?.closest('input, textarea, select, [contenteditable]:not([contenteditable="false"]), [role="textbox"], [role="slider"]')) return false;
    return !document.querySelector('[role="dialog"], [role="alertdialog"]');
}

/** Where the voice is (computer-brief v1.0.0 rule 3): the narrative
 *  sentence being read, from the brief's sentence marks (#1199): its
 *  paragraph and its UTF-16 span in the shown text. */
export function readingAt(position: number, marks: readonly MobileBriefAudioMark[] | undefined): { paragraph: number; from: number; to: number } | null {
    const mark = (marks ?? []).find((m) => m.source === "narrative" && position >= m.start && position < m.end);
    return mark ? { paragraph: mark.paragraph, from: mark.from, to: mark.to } : null;
}

function clock(seconds: number): string {
    const total = Math.max(0, Math.round(seconds));
    return `${Math.floor(total / 60)}:${String(total % 60).padStart(2, "0")}`;
}

/**
 * The brief in full on the computer (computer-brief v1.0.0, 19a): in Today's
 * column with ‹ Today, the player (Space plays, ← and → move 15 seconds, the
 * speed), every paragraph with Reply about this and Not right, the brief's
 * own Your work items under the paragraph that mentions them, and on the
 * right In this brief (its sources by kind, each opening its Why) above
 * Past briefs. Drawn from what this computer made.
 */
export function TodayBriefFull({ brief, view, active, items = [], onAnswer, onUndo, onHandOff, onReply, onNotRight, onOpenBrief, onBack, extras = true }: {
    /** The brief's extras (the brief-extras switch, scope cut 2026-11 B2).
     *  Off: Hear it is play, pause and a bar (no speed, no 15 s seek, no
     *  read-along), no items under paragraphs, and one sources line in place
     *  of In this brief by kind. */
    extras?: boolean;
    onUndo?: (id: string) => Promise<WorkAnswerError | null>;
    /** A Yes on agent work opens the hand-off sheet (SH4). */
    onHandOff?: (item: WorkItem) => void;
    brief: MobileBriefDocument;
    view: TodayBriefView;
    active: boolean;
    /** Your work's items from this brief, placed by their paragraph. */
    items?: WorkItem[];
    onAnswer?: (id: string, input: WorkAnswerInput) => Promise<WorkAnswerError | null>;
    onReply?: (index: number, text: string) => void;
    onNotRight?: (id: string, input: WorkAnswerInput) => Promise<WorkAnswerError | null>;
    onOpenBrief: (briefId: string, title: string) => void;
    onBack: () => void;
}) {
    const { t, i18n } = useTranslation("today");
    const tr: Translate = (key, options) => t(key as never, options as never) as unknown as string;
    const language = i18n.language;
    const displayed = briefForDisplay(brief);
    const title = briefTitle(displayed, language, tr, view.briefRun);
    const madeAt = new Date(displayed.asOf);
    const day = Number.isNaN(madeAt.getTime()) ? "" : madeAt.toLocaleDateString(language, { weekday: "long", month: "long", day: "numeric" });
    const audio = displayed.audio && (!displayed.audio.status || displayed.audio.status === "ok") && displayed.audio.bytes > 0 && briefHasWords(displayed) ? displayed.audio : null;
    const paragraphs = (displayed.narrative ?? []).map((paragraph) => paragraph.text?.trim() ?? "");
    const sources = briefSourcesByKind(displayed);
    // Read-along follows the voice until the person scrolls away; Play
    // follows again.
    const [follow, setFollow] = useState(true);
    const readingRef = useRef<HTMLSpanElement | null>(null);
    const [progress, setProgress] = useState({ position: 0, duration: audio?.durationSeconds ?? 0 });
    const [speed, setSpeed] = useState(briefPlaybackRate());
    const [notRight, setNotRight] = useState<number | null>(null);

    const [playing, setPlaying] = useState(false);
    useEffect(() => onBriefPlaybackProgress((next) => {
        if (next.briefId !== brief.briefId) return;
        setProgress({ position: next.position, duration: next.duration || audio?.durationSeconds || 0 });
        setPlaying((was) => {
            const now = next.state === "playing";
            if (now && !was) setFollow(true);
            return now;
        });
    }), [brief.briefId, audio?.durationSeconds]);
    const reading = playing && extras ? readingAt(progress.position, audio?.marks) : null;
    const readingKey = reading ? `${reading.paragraph}:${reading.from}` : "";
    useEffect(() => {
        if (follow && readingKey) readingRef.current?.scrollIntoView?.({ block: "nearest", behavior: "smooth" });
    }, [follow, readingKey]);
    useEffect(() => {
        if (!active || !audio) return;
        const onKey = (event: KeyboardEvent) => {
            if (spaceIsFree(event)) {
                event.preventDefault();
                void measureToday("T08", "do", "done", () => toggleBriefPlayback(brief.briefId).catch(() => {}));
            } else if (extras && arrowIsFree(event)) {
                event.preventDefault();
                seekBriefPlayback(brief.briefId, event.key === "ArrowLeft" ? -SEEK_SECONDS : SEEK_SECONDS);
            } else if (event.key === "Escape" && !document.querySelector('[role="dialog"]')) {
                onBack();
            }
        };
        document.addEventListener("keydown", onKey);
        return () => document.removeEventListener("keydown", onKey);
    }, [active, audio, brief.briefId, onBack, extras]);

    const itemsFor = (index: number) => items.filter((item) => item.ref?.briefId === brief.briefId && Number(item.ref?.paragraph) === index);
    const share = progress.duration > 0 ? Math.min(1, progress.position / progress.duration) : 0;

    return (
        <section data-testid="today-brief-full" aria-label={title} className="flex flex-col gap-5">
            <button type="button" className="flex items-center gap-0.5 self-start text-[13px] font-medium text-accent" onClick={onBack} data-testid="brief-full-back">
                <ChevronLeft aria-hidden className="size-3.5" />{tr("cbrief.back")}
            </button>
            <div className="grid grid-cols-1 items-start gap-6 md:grid-cols-[minmax(0,1fr)_240px]">
                <div className="flex min-w-0 flex-col gap-4">
                    <header className="flex flex-col gap-0.5">
                        <h2 className="text-[20px] font-semibold text-ink">{title}</h2>
                        {day && <p className="text-[12.5px] text-muted">{tr("cbrief.madeHere", { day })}</p>}
                    </header>
                    {audio && (
                        <div className="flex flex-col gap-2 rounded-xl border border-border bg-panel px-4 py-3 shadow-card" data-testid="brief-full-player">
                            <div className="flex flex-wrap items-center gap-3">
                                <BriefAudioPlayer key={brief.briefId} briefId={brief.briefId} audio={audio} caption={title} />
                                {extras && <span className="ml-auto flex items-center gap-1">
                                    <Button size="icon" variant="quiet" aria-label={tr("cbrief.back15")} aria-keyshortcuts="ArrowLeft" onClick={() => seekBriefPlayback(brief.briefId, -SEEK_SECONDS)}><RotateCcw className="size-4" /></Button>
                                    <Button size="icon" variant="quiet" aria-label={tr("cbrief.on15")} aria-keyshortcuts="ArrowRight" onClick={() => seekBriefPlayback(brief.briefId, SEEK_SECONDS)}><RotateCw className="size-4" /></Button>
                                    <Button size="sm" variant="quiet" aria-label={tr("cbrief.speed")} data-testid="brief-full-speed" onClick={() => {
                                        const next = SPEEDS[(SPEEDS.indexOf(speed as typeof SPEEDS[number]) + 1) % SPEEDS.length];
                                        setSpeed(next);
                                        setBriefPlaybackRate(next);
                                    }}>{`${speed}×`}</Button>
                                </span>}
                            </div>
                            <div className="flex items-center gap-2 text-[11.5px] tabular-nums text-muted">
                                <span>{clock(progress.position)}</span>
                                <span className="relative h-1 flex-1 overflow-hidden rounded-full bg-surface-active" aria-hidden>
                                    <span className="absolute inset-y-0 left-0 rounded-full bg-accent" style={{ width: `${share * 100}%` }} />
                                </span>
                                <span>{clock(progress.duration)}</span>
                            </div>
                        </div>
                    )}
                    <div className="flex flex-col gap-4" data-testid="brief-full-text" onWheel={() => setFollow(false)} onTouchMove={() => setFollow(false)}>
                        {paragraphs.map((text, index) => text && (
                            <div key={index} className="group flex flex-col gap-2">
                                <p className="text-[15px] leading-[1.65] text-ink">
                                    {reading?.paragraph === index ? <>
                                        {text.slice(0, reading.from)}
                                        <span ref={readingRef} data-reading className="rounded bg-surface-active">{text.slice(reading.from, reading.to)}</span>
                                        {text.slice(reading.to)}
                                    </> : text}
                                </p>
                                {extras && itemsFor(index).map((item) => <CompactItem key={item.id} item={item} onAnswer={onAnswer} onUndo={onUndo} onHandOff={onHandOff} tr={tr} />)}
                                <div className="flex gap-3 text-[12.5px] opacity-0 transition-opacity focus-within:opacity-100 group-hover:opacity-100" data-testid="brief-full-actions">
                                    {onReply && <button type="button" className="font-medium text-accent" onClick={() => onReply(index, text)}>{tr("cbrief.reply")}</button>}
                                    {onNotRight && <button type="button" className="font-medium text-muted hover:text-ink" onClick={() => setNotRight(index)} data-testid="brief-full-not-right">{tr("cbrief.notRight")}</button>}
                                </div>
                            </div>
                        ))}
                    </div>
                </div>
                <aside className="flex flex-col gap-2" data-testid="brief-full-past">
                    {!extras && sources.length > 0 && (
                        <p className="mb-3 px-2.5 text-[12.5px] text-muted" data-testid="brief-full-sources-line">{briefSourcesLine(displayed, (kind) => tr(`cbrief.kind.${kind}`))}</p>
                    )}
                    {extras && sources.length > 0 && (
                        <section className="mb-3 flex flex-col gap-1" data-testid="brief-full-sources" aria-label={tr("cbrief.inBrief")}>
                            <h3 className="text-[11px] font-semibold uppercase tracking-wider text-muted">{tr("cbrief.inBrief")}</h3>
                            {sources.map((group) => <BriefSourceWhy key={group.kind} group={group} />)}
                            <p className="px-2.5 text-[11.5px] text-muted">{tr("cbrief.eachWhy")}</p>
                        </section>
                    )}
                    <h3 className="text-[11px] font-semibold uppercase tracking-wider text-muted">{tr("ctoday.pastBriefs")}</h3>
                    <PastBriefsWithState currentBriefId={brief.briefId} onOpen={onOpenBrief} tr={tr} />
                    <p className="px-2.5 text-[11.5px] text-muted">{tr("cbrief.pastFoot")}</p>
                </aside>
            </div>
            {notRight !== null && onNotRight && (
                <NotRightSheet
                    sentence={paragraphs[notRight]}
                    onClose={() => setNotRight(null)}
                    onSend={(input) => onNotRight(sentenceWorkId(brief.briefId, notRight), input)}
                />
            )}
        </section>
    );
}

/** Past briefs by day, newest first, with where the person is with each
 *  (computer-brief v1.0.0 rule 9; ListPastBriefs with #1199's state): a filled dot until
 *  played, an open ring once heard. */
function PastBriefsWithState({ currentBriefId, onOpen, tr }: { currentBriefId: string; onOpen: (briefId: string, title: string) => void; tr: Translate }) {
    const { i18n } = useTranslation("today");
    const [rows, setRows] = useState<MobileBriefSummary[] | null>(null);
    const [failed, setFailed] = useState(false);
    const [attempt, setAttempt] = useState(0);
    useEffect(() => {
        let live = true;
        setFailed(false);
        Promise.resolve().then(() => ListPastBriefs()).then((next) => { if (live) setRows(next ?? []); }).catch(() => { if (live) setFailed(true); });
        const withdraw = onPagePrivacyWithdrawal(() => { live = false; setRows(null); });
        return () => { live = false; withdraw(); };
    }, [attempt]);
    const shown = (rows ?? []).filter((row) => row.briefId !== currentBriefId);
    return (
        <div className="flex flex-col gap-1">
            {shown.map((row) => {
                const at = new Date(row.asOf);
                const kind = briefKind({ slot: row.slot, asOf: row.asOf });
                const title = Number.isNaN(at.getTime()) ? tr("todayBrief.slot.on_request") : tr("todayBrief.pending.lastBriefTitle", {
                    brief: tr(`todayBrief.slot.${kind === "morning" || kind === "evening" ? kind : "on_request"}`),
                    day: at.toLocaleDateString(i18n.language, { weekday: "short", month: "short", day: "numeric" }),
                    time: clockTime(at, i18n.language, tr),
                });
                const played = !!row.played || row.listened === "part_heard" || row.listened === "played";
                const left = row.toAnswer ?? 0;
                const where = !played ? tr("cbrief.notPlayed")
                    : left > 0 ? tr("cbrief.leftToAnswer", { count: left })
                    : (row.answered ?? 0) > 0 ? tr("cbrief.allAnswered") : "";
                return (
                    <button key={row.briefId} type="button" data-testid="past-brief-row" className="flex items-start gap-2 rounded-lg px-2.5 py-2 text-left outline-none hover:bg-surface-hover focus-visible:ring-2 focus-visible:ring-border-focus" onClick={() => measureToday("T05", "open", "opened", () => onOpen(row.briefId, title))}>
                        <span aria-hidden data-played={played} className={!played ? "mt-1.5 size-2 shrink-0 rounded-full bg-accent" : "mt-1.5 size-2 shrink-0 rounded-full border border-muted"} />
                        <span className="flex min-w-0 flex-col gap-0.5">
                            <span className="text-[13px] font-medium text-ink">{title}</span>
                            {where && <span className="text-[12px] text-muted">{where}</span>}
                        </span>
                    </button>
                );
            })}
            {rows && shown.length === 0 && !failed && <p data-testid="past-briefs-empty" className="px-2.5 text-[13px] text-muted">{tr("ctoday.pastBriefsEmpty")}</p>}
            {failed && <p role="alert" data-testid="past-briefs-failed" className="px-2.5 text-[13px] text-copper">{tr("ctoday.pastBriefsFailed")}{" "}<button type="button" className="font-medium text-accent" onClick={() => setAttempt((n) => n + 1)}>{tr("ctoday.tryAgain")}</button></p>}
        </div>
    );
}

/** The answer a key means on a brief's item (computer-brief v1.0.0 rule 4):
 *  Y, N and M; Useful and Not useful (Y, N) for a result back. */
export function compactKeyAction(item: WorkItem, key: string): "yes" | "no" | "not_mine" | "useful" | "not_useful" | null {
    const back = item.state === "back";
    switch (key) {
    case "y": return back ? "useful" : "yes";
    case "n": return back ? "not_useful" : "no";
    case "m": return back ? null : "not_mine";
    default: return null;
    }
}

/** A brief's item where the brief mentions it (computer-brief v1.0.0 rule
 *  4): one line and its answers with their keys; ↑ ↓ move between the
 *  brief's items; an answered one is one line with Undo. A Yes on agent work
 *  opens the hand-off sheet, as in Your work. */
function CompactItem({ item, onAnswer, onUndo, onHandOff, tr, now = new Date() }: {
    item: WorkItem;
    onAnswer?: (id: string, input: WorkAnswerInput) => Promise<WorkAnswerError | null>;
    onUndo?: (id: string) => Promise<WorkAnswerError | null>;
    onHandOff?: (item: WorkItem) => void;
    tr: Translate;
    now?: Date;
}) {
    const [busy, setBusy] = useState(false);
    const [error, setError] = useState<WorkAnswerError | null>(null);
    const back = item.state === "back";
    const agentWork = !back && item.yes.does === "agent";
    const answer = (action: string) => {
        if (busy) return;
        if (action === "yes" && agentWork) { onHandOff?.(item); return; }
        if (!onAnswer) return;
        setBusy(true);
        setError(null);
        void measureToday("W01", "do", "done", () => onAnswer(item.id, { action })).then((failed) => setError(failed ?? null)).finally(() => setBusy(false));
    };
    const undo = () => {
        if (!onUndo || busy) return;
        setBusy(true);
        setError(null);
        void measureToday("W02", "do", "done", () => onUndo(item.id)).then((failed) => setError(failed ?? null)).finally(() => setBusy(false));
    };
    const onKeyDown = (event: React.KeyboardEvent<HTMLElement>) => {
        if (event.target !== event.currentTarget || event.metaKey || event.ctrlKey || event.altKey || event.shiftKey || event.nativeEvent.isComposing) return;
        if (event.key === "ArrowDown" || event.key === "ArrowUp") {
            const all = Array.from(event.currentTarget.closest("[data-testid='brief-full-text']")?.querySelectorAll<HTMLElement>("[data-brief-item]") ?? []);
            const next = all[all.indexOf(event.currentTarget) + (event.key === "ArrowDown" ? 1 : -1)];
            if (next) { event.preventDefault(); next.focus(); }
            return;
        }
        if (item.state !== "open" && item.state !== "back") return;
        const action = compactKeyAction(item, event.key.toLowerCase());
        if (!action || (action === "yes" && agentWork && !onHandOff)) return;
        event.preventDefault();
        answer(action);
    };
    const shared = { "data-brief-item": "", "data-item-id": item.id, tabIndex: 0, role: "article", onKeyDown } as const;
    if (item.state !== "open" && item.state !== "back") {
        const until = Date.parse(item.answer?.undoUntil ?? "");
        const undoable = !!onUndo && Number.isFinite(until) && until > now.getTime();
        return (
            <div {...shared} aria-label={item.answer?.line || item.work.title} className="flex items-center justify-between gap-2 rounded-lg bg-surface-active px-3 py-1.5 outline-none focus-visible:ring-2 focus-visible:ring-border-focus" data-testid="brief-full-item">
                <span className="min-w-0 text-[12.5px] text-muted">{item.answer?.line || item.work.title}</span>
                {undoable && <button type="button" className="shrink-0 text-[12px] font-medium text-accent" disabled={busy} onClick={undo} data-testid="brief-full-item-undo">{tr("work.undo")}</button>}
                {error && <span role="alert" className="text-[12px] text-copper">{tr(`work.error.${error}`)}</span>}
            </div>
        );
    }
    return (
        <div {...shared} aria-label={item.work.title} aria-keyshortcuts={back ? "Y N" : "Y N M"} className="flex flex-wrap items-center justify-between gap-2 rounded-lg border border-border px-3 py-2 outline-none focus-visible:ring-2 focus-visible:ring-border-focus" data-testid="brief-full-item">
            <span className="min-w-0 text-[13px] font-medium text-ink">{item.work.title}</span>
            <span className="flex gap-1.5">
                {(!agentWork || onHandOff) && <KeyButton label={tr(back ? "work.useful" : "work.yes")} keyName="Y" primary disabled={busy} onClick={() => answer(back ? "useful" : "yes")} />}
                <KeyButton label={tr(back ? "work.notUseful" : "work.no")} keyName="N" disabled={busy} onClick={() => answer(back ? "not_useful" : "no")} />
                {!back && <KeyButton label={tr("work.notMine")} keyName="M" disabled={busy} onClick={() => answer("not_mine")} />}
            </span>
            {error && <p role="alert" className="w-full text-[12px] text-copper">{tr(`work.error.${error}`)}</p>}
        </div>
    );
}

function KeyButton({ label, keyName, primary = false, disabled, onClick }: { label: string; keyName: string; primary?: boolean; disabled?: boolean; onClick: () => void }) {
    return (
        <Button size="sm" variant={primary ? "default" : "outline"} disabled={disabled} aria-keyshortcuts={keyName} onClick={onClick}>
            {label}<kbd aria-hidden className="ml-1.5 text-[11px] opacity-60">{keyName}</kbd>
        </Button>
    );
}

/** Not right (computer-brief v1.0.0, 19b): a No on one sentence, with an
 *  optional reason or the person's own words. */
function NotRightSheet({ sentence, onSend, onClose }: {
    sentence: string;
    onSend: (input: WorkAnswerInput) => Promise<WorkAnswerError | null>;
    onClose: () => void;
}) {
    const { t } = useTranslation("today");
    const tr: Translate = (key, options) => t(key as never, options as never) as unknown as string;
    const [reason, setReason] = useState<string>("");
    const [words, setWords] = useState("");
    const [busy, setBusy] = useState(false);
    const [error, setError] = useState<WorkAnswerError | null>(null);
    const send = async () => {
        if (busy) return;
        setBusy(true);
        setError(null);
        const input: WorkAnswerInput = { action: "no", ...(reason ? { reason } : {}), ...(words.trim() ? { after: words.trim() } : {}) };
        const failed = await measureToday("W04", "do", "done", () => onSend(input));
        setBusy(false);
        if (failed) setError(failed);
        else onClose();
    };
    return (
        <Dialog open onOpenChange={(open) => { if (!open) onClose(); }}>
            <DialogContent data-testid="not-right-sheet" className="flex max-w-[520px] flex-col gap-0 p-0">
                <DialogHeader>
                    <DialogTitle>{tr("cbrief.notRightTitle")}</DialogTitle>
                </DialogHeader>
                <div className="flex flex-col gap-3 px-6 py-4">
                    <blockquote className="border-border border-y py-2 text-[14px] leading-[1.55] text-ink">{sentence}</blockquote>
                    <DialogDescription className="text-[12.5px] text-muted">{tr("cbrief.whatsWrong")}</DialogDescription>
                    <div className="flex flex-wrap gap-1.5">
                        {NOT_RIGHT_REASONS.map((key) => (
                            <Button key={key} size="sm" variant={reason === key ? "default" : "outline"} aria-pressed={reason === key} onClick={() => setReason(reason === key ? "" : key)}>{tr(`cbrief.reason.${key}`)}</Button>
                        ))}
                    </div>
                    <Textarea
                        aria-label={tr("cbrief.sayRight")}
                        placeholder={tr("cbrief.sayRight")}
                        value={words}
                        onChange={(event) => setWords(event.target.value)}
                        onKeyDown={(event) => { if (event.key === "Enter" && event.metaKey) { event.preventDefault(); void send(); } }}
                        className="min-h-[72px] text-[14px]"
                        data-testid="not-right-words"
                    />
                    <p className="text-[12px] text-muted">{tr("cbrief.follows")}</p>
                    {error && <p role="alert" className="text-[12.5px] text-copper" data-testid="not-right-error">{tr(`work.error.${error}`)}</p>}
                </div>
                <footer className="flex items-center justify-end gap-2 border-t border-border-divider px-6 py-4">
                    <Button variant="quiet" onClick={onClose}>{tr("work.cancel")}</Button>
                    <Button disabled={busy} onClick={() => void send()} data-testid="not-right-send">{tr("cbrief.notRightSend")} <kbd className="ml-1.5 text-[11px] opacity-70">⌘↩</kbd></Button>
                </footer>
            </DialogContent>
        </Dialog>
    );
}
