import { measureToday } from "@/lib/today-telemetry";
import { useTodaySectionSeen } from "@/hooks/useTodaySectionSeen";
import { useEffect, useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { Check } from "lucide-react";
import type { AssistantHistoryView, CalendarFirstDay, WatchedToday } from "@/lib/wails-shim-app";
import { GetWatchedToday } from "@/lib/wails-shim-app";
import { clockTime } from "@/lib/brief-time";
import { historyFound, historyName } from "@/hooks/useFirstDay";
import { cn } from "@/lib/utils";
import { TodayPermissionControl } from "./TodayPermissionControl";
import { activeInterval, waitForAppActive } from "@/lib/app-activity";

type TodayT = (key: string, options?: Record<string, unknown>) => string;

/** How long until the portrait is ready, by the portrait handoff's bar of
 *  about five days of watched work. */
const PORTRAIT_DAYS = 5;
const MINUTE_MS = 60_000;

/** The line under Today's date on day 1 and day 2: "Your first day with
 *  Isoform. Next brief at 6:00 PM." Null when the next brief is unknown. */
export function firstDayLine(day: number, nextRunAt: string | undefined, now: Date, language: string, tr: TodayT): string | null {
    if (!nextRunAt) return null;
    const next = new Date(nextRunAt);
    if (Number.isNaN(next.getTime())) return null;
    const time = clockTime(next, language, tr);
    const tomorrow = new Date(now.getFullYear(), now.getMonth(), now.getDate() + 1).getTime() <= next.getTime();
    const key = day === 0 ? (tomorrow ? "firstDay.lineTomorrow" : "firstDay.line") : (tomorrow ? "firstDay.lineDay2Tomorrow" : "firstDay.lineDay2");
    return tr(key, { time });
}

/** "5 minutes", "1 hour 25 minutes". */
export function watchedDuration(minutes: number, tr: TodayT): string {
    const h = Math.floor(minutes / 60), m = minutes % 60;
    if (!h) return tr("firstDay.know.minutes", { count: m });
    if (!m) return tr("firstDay.know.hours", { count: h });
    return tr("firstDay.know.hoursMinutes", { hours: tr("firstDay.know.hours", { count: h }), minutes: tr("firstDay.know.minutes", { count: m }) });
}

/** Watched time today, read on open and every minute while Today shows. */
export function useWatchedToday(enabled: boolean): WatchedToday | null {
    const [watched, setWatched] = useState<WatchedToday | null>(null);
    useEffect(() => {
        if (!enabled) return;
        let live = true;
        const read = () => Promise.resolve().then(() => waitForAppActive()).then(() => live ? GetWatchedToday() : null).then((next) => { if (live) setWatched(next ?? null); }).catch(() => {});
        void read();
        const stop = activeInterval(read, MINUTE_MS);
        return () => { live = false; stop(); };
    }, [enabled]);
    return watched;
}

type RowState = "done" | "waiting" | "live";

function KnowRow({ state, title, detail, action, control, onOpen, testId }: {
    state: RowState;
    title: string;
    detail?: string;
    action?: { label: string; onClick: () => void };
    control?: ReactNode;
    onOpen?: () => void;
    testId: string;
}) {
    const { t } = useTranslation("today");
    const marker = state === "done" ? (
        <span className="mt-[3px] flex size-4 shrink-0 items-center justify-center rounded-full bg-success-bg text-success"><Check className="size-2.5" strokeWidth={3} /></span>
    ) : state === "live" ? (
        <span className="mt-[3px] flex size-4 shrink-0 items-center justify-center">
            <span className="size-2 rounded-full bg-accent animate-breathe opacity-80 motion-reduce:animate-none" />
        </span>
    ) : (
        <span className="mt-[3px] size-4 shrink-0 rounded-full border border-border-strong" />
    );
    const stateLabel = t(state === "done" ? "firstDay.know.done" : state === "live" ? "firstDay.know.live" : "firstDay.know.waiting");
    const body = (
        <>
            {marker}
            <span className="min-w-0 flex-1">
                <span className={cn("block text-[13px] leading-[18px]", state === "waiting" ? "text-muted" : "font-medium text-ink")}>{title}</span>
                {detail && <span className="mt-0.5 block text-[12px] leading-[17px] text-muted">{detail}</span>}
            </span>
        </>
    );
    return (
        <li data-know-row={testId} data-state={state} className="flex items-start gap-2.5 border-b border-border-divider py-2.5 last:border-0">
            <span className="sr-only">{stateLabel}</span>
            {onOpen ? (
                <button type="button" onClick={() => measureToday(testId === "calendar" ? "T33" : testId === "history" ? "T36" : "T37", "open", "opened", onOpen)} className="-mx-1 flex min-w-0 flex-1 items-start gap-2.5 rounded-md px-1 text-left transition-colors hover:bg-surface-hover">{body}</button>
            ) : (
                <span className="flex min-w-0 flex-1 items-start gap-2.5">{body}</span>
            )}
            {action && (
                <button type="button" onClick={action.onClick} className="mt-px shrink-0 text-[12px] font-medium text-accent hover:underline">{action.label}</button>
            )}
            {control}
        </li>
    );
}

export type GettingToKnowYouProps = {
    day: number;
    active?: boolean;
    history: AssistantHistoryView | null;
    calendar: CalendarFirstDay | null;
    watched: WatchedToday | null;
    topicNames: string[];
    evening: { at: string | null; ready: boolean };
    /** Where the card sits: beside the brief, or under it on a narrow page. */
    placement: "side" | "below";
    /** An import already happened: the "Bring what it knows" line goes. */
    imported?: boolean;
    onOpenPeople: () => void;
    onOpenAboutYou: () => void;
    onOpenTopics?: () => void;
    onOpenBrief?: () => void;
    onImport: () => void;
};

/**
 * Getting to know you (first-day handoff §3): what Isoform already has and
 * when the rest comes. Only rows for sources that exist; real times; shown
 * on day 1 and day 2.
 */
export function GettingToKnowYou({ day, active = true, history, calendar, watched, topicNames, evening, placement, imported = false, ...open }: GettingToKnowYouProps) {
    const { t, i18n } = useTranslation("today");
    const { t: tc } = useTranslation("common");
    const tr: TodayT = (key, options) => String(t(key as never, options as never));
    const clock = (at: number | string) => clockTime(new Date(at), i18n.language, tr);
    const rows: ReactNode[] = [];

    if (calendar) {
        if (calendar.paused || calendar.readsOff) {
            rows.push(<KnowRow key="calendar" testId="calendar" state="waiting" title={tr("firstDay.know.calendarOff")} detail={tr(calendar.paused ? "firstDay.know.paused" : "firstDay.know.off")} control={calendar.readsOff && !calendar.paused ? <TodayPermissionControl permission="calendars" action="T34" /> : undefined} />);
        } else if (calendar.connected) {
            const title = calendar.peopleThisWeek === undefined ? tr("firstDay.know.calendar") : tr("firstDay.know.calendarWeekTitle");
            const detail = calendar.peopleThisWeek === undefined ? undefined : tr("firstDay.know.calendarWeekPeople", { count: calendar.peopleThisWeek });
            rows.push(<KnowRow key="calendar" testId="calendar" state="done" title={title} detail={detail} onOpen={open.onOpenPeople} />);
        } else {
            rows.push(<KnowRow key="calendar" testId="calendar" state="waiting" title={tr("firstDay.know.calendarOff")} control={<TodayPermissionControl permission="calendars" action="T34" />} />);
        }
    }

    if (historyFound(history)) {
        const title = tr("firstDay.know.history", { history: historyName(history.assistants, tc) });
        if (history.state === "reading") {
            rows.push(<KnowRow key="history" testId="history" state="live" title={title} detail={tr("firstDay.know.reading")} />);
        } else {
            const detail = [tr("firstDay.know.projects", { count: history.projects.length }), ...(history.beliefCount > 0 ? [tr("firstDay.know.beliefs", { count: history.beliefCount })] : [])].join(tr("firstDay.know.and"));
            rows.push(<KnowRow key="history" testId="history" state="done" title={title} detail={detail} onOpen={open.onOpenAboutYou} />);
        }
    }

    if (watched) {
        if (!watched.screenRecording || watched.paused) {
            rows.push(<KnowRow key="watching" testId="watching" state="waiting" title={tr("firstDay.know.watching")} detail={tr("firstDay.know.off")} control={!watched.screenRecording ? <TodayPermissionControl permission="screen_recording" action="T35" /> : undefined} />);
        } else {
            const detail = watched.minutes < 1 ? tr("firstDay.know.starting")
                : tr(evening.ready ? "firstDay.know.today" : "firstDay.know.soFar", { time: watchedDuration(watched.minutes, tr) });
            rows.push(<KnowRow key="watching" testId="watching" state="live" title={tr("firstDay.know.watching")} detail={detail} />);
        }
    }

    if (topicNames.length > 0) {
        const names = topicNames.slice(0, 2).join(", ");
        rows.push(<KnowRow key="topics" testId="topics" state="done" title={tr("firstDay.know.topics")} detail={topicNames.length > 2 ? tr("firstDay.know.topicsMore", { names }) : names} onOpen={open.onOpenTopics} />);
    } else if (watched?.startedAt && watched.screenRecording && !watched.paused) {
        rows.push(<KnowRow key="topics" testId="topics" state="waiting" title={tr("firstDay.know.topics")} detail={tr("firstDay.know.about", { time: clock(watched.startedAt + 60 * MINUTE_MS) })} />);
    }

    if (day === 0 && (evening.ready || evening.at)) {
        rows.push(evening.ready
            ? <KnowRow key="evening" testId="evening" state="done" title={tr("firstDay.know.evening")} detail={open.onOpenBrief ? tr(placement === "side" ? "firstDay.know.eveningReadyLeft" : "firstDay.know.eveningReadyAbove") : evening.at ? clock(evening.at) : undefined} onOpen={open.onOpenBrief} />
            : <KnowRow key="evening" testId="evening" state="waiting" title={tr("firstDay.know.evening")} detail={clock(evening.at!)} />);
    }

    rows.push(<KnowRow key="portrait" testId="portrait" state="waiting" title={tr("firstDay.know.portrait")} detail={tr("firstDay.know.portraitIn", { count: Math.max(1, PORTRAIT_DAYS - day) })} />);

    const seenRef = useTodaySectionSeen(active, "getting_to_know_you", rows.length);
    return (
        <section ref={seenRef} aria-labelledby="getting-to-know-you" data-testid="getting-to-know-you" data-placement={placement}
            className="flex flex-col rounded-xl border border-border bg-panel px-[18px] py-4 shadow-card">
            <header className="mb-1 flex items-baseline gap-2">
                <h2 id="getting-to-know-you" className="text-[15px] font-semibold text-ink">{tr("firstDay.know.title")}</h2>
                <span className="ml-auto text-[11px] font-semibold uppercase tracking-wider text-accent">{tr("firstDay.know.day", { n: day + 1 })}</span>
            </header>
            <ul className="flex flex-col">{rows}</ul>
            <div className="mt-2 flex flex-col gap-2 border-t border-border-divider pt-3 text-[12px] leading-[17px]">
                {!imported && (
                    <p className="text-muted">
                        {tr("firstDay.know.importQuestion")}{" "}
                        <button type="button" onClick={() => measureToday("T38", "open", "opened", open.onImport)} className="font-medium text-accent hover:underline" data-testid="know-import">{tr("firstDay.know.importAction")}</button>
                    </p>
                )}
                <p className="text-muted" data-testid="know-local-line">{tc("firstDay.localLine")}</p>
            </div>
        </section>
    );
}
