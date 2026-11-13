// People (RC 2): ordered by what is open between you. The headline says
// what you owe; profiles that look like one person merge with an Undo; the
// detail beside the list briefs you and drafts a reply in place. Everything the page had before stays one
// click away in the page's More menu, a person's More menu or the detail's
// folded sections; senders Isoform could not name sort out in a sheet.
import { MutableRefObject, useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Contact, Loader2, MoreHorizontal, RefreshCw, Search, Sparkles, UserPlus } from 'lucide-react';
import {
    CleanupAnonymousPrincipals,
    DeletePrincipal,
    MergePeople,
    RefreshDerivedView,
    UnlinkPersonContact,
    type ContactsAccessStatus,
    type PeopleOverview,
    type PersonCard,
    type PrincipalDTO,
    type PrincipalWithStatsDTO,
} from '../lib/wails-shim-app';
import { GetContactsAccessStatus, ListPeopleOverview, ListPrincipalsWithStats, usePagePrivacyWithdrawal, usePageQueryRefresh } from '../lib/page-queries';
import { pageQueryCache } from '../lib/query-cache';
import { markPageContent } from '../lib/page-switch-timing';
import { EventsOn } from '../../wailsjs/runtime/runtime';
import { useBudgetedRefresh } from '../lib/app-activity';
import { ConfirmDialog } from './confirm-dialog';
import { ContactEditorDialog } from './contacts/contact-editor';
import { PrincipalPicker } from './contacts/principal-picker';
import { ContactsImportDialog } from './contacts/contacts-import-dialog';
import { ContactLinkDialog } from './contacts/contact-link-dialog';
import { Dialog, DialogCloseButton, DialogContent, DialogDescription, DialogHeader, DialogTitle } from './ui/dialog';
import { Avatar } from './ui/avatar';
import { Button } from './ui/button';
import { Sheet, SheetCloseButton, SheetContent, SheetDescription, SheetHeader, SheetTitle } from '@/components/ui/sheet';
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from './ui/dropdown-menu';
import { ScrollArea } from './ui/scroll-area';
import { useObservationPaused } from './today/useObservationPaused';
import { MergeBanners, MergeReceiptLine, NotSameReceiptLine, personName, type MergeReceipt } from './people/people-merge-banner';
import { PersonDetailPanel, type PersonActions } from './people/person-detail';
import { CalendarPeopleSection } from './people-first-day';
import { useFirstDay } from '@/hooks/useFirstDay';
import {
    appLabel,
    headlineWords,
    lastSeenWords,
    loopChip,
    loopLine,
    loopNeedsYou,
    readWithRetry,
    relationshipLabel,
    type Translate,
} from './people/people-format';
import { cn } from '../lib/utils';
import { PeopleDevToolsContext, PeopleFailureLine } from './people/people-failure';
import { ContactsStatusLine } from './people/contacts-status-line';
import { clearSavedPeopleView, peopleReadStamp, readSavedPeopleView, savePeopleView } from '../lib/people-saved-view';
import { SelfSenderGuesses } from './people/self-sender-guesses';

interface PeoplePageProps {
    /** The dialog's Escape asks the page first: true when the page stepped
     *  back (cleared the search), false to let the dialog close. */
    escapeRef?: MutableRefObject<(() => boolean) | null>;
    /** Called when the user clicks an entry in a person's "Activity
     *  sessions". Parent should close the Who you know page and open the Activity
     *  page anchored to the given session id. anchorTimestamp is when
     *  Isoform last saw the person in that session; principalName lets the
     *  Activity page highlight matching chat-message senders. */
    onOpenActivitySession?: (sessionId: string, anchorTimestamp?: number, principalName?: string) => void;
    /** "self" opens on your own card once the list loads (Settings >
     *  Learning and memory > Your profile). With no self entry yet, it opens
     *  the "Name yourself" dialog instead. `{ personId }` opens on that
     *  person (Search); a person no longer listed falls back to the usual
     *  first card. A new value applies again while the page stays mounted. */
    initialSelection?: PeopleSelection;
    /** Technical details, voice scores, raw reasons and the account-id
     *  field show only with developer tools on. */
    devToolsUnlocked?: boolean;
    /** False while the dialog is closed (the content is retained after the
     *  first open): the contacts poll pauses, and a reopen re-reads whatever
     *  outlived its TTL. */
    active?: boolean;
}

type LoadState = 'loading' | 'ready' | 'failed';

/** The cached reads a People write changes. DeletePrincipal, MergePeople,
 *  UndoMerge, CleanupAnonymousPrincipals and the editor's saves emit no
 *  contacts event, so every successful mutation invalidates these before
 *  reloading — a reload must never re-paint the pre-write cache. */
const PEOPLE_READS = ['ListPeopleOverview', 'ListPrincipalsWithStats'];
/** The contacts status safety resync while Who you know shows (it was a
 *  60 s poll; the sync runs every 6 hours and announces itself). */
export const PEOPLE_RESYNC_MS = 15 * 60_000;
function invalidatePeopleReads() {
    pageQueryCache.invalidate(PEOPLE_READS);
    clearSavedPeopleView();
}

/** Who People opens on: your own card, or one person. */
export type PeopleSelection = 'self' | { personId: string };

/** Senders Isoform could not match to a person, and people it has only seen
 *  in content. Neither is in "Everyone else": unidentified senders with an
 *  open loop stay in Open between you, the rest sort out in the sheet, and
 *  people seen only in content are found by search. */
function isUnnamedKind(kind: string): boolean {
    return kind === 'anonymous' || kind === 'content_subject';
}

// A cached paint may be stale while its binding is still running. An empty
// answer is usable only after that read settles successfully.
async function settledPeopleRead<T>(name: string, read: () => Promise<T>): Promise<T> {
    const value = await read();
    const state = pageQueryCache.state(name);
    if (state?.pending) return await state.pending as T;
    if (state?.error) throw state.error;
    return value;
}

