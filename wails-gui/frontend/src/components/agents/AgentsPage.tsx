import { useCallback, useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { FileText, Plus } from "lucide-react";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import { markPageContent } from "@/lib/page-switch-timing";
import {
    DisconnectAgent,
    DisconnectAgentHere,
    GetConnectedAgents,
    GetTaskLinkResults,
    GetWorkOnItHandoffs,
    GetYourAgents,
    OpenAgentFile,
    SetYourAgent,
    type ConnectedAgent,
    type TaskLinkResult,
    type WorkOnItHandoff,
    type YourAgents,
} from "@/lib/wails-shim-app";
import { agentName, agentRows, madeThisWeek, todayTimeline, type AgentRow, type TimelineEntry } from "./agents-model";
import { ConnectAgentDialog } from "./ConnectAgentDialog";

interface Snapshot {
    agents: YourAgents;
    connected: ConnectedAgent[];
    handoffs: WorkOnItHandoff[];
    results: TaskLinkResult[];
}

const sectionLabel = "text-[11px] font-semibold uppercase tracking-wide text-muted select-none";
const card = "rounded-xl border border-border bg-panel";

function initials(name: string): string {
    const letters = name.replace(/[^\p{L}\p{N} ]/gu, "").trim();
    return (letters.slice(0, 2) || "?").replace(/^./, (c) => c.toUpperCase());
}

function clock(at: number, language: string): string {
    return new Date(at).toLocaleTimeString(language, { hour: "numeric", minute: "2-digit" });
}

/**
 * Agents (computer-agents v1.0.0): your agents, what passed between you and
 * them today, and what they made. Requests between agents (C2) and what
 * Isoform learned from them (C4) are not built yet, so they are not shown.
 */
export function AgentsPage({ now = () => new Date(), onOpenTasks }: { now?: () => Date; onOpenTasks?: () => void }) {
    const { t, i18n } = useTranslation("app");
    const [snapshot, setSnapshot] = useState<Snapshot | null>(null);
    const [failed, setFailed] = useState(false);
    const [selected, setSelected] = useState<string | null>(null);
    const [connectOpen, setConnectOpen] = useState(false);
    const [actionFailed, setActionFailed] = useState(false);

    const reload = useCallback(async () => {
        try {
            const [agents, connected, handoffs, results] = await Promise.all([GetYourAgents(), GetConnectedAgents(), GetWorkOnItHandoffs(), GetTaskLinkResults()]);
            setSnapshot({ agents, connected: connected ?? [], handoffs: handoffs ?? [], results: results ?? [] });
            setFailed(false);
        } catch {
            setFailed(true);
        }
    }, []);

    useEffect(() => {
        void reload();
    }, [reload]);
    useEffect(() => {
        if (snapshot) markPageContent("agents");
    }, [snapshot]);

    const rows = useMemo(() => (snapshot ? agentRows(snapshot.agents, snapshot.connected, snapshot.handoffs, snapshot.results) : []), [snapshot]);
    const selectedRow = rows.find((r) => r.id === selected);
    const timeline = useMemo(() => (snapshot ? todayTimeline(snapshot.handoffs, snapshot.results, now(), selectedRow) : []), [snapshot, selectedRow, now]);
    const made = useMemo(() => (snapshot ? madeThisWeek(snapshot.results, now()) : []), [snapshot, now]);
    const nameOf = (id: string) => (snapshot ? agentName(id, snapshot.agents, snapshot.connected) : id);

    const act = async (action: () => Promise<unknown>) => {
        try {
            await action();
            setActionFailed(false);
        } catch {
            setActionFailed(true);
        }
        await reload();
    };

    return (
        <div className="flex flex-1 min-h-0 flex-col overflow-y-auto" data-agents-page>
            <div className="mx-auto flex w-full max-w-[1080px] flex-col gap-5 px-8 pt-8 pb-12">
                <header className="flex items-start justify-between gap-4">
                    <div className="flex flex-col gap-1">
                        <h1 className="text-[26px] font-normal leading-[1.3] text-ink">{t("agents.title")}</h1>
                        <p className="text-[14px] text-muted">{t("agents.sub")}</p>
                    </div>
                    {onOpenTasks && (
                        <Button variant="quiet" size="sm" onClick={onOpenTasks} data-agents-open-tasks>{t("agents.allTasks")}</Button>
                    )}
                </header>
                {failed && (
                    <div className="flex items-center gap-3 text-[13px] text-error" data-agents-failed>
                        {t("agents.failed")}
                        <Button variant="quiet" size="sm" onClick={() => void reload()}>{t("agents.retry")}</Button>
                    </div>
                )}
                {actionFailed && <p className="text-[13px] text-error" data-agents-action-failed>{t("agents.actionFailed")}</p>}
                <div className="grid grid-cols-1 gap-6 lg:grid-cols-[252px_minmax(0,1fr)_240px]">
                    <section aria-label={t("agents.yours")} className="flex flex-col gap-2.5">
                        <h2 className={sectionLabel}>{t("agents.yours")}</h2>
                        {snapshot && rows.length === 0 ? (
                            <p className="text-[13px] text-muted" data-agents-empty>{t("agents.empty.agents")}</p>
                        ) : (
                            rows.length > 0 && (
                                <ul className={cn(card, "flex flex-col px-3.5 py-1")}>
                                    {rows.map((row, i) => (
                                        <li key={row.id} className={cn(i > 0 && "border-t border-border")}>
                                            <AgentRowButton row={row} active={row.id === selected} onClick={() => setSelected(row.id === selected ? null : row.id)} />
                                        </li>
                                    ))}
                                </ul>
                            )
                        )}
                        <Button variant="link" className="h-auto self-start px-1 py-0.5 text-[14px] no-underline hover:underline" onClick={() => setConnectOpen(true)} data-agents-connect>
                            <Plus className="size-4" />
                            {t("agents.connect")}
                        </Button>
                        {selectedRow && (
                            <div className="flex flex-col items-start gap-1" data-agents-selected-actions>
                                {(selectedRow.connectionId || selectedRow.connectedHere) && (
                                    <Button variant="quiet" size="sm" onClick={() => void act(async () => {
                                        if (selectedRow.connectedHere) await DisconnectAgentHere(selectedRow.id);
                                        if (selectedRow.connectionId) await DisconnectAgent(selectedRow.connectionId);
                                        setSelected(null);
                                    })}>
                                        {t("agents.disconnect")}
                                    </Button>
                                )}
                                {!selectedRow.chosenFor && !selectedRow.id.startsWith("ag_") && (
                                    <Button variant="quiet" size="sm" onClick={() => void act(() => SetYourAgent(selectedRow.kind, selectedRow.id))}>
                                        {selectedRow.kind === "code" ? t("agents.makeCode") : t("agents.makeElse")}
                                    </Button>
                                )}
                            </div>
                        )}
                    </section>

                    <section aria-label={t("agents.between")} className="flex min-w-0 flex-col gap-1">
                        <div className="flex items-baseline justify-between gap-3">
                            <h2 className={sectionLabel}>{t("agents.between")}</h2>
                            {selectedRow && (
                                <Button variant="quiet" size="sm" onClick={() => setSelected(null)} data-agents-show-all>{t("agents.showAll")}</Button>
                            )}
                        </div>
                        {snapshot && timeline.length === 0 && <p className="pt-1 text-[13px] text-muted" data-agents-timeline-empty>{t("agents.empty.between")}</p>}
                        <ol className="flex flex-col">
                            {timeline.map((entry) => (
                                <TimelineItem key={entry.key} entry={entry} name={nameOf(entry.agentId)} language={i18n.language} onOpenFile={(linkId, file) => void act(() => OpenAgentFile(linkId, file))} />
                            ))}
                        </ol>
                    </section>

                    <section aria-label={t("agents.made")} className="flex flex-col gap-2.5">
                        <div className="flex items-baseline justify-between">
                            <h2 className={sectionLabel}>{t("agents.made")}</h2>
                            <span className="text-[12.5px] text-muted">{t("agents.thisWeek")}</span>
                        </div>
                        {snapshot && made.length === 0 ? (
                            <p className="text-[13px] text-muted" data-agents-made-empty>{t("agents.empty.made")}</p>
                        ) : (
                            made.length > 0 && (
                                <ul className={cn(card, "flex flex-col px-3.5 py-1")}>
                                    {made.map((file, i) => (
                                        <li key={file.key} className={cn(i > 0 && "border-t border-border")}>
                                            <button type="button" className="flex w-full items-center gap-2.5 py-2 text-left" onClick={() => void act(() => OpenAgentFile(file.linkId, file.name))}>
                                                <FileText className="size-4 shrink-0 text-icon-subtle" />
                                                <span className="min-w-0 flex-1">
                                                    <span className="block truncate text-[13.5px] text-ink">{file.name}</span>
                                                    <span className="block text-[12px] text-muted">{nameOf(file.agentId)}</span>
                                                </span>
                                            </button>
                                        </li>
                                    ))}
                                </ul>
                            )
                        )}
                    </section>
                </div>
            </div>
            <ConnectAgentDialog
                open={connectOpen}
                onOpenChange={(open) => {
                    setConnectOpen(open);
                    if (!open) void reload();
                }}
                agents={snapshot?.agents ?? null}
                onConnected={() => void reload()}
            />
        </div>
    );
}

function AgentRowButton({ row, active, onClick }: { row: AgentRow; active: boolean; onClick: () => void }) {
    const { t } = useTranslation("app");
    return (
        <button
            type="button"
            aria-pressed={active}
            onClick={onClick}
            data-agent-row={row.id}
            className={cn("-mx-2 my-1 flex w-[calc(100%+16px)] items-center gap-2.5 rounded-lg px-2 py-2 text-left transition-colors hover:bg-surface-hover", active && "bg-surface-active")}
        >
            <span aria-hidden className="flex size-7 shrink-0 items-center justify-center rounded-lg bg-surface-active text-[11.5px] font-semibold text-ink">{initials(row.name)}</span>
            <span className="min-w-0 flex-1">
                <span className="flex items-center gap-1.5 text-[14px] font-semibold text-ink">
                    <span className="truncate">{row.name}</span>
                    {row.working && <span className="size-2 shrink-0 rounded-full bg-success" aria-label={t("agents.workingDot")} />}
                </span>
                <span className="block text-[12px] text-muted">{t(`agents.job.${row.job}`)}</span>
                {row.working && <span className="mt-0.5 block truncate text-[12px] text-muted">{t("agents.working", { title: row.working })}</span>}
            </span>
        </button>
    );
}

function TimelineItem({ entry, name, language, onOpenFile }: { entry: TimelineEntry; name: string; language: string; onOpenFile: (linkId: string, file: string) => void }) {
    const { t } = useTranslation("app");
    const [showText, setShowText] = useState(false);
    return (
        <li className="flex gap-3" data-agents-entry={entry.type}>
            <span className="w-[58px] shrink-0 pt-1.5 text-right text-[12px] text-muted">{clock(entry.at, language)}</span>
            <div className="flex min-w-0 flex-1 flex-col gap-2 border-l-2 border-border pt-1 pb-3 pl-3.5">
                {entry.type === "handoff" ? (
                    <>
                        <span className="text-[12.5px] text-muted">{t("agents.youTo", { agent: name })}</span>
                        <p className="text-[14.5px] leading-normal text-ink">
                            {t("agents.yesOn", { title: entry.title })}{" "}
                            <button type="button" className="text-accent hover:underline" aria-expanded={showText} onClick={() => setShowText((v) => !v)}>
                                {t("agents.whatWent")}
                            </button>
                        </p>
                        {entry.taskState === "done" && (
                            <p className="text-[14.5px] leading-normal text-ink" data-agents-task-back>
                                <span className="block text-[12.5px] text-muted">{t("agents.backFromTask", { agent: name })}</span>
                                {entry.taskResult}
                            </p>
                        )}
                        {entry.taskState === "failed" && <p className="text-[12.5px] text-muted" data-agents-task-failed>{t("agents.notFinished", { agent: name })}</p>}
                        {showText && <pre className="max-h-64 overflow-auto whitespace-pre-wrap rounded-lg border border-border bg-panel px-3 py-2 font-sans text-[13px] leading-relaxed text-ink" data-agents-what-went>{entry.text}</pre>}
                    </>
                ) : (
                    <>
                        <span className="text-[12.5px] text-muted">
                            {t("agents.isBack", { agent: name })}
                            {entry.status !== "done" && <> · {t(`agents.status.${entry.status}`)}</>}
                        </span>
                        {entry.summary && <p className="text-[14.5px] leading-normal text-ink">{entry.summary}</p>}
                        {entry.files.map((file) => (
                            <button key={file} type="button" onClick={() => onOpenFile(entry.linkId, file)} className="inline-flex items-center gap-2 self-start rounded-lg border border-border px-2.5 py-1.5 text-[13px] text-ink hover:bg-surface-hover">
                                <FileText className="size-4 text-icon-subtle" />
                                {file}
                            </button>
                        ))}
                    </>
                )}
            </div>
        </li>
    );
}
