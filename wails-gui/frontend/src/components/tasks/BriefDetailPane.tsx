import { useCallback, useEffect, useRef, useState } from "react";
import { briefAssistantName } from "@/lib/brief-assistant";
import { useTranslation } from "react-i18next";
import { CheckCircle2, X } from "lucide-react";
import { Button } from "@/components/ui/button";
import { clockTime } from "@/lib/brief-time";
import { briefMadeAheadRunLine } from "@/lib/brief-title";
import { BriefSignIn } from "@/components/today/BriefSignIn";
import { describeBriefTime } from "@/components/today/TodayStatus";
import { briefEvery, briefNext } from "./brief-schedule-text";
import { GenerateTodayMissedBrief, GetBriefScheduleDetail, type BriefScheduleDetail, type BriefScheduleRun } from "@/lib/wails-shim-app";
import { EventsOn } from "../../../wailsjs/runtime/runtime";

export type BriefSlot = "morning" | "evening";

type MakeState = { kind: "idle" } | { kind: "making"; token: string } | { kind: "made"; at: Date } | { kind: "failed" } | { kind: "cooldown"; until: string };

const startOfDay = (d: Date) => new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime();

/** The brief's own detail beside Tasks › Scheduled (tasks review v1.0.0 §3):
 *  its schedule, Make it now, the last three runs and where its times are
 *  changed. A brief is a setting, so there is no Pause, Run now, history,
 *  Edit in Chat or Delete. */
