import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Trans, useTranslation } from "react-i18next";
import i18n from "@/i18n";
import { activeInterval } from "@/lib/app-activity";
import { useFeatureSwitches } from "@/hooks/useFeatureSwitches";
import { SettingsPageBoundary } from "@/components/settings/settings-page-boundary";
import { SettingsDevToolsContext } from "@/components/settings/settings-dev-tools";
import {
    RefreshCw,
    Loader2,
    Bell,
    ListFilter,
    Play,
    AlertCircle,
    Copy,
    ChevronDown,
    ChevronRight,
    Trash2,
    Activity,
} from "lucide-react";
import { ScrollArea } from "@/components/ui/scroll-area";
import { ConfirmDialog } from "@/components/confirm-dialog";
import { Collapsible, CollapsibleTrigger, CollapsibleContent } from "@/components/ui/collapsible";
import { Button } from "@/components/ui/button";
import { Switch } from "@/components/ui/switch";
import {
    TestDesktopNotification,
    FireDelayedDesktopNotification,
    GetTriageLogs,
    GetActivityPipelineRuns,
    ClearActivityPipelineRuns,
    RunActivityPipelineNow,
    GetBrowserBridgeEnabled,
    SetBrowserBridgeEnabled,
    GetAudioCaptureEvents,
    GetBackgroundAudioStatus,
    GetPerfSnapshot,
    NativeClipboardSetText,
    OpenExternalURL,
    ProbeAXParser,
    ListAXParserCatalog,
    ListAXParserUnsupportedApps,
    ListAXWindowsByBundle,
} from "../../wailsjs/go/main/App";
import {
    type AXProbeResult,
    type AXProbeOptions,
    type AXParserCatalogEntry,
    type AXUnsupportedApp,
} from "@/lib/wails-shim-app";
import { BrowserOpenURL, EventsOn } from "../../wailsjs/runtime/runtime";
import type { main } from "../../wailsjs/go/models";
import { toast } from "sonner";
import { notifyToast } from "@/lib/notify";
import { LongToastTestButton } from "@/components/notification-test-toast";
import { isWailsEnvironment } from "@/lib/browser-bridge";
import { getFileManagerName } from "@/lib/platform";
import { PurposeMapDebugSettings } from "@/components/purpose-map-debug-settings";
import { SettingsCard, SettingsRow } from "@/components/settings/settings-primitives";
import { formatBytes } from "@/components/settings/settings-format";
import {
    copyPerfReport,
    fmtDuration,
    fmtMB,
    fmtMs,
    fmtPct,
    sortSubprocs,
    type PerfSnapshotShape,
} from "@/components/settings/perf-report";
import { SettingsNav } from "@/components/settings/settings-nav";
import {
    SETTINGS_ADVANCED_ANCHOR,
    SettingsAdvancedRevealContext,
    useSettingsAnchorScroll,
} from "@/components/settings/settings-advanced";
import {
    DEFAULT_SETTINGS_PAGE,
    getSettingsPage,
    isDevToolsTopicId,
    isSettingsPageId,
    resolveSettingsTopic,
    shownSettingsDestination,
    type ChatAppId,
    type ResolvedSettingsTopic,
    type SettingsDestination,
    type SettingsOpenOptions,
    type SettingsTopic,
} from "@/components/settings/settings-registry";
import { NotificationsPage } from "@/components/settings/pages/notifications-page";
import { AssistantPage } from "@/components/settings/pages/assistant-page";
import { ObservationPage } from "@/components/settings/pages/observation-page";
import { ClosedEyesPage } from "@/components/settings/pages/closed-eyes-page";
import { YourDataPage } from "@/components/settings/pages/your-data-page";
import { MemoryDebugPage } from "@/components/settings/pages/memory-debug-page";
import { IPhonePage } from "@/components/settings/pages/iphone-page";
import { YourWatchPage } from "@/components/settings/pages/your-watch-page";
import { AgentOfYouPage } from "@/components/settings/pages/agent-of-you/agent-of-you-page";
import { useAoyHeader } from "@/components/settings/pages/agent-of-you/aoy-shared";
import { PluginsPage } from "@/components/settings/pages/plugins-page";
import { ToolServersPage } from "@/components/settings/pages/tool-servers-page";
import { ChatAppsPage } from "@/components/settings/pages/chat-apps-page";
import { OtherAIAppsPage } from "@/components/settings/pages/other-ai-apps-page";
import { HelpAboutPage } from "@/components/settings/pages/help-about-page";

// Shared primitives, the AI provider form and the topic type stay importable from here.
export {
    SettingsCard,
    SettingsRow,
    SettingsSectionHeader,
} from "@/components/settings/settings-primitives";
export { AIProviderSettingsCard } from "@/components/settings/pages/assistant-page";
export type { SettingsOpenOptions, SettingsTopic } from "@/components/settings/settings-registry";

const isSuccessStatus = (s?: string): boolean =>
    s === 'created' || s === 'updated' || s === 'dispatched' || s === 'shadowed' || s === 'scheduled' || s === 'triggered';

// ── Props ──

interface SettingsPageProps {
    onClose: () => void;
    onReplayOnboarding: () => void;
    /** Page to open on, from a deep link. Without one, Settings opens on
     *  What Isoform sees. Legacy ids are resolved through the registry
     *  (resolveSettingsTopic). */
    initialTopic?: SettingsTopic;
    /** How that deep link opens the page: with Advanced expanded, or
     *  scrolled to an anchor. App passes a new object for every
     *  openSettings() call, so asking again for the page already open (after
     *  the person moved away or collapsed Advanced) still navigates. */
    initialOptions?: SettingsOpenOptions;
    /** Developer tools on/off (useDevToolsUnlock): on by default in staging
     *  and dev builds, and Cmd/Ctrl+Shift+Option+D turns them on or off in
     *  any build. Shows the Developer tools group below About. */
    devToolsUnlocked?: boolean;
    /** What Isoform sees › Messages' Contacts row: closes Settings and opens
     *  Who you know. */
    onOpenPeople: () => void;
    /** False while the dialog is closed (the content is retained after the
     *  first open): window-level listeners pause until it shows again. */
    active?: boolean;
    /** Opens the Activity dialog scrolled to `sessionId`, highlights each
     *  (sessionId, snapshotId) pair on the snapshot timeline, and restricts
     *  the left session list to `groupSessionIds` so operators see ONLY the
     *  triage group's sessions — not every session that happens to share a
     *  date filter. Wired by the Activity Pipeline settings page. */
    onJumpToActivity?: (
        sessionId: string,
        frames: {
            kind: string;
            sessionId: string;
            snapshotId: number;
            promptText?: string;
            skipReason?: string;
            ocrConfidence?: number;
            parsedSiblingCount?: number;
            browserUrl?: string;
            dedupChainLen?: number;
        }[],
        pipelineRunId: number,
        groupSessionIds: string[],
    ) => void;
}

/** Where Settings is, plus the current Advanced reveal: non-zero and new for
 *  every navigation that asks for Advanced (SettingsAdvancedRevealContext). */
interface SettingsLocation extends ResolvedSettingsTopic {
    reveal: number;
}

let revealSequence = 0;

function locate(resolved: ResolvedSettingsTopic): SettingsLocation {
    return { ...resolved, reveal: resolved.revealAdvanced ? ++revealSequence : 0 };
}

function initialDestination(
    initialTopic: SettingsTopic | undefined,
    initialOptions: SettingsOpenOptions | undefined,
): SettingsLocation {
    return locate(
        initialTopic
            ? resolveSettingsTopic(initialTopic, initialOptions)
            : { page: DEFAULT_SETTINGS_PAGE },
    );
}

// ── Main component ──

export function SettingsPage({
    onClose,
    onReplayOnboarding,
    initialTopic,
    initialOptions,
    devToolsUnlocked = false,
    onOpenPeople,
    onJumpToActivity,
    active = true,
}: SettingsPageProps) {
    const { t } = useTranslation('settings');
    const [destination, setDestination] = useState<SettingsLocation>(() =>
        initialDestination(initialTopic, initialOptions),
    );
    const showDevTools = devToolsUnlocked;
    // A Developer tools page (remembered from earlier, or a deep link) falls
    // back to What Isoform sees while Developer tools are off.
    // A page of a turned-off feature (Chat apps while Chat is off) lands on
    // Your data, which lists what still leaves.
    const features = useFeatureSwitches();
    const activePage: SettingsDestination =
        !showDevTools && isDevToolsTopicId(destination.page)
            ? DEFAULT_SETTINGS_PAGE
            : shownSettingsDestination(destination.page, features);

    // A deep link that arrives while Settings is already open.
    const lastDeepLink = useRef({ topic: initialTopic, options: initialOptions });
    useEffect(() => {
        const last = lastDeepLink.current;
        if (last.topic === initialTopic && last.options === initialOptions) return;
        lastDeepLink.current = { topic: initialTopic, options: initialOptions };
        setDestination(
            locate(
                initialTopic
                    ? resolveSettingsTopic(initialTopic, initialOptions)
                    : { page: DEFAULT_SETTINGS_PAGE },
            ),
        );
    }, [initialTopic, initialOptions]);

    const selectPage = useCallback(
        (page: SettingsDestination, options?: SettingsOpenOptions) => {
            setDestination(locate(resolveSettingsTopic(page, options)));
        },
        [],
    );

    // The Chat apps list has no Advanced of its own: each app keeps its setup
    // steps and connection details under its own Advanced. So a search or
    // link that asked for Advanced on the list carries over into the first
    // app opened from it.
    const selectChatApp = useCallback((chatApp: ChatAppId | undefined) => {
        setDestination((current) => {
            const next = locate({ page: "chat-apps", chatApp });
            const fromRevealedList =
                chatApp !== undefined && current.page === "chat-apps" && !current.chatApp && current.reveal > 0;
            return fromRevealedList ? { ...next, reveal: current.reveal } : next;
        });
    }, []);

    // A link that asks for Advanced, or names an anchor, lands on it.
    const contentRef = useRef<HTMLDivElement>(null);
    useSettingsAnchorScroll(
        contentRef,
        destination.anchor ?? (destination.reveal > 0 ? SETTINGS_ADVANCED_ANCHOR : undefined),
        destination,
    );

    const pageEntry = isSettingsPageId(activePage) ? getSettingsPage(activePage) : null;
    const title = pageEntry
        ? (t((pageEntry.titleKey ?? pageEntry.labelKey) as never) as string)
        : (t(`topics.${activePage}` as never) as string);
    const subtitle = pageEntry ? (t(pageEntry.subtitleKey as never) as string) : null;
    // Agent of You sets its own title and breadcrumb once Bo approves (v2.1.3 §5).
    const aoyStore = useAoyHeader();
    const aoyHeader = activePage === "your-agent" ? aoyStore : null;

    // Escape key closes settings. The dialog content is retained after the
    // first open, so this listener must exist only while the dialog shows —
    // otherwise a hidden Settings would swallow Escape presses app-wide.
    useEffect(() => {
        if (!active) return;
        const handleKeyDown = (e: KeyboardEvent) => {
            if (e.key === "Escape") {
                // A layered surface (Combobox popover, etc.) that already
                // consumed this Escape marks it defaultPrevented — don't
                // close the whole settings dialog on the same keypress.
                if (e.defaultPrevented) return;
                e.preventDefault();
                (document.activeElement as HTMLElement)?.blur();
                onClose();
            }
        };
        window.addEventListener("keydown", handleKeyDown);
        return () => window.removeEventListener("keydown", handleKeyDown);
    }, [onClose, active]);

    const renderPage = () => {
        switch (activePage) {
            case "notifications":
                return <NotificationsPage onOpenPage={selectPage} />;
            case "ai-provider":
                return <AssistantPage />;
            case "activity":
                return <ObservationPage onOpenPage={selectPage} anchor={destination.anchor} anchorKey={destination} onOpenPeople={onOpenPeople} />;
            case "closed-eyes":
                return <ClosedEyesPage />;
            case "your-data":
                return <YourDataPage onOpenPage={selectPage} />;
            case "iphone":
                return <IPhonePage />;
            case "your-watch":
                return <YourWatchPage />;
            case "your-agent":
                return <AgentOfYouPage />;
            case "plugins":
                return <PluginsPage />;
            case "mcp":
                return <ToolServersPage />;
            case "chat-apps":
                return <ChatAppsPage selectedApp={destination.chatApp} onSelectApp={selectChatApp} />;
            case "use-yansu-anywhere":
                return <OtherAIAppsPage />;
            case "about":
                return <HelpAboutPage onReplayOnboarding={onReplayOnboarding} />;
            // ── Developer tools ──
            case "triage":
                return <TriageLogSettings />;
            case "activity-pipeline":
                return <ActivityPipelineSettings onJumpToActivity={onJumpToActivity} />;
            case "memory-debug":
                return <MemoryDebugPage />;
            case "purpose-map":
                return <PurposeMapDebugSettings />;
            case "perf-analysis":
                return <PerfAnalysisSettings />;
            case "audio-capture":
                return <AudioCaptureSettings />;
            case "ax-parser":
                return <AXParserSettings />;
            case "notification-tests":
                return <NotificationTestSettings />;
            case "browser-bridge":
                return <BrowserBridgeSettings />;
        }
    };

    return (
        <div className="settings-surface h-full flex flex-col">
            <div className="flex-1 min-h-0 flex flex-row">
                <SettingsNav
                    activePage={activePage}
                    onSelect={selectPage}
                    showDevTools={showDevTools}
                />

                {/* ── Right content area ── */}
                <div className="flex-1 min-w-0 flex flex-col">
                    {/* Page header: title plus one sentence on what the page is for */}
                    <div className="window-drag w-full max-w-[760px] mx-auto px-4 pr-12 pt-5 pb-5 shrink-0 flex flex-col gap-1 md:px-10 md:pr-12">
                        {aoyHeader ? (
                            <>
                                {aoyHeader.crumbs.length > 0 && (
                                    <nav className="flex items-center gap-1.5 text-sm text-muted" data-testid="aoy-crumbs">
                                        {aoyHeader.crumbs.map((crumb) => (
                                            <span key={crumb.label} className="flex items-center gap-1.5">
                                                <button type="button" className="text-accent hover:underline underline-offset-2" onClick={crumb.onClick}>{crumb.label}</button>
                                                <ChevronRight className="h-3.5 w-3.5" aria-hidden />
                                            </span>
                                        ))}
                                        <span className="truncate">{aoyHeader.title}</span>
                                    </nav>
                                )}
                                <div className="flex items-center justify-between gap-3">
                                    <h1 className="settings-page-title">{aoyHeader.title}</h1>
                                    {aoyHeader.trailing}
                                </div>
                                {aoyHeader.lead && <p className="settings-page-sub">{aoyHeader.lead}</p>}
                            </>
                        ) : (
                            <>
                                <h1 className="settings-page-title">{title}</h1>
                                {subtitle && <p className="settings-page-sub">{subtitle}</p>}
                            </>
                        )}
                    </div>

                    {/* Settings content — scrollable */}
                    <ScrollArea key={`${activePage}:${destination.chatApp ?? ""}`} className="flex-1">
                        <div ref={contentRef} className="w-full max-w-[760px] mx-auto px-4 pb-14 space-y-6 md:px-10">
                            <SettingsDevToolsContext.Provider value={showDevTools}>
                            <SettingsAdvancedRevealContext.Provider value={destination.reveal}>
                                <SettingsPageBoundary key={`${activePage}:${destination.chatApp ?? ""}`}>
                                    {renderPage()}
                                </SettingsPageBoundary>
                            </SettingsAdvancedRevealContext.Provider>
                            </SettingsDevToolsContext.Provider>
                        </div>
                    </ScrollArea>
                </div>
            </div>
        </div>
    );
}

function humanizeTechnicalValue(value: string): string {
	const label = value.replaceAll("_", " ").trim();
	return label ? label.charAt(0).toUpperCase() + label.slice(1) : "—";
}

// ── Browser bridge settings (dev tools) ──

function BrowserBridgeSettings() {
	const { t } = useTranslation('settings');
    const [enabled, setEnabledState] = useState(false);
    const [loading, setLoading] = useState(true);

    useEffect(() => {
        GetBrowserBridgeEnabled()
            .then((v) => {
                setEnabledState(v);
                setLoading(false);
            })
            .catch(() => setLoading(false));
    }, []);

    const handleToggle = () => {
        const next = !enabled;
        setEnabledState(next);
        SetBrowserBridgeEnabled(next).catch(() => {
            setEnabledState(!next);
            toast.error(t('browserBridge.toggleFailed'));
        });
    };

    return (
        <SettingsCard
            title={t('browserBridge.cardTitle')}
            description={t('browserBridge.cardDescription')}
        >
            <SettingsRow
                title={t('browserBridge.rowTitle')}
                description={t('browserBridge.rowDescription')}
                control={
                    <Switch
                        checked={enabled}
                        disabled={loading}
                        onCheckedChange={handleToggle}
                    />
                }
            />
            <p className="mt-2 text-xs leading-relaxed text-warning">
                {t('browserBridge.warning')}
            </p>
        </SettingsCard>
    );
}

