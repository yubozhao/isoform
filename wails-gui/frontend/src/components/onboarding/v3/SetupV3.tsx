import { useCallback, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { Check, CornerDownLeft } from "lucide-react";
import { LoadUIState, SaveUIState } from "../../../../wailsjs/go/main/App";
import { Button } from "@/components/ui/button";
import { CloseMainChatSetupSteps } from "@/lib/wails-shim-app";
import { revealMainWindow, setSetupWindowMode } from "../v2/native";
import { IntroScreen } from "./IntroScreen";
import { SeesStep } from "./SeesStep";
import { AgentsStep } from "./LaterSteps";
import { TodayStep } from "./TodayStep";
import { RightStep } from "./RightStep";
import { PhoneStep } from "./PhoneStep";
import {
    resumeStageV3,
    SEES_V3_ROWS,
    SETUP_V3_INTRO,
    SETUP_V3_STEPS,
    type SetupV3Stage,
    type SetupV3Step,
} from "./steps";
import "./setup-v3.css";

// Computer setup v1.0.0 (docs/handoff-2026-11-15/handoff-2026-11-15-2100-computer-setup-v1.0.0.md):
// four screens that ask nothing, then five steps in a rail, each skippable,
// and Finish setup lands on Today. The stage is saved in ui_state so the
// reopen macOS forces after Screen Recording comes back to the same step.

const STAGE_KEY = "yansu.onboarding.step";

/** What step 1 asked; the main chat then never asks for it again (main_chat_setup.go). */
const SEES_CHAT_KEYS = SEES_V3_ROWS.filter((row) => row.id !== "notifications").flatMap((row) => row.keys);

const STEP_NAMES = {
    sees: "setup.step1",
    right: "setup.step2",
    today: "setup.step3",
    agents: "setup.step4",
    phone: "setup.step5",
} as const;

export function SetupV3({ onFinish, onSkip }: { onFinish: () => void; onSkip: () => void }) {
    const { t } = useTranslation("onboarding");
    const [stage, setStage] = useState<SetupV3Stage>({ kind: "intro", screen: 1 });
    const [done, setDone] = useState<Set<SetupV3Step>>(new Set());
    const [restored, setRestored] = useState(false);

    // The saved stage is read before anything is written, so an early write
    // never overwrites the step a reopen should come back to.
    useEffect(() => {
        let mounted = true;
        LoadUIState(STAGE_KEY)
            .then((saved) => {
                const resumed = mounted && saved ? resumeStageV3(saved) : null;
                if (resumed) setStage(resumed);
            })
            .catch(() => {})
            .finally(() => mounted && setRestored(true));
        return () => {
            mounted = false;
        };
    }, []);

    useEffect(() => {
        if (!restored || stage.kind !== "step") return;
        void SaveUIState(STAGE_KEY, stage.step).catch(() => {});
    }, [restored, stage]);

    useEffect(() => {
        void setSetupWindowMode(true);
    }, []);

    const finish = useCallback(() => {
        void CloseMainChatSetupSteps(["notifications", "first_task"]).catch(() => {});
        void SaveUIState(STAGE_KEY, "").catch(() => {});
        void setSetupWindowMode(false);
        onFinish();
        requestAnimationFrame(() => requestAnimationFrame(() => void revealMainWindow()));
    }, [onFinish]);

    const nextStep = useCallback(
        (step: SetupV3Step, answered: boolean) => {
            if (answered) setDone((d) => new Set(d).add(step));
            if (answered && step === "sees") void CloseMainChatSetupSteps(SEES_CHAT_KEYS).catch(() => {});
            const i = SETUP_V3_STEPS.indexOf(step);
            if (i === SETUP_V3_STEPS.length - 1) {
                finish();
                return;
            }
            setStage({ kind: "step", step: SETUP_V3_STEPS[i + 1] });
        },
        [finish],
    );

    const advance = useCallback(() => {
        if (stage.kind === "intro") {
            const i = SETUP_V3_INTRO.indexOf(stage.screen);
            setStage(
                i < SETUP_V3_INTRO.length - 1
                    ? { kind: "intro", screen: SETUP_V3_INTRO[i + 1] }
                    : { kind: "step", step: SETUP_V3_STEPS[0] },
            );
            return;
        }
        nextStep(stage.step, true);
    }, [stage, nextStep]);

    // Return is Continue; Escape leaves setup for this session, as in setup v2.
    useEffect(() => {
        const onKey = (e: KeyboardEvent) => {
            if (e.isComposing || e.keyCode === 229 || e.defaultPrevented) return;
            const target = e.target instanceof Element ? e.target : null;
            if (target?.closest("input, textarea, button, [contenteditable]")) return;
            if (e.key === "Enter") advance();
            else if (e.key === "Escape") onSkip();
        };
        window.addEventListener("keydown", onKey);
        return () => window.removeEventListener("keydown", onKey);
    }, [advance, onSkip]);

    if (stage.kind === "intro") {
        return (
            <div className="sv3" data-testid="setup-v3">
                <div className="sv3-drag" />
                <IntroScreen
                    screen={stage.screen}
                    onContinue={advance}
                    onSkip={() => setStage({ kind: "step", step: SETUP_V3_STEPS[0] })}
                />
            </div>
        );
    }

    const step = stage.step;
    const last = step === SETUP_V3_STEPS[SETUP_V3_STEPS.length - 1];
    return (
        <div className="sv3" data-testid="setup-v3">
            <div className="sv3-drag" />
            <div className="sv3-layout">
                <nav className="sv3-rail" aria-label={t("setup.railTitle")}>
                    <p className="sv3-rail-title">{t("setup.railTitle")}</p>
                    <ol>
                        {SETUP_V3_STEPS.map((s, i) => (
                            <li
                                key={s}
                                className={s === step ? "on" : undefined}
                                aria-current={s === step ? "step" : undefined}
                                data-testid={`setup-v3-rail-${s}`}
                            >
                                <span className="sv3-rail-mark">
                                    {done.has(s) ? <Check size={12} aria-label={t("setup.allowed")} /> : i + 1}
                                </span>
                                {t(STEP_NAMES[s])}
                            </li>
                        ))}
                    </ol>
                    <p className="sv3-rail-foot">{t("setup.railFoot")}</p>
                </nav>
                <main className="sv3-main">
                    {step === "sees" ? (
                        <SeesStep />
                    ) : step === "right" ? (
                        <RightStep />
                    ) : step === "today" ? (
                        <TodayStep />
                    ) : step === "agents" ? (
                        <AgentsStep onNext={() => nextStep("agents", true)} />
                    ) : (
                        <PhoneStep />
                    )}
                    <div className="sv3-actions">
                        <Button variant="quiet" onClick={() => nextStep(step, false)} data-testid="setup-v3-skip-step">
                            {t("setup.skipStep")}
                        </Button>
                        <Button onClick={() => nextStep(step, true)} data-testid="setup-v3-next">
                            {last ? t("setup.finish") : t("ob.continue")}
                            <CornerDownLeft size={14} aria-hidden />
                        </Button>
                    </div>
                </main>
            </div>
        </div>
    );
}
