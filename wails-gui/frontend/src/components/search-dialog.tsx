import {
    useCallback,
    useEffect,
    useMemo,
    useRef,
    useState,
    type ComponentType,
    type MutableRefObject,
} from "react";
import { useTranslation } from "react-i18next";
import type { TFunction } from "i18next";
import { waitInWords } from "@/lib/brief-wait";
import { toast } from "sonner";
import {
    Activity,
    BookOpen,
    Brain,
    CircleUser,
    FolderKanban,
    MessageSquare,
    ListChecks,
    PanelLeft,
    Pause,
    Play,
    Settings,
    Sunrise,
    Target,
    Waypoints,
} from "lucide-react";
import {
    CommandDialog,
    CommandGroup,
    CommandInput,
    CommandItem,
    CommandList,
} from "@/components/ui/command";
import { AutomationIcon } from "@/assets/icons/AutomationIcon";
import { HandoffIcon } from "@/assets/icons/HandoffIcon";
import { TopicIcon } from "@/assets/icons/TopicIcon";
import {
    searchSettingsPages,
    settingsPageNameMatches,
    splitSettingsKeywords,
    type ChatAppId,
    type SettingsOpenOptions,
    type SettingsPageEntry,
    type SettingsTopic,
} from "@/components/settings/settings-registry";
import type { TodayTopic } from "@/components/today/topics";
import { useObservationPaused } from "@/components/today/useObservationPaused";
import type { CronJobState } from "@/hooks/useCronJobs";
import { reconcileTodayTopics, toTodayTopics } from "@/hooks/useTodayTopics";
import { shortDate } from "@/lib/know-how";
import { calendarDaysAgo } from "@/lib/memory-content";
import { markPageContent } from "@/lib/page-switch-timing";
import { STANDALONE_CHAT_PROJECT_ID, isMainChatSession } from "@/lib/stream-helpers";
import {
    GenerateMobileBriefNow,
    SearchEverything,
    SetClosedEyesModeEnabled,
    type HandoffEntry,
    type SearchPlace,
    type SearchResult,
    type SearchResults,
    type TodayTopicsView,
} from "@/lib/wails-shim-app";
import { pageQueryCache } from "@/lib/query-cache";
import { GetTodayTopics, GetWorkList, ListHandoffs, usePagePrivacyWithdrawal, usePageQueryRefresh } from "@/lib/page-queries";
import type { WorkList } from "@/lib/wails-shim-app";
import { EventsOn } from "../../wailsjs/runtime/runtime";
import type { Project, Session } from "@/types";

/** The pages Search opens where Library's parts went (sidebar handoff):
 *  Tasks, its Scheduled and All done views, and How you do things. */
export type LibraryPageId = "automations" | "tasks" | "sops" | "deliverables";

/** Where each result opens. App owns the pages, so it supplies these. */
export interface SearchDialogActions {
    goToMainChat: () => void;
    openSession: (projectId: string, sessionId: string) => void;
    /** Opens the chat and scrolls to this message. */
    openChatMessage: (sessionId: string, itemId: string) => void;
    openProject: (projectId: string) => void;
    openAutomation: (jobId: string) => void;
    openHandoff: (id: string) => void;
    openLibraryPage: (page: LibraryPageId) => void;
    openSettings: (topic?: SettingsTopic, options?: SettingsOpenOptions) => void;
    toggleSidebar: () => void;
    openToday: () => void;
    /** Opens Activity; with a time, on that day. */
    openActivity: (at?: number) => void;
    openKnowHow: (id?: string) => void;
    openAboutYou: (id?: string) => void;
    /** Opens People with this person selected. */
    openPerson: (id: string) => void;
    /** Opens Goals on this goal's story. */
    openGoal: (id: string) => void;
    /** Opens the topic beside Today, as a pinned topic does. */
    openTopic: (topic: TodayTopic) => void;
    /** Opens Today on this Your work item. */
    openWorkItem?: (id: string) => void;
}

interface SearchDialogProps {
    open: boolean;
    onOpenChange: (open: boolean) => void;
    projects: Project[];
    automations?: CronJobState[];
    /** The shortcut a keybinding id is bound to, ready to show. */
    shortcutFor?: (actionId: string) => string | undefined;
    actions: SearchDialogActions;
}

// The artboard's order (RC2-Search): each place, Settings last; actions first
// only when the query names one (queryOrder). The places both old dialogs
// had and the artboard does not name (Upcoming, One-time work, Projects,
// Library pages) sit after the artboard's places.
type GroupId =
    | "ready"
    | "recent"
    | "actions"
    | "topics"
    | "chats"
    | "activity"
    | "people"
    | "goals"
    | "knowHow"
    | "aboutYou"
    | "upcoming"
    | "handoffs"
    | "projects"
    | "pages"
    | "settings";

