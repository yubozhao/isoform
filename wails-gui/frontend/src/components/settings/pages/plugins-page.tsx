import { useCallback, useEffect, useState } from "react";
import type { ParseKeys } from "i18next";
import { useTranslation } from "react-i18next";
import { LockKeyhole } from "lucide-react";
import { SettingsFailureLine, SettingsPlaceholderRows, useSettingsFailure } from "@/components/settings/settings-failure";
import { Switch } from "@/components/ui/switch";
import { SettingsCard, SettingsRow } from "@/components/settings/settings-primitives";
import { SelfUpdatesSettings } from "@/components/self-updates-settings";
import { ListPlugins, SetBuiltinOn } from "../../../../wailsjs/go/main/App";
import type { PluginInfo } from "@/lib/wails-shim-app";

// Go wraps these existing setter errors with the plugin ID. Translate the
// known refusal, including where to resolve it, without exposing raw errors.
const TOGGLE_ERRORS = [
    { match: /enter a phone number before/i, key: "plugins.errors.phoneNumber" },
    { match: /accept the text message terms/i, key: "plugins.errors.textConsent" },
    { match: /enter the address of a relay before/i, key: "plugins.errors.relay" },
    { match: /screen observation is paused or switched off/i, key: "plugins.errors.observation" },
    { match: /plugin settings are unreadable/i, key: "plugins.errors.unreadable" },
] as const;

/** Switches that no longer do anything and so are not shown; their saved
 *  value is kept. `auto-speak` read Chat's answers aloud, which went
 *  (answers v1.0.0 T2, settings batch D1). */
const HIDDEN_PLUGIN_IDS: readonly string[] = ["auto-speak"];

function shown(list: PluginInfo[] | null | undefined): PluginInfo[] {
    return (list ?? []).filter((plugin) => !HIDDEN_PLUGIN_IDS.includes(plugin.id));
}

export function PluginsPage() {
    const { t } = useTranslation("settings");
    const { failure: changeFailure, fail: failChange } = useSettingsFailure();
    const [plugins, setPlugins] = useState<PluginInfo[] | null>(null);
    const [loadFailed, setLoadFailed] = useState(false);
    const [saving, setSaving] = useState(false);

    const reload = useCallback(async () => {
        setLoadFailed(false);
        try {
            setPlugins(shown(await ListPlugins()));
        } catch {
            setLoadFailed(true);
        }
    }, []);

    useEffect(() => {
        void reload();
    }, [reload]);

    const toggle = async (plugin: PluginInfo, enabled: boolean) => {
        if (plugin.essential || plugin.readErrorKey || saving) return;
        setSaving(true);
        try {
            // The returned list includes any dependent switches changed by Go.
            setPlugins(shown(await SetBuiltinOn(plugin.id, enabled)));
        } catch (error) {
            const detail = error instanceof Error ? error.message : String(error);
            const refusal = TOGGLE_ERRORS.find(({ match }) => match.test(detail));
            failChange(
                refusal ? t(refusal.key) : t("plugins.toggleFailed", { name: t(plugin.nameKey as ParseKeys<"settings">) }),
                () => void toggle(plugin, enabled),
            );
            // Existing setters can persist earlier dependency changes before
            // a later setter fails. Read the actual saved state again.
            await reload();
        } finally {
            setSaving(false);
        }
    };

    return (
        <>
            <SettingsCard title={t("plugins.builtinSection")} description={t("plugins.builtinDescription")}>
                {loadFailed ? (
                    <SettingsFailureLine failure={{ text: t("plugins.loadFailed"), retry: () => void reload() }} />
                ) : plugins === null ? (
                    <SettingsPlaceholderRows rows={3} />
                ) : (
                    <>
                        {plugins.map((plugin) => {
                            const name = t(plugin.nameKey as ParseKeys<"settings">);
                            const lockId = `plugin-${plugin.id}-lock`;
                            const readErrorId = `plugin-${plugin.id}-read-error`;
                            return (
                                <SettingsRow key={plugin.id} title={name} description={
                                    <span className="flex flex-col gap-1.5">
                                        <span>{t(plugin.descriptionKey as ParseKeys<"settings">)}</span>
                                        <span className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
                                            <span className="rounded-full bg-ink/5 px-2 py-0.5">
                                                {t("plugins.builtinSection")}
                                            </span>
                                            <span className="rounded-full bg-ink/5 px-2 py-0.5">
                                                {t(`plugins.kinds.${plugin.kind}` as ParseKeys<"settings">)}
                                            </span>
                                            <span>{t("plugins.version", { version: plugin.version })}</span>
                                            <span>{t("plugins.source", { source: t(`plugins.sources.${plugin.source}` as ParseKeys<"settings">) })}</span>
                                        </span>
                                        {plugin.essential && (
                                            <span id={lockId} className="flex items-center gap-1.5 text-xs text-muted-foreground">
                                                <LockKeyhole className="h-3.5 w-3.5 shrink-0" aria-hidden="true" />
                                                {t(plugin.lockedReasonKey as ParseKeys<"settings">)}
                                            </span>
                                        )}
                                        {plugin.readErrorKey && (
                                            <span id={readErrorId}>
                                                {t(plugin.readErrorKey as ParseKeys<"settings">)}
                                            </span>
                                        )}
                                    </span>
                                } control={
                                    <>
                                        {plugin.readErrorKey && (
                                            <span className="text-xs text-muted-foreground">{t("plugins.stateUnavailable")}</span>
                                        )}
                                        <Switch
                                            checked={!plugin.readErrorKey && plugin.enabled}
                                            disabled={plugin.essential || Boolean(plugin.readErrorKey) || saving}
                                            aria-label={name}
                                            aria-describedby={plugin.readErrorKey ? readErrorId : plugin.essential ? lockId : undefined}
                                            onCheckedChange={(enabled) => void toggle(plugin, enabled)}
                                        />
                                    </>
                                } />
                            );
                        })}
                    </>
                )}
                <SettingsFailureLine failure={changeFailure} />
            </SettingsCard>
            {/* Self Dream's proposals, beside the memory-dream switches that
                produce them (settings batch D3, from Learning and memory;
                design to place). Shown only while proposals wait. */}
            <SelfUpdatesSettings />
        </>
    );
}
