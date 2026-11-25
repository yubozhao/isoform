import { useCallback, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { Download, Check } from "lucide-react";
import { Switch } from "@/components/ui/switch";
import {
    GetAutoSpeakNotificationsEnabled,
    SetAutoSpeakNotificationsEnabled,
    GetTtsModelStatus,
} from "../../../../wailsjs/go/main/App";
import { EventsOn } from "../../../../wailsjs/runtime/runtime";
import { useSettingsFailure } from "@/components/settings/settings-failure";
import { SettingsCard, SettingsRow } from "@/components/settings/settings-primitives";
import { SettingReadError } from "./setting-read-error";

// The voice for Hear it and what Isoform reads aloud on its own, on Briefs and
// notifications (computer-conformance-answers v1.0.0 T2). It came from
// General's Advanced in settings batch D1; reading Chat's answers aloud went.
// Announce finished automations keeps its behaviour and saved value but is not
// shown: automations are behind their switch (answers v1.2.0 AG12). Read
// notifications aloud sits here beside the voices (answers v1.5.0 S22): off
// until turned on, silenced by quiet hours (notification_speech.go).

// ── Read aloud ──

// One independent read-aloud category toggle. Each category persists through
// its own Get/Set binding; toggles are isolated so flipping one never touches
// the others. Optimistic update with rollback + an in-place failure line.
function AutoSpeakCategoryRow({
    title,
    description,
    get,
    set,
}: {
    title: string;
    description: string;
    get: () => Promise<boolean>;
    set: (v: boolean) => Promise<void>;
}) {
    const { t } = useTranslation("settings");
    const { failure: changeFailure, fail: failChange } = useSettingsFailure();
    const [enabled, setEnabledState] = useState(false);
    const [loading, setLoading] = useState(true);
    const [readFailed, setReadFailed] = useState(false);

    // A failed read has no switch state to show: an "off" switch would be a
    // guess, so the row offers a retry instead (same as Open at login).
    const read = useCallback(() => {
        setLoading(true);
        setReadFailed(false);
        get()
            .then((v) => {
                setEnabledState(v);
                setLoading(false);
            })
            .catch(() => {
                setReadFailed(true);
                setLoading(false);
            });
    }, [get]);

    useEffect(() => {
        read();
    }, [read]);

    const handleToggle = () => {
        const next = !enabled;
        setLoading(true);
        setEnabledState(next);
        set(next)
            .catch(() => {
                setEnabledState(!next);
                failChange(t("autoSpeak.toggleFailed"), () => void handleToggle());
            })
            .finally(() => {
                setLoading(false);
            });
    };

    return (
        <SettingsRow
            title={title}
            failure={changeFailure}
            description={description}
            control={
                readFailed ? <SettingReadError retry={read} /> : (
                    <Switch
                        checked={enabled}
                        disabled={loading}
                        onCheckedChange={handleToggle}
                        aria-label={title}
                    />
                )
            }
        />
    );
}

// Per-voice download status pill. Installed reuses the positive-status badge
// style used elsewhere in settings; not-installed is a muted hint that turning
// a category on (or reading that language) will download it.
function VoiceStatusPill({ installed }: { installed: boolean }) {
    const { t } = useTranslation("settings");
    if (installed) {
        return (
            <span className="inline-flex items-center gap-1 rounded-full bg-success-bg px-2 py-0.5 text-xs font-medium text-success">
                <Check className="h-3 w-3" />
                {t("autoSpeak.voiceInstalled")}
            </span>
        );
    }
    return (
        <span className="inline-flex items-center gap-1 text-xs text-muted-foreground">
            <Download className="h-3 w-3" />
            {t("autoSpeak.voiceNotInstalled")}
        </span>
    );
}

// Shows which on-device voices are already downloaded. Refreshes when a
// model finishes downloading (tts:model-ready) so the pill flips to Installed
// without a settings reopen. The voice model names are secondary text.
function AutoSpeakVoiceStatus() {
    const { t } = useTranslation("settings");
    const [status, setStatus] = useState<{ zh: boolean; en: boolean } | null>(
        null,
    );

    const refresh = useCallback(() => {
        GetTtsModelStatus()
            .then(setStatus)
            .catch(() => {});
    }, []);

    useEffect(() => {
        refresh();
        const unsub = EventsOn("tts:model-ready", () => refresh());
        return () => unsub();
    }, [refresh]);

    if (!status) return null;

    return (
        <>
            <div className="settings-row-desc pt-2 font-medium">
                {t("autoSpeak.voicesTitle")}
            </div>
            <SettingsRow
                title={t("autoSpeak.voiceChinese")}
                description={t("autoSpeak.voiceChineseDetail")}
                control={<VoiceStatusPill installed={status.zh} />}
            />
            <SettingsRow
                title={t("autoSpeak.voiceEnglish")}
                description={t("autoSpeak.voiceEnglishDetail")}
                control={<VoiceStatusPill installed={status.en} />}
            />
        </>
    );
}

export function AutoSpeakSettings() {
    const { t } = useTranslation("settings");

    return (
        <SettingsCard
            title={t("autoSpeak.cardTitle")}
            description={t("autoSpeak.cardDescription")}
        >
            <AutoSpeakCategoryRow
                title={t("autoSpeak.notificationsTitle")}
                description={t("autoSpeak.notificationsDescription")}
                get={GetAutoSpeakNotificationsEnabled}
                set={SetAutoSpeakNotificationsEnabled}
            />
            <AutoSpeakVoiceStatus />
        </SettingsCard>
    );
}
