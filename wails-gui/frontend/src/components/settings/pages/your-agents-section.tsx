import { useCallback, useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { Plus } from "lucide-react";
import { ConfirmDialog } from "@/components/confirm-dialog";
import { agentRows, type AgentRow } from "@/components/agents/agents-model";
import { ConnectAgentDialog } from "@/components/agents/ConnectAgentDialog";
import { SettingsFailureLine } from "@/components/settings/settings-failure";
import { SettingsCard, SettingsRow } from "@/components/settings/settings-primitives";
import { Button } from "@/components/ui/button";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import {
    DisconnectAgent,
    DisconnectAgentHere,
    GetConnectedAgents,
    GetYourAgents,
    SetYourAgent,
    type ConnectedAgent,
    type WorkOnItKind,
    type YourAgents,
} from "@/lib/wails-shim-app";

const JOBS: readonly WorkOnItKind[] = ["code", "else"];

interface Snapshot {
    agents: YourAgents;
    connected: ConnectedAgent[];
}

/**
 * Settings › Your agents, its top (computer-settings v1.0.0 rule 5,
 * computer-agents v1.1.0 rules 12 and 13): the agent for each job, with
 * Change; every connected agent with Disconnect, asked once; and Connect an
 * agent. Learn from it waits for agents' history as a source (C4), as on the
 * Agents page.
 */
export function YourAgentsSection() {
    const { t } = useTranslation("settings");
    const { t: tApp } = useTranslation("app");
    const [snapshot, setSnapshot] = useState<Snapshot | null>(null);
    const [loadFailed, setLoadFailed] = useState(false);
    const [actionFailed, setActionFailed] = useState(false);
    const [connectOpen, setConnectOpen] = useState(false);
    const [disconnecting, setDisconnecting] = useState<AgentRow | null>(null);

    const reload = useCallback(async () => {
        try {
            const [agents, connected] = await Promise.all([GetYourAgents(), GetConnectedAgents()]);
            setSnapshot({ agents, connected: connected ?? [] });
            setLoadFailed(false);
        } catch {
            setLoadFailed(true);
        }
    }, []);

    useEffect(() => {
        void reload();
    }, [reload]);

    const act = async (action: () => Promise<unknown>) => {
        try {
            await action();
            setActionFailed(false);
        } catch {
            setActionFailed(true);
        }
        await reload();
    };

    const rows = useMemo(
        () => (snapshot ? agentRows(snapshot.agents, snapshot.connected, [], []).filter((row) => row.connected || row.connectedHere || !!row.connectionId) : []),
        [snapshot],
    );

    return (
        <>
            <SettingsCard title={t("yourAgents.jobsTitle")} description={t("yourAgents.note")}>
                {loadFailed && <SettingsFailureLine failure={{ text: t("yourAgents.loadFailed"), retry: () => void reload() }} />}
                {actionFailed && <SettingsFailureLine failure={{ text: t("yourAgents.actionFailed") }} />}
                {JOBS.map((kind) => {
                    const choices = snapshot?.agents.agents.filter((agent) => agent.kind === kind) ?? [];
                    const chosen = snapshot?.agents.choices[kind] ?? "";
                    const label = kind === "code" ? t("yourAgents.forCode") : t("yourAgents.forElse");
                    return (
                        <SettingsRow
                            key={kind}
                            title={label}
                            description={kind === "code" ? t("yourAgents.forCodeSub") : t("yourAgents.forElseSub")}
                            control={
                                <Select
                                    value={chosen || undefined}
                                    disabled={!snapshot || choices.length === 0}
                                    onValueChange={(id) => void act(() => SetYourAgent(kind, id))}
                                >
                                    <SelectTrigger className="w-[200px] text-xs" aria-label={label} data-your-agents-job={kind}>
                                        <SelectValue placeholder={t("yourAgents.notChosen")} />
                                    </SelectTrigger>
                                    <SelectContent>
                                        {choices.map((agent) => (
                                            <SelectItem key={agent.id} value={agent.id} className="text-xs">
                                                {agent.name}
                                            </SelectItem>
                                        ))}
                                    </SelectContent>
                                </Select>
                            }
                        />
                    );
                })}
            </SettingsCard>

            <SettingsCard title={t("yourAgents.connectedTitle")}>
                {snapshot && rows.length === 0 && <p className="settings-row-desc" data-your-agents-empty>{t("yourAgents.empty")}</p>}
                {rows.map((row) => (
                    <SettingsRow
                        key={row.id}
                        title={row.name}
                        description={tApp(`agents.job.${row.job}`)}
                        control={
                            <Button variant="outline" size="sm" onClick={() => setDisconnecting(row)} data-your-agents-disconnect={row.id}>
                                {t("yourAgents.disconnect")}
                            </Button>
                        }
                    />
                ))}
                <Button
                    variant="link"
                    className="mt-2 h-auto self-start px-0 py-0.5 text-[14px] no-underline hover:underline"
                    onClick={() => setConnectOpen(true)}
                    data-your-agents-connect
                >
                    <Plus className="size-4" />
                    {t("yourAgents.connect")}
                </Button>
            </SettingsCard>

            <ConnectAgentDialog
                open={connectOpen}
                onOpenChange={setConnectOpen}
                agents={snapshot?.agents ?? null}
                onConnected={() => void reload()}
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