export function PeoplePage({ escapeRef, initialSelection, devToolsUnlocked = false, active = true }: PeoplePageProps) {
    const { t } = useTranslation(['people', 'common']);
    const tr = useCallback<Translate>((key, options) => t(`people:${key}` as never, options as never) as unknown as string, [t]);
    const paused = useObservationPaused() === true;

    // A reopen paints the last reads from the page query cache. A first open
    // after launch paints the saved view once it arrives (below), while the
    // reads run behind it; the skeleton is for a person's very first load only.
    const [initial] = useState(() => {
        const cachedOverview = pageQueryCache.peek<PeopleOverview>('ListPeopleOverview');
        if (cachedOverview !== undefined) return { overview: cachedOverview, entries: pageQueryCache.peek<PrincipalWithStatsDTO[]>('ListPrincipalsWithStats') ?? [] };
        return null;
    });
    const [overview, setOverview] = useState<PeopleOverview | null>(initial?.overview ?? null);
    const [entries, setEntries] = useState<PrincipalWithStatsDTO[]>(initial?.entries ?? []);
    const [selfReadReady, setSelfReadReady] = useState(false);
    const [load, setLoad] = useState<LoadState>(initial ? 'ready' : 'loading');
    const [refreshing, setRefreshing] = useState(false);
    const [revision, setRevision] = useState(0);
    const [selectedID, setSelectedID] = useState<string | null>(null);
    // Clicking the selected person again reloads its detail (N53).
    const [reloadToken, setReloadToken] = useState(0);
    const pickPerson = useCallback((id: string) => {
        if (id === selectedID) setReloadToken((n) => n + 1);
        setSelectedID(id);
    }, [selectedID]);
    const firstDay = useFirstDay();
    const calendarPeople = firstDay.firstDays && firstDay.calendar?.connected ? firstDay.calendar.people ?? [] : [];
    const [keyword, setKeyword] = useState('');
    const [receipt, setReceipt] = useState<MergeReceipt | null>(null);
    // Not the same, with Undo until the page closes; and the person a merge
    // just made, which lands in the list with the wash.
    const [notSame, setNotSame] = useState<string[] | null>(null);
    const [landedID, setLandedID] = useState<string | null>(null);

    const [editTarget, setEditTarget] = useState<PrincipalDTO | null>(null);
    const [promoteTarget, setPromoteTarget] = useState<PrincipalDTO | null>(null);
    const [createOpen, setCreateOpen] = useState(false);
    const [selfCreateOpen, setSelfCreateOpen] = useState(false);
    const [deleteTarget, setDeleteTarget] = useState<PrincipalDTO | null>(null);
    const [mergeSource, setMergeSource] = useState<PrincipalDTO | null>(null);
    const [cleanupOpen, setCleanupOpen] = useState(false);
    const [cleanupBusy, setCleanupBusy] = useState(false);
    const [contactsImportOpen, setContactsImportOpen] = useState(false);
    const [contactsAccess, setContactsAccess] = useState<ContactsAccessStatus | null>(() => pageQueryCache.peek<ContactsAccessStatus>('GetContactsAccessStatus') ?? null);
    const [linkTarget, setLinkTarget] = useState<PrincipalDTO | null>(null);
    const [sendersOpen, setSendersOpen] = useState(false);
    // Failures and answers in place: a person's failed delete or unlink sits
    // at the top of their detail; cleanup answers on the "Sort them out" line.
    const [personFailure, setPersonFailure] = useState<{ id: string; kind: 'delete' | 'unlink'; name: string; principal: PrincipalDTO } | null>(null);
    const [cleanupFailed, setCleanupFailed] = useState(false);
    const [cleanupDone, setCleanupDone] = useState<'cleaned' | 'nothing' | null>(null);

    const refreshContacts = useCallback(async () => {
        try {
            setContactsAccess(await GetContactsAccessStatus());
        } catch {
            setContactsAccess(null);
        }
    }, []);

    // The overview orders people by open loops; the directory rows carry
    // what search, the groups and the actions need. A later failed re-read
    // keeps the page on screen and says nothing. A cache- or saved-seeded
    // page already counts as data.
    const hasData = useRef(initial !== null);
    const overviewRef = useRef<PeopleOverview | null>(initial?.overview ?? null);
    const entriesRef = useRef<PrincipalWithStatsDTO[]>(initial?.entries ?? []);
    // Reads overlap (Refresh, a Contacts sync, an edit); only the latest
    // one started may change the page.
    const readSeq = useRef(0);
    // Counts privacy withdrawals only, so a saved copy that arrives after one is dropped.
    const withdrawals = useRef(0);
    const reload = useCallback(async () => {
        const seq = ++readSeq.current;
        setRefreshing(true);
        setSelfReadReady(false);
        // The stamp is taken before any read starts; a save made under a
        // stamp that has since moved is refused.
        const stamp = await peopleReadStamp();
        const overviewRead = readWithRetry(async () => {
            const value = await settledPeopleRead("ListPeopleOverview", ListPeopleOverview);
            if (!value || !Array.isArray(value.open) || !Array.isArray(value.everyone)) throw new Error('Invalid people overview');
            return value;
        }).then((nextOverview) => {
            if (seq !== readSeq.current) return;
            overviewRef.current = nextOverview;
            setOverview(nextOverview);
            setLoad('ready');
            hasData.current = true;
            // Saved as soon as the overview is in, with the directory rows known so far.
            void savePeopleView(stamp, nextOverview, entriesRef.current);
            setRevision((n) => n + 1);
            return true;
        }).catch(() => {
            if (seq === readSeq.current && !hasData.current) setLoad('failed');
            return false;
        });
        const entriesRead = readWithRetry(async () => {
            const rows = await settledPeopleRead("ListPrincipalsWithStats", ListPrincipalsWithStats);
            if (!Array.isArray(rows)) throw new Error('Invalid people directory');
            return rows;
        }).then((rows) => {
            if (seq !== readSeq.current) return;
            entriesRef.current = rows;
            setEntries(entriesRef.current);
            if (overviewRef.current) void savePeopleView(stamp, overviewRef.current, entriesRef.current);
            return true;
        }).catch(() => false);
        const [overviewOK, entriesOK] = await Promise.all([overviewRead, entriesRead]);
        if (seq === readSeq.current) {
            setSelfReadReady(Boolean(overviewOK && entriesOK));
            setRefreshing(false);
        }
    }, []);

    // A prefetch, revalidation or invalidation of these reads re-reads the
    // page; the cache's single-flight keeps overlapping reads to one call.
    usePageQueryRefresh(['ListPeopleOverview', 'ListPrincipalsWithStats'], reload);
    usePageQueryRefresh(['GetContactsAccessStatus'], refreshContacts);

    // First open after launch: paint the saved directory while the reads run.
    // It lands only if no read has finished and no privacy withdrawal has
    // happened since the page opened; Go checks its stamp before serving it.
    useEffect(() => {
        if (initial) return;
        const withdrawalsAtOpen = withdrawals.current;
        let live = true;
        void readSavedPeopleView().then((saved) => {
            if (!saved || !live || hasData.current || withdrawals.current !== withdrawalsAtOpen) return;
            overviewRef.current = saved.overview;
            entriesRef.current = saved.entries;
            // A saved-seeded page counts as data: a failed read keeps it.
            hasData.current = true;
            setOverview(saved.overview);
            setEntries(saved.entries);
            setLoad('ready');
        });
        return () => { live = false; };
    }, []);

    // A privacy transition (Forget, a protection change, Closed Eyes,
    // background listening, a permission change, sign-out) withdraws the
    // pre-transition directory synchronously — rendered, remembered (search,
    // selection, the merge receipt's name) and in flight — before the sweep's
    // re-read restores what is still permitted.
    const withdraw = useCallback(() => {
        readSeq.current++;
        withdrawals.current++;
        hasData.current = false;
        overviewRef.current = null;
        entriesRef.current = [];
        clearSavedPeopleView();
        setOverview(null);
        setEntries([]);
        setSelfReadReady(false);
        setLoad('loading');
        setSelectedID(null);
        setKeyword('');
        setReceipt(null);
        setNotSame(null);
        setLandedID(null);
        setContactsAccess(null);
        setEditTarget(null);
        setPromoteTarget(null);
        setCreateOpen(false);
        setSelfCreateOpen(false);
        setDeleteTarget(null);
        setMergeSource(null);
        setCleanupOpen(false);
        setContactsImportOpen(false);
        setLinkTarget(null);
        setSendersOpen(false);
        setPersonFailure(null);
        setCleanupFailed(false);
        setCleanupDone(null);
    }, []);
    usePagePrivacyWithdrawal(withdraw);

    const retry = useCallback(() => {
        setLoad('loading');
        void reload();
    }, [reload]);

    useEffect(() => {
        void reload();
    }, [reload]);

    // A cache-seeded first paint already shows the page's content; the dev
    // page-switch timing otherwise waits on the network read to mark it.
    useEffect(() => {
        if (hasData.current) markPageContent('people');
    }, []);

    // Open loops are hidden while observing is paused; read again when
    // that changes so the headline and cards follow at once.
    const lastPaused = useRef<boolean | null>(null);
    useEffect(() => {
        const before = lastPaused.current;
        lastPaused.current = paused;
        if (before !== null && before !== paused) void reload();
    }, [paused, reload]);

    useEffect(() => {
        void refreshContacts();
        const off = EventsOn('contacts:updated', () => {
            void refreshContacts();
            void reload();
        });
        const offLoops = EventsOn('people:loops-changed', () => {
            pageQueryCache.invalidate(['ListPeopleOverview', 'ListPrincipalsWithStats']);
            void reload();
        });
        return () => {
            off();
            offLoops();
        };
    }, [refreshContacts, reload]);
    // The contacts sync announces itself ('contacts:updated'); this is only
    // the safety resync while the page shows, never while it is hidden.
    useBudgetedRefresh(() => void refreshContacts(), PEOPLE_RESYNC_MS, active);

    // Reopened after a quiet stretch: re-read whatever outlived its TTL.
    const wasActive = useRef(active);
    useEffect(() => {
        if (active && !wasActive.current) void reload();
        wasActive.current = active;
    }, [active, reload]);

    const byId = useMemo(() => new Map(entries.map((e) => [e.principal.id, e])), [entries]);
    const cardsById = useMemo(() => {
        const map = new Map<string, PersonCard>();
        for (const card of [...(overview?.open ?? []), ...(overview?.everyone ?? [])]) map.set(card.id, card);
        return map;
    }, [overview]);

    const groups = useMemo(() => {
        const everyone = overview?.everyone ?? [];
        const kindOf = (card: PersonCard) => byId.get(card.id)?.principal.kind ?? card.kind;
        return {
            // Anyone with an open loop stays here, unidentified or seen only in
            // content, so the headline and the list agree.
            open: overview?.open ?? [],
            others: everyone.filter((c) => kindOf(c) !== 'self' && !isUnnamedKind(kindOf(c))),
            self: entries.find((e) => e.principal.kind === 'self') ?? null,
            selfCard: [...(overview?.open ?? []), ...everyone].find((c) => c.kind === 'self') ?? null,
            // Anyone Isoform knows by name, open or not: decides which empty
            // hint "Everyone else" shows.
            hasNamed: entries.some((e) => e.principal.kind !== 'self' && !isUnnamedKind(e.principal.kind)),
        };
    }, [overview, entries, byId]);

    // Every sender Isoform could not match to a person: the page menu's
    // cleanup covers all of them.
    const anonymousCount = useMemo(() => entries.filter((e) => e.principal.kind === 'anonymous').length, [entries]);

    // The senders for the sort-out sheet: unidentified ones without an open
    // loop (those with one are in Open between you), most recently seen first.
    const senders = useMemo(
        () => {
            const ids = new Set(entries.filter((e) => e.principal.kind === 'anonymous').map((e) => e.principal.id));
            for (const card of cardsById.values()) if (card.kind === 'anonymous') ids.add(card.id);
            return [...ids]
                .filter((id) => (cardsById.get(id)?.loops.length ?? 0) === 0)
                .sort((a, b) => (cardsById.get(b)?.lastSeenAt ?? 0) - (cardsById.get(a)?.lastSeenAt ?? 0));
        },
        [entries, cardsById],
    );

    // The sheet closes itself once there is nobody left to sort out.
    useEffect(() => {
        if (senders.length === 0) setSendersOpen(false);
    }, [senders.length]);

    // Apply initialSelection after the first load finishes, and again when
    // a new one arrives; otherwise open on the first person with something
    // open, as the overview reads. `null` means nothing was applied yet.
    const appliedSelection = useRef<PeopleSelection | undefined | null>(null);
    useEffect(() => {
        if (load !== 'ready' || appliedSelection.current === initialSelection) return;
        appliedSelection.current = initialSelection;
        if (initialSelection === 'self') {
            if (groups.self || groups.selfCard) setSelectedID(groups.self?.principal.id ?? groups.selfCard!.id);
            else if (selfReadReady) setSelfCreateOpen(true);
            else { appliedSelection.current = null; }
            return;
        }
        if (initialSelection && (byId.has(initialSelection.personId) || cardsById.has(initialSelection.personId))) {
            setSelectedID(initialSelection.personId);
            return;
        }
        const first = groups.open[0] ?? groups.others[0];
        if (first) setSelectedID(first.id);
        else if (groups.self) setSelectedID(groups.self.principal.id);
    }, [load, initialSelection, groups, byId, cardsById, selfReadReady]);

    // A selected person who disappeared (deleted, merged away) leaves the
    // detail empty rather than pointing at a missing row.
    useEffect(() => {
        if (selectedID && load === 'ready' && !byId.has(selectedID) && !cardsById.has(selectedID)) setSelectedID(null);
    }, [selectedID, load, byId, cardsById]);

    const results = useMemo(() => {
        const q = keyword.trim().toLowerCase();
        if (!q) return null;
        return entries.filter((e) => {
            const p = e.principal;
            if (p.displayName.toLowerCase().includes(q) || p.contactName?.toLowerCase().includes(q)) return true;
            if (p.relationship.toLowerCase().includes(q)) return true;
            if ((p.aliases ?? []).some((a) => a.toLowerCase().includes(q))) return true;
            return (p.appHandles ?? []).some(
                (h) =>
                    h.app !== 'contacts' &&
                    h.app !== 'vcard' &&
                    (h.app.toLowerCase().includes(q) || h.displayHandle.toLowerCase().includes(q) || h.handleId.toLowerCase().includes(q)),
            );
        });
    }, [entries, keyword]);

    const handleDelete = useCallback(
        async (p: PrincipalDTO) => {
            setPersonFailure(null);
            try {
                await DeletePrincipal(p.id);
                invalidatePeopleReads();
                if (selectedID === p.id) setSelectedID(null);
                await reload();
            } catch {
                setPersonFailure({ id: p.id, kind: 'delete', name: p.contactName || p.displayName || tr('unnamed'), principal: p });
            }
        },
        [reload, selectedID, tr],
    );

    const handleCleanup = useCallback(async () => {
        setCleanupBusy(true);
        setCleanupFailed(false);
        try {
            const rep = await CleanupAnonymousPrincipals();
            invalidatePeopleReads();
            const changed = rep.renamed + rep.merged + rep.crossAppMerged + rep.mergedIntoNamed + rep.prunedNonChat + rep.prunedImplausible > 0;
            setCleanupDone(changed ? 'cleaned' : 'nothing');
            setCleanupOpen(false);
            await reload();
        } catch {
            // The confirmation stays open with the failure and Try again.
            setCleanupFailed(true);
        } finally {
            setCleanupBusy(false);
        }
    }, [reload]);

    const merged = useCallback(
        async (next: MergeReceipt, targetID?: string) => {
            setReceipt(next);
            setNotSame(null);
            invalidatePeopleReads();
            await reload();
            if (targetID) {
                setSelectedID(targetID);
                setLandedID(targetID);
            }
        },
        [reload],
    );

    // The Merge in a person's More menu takes the same reversible path as
    // the banner: the source is hidden behind an Undo receipt, not deleted.
    const handleManualMerge = useCallback(
        async (src: PrincipalDTO, dst: PrincipalDTO) => {
            const result = await MergePeople(dst.id, [src.id]);
            setMergeSource(null);
            await merged({ mergeId: result.mergeId, count: 2, name: result.target?.name || dst.contactName || dst.displayName || tr('unnamed') }, dst.id);
        },
        [merged, tr],
    );

    const unlinkContact = useCallback(
        async (p: PrincipalDTO) => {
            setPersonFailure(null);
            try {
                await UnlinkPersonContact(p.id);
                invalidatePeopleReads();
                await reload();
            } catch {
                setPersonFailure({ id: p.id, kind: 'unlink', name: p.contactName || p.displayName || tr('unnamed'), principal: p });
            }
        },
        [reload, tr],
    );

    const actions = useMemo<PersonActions>(
        () => ({
            onEdit: setEditTarget,
            onPromote: setPromoteTarget,
            onDelete: setDeleteTarget,
            onMerge: setMergeSource,
            onLink: setLinkTarget,
            onUnlink: (p) => void unlinkContact(p),
        }),
        [unlinkContact],
    );

    // Radix asks the dialog only while the page is the top layer, so an open
    // menu or a nested dialog keeps its Escape. The page steps back by
    // clearing the search; with nothing to clear, the dialog closes.
    const handleEscape = useCallback((): boolean => {
        if (!keyword) return false;
        setKeyword('');
        return true;
    }, [keyword]);

    useEffect(() => {
        if (!escapeRef) return;
        escapeRef.current = handleEscape;
        return () => {
            if (escapeRef.current === handleEscape) escapeRef.current = null;
        };
    }, [escapeRef, handleEscape]);

    const selectedEntry = selectedID ? byId.get(selectedID) : undefined;
    const selectedCard = selectedID ? cardsById.get(selectedID) : undefined;
    const knownCount = overview?.knownCount ?? entries.length;
    // Day zero: nobody known and no calendar people. Only You shows.
    const dayZero = load === 'ready' && knownCount === 0 && calendarPeople.length === 0 && groups.open.length === 0;
    const headline = dayZero
        ? tr('overview.dayZero')
        : calendarPeople.length > 0 && groups.open.length === 0
          ? tr('firstDay.headline', { count: calendarPeople.length })
          : overview
            ? headlineWords(tr, groups.open.filter((card) => card.loops.some(loopNeedsYou)).length, groups.open.length, paused)
            : tr('title');
    const shownCount = groups.open.length + groups.others.length;
    const subline = dayZero ? '' : calendarPeople.length > 0 ? tr('firstDay.more') : shownCount > 0 ? tr('overview.subline', { count: shownCount }) : '';
    const failureNotice =
        personFailure && personFailure.id === selectedID ? (
            <PeopleFailureLine
                text={personFailure.kind === 'delete' ? tr('errorDelete', { name: personFailure.name }) : tr('contactUnlinkFailed', { name: personFailure.name })}
                onRetry={() => (personFailure.kind === 'delete' ? handleDelete(personFailure.principal) : unlinkContact(personFailure.principal))}
            />
        ) : undefined;

    return (
        <PeopleDevToolsContext.Provider value={devToolsUnlocked}>
        <div className="flex h-full bg-background">
            <ScrollArea className="min-w-0 flex-1">
                <main className="flex flex-col gap-[26px] px-10 pb-14 pt-9">
                    <header className="flex flex-col gap-2">
                        <div className="flex items-center justify-between gap-3">
                            <span className="text-[11px] font-medium uppercase tracking-[0.08em] text-muted">{tr('title')}</span>
                            <div className="flex items-center gap-1.5">
                                <div className="relative">
                                    <Search className="pointer-events-none absolute left-2.5 top-1/2 size-3.5 -translate-y-1/2 text-muted" aria-hidden />
                                    <input
                                        type="search"
                                        value={keyword}
                                        onChange={(e) => setKeyword(e.target.value)}
                                        placeholder={tr('overview.searchLabel')}
                                        aria-label={tr('overview.searchLabel')}
                                        className="h-8 w-[264px] rounded-lg border border-border bg-panel pl-8 pr-3 text-[13px] text-ink outline-none placeholder:text-muted focus-visible:border-border-focus"
                                    />
                                </div>
                                <PageMoreMenu
                                    refreshing={refreshing}
                                    cleanupBusy={cleanupBusy}
                                    anonymousCount={anonymousCount}
                                    onAdd={() => setCreateOpen(true)}
                                    onContacts={() => setContactsImportOpen(true)}
                                    onCleanup={() => setCleanupOpen(true)}
                                    onRefresh={() => {
                                        // Who you know is built once a day; an explicit
                                        // Refresh builds it now, then re-reads it even
                                        // while the cached values are still fresh.
                                        void RefreshDerivedView('people').catch(() => undefined).finally(() => {
                                            pageQueryCache.invalidate(['ListPeopleOverview', 'ListPrincipalsWithStats']);
                                            void reload();
                                        });
                                    }}
                                />
                            </div>
                        </div>
                        {load === 'loading' ? (
                            <span aria-hidden className="mt-1 h-7 w-3/5 animate-pulse rounded-md bg-surface-active" />
                        ) : (
                            <h1 className="text-[26px] font-normal leading-[1.3] text-ink">
                                {headline}
                            </h1>
                        )}
                        {load === 'ready' && subline && <p className="text-[14px] text-muted">{subline}</p>}
                        {contactsAccess && (
                            <ContactsStatusLine
                                access={contactsAccess}
                                onOpenSheet={() => setContactsImportOpen(true)}
                                onChanged={() => {
                                    pageQueryCache.invalidate(['GetContactsAccessStatus']);
                                    return refreshContacts();
                                }}
                            />
                        )}
                    </header>

                    {load === 'loading' && <OverviewSkeleton label={tr('loading')} />}
                    {load === 'failed' && <PeopleFailureLine text={tr('overview.loadFailed')} onRetry={retry} />}
                    {load !== 'ready' && (
                        <section aria-labelledby="people-you-h" className="flex flex-col gap-1">
                            <h2 id="people-you-h" className="mb-2 text-[18px] font-semibold text-ink">{tr('groupSelf')}</h2>
                            <SelfRowPlaceholder />
                        </section>
                    )}

                    {load === 'ready' && results && (
                        <section aria-labelledby="people-results-h" className="flex flex-col gap-1">
                            <h2 id="people-results-h" className="mb-2 text-[18px] font-semibold text-ink">
                                {tr('overview.results')}
                            </h2>
                            {results.length === 0 ? (
                                <p className="text-[13px] text-muted">{tr('overview.noResults', { query: keyword.trim() })}</p>
                            ) : (
                                results.map((e) => (
                                    <PersonRow key={e.principal.id} entry={e} card={cardsById.get(e.principal.id)} selected={selectedID === e.principal.id} onSelect={pickPerson} />
                                ))
                            )}
                        </section>
                    )}

                    {load === 'ready' && !results && (
                        <>
                            {receipt && (
                                <MergeReceiptLine
                                    receipt={receipt}
                                    onUndone={async () => {
                                        setReceipt(null);
                                        setLandedID(null);
                                        invalidatePeopleReads();
                                        await reload();
                                    }}
                                />
                            )}
                            {notSame && (
                                <NotSameReceiptLine
                                    ids={notSame}
                                    onUndone={async () => {
                                        setNotSame(null);
                                        invalidatePeopleReads();
                                        await reload();
                                    }}
                                />
                            )}
                            <MergeBanners
                                groups={overview?.duplicates ?? []}
                                people={byId}
                                onMerged={(next, targetID) => void merged(next, targetID)}
                                onNotSame={(ids) => {
                                    setReceipt(null);
                                    setNotSame(ids);
                                    invalidatePeopleReads();
                                    void reload();
                                }}
                                onReview={pickPerson}
                            />

                            {groups.open.length > 0 && (
                                <section aria-labelledby="people-open-h" className="flex flex-col gap-3">
                                    <h2 id="people-open-h" className="text-[18px] font-semibold text-ink">
                                        {tr('overview.openHeading')}
                                    </h2>
                                    <div className="grid grid-cols-[repeat(auto-fill,minmax(min(100%,260px),1fr))] gap-3">
                                        {groups.open.map((card) => (
                                            <OpenCard key={card.id} card={card} entry={byId.get(card.id)} selected={selectedID === card.id} landed={landedID === card.id} onSelect={pickPerson} />
                                        ))}
                                    </div>
                                </section>
                            )}

                            <CalendarPeopleSection people={calendarPeople} known={(id) => byId.has(id)} onSelect={pickPerson} />

                            {groups.others.length > 0 && (
                                <section aria-labelledby="people-all-h" className="flex flex-col gap-1">
                                    <h2 id="people-all-h" className="mb-2 text-[18px] font-semibold text-ink">
                                        {tr('overview.everyoneHeading')}
                                    </h2>
                                    {groups.others.map((card) => (
                                        <PersonRow key={card.id} entry={byId.get(card.id)} card={card} selected={selectedID === card.id} landed={landedID === card.id} onSelect={pickPerson} />
                                    ))}
                                </section>
                            )}

                            {/* After a cleanup, the Sort them out line answers. */}
                            {cleanupDone ? (
                                <p role="status" className="text-[13px] text-muted">
                                    {cleanupDone === 'cleaned' ? tr('cleanupDone') : tr('cleanupNothing')}
                                </p>
                            ) : senders.length > 0 && (
                                <p className="flex flex-wrap items-center gap-x-2 text-[13px] text-muted">
                                    <span>{tr('overview.sortLine', { count: senders.length })}</span>
                                    <Button variant="link" size="sm" className="h-auto p-0 text-[13px]" onClick={() => setSendersOpen(true)}>
                                        {tr('overview.sortButton')}
                                    </Button>
                                </p>
                            )}

                            <section aria-labelledby="people-you-h" className="flex flex-col gap-1">
                                <h2 id="people-you-h" className="mb-2 text-[18px] font-semibold text-ink">
                                    {tr('groupSelf')}
                                </h2>
                                {groups.self || groups.selfCard ? (
                                    <PersonRow entry={groups.self ?? undefined} card={groups.selfCard ?? undefined} selected={selectedID === (groups.self?.principal.id ?? groups.selfCard?.id)} onSelect={pickPerson} />
                                ) : selfReadReady ? (
                                    <div className="flex flex-col items-start gap-2.5">
                                        <p className="text-[13px] leading-relaxed text-muted">{tr('emptySelf')}</p>
                                        <Button variant="secondary" size="sm" onClick={() => setSelfCreateOpen(true)}>
                                            <UserPlus className="size-3.5" />
                                            {tr('nameYourselfButton')}
                                        </Button>
                                    </div>
                                ) : <SelfRowPlaceholder />}
                            </section>
                            <SelfSenderGuesses
                                guesses={overview?.selfGuesses ?? []}
                                onMerged={async (result, targetID) => {
                                    await merged({ mergeId: result.mergeId, count: 2, name: result.target.name }, targetID);
                                }}
                                onNotSame={async (ids) => {
                                    setReceipt(null);
                                    setNotSame(ids);
                                    invalidatePeopleReads();
                                    await reload();
                                }}
                            />
                        </>
                    )}
                </main>
            </ScrollArea>

            <aside aria-label={tr('detail.label')} className="w-[min(400px,42%)] shrink-0 border-l border-border-subtle bg-background-accent">
                <ScrollArea className="h-full">
                    {/* The top padding leaves room for the dialog's close button. */}
                    <div className="px-6 pb-10 pt-12">
                        {selectedID ? (
                            <PersonDetailPanel
                                key={selectedID}
                                personId={selectedID}
                                card={selectedCard}
                                entry={selectedEntry}
                                revision={revision}
                                reloadToken={reloadToken}
                                actions={actions}
                                notice={failureNotice}
                            />
                        ) : load === 'ready' && knownCount > 0 ? (
                            <div className="flex flex-col gap-2 pt-10 text-center">
                                <h2 className="text-[14px] font-medium text-ink">{tr('overview.pickHint')}</h2>
                                <p className="text-[13px] leading-relaxed text-muted">{tr('overview.pickDesc')}</p>
                            </div>
                        ) : null}
                    </div>
                </ScrollArea>
            </aside>

            {createOpen && (
                <ContactEditorDialog
                    mode="create"
                    open={createOpen}
                    onOpenChange={(v) => setCreateOpen(v)}
                    onSaved={async (saved) => {
                        setCreateOpen(false);
                        invalidatePeopleReads();
                        await reload();
                        setSelectedID(saved.id);
                    }}
                />
            )}
            {selfCreateOpen && (
                <ContactEditorDialog
                    mode="self"
                    open={selfCreateOpen}
                    onOpenChange={(v) => setSelfCreateOpen(v)}
                    onSaved={async (saved) => {
                        setSelfCreateOpen(false);
                        invalidatePeopleReads();
                        await reload();
                        setSelectedID(saved.id);
                    }}
                />
            )}
            {editTarget && (
                <ContactEditorDialog
                    mode="edit"
                    initial={editTarget}
                    open={!!editTarget}
                    onOpenChange={(v) => !v && setEditTarget(null)}
                    onSaved={async () => {
                        setEditTarget(null);
                        invalidatePeopleReads();
                        await reload();
                    }}
                    onChanged={async () => {
                        invalidatePeopleReads();
                        await reload();
                    }}
                />
            )}
            {promoteTarget && (
                <ContactEditorDialog
                    mode="promote"
                    initial={promoteTarget}
                    open={!!promoteTarget}
                    onOpenChange={(v) => !v && setPromoteTarget(null)}
                    onChanged={async () => {
                        invalidatePeopleReads();
                        await reload();
                    }}
                    onSaved={async (saved) => {
                        setPromoteTarget(null);
                        invalidatePeopleReads();
                        await reload();
                        setSelectedID(saved.id);
                    }}
                />
            )}
            {linkTarget && (
                <ContactLinkDialog
                    personID={linkTarget.id}
                    onClose={() => setLinkTarget(null)}
                    onLinked={() => {
                        setLinkTarget(null);
                        invalidatePeopleReads();
                        void reload();
                    }}
                />
            )}
            <ContactsImportDialog
                open={contactsImportOpen}
                onOpenChange={setContactsImportOpen}
                onImported={() => {
                    invalidatePeopleReads();
                    pageQueryCache.invalidate(['GetContactsAccessStatus']);
                    void reload();
                    void refreshContacts();
                }}
            />
            <ConfirmDialog
                open={!!deleteTarget}
                onOpenChange={(v) => !v && setDeleteTarget(null)}
                title={tr('deleteTitle', { name: deleteTarget?.contactName || deleteTarget?.displayName || tr('unnamed') })}
                description={tr('deleteDescription')}
                confirmLabel={tr('deleteConfirm')}
                variant="danger"
                onConfirm={() => {
                    if (deleteTarget) void handleDelete(deleteTarget);
                }}
            />
            {mergeSource && <MergePrincipalDialog source={mergeSource} onOpenChange={(v) => !v && setMergeSource(null)} onConfirm={(dst) => handleManualMerge(mergeSource, dst)} />}
            <ConfirmDialog
                open={cleanupOpen}
                onOpenChange={(v) => {
                    if (!v && !cleanupBusy) {
                        setCleanupOpen(false);
                        setCleanupFailed(false);
                    }
                }}
                title={tr('cleanupTitle', { count: anonymousCount })}
                widthClass="w-[520px]"
                description={
                    <div className="flex flex-col gap-3">
                        <p>{tr('cleanupIntro')}</p>
                        <ul className="flex flex-col gap-1.5 pl-1">
                            {(['1', '2', '3'] as const).map((n) => (
                                <li key={n}>
                                    <span className="font-medium text-ink">{tr(`cleanupStep${n}Title`)}</span>
                                    {tr('cleanupStepJoin')}
                                    {tr(`cleanupStep${n}Body`)}
                                </li>
                            ))}
                        </ul>
                        <p className="text-[12px] text-muted">{tr('cleanupFootnote')}</p>
                    </div>
                }
                notice={cleanupFailed ? <PeopleFailureLine text={tr('cleanupFailed')} /> : undefined}
                confirmLabel={cleanupFailed ? tr('overview.retry') : tr('cleanupConfirm')}
                confirmDisabled={cleanupBusy}
                closeOnConfirm={false}
                onConfirm={() => void handleCleanup()}
            />
            <SendersSheet
                open={sendersOpen && senders.length > 0}
                onOpenChange={setSendersOpen}
                onCleanup={() => {
                    setSendersOpen(false);
                    setCleanupOpen(true);
                }}
            >
                {senders.map((id) => (
                    <PersonRow
                        key={id}
                        entry={byId.get(id)}
                        card={cardsById.get(id)}
                        selected={selectedID === id}
                        onSelect={(id) => {
                            // Same as before: a row selects the person, and their
                            // detail (name, merge, delete) opens beside the list.
                            setSelectedID(id);
                            setSendersOpen(false);
                        }}
                    />
                ))}
            </SendersSheet>
        </div>
        </PeopleDevToolsContext.Provider>
    );
}

