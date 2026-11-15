import type { ComponentType } from "react";
import {
    Activity,
    Palette,
    Bell,
    Blocks,
    Brain,
    Cpu,
    HardDrive,
    Info,
    LifeBuoy,
    MessagesSquare,
    Package,
    Puzzle,
    Server,
    Settings,
    User,
    ShieldCheck,
    Smartphone,
    Sparkles,
} from "lucide-react";
import {
    DEV_TOOLS_TOPIC_IDS,
    type DevToolsTopicId,
} from "@/components/dev-tools-navigation";

// ── Settings registry ──
//
// One place that says which pages Settings has, how the sidebar groups them,
// what each page is called and what it is for. The sidebar, the page header,
// the sidebar search and the command palette all read from here, so a page is
// added or renamed in exactly one spot.
//
// Page ids are stable: they are what deep links (openSettings, the
// `settings:open` event, composer chips, chat-error buttons) pass in. Labels
// can change freely; ids never do. Ids that no longer have their own page are
// kept as aliases and resolved by resolveSettingsTopic().

export const SETTINGS_SECTION_IDS = [
    "basics",
    "personal-ai",
    "devices",
    "extensions",
    "help",
] as const;

export type SettingsSectionId = (typeof SETTINGS_SECTION_IDS)[number];

export const SETTINGS_PAGE_IDS = [
    "overview",
    "general",
    "profile",
    "appearance",
    "notifications",
    "your-agent",
    "ai-provider",
    "activity",
    "memory",
    "security",
    "storage",
    "iphone",
    "plugins",
    "skills",
    "mcp",
    "chat-apps",
    "use-yansu-anywhere",
    "about",
] as const;

export type SettingsPageId = (typeof SETTINGS_PAGE_IDS)[number];

/** Chat apps that open as a drill-in on the Chat apps page. Feishu and Lark
 *  share one row but keep separate ids because their state is per brand. */
export const CHAT_APP_IDS = [
    "slack",
    "discord",
    "telegram",
    "wechat",
    "feishu",
    "lark",
] as const;

export type ChatAppId = (typeof CHAT_APP_IDS)[number];

/** Ids that used to be their own Settings page. Deep links that still use
 *  them land on the page that now holds that content. */
export const LEGACY_SETTINGS_TOPIC_IDS = [
    "language",
    "read-aloud",
    "keybindings",
    "account",
    "subscription",
    "support",
    "text-messages",
    "phone-calls",
    ...CHAT_APP_IDS,
] as const;

export type LegacySettingsTopic = (typeof LEGACY_SETTINGS_TOPIC_IDS)[number];

/** Legacy ids whose content now sits under its page's Advanced section:
 *  they open that page with Advanced expanded. */
const LEGACY_TOPICS_UNDER_ADVANCED: readonly LegacySettingsTopic[] = ["keybindings", "read-aloud"];

/** Every id openSettings() accepts: a page, a legacy alias or a Developer
 *  tools page. */
export type SettingsTopic = SettingsPageId | LegacySettingsTopic | DevToolsTopicId;

/** How a deep link asks Settings to open its page: openSettings(topic,
 *  options) and the `settings:open` event both take these. */
export interface SettingsOpenOptions {
    /** Open the page with its Advanced section expanded, for a link to
     *  something kept there (your own API key, the search index, the server
     *  address). */
    advanced?: boolean;
    /** Scroll to the element on the page marked with this
     *  `data-settings-anchor` value (settingsAnchor() in settings-advanced)
     *  once it renders. With `advanced` and no anchor, Settings scrolls to the
     *  Advanced section itself. */
    anchor?: string;
}

/** Where Settings actually navigates: a registered page or a Developer tools
 *  page. */
export type SettingsDestination = SettingsPageId | DevToolsTopicId;

export interface SettingsSection {
    id: SettingsSectionId;
    /** settings-namespace key of the heading shown above the section rows. */
    headingKey?: string;
    /** settings-namespace key of the one-sentence explanation under the heading. */
    explanationKey?: string;
    /** Collapsed by default; opens on click or when the active page is inside. */
    collapsible?: boolean;
    /** Draws a thin divider above the section. */
    dividerBefore?: boolean;
}

