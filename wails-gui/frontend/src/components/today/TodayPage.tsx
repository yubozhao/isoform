import { markPageContent } from '@/lib/page-switch-timing';
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { ChevronLeft } from "lucide-react";
import { useTodayStatus } from "@/hooks/useTodayStatus";
import { useAssistantSignOut } from "@/hooks/useAssistantSignOut";
import { useTodayDayShape } from "@/hooks/useTodayDayShape";
import { useLatestBrief } from "@/hooks/useLatestBrief";
import type { TodayActionHandlers } from "./types";
import { TodayStatus } from "./TodayStatus";
import { bandBrief, briefSlotKey, TodayBriefSection } from "./TodayBriefSection";
import { briefClockTime, clockTime, computerClock } from "@/lib/brief-time";
import { meetingStarter, projectWithProblem } from "@/lib/first-day-starters";
import { TodayLastBriefPanel } from "./TodayLastBriefPanel";
import { TodayKeysLine, TodayYourWork } from "./TodayYourWork";
import { TodayBriefFull } from "./TodayBriefFull";
import { TodayTopicPage } from "./TodayTopicPage";
import { TodayHandOffSheet } from "./TodayHandOffSheet";
import { GetTodayDayMeetingTarget, GetYourAgents, type TodayBeforeYouTalk, type WorkItem, type YourAgents } from "@/lib/wails-shim-app";
import { useWorkList } from "@/hooks/useWorkList";
import type { TodayTopic } from "./topics";
import { useBriefSavedAnswers } from "@/hooks/useBriefSavedAnswers";
import { GetMobileBriefSchedule } from "@/lib/page-queries";
import { useTodayTopics } from "@/hooks/useTodayTopics";
import { useTodayQuietLines } from "@/hooks/useTodayQuietLines";
import { describeQuietLines } from "./quietLines";
import { resetTodayChatDrafts } from "@/lib/today-chat-prepared";
import { useObservationPaused } from "./useObservationPaused";
import { TodayPauseLine } from "./TodayPauseLine";
import type { TodayBriefFocus } from "@/lib/main-chat-navigation";
import { historyFound, historyName, useFirstDay } from "@/hooks/useFirstDay";
import { TroubleLine, useTrouble } from "@/components/trouble-line";

/** Past this, a phone that went quiet while the watch was listening is said
 *  on Today (computer-conformance-answers v1.4.0 W5). */
export const PHONE_QUIET_ON_TODAY_MS = 3 * 60 * 60_000;

/** A quiet phone is Today's trouble only when the watch was listening when it
 *  went quiet, past 3 hours (W3). */
export function phoneQuietOnToday(line: { since: string; watchListening?: boolean }, now = Date.now()): boolean {
    const since = Date.parse(line.since);
    return line.watchListening === true && Number.isFinite(since) && now - since >= PHONE_QUIET_ON_TODAY_MS;
}

/** "07:00" from the brief schedule as this computer's clock time. */
function scheduleClock(value: string | undefined, language: string, tr: (key: string, options?: Record<string, unknown>) => string): string | null {
    const match = /^(\d{1,2}):(\d{2})$/.exec(value?.trim() ?? "");
    if (!match) return null;
    const at = new Date();
    at.setHours(Number(match[1]), Number(match[2]), 0, 0);
    return clockTime(at, language, tr);
}

/** The brief schedule for Today's header, read once while Today is in front.
 *  A failed read says so with Try again, never wrong or silently missing
 *  times (hub rule, 2026-11-21). */
