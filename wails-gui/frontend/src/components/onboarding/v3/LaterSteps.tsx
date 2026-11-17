import { useCallback, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { Button } from "@/components/ui/button";
import { GetYourAgents, SetYourAgent, type YourAgents } from "@/lib/wails-shim-app";
import { ConnectAgentPanel } from "@/components/agents/ConnectAgentDialog";
import { useAgentProbe } from "../v2/useAgentProbe";
import { YourAIScene } from "../v2/scenes/YourAIScene";
import { prefersReducedMotion } from "../v2/motion";
import "../v2/setup-v2.css";

/** Product names stay untranslated. */
const AGENT_NAMES = { claude: "Claude Code", codex: "Codex", ollama: "Ollama" } as const;

// Step 4 · Your agents (computer setup v1.0.0 rule 7): the agent Isoform
// thinks with (setup v2's Your AI, kept whole), then Connect an agent's body,
// shared with the Agents page: the agents found on this computer with Connect
// and its confirmation, and the one message for any other agent. The first
// agent connected takes the job its kind suggests ("Connected · for code").
export function AgentsStep({ onNext }: { onNext: () => void }) {
    const { t } = useTranslation("onboarding");
    const { probe } = useAgentProbe(true);
    const thinker = probe?.agents.find((agent) => agent.installed && agent.signedIn);
    const [open, setOpen] = useState(false);
    const showScene = open || (probe !== null && !thinker);
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
            {thinker && !showScene && (
                <div className="sv3-row sv3-models" data-testid="setup-v3-thinker">
                    <p className="sv3-strong">{t("setup.thinksWith", { agent: AGENT_NAMES[thinker.id] })}</p>
                    <Button size="sm" variant="outline" onClick={() => setOpen(true)}>
                        {t("setup.change")}
                    </Button>
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
