import { useCallback, useEffect, useRef, useState } from "react";
import { useSettingsDevTools } from "@/components/settings/settings-dev-tools";
import { useTranslation } from "react-i18next";
import { Download, CheckCircle2, Play, AlertCircle, Save, ShieldCheck } from "lucide-react";
import {
    Select,
    SelectTrigger,
    SelectValue,
    SelectContent,
    SelectItem,
} from "@/components/ui/select";
import {
    GetMemoryConfig,
    GetMemoryStatus,
    GetPlatformPermissions,
    SaveMemoryConfig,
    TestEmbedding,
} from "../../../../wailsjs/go/main/App";
import { EventsOn } from "../../../../wailsjs/runtime/runtime";
import type { main } from "../../../../wailsjs/go/models";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { AIMemoryImportDialog } from "@/components/ai-memory-import-dialog";
import { SelfUpdatesSettings } from "@/components/self-updates-settings";
import { MemoryContextDebugSettings } from "@/components/memory-context-debug-settings";
import { DailyTopicsPreview } from "@/components/daily-topics-preview";
import { SettingsCard, SettingsRow } from "@/components/settings/settings-primitives";
import { SettingsAdvanced } from "@/components/settings/settings-advanced";
import { formatBytes } from "@/components/settings/settings-format";
import {
    LearnByComparisonSettings,
    LearningFeaturesSettings,
} from "@/components/settings/pages/learning-features";

// ── Search index (Advanced) ──
//
// How Isoform finds related memories when you ask: the embedding provider
// behind semantic search in Memory and Activity, its status and the model
// download. Everything here is optional; Automatic works for most people.

// Labels are localized at render time via t(`embedding.providerOption${value}`).
const EMBEDDING_PROVIDER_OPTIONS = [
    { value: "auto" },
    { value: "local" },
    { value: "ollama" },
    { value: "openai" },
    { value: "none" },
] as const;

function formatSpeed(bytesPerSec: number): string {
    if (bytesPerSec < 1024) return `${Math.round(bytesPerSec)} B/s`;
    if (bytesPerSec < 1024 * 1024)
        return `${(bytesPerSec / 1024).toFixed(1)} KB/s`;
    return `${(bytesPerSec / (1024 * 1024)).toFixed(1)} MB/s`;
}

interface DownloadProgress {
    fileName: string;
    downloaded: number;
    total: number;
    bytesPerSec: number;
}

const INPUT_CLASS =
    "w-full px-3 py-1.5 text-xs rounded-md border border-border bg-panel text-ink placeholder:text-muted focus:outline-none focus:ring-1 focus:ring-accent font-mono";

