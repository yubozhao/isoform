import { useAboutInfo } from "@/hooks/use-about-info";
import { UpdateReadyCard } from "@/components/update-ready-card";
import { warmLazyPages } from "@/lib/lazy-pages";
import { useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import {
    Activity,
    BookOpen,
    ChevronsUpDown,
    CircleCheck,
    Keyboard,
    LifeBuoy,
    Settings,
    User,
    Users,
    X,
} from "lucide-react";
import { Plus } from "lucide-react";
import { SearchIcon } from "@/assets/icons/SearchIcon";
import { TodayIcon } from "@/assets/icons/TodayIcon";
import { ChatBubbleIcon } from "@/assets/icons/ChatBubbleIcon";
import { LogoWithFontIcon } from "@/assets/icons/LogoWithFontIcon";
import { MarkMotion } from "@/components/mark-motion/MarkMotion";
import { HandoffIcon } from "@/assets/icons/HandoffIcon";
import { TranscriptIcon } from "@/assets/icons/TranscriptIcon";
import { ObservingStatus } from "@/components/observing-status";
import { CollapseIcon } from "@/assets/icons/CollapseIcon";
import { useUserProfile } from "@/hooks/useUserProfile";
import {
    Popover,
    PopoverTrigger,
    PopoverContent,
} from "@/components/ui/popover";
import { Avatar } from "@/components/ui/avatar";
import { isMac } from "@/lib/platform";
import { prefetchPage, prefetchSessionStream } from "@/lib/page-queries";
import {
    STANDALONE_CHAT_PROJECT_ID,
    isMainChatSession,
} from "@/lib/stream-helpers";
import { type Project, type Session } from "@/types";
import { SIDEBAR_OPEN_WIDTH, SIDEBAR_COLLAPSED_WIDTH } from "@/hooks/useSidebarResize";
import { Button } from "@/components/ui/button";
import { groupSidebarSessions } from "@/lib/sidebar-session-order";
import {
    ContextMenu,
    ContextMenuContent,
    ContextMenuItem,
    ContextMenuSeparator,
    ContextMenuTrigger,
} from "@/components/ui/context-menu";

/** The main views. About you, How you do things and Who you know are main
 *  views too (sidebar handoff 1.0.0, section 2): "memory", "knowledge" and
 *  "people". Library and its pages are gone (section 3). */
export type AppMode =
    | "chat"
    | "today"
    | "activity"
    | "tasks"
    | "memory"
    | "knowledge"
    | "people";

const sectionLabelClass =
    "text-[11px] font-medium uppercase tracking-wide text-muted select-none";

/** A section's small caps label. */
function SidebarSectionHeader({ label }: { label: string }) {
    return <div className={`px-2 pt-2 pb-1 ${sectionLabelClass}`}>{label}</div>;
}

/** Strips the redundant "Transcript:" / "Handoff:" prefix baked into the
 *  session label so the Recent rows read cleanly. */
function cleanLabel(label: string | null | undefined): string {
    if (!label) return "";
    return label.replace(/^(Transcript|Handoff):\s*/i, "").trim();
}

interface SidebarProps {
    projects: Project[];
    activeProjectId: string | null;
    collapsed: boolean;
    width: number;
    isResizing?: boolean;
    onToggleCollapse: () => void;
    /** Opens Search and commands (the same dialog as Command K). */
    onOpenSearch: () => void;
    shortcutFor?: (actionId: string) => string | undefined;
    onOpenSettings: () => void;
    onOpenProfile?: () => void;
    onOpenShortcuts?: () => void;
    onOpenHelp?: () => void;
    onOpenObservationSettings?: () => void;
    updateReady?: boolean;
    /** What Tasks › Needs you lists: the copper count on Tasks, from 1. */
    waitingCount?: number;
    /** An unread reply or question waits in Chat: a copper dot on Chat. */
    chatUnread?: boolean;
    onOpenActivity: () => void;
    onOpenKnowledge: () => void;
    onOpenMemory: () => void;
    onOpenPeople: () => void;
    /** Opens the Today (Morning Brief) companion panel. */
    onOpenToday?: () => void;
    /** Whether the Today panel is currently open (drives the active state). */
    todayOpen?: boolean;
    /** Whether the Activity page is currently open (drives the active state). */
    activityOpen?: boolean;
    /** Opens the Tasks page. */
    onOpenTasks?: () => void;
    tasksOpen?: boolean;
    /** Opens the main chat (the Isoform row), creating it when missing. */
    onOpenMainChat: () => void;
    /** Starts a new chat: the + on the Chat row, shown on hover and focus. */
    onNewChat?: () => void;
    onSwitchSession: (projectId: string, sessionId: string) => void;
    onCloseSession: (projectId: string, sessionId: string) => void;
    onTogglePin: (projectId: string, sessionId: string) => void;
    onRenameSession: (
        projectId: string,
        sessionId: string,
        newLabel: string,
    ) => void;
    onMarkSessionRead: (sessionId: string) => void;
    onMarkSessionUnread: (sessionId: string) => void;
    onForkSession: (projectId: string, sessionId: string) => void;
    onLogoClick?: () => void;
    isStaging?: boolean;
    /** Current app mode — a chat row shows active only in the chats. */
    appMode?: AppMode;
    /** Session local IDs that have unread content (e.g. automation results). */
    unreadSessionIds?: Set<string>;
    /** Topics pinned to the sidebar (RC 1), listed under Pinned; each opens
     *  beside Today. There is no Topics row of its own. */
    pinnedTopics?: Array<{ id: string; name: string }>;
    activeTopicId?: string | null;
    onOpenTopic?: (topicId: string) => void;
    onUnpinTopic?: (topicId: string) => void;
}

export function Sidebar({
    projects,
    activeProjectId,
    collapsed,
    width: _width,
    isResizing = false,
    onToggleCollapse,
    onOpenSearch,
    shortcutFor,
    onOpenSettings,
    onOpenProfile,
    onOpenShortcuts,
    onOpenHelp,
    onOpenObservationSettings,
    updateReady: updateReadyFromProps = false,
    waitingCount = 0,
    chatUnread = false,
    onOpenActivity,
    onOpenKnowledge,
    onOpenMemory,
    onOpenPeople,
    onOpenToday,
    todayOpen,
    activityOpen,
    onOpenTasks,
    tasksOpen,
    onOpenMainChat,
    onNewChat,
    onSwitchSession,
    onCloseSession,
    onTogglePin,
    onRenameSession,
    onMarkSessionRead,
    onMarkSessionUnread,
    onForkSession,
    onLogoClick,
    isStaging,
    appMode,
    unreadSessionIds,
    pinnedTopics = [],
    activeTopicId = null,
    onOpenTopic,
    onUnpinTopic,
}: SidebarProps) {
    const { info: aboutInfo } = useAboutInfo();
    const [updateDismissed, setUpdateDismissed] = useState(false);
    const updateDownloaded = aboutInfo?.update.status === "update-downloaded";
    const updateReady = updateDownloaded || updateReadyFromProps;
    const { t } = useTranslation("app");
    const { profile } = useUserProfile();
    const [userPopoverOpen, setUserPopoverOpen] = useState(false);
    const [renameTarget, setRenameTarget] = useState<{
        projectId: string;
        sessionId: string;
        label: string;
    } | null>(null);
    const [renameValue, setRenameValue] = useState("");
    // Set when Escape cancels the inline rename so the input's unmount blur
    // doesn't commit the discarded value.
    const renameEscapedRef = useRef(false);
    // Set when Rename is picked from the context menu: the menu's close-time
    // focus restoration would steal focus from the freshly-mounted inline
    // input, so we suppress it for that one close.
    const renameFocusGuardRef = useRef(false);

    // The name Isoform calls the user (People "self"), or a plain "You"
    // until they give one.
    const displayName =
        profile?.display_name || profile?.name || t("sidebar.you");

    const standaloneProject = useMemo(
        () => projects.find((p) => p.id === STANDALONE_CHAT_PROJECT_ID) ?? null,
        [projects],
    );
    const standaloneWorkspace = useMemo(() => {
        if (!standaloneProject) return null;
        return (
            standaloneProject.workspaces.find(
                (w) => w.id === standaloneProject.activeWorkspaceId,
            ) ??
            standaloneProject.workspaces[0] ??
            null
        );
    }, [standaloneProject]);
    const standaloneSessions = standaloneWorkspace?.sessions ?? [];
    const activeStandaloneSessionId =
        appMode === "chat"
            ? (standaloneWorkspace?.activeSessionId ?? null)
            : null;

    // The main chat has its own fixed row at the top; everything else in the
    // standalone workspace (older chats, meeting transcripts) lists under
    // Pinned when pinned and is reached from Search otherwise.
    const mainChat = useMemo(
        () => standaloneSessions.find(isMainChatSession) ?? null,
        [standaloneSessions],
    );
    // On the Chat page the Chat row is selected unless another known chat is
    // on screen, so it holds from the first frame, before the main chat loads.
    const otherChatOnScreen =
        activeProjectId !== (standaloneProject?.id ?? STANDALONE_CHAT_PROJECT_ID) ||
        (activeStandaloneSessionId !== null &&
            activeStandaloneSessionId !== mainChat?.localId &&
            standaloneSessions.some((s) => s.localId === activeStandaloneSessionId));
    const isOnMainChat = appMode === "chat" && !otherChatOnScreen;
    // A page change blinks the mark once (mark motion rule 6).
    const markPageKey = [appMode, todayOpen, tasksOpen, activityOpen, activeTopicId, isOnMainChat].join("|");

    // Only pinned chats are listed; every other chat is reached from Search.
    const { pinnedSessions } = useMemo(
        () => groupSidebarSessions(standaloneSessions.filter((s) => !isMainChatSession(s))),
        [standaloneSessions],
    );

    // Index EVERY session in EVERY project/workspace, not just the
    // standalone project's. Transcript chat sessions are created in whatever
    // project activeProjectAndWorkspace returns (often a proactive project,
    // not standalone), so a localId from a meeting event's children may live
    // outside standaloneSessions. Indexing globally lets the Recent meeting
    // parent always resolve its children's labels — without this, those
    // events used to be skipped entirely because `children.length === 0`.
    // The sibling projectId map is required for click routing: passing the
    // standalone projectId for a session that actually lives in proactive
    // makes handleSwitchSession silently no-op.
    const sessionProjectIdById = useMemo(() => {
        const map = new Map<string, string>();
        for (const p of projects) {
            for (const w of p.workspaces) {
                for (const s of w.sessions) {
                    if (!map.has(s.localId)) map.set(s.localId, p.id);
                }
            }
        }
        return map;
    }, [projects]);
    // Resolve which project actually owns a session before routing the click:
    // transcripts created from meeting detection live in proactive projects,
    // not in standalone, so handing standaloneProject.id to handleSwitchSession
    // (the old default) silently no-ops for those rows. Fall back to standalone
    // for sessions we don't yet know about (cold start) so the click reaches
    // an attempt even before sessions hydrate.
    const projectIdForSession = (localId: string): string =>
        sessionProjectIdById.get(localId) ??
        standaloneProject?.id ??
        STANDALONE_CHAT_PROJECT_ID;

    const isSessionActive = (localId: string) =>
        appMode === "chat" &&
        activeProjectId === standaloneProject?.id &&
        localId === activeStandaloneSessionId;

    // The rail remains mounted at the board width when collapsed.
    const effectiveWidth = collapsed ? SIDEBAR_COLLAPSED_WIDTH : SIDEBAR_OPEN_WIDTH;

    // Every list row uses the board's inset 32px pitch and selection token.
    const rowClass = (active: boolean, _dense = false) =>
        `flex items-center gap-2 w-full px-2 h-8 text-[13px] rounded-lg transition-none cursor-pointer ${
            active
                ? "bg-surface-active text-ink"
                : "text-muted hover:bg-surface-hover"
        }`;

    // Hover or keyboard focus on a navigation row starts that page's primary
    // reads; fresh cache entries skip the wire and nothing is cancelled.
    const warm = (page: string) => ({
        onPointerEnter: () => prefetchPage(page),
        onFocus: () => prefetchPage(page),
    });

    const renderSessionRow = (session: Session) => {
        const active = isSessionActive(session.localId);
        const isUnread = unreadSessionIds?.has(session.localId) ?? false;
        const projectId = projectIdForSession(session.localId);
        const isRenaming = renameTarget?.sessionId === session.localId;
        const commitRename = () => {
            if (renameEscapedRef.current) {
                renameEscapedRef.current = false;
                setRenameTarget(null);
                return;
            }
            const trimmed = renameValue.trim();
            if (trimmed && trimmed !== session.label) {
                onRenameSession(projectId, session.localId, trimmed);
            }
            setRenameTarget(null);
        };
        // Leading icon by session type: transcript / handoff by label prefix,
        // otherwise a chat bubble for a regular conversation.
        const LeadIcon = /^Transcript:/i.test(session.label)
            ? TranscriptIcon
            : /^Handoff:/i.test(session.label)
              ? HandoffIcon
              : ChatBubbleIcon;
        return (
            <ContextMenu key={session.localId}>
                <ContextMenuTrigger asChild>
                    <div
                        data-session-row={session.localId}
                        role="button"
                        aria-current={active ? "page" : undefined}
                        tabIndex={0}
                        className={`group/session ${collapsed && !isRenaming ? `relative flex items-center justify-center size-7 shrink-0 rounded-full text-[12px] cursor-pointer ${active ? "bg-surface-active text-ink" : "text-muted hover:bg-surface-hover"}` : rowClass(active, true)} focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent-2/40`}
                        onPointerEnter={() => prefetchSessionStream(session.localId)}
                        onFocus={() => prefetchSessionStream(session.localId)}
                        onClick={() =>
                            onSwitchSession(projectId, session.localId)
                        }
                        onKeyDown={(event) => {
                            // Only the row itself: keys aimed at the rename
                            // input or the close button belong to them.
                            if (event.target !== event.currentTarget) return;
                            if (event.key === "Enter" || event.key === " ") {
                                event.preventDefault();
                                onSwitchSession(projectId, session.localId);
                            }
                        }}
                        title={session.label}
                    >
                        {!collapsed && <LeadIcon className="size-[15px] shrink-0" />}
                        {isRenaming ? (
                            <input
                                // Focus deferred a frame: at mount the context
                                // menu's FocusScope is still alive and would
                                // yank an eager autoFocus straight back, so we
                                // focus + select after the menu has unmounted.
                                ref={(el) => {
                                    // Inline ref runs on every render; the
                                    // activeElement guard makes the focus +
                                    // select-all a mount-only effect instead
                                    // of re-selecting on each keystroke.
                                    if (el && document.activeElement !== el) {
                                        requestAnimationFrame(() => {
                                            el.focus();
                                            el.select();
                                        });
                                    }
                                }}
                                type="text"
                                maxLength={120}
                                value={renameValue}
                                onChange={(event) =>
                                    setRenameValue(event.target.value)
                                }
                                onClick={(event) => event.stopPropagation()}
                                onKeyDown={(event) => {
                                    if (event.key === "Enter") {
                                        event.currentTarget.blur();
                                    } else if (event.key === "Escape") {
                                        renameEscapedRef.current = true;
                                        event.currentTarget.blur();
                                    }
                                }}
                                onBlur={commitRename}
                                className="flex-1 min-w-0 rounded-[4px] bg-background px-1 -mx-1 text-ink outline-none ring-1 ring-border-focus"
                            />
                        ) : (
                            <span className={collapsed ? "text-center" : "flex-1 min-w-0 truncate"}>
                                {collapsed ? Array.from(cleanLabel(session.label))[0]?.toLocaleUpperCase() : cleanLabel(session.label)}
                            </span>
                        )}
                        {/* Fixed 20px trailing slot keeps the row height and
                            width stable: the unread dot sits centered in it,
                            and on hover the close button appears in the same
                            slot (replacing the dot) instead of pushing it. */}
                        {collapsed && !isRenaming && isUnread && (
                            <span aria-label={t("sidebar.unread")} className="absolute right-0 top-0 size-1.5 rounded-full bg-accent-2" />
                        )}
                        {!collapsed && !isRenaming && (
                            <span className="relative flex items-center justify-center w-5 h-5 shrink-0 ml-1 -mr-1.5">
                                {isUnread && (
                                    <span className="size-1.5 rounded-full bg-accent-2/50 group-hover/session:hidden" />
                                )}
                                <button
                                    className="absolute inset-0 hidden group-hover/session:flex items-center justify-center rounded text-muted/80 hover:text-ink hover:bg-surface-active cursor-pointer"
                                    onClick={(event) => {
                                        event.stopPropagation();
                                        onCloseSession(
                                            projectId,
                                            session.localId,
                                        );
                                    }}
                                    title={t("sidebar.closeSession")}
                                >
                                    <X className="h-3.5 w-3.5" />
                                </button>
                            </span>
                        )}
                    </div>
                </ContextMenuTrigger>
                <ContextMenuContent
                    onCloseAutoFocus={(event) => {
                        if (renameFocusGuardRef.current) {
                            renameFocusGuardRef.current = false;
                            event.preventDefault();
                        }
                    }}
                >
                    <ContextMenuItem
                        onSelect={() => onTogglePin(projectId, session.localId)}
                    >
                        {session.pinned
                            ? t("sessionItem.unpin")
                            : t("sessionItem.pin")}
                    </ContextMenuItem>
                    <ContextMenuItem
                        onSelect={() =>
                            isUnread
                                ? onMarkSessionRead(session.localId)
                                : onMarkSessionUnread(session.localId)
                        }
                    >
                        {isUnread
                            ? t("sessionItem.markRead")
                            : t("sessionItem.markUnread")}
                    </ContextMenuItem>
                    <ContextMenuItem
                        onSelect={() => {
                            renameFocusGuardRef.current = true;
                            setRenameTarget({
                                projectId,
                                sessionId: session.localId,
                                label: session.label,
                            });
                            setRenameValue(session.label);
                        }}
                    >
                        {t("sessionItem.rename")}
                    </ContextMenuItem>
                    <ContextMenuItem
                        disabled={session.streaming}
                        onSelect={() => onForkSession(projectId, session.localId)}
                    >
                        {t("sessionItem.branch")}
                    </ContextMenuItem>
                    <ContextMenuSeparator />
                    <ContextMenuItem
                        destructive
                        onSelect={() => onCloseSession(projectId, session.localId)}
                    >
                        {t("sessionItem.delete")}
                    </ContextMenuItem>
                </ContextMenuContent>
            </ContextMenu>
        );
    };

    // The badge counts from 1; a count of 0 is never shown.
    const tasksBadge = waitingCount > 0 ? waitingCount : undefined;

    // The seven rows, in order: the rail and the expanded sidebar share them
    // (sidebar handoff 1.0.0, section 2). The rail adds a divider before You.
    type RailRow = {
        label: string;
        tooltip?: string;
        Icon: React.ComponentType<{ className?: string }>;
        onClick?: () => void;
        active?: boolean;
        page: string;
        badge?: number;
        dot?: boolean;
        warmLazy?: boolean;
    };
    const railRows: Array<RailRow | "divider"> = [
        { label: t("sidebar.search"), Icon: SearchIcon, onClick: onOpenSearch, active: false, page: "search" },
        { label: t("sidebar.today"), Icon: TodayIcon, onClick: onOpenToday, active: todayOpen, page: "today" },
        { label: t("sidebar.mainChat"), Icon: ChatBubbleIcon, onClick: onOpenMainChat, active: isOnMainChat, page: "chat", dot: chatUnread },
        { label: t("sidebar.tasks"), Icon: CircleCheck, onClick: onOpenTasks, active: tasksOpen, page: "tasks", badge: tasksBadge },
        { label: t("sidebar.activity"), Icon: Activity, onClick: onOpenActivity, active: activityOpen, warmLazy: true, page: "activity" },
        "divider",
        { label: t("userMenu.memory"), tooltip: t("userMenu.aboutDescription"), Icon: User, onClick: onOpenMemory, active: appMode === "memory", page: "about-you" },
        { label: t("userMenu.knowledge"), tooltip: t("userMenu.knowHowDescription"), Icon: BookOpen, onClick: onOpenKnowledge, active: appMode === "knowledge", page: "know-how" },
        { label: t("userMenu.people"), tooltip: t("userMenu.peopleDescription"), Icon: Users, onClick: onOpenPeople, active: appMode === "people", page: "people" },
    ];

    // One expanded row. The active row has the rounded highlight only (no
    // left-edge bar, project rule 12).
    const navRow = ({ key, label, tooltip, Icon, active, onClick, page, delay, badge, dot, warmLazy, attrs }: {
        key: string;
        label: string;
        tooltip?: string;
        Icon: React.ComponentType<{ className?: string }>;
        active: boolean;
        onClick: () => void;
        page: string;
        delay?: number;
        badge?: number;
        dot?: boolean;
        warmLazy?: boolean;
        attrs?: Record<string, string>;
    }) => {
        const count = badge && badge > 0 ? badge : 0;
        return (
            <Button key={key} variant="quiet" size="sm"
                {...attrs}
                className={`sidebar-row-in flex items-center justify-start font-normal gap-2 w-full px-2 h-8 text-[13px] rounded-lg transition-none cursor-pointer ${
                    active
                        ? "bg-background dark:bg-panel text-accent"
                        : "text-muted hover:bg-surface-hover"
                }`}
                aria-current={active ? "page" : undefined}
                title={tooltip}
                onClick={onClick}
                onMouseEnter={warmLazy ? warmLazyPages : undefined}
                style={delay !== undefined ? { animationDelay: `${delay}ms` } : undefined}
                {...warm(page)}
            >
                <Icon className="size-[15px] shrink-0" />
                <span className="flex-1 min-w-0 text-left truncate">{label}</span>
                {count > 0 && <span title={t("sidebar.waiting", { count })} aria-label={t("sidebar.waiting", { count })} className="inline-flex items-center justify-center min-w-[18px] h-[18px] px-1 rounded-full bg-warning-bg text-warning text-[11px] font-semibold">{count}</span>}
                {dot && <span aria-label={t("sidebar.unread")} data-chat-dot className="size-1.5 shrink-0 rounded-full bg-copper mr-1" />}
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
                            <Button variant="quiet" size="icon" className="shrink-0 mb-2" aria-label={t("sidebar.show")} title={t("sidebar.show")} onClick={onToggleCollapse}>
                                <MarkMotion size={19} needsCount={waitingCount} pageKey={markPageKey} />
                            </Button>
                            {railRows.map((row, index) => row === "divider" ? (
                                <div key="divider" data-sidebar-divider className="my-1 h-px w-5 shrink-0 bg-border-subtle" />
                            ) : (
                                <Button key={row.label} variant="quiet" size="icon" className={`relative shrink-0 transition-none ${row.active ? "bg-surface-active" : ""}`} aria-label={row.label} aria-current={row.active ? "page" : undefined} title={row.badge ? t("sidebar.waiting", { count: row.badge }) : (row.tooltip ?? row.label)} onClick={row.onClick} onMouseEnter={row.warmLazy ? warmLazyPages : undefined} {...warm(row.page)} data-rail-index={index}>
                                    <span className="relative inline-flex size-[15px]">
                                        <row.Icon className="size-[15px]" />
                                        {row.badge ? <span className="absolute -right-2 -top-2 inline-flex items-center justify-center h-4 min-w-4 rounded-full bg-waiting-badge text-waiting-badge-fg px-0.5 text-[9px] leading-none font-medium" aria-label={t("sidebar.waiting", { count: row.badge })}>{row.badge > 9 ? t("sidebar.waitingOverflow") : row.badge}</span> : null}
                                        {row.dot ? <span aria-label={t("sidebar.unread")} className="absolute -right-1 -top-1 size-1.5 rounded-full bg-copper" /> : null}
                                    </span>
                                </Button>
                            ))}
                            <div className="flex-1 min-h-0 overflow-y-auto w-full flex flex-col items-center gap-1 pt-3">
                                {onOpenTopic && pinnedTopics.map((topic) => (
                                    <ContextMenu key={topic.id}>
                                        <ContextMenuTrigger asChild>
                                            <Button variant="quiet" size="icon" className={`size-7 rounded-full text-[12px] shrink-0 ${activeTopicId === topic.id ? "bg-surface-active text-ink" : ""}`} aria-current={activeTopicId === topic.id ? "page" : undefined} title={topic.name} aria-label={topic.name} onClick={() => onOpenTopic(topic.id)} {...warm("today")}>{Array.from(topic.name)[0]?.toLocaleUpperCase()}</Button>
                                        </ContextMenuTrigger>
                                        {onUnpinTopic && <ContextMenuContent><ContextMenuItem onSelect={() => onUnpinTopic(topic.id)}>{t("sessionItem.unpin")}</ContextMenuItem></ContextMenuContent>}
                                    </ContextMenu>
                                ))}
                                {pinnedSessions.map(renderSessionRow)}
                            </div>
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

                    {/* Top Actions */}
                    {/* The pinned list scrolls beneath this fixed group. */}
                    <nav aria-label={t("sidebar.mainNavigation")} className="px-3 flex flex-col gap-1 mb-2">
                        <Button variant="quiet" size="sm"
                            className="sidebar-row-in flex items-center justify-start font-normal gap-2 w-full px-2 h-8 text-[13px] text-muted rounded-lg hover:bg-surface-hover transition-colors duration-[var(--motion-duration-fast)] ease-[var(--motion-ease-out)] cursor-pointer"
                            onClick={onOpenSearch}
                            style={{ animationDelay: "0ms" }}
                            {...warm("search")}
                        >
                            <SearchIcon className="size-[15px] shrink-0 relative top-[0.5px]" />
                            <span>{t("sidebar.search")}</span>
                            {shortcutFor?.("command-palette") && <span className="ml-auto text-[11.5px] text-muted">{shortcutFor("command-palette")}</span>}
                        </Button>
                        {navRow({ key: "today", label: t("sidebar.today"), Icon: TodayIcon, active: !!todayOpen, onClick: () => onOpenToday?.(), page: "today", delay: 15 })}
                        <div className="group/chat relative">
                            {navRow({ key: "chat", label: t("sidebar.mainChat"), Icon: ChatBubbleIcon, active: isOnMainChat, onClick: () => onOpenMainChat(), page: "chat", delay: 30, dot: chatUnread, attrs: { "data-main-chat-row": "" } })}
                            {onNewChat && (
                                <Button variant="quiet" size="icon"
                                    data-sidebar-new-chat
                                    aria-label={t("sidebar.newChat")}
                                    title={t("sidebar.newChat")}
                                    onClick={onNewChat}
                                    className="absolute right-1 top-1/2 size-6 -translate-y-1/2 rounded-md text-icon-subtle opacity-0 transition-opacity hover:bg-surface-hover hover:text-ink focus-visible:opacity-100 group-hover/chat:opacity-100 group-focus-within/chat:opacity-100"
                                >
                                    <Plus className="size-3.5" />
                                </Button>
                            )}
                        </div>
                        {navRow({ key: "tasks", label: t("sidebar.tasks"), Icon: CircleCheck, active: !!tasksOpen, onClick: () => onOpenTasks?.(), page: "tasks", delay: 45, badge: waitingCount, attrs: { "data-tasks-row": "" } })}
                        {navRow({ key: "activity", label: t("sidebar.activity"), Icon: Activity, active: !!activityOpen, onClick: () => onOpenActivity(), page: "activity", delay: 60, warmLazy: true, attrs: { "data-activity-row": "" } })}
                    </nav>

                    {/* You: what Isoform knows about you. The rows are always
                        there; their pages fill in (sidebar handoff, section 2). */}
                    <nav aria-label={t("sidebar.you")} data-sidebar-section="you" className="sidebar-row-in px-3 flex flex-col gap-1 mb-2" style={{ animationDelay: "75ms" }}>
                        <SidebarSectionHeader label={t("sidebar.you")} />
                        {navRow({ key: "memory", label: t("userMenu.memory"), tooltip: t("userMenu.aboutDescription"), Icon: User, active: appMode === "memory", onClick: onOpenMemory, page: "about-you", attrs: { "data-about-you-row": "" } })}
                        {navRow({ key: "knowledge", label: t("userMenu.knowledge"), tooltip: t("userMenu.knowHowDescription"), Icon: BookOpen, active: appMode === "knowledge", onClick: onOpenKnowledge, page: "know-how", attrs: { "data-know-how-row": "" } })}
                        {navRow({ key: "people", label: t("userMenu.people"), tooltip: t("userMenu.peopleDescription"), Icon: Users, active: appMode === "people", onClick: onOpenPeople, page: "people", attrs: { "data-people-row": "" } })}
                    </nav>

                    {/* Scrollable content: Pinned chats, only once something
                        is pinned; every other chat is reached from Search. */}
                    <div className="flex-1 min-h-0 overflow-y-auto sidebar-scroll relative px-3">
                        {pinnedSessions.length > 0 || (onOpenTopic && pinnedTopics.length > 0) ? (
                            <div
                                data-sidebar-section="pinned"
                                className="sidebar-row-in flex flex-col gap-[5px]"
                                style={{ animationDelay: "90ms" }}
                            >
                                <SidebarSectionHeader
                                    label={t("sidebar.pinned")}
                                />
                                <div className="flex flex-col gap-1">
                                    {onOpenTopic && pinnedTopics.map((topic) => (
                                        <ContextMenu key={`topic:${topic.id}`}>
                                            <ContextMenuTrigger asChild>
                                                <button
                                                    type="button"
                                                    data-topic-row={topic.id}
                                                    aria-current={activeTopicId === topic.id ? "page" : undefined}
                                                    className={`${rowClass(activeTopicId === topic.id, true)} h-7 pl-[31px] text-left focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent-2/40`}
                                                    onClick={() => onOpenTopic(topic.id)}
                                                    {...warm("today")}
                                                    title={topic.name}
                                                >
                                                    <span className="flex-1 min-w-0 truncate">{topic.name}</span>
                                                </button>
                                            </ContextMenuTrigger>
                                            {onUnpinTopic && (
                                                <ContextMenuContent>
                                                    <ContextMenuItem onSelect={() => onUnpinTopic(topic.id)}>
                                                        {t("sessionItem.unpin")}
                                                    </ContextMenuItem>
                                                </ContextMenuContent>
                                            )}
                                        </ContextMenu>
                                    ))}
                                    {pinnedSessions.map(renderSessionRow)}
                                </div>
                            </div>
                        ) : null}

                    </div>

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
                                    onOpenActivity={() => {
                                        setUserPopoverOpen(false);
                                        onOpenActivity();
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