function useBriefTimes(active: boolean): { times: { morning: string; evening: string } | null; failed: boolean; retry: () => void } {
    const [schedule, setSchedule] = useState<{ enabled: boolean; morning: string; evening: string } | null>(null);
    const [failed, setFailed] = useState(false);
    const [attempt, setAttempt] = useState(0);
    useEffect(() => {
        if (!active || schedule) return;
        let live = true;
        Promise.resolve().then(() => GetMobileBriefSchedule()).then((next) => { if (live && next) { setSchedule(next); setFailed(false); } }).catch(() => { if (live) setFailed(true); });
        return () => { live = false; };
    }, [active, schedule, attempt]);
    return { times: schedule?.enabled ? schedule : null, failed, retry: () => { setFailed(false); setAttempt((n) => n + 1); } };
}

function todayLabel(language: string): string {
    return new Date().toLocaleDateString(language, {
        weekday: "long",
        month: "long",
        day: "numeric",
    });
}


/**
 * Today on the computer: the brief band across the top, Your work under it
 * with the keys strip at the foot (computer-today v1.0.0). Nothing opens
 * beside Today (v1.2.0 rule 1): the brief in full, a topic and an earlier
 * brief open on their own page in Today's place with ‹ Today (Esc goes back);
 * Chat, Activity and Mail open where they live; a sheet is only for the
 * hand-off.
 */
