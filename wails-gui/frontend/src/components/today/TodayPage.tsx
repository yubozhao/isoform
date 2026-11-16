import { measureToday } from "@/lib/today-telemetry";
import { markPageContent } from '@/lib/page-switch-timing';
import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { MessageSquare, X } from "lucide-react";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import { useTodayStatus } from "@/hooks/useTodayStatus";
import { useAssistantSignOut } from "@/hooks/useAssistantSignOut";
import { useTodayDayShape } from "@/hooks/useTodayDayShape";
import { useLatestBrief } from "@/hooks/useLatestBrief";
import { useGoalCheckIn } from "@/hooks/useGoalCheckIn";
import { TodayPortraitCard, useTodayPortraitCard } from "./TodayPortraitCard";
import { useTopicFinds } from "@/hooks/useTopicFinds";
import type { TodayActionHandlers } from "./types";
import { TodayStatus } from "./TodayStatus";
import { briefSlotKey, TodayBriefSection } from "./TodayBriefSection";
import { YourTopicsDialog } from "./YourTopicsDialog";
import { TopicPanel } from "./TopicPanel";
import { briefClockTime, clockContextKey, clockTime, computerClock } from "@/lib/brief-time";
import { meetingStarter, projectWithProblem } from "@/lib/first-day-starters";
import { MainChatPanel } from "./MainChatPanel";
import { TodayLastBriefPanel } from "./TodayLastBriefPanel";
import { TodayPastBriefsPanel } from "./TodayPastBriefsPanel";
import { TodayYourWork } from "./TodayYourWork";
import { TodayBriefFull } from "./TodayBriefFull";
import { TodayTopicPage } from "./TodayTopicPage";
import { bandBrief } from "./TodayBriefSection";
import { TodayHandOffSheet } from "./TodayHandOffSheet";
import { GetYourAgents, type WorkItem, type YourAgents } from "@/lib/wails-shim-app";
import { useWorkList } from "@/hooks/useWorkList";
import { TodayReplyDraftPanel, type TodayReplyDraftRequest } from "./TodayReplyDraftPanel";
import { TodayMeetingNotePanel, type TodayMeetingNoteRequest } from "./TodayMeetingNotePanel";
import { firstTopic, SIDEBAR_TOPICS_CHANGED, TOPIC_PANEL_PINNED_MIN_WIDTH, topicPanelWidth, type TodayTopic } from "./topics";
import { TodayTopicsGrid } from "./TodayTopicsGrid";
import { GetTodayPanelAnswer, PinTopicToSidebar, PrepareTodayBriefParagraph, type TodayPanelAnswer } from "@/lib/wails-shim-app";
import { useBriefSavedAnswers } from "@/hooks/useBriefSavedAnswers";
import { GetMobileBriefSchedule, PageEventsOn } from "@/lib/page-queries";
import { useTodayTopics } from "@/hooks/useTodayTopics";
import { useTodayQuietLines } from "@/hooks/useTodayQuietLines";
import { describeQuietLines } from "./quietLines";
import { Sheet, SheetContent, SheetTitle } from "@/components/ui/sheet";
import { FindTimeAnswer } from "@/components/find-time-answer";
import { HoldCard, type HoldCardSource } from "./HoldCard";
import { resetTodayChatDrafts } from "@/lib/today-chat-prepared";
import type { TodayChatContextRef, TodayChatPreview, TodayChatStartOptions } from "@/lib/today-chat-navigation";
import { prepareTodayDraft, type TodayDraftBox } from "@/lib/today-draft-preparation";
import { useObservationPaused } from "./useObservationPaused";
import { TodayPauseLine } from "./TodayPauseLine";
import { useTodayBeforeYouTalk } from "@/hooks/useTodayBeforeYouTalk";
import { TodayBeforeYouTalkCard } from "./TodayBeforeYouTalkCard";
import { TodayNeedsSection } from "./TodayNeedsSection";
import { TodayDoneSection } from "./TodayDoneSection";
import { TodayDoneAnswerPanel } from "./TodayDoneAnswerPanel";
import { TodayDoneResultPanel } from "./TodayDoneResultPanel";
import { useTodayDone } from "@/hooks/useTodayDone";
import { onPagePrivacyWithdrawal } from "@/lib/page-privacy-withdrawal";
import { useTodayNeeds } from "@/hooks/useTodayNeeds";
import { useMainChatItemPanel } from "@/hooks/useMainChatItemPanel";
import { TaskThreadPane } from "@/components/tasks/TaskThreadPane";
import { GetTodayDayMeetingTarget, GetTodayNeedCheck, MarkTodayDoneOpened, RecordTodayAction, type TodayBeforeYouTalk, type TodayDone, type TodayNeed, type TodayNeedResolution } from "@/lib/wails-shim-app";
import type { TodayBriefFocus } from "@/lib/main-chat-navigation";
import { historyFound, historyName, useFirstDay } from "@/hooks/useFirstDay";
import { GettingToKnowYou, useWatchedToday, YourProjects } from "./TodayFirstDay";
import { AIMemoryImportDialog } from "@/components/ai-memory-import-dialog";

/** "07:00" from the brief schedule as this computer's clock time. */
function scheduleClock(value: string | undefined, language: string, tr: (key: string, options?: Record<string, unknown>) => string): string | null {
    const match = /^(\d{1,2}):(\d{2})$/.exec(value?.trim() ?? "");
    if (!match) return null;
    const at = new Date();
    at.setHours(Number(match[1]), Number(match[2]), 0, 0);
    return clockTime(at, language, tr);
}

/** The brief schedule for Today's header, read once while Today is in front;
 *  a failed read shows no times rather than wrong ones. */
function useBriefTimes(active: boolean): { morning: string; evening: string } | null {
    const [schedule, setSchedule] = useState<{ enabled: boolean; morning: string; evening: string } | null>(null);
    useEffect(() => {
        if (!active || schedule) return;
        let live = true;
        Promise.resolve().then(() => GetMobileBriefSchedule()).then((next) => { if (live && next) setSchedule(next); }).catch(() => {});
        return () => { live = false; };
    }, [active, schedule]);
    return schedule?.enabled ? schedule : null;
}

function todayLabel(language: string): string {
    return new Date().toLocaleDateString(language, {
        weekday: "long",
        month: "long",
        day: "numeric",
    });
}

/** The main chat panel's box: its words, whether a click put them there,
 *  the Today item they came from and the context line for it. */
interface MainDraft {
    draft: string;
    prepared: boolean;
    ref: TodayChatContextRef | null;
    preview?: TodayChatPreview;
}
const EMPTY_MAIN_DRAFT: MainDraft = { draft: "", prepared: false, ref: null };

/** Today's own width at which Getting to know you sits beside the brief:
 *  a 270 px card next to a brief column of about 470 px. */
const FIRST_DAY_SIDE_MIN_WIDTH = 860;

