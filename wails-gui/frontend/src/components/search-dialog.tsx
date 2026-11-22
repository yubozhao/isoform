import {
    Fragment,
    useCallback,
    useEffect,
    useMemo,
    useRef,
    useState,
    type ComponentType,
    type MutableRefObject,
    type ReactNode,
} from "react";
import { useTranslation } from "react-i18next";
import type { TFunction } from "i18next";
import {
    Activity,
    BookOpen,
    Bot,
    Brain,
    CircleUser,
    MessageSquare,
    ListChecks,
    Settings,
    Sunrise,
} from "lucide-react";
import {
    CommandDialog,
    CommandGroup,
    CommandInput,
    CommandItem,
    CommandList,
} from "@/components/ui/command";
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
import { PAGE_KEYS, type PageKey } from "@/hooks/useKeybindings";
import { toTodayTopics } from "@/hooks/useTodayTopics";
import { shortDate } from "@/lib/know-how";
import { calendarDaysAgo } from "@/lib/memory-content";
import { markPageContent } from "@/lib/page-switch-timing";
import { STANDALONE_CHAT_PROJECT_ID, isMainChatSession } from "@/lib/stream-helpers";
import {
    FindAnything,
    ForgetFound,
    GetMemoryForgetPreview,
    NoteFindSearch,
    SearchEverything,
    type FindResult,
    type FindResults,
    type SearchPlace,
    type SearchResult,
    type SearchResults,
    type TodayTopicsView,
} from "@/lib/wails-shim-app";
import { pageQueryCache } from "@/lib/query-cache";
import { GetTodayTopics, GetWorkList, usePagePrivacyWithdrawal, usePageQueryRefresh } from "@/lib/page-queries";
import type { WorkList } from "@/lib/wails-shim-app";
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
    /** Opens one of the five places, as its key (⌘1 to ⌘5) does. */
    goToPage: (page: PageKey) => void;
}

interface SearchDialogProps {
    open: boolean;
    onOpenChange: (open: boolean) => void;
    projects: Project[];
    /** Unused since Find dropped Scheduled (computer-map v1.0.0 section 3.8);
     *  App still passes it. */
    automations?: CronJobState[];
    /** The shortcut a keybinding id is bound to, ready to show. */
    shortcutFor?: (actionId: string) => string | undefined;
    actions: SearchDialogActions;
}

// Conformance answers S10 (replacing computer-find v1.0.0 rule 4): Ready,
// Topics, Conversations, People, Claims, Chat, Your answers, Go to; Go to is
// navigation, so it is last, and the Settings pages end it. An empty box
// shows Ready alone. ⌘K finds; it runs no commands (S6).
// The Topics page's Recent topics, the old Library pages and the places
// FindAnything now covers are gone (computer-map v1.0.0 section 3).
type GroupId =
    | "ready"
    | "topics"
    | "conversations"
    | "people"
    | "claims"
    | "chats"
    | "answers"
    | "goTo";

const QUERY_ORDER: readonly GroupId[] = ["ready", "topics", "conversations", "people", "claims", "chats", "answers", "goTo"];


/** The query's words, as FindAnything matches them (findQueryWords): the
 *  lowercased words, from two characters (one for CJK). */
function queryWords(query: string): string[] {
    const text = query.trim();
    const runes = Array.from(text);
    if (runes.length > 200) return [];
    if (runes.length < 2 && !(runes.length === 1 && /[\p{Script=Han}\p{Script=Hiragana}\p{Script=Katakana}\p{Script=Hangul}]/u.test(runes[0]))) return [];
    return text.toLowerCase().split(/\s+/).filter(Boolean);
}

