import { measureToday } from "@/lib/today-telemetry";
import { useTodaySectionSeen } from "@/hooks/useTodaySectionSeen";
import { memo, useCallback, useId, useRef } from "react";
import { useTranslation } from "react-i18next";
import { AlertCircle, RefreshCw } from "lucide-react";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import { useTopicRefreshIndicator } from "@/hooks/useTopicRefreshIndicator";
import type { TodayTopicsStatus } from "@/hooks/useTodayTopics";
import { clockTime, computerClock } from "@/lib/brief-time";
import { TOPIC_CARD_LINE_MAX, topicActiveDays, topicCards, type TodayTopic } from "./topics";

type Translate = (key: string, options?: Record<string, unknown>) => string;

/** "Mon 10:00 AM", or only the time when it is today, in the one clock. */
function shortWhen(iso: string | undefined, now: Date, language: string, tr: Translate): string | null {
    if (!iso) return null;
    const when = new Date(iso);
    if (Number.isNaN(when.getTime())) return null;
    const time = clockTime(when, language, tr);
    return when.toDateString() === now.toDateString()
        ? time
        : `${when.toLocaleDateString(language, { weekday: "short" })} ${time}`;
}

/** A followed topic's reason: "You spent time on Wearable AI yesterday". */
export function topicWhy(topic: TodayTopic, now: Date, language: string, tr: Translate): string | null {
    if (!topic.lastSeenAt) return null;
    const seen = new Date(topic.lastSeenAt);
    if (Number.isNaN(seen.getTime())) return null;
    const day = (date: Date) => new Date(date.getFullYear(), date.getMonth(), date.getDate()).getTime();
    const days = Math.round((day(now) - day(seen)) / 86_400_000);
    const when = days <= 0 ? tr("finds.whenToday")
        : days === 1 ? tr("finds.whenYesterday")
            : tr("finds.whenOn", { day: seen.toLocaleDateString(language, days < 7 ? { weekday: "long" } : { month: "long", day: "numeric" }) });
    return tr("finds.why", { topic: topic.name, when });
}

/** A local day (YYYY-MM-DD) as a card's when: today, yesterday or a weekday. */
function activityDayWhen(day: string, now: Date, language: string, tr: Translate): string {
    const date = new Date(`${day}T12:00:00`);
    const dayStart = (value: Date) => new Date(value.getFullYear(), value.getMonth(), value.getDate()).getTime();
    const days = Math.round((dayStart(now) - dayStart(date)) / 86_400_000);
    if (days <= 0) return tr("finds.whenToday");
    if (days === 1) return tr("finds.whenYesterday");
    return date.toLocaleDateString(language, { weekday: "short" });
}

