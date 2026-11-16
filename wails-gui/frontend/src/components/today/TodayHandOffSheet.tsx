import { useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { X } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { measureToday } from "@/lib/today-telemetry";
import type { WorkAnswerInput, WorkItem, WorkOnItKind, WorkOnItSent, YourAgentView, YourAgents } from "@/lib/wails-shim-app";
import { WorkOnItDialog } from "@/components/work-on-it/work-on-it";
import type { WorkAnswerError } from "@/hooks/useWorkList";

type Translate = (key: string, options?: Record<string, unknown>) => string;

/** The kind of work an item is (code goes to the code agent): the item's own
 *  word when the computer gives one, else the kind of the agent it names. */
export function handOffKind(item: WorkItem, agents: YourAgents | null): WorkOnItKind {
    const said = item.yes.kind;
    if (said === "code" || said === "else") return said;
    const named = agents?.agents.find((agent) => agent.id === item.yes.agent);
    return named?.kind === "code" ? "code" : "else";
}

/** The agent the work goes to: the one the person picked for this hand-off,
 *  else their choice for this kind of work (phone-agent v1.1.0, B4). */
export function handOffAgent(kind: WorkOnItKind, agents: YourAgents | null, picked?: string): YourAgentView | null {
    const list = agents?.agents ?? [];
    return list.find((agent) => agent.id === picked) ?? list.find((agent) => agent.id === agents?.choices[kind]) ?? null;
}

/** A topic's proposal goes as it was proposed (Go: no edit, no removals). */
export function handOffFixed(item: WorkItem): boolean {
    return item.source === "topic";
}

/**
 * Hand it to your agent (computer-today v1.0.0, SH4; phone-agent v1.0.0 and
 * v1.1.0): the sheet over Today a Yes on agent work opens. It shows exactly
 * what goes, the work and What it gets with each removable row, and sends
 * only on Send (⌘↩). The agent is picked by the work; Change agent picks
 * another for this hand-off only.
 */
export function TodayHandOffSheet({ item, edited, twoStep = false, failedText = "", agents, agentsFailed = false, onRetryAgents, onSend, onClose }: {
    /** What the computer said when a send failed, quoted when the agent couldn't start. */
    failedText?: string;
    /** The Yes is already recorded (Yes · not sent): Send hands it over. */
    twoStep?: boolean;
    /** The agents couldn't be read: the sheet says so, with Try again. */
    agentsFailed?: boolean;
    onRetryAgents?: () => void;
    item: WorkItem;
    /** The person's own version from Edit, which goes in place of the work. */
    edited?: string;
    agents: YourAgents | null;
    onSend: (input: WorkAnswerInput) => Promise<WorkAnswerError | null>;
    onClose: () => void;
}) {
    const { t } = useTranslation("today");
    const tr: Translate = (key, options) => t(key as never, options as never) as unknown as string;
    const kind = handOffKind(item, agents);
    const [picked, setPicked] = useState<string | undefined>(undefined);
    const [choosing, setChoosing] = useState(false);
    const [removed, setRemoved] = useState<string[]>([]);
    const [busy, setBusy] = useState(false);
    const [error, setError] = useState<WorkAnswerError | null>(null);
    const [away, setAway] = useState(false);
    // The agent couldn't start (computer-today v1.1.0 rule 10): nothing went,
    // the item stays offered, and the sheet quotes what it said.
    const [couldnt, setCouldnt] = useState(false);
    const agent = handOffAgent(kind, agents, picked);
    const others = useMemo(() => (agents?.agents ?? []).filter((row) => row.kind === kind && row.id !== agent?.id), [agents, kind, agent?.id]);
    const gets = (item.gets ?? []).filter((get) => !removed.includes(get.id));
    const text = edited ?? item.work.text ?? item.work.title;
    const name = agent?.name ?? tr("agent.yourAgent");

    const send = async () => {
        if (busy || !agent) return;
        setBusy(true);
        setError(null);
        setCouldnt(false);
        const input: WorkAnswerInput = twoStep
            ? { action: "send", removed, agentId: agent.id }
            : edited !== undefined
            ? { action: "edit", before: item.work.text ?? item.work.title, after: edited, removed, agentId: agent.id }
            : { action: "yes", removed, agentId: agent.id };
        const failed = await measureToday("W03", "do", "done", () => onSend(input));
        setBusy(false);
        // An agent that hasn't joined Isoform takes the work through Work on
        // it (a link or a copy, the person's tap); the Yes stays not sent
        // until that hand-off is recorded against the item (#1150, 26.1).
        if (failed === "agentAway") setAway(true);
        else if (failed === "failed") setCouldnt(true);
        else if (failed) setError(failed);
        else onClose();
    };
    const recordAway = async (sent: WorkOnItSent) => {
        const failed = await onSend({ action: "send", recordId: sent.recordId });
        if (failed) { setAway(false); setError(failed); } else onClose();
    };
    useEffect(() => {
        if (away) return;
        const onKey = (event: KeyboardEvent) => {
            if (event.key === "Enter" && event.metaKey) { event.preventDefault(); void send(); }
        };
        document.addEventListener("keydown", onKey);
        return () => document.removeEventListener("keydown", onKey);
    });

    if (away && agent) {
        const what = [text, ...gets.map((get) => get.label)].join("\n");
        return <WorkOnItDialog input={{ sourceKind: "work_item", sourceId: item.id, title: item.work.title, detail: what, done: "", kind }} agentId={agent.id} onSent={(sent) => void recordAway(sent)} onClose={onClose} />;
    }
    return (
        <Dialog open onOpenChange={(open) => { if (!open) onClose(); }}>
            <DialogContent data-testid="handoff-sheet" className="flex max-w-[560px] flex-col gap-0 p-0">
                <DialogHeader className="flex flex-row items-start gap-3">
                    <span aria-hidden className="flex size-9 shrink-0 items-center justify-center rounded-full bg-surface-active text-[14px] font-semibold text-ink">{name.slice(0, 1).toUpperCase()}</span>
                    <div className="flex min-w-0 flex-1 flex-col gap-0.5">
                        <DialogTitle>{tr("agent.sheet.title", { agent: name })}</DialogTitle>
                        <DialogDescription data-testid="handoff-subtitle">
                            {tr(agent?.connected && (agent.reach === "connected_computer" || agent.reach === "on_computer") ? "agent.subtitle" : "agent.subtitleKind", { kind: tr(`agent.kind.${kind}`) })}
                            {" · "}
                            <button type="button" className="font-medium text-accent" aria-expanded={choosing} onClick={() => setChoosing((value) => !value)} data-testid="handoff-change-agent">{tr("agent.change")}</button>
                        </DialogDescription>
                        {choosing && (
                            <div className="mt-1.5 flex flex-wrap gap-1.5" data-testid="handoff-agents">
                                {others.length === 0 && <span className="text-[12.5px] text-muted">{tr("agent.noOther")}</span>}
                                {others.map((row) => (
                                    <Button key={row.id} size="sm" variant="outline" onClick={() => { setPicked(row.id); setChoosing(false); }}>{row.name}</Button>
                                ))}
                            </div>
                        )}
                    </div>
                </DialogHeader>
                <div className="flex max-h-[56vh] flex-col gap-3 overflow-y-auto px-6 py-4">
                    <p className="text-[12.5px] font-medium text-muted">{tr("agent.exact")}</p>
                    <p className="whitespace-pre-wrap text-[14px] leading-[1.55] text-ink" data-testid="handoff-text">{text}</p>
                    {gets.length > 0 && (
                        <div className="flex flex-col gap-1" data-testid="handoff-gets">
                            <p className="text-[11px] font-semibold uppercase tracking-wider text-muted">{tr("agent.gets")}</p>
                            {gets.map((get) => (
                                <div key={get.id} className="flex items-center justify-between gap-2 rounded-lg border border-border px-3 py-1.5" data-testid="handoff-get">
                                    <span className="min-w-0 truncate text-[13px] text-ink">{get.label}</span>
                                    {get.removable && !handOffFixed(item) && (
                                        <Button size="icon" variant="quiet" aria-label={tr("agent.remove", { label: get.label })} onClick={() => setRemoved((list) => [...list, get.id])}>
                                            <X className="size-3.5" />
                                        </Button>
                                    )}
                                </div>
                            ))}
                        </div>
                    )}
                    {(agentsFailed || (agents && !agent)) && (
                        <p role="alert" className="text-[12.5px] text-copper" data-testid="handoff-agents-failed">
                            {tr(agentsFailed ? "agent.readFailed" : "agent.noneChosen")}{" "}
                            {agentsFailed && onRetryAgents && <button type="button" className="font-medium text-accent" onClick={onRetryAgents}>{tr("ctoday.tryAgain")}</button>}
                        </p>
                    )}
                    {couldnt && (
                        <div role="alert" className="flex flex-col gap-1.5 rounded-lg border border-border bg-surface-active px-3 py-2.5" data-testid="handoff-couldnt">
                            <p className="text-[13.5px] font-semibold text-ink">{tr("ctoday.couldntStart", { agent: name })}</p>
                            <p className="text-[12.5px] text-muted">{tr("ctoday.nothingSent")}</p>
                            {failedText && <>
                                <p className="text-[12.5px] text-muted">{tr("ctoday.agentSaid", { agent: name })}</p>
                                <pre className="whitespace-pre-wrap break-words font-mono text-[12px] text-ink" data-testid="handoff-agent-said">{failedText}</pre>
                            </>}
                        </div>
                    )}
                    {error && <p role="alert" className="text-[12.5px] text-copper" data-testid="handoff-error">{tr(`work.error.${error}`)}</p>}
                </div>
                <footer className="flex flex-col gap-2 border-t border-border-divider px-6 py-4">
                    <div className="flex items-center justify-end gap-2">
                        <Button variant="quiet" onClick={() => measureToday("W03", "open", "opened", onClose)}>{tr("work.cancel")}</Button>
                        <Button disabled={busy || !agent} onClick={() => void send()} data-testid="handoff-send">
                            {tr(couldnt ? "ctoday.sendAgain" : "agent.send", { agent: name })} <kbd className="ml-1.5 text-[11px] opacity-70">⌘↩</kbd>
                        </Button>
                    </div>
                    <p className="text-[12px] text-muted" data-testid="handoff-foot">{tr("agent.foot", { agent: name })}</p>
                </footer>
            </DialogContent>
        </Dialog>
    );
}
