import { measureToday, recordTodayAction } from "@/lib/today-telemetry";
import { assistantLabel } from "@/lib/brief-assistant";
import { useTodaySectionSeen } from "@/hooks/useTodaySectionSeen";
import { isCommitmentItemKey, isFindTimeLabel } from "./commitment-key";
import { findTimeRangeFor } from "@/lib/find-time-range";
import { useCallback, useEffect, useId, useMemo, useReducer, useRef, useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { ArrowUpRight, Check, ChevronRight, Loader2, MessageCircle, Video } from "lucide-react";
import { Button } from "@/components/ui/button";
import { DigestMarkdown } from "@/components/digest-markdown";
import { cn } from "@/lib/utils";
import { BrowserOpenURL } from "../../../wailsjs/runtime/runtime";
import {
    ReopenMainChatSetupSteps,
    GetHoldSettings,
    OpenExternalURL,
    OpenPermissionSettings,
    RemoveHold,
    UndoRemoveHold,
    PostTodayClosingAnswer,
    GetTodayPrepMeetingForPerson,
    GetTodayBriefDraftRoute,
    type MobileBriefDocument,
    type MobileBriefCard,
    type MobileBriefItem,
    type MobileBriefRef,
    type MobileBriefSections,
    type TodayBriefView,
    type TodayDayShape,
    type GoalCheckInView,
} from "@/lib/wails-shim-app";
import type { MakeBriefState } from "@/hooks/useLatestBrief";
import type { TodayActionHandlers } from "./types";
import { BriefSignIn } from "./BriefSignIn";
import { TodayPermissionControl } from "./TodayPermissionControl";
import { isDisplayTopic } from "./topics";
import { todayBriefCardRef, type TodayChatContextRef } from "@/lib/today-chat-navigation";
import { useFirstBriefSeen } from "@/hooks/useFirstBriefSeen";
import { describeBriefTime } from "./TodayStatus";
import { upcomingWhen } from "@/components/automations/upcoming-text";
import { createScheduleTextLabels } from "@/components/automations/schedule-utils";
import { briefClockTime, briefDateTime, clockTime, shortBriefClockLabel, shortBriefTimes, computerHourOptions } from "@/lib/brief-time";
import { briefHasWords, briefLateLine, briefTitle } from "@/lib/brief-title";

import { handoffBlockedReasonKey } from "@/components/handoffs/handoffStatus";
import { TodayGoalMenu } from "./TodayGoalMenu";
import { TodayGoalCheckIn } from "./TodayGoalCheckIn";
import { TodayDayStrip, type HoldPreview } from "./TodayDayStrip";
import { briefForDisplay } from "@/lib/brief-hygiene";
import { activeInterval } from "@/lib/app-activity";
import { pageQueryCache } from "@/lib/query-cache";
import { BriefAudioPlayer } from "./BriefAudioPlayer";
import { YourWeekCard } from "./YourWeekCard";
import { TodayWeekBeliefReceipt, useTodayWeekBelief, type TodayWeekBeliefReview } from "./useTodayWeekBelief";
import { useBriefHoldOffer } from "./useBriefHoldOffer";
import { useDirectHold } from "./useDirectHold";
import { MorningWeekQuestion } from "./MorningWeekQuestion";
import { TodayNoticedAction } from "./TodayNoticedAction";

export { briefSlotKey } from "@/lib/brief-time";

type BriefSection = keyof MobileBriefSections | "upcoming";

type Translate = (key: string, options?: Record<string, unknown>) => string;

/** The brief's first sentence, for naming the events it mentions on the day strip (R30). */
function firstSentence(text?: string): string | undefined {
    return text?.trim().split(/(?<=[.!?。])\s*/)[0] || undefined;
}

function isReplyDraftCard(card: MobileBriefCard): boolean {
    return [card.label ?? "", card.message].some((text) => /^(?:(?:yes|please)[,，]?\s+)?(?:draft|write|reply)\b|^(?:(?:是|好|好的)[，,、\s]*)?(?:起草|撰写|回复)/i.test(text.trim()));
}

function isReplyChip(card: MobileBriefCard): boolean {
    return /\b(?:draft|write)\s+(?:a |the )?reply\b|^reply\b|(?:起草|撰写).{0,12}回复|^回复/i.test((card.label ?? "").trim());
}

function isNewMessageChip(label: string): boolean {
    return /^(?:write|draft)\s+to\b|^(?:write|draft)\s+(?:a |an |the )?(?:message|email|note)\s+to\b|^给.{1,40}(?:写|发)(?:邮件|消息|信)/i.test(label.trim());
}

function prepMeetingRefs(card: MobileBriefCard, evidence: Map<string, MobileBriefRef>): { meetings: string[]; people: string[] } | null {
    if (!/^(?:prep(?:are)?\s+(?:me\s+)?for\b|会前准备|准备.*(?:会议|会面|1[:：]1))/i.test((card.label ?? "").trim())) return null;
    const refs = (card.refs ?? []).map((ref) => evidence.get(ref));
    const ids = (kind: string) => [...new Set(refs.filter((ref) => ref?.sourceKind === kind && ref.sourceId).map((ref) => ref!.sourceId))];
    return { meetings: ids("calendar"), people: ids("principal") };
}

/** The person id a Prep me chip needs Go to resolve: it has no single
 *  calendar reference, so only its one person id can name the meeting. */
function prepPersonID(card: MobileBriefCard, evidence: Map<string, MobileBriefRef>): string | null {
    const refs = prepMeetingRefs(card, evidence);
    return refs && refs.meetings.length !== 1 && refs.people.length === 1 ? refs.people[0] : null;
}

/** undefined: not a Prep me chip. null: withheld, because no record proves
 *  the one meeting. A name in the chip is never a lookup key. */
function prepMeetingID(card: MobileBriefCard, evidence: Map<string, MobileBriefRef>, byPerson: ReadonlyMap<string, string>): string | null | undefined {
    const refs = prepMeetingRefs(card, evidence);
    if (!refs) return undefined;
    if (refs.meetings.length === 1) return refs.meetings[0];
    const person = prepPersonID(card, evidence);
    return person ? byPerson.get(person) ?? null : null;
}

/** A draft chip to a person (a person id, no waiting-reply source) starts a
 *  new message. It is offered only once Go proves the address from a record.
 *  A draft about work with no person keeps its own action. */
function isNewMessageDraftCard(card: MobileBriefCard, evidence: Map<string, MobileBriefRef>): boolean {
    const refs = (card.refs ?? []).map((ref) => evidence.get(ref));
    return isReplyDraftCard(card) && !isReplyChip(card) && !isNewMessageChip(card.label ?? "") &&
        !refs.some((ref) => ref?.sourceKind === "brief_waiting_reply") && refs.some((ref) => ref?.sourceKind === "principal");
}

function useNewMessageRoutes(brief: MobileBriefDocument | null, enabled: boolean): ReadonlySet<string> {
    const candidates = useMemo(() => {
        if (!brief || !enabled) return [];
        const evidence = new Map((brief.evidence ?? []).map((ref) => [ref.ref, ref]));
        return (brief.cards ?? []).filter((card) => card.mode === "send" && card.message.trim() && isNewMessageDraftCard(card, evidence));
    }, [brief, enabled]);
    const key = `${brief?.briefId ?? ""}\n${candidates.map((card) => `${card.id}:${card.message}`).join("\n")}`;
    const [proven, setProven] = useState<ReadonlySet<string>>(new Set());
    useEffect(() => {
        let current = true;
        setProven(new Set());
        if (!brief || candidates.length === 0) return () => { current = false; };
        void Promise.all(candidates.map((card) => Promise.resolve()
            .then(() => GetTodayBriefDraftRoute({ ...todayBriefCardRef(brief.briefId, card.id, card.message), briefDraft: true }))
            .then((route) => route?.address ? card.id : null).catch(() => null))).then((ids) => {
            if (current) setProven(new Set(ids.filter((id): id is string => !!id)));
        });
        return () => { current = false; };
        // The key names the same brief and cards.
        // eslint-disable-next-line react-hooks/exhaustive-deps
    }, [key]);
    return proven;
}

/** Resolves person-only Prep me chips to the one meeting still to come today
 *  with that attendee. A failed or ambiguous lookup leaves the chip withheld. */
function usePrepPersonMeetings(brief: MobileBriefDocument | null, enabled: boolean): ReadonlyMap<string, string> {
    const people = useMemo(() => {
        if (!brief || !enabled) return [];
        const evidence = new Map((brief.evidence ?? []).map((ref) => [ref.ref, ref]));
        return [...new Set((brief.cards ?? []).map((card) => prepPersonID(card, evidence)).filter((id): id is string => !!id))].sort();
    }, [brief, enabled]);
    const key = people.join("\n");
    const [meetings, setMeetings] = useState<ReadonlyMap<string, string>>(new Map());
    useEffect(() => {
        let current = true;
        setMeetings(new Map());
        if (people.length === 0) return () => { current = false; };
        void Promise.all(people.map((person) => Promise.resolve().then(() => GetTodayPrepMeetingForPerson(person)).then((id) => [person, id] as const).catch(() => null))).then((found) => {
            if (!current) return;
            setMeetings(new Map(found.filter((entry): entry is readonly [string, string] => !!entry && !!entry[1])));
        });
        return () => { current = false; };
        // The joined key names the same people as the array.
        // eslint-disable-next-line react-hooks/exhaustive-deps
    }, [key]);
    return meetings;
}

/** The sections after "What needs your attention" and the next meetings, in the order
 *  Today lists them. What you looked at, the files you opened and the
 *  schedules that ran stay on Activity and Upcoming; Today links there with
 *  one line instead. What Isoform learned gets its own row. */
const LIST_SECTIONS: ReadonlyArray<keyof MobileBriefSections> = [
    "workingToward",
    "commitments",
    "waitingOnYou",
    "moved",
    "openLoops",
    "people",
    "deliverables",
];

/** The rows under "More from today" (RC 1), in their order. A list row opens
 *  its brief sections in place, as Today showed them before; the goal
 *  sections fold under "What you're working toward" and the work sections
 *  under "What you worked on". */
const WORKING_TOWARD_SECTIONS: ReadonlyArray<keyof MobileBriefSections> = ["workingToward", "commitments", "waitingOnYou"];
const WORKED_ON_SECTIONS: ReadonlyArray<keyof MobileBriefSections> = ["moved", "openLoops", "people", "deliverables"];

/** A row's detail names at most this many of its items. */
const MORE_DETAIL_TITLES = 3;

/** The brief sections Today no longer lists; when any has items, one line
 *  points at Activity. */
const OFF_PAGE_SECTIONS: ReadonlyArray<keyof MobileBriefSections> = ["watching", "doing", "schedules"];

/** Today shows the next meetings and at most this many rows per section. */
export const MAX_MEETINGS = 3;
export const MAX_SECTION_ITEMS = 3;

/** A list on Today: a brief section, or the open loops that need the user. */
type BriefListName = BriefSection | "needsYou";

function deadline(item: MobileBriefItem): Date | null {
    if (item.evidenceUnavailable || !item.dueAt) return null;
    const due = new Date(item.dueAt);
    return Number.isNaN(due.getTime()) ? null : due;
}

function deadlineStatus(item: MobileBriefItem, now: Date, timeZone?: string): "dueToday" | "overdue" | null {
    const due = deadline(item);
    if (!due) return null;
    let formatter: Intl.DateTimeFormat;
    const options = { year: "numeric", month: "2-digit", day: "2-digit" } as const;
    try {
        formatter = new Intl.DateTimeFormat("en-CA", { ...options, timeZone });
    } catch {
        // Older briefs may omit the Mac's zone; invalid zones also fall back
        // to this device's calendar, as the phone does.
        formatter = new Intl.DateTimeFormat("en-CA", options);
    }
    const day = (date: Date) => {
        const parts = formatter.formatToParts(date);
        return ["year", "month", "day"].map((type) => parts.find((part) => part.type === type)?.value).join("-");
    };
    const today = day(now);
    const dueDay = day(due);
    if (dueDay < today) return "overdue";
    return dueDay === today ? "dueToday" : null;
}

/** Due purposes, dated open loops and work waiting on a decision. A past
 *  hour today is still due today; only earlier calendar days are overdue.
 *  Briefs that say why an item needs attention (attentionReason) are listed
 *  by that instead; see attentionMode. */
export function needsYou(item: MobileBriefItem, now = new Date(), purpose = false, timeZone?: string): boolean {
    const due = deadline(item);
    if (due) {
        const isPurpose = item.kind !== "one_time_work"
            && (purpose || ["goal", "commitment", "responsibility", "concern", "opportunity"].includes(item.kind));
        if (!isPurpose || deadlineStatus(item, now, timeZone)) return true;
    }
    if (item.kind === "one_time_work") {
        return item.status === "blocked" || item.status === "needs_review";
    }
    return !due && item.kind === "commitment";
}

/** A brief that gives any item an attentionReason decides what needs the
 *  user's attention itself: only items with a reason are listed. */
export function attentionMode(brief: MobileBriefDocument): boolean {
    const all = [...Object.values(brief.sections ?? {}).flatMap((items) => (Array.isArray(items) ? items : [])), ...(brief.upcoming ?? [])];
    return all.some((item) => !!item?.attentionReason?.trim());
}

function sinceTime(item: MobileBriefItem): number {
    const since = item.since ? Date.parse(item.since) : NaN;
    return Number.isFinite(since) ? since : Infinity;
}

/** The calendar day of an instant in the brief's zone, as UTC midnight. */
function civilDay(date: Date, timeZone?: string): number {
    let parts: Intl.DateTimeFormatPart[];
    try {
        parts = new Intl.DateTimeFormat("en-CA", { year: "numeric", month: "2-digit", day: "2-digit", timeZone }).formatToParts(date);
    } catch {
        parts = new Intl.DateTimeFormat("en-CA", { year: "numeric", month: "2-digit", day: "2-digit" }).formatToParts(date);
    }
    const part = (type: string) => Number(parts.find((p) => p.type === type)?.value);
    return Date.UTC(part("year"), part("month") - 1, part("day"));
}

function zonedFormat(date: Date, language: string, options: Intl.DateTimeFormatOptions, timeZone?: string): string {
    try {
        return new Intl.DateTimeFormat(language, { ...options, timeZone }).format(date);
    } catch {
        return new Intl.DateTimeFormat(language, options).format(date);
    }
}

/** How long ago an item was first recorded, the way a person says it and as
 *  the brief does: "yesterday", "Thursday" this week, "last Thursday" in the
 *  previous one, then the date. Null for an item first seen today. */
function hasWrittenBrief(brief: MobileBriefDocument | null): boolean {
    return brief?.narrativeStatus === "ok" && briefHasWords(brief);
}

export function briefSinceDay(since: string | undefined, now: Date, language: string, tr: Translate, timeZone?: string): string | null {
    const at = since ? new Date(since) : null;
    if (!at || Number.isNaN(at.getTime())) return null;
    const day = 86_400_000;
    const days = Math.round((civilDay(at, timeZone) - civilDay(now, timeZone)) / day);
    if (days >= 0) return null;
    if (days === -1) return tr("todayBrief.dayWords.yesterday");
    if (days >= -6) {
        const weekday = zonedFormat(at, language, { weekday: language.startsWith("zh") ? "short" : "long" }, timeZone);
        // Monday-to-Sunday weeks, as the brief counts them.
        const todayIndex = (new Date(civilDay(now, timeZone)).getUTCDay() + 6) % 7;
        return tr(-days <= todayIndex ? "todayBrief.dayWords.weekday" : "todayBrief.dayWords.lastWeekday", { day: weekday });
    }
    const sameYear = zonedFormat(at, "en-CA", { year: "numeric" }, timeZone) === zonedFormat(now, "en-CA", { year: "numeric" }, timeZone);
    return zonedFormat(at, language, { month: "long", day: "numeric", ...(sameYear ? {} : { year: "numeric" as const }) }, timeZone);
}

/** Attention order: the brief's tier, then the oldest first. */
function attentionOrder(a: MobileBriefItem, b: MobileBriefItem): number {
    const rank = (item: MobileBriefItem) => (item.attentionRank && item.attentionRank > 0 ? item.attentionRank : Infinity);
    return (rank(a) - rank(b)) || (sinceTime(a) - sinceTime(b)) || 0;
}

function needsAttentionByReason(item: MobileBriefItem): boolean {
    return !!item.attentionReason?.trim() && !item.evidenceUnavailable;
}

/** Cards outside the goal sections and open loops that can still need the
 *  user, such as work left in a cart; listed under attention by reason only. */
const ATTENTION_OTHER_SECTIONS: ReadonlyArray<keyof MobileBriefSections> = ["moved", "deliverables", "people"];

/** The meetings still ahead, soonest first, without the day-shape line. */
/**
 * The brief's paragraphs in part order: done first, then next, so the closing
 * question stays last. The stored narrative is already in this order; the
 * counts in narrativeParts say where next starts. Briefs without parts keep
 * their narrative as it is.
 */
export function narrativeInPartOrder(brief: { narrative?: MobileBriefParagraph[] | null; narrativeParts?: { kind: string; count: number }[] | null }): MobileBriefParagraph[] {
    const narrative = brief.narrative ?? [];
    const parts = brief.narrativeParts ?? [];
    const doneCount = parts.find((part) => part.kind === "done")?.count ?? 0;
    if (parts.length === 0 || doneCount <= 0 || doneCount >= narrative.length) return narrative;
    return [...narrative.slice(0, doneCount), ...narrative.slice(doneCount)];
}

export function nextMeetings(items: MobileBriefItem[], now = new Date()): MobileBriefItem[] {
    return items
        .filter((item) => {
            if (item.kind === "day_shape" || item.status === "done") return false;
            // Older briefs have only a start time. Once that passes, do not
            // let a stale generation-time status occupy a next-meeting slot.
            const end = Date.parse(item.endAt || item.at);
            return !Number.isFinite(end) || end > now.getTime();
        })
        .map((item) => {
            const start = Date.parse(item.at);
            return Number.isFinite(start)
                ? { ...item, status: start <= now.getTime() ? "now" : "upcoming" }
                : item;
        })
        .sort((a, b) => (Date.parse(a.at) || 0) - (Date.parse(b.at) || 0))
        .slice(0, MAX_MEETINGS);
}

/** Settings > Observation holds the screen, accessibility and calendar switches. */
const PERMISSIONS_SETTINGS_TOPIC = "activity";

/** A web link Today may open: http or https only. */
export function webLink(raw: string | undefined): string | null {
    if (!raw) return null;
    try {
        const url = new URL(raw);
        return url.protocol === "http:" || url.protocol === "https:" ? url.toString() : null;
    } catch {
        return null;
    }
}

type ItemLink =
    | { kind: "join" | "url"; url: string }
    | { kind: "memory"; id: string }
    | { kind: "people" | "automations" | "activity" | "mainChat" };

/** Where an item leads: a meeting's join link, a find's page, or the page
 *  of the record behind it. */
export function briefItemLink(
    item: MobileBriefItem,
    section: BriefSection,
    evidence: Map<string, MobileBriefRef>,
): ItemLink | null {
    if (item.evidenceUnavailable) return null;
    if (item.chatMessageId || item.chatSessionId) return { kind: "mainChat" };
    const url = webLink(item.url);
    if (url) {
        return section === "meetings" ? (item.status === "done" ? null : { kind: "join", url }) : { kind: "url", url };
    }
    for (const refID of item.refs ?? []) {
        const ref = evidence.get(refID);
        if (!ref) continue;
        switch (ref.sourceKind) {
            case "memory":
            case "knowledge":
                return { kind: "memory", id: ref.sourceId };
            case "principal":
                return { kind: "people" };
            case "schedule":
            case "schedule_run":
                return { kind: "automations" };
            case "meeting":
            case "activity":
            case "activity_session":
            case "activity_run":
                return { kind: "activity" };
            case "main_chat_message":
            case "chat_message":
                return { kind: "mainChat" };
            case "web":
                if (ref.sourceId.startsWith("site:")) {
                    const site = webLink(`https://${ref.sourceId.slice(5)}`);
                    if (site) return { kind: "url", url: site };
                }
                break;
        }
    }
    return null;
}

function briefDate(iso: string | undefined, language: string, timeZone?: string): string | null {
    if (!iso) return null;
    const when = new Date(iso);
    if (Number.isNaN(when.getTime())) return null;
    const options = { month: "short", day: "numeric", year: "numeric" } as const;
    try {
        return when.toLocaleDateString(language, { ...options, timeZone });
    } catch {
        return when.toLocaleDateString(language, options);
    }
}

function isPurposeSection(section: BriefSection): boolean {
    return section === "workingToward" || section === "commitments" || section === "waitingOnYou";
}

function primaryPurposeID(item: MobileBriefItem, evidence: Map<string, MobileBriefRef>): string | undefined {
    if (item.purposeId) return item.purposeId;
    const primary = evidence.get(item.refs[0]);
    return primary?.sourceKind === "purpose" ? primary.sourceId : undefined;
}

/** The brief without the rows of goals the user just marked done or forgot. */
function withoutHiddenGoals(brief: MobileBriefDocument, hidden: ReadonlySet<string>): MobileBriefDocument {
    if (hidden.size === 0) return brief;
    const evidence = new Map((brief.evidence ?? []).map((ref) => [ref.ref, ref]));
    const sections = { ...brief.sections };
    for (const name of Object.keys(sections) as Array<keyof MobileBriefSections>) {
        const items = sections[name];
        if (!items) continue;
        sections[name] = items.filter((item) => {
            const id = primaryPurposeID(item, evidence);
            return !id || !hidden.has(id);
        });
    }
    return { ...brief, sections };
}

function purposeEvidence(item: MobileBriefItem, evidence: Map<string, MobileBriefRef>): MobileBriefRef[] {
    // Purpose refs identify this card or its next step; neither is an observation.
    return item.refs.map((ref) => evidence.get(ref)).filter((ref): ref is MobileBriefRef =>
        !!ref?.label && ref.sourceKind !== "purpose");
}

function evidenceText(ref: MobileBriefRef, language: string, tr: Translate): string {
    const sourceLabels: Record<string, string> = {
        activity: "activity",
        activity_session: "session",
        activity_run: "summary",
        activity_outcome: "outcome",
        engagement_moment: "reminder",
        chat_message: "mainChat",
        main_chat_message: "mainChat",
        purpose: "purpose",
    };
    const kind = tr(`todayBrief.sourceKinds.${sourceLabels[ref.sourceKind] ?? "source"}`);
    // Purpose and delivery refs can carry a fallback brief date. Only activity
    // records establish when the user's work was observed.
    const observedAt = ["activity", "activity_session", "activity_run", "activity_outcome"].includes(ref.sourceKind)
        ? briefDate(ref.observedAt, language) : null;
    return tr(observedAt ? "todayBrief.evidenceDated" : "todayBrief.evidenceSource", {
        kind, source: ref.label, date: observedAt ?? "",
    });
}

/** The brief's card and the cards of its states (RC 1 Today and States). */
const BRIEF_CARD = "flex flex-col rounded-xl border border-border bg-panel px-[26px] py-6 shadow-card";

/** Which "More from today" rows are open. Rows start folded and stay as the
 *  user left them while Today is open. */
function useOpenRows(): MoreRowsState {
    const [open, setOpen] = useState<ReadonlySet<string>>(() => new Set());
    const toggle = useCallback((key: string) => setOpen((current) => {
        const next = new Set(current);
        if (!next.delete(key)) next.add(key);
        return next;
    }), []);
    return useMemo(() => ({ open, toggle }), [open, toggle]);
}

interface MoreRowsState {
    open: ReadonlySet<string>;
    toggle: (key: string) => void;
}

/**
 * Today's main content (RC 1): the brief the Mac built (the same document the
 * phone reads) in its card, or when the next one comes and a way to make one
 * now, or, when it is empty because Isoform cannot see the day, what is
 * missing with the ways to turn it on. After the card come Before you talk
 * when eligible, Your topics and the goal check-in. Nothing here asks macOS for a permission: the
 * buttons open Settings or the main chat's setup, where each ask happens on a
 * tap.
 */
export function TodayBriefSection({
    view,
    loading,
    loadFailed = false,
    onRetry,
    make,
    nextAllowedAt,
    onMakeNow,
    onOpenBrief,
    locateNonce = 0,
    handlers,
    now: providedNow,
    observationPaused = false,
    active = true,
    goalCheckIn = null,
    onGoalCheckInAnswered,
    beforeTopics,
    afterTopics,
    firstBrief,
    topics,
    dayShape = null,
    dayShapeDate = "",
    dayShapeFailure = null,
    quietFooter,
}: {
    view: TodayBriefView | null;
    dayShape?: TodayDayShape | null;
    dayShapeDate?: string;
    dayShapeFailure?: { lastUpdatedAt: number | null; retry: () => void } | null;
    quietFooter?: ReactNode;
    loading: boolean;
    /** Observation is paused (Closed Eyes), or null while that is unknown:
     *  Today then prepares no chat context and reads no sources. */
    observationPaused?: boolean | null;
    /** Retained Today is visible; only then can a check-in count as shown. */
    active?: boolean;
    /** The goal Today asks "Still working on ...?" about, if any. */
    goalCheckIn?: GoalCheckInView | null;
    onGoalCheckInAnswered?: () => void;
    /** The brief could not be read; onRetry reads it again. */
    loadFailed?: boolean;
    onRetry?: () => void;
    make: MakeBriefState;
    nextAllowedAt: string;
    onMakeNow: () => void;
    /** Opens a stored brief in place of today's view; no id is today again. */
    onOpenBrief?: (briefId?: string, title?: string) => void;
    /** A new value rings the brief's card once (a missed brief's Show). */
    locateNonce?: number;
    handlers: TodayActionHandlers;
    now?: Date;
    /** The next known-person meeting, immediately before Your topics. */
    beforeTopics?: ReactNode;
    /** One-time cards follow the current topics. */
    afterTopics?: ReactNode;
    /** Day 1's first brief: its title and source ("Your first brief · 2:05 PM ·
     *  from today's calendar and your Claude Code history") and the chips
     *  made from the same facts, which fill the box unsent. */
    firstBrief?: { caption?: string; chips: string[]; strip?: { title: string; source: string } } | null;
    /** "Your topics", between the goal check-in and the suggestion. */
    topics?: ReactNode;
}) {
    const { t, i18n } = useTranslation("today");
    const beliefReview = useTodayWeekBelief(active);
    const tr: Translate = (key, options) => t(key as never, options as never) as unknown as string;
    // Only a brief with content is observed; its card holds the ref.
    const seenRef = useFirstBriefSeen(!!view?.brief && !view.empty);
    const sectionBrief = view?.brief;
    const briefSeenRef = useTodaySectionSeen(active, "brief", view?.quiet || sectionBrief?.yourWeek ? 0 : sectionBrief ? (briefForDisplay(sectionBrief).narrative?.length ?? 0) : 0, !loading);
    const rows = useOpenRows();
    // While observation is paused every chat action opens with its card's own
    // words and no observed context, and nothing sends itself.
    const chatHandlers = useMemo<TodayActionHandlers>(() => observationPaused !== true ? handlers : {
        ...handlers,
        onStartChat: (prompt, context, options) => handlers.onStartChat(prompt, context, { ...options, paused: true }),
    }, [handlers, observationPaused]);
    const [, refreshTime] = useReducer((tick: number) => tick + 1, 0);
    const now = providedNow ?? new Date();
    useEffect(() => {
        if (providedNow) return;
        // Re-evaluate cached meeting times and deadlines while Today stays
        // open and after the Mac wakes, without regenerating the brief.
        // The interval stops while the window is hidden; focus or showing it
        // again re-evaluates at once.
        return activeInterval(refreshTime, 60_000);
    }, [providedNow]);

    const { t: tActivityRaw } = useTranslation("activity");
    const tActivity: Translate = (key, options) => tActivityRaw(key as never, options as never) as unknown as string;
    // Goals marked done or forgotten here leave at once; the next read of the
    // brief leaves them out on the computer's side, and Undo brings them back.
    const [hiddenGoals, setHiddenGoals] = useState<ReadonlySet<string>>(() => new Set());
    const onGoalHidden = useCallback((goalId: string, hidden: boolean) => {
        setHiddenGoals((current) => {
            if (current.has(goalId) === hidden) return current;
            const next = new Set(current);
            if (hidden) next.add(goalId);
            else next.delete(goalId);
            return next;
        });
    }, []);
    const [opening, setOpening] = useState(false);
    const [retryOpening, setRetryOpening] = useState<(() => void) | null>(null);
    const openingRequest = useRef(0);
    const wasActive = useRef(active);
    useEffect(() => {
        // Today is retained while Chat is shown. Returning to Today supersedes
        // the earlier opening, even if its Wails promise has not settled.
        if (active && !wasActive.current) {
            openingRequest.current += 1;
            setOpening(false);
        }
        wasActive.current = active;
    }, [active]);
    // An answer was clicked: its words are in the box on the right. A new
    // brief starts over.
    const [holdPreview, setHoldPreview] = useState<HoldPreview>("proposed");
    const [stripRemoval, setStripRemoval] = useState<{ id: string; eventId: string } | null>(null);
    const [stripHidden, setStripHidden] = useState<ReadonlySet<string>>(() => new Set());
    const [stripError, setStripError] = useState<"remove" | "restore" | "access" | "edited" | null>(null);
    useEffect(() => {
        if (!stripRemoval) return;
        const timer = window.setTimeout(() => setStripRemoval(null), 5000);
        return () => window.clearTimeout(timer);
    }, [stripRemoval]);
    useEffect(() => {
        if (!dayShape) return;
        setStripHidden((current) => {
            const remaining = new Set([...current].filter((id) => dayShape.events.some((event) => event.id === id)));
            return remaining.size === current.size ? current : remaining;
        });
    }, [dayShape]);
    const removeStripHold = async (id?: string) => {
        const event = dayShape?.events.find((entry) => entry.holdId === id);
        if (!id || !event) return;
        setStripError(null);
        try {
            await measureToday("T31", "reach", "done", () => RemoveHold(id));
            setStripHidden((current) => new Set([...current, event.id]));
            setStripRemoval({ id, eventId: event.id });
            pageQueryCache.invalidate(["GetTodayDayShape"], false);
        } catch (failure) {
            if (String(failure).includes("HOLD_CALENDAR_EDITED")) { setStripError("edited"); return; }
            try { setStripError((await GetHoldSettings()).granted ? "remove" : "access"); }
            catch { setStripError("remove"); }
        }
    };
    const undoStripRemoval = async () => {
        if (!stripRemoval) return;
        try {
            await measureToday("T31", "reach", "undone", () => UndoRemoveHold(stripRemoval.id));
            setStripHidden((current) => { const next = new Set(current); next.delete(stripRemoval.eventId); return next; });
            setStripRemoval(null); setStripError(null);
            pageQueryCache.invalidate(["GetTodayDayShape"], false);
        } catch { setStripError("restore"); }
    };
    // "Turn off Your week" from its menu: the brief reads as on other evenings.
    const [weekOff, setWeekOff] = useState(false);
    const [closingInteraction, setClosingInteraction] = useState("");

    const brief = view?.brief ?? null;
    const prepPeople = usePrepPersonMeetings(brief, active && observationPaused === false && !!handlers.onOpenDayMeeting);
    const draftRoutes = useNewMessageRoutes(brief, active && observationPaused === false && !!handlers.onDraftBriefCard);
    const closingInteractionKey = brief ? `${brief.briefId}\n${brief.narrative?.at(-1)?.text ?? ""}` : "";
    const briefHold = useBriefHoldOffer(brief, active && observationPaused === false && !!handlers.onFindTime && !!dayShape?.calendarConnected && !view?.quiet && !view?.closingAnswer && closingInteraction !== closingInteractionKey && (!brief?.yourWeek || weekOff), dayShapeDate, brief?.timezone);
    const directHold = useDirectHold(briefHold.offer);
    const briefId = brief?.briefId ?? "";
    const holdKey = `${briefId}|${dayShape?.hold?.start ?? ""}|${dayShape?.hold?.end ?? ""}|${dayShape?.hold?.label ?? ""}`;
    // A live reread can withdraw an expired/conflicting hold. Clear its local
    // acceptance and held helper along with the strip, without touching the draft.
    useEffect(() => { setHoldPreview("proposed"); }, [holdKey]);
    const building = make === "making" || !!view?.building;
    // Today's slot whose time has passed with no brief yet (Go
    // MobileBriefHealth.PendingSlot): it is on its way, whatever is below.
    const pending = view?.enabled && (view.pendingSlot === "morning" || view.pendingSlot === "evening") ? view.pendingSlot : null;
    // A brief made before the pending slot's time is from an earlier slot.
    const pendingTime = Date.parse(view?.pendingAt ?? "");
    const earlierBrief = !brief || !Number.isFinite(pendingTime) || !(Date.parse(brief.asOf) >= pendingTime);
    // A brief shows in full on Today only on the day it was made; from
    // midnight it is the last brief (today-review v1.1.23 R52).
    const madeAt = brief ? new Date(brief.asOf) : null;
    const fromEarlierDay = !!madeAt && !Number.isNaN(madeAt.getTime())
        && new Date(madeAt.getFullYear(), madeAt.getMonth(), madeAt.getDate()).getTime() < new Date(now.getFullYear(), now.getMonth(), now.getDate()).getTime();
    const waiting = hasWrittenBrief(brief) && !view?.firstBrief ? null : pending ?? (fromEarlierDay ? (now.getHours() < 12 ? "morning" : "evening") : null);
    const missing = view?.missing ?? [];
    const hasContent = !!view && brief !== null && !view.empty;
    const summary = view?.dailySummary;
    const summaryRow = summary?.text ? (
        <MoreRow key="summary" rowKey="summary" rows={rows} label={tr("todayBrief.dailySummary")} detail={summaryDetail(summary, i18n.language, tr)} testId="today-more-summary">
            <LocalDailySummary summary={summary} handlers={chatHandlers} tr={tr} now={now} labelHidden />
        </MoreRow>
    ) : null;

    // One tree in every state: only the card changes, so Your topics, the
    // suggestion and the rows keep what the user was doing in them.
    let parts: BriefParts;
    if (loading && !view) {
        parts = {
            card: (
                <div className={cn(BRIEF_CARD, "gap-2.5")} aria-busy="true" data-testid="brief-card-loading">
                    <span className="h-2.5 w-2/5 rounded-md bg-surface-active" />
                    <span className="h-3 w-full rounded-md bg-surface-active" />
                    <span className="h-3 w-[92%] rounded-md bg-surface-active" />
                    <span className="h-3 w-[70%] rounded-md bg-surface-active" />
                </div>
            ),
            checkIn: null,
            rows: [],
            footer: null,
        };
    } else if (!view && loadFailed) {
        parts = {
            card: (
                <div className={cn(BRIEF_CARD, "gap-2.5")}>
                    {!brief && !view?.lastBriefId && <p className="text-[14px] font-medium text-ink">{tr("todayBrief.firstTitle")}</p>}
                    <p role="alert" data-testid="brief-load-failed" className="text-[13.5px] text-copper">{tr("todayBrief.loadFailed")}{onRetry && <> <button type="button" className="text-accent" onClick={() => measureToday("T03", "do", "done", onRetry)}>{tr("todayBrief.retry")}</button></>}</p>
                </div>
            ),
            checkIn: null,
            rows: [],
            footer: null,
        };
    } else if ((hasWrittenBrief(brief) || (!(pending && earlierBrief) && !fromEarlierDay)) && (hasContent || view?.quiet) && brief) {
        const displayed = briefForDisplay(brief);
        parts = briefParts({
            firstBrief,
            brief: view?.quiet ? { ...displayed, narrative: [], weeklyRecap: [], yourWeek: undefined, cards: [], offers: [], audio: undefined } : displayed,
            briefRun: view?.briefRun,
            weekAnswer: view?.weekAnswer,
            closingAnswer: view?.closingAnswer,
            onClosingInteract: () => setClosingInteraction(closingInteractionKey),
            dayShape,
            dayShapeDate,
            dayShapeFailure,
            holdPreview,
            briefHold,
            directHold,
            stripHidden,
            stripRemoval,
            stripError,
            removeStripHold,
            undoStripRemoval,
            setHoldPreview,
            handlers: chatHandlers,
            tr,
            tActivity,
            language: i18n.language,
            now,
            cardsShown: observationPaused !== null,
            paused: observationPaused === true,
            cardRef: seenRef,
            active,
            goalCheckIn,
            onGoalCheckInAnswered,
            hiddenGoals,
            onGoalHidden,
            opening,
            openingRequest,
            setOpening,
            onCardGone: (retry) => setRetryOpening(() => retry),
            rows,
            summaryRow,
            beliefReview,
            prepPeople,
            draftRoutes,
            blind: missing.length > 0 && !pending ? <Blind missing={missing} handlers={handlers} tr={tr} onChanged={onRetry} /> : null,
        });
        if (view?.quiet) {
            const since = briefSinceDay(view.quietSince, now, i18n.language, tr, brief?.timezone);
            const yesterday = civilDay(now, brief?.timezone) - civilDay(new Date(view.quietSince ?? now), brief?.timezone) === 86_400_000;
            const lead = tr(!view.quietSince ? "todayBrief.quiet.window" : yesterday ? "todayBrief.quiet.yesterday" : since ? "todayBrief.quiet.dated" : "todayBrief.quiet.last", { day: since });
            const next = briefClockTime(view.nextAt, i18n.language, tr, brief?.timezone);
            parts.card = <div className={cn(BRIEF_CARD, "gap-2.5")} data-testid="brief-quiet">
                <p className="text-[14px] text-muted">{tr(view.quietScreens == null ? "todayBrief.quiet.withoutCount" : "todayBrief.quiet.withCount", { lead, count: view.quietScreens ?? 0 })}{next ? tr("todayBrief.quiet.next", { time: next }) : ""}</p>
                {missing.length > 0 && !pending && <Blind missing={missing} handlers={handlers} tr={tr} onChanged={onRetry} />}
                <SensitiveExcluded count={brief.limits?.excludedSensitiveItems ?? 0} tr={tr} />
            </div>;
        }
    } else if (waiting) {
        // This slot's brief is missing: the pending card and today's strip
        // are all there is. An earlier slot's brief is never shown as the
        // brief, and its question can't be answered (today-review v1.1.5 #1).
        parts = { card: null, checkIn: null, rows: [summaryRow], footer: null };
    } else {
        parts = {
            card: (
                <div className={cn(BRIEF_CARD, "gap-2.5")}>
                    {building && (
                        <>
                            <p className="flex items-center gap-2 text-[13.5px] text-muted" role="status">
                                <Loader2 className="size-3.5 animate-spin" />
                                {tr(brief === null ? "todayBrief.makingFirst" : "todayBrief.making")}
                            </p>
                            <div aria-hidden className="flex flex-col gap-2 opacity-60">
                                <span className="h-3 w-full rounded-md bg-surface-active" />
                                <span className="h-3 w-4/5 rounded-md bg-surface-active" />
                            </div>
                        </>
                    )}
                    {!building && <NoBrief view={view} hasEmptyBrief={brief !== null} now={now} tr={tr} />}
                    {!building && (
                        <div className="mt-1.5">
                            <MakeNow make={make} building={building} nextAllowedAt={nextAllowedAt} onMakeNow={onMakeNow} now={now} tr={tr} />
                        </div>
                    )}
                    {missing.length > 0 && <Blind missing={missing} handlers={handlers} tr={tr} onChanged={onRetry} inline />}
                    {!building && <SensitiveExcluded count={brief?.limits?.excludedSensitiveItems ?? 0} tr={tr} />}
                </div>
            ),
            checkIn: null,
            rows: [summaryRow],
            footer: null,
        };
    }

    const pendingCard = waiting && !(loading && !view) && !(!view && loadFailed) ? (
        <PendingBrief
            slot={waiting}
            building={building}
            retryAt={view?.retryAt ?? ""}
            attempts={view?.pendingAttempts ?? 0}
            cause={view?.pendingCause}
            assistant={view?.pendingAssistant}
            lastBriefId={view?.firstBrief ? "" : view?.lastBriefId || (brief?.briefId ?? "")}
            lastSlot={view?.lastBriefId ? view.lastSlot ?? "" : brief?.displaySlot ?? brief?.slot ?? ""}
            lastAt={view?.lastBriefId ? view.lastAt ?? "" : brief?.asOf ?? ""}
            strip={dayShapeFailure ? <CalendarReadFailed retry={dayShapeFailure.retry} tr={tr} /> : <TodayDayStrip requestedDate={dayShapeDate ?? ""} shape={dayShape} onOpenMeeting={handlers.onOpenDayMeeting} />}
            make={make}
            nextAllowedAt={nextAllowedAt}
            onMakeNow={onMakeNow}
            onOpenBrief={onOpenBrief}
            now={now}
            tr={tr}
            blind={missing.length > 0 ? <Blind missing={missing} handlers={handlers} tr={tr} onChanged={onRetry} inline /> : null}
        />
    ) : null;
    const hasQuietFooter = !!quietFooter;
    return (
        <div data-testid="today-brief" className="flex flex-col gap-[18px]">
            {locateNonce > 0 ? (
                // Keyed by the request, so each Show rings the card again.
                <div key={locateNonce} className="activity-card-locate flex flex-col gap-[18px]" data-testid="today-brief-located">
                    {(pendingCard || parts.card) && <div ref={briefSeenRef}>{pendingCard}{parts.card}</div>}
                </div>
            ) : (pendingCard || parts.card) && <div ref={briefSeenRef}>{pendingCard}{parts.card}</div>}
            {view?.nothingNew && brief && <p className="text-[13.5px] text-muted" role="status">{tr("todayBrief.nothingNewSince", { time: briefClockTime(view.briefRun?.status === "ok" && view.briefRun.briefId === brief.briefId && view.briefRun.at ? view.briefRun.at : brief.asOf, i18n.language, tr, brief.timezone) })}</p>}
            {beliefReview.receipt && (!hasContent || view?.quiet || !brief?.yourWeek?.learned?.some((row) => row.memoryId === beliefReview.receipt?.id)) && <TodayWeekBeliefReceipt review={beliefReview} />}
            {retryOpening && <button type="button" role="alert" className="self-start text-[12px] text-error" onClick={() => measureToday("T11", "open", "opened", () => { setRetryOpening(null); retryOpening(); })}>{tr("actions.chatOpenFailed")}</button>}
            {hasContent && view && !view.enabled ? (
                <div className="-mt-5 flex flex-wrap items-center gap-3" data-testid="today-brief-schedule-off">
                    <span className="text-[12px] text-muted">{tr("todayBrief.scheduleOffShort")}</span>
                    {missing.length === 0 && <MakeNow make={make} building={building} nextAllowedAt={nextAllowedAt} onMakeNow={onMakeNow} now={now} tr={tr} />}
                </div>
            ) : null}
            {beforeTopics}
            {topics}
            {afterTopics}
            {parts.checkIn}
            {hasQuietFooter && <footer data-testid="today-quiet-footer" className="flex flex-col gap-1.5 text-[12px] leading-snug text-muted">
                {quietFooter}
            </footer>}
        </div>
    );
}

/** The calendar couldn't be read: the line stands in for the strip, never
 *  under an empty bar (today-review v1.1.5 #3). */
function CalendarReadFailed({ retry, tr }: { retry: () => void; tr: Translate }) {
    return (
        <p role="alert" className="text-[13px] text-muted" data-testid="day-strip-read-failed">
            {tr("dayStrip.readFailed")}{" "}
            <button type="button" className="text-accent" onClick={() => measureToday("T32", "do", "done", retry)}>{tr("dayStrip.retry")}</button>
        </p>
    );
}

/** Today's scheduled brief has not arrived (today-review v1.1.5 #1, #2): what
 *  the run record proves about its tries and the next one, today's strip,
 *  the one Make my brief now, and the last brief, which opens in the panel
 *  with its day. */
function lastBriefDay(at: Date, now: Date, language: string, tr: Translate): string {
    const day = (date: Date) => new Date(date.getFullYear(), date.getMonth(), date.getDate()).getTime();
    const ago = Math.round((day(now) - day(at)) / 86_400_000);
    if (ago === 0) return tr("todayBrief.pending.dayToday");
    if (ago === 1) return tr("todayBrief.pending.dayYesterday");
    return at.toLocaleDateString(language, { weekday: "long" });
}

function PendingBrief({ slot, building, retryAt, attempts, cause, assistant, lastBriefId, lastSlot, lastAt, strip, make, nextAllowedAt, onMakeNow, onOpenBrief, now, tr, blind }: {
    blind?: ReactNode;
    slot: "morning" | "evening";
    building: boolean;
    retryAt: string;
    attempts: number;
    /** Why the last try failed, when Isoform knows (e.g. "timeout"). */
    cause?: string;
    assistant?: string;
    lastBriefId: string;
    lastSlot: string;
    lastAt: string;
    strip: ReactNode;
    make: MakeBriefState;
    nextAllowedAt: string;
    onMakeNow: () => void;
    onOpenBrief?: (briefId?: string, title?: string) => void;
    now: Date;
    tr: Translate;
}) {
    const { i18n } = useTranslation("today");
    const language = i18n.language;
    const signedOut = cause === "signed_out" && !!assistant && attempts > 0;
    const retryDate = !building && retryAt ? new Date(retryAt) : null;
    const retry = retryDate && !Number.isNaN(retryDate.getTime()) && retryDate.getTime() > now.getTime() ? clockTime(retryDate, language, tr) : null;
    const lastDate = lastAt ? new Date(lastAt) : null;
    const last = lastBriefId && lastDate && !Number.isNaN(lastDate.getTime()) ? {
        brief: tr(`todayBrief.slot.${lastSlot === "morning" || lastSlot === "evening" ? lastSlot : "on_request"}`),
        // Day words as everywhere else: today, yesterday, then the weekday (today-review v1.1.13 D6).
        day: lastBriefDay(lastDate, now, language, tr),
        time: clockTime(lastDate, language, tr),
    } : null;
    return (
        <div className={cn(BRIEF_CARD, "gap-2.5")} data-testid="brief-pending">
            {!lastBriefId && <p className="text-[14px] font-medium text-ink">{tr("todayBrief.firstTitle")}</p>}
            {building ? (
                <p className="flex items-center gap-2 text-[13.5px] text-muted" role="status">
                    <Loader2 className="size-3.5 animate-spin" />
                    {tr(slot === "evening" ? "todayBrief.pending.makingEvening" : "todayBrief.pending.makingMorning")}
                </p>
            ) : signedOut ? (
                <p className="text-[14px] text-copper" role="status" data-testid="brief-pending-line">
                    {tr("todayBrief.pending.signedOut", { count: attempts, ai: assistantLabel(assistant, { claudeCode: tr("status.assistantClaudeCode"), codex: tr("status.assistantCodex"), yourAI: tr("todayBrief.pending.yourAI") }) })}{" "}<BriefSignIn assistant={assistant!} size="14px" />
                </p>
            ) : retry ? (
                <p className="text-[14px] text-ink" role="status" data-testid="brief-pending-line">
                    {attempts > 0 ? tr(cause === "timeout" ? "todayBrief.pending.triedTimeout" : cause === "signed_out" ? "todayBrief.pending.triedSignedOut" : "todayBrief.pending.tried", { count: attempts, time: retry, ai: assistantLabel(assistant, { claudeCode: tr("status.assistantClaudeCode"), codex: tr("status.assistantCodex"), yourAI: tr("todayBrief.pending.yourAI") }) }) : tr("todayBrief.pending.retry", { time: retry })}
                </p>
            ) : null}
            {strip}
            {!building && !signedOut && <div className="mt-1.5"><MakeNow make={make} building={building} nextAllowedAt={nextAllowedAt} onMakeNow={onMakeNow} now={now} tr={tr} /></div>}
            {onOpenBrief && last && (
                <p className="text-[12.5px] text-muted" data-testid="brief-pending-last">
                    {tr("todayBrief.pending.lastBrief", last)} · <Button variant="link" size="sm" className="h-auto p-0 text-[12.5px] font-medium text-accent hover:no-underline" onClick={() => measureToday("T05", "open", "opened", () => onOpenBrief(lastBriefId, tr("todayBrief.pending.lastBriefTitle", last)))}>{tr("todayBrief.pending.readIt")}</Button>
                </p>
            )}
            {blind}
        </div>
    );
}

/** "Make my brief now", or when the next one can be made, and why the last
 *  try failed. A brief made on request starts a short cooldown, during which
 *  the line replaces the button (RC1-States). */
function MakeNow({ make, building, nextAllowedAt, onMakeNow, now, tr }: {
    make: MakeBriefState;
    building: boolean;
    nextAllowedAt: string;
    onMakeNow: () => void;
    now: Date;
    tr: Translate;
}) {
    if (make === "cooldown") {
        return (
            <span className="text-[12.5px] text-muted" data-testid="brief-cooldown">
                {tr("todayBrief.cooldown", { when: describeBriefTime(nextAllowedAt, now, tr) ?? "" })}
            </span>
        );
    }
    return (
        <span className="flex flex-wrap items-center gap-3">
            <Button size="sm" onClick={() => measureToday("T04", "do", "done", onMakeNow)} disabled={building}>
                {tr("todayBrief.makeNow")}
            </Button>
            {make === "failed" && (
                <span role="alert" className="text-[12px] text-muted">
                    {tr("todayBrief.failed")}
                </span>
            )}
        </span>
    );
}

function SensitiveExcluded({ count, tr }: { count: number; tr: Translate }) {
    if (count <= 0) return null;
    return (
        <p className="text-[12px] text-muted" data-testid="brief-sensitive-excluded">
            {tr("todayBrief.sensitiveExcluded", { count })}
        </p>
    );
}

function NoBrief({
    view,
    hasEmptyBrief,
    now,
    tr,
}: {
    view: TodayBriefView | null;
    hasEmptyBrief: boolean;
    now: Date;
    tr: Translate;
}) {
    if (hasEmptyBrief && (view?.missing.length ?? 0) === 0) {
        return <p className="text-[14px] text-ink">{tr("todayBrief.empty")}</p>;
    }
    if (!view?.enabled) {
        return <p className="text-[14px] text-ink">{tr("todayBrief.scheduleOff")}</p>;
    }
    const when = describeBriefTime(view.nextAt, now, tr);
    if (!when) return <p className="text-[14px] text-ink">{tr("todayBrief.noneYet")}</p>;
    const key = view.nextSlot === "evening" ? "todayBrief.nextEvening" : "todayBrief.nextMorning";
    return <p className="text-[14px] text-ink">{tr(key, { when })}</p>;
}

/** Isoform cannot see the day: this replaces the card's words, never the
 *  page (RC1-States). */
function Blind({
    missing,
    handlers,
    tr,
    onChanged,
    inline = false,
}: {
    missing: string[];
    handlers: TodayActionHandlers;
    tr: Translate;
    onChanged?: () => void;
    inline?: boolean;
}) {
    const permission = missing.includes("screen_recording") ? "screen_recording" : missing.includes("calendars") ? "calendars" : null;
    return (
        <div className="flex flex-col gap-2.5" data-testid="today-brief-blind">
            {permission ? <TodayPermissionControl permission={permission} action="T06" explain inline={inline} bothMissing={missing.includes("screen_recording") && missing.includes("calendars")} onChanged={onChanged} /> : <>
                <p className="text-[14px] font-semibold text-ink">{tr("todayBrief.blindTitle")}</p>
                <p className="text-[13px] leading-relaxed text-muted">{tr("todayBrief.blindBody")}</p>
                {handlers.onOpenSettings && <Button size="sm" variant="secondary" onClick={() => measureToday("T06", "open", "opened", () => handlers.onOpenSettings?.(PERMISSIONS_SETTINGS_TOPIC))}>{tr("todayBrief.openSettings")}</Button>}
            </>}
        </div>
    );
}

function summaryDate(summary: TodayBriefView["dailySummary"], now: Date): string {
    const at = new Date(summary?.at ?? "");
    const day = Number.isNaN(at.getTime()) ? now : at;
    return `${day.getFullYear()}-${String(day.getMonth() + 1).padStart(2, "0")}-${String(day.getDate()).padStart(2, "0")}`;
}

function summaryChatContext(date: string): TodayChatContextRef {
    return { sourceKind: "summary", sourceId: date };
}

/** "Written at 1:51pm": when the day's summary was made. */
function summaryDetail(summary: TodayBriefView["dailySummary"], language: string, tr: Translate): string | null {
    const time = briefClockTime(summary?.at, language, tr);
    return time ? tr("todayBrief.more.summaryAt", { time }) : null;
}

function LocalDailySummary({ summary, handlers, tr, now, labelHidden = false }: {
    summary: TodayBriefView["dailySummary"];
    handlers: TodayActionHandlers;
    tr: Translate;
    now: Date;
    /** Its row already names it; the label stays for screen readers. */
    labelHidden?: boolean;
}) {
    if (!summary?.text) return null;
    const date = summaryDate(summary, now);
    return (
        <section data-testid="brief-daily-summary" className="rounded-2xl border border-border bg-background p-5">
            <div className={labelHidden ? "sr-only" : "text-[11px] font-semibold uppercase tracking-wider text-muted/70"}>{tr("todayBrief.dailySummary")}</div>
            {/* The day summary is written in Markdown, as the Activity page
                shows it: its headings and lists render, never their markers. */}
            <DigestMarkdown text={summary.text} variant="compact" className={labelHidden ? undefined : "mt-2"} />
            <div className="mt-3 flex gap-3 text-[12px]">
                <Button size="sm" variant="ghost" onClick={() => handlers.onStartChat(tr("prompts.summary", { date }), summaryChatContext(date))}>{tr("actions.askAbout")}</Button>
                <Button size="sm" variant="ghost" aria-label={tr("actions.openActivityForSummary")} onClick={handlers.onOpenActivity}>{tr("actions.openActivity")}</Button>
            </div>
        </section>
    );
}

/** "More from today" (RC 1): one row per part of the day, each opening its
 *  section in place or leading to its page. Hidden when every row is. */
function MoreFromToday({ tr, children, footer, showEmpty }: { tr: Translate; children: ReactNode[]; footer?: ReactNode; showEmpty?: boolean }) {
    const headingId = useId();
    if (!children.some(Boolean) && !footer && !showEmpty) return null;
    return (
        <section aria-labelledby={headingId} data-testid="today-more" className="flex flex-col">
            <h2 id={headingId} className="mb-1.5 text-[13px] font-semibold text-muted">{tr("todayBrief.more.title")}</h2>
            {children}
            {footer && <div className="mt-3 flex flex-col items-start gap-1.5">{footer}</div>}
        </section>
    );
}

/** A row's button: the section's name, a one-line detail and a chevron that
 *  turns when the row is open. */
function MoreRowButton({ id, label, detail, expanded, controls, onClick, testId }: {
    id: string;
    label: string;
    detail?: string | null;
    /** Undefined for a row that leads to another page. */
    expanded?: boolean;
    controls?: string;
    onClick: () => void;
    testId?: string;
}) {
    return (
        <button
            type="button"
            data-testid={testId}
            aria-expanded={expanded}
            aria-controls={expanded === undefined ? undefined : controls}
            aria-labelledby={`${id}-label`}
            aria-describedby={detail ? `${id}-detail` : undefined}
            onClick={onClick}
            className="flex w-full items-center gap-3 rounded-lg px-2 py-2.5 text-left text-ink transition-colors hover:bg-surface-hover focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-border-focus"
        >
            <span id={`${id}-label`} className="w-[190px] shrink-0 text-[13px] font-medium">{label}</span>
            <span id={`${id}-detail`} className="min-w-0 flex-1 truncate text-[12.5px] text-muted">{detail}</span>
            <ChevronRight aria-hidden className={cn("size-3.5 shrink-0 text-muted transition-transform duration-150", expanded && "rotate-90")} />
        </button>
    );
}

/** One "More from today" row that opens its section in place. list is the
 *  row's own brief list (BriefList with this row as its header); children
 *  are the rest of the section, shown under it while the row is open. */
function MoreRow({ rowKey, rows, label, detail, testId, list, children, action }: {
    rowKey: string;
    rows: MoreRowsState;
    label: string;
    detail: string | null;
    /** The wrapper's test id when list is absent. */
    testId: string;
    list?: (header: ReactNode, listHidden: boolean, listId: string) => ReactNode;
    children?: ReactNode;
    action?: ReactNode;
}) {
    const id = `today-more-${rowKey}`;
    const open = rows.open.has(rowKey);
    const header = (
        <MoreRowButton
            id={id}
            label={label}
            detail={detail}
            expanded={open}
            controls={[list ? `${id}-list` : "", children ? `${id}-panel` : ""].filter(Boolean).join(" ")}
            onClick={() => rows.toggle(rowKey)}
        />
    );
    return (
        <div className="border-b border-border-divider" data-more-row={rowKey}>
            {list ? list(header, !open, `${id}-list`) : <div data-testid={testId}>{header}</div>}
            {action}
            {children && (
                <div id={`${id}-panel`} hidden={!open} className="flex flex-col gap-4 pb-4 pt-1">
                    {children}
                </div>
            )}
        </div>
    );
}

/** The brief's own parts of Today: its card, the goal check-in, and the
 *  rows under "More from today" with the line to Activity. A plain function,
 *  so Today keeps one tree whatever state the brief is in and its other
 *  parts (topics, the suggestion, news) never remount. */
interface BriefParts {
    card: ReactNode;
    checkIn: ReactNode;
    rows: ReactNode[];
    footer: ReactNode;
}

function briefParts({
    firstBrief,
    brief: storedBrief,
    briefRun,
    weekAnswer,
    closingAnswer,
    onClosingInteract,
    dayShape,
    dayShapeDate,
    dayShapeFailure,
    holdPreview,
    briefHold,
    directHold,
    stripHidden,
    stripRemoval,
    stripError,
    removeStripHold,
    undoStripRemoval,
    setHoldPreview,
    handlers,
    tr,
    tActivity,
    language,
    now,
    cardsShown,
    paused,
    cardRef,
    onCardGone,
    active = true,
    goalCheckIn,
    onGoalCheckInAnswered,
    hiddenGoals,
    onGoalHidden,
    opening,
    openingRequest,
    setOpening,
    rows,
    summaryRow,
    beliefReview,
    prepPeople,
    draftRoutes,
    blind,
}: {
    /** New-message draft cards whose address a record proves. */
    draftRoutes: ReadonlySet<string>;
    blind?: ReactNode;
    /** Person-only Prep me chips resolved to their one meeting today. */
    prepPeople: ReadonlyMap<string, string>;
    firstBrief?: { caption?: string; chips: string[]; strip?: { title: string; source: string } } | null;
    brief: MobileBriefDocument;
    briefRun?: TodayBriefView["briefRun"];
    weekAnswer?: { weekStart: string; answer: string };
    closingAnswer?: NonNullable<TodayBriefView["closingAnswer"]>;
    onClosingInteract: () => void;
    dayShape: TodayDayShape | null;
    dayShapeDate: string;
    dayShapeFailure: { lastUpdatedAt: number | null; retry: () => void } | null;
    holdPreview: HoldPreview;
    briefHold: ReturnType<typeof useBriefHoldOffer>;
    directHold: ReturnType<typeof useDirectHold>;
    stripHidden: ReadonlySet<string>;
    stripRemoval: { id: string; eventId: string } | null;
    stripError: "remove" | "restore" | "access" | "edited" | null;
    removeStripHold: (id?: string) => Promise<void>;
    undoStripRemoval: () => Promise<void>;
    setHoldPreview: (value: HoldPreview) => void;
    handlers: TodayActionHandlers;
    tr: Translate;
    tActivity: Translate;
    language: string;
    now: Date;
    /** Shows the brief's cards; off while observation is paused or unknown,
     *  when the computer cannot check their sources. */
    cardsShown: boolean;
    /** Observation is paused: the card says why its buttons are gone. */
    paused: boolean;
    cardRef: React.RefObject<HTMLElement>;
    /** A card left the brief since Today read it. */
    onCardGone: (retry: () => void) => void;
    active?: boolean;
    goalCheckIn?: GoalCheckInView | null;
    onGoalCheckInAnswered?: () => void;
    /** Goals marked done or forgotten here, left out at once. */
    hiddenGoals: ReadonlySet<string>;
    onGoalHidden: (goalId: string, hidden: boolean) => void;
    /** "Talk about this brief" is opening the conversation. */
    opening: boolean;
    openingRequest: React.MutableRefObject<number>;
    setOpening: (opening: boolean) => void;
    rows: MoreRowsState;
    /** "Today so far" and "Daily news", built by the caller. */
    summaryRow: ReactNode;
    beliefReview: TodayWeekBeliefReview;
}): BriefParts {
    // The goal Today checks in about is asked about instead of listed.
    const checkIn = goalCheckIn && !hiddenGoals.has(goalCheckIn.goalId) ? goalCheckIn : null;
    const brief = withoutHiddenGoals(storedBrief, checkIn ? new Set([...hiddenGoals, checkIn.goalId]) : hiddenGoals);
    // Older stored briefs and Go's empty lists can arrive as null.
    const storedNarrative = narrativeInPartOrder(brief);
    const storedClosing = storedNarrative.at(-1);
    const closingText = storedClosing?.text ?? "";
    const holdQuestion = /[?？]\s*$/.test(closingText) && /\b(?:hold|reserve|block|set aside|make time)\b|预留|留出|留给|安排.*时间/i.test(closingText);
    const hasHoldOffer = Boolean(storedClosing?.hold || holdQuestion || (dayShape?.hold && /[?？]/.test(closingText)));
    const narrative = hasHoldOffer ? storedNarrative.slice(0, -1) : storedNarrative;
    const evidence = new Map((brief.evidence ?? []).map((ref) => [ref.ref, ref]));
    const meetingItems = brief.sections.meetings ?? [];
    const meetings = nextMeetings(meetingItems, now);
    const upcoming = brief.upcoming ?? [];
    // Commitment cards retain the moment/chat reference when the same Purpose
    // also has a progress card. Promote before applying each section's cap.
    const purposeSections = ["commitments", "workingToward", "waitingOnYou"] as const;
    const byReason = attentionMode(brief);
    const needingSections = new Map<MobileBriefItem, BriefSection>();
    const needingPurposes = new Set<string>();
    for (const name of purposeSections) {
        for (const item of brief.sections[name] ?? []) {
            const attention = byReason ? needsAttentionByReason(item) : !!deadlineStatus(item, now, brief.timezone);
            if (item.kind === "find" || !attention) continue;
            const id = primaryPurposeID(item, evidence);
            if (id && needingPurposes.has(id)) continue;
            if (id) needingPurposes.add(id);
            needingSections.set(item, name);
        }
    }
    const duePurposeCandidate = (item: MobileBriefItem) => {
        const id = primaryPurposeID(item, evidence);
        return needingSections.has(item) || !!id && needingPurposes.has(id);
    };
    const remainingPurposeItems = (name: typeof purposeSections[number]) => (brief.sections[name] ?? [])
        .filter((item) => item.kind !== "find" && !duePurposeCandidate(item));
    const purposeItems = [
        ...needingSections.keys(),
        ...purposeSections.flatMap((name) => remainingPurposeItems(name).slice(0, MAX_SECTION_ITEMS)),
    ];
    const representedPurposes = new Set(purposeItems.map((item) => primaryPurposeID(item, evidence)).filter((id): id is string => !!id));
    // Hide a legacy copy when its primary Purpose is represented by a card.
    // Secondary refs can identify a next step; handoffs keep their own actions.
    const openLoops = (brief.sections.openLoops ?? []).filter((item) => {
        if (item.kind === "one_time_work") return true;
        const id = primaryPurposeID(item, evidence);
        return !id || !representedPurposes.has(id);
    });
    const openLoopNeedsYou = (item: MobileBriefItem) => byReason
        ? needsAttentionByReason(item)
        : needsYou(item, now, !!primaryPurposeID(item, evidence), brief.timezone);
    if (byReason) {
        for (const name of ATTENTION_OTHER_SECTIONS) {
            for (const item of brief.sections[name] ?? []) {
                if (item.kind !== "find" && needsAttentionByReason(item)) needingSections.set(item, name);
            }
        }
    }
    const needing = [...needingSections.keys(), ...openLoops.filter(openLoopNeedsYou)]
        .sort((a, b) => byReason
            ? attentionOrder(a, b)
            : (deadline(a)?.getTime() ?? Infinity) - (deadline(b)?.getTime() ?? Infinity));
    // Only the displayed promotions replace their source cards. An earlier
    // general open loop can take a slot, so select after the combined sort.
    const displayedNeeding = new Set(needing.slice(0, MAX_SECTION_ITEMS));
    const displayedNeedsPurposes = new Set([...displayedNeeding]
        .filter((item) => item.kind !== "one_time_work")
        .map((item) => primaryPurposeID(item, evidence)).filter((id): id is string => !!id));
    const visiblePurposes = new Set(displayedNeedsPurposes);
    const purposeLists = new Map<BriefSection, MobileBriefItem[]>();
    // Prefer the richer commitment card only if it survives that section's
    // cap. A later section can still show a Purpose whose earlier card did not.
    for (const name of purposeSections) {
        const items = (brief.sections[name] ?? []).filter((item) => {
            if (item.kind === "find" || displayedNeeding.has(item)) return false;
            const id = primaryPurposeID(item, evidence);
            return !id || !displayedNeedsPurposes.has(id) && !(needingPurposes.has(id) && visiblePurposes.has(id));
        });
        purposeLists.set(name, items);
        for (const item of items.slice(0, MAX_SECTION_ITEMS)) {
            const id = primaryPurposeID(item, evidence);
            if (id) visiblePurposes.add(id);
        }
    }
    const remainingOpenLoops = (brief.sections.openLoops ?? []).filter((item) => {
        if (displayedNeeding.has(item)) return false;
        const id = primaryPurposeID(item, evidence);
        // Keep the existing cap for ordinary attention items. A legacy copy
        // of an overflow Purpose remains eligible if no card is displayed.
        if (openLoopNeedsYou(item) && (!id || !needingPurposes.has(id) || item.kind === "one_time_work")) return false;
        if (item.kind === "one_time_work") return true;
        return !id || !visiblePurposes.has(id);
    });
    const lists = LIST_SECTIONS.map((name) => ({
        name,
        items: (name === "openLoops" ? remainingOpenLoops
            : isPurposeSection(name) ? purposeLists.get(name) ?? []
                : brief.sections[name] ?? [])
            // A card listed under attention is not repeated in its section.
            .filter((item) => item.kind !== "find" && !displayedNeeding.has(item)),
    }));
    const learnedCount = (brief.sections.learned ?? []).filter((item) => item.kind !== "find").length;
    const offPage = OFF_PAGE_SECTIONS.some((name) => (brief.sections[name] ?? []).some((item) => item.kind !== "find"))
        || (brief.limits?.truncatedSections?.length ?? 0) > 0
        || upcoming.length > MAX_SECTION_ITEMS
        || needing.length > MAX_SECTION_ITEMS
        || meetingItems.filter((item) => item.kind !== "day_shape").length > meetings.length
        || lists.some(({ items }) => items.length > MAX_SECTION_ITEMS);
    // The title names the brief (design section 1): its kind and the time it was made.
    const title = briefTitle(brief, language, tr, briefRun);
    // Made after its time because the computer was asleep then.
    const lateLine = briefLateLine(brief, language, tr, briefRun);
    // The brief's cards (backlog item 56): at most two buttons, the opener
    // behind "Talk about this brief", and each row's question. The computer
    // wrote their words with the brief; a click puts them in the message box
    // at once, unsent (backlog item 64), and Send checks the card again.
    const cards = cardsShown ? (brief.cards ?? []).filter((card) => {
        const weekLabel = brief.yourWeek?.open?.find((row) => row.cardId === card.id)?.label;
        const reply = isReplyChip(card) || (weekLabel ? isReplyChip({ ...card, label: weekLabel }) : false);
        return (!hasHoldOffer || card.kind !== "action") && !isNewMessageChip(card.label ?? "") && !isNewMessageChip(weekLabel ?? "") && (!reply || !paused && card.refs.some((ref) => evidence.get(ref)?.sourceKind === "brief_waiting_reply")) &&
            // With no proven address the new-message draft is not offered.
            (!isNewMessageDraftCard(card, evidence) || !paused && draftRoutes.has(card.id));
    }) : [];
    // On the last evening of the work week "Your week" is the brief: it
    // stands in for the daily text, its questions and the closing question
    // (docs/your-week/handoff.md).
    const week = brief.yourWeek ?? null;
    const lastParagraph = narrative[narrative.length - 1];
    const rawClosing = !week && lastParagraph && /[?？]/.test(lastParagraph.text) ? lastParagraph : null;
    const actionCandidates = week ? [] : cards.filter((card) => card.kind === "action" && card.label?.trim() &&
        // Only answers bound by the computer belong under a closing question;
        // older ordinary actions must never stand in for a missing answer.
        (!rawClosing || card.mode === "send" && !!card.answerTo?.trim()) &&
        (card.mode === "open_activity" ? !paused && card.target && handlers.onOpenActivitySuggestion : card.mode === "send" && card.message.trim()));
    // One answer per offer (at most two), then "Not now" (today-review v1.1.8 #3).
    const actionCards = actionCandidates.slice(0, rawClosing && CLOSING_DECLINE.test(actionCandidates[2]?.label?.trim() ?? "") ? 3 : 2);
    const closing = rawClosing && actionCards.length > 0 ? rawClosing : null;
    const itemCards = new Map(cards.filter((card) => card.kind === "item" && card.itemKey && card.message.trim()).map((card) => [card.itemKey!, card]));
    // A Find time question that names a day or a week reads that date range.
    const findTimeInRange = (card: MobileBriefCard) => {
        if (!handlers.onFindTimeRange || !isFindTimeLabel(card.label)) return false;
        const range = findTimeRangeFor(`${card.label ?? ""} ${card.message}`, now);
        if (!range) return false;
        handlers.onFindTimeRange(range);
        return true;
    };
    const sendCard = (card: MobileBriefCard, action?: "T84") => {
        if (findTimeInRange(card)) return;
        if (handlers.onFindTime && card.itemKey && isCommitmentItemKey(card.itemKey) &&
            isFindTimeLabel(card.label)) {
            const item = (brief.sections.commitments ?? []).find((candidate) => candidate.key === card.itemKey || candidate.id === card.itemKey || `commitments:${candidate.id}` === card.itemKey);
            handlers.onFindTime(item ? { thing: { title: item.title, sourceKind: "brief", sourceId: card.itemKey }, due: item.dueAt, reason: item.attentionReason } : undefined);
            return;
        }
        if (card.mode === "open_activity") {
            if (card.target) handlers.onOpenActivitySuggestion?.(card.target);
            return;
        }
        if (card.mode !== "send") return;
        handlers.onStartChat(card.message, { ...todayBriefCardRef(brief.briefId, card.id, card.message), ...(action ? { telemetryAction: action } : {}) });
    };
    const askCard = (card: MobileBriefCard, action: "T10" | "T81" = "T10") => {
        if (findTimeInRange(card)) return;
        if (handlers.onFindTime && isCommitmentItemKey(card.itemKey) && isFindTimeLabel(card.label)) {
            sendCard(card);
            return;
        }
        if (handlers.onAskBriefCard && card.mode === "send" && card.message.trim()) {
            handlers.onAskBriefCard(card.message, { ...todayBriefCardRef(brief.briefId, card.id, card.message), telemetryAction: action });
        } else {
            sendCard(card);
        }
    };
    // The chat opens while the computer checks the brief.
    const runOpening = (open: () => Promise<boolean>) => {
        if (opening) return;
        const request = ++openingRequest.current;
        setOpening(true);
        const failed = () => {
            if (openingRequest.current === request) onCardGone(() => runOpening(open));
        };
        const finish = () => {
            if (openingRequest.current === request) setOpening(false);
        };
        try {
            void open().then((opened) => { if (!opened) failed(); }, failed).finally(finish);
        } catch {
            failed();
            finish();
        }
    };
    const openConversation = () => {
        if (handlers.onOpenBriefConversation) runOpening(() => handlers.onOpenBriefConversation!(brief.briefId));
    };
    const askAboutParagraph = (paragraph: number, text: string, question: string) => {
        if (handlers.onAskAboutParagraph) runOpening(() => handlers.onAskAboutParagraph!(brief.briefId, paragraph, text, question));
    };
    // The brief ends on one question (mobileBriefDeliveredClosing in Go): its
    // last paragraph when that asks something. It closes the card, with the
    // answers under it.
    // The written brief always shows; on a week day the week section follows it.
    const paragraphs = rawClosing ? narrative.slice(0, -1) : narrative;
    const learnedRefs = new Set((brief.sections.learned ?? []).filter((item) => item.kind !== "find").flatMap((item) => item.refs ?? []));
    const paragraphCards = cards.filter((card) =>
        (card.kind === "question" && card.mode === "send" || card.kind === "item" && card.paragraph !== undefined) &&
        !!card.label?.trim() && !!card.message.trim() &&
        (prepMeetingID(card, evidence, prepPeople) === undefined || !paused && !!handlers.onOpenDayMeeting && !!prepMeetingID(card, evidence, prepPeople)));
    const paragraphChips = new Map<number, MobileBriefCard>();
    const chipLabels = new Set<string>();
    for (const card of paragraphCards) {
        const paragraph = card.paragraph;
        const label = card.label!.trim().replace(/\s+/g, " ").toLocaleLowerCase();
        if (paragraph === undefined || paragraph < 0 || paragraph >= paragraphs.length ||
            paragraphChips.has(paragraph) || chipLabels.has(label) || paragraphChips.size >= 3) continue;
        paragraphChips.set(paragraph, card);
        chipLabels.add(label);
    }
    // Play needs the words on the same brief: a recording never plays alone.
    const audio = !hasHoldOffer && brief.audio && (!brief.audio.status || brief.audio.status === "ok") && brief.audio.bytes > 0 && briefHasWords(brief) ? brief.audio : null;
    const asOf = firstBrief?.caption ?? title;
    const hasClosing = !!briefHold.offer || !!closing || actionCards.length > 0;

    // "More from today": each row opens the sections Today listed before RC 1.
    const listProps = { itemCards, onCard: sendCard, briefId: brief.briefId, evidence, handlers, tr, now, timeZone: brief.timezone };
    const titles = (items: MobileBriefItem[]) => tr("todayBrief.more.items", {
        count: items.length,
        titles: items.slice(0, MORE_DETAIL_TITLES).map((item) => item.title).join(", "),
    });
    const listed = (names: ReadonlyArray<keyof MobileBriefSections>) => names
        .map((name) => ({ name, items: lists.find((list) => list.name === name)?.items.slice(0, MAX_SECTION_ITEMS) ?? [] }))
        .filter(({ items }) => items.length > 0);
    const groupRow = (rowKey: string, names: ReadonlyArray<keyof MobileBriefSections>, detail: (shown: ReturnType<typeof listed>) => string) => {
        const shown = listed(names);
        if (shown.length === 0) return null;
        const primary = shown[0].name === names[0] ? shown[0] : null;
        const rest = primary ? shown.slice(1) : shown;
        return (
            <MoreRow
                key={rowKey}
                action={rowKey === "workingToward" && handlers.onOpenGoals ? (
                    <Button variant="quiet" size="sm" className="mb-2" onClick={handlers.onOpenGoals}>{tr("actions.openGoals")}</Button>
                ) : undefined}
                rowKey={rowKey}
                rows={rows}
                label={tr(`todayBrief.sections.${names[0]}`)}
                detail={detail(shown)}
                testId={`today-more-${rowKey}`}
                list={primary ? (header, listHidden, listId) => (
                    <BriefList {...listProps} name={primary.name} items={primary.items} onGoalHidden={onGoalHidden} header={header} listHidden={listHidden} listId={listId} />
                ) : undefined}
            >
                {rest.length > 0 ? rest.map(({ name, items }) => (
                    <BriefList key={name} {...listProps} name={name} items={items} onGoalHidden={onGoalHidden} />
                )) : undefined}
            </MoreRow>
        );
    };
    const listRow = (name: "needsYou" | "meetings" | "upcoming", items: MobileBriefItem[], detail: string, extra: Partial<Parameters<typeof BriefList>[0]> = {}) => items.length > 0 ? (
        <MoreRow
            key={name}
            rowKey={name}
            rows={rows}
            label={tr(`todayBrief.sections.${name}`)}
            detail={detail}
            testId={`today-more-${name}`}
            list={(header, listHidden, listId) => (
                <BriefList {...listProps} {...extra} name={name} items={items} header={header} listHidden={listHidden} listId={listId} />
            )}
        />
    ) : null;
    const goalCounts = (shown: ReturnType<typeof listed>) => shown
        .map(({ name, items }) => tr(`todayBrief.more.${name}`, { count: items.length }))
        .join(", ");
    const shownNeeding = needing.slice(0, MAX_SECTION_ITEMS);
    const shownUpcoming = upcoming.slice(0, MAX_SECTION_ITEMS);
    const moreRows = [
        listRow("needsYou", shownNeeding, titles(shownNeeding), { sourceSections: needingSections, onGoalHidden }),
        handlers.onOpenTasks ? (
            <Button key="all-tasks" variant="quiet" size="sm" className="self-start" onClick={() => measureToday("T17", "open", "opened", () => handlers.onOpenTasks?.())}>{tr("actions.seeAllTasks")}</Button>
        ) : null,
        listRow("meetings", meetings, meetings.map((item) => {
            const time = briefClockTime(item.at, language, tr, brief.timezone);
            const title = item.title;
            return time ? tr("todayBrief.more.meetingAt", { title, time }) : title;
        }).join(" · ")),
        summaryRow,
        groupRow("workingToward", WORKING_TOWARD_SECTIONS, goalCounts),
        listRow("upcoming", shownUpcoming, titles(shownUpcoming)),
        groupRow("moved", WORKED_ON_SECTIONS, (shown) => titles(shown.flatMap(({ items }) => items))),
            learnedCount > 0 ? (
            <div key="learned" className="border-b border-border-divider" data-more-row="learned">
                <MoreRowButton
                    id="today-more-learned"
                    testId="brief-learned-link"
                    label={tr("todayBrief.sections.learned")}
                    detail={tr("todayBrief.seeLearned", { count: learnedCount })}
                    onClick={() => measureToday("T19", "open", "opened", () => handlers.onOpenMemory())}
                />
            </div>
        ) : null,
    ];

    return {
        card: (
            <section
                ref={cardRef}
                aria-label={title}
                className={cn(BRIEF_CARD, "gap-3.5")}
                data-testid="brief-card"
            >
                {audio ? (
                    // Keyed by brief: a file still loading for the last brief
                    // never plays under this one.
                    <BriefAudioPlayer key={brief.briefId} briefId={brief.briefId} audio={audio} caption={asOf} />
                ) : (
                    <div className="text-[12px] text-muted" data-testid="brief-no-recording">{asOf}</div>
                )}
                {lateLine && <p className="-mt-2 text-[12px] text-muted" data-testid="brief-late-line">{lateLine}</p>}
                {dayShapeFailure ? <CalendarReadFailed retry={dayShapeFailure.retry} tr={tr} /> : (<TodayDayStrip heading={firstBrief?.strip} briefText={firstSentence(brief.narrative?.[0]?.text)} requestedDate={dayShapeDate} shape={dayShape ? { ...dayShape, events: dayShape.events.filter((event) => !stripHidden.has(event.id)), hold: briefHold.offer?.preferred && !briefHold.dismissal ? { ...briefHold.offer.preferred, label: briefHold.offer.thing.title } : null } : null} holdPreview={briefHold.offer ? (directHold.removed ? "dismissed" : directHold.held ? "held" : "proposed") : holdPreview} holdTitle={briefHold.offer?.thing.title} onOpenHold={briefHold.offer && !briefHold.dismissal ? () => measureToday("T31", "reach", "opened", () => handlers.onFindTime?.({ ...briefHold.offer!, telemetryAction: "T31" })) : undefined} onRemoveHold={(id) => { if (id) void removeStripHold(id); else void directHold.remove(); }} timeZone={brief.timezone} onOpenMeeting={handlers.onOpenDayMeeting} />)}
                {stripRemoval && <p role="status" className="text-[13px] text-muted">{tr("holds.removed")} · <button type="button" className="text-accent" onClick={() => void undoStripRemoval()}>{tr("holds.undo")}</button></p>}
                {stripError && <div role="status" className="text-[13px] text-destructive">{tr(stripError === "access" ? "holds.accessOff" : stripError === "remove" ? "holds.removeFailed" : stripError === "edited" ? "holds.edited" : "holds.restoreFailed")} {stripError === "restore" && stripRemoval && <button type="button" className="text-accent" onClick={() => void undoStripRemoval()}>{tr("holds.retry")}</button>} {stripError === "access" && <button type="button" className="text-accent" onClick={() => void measureToday("T31", "open", "opened", () => OpenPermissionSettings("calendars"))}>{tr("holds.openSettings")}</button>} {stripError !== "restore" && <button type="button" className="text-accent" onClick={() => void measureToday("T31", "open", "opened", () => OpenExternalURL("/System/Applications/Calendar.app"))}>{tr("holds.openCalendar")}</button>}</div>}

                {paragraphs.length > 0 && (
                    <div className="flex flex-col gap-4" data-testid="brief-narrative">
                        {paragraphs.map((para, i) => (
                            <BriefParagraph
                                key={i}
                                text={para.text}
                                topic={para.topicId && para.topicName?.trim() && isDisplayTopic({ name: para.topicName }) && handlers.onOpenTopic
                                    ? { name: para.topicName, open: () => handlers.onOpenTopic?.(para.topicId!) }
                                    : null}
                                chip={paragraphChips.get(i)}
                                onOpenLearned={(para.refs ?? []).some((ref) => learnedRefs.has(ref)) ? () => handlers.onOpenMemory() : undefined}
                                answered={!!paragraphChips.get(i) && !!handlers.answeredBriefCards?.has(paragraphChips.get(i)!.id)}
                                showAsk={cardsShown && !paused && !!handlers.onAskAboutParagraph}
                                onAsk={(question) => askAboutParagraph(i, para.text, question)}
                                onChip={(card) => {
                                    const meeting = prepMeetingID(card, evidence, prepPeople);
                                    if (meeting && handlers.onOpenDayMeeting) {
                                        handlers.onOpenDayMeeting(meeting);
                                    } else if (isReplyDraftCard(card) && handlers.onDraftBriefCard) {
                                        if (isNewMessageDraftCard(card, evidence)) handlers.onDraftBriefCard(brief.briefId, card, true);
                                        else handlers.onDraftBriefCard(brief.briefId, card);
                                    } else {
                                        if (card.kind === "question" && !handlers.onAskBriefCard && para.topicId && handlers.onPrepareInTopic?.(para.topicId, card.message)) return;
                                        askCard(card);
                                    }
                                }}
                            />
                        ))}
                    </div>
                )}
                {(brief.weekQuestion || weekAnswer) && !paused && <MorningWeekQuestion
                    key={brief.weekQuestion?.weekStart ?? weekAnswer?.weekStart} briefID={brief.briefId}
                    recommendation={brief.weekQuestion?.recommendation} initialAnswer={weekAnswer?.answer} />}
                {week && (
                    <div data-testid="your-week-section">
                        <YourWeekCard
                            week={week}
                            active={active}
                            briefId={brief.briefId}
                            beliefReview={beliefReview}
                            cards={cards}
                            timeZone={brief.timezone}
                            handlers={handlers}
                            onCard={(card) => sendCard(card, "T84")}
                            onAskCard={(card) => askCard(card, "T81")}
                        />
                    </div>
                )}
                {firstBrief && firstBrief.chips.length > 0 && handlers.onPrefillChat && (
                    <div className="flex flex-wrap gap-2" data-testid="first-brief-chips">
                        {firstBrief.chips.map((chip) => (
                            <button key={chip} type="button" onClick={() => measureToday("T15", "ask", "opened", () => handlers.onPrefillChat?.(chip))}
                                className="rounded-lg border border-chip-border bg-chip-bg px-[11px] py-1.5 text-[12.5px] font-normal text-ink transition-colors hover:border-border-focus">
                                {chip}
                            </button>
                        ))}
                    </div>
                )}
                {hasClosing && (
                    <div
                        className={cn("flex flex-col gap-3", (paragraphs.length > 0 || !!week) && "border-t border-border-divider pt-4")}
                        data-testid="brief-closing"
                    >
                        {briefHold.dismissal && briefHold.offer ? <p role="status" className="text-[13px] text-muted">{tr("holds.notNowMessage", { title: briefHold.offer.thing.title })} · <button type="button" className="text-accent" onClick={() => measureToday("T14", "do", "undone", briefHold.undo)}>{tr("holds.undo")}</button></p> : (closing || briefHold.offer?.preferred) && (
                            <p className="text-[16px] font-medium leading-[1.6] text-ink" data-testid="brief-closing-question">
                                {briefHold.offer?.preferred ? tr("holds.question", { title: briefHold.offer.thing.title, when: tr("holds.weekRange", { start: zonedFormat(new Date(briefHold.offer.preferred.start), language, { weekday: "long", hour: "numeric", minute: "2-digit", ...computerHourOptions() }, brief.timezone), end: zonedFormat(new Date(briefHold.offer.preferred.end), language, { hour: "numeric", minute: "2-digit", ...computerHourOptions() }, brief.timezone) }) }) : closing?.text}
                            </p>
                        )}
                        {briefHold.offer && briefHold.shared && !briefHold.dismissal && !directHold.held && <p className="text-[13px] text-muted">{tr("holds.sharedBusy")}</p>}
                        {briefHold.offer && !briefHold.dismissal ? <div className="flex flex-wrap items-center gap-2">{directHold.removed ? <span role="status" className="text-[13px] text-muted">{tr("holds.removed")}{directHold.canRestore && <> · <button type="button" className="text-accent" onClick={() => void directHold.restore()}>{tr("holds.undo")}</button></>}</span> : directHold.checking ? <span role="status" className="text-[13px] text-muted">{tr("holds.checking")}</span> : directHold.held ? <span role="status" className="text-[13px] text-muted">{tr("holds.held")}{directHold.canUndo && <> · <button type="button" className="text-accent" onClick={() => void directHold.undo()}>{tr("holds.undo")}</button></>}</span> : <><Button size="sm" onClick={() => void measureToday("T14", "do", "done", directHold.hold)}>{tr("holds.hold", { when: zonedFormat(new Date(briefHold.offer.preferred!.start), language, { hour: "numeric", minute: "2-digit", ...computerHourOptions() }, brief.timezone) })}</Button><Button size="sm" variant="secondary" onClick={() => measureToday("T14", "do", "done", briefHold.dismiss)}>{tr("holds.notNow")}</Button><Button size="sm" variant="quiet" onClick={() => handlers.onFindTime?.(briefHold.offer!)}>{tr("holds.otherTimes")}</Button></>}{directHold.failed && <span role="alert" className="text-[13px] text-muted">{tr("holds.failed")}{directHold.held && <> <button type="button" className="text-accent" onClick={() => handlers.onFindTime?.(briefHold.offer!)}>{tr("holds.otherTimes")}</button></>}</span>}</div> : !briefHold.dismissal && closing && actionCards.some((card) => !!card.answerTo) ? (
                            <ClosingAnswerActions key={`${brief.briefId}:${actionCards[0]?.answerTo ?? ""}`} briefID={brief.briefId} cards={actionCards} initialAnswer={closingAnswer} opening={opening} onOpen={openConversation} onDraft={handlers.onDraftBriefCard} onInteract={onClosingInteract} />
                        ) : !briefHold.dismissal && actionCards.length > 0 && (
                            <div className="flex flex-wrap items-center gap-2" data-testid="brief-conversation-actions">
                                {actionCards.map((card, index) => card.mode === "open_activity" && card.target && handlers.onOpenActivitySuggestion ? (
                                    <TodayNoticedAction key={card.id} target={card.target} primary={index === 0}
                                        label={tActivity(card.target.action === "routine" ? "noticed.makeRoutine" : "rowActions.addFollowUp")}
                                        onEdit={handlers.onOpenActivitySuggestion} />
                                ) : (
                                    <Button key={card.id} size="sm" variant={index === 0 ? "default" : "secondary"} onClick={() => measureToday("T12", "open", "opened", () => {
                                        sendCard(card);
                                        if (dayShape?.hold && closing && card.answerTo) setHoldPreview(index === 0 ? "held" : "dismissed");
                                    })}>{closingLabel(card.label, tr)}</Button>
                                ))}
                            </div>
                        )}
                    </div>
                )}
                {blind}
                <SensitiveExcluded count={brief.limits?.excludedSensitiveItems ?? 0} tr={tr} />
            </section>
        ),
        checkIn: checkIn && (
            <TodayGoalCheckIn
                active={active}
                checkIn={checkIn}
                dueLabel={briefDate(checkIn.dueAt, language, brief.timezone)}
                onHidden={onGoalHidden}
                onAnswered={() => onGoalCheckInAnswered?.()}
            />
        ),
        rows: moreRows,
        footer: offPage ? (
            <Button
                type="button"
                variant="link"
                size="sm"
                data-testid="brief-more-on-activity"
                className="ml-auto self-end"
                onClick={handlers.onOpenActivity}
            >
                {tr("todayBrief.moreOnActivity")}
            </Button>
        ) : null,
    };
}

/** A closing answer that declines reads "Not now" (暂不), the one word for
 *  putting something off (today-review v1.1.2 #6, v1.1.4 #6), whatever words
 *  the writer chose ("No, leave it", "Not today"). */
export const CLOSING_DECLINE = /^(?:not today|not now|no(?:[\s,.!，。].*)?|今天不做|暂不.*|先不.*|不用.*|不了.*|不[，,。].*)$/i;

function closingLabel(label: string | undefined, tr: Translate): string {
    return CLOSING_DECLINE.test((label ?? "").trim()) ? tr("todayBrief.notNow") : label ?? "";
}

function ClosingAnswerActions({ briefID, cards, initialAnswer, opener, opening, onOpen, onDraft, onInteract }: {
    briefID: string;
    cards: MobileBriefCard[];
    initialAnswer?: NonNullable<TodayBriefView["closingAnswer"]>;
    opener?: MobileBriefCard;
    opening: boolean;
    onOpen: () => void;
    onDraft?: TodayActionHandlers["onDraftBriefCard"];
    onInteract: () => void;
}) {
    const { t } = useTranslation("today");
    const closingLabel = (label?: string) => CLOSING_DECLINE.test((label ?? "").trim()) ? t("holds.notNow") : (label ?? "");
    const [answer, setAnswer] = useState(initialAnswer);
    const [editing, setEditing] = useState(false);
    const [busy, setBusy] = useState(false);
    const [failed, setFailed] = useState(false);
    useEffect(() => { setAnswer(initialAnswer); }, [initialAnswer?.cardId, initialAnswer?.questionDigest, initialAnswer?.label]);
    const choose = async (card: MobileBriefCard) => {
        if (busy) return;
        const started = performance.now();
        onInteract();
        setBusy(true);
        setFailed(false);
        try {
            const saved = await PostTodayClosingAnswer(briefID, card.id, card.message, answer?.cardId ?? "");
            recordTodayAction("T12", "do", "sent", started);
            setAnswer(saved);
            setEditing(false);
            if (onDraft && isReplyDraftCard(card)) onDraft(briefID, card);
        } catch { setFailed(true); recordTodayAction("T12", "do", "failed", started); }
        finally { setBusy(false); }
    };
    return <div className="flex flex-col gap-2" data-testid="brief-conversation-actions">
        {answer && !editing ? <div className="flex flex-wrap items-center gap-2 text-[13px] text-muted">
            <span>{t("weekQuestion.answered", { answer: closingLabel(answer.label) })}</span>
            <Button size="sm" variant="quiet" onClick={() => measureToday("T12", "open", "opened", () => setEditing(true))}>{t("weekQuestion.change")}</Button>
        </div> : <div className="flex flex-wrap items-center gap-2">
            {cards.map((card, index) => <Button key={card.id} size="sm" variant={answer?.cardId === card.id || !answer && index === 0 ? "default" : "secondary"} disabled={busy} onClick={() => void choose(card)}>{closingLabel(card.label)}</Button>)}
            {opener && <Button size="sm" variant="quiet" className="ml-auto" data-testid="brief-talk-button" disabled={opening} onClick={onOpen}>
                {opening && <Loader2 className="size-3.5 animate-spin" />}{t("todayBrief.talkAboutBrief")}
            </Button>}
        </div>}
        {failed && <p role="alert" className="text-[13px] text-error">{t("weekQuestion.failed")}</p>}
    </div>;
}

/** One paragraph of the brief, with its topic and optional specific chip. */
function BriefParagraph({ text, topic, chip, answered, onOpenLearned, showAsk, onAsk, onChip }: {
    text: string;
    topic: { name: string; open: () => void } | null;
    chip?: MobileBriefCard;
    answered: boolean;
    onOpenLearned?: () => void;
    showAsk: boolean;
    onAsk: (question: string) => void;
    onChip: (card: MobileBriefCard) => void;
}) {
    const { t } = useTranslation("today");
    const [asking, setAsking] = useState(false);
    const [question, setQuestion] = useState("");
    const topicIndex = topic ? text.indexOf(topic.name) : -1;
    const lines = text.split("\n");
    return (
        <div className="group relative flex min-w-0 flex-col gap-2 pr-10 [overflow-wrap:anywhere]">
            <p className="flex flex-col gap-4 text-[15.5px] leading-[1.6] text-ink">{lines.map((line, index) => {
                const indexOfTopic = topic ? line.indexOf(topic.name) : -1;
                return <span key={index} className={line.trim() ? "whitespace-pre-wrap" : "sr-only"}>{topic && indexOfTopic >= 0 ? <>{line.slice(0, indexOfTopic)}<Button data-topic-mention variant="link" size="sm" className="inline h-auto whitespace-normal p-0 align-baseline text-[length:inherit] font-normal leading-[inherit] text-accent hover:no-underline" onClick={() => measureToday("T09", "open", "opened", topic.open)}>{topic.name}</Button>{line.slice(indexOfTopic + topic.name.length)}</> : line}{index < lines.length - 1 && <span className="sr-only">{"\n"}</span>}</span>;
            })}</p>
            {((topic && topicIndex < 0) || chip || onOpenLearned) && (
                <div className="flex w-fit max-w-full min-w-0 flex-wrap items-center gap-1.5" data-testid="brief-paragraph-extras">
                    {topic && topicIndex < 0 && (
                        <Button variant="link" size="sm" className="h-auto min-w-0 flex-[0_1_auto] whitespace-normal [overflow-wrap:anywhere] p-0 text-left text-[12px] font-normal text-accent hover:no-underline" onClick={() => measureToday("T09", "open", "opened", topic.open)}>
                            {topic.name}
                        </Button>
                    )}
                    {onOpenLearned && (
                        <Button variant="link" size="sm" className="h-auto p-0 text-[12px]" onClick={() => measureToday("T19", "open", "opened", onOpenLearned)}>
                            {t("actions.openMemory")}
                        </Button>
                    )}
                    {chip && (
                        <Button
                            key={chip.id}
                            variant="outline"
                            size="sm"
                            data-testid="brief-question-chip"
                            className="h-auto min-h-7 min-w-0 max-w-full flex-[0_1_auto] whitespace-normal [overflow-wrap:anywhere] py-1.5 text-left leading-snug border-chip-border bg-chip-bg px-[11px] text-[12.5px] font-normal text-ink hover:border-border-focus hover:bg-chip-bg"
                            onClick={() => measureToday("T10", "ask", "opened", () => onChip(chip))}
                        >
                            <span className="min-w-0">{chip.label}</span>
                            {answered && <Check aria-hidden className="size-3.5" />}
                        </Button>
                    )}
                </div>
            )}
            {showAsk && <Button type="button" variant="ghost" size="icon" data-testid="brief-ask-about-paragraph"
                aria-label={t("todayBrief.askAboutParagraph")} title={t("todayBrief.askAboutParagraph")}
                className="absolute right-0 top-0 flex size-7 rounded-full text-muted opacity-0 transition-opacity group-hover:opacity-100 group-focus-within:opacity-100 focus-visible:opacity-100 motion-reduce:transition-none"
                onClick={() => measureToday("T11", "open", "opened", () => setAsking(true))}>
                <MessageCircle aria-hidden className="size-3.5" />
            </Button>}
            {showAsk && asking && <form className="my-2 flex gap-2" onKeyDown={(event) => { if (event.key === "Escape") { event.stopPropagation(); setAsking(false); } }}
                onBlur={(event) => { if (!question.trim() && !event.currentTarget.contains(event.relatedTarget)) setAsking(false); }}
                onSubmit={(event) => { event.preventDefault(); if (question.trim()) measureToday("T11", "ask", "sent", () => onAsk(question.trim())); }}>
                <input autoFocus className="min-w-0 flex-1 rounded-md border border-border bg-panel px-3 py-2 text-sm text-ink"
                    aria-label={t("todayBrief.askParagraphPlaceholder")} placeholder={t("todayBrief.askParagraphPlaceholder")}
                    value={question} onChange={(event) => setQuestion(event.target.value)} />
                <Button type="submit" size="sm" disabled={!question.trim()}>{t("todayBrief.askParagraphButton")}</Button>
            </form>}
        </div>
    );
}

function BriefList({
    name,
    items,
    evidence,
    handlers,
    tr,
    now,
    sourceSections,
    timeZone,
    itemCards,
    onCard,
    onGoalHidden,
    header,
    listHidden = false,
    listId,
}: {
    name: BriefListName;
    /** Each row's question, by the row's key (or id). */
    itemCards: Map<string, MobileBriefCard>;
    onCard: (card: MobileBriefCard) => void;
    briefId: string;
    items: MobileBriefItem[];
    evidence: Map<string, MobileBriefRef>;
    handlers: TodayActionHandlers;
    tr: Translate;
    now: Date;
    sourceSections?: Map<MobileBriefItem, BriefSection>;
    timeZone?: string;
    /** Done and Forget on a goal row; rows without it have no goal menu. */
    onGoalHidden?: (goalId: string, hidden: boolean) => void;
    /** Replaces the section's label: its "More from today" row. */
    header?: ReactNode;
    /** The row is folded. */
    listHidden?: boolean;
    listId?: string;
}) {
    return (
        <div data-testid={`brief-section-${name}`}>
            {header ?? (
                <div className="mb-2 text-[11px] font-semibold uppercase tracking-wider text-muted/70">
                    {tr(`todayBrief.sections.${name}`)}
                </div>
            )}
            <ul id={listId} hidden={listHidden} className={cn("divide-y divide-border rounded-2xl border border-border bg-background", header && "mb-4 mt-1")}>
                {items.map((item) => {
                    const section: BriefSection = sourceSections?.get(item) ?? (name === "needsYou" ? "openLoops" : name);
                    return (
                        <BriefRow
                            key={item.id}
                            item={item}
                            section={section}
                            showFindTime={name === "needsYou" && section === "commitments"}
                            link={briefItemLink(item, section, evidence)}
                            card={itemCards.get(item.key || item.id)}
                            onCard={onCard}
                            evidence={isPurposeSection(section) ? purposeEvidence(item, evidence) : []}
                            handlers={handlers}
                            tr={tr}
                            now={now}
                            timeZone={timeZone}
                            goalId={onGoalHidden && isPurposeSection(section) ? primaryPurposeID(item, evidence) : undefined}
                            onGoalHidden={onGoalHidden}
                        />
                    );
                })}
            </ul>
        </div>
    );
}

function BriefRow({
    item,
    section,
    showFindTime,
    link,
    card,
    onCard,
    evidence,
    handlers,
    tr,
    now,
    timeZone,
    goalId,
    onGoalHidden,
}: {
    item: MobileBriefItem;
    section: BriefSection;
    showFindTime: boolean;
    link: ItemLink | null;
    /** The row's question, sent at once by "Chat about this". */
    card?: MobileBriefCard;
    onCard: (card: MobileBriefCard) => void;
    evidence: MobileBriefRef[];
    handlers: TodayActionHandlers;
    tr: Translate;
    now: Date;
    timeZone?: string;
    /** The goal behind a goal row; it gets the "..." with Done and Forget. */
    goalId?: string;
    onGoalHidden?: (goalId: string, hidden: boolean) => void;
}) {
    const { t: tApp, i18n } = useTranslation("app");
    const blockedKey = handoffBlockedReasonKey({ status: item.status, blockedReason: item.context?.blockedReason });
    const due = section === "upcoming" && item.at ? new Date(item.at) : null;
    const dueLabel = due && !Number.isNaN(due.getTime())
        ? briefDateTime(item.at, i18n.resolvedLanguage ?? i18n.language, tr, timeZone)
        : null;
    const spec = item.context?.scheduleSpec;
    let schedule = "";
    if (spec) {
        schedule = spec.kind === "at" && spec.atMs
            ? tApp("automationsPage.when.once", { when: briefDateTime(spec.atMs, i18n.resolvedLanguage ?? i18n.language, tr, spec.timezone) })
            : upcomingWhen({ kind: "schedule", time: "", scheduleKind: spec.kind, scheduleConfig: {
                expr: spec.expression, every_ms: spec.everyMs, at_ms: spec.atMs,
            } }, now, tApp, tr, createScheduleTextLabels(tApp));
        schedule = shortBriefClockLabel(schedule, i18n.language, tr);
        if (schedule && spec.timezone) schedule = tr("todayBrief.scheduleWithTimezone", { schedule, timezone: spec.timezone });
    }
    const lastTouch = briefDate(item.lastTouch, i18n.language);
    const dueAt = briefDate(item.dueAt, i18n.language, timeZone);
    const dueStatus = deadlineStatus(item, now, timeZone);
    const purposeSection = isPurposeSection(section);
    // "What you worked on" shows what moved and what is left, never app or
    // file lists.
    const outcomeSection = section === "moved";
    const sinceDay = item.attentionReason?.trim() || item.evidenceUnavailable
        ? null
        : briefSinceDay(item.since, now, i18n.resolvedLanguage ?? i18n.language, tr, timeZone);
    const canFollow = link?.kind !== "mainChat" || !!handlers.onOpenMainChat;
    const follow = () => {
        if (!link) return;
        switch (link.kind) {
            case "join":
            case "url":
                BrowserOpenURL(link.url);
                break;
            case "memory":
                handlers.onOpenMemory(link.id);
                break;
            case "people":
                handlers.onOpenPeople();
                break;
            case "automations":
                handlers.onOpenAutomations();
                break;
            case "activity":
                handlers.onOpenActivity();
                break;
            case "mainChat":
                handlers.onOpenMainChat?.();
                break;
        }
    };
    const status =
        item.evidenceUnavailable
            ? (item.status === "active" || item.status === "waiting" ? tr(`todayBrief.purposeStatus.${item.status}`) : null)
            : dueStatus
              ? tr(`todayBrief.deadlineStatus.${dueStatus}`)
              : section === "meetings" && (item.status === "now" || item.status === "upcoming" || item.status === "done")
              ? tr(`todayBrief.meetingStatus.${item.status}`)
              : section === "commitments" && !deadline(item) && (item.status === "due_today" || item.status === "slipping")
                ? tr(`todayBrief.commitmentStatus.${item.status}`)
                : section === "schedules" && ["success", "failed", "running", "pending"].includes(item.status)
                  ? tr(`status.${item.status}`)
                  : null;

    return (
        <li className="group flex flex-wrap items-start gap-3 px-4 py-3">
            <div className="min-w-0 flex-1">
                <div className="flex flex-wrap items-baseline gap-x-2 gap-y-0.5">
                    <span className={cn("text-[13px] font-medium text-ink", purposeSection ? "break-words" : "truncate")}>{item.title}</span>
                    {status && <span className="shrink-0 text-[11px] text-muted">{status}</span>}
                </div>
                {item.attentionReason?.trim() && !item.evidenceUnavailable && (
                    <p className="mt-0.5 text-[12px] leading-snug text-ink/85" data-testid="brief-attention-reason">{item.attentionReason}</p>
                )}
                {sinceDay && <p className="mt-0.5 text-[11px] text-muted" data-testid="brief-open-since">{tr("todayBrief.openSince", { day: sinceDay })}</p>}
                {(item.detail || item.evidenceUnavailable) && (
                    <p className="mt-0.5 text-[12px] leading-snug text-muted">
                        {item.evidenceUnavailable ? tr("todayBrief.evidenceUnavailable") : item.detail}
                    </p>
                )}
                {purposeSection && !item.evidenceUnavailable && (
                    <>
                        <p className="mt-1 text-[11px] text-muted">
                            {lastTouch ? tr("todayBrief.lastTouch", { date: lastTouch }) : tr("todayBrief.noRecordedTouch")}
                        </p>
                        {dueAt && !dueStatus && <p className="mt-1 text-[11px] text-muted">{tr("todayBrief.purposeDueAt", { date: dueAt })}</p>}
                        {item.nextStep && <p className="mt-1 text-[12px] leading-snug text-ink/85">{tr("todayBrief.nextStep", { step: item.nextStep })}</p>}
                        {evidence.length > 0 && (
                            <ul className="mt-2 space-y-1" aria-label={tr("todayBrief.evidence")}>
                                {evidence.map((ref) => (
                                    <li key={ref.ref} className="text-[11px] leading-snug text-muted">
                                        {evidenceText(ref, i18n.language, tr)}
                                    </li>
                                ))}
                            </ul>
                        )}
                    </>
                )}
                {!item.evidenceUnavailable && (
                    <>
                        {!purposeSection && dueAt && !dueStatus && <p className="mt-1 text-[11px] text-muted">{tr("todayBrief.purposeDueAt", { date: dueAt })}</p>}
                        {dueLabel && <p className="mt-0.5 text-[12px] text-muted">{tr(due! < now ? "todayBrief.overdueAt" : "todayBrief.dueAt", { time: dueLabel })}</p>}
                        {schedule && <p className="mt-0.5 text-[12px] text-muted">{shortBriefTimes(schedule, i18n.language, tr)}</p>}
                        {item.context?.preview && <p className="mt-1 text-[12px] leading-snug text-ink/85">{item.context.preview}</p>}
                        {item.context?.sourceLabel && !outcomeSection && <p className="mt-0.5 text-[12px] text-muted">{item.context.sourceLabel}</p>}
                        {blockedKey && <p className="mt-0.5 text-[12px] text-muted">{tApp(blockedKey)}</p>}
                        {!!item.context?.fileCount && !outcomeSection && <p className="mt-0.5 text-[12px] text-muted">{tr("todayBrief.files", { count: item.context.fileCount })}</p>}
                        {outcomeSection && item.nextStep && <p className="mt-1 text-[12px] leading-snug text-ink/85">{tr("todayBrief.nextStep", { step: item.nextStep })}</p>}
                    </>
                )}
            </div>
            {!item.evidenceUnavailable && (
                <div className="flex shrink-0 items-center gap-3 pt-0.5 text-[12px]">
                    {item.context?.jobId && (section === "upcoming" || item.status === "failed") && (
                        <Button size="sm" variant="secondary" onClick={() => handlers.onRunNow(item.context!.jobId!)}>
                            {tr(item.status === "failed" ? "actions.retry" : "actions.runNow")}
                        </Button>
                    )}
                    {link?.kind === "join" ? (
                        <Button size="sm" variant="secondary" onClick={follow}>
                            <Video className="size-3.5" />
                            {tr("todayBrief.join")}
                        </Button>
                    ) : link && canFollow ? (
                        <button type="button" className="inline-flex items-center gap-0.5 font-medium text-muted transition-colors hover:text-ink" onClick={follow}>
                            {tr(link.kind === "mainChat" ? "todayBrief.viewInMainChat" : "todayBrief.open")}
                            {link.kind === "url" && <ArrowUpRight className="size-3.5 text-muted" />}
                        </button>
                    ) : null}
                    {showFindTime && handlers.onFindTime && (
                        <Button size="sm" variant="outline" onClick={() => measureToday("T14", "reach", "opened", () => handlers.onFindTime?.({ thing: { title: item.title, sourceKind: "brief", sourceId: item.key || item.id }, due: item.dueAt, reason: item.attentionReason }))}>
                            {tr("holds.findTime")}
                        </Button>
                    )}
                    {card && (
                        <button
                            type="button"
                            className="font-medium text-muted transition-colors hover:text-ink"
                            onClick={() => measureToday("T23", "ask", "opened", () => onCard(card))}
                        >
                            {tr("actions.askAbout")}
                        </button>
                    )}
                </div>
            )}
            {goalId && onGoalHidden && <TodayGoalMenu goalId={goalId} title={item.title} onHidden={onGoalHidden} />}
        </li>
    );
}
