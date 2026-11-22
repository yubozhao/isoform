import { useCallback, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import type { TFunction } from "i18next";
import { EventsEmit } from "../../../wailsjs/runtime/runtime";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { SettingsFailureLine } from "@/components/settings/settings-failure";
import {
    GetAssistantGiven,
    type AssistantGivenHour,
    type AssistantGivenSource,
    type AssistantGivenView,
} from "@/lib/wails-shim-app";

const groupLabel = "text-[11px] font-semibold uppercase tracking-wide text-muted select-none";

/** The See them source kinds, by What Isoform sees' names (design T15).
 *  Every kind the record keeps is here; TestEveryAssistantCallKindIsNamed
 *  fails on one that isn't. */
export const SOURCE_KINDS = [
    "mail", "calendar", "screen", "call", "watch", "messages", "wechat", "memory", "knowledge",
    "people", "activity", "goals", "chat", "web", "topic_names", "agents_history", "work", "files",
] as const;
type SourceKind = (typeof SOURCE_KINDS)[number];

function knownKind(kind: string): SourceKind {
    // An unknown kind (from a newer build) reads as the day's activity, never "Other".
    return (SOURCE_KINDS as readonly string[]).includes(kind) ? (kind as SourceKind) : "activity";
}

/** "Mail (4 threads), Calendar (1 event)": each kind with its count and unit. */
export function givenSources(sources: AssistantGivenSource[], t: TFunction<"settings">): string {
    return sources.map((s) => t(`given.counts.${knownKind(s.kind)}`, { count: Math.max(1, s.count) })).join(t("given.join"));
}

function clock(ms: number, locale: string): string {
    return new Date(ms).toLocaleTimeString(locale, { hour: "numeric", minute: "2-digit" });
}

function dayName(ms: number, locale: string): string {
    return new Date(ms).toLocaleDateString(locale, { weekday: "long", month: "short", day: "numeric" });
}

/** "49 min", "1 h 12 min": the watch's form. */
function length(fromMs: number, toMs: number, t: TFunction<"settings">): string {
    const minutes = Math.max(1, Math.round((toMs - fromMs) / 60_000));
    const h = Math.floor(minutes / 60);
    const m = minutes % 60;
    if (h === 0) return t("given.minutes", { m });
    return m === 0 ? t("given.hoursOnly", { h }) : t("given.hoursMinutes", { h, m });
}

function when(ms: number, locale: string, t: TFunction<"settings">): string {
    return t("given.when", { date: dayName(ms, locale), time: clock(ms, locale) });
}

function OpenLink({ onOpen }: { onOpen: () => void }) {
    const { t } = useTranslation("settings");
    return (
        <button type="button" className="shrink-0 text-[12px] font-medium text-muted transition-colors hover:text-ink" onClick={onOpen}>
            {t("given.open")}
        </button>
    );
}

function Row({ title, meta, from, action }: { title: string; meta?: string; from: string; action?: React.ReactNode }) {
    return (
        <li className="flex items-start gap-3 py-2.5" data-given-row>
            <div className="min-w-0 flex-1">
                <div className="flex items-baseline gap-2">
                    <span className="truncate text-[13px] text-ink">{title}</span>
                    {meta && <span className="shrink-0 text-[12px] text-muted">{meta}</span>}
                </div>
                <p className="mt-0.5 text-[12px] text-muted">{from}</p>
            </div>
            {action}
        </li>
    );
}

/**
 * One group. The page never says more than the code does (design T10): a
 * group whose rows all name what they read carries the Forget line; one
 * with a row counted by the hour carries see.byHour instead.
 */
function Group({ title, count, removable, byHour = false, assistant, after, children }: {
    title: string;
    count: number;
    /** Every row names what it read, so Forget on any of it removes the row. */
    removable: boolean;
    /** It holds a row counted by the hour (a pass that names nothing it read). */
    byHour?: boolean;
    assistant: string;
    /** A line of the group's own under its rows (the passes' note). */
    after?: React.ReactNode;
    children: React.ReactNode;
}) {
    const { t } = useTranslation("settings");
    return (
        <section className="space-y-1" data-given-group>
            <h3 className={groupLabel}>
                {title} <span className="tabular-nums">{count}</span>
            </h3>
            {count > 0 ? <ul className="divide-y divide-border">{children}</ul> : null}
            {count > 0 && after}
            {count > 0 && byHour && (
                <p className="text-[12px] text-muted" data-given-by-hour>{t("see.byHour")}</p>
            )}
            {count > 0 && removable && (
                <p className="text-[12px] leading-relaxed text-muted" data-given-foot>{t("given.foot", { assistant })}</p>
            )}
        </section>
    );
}

function groupName(group: AssistantGivenHour["group"], t: TFunction<"settings">): string {
    switch (group) {
        case "about":
            return t("see.learning");
        case "work":
            return t("see.work");
        default:
            return t("given.sense");
    }
}

/** "10 AM · Learning about you · 3 times". */
function HourRow({ hour, locale, onOpen }: { hour: AssistantGivenHour; locale: string; onOpen?: () => void }) {
    const { t } = useTranslation("settings");
    const parts = [t("given.from", { sources: hour.sources.length > 0 ? givenSources(hour.sources, t) : t("given.sawThatHour") })];
    if (hour.made && hour.made > 0) parts.push(t("see.made", { count: hour.made, n: hour.made }));
    return (
        <Row
            title={t("given.hourRow", {
                hour: new Date(hour.from).toLocaleTimeString(locale, { hour: "numeric" }),
                group: groupName(hour.group, t),
                times: t("given.times", { count: hour.times, n: hour.times }),
            })}
            from={parts.join(" · ")}
            action={onOpen ? <OpenLink onOpen={onOpen} /> : undefined}
        />
    );
}

/**
 * See them (computer-data v1.1.0): what the assistant was given this week,
 * by name and time, never the text. A failed read shows its error with Try
 * again, never an empty sheet.
 */
export function AssistantGivenSheet({ open, onOpenChange, assistant }: {
    open: boolean;
    onOpenChange: (open: boolean) => void;
    /** The assistant's name, as Your data's row shows it. */
    assistant: string;
}) {
    const { t, i18n } = useTranslation("settings");
    const locale = i18n.language;
    const [view, setView] = useState<AssistantGivenView | null>(null);
    const [failed, setFailed] = useState(false);

    const load = useCallback(() => {
        setFailed(false);
        GetAssistantGiven()
            .then((next) => setView(next))
            .catch(() => {
                setView(null);
                setFailed(true);
            });
    }, []);
    useEffect(() => {
        if (open) load();
    }, [open, load]);

    const openTarget = (target: string) => {
        onOpenChange(false);
        EventsEmit("notification:clicked", target);
    };

    const sense = view ? view.stretches.length + view.watch.length + view.sense.length : 0;
    return (
        <Dialog open={open} onOpenChange={onOpenChange}>
            <DialogContent className="w-[560px] max-w-[90vw]" data-testid="assistant-given-sheet">
                <DialogHeader className="flex-col items-start gap-1.5">
                    <DialogTitle>{t("given.title", { assistant })}</DialogTitle>
                    <DialogDescription>{t("given.sub")}</DialogDescription>
                </DialogHeader>
                <div className="min-h-0 space-y-5 overflow-y-auto px-6 py-5">
                    {failed && <SettingsFailureLine failure={{ text: t("given.failed", { assistant }), retry: load }} />}
                    {view && (
                        <>
                            <Group title={t("given.sense")} count={sense} removable={view.sense.every((h) => h.removable)} byHour={view.sense.some((h) => !h.removable)} assistant={assistant}>
                                {view.stretches.map((s) => {
                                    const parts = [];
                                    if (s.screen) parts.push(t("given.screen", { count: s.everyMinutes, m: s.everyMinutes }));
                                    if (s.wechat > 0) parts.push(t("given.wechat", { count: s.wechat, k: s.wechat }));
                                    if (s.calls > 0) parts.push(t("given.calls", { count: s.calls }));
                                    if (s.messages > 0) parts.push(t("given.messages", { count: s.messages }));
                                    return (
                                        <Row
                                            key={`s-${s.from}`}
                                            title={t("given.stretch", { date: dayName(s.from, locale), from: clock(s.from, locale), to: clock(s.to, locale) })}
                                            meta={t("given.times", { count: s.times, n: s.times })}
                                            from={t("given.from", { sources: parts.join(t("given.join")) })}
                                        />
                                    );
                                })}
                                {view.watch.map((w) => (
                                    <Row
                                        key={`w-${w.from}`}
                                        title={t("given.stretch", { date: dayName(w.from, locale), from: clock(w.from, locale), to: clock(w.to, locale) })}
                                        from={t("given.from", {
                                            sources: w.name
                                                ? t("given.watch", { title: w.name, length: length(w.from, w.to, t) })
                                                : t("given.watchHour", { hour: new Date(w.from).toLocaleTimeString(locale, { hour: "numeric" }), length: length(w.from, w.to, t) }),
                                        })}
                                    />
                                ))}
                                {view.sense.map((h) => (
                                    <HourRow key={`sense-${h.from}`} hour={h} locale={locale} />
                                ))}
                            </Group>
                            <Group
                                title={t("see.learning")}
                                count={view.about.length + view.passes.length}
                                removable={view.passes.length === 0 && view.about.every((h) => h.removable)}
                                byHour={view.about.some((h) => !h.removable)}
                                assistant={assistant}
                                after={view.passes.length > 0 ? <p className="text-[12px] text-muted" data-given-pass-note>{t("given.passNote")}</p> : undefined}
                            >
                                {view.about.map((h) => (
                                    <HourRow key={`about-${h.from}`} hour={h} locale={locale} onOpen={() => openTarget("memory:")} />
                                ))}
                                {view.passes.map((p) => (
                                    <li key={`pass-${p.pass}`} className="py-2.5 text-[13px] text-ink" data-given-row data-given-pass>
                                        {t("given.passRow", { name: t(`given.passes.${p.pass}`), times: t("given.times", { count: p.times, n: p.times }) })}
                                    </li>
                                ))}
                            </Group>
                            <Group title={t("see.work")} count={view.work.length} removable={view.work.every((h) => h.removable)} byHour={view.work.some((h) => !h.removable)} assistant={assistant}>
                                {view.work.map((h) => (
                                    <HourRow key={`work-${h.from}`} hour={h} locale={locale} />
                                ))}
                            </Group>
                            <Group title={t("given.briefs")} count={view.briefs.length} removable assistant={assistant}>
                                {view.briefs.map((b) => (
                                    <Row
                                        key={b.briefId}
                                        title={t("given.briefAt", { date: dayName(b.at, locale), time: clock(b.at, locale) })}
                                        from={t("given.from", { sources: givenSources(b.sources, t) })}
                                        action={<OpenLink onOpen={() => openTarget(`today:brief:${b.briefId}`)} />}
                                    />
                                ))}
                            </Group>
                            <Group title={t("given.answers")} count={view.answers.length} removable={view.answers.every((a) => a.removable)} assistant={assistant}>
                                {view.answers.map((a, i) => (
                                    <Row
                                        key={a.itemId || `a-${a.at}-${i}`}
                                        title={a.question ? t("given.question", { question: a.question }) : when(a.at, locale, t)}
                                        meta={a.question ? when(a.at, locale, t) : undefined}
                                        from={a.sources.length > 0 ? t("given.from", { sources: givenSources(a.sources, t) }) : t("given.fromQuestion")}
                                        action={a.itemId ? <OpenLink onOpen={() => openTarget(`main-chat-item:${a.itemId}`)} /> : undefined}
                                    />
                                ))}
                            </Group>
                            {view.proposals.length > 0 && (
                                <Group title={t("given.proposals")} count={view.proposals.length} removable assistant={assistant}>
                                    {view.proposals.map((p) => (
                                        <Row
                                            key={p.proposalId}
                                            title={p.title || when(p.at, locale, t)}
                                            meta={p.title ? when(p.at, locale, t) : undefined}
                                            from={t("given.from", { sources: t("given.kinds.topic_names") })}
                                        />
                                    ))}
                                </Group>
                            )}
                            {view.held > 0 && (
                                // Calls no group shows yet still read the day: say so, by count.
                                <p className="text-[12px] text-muted" data-given-held>{t("given.held", { count: view.held })}</p>
                            )}
                        </>
                    )}
                </div>
                <div className="flex justify-end border-t border-border px-6 py-4">
                    <Button size="sm" onClick={() => onOpenChange(false)}>
                        {t("given.done")}
                    </Button>
                </div>
            </DialogContent>
        </Dialog>
    );
}
