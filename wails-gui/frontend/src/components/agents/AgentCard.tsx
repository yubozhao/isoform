import { forwardRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { X } from "lucide-react";
import { Button } from "@/components/ui/button";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import type { WorkOnItKind } from "@/lib/wails-shim-app";
import { canLearnFrom, type AgentRow, type WeekCounts } from "./agents-model";
import { LearnFromItSwitch } from "./LearnFromItSwitch";

export const agentCardSectionLabel = "text-[11px] font-semibold uppercase tracking-wide text-muted select-none";

export function agentInitials(name: string): string {
    const letters = name.replace(/[^\p{L}\p{N} ]/gu, "").trim();
    return (letters.slice(0, 2) || "?").replace(/^./, (c) => c.toUpperCase());
}

export function agentDate(at: number, language: string): string {
    return new Date(at).toLocaleDateString(language, { month: "short", day: "numeric" });
}

/** The card's first line: "Connected {date} · {where} · {how it runs}" (computer-agents v1.1.0 rule 11). */
export function useAgentCardLine(row: AgentRow, language: string): string {
    const { t } = useTranslation("app");
    const state = row.state === "connected" && row.connectedAt
        ? t("agents.card.connectedOn", { date: agentDate(row.connectedAt, language) })
        : row.state === "disconnected" && row.disconnectedAt
            ? t("agents.disconnectedOn", { date: agentDate(row.disconnectedAt, language) })
            : t(`agents.state.${row.state}`);
    const parts = [state];
    switch (row.where) {
        case "here":
            parts.push(t("agents.where.here"), t("agents.card.runsWhenStarted"));
            break;
        case "itsApp":
            parts.push(t("agents.where.itsApp"));
            break;
        case "byText":
            parts.push(t("agents.where.byText"));
            break;
        case "device":
            if (row.device) parts.push(row.device);
            break;
    }
    return parts.join(" · ");
}

export interface AgentCardProps {
    row: AgentRow;
    language: string;
    week: WeekCounts;
    /** Set after Change its job replaced another agent (agents-join v1.0.0 rule 8). */
    takesOver?: { kind: WorkOnItKind; current: string; agent: string } | null;
    onClose: () => void;
    onChangeJob: (kind: WorkOnItKind, agentId: string) => void;
    onDisconnect: () => void;
    /** After Learn from it changed (ok) or failed to. */
    onLearnChanged: (ok: boolean) => void;
}

/**
 * One agent opened (computer-agents v1.1.0 13d): badge, name, how it is
 * connected, its job with Change its job ›, what a joined agent may open,
 * Learn from it (only for agents on this computer), this week, and
 * Disconnect, asked once.
 */
export const AgentCard = forwardRef<HTMLElement, AgentCardProps>(function AgentCard({ row, language, week, takesOver, onClose, onChangeJob, onDisconnect, onLearnChanged }, ref) {
    const { t } = useTranslation("app");
    const { t: tSettings } = useTranslation("settings");
    const [asking, setAsking] = useState(false);
    const line = useAgentCardLine(row, language);
    // Only catalog agents can hold a job (SetYourAgent checks the kind).
    const canChangeJob = !row.id.startsWith("ag_") && row.state !== "disconnected";
    const canDisconnect = row.state === "connected" && (!!row.connectionId || row.connectedHere);
    const jobWord = row.chosenFor === "code" ? t("agents.card.jobCode") : row.chosenFor === "else" ? t("agents.card.jobElse") : t("agents.card.jobNone");
    const weekLine = [t("agents.card.weekHandoffs", { count: week.handoffs }), t("agents.card.weekBack", { count: week.back }), t("agents.card.weekUseful", { count: week.useful })].join(" · ");
    const mayOpen = row.mayOpen && !row.connectedHere
        ? row.kind === "code" ? t("agents.card.mayOpen", { repos: row.mayOpen }) : t("agents.card.mayOpenOther", { what: row.mayOpen })
        : null;

    return (
        <section ref={ref} tabIndex={-1} aria-label={row.name} className="flex flex-col gap-4 rounded-xl border border-border bg-panel p-4 outline-none" data-agent-card={row.id}>
            <div className="flex items-start gap-3">
                <span aria-hidden className="flex size-9 shrink-0 items-center justify-center rounded-lg bg-surface-active text-[13px] font-semibold text-ink">{agentInitials(row.name)}</span>
                <div className="min-w-0 flex-1">
                    <h3 className="truncate text-[16px] font-semibold text-ink">{row.name}</h3>
                    <p className="text-[12.5px] text-muted" data-agent-card-line>{line}</p>
                </div>
                <Button variant="quiet" size="icon" className="size-7 shrink-0" aria-label={t("agents.card.close", { agent: row.name })} onClick={onClose} data-agent-card-close>
                    <X className="size-4" />
                </Button>
            </div>

            <div className="flex flex-col gap-1">
                <h4 className={agentCardSectionLabel}>{t("agents.card.job")}</h4>
                <div className="flex flex-wrap items-baseline justify-between gap-x-3 gap-y-1">
                    <p className="text-[13.5px] text-ink" data-agent-card-job>{jobWord}</p>
                    {canChangeJob && (
                        <DropdownMenu>
                            <DropdownMenuTrigger asChild>
                                <Button variant="link" className="h-auto px-0 py-0 text-[13px] no-underline hover:underline" data-agent-card-change-job>{t("agents.card.changeJob")}</Button>
                            </DropdownMenuTrigger>
                            <DropdownMenuContent align="end">
                                <DropdownMenuItem onSelect={() => onChangeJob(row.kind, row.id)} data-agent-job-option={row.kind}>
                                    {row.kind === "code" ? t("agents.card.forCode") : t("agents.card.forElse")}
                                </DropdownMenuItem>
                                {/* Not now clears the choice only when this agent holds it. */}
                                <DropdownMenuItem disabled={!row.chosenFor} onSelect={() => row.chosenFor && onChangeJob(row.chosenFor, "")} data-agent-job-option="none">
                                    {t("agents.card.notNow")}
                                </DropdownMenuItem>
                            </DropdownMenuContent>
                        </DropdownMenu>
                    )}
                </div>
                {takesOver && (
                    <p className="text-[12.5px] text-muted" data-agent-card-takes-over>
                        {tSettings("yourAgents.takesOver", { current: takesOver.current, agent: takesOver.agent, kind: takesOver.kind === "code" ? tSettings("yourAgents.kindCode") : tSettings("yourAgents.kindElse") })}
                    </p>
                )}
            </div>

            {/* What it may open: a joined agent's own words; Change › waits on the backend (Needs backend 4). */}
            {mayOpen && (
                <div className="flex flex-col gap-1">
                    <h4 className={agentCardSectionLabel}>{t("agents.card.mayOpenTitle")}</h4>
                    <p className="text-[13.5px] text-ink" data-agent-card-may-open>{mayOpen}</p>
                </div>
            )}

            <div className="flex flex-col gap-1" data-agent-card-learn>
                <div className="flex items-center justify-between gap-3">
                    <h4 className={agentCardSectionLabel}>{t("agents.learn.title")}</h4>
                    {canLearnFrom(row) && <LearnFromItSwitch agentId={row.id} name={row.name} on={row.learnFromIt} onChanged={onLearnChanged} />}
                </div>
                <p className="text-[13px] leading-relaxed text-muted">
                    {canLearnFrom(row) ? t("agents.learn.body", { agent: row.name }) : t("agents.learnNotHere")}
                </p>
            </div>

            <div className="flex flex-col gap-1">
                <h4 className={agentCardSectionLabel}>{t("agents.card.week")}</h4>
                <p className="text-[13.5px] text-ink" data-agent-card-week>{weekLine}</p>
            </div>

            {canDisconnect && (
                <div className="flex flex-col items-start gap-2 border-t border-border pt-3">
                    {!asking ? (
                        <Button variant="quiet" size="sm" onClick={() => setAsking(true)} data-agents-disconnect>{t("agents.disconnect")}</Button>
                    ) : (
                        <div className="flex flex-col gap-2 rounded-lg bg-panel-warm p-3" data-agents-disconnect-ask>
                            <p className="text-[13px] leading-relaxed text-ink">{t("agents.disconnectAsk", { agent: row.name })}</p>
                            <div className="flex gap-2">
                                <Button size="sm" variant="outline" onClick={() => setAsking(false)}>{t("agents.connectSheet.cancel")}</Button>
                                <Button size="sm" onClick={() => { setAsking(false); onDisconnect(); }} data-agents-disconnect-yes>{t("agents.disconnect")}</Button>
                            </div>
                        </div>
                    )}
                </div>
            )}
        </section>
    );
});