function escapeRegExp(text: string): string {
    return text.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

/** The text with the query's words in bold (conformance answers S5: why a
 *  result matched shows as its matched words), as React elements. */
function withMatches(text: string, words: readonly string[]): ReactNode {
    if (!words.length || !text) return text;
    const pattern = new RegExp(`(${[...words].sort((a, b) => b.length - a.length).map(escapeRegExp).join("|")})`, "giu");
    const parts = text.split(pattern);
    if (parts.length === 1) return text;
    return parts.map((part, index) =>
        index % 2 === 1 ? <strong key={index} data-testid="find-match" className="font-semibold text-ink">{part}</strong> : <Fragment key={index}>{part}</Fragment>,
    );
}

// The SearchEverything places ⌘K still lists: topics and chats (no other
// source finds them), people, which join FindAnything's People, and How you
// do things, whose lines are claims on You (you v1.0.0). The rest are what
// Conversations and Claims find, or pages the design removed.
const PLACE_GROUP: Partial<Record<SearchPlace, GroupId>> = {
    topic: "topics",
    chat: "chats",
    person: "people",
    know_how: "claims",
};

const SHOWN_PLACES: readonly SearchPlace[] = ["topic", "chat", "person", "know_how"];
/** Ready rows on an empty box: the work that waits, first. */
const READY_WHEN_EMPTY = 5;

const RESULTS_PER_PLACE = 8;
// Keys that move the selection: after one, results arriving later keep the
// row the person moved to instead of jumping back to the first.
const MOVE_KEYS = new Set(["ArrowDown", "ArrowUp", "Home", "End"]);
const SEARCH_DEBOUNCE_MS = 120;

type Icon = ComponentType<{ className?: string }>;

interface Row {
    /** The cmdk value: unique in the list. */
    key: string;
    title: string;
    meta?: string;
    shortcut?: string;
    Icon: Icon;
    run: () => void;
    /** Forget on a found thing (computer-find v1.0.0 rule 5). */
    forget?: () => Promise<void>;
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
 * Find (⌘K, computer-find v1.0.0 section 18b): anything Isoform keeps, ready
 * work first. FindAnything finds conversations, people, claims and your
 * answers; Your work gives Ready; SearchEverything still finds chats. Go to
 * lists the five places with their keys. An empty box shows the work that
 * waits and Go to.
 */
export function SearchDialog({ open, onOpenChange, projects, shortcutFor, actions }: SearchDialogProps) {
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
function SearchBody({ escapeRef, movedRef, selected, setSelected, close, projects, shortcutFor, actions }: SearchBodyProps) {
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

    // The dialog's own content mark, for the page-switch timing.
    useEffect(() => {
        markPageContent("search");
    }, []);

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

    // ⌘K over everything Isoform keeps (computer-find v1.0.0 rule 4; FindAnything).
    const [found, setFound] = useState<FindResults | null>(null);
    const findRef = useRef(0);
    useEffect(() => {
        const text = query.trim();
        const request = ++findRef.current;
        if (!text || paused) { setFound(null); return; }
        const timer = window.setTimeout(() => {
            Promise.resolve().then(() => FindAnything(text, RESULTS_PER_PLACE))
                .then((results) => { if (request === findRef.current) setFound(results ?? null); })
                .catch(() => { if (request === findRef.current) setFound(null); });
        }, SEARCH_DEBOUNCE_MS);
        return () => window.clearTimeout(timer);
    }, [query, paused]);
    const foundNow = found && found.query === query.trim() ? found : null;
    // Found things the person forgot leave the list at once.
    const [forgotten, setForgotten] = useState<ReadonlySet<string>>(new Set());
    const [forgetFailed, setForgetFailed] = useState(false);
    const searching = Boolean(needle) && !current;

    // A privacy transition while the dialog is open withdraws the query, the
    // results it produced and the loaded sources at once; the sweep's refresh
    // re-reads what is still permitted. (A closed dialog holds nothing.)
    usePagePrivacyWithdrawal(useCallback(() => {
        requestRef.current += 1;
        findRef.current += 1;
        setQuery("");
        setRemote(null);
        setFound(null);
    }, []));

    // ── Rows ──

    // What you search for is kept on this computer, once a result opens
    // (rule 6; NoteFindSearch).
    const queryRef = useRef(query);
    queryRef.current = query;
    const run = useCallback(
        (fn: () => void) => () => {
            const text = queryRef.current.trim();
            if (text) void Promise.resolve().then(() => NoteFindSearch(text)).catch(() => {});
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

    // A found topic opens as the registry describes it (its kind, whether it
    // is pinned), read only when it opens: the shared cache first, then one
    // read. Only a topic the registry no longer lists opens from the found row.
    const openTopicById = useCallback(
        (id: string, name: string) => {
            const found: TodayTopic = { id, name, kind: "work", pinned: false };
            const pick = (raw: unknown) => toTodayTopics(raw).find((topic) => topic.id === id) ?? found;
            const cached = pageQueryCache.peek<TodayTopicsView>("GetTodayTopics");
            if (cached !== undefined) {
                act().openTopic(pick(cached));
                return;
            }
            Promise.resolve()
                .then(() => GetTodayTopics())
                .then((raw) => act().openTopic(pick(raw)), () => act().openTopic(found));
        },
        [act],
    );

    // Go to (computer-keys v1.0.0): the five places with their keys, then
    // Settings. The names are the sidebar's, so Find says what the app says.
    const goToRows = useMemo<Row[]>(() => {
        const label: Record<PageKey, string> = {
            today: t("sidebar.today"),
            chat: t("sidebar.mainChat"),
            agents: t("sidebar.agents"),
            activity: t("sidebar.activity"),
            you: t("sidebar.you"),
        };
        const icon: Record<PageKey, Icon> = { today: Sunrise, chat: MessageSquare, agents: Bot, activity: Activity, you: CircleUser };
        const rows: Row[] = PAGE_KEYS.map((page) => ({
            key: `goto:${page}`,
            title: label[page],
            shortcut: shortcutFor?.(`go-${page}`),
            Icon: icon[page],
            run: run(() => act().goToPage(page)),
        }));
        rows.push({
            key: "goto:settings",
            title: t("searchDialog.goTo.settings"),
            shortcut: shortcutFor?.("open-settings"),
            Icon: Settings,
            run: run(() => act().openSettings()),
        });
        return needle ? rows.filter((row) => includes(row.title, needle)) : rows;
    }, [t, shortcutFor, needle, run, act]);

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

    // One FindAnything result as a row: its title, its snippet or source
    // line, and where it opens.
    const foundRow = (result: FindResult): Row | null => {
        const target = result.target;
        const open = target.kind === "work" ? () => (act().openWorkItem ?? act().openToday)(target.itemId ?? target.id ?? "")
            : target.kind === "session" && target.sessionId ? () => (target.itemId ? act().openChatMessage(target.sessionId!, target.itemId) : act().openSession(projectIdFor(target.sessionId!), target.sessionId!))
            : target.kind === "activity" ? () => act().openActivity(target.at ?? target.startMs)
            : target.kind === "person" && target.id ? () => act().openPerson(target.id!)
            : target.kind === "belief" ? () => act().openAboutYou(target.id)
            : target.kind === "answer" ? () => act().openToday()
            : null;
        if (!open) return null;
        // A person is one row whichever source found them.
        const key = result.group === "people" && target.id ? `person:${target.id}` : `${result.group}:${result.id}`;
        if (forgotten.has(key)) return null;
        const what = result.forget;
        const forget = what ? async () => {
            setForgetFailed(false);
            try {
                const token = what.kind === "belief" ? (await GetMemoryForgetPreview(what.id)).confirmationToken : "";
                await ForgetFound(what.kind, what.id, token);
                setForgotten((current) => new Set(current).add(key));
            } catch {
                setForgetFailed(true);
            }
        } : undefined;
        const sourceKey = result.source ? `searchDialog.findSource.${result.source}` : "";
        const source = sourceKey && i18n.exists(`app:${sourceKey}`) ? t(sourceKey as never) as string : "";
        return {
            key,
            forget,
            title: result.title,
            meta: [result.snippet, source].filter(Boolean).join(" · ") || undefined,
            Icon: result.group === "people" ? CircleUser : result.group === "claims" ? Brain : result.group === "answers" ? ListChecks : Activity,
            run: run(open),
        };
    };

    const groups = useMemo<Group[]>(() => {
        const byGroup = new Map<GroupId, Row[]>();
        const push = (id: GroupId, row: Row) => {
            const rows = byGroup.get(id) ?? [];
            if (!rows.some((existing) => existing.key === row.key)) rows.push(row);
            byGroup.set(id, rows);
        };

        // Ready: what Isoform made or offers that waits for the person.
        const waiting = (work?.items ?? []).filter((item) => item.state === "open" || item.state === "back");
        for (const item of needle ? waiting : waiting.slice(0, READY_WHEN_EMPTY)) {
            if (needle && !includes(item.work.title, needle) && !includes(item.noticed, needle)) continue;
            push("ready", {
                key: `ready:${item.id}`,
                title: item.work.title,
                meta: item.first.map((part) => part.text).filter(Boolean).join(" · ") || t(`searchDialog.meta.ready`),
                Icon: ListChecks,
                run: run(() => (act().openWorkItem ?? act().openToday)(item.id)),
            });
        }
        // An empty box shows the work that waits and nothing else (S10).
        if (!needle) {
            const ready = byGroup.get("ready") ?? [];
            return ready.length ? [{ id: "ready" as const, rows: ready }] : [];
        }

        for (const group of foundNow?.groups ?? []) {
            for (const result of group.results ?? []) {
                const row = foundRow(result);
                if (row) push(group.group, row);
            }
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
            const id = PLACE_GROUP[group.place];
            if (!id) continue;
            for (const result of group.results ?? []) {
                const row = resultRow(result, { t, language, run, act, projectIdFor, chatName, openTopicById });
                if (!row) continue;
                if (result.target.kind === "chat" && titled.has(result.target.sessionId)) continue;
                push(id, row);
            }
        }

        // Go to: the places, then every Settings page from the registry, by
        // name, subtitle or keywords, so a new page shows up here when it is
        // registered (S10: Settings pages end Go to).
        for (const row of goToRows) push("goTo", row);
        const text = query.trim();
        const translate = (key: string) => ts(key as never) as string;
        for (const entry of searchSettingsPages(text, translate)) {
            push("goTo", {
                key: `settings:${entry.id}`,
                title: translate(entry.labelKey),
                meta: translate(entry.subtitleKey),
                Icon: entry.icon,
                run: run(() => openSettingsPage(entry, text)),
            });
        }

        return QUERY_ORDER
            .map((id) => ({ id, rows: byGroup.get(id) ?? [] }))
            .filter((group) => group.rows.length > 0);
    }, [
        work,
        foundNow,
        forgotten,
        needle,
        query,
        chats,
        goToRows,
        chatRow,
        current,
        t,
        ts,
        language,
        run,
        act,
        projectIdFor,
        chatName,
        openTopicById,
        openSettingsPage,
    ]);

    // ── Selection: the first row, also as more rows arrive, until the
    // person moves it; their row is kept while it is still listed. ──

    const rows = useMemo(() => groups.flatMap((group) => group.rows), [groups]);
    const keys = useMemo(() => rows.map((row) => row.key), [rows]);
    const selectedQuery = useRef<string | null>(null);
    useEffect(() => {
        if (selectedQuery.current !== query) movedRef.current = false;
        selectedQuery.current = query;
        const first = rows[0]?.key ?? "";
        if ((!movedRef.current || !keys.includes(selected)) && selected !== first) setSelected(first);
    }, [rows, keys, query, selected, setSelected, movedRef]);

    // ── Quiet lines ──

    const failedPlaces = useMemo(() => {
        if (!needle) return [];
        const failed = new Set<SearchPlace>();
        if (current?.failed) SHOWN_PLACES.forEach((place) => failed.add(place));
        for (const place of current?.results?.failedPlaces ?? []) failed.add(place);
        return SHOWN_PLACES.filter((place) => failed.has(place));
    }, [needle, current]);

    const words = useMemo(() => queryWords(query), [query]);
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
                                    <span className="truncate text-[13.5px] font-medium text-ink">{withMatches(row.title, words)}</span>
                                    {row.meta ? <span className="truncate text-xs text-muted">{withMatches(row.meta, words)}</span> : null}
                                </span>
                                {row.forget && (
                                    <button
                                        type="button"
                                        data-testid="find-forget"
                                        className="shrink-0 rounded px-1.5 text-xs text-muted hover:text-ink"
                                        onPointerDown={(event) => event.stopPropagation()}
                                        onClick={(event) => { event.stopPropagation(); event.preventDefault(); void row.forget!(); }}
                                    >{t("searchDialog.forget")}</button>
                                )}
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
                {failedPlaces.length > 0 ? (
                    <p className="px-3 pt-2 pb-1 text-xs text-muted" data-testid="search-failed">
                        {t("searchDialog.failed", {
                            places: listOf(
                                failedPlaces.map((place) => t(`searchDialog.places.${place as "topic" | "chat" | "person" | "know_how"}`)),
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
            {forgetFailed && <p role="alert" className="border-t border-border px-4 pt-2 text-[11.5px] text-copper">{t("searchDialog.forgetFailed")}</p>}
            <p className="border-t border-border px-4 pt-2 text-[11.5px] text-muted" data-testid="find-foot">{t("searchDialog.findFoot")}</p>
            <div className="flex items-center gap-3 whitespace-nowrap px-4 py-2.5 text-xs text-muted">
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
    openTopicById: (id: string, name: string) => void;
}

/** One SearchEverything result ⌘K still lists (a topic, a chat, a message
 *  in one, a person, a know-how line) as a row: what it is and when, in
 *  words. */
function resultRow(result: SearchResult, ctx: ResultRowContext): Row | null {
    const { t, language, run, act } = ctx;
    const when = whenInWords(result.at, t, language);
    const target = result.target;
    switch (target.kind) {
        case "topic":
            // Opens beside Today, as the topic's item does.
            return {
                key: `topic:${target.id}`,
                title: result.title,
                meta: t("searchDialog.meta.topic"),
                Icon: TopicIcon,
                run: run(() => ctx.openTopicById(target.id, result.title)),
            };
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
        case "person":
            return {
                key: `person:${target.id}`,
                title: result.title,
                meta: t("searchDialog.meta.person"),
                Icon: CircleUser,
                run: run(() => act().openPerson(target.id)),
            };
        case "knowledge":
            return {
                key: `know-how:${target.id}`,
                title: result.title,
                meta: joinMeta(t("searchDialog.meta.knowHow"), when),
                Icon: BookOpen,
                run: run(() => act().openKnowHow(target.id)),
            };
        default:
            return null;
    }
}
