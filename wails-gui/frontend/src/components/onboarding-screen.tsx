import { useCallback, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import type { OnboardingState } from "@/types";
import { LoadUIState, SaveUIState } from "../../wailsjs/go/main/App";
import { CloseMainChatSetupSteps } from "@/lib/wails-shim-app";
import "./onboarding/v2/setup-v2.css";
import { SetupIcon, SetupIconSprite } from "./onboarding/v2/SetupIcons";
import { prefersReducedMotion } from "./onboarding/v2/motion";
import { revealMainWindow, setSetupWindowMode } from "./onboarding/v2/native";
import {
    SETUP_V2_SCREENS,
    type SetupV2Screen,
    type SetupV2Still,
} from "./onboarding/v2/types";
import { NoticesScene } from "./onboarding/v2/scenes/NoticesScene";
import { YourAIScene } from "./onboarding/v2/scenes/YourAIScene";
import { SeesScene, SEES_ROWS } from "./onboarding/v2/scenes/SeesScene";
import { RunsScene } from "./onboarding/v2/scenes/RunsScene";
import { SendScene } from "./onboarding/v2/scenes/SendScene";
import { ReachesScene } from "./onboarding/v2/scenes/ReachesScene";
import { FinishScene } from "./onboarding/v2/scenes/FinishScene";
import { OnThisMac } from "./onboarding/v2/OnThisMac";
import { EventsEmit } from "@/lib/wails-shim-runtime";

// First-run setup v2 (docs/handoff-2026-10-28/handoff-2026-10-28-1215-setup-v2.0.0.md,
// updated by handoff-2026-10-28-1315-setup-v2.1.0.md and
// docs/handoff-2026-10-29/handoff-2026-10-29-1523-setup-v2.2.0.md): six screens in a fixed
// 600 x 560 window, each with the "On this computer" pill, then the finish, where
// the icon flies into the menu bar and the main window opens on Chat.

/**
 * The screen the user was on, kept in ui_state so the reopen macOS forces
 * after Screen Recording comes back to the same screen.
 */
const ONBOARDING_STEP_STORAGE_KEY = "yansu.onboarding.step";

/**
 * How long finishing waits for the main chat to hear what setup already
 * asked, before it opens Isoform anyway.
 */
const CHAT_SETUP_HANDOFF_TIMEOUT_MS = 1500;

const handoffTimeout = () =>
    new Promise((resolve) => setTimeout(resolve, CHAT_SETUP_HANDOFF_TIMEOUT_MS));

/** Where a step saved by the setup before v2 resumes. */
export function resumeScreen(saved: string): SetupV2Screen | null {
    if ((SETUP_V2_SCREENS as readonly string[]).includes(saved)) {
        return saved as SetupV2Screen;
    }
    switch (saved) {
        case "welcome":
        case "dependencies":
        case "agent":
            return "notices";
        case "permissions":
        case "notifications":
        case "models":
            return "sees";
        case "memory-import":
        case "first-task":
            return "reaches";
        default:
            return null;
    }
}

const SEES_KEYS = SEES_ROWS.flatMap((row) => row.keys);

type Stage = SetupV2Screen | "finish";

interface OnboardingScreenProps {
    state: OnboardingState;
    onStateChange: (next: OnboardingState) => void;
    /** Ends setup; the main window opens on Chat. */
    onFinish: (firstTaskPrompt?: string) => void;
    /** Escape: leaves setup for this session. */
    onSkip: () => void;
    /** Development gallery: one frozen state, no backend reads or writes. */
    still?: SetupV2Still;
}

function stageForStill(still: SetupV2Still): Stage {
    if (still === "on-this-mac") return "notices";
    if (still.startsWith("ai")) return "ai";
    if (still.startsWith("sees")) return "sees";
    if (still.startsWith("runs")) return "runs";
    if (still.startsWith("reaches")) return "reaches";
    if (still.startsWith("finish")) return "finish";
    return still as SetupV2Screen;
}

export function OnboardingScreen({
    state,
    onStateChange,
    onFinish,
    onSkip,
    still,
}: OnboardingScreenProps) {
    const { t } = useTranslation("onboarding");
    const [stage, setStage] = useState<Stage>(() => (still ? stageForStill(still) : "notices"));
    // The first screen appears without the rise-in; later moves play it,
    // or the 0.2 s crossfade under Reduce Motion.
    const [moved, setMoved] = useState(false);
    const [closing, setClosing] = useState(false);
    const [reduced, setReduced] = useState(prefersReducedMotion);
    const live = !still;

    useEffect(() => {
        const query = window.matchMedia?.("(prefers-reduced-motion: reduce)");
        if (!query) return;
        const update = () => setReduced(query.matches);
        query.addEventListener?.("change", update);
        return () => query.removeEventListener?.("change", update);
    }, []);

    // False until the saved screen has been read: Wails runs each binding on
    // its own goroutine, so an early write could land after the read and
    // overwrite the screen to resume.
    const [stepRestored, setStepRestored] = useState(false);
    useEffect(() => {
        if (!live) return;
        let mounted = true;
        LoadUIState(ONBOARDING_STEP_STORAGE_KEY)
            .then((saved) => {
                const resumed = mounted && saved ? resumeScreen(saved) : null;
                if (resumed) setStage((current) => (current === "notices" ? resumed : current));
            })
            .catch(() => {})
            .finally(() => {
                if (mounted) setStepRestored(true);
            });
        return () => {
            mounted = false;
        };
    }, [live]);

    useEffect(() => {
        if (!live || !stepRestored || stage === "finish") return;
        void SaveUIState(ONBOARDING_STEP_STORAGE_KEY, stage).catch(() => {});
    }, [live, stepRestored, stage]);

    // The fixed setup window.
    useEffect(() => {
        if (!live) return;
        void setSetupWindowMode(true);
    }, [live]);

    // What setup asked closes the same ask in the main chat, so Isoform never
    // asks for it again right after (main_chat_setup.go). A send waits for the
    // earlier sends about the same steps, so a changed mind always lands last.
    const chatSetupSends = useRef(new Map<string, Promise<void>>());
    const closeChatSetupSteps = useCallback((steps: string[]) => {
        const sends = chatSetupSends.current;
        const done = Promise.all(steps.map((key) => sends.get(key)))
            .then(() => CloseMainChatSetupSteps(steps))
            .then(
                () => {},
                () => {},
            );
        for (const key of steps) sends.set(key, done);
    }, []);

    const go = useCallback((next: Stage) => {
        setMoved(true);
        setStage(next);
    }, []);

    const next = useCallback(() => {
        if (!live) return;
        if (stage === "sees") {
            closeChatSetupSteps(SEES_KEYS);
            onStateChange({ ...state, permissionsComplete: true });
        }
        if (stage === "reaches") {
            closeChatSetupSteps(["notifications", "first_task"]);
            void SaveUIState(ONBOARDING_STEP_STORAGE_KEY, "").catch(() => {});
            go("finish");
            return;
        }
        const i = SETUP_V2_SCREENS.indexOf(stage as SetupV2Screen);
        if (i >= 0) go(SETUP_V2_SCREENS[i + 1]);
    }, [live, stage, state, onStateChange, closeChatSetupSteps, go]);

    const back = useCallback(() => {
        const i = SETUP_V2_SCREENS.indexOf(stage as SetupV2Screen);
        if (i > 0) go(SETUP_V2_SCREENS[i - 1]);
    }, [stage, go]);

    const finishing = useRef(false);
    const finished = useCallback(async () => {
        if (finishing.current) return;
        finishing.current = true;
        await Promise.race([Promise.all(chatSetupSends.current.values()), handoffTimeout()]);
        void setSetupWindowMode(false);
        onFinish();
        // Let the main window paint Chat before it fades in.
        requestAnimationFrame(() => requestAnimationFrame(() => void revealMainWindow()));
    }, [onFinish]);

    useEffect(() => {
        if (!live) return;
        const handleKeyDown = (e: KeyboardEvent) => {
            // An Escape that closes an input method's candidate list is not a
            // skip: WKWebView ends a composition with isComposing=false and
            // keyCode 229. An Escape a screen already handled is not one either.
            if (e.key !== "Escape" || e.isComposing || e.keyCode === 229 || e.defaultPrevented) {
                return;
            }
            onSkip();
        };
        window.addEventListener("keydown", handleKeyDown);
        return () => window.removeEventListener("keydown", handleKeyDown);
    }, [live, onSkip]);

    const index = SETUP_V2_SCREENS.indexOf(stage as SetupV2Screen);
    const enter = moved ? (reduced ? "fade" : "enter") : "";
    const sceneProps = { onNext: next, reduced, still, enter };

    return (
        <div className={`sv2${still ? " still" : ""}`} data-testid="setup-v2">
            <SetupIconSprite />
            <div className="drag" />
            <div className={`win${closing ? " closing" : ""}`}>
                {stage !== "finish" && (
                    <>
                        <div
                            className="prog"
                            role="progressbar"
                            aria-label={t("v2.progress")}
                            aria-valuemin={1}
                            aria-valuemax={SETUP_V2_SCREENS.length}
                            aria-valuenow={index + 1}
                            aria-valuetext={t("v2.stepCount", {
                                current: index + 1,
                                total: SETUP_V2_SCREENS.length,
                            })}
                        >
                            {SETUP_V2_SCREENS.map((key, k) => (
                                <i key={key} className={k < index ? "was" : k === index ? "on" : undefined} />
                            ))}
                        </div>
                        {index > 0 && (
                            <button
                                className="back"
                                type="button"
                                data-testid="setup-v2-back"
                                aria-label={t("v2.back")}
                                onClick={back}
                            >
                                <SetupIcon id="i-chev" />
                            </button>
                        )}
                        <OnThisMac initiallyOpen={still === "on-this-mac"} onNavigate={(job) => {
                            onSkip();
                            window.setTimeout(() => {
                                if (job === "contacts") EventsEmit("settings:open", "activity", { anchor: "sees-messages" });
                                else EventsEmit("settings:open", "activity", { anchor: `sees-${job}` });
                            }, 0);
                        }} />
                    </>
                )}
                {stage === "notices" ? (
                    <NoticesScene key="notices" {...sceneProps} />
                ) : stage === "ai" ? (
                    <YourAIScene key="ai" {...sceneProps} />
                ) : stage === "sees" ? (
                    <SeesScene key="sees" {...sceneProps} />
                ) : stage === "runs" ? (
                    <RunsScene key="runs" {...sceneProps} />
                ) : stage === "send" ? (
                    <SendScene key="send" {...sceneProps} />
                ) : stage === "reaches" ? (
                    <ReachesScene key="reaches" {...sceneProps} />
                ) : (
                    <FinishScene
                        reduced={reduced}
                        still={!!still}
                        onClosing={() => setClosing(true)}
                        onDone={() => void finished()}
                    />
                )}
            </div>
        </div>
    );
}
