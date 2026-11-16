import { useCallback, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { GetForgetEverything, StartForgetEverything, type ForgetEverythingStatus } from "@/lib/wails-shim-app";
import { EventsOn } from "../../../wailsjs/runtime/runtime";
import { Button } from "@/components/ui/button";
import { ConfirmDialog } from "@/components/confirm-dialog";
import { SettingsCard, SettingsRow } from "./settings-primitives";
import { useSettingsFailure } from "./settings-failure";

/** Settings › Your data › Forget everything (computer-data v1.0.0 rule 6):
 *  last on the page, in red, asked first. One job removes everything Isoform
 *  keeps about the person, store by store; it survives a quit, and a step
 *  that waits (the Agent of You server, a busy store) says so. Settings, the
 *  phone pairing and the app stay. */
export function ForgetEverythingCard() {
    const { t } = useTranslation("settings");
    const [status, setStatus] = useState<ForgetEverythingStatus | null>(null);
    const [asking, setAsking] = useState(false);
    const { failure, fail, clear } = useSettingsFailure();

    useEffect(() => {
        GetForgetEverything().then(setStatus).catch(() => {});
        return EventsOn("your-data:forget", (next: ForgetEverythingStatus) => setStatus(next));
    }, []);

    const start = useCallback(() => {
        clear();
        StartForgetEverything()
            .then(setStatus)
            .catch(() => fail(t("yourData.forgetFailed"), start));
    }, [clear, fail, t]);

    // A step a newer build added may have no words yet; its id stands in.
    const stepName = (id: string) => {
        const key = `yourData.forgetSteps.${id}`;
        const name = t(key as "yourData.forgetSteps.memory");
        return name === key ? id : name;
    };
    const steps = status?.steps ?? [];
    const done = steps.filter((s) => s.state === "done").length;
    const waiting = steps.filter((s) => s.state === "failed");
    const state = status?.state ?? "idle";
    const description =
        state === "running"
            ? t("yourData.forgetRunning", { done, total: steps.length })
            : state === "pending"
              ? t("yourData.forgetPending", { count: waiting.length })
              : state === "done"
                ? t("yourData.forgetDone")
                : t("yourData.forgetSub");

    return (
        <SettingsCard title={t("yourData.forgetTitle")}>
            <div data-your-data-row="forget">
                <SettingsRow
                    title={t("yourData.forget")}
                    description={description}
                    failure={failure}
                    control={
                        state === "running" ? undefined : (
                            <Button size="sm" variant="destructive" onClick={() => (state === "pending" ? start() : setAsking(true))}>
                                {state === "pending" ? t("yourData.forgetRetry") : t("yourData.forgetButton")}
                            </Button>
                        )
                    }
                />
                {waiting.length > 0 && (
                    <ul className="px-4 pb-3 text-[13px] text-muted" data-testid="forget-waiting">
                        {waiting.map((s) => (
                            <li key={s.id}>{t("yourData.forgetWaitingStep", { step: stepName(s.id) })}</li>
                        ))}
                    </ul>
                )}
            </div>
            <ConfirmDialog
                open={asking}
                onOpenChange={setAsking}
                title={t("yourData.forgetConfirmTitle")}
                description={t("yourData.forgetConfirm")}
                confirmLabel={t("yourData.forgetConfirmButton")}
                variant="danger"
                onConfirm={start}
            />
        </SettingsCard>
    );
}