// ── Page More menu ──

function PageMoreMenu({
    refreshing,
    cleanupBusy,
    anonymousCount,
    onAdd,
    onContacts,
    onCleanup,
    onRefresh,
}: {
    refreshing: boolean;
    cleanupBusy: boolean;
    anonymousCount: number;
    onAdd: () => void;
    onContacts: () => void;
    onCleanup: () => void;
    onRefresh: () => void;
}) {
    const { t } = useTranslation('people');
    const tr: Translate = (key, options) => t(key as never, options as never) as unknown as string;
    return (
        <DropdownMenu>
            <DropdownMenuTrigger asChild>
                <Button variant="outline" size="icon" aria-label={tr('menu.pageMore')} aria-busy={refreshing || cleanupBusy}>
                    {refreshing || cleanupBusy ? <Loader2 className="size-4 animate-spin" /> : <MoreHorizontal className="size-4" />}
                </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end">
                <DropdownMenuItem onSelect={onAdd}>
                    <UserPlus />
                    {tr('menu.addPerson')}
                </DropdownMenuItem>
                <DropdownMenuItem onSelect={onContacts}>
                    <Contact />
                    {tr('contactsSyncTitle')}
                </DropdownMenuItem>
                {anonymousCount > 0 && (
                    <DropdownMenuItem onSelect={onCleanup} disabled={cleanupBusy}>
                        <Sparkles />
                        {tr('menu.cleanup')}
                    </DropdownMenuItem>
                )}
                <DropdownMenuItem onSelect={onRefresh} disabled={refreshing}>
                    <RefreshCw />
                    {tr('menu.refresh')}
                </DropdownMenuItem>
            </DropdownMenuContent>
        </DropdownMenu>
    );
}

