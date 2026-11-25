import type { TodayChatContextRef, TodayChatStartOptions } from "@/lib/today-chat-navigation";

// ── Today's news (AI-curated via web search, cached daily) ──
// Mirrors the Go TodayNewsData/TodayNewsItem JSON shape returned by
// GetTodayNews(). Generation follows the daily-summary pattern: GetTodayNews
// returns the cached brief + a status; GenerateTodayNews() (async) kicks off
// web-search generation and emits `today:news-*` events on completion.

export type TodayNewsStatus =
    | "ready"
    | "generating"
    | "none"
    | "error"
    | "unconfigured"
    /** The last run was blocked by a permission decision. */
    | "denied"
    /** The last run could not reach the web. */
    | "unavailable";

export interface TodayNewsItem {
    title: string;
    summary: string;
    source: string;
    url: string;
}

export interface TodayNewsData {
    date: string;
    /** The news was picked for the user's own topics and interests. */
    personalized?: boolean;
    status: TodayNewsStatus;
    items: TodayNewsItem[];
    updatedAt: number;
}

export interface TodayActionHandlers {
    /** false while the chat switch is off (scope cut 2026-11, A3): every way
     *  into Chat leaves Today. Unset means Chat is there. */
    chat?: boolean;
    /** Answers a commitment's Find time chip in place. */
    onFindTime?: (source?: import("./HoldCard").HoldCardSource) => void;
    onFindTimeRange?: (range: { first: string; last: string; minutes: number; nextWeek?: boolean }) => void;
    /** Opens the main chat with an action's request in the composer, unsent:
     *  news, the day's summary and the brief's cards (backlog item 64). */
    onStartChat: (prompt: string, context: TodayChatContextRef, options?: TodayChatStartOptions) => void;
    /** "Ask about {topic} ›": opens Chat with the topic attached, an empty
     *  field and nothing sent (computer-conformance-answers v1.2.0 T11). */
    onAskAboutTopic?: (topic: { id: string; name: string }) => void;
    /** "Talk about this brief": Isoform speaks first in the main chat, and the
     *  brief goes with the user's answer. Resolves false when it cannot. */
    onOpenBriefConversation?: (briefId: string) => Promise<boolean>;
    /** Opens the main chat about one body paragraph of the brief. */
    onAskAboutParagraph?: (briefId: string, paragraph: number, text: string, question: string) => Promise<boolean>;
    /** Answers the displayed brief card in the panel, with its source checked at admission. */
    onAskBriefCard?: (prompt: string, context: TodayChatContextRef) => void;
    /** newMessage: no Mail thread; the person's address was proven from a record. */
    onDraftBriefCard?: (briefId: string, card: import("@/lib/wails-shim-app").MobileBriefCard, newMessage?: boolean) => void;
    onDraftMeetingNote?: (meeting: import("@/lib/wails-shim-app").TodayBeforeYouTalk, prompt: string) => void;
    answeredBriefCards?: ReadonlySet<string>;
    onRunNow: (jobId: string) => void;
    onOpenMemory: (entryId?: string) => void;
    onOpenAutomations: () => void;
    /** Opens a brief's own detail beside Tasks › Scheduled (a missed brief's Why). */
    onOpenBriefDetail?: (slot: "morning" | "evening") => void;
    onOpenGoals?: () => void;
    onOpenTasks?: () => void;
    onOpenDoneTasks?: () => void;
    /** A source-bound weekly result opens the exact task thread. */
    onOpenWeekTask?: (id: string) => void;
    onOpenDayMeeting?: (id: string) => void;
    /** B2 owns the saved, source-bound meeting prep host. */
    onBriefMeeting?: (meeting: import("@/lib/wails-shim-app").TodayBeforeYouTalk, text: string) => void;
    onOpenMeetingNotes?: (sessionId: string) => void;
    onOpenActivity: () => void;
    /** Agents, for Your work's "Connect an agent ›" (computer-today v1.1.0 rule 3). */
    onOpenAgents?: () => void;
    /** Opens Activity on a suggestion's day, with its editable field or draft. */
    onOpenActivitySuggestion?: (target: import("@/lib/wails-shim-app").MobileBriefActivityTarget) => void;
    onOpenPeople: (personId?: string) => void;
    /** Opens a Settings page (the brief's missing permissions live on
     *  "activity", Settings > Observation). */
    onOpenSettings?: (topic: string) => void;
    /** Opens the main chat, where setup asks one permission at a time. */
    onOpenMainChat?: () => void;
    /** Opens the main chat with these words in the box, unsent (the first
     *  day's chips and starters). */
    onPrefillChat?: (text: string) => void;
    /** Opens what one of Isoform's cards points at (Mail, a day on
     *  Activity, a person), as the main chat page does. */
    onOpenReportTarget?: (target: import("@/lib/stream-helpers").MainChatReportTarget) => void;
    /** Opens a topic beside Today (RC 1, part 2); a paragraph's topic shows
     *  only when this exists. */
    onOpenTopic?: (topicId: string) => void;
    /** Opens a topic with prepared words in its box, unsent (a paragraph's
     *  question). Returns false when Today has no such topic; the words then
     *  go to the main chat's box. */
    onPrepareInTopic?: (topicId: string, text: string) => boolean;
}
