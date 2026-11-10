import { act, cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import i18n from "@/i18n";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { useSidebarResize } from "@/hooks/useSidebarResize";
import { Sidebar } from "./sidebar";

const sidebarCss = readFileSync(resolve(__dirname, "../index.css"), "utf8");

vi.mock("@/lib/platform", () => ({ isMac: true, isWindows: false, isLinux: false }));
vi.mock("@/hooks/useUserProfile", () => ({
    useUserProfile: () => ({ profile: { name: "Bo", email: "bo@example.com" }, loading: false }),
}));
vi.mock("@/lib/wails-shim-app", () => ({
    ListHandoffs: vi.fn(async () => []),
    EnsureSelfProfile: vi.fn(async () => ({})),
    GetPlatformPermissions: vi.fn(async () => ({ items: [] })),
    GetBackgroundListening: vi.fn(async () => true),
    GetDoingNow: vi.fn(async () => ({ kind: "watching" })),
    GetLatestBrief: vi.fn(async () => ({ brief: null, building: false, enabled: true, nextAt: "", nextSlot: "" })),
    GetCaptureCounts: vi.fn(async () => ({ sources: [] })),
    PauseObservationFor: vi.fn(async () => {}),
    ResumeObservation: vi.fn(async () => {}),
}));
vi.mock("../../wailsjs/runtime/runtime", () => ({ EventsOn: vi.fn(() => () => {}) }));

const noop = () => {};
function SidebarFrame() {
    const state = useSidebarResize({ layoutRef: { current: null } });
    return <Sidebar
        projects={[]}
        activeProjectId={null}
        collapsed={state.sidebarCollapsed}
        width={state.sidebarWidth}
        onToggleCollapse={() => state.setSidebarCollapsed((value) => !value)}
        onOpenSearch={noop}
        onOpenSettings={noop}
        onOpenActivity={noop}
        onOpenKnowledge={noop}
        onOpenMemory={noop}
        onOpenPeople={noop}
        onOpenToday={noop}
        onOpenMainChat={noop}
        onOpenTasks={noop}
        onSwitchSession={noop}
        onCloseSession={noop}
        onTogglePin={noop}
        onRenameSession={noop}
        onMarkSessionRead={noop}
        onMarkSessionUnread={noop}
        onForkSession={noop}
        waitingCount={2}
        isStaging
        appMode="today"
    />;
}

function resize(width: number) {
    act(() => {
        Object.defineProperty(window, "innerWidth", { value: width, configurable: true });
        window.dispatchEvent(new Event("resize"));
    });
}

function expectPainted(element: Element) {
    // Visibility includes ancestors: a present SVG is still blank if the
    // shared content wrapper has opacity zero. jsdom has no layout engine.
    expect(element).toBeVisible();
    for (let node: Element | null = element; node; node = node.parentElement) {
        const style = getComputedStyle(node);
        expect(style.width).not.toBe("0px");
        expect(style.height).not.toBe("0px");
        expect(style.pointerEvents).not.toBe("none");
    }
}

async function expectFoldedRail() {
    const t = i18n.getFixedT(i18n.language, "app");
    const rail = document.querySelector("aside")!;
    expect(rail).toHaveAttribute("data-collapsed", "true");
    expect(within(rail).queryByText(t("sidebar.stagingBadge"))).not.toBeInTheDocument();
    expect(rail).toHaveStyle({ width: "56px" });
    for (const key of ["search", "today", "mainChat", "tasks", "activity", "show"] as const) {
        const button = within(rail).getByRole("button", { name: t(`sidebar.${key}`) });
        const icon = button.querySelector("svg")!;
        expectPainted(icon);
        expect(Number(icon.getAttribute("width"))).toBeGreaterThan(0);
        expect(Number(icon.getAttribute("height"))).toBeGreaterThan(0);
    }
    const observing = await screen.findByRole("button", { name: t("observingStatus.label") });
    expectPainted(observing.querySelector("span span")!);
    const account = within(rail).getByRole("button", { name: t("userMenu.account", { name: "Bo" }) });
    expectPainted(within(account).getByText("B"));
    expectPainted(within(rail).getByLabelText(t("sidebar.waiting", { count: 2 })));
}

describe.each(["paper", "onedark"])("Folded sidebar paint on macOS (%s)", (theme) => {
    let style: HTMLStyleElement;
    let initialWidth: number;
    let initialTheme: string | null;

    beforeEach(() => {
        sessionStorage.clear();
        initialWidth = window.innerWidth;
        initialTheme = document.documentElement.getAttribute("data-theme");
        document.documentElement.setAttribute("data-theme", theme);
        resize(1160);
        // Vitest strips imported styles. Apply the production sidebar motion
        // rules, including collapsed selectors, rather than a test CSS copy.
        const start = sidebarCss.indexOf(".sidebar-width-transition-expand {");
        const end = sidebarCss.indexOf("@keyframes page-enter", start);
        expect(start).toBeGreaterThan(-1);
        expect(end).toBeGreaterThan(start);
        style = document.createElement("style");
        style.textContent = sidebarCss.slice(start, end);
        document.head.append(style);
    });
    afterEach(() => {
        cleanup();
        style?.remove();
        sessionStorage.clear();
        resize(initialWidth);
        if (initialTheme === null) document.documentElement.removeAttribute("data-theme");
        else document.documentElement.setAttribute("data-theme", initialTheme);
    });

    it.each(["en", "zh-CN"])("keeps the rail painted after hand-folding and reopens in %s", async (language) => {
        await i18n.changeLanguage(language);
        const t = i18n.getFixedT(language, "app");
        render(<SidebarFrame />);
        expect(document.querySelector("aside")).toHaveStyle({ width: "240px" });
        expect(screen.getByText(t("sidebar.stagingBadge"))).toBeInTheDocument();
        fireEvent.click(screen.getByTitle(t("sidebar.collapse")));
        await expectFoldedRail();
        fireEvent.click(screen.getByRole("button", { name: t("sidebar.show") }));
        expect(document.querySelector("aside")).toHaveAttribute("data-collapsed", "false");
        expect(document.querySelector("aside")).toHaveStyle({ width: "240px" });
        expectPainted(screen.getByText(t("sidebar.search")));
    });

    it.each(["en", "zh-CN"])("paints the same rail after auto-collapse below 1160 in %s", async (language) => {
        await i18n.changeLanguage(language);
        render(<SidebarFrame />);
        resize(1159);
        await expectFoldedRail();
        expect(sessionStorage.getItem("isoform.ui.sidebar.handCollapsed")).toBeNull();
        resize(1160);
        expect(document.querySelector("aside")).toHaveAttribute("data-collapsed", "false");
        expect(document.querySelector("aside")).toHaveStyle({ width: "240px" });
    });

    it("paints the rail when the window starts narrow", async () => {
        await i18n.changeLanguage("en");
        resize(1000);
        render(<SidebarFrame />);
        await expectFoldedRail();
    });
});
