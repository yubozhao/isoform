import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import {
    DeferModelDownloads,
    GetModelChoices,
    PlanSetupModelDownload,
    StartModelDownloads,
} from "@/lib/wails-shim-app";
import type { ModelChoices, ModelDownloadPlan } from "@/lib/setup-backend";
import { Button } from "@/components/ui/button";
import { chosenModels, formatModelBytes, planRequest } from "../v2/models";

// The models that read the screen and calls on this computer (setup v2.2.0's
// Runs on this computer), as one line in step 1: Download now, or Later,
// which waits until the computer is plugged in and idle. Leaving the step
// without either is Later, so the models still come.
export function ModelsLine({ micOn }: { micOn: boolean }) {
    const { t, i18n } = useTranslation("onboarding");
    const [choices, setChoices] = useState<ModelChoices | null>(null);
    const [plan, setPlan] = useState<ModelDownloadPlan | null>(null);
    const [state, setState] = useState<"choose" | "downloading" | "later" | "failed">("choose");
    const decided = useRef(false);
    const planRef = useRef<ModelDownloadPlan | null>(null);

    useEffect(() => {
        let live = true;
        GetModelChoices()
            .then((next) => live && setChoices(next))
            .catch(() => {});
        return () => {
            live = false;
        };
    }, []);

    const chosen = useMemo(() => (choices ? chosenModels(choices, i18n.language) : {}), [choices, i18n.language]);

    useEffect(() => {
        if (!choices || decided.current) return;
        let live = true;
        const afterCall = choices.jobs.find((job) => job.job === "afterCall")?.chosenId !== "none";
        const { choices: wire, skip } = planRequest(chosen, {
            calls: micOn,
            afterCall: micOn && afterCall,
            voice: true,
            always: true,
        });
        PlanSetupModelDownload(wire, skip)
            .then((next) => {
                if (!live) return;
                setPlan(next);
                planRef.current = next;
            })
            .catch(() => {});
        return () => {
            live = false;
        };
    }, [choices, chosen, micOn]);

    useEffect(
        () => () => {
            const pending = planRef.current;
            if (!decided.current && pending && pending.items.length > 0) {
                void DeferModelDownloads(pending).catch(() => {});
            }
        },
        [],
    );

    const download = useCallback(async () => {
        if (!plan) return;
        decided.current = true;
        try {
            await StartModelDownloads(plan);
            setState("downloading");
        } catch {
            decided.current = false;
            setState("failed");
        }
    }, [plan]);

    const later = useCallback(async () => {
        if (!plan) return;
        decided.current = true;
        await DeferModelDownloads(plan).catch(() => {});
        setState("later");
    }, [plan]);

    if (!plan || plan.items.length === 0) return null;
    return (
        <div className="sv3-row sv3-models" data-testid="setup-v3-models">
            <div>
                <p className="sv3-strong">{t("v2.runs.title")}</p>
                <p className="sv3-quiet">
                    {state === "downloading"
                        ? t("v2.runs.downloading")
                        : state === "later"
                          ? t("setup.modelsLater")
                          : t("v2.runs.total", {
                                total: formatModelBytes(plan.totalBytes),
                                free: formatModelBytes(plan.freeBytes),
                            })}
                </p>
            </div>
            {(state === "choose" || state === "failed") && (
                <span className="sv3-answers">
                    <Button size="sm" variant="outline" onClick={() => void later()}>
                        {t("setup.modelsLaterButton")}
                    </Button>
                    <Button size="sm" disabled={!plan.enoughSpace} onClick={() => void download()}>
                        {t("setup.modelsDownload")}
                    </Button>
                </span>
            )}
        </div>
    );
}