function SearchIndexSettings() {
    const { t } = useTranslation('settings');
    const [status, setStatus] = useState<main.MemoryStoreStatus | null>(null);
    const [provider, setProvider] = useState("");
    const [apiKey, setApiKey] = useState("");
    const [baseUrl, setBaseUrl] = useState("");
    const [model, setModel] = useState("");
    const [saving, setSaving] = useState(false);
    const [dlProgress, setDlProgress] = useState<DownloadProgress | null>(null);
    const [testing, setTesting] = useState(false);
    const statusRefreshTimer = useRef<ReturnType<typeof setTimeout>>();
    useEffect(() => () => clearTimeout(statusRefreshTimer.current), []);
    const [testResult, setTestResult] =
        useState<main.TestEmbeddingResult | null>(null);

    useEffect(() => {
        GetMemoryConfig().then((cfg) => {
            setProvider(cfg.provider || "auto");
            setApiKey(cfg.apiKey);
            setBaseUrl(cfg.baseUrl);
            setModel(cfg.model);
        });
        GetMemoryStatus().then(setStatus);
    }, []);

    // Listen for download progress events
    useEffect(() => {
        const unsub = EventsOn(
            "embedding:download-progress",
            (data: DownloadProgress) => {
                if (data.total > 0 && data.downloaded >= data.total) {
                    // Download complete — clear after brief delay
                    setTimeout(() => setDlProgress(null), 500);
                } else {
                    setDlProgress(data);
                }
            },
        );
        return unsub;
    }, []);

    // Refresh status whenever the memory index changes so the file/chunk/
    // cached-embeds counters stay live without requiring a panel reopen.
    useEffect(() => {
        const unsub = EventsOn("memory:updated", () => {
            GetMemoryStatus().then(setStatus);
        });
        return unsub;
    }, []);

    const needsApiKey = provider === "openai";
    const showAdvanced = provider === "openai" || provider === "ollama";

    // The Go side reports the provider that is actually running: "local-onnx",
    // "ollama", "openai" or "none"; an empty value means Automatic.
    const providerName = (value: string): string => {
        switch (value) {
            case "":
            case "auto":
                return t('embedding.providerNames.auto');
            case "local":
            case "local-onnx":
                return t('embedding.providerNames.local');
            case "ollama":
                return t('embedding.providerNames.ollama');
            case "openai":
                return t('embedding.providerNames.openai');
            case "none":
                return t('embedding.providerNames.none');
            default:
                return value;
        }
    };

    const handleSave = () => {
        setSaving(true);
        setTestResult(null);
        const cfg = {
            provider: provider === "auto" ? "" : provider,
            apiKey,
            baseUrl,
            model,
        } as main.MemoryStoreConfig;
        SaveMemoryConfig(cfg)
            .then(() => {
                toast.success(t('embedding.saveSuccessToast'));
                // Refresh status after re-init
                statusRefreshTimer.current = setTimeout(() => GetMemoryStatus().then(setStatus), 1000);
            })
            .catch((err) => toast.error(t('embedding.saveFailedToast', { error: String(err) })))
            .finally(() => setSaving(false));
    };

    const handleTest = () => {
        setTesting(true);
        setTestResult(null);
        const cfg = {
            provider: provider === "auto" ? "" : provider,
            apiKey,
            baseUrl,
            model,
        } as main.MemoryStoreConfig;
        TestEmbedding(cfg)
            .then((result) => {
                setTestResult(result);
            })
            .catch((err) => toast.error(t('embedding.testErrorToast', { error: String(err) })))
            .finally(() => setTesting(false));
    };

    return (
        <SettingsCard
            title={t('embedding.title')}
            description={t('embedding.description')}
        >
            <div className="flex flex-col gap-4">
                {status && (
                    <div className="space-y-1.5 rounded-lg border border-border bg-panel p-3 text-[11px]">
                        <div className="flex items-center justify-between">
                            <span className="text-muted">{t('embedding.statusProvider')}</span>
                            <span className="text-ink">{providerName(status.provider)}</span>
                        </div>
                        <div className="flex items-center justify-between">
                            <span className="text-muted">{t('embedding.statusModel')}</span>
                            <span className="text-ink">
                                {status.model || "—"}
                            </span>
                        </div>
                        <div className="flex items-center justify-between">
                            <span className="text-muted">{t('embedding.statusVectorSearch')}</span>
                            <span
                                className={
                                    status.vectorReady
                                        ? "text-accent-2 font-medium"
                                        : "text-ink"
                                }
                            >
                                {status.vectorReady ? t('embedding.statusActive') : t('embedding.statusInactive')}
                            </span>
                        </div>
                        <div className="flex items-center justify-between">
                            <span className="text-muted">{t('embedding.statusIndexed')}</span>
                            <span className="text-ink">
                                {t('embedding.statusIndexedValue', { count: status.files, files: status.files, chunks: status.chunks })}
                            </span>
                        </div>
                        {status.cachedEmbeds > 0 && (
                            <div className="flex items-center justify-between">
                                <span className="text-muted">
                                    {t('embedding.statusCachedEmbeddings')}
                                </span>
                                <span className="text-ink">
                                    {status.cachedEmbeds}
                                </span>
                            </div>
                        )}
                    </div>
                )}

                {dlProgress && (
                    <div className="space-y-2">
                        <div className="flex items-center justify-between text-[11px]">
                            <span className="text-ink font-medium flex items-center gap-1.5">
                                <Download className="h-3 w-3 animate-pulse text-accent-2" />
                                {t('embedding.downloading', { file: dlProgress.fileName })}
                            </span>
                            <span className="text-muted">
                                {dlProgress.bytesPerSec > 0 &&
                                    formatSpeed(dlProgress.bytesPerSec)}
                            </span>
                        </div>
                        <div className="w-full h-1.5 bg-border/50 rounded-full overflow-hidden">
                            <div
                                className="h-full bg-accent-2 rounded-full transition-all duration-300"
                                style={{
                                    width:
                                        dlProgress.total > 0
                                            ? `${Math.min(100, (dlProgress.downloaded / dlProgress.total) * 100)}%`
                                            : "50%",
                                }}
                            />
                        </div>
                        <div className="flex items-center justify-between text-[10px] text-muted">
                            <span>
                                {formatBytes(dlProgress.downloaded)}
                                {dlProgress.total > 0
                                    ? ` / ${formatBytes(dlProgress.total)}`
                                    : ""}
                            </span>
                            {dlProgress.total > 0 && (
                                <span>
                                    {Math.round(
                                        (dlProgress.downloaded /
                                            dlProgress.total) *
                                            100,
                                    )}
                                    %
                                </span>
                            )}
                        </div>
                    </div>
                )}

                <div className="space-y-1.5">
                    <label className="text-xs font-medium text-ink">
                        {t('embedding.providerLabel')}
                    </label>
                    <Select value={provider} onValueChange={setProvider}>
                        <SelectTrigger className="w-full text-xs" aria-label={t('embedding.providerLabel')}>
                            <SelectValue />
                        </SelectTrigger>
                        <SelectContent>
                            {EMBEDDING_PROVIDER_OPTIONS.map((opt) => (
                                <SelectItem
                                    key={opt.value}
                                    value={opt.value}
                                    className="text-xs"
                                >
                                    {t(`embedding.providerOption${opt.value.charAt(0).toUpperCase()}${opt.value.slice(1)}` as 'embedding.providerOptionAuto')}
                                </SelectItem>
                            ))}
                        </SelectContent>
                    </Select>
                    <p className="text-[10px] text-muted leading-relaxed">
                        {provider === "auto" && t('embedding.providerHintAuto')}
                        {provider === "local" && t('embedding.providerHintLocal')}
                        {provider === "ollama" && t('embedding.providerHintOllama')}
                        {provider === "openai" && t('embedding.providerHintOpenai')}
                        {provider === "none" && t('embedding.providerHintNone')}
                    </p>
                </div>

                {needsApiKey && (
                    <div className="space-y-1.5">
                        <label htmlFor="search-index-api-key" className="text-xs font-medium text-ink">
                            {t('embedding.apiKeyLabel')}
                        </label>
                        <input
                            id="search-index-api-key"
                            type="password"
                            className={INPUT_CLASS}
                            placeholder={t('embedding.apiKeyPlaceholder')}
                            value={apiKey}
                            onChange={(e) => setApiKey(e.target.value)}
                        />
                        <p className="text-[10px] text-muted">
                            {t('embedding.apiKeyHint')}
                        </p>
                    </div>
                )}

                {showAdvanced && (
                    <>
                        <div className="space-y-1.5">
                            <label htmlFor="search-index-base-url" className="text-xs font-medium text-ink">
                                {t('embedding.baseUrlLabel')}{" "}
                                <span className="text-muted font-normal">
                                    {t('embedding.optionalSuffix')}
                                </span>
                            </label>
                            <input
                                id="search-index-base-url"
                                type="text"
                                className={INPUT_CLASS}
                                placeholder={
                                    provider === "ollama"
                                        ? "http://localhost:11434/v1"
                                        : "https://api.openai.com/v1"
                                }
                                value={baseUrl}
                                onChange={(e) => setBaseUrl(e.target.value)}
                            />
                        </div>
                        <div className="space-y-1.5">
                            <label htmlFor="search-index-model" className="text-xs font-medium text-ink">
                                {t('embedding.modelLabel')}{" "}
                                <span className="text-muted font-normal">
                                    {t('embedding.optionalSuffix')}
                                </span>
                            </label>
                            <input
                                id="search-index-model"
                                type="text"
                                className={INPUT_CLASS}
                                placeholder={
                                    provider === "ollama"
                                        ? "nomic-embed-text"
                                        : "text-embedding-3-small"
                                }
                                value={model}
                                onChange={(e) => setModel(e.target.value)}
                            />
                        </div>
                    </>
                )}

                <div className="flex items-center gap-2">
                    <button
                        className="inline-flex items-center gap-1.5 px-4 py-2 rounded-md text-xs font-medium patina-verdigris bg-solid-btn-bg text-solid-btn-fg shadow-[var(--shadow-solid-btn)] hover:opacity-90 transition-colors disabled:opacity-50"
                        onClick={handleSave}
                        disabled={saving || (needsApiKey && !apiKey.trim())}
                    >
                        <Save className="h-3 w-3" />
                        {saving ? t('embedding.savingButton') : t('embedding.saveButton')}
                    </button>
                    <button
                        className="inline-flex items-center gap-1.5 px-4 py-2 rounded-md text-xs font-medium border border-border bg-panel text-ink hover:bg-ink/5 transition-colors disabled:opacity-50"
                        onClick={handleTest}
                        disabled={testing || saving}
                    >
                        <Play className="h-3 w-3" />
                        {testing ? t('embedding.testingButton') : t('embedding.testButton')}
                    </button>
                </div>

                {testResult && (
                    <div
                        className={`rounded-md border p-3 space-y-1.5 text-[11px] ${testResult.success ? "border-accent-2/40 bg-accent-2/5" : "border-error-border bg-error-bg/50"}`}
                    >
                        <div className="flex items-center gap-1.5 font-medium">
                            {testResult.success ? (
                                <CheckCircle2 className="h-3.5 w-3.5 text-accent-2" />
                            ) : (
                                <AlertCircle className="h-3.5 w-3.5 text-error" />
                            )}
                            <span
                                className={
                                    testResult.success
                                        ? "text-accent-2"
                                        : "text-error"
                                }
                            >
                                {testResult.success
                                    ? t('embedding.testPassed')
                                    : t('embedding.testFailed')}
                            </span>
                        </div>
                        {testResult.success ? (
                            <div className="text-muted space-y-0.5 pl-5">
                                <div>
                                    {t('embedding.testProviderModel', { provider: providerName(testResult.provider), model: testResult.model })}
                                </div>
                                <div>{t('embedding.testDimensions', { dimensions: testResult.dimensions })}</div>
                                <div>{t('embedding.testLatency', { latency: testResult.latencyMs })}</div>
                            </div>
                        ) : (
                            <div className="text-error pl-5">
                                {testResult.error}
                            </div>
                        )}
                    </div>
                )}
            </div>
        </SettingsCard>
    );
}

