import { useCallback, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { Switch } from "@/components/ui/switch";
import { SettingsCard, SettingsRow } from "@/components/settings/settings-primitives";
import { SettingsFailureLine, useSettingsFailure } from "@/components/settings/settings-failure";
import { briefClockTime, briefDateAtTime } from "@/lib/brief-time";
import {
    GetWatchPhoneQuiet,
    GetWatchSettings,
    SetWatchHeartRate,
    SetWatchNotes,
    SetWatchPlace,
    type WatchPhoneQuietView,
    type WatchSettingsView,
} from "@/lib/wails-shim-app";

// ── Settings › Your watch (page id "your-watch") ──
//
// computer-conformance answers v1.0.0 T1: the watch's state line, then three
// switches, each the same switch as the phone's Watch tab (one state, both
// devices). Listening isn't repeated here: it's What Isoform sees › Your
// watch. The phone-quiet line (W3, W5) and the listening-off-pending line
// (W7) come from GetWatchPhoneQuiet. Nothing here stops or deletes audio.

type SwitchKey = "notes" | "heartRate" | "place";

/** Place shows now that the phone and watch follow the computer's choice
 *  and the phone lists Apple naming the area (PR 1466). Kept as a switch so a
 *  later hold is one line. */
export const SHOW_WATCH_PLACE = true;

const SETTERS: Record<SwitchKey, (on: boolean) => Promise<WatchSettingsView>> = {
    notes: SetWatchNotes,
    heartRate: SetWatchHeartRate,
    place: SetWatchPlace,
};

/** "3:04 PM" today, "Nov 21 at 3:04 PM" before. */
function whenWords(iso: string | undefined, language: string, t: (key: string, options?: Record<string, unknown>) => string) {
    if (!iso) return null;
    const when = new Date(iso);
    if (Number.isNaN(when.getTime())) return null;
    const today = when.toDateString() === new Date().toDateString();
    return today ? briefClockTime(iso, language, t) : briefDateAtTime(iso, language, t);
}

export function YourWatchPage({ showPlace = SHOW_WATCH_PLACE }: { showPlace?: boolean } = {}) {
    const { t, i18n } = useTranslation("settings");
    const { t: clock } = useTranslation("today");
    const { failure: changeFailure, fail: failChange, clear } = useSettingsFailure();
    const [view, setView] = useState<WatchSettingsView | null>(null);
    const [quiet, setQuiet] = useState<WatchPhoneQuietView | null>(null);
    const [readFailed, setReadFailed] = useState(false);
    const [saving, setSaving] = useState<SwitchKey | null>(null);

    const read = useCallback(() => {
        setReadFailed(false);
        Promise.all([GetWatchSettings(), GetWatchPhoneQuiet()])
            .then(([settings, phone]) => {
                setView(settings);
                setQuiet(phone);
            })
            .catch(() => {
                setView(null);
                setQuiet(null);
                setReadFailed(true);
            });
    }, []);
    useEffect(() => {
        read();
        window.addEventListener("focus", read);
        return () => window.removeEventListener("focus", read);
    }, [read]);

    const change = async (key: SwitchKey, on: boolean) => {
        clear();
        setSaving(key);
        try {
            setView(await SETTERS[key](on));
        } catch {
            failChange(t("yourWatch.changeFailed"), () => void change(key, on));
        } finally {
            setSaving(null);
        }
    };

    if (readFailed) {
        return (
            <SettingsCard>
                <SettingsFailureLine failure={{ text: t("yourWatch.readFailed"), retry: read }} />
            </SettingsCard>
        );
    }
    if (!view || !quiet) return null;
    // Unknown pairing (a paired phone hasn't reported yet) is not "no watch".
    if (view.paired === null) {
        return (
            <SettingsCard>
                <SettingsFailureLine failure={{ text: t("yourWatch.pairingUnknown"), retry: read }} />
            </SettingsCard>
        );
    }
    if (!view.paired) {
        return (
            <SettingsCard>
                <p className="settings-row-desc" data-testid="your-watch-none">
                    {t("sees.noWatch")}
                </p>
            </SettingsCard>
        );
    }

    const language = i18n.language;
    const lastHeard = whenWords(view.lastHeardAt, language, clock as never);
    const state = view.listening.off
        ? t("sees.watchOff")
        : lastHeard
          ? t("yourWatch.stateHeard", { time: lastHeard })
          : t("yourWatch.stateListening");
    const phoneTime = whenWords(quiet.since, language, clock as never);

    const row = (key: SwitchKey, lines: string[] = []) => {
        const title = t(`yourWatch.${key}.title` as never) as string;
        const setting = view[key];
        return (
            <div key={key} data-your-watch-row={key}>
                <SettingsRow
                    title={title}
                    description={t(`yourWatch.${key}.description` as never) as string}
                    control={
                        <Switch
                            aria-label={title}
                            checked={setting.on}
                            disabled={saving !== null}
                            onCheckedChange={(next) => void change(key, next)}
                        />
                    }
                />
                {setting.on &&
                    lines.map((line, index) => (
                        <p
                            key={index}
                            className="settings-row-desc -mt-1 pb-2"
                            data-testid={index === 0 ? `your-watch-interim-${key}` : `your-watch-apple-${key}`}
                        >
                            {line}
                        </p>
                    ))}
            </div>
        );
    };

    return (
        <>
            <SettingsCard>
                <p className="settings-row-title" data-testid="your-watch-state">
                    {state}
                </p>
                {quiet.offPending && view.listening.off && (
                    <p className="settings-row-desc" data-testid="your-watch-off-pending">
                        {phoneTime ? t("watch.offPending", { time: phoneTime }) : t("yourWatch.offPendingNever")}
                    </p>
                )}
                {quiet.quiet && phoneTime && (
                    <p className="settings-row-desc text-warning" data-testid="your-watch-phone-quiet">
                        {t("watch.phoneQuiet", { time: phoneTime })}
                    </p>
                )}
            </SettingsCard>
            <SettingsCard>
                {row("notes")}
                {/* While on (interim words for design): the watch asks the
                    wearer to allow it the first time; Place names the area
                    through Apple (PR 1466). */}
                {row("heartRate", [t("yourWatch.allowOnWatch")])}
                {showPlace && row("place", [t("yourWatch.allowOnWatch"), t("yourWatch.place.apple")])}
                <SettingsFailureLine failure={changeFailure} />
            </SettingsCard>
        </>
    );
}