// ── Cards and rows ──

function roleWords(tr: Translate, t: (key: string) => string, exists: (key: string) => boolean, entry: PrincipalWithStatsDTO | undefined, card: PersonCard | undefined): string {
    const relationship = entry?.principal.relationship || card?.relationship || '';
    if (relationship) return relationshipLabel(t, exists, relationship);
    const kind = entry?.principal.kind ?? card?.kind;
    const messages = entry?.stats.totalMessages ?? 0;
    if (kind === 'self') return tr('kind.self');
    // An unnamed sender reads "3 messages"; nothing recorded says nothing.
    return messages > 0 ? tr('rowMessages', { count: messages }) : '';
}

function lastWords(tr: Translate, lang: string, card: PersonCard | undefined): string {
    if (!card || card.lastSeenAt <= 0) return '';
    const when = lastSeenWords(tr, lang, card.lastSeenAt);
    return card.lastSeenApp ? tr('card.last', { app: appLabel(tr, card.lastSeenApp), when }) : tr('card.lastNoApp', { when });
}

function OpenCard({ card, entry, selected, landed = false, onSelect }: { card: PersonCard; entry?: PrincipalWithStatsDTO; selected: boolean; landed?: boolean; onSelect: (id: string) => void }) {
    const { t, i18n } = useTranslation('people');
    const ref = useRef<HTMLButtonElement>(null);
    useEffect(() => {
        if (landed) ref.current?.scrollIntoView?.({ block: 'nearest' });
    }, [landed]);
    const tr: Translate = (key, options) => t(key as never, options as never) as unknown as string;
    const name = personName(entry, card.name || tr('unnamed'));
    const first = card.loops[0];
    const needsYou = first ? loopNeedsYou(first) : false;
    const context = first ? loopLine(tr, first, name, i18n.language) : '';
    const more = card.loops.length - 1;
    const last = lastWords(tr, i18n.language, card);
    return (
        <button
            ref={ref}
            type="button"
            onClick={() => onSelect(card.id)}
            aria-pressed={selected}
            data-landed={landed || undefined}
            className={cn(
                'flex flex-col gap-2.5 rounded-xl border border-border p-4 text-left text-ink transition-colors hover:border-border-focus focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent-2/40',
                selected ? 'bg-surface-active' : 'bg-panel',
                landed && 'answer-wash',
            )}
        >
            <span className="flex w-full items-center gap-2.5">
                <Avatar src={entry?.principal.contactPhoto || card.photo} alt="" fallback={name} className="h-[34px] w-[34px] text-[12px]" />
                <span className="flex min-w-0 flex-col">
                    <span className="truncate text-[14px] font-semibold">{name}</span>
                    <span className="truncate text-[12px] text-muted">{roleWords(tr, (key) => t(key as never) as unknown as string, (key) => i18n.exists(key), entry, card)}</span>
                </span>
            </span>
            {first && (
                <span
                    className={cn(
                        'inline-flex h-6 items-center self-start rounded-md border px-2 text-[12px] font-medium',
                        needsYou ? 'border-warning-border bg-warning-bg text-warning' : 'border-border bg-panel-warm text-muted',
                    )}
                >
                    {loopChip(tr, i18n.language, first)}
                </span>
            )}
            {(context || more > 0) && (
                <span className="text-[13px]">
                    {context}
                    {more > 0 && <span className="text-muted">{context ? ' · ' : ''}{tr('card.moreOpen', { count: more })}</span>}
                </span>
            )}
            {last && <span className="text-[12px] text-muted">{last}</span>}
        </button>
    );
}

