import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { FileText, Plus } from "lucide-react";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import { markPageContent } from "@/lib/page-switch-timing";
import {
    AskAgentAgain,
    DisconnectAgent,
    DisconnectAgentHere,
    GetAssistantHistory,
    GetConnectedAgents,
    GetHandoffStatuses,
    GetTaskLinkResults,
    GetUsefulHandoffs,
    GetWorkOnItHandoffs,
    GetYourAgents,
    OpenAgentApp,
    OpenAgentFile,
    SetYourAgent,
    TakeHandoffBack,
    type AssistantHistoryView,
    type ConnectedAgent,
    type HandoffStatus,
    type TaskLinkResult,
    type WorkOnItHandoff,
    type WorkOnItKind,
    type YourAgents,
} from "@/lib/wails-shim-app";
import { agentName, agentRows, canLearnFrom, handoffTimeline, learnedLines, madeThisWeek, rowForAgent, todayTimeline, weekCounts, type AgentRow, type TimelineEntry } from "./agents-model";
import { AgentCard, agentDate, agentInitials } from "./AgentCard";
import { ConnectAgentDialog, ConnectAgentPanel } from "./ConnectAgentDialog";

interface Snapshot {
    agents: YourAgents;
    connected: ConnectedAgent[];
    handoffs: WorkOnItHandoff[];
    results: TaskLinkResult[];
    statuses: HandoffStatus[];
    /** Hand-offs whose result the person judged Useful. */
    useful: Set<string>;
}

/**
 * Opens one hand-off on Agents (conformance answers v1.2.0 G4): its agent's
 * card, the timeline narrowed to that hand-off with What went open, and
 * "‹ {backLabel}" back to where it came from. A new nonce applies it again.
 */
export interface OpenHandoff {
    id: string;
    nonce: number;
    backLabel: string;
    onBack: () => void;
}

/** States that say something about an agent's row, most urgent first. */
const ROW_STATES: HandoffStatus["state"][] = ["couldnt_start", "no_word", "working", "has_it", "opened"];

/** The status that speaks for an agent's row: its most urgent open hand-off. */
function rowStatus(row: AgentRow, statuses: HandoffStatus[]): HandoffStatus | undefined {
    const keys = row.connectionId ? [row.id, row.connectionId] : [row.id];
    const mine = statuses.filter((s) => keys.includes(s.agentId) && ROW_STATES.includes(s.state));
    mine.sort((a, b) => ROW_STATES.indexOf(a.state) - ROW_STATES.indexOf(b.state) || b.sentAt - a.sentAt);
    return mine[0];
}

function weekday(at: number, language: string): string {
    return new Date(at).toLocaleDateString(language, { weekday: "long" });
}

const sectionLabel = "text-[11px] font-semibold uppercase tracking-wide text-muted select-none";
const card = "rounded-xl border border-border bg-panel";

function clock(at: number, language: string): string {
    return new Date(at).toLocaleTimeString(language, { hour: "numeric", minute: "2-digit" });
}

/**
 * Agents (computer-agents v1.0.0): your agents, what passed between you and
 * them today, what they made, and what Isoform learned from the ones you
 * turned Learn from it on for. Requests between agents (C2) are not shown yet.
 */
