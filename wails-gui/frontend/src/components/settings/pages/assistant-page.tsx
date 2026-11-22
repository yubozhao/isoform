import { useEffect, useMemo, useState } from "react";
import { useSettingsDevTools } from "@/components/settings/settings-dev-tools";
import { useTranslation } from "react-i18next";
import { CheckCircle2, ChevronDown, Play, AlertCircle, Save, Copy } from "lucide-react";
import {
    Select,
    SelectTrigger,
    SelectValue,
    SelectContent,
    SelectItem,
    SelectSeparator,
} from "@/components/ui/select";
import { Combobox } from "@/components/ui/combobox";
import {
    type AIProviderConfig,
    type AIProviderPreset,
    type TestAIProviderResult,
} from "@/lib/wails-shim-app";
import { BrowserOpenURL } from "../../../../wailsjs/runtime/runtime";
import { toast } from "sonner";
import {
    GetAIProviderConfig,
    SaveAIProviderConfig,
    SetCachedAIProviderModels,
    TestAIProvider,
    BuildProviderTestCurl,
    ListAIProviderModels,
    ListAIProviderPresets,
} from "@/lib/wails-shim-app";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
import { SettingsCard, SettingsSectionHeader } from "@/components/settings/settings-primitives";
import { SettingsFailureLine, useSettingsFailure } from "@/components/settings/settings-failure";
import { YourAgentsSection } from "./your-agents-section";
import { YourAISection } from "./your-ai-section";

// Non-preset picker entries: the two external-agent backends. The Isoform
// gateway has been retired, so it is no longer selectable. API providers
// come from the backend preset catalog (ListAIProviderPresets), so they are
// not listed here.
const AI_PROVIDER_OPTIONS = [
    { value: "claude-code", labelKey: "providerOptionClaudeCode", hintKey: "providerHintClaudeCode" },
    { value: "codex", labelKey: "providerOptionCodex", hintKey: "providerHintCodex" },
] as const;

const CLAUDE_CODE_DEFAULT_MODEL = "sonnet";
const CLAUDE_CODE_DEFAULT_LIGHT_MODEL = "haiku";
const CODEX_DEFAULT_MODEL = "gpt-5.5";
const CODEX_DEFAULT_LIGHT_MODEL = "gpt-5.6-luna";

// Ordered most to least capable, matching the newest-first Codex list below.
const CLAUDE_CODE_MODEL_OPTIONS = [
    { value: "fable", label: "Fable" },
    { value: "opus", label: "Opus" },
    { value: "sonnet", label: "Sonnet" },
    { value: "haiku", label: "Haiku" },
] as const;

const CODEX_MODEL_OPTIONS = [
    { value: "gpt-5.6-sol", label: "GPT-5.6 Sol" },
    { value: "gpt-5.6-terra", label: "GPT-5.6 Terra" },
    { value: "gpt-5.6-luna", label: "GPT-5.6 Luna" },
    { value: "gpt-5.5", label: "GPT-5.5" },
    { value: "gpt-5.4", label: "GPT-5.4" },
    { value: "gpt-5.4-mini", label: "GPT-5.4 Mini" },
    { value: "gpt-5.3-codex-spark", label: "GPT-5.3 Codex Spark" },
] as const;

