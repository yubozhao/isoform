import { useCallback, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { SettingsCard, SettingsRow } from "@/components/settings/settings-primitives";
import { SettingsFailureLine } from "@/components/settings/settings-failure";
import { formatBytes } from "@/components/settings/settings-format";
import { VoiceMemoryCard } from "@/components/settings/voice-memory-card";
import { ForgetEverythingCard } from "@/components/settings/forget-everything-card";
import { KeepTimesSheet } from "@/components/settings/keep-times-sheet";
import { WhatLeavesCard } from "@/components/settings/what-leaves-card";
import { Button } from "@/components/ui/button";
import type { SettingsPageId } from "@/components/settings/settings-registry";
import { DeleteAnswers, ExportAnswers, GetYourDataSummary, type CaptureCountSource, type YourDataSummary } from "@/lib/wails-shim-app";
import { useCaptureCounts } from "@/hooks/useCaptureCounts";
import { ConfirmDialog } from "@/components/confirm-dialog";
import { useSettingsFailure } from "@/components/settings/settings-failure";
import { openSettingsPage, type OpenSettingsPage } from "./capture-shared";
import { getWorkProfile } from "@/lib/work-profile";
import type { TFunction } from "i18next";

/** An agent's name in "this week {n}: {agent} {k}, …"; the app's own
 *  assistant (no agent pinned) is "the AI you chose". */
function handoffAgentName(agent: string, t: TFunction<"settings">): string {
    switch (agent) {
        case "claude-code":
            return t("assistantChoice.agents.claudeCode");
        case "codex":
            return t("assistantChoice.agents.codex");
        case "deepseek":
            return t("assistantChoice.agents.deepseek");
        default:
            return t("yourData.agentOwn");
    }
}

type SourceKey = "screen" | "calls" | "mail" | "messages" | "watch" | "agentsHistory";

/** What Isoform captured, today and in the last seven days, on the source row
 *  it came from (answers v1.5.0 T16: the captured-counts card went from What
 *  Isoform sees). A kind Go adds later stays hidden until it gets a row. */
const COUNTS_BY_SOURCE: Partial<Record<SourceKey, CaptureCountSource[]>> = {
    screen: ["screens", "textRead", "typing"],
    calls: ["audioMinutes", "meetings"],
    mail: ["calendarEvents", "calendarPeeks"],
};

/** Settings › Your data (computer-data v1.0.0): what it keeps, by the six
 *  sources of What Isoform sees in its order, with keep times and sizes; the
 *  whole against the cap; Yours; and what leaves this computer this week.
 *  Every number comes from GetYourDataSummary. */
export function YourDataPage({ onOpenPage }: { onOpenPage?: OpenSettingsPage } = {}) {
    const { t, i18n } = useTranslation("settings");
    const [summary, setSummary] = useState<YourDataSummary | null>(null);
    const [loadFailed, setLoadFailed] = useState(false);
    // The claims Agent of You can say: the approved work profile's lines.
    // Approving is what sends them, so the row shows once one is approved
    // (computer-data v1.0.0 rule 5). Read on this computer, never from the
    // server.
    const [aoyClaims, setAoyClaims] = useState<number | null>(null);
    // A failed read shows its failure with Try again, never a hidden row:
    // without it, the page can't say whether Agent of You was sent anything.
    const [aoyFailed, setAoyFailed] = useState(false);
    const { failure: exportFailure, fail: failExport } = useSettingsFailure();
    const exportAnswers = () => {
        ExportAnswers().catch(() => failExport(t("yourData.exportFailed"), exportAnswers));
    };
    // Delete your answers (rule 4): the record and what was learned from it go;
    // each item keeps its state, without Undo.
    const [askingDelete, setAskingDelete] = useState(false);
    const [deleting, setDeleting] = useState(false);
    const deleteAnswers = () => {
        setDeleting(true);
        DeleteAnswers()
            .then(() => reload())
            .catch(() => failExport(t("yourData.deleteFailed"), deleteAnswers))
            .finally(() => setDeleting(false));
    };

    const reload = useCallback(() => {
        GetYourDataSummary()
            .then((next) => {
                setSummary(next);
                setLoadFailed(false);
            })
            .catch(() => setLoadFailed(true));
    }, []);
    const loadAoy = useCallback(() => {
        getWorkProfile()
            .then((view) => {
                const lines = view?.approvedVersion ? view.approved?.lines ?? [] : null;
                setAoyClaims(lines ? lines.filter((line) => !line.removed && !line.filtered).length : null);
                setAoyFailed(false);
            })
            .catch(() => {
                setAoyClaims(null);
                setAoyFailed(true);
            });
    }, []);
    useEffect(() => {
        loadAoy();
    }, [loadAoy]);
    useEffect(() => {
        reload();
        window.addEventListener("focus", reload);
        return () => window.removeEventListener("focus", reload);
    }, [reload]);

    const kept = summary
        ? summary.retentionDays > 0
            ? t("yourData.keptDays", { count: summary.retentionDays })
            : t("yourData.keptUntilDeleted")
        : undefined;
    // Change on a keep time or the total opens the keep-times sheet here
    // (settings batch D2: Storage went); Change on Messages opens its source.
    const [changing, setChanging] = useState(false);
    const change = (page?: SettingsPageId) => (
        <Button size="sm" variant="outline" onClick={() => (page ? openSettingsPage(page, onOpenPage) : setChanging(true))}>
            {t("yourData.change")}
        </Button>
    );
    const turnOn = (page: SettingsPageId, anchor?: string) => (
        <Button size="sm" variant="outline" onClick={() => openSettingsPage(page, onOpenPage, anchor ? { anchor } : undefined)}>
            {t("yourData.turnOn")}
        </Button>
    );
    // A source whose switch is off says Off and offers Turn on, never a line
    // about what it keeps (computer-data v1.0.0 rule 2).
    const off = (key: "callsOn" | "messagesOn" | "agentsHistoryOn") => summary?.[key] === false;
    // Read but unknown: the switch's own read failed. The row says so, with
    // Try again, and claims neither on nor off.
    const unknown = (key: "callsOn" | "messagesOn" | "agentsHistoryOn") => summary !== null && summary[key] === undefined;
    const switchFailure = { text: t("yourData.switchReadFailed"), retry: () => reload() };
    const line = (...parts: Array<string | undefined>) => parts.filter(Boolean).join(" · ");
    // Numbers only once activity.db answers; while observing is paused they
    // still say what was kept (the card's "Paused" line went with it).
    const captured = useCaptureCounts();
    const countFormat = new Intl.NumberFormat(i18n.language);
    const countLines = (key: SourceKey): string[] => {
        const kinds = COUNTS_BY_SOURCE[key];
        if (!kinds || !captured.counts || captured.counts.notReady) return [];
        return (captured.counts.sources ?? [])
            .filter((row) => kinds.includes(row.source) && row.week > 0)
            .map((row) =>
                t("yourData.counted", {
                    kind: t(`yourData.countKinds.${row.source}` as never),
                    today: countFormat.format(row.today),
                    week: countFormat.format(row.week),
                }),
            );
    };
    const size = (bytes: number | undefined) => (bytes && bytes > 0 ? formatBytes(bytes) : undefined);

    const byAgent = Object.entries(summary?.handoffsByAgent ?? {})
        .filter(([, count]) => count > 0)
        .sort((a, b) => b[1] - a[1])
        .map(([agent, count]) => t("yourData.agentCount", { agent: handoffAgentName(agent, t), count }))
        .join(", ");
    const agentsLine = summary && summary.handoffsThisWeek > 0
        ? t("yourData.agentsSub", { count: summary.handoffsThisWeek, counts: byAgent })
        : t("yourData.agentsSubPlain");

    const sources: Array<{ key: SourceKey; title: string; description: string; control?: React.ReactNode; failure?: { text: string; retry: () => void } }> = [
        { key: "screen", title: t("yourData.screen"), description: line(summary ? t("yourData.screenSub", { count: summary.ignoredApps }) : t("yourData.screenSubPlain"), kept, size(summary?.screenBytes)), control: change() },
        off("callsOn")
            ? { key: "calls", title: t("yourData.calls"), description: line(t("yourData.off"), size(summary?.audioBytes)), control: turnOn("activity", "sees-calls") }
            : { key: "calls", title: t("yourData.calls"), description: line(t("yourData.callsSub"), kept, size(summary?.audioBytes)), control: change() },
        { key: "mail", title: t("yourData.mail"), description: line(t("yourData.mailSub"), t("yourData.noSize")) },
        unknown("messagesOn")
            ? { key: "messages", title: t("yourData.messages"), description: line(summary?.messagesNotes ? t("yourData.notes", { count: summary.messagesNotes }) : undefined, size(summary?.messagesBytes)), failure: switchFailure }
            : off("messagesOn")
            ? { key: "messages", title: t("yourData.messages"), description: line(t("yourData.off"), summary?.messagesNotes ? t("yourData.notes", { count: summary.messagesNotes }) : undefined, size(summary?.messagesBytes)), control: turnOn("activity", "sees-messages") }
            : { key: "messages", title: t("yourData.messages"), description: line(t("yourData.messagesSub"), summary?.messagesNotes ? t("yourData.notes", { count: summary.messagesNotes }) : undefined, size(summary?.messagesBytes)), control: change("activity") },
        { key: "watch", title: t("yourData.watch"), description: line(t("yourData.watchSub"), t("yourData.keptUntilDeleted"), size(summary?.watchBytes)) },
        // The switch is on unless turned off (What Isoform sees › Your agents'
        // history), so the row says which it is.
        unknown("agentsHistoryOn")
            ? { key: "agentsHistory", title: t("yourData.agentsHistory"), description: line(summary?.agentsHistoryItems ? t("yourData.learned", { count: summary.agentsHistoryItems }) : undefined, size(summary?.agentsHistoryBytes)), failure: switchFailure }
            : off("agentsHistoryOn")
            ? { key: "agentsHistory", title: t("yourData.agentsHistory"), description: line(t("yourData.off"), summary?.agentsHistoryItems ? t("yourData.learned", { count: summary.agentsHistoryItems }) : undefined, size(summary?.agentsHistoryBytes)), control: turnOn("activity", "sees-agents") }
            : { key: "agentsHistory", title: t("yourData.agentsHistory"), description: line(summary ? t("yourData.agentsHistoryOnSub") : undefined, summary?.agentsHistoryItems ? t("yourData.learned", { count: summary.agentsHistoryItems }) : undefined, size(summary?.agentsHistoryBytes)) },
    ];

    return (
        <>
            {loadFailed && <SettingsFailureLine failure={{ text: t("yourData.loadFailed"), retry: reload }} />}
            <SettingsCard title={t("yourData.keeps")}>
                {sources.map((source) => {
                    const lines = countLines(source.key);
                    return (
                        <div key={source.key} data-your-data-row={source.key}>
                            <SettingsRow
                                title={source.title}
                                description={lines.length > 0 ? (
                                    <>
                                        {source.description}
                                        {lines.map((text, index) => (
                                            <span key={index} className="block" data-your-data-counts={source.key}>{text}</span>
                                        ))}
                                    </>
                                ) : source.description}
                                control={source.control}
                                failure={source.failure}
                            />
                        </div>
                    );
                })}
                {captured.loadFailed && (
                    <SettingsFailureLine failure={{ text: t("yourData.countsFailed"), retry: () => captured.refresh() }} />
                )}
                {summary && summary.capBytes > 0 && (
                    <div data-your-data-row="total">
                        <SettingsRow
                            title={t("yourData.total", { used: formatBytes(summary.usedBytes), cap: formatBytes(summary.capBytes) })}
                            control={change()}
                        />
                    </div>
                )}
            </SettingsCard>
            <SettingsCard title={t("yourData.yours")}>
                <div data-your-data-row="answers">
                    <SettingsRow
                        title={summary && summary.answers > 0 && summary.answersSince
                            ? t("yourData.answersSince", {
                                count: summary.answers,
                                date: new Date(summary.answersSince).toLocaleDateString(i18n.language, { month: "long", day: "numeric" }),
                            })
                            : t("yourData.answersNone")}
                        description={t("yourData.answersKept")}
                        failure={exportFailure}
                        control={summary && summary.answers > 0 ? (
                            <span className="flex gap-2">
                                <Button size="sm" variant="outline" aria-label={t("yourData.exportLabel")} onClick={exportAnswers}>
                                    {t("yourData.export")}
                                </Button>
                                <Button size="sm" variant="outline" disabled={deleting} aria-label={t("yourData.deleteLabel")} onClick={() => setAskingDelete(true)}>
                                    {t("yourData.delete")}
                                </Button>
                            </span>
                        ) : undefined}
                    />
                    <ConfirmDialog
                        open={askingDelete}
                        onOpenChange={setAskingDelete}
                        title={t("yourData.deleteConfirmTitle")}
                        description={t("yourData.deleteConfirm", { count: summary?.answers ?? 0 })}
                        confirmLabel={t("yourData.deleteConfirmButton")}
                        variant="danger"
                        onConfirm={deleteAnswers}
                    />
                </div>
            </SettingsCard>
            <VoiceMemoryCard />
            <WhatLeavesCard
                summary={summary}
                reload={reload}
                agentsLine={agentsLine}
                agentNames={(agent) => handoffAgentName(agent, t)}
                extraRows={
                    <>
                        <div data-your-data-row="devices">
                            <SettingsRow
                                title={t("yourData.devices")}
                                description={summary && summary.pairedDevices === 0 ? t("yourData.devicesNone") : t("yourData.devicesSub")}
                            />
                        </div>
                        <div data-your-data-row="notifications">
                            <SettingsRow title={t("yourData.notifications")} description={t("yourData.notificationsSub")} />
                        </div>
                        {(aoyClaims !== null || aoyFailed) && (
                            <div data-your-data-row="agentOfYou">
                                <SettingsRow
                                    title={t("yourData.aoy")}
                                    description={aoyClaims !== null ? t("yourData.aoySub", { count: aoyClaims }) : undefined}
                                    failure={aoyFailed ? { text: t("yourData.aoyReadFailed"), retry: loadAoy } : undefined}
                                />
                            </div>
                        )}
                    </>
                }
            />
            <ForgetEverythingCard />
            <KeepTimesSheet
                open={changing}
                onOpenChange={(open) => {
                    setChanging(open);
                    if (!open) reload();
                }}
            />
        </>
    );
}