const PLACE_ORDER: readonly GroupId[] = [
    "topics",
    "chats",
    "activity",
    "people",
    "goals",
    "knowHow",
    "aboutYou",
    "upcoming",
    "handoffs",
    "projects",
    "pages",
];

/** Actions lead only when the query names one (an action's label starts
 *  with it, "pause", "make my"); otherwise what was found comes first and
 *  the actions sit just before Settings, so a word that merely appears in an
 *  action ("brief", "observ") never puts it on top. */
function queryOrder(actionsNamed: boolean): readonly GroupId[] {
    // Ready work leads (computer-find v1.0.0 rule 4): prep, drafts, results.
    return actionsNamed ? ["ready", "actions", ...PLACE_ORDER, "settings"] : ["ready", ...PLACE_ORDER, "actions", "settings"];
}

const PLACE_GROUP: Record<SearchPlace, GroupId> = {
    topic: "topics",
    chat: "chats",
    activity: "activity",
    person: "people",
    goal: "goals",
    know_how: "knowHow",
    about_you: "aboutYou",
};

const ALL_PLACES: readonly SearchPlace[] = ["topic", "chat", "activity", "person", "goal", "know_how", "about_you"];
/** A place the quiet failure line can name: what SearchEverything reads,
 *  and the one-time work list the dialog reads itself. */
type FailedPlace = SearchPlace | "handoffs";

const RESULTS_PER_PLACE = 8;
// Keys that move the selection: after one, results arriving later keep the
// row the person moved to instead of jumping back to the first.
const MOVE_KEYS = new Set(["ArrowDown", "ArrowUp", "Home", "End"]);
const SEARCH_DEBOUNCE_MS = 120;
const RECENT_CHATS = 5;
const RECENT_TOPICS = 3;

type Icon = ComponentType<{ className?: string }>;

interface Row {
    /** The cmdk value: unique in the list. */
    key: string;
    title: string;
    meta?: string;
    shortcut?: string;
    Icon: Icon;
    run: () => void;
    /** Running it changes something (pauses observing, makes a brief): it is
     *  never selected for the person, so Enter alone cannot run it. */
    changesState?: boolean;
}

interface Group {
    id: GroupId;
    rows: Row[];
}

interface ChatEntry {
    projectId: string;
    projectName: string;
    session: Session;
}

// The chat apps with their own row on the Chat apps page (Lark shares
// Feishu's row and is named in its label).
const CHAT_APP_ROW_IDS: readonly ChatAppId[] = ["slack", "discord", "telegram", "wechat", "feishu"];

/** The one chat app whose name starts with the query ("slack", "tele",
 *  "微信", "lark"), so the dialog can open its setup as the old per-app
 *  rows did. Short or ambiguous queries name none. */
function chatAppNamed(query: string, ts: TFunction<"settings">): ChatAppId | undefined {
    const needle = query.trim().toLowerCase();
    if (needle.length < 2) return undefined;
    const matches = CHAT_APP_ROW_IDS.filter((id) =>
        (ts(`chatApps.apps.${id}.name` as never) as string)
            .toLowerCase()
            .split("/")
            .some((part) => part.trim().startsWith(needle)),
    );
    return matches.length === 1 ? matches[0] : undefined;
}

/** "today", "yesterday" or a short date: never a clock time. */
function whenInWords(ms: number, t: TFunction<"app">, language: string): string | undefined {
    if (!Number.isFinite(ms) || ms <= 0) return undefined;
    const days = calendarDaysAgo(ms);
    if (days === 0) return t("searchDialog.when.today");
    if (days === 1) return t("searchDialog.when.yesterday");
    return shortDate(ms, language);
}

function joinMeta(...parts: (string | undefined)[]): string | undefined {
    const kept = parts.filter((part): part is string => Boolean(part && part.trim()));
    return kept.length ? kept.join(" · ") : undefined;
}

function includes(text: string | undefined, needle: string): boolean {
    return Boolean(text && text.toLowerCase().includes(needle));
}

function listOf(items: string[], language: string): string {
    try {
        return new Intl.ListFormat(language, { style: "long", type: "conjunction" }).format(items);
    } catch {
        return items.join(", ");
    }
}


/**
 * Search and commands, one dialog (RC 2 D3): Command K and the sidebar's
 * Search open it. It finds the words across topics, chats, Activity, people,
 * goals, know-how and About you (SearchEverything, which applies each
 * place's read gates), plus every source the old Search and command palette
 * had, and runs the actions. An empty box shows recent chats and topics, the
 * actions and the Library pages.
 */
