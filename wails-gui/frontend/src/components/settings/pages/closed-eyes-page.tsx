import { useCallback, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { SettingsFailureLine, useSettingsFailure } from "@/components/settings/settings-failure";
import { SettingsRow } from "@/components/settings/settings-primitives";
import { Button } from "@/components/ui/button";
import { Switch } from "@/components/ui/switch";
import {
    GetClosedEyesResumeAt,
    IsClosedEyesModeEnabled,
    PauseObservationFor,
    SetClosedEyesModeEnabled,
} from "@/lib/wails-shim-app";
import { EventsOn } from "../../../../wailsjs/runtime/runtime";
import { PAUSE_OBSERVING_COMBO, comboToDisplayString } from "@/hooks/useKeybindings";
import { SettingsControlCard, SettingsReason } from "./capture-shared";

const CLOSED_EYES_CHANGED_EVENT = "closed-eyes:changed";

/** How long Closed Eyes lasts, asked when it is turned on (phone-settings
 *  v1.0.0 rule 3, shared by the computer). null means until you end it. */
export const CLOSED_EYES_DURATIONS = [15, 60, null] as const;
type ClosedEyesDuration = (typeof CLOSED_EYES_DURATIONS)[number];

interface ClosedEyesState {
    closed: boolean | null;
    resumeAt: string;
    loadFailed: boolean;
}

/** Settings › Closed Eyes: one switch for every device. Turning it on asks for
 *  how long; on, it reads "Closed Eyes until {time}" (computer-settings
 *  v1.0.0 section 1, phone-settings v1.0.0 rule 3). */
export function ClosedEyesPage() {
    const { t, i18n } = useTranslation("settings");
    const { failure, fail } = useSettingsFailure();
    const [state, setState] = useState<ClosedEyesState>({ closed: null, resumeAt: "", loadFailed: false });
    const [asking, setAsking] = useState(false);
    const [saving, setSaving] = useState(false);

    const reload = useCallback(() => {
        Promise.all([IsClosedEyesModeEnabled(), GetClosedEyesResumeAt()])
            .then(([closed, resumeAt]) => setState({ closed: Boolean(closed), resumeAt: resumeAt ?? "", loadFailed: false }))
            .catch(() => setState((current) => ({ ...current, loadFailed: true })));
    }, []);

    useEffect(() => {
        reload();
        const off = EventsOn(CLOSED_EYES_CHANGED_EVENT, () => reload());
        window.addEventListener("focus", reload);
        return () => {
            off();
            window.removeEventListener("focus", reload);
        };
    }, [reload]);

    const run = async (action: () => Promise<void>) => {
        setSaving(true);
        try {
            await action();
            setAsking(false);
        } catch {
            fail(t("closedEyes.toggleFailed"), () => void run(action));
        } finally {
            setSaving(false);
            reload();
        }
    };

    const close = (minutes: ClosedEyesDuration) =>
        run(() => (minutes === null ? SetClosedEyesModeEnabled(true) : PauseObservationFor(minutes)));
    const open = () => run(() => SetClosedEyesModeEnabled(false));

    const until = state.resumeAt
        ? new Date(state.resumeAt).toLocaleTimeString(i18n.language, { hour: "numeric", minute: "2-digit" })
        : "";
    const title = t("closedEyes.title");

    return (
        <SettingsControlCard
            title={title}
            description={t("closedEyes.description")}
            control={
                <Switch
                    aria-label={title}
                    checked={state.closed === true || asking}
                    disabled={state.closed === null || saving}
                    onCheckedChange={(next) => {
                        if (next) setAsking(true);
                        else if (asking && state.closed !== true) setAsking(false);
                        else void open();
                    }}
                />
            }
        >
            {state.loadFailed && <SettingsFailureLine failure={{ text: t("closedEyes.loadFailed"), retry: reload }} />}
            <SettingsFailureLine failure={failure} />
            {state.closed === true && (
                <SettingsReason>{until ? t("closedEyes.until", { time: until }) : t("closedEyes.untilEnded")}</SettingsReason>
            )}
            {asking && state.closed !== true && (
                <div role="group" aria-label={t("closedEyes.howLong")} data-closed-eyes-durations className="flex flex-wrap items-center gap-2 pt-1">
                    <span className="text-[13px] text-muted mr-1">{t("closedEyes.howLong")}</span>
                    {CLOSED_EYES_DURATIONS.map((minutes) => (
                        <Button key={String(minutes)} size="sm" variant="outline" disabled={saving} onClick={() => void close(minutes)}>
                            {minutes === null ? t("closedEyes.untilIEnd") : t("closedEyes.forMinutes", { count: minutes })}
                        </Button>
                    ))}
                </div>
            )}
            {PAUSE_OBSERVING_COMBO && (
                <SettingsRow
                    title={t("closedEyes.shortcutTitle")}
                    description={t("closedEyes.shortcutDescription")}
                    control={<span className="inline-flex items-center px-2.5 py-1 rounded-md text-xs bg-ink/5 text-muted">{comboToDisplayString(PAUSE_OBSERVING_COMBO)}</span>}
                />
            )}
        </SettingsControlCard>
    );
}