// ── AX Parser Probe (DevTools) ──
// Lets developers validate a new app parser by capturing the current
// foreground app's accessibility tree on demand instead of waiting for
// the activity pipeline. Backed by ProbeAXParser / ListAXParserCatalog
// / ListAXWindowsByBundle in activity_monitor.go. Supports a "by bundle
// ID" mode that bypasses NSWorkspace.frontmostApplication so the probe
// works on multi-monitor setups where Universal Control hijacks the
// frontmost signal mid-cursor-cross.

type AXProbeMode = "frontmost" | "bundle";

type AXWindowEntry = {
    index: number;
    role?: string;
    title?: string;
    treeBytes?: number;
};

function parseAXWindowList(raw: string): AXWindowEntry[] {
    if (!raw) return [];
    try {
        const parsed = JSON.parse(raw);
        if (!Array.isArray(parsed)) return [];
        return parsed.map((w, i) => ({
            index: typeof w?.index === "number" ? w.index : i,
            role: typeof w?.role === "string" ? w.role : undefined,
            title: typeof w?.title === "string" ? w.title : undefined,
            treeBytes: typeof w?.treeBytes === "number" ? w.treeBytes : undefined,
        }));
    } catch {
        return [];
    }
}

function AXParserSettings() {
    const { t } = useTranslation('settings');
    const [mode, setMode] = useState<AXProbeMode>("frontmost");
    const [bundleId, setBundleId] = useState("");
    const [delaySeconds, setDelaySeconds] = useState(3);
    const [label, setLabel] = useState("");
    const [windowIndex, setWindowIndex] = useState(-1);
    const [windows, setWindows] = useState<AXWindowEntry[]>([]);
    const [windowsLoading, setWindowsLoading] = useState(false);
    const [probing, setProbing] = useState(false);
    const [result, setResult] = useState<AXProbeResult | null>(null);
    const [includeOCR, setIncludeOCR] = useState(false);
    const [upscaleOCR, setUpscaleOCR] = useState(false);
    const [forceFusedPreview, setForceFusedPreview] = useState(false);
    const [catalog, setCatalog] = useState<AXParserCatalogEntry[]>([]);
    const [catalogError, setCatalogError] = useState<string | null>(null);
    const [catalogOpen, setCatalogOpen] = useState(false);
    const [unsupported, setUnsupported] = useState<AXUnsupportedApp[]>([]);
    const [unsupportedError, setUnsupportedError] = useState<string | null>(null);
    const [unsupportedOpen, setUnsupportedOpen] = useState(false);

    useEffect(() => {
        ListAXParserCatalog()
            .then((entries) => {
                setCatalog(entries ?? []);
                setCatalogError(null);
            })
            .catch((err: unknown) => {
                setCatalog([]);
                setCatalogError(err instanceof Error ? err.message : String(err));
            });
        ListAXParserUnsupportedApps()
            .then((entries) => {
                setUnsupported(entries ?? []);
                setUnsupportedError(null);
            })
            .catch((err: unknown) => {
                setUnsupported([]);
                setUnsupportedError(err instanceof Error ? err.message : String(err));
            });
    }, []);

    const handleListWindows = useCallback(async () => {
        const trimmed = bundleId.trim();
        if (!trimmed) return;
        setWindowsLoading(true);
        try {
            const raw = await ListAXWindowsByBundle(trimmed);
            const list = parseAXWindowList(raw);
            setWindows(list);
            setWindowIndex(-1);
            if (list.length === 0) {
                toast(t('axParser.noWindowsFound'));
            }
        } catch (err) {
            toast.error(t('axParser.probeFailedToast', {
                error: err instanceof Error ? err.message : String(err),
            }));
        } finally {
            setWindowsLoading(false);
        }
    }, [bundleId, t]);

    const handleProbe = useCallback(async () => {
        setProbing(true);
        try {
            const opts: AXProbeOptions = {
                bundleId: mode === "bundle" ? bundleId.trim() : "",
                windowIndex: mode === "bundle" ? windowIndex : -1,
                delaySeconds,
                includeTree: false,
                includeOCR,
                upscaleOCR: includeOCR && upscaleOCR,
                label: label.trim(),
            };
            const r = await ProbeAXParser(opts);
            setResult(r);
        } catch (err) {
            toast.error(t('axParser.probeFailedToast', {
                error: err instanceof Error ? err.message : String(err),
            }));
        } finally {
            setProbing(false);
        }
    }, [mode, bundleId, delaySeconds, label, windowIndex, includeOCR, upscaleOCR, t]);

    const handleCopyJson = useCallback(async (value: unknown) => {
        const text = typeof value === "string" ? value : JSON.stringify(value, null, 2);
        try {
            await navigator.clipboard.writeText(text);
            toast.success(t('axParser.copiedToast'));
        } catch {
            // Fall back to the Wails clipboard binding if the web API is
            // blocked (file:// origin, missing permission). NativeClipboard
            // is best-effort — silent on failure.
            try {
                await NativeClipboardSetText(text);
                toast.success(t('axParser.copiedToast'));
            } catch {
                /* noop */
            }
        }
    }, [t]);

    const handleOpenDump = useCallback((path?: string) => {
        if (!path) return;
        OpenExternalURL(`file://${path}`).catch(() => {
            /* noop — user can copy the path from the UI */
        });
    }, []);

    const showBundleControls = mode === "bundle";
    const probeDisabled =
        probing ||
        (mode === "bundle" && bundleId.trim() === "");

    const formatBytes = (n: number | undefined): string => {
        if (!n || n <= 0) return "0";
        if (n < 1024) return `${n} B`;
        if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
        return `${(n / (1024 * 1024)).toFixed(2)} MB`;
    };

    return (
        <>
            <SettingsCard
                title={t('axParser.cardTitle')}
                description={t('axParser.cardDescription')}
            >
                <div className="flex flex-col gap-4">
                    {/* Mode selector */}
                    <div className="settings-segment" role="radiogroup" aria-label={t('axParser.modeLabel')}>
                        {(["frontmost", "bundle"] as const).map((choice) => (
                            <button
                                key={choice}
                                type="button"
                                role="radio"
                                aria-checked={mode === choice}
                                data-active={mode === choice}
                                style={mode === choice ? { backgroundColor: "var(--color-surface-active)" } : undefined}
                                onClick={() => setMode(choice)}
                            >
                                {t(choice === "frontmost" ? 'axParser.modeFrontmost' : 'axParser.modeBundle')}
                            </button>
                        ))}
                    </div>
                    <p className="text-xs text-muted">
                        {showBundleControls
                            ? t('axParser.modeBundleHint')
                            : t('axParser.modeFrontmostHint')}
                    </p>

                    {showBundleControls && (
                        <div className="flex flex-col gap-2">
                            <label className="text-xs font-medium text-muted">
                                {t('axParser.bundleIdLabel')}
                            </label>
                            <div className="flex items-center gap-2">
                                <input
                                    type="text"
                                    list="ax-parser-bundle-suggestions"
                                    value={bundleId}
                                    onChange={(e) => setBundleId(e.target.value)}
                                    placeholder={t('axParser.bundleIdPlaceholder')}
                                    className="flex-1 h-8 px-2 text-sm rounded-md border border-line bg-background"
                                />
                                <Button
                                    size="sm"
                                    variant="outline"
                                    onClick={handleListWindows}
                                    disabled={bundleId.trim() === "" || windowsLoading}
                                >
                                    {windowsLoading && (
                                        <Loader2 className="h-3.5 w-3.5 animate-spin" />
                                    )}
                                    {t('axParser.listWindowsButton')}
                                </Button>
                            </div>
                            <datalist id="ax-parser-bundle-suggestions">
                                {catalog.flatMap((entry) =>
                                    (entry.bundles ?? []).map((b) => (
                                        <option key={`${entry.name}-${b}`} value={b}>
                                            {entry.label}
                                        </option>
                                    )),
                                )}
                            </datalist>
                        </div>
                    )}

                    {showBundleControls && windows.length > 0 && (
                        <div className="flex flex-col gap-2">
                            <div className="flex items-center justify-between">
                                <span className="text-xs font-medium text-muted">
                                    {t('axParser.windowsTableTitle')}
                                </span>
                                <Button
                                    size="sm"
                                    variant="ghost"
                                    onClick={() => setWindowIndex(-1)}
                                >
                                    {t('axParser.windowsUseDefault')}
                                </Button>
                            </div>
                            <p className="text-xs text-muted">{t('axParser.windowsTableHint')}</p>
                            <div className="rounded-md border border-line overflow-hidden">
                                <table className="w-full text-xs">
                                    <thead className="bg-ink/[0.04]">
                                        <tr>
                                            <th className="text-left px-2 py-1.5 font-medium">
                                                {t('axParser.windowsColIndex')}
                                            </th>
                                            <th className="text-left px-2 py-1.5 font-medium">
                                                {t('axParser.windowsColRole')}
                                            </th>
                                            <th className="text-left px-2 py-1.5 font-medium">
                                                {t('axParser.windowsColTitle')}
                                            </th>
                                            <th className="text-right px-2 py-1.5 font-medium">
                                                {t('axParser.windowsColBytes')}
                                            </th>
                                        </tr>
                                    </thead>
                                    <tbody>
                                        {windows.map((w) => {
                                            const selected = windowIndex === w.index;
                                            return (
                                                <tr
                                                    key={w.index}
                                                    onClick={() => setWindowIndex(w.index)}
                                                    className={`cursor-pointer transition-colors ${
                                                        selected
                                                            ? "bg-surface-active text-ink"
                                                            : "hover:bg-ink/[0.04]"
                                                    }`}
                                                >
                                                    <td className="px-2 py-1.5">{w.index}</td>
                                                    <td className="px-2 py-1.5">{w.role ?? ""}</td>
                                                    <td className="px-2 py-1.5 truncate max-w-[24rem]">
                                                        {w.title ?? ""}
                                                    </td>
                                                    <td className="px-2 py-1.5 text-right tabular-nums">
                                                        {formatBytes(w.treeBytes)}
                                                    </td>
                                                </tr>
                                            );
                                        })}
                                    </tbody>
                                </table>
                            </div>
                        </div>
                    )}

                    <div className="flex flex-col gap-2">
                        <label className="text-xs font-medium text-muted">
                            {t('axParser.delayLabel')}
                        </label>
                        <input
                            type="number"
                            min={0}
                            max={60}
                            value={delaySeconds}
                            onChange={(e) =>
                                setDelaySeconds(Math.max(0, Math.min(60, Number(e.target.value) || 0)))
                            }
                            className="w-24 h-8 px-2 text-sm rounded-md border border-line bg-background"
                        />
                        <p className="text-xs text-muted">{t('axParser.delayHint')}</p>
                    </div>

                    <div className="flex flex-col gap-2">
                        <label className="text-xs font-medium text-muted">
                            {t('axParser.labelLabel')}
                        </label>
                        <input
                            type="text"
                            value={label}
                            onChange={(e) => setLabel(e.target.value)}
                            placeholder={t('axParser.labelPlaceholder')}
                            className="h-8 px-2 text-sm rounded-md border border-line bg-background"
                        />
                        <p className="text-xs text-muted">{t('axParser.labelHint')}</p>
                    </div>

                    <SettingsRow
                        title={t('axParser.includeOCRLabel')}
                        description={t('axParser.includeOCRDescription')}
                        control={
                            <Switch
                                checked={includeOCR}
                                onCheckedChange={setIncludeOCR}
                            />
                        }
                    />

                    {includeOCR && (
                        <SettingsRow
                            title={t('axParser.upscaleOCRLabel')}
                            description={t('axParser.upscaleOCRDescription')}
                            control={
                                <Switch
                                    checked={upscaleOCR}
                                    onCheckedChange={setUpscaleOCR}
                                />
                            }
                        />
                    )}

                    <div className="flex justify-end items-center gap-2">
                        {/*
                         * Spinner lives outside the Button so the button's own
                         * width / opacity transition doesn't fire together with
                         * a content reflow. WebKit (Wails webview) left a paint
                         * ghost when both happened in the same frame —
                         * disabled:opacity-50 + extra inline child shifted the
                         * left edge while transition-all animated opacity, and
                         * the old-position layer composed under the new one as
                         * a grey rounded rectangle. Keeping the button at a
                         * stable size sidesteps that.
                         */}
                        {probing && (
                            <Loader2
                                className="h-3.5 w-3.5 animate-spin text-muted"
                                aria-hidden
                            />
                        )}
                        <Button
                            size="sm"
                            onClick={handleProbe}
                            disabled={probeDisabled}
                        >
                            {t('axParser.probeButton')}
                        </Button>
                    </div>

                    {result && (
                        <div className="flex flex-col gap-3 text-sm border-t border-line pt-4">
                        <div className="grid grid-cols-2 gap-x-4 gap-y-1.5 text-xs">
                            <span className="text-muted">{t('axParser.matchedAppLabel')}</span>
                            <span className="font-medium">
                                {result.dispatch.matchedApp || t('axParser.noMatchedApp')}
                            </span>
                            <span className="text-muted">{t('axParser.contentTypeLabel')}</span>
                            <span className="font-mono">
                                {result.dispatch.contentType || "—"}
                            </span>
                            <span className="text-muted">{t('axParser.messagesCountLabel')}</span>
                            <span className="tabular-nums">{result.dispatch.messagesCount}</span>
                            {result.dispatch.title && (
                                <>
                                    <span className="text-muted">{t('axParser.titleLabel')}</span>
                                    <span className="truncate">{result.dispatch.title}</span>
                                </>
                            )}
                            {result.dispatch.channel && (
                                <>
                                    <span className="text-muted">{t('axParser.channelLabel')}</span>
                                    <span className="truncate">{result.dispatch.channel}</span>
                                </>
                            )}
                            <span className="text-muted">{t('axParser.appNameLabel')}</span>
                            <span className="truncate">{result.appName || result.bundleId}</span>
                            <span className="text-muted">{t('axParser.windowTitleLabel')}</span>
                            <span className="truncate">{result.windowTitle || "—"}</span>
                            {result.browserURL && (
                                <>
                                    <span className="text-muted">{t('axParser.browserURLLabel')}</span>
                                    <span className="font-mono truncate">{result.browserURL}</span>
                                </>
                            )}
                            <span className="text-muted">{t('axParser.axTreeBytesLabel')}</span>
                            <span className="tabular-nums">{formatBytes(result.axTreeBytes)}</span>
                            {result.parsed?.metadata?.axCoverage && (
                                <>
                                    <span className="text-muted">{t('axParser.axCoverageLabel')}</span>
                                    <span className="font-mono">{result.parsed.metadata.axCoverage}</span>
                                </>
                            )}
                            {result.dumpPath && (
                                <>
                                    <span className="text-muted">{t('axParser.dumpPathLabel')}</span>
                                    <span className="flex items-center gap-2 min-w-0">
                                        <span className="font-mono text-xs truncate">{result.dumpPath}</span>
                                        <button
                                            type="button"
                                            onClick={() => handleCopyJson(result.dumpPath)}
                                            className="shrink-0 text-accent hover:underline text-xs"
                                        >
                                            {t('axParser.dumpCopyButton')}
                                        </button>
                                        <button
                                            type="button"
                                            onClick={() => handleOpenDump(result.dumpPath)}
                                            className="shrink-0 text-accent hover:underline text-xs"
                                        >
                                            {t('axParser.dumpOpenButton', { fileManager: getFileManagerName(i18n.language) })}
                                        </button>
                                    </span>
                                </>
                            )}
                        </div>

                        {result.parsed?.body && (
                            <div className="flex flex-col gap-1">
                                <div className="flex items-center justify-between">
                                    <span className="text-xs font-medium text-muted">
                                        {t('axParser.bodyLabel')}
                                    </span>
                                    <button
                                        type="button"
                                        onClick={() => handleCopyJson(result.parsed?.body)}
                                        className="text-xs text-accent hover:underline"
                                    >
                                        {t('axParser.copyJsonButton')}
                                    </button>
                                </div>
                                <pre className="text-xs whitespace-pre-wrap font-mono rounded-md border border-line bg-ink/[0.03] p-2 max-h-64 overflow-auto">
                                    {result.parsed.body}
                                </pre>
                            </div>
                        )}

                        {(result.ocrText || result.ocrError) && (
                            <div className="flex flex-col gap-1">
                                <div className="flex items-center justify-between">
                                    <span className="text-xs font-medium text-muted">
                                        {t('axParser.ocrTextLabel', {
                                            ms: result.ocrDurationMs ?? 0,
                                        })}
                                    </span>
                                    {result.ocrText && (
                                        <button
                                            type="button"
                                            onClick={() => handleCopyJson(result.ocrText)}
                                            className="text-xs text-accent hover:underline"
                                        >
                                            {t('axParser.copyJsonButton')}
                                        </button>
                                    )}
                                </div>
                                {result.ocrError ? (
                                    <p className="text-xs text-error">{result.ocrError}</p>
                                ) : (
                                    <pre className="text-xs whitespace-pre-wrap font-mono rounded-md border border-line bg-ink/[0.03] p-2 max-h-64 overflow-auto">
                                        {result.ocrText}
                                    </pre>
                                )}
                            </div>
                        )}

                        {(result.fusedBody || result.fusedReason) && (() => {
                            const previewAvailable = !!result.fusedWithOCRPreview;
                            const showingPreview = forceFusedPreview && previewAvailable;
                            const displayedBody = showingPreview
                                ? result.fusedWithOCRPreview!
                                : (result.fusedBody ?? "");
                            return (
                                <div className="flex flex-col gap-1">
                                    <div className="flex items-center justify-between">
                                        <span className="text-xs font-medium text-muted">
                                            {t('axParser.fusedBodyLabel')}
                                        </span>
                                        <button
                                            type="button"
                                            onClick={() => handleCopyJson(displayedBody)}
                                            className="text-xs text-accent hover:underline"
                                        >
                                            {t('axParser.copyJsonButton')}
                                        </button>
                                    </div>
                                    {result.fusedReason && (
                                        <p className="text-xs text-muted">
                                            <span className={`inline-block px-1.5 py-0.5 mr-1.5 rounded text-[10px] font-medium ${
                                                result.fusedSpliced
                                                    ? "bg-success-bg text-success"
                                                    : "bg-ink/[0.06] text-muted"
                                            }`}>
                                                {result.fusedSpliced
                                                    ? t('axParser.fusedSplicedTag')
                                                    : t('axParser.fusedNotSplicedTag')}
                                            </span>
                                            {result.fusedReason}
                                        </p>
                                    )}
                                    {previewAvailable && (
                                        <label className="flex items-center gap-2 text-xs text-muted cursor-pointer mt-1">
                                            <input
                                                type="checkbox"
                                                checked={forceFusedPreview}
                                                onChange={(e) => setForceFusedPreview(e.target.checked)}
                                                className="cursor-pointer"
                                            />
                                            {t('axParser.fusedForcePreviewLabel')}
                                        </label>
                                    )}
                                    {displayedBody && (
                                        <pre className="text-xs whitespace-pre-wrap font-mono rounded-md border border-line bg-ink/[0.03] p-2 max-h-64 overflow-auto">
                                            {displayedBody}
                                        </pre>
                                    )}
                                </div>
                            );
                        })()}

                        {result.parsed?.messages && result.parsed.messages.length > 0 && (
                            <div className="flex flex-col gap-1">
                                <span className="text-xs font-medium text-muted">
                                    {t('axParser.messagesLabel')}
                                </span>
                                <div className="rounded-md border border-line max-h-64 overflow-auto">
                                    {result.parsed.messages.map((m, i) => (
                                        <div
                                            key={i}
                                            className="px-2 py-1.5 text-xs border-b border-line last:border-b-0"
                                        >
                                            <div className="flex items-center justify-between text-muted">
                                                <span className="font-medium">{m.sender || "—"}</span>
                                                {m.timestamp && (
                                                    <span className="font-mono">{m.timestamp}</span>
                                                )}
                                            </div>
                                            <p className="whitespace-pre-wrap">{m.content}</p>
                                        </div>
                                    ))}
                                </div>
                            </div>
                        )}

                        </div>
                    )}
                </div>
            </SettingsCard>

            <SettingsCard
                title={t('axParser.catalogSectionTitle')}
                description={t('axParser.catalogDescription')}
            >
                {catalogError ? (
                    <p className="text-xs text-error">{t('axParser.catalogLoadFailed')}</p>
                ) : (
                    <Collapsible open={catalogOpen} onOpenChange={setCatalogOpen}>
                        <CollapsibleTrigger className="text-xs text-accent hover:underline self-start flex items-center gap-1">
                            <ChevronDown
                                className={`h-3 w-3 transition-transform ${
                                    catalogOpen ? "rotate-180" : ""
                                }`}
                            />
                            {catalogOpen
                                ? t('axParser.catalogHide', { count: catalog.length })
                                : t('axParser.catalogShow', { count: catalog.length })}
                        </CollapsibleTrigger>
                        <CollapsibleContent className="mt-2 flex flex-col gap-4">
                            {(() => {
                                const nativeEntries = catalog.filter(
                                    (e) => e.bundles && e.bundles.length > 0,
                                );
                                const webEntries = catalog.filter(
                                    (e) => e.urlHosts && e.urlHosts.length > 0,
                                );
                                return (
                                    <>
                                        <div className="flex flex-col gap-1">
                                            <span className="text-xs font-medium text-muted">
                                                {t('axParser.catalogNativeTitle', {
                                                    count: nativeEntries.length,
                                                })}
                                            </span>
                                            <div className="rounded-md border border-line overflow-hidden">
                                                <table className="w-full text-xs">
                                                    <thead className="bg-ink/[0.04]">
                                                        <tr>
                                                            <th className="text-left px-2 py-1.5 font-medium">
                                                                {t('axParser.catalogColParser')}
                                                            </th>
                                                            <th className="text-left px-2 py-1.5 font-medium">
                                                                {t('axParser.catalogColContent')}
                                                            </th>
                                                            <th className="text-left px-2 py-1.5 font-medium">
                                                                {t('axParser.catalogColBundles')}
                                                            </th>
                                                        </tr>
                                                    </thead>
                                                    <tbody>
                                                        {nativeEntries.map((entry) => (
                                                            <tr key={entry.name} className="border-t border-line align-top">
                                                                <td className="px-2 py-1.5">
                                                                    <div className="font-medium">{entry.label}</div>
                                                                    {entry.notes && (
                                                                        <div className="text-[10px] text-muted mt-0.5 leading-snug whitespace-normal">
                                                                            {entry.notes}
                                                                        </div>
                                                                    )}
                                                                </td>
                                                                <td className="px-2 py-1.5 font-mono text-muted">
                                                                    {entry.contentType || t('axParser.catalogEmpty')}
                                                                </td>
                                                                <td className="px-2 py-1.5 font-mono text-muted">
                                                                    {entry.bundles!.join(", ")}
                                                                </td>
                                                            </tr>
                                                        ))}
                                                    </tbody>
                                                </table>
                                            </div>
                                        </div>

                                        <div className="flex flex-col gap-1">
                                            <span className="text-xs font-medium text-muted">
                                                {t('axParser.catalogWebTitle', {
                                                    count: webEntries.length,
                                                })}
                                            </span>
                                            <div className="rounded-md border border-line overflow-hidden">
                                                <table className="w-full text-xs">
                                                    <thead className="bg-ink/[0.04]">
                                                        <tr>
                                                            <th className="text-left px-2 py-1.5 font-medium">
                                                                {t('axParser.catalogColParser')}
                                                            </th>
                                                            <th className="text-left px-2 py-1.5 font-medium">
                                                                {t('axParser.catalogColContent')}
                                                            </th>
                                                            <th className="text-left px-2 py-1.5 font-medium">
                                                                {t('axParser.catalogColURLs')}
                                                            </th>
                                                        </tr>
                                                    </thead>
                                                    <tbody>
                                                        {webEntries.map((entry) => (
                                                            <tr key={entry.name} className="border-t border-line align-top">
                                                                <td className="px-2 py-1.5">
                                                                    <div className="font-medium">{entry.label}</div>
                                                                    {entry.notes && (
                                                                        <div className="text-[10px] text-muted mt-0.5 leading-snug whitespace-normal">
                                                                            {entry.notes}
                                                                        </div>
                                                                    )}
                                                                </td>
                                                                <td className="px-2 py-1.5 font-mono text-muted">
                                                                    {entry.contentType || t('axParser.catalogEmpty')}
                                                                </td>
                                                                <td className="px-2 py-1.5 font-mono text-muted">
                                                                    {entry.urlHosts!.join(", ")}
                                                                </td>
                                                            </tr>
                                                        ))}
                                                    </tbody>
                                                </table>
                                            </div>
                                        </div>
                                    </>
                                );
                            })()}
                        </CollapsibleContent>
                    </Collapsible>
                )}
            </SettingsCard>

            <SettingsCard
                title={t('axParser.unsupportedSectionTitle')}
                description={t('axParser.unsupportedDescription')}
            >
                {unsupportedError ? (
                    <p className="text-xs text-error">
                        {t('axParser.unsupportedLoadFailed')}
                    </p>
                ) : unsupported.length === 0 ? (
                    <p className="text-xs text-muted">
                        {t('axParser.unsupportedEmpty')}
                    </p>
                ) : (
                    <Collapsible open={unsupportedOpen} onOpenChange={setUnsupportedOpen}>
                        <CollapsibleTrigger className="text-xs text-accent hover:underline self-start flex items-center gap-1">
                            <ChevronDown
                                className={`h-3 w-3 transition-transform ${
                                    unsupportedOpen ? "rotate-180" : ""
                                }`}
                            />
                            {unsupportedOpen
                                ? t('axParser.unsupportedHide')
                                : t('axParser.unsupportedShow', { count: unsupported.length })}
                        </CollapsibleTrigger>
                        <CollapsibleContent className="mt-2">
                            <div className="rounded-md border border-line overflow-hidden">
                                <table className="w-full text-xs">
                                    <thead className="bg-ink/[0.04]">
                                        <tr>
                                            <th className="text-left px-2 py-1.5 font-medium">
                                                {t('axParser.unsupportedColApp')}
                                            </th>
                                            <th className="text-left px-2 py-1.5 font-medium">
                                                {t('axParser.unsupportedColReason')}
                                            </th>
                                            <th className="text-left px-2 py-1.5 font-medium whitespace-nowrap">
                                                {t('axParser.unsupportedColProbedAt')}
                                            </th>
                                        </tr>
                                    </thead>
                                    <tbody>
                                        {unsupported.map((entry) => (
                                            <tr
                                                key={entry.bundles.join(",") || entry.label}
                                                className="border-t border-line align-top"
                                            >
                                                <td className="px-2 py-1.5">
                                                    <div className="font-medium">{entry.label}</div>
                                                    <div className="text-[10px] text-muted mt-0.5 font-mono">
                                                        {entry.bundles.join(", ")}
                                                    </div>
                                                </td>
                                                <td className="px-2 py-1.5 text-muted leading-snug whitespace-normal">
                                                    {entry.reason}
                                                </td>
                                                <td className="px-2 py-1.5 font-mono text-muted whitespace-nowrap">
                                                    {entry.probedAt}
                                                </td>
                                            </tr>
                                        ))}
                                    </tbody>
                                </table>
                            </div>
                        </CollapsibleContent>
                    </Collapsible>
                )}
            </SettingsCard>
        </>
    );
}

