import { act, cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import i18n from "@/i18n";
import en from "@/i18n/locales/en/app.json";
import zh from "@/i18n/locales/zh-CN/app.json";
import enSettings from "@/i18n/locales/en/settings.json";
import { SETTINGS_PAGES } from "@/components/settings/settings-registry";
import { STANDALONE_CHAT_PROJECT_ID, createSession } from "@/lib/stream-helpers";
import { resetPageQueries } from "@/lib/page-queries";
import type { SearchResults } from "@/lib/wails-shim-app";
import type { Project, Session } from "@/types";
import componentSource from "./search-dialog.tsx?raw";
import commandSource from "./ui/command.tsx?raw";
import appCss from "../index.css?raw";

const fixture = vi.hoisted(() => ({
    bindings: new Map<string, ReturnType<typeof vi.fn>>(),
    listeners: new Map<string, Set<(payload: unknown) => void>>(),
}));
vi.mock("@/lib/wails-shim-app", () => new Proxy({}, {
    has: () => true,
    get: (_target, name: string) => {
        if (name === "then") return undefined;
        if (!fixture.bindings.has(name)) fixture.bindings.set(name, vi.fn().mockResolvedValue(undefined));
        return fixture.bindings.get(name);
    },
}));
vi.mock("@/lib/wails-shim-runtime", () => ({
    EventsOn: (name: string, handler: (payload: unknown) => void) => {
        const set = fixture.listeners.get(name) ?? new Set();
        set.add(handler);
        fixture.listeners.set(name, set);
        return () => set.delete(handler);
    },
}));
const toast = vi.hoisted(() => Object.assign(vi.fn(), { error: vi.fn(), success: vi.fn() }));
vi.mock("sonner", () => ({ toast }));

import { SearchDialog, type SearchDialogActions } from "./search-dialog";

function binding(name: string) {
    if (!fixture.bindings.has(name)) fixture.bindings.set(name, vi.fn());
    return fixture.bindings.get(name)!;
}

function session(localId: string, label: string, extra: Partial<Session> = {}): Session {
    return { ...createSession("claude-sonnet-4-5", "ws", label), localId, updatedAt: 10, ...extra };
}

function project(id: string, name: string, sessions: Session[]): Project {
    return {
        id,
        name,
        path: "",
        activeWorkspaceId: `${id}-ws`,
        workspaces: [{ id: `${id}-ws`, label: name, isDefault: true, activeSessionId: null, sessions }],
    } as unknown as Project;
}

const NOW = Date.now();

function projectsFixture(): Project[] {
    return [
        project(STANDALONE_CHAT_PROJECT_ID, "General Chat", [
            session("main-1", "Isoform", { kind: "main", updatedAt: NOW }),
            session("side-1", "Pricing page", { kind: "side", updatedAt: NOW - 1000 }),
            session("legacy-1", "Transcript: Zoom standup", { updatedAt: NOW - 2000 }),
        ]),
        project("proj-1", "Halcyon app", [session("work-1", "Halcyon ERP connector", { updatedAt: NOW - 3000 })]),
        project("proactive", "Proactive", []),
    ];
}

const AUTOMATIONS = [
    { id: "job-1", name: "Weekly invoices", description: "Send the invoices", surfaceKind: "" },
    { id: "job-sys", name: "Daily News invoices", description: "", surfaceKind: "system" },
] as never[];

function actionsMock(): SearchDialogActions {
    return {
        goToMainChat: vi.fn(),
        openSession: vi.fn(),
        openChatMessage: vi.fn(),
        openProject: vi.fn(),
        openAutomation: vi.fn(),
        openHandoff: vi.fn(),
        openLibraryPage: vi.fn(),
        openSettings: vi.fn(),
        toggleSidebar: vi.fn(),
        openToday: vi.fn(),
        openActivity: vi.fn(),
        openKnowHow: vi.fn(),
        openAboutYou: vi.fn(),
        openPerson: vi.fn(),
        openGoal: vi.fn(),
        openTopic: vi.fn(),
        goToPage: vi.fn(),
    };
}

const REMOTE: SearchResults = {
    query: "halcyon",
    failedPlaces: [],
    groups: [
        { place: "topic", results: [{ id: "t-2", place: "topic", title: "Halcyon rollout", snippet: "", at: NOW, target: { kind: "topic", id: "t-2" } }] },
        {
            place: "chat",
            results: [
                { id: "msg-1", place: "chat", title: "Isoform", snippet: "Halcyon asked to move the sync up", at: NOW, topicName: "Halcyon ERP sync", target: { kind: "chat_item", sessionId: "main-1", itemId: "msg-1" } },
                // A title the local list already found: not listed twice.
                { id: "work-1", place: "chat", title: "Halcyon ERP connector", snippet: "", at: NOW, target: { kind: "chat", sessionId: "work-1" } },
            ],
        },
        {
            place: "activity",
            results: [
                { id: "moment:a1", place: "activity", title: "Reviewing Halcyon connector", snippet: "", at: NOW - 86_400_000, target: { kind: "activity_moment", activityId: "a1", sessionId: "s1", at: NOW - 86_400_000 } },
                { id: "thread:th1", place: "activity", title: "Halcyon kickoff", snippet: "", at: NOW, target: { kind: "activity_thread", threadId: "th1", startMs: NOW, endMs: NOW } },
            ],
        },
        { place: "person", results: [{ id: "p-1", place: "person", title: "Priya at Halcyon", snippet: "", at: NOW, target: { kind: "person", id: "p-1" } }] },
        { place: "goal", results: [{ id: "g-1", place: "goal", title: "Ship the Halcyon sync", snippet: "", at: NOW, target: { kind: "goal", id: "g-1" } }] },
        { place: "know_how", results: [{ id: "k-1", place: "know_how", title: "Halcyon field mapping", snippet: "", at: NOW, target: { kind: "knowledge", id: "k-1" } }] },
        { place: "about_you", results: [{ id: "m-1", place: "about_you", title: "Talks through Halcyon plans first", snippet: "", at: NOW, target: { kind: "memory", id: "m-1" } }] },
    ],
};

async function renderSearch(options: { shortcutFor?: (id: string) => string | undefined } = {}) {
    const actions = actionsMock();
    const onOpenChange = vi.fn();
    await act(async () => {
        render(
            <SearchDialog
                open
                onOpenChange={onOpenChange}
                projects={projectsFixture()}
                automations={AUTOMATIONS}
                shortcutFor={options.shortcutFor}
                actions={actions}
            />,
        );
    });
    return { actions, onOpenChange };
}

function input() {
    return screen.getByRole("combobox");
}

async function type(value: string) {
    await act(async () => {
        fireEvent.change(input(), { target: { value } });
    });
}

/** The visible group headings, in order. */
function headings(): string[] {
    return Array.from(document.querySelectorAll("[cmdk-group-heading]")).map((el) => el.textContent ?? "");
}

function group(heading: string): HTMLElement {
    const el = Array.from(document.querySelectorAll("[cmdk-group]")).find(
        (candidate) => candidate.querySelector("[cmdk-group-heading]")?.textContent === heading,
    );
    if (!el) throw new Error(`no group ${heading}; have ${headings().join(", ")}`);
    return el as HTMLElement;
}

function rowTitles(heading: string): string[] {
    return within(group(heading))
        .getAllByRole("option")
        .map((row) => row.querySelector("span.truncate")?.textContent ?? "");
}

/** Whole text, though matched words split it into bold parts. */
function txt(text: string) {
    return (_: string, el: Element | null) =>
        Boolean(el && el.textContent === text && !Array.from(el.children).some((child) => child.textContent === text));
}

function option(name: string | RegExp): HTMLElement {
    return screen.getByRole("option", { name });
}

// Scope cut A2: these guards run with Chat off, as the app does by default;
// the rollback cases turn the \`chat\` switch on.
beforeEach(() => {
    const g = globalThis as Record<string, unknown>;
    g.__isoformFeatureSwitchesForTest = { ...(g.__isoformFeatureSwitchesForTest as object), chat: false };
});

describe("SearchDialog (Find)", () => {
    beforeAll(() => {
        Element.prototype.scrollIntoView = vi.fn();
        globalThis.ResizeObserver ??= class {
            observe() {}
            unobserve() {}
            disconnect() {}
        } as unknown as typeof ResizeObserver;
    });

    beforeEach(async () => {
        await i18n.changeLanguage("en");
        // The dialog reads through the shared query cache; each test starts cold.
        resetPageQueries();
        fixture.listeners.clear();
        for (const mock of fixture.bindings.values()) mock.mockReset().mockResolvedValue(undefined);
        toast.mockReset();
        toast.error.mockReset();
        toast.success.mockReset();
        binding("IsClosedEyesModeEnabled").mockResolvedValue(false);
        binding("ListHandoffs").mockResolvedValue([{ id: "h-1", title: "Invoice cleanup handoff" }]);
        binding("GetTodayTopics").mockResolvedValue([
            { id: "t-1", name: "Halcyon ERP sync", kind: "project", status: "active", pinnedToSidebar: false },
            { id: "t-3", name: "Northwind kickoff", kind: "project", status: "active", pinnedToSidebar: false },
        ]);
        binding("SearchEverything").mockResolvedValue({ query: "", groups: [], failedPlaces: [] });
    });
    afterEach(cleanup);

    const workItem = (id: string, title: string, state = "open", first: string[] = []) => ({
        id, kind: "done", row: "prep", state, source: "prep", ref: {}, first: first.map((text) => ({ text })), noticed: "",
        work: { title }, yes: { does: "keep", line: "" }, reasons: [], createdAt: "", order: 0,
    });
    const workList = (items: unknown[]) => ({ v: 1, version: "1", generatedAt: "", toAnswer: items.length, backFromAgents: 0, items });
    const KEYS: Record<string, string> = { "go-today": "⌘1", "go-agents": "⌘2", "go-activity": "⌘3", "go-you": "⌘4", "go-chat": "⌘5", "open-settings": "⌘," };
    // Scope cut A2: four places, no Chat.
    const GO_TO = [en.sidebar.today, en.sidebar.agents, en.sidebar.activity, en.sidebar.you, en.searchDialog.goTo.settings];
    const SWITCHES_KEY = "__isoformFeatureSwitchesForTest";
    let switchesBefore: unknown;
    /** The `chat` switch on (the rollback): Chat's group and Go to › Chat come back. */
    const chatOn = () => {
        const g = globalThis as Record<string, unknown>;
        switchesBefore = g[SWITCHES_KEY];
        g[SWITCHES_KEY] = { ...(switchesBefore as object), chat: true };
    };
    const chatRestore = () => {
        (globalThis as Record<string, unknown>)[SWITCHES_KEY] = switchesBefore;
    };
    const settle = () => act(async () => { await new Promise((resolve) => setTimeout(resolve, 400)); });

    it("puts ready work from Your work first and opens it on Today", async () => {
        binding("GetWorkList").mockResolvedValue(workList([
            workItem("prep:e1", "Prep for Lena", "open", ["For your 10:00 with Lena"]),
            workItem("prep:e2", "Prep for Lena's team", "answered"),
        ]));
        const { actions } = await renderSearch();
        await type("lena");
        expect(headings()[0]).toBe("Ready");
        expect(screen.getByText(txt("Prep for Lena"))).toBeInTheDocument();
        expect(screen.queryByText(txt("Prep for Lena's team"))).toBeNull();
        await act(async () => { fireEvent.click(screen.getByText(txt("Prep for Lena"))); });
        expect(actions.openToday).toHaveBeenCalled();
    });

    it("shows what FindAnything found, in its groups, and keeps the search once a result opens", async () => {
        binding("FindAnything").mockResolvedValue({ query: "lena", failedGroups: [], groups: [
            { group: "conversations", results: [{ id: "c1", group: "conversations", title: "Call with Lena", snippet: "the budget", target: { kind: "activity", at: 1000 } }] },
            { group: "people", results: [{ id: "p1", group: "people", title: "Lena Ortiz", target: { kind: "person", id: "lena" } }] },
            { group: "claims", results: [{ id: "b1", group: "claims", title: "Lena runs Halcyon", target: { kind: "belief", id: "m1" }, forget: { kind: "belief", id: "m1" } }] },
        ] });
        const { actions } = await renderSearch();
        await type("lena");
        await settle();
        expect(headings().slice(0, 3)).toEqual(["Conversations", "People", "Claims"]);
        expect(screen.getByTestId("find-foot")).toHaveTextContent("What you search for is kept on this computer, in Activity.");
        // A belief forgets with its preview's token; a person has no Forget.
        binding("GetMemoryForgetPreview").mockResolvedValue({ confirmationToken: "tok" });
        binding("ForgetFound").mockResolvedValue(undefined);
        expect(screen.getAllByTestId("find-forget")).toHaveLength(1);
        await act(async () => { fireEvent.click(screen.getByTestId("find-forget")); });
        expect(binding("ForgetFound")).toHaveBeenCalledWith("belief", "m1", "tok");
        expect(screen.queryByText(txt("Lena runs Halcyon"))).toBeNull();
        await act(async () => { fireEvent.click(screen.getByText(txt("Lena Ortiz"))); });
        expect(actions.openPerson).toHaveBeenCalledWith("lena");
        expect(binding("NoteFindSearch")).toHaveBeenCalledWith("lena");
    });

    it("has no Chat group and no Go to › Chat while Chat is gone (scope cut A2)", async () => {
        binding("SearchEverything").mockResolvedValue(REMOTE);
        await renderSearch();
        await type("halcyon");
        await screen.findByText(txt("Priya at Halcyon"));
        await settle();
        expect(headings()).not.toContain(en.searchDialog.groups.chats);
        expect(screen.queryByText(txt("Halcyon asked to move the sync up"))).toBeNull();
        await type("chat");
        expect(rowTitles(en.searchDialog.groups.goTo)).not.toContain(en.sidebar.mainChat);
    });

    // Settings owner's A5 review: Chat apps is a Chat page, so Find doesn't
    // list it under Go to while Chat is off (searchSettingsPages' features).
    it("lists no Chat apps page under Go to while Chat is gone", async () => {
        await renderSearch();
        for (const word of ["Slack", enSettings.pages["chat-apps"].label]) {
            await type(word);
            await settle();
            expect(screen.queryByRole("option", { name: new RegExp(enSettings.pages["chat-apps"].label) })).toBeNull();
        }
        await type("notifications");
        expect(rowTitles(en.searchDialog.groups.goTo)).toContain(enSettings.pages.notifications.label);
    });

    it("orders a query's groups as design S10 does, with Chat while its switch is on", async () => {
        chatOn();
        try {
        binding("GetWorkList").mockResolvedValue(workList([workItem("w1", "A draft for Ana")]));
        binding("FindAnything").mockResolvedValue({ query: "a", failedGroups: [], groups: [
            { group: "answers", results: [{ id: "x1", group: "answers", title: "Said yes to a call", target: { kind: "answer", id: "x1" }, forget: { kind: "answer", id: "x1" } }] },
            { group: "claims", results: [{ id: "b1", group: "claims", title: "Likes a plan", target: { kind: "belief", id: "m1" } }] },
            { group: "people", results: [{ id: "p1", group: "people", title: "Ana", target: { kind: "person", id: "ana" } }] },
            { group: "conversations", results: [{ id: "c1", group: "conversations", title: "Call with Ana", target: { kind: "activity", at: 1000 } }] },
        ] });
        binding("SearchEverything").mockResolvedValue({ query: "a", failedPlaces: [], groups: REMOTE.groups.filter((g) => g.place === "topic") });
        await renderSearch();
        await type("a");
        await settle();
        expect(headings()).toEqual([
            en.searchDialog.groups.ready,
            en.searchDialog.groups.topics,
            en.searchDialog.groups.conversations,
            en.searchDialog.groups.people,
            en.searchDialog.groups.claims,
            en.searchDialog.groups.chats,
            en.searchDialog.groups.answers,
            en.searchDialog.groups.goTo,
        ]);
        // Go to is last: the places, then the Settings pages, in one group.
        const goTo = rowTitles(en.searchDialog.groups.goTo);
        // "a" matches every place but You; Chat comes after the four.
        expect(goTo.slice(0, 4)).toEqual([en.sidebar.today, en.sidebar.agents, en.sidebar.activity, en.sidebar.mainChat]);
        expect(goTo.length).toBeGreaterThan(5);
        } finally {
            chatRestore();
        }
    });

    it("lists a person once when both FindAnything and the chat search find them", async () => {
        binding("FindAnything").mockResolvedValue({ query: "priya", failedGroups: [], groups: [
            { group: "people", results: [{ id: "people:p-1", group: "people", title: "Priya at Halcyon", target: { kind: "person", id: "p-1" } }] },
        ] });
        binding("SearchEverything").mockResolvedValue({ query: "priya", failedPlaces: [], groups: REMOTE.groups.filter((g) => g.place === "person") });
        await renderSearch();
        await type("priya");
        await settle();
        expect(rowTitles(en.searchDialog.groups.people)).toEqual(["Priya at Halcyon"]);
    });

    describe("the empty box", () => {
        it("shows nothing without ready work: no Go to, topics, recents, actions or Library pages", async () => {
            await renderSearch();
            await act(async () => {});
            expect(headings()).toEqual([]);
            expect(screen.queryAllByRole("option")).toHaveLength(0);
            for (const word of ["Topics", "Recent", "Actions", "Go to", "Tasks", "Automations", "Deliverables", "Goals", "Pause observing", "Search and commands", "Go to chat"]) {
                expect(screen.queryByText(word)).toBeNull();
            }
            expect(binding("GetTodayTopics")).not.toHaveBeenCalled();
            expect(binding("ListHandoffs")).not.toHaveBeenCalled();
            expect(binding("SearchEverything")).not.toHaveBeenCalled();
        });

        it("shows Ready alone, up to 5 rows of the work that waits", async () => {
            binding("GetWorkList").mockResolvedValue(workList([
                ...Array.from({ length: 7 }, (_, i) => workItem(`w${i}`, `Draft ${i}`)),
                workItem("done", "Already answered", "answered"),
            ]));
            await renderSearch();
            await act(async () => {});
            expect(headings()).toEqual([en.searchDialog.groups.ready]);
            expect(rowTitles(en.searchDialog.groups.ready)).toEqual(["Draft 0", "Draft 1", "Draft 2", "Draft 3", "Draft 4"]);
        });

        it("selects the first row and shows the keys in the footer", async () => {
            binding("GetWorkList").mockResolvedValue(workList([workItem("w1", "Draft 1")]));
            await renderSearch({ shortcutFor: (id) => (id === "command-palette" ? "Ctrl+K" : undefined) });
            await act(async () => {});
            expect(screen.getAllByRole("option")[0]).toHaveAttribute("aria-selected", "true");
            expect(screen.getByText(txt("Ctrl+K from anywhere"))).toBeInTheDocument();
            expect(screen.getByText(en.searchDialog.hints.move)).toBeInTheDocument();
            expect(screen.getByText(en.searchDialog.hints.close)).toBeInTheDocument();
        });
    });

    describe("Go to", () => {
        it("shows each place with its key when the query names it", async () => {
            await renderSearch({ shortcutFor: (id) => KEYS[id] });
            const keys = ["⌘1", "⌘2", "⌘3", "⌘4", "⌘,"];
            for (const [index, label] of GO_TO.entries()) {
                await type(label);
                const row = within(group(en.searchDialog.groups.goTo)).getAllByRole("option")
                    .find((candidate) => candidate.querySelector("span.truncate")?.textContent === label)!;
                expect(row.querySelector("kbd")?.textContent).toBe(keys[index]);
            }
        // Loops over every page: slow under load.
        }, 20_000);

        it("opens a place through goToPage, and Settings through openSettings", async () => {
            const { actions } = await renderSearch();
            await type("agents");
            fireEvent.click(within(group(en.searchDialog.groups.goTo)).getAllByRole("option")
                .find((row) => row.querySelector("span.truncate")?.textContent === en.sidebar.agents)!);
            expect(actions.goToPage).toHaveBeenCalledWith("agents");
            await type("settings");
            fireEvent.click(within(group(en.searchDialog.groups.goTo)).getAllByRole("option")
                .find((row) => row.querySelector("span.truncate")?.textContent === en.searchDialog.goTo.settings)!);
            expect(actions.openSettings).toHaveBeenCalledWith();
        });

        it("lists the place a query names", async () => {
            const { actions } = await renderSearch();
            await type("agen");
            expect(rowTitles(en.searchDialog.groups.goTo)[0]).toBe(en.sidebar.agents);
            fireEvent.keyDown(input(), { key: "Enter" });
            expect(actions.goToPage).toHaveBeenCalledWith("agents");
        });
    });

    describe("keeps the chat and Settings sources", () => {
        // These search chats: the rollback with the `chat` switch on.
        beforeEach(chatOn);
        afterEach(chatRestore);
        it("finds chats by title in every project and opens the owning project's chat", async () => {
            const { actions } = await renderSearch();
            await type("halcyon erp");
            expect(rowTitles(en.searchDialog.groups.chats)).toContain("Halcyon ERP connector");
            fireEvent.click(option(/Halcyon ERP connector/));
            expect(actions.openSession).toHaveBeenCalledWith("proj-1", "work-1");
        });

        it("finds a project's chats by the project's name, as the palette did", async () => {
            const { actions } = await renderSearch();
            await type("halcyon app");
            const row = within(group(en.searchDialog.groups.chats)).getByRole("option", { name: /Halcyon ERP connector/ });
            // A real project is named in the meta line.
            expect(row).toHaveTextContent("Halcyon app");
            fireEvent.click(row);
            expect(actions.openSession).toHaveBeenCalledWith("proj-1", "work-1");
        });

        it("finds old side chats and transcripts in the standalone project", async () => {
            const { actions } = await renderSearch();
            await type("zoom");
            fireEvent.click(option(/Transcript: Zoom standup/));
            expect(actions.openSession).toHaveBeenCalledWith(STANDALONE_CHAT_PROJECT_ID, "legacy-1");
        });

        it("no longer lists automations, one-time work, projects or Library pages", async () => {
            await renderSearch();
            await type("invoice");
            expect(screen.queryByText(txt("Weekly invoices"))).toBeNull();
            expect(screen.queryByText(txt("Invoice cleanup handoff"))).toBeNull();
            await type("halcyon app");
            expect(headings()).not.toContain("Projects");
            for (const word of ["Tasks", "Automations", "Deliverables"]) {
                await type(word.toLowerCase());
                expect(screen.queryByRole("group", { name: "Pages" })).toBeNull();
                expect(headings()).not.toContain("Pages");
            }
        });

        it("lists every Settings page from the registry by its name", async () => {
            const { actions } = await renderSearch();
            for (const entry of SETTINGS_PAGES) {
                const label = i18n.t(entry.labelKey as never, { ns: "settings" }) as string;
                await type(label);
                expect(rowTitles(en.searchDialog.groups.goTo)).toContain(label);
                const row = within(group(en.searchDialog.groups.goTo)).getAllByRole("option").find(
                    (candidate) => candidate.querySelector("span.truncate")?.textContent === label,
                )!;
                fireEvent.click(row);
                expect(actions.openSettings).toHaveBeenLastCalledWith(entry.id);
            }
            expect(actions.openSettings).toHaveBeenCalledTimes(SETTINGS_PAGES.length);
        // Loops over every page: slow under load.
        }, 20_000);

        it("opens a page found by its keywords with Advanced expanded", async () => {
            const { actions } = await renderSearch();
            // "relay" names the Phone page's server address, kept under Advanced.
            await type("relay");
            fireEvent.click(option(new RegExp(enSettings.pages.iphone.label)));
            expect(actions.openSettings).toHaveBeenCalledWith("iphone", { advanced: true });
        });

        it("opens a page found only by its one-line description as usual", async () => {
            const { actions } = await renderSearch();
            const entry = SETTINGS_PAGES.find((page) => page.id === "notifications")!;
            const subtitle = i18n.t(entry.subtitleKey as never, { ns: "settings" }) as string;
            const keywords = (i18n.t(entry.keywordsKey as never, { ns: "settings" }) as string).toLowerCase();
            const label = (i18n.t(entry.labelKey as never, { ns: "settings" }) as string).toLowerCase();
            // A word of the description that is neither the name nor a keyword.
            const word = subtitle.split(/\s+/).map((w) => w.replace(/[^\p{L}]/gu, "")).find(
                (w) => w.length > 4 && !keywords.includes(w.toLowerCase()) && !label.includes(w.toLowerCase()),
            )!;
            expect(word).toBeTruthy();
            await type(word);
            const row = within(group(en.searchDialog.groups.goTo)).getAllByRole("option").find(
                (candidate) => candidate.querySelector("span.truncate")?.textContent === enSettings.pages.notifications.label,
            )!;
            fireEvent.click(row);
            expect(actions.openSettings).toHaveBeenCalledWith("notifications");
        });

        it("opens a chat app's setup when the search names it", async () => {
            const { actions } = await renderSearch();
            await type("Slack");
            fireEvent.click(option(new RegExp(enSettings.pages["chat-apps"].label)));
            expect(actions.openSettings).toHaveBeenCalledWith("slack");
        });
    });

    describe("runs no commands (conformance answers S6)", () => {
        it("has no Actions group: Closed Eyes, the brief and the sidebar toggle live elsewhere", async () => {
            const { actions } = await renderSearch();
            for (const query of ["closed", "open eyes", "make my", "brief", "show or", "sidebar"]) {
                await type(query);
                expect(headings()).not.toContain("Actions");
                expect(screen.queryByText(/^(Closed Eyes|Open eyes|Make my brief now|Show or hide the sidebar|Pause observing)$/)).toBeNull();
                fireEvent.keyDown(input(), { key: "Enter" });
            }
            await act(async () => { await Promise.resolve(); });
            expect(binding("SetClosedEyesModeEnabled")).not.toHaveBeenCalled();
            expect(binding("GenerateMobileBriefNow")).not.toHaveBeenCalled();
            expect(actions.toggleSidebar).not.toHaveBeenCalled();
            // No "command" wording anywhere in Find's words.
            expect(JSON.stringify(en.searchDialog)).not.toMatch(/command/i);
            expect(JSON.stringify(en.searchDialog)).not.toMatch(/or run/i);
            expect(en.searchDialog.placeholder).toBe("Find anything Isoform keeps");
            expect(screen.getByPlaceholderText("Find anything Isoform keeps")).toBeInTheDocument();
        });
    });

    describe("matched words (conformance answers S5)", () => {
        // These search chats: the rollback with the `chat` switch on.
        beforeEach(chatOn);
        afterEach(chatRestore);
        it("bolds the query's words in the title and the meta line, case-insensitively, as elements", async () => {
            binding("SearchEverything").mockResolvedValue(REMOTE);
            await renderSearch();
            await type("halcyon SYNC");
            const message = await screen.findByRole("option", { name: /Halcyon asked to move the sync up/ });
            const bold = within(message).getAllByTestId("find-match").map((el) => el.textContent);
            // Title "Halcyon asked to move the sync up", meta "... Halcyon ERP sync".
            expect(bold).toEqual(["Halcyon", "sync", "Halcyon", "sync"]);
            expect(within(message).getAllByTestId("find-match")[0].tagName).toBe("STRONG");
            // A one-letter query bolds nothing, as FindAnything matches nothing.
            await type("a");
            expect(screen.queryAllByTestId("find-match")).toHaveLength(0);
            expect(componentSource).not.toMatch(/dangerouslySetInnerHTML/);
        });

        it("treats the words as text, not a pattern", async () => {
            await renderSearch();
            await type("(.*");
            expect(screen.queryAllByTestId("find-match")).toHaveLength(0);
        });
    });

    describe("search over chats and people", () => {
        // These search chats: the rollback with the `chat` switch on.
        beforeEach(chatOn);
        afterEach(chatRestore);
        beforeEach(() => {
            binding("SearchEverything").mockResolvedValue(REMOTE);
        });

        it("lists topics, chats, people and know-how, and none of the places Find no longer lists", async () => {
            await renderSearch();
            await type("halcyon");
            await screen.findByText(txt("Priya at Halcyon"));
            expect(binding("SearchEverything")).toHaveBeenCalledWith("halcyon", expect.any(Number));
            expect(headings()).toEqual([
                en.searchDialog.groups.topics,
                en.searchDialog.groups.people,
                en.searchDialog.groups.claims,
                en.searchDialog.groups.chats,
            ]);
            // The local title row is not repeated.
            expect(rowTitles(en.searchDialog.groups.chats)).toEqual(["Halcyon ERP connector", "Halcyon asked to move the sync up"]);
            for (const title of ["Reviewing Halcyon connector", "Halcyon kickoff", "Ship the Halcyon sync", "Talks through Halcyon plans first"]) {
                expect(screen.queryByText(title)).toBeNull();
            }
            expect(screen.getByText(en.searchDialog.count_other.replace("{{count}}", String(screen.getAllByRole("option").length)))).toBeInTheDocument();
        });

        it("says when things are, in words, never a clock time", async () => {
            await renderSearch();
            await type("halcyon");
            const message = await screen.findByRole("option", { name: /Halcyon asked to move the sync up/ });
            // The main chat is named as in the sidebar.
            expect(message).toHaveTextContent(`${en.sidebar.mainChat} · ${en.searchDialog.when.today} · Halcyon ERP sync`);
            for (const row of screen.getAllByRole("option")) {
                expect(row.textContent).not.toMatch(/\d{1,2}:\d{2}/);
            }
        });

        it("opens each result where it lives", async () => {
            const { actions } = await renderSearch();
            await type("halcyon");
            await screen.findByText(txt("Priya at Halcyon"));
            fireEvent.click(option(/Halcyon asked to move the sync up/));
            expect(actions.openChatMessage).toHaveBeenCalledWith("main-1", "msg-1");
            fireEvent.click(option(/Priya at Halcyon/));
            expect(actions.openPerson).toHaveBeenCalledWith("p-1");
        });

        it("finds a topic by its name and opens it beside Today, second after Ready", async () => {
            const { actions } = await renderSearch();
            await type("halcyon roll");
            await screen.findByText(txt("Halcyon rollout"));
            expect(rowTitles(en.searchDialog.groups.topics)).toEqual(["Halcyon rollout"]);
            expect(headings()[0]).toBe(en.searchDialog.groups.topics);
            expect(option(/Halcyon rollout/)).toHaveAttribute("aria-selected", "true");
            await act(async () => { fireEvent.keyDown(input(), { key: "Enter" }); });
            expect(actions.openTopic).toHaveBeenCalledWith({ id: "t-2", name: "Halcyon rollout", kind: "work", pinned: false });
        });

        it("opens a found topic as the registry describes it: a pinned Following topic stays one", async () => {
            binding("GetTodayTopics").mockResolvedValue([
                { id: "t-2", name: "Halcyon rollout", kind: "following", status: "active", pinnedToSidebar: true },
            ]);
            const { actions } = await renderSearch();
            await type("halcyon roll");
            await screen.findByText(txt("Halcyon rollout"));
            await act(async () => { fireEvent.click(option(/Halcyon rollout/)); });
            expect(binding("GetTodayTopics")).toHaveBeenCalledTimes(1);
            expect(actions.openTopic).toHaveBeenCalledWith(expect.objectContaining({ id: "t-2", name: "Halcyon rollout", kind: "followed", pinned: true }));
        });

        it("keeps How you do things findable, as claims marked How you do things", async () => {
            const { actions } = await renderSearch();
            await type("halcyon");
            const row = await screen.findByRole("option", { name: /Halcyon field mapping/ });
            expect(within(group(en.searchDialog.groups.claims)).getByRole("option", { name: /Halcyon field mapping/ })).toBe(row);
            expect(row).toHaveTextContent(en.searchDialog.meta.knowHow);
            fireEvent.click(row);
            expect(actions.openKnowHow).toHaveBeenCalledWith("k-1");
        });

        it("shows what loaded and a quiet line for a place that failed", async () => {
            binding("SearchEverything").mockResolvedValue({
                ...REMOTE,
                groups: REMOTE.groups.filter((g) => g.place === "person"),
                failedPlaces: ["chat", "know_how"],
            });
            await renderSearch();
            await type("halcyon");
            await screen.findByText(txt("Priya at Halcyon"));
            expect(screen.getByTestId("search-failed")).toHaveTextContent(
                en.searchDialog.failed.replace("{{places}}", `${en.searchDialog.places.chat} and ${en.searchDialog.places.know_how}`),
            );
        });

        it("keeps the local chats when the whole search fails", async () => {
            binding("SearchEverything").mockRejectedValue(new Error("down"));
            await renderSearch();
            await type("halcyon");
            const places = new Intl.ListFormat("en", { style: "long", type: "conjunction" }).format(["topics", "chats", "people", "How you do things"]);
            expect(await screen.findByTestId("search-failed")).toHaveTextContent(en.searchDialog.failed.replace("{{places}}", places));
            expect(rowTitles(en.searchDialog.groups.chats)).toEqual(["Halcyon ERP connector"]);
        });

        it("shows nothing Isoform saw while eyes are closed", async () => {
            binding("IsClosedEyesModeEnabled").mockResolvedValue(true);
            binding("FindAnything").mockResolvedValue({ query: "halcyon", failedGroups: [], groups: [
                { group: "conversations", results: [{ id: "c1", group: "conversations", title: "Call about Halcyon", target: { kind: "activity", at: 1000 } }] },
            ] });
            await renderSearch();
            await type("halcyon");
            await screen.findByText(txt("Priya at Halcyon"));
            await settle();
            expect(binding("FindAnything")).not.toHaveBeenCalled();
            expect(screen.queryByText(txt("Call about Halcyon"))).toBeNull();
            expect(screen.queryByText(txt("Reviewing Halcyon connector"))).toBeNull();
            expect(screen.getByTestId("search-paused")).toHaveTextContent(en.searchDialog.pausedNote);
        });

        it("says plainly when nothing matches", async () => {
            binding("SearchEverything").mockResolvedValue({ query: "zzz", groups: [], failedPlaces: [] });
            await renderSearch();
            await type("zzzq");
            expect(await screen.findByText(en.searchDialog.noResults.replace("{{query}}", "zzzq"))).toBeInTheDocument();
        });
    });

    describe("privacy withdrawal", () => {
        it("a privacy event withdraws the query and the results before the re-read lands", async () => {
            binding("SearchEverything").mockResolvedValue(REMOTE);
            await renderSearch();
            await type("halcyon");
            await screen.findByText(txt("Priya at Halcyon"));
            expect(input()).toHaveValue("halcyon");
            await act(async () => {
                fixture.listeners.get("memory:withdrawn")?.forEach((handler) => handler(undefined));
            });
            expect(input()).toHaveValue("");
            expect(screen.queryByText(txt("Priya at Halcyon"))).toBeNull();
            expect(screen.queryByText(txt("Halcyon asked to move the sync up"))).toBeNull();
            expect(headings()).toEqual([]);
        });
    });

    describe("Enter opens what was found", () => {
        // These search chats: the rollback with the `chat` switch on.
        beforeEach(chatOn);
        afterEach(chatRestore);
        it("\"brief\" and Enter opens the chat that says it", async () => {
            binding("SearchEverything").mockResolvedValue({
                query: "brief",
                failedPlaces: [],
                groups: [{ place: "chat", results: [
                    { id: "msg-b", place: "chat", title: "Isoform", snippet: "brief notes for Friday", at: NOW, target: { kind: "chat_item", sessionId: "main-1", itemId: "msg-b" } },
                ] }],
            });
            const { actions } = await renderSearch();
            await type("brief");
            await screen.findByText(txt("brief notes for Friday"));
            fireEvent.keyDown(input(), { key: "Enter" });
            expect(actions.openChatMessage).toHaveBeenCalledWith("main-1", "msg-b");
            expect(binding("GenerateMobileBriefNow")).not.toHaveBeenCalled();
        });
    });

    describe("keys", () => {
        it("moves with the arrows and opens with Enter", async () => {
            binding("GetWorkList").mockResolvedValue(workList([workItem("w0", "Draft 0"), workItem("w1", "Draft 1"), workItem("w2", "Draft 2")]));
            const actions = { ...actionsMock(), openWorkItem: vi.fn() };
            await act(async () => {
                render(<SearchDialog open onOpenChange={vi.fn()} projects={[]} actions={actions} />);
            });
            await act(async () => {});
            const rows = screen.getAllByRole("option");
            expect(rows[0]).toHaveAttribute("aria-selected", "true");
            fireEvent.keyDown(input(), { key: "ArrowDown" });
            fireEvent.keyDown(input(), { key: "ArrowDown" });
            expect(screen.getAllByRole("option")[2]).toHaveAttribute("aria-selected", "true");
            fireEvent.keyDown(input(), { key: "Enter" });
            expect(actions.openWorkItem).toHaveBeenCalledWith("w2");
        });

        it("selects the first row again when the query changes", async () => {
            binding("GetWorkList").mockResolvedValue(workList([workItem("w0", "Halcyon draft"), workItem("w1", "Halcyon notes")]));
            await renderSearch();
            await act(async () => {});
            fireEvent.keyDown(input(), { key: "ArrowDown" });
            await type("halcyon");
            expect(screen.getAllByRole("option")[0]).toHaveAttribute("aria-selected", "true");
        });

        it("Escape clears the query first, then closes, inside the real dialog", async () => {
            const { onOpenChange } = await renderSearch();
            await type("halcyon");
            fireEvent.keyDown(input(), { key: "Escape" });
            expect(input()).toHaveValue("");
            expect(onOpenChange).not.toHaveBeenCalled();
            fireEvent.keyDown(input(), { key: "Escape" });
            expect(onOpenChange).toHaveBeenCalledWith(false);
        });
    });

    describe("copy and design rules", () => {
        it("is called Find and is translated, the screen-reader title included", async () => {
            expect(en.searchDialog.title).toBe("Find");
            await i18n.changeLanguage("zh-CN");
            await renderSearch();
            expect(screen.getByRole("dialog", { name: zh.searchDialog.title })).toBeInTheDocument();
            expect(screen.getByPlaceholderText(zh.searchDialog.placeholder)).toBeInTheDocument();
            await type("Agents");
            expect(screen.getByText(zh.searchDialog.groups.goTo)).toBeInTheDocument();
            // Closed Eyes is a product name: it stays English in zh-CN.
            expect(zh.searchDialog.pausedNote).toContain("Closed Eyes");
        });

        it("has the same Find keys in English and Chinese", () => {
            const keys = (value: unknown, prefix = ""): string[] =>
                value && typeof value === "object"
                    ? Object.entries(value).flatMap(([key, child]) => keys(child, `${prefix}${key}.`))
                    : [prefix];
            expect(keys(zh.searchDialog).sort()).toEqual(keys(en.searchDialog).sort());
        });

        it("has no hardcoded copy and never uses the fallback form of t()", () => {
            expect(componentSource).not.toMatch(/<span>[A-Za-z][^<{]*<\/span>/);
            expect(componentSource).not.toMatch(/<p[^>]*>[A-Za-z][^<{]*<\/p>/);
            expect(componentSource).not.toMatch(/(?:heading|placeholder|title|label)="[A-Za-z]/);
            expect(componentSource).not.toMatch(/\bt\("[^"]+",\s*"/);
        });

        it("marks the selected row with the surface-active token, not an ink fill or a left bar", () => {
            expect(commandSource).toMatch(/data-\[selected=true\]:bg-surface-active/);
            expect(commandSource).not.toMatch(/data-\[selected=true\]:bg-ink/);
            // CLAUDE.md rule 12: no left-edge accent bar on a selected row.
            expect(appCss).not.toMatch(/\[cmdk-item\][^{]*::before/);
        });
    });
});
