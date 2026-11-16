import { useEffect, useMemo, useState, type ComponentType } from "react";
import { useTranslation } from "react-i18next";
import {
    Activity,
    Bell,
    ChevronDown,
    Gauge,
    Globe,
    ListFilter,
    Mic,
    Search,
    SearchCode,
    Target,
    Wrench,
} from "lucide-react";
import { ScrollArea } from "@/components/ui/scroll-area";
import { useAoyNavTag } from "@/components/settings/pages/agent-of-you/aoy-shared";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
import {
    DEV_TOOLS_GROUPS,
    DEV_TOOLS_TOPIC_IDS,
    type DevToolsTopicId,
} from "@/components/dev-tools-navigation";
import {
    SETTINGS_SECTIONS,
    getSettingsPage,
    isDevToolsTopicId,
    isSettingsPageId,
    pagesInSection,
    searchSettingsPages,
    settingsPageNameMatches,
    type SettingsDestination,
    type SettingsOpenOptions,
    type SettingsPageEntry,
    type SettingsSection,
    type SettingsSectionId,
} from "@/components/settings/settings-registry";

export const DEV_TOOLS_TOPIC_ICONS: Record<DevToolsTopicId, ComponentType<{ className?: string }>> = {
    "activity-pipeline": Activity,
    "audio-capture": Mic,
    "ax-parser": SearchCode,
    triage: ListFilter,
    "purpose-map": Target,
    "browser-bridge": Globe,
    "notification-tests": Bell,
    "perf-analysis": Gauge,
};

const NAV_ITEM_CLASS =
    "settings-nav-item w-full flex items-center gap-2.5 px-2.5 py-1.5 text-[12.5px] text-left";

interface NavResult {
    id: SettingsDestination;
    label: string;
    icon: ComponentType<{ className?: string }>;
    /** The query matched the page's subtitle or keywords rather than its
     *  name, so what the person is looking for may sit under Advanced. */
    revealAdvanced: boolean;
}

export interface SettingsNavProps {
    activePage: SettingsDestination;
    /** `{ advanced: true }` when a search result should open with Advanced
     *  expanded; each such selection expands it again, even on the page
     *  already open after the person collapsed it. */
    onSelect: (id: SettingsDestination, options?: SettingsOpenOptions) => void;
    /** Developer tools are on: by default in staging and dev builds, and
     *  Cmd/Ctrl+Shift+Option+D turns them on or off. */
    showDevTools: boolean;
}

/**
 * The Settings sidebar, built from the registry: a search field, the pages
 * grouped into labelled sections (each heading carries a one-line
 * explanation), the collapsible Extensions group, a divider before Help and
 * about, and the Developer tools group while it is on. The active row is
 * shown by its rounded background only.
 */