// ── Triage Log Settings ──

const TRIAGE_DECISION_COLORS: Record<string, string> = {
    skip: 'text-muted',
    skipped: 'text-muted',
    dismiss: 'text-warning',
    filter: 'text-muted',
    worth: 'text-success',
    not_worth: 'text-error',
    triage: 'text-info',
    saved: 'text-success',
    dedup_update: 'text-success',
    dedup_skip: 'text-muted',
    correction_match_skip: 'text-warning',
    subject_gate_coerce: 'text-warning',
    subject_gate_skip: 'text-muted',
    attribution_coerce: 'text-warning',
    attribution_skip: 'text-muted',
    error: 'text-error',
};

const TRIAGE_DECISION_BG: Record<string, string> = {
    skip: 'bg-muted/15',
    skipped: 'bg-muted/15',
    dismiss: 'bg-warning-bg',
    filter: 'bg-muted/15',
    worth: 'bg-success-bg',
    not_worth: 'bg-error-bg',
    triage: 'bg-info/15',
    saved: 'bg-success-bg',
    dedup_update: 'bg-success-bg',
    dedup_skip: 'bg-muted/15',
    correction_match_skip: 'bg-warning-bg',
    subject_gate_coerce: 'bg-warning-bg',
    subject_gate_skip: 'bg-muted/15',
    attribution_coerce: 'bg-warning-bg',
    attribution_skip: 'bg-muted/15',
    error: 'bg-error-bg',
};

const TRIAGE_SOURCE_BADGE: Record<string, string> = {
    activity: 'bg-info/15 text-info',
    slack: 'bg-warning/15 text-warning',
};

const TRIAGE_DECISION_KEYS = [
    'correction_match_skip',
    'subject_gate_coerce',
    'subject_gate_skip',
    'attribution_coerce',
    'attribution_skip',
    'dedup_update',
    'dedup_skip',
    'not_worth',
    'skipped',
    'dismiss',
    'filter',
    'triage',
    'saved',
    'error',
    'worth',
    'skip',
] as const;

function getTriageDecisionStyle(decision: string): { color: string; bg: string } {
    for (const key of TRIAGE_DECISION_KEYS) {
        if (decision.includes(key)) {
            return { color: TRIAGE_DECISION_COLORS[key], bg: TRIAGE_DECISION_BG[key] };
        }
    }
    return { color: 'text-muted', bg: 'bg-muted/10' };
}

function getTriageDecisionKey(decision: string): string | null {
    const exact = TRIAGE_DECISION_KEYS.find((key) => decision === key);
    return exact ?? TRIAGE_DECISION_KEYS.find((key) => decision.includes(key)) ?? null;
}

type TriageReasonPresentation = {
    summary: string;
    title?: string;
    description?: string;
    points?: number;
};

// Optional fields also accept retained entries and older generated bindings.
type TriagePanelEntry = main.TriageLogEntry & {
    title?: string;
    description?: string;
};

function extractQuotedTriageField(reason: string, field: string): string {
    const match = reason.match(
        new RegExp(`(?:^|\\s)${field}=("(?:[^"\\\\]|\\\\.)*")`),
    );
    if (!match) return '';
    try {
        return JSON.parse(match[1]) as string;
    } catch {
        return match[1].slice(1, -1);
    }
}

function getTriageReasonPresentation(
    translate: (key: never, options?: Record<string, unknown>) => unknown,
    source: string,
    reason: string,
    content?: Pick<TriagePanelEntry, 'title' | 'description'>,
): TriageReasonPresentation {
    const tooFewEvents = reason.match(/^too few events:\s*(\d+)\s*<\s*(\d+)$/i);
    if (tooFewEvents) {
        return {
            summary: translate('triage.reasons.tooFewEvents' as never, {
                current: Number(tooFewEvents[1]),
                minimum: Number(tooFewEvents[2]),
            }) as string,
        };
    }
    const tooShort = reason.match(/^too short duration:\s*(.+?)\s*<\s*(.+)$/i);
    if (tooShort) {
        return {
            summary: translate('triage.reasons.tooShort' as never, {
                current: tooShort[1],
                minimum: tooShort[2],
            }) as string,
        };
    }
    const tooFewApps = reason.match(/^too few apps:\s*(\d+)\s*<\s*(\d+)/i);
    if (tooFewApps) {
        return {
            summary: translate('triage.reasons.tooFewApps' as never, {
                current: Number(tooFewApps[1]),
                minimum: Number(tooFewApps[2]),
            }) as string,
        };
    }

    // Current records keep panel content separate from the diagnostic reason.
    // Retained legacy records still carry quoted fields in that reason.
    const isSavedEntry = source === 'knowledge' || source === 'memory';
    const title = content?.title || (isSavedEntry ? '' : extractQuotedTriageField(reason, 'title'));
    const description = content?.description || (isSavedEntry ? '' : extractQuotedTriageField(reason, 'description'));
    const statedReason = isSavedEntry ? '' : extractQuotedTriageField(reason, 'reason');
    const pointsMatch = reason.match(/(?:^|\s)points=(\d+)/);
    const points = pointsMatch ? Number(pointsMatch[1]) : undefined;
    if (title || description || statedReason) {
        return {
            summary: title || statedReason || description,
            title: title || undefined,
            description: description || statedReason || undefined,
            points,
        };
    }

    if (reason === 'all triage features disabled by settings') {
        return {
            summary: translate('triage.reasons.learningDisabled' as never) as string,
        };
    }
    if (reason.startsWith('legacy activity triage cooldown:')) {
        return {
            summary: translate('triage.reasons.waitingToRetry' as never) as string,
        };
    }
    if (reason === 'AI returned no memory points') {
        return {
            summary: translate('triage.reasons.noAboutYouUpdate' as never) as string,
        };
    }
    if (/^(?:save|update|triage) failed:/i.test(reason)) {
        return {
            summary: translate('triage.reasons.learningFailed' as never, {
                source,
            }) as string,
        };
    }
    return { summary: reason };
}

