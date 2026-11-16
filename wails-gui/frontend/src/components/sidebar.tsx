import { useAboutInfo } from "@/hooks/use-about-info";
import { UpdateReadyCard } from "@/components/update-ready-card";
import { warmLazyPages } from "@/lib/lazy-pages";
import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import {
    Activity,
    Bot,
    ChevronsUpDown,
    Keyboard,
    LifeBuoy,
    Settings,
    User,
} from "lucide-react";
import { TodayIcon } from "@/assets/icons/TodayIcon";
import { ChatBubbleIcon } from "@/assets/icons/ChatBubbleIcon";
import { LogoWithFontIcon } from "@/assets/icons/LogoWithFontIcon";
import { MarkMotion } from "@/components/mark-motion/MarkMotion";
import { ObservingStatus } from "@/components/observing-status";
import { SidebarFoot } from "@/components/sidebar-foot";
import { CollapseIcon } from "@/assets/icons/CollapseIcon";
import { useUserProfile } from "@/hooks/useUserProfile";
import {
    Popover,
    PopoverTrigger,
    PopoverContent,
} from "@/components/ui/popover";
import { Avatar } from "@/components/ui/avatar";
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
    onOpenProfile?: () => void;
    onOpenShortcuts?: () => void;
    onOpenHelp?: () => void;
    onOpenObservationSettings?: () => void;
    /** Settings › Your phone: the phone line opens it. */
    onOpenPhoneSettings?: () => void;
    updateReady?: boolean;
    /** What Today has to answer: the count on Today, from 1. */
    waitingCount?: number;
    /** Requests between your agents waiting for your Yes: the count on Agents. */
    agentsWaiting?: number;
    /** An unread reply or question waits in Chat: a copper dot on Chat. */
    chatUnread?: boolean;
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
    onOpenProfile,
    onOpenShortcuts,
    onOpenHelp,
    onOpenObservationSettings,
    onOpenPhoneSettings,
    updateReady: updateReadyFromProps = false,
    waitingCount = 0,
    agentsWaiting = 0,
    chatUnread = false,
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
    const { info: aboutInfo } = useAboutInfo();
    const [updateDismissed, setUpdateDismissed] = useState(false);
    const updateDownloaded = aboutInfo?.update.status === "update-downloaded";
    const updateReady = updateDownloaded || updateReadyFromProps;
    const { t } = useTranslation("app");
    const { profile } = useUserProfile();
    const [userPopoverOpen, setUserPopoverOpen] = useState(false);

    // The name Isoform calls the user (People "self"), or a plain "You"
    // until they give one.
    const displayName =
        profile?.display_name || profile?.name || t("sidebar.you");

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
        dot?: boolean;
        warmLazy?: boolean;
    };
    const places: Place[] = [
        { key: "today", label: t("sidebar.today"), Icon: TodayIcon, onClick: onOpenToday, active: appMode === "today", page: "today", badge: waitingCount, badgeLabel: t("sidebar.toAnswer", { count: waitingCount }) },
        { key: "chat", label: t("sidebar.mainChat"), Icon: ChatBubbleIcon, onClick: onOpenMainChat, active: isOnMainChat, page: "chat", dot: chatUnread },
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
                onClick={place.onClick}
                onMouseEnter={place.warmLazy ? warmLazyPages : undefined}
                style={{ animationDelay: `${delay}ms` }}
                {...warm(place.page)}
            >
                <place.Icon className="size-[15px] shrink-0" />
                <span className="flex-1 min-w-0 text-left truncate">{place.label}</span>
                {count > 0 && <span title={place.badgeLabel} aria-label={place.badgeLabel} className="inline-flex items-center justify-center min-w-[18px] h-[18px] px-1 rounded-full bg-warning-bg text-warning text-[11px] font-semibold">{count}</span>}
                {place.dot && <span aria-label={t("sidebar.unread")} data-chat-dot className="size-1.5 shrink-0 rounded-full bg-copper mr-1" />}
            </Button>
        );
    };

    const collapseButton = (
        <Button variant="quiet" size="icon"
            className="sidebar-row-in p-1 rounded-md text-icon-subtle hover:text-ink hover:bg-surface-hover transition-colors duration-[var(--motion-duration-fast)] ease-[var(--motion-ease-out)] cursor-pointer"
            onClick={onToggleCollapse}
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
                            {places.map((place, index) => (
                                <Button key={place.key} variant="quiet" size="icon" className={`relative shrink-0 transition-none ${place.active ? "bg-surface-active" : ""}`} aria-label={place.badge ? t("sidebar.railWithCount", { place: place.label, detail: place.badgeLabel }) : place.label} aria-current={place.active ? "page" : undefined} title={place.badge ? place.badgeLabel : place.label} onClick={place.onClick} onMouseEnter={place.warmLazy ? warmLazyPages : undefined} {...warm(place.page)} data-rail-index={index} data-sidebar-place={place.key}>
                                    <span className="relative inline-flex size-[15px]">
                                        <place.Icon className="size-[15px]" />
                                        {place.badge ? <span className="absolute -right-2 -top-2 inline-flex items-center justify-center h-4 min-w-4 rounded-full bg-waiting-badge text-waiting-badge-fg px-0.5 text-[9px] leading-none font-medium" aria-label={place.badgeLabel}>{place.badge > 9 ? t("sidebar.waitingOverflow") : place.badge}</span> : null}
                                        {place.dot ? <span aria-label={t("sidebar.unread")} className="absolute -right-1 -top-1 size-1.5 rounded-full bg-copper" /> : null}
                                    </span>
                                </Button>
                            ))}
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
                    <div hidden={collapsed}>
                        <UpdateReadyCard
                            visible={updateDownloaded && !aboutInfo?.later && !updateDismissed}
                            version={aboutInfo?.update.version ?? ""}
                            restartNeeded={aboutInfo?.update.restart_needed ?? false}
                            onLater={() => setUpdateDismissed(true)}
                        />
                    </div>
                    <ObservingStatus collapsed={collapsed} onOpenSettings={onOpenObservationSettings} />
                    {foot}

                    {/* Bottom user area */}
                    <div className="shrink-0 h-[52px] py-2 flex items-center px-3 w-full">
                        <Popover
                            modal={false}
                            open={userPopoverOpen}
                            onOpenChange={setUserPopoverOpen}
                        >
                            <PopoverTrigger asChild>
                                <Button variant="quiet" size={collapsed ? "icon" : "sm"} title={displayName} aria-label={t("userMenu.account", { name: displayName })} onMouseEnter={warmLazyPages} className={`group/user flex items-center rounded-lg hover:bg-surface-hover transition-colors cursor-pointer ${collapsed ? "size-8 p-0 justify-center" : "h-10 w-full gap-2.5 px-2 py-1.5"}`}>
                                    <Avatar
                                        src={null}
                                        fallback={displayName}
                                        className={`${collapsed ? "size-7" : "h-[26px] w-[26px]"} text-[11px] shrink-0`}
                                    />
                                    {!collapsed && <><span className="flex-1 min-w-0 text-left">
                                        <span className="text-[13px] font-medium text-ink transition-colors truncate block">
                                            {displayName}
                                        </span>
                                        {updateReady && <span className="block text-[11.5px] text-muted">{t("userMenu.updateReady")}</span>}
                                    </span><ChevronsUpDown className="size-3.5 text-muted shrink-0" /></>}
                                </Button>
                            </PopoverTrigger>
                            <PopoverContent
                                side="top"
                                align="start"
                                sideOffset={8}
                                avoidCollisions={false}
                                collisionPadding={12}
                                className="w-[320px] max-h-[var(--radix-popover-content-available-height)] overflow-y-auto rounded-xl border-border-strong p-1.5 shadow-card"
                                onOpenAutoFocus={(e) => e.preventDefault()}
                            >
                                <UserPopoverMenu
                                    displayName={displayName}
                                    shortcutFor={shortcutFor}
                                    onOpenSettings={() => {
                                        setUserPopoverOpen(false);
                                        onOpenSettings();
                                    }}
                                    onOpenProfile={() => { setUserPopoverOpen(false); onOpenProfile?.(); }}
                                    onOpenShortcuts={() => { setUserPopoverOpen(false); onOpenShortcuts?.(); }}
                                    onOpenHelp={() => { setUserPopoverOpen(false); onOpenHelp?.(); }}
                                />
                            </PopoverContent>
                        </Popover>
                    </div>
                </div>

            </aside>
        </>
    );
}

