import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { Loader2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import {
    Dialog,
    DialogContent,
    DialogDescription,
    DialogTitle,
} from "@/components/ui/dialog";
import {
    PlanToolDownload,
    StartToolDownload,
    type ToolDownloadPlan,
} from "@/lib/wails-shim-app";
import { formatBytes } from "@/components/settings/settings-format";

type Phase = "planning" | "review" | "starting" | "failed";

// The shared confirmation sheet for a shipped tool (Windows Git). It never
// starts a download on open, cancel, Escape or dismissal: only the explicit
// confirm handler submits the exact backend-issued plan, once, while a pending
// Start blocks duplicate confirmation. Failures and expired plans require a
// fresh plan and another explicit confirmation.
export function ToolDownloadSheet({
    open,
    onOpenChange,
    onInstalled,
}: {
    open: boolean;
    onOpenChange: (open: boolean) => void;
    onInstalled?: () => void;
}) {
    const { t } = useTranslation("onboarding");
    const [phase, setPhase] = useState<Phase>("planning");
    const [plan, setPlan] = useState<ToolDownloadPlan | null>(null);
    const [failedKind, setFailedKind] = useState<"plan" | "start">("plan");
    const [planVersion, setPlanVersion] = useState(0);
    const requestRef = useRef(0);

    useEffect(() => {
        if (!open) return;
        let live = true;
        const request = ++requestRef.current;
        setPhase("planning");
        setPlan(null);
        void PlanToolDownload("windows-git")
            .then((next) => {
                if (!live || request !== requestRef.current) return;
                setPlan(next);
                setPhase("review");
            })
            .catch(() => {
                if (!live || request !== requestRef.current) return;
                setFailedKind("plan");
                setPhase("failed");
            });
        return () => {
            live = false;
        };
    }, [open, planVersion]);

    const close = () => {
        if (phase === "starting") return;
        onOpenChange(false);
    };

    const confirm = async () => {
        if (phase !== "review" || !plan) return;
        if (plan.expiresAt <= Date.now()) {
            setFailedKind("start");
            setPhase("failed");
            return;
        }
        setPhase("starting");
        try {
            await StartToolDownload(plan);
            setPlan(null);
            onInstalled?.();
            onOpenChange(false);
        } catch {
            setFailedKind("start");
            setPhase("failed");
        }
    };

    const retry = () => {
        setPlan(null);
        setPlanVersion((version) => version + 1);
    };

    const entry = plan?.entry;

    return (
        <Dialog open={open} onOpenChange={(next) => !next && close()}>
            <DialogContent
                className="w-[560px] max-w-[calc(100vw-32px)] max-h-[92dvh]"
                onEscapeKeyDown={(event) => event.stopPropagation()}
            >
                <div className="px-[22px] pt-4 pb-2 flex flex-col gap-2.5">
                    <DialogTitle>
                        {t("tools.sheetTitle", {
                            name: entry?.displayName ?? t("tools.gitName"),
                        })}
                    </DialogTitle>
                    <DialogDescription className="text-[15px] font-semibold text-ink">
                        {t("tools.sheetDescription")}
                    </DialogDescription>
                </div>
                <div className="px-[22px] pb-4 overflow-y-auto flex flex-col gap-3">
                    {phase === "planning" && (
                        <p role="status" className="setup-note flex items-center gap-2">
                            <Loader2 className="size-3.5 animate-spin" aria-hidden />
                            {t("tools.planning")}
                        </p>
                    )}
                    {phase === "failed" && (
                        <p role="alert" className="text-xs text-error">
                            {t(
                                failedKind === "plan"
                                    ? "tools.planFailed"
                                    : "tools.startFailed",
                                { name: entry?.displayName ?? t("tools.gitName") },
                            )}
                        </p>
                    )}
                    {plan && entry && (
                        <div className="setup-list" data-testid="tool-download-plan">
                            <div className="px-3.5 py-2.5 border-t first:border-t-0 border-border-subtle flex flex-col gap-1">
                                <div className="flex justify-between gap-2 text-[12.5px]">
                                    <span className="font-semibold">
                                        {entry.displayName}
                                    </span>
                                    <span className="text-muted">
                                        {t("tools.itemSize", {
                                            size: formatBytes(entry.sizeBytes),
                                        })}
                                    </span>
                                </div>
                                <div className="flex items-center gap-2 text-xs text-muted">
                                    <span>{t("tools.version", { version: entry.version })}</span>
                                </div>
                                <div className="flex items-center gap-2 text-xs text-muted">
                                    <span>{t("tools.source")}</span>
                                    <a
                                        className="truncate text-ink underline underline-offset-2"
                                        href={entry.source}
                                        target="_blank"
                                        rel="noreferrer"
                                        title={entry.source}
                                    >
                                        {entry.source}
                                    </a>
                                </div>
                                <div className="flex items-center gap-2 text-xs text-muted">
                                    <span>{t("tools.licence")}</span>
                                    <a
                                        className="whitespace-nowrap text-ink underline underline-offset-2"
                                        href={entry.licenceUrl}
                                        target="_blank"
                                        rel="noreferrer"
                                    >
                                        {entry.licence}
                                    </a>
                                </div>
                                <div className="flex items-center gap-2 text-xs text-muted">
                                    <span>{t("tools.saveTo")}</span>
                                    <code
                                        className="flex-1 truncate text-ink"
                                        title={plan.folder}
                                    >
                                        {plan.folder}
                                    </code>
                                </div>
                            </div>
                        </div>
                    )}
                </div>
                <div className="border-t border-border-subtle px-[22px] py-4 flex justify-end gap-2">
                    <Button
                        variant="outline"
                        disabled={phase === "starting"}
                        onClick={close}
                    >
                        {t("tools.cancel")}
                    </Button>
                    {phase === "failed" ? (
                        <Button onClick={retry}>{t("tools.retry")}</Button>
                    ) : (
                        <Button
                            data-testid="tool-download-confirm"
                            disabled={phase !== "review"}
                            onClick={() => void confirm()}
                        >
                            {phase === "starting" ? (
                                <>
                                    <Loader2 className="size-3.5 animate-spin" aria-hidden />
                                    {t("tools.pending")}
                                </>
                            ) : (
                                t("tools.confirm")
                            )}
                        </Button>
                    )}
                </div>
            </DialogContent>
        </Dialog>
    );
}