type TriageSourceFilter = 'all' | 'activity' | 'slack';

function triagePanelEntryKey(entry: TriagePanelEntry): string {
    return JSON.stringify([entry.timestamp, entry.source, entry.id, entry.decision]);
}

function isTriageSavedEntry(entry: TriagePanelEntry): boolean {
    return entry.source === 'knowledge' || entry.source === 'memory';
}

function TriageLogSettings() {
    const { t } = useTranslation('settings');
    const [entries, setEntries] = useState<TriagePanelEntry[]>([]);
    const [filter, setFilter] = useState<TriageSourceFilter>('all');
    const listRef = useRef<HTMLDivElement>(null);

    const readRevision = useRef(0);
    const livePreviews = useRef(new Map<string, TriagePanelEntry>());
    const refreshLogs = useCallback((keepLivePreviews: boolean) => {
        const revision = ++readRevision.current;
        if (!keepLivePreviews) livePreviews.current.clear();
        // Drop resolved content immediately on withdrawal; a slower old read
        // must not restore a title after Forget, deletion or Closed Eyes.
        setEntries((prev) => prev.map((entry) => keepLivePreviews && !isTriageSavedEntry(entry)
            ? entry
            : { ...entry, title: undefined, description: undefined, ...(!keepLivePreviews ? { reason: '' } : {}) }));
        GetTriageLogs().then((result) => {
            if (revision === readRevision.current) {
                setEntries((result ?? []).map((entry) => !isTriageSavedEntry(entry)
                    ? livePreviews.current.get(triagePanelEntryKey(entry)) ?? entry
                    : entry));
            }
        }).catch(() => { /* Keep the neutral rows if the current read fails. */ });
    }, []);
    const loadLogs = useCallback(() => refreshLogs(false), [refreshLogs]);

    useEffect(() => {
        loadLogs();
        // Saved-entry titles use the current read binding on live updates.
        // Only gated non-artifact previews stay transient between refreshes.
        const cleanups = ['memory:updated', 'knowledge:updated', 'closed-eyes:changed']
            .map((event) => EventsOn(event, loadLogs));
        cleanups.push(EventsOn('triage:log', (entry: TriagePanelEntry) => {
            // Non-artifact previews are gated by the backend's exact debug
            // switch. Keep them transient; saved-entry titles always reload.
            if (!isTriageSavedEntry(entry)) {
                livePreviews.current.set(triagePanelEntryKey(entry), entry);
                if (livePreviews.current.size > 500) {
                    const oldest = livePreviews.current.keys().next().value;
                    if (oldest !== undefined) livePreviews.current.delete(oldest);
                }
                setEntries((prev) => [...prev, entry].slice(-500));
            }
            refreshLogs(true);
        }));
        window.addEventListener('focus', loadLogs);
        return () => {
            ++readRevision.current;
            livePreviews.current.clear();
            cleanups.forEach((cleanup) => cleanup());
            window.removeEventListener('focus', loadLogs);
        };
    }, [loadLogs, refreshLogs]);

    // Auto-scroll on new entries
    useEffect(() => {
        const list = listRef.current;
        if (list) list.scrollTop = list.scrollHeight;
    }, [entries]);

    const filtered = filter === 'all' ? entries : entries.filter((e) => e.source === filter);

    const stats: Record<string, number> = { activity: 0, slack: 0 };
    for (const e of entries) {
        if (e.source in stats) stats[e.source]++;
    }

    return (
        <>
            <div>
                <h2 className="text-sm font-semibold text-ink">
                    {t('triage.title')}
                </h2>
                <p className="mt-1 text-sm text-muted">
                    {t('triage.bodyDescription')}
                </p>
            </div>

            {/* Filter bar */}
            <div className="flex items-center gap-2">
                <ListFilter className="h-3.5 w-3.5 text-muted" />
                {(['all', 'activity', 'slack'] as TriageSourceFilter[]).map((f) => (
                    <button
                        key={f}
                        className={`px-2.5 py-1 rounded-md text-xs font-medium transition-colors ${
                            filter === f
                                ? 'bg-surface-active text-ink'
                                : 'text-muted hover:text-ink hover:bg-ink/[0.06]'
                        }`}
                        onClick={() => setFilter(f)}
                    >
                        {t(`triage.filters.${f}` as never) as string}
                        {f !== 'all' && stats[f] > 0 && (
                            <span className="ml-1 text-[10px] opacity-60">{stats[f]}</span>
                        )}
                    </button>
                ))}
                <div className="flex-1" />
                <button
                    className="p-1 rounded-md text-muted hover:text-ink hover:bg-ink/[0.06] transition-colors"
                    onClick={loadLogs}
                    title={t('triage.reload')}
                >
                    <RefreshCw className="h-3.5 w-3.5" />
                </button>
                <span className="text-xs text-muted">{t('triage.entriesCount', { count: filtered.length })}</span>
            </div>

            {/* Log entries */}
            <SettingsCard title="">
                <div ref={listRef} className="max-h-[500px] overflow-y-auto space-y-0.5 -mx-1">
                    {filtered.length === 0 && (
                        <div className="text-center text-xs text-muted py-12">
                            {t('triage.emptyState')}
                        </div>
                    )}
                    {filtered.map((entry, idx) => {
                        const style = getTriageDecisionStyle(entry.decision);
                        const decisionKey = getTriageDecisionKey(entry.decision);
                        const decisionLabel = decisionKey
                            ? t(`triage.decisions.${decisionKey}` as never) as string
                            : humanizeTechnicalValue(entry.decision);
                        const sourceKey = `triage.filters.${entry.source}`;
                        const translatedSource = t(sourceKey as never) as string;
                        const sourceLabel = translatedSource === sourceKey
                            ? humanizeTechnicalValue(entry.source)
                            : translatedSource;
                        const reasonPresentation = getTriageReasonPresentation(
                            t,
                            entry.source,
                            entry.reason,
                            entry,
                        );
                        return (
                            <details
                                key={`${entry.timestamp}-${idx}`}
                                className="group rounded-md transition-colors open:bg-ink/[0.035] hover:bg-ink/[0.06]"
                            >
                                <summary className="flex cursor-pointer list-none flex-wrap items-start gap-2 px-2 py-1.5 [&::-webkit-details-marker]:hidden">
                                    <span className="w-[80px] shrink-0 pt-0.5 font-mono text-[11px] text-muted/60">
                                        {entry.timestamp.slice(11)}
                                    </span>
                                    <span
                                        className={`shrink-0 rounded px-1.5 py-0.5 text-[10px] font-medium ${TRIAGE_SOURCE_BADGE[entry.source] ?? 'bg-muted/15 text-muted'}`}
                                        title={entry.source}
                                    >
                                        {sourceLabel}
                                    </span>
                                    <span
                                        className={`shrink-0 rounded px-1.5 py-0.5 text-[10px] font-medium ${style.bg} ${style.color}`}
                                        title={t('triage.technicalDecision', { value: entry.decision })}
                                    >
                                        {decisionLabel}
                                    </span>
                                    {(entry.worthMemory != null || entry.worthKnowledge != null || entry.worthAutomation != null) && (
                                        <span className="flex shrink-0 items-center gap-1 pt-0.5">
                                            {entry.worthMemory != null && (
                                                <span className={`rounded px-1 py-0.5 text-[9px] font-medium ${entry.worthMemory ? 'bg-accent-light text-accent' : 'bg-muted/10 text-muted/40'}`} title={t('triage.worthTooltip.memory', { value: String(entry.worthMemory) })}>
                                                    {t('triage.signals.memory')}
                                                </span>
                                            )}
                                            {entry.worthKnowledge != null && (
                                                <span className={`rounded px-1 py-0.5 text-[9px] font-medium ${entry.worthKnowledge ? 'bg-accent-light text-accent' : 'bg-muted/10 text-muted/40'}`} title={t('triage.worthTooltip.knowledge', { value: String(entry.worthKnowledge) })}>
                                                    {t('triage.signals.knowledge')}
                                                </span>
                                            )}
                                            {entry.worthAutomation != null && (
                                                <span className={`rounded px-1 py-0.5 text-[9px] font-medium ${entry.worthAutomation ? 'bg-accent-light text-accent' : 'bg-muted/10 text-muted/40'}`} title={t('triage.worthTooltip.scheduled', { value: String(entry.worthAutomation) })}>
                                                    {t('triage.signals.scheduled')}
                                                </span>
                                            )}
                                        </span>
                                    )}
                                    <span className="min-w-0 basis-full truncate pt-0.5 text-[11px] text-ink/70 transition-colors group-hover:text-ink sm:basis-0 sm:flex-1" title={reasonPresentation.summary}>
                                        {reasonPresentation.summary}
                                    </span>
                                    <ChevronDown
                                        aria-hidden="true"
                                        className="mt-0.5 h-3.5 w-3.5 shrink-0 text-muted transition-transform group-open:rotate-180"
                                    />
                                </summary>
                                <div className="mx-2 border-t border-border py-2 text-[10px]">
                                    {(reasonPresentation.description || reasonPresentation.points != null) && (
                                        <dl className="grid grid-cols-[110px_minmax(0,1fr)] gap-x-3 gap-y-1">
                                            {reasonPresentation.description && (
                                                <>
                                                    <dt className="text-muted">{t('triage.activityDescription')}</dt>
                                                    <dd className="break-words text-ink">{reasonPresentation.description}</dd>
                                                </>
                                            )}
                                            {reasonPresentation.points != null && (
                                                <>
                                                    <dt className="text-muted">{t('triage.learningUpdates')}</dt>
                                                    <dd className="text-ink">{reasonPresentation.points}</dd>
                                                </>
                                            )}
                                        </dl>
                                    )}
                                    <details className="group/technical mt-2">
                                        <summary className="cursor-pointer select-none font-medium text-muted hover:text-ink">
                                            {t('triage.technicalDetails')}
                                        </summary>
                                        <dl className="mt-2 grid grid-cols-[110px_minmax(0,1fr)] gap-x-3 gap-y-1">
                                            <dt className="text-muted">{t('triage.technicalId')}</dt>
                                            <dd className="break-all font-mono text-ink">{entry.id}</dd>
                                            <dt className="text-muted">{t('triage.technicalDecisionLabel')}</dt>
                                            <dd className="break-all font-mono text-ink">{entry.decision}</dd>
                                            <dt className="text-muted">{t('triage.technicalReasonLabel')}</dt>
                                            <dd className="break-words font-mono text-ink">{entry.reason}</dd>
                                            <dt className="text-muted">{t('triage.technicalTimestamp')}</dt>
                                            <dd className="break-all font-mono text-ink">{entry.timestamp}</dd>
                                        </dl>
                                    </details>
                                </div>
                            </details>
                        );
                    })}
                </div>
            </SettingsCard>
        </>
    );
}

// ── Activity Pipeline Settings ──

type ActivityPipelineFilter =
    | 'all'
    | 'tick'
    | 'group'
    | 'automation'
    | 'memory'
    | 'knowledge'
    | 'failed'
    | 'dismissed';

function fmtTime(ms: number): string {
    if (!ms) return '';
    const d = new Date(ms);
    const hh = String(d.getHours()).padStart(2, '0');
    const mm = String(d.getMinutes()).padStart(2, '0');
    const ss = String(d.getSeconds()).padStart(2, '0');
    return `${hh}:${mm}:${ss}`;
}

function fmtDate(ms: number): string {
    if (!ms) return '';
    const d = new Date(ms);
    const yyyy = d.getFullYear();
    const mo = String(d.getMonth() + 1).padStart(2, '0');
    const dd = String(d.getDate()).padStart(2, '0');
    return `${yyyy}-${mo}-${dd}`;
}

// Pretty-print a string as JSON when possible. LLMs often wrap JSON in
// ```json ... ``` fences, so strip those before parsing. Falls back to the
// original text when parsing fails.
function tryPrettyJSON(raw: string): string {
    const trimmed = raw.trim();
    const fenced = trimmed.match(/^```(?:json)?\s*([\s\S]*?)\s*```$/i);
    const candidate = fenced ? fenced[1] : trimmed;
    try {
        return JSON.stringify(JSON.parse(candidate), null, 2);
    } catch {
        return raw;
    }
}

function statusBadgeStyle(status?: string): string {
    if (!status) return 'bg-muted/15 text-muted';
    // created  = entry written for the first time (terminal success)
    // updated  = existing tracked entry replaced in place by the
    //            async worker writeback (terminal success)
    // dispatched = memory/knowledge handed to async worker; later
    //              replaced by created/updated/error when the worker emits
    //              its writeback event
    // shadowed = initiative persisted without execution
    // scheduled / triggered = legacy success states, kept for forward compat
    if (
        status === 'created' ||
        status === 'updated' ||
        status === 'dispatched' ||
        status === 'shadowed' ||
        status === 'scheduled' ||
        status === 'triggered'
    ) {
        return 'bg-success/15 text-success';
    }
    if (status === 'dismissed' || status === 'skipped') return 'bg-warning-bg text-warning';
    if (status === 'error') return 'bg-error-bg text-error';
    return 'bg-muted/15 text-muted';
}

function localizedTechnicalLabel(
    translate: (key: never, options?: Record<string, unknown>) => unknown,
    keyPrefix: string,
    value: string,
): string {
    const key = `${keyPrefix}.${value}`;
    const translated = translate(key as never) as string;
    return translated === key ? humanizeTechnicalValue(value) : translated;
}

interface ActivityPipelineSettingsProps {
    onJumpToActivity?: (
        sessionId: string,
        frames: {
            kind: string;
            sessionId: string;
            snapshotId: number;
            promptText?: string;
            skipReason?: string;
            ocrConfidence?: number;
            parsedSiblingCount?: number;
            browserUrl?: string;
            dedupChainLen?: number;
        }[],
        pipelineRunId: number,
        groupSessionIds: string[],
    ) => void;
}

/** parsePipelineSessionIds reads the JSON-encoded `sessionIds` blob stored on
 *  an ActivityPipelineRunRow. Backend stores the full list of session IDs in
 *  the merged group as a JSON string (NULL/empty for tick rows). Failures
 *  log + return [] — pipeline observability must never block the UI. */
function parsePipelineSessionIds(raw: string | undefined): string[] {
    if (!raw) return [];
    try {
        const parsed = JSON.parse(raw);
        if (Array.isArray(parsed)) {
            return parsed.filter((v): v is string => typeof v === 'string');
        }
        return [];
    } catch (err) {
        console.warn('[activity-pipeline] failed to parse sessionIds:', err);
        return [];
    }
}

/** Time window selectors for the cross-run Stats card. "All" keeps every
 *  row currently loaded (capped at 200 by ListActivityPipelineRuns);
 *  "24h" / "7d" cut by started_at. */
type PipelineStatsRange = 'all' | '24h' | '7d';

/** Aggregated per-window statistics computed from a list of pipeline run
 *  rows. Purely derived; never persisted. The shape mirrors the questions
 *  the operator wants to answer at-a-glance:
 *
 *    - "is the pipeline finding anything worth keeping" (winnerMix, avgs)
 *    - "is one drop reason dominating" (dropBreakdown)
 *    - "are my caps set too aggressively" (capPressure)
 *    - "is OCR quality drifting" (ocrConfidence)
 *
 * Empty-window safe: every numeric defaults to 0 / NaN-free, every map
 * to an empty Record. UI gates on totalGroups before rendering anything
 * so we never paint "0% / 0% / 0%" bars for a missing window. */
type PipelineStats = {
    /** group-kind rows in this window (skipping tick rows, which carry
     *  no selectedFrames). drives "show stats card or empty state". */
    totalGroups: number;
    /** Subset of totalGroups whose selectedFrames is non-empty. Used as
     *  the denominator for every frame-derived ratio so legacy rows
     *  (pre-selected_frames column) and partially-written groups don't
     *  drag averages toward zero or fabricate a "no drops" reading. */
    groupsWithFrames: number;
    /** totalGroups - groupsWithFrames. Surfaced in the UI as a quiet
     *  footnote so operators understand what was excluded and don't
     *  read the smaller denominator as "fewer runs happened". */
    groupsWithoutFrames: number;
    winnerMix: { parsed: number; ocr: number };
    avgWinnersPerRun: number;
    avgDropsPerRun: number;
    /** skipReason → count across the window. Sorted desc in the UI. */
    dropBreakdown: Record<string, number>;
    /** runs that hit the OCR 12-frame cap (any ocr_over_cap record) and
     *  the parsed 30 KB cap (any parsed_oversize record). Percentage UI
     *  computes against groupsWithFrames. */
    capPressure: { ocrOverCap: number; parsedOversize: number };
    /** OCR winner confidence summary. avg over all OCR winners in the
     *  window; lowConfShare is the fraction below 0.7 (the same warn
     *  threshold the per-snapshot pill uses). */
    ocrConfidence: { count: number; avg: number; lowConfShare: number };
};

