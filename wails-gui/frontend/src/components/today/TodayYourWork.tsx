import { useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/textarea";
import { clockTime } from "@/lib/brief-time";
import { measureToday } from "@/lib/today-telemetry";
import type { WorkAnswerInput, WorkItem, WorkList } from "@/lib/wails-shim-app";
import type { WorkAnswerError } from "@/hooks/useWorkList";

type Translate = (key: string, options?: Record<string, unknown>) => string;

/** A hand-off taken back from its agent (phone-agent v1.2.1 rules 4-5); the
 *  computer sends it on the item as `takenBack`. */
type TakenBack = { agentId?: string; agentName: string; at: string; undoUntil?: string };
function takenBackOf(item: WorkItem): TakenBack | null {
    return (item as WorkItem & { takenBack?: TakenBack }).takenBack ?? null;
}

/** Items the person answers now: open ones, and results back from an agent. */
export function workToJudge(list: WorkList | null): WorkItem[] {
    return (list?.items ?? []).filter((item) => item.state === "open" || item.state === "back").sort((a, b) => a.order - b.order);
}

/** A time from the list in the reader's day words: the clock time today,
 *  "tomorrow 10:00 AM", otherwise the weekday and the time. */
export function workWhen(at: string | undefined, now: Date, language: string, tr: Translate): string {
    const date = at ? new Date(at) : null;
    if (!date || Number.isNaN(date.getTime())) return "";
    const day = (value: Date) => new Date(value.getFullYear(), value.getMonth(), value.getDate()).getTime();
    const days = Math.round((day(date) - day(now)) / 86_400_000);
    const time = clockTime(date, language, tr);
    if (days === 0) return time;
    if (days === 1) return tr("work.tomorrowAt", { time });
    if (days === -1) return tr("work.yesterdayAt", { time });
    return tr("work.dayAt", { day: date.toLocaleDateString(language, { weekday: "long" }), time });
}

/** The key that means each answer on an item: Y, N, M and E; a result back is
 *  judged with Y (Useful) and N (Not useful). */
function keyAction(item: WorkItem, key: string): WorkAnswerInput["action"] | "edit-open" | null {
    const back = item.state === "back";
    switch (key) {
    case "y": return back ? "useful" : "yes";
    case "n": return back ? "not_useful" : "no";
    case "m": return back ? null : "not_mine";
    // A topic's proposal starts as it was proposed: it has no Edit.
    case "e": return back || (handsOff(item) && item.source === "topic") ? null : "edit-open";
    default: return null;
    }
}

/** A Yes on this work hands it to an outside agent: until the hand-off sheet
 *  (SH4) shows exactly what goes, Yes and Hand it over stay off. */
export function handsOff(item: WorkItem): boolean {
    return item.yes.does === "agent";
}

function keysAreFree(event: KeyboardEvent): boolean {
    if (event.defaultPrevented || event.ctrlKey || event.altKey) return false;
    const target = event.target instanceof Element ? event.target : null;
    if (target?.closest('input, textarea, select, [contenteditable]:not([contenteditable="false"]), [role="textbox"], [role="combobox"]')) return false;
    return !document.querySelector('[role="dialog"], [role="alertdialog"]');
}

/**
 * Your work on Today (computer-today v1.0.0, SH2): the open item on the left
 * with Yes, No, Not mine and Edit and their keys; Up next, With your agents
 * and Done today on the right; the keyboard row at the foot. Words come from
 * the computer as composed for both devices (B2); only day and time words and
 * the fixed UI words are this page's.
 */
/** Two hours with nothing from an agent (computer-today v1.1.0 rule 12). */
export const NO_WORD_MS = 2 * 60 * 60_000;
/** How long the one-line Undo stays after an answer (rule 13). */
export const UNDO_LINE_MS = 5_000;

export function TodayYourWork({ list, active, onAnswer, onUndo, onHandOff, noAgents = false, onConnectAgent, agentsFailed = false, onRetryAgents, onOpenRef, title, keys = true, focusRequest = null, now = new Date() }: {
    /** Open this item, asked for from elsewhere (Quick chat's Open): an open
     *  one becomes the open item with focus; any other is brought into view.
     *  A new nonce asks again. */
    focusRequest?: { itemId: string; nonce: number } | null;
    /** The agents couldn't be read: the Your agents card says so, with Try
     *  again, instead of leaving itself out. */
    agentsFailed?: boolean;
    onRetryAgents?: () => void;
    /** The keys line under the list. Today draws it as its own strip at the
     *  window's foot instead (computer-window v1.0.0 rule 8). */
    keys?: boolean;
    /** The heading in place of "Your work" (a topic's "Next"). */
    title?: string;
    /** Opens what a source-line part names: a topic, a brief (computer-find v1.0.0 rule 1). */
    onOpenRef?: (ref: string) => boolean;
    /** No agent connected yet: the right column says so once (rule 3). */
    noAgents?: boolean;
    onConnectAgent?: () => void;
    /** Opens the hand-off sheet for agent work (SH4); without it, Yes on
     *  agent work stays off. */
    onHandOff?: (item: WorkItem, edited?: string) => void;
    list: WorkList | null;
    /** Today is the page in front: only then do the keys answer. */
    active: boolean;
    onAnswer: (id: string, input: WorkAnswerInput) => Promise<WorkAnswerError | null>;
    onUndo: (id: string) => Promise<WorkAnswerError | null>;
    now?: Date;
}) {
    const { t, i18n } = useTranslation("today");
    const tr: Translate = (key, options) => t(key as never, options as never) as unknown as string;
    const language = i18n.language;
    const judge = useMemo(() => workToJudge(list), [list]);
    const withAgents = (list?.items ?? []).filter((item) => item.state === "with_agent");
    const done = (list?.items ?? []).filter((item) => item.state === "answered" && !!item.answer)
        .sort((a, b) => Date.parse(b.answer!.at) - Date.parse(a.answer!.at));
    const [focusId, setFocusId] = useState<string | null>(null);
    const index = Math.max(0, judge.findIndex((item) => item.id === focusId));
    const open = judge[index] ?? null;
    const [noFor, setNoFor] = useState<string | null>(null);
    const [editFor, setEditFor] = useState<string | null>(null);
    const [draft, setDraft] = useState("");
    const [busy, setBusy] = useState(false);
    const [error, setError] = useState<WorkAnswerError | null>(null);
    const boxRef = useRef<HTMLDivElement>(null);

    const undoable = (item: WorkItem) => {
        const until = Date.parse(item.answer?.undoUntil || list?.undoUntil || "");
        return Number.isFinite(until) && until > now.getTime();
    };
    const lastUndoable = [...done, ...withAgents].filter((item) => item.answer && undoable(item))
        .sort((a, b) => Date.parse(b.answer!.at) - Date.parse(a.answer!.at))[0] ?? null;

    const submit = async (item: WorkItem, input: WorkAnswerInput) => {
        if (busy) return;
        setBusy(true);
        setError(null);
        const failed = await measureToday("W01", "do", "done", () => onAnswer(item.id, input));
        setBusy(false);
        if (failed) { setError(failed); return; }
        setNoFor(null);
        setEditFor(null);
        if (input.action !== "ask_again") showUndoLine(item.id);
        // The answered item leaves the open list; the next one takes its place.
        if (item.id === open?.id) {
            const next = judge[index + 1] ?? judge[index - 1] ?? null;
            setFocusId(next?.id ?? null);
        }
    };
    // The one-line Undo for five seconds after an answer; Done today keeps
    // Undo until tomorrow's build.
    const [undoLine, setUndoLine] = useState<string | null>(null);
    const undoTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
    const showUndoLine = (id: string) => {
        if (undoTimer.current) clearTimeout(undoTimer.current);
        setUndoLine(id);
        undoTimer.current = setTimeout(() => setUndoLine(null), UNDO_LINE_MS);
    };
    useEffect(() => () => { if (undoTimer.current) clearTimeout(undoTimer.current); }, []);
    const lineItem = undoLine ? (list?.items ?? []).find((item) => item.id === undoLine && (item.answer || takenBackOf(item))) ?? null : null;
    // Undo on a hand-off says what it can't undo (rule 13).
    const lineText = (item: WorkItem) => {
        const taken = !item.answer ? takenBackOf(item) : null;
        if (taken) return tr("ctoday.takenBack", { agent: taken.agentName || tr("agent.yourAgent") });
        return item.answer?.line || item.work.title;
    };
    const undo = async (item: WorkItem) => {
        setUndoLine(null);
        setError(null);
        const failed = await measureToday("W02", "do", "done", () => onUndo(item.id));
        if (failed) setError(failed);
        else setFocusId(item.id);
    };
    // Use it, or for agent work Hand it over, which opens the sheet with the
    // person's version.
    const applyEdit = (item: WorkItem) => {
        if (!draft.trim()) return;
        if (handsOff(item)) { if (onHandOff) { setEditFor(null); onHandOff(item, draft); } return; }
        void submit(item, { action: "edit", before: item.work.text ?? item.work.title, after: draft });
    };
    const act = (item: WorkItem, action: ReturnType<typeof keyAction>) => {
        if (!action) return;
        if (action === "edit-open") {
            setEditFor(item.id);
            setDraft(item.work.text ?? item.work.title);
            return;
        }
        // A No, or a Not useful on a result, asks for a reason when the item
        // has reasons (computer-today v1.1.0 rule 5).
        if ((action === "no" || action === "not_useful") && item.reasons.length > 0) { setNoFor(item.id); return; }
        if (action === "yes" && handsOff(item)) { if (onHandOff) onHandOff(item); return; }
        void submit(item, { action });
    };

    useEffect(() => {
        if (!active) return;
        const onKey = (event: KeyboardEvent) => {
            if (!keysAreFree(event)) return;
            const key = event.key.toLowerCase();
            if (event.metaKey) {
                if (key === "z" && !event.shiftKey && lastUndoable) { event.preventDefault(); void undo(lastUndoable); }
                return;
            }
        };
        document.addEventListener("keydown", onKey);
        return () => document.removeEventListener("keydown", onKey);
    });

    // Y N M E and the arrows act on the item that has focus, never while
    // typing (pass-access v1.0.0, Today 1 and 2): ↑ ↓ move focus across the
    // items in reading order, and the key answers the focused one.
    const sectionRef = useRef<HTMLElement>(null);
    const onItemKey = (event: React.KeyboardEvent) => {
        const native = event.nativeEvent;
        if (!active || native.isComposing || event.metaKey || event.ctrlKey || event.altKey) return;
        const target = event.target instanceof Element ? event.target : null;
        if (!target || target.closest('input, textarea, select, [contenteditable]:not([contenteditable="false"])')) return;
        const element = target.closest<HTMLElement>("[data-work-item]");
        if (!element) return;
        if (event.key === "ArrowDown" || event.key === "ArrowUp") {
            const items = Array.from(sectionRef.current?.querySelectorAll<HTMLElement>("[data-work-item]") ?? []);
            const next = items[items.indexOf(element) + (event.key === "ArrowDown" ? 1 : -1)];
            if (next) { event.preventDefault(); next.focus(); }
            return;
        }
        if (event.shiftKey || editFor) return;
        const item = judge.find((row) => row.id === element.dataset.itemId);
        if (!item) return;
        const action = keyAction(item, event.key.toLowerCase());
        if (!action) return;
        event.preventDefault();
        // Reasons and Edit show on the open item: a row they need becomes it.
        if (item.id !== open?.id && (action === "edit-open" || ((action === "no" || action === "not_useful") && item.reasons.length > 0))) openRow(item.id);
        act(item, action);
    };
    // An item asked for from elsewhere opens here (Quick chat's Open).
    useEffect(() => {
        if (!focusRequest) return;
        if (judge.some((item) => item.id === focusRequest.itemId)) { openRow(focusRequest.itemId); return; }
        const row = Array.from(sectionRef.current?.querySelectorAll<HTMLElement>("[data-item-id]") ?? []).find((element) => element.dataset.itemId === focusRequest.itemId);
        row?.scrollIntoView?.({ block: "nearest" });
        row?.focus();
        // eslint-disable-next-line react-hooks/exhaustive-deps
    }, [focusRequest?.nonce, focusRequest?.itemId, !!list]);
    // Enter on a row makes it the open item and puts focus on it.
    const openRow = (id: string) => {
        setFocusId(id); setNoFor(null); setEditFor(null);
        requestAnimationFrame(() => boxRef.current?.focus());
    };

    if (!list) return null;
    const rowWord = (item: WorkItem) => item.agent?.name || tr(`work.row.${["draft", "prep", "check", "offered", "claim", "result"].includes(item.row) ? item.row : "offered"}`);
    // A part with a time is worded for the reader's day, one with a known
    // phrase key from i18n; the computer's text is the fallback.
    const partText = (part: WorkItem["first"][number]) => {
        if (part.at) { const when = workWhen(part.at, now, language, tr); if (when) return when; }
        if (part.key && i18n.exists(`today:work.first.${part.key}`)) return tr(`work.first.${part.key}`);
        return part.text;
    };
    const yesWhen = open?.yes.at ? workWhen(open.yes.at, now, language, tr) : "";
    const back = open?.state === "back";

    return (
        <section ref={sectionRef} aria-labelledby="today-your-work-title" data-testid="today-your-work" className="@container flex flex-col gap-3" onKeyDown={onItemKey}>
            <header className="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1">
                <h2 id="today-your-work-title" className="text-[15px] font-semibold text-ink">{title ?? tr("work.title")}</h2>
                <span className="text-[12.5px] text-muted" data-testid="your-work-counts">
                    {list.backFromAgents > 0 ? tr("ctoday.workSub", { n: list.toAnswer, m: list.backFromAgents })
                        : judge.length > 0 && judge.every((item) => item.source === "day_one") ? tr("ctoday.dayOne", { count: list.toAnswer })
                        : tr("work.toAnswer", { count: list.toAnswer })}
                </span>
            </header>
            {/* Two columns when Today's content is 880 wide or more: Your work
                480 to 640, the right column 320 to 372, 24 between
                (computer-window v1.0.0 rule 5). Below, one column in rank
                order (rule 6). */}
            <div className="grid grid-cols-1 items-start gap-6 @min-[880px]:grid-cols-[minmax(480px,640px)_minmax(320px,372px)]" data-testid="your-work-columns">
                {open ? (
                    <div
                        ref={boxRef}
                        data-testid="your-work-open"
                        data-work-item
                        data-item-id={open.id}
                        tabIndex={0}
                        role="article"
                        aria-label={tr("work.itemLabel", { source: open.first.map((part) => part.text).filter(Boolean).join(" · "), sentence: open.noticed, work: open.work.title, yes: open.yes.line })}
                        className="flex flex-col gap-3 rounded-xl border border-border bg-panel px-[22px] py-5 shadow-card outline-none"
                    >
                        {back ? (
                            <p className="text-[12px] text-muted" data-testid="your-work-back-from">
                                {tr("ctoday.backFrom", { agent: open.agent?.name ?? tr("agent.yourAgent"), time: workWhen(open.answer?.at ?? open.agent?.since, now, language, tr) })}
                            </p>
                        ) : open.first.length > 0 && (
                            <p className="text-[12px] text-muted" data-testid="your-work-first">
                                {open.first.map((part, index) => {
                                    const text = partText(part);
                                    if (!text) return null;
                                    const sep = index > 0 ? " · " : "";
                                    return part.ref && onOpenRef ? (
                                        <span key={index}>{sep}<button type="button" className="underline-offset-2 hover:text-ink hover:underline" data-testid="your-work-source" onClick={() => onOpenRef(part.ref!)}>{text}</button></span>
                                    ) : <span key={index}>{sep}{text}</span>;
                                })}
                            </p>
                        )}
                        {open.noticed && <p className="text-[14px] font-semibold leading-snug text-ink">{open.noticed}</p>}
                        {editFor === open.id ? (
                            <div className="flex flex-col gap-2">
                                <Textarea
                                    autoFocus
                                    aria-label={open.work.title}
                                    data-testid="your-work-edit"
                                    value={draft}
                                    onChange={(event) => setDraft(event.target.value)}
                                    onKeyDown={(event) => {
                                        if (event.key === "Enter" && event.metaKey) { event.preventDefault(); applyEdit(open); }
                                        if (event.key === "Escape") { event.preventDefault(); setEditFor(null); }
                                    }}
                                    className="min-h-[110px] text-[14px]"
                                />
                                {(open.gets ?? []).length > 0 && (
                                    <div className="flex flex-wrap gap-1.5" data-testid="your-work-gets">
                                        {open.gets!.map((get) => <span key={get.id} className="rounded-full border border-border px-2.5 py-0.5 text-[12px] text-muted">{get.label}</span>)}
                                    </div>
                                )}
                                <div className="flex items-center gap-2">
                                    <Button size="sm" disabled={busy || !draft.trim() || (handsOff(open) && !onHandOff)} onClick={() => applyEdit(open)}>
                                        {tr(open.yes.does === "agent" ? "work.handOver" : "work.useIt")} <kbd className="ml-1 text-[11px] opacity-70">⌘↩</kbd>
                                    </Button>
                                    <Button size="sm" variant="quiet" onClick={() => setEditFor(null)}>{tr("work.cancel")}</Button>
                                </div>
                            </div>
                        ) : (
                            <div className="flex flex-col gap-1">
                                <p className="text-[14px] font-medium text-ink">{open.work.title}</p>
                                {open.work.text && <p className="whitespace-pre-wrap text-[13.5px] leading-[1.55] text-ink">{open.work.text}</p>}
                            </div>
                        )}
                        {!back && takenBackOf(open) && (
                            <p className="rounded-lg bg-surface-active px-3 py-2 text-[12.5px] text-muted" data-testid="your-work-taken-back">
                                {tr("ctoday.takenBackItem", { agent: takenBackOf(open)!.agentName || tr("agent.yourAgent"), time: workWhen(takenBackOf(open)!.at, now, language, tr) })}
                            </p>
                        )}
                        {back && <p className="text-[13px] font-medium text-ink">{tr("ctoday.useful")}</p>}
                        {open.yes.line && !back && <p className="text-[12.5px] text-muted" data-testid="your-work-yes-line">{open.yes.line}{yesWhen ? ` · ${yesWhen}` : ""}</p>}
                        {noFor === open.id ? (
                            <div className="flex flex-wrap items-center gap-1.5" data-testid="your-work-reasons">
                                {open.reasons.map((reason) => (
                                    <Button key={reason} size="sm" variant="outline" disabled={busy} onClick={() => void submit(open, { action: back ? "not_useful" : "no", reason })}>{tr(`work.reason.${reason}`)}</Button>
                                ))}
                                <Button size="sm" variant="quiet" disabled={busy} onClick={() => void submit(open, { action: back ? "not_useful" : "no" })}>{tr("work.skip")}</Button>
                            </div>
                        ) : editFor !== open.id && (
                            <div className="flex flex-wrap items-center gap-2" data-testid="your-work-actions">
                                <AnswerButton label={tr(back ? "work.useful" : "work.yes")} keyName="Y" primary disabled={busy || (!back && handsOff(open) && !onHandOff)} onClick={() => act(open, keyAction(open, "y"))} />
                                <AnswerButton label={tr(back ? "work.notUseful" : "work.no")} keyName="N" disabled={busy} onClick={() => act(open, keyAction(open, "n"))} />
                                {!back && <AnswerButton label={tr("work.notMine")} keyName="M" disabled={busy} onClick={() => act(open, "not_mine")} />}
                                {!back && !(handsOff(open) && open.source === "topic") && <AnswerButton label={tr("work.edit")} keyName="E" disabled={busy} onClick={() => act(open, "edit-open")} />}
                            </div>
                        )}
                        {!back && handsOff(open) && !onHandOff && <p className="text-[12.5px] text-muted" data-testid="your-work-handoff-soon">{tr("work.handOffSoon")}</p>}
                        {error && <p role="alert" className="text-[12.5px] text-copper" data-testid="your-work-error">{tr(`work.error.${error}`)}</p>}
                        <p className="text-[12px] text-muted" data-testid="your-work-move">{tr("ctoday.move", { i: index + 1, n: judge.length })}</p>
                    </div>
                ) : (
                    <div className="flex flex-col gap-1 rounded-xl border border-border bg-panel px-[22px] py-5 shadow-card" data-testid="your-work-empty">
                        <p className="text-[14px] font-medium text-ink">{tr("work.allDone")}</p>
                        <p className="text-[12.5px] text-muted">{tr("work.allDoneHint")}</p>
                        {error && <p role="alert" className="text-[12.5px] text-copper" data-testid="your-work-error">{tr(`work.error.${error}`)}</p>}
                    </div>
                )}
                <div className="flex flex-col gap-4">
                    {judge.length > 1 && (
                        <WorkGroup title={tr("work.upNext")} testId="your-work-up-next">
                            {judge.filter((item) => item.id !== open?.id).slice(0, 4).map((item) => (
                                <button
                                    key={item.id}
                                    type="button"
                                    data-testid="your-work-next-row"
                                    data-work-item
                                    data-item-id={item.id}
                                    className="flex w-full items-baseline justify-between gap-2 rounded-lg px-2 py-1.5 text-left hover:bg-surface-hover"
                                    onClick={() => openRow(item.id)}
                                >
                                    <span className="min-w-0 truncate text-[13px] text-ink">{item.work.title}</span>
                                    <span className="shrink-0 text-[11.5px] text-muted">{rowWord(item)}</span>
                                </button>
                            ))}
                        </WorkGroup>
                    )}
                    {withAgents.length > 0 && (
                        <WorkGroup title={tr("ctoday.withAgents")} testId="your-work-with-agents">
                            {withAgents.map((item) => {
                                const since = Date.parse(item.agent?.since ?? "");
                                const quiet = Number.isFinite(since) && now.getTime() - since >= NO_WORD_MS;
                                // No word after two hours: amber, with Ask again
                                // and Take it back (computer-today v1.1.0 rule 12).
                                return quiet ? (
                                    <div key={item.id} data-work-item data-item-id={item.id} tabIndex={0} role="article" className="flex flex-col gap-1 rounded-lg px-2 py-1" data-testid="your-work-no-word">
                                        <p className="text-[13px] text-warning">{item.work.title} · {tr("ctoday.noWord", { agent: item.agent?.name ?? tr("agent.yourAgent"), time: workWhen(item.agent!.since, now, language, tr) })}</p>
                                        <span className="flex gap-3">
                                            <button type="button" className="text-[12px] font-medium text-accent" disabled={busy} onClick={() => void submit(item, { action: "ask_again" })} data-testid="your-work-ask-again">{tr("ctoday.askAgain")}</button>
                                            <button type="button" className="text-[12px] font-medium text-accent" disabled={busy} onClick={() => void submit(item, { action: "take_back" })} data-testid="your-work-take-back">{tr("ctoday.takeBack")}</button>
                                        </span>
                                    </div>
                                ) : (
                                    <p key={item.id} data-work-item data-item-id={item.id} tabIndex={0} role="article" className="rounded-lg px-2 py-1 text-[13px] text-ink">
                                        {[item.work.title, item.agent?.name, item.agent?.since ? tr("work.since", { time: workWhen(item.agent.since, now, language, tr) }) : ""].filter(Boolean).join(" · ")}
                                    </p>
                                );
                            })}
                        </WorkGroup>
                    )}
                    {done.length > 0 && (
                        <WorkGroup title={tr("work.doneToday")} testId="your-work-done">
                            {done.map((item) => (
                                <div key={item.id} data-work-item data-item-id={item.id} tabIndex={0} role="article" className="flex items-baseline justify-between gap-2 rounded-lg px-2 py-1">
                                    <span className="min-w-0 text-[13px] text-ink">{item.answer!.line || item.work.title}</span>
                                    <span className="flex shrink-0 items-baseline gap-2">
                                        {item.answer!.notSent && onHandOff && <button type="button" className="text-[12px] font-medium text-accent" data-testid="your-work-send" onClick={() => onHandOff(item)}>{tr("work.send")}</button>}
                                        {undoable(item) && <button type="button" className="text-[12px] font-medium text-accent" onClick={() => void undo(item)}>{tr("work.undo")}</button>}
                                    </span>
                                </div>
                            ))}
                        </WorkGroup>
                    )}
                    {agentsFailed && !noAgents && (
                        <div role="alert" className="flex flex-col gap-1 rounded-xl border border-border bg-panel px-3 py-2.5" data-testid="your-work-agents-failed">
                            <h3 className="text-[11px] font-semibold uppercase tracking-wider text-muted">{tr("ctoday.yourAgents")}</h3>
                            <p className="text-[12.5px] leading-snug text-copper">{tr("ctoday.agentsFailed")}</p>
                            {onRetryAgents && <button type="button" className="self-start text-[12.5px] font-medium text-accent" onClick={onRetryAgents}>{tr("ctoday.tryAgain")}</button>}
                        </div>
                    )}
                    {noAgents && (
                        <div className="flex flex-col gap-1 rounded-xl border border-border bg-panel px-3 py-2.5" data-testid="your-work-no-agents">
                            <h3 className="text-[11px] font-semibold uppercase tracking-wider text-muted">{tr("ctoday.yourAgents")}</h3>
                            <p className="text-[12.5px] leading-snug text-ink">{tr("ctoday.noAgents")}</p>
                            {onConnectAgent && <button type="button" className="self-start text-[12.5px] font-medium text-accent" onClick={onConnectAgent}>{tr("ctoday.connectAgent")}</button>}
                        </div>
                    )}
                </div>
            </div>
            {lineItem && (
                <div role="status" className="flex items-center justify-between gap-3 rounded-lg border border-border bg-panel px-3 py-2 text-[13px] text-ink shadow-card" data-testid="your-work-undo-line">
                    <span className="min-w-0 truncate">{lineText(lineItem)}</span>
                    <button type="button" className="shrink-0 font-medium text-accent" onClick={() => void undo(lineItem)}>{tr("work.undo")} <kbd className="ml-1 text-[11px] opacity-60">⌘Z</kbd></button>
                </div>
            )}
            {keys && <TodayKeysLine />}
        </section>
    );
}

/** The keys for Your work: ↑↓ move, Y N M answer, E edit, ⌘Z undo, and
 *  ⌥Space Quick chat at the right (computer-today v1.0.0 rule 4). */
export function TodayKeysLine() {
    const { t } = useTranslation("today");
    const tr: Translate = (key, options) => t(key as never, options as never) as unknown as string;
    return (
        <div className="@container" data-testid="your-work-keys">
            <div className="flex flex-wrap items-center justify-between gap-x-4 gap-y-1 text-[12px] text-muted">
                <span className="flex flex-wrap items-center gap-x-1.5">
                    <kbd className="rounded border border-border px-1">↑</kbd><kbd className="rounded border border-border px-1">↓</kbd>
                    <kbd className="rounded border border-border px-1">Y</kbd><kbd className="rounded border border-border px-1">N</kbd><kbd className="rounded border border-border px-1">M</kbd>
                    <kbd className="rounded border border-border px-1">E</kbd><kbd className="rounded border border-border px-1">⌘Z</kbd>
                    <span className="ml-1">{tr("ctoday.keys")}</span>
                </span>
                {/* Below 880 the strip drops Quick chat's hint; ⌥Space still works (rule 8). */}
                <span className="hidden @min-[880px]:inline" data-testid="your-work-keys-quick-chat"><kbd className="rounded border border-border px-1">⌥Space</kbd> {tr("work.quickChat")}</span>
            </div>
        </div>
    );
}

function AnswerButton({ label, keyName, primary = false, disabled, onClick }: { label: string; keyName: string; primary?: boolean; disabled?: boolean; onClick: () => void }) {
    return (
        <Button size="sm" variant={primary ? "default" : "outline"} disabled={disabled} aria-keyshortcuts={keyName} onClick={onClick}>
            {label}<kbd aria-hidden className="ml-1.5 text-[11px] opacity-60">{keyName}</kbd>
        </Button>
    );
}

function WorkGroup({ title, testId, children }: { title: string; testId: string; children: React.ReactNode }) {
    const headingId = `${testId}-title`;
    return (
        <section aria-labelledby={headingId} className="flex flex-col gap-1" data-testid={testId}>
            <h3 id={headingId} className="px-2 text-[11px] font-semibold uppercase tracking-wider text-muted">{title}</h3>
            {children}
        </section>
    );
}
