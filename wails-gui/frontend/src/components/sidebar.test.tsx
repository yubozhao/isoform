import { act, cleanup, fireEvent, render, renderHook, screen, waitFor, within } from "@testing-library/react";
import { useState } from "react";
import { ProfilePage } from "./settings/pages/profile-page";
import { beforeEach, describe, expect, it, vi } from "vitest";
import i18n from "@/i18n";
import en from "@/i18n/locales/en/app.json";
import zh from "@/i18n/locales/zh-CN/app.json";
import { STANDALONE_CHAT_PROJECT_ID } from "@/lib/stream-helpers";
import type { Project } from "@/types";
import sidebarSource from "./sidebar.tsx?raw";
import footSource from "./sidebar-foot.tsx?raw";
import { Sidebar } from "./sidebar";
import { phoneLineState, watchHeardTime, watchLineState } from "./sidebar-foot";
import { PAGE_KEYS, PAUSE_OBSERVING_COMBO, comboToDisplayString, useKeybindings } from "@/hooks/useKeybindings";
import { axeViolations } from "@/test-utils/axe";

const mocks = vi.hoisted(() => ({
    about: { update: { status: "idle", version: "0.1.302" }, later: false },
    profile: { name: "Alex", email: "alex@example.com" } as Record<string, string> | null,
    refreshUserProfile: vi.fn(),
    ensureSelf: vi.fn(async () => ({})),
    mobile: null as unknown,
    watch: [] as unknown[],
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
    EnsureSelfProfile: mocks.ensureSelf,
    // The sidebar's mark follows the status line and the brief.
    GetDoingNow: vi.fn(async () => ({ kind: "watching" })),
    GetLatestBrief: vi.fn(async () => ({ brief: null, building: false, enabled: true, nextAt: "", nextSlot: "" })),
    GetMobileConnectionStatus: vi.fn(async () => mocks.mobile),
    GetWatchRecordings: vi.fn(async () => mocks.watch),
    IsClosedEyesModeEnabled: vi.fn(async () => false),
    SetClosedEyesModeEnabled: vi.fn(async () => {}),
}));
vi.mock("../../wailsjs/runtime/runtime", () => ({ EventsOn: vi.fn(() => () => {}) }));

function buildProjects(): Project[] {
    return [
        {
            id: STANDALONE_CHAT_PROJECT_ID,
            name: "General Chat",
            path: "",
            activeSessionId: null,
            activeWorkspaceId: "ws-1",
            sortOrder: 0,
            workspaces: [{ id: "ws-1", label: "General Chat", isDefault: true, activeSessionId: null, sessions: [], createdAt: 1, updatedAt: 1 }],
        } as unknown as Project,
    ];
}

function baseProps(): React.ComponentProps<typeof Sidebar> {
    return {
        projects: buildProjects(),
        activeProjectId: STANDALONE_CHAT_PROJECT_ID,
        collapsed: false,
        width: 260,
        onToggleCollapse: vi.fn(),
        onOpenSettings: vi.fn(),
        onOpenToday: vi.fn(),
        onOpenMainChat: vi.fn(),
        onOpenAgents: vi.fn(),
        onOpenActivity: vi.fn(),
        onOpenYou: vi.fn(),
        closedEyes: false,
        onToggleClosedEyes: vi.fn(),
        appMode: "today",
    };
}

function renderSidebar(handlers: Partial<React.ComponentProps<typeof Sidebar>> = {}) {
    const props = { ...baseProps(), ...handlers };
    const view = render(<Sidebar {...props} />);
    return { props, ...view };
}

const places = (locale: typeof en) => [locale.sidebar.today, locale.sidebar.mainChat, locale.sidebar.agents, locale.sidebar.activity, locale.sidebar.you];