export interface SettingsPageEntry {
    id: SettingsPageId;
    section: SettingsSectionId;
    /** settings-namespace key of the sidebar label and page title. */
    labelKey: string;
    /** settings-namespace key of a page title that differs from the sidebar
     *  label (Overview reads "How Isoform works for you"). */
    titleKey?: string;
    /** settings-namespace key of the one plain-language sentence under the
     *  page title that says what the page is for. */
    subtitleKey: string;
    /** settings-namespace key of a comma-separated list of extra search words. */
    keywordsKey: string;
    icon: ComponentType<{ className?: string }>;
    /** Older ids that now open this page. */
    aliases: readonly LegacySettingsTopic[];
    /** Shows a Soon tag beside the label; the page itself is a preview. */
    comingSoon?: boolean;
}

export const SETTINGS_SECTIONS: readonly SettingsSection[] = [
    { id: "basics" },
    {
        id: "personal-ai",
        headingKey: "nav.sections.personalAi.heading",
        explanationKey: "nav.sections.personalAi.explanation",
    },
    { id: "devices" },
    {
        id: "extensions",
        headingKey: "nav.sections.extensions.heading",
        explanationKey: "nav.sections.extensions.explanation",
        collapsible: true,
    },
    { id: "help", dividerBefore: true },
];

function page(
    id: SettingsPageId,
    section: SettingsSectionId,
    icon: SettingsPageEntry["icon"],
    aliases: readonly LegacySettingsTopic[] = [],
): SettingsPageEntry {
    return {
        id,
        section,
        labelKey: `pages.${id}.label`,
        subtitleKey: `pages.${id}.subtitle`,
        keywordsKey: `pages.${id}.keywords`,
        icon,
        aliases,
    };
}

export const SETTINGS_PAGES: readonly SettingsPageEntry[] = [
    { ...page("overview", "basics", Info, ["text-messages", "phone-calls"]), titleKey: "pages.overview.title" },
    page("general", "basics", Settings, [
        "language",
        "read-aloud",
        "keybindings",
        "account",
    ]),
    page("profile", "basics", User),
    page("appearance", "basics", Palette),
    page("notifications", "basics", Bell),
    { ...page("your-agent", "basics", Sparkles), titleKey: "pages.your-agent.title", comingSoon: true },
    page("ai-provider", "personal-ai", Cpu, ["subscription"]),
    page("activity", "personal-ai", Activity),
    page("memory", "personal-ai", Brain),
    page("security", "personal-ai", ShieldCheck),
    page("storage", "personal-ai", HardDrive),
    page("iphone", "devices", Smartphone),
    page("plugins", "extensions", Puzzle),
    page("skills", "extensions", Package),
    page("mcp", "extensions", Server),
    page("chat-apps", "extensions", MessagesSquare, CHAT_APP_IDS),
    page("use-yansu-anywhere", "extensions", Blocks),
    page("about", "help", LifeBuoy, ["support"]),
];

export const DEFAULT_SETTINGS_PAGE: SettingsPageId = "overview";

export function isSettingsPageId(id: string): id is SettingsPageId {
    return (SETTINGS_PAGE_IDS as readonly string[]).includes(id);
}

export function isChatAppId(id: string): id is ChatAppId {
    return (CHAT_APP_IDS as readonly string[]).includes(id);
}

export function isDevToolsTopicId(id: string): id is DevToolsTopicId {
    return (DEV_TOOLS_TOPIC_IDS as readonly string[]).includes(id);
}

export function getSettingsPage(id: SettingsPageId): SettingsPageEntry {
    return SETTINGS_PAGES.find((entry) => entry.id === id) ?? SETTINGS_PAGES[0];
}

export function getSettingsSection(id: SettingsSectionId): SettingsSection {
    return SETTINGS_SECTIONS.find((section) => section.id === id) ?? SETTINGS_SECTIONS[0];
}

export function pagesInSection(id: SettingsSectionId): SettingsPageEntry[] {
    return SETTINGS_PAGES.filter((entry) => entry.section === id);
}

export interface ResolvedSettingsTopic {
    page: SettingsDestination;
    /** Set when the id named one chat app: the Chat apps page opens its setup. */
    chatApp?: ChatAppId;
    /** Set when the link or the sidebar search asked for something the page
     *  keeps under Advanced: the page opens with its Advanced section
     *  expanded. */
    revealAdvanced?: boolean;
    /** `data-settings-anchor` value to scroll to once the page renders. */
    anchor?: string;
}

