import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Switch } from "@/components/ui/switch";
import { ConfirmDialog } from "@/components/confirm-dialog";
import { SetLearnFromIt } from "@/lib/wails-shim-app";

/**
 * Learn from it for one agent on this computer (computer-agents v1.1.0 rule
 * 12): off until the person turns it on. Turning it off asks once, because
 * Isoform then forgets what came from that agent's history.
 */
export function LearnFromItSwitch({ agentId, name, on, onChanged, label }: {
    agentId: string;
    name: string;
    on: boolean;
    /** Called after the change went through (or failed, with false). */
    onChanged: (ok: boolean) => void;
    /** The switch's accessible name; defaults to "Learn from it". */
    label?: string;
}) {
    const { t } = useTranslation("app");
    const [asking, setAsking] = useState(false);
    const [busy, setBusy] = useState(false);

    const set = async (next: boolean) => {
        setBusy(true);
        try {
            await SetLearnFromIt(agentId, next);
            onChanged(true);
        } catch {
            onChanged(false);
        } finally {
            setBusy(false);
        }
    };

    return (
        <>
            <Switch
                checked={on}
                disabled={busy}
                aria-label={label ?? t("agents.learn.title")}
                onCheckedChange={(next) => (next ? void set(true) : setAsking(true))}
                data-learn-switch={agentId}
            />
            <ConfirmDialog
                open={asking}
                onOpenChange={setAsking}
                title={t("agents.learn.offTitle", { agent: name })}
                description={t("agents.learn.offBody", { agent: name })}
                confirmLabel={t("agents.learn.offConfirm")}
                onConfirm={() => void set(false)}
            />
        </>
    );
}