export function BriefDetailPane({ slot, onClose, onOpenToday, onChangeTimes }: {
    slot: BriefSlot;
    onClose: () => void;
    /** Opens Today on a brief; no id opens the newest one. */
    onOpenToday?: (briefId?: string) => void;
    /** Opens Settings where the brief's times are set. */
    onChangeTimes?: () => void;
}) {
    const { t, i18n } = useTranslation("app");
    const { t: tToday } = useTranslation("today");
    const tr = useCallback((key: string, options?: Record<string, unknown>) => tToday(key as never, options as never) as unknown as string, [tToday]);
    const [detail, setDetail] = useState<BriefScheduleDetail | null>(null);
    const [failed, setFailed] = useState(false);
    const [make, setMake] = useState<MakeState>({ kind: "idle" });
    const makeRef = useRef(make);
    makeRef.current = make;
    const generation = useRef(0);

    const load = useCallback(async () => {
        const mine = ++generation.current;
        setFailed(false);
        try {
            const next = await GetBriefScheduleDetail(slot);
            if (mine === generation.current) setDetail(next);
        } catch {
            if (mine === generation.current) setFailed(true);
        }
    }, [slot]);

    useEffect(() => {
        setDetail(null);
        setMake({ kind: "idle" });
        void load();
    }, [load]);

    useEffect(() => {
        const refresh = () => void load();
        const off = [EventsOn("brief:stored", refresh), EventsOn("mobile:updated", refresh)];
        return () => off.forEach((unsubscribe) => unsubscribe());
    }, [load]);

    useEffect(() => {
        const onKey = (event: KeyboardEvent) => {
            if (event.key !== "Escape" || event.defaultPrevented) return;
            onClose();
        };
        window.addEventListener("keydown", onKey);
        return () => window.removeEventListener("keydown", onKey);
    }, [onClose]);

    useEffect(() => EventsOn("today:brief-request-finished", (token: unknown, success: unknown) => {
        const current = makeRef.current;
        if (current.kind !== "making" || token !== current.token || typeof success !== "boolean") return;
        setMake(success ? { kind: "made", at: new Date() } : { kind: "failed" });
        if (success) void load();
    }), [load]);

    const makeNow = async () => {
        if (makeRef.current.kind === "making") return;
        const token = `${Date.now().toString(36)}-${Math.random().toString(36).slice(2)}`;
        setMake({ kind: "making", token });
        try {
            const result = await GenerateTodayMissedBrief(token);
            if (result.status === "cooldown") setMake({ kind: "cooldown", until: result.nextAllowedAt });
        } catch {
            setMake({ kind: "failed" });
        }
    };

    const now = new Date();
    const language = i18n.language;
    const clock = (date: Date) => clockTime(date, language, tr);
    // The row's own words (brief-schedule-text), from the scheduler's next run.
    const blockedRun = detail?.runs.find((run) => run.waitingForSignIn && !!run.failureAssistant);
    const scheduleLine = detail ? [briefEvery(detail.time, language, t, tr), detail.enabled ? briefNext(detail.nextRunAt, now, language, t, tr) : ""].filter(Boolean).join(" · ") : "";
    const dayName = (date: Date) => {
        const diff = Math.round((startOfDay(date) - startOfDay(now)) / 86_400_000);
        if (diff === 0) return t("briefDetail.today");
        if (diff === -1) return t("briefDetail.yesterday");
        return date.toLocaleDateString(language, { weekday: "long" });
    };

    return (
        <aside className="flex w-[420px] min-h-0 shrink-0 flex-col border-l border-border bg-panel" data-testid="brief-detail" aria-label={t(slot === "evening" ? "briefDetail.titleEvening" : "briefDetail.titleMorning")}>
            <header className="flex items-start gap-3 border-b border-border px-5 pb-4 pt-5">
                <div className="min-w-0 flex-1">
                    <h2 className="text-[17px] font-semibold text-ink">{t(slot === "evening" ? "briefDetail.titleEvening" : "briefDetail.titleMorning")}</h2>
                    {scheduleLine && <p data-testid="brief-detail-schedule" className="mt-1 text-[13px] text-muted">{scheduleLine}</p>}
                </div>
                <Button variant="quiet" size="icon" aria-label={t("briefDetail.close")} onClick={onClose}><X className="size-4" /></Button>
            </header>
            <div className="flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto bg-surface px-5 py-4">
                <p className="text-[13px] text-muted">{t("briefDetail.what")}</p>
                <div className="flex flex-wrap items-center gap-2 text-[13px]" data-testid="brief-detail-make">
                    {make.kind === "making" ? <span role="status" className="text-muted">{t("briefDetail.making")}</span>
                        : make.kind === "made" ? <span role="status" className="text-muted">{t("briefDetail.made", { time: clock(make.at) })} · <Button variant="link" size="sm" className="h-auto p-0 text-[13px] font-medium hover:no-underline" onClick={() => onOpenToday?.()}>{t("briefDetail.openOnToday")}</Button></span>
                            : !blockedRun && <Button size="sm" onClick={() => void makeNow()}>{t("briefDetail.makeNow")}</Button>}
                    {make.kind === "failed" && <span role="alert" className="text-error">{tToday("todayBrief.failed")}</span>}
                    {make.kind === "cooldown" && <span role="status" className="text-muted">{tToday("todayBrief.cooldown", { when: describeBriefTime(make.until, now, tr, language) ?? "" })}</span>}
                </div>
                {/* The heading appears with its rows (tasks-review v1.0.14 K3). */}
                {(failed || (detail && detail.runs.length > 0)) && <section aria-label={t("briefDetail.lastThree")}>
                    <h3 className="mb-2 text-[11px] font-medium uppercase tracking-wider text-muted">{t("briefDetail.lastThree")}</h3>
                    {failed ? (
                        <p role="alert" className="text-[13px] text-muted">{t("briefDetail.loadFailed")} <Button variant="link" size="sm" className="h-auto p-0 text-[13px]" onClick={() => void load()}>{t("tasks.retry")}</Button></p>
                    ) : detail && detail.runs.length > 0 ? (
                        <div className="divide-y divide-border-divider overflow-hidden rounded-xl border border-border bg-panel">
                            {detail.runs.map((run) => <BriefRunRow key={`${run.briefId}:${run.status}`} run={run} dayName={dayName} clock={clock} onOpenToday={onOpenToday} />)}
                        </div>
                    ) : null}
                </section>}
                <p className="text-[13px] text-muted" data-testid="brief-detail-foot">
                    <Button variant="link" size="sm" className="h-auto p-0 text-[13px] font-medium hover:no-underline" onClick={() => onChangeTimes?.()}>{t("briefDetail.changeTimes")}</Button>
                    {" · "}{t("briefDetail.inSettings")}
                </p>
            </div>
        </aside>
    );
}

/** "7:00" from a clock that may carry a period ("上午7:00"): the zh lines
 *  name the scheduled time without it ("不是 7:00"). */
const bareClock = (date: Date) => `${(date.getHours() + 11) % 12 + 1}:${String(date.getMinutes()).padStart(2, "0")}`;