/** Filter pipeline runs to a time range, count group activity, and roll
 *  up the per-frame audit data into the aggregates the Stats card needs.
 *
 *  Pure function so it's safe to recompute on every render — the data
 *  set is bounded (≤200 rows), each row's selectedFrames is already
 *  decoded JSON, and the inner loops are O(N·F) where F is small
 *  (typically <50 frames per run after dedup). No memoization needed
 *  beyond the parent's useMemo. */
function computePipelineStats(runs: main.ActivityPipelineRunRow[], range: PipelineStatsRange): PipelineStats {
    const empty: PipelineStats = {
        totalGroups: 0,
        groupsWithFrames: 0,
        groupsWithoutFrames: 0,
        winnerMix: { parsed: 0, ocr: 0 },
        avgWinnersPerRun: 0,
        avgDropsPerRun: 0,
        dropBreakdown: {},
        capPressure: { ocrOverCap: 0, parsedOversize: 0 },
        ocrConfidence: { count: 0, avg: 0, lowConfShare: 0 },
    };
    if (runs.length === 0) return empty;

    const now = Date.now();
    const cutoff = range === '24h' ? now - 24 * 60 * 60 * 1000
        : range === '7d' ? now - 7 * 24 * 60 * 60 * 1000
        : 0;
    const inWindow = runs.filter((r) =>
        r.kind === 'group'
        && (cutoff === 0 || r.startedAt >= cutoff),
    );
    if (inWindow.length === 0) return empty;

    let parsedWinners = 0;
    let ocrWinners = 0;
    let totalDrops = 0;
    let groupsWithFrames = 0;
    const dropBreakdown: Record<string, number> = {};
    let ocrOverCapRuns = 0;
    let parsedOversizeRuns = 0;
    let ocrConfSum = 0;
    let ocrConfCount = 0;
    let ocrLowConfCount = 0;

    for (const run of inWindow) {
        const frames = run.selectedFrames ?? [];
        // Legacy rows (pre-selected_frames column) and any future
        // groups whose selected_frames write was skipped contribute
        // nothing to frame-derived numerators. Excluding them from the
        // denominator too keeps averages honest — see PipelineStats doc
        // on groupsWithFrames for the rationale.
        if (frames.length === 0) continue;
        groupsWithFrames++;
        let hitOcrCap = false;
        let hitParsedOversize = false;
        for (const f of frames) {
            switch (f.kind) {
                case 'parsed':
                    parsedWinners++;
                    break;
                case 'ocr':
                    ocrWinners++;
                    if (typeof f.ocrConfidence === 'number' && f.ocrConfidence > 0) {
                        ocrConfSum += f.ocrConfidence;
                        ocrConfCount++;
                        if (f.ocrConfidence < 0.7) ocrLowConfCount++;
                    }
                    break;
                case 'parsed_skipped':
                case 'ocr_skipped': {
                    totalDrops++;
                    const reason = f.skipReason ?? 'unknown';
                    dropBreakdown[reason] = (dropBreakdown[reason] ?? 0) + 1;
                    if (reason === 'ocr_over_cap') hitOcrCap = true;
                    if (reason === 'parsed_oversize') hitParsedOversize = true;
                    break;
                }
                // parsed_sibling is folded — not a drop, not a winner.
                // Intentionally skipped so it doesn't double-count.
            }
        }
        if (hitOcrCap) ocrOverCapRuns++;
        if (hitParsedOversize) parsedOversizeRuns++;
    }

    const totalGroups = inWindow.length;
    // Defensive: if every group in the window lacks frame data (e.g. a
    // pure-legacy window), divide-by-zero would NaN every ratio. Bail
    // to the empty-shape via the safe division below; the UI will fall
    // through to the "no drops / nothing to show" branch.
    const denom = groupsWithFrames > 0 ? groupsWithFrames : 1;
    return {
        totalGroups,
        groupsWithFrames,
        groupsWithoutFrames: totalGroups - groupsWithFrames,
        winnerMix: { parsed: parsedWinners, ocr: ocrWinners },
        avgWinnersPerRun: (parsedWinners + ocrWinners) / denom,
        avgDropsPerRun: totalDrops / denom,
        dropBreakdown,
        capPressure: { ocrOverCap: ocrOverCapRuns, parsedOversize: parsedOversizeRuns },
        ocrConfidence: {
            count: ocrConfCount,
            avg: ocrConfCount > 0 ? ocrConfSum / ocrConfCount : 0,
            lowConfShare: ocrConfCount > 0 ? ocrLowConfCount / ocrConfCount : 0,
        },
    };
}

/** SkipReason values whose translation key lives under
 *  activityPipeline.stats.reasonLabel.*. Guards the dynamic key lookup
 *  so unknown reasons fall through to displaying the raw enum string. */
const KNOWN_DROP_REASONS = [
    'parsed_oversize',
    'ocr_covered_by_parsed',
    'ocr_prefix_dedup',
    'ocr_over_cap',
] as const;
type KnownDropReason = typeof KNOWN_DROP_REASONS[number];
function isKnownDropReason(reason: string): reason is KnownDropReason {
    return (KNOWN_DROP_REASONS as readonly string[]).includes(reason);
}

/** Compact stat readout used by the cross-run Stats card. `warn` darkens
 *  to amber when the figure crosses an operationally significant threshold
 *  (cap pressure > 20%, low OCR confidence) so the operator can spot
 *  trouble at a glance without reading every number. */
function StatTile({ label, value, hint, warn }: { label: string; value: string; hint?: string; warn?: boolean }) {
    return (
        <div className={`rounded border px-2 py-1.5 ${warn ? 'border-warning-border bg-warning-bg' : 'border-border bg-panel'}`}>
            <div className="text-[10px] text-muted truncate" title={label}>{label}</div>
            <div className={`text-sm font-semibold tabular-nums ${warn ? 'text-warning' : 'text-ink'}`}>{value}</div>
            {hint && <div className="text-[10px] text-muted/80 tabular-nums">{hint}</div>}
        </div>
    );
}

