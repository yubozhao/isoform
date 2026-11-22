import { warmLazyPages } from "@/lib/lazy-pages";
import { useMemo } from "react";
import { useTranslation } from "react-i18next";
import { Activity, Bot, User } from "lucide-react";
import { TodayIcon } from "@/assets/icons/TodayIcon";
import { ChatBubbleIcon } from "@/assets/icons/ChatBubbleIcon";
import { LogoWithFontIcon } from "@/assets/icons/LogoWithFontIcon";
import { MarkMotion } from "@/components/mark-motion/MarkMotion";
import { SidebarFoot } from "@/components/sidebar-foot";
import { CollapseIcon } from "@/assets/icons/CollapseIcon";
import { isMac } from "@/lib/platform";
import { prefetchPage } from "@/lib/page-queries";
import {
    STANDALONE_CHAT_PROJECT_ID,
    isMainChatSession,
} from "@/lib/stream-helpers";
import { type Project } from "@/types";
import { SIDEBAR_OPEN_WIDTH, SIDEBAR_COLLAPSED_WIDTH } from "@/hooks/useSidebarResize";
import { Button } from "@/components/ui/button";

/** The main views. The sidebar shows five places (computer-map v1.0.0 §2):
 *  Today, Chat, Agents, Activity and You. You is About you ("memory"), How
 *  you do things ("knowledge") and Who you know ("people") until the one You
 *  page with tabs replaces them; each is a tab of You. */
export type AppMode =
    | "chat"
    | "today"
    | "activity"
    | "agents"
    | "you"
    | "memory"
    | "knowledge"
    | "people";

/** The tabs of You ("agent" is Agent of You, Soon). "you" is the one You
 *  page with tabs; until it lands, each tab opens the view named in
 *  YOU_TAB_MODE. */
export type YouTab = "about" | "howYouDo" | "people" | "agent";
export const YOU_TAB_MODE: Record<YouTab, AppMode> = { about: "memory", howYouDo: "knowledge", people: "people", agent: "memory" };
export function isYouMode(mode: AppMode | undefined): boolean {
    return mode === "you" || mode === "memory" || mode === "knowledge" || mode === "people";
}

interface SidebarProps {
    projects: Project[];
    activeProjectId: string | null;
    collapsed: boolean;
    width: number;
    isResizing?: boolean;
    onToggleCollapse: () => void;
    shortcutFor?: (actionId: string) => string | undefined;
    onOpenSettings: () => void;
    /** Settings › Your phone: the phone line opens it. */
    onOpenPhoneSettings?: () => void;
    /** What Today has to answer: the count on Today, from 1. */
    waitingCount?: number;
    /** Requests between your agents waiting for your yes: the count on Agents. */
    agentsWaiting?: number;
    onOpenToday: () => void;
    onOpenMainChat: () => void;
    onOpenAgents: () => void;
    onOpenActivity: () => void;
    /** Opens You, on a tab when one is given; without one, the tab you were on. */
    onOpenYou: (tab?: YouTab) => void;
    /** Closed Eyes, shared with the app's ⌘⇧E shortcut. */
    closedEyes: boolean;
    onToggleClosedEyes: () => void;
    onLogoClick?: () => void;
    isStaging?: boolean;
    /** Current app mode: drives which place is selected. */
    appMode?: AppMode;
}

