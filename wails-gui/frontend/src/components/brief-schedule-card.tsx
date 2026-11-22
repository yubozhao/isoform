import { useCallback, useEffect, useRef, useState } from "react";
import { Check, Copy, Loader2 } from "lucide-react";
import { useTranslation } from "react-i18next";
import { activeInterval } from "@/lib/app-activity";
import { GetMobileBriefHealth } from "@/lib/page-queries";
import {
    GenerateMobileBriefNow,
    GetMobileBriefSchedule,
    SetMobileBriefSchedule,
    type MobileBriefHealth,
    type MobileBriefRequestResult,
    type MobileBriefSchedule,
} from "@/lib/wails-shim-app";
import { EventsOn } from "../../wailsjs/runtime/runtime";
import { SettingsFailureLine } from "@/components/settings/settings-failure";
import { SettingsCard } from "./settings/settings-primitives";
import { Button } from "./ui/button";
import { Switch } from "./ui/switch";
import {
    CLOCK_PATTERN,
    LoadState,
    SettingRow,
    TIME_INPUT_CLASS,
    clockLabel,
    clockOptions,
    copyText,
    formatClock,
    formatDateTime,
    sameDraft,
    scheduleDraftOf,
    type ScheduleDraft,
} from "./mobile-connection-settings";

const SCHEDULE_POLL_MS = 60_000;

type BriefErrorScope = "scheduleLoad" | "schedule" | "brief";

/**
 * The brief's schedule: when it is written and sent, adaptive timing, a fresh
 * brief now and the podcast feed. Settings › Briefs and notifications holds it
 * (computer-settings v1.0.0 rule 6: "the brief times"); it moved here from
 * Your phone, unchanged.
 */