function ActivityPipelineSettings({ onJumpToActivity }: ActivityPipelineSettingsProps) {
    const { t } = useTranslation('settings');
    const nativeRuntime = isWailsEnvironment();
    const [runs, setRuns] = useState<main.ActivityPipelineRunRow[]>([]);
    const [filter, setFilter] = useState<ActivityPipelineFilter>('all');
    const [expandedId, setExpandedId] = useState<number | null>(null);
    const [busy, setBusy] = useState(false);
    // Both actions change data (one processes pending activity, the other
    // deletes the history), so each asks first.
    const [confirmAction, setConfirmAction] = useState<'run' | 'clear' | null>(null);
    const [statsRange, setStatsRange] = useState<PipelineStatsRange>('7d');
    const aggStats = useMemo(() => computePipelineStats(runs, statsRange), [runs, statsRange]);

    const loadRuns = useCallback(() => {
        GetActivityPipelineRuns(200).then((result) => {
            setRuns(result ?? []);
        }).catch((err) => {
            toast.error(t('activityPipeline.toasts.loadFailed', { error: String(err) }));
        });
    }, [t]);

    useEffect(() => {
        loadRuns();
    }, [loadRuns]);

    // Real-time updates: prepend new runs (backend emits with completed timestamp).
    useEffect(() => {
        const cleanup = EventsOn('activity_pipeline:run', (run: main.ActivityPipelineRunRow) => {
            setRuns((prev) => {
                // Replace if same ID already exists, else prepend.
                const idx = prev.findIndex((r) => r.id === run.id);
                if (idx >= 0) {
                    const next = [...prev];
                    next[idx] = run;
                    return next;
                }
                const next = [run, ...prev];
                if (next.length > 200) return next.slice(0, 200);
                return next;
            });
        });
        return cleanup;
    }, []);

    const handleRunNow = useCallback(async () => {
        setBusy(true);
        try {
            // RunActivityPipelineNow is synchronous on the Go side — returns
            // after evaluate() finishes. Live rows arrive via EventsOn above,
            // but we still reload to catch the tick row written in the defer.
            await RunActivityPipelineNow();
            toast.success(t('activityPipeline.toasts.triggered'));
            loadRuns();
        } catch (err) {
            const msg = err instanceof Error ? err.message : String(err);
            // Backend returns errGeneratorBusy when TryLock fails. Show a
            // distinct warning so the user knows the trigger was a no-op.
            if (msg.includes('already running')) {
                toast.warning(t('activityPipeline.toasts.alreadyRunning'));
            } else {
                toast.error(t('activityPipeline.toasts.triggerFailed', { error: msg }));
            }
        } finally {
            setBusy(false);
        }
    }, [loadRuns, t]);

    const handleClear = useCallback(async () => {
        setBusy(true);
        try {
            await ClearActivityPipelineRuns();
            setRuns([]);
            setExpandedId(null);
            toast.success(t('activityPipeline.toasts.cleared'));
        } catch (err) {
            toast.error(t('activityPipeline.toasts.clearFailed', { error: String(err) }));
        } finally {
            setBusy(false);
        }
    }, [t]);

    const matchesFilter = useCallback((r: main.ActivityPipelineRunRow): boolean => {
        switch (filter) {
            case 'all':
                return true;
            case 'tick':
                return r.kind === 'tick';
            case 'group':
                return r.kind === 'group';
            case 'automation':
                return isSuccessStatus(r.automationStatus);
            case 'memory':
                return isSuccessStatus(r.memoryStatus);
            case 'knowledge':
                return isSuccessStatus(r.knowledgeStatus);
            case 'failed':
                return Boolean(r.error)
                    || r.automationStatus === 'error'
                    || r.triageStatus === 'error'
                    || r.memoryStatus === 'error'
                    || r.knowledgeStatus === 'error';
            case 'dismissed':
                return r.triageStatus === 'dismissed' || r.prefilterPass === false;
            default:
                return true;
        }
    }, [filter]);

    const filtered = useMemo(() => runs.filter(matchesFilter), [runs, matchesFilter]);

    const stats = useMemo(() => {
        let tick = 0, group = 0, automation = 0, memory = 0, knowledge = 0, dismissed = 0, failed = 0;
        for (const r of runs) {
            if (r.kind === 'tick') tick++;
            if (r.kind === 'group') group++;
            if (isSuccessStatus(r.automationStatus)) automation++;
            if (isSuccessStatus(r.memoryStatus)) memory++;
            if (isSuccessStatus(r.knowledgeStatus)) knowledge++;
            if (r.triageStatus === 'dismissed' || r.prefilterPass === false) dismissed++;
            if (Boolean(r.error) || r.automationStatus === 'error' || r.triageStatus === 'error' || r.memoryStatus === 'error' || r.knowledgeStatus === 'error') failed++;
        }
        return { tick, group, automation, memory, knowledge, dismissed, failed };
    }, [runs]);

    const filters: { id: ActivityPipelineFilter; labelKey: string; count?: number }[] = [
        { id: 'all', labelKey: 'all' },
        { id: 'tick', labelKey: 'tick', count: stats.tick },
        { id: 'group', labelKey: 'group', count: stats.group },
        { id: 'automation', labelKey: 'scheduled', count: stats.automation },
        { id: 'memory', labelKey: 'memory', count: stats.memory },
        { id: 'knowledge', labelKey: 'knowledge', count: stats.knowledge },
        { id: 'dismissed', labelKey: 'dismissed', count: stats.dismissed },
        { id: 'failed', labelKey: 'failed', count: stats.failed },
    ];

    return (
        <>
            <div>
                <h2 className="text-sm font-semibold text-ink">
                    {t('activityPipeline.title')}
                </h2>
                <p className="mt-1 text-sm text-muted">
                    {t('activityPipeline.description')}
                </p>
            </div>
            <details className="group/technical text-[11px] text-muted/80">
                <summary className="cursor-pointer select-none font-medium text-muted hover:text-ink">
                    {t('activityPipeline.technicalStates')}
                </summary>
                <p className="mt-1 leading-5">
                    <Trans
                        i18nKey="activityPipeline.statusMeaningHtml"
                        ns="settings"
                        components={[
                            <code />,
                            <code />,
                            <code />,
                            <code />,
                            <code />,
                            <code />,
                            <code />,
                            <code />,
                            <code />,
                            <code />,
                        ]}
                    />
                </p>
            </details>

            {/* Toolbar */}
            <div className="flex items-center gap-2">
                {nativeRuntime && (
                    <Button size="sm" variant="outline" disabled={busy} onClick={() => setConfirmAction('run')}>
                        <Play className="h-3.5 w-3.5 mr-1.5" />
                        {t('activityPipeline.runNow')}
                    </Button>
                )}
                <Button size="sm" variant="outline" disabled={busy} onClick={loadRuns}>
                    <RefreshCw className="h-3.5 w-3.5 mr-1.5" />
                    {t('activityPipeline.refresh')}
                </Button>
                <div className="flex-1" />
                {nativeRuntime && (
                    <Button
                        size="sm"
                        variant="destructive"
                        disabled={busy || runs.length === 0}
                        onClick={() => setConfirmAction('clear')}
                    >
                        <Trash2 className="h-3.5 w-3.5 mr-1.5" />
                        {t('activityPipeline.clear')}
                    </Button>
                )}
            </div>
            <ConfirmDialog
                open={confirmAction === 'run'}
                onOpenChange={(open) => {
                    if (!open) setConfirmAction(null);
                }}
                title={t('activityPipeline.confirmRun.title')}
                description={t('activityPipeline.confirmRun.description')}
                confirmLabel={t('activityPipeline.confirmRun.confirm')}
                onConfirm={() => void handleRunNow()}
            />
            <ConfirmDialog
                open={confirmAction === 'clear'}
                onOpenChange={(open) => {
                    if (!open) setConfirmAction(null);
                }}
                title={t('activityPipeline.confirmClear.title')}
                description={t('activityPipeline.confirmClear.description')}
                confirmLabel={t('activityPipeline.confirmClear.confirm')}
                variant="danger"
                onConfirm={() => void handleClear()}
            />

            {/* P2 — Cross-run Stats. Collapsed by default so operators
                who are only diffing one run aren't overwhelmed by aggregate
                numbers; expand to see "how does the pipeline behave across
                the last 24h / 7d / all loaded runs". Native <details> for
                a11y + zero state management. */}
            <details className="group/stats border border-border rounded-md bg-panel">
                <summary className="cursor-pointer select-none px-3 py-2 flex items-center gap-2 text-sm font-medium hover:bg-ink/[0.04]">
                    <ChevronRight
                        aria-hidden
                        className="h-3.5 w-3.5 shrink-0 transition-transform group-open/stats:rotate-90 text-muted"
                    />
                    {t('activityPipeline.stats.title')}
                    {aggStats.totalGroups > 0 && (
                        <span className="text-xs text-muted/80 font-normal">
                            {t('activityPipeline.stats.headerSummary', { count: aggStats.totalGroups })}
                        </span>
                    )}
                </summary>
                <div className="px-3 pb-3 pt-1 space-y-3 text-xs">
                    {/* Range selector — chips, same affordance as the filter
                        chips below so users don't have to learn two patterns. */}
                    <div className="flex items-center gap-1.5">
                        <span className="text-muted">{t('activityPipeline.stats.rangeLabel')}</span>
                        {(['24h', '7d', 'all'] as const).map((r) => (
                            <button
                                key={r}
                                type="button"
                                onClick={() => setStatsRange(r)}
                                className={`px-2 py-0.5 rounded text-[11px] font-medium transition-colors ${
                                    statsRange === r
                                        ? 'bg-surface-active text-ink'
                                        : 'text-muted hover:text-ink hover:bg-ink/[0.06]'
                                }`}
                            >
                                {t(`activityPipeline.stats.range.${r}` as const)}
                            </button>
                        ))}
                    </div>

                    {aggStats.totalGroups === 0 ? (
                        <div className="text-muted py-4 text-center">
                            {t('activityPipeline.stats.emptyWindow')}
                        </div>
                    ) : aggStats.groupsWithFrames === 0 ? (
                        // Pure-legacy window: every group in range predates
                        // selected_frames. Rendering the normal grid would
                        // surface 0.0 averages, "No drops", and 0 / 0 cap
                        // pressure — all of which read as "everything healthy"
                        // when the truth is "we have no audit data for any of
                        // these runs". Show a distinct state so the operator
                        // doesn't trust meaningless zeros.
                        <div className="text-muted py-4 text-center text-warning">
                            {t('activityPipeline.stats.noAuditDataInWindow', { count: aggStats.totalGroups })}
                        </div>
                    ) : (
                        <>
                            {/* Headline counters — group runs are the unit of analysis;
                                avg winners/drops normalize against group count so the
                                numbers stay comparable across windows. */}
                            <div className="grid grid-cols-3 gap-3">
                                <StatTile
                                    label={t('activityPipeline.stats.totalGroups')}
                                    value={aggStats.totalGroups.toString()}
                                />
                                <StatTile
                                    label={t('activityPipeline.stats.avgWinners')}
                                    value={aggStats.avgWinnersPerRun.toFixed(1)}
                                />
                                <StatTile
                                    label={t('activityPipeline.stats.avgDrops')}
                                    value={aggStats.avgDropsPerRun.toFixed(1)}
                                />
                            </div>

                            {/* Winner mix — single horizontal bar split parsed vs ocr.
                                Both colors match the timeline tick palette so the
                                operator's mental model carries over. */}
                            {(aggStats.winnerMix.parsed + aggStats.winnerMix.ocr) > 0 && (
                                <div>
                                    <div className="flex items-center justify-between text-muted mb-1">
                                        <span>{t('activityPipeline.stats.winnerMix')}</span>
                                        <span className="tabular-nums">
                                            {t('activityPipeline.stats.winnerMixDetail', {
                                                parsed: aggStats.winnerMix.parsed,
                                                ocr: aggStats.winnerMix.ocr,
                                            })}
                                        </span>
                                    </div>
                                    {(() => {
                                        const total = aggStats.winnerMix.parsed + aggStats.winnerMix.ocr;
                                        const parsedPct = (aggStats.winnerMix.parsed / total) * 100;
                                        return (
                                            <div className="flex h-2 rounded-full overflow-hidden bg-ink/5">
                                                <div className="bg-success" style={{ width: `${parsedPct}%` }} />
                                                <div className="bg-info flex-1" />
                                            </div>
                                        );
                                    })()}
                                </div>
                            )}

                            {/* Drop reason breakdown — sorted desc so the dominant
                                pressure surface is at the top. Empty drop list
                                (everything was a winner) gets a soft "no drops"
                                line; not an error. */}
                            <div>
                                <div className="text-muted mb-1">{t('activityPipeline.stats.dropBreakdown')}</div>
                                {Object.keys(aggStats.dropBreakdown).length === 0 ? (
                                    <div className="text-muted/60 italic">{t('activityPipeline.stats.noDrops')}</div>
                                ) : (
                                    (() => {
                                        const entries = Object.entries(aggStats.dropBreakdown).sort((a, b) => b[1] - a[1]);
                                        const max = entries[0][1];
                                        return (
                                            <div className="space-y-1">
                                                {entries.map(([reason, count]) => (
                                                    <div key={reason} className="flex items-center gap-2">
                                                        <span className="w-44 truncate text-ink/80" title={reason}>
                                                            {isKnownDropReason(reason)
                                                                ? t(`activityPipeline.stats.reasonLabel.${reason}` as const)
                                                                : reason}
                                                        </span>
                                                        <div className="flex-1 h-1.5 rounded-full bg-ink/5 overflow-hidden">
                                                            <div
                                                                className="h-full bg-error-bg"
                                                                style={{ width: `${(count / max) * 100}%` }}
                                                            />
                                                        </div>
                                                        <span className="tabular-nums w-12 text-right text-muted">{count}</span>
                                                    </div>
                                                ))}
                                            </div>
                                        );
                                    })()
                                )}
                            </div>

                            {/* Cap pressure — % of audited runs that hit each cap.
                                Uses groupsWithFrames as denominator (NOT
                                totalGroups) so legacy groups with no
                                selected_frames don't dilute the percentage. */}
                            <div className="grid grid-cols-2 gap-3">
                                <StatTile
                                    label={t('activityPipeline.stats.capOcr')}
                                    value={`${aggStats.capPressure.ocrOverCap} / ${aggStats.groupsWithFrames}`}
                                    hint={t('activityPipeline.stats.capPct', {
                                        value: aggStats.groupsWithFrames > 0
                                            ? ((aggStats.capPressure.ocrOverCap / aggStats.groupsWithFrames) * 100).toFixed(0)
                                            : '0',
                                    })}
                                    warn={aggStats.groupsWithFrames > 0
                                        && aggStats.capPressure.ocrOverCap / aggStats.groupsWithFrames > 0.2}
                                />
                                <StatTile
                                    label={t('activityPipeline.stats.capParsed')}
                                    value={`${aggStats.capPressure.parsedOversize} / ${aggStats.groupsWithFrames}`}
                                    hint={t('activityPipeline.stats.capPct', {
                                        value: aggStats.groupsWithFrames > 0
                                            ? ((aggStats.capPressure.parsedOversize / aggStats.groupsWithFrames) * 100).toFixed(0)
                                            : '0',
                                    })}
                                    warn={aggStats.groupsWithFrames > 0
                                        && aggStats.capPressure.parsedOversize / aggStats.groupsWithFrames > 0.2}
                                />
                            </div>

                            {/* Audit coverage footnote — exposes how many group
                                runs in the window were excluded from the
                                analytical denominator. Quiet by design: only
                                visible when non-zero, so a clean window stays
                                uncluttered. */}
                            {aggStats.groupsWithoutFrames > 0 && (
                                <div className="text-[10px] text-muted/70 italic">
                                    {t('activityPipeline.stats.auditCoverageNote', {
                                        excluded: aggStats.groupsWithoutFrames,
                                        total: aggStats.totalGroups,
                                    })}
                                </div>
                            )}

                            {/* OCR confidence summary. Skipped when no OCR winners
                                in the window (parsed-only runs are normal for
                                short Slack-heavy bursts). */}
                            {aggStats.ocrConfidence.count > 0 && (
                                <div className="grid grid-cols-2 gap-3">
                                    <StatTile
                                        label={t('activityPipeline.stats.ocrAvgConf')}
                                        value={`${(aggStats.ocrConfidence.avg * 100).toFixed(0)}%`}
                                        warn={aggStats.ocrConfidence.avg < 0.7}
                                    />
                                    <StatTile
                                        label={t('activityPipeline.stats.ocrLowConfShare')}
                                        value={`${(aggStats.ocrConfidence.lowConfShare * 100).toFixed(0)}%`}
                                        hint={t('activityPipeline.stats.ocrLowConfHint', { count: aggStats.ocrConfidence.count })}
                                        warn={aggStats.ocrConfidence.lowConfShare > 0.3}
                                    />
                                </div>
                            )}
                        </>
                    )}
                </div>
            </details>

            {/* Filter chips */}
            <div className="flex items-center gap-2 flex-wrap">
                <ListFilter className="h-3.5 w-3.5 text-muted" />
                {filters.map((f) => (
                    <button
                        key={f.id}
                        className={`px-2.5 py-1 rounded-md text-xs font-medium transition-colors ${
                            filter === f.id
                                ? 'bg-surface-active text-ink'
                                : 'text-muted hover:text-ink hover:bg-ink/[0.06]'
                        }`}
                        onClick={() => setFilter(f.id)}
                    >
                        {t(`activityPipeline.filters.${f.labelKey}` as never) as string}
                        {f.count != null && f.count > 0 && (
                            <span className="ml-1 text-[10px] opacity-60">{f.count}</span>
                        )}
                    </button>
                ))}
                <div className="flex-1" />
                <span className="text-xs text-muted">{t('activityPipeline.entriesCount', { count: filtered.length })}</span>
            </div>

            {/* Run list */}
            <SettingsCard title="">
                <div className="max-h-[600px] overflow-y-auto -mx-1">
                    {filtered.length === 0 && (
                        <div className="text-center text-xs text-muted py-12">
                            {t('activityPipeline.emptyState')}
                        </div>
                    )}
                    {filtered.map((run) => {
                        const isExpanded = expandedId === run.id;
                        const isTick = run.kind === 'tick';
                        const initiativeId = (run as main.ActivityPipelineRunRow & { initiativeId?: string }).initiativeId;
                        const shadowRunId = (run as main.ActivityPipelineRunRow & { shadowRunId?: string }).shadowRunId;
                        const runKindLabel = localizedTechnicalLabel(t, 'activityPipeline.kindLabels', run.kind);
                        const runStatusLabel = (value: string) => localizedTechnicalLabel(t, 'activityPipeline.statusLabels', value);
                        return (
                            <div
                                key={run.id}
                                className="border-b border-border/40 last:border-b-0"
                            >
                                <button
                                    type="button"
                                    onClick={() => setExpandedId(isExpanded ? null : run.id)}
                                    className="grid w-full grid-cols-[minmax(0,1fr)_auto] items-start gap-2 px-2 py-2 text-left transition-colors hover:bg-ink/[0.06] sm:grid-cols-[140px_minmax(0,1fr)_auto]"
                                >
                                    <span className="col-span-2 font-mono text-[11px] text-muted/60 sm:col-span-1 sm:w-[140px] sm:pt-0.5">
                                        {fmtDate(run.startedAt)} {fmtTime(run.startedAt)}
                                    </span>
                                    <span className="min-w-0">
                                        <span className="flex min-w-0 items-center gap-2">
                                            <span
                                                className={`shrink-0 rounded px-1.5 py-0.5 text-[10px] font-medium ${
                                                    isTick
                                                        ? 'bg-info/15 text-info'
                                                        : 'bg-accent-light text-accent'
                                                }`}
                                                title={t('activityPipeline.technicalKind', { value: run.kind })}
                                            >
                                                {runKindLabel}
                                            </span>
                                            <span className="min-w-0 truncate pt-0.5 text-[11px] text-ink/70" title={isTick ? undefined : run.groupId}>
                                                {isTick
                                                    ? t('activityPipeline.tickSummary', { pending: run.pendingCount, groups: run.groupCount, filtered: run.filteredCount, candidates: run.candidateCount })
                                                    : t('activityPipeline.groupSummary', { groupId: run.groupId, count: run.eventCount })}
                                            </span>
                                        </span>
                                        {!isTick && (
                                            <span className="mt-1.5 flex flex-wrap items-center gap-1">
                                            {(run.worthMemory != null || run.worthKnowledge != null || run.worthAutomation != null || run.worthHandoff != null) && (
                                                <span className="flex flex-wrap items-center gap-1">
                                                    {run.worthMemory != null && (
                                                        <span className={`text-[9px] font-medium px-1 py-0.5 rounded ${run.worthMemory ? 'bg-accent-light text-accent' : 'bg-muted/10 text-muted/40'}`} title={t('activityPipeline.worthTooltip.memory', { value: String(run.worthMemory) })}>{t('activityPipeline.streamLabels.memory')}</span>
                                                    )}
                                                    {run.worthKnowledge != null && (
                                                        <span className={`text-[9px] font-medium px-1 py-0.5 rounded ${run.worthKnowledge ? 'bg-accent-light text-accent' : 'bg-muted/10 text-muted/40'}`} title={t('activityPipeline.worthTooltip.knowledge', { value: String(run.worthKnowledge) })}>{t('activityPipeline.streamLabels.knowledge')}</span>
                                                    )}
                                                    {run.worthAutomation != null && (
                                                        <span className={`text-[9px] font-medium px-1 py-0.5 rounded ${run.worthAutomation ? 'bg-accent-light text-accent' : 'bg-muted/10 text-muted/40'}`} title={t('activityPipeline.worthTooltip.scheduled', { value: String(run.worthAutomation) })}>{t('activityPipeline.streamLabels.work')}</span>
                                                    )}
                                                    {run.worthHandoff != null && (
                                                        <span className={`text-[9px] font-medium px-1 py-0.5 rounded ${run.worthHandoff ? 'bg-accent-light text-accent' : 'bg-muted/10 text-muted/40'}`} title={t('activityPipeline.worthTooltip.handoff', { value: String(run.worthHandoff) })}>{t('activityPipeline.streamLabels.handoff')}</span>
                                                    )}
                                                </span>
                                            )}
                                            <span className="flex flex-wrap items-center gap-1">
                                                {run.triageStatus && (
                                                    <span className={`text-[9px] font-medium px-1.5 py-0.5 rounded ${statusBadgeStyle(run.triageStatus)}`} title={t('activityPipeline.statusTooltip.triage', { value: run.triageStatus })}>
                                                        {t('activityPipeline.streamLabels.learning')}: {runStatusLabel(run.triageStatus)}
                                                    </span>
                                                )}
                                                {run.automationStatus && (
                                                    <span className={`text-[9px] font-medium px-1.5 py-0.5 rounded ${statusBadgeStyle(run.automationStatus)}`} title={t('activityPipeline.statusTooltip.scheduled', { value: run.automationStatus })}>
                                                        {shadowRunId ? t('activityPipeline.streamLabels.shadow') : t('activityPipeline.streamLabels.work')}: {runStatusLabel(run.automationStatus)}
                                                    </span>
                                                )}
                                                {run.memoryStatus && (
                                                    <span className={`text-[9px] font-medium px-1.5 py-0.5 rounded ${statusBadgeStyle(run.memoryStatus)}`} title={t('activityPipeline.statusTooltip.memory', { value: run.memoryStatus })}>
                                                        {t('activityPipeline.streamLabels.memory')}: {runStatusLabel(run.memoryStatus)}
                                                    </span>
                                                )}
                                                {run.knowledgeStatus && (
                                                    <span className={`text-[9px] font-medium px-1.5 py-0.5 rounded ${statusBadgeStyle(run.knowledgeStatus)}`} title={t('activityPipeline.statusTooltip.knowledge', { value: run.knowledgeStatus })}>
                                                        {t('activityPipeline.streamLabels.knowledge')}: {runStatusLabel(run.knowledgeStatus)}
                                                    </span>
                                                )}
                                                {run.handoffStatus && (
                                                    <span className={`text-[9px] font-medium px-1.5 py-0.5 rounded ${statusBadgeStyle(run.handoffStatus)}`} title={t('activityPipeline.statusTooltip.handoff', { value: run.handoffStatus })}>
                                                        {t('activityPipeline.streamLabels.handoff')}: {runStatusLabel(run.handoffStatus)}
                                                    </span>
                                                )}
                                                {run.triagePromptBytes != null && run.triagePromptBytes > 0 && (
                                                    <span className="text-[9px] font-medium px-1.5 py-0.5 rounded bg-muted/15 text-muted">
                                                        {t('activityPipeline.promptSizeChip', { size: formatBytes(run.triagePromptBytes) })}
                                                    </span>
                                                )}
                                            </span>
                                            </span>
                                        )}
                                    </span>
                                    <span className="flex items-center gap-1">
                                        {run.error && (
                                            <span className="shrink-0 rounded bg-error-bg px-1.5 py-0.5 text-[10px] font-medium text-error">
                                                {t('activityPipeline.errorBadge')}
                                            </span>
                                        )}
                                        <ChevronDown
                                            className={`h-3.5 w-3.5 shrink-0 text-muted transition-transform ${isExpanded ? 'rotate-180' : ''}`}
                                        />
                                    </span>
                                </button>

                                {isExpanded && (
                                    <div className="px-3 pb-3 pt-1 space-y-3 bg-ink/[0.02]">
                                        {/* Metadata */}
                                        <div className="grid grid-cols-2 gap-x-4 gap-y-1 text-[11px]">
                                            <div className="text-muted">{t('activityPipeline.metadata.id')}</div>
                                            <div className="font-mono text-ink/80">{run.id}</div>
                                            <div className="text-muted">{t('activityPipeline.metadata.started')}</div>
                                            <div className="font-mono text-ink/80">{fmtDate(run.startedAt)} {fmtTime(run.startedAt)}</div>
                                            <div className="text-muted">{t('activityPipeline.metadata.completed')}</div>
                                            <div className="font-mono text-ink/80">{fmtDate(run.completedAt)} {fmtTime(run.completedAt)}</div>
                                            {!isTick && run.durationMs != null && (
                                                <>
                                                    <div className="text-muted">{t('activityPipeline.metadata.duration')}</div>
                                                    <div className="font-mono text-ink/80">{t('activityPipeline.metadata.durationSeconds', { count: Math.round(run.durationMs / 1000) })}</div>
                                                </>
                                            )}
                                            {!isTick && run.appNames && (
                                                <>
                                                    <div className="text-muted">{t('activityPipeline.metadata.apps')}</div>
                                                    <div className="font-mono text-ink/80 truncate" title={run.appNames}>{run.appNames}</div>
                                                </>
                                            )}
                                            {!isTick && run.sessionIds && (
                                                <>
                                                    <div className="text-muted">{t('activityPipeline.metadata.sessions')}</div>
                                                    <div className="font-mono text-ink/80 truncate" title={run.sessionIds}>{run.sessionIds}</div>
                                                </>
                                            )}
                                        </div>

                                        {/* Prefilter */}
                                        {!isTick && run.prefilterPass != null && (
                                            <div className="space-y-1">
                                                <div className="text-[11px] font-semibold text-muted">{t('activityPipeline.prefilter.title')}</div>
                                                <div className="text-[11px] text-ink/80">
                                                    {run.prefilterPass ? t('activityPipeline.prefilter.passed') : t('activityPipeline.prefilter.failed')}
                                                    {run.prefilterReason ? ` — ${run.prefilterReason}` : ''}
                                                </div>
                                            </div>
                                        )}

                                        {/* Per-stream outcomes */}
                                        {!isTick && (
                                            <div className="space-y-1">
                                                <div className="text-[11px] font-semibold text-muted">{t('activityPipeline.outcomes.title')}</div>
                                                <div className="grid grid-cols-2 gap-x-4 gap-y-1 text-[11px]">
                                                    <div className="text-muted">{t('activityPipeline.outcomes.triage')}</div>
                                                    <div className="text-ink/80">{run.triageStatus || t('activityPipeline.outcomes.fallback')}{run.triageReason ? ` — ${run.triageReason}` : ''}</div>
                                                    <div className="text-muted">{t('activityPipeline.outcomes.scheduled')}</div>
                                                    <div className="text-ink/80">{run.automationStatus || t('activityPipeline.outcomes.fallback')}{run.automationReason ? ` — ${run.automationReason}` : ''}{shadowRunId ? ` ${t('activityPipeline.outcomes.shadowRunSuffix', { shadowRunId })}` : ''}{initiativeId ? ` ${t('activityPipeline.outcomes.initiativeSuffix', { initiativeId })}` : ''}</div>
                                                    <div className="text-muted">{t('activityPipeline.outcomes.memory')}</div>
                                                    <div className="text-ink/80">{run.memoryStatus || t('activityPipeline.outcomes.fallback')}{run.memoryReason ? ` — ${run.memoryReason}` : ''}</div>
                                                    <div className="text-muted">{t('activityPipeline.outcomes.knowledge')}</div>
                                                    <div className="text-ink/80">{run.knowledgeStatus || t('activityPipeline.outcomes.fallback')}{run.knowledgeReason ? ` — ${run.knowledgeReason}` : ''}</div>
                                                    <div className="text-muted">{t('activityPipeline.outcomes.handoff')}</div>
                                                    <div className="text-ink/80">{run.handoffStatus || t('activityPipeline.outcomes.fallback')}{run.handoffReason ? ` — ${run.handoffReason}` : ''}{run.handoffProposalId ? ` ${t('activityPipeline.outcomes.proposalSuffix', { proposalId: run.handoffProposalId })}` : ''}</div>
                                                </div>
                                            </div>
                                        )}

                                        {/* Triage feature flag snapshot — the per-feature
                                            toggle state at the moment this prompt was
                                            assembled. Visually distinct from the Worth chips
                                            (those report AI decisions, these report user
                                            opt-in). Disabled flags are stripped from the
                                            prompt and downstream skipped. */}
                                        {!isTick && run.triageFlags && (() => {
                                            let parsed: { memory?: boolean; knowledge?: boolean; shadow?: boolean; automation?: boolean; handoff?: boolean } = {};
                                            try { parsed = JSON.parse(run.triageFlags); } catch { /* malformed — render empty */ }
                                            const items: Array<{ label: string; on: boolean }> = [
                                                { label: 'Memory', on: !!parsed.memory },
                                                { label: 'Knowledge', on: !!parsed.knowledge },
                                                { label: 'Shadow', on: parsed.shadow ?? !!parsed.automation },
                                                { label: 'Automation', on: !!parsed.automation },
                                                { label: 'Handoff', on: !!parsed.handoff },
                                            ];
                                            return (
                                                <div className="space-y-1">
                                                    <div className="text-[11px] font-semibold text-muted">{t('activityPipeline.triageFlagsTitle')}</div>
                                                    <div className="flex items-center gap-1 flex-wrap">
                                                        {items.map((it) => (
                                                            <span
                                                                key={it.label}
                                                                className={`text-[10px] font-medium px-1.5 py-0.5 rounded ${it.on ? 'bg-success/15 text-success' : 'bg-muted/10 text-muted/50 line-through'}`}
                                                            >
                                                                {it.label}
                                                            </span>
                                                        ))}
                                                    </div>
                                                    <div className="text-[10px] text-muted/70">{t('activityPipeline.triageFlagsHint')}</div>
                                                </div>
                                            );
                                        })()}

                                        {/* AI input */}
                                        {!isTick && run.aiSummary && (
                                            <div className="space-y-1">
                                                <div className="text-[11px] font-semibold text-muted">{t('activityPipeline.aiInputTitle')}</div>
                                                <pre className="text-[11px] font-mono text-ink/80 whitespace-pre-wrap bg-ink/[0.04] rounded p-2 max-h-[300px] overflow-auto">
                                                    {run.aiSummary}
                                                </pre>
                                            </div>
                                        )}

                                        {/* Selected frames → jump to Activity for visual audit */}
                                        {!isTick && run.selectedFrames && run.selectedFrames.length > 0 && onJumpToActivity && (() => {
                                            // Build the displayable frame list. OCR frames whose real
                                            // time fell outside the session's snapshot range get pinned
                                            // with snapshotId=0 by the backend — drop those, they have
                                            // nothing to render. Keep ALL valid kinds (parsed, ocr,
                                            // parsed_sibling, parsed_skipped, ocr_skipped) so the audit
                                            // UI can paint siblings (slate) and skips (rose) alongside
                                            // the winners (emerald/sky). Filtering by kind here would
                                            // strand the new skip-reason data even though the backend
                                            // recorded it.
                                            const frames = run.selectedFrames
                                                .filter((f) => f.sessionId && f.snapshotId != null && f.snapshotId > 0)
                                                .map((f) => ({
                                                    kind: f.kind,
                                                    sessionId: f.sessionId,
                                                    snapshotId: f.snapshotId as number,
                                                    promptText: f.promptText,
                                                    skipReason: f.skipReason,
                                                    ocrConfidence: f.ocrConfidence,
                                                    parsedSiblingCount: f.parsedSiblingCount,
                                                    browserUrl: f.browserUrl,
                                                    dedupChainLen: f.dedupChainLen,
                                                }));
                                            // Seed session must come from a WINNER frame — landing on a
                                            // session whose only frames are siblings/skips would open an
                                            // empty timeline. Counter math also restricted to winners so
                                            // the button label still reads "View N used frames" not
                                            // "View N considered frames".
                                            const parsedCount = frames.filter((f) => f.kind === 'parsed').length;
                                            const ocrCount = frames.filter((f) => f.kind === 'ocr').length;
                                            const firstFrame = frames.find((f) => f.kind === 'parsed' || f.kind === 'ocr');
                                            // Nothing displayable: skip the button entirely. A "View 0
                                            // used frames" CTA is worse than no CTA — it implies action
                                            // but does nothing useful.
                                            if (!firstFrame || parsedCount + ocrCount === 0) return null;
                                            // Full group session list — every member of the merged group,
                                            // even ones whose snapshots all got dedup-dropped and never
                                            // appear in selectedFrames. Without this the sidebar would
                                            // fall back to the date-filter view and bury group members
                                            // in unrelated sessions.
                                            const groupSessionIds = parsePipelineSessionIds(run.sessionIds);
                                            return (
                                                <div className="flex items-center gap-2 flex-wrap">
                                                    <Button
                                                        size="sm"
                                                        variant="outline"
                                                        onClick={() => onJumpToActivity(firstFrame.sessionId, frames, run.id, groupSessionIds)}
                                                    >
                                                        <Activity className="h-3.5 w-3.5 mr-1.5" />
                                                        {t('activityPipeline.selectedFrames.viewButton', {
                                                            count: parsedCount + ocrCount,
                                                            parsed: parsedCount,
                                                            ocr: ocrCount,
                                                        })}
                                                    </Button>
                                                    <span className="text-[11px] text-muted/80">
                                                        {t('activityPipeline.selectedFrames.viewHint')}
                                                    </span>
                                                </div>
                                            );
                                        })()}

                                        {/* AI response */}
                                        {!isTick && run.aiResponseRaw && (
                                            <div className="space-y-1">
                                                <div className="text-[11px] font-semibold text-muted">{t('activityPipeline.aiResponseTitle')}</div>
                                                <pre className="text-[11px] font-mono text-ink/80 whitespace-pre-wrap bg-ink/[0.04] rounded p-2 max-h-[300px] overflow-auto">
                                                    {tryPrettyJSON(run.aiResponseRaw)}
                                                </pre>
                                            </div>
                                        )}

                                        {/* Triage prompt — the exact text sent to the LLM
                                            for this run, after per-feature sections were
                                            trimmed according to triageFlags. Empty body
                                            means triage short-circuited (e.g. all features
                                            disabled or runner not ready). */}
                                        {!isTick && run.triageFlags && (
                                            <div className="space-y-1">
                                                <div className="text-[11px] font-semibold text-muted">
                                                    {t('activityPipeline.triagePromptTitle')}
                                                    {run.triagePromptBytes != null && run.triagePromptBytes > 0 && (
                                                        <span className="ml-1.5 text-muted/60 font-normal">({formatBytes(run.triagePromptBytes)})</span>
                                                    )}
                                                </div>
                                                {run.triagePrompt ? (
                                                    <pre className="text-[11px] font-mono text-ink/80 whitespace-pre-wrap bg-ink/[0.04] rounded p-2 max-h-[300px] overflow-auto">
                                                        {run.triagePrompt}
                                                    </pre>
                                                ) : (
                                                    <div className="text-[11px] italic text-muted/70">{t('activityPipeline.triagePromptEmpty')}</div>
                                                )}
                                            </div>
                                        )}

                                        {/* Error */}
                                        {run.error && (
                                            <div className="space-y-1">
                                                <div className="text-[11px] font-semibold text-error">{t('activityPipeline.errorTitle')}</div>
                                                <pre className="text-[11px] font-mono text-error whitespace-pre-wrap bg-error-bg/50 rounded p-2 max-h-[200px] overflow-auto">
                                                    {run.error}
                                                </pre>
                                            </div>
                                        )}
                                    </div>
                                )}
                            </div>
                        );
                    })}
                </div>
            </SettingsCard>
        </>
    );
}

