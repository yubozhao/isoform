import { describe, expect, it } from "vitest";

import enSettings from "@/i18n/locales/en/settings.json";
import zhSettings from "@/i18n/locales/zh-CN/settings.json";
import { DEV_TOOLS_TOPIC_IDS } from "@/components/dev-tools-navigation";
import {
    CHAT_APP_IDS,
    DEFAULT_SETTINGS_PAGE,
    LEGACY_SETTINGS_TOPIC_IDS,
    SETTINGS_PAGES,
    SETTINGS_PAGE_IDS,
    SETTINGS_SECTIONS,
    pagesInSection,
    readSettingsOpenEvent,
    resolveSettingsTopic,
    searchSettingsPages,
    settingsPageNameMatches,
    splitSettingsKeywords,
} from "@/components/settings/settings-registry";

type Locale = Record<string, unknown>;

function lookup(locale: Locale, key: string): unknown {
    return key.split(".").reduce<unknown>(
        (node, part) =>
            node && typeof node === "object" ? (node as Record<string, unknown>)[part] : undefined,
        locale,
    );
}

const LOCALES: [string, Locale][] = [
    ["en", enSettings as Locale],
    ["zh-CN", zhSettings as Locale],
];

// Every id the old SettingsTopic union accepted, including the ones that
// never rendered. Each must still open something sensible.
const OLD_TOPIC_IDS = [
    "general",
    "account",
    "subscription",
    "read-aloud",
    "appearance",
    "language",
    "editor",
    "keybindings",
    "security",
    "activity",
    "skills",
    "mcp",
    "use-yansu-anywhere",
    "iphone",
    "app-access",
    "google",
    "slackbot",
    "notion",
    "github",
    "linear",
    "feishu-agent",
    "slack",
    "teams",
    "feishu",
    "lark",
    "ringcentral",
    "discord",
    "telegram",
    "wechat",
    "memory",
    "ai-provider",
    "wsl",
    "ssh",
    "triage",
    "activity-pipeline",
    "shadow-agent",
    "purpose-map",
    "perf-analysis",
    "handoff-monitor",
    "audio-capture",
    "notifications",
    "onboarding",
    "browser-bridge",
    "cursor-overlay",
    "debug",
    "ax-parser",
    "support",
    "about",
];

const DESTINATIONS = new Set<string>([...SETTINGS_PAGE_IDS, ...DEV_TOOLS_TOPIC_IDS]);

// Emoji and pictographic symbols are not allowed in UI copy (CLAUDE.md rule 9).
const PICTOGRAPHIC = /\p{Extended_Pictographic}/u;