export function Sidebar({
    projects,
    activeProjectId,
    collapsed,
    width: _width,
    isResizing = false,
    onToggleCollapse,
    shortcutFor,
    onOpenSettings,
    onOpenPhoneSettings,
    waitingCount = 0,
    agentsWaiting = 0,
    onOpenToday,
    onOpenMainChat,
    onOpenAgents,
    onOpenActivity,
    onOpenYou,
    closedEyes,
    onToggleClosedEyes,
    onLogoClick,
    isStaging,
    appMode,
}: SidebarProps) {
    const { t } = useTranslation("app");

    // Chat is one thread (computer-map §3 item 7): the Chat place is selected
    // whenever the chat view shows the main chat, from the first frame.
    const standaloneProject = useMemo(
        () => projects.find((p) => p.id === STANDALONE_CHAT_PROJECT_ID) ?? null,
        [projects],
    );
    const standaloneWorkspace = standaloneProject
        ? (standaloneProject.workspaces.find((w) => w.id === standaloneProject.activeWorkspaceId) ?? standaloneProject.workspaces[0] ?? null)
        : null;
    const standaloneSessions = standaloneWorkspace?.sessions ?? [];
    const activeStandaloneSessionId = standaloneWorkspace?.activeSessionId ?? null;
    const mainChat = standaloneSessions.find(isMainChatSession) ?? null;
    const otherChatOnScreen =
        activeProjectId !== (standaloneProject?.id ?? STANDALONE_CHAT_PROJECT_ID) ||
        (activeStandaloneSessionId !== null &&
            activeStandaloneSessionId !== mainChat?.localId &&
            standaloneSessions.some((s) => s.localId === activeStandaloneSessionId));
    const isOnMainChat = appMode === "chat" && !otherChatOnScreen;
    const youOpen = isYouMode(appMode);
    // A page change blinks the mark once (mark motion rule 6).
    const markPageKey = [appMode, isOnMainChat].join("|");

    // The rail remains mounted at the board width when collapsed.
    const effectiveWidth = collapsed ? SIDEBAR_COLLAPSED_WIDTH : SIDEBAR_OPEN_WIDTH;

    // Hover or keyboard focus on a navigation row starts that page's primary
    // reads; fresh cache entries skip the wire and nothing is cancelled.
    const warm = (page: string) => ({
        onPointerEnter: () => prefetchPage(page),
        onFocus: () => prefetchPage(page),
    });

    // The five places, in order (computer-map v1.0.0 §2.1). Counts only on
    // Today (to answer) and Agents (waiting for your Yes).
    type Place = {
        key: string;
        label: string;
        Icon: React.ComponentType<{ className?: string }>;
        onClick: () => void;
        active: boolean;
        page: string;
        badge?: number;
        badgeLabel?: string;
        warmLazy?: boolean;
    };
    const places: Place[] = [
        { key: "today", label: t("sidebar.today"), Icon: TodayIcon, onClick: onOpenToday, active: appMode === "today", page: "today", badge: waitingCount, badgeLabel: t("sidebar.toAnswer", { count: waitingCount }) },
        { key: "chat", label: t("sidebar.mainChat"), Icon: ChatBubbleIcon, onClick: onOpenMainChat, active: isOnMainChat, page: "chat" },
        { key: "agents", label: t("sidebar.agents"), Icon: Bot, onClick: onOpenAgents, active: appMode === "agents", page: "agents", badge: agentsWaiting, badgeLabel: t("sidebar.agentsWaiting", { count: agentsWaiting }) },
        { key: "activity", label: t("sidebar.activity"), Icon: Activity, onClick: onOpenActivity, active: appMode === "activity", page: "activity", warmLazy: true },
        { key: "you", label: t("sidebar.you"), Icon: User, onClick: () => onOpenYou(), active: youOpen, page: "about-you" },
    ];

    // One expanded row. The active row has the rounded highlight only (no
    // left-edge bar, project rule 12).
    const navRow = (place: Place, delay: number) => {
        const count = place.badge && place.badge > 0 ? place.badge : 0;
        return (
            <Button key={place.key} variant="quiet" size="sm"
                data-sidebar-place={place.key}
                {...(place.key === "chat" ? { "data-main-chat-row": "" } : {})}
                className={`sidebar-row-in flex items-center justify-start font-normal gap-2 w-full px-2 h-8 text-[13px] rounded-lg transition-none cursor-pointer ${
                    place.active
                        ? "bg-background dark:bg-panel text-accent"
                        : "text-muted hover:bg-surface-hover"
                }`}
                aria-current={place.active ? "page" : undefined}
                aria-label={count > 0 && place.badgeLabel ? t("sidebar.rowWaiting", { label: place.label, state: place.badgeLabel }) : undefined}
                onClick={place.onClick}
                onMouseEnter={place.warmLazy ? warmLazyPages : undefined}
                style={{ animationDelay: `${delay}ms` }}
                {...warm(place.page)}
            >
                <place.Icon className="size-[15px] shrink-0" />
                <span className="flex-1 min-w-0 text-left truncate">{place.label}</span>
                {count > 0 && <span role="img" title={place.badgeLabel} aria-label={place.badgeLabel} className="inline-flex items-center justify-center min-w-[18px] h-[18px] px-1 rounded-full bg-warning-bg text-warning text-[11px] font-semibold">{count}</span>}
            </Button>
        );
    };

    const collapseButton = (
        <Button variant="quiet" size="icon"
            className="sidebar-row-in p-1 rounded-md text-icon-subtle hover:text-ink hover:bg-surface-hover transition-colors duration-[var(--motion-duration-fast)] ease-[var(--motion-ease-out)] cursor-pointer"
            onClick={onToggleCollapse}
            aria-label={t("sidebar.collapse")}
            title={t("sidebar.collapse")}
            style={
                {
                    "--wails-draggable": "no-drag",
                    animationDelay: "0ms",
                } as React.CSSProperties
            }
        >
            <CollapseIcon size={15} variant="stroke" />
        </Button>
    );

    const foot = (
        <SidebarFoot
            collapsed={collapsed}
            onOpenSettings={onOpenSettings}
            onOpenPhone={onOpenPhoneSettings ?? onOpenSettings}
            closedEyes={closedEyes}
            onToggleClosedEyes={onToggleClosedEyes}
        />
    );

    return (
        <>
            <aside
                data-collapsed={collapsed ? "true" : "false"}
                className={`patina-ground shrink-0 relative bg-panel dark:bg-background-accent h-screen overflow-hidden ${
                    "border-r border-border-subtle"
                } ${
                    isResizing
                        ? "transition-none"
                        : collapsed
                          ? "sidebar-width-transition-collapse"
                          : "sidebar-width-transition-expand"
                }`}
                style={{ width: effectiveWidth }}
            >
                <div className={`w-full flex flex-col h-full ${collapsed ? "items-center" : ""}`}>
                    {collapsed ? (
                        <div className="flex flex-col items-center w-full flex-1 min-h-0 px-2.5 pb-3 pt-11 gap-1">
                            <Button variant="quiet" size="icon" className="shrink-0 mb-2" aria-label={t("sidebar.show")} title={shortcutFor?.("toggle-sidebar") ? t("sidebar.showWithKey", { key: shortcutFor("toggle-sidebar") }) : t("sidebar.show")} onClick={onToggleCollapse}>
                                <MarkMotion size={19} needsCount={waitingCount} pageKey={markPageKey} />
                            </Button>
                            {places.map((place, index) => {
                                // Hover names the place and its key (computer-window rule 4).
                                const key = shortcutFor?.(`go-${place.key}`);
                                const name = place.badge ? t("sidebar.railWithCount", { place: place.label, detail: place.badgeLabel }) : place.label;
                                return (
                                <Button key={place.key} variant="quiet" size="icon" className={`relative shrink-0 transition-none ${place.active ? "bg-surface-active" : ""}`} aria-label={name} aria-current={place.active ? "page" : undefined} title={key ? t("sidebar.railHover", { name, key }) : name} onClick={place.onClick} onMouseEnter={place.warmLazy ? warmLazyPages : undefined} {...warm(place.page)} data-rail-index={index} data-sidebar-place={place.key}>
                                    <span className="relative inline-flex size-[15px]">
                                        <place.Icon className="size-[15px]" />
                                        {place.badge ? <span role="img" className="absolute -right-2 -top-2 inline-flex items-center justify-center h-4 min-w-4 rounded-full bg-waiting-badge text-waiting-badge-fg px-0.5 text-[9px] leading-none font-medium" aria-label={place.badgeLabel}>{place.badge > 9 ? t("sidebar.waitingOverflow") : place.badge}</span> : null}
                                    </span>
                                </Button>
                                );
                            })}
                            <div className="flex-1 min-h-0" />
                        </div>
                    ) : <>

                    {/* On macOS, render a short drag strip above the header so
                    the logo clears the traffic-light buttons (which sit around
                    y≈12–26). Kept tight (28px) to avoid excess whitespace above
                    the logo; the main toolbar keeps its own 44px region so its
                    buttons don't hug the window edge. */}
                    {isMac && (
                        <div
                            className="h-[28px] shrink-0"
                            style={
                                {
                                    "--wails-draggable": "drag",
                                } as React.CSSProperties
                            }
                        />
                    )}

                    {/* Keep the mark aligned with the row icon column. */}
                    <div className="h-[48px] flex items-center justify-between pl-5 pr-3">
                        <div
                            className="flex items-center cursor-default select-none"
                            onClick={onLogoClick}
                        >
                            <LogoWithFontIcon
                                height={20}
                                className="text-accent"
                                blinkOnHover
                                mark={<MarkMotion size={19} needsCount={waitingCount} pageKey={markPageKey} hoverBlink />}
                            />
                            {isStaging && (
                                <span className="ml-1.5 inline-flex items-center px-1.5 py-0.5 rounded text-[9px] font-semibold uppercase tracking-wider bg-warning-bg text-warning border border-warning-border">
                                    {t("sidebar.stagingBadge")}
                                </span>
                            )}
                        </div>
                        {collapseButton}
                    </div>

                    <nav aria-label={t("sidebar.mainNavigation")} className="px-3 flex flex-col gap-1 mb-2">
                        {places.map((place, index) => navRow(place, index * 15))}
                    </nav>

                    <div className="flex-1 min-h-0" />

                    </>}
                    {foot}

                </div>

            </aside>
        </>
    );
}