export function TodayPage({
    handlers,
    onContentReady,
    active = true,
    topicRequest = null,
    briefRequest = null,
    onTopicRequestDone,
    onActiveTopicChange,
    workRequest = null,
}: {
    /** A Your work item to open (Quick chat's Open, #1414/#1415): Today comes
     *  back from any page in its place and opens that item. */
    workRequest?: { itemId: string; nonce: number } | null;
    handlers: TodayActionHandlers;
    /** Kept for the app shell; the docked main chat left Today (v1.2.0). */
    mainChatSession?: import('@/types').Session;
    onLoadMainChatMore?: () => Promise<void>;
    onHydrateMainChat?: (sessionId: string, items: import('@/types').StreamItem[], hasMore: boolean, cursor: number) => void;
    hideMainChatRoutine?: boolean;
    onContentReady?: () => void;
    active?: boolean;
    onOpenReportTarget?: (target: import("@/lib/stream-helpers").MainChatReportTarget, onError?: () => void) => void;
    wideMode?: boolean;
    /** A topic to open, asked for from elsewhere in the app; it opens its
     *  page in Today's place. A new nonce opens it again. */
    topicRequest?: { topic: TodayTopic; nonce: number } | null;
    /** A stored brief to open from a main-chat action; a new nonce opens it
     *  again. focus "status" (a missed brief's Show) scrolls to the brief's
     *  state and starts the brief that is on its way. */
    briefRequest?: { briefId?: string; focus?: TodayBriefFocus; nonce: number } | null;
    onTopicRequestDone?: () => void;
    onActiveTopicChange?: (id: string | null) => void;
}) {
    const { t, i18n } = useTranslation("today");
    const tr = (key: string, options?: Record<string, unknown>) => t(key as never, options as never) as unknown as string;
    const { t: tc } = useTranslation("common");
    const { t: ta } = useTranslation("app");
    const status = useTodayStatus(true);
    const assistantSignOut = useAssistantSignOut();
    // What stopped that nobody chose, one line under the date (computer-trouble
    // v1.0.0): the screen, the assistant, the internet. A quiet phone shows here
    // only when the watch was listening when it went quiet, past 3 hours
    // (computer-conformance-answers v1.4.0 W3, W5). The status line already
    // says a sign-out, so it isn't said twice.
    const trouble = useTrouble().filter((line) => !(assistantSignOut && line.kind === "signed_out") && (line.kind !== "phone" || phoneQuietOnToday(line)));
    const latest = useLatestBrief(true, briefRequest?.briefId, briefRequest?.nonce);
    const answeredBriefCards = useBriefSavedAnswers(latest.view?.brief, active);
    const { times: briefTimes, failed: briefTimesFailed, retry: retryBriefTimes } = useBriefTimes(active);
    const work = useWorkList(true);
    const todayTopics = useTodayTopics(active);
    const observationPaused = useObservationPaused();
    useEffect(() => {
        // Drafts prepared before the pause named what Isoform observed; drop
        // them. Resuming prepares every action again.
        if (observationPaused) resetTodayChatDrafts();
    }, [observationPaused]);

    // One page in Today's place at a time (v1.2.0 rule 1): the brief in
    // full, a topic, or an earlier brief. Back returns to Today and puts
    // focus back on what opened it.
    type TodayPlace = { kind: "brief" } | { kind: "topic"; topic: TodayTopic } | { kind: "earlier"; briefId: string; title: string };
    const [place, setPlace] = useState<TodayPlace | null>(null);
    const returnFocus = useRef<HTMLElement | null>(null);
    const openPlace = (next: TodayPlace) => {
        returnFocus.current = document.activeElement instanceof HTMLElement ? document.activeElement : null;
        setPlace(next);
    };
    const back = useCallback(() => {
        setPlace(null);
        const target = returnFocus.current;
        returnFocus.current = null;
        requestAnimationFrame(() => { if (target?.isConnected) target.focus(); });
    }, []);
    const fullBrief = place?.kind === "brief" ? bandBrief(latest.view) : null;
    useEffect(() => { if (workRequest) setPlace(null); }, [workRequest?.nonce]);
    useEffect(() => {
        onActiveTopicChange?.(place?.kind === "topic" ? place.topic.id : null);
    }, [onActiveTopicChange, place]);
    // Esc goes back from a page in Today's place (the brief in full handles
    // its own keys, Esc included).
    useEffect(() => {
        if (!active || !place || place.kind === "brief") return;
        const onKey = (event: KeyboardEvent) => {
            if (event.key !== "Escape" || event.defaultPrevented || document.querySelector('[role="dialog"], [role="alertdialog"]')) return;
            event.preventDefault();
            back();
        };
        document.addEventListener("keydown", onKey);
        return () => document.removeEventListener("keydown", onKey);
    }, [active, place, back]);
    const openTopic = useCallback((topicId: string, known?: TodayTopic): boolean => {
        const topic = known ?? todayTopics.topics.find((row) => row.id === topicId);
        if (!topic) return false;
        openPlace({ kind: "topic", topic });
        return true;
        // eslint-disable-next-line react-hooks/exhaustive-deps
    }, [todayTopics.topics]);
    useEffect(() => {
        if (!topicRequest) return;
        openTopic(topicRequest.topic.id, topicRequest.topic);
        onTopicRequestDone?.();
        // eslint-disable-next-line react-hooks/exhaustive-deps
    }, [topicRequest?.nonce]);
    const openBrief = (briefId?: string, title?: string) => {
        if (!briefId) return;
        if (briefId === latest.view?.brief?.briefId && bandBrief(latest.view)) { openPlace({ kind: "brief" }); return; }
        openPlace({ kind: "earlier", briefId, title: title ?? "" });
    };
    useEffect(() => {
        // A request for an earlier brief while this slot's is missing opens it
        // on its own page: Today keeps the pending card.
        const brief = latest.view?.brief;
        if (briefRequest?.briefId && latest.view?.pendingSlot && brief?.briefId === briefRequest.briefId) openPlace({ kind: "earlier", briefId: brief.briefId, title: "" });
        // eslint-disable-next-line react-hooks/exhaustive-deps
    }, [briefRequest?.nonce, briefRequest?.briefId, latest.view?.pendingSlot, latest.view?.brief?.briefId]);

    // The hand-off sheet (SH4) and the person's agents, read when it opens.
    const [handOff, setHandOff] = useState<{ item: WorkItem; edited?: string; twoStep?: boolean } | null>(null);
    const [yourAgents, setYourAgents] = useState<YourAgents | null>(null);
    const [agentsFailed, setAgentsFailed] = useState(false);
    const readYourAgents = () => {
        setAgentsFailed(false);
        Promise.resolve().then(() => GetYourAgents()).then((next) => {
            if (next?.agents?.length) setYourAgents(next);
            else setAgentsFailed(true);
        }).catch(() => setAgentsFailed(true));
    };
    // Whether any agent is connected, for Your work's day-one card (v1.1.0
    // rule 3): read once while Today is in front.
    // A failed read says so in the card's place, with Try again.
    const [agentsReadFailed, setAgentsReadFailed] = useState(false);
    const [agentsAttempt, setAgentsAttempt] = useState(0);
    useEffect(() => {
        if (!active || yourAgents) return;
        let live = true;
        Promise.resolve().then(() => GetYourAgents()).then((next) => { if (live && next?.agents) { setYourAgents(next); setAgentsReadFailed(false); } }).catch(() => { if (live) setAgentsReadFailed(true); });
        return () => { live = false; };
    }, [active, yourAgents, agentsAttempt]);
    // The first Yes is the judgment and is recorded at once (Yes · not sent);
    // Send on the sheet is the action (phone-agent v1.0.0 rule 3). A Yes
    // already recorded, from Done today's Send, only sends.
    const openHandOff = (item: WorkItem, edited?: string) => {
        readYourAgents();
        if (item.answer?.notSent) { setHandOff({ item, twoStep: true }); return; }
        if (edited !== undefined || item.state !== "open") { setHandOff({ item, edited }); return; }
        setHandOff({ item });
        void work.answer(item.id, { action: "yes", send: false }).then((failed) => {
            setHandOff((current) => current?.item.id === item.id ? { ...current, twoStep: !failed } : current);
        });
    };

    const { shape: dayShape, dateISO: dayShapeDate, failed: dayShapeFailed, lastUpdatedAt: dayShapeUpdatedAt, retry: retryDayShape } = useTodayDayShape(active, latest.view?.brief?.localDate,
        !!latest.view?.brief && briefSlotKey(latest.view.brief.slot, latest.view.brief.asOf, latest.view.brief.timezone) === "evening");
    useEffect(() => {
        if (latest.loading) return;
        markPageContent('today');
        onContentReady?.();
    }, [latest.loading, onContentReady]);
    useEffect(() => {
        const brief = latest.view?.brief;
        if (!active || !briefRequest?.briefId || latest.loading || brief?.briefId !== briefRequest.briefId || !brief.yourWeek) return;
        document.querySelector('[data-testid="your-week-section"]')?.scrollIntoView({ behavior: "smooth", block: "start" });
    }, [active, briefRequest?.nonce, briefRequest?.briefId, latest.loading, latest.view?.brief]);
    // A missed brief's Show: Today's brief is located at once, and the brief
    // that is on its way starts now rather than at its next retry.
    const [locateNonce, setLocateNonce] = useState(0);
    const statusFocus = useRef(0);
    useEffect(() => {
        if (!active || briefRequest?.focus !== "status" || latest.loading || statusFocus.current === briefRequest.nonce) return;
        statusFocus.current = briefRequest.nonce;
        setPlace(null);
        setLocateNonce((n) => n + 1);
        document.querySelector('[data-testid="today-brief"]')?.scrollIntoView({ behavior: "smooth", block: "start" });
        if (latest.view?.pendingSlot && !latest.view.building && (latest.make === "idle" || latest.make === "failed")) void latest.makeNow();
    }, [active, briefRequest?.focus, briefRequest?.nonce, latest.loading, latest.view, latest.make, latest.makeNow]);

    // What Isoform captured today moved to Activity; Today keeps only the
    // paused line, from the observation state it already has.
    const pausedText = observationPaused === true;
    // Notice kinds the user moved to Today: one muted line each, under the
    // status lines (RC 1 A4).
    const { t: tSettings, i18n: settingsI18n } = useTranslation("settings");
    const quietRaw = useTodayQuietLines(active);
    const quietLines = describeQuietLines(
        quietRaw,
        tr,
        (kind) => {
            const key = `proactiveNotifications.kinds.${kind}.label`;
            const known = typeof settingsI18n?.exists === "function" ? settingsI18n.exists(key, { ns: "settings" }) : true;
            return known ? tSettings(key as never) as unknown as string : null;
        },
        i18n.language,
    );

    // Every action opens where it lives (v1.2.0): Chat through the app's own
    // handlers, a past meeting's note in Activity, a topic on its page here.
    // Find time goes (rule 6), so no handler for it is passed down.
    // A meeting on the day strip that couldn't be opened says so, with Try
    // again; a failed read is never silent (hub rule, 2026-11-21).
    const [dayMeetingFailed, setDayMeetingFailed] = useState<string | null>(null);
    const pageHandlers = useMemo<TodayActionHandlers>(() => {
        const { onFindTime: _findTime, onFindTimeRange: _findTimeRange, ...rest } = handlers;
        void _findTime; void _findTimeRange;
        return {
            ...rest,
            answeredBriefCards,
            onOpenDayMeeting: function openDayMeeting(id) {
                setDayMeetingFailed(null);
                void GetTodayDayMeetingTarget(id).then((target) => {
                    if (target.kind === "notes" && target.sessionId && handlers.onOpenMeetingNotes) handlers.onOpenMeetingNotes(target.sessionId);
                    else if (target.kind === "prep" && target.meeting) {
                        const meeting: TodayBeforeYouTalk = target.meeting;
                        const time = computerClock(new Date(meeting.startAt), i18n.resolvedLanguage);
                        handlers.onStartChat(tr("beforeYouTalk.briefPrompt", { title: meeting.meetingTitle, name: meeting.personName, time }), { sourceKind: "calendar", sourceId: meeting.meetingId, meetingPrep: true, meetingStartMs: meeting.startAt, generatedTitle: meeting.meetingTitle, generatedPeople: [meeting.personName] });
                    } else {
                        throw new Error("meeting target unavailable");
                    }
                }).catch(() => setDayMeetingFailed(id));
            },
            onOpenTopic: (topicId: string) => { if (!openTopic(topicId)) handlers.onOpenTopic?.(topicId); },
        };
        // eslint-disable-next-line react-hooks/exhaustive-deps
    }, [handlers, answeredBriefCards, openTopic, i18n]);
    // Ask about {topic} ›: Chat with the topic attached (computer-conformance
    // answers v1.0.0, the correction to computer-today v1.2.0 rule 3).
    const askAboutTopic = (topic: TodayTopic) => handlers.onStartChat(tr("prompts.askTopic", { topic: topic.name }), { sourceKind: "topic", sourceId: topic.id, generatedTitle: topic.name });

    // Day 1's first brief says what it was made from, with chips from the
    // same facts (first-day handoff §3).
    const firstDay = useFirstDay();
    const briefShown = latest.view?.brief;
    const eveningReady = firstDay.day === 0 && !!briefShown && briefSlotKey(briefShown.slot, briefShown.asOf, briefShown.timezone) === "evening";
    let firstBrief: { caption?: string; chips: string[]; strip: { title: string; source: string } } | null = null;
    if (firstDay.firstDays) {
        const strip = { title: tr(new Date().getHours() >= 12 ? "firstDay.stripAfternoon" : "firstDay.stripDay"), source: tc("firstDay.fromCalendar") };
        firstBrief = { chips: [], strip };
        if (briefShown && firstDay.day === 0) {
            const time = briefClockTime(briefShown.asOf, i18n.language, tr, briefShown.timezone) ?? "";
            if (eveningReady) {
                firstBrief.caption = [tr("firstDay.eveningTitle"), time, tr("firstDay.eveningSource")].filter(Boolean).join(" · ");
            } else if (briefShown.coldStart) {
                const source = briefShown.fromHistory && historyFound(firstDay.history)
                    ? tr("firstDay.briefSource", { history: historyName(firstDay.history.assistants, tc) })
                    : tr("firstDay.briefSourceCalendar");
                firstBrief.caption = [tr("firstDay.briefTitle"), time, source].filter(Boolean).join(" · ");
                const clock = (at: number) => clockTime(new Date(at), i18n.language, tr);
                const meeting = meetingStarter(firstDay.calendar, { t: (key, options) => String(ta(key as never, options as never)), fromCalendar: "", clock, language: i18n.language });
                firstBrief.chips = [meeting, projectWithProblem(firstDay.history)?.openChip].filter((chip): chip is string => !!chip);
            }
        }
    }

    return (
        <div className="flex min-h-0 min-w-0 flex-1 flex-col" data-testid="today-main">
            <div className="today-page-content patina-ground min-h-0 min-w-0 flex-1 overflow-y-auto">
                {/* Empty space under the title strip drags the window. */}
                <div className="window-drag h-7" />
                <div data-testid="today-column" className="mx-auto w-full max-w-[1180px] px-7 pb-14">
                    {fullBrief && latest.view ? (
                        <TodayBriefFull
                            brief={fullBrief}
                            view={latest.view}
                            active={active && !handOff}
                            items={work.list?.items ?? []}
                            onAnswer={work.answer}
                            onUndo={work.undo}
                            onHandOff={openHandOff}
                            onNotRight={work.answer}
                            onReply={handlers.onAskAboutParagraph ? (index, text) => { void handlers.onAskAboutParagraph!(fullBrief.briefId, index, text, ""); } : undefined}
                            onOpenBrief={(id, title) => openBrief(id, title)}
                            onBack={back}
                        />
                    ) : place?.kind === "topic" ? (
                        <TodayTopicPage
                            topic={place.topic}
                            list={work.list}
                            active={active && !handOff}
                            onAnswer={work.answer}
                            onUndo={work.undo}
                            onHandOff={openHandOff}
                            onAsk={() => askAboutTopic(place.topic)}
                            onBack={back}
                        />
                    ) : place?.kind === "earlier" ? (
                        <section className="flex flex-col gap-4" data-testid="today-earlier-brief">
                            <button type="button" className="flex items-center gap-0.5 self-start text-[13px] font-medium text-accent" onClick={back} data-testid="today-earlier-back">
                                <ChevronLeft aria-hidden className="size-3.5" />{tr("cbrief.back")}
                            </button>
                            <TodayLastBriefPanel key={place.briefId} briefId={place.briefId} title={place.title} onClose={back} />
                        </section>
                    ) : <>
                    <header className="window-drag mb-[18px] flex flex-col items-start gap-1.5 text-left" data-testid="today-header">
                        <div className="flex w-full flex-wrap items-baseline justify-between gap-x-4 gap-y-1">
                            <h1 className="display text-ink" aria-label={work.list && work.list.toAnswer > 0 ? tr("ctoday.titleCount", { count: work.list.toAnswer }) : undefined}>{tr("ctoday.title")}</h1>
                            {briefTimes && (() => {
                                const morning = scheduleClock(briefTimes.morning, i18n.language, tr);
                                const evening = scheduleClock(briefTimes.evening, i18n.language, tr);
                                return morning && evening ? <span className="text-[12.5px] text-muted" data-testid="today-brief-times">{tr("ctoday.times", { morning, evening })}</span> : null;
                            })()}
                            {!briefTimes && briefTimesFailed && (
                                <span role="alert" className="text-[12.5px] text-copper" data-testid="today-brief-times-failed">
                                    {tr("ctoday.timesFailed")}{" "}
                                    <button type="button" className="font-medium text-accent" onClick={retryBriefTimes}>{tr("ctoday.tryAgain")}</button>
                                </span>
                            )}
                        </div>
                        <TodayStatus
                            date={todayLabel(i18n.language)}
                            brief={status.brief && latest.view ? { ...status.brief, pendingSlot: latest.view.pendingSlot, pendingAt: latest.view.pendingAt, retryAt: latest.view.retryAt, pendingAttempts: latest.view.pendingAttempts, pendingCause: latest.view.pendingCause } : status.brief}
                            building={!!latest.view?.building || latest.make === "making"}
                            onMissedWhy={handlers.onOpenBriefDetail}
                            signOut={assistantSignOut}
                        />
                        {trouble.length > 0 && <div className="w-full" data-testid="today-trouble"><TroubleLine lines={trouble} /></div>}
                    </header>

                    {/* The brief across the top, Your work under it. */}
                    <TodayBriefSection
                        dayShape={dayShape}
                        dayShapeDate={dayShapeDate}
                        dayShapeFailure={dayShapeFailed ? { lastUpdatedAt: dayShapeUpdatedAt, retry: retryDayShape } : null}
                        quietFooter={quietLines.length > 0 || pausedText ? <>{quietLines.map((line) => <p key={line.id} data-testid="today-quiet-line">{line.text}</p>)}{pausedText && <TodayPauseLine />}</> : null}
                        band
                        onOpenPastBriefs={() => openPlace({ kind: "brief" })}
                        onReadAll={() => openPlace({ kind: "brief" })}
                        afterBand={work.list ? <TodayYourWork keys={false} focusRequest={workRequest} list={work.list} active={active && !handOff} onAnswer={work.answer} onUndo={work.undo} onHandOff={openHandOff} noAgents={!!yourAgents && !yourAgents.agents.some((agent) => agent.connected)} onConnectAgent={handlers.onOpenAgents} agentsFailed={!yourAgents && agentsReadFailed} onRetryAgents={() => { setAgentsReadFailed(false); setAgentsAttempt((n) => n + 1); }} onOpenRef={(ref) => {
                            // A topic opens its page in Today's place; a brief in full.
                            if (ref.startsWith("topic:")) return openTopic(ref.slice("topic:".length));
                            if (ref.startsWith("brief:")) { openBrief(ref.slice("brief:".length)); return true; }
                            return false;
                        }} /> : null}
                        view={latest.view}
                        loading={latest.loading}
                        loadFailed={latest.loadFailed}
                        onRetry={latest.refresh}
                        make={latest.make}
                        nextAllowedAt={latest.nextAllowedAt}
                        onMakeNow={() => void latest.makeNow()}
                        onOpenBrief={openBrief}
                        locateNonce={locateNonce}
                        handlers={pageHandlers}
                        observationPaused={observationPaused}
                        active={active}
                        firstBrief={firstBrief}
                    />
                    {dayMeetingFailed && (
                        <p role="alert" className="mt-3 text-[12.5px] text-copper" data-testid="today-day-meeting-failed">
                            <button type="button" onClick={() => pageHandlers.onOpenDayMeeting?.(dayMeetingFailed)}>{tr("dayStrip.openFailed")}</button>
                        </p>
                    )}
                    </>}
                </div>
            </div>
            {/* The keys line is a strip of its own at the foot: content
                scrolls under it, never behind it (computer-window v1.0.0
                rule 8). Only on Today itself, where the keys answer. */}
            {work.list && !place && (
                <div className="shrink-0 border-t border-border-divider bg-background px-7 py-2.5" data-testid="today-keys-strip">
                    <div className="mx-auto w-full max-w-[1180px]"><TodayKeysLine /></div>
                </div>
            )}
            {active && handOff && <TodayHandOffSheet key={handOff.item.id} item={handOff.item} edited={handOff.edited} twoStep={handOff.twoStep} failedText={work.failedText} agents={yourAgents} agentsFailed={agentsFailed} onRetryAgents={readYourAgents} onSend={(input) => work.answer(handOff.item.id, input)} onClose={() => setHandOff(null)} />}
        </div>
    );
}
