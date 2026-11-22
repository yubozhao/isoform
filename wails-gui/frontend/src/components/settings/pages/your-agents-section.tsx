import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { Plus } from "lucide-react";
import { ConfirmDialog } from "@/components/confirm-dialog";
import { agentRows, canLearnFrom, type AgentRow } from "@/components/agents/agents-model";
import { LearnFromItSwitch } from "@/components/agents/LearnFromItSwitch";
import { ConnectAgentDialog } from "@/components/agents/ConnectAgentDialog";
import { SettingsFailureLine, SettingsPlaceholderRows, useReadDeadline } from "@/components/settings/settings-failure";
import { SettingsCard, SettingsRow } from "@/components/settings/settings-primitives";
import { Button } from "@/components/ui/button";
import { jobHeldBy, useIsoformAssistant } from "./your-ai-section";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import {
    ConnectAgentHere,
    DisconnectAgent,
    DisconnectAgentHere,
    GetConnectedAgents,
    GetYourAgents,
    SetYourAgent,
    type ConnectedAgent,
    type WorkOnItKind,
    type YourAgents,
    type YourAgentView,
} from "@/lib/wails-shim-app";

const JOBS: readonly WorkOnItKind[] = ["code", "else"];

/** Coding agents that connect on this computer with one click (as in the Connect sheet). */
const ONE_CLICK = new Set(["claude-code", "codex"]);

interface TakeOver {
    kind: WorkOnItKind;
    current: string;
    agent: string;
}

/**
 * Settings › Your agents (computer-activity v1.2.0 rule 5, computer-settings
 * v1.0.0 rule 5): Your jobs, the agent for each kind of work with Change;
 * Other agents, the same rows as the Agents page, each Connected or Not
 * connected and where it runs, with Disconnect; coding agents found here and
 * not connected under "Found on this computer" with Connect (conformance
 * answers v1.0.0 G3); Connect an agent; and the note at the foot. An agent
 * that is also Isoform's own assistant says so (G1). Each agent on this
 * computer says whether Learn from it is on and carries the same switch as
 * its card on Agents (one place to change it from either page, no
 * cross-page jump); others say it works only on this computer, in grey.
 *
 * The two reads load on their own: what returns shows, and a read that fails
 * or hasn't answered by the deadline shows the failure line with Try again,
 * never an empty card.
 */
