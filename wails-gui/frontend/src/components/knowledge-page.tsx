import { useCallback, useEffect, useMemo, useRef, useState, type MutableRefObject } from 'react';
import type { TFunction } from 'i18next';
import { historyFound, historyName, useFirstDay } from '@/hooks/useFirstDay';
import { useTranslation } from 'react-i18next';
import {
    BookOpen,
    CalendarDays,
    Check,
    ChevronRight,
    Copy,
    History,
    Languages,
    MoreHorizontal,
    RefreshCw,
    PenLine,
    RotateCcw,
    Save,
    Trash2,
    X,
} from 'lucide-react';
import { SearchIcon } from '@/assets/icons/SearchIcon';
import { ScrollArea } from '@/components/ui/scroll-area';
import { Button } from '@/components/ui/button';
import { Textarea } from '@/components/ui/textarea';
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '@/components/ui/collapsible';
import {
    DropdownMenu,
    DropdownMenuContent,
    DropdownMenuItem,
    DropdownMenuSeparator,
    DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu';
import { flattenSessions, parseFrontmatter, splitHeaderAndBody } from '@/components/entry-content';
import { MarkdownContent } from '@/components/markdown-content';
import { onAccountIdentityWithdrawal } from '@/lib/privacy-reset';
import { ConfirmDialog } from '@/components/confirm-dialog';
import { KnowledgeTranslateDialog, type GlobalTranslateTarget } from '@/components/knowledge-translate-dialog';
import { ActivityInlineNotice, errorNotice, type ActivityNotice } from '@/components/activity-inline-notice';
import { useDevTools } from '@/lib/dev-tools-context';
import { ActivityAskInline } from '@/components/activity-ask-inline';

const NO_FIELDS: KnowHowFields = { title: '', trigger: '', steps: [] };
import { GetKnowledgeEntries, GetKnowHowMatch, ListKnowHow, usePagePrivacyWithdrawal, usePageQueryRefresh } from '@/lib/page-queries';
import { markPageContent } from '@/lib/page-switch-timing';
import { pageQueryCache } from '@/lib/query-cache';
import { cn } from '@/lib/utils';
import { PlaybookReader } from '@/components/sops/PlaybookReader';
import { PlaybookCards, type PlaybookRunState } from '@/components/playbooks/PlaybookCards';
import { PlaybookPageActions } from '@/components/playbooks/PlaybookPageActions';
import { listConfirmedPlaybooks } from '@/components/playbooks/playbook-lists';
import { usePlaybookLibrary } from '@/components/playbooks/usePlaybookLibrary';
import { GenerateSOPFromDescription, RefreshDerivedView, RunPlaybookNow, type SopEntry, type SopRunResult } from '@/lib/wails-shim-app';
import {
    KNOW_HOW_KINDS,
    candidateDatesLabel,
    candidateDurationLabel,
    dateTimeInWords,
    dayInWords,
    groupByTime,
    knowHowKind,
    matchWhen,
    matchesKeyword,
    originSentence,
    reuseLabel,
    applyKnowHowFields,
    knowHowFields,
    playbookRequest,
    type KnowHowFields,
    savedThisWeek,
    savedTimestamp,
    type KnowHowFilter,
    type TimeGroupKey,
} from '@/lib/know-how';
import { groupIdeas, ideaHead, uniqueBy, type IdeaGroup } from '@/lib/idea-copies';
import { useIdeaMeanings } from '@/lib/idea-meanings';
import { EventsOn } from '../../wailsjs/runtime/runtime';
import {
    DismissKnowHowMatch,
    GetKnowHowCandidate,
    ConfirmKnowHowCandidate,
    DeclineKnowHowCandidate,
    UndoDeclineKnowHowCandidate,
    UndoConfirmKnowHowCandidate,
    SaveKnowHowCandidate,
    DeleteKnowledgeEntry,
    SaveKnowledgeEntry,
    ListKnowledgeVersions,
    GetKnowledgeVersion,
    TranslateKnowledgeEntryToCurrentLanguage,
    TranslateKnowledgeVersionToCurrentLanguage,
    StartTranslateAllCurrentKnowledgeEntries,
} from '../../wailsjs/go/main/App';
import type { main } from '../../wailsjs/go/models';

interface KnowledgePageProps {
    onOpenActivitySession?: (sessionId: string) => void;
    /** When set, the page selects this entry on mount. Cleared by parent after consumption. */
    initialSelectedId?: string | null;
    /** Puts a prepared request in the main chat box. Nothing is sent. */
    /** The dialog's Escape asks the page first: true when the page stepped
     *  back (history, edit, the open entry), false to let the dialog close. */
    escapeRef?: MutableRefObject<(() => boolean) | null>;
    /** False while the dialog is closed (the content is retained after the
     *  first open): a reopen re-reads whatever outlived its TTL. */
    active?: boolean;
    /** Starts a playbook's run in chat. The card's Run now calls it with the run,
     *  as the Library playbooks list's onStartSop did. Omit it and Run now is hidden. */
    onRunPlaybook?: (result: SopRunResult) => Promise<void>;
    /** Opens this playbook's detail here, e.g. from a notification or Search. */
    openPlaybookId?: string | null;
    /** Incremented with each request to open openPlaybookId. */
    openPlaybookSignal?: number;
    /** Called once the requested playbook is open, so it is not opened again. */
    onOpenPlaybookHandled?: () => void;
    /** Incremented by the app header Back button while a playbook is open. */
    backSignal?: number;
    /** Lets the app header mirror whether a playbook's detail is showing. */
    onReaderOpenChange?: (open: boolean) => void;
}

const MAX_CARD_APPS = 3;
/** The proposal's answer: its line keeps Undo until the person leaves the page. */
type CandidateResult = {
    kind: 'confirmed' | 'declined';
    id: string;
};

/** The cached reads a knowledge write changes; a reload must re-read them
 *  from the backend, never re-paint the pre-write cache (the backend events
 *  cover save/delete but land after the write's own reload). */
const KNOWLEDGE_READS = ['ListKnowHow', 'GetKnowledgeEntries'];
function invalidateKnowledgeReads() {
    pageQueryCache.invalidate(KNOWLEDGE_READS);
}

type KnowledgeVersionListItem = {
    timestampMs: number;
    sizeBytes: number;
};

type SelectedKnowledgeVersion = number | 'current' | null;

// The Go GlobalTranslationBatchSummary travels only in this event, so Wails
// generates no model for it.
type GlobalTranslationDoneEvent = {
    scope: string;
    summary?: { targetLanguage: string; total: number; translated: number; failed: number };
    error?: string;
};

type LoadState = 'loading' | 'ready' | 'failed';

/**
 * How you do things (RC 2 D5): "You've hit this before", cards by kind with
 * reuse counts, and a detail with a prepared request for the main chat.
 * Everything the How you do things page had stays reachable from the page More menu,
 * the entry's More menu or Technical details.
 */
export function KnowledgePage({
    onOpenActivitySession,
    initialSelectedId,
    escapeRef,
    active = true,
    onRunPlaybook,
    openPlaybookId = null,
    openPlaybookSignal = 0,
    onOpenPlaybookHandled,
    backSignal = 0,
    onReaderOpenChange,
}: KnowledgePageProps) {
    const { t, i18n } = useTranslation('knowledge');
    const locale = i18n.resolvedLanguage || i18n.language || 'en';
    // A revisit seeds from the page query cache and re-reads in the
    // background; only a cold cache shows the skeleton.
    const [cards, setCards] = useState<main.KnowHowCard[]>(() => pageQueryCache.peek<main.KnowHowCard[]>('ListKnowHow') ?? []);
    const [entries, setEntries] = useState<Map<string, main.KnowledgeEntry>>(
        () => new Map((pageQueryCache.peek<main.KnowledgeEntry[]>('GetKnowledgeEntries') ?? []).map((entry) => [entry.id, entry])),
    );
    const [loadState, setLoadState] = useState<LoadState>(() => (pageQueryCache.peek('ListKnowHow') === undefined ? 'loading' : 'ready'));
    const [match, setMatch] = useState<main.KnowHowMatch | null>(() => pageQueryCache.peek<main.KnowHowMatch>('GetKnowHowMatch') ?? null);
    const [dismissing, setDismissing] = useState(false);
    const [dismissFailed, setDismissFailed] = useState<ActivityNotice | null>(null);
    const [kind, setKind] = useState<KnowHowFilter>('all');
    const [keyword, setKeyword] = useState('');
    const [timeGrouped, setTimeGrouped] = useState(false);
    const [selectedId, setSelectedId] = useState<string | null>(initialSelectedId ?? null);
    // Confirmed playbooks ("Isoform can do these for you") and the one open in
    // the main column. Drafts never reach this page.
    const library = usePlaybookLibrary();
    const [selectedPlaybookId, setSelectedPlaybookId] = useState<string | null>(null);
    const [handledPlaybookSignal, setHandledPlaybookSignal] = useState(0);
    const [handledBackSignal, setHandledBackSignal] = useState(backSignal);
    const [runState, setRunState] = useState<Record<string, PlaybookRunState>>({});
    const [deleteTarget, setDeleteTarget] = useState<{ id: string; title: string; copies: string[] } | null>(null);
    const [historyMode, setHistoryMode] = useState(false);
    const [historyVersions, setHistoryVersions] = useState<KnowledgeVersionListItem[]>([]);
    const [historySelectedTs, setHistorySelectedTs] = useState<SelectedKnowledgeVersion>(null);
    const [historyVersionContent, setHistoryVersionContent] = useState('');
    const [historyLoadingList, setHistoryLoadingList] = useState(false);
    const [historyLoadingContent, setHistoryLoadingContent] = useState(false);
    const [historyError, setHistoryError] = useState<string | null>(null);
    const [historyRetry, setHistoryRetry] = useState(0);
    const [actionError, setActionError] = useState<string | null>(null);
    const [translating, setTranslating] = useState(false);
    const [editing, setEditing] = useState(false);
    const [editFields, setEditFields] = useState<KnowHowFields>(NO_FIELDS);
    const [detailNotice, setDetailNotice] = useState<ActivityNotice | null>(null);
    const [translateAllNotice, setTranslateAllNotice] = useState<ActivityNotice | null>(null);
    const [saving, setSaving] = useState(false);
    const [globalTranslateOpen, setGlobalTranslateOpen] = useState(false);
    const [globalTranslateTarget, setGlobalTranslateTarget] = useState<GlobalTranslateTarget>('follow-ui');
    const [globalTranslateCustomLanguage, setGlobalTranslateCustomLanguage] = useState('');
    const [globalTranslating, setGlobalTranslating] = useState(false);
    const [globalTranslateError, setGlobalTranslateError] = useState<string | null>(null);
    // The confirm moment: one observed-repetition proposal, its draft review,
    // and the result line with its Undo window.
    const [candidate, setCandidate] = useState<main.KnowHowCandidate | null>(null);
    const [candidateResult, setCandidateResult] = useState<CandidateResult | null>(null);
    const [candidateBusy, setCandidateBusy] = useState(false);
    const [candidateFailed, setCandidateFailed] = useState<ActivityNotice | null>(null);
    // Try again repeats the action that failed (W6), through the latest handlers.
    const candidateActions = useRef<{ decline: () => void; confirm: () => void; undo: () => void; save: () => void; load: (opts?: { afterUndo?: boolean }) => void }>({ decline() {}, confirm() {}, undo() {}, save() {}, load() {} });
    const candidateView = useRef({ reviewing: false, editing: false });
    const [reviewingCandidate, setReviewingCandidate] = useState(false);
    const [candidateEditing, setCandidateEditing] = useState(false);
    const [candidateEditTitle, setCandidateEditTitle] = useState('');
    const [candidateEditTrigger, setCandidateEditTrigger] = useState('');
    const [candidateEditSteps, setCandidateEditSteps] = useState('');
    // The how-to a confirmation just saved lands at the top, with the wash.
    const [landedId, setLandedId] = useState<string | null>(null);
    const rootRef = useRef<HTMLDivElement>(null);
    // Every privacy transition bumps the epoch: pending candidate reads and
    // edits complete against a retired epoch and are dropped instead of
    // repopulating withdrawn state.
    const candidateEpoch = useRef(0);
    // Whether the first paint had cached cards; a cache-seeded mount has no
    // network read to mark the page's content (page-switch-timing maps the
    // ListKnowHow binding), so the mount effect below marks it instead.
    const cacheSeeded = useRef(pageQueryCache.peek('ListKnowHow') !== undefined);

    // Cards come from ListKnowHow; the raw markdown (copy, edit, history,
    // the full entry) from GetKnowledgeEntries. A later failed re-read keeps
    // what is on screen and says nothing. Reads apply in start order, so a
    // slow early response never replaces a newer one (or a withdrawn page).
    const entriesSeq = useRef(0);
    const loadEntries = useCallback(async () => {
        const seq = ++entriesSeq.current;
        try {
            // The cached GetKnowledgeEntries rides on the shim's export * of
            // the generated bindings, which carries no type for it yet.
            const [cardList, entryList] = await Promise.all([ListKnowHow(), GetKnowledgeEntries() as Promise<main.KnowledgeEntry[]>]);
            if (seq !== entriesSeq.current) return null;
            setCards(cardList ?? []);
            setEntries(new Map((entryList ?? []).map((entry) => [entry.id, entry])));
            setLoadState('ready');
            return entryList ?? [];
        } catch {
            if (seq === entriesSeq.current) setLoadState((state) => (state === 'ready' ? state : 'failed'));
            return null;
        }
    }, []);

    const matchSeq = useRef(0);
    const dismissRef = useRef<() => void>(() => {});
    const loadMatch = useCallback(() => {
        const seq = ++matchSeq.current;
        GetKnowHowMatch()
            .then((result) => {
                if (seq !== matchSeq.current) return;
                setMatch(result ?? null);
            })
            // A failed re-read keeps the banner on screen (W20).
            .catch(() => {});
    }, []);

    const loadCandidate = useCallback((opts?: { afterUndo?: boolean }) => {
        const epoch = candidateEpoch.current;
        GetKnowHowCandidate()
            .then((result) => {
                if (candidateEpoch.current !== epoch) return;
                setCandidate(result ?? null);
                if (opts?.afterUndo) {
                    setCandidateFailed(null);
                    setReviewingCandidate(!!result);
                }
                if (!result) {
                    setReviewingCandidate(false);
                    setCandidateEditing(false);
                }
            })
            .catch(() => {
                // A failed re-read keeps what's on screen (W19); while it's open, say so.
                if (candidateEpoch.current !== epoch) return;
                const view = candidateView.current;
                if (opts?.afterUndo || view.reviewing || view.editing) {
                    setCandidateFailed(errorNotice(t('candidate.loadFailed'), () => candidateActions.current.load(opts)));
                }
            });
    }, [t]);

    // The proposal projects observed sessions, so a privacy transition (Closed
    // Eyes, a Forget, a newly protected app, sign-out) withdraws every rendered
    // and retained copy of it at once — banner, draft detail, edit fields and
    // the Undo line — whatever a late Get/Save/Undo completion still says.
    const withdrawCandidate = useCallback(() => {
        candidateEpoch.current += 1;
        setCandidate(null);
        setCandidateResult(null);
        setLandedId(null);
        setCandidateBusy(false);
        setCandidateFailed(null);
        setReviewingCandidate(false);
        setCandidateEditing(false);
        setCandidateEditTitle('');
        setCandidateEditTrigger('');
        setCandidateEditSteps('');
    }, []);

    const refreshKnowledge = useCallback(() => {
        void loadEntries();
        loadMatch();
    }, [loadEntries, loadMatch]);
    const knowledgeRefreshTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
    const scheduleKnowledgeRefresh = useCallback(() => {
        if (knowledgeRefreshTimer.current !== null) return;
        knowledgeRefreshTimer.current = setTimeout(() => {
            knowledgeRefreshTimer.current = null;
            refreshKnowledge();
        }, 0);
    }, [refreshKnowledge]);
    useEffect(() => () => {
        if (knowledgeRefreshTimer.current !== null) clearTimeout(knowledgeRefreshTimer.current);
    }, []);

    const refresh = useCallback(() => {
        refreshKnowledge();
        loadCandidate();
    }, [refreshKnowledge, loadCandidate]);

    // Candidate lifecycle events: proposed, confirmed, declined, withdrawn.
    useEffect(() => {
        const cleanup = EventsOn('knowhow:candidate-updated', (event?: { action?: string }) => {
            if (event?.action === 'withdrawn') withdrawCandidate();
            loadCandidate();
        });
        return cleanup;
    }, [loadCandidate, withdrawCandidate]);

    // Privacy transitions withdraw the proposal synchronously; the re-read then
    // restores only what the gates still allow (e.g. after Closed Eyes resume).
    useEffect(() => {
        const withdrawAndReread = () => {
            withdrawCandidate();
            loadCandidate();
        };
        const cleanups = [
            EventsOn('closed-eyes:changed', (paused: boolean) => {
                if (paused) {
                    withdrawCandidate();
                    return;
                }
                loadCandidate();
            }),
            EventsOn('memory:updated', (event?: { reason?: string }) => {
                // Confirm's own write and index notification do not revoke
                // its pending response or Undo. Untagged events fail closed.
                if (event?.reason === 'knowhow-confirm' || event?.reason === 'index') {
                    loadCandidate();
                    return;
                }
                withdrawAndReread();
            }),
            EventsOn('sensitive-apps:updated', withdrawAndReread),
            // Sign-out and account removal withdraw through the shared
            // identity fence (lib/privacy-reset.ts): a signed-in account gone
            // or switched off, an unreadable payload, or an unknown baseline
            // all fail closed; ordinary quota refreshes with the same
            // identities do not touch the proposal.
            onAccountIdentityWithdrawal(withdrawAndReread),
        ];
        return () => {
            for (const cleanup of cleanups) cleanup();
        };
    }, [loadCandidate, withdrawCandidate]);


    // Knowledge events invalidate the whole read family. Its card-list
    // publication is the single signal for the combined loader.
    usePageQueryRefresh(['ListKnowHow'], scheduleKnowledgeRefresh);

    useEffect(() => {
        refresh();
        if (cacheSeeded.current) markPageContent('know-how');
    }, [refresh]);

    // Reopened after a quiet stretch: re-read whatever outlived its TTL.
    const wasActive = useRef(active);
    useEffect(() => {
        if (active && !wasActive.current) refresh();
        wasActive.current = active;
    }, [active, refresh]);

    useEffect(() => {
        const cleanup = EventsOn('global-translation:done', (event: GlobalTranslationDoneEvent) => {
            if (event.scope !== 'knowledge') return;

            setGlobalTranslating(false);
            invalidateKnowledgeReads();
            void loadEntries();

            // Nothing on success; a line only when some fail (W9).
            const failed = event.error ? event.summary?.total || event.summary?.failed || 0 : event.summary?.failed ?? 0;
            if (!event.error && failed <= 0) return;
            setTranslateAllNotice(errorNotice(t('translate.global.someFailed', { failed: failed || '' }).trim(), () => {
                setTranslateAllNotice(null);
                setGlobalTranslateError(null);
                setGlobalTranslateOpen(true);
            }));
        });
        return cleanup;
    }, [loadEntries, t]);

    // A notification's "View" while the page is open switches the entry.
    useEffect(() => {
        if (initialSelectedId) {
            setSelectedId(initialSelectedId);
            setSelectedPlaybookId(null);
            setReviewingCandidate(false);
            setCandidateEditing(false);
        }
    }, [initialSelectedId]);

    // One card per idea (W40): copies whose titles match share a card, and the
    // groups cover every copy, so an action reaches all of them.
    const meanings = useIdeaMeanings(cards, cardTitle, cardSavedAt, IDEA_MEANING_THRESHOLD_KNOW_HOW, cardMeaningText);
    const ideaGroups = useMemo(() => groupIdeas(cards, cardTitle, cardSavedAt, meanings), [cards, meanings]);
    const ideaGroupOfId = useMemo(() => {
        const byId = new Map<string, IdeaGroup<main.KnowHowCard>>();
        for (const group of ideaGroups) {
            for (const card of group.copies) {
                byId.set(card.id, group);
                for (const mergedId of card.mergedIds ?? []) byId.set(mergedId, group);
            }
        }
        return byId;
    }, [ideaGroups]);
    /** Every stored entry behind the idea a card or any of its copies belongs to. */
    const idsOfIdea = useCallback((id: string) => {
        const group = ideaGroupOfId.get(id);
        if (!group) return [id];
        return group.copies.flatMap((card) => [card.id, ...(card.mergedIds ?? [])]);
    }, [ideaGroupOfId]);
    const ideaHeads = useMemo(() => ideaGroups.map((group) => group.copies[0]), [ideaGroups]);
    const copyCounts = useMemo(() => new Map(ideaGroups.map((group) => [
        group.copies[0].id,
        group.copies.reduce((sum, card) => sum + 1 + (card.mergedIds?.length ?? 0), 0),
    ])), [ideaGroups]);
    const visible = useMemo(() => {
        const passes = (card: main.KnowHowCard) =>
            (kind === 'all' || knowHowKind(card.kind) === kind)
            && matchesKeyword(card, entries.get(card.id)?.content ?? '', keyword);
        // A group shows while any copy passes the filters; its newest passing copy heads the card.
        const shown = ideaGroups.flatMap((group) => {
            const head = ideaHead(group, passes);
            return head ? [head] : [];
        });
        const landed = shown.findIndex((card) => card.id === landedId || (!!landedId && ideaGroupOfId.get(landedId)?.copies[0].id === card.id));
        return landed > 0 ? [shown[landed], ...shown.slice(0, landed), ...shown.slice(landed + 1)] : shown;
    }, [entries, ideaGroupOfId, ideaGroups, keyword, kind, landedId]);
    // The card the detail shows is the newest copy of the idea; its moments
    // come from every copy.
    const selectedGroup = useMemo(
        () => (selectedId ? ideaGroupOfId.get(selectedId) ?? null : null),
        [ideaGroupOfId, selectedId],
    );
    const selected = useMemo(() => selectedGroup?.copies[0] ?? null, [selectedGroup]);
    const selectedCopies = selectedGroup?.copies ?? [];
    const selectedEntry = selected ? entries.get(selected.id) ?? null : null;
    const playbookItems = useMemo(
        () => listConfirmedPlaybooks(library.sops, library.playbooks),
        [library.sops, library.playbooks],
    );
    // The search box filters playbooks by name and description too; kinds do not apply to them.
    const visiblePlaybooks = useMemo(() => {
        const query = keyword.trim().toLowerCase();
        if (!query) return playbookItems;
        return playbookItems.filter(({ entry }) =>
            entry.title.toLowerCase().includes(query) || entry.description.toLowerCase().includes(query));
    }, [keyword, playbookItems]);
    const openPlaybook = useMemo(
        () => playbookItems.find(({ entry }) => entry.id === selectedPlaybookId) ?? null,
        [playbookItems, selectedPlaybookId],
    );
    const playbookOpen = openPlaybook !== null;

    const handleExitHistory = useCallback(() => {
        setHistoryMode(false);
        setHistoryVersions([]);
        setHistorySelectedTs(null);
        setHistoryVersionContent('');
        setHistoryError(null);
        setActionError(null);
        setHistoryLoadingList(false);
        setHistoryLoadingContent(false);
    }, []);

    const handleCancelEdit = useCallback(() => {
        setEditing(false);
        setEditFields(NO_FIELDS);
    }, []);

    // A privacy transition (Forget, a protection change, Closed Eyes,
    // background listening, a permission change, sign-out) withdraws the
    // pre-transition text synchronously — cached, rendered and remembered —
    // and supersedes the old reads; the cache sweep's refresh re-reads what
    // is still permitted. Ordinary invalidations keep the stale paint.
    const withdraw = useCallback(() => {
        entriesSeq.current++;
        matchSeq.current++;
        setCards([]);
        setEntries(new Map());
        setLoadState('loading');
        setMatch(null);
        setSelectedId(null);
        setKeyword('');
        setDeleteTarget(null);
        setActionError(null);
        handleExitHistory();
        handleCancelEdit();
    }, [handleExitHistory, handleCancelEdit]);
    usePagePrivacyWithdrawal(withdraw);

    const handleSelect = useCallback((id: string | null) => {
        setActionError(null);
        setSelectedId(id);
        setSelectedPlaybookId(null);
        setReviewingCandidate(false);
        setCandidateEditing(false);
    }, []);

    const openPlaybookDetail = useCallback((id: string) => {
        handleSelect(null);
        setSelectedPlaybookId(id);
    }, [handleSelect]);

    // A notification's or Search's request to open a playbook here.
    useEffect(() => {
        if (!openPlaybookId || openPlaybookSignal === handledPlaybookSignal) return;
        setHandledPlaybookSignal(openPlaybookSignal);
        openPlaybookDetail(openPlaybookId);
        onOpenPlaybookHandled?.();
    }, [handledPlaybookSignal, onOpenPlaybookHandled, openPlaybookDetail, openPlaybookId, openPlaybookSignal]);

    // The app header's Back closes the playbook detail, as it did on the Library page.
    useEffect(() => {
        if (handledBackSignal === backSignal) return;
        setHandledBackSignal(backSignal);
        if (playbookOpen) setSelectedPlaybookId(null);
    }, [backSignal, handledBackSignal, playbookOpen]);

    useEffect(() => {
        onReaderOpenChange?.(playbookOpen);
    }, [onReaderOpenChange, playbookOpen]);

    // Card's Run now runs in place, like the playbook's own (W11): the button
    // becomes the running line, a failure stays on the card with Try again.
    const runPlaybook = useCallback((entry: SopEntry) => {
        if (runState[entry.id] === 'running') return;
        setRunState((old) => ({ ...old, [entry.id]: 'running' }));
        Promise.resolve().then(() => RunPlaybookNow(entry.id, entry.sites ?? [])).catch(() => {
            setRunState((old) => ({ ...old, [entry.id]: errorNotice(t('playbooks.runFailed'), () => runPlaybookRef.current(entry)) }));
        });
    }, [runState, t]);
    const runPlaybookRef = useRef(runPlaybook);
    runPlaybookRef.current = runPlaybook;

    const handleShowFix = useCallback(() => {
        if (!match) return;
        setKind('all');
        setKeyword('');
        handleSelect(match.entryId);
        requestAnimationFrame(() => {
            Array.from(document.querySelectorAll<HTMLElement>('[data-know-how-id]'))
                .find((element) => element.dataset.knowHowId === match.entryId)
                ?.scrollIntoView?.({ block: 'nearest' });
        });
    }, [handleSelect, match]);

    const handleDismissMatch = useCallback(() => {
        if (!match || dismissing) return;
        setDismissing(true);
        setDismissFailed(null);
        DismissKnowHowMatch(match.entryId, match.signature)
            .then(() => {
                pageQueryCache.invalidate(['GetKnowHowMatch']);
                setMatch(null);
            })
            .catch(() => setDismissFailed(errorNotice(t('match.dismissFailed'), () => dismissRef.current())))
            .finally(() => setDismissing(false));
    }, [dismissing, match, t]);

    const handleReviewCandidate = useCallback(() => {
        setCandidateFailed(null);
        setSelectedId(null);
        handleExitHistory();
        handleCancelEdit();
        setReviewingCandidate(true);
    }, [handleCancelEdit, handleExitHistory]);

    const handleDeclineCandidate = useCallback(() => {
        if (!candidate || candidateBusy) return;
        setCandidateBusy(true);
        setCandidateFailed(null);
        const epoch = candidateEpoch.current;
        const id = candidate.id;
        DeclineKnowHowCandidate(id)
            .then(() => {
                if (candidateEpoch.current !== epoch) return;
                setCandidate(null);
                setReviewingCandidate(false);
                setCandidateEditing(false);
                setCandidateResult({ kind: 'declined', id });
            })
            .catch(() => {
                if (candidateEpoch.current !== epoch) return;
                setCandidateFailed(errorNotice(t('candidate.actionFailed'), () => candidateActions.current.decline()));
            })
            .finally(() => {
                if (candidateEpoch.current !== epoch) return;
                setCandidateBusy(false);
            });
    }, [candidate, candidateBusy, t]);

    const handleConfirmCandidate = useCallback(() => {
        if (!candidate || candidateBusy) return;
        setCandidateBusy(true);
        setCandidateFailed(null);
        const id = candidate.id;
        const epoch = candidateEpoch.current;
        ConfirmKnowHowCandidate(id)
            .then((result) => {
                if (candidateEpoch.current !== epoch) return;
                setCandidate(null);
                setReviewingCandidate(false);
                setCandidateEditing(false);
                setCandidateResult({ kind: 'confirmed', id });
                invalidateKnowledgeReads();
                void loadEntries();
                // The new card shows selected, past any filter.
                setKind('all');
                setKeyword('');
                if (result?.knowledgeId) {
                    setSelectedId(result.knowledgeId);
                    setLandedId(result.knowledgeId);
                }
            })
            .catch(() => {
                if (candidateEpoch.current !== epoch) return;
                setCandidateFailed(errorNotice(t('candidate.actionFailed'), () => candidateActions.current.confirm()));
            })
            .finally(() => {
                if (candidateEpoch.current !== epoch) return;
                setCandidateBusy(false);
            });
    }, [candidate, candidateBusy, loadEntries, t]);

    // Undo returns to the open proposal in its review state, for either outcome.
    const handleUndoCandidateResult = useCallback(() => {
        if (!candidateResult || candidateBusy) return;
        setCandidateBusy(true);
        setCandidateFailed(null);
        const epoch = candidateEpoch.current;
        const undo = candidateResult.kind === 'confirmed' ? UndoConfirmKnowHowCandidate : UndoDeclineKnowHowCandidate;
        undo(candidateResult.id)
            .then(() => {
                if (candidateEpoch.current !== epoch) return;
                setCandidateResult(null);
                setLandedId(null);
                setSelectedId(null);
                if (candidateResult.kind === 'confirmed') invalidateKnowledgeReads();
                void loadEntries();
                loadCandidate({ afterUndo: true });
            })
            .catch(() => {
                if (candidateEpoch.current !== epoch) return;
                setCandidateFailed(errorNotice(t('candidate.undoFailed'), () => candidateActions.current.undo()));
            })
            .finally(() => {
                if (candidateEpoch.current !== epoch) return;
                setCandidateBusy(false);
            });
    }, [candidateBusy, candidateResult, loadCandidate, loadEntries, t]);

    const handleEditCandidate = useCallback(() => {
        if (!candidate) return;
        setCandidateFailed(null);
        setCandidateEditTitle(candidate.title);
        setCandidateEditTrigger(candidate.trigger ?? '');
        setCandidateEditSteps((candidate.steps ?? []).join('\n'));
        setCandidateEditing(true);
    }, [candidate]);

    const handleSaveCandidateEdit = useCallback(() => {
        if (!candidate || candidateBusy) return;
        setCandidateBusy(true);
        setCandidateFailed(null);
        const epoch = candidateEpoch.current;
        SaveKnowHowCandidate(candidate.id, candidateEditTitle, candidateEditTrigger, candidateEditSteps.split('\n'))
            .then((saved) => {
                if (candidateEpoch.current !== epoch) return;
                if (saved) setCandidate(saved);
                setCandidateEditing(false);
            })
            .catch(() => {
                if (candidateEpoch.current !== epoch) return;
                setCandidateFailed(errorNotice(t('candidate.actionFailed'), () => candidateActions.current.save()));
            })
            .finally(() => {
                if (candidateEpoch.current !== epoch) return;
                setCandidateBusy(false);
            });
    }, [candidate, candidateBusy, candidateEditSteps, candidateEditTitle, candidateEditTrigger, t]);

    candidateActions.current = {
        decline: handleDeclineCandidate,
        confirm: handleConfirmCandidate,
        undo: handleUndoCandidateResult,
        save: handleSaveCandidateEdit,
        load: loadCandidate,
    };
    candidateView.current = { reviewing: reviewingCandidate, editing: candidateEditing };
    dismissRef.current = handleDismissMatch;

    // The saved line and its Undo clear when you leave the page (W17).
    useEffect(() => {
        if (active) return;
        setCandidateResult(null);
        setLandedId(null);
    }, [active]);

    const handleDelete = useCallback(
        (id: string) => {
            // A card for an idea deletes every copy of it (W40).
            const ids = [...new Set(idsOfIdea(id))];
            Promise.all(ids.map((entryId) => DeleteKnowledgeEntry(entryId)))
                .then(() => {
                    invalidateKnowledgeReads();
                    if (selectedId === id) {
                        setSelectedId(null);
                        handleExitHistory();
                        handleCancelEdit();
                        // The entry's More menu unmounts with the detail.
                        rootRef.current?.focus();
                    }
                    if (match?.entryId === id) setMatch(null);
                    void loadEntries();
                })
                .catch(() => setDetailNotice(errorNotice(t('detail.deleteFailed'), () => handleDelete(id))));
        },
        [handleCancelEdit, handleExitHistory, idsOfIdea, loadEntries, match?.entryId, selectedId, t],
    );

    const handleCopyMarkdown = useCallback(() => {
        if (!selectedEntry) return;
        navigator.clipboard
            .writeText(selectedEntry.content)
            .then(() => setDetailNotice(null))
            .catch(() => setDetailNotice(errorNotice(t('more.copyFailed'), handleCopyMarkdown)));
    }, [selectedEntry, t]);

    const handleEdit = useCallback(() => {
        if (!selectedEntry) return;
        setActionError(null);
        setEditFields(knowHowFields(selectedEntry.content, {
            title: selected?.title ?? '',
            trigger: selected?.trigger ?? '',
            steps: selected?.points ?? [],
        }));
        setEditing(true);
    }, [selected, selectedEntry]);

    const handleSave = useCallback(() => {
        if (!selected || saving) return;
        setActionError(null);
        setSaving(true);
        SaveKnowledgeEntry(selected.id, applyKnowHowFields(selectedEntry?.content ?? '', editFields))
            .then((saved) => {
                invalidateKnowledgeReads();
                setEditing(false);
                setEditFields(NO_FIELDS);
                setSelectedId(saved.id);
                void loadEntries();
            })
            .catch(() => setActionError(t('edit.saveFailed')))
            .finally(() => setSaving(false));
    }, [editFields, loadEntries, saving, selected, selectedEntry, t]);

    // Radix asks the dialog only while the page is the top layer, so an open
    // menu, the delete confirmation or a stacked Activity dialog keeps Escape.
    const handleEscape = useCallback((): boolean => {
        if (playbookOpen) {
            setSelectedPlaybookId(null);
            return true;
        }
        if (candidateEditing) {
            setCandidateEditing(false);
            return true;
        }
        if (reviewingCandidate) {
            setReviewingCandidate(false);
            return true;
        }
        if (historyMode) {
            handleExitHistory();
            return true;
        }
        if (editing) {
            handleCancelEdit();
            return true;
        }
        if (selectedId) {
            setSelectedId(null);
            return true;
        }
        return false;
    }, [candidateEditing, editing, handleCancelEdit, handleExitHistory, historyMode, playbookOpen, reviewingCandidate, selectedId]);

    useEffect(() => {
        if (!escapeRef) return;
        escapeRef.current = handleEscape;
        return () => {
            if (escapeRef.current === handleEscape) escapeRef.current = null;
        };
    }, [escapeRef, handleEscape]);

    const finishTranslation = useCallback((saved: main.KnowledgeEntry) => {
        invalidateKnowledgeReads();
        void loadEntries();
        setSelectedId(saved.id);
        handleExitHistory();
    }, [handleExitHistory, loadEntries]);

    const handleTranslateCurrent = useCallback(() => {
        if (!selected || translating) return;
        setActionError(null);
        setTranslating(true);
        TranslateKnowledgeEntryToCurrentLanguage(selected.id)
            .then(finishTranslation)
            .catch((err: unknown) => {
                setActionError(t('detail.translateFailed', { error: err instanceof Error ? err.message : String(err) }));
            })
            .finally(() => setTranslating(false));
    }, [finishTranslation, selected, t, translating]);

    const handleTranslateHistorySelection = useCallback(() => {
        if (!selected || translating || historySelectedTs === null) return;
        if (historySelectedTs === 'current') {
            handleTranslateCurrent();
            return;
        }
        setActionError(null);
        setTranslating(true);
        TranslateKnowledgeVersionToCurrentLanguage(selected.id, historySelectedTs)
            .then(finishTranslation)
            .catch((err: unknown) => {
                setActionError(t('detail.translateFailed', { error: err instanceof Error ? err.message : String(err) }));
            })
            .finally(() => setTranslating(false));
    }, [finishTranslation, handleTranslateCurrent, historySelectedTs, selected, t, translating]);

    const handleGlobalTranslate = useCallback(async () => {
        if (globalTranslating || cards.length === 0) return;
        if (globalTranslateTarget === 'other' && globalTranslateCustomLanguage.trim() === '') {
            setGlobalTranslateError(t('translate.global.customRequired'));
            return;
        }

        setGlobalTranslateError(null);
        setGlobalTranslating(true);

        try {
            const start = await StartTranslateAllCurrentKnowledgeEntries({
                target: globalTranslateTarget,
                language: globalTranslateCustomLanguage.trim(),
            });
            void start;
            setGlobalTranslateOpen(false);
            setTranslateAllNotice(null);
        } catch (err: unknown) {
            setGlobalTranslating(false);
            setGlobalTranslateError(t('translate.global.startFailed', { error: err instanceof Error ? err.message : String(err) }));
        }
    }, [cards.length, globalTranslateCustomLanguage, globalTranslating, globalTranslateTarget, t]);

    useEffect(() => {
        if (!historyMode || !selected) return;
        let cancelled = false;
        setHistoryLoadingList(true);
        setHistoryError(null);
        ListKnowledgeVersions(selected.id)
            .then((result: KnowledgeVersionListItem[] | null) => {
                if (cancelled) return;
                const list = result ?? [];
                setHistoryVersions(list);
                setHistorySelectedTs(list.length > 0 ? list[0].timestampMs : 'current');
            })
            .catch(() => {
                if (!cancelled) setHistoryError(t('history.loadFailed'));
            })
            .finally(() => {
                if (!cancelled) setHistoryLoadingList(false);
            });
        return () => {
            cancelled = true;
        };
    }, [historyMode, historyRetry, selected?.id, t]);

    useEffect(() => {
        if (!historyMode || !selectedEntry || historySelectedTs === null) {
            setHistoryVersionContent('');
            setHistoryLoadingContent(false);
            return;
        }
        if (historySelectedTs === 'current') {
            setHistoryVersionContent(selectedEntry.content);
            setHistoryLoadingContent(false);
            setHistoryError(null);
            return;
        }
        let cancelled = false;
        setHistoryLoadingContent(true);
        setHistoryError(null);
        GetKnowledgeVersion(selectedEntry.id, historySelectedTs)
            .then((content: string) => {
                if (!cancelled) setHistoryVersionContent(content);
            })
            .catch(() => {
                if (cancelled) return;
                setHistoryVersionContent('');
                setHistoryError(t('history.snapshotFailed'));
            })
            .finally(() => {
                if (!cancelled) setHistoryLoadingContent(false);
            });
        return () => {
            cancelled = true;
        };
    }, [historyMode, historyRetry, selectedEntry?.id, selectedEntry?.content, historySelectedTs, t]);

    useEffect(() => {
        if (historyMode) handleExitHistory();
        if (editing) handleCancelEdit();
        setDetailNotice(null);
        // Only exit on entry switch; including historyMode/editing would close
        // them on open.
        // eslint-disable-next-line react-hooks/exhaustive-deps
    }, [selected?.id]);

    const ready = loadState === 'ready';
    const hasCards = ready && cards.length > 0;
    const wideAside = (editing || historyMode) && !!selected;

    let aside: React.ReactNode = null;
    if (candidateEditing && candidate) {
        aside = (
            <KnowHowCandidateEditPanel
                title={candidateEditTitle}
                trigger={candidateEditTrigger}
                steps={candidateEditSteps}
                saving={candidateBusy}
                failed={candidateFailed}
                onTitleChange={setCandidateEditTitle}
                onTriggerChange={setCandidateEditTrigger}
                onStepsChange={setCandidateEditSteps}
                onSave={handleSaveCandidateEdit}
                onCancel={() => setCandidateEditing(false)}
            />
        );
    } else if (reviewingCandidate && candidate) {
        aside = (
            <KnowHowCandidateDetail
                candidate={candidate}
                locale={locale}
                busy={candidateBusy}
                failed={candidateFailed}
                onConfirm={handleConfirmCandidate}
                onEditSteps={handleEditCandidate}
                onNotHowTo={handleDeclineCandidate}
            />
        );
    } else if (hasCards) {
        if (editing && selected) {
            aside = (
                <KnowHowEditPanel
                    fields={editFields}
                    saving={saving}
                    error={actionError}
                    onChange={setEditFields}
                    onSave={handleSave}
                    onCancel={handleCancelEdit}
                />
            );
        } else if (historyMode && selected) {
            aside = (
                <KnowHowHistoryPanel
                    title={selected.title}
                    locale={locale}
                    versions={historyVersions}
                    selectedTs={historySelectedTs}
                    content={historyVersionContent}
                    loadingList={historyLoadingList}
                    loadingContent={historyLoadingContent}
                    notice={historyError
                        ? errorNotice(historyError, () => setHistoryRetry((n) => n + 1))
                        : actionError ? errorNotice(actionError, handleTranslateHistorySelection) : null}
                    translating={translating}
                    onSelect={setHistorySelectedTs}
                    onTranslate={handleTranslateHistorySelection}
                    onExit={handleExitHistory}
                    onOpenActivitySession={onOpenActivitySession}
                />
            );
        } else if (selected) {
            aside = (
                <KnowHowDetail
                    card={selected}
                    copies={selectedCopies}
                    entry={selectedEntry}
                    locale={locale}
                    translating={translating}
                    notice={detailNotice ?? (actionError ? errorNotice(actionError, handleTranslateCurrent) : null)}
                    onEdit={handleEdit}
                    onCopyMarkdown={handleCopyMarkdown}
                    onTranslate={handleTranslateCurrent}
                    onHistory={() => setHistoryMode(true)}
                    onDelete={() => setDeleteTarget({
                        id: selected.id,
                        title: selected.title,
                        // Every other copy of this card goes too, so the list shows them first.
                        copies: selectedCopies.slice(1).map((copy) => copy.title),
                    })}
                    onOpenActivitySession={onOpenActivitySession}
                />
            );
        }
        // No At a glance: until a card is picked the list takes the full width (W39).
    }

    if (openPlaybook) {
        return (
            <div ref={rootRef} className="flex h-full bg-background text-ink outline-none" tabIndex={-1}>
                <div className="flex min-w-0 flex-1 flex-col">
                    <PlaybookReader
                        key={openPlaybook.entry.id}
                        entry={openPlaybook.entry}
                        plan={openPlaybook.plan}
                        channels={library.playbooks?.channels ?? null}
                        defaultChannels={library.playbooks?.defaultChannels ?? ['desktop', 'text']}
                        wide
                        onBack={() => setSelectedPlaybookId(null)}
                        onClose={() => setSelectedPlaybookId(null)}
                        onChanged={library.reload}
                        onStartSop={onRunPlaybook}
                        onOpenPlaybook={setSelectedPlaybookId}
                        schedulesFailed={library.schedulesFailed}
                    />
                </div>
            </div>
        );
    }

    return (
        <div ref={rootRef} className="flex h-full bg-background text-ink outline-none" tabIndex={-1}>
            <ScrollArea className="min-w-0 flex-1">
                <div className="flex flex-col gap-[26px] px-10 pt-9 pb-14">
                    <div className="flex flex-wrap items-start justify-between gap-4">
                        <KnowHowHeader cards={ready ? ideaHeads : null} />
                        <PlaybookPageActions authorStatus={library.authorStatus} onOpenPlaybook={setSelectedPlaybookId} />
                    </div>
                    {/* A failed read keeps what is on screen; only a first read that fails says so (W21). */}
                    {library.loadFailed && selectedPlaybookId && (
                        <ActivityInlineNotice notice={errorNotice(t('playbooks.openFailed'), library.reload)} className="-mt-3 text-[13px]" />
                    )}
                    {library.loadFailed && !library.loaded && !selectedPlaybookId && (
                        <ActivityInlineNotice notice={errorNotice(t('playbooks.loadFailed'), library.reload)} className="-mt-3 text-[13px]" />
                    )}
                    {visiblePlaybooks.length > 0 && (
                        <PlaybookCards
                            items={visiblePlaybooks}
                            locale={locale}
                            onOpen={openPlaybookDetail}
                            onRun={runPlaybook}
                            runState={runState}
                            whenUnknown={library.schedulesFailed}
                        />
                    )}
                    {globalTranslating && (
                        <p role="status" className="-mt-4 text-[12.5px] text-muted">
                            {t('translate.global.inProgress')}
                        </p>
                    )}
                    {!globalTranslating && <ActivityInlineNotice notice={translateAllNotice} className="-mt-4 text-[12.5px]" />}
                    {candidate && (
                        <KnowHowCandidateBanner
                            candidate={candidate}
                            locale={locale}
                            busy={candidateBusy}
                            failed={!reviewingCandidate && !candidateEditing ? candidateFailed : null}
                            onConfirm={handleConfirmCandidate}
                            onReview={handleReviewCandidate}
                            onDecline={handleDeclineCandidate}
                        />
                    )}
                    {!candidate && <ActivityInlineNotice notice={candidateFailed} className="px-1" />}
                    {candidateResult && (
                        <div
                            role="status"
                            className={cn(
                                'answer-line flex flex-wrap items-center gap-1.5 px-1 text-[13px]',
                                candidateResult.kind === 'confirmed' ? 'text-success' : 'text-muted',
                            )}
                        >
                            {candidateResult.kind === 'confirmed' && <Check className="h-3.5 w-3.5 flex-none" aria-hidden="true" />}
                            <span>
                                {candidateResult.kind === 'confirmed' ? t('candidate.confirmed') : t('candidate.declined')}
                            </span>
                            <span aria-hidden="true" className="text-muted">·</span>
                            <button
                                type="button"
                                onClick={handleUndoCandidateResult}
                                disabled={candidateBusy}
                                aria-busy={candidateBusy}
                                className="cursor-pointer rounded-sm border-0 bg-transparent p-0 font-semibold text-accent hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-border-focus disabled:opacity-60"
                            >
                                {t('candidate.undo')}
                            </button>
                        </div>
                    )}
                    {match && hasCards && (
                        <KnowHowMatchBanner
                            match={match}
                            locale={locale}
                            dismissing={dismissing}
                            dismissFailed={dismissFailed}
                            onShow={handleShowFix}
                            onDismiss={handleDismissMatch}
                        />
                    )}
                    {hasCards && (
                        <div className="flex flex-wrap items-center gap-3">
                            <KindFilter value={kind} onChange={setKind} />
                            <div className="ml-auto flex items-center gap-2">
                                <div className="relative">
                                    <SearchIcon className="pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted" />
                                    <input
                                        type="search"
                                        aria-label={t('search.label')}
                                        placeholder={t('search.placeholder')}
                                        className="h-8 w-52 rounded-lg border border-border bg-panel pl-8 pr-3 text-[12.5px] text-ink placeholder:text-muted focus:border-border-focus focus:outline-none"
                                        value={keyword}
                                        onChange={(e) => setKeyword(e.target.value)}
                                    />
                                </div>
                                <PageMoreMenu
                                    grouped={timeGrouped}
                                    canTranslateAll={!globalTranslating}
                                    onToggleGrouped={() => setTimeGrouped((value) => !value)}
                                    onTranslateAll={() => {
                                        setGlobalTranslateError(null);
                                        setGlobalTranslateOpen(true);
                                    }}
                                    onRefresh={() => {
                                        // How you do things is built once a day; this builds it now.
                                        void RefreshDerivedView('know-how')
                                            .then(() => pageQueryCache.invalidate(['ListKnowHow'], false))
                                            .catch(() => undefined);
                                    }}
                                />
                            </div>
                        </div>
                    )}
                    {loadState === 'loading' && <KnowHowSkeleton />}
                    {loadState === 'failed' && (
                        <div role="alert" className="flex flex-col gap-2.5 rounded-xl border border-border bg-panel p-[18px]">
                            <span className="text-[13.5px]">{t('states.loadFailed')}</span>
                            <div>
                                <Button
                                    variant="secondary"
                                    size="sm"
                                    onClick={() => {
                                        setLoadState('loading');
                                        void loadEntries();
                                        loadMatch();
                                    }}
                                >
                                    {t('states.retry')}
                                </Button>
                            </div>
                        </div>
                    )}
                    {ready && cards.length === 0 && <KnowHowEmptyState />}
                    {hasCards && visible.length === 0 && (
                        <p className="text-[13.5px] text-muted">
                            {keyword.trim()
                                ? t('states.noMatch')
                                : t('states.noKind', { kind: kind === 'all' ? '' : t(`kinds.${kind}`) })}
                        </p>
                    )}
                    {hasCards && visible.length > 0 && (
                        timeGrouped ? (
                            groupByTime(visible).map(([group, items]) => (
                                <section key={group} className="flex flex-col gap-2.5" aria-label={t(`groups.${group}`)}>
                                    <h2 className="m-0 text-[11px] font-medium uppercase tracking-[0.08em] text-muted">
                                        {t(`groups.${group as TimeGroupKey}`)}
                                    </h2>
                                    <CardGrid cards={items} copyCounts={copyCounts} selectedId={selected?.id ?? null} landedId={landedId} locale={locale} onSelect={handleSelect} />
                                </section>
                            ))
                        ) : (
                            <CardGrid cards={visible} copyCounts={copyCounts} selectedId={selected?.id ?? null} landedId={landedId} locale={locale} onSelect={handleSelect} />
                        )
                    )}
                </div>
            </ScrollArea>

            {aside && (
                <aside
                    aria-label={t('detail.label')}
                    className={cn(
                        'flex shrink-0 flex-col border-l border-border-subtle bg-background-accent',
                        wideAside ? 'w-[min(640px,60%)]' : 'w-[min(420px,45%)]',
                    )}
                >
                    {aside}
                </aside>
            )}

            <KnowledgeTranslateDialog
                open={globalTranslateOpen}
                onOpenChange={setGlobalTranslateOpen}
                target={globalTranslateTarget}
                customLanguage={globalTranslateCustomLanguage}
                entriesCount={cards.length}
                translating={globalTranslating}
                error={globalTranslateError}
                onTargetChange={setGlobalTranslateTarget}
                onCustomLanguageChange={setGlobalTranslateCustomLanguage}
                onSubmit={handleGlobalTranslate}
            />

            <ConfirmDialog
                open={deleteTarget !== null}
                onOpenChange={(open) => {
                    if (!open) setDeleteTarget(null);
                }}
                title={t('deleteDialog.title')}
                description={(
                    <>
                        <span>{t('deleteDialog.description', { title: deleteTarget?.title ?? '' })}</span>
                        {(deleteTarget?.copies.length ?? 0) > 0 && (
                            <>
                                <span className="mt-3 block">{t('deleteDialog.copies')}</span>
                                <ul className="mt-1.5 list-disc pl-5">
                                    {deleteTarget?.copies.map((copy, index) => <li key={index}>{copy}</li>)}
                                </ul>
                            </>
                        )}
                    </>
                )}
                confirmLabel={t('deleteDialog.confirm')}
                variant="danger"
                onConfirm={() => {
                    if (deleteTarget) handleDelete(deleteTarget.id);
                }}
            />
        </div>
    );
}

/** Where a piece of know-how came from, when the Claude Code or Codex
 *  history read gave it (first-day handoff §5): "From a Claude Code session,
 *  Oct 20" or "From your CLAUDE.md". Empty for every other source. */
export function historySourceLabel(t: TFunction<'knowledge'>, card: main.KnowHowCard, locale: string): string {
    const extra = card as main.KnowHowCard & { sourceAssistant?: string; sourceAt?: number };
    switch (card.source) {
        case 'claude-md':
            return t('firstDay.fromClaudeMd');
        case 'assistant-history': {
            const at = extra.sourceAt || card.savedAt;
            return t('firstDay.fromSession', {
                assistant: extra.sourceAssistant === 'codex' ? 'Codex' : 'Claude Code',
                date: at > 0 ? new Date(at).toLocaleDateString(locale, { month: 'short', day: 'numeric' }) : '',
            });
        }
        default:
            return '';
    }
}

function KnowHowHeader({ cards }: { cards: main.KnowHowCard[] | null }) {
    const { t } = useTranslation('knowledge');
    const { t: tCommon } = useTranslation('common');
    const firstDay = useFirstDay();
    const week = cards ? savedThisWeek(cards) : 0;
    const fromHistory = firstDay.firstDays && historyFound(firstDay.history)
        ? (cards ?? []).filter((card) => card.source === 'assistant-history' || card.source === 'claude-md').length
        : 0;
    return (
        <header className="flex flex-col gap-2">
            <div className="text-[11px] font-medium uppercase tracking-[0.08em] text-muted">{t('header.label')}</div>
            <h1 className="m-0 text-[26px] font-normal leading-[1.3]">{t('header.heading')}</h1>
            {fromHistory > 0 && firstDay.history ? (
                <p className="m-0 text-sm text-muted">{t('firstDay.line', { count: fromHistory, history: historyName(firstDay.history.assistants, tCommon) })}</p>
            ) : cards && cards.length > 0 && (
                <p className="m-0 text-sm text-muted">
                    {week > 0
                        ? t('header.countThisWeek', { count: cards.length, week })
                        : t('header.count', { count: cards.length })}{' '}
                    {t('header.resurface')}
                </p>
            )}
        </header>
    );
}

function KnowHowMatchBanner({
    match,
    locale,
    dismissing,
    dismissFailed,
    onShow,
    onDismiss,
}: {
    match: main.KnowHowMatch;
    locale: string;
    dismissing: boolean;
    dismissFailed: ActivityNotice | null;
    onShow: () => void;
    onDismiss: () => void;
}) {
    const { t } = useTranslation('knowledge');
    const when = matchWhen(t, match.previousAt, locale);
    return (
        <section
            data-testid="know-how-match"
            className="flex flex-wrap items-center gap-x-4 gap-y-3 rounded-xl border border-border bg-panel px-[18px] py-4 shadow-[var(--shadow-card)]"
        >
            <RotateCcw className="size-[18px] shrink-0 text-muted" aria-hidden="true" />
            <div className="flex min-w-0 flex-[1_1_340px] flex-col gap-0.5">
                <span className="text-sm font-semibold">{t('match.title', { title: match.title })}</span>
                <span className="text-[13px] text-muted">
                    {match.currentApp
                        ? t('match.bodyApp', { app: match.currentApp, when })
                        : t('match.body', { when })}
                </span>
                <ActivityInlineNotice notice={dismissFailed} className="text-[12.5px]" />
            </div>
            <Button size="sm" onClick={onShow}>
                {t('match.show')}
            </Button>
            <Button variant="outline" size="sm" onClick={onDismiss} disabled={dismissing} aria-busy={dismissing}>
                {t('match.dismiss')}
            </Button>
        </section>
    );
}

/** The proposal at the top of the page: the same steps seen the same way in
 *  three or more sessions. Review opens the draft; Not a how-to suppresses
 *  this exact sequence for good. */
function KnowHowCandidateBanner({
    candidate,
    locale,
    busy,
    failed,
    onConfirm,
    onReview,
    onDecline,
}: {
    candidate: main.KnowHowCandidate;
    locale: string;
    busy: boolean;
    failed: ActivityNotice | null;
    onConfirm: () => void;
    onReview: () => void;
    onDecline: () => void;
}) {
    const { t } = useTranslation('knowledge');
    const dates = candidateDatesLabel(t, candidate.occurredAtMs ?? [], locale);
    const each = t('candidate.aboutEach', { duration: candidateDurationLabel(t, candidate.avgDurationMs) });
    return (
        <section
            data-testid="know-how-candidate"
            aria-label={t('candidate.bannerLabel')}
            className="flex flex-wrap items-center gap-x-4 gap-y-3 rounded-xl border border-border bg-panel px-[18px] py-4 shadow-[var(--shadow-card)]"
        >
            <PenLine className="size-[18px] shrink-0 text-muted" aria-hidden="true" />
            <div className="flex min-w-0 flex-[1_1_340px] flex-col gap-0.5">
                <span className="text-sm font-semibold">{t('candidate.title', { count: candidate.times })}</span>
                <span className="text-[13px] text-muted">
                    {t('candidate.body', { title: candidate.title, count: (candidate.steps ?? []).length, dates, each })}
                </span>
                <ActivityInlineNotice notice={failed} className="text-[12.5px]" />
            </div>
            <Button size="sm" onClick={onConfirm} disabled={busy} aria-busy={busy}>
                {t('candidate.confirm')}
            </Button>
            <Button variant="secondary" size="sm" onClick={onReview}>
                {t('candidate.review')}
            </Button>
            <Button variant="outline" size="sm" onClick={onDecline} disabled={busy} aria-busy={busy}>
                {t('candidate.notHowTo')}
            </Button>
        </section>
    );
}

/** The draft review: what would be written down, what stays in Activity, and
 *  the three answers — Confirm, Edit steps, Not a how-to. */
function KnowHowCandidateDetail({
    candidate,
    locale,
    busy,
    failed,
    onConfirm,
    onEditSteps,
    onNotHowTo,
}: {
    candidate: main.KnowHowCandidate;
    locale: string;
    busy: boolean;
    failed: ActivityNotice | null;
    onConfirm: () => void;
    onEditSteps: () => void;
    onNotHowTo: () => void;
}) {
    const { t } = useTranslation('knowledge');
    const steps = candidate.steps ?? [];
    const dates = candidateDatesLabel(t, candidate.occurredAtMs ?? [], locale);
    const each = t('candidate.aboutEach', { duration: candidateDurationLabel(t, candidate.avgDurationMs) });
    const apps = (candidate.apps ?? []).join(', ');
    const whenLine = [dates, apps, each].filter(Boolean).join(' · ');
    const meta = [
        t('technical.source', { source: 'observed' }),
        t('technical.sessions', { count: candidate.times }),
        t('technical.scope', { scope: candidate.scope || 'mine' }),
    ].join(' · ');

    return (
        <ScrollArea className="h-full">
            <div className="flex flex-col gap-5 px-6 pt-7 pb-7">
                <div className="flex flex-col gap-2">
                    <div className="flex items-center gap-2 pr-10">
                        <span className="text-[11px] font-medium uppercase tracking-[0.08em] text-muted">
                            {t('candidate.draftLabel')}
                        </span>
                    </div>
                    <h2 className="m-0 text-[19px] font-semibold leading-[1.35] select-text">{candidate.title}</h2>
                    {candidate.description && <p className="m-0 text-sm text-muted select-text">{candidate.description}</p>}
                </div>

                <ActivityInlineNotice notice={failed} className="m-0 text-[12.5px]" />

                {candidate.trigger && (
                    <section className="flex flex-col gap-2">
                        <h3 className="m-0 text-[13px] font-semibold">{t('detail.whenToUse')}</h3>
                        <p className="m-0 text-[13.5px] leading-normal select-text">{candidate.trigger}</p>
                    </section>
                )}

                <section className="flex flex-col gap-2">
                    <h3 className="m-0 text-[13px] font-semibold">{t('detail.points.how_to')}</h3>
                    <ol className="m-0 flex list-decimal flex-col gap-2 pl-5 select-text">
                        {steps.map((step, index) => (
                            <li key={index} className="text-[13.5px] leading-normal">{step}</li>
                        ))}
                    </ol>
                </section>

                <section className="flex flex-col gap-2">
                    <h3 className="m-0 text-[13px] font-semibold">{t('detail.origin')}</h3>
                    <div className="flex flex-col gap-1 rounded-lg border border-border bg-panel px-3 py-2.5">
                        {whenLine && <span className="text-xs text-muted">{whenLine}</span>}
                        <span className="text-[13.5px]">{t('detail.sources.observed', { count: candidate.times })}</span>
                    </div>
                </section>

                <section className="flex flex-col gap-2 rounded-[10px] bg-panel-warm px-3.5 py-3">
                    <div className="grid grid-cols-2 gap-3 text-[12.5px] leading-normal">
                        <div>
                            <div className="pb-1 text-[11px] font-medium uppercase tracking-[0.08em] text-muted">
                                {t('candidate.writtenDownTitle')}
                            </div>
                            {t('candidate.writtenDownBody', { count: steps.length })}
                        </div>
                        <div>
                            <div className="pb-1 text-[11px] font-medium uppercase tracking-[0.08em] text-muted">
                                {t('candidate.staysTitle')}
                            </div>
                            {t('candidate.staysBody')}
                        </div>
                    </div>
                </section>

                <div className="flex flex-wrap items-center gap-2 border-t border-border-subtle pt-3.5">
                    <Button size="sm" onClick={onConfirm} disabled={busy} aria-busy={busy}>
                        {t('candidate.confirm')}
                    </Button>
                    <Button variant="secondary" size="sm" onClick={onEditSteps} disabled={busy}>
                        {t('candidate.editSteps')}
                    </Button>
                    <Button variant="outline" size="sm" onClick={onNotHowTo} disabled={busy}>
                        {t('candidate.notHowTo')}
                    </Button>
                </div>

                <TechnicalDetails>
                    <p className="m-0 select-text">{meta}</p>
                    <p className="m-0 font-mono text-[11.5px] select-text">{t('technical.id', { id: candidate.id })}</p>
                </TechnicalDetails>
            </div>
        </ScrollArea>
    );
}

/** Edit steps: the draft's title, when to use it and one step per line. The
 *  draft stays a draft until Confirm. */
function KnowHowCandidateEditPanel({
    title,
    trigger,
    steps,
    saving,
    failed,
    onTitleChange,
    onTriggerChange,
    onStepsChange,
    onSave,
    onCancel,
}: {
    title: string;
    trigger: string;
    steps: string;
    saving: boolean;
    failed: ActivityNotice | null;
    onTitleChange: (value: string) => void;
    onTriggerChange: (value: string) => void;
    onStepsChange: (value: string) => void;
    onSave: () => void;
    onCancel: () => void;
}) {
    const { t } = useTranslation('knowledge');
    return (
        <div className="flex h-full min-h-0 flex-col">
            <div className="flex items-center gap-2 pl-6 pr-14 pt-6 pb-3">
                <h2 className="m-0 text-sm font-medium">{t('candidate.editTitle')}</h2>
                <div className="ml-auto flex items-center gap-2">
                    <Button
                        size="sm"
                        onClick={onSave}
                        disabled={saving || title.trim().length === 0 || steps.trim().length === 0}
                        aria-busy={saving}
                    >
                        <Save className="size-3.5" />
                        {saving ? t('edit.saving') : t('edit.save')}
                    </Button>
                    <Button variant="outline" size="sm" onClick={onCancel}>
                        {t('edit.cancel')}
                    </Button>
                </div>
            </div>
            <ActivityInlineNotice notice={failed} className="mx-6 mb-3 text-[12.5px]" />
            <div className="flex min-h-0 flex-1 flex-col gap-3 px-6 pb-6">
                <label className="flex flex-col gap-1.5 text-[12px] font-medium text-muted">
                    {t('candidate.editTitleField')}
                    <input
                        type="text"
                        value={title}
                        onChange={(e) => onTitleChange(e.target.value)}
                        className="h-8 rounded-lg border border-border bg-panel px-3 text-[12.5px] font-normal text-ink focus:border-border-focus focus:outline-none"
                    />
                </label>
                <label className="flex flex-col gap-1.5 text-[12px] font-medium text-muted">
                    {t('detail.whenToUse')}
                    <input
                        type="text"
                        value={trigger}
                        onChange={(e) => onTriggerChange(e.target.value)}
                        className="h-8 rounded-lg border border-border bg-panel px-3 text-[12.5px] font-normal text-ink focus:border-border-focus focus:outline-none"
                    />
                </label>
                <label className="flex min-h-0 flex-1 flex-col gap-1.5 text-[12px] font-medium text-muted">
                    {t('candidate.editStepsField')}
                    <Textarea
                        aria-label={t('candidate.editStepsField')}
                        className="min-h-0 flex-1 resize-none text-xs leading-relaxed"
                        value={steps}
                        onChange={(e) => onStepsChange(e.target.value)}
                        autoFocus
                    />
                </label>
            </div>
        </div>
    );
}

function KindFilter({ value, onChange }: { value: KnowHowFilter; onChange: (kind: KnowHowFilter) => void }) {    const { t } = useTranslation('knowledge');
    const filters: KnowHowFilter[] = ['all', ...KNOW_HOW_KINDS];
    return (
        <div role="group" aria-label={t('filterLabel')} className="flex flex-wrap gap-1.5">
            {filters.map((filter) => {
                const on = value === filter;
                return (
                    <Button
                        key={filter}
                        variant="ghost"
                        size="sm"
                        aria-pressed={on}
                        onClick={() => onChange(filter)}
                        className={cn(on && 'bg-surface-active')}
                    >
                        {t(`kinds.${filter}`)}
                    </Button>
                );
            })}
        </div>
    );
}

function PageMoreMenu({
    grouped,
    canTranslateAll,
    onToggleGrouped,
    onTranslateAll,
    onRefresh,
}: {
    grouped: boolean;
    canTranslateAll: boolean;
    onToggleGrouped: () => void;
    onTranslateAll: () => void;
    onRefresh: () => void;
}) {
    const { t } = useTranslation('knowledge');
    return (
        <DropdownMenu>
            <DropdownMenuTrigger asChild>
                <Button variant="outline" size="icon" aria-label={t('more.page')}>
                    <MoreHorizontal className="size-4" />
                </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end">
                <DropdownMenuItem onSelect={onToggleGrouped}>
                    <CalendarDays />
                    {grouped ? t('more.ungroup') : t('more.groupByTime')}
                </DropdownMenuItem>
                <DropdownMenuItem disabled={!canTranslateAll} onSelect={onTranslateAll}>
                    <Languages />
                    {t('more.translateAll')}
                </DropdownMenuItem>
                <DropdownMenuItem onSelect={onRefresh}>
                    <RefreshCw />
                    {t('more.refresh')}
                </DropdownMenuItem>
            </DropdownMenuContent>
        </DropdownMenu>
    );
}

/**
 * Cosine threshold for know-how. The embedder reads the title and the takeaway
 * together (launch walk W40), which lifts paraphrases to about 0.78 and leaves
 * unrelated pairs near 0; 0.7 keeps sampled groups to one idea each.
 */
const IDEA_MEANING_THRESHOLD_KNOW_HOW = 0.7;

function cardTitle(card: main.KnowHowCard): string {
    return card.title;
}

/** What the embedder reads for a card: its title and its takeaway. */
function cardMeaningText(card: main.KnowHowCard): string {
    return card.takeaway ? `${card.title}. ${card.takeaway}` : card.title;
}

function cardSavedAt(card: main.KnowHowCard): number {
    return card.savedAt;
}

function CardGrid({
    cards,
    copyCounts,
    selectedId,
    landedId = null,
    locale,
    onSelect,
}: {
    cards: main.KnowHowCard[];
    /** Saved copies each card stands for, by the card's id. */
    copyCounts: Map<string, number>;
    selectedId: string | null;
    landedId?: string | null;
    locale: string;
    onSelect: (id: string | null) => void;
}) {
    return (
        <div className="grid grid-cols-[repeat(auto-fill,minmax(min(100%,300px),1fr))] gap-3">
            {cards.map((card) => (
                <KnowHowCardButton
                    key={card.id}
                    card={card}
                    copies={copyCounts.get(card.id) ?? 1}
                    locale={locale}
                    selected={card.id === selectedId}
                    landed={card.id === landedId}
                    onSelect={onSelect}
                />
            ))}
        </div>
    );
}

function KnowHowCardButton({
    card,
    copies = 1,
    locale,
    selected,
    landed = false,
    onSelect,
}: {
    card: main.KnowHowCard;
    /** Saved copies this card stands for; above one it reads "Seen N times". */
    copies?: number;
    locale: string;
    selected: boolean;
    /** Just saved from the proposal: scrolled into view with the violet wash, once. */
    landed?: boolean;
    onSelect: (id: string | null) => void;
}) {
    const { t } = useTranslation('knowledge');
    const ref = useRef<HTMLButtonElement>(null);
    useEffect(() => {
        if (landed) ref.current?.scrollIntoView?.({ block: 'nearest' });
    }, [landed]);
    const when = dayInWords(t, card.savedAt, locale);
    const apps = (card.apps ?? []).slice(0, MAX_CARD_APPS).join(', ');
    const from = historySourceLabel(t, card, locale) || (when && apps ? t('from', { when, apps }) : when || apps);
    return (
        <button
            ref={ref}
            type="button"
            data-know-how-id={card.id}
            data-landed={landed || undefined}
            aria-pressed={selected}
            onClick={() => onSelect(selected ? null : card.id)}
            className={cn(
                'flex flex-col items-start gap-2.5 rounded-xl border border-border p-4 text-left text-ink transition-colors hover:border-border-focus focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent-2/40',
                selected ? 'bg-surface-active' : 'bg-panel',
                landed && 'answer-wash',
            )}
        >
            <span className="flex w-full items-center gap-2">
                <span className="inline-flex h-[22px] items-center rounded-md border border-border bg-panel-warm px-2 text-[11.5px] font-medium text-muted">
                    {t(`kinds.${knowHowKind(card.kind)}`)}
                </span>
                {card.reuseCount >= 1 && <span className="ml-auto text-xs text-muted">{reuseLabel(t, card.reuseCount)}</span>}
            </span>
            <span className="text-[14.5px] font-semibold leading-[1.35]">{card.title}</span>
            {card.takeaway && <span className="line-clamp-3 text-[13px]">{card.takeaway}</span>}
            {(from || copies > 1) && (
                <span className="flex w-full flex-wrap items-center gap-x-2 text-xs text-muted">
                    {from && <span>{from}</span>}
                    {copies > 1 && <span className="ml-auto">{t('reuse.copies', { count: copies })}</span>}
                </span>
            )}
        </button>
    );
}

function KnowHowDetail({
    card,
    copies = [card],
    entry,
    locale,
    translating,
    notice,
    onEdit,
    onCopyMarkdown,
    onTranslate,
    onHistory,
    onDelete,
    onOpenActivitySession,
}: {
    card: main.KnowHowCard;
    /** Every copy of the idea, newest first; their moments are the card's sources. */
    copies?: main.KnowHowCard[];
    entry: main.KnowledgeEntry | null;
    locale: string;
    translating: boolean;
    notice: ActivityNotice | null;
    onEdit: () => void;
    onCopyMarkdown: () => void;
    onTranslate: () => void;
    onHistory: () => void;
    onDelete: () => void;
    onOpenActivitySession?: (sessionId: string) => void;
}) {
    const { t } = useTranslation('knowledge');
    const kind = knowHowKind(card.kind);
    const points = card.points ?? [];
    // Sources from every copy, oldest first, each moment once.
    const sources = uniqueBy(
        copies.flatMap((copy) => copy.moments ?? []).sort((a, b) => a.at - b.at),
        (moment) => `${moment.at}|${moment.assistant}|${moment.app}|${moment.sessionId}`,
    );
    const apps = (card.apps ?? []).join(', ');
    const when = card.savedAt > 0 ? dateTimeInWords(card.savedAt, locale) : '';
    const whenLine = [when, apps].filter(Boolean).join(' · ');
    const [asking, setAsking] = useState(false);
    const [draft, setDraft] = useState<'idle' | 'drafting' | 'failed'>('idle');
    useEffect(() => {
        setAsking(false);
        setDraft('idle');
    }, [card.id]);
    // Turn into a playbook drafts in place, through the same author as Create (W8).
    const turnIntoPlaybook = () => {
        setDraft('drafting');
        GenerateSOPFromDescription(playbookRequest(card)).catch(() => setDraft('failed'));
    };
    const source = historySourceLabel(t, card, locale) || originSentence(t, card.source ?? '', card.sessionCount);
    const meta = [
        card.formatVersion ? t('technical.formatVersion', { version: card.formatVersion }) : '',
        card.source ? t('technical.source', { source: card.source }) : '',
        card.sessionCount > 0 ? t('technical.sessions', { count: card.sessionCount }) : '',
        card.savedAt > 0 ? t('technical.saved', { value: savedTimestamp(card.savedAt) }) : '',
        t('technical.scope', { scope: card.scope || 'mine' }),
    ].filter(Boolean).join(' · ');

    return (
        <ScrollArea className="h-full">
            <div className="flex flex-col gap-5 px-6 pt-7 pb-7">
                <div className="flex flex-col gap-2">
                    <div className="flex items-center gap-2 pr-10">
                        <span className="text-[11px] font-medium uppercase tracking-[0.08em] text-muted">
                            {t(`kinds.${kind}`)}
                        </span>
                        {card.reuseCount >= 1 && (
                            <span className="ml-auto text-xs text-muted">{reuseLabel(t, card.reuseCount)}</span>
                        )}
                    </div>
                    <h2 className="m-0 text-[19px] font-semibold leading-[1.35] select-text">{card.title}</h2>
                    {card.takeaway && <p className="m-0 text-sm text-muted select-text">{card.takeaway}</p>}
                </div>

                {translating && (
                    <p role="status" className="m-0 text-[12.5px] text-muted">{t('detail.translating')}</p>
                )}
                <ActivityInlineNotice notice={notice} className="m-0 text-[12.5px]" />

                {card.trigger && (
                    <section className="flex flex-col gap-2">
                        <h3 className="m-0 text-[13px] font-semibold">{t('detail.whenToUse')}</h3>
                        <p className="m-0 text-[13.5px] leading-normal select-text">{card.trigger}</p>
                    </section>
                )}

                <section className="flex flex-col gap-2">
                    <h3 className="m-0 text-[13px] font-semibold">{t(`detail.points.${kind}`)}</h3>
                    {points.length > 0 ? (
                        <ol className="m-0 flex list-decimal flex-col gap-2 pl-5 select-text">
                            {points.map((point, index) => (
                                <li key={index} className="text-[13.5px] leading-normal">{point}</li>
                            ))}
                        </ol>
                    ) : (
                        // No points: the know-how's own words show under the heading (W12).
                        entry && <EntryBody content={entry.content} omit={[card.title, card.takeaway]} />
                    )}
                </section>

                <section className="flex flex-col gap-2">
                    <h3 className="m-0 text-[13px] font-semibold">{t('detail.origin')}</h3>
                    <div className="flex flex-col gap-1 rounded-lg border border-border bg-panel px-3 py-2.5">
                        {whenLine && <span className="text-xs text-muted">{whenLine}</span>}
                        <span className="text-[13.5px]">{source}</span>
                    </div>
                    {(sources.length) > 1 ? (
                        <LessonMoments moments={sources} locale={locale} onOpenActivitySession={onOpenActivitySession} />
                    ) : (
                        entry && <EntryMoments content={entry.content} locale={locale} onOpenActivitySession={onOpenActivitySession} />
                    )}
                    {card.topicName && (
                        <span className="text-[12.5px] text-muted">{t('detail.topic', { name: card.topicName })}</span>
                    )}
                </section>

                <div className="flex flex-wrap items-center gap-2 border-t border-border-subtle pt-3.5">
                    {kind === 'how_to' && draft === 'idle' && (
                        <Button size="sm" onClick={turnIntoPlaybook}>
                            {t('detail.cta.how_to')}
                        </Button>
                    )}
                    <Button
                        variant={kind === 'how_to' && draft === 'idle' ? 'secondary' : 'default'}
                        size="sm"
                        aria-expanded={asking}
                        onClick={() => setAsking((open) => !open)}
                    >
                        {t('detail.ask')}
                    </Button>
                    <Button variant="outline" size="sm" onClick={onEdit} disabled={!entry}>
                        {t('detail.edit')}
                    </Button>
                    <EntryMoreMenu
                        title={card.title}
                        hasEntry={!!entry}
                        hasHistory={!!entry?.hasHistory}
                        translating={translating}
                        onCopyMarkdown={onCopyMarkdown}
                        onTranslate={onTranslate}
                        onHistory={onHistory}
                        onDelete={onDelete}
                    />
                </div>

                {draft === 'drafting' && (
                    <p role="status" className="m-0 text-[12.5px] text-muted">{t('detail.drafting')}</p>
                )}
                {draft === 'failed' && (
                    <ActivityInlineNotice notice={errorNotice(t('detail.draftFailed'), turnIntoPlaybook)} className="m-0 text-[12.5px]" />
                )}
                {asking && (
                    <ActivityAskInline context={{ sourceKind: 'knowledge', sourceId: card.id }} onClose={() => setAsking(false)} />
                )}

                <TechnicalDetails>
                    {meta && <p className="m-0 select-text">{meta}</p>}
                    <p className="m-0 font-mono text-[11.5px] select-text">
                        {t('technical.id', { id: card.id })}
                        {card.topicId ? ` · ${t('technical.topicId', { id: card.topicId })}` : ''}
                    </p>
                    {entry && (
                        <>
                            <EntrySessions content={entry.content} onOpenActivitySession={onOpenActivitySession} />
                            <div className="mt-1 flex flex-col gap-2">
                                <h4 className="m-0 text-xs font-medium text-ink">{t('technical.fullEntry')}</h4>
                                <EntryBody content={entry.content} />
                            </div>
                        </>
                    )}
                </TechnicalDetails>
            </div>
        </ScrollArea>
    );
}

function EntryMoreMenu({
    title,
    hasEntry,
    hasHistory,
    translating,
    onCopyMarkdown,
    onTranslate,
    onHistory,
    onDelete,
}: {
    title: string;
    hasEntry: boolean;
    hasHistory: boolean;
    translating: boolean;
    onCopyMarkdown: () => void;
    onTranslate: () => void;
    onHistory: () => void;
    onDelete: () => void;
}) {
    const { t } = useTranslation('knowledge');
    return (
        <DropdownMenu>
            <DropdownMenuTrigger asChild>
                <Button variant="outline" size="icon" aria-label={t('more.entry', { title })}>
                    <MoreHorizontal className="size-4" />
                </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end">
                <DropdownMenuItem disabled={!hasEntry} onSelect={onCopyMarkdown}>
                    <Copy />
                    {t('more.copyMarkdown')}
                </DropdownMenuItem>
                <DropdownMenuItem disabled={translating} onSelect={onTranslate}>
                    <Languages />
                    {t('more.translate')}
                </DropdownMenuItem>
                {hasHistory && (
                    <DropdownMenuItem onSelect={onHistory}>
                        <History />
                        {t('more.history')}
                    </DropdownMenuItem>
                )}
                <DropdownMenuSeparator />
                <DropdownMenuItem destructive onSelect={onDelete}>
                    <Trash2 />
                    {t('more.delete')}
                </DropdownMenuItem>
            </DropdownMenuContent>
        </DropdownMenu>
    );
}

function KnowHowEditPanel({
    fields,
    saving,
    error,
    onChange,
    onSave,
    onCancel,
}: {
    fields: KnowHowFields;
    saving: boolean;
    error: string | null;
    onChange: (value: KnowHowFields) => void;
    onSave: () => void;
    onCancel: () => void;
}) {
    const { t } = useTranslation('knowledge');
    // Title, When to use it and Steps; the file's header is kept, never shown (W14).
    return (
        <ScrollArea className="h-full">
            <div className="flex flex-col gap-4 px-6 pt-6 pb-6">
                <div className="flex items-center gap-2 pr-8">
                    <h2 className="m-0 text-sm font-medium">{t('edit.title')}</h2>
                    <div className="ml-auto flex items-center gap-2">
                        <Button size="sm" onClick={onSave} disabled={saving || fields.title.trim().length === 0} aria-busy={saving}>
                            <Save className="size-3.5" />
                            {saving ? t('edit.saving') : t('edit.save')}
                        </Button>
                        <Button variant="outline" size="sm" onClick={onCancel}>
                            {t('edit.cancel')}
                        </Button>
                    </div>
                </div>
                <ActivityInlineNotice notice={error ? errorNotice(error, onSave) : null} className="m-0 text-[12.5px]" />
                <label className="flex flex-col gap-1.5 text-[12.5px] font-medium">
                    {t('edit.titleField')}
                    <input className="h-8 rounded-lg border border-border bg-panel px-2.5 text-[13px] font-normal text-ink focus:border-border-focus focus:outline-none" value={fields.title} autoFocus onChange={(e) => onChange({ ...fields, title: e.target.value })} />
                </label>
                <label className="flex flex-col gap-1.5 text-[12.5px] font-medium">
                    {t('detail.whenToUse')}
                    <Textarea rows={2} value={fields.trigger} onChange={(e) => onChange({ ...fields, trigger: e.target.value })} />
                </label>
                <label className="flex flex-col gap-1.5 text-[12.5px] font-medium">
                    {t('edit.stepsField')}
                    <Textarea
                        rows={8}
                        value={fields.steps.join('\n')}
                        onChange={(e) => onChange({ ...fields, steps: e.target.value.split('\n') })}
                    />
                </label>
            </div>
        </ScrollArea>
    );
}

function KnowHowHistoryPanel({
    title,
    locale,
    versions,
    selectedTs,
    content,
    loadingList,
    loadingContent,
    notice,
    translating,
    onSelect,
    onTranslate,
    onExit,
    onOpenActivitySession,
}: {
    title: string;
    locale: string;
    versions: KnowledgeVersionListItem[];
    selectedTs: SelectedKnowledgeVersion;
    content: string;
    loadingList: boolean;
    loadingContent: boolean;
    notice: ActivityNotice | null;
    translating: boolean;
    onSelect: (ts: SelectedKnowledgeVersion) => void;
    onTranslate: () => void;
    onExit: () => void;
    onOpenActivitySession?: (sessionId: string) => void;
}) {
    const { t } = useTranslation('knowledge');
    const rowClass = (on: boolean) =>
        cn(
            'w-full rounded-md px-3 py-2 text-left text-xs transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent-2/40',
            on ? 'bg-active-row-bg font-medium text-ink' : 'text-muted hover:bg-surface-hover hover:text-ink',
        );
    return (
        <div className="flex h-full min-h-0 flex-col">
            <div className="flex items-start gap-3 pl-6 pr-14 pt-6 pb-3">
                <div className="min-w-0 flex-1">
                    <div className="mb-0.5 flex items-center gap-1.5 text-[11px] uppercase tracking-[0.08em] text-muted">
                        <History className="size-3" aria-hidden="true" />
                        <span>{t('history.versionHistory')}</span>
                    </div>
                    <h2 className="m-0 truncate text-sm font-medium">{title}</h2>
                </div>
                <Button
                    variant="outline"
                    size="sm"
                    onClick={onTranslate}
                    disabled={translating || selectedTs === null}
                    title={t('translate.selectedTooltip')}
                    aria-busy={translating}
                >
                    <Languages className="size-3.5" />
                    {translating ? t('translate.translating') : t('translate.action')}
                </Button>
                <Button variant="outline" size="sm" onClick={onExit}>
                    <X className="size-3.5" />
                    {t('history.exit')}
                </Button>
            </div>
            <ActivityInlineNotice notice={notice} className="mx-6 mb-3 text-[12.5px]" />
            <div className="flex min-h-0 flex-1 border-t border-border-subtle">
                <ScrollArea className="w-44 shrink-0 border-r border-border-subtle">
                    <div className="space-y-0.5 p-2">
                        <button type="button" className={rowClass(selectedTs === 'current')} onClick={() => onSelect('current')}>
                            {t('history.current')}
                        </button>
                        {loadingList && versions.length === 0 ? (
                            <div className="py-4 text-center text-xs text-muted" aria-busy="true">{t('history.loading')}</div>
                        ) : versions.length === 0 ? (
                            notice?.tone !== 'error' && <div className="px-3 py-6 text-center text-xs leading-relaxed text-muted">{t('history.noHistoryYet')}</div>
                        ) : (
                            versions.map((version) => (
                                <button
                                    key={version.timestampMs}
                                    type="button"
                                    className={rowClass(selectedTs === version.timestampMs)}
                                    onClick={() => onSelect(version.timestampMs)}
                                >
                                    {dateTimeInWords(version.timestampMs, locale)}
                                </button>
                            ))
                        )}
                    </div>
                </ScrollArea>
                <ScrollArea className="min-w-0 flex-1">
                    {loadingContent ? (
                        <div className="py-10 text-center text-xs text-muted" aria-busy="true">{t('history.loadingSnapshot')}</div>
                    ) : selectedTs === null ? (
                        <div className="py-10 text-center text-xs text-muted">{t('history.selectSnapshot')}</div>
                    ) : (
                        <div className="flex flex-col gap-4 px-5 py-4">
                            <EntryBody content={content} />
                            <TechnicalDetails>
                                <EntryFields content={content} />
                                <EntrySessions content={content} onOpenActivitySession={onOpenActivitySession} />
                            </TechnicalDetails>
                        </div>
                    )}
                </ScrollArea>
            </div>
        </div>
    );
}

function TechnicalDetails({ children }: { children: React.ReactNode }) {
    const { t } = useTranslation('knowledge');
    // Developer tools only (W12).
    if (!useDevTools()) return null;
    return (
        <Collapsible>
            <CollapsibleTrigger className="group inline-flex items-center gap-1 rounded-md text-[12.5px] text-muted transition-colors hover:text-ink focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent-2/40">
                <ChevronRight className="size-3.5 transition-transform group-data-[state=open]:rotate-90" aria-hidden="true" />
                {t('technical.title')}
            </CollapsibleTrigger>
            <CollapsibleContent className="mt-2 flex flex-col gap-2 text-[12.5px] text-muted">{children}</CollapsibleContent>
        </Collapsible>
    );
}

/** The entry's words without its metadata block. */
/** The entry's own words, without the paragraphs the detail already shows. */
function EntryBody({ content, omit = [] }: { content: string; omit?: Array<string | undefined> }) {
    const { body } = splitHeaderAndBody(content);
    const shown = new Set(omit.map((o) => o?.trim()).filter(Boolean));
    const kept = body
        .split(/\n{2,}/)
        .filter((block) => !shown.has(block.replace(/^#+\s+/, '').trim()))
        .join('\n\n');
    if (!kept.trim()) return null;
    return (
        <div className="rounded-lg border border-border bg-panel px-4 py-3 text-ink select-text">
            <MarkdownContent content={kept} />
        </div>
    );
}

// Labels for the metadata fields knowledge files carry, OKF and legacy.
const FIELD_LABELS: Record<string, 'type' | 'timestamp' | 'source' | 'dimension' | 'apps' | 'kind' | 'trigger'> = {
    type: 'type',
    timestamp: 'timestamp',
    updated: 'timestamp',
    time: 'timestamp',
    source: 'source',
    dimension: 'dimension',
    apps: 'apps',
    kind: 'kind',
    trigger: 'trigger',
};

/** The file's raw metadata, for Technical details only. */
function EntryFields({ content }: { content: string }) {
    const { t } = useTranslation('knowledge');
    const fields = parseFrontmatter(content).filter((item) => !item.sessions && !item.groups && item.value);
    if (fields.length === 0) return null;
    return (
        <dl className="m-0 grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 select-text">
            {fields.map((item) => {
                const known = FIELD_LABELS[item.key.toLowerCase()];
                return (
                    <div key={item.key} className="contents">
                        <dt>{known ? t(`technical.fields.${known}`) : item.key}</dt>
                        <dd className="m-0 break-all font-mono text-[11.5px] text-ink">{item.value}</dd>
                    </div>
                );
            })}
        </dl>
    );
}

/** Where it came from: one link per moment, each opening it in Activity (W12). */
/** Each time the lesson was learned, oldest first (W40). */
function LessonMoments({
    moments,
    locale,
    onOpenActivitySession,
}: {
    moments: main.KnowHowMoment[];
    locale: string;
    onOpenActivitySession?: (sessionId: string) => void;
}) {
    const { t } = useTranslation('knowledge');
    return (
        <ul className="m-0 flex list-none flex-col items-start p-0" aria-label={t('detail.origin')}>
            {moments.map((moment, index) => {
                const when = moment.at > 0 ? dateTimeInWords(moment.at, locale) : '';
                const who = moment.assistant === 'codex' ? 'Codex' : moment.assistant ? 'Claude Code' : moment.app;
                const label = when && who ? t('detail.moment', { when, apps: who }) : when || who;
                if (!label) return null;
                return (
                    <li key={`${moment.at}-${index}`}>
                        {onOpenActivitySession && moment.sessionId ? (
                            <Button variant="link" size="sm" className="h-auto px-0" onClick={() => onOpenActivitySession(moment.sessionId)}>
                                {label}
                            </Button>
                        ) : (
                            <span className="text-[12.5px] text-muted">{label}</span>
                        )}
                    </li>
                );
            })}
        </ul>
    );
}

function EntryMoments({
    content,
    locale,
    onOpenActivitySession,
}: {
    content: string;
    locale: string;
    onOpenActivitySession?: (sessionId: string) => void;
}) {
    const { t } = useTranslation('knowledge');
    const item = parseFrontmatter(content).find((field) => field.sessions || field.groups);
    const sessions = item ? flattenSessions(item) : [];
    if (sessions.length === 0) return null;
    return (
        <ul className="m-0 flex list-none flex-col items-start p-0" aria-label={t('detail.origin')}>
            {sessions.map((session) => {
                const at = Date.parse(session.time ?? '');
                const when = Number.isFinite(at) ? dateTimeInWords(at, locale) : session.time ?? '';
                const label = when && session.apps ? t('detail.moment', { when, apps: session.apps }) : when || session.apps || '';
                if (!label) return null;
                return (
                    <li key={session.id}>
                        {onOpenActivitySession ? (
                            <Button variant="link" size="sm" className="h-auto px-0" onClick={() => onOpenActivitySession(session.id)}>
                                {label}
                            </Button>
                        ) : (
                            <span className="text-[12.5px] text-muted">{label}</span>
                        )}
                    </li>
                );
            })}
        </ul>
    );
}

/** The Activity sessions an entry came from, each one click from Activity. */
function EntrySessions({
    content,
    onOpenActivitySession,
}: {
    content: string;
    onOpenActivitySession?: (sessionId: string) => void;
}) {
    const { t } = useTranslation('knowledge');
    const item = parseFrontmatter(content).find((field) => field.sessions || field.groups);
    const sessions = item ? flattenSessions(item) : [];
    if (sessions.length === 0) return null;
    return (
        <div className="flex flex-col gap-1">
            <span>{t('technical.activitySessions')}</span>
            <ul className="m-0 flex list-none flex-col items-start p-0">
                {sessions.map((session, index) => {
                    const label = [t('technical.session', { n: index + 1 }), session.time, session.apps].filter(Boolean).join(' · ');
                    return (
                        <li key={session.id}>
                            {onOpenActivitySession ? (
                                <Button variant="link" size="sm" title={session.id} onClick={() => onOpenActivitySession(session.id)}>
                                    {label}
                                </Button>
                            ) : (
                                <span title={session.id}>{label}</span>
                            )}
                        </li>
                    );
                })}
            </ul>
        </div>
    );
}

/** Shown beside the cards while no entry is open: the time buckets and the
 *  top sources the How you do things page's summary had. */

function KnowHowSkeleton() {
    const { t } = useTranslation('knowledge');
    return (
        <div
            role="status"
            aria-busy="true"
            className="grid grid-cols-[repeat(auto-fill,minmax(min(100%,300px),1fr))] gap-3"
        >
            <span className="sr-only">{t('states.loading')}</span>
            {Array.from({ length: 6 }, (_, index) => (
                <div key={index} className="flex flex-col gap-2.5 rounded-xl border border-border bg-panel p-4">
                    <span className="block h-2.5 w-2/5 rounded-md bg-surface-active" />
                    <span className="block h-3 w-full rounded-md bg-surface-active" />
                    <span className="block h-3 w-4/5 rounded-md bg-surface-active" />
                </div>
            ))}
        </div>
    );
}

/** Empty state when no knowledge entries exist */
function KnowHowEmptyState() {
    const { t } = useTranslation('knowledge');
    const categoryKeys = ['technical', 'decisions', 'failures', 'gotchas'] as const;
    return (
        <div className="flex justify-center py-[8vh] text-muted">
            <div className="mx-auto max-w-md px-6 text-center">
                <BookOpen className="mx-auto mb-4 h-12 w-12 opacity-20" aria-hidden="true" />
                <p className="mb-2 text-sm font-medium text-ink">{t('emptyState.heading')}</p>
                <p className="mb-5 text-xs leading-relaxed text-muted">{t('emptyState.description')}</p>
                <div className="grid grid-cols-2 gap-3 text-left">
                    {categoryKeys.map((key) => (
                        <div key={key} className="rounded-lg border border-border bg-panel px-3 py-2.5">
                            <p className="mb-0.5 text-[11px] font-medium text-ink">{t(`emptyState.categories.${key}.label`)}</p>
                            <p className="text-[10px] leading-relaxed text-muted">{t(`emptyState.categories.${key}.desc`)}</p>
                        </div>
                    ))}
                </div>
                <p className="mt-5 text-[11px] text-muted">{t('emptyState.footer')}</p>
            </div>
        </div>
    );
}
