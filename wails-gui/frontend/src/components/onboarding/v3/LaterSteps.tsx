import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Button } from "@/components/ui/button";
import { useAgentProbe } from "../v2/useAgentProbe";
import { YourAIScene } from "../v2/scenes/YourAIScene";
import { prefersReducedMotion } from "../v2/motion";
import "../v2/setup-v2.css";

/** Product names stay untranslated. */
const AGENT_NAMES = { claude: "Claude Code", codex: "Codex", ollama: "Ollama" } as const;

// Step 4 opens on the agent Isoform thinks with (setup v2's Your AI, kept
// whole: install, sign-in and the test call). Change opens it in place; with
// no agent signed in it starts open.
export function AgentsStep({ onNext }: { onNext: () => void }) {
    const { t } = useTranslation("onboarding");
    const { probe } = useAgentProbe(true);
    const thinker = probe?.agents.find((agent) => agent.installed && agent.signedIn);
    const [open, setOpen] = useState(false);
    const showScene = open || (probe !== null && !thinker);
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
        </div>
    );
}