export function BriefScheduleCard({ onOpenAssistant }: {
    /** Opens Assistant with Advanced expanded, where the switch that brings
     *  the written summary back lives. Without it the note only names it. */
    onOpenAssistant?: () => void;
} = {}) {
    const { t, i18n } = useTranslation("settings");
    const locale = i18n.resolvedLanguage ?? i18n.language;

    const [schedule, setSchedule] = useState<MobileBriefSchedule | null>(null);
    // Last run, next run and missed runs; null until read, and left as it was
    // when the read fails, because the schedule card is still useful without it.
    const [health, setHealth] = useState<MobileBriefHealth | null>(null);
    const [errors, setErrors] = useState<Partial<Record<BriefErrorScope, string>>>({});
    const [briefBusy, setBriefBusy] = useState(false);
    const [briefNotice, setBriefNotice] = useState<string | null>(null);
    const [feedCopied, setFeedCopied] = useState(false);
    // null means "show the saved value"; a draft is the user's edit.
    const [scheduleDraft, setScheduleDraft] = useState<ScheduleDraft | null>(null);

    const mounted = useRef(false);
    // Bumped by every read or write; a read whose version is stale when it resolves is dropped.
    const scheduleVersion = useRef(0);
    const scheduleRef = useRef<MobileBriefSchedule | null>(null);
    const scheduleSavingRef = useRef(false);
    // The newest schedule waiting to be saved; a running save picks it up when it finishes.
    const pendingScheduleRef = useRef<ScheduleDraft | null>(null);
    const briefRef = useRef(false);

    const setError = useCallback((scope: BriefErrorScope, message?: string) => {
        setErrors((current) => ({ ...current, [scope]: message }));
    }, []);

    const applySchedule = useCallback((next: MobileBriefSchedule) => {
        scheduleRef.current = next;
        setSchedule(next);
    }, []);

    const refreshSchedule = useCallback(async () => {
        if (scheduleSavingRef.current) return;
        const version = ++scheduleVersion.current;
        try {
            const next = await GetMobileBriefSchedule();
            if (mounted.current && version === scheduleVersion.current) {
                applySchedule(next);
                setError("scheduleLoad");
            }
        } catch {
            if (mounted.current && version === scheduleVersion.current) {
                setError("scheduleLoad", t("mobileConnection.scheduleLoadFailed"));
            }
        }
    }, [applySchedule, setError, t]);

    const refreshHealth = useCallback(async () => {
        try {
            const next = await GetMobileBriefHealth();
            if (mounted.current) setHealth(next);
        } catch {
            // The schedule card reads without it.
        }
    }, []);

    useEffect(() => {
        mounted.current = true;
        const refreshAll = () => {
            void refreshSchedule();
            void refreshHealth();
        };
        refreshAll();
        const unsubscribe = EventsOn("mobile:updated", () => refreshAll());
        window.addEventListener("focus", refreshAll);
        // Activity can defer a prediction without a mobile event. Keep the
        // visible schedule current through the same versioned read as focus.
        // The timer stops while the window is hidden or the Mac locked.
        const stopSchedulePoll = activeInterval(() => {
            if (document.visibilityState === "visible" && document.hasFocus()) void refreshSchedule();
        }, SCHEDULE_POLL_MS);
        return () => {
            mounted.current = false;
            scheduleVersion.current += 1;
            window.removeEventListener("focus", refreshAll);
            stopSchedulePoll();
            unsubscribe();
        };
    }, [refreshHealth, refreshSchedule]);

    const shownSchedule: ScheduleDraft | null =
        scheduleDraft ?? (schedule ? scheduleDraftOf(schedule) : null);
    const morningValid = shownSchedule !== null && CLOCK_PATTERN.test(shownSchedule.morning);
    const eveningValid = shownSchedule !== null && CLOCK_PATTERN.test(shownSchedule.evening);

    // Saves on change, one request at a time; the newest change wins. A failed
    // save puts the controls back to the saved schedule and says so.
    const persistSchedule = async (next: ScheduleDraft) => {
        pendingScheduleRef.current = next;
        if (scheduleSavingRef.current) return;
        scheduleSavingRef.current = true;
        scheduleVersion.current += 1;
        setError("schedule");
        try {
            while (pendingScheduleRef.current !== null) {
                const draft = pendingScheduleRef.current;
                pendingScheduleRef.current = null;
                const base = scheduleRef.current;
                if (base === null) break;
                const saved = await SetMobileBriefSchedule({ ...base, ...draft });
                if (!mounted.current) return;
                applySchedule(saved);
                setError("scheduleLoad");
                const savedDraft = scheduleDraftOf(saved);
                setScheduleDraft((current) =>
                    current !== null && sameDraft(current, savedDraft) ? null : current,
                );
            }
        } catch {
            pendingScheduleRef.current = null;
            if (mounted.current) {
                setScheduleDraft(null);
                setError("schedule", t("mobileConnection.scheduleSaveFailed"));
            }
        } finally {
            scheduleSavingRef.current = false;
        }
    };

    const patchSchedule = (patch: Partial<ScheduleDraft>) => {
        if (!schedule || !shownSchedule) return;
        const next = { ...shownSchedule, ...patch };
        setScheduleDraft(next);
        setError("schedule");
        // A half-typed time stays on screen with a hint; the rest still saves.
        const savable: ScheduleDraft = {
            enabled: next.enabled,
            morning: CLOCK_PATTERN.test(next.morning) ? next.morning : schedule.morning,
            evening: CLOCK_PATTERN.test(next.evening) ? next.evening : schedule.evening,
            feedEnabled: next.feedEnabled,
            adaptiveTiming: next.adaptiveTiming,
        };
        // While a save is running, queue even a change back to the saved
        // value, so the newest choice is what ends up stored.
        const saved = scheduleRef.current ?? schedule;
        if (scheduleSavingRef.current || !sameDraft(savable, scheduleDraftOf(saved))) {
            void persistSchedule(savable);
        }
    };

    const announceBrief = (result: MobileBriefRequestResult) => {
        switch (result.status) {
            case "started":
                setBriefNotice(t("mobileConnection.briefWriting"));
                return;
            case "running":
                setBriefNotice(t("mobileConnection.briefRunningTitle"));
                return;
            case "cooldown":
                setBriefNotice(
                    t("mobileConnection.briefCooldownBody", {
                        time: formatClock(result.nextAllowedAt, locale) ?? t("mobileConnection.unknownTime"),
                    }),
                );
                return;
        }
    };

    const generateBrief = async () => {
        if (briefRef.current) return;
        briefRef.current = true;
        setBriefBusy(true);
        setBriefNotice(null);
        setError("brief");
        try {
            const result = await GenerateMobileBriefNow();
            if (mounted.current) announceBrief(result);
            void refreshSchedule();
        } catch {
            if (mounted.current) setError("brief", t("mobileConnection.briefFailed"));
        } finally {
            briefRef.current = false;
            if (mounted.current) setBriefBusy(false);
        }
    };

    const slotLabel = (slot: string): string | null => {
        switch (slot) {
            case "morning":
                return t("mobileConnection.slotMorning");
            case "evening":
                return t("mobileConnection.slotEvening");
            case "on_request":
                return t("mobileConnection.slotOnRequest");
            default:
                return null;
        }
    };

    const describeLastBrief = (current: MobileBriefSchedule): string => {
        const generatedAt = formatDateTime(current.lastGeneratedAt, locale);
        if (generatedAt === null) return t("mobileConnection.briefNever");
        const slot = slotLabel(current.lastSlot);
        return slot === null
            ? t("mobileConnection.briefLastNoSlot", { time: generatedAt })
            : t("mobileConnection.briefLast", { slot, time: generatedAt });
    };

    const describeTimingReason = (reason: string | undefined): string => {
        switch (reason) {
            case "fixed_slot":
                return t("mobileConnection.briefTimingFixed");
            case "learned_opens":
                return t("mobileConnection.briefTimingOpens");
            case "learned_presence":
                return t("mobileConnection.briefTimingPresence");
            case "learned_activity":
                return t("mobileConnection.briefTimingActivity");
            case "before_meeting":
                return t("mobileConnection.briefTimingMeeting");
            case "work_settled":
                return t("mobileConnection.briefTimingSettled");
            case "quiet_hours":
                return t("mobileConnection.briefTimingQuiet");
            case "waiting_for_activity":
                return t("mobileConnection.briefTimingWaiting");
            case "no_window":
                return t("mobileConnection.briefTimingNoWindow");
            default:
                return t("mobileConnection.briefTimingEstimate");
        }
    };

    const describePrediction = (current: MobileBriefSchedule | null): string | null => {
        if (!current?.enabled) return null;
        const reason = describeTimingReason(current.timingReason);
        const at = current.nextPredictedAt ? new Date(current.nextPredictedAt) : null;
        if (at && !Number.isNaN(at.getTime())) {
            // settings-review ST67: the slot follows the time, and "your
            // chosen time" only when it is that time.
            const evening = at.getHours() >= 12;
            const now = new Date();
            const day = (d: Date) => new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime();
            const days = Math.round((day(at) - day(now)) / 86_400_000);
            const weekday = at.toLocaleDateString(locale, { weekday: "long" });
            const when = days <= 0
                ? (evening ? t("mobileConnection.briefWhenThisEvening") : t("mobileConnection.briefWhenThisMorning"))
                : days === 1
                    ? (evening ? t("mobileConnection.briefWhenTomorrowEvening") : t("mobileConnection.briefWhenTomorrowMorning"))
                    : evening
                        ? t("mobileConnection.briefWhenLaterEvening", { day: weekday })
                        : t("mobileConnection.briefWhenLaterMorning", { day: weekday });
            const hhmm = `${String(at.getHours()).padStart(2, "0")}:${String(at.getMinutes()).padStart(2, "0")}`;
            const chosen = evening ? current.evening : current.morning;
            const timing = current.timingReason === "fixed_slot"
                ? (chosen === hhmm ? reason : t("mobileConnection.briefTimingFinish"))
                : current.timingReason === "work_settled"
                    ? t("mobileConnection.briefTimingFinish")
                    : reason;
            const time = at.toLocaleTimeString(locale, { hour: "numeric", minute: "2-digit" });
            return t("mobileConnection.briefPredictedWhen", { when, time, reason: timing });
        }
        const time = formatDateTime(current.nextPredictedAt ?? "", locale);
        const slot = slotLabel(current.nextPredictedSlot ?? "");
        if (time !== null && slot !== null) {
            return t("mobileConnection.briefPredicted", { slot, time, reason });
        }
        return current.timingReason === "no_window"
            ? t("mobileConnection.briefPredictionUnavailable", { reason })
            : null;
    };

    const prediction = describePrediction(schedule);

    // The health lines under the last brief: the next run, then the missed and
    // failing runs of the last week when there are any.
    const describeHealth = (current: MobileBriefHealth | null): string[] => {
        if (!current) return [];
        const lines: string[] = [];
        const nextAt = formatDateTime(current.nextRunAt, locale);
        const nextSlot = slotLabel(current.nextSlot);
        if (prediction === null && current.enabled && nextAt !== null && nextSlot !== null) {
            lines.push(t("mobileConnection.briefNext", { slot: nextSlot, time: nextAt }));
        }
        if (current.missedRuns > 0) {
            lines.push(t("mobileConnection.briefMissedWeek", { count: current.missedRuns }));
        }
        const failingSlot = slotLabel(current.failingSlot);
        if (failingSlot !== null && current.failedAttempts > 0) {
            lines.push(
                t("mobileConnection.briefFailing", { slot: failingSlot, count: current.failedAttempts }),
            );
        }
        return lines;
    };


    return (
        <>
            <SettingsCard
                title={t("mobileConnection.briefTitle")}
                description={t("mobileConnection.briefDescription")}
            >
                {schedule === null || shownSchedule === null ? (
                    <LoadState
                        failed={Boolean(errors.scheduleLoad)}
                        loadingText={t("mobileConnection.briefLoading")}
                        failedText={t("mobileConnection.scheduleLoadFailed")}
                        onRetry={() => void refreshSchedule()}
                    />
                ) : (
                    <div className="flex flex-col">
                        <SettingRow
                            title={t("mobileConnection.briefAutomatic")}
                            description={t("mobileConnection.briefAutomaticDescription")}
                            control={
                                <Switch
                                    aria-label={t("mobileConnection.briefAutomatic")}
                                    checked={shownSchedule.enabled}
                                    onCheckedChange={(checked) => patchSchedule({ enabled: checked })}
                                />
                            }
                        />
                        <SettingRow
                            title={t("mobileConnection.briefAdaptive")}
                            description={t("mobileConnection.briefAdaptiveDescription")}
                            control={
                                <Switch
                                    aria-label={t("mobileConnection.briefAdaptive")}
                                    checked={shownSchedule.adaptiveTiming ?? false}
                                    disabled={!shownSchedule.enabled}
                                    onCheckedChange={(checked) => patchSchedule({ adaptiveTiming: checked })}
                                />
                            }
                        >
                            <p className="text-xs text-muted">{t("mobileConnection.briefAdaptivePrivacy")}</p>
                        </SettingRow>
                        <SettingRow
                            title={t("mobileConnection.briefMorning")}
                            control={
                                <select
                                    aria-label={t("mobileConnection.briefMorning")}
                                    value={shownSchedule.morning}
                                    disabled={!shownSchedule.enabled}
                                    aria-invalid={!morningValid}
                                    onChange={(event) => patchSchedule({ morning: event.target.value })}
                                    className={TIME_INPUT_CLASS}
                                >
                                    {clockOptions(shownSchedule.morning).map((clock) => (
                                        <option key={clock} value={clock}>
                                            {clockLabel(clock, locale)}
                                        </option>
                                    ))}
                                </select>
                            }
                        />
                        <SettingRow
                            title={t("mobileConnection.briefEvening")}
                            control={
                                <select
                                    aria-label={t("mobileConnection.briefEvening")}
                                    value={shownSchedule.evening}
                                    disabled={!shownSchedule.enabled}
                                    aria-invalid={!eveningValid}
                                    onChange={(event) => patchSchedule({ evening: event.target.value })}
                                    className={TIME_INPUT_CLASS}
                                >
                                    {clockOptions(shownSchedule.evening).map((clock) => (
                                        <option key={clock} value={clock}>
                                            {clockLabel(clock, locale)}
                                        </option>
                                    ))}
                                </select>
                            }
                        />
                        <p className="settings-row-desc pt-3">
                            {shownSchedule.enabled
                                ? t("mobileConnection.briefTimesHint")
                                : t("mobileConnection.briefTimesOffHint")}
                        </p>
                        {prediction !== null && (
                            <p className="pt-2 text-xs text-muted">{prediction}</p>
                        )}
                        {shownSchedule.enabled && (!morningValid || !eveningValid) && (
                            <p role="alert" className="pt-2 text-xs text-copper">
                                {t("mobileConnection.briefTimeInvalid")}
                            </p>
                        )}
                        {errors.schedule && (
                            <p role="alert" className="pt-2 text-xs text-copper">
                                {errors.schedule}
                            </p>
                        )}
                        <SettingRow
                            title={t("mobileConnection.briefNowTitle")}
                            description={t("mobileConnection.briefNowDescription")}
                            control={
                                <Button
                                    type="button"
                                    variant="outline"
                                    size="sm"
                                    disabled={briefBusy}
                                    onClick={() => void generateBrief()}
                                >
                                    {briefBusy && <Loader2 className="size-3.5 animate-spin" />}
                                    {t("mobileConnection.generateBrief")}
                                </Button>
                            }
                        >
                            <p className="text-xs text-muted">{describeLastBrief(schedule)}</p>
                            {describeHealth(health).map((line) => (
                                <p key={line} className="text-xs text-muted">
                                    {line}
                                </p>
                            ))}
                        <SettingRow
                            title={t("mobileConnection.briefFeedTitle")}
                            description={t("mobileConnection.briefFeedDescription")}
                            control={
                                <Switch
                                    aria-label={t("mobileConnection.briefFeedTitle")}
                                    checked={shownSchedule.feedEnabled}
                                    onCheckedChange={(checked) => patchSchedule({ feedEnabled: checked })}
                                />
                            }
                        />
                        {shownSchedule.feedEnabled && (
                            <div className="flex flex-col gap-2 pt-1 pb-1">
                                {schedule.feedUrl ? (
                                    <>
                                        <p className="break-all font-mono text-xs text-ink" data-testid="brief-feed-url">
                                            {schedule.feedUrl}
                                        </p>
                                        <div>
                                            <Button
                                                type="button"
                                                variant="outline"
                                                size="sm"
                                                onClick={() =>
                                                    void copyText(schedule.feedUrl).then((ok) => {
                                                        if (mounted.current && ok) setFeedCopied(true);
                                                    })
                                                }
                                            >
                                                {feedCopied ? <Check className="size-3.5" /> : <Copy className="size-3.5" />}
                                                {feedCopied
                                                    ? t("mobileConnection.briefFeedCopied")
                                                    : t("mobileConnection.briefFeedCopy")}
                                            </Button>
                                        </div>
                                    </>
                                ) : (
                                    <p className="text-xs text-muted">{t("mobileConnection.briefFeedStarting")}</p>
                                )}
                                <p className="settings-row-desc">{t("mobileConnection.briefFeedHint")}</p>
                            </div>
                        )}
                        </SettingRow>
                        {briefNotice && (
                            <p role="status" className="pt-2 text-xs text-ink">
                                {briefNotice}
                            </p>
                        )}
                        {errors.brief && (
                            <SettingsFailureLine
                                className="pt-2"
                                failure={{ text: errors.brief, retry: () => void generateBrief() }}
                            />
                        )}
                    </div>
                )}
            </SettingsCard>
            {schedule !== null && !schedule.narrativeAvailable && (
                <SettingsCard
                    title={t("mobileConnection.narrativeTitle")}
                    headerRight={
                        onOpenAssistant ? (
                            <Button size="sm" variant="outline" onClick={onOpenAssistant}>
                                {t("mobileConnection.openAssistant")}
                            </Button>
                        ) : undefined
                    }
                >
                    <p role="note" className="settings-card-desc">
                        {t("mobileConnection.narrativeUnavailable")}
                    </p>
                </SettingsCard>
            )}
        </>
    );
}