function BriefRunRow({ run, dayName, clock, onOpenToday }: {
    run: BriefScheduleRun;
    dayName: (date: Date) => string;
    clock: (date: Date) => string;
    onOpenToday?: (briefId?: string) => void;
}) {
    const { t, i18n } = useTranslation("app");
    const { t: tToday } = useTranslation("today");
    const scheduled = new Date(run.scheduledAt);
    if (Number.isNaN(scheduled.getTime())) return null;
    const time = clock(scheduled);
    if (run.status === "ok") {
        // The time a brief shows is when it was ready to read, on time or not;
        // its slot is only the fallback when no ready time was recorded (R64).
        const made = new Date(run.at);
        const readyAt = Number.isNaN(made.getTime()) ? null : made;
        const came = (run.late || run.madeNow) && readyAt ? readyAt : null;
        const shown = readyAt ?? scheduled;
        // A brief made before its slot names the time it was made, not its slot (R64, P157).
        const madeAhead = run.early && readyAt
            ? briefMadeAheadRunLine(run, (isoDay) => dayName(new Date(`${isoDay}T12:00:00`)), i18n.language, (key, options) => tToday(key as never, options as never) as unknown as string)
            : null;
        const why = run.reason === "asleep" || run.reason === "closed" ? t(`briefDetail.lateReason.${run.reason}`) : "";
        const line = !came ? ""
            : run.madeNow ? t("briefDetail.madeNow", { at: clock(came) })
                : why ? t("briefDetail.cameLate", { at: clock(came), time, bare: bareClock(scheduled), reason: why })
                    : t("briefDetail.cameLateNoReason", { at: clock(came), time, bare: bareClock(scheduled) });
        return (
            <div data-brief-run={run.briefId} data-brief-run-kind={run.madeNow ? "made-now" : run.late ? "late" : "ok"} className="flex items-start gap-3 px-4 py-3">
                <CheckCircle2 aria-hidden="true" className="mt-0.5 size-[18px] shrink-0 text-accent" />
                <span className="min-w-0 flex-1">
                    <span className="block text-[14px] font-medium text-ink">{madeAhead ?? t("briefDetail.ran", { day: dayName(shown), time: clock(shown) })}</span>
                    {line && <span data-brief-run-line className="mt-0.5 block text-[13px] text-muted">{line}</span>}
                </span>
                <Button variant="quiet" size="sm" onClick={() => onOpenToday?.(run.briefId)}>{t("briefDetail.openOnToday")}</Button>
            </div>
        );
    }
    // The cause the run record proves, in plain words, never a step's name
    // (tasks-review v1.0.8 #2): its failed tries, and the next try while the
    // slot's window is open.
    const attempts = run.attempts ?? 0;
    const nextTry = run.status === "failed" && run.nextTryAt ? new Date(run.nextTryAt) : null;
    const next = nextTry && !Number.isNaN(nextTry.getTime()) ? clock(nextTry) : "";
    const blocked = run.waitingForSignIn && !!run.failureAssistant;
    const recordedFailure = attempts > 0 && run.failureCause === "timeout" ? t(next ? "briefDetail.triedTimeoutNext" : "briefDetail.triedTimeout", { count: attempts, time: next }) : attempts > 0 && run.failureCause === "signed_out" ? t(!blocked && next ? "briefDetail.triedSignedOutNext" : "briefDetail.triedSignedOut", { count: attempts, time: next, ai: briefAssistantName(run.failureAssistant, t) }) : "";
    const line = recordedFailure || (run.status === "failed"
        ? attempts > 0 ? t(next ? "briefDetail.triedNext" : "briefDetail.tried", { count: attempts, time: next }) : t(next ? "briefDetail.didntFinishNext" : "briefDetail.didntFinish", { time: next })
        : run.reason === "asleep" ? t("briefDetail.missedAsleep", { time })
            : run.reason === "closed" ? t("briefDetail.missedClosed", { time })
                : attempts > 0 ? t("briefDetail.tried", { count: attempts })
                    : t("briefDetail.missed", { time }));
    return (
        <div data-brief-run={run.briefId} data-brief-run-kind={run.status === "failed" ? "failed" : "missed"} className="flex items-start gap-3 px-4 py-3">
            <span aria-hidden="true" className="mt-1.5 size-2 shrink-0 rounded-full bg-muted" />
            <span className="min-w-0 flex-1">
                <span className="block text-[14px] font-medium text-ink">{t("briefDetail.ran", { day: dayName(scheduled), time })}</span>
                <span className="mt-0.5 block text-[13px] text-muted">{line}{blocked && <> <BriefSignIn assistant={run.failureAssistant!} /></>}</span>
            </span>
        </div>
    );
}