export function AgentsPage({ now = () => new Date(), openHandoff, onOpenActivityAgents }: {
    now?: () => Date;
    openHandoff?: OpenHandoff;
    /** Opens Activity › Agents, where each Learned from them line leads. */
    onOpenActivityAgents?: () => void;
}) {
    const { t, i18n } = useTranslation("app");
    const [snapshot, setSnapshot] = useState<Snapshot | null>(null);
    const [failed, setFailed] = useState(false);
    const [selected, setSelected] = useState<string | null>(null);
    // One hand-off opened from elsewhere (G4); cleared when the person picks another view.
    const [focusHandoff, setFocusHandoff] = useState<string | null>(null);
    const [connectOpen, setConnectOpen] = useState(false);
    const [actionFailed, setActionFailed] = useState(false);
    const [takesOver, setTakesOver] = useState<{ kind: WorkOnItKind; current: string; agent: string } | null>(null);
    const [history, setHistory] = useState<AssistantHistoryView | null>(null);
    const [historyFailed, setHistoryFailed] = useState(false);
    const cardRef = useRef<HTMLElement>(null);
    const appliedNonce = useRef<number | null>(null);

    const reload = useCallback(async () => {
        try {
            const [agents, connected, handoffs, results, statuses, useful] = await Promise.all([GetYourAgents(), GetConnectedAgents(), GetWorkOnItHandoffs(), GetTaskLinkResults(), GetHandoffStatuses(), GetUsefulHandoffs()]);
            setSnapshot({ agents, connected: connected ?? [], handoffs: handoffs ?? [], results: results ?? [], statuses: statuses ?? [], useful: new Set(useful ?? []) });
            setFailed(false);
        } catch {
            setFailed(true);
        }
    }, []);

    // What Isoform read from agents' own history, for Learned from them; on its own, so a slow read holds up nothing else.
    const reloadHistory = useCallback(async () => {
        try {
            setHistory(await GetAssistantHistory());
            setHistoryFailed(false);
        } catch {
            setHistoryFailed(true);
        }
    }, []);

    useEffect(() => {
        void reload();
        void reloadHistory();
    }, [reload, reloadHistory]);
    useEffect(() => {
        if (snapshot) markPageContent("agents");
    }, [snapshot]);

    const rows = useMemo(() => (snapshot ? agentRows(snapshot.agents, snapshot.connected, snapshot.handoffs, snapshot.results, { disconnected: true }) : []), [snapshot]);
    const selectedRow = rows.find((r) => r.id === selected);
    const timeline = useMemo(() => {
        if (!snapshot) return [];
        if (focusHandoff) return handoffTimeline(snapshot.handoffs, snapshot.results, focusHandoff);
        return todayTimeline(snapshot.handoffs, snapshot.results, now(), selectedRow);
    }, [snapshot, selectedRow, focusHandoff, now]);
    const week = useMemo(() => (snapshot && selectedRow ? weekCounts(selectedRow, snapshot.handoffs, snapshot.results, snapshot.useful, now()) : null), [snapshot, selectedRow, now]);
    const learned = useMemo(() => (history ? learnedLines(rows, history.projects ?? []) : []), [history, rows]);
    const learnable = rows.filter((r) => canLearnFrom(r) && r.state !== "disconnected");
    const learnAllOff = !learnable.some((r) => r.learnFromIt);
    const made = useMemo(() => (snapshot ? madeThisWeek(snapshot.results, now()) : []), [snapshot, now]);
    const nameOf = (id: string) => (snapshot ? agentName(id, snapshot.agents, snapshot.connected) : id);
    const statusOf = (id: string) => snapshot?.statuses.find((s) => s.id === id);
    // With no agent connected and nothing handed off, the page is the connect
    // sheet, full size (computer-agents v1.1.0 13a).
    const noAgents = !!snapshot && !rows.some((r) => r.connected) && snapshot.handoffs.length === 0 && !openHandoff;

    const select = useCallback((id: string | null) => {
        setSelected(id);
        setFocusHandoff(null);
        setTakesOver(null);
    }, []);

    // Opening an agent moves focus to its card, so Tab goes on into it.
    useEffect(() => {
        if (selected) cardRef.current?.focus({ preventScroll: true });
    }, [selected]);

    // Escape closes the card (but not while one of its menus is open).
    useEffect(() => {
        if (!selected) return;
        const onKey = (e: KeyboardEvent) => {
            if (e.key !== "Escape" || e.defaultPrevented) return;
            if (document.activeElement?.closest("[role=menu]")) return;
            select(null);
        };
        window.addEventListener("keydown", onKey);
        return () => window.removeEventListener("keydown", onKey);
    }, [selected, select]);

    // G4: open the hand-off asked for, once per nonce, when the snapshot is in.
    useEffect(() => {
        if (!openHandoff || !snapshot || appliedNonce.current === openHandoff.nonce) return;
        appliedNonce.current = openHandoff.nonce;
        const handoff = snapshot.handoffs.find((h) => h.id === openHandoff.id);
        setSelected(handoff ? (rowForAgent(rows, handoff.agentId)?.id ?? null) : null);
        setTakesOver(null);
        setFocusHandoff(openHandoff.id);
    }, [openHandoff, snapshot, rows]);

    const changeJob = async (kind: WorkOnItKind, agentId: string) => {
        if (!snapshot || !selectedRow) return;
        const previous = snapshot.agents.choices[kind];
        const replaced = agentId && previous && previous !== agentId ? agentName(previous, snapshot.agents, snapshot.connected) : "";
        const ok = await act(() => SetYourAgent(kind, agentId));
        setTakesOver(ok && replaced ? { kind, current: replaced, agent: selectedRow.name } : null);
    };

    const act = async (action: () => Promise<unknown>) => {
        let ok = true;
        try {
            await action();
            setActionFailed(false);
        } catch {
            setActionFailed(true);
            ok = false;
        }
        await reload();
        return ok;
    };

    return (
        <div className="flex flex-1 min-h-0 flex-col overflow-y-auto" data-agents-page>
            <div className="mx-auto flex w-full max-w-[1080px] flex-col gap-5 px-8 pt-8 pb-12">
                {/* No All tasks: the Tasks page goes (computer-today v1.0.0 rule 9), and an
                    agent's own sessions aren't Isoform's to show (computer-agents v1.0.0 rule 16). */}
                {openHandoff && (
                    <Button variant="quiet" size="sm" className="-ml-2 self-start" onClick={openHandoff.onBack} data-agents-back>
                        {t("agents.back", { label: openHandoff.backLabel })}
                    </Button>
                )}
                <header className="flex flex-col gap-1">
                    <h1 className="text-[26px] font-normal leading-[1.3] text-ink">{t("agents.title")}</h1>
                    <p className="text-[14px] text-muted">{t("agents.sub")}</p>
                </header>
                {failed && (
                    <div className="flex items-center gap-3 text-[13px] text-error" data-agents-failed>
                        {t("agents.failed")}
                        <Button variant="quiet" size="sm" onClick={() => void reload()}>{t("agents.retry")}</Button>
                    </div>
                )}
                {actionFailed && <p className="text-[13px] text-error" data-agents-action-failed>{t("agents.actionFailed")}</p>}
                {noAgents ? (
                    <section className={cn(card, "mx-auto flex w-full max-w-[640px] flex-col gap-4 p-6")} aria-label={t("agents.emptyTitle")} data-agents-none>
                        <div className="flex flex-col gap-1.5">
                            <h2 className="text-[18px] font-semibold text-ink">{t("agents.emptyTitle")}</h2>
                            <p className="text-[13.5px] leading-relaxed text-muted">{t("agents.emptyBody")}</p>
                        </div>
                        <ConnectAgentPanel active={!connectOpen} agents={snapshot?.agents ?? null} onConnected={() => void reload()} />
                        <p className="text-[12.5px] text-muted">{t("agents.untilThen")}</p>
                    </section>
                ) : (
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
                                            <AgentRowButton row={row} status={rowStatus(row, snapshot?.statuses ?? [])} language={i18n.language} active={row.id === selected} onClick={() => select(row.id === selected ? null : row.id)} />
                                            {row.state === "disconnected" && (
                                                <Button variant="outline" size="sm" className="mb-2 ml-[38px]" onClick={() => setConnectOpen(true)} data-agents-connect-again={row.id}>
                                                    {t("agents.connectAgain")}
                                                </Button>
                                            )}
                                        </li>
                                    ))}
                                </ul>
                            )
                        )}
                        <Button variant="link" className="h-auto self-start px-1 py-0.5 text-[14px] no-underline hover:underline" onClick={() => setConnectOpen(true)} data-agents-connect>
                            <Plus className="size-4" />
                            {t("agents.connect")}
                        </Button>
                    </section>

                    <section aria-label={t("agents.between")} className="flex min-w-0 flex-col gap-1">
                        {/* One agent opened: its card sits beside the list, and the timeline under it narrows to it (13d). */}
                        {selectedRow && week && (
                            <div className="mb-4">
                                <AgentCard
                                    ref={cardRef}
                                    row={selectedRow}
                                    language={i18n.language}
                                    week={week}
                                    takesOver={takesOver}
                                    onClose={() => select(null)}
                                    onChangeJob={(kind, agentId) => void changeJob(kind, agentId)}
                                    onLearnChanged={(ok) => {
                                        setActionFailed(!ok);
                                        void reload();
                                        void reloadHistory();
                                    }}
                                    onDisconnect={() => void act(async () => {
                                        if (selectedRow.connectedHere) await DisconnectAgentHere(selectedRow.id);
                                        if (selectedRow.connectionId) await DisconnectAgent(selectedRow.connectionId);
                                    })}
                                />
                            </div>
                        )}
                        <h2 className={sectionLabel}>{t("agents.between")}</h2>
                        {snapshot && timeline.length === 0 && (
                            <div className="flex flex-col gap-0.5 pt-1" data-agents-timeline-empty>
                                <p className="text-[13px] text-muted">{t("agents.empty.between")}</p>
                                <p className="text-[12.5px] text-muted">{t("agents.quietTodaySub")}</p>
                            </div>
                        )}
                        <ol className="flex flex-col">
                            {timeline.map((entry) => (
                                <TimelineItem
                                    key={entry.key}
                                    entry={entry}
                                    status={entry.type === "handoff" ? statusOf(entry.key.slice(2)) : undefined}
                                    name={nameOf(entry.agentId)}
                                    opens={rowForAgent(rows, entry.agentId)?.opens ? rowForAgent(rows, entry.agentId)?.id : undefined}
                                    focused={!!focusHandoff && entry.key === `h-${focusHandoff}`}
                                    focusNonce={openHandoff?.nonce}
                                    language={i18n.language}
                                    onOpenAgent={(id) => void act(() => OpenAgentApp(id))}
                                    onOpenFile={(linkId, file) => void act(() => OpenAgentFile(linkId, file))}
                                    onAskAgain={(id) => void act(() => AskAgentAgain(id))}
                                    onTakeBack={(id) => void act(() => TakeHandoffBack(id))}
                                />
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

                        <h2 className={cn(sectionLabel, "mt-4")}>{t("agents.learned")}</h2>
                        <div className="flex flex-col gap-1.5" data-agents-learned>
                            {historyFailed ? (
                                <div className="flex items-center gap-2 text-[13px] text-error" data-agents-learned-failed>
                                    {t("agents.learn.failed")}
                                    <Button variant="quiet" size="sm" onClick={() => void reloadHistory()}>{t("agents.retry")}</Button>
                                </div>
                            ) : learnAllOff ? (
                                <p className="text-[13px] text-muted" data-agents-learn-off-all>
                                    {t("agents.learnOffAll")}{" "}
                                    {learnable[0] && (
                                        <button type="button" className="text-accent hover:underline" onClick={() => select(learnable[0].id)} data-agents-learn-choose>
                                            {t("agents.learnChoose")}
                                        </button>
                                    )}
                                </p>
                            ) : history && learned.length === 0 ? (
                                <p className="text-[13px] text-muted" data-agents-learned-none>{t("agents.learn.none")}</p>
                            ) : (
                                learned.length > 0 && (
                                    <ul className={cn(card, "flex flex-col px-3.5 py-1")}>
                                        {learned.map((line, i) => (
                                            <li key={line.key} className={cn(i > 0 && "border-t border-border")}>
                                                <button type="button" className="w-full py-2 text-left text-[13px] leading-snug text-ink disabled:cursor-default" disabled={!onOpenActivityAgents} onClick={onOpenActivityAgents} data-agents-learned-line>
                                                    {t("agents.learn.line", {
                                                        count: line.sessions,
                                                        project: line.project,
                                                        agents: line.agents.length > 1 ? t("agents.learn.and", { a: line.agents.slice(0, -1).join(", "), b: line.agents[line.agents.length - 1] }) : line.agents[0],
                                                    })}
                                                </button>
                                            </li>
                                        ))}
                                    </ul>
                                )
                            )}
                            <p className="text-[12px] text-muted">{t("agents.learnedFoot")}</p>
                        </div>
                    </section>
                </div>
                )}
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

function AgentRowButton({ row, status, language, active, onClick }: { row: AgentRow; status?: HandoffStatus; language: string; active: boolean; onClick: () => void }) {
    const { t } = useTranslation("app");
    // Disconnected fades: muted words, not lowered opacity (keeps AA contrast).
    const gone = row.state === "disconnected";
    const jobWord = row.chosenFor === "code" ? t("agents.job.forCode") : row.chosenFor === "else" ? t("agents.job.forElse") : "";
    const stateWord = t(`agents.state.${row.state}`);
    return (
        <button
            type="button"
            aria-pressed={active}
            onClick={onClick}
            data-agent-row={row.id}
            className={cn("-mx-2 my-1 flex w-[calc(100%+16px)] items-center gap-2.5 rounded-lg px-2 py-2 text-left transition-colors hover:bg-surface-hover", active && "bg-surface-active")}
        >
            <span aria-hidden className={cn("flex size-7 shrink-0 items-center justify-center rounded-lg text-[11.5px] font-semibold", gone ? "bg-surface-hover text-muted" : "bg-surface-active text-ink")}>{agentInitials(row.name)}</span>
            <span className="min-w-0 flex-1">
                <span className={cn("flex items-center gap-1.5 text-[14px] font-semibold", gone ? "text-muted" : "text-ink")}>
                    <span className="truncate">{row.name}</span>
                    {/* Green only while Isoform can see it working (computer-agents v1.1.0 rule 1). */}
                    {!gone && status?.state === "working" && <span className="size-2 shrink-0 rounded-full bg-success" aria-label={t("agents.workingDot")} />}
                </span>
                {gone ? (
                    <span className="block text-[12px] text-muted" data-agent-row-state="disconnected">
                        {row.disconnectedAt ? t("agents.disconnectedOn", { date: agentDate(row.disconnectedAt, language) }) : stateWord}
                    </span>
                ) : (
                    <>
                        {/* Connected / Not connected are the only states (agents-join v1.0.0 rule 3). */}
                        <span className="block text-[12px] text-muted" data-agent-row-state={row.state}>{jobWord ? t("agents.rowLine", { job: jobWord, state: stateWord }) : stateWord}</span>
                        <RowStatusLine status={status} lastWorkedAt={row.lastWorkedAt} language={language} />
                    </>
                )}
            </span>
        </button>
    );
}

function RowStatusLine({ status, lastWorkedAt, language }: { status?: HandoffStatus; lastWorkedAt?: number; language: string }) {
    const { t } = useTranslation("app");
    const line = "mt-0.5 block truncate text-[12px]";
    switch (status?.state) {
        case "couldnt_start":
            return <span className={cn(line, "text-warning")} data-agent-row-status="couldnt_start">{t("agents.couldntStart", { time: clock(status.sentAt, language) })}</span>;
        case "no_word":
            return <span className={cn(line, "text-warning")} data-agent-row-status="no_word">{t("agents.noWordSince", { time: clock(status.since ?? status.sentAt, language) })}</span>;
        case "working":
            return <span className={cn(line, "text-muted")} data-agent-row-status="working">{status.line ? t("agents.workingLine", { line: status.line }) : t("agents.working", { title: status.title })}</span>;
        case "has_it":
        case "opened":
            return <span className={cn(line, "text-muted")} data-agent-row-status="has_it">{t("agents.hasIt", { title: status.title })}</span>;
        default:
            return lastWorkedAt ? <span className={cn(line, "text-muted")}>{t("agents.lastWorked", { day: weekday(lastWorkedAt, language) })}</span> : null;
    }
}

function TimelineItem({ entry, status, name, opens, focused, focusNonce, language, onOpenAgent, onOpenFile, onAskAgain, onTakeBack }: {
    entry: TimelineEntry;
    status?: HandoffStatus;
    name: string;
    /** The agent's id for Open {agent}, when it has somewhere to open. */
    opens?: string;
    /** Opened from elsewhere (G4): What went shows and it scrolls into view. */
    focused?: boolean;
    focusNonce?: number;
    language: string;
    onOpenAgent: (agentId: string) => void;
    onOpenFile: (linkId: string, file: string) => void;
    onAskAgain: (id: string) => void;
    onTakeBack: (id: string) => void;
}) {
    const { t } = useTranslation("app");
    const [showText, setShowText] = useState(!!focused);
    const itemRef = useRef<HTMLLIElement>(null);
    useEffect(() => {
        if (!focused) return;
        setShowText(true);
        itemRef.current?.scrollIntoView?.({ block: "nearest" });
    }, [focused, focusNonce]);
    return (
        <li ref={itemRef} className="flex gap-3" data-agents-entry={entry.type} data-agents-focused={focused || undefined}>
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
                        {status && <HandoffStatusLine status={status} name={name} opens={opens} language={language} onOpenAgent={onOpenAgent} onAskAgain={onAskAgain} onTakeBack={onTakeBack} />}
                        {entry.taskState === "done" && (
                            <p className="text-[14.5px] leading-normal text-ink" data-agents-task-back>
                                <span className="block text-[12.5px] text-muted">{t("agents.backFromTask", { agent: name })}</span>
                                {entry.taskResult}
                            </p>
                        )}
                        {showText && <pre className="max-h-64 overflow-auto whitespace-pre-wrap rounded-lg border border-border bg-panel px-3 py-2 font-sans text-[13px] leading-relaxed text-ink" data-agents-what-went>{entry.text}</pre>}
                    </>
                ) : (
                    <>
                        <span className="text-[12.5px] text-muted">
                            {/* No "Blocked" or "Declined": the only words are what Isoform knows (agents-join v1.0.0 rule 3). */}
                            {t("agents.isBack", { agent: name })}
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

/** What Isoform knows about one hand-off, in agents-join §7's words. */
function HandoffStatusLine({ status, name, opens, language, onOpenAgent, onAskAgain, onTakeBack }: { status: HandoffStatus; name: string; opens?: string; language: string; onOpenAgent: (agentId: string) => void; onAskAgain: (id: string) => void; onTakeBack: (id: string) => void }) {
    const { t } = useTranslation("app");
    const text = "text-[12.5px] text-muted";
    switch (status.state) {
        case "sent":
            return <p className={text} data-handoff-state="sent">{t("agents.status.sent", { time: clock(status.sentAt, language) })}</p>;
        case "opened":
            return <p className={text} data-handoff-state="opened">{t("agents.status.opened", { agent: name, time: clock(status.sentAt, language) })}</p>;
        case "has_it":
            return <p className={text} data-handoff-state="has_it">{t("agents.status.has", { agent: name })}</p>;
        case "working":
            return <p className={text} data-handoff-state="working">{status.line ? t("agents.workingLine", { line: status.line }) : t("agents.status.workingPlain")}</p>;
        case "no_word":
            return (
                <div className="flex flex-col items-start gap-1.5" data-handoff-state="no_word">
                    <p className="text-[12.5px] text-warning">{t("agents.status.noWord", { agent: name, time: clock(status.since ?? status.sentAt, language) })}</p>
                    <div className="flex gap-2">
                        <Button size="sm" variant="outline" onClick={() => onAskAgain(status.id)} data-handoff-ask-again>{t("agents.status.askAgain")}</Button>
                        <Button size="sm" variant="quiet" onClick={() => onTakeBack(status.id)} data-handoff-take-back>{t("agents.status.takeBack")}</Button>
                    </div>
                </div>
            );
        case "couldnt_start":
            return (
                <div className="flex flex-col items-start gap-1.5" data-handoff-state="couldnt_start">
                    <p className="text-[12.5px] text-warning">{t("agents.status.couldntStart", { agent: name })}</p>
                    {status.line && <p className={text}>{t("agents.status.said", { agent: name, line: status.line })}</p>}
                    <p className={text} data-handoff-nothing-sent>{t("agents.status.nothingSent")}</p>
                    <div className="flex gap-2">
                        {opens && <Button size="sm" variant="outline" onClick={() => onOpenAgent(opens)} data-handoff-open-agent>{t("agents.status.openAgent", { agent: name })}</Button>}
                        <Button size="sm" variant="quiet" onClick={() => onAskAgain(status.id)} data-handoff-send-again>{t("agents.status.sendAgain")}</Button>
                    </div>
                </div>
            );
        case "taken_back":
            return <p className={text} data-handoff-state="taken_back">{t("agents.status.takenBack", { agent: name })}</p>;
        default:
            return null;
    }
}
