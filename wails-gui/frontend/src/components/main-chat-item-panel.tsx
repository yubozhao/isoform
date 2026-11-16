import { measureToday, type TodayActionKind, type TodayActionOutcome } from "@/lib/today-telemetry";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { X } from "lucide-react";
import { Button } from "@/components/ui/button";
import { MainChatProactiveCard, retiredBriefCopy } from "@/components/main-chat-proactive-card";
import { useMainChatItemPanel, type MainChatItemPanelSurface } from "@/hooks/useMainChatItemPanel";
import { DesktopDraftTaskID, OpenDeliverableFile, OpenExternalURL, ReturnToMainChatPlace, type DesktopTaskThread } from "@/lib/wails-shim-app";
import { getMainChatProactive, type MainChatReportTarget } from "@/lib/stream-helpers";
import { TaskThreadPane } from "@/components/tasks/TaskThreadPane";
import { FindTimeAnswer } from "@/components/find-time-answer";
import { HoldCard } from "@/components/today/HoldCard";
import { computerClock } from "@/lib/brief-time";
import { briefTitle } from "@/lib/brief-title";

export function MainChatItemPanel({
    surface,
    onOpenTarget,
}: {
    surface: MainChatItemPanelSurface;
    onOpenTarget?: (target: MainChatReportTarget) => void;
}) {
    const { t, i18n } = useTranslation("chat");
    const { t: tToday } = useTranslation("today");
    const track = <T,>(kind: TodayActionKind, outcome: TodayActionOutcome, fn: () => T): T => surface === "today" ? measureToday("D01", kind, outcome, fn) : fn();
    const [openFailed, setOpenFailed] = useState(false);
    const panel = useMainChatItemPanel(surface);
    const message = panel.item ? getMainChatProactive(panel.item) : null;
    const [task, setTask] = useState<{ moment: string; id: string } | null>(null);

    useEffect(() => {
        let active = true;
        setTask(null);
        if (!message) return () => { active = false; };
        if (message.work?.handoffId) setTask({ moment: message.momentId, id: message.work.handoffId });
        else if (message.kind === "waiting_on_reply" && message.draftContent) {
            void Promise.resolve().then(() => DesktopDraftTaskID(message.momentId)).then((id) => { if (active && typeof id === "string" && id) setTask({ moment: message.momentId, id }); }).catch(() => {});
        }
        return () => { active = false; };
    }, [message?.momentId, message?.kind, message?.work?.handoffId, Boolean(message?.draftContent)]);

    useEffect(() => { setOpenFailed(false); }, [panel.openId]);

    useEffect(() => {
        if (!panel.openId) return;
        const onKeyDown = (event: KeyboardEvent) => {
            if (event.key === "Escape" && !event.defaultPrevented) panel.close();
        };
        window.addEventListener("keydown", onKeyDown);
        return () => window.removeEventListener("keydown", onKeyDown);
    }, [panel.openId, panel.close]);

    if (!panel.openId) return null;
    if (!panel.item || !message) {
        return (
            <aside
                data-testid="main-chat-item-panel"
                data-panel-open={panel.openId}
                className="flex min-h-0 shrink-0 flex-col gap-3 overflow-y-auto border-l border-border-subtle bg-background-accent px-4 py-4"
                style={{ width: 420 }}
            >
                <header className="flex items-center justify-between gap-3">
                    <h2 className="min-w-0 truncate text-[14px] font-semibold text-ink">{t("mainChat.proactive.revalidating")}</h2>
                    <Button size="icon" variant="quiet" aria-label={t("mainChat.proactive.closePanel")} onClick={() => track("open", "opened", panel.close)}>
                        <X className="size-4" />
                    </Button>
                </header>
                <p role="status" className="text-[13px] text-muted">{t("mainChat.proactive.revalidating")}</p>
            </aside>
        );
    }

    const selectedItem = panel.item;
    const briefHeader = message.kind === 'first_week' && message.briefId && message.briefDisplay
        ? briefTitle(message.briefDisplay as Parameters<typeof briefTitle>[0], i18n.resolvedLanguage ?? i18n.language, (key, options) => String(tToday(key as never, options as never)))
        : (message.kind === 'brief_ready' || message.kind === 'weekly_recap') && retiredBriefCopy(message.headline)
        ? message.briefDisplay
            ? briefTitle(message.briefDisplay as Parameters<typeof briefTitle>[0], i18n.resolvedLanguage ?? i18n.language, (key, options) => tToday(key as never, options as never) as unknown as string)
            : tToday(message.kind === 'brief_ready' ? 'todayBrief.slot.on_request' : 'todayBrief.kind.week')
        : message.localized?.[(i18n.resolvedLanguage ?? i18n.language)?.toLowerCase().startsWith("zh") ? "zh-CN" : "en"]?.headline ?? message.headline;
    const openTarget = (target: MainChatReportTarget) => {
        if (target.kind === "calendar") {
            void OpenExternalURL("/System/Applications/Calendar.app");
            return;
        }
        onOpenTarget?.(target);
    };
    const returnToPlace = async () => {
        try {
            if (await ReturnToMainChatPlace(message.momentId)) {
                panel.close();
                return;
            }
        } catch {
            panel.setPlaceFailed(true);
            return;
        }
        panel.setPlaceFailed(true);
    };
    const time = (value?: string) => {
        if (!value) return "";
        const date = new Date(value);
        return Number.isNaN(date.getTime())
            ? value
            : computerClock(date, i18n.resolvedLanguage);
    };
    const events = message.events ?? (message.event ? [message.event] : []);
    const destination = message.target.kind === "person"
        ? { label: t("mainChat.card.button.showName", { name: message.personName ?? message.headline }), target: message.target }
        : message.target.kind === "playbook"
          ? { label: t("mainChat.card.button.review"), target: message.target, foot: t("mainChat.proactive.howYouDoThings") }
          : message.target.kind === "automation" || message.target.kind === "upcoming"
            ? { label: t("mainChat.proactive.openInUpcoming"), target: { kind: "upcoming" as const } }
            : null;

    const panelDetails = (thread?: DesktopTaskThread | null, titleInHeader = false) => message.kind === "commitment_slipping" ? (message.holdSource ? <HoldCard source={{ ...message.holdSource, reason: message.body, ...(surface === "today" ? { telemetryAction: "D01" as const } : {}) }} onClose={panel.close} /> : <FindTimeAnswer />) : (<>
            {message.place?.bundleId && (
                <div className="flex flex-wrap items-center gap-2">
                    <Button size="sm" variant="secondary" onClick={() => void track("open", "opened", returnToPlace)}>
                        {t("mainChat.proactive.returnToPlace", { app: message.place.app || message.place.bundleId })}
                    </Button>
                    {panel.placeFailed && message.target.kind === "thread" && (
                        <Button size="sm" variant="quiet" onClick={() => track("open", "opened", () => onOpenTarget?.(message.target))}>
                            {t("mainChat.card.button.showThread")}
                        </Button>
                    )}
                </div>
            )}
            {message.target.kind === "thread" && (
                <Button size="sm" variant="secondary" className="self-start" onClick={() => track("open", "opened", () => onOpenTarget?.(message.target))}>
                    {t("mainChat.card.button.openInActivity")}
                </Button>
            )}
            {destination && (
                <Button size="sm" variant="secondary" className="self-start" onClick={() => track("open", "opened", () => onOpenTarget?.(destination.target))}>
                    {destination.label}
                </Button>
            )}
            {destination?.foot && <p className="text-[12px] text-muted">{destination.foot}</p>}
            {events.length > 0 && (
                <section className="flex flex-col gap-2 rounded-lg border border-border bg-panel-warm px-3 py-2.5" aria-label={t("mainChat.proactive.eventDetails")}>
                    {events.map((event, index) => (
                        <div key={`${event.start ?? "event"}-${index}`} className="flex flex-col gap-1">
                            {event.private ? (
                                <p className="text-[13px] font-medium text-ink">{t("mainChat.proactive.privateEvent")}</p>
                            ) : (
                                <>
                                    {event.title && <p className="break-words text-[13px] font-medium text-ink">{event.title}</p>}
                                    {event.place && <p className="break-words text-[12px] text-muted">{event.place}</p>}
                                    {event.people?.length && <p className="break-words text-[12px] text-muted">{event.people.join(", ")}</p>}
                                </>
                            )}
                            {(event.start || event.end) && (
                                <p className="text-[12px] text-muted">
                                    {t("mainChat.proactive.eventWhen", { start: time(event.start), end: time(event.end) })}
                                </p>
                            )}
                        </div>
                    ))}
                </section>
            )}
            {message.work && (message.work.result || message.work.deliverables.length > 0) && (
                <section className="flex flex-col gap-2 rounded-lg border border-border bg-panel-warm px-3 py-2.5" aria-label={t("mainChat.proactive.workDetails")}>
                    {openFailed && <p role="alert" className="text-xs text-subtle">{t("failure.openIt")}</p>}
                    {message.work.result && !thread?.events?.some((event) => event.text === message.work?.result) && <p className="whitespace-pre-wrap break-words text-[13px] leading-relaxed text-ink">{message.work.result}</p>}
                    {message.work.deliverables.length > 0 && (
                        <div className="flex flex-col items-start gap-1.5">
                            {message.work.deliverables.map((deliverable) => (
                                <Button
                                    key={`${deliverable.url}:${deliverable.label}`}
                                    size="sm"
                                    variant="link"
                                    className="h-auto px-0 text-left"
                                    onClick={() => {
                                        const open = track("open", "opened", () => /^https?:\/\//i.test(deliverable.url)
                                            ? OpenExternalURL(deliverable.url)
                                            : OpenDeliverableFile("handoff", message.work!.handoffId, deliverable.url));
                                        void open.catch(() => setOpenFailed(true));
                                    }}
                                >
                                    {t("mainChat.card.button.openFile", { file: deliverable.label })}
                                </Button>
                            ))}
                        </div>
                    )}
                </section>
            )}
            <MainChatProactiveCard item={selectedItem} onOpenTarget={openTarget} compact inPanel titleInHeader={titleInHeader} panelSurface={surface} />
    </>);

    if (task?.moment === message.momentId) return (
        <aside data-testid="main-chat-item-panel" data-panel-open={message.momentId} className="flex min-h-0 shrink-0 flex-col border-l border-border-subtle bg-background-accent" style={{ width: 420 }}>
            <div className="flex justify-end px-3 pt-2"><Button size="icon" variant="quiet" aria-label={t("mainChat.proactive.closePanel")} onClick={() => track("open", "opened", panel.close)}><X className="size-4" /></Button></div>
            <TaskThreadPane id={task.id} telemetryAction={surface === "today" ? "D01" : undefined} onClose={panel.close} renderSupplement={panelDetails} />
        </aside>
    );

    return (
        <aside
            data-testid="main-chat-item-panel"
            data-panel-open={message.momentId}
            className="flex min-h-0 shrink-0 flex-col gap-3 overflow-y-auto border-l border-border-subtle bg-background-accent px-4 py-4"
            style={{ width: 420 }}
        >
            <header className="flex items-center justify-between gap-3">
                <h2 className="min-w-0 truncate text-[14px] font-semibold text-ink">{briefHeader}</h2>
                <Button size="icon" variant="quiet" aria-label={t("mainChat.proactive.closePanel")} onClick={() => track("open", "opened", panel.close)}>
                    <X className="size-4" />
                </Button>
            </header>
            {panelDetails(null, true)}
        </aside>
    );
}