describe("settings registry", () => {
    it("resolves every old topic id to a registered page", () => {
        for (const id of OLD_TOPIC_IDS) {
            const resolved = resolveSettingsTopic(id);
            expect(DESTINATIONS.has(resolved.page), `${id} -> ${resolved.page}`).toBe(true);
        }
        for (const id of LEGACY_SETTINGS_TOPIC_IDS) {
            expect(SETTINGS_PAGE_IDS as readonly string[]).toContain(resolveSettingsTopic(id).page);
        }
    });

    it("maps legacy ids to the page that now holds their content", () => {
        const expected: Record<string, string> = {
            appearance: "appearance",
            language: "general",
            account: "general",
            support: "about",
            subscription: "ai-provider",
        };
        for (const [id, page] of Object.entries(expected)) {
            expect(resolveSettingsTopic(id)).toEqual({ page });
        }
        // The Using apps page went with the bundled computer-use driver; its
        // old links open What Isoform sees, where Settings opens.
        for (const id of ["app-access", "cursor-overlay", "google", "slackbot", "notion", "github", "linear", "feishu-agent"]) {
            expect(resolveSettingsTopic(id)).toEqual({ page: "activity" });
        }
        // Keyboard shortcuts and Read aloud now sit under General > Advanced.
        expect(resolveSettingsTopic("keybindings")).toEqual({ page: "general", revealAdvanced: true });
        expect(resolveSettingsTopic("read-aloud")).toEqual({ page: "general", revealAdvanced: true });
    });

    it("lets a deep link ask for Advanced and an anchor", () => {
        expect(resolveSettingsTopic("ai-provider", { advanced: true })).toEqual({
            page: "ai-provider",
            revealAdvanced: true,
        });
        expect(resolveSettingsTopic("memory", { advanced: true, anchor: "search-index" })).toEqual({
            page: "memory",
            revealAdvanced: true,
            anchor: "search-index",
        });
        expect(resolveSettingsTopic("slack", { advanced: true })).toEqual({
            page: "chat-apps",
            chatApp: "slack",
            revealAdvanced: true,
        });
        expect(resolveSettingsTopic("iphone", { advanced: false })).toEqual({ page: "iphone" });
        expect(resolveSettingsTopic("iphone", {})).toEqual({ page: "iphone" });
    });

    it("reads the settings:open event payload", () => {
        expect(readSettingsOpenEvent("iphone")).toEqual({ topic: "iphone", options: {} });
        expect(readSettingsOpenEvent("ai-provider", { advanced: true })).toEqual({
            topic: "ai-provider",
            options: { advanced: true },
        });
        expect(readSettingsOpenEvent({ topic: "memory", advanced: true, anchor: "search-index" })).toEqual({
            topic: "memory",
            options: { advanced: true, anchor: "search-index" },
        });
        // Missing or malformed parts are dropped rather than trusted.
        expect(readSettingsOpenEvent(undefined)).toEqual({ topic: undefined, options: {} });
        expect(readSettingsOpenEvent("")).toEqual({ topic: undefined, options: {} });
        expect(readSettingsOpenEvent("iphone", { advanced: "yes", anchor: 4 })).toEqual({
            topic: "iphone",
            options: {},
        });
        expect(readSettingsOpenEvent({ advanced: true })).toEqual({ topic: undefined, options: { advanced: true } });
    });

    it("opens a chat app as a drill-in on the Chat apps page", () => {
        for (const app of CHAT_APP_IDS) {
            expect(resolveSettingsTopic(app)).toEqual({ page: "chat-apps", chatApp: app });
        }
    });

    it("keeps page ids and Developer tools ids pointing at themselves", () => {
        for (const id of SETTINGS_PAGE_IDS) {
            expect(resolveSettingsTopic(id)).toEqual({ page: id });
        }
        for (const id of DEV_TOOLS_TOPIC_IDS) {
            expect(resolveSettingsTopic(id)).toEqual({ page: id });
        }
    });

    it("falls back to What Isoform sees for unknown or missing ids", () => {
        for (const id of ["editor", "teams", "ringcentral", "wsl", "ssh", "onboarding", "debug", "nope", "", null, undefined]) {
            expect(resolveSettingsTopic(id)).toEqual({ page: "activity" });
        }
    });

    it("gives every page exactly one entry and a section that exists", () => {
        expect(SETTINGS_PAGES.map((entry) => entry.id)).toEqual([...SETTINGS_PAGE_IDS]);
        const sectionIds = SETTINGS_SECTIONS.map((section) => section.id);
        for (const entry of SETTINGS_PAGES) {
            expect(sectionIds).toContain(entry.section);
        }
        for (const section of SETTINGS_SECTIONS) {
            expect(pagesInSection(section.id).length, section.id).toBeGreaterThan(0);
        }
        // No alias is claimed twice, and no alias shadows a real page.
        const aliases = SETTINGS_PAGES.flatMap((entry) => [...entry.aliases]);
        expect(new Set(aliases).size).toBe(aliases.length);
        for (const alias of aliases) {
            expect(SETTINGS_PAGE_IDS as readonly string[]).not.toContain(alias);
        }
    });

    it("lays out the sidebar in the agreed order", () => {
        expect(SETTINGS_SECTIONS.map((section) => [section.id, pagesInSection(section.id).map((p) => p.id)])).toEqual([
            // computer-settings v1.0.0 rule 1: What Isoform sees, Closed Eyes, Your data,
            // Your agents, Your phone, Briefs and notifications.
            ["basics", ["activity", "closed-eyes", "your-data", "ai-provider", "your-agent", "iphone", "notifications"]],
            ["personal-ai", ["memory", "security", "storage"]],
            ["app", ["overview", "general", "profile", "appearance"]],
            ["extensions", ["plugins", "skills", "mcp", "chat-apps", "use-yansu-anywhere"]],
            ["help", ["about"]],
        ]);
        const extensions = SETTINGS_SECTIONS.find((section) => section.id === "extensions");
        expect(extensions?.collapsible).toBe(true);
        expect(SETTINGS_SECTIONS.find((section) => section.id === "help")?.dividerBefore).toBe(true);
        // Settings opens on What Isoform sees, its first page.
        expect(SETTINGS_PAGES[0].id).toBe("activity");
        expect(DEFAULT_SETTINGS_PAGE).toBe("activity");
    });

    it.each(LOCALES)("gives Overview a page title of its own beside its sidebar label in %s", (_name, locale) => {
        const overview = SETTINGS_PAGES.find((entry) => entry.id === "overview")!;
        expect(overview.titleKey).toBe("pages.overview.title");
        const title = lookup(locale, overview.titleKey!);
        expect(typeof title).toBe("string");
        expect(title).not.toBe(lookup(locale, overview.labelKey));
        // Overview and Agent of You read differently from their sidebar rows (v2.0.0 copies Overview's pattern).
        expect(SETTINGS_PAGES.filter((entry) => entry.titleKey).map((entry) => entry.id)).toEqual(["your-agent", "overview"]);
    });

    it.each(LOCALES)("has a label, one-sentence subtitle and keywords for every page in %s", (_name, locale) => {
        for (const entry of SETTINGS_PAGES) {
            for (const key of [entry.labelKey, entry.subtitleKey, entry.keywordsKey]) {
                const value = lookup(locale, key);
                expect(typeof value, key).toBe("string");
                expect((value as string).trim(), key).not.toBe("");
                expect(PICTOGRAPHIC.test(value as string), key).toBe(false);
            }
            const subtitle = lookup(locale, entry.subtitleKey) as string;
            // One sentence: a single terminal full stop and no sentence break inside.
            expect(subtitle, entry.subtitleKey).toMatch(/[.。]$/);
            expect(subtitle.slice(0, -1), entry.subtitleKey).not.toMatch(/[.。] /);
            expect(subtitle.slice(0, -1), entry.subtitleKey).not.toMatch(/。/);
            expect(splitSettingsKeywords(lookup(locale, entry.keywordsKey) as string).length).toBeGreaterThan(2);
        }
    });

    it.each(LOCALES)("has a heading and an explanation for every labelled section in %s", (_name, locale) => {
        for (const section of SETTINGS_SECTIONS) {
            for (const key of [section.headingKey, section.explanationKey]) {
                if (!key) continue;
                const value = lookup(locale, key);
                expect(typeof value, key).toBe("string");
                expect((value as string).trim(), key).not.toBe("");
            }
            expect(Boolean(section.headingKey)).toBe(Boolean(section.explanationKey));
        }
    });

    it.each(LOCALES)("names and describes every chat app in %s", (_name, locale) => {
        for (const app of ["slack", "discord", "telegram", "wechat", "feishu"]) {
            expect(typeof lookup(locale, `chatApps.apps.${app}.name`)).toBe("string");
            expect(typeof lookup(locale, `chatApps.apps.${app}.description`)).toBe("string");
        }
        for (const brand of ["feishu", "lark"]) {
            expect(typeof lookup(locale, `chatApps.brands.${brand}`)).toBe("string");
        }
    });

    it("uses the product name Isoform in copy", () => {
        const copy = JSON.stringify([enSettings.nav, enSettings.pages, enSettings.chatApps]);
        expect(copy).not.toMatch(/Yansu/);
        expect(copy).toMatch(/Isoform/);
    });

    it("finds pages by label, subtitle and keywords", () => {
        const translate = (key: string) => lookup(enSettings as Locale, key) as string;
        expect(searchSettingsPages("theme", translate).map((p) => p.id)).toEqual(["general", "appearance"]);
        expect(searchSettingsPages("texture", translate).map((p) => p.id)).toEqual([]);
        expect(searchSettingsPages("MCP", translate).map((p) => p.id)).toContain("mcp");
        expect(searchSettingsPages("subscription", translate).map((p) => p.id)).toContain("ai-provider");
        expect(searchSettingsPages("Slack", translate).map((p) => p.id)).toContain("chat-apps");
        expect(searchSettingsPages("   ", translate)).toEqual([]);

        const translateZh = (key: string) => lookup(zhSettings as Locale, key) as string;
        expect(searchSettingsPages("纹理", translateZh).map((p) => p.id)).toEqual([]);
        expect(searchSettingsPages("主题", translateZh).map((p) => p.id)).toEqual(["general", "appearance"]);
    });

    it("tells a match on the page name from a match on its details", () => {
        const translate = (key: string) => lookup(enSettings as Locale, key) as string;
        const iphone = SETTINGS_PAGES.find((entry) => entry.id === "iphone")!;
        expect(settingsPageNameMatches(iphone, " phone ", translate)).toBe(true);
        expect(settingsPageNameMatches(iphone, "relay", translate)).toBe(false);
        expect(settingsPageNameMatches(iphone, "  ", translate)).toBe(false);
    });

    it("lists pages found by name before pages that only mention the term", () => {
        const translate = (key: string) => lookup(enSettings as Locale, key) as string;
        const phone = searchSettingsPages("Phone", translate).map((p) => p.id);
        expect(phone[0]).toBe("iphone");
        expect(phone).toContain("notifications");
        expect(searchSettingsPages("privacy", translate)[0].id).toBe("security");
    });

    // Each page's keywords name what it keeps under Advanced, so the jargon
    // that lives only there still finds the page (in both languages).
    it.each([
        ["en", "Search index", "memory"],
        ["en", "embeddings", "memory"],
        ["en", "Prediction confidence", "memory"],
        ["en", "Keyboard shortcuts", "general"],
        ["en", "tool steps", "general"],
        ["en", "Where notifications go", "notifications"],
        ["en", "sandbox", "ai-provider"],
        ["en", "Who handles what", "ai-provider"],
        ["en", "magpie", "ai-provider"],
        ["en", "Server address", "iphone"],
        ["en", "relay", "iphone"],
        ["en", "Storage limit", "storage"],
        ["en", "free up space", "storage"],
        ["en", "models", "storage"],
        ["en", "permissions", "security"],
        ["en", "Capture status", "activity"],
        ["en", "environment variables", "mcp"],
        ["en", "Where skills are stored", "skills"],
        ["en", "release notes", "about"],
        ["en", "worth a look", "overview"],
        ["en", "Pause for 1 hour", "overview"],
        ["zh-CN", "搜索索引", "memory"],
        ["zh-CN", "任务分工", "ai-provider"],
        ["zh-CN", "服务器地址", "iphone"],
        ["zh-CN", "提醒发到哪里", "notifications"],
        ["zh-CN", "存储上限", "storage"],
        ["zh-CN", "模型", "storage"],
        ["zh-CN", "环境变量", "mcp"],
        ["zh-CN", "值得一看", "overview"],
    ])("finds %s '%s' on %s", (lang, query, page) => {
        const locale = (lang === "en" ? enSettings : zhSettings) as Locale;
        const translate = (key: string) => lookup(locale, key) as string;
        expect(searchSettingsPages(query, translate).map((p) => p.id)).toContain(page);
    });

});