describe("Sidebar: five places (computer-map v1.0.0 §2)", () => {
    beforeEach(async () => {
        await i18n.changeLanguage("en");
        localStorage.clear();
        mocks.about = { update: { status: "idle", version: "0.1.302" }, later: false };
        mocks.profile = { name: "Alex", email: "alex@example.com" };
        mocks.mobile = null;
        mocks.watch = [];
        mocks.refreshUserProfile.mockClear();
        mocks.ensureSelf.mockClear();
    });

    it.each(["en", "zh-CN"])("renders the sidebar mark with the accent in %s", async (locale) => {
        await i18n.changeLanguage(locale);
        renderSidebar();
        const lockup = screen.getByText("ISOFORM").closest("svg")!;
        expect(lockup).toHaveClass("text-accent");
        expect(lockup.querySelector("svg")).toHaveClass("text-accent");
    });

    it.each([en, zh])("lists Today, Chat, Agents, Activity and You, in order", async (locale) => {
        await i18n.changeLanguage(locale === en ? "en" : "zh-CN");
        renderSidebar();
        const navigation = screen.getByRole("navigation", { name: locale.sidebar.mainNavigation });
        expect(within(navigation).getAllByRole("button").map((row) => row.textContent)).toEqual(places(locale));
    });

    it.each([false, true])("keeps the same five places on the rail and opens each (collapsed=%s)", (collapsed) => {
        const { props } = renderSidebar({ collapsed });
        const rows = [...document.querySelectorAll<HTMLElement>("[data-sidebar-place]")];
        expect(rows.map((row) => row.getAttribute("data-sidebar-place"))).toEqual(["today", "chat", "agents", "activity", "you"]);
        const callbacks = [props.onOpenToday, props.onOpenMainChat, props.onOpenAgents, props.onOpenActivity, props.onOpenYou];
        rows.forEach((row, index) => {
            fireEvent.click(row);
            expect(callbacks[index]).toHaveBeenCalledOnce();
        });
    });

    it("has none of what goes: Search, Tasks, Goals, Library, Pinned, groups, sessions, New chat", () => {
        renderSidebar();
        for (const text of [en.sidebar.search, en.sidebar.tasks, en.sidebar.pinned, en.sidebar.customGroups, en.sidebar.newChat, "Goals", "Library", en.userMenu.memory, en.userMenu.knowledge, en.userMenu.people]) {
            expect(screen.queryByText(text)).toBeNull();
            expect(screen.queryByRole("button", { name: text })).toBeNull();
        }
        expect(document.querySelector("[data-session-row], [data-topic-row], [data-sidebar-new-chat]")).toBeNull();
        expect(sidebarSource).not.toMatch(/onOpenSearch|onSwitchSession|pinnedTopics|onOpenTasks/);
    });

    // computer-you v1.0.0 rule 6: You shows once; its tabs are on the You page.
    it.each(["memory", "knowledge", "people"] as const)("selects You on %s, with no tab rows in the sidebar", (appMode) => {
        renderSidebar({ appMode });
        const places = [...document.querySelectorAll("[data-sidebar-place][aria-current=page]")];
        expect(places).toHaveLength(1);
        expect(places[0]).toHaveAttribute("data-sidebar-place", "you");
        expect(places[0].className).not.toMatch(/border-l/);
        expect(document.querySelector("[data-sidebar-you-tabs], [data-you-tab]")).toBeNull();
    });

    it("selects Agents on the agents view", () => {
        renderSidebar({ appMode: "agents" });
        expect(document.querySelector("[aria-current=page]")).toHaveAttribute("data-sidebar-place", "agents");
    });

    it("counts only on Today and Agents, never 0, and puts the unread dot on Chat", () => {
        renderSidebar({ waitingCount: 0, agentsWaiting: 0 });
        expect(screen.queryByText("0")).toBeNull();
        expect(document.querySelector("[data-chat-dot]")).toBeNull();
        cleanup();
        renderSidebar({ waitingCount: 3, agentsWaiting: 2, chatUnread: true });
        const place = (key: string) => document.querySelector(`[data-sidebar-place=${key}]`) as HTMLElement;
        expect(within(place("today")).getByLabelText(i18n.t("app:sidebar.toAnswer", { count: 3 }))).toHaveTextContent("3");
        expect(within(place("agents")).getByLabelText(i18n.t("app:sidebar.agentsWaiting", { count: 2 }))).toHaveTextContent("2");
        expect(place("chat").querySelector("[data-chat-dot]")).not.toBeNull();
        expect(place("chat")).toHaveAttribute("data-main-chat-row");
        for (const key of ["chat", "activity", "you"]) expect(place(key).textContent).not.toMatch(/\d/);
    });

    it.each([0, 2, 10])("keeps the folded Today badge compact for %i items", (count) => {
        renderSidebar({ collapsed: true, waitingCount: count });
        const label = i18n.t("app:sidebar.toAnswer", { count });
        if (count === 0) {
            expect(screen.queryByLabelText(label)).toBeNull();
            return;
        }
        expect(screen.getByLabelText(label)).toHaveTextContent(count > 9 ? "9+" : String(count));
        // VoiceOver reads the icon as the sidebar row would (computer-window rule 4).
        expect(document.querySelector("[data-sidebar-place=today]")).toHaveAttribute("aria-label", `${en.sidebar.today}, ${label}`);
    });

    it("places the foot below the places: phone line, Closed Eyes with ⌘⇧E, then Settings", async () => {
        mocks.mobile = { enabled: true, relayState: "connected", devices: [{ deviceId: "d1" }] };
        const { props } = renderSidebar();
        const phone = await screen.findByText(en.sidebar.phone.connected);
        const eyes = document.querySelector<HTMLElement>("[data-closed-eyes-button]")!;
        const settings = document.querySelector<HTMLElement>("[data-sidebar-settings]")!;
        const navigation = screen.getByRole("navigation", { name: en.sidebar.mainNavigation });
        expect(navigation.compareDocumentPosition(phone) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
        expect(phone.compareDocumentPosition(eyes) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
        expect(eyes.compareDocumentPosition(settings) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
        expect(eyes).toHaveTextContent(en.sidebar.closedEyes);
        // One shortcut, the system-wide one (design, pass-questions v1.0.0).
        if (PAUSE_OBSERVING_COMBO) expect(eyes).toHaveTextContent(comboToDisplayString(PAUSE_OBSERVING_COMBO));
        expect(eyes.textContent).not.toMatch(/⌘⇧E|Ctrl Shift E/);
        fireEvent.click(eyes);
        expect(props.onToggleClosedEyes).toHaveBeenCalledOnce();
        fireEvent.click(settings);
        expect(props.onOpenSettings).toHaveBeenCalledOnce();
    });

    it("shows Closed Eyes pressed while eyes are closed", () => {
        renderSidebar({ closedEyes: true });
        const eyes = document.querySelector<HTMLElement>("[data-closed-eyes-button]")!;
        expect(eyes).toHaveAttribute("aria-pressed", "true");
        expect(eyes).toHaveTextContent(en.sidebar.closedEyesOn);
    });

    it("opens Your phone from the phone line", async () => {
        const onOpenPhoneSettings = vi.fn();
        renderSidebar({ onOpenPhoneSettings });
        fireEvent.click(await screen.findByText(en.sidebar.phone.none));
        expect(onOpenPhoneSettings).toHaveBeenCalledOnce();
    });

    it("shows the watch line only once a watch has sent audio", async () => {
        renderSidebar();
        await screen.findByText(en.sidebar.phone.none);
        expect(document.querySelector("[data-watch-line]")).toBeNull();
        cleanup();
        mocks.watch = [{ id: "w1", startedAt: new Date().toISOString() }];
        renderSidebar();
        expect(await screen.findByText(en.sidebar.watch.listening)).toBeInTheDocument();
        // One still pose beside the words (pass-questions v1.0.0 rule 4).
        expect(document.querySelector("[data-watch-line]")).toHaveAttribute("data-watch-pose", "listening");
        expect(document.querySelector("[data-watch-line] svg")).not.toBeNull();
        // The mark always has a label (pass-access v1.0.0 rule 12).
        expect(screen.getByRole("img", { name: en.sidebar.pose.listening })).toBeInTheDocument();
    });

    it("keeps the update card above observing and the account status after Later", async () => {
        mocks.about = { update: { status: "update-downloaded", version: "0.1.302" }, later: false };
        renderSidebar();
        const card = await screen.findByRole("region", { name: "Update ready" });
        const observing = screen.getByRole("button", { name: /^Background observing/ });
        expect(card.compareDocumentPosition(observing) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
        fireEvent.click(screen.getByRole("button", { name: "Later" }));
        await waitFor(() => expect(screen.queryByRole("region", { name: "Update ready" })).not.toBeInTheDocument());
        expect(screen.getByText(en.userMenu.updateReady)).toBeInTheDocument();
    });

    it("uses the board widths", () => {
        const { rerender, props } = renderSidebar();
        expect(document.querySelector("aside")).toHaveStyle({ width: "214px" });
        rerender(<Sidebar {...props} collapsed />);
        expect(document.querySelector("aside")).toHaveStyle({ width: "56px" });
        expect(screen.getByTitle(en.sidebar.show)).toBeInTheDocument();
    });

    it("shows the user's name at the bottom left, or You until there is one", () => {
        const { unmount } = renderSidebar();
        expect(screen.getByText("Alex")).toBeInTheDocument();
        unmount();
        mocks.profile = null;
        renderSidebar();
        expect(within(document.querySelector<HTMLElement>(".group\\/user")!).getByText(en.sidebar.you)).toBeInTheDocument();
    });

    it("changes the name from the bottom-left menu", async () => {
        function AccountProfileHarness() {
            const [profileOpen, setProfileOpen] = useState(false);
            return <>
                <Sidebar {...baseProps()} onOpenProfile={() => setProfileOpen(true)} />
                {profileOpen && <ProfilePage />}
            </>;
        }
        render(<AccountProfileHarness />);
        fireEvent.click(screen.getByText("Alex"));
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
    });

    it("keeps the account menu to the name, Settings, Keyboard shortcuts and Help", () => {
        const { props } = renderSidebar({ onOpenProfile: vi.fn(), onOpenShortcuts: vi.fn(), onOpenHelp: vi.fn() });
        fireEvent.click(screen.getByText("Alex"));
        const menu = screen.getByRole("button", { name: "Alex" }).parentElement!;
        expect(within(menu).getAllByRole("button").map((item) => item.textContent?.replace(/(?:Ctrl ,|⌘,|Ctrl \/|⌘\/)$/, ""))).toEqual(["AAlex", en.userMenu.settings, en.userMenu.shortcuts, en.userMenu.help]);
        fireEvent.click(screen.getByText(en.userMenu.help));
        expect(props.onOpenHelp).toHaveBeenCalledOnce();
    });

    it("keeps both locales in step and has no left-edge accent bar", () => {
        expect(Object.keys(zh.sidebar).sort()).toEqual(Object.keys(en.sidebar).sort());
        expect(Object.keys(zh.sidebar.phone).sort()).toEqual(Object.keys(en.sidebar.phone).sort());
        expect(Object.keys(zh.sidebar.watch).sort()).toEqual(Object.keys(en.sidebar.watch).sort());
        expect(Object.keys(zh.sidebar.pose).sort()).toEqual(Object.keys(en.sidebar.pose).sort());
        for (const source of [sidebarSource, footSource]) {
            expect(source).not.toMatch(/border-l\b|w-0\.5|w-\[2px\]/);
        }
    });
});

describe("Sidebar foot states", () => {
    it("reads the phone line from the pairing and the relay", () => {
        expect(phoneLineState(null)).toBe("none");
        const base = { enabled: true, relayState: "connected", devices: [] } as never;
        expect(phoneLineState(base)).toBe("none");
        expect(phoneLineState({ ...(base as object), devices: [{ deviceId: "d" }] } as never)).toBe("connected");
        expect(phoneLineState({ ...(base as object), relayState: "offline", devices: [{ deviceId: "d" }] } as never)).toBe("offline");
        expect(phoneLineState({ ...(base as object), enabled: false, devices: [{ deviceId: "d" }] } as never)).toBe("none");
    });

    it("calls the watch listening within the hour and last heard after", () => {
        const now = Date.parse("2026-11-15T18:00:00Z");
        expect(watchLineState([], now)).toEqual({ kind: "none" });
        expect(watchLineState([{ startedAt: "2026-11-15T17:30:00Z" }] as never, now)).toEqual({ kind: "listening" });
        const old = watchLineState([{ startedAt: "2026-11-15T12:00:00Z" }, { startedAt: "bad" }] as never, now);
        expect(old.kind).toBe("heard");
    });

    // 2026-11-16: "last heard 5:43" was two days old and read like that morning.
    it("says the day of a last-heard time that is not today", () => {
        const now = new Date(2026, 10, 16, 14, 30);
        expect(watchHeardTime(new Date(2026, 10, 16, 5, 43), "en-US", now)).toBe("5:43 AM");
        const saturday = watchHeardTime(new Date(2026, 10, 14, 17, 43), "en-US", now);
        expect(saturday).toContain("Sat");
        expect(saturday).toContain("5:43");
        const older = watchHeardTime(new Date(2026, 10, 2, 17, 43), "en-US", now);
        expect(older).toContain("Nov 2");
        expect(older).toContain("5:43");
    });
});

// Pass-access v1.0.0 (shell AX1, AX2): the sidebar by keyboard and VoiceOver.
describe("Sidebar accessibility", () => {
    beforeEach(async () => { await i18n.changeLanguage("en"); });

    it.each([false, true])("has no axe violations, open or folded (folded: %s)", async (collapsed) => {
        renderSidebar({ collapsed, waitingCount: 4, chatUnread: true });
        expect(await axeViolations(document.querySelector("aside")!)).toEqual([]);
    });

    it.each([false, true])("reads counts and states as words, not just a badge or a dot (folded: %s)", (collapsed) => {
        renderSidebar({ collapsed, waitingCount: 4, chatUnread: true });
        expect(screen.getByRole("button", { name: "Today, 4 to answer" })).toBeInTheDocument();
        expect(screen.getByRole("button", { name: "Chat, unread" })).toBeInTheDocument();
    });

    it("names every button", () => {
        renderSidebar({ waitingCount: 1, chatUnread: true });
        const unnamed = screen.getAllByRole("button").filter((b) => !(b.getAttribute("aria-label") || b.textContent || "").trim());
        expect(unnamed.map((b) => b.outerHTML.replace(/ class="[^"]*"/g, "").slice(0, 240))).toEqual([]);
    });

    it("gives Today, Chat, Agents, Activity and You the keys 1 to 5, in the sidebar's order", () => {
        const { result } = renderHook(() => useKeybindings());
        expect(PAGE_KEYS).toEqual(["today", "chat", "agents", "activity", "you"]);
        renderSidebar();
        const order = Array.from(document.querySelectorAll("aside [data-sidebar-place]")).map((el) => el.getAttribute("data-sidebar-place"));
        expect(order.slice(0, 5)).toEqual([...PAGE_KEYS]);
        PAGE_KEYS.forEach((page, i) => {
            const combo = result.current.getCombo(`go-${page}`)!;
            expect(combo.key).toBe(String(i + 1));
            expect(combo.meta || combo.ctrl).toBe(true);
            const event = new KeyboardEvent("keydown", { key: String(i + 1), metaKey: combo.meta, ctrlKey: combo.ctrl });
            expect(result.current.findMatchingAction(event)).toBe(`go-${page}`);
            const action = result.current.actions.find((a) => a.id === `go-${page}`)!;
            expect(action.label).not.toMatch(/keybindings\.commands/);
        });
    });
});
