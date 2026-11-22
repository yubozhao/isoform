import { useEffect, useId, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';
import { Check, ChevronDown, Copy, History, Languages, MoreHorizontal, PanelRightClose, Plus, Trash2, UserX } from 'lucide-react';
import type { main } from '../../wailsjs/go/models';
import {
    GetCommunicationDraftExamples,
    type AboutYouBelief,
    type CommunicationDraftExample,
    type EntryUses,
    type ReadableEvidence,
} from '@/lib/wails-shim-app';
import { GetEntryUses, GetMemoryEvidenceReadable, ListMemoryVersions } from '@/lib/page-queries';
import { pageQueryCache } from '@/lib/query-cache';
import { SaveMemoryEntry } from '../../wailsjs/go/main/App';
import { EventsOn } from '../../wailsjs/runtime/runtime';
import {
    beliefInSecondPerson,
    humanMemoryText,
    memoryApps,
    memoryDate,
    memoryKind,
    memoryConfirmed,
    memoryLabel,
    memoryPoints,
    calendarDaysAgo,
    relativeWords,
} from '@/lib/memory-content';
import { durationWords, USE_SURFACES } from '@/lib/about-you';
import { communicationDraftDetails, memoryArea } from '@/lib/communication';
import { onPrivacyReset } from '@/lib/privacy-reset';
import { Button } from '@/components/ui/button';
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '@/components/ui/collapsible';
import {
    DropdownMenu,
    DropdownMenuContent,
    DropdownMenuItem,
    DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu';
import { ConfidenceBar } from '@/components/about-you/belief-status';
import {
    BeliefChip,
    ClaimEditField,
    ReviewOutcome,
    answerWhen,
    answeredStatus,
    reviewOpen,
    type BeliefReview,
    type ReviewItem,
} from '@/components/about-you/new-this-week';

const DIMENSIONS = ['user_insight', 'topic_highlight', 'deep_user_model', 'agentic_memory', 'custom'];

/** The detail's More menu: support and maintenance actions that stay one
 *  menu away (copy ID, version history, translate, Wrong subject). */
export function MemoryMoreMenu({
    entry,
    onHistory,
    onTranslate,
    onReject,
    devTools = false,
}: {
    entry: main.MemoryEntry;
    onHistory: () => void;
    onTranslate: () => void;
    onReject: () => void;
    /** Developer tools on: Copy memory ID. */
    devTools?: boolean;
}) {
    const { t } = useTranslation(['memory', 'common']);
    return (
        <DropdownMenu>
            <DropdownMenuTrigger asChild>
                <Button variant="ghost" size="icon" aria-label={t('detail.more')}>
                    <MoreHorizontal className="h-4 w-4" />
                </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end">
                {devTools && (
                    <DropdownMenuItem onSelect={() => void navigator.clipboard?.writeText(entry.id).catch(() => undefined)}>
                        <Copy />
                        {t('support.copyId')}
                    </DropdownMenuItem>
                )}
                {entry.hasHistory && (
                    <DropdownMenuItem onSelect={onHistory}>
                        <History />
                        {t('header.versionHistory')}
                    </DropdownMenuItem>
                )}
                <DropdownMenuItem onSelect={onTranslate}>
                    <Languages />
                    {t('header.translate')}
                </DropdownMenuItem>
                <DropdownMenuItem onSelect={onReject}>
                    <UserX />
                    {t('aboutYou.detail.wrongSubject')}
                </DropdownMenuItem>
            </DropdownMenuContent>
        </DropdownMenu>
    );
}

function SkeletonLines({ widths }: { widths: string[] }) {
    return (
        <div aria-busy="true" className="flex flex-col gap-2.5">
            {widths.map((width, index) => (
                <span key={index} className="block h-3 rounded-md bg-surface-active motion-safe:animate-pulse" style={{ width }} />
            ))}
        </div>
    );
}

/** The selected belief: its points, why Isoform believes it, where it is
 *  used, and the actions to confirm, correct or forget it. */
/** One piece of evidence behind a belief: when it was seen, and what was said. */
function EvidenceArticle({ item, onReject }: { item: ReadableEvidence; onReject: () => void }) {
    const { t } = useTranslation('memory');
    const duration = durationWords(t, item.durationMs);
    const seen = t('aboutYou.evidence.seen', { when: relativeWords(t, item.at) });
    const said = beliefInSecondPerson(humanMemoryText(item.text), { nounPhrase: false });
    const words = item.appName
        ? t('aboutYou.evidence.from', { app: item.appName, text: said.replace(/^You(r?)\b/, 'you$1') })
        : said;
    return (
        <article className="flex flex-col gap-1 rounded-lg border border-border bg-panel px-3 py-2.5">
            <div className="flex items-center justify-between gap-2 text-xs text-muted">
                <span>{duration ? t('aboutYou.evidence.withDuration', { seen, duration }) : seen}</span>
                <Button size="sm" variant="link" className="h-auto px-0" onClick={onReject}>
                    {t('detail.notRight')}
                </Button>
            </div>
            <span className="text-[13px] leading-snug text-ink">{words}</span>
        </article>
    );
}

/** Evidence from the other saved copies of a card's idea, newest first. */
function CopyEvidence({ ids, onReject }: { ids: string[]; onReject: () => void }) {
    const [rows, setRows] = useState<ReadableEvidence[]>([]);
    const key = ids.join('\u0000');
    useEffect(() => {
        let current = true;
        Promise.all(ids.map((id) => GetMemoryEvidenceReadable(id).catch(() => [] as ReadableEvidence[])))
            .then((lists) => {
                if (current) setRows(lists.flat().sort((a, b) => b.at - a.at));
            });
        return () => { current = false; };
        // ids change with their joined key only.
        // eslint-disable-next-line react-hooks/exhaustive-deps
    }, [key]);
    return (
        <>
            {rows.map((item, index) => (
                <EvidenceArticle key={`copy-${item.at}-${index}`} item={item} onReject={onReject} />
            ))}
        </>
    );
}

export function MemoryDetail({
    entry,
    belief,
    editRequest,
    onSaved,
    onForget,
    onReject,
    review,
    onEditEnd,
    onClose,
    onHistory,
    onTranslate,
    onOpenBelief,
    onOpenActivitySession,
    devTools = false,
    translateFailed = false,
    copyIds = [],
}: {
    entry: main.MemoryEntry;
    belief?: AboutYouBelief;
    /** The other saved copies of this idea; their evidence joins the list (one card per idea). */
    copyIds?: string[];
    editRequest: number;
    onSaved: (entry: main.MemoryEntry) => void;
    onForget: () => void;
    onReject: () => void;
    /** That's me and Not quite, shared with New this week (answer feedback section 7). */
    review?: BeliefReview;
    /** An edit ended: saved (true) or cancelled (false). */
    onEditEnd?: (saved: boolean) => void;
    onClose?: () => void;
    onHistory?: () => void;
    onTranslate?: () => void;
    onOpenBelief?: (id: string) => void;
    onOpenActivitySession?: (sessionId: string) => void;
    /** Developer tools on: Copy memory ID and Technical details. */
    devTools?: boolean;
    /** Translate failed; the copper line offers Try again (onTranslate). */
    translateFailed?: boolean;
}) {
    const { t, i18n } = useTranslation(['memory', 'common']);
    const lastSave = useRef('');
    const sectionId = useId();
    const points = useMemo(() => memoryPoints(entry.content), [entry.content]);
    const [active, setActive] = useState<{
        pointId: string | null;
        content: string;
        entryId: string;
    } | null>(null);
    // A projected point has no persisted ID. Keep its captured identity and
    // source revision together; a refresh must never silently rebase a draft.
    const staleDraft =
        active !== null &&
        (active.content !== entry.content || active.entryId !== entry.id);
    const [draft, setDraft] = useState('');
    const [saving, setSaving] = useState(false);
    const [error, setError] = useState(false);
    const [evidence, setEvidence] = useState<ReadableEvidence[] | null>(null);
    const [evidenceError, setEvidenceError] = useState(false);
    const [uses, setUses] = useState<EntryUses | null>(null);
    const [usesError, setUsesError] = useState(false);
    const [reload, setReload] = useState(0);
    const [createdAt, setCreatedAt] = useState(0);
    const area = memoryArea(belief?.area ?? entry.area);
    const communication = area === 'communication';
    const [examples, setExamples] = useState<CommunicationDraftExample[] | null>(null);
    const [examplesError, setExamplesError] = useState(false);
    const [examplesReload, setExamplesReload] = useState(0);

    // However the editor goes away (the detail closes, another belief opens,
    // Escape), an edit that was not saved ends as cancelled.
    const editingRef = useRef(false);
    editingRef.current = active !== null;
    const onEditEndRef = useRef(onEditEnd);
    onEditEndRef.current = onEditEnd;
    useEffect(() => () => {
        if (editingRef.current) onEditEndRef.current?.(false);
    }, []);

    useEffect(() => {
        if (editRequest > 0 && active === null) {
            setActive({
                pointId: points[0]?.id ?? null,
                content: entry.content,
                entryId: entry.id,
            });
            setDraft(points[0]?.text ?? '');
            setError(false);
        }
    }, [editRequest]);

    // Evidence and uses reload on memory updates and when observation is
    // paused; a stale request never restores rows after a privacy change.
    // A warm cache (retained selection, repeated deep link) paints the last
    // read at once and revalidates behind it.
    useEffect(() => {
        let current = true;
        setEvidence(pageQueryCache.peek<ReadableEvidence[]>('GetMemoryEvidenceReadable', [entry.id]) ?? null);
        setEvidenceError(false);
        const cachedUses = pageQueryCache.peek<EntryUses>('GetEntryUses', ['memory', entry.id]);
        setUses(cachedUses ? { total: cachedUses.total ?? 0, lastAt: cachedUses.lastAt ?? 0, surfaces: cachedUses.surfaces ?? [] } : null);
        setUsesError(false);
        GetMemoryEvidenceReadable(entry.id)
            .then((rows) => {
                if (current) setEvidence(rows ?? []);
            })
            .catch(() => {
                if (current) setEvidenceError(true);
            });
        GetEntryUses('memory', entry.id)
            .then((result) => {
                if (current) setUses({ total: result?.total ?? 0, lastAt: result?.lastAt ?? 0, surfaces: result?.surfaces ?? [] });
            })
            .catch(() => {
                if (current) setUsesError(true);
            });
        const invalidate = () => {
            current = false;
            setReload((value) => value + 1);
        };
        const cleanups = ['closed-eyes:changed', 'memory:updated'].map((name) =>
            EventsOn(name, invalidate),
        );
        return () => {
            current = false;
            cleanups.forEach((cleanup) => cleanup());
        };
    }, [entry.id, entry.content, reload]);

    // Examples reload with the evidence invalidation above and always
    // re-read through the backend policy. A privacy reset (protecting an
    // app, Closed Eyes, Forget, or the account sign-out boundary) withdraws
    // the shown draft text in the event turn and cancels the pending read,
    // so a response composed before the change cannot reappear.
    useEffect(() => {
        let generation = 0;
        setExamples(null);
        setExamplesError(false);
        if (!communication) return;
        const load = () => {
            const ticket = ++generation;
            GetCommunicationDraftExamples(entry.id)
                .then((rows) => {
                    if (ticket === generation) setExamples(rows ?? []);
                })
                .catch(() => {
                    if (ticket === generation) setExamplesError(true);
                });
        };
        load();
        const offPrivacy = onPrivacyReset(() => {
            generation += 1;
            setExamples(null);
            setExamplesError(false);
            setExamplesReload((value) => value + 1);
        });
        return () => {
            generation += 1;
            offPrivacy();
        };
    }, [communication, entry.id, examplesReload, reload]);

    // The first snapshot dates an entry that has no belief read yet.
    useEffect(() => {
        if (belief?.firstLearnedAt) return;
        let current = true;
        const apply = (versions: main.MemoryVersion[] | null | undefined) => {
            const times = (versions ?? [])
                .map((version) => version.timestampMs)
                .filter((time) => time > 0);
            setCreatedAt(times.length ? Math.min(...times) : entry.updatedAt || entry.modTime);
        };
        // A warm cache answers at once; the read revalidates behind it.
        const cached = pageQueryCache.peek<main.MemoryVersion[]>('ListMemoryVersions', [entry.id]);
        if (cached) apply(cached);
        ListMemoryVersions(entry.id)
            .then((versions) => {
                if (current) apply(versions);
            })
            .catch(() => {});
        return () => {
            current = false;
        };
    }, [entry.id, entry.updatedAt, entry.modTime, belief?.firstLearnedAt]);

    const startEdit = (pointId: string | null) => {
        if (saving || active !== null) return;
        setActive({ pointId, content: entry.content, entryId: entry.id });
        setDraft(points.find((point) => point.id === pointId)?.text ?? '');
        setError(false);
    };
    const save = async (content: string) => {
        if (saving) return;
        setSaving(true);
        setError(false);
        lastSave.current = content;
        try {
            const saved = await SaveMemoryEntry(entry.id, content);
            editingRef.current = false;
            setActive(null);
            setDraft('');
            onSaved(saved);
            onEditEnd?.(true);
        } catch {
            setError(true);
        } finally {
            setSaving(false);
        }
    };
    const savePoint = () => {
        if (active === null || staleDraft || !draft.trim()) return;
        const text = draft.trim().replace(/\n+/g, ' ');
        if (active.pointId === null) {
            void save(`${active.content.trimEnd()}\n- ${text}\n`);
        } else {
            const point = memoryPoints(active.content).find(
                (point) => point.id === active.pointId,
            );
            if (!point) return;
            void save(
                active.content.slice(0, point.start) +
                    text +
                    active.content.slice(point.end),
            );
        }
    };
    const forgetPoint = (pointId: string) => {
        if (saving || active !== null) return;
        if (points.length === 1) {
            onForget();
            return;
        }
        const point = points.find((point) => point.id === pointId);
        if (!point) return;
        const siblings = points.filter(
            (other) => other.lineStart === point.lineStart,
        );
        const start = siblings.length === 1 ? point.lineStart : point.start;
        const end = siblings.length === 1 ? point.lineEnd : point.end;
        void save(entry.content.slice(0, start) + entry.content.slice(end));
    };
    const evidenceTimes = (evidence ?? [])
        .map((item) => item.at)
        .filter((at) => at > 0);
    const firstLearned = belief?.firstLearnedAt || (evidenceTimes.length
        ? Math.min(...evidenceTimes, ...(createdAt > 0 ? [createdAt] : []))
        : createdAt);
    const apps = memoryApps(entry.apps ?? []);
    const appWords = new Intl.ListFormat(i18n.language, {
        style: 'long',
        type: 'conjunction',
    }).format(apps);
    const lastChange = entry.updatedAt || entry.modTime;
    const when = memoryDate(t, lastChange, i18n.language);
    const updated =
        calendarDaysAgo(lastChange) === 0
            ? t(
                  new Date(lastChange).getHours() < 12
                      ? 'detail.updatedMorning'
                      : new Date(lastChange).getHours() < 18
                        ? 'detail.updatedAfternoon'
                        : 'detail.updatedEvening',
              )
            : t('detail.updated', {
                  when:
                      calendarDaysAgo(lastChange) > 2
                          ? t('relativeTime.daysAgo', { count: calendarDaysAgo(lastChange) })
                          : when,
              });
    const hasWhy = evidenceError || evidence === null || evidence.length > 0;
    const range = firstLearned
        ? calendarDaysAgo(firstLearned) === 0
            ? t('detail.workToday')
            : calendarDaysAgo(firstLearned) === 1
              ? t('detail.lastTwoDays')
              : t('detail.since', { count: calendarDaysAgo(firstLearned) })
        : t('detail.yourWork');
    const label = memoryLabel(entry, t);
    const dimension = DIMENSIONS.includes(entry.dimension) ? entry.dimension : 'uncategorized';
    // The newest readable moment; otherwise (none readable, or the read
    // failed) the session the memory links to, as the old Recent list's jump
    // did. Activity applies its own gates.
    const evidenceSession = (evidence ?? []).find((item) => item.activitySessionId)?.activitySessionId;
    const firstSession = evidenceSession ?? (evidence === null && !evidenceError ? undefined : entry.activitySessionIds?.[0]);
    // Without the About you read, the file itself says whether it is confirmed.
    const ownStatus = belief?.status ?? (memoryConfirmed(entry) ? 'confirmed' : 'inferred');
    const result = review?.results[entry.id];
    const status = answeredStatus(result, ownStatus);
    const answerItem: ReviewItem = {
        id: entry.id,
        text: beliefInSecondPerson(humanMemoryText(points[0]?.text ?? '')) || memoryLabel(entry, t),
        where: '',
        communication,
        portraitCandidate: belief?.portraitCandidate,
        status: ownStatus,
    };
    const asking = result?.kind === 'asking' || result?.kind === 'saving' || result?.kind === 'answerFailed';
    const answered = !reviewOpen(result) && !asking;
    const cancelEdit = () => {
        editingRef.current = false;
        setActive(null);
        onEditEnd?.(false);
    };

    const editor = (
        <div className="rounded-xl border border-border-focus bg-panel p-3 space-y-3">
            {staleDraft && (
                <p role="alert" className="text-xs leading-6 text-muted">
                    {t('detail.staleDraft')}
                </p>
            )}
            <textarea
                autoFocus
                aria-label={t('detail.pointLabel')}
                value={draft}
                disabled={saving}
                onChange={(event) => setDraft(event.target.value)}
                onKeyDown={(event) => {
                    // The page's Escape leaves an open editor to cancel itself.
                    if (event.key !== 'Escape') return;
                    event.stopPropagation();
                    if (!saving) cancelEdit();
                }}
                className="w-full min-h-20 resize-y bg-transparent text-sm leading-relaxed text-ink focus:outline-none"
            />
            <div className="flex gap-2">
                <Button size="sm" onClick={savePoint} disabled={saving || staleDraft || !draft.trim()}>
                    {t(saving ? 'header.saving' : 'header.save')}
                </Button>
                <Button size="sm" variant="outline" onClick={cancelEdit} disabled={saving}>
                    {t('common:cancel')}
                </Button>
            </div>
            <p className="text-xs text-muted">{t('editor.attribution')}</p>
        </div>
    );

    return (
        <div className="flex flex-col gap-5 pb-10 font-sans">
            <header className="flex flex-col gap-2">
                <div className="flex items-center gap-2">
                    <span className="text-[11px] font-medium uppercase tracking-[0.08em] text-muted">
                        {communication ? t('aboutYou.areas.communication') : t(`kinds.${memoryKind(entry)}`)}
                    </span>
                    <span className="ml-auto flex shrink-0 items-center gap-1">
                        {onHistory && onTranslate && (
                            <MemoryMoreMenu entry={entry} onHistory={onHistory} onTranslate={onTranslate} onReject={onReject} devTools={devTools} />
                        )}
                        {onClose && (
                            <Button variant="ghost" size="icon" aria-label={t('aboutYou.detail.close')} onClick={onClose}>
                                <PanelRightClose className="h-4 w-4" />
                            </Button>
                        )}
                    </span>
                </div>
                <h2 className="m-0 text-lg font-semibold leading-snug text-ink break-words">{label}</h2>
                {belief && (
                    <span className="flex items-center gap-2 text-xs text-muted">
                        <ConfidenceBar level={belief.confidence} />
                        <BeliefChip status={status} />
                    </span>
                )}
            </header>

            <section aria-label={t('detail.points')} className="space-y-1.5">
                {staleDraft && editor}
                {points.map((point) =>
                    !staleDraft && active?.pointId === point.id ? (
                        <div key={point.id}>{editor}</div>
                    ) : (
                        <div
                            key={point.id}
                            className="group flex items-start gap-1 rounded-lg hover:bg-surface-hover transition-colors"
                        >
                            <button
                                type="button"
                                disabled={saving || active !== null}
                                onClick={() => startEdit(point.id)}
                                className="flex-1 min-w-0 rounded-lg px-3 py-2 text-left text-[13px] leading-relaxed text-ink break-words focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-border-focus"
                            >
                                {beliefInSecondPerson(humanMemoryText(point.text))}
                            </button>
                            <button
                                type="button"
                                disabled={saving || active !== null}
                                onClick={() => forgetPoint(point.id)}
                                aria-label={t('detail.forgetPoint', {
                                    point: humanMemoryText(point.text),
                                })}
                                title={t('row.forget')}
                                className="mt-1 mr-1 rounded-lg p-1.5 text-muted hover:text-ink hover:bg-surface-hover focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-border-focus opacity-100 [@media(hover:hover)]:opacity-0 group-hover:opacity-100 group-focus-within:opacity-100"
                            >
                                <Trash2 className="h-3.5 w-3.5" />
                            </button>
                        </div>
                    ),
                )}
                {!staleDraft && active?.pointId === null ? (
                    editor
                ) : (
                    <Button
                        size="sm"
                        variant="link"
                        disabled={saving || active !== null}
                        onClick={() => startEdit(null)}
                    >
                        <Plus className="h-3.5 w-3.5" />
                        {t('detail.addPoint')}
                    </Button>
                )}
                {!points.length && active === null && (
                    <p className="px-3 text-sm text-muted">{t('row.empty')}</p>
                )}
                {error && (
                    <p role="alert" className="m-0 flex flex-wrap items-center gap-2 px-3 text-[12.5px] text-copper">
                        <span>{t('aboutYou.detail.saveFailed')}</span>
                        <button type="button" className="text-accent hover:underline" disabled={saving} onClick={() => void save(lastSave.current)}>
                            {t('aboutYou.retry')}
                        </button>
                    </p>
                )}
            </section>

            <p className="m-0 text-xs leading-6 text-muted" data-testid="memory-provenance">
                {t(
                    entry.source === 'manual'
                        ? 'detail.fromYou'
                        : apps.length
                          ? 'detail.provenanceApps'
                          : 'detail.provenance',
                    { range, apps: appWords },
                )}{' '}
                {updated}
            </p>

            {hasWhy ? (
            <section aria-labelledby={`${sectionId}-why`} className="flex flex-col gap-2">
                <h3 id={`${sectionId}-why`} className="m-0 text-[13px] font-semibold text-ink">{t('detail.why')}</h3>
                {evidenceError ? (
                    <div role="alert" className="flex items-center gap-3 text-[13px] text-copper">
                        <span className="flex-1">{t('aboutYou.detail.evidenceFailed')}</span>
                        <Button size="sm" variant="secondary" onClick={() => setReload((value) => value + 1)}>
                            {t('aboutYou.retry')}
                        </Button>
                    </div>
                ) : evidence === null ? (
                    <SkeletonLines widths={['60%', '90%', '75%']} />
                ) : evidence.length === 0 ? (
                    null
                ) : (
                    <>
                        {evidence.map((item, index) => (
                            <EvidenceArticle key={`${item.at}-${index}`} item={item} onReject={onReject} />
                        ))}
                        {copyIds.length > 0 && <CopyEvidence ids={copyIds} onReject={onReject} />}
                    </>
                )}
                {!communication && firstSession && onOpenActivitySession && (
                    <Button size="sm" variant="link" className="self-start px-0" onClick={() => onOpenActivitySession(firstSession)}>
                        {t(evidenceSession ? 'aboutYou.detail.seeInActivity' : 'aboutYou.detail.openSession')}
                    </Button>
                )}
            </section>
            ) : (
                !communication && firstSession && onOpenActivitySession && (
                    <Button size="sm" variant="link" className="self-start px-0" onClick={() => onOpenActivitySession(firstSession)}>
                        {t(evidenceSession ? 'aboutYou.detail.seeInActivity' : 'aboutYou.detail.openSession')}
                    </Button>
                )
            )}

            <section aria-labelledby={`${sectionId}-uses`} className="flex flex-col gap-2">
                <h3 id={`${sectionId}-uses`} className="m-0 text-[13px] font-semibold text-ink">{t('aboutYou.detail.usesTitle')}</h3>
                {usesError ? (
                    <div role="alert" className="flex items-center gap-3 text-[13px] text-copper">
                        <span className="flex-1">{t('aboutYou.detail.usesFailed')}</span>
                        <Button size="sm" variant="secondary" onClick={() => setReload((value) => value + 1)}>
                            {t('aboutYou.retry')}
                        </Button>
                    </div>
                ) : uses === null ? (
                    <SkeletonLines widths={['70%', '50%']} />
                ) : uses.total === 0 ? (
                    <p className="m-0 text-xs text-muted">{t('aboutYou.detail.usesNone')}</p>
                ) : (
                    <ul className="m-0 flex list-none flex-col gap-1.5 p-0">
                        {USE_SURFACES.map((surface) => uses.surfaces.find((row) => row.surface === surface))
                            .filter((row): row is EntryUses['surfaces'][number] => !!row && row.count > 0)
                            .map((row) => (
                                <li key={row.surface} className="flex items-baseline gap-2.5 text-[13px] text-ink">
                                    <span aria-hidden="true" className="h-1.5 w-1.5 flex-none translate-y-[-1px] rounded-full bg-accent" />
                                    <span>
                                        {t(`aboutYou.uses.${row.surface}`, { count: row.count })}
                                        {row.lastAt > 0 && (
                                            <span className="text-muted">{' · '}{t('aboutYou.uses.last', { when: relativeWords(t, row.lastAt) })}</span>
                                        )}
                                    </span>
                                </li>
                            ))}
                    </ul>
                )}
            </section>

            {communication && (examples === null || examplesError || examples.length > 0) && (
                <section aria-labelledby={`${sectionId}-drafts`} className="flex flex-col gap-2">
                    <h3 id={`${sectionId}-drafts`} className="m-0 text-[13px] font-semibold text-ink">{t('aboutYou.communication.draftsTitle')}</h3>
                    {examplesError ? (
                        <div role="alert" className="flex items-center gap-3 text-[13px] text-copper">
                            <span className="flex-1">{t('aboutYou.communication.draftsFailed')}</span>
                            <Button size="sm" variant="secondary" onClick={() => setExamplesReload((value) => value + 1)}>
                                {t('aboutYou.retry')}
                            </Button>
                        </div>
                    ) : examples === null ? (
                        <SkeletonLines widths={['55%', '90%', '70%']} />
                    ) : examples.map((example) => {
                        const details = communicationDraftDetails(t, 'aboutYou.communication', example);
                        return (
                            <article key={example.id} className="flex flex-col gap-2 rounded-lg border border-border bg-panel p-3">
                                <div className="flex flex-col gap-1 text-[12.5px]">
                                    <span className="text-ink">
                                        {example.app
                                            ? t('aboutYou.communication.toApp', { to: example.to, app: example.app })
                                            : t('aboutYou.communication.toOnly', { to: example.to })}
                                    </span>
                                    <span className="whitespace-pre-wrap break-words leading-relaxed text-ink">{example.text}</span>
                                </div>
                                <div className="flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-muted">
                                    <span>{t('aboutYou.communication.written', { name: example.recipientName, details })}</span>
                                    {onOpenBelief && (
                                        <Button
                                            size="sm"
                                            variant="link"
                                            className="ml-auto h-auto px-0"
                                            onClick={() => onOpenBelief(entry.id)}
                                        >
                                            {t('aboutYou.communication.change')}
                                        </Button>
                                    )}
                                </div>
                            </article>
                        );
                    })}
                </section>
            )}

            <div className="flex flex-col gap-2.5 border-t border-border-subtle pt-3.5">
                {review && asking && <ClaimEditField item={answerItem} review={review} />}
                {review && answered && <ReviewOutcome item={answerItem} review={review} />}
                {/* Later: when the person answered, and what Isoform does with it. */}
                {!asking && !answered && status === 'confirmed' && (belief?.confirmedAt ?? 0) > 0 && (
                    <p className="m-0 flex items-center gap-1.5 text-[13px] text-muted">
                        <Check className="h-3.5 w-3.5 flex-none" aria-hidden="true" />
                        {t('aboutYou.detail.confirmedWhen', { when: answerWhen(t, i18n.language, belief!.confirmedAt) })}
                    </p>
                )}
                {!asking && !answered && status === 'not_quite' && (belief?.notQuiteAt ?? 0) > 0 && (
                    <p className="m-0 text-[13px] text-muted">
                        {t('aboutYou.detail.notQuiteWhen', { when: answerWhen(t, i18n.language, belief!.notQuiteAt ?? 0) })}
                    </p>
                )}
                <div className="flex flex-wrap gap-2">
                    {/* The same answers as the claim's line: Yes, No, Edit (redesign v1.0.0 section 1). */}
                    {review && !answered && !asking && status !== 'confirmed' && (
                        <Button size="sm" onClick={() => void review.confirm(entry.id, status === 'not_quite')}>
                            {t('aboutYou.review.yes')}
                        </Button>
                    )}
                    {review && !answered && !asking && status !== 'not_quite' && (
                        <Button size="sm" variant="secondary" onClick={() => void review.answerNotQuite(entry.id, '')}>
                            {t('aboutYou.review.no')}
                        </Button>
                    )}
                    {review && !answered && !asking && (
                        <Button size="sm" variant="outline" onClick={() => review.ask(entry.id)}>
                            {t('aboutYou.review.edit')}
                        </Button>
                    )}
                    <Button size="sm" variant="secondary" onClick={() => startEdit(points[0]?.id ?? null)} disabled={saving || active !== null}>
                        {t('aboutYou.detail.editWording')}
                    </Button>
                    <Button size="sm" variant="secondary" onClick={onForget}>
                        {t('forget.action')}
                    </Button>
                </div>
                {result?.kind === 'confirmFailed' && (
                    <p role="alert" className="m-0 text-[12.5px] text-copper">{t('aboutYou.review.confirmFailed')}</p>
                )}
                {translateFailed && (
                    <p role="alert" className="m-0 flex flex-wrap items-center gap-2 text-[12.5px] text-copper">
                        <span>{t('aboutYou.detail.translateFailed')}</span>
                        {onTranslate && (
                            <button type="button" className="text-accent hover:underline" onClick={onTranslate}>{t('aboutYou.retry')}</button>
                        )}
                    </p>
                )}
            </div>

            {devTools && <Collapsible className="text-xs text-muted">
                <CollapsibleTrigger className="group/tech flex items-center gap-1.5 rounded-md text-[12.5px] text-muted hover:text-ink focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-border-focus">
                    {t('aboutYou.technical.title')}
                    <ChevronDown className="h-3.5 w-3.5 transition-transform group-data-[state=open]/tech:rotate-180" />
                </CollapsibleTrigger>
                <CollapsibleContent>
                    <dl className="mt-3 grid grid-cols-[auto_1fr] gap-x-6 gap-y-2">
                        <dt>{t('detail.kind')}</dt>
                        <dd className="m-0 text-ink">{t(`kinds.${memoryKind(entry)}`)}</dd>
                        <dt>{t('detail.dimension')}</dt>
                        <dd className="m-0 text-ink">{t(`dimensions.${dimension as 'custom'}`)}</dd>
                        {entry.source && (
                            <>
                                <dt>{t('aboutYou.technical.source')}</dt>
                                <dd className="m-0 text-ink">{sourceLabel(t, entry.source)}</dd>
                            </>
                        )}
                        {belief && (
                            <>
                                <dt>{t('aboutYou.technical.confidence')}</dt>
                                <dd className="m-0 text-ink">
                                    {t('aboutYou.technical.confidenceValue', {
                                        level: belief.confidence,
                                        sessions: t('aboutYou.technical.sessions', { count: belief.sessionCount }),
                                        days: t('aboutYou.technical.days', { count: belief.dayCount }),
                                    })}
                                </dd>
                            </>
                        )}
                        <dt>{t('detail.firstLearned')}</dt>
                        <dd className="m-0 text-ink">{memoryDate(t, firstLearned, i18n.language)}</dd>
                        <dt>{t('detail.lastUpdated')}</dt>
                        <dd className="m-0 text-ink">{when}</dd>
                    </dl>
                </CollapsibleContent>
            </Collapsible>}
        </div>
    );
}

/** A readable label for a memory Source value. Keeps unknown values as-is. */
export function sourceLabel(t: TFunction<'memory'>, source: string): string {
    switch (source) {
        case 'activity':
        case 'manual':
        case 'user_edit':
            return t(`sources.${source}` as never) as string;
        default:
            return source.charAt(0).toUpperCase() + source.slice(1);
    }
}