export function SearchDialog({ open, onOpenChange, projects, automations, shortcutFor, actions }: SearchDialogProps) {
    const { t } = useTranslation("app");
    // The body steps back first (clears the query); with nothing to clear,
    // Escape closes the dialog.
    const escapeRef = useRef<(() => boolean) | null>(null);
    const [selected, setSelected] = useState("");
    // Whether the person moved the selection (keys or pointer) since the
    // query last changed.
    const movedRef = useRef(false);
    return (
        <CommandDialog
            open={open}
            onOpenChange={onOpenChange}
            title={t("searchDialog.title")}
            description={t("searchDialog.description")}
            contentClassName="top-[12vh] w-[680px] max-w-[calc(100vw-32px)] max-h-[min(600px,76vh)] border-border-strong"
            onEscapeKeyDown={(event) => {
                if (escapeRef.current?.()) event.preventDefault();
            }}
            commandProps={{
                shouldFilter: false,
                loop: true,
                value: selected,
                onValueChange: setSelected,
                label: t("searchDialog.inputLabel"),
                onKeyDown: (event) => {
                    if (MOVE_KEYS.has(event.key)) movedRef.current = true;
                },
                onPointerMove: () => {
                    movedRef.current = true;
                },
            }}
        >
            <SearchBody
                escapeRef={escapeRef}
                movedRef={movedRef}
                selected={selected}
                setSelected={setSelected}
                close={() => onOpenChange(false)}
                projects={projects}
                automations={automations}
                shortcutFor={shortcutFor}
                actions={actions}
            />
        </CommandDialog>
    );
}

interface SearchBodyProps {
    escapeRef: MutableRefObject<(() => boolean) | null>;
    movedRef: MutableRefObject<boolean>;
    selected: string;
    setSelected: (value: string) => void;
    close: () => void;
    projects: Project[];
    automations?: CronJobState[];
    shortcutFor?: (actionId: string) => string | undefined;
    actions: SearchDialogActions;
}

interface RemoteState {
    query: string;
    results: SearchResults | null;
    /** The whole call failed: every place it reads. */
    failed: boolean;
}

