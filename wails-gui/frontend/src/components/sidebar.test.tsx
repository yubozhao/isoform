import { act, cleanup, fireEvent, render, renderHook, screen, waitFor, within } from "@testing-library/react";
import { useState } from "react";
import { ProfilePage } from "./settings/pages/profile-page";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import i18n from "@/i18n";
import en from "@/i18n/locales/en/app.json";
import zh from "@/i18n/locales/zh-CN/app.json";
import { STANDALONE_CHAT_PROJECT_ID, createSession } from "@/lib/stream-helpers";
import type { Project, Session } from "@/types";
import sidebarSource from "./sidebar.tsx?raw";
import { Sidebar } from "./sidebar";
import { comboToDisplayString, useKeybindings } from "@/hooks/useKeybindings";

const mocks = vi.hoisted(() => ({
    about: { update: { status: "idle", version: "0.1.302" }, later: false },
    profile: { name: "Alex", email: "alex@example.com" } as Record<string, string> | null,
    refreshUserProfile: vi.fn(),
    ensureSelf: vi.fn(async () => ({})),
}));
vi.mock("@/hooks/useUserProfile", () => ({
    useUserProfile: () => ({ profile: mocks.profile, loading: false }),
    refreshUserProfile: mocks.refreshUserProfile,
}));
vi.mock("@/components/observing-status", () => ({
    ObservingStatus: () => <button aria-label="Background observing status">Observing in background</button>,
}));
vi.mock("@/lib/wails-shim-app", () => ({
    GetAboutInfo: vi.fn(async () => mocks.about),
    DismissUpdateUntilNextLaunch: vi.fn(async () => { mocks.about = { ...mocks.about, later: true }; }),
    RestartToUpdate: vi.fn(async () => {}),
    ListHandoffs: vi.fn(async () => []),
    EnsureSelfProfile: mocks.ensureSelf,
    // The sidebar's mark follows the status line and the brief.
    GetDoingNow: vi.fn(async () => ({ kind: "watching" })),
    GetLatestBrief: vi.fn(async () => ({ brief: null, building: false, enabled: true, nextAt: "", nextSlot: "" })),
}));
vi.mock("../../wailsjs/runtime/runtime", () => ({ EventsOn: vi.fn(() => () => {}) }));

function session(label: string, extra: Partial<Session> = {}): Session {
    return { ...createSession("claude-sonnet-4-5", "ws-1", label), updatedAt: 10, ...extra };
}

function buildProjects(sessions: Session[], activeSessionId: string | null): Project[] {
    return [
        {
            id: STANDALONE_CHAT_PROJECT_ID,
            name: "General Chat",
            path: "",
            activeSessionId,
            activeWorkspaceId: "ws-1",
            sortOrder: 0,
            workspaces: [
                {
                    id: "ws-1",
                    label: "General Chat",
                    isDefault: true,
                    activeSessionId,
                    sessions,
                    createdAt: 1,
                    updatedAt: 1,
                },
            ],
        } as unknown as Project,
    ];
}

function SidebarHarness({ sessions, active, collapsed = false, onOpenProfile }: { sessions: Session[]; active: string | null; collapsed?: boolean; onOpenProfile?: () => void }) {
    const noop = () => {};
    return (
        <Sidebar
            projects={buildProjects(sessions, active)}
            activeProjectId={STANDALONE_CHAT_PROJECT_ID}
            collapsed={collapsed}
            width={260}
            onToggleCollapse={noop}
            onOpenSearch={noop}
            onOpenSettings={noop}
            onOpenProfile={onOpenProfile}
            onOpenActivity={noop}
            onOpenKnowledge={noop}
            onOpenMemory={noop}
            onOpenPeople={noop}
            onOpenToday={noop}
            onOpenMainChat={noop}
            onSwitchSession={noop}
            onCloseSession={noop}
            onTogglePin={noop}
            onRenameSession={noop}
            onMarkSessionRead={noop}
            onMarkSessionUnread={noop}
            onForkSession={noop}
            appMode="chat"
        />
    );
}

function renderSidebar(sessions: Session[], activeSessionId: string | null, handlers: Partial<React.ComponentProps<typeof Sidebar>> = {}) {
    const props: React.ComponentProps<typeof Sidebar> = {
        projects: buildProjects(sessions, activeSessionId),
        activeProjectId: STANDALONE_CHAT_PROJECT_ID,
        collapsed: false,
        width: 260,
        onToggleCollapse: vi.fn(),
        onOpenSearch: vi.fn(),
        onOpenSettings: vi.fn(),
        onOpenActivity: vi.fn(),
        onOpenKnowledge: vi.fn(),
        onOpenMemory: vi.fn(),
        onOpenPeople: vi.fn(),
        onOpenToday: vi.fn(),
        onOpenMainChat: vi.fn(),
        onSwitchSession: vi.fn(),
        onCloseSession: vi.fn(),
        onTogglePin: vi.fn(),
        onRenameSession: vi.fn(),
        onMarkSessionRead: vi.fn(),
        onMarkSessionUnread: vi.fn(),
        onForkSession: vi.fn(),
        appMode: "chat",
        ...handlers,
    };
    render(<Sidebar {...props} />);
    return props;
}

