import { useCallback, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { activeInterval } from "@/lib/app-activity";
import { cn } from "@/lib/utils";
import {
    AnswerAgentJoin,
    CancelAgentJoin,
    ConnectAgentHere,
    GetAgentJoin,
    NativeClipboardSetText,
    StartAgentJoin,
    type AgentJoin,
    type AgentJoinStart,
    type YourAgents,
} from "@/lib/wails-shim-app";

/** How often the sheet reads the join while it waits for the agent. */
export const JOIN_POLL_MS = 1500;

const sectionLabel = "text-[11px] font-semibold uppercase tracking-wide text-muted select-none";

/**
 * Connect an agent (computer-agents v1.0.0 rules 11 to 13): the agents found
 * on this computer, and the one message for any other agent. The agent joins
 * only after the person allows it with the matching check number.
 */
export function ConnectAgentDialog({ open, onOpenChange, agents, onConnected }: {
    open: boolean;
    onOpenChange: (open: boolean) => void;
    agents: YourAgents | null;
    onConnected: () => void;
}) {
    const { t } = useTranslation("app");
    const [start, setStart] = useState<AgentJoinStart | null>(null);
    const [join, setJoin] = useState<AgentJoin | null>(null);
    const [startFailed, setStartFailed] = useState(false);
    const [copied, setCopied] = useState(false);
    const [pasteInto, setPasteInto] = useState<string | null>(null);
    const [asking, setAsking] = useState<string | null>(null);
    const [hereFailed, setHereFailed] = useState<string | null>(null);
    const [connectedHere, setConnectedHere] = useState<Set<string>>(new Set());
    const announced = useRef<string | null>(null);

    const makeLink = useCallback(async () => {
        setCopied(false);
        try {
            const made = await StartAgentJoin();
            setStart(made);
            setJoin({ state: "waiting", expiresAt: made.expiresAt });
            setStartFailed(false);
        } catch {
            setStart(null);
            setJoin(null);
            setStartFailed(true);
        }
    }, []);

    useEffect(() => {
        if (!open) return;
        announced.current = null;
        setPasteInto(null);
        setAsking(null);
        setHereFailed(null);
        setConnectedHere(new Set());
        void makeLink();
    }, [open, makeLink]);

    const waiting = join?.state === "waiting" || join?.state === "requested";
    useEffect(() => {
        if (!open || !waiting) return;
        // Read only while the window is looked at (lib/app-activity.ts).
        return activeInterval(() => {
            void GetAgentJoin().then((current) => {
                // The status read that hands the agent its token spends the join.
                setJoin((previous) => current ?? (previous?.state === "allowed" ? previous : { state: "expired", expiresAt: previous?.expiresAt ?? 0 }));
            }).catch(() => undefined);
        }, JOIN_POLL_MS);
    }, [open, waiting]);

    useEffect(() => {
        if (join?.state === "allowed" && join.agentId && announced.current !== join.agentId) {
            announced.current = join.agentId;
            onConnected();
        }
    }, [join, onConnected]);

    const close = (next: boolean) => {
        if (!next && waiting) void CancelAgentJoin();
        onOpenChange(next);
    };

    const copy = async () => {
        if (!start) return;
        try {
            await NativeClipboardSetText(start.message);
            setCopied(true);
        } catch {
            setCopied(false);
        }
    };

    const answer = async (allow: boolean) => {
        try {
            await AnswerAgentJoin(allow);
            setJoin((j) => (j ? { ...j, state: allow ? "allowed" : "denied" } : j));
        } catch {
            const current = await GetAgentJoin().catch(() => null);
            setJoin(current);
        }
    };

    const here = (agents?.agents ?? []).filter((a) => a.kind === "code" && a.found);
    const pasteName = pasteInto ? here.find((a) => a.id === pasteInto)?.name : undefined;
    // Claude Code and Codex run here and connect with one click; any other
    // coding agent here (Cursor) gets the join message.
    const oneClick = (id: string) => id === "claude-code" || id === "codex";
    const connectHere = async (id: string) => {
        setAsking(null);
        try {
            await ConnectAgentHere(id);
            setConnectedHere((prev) => new Set(prev).add(id));
            setHereFailed(null);
            onConnected();
        } catch {
            setHereFailed(id);
        }
    };

    return (
        <Dialog open={open} onOpenChange={close}>
            <DialogContent className="w-[560px]" data-connect-agent>
                <DialogHeader className="flex-col items-start gap-1.5">
                    <DialogTitle>{t("agents.connectSheet.title")}</DialogTitle>
                    <DialogDescription>{t("agents.connectSheet.body")}</DialogDescription>
                </DialogHeader>
                <div className="flex flex-col gap-5 overflow-y-auto px-6 pt-4 pb-6">
                    {here.length > 0 && (
                        <section className="flex flex-col gap-2" aria-label={t("agents.connectSheet.onThis")}>
                            <h3 className={sectionLabel}>{t("agents.connectSheet.onThis")}</h3>
                            <ul className="flex flex-col rounded-xl border border-border bg-panel px-3.5 py-1">
                                {here.map((agent, i) => (
                                    <li key={agent.id} className={cn("flex flex-col gap-2 py-2.5", i > 0 && "border-t border-border")} data-connect-here={agent.id}>
                                        <div className="flex items-center gap-2.5">
                                            <span className="min-w-0 flex-1">
                                                <span className="block text-[14px] font-semibold text-ink">{agent.name}</span>
                                                <span className="block text-[12px] text-muted">{t("agents.connectSheet.found")}</span>
                                            </span>
                                            {agent.connected || connectedHere.has(agent.id) ? (
                                                <span className="text-[13px] text-muted">{t("agents.job.connected")}</span>
                                            ) : (
                                                <Button variant="outline" size="sm" onClick={() => (oneClick(agent.id) ? setAsking(agent.id) : setPasteInto(agent.id))} disabled={!oneClick(agent.id) && !start}>
                                                    {t("agents.connectSheet.connectThis")}
                                                </Button>
                                            )}
                                        </div>
                                        {asking === agent.id && (
                                            <div className="flex flex-col gap-2 rounded-lg bg-panel-warm p-3" data-connect-ask={agent.id}>
                                                <p className="text-[13px] leading-relaxed text-ink">{t("agents.connectSheet.connectAsk", { agent: agent.name })}</p>
                                                <div className="flex gap-2">
                                                    <Button size="sm" onClick={() => void connectHere(agent.id)} data-connect-here-yes>{t("agents.connectSheet.connectThis")}</Button>
                                                    <Button size="sm" variant="outline" onClick={() => setAsking(null)}>{t("agents.connectSheet.cancel")}</Button>
                                                </div>
                                            </div>
                                        )}
                                        {hereFailed === agent.id && <p className="text-[12.5px] text-error" data-connect-here-failed>{t("agents.connectSheet.connectFailed", { agent: agent.name })}</p>}
                                    </li>
                                ))}
                            </ul>
                        </section>
                    )}

                    <section className="flex flex-col gap-2" aria-label={t("agents.connectSheet.anyOther")}>
                        <h3 className={sectionLabel}>{pasteName ? t("agents.connectSheet.pasteIntoTitle", { agent: pasteName }) : t("agents.connectSheet.anyOther")}</h3>
                        {startFailed ? (
                            <div className="flex items-center gap-3 text-[13px] text-error" data-connect-off>
                                {t("agents.connectSheet.off")}
                                <Button variant="quiet" size="sm" onClick={() => void makeLink()}>{t("agents.retry")}</Button>
                            </div>
                        ) : (
                            start && (
                                <>
                                    <div className="flex items-start gap-3 rounded-xl border border-border bg-panel-warm px-3.5 py-3">
                                        <p className="min-w-0 flex-1 break-words text-[13.5px] leading-relaxed text-ink" data-connect-message>{start.message}</p>
                                        <Button variant="outline" size="sm" onClick={() => void copy()} data-connect-copy>
                                            {copied ? t("agents.connectSheet.copied") : t("agents.connectSheet.copy")}
                                        </Button>
                                    </div>
                                    <p className="text-[12.5px] leading-relaxed text-muted">
                                        {pasteName ? t("agents.connectSheet.pasteInto", { agent: pasteName }) : t("agents.connectSheet.anyOtherHelp")}
                                    </p>
                                    {start.local && <p className="text-[12.5px] leading-relaxed text-muted" data-connect-local>{t("agents.connectSheet.localOnly")}</p>}
                                </>
                            )
                        )}
                    </section>

                    {join && <JoinState join={join} onAnswer={(allow) => void answer(allow)} onNewLink={() => void makeLink()} />}
                </div>
            </DialogContent>
        </Dialog>
    );
}

function JoinState({ join, onAnswer, onNewLink }: { join: AgentJoin; onAnswer: (allow: boolean) => void; onNewLink: () => void }) {
    const { t } = useTranslation("app");
    switch (join.state) {
        case "waiting":
            return <p className="text-[13px] text-muted" data-join-state="waiting">{t("agents.connectSheet.waiting")}</p>;
        case "requested":
            return (
                <div className="flex flex-col gap-2 rounded-xl border border-border bg-panel p-3.5 ring-2 ring-accent" data-join-state="requested">
                    <p className="text-[15px] font-semibold text-ink">{t("agents.connectSheet.asks", { name: join.name })}</p>
                    <p className="text-[13px] text-muted">{t("agents.connectSheet.asksDevice", { device: join.device })}</p>
                    <p className="text-[13px] text-muted">{t("agents.connectSheet.asksOpen", { mayOpen: join.mayOpen })}</p>
                    <p className="text-[13px] text-ink">{t("agents.connectSheet.check", { check: join.check, name: join.name })}</p>
                    <div className="flex gap-2 pt-1">
                        <Button size="sm" onClick={() => onAnswer(true)} data-join-allow>{t("agents.connectSheet.allow")}</Button>
                        <Button size="sm" variant="outline" onClick={() => onAnswer(false)} data-join-deny>{t("agents.connectSheet.deny")}</Button>
                    </div>
                </div>
            );
        case "allowed":
            return (
                <div className="flex items-center gap-3" data-join-state="allowed">
                    <p className="text-[13.5px] text-ink">{t("agents.connectSheet.joined", { name: join.name ?? "" })}</p>
                    <Button variant="quiet" size="sm" onClick={onNewLink}>{t("agents.connectSheet.another")}</Button>
                </div>
            );
        default:
            return (
                <div className="flex items-center gap-3" data-join-state={join.state}>
                    <p className="text-[13px] text-muted">{join.state === "denied" ? t("agents.connectSheet.denied") : t("agents.connectSheet.expired")}</p>
                    <Button variant="quiet" size="sm" onClick={onNewLink}>{t("agents.connectSheet.newLink")}</Button>
                </div>
            );
    }
}