// Mounted only while the dialog is open (Radix unmounts closed content), so
// the query and the loaded sources start fresh on every open.
function SearchBody({ escapeRef, movedRef, selected, setSelected, close, projects, automations, shortcutFor, actions }: SearchBodyProps) {
    const { t, i18n } = useTranslation("app");
    const { t: ts } = useTranslation("settings");
    const language = i18n.resolvedLanguage ?? i18n.language;
    const [query, setQuery] = useState("");
    const needle = query.trim().toLowerCase();
    const paused = useObservationPaused() === true;
    // App rebuilds its handlers on every render; rows read the latest ones
    // when they run instead of being rebuilt with them.
    const actionsRef = useRef(actions);
    actionsRef.current = actions;
    const act = useCallback(() => actionsRef.current, []);

    // Escape clears the query first.
    useEffect(() => {
        const stepBack = () => {
            if (!query) return false;
            setQuery("");
            return true;
        };
        escapeRef.current = stepBack;
        return () => {
            if (escapeRef.current === stepBack) escapeRef.current = null;
        };
    }, [escapeRef, query]);

    // ── Sources read on open ──

    // Your work, from the shared page cache (computer-find v1.0.0: Ready).
    const [work, setWork] = useState<WorkList | null>(() => pageQueryCache.peek<WorkList>("GetWorkList") ?? null);
    const loadWork = useCallback(() => {
        Promise.resolve().then(() => GetWorkList()).then((next) => { if (next) setWork(next); }).catch(() => {});
    }, []);
    usePageQueryRefresh(["GetWorkList"], loadWork);
    useEffect(() => { loadWork(); }, [loadWork]);

    // A reopen lists the cached recents at once; the loads revalidate them.
    const [handoffs, setHandoffs] = useState<HandoffEntry[]>(() => pageQueryCache.peek<HandoffEntry[]>("ListHandoffs") ?? []);
    const [handoffsFailed, setHandoffsFailed] = useState(false);
    const handoffsRequest = useRef(0);
    const loadHandoffs = useCallback(() => {
        const current = ++handoffsRequest.current;
        ListHandoffs()
            .then((entries) => {
                if (current !== handoffsRequest.current) return;
                setHandoffs(entries ?? []);
                setHandoffsFailed(false);
            })
            .catch(() => {
                // The rest still shows; a quiet line names this place.
                if (current === handoffsRequest.current) setHandoffsFailed(true);
            });
    }, []);
    usePageQueryRefresh(["ListHandoffs"], loadHandoffs);
    useEffect(() => {
        loadHandoffs();
        const off = EventsOn("handoff:updated", loadHandoffs);
        return () => {
            handoffsRequest.current += 1;
            off();
        };
    }, [loadHandoffs]);

    // The topic registry: recents, and topic names until SearchEverything's
    // topic source is wired (A1). Under Closed Eyes it returns followed
    // topics only, so it is read again when the pause changes or the registry
    // publishes. Before the registry exists the read is unavailable: no
    // topics, and no failure.
    const [topicsSeed] = useState(() => pageQueryCache.peek<TodayTopicsView>("GetTodayTopics"));
    const [topics, setTopics] = useState<TodayTopic[]>(() => {
        if (topicsSeed === undefined) return [];
        const next = toTodayTopics(topicsSeed);
        const retain = !Array.isArray(topicsSeed) && topicsSeed?.refreshing && !topicsSeed.clear && next.length === 0;
        return retain ? [] : next;
    });
    const [topicsFirstRun, setTopicsFirstRun] = useState(() => !Array.isArray(topicsSeed) && topicsSeed?.firstRun === true);
    const [topicsFailed, setTopicsFailed] = useState(false);
    const topicsRequest = useRef(0);
    const loadTopics = useCallback(() => {
        const current = ++topicsRequest.current;
        GetTodayTopics()
            .then((views) => {
                if (current !== topicsRequest.current) return;
                const next = toTodayTopics(views);
                const retain = !Array.isArray(views) && views?.refreshing && !views.clear && next.length === 0;
                setTopics((previous) => (retain ? previous : reconcileTodayTopics(previous, next)));
                setTopicsFirstRun(!Array.isArray(views) && views?.firstRun === true);
                setTopicsFailed(false);
                markPageContent("search");
            })
            .catch((error) => {
                if (current === topicsRequest.current) setTopicsFailed(!String(error).includes("is not available"));
            });
    }, []);
    usePageQueryRefresh(["GetTodayTopics"], loadTopics);
    useEffect(() => {
        loadTopics();
        const off = EventsOn("topics:updated", loadTopics);
        return () => {
            topicsRequest.current += 1;
            off();
        };
    }, [paused, loadTopics]);
    useEffect(() => {
        // A seeded dialog has recents at mount; the timing mark belongs to it.
        if (topicsSeed !== undefined) markPageContent("search");
    }, [topicsSeed]);

    // ── Search over everything ──

    const [remote, setRemote] = useState<RemoteState | null>(null);
    const requestRef = useRef(0);
    useEffect(() => {
        const text = query.trim();
        const request = ++requestRef.current;
        if (!text) {
            setRemote(null);
            return;
        }
        const timer = window.setTimeout(() => {
            SearchEverything(text, RESULTS_PER_PLACE)
                .then((results) => {
                    if (request === requestRef.current) setRemote({ query: text, results, failed: false });
                })
                .catch(() => {
                    if (request === requestRef.current) setRemote({ query: text, results: null, failed: true });
                });
        }, SEARCH_DEBOUNCE_MS);
        return () => window.clearTimeout(timer);
    }, [query, paused]);
    const current = remote && remote.query === query.trim() ? remote : null;
    const searching = Boolean(needle) && !current;

    // A privacy transition while the dialog is open withdraws the query, the
    // results it produced and the loaded sources at once; the sweep's refresh
    // re-reads what is still permitted. (A closed dialog holds nothing.)
    usePagePrivacyWithdrawal(useCallback(() => {
        handoffsRequest.current += 1;
        topicsRequest.current += 1;
        requestRef.current += 1;
        setQuery("");
        setRemote(null);
        setHandoffs([]);
        setHandoffsFailed(false);
        setTopics([]);
        setTopicsFailed(false);
    }, []));

    // ── Rows ──

    const run = useCallback(
        (fn: () => void) => () => {
            close();
            fn();
        },
        [close],
    );

    const projectIdFor = useCallback(
        (sessionId: string): string => {
            for (const project of projects) {
                for (const workspace of project.workspaces) {
                    if (workspace.sessions.some((session) => session.localId === sessionId)) return project.id;
                }
            }
            return STANDALONE_CHAT_PROJECT_ID;
        },
        [projects],
    );
    // The main chat is "Chat" wherever a result names its chat, as in
    // the sidebar; other chats keep their title.
    const mainChatIds = useMemo(() => {
        const ids = new Set<string>();
        for (const project of projects) {
            for (const workspace of project.workspaces) {
                for (const session of workspace.sessions) if (isMainChatSession(session)) ids.add(session.localId);
            }
        }
        return ids;
    }, [projects]);
    const chatName = useCallback(
        (sessionId: string, title: string) => (mainChatIds.has(sessionId) ? t("sidebar.mainChat") : title),
        [mainChatIds, t],
    );

    // Every chat in every project but the main chat, which has its own
    // action. Most recent first.
    const chats = useMemo(() => {
        const result: ChatEntry[] = [];
        const seen = new Set<string>();
        for (const project of projects) {
            for (const workspace of project.workspaces) {
                for (const session of workspace.sessions) {
                    if (isMainChatSession(session) || seen.has(session.localId)) continue;
                    seen.add(session.localId);
                    result.push({ projectId: project.id, projectName: project.name, session });
                }
            }
        }
        return result.sort((a, b) => b.session.updatedAt - a.session.updatedAt);
    }, [projects]);

    const chatRow = useCallback(
        ({ projectId, projectName, session }: ChatEntry): Row => ({
            key: `chat:${session.localId}`,
            title: session.label,
            // The standalone container's name says nothing; a real project's does.
            meta: joinMeta(
                projectId === STANDALONE_CHAT_PROJECT_ID ? t("searchDialog.meta.chat") : projectName,
                whenInWords(session.updatedAt, t, language),
            ),
            Icon: MessageSquare,
            run: run(() => act().openSession(projectId, session.localId)),
        }),
        [t, language, run, act],
    );

    const topicRow = useCallback(
        (topic: TodayTopic): Row => ({
            key: `topic:${topic.id}`,
            title: topic.name,
            meta: t("searchDialog.meta.topic"),
            Icon: TopicIcon,
            run: run(() => act().openTopic(topic)),
        }),
        [t, run, act],
    );
    // A topic search found, as the registry describes it when it lists it.
    const topicById = useCallback(
        (id: string, name: string): TodayTopic =>
            topics.find((topic) => topic.id === id) ?? { id, name, kind: "work", pinned: false },
        [topics],
    );

    const makeBrief = useCallback(async () => {
        try {
            const result = await GenerateMobileBriefNow();
            switch (result.status) {
                case "cooldown":
                    toast(waitInWords(result.nextAllowedAt, t));
                    return;
                case "running":
                    toast(t("searchDialog.brief.running"));
                    act().openToday();
                    return;
                default:
                    toast.success(t("searchDialog.brief.started"));
                    act().openToday();
            }
        } catch {
            toast.error(t("searchDialog.brief.failed"));
        }
    }, [act, t]);

    const actionRows = useMemo<Row[]>(() => {
        const rows: Row[] = [
            {
                key: "action:pause",
                title: paused ? t("searchDialog.actions.resume") : t("searchDialog.actions.pause"),
                shortcut: shortcutFor?.("pause-observing"),
                Icon: paused ? Play : Pause,
                changesState: true,
                run: run(() => {
                    // Write the new gate state through the cache so every
                    // reader agrees before the backend's event lands.
                    const next = !paused;
                    SetClosedEyesModeEnabled(next)
                        .then(() => pageQueryCache.set("IsClosedEyesModeEnabled", [], next))
                        .catch(() => toast.error(t("searchDialog.pauseFailed")));
                }),
            },
            {
                key: "action:brief",
                title: t("searchDialog.actions.brief"),
                meta: t("searchDialog.meta.today"),
                Icon: Sunrise,
                changesState: true,
                run: run(() => void makeBrief()),
            },
            {
                key: "action:main-chat",
                title: t("searchDialog.mainChat"),
                Icon: MessageSquare,
                run: run(() => act().goToMainChat()),
            },
            {
                key: "action:sidebar",
                title: t("searchDialog.actions.toggleSidebar"),
                shortcut: shortcutFor?.("toggle-sidebar"),
                Icon: PanelLeft,
                run: run(() => act().toggleSidebar()),
            },
            {
                key: "action:settings",
                title: t("searchDialog.actions.settings"),
                shortcut: shortcutFor?.("open-settings"),
                Icon: Settings,
                run: run(() => act().openSettings()),
            },
            {
                key: "action:activity",
                title: t("searchDialog.actions.activity"),
                Icon: Activity,
                run: run(() => act().openActivity()),
            },
            {
                key: "action:know-how",
                title: t("searchDialog.actions.knowHow"),
                Icon: BookOpen,
                run: run(() => act().openKnowHow()),
            },
            {
                key: "action:about-you",
                title: t("searchDialog.actions.aboutYou"),
                Icon: Brain,
                run: run(() => act().openAboutYou()),
            },
        ];
        return needle ? rows.filter((row) => includes(row.title, needle)) : rows;
    }, [paused, t, shortcutFor, run, makeBrief, act, needle]);

    // Tasks with Scheduled and All done, How you do things and Goals: where
    // Library's pages live now (sidebar handoff, section 3).
    const pageRows = useMemo<Row[]>(() => {
        const pages: { id: LibraryPageId | "goals"; label: string; Icon: Icon }[] = [
            { id: "tasks", label: t("sidebar.tasks"), Icon: HandoffIcon },
            { id: "automations", label: t("automations.title"), Icon: AutomationIcon },
            { id: "deliverables", label: t("deliverablesPage.title"), Icon: HandoffIcon },
            { id: "sops", label: t("userMenu.knowledge"), Icon: ListChecks },
            { id: "goals", label: t("searchDialog.groups.goals"), Icon: Target },
        ];
        return pages
            .filter((page) => !needle || includes(page.label, needle))
            .map((page) => ({
                key: `page:${page.id}`,
                title: page.label,
                meta: t("searchDialog.meta.page"),
                Icon: page.Icon,
                run: run(() => page.id === "goals" ? act().openGoal("") : act().openLibraryPage(page.id)),
            }));
    }, [t, needle, run, act]);

    // Like the Settings sidebar search: a hit on the page name opens the
    // page as usual; a hit that came only from its keywords (which name what
    // the page keeps under Advanced) opens it with Advanced expanded. A
    // query that names one chat app opens that app's setup.
    const openSettingsPage = useCallback(
        (entry: SettingsPageEntry, text: string) => {
            const translate = (key: string) => ts(key as never) as string;
            const chatApp = entry.id === "chat-apps" ? chatAppNamed(text, ts) : undefined;
            const needleText = text.trim().toLowerCase();
            const subtitleHit = translate(entry.subtitleKey).toLowerCase().includes(needleText);
            const keywordHit = splitSettingsKeywords(translate(entry.keywordsKey)).some((term) => term.toLowerCase().includes(needleText));
            if (chatApp) act().openSettings(chatApp);
            else if (text && !settingsPageNameMatches(entry, text, translate) && keywordHit && !subtitleHit) {
                act().openSettings(entry.id, { advanced: true });
            } else act().openSettings(entry.id);
        },
        [ts, act],
    );

    const groups = useMemo<Group[]>(() => {
        if (!needle) {
            const recent: Row[] = [
                ...topics.slice(0, RECENT_TOPICS).map(topicRow),
                ...chats.slice(0, RECENT_CHATS).map(chatRow),
            ];
            return [
                { id: "recent" as const, rows: recent },
                { id: "actions" as const, rows: actionRows },
                { id: "pages" as const, rows: pageRows },
            ].filter((group) => group.rows.length > 0);
        }

        const byGroup = new Map<GroupId, Row[]>();
        const push = (id: GroupId, row: Row) => {
            const rows = byGroup.get(id) ?? [];
            if (!rows.some((existing) => existing.key === row.key)) rows.push(row);
            byGroup.set(id, rows);
        };

        for (const row of actionRows) push("actions", row);

        // Ready: what Isoform made or offers that waits for the person.
        for (const item of work?.items ?? []) {
            if (item.state !== "open" && item.state !== "back") continue;
            if (!includes(item.work.title, needle) && !includes(item.noticed, needle)) continue;
            push("ready", {
                key: `ready:${item.id}`,
                title: item.work.title,
                meta: item.first.map((part) => part.text).filter(Boolean).join(" · ") || t(`searchDialog.meta.ready`),
                Icon: ListChecks,
                run: run(() => (act().openWorkItem ?? act().openToday)(item.id)),
            });
        }

        // Topics by name from the registry, then what search found.
        for (const topic of topics) {
            if (includes(topic.name, needle)) push("topics", topicRow(topic));
        }
        // Chats by title, in every project, as both old dialogs found them,
        // or by their project's name, as the palette did.
        const titled = new Set<string>();
        for (const chat of chats) {
            if (!includes(chat.session.label, needle) && !includes(chat.projectName, needle)) continue;
            titled.add(chat.session.localId);
            push("chats", chatRow(chat));
        }

        for (const group of current?.results?.groups ?? []) {
            // Under Closed Eyes nothing Isoform saw is shown, even if a
            // result arrived from before the pause.
            if (paused && group.place === "activity") continue;
            for (const result of group.results ?? []) {
                const row = resultRow(result, {
                    t,
                    language,
                    run,
                    act,
                    projectIdFor,
                    chatName,
                    topicRow: (id, name) => topicRow(topicById(id, name)),
                });
                if (!row) continue;
                if (result.target.kind === "chat" && titled.has(result.target.sessionId)) continue;
                push(PLACE_GROUP[group.place] ?? "chats", row);
            }
        }

        const jobs = (automations ?? []).filter((job) => job.surfaceKind !== "system");
        for (const job of jobs) {
            const name = job.name || job.id;
            if (!includes(name, needle) && !includes(job.description, needle)) continue;
            push("upcoming", {
                key: `upcoming:${job.id}`,
                title: name,
                meta: t("searchDialog.meta.upcoming"),
                Icon: AutomationIcon,
                run: run(() => act().openAutomation(job.id)),
            });
        }
        for (const entry of handoffs) {
            if (!includes(entry.title, needle)) continue;
            push("handoffs", {
                key: `handoff:${entry.id}`,
                title: entry.title,
                meta: t("searchDialog.meta.handoff"),
                Icon: HandoffIcon,
                run: run(() => act().openHandoff(entry.id)),
            });
        }
        // The synthetic standalone container and the legacy "Proactive"
        // project are not projects to open.
        for (const project of projects) {
            if (project.id === STANDALONE_CHAT_PROJECT_ID || project.name === "Proactive") continue;
            if (!includes(project.name, needle)) continue;
            push("projects", {
                key: `project:${project.id}`,
                title: project.name,
                meta: t("searchDialog.meta.project"),
                Icon: FolderKanban,
                run: run(() => act().openProject(project.id)),
            });
        }
        for (const row of pageRows) push("pages", row);

        // Every Settings page from the registry, by name, subtitle or
        // keywords, so a new page shows up here when it is registered.
        const text = query.trim();
        const translate = (key: string) => ts(key as never) as string;
        for (const entry of searchSettingsPages(text, translate)) {
            push("settings", {
                key: `settings:${entry.id}`,
                title: translate(entry.labelKey),
                meta: translate(entry.subtitleKey),
                Icon: entry.icon,
                run: run(() => openSettingsPage(entry, text)),
            });
        }

        const actionsNamed = actionRows.some((row) => row.title.toLowerCase().startsWith(needle));
        return queryOrder(actionsNamed)
            .map((id) => ({ id, rows: byGroup.get(id) ?? [] }))
            .filter((group) => group.rows.length > 0);
    }, [
        work,
        needle,
        query,
        topics,
        chats,
        actionRows,
        pageRows,
        topicRow,
        topicById,
        chatRow,
        current,
        paused,
        t,
        ts,
        language,
        run,
        act,
        projectIdFor,
        chatName,
        automations,
        handoffs,
        projects,
        openSettingsPage,
    ]);

    // ── Selection: the first row, also as more rows arrive, until the
    // person moves it; their row is kept while it is still listed. A first
    // row that changes something is not selected: Enter does nothing until
    // the person moves to a row (arrows or pointer). ──

    const rows = useMemo(() => groups.flatMap((group) => group.rows), [groups]);
    const keys = useMemo(() => rows.map((row) => row.key), [rows]);
    const selectedQuery = useRef<string | null>(null);
    useEffect(() => {
        if (selectedQuery.current !== query) movedRef.current = false;
        selectedQuery.current = query;
        const first = rows[0] && !rows[0].changesState ? rows[0].key : "";
        if ((!movedRef.current || !keys.includes(selected)) && selected !== first) setSelected(first);
    }, [rows, keys, query, selected, setSelected, movedRef]);

    // ── Quiet lines ──

    const failedPlaces = useMemo(() => {
        if (!needle) return [];
        const failed = new Set<FailedPlace>();
        if (current?.failed) ALL_PLACES.forEach((place) => failed.add(place));
        for (const place of current?.results?.failedPlaces ?? []) failed.add(place);
        if (topicsFailed) failed.add("topic");
        if (handoffsFailed) failed.add("handoffs");
        return [...ALL_PLACES, "handoffs" as const].filter((place) => failed.has(place));
    }, [needle, current, topicsFailed, handoffsFailed]);

    const resultCount = keys.length;
    const noMatches = Boolean(needle) && resultCount === 0 && !searching;
    const combo = shortcutFor?.("command-palette");

    return (
        <>
            <CommandInput
                placeholder={t("searchDialog.placeholder")}
                value={query}
                onValueChange={setQuery}
                trailing={
                    needle ? (
                        <span className="shrink-0 text-xs text-muted" aria-live="polite">
                            {searching ? t("searchDialog.searching") : t("searchDialog.count", { count: resultCount })}
                        </span>
                    ) : null
                }
            />
            <CommandList className="max-h-none min-h-0 flex-1 px-2 pt-1.5 pb-2">
                {groups.map((group) => (
                    <CommandGroup key={group.id} heading={t(`searchDialog.groups.${group.id}`)}>
                        {group.rows.map((row) => (
                            <CommandItem key={row.key} value={row.key} onSelect={row.run} className="group gap-3 px-2.5">
                                <span className="flex size-7 shrink-0 items-center justify-center rounded-lg border border-border bg-panel-warm text-muted">
                                    <row.Icon className="size-3.5" />
                                </span>
                                <span className="flex min-w-0 flex-1 flex-col gap-px">
                                    <span className="truncate text-[13.5px] font-medium text-ink">{row.title}</span>
                                    {row.meta ? <span className="truncate text-xs text-muted">{row.meta}</span> : null}
                                </span>
                                {row.shortcut ? (
                                    <kbd className="shrink-0 font-sans text-xs text-muted">{row.shortcut}</kbd>
                                ) : (
                                    <span className="hidden shrink-0 text-xs text-muted group-data-[selected=true]:inline">
                                        {t("searchDialog.enterToOpen")}
                                    </span>
                                )}
                            </CommandItem>
                        ))}
                    </CommandGroup>
                ))}
                {noMatches ? (
                    <p className="px-3 py-6 text-[13.5px] text-muted">{t("searchDialog.noResults", { query: query.trim() })}</p>
                ) : null}
                {topicsFirstRun && topics.length === 0 ? (
                    <p className="px-3 pt-2 pb-1 text-xs text-muted" data-testid="search-topics-gathering">
                        {t("searchDialog.topicsGathering")}
                    </p>
                ) : null}
                {failedPlaces.length > 0 ? (
                    <p className="px-3 pt-2 pb-1 text-xs text-muted" data-testid="search-failed">
                        {t("searchDialog.failed", {
                            places: listOf(
                                failedPlaces.map((place) => t(`searchDialog.places.${place}`)),
                                language,
                            ),
                        })}
                    </p>
                ) : null}
                {paused && needle ? (
                    <p className="px-3 pt-2 pb-1 text-xs text-muted" data-testid="search-paused">
                        {t("searchDialog.pausedNote")}
                    </p>
                ) : null}
            </CommandList>
            <div className="flex items-center gap-3 whitespace-nowrap border-t border-border px-4 py-2.5 text-xs text-muted">
                {combo ? <span>{t("searchDialog.hints.anywhere", { combo })}</span> : null}
                <span>{t("searchDialog.hints.move")}</span>
                <span>{t("searchDialog.hints.open")}</span>
                <span>{t("searchDialog.hints.close")}</span>
                <span className="ml-auto min-w-0 truncate">{t("searchDialog.hints.words")}</span>
            </div>
        </>
    );
}