export function SettingsNav({ activePage, onSelect, showDevTools }: SettingsNavProps) {
    const { t } = useTranslation("settings");
    const aoyTag = useAoyNavTag();
    const [query, setQuery] = useState("");

    const activeSection: SettingsSectionId | null = isSettingsPageId(activePage)
        ? getSettingsPage(activePage).section
        : null;
    const [expanded, setExpanded] = useState<Partial<Record<SettingsSectionId, boolean>>>(() =>
        activeSection ? { [activeSection]: true } : {},
    );
    const [devToolsOpen, setDevToolsOpen] = useState(() => isDevToolsTopicId(activePage));

    // A deep link or a search result can land inside a collapsed group: open it.
    useEffect(() => {
        if (!activeSection) return;
        setExpanded((prev) => (prev[activeSection] ? prev : { ...prev, [activeSection]: true }));
    }, [activeSection]);
    useEffect(() => {
        if (isDevToolsTopicId(activePage)) setDevToolsOpen(true);
    }, [activePage]);

    const results = useMemo<NavResult[]>(() => {
        if (!query.trim()) return [];
        const translate = (key: string) => t(key as never) as string;
        const needle = query.trim().toLowerCase();
        const pages: NavResult[] = searchSettingsPages(query, translate).map((entry) => ({
            id: entry.id,
            label: translate(entry.labelKey),
            icon: entry.icon,
            revealAdvanced: !settingsPageNameMatches(entry, query, translate),
        }));
        if (!showDevTools) return pages;
        const devPages: NavResult[] = DEV_TOOLS_TOPIC_IDS.map((id) => ({
            id,
            label: translate(`topics.${id}`),
            icon: DEV_TOOLS_TOPIC_ICONS[id],
            revealAdvanced: false,
        })).filter((entry) => entry.label.toLowerCase().includes(needle));
        return [...pages, ...devPages];
    }, [query, showDevTools, t]);

    // A match on a page's keywords often names something under its Advanced
    // section, so the page opens with Advanced expanded for that visit.
    const openResult = (result: NavResult) => {
        setQuery("");
        if (result.revealAdvanced) onSelect(result.id, { advanced: true });
        else onSelect(result.id);
    };

    const renderPageRow = (entry: SettingsPageEntry) => (
        <button
            key={entry.id}
            type="button"
            onClick={() => onSelect(entry.id)}
            data-active={activePage === entry.id}
            aria-current={activePage === entry.id ? "page" : undefined}
            className={NAV_ITEM_CLASS}
        >
            <entry.icon className="h-4 w-4 shrink-0" />
            {entry.id === "your-agent" && aoyTag ? (
                <span className="flex min-w-0 flex-1 items-center gap-2">
                    <span className="truncate">{t("aoy.aoy")}</span>
                    <span className="settings-nav-soon settings-nav-tag" data-testid="aoy-nav-tag">
                        {t(aoyTag === "on" ? "aoy.tagOn" : aoyTag === "paused" ? "aoy.tagPaused" : "aoy.tagSetup")}
                    </span>
                </span>
            ) : entry.comingSoon ? (
                <span className="flex min-w-0 flex-1 flex-col items-start gap-0.5">
                    <span className="max-w-full truncate">{t(entry.labelKey as never)}</span>
                    <span className="settings-nav-soon">{t("nav.soon")}</span>
                </span>
            ) : (
                <span className="flex-1 truncate">{t(entry.labelKey as never)}</span>
            )}
        </button>
    );

    const renderSection = (section: SettingsSection, index: number) => {
        const pages = pagesInSection(section.id);
        if (pages.length === 0) return null;
        const spacing = index === 0 ? "" : section.dividerBefore ? "mt-3 pt-3 settings-nav-group-divider" : "mt-4";
        const heading = section.headingKey ? t(section.headingKey as never) : null;
        const explanation = section.explanationKey ? t(section.explanationKey as never) : null;
        const explanationNode = explanation ? (
            <span className="settings-nav-label block text-[11px] leading-[1.4] font-normal normal-case">
                {explanation}
            </span>
        ) : null;

        if (section.collapsible && heading) {
            const open = Boolean(expanded[section.id]);
            return (
                <div key={section.id} className={spacing}>
                    <Collapsible
                        open={open}
                        onOpenChange={(next) => setExpanded((prev) => ({ ...prev, [section.id]: next }))}
                    >
                        <CollapsibleTrigger asChild>
                            <button
                                type="button"
                                className="settings-nav-item w-full flex flex-col items-stretch gap-0.5 px-2.5 py-1.5 text-left"
                            >
                                <span className="flex items-center gap-2">
                                    <span className="settings-nav-label flex-1 text-[11px] uppercase">
                                        {heading}
                                    </span>
                                    <ChevronDown
                                        aria-hidden
                                        className={`h-3.5 w-3.5 shrink-0 transition-transform ${open ? "rotate-180" : ""}`}
                                    />
                                </span>
                                {explanationNode}
                            </button>
                        </CollapsibleTrigger>
                        <CollapsibleContent>
                            <div className="flex flex-col gap-0.5 mt-1">{pages.map(renderPageRow)}</div>
                        </CollapsibleContent>
                    </Collapsible>
                </div>
            );
        }

        return (
            <div key={section.id} className={spacing}>
                {heading && (
                    <div className="px-2.5 pb-1.5 flex flex-col gap-0.5">
                        <span className="settings-nav-label text-[11px] uppercase">{heading}</span>
                        {explanationNode}
                    </div>
                )}
                <div className="flex flex-col gap-0.5">{pages.map(renderPageRow)}</div>
            </div>
        );
    };

    const searching = query.trim().length > 0;

    return (
        <nav className="settings-nav w-[164px] md:w-[212px] shrink-0 flex flex-col">
            <div className="window-drag px-3 pt-4 pb-3 shrink-0">
                <div className="relative">
                    <Search
                        aria-hidden
                        className="pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted"
                    />
                    <input
                        type="search"
                        value={query}
                        onChange={(event) => setQuery(event.target.value)}
                        onKeyDown={(event) => {
                            if (event.key === "Enter" && results.length > 0) {
                                event.preventDefault();
                                openResult(results[0]);
                            }
                        }}
                        placeholder={t("nav.searchPlaceholder")}
                        aria-label={t("nav.searchPlaceholder")}
                        className="h-8 w-full border pl-8 pr-2 text-[12.5px] placeholder:text-muted/70"
                    />
                </div>
            </div>
            <ScrollArea className="flex-1">
                <div className="pb-3 px-3 flex flex-col">
                    {searching ? (
                        results.length > 0 ? (
                            <div className="flex flex-col gap-0.5">
                                {results.map((result) => (
                                    <button
                                        key={result.id}
                                        type="button"
                                        onClick={() => openResult(result)}
                                        data-active={activePage === result.id}
                                        className={NAV_ITEM_CLASS}
                                    >
                                        <result.icon className="h-4 w-4 shrink-0" />
                                        <span className="flex-1 truncate">{result.label}</span>
                                    </button>
                                ))}
                            </div>
                        ) : (
                            <p className="px-2.5 py-1.5 text-[12px] text-muted">{t("nav.noResults")}</p>
                        )
                    ) : (
                        <>
                            {SETTINGS_SECTIONS.map(renderSection)}

                            {showDevTools && (
                                <div className="mt-3 pt-3 settings-nav-group-divider">
                                    <Collapsible open={devToolsOpen} onOpenChange={setDevToolsOpen}>
                                        <CollapsibleTrigger asChild>
                                            <button
                                                type="button"
                                                data-active={isDevToolsTopicId(activePage) && !devToolsOpen}
                                                className={NAV_ITEM_CLASS}
                                            >
                                                <Wrench className="h-4 w-4 shrink-0" />
                                                <span className="flex-1">{t("topics.devTools")}</span>
                                                <ChevronDown
                                                    aria-hidden
                                                    className={`h-3.5 w-3.5 shrink-0 transition-transform ${
                                                        devToolsOpen ? "rotate-180" : ""
                                                    }`}
                                                />
                                            </button>
                                        </CollapsibleTrigger>
                                        <CollapsibleContent>
                                            <div className="flex flex-col gap-0.5 ml-3 mt-0.5">
                                                {DEV_TOOLS_GROUPS.map((group, index) => (
                                                    <div
                                                        key={group.id}
                                                        className={index === 0 ? "pb-1" : "border-t border-border/60 py-1"}
                                                    >
                                                        <div className="px-2.5 pb-1 pt-1 text-[9px] font-semibold text-muted">
                                                            {t(`devTools.groups.${group.id}` as never)}
                                                        </div>
                                                        {group.topics.map((topicId) => {
                                                            const TopicIcon = DEV_TOOLS_TOPIC_ICONS[topicId];
                                                            return (
                                                                <button
                                                                    key={topicId}
                                                                    type="button"
                                                                    onClick={() => onSelect(topicId)}
                                                                    data-active={activePage === topicId}
                                                                    aria-current={activePage === topicId ? "page" : undefined}
                                                                    className={NAV_ITEM_CLASS}
                                                                >
                                                                    <TopicIcon className="h-4 w-4 shrink-0" />
                                                                    <span className="min-w-0 flex-1 whitespace-normal leading-4">
                                                                        {t(`topics.${topicId}` as never)}
                                                                    </span>
                                                                </button>
                                                            );
                                                        })}
                                                    </div>
                                                ))}
                                            </div>
                                        </CollapsibleContent>
                                    </Collapsible>
                                </div>
                            )}
                        </>
                    )}
                </div>
            </ScrollArea>
        </nav>
    );
}