describe("Sidebar (main chat, no side chats)", () => {
    beforeEach(async () => {
        await i18n.changeLanguage("en");
        localStorage.clear();
        mocks.about = { update: { status: "idle", version: "0.1.302" }, later: false };
        mocks.profile = { name: "Alex", email: "alex@example.com" };
        mocks.refreshUserProfile.mockClear();
        mocks.ensureSelf.mockClear();
    });

    it.each(["en", "zh-CN"])("renders the sidebar mark with the accent in %s", async (locale) => {
        await i18n.changeLanguage(locale);
        renderSidebar([], null);
        const lockup = screen.getByText("ISOFORM").closest("svg")!;
        expect(lockup).toHaveClass("text-accent");
        const mark = lockup.querySelector("svg")!;
        expect(mark).toHaveClass("text-accent");
        // At rest the mark is the logo's two paths; the animated drawing
        // (mark motion v1.0.1) stays hidden until something plays.
        expect(mark.querySelectorAll('[data-k="logo"] path:not([data-k="llid"])')).toHaveLength(2);
        expect(mark.querySelector('[data-k="kit"]')).toHaveStyle({ display: "none" });
        expect(lockup.querySelector("image, filter, linearGradient")).toBeNull();
        expect([...lockup.querySelectorAll("rect")].every((rect) => rect.parentElement?.localName === "clipPath")).toBe(true);
        for (const el of mark.querySelectorAll("[fill], [stroke]")) {
            for (const attr of ["fill", "stroke"]) {
                const value = el.getAttribute(attr);
                if (value) expect(["none", "var(--color-accent)"]).toContain(value);
            }
        }
        expect(lockup.querySelector(".logo-eye-hover")).not.toBeNull();
    });

    it("places one update card above observing and keeps the account status after Later", async () => {
        mocks.about = { update: { status: "update-downloaded", version: "0.1.302" }, later: false };
        renderSidebar([], null);
        const card = await screen.findByRole("region", { name: "Update ready" });
        const observing = screen.getByRole("button", { name: "Background observing status" });
        expect(card.compareDocumentPosition(observing) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
        fireEvent.click(screen.getByRole("button", { name: "Later" }));
        await waitFor(() => expect(screen.queryByRole("region", { name: "Update ready" })).not.toBeInTheDocument());
        expect(screen.getByText("Update ready")).toBeInTheDocument();
    });

    it("keeps the one update card mounted across the 240/56 rail and retains Later account status", async () => {
        mocks.about = { update: { status: "update-downloaded", version: "0.1.302" }, later: false };
        const { rerender } = render(<SidebarHarness sessions={[]} active={null} />);
        const card = await screen.findByRole("region", { name: "Update ready" });
        expect(screen.getAllByRole("region", { name: "Update ready" })).toHaveLength(1);
        expect(card).toBeVisible();
        expect(document.querySelector("aside")).toHaveStyle({ width: "240px" });
        rerender(<SidebarHarness sessions={[]} active={null} collapsed />);
        expect(document.querySelector("aside")).toHaveStyle({ width: "56px" });
        expect(card).not.toBeVisible();
        expect(screen.getByRole("button", { name: "Show sidebar" })).toBeVisible();
        rerender(<SidebarHarness sessions={[]} active={null} />);
        expect(await screen.findByRole("region", { name: "Update ready" })).toBe(card);
        expect(card).toBeVisible();
        fireEvent.click(screen.getByRole("button", { name: "Later" }));
        await waitFor(() => expect(screen.queryByRole("region", { name: "Update ready" })).not.toBeInTheDocument());
        expect(screen.getByText(en.userMenu.updateReady)).toBeVisible();
    });

    it.each([en, zh])("orders Search, Today, Main chat and Activity, then places observing above the user", async (locale) => {
        await i18n.changeLanguage(locale === en ? "en" : "zh-CN");
        renderSidebar([], null);
        const navigation = screen.getByRole("navigation", { name: locale.sidebar.mainNavigation });
        expect(within(navigation).getAllByRole("button").map((row) => row.textContent?.replace(/(?:Ctrl K|⌘K)$/, ""))).toEqual([
            locale.sidebar.search, locale.sidebar.today, locale.sidebar.mainChat, locale.sidebar.tasks, locale.sidebar.activity,
        ]);
        const observing = screen.getByRole("button", { name: "Background observing status" });
        const user = document.querySelector('.group\\/user')!;
        expect(navigation.compareDocumentPosition(observing) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
        expect(observing.compareDocumentPosition(user) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
        expect(navigation.contains(observing)).toBe(false);
    });

    it.each([en, zh])("keeps Tasks between Chat and Activity", async (locale) => {
        await i18n.changeLanguage(locale === en ? "en" : "zh-CN");
        const onOpenTasks = vi.fn();
        renderSidebar([], null, { onOpenTasks, tasksOpen: true });
        const navigation = screen.getByRole("navigation", { name: locale.sidebar.mainNavigation });
        const rows = within(navigation).getAllByRole("button");
        expect(rows.map((row) => row.textContent?.replace(/(?:Ctrl K|⌘K)$/, ""))).toEqual([
            locale.sidebar.search, locale.sidebar.today, locale.sidebar.mainChat, locale.sidebar.tasks, locale.sidebar.activity,
        ]);
        const tasks = rows[3];
        expect(tasks.getAttribute("aria-current")).toBe("page");
        fireEvent.click(tasks);
        expect(onOpenTasks).toHaveBeenCalledTimes(1);
    });

    it.each([false, true])("keeps the five primary rows, then the You section, in order when collapsed=%s", (collapsed) => {
        const onOpenTasks = vi.fn();
        const props = renderSidebar([], null, { collapsed, onOpenTasks, tasksOpen: true });
        const primary = [en.sidebar.search, en.sidebar.today, en.sidebar.mainChat, en.sidebar.tasks, en.sidebar.activity];
        const you = [en.userMenu.memory, en.userMenu.knowledge, en.userMenu.people];
        let rows: HTMLElement[];
        if (collapsed) {
            const rail = document.querySelector<HTMLElement>("aside > div > div")!;
            const children = Array.from(rail.children).slice(1, 10) as HTMLElement[];
            // The rail puts one divider between the primary rows and You.
            expect(children.map((child) => child.hasAttribute("data-sidebar-divider"))).toEqual([false, false, false, false, false, true, false, false, false]);
            rows = children.filter((child) => child.tagName === "BUTTON");
            expect(rows.map((row) => row.getAttribute("aria-label"))).toEqual([...primary, ...you]);
        } else {
            const navigation = screen.getByRole("navigation", { name: en.sidebar.mainNavigation });
            const section = screen.getByRole("navigation", { name: en.sidebar.you });
            expect(within(section).getByText(en.sidebar.you)).toBeInTheDocument();
            expect(navigation.compareDocumentPosition(section) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
            expect(within(navigation).getAllByRole("button").map((row) => row.textContent?.replace(/(?:Ctrl K|⌘K)$/, ""))).toEqual(primary);
            expect(within(section).getAllByRole("button").map((row) => row.textContent)).toEqual(you);
            rows = [...within(navigation).getAllByRole("button"), ...within(section).getAllByRole("button")];
        }
        expect(screen.queryByRole("button", { name: "Goals" })).toBeNull();
        fireEvent.click(rows[3]);
        expect(onOpenTasks).toHaveBeenCalledOnce();
        expect(rows[3]).toHaveAttribute("aria-current", "page");
        for (const [index, callback] of [[5, props.onOpenMemory], [6, props.onOpenKnowledge], [7, props.onOpenPeople]] as const) {
            fireEvent.click(rows[index]);
            expect(callback).toHaveBeenCalledOnce();
        }
    });

    it.each(["memory", "knowledge", "people"] as const)("marks the %s You row current with the rounded fill only", (appMode) => {
        renderSidebar([], null, { appMode });
        const section = screen.getByRole("navigation", { name: en.sidebar.you });
        const current = within(section).getAllByRole("button").filter((row) => row.getAttribute("aria-current") === "page");
        expect(current).toHaveLength(1);
        expect(current[0]).toHaveTextContent(en.userMenu[appMode]);
        expect(current[0].className).not.toMatch(/border-l/);
    });

    it("pins Search, Today and Main chat at the top and lists no chat rows but pinned ones", () => {
        const main = session("Isoform", { localId: "main-1", kind: "main", updatedAt: 99 });
        const side = session("Pricing page", { localId: "side-1", kind: "side", parentId: "main-1", updatedAt: 50 });
        const legacy = session("Transcript: Zoom - Oct 1 09:00", { localId: "legacy-1", updatedAt: 40 });
        const props = renderSidebar([main, side, legacy], "main-1");

        const mainRow = document.querySelector("[data-main-chat-row]") as HTMLElement;
        expect(mainRow).not.toBeNull();
        expect(mainRow).toHaveTextContent(en.sidebar.mainChat);
        // The wordmark above already says Isoform; the row says what it is.
        expect(en.sidebar.mainChat).toBe("Chat");
        expect(mainRow).not.toHaveTextContent("Isoform");
        expect(mainRow.getAttribute("aria-current")).toBe("page");
        expect(screen.getByText(en.sidebar.today)).toBeInTheDocument();
        expect(screen.getByText(en.sidebar.search)).toBeInTheDocument();

        // Neither the main chat nor an unpinned chat is a session row: old
        // side chats and legacy chats are reached from Search only (its
        // dialog lists them: search-dialog.test.tsx).
        expect(document.querySelector("[data-session-row='main-1']")).toBeNull();
        expect(document.querySelector("[data-session-row='side-1']")).toBeNull();
        expect(document.querySelector("[data-session-row='legacy-1']")).toBeNull();
        // The Search row opens the one Search and commands dialog.
        fireEvent.click(screen.getByText(en.sidebar.search));
        expect(props.onOpenSearch).toHaveBeenCalledTimes(1);

        fireEvent.click(mainRow);
        expect(props.onOpenMainChat).toHaveBeenCalledTimes(1);
    });

    it("offers no side chat anywhere: no New side chat row, no Side chats section, no strings", () => {
        const main = session("Isoform", { localId: "main-1", kind: "main" });
        const emptySide = session("Session 09:00", { localId: "side-2", kind: "side", parentId: "main-1" });
        renderSidebar([main, emptySide], "side-2");

        expect(document.querySelector("[data-new-side-chat-row]")).toBeNull();
        expect(document.querySelector("[data-sidebar-section='sideChats']")).toBeNull();
        expect(screen.queryByText(/side chat/i)).toBeNull();
        expect(sidebarSource).not.toMatch(/sideChat|SideChat|side chat/);
        for (const locale of [en, zh]) {
            expect(Object.keys(locale.sidebar).join(" ")).not.toMatch(/sideChat/i);
            expect(JSON.stringify(locale)).not.toMatch(/side chat|分支对话/);
        }
        // The blank side chat the user still sits on is not a row either.
        expect(document.querySelector("[data-session-row='side-2']")).toBeNull();
        const mainRow = document.querySelector("[data-main-chat-row]") as HTMLElement;
        expect(mainRow.getAttribute("aria-current")).toBeNull();
    });

    it("opens the Activity page from its row below Main chat, and marks it current", () => {
        const props = renderSidebar([], null, { activityOpen: false });
        const row = document.querySelector("[data-activity-row]") as HTMLElement;
        expect(row).not.toBeNull();
        expect(row).toHaveTextContent(en.sidebar.activity);
        expect(row.getAttribute("aria-current")).toBeNull();
        // The row sits right below Main chat.
        const mainRow = document.querySelector("[data-main-chat-row]") as HTMLElement;
        expect(mainRow.compareDocumentPosition(row) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
        fireEvent.click(row);
        expect(props.onOpenActivity).toHaveBeenCalledTimes(1);

        cleanup();
        renderSidebar([], null, { activityOpen: true });
        expect(
            (document.querySelector("[data-activity-row]") as HTMLElement).getAttribute("aria-current"),
        ).toBe("page");
    });

    it("has no Library anywhere: no section, no rows, no strings", () => {
        const main = session("Isoform", { localId: "main-1", kind: "main" });
        const pinned = session("Trip plan", { localId: "side-p", kind: "side", parentId: "main-1", pinned: true, updatedAt: 70 });
        renderSidebar([main, pinned], "main-1");
        // Library is gone (sidebar handoff 1.0.0, section 3): its content
        // lives on Tasks and the You pages; the sections are You and Pinned.
        expect(Array.from(document.querySelectorAll("[data-sidebar-section]")).map((el) => el.getAttribute("data-sidebar-section"))).toEqual(["you", "pinned"]);
        fireEvent.click(document.querySelector('.group\\/user')!);
        for (const word of ["Library", "Upcoming", "One-time work", "Playbooks", "Deliverables", "Goals", "What Isoform knows"]) {
            expect(screen.queryByText(word)).toBeNull();
        }
        expect(sidebarSource).not.toMatch(/onOpenLibrary|LibraryMenu|"library"|sidebar\.library|userMenu\.library/);
        for (const locale of [en, zh]) {
            for (const key of ["library", "automationNav", "handoffsNav", "sopsNav", "deliverablesNav", "goals"]) expect(locale.sidebar).not.toHaveProperty(key);
            for (const key of ["library", "libraryDescription", "deliverables", "knows"]) expect(locale.userMenu).not.toHaveProperty(key);
        }
    });

    it("lists pinned chats under Pinned and finds every other chat from Search", () => {
        const main = session("Isoform", { localId: "main-1", kind: "main" });
        const blank = session("Session 09:00", { localId: "side-b", kind: "side", parentId: "main-1" });
        const other = session("Pricing page", { localId: "side-1", kind: "side", parentId: "main-1", pinned: true, updatedAt: 50 });
        render(<SidebarHarness sessions={[main, blank, other]} active="side-b" />);
        expect(document.querySelector("[data-session-row='side-b']")).toBeNull();
        expect(document.querySelector("[data-session-row='side-1']")).not.toBeNull();
        expect(document.querySelectorAll("[aria-current='page']")).toHaveLength(0);
    });

    it("opens a pinned chat with Enter and Space and still opens it on click", async () => {
        const user = userEvent.setup();
        const main = session("Isoform", { localId: "main-1", kind: "main" });
        const pinned = session("Trip plan", { localId: "side-p", kind: "side", parentId: "main-1", pinned: true, updatedAt: 70 });
        const props = renderSidebar([main, pinned], null);

        const row = document.querySelector("[data-session-row='side-p']") as HTMLElement;
        expect(row).not.toBeNull();
        expect(row.getAttribute("role")).toBe("button");
        expect(row.getAttribute("tabindex")).toBe("0");
        expect(row.className).toMatch(/focus-visible:ring/);

        // Tab through the sidebar until the row holds focus.
        for (let i = 0; i < 30 && document.activeElement !== row; i++) {
            await user.tab();
        }
        expect(document.activeElement).toBe(row);
        await user.keyboard("{Enter}");
        expect(props.onSwitchSession).toHaveBeenCalledTimes(1);
        expect(props.onSwitchSession).toHaveBeenLastCalledWith(STANDALONE_CHAT_PROJECT_ID, "side-p");
        await user.keyboard(" ");
        expect(props.onSwitchSession).toHaveBeenCalledTimes(2);
        expect(props.onSwitchSession).toHaveBeenLastCalledWith(STANDALONE_CHAT_PROJECT_ID, "side-p");

        // The click path is unchanged.
        fireEvent.click(row);
        expect(props.onSwitchSession).toHaveBeenCalledTimes(3);
    });

    it("shows the user's name at the bottom left, or You until there is one", () => {
        const { unmount } = render(<SidebarHarness sessions={[]} active={null} />);
        expect(screen.getByText("Alex")).toBeInTheDocument();
        unmount();
        mocks.profile = null;
        render(<SidebarHarness sessions={[]} active={null} />);
        expect(within(document.querySelector<HTMLElement>('.group\\/user')!).getByText(en.sidebar.you)).toBeInTheDocument();
        expect(screen.queryByText("User")).toBeNull();
    });

    it("changes the name from the bottom-left menu", async () => {
        function AccountProfileHarness() {
            const [profileOpen, setProfileOpen] = useState(false);
            return <>
                <SidebarHarness sessions={[]} active={null} onOpenProfile={() => setProfileOpen(true)} />
                {profileOpen && <ProfilePage />}
            </>;
        }
        render(<AccountProfileHarness />);
        fireEvent.click(screen.getByText("Alex"));
        // The menu header now routes to Profile rather than an inline dialog.
        fireEvent.click(screen.getByRole("button", { name: "Alex" }));
        const input = await screen.findByLabelText(en.userMenu.nameLabel);
        expect(input).toHaveValue("Alex");
        mocks.ensureSelf.mockImplementationOnce(async () => {
            mocks.profile = { ...mocks.profile, display_name: "Alex User" };
            return {};
        });
        fireEvent.change(input, { target: { value: "  Alex User " } });
        await act(async () => fireEvent.click(screen.getByText(en.userMenu.saveName)));
        expect(mocks.ensureSelf).toHaveBeenCalledWith("Alex User", []);
        expect(mocks.refreshUserProfile).toHaveBeenCalledTimes(1);
        expect(input).toHaveValue("Alex User");

        mocks.ensureSelf.mockRejectedValueOnce(new Error("store closed"));
        fireEvent.change(input, { target: { value: "Alex Example" } });
        await act(async () => fireEvent.click(screen.getByText(en.userMenu.saveName)));
        expect(screen.getByRole("alert")).toHaveTextContent(en.userMenu.nameSaveError);
        expect(input).toHaveValue("Alex Example");
    });

    it.each([en, zh])("opens Profile from the menu header and keeps the account menu to four items", async (locale) => {
        await i18n.changeLanguage(locale === en ? "en" : "zh-CN");
        const props = renderSidebar([], null, { onOpenProfile: vi.fn(), onOpenShortcuts: vi.fn(), onOpenHelp: vi.fn() });
        fireEvent.click(screen.getByText("Alex"));
        const header = screen.getByRole("button", { name: "Alex" });
        const menu = header.parentElement!;
        // The name, Settings, Keyboard shortcuts, Help and feedback; the You
        // rows moved to the sidebar and Library is gone.
        expect(within(menu).getAllByRole("button").map((item) => item.textContent?.replace(/(?:Ctrl ,|⌘,|Ctrl \/|⌘\/)$/, ""))).toEqual(["AAlex", locale.userMenu.settings, locale.userMenu.shortcuts, locale.userMenu.help]);
        for (const text of [locale.userMenu.memory, locale.userMenu.knowledge, locale.userMenu.people, locale.userMenu.aboutDescription, locale.userMenu.changeName, locale.userMenu.activity]) {
            expect(within(menu).queryByText(text)).toBeNull();
        }
        expect(within(menu).queryByText("alex@example.com")).toBeNull();
        fireEvent.click(header);
        expect(props.onOpenProfile).toHaveBeenCalledOnce();
        for (const [label, callback] of [[locale.userMenu.settings, props.onOpenSettings], [locale.userMenu.shortcuts, props.onOpenShortcuts], [locale.userMenu.help, props.onOpenHelp]] as const) {
            fireEvent.click(document.querySelector('.group\\/user')!);
            fireEvent.click(screen.getByText(label));
            expect(callback).toHaveBeenCalledOnce();
        }
    });

    it("keeps every account-menu description within the designed locale limits", () => {
        const overLimit = (userMenu: typeof en.userMenu, limit: number) => Object.entries(userMenu)
            .filter(([key, value]) => key.endsWith("Description") && value.length > limit)
            .map(([key]) => key);
        expect(overLimit(en.userMenu, 36)).toEqual([]);
        expect(overLimit(zh.userMenu, 16)).toEqual([]);
    });

    it.each([en, zh])("gives each You row its description as a tooltip", async (locale) => {
        await i18n.changeLanguage(locale === en ? "en" : "zh-CN");
        renderSidebar([], null);
        const section = screen.getByRole("navigation", { name: locale.sidebar.you });
        expect(within(section).getAllByRole("button").map((row) => row.getAttribute("title"))).toEqual([
            locale.userMenu.aboutDescription, locale.userMenu.knowHowDescription, locale.userMenu.peopleDescription,
        ]);
    });

    it.each([en, zh])("shows saved rebindings in Search and the account menu", async (locale) => {
        await i18n.changeLanguage(locale === en ? "en" : "zh-CN");
        const overrides = {
            "command-palette": { ctrl: true, shift: true, key: "f" },
            "open-settings": { alt: true, key: "s" },
            "keyboard-shortcuts": { ctrl: true, alt: true, key: "h" },
        };
        localStorage.setItem("yansu.keybindings", JSON.stringify(overrides));
        const { result } = renderHook(() => useKeybindings());
        const shortcutFor = (id: string) => {
            const combo = result.current.getCombo(id);
            return combo ? comboToDisplayString(combo) : undefined;
        };
        renderSidebar([], null, { shortcutFor });
        const navigation = screen.getByRole("navigation", { name: locale.sidebar.mainNavigation });
        expect(within(within(navigation).getByText(locale.sidebar.search).closest("button")!).getByText(shortcutFor("command-palette")!)).toBeInTheDocument();
        fireEvent.click(screen.getByText("Alex"));
        const menu = screen.getByRole("button", { name: "Alex" }).parentElement!;
        for (const [label, id] of [[locale.userMenu.settings, "open-settings"], [locale.userMenu.shortcuts, "keyboard-shortcuts"]]) {
            expect(within(within(menu).getByText(label).closest("button")!).getByText(shortcutFor(id)!)).toBeInTheDocument();
        }
        for (const hint of [locale.sidebar.searchShortcutMac, locale.sidebar.searchShortcutOther, locale.userMenu.settingsShortcutMac, locale.userMenu.settingsShortcutOther, locale.userMenu.shortcutsShortcutMac, locale.userMenu.shortcutsShortcutOther]) {
            expect(screen.queryByText(hint)).toBeNull();
        }
    });

    it("uses the board widths, waiting count and collapsed pinned circles", () => {
        const { unmount } = render(<SidebarHarness sessions={[]} active={null} />);
        expect(document.querySelector("aside")).toHaveStyle({ width: "240px" });
        unmount();
        const props = renderSidebar([], null, { collapsed: true, waitingCount: 2, pinnedTopics: [{ id: "northwind", name: "Northwind kickoff" }], onOpenTopic: vi.fn() });
        expect(document.querySelector("aside")).toHaveStyle({ width: "56px" });
        expect(screen.getByTitle("Show sidebar")).toBeInTheDocument();
        expect(screen.getByLabelText("2 waiting on you")).toHaveTextContent("2");
        expect(screen.getByTitle("2 waiting on you")).toHaveAttribute("aria-label", en.sidebar.tasks);
        const topic = screen.getByRole("button", { name: "Northwind kickoff" });
        expect(topic).toHaveTextContent("N");
        expect(topic).toHaveClass("size-7", "rounded-full");
        fireEvent.click(topic);
        expect(props.onOpenTopic).toHaveBeenCalledWith("northwind");
    });

    it.each([[en, false], [en, true], [zh, false], [zh, true]] as const)("opens a 320 px floating account layer with four items", async (locale, collapsed) => {
        await i18n.changeLanguage(locale === en ? "en" : "zh-CN");
        renderSidebar([], null, { collapsed });
        const account = screen.getByRole("button", { name: i18n.t("app:userMenu.account", { name: "Alex" }) });
        expect(account.parentElement).toHaveClass("px-3", "w-full");
        if (collapsed) {
            expect(account.textContent).toBe("A");
            expect(within(account).getByText("A").parentElement).toHaveClass("size-7");
        }
        fireEvent.click(account);
        const layer = screen.getByRole("button", { name: "Alex" }).parentElement!;
        expect(layer).toHaveClass("w-[320px]");
        expect(layer).toHaveAttribute("data-side", "top");
        expect(layer).toHaveAttribute("data-align", "start");
        expect(document.querySelector("aside")!.contains(layer)).toBe(false);
        expect(account).toBeVisible();
        expect(document.body.style.pointerEvents).not.toBe("none");
        expect(within(layer).getAllByRole("button")).toHaveLength(4);
    });

    it.each([0, 2, 9, 10, 23])("keeps the folded waiting badge compact for %i items", (count) => {
        renderSidebar([], null, { collapsed: true, waitingCount: count });
        if (count === 0) {
            expect(screen.queryByLabelText(i18n.t("app:sidebar.waiting", { count }))).toBeNull();
            return;
        }
        const badge = screen.getByLabelText(i18n.t("app:sidebar.waiting", { count }));
        expect(badge).toHaveTextContent(count > 9 ? "9+" : String(count));
        expect(badge).toHaveClass("h-4", "min-w-4", "bg-waiting-badge", "text-waiting-badge-fg", "-right-2", "-top-2");
        expect(badge.parentElement).toContainElement(badge.parentElement!.querySelector("svg"));
        expect(badge.parentElement).toHaveClass("relative", "size-[15px]");
        expect(badge.closest("button")).toHaveAttribute("title", i18n.t("app:sidebar.waiting", { count }));
    });

    it("never shows a 0 badge on Tasks and puts the unread dot on Chat", () => {
        renderSidebar([], null, { waitingCount: 0 });
        const tasks = document.querySelector("[data-tasks-row]") as HTMLElement;
        expect(tasks.textContent).toBe(en.sidebar.tasks);
        expect(screen.queryByText("0")).toBeNull();
        expect(document.querySelector("[data-chat-dot]")).toBeNull();
        cleanup();
        renderSidebar([], null, { waitingCount: 3, chatUnread: true });
        expect(within(document.querySelector("[data-tasks-row]") as HTMLElement).getByText("3")).toBeInTheDocument();
        expect((document.querySelector("[data-main-chat-row]") as HTMLElement).querySelector("[data-chat-dot]")).not.toBeNull();
    });

    it("shows a waiting badge in the open sidebar and an update line only when ready", () => {
        renderSidebar([], null, { waitingCount: 2, updateReady: true });
        expect(screen.getByTitle("2 waiting on you")).toHaveTextContent("2");
        expect(screen.getByText(en.userMenu.updateReady)).toBeInTheDocument();
    });

    it("keeps both locales in step and has no left-edge accent bar on the fixed rows", () => {
        expect(Object.keys(zh.sidebar).sort()).toEqual(Object.keys(en.sidebar).sort());
        expect(Object.keys(zh.userMenu).sort()).toEqual(Object.keys(en.userMenu).sort());
        expect(en.sidebar.newChat).toBe("New chat");
        expect(en.sidebar).not.toHaveProperty("recent");
        expect(zh.sidebar.mainChat).not.toBe("Isoform");
        expect(sidebarSource).not.toMatch(/data-main-chat-row[^>]*border-l/);
        expect(sidebarSource).not.toMatch(/w-0\.5|w-\[2px\]/);
    });
});

describe("Sidebar pinned topics (RC 1)", () => {
    it("lists pinned topics under Pinned, with no Topics row, and opens one on a click", () => {
        const props = renderSidebar([], null, {
            pinnedTopics: [{ id: "halcyon", name: "Halcyon ERP sync" }],
            onOpenTopic: vi.fn(),
            onUnpinTopic: vi.fn(),
        });
        const pinned = document.querySelector("[data-sidebar-section='pinned']") as HTMLElement;
        expect(pinned).not.toBeNull();
        const row = within(pinned).getByRole("button", { name: "Halcyon ERP sync" });
        fireEvent.click(row);
        expect(props.onOpenTopic).toHaveBeenCalledWith("halcyon");
        expect(screen.queryByText(/^Topics$/)).not.toBeInTheDocument();
    });

    it("unpins a topic from its menu", async () => {
        const props = renderSidebar([], null, {
            pinnedTopics: [{ id: "halcyon", name: "Halcyon ERP sync" }],
            onOpenTopic: vi.fn(),
            onUnpinTopic: vi.fn(),
        });
        fireEvent.contextMenu(screen.getByRole("button", { name: "Halcyon ERP sync" }));
        fireEvent.click(await screen.findByRole("menuitem", { name: "Unpin" }));
        expect(props.onUnpinTopic).toHaveBeenCalledWith("halcyon");
    });

    it("shows no Pinned section for topics when nothing can open them", () => {
        renderSidebar([], null, { pinnedTopics: [{ id: "halcyon", name: "Halcyon ERP sync" }] });
        expect(document.querySelector("[data-sidebar-section='pinned']")).toBeNull();
    });
});


describe("Collapsed pinned selection and unread state", () => {
    it("fills only the active session circle and preserves unread state", () => {
        const active = session("Active", { localId: "active", pinned: true });
        const unread = session("Unread", { localId: "unread", pinned: true });
        renderSidebar([active, unread], "active", { collapsed: true, unreadSessionIds: new Set(["unread"]) });
        const selected = document.querySelector('[data-session-row="active"]')!;
        const other = document.querySelector('[data-session-row="unread"]')!;
        expect(selected).toHaveClass("bg-surface-active");
        expect(other).not.toHaveClass("bg-surface-active", "bg-active-row-bg");
        expect(within(other as HTMLElement).getByLabelText(en.sidebar.unread)).toBeInTheDocument();
    });
    it("fills only the topic actually selected in Today", () => {
        renderSidebar([], null, { collapsed: true, appMode: "today", activeTopicId: "b", pinnedTopics: [{ id: "a", name: "Alpha" }, { id: "b", name: "Beta" }], onOpenTopic: vi.fn() });
        expect(screen.getByRole("button", { name: "Alpha" })).not.toHaveClass("bg-surface-active", "bg-active-row-bg");
        expect(screen.getByRole("button", { name: "Beta" })).toHaveClass("bg-surface-active");
        expect(screen.getByRole("button", { name: "Beta" })).toHaveAttribute("aria-current", "page");
    });
});

it.each(['paper', 'onedark'])('keeps the legacy ground marker on the sidebar in %s', (theme) => {
    document.documentElement.dataset.theme = theme;
    const { container } = render(<SidebarHarness sessions={[]} active={null} />);
    expect(container.querySelector('aside')).toHaveClass('patina-ground');
    expect(container.querySelectorAll('.patina-ground')).toHaveLength(1);
});

it.each(['today', 'activity'] as const)('uses a rounded fill and violet for the active %s tab, reserving rings for keyboard focus', async (appMode) => {
    await i18n.changeLanguage('en');
    renderSidebar([], null, { appMode, todayOpen: appMode === "today", activityOpen: appMode === "activity" });
    const row = screen.getByRole('button', { name: appMode === 'today' ? 'Today' : 'Activity' });
    expect(row).toHaveAttribute('aria-current', 'page');
    expect(row).not.toHaveFocus();
    expect(row).toHaveClass('bg-background', 'dark:bg-panel', 'text-accent');
    expect(row.className.split(/\s+/).filter(value => /(?:ring|outline|border-accent)/.test(value)).every(value => value.startsWith('focus-visible:'))).toBe(true);
    expect(row).toHaveClass('focus-visible:ring-2');
});

// K8: with SessionTabs gone, the sidebar is where a chat opens, pins,
// renames and is deleted.
describe("Sidebar chat management without session tabs (K8)", () => {
    beforeEach(async () => {
        await i18n.changeLanguage("en");
        localStorage.clear();
    });

    it("pins, renames and deletes a chat from its row's menu", async () => {
        const main = session("Isoform", { localId: "main-1", kind: "main" });
        const pinned = session("Trip plan", { localId: "side-p", kind: "side", parentId: "main-1", pinned: true, updatedAt: 70 });
        const props = renderSidebar([main, pinned], null);
        const row = document.querySelector("[data-session-row='side-p']") as HTMLElement;
        fireEvent.click(row);
        expect(props.onSwitchSession).toHaveBeenCalledWith(STANDALONE_CHAT_PROJECT_ID, "side-p");

        fireEvent.contextMenu(row);
        fireEvent.click(await screen.findByRole("menuitem", { name: en.sessionItem.unpin }));
        expect(props.onTogglePin).toHaveBeenCalledWith(STANDALONE_CHAT_PROJECT_ID, "side-p");

        fireEvent.contextMenu(row);
        fireEvent.click(await screen.findByRole("menuitem", { name: en.sessionItem.rename }));
        const field = await screen.findByDisplayValue("Trip plan");
        fireEvent.change(field, { target: { value: "Lisbon trip" } });
        // Enter blurs the field, and the blur commits the new name.
        fireEvent.keyDown(field, { key: "Enter" });
        fireEvent.blur(field);
        await waitFor(() => expect(props.onRenameSession).toHaveBeenCalledWith(STANDALONE_CHAT_PROJECT_ID, "side-p", "Lisbon trip"));

        fireEvent.contextMenu(document.querySelector("[data-session-row='side-p']") as HTMLElement);
        fireEvent.click(await screen.findByRole("menuitem", { name: en.sessionItem.delete }));
        expect(props.onCloseSession).toHaveBeenCalledWith(STANDALONE_CHAT_PROJECT_ID, "side-p");
    });
});

describe('sidebar Chat row New chat (chat-review v1.0.2 #9)', () => {
    it('offers a labelled + on the Chat row that starts a new chat', () => {
        const onNewChat = vi.fn();
        renderSidebar([], null, { onNewChat });
        const plus = document.querySelector('[data-sidebar-new-chat]') as HTMLElement;
        expect(plus).not.toBeNull();
        expect(plus).toHaveAttribute('aria-label');
        expect(plus.querySelector('svg.lucide-plus')).not.toBeNull();
        expect(plus.className).toMatch(/group-hover\/chat:opacity-100/);
        expect(plus.className).toMatch(/group-focus-within\/chat:opacity-100/);
        expect(plus.parentElement?.querySelector('[data-main-chat-row]')).not.toBeNull();
        fireEvent.click(plus);
        expect(onNewChat).toHaveBeenCalledTimes(1);
    });
});

describe("The selected row is the page on screen (today-review v1.1.14 D9)", () => {
    const filled = /(^|\s)(bg-background|bg-surface-active)(\s|$)/;
    const pages = [
        ["today", { todayOpen: true }, en.sidebar.today],
        ["tasks", { tasksOpen: true }, en.sidebar.tasks],
        ["activity", { activityOpen: true }, en.sidebar.activity],
        ["memory", {}, en.userMenu.memory],
        ["knowledge", {}, en.userMenu.knowledge],
        ["people", {}, en.userMenu.people],
    ] as const;
    const labels = [en.sidebar.today, en.sidebar.mainChat, ...pages.slice(1).map(([, , label]) => label)];
    const rows = () => labels.map((label) => screen.getAllByRole("button", { name: label })[0]);
    const expectSelected = (label: string) => {
        const current = rows().filter((row) => row.getAttribute("aria-current") === "page");
        expect(current).toHaveLength(1);
        expect(current[0]).toHaveTextContent(label);
        expect(rows().filter((row) => filled.test(row.className))).toEqual(current);
    };

    it("marks only the page on screen from the first render and on every switch, with no color transition to wait on", () => {
        const props = renderSidebar([], null, { appMode: "today", todayOpen: true });
        expectSelected(en.sidebar.today);
        cleanup();
        const { rerender } = render(<Sidebar {...props} />);
        for (const [appMode, open, label] of [...pages, pages[0]]) {
            rerender(<Sidebar {...props} todayOpen={false} tasksOpen={false} activityOpen={false} {...open} appMode={appMode} />);
            expectSelected(label);
        }
        for (const row of rows()) expect(row.className).not.toMatch(/\btransition-(colors|all)\b/);
    });

    it.each(pages)("opens on %s with that row selected in the first frame, never Chat", (appMode, open, label) => {
        renderSidebar([], null, { appMode, ...open });
        expectSelected(label);
        expect(screen.getAllByRole("button", { name: en.sidebar.mainChat })[0]).not.toHaveAttribute("aria-current");
    });
});
