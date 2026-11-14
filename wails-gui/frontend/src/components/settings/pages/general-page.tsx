import { useReadingWidth } from "@/hooks/use-reading-width";
import { useCallback, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { Download, Check } from "lucide-react";
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
    GetAutoSpeakEnabled,
    SetAutoSpeakEnabled,
    GetAutoSpeakAutomationEnabled,
    SetAutoSpeakAutomationEnabled,
    GetAutoSpeakNotificationsEnabled,
    SetAutoSpeakNotificationsEnabled,
    GetTtsModelStatus,
} from "../../../../wailsjs/go/main/App";
import { EventsOn } from "../../../../wailsjs/runtime/runtime";
import { SettingsFailureLine, useSettingsFailure } from "@/components/settings/settings-failure";
import {
    getLanguagePreference,
    setLanguagePreference,
    type LanguagePreference,
} from "@/i18n";
import { useTheme } from "@/hooks/useTheme";
import { useShowToolSteps } from "@/hooks/useShowToolSteps";
import type { ThemePreference } from "@/themes/types";
import { KeybindingsSettings, QuickChatShortcutCard } from "@/components/keybindings-settings";
import type { useKeybindings } from "@/hooks/useKeybindings";
import { SettingsCard, SettingsRow } from "@/components/settings/settings-primitives";
import { SettingReadError } from "./setting-read-error";
import { SettingsAdvanced } from "@/components/settings/settings-advanced";

// ── Open at login ──

/** Only macOS and Windows register a login item; elsewhere the card hides
 *  and the rest of the page still renders. */
function StartAtLoginSettings() {
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
        GetStartAtLogin().then((value) => {
            if (typeof value !== "boolean") throw new Error("Invalid setting");
            setStartAtLoginState(value);
        }).catch(() => setReadFailed(true));
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

    // Render the full card layout on first paint even before the async
    // `GetStartAtLogin` resolves — the row/card shape matches the final
    // state, so there is no blank area or layout shift when the toggle
    // hydrates. The control itself becomes a subtle skeleton until the
    // real boolean arrives.
    return (
        <SettingsCard>
            <SettingsRow
                title={t("general.startAtLogin.title")}
                description={t("general.startAtLogin.description")}
                control={
                    readFailed ? <SettingReadError retry={read} /> : startAtLogin === null ? (
                        <div
                            aria-hidden
                            className="h-5 w-9 rounded-full bg-active-row-bg animate-pulse"
                        />
                    ) : (
                        <Switch
                            checked={startAtLogin}
                            disabled={saving}
                            onCheckedChange={handleLoginToggle}
                            aria-label={t("general.startAtLogin.title")}
                        />
                    )
                }
            />
            <SettingsFailureLine failure={changeFailure} />
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

export function AppearanceSettings({ readingWidth = false }: { readingWidth?: boolean }) {
    const { t } = useTranslation("settings");
    const { preference, setPreference } = useTheme();
    const [full, setFull] = useReadingWidth();

    return (
        <SettingsCard>
            <SettingsRow
                title={t("general.appearance.title")}
                description={t("general.appearance.description")}
                control={
                    <div
                        className="settings-segment"
                        role="radiogroup"
                        aria-label={t("general.appearance.title")}
                    >
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
            {readingWidth && <SettingsRow title={t("general.readingWidth.title")} control={
                <div className="settings-segment" role="radiogroup" aria-label={t("general.readingWidth.title")}>
                    {[false, true].map((value) => <button key={String(value)} type="button" role="radio" aria-checked={full === value} data-active={full === value} onClick={() => setFull(value)}>
                        {t(value ? "general.readingWidth.full" : "general.readingWidth.centered")}
                    </button>)}
                </div>
            } />}
        </SettingsCard>
    );
}

// ── Language ──

const LANGUAGE_CHOICES: { value: LanguagePreference; labelKey: string }[] = [
    { value: "system", labelKey: "general.language.system" },
    { value: "en", labelKey: "general.language.english" },
    { value: "zh-CN", labelKey: "general.language.chinese" },
];

function LanguageSettings() {
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
                        <SelectTrigger
                            className="w-[180px] text-xs"
                            aria-label={t("general.language.title")}
                        >
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

// ── Read aloud (Advanced) ──

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

function AutoSpeakSettings() {
    const { t } = useTranslation("settings");

    return (
        <SettingsCard
            title={t("autoSpeak.cardTitle")}
            description={t("autoSpeak.cardDescription")}
        >
            <AutoSpeakCategoryRow
                title={t("autoSpeak.chatTitle")}
                description={t("autoSpeak.chatDescription")}
                get={GetAutoSpeakEnabled}
                set={SetAutoSpeakEnabled}
            />
            <AutoSpeakCategoryRow
                title={t("autoSpeak.automationTitle")}
                description={t("autoSpeak.automationDescription")}
                get={GetAutoSpeakAutomationEnabled}
                set={SetAutoSpeakAutomationEnabled}
            />
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

// ── Tool steps in chat (Advanced) ──

/** Shows each tool call and its result under the chat reply (useShowToolSteps,
 *  read by stream-card and tool-group-item). Moved here from Developer
 *  tools > Chat context. */
function ToolStepsSettings() {
    const { t } = useTranslation("settings");
    const [enabled, setEnabled] = useShowToolSteps();

    return (
        <SettingsCard>
            <SettingsRow
                title={t("general.toolSteps.title")}
                description={t("general.toolSteps.description")}
                control={
                    <Switch
                        checked={enabled}
                        onCheckedChange={setEnabled}
                        aria-label={t("general.toolSteps.title")}
                    />
                }
            />
        </SettingsCard>
    );
}

// ── General page ──

export interface GeneralPageProps {
    keybindings: ReturnType<typeof useKeybindings>;
    vimMode: boolean;
    onVimModeChange: (enabled: boolean) => void;
}

/**
 * General: how the app starts, looks, which language it uses and the Quick
 * Chat shortcut. Keyboard shortcuts, read aloud and tool steps in chat sit
 * under Advanced. The old Appearance, Language, Read aloud and Keyboard
 * shortcuts page ids resolve here.
 */
export function GeneralPage({ keybindings, vimMode, onVimModeChange }: GeneralPageProps) {
    const { t } = useTranslation("settings");
    return (
        <>
            <StartAtLoginSettings />
            <AppearanceSettings />
            <LanguageSettings />
            <QuickChatShortcutCard keybindings={keybindings} />
            <SettingsAdvanced pageId="general" hint={t("general.advancedHint")}>
                <KeybindingsSettings
                    keybindings={keybindings}
                    vimMode={vimMode}
                    onVimModeChange={onVimModeChange}
                />
                <AutoSpeakSettings />
                <ToolStepsSettings />
            </SettingsAdvanced>
        </>
    );
}