// ── Import memories (everyday) ──
//
// Moved out of Developer tools > Onboarding. The same dialog as the Memory
// page's import button.

function MemoryImportCard() {
    const { t } = useTranslation("settings");
    const [open, setOpen] = useState(false);

    return (
        <>
            <SettingsCard
                title={t("learningMemory.import.title")}
                description={t("learningMemory.import.description")}
            >
                <div className="flex justify-end">
                    <Button size="sm" onClick={() => setOpen(true)}>
                        <Download className="h-3.5 w-3.5" />
                        {t("learningMemory.import.button")}
                    </Button>
                </div>
            </SettingsCard>
            <AIMemoryImportDialog open={open} onOpenChange={setOpen} />
        </>
    );
}

// ── Contacts prerequisite ──
//
// The People sheet explains what is read and records consent before macOS
// is asked. Settings only links there; it cannot request Contacts itself.

const CONTACTS_PERMISSION_KEY = "contacts";

function ContactsPermissionRow({ onOpenPeople }: { onOpenPeople: () => void }) {
    const { t } = useTranslation("settings");
    const [missing, setMissing] = useState(false);
    const mounted = useRef(true);

    const refresh = useCallback(() => {
        GetPlatformPermissions()
            .then((permissions) => {
                if (!mounted.current) return;
                const item = permissions.items.find((i) => i.key === CONTACTS_PERMISSION_KEY);
                setMissing(Boolean(item && item.actionable && !item.granted));
            })
            .catch(() => {
                // Without a reading there is nothing to prompt for.
            });
    }, []);

    useEffect(() => {
        mounted.current = true;
        refresh();
        let timer: ReturnType<typeof setTimeout>;
        // Coming back from System Settings: read the grant again.
        const onWindowFocus = () => {
            clearTimeout(timer);
            timer = setTimeout(refresh, 500);
        };
        window.addEventListener("focus", onWindowFocus);
        const offPerm = EventsOn("permissions:changed", () => refresh());
        return () => {
            mounted.current = false;
            clearTimeout(timer);
            window.removeEventListener("focus", onWindowFocus);
            offPerm();
        };
    }, [refresh]);

    if (!missing) return null;

    return (
        <>
            <SettingsRow
                title={t("learningMemory.contacts.title")}
                description={t("learningMemory.contacts.description")}
                control={
                    <Button
                        type="button"
                        variant="outline"
                        size="sm"
                        onClick={onOpenPeople}
                    >
                        {t("learningMemory.contacts.openPeople")}
                    </Button>
                }
            />
        </>
    );
}