function topicCardLine(topic: TodayTopic, now: Date, language: string, tr: Translate): { text: string; isNew: boolean } | null {
    // Raw window titles and click trails are observations, not a summary of
    // what moved. A line that can't say what moved falls through to the next
    // true thing: the week's days, then when it last moved. Never a bare status.
    const clause = (text: string, when: string) => {
        const clean = text.trim();
        if (!clean || [...`${clean} · ${when}`].length > TOPIC_CARD_LINE_MAX || /[\n\r"“”\\]|\b(?:visited|opened|clicked|viewed|browsed)\b|访问了|打开了|点击了/i.test(clean)) return null;
        return clean;
    };
    const firstLine = (text?: string) => text?.trim() ?? "";
    const find = firstLine(topic.latestFind?.text);
    const findWhen = shortWhen(topic.latestFind?.at, now, language, tr);
    if (find && findWhen) {
        const headline = clause(tr("yourTopics.newFind", { headline: find }), findWhen);
        if (headline) return { text: `${headline} · ${findWhen}`, isNew: true };
    }
    const latest = firstLine(topic.latest?.text);
    const latestWhen = shortWhen(topic.latest?.at, now, language, tr);
    if (latest && latestWhen && !/^updated\.?$/i.test(latest)) {
        const text = clause(latest, latestWhen);
        if (text) return { text: `${text} · ${latestWhen}`, isNew: false };
    }
    const days = topicActiveDays(topic, now);
    const lastDay = topic.activityDays?.[0];
    if (days > 0 && lastDay) {
        return { text: tr("yourTopics.activityDays", { count: days, when: activityDayWhen(lastDay, now, language, tr) }), isNew: false };
    }
    const last = topic.lastSeenAt ?? topic.latest?.at ?? (topic.activityDays?.[0] ? `${topic.activityDays[0]}T12:00:00` : undefined);
    const date = last ? new Date(last) : null;
    if (topic.pinned && date && !Number.isNaN(date.getTime()) && new Date(date.getFullYear(), date.getMonth(), date.getDate()) <= now) {
        return { text: tr("yourTopics.quietSince", { day: date.toLocaleDateString(language, { weekday: "long" }) }), isNew: false };
    }
    // A moment from this week, with no day list, is one day worked this week (the same form as above).
    const latestAt = topic.latest?.at ? new Date(topic.latest.at) : null;
    const dayStart = (value: Date) => new Date(value.getFullYear(), value.getMonth(), value.getDate()).getTime();
    if (latestWhen && latestAt && !Number.isNaN(latestAt.getTime()) && Math.round((dayStart(now) - dayStart(latestAt)) / 86_400_000) < 7) {
        return { text: tr("yourTopics.activityDays", { count: 1, when: latestWhen }), isNew: false };
    }
    // An older moment keeps its card: the date it was, with no day count.
    if (latestAt && !Number.isNaN(latestAt.getTime())) {
        return { text: tr("yourTopics.lastOn", { when: latestAt.toLocaleDateString(language, { month: "short", day: "numeric" }) }), isNew: false };
    }
    return null;
}

const TodayTopicCard = memo(function TodayTopicCard({ topic, selected, onPick, day, alone = false }: {
    topic: TodayTopic;
    selected: boolean;
    /** The only card: it spans the row, with its line beside the name. */
    alone?: boolean;
    onPick: (topic: TodayTopic) => void;
    day: number;
}) {
    const { t, i18n } = useTranslation("today");
    const tr: Translate = (key, options) => t(key as never, options as never) as unknown as string;
    const line = topicCardLine(topic, new Date(day), i18n.language, tr);
    return (
        <button
            type="button"
            aria-pressed={selected}
            onClick={() => measureToday("T50", "open", "opened", () => onPick(topic))}
            data-testid="today-topic-card"
            className={cn(
                alone && "today-topic-card-alone",
                "flex min-w-0 max-w-full flex-col items-start gap-2 whitespace-normal [overflow-wrap:anywhere] rounded-xl border p-3.5 text-left text-ink transition-colors hover:border-border-focus focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent-2/40",
                selected ? "border-accent bg-surface-active" : "border-border bg-background",
            )}
        >
            <span className="min-w-0 max-w-full text-[14.5px] font-semibold">{topic.name}</span>
            {line && <span className={cn("min-w-0 max-w-full text-[12.5px] leading-[1.45]", line.isNew ? "text-accent" : "text-muted")} data-testid="today-topic-line">{line.text}</span>}
        </button>
    );
});

/**
 * "Your topics" on Today (RC 1): one grid of work topics, each with its state
 * as the reason, and followed topics that have one. A card opens the topic
 * beside Today; it never sends anything. Each state keeps the rest of Today
 * usable (RC1-States).
 */
export function TodayTopicsGrid({
    topics,
    briefTopicIds = [],
    status,
    refreshing = false,
    refreshFailed = false,
    lastUpdatedAt = null,
    firstRun = false,
    selectedId,
    onPick,
    onRetry,
    onEdit,
    source,
    now = new Date(),
    active = true,
}: {
    topics: TodayTopic[];
    briefTopicIds?: string[];
    status: TodayTopicsStatus;
    refreshing?: boolean;
    refreshFailed?: boolean;
    lastUpdatedAt?: number | null;
    firstRun?: boolean;
    selectedId: string | null;
    onPick: (topic: TodayTopic) => void;
    onRetry: () => void;
    onEdit: () => void;
    /** Where the topics came from, beside the heading (the first days). */
    source?: string;
    now?: Date;
    active?: boolean;
}) {
    const { t, i18n } = useTranslation("today");
    const tr: Translate = (key, options) => t(key as never, options as never) as unknown as string;
    const headingId = useId();
    const order = new Map<string, number>();
    briefTopicIds.forEach((id, index) => { if (!order.has(id)) order.set(id, index); });
    // What took most of the last seven days leads; pinned, waiting and the
    // brief's order break ties.
    topics = topicCards(topics.filter((topic) => topicCardLine(topic, now, i18n.language, tr)), now).sort((a, b) =>
        topicActiveDays(b, now) - topicActiveDays(a, now) ||
        Number(b.pinned) - Number(a.pinned) ||
        Number(b.status === "waiting") - Number(a.status === "waiting")
        || (order.get(a.id) ?? Infinity) - (order.get(b.id) ?? Infinity) || 0);

    const positions = useRef<string[]>([]);
    const byId = new Map(topics.map((topic) => [topic.id, topic]));
    const ordered = positions.current.flatMap((id) => {
        const topic = byId.get(id);
        if (!topic) return [];
        byId.delete(id);
        return [topic];
    });
    topics = [...ordered, ...byId.values()];
    const seenRef = useTodaySectionSeen(active, "your_topics", topics.length, status !== "loading");
    positions.current = topics.map((topic) => topic.id);
    const pickRef = useRef(onPick);
    pickRef.current = onPick;
    const pick = useCallback((topic: TodayTopic) => pickRef.current(topic), []);
    const day = new Date(now.getFullYear(), now.getMonth(), now.getDate()).getTime();
    const pending = refreshing || status === "loading";
    const showRefreshing = useTopicRefreshIndicator(pending);

    let body: React.ReactNode;
    if (topics.length === 0 && firstRun && status !== "failed") {
        body = (
            <p className="rounded-xl border border-border bg-panel px-[18px] py-4 text-[14px] text-ink" data-testid="today-topics-gathering">
                {tr("yourTopics.gathering")}
            </p>
        );
    } else if ((status === "loading" || refreshing) && topics.length === 0) {
        body = (
            <div aria-busy="true" className="grid grid-cols-2 gap-2.5" data-testid="today-topics-loading">
                {[0, 1].map((i) => (
                    <div key={i} className="flex flex-col gap-1.5 rounded-xl border border-border p-3.5">
                        <span className="h-2 w-2/5 rounded-md bg-surface-active" />
                        <span className="h-3 w-4/5 rounded-md bg-surface-active" />
                        <span className="h-2.5 w-3/5 rounded-md bg-surface-active" />
                    </div>
                ))}
            </div>
        );
    } else if (status === "failed" && topics.length === 0) {
        body = (
            <div className="flex flex-col gap-2.5 rounded-xl border border-border bg-panel px-[18px] py-4">
                <p role="alert" className="flex items-center gap-2 text-[13.5px] text-ink">
                    <AlertCircle className="size-[15px] shrink-0 text-error" />
                    {tr("yourTopics.loadFailed")}
                </p>
                <div>
                    <Button size="sm" variant="secondary" onClick={() => measureToday("T51", "do", "done", onRetry)}>{tr("todayBrief.retry")}</Button>
                </div>
            </div>
        );
    } else if (status === "ready" && topics.length === 0) {
        body = (
            <p className="rounded-xl border border-border bg-panel px-[18px] py-4 text-[14px] text-ink" data-testid="today-topics-empty">
                {tr("yourTopics.empty")}
            </p>
        );
    } else {
        body = (
            // One or two topics fill the row instead of leaving empty columns.
            <div className="today-topics-grid grid gap-2.5" data-count={Math.min(topics.length, 3)}>
                {topics.map((topic) => (
                    <TodayTopicCard key={topic.id} topic={topic} selected={topic.id === selectedId} onPick={pick} day={day} alone={topics.length === 1} />
                ))}
            </div>
        );
    }

    return (
        <section ref={seenRef} aria-labelledby={headingId} className="flex min-w-0 flex-col gap-3" data-testid="today-topics">
            <div className="flex flex-wrap items-baseline gap-x-2.5 gap-y-1.5">
                <h2 id={headingId} className="text-[18px] font-semibold text-ink">{tr("yourTopics.title")}</h2>
                {source && <span className="text-[12.5px] text-muted" data-testid="today-topics-source">{source}</span>}
                <Button size="icon" variant="quiet" className="ml-auto size-7" onClick={() => measureToday("T51", "do", "done", onRetry)} title={tr("yourTopics.refresh")} aria-label={tr("yourTopics.refresh")}>
                    <RefreshCw className="size-3.5" />
                </Button>
                <Button size="sm" variant="quiet" onClick={() => measureToday("T53", "open", "opened", onEdit)}>{tr("yourTopics.edit")}</Button>
            </div>
            {showRefreshing && <p role="status" className="text-[12.5px] leading-[18px] text-muted" data-testid="today-topics-refreshing">{tr("yourTopics.refreshing")}</p>}
            {refreshFailed && topics.length > 0 && <p role="alert" className="text-[12px] text-muted">{tr("refresh.failed")}{lastUpdatedAt && ` · ${tr("refresh.showing", { time: computerClock(new Date(lastUpdatedAt), i18n.language) })}`} · <button type="button" className="text-accent underline" onClick={() => measureToday("T51", "do", "done", onRetry)}>{tr("refresh.retry")}</button></p>}
            {body}
        </section>
    );
}