// ── Notification Test Settings ──

function NotificationTestSettings() {
    const { t } = useTranslation('settings');
    return (
        <>
            <SettingsCard title={t('notifications.cardTitle')}>
                <p className="text-sm text-muted leading-relaxed mb-4">
                    {t('notifications.cardDescription')}
                </p>
                <div className="flex flex-col gap-3">
                    <div className="flex items-center justify-between gap-4">
                        <div>
                            <p className="text-sm font-medium text-ink">{t('notifications.longToast.title')}</p>
                            <p className="text-xs text-muted">{t('notifications.longToast.description')}</p>
                        </div>
                        <LongToastTestButton />
                    </div>
                    <div className="flex items-center justify-between gap-4">
                        <div>
                            <p className="text-sm font-medium text-ink">{t('notifications.desktopTest.title')}</p>
                            <p className="text-xs text-muted">{t('notifications.desktopTest.description')}</p>
                        </div>
                        <Button
                            variant="outline"
                            size="sm"
                            onClick={async () => {
                                try {
                                    await TestDesktopNotification();
                                } catch {
                                    // Rejects when notifications are disabled in
                                    // System Settings — surface that as an in-app
                                    // toast so the suppression is visible.
                                    notifyToast(
                                        t('notifications.desktopTest.suppressedTitle'),
                                        t('notifications.desktopTest.suppressedBody'),
                                        'info',
                                    );
                                }
                            }}
                        >
                            <Bell className="w-3.5 h-3.5 mr-1.5" />
                            {t('notifications.desktopTest.button')}
                        </Button>
                    </div>
                    <div className="flex items-center justify-between gap-4">
                        <div>
                            <p className="text-sm font-medium text-ink">{t('notifications.windowPopRepro.title')}</p>
                            <p className="text-xs text-muted">{t('notifications.windowPopRepro.description')}</p>
                        </div>
                        <Button
                            variant="outline"
                            size="sm"
                            onClick={() => {
                                // Arm a native toast that fires 5s later from a Go
                                // goroutine (not a WebView timer, which macOS
                                // throttles while the window is hidden) so the
                                // window can be closed to the menu bar first.
                                void FireDelayedDesktopNotification(
                                    t('notifications.windowPopRepro.toastTitle'),
                                    t('notifications.windowPopRepro.toastBody'),
                                );
                                notifyToast(
                                    t('notifications.windowPopRepro.armedTitle'),
                                    t('notifications.windowPopRepro.armedBody'),
                                    'info',
                                );
                            }}
                        >
                            <Bell className="w-3.5 h-3.5 mr-1.5" />
                            {t('notifications.windowPopRepro.button')}
                        </Button>
                    </div>
                </div>
            </SettingsCard>
        </>
    );
}

// ── Audio Capture (DevTools) ──
//
// Surfaces native recorder lifecycle events so "why is my voice missing
// from meeting transcripts?" stops being a silent failure. The backend
// emits `audio-capture:event` on start attempts, stops, and errors. This
// panel renders them live.

const AUDIO_CAPTURE_KIND_BADGE: Record<string, { bg: string; color: string }> = {
    started: { bg: 'bg-automation-bg', color: 'text-automation' },
    resumed: { bg: 'bg-automation-bg', color: 'text-automation' },
    restart: { bg: 'bg-info/15', color: 'text-info' },
    suspended: { bg: 'bg-warning-bg', color: 'text-warning' },
    stopped: { bg: 'bg-muted/20', color: 'text-muted' },
    config_changed: { bg: 'bg-panel-warm', color: 'text-ink' },
    start_attempt: { bg: 'bg-muted/15', color: 'text-muted' },
    restart_attempt: { bg: 'bg-muted/15', color: 'text-muted' },
    resume_attempt: { bg: 'bg-muted/15', color: 'text-muted' },
    error: { bg: 'bg-error-bg', color: 'text-error' },
};

