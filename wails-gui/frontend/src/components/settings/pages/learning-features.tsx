import { useCallback, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { useSettingsFailure } from "@/components/settings/settings-failure";
import { Switch } from "@/components/ui/switch";
import { LoadUIState, SaveUIState } from "../../../../wailsjs/go/main/App";
import {
    ACTIVITY_LEARNING_FEATURE_KEYS,
    ACTIVITY_LEARNING_FEATURE_ROWS,
} from "@/components/activity-learning-settings";
import { SettingsCard, SettingsRow } from "@/components/settings/settings-primitives";
import { SettingReadError } from "./setting-read-error";

// ── What Isoform learns ──
//
// The activity-learning switches, moved here from the Observation page. Each
// switch is a ui_state key the Go side reads (activity_triage_prompt.go);
// turning one off trims the matching model work and skips what it would have
// created. Observing never starts work on its own.
//
// "Prediction confidence" is less common, so it sits on Developer tools; the
// "Learn by comparison" switch went (answers v1.5.0 S16).

const SHADOW_CONFIDENCE_ADJUSTMENT_KEY = "shadow_confidence_adjustment_enabled";

const EVERYDAY_ROWS = ACTIVITY_LEARNING_FEATURE_ROWS.filter((row) => row.key !== "shadow");

/**
 * One persisted learning switch. A missing or empty value keeps the default
 * of on, the same default the Go side uses (loadTriageFeatureFlags), so
 * installs that never touched the switch keep learning. A failed save puts the
 * switch back and says so.
 */
function useLearningSwitch(stateKey: string) {
    const { t } = useTranslation("settings");
    const { failure: changeFailure, fail: failChange } = useSettingsFailure();
    const [enabled, setEnabled] = useState<boolean | null>(null);
    const [readFailed, setReadFailed] = useState(false);
    const read = useCallback(() => {
        setEnabled(null);
        setReadFailed(false);
        LoadUIState(stateKey).then((value) => {
            if (typeof value !== "string") throw new Error("Invalid setting");
            setEnabled(value !== "false");
        }).catch(() => setReadFailed(true));
    }, [stateKey]);
    useEffect(() => { read(); }, [read]);
    const update = useCallback((next: boolean) => {
        setEnabled(next);
        SaveUIState(stateKey, next ? "true" : "false").catch(() => {
            read();
            failChange(t("activityTriage.saveFailed"), () => void update(next));
        });
    }, [stateKey, t, read]);
    return { enabled, update, readFailed, read, changeFailure };
}

function LearningControl({ setting, title, disabled = false }: {
    setting: ReturnType<typeof useLearningSwitch>; title: string; disabled?: boolean;
}) {
    if (setting.readFailed) return <SettingReadError retry={setting.read} />;
    if (setting.enabled === null) return null;
    return <Switch aria-label={title} checked={setting.enabled} disabled={disabled} onCheckedChange={setting.update} />;
}

function LearningSwitchRow({
    stateKey,
    title,
    description,
}: {
    stateKey: string;
    title: string;
    description: string;
}) {
    const setting = useLearningSwitch(stateKey);
    return (
        <SettingsRow
            title={title}
            description={description}
            failure={setting.changeFailure}
            control={<LearningControl setting={setting} title={title} />}
        />
    );
}

/** Everyday card: what Isoform may learn from what you do on this computer. */
export function LearningFeaturesSettings() {
    const { t } = useTranslation("settings");
    const translateSetting = (key: string) => String(t(key as never));

    return (
        <SettingsCard
            title={t("learningMemory.learns.title")}
            description={t("activityTriage.cardDescription")}
        >
            {EVERYDAY_ROWS.map((row) => (
                <LearningSwitchRow
                    key={row.key}
                    stateKey={ACTIVITY_LEARNING_FEATURE_KEYS[row.key]}
                    title={translateSetting(row.titleKey)}
                    description={translateSetting(row.descriptionKey)}
                />
            ))}
        </SettingsCard>
    );
}

/**
 * Developer tools: "Prediction confidence", which initiative_confidence.go
 * still reads for automation proposals. "Learn by comparison"
 * (triage_enable_shadow) went from here (computer-conformance answers v1.5.0
 * S16: a row stays only while code reads it): the shadow observer was deleted
 * on November 14, and the one triage that still loads the key forces it off
 * (activitySummaryTriageFlags), so the switch changed nothing. Confidence no
 * longer waits on it.
 */
export function PredictionConfidenceSettings() {
    const { t } = useTranslation("settings");
    const confidence = useLearningSwitch(SHADOW_CONFIDENCE_ADJUSTMENT_KEY);
    const confidenceTitle = t("activityTriage.confidenceAdjustmentTitle");

    return (
        <SettingsCard>
            <SettingsRow
                title={confidenceTitle}
                description={t("activityTriage.confidenceAdjustmentDescription")}
                failure={confidence.changeFailure}
                control={<LearningControl setting={confidence} title={confidenceTitle} />}
            />
        </SettingsCard>
    );
}
