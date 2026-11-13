import { useCallback, useEffect, useMemo, useRef, useState, type MutableRefObject, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { ChevronDown, ChevronRight, Download, Languages, Moon, MoreHorizontal, Plus, RefreshCw, Target } from 'lucide-react';
import { SearchIcon } from '@/assets/icons/SearchIcon';
import { ScrollArea } from '@/components/ui/scroll-area';
import { Button } from '@/components/ui/button';
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '@/components/ui/collapsible';
import {
    DropdownMenu,
    DropdownMenuContent,
    DropdownMenuItem,
    DropdownMenuSeparator,
    DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu';
import { cn } from '@/lib/utils';
import {
    SearchMemory,
    SyncMemoryIndex,
    TranslateMemoryEntryToCurrentLanguage,
} from '../../wailsjs/go/main/App';
import {
    PrincipalDTO,
    ReassignMemoryAttribution,
    RefreshDerivedView,
    type AboutYouBelief,
    type AboutYouView,
    type GoalItemView,
    type MemoryForgetPreview,
} from '../lib/wails-shim-app';
import { GetAboutYou, GetAboutYouPortrait, GetMemoryEntries, GetMemoryStatus, usePagePrivacyWithdrawal, usePageQueryRefresh } from '@/lib/page-queries';
import { pageQueryCache } from '@/lib/query-cache';
import { markPageContent } from '@/lib/page-switch-timing';
import type { main } from '../../wailsjs/go/models';
import { AIMemoryImportDialog } from '@/components/ai-memory-import-dialog';
import { MemoryDreamDialog } from '@/components/memory-dream-dialog';
import { MemoryForgetDialog } from '@/components/memory-forget-dialog';
import { MemoryHistoryDialog } from '@/components/memory-history-dialog';
import { MemoryNewDialog } from '@/components/memory-new-dialog';
import { MemoryTranslateAllDialog } from '@/components/memory-translate-dialog';
import { RejectAttributionDialog } from '@/components/memory-reject-dialog';
import { MemoryRow, memoryText, memoryLabel, learnedWhen } from '@/components/memory-row';
import { MemoryDetail } from '@/components/memory-detail';
import { AboutYouPortraitCard, type PortraitEvidence } from '@/components/about-you/portrait';
import { NewThisWeek, answeredStatus, useBeliefReview, type ReviewItem, type ReviewResult } from '@/components/about-you/new-this-week';
import { GoalStory, useGoalsBoard } from '@/components/goals/GoalsPage';
import { GoalsGroup, useGoalsCopy } from '@/components/goals/GoalsGroup';
import { beliefInSecondPerson, MEMORY_KINDS, humanMemoryText, memoryApps, memoryKind, memoryPoints, type MemoryKind, visibleMemoryEntries } from '@/lib/memory-content';
import { beliefSummary } from '@/lib/about-you';
import { historyFound, historyName, useFirstDay } from '@/hooks/useFirstDay';
import { MEMORY_AREAS, memoryArea, sentMessageEvidence, type MemoryArea } from '@/lib/communication';
import { groupIdeas, ideaCopyIds, ideaHead, type IdeaGroup } from '@/lib/idea-copies';
import { useIdeaMeanings } from '@/lib/idea-meanings';

/** Cosine threshold for belief titles (see idea_meaning.go). */
const IDEA_MEANING_THRESHOLD_BELIEFS = 0.75;

interface MemoryPageProps {
    onOpenActivitySession?: (sessionId: string) => void;
    initialSelectedId?: string | null;
    /** The dialog's Escape asks the page first: true when the page stepped
     *  back (an open editor, the open belief), false to let the dialog close. */
    escapeRef?: MutableRefObject<(() => boolean) | null>;
    /** False while the page is closed but kept mounted (behind Activity). */
    active?: boolean;
    /** Opens Chat with "I want to set a new goal" typed and unsent (Add a goal).
     *  Absent, the Add a goal button is not shown. */
    /** Developer tools (⌘⇧⌥D): Dream, Re-index, memory IDs and Technical details show only then. */
    devToolsUnlocked?: boolean;
    /** Opens Chat about a goal, with the goal attached (the story's Talk about). */
    onTalkAboutGoal?: (goal: GoalItemView, prompt: string) => void;
    /** A goal to open from outside (Search, a notice); a new signal opens it again. */
    openGoalId?: string | null;
    openGoalSignal?: number;
    /** Called once the open request is taken. */
    onOpenGoalHandled?: () => void;
}

const COLLAPSED_GROUPS_STORAGE_KEY = 'memory:collapsedGroups';
/** The Goals group's key in the collapsed set, beside the belief areas. */
const GOALS_GROUP = 'goals';

/**
 * Most accurate "updated" timestamp (ms) for a memory entry.
 * Prefers frontmatter `Updated` over file mtime, since reindex/touch
 * operations can reset mtime and misstate when a belief was learned.
 */
/** The list chip changes with the answer, before the next read (answer feedback section 7). */
function withAnswer(belief: AboutYouBelief | undefined, result: ReviewResult | undefined): AboutYouBelief | undefined {
    if (!belief || !result) return belief;
    const status = answeredStatus(result, belief.status);
    return status === belief.status ? belief : { ...belief, status };
}

function bestTime(entry: main.MemoryEntry): number {
    return entry.updatedAt && entry.updatedAt > 0 ? entry.updatedAt : entry.modTime;
}

function highlightMatches(text: string, query: string): ReactNode {
    const needle = query.trim().toLowerCase();
    if (!needle) return text;
    const lower = text.toLowerCase();
    const out: ReactNode[] = [];
    let cursor = 0;
    let key = 0;
    for (let pos = lower.indexOf(needle); pos !== -1; pos = lower.indexOf(needle, cursor)) {
        if (pos > cursor) out.push(text.slice(cursor, pos));
        out.push(
            <mark
                key={key++}
                className="rounded-sm bg-surface-active px-0.5 font-medium text-ink"
            >
                {text.slice(pos, pos + needle.length)}
            </mark>,
        );
        cursor = pos + needle.length;
    }
    if (cursor === 0) return text;
    if (cursor < text.length) out.push(text.slice(cursor));
    return out;
}

function formatFileSize(bytes: number): string {
    if (bytes < 1024) return `${bytes} B`;
    if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
    return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

/**
 * About you: what Isoform believes about the person. A weekly portrait,
 * new beliefs to check, every belief grouped by area with its kind and
 * confidence, and a detail with readable evidence and where it is used.
 * Maintenance stays reachable from the page's More menu, the detail's More
 * menu and Technical details.
 */
export function MemoryPage({
    onOpenActivitySession,
    initialSelectedId,
    escapeRef,
    active = true,
    devToolsUnlocked = false,
    onTalkAboutGoal,
    openGoalId,
    openGoalSignal,
    onOpenGoalHandled,
}: MemoryPageProps) {
    const { t, i18n } = useTranslation(['memory', 'common']);
    // A warm reopen paints the cached reads first and revalidates behind
    // them; only a true first load shows the skeleton.
    const [entries, setEntries] = useState<main.MemoryEntry[]>(() => visibleMemoryEntries(pageQueryCache.peek<main.MemoryEntry[]>('GetMemoryEntries') ?? []));
    const [entriesState, setEntriesState] = useState<'loading' | 'ready' | 'failed'>(() => (pageQueryCache.peek('GetMemoryEntries') !== undefined ? 'ready' : 'loading'));
    const [about, setAbout] = useState<AboutYouView | null>(() => pageQueryCache.peek<AboutYouView>('GetAboutYou') ?? null);
    const [aboutFailed, setAboutFailed] = useState(false);
    const rootRef = useRef<HTMLDivElement>(null);
    const searchRef = useRef<HTMLInputElement>(null);
    // Reads apply in order, so a slow early response never replaces a newer one.
    const entriesRead = useRef(0);
    const aboutRead = useRef(0);
    const [status, setStatus] = useState<main.MemoryStoreStatus | null>(() => pageQueryCache.peek<main.MemoryStoreStatus>('GetMemoryStatus') ?? null);
    // True when the first paint came from the cache.
    const seeded = useRef(pageQueryCache.peek('GetMemoryEntries') !== undefined);
    const [selectedId, setSelectedId] = useState<string | null>(initialSelectedId ?? null);
    const [editRequest, setEditRequest] = useState(0);
    const [keyword, setKeyword] = useState('');
    const [searchResults, setSearchResults] = useState<main.MemorySearchResult[] | null>(null);
    const [, setSearching] = useState(false);
    const [kindFilter, setKindFilter] = useState<MemoryKind | 'all'>('all');
    const [syncing, setSyncing] = useState(false);
    const [forgetTarget, setForgetTarget] = useState<{ id: string; title: string; copyIds?: string[]; copyTexts?: string[]; rejectionReason?: string } | null>(null);
    const [rejectTarget, setRejectTarget] = useState<{ id: string; title: string } | null>(null);
    const [historyFor, setHistoryFor] = useState<main.MemoryEntry | null>(null);
    const [importDialogOpen, setImportDialogOpen] = useState(false);
    const [dreamDialogOpen, setDreamDialogOpen] = useState(false);
    const [newDialogOpen, setNewDialogOpen] = useState(false);
    const [translateAllOpen, setTranslateAllOpen] = useState(false);
    const [translateAllBusy, setTranslateAllBusy] = useState(false);
    const [translating, setTranslating] = useState(false);
    const [translateFailed, setTranslateFailed] = useState(false);
    const [syncFailed, setSyncFailed] = useState(false);
    const [reviewOrder, setReviewOrder] = useState<string[]>([]);
    const reviewSnapshots = useRef(new Map<string, ReviewItem>());
    const [collapsedGroups, setCollapsedGroups] = useState<Set<string>>(() => {
        try {
            const raw = localStorage.getItem(COLLAPSED_GROUPS_STORAGE_KEY);
            if (!raw) return new Set();
            const parsed: unknown = JSON.parse(raw);
            return Array.isArray(parsed) ? new Set<string>(parsed.filter((v): v is string => typeof v === 'string')) : new Set();
        } catch {
            return new Set();
        }
    });

    // Goals share the detail beside the list with beliefs: the board owns the
    // open goal, and the page keeps the two from being open at once.
    const goals = useGoalsBoard({ openGoalId, openGoalSignal, onOpenGoalHandled, inPlaceFailures: true });
    const { setOpenId: setGoalOpenId } = goals;
    const goalOpenId = goals.openId;
    const goalOpened = goals.opened ?? null;
    const goalCopy = useGoalsCopy();
    const goalNow = new Date();
    const goalsRef = useRef<HTMLElement>(null);
    const [addingGoal, setAddingGoal] = useState(false);
    const hasGoals = goals.tracking.length + goals.held.length + goals.done.length > 0;

    useEffect(() => {
        if (initialSelectedId) {
            setSelectedId(initialSelectedId);
        }
    }, [initialSelectedId]);

    // A goal to open from outside (Search, a notice): the belief detail gives
    // way, the Goals group unfolds and comes into view, and the board opens its story.
    useEffect(() => {
        if (!openGoalId) return;
        setSelectedId(null);
        setCollapsedGroups((prev) => {
            if (!prev.has(GOALS_GROUP)) return prev;
            const next = new Set(prev);
            next.delete(GOALS_GROUP);
            return next;
        });
        goalsRef.current?.scrollIntoView?.({ block: 'start' });
        // A new request is a new id or signal; the callbacks are not one.
        // eslint-disable-next-line react-hooks/exhaustive-deps
    }, [openGoalId, openGoalSignal]);

    const loadEntries = useCallback(() => {
        const read = ++entriesRead.current;
        // The cached GetMemoryEntries rides on the shim's export * of the
        // generated bindings, which carries no type for it yet.
        // Y12: the portrait paints with the page, so the first read waits for
        // it too; a failed portrait read still opens the page and says so.
        Promise.all([GetMemoryEntries() as Promise<main.MemoryEntry[]>, GetAboutYouPortrait().catch(() => null)])
            .then(([result]) => {
                if (read !== entriesRead.current) return;
                setEntries(visibleMemoryEntries(result ?? []));
                setEntriesState('ready');
            })
            .catch(() => {
                if (read === entriesRead.current) setEntriesState((state) => (state === 'ready' ? state : 'failed'));
            });
    }, []);

    // A later failed read keeps the last confidence and new beliefs on screen.
    const loadAbout = useCallback(() => {
        const read = ++aboutRead.current;
        GetAboutYou()
            .then((result) => {
                if (read !== aboutRead.current) return;
                setAbout(result);
                setAboutFailed(false);
            })
            .catch(() => {
                if (read === aboutRead.current) setAboutFailed(true);
            });
    }, []);

    const loadStatus = useCallback(() => {
        // Same shim export * gap as GetMemoryEntries above.
        (GetMemoryStatus() as Promise<main.MemoryStoreStatus>).then(setStatus).catch(() => {});
    }, []);

    const loadAll = useCallback(() => {
        loadEntries();
        loadAbout();
        loadStatus();
    }, [loadEntries, loadAbout, loadStatus]);

    // An invalidation (a privacy sweep, memory:updated) or a published
    // revalidation re-runs the reads; single-flight keeps them to one call.
    // Subscribed while hidden too: a privacy transition must replace what a
    // retained page holds before it can be seen again.
    usePageQueryRefresh(['GetMemoryEntries', 'GetAboutYou', 'GetMemoryStatus'], loadAll);

    // A privacy transition withdraws the pre-transition beliefs synchronously
    // — rendered, remembered (search, selection, review copies) and in flight
    // — before the sweep's re-read restores what is still permitted.
    const withdraw = useCallback(() => {
        entriesRead.current++;
        aboutRead.current++;
        setEntries([]);
        setEntriesState('loading');
        setAbout(null);
        setAboutFailed(false);
        setStatus(null);
        setSelectedId(null);
        setKeyword('');
        setSearchResults(null);
        setReviewOrder([]);
        reviewSnapshots.current.clear();
        setForgetTarget(null);
        setRejectTarget(null);
        setHistoryFor(null);
    }, []);
    usePagePrivacyWithdrawal(withdraw);

    useEffect(() => {
        loadAll();
    }, [loadAll]);

    // Reopened after a quiet stretch: re-read whatever outlived its TTL.
    const wasActive = useRef(active);
    useEffect(() => {
        if (active && !wasActive.current) loadAll();
        wasActive.current = active;
    }, [active, loadAll]);

    // A cache-seeded first paint already shows the beliefs; the dev
    // page-switch timing otherwise waits on the network read to mark it.
    useEffect(() => {
        if (seeded.current) markPageContent('about-you');
    }, []);

    // Text hits remain available immediately, even offline or before indexing.
    // Indexed matches supplement the same grouped rows, never filename rows.
    useEffect(() => {
        let cancelled = false;
        setSearchResults(null);
        setSearching(false);
        if (!keyword.trim()) return;
        const timer = setTimeout(() => {
            setSearching(true);
            SearchMemory(keyword.trim(), 20)
                .then((results) => { if (!cancelled) setSearchResults(results ?? []); })
                .catch(() => { if (!cancelled) setSearchResults([]); })
                .finally(() => { if (!cancelled) setSearching(false); });
        }, 300);
        return () => {
            cancelled = true;
            clearTimeout(timer);
        };
    }, [keyword, entries]);

    const beliefsById = useMemo(
        () => new Map((about?.beliefs ?? []).map((belief) => [belief.id, belief] as [string, AboutYouBelief])),
        [about],
    );
    // The beliefs behind a portrait phrase, in the words the groups show.
    const describePortraitBelief = useCallback((id: string): PortraitEvidence | undefined => {
        const entry = entries.find((row) => row.id === id);
        if (!entry) return undefined;
        const belief = beliefsById.get(id);
        return {
            id,
            text: beliefInSecondPerson(humanMemoryText(memoryPoints(entry.content)[0]?.text ?? '')) || memoryLabel(entry, t),
            status: belief?.status ?? 'inferred',
            confidence: belief?.confidence ?? 0,
            sessionCount: belief?.sessionCount ?? 0,
            dayCount: belief?.dayCount ?? 0,
            apps: memoryApps(entry.apps ?? []),
        };
    }, [beliefsById, entries, t]);

    const filteredEntries = useMemo(() => {
        const query = keyword.trim().toLowerCase();
        const matchedIds = new Set((searchResults ?? []).map((result) => result.path.replace(/\.md$/, '')));
        return entries.filter((entry) => {
            if (kindFilter !== 'all' && memoryKind(entry) !== kindFilter) return false;
            return !query || memoryText(entry).toLowerCase().includes(query)
                || memoryLabel(entry, t).toLowerCase().includes(query) || matchedIds.has(entry.id);
        });
    }, [entries, keyword, searchResults, kindFilter, t]);

    // One card per idea (W40, Y33): saved copies whose titles match share a
    // card. Actions reach every copy, so the groups span all entries.
    const ideaTitle = useCallback((entry: main.MemoryEntry) => (
        beliefInSecondPerson(humanMemoryText(memoryPoints(entry.content)[0]?.text ?? '')) || memoryLabel(entry, t)
    ), [t]);
    const meanings = useIdeaMeanings(entries, ideaTitle, bestTime, IDEA_MEANING_THRESHOLD_BELIEFS);
    const ideaGroups = useMemo(() => groupIdeas(entries, ideaTitle, bestTime, meanings), [entries, ideaTitle, meanings]);
    const ideaGroupOf = useMemo(() => {
        const byId = new Map<string, IdeaGroup<main.MemoryEntry>>();
        for (const group of ideaGroups) for (const copy of group.copies) byId.set(copy.id, group);
        return byId;
    }, [ideaGroups]);
    const copyIdsOf = useCallback((id: string) => {
        const group = ideaGroupOf.get(id);
        return group ? ideaCopyIds(group, (entry) => entry.id) : [id];
    }, [ideaGroupOf]);
    // Each shown idea: its newest copy that passes the filters, and how many copies it stands for.
    type IdeaRow = { head: main.MemoryEntry; copies: number };
    const groupedEntries = useMemo<Map<MemoryArea, IdeaRow[]>>(() => {
        const shown = new Set(filteredEntries.map((entry) => entry.id));
        const rows = ideaGroups.flatMap((group): Array<{ area: MemoryArea; row: IdeaRow }> => {
            const head = ideaHead(group, (entry) => shown.has(entry.id));
            if (!head) return [];
            return [{ area: memoryArea(beliefsById.get(head.id)?.area ?? head.area), row: { head, copies: group.copies.length } }];
        });
        return new Map(MEMORY_AREAS.map((area): [MemoryArea, IdeaRow[]] => [
            area,
            rows.filter((item) => item.area === area).map((item) => item.row),
        ]).filter(([, list]) => list.length > 0));
    }, [beliefsById, filteredEntries, ideaGroups]);
    // Ideas, not copies: the counts the page shows.
    const ideaCount = ideaGroups.length;
    const confirmedIdeaCount = useMemo(
        () => ideaGroups.filter((group) => group.copies.some((entry) => beliefsById.get(entry.id)?.status === 'confirmed')).length,
        [beliefsById, ideaGroups],
    );

    const selected = useMemo(
        () => entries.find((e) => e.id === selectedId) ?? null,
        [entries, selectedId],
    );

    useEffect(() => {
        try {
            localStorage.setItem(
                COLLAPSED_GROUPS_STORAGE_KEY,
                JSON.stringify(Array.from(collapsedGroups)),
            );
        } catch {
            // localStorage failures shouldn't break the UI — just lose persistence.
        }
    }, [collapsedGroups]);

    const toggleGroup = useCallback((group: string) => {
        setCollapsedGroups((prev) => {
            const next = new Set(prev);
            if (next.has(group)) next.delete(group);
            else next.add(group);
            return next;
        });
    }, []);

    const handleForgottenInOneClick = useCallback((id: string) => {
        setSelectedId((current) => (current === id ? null : current));
        loadAll();
    }, [loadAll]);
    // Keep the confirmed file: a later point edit posts this content back.
    const handleConfirmed = useCallback((saved: main.MemoryEntry) => {
        // An older list read still in flight must not restore the pre-confirm copy.
        entriesRead.current++;
        setEntries((current) => current.map((entry) => (entry.id === saved.id ? saved : entry)));
        loadAbout();
    }, [loadAbout]);
    const entriesRef = useRef(entries);
    entriesRef.current = entries;
    const contentOf = useCallback((id: string) => entriesRef.current.find((entry) => entry.id === id)?.content, []);
    const review = useBeliefReview({
        onConfirmed: handleConfirmed,
        onForgotten: handleForgottenInOneClick,
        onChanged: loadAll,
        contentOf,
        copiesOf: copyIdsOf,
        // A card for several copies is forgotten from the dialog, which lists them.
        onForgetCopies: (id) => {
            const entry = entriesRef.current.find((row) => row.id === id);
            if (entry) forgetEntry(entry);
        },
    });
    const { cancelPending } = review;

    // A one-click forget runs only while its Undo is in view: closing the
    // page or jumping to Activity (which keeps the page mounted) cancels it.
    useEffect(() => {
        if (!active) cancelPending();
    }, [active, cancelPending]);
    const openActivitySession = useMemo(() => (onOpenActivitySession
        ? (sessionId: string) => {
            cancelPending();
            onOpenActivitySession(sessionId);
        }
        : undefined), [cancelPending, onOpenActivitySession]);

    // New beliefs keep their place once shown, so a card can report what
    // happened after its belief leaves "New this week".
    useEffect(() => {
        const ids = about?.newThisWeek ?? [];
        setReviewOrder((prev) => {
            const added = ids.filter((id) => !prev.includes(id));
            return added.length ? [...prev, ...added] : prev;
        });
    }, [about]);
    // The first days (first-day handoff §5): the three most confident beliefs
    // from the Claude Code or Codex history come first, with their source.
    const firstDay = useFirstDay();
    const { t: tCommon } = useTranslation('common');
    const historyBeliefs = useMemo(
        () => firstDay.firstDays && historyFound(firstDay.history)
            ? (about?.beliefs ?? []).filter((belief) => belief.source === 'assistant-history')
            : [],
        [about, firstDay.firstDays, firstDay.history],
    );
    const checkFirstIds = useMemo(
        () => historyBeliefs
            .filter((belief) => belief.status !== 'confirmed')
            .sort((a, b) => b.confidence - a.confidence || b.sessionCount - a.sessionCount || b.updatedAt - a.updatedAt)
            .slice(0, 3)
            .map((belief) => belief.id),
        [historyBeliefs],
    );
    const checkFirstSnapshots = useRef(new Map<string, ReviewItem>());
    const checkFirstItems = useMemo(() => {
        const items: ReviewItem[] = [];
        for (const id of checkFirstIds) {
            const entry = entries.find((row) => row.id === id);
            const belief = beliefsById.get(id);
            if (!entry || !belief) continue;
            const item: ReviewItem = {
                id,
                text: beliefInSecondPerson(humanMemoryText(memoryPoints(entry.content)[0]?.text ?? '')) || memoryLabel(entry, t),
                where: t('aboutYou.firstDay.source', {
                    assistant: belief.sourceAssistant === 'codex' ? 'Codex' : 'Claude Code',
                    date: new Date(belief.sourceAt || belief.firstLearnedAt).toLocaleDateString(i18n.language, { month: 'short', day: 'numeric' }),
                }),
                communication: memoryArea(belief.area ?? entry.area) === 'communication',
                portraitCandidate: belief.portraitCandidate,
                status: belief.status,
            };
            checkFirstSnapshots.current.set(id, item);
            items.push(item);
        }
        for (const [id, item] of checkFirstSnapshots.current) {
            if (!checkFirstIds.includes(id) && review.results[id]) items.push(item);
        }
        return items;
    }, [beliefsById, checkFirstIds, entries, i18n.language, review.results, t]);
    const reviewItems = useMemo(() => {
        const current = new Set((about?.newThisWeek ?? []).filter((id) => !checkFirstSnapshots.current.has(id)));
        const list = new Intl.ListFormat(i18n.language, { style: 'long', type: 'conjunction' });
        const items: ReviewItem[] = [];
        const savedAt = new Map<string, number>();
        for (const id of reviewOrder) {
            const entry = entries.find((row) => row.id === id);
            if (entry && current.has(id)) {
                savedAt.set(id, bestTime(entry));
                const belief = beliefsById.get(id);
                const seen = belief && belief.sessionCount > 0
                    ? t('aboutYou.review.seen', { count: belief.sessionCount })
                    : learnedWhen(t, belief?.firstLearnedAt || bestTime(entry));
                const apps = memoryApps(entry.apps ?? []);
                const communication = memoryArea(belief?.area ?? entry.area) === 'communication';
                const seenWhere = apps.length ? t('aboutYou.review.where', { seen, apps: list.format(apps) }) : seen;
                const evidence = sentMessageEvidence(t, belief, entry.apps ?? [], i18n.language);
                const item: ReviewItem = {
                    id,
                    text: beliefInSecondPerson(humanMemoryText(memoryPoints(entry.content)[0]?.text ?? '')) || memoryLabel(entry, t),
                    where: communication
                        ? t('aboutYou.review.communicationWhere', {
                            area: t('aboutYou.areas.communication'),
                            evidence: evidence || seenWhere,
                        })
                        : seenWhere,
                    communication,
                    portraitCandidate: belief?.portraitCandidate,
                    status: belief?.status ?? 'inferred',
                };
                reviewSnapshots.current.set(id, item);
                items.push(item);
            } else if (review.results[id] && reviewSnapshots.current.has(id)) {
                items.push(reviewSnapshots.current.get(id)!);
                const live = entries.find((row) => row.id === id);
                savedAt.set(id, live ? bestTime(live) : 0);
            }
        }
        // One card per idea: the newest shown copy heads it, and it stands for every copy.
        const newest = new Map<string, ReviewItem>();
        for (const item of items) {
            const key = ideaGroupOf.get(item.id)?.key ?? `item:${item.id}`;
            const held = newest.get(key);
            if (!held || (savedAt.get(item.id) ?? 0) > (savedAt.get(held.id) ?? 0)) newest.set(key, item);
        }
        return [...newest.values()].map((head) => {
            const copyIds = copyIdsOf(head.id);
            return { ...head, copyIds, copies: copyIds.length };
        });
    }, [about, beliefsById, copyIdsOf, entries, i18n.language, ideaGroupOf, review.results, reviewOrder, t, checkFirstItems]);

    const handleForgot = useCallback(
        (preview: MemoryForgetPreview) => {
            if (selectedId === preview.memoryId) setSelectedId(null);
            review.cancel(preview.memoryId);
            loadAll();
        },
        [selectedId, loadAll, review],
    );

    const handleReassignAttribution = useCallback(
        async (id: string, principal: PrincipalDTO, reason: string) => {
            // A failure stays in the Wrong subject dialog, which says so.
            await ReassignMemoryAttribution(id, principal.id, reason);
            if (selectedId === id) setSelectedId(null);
            loadAll();
        },
        [loadAll, selectedId],
    );

    const handleSelect = useCallback((id: string) => {
        setEditRequest(0);
        setTranslateFailed(false);
        setSelectedId(id);
        setGoalOpenId(null);
    }, [setGoalOpenId]);

    const openGoal = useCallback((goal: GoalItemView) => {
        setSelectedId(null);
        setGoalOpenId(goal.id);
    }, [setGoalOpenId]);

    const handleEdit = useCallback((entry: main.MemoryEntry) => {
        setSelectedId(entry.id);
        setGoalOpenId(null);
        setEditRequest((request) => request + 1);
    }, [setGoalOpenId]);

    // Not quite from a portrait phrase asks the decided question in the detail.
    const handleNotQuite = useCallback((id: string) => {
        if (!entries.some((row) => row.id === id)) return;
        setEditRequest(0);
        setTranslateFailed(false);
        setSelectedId(id);
        setGoalOpenId(null);
        review.ask(id);
    }, [entries, review, setGoalOpenId]);

    const handleSync = useCallback(() => {
        setSyncing(true);
        setSyncFailed(false);
        SyncMemoryIndex()
            .then(loadAll)
            .catch(() => setSyncFailed(true))
            .finally(() => setSyncing(false));
    }, [loadAll]);

    const finishTranslation = useCallback((saved: main.MemoryEntry) => {
        loadAll();
        setTranslateFailed(false);
        setSelectedId(saved.id);
    }, [loadAll]);

    const handleTranslateCurrent = useCallback(() => {
        if (!selected || translating) return;
        setTranslating(true);
        setTranslateFailed(false);
        TranslateMemoryEntryToCurrentLanguage(selected.id)
            .then(finishTranslation)
            .catch(() => setTranslateFailed(true))
            .finally(() => setTranslating(false));
    }, [finishTranslation, selected, translating]);

    // Radix asks the dialog only when the page is the top layer, so an open
    // menu or nested dialog keeps its own Escape. An open point editor
    // cancels itself; a focused search clears; then the open belief or goal closes;
    // then any search clears; then the dialog closes.
    const handleEscape = useCallback(() => {
        const focused = document.activeElement;
        if (focused instanceof HTMLTextAreaElement && rootRef.current?.contains(focused)) return true;
        if (keyword && focused === searchRef.current) {
            setKeyword('');
            return true;
        }
        if (selectedId) {
            setSelectedId(null);
            return true;
        }
        if (goalOpenId) {
            setGoalOpenId(null);
            return true;
        }
        if (keyword) {
            setKeyword('');
            return true;
        }
        return false;
    }, [keyword, selectedId, goalOpenId, setGoalOpenId]);

    useEffect(() => {
        if (!escapeRef) return;
        escapeRef.current = handleEscape;
        return () => {
            if (escapeRef.current === handleEscape) escapeRef.current = null;
        };
    }, [escapeRef, handleEscape]);

    // The reviewed preview replaces a pending one-click forget of the same belief.
    const forgetEntry = (entry: main.MemoryEntry) => {
        const copyIds = copyIdsOf(entry.id);
        for (const copy of copyIds) review.cancel(copy);
        // Every copy is listed in the dialog before any of them is forgotten.
        const copyTexts = copyIds.length > 1
            ? copyIds.filter((id) => id !== entry.id).map((id) => ideaTitle(entries.find((row) => row.id === id) ?? entry))
            : undefined;
        setForgetTarget({ id: entry.id, title: memoryLabel(entry, t), copyIds, copyTexts });
    };
    const rejectEntry = (entry: main.MemoryEntry) => setRejectTarget({ id: entry.id, title: memoryLabel(entry, t) });

    const totals = about?.totals ?? { beliefs: entries.length, confirmed: 0 };
    const earliest = useMemo(() => {
        const times = (about?.beliefs ?? []).map((belief) => belief.firstLearnedAt).filter((at) => at > 0);
        const fallback = entries.map(bestTime).filter((at) => at > 0);
        const all = times.length ? times : fallback;
        return all.length ? Math.min(...all) : 0;
    }, [about, entries]);
    const storage = about?.storage ?? (status
        ? { files: entries.length, bytes: entries.reduce((sum, entry) => sum + entry.sizeBytes, 0), chunks: status.chunks, vectorReady: status.vectorReady }
        : null);

    // About you is built once a day; Refresh now builds it again (the page
    // keeps showing the current version until the new one arrives).
    const refreshAboutYou = () => {
        void RefreshDerivedView('about-you')
            .then(() => pageQueryCache.invalidate(['GetAboutYou', 'GetAboutYouPortrait', 'GetPortraitTodayCard'], false))
            .catch(() => undefined);
    };

    const pageMenu = (
        <DropdownMenu>
            <DropdownMenuTrigger asChild>
                <Button variant="ghost" size="icon" aria-label={t('aboutYou.more')}>
                    <MoreHorizontal className="h-4 w-4" />
                </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end">
                <DropdownMenuItem onSelect={() => setNewDialogOpen(true)}>
                    <Plus />
                    {t('header.newFile')}
                </DropdownMenuItem>
                <DropdownMenuItem onSelect={() => setImportDialogOpen(true)}>
                    <Download />
                    {t('header.importFromAi')}
                </DropdownMenuItem>
                {goals.page && !hasGoals && (
                    <DropdownMenuItem data-testid="menu-add-goal" onSelect={() => setAddingGoal(true)}>
                        <Target />
                        {t('aboutYou.goals.add')}
                    </DropdownMenuItem>
                )}
                {devToolsUnlocked && (
                    <DropdownMenuItem onSelect={() => setDreamDialogOpen(true)}>
                        <Moon />
                        {t('aboutYou.dream')}
                    </DropdownMenuItem>
                )}
                <DropdownMenuItem onSelect={refreshAboutYou}>
                    <RefreshCw />
                    {t('aboutYou.refresh')}
                </DropdownMenuItem>
                <DropdownMenuSeparator />
                <DropdownMenuItem disabled={translateAllBusy || entries.length === 0} onSelect={() => setTranslateAllOpen(true)}>
                    <Languages />
                    {t('header.globalTranslate')}
                </DropdownMenuItem>
                {devToolsUnlocked && (
                    <DropdownMenuItem disabled={syncing} onSelect={handleSync}>
                        <RefreshCw className={cn(syncing && 'motion-safe:animate-spin')} />
                        {t('header.reindex')}
                    </DropdownMenuItem>
                )}
            </DropdownMenuContent>
        </DropdownMenu>
    );

    let beliefs: ReactNode;
    if (entriesState === 'loading') {
        beliefs = (
            <div aria-busy="true" className="flex flex-col gap-3">
                {[0, 1].map((column) => (
                    <div key={column} className="flex flex-col gap-3">
                        <span className="block h-3.5 w-1/3 rounded-md bg-surface-active motion-safe:animate-pulse" />
                        {['90%', '75%', '82%'].map((width) => (
                            <span key={width} className="block h-3 rounded-md bg-surface-active motion-safe:animate-pulse" style={{ width }} />
                        ))}
                    </div>
                ))}
            </div>
        );
    } else if (entriesState === 'failed') {
        beliefs = (
            <div role="alert" className="flex items-center gap-3 rounded-xl border border-border bg-panel px-4 py-3.5 text-[13px] text-copper">
                <span className="flex-1">{t('aboutYou.loadFailed')}</span>
                <Button size="sm" variant="secondary" onClick={loadAll}>{t('aboutYou.retry')}</Button>
            </div>
        );
    } else if (entries.length === 0) {
        beliefs = null;
    } else {
        beliefs = (
            <section aria-label={t('aboutYou.beliefs')} className="flex flex-col gap-5">
                <div className="flex flex-wrap items-center gap-x-4 gap-y-2.5">
                    <div className="relative min-w-[200px] max-w-sm flex-1">
                        <SearchIcon className="absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted" />
                        <input
                            ref={searchRef}
                            type="text"
                            placeholder={t('searchPlaceholder') as string}
                            aria-label={t('searchPlaceholder') as string}
                            className="w-full rounded-lg border border-border bg-panel py-1.5 pl-8 pr-3 text-[13px] text-ink placeholder:text-muted focus:border-border-focus focus:outline-none"
                            value={keyword}
                            onChange={(e) => setKeyword(e.target.value)}
                        />
                    </div>
                    <label className="flex items-center gap-2 text-[13px] text-muted">
                        {t('sidebar.filterKind')}
                        <select
                            className="rounded-lg border border-border bg-panel px-2 py-1.5 text-[13px] text-ink focus:border-border-focus focus:outline-none"
                            value={kindFilter}
                            onChange={(event) => setKindFilter(event.target.value as MemoryKind | 'all')}
                        >
                            <option value="all">{t('sidebar.allKinds')}</option>
                            {MEMORY_KINDS.map((kind) => (
                                <option key={kind} value={kind}>{t(`kinds.${kind}`)}</option>
                            ))}
                        </select>
                    </label>
                </div>
                {filteredEntries.length === 0 ? (
                    <p className="m-0 py-6 text-center text-[13px] text-muted">{keyword.trim() ? t('sidebar.nothingMatches', { query: keyword.trim() }) : t('sidebar.noFilesMatch')}</p>
                ) : (
                    // Groups stack at full width; each group's beliefs flow into
                    // as many columns as the width allows.
                    <div data-testid="belief-groups" className="flex flex-col gap-7">
                        {Array.from(groupedEntries.entries()).map(([group, rows]) => {
                            const collapsed = !keyword.trim() && collapsedGroups.has(group);
                            const label = t(`aboutYou.areas.${group}`);
                            return (
                                <Collapsible key={group} asChild open={!collapsed} onOpenChange={() => toggleGroup(group)}>
                                <section className="flex min-w-0 flex-col gap-1.5">
                                    <h2 className="m-0">
                                        <CollapsibleTrigger className="inline-flex items-baseline gap-2 rounded-md pb-1 text-left text-base font-semibold text-ink focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-border-focus">
                                            <span>{label}</span>
                                            <span className="text-[12.5px] font-normal text-muted">{rows.length}</span>
                                            {collapsed
                                                ? <ChevronRight className="h-3.5 w-3.5 self-center text-muted" />
                                                : <ChevronDown className="h-3.5 w-3.5 self-center text-muted" />}
                                        </CollapsibleTrigger>
                                    </h2>
                                    {group === 'communication' && (
                                        <p className="m-0 pb-1 text-[12.5px] text-muted">{t('aboutYou.communication.note')}</p>
                                    )}
                                    <CollapsibleContent data-testid="belief-group-rows" className="grid gap-x-6 gap-y-1.5 [grid-template-columns:repeat(auto-fill,minmax(min(100%,340px),1fr))]">
                                    {rows.map(({ head: entry, copies }) => (
                                        <MemoryRow
                                            key={entry.id}
                                            entry={entry}
                                            copies={copies}
                                            belief={withAnswer(beliefsById.get(entry.id), review.results[entry.id])}
                                            showKind
                                            selected={selectedId === entry.id}
                                            onSelect={() => handleSelect(entry.id)}
                                            onEdit={() => handleEdit(entry)}
                                            onForget={() => forgetEntry(entry)}
                                            onReject={() => rejectEntry(entry)}
                                            highlight={(text) => highlightMatches(text, keyword)}
                                        />
                                    ))}
                                    </CollapsibleContent>
                                </section>
                                </Collapsible>
                            );
                        })}
                    </div>
                )}
            </section>
        );
    }

    return (
        <div ref={rootRef} className="flex h-full min-h-0 bg-background text-ink">
            <ScrollArea className="min-w-0 flex-1">
                <main className="flex flex-col gap-7 px-10 pt-9 pb-14">
                    <header className="flex items-start gap-3 pr-10">
                        <div className="flex min-w-0 flex-1 flex-col gap-2">
                            <div className="text-[11px] font-medium uppercase tracking-[0.08em] text-muted">{t('aboutYou.eyebrow')}</div>
                            <h1 className="m-0 text-[26px] font-normal leading-[1.3] text-ink">{t('aboutYou.title')}</h1>
                            {entriesState === 'ready' && (
                                <p className="m-0 text-sm text-muted">
                                    {historyBeliefs.length > 0 && totals.confirmed === 0 && firstDay.history
                                        ? t('aboutYou.firstDay.lineWithLocal', { line: t('aboutYou.firstDay.line', { count: historyBeliefs.length, history: historyName(firstDay.history.assistants, tCommon) }), local: t('aboutYou.summary.local') })
                                        : entries.length === 0
                                        ? t('aboutYou.summary.empty')
                                        : t('aboutYou.summary.line', {
                                            beliefs: beliefSummary(t, ideaCount || entries.length, earliest),
                                            confirmed: !about
                                                ? ''
                                                : confirmedIdeaCount > 0
                                                    ? t('aboutYou.summary.confirmed', { count: confirmedIdeaCount })
                                                    : t('aboutYou.summary.noneConfirmed'),
                                            local: t('aboutYou.summary.local'),
                                        }).replace(/\s{2,}/g, ' ')}
                                </p>
                            )}
                        </div>
                        {pageMenu}
                    </header>

                    {devToolsUnlocked && syncFailed && (
                        <p role="alert" className="m-0 -mt-4 flex flex-wrap items-center gap-2 text-[12.5px] text-copper">
                            <span>{t('aboutYou.reindexFailed')}</span>
                            <button type="button" className="text-accent hover:underline" disabled={syncing} onClick={handleSync}>{t('aboutYou.retry')}</button>
                        </p>
                    )}

                    <NewThisWeek
                        items={checkFirstItems}
                        review={review}
                        headingId="about-you-check-first"
                        title={t('aboutYou.firstDay.checkFirst')}
                        hint={firstDay.history ? t('aboutYou.firstDay.checkFirstHint', { history: historyName(firstDay.history.assistants, tCommon) }) : undefined}
                    />

                    {entriesState === 'ready' && entries.length > 0 && (
                        <AboutYouPortraitCard describe={describePortraitBelief} onShowBelief={handleSelect} onNotQuiteBelief={handleNotQuite} />
                    )}

                    {aboutFailed && !about && entriesState === 'ready' && entries.length > 0 && (
                        <div role="alert" className="flex items-center gap-3 rounded-xl border border-border bg-panel px-4 py-3.5 text-[13px] text-copper">
                            <span className="flex-1">{t('aboutYou.aboutFailed')}</span>
                            <Button size="sm" variant="secondary" onClick={loadAbout}>{t('aboutYou.retry')}</Button>
                        </div>
                    )}

                    <GoalsGroup
                        board={goals}
                        now={goalNow}
                        open={!collapsedGroups.has(GOALS_GROUP)}
                        onToggle={() => toggleGroup(GOALS_GROUP)}
                        adding={addingGoal}
                        onAddingChange={setAddingGoal}
                        onOpenGoal={openGoal}
                        selectedId={goalOpenId}
                        groupRef={goalsRef}
                    />

                    <NewThisWeek items={reviewItems} review={review} />

                    {beliefs}

                    {devToolsUnlocked && storage && (
                        <Collapsible className="text-[12.5px] text-muted">
                            <CollapsibleTrigger className="group/tech flex items-center gap-1.5 rounded-md hover:text-ink focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-border-focus">
                                {t('aboutYou.technical.title')}
                                <ChevronDown className="h-3.5 w-3.5 transition-transform group-data-[state=open]/tech:rotate-180" />
                            </CollapsibleTrigger>
                            <CollapsibleContent>
                                <p className="m-0 mt-2">
                                    {[
                                        t('aboutYou.technical.files', { count: storage.files }),
                                        formatFileSize(storage.bytes),
                                        t('aboutYou.technical.chunks', { count: storage.chunks }),
                                        t(storage.vectorReady ? 'aboutYou.technical.vectorActive' : 'aboutYou.technical.vectorInactive'),
                                    ].join(' · ')}
                                </p>
                                {status?.model && <p className="m-0 mt-1">{t('aboutYou.technical.model', { model: status.model })}</p>}
                            </CollapsibleContent>
                        </Collapsible>
                    )}
                </main>
            </ScrollArea>

            {selected ? (
                <aside
                    aria-label={t('aboutYou.detail.label')}
                    className="flex w-[min(420px,45%)] shrink-0 flex-col border-l border-border-subtle bg-background-accent"
                >
                    <ScrollArea className="flex-1">
                        <div className="py-7 pl-6 pr-12">
                            <MemoryDetail
                                key={selected.id}
                                entry={selected}
                                copyIds={copyIdsOf(selected.id).filter((id) => id !== selected.id)}
                                belief={beliefsById.get(selected.id)}
                                editRequest={editRequest}
                                onSaved={(saved) => {
                                    setEntries((current) => current.map((entry) => entry.id === saved.id ? saved : entry));
                                    loadAbout();
                                    loadStatus();
                                }}
                                onForget={() => forgetEntry(selected)}
                                onReject={() => rejectEntry(selected)}
                                review={review}
                                onClose={() => setSelectedId(null)}
                                onHistory={() => setHistoryFor(selected)}
                                onTranslate={handleTranslateCurrent}
                                onOpenBelief={handleSelect}
                                onOpenActivitySession={openActivitySession}
                                devTools={devToolsUnlocked}
                                translateFailed={translateFailed}
                            />
                        </div>
                    </ScrollArea>
                </aside>
            ) : goalOpened ? (
                <aside
                    aria-label={t('aboutYou.goals.detailLabel')}
                    className="flex w-[min(420px,45%)] shrink-0 flex-col border-l border-border-subtle bg-background-accent"
                >
                    <ScrollArea className="flex-1">
                        <div className="py-7 pl-6 pr-12">
                            <GoalStory
                                goal={goalOpened}
                                now={goalNow}
                                language={goalCopy.language}
                                t={goalCopy.tr}
                                actions={goals.actions}
                                onBack={() => goals.setOpenId(null)}
                                onTalkAbout={onTalkAboutGoal
                                    ? () => onTalkAboutGoal(goalOpened, goalCopy.tr('goalsPage.story.talkPrompt', { title: goalOpened.title }))
                                    : undefined}
                            />
                        </div>
                    </ScrollArea>
                </aside>
            ) : null}

            <MemoryForgetDialog
                target={forgetTarget}
                rejectionReason={forgetTarget?.rejectionReason}
                onOpenChange={(open) => { if (!open) setForgetTarget(null); }}
                onForgot={handleForgot}
                devTools={devToolsUnlocked}
            />

            <RejectAttributionDialog
                target={rejectTarget}
                onOpenChange={(open) => { if (!open) setRejectTarget(null); }}
                onReject={async (reason) => {
                    if (!rejectTarget) return;
                    setForgetTarget({ ...rejectTarget, rejectionReason: reason });
                    setRejectTarget(null);
                }}
                onReassign={async (principal, reason) => {
                    if (!rejectTarget) return;
                    await handleReassignAttribution(rejectTarget.id, principal, reason);
                    setRejectTarget(null);
                }}
            />

            <MemoryHistoryDialog
                entry={historyFor}
                onOpenChange={(open) => { if (!open) setHistoryFor(null); }}
                onTranslated={finishTranslation}
                onOpenActivitySession={openActivitySession}
            />

            <MemoryNewDialog
                open={newDialogOpen}
                onOpenChange={setNewDialogOpen}
                onSaved={(saved) => {
                    loadAll();
                    setSelectedId(saved.id);
                }}
            />

            <MemoryTranslateAllDialog
                open={translateAllOpen}
                onOpenChange={setTranslateAllOpen}
                entryCount={entries.length}
                onBusyChange={setTranslateAllBusy}
                onDone={loadAll}
            />

            <AIMemoryImportDialog
                open={importDialogOpen}
                onOpenChange={setImportDialogOpen}
                onImported={loadAll}
            />

            <MemoryDreamDialog
                open={dreamDialogOpen}
                onOpenChange={setDreamDialogOpen}
                onRunCompleted={loadAll}
            />
        </div>
    );
}