function AudioCaptureSettings() {
    const { t } = useTranslation('settings');
    const [events, setEvents] = useState<main.AudioCaptureEvent[]>([]);
    const [status, setStatus] = useState<main.BackgroundAudioStatus | null>(null);
    const listRef = useRef<HTMLDivElement>(null);

    const audioCaptureKindLabel = (kind: string): string => {
        const translated = t(`audioCapture.kind.${kind}` as never) as string;
        if (translated && translated !== `audioCapture.kind.${kind}`) return translated;
        return kind;
    };

    const audioCaptureOutcomeLabel = (outcome: string): string => {
        const key = `audioCapture.outcome.${outcome}`;
        const translated = t(key as never) as string;
        return translated === key ? humanizeTechnicalValue(outcome) : translated;
    };

    const loadEvents = useCallback(() => {
        GetAudioCaptureEvents().then((result) => setEvents(result ?? []));
    }, []);

    const loadStatus = useCallback(() => {
        GetBackgroundAudioStatus().then(setStatus).catch(() => setStatus(null));
    }, []);

    useEffect(() => {
        loadEvents();
        loadStatus();
    }, [loadEvents, loadStatus]);

    // Live updates: each new event arrives via `audio-capture:event` from
    // the Go event store. Matches the pattern used by triage:log and
    // parser:dispatch.
    useEffect(() => {
        const cleanup = EventsOn('audio-capture:event', (entry: main.AudioCaptureEvent) => {
            setEvents((prev) => {
                const next = [...prev, entry];
                if (next.length > 500) return next.slice(next.length - 500);
                return next;
            });
            // Status changes whenever recorder lifecycle state changes.
            loadStatus();
        });
        return cleanup;
    }, [loadStatus]);

    useEffect(() => {
        const list = listRef.current;
        if (list) list.scrollTop = list.scrollHeight;
    }, [events]);

    const micActive = !!status?.activeSources?.includes('microphone');
    const sysActive = !!status?.activeSources?.includes('system_audio');
    const suspendedReason = (status as (main.BackgroundAudioStatus & { suspendedReason?: string }) | null)?.suspendedReason ?? '';
    const suspensionKey = suspendedReason
        ? `audioCapture.suspension.${suspendedReason}`
        : '';
    const suspensionMessage = suspensionKey
        ? t(suspensionKey as never) as string
        : '';
    const expectedPause = suspendedReason === 'desktop_locked' || suspendedReason === 'privacy_pause';
    const inactiveLabel = expectedPause ? t('audioCapture.paused') : t('audioCapture.stopped');
    const inactiveDot = expectedPause ? 'bg-warning' : 'bg-error';

    return (
        <>
            <p className="text-sm text-muted">
                {t('audioCapture.description')}
            </p>

            {/* Current status */}
            <SettingsCard title={t('audioCapture.currentStatusTitle')}>
                <div className="space-y-2">
                    <div className="flex flex-wrap items-center gap-3 text-sm">
                        <div className="flex items-center gap-1.5">
                            <span className={`inline-block h-2 w-2 rounded-full ${micActive ? 'bg-automation' : inactiveDot}`} />
                            <span className="font-medium">{t('audioCapture.mic')}</span>
                            <span className="text-muted">{micActive ? t('audioCapture.running') : inactiveLabel}</span>
                        </div>
                        <span className="text-muted/40">·</span>
                        <div className="flex items-center gap-1.5">
                            <span className={`inline-block h-2 w-2 rounded-full ${sysActive ? 'bg-automation' : inactiveDot}`} />
                            <span className="font-medium">{t('audioCapture.systemAudio')}</span>
                            <span className="text-muted">{sysActive ? t('audioCapture.running') : inactiveLabel}</span>
                        </div>
                        <div className="flex-1" />
                        <button
                            className="p-1 rounded-md text-muted hover:text-ink hover:bg-ink/[0.06] transition-colors"
                            onClick={() => { loadEvents(); loadStatus(); }}
                            title={t('audioCapture.reload')}
                            aria-label={t('audioCapture.reload')}
                        >
                            <RefreshCw className="h-3.5 w-3.5" />
                        </button>
                    </div>
                    {suspensionMessage && (
                        <p role="status" className="text-xs text-muted">
                            {suspensionMessage}
                        </p>
                    )}
                </div>
            </SettingsCard>

            {/* Event log */}
            <SettingsCard title={t('audioCapture.eventLogTitle')}>
                <div ref={listRef} className="max-h-[500px] overflow-y-auto space-y-0.5 -mx-1">
                    {events.length === 0 && (
                        <div className="text-center text-xs text-muted py-12">
                            {t('audioCapture.emptyState')}
                        </div>
                    )}
                    {events.map((entry, idx) => {
                        const style = AUDIO_CAPTURE_KIND_BADGE[entry.kind] ?? { bg: 'bg-muted/15', color: 'text-muted' };
                        const ts = new Date(entry.timestamp).toLocaleTimeString();
                        const outcomeStyle = entry.outcome === 'failed'
                            ? 'bg-error-bg text-error'
                            : entry.outcome === 'success'
                                ? 'bg-success-bg text-success'
                                : '';
                        return (
                            <div
                                key={`${entry.timestamp}-${idx}`}
                                className="flex items-start gap-2 py-1.5 px-2 rounded-md hover:bg-ink/[0.06] group transition-colors"
                            >
                                <span className="text-[11px] text-muted/60 font-mono shrink-0 w-[80px] pt-0.5">
                                    {ts}
                                </span>
                                <span className="text-[10px] font-medium px-1.5 py-0.5 rounded shrink-0 bg-muted/15 text-muted">
                                    {entry.source || t('audioCapture.sourceFallback')}
                                </span>
                                <span className={`text-[10px] font-medium px-1.5 py-0.5 rounded shrink-0 ${style.bg} ${style.color}`}>
                                    {audioCaptureKindLabel(entry.kind)}
                                </span>
                                {entry.outcome && (
                                    <span
                                        className={`text-[10px] font-medium px-1.5 py-0.5 rounded shrink-0 ${outcomeStyle}`}
                                        title={t('audioCapture.technicalOutcome', { value: entry.outcome })}
                                    >
                                        {audioCaptureOutcomeLabel(entry.outcome)}
                                    </span>
                                )}
                                <span className="text-xs text-ink/80 flex-1 break-words pt-0.5">
                                    {entry.message || <span className="text-muted/60">{t('audioCapture.messageFallback')}</span>}
                                </span>
                            </div>
                        );
                    })}
                </div>
            </SettingsCard>
        </>
    );
}

function PerfStatBox({ label, value, hint }: { label: string; value: string; hint?: string }) {
    return (
        <div className="rounded-md bg-background p-2.5">
            <div className="text-muted text-[11px] mb-0.5">{label}</div>
            <div className="font-mono text-ink">{value}</div>
            {hint && <div className="text-muted text-[10px] mt-0.5">{hint}</div>}
        </div>
    );
}

function PerfAnalysisSettings() {
    const { t } = useTranslation('settings');
    const [snap, setSnap] = useState<PerfSnapshotShape | null>(null);
    const [error, setError] = useState<string>('');
    const [busy, setBusy] = useState(false);
    const [autoRefresh, setAutoRefresh] = useState(true);

    const refresh = useCallback(async () => {
        setBusy(true);
        try {
            const result = await GetPerfSnapshot();
            setSnap(result as unknown as PerfSnapshotShape);
            setError('');
        } catch (err) {
            setError(err instanceof Error ? err.message : String(err));
        } finally {
            setBusy(false);
        }
    }, []);

    useEffect(() => {
        refresh();
    }, [refresh]);

    useEffect(() => {
        if (!autoRefresh) return;
        // Paused while the window is hidden or the Mac locked.
        return activeInterval(refresh, 3000);
    }, [autoRefresh, refresh]);

    const handleCopy = useCallback(async () => {
        if (!snap) return;
        await copyPerfReport(
            snap,
            t('perfAnalysis.toasts.copySuccess'),
            (err) => t('perfAnalysis.toasts.copyFailed', { error: String(err) }),
            t('perfAnalysis.toasts.copyFailedManual'),
        );
    }, [snap, t]);

    const handleOpenPprof = useCallback(() => {
        if (snap?.pprof.url) BrowserOpenURL(snap.pprof.url);
    }, [snap]);

    const subprocesses = useMemo(() => snap ? [...snap.subprocesses].sort(sortSubprocs) : [], [snap]);

    return (
        <>
            <SettingsCard
                title={t('perfAnalysis.cardTitle')}
                description={t('perfAnalysis.cardDescription')}
                headerRight={
                    <div className="flex items-center gap-2">
                        <label className="flex items-center gap-1.5 text-xs text-muted">
                            <Switch checked={autoRefresh} onCheckedChange={setAutoRefresh} />
                            {t('perfAnalysis.autoRefresh')}
                        </label>
                        <Button size="sm" variant="outline" onClick={refresh} disabled={busy}>
                            <RefreshCw className={`w-3.5 h-3.5 ${busy ? 'animate-spin' : ''}`} />
                            {t('perfAnalysis.refresh')}
                        </Button>
                        <Button size="sm" onClick={handleCopy} disabled={!snap}>
                            <Copy className="w-3.5 h-3.5" />
                            {t('perfAnalysis.copyReport')}
                        </Button>
                    </div>
                }
            >
                {error && (
                    <div className="flex items-start gap-2 rounded-md border border-error-border bg-error-bg p-3 text-xs text-error">
                        <AlertCircle className="w-3.5 h-3.5 mt-0.5 shrink-0" />
                        <span>{t('perfAnalysis.snapshotLoadError', { error })}</span>
                    </div>
                )}

                {snap?.warnings && snap.warnings.length > 0 && (
                    <div className="mb-3 rounded-md border border-warning-border bg-warning-bg p-3">
                        <div className="flex items-center gap-2 mb-1 text-xs font-semibold text-warning">
                            <AlertCircle className="w-3.5 h-3.5" />
                            {t('perfAnalysis.potentialIssues', { count: snap.warnings.length })}
                        </div>
                        <ul className="ml-5 list-disc space-y-0.5 text-xs text-warning">
                            {snap.warnings.map((w, i) => (
                                <li key={i}>{w}</li>
                            ))}
                        </ul>
                    </div>
                )}

                {snap && (
                    <div className="grid grid-cols-2 gap-2 text-xs">
                        <div className="rounded-md bg-background p-2.5">
                            <div className="text-muted text-[11px] mb-0.5">{t('perfAnalysis.stats.uptime')}</div>
                            <div className="font-mono text-ink">{fmtDuration(snap.uptime_ms / 1000)}</div>
                        </div>
                        <div className="rounded-md bg-background p-2.5">
                            <div className="text-muted text-[11px] mb-0.5">{t('perfAnalysis.stats.version')}</div>
                            <div className="font-mono text-ink">{snap.version}</div>
                            <div className="text-muted text-[10px]">{snap.go_version} · {snap.platform}</div>
                        </div>
                    </div>
                )}
            </SettingsCard>

            {snap && (
                <SettingsCard
                    title={t('perfAnalysis.runtime.title')}
                    description={t('perfAnalysis.runtime.description')}
                >
                    <div className="grid grid-cols-3 gap-2 text-xs">
                        <PerfStatBox label={t('perfAnalysis.stats.goroutines')} value={String(snap.runtime.goroutines)} hint={snap.runtime.goroutines >= 200 ? t('perfAnalysis.stats.goroutinesHintHigh') : t('perfAnalysis.stats.goroutinesHintHealthy')} />
                        <PerfStatBox label={t('perfAnalysis.stats.heapInUse')} value={fmtMB(snap.runtime.heap_in_use_mb)} hint={t('perfAnalysis.stats.heapInUseHint', { value: fmtMB(snap.runtime.heap_sys_mb) })} />
                        <PerfStatBox label={t('perfAnalysis.stats.processRSS')} value={fmtMB(snap.runtime.rss_mb)} hint={t('perfAnalysis.stats.processRSSHint', { value: fmtMB(snap.runtime.vsz_mb) })} />
                        <PerfStatBox label={t('perfAnalysis.stats.cpuInstant')} value={fmtPct(snap.runtime.cpu_pct)} hint={snap.runtime.cpu_pct >= 200 ? t('perfAnalysis.stats.cpuInstantHintSaturated') : ''} />
                        <PerfStatBox label={t('perfAnalysis.stats.gcCycles')} value={String(snap.runtime.num_gc)} hint={t('perfAnalysis.stats.gcCyclesHint', { value: fmtMs(snap.runtime.gc_pause_last_ms) })} />
                        <PerfStatBox label={t('perfAnalysis.stats.totalAllocs')} value={fmtMB(snap.runtime.heap_allocs_total_mb)} hint={t('perfAnalysis.stats.totalAllocsHint')} />
                    </div>
                </SettingsCard>
            )}

            {snap?.activity_pipeline && (
                <SettingsCard
                    title={t('perfAnalysis.activityPipeline.title')}
                    description={t('perfAnalysis.activityPipeline.description')}
                >
                    <div className="grid grid-cols-3 gap-2 text-xs">
                        <PerfStatBox
                            label={t('perfAnalysis.stats.micQueue')}
                            value={t('perfAnalysis.stats.queueChunks', { count: snap.activity_pipeline.background_audio.microphone.queue_depth })}
                            hint={fmtMs(snap.activity_pipeline.background_audio.microphone.queue_duration_ms)}
                        />
                        <PerfStatBox
                            label={t('perfAnalysis.stats.systemQueue')}
                            value={t('perfAnalysis.stats.queueChunks', { count: snap.activity_pipeline.background_audio.system_audio.queue_depth })}
                            hint={fmtMs(snap.activity_pipeline.background_audio.system_audio.queue_duration_ms)}
                        />
                        <PerfStatBox
                            label={t('perfAnalysis.stats.whisperP95')}
                            value={fmtMs(snap.activity_pipeline.background_audio.whisper_p95_ms)}
                            hint={t('perfAnalysis.stats.whisperHint', { count: snap.activity_pipeline.background_audio.whisper_in_flight })}
                        />
                        <PerfStatBox
                            label={t('perfAnalysis.stats.capture')}
                            value={fmtMs(snap.activity_pipeline.capture.p95_ms)}
                            hint={t('perfAnalysis.stats.captureHint', { completed: snap.activity_pipeline.capture.completed, attempts: snap.activity_pipeline.capture.attempts })}
                        />
                        <PerfStatBox
                            label={t('perfAnalysis.stats.piiP95')}
                            value={fmtMs(snap.activity_pipeline.pii.p95_ms)}
                            hint={t('perfAnalysis.stats.piiHint', {
                                inFlight: snap.activity_pipeline.pii.in_flight,
                                throttled: snap.activity_pipeline.pii.throttled ?? 0,
                                mode: snap.activity_pipeline.pii.mode || (snap.activity_pipeline.pii.ready ? 'gliner' : 'unknown'),
                                queued: snap.activity_pipeline.pii.queued_frames ?? 0,
                            })}
                        />
                        <PerfStatBox
                            label={t('perfAnalysis.stats.visualChecks')}
                            value={snap.activity_pipeline.visual.checks.toLocaleString()}
                            hint={t('perfAnalysis.stats.visualHint', { count: snap.activity_pipeline.visual.promoted.toLocaleString() })}
                        />
                    </div>
                </SettingsCard>
            )}

            {snap && subprocesses.length > 0 && (
                <SettingsCard
                    title={t('perfAnalysis.subprocesses.title', { count: subprocesses.length })}
                    description={t('perfAnalysis.subprocesses.description')}
                >
                    <div className="max-h-96 overflow-auto">
                        <table className="w-full text-xs font-mono">
                            <thead className="sticky top-0 bg-panel">
                                <tr className="text-left text-muted border-b border-border">
                                    <th className="py-1 pr-2 font-normal">{t('perfAnalysis.subprocesses.headerPid')}</th>
                                    <th className="py-1 pr-2 font-normal">{t('perfAnalysis.subprocesses.headerKind')}</th>
                                    <th className="py-1 pr-2 font-normal">{t('perfAnalysis.subprocesses.headerDetail')}</th>
                                    <th className="py-1 pr-2 font-normal text-right">{t('perfAnalysis.subprocesses.headerElapsed')}</th>
                                    <th className="py-1 pr-2 font-normal text-right">{t('perfAnalysis.subprocesses.headerRss')}</th>
                                    <th className="py-1 pr-2 font-normal text-right">{t('perfAnalysis.subprocesses.headerCpu')}</th>
                                </tr>
                            </thead>
                            <tbody>
                                {subprocesses.map((sp) => {
                                    const isCliJs = sp.kind === 'cli.js';
                                    const kindKey = sp.kind === 'cli.js' ? 'cliJs' : sp.kind;
                                    const kindLabel = (t(`perfAnalysis.subprocesses.kind.${kindKey}` as never) as string) || sp.kind;
                                    const detail = isCliJs
                                        ? `${sp.model ?? '?'} · ${sp.session_id?.slice(0, 8) ?? ''}`
                                        : sp.kind === 'yansu'
                                            ? t('perfAnalysis.subprocesses.mainProcess')
                                            : '—';
                                    return (
                                        <tr key={sp.pid} className={`border-b border-border/40 ${sp.orphan ? 'text-warning' : ''}`}>
                                            <td className="py-1 pr-2 text-ink">{sp.pid}</td>
                                            <td className="py-1 pr-2">
                                                {kindLabel}
                                                {sp.orphan && <span className="ml-1 text-[10px] uppercase">{t('perfAnalysis.subprocesses.orphan')}</span>}
                                            </td>
                                            <td className="py-1 pr-2 truncate" title={sp.command}>{detail}</td>
                                            <td className="py-1 pr-2 text-right text-muted">{fmtDuration(sp.elapsed_seconds)}</td>
                                            <td className="py-1 pr-2 text-right text-ink">{fmtMB(sp.rss_mb)}</td>
                                            <td className="py-1 pr-2 text-right text-ink">{fmtPct(sp.cpu_pct)}</td>
                                        </tr>
                                    );
                                })}
                            </tbody>
                        </table>
                    </div>
                </SettingsCard>
            )}

            {snap && (
                <SettingsCard
                    title={t('perfAnalysis.pprof.title')}
                    description={t('perfAnalysis.pprof.description')}
                >
                    <div className="flex items-center justify-between text-xs">
                        <div>
                            {t('perfAnalysis.pprof.statusLabel')} <span className={snap.pprof.enabled ? 'text-success' : 'text-muted'}>
                                {snap.pprof.enabled ? t('perfAnalysis.pprof.enabled') : t('perfAnalysis.pprof.disabled')}
                            </span>
                            {snap.pprof.url && (
                                <span className="ml-3 font-mono text-muted">{snap.pprof.url}</span>
                            )}
                        </div>
                        {snap.pprof.enabled && (
                            <Button size="sm" variant="outline" onClick={handleOpenPprof}>
                                {t('perfAnalysis.pprof.openButton')}
                            </Button>
                        )}
                    </div>
                </SettingsCard>
            )}
        </>
    );
}