function SelfRowPlaceholder() {
    return <div aria-hidden className="h-[53px] border-b border-border-divider" />;
}

function PersonRow({ entry, card, selected, landed = false, onSelect }: { entry?: PrincipalWithStatsDTO; card?: PersonCard; selected: boolean; landed?: boolean; onSelect: (id: string) => void }) {
    const { t, i18n } = useTranslation('people');
    // A merged person lands in the list with the wash, scrolled into view.
    const ref = useRef<HTMLButtonElement>(null);
    useEffect(() => {
        if (landed) ref.current?.scrollIntoView?.({ block: 'nearest' });
    }, [landed]);
    const tr: Translate = (key, options) => t(key as never, options as never) as unknown as string;
    const id = entry?.principal.id ?? card?.id ?? '';
    const name = personName(entry, card?.name || tr('unnamed'));
    const isSelf = (entry?.principal.kind ?? card?.kind) === 'self';
    return (
        <button
            ref={ref}
            type="button"
            onClick={() => onSelect(id)}
            aria-current={selected ? 'true' : undefined}
            data-landed={landed || undefined}
            className={cn(
                'flex w-full flex-wrap items-center gap-3 rounded-lg border-b border-border-divider px-2 py-2.5 text-left text-ink transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent-2/40',
                selected ? 'bg-surface-active' : 'hover:bg-surface-hover',
                landed && 'answer-wash',
            )}
        >
            <Avatar src={entry?.principal.contactPhoto || card?.photo} alt="" fallback={name} />
            <span className="min-w-0 flex-[1_1_160px] truncate text-[13px] font-medium">{isSelf ? tr('overview.selfNamed', { name }) : name}</span>
            <span className="min-w-0 flex-[2_1_200px] truncate text-[12.5px] text-muted">
                {!isSelf && roleWords(tr, (key) => t(key as never) as unknown as string, (key) => i18n.exists(key), entry, card)}
            </span>
            <span className="text-[12px] text-muted">{lastWords(tr, i18n.language, card)}</span>
        </button>
    );
}