// ── See what Isoform knows (everyday) ──

interface KnowledgeLinks {
    onOpenMemory: () => void;
    onOpenPeople: () => void;
    onOpenProfile: () => void;
}

function KnowledgeLinksCard({ onOpenMemory, onOpenPeople, onOpenProfile }: KnowledgeLinks) {
    const { t } = useTranslation("settings");
    const openButton = (label: string, onClick: () => void) => (
        <Button type="button" variant="outline" size="sm" aria-label={label} onClick={onClick}>
            {t("learningMemory.knows.open")}
        </Button>
    );

    return (
        <SettingsCard
            title={t("learningMemory.knows.title")}
            description={t("learningMemory.knows.description")}
        >
            <SettingsRow
                title={t("learningMemory.knows.memoryTitle")}
                description={t("learningMemory.knows.memoryDescription")}
                control={openButton(t("learningMemory.knows.openMemory"), onOpenMemory)}
            />
            <SettingsRow
                title={t("learningMemory.knows.peopleTitle")}
                description={t("learningMemory.knows.peopleDescription")}
                control={openButton(t("learningMemory.knows.openPeople"), onOpenPeople)}
            />
            <ContactsPermissionRow onOpenPeople={onOpenPeople} />
            <SettingsRow
                title={t("learningMemory.knows.profileTitle")}
                description={t("learningMemory.knows.profileDescription")}
                control={openButton(t("learningMemory.knows.openProfile"), onOpenProfile)}
            />
        </SettingsCard>
    );
}