interface ResultRowContext {
    t: TFunction<"app">;
    language: string;
    run: (fn: () => void) => () => void;
    act: () => SearchDialogActions;
    projectIdFor: (sessionId: string) => string;
    chatName: (sessionId: string, title: string) => string;
    topicRow: (id: string, name: string) => Row;
}

/** One SearchEverything result as a row: what it is and when, in words. */
function resultRow(result: SearchResult, ctx: ResultRowContext): Row | null {
    const { t, language, run, act } = ctx;
    const when = whenInWords(result.at, t, language);
    const target = result.target;
    switch (target.kind) {
        case "topic":
            return ctx.topicRow(target.id, result.title);
        case "chat":
            return {
                key: `chat:${target.sessionId}`,
                title: ctx.chatName(target.sessionId, result.title),
                meta: joinMeta(t("searchDialog.meta.chat"), when),
                Icon: MessageSquare,
                run: run(() => act().openSession(ctx.projectIdFor(target.sessionId), target.sessionId)),
            };
        case "chat_item":
            return {
                key: `message:${target.sessionId}:${target.itemId}`,
                // The matching words; the chat it is in goes in the meta.
                title: result.snippet || result.title,
                meta: joinMeta(ctx.chatName(target.sessionId, result.title), when, result.topicName),
                Icon: MessageSquare,
                run: run(() => act().openChatMessage(target.sessionId, target.itemId)),
            };
        case "activity_moment":
            return {
                key: `activity:${result.id}`,
                title: result.title,
                meta: joinMeta(t("searchDialog.meta.moment"), when, result.topicName),
                Icon: Activity,
                run: run(() => act().openActivity(target.at || result.at)),
            };
        case "activity_thread":
            return {
                key: `activity:${result.id}`,
                title: result.title,
                meta: joinMeta(t("searchDialog.meta.thread"), when, result.topicName),
                Icon: Waypoints,
                run: run(() => act().openActivity(target.startMs || result.at)),
            };
        case "person":
            return {
                key: `person:${target.id}`,
                title: result.title,
                meta: t("searchDialog.meta.person"),
                Icon: CircleUser,
                run: run(() => act().openPerson(target.id)),
            };
        case "goal":
            return {
                key: `goal:${target.id}`,
                title: result.title,
                meta: t("searchDialog.meta.goal"),
                Icon: Target,
                run: run(() => act().openGoal(target.id)),
            };
        case "knowledge":
            return {
                key: `know-how:${target.id}`,
                title: result.title,
                meta: joinMeta(t("searchDialog.meta.knowHow"), when),
                Icon: BookOpen,
                run: run(() => act().openKnowHow(target.id)),
            };
        case "memory":
            return {
                key: `about-you:${target.id}`,
                title: result.title,
                meta: t("searchDialog.meta.aboutYou"),
                Icon: Brain,
                run: run(() => act().openAboutYou(target.id)),
            };
        default:
            return null;
    }
}
