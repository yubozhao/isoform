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
    opensKeysSheet,
    opensYouPage,
    pagesInSection,
    readSettingsOpenEvent,
    resolveSettingsTopic,
    searchSettingsPages,
    settingsPageNameMatches,
    splitSettingsKeywords,
    settingsPageShown,
    shownSettingsDestination,
    getSettingsPage,
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
    "overview",
    "profile",
    "text-messages",
    "phone-calls",
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

/** Subtitles design wrote as two sentences. */
const TWO_SENTENCE_SUBTITLES = new Set(["pages.activity.subtitle"]);

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
            // Settings batch D1 (answers v1.0.0 T2): General, Profile and
            // Appearance went into About; Read aloud and the text messages
            // channel into Briefs and notifications; Overview and phone calls
            // into What Isoform sees; Plugins, Skills and Tool servers into
            // Your agents. Plugins, Tool servers and Chat apps stay until the
            // switches only they hold have homes.
            general: "about",
            profile: "about",
            appearance: "about",
            language: "about",
            account: "about",
            support: "about",
            subscription: "ai-provider",
            skills: "ai-provider",
            // Settings batch D2: Privacy and Storage went into Your data (keep
            // times, the cap), About (diagnostic data, models) and What
            // Isoform sees (app lists, agents' history, permissions).
            security: "your-data",
            storage: "your-data",
            // Settings batch D3: App.tsx opens You › About you for `memory`
            // (opensYouPage); in Settings it falls back to What Isoform sees.
            memory: "activity",
            overview: "activity",
            "phone-calls": "activity",
            "text-messages": "notifications",
            "read-aloud": "notifications",
        };
        for (const [id, page] of Object.entries(expected)) {
            expect(resolveSettingsTopic(id)).toEqual({ page });
        }
        // The Using apps page went with the bundled computer-use driver; its
        // old links open What Isoform sees, where Settings opens.
        for (const id of ["app-access", "cursor-overlay", "google", "slackbot", "notion", "github", "linear", "feishu-agent"]) {
            expect(resolveSettingsTopic(id)).toEqual({ page: "activity" });
        }
        // Keyboard shortcuts is the Cmd+/ sheet: App.tsx opens it before
        // Settings resolves anything, so here the old id only falls back to
        // About.
        expect(opensKeysSheet("keybindings")).toBe(true);
        expect(opensKeysSheet("general")).toBe(false);
        expect(opensKeysSheet(undefined)).toBe(false);
        expect(resolveSettingsTopic("keybindings")).toEqual({ page: "about" });
    });

    it("keeps no page of its own for the sections settings batch D1 removed", () => {
        for (const id of ["overview", "general", "profile", "appearance", "skills"]) {
            expect(SETTINGS_PAGE_IDS as readonly string[], id).not.toContain(id);
            expect(LEGACY_SETTINGS_TOPIC_IDS as readonly string[], id).toContain(id);
            expect(SETTINGS_PAGE_IDS as readonly string[], id).toContain(resolveSettingsTopic(id).page);
        }
        expect(SETTINGS_SECTIONS.map((section) => section.id)).not.toContain("app");
    });

    it("keeps no Learning and memory page or Personal AI heading (settings batch D3)", () => {
        expect(SETTINGS_PAGE_IDS as readonly string[]).not.toContain("memory");
        expect(LEGACY_SETTINGS_TOPIC_IDS as readonly string[]).toContain("memory");
        expect(SETTINGS_SECTIONS.map((section) => section.id)).not.toContain("personal-ai");
        expect(opensYouPage("memory")).toBe(true);
        expect(opensYouPage("activity")).toBe(false);
        expect(opensYouPage(undefined)).toBe(false);
    });

    it("keeps no page of its own for Privacy and Storage (settings batch D2)", () => {
        for (const id of ["security", "storage"]) {
            expect(SETTINGS_PAGE_IDS as readonly string[], id).not.toContain(id);
            expect(LEGACY_SETTINGS_TOPIC_IDS as readonly string[], id).toContain(id);
            expect(resolveSettingsTopic(id)).toEqual({ page: "your-data" });
        }
    });

    it("lets a deep link ask for Advanced and an anchor", () => {
        expect(resolveSettingsTopic("ai-provider", { advanced: true })).toEqual({
            page: "ai-provider",
            revealAdvanced: true,
        });
        expect(resolveSettingsTopic("iphone", { advanced: true, anchor: "server-address" })).toEqual({
            page: "iphone",
            revealAdvanced: true,
            anchor: "server-address",
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

    it("keeps Plugins, Tool servers and Chat apps while they hold the only switch for a send", () => {
        // Plugins holds topic-finds and memory-dream (on by default, both send
        // to the assistant); Tool servers holds the MCP servers Chat calls;
        // Chat apps holds the bots Chat answers through.
        for (const id of ["plugins", "mcp", "chat-apps"]) {
            expect(SETTINGS_PAGE_IDS as readonly string[]).toContain(id);
        }
    });

    it("opens Your watch from its own id (settings batch W)", () => {
        expect(resolveSettingsTopic("your-watch")).toEqual({ page: "your-watch" });
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
            // answers v1.0.0 G1: What Isoform sees, Closed Eyes, Your data,
            // (Your AI,) Your agents, Agent of You, Your phone, Your watch
            // (batch W), Briefs and notifications, About. Your AI comes in
            // another batch; Plugins, Tool servers, Chat apps and Other AI
            // apps go later.
            ["basics", ["activity", "closed-eyes", "your-data", "ai-provider", "your-agent", "iphone", "your-watch", "notifications"]],
            ["extensions", ["plugins", "mcp", "chat-apps", "use-yansu-anywhere"]],
            ["help", ["about"]],
        ]);
        const extensions = SETTINGS_SECTIONS.find((section) => section.id === "extensions");
        expect(extensions?.collapsible).toBe(true);
        expect(SETTINGS_SECTIONS.find((section) => section.id === "help")?.dividerBefore).toBe(true);
        // Settings opens on What Isoform sees, its first page.
        expect(SETTINGS_PAGES[0].id).toBe("activity");
        expect(DEFAULT_SETTINGS_PAGE).toBe("activity");
    });

    it("gives only Agent of You a page title of its own", () => {
        expect(SETTINGS_PAGES.filter((entry) => entry.titleKey).map((entry) => entry.id)).toEqual(["your-agent"]);
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
            expect(subtitle, entry.subtitleKey).toMatch(/[.。]$/);
            // What Isoform sees' lead is design's two sentences (computer-settings
            // v1.0.0 sees.sub: "Every source, on one page. Off means nothing from
            // it is kept."), so it alone may have one sentence break.
            if (TWO_SENTENCE_SUBTITLES.has(entry.subtitleKey)) {
                expect(subtitle.slice(0, -1).split(/[.。] ?/).length, entry.subtitleKey).toBe(2);
            } else {
                // One sentence: a single terminal full stop and no sentence break inside.
                expect(subtitle.slice(0, -1), entry.subtitleKey).not.toMatch(/[.。] /);
                expect(subtitle.slice(0, -1), entry.subtitleKey).not.toMatch(/。/);
            }
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
        expect(searchSettingsPages("theme", translate).map((p) => p.id)).toEqual(["about"]);
        expect(searchSettingsPages("texture", translate).map((p) => p.id)).toEqual([]);
        expect(searchSettingsPages("MCP", translate).map((p) => p.id)).toContain("mcp");
        expect(searchSettingsPages("subscription", translate).map((p) => p.id)).toContain("ai-provider");
        expect(searchSettingsPages("Slack", translate).map((p) => p.id)).toContain("chat-apps");
        expect(searchSettingsPages("   ", translate)).toEqual([]);

        const translateZh = (key: string) => lookup(zhSettings as Locale, key) as string;
        expect(searchSettingsPages("纹理", translateZh).map((p) => p.id)).toEqual([]);
        expect(searchSettingsPages("主题", translateZh).map((p) => p.id)).toEqual(["about"]);
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
        expect(searchSettingsPages("privacy", translate).map((p) => p.id)).toContain("your-data");
    });

    // Each page's keywords name what it keeps under Advanced, so the jargon
    // that lives only there still finds the page (in both languages).
    it.each([
        ["en", "open at login", "about"],
        ["en", "your name", "about"],
        ["en", "read aloud", "notifications"],
        ["en", "quiet hours", "notifications"],
        ["en", "Where notifications go", "notifications"],
        ["en", "sandbox", "ai-provider"],
        ["en", "Who handles what", "ai-provider"],
        ["en", "magpie", "ai-provider"],
        ["en", "Server address", "iphone"],
        ["en", "relay", "iphone"],
        ["en", "Storage limit", "your-data"],
        ["en", "keep recordings", "your-data"],
        ["en", "models", "about"],
        ["en", "diagnostic data", "about"],
        ["en", "permissions", "activity"],
        ["en", "Capture status", "activity"],
        ["en", "environment variables", "mcp"],
        ["en", "release notes", "about"],
        ["zh-CN", "任务分工", "ai-provider"],
        ["zh-CN", "服务器地址", "iphone"],
        ["zh-CN", "提醒发到哪里", "notifications"],
        ["zh-CN", "存储上限", "your-data"],
        ["zh-CN", "模型", "about"],
        ["zh-CN", "诊断数据", "about"],
        ["zh-CN", "环境变量", "mcp"],
        ["zh-CN", "登录时打开", "about"],
        ["zh-CN", "朗读", "notifications"],
    ])("finds %s '%s' on %s", (lang, query, page) => {
        const locale = (lang === "en" ? enSettings : zhSettings) as Locale;
        const translate = (key: string) => lookup(locale, key) as string;
        expect(searchSettingsPages(query, translate).map((p) => p.id)).toContain(page);
    });


    describe("pages of a turned-off feature (scope cut A5)", () => {
        const off = { chat: false };
        const on = { chat: true };
        const translate = (key: string) => key.split(".").reduce<unknown>((node, part) => (node as Record<string, unknown>)?.[part], enSettings) as string;

        it("hides Chat apps while Chat is off and keeps every other page", () => {
            expect(settingsPageShown(getSettingsPage("chat-apps"), off)).toBe(false);
            expect(settingsPageShown(getSettingsPage("chat-apps"), on)).toBe(true);
            const hidden = SETTINGS_PAGES.filter((entry) => !settingsPageShown(entry, off)).map((entry) => entry.id);
            expect(hidden).toEqual(["chat-apps"]);
            expect(pagesInSection("extensions", off).map((entry) => entry.id)).not.toContain("chat-apps");
            expect(pagesInSection("extensions", on).map((entry) => entry.id)).toContain("chat-apps");
        });

        it("doesn't find Chat apps while Chat is off", () => {
            expect(searchSettingsPages("Slack", translate, on).map((entry) => entry.id)).toContain("chat-apps");
            expect(searchSettingsPages("Slack", translate, off).map((entry) => entry.id)).not.toContain("chat-apps");
        });

        it("lands a link to Chat apps on Your data while Chat is off", () => {
            expect(shownSettingsDestination("chat-apps", off)).toBe("your-data");
            expect(shownSettingsDestination("chat-apps", on)).toBe("chat-apps");
            expect(shownSettingsDestination("mcp", off)).toBe("mcp");
            expect(shownSettingsDestination("notifications", off)).toBe("notifications");
        });
    });
});