/** Sort them out: the senders Isoform could not name, beside the list. Each
 *  row opens that person's detail on the page; the page menu's cleanup is
 *  one button here too. */
function SendersSheet({
    open,
    onOpenChange,
    onCleanup,
    children,
}: {
    open: boolean;
    onOpenChange: (open: boolean) => void;
    onCleanup: () => void;
    children: React.ReactNode;
}) {
    const { t } = useTranslation('people');
    const tr: Translate = (key, options) => t(key as never, options as never) as unknown as string;
    return (
        <Sheet open={open} onOpenChange={onOpenChange}>
            <SheetContent>
                <SheetHeader>
                    <div className="flex min-w-0 flex-col gap-1">
                        <SheetTitle>{tr('overview.sortTitle')}</SheetTitle>
                        <SheetDescription>{tr('overview.unidentifiedHint')}</SheetDescription>
                    </div>
                    <SheetCloseButton />
                </SheetHeader>
                <ScrollArea className="min-h-0 flex-1">
                    <div className="flex flex-col gap-1 px-4 pb-6">{children}</div>
                </ScrollArea>
                <div className="flex shrink-0 items-center justify-end border-t border-border-subtle px-6 py-4">
                    <Button variant="secondary" size="sm" onClick={onCleanup}>
                        {tr('menu.cleanup')}
                    </Button>
                </div>
            </SheetContent>
        </Sheet>
    );
}

