// Settings › Agent of You, every state (handoff v2.1.3 §5). Before Bo approves
// it is today's reserved-name page with the join card on top: Agent of You is
// opening (o01) or You asked to join (o02). Once approved it is the owner's
// page: the steps (o03), On (o10) or Paused (o11), and its screens.
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { Button } from "@/components/ui/button";
import { SettingsFailureLine, useSettingsFailure } from "@/components/settings/settings-failure";
import { agentOwnerAction, type AgentOwnerStatus } from "@/lib/agent-owner";
import type { YourAgentReservation } from "@/lib/wails-shim-app";
import { YourAgentPage } from "../your-agent-page";
import { isOwnerState, setAoyHeader, setAoyNavTag, tagForState, useOwnerSnapshot, type AoyCrumb } from "./aoy-shared";
import { AOY_LINE, AOY_LINK } from "./aoy-ui";
import { OwnerHome } from "./owner-home";
import { OptionsView, RateView } from "./owner-settings-views";
import { BlockedView, JobView, JobsView } from "./owner-jobs";
import { BalanceView } from "./owner-balance";
import { WorkProfileView } from "./work-profile-view";

export type AoyView =
    | { kind: "home" }
    | { kind: "profile" }
    | { kind: "rate" }
    | { kind: "opts" }
    | { kind: "jobs" }
    | { kind: "job"; id: string }
    | { kind: "balance" }
    | { kind: "blocked" };

/** Ask to join (o01) or the request waiting for Bo (o02), above the reserved-name card. */
function JoinCard({ state, reservation, onChanged }: { state: string; reservation: YourAgentReservation; onChanged: () => void }) {
    const { t } = useTranslation("settings");
    const failure = useSettingsFailure();
    const [busy, setBusy] = useState(false);
    const run = async (action: "join" | "cancel_join") => {
        failure.clear();
        setBusy(true);
        try {
            await agentOwnerAction(action);
            onChanged();
        } catch {
            failure.fail(t("overview.changeFailed"), () => void run(action));
        } finally {
            setBusy(false);
        }
    };
    if (state === "asked") {
        return (
            <div className="settings-card settings-card--padded flex flex-col gap-2" data-testid="aoy-asked">
                <h2 className="settings-card-title">{t("aoy.askedT")}</h2>
                <p className="settings-card-desc">{t("aoy.askedL", { email: reservation.email })}</p>
                <button type="button" className={`w-fit text-sm ${AOY_LINK}`} disabled={busy} onClick={() => void run("cancel_join")}>{t("aoy.cancelAsk")}</button>
                <SettingsFailureLine failure={failure.failure} />
            </div>
        );
    }
    return (
        <div className="settings-card settings-card--padded flex flex-col gap-3 border-accent/40" data-testid="aoy-opening">
            <h2 className="settings-card-title">{t("aoy.openT")}</h2>
            <p className="settings-card-desc">{t("aoy.openL")}</p>
            <Button className="w-fit" disabled={busy} onClick={() => void run("join")}>{t("aoy.askJoin")}</Button>
            <p className={AOY_LINE}>{t("aoy.nothingLeaves")}</p>
            <SettingsFailureLine failure={failure.failure} />
        </div>
    );
}

function OwnerArea({ status }: { status: AgentOwnerStatus }) {
    const { t } = useTranslation("settings");
    const [view, setView] = useState<AoyView>({ kind: "home" });
    const home = () => setView({ kind: "home" });
    const root: AoyCrumb = { label: t("aoy.aoy"), onClick: home };
    const jobsCrumb: AoyCrumb = { label: t("aoy.jobs"), onClick: () => setView({ kind: "jobs" }) };

    useEffect(() => {
        const titled = (title: string, crumbs: AoyCrumb[] = [root]) => setAoyHeader({ crumbs, title });
        switch (view.kind) {
            case "home":
                setAoyHeader({ crumbs: [], title: t("aoy.aoy"), lead: t("aoy.lead", { name: status.name }) });
                break;
            case "profile":
                titled(t("aoy.wp"));
                break;
            case "rate":
                titled(t("aoy.rse"));
                break;
            case "opts":
                titled(t("aoy.opts"));
                break;
            case "jobs":
                titled(t("aoy.jobs"));
                break;
            case "balance":
                titled(t("aoy.bal"));
                break;
            case "blocked":
                titled(t("aoy.blocked"));
                break;
            case "job":
                // The job screen sets its own title once its snapshot lands.
                titled(t("aoy.jobs"), [root, jobsCrumb]);
                break;
        }
        // eslint-disable-next-line react-hooks/exhaustive-deps
    }, [view, t, status.name]);
    useEffect(() => () => setAoyHeader(null), []);

    switch (view.kind) {
        case "profile":
            return <WorkProfileView />;
        case "rate":
            return <RateView status={status} />;
        case "opts":
            return <OptionsView status={status} />;
        case "jobs":
            return <JobsView openJob={(id) => setView({ kind: "job", id })} />;
        case "job":
            return <JobView key={view.id} id={view.id} crumbs={[root, jobsCrumb]} onBlocked={() => setView({ kind: "blocked" })} />;
        case "balance":
            return <BalanceView />;
        case "blocked":
            return <BlockedView />;
        default:
            return <OwnerHome status={status} open={setView} />;
    }
}

export function AgentOfYouPage() {
    const status = useOwnerSnapshot("status");
    const state = status.data?.state ?? "";
    useEffect(() => {
        if (status.loaded) setAoyNavTag(tagForState(state));
    }, [status.loaded, state]);

    if (status.data && isOwnerState(state)) return <OwnerArea status={status.data} />;
    return (
        <YourAgentPage
            ownerCard={(reservation) => (status.loaded && !status.failed ? <JoinCard state={state} reservation={reservation} onChanged={status.reload} /> : null)}
        />
    );
}