// ── Chat context preview (Advanced) ──
//
// Moved from Developer tools > Chat context. The card gives it the same title
// and description styles as every other settings card, so the preview drops
// its own heading block and top divider (hideHeader); its section keeps the
// title as its accessible name.

function ChatContextPreviewCard() {
    const { t } = useTranslation("settings");
    return (
        <SettingsCard
            title={t("debug.memoryContext.title")}
            description={t("debug.memoryContext.description")}
            headerRight={
                <span className="flex shrink-0 items-center gap-1.5 text-[11px] text-muted">
                    <ShieldCheck className="h-3.5 w-3.5" />
                    {t("debug.memoryContext.previewOnly")}
                </span>
            }
        >
            <MemoryContextDebugSettings hideHeader />
        </SettingsCard>
    );
}

// The daily topics preview, while design draws the real screen.
function DailyTopicsPreviewCard() {
    const { t } = useTranslation("settings");
    return (
        <SettingsCard title={t("debug.dailyTopics.title")} description={t("debug.dailyTopics.description")}>
            <DailyTopicsPreview />
        </SettingsCard>
    );
}

// ── Learning and memory page ──

export type LearningMemoryPageProps = KnowledgeLinks;

/**
 * Learning and memory (page id "memory"): what Isoform may learn, the changes
 * it proposes to what it understands about you, importing memories from other
 * AI tools and links to where it keeps what it learned. Learn by comparison,
 * the chat context preview and the search index sit under Advanced.
 *
 * The three link callbacks close Settings and open the About you (Memory) page,
 * the Who you know page and your own card on the Who you know page.
 */
export function LearningMemoryPage({ onOpenMemory, onOpenPeople, onOpenProfile }: LearningMemoryPageProps) {
    const { t } = useTranslation("settings");
    const devTools = useSettingsDevTools();

    return (
        <>
            <LearningFeaturesSettings />
            <SelfUpdatesSettings />
            <MemoryImportCard />
            <KnowledgeLinksCard
                onOpenMemory={onOpenMemory}
                onOpenPeople={onOpenPeople}
                onOpenProfile={onOpenProfile}
            />
            <SettingsAdvanced
                pageId="memory"
                hint={t(devTools ? "learningMemory.advancedHint" : "learningMemory.advancedHintPlain")}
            >
                <LearnByComparisonSettings />
                {devTools && <ChatContextPreviewCard />}
                {devTools && <DailyTopicsPreviewCard />}
                {devTools && <SearchIndexSettings />}
            </SettingsAdvanced>
        </>
    );
}
