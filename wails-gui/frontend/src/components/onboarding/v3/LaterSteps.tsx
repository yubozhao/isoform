import { useCallback, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { useFeatureSwitches } from "@/hooks/useFeatureSwitches";
import { Button } from "@/components/ui/button";
import {
    GetYourAgents,
    OpenAssistantSignIn,
    SetupInstallAssistant,
    SetYourAgent,
    type YourAgents,
} from "@/lib/wails-shim-app";
import { ConnectAgentPanel } from "@/components/agents/ConnectAgentDialog";
import { useAgentProbe } from "../v2/useAgentProbe";
import { YourAIScene } from "../v2/scenes/YourAIScene";
import { prefersReducedMotion } from "../v2/motion";
import "../v2/setup-v2.css";

/** Product names stay untranslated. */
const AGENT_NAMES = { claude: "Claude Code", codex: "Codex", ollama: "Ollama" } as const;

// Step 4 · Your agents (computer setup v1.0.0 rule 7). The top row is the
// assistant that reads your day (setup-answers answer 3, never "thinks with",
// never "agent"): signed in, or Install or Sign in, which opens the
// assistant's own sign-in. Change, or an install or sign-in that could not be
// done here, opens setup v2's Your AI, kept whole. Then Connect an agent's body,
// shared with the Agents page: the agents found on this computer with Connect
// and its confirmation, and the one message for any other agent. The first
// agent connected takes the job its kind suggests ("Connected · for code").
export function AgentsStep({ onNext }: { onNext: () => void }) {
    const { t } = useTranslation("onboarding");
    const chatOn = useFeatureSwitches().chat;
    const { probe, refresh } = useAgentProbe(true);
    const signedIn = probe?.agents.find((agent) => agent.installed && agent.signedIn);
    // The assistant the row names: the one signed in, else one installed, else Claude Code.
    const assistant = signedIn ?? probe?.agents.find((agent) => agent.installed) ?? null;
    const assistantId = assistant?.id ?? "claude";
    const [open, setOpen] = useState(false);
    const [busy, setBusy] = useState(false);
    const showScene = open;

    const install = useCallback(async () => {
        setBusy(true);
        try {
            const r = await SetupInstallAssistant(assistantId);
            if (!r.installed && !r.signedIn) setOpen(true);
            else void refresh();
        } catch {
            setOpen(true);
        } finally {
            setBusy(false);
        }
    }, [assistantId, refresh]);

    const signIn = useCallback(async () => {
        try {
            await OpenAssistantSignIn(assistantId);
        } catch {
            setOpen(true);
        }
    }, [assistantId]);
    const [agents, setAgents] = useState<YourAgents | null>(null);
    const [jobs, setJobs] = useState<Record<string, string>>({});

    useEffect(() => {
        GetYourAgents().then(setAgents).catch(() => {});
    }, []);

    const connected = useCallback(async (agentId: string) => {
        try {
            let next = await GetYourAgents();
            const agent = next.agents.find((a) => a.id === agentId);
            if (agent && !next.choices[agent.kind]) {
                next = await SetYourAgent(agent.kind, agent.id);
            }
            setAgents(next);
            if (agent && next.choices[agent.kind] === agent.id) {
                setJobs((j) => ({ ...j, [agent.id]: agent.kind }));
            }
        } catch {
            /* The agent stays connected; its job can be chosen in Your agents. */
        }
    }, []);

    return (
        <div className="sv3-step" data-testid="setup-v3-agents">
            <h2 className="sv3-step-title">{t("setup.step4")}</h2>
            {probe && !showScene && (
                <div className="sv3-row sv3-models" data-testid="setup-v3-assistant">
                    <div>
                        <p className="sv3-strong">{t(chatOn ? "setup.assistant" : "setup.assistantNoChat", { assistant: AGENT_NAMES[assistantId] })}</p>
                        {signedIn && <p className="sv3-quiet">{t("setup.assistantOk")}</p>}
                    </div>
                    {signedIn ? (
                        <Button size="sm" variant="outline" onClick={() => setOpen(true)}>
                            {t("setup.change")}
                        </Button>
                    ) : assistant ? (
                        <Button size="sm" onClick={() => void signIn()} data-testid="setup-v3-assistant-sign-in">
                            {t("setup.signIn")}
                        </Button>
                    ) : (
                        <Button size="sm" disabled={busy} onClick={() => void install()} data-testid="setup-v3-assistant-install">
                            {t("setup.install")}
                        </Button>
                    )}
                </div>
            )}
            {showScene && (
                <div className="sv2 sv3-embed" data-testid="setup-v3-your-ai">
                    <div className="win">
                        <YourAIScene onNext={onNext} reduced={prefersReducedMotion()} enter="" />
                    </div>
                </div>
            )}
            <p className="sv3-body">{t("setup.agentsLead")}</p>
            <div className="sv3-connect" data-testid="setup-v3-connect">
                <ConnectAgentPanel active agents={agents} onConnected={(id) => void connected(id)} />
            </div>
            {Object.entries(jobs).map(([id, kind]) => (
                <p key={id} className="sv3-allowed" data-testid={`setup-v3-job-${id}`}>
                    {t(kind === "code" ? "setup.connectedForCode" : "setup.connectedForElse", {
                        agent: agents?.agents.find((a) => a.id === id)?.name ?? id,
                    })}
                </p>
            ))}
        </div>
    );
}