// AIProviderSettingsCard is the BYOA ("bring your own agent / AI-API")
// surface. Two BYOA modes:
//   - API key: route the in-process agent through the user's own
//     Anthropic- or OpenAI-compatible endpoint + key.
//   - External agent: spawn the user's own Claude Code / Codex CLI.
// Mirrors EmbeddingSettingsCard's layout/styling.
export function AIProviderSettingsCard({
    flat = false,
}: {
    // Render only the form, with no header and no card of its own, for a
    // surface that already wraps it in a card (Settings > Assistant).
    flat?: boolean;
}) {
    const { t } = useTranslation('settings');
    const devTools = useSettingsDevTools();
    const headerDescription = t('aiProvider.description');
    // `selection` is the picker value: a preset id ("openai", "deepseek",
    // "custom", ...) or an external agent ("claude-code" / "codex"). The Isoform
    // gateway has been retired and is no longer selectable. The wire format
    // saved to AIProviderConfig.provider is derived from the chosen preset's
    // apiFormat.
    const [selection, setSelection] = useState("anthropic");
    const [apiKey, setApiKey] = useState("");
    const [baseUrl, setBaseUrl] = useState("");
    const [model, setModel] = useState("");
    // Optional "small-brain" model for cheap background tasks (summaries,
    // titles, memory extraction). Empty = use the same model as the big-brain.
    const [lightModel, setLightModel] = useState("");
    const [command, setCommand] = useState("");
    const [disableSandbox, setDisableSandbox] = useState(false);
    const [presets, setPresets] = useState<AIProviderPreset[]>([]);
    const [saving, setSaving] = useState(false);
    const [testing, setTesting] = useState(false);
    const [testResult, setTestResult] =
        useState<TestAIProviderResult | null>(null);
    // Copy-paste curl command mirroring the probe, for manual debugging when
    // the in-app test surfaces only an opaque status (e.g. an empty-body 500).
    const [curlCmd, setCurlCmd] = useState("");
    // Model dropdown options fetched from the provider's listing endpoint.
    const [models, setModels] = useState<string[]>([]);
    const [loadingModels, setLoadingModels] = useState(false);
    /** In place of toasts (settings-review ST9): one line under the model field, one under Save and Test. */
    const modelsFailure = useSettingsFailure();
    const formFailure = useSettingsFailure();
    const [modelsNone, setModelsNone] = useState(false);
    // The less common fields (CLI path, sandbox, curl) sit behind "More options".
    const [moreOpen, setMoreOpen] = useState(false);

    useEffect(() => {
        ListAIProviderPresets().then((list) => setPresets(list ?? []));
        GetAIProviderConfig().then((cfg) => {
            // Restore the picker selection: prefer the stored preset id, fall
            // back to the wire format for legacy configs (provider "openai" /
            // "anthropic" / "claude-code" / "codex" all double as ids).
            const sel = cfg.presetId || cfg.provider || "anthropic";
            setSelection(sel);
            setApiKey(cfg.apiKey || "");
            setBaseUrl(cfg.baseUrl || "");
            setCommand(cfg.command || "");
            setDisableSandbox(cfg.disableSandbox ?? false);
            // A custom CLI path or a disabled sandbox stays in view, so a
            // risky setting is never hidden behind "More options".
            if (cfg.command || cfg.disableSandbox) setMoreOpen(true);
            if (sel === "claude-code") {
                setModel(cfg.model || CLAUDE_CODE_DEFAULT_MODEL);
                setLightModel(cfg.lightModel || CLAUDE_CODE_DEFAULT_LIGHT_MODEL);
            } else if (sel === "codex") {
                setModel(cfg.model || CODEX_DEFAULT_MODEL);
                setLightModel(cfg.lightModel || CODEX_DEFAULT_LIGHT_MODEL);
            } else {
                setModel(cfg.model || "");
                setLightModel(cfg.lightModel || "");
            }
            // Repopulate the dropdowns from the last fetched list so the user
            // doesn't have to click "Fetch models" again on every reopen.
            setModels(cfg.cachedModels ?? []);
        });
    }, []);

    // The gateway option is gone everywhere, so a stored "yansu" selection
    // (legacy config) would strand the picker on an invisible value. Once
    // presets load, snap any non-existent selection to the first API preset.
    useEffect(() => {
        if (presets.length === 0) return;
        const isPreset = presets.some((p) => p.id === selection);
        const isExt = selection === "claude-code" || selection === "codex";
        if (!isPreset && !isExt) {
            setSelection(presets[0].id);
        }
    }, [presets, selection]);

    const selectedPreset = presets.find((p) => p.id === selection) ?? null;
    const isAPI = selectedPreset !== null;
    const isExternal = selection === "claude-code" || selection === "codex";
    const requiresKey = selectedPreset?.requiresKey ?? true;
    const externalOption = AI_PROVIDER_OPTIONS.find((o) => o.value === selection);
    const externalModelOptions = useMemo(
        () =>
            selection === "claude-code"
                ? CLAUDE_CODE_MODEL_OPTIONS
                : CODEX_MODEL_OPTIONS,
        [selection],
    );

    const buildConfig = (): AIProviderConfig => ({
        // Save the wire format (the runner contract); the preset id is a
        // UI/listing convenience persisted alongside it.
        provider: selectedPreset ? selectedPreset.apiFormat : selection,
        presetId: selectedPreset ? selectedPreset.id : "",
        apiKey,
        baseUrl,
        model,
        lightModel,
        command,
        disableSandbox: isExternal ? disableSandbox : undefined,
        // Persist the fetched list so the dropdowns survive a reopen.
        cachedModels: models,
    });

    const handleSelectProvider = (next: string) => {
        if (next === selection) return;
        setSelection(next);
        setModels([]);
        setTestResult(null);
        setCurlCmd("");
        // Switching provider invalidates provider-specific fields: the API key
        // belongs to the previous provider, and the stored model is unlikely to
        // exist on the new endpoint. Clear the key and prefill the new preset's
        // defaults so stale values from another provider can't be saved.
        setApiKey("");
        const preset = presets.find((p) => p.id === next);
        if (preset) {
            setBaseUrl(preset.baseUrl);
            setModel(preset.seedModels?.[0] ?? "");
            setLightModel("");
        } else if (next === "claude-code") {
            setModel(CLAUDE_CODE_DEFAULT_MODEL);
            setLightModel(CLAUDE_CODE_DEFAULT_LIGHT_MODEL);
        } else if (next === "codex") {
            setModel(CODEX_DEFAULT_MODEL);
            setLightModel(CODEX_DEFAULT_LIGHT_MODEL);
        } else {
            setModel("");
            setLightModel("");
        }
    };

    const handleSave = async () => {
        if (saving) return;
        setSaving(true);
        setTestResult(null);
        formFailure.clear();
        try {
            await SaveAIProviderConfig(buildConfig());
        } catch {
            formFailure.fail(t('settingChange.failed'), () => void handleSave());
        } finally {
            setSaving(false);
        }
    };

    const handleFetchModels = () => {
        setLoadingModels(true);
        modelsFailure.clear();
        setModelsNone(false);
        ListAIProviderModels(buildConfig())
            .then((list) => {
                const next = list ?? [];
                setModels(next);
                if (next.length === 0) {
                    setModelsNone(true);
                } else {
                    // Persist the moment the fetch succeeds (no Save needed) so
                    // the dropdowns repopulate on reopen instead of forcing a
                    // re-fetch every time. Best-effort: a cache write failure
                    // must not break the fetch UX.
                    SetCachedAIProviderModels(next).catch(() => {});
                }
            })
            .catch(() => modelsFailure.fail(t('aiProvider.modelsFetchFailed'), handleFetchModels))
            .finally(() => setLoadingModels(false));
    };

    const handleTest = () => {
        setTesting(true);
        setTestResult(null);
        setCurlCmd("");
        formFailure.clear();
        TestAIProvider(buildConfig())
            .then((result) => {
                // The inline result banner below the buttons is the single
                // surface for the outcome — no toast on top of it.
                setTestResult(result);
                if (!result.success) {
                    // Surface the equivalent curl so the user can rerun it in a
                    // terminal and see the full response the in-app probe can't.
                    BuildProviderTestCurl(buildConfig())
                        .then((cmd) => setCurlCmd(cmd || ""))
                        .catch(() => {});
                }
            })
            .catch(() => formFailure.fail(t('aiProvider.testErrorToast'), handleTest))
            .finally(() => setTesting(false));
    };

    const handleCopyCurl = async () => {
        const cmd = await BuildProviderTestCurl(buildConfig()).catch(() => "");
        if (!cmd) return;
        setCurlCmd(cmd);
        try {
            await navigator.clipboard.writeText(cmd);
            toast.success(t('aiProvider.curlCopied'));
        } catch {
            /* clipboard blocked — the command is still shown below to copy manually */
        }
    };

    const form = (
        <div className="flex flex-col gap-4">
            <div className="space-y-1.5">
                <label className="text-xs font-medium text-ink">
                    {t('aiProvider.providerLabel')}
                </label>
                <Select
                    value={selection}
                    onValueChange={handleSelectProvider}
                >
                    <SelectTrigger className="w-full text-xs">
                        <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                        {presets.map((p) => (
                            <SelectItem
                                key={p.id}
                                value={p.id}
                                className="text-xs"
                            >
                                {p.name}
                            </SelectItem>
                        ))}
                                                    <>
                                {/* Divider: API-key providers above, local
                                    agent CLIs below. */}
                                <SelectSeparator />
                                <SelectItem value="claude-code" className="text-xs">
                                    {t('aiProvider.providerOptionClaudeCode')}
                                </SelectItem>
                                <SelectItem value="codex" className="text-xs">
                                    {t('aiProvider.providerOptionCodex')}
                                </SelectItem>
                            </>
                    </SelectContent>
                </Select>
                <p className="text-[10px] text-muted leading-relaxed">
                    {isAPI
                        ? t('aiProvider.providerHintApi', { name: selectedPreset?.name ?? "" })
                        : externalOption &&
                          t(`aiProvider.${externalOption.hintKey}` as 'aiProvider.providerHintClaudeCode')}
                </p>
            </div>

            {isAPI && (
                <>
                    <div className="space-y-1.5">
                        <label className="text-xs font-medium text-ink">
                            {t('aiProvider.baseUrlLabel')}{" "}
                            <span className="text-muted font-normal">
                                {t('aiProvider.optionalSuffix')}
                            </span>
                        </label>
                        <input
                            type="text"
                            className="w-full px-3 py-1.5 text-xs rounded-md border border-border bg-panel text-ink placeholder:text-muted focus:outline-none focus:ring-1 focus:ring-accent font-mono"
                            placeholder={
                                selectedPreset?.baseUrl ||
                                (selectedPreset?.apiFormat === "anthropic"
                                    ? "https://api.anthropic.com"
                                    : "https://api.openai.com/v1")
                            }
                            value={baseUrl}
                            onChange={(e) => setBaseUrl(e.target.value)}
                        />
                        <p className="text-[10px] text-muted">
                            {t('aiProvider.baseUrlHint')}
                        </p>
                    </div>

                    <div className="space-y-1.5">
                        <div className="flex items-center justify-between gap-2">
                            <label className="text-xs font-medium text-ink">
                                {t('aiProvider.apiKeyLabel')}{" "}
                                {!requiresKey && (
                                    <span className="text-muted font-normal">
                                        {t('aiProvider.optionalSuffix')}
                                    </span>
                                )}
                            </label>
                            {selectedPreset?.apiKeyUrl && (
                                <button
                                    type="button"
                                    className="text-[10px] text-accent hover:underline"
                                    onClick={() =>
                                        BrowserOpenURL(selectedPreset.apiKeyUrl)
                                    }
                                >
                                    {t('aiProvider.getApiKey')}
                                </button>
                            )}
                        </div>
                        <input
                            type="password"
                            className="w-full px-3 py-1.5 text-xs rounded-md border border-border bg-panel text-ink placeholder:text-muted focus:outline-none focus:ring-1 focus:ring-accent font-mono"
                            placeholder={t('aiProvider.apiKeyPlaceholder')}
                            value={apiKey}
                            onChange={(e) => setApiKey(e.target.value)}
                        />
                        <p className="text-[10px] text-muted">
                            {requiresKey
                                ? t('aiProvider.apiKeyHint')
                                : t('aiProvider.apiKeyHintLocal')}
                        </p>
                    </div>

                    <div className="space-y-1.5">
                        <label className="text-xs font-medium text-ink">
                            {t('aiProvider.modelLabel')}{" "}
                            <span className="text-muted font-normal">
                                {t('aiProvider.optionalSuffix')}
                            </span>
                        </label>
                        <div className="flex items-center gap-2">
                            <div className="flex-1 min-w-0">
                                <Combobox
                                    options={(models.length > 0
                                        ? models
                                        : selectedPreset?.seedModels ?? []
                                    ).map((m) => ({ value: m, label: m }))}
                                    value={model}
                                    onValueChange={setModel}
                                    allowCustom
                                    placeholder={
                                        selectedPreset?.seedModels?.[0] ||
                                        (selectedPreset?.apiFormat === "anthropic"
                                            ? "claude-sonnet-4-6"
                                            : "gpt-4o")
                                    }
                                    searchPlaceholder={t('aiProvider.modelSearchPlaceholder')}
                                    emptyMessage={t('aiProvider.modelsEmpty')}
                                />
                            </div>
                            <button
                                type="button"
                                className="shrink-0 inline-flex items-center gap-1.5 px-3 py-1.5 rounded-md text-xs font-medium border border-border bg-panel text-ink hover:bg-ink/5 transition-colors disabled:opacity-50"
                                onClick={handleFetchModels}
                                disabled={loadingModels || (requiresKey && !apiKey.trim())}
                            >
                                {loadingModels
                                    ? t('aiProvider.fetchingModels')
                                    : t('aiProvider.fetchModels')}
                            </button>
                        </div>
                        {models.length > 0 && (
                            <p className="text-[10px] text-muted">
                                {t('aiProvider.modelsFound', { count: models.length })}
                            </p>
                        )}
                        {modelsNone && <p className="text-[12px] text-muted">{t('aiProvider.modelsNone')}</p>}
                        <SettingsFailureLine failure={modelsFailure.failure} />
                        <p className="text-[10px] text-muted">
                            {t('aiProvider.modelHint')}
                        </p>
                    </div>

                    <div className="space-y-1.5">
                        <label className="text-xs font-medium text-ink">
                            {t('aiProvider.lightModelLabel')}{" "}
                            <span className="text-muted font-normal">
                                {t('aiProvider.optionalSuffix')}
                            </span>
                        </label>
                        <div className="flex-1 min-w-0">
                            <Combobox
                                options={(models.length > 0
                                    ? models
                                    : selectedPreset?.seedModels ?? []
                                ).map((m) => ({ value: m, label: m }))}
                                value={lightModel}
                                onValueChange={setLightModel}
                                allowCustom
                                placeholder={model || t('aiProvider.lightModelPlaceholder')}
                                searchPlaceholder={t('aiProvider.modelSearchPlaceholder')}
                                emptyMessage={t('aiProvider.modelsEmpty')}
                            />
                        </div>
                        <p className="text-[10px] text-muted">
                            {t('aiProvider.lightModelHint')}
                        </p>
                    </div>
                </>
            )}

            {isExternal && (
                <>
                    <div className="space-y-1.5">
                        <label className="text-xs font-medium text-ink">
                            {t('aiProvider.modelLabel')}
                        </label>
                        <Combobox
                            options={externalModelOptions}
                            value={model}
                            onValueChange={setModel}
                            allowCustom
                            searchPlaceholder={t('aiProvider.modelSearchPlaceholder')}
                            emptyMessage={t('aiProvider.modelsEmpty')}
                        />
                        <p className="text-[10px] text-muted">
                            {t('aiProvider.externalModelHint')}
                        </p>
                    </div>

                    <div className="space-y-1.5">
                        <label className="text-xs font-medium text-ink">
                            {t('aiProvider.lightModelLabel')}
                        </label>
                        <Combobox
                            options={externalModelOptions}
                            value={lightModel}
                            onValueChange={setLightModel}
                            allowCustom
                            searchPlaceholder={t('aiProvider.modelSearchPlaceholder')}
                            emptyMessage={t('aiProvider.modelsEmpty')}
                        />
                        <p className="text-[10px] text-muted">
                            {t('aiProvider.externalLightModelHint')}
                        </p>
                    </div>
                </>
            )}

            {(isAPI || isExternal) && (
                <Collapsible open={moreOpen} onOpenChange={setMoreOpen} className="flex flex-col">
                    <CollapsibleTrigger className="flex items-center gap-1.5 self-start text-xs text-muted hover:text-ink transition-colors">
                        <ChevronDown
                            aria-hidden
                            className={`h-3 w-3 shrink-0 transition-transform ${moreOpen ? "rotate-180" : ""}`}
                        />
                        {t('aiProvider.moreOptions')}
                    </CollapsibleTrigger>
                    <CollapsibleContent className="flex flex-col gap-4 pt-3">
                        {isExternal && (
                            <>
                                <div className="space-y-1.5">
                                    <label className="text-xs font-medium text-ink">
                                        {t('aiProvider.commandLabel')}{" "}
                                        <span className="text-muted font-normal">
                                            {t('aiProvider.optionalSuffix')}
                                        </span>
                                    </label>
                                    <input
                                        type="text"
                                        className="w-full px-3 py-1.5 text-xs rounded-md border border-border bg-panel text-ink placeholder:text-muted focus:outline-none focus:ring-1 focus:ring-accent font-mono"
                                        placeholder={selection === "codex" ? "codex" : "claude"}
                                        value={command}
                                        onChange={(e) => setCommand(e.target.value)}
                                    />
                                    <p className="text-[10px] text-muted">
                                        {t('aiProvider.commandHint')}
                                    </p>
                                </div>

                                {/* Grid keeps the checkbox vertically centered against the
                                    title line (row 1) while the hint indents under the
                                    title (col 2) — no pixel-nudge margins. The hint turns
                                    to the warning tone while the sandbox is off. */}
                                <label className="grid grid-cols-[auto_1fr] items-center gap-x-2 gap-y-0.5 cursor-pointer">
                                    <input
                                        type="checkbox"
                                        className="rounded border-border"
                                        checked={disableSandbox}
                                        onChange={(e) => setDisableSandbox(e.target.checked)}
                                    />
                                    <span className="text-xs font-medium text-ink">
                                        {t('aiProvider.disableSandboxLabel')}
                                    </span>
                                    <p
                                        className={`col-start-2 text-[10px] leading-relaxed ${disableSandbox ? "text-warning" : "text-muted"}`}
                                    >
                                        {t('aiProvider.disableSandboxHint')}
                                    </p>
                                </label>
                            </>
                        )}
                        {devTools && isAPI && (
                            <div className="flex flex-col gap-1.5">
                                <button
                                    className="inline-flex items-center gap-1.5 self-start px-4 py-2 rounded-md text-xs font-medium border border-border bg-panel text-ink hover:bg-ink/5 transition-colors disabled:opacity-50"
                                    onClick={handleCopyCurl}
                                    disabled={
                                        (requiresKey && !apiKey.trim()) || !model.trim()
                                    }
                                >
                                    <Copy className="h-3 w-3" />
                                    {t('aiProvider.copyCurl')}
                                </button>
                                <p className="text-[10px] text-muted leading-relaxed">
                                    {t('aiProvider.curlHint')}
                                </p>
                            </div>
                        )}
                    </CollapsibleContent>
                </Collapsible>
            )}

            <div className="flex items-center gap-2">
                <button
                    className="inline-flex items-center gap-1.5 px-4 py-2 rounded-md text-xs font-medium patina-verdigris bg-solid-btn-bg text-solid-btn-fg shadow-[var(--shadow-solid-btn)] hover:opacity-90 transition-colors disabled:opacity-50"
                    onClick={handleSave}
                    disabled={
                        saving ||
                        (isAPI &&
                            ((requiresKey && !apiKey.trim()) || !model.trim()))
                    }
                >
                    <Save className="h-3 w-3" />
                    {saving ? t('aiProvider.savingButton') : t('aiProvider.saveButton')}
                </button>
                {(isAPI || isExternal) && (
                    <button
                        className="inline-flex items-center gap-1.5 px-4 py-2 rounded-md text-xs font-medium border border-border bg-panel text-ink hover:bg-ink/5 transition-colors disabled:opacity-50"
                        onClick={handleTest}
                        disabled={
                            testing ||
                            saving ||
                            (isAPI &&
                                ((requiresKey && !apiKey.trim()) ||
                                    !model.trim()))
                        }
                    >
                        <Play className="h-3 w-3" />
                        {testing ? t('aiProvider.testingButton') : t('aiProvider.testButton')}
                    </button>
                )}
            </div>

            <SettingsFailureLine failure={formFailure.failure} />

            {testResult && (
                <div
                    className={`flex flex-col self-start rounded-md border px-3 py-2 space-y-1 text-[11px] ${testResult.success ? "border-accent-2/40 bg-accent-2/5" : "border-copper/30 bg-copper/5"}`}
                >
                    <div className="flex items-center gap-1.5 font-medium">
                        {testResult.success ? (
                            <CheckCircle2 className="h-3.5 w-3.5 text-accent-2" />
                        ) : (
                            <AlertCircle className="h-3.5 w-3.5 text-copper" />
                        )}
                        <span
                            className={
                                testResult.success
                                    ? "text-accent-2"
                                    : "text-copper"
                            }
                        >
                            {testResult.success
                                ? t('aiProvider.testPassed')
                                : t('aiProvider.testFailed')}
                            {testResult.success && testResult.latencyMs != null && (
                                <span className="text-muted font-normal ml-1.5">
                                    {testResult.latencyMs}ms
                                </span>
                            )}
                        </span>
                    </div>
                    {!testResult.success && (
                        <p className="text-muted break-words">
                            {testResult.error}
                        </p>
                    )}
                </div>
            )}

            {devTools && curlCmd && (
                <div className="rounded-md border border-border bg-panel p-3 space-y-2 text-[11px]">
                    <p className="text-muted">
                        {t('aiProvider.curlHint')}
                    </p>
                    <pre className="overflow-x-auto whitespace-pre-wrap break-all rounded bg-ink/5 p-2 font-mono text-[10px] text-ink">
                        {curlCmd}
                    </pre>
                    <button
                        className="inline-flex items-center gap-1.5 px-3 py-1.5 rounded-md text-[11px] font-medium border border-border bg-panel text-ink hover:bg-ink/5 transition-colors"
                        onClick={() => {
                            navigator.clipboard
                                ?.writeText(curlCmd)
                                .then(() =>
                                    toast.success(t('aiProvider.curlCopied')),
                                )
                                .catch(() => {});
                        }}
                    >
                        <Copy className="h-3 w-3" />
                        {t('aiProvider.copyCurl')}
                    </button>
                </div>
            )}
        </div>
    );

    if (flat) return form;

    return (
        <div className="flex flex-col gap-6">
                <SettingsSectionHeader
                    title={t('aiProvider.title')}
                    description={headerDescription}
                />

            <SettingsCard title={t('aiProvider.providerCardTitle')}>{form}</SettingsCard>
        </div>
    );
}

// ── Assistant page ──

/**
 * Your agents (page id "ai-provider"): each agent's job and Connect an
 * agent first (computer-settings v1.0.0 rule 5); then who answers, with each agent's model and
 * effort, how much usage is left and which tools are on this Mac, with the
 * user's own API key or model under Advanced. Isoform's own prompts are not
 * editable here. The "subscription" id resolves here.
 */
export function AssistantPage() {
    const { t } = useTranslation("settings");
    return (
        <div className="flex flex-col gap-6">
            <YourAgentsSection />
            {/* Interim: Your AI stays on this page, where the model accounts
                were, under its own heading until the Settings registry's
                "Your AI" entry (just before Your agents) renders
                YourAISection (G1). Then these two lines go. */}
            <SettingsSectionHeader title={t("yourAI.title")} />
            <YourAISection />
        </div>
    );
}