function PopoverMenuItem({ icon: Icon, label, description, shortcut, onClick, warmPage }: {
    icon: React.ComponentType<{ className?: string }>;
    label: string;
    description?: string;
    shortcut?: string;
    onClick?: () => void;
    /** Hover/focus starts this page's primary reads (see lib/page-queries). */
    warmPage?: string;
}) {
    return <Button variant="quiet" size="sm" className="w-full h-auto justify-start gap-2.5 px-2.5 py-[7px] text-[13px] font-normal" onClick={onClick}
        onPointerEnter={warmPage ? () => prefetchPage(warmPage) : undefined}
        onFocus={warmPage ? () => prefetchPage(warmPage) : undefined}>
        <Icon className="size-4 shrink-0 text-muted" />
        <span className="min-w-0 text-left"><span className="block">{label}</span>{description && <span title={description} className="block truncate text-[11.5px] text-muted font-normal leading-tight">{description}</span>}</span>
        {shortcut && <span className="ml-auto text-[11.5px] text-muted">{shortcut}</span>}
    </Button>;
}

/** The account menu: your name, Settings, Keyboard shortcuts, and Help and
 *  feedback. About you, How you do things and Who you know are sidebar rows
 *  now, and Library is gone (sidebar handoff 1.0.0, section 2). */
export function UserPopoverMenu({ displayName, shortcutFor, onOpenSettings, onOpenProfile, onOpenShortcuts, onOpenHelp }: {
    displayName: string;
    shortcutFor?: (actionId: string) => string | undefined;
    onOpenSettings: () => void;
    onOpenActivity?: () => void;
    onOpenProfile?: () => void;
    onOpenShortcuts?: () => void;
    onOpenHelp?: () => void;
}) {
    const { t } = useTranslation("app");
    return <>
        <Button variant="quiet" size="sm" className="w-full h-auto justify-start gap-2.5 px-2.5 py-2 text-[13px] font-medium" aria-label={displayName} onClick={onOpenProfile}
            onPointerEnter={() => prefetchPage('settings')} onFocus={() => prefetchPage('settings')}>
            <Avatar fallback={displayName} size="md" /><span className="truncate">{displayName}</span>
        </Button>
        <div className="mx-1.5 my-1 h-px bg-border-subtle" />
        <PopoverMenuItem icon={Settings} label={t("userMenu.settings")} shortcut={shortcutFor?.("open-settings")} onClick={onOpenSettings} warmPage="settings" />
        <PopoverMenuItem icon={Keyboard} label={t("userMenu.shortcuts")} shortcut={shortcutFor?.("keyboard-shortcuts")} onClick={onOpenShortcuts} warmPage="settings" />
        <PopoverMenuItem icon={LifeBuoy} label={t("userMenu.help")} onClick={onOpenHelp} warmPage="settings" />
    </>;
}