function resolvePage(id: string | null | undefined): ResolvedSettingsTopic {
    if (!id) return { page: DEFAULT_SETTINGS_PAGE };
    if (isSettingsPageId(id)) return { page: id };
    if (isChatAppId(id)) return { page: "chat-apps", chatApp: id };
    const owner = SETTINGS_PAGES.find((entry) =>
        (entry.aliases as readonly string[]).includes(id),
    );
    if (owner) {
        return (LEGACY_TOPICS_UNDER_ADVANCED as readonly string[]).includes(id)
            ? { page: owner.id, revealAdvanced: true }
            : { page: owner.id };
    }
    if (isDevToolsTopicId(id)) return { page: id };
    return { page: DEFAULT_SETTINGS_PAGE };
}

/**
 * Maps any id a caller may pass to openSettings() to the page that shows it.
 * Registered pages and Developer tools pages map to themselves, legacy ids map
 * to the page that absorbed them (a chat app keeps its id as the drill-in,
 * and Keyboard shortcuts and Read aloud open General with Advanced
 * expanded), and anything unknown falls back to Overview. `options` can ask
 * for Advanced expanded and an anchor to scroll to.
 */
export function resolveSettingsTopic(
    id: string | null | undefined,
    options: SettingsOpenOptions = {},
): ResolvedSettingsTopic {
    const resolved = resolvePage(id);
    if (options.advanced) resolved.revealAdvanced = true;
    if (options.anchor) resolved.anchor = options.anchor;
    return resolved;
}

function readOpenOptions(raw: unknown): SettingsOpenOptions {
    if (!raw || typeof raw !== "object") return {};
    const { advanced, anchor } = raw as Record<string, unknown>;
    const options: SettingsOpenOptions = {};
    if (advanced === true) options.advanced = true;
    if (typeof anchor === "string" && anchor) options.anchor = anchor;
    return options;
}

/**
 * Reads the untyped `settings:open` event payload. Callers emit the topic and,
 * optionally, the options after it:
 * `EventsEmit("settings:open", "ai-provider", { advanced: true })`. One object
 * `{ topic, advanced, anchor }` works too. Anything else is ignored, and a
 * missing topic opens Overview.
 */
export function readSettingsOpenEvent(
    payload: unknown,
    extra?: unknown,
): { topic?: SettingsTopic; options: SettingsOpenOptions } {
    const fromObject = Boolean(payload) && typeof payload === "object";
    const rawTopic = fromObject ? (payload as Record<string, unknown>).topic : payload;
    const topic = typeof rawTopic === "string" && rawTopic ? (rawTopic as SettingsTopic) : undefined;
    return { topic, options: readOpenOptions(fromObject ? payload : extra) };
}

/** Splits a translated keyword list ("theme, dark mode, ...") into terms. */
export function splitSettingsKeywords(raw: string): string[] {
    return raw
        .split(/[,，、]/)
        .map((term) => term.trim())
        .filter(Boolean);
}

/**
 * True when the query is part of the page's name. A page found by its
 * subtitle or keywords instead often keeps the match under Advanced, so the
 * sidebar search (and the command palette) open it with Advanced expanded.
 */
export function settingsPageNameMatches(
    entry: SettingsPageEntry,
    query: string,
    translate: (key: string) => string,
): boolean {
    const needle = query.trim().toLowerCase();
    return needle.length > 0 && translate(entry.labelKey).toLowerCase().includes(needle);
}

/**
 * Pages whose label, subtitle or keywords contain the query (case-insensitive).
 * Pages whose name matches come first, then the rest in registry order, so
 * "iPhone" opens the iPhone page even though Notifications also mentions it.
 * `translate` resolves a settings-namespace key. Keywords name what each page
 * keeps under Advanced too, so a search for a technical term (MCP, relay,
 * embedding, sandbox) still finds its page.
 */
export function searchSettingsPages(
    query: string,
    translate: (key: string) => string,
): SettingsPageEntry[] {
    const needle = query.trim().toLowerCase();
    if (!needle) return [];
    const byName: SettingsPageEntry[] = [];
    const byDetail: SettingsPageEntry[] = [];
    for (const entry of SETTINGS_PAGES) {
        if (settingsPageNameMatches(entry, query, translate)) {
            byName.push(entry);
            continue;
        }
        const details = [
            translate(entry.subtitleKey),
            ...splitSettingsKeywords(translate(entry.keywordsKey)),
        ];
        if (details.some((text) => text.toLowerCase().includes(needle))) byDetail.push(entry);
    }
    return [...byName, ...byDetail];
}
