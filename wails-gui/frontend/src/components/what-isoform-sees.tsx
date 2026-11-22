import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { GetWhatIsoformSees, type WhatIsoformSees } from "@/lib/wails-shim-app";
import { EventsEmit } from "@/lib/wails-shim-runtime";

const jobs = ["mail", "calendar", "calls", "screen", "assistant", "contacts"] as const;
type Job = typeof jobs[number];

export function WhatIsoformSeesSheet({ open, onOpenChange, onNavigate }: {
    open: boolean;
    onOpenChange: (open: boolean) => void;
    onNavigate?: (job: Job) => void;
}) {
    const { t } = useTranslation("app");
    const [view, setView] = useState<WhatIsoformSees | null>(null);
    const [failed, setFailed] = useState(false);
    const [attempt, setAttempt] = useState(0);
    useEffect(() => {
        if (!open) return;
        let active = true;
        setView(null);
        setFailed(false);
        GetWhatIsoformSees().then((next) => {
            if (!active) return;
            if (!next || !Array.isArray(next.sources)) setFailed(true);
            else setView(next);
        }).catch(() => { if (active) setFailed(true); });
        return () => { active = false; };
    }, [open, attempt]);
    const openSources = () => {
        onOpenChange(false);
        EventsEmit("settings:open", "activity");
    };
    const navigate = (job: Job) => {
        onOpenChange(false);
        if (onNavigate) onNavigate(job);
        else if (job === "contacts") EventsEmit("settings:open", "activity", { anchor: "sees-messages" });
        else EventsEmit("settings:open", "activity", { anchor: `sees-${job}` });
    };
    return (
        <Dialog open={open} onOpenChange={onOpenChange}>
            <DialogContent data-what-isoform-sees className="max-w-[calc(100vw-32px)]" onEscapeKeyDown={(event) => { event.preventDefault(); onOpenChange(false); }}>
                <DialogHeader>
                    <div><DialogTitle>{t("whatIsoformSees.title")}</DialogTitle><DialogDescription>{t("whatIsoformSees.subtitle")}</DialogDescription></div>
                    <Button variant="quiet" size="sm" onClick={() => onOpenChange(false)}>{t("whatIsoformSees.close")}</Button>
                </DialogHeader>
                <div className="overflow-y-auto px-6 pb-6">
                    {failed && <p role="alert" className="py-4 text-sm text-copper">{t("whatIsoformSees.loadFailed")} <button type="button" className="text-accent hover:underline" onClick={() => setAttempt((n) => n + 1)}>{t("whatIsoformSees.retry")}</button></p>}
                    {view && <>
                        <div className="divide-y divide-border rounded-xl border border-border">
                            {jobs.map((job) => {
                                const source = view.sources.find((row) => row.job === job);
                                if (!source) return null;
                                const name = job === "assistant" ? (view.assistant === "codex" ? t("whatIsoformSees.names.codex") : t("whatIsoformSees.names.claude")) : t(`whatIsoformSees.names.${job}`);
                                const detail = t(`whatIsoformSees.${job}.${source.on || job === "assistant" ? "on" : "off"}` as never);
                                return <div className="flex items-center gap-3 px-4 py-3" key={job}>
                                    <div className="min-w-0 flex-1"><h3 className="text-sm font-semibold text-ink">{name}</h3><p className="text-xs leading-relaxed text-muted">{detail}</p></div>
                                    {source.on ? <span className="text-xs text-muted">{t("whatIsoformSees.on")}</span> : <Button variant="secondary" size="sm" onClick={() => navigate(job)}>{t("whatIsoformSees.turnOn")}</Button>}
                                </div>;
                            })}
                        </div>
                        <h3 className="mt-5 text-xs font-semibold uppercase text-muted">{t("whatIsoformSees.leaves.title")}</h3>
                        <div className="mt-2 space-y-2 rounded-xl border border-border px-4 py-3 text-sm text-ink">
                            <p>{t(view.assistant === "codex" ? "whatIsoformSees.leaves.ai.codex" : view.assistant === "claude_code" ? "whatIsoformSees.leaves.ai.claude" : "whatIsoformSees.leaves.ai.isoform")}</p>
                            {view.sources.some((row) => row.job === "screen" && row.on) && <p>{t("whatIsoformSees.leaves.wechat")}</p>}
                            {view.phonePaired && <p>{t("whatIsoformSees.leaves.phone")}</p>}
                        </div>
                        <h3 className="mt-5 text-xs font-semibold uppercase text-muted">{t("whatIsoformSees.tap.title")}</h3>
                        <p className="mt-2 rounded-xl border border-border px-4 py-3 text-sm text-ink">{t("whatIsoformSees.tap.line")}</p>
                        <button type="button" className="mt-3 text-xs text-accent hover:underline" onClick={openSources}>{t("whatIsoformSees.foot")}</button>
                    </>}
                </div>
            </DialogContent>
        </Dialog>
    );
}