export function YourAgentsSection() {
    const { t } = useTranslation("settings");
    const { t: tApp } = useTranslation("app");
    const [agents, setAgents] = useState<YourAgents | null>(null);
    const [connected, setConnected] = useState<ConnectedAgent[] | null>(null);
    const [loadFailed, setLoadFailed] = useState(false);
    const [actionFailed, setActionFailed] = useState(false);
    const [takeOver, setTakeOver] = useState<TakeOver | null>(null);
    const [connectOpen, setConnectOpen] = useState(false);
    const [connecting, setConnecting] = useState<AgentRow | null>(null);
    const [disconnecting, setDisconnecting] = useState<AgentRow | null>(null);
    const attempt = useRef(0);

    const reload = useCallback(async () => {
        const id = ++attempt.current;
        setLoadFailed(false);
        const settle = <T,>(read: () => Promise<T>, keep: (value: T) => void) =>
            read().then(
                (value) => {
                    if (attempt.current === id) keep(value);
                },
                () => {
                    if (attempt.current === id) setLoadFailed(true);
                },
            );
        await Promise.all([settle(GetYourAgents, setAgents), settle(GetConnectedAgents, (list) => setConnected(list ?? []))]);
    }, []);

    useEffect(() => {
        void reload();
    }, [reload]);

    const late = useReadDeadline((agents === null || connected === null) && !loadFailed);
    const failure = loadFailed || late ? { text: t("yourAgents.loadFailed"), retry: () => void reload() } : null;

    const act = async (action: () => Promise<unknown>): Promise<boolean> => {
        let ok = true;
        try {
            await action();
            setActionFailed(false);
        } catch {
            ok = false;
            setActionFailed(true);
        }
        await reload();
        return ok;
    };

    const choose = async (kind: WorkOnItKind, previous: YourAgentView | undefined, agent: YourAgentView) => {
        setTakeOver(null);
        const ok = await act(() => SetYourAgent(kind, agent.id));
        if (ok && previous && previous.id !== agent.id) setTakeOver({ kind, current: previous.name, agent: agent.name });
    };

    const whereText = (row: AgentRow) => (row.where === "device" ? (row.device ?? "") : tApp(`agents.where.${row.where}`));

    const rows = useMemo(() => (agents ? agentRows(agents, connected ?? [], [], []) : []), [agents, connected]);
    // A coding agent installed here and not connected sits under "Found on
    // this computer" with Connect (G3): it was never added, so nothing to remove.
    const isFound = (row: AgentRow) => row.kind === "code" && row.found && row.state === "notConnected";
    const others = rows.filter((row) => !isFound(row));
    const found = rows.filter(isFound);
    const assistantId = useIsoformAssistant();
    const assistantJob = jobHeldBy(agents, assistantId);

    // Learn from it (computer-activity v1.2.0 rule 5): on or off for agents on this computer, grey "only on this computer" for others.
    const learnLine = (row: AgentRow) =>
        canLearnFrom(row) ? (
            <span className="mt-1 flex items-center gap-2" data-your-agents-learn={row.id}>
                {row.learnFromIt ? t("yourAgents.learnOn") : t("yourAgents.learnOff")}
                <LearnFromItSwitch
                    agentId={row.id}
                    name={row.name}
                    on={row.learnFromIt}
                    label={tApp("agents.learn.title")}
                    onChanged={(ok) => {
                        setActionFailed(!ok);
                        void reload();
                    }}
                />
            </span>
        ) : (
            <span className="block text-muted" data-your-agents-learn={row.id}>
                {t("yourAgents.learnOnlyHere")}
            </span>
        );

    const rowDescription = (row: AgentRow) => {
        const line = t("yourAgents.stateWhere", { state: tApp(`agents.state.${row.state}`), where: whereText(row) });
        if (row.id !== assistantId || !assistantJob) return (
            <>
                {line}
                {learnLine(row)}
            </>
        );
        return (
            <>
                {line}
                {learnLine(row)}
                <span className="block" data-your-agents-also={row.id}>
                    {t("yourAI.alsoAgent", { app: row.name, job: assistantJob === "code" ? t("yourAI.jobCode") : t("yourAI.jobElse") })}
                </span>
            </>
        );
    };


    const rowControl = (row: AgentRow) => {
        if (row.state === "connected") {
            return (
                <Button
                    variant="outline"
                    size="sm"
                    disabled={!row.connectedHere && !row.connectionId}
                    onClick={() => setDisconnecting(row)}
                    data-your-agents-disconnect={row.id}
                >
                    {t("yourAgents.disconnect")}
                </Button>
            );
        }
        if (row.kind === "code" && row.found) {
            return (
                <Button
                    variant="outline"
                    size="sm"
                    onClick={() => (ONE_CLICK.has(row.id) ? setConnecting(row) : setConnectOpen(true))}
                    data-your-agents-connect-here={row.id}
                >
                    {tApp("agents.connectSheet.connectThis")}
                </Button>
            );
        }
        // Remove is only for an agent the person added that can't connect,
        // whose tasks are written in (Muse, Instinct; conformance answers G3).
        // There is no way to add one yet (batch E), so no button.
        return null;
    };

    // Connect an agent, then the note, end the section (computer-activity v1.2.0 rule 5).
    const foot = (
        <>
            <Button
                variant="link"
                className="mt-2 h-auto self-start px-0 py-0.5 text-[14px] no-underline hover:underline"
                onClick={() => setConnectOpen(true)}
                data-your-agents-connect
            >
                <Plus className="size-4" />
                {t("yourAgents.connect")}
            </Button>
            <p className="settings-row-desc mt-3" data-your-agents-note>
                {t("yourAgents.note")}
            </p>
        </>
    );

    return (
        <>
            <SettingsCard title={t("yourAgents.jobsTitle")}>
                <SettingsFailureLine failure={failure} />
                {actionFailed && <SettingsFailureLine failure={{ text: t("yourAgents.actionFailed") }} />}
                {agents === null && !failure && <SettingsPlaceholderRows />}
                {agents &&
                    JOBS.map((kind) => {
                        const choices = agents.agents.filter((agent) => agent.kind === kind && agent.reach !== "disconnected");
                        const chosen = agents.agents.find((agent) => agent.id === agents.choices[kind]);
                        const label = kind === "code" ? t("yourAgents.forCode") : t("yourAgents.forElse");
                        const sub = kind === "code" ? t("yourAgents.forCodeSub") : t("yourAgents.forElseSub");
                        return (
                            <SettingsRow
                                key={kind}
                                title={label}
                                description={chosen ? sub : t("yourAgents.noneChosen")}
                                control={
                                    <div className="flex items-center gap-3">
                                        {chosen && (
                                            <span className="text-[13px] text-ink" data-your-agents-chosen={kind}>
                                                {chosen.name}
                                            </span>
                                        )}
                                        <DropdownMenu>
                                            <DropdownMenuTrigger asChild>
                                                <Button variant="outline" size="sm" disabled={choices.length === 0} data-your-agents-change={kind}>
                                                    {t("yourAgents.change")}
                                                </Button>
                                            </DropdownMenuTrigger>
                                            <DropdownMenuContent align="end">
                                                {choices.map((agent) => (
                                                    <DropdownMenuItem
                                                        key={agent.id}
                                                        disabled={agent.id === chosen?.id}
                                                        onSelect={() => void choose(kind, chosen, agent)}
                                                        data-your-agents-pick={agent.id}
                                                    >
                                                        {agent.name}
                                                    </DropdownMenuItem>
                                                ))}
                                            </DropdownMenuContent>
                                        </DropdownMenu>
                                    </div>
                                }
                            />
                        );
                    })}
                {takeOver && (
                    <p className="settings-row-desc" data-your-agents-takes-over>
                        {t("yourAgents.takesOver", {
                            current: takeOver.current,
                            agent: takeOver.agent,
                            kind: takeOver.kind === "code" ? t("yourAgents.kindCode") : t("yourAgents.kindElse"),
                        })}
                    </p>
                )}
            </SettingsCard>

            <SettingsCard title={t("yourAgents.otherTitle")}>
                {agents === null && !failure && <SettingsPlaceholderRows />}
                {agents && rows.length === 0 && (
                    <p className="settings-row-desc" data-your-agents-empty>
                        {t("yourAgents.empty")}
                    </p>
                )}
                {others.map((row) => (
                    <SettingsRow key={row.id} title={row.name} description={rowDescription(row)} control={rowControl(row)} />
                ))}
                {found.length === 0 && foot}
            </SettingsCard>

            {found.length > 0 && (
                <SettingsCard title={tApp("agents.connectSheet.found")}>
                    <div data-your-agents-found>
                        {found.map((row) => (
                            <SettingsRow key={row.id} title={row.name} description={rowDescription(row)} control={rowControl(row)} />
                        ))}
                    </div>
                    {foot}
                </SettingsCard>
            )}

            <ConnectAgentDialog open={connectOpen} onOpenChange={setConnectOpen} agents={agents} onConnected={() => void reload()} />
            <ConfirmDialog
                open={connecting !== null}
                onOpenChange={(open) => {
                    if (!open) setConnecting(null);
                }}
                title={connecting?.name ?? ""}
                description={tApp("agents.connectSheet.connectAsk", { agent: connecting?.name ?? "" })}
                confirmLabel={tApp("agents.connectSheet.connectThis")}
                onConfirm={() => {
                    const row = connecting;
                    if (row) void act(() => ConnectAgentHere(row.id));
                }}
            />
            <ConfirmDialog
                open={disconnecting !== null}
                onOpenChange={(open) => {
                    if (!open) setDisconnecting(null);
                }}
                title={t("yourAgents.disconnectTitle", { agent: disconnecting?.name ?? "" })}
                description={t("yourAgents.disconnectBody")}
                confirmLabel={t("yourAgents.disconnect")}
                variant="danger"
                onConfirm={() => {
                    const row = disconnecting;
                    if (!row) return;
                    void act(async () => {
                        if (row.connectedHere) await DisconnectAgentHere(row.id);
                        if (row.connectionId) await DisconnectAgent(row.connectionId);
                    });
                }}
            />
        </>
    );
}