function OverviewSkeleton({ label }: { label: string }) {
    return (
        <div aria-busy="true" aria-label={label} className="flex flex-col gap-3">
            <span className="h-4 w-40 animate-pulse rounded bg-surface-active" />
            <div className="grid grid-cols-[repeat(auto-fill,minmax(min(100%,260px),1fr))] gap-3">
                {[0, 1, 2].map((i) => (
                    <div key={i} className="flex flex-col gap-2.5 rounded-xl border border-border bg-panel p-4">
                        <span className="h-3 w-1/2 animate-pulse rounded bg-surface-active" />
                        <span className="h-5 w-2/5 animate-pulse rounded bg-surface-active" />
                        <span className="h-3 w-full animate-pulse rounded bg-surface-active" />
                        <span className="h-2.5 w-1/3 animate-pulse rounded bg-surface-active" />
                    </div>
                ))}
            </div>
        </div>
    );
}

// ── Merge into another person ──

function MergePrincipalDialog({
    source,
    onOpenChange,
    onConfirm,
}: {
    source: PrincipalDTO;
    onOpenChange: (open: boolean) => void;
    onConfirm: (target: PrincipalDTO) => Promise<void>;
}) {
    const { t } = useTranslation('people');
    const [target, setTarget] = useState<PrincipalDTO | null>(null);
    const [submitting, setSubmitting] = useState(false);
    const [failed, setFailed] = useState(false);

    const submit = async () => {
        if (!target) return;
        setSubmitting(true);
        setFailed(false);
        try {
            await onConfirm(target);
        } catch {
            // In place, in copper; Merge reads Try again.
            setFailed(true);
            setSubmitting(false);
        }
    };

    return (
        <Dialog open onOpenChange={onOpenChange}>
            <DialogContent className="w-[560px]">
                <DialogHeader className="items-start">
                    <div>
                        <DialogTitle>{t('mergeTitle', { name: source.contactName || source.displayName || t('unnamed') })}</DialogTitle>
                        <DialogDescription className="mt-1">{t('mergeDescription')}</DialogDescription>
                    </div>
                    <DialogCloseButton />
                </DialogHeader>
                <div className="flex flex-col gap-3 px-6 py-4">
                    <p className="text-[12px] font-medium text-muted">{t('mergePickerLabel')}</p>
                    <PrincipalPicker
                        selectedID={target?.id ?? null}
                        onSelect={(p) => {
                            if (p.id === source.id) return; // can't merge into self
                            setTarget(p);
                        }}
                        autoFocusSearch
                    />
                    {target && (
                        <p className="rounded-md border border-border bg-panel-warm px-3 py-2 text-[12px] text-muted">
                            {t('mergePreview', { src: source.contactName || source.displayName || t('unnamed'), dst: target.contactName || target.displayName || t('unnamed') })}
                        </p>
                    )}
                    {failed && <PeopleFailureLine text={t('merge.failed')} />}
                </div>
                <div className="flex items-center justify-end gap-2 px-6 pb-5">
                    <Button variant="outline" size="sm" onClick={() => onOpenChange(false)} disabled={submitting}>
                        {t('mergeCancel')}
                    </Button>
                    <Button size="sm" onClick={() => void submit()} disabled={!target || submitting || target.id === source.id} aria-busy={submitting}>
                        {failed ? t('overview.retry') : t('mergeConfirm')}
                    </Button>
                </div>
            </DialogContent>
        </Dialog>
    );
}
