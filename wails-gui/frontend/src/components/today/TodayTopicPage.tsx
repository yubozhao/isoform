import { useMemo } from "react";
import { useTranslation } from "react-i18next";
import { ChevronLeft } from "lucide-react";
import type { WorkAnswerInput, WorkItem, WorkList } from "@/lib/wails-shim-app";
import type { WorkAnswerError } from "@/hooks/useWorkList";
import type { TodayTopic } from "./topics";
import { TodayYourWork, workWhen } from "./TodayYourWork";

type Translate = (key: string, options?: Record<string, unknown>) => string;

/** Days of the last seven (today included) the person worked on it. */
export function seenDays(days: readonly string[] | undefined, now = new Date()): number {
    const key = (date: Date) => `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, "0")}-${String(date.getDate()).padStart(2, "0")}`;
    const week = new Set(Array.from({ length: 7 }, (_, i) => key(new Date(now.getFullYear(), now.getMonth(), now.getDate() - i))));
    return new Set((days ?? []).filter((day) => week.has(day))).size;
}

/** The topic's own items of Your work: its proposals and what was answered. */
export function topicWork(list: WorkList | null, topicId: string): WorkList | null {
    if (!list) return null;
    const items = list.items.filter((item) => item.ref?.topicId === topicId);
    return { ...list, items, toAnswer: items.filter((item) => item.state === "open" || item.state === "back").length, backFromAgents: items.filter((item) => item.state === "back").length };
}

/**
 * A topic, opened (computer-find v1.0.0, 18a): in Today's column with
 * ‹ Today, its kind and name, the days it was seen this week; Next, its
 * proposal as the shared item with the keys, and Done for this topic (Your
 * work, filtered to the topic); What Isoform saw; and Ask about it.
 */
export function TodayTopicPage({ topic, list, active, onAnswer, onUndo, onHandOff, onAsk, onBack, quickChat = true, now = new Date() }: {
    quickChat?: boolean;
    topic: TodayTopic;
    list: WorkList | null;
    active: boolean;
    onAnswer: (id: string, input: WorkAnswerInput) => Promise<WorkAnswerError | null>;
    onUndo: (id: string) => Promise<WorkAnswerError | null>;
    onHandOff?: (item: WorkItem, edited?: string) => void;
    /** Ask about {topic} ›: Chat with the topic attached. */
    onAsk?: () => void;
    onBack: () => void;
    now?: Date;
}) {
    const { t, i18n } = useTranslation("today");
    const tr: Translate = (key, options) => t(key as never, options as never) as unknown as string;
    const work = useMemo(() => topicWork(list, topic.id), [list, topic.id]);
    const seen = seenDays(topic.activityDays, now);
    const saw = [topic.latest, topic.latestFind, topic.next].filter((moment): moment is NonNullable<typeof moment> => !!moment?.text);
    return (
        <section data-testid="today-topic-page" aria-label={topic.name} className="flex flex-col gap-5">
            <button type="button" className="flex items-center gap-0.5 self-start text-[13px] font-medium text-accent" onClick={onBack} data-testid="topic-page-back">
                <ChevronLeft aria-hidden className="size-3.5" />{tr("cbrief.back")}
            </button>
            <header className="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1">
                <div className="flex flex-col gap-0.5">
                    <span className={topic.kind === "work" ? "text-[11px] font-semibold uppercase tracking-wider text-accent" : "text-[11px] font-semibold uppercase tracking-wider text-muted"}>{tr(topic.kind === "work" ? "topic.kindProject" : "topic.kindFollowing")}</span>
                    <h2 className="text-[20px] font-semibold text-ink">{topic.name}</h2>
                </div>
                {topic.activityDays && <span className="text-[12.5px] text-muted" data-testid="topic-page-seen">{tr("topic.seen", { n: seen })}</span>}
            </header>
            <div className="grid grid-cols-1 items-start gap-6 min-[1100px]:grid-cols-[minmax(0,600px)_minmax(0,1fr)]">
                {work && <TodayYourWork quickChat={quickChat} list={work} active={active} onAnswer={onAnswer} onUndo={onUndo} onHandOff={onHandOff} title={tr("topic.next")} now={now} />}
                <aside className="flex flex-col gap-2" data-testid="topic-page-saw">
                    <h3 className="text-[11px] font-semibold uppercase tracking-wider text-muted">{tr("topic.saw")}</h3>
                    {saw.map((moment, index) => (
                        <p key={index} className="text-[13px] leading-snug text-ink">
                            {moment.text}
                            {moment.at && <span className="text-muted"> · {workWhen(moment.at, now, i18n.language, tr)}</span>}
                        </p>
                    ))}
                    {onAsk && <button type="button" className="self-start text-[13px] font-medium text-accent" onClick={onAsk} data-testid="topic-page-ask">{tr("topic.ask", { topic: topic.name })}</button>}
                </aside>
            </div>
        </section>
    );
}
