import { act, cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import i18n from "@/i18n";
import en from "@/i18n/locales/en/app.json";
import zh from "@/i18n/locales/zh-CN/app.json";
import enSettings from "@/i18n/locales/en/settings.json";
import { SETTINGS_PAGES } from "@/components/settings/settings-registry";
import { STANDALONE_CHAT_PROJECT_ID, createSession } from "@/lib/stream-helpers";
import { pageQueryCache } from "@/lib/query-cache";
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

function option(name: string | RegExp): HTMLElement {
    return screen.getByRole("option", { name });
}

describe("SearchDialog (search and commands)", () => {
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

    it("puts ready work from Your work first and opens it on Today", async () => {
        binding("GetWorkList").mockResolvedValue({ v: 1, version: "1", generatedAt: "", toAnswer: 1, backFromAgents: 0, items: [
            { id: "prep:e1", kind: "done", row: "prep", state: "open", source: "prep", ref: {}, first: [{ text: "For your 10:00 with Lena" }], noticed: "", work: { title: "Prep for Lena" }, yes: { does: "keep", line: "" }, reasons: [], createdAt: "", order: 0 },
            { id: "prep:e2", kind: "done", row: "prep", state: "answered", source: "prep", ref: {}, first: [], noticed: "", work: { title: "Prep for Lena's team" }, yes: { does: "keep", line: "" }, reasons: [], createdAt: "", order: 1 },
        ] });
        const { actions } = await renderSearch();
        await type("lena");
        expect(headings()[0]).toBe("Ready");
        expect(screen.getByText("Prep for Lena")).toBeInTheDocument();
        expect(screen.queryByText("Prep for Lena's team")).toBeNull();
        await act(async () => { fireEvent.click(screen.getByText("Prep for Lena")); });
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
        await act(async () => { await new Promise((resolve) => setTimeout(resolve, 400)); });
        expect(headings()).toEqual(expect.arrayContaining(["Conversations", "Who you know", "Claims"]));
        expect(headings()[0]).toBe("Conversations");
        expect(headings().indexOf("Conversations")).toBeLessThan(headings().indexOf("Claims"));
        expect(screen.getByTestId("find-foot")).toHaveTextContent("What you search for is kept on this computer, in Activity.");
        // A belief forgets with its preview's token; a person has no Forget.
        binding("GetMemoryForgetPreview").mockResolvedValue({ confirmationToken: "tok" });
        binding("ForgetFound").mockResolvedValue(undefined);
        expect(screen.getAllByTestId("find-forget")).toHaveLength(1);
        await act(async () => { fireEvent.click(screen.getByTestId("find-forget")); });
        expect(binding("ForgetFound")).toHaveBeenCalledWith("belief", "m1", "tok");
        expect(screen.queryByText("Lena runs Halcyon")).toBeNull();
        await act(async () => { fireEvent.click(screen.getByText("Lena Ortiz")); });
        expect(actions.openPerson).toHaveBeenCalledWith("lena");
        expect(binding("NoteFindSearch")).toHaveBeenCalledWith("lena");
    });

    it("replaces an empty cold bootstrap after topics are published", async () => {
        binding("GetTodayTopics").mockResolvedValueOnce({ topics: [], refreshing: true });
        await renderSearch();
        expect(screen.queryByText("Halcyon ERP sync")).toBeNull();
        await act(async () => {
            fixture.listeners.get("topics:updated")?.forEach((handler) => handler(undefined));
        });
        expect(screen.getByText("Halcyon ERP sync")).toBeInTheDocument();
        expect(binding("GetTodayTopics")).toHaveBeenCalledTimes(2);
    });

    it("shows the gathering note for empty and non-empty queries during first run", async () => {
        binding("GetTodayTopics").mockResolvedValue({ topics: [], refreshing: false, firstRun: true });
        await renderSearch();
        expect(screen.getByTestId("search-topics-gathering")).toHaveTextContent(en.searchDialog.topicsGathering);
        expect(screen.queryByText(/not available/i)).toBeNull();
        await type("nothing");
        expect(screen.getByTestId("search-topics-gathering")).toHaveTextContent(en.searchDialog.topicsGathering);
    });

    it("does not show gathering after a published snapshot filters to zero topics", async () => {
        binding("GetTodayTopics").mockResolvedValue({ topics: [], refreshing: false, firstRun: false, clear: true });
        await renderSearch();
        expect(screen.queryByTestId("search-topics-gathering")).toBeNull();
        expect(screen.queryByText(/not available/i)).toBeNull();
    });

    it("removes a withdrawn topic from search recents while retaining its survivor", async () => {
        binding("GetTodayTopics").mockResolvedValue({
            topics: [
                { id: "topic-a", kind: "project", name: "Atlas", status: "active" },
                { id: "topic-b", kind: "project", name: "Beacon", status: "active" },
            ],
            firstRun: false,
            refreshing: false,
        });
        await renderSearch();
        expect(screen.getByText("Atlas")).toBeInTheDocument();
        expect(screen.getByText("Beacon")).toBeInTheDocument();
        binding("GetTodayTopics").mockResolvedValue({
            topics: [{ id: "topic-b", kind: "project", name: "Beacon", status: "active" }],
            firstRun: false,
            clear: false,
            refreshing: false,
        });
        await act(async () => {
            fixture.listeners.get("topics:updated")?.forEach((handler) => handler(undefined));
        });
        expect(screen.queryByText("Atlas")).toBeNull();
        expect(screen.getByText("Beacon")).toBeInTheDocument();
    });

    it("paints cached recents synchronously when the dialog reopens", async () => {
        pageQueryCache.set("GetTodayTopics", [], [
            { id: "topic-0", kind: "project", name: "Fixture topic 0", status: "moving" },
        ]);
        pageQueryCache.set("ListHandoffs", [], []);
        render(<SearchDialog open onOpenChange={vi.fn()} projects={[]} actions={actionsMock()} />);
        // Synchronous, before the revalidating reads settle.
        expect(rowTitles(en.searchDialog.groups.recent)).toContain("Fixture topic 0");
        await act(async () => {});
    });

    it("retains good topics during refresh and clears them on source withdrawal", async () => {
        await renderSearch();
        binding("GetTodayTopics").mockResolvedValue({ topics: [], refreshing: true });
        await act(async () => {
            fixture.listeners.get("topics:updated")?.forEach((handler) => handler(undefined));
        });
        expect(screen.getByText("Halcyon ERP sync")).toBeInTheDocument();
        binding("GetTodayTopics").mockResolvedValue({ topics: [], refreshing: true, clear: true });
        await act(async () => {
            fixture.listeners.get("topics:updated")?.forEach((handler) => handler(undefined));
        });
        expect(screen.queryByText("Halcyon ERP sync")).toBeNull();
    });

    it("reconciles topic rows in place: an identical refresh keeps the DOM nodes, a change swaps just the row", async () => {
        await renderSearch();
        const before = screen.getByText("Halcyon ERP sync");
        // A refresh returning equal content as new objects keeps state and DOM.
        binding("GetTodayTopics").mockResolvedValue([
            { id: "t-1", name: "Halcyon ERP sync", kind: "project", status: "active", pinnedToSidebar: false },
            { id: "t-3", name: "Northwind kickoff", kind: "project", status: "active", pinnedToSidebar: false },
        ]);
        await act(async () => {
            fixture.listeners.get("topics:updated")?.forEach((handler) => handler(undefined));
        });
        expect(screen.getByText("Halcyon ERP sync")).toBe(before);
        // A genuine change replaces that row's text.
        binding("GetTodayTopics").mockResolvedValue([
            { id: "t-1", name: "Halcyon ERP migration", kind: "project", status: "active", pinnedToSidebar: false },
            { id: "t-3", name: "Northwind kickoff", kind: "project", status: "active", pinnedToSidebar: false },
        ]);
        await act(async () => {
            fixture.listeners.get("topics:updated")?.forEach((handler) => handler(undefined));
        });
        expect(screen.getByText("Halcyon ERP migration")).toBeInTheDocument();
        expect(screen.queryByText("Halcyon ERP sync")).toBeNull();
    });

    it("ignores a stale topic read and removes the update subscription on close", async () => {
        let finish: (value: unknown) => void = () => {};
        binding("GetTodayTopics").mockImplementationOnce(() => new Promise((resolve) => { finish = resolve; }));
        await renderSearch();
        // The shared query-cache installer holds its own topics:updated
        // subscription; the dialog adds exactly one more and removes it.
        const whileOpen = fixture.listeners.get("topics:updated")?.size ?? 0;
        await act(async () => {
            fixture.listeners.get("topics:updated")?.forEach((handler) => handler(undefined));
        });
        expect(screen.getByText("Halcyon ERP sync")).toBeInTheDocument();
        await act(async () => { finish({ topics: [], refreshing: true, clear: true }); });
        expect(screen.getByText("Halcyon ERP sync")).toBeInTheDocument();
        cleanup();
        expect(fixture.listeners.get("topics:updated")?.size ?? 0).toBe(whileOpen - 1);
    });

    describe("the empty box", () => {
        it("shows recent topics and chats, the actions in the artboard's order, and the pages Library's content moved to", async () => {
            await renderSearch();
            expect(headings()).toEqual([en.searchDialog.groups.recent, en.searchDialog.groups.actions, en.searchDialog.groups.pages]);
            // Topics, then chats by last activity; the main chat has its own action.
            expect(rowTitles(en.searchDialog.groups.recent)).toEqual([
                "Halcyon ERP sync",
                "Northwind kickoff",
                "Pricing page",
                "Transcript: Zoom standup",
                "Halcyon ERP connector",
            ]);
            expect(rowTitles(en.searchDialog.groups.actions)).toEqual([
                en.searchDialog.actions.pause,
                en.searchDialog.actions.brief,
                en.searchDialog.mainChat,
                en.searchDialog.actions.toggleSidebar,
                en.searchDialog.actions.settings,
                en.searchDialog.actions.activity,
                en.searchDialog.actions.knowHow,
                en.searchDialog.actions.aboutYou,
            ]);
            expect(rowTitles(en.searchDialog.groups.pages)).toEqual([
                en.sidebar.tasks,
                en.automations.title,
                en.deliverablesPage.title,
                en.userMenu.knowledge,
                en.searchDialog.groups.goals,
            ]);
            // Library is gone (sidebar handoff, section 3): no row names it.
            for (const word of ["Library", "Upcoming", "One-time work", "Playbooks", "Deliverables"]) {
                expect(screen.queryByText(word)).toBeNull();
            }
            // One main chat: no New chat action anywhere.
            expect(screen.queryByText(/new chat/i)).toBeNull();
            expect(binding("SearchEverything")).not.toHaveBeenCalled();
        });

        it("selects the first row and shows the keys in the footer", async () => {
            await renderSearch({ shortcutFor: (id) => (id === "command-palette" ? "Ctrl+K" : undefined) });
            expect(screen.getAllByRole("option")[0]).toHaveAttribute("aria-selected", "true");
            expect(screen.getByText("Ctrl+K from anywhere")).toBeInTheDocument();
            expect(screen.getByText(en.searchDialog.hints.move)).toBeInTheDocument();
            expect(screen.getByText(en.searchDialog.hints.close)).toBeInTheDocument();
        });
    });

    describe("keeps every source the old Search and command palette had", () => {
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

        it("finds automations by name or description, without system jobs", async () => {
            const { actions } = await renderSearch();
            await type("invoices");
            expect(rowTitles(en.searchDialog.groups.upcoming)).toEqual(["Weekly invoices"]);
            fireEvent.click(option(/Weekly invoices/));
            expect(actions.openAutomation).toHaveBeenCalledWith("job-1");
        });

        it("says when one-time work could not be read, and still shows the rest", async () => {
            binding("ListHandoffs").mockRejectedValue(new Error("store not ready"));
            await renderSearch();
            await type("weekly");
            expect(rowTitles(en.searchDialog.groups.upcoming)).toEqual(["Weekly invoices"]);
            expect(await screen.findByTestId("search-failed")).toHaveTextContent(
                en.searchDialog.failed.replace("{{places}}", en.searchDialog.places.handoffs),
            );
        });

        it("finds handoffs by title", async () => {
            const { actions } = await renderSearch();
            await type("cleanup");
            fireEvent.click(option(/Invoice cleanup handoff/));
            expect(actions.openHandoff).toHaveBeenCalledWith("h-1");
        });

        it("finds projects, leaving out the standalone container and Proactive", async () => {
            const { actions } = await renderSearch();
            await type("a");
            await type("halcyon app");
            expect(rowTitles(en.searchDialog.groups.projects)).toEqual(["Halcyon app"]);
            fireEvent.click(within(group(en.searchDialog.groups.projects)).getByRole("option", { name: /Halcyon app/ }));
            expect(actions.openProject).toHaveBeenCalledWith("proj-1");
            await type("proactive");
            expect(screen.queryByText(en.searchDialog.groups.projects)).toBeNull();
            await type("general chat");
            expect(screen.queryByText(en.searchDialog.groups.projects)).toBeNull();
        });

        it("routes the Tasks, Scheduled, All done and How you do things page rows", async () => {
            const { actions } = await renderSearch();
            const cases = [
                [en.sidebar.tasks, "tasks"],
                [en.automations.title, "automations"],
                [en.deliverablesPage.title, "deliverables"],
                [en.userMenu.knowledge, "sops"],
            ] as const;
            for (const [label, id] of cases) {
                await type(label);
                fireEvent.click(within(group(en.searchDialog.groups.pages)).getByRole("option", { name: new RegExp(label) }));
                expect(actions.openLibraryPage).toHaveBeenLastCalledWith(id);
            }
        });

        it("opens Goals even without a goal result", async () => {
            const { actions } = await renderSearch();
            await type(en.searchDialog.groups.goals);
            fireEvent.click(within(group(en.searchDialog.groups.pages)).getByRole("option", { name: new RegExp(en.searchDialog.groups.goals) }));
            expect(actions.openGoal).toHaveBeenCalledWith("");
        });

        it("lists every Settings page from the registry by its name", async () => {
            const { actions } = await renderSearch();
            for (const entry of SETTINGS_PAGES) {
                const label = i18n.t(entry.labelKey as never, { ns: "settings" }) as string;
                await type(label);
                expect(rowTitles(en.searchDialog.groups.settings)).toContain(label);
                const row = within(group(en.searchDialog.groups.settings)).getAllByRole("option").find(
                    (candidate) => candidate.querySelector("span.truncate")?.textContent === label,
                )!;
                fireEvent.click(row);
                expect(actions.openSettings).toHaveBeenLastCalledWith(entry.id);
            }
            expect(actions.openSettings).toHaveBeenCalledTimes(SETTINGS_PAGES.length);
        });

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
            const row = within(group(en.searchDialog.groups.settings)).getAllByRole("option").find(
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

        it("runs the palette's actions: sidebar, Activity, know-how, About you", async () => {
            const { actions, onOpenChange } = await renderSearch();
            fireEvent.click(option(new RegExp(en.searchDialog.actions.toggleSidebar)));
            fireEvent.click(option(new RegExp(en.searchDialog.actions.activity)));
            fireEvent.click(option(new RegExp(en.searchDialog.actions.knowHow)));
            fireEvent.click(option(new RegExp(en.searchDialog.actions.aboutYou)));
            expect(actions.toggleSidebar).toHaveBeenCalledTimes(1);
            expect(actions.openActivity).toHaveBeenCalledWith();
            expect(actions.openKnowHow).toHaveBeenCalledWith();
            expect(actions.openAboutYou).toHaveBeenCalledWith();
            expect(onOpenChange).toHaveBeenCalledWith(false);
        });
    });

    describe("the new actions", () => {
        it("goes to the main chat and opens Settings", async () => {
            const { actions } = await renderSearch();
            fireEvent.click(option(new RegExp(en.searchDialog.mainChat)));
            fireEvent.click(option(new RegExp(en.searchDialog.actions.settings)));
            expect(actions.goToMainChat).toHaveBeenCalledTimes(1);
            expect(actions.openSettings).toHaveBeenCalledWith();
        });

        it("pauses observing, and offers Resume while paused", async () => {
            await renderSearch({ shortcutFor: (id) => (id === "pause-observing" ? "Ctrl+Alt+Shift+E" : undefined) });
            const pause = option(new RegExp(en.searchDialog.actions.pause));
            expect(pause).toHaveTextContent("Ctrl+Alt+Shift+E");
            await act(async () => {
                fireEvent.click(pause);
            });
            expect(binding("SetClosedEyesModeEnabled")).toHaveBeenCalledWith(true);

            cleanup();
            binding("IsClosedEyesModeEnabled").mockResolvedValue(true);
            await renderSearch();
            await act(async () => {
                fireEvent.click(option(new RegExp(en.searchDialog.actions.resume)));
            });
            expect(binding("SetClosedEyesModeEnabled")).toHaveBeenLastCalledWith(false);
        });

        it("makes the brief now and opens Today, or says when it can run again", async () => {
            binding("GenerateMobileBriefNow").mockResolvedValue({ status: "started", nextAllowedAt: "" });
            const { actions } = await renderSearch();
            await act(async () => {
                fireEvent.click(option(new RegExp(en.searchDialog.actions.brief)));
            });
            expect(binding("GenerateMobileBriefNow")).toHaveBeenCalledTimes(1);
            expect(actions.openToday).toHaveBeenCalledTimes(1);
            expect(toast.success).toHaveBeenCalledWith(en.searchDialog.brief.started);

            cleanup();
            binding("GenerateMobileBriefNow").mockResolvedValue({ status: "cooldown", nextAllowedAt: "" });
            const second = await renderSearch();
            await act(async () => {
                fireEvent.click(option(new RegExp(en.searchDialog.actions.brief)));
            });
            expect(second.actions.openToday).not.toHaveBeenCalled();
            expect(toast).toHaveBeenCalledWith(en.searchDialog.brief.cooldownSoon);

            // A real time is said as a wait in words, never a clock time.
            cleanup();
            binding("GenerateMobileBriefNow").mockResolvedValue({
                status: "cooldown",
                nextAllowedAt: new Date(Date.now() + 12 * 60_000 - 1000).toISOString(),
            });
            await renderSearch();
            await act(async () => {
                fireEvent.click(option(new RegExp(en.searchDialog.actions.brief)));
            });
            expect(toast).toHaveBeenLastCalledWith(en.searchDialog.brief.cooldownMinutes_other.replace("{{count}}", "12"));
            cleanup();
            binding("GenerateMobileBriefNow").mockResolvedValue({
                status: "cooldown",
                nextAllowedAt: new Date(Date.now() + 2 * 3_600_000).toISOString(),
            });
            await renderSearch();
            await act(async () => {
                fireEvent.click(option(new RegExp(en.searchDialog.actions.brief)));
            });
            expect(toast).toHaveBeenLastCalledWith(en.searchDialog.brief.cooldownHours_other.replace("{{count}}", "2"));
            expect(String(toast.mock.lastCall?.[0])).not.toMatch(/\d{1,2}:\d{2}/);
        });
    });

    describe("search over everything", () => {
        beforeEach(() => {
            binding("SearchEverything").mockResolvedValue(REMOTE);
        });

        it("groups results in the artboard's order", async () => {
            await renderSearch();
            await type("halcyon");
            await screen.findByText("Priya at Halcyon");
            expect(binding("SearchEverything")).toHaveBeenCalledWith("halcyon", expect.any(Number));
            expect(headings()).toEqual([
                en.searchDialog.groups.topics,
                en.searchDialog.groups.chats,
                en.searchDialog.groups.activity,
                en.searchDialog.groups.people,
                en.searchDialog.groups.goals,
                en.searchDialog.groups.knowHow,
                en.searchDialog.groups.aboutYou,
                en.searchDialog.groups.projects,
            ]);
            // The registry's topic, then the one search found; the local
            // title row is not repeated.
            expect(rowTitles(en.searchDialog.groups.topics)).toEqual(["Halcyon ERP sync", "Halcyon rollout"]);
            expect(rowTitles(en.searchDialog.groups.chats)).toEqual(["Halcyon ERP connector", "Halcyon asked to move the sync up"]);
            expect(screen.getByText(en.searchDialog.count_other.replace("{{count}}", String(screen.getAllByRole("option").length)))).toBeInTheDocument();
        });

        it("says when things are, in words, never a clock time", async () => {
            await renderSearch();
            await type("halcyon");
            const message = await screen.findByRole("option", { name: /Halcyon asked to move the sync up/ });
            // The main chat is named as in the sidebar.
            expect(message).toHaveTextContent(`${en.sidebar.mainChat} · ${en.searchDialog.when.today} · Halcyon ERP sync`);
            expect(option(/Reviewing Halcyon connector/)).toHaveTextContent(en.searchDialog.when.yesterday);
            for (const row of screen.getAllByRole("option")) {
                expect(row.textContent).not.toMatch(/\d{1,2}:\d{2}/);
            }
        });

        it("opens each result where it lives", async () => {
            const { actions } = await renderSearch();
            await type("halcyon");
            await screen.findByText("Priya at Halcyon");
            fireEvent.click(option(/Halcyon asked to move the sync up/));
            expect(actions.openChatMessage).toHaveBeenCalledWith("main-1", "msg-1");
            fireEvent.click(option(/Priya at Halcyon/));
            expect(actions.openPerson).toHaveBeenCalledWith("p-1");
            fireEvent.click(option(/Ship the Halcyon sync/));
            expect(actions.openGoal).toHaveBeenCalledWith("g-1");
            fireEvent.click(option(/Halcyon field mapping/));
            expect(actions.openKnowHow).toHaveBeenCalledWith("k-1");
            fireEvent.click(option(/Talks through Halcyon plans first/));
            expect(actions.openAboutYou).toHaveBeenCalledWith("m-1");
            fireEvent.click(option(/Reviewing Halcyon connector/));
            expect(actions.openActivity).toHaveBeenCalledWith(NOW - 86_400_000);
            fireEvent.click(option(/Halcyon kickoff/));
            expect(actions.openActivity).toHaveBeenLastCalledWith(NOW);
            // A topic opens beside Today, as the registry lists it.
            fireEvent.click(option(/Halcyon rollout/));
            expect(actions.openTopic).toHaveBeenCalledWith({ id: "t-2", name: "Halcyon rollout", kind: "work", pinned: false });
            fireEvent.click(within(group(en.searchDialog.groups.topics)).getByRole("option", { name: /Halcyon ERP sync/ }));
            expect(actions.openTopic).toHaveBeenLastCalledWith(expect.objectContaining({ id: "t-1", name: "Halcyon ERP sync", kind: "work" }));
        });

        it("shows what loaded and a quiet line for a place that failed", async () => {
            binding("SearchEverything").mockResolvedValue({
                ...REMOTE,
                groups: REMOTE.groups.filter((g) => g.place === "person"),
                failedPlaces: ["activity", "know_how"],
            });
            await renderSearch();
            await type("halcyon");
            await screen.findByText("Priya at Halcyon");
            expect(screen.getByTestId("search-failed")).toHaveTextContent(
                en.searchDialog.failed.replace("{{places}}", "Activity and How you do things"),
            );
        });

        it("keeps the local sources when the whole search fails", async () => {
            binding("SearchEverything").mockRejectedValue(new Error("down"));
            await renderSearch();
            await type("halcyon");
            const places = new Intl.ListFormat("en", { style: "long", type: "conjunction" }).format(
                ["topics", "chats", "Activity", "people", "goals", "How you do things", "About you"],
            );
            expect(await screen.findByTestId("search-failed")).toHaveTextContent(en.searchDialog.failed.replace("{{places}}", places));
            expect(rowTitles(en.searchDialog.groups.chats)).toEqual(["Halcyon ERP connector"]);
            expect(rowTitles(en.searchDialog.groups.topics)).toEqual(["Halcyon ERP sync"]);
        });

        it("shows nothing Isoform saw while observing is paused", async () => {
            binding("IsClosedEyesModeEnabled").mockResolvedValue(true);
            await renderSearch();
            await type("halcyon");
            await screen.findByText("Priya at Halcyon");
            expect(screen.queryByText(en.searchDialog.groups.activity)).toBeNull();
            expect(screen.queryByText("Reviewing Halcyon connector")).toBeNull();
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
        it("a privacy event withdraws the query, the results and the recents before the re-read lands", async () => {
            binding("SearchEverything").mockResolvedValue(REMOTE);
            await renderSearch();
            await type("halcyon");
            await screen.findByText("Priya at Halcyon");
            expect(input()).toHaveValue("halcyon");
            // The sweep's re-reads never answer; the withdrawal must not wait on them.
            binding("GetTodayTopics").mockImplementation(() => new Promise(() => {}));
            binding("ListHandoffs").mockImplementation(() => new Promise(() => {}));
            await act(async () => {
                fixture.listeners.get("memory:withdrawn")?.forEach((handler) => handler(undefined));
            });
            expect(input()).toHaveValue("");
            expect(screen.queryByText("Priya at Halcyon")).toBeNull();
            expect(screen.queryByText("Halcyon asked to move the sync up")).toBeNull();
            // The cached recents are withdrawn too; only the local chats remain.
            expect(rowTitles(en.searchDialog.groups.recent)).toEqual([
                "Pricing page",
                "Transcript: Zoom standup",
                "Halcyon ERP connector",
            ]);
        });

        it("an ordinary event keeps the painted query, results and topics while they re-read", async () => {
            binding("SearchEverything").mockResolvedValue(REMOTE);
            await renderSearch();
            await type("halcyon");
            await screen.findByText("Priya at Halcyon");
            binding("GetTodayTopics").mockImplementation(() => new Promise(() => {}));
            await act(async () => {
                fixture.listeners.get("topics:updated")?.forEach((handler) => handler(undefined));
            });
            expect(input()).toHaveValue("halcyon");
            expect(screen.getByText("Priya at Halcyon")).toBeInTheDocument();
            expect(screen.getByText("Halcyon ERP sync")).toBeInTheDocument();
        });
    });

    describe("Enter never changes something by accident", () => {
        it("a word that appears in an action finds results first: \"brief\" and Enter opens the chat, not a brief", async () => {
            binding("SearchEverything").mockResolvedValue({
                query: "brief",
                failedPlaces: [],
                groups: [{ place: "chat", results: [
                    { id: "msg-b", place: "chat", title: "Isoform", snippet: "brief notes for Friday", at: NOW, target: { kind: "chat_item", sessionId: "main-1", itemId: "msg-b" } },
                ] }],
            });
            const { actions } = await renderSearch();
            await type("brief");
            await screen.findByText("brief notes for Friday");
            expect(headings().indexOf(en.searchDialog.groups.chats)).toBeLessThan(headings().indexOf(en.searchDialog.groups.actions));
            fireEvent.keyDown(input(), { key: "Enter" });
            expect(actions.openChatMessage).toHaveBeenCalledWith("main-1", "msg-b");
            expect(binding("GenerateMobileBriefNow")).not.toHaveBeenCalled();
            expect(actions.openToday).not.toHaveBeenCalled();
        });

        it("\"observ\" and Enter does not pause observing", async () => {
            await renderSearch();
            await type("observ");
            fireEvent.keyDown(input(), { key: "Enter" });
            await act(async () => { await Promise.resolve(); });
            expect(binding("SetClosedEyesModeEnabled")).not.toHaveBeenCalled();
        });

        it("an action the query names leads but is not selected; Enter runs it only after moving to it", async () => {
            await renderSearch();
            await type("pause");
            expect(headings()[0]).toBe(en.searchDialog.groups.actions);
            const pause = option(new RegExp(en.searchDialog.actions.pause));
            expect(pause).toHaveAttribute("aria-selected", "false");
            fireEvent.keyDown(input(), { key: "Enter" });
            await act(async () => { await Promise.resolve(); });
            expect(binding("SetClosedEyesModeEnabled")).not.toHaveBeenCalled();
            fireEvent.keyDown(input(), { key: "ArrowDown" });
            expect(option(new RegExp(en.searchDialog.actions.pause))).toHaveAttribute("aria-selected", "true");
            await act(async () => { fireEvent.keyDown(input(), { key: "Enter" }); });
            expect(binding("SetClosedEyesModeEnabled")).toHaveBeenCalledWith(true);
        });

        it("with no recents, the empty box selects no action that changes something", async () => {
            binding("GetTodayTopics").mockResolvedValue([]);
            const actions = actionsMock();
            await act(async () => {
                render(<SearchDialog open onOpenChange={vi.fn()} projects={[]} actions={actions} />);
            });
            expect(option(new RegExp(en.searchDialog.actions.pause))).toHaveAttribute("aria-selected", "false");
            fireEvent.keyDown(input(), { key: "Enter" });
            await act(async () => { await Promise.resolve(); });
            expect(binding("SetClosedEyesModeEnabled")).not.toHaveBeenCalled();
            expect(binding("GenerateMobileBriefNow")).not.toHaveBeenCalled();
        });
    });

    describe("keys", () => {
        it("moves with the arrows and opens with Enter", async () => {
            const { actions } = await renderSearch();
            const rows = screen.getAllByRole("option");
            expect(rows[0]).toHaveAttribute("aria-selected", "true");
            fireEvent.keyDown(input(), { key: "ArrowDown" });
            fireEvent.keyDown(input(), { key: "ArrowDown" });
            expect(screen.getAllByRole("option")[2]).toHaveAttribute("aria-selected", "true");
            fireEvent.keyDown(input(), { key: "Enter" });
            expect(actions.openSession).toHaveBeenCalledWith(STANDALONE_CHAT_PROJECT_ID, "side-1");
        });

        it("selects the first row again when the query changes", async () => {
            await renderSearch();
            fireEvent.keyDown(input(), { key: "ArrowDown" });
            await type("invoices");
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
        it("is translated, the screen-reader title included", async () => {
            await i18n.changeLanguage("zh-CN");
            await renderSearch();
            expect(screen.getByRole("dialog", { name: zh.searchDialog.title })).toBeInTheDocument();
            expect(screen.getByPlaceholderText(zh.searchDialog.placeholder)).toBeInTheDocument();
            expect(screen.getByText(zh.searchDialog.actions.pause)).toBeInTheDocument();
            expect(screen.getByText(zh.searchDialog.mainChat)).toBeInTheDocument();
            expect(screen.getByText(zh.searchDialog.groups.recent)).toBeInTheDocument();
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
