import { useCallback, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { SettingsCard, SettingsRow } from "@/components/settings/settings-primitives";
import { SettingsFailureLine } from "@/components/settings/settings-failure";
import { formatBytes } from "@/components/settings/settings-format";
import { VoiceMemoryCard } from "@/components/settings/voice-memory-card";
import { ForgetEverythingCard } from "@/components/settings/forget-everything-card";
import { Button } from "@/components/ui/button";
import type { SettingsPageId } from "@/components/settings/settings-registry";
import { DeleteAnswers, ExportAnswers, GetYourDataSummary, type YourDataSummary } from "@/lib/wails-shim-app";
import { ConfirmDialog } from "@/components/confirm-dialog";
import { useSettingsFailure } from "@/components/settings/settings-failure";
import { openSettingsPage, type OpenSettingsPage } from "./capture-shared";

type SourceKey = "screen" | "calls" | "mail" | "messages" | "watch" | "agentsHistory";

/** Settings › Your data (computer-data v1.0.0): what it keeps, by the six
 *  sources of What Isoform sees in its order, with keep times and sizes; the
 *  whole against the cap; Yours; and what leaves this computer this week.
 *  Every number comes from GetYourDataSummary. */
export function YourDataPage({ onOpenPage }: { onOpenPage?: OpenSettingsPage } = {}) {
    const { t, i18n } = useTranslation("settings");
    const [summary, setSummary] = useState<YourDataSummary | null>(null);
    const [loadFailed, setLoadFailed] = useState(false);
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
    const change = (page: SettingsPageId) => (
        <Button size="sm" variant="outline" onClick={() => openSettingsPage(page, onOpenPage)}>
            {t("yourData.change")}
        </Button>
    );
    const line = (...parts: Array<string | undefined>) => parts.filter(Boolean).join(" · ");
    const size = (bytes: number | undefined) => (bytes && bytes > 0 ? formatBytes(bytes) : undefined);

    const sources: Array<{ key: SourceKey; title: string; description: string; control?: React.ReactNode }> = [
        { key: "screen", title: t("yourData.screen"), description: line(summary ? t("yourData.screenSub", { count: summary.ignoredApps }) : t("yourData.screenSubPlain"), kept, size(summary?.screenBytes)), control: change("storage") },
        { key: "calls", title: t("yourData.calls"), description: line(t("yourData.callsSub"), kept, size(summary?.audioBytes)), control: change("storage") },
        { key: "mail", title: t("yourData.mail"), description: line(t("yourData.mailSub"), t("yourData.noSize")) },
        { key: "messages", title: t("yourData.messages"), description: line(t("yourData.messagesSub"), summary?.messagesNotes ? t("yourData.notes", { count: summary.messagesNotes }) : undefined, size(summary?.messagesBytes)), control: change("activity") },
        { key: "watch", title: t("yourData.watch"), description: line(t("yourData.watchSub"), t("yourData.keptUntilDeleted"), size(summary?.watchBytes)) },
        { key: "agentsHistory", title: t("yourData.agentsHistory"), description: line(t("yourData.agentsHistorySub"), summary?.agentsHistoryItems ? t("yourData.learned", { count: summary.agentsHistoryItems }) : undefined, size(summary?.agentsHistoryBytes)) },
    ];

    return (
        <>
            {loadFailed && <SettingsFailureLine failure={{ text: t("yourData.loadFailed"), retry: reload }} />}
            <SettingsCard title={t("yourData.keeps")}>
                {sources.map((source) => (
                    <div key={source.key} data-your-data-row={source.key}>
                        <SettingsRow title={source.title} description={source.description} control={source.control} />
                    </div>
                ))}
                {summary && summary.capBytes > 0 && (
                    <div data-your-data-row="total">
                        <SettingsRow
                            title={t("yourData.total", { used: formatBytes(summary.usedBytes), cap: formatBytes(summary.capBytes) })}
                            control={change("storage")}
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
            <SettingsCard title={t("yourData.leaves")}>
                <div data-your-data-row="agents">
                    <SettingsRow
                        title={t("yourData.agents")}
                        description={summary ? t("yourData.agentsSub", { count: summary.handoffsThisWeek }) : t("yourData.agentsSubPlain")}
                    />
                </div>
                <div data-your-data-row="devices">
                    <SettingsRow
                        title={t("yourData.devices")}
                        description={summary && summary.pairedDevices === 0 ? t("yourData.devicesNone") : t("yourData.devicesSub")}
                    />
                </div>
                <div data-your-data-row="notifications">
                    <SettingsRow title={t("yourData.notifications")} description={t("yourData.notificationsSub")} />
                </div>
            </SettingsCard>
            <ForgetEverythingCard />
        </>
    );
}
