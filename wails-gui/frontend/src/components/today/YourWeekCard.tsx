import { useTodaySectionSeen } from "@/hooks/useTodaySectionSeen";
import { measureToday, recordTodayAction, type TodayActionKind, type TodayActionOutcome } from "@/lib/today-telemetry";
import { isCommitmentItemKey, isFindTimeLabel } from "./commitment-key";
import { findTimeRangeFor } from "@/lib/find-time-range";
import { weekRowsShown } from "@/lib/brief-week-rows";
import { useEffect, useId, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import type { TFunction } from "i18next";
import { BellOff, Check, MoreHorizontal } from "lucide-react";
import { Button } from "@/components/ui/button";
import {
    DropdownMenu,
    DropdownMenuContent,
    DropdownMenuItem,
    DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { cn } from "@/lib/utils";
import { onHoldSettingsChanged } from "@/lib/hold-settings-events";
import {
    OpenDeliverableFile,
    RecordTodayAction,
    GetTodayYourWeekDeclined,
    DeclineTodayYourWeek,
    DismissHoldOffer,
    GetHoldSettings,
    GetHoldDayShape,
    RemoveHold,
    UndoRemoveHold,
    type TodayDayShape,
    ProposeHold,
    SetEngagementKindEnabled,
    UndoDismissHoldOffer,
    type MobileBriefCard,
    type MobileBriefWeek,
} from "@/lib/wails-shim-app";
import type { TodayActionHandlers } from "./types";
import { onPagePrivacyWithdrawal } from "@/lib/page-privacy-withdrawal";
import { TodayWeekBeliefReceipt, type TodayWeekBeliefReview } from "./useTodayWeekBelief";
import type { HoldCardSource } from "./HoldCard";
import { useDirectHold } from "./useDirectHold";
import { TodayDayStrip } from "./TodayDayStrip";
import { computerHourOptions } from "@/lib/brief-time";

/** The engagement kind behind "Your week" (Go engagement.KindWeeklyRecap). */
const YOUR_WEEK_KIND = "weekly_recap";

// The person's own time is ink (docs/design-system): the top topic darkest,
// the rest of the time on this computer lightest.
const TIME_SHADES = ["bg-ink", "bg-ink/55", "bg-ink/30"] as const;
const OTHER_SHADE = "bg-border-strong";

const HEADING = "text-[11px] font-semibold uppercase tracking-wider text-muted";
const CHIP = "h-auto min-h-7 min-w-0 max-w-full flex-[0_1_auto] whitespace-normal [overflow-wrap:anywhere] py-1.5 text-left leading-snug border-chip-border bg-chip-bg px-[11px] text-[12.5px] font-normal text-ink hover:border-border-focus hover:bg-chip-bg";
const ROW = "flex w-full min-w-0 items-baseline gap-2 rounded-lg px-2 py-1.5 text-left text-[13.5px] text-ink transition-colors hover:bg-surface-hover focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-border-focus";

/** "Oct 19 to 23" / "10月19日至23日": the end drops its month when it is the same. */
export function yourWeekRange(start: string, end: string, language: string, t: TFunction<"today">): string {
    const parse = (date: string) => new Date(`${date}T12:00:00Z`);
    const from = parse(start);
    const to = parse(end);
    if (Number.isNaN(from.getTime()) || Number.isNaN(to.getTime())) return "";
    const monthDay = new Intl.DateTimeFormat(language, { month: "short", day: "numeric", timeZone: "UTC" });
    const sameMonth = from.getUTCMonth() === to.getUTCMonth();
    const endText = sameMonth ? new Intl.DateTimeFormat(language, { day: "numeric", timeZone: "UTC" }).format(to) : monthDay.format(to);
    return t("todayBrief.yourWeek.range", { start: monthDay.format(from), end: endText });
}

function weekday(at: string | undefined, language: string, timeZone?: string): string {
    if (!at) return "";
    const date = new Date(at);
    if (Number.isNaN(date.getTime())) return "";
    try {
        return new Intl.DateTimeFormat(language, { weekday: "short", timeZone }).format(date);
    } catch {
        return new Intl.DateTimeFormat(language, { weekday: "short" }).format(date);
    }
}

function weekSlotParts(at: string, timeZone?: string): { day: string; hour: number } | null {
    const date = new Date(at);
    if (!Number.isFinite(date.getTime())) return null;
    const format = (zone?: string) => new Intl.DateTimeFormat("en-CA", { timeZone: zone, year: "numeric", month: "2-digit", day: "2-digit", hour: "numeric", hourCycle: "h23" }).formatToParts(date);
    let parts: Intl.DateTimeFormatPart[];
    try { parts = format(timeZone); } catch { parts = format(); }
    const value = (type: string) => parts.find((part) => part.type === type)?.value ?? "";
    return { day: `${value("year")}-${value("month")}-${value("day")}`, hour: Number(value("hour")) };
}

function weekSlotTime(at: string, language: string, timeZone: string | undefined, includeDay: boolean): string {
    const options: Intl.DateTimeFormatOptions = { ...(includeDay ? { weekday: "long" } : {}), hour: "numeric", minute: "2-digit", ...computerHourOptions() };
    try { return new Intl.DateTimeFormat(language, { ...options, timeZone }).format(new Date(at)); }
    catch { return new Intl.DateTimeFormat(language, options).format(new Date(at)); }
}

/**
 * "Your week" (docs/your-week/handoff.md): the last-workday evening brief, written
 * for the person. Title, one lead sentence, where the time went, what got
 * done for them, what is still open (one chip each), what Isoform learned
 * (each with its fix) and one next-workday recommendation. Every part hides
 * when empty. The words come from the computer in the brief's language; this
 * card adds only headings and buttons.
 */
export function YourWeekCard({ week, cards, timeZone, handlers, onCard, onAskCard, hideTurnOff = false, briefId, beliefReview, active = false }: {
    week: MobileBriefWeek;
    active?: boolean;
    /** The brief's cards; a chip or the next-workday answer is one of them. */
    cards: MobileBriefCard[];
    timeZone?: string;
    handlers: TodayActionHandlers;
    /** Puts a card's words in the box on the right, unsent. */
    onCard: (card: MobileBriefCard) => void;
    onAskCard?: (card: MobileBriefCard) => void;
    hideTurnOff?: boolean;
    briefId?: string;
    beliefReview?: TodayWeekBeliefReview;
}) {
    const { t, i18n } = useTranslation("today");
    const track = <T,>(id: string, kind: TodayActionKind, outcome: TodayActionOutcome, fn: () => T): T => hideTurnOff ? fn() : measureToday(id, kind, outcome, fn);
    const { t: tm } = useTranslation("memory");
    const fieldId = useId();
    const [editingBelief, setEditingBelief] = useState<string | null>(null);
    const [beliefWords, setBeliefWords] = useState("");
    useEffect(() => onPagePrivacyWithdrawal(() => { setEditingBelief(null); setBeliefWords(""); }), []);
    const language = i18n.language;
    const [weekOff, setWeekOff] = useState(false);
    const [turningWeek, setTurningWeek] = useState(false);
    const [actionError, setActionError] = useState<"turnOffFailed" | "undoFailed" | "openFailed" | null>(null);
    const [undone, setUndone] = useState<ReadonlySet<string>>(new Set());
    const [askAnswered, setAskAnswered] = useState<"accepted" | "declined" | null>(null);
    const [declineState, setDeclineState] = useState<{ week: string; loading: boolean; failed: "read" | "save" | null }>({ week: week.start, loading: true, failed: null });
    const declineRequest = useRef(0);
    const [declineReadAttempt, setDeclineReadAttempt] = useState(0);
    useEffect(() => {
        const id = ++declineRequest.current;
        setAskAnswered(null);
        setDeclineState({ week: week.start, loading: true, failed: null });
        void GetTodayYourWeekDeclined(week.start).then((declined) => {
            if (id !== declineRequest.current) return;
            setAskAnswered(declined ? "declined" : null);
            setDeclineState({ week: week.start, loading: false, failed: null });
        }).catch(() => {
            if (id === declineRequest.current) setDeclineState({ week: week.start, loading: false, failed: "read" });
        });
        return () => { declineRequest.current++; };
    }, [week.start, declineReadAttempt]);
    const declineAsk = async () => {
        const started = performance.now();
        const id = ++declineRequest.current;
        setDeclineState({ week: week.start, loading: true, failed: null });
        try {
            await DeclineTodayYourWeek(week.start);
            if (!hideTurnOff) recordTodayAction("T85", "do", "done", started);
            if (id === declineRequest.current) setAskAnswered("declined");
        } catch {
            if (!hideTurnOff) recordTodayAction("T85", "do", "failed", started);
            if (id === declineRequest.current) setDeclineState({ week: week.start, loading: false, failed: "save" });
        }
    };
    const [holdAskState, setHoldAsk] = useState<{ source: HoldCardSource; start: string; end: string; shared: boolean } | null>(null);
    const [holdDismissal, setHoldDismissal] = useState<string | null>(null);
    const [holdSettingsVersion, setHoldSettingsVersion] = useState(0);
    useEffect(() => onHoldSettingsChanged(() => setHoldSettingsVersion((version) => version + 1)), []);
    const sendable = (id?: string) => id ? cards.find((card) => card.id === id && card.mode === "send" && card.message.trim()) : undefined;

    const time = week.time ?? [];
    const totalHours = time.reduce((sum, row) => sum + row.hours, 0);
    const done = weekRowsShown(week, !!handlers.onOpenWeekTask).done;
    const open = week.open ?? [];
    const learned = (week.learned ?? []).filter((row) => !undone.has(row.id));
    const seenRef = useTodaySectionSeen(active && !hideTurnOff, "your_week", (totalHours > 0 ? time.length : 0) + done.length + open.length + learned.length);
    // Old briefs can still contain the preview-only hold offer.
    const ask = week.ask?.hold ? undefined : week.ask;
    const askCard = sendable(ask?.cardId);
    const askInTopic = !askCard && !!ask?.topicId && !!handlers.onPrepareInTopic;
    const askDay = ask?.hold
        ? weekday(ask.hold.start, language, timeZone)
        : weekday(ask?.day ? `${ask.day}T12:00:00Z` : undefined, language, "UTC");
    const range = yourWeekRange(week.start, week.end, language, t);

    const askKey = ask?.cardId?.startsWith("week:ask:") ? ask.cardId.slice("week:ask:".length) : "";
    const holdRow = isCommitmentItemKey(askKey) ? open.find((row) => row.itemKey === askKey && isFindTimeLabel(row.label)) : undefined;
    const askSubject = open.find((row) => row.itemKey === askKey)?.title ?? time.find((row) => row.topicId && row.topicId === ask?.topicId)?.name;
    const holdAsk = holdAskState && holdRow && askCard && holdRow.itemKey === holdAskState.source.thing.sourceId && holdRow.title === holdAskState.source.thing.title && weekSlotParts(holdAskState.start, timeZone)?.day === ask?.day ? holdAskState : null;
    const directHold = useDirectHold(holdAsk?.source ?? null);
    const [holdShape, setHoldShape] = useState<TodayDayShape | null>(null);
    const [removedStripHold, setRemovedStripHold] = useState<string | null>(null);
    const [stripHoldError, setStripHoldError] = useState<"remove" | "restore" | null>(null);
    useEffect(() => {
        if (!removedStripHold) return;
        const timer = window.setTimeout(() => setRemovedStripHold(null), 5000);
        return () => window.clearTimeout(timer);
    }, [removedStripHold]);
    useEffect(() => {
        let active = true;
        setHoldShape(null);
        if (holdAsk && ask?.day) void GetHoldDayShape(ask.day).then((shape) => { if (active) setHoldShape(shape); }).catch(() => {});
        return () => { active = false; };
    }, [holdAsk?.start, holdAsk?.end, ask?.day]);
    useEffect(() => {
        let active = true;
        setHoldAsk(null); setHoldDismissal(null);
        if (!holdRow || !askCard || !ask?.day || !handlers.onFindTime) return () => { active = false; };
        const since = weekday(holdRow.since, language, timeZone);
        const source: HoldCardSource = { thing: { title: holdRow.title, sourceKind: "week", sourceId: holdRow.itemKey }, ...(since ? { reason: t("holds.openSince", { day: since }) } : {}) };
        void GetHoldSettings().then(async (settings) => {
            if (!settings.granted || !settings.offerHolds || !settings.calendarId) return;
            const proposal = await ProposeHold(source.thing, "", 0);
            if (!active || proposal.dismissed || proposal.existing) return;
            const slot = proposal.slots.find((candidate) => {
                const parts = weekSlotParts(candidate.start, timeZone);
                return parts !== null && parts.day === ask.day && parts.hour < 12;
            });
            if (slot) setHoldAsk({ source: { ...source, preferred: slot }, ...slot, shared: !!settings.calendars?.find((calendar) => calendar.id === settings.calendarId)?.meetingsAccount });
        }).catch(() => { /* Keep the ordinary planning ask when availability is unknown. */ });
        return () => { active = false; };
    }, [ask?.day, askCard?.id, holdRow?.itemKey, holdRow?.title, holdRow?.since, handlers.onFindTime, language, timeZone, holdSettingsVersion, t]);
    useEffect(() => {
        if (!holdDismissal) return;
        const timer = window.setTimeout(() => { setHoldDismissal(null); setHoldAsk(null); setAskAnswered("declined"); }, 5000);
        return () => window.clearTimeout(timer);
    }, [holdDismissal]);

    const setWeekEnabled = async (enabled: boolean) => {
        if (turningWeek) return;
        const started = performance.now();
        setTurningWeek(true);
        try {
            await SetEngagementKindEnabled(YOUR_WEEK_KIND, enabled);
            setActionError(null);
            setWeekOff(!enabled);
            void RecordTodayAction("today", "T78", "do", enabled ? "undone" : "done", Math.max(0, Math.round(performance.now() - started))).catch(() => {});
        } catch {
            setActionError(enabled ? "undoFailed" : "turnOffFailed");
            void RecordTodayAction("today", "T78", "do", "failed", Math.max(0, Math.round(performance.now() - started))).catch(() => {});
        } finally {
            setTurningWeek(false);
        }
    };
    const undoMute = async (id: string, kind: string) => {
        const started = performance.now();
        setUndone((current) => new Set([...current, id]));
        try {
            await SetEngagementKindEnabled(kind, true);
            if (!hideTurnOff) recordTodayAction("T82", "do", "undone", started);
        } catch {
            if (!hideTurnOff) recordTodayAction("T82", "do", "failed", started);
            setUndone((current) => new Set([...current].filter((value) => value !== id)));
            setActionError("undoFailed");
        }
    };
    const openDone = (row: NonNullable<MobileBriefWeek["done"]>[number]) => {
        if (row.kind === "deliverable" && row.source && row.sourceId && row.path) {
            void Promise.resolve(OpenDeliverableFile(row.source, row.sourceId, row.path)).catch(() => setActionError("openFailed"));
            return;
        }
        if (row.taskId && handlers.onOpenWeekTask) { handlers.onOpenWeekTask(row.taskId); return; }
    };
    const acceptAsk = () => {
        if (!ask) return;
        if (handlers.onFindTimeRange && ask.day) {
            const first = new Date(`${ask.day}T12:00:00Z`);
            if (!Number.isFinite(first.getTime())) return;
            const weekday = first.getUTCDay();
            first.setUTCDate(first.getUTCDate() - ((weekday + 6) % 7));
            const last = new Date(first);
            last.setUTCDate(last.getUTCDate() + 4);
            handlers.onFindTimeRange({ first: first.toISOString().slice(0, 10), last: last.toISOString().slice(0, 10), minutes: 60, nextWeek: true });
        } else if (askCard) onCard(askCard);
        else if (!ask.topicId || !handlers.onPrepareInTopic?.(ask.topicId, ask.message)) return;
        setAskAnswered("accepted");
    };

    if (weekOff) {
        return <div ref={seenRef} data-testid="brief-your-week" className="text-[13px] text-muted">
            <span>{t("todayBrief.yourWeek.turnedOff")}</span> · <button type="button" className="text-accent underline" disabled={turningWeek} onClick={() => void setWeekEnabled(true)}>{t("todayBrief.yourWeek.undo")}</button>
            {actionError && <p role="alert" className="mt-2 text-error">{t(`todayBrief.yourWeek.${actionError}`)}</p>}
        </div>;
    }
    return (
        <div ref={seenRef} className="flex flex-col gap-5" data-testid="brief-your-week">
            {actionError && <p role="alert" className="text-[12px] text-error">{t(`todayBrief.yourWeek.${actionError}`)}</p>}
            <div className="flex items-start justify-between gap-3">
                <h2 className="text-[18px] font-semibold leading-snug text-ink">{t("todayBrief.yourWeek.title", { range })}</h2>
                {!hideTurnOff && <DropdownMenu>
                    <DropdownMenuTrigger asChild>
                        <button
                            type="button"
                            onClick={() => track("T78", "open", "opened", () => undefined)}
                            data-testid="your-week-menu"
                            aria-label={t("todayBrief.yourWeek.more")}
                            className="flex size-7 shrink-0 items-center justify-center rounded-md text-muted transition-colors hover:bg-ink/10 hover:text-ink focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-border-focus"
                        >
                            <MoreHorizontal className="size-4" />
                        </button>
                    </DropdownMenuTrigger>
                    <DropdownMenuContent align="end" sideOffset={4}>
                        <DropdownMenuItem disabled={turningWeek} onClick={() => void setWeekEnabled(false)}>
                            <BellOff className="size-3.5" />
                            {t("todayBrief.yourWeek.turnOff")}
                        </DropdownMenuItem>
                    </DropdownMenuContent>
                </DropdownMenu>}
            </div>

            <p className="whitespace-pre-line text-[16px] font-medium leading-[1.6] text-ink" data-testid="your-week-lead">{week.lead.replace(/([^\n])\n(?!\n)/g, '$1\n\n')}</p>

            {time.length > 0 && totalHours > 0 && (
                <section className="flex flex-col gap-2.5" data-testid="your-week-time">
                    <h3 className={HEADING}>{t("todayBrief.yourWeek.timeHeading")}</h3>
                    <div aria-hidden className="flex h-2.5 w-full gap-0.5 overflow-hidden rounded-full">
                        {time.map((row, index) => (
                            <span
                                key={row.topicId ?? "other"}
                                className={cn("h-full first:rounded-l-full last:rounded-r-full", row.other ? OTHER_SHADE : TIME_SHADES[index] ?? TIME_SHADES[2])}
                                style={{ width: `${(row.hours / totalHours) * 100}%` }}
                            />
                        ))}
                    </div>
                    <ul className="flex flex-wrap gap-x-4 gap-y-1">
                        {time.map((row, index) => {
                            const label = t("todayBrief.yourWeek.hours", { name: row.other ? t("todayBrief.yourWeek.other") : row.name, hours: row.hours });
                            const dot = <span aria-hidden className={cn("size-2 shrink-0 rounded-full", row.other ? OTHER_SHADE : TIME_SHADES[index] ?? TIME_SHADES[2])} />;
                            return (
                                <li key={row.topicId ?? "other"} className="flex items-center gap-1.5 text-[13px] text-ink">
                                    {dot}
                                    {!row.other && row.topicId && handlers.onOpenTopic ? (
                                        <button type="button" className="rounded-sm hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-border-focus" onClick={() => track("T79", "open", "opened", () => handlers.onOpenTopic?.(row.topicId!))}>{label}</button>
                                    ) : label}
                                </li>
                            );
                        })}
                    </ul>
                    <p className="text-[12px] text-muted">{t("todayBrief.yourWeek.timeFootnote")}</p>
                </section>
            )}

            {done.length > 0 && (
                <section className="flex flex-col gap-1" data-testid="your-week-done">
                    <h3 className={cn(HEADING, "mb-1")}>{t("todayBrief.yourWeek.doneHeading")}</h3>
                    {done.map((row) => {
                        const title = row.kind === "meetings" ? t("todayBrief.yourWeek.meetings", { count: row.count ?? 1 }) : row.title ?? "";
                        const day = row.kind === "meetings" ? "" : weekday(row.at, language, timeZone);
                        const text = day ? t("todayBrief.yourWeek.doneOn", { title, day }) : title;
                        return (
                            <button key={row.id} type="button" className={cn(ROW, "-mx-2 w-[calc(100%+1rem)]")} onClick={() => track("T80", "open", "opened", () => openDone(row))} data-testid="your-week-done-row">
                                <span className="min-w-0 truncate">{text}</span>
                            </button>
                        );
                    })}
                    {(week.doneMore ?? 0) > 0 && (
                        <Button variant="link" size="sm" className="h-auto self-start p-0 text-[12.5px] font-normal text-accent" onClick={() => track("T80", "open", "opened", () => handlers.onOpenActivity())}>
                            {t("todayBrief.yourWeek.doneMore", { count: week.doneMore })}
                        </Button>
                    )}
                </section>
            )}

            {open.length > 0 && (
                <section className="flex flex-col gap-2.5" data-testid="your-week-open">
                    <h3 className={HEADING}>{t("todayBrief.yourWeek.openHeading")}</h3>
                    {open.map((row) => {
                        const since = weekday(row.since, language, timeZone);
                        const chip = row.label ? sendable(row.cardId) : undefined;
                        return (
                            <div key={row.itemKey} className="flex flex-col gap-1.5" data-testid="your-week-open-row">
                                <p className="text-[13.5px] leading-snug text-ink [overflow-wrap:anywhere]">
                                    {row.title}
                                    {since && <span className="text-muted">{" · "}{t("todayBrief.yourWeek.since", { day: since })}</span>}
                                </p>
                                {chip && (
                                    <div>
                                        <Button variant="outline" size="sm" className={CHIP} onClick={() => track("T81", "ask", "opened", () => {
                                            const range = isFindTimeLabel(row.label) && handlers.onFindTimeRange ? findTimeRangeFor(row.label ?? "", new Date()) : null;
                                            if (range) handlers.onFindTimeRange?.(range);
                                            else if (isCommitmentItemKey(row.itemKey) && isFindTimeLabel(row.label) && handlers.onFindTime) handlers.onFindTime({ thing: { title: row.title, sourceKind: "week", sourceId: row.itemKey } });
                                            else (onAskCard ?? onCard)(chip);
                                        })} data-testid="your-week-chip">
                                            <span className="min-w-0">{row.label}</span>
                                            {handlers.answeredBriefCards?.has(chip.id) && <Check aria-hidden className="size-3.5" />}
                                        </Button>
                                    </div>
                                )}
                            </div>
                        );
                    })}
                </section>
            )}

            {learned.length > 0 && (
                <section className="flex flex-col gap-2" data-testid="your-week-learned">
                    <h3 className={HEADING}>{t("todayBrief.yourWeek.learnedHeading")}</h3>
                    {learned.map((row) => (
                        <div key={row.id} className="flex flex-wrap items-baseline gap-x-2 gap-y-1">
                            {row.memoryId && beliefReview?.receipt?.id === row.memoryId ? <TodayWeekBeliefReceipt review={beliefReview} /> : <>
                            <p className="min-w-0 flex-1 text-[13.5px] leading-snug text-ink">{row.text}</p>
                            {row.kind === "mute" && row.engagementKind ? (
                                <Button variant="quiet" size="sm" onClick={() => void undoMute(row.id, row.engagementKind!)}>{t("todayBrief.yourWeek.undo")}</Button>
                            ) : row.memoryId && beliefReview && briefId ? (
                                editingBelief === row.memoryId ? <div className="w-full rounded-[10px] bg-panel-warm p-3">
                                    <label htmlFor={fieldId} className="mb-1.5 block text-[12.5px] text-muted">{tm("aboutYou.review.notQuiteLabel")}</label>
                                    <textarea id={fieldId} autoFocus rows={1} value={beliefWords} placeholder={row.text} onChange={(event) => setBeliefWords(event.target.value)}
                                        onKeyDown={(event) => {
                                            if (event.key === "Escape") { event.preventDefault(); event.stopPropagation(); setEditingBelief(null); setBeliefWords(""); }
                                            else if (event.key === "Enter" && !event.shiftKey && !event.nativeEvent.isComposing) { event.preventDefault(); const words = beliefWords; setEditingBelief(null); setBeliefWords(""); void beliefReview.save(briefId, row.memoryId!, row.text, words); }
                                        }}
                                        className="max-h-[calc(3*1.45em+18px)] min-h-[calc(1.45em+18px)] w-full resize-none rounded-lg border border-border bg-panel px-2.5 py-2 text-[13.5px] leading-[1.45] text-ink [field-sizing:content] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-border-focus" />
                                    <div className="mt-2 flex gap-1.5">
                                        <Button size="sm" onClick={() => { const words = beliefWords; setEditingBelief(null); setBeliefWords(""); void beliefReview.save(briefId, row.memoryId!, row.text, words); }}>{tm("aboutYou.review.save")}</Button>
                                        <Button size="sm" variant="outline" onClick={() => track("T83", "do", "stopped", () => { setEditingBelief(null); setBeliefWords(""); })}>{tm("aboutYou.review.cancel")}</Button>
                                    </div>
                                </div> : <Button variant="quiet" size="sm" onClick={() => track("T83", "open", "opened", () => { setEditingBelief(row.memoryId!); setBeliefWords(""); })}>{tm("aboutYou.review.notQuite")}</Button>
                            ) : null}
                            </>}
                        </div>
                    ))}
                </section>
            )}

            {holdDismissal && holdAsk && <p role="status" className="text-[13px] text-muted">{t("holds.notNowMessage", { title: holdAsk.source.thing.title })} · <button type="button" className="text-accent" onClick={() => void track("T85", "do", "undone", () => UndoDismissHoldOffer(holdDismissal)).then(() => setHoldDismissal(null)).catch(() => { setHoldDismissal(null); setHoldAsk(null); })}>{t("holds.undo")}</button></p>}
            {ask && declineState.week === week.start && declineState.failed === "read" && (
                <button type="button" role="alert" className="self-start text-[12px] text-error" onClick={() => track("T85", "do", "done", () => setDeclineReadAttempt((attempt) => attempt + 1))}>{t("todayBrief.yourWeek.declineReadFailed")}</button>
            )}
            {!holdDismissal && ask && (askCard || askInTopic) && declineState.week === week.start && !declineState.loading && declineState.failed !== "read" && askAnswered !== "declined" && (
                <div className="flex flex-col gap-3 border-t border-border-divider pt-4" data-testid="your-week-ask">
                    {holdAsk && holdShape && !directHold.removed && <TodayDayStrip shape={{ ...holdShape, events: holdShape.events.filter((event) => !directHold.status?.id || event.holdId !== directHold.status.id), hold: { start: holdAsk.start, end: holdAsk.end, label: holdAsk.source.thing.title } }} requestedDate={ask.day} timeZone={timeZone} holdPreview={directHold.held ? "held" : "proposed"} holdTitle={holdAsk.source.thing.title} onOpenHold={() => handlers.onFindTime?.(holdAsk.source)} onRemoveHold={(id) => {
                        if (!id) { void directHold.remove(); return; }
                        setStripHoldError(null);
                        void RemoveHold(id).then(async () => { setRemovedStripHold(id); if (ask.day) setHoldShape(await GetHoldDayShape(ask.day)); }).catch(() => setStripHoldError("remove"));
                    }} />}
                    {removedStripHold && <p role="status" className="text-[13px] text-muted">{t("holds.removed")} · <button type="button" className="text-accent" onClick={() => void UndoRemoveHold(removedStripHold).then(async () => { setRemovedStripHold(null); if (ask.day) setHoldShape(await GetHoldDayShape(ask.day)); }).catch(() => setStripHoldError("restore"))}>{t("holds.undo")}</button></p>}
                    {stripHoldError && <p role="alert" className="text-[13px] text-muted">{t(stripHoldError === "remove" ? "holds.removeFailed" : "holds.restoreFailed")}</p>}
                    <p className="text-[16px] font-medium leading-[1.6] text-ink">{holdAsk ? t("holds.weekQuestion", { title: holdAsk.source.thing.title, when: t("holds.weekRange", { start: weekSlotTime(holdAsk.start, language, timeZone, true), end: weekSlotTime(holdAsk.end, language, timeZone, false) }) }) : handlers.onFindTimeRange && askSubject ? t("todayBrief.yourWeek.whenCanStart", { subject: askSubject }) : ask.text}</p>
                    {holdAsk?.shared && !directHold.held && <p className="text-[13px] text-muted">{t("holds.sharedBusy")}</p>}
                    <div className="flex flex-wrap items-center gap-2">
                        {holdAsk && directHold.removed ? <span role="status" className="text-[13px] text-muted">{t("holds.removed")}{directHold.canRestore && <> · <button type="button" className="text-accent" onClick={() => void directHold.restore()}>{t("holds.undo")}</button></>}</span> : holdAsk && directHold.checking ? <span role="status" className="text-[13px] text-muted">{t("holds.checking")}</span> : holdAsk && directHold.held ? <span role="status" className="text-[13px] text-muted">{t("holds.held")}{directHold.canUndo && <> · <button type="button" className="text-accent" onClick={() => void directHold.undo()}>{t("holds.undo")}</button></>}</span> : <><Button size="sm" onClick={holdAsk ? () => void track("T84", "do", "done", directHold.hold) : () => track("T84", "ask", "opened", acceptAsk)}>{holdAsk ? t("holds.hold", { when: weekSlotTime(holdAsk.start, language, timeZone, true) }) : handlers.onFindTimeRange && ask.day ? t("todayBrief.yourWeek.ask") : t("todayBrief.yourWeek.planDay", { day: askDay })}</Button>
                        <Button size="sm" variant="secondary" onClick={holdAsk ? () => void track("T85", "do", "done", () => DismissHoldOffer(holdAsk.source.thing)).then(setHoldDismissal).catch(() => setHoldAsk(null)) : () => void declineAsk()}>{t("todayBrief.yourWeek.notNow")}</Button>{holdAsk && <Button size="sm" variant="quiet" onClick={() => handlers.onFindTime?.(holdAsk.source)}>{t("holds.otherTimes")}</Button>}</>}
                        {holdAsk && directHold.failed && <span role="alert" className="text-[13px] text-muted">{t("holds.failed")}{directHold.held && <> <button type="button" className="text-accent" onClick={() => handlers.onFindTime?.(holdAsk.source)}>{t("holds.otherTimes")}</button></>}</span>}
                    </div>
                    {declineState.failed === "save" && <p role="alert" className="text-[12px] text-error">{t("todayBrief.yourWeek.declineFailed")}</p>}
                </div>
            )}
        </div>
    );
}
