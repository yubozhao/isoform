import { useCallback, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { Button } from "@/components/ui/button";
import { Switch } from "@/components/ui/switch";
import {
    Select,
    SelectTrigger,
    SelectValue,
    SelectContent,
    SelectItem,
} from "@/components/ui/select";
import {
    GetStartAtLogin,
    SetStartAtLogin,
} from "../../../../wailsjs/go/main/App";
import {
    EnsureSelfProfile,
    GetModelChoices,
    PlanSetupModelDownload,
} from "@/lib/wails-shim-app";
import { refreshUserProfile, useUserProfile } from "@/hooks/useUserProfile";
import { SettingsFailureLine, useSettingsFailure } from "@/components/settings/settings-failure";
import {
    getLanguagePreference,
    setLanguagePreference,
    type LanguagePreference,
} from "@/i18n";
import { useTheme } from "@/hooks/useTheme";
import type { ThemePreference } from "@/themes/types";
import { SettingsCard, SettingsRow } from "@/components/settings/settings-primitives";
import {
    QUIET_JOBS,
    SPEECH_JOBS,
    chosenModels,
    formatModelBytes,
    wireChoices,
} from "@/components/onboarding/v2/models";
import { SettingReadError } from "./setting-read-error";

// The rows About holds (computer-conformance-answers v1.0.0 T2): Your name,
// Open at login, Language, Appearance, then Version and updates and Help in
// help-about-page.tsx, with the models line (answers v1.2.0 S11). They came
// from the General, Profile and Appearance pages, which went in settings
// batch D1; each keeps its saved value.

// ── Your name ──

/** The same local self identity People and the account menu use. */
export function YourNameSettings() {
    const { t } = useTranslation("settings");
    const { t: ta } = useTranslation("app");
    const { profile, loading } = useUserProfile();
    const [draft, setDraft] = useState<string | null>(null);
    const [saving, setSaving] = useState(false);
    const [failed, setFailed] = useState(false);
    const current = profile?.display_name || profile?.name || "";
    const name = draft ?? current;
    const save = async () => {
        if (!name.trim() || saving) return;
        setSaving(true);
        setFailed(false);
        try {
            await EnsureSelfProfile(name.trim(), []);
            refreshUserProfile();
            setDraft(null);
        } catch {
            setFailed(true);
        } finally {
            setSaving(false);
        }
    };
    const changed = name.trim() !== current;
    return (
        <SettingsCard>
            <form
                className="flex flex-col gap-3"
                data-testid="about-your-name"
                onSubmit={(event) => {
                    event.preventDefault();
                    void save();
                }}
            >
                <label htmlFor="about-name" className="settings-row-title">
                    {t("about.yourName")}
                </label>
                <input
                    id="about-name"
                    className="h-8 rounded-lg border border-border bg-panel px-3 text-[13px] text-ink focus-visible:outline-border-focus"
                    value={name}
                    disabled={loading || saving}
                    maxLength={80}
                    onChange={(event) => setDraft(event.target.value)}
                    placeholder={ta("userMenu.namePlaceholder")}
                />
                {failed && (
                    <p role="alert" className="text-[12px] text-copper">
                        {ta("userMenu.nameSaveError")}
                    </p>
                )}
                {changed && (
                    <div className="flex justify-end gap-2">
                        <Button
                            type="button"
                            variant="quiet"
                            size="sm"
                            disabled={saving}
                            onClick={() => {
                                setDraft(null);
                                setFailed(false);
                            }}
                        >
                            {ta("userMenu.cancelName")}
                        </Button>
                        <Button type="submit" size="sm" disabled={loading || saving || !name.trim()}>
                            {ta("userMenu.saveName")}
                        </Button>
                    </div>
                )}
            </form>
        </SettingsCard>
    );
}

// ── Open at login ──

/** Only macOS and Windows register a login item; elsewhere the row hides
 *  and the rest of the page still renders. On by default
 *  (ensureStartAtLoginEnabled in menubar_darwin.go). */
export function StartAtLoginSettings() {
    const { t } = useTranslation("settings");
    const { failure: changeFailure, fail: failChange } = useSettingsFailure();
    const isMac = navigator.platform.toLowerCase().includes("mac");
    const isWindows = navigator.platform.toLowerCase().includes("win");
    const isSupported = isMac || isWindows;

    const [startAtLogin, setStartAtLoginState] = useState<boolean | null>(null);
    const [readFailed, setReadFailed] = useState(false);
    const [saving, setSaving] = useState(false);

    const read = useCallback(() => {
        setStartAtLoginState(null);
        setReadFailed(false);
        GetStartAtLogin()
            .then((value) => {
                if (typeof value !== "boolean") throw new Error("Invalid setting");
                setStartAtLoginState(value);
            })
            .catch(() => setReadFailed(true));
    }, []);
    useEffect(() => {
        if (isSupported) read();
    }, [isSupported, read]);

    if (!isSupported) return null;

    // One save at a time: a quick second click used to send a second save
    // that could land before the first.
    const handleLoginToggle = () => {
        if (startAtLogin === null || saving) return;
        const next = !startAtLogin;
        setStartAtLoginState(next);
        setSaving(true);
        SetStartAtLogin(next)
            .catch(() => {
                read();
                failChange(t("general.startAtLogin.saveFailed"), () => void handleLoginToggle());
            })
            .finally(() => setSaving(false));
    };

    // The row keeps its final shape before the read resolves; only the
    // control is a skeleton until then.
    return (
        <SettingsCard>
            <SettingsRow
                title={t("about.openAtLogin")}
                description={t("general.startAtLogin.description")}
                control={
                    readFailed ? (
                        <SettingReadError retry={read} />
                    ) : startAtLogin === null ? (
                        <div aria-hidden className="h-5 w-9 rounded-full bg-active-row-bg animate-pulse" />
                    ) : (
                        <Switch
                            checked={startAtLogin}
                            disabled={saving}
                            onCheckedChange={handleLoginToggle}
                            aria-label={t("about.openAtLogin")}
                        />
                    )
                }
            />
            <SettingsFailureLine failure={changeFailure} />
        </SettingsCard>
    );
}

// ── Language ──

const LANGUAGE_CHOICES: { value: LanguagePreference; labelKey: string }[] = [
    { value: "system", labelKey: "general.language.system" },
    { value: "en", labelKey: "general.language.english" },
    { value: "zh-CN", labelKey: "general.language.chinese" },
];

export function LanguageSettings() {
    const { t } = useTranslation("settings");
    const [preference, setPreference] = useState<LanguagePreference>(getLanguagePreference);

    const handleLanguageChange = (value: string) => {
        const next = value as LanguagePreference;
        setPreference(next);
        void setLanguagePreference(next);
    };

    return (
        <SettingsCard>
            <SettingsRow
                title={t("general.language.title")}
                description={t("general.language.description")}
                control={
                    <Select value={preference} onValueChange={handleLanguageChange}>
                        <SelectTrigger className="w-[180px] text-xs" aria-label={t("general.language.title")}>
                            <SelectValue />
                        </SelectTrigger>
                        <SelectContent>
                            {LANGUAGE_CHOICES.map((choice) => (
                                <SelectItem key={choice.value} value={choice.value} className="text-xs">
                                    {t(choice.labelKey as never)}
                                </SelectItem>
                            ))}
                        </SelectContent>
                    </Select>
                }
            />
        </SettingsCard>
    );
}

// ── Appearance ──

const APPEARANCE_CHOICES: ThemePreference[] = ["paper", "onedark", "system"];

const APPEARANCE_LABEL_KEYS: Record<ThemePreference, string> = {
    paper: "general.appearance.light",
    onedark: "general.appearance.dark",
    system: "general.appearance.system",
};

/** Light, dark or the computer's. Reading width went (answers v1.0.0 T2:
 *  the window's widths decide it). */
export function AppearanceSettings() {
    const { t } = useTranslation("settings");
    const { preference, setPreference } = useTheme();

    return (
        <SettingsCard>
            <SettingsRow
                title={t("general.appearance.title")}
                description={t("general.appearance.description")}
                control={
                    <div className="settings-segment" role="radiogroup" aria-label={t("general.appearance.title")}>
                        {APPEARANCE_CHOICES.map((choice) => (
                            <button
                                key={choice}
                                type="button"
                                role="radio"
                                aria-checked={preference === choice}
                                data-active={preference === choice}
                                onClick={() => setPreference(choice)}
                            >
                                {t(APPEARANCE_LABEL_KEYS[choice] as never)}
                            </button>
                        ))}
                    </div>
                }
            />
        </SettingsCard>
    );
}

// ── Models on this computer ──

type ModelsState =
    | { kind: "loading" }
    | { kind: "failed" }
    | { kind: "ready"; size: number; short: { size: number; free: number } | null };

/**
 * The privacy model (takes out names and numbers before text leaves) and the
 * memory model (finding things), which download in idle time whatever the
 * microphone says (answers v1.2.0 S11). Their size is the catalog's for the
 * chosen models; short of disk, the line setup shows (ModelsLine) says what
 * they wait for. Sizes and free space come from setup's own bindings
 * (GetModelChoices, PlanSetupModelDownload), not a second one.
 */
export function AboutModels() {
    const { t, i18n } = useTranslation("settings");
    const [state, setState] = useState<ModelsState>({ kind: "loading" });

    const read = useCallback(() => {
        let live = true;
        setState({ kind: "loading" });
        void (async () => {
            try {
                const choices = await GetModelChoices();
                const chosen = chosenModels(choices, i18n.language);
                const size = QUIET_JOBS.reduce((sum, job) => sum + (chosen[job]?.sizeBytes ?? 0), 0);
                const plan = await PlanSetupModelDownload(wireChoices(choices, i18n.language), SPEECH_JOBS);
                const short =
                    plan.items.length > 0 && !plan.enoughSpace
                        ? { size: plan.totalBytes, free: plan.freeBytes }
                        : null;
                if (live) setState({ kind: "ready", size, short });
            } catch {
                if (live) setState({ kind: "failed" });
            }
        })();
        return () => {
            live = false;
        };
    }, [i18n.language]);

    useEffect(() => read(), [read]);

    return (
        <SettingsCard>
            <div className="flex flex-col gap-1" data-testid="about-models">
                {state.kind === "failed" ? (
                    <SettingReadError retry={() => void read()} />
                ) : state.kind === "loading" ? (
                    <div aria-hidden className="h-4 w-64 rounded bg-active-row-bg animate-pulse" />
                ) : (
                    <>
                        <span className="settings-row-title">
                            {t("about.models", { size: formatModelBytes(state.size) })}
                        </span>
                        {state.short && (
                            <span className="settings-row-desc text-warning" data-testid="about-models-short">
                                {t("about.modelsShort", {
                                    size: formatModelBytes(state.short.size),
                                    free: formatModelBytes(state.short.free),
                                })}
                            </span>
                        )}
                    </>
                )}
            </div>
        </SettingsCard>
    );
}