export function TodayPage({
    handlers,
    onOpenReportTarget,
    onContentReady,
    active = true,
    topicRequest = null,
    briefRequest = null,
    onTopicRequestDone,
    onActiveTopicChange,
    mainChatSession,
    onLoadMainChatMore,
    onHydrateMainChat,
    hideMainChatRoutine,
}: {
    handlers: TodayActionHandlers;
    mainChatSession?: import('@/types').Session;
    onLoadMainChatMore?: () => Promise<void>;
    onHydrateMainChat?: (sessionId: string, items: import('@/types').StreamItem[], hasMore: boolean, cursor: number) => void;
    hideMainChatRoutine?: boolean;
    onContentReady?: () => void;
    active?: boolean;
    /** Opens what one of Isoform's cards in the panel points at. */
    onOpenReportTarget?: (target: import("@/lib/stream-helpers").MainChatReportTarget, onError?: () => void) => void;
    wideMode?: boolean;
    /** A topic to open beside Today, asked for from the sidebar's Pinned
     *  topics; a new nonce opens it again. */
    topicRequest?: { topic: TodayTopic; nonce: number } | null;
    /** A stored brief to open from a main-chat action; a new nonce opens it
     *  again. focus "status" (a missed brief's Show) scrolls to the brief's
     *  state and starts the brief that is on its way. */
    briefRequest?: { briefId?: string; focus?: TodayBriefFocus; nonce: number } | null;
    /** Today opened the requested topic; the request must not open it again
     *  when Today next mounts. */
    onTopicRequestDone?: () => void;
    onActiveTopicChange?: (id: string | null) => void;
}) {
    const { t, i18n } = useTranslation("today");
    const { t: tChat } = useTranslation("chat");
    const status = useTodayStatus(true);
    const assistantSignOut = useAssistantSignOut();
    const latest = useLatestBrief(true, briefRequest?.briefId, briefRequest?.nonce);
    const answeredBriefCards = useBriefSavedAnswers(latest.view?.brief, active);
    // "Last brief: … · Read it" opens an earlier brief in the panel, with its
    // day, never on Today as the brief (today-review v1.1.5 #1).
    const [lastBriefPanel, setLastBriefPanel] = useState<string | null>(null);
    const [lastBriefTitle, setLastBriefTitle] = useState("");
    // Past briefs (SH1) opens beside Today; a row opens its brief there.
    const [pastBriefsOpen, setPastBriefsOpen] = useState(false);
    const openPastBriefs = () => {
        setLastBriefPanel(null);
        setPastBriefsOpen(true);
        setPanel({ topicId: null, sheetOpen: !wide });
    };
    const briefTimes = useBriefTimes(active);
    const work = useWorkList(true);
    // The brief in full (computer-brief v1.0.0) in Today's column, from Read all.
    const [briefFull, setBriefFull] = useState(false);
    const fullBrief = briefFull ? bandBrief(latest.view) : null;
    // A topic opened from its item's source line, in Today's column (computer-find v1.0.0, 18a).
    const [topicPageId, setTopicPageId] = useState<string | null>(null);
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
    // The first Yes is the judgment and is recorded at once (Yes · not sent);
    // Send on the sheet is the action (phone-agent v1.0.0 rule 3). A Yes
    // already recorded, from Done today's Send, only sends.
    // Whether any agent is connected, for Your work's day-one card (v1.1.0
    // rule 3): read once while Today is in front.
    useEffect(() => {
        if (!active || yourAgents) return;
        let live = true;
        Promise.resolve().then(() => GetYourAgents()).then((next) => { if (live && next?.agents) setYourAgents(next); }).catch(() => {});
        return () => { live = false; };
    }, [active, yourAgents]);
    const openHandOff = (item: WorkItem, edited?: string) => {
        readYourAgents();
        if (item.answer?.notSent) { setHandOff({ item, twoStep: true }); return; }
        if (edited !== undefined || item.state !== "open") { setHandOff({ item, edited }); return; }
        setHandOff({ item });
        void work.answer(item.id, { action: "yes", send: false }).then((failed) => {
            setHandOff((current) => current?.item.id === item.id ? { ...current, twoStep: !failed } : current);
        });
    };
    const openBrief = (briefId?: string, title?: string) => {
        if (!briefId) return;
        setPastBriefsOpen(false);
        setLastBriefTitle(title ?? "");
        setLastBriefPanel(briefId);
        setPanel({ topicId: null, sheetOpen: !wide });
    };
    useEffect(() => {
        // A request for an earlier brief while this slot's is missing opens it
        // in the panel too: Today keeps the pending card.
        const brief = latest.view?.brief;
        if (briefRequest?.briefId && latest.view?.pendingSlot && brief?.briefId === briefRequest.briefId) setLastBriefPanel(brief.briefId);
    }, [briefRequest?.nonce, briefRequest?.briefId, latest.view?.pendingSlot, latest.view?.brief?.briefId]);
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
    // A missed brief's Show: Today's brief card is located at once, and the
    // brief that is on its way starts now rather than at its next retry.
    const [locateNonce, setLocateNonce] = useState(0);
    const statusFocus = useRef(0);
    useEffect(() => {
        if (!active || briefRequest?.focus !== "status" || latest.loading || statusFocus.current === briefRequest.nonce) return;
        statusFocus.current = briefRequest.nonce;
        setLocateNonce((n) => n + 1);
        document.querySelector('[data-testid="today-brief"]')?.scrollIntoView({ behavior: "smooth", block: "start" });
        if (latest.view?.pendingSlot && !latest.view.building && (latest.make === "idle" || latest.make === "failed")) void latest.makeNow();
    }, [active, briefRequest?.focus, briefRequest?.nonce, latest.loading, latest.view, latest.make, latest.makeNow]);
    const goalCheckIn = useGoalCheckIn(true);
    const portraitCard = useTodayPortraitCard(true);
    const finds = useTopicFinds(true);
    const observationPaused = useObservationPaused();
    const beforeYouTalk = useTodayBeforeYouTalk(active && observationPaused === false);
    useEffect(() => {
        // Drafts prepared before the pause named what Isoform observed; drop
        // them. Resuming prepares every action again.
        if (observationPaused) resetTodayChatDrafts();
    }, [observationPaused]);
    const [topicsOpen, setTopicsOpen] = useState(false);
    const tr = (key: string, options?: Record<string, unknown>) => t(key as never, options as never) as unknown as string;
    const { t: tc } = useTranslation("common");
    const { t: ta } = useTranslation("app");
    // What Isoform captured today moved to Activity (snappy-and-first-run-1.0.md,
    // section 4): it is a count with nothing to act on, and reading it scans
    // activity.db. Today keeps only the paused line, from the observation
    // state it already has.
    const pausedText = observationPaused === true;
    // Notice kinds the user moved to Today: one muted line each, under the
    // status lines (RC 1 A4).
    const { t: tSettings, i18n: settingsI18n } = useTranslation("settings");
    // Read while Today is the page in front; a retained hidden Today reads
    // nothing (snappy-and-first-run-1.0.md, section 1).
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

    // The topic panel (RC 1): pinned beside the page while Today is wide
    // enough, on the first topic waiting on the user; a Sheet otherwise,
    // opened by a click. Its box keeps prepared words across both.
    const todayTopics = useTodayTopics(active);
    // The registry is on this computer and supplies the Your topics grid.
    const registry = todayTopics.status !== "unavailable";
    // A topic opened from the sidebar may not be among today's topics.
    const [requested, setRequested] = useState<TodayTopic | null>(null);
    const [pinOverrides, setPinOverrides] = useState<Record<string, boolean>>({});
    // A registry read after a pin is the truth for the topics it lists.
    useEffect(() => {
        setPinOverrides((current) => {
            const kept = Object.fromEntries(Object.entries(current).filter(([id]) => !todayTopics.topics.some((topic) => topic.id === id)));
            return Object.keys(kept).length === Object.keys(current).length ? current : kept;
        });
    }, [todayTopics.topics]);
    const topics = useMemo(() => {
        const list = requested && !todayTopics.topics.some((topic) => topic.id === requested.id)
            ? [...todayTopics.topics, requested] : todayTopics.topics;
        return list.map((topic) => (topic.id in pinOverrides ? { ...topic, pinned: pinOverrides[topic.id] } : topic));
    }, [todayTopics.topics, requested, pinOverrides]);
    // Topic names for the tags on Isoform's cards in the panel.
    const topicNames = useMemo(() => new Map(topics.map((topic) => [topic.id, topic.name])), [topics]);
    const rootRef = useRef<HTMLDivElement>(null);
    // The space next to the sidebar (Today's own width), not the window's:
    // with the sidebar folded the panel pins on a narrower window.
    const space = useContentWidth(rootRef);
    const wide = space >= TOPIC_PANEL_PINNED_MIN_WIDTH;
    const panelWidth = topicPanelWidth(space);
    // The first two days: Getting to know you beside the brief when the page
    // has room for both, under it otherwise (first-day handoff §3).
    const firstDay = useFirstDay();
    const watched = useWatchedToday(firstDay.firstDays && active);
    const [importOpen, setImportOpen] = useState(false);
    const [imported, setImported] = useState(false);
    const knowSide = (wide ? space - panelWidth : space) >= FIRST_DAY_SIDE_MIN_WIDTH;
    const [panel, setPanel] = useState<{ topicId: string | null; sheetOpen: boolean }>({ topicId: null, sheetOpen: false });
    const needs = useTodayNeeds();
    const done = useTodayDone();
    const itemPanel = useMainChatItemPanel("today");
    const [needTaskId, setNeedTaskId] = useState<string | null>(null);
    const [doneTaskId, setDoneTaskId] = useState<string | null>(null);
    const [doneItem, setDoneItem] = useState<TodayDone | null>(null);
    const [doneMarkFailed, setDoneMarkFailed] = useState<string | null>(null);
    const [dayMeetingFailed, setDayMeetingFailed] = useState<string | null>(null);
    const [draftRetry, setDraftRetry] = useState<(() => void) | null>(null);
    const [reportRetry, setReportRetry] = useState<(() => void) | null>(null);
    const [needOpenRetry, setNeedOpenRetry] = useState<(() => void) | null>(null);
    const dayMeetingRequest = useRef(0);
    const needRequest = useRef(0);
    const [needCheckFailures, setNeedCheckFailures] = useState<Record<string, number>>({});
    const [needReceipts, setNeedReceipts] = useState<{ item: TodayNeed; result: TodayNeedResolution; index: number; receivedAt: number }[]>([]);
    useEffect(() => {
        const briefTime = Date.parse(latest.view?.brief?.asOf ?? "");
        if (Number.isFinite(briefTime)) setNeedReceipts((receipts) => {
            const retained = receipts.filter((receipt) => briefTime <= receipt.receivedAt);
            return retained.length === receipts.length ? receipts : retained;
        });
    }, [latest.view?.brief?.asOf]);
    const markDoneOpened = (id: string) => {
        setDoneMarkFailed(null);
        void MarkTodayDoneOpened(id).catch(() => setDoneMarkFailed(id));
    };
    useEffect(() => onPagePrivacyWithdrawal(() => { dayMeetingRequest.current++; needRequest.current++; setNeedReceipts([]); setNeedCheckFailures({}); setDayMeetingFailed(null); setDraftRetry(null); setReportRetry(null); setNeedOpenRetry(null); setDoneItem(null); setNeedTaskId(null); setDoneTaskId(null); setDoneMarkFailed(null); }), []);
    const openNeedTarget = (item: TodayNeed) => {
        setNeedOpenRetry(null);
        dayMeetingRequest.current++; setDayMeetingFailed(null);
        paragraphRequest.current++; setFindTimeOpen(false); setSavedPanel(null); setMeetingPrep(null); setReplyDraft(null); setMeetingNote(null); setStarterRequest(null);
        setDoneItem(null); setDoneTaskId(null); setDoneMarkFailed(null);
        setPanel({ topicId: null, sheetOpen: !wide });
        if (item.targetKind === "handoff") {
            itemPanel.close();
            setNeedTaskId(item.targetId);
        } else if (item.targetKind === "chat_item") {
            setNeedTaskId(null);
            itemPanel.openMoment(item.id, false, undefined, () => setNeedOpenRetry(() => () => openNeed(item)));
        }
    };
    const openNeed = (item: TodayNeed, slot?: number) => {
        const request = ++needRequest.current;
        const index = slot ?? needs.items.findIndex((row) => row.id === item.id);
        // Every row reads its source first: open, resolved, or couldn't check.
        void GetTodayNeedCheck(item.id, item.kind, item.targetKind, item.targetId).then((result) => {
            if (request !== needRequest.current) return;
            setNeedCheckFailures((failures) => { const next = { ...failures }; delete next[item.id]; return next; });
            if (result.kind === "resolved" || (result.kind === "you_replied" && result.at)) setNeedReceipts((receipts) => [...receipts.filter((receipt) => receipt.item.id !== item.id), { item, result, index: Math.max(0, index), receivedAt: Date.now() }]);
            else if (item.kind === "commitment_slipping") pageHandlers.onFindTime?.();
            else openNeedTarget(item);
        }).catch(() => { if (request === needRequest.current) setNeedCheckFailures((failures) => ({ ...failures, [item.id]: (failures[item.id] ?? 0) + 1 })); });
    };
    const openDone = (item: TodayDone) => {
        needRequest.current++;
        dayMeetingRequest.current++; setDayMeetingFailed(null);
        paragraphRequest.current++; setFindTimeOpen(false); setSavedPanel(null); setMeetingPrep(null); setReplyDraft(null); setMeetingNote(null); setStarterRequest(null);
        setPanel({ topicId: null, sheetOpen: !wide });
        setDoneMarkFailed(null);
        setDoneItem(null); setNeedTaskId(null); setDoneTaskId(null);
        if (item.kind === "answer_ready") {
            itemPanel.close();
            setDoneItem(item);
        } else if (item.targetKind === "handoff") {
            itemPanel.close();
            setNeedTaskId(item.targetId);
            setDoneTaskId(item.id);
        } else if (item.targetKind === "chat_item") {
            itemPanel.openMoment(item.targetId, false, () => markDoneOpened(item.id), () => setNeedOpenRetry(() => () => openDone(item)));
        } else {
            itemPanel.close();
            setDoneItem(item);
            markDoneOpened(item.id);
        }
    };
    const openReport = (target: import("@/lib/stream-helpers").MainChatReportTarget) => {
        setReportRetry(null);
        if (onOpenReportTarget) onOpenReportTarget(target, () => setReportRetry(() => () => openReport(target)));
        else handlers.onOpenReportTarget?.(target);
    };
    const openDoneTarget = (item: TodayDone) => {
        if (item.targetKind !== "automation" && item.targetKind !== "playbook") return;
        openReport({ kind: item.targetKind, id: item.targetId, runId: item.runId });
    };
    const [starterRequest, setStarterRequest] = useState<{ text: string; id: number; context?: TodayChatContextRef; firstChip?: boolean } | null>(null);
    const [savedPanel, setSavedPanel] = useState<{ text: string; context: TodayChatContextRef; status: "loading" | "ready" | "failed"; answer?: TodayPanelAnswer } | null>(null);
    const [meetingPrep, setMeetingPrep] = useState<{ meeting: TodayBeforeYouTalk; text: string; context: TodayChatContextRef } | null>(null);
    const [replyDraft, setReplyDraft] = useState<TodayReplyDraftRequest | null>(null);
    const [meetingNote, setMeetingNote] = useState<TodayMeetingNoteRequest | null>(null);
    const starterSequence = useRef(0);
    const paragraphRequest = useRef(0);
    const [findTimeOpen, setFindTimeOpen] = useState(false);
    const [mainItemOpen, setMainItemOpen] = useState(false);
    const [closeItemRequest, setCloseItemRequest] = useState(0);
    const [findTimeRange, setFindTimeRange] = useState<{ first: string; last: string; minutes: number; nextWeek?: boolean } | undefined>();
    const [holdSource, setHoldSource] = useState<HoldCardSource | null>(null);
    useEffect(() => {
        if (active) return;
        needRequest.current++;
        dayMeetingRequest.current++;
        setDayMeetingFailed(null);
        paragraphRequest.current++;
        setSavedPanel(null);
        setMeetingPrep(null);
        setReplyDraft(null);
        setMeetingNote(null);
        setTopicsOpen(false);
        setFindTimeOpen(false);
        setPanel((current) => current.sheetOpen ? { ...current, sheetOpen: false } : current);
    }, [active]);
    useEffect(() => {
        const clear = () => { paragraphRequest.current++; setSavedPanel(null); setMeetingPrep(null); setReplyDraft(null); setMeetingNote(null); };
        const offs = ["memory:withdrawn", "sensitive-apps:updated", "closed-eyes:changed", "brief:stored", "handoff:updated", "calendar:synced", "topics:withdrawn"].map((event) => PageEventsOn(event, clear));
        return () => offs.forEach((off) => off());
    }, []);
    useEffect(() => {
        if (!active || !wide || !(findTimeOpen || replyDraft || meetingNote)) return;
        const close = (event: KeyboardEvent) => {
            if (event.key !== "Escape" || event.defaultPrevented) return;
            event.preventDefault(); setFindTimeOpen(false); setReplyDraft(null); setMeetingNote(null);
        };
        document.addEventListener("keydown", close);
        return () => document.removeEventListener("keydown", close);
    }, [active, wide, findTimeOpen, replyDraft, meetingNote]);
    // One box per topic, so words typed for one topic wait while another is open.
    const [drafts, setDrafts] = useState<Record<string, { draft: string; prepared: boolean }>>({});
    // The main chat's own box in the panel keeps its words while a topic
    // opens; an answer prepared from the brief carries its Today item and
    // the context line for it.
    const [mainDraft, setMainDraftState] = useState<MainDraft>(EMPTY_MAIN_DRAFT);
    const mainDraftRef = useRef<MainDraft>(EMPTY_MAIN_DRAFT);
    const setMainDraft = useCallback((next: MainDraft | ((current: MainDraft) => MainDraft)) => {
        mainDraftRef.current = typeof next === "function" ? next(mainDraftRef.current) : next;
        setMainDraftState(mainDraftRef.current);
    }, []);
    // Each fill of the box starts over; a later fill or a cleared box drops
    // the result of an older preparation.
    const mainFill = useRef(0);
    const mainFilledText = useRef("");
    // Only a click switches the panel (a topic card, a paragraph's topic, a
    // question, a pinned topic, a Search result); the one exception is the
    // first time the panel shows, when the topics are already known and one
    // waits on the user. Otherwise the panel is the whole main chat, and it
    // stays so once topics load, so it never jumps while the user types.
    const retainedTopic = useRef<TodayTopic | null>(null);
    const publishedTopic = topics.find((topic) => topic.id === panel.topicId) ?? null;
    if (publishedTopic) retainedTopic.current = publishedTopic;
    const shownTopic = publishedTopic ?? (retainedTopic.current?.id === panel.topicId ? retainedTopic.current : null);
    useEffect(() => {
        onActiveTopicChange?.(wide || panel.sheetOpen ? shownTopic?.id ?? null : null);
    }, [onActiveTopicChange, wide, panel.sheetOpen, shownTopic?.id]);
    const autoPicked = useRef(false);
    useEffect(() => {
        if (!wide || autoPicked.current) return;
        autoPicked.current = true;
        if (todayTopics.status !== "ready") return;
        const first = firstTopic(topics);
        if (first?.status !== "waiting") return;
        setPanel((current) => (current.topicId ? current : { topicId: first.id, sheetOpen: false }));
    }, [wide, todayTopics.status, topics]);
    // The Sheet is only for narrow windows; a wide one pins the panel.
    useEffect(() => {
        if (wide) setPanel((current) => (current.sheetOpen ? { ...current, sheetOpen: false } : current));
    }, [wide]);
    const openTopic = useCallback((topicId: string, draft?: string): boolean => {
        const selected = topics.find((topic) => topic.id === topicId);
        if (!selected) return false;
        needRequest.current++; setNeedOpenRetry(null); itemPanel.close();
        dayMeetingRequest.current++; setDayMeetingFailed(null);
        paragraphRequest.current++;
        setSavedPanel(null);
        setMeetingPrep(null);
        setReplyDraft(null);
        setMeetingNote(null);
        setFindTimeOpen(false);
        retainedTopic.current = selected;
        setNeedTaskId(null); setDoneTaskId(null); setDoneItem(null);
        if (draft !== undefined) setDrafts((current) => ({ ...current, [topicId]: { draft, prepared: true } }));
        setPanel({ topicId, sheetOpen: !wide });
        // A topic opened from the sidebar leaves Today's list once another opens.
        setRequested((current) => (current && current.id !== topicId ? null : current));
        return true;
    }, [topics, wide]);
    useEffect(() => {
        if (!topicRequest) return;
        needRequest.current++; dayMeetingRequest.current++; paragraphRequest.current++;
        setNeedOpenRetry(null); setDayMeetingFailed(null); setSavedPanel(null); setMeetingPrep(null); setReplyDraft(null); setMeetingNote(null); setStarterRequest(null); itemPanel.close();
        setFindTimeOpen(false);
        retainedTopic.current = topicRequest.topic;
        setRequested(topicRequest.topic);
        setNeedTaskId(null); setDoneTaskId(null); setDoneItem(null);
        setPanel({ topicId: topicRequest.topic.id, sheetOpen: !wide });
        onTopicRequestDone?.();
        // eslint-disable-next-line react-hooks/exhaustive-deps
    }, [topicRequest?.nonce]);
    const togglePin = useCallback(async (topic: TodayTopic): Promise<boolean> => {
        try {
            await PinTopicToSidebar(topic.id, !topic.pinned);
        } catch {
            return false;
        }
        setPinOverrides((current) => ({ ...current, [topic.id]: !topic.pinned }));
        if (requested?.id === topic.id) setRequested({ ...requested, pinned: !topic.pinned });
        window.dispatchEvent(new Event(SIDEBAR_TOPICS_CHANGED));
        return true;
    }, [requested]);
    // An answer, a brief card or a question with no topic fills the box on
    // the right, unsent (Claude Design): the main chat panel, or the Sheet on
    // a narrow window. It is prepared exactly as the main chat's own box
    // prepares it (prepareTodayDraft): the computer's current words, the
    // reference Go checks again at Send, the paused draft, and the context
    // line while it loads.
    const prepareInMainChat = useCallback(async (prompt: string, context: TodayChatContextRef, options: TodayChatStartOptions = {}) => {
        if (!prompt.trim()) return;
        const id = ++mainFill.current;
        setDraftRetry(null);
        const box: TodayDraftBox = {
            fill: (text, itemContext, preview) => {
                mainFilledText.current = text;
                setMainDraft({ draft: text, prepared: true, ref: itemContext ?? null, preview });
                setNeedTaskId(null); setDoneTaskId(null); setDoneItem(null);
                setPanel({ topicId: null, sheetOpen: !wide });
            },
            update: (patch) => setMainDraft((current) => ({
                ...current,
                ...("context" in patch ? { ref: patch.context ?? null } : {}),
                ...(patch.preview ? { preview: patch.preview } : {}),
                ...(patch.text !== undefined ? { draft: patch.text } : {}),
            })),
            clear: () => {
                mainFill.current++;
                setMainDraft(EMPTY_MAIN_DRAFT);
            },
            stillApplies: () => mainFill.current === id && mainDraftRef.current.draft !== "",
            unedited: () => mainDraftRef.current.draft === mainFilledText.current,
            onCardGone: () => setDraftRetry(() => () => void prepareInMainChat(prompt, context, options)),
        };
        await prepareTodayDraft(box, prompt, context, options, t, i18n.resolvedLanguage ?? i18n.language);
        // eslint-disable-next-line react-hooks/exhaustive-deps
    }, [wide, setMainDraft, t, i18n]);
    const openSourceAsk = useCallback((text: string, context: TodayChatContextRef, meeting?: TodayBeforeYouTalk) => {
        if (!text.trim()) return;
        needRequest.current++; setNeedOpenRetry(null);
        setNeedTaskId(null); setDoneTaskId(null); setDoneItem(null); itemPanel.close();
        const id = ++paragraphRequest.current;
        setFindTimeOpen(false);
        setReplyDraft(null);
        setMeetingNote(null);
        setMeetingPrep(meeting ? { meeting, text, context } : null);
        setSavedPanel({ text, context, status: "loading" });
        setPanel({ topicId: null, sheetOpen: !wide });
        void GetTodayPanelAnswer(context).then((answer) => {
            if (id !== paragraphRequest.current) return;
            if (answer && (meeting || answer.question === text)) setSavedPanel({ text, context, status: "ready", answer });
            else {
                setSavedPanel(null);
                setStarterRequest({ text, context, id: ++starterSequence.current });
            }
        }).catch(() => { if (id === paragraphRequest.current) setSavedPanel({ text, context, status: "failed" }); });
    }, [wide]);
    const pageHandlers = useMemo<TodayActionHandlers>(() => ({
        ...handlers,
        onStartChat: (prompt, context, options) => {
            needRequest.current++; setNeedOpenRetry(null);
            setNeedTaskId(null); setDoneTaskId(null); setDoneItem(null); itemPanel.close();
            paragraphRequest.current++;
            setFindTimeOpen(false);
            setSavedPanel(null);
            setMeetingPrep(null);
            setReplyDraft(null);
            setMeetingNote(null);
            setStarterRequest(null);
            void prepareInMainChat(prompt, context, options);
        },
        onFindTimeRange: (range) => {
            pageHandlers.onFindTime?.();
            setFindTimeRange(range);
        },
        onFindTime: (source) => {
            setFindTimeRange(undefined);
            needRequest.current++; setNeedOpenRetry(null);
            setNeedTaskId(null); setDoneTaskId(null); setDoneItem(null); itemPanel.close();
            setHoldSource(source ? { ...source, telemetryAction: source.telemetryAction ?? (source.thing.sourceKind === "brief" ? "T14" : source.thing.sourceKind === "week" ? "T84" : "D01") } : null);
            paragraphRequest.current++;
            setSavedPanel(null);
            setMeetingPrep(null);
            setReplyDraft(null);
            setMeetingNote(null);
            setStarterRequest(null);
            setFindTimeOpen(true);
            setPanel({ topicId: null, sheetOpen: !wide });
        },
        onPrefillChat: (text) => {
            needRequest.current++; setNeedOpenRetry(null);
            setNeedTaskId(null); setDoneTaskId(null); setDoneItem(null); itemPanel.close();
            if (!text.trim()) return;
            setFindTimeOpen(false);
            paragraphRequest.current++;
            setSavedPanel(null);
            setMeetingPrep(null);
            setReplyDraft(null);
            setMeetingNote(null);
            setStarterRequest({ text, id: ++starterSequence.current, firstChip: true });
            setPanel({ topicId: null, sheetOpen: !wide });
        },
        answeredBriefCards,
        onAskBriefCard: (text, context) => openSourceAsk(text, context),
        onDraftBriefCard: (briefId, card, newMessage) => {
            needRequest.current++; setNeedOpenRetry(null);
            setNeedTaskId(null); setDoneTaskId(null); setDoneItem(null); itemPanel.close();
            setFindTimeOpen(false);
            paragraphRequest.current++;
            setSavedPanel(null);
            setMeetingPrep(null);
            setStarterRequest(null);
            setMeetingNote(null);
            setReplyDraft({ briefId, cardId: card.id, message: card.message, label: card.label ?? "", id: ++starterSequence.current, ...(newMessage ? { newMessage: true } : {}) });
            setPanel({ topicId: null, sheetOpen: !wide });
        },
        onDraftMeetingNote: (meeting, prompt) => {
            needRequest.current++; setNeedOpenRetry(null);
            setNeedTaskId(null); setDoneTaskId(null); setDoneItem(null); itemPanel.close();
            paragraphRequest.current++;
            setFindTimeOpen(false);
            setSavedPanel(null);
            setMeetingPrep(null);
            setReplyDraft(null);
            setStarterRequest(null);
            setMeetingNote({ meeting, prompt, id: ++starterSequence.current });
            setPanel({ topicId: null, sheetOpen: !wide });
        },
        onOpenWeekTask: (id) => {
            dayMeetingRequest.current++; needRequest.current++; setDayMeetingFailed(null); setNeedOpenRetry(null);
            paragraphRequest.current++; setFindTimeOpen(false); setSavedPanel(null); setMeetingPrep(null); setReplyDraft(null); setMeetingNote(null); setStarterRequest(null);
            itemPanel.close(); setDoneItem(null); setDoneTaskId(null); setNeedTaskId(id); setPanel({ topicId: null, sheetOpen: !wide });
        },
        onOpenDayMeeting: (id) => {
            needRequest.current++; setNeedOpenRetry(null);
            const request = ++dayMeetingRequest.current;
            setDayMeetingFailed(null);
            const started = performance.now();
            void GetTodayDayMeetingTarget(id).then((target) => {
                if (request !== dayMeetingRequest.current) return;
                if (target.kind === "notes" && target.sessionId && handlers.onOpenMeetingNotes) {
                    handlers.onOpenMeetingNotes(target.sessionId);
                } else if (target.kind === "prep" && target.meeting) {
                    const meeting: TodayBeforeYouTalk = target.meeting;
                    const time = computerClock(new Date(meeting.startAt), i18n.resolvedLanguage);
                    openSourceAsk(tr("beforeYouTalk.briefPrompt", { title: meeting.meetingTitle, name: meeting.personName, time }), { sourceKind: "calendar", sourceId: meeting.meetingId, meetingPrep: true, meetingStartMs: meeting.startAt, generatedTitle: meeting.meetingTitle, generatedPeople: [meeting.personName] }, meeting);
                } else {
                    throw new Error("meeting target unavailable");
                }
                void RecordTodayAction("today", "T31", target.kind === "prep" ? "ask" : "open", "opened", Math.max(0, Math.round(performance.now() - started))).catch(() => {});
            }).catch(() => {
                if (request !== dayMeetingRequest.current) return;
                setDayMeetingFailed(id);
                void RecordTodayAction("today", "T31", "open", "failed", Math.max(0, Math.round(performance.now() - started))).catch(() => {});
            });
        },
        onBriefMeeting: (meeting, text) => openSourceAsk(text, { sourceKind: "calendar", sourceId: meeting.meetingId, meetingPrep: true, meetingStartMs: meeting.startAt, generatedTitle: meeting.meetingTitle, generatedPeople: [meeting.personName] }, meeting),
        onAskAboutParagraph: async (briefId, paragraph, text, question) => {
            needRequest.current++; setNeedOpenRetry(null);
            setNeedTaskId(null); setDoneTaskId(null); setDoneItem(null); itemPanel.close();
            setFindTimeOpen(false);
            const id = ++paragraphRequest.current;
            setSavedPanel(null);
            setMeetingPrep(null);
            setReplyDraft(null);
            setMeetingNote(null);
            try {
                const context = await PrepareTodayBriefParagraph(briefId, paragraph, text);
                if (id !== paragraphRequest.current) return true;
                setPanel({ topicId: null, sheetOpen: !wide });
                setSavedPanel({ text: question, context, status: "loading" });
                let answer: TodayPanelAnswer | null;
                try { answer = await GetTodayPanelAnswer(context); }
                catch {
                    if (id === paragraphRequest.current) setSavedPanel({ text: question, context, status: "failed" });
                    return true;
                }
                if (id !== paragraphRequest.current) return true;
                if (answer?.question === question) setSavedPanel({ text: question, context, status: "ready", answer });
                else { setSavedPanel(null); setStarterRequest({ text: question, context, id: ++starterSequence.current }); }
                return true;
            } catch {
                return id !== paragraphRequest.current;
            }
        },
        ...(topics.length === 0 ? {} : {
            onOpenTopic: (topicId: string) => void openTopic(topicId),
            onPrepareInTopic: (topicId: string, text: string) => openTopic(topicId, text),
        }),
    }), [handlers, openTopic, openSourceAsk, prepareInMainChat, topics.length, wide, answeredBriefCards, i18n, t]);
    const clockContext = clockContextKey();
    const panelProps = useMemo(() => shownTopic ? {
        topic: shownTopic,
        clockContext,
        draft: drafts[shownTopic.id]?.draft ?? "",
        prepared: drafts[shownTopic.id]?.prepared ?? false,
        onDraftChange: (draft: string, prepared: boolean) => setDrafts((current) => ({ ...current, [shownTopic.id]: { draft, prepared } })),
        handlers,
        observationPaused,
        onTogglePin: registry ? () => togglePin(shownTopic) : undefined,
    } : null, [shownTopic, clockContext, drafts, handlers, observationPaused, registry, togglePin]);
    const mainChatPanelProps = {
        sharedSession: mainChatSession,
        onLoadSharedMore: onLoadMainChatMore,
        onHydrateShared: onHydrateMainChat,
        hideRoutine: hideMainChatRoutine,
        topicsFailed: todayTopics.status === "failed",
        topicsRefreshing: todayTopics.refreshing || todayTopics.status === "loading",
        topicsGathering: todayTopics.firstRun && todayTopics.topics.length === 0,
        onRetryTopics: () => void todayTopics.refresh(),
        draft: mainDraft.draft,
        prepared: mainDraft.prepared,
        contextRef: mainDraft.ref,
        preview: mainDraft.preview,
        topicNames,
        onOpenReportTarget: openReport,
        starterRequest,
        closeItemRequest,
        onItemOpenChange: setMainItemOpen,
        savedPanel,
        meetingPrep,
        onRefreshSaved: () => {
            const request = savedPanel ?? meetingPrep;
            if (!request) return;
            setStarterRequest({ text: request.text, context: request.context, id: ++starterSequence.current });
            setSavedPanel(null);
        },
        onRetrySaved: () => { if (savedPanel) openSourceAsk(savedPanel.text, savedPanel.context, meetingPrep?.meeting); },
        onLeaveSaved: () => { paragraphRequest.current++; setStarterRequest(null); setSavedPanel(null); setMeetingPrep(null); setReplyDraft(null); setMeetingNote(null); },
        onStarterConsumed: (id: number) => setStarterRequest((current) => current?.id === id ? null : current),
        // Words cleared or sent drop the item they were prepared from and
        // end its preparation; edits keep it, so Send still asks Go.
        onDraftChange: (draft: string, prepared: boolean) => {
            if (!draft) {
                mainFill.current++;
                setMainDraft(EMPTY_MAIN_DRAFT);
            } else {
                setMainDraft((current) => ({ ...current, draft, prepared }));
            }
        },
        handlers,
    };

    const briefShown = latest.view?.brief;
    const eveningReady = firstDay.day === 0 && !!briefShown && briefSlotKey(briefShown.slot, briefShown.asOf, briefShown.timezone) === "evening";
    const nextBrief = status.brief?.enabled && status.brief.nextRunAt ? new Date(status.brief.nextRunAt) : null;
    const eveningAt = nextBrief && nextBrief.toDateString() === new Date().toDateString() && nextBrief.getHours() >= 12 ? status.brief!.nextRunAt : null;
    const scrollTo = (testId: string) => document.querySelector(`[data-testid="${testId}"]`)?.scrollIntoView({ behavior: "smooth", block: "start" });
    const showProjects = firstDay.firstDays && historyFound(firstDay.history) && firstDay.history.projects.length > 0 && todayTopics.topics.length === 0;
    const knowCard = firstDay.firstDays && firstDay.day !== null ? (
        <GettingToKnowYou
            day={firstDay.day}
            active={active}
            history={firstDay.history}
            calendar={firstDay.calendar}
            watched={watched}
            topicNames={todayTopics.topics.map((topic) => topic.name)}
            evening={{ at: eveningReady ? briefShown?.asOf ?? eveningAt : eveningAt, ready: eveningReady }}
            placement={knowSide ? "side" : "below"}
            imported={imported}
            onOpenPeople={() => handlers.onOpenPeople()}
            onOpenAboutYou={() => handlers.onOpenMemory()}
            onOpenTopics={registry && !showProjects ? () => scrollTo("today-topics") : undefined}
            onOpenBrief={briefShown && (!latest.view?.empty || latest.view?.quiet) ? () => scrollTo("brief-card") : undefined}
            onImport={() => setImportOpen(true)}
        />
    ) : null;
    const knowAside = !!knowCard && knowSide;
    // Day 1's first brief says what it was made from, with chips from the
    // same facts; the strip says where its events came from (handoff §3).
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
        <div ref={rootRef} className="flex min-h-0 min-w-0 flex-1">
            <div className="today-page-content patina-ground min-w-0 flex-1 overflow-y-auto">
                {/* Empty space under the title strip drags the window. On a
                    narrow window the Chat button opens the panel as a Sheet,
                    since with no topic nothing else would. */}
                <div className="window-drag relative h-7">
                    {!wide && (
                        <Button
                            size="sm"
                            variant="quiet"
                            className="absolute right-4 top-3"
                            onClick={() => measureToday("T67", "open", "opened", () => setPanel((current) => ({ ...current, sheetOpen: true })))}
                            data-testid="today-open-chat"
                        >
                            <MessageSquare className="size-3.5" />
                            {tr("mainChatPanel.open")}
                        </Button>
                    )}
                </div>
                <div
                    data-testid="today-column"
                    className={cn("mx-auto w-full px-6 pb-14", knowAside ? "max-w-[1080px]" : "max-w-[760px]")}
                >
                    {fullBrief && latest.view ? (
                        <TodayBriefFull
                            brief={fullBrief}
                            view={latest.view}
                            active={active && !handOff}
                            items={work.list?.items ?? []}
                            onAnswer={work.answer}
                            onNotRight={work.answer}
                            onReply={pageHandlers.onAskAboutParagraph ? (index, text) => { void pageHandlers.onAskAboutParagraph!(fullBrief.briefId, index, text, ""); } : undefined}
                            onOpenBrief={(id, title) => { setBriefFull(false); openBrief(id, title); }}
                            onBack={() => setBriefFull(false)}
                        />
                    ) : topicPageId && todayTopics.topics.some((topic) => topic.id === topicPageId) ? (
                        <TodayTopicPage
                            topic={todayTopics.topics.find((topic) => topic.id === topicPageId)!}
                            list={work.list}
                            active={active && !handOff}
                            onAnswer={work.answer}
                            onUndo={work.undo}
                            onHandOff={openHandOff}
                            onAsk={() => { void openTopic(topicPageId); }}
                            onBack={() => setTopicPageId(null)}
                        />
                    ) : <>
                    <header className="window-drag mb-[18px] flex flex-col items-start gap-1.5 text-left" data-testid="today-header">
                        <div className="flex w-full flex-wrap items-baseline justify-between gap-x-4 gap-y-1">
                            <h1 className="display text-ink" aria-label={work.list && work.list.toAnswer > 0 ? tr("ctoday.titleCount", { count: work.list.toAnswer }) : undefined}>{tr("ctoday.title")}</h1>
                            {briefTimes && (() => {
                                const morning = scheduleClock(briefTimes.morning, i18n.language, tr);
                                const evening = scheduleClock(briefTimes.evening, i18n.language, tr);
                                return morning && evening ? <span className="text-[12.5px] text-muted" data-testid="today-brief-times">{tr("ctoday.times", { morning, evening })}</span> : null;
                            })()}
                        </div>
                        <TodayStatus
                            date={todayLabel(i18n.language)}
                            brief={status.brief && latest.view ? { ...status.brief, pendingSlot: latest.view.pendingSlot, pendingAt: latest.view.pendingAt, retryAt: latest.view.retryAt, pendingAttempts: latest.view.pendingAttempts, pendingCause: latest.view.pendingCause } : status.brief}
                            building={!!latest.view?.building || latest.make === "making"}
                            onMissedWhy={handlers.onOpenBriefDetail}
                            signOut={assistantSignOut}
                        />
                    </header>

                    {/* The brief the Mac built for today, or when it comes,
                        then Before you talk when eligible, Your topics, the
                        week's suggestion and the rest under "More from today". */}
                    <div className={knowAside ? "grid grid-cols-[minmax(0,1fr)_270px] items-start gap-5" : undefined} data-testid="today-first-day-grid">
                    <TodayBriefSection
                        dayShape={dayShape}
                        dayShapeDate={dayShapeDate}
                        dayShapeFailure={dayShapeFailed ? { lastUpdatedAt: dayShapeUpdatedAt, retry: retryDayShape } : null}
                        quietFooter={quietLines.length > 0 || pausedText ? <>{quietLines.map((line) => <p key={line.id} data-testid="today-quiet-line">{line.text}</p>)}{pausedText && <TodayPauseLine />}</> : null}
                        band
                        onOpenPastBriefs={openPastBriefs}
                        onReadAll={() => setBriefFull(true)}
                        afterBand={work.list ? <TodayYourWork list={work.list} active={active && !handOff} onAnswer={work.answer} onUndo={work.undo} onHandOff={openHandOff} noAgents={!!yourAgents && !yourAgents.agents.some((agent) => agent.connected)} onOpenRef={(ref) => {
                            // A topic opens beside Today; this brief opens in full; an earlier one in the panel.
                            if (ref.startsWith("topic:")) {
                                const id = ref.slice("topic:".length);
                                if (todayTopics.topics.some((topic) => topic.id === id)) { setBriefFull(false); setTopicPageId(id); return true; }
                                return openTopic(id);
                            }
                            if (ref.startsWith("brief:")) {
                                const id = ref.slice("brief:".length);
                                if (id === latest.view?.brief?.briefId && bandBrief(latest.view)) setBriefFull(true);
                                else openBrief(id);
                                return true;
                            }
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
                        goalCheckIn={goalCheckIn.checkIn}
                        onGoalCheckInAnswered={goalCheckIn.refresh}
                        firstBrief={firstBrief}
                        beforeTopics={(draftRetry || dayMeetingFailed || beforeYouTalk.card || needReceipts.length > 0 || needs.items.length > 0 || done.items.length > 0) && (
                            <>
                                {draftRetry && <button type="button" role="alert" className="self-start text-xs text-error" onClick={() => measureToday("T12", "open", "opened", () => { setDraftRetry(null); draftRetry(); })}>{tr("todayBrief.cardGone")}</button>}
                                {dayMeetingFailed && <p role="alert" className="text-xs text-error"><button type="button" onClick={() => pageHandlers.onOpenDayMeeting?.(dayMeetingFailed)}>{tr("dayStrip.openFailed")}</button></p>}
                                {beforeYouTalk.card && (
                                    <TodayBeforeYouTalkCard key={beforeYouTalk.card.meetingId} card={beforeYouTalk.card} handlers={pageHandlers} onDismiss={beforeYouTalk.dismiss} onMute={beforeYouTalk.mute} active={active} />
                                )}
                                <TodayNeedsSection items={needs.items} receipts={needReceipts} failures={needCheckFailures} active={active} onOpen={openNeed} onOpenTasks={() => pageHandlers.onOpenTasks?.()} />
                                <TodayDoneSection items={done.items} active={active} onOpen={openDone} onOpenTasks={() => pageHandlers.onOpenDoneTasks?.()} />
                            </>
                        )}
                        afterTopics={(!knowAside && knowCard) || portraitCard.card ? <>
                            {!knowAside && knowCard}
                            {portraitCard.card && <TodayPortraitCard card={portraitCard.card} onRead={() => pageHandlers.onOpenMemory()} onDismiss={portraitCard.dismiss} />}
                        </> : null}
                        topics={showProjects && firstDay.history ? (
                            <YourProjects history={firstDay.history} active={active} />
                        ) : registry ? (
                            <TodayTopicsGrid
                                active={active}
                                topics={todayTopics.topics}
                                briefTopicIds={(latest.view?.brief?.narrative ?? []).flatMap((p) => p.topicId ? [p.topicId] : [])}
                                status={todayTopics.status}
                                refreshing={todayTopics.refreshing}
                                refreshFailed={todayTopics.refreshFailed}
                                lastUpdatedAt={todayTopics.lastUpdatedAt}
                                firstRun={todayTopics.firstRun}
                                selectedId={shownTopic?.id ?? null}
                                onPick={(topic) => void openTopic(topic.id)}
                                onRetry={() => void todayTopics.refresh()}
                                onEdit={() => setTopicsOpen(true)}
                                source={firstDay.firstDays && historyFound(firstDay.history)
                                    ? tr("firstDay.topicsMerged", { history: historyName(firstDay.history.assistants, tc) })
                                    : undefined}
                            />
                        ) : null}
                    />
                    {knowAside && <aside className="sticky top-4">{knowCard}</aside>}
                    </div>
                    </>}
                </div>
            </div>
            {/* At desktop widths the panel is always on screen beside the
                page, never a drawer that starts closed (handoff): a topic
                when one is open, otherwise the whole main chat. */}
            {wide && (
                <div className="shrink-0" style={{ width: panelWidth }} data-testid="topic-panel-pinned">
                    {/* Keyed by topic: a send still on its way stays with
                        the topic it was sent in. */}
                    {findTimeOpen ? (
                        <div className="flex h-full flex-col gap-3 border-l border-border-subtle bg-background-accent p-4" data-testid="today-find-time-panel">
                            <div className="flex items-center justify-between gap-3">
                                <h2 className="text-[14px] font-semibold text-ink">{tChat("mainChat.card.button.findTime")}</h2>
                                <Button size="icon" variant="quiet" aria-label={tChat("mainChat.proactive.closePanel")} onClick={() => measureToday(holdSource?.telemetryAction ?? "T31", "open", "opened", () => setFindTimeOpen(false))}><X className="size-4" /></Button>
                            </div>
                            {holdSource ? <HoldCard source={holdSource} onClose={() => setFindTimeOpen(false)} /> : <FindTimeAnswer key={findTimeRange ? `${findTimeRange.first}:${findTimeRange.last}:${findTimeRange.minutes}` : "unscoped"} range={findTimeRange} />}
                        </div>
                    ) : panelProps ? (
                        <TopicPanel key={panelProps.topic.id} {...panelProps} variant="pinned" />
                    ) : doneItem ? (
                        doneItem.kind === "answer_ready" ? <TodayDoneAnswerPanel item={doneItem} onClose={() => setDoneItem(null)} /> : <TodayDoneResultPanel item={doneItem} onClose={() => setDoneItem(null)} onOpenTarget={openDoneTarget} />
                    ) : needTaskId ? (
                        <TaskThreadPane id={needTaskId} telemetryAction={doneTaskId ? "T80" : "D01"} onClose={() => { setNeedTaskId(null); setDoneTaskId(null); }} onOpened={doneTaskId ? () => markDoneOpened(doneTaskId) : undefined} />
                    ) : meetingNote ? (
                        <TodayMeetingNotePanel key={meetingNote.id} request={meetingNote} onClose={() => setMeetingNote(null)} />
                    ) : replyDraft ? (
                        <TodayReplyDraftPanel request={replyDraft} onClose={() => setReplyDraft(null)} />
                    ) : pastBriefsOpen ? (
                        <TodayPastBriefsPanel currentBriefId={latest.view?.brief?.briefId} onOpen={openBrief} onClose={() => setPastBriefsOpen(false)} />
                    ) : lastBriefPanel ? (
                        <TodayLastBriefPanel key={lastBriefPanel} briefId={lastBriefPanel} title={lastBriefTitle} onClose={() => setLastBriefPanel(null)} />
                    ) : (
                        <MainChatPanel {...mainChatPanelProps} variant="pinned" />
                    )}
                    {needOpenRetry && <button type="button" role="alert" className="px-4 py-2 text-xs text-error" onClick={() => measureToday("D01", "open", "opened", () => { setNeedOpenRetry(null); needOpenRetry(); })}>{tr("actions.chatOpenFailed")}</button>}
                    {reportRetry && <button type="button" role="alert" className="px-4 py-2 text-xs text-error" onClick={() => measureToday("T70", "open", "opened", () => { setReportRetry(null); reportRetry(); })}>{tr("actions.reportOpenFailed")}</button>}
                    {doneMarkFailed && <button type="button" className="px-4 py-2 text-sm text-muted" onClick={() => measureToday("T21", "do", "done", () => markDoneOpened(doneMarkFailed))}>{tr("doneForYou.saveFailed")}</button>}
                </div>
            )}
            <Sheet open={active && !wide && panel.sheetOpen} onOpenChange={(open) => { setPanel((current) => ({ ...current, sheetOpen: open })); if (!open) { setFindTimeOpen(false); setReplyDraft(null); setMeetingNote(null); } }}>
                <SheetContent aria-describedby={undefined} onEscapeKeyDown={(event) => {
                    if (findTimeOpen || replyDraft || meetingNote || savedPanel || meetingPrep || mainItemOpen) {
                        event.preventDefault(); setCloseItemRequest((value) => value + 1); paragraphRequest.current++; setStarterRequest(null); setFindTimeOpen(false); setReplyDraft(null); setMeetingNote(null); setSavedPanel(null); setMeetingPrep(null);
                    }
                }}>
                    {!wide && (findTimeOpen
                        ? <><div className="flex items-center justify-between gap-3"><SheetTitle>{tChat("mainChat.card.button.findTime")}</SheetTitle><Button size="icon" variant="quiet" aria-label={tChat("mainChat.proactive.closePanel")} onClick={() => setFindTimeOpen(false)}><X className="size-4" /></Button></div>{holdSource ? <HoldCard source={holdSource} onClose={() => setFindTimeOpen(false)} /> : <FindTimeAnswer key={findTimeRange ? `${findTimeRange.first}:${findTimeRange.last}:${findTimeRange.minutes}` : "unscoped"} range={findTimeRange} />}</>
                        : panelProps
                        ? <TopicPanel key={panelProps.topic.id} {...panelProps} variant="sheet" />
                        : doneItem ? (doneItem.kind === "answer_ready" ? <TodayDoneAnswerPanel item={doneItem} onClose={() => setDoneItem(null)} /> : <TodayDoneResultPanel item={doneItem} onClose={() => setDoneItem(null)} onOpenTarget={openDoneTarget} />)
                        : needTaskId ? <TaskThreadPane id={needTaskId} telemetryAction={doneTaskId ? "T80" : "D01"} onClose={() => { setNeedTaskId(null); setDoneTaskId(null); }} onOpened={doneTaskId ? () => markDoneOpened(doneTaskId) : undefined} />
                        : meetingNote ? <TodayMeetingNotePanel key={meetingNote.id} request={meetingNote} sheet onClose={() => setMeetingNote(null)} />
                        : replyDraft ? <TodayReplyDraftPanel request={replyDraft} sheet onClose={() => { setReplyDraft(null); setMeetingNote(null); }} />
                        : pastBriefsOpen ? <TodayPastBriefsPanel currentBriefId={latest.view?.brief?.briefId} onOpen={openBrief} onClose={() => setPastBriefsOpen(false)} />
                        : lastBriefPanel ? <TodayLastBriefPanel key={lastBriefPanel} briefId={lastBriefPanel} title={lastBriefTitle} onClose={() => setLastBriefPanel(null)} />
                        : <MainChatPanel {...mainChatPanelProps} variant="sheet" />)}
                    {needOpenRetry && <button type="button" role="alert" className="px-4 py-2 text-xs text-error" onClick={() => measureToday("D01", "open", "opened", () => { setNeedOpenRetry(null); needOpenRetry(); })}>{tr("actions.chatOpenFailed")}</button>}
                    {reportRetry && <button type="button" role="alert" className="px-4 py-2 text-xs text-error" onClick={() => measureToday("T70", "open", "opened", () => { setReportRetry(null); reportRetry(); })}>{tr("actions.reportOpenFailed")}</button>}
                    {doneMarkFailed && <button type="button" className="px-4 py-2 text-sm text-muted" onClick={() => measureToday("T21", "do", "done", () => markDoneOpened(doneMarkFailed))}>{tr("doneForYou.saveFailed")}</button>}
                </SheetContent>
            </Sheet>
            {active && handOff && <TodayHandOffSheet key={handOff.item.id} item={handOff.item} edited={handOff.edited} twoStep={handOff.twoStep} failedText={work.failedText} agents={yourAgents} agentsFailed={agentsFailed} onRetryAgents={readYourAgents} onSend={(input) => work.answer(handOff.item.id, input)} onClose={() => setHandOff(null)} />}
            <AIMemoryImportDialog open={active && importOpen} onOpenChange={setImportOpen} onImported={() => setImported(true)} />
            <YourTopicsDialog open={active && topicsOpen} onOpenChange={(open) => { if (open) setTopicsOpen(true); else measureToday("T59", "open", "opened", () => setTopicsOpen(false)); }} onChanged={finds.refresh} />
        </div>
    );
}

/** An element's width in px, followed as it resizes; 0 before it is measured. */
function useContentWidth(ref: React.RefObject<HTMLElement>): number {
    const [width, setWidth] = useState(0);
    useLayoutEffect(() => {
        const element = ref.current;
        if (!element) return;
        const measure = () => {
            const width = Math.round(element.getBoundingClientRect().width);
            if (width > 0) setWidth(width);
        };
        measure();
        if (typeof ResizeObserver === "undefined") return;
        const observer = new ResizeObserver(measure);
        observer.observe(element);
        return () => observer.disconnect();
    }, [ref]);
    return width;
}
