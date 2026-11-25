// The person detail beside the Who you know overview: who they are, Brief me
// and Draft a reply in place, what is open, your history together, shared
// topics and where Isoform saw them, then everything else folded below. A
// section shows only when it has something in it.
import { useCallback, useEffect, useRef, useState, type ReactNode } from 'react';
import { useChatOn } from '@/hooks/useFeatureSwitches';
import { useTranslation } from 'react-i18next';
import { GitMerge, Link, MoreHorizontal, Pencil, Trash2, Unlink, UserPlus } from 'lucide-react';
import { type PersonCard, type PersonDetail, type PrincipalDTO, type PrincipalWithStatsDTO } from '@/lib/wails-shim-app';
import { GetPersonDetail } from '@/lib/page-queries';
import { pageQueryCache } from '@/lib/query-cache';
import { Avatar } from '../ui/avatar';
import { Button } from '../ui/button';
import {
    DropdownMenu,
    DropdownMenuContent,
    DropdownMenuItem,
    DropdownMenuSeparator,
    DropdownMenuTrigger,
} from '../ui/dropdown-menu';
import { appLabel, dayWords, listWords, loopChip, loopLine, loopNeedsYou, readWithRetry, relationshipLabel, togetherWords, type Translate } from './people-format';
import { PeopleFailureLine, usePeopleDevTools } from './people-failure';
import { PersonBriefBlock, PersonReplyDraft } from './person-brief';
import { DetailFold, PersonDossier, PersonTechnicalDetails, RecentMessagesList, VoiceFingerprint } from './person-sections';
import { PromiseLine, isPromise, sortPromises } from './open-between-you';
import { PersonNext, PersonVoiceLine } from './person-voice';

export interface PersonActions {
    onEdit: (p: PrincipalDTO) => void;
    onPromote: (p: PrincipalDTO) => void;
    onDelete: (p: PrincipalDTO) => void;
    onMerge: (p: PrincipalDTO) => void;
    onLink: (p: PrincipalDTO) => void;
    onUnlink: (p: PrincipalDTO) => void;
}

/** Quiet re-reads after a failed read, before the failure line shows (N45). */
const quietRetryWaits = [400, 1200, 3000];

export function PersonDetailPanel({
    personId,
    card,
    entry,
    revision,
    reloadToken = 0,
    actions,
    notice,
    onAnswered,
}: {
    personId: string;
    card?: PersonCard;
    entry?: PrincipalWithStatsDTO;
    /** Bumped by the page after a change, so the detail reads again. */
    revision: number;
    /** Bumped when the selected person is clicked again: the detail reads again. */
    reloadToken?: number;
    actions: PersonActions;
    /** A failed action on this person (delete, unlink), at the top. */
    notice?: ReactNode;
    /** A promise here was answered: the page re-reads who you know. */
    onAnswered?: () => void;
}) {
    const { t, i18n } = useTranslation('people');
    const tr: Translate = (key, options) => t(key as never, options as never) as unknown as string;
    const [detail, setDetail] = useState<PersonDetail | null>(
        () => pageQueryCache.peek<PersonDetail>('GetPersonDetail', [personId]) ?? null,
    );
    const [failed, setFailed] = useState(false);
    const [attempt, setAttempt] = useState(0);
    // How many quiet re-reads a failed re-read has used since the last success.
    const quietRetries = useRef(0);
    const reread = useCallback(() => {
        quietRetries.current = 0;
        setAttempt((n) => n + 1);
    }, []);
    const lastRevision = useRef(revision);
    const lastReload = useRef(reloadToken);
    // How many successes have landed for this person. A failure only shows
    // when no success landed after its read started, so a stale error never
    // overrides a later success (N53).
    const landed = useRef(0);
    // The detail on screen, read by the load effect's failure path.
    const shown = useRef<PersonDetail | null>(null);
    useEffect(() => {
        shown.current = detail;
    }, [detail]);
    const devTools = usePeopleDevTools();
    const [draftFor, setDraftFor] = useState<string | null>(null);
    useEffect(() => setDraftFor(null), [personId]);

    // A new person paints from the cache when warm (a retained selection or a
    // repeated deep link); a re-read keeps what is shown.
    useEffect(() => {
        setDetail(pageQueryCache.peek<PersonDetail>('GetPersonDetail', [personId]) ?? null);
        setFailed(false);
        quietRetries.current = 0;
    }, [personId]);

    // A value for this person that lands in the cache from another read paints
    // here too, so a failure of this panel's own read never outlives it (N53).
    useEffect(() => pageQueryCache.subscribe((name, args) => {
        if (name !== 'GetPersonDetail' || (args && args[0] !== personId)) return;
        const value = pageQueryCache.peek<PersonDetail>('GetPersonDetail', [personId]);
        if (!value) return;
        landed.current++;
        quietRetries.current = 0;
        setDetail(value);
        setFailed(false);
    }), [personId]);

    useEffect(() => {
        let alive = true;
        let quiet: ReturnType<typeof setTimeout> | undefined;
        // The page's revision bump (a people change) means re-read from the
        // source, not a fresh-cache answer; the page-queries event map also
        // clears this name on contacts/privacy events. Clicking the selected
        // person again (reloadToken) re-reads the same way.
        const revisionChanged = revision !== lastRevision.current;
        lastRevision.current = revision;
        const reloaded = reloadToken !== lastReload.current;
        lastReload.current = reloadToken;
        if (attempt > 0 || revisionChanged || reloaded) pageQueryCache.invalidate(['GetPersonDetail'], true, [personId]);
        const started = landed.current;
        readWithRetry(() => GetPersonDetail(personId))
            .then((next) => {
                if (!alive) return;
                landed.current++;
                quietRetries.current = 0;
                setDetail(next);
                setFailed(false);
            })
            .catch(() => {
                // A failure a later success overtook says nothing (N53).
                if (!alive || landed.current !== started) return;
                // A failed re-read keeps the detail on screen and tries again
                // quietly; the failure line shows only when the tries run out (N45).
                // A first read that fails shows the failure line as before.
                const wait = shown.current ? quietRetryWaits[quietRetries.current] : undefined;
                if (wait !== undefined) {
                    quietRetries.current++;
                    quiet = setTimeout(() => {
                        if (alive) setAttempt((n) => n + 1);
                    }, wait);
                    return;
                }
                setFailed(true);
            });
        return () => {
            alive = false;
            clearTimeout(quiet);
        };
    }, [personId, revision, attempt, reloadToken]);

    const principal = entry?.principal;
    const kind = principal?.kind ?? card?.kind ?? '';
    const isSelf = kind === 'self';
    const name = principal?.contactName || detail?.name || card?.name || tr('unnamed');
    // The chat finds a person by the name stored on the profile.
    const relationship = detail?.relationship || principal?.relationship || card?.relationship || '';
    const role = relationship
        ? relationshipLabel((key) => t(key as never) as unknown as string, (key) => i18n.exists(key), relationship)
        : kind && kind !== 'anonymous'
          ? tr(`kind.${kind}`)
          : '';
    const aliases = (principal?.aliases ?? []).filter((a) => a && a !== name);

    // The header and its More menu come from the directory row, so Edit,
    // link, merge and Delete stay reachable while the detail loads or fails.
    const header = (
        <div className="flex items-center gap-3">
            <Avatar src={principal?.contactPhoto || card?.photo} alt="" fallback={name} className="h-12 w-12 text-[15px]" />
            <div className="flex min-w-0 flex-1 flex-col gap-0.5">
                <h2 className="truncate text-[18px] font-semibold leading-tight text-ink" title={name}>
                    {name}
                </h2>
                {role && <span className="text-[12.5px] text-muted">{role}</span>}
                {aliases.length > 0 && <span className="truncate text-[12px] text-muted">{tr('detail.alsoKnownAs', { names: listWords(i18n.language, aliases) })}</span>}
            </div>
            {principal && <PersonMoreMenu principal={principal} name={name} actions={actions} />}
        </div>
    );

    const chatOn = useChatOn();
    if (!detail) {
        return (
            <article aria-label={name} className="flex flex-col gap-5">
                {header}
                {notice}
                {failed ? (
                    <PeopleFailureLine text={tr('detail.loadFailed')} onRetry={reread} />
                ) : (
                    <DetailSkeleton label={tr('detail.loading')} />
                )}
            </article>
        );
    }

    const replyOwed = detail.loops.find((loop) => loop.kind === 'reply_owed');
    // Brief me shows only when Isoform has something to say about them.
    const canBrief =
        !isSelf &&
        (detail.loops.length > 0 || detail.together.length > 0 || !!detail.researchSummary || detail.messageCount > 0 || (entry?.stats.totalMessages ?? 0) > 0);
    const summary = detail.researchSummary || (isSelf ? tr('detail.selfSummary') : '');
    // Promises are answered under Still open?; other open loops (a reply you
    // owe, a send waiting on them, back in touch) stay listed as before.
    const promises = sortPromises(detail.loops.filter(isPromise).map((loop) => ({ loop, personId, name })));
    const otherLoops = detail.loops.filter((loop) => !isPromise(loop));
    // "Said in Chat" notes are hidden while Chat is (scope cut A4); they stay
    // stored, and Forget everything still removes them.
    const chatNotes = chatOn ? detail.chatNotes ?? [] : [];
    const promiseAnswered = () => {
        reread();
        onAnswered?.();
    };

    return (
        <article aria-label={name} className="flex flex-col gap-5">
            {header}
            {notice}

            {summary && <p className={detail.researchSummary ? 'text-[14px] leading-[1.55] text-ink' : 'text-[13px] leading-[1.55] text-muted'}>{summary}</p>}

            {(canBrief || (!isSelf && replyOwed)) && (
                <div className="flex flex-wrap items-start gap-2">
                    {canBrief && <PersonBriefBlock key={personId} personId={personId} />}
                    {!isSelf && replyOwed && draftFor === null && (
                        <Button variant="secondary" size="sm" onClick={() => setDraftFor(replyOwed.id)}>
                            {tr('detail.draft')}
                        </Button>
                    )}
                </div>
            )}

            {!isSelf && <PersonNext personId={personId} />}

            {promises.length > 0 && (
                <section aria-labelledby={`person-promises-${personId}`} className="flex flex-col gap-1">
                    <h3 id={`person-promises-${personId}`} className="m-0 text-[13px] font-semibold text-ink">{tr('who.open')}</h3>
                    <div className="-mx-3 flex flex-col">
                        {promises.map((entry) => (
                            <PromiseLine key={entry.loop.id} entry={entry} onAnswered={promiseAnswered} />
                        ))}
                    </div>
                </section>
            )}

            {otherLoops.length > 0 && (
                <section className="flex flex-col gap-2">
                    {promises.length === 0 && <h3 className="text-[13px] font-semibold text-ink">{tr('detail.openLoops')}</h3>}
                    <ul className="flex flex-col gap-2">
                        {otherLoops.map((loop) => (
                            <li key={`${loop.kind}:${loop.id}`} className="flex flex-col text-[14px] text-ink">
                                <span className="flex items-start gap-2.5">
                                    <span aria-hidden className="mt-[7px] size-1.5 shrink-0 rounded-full bg-muted" />
                                    <span className="flex min-w-0 flex-col">
                                        {loopNeedsYou(loop) && <span className="mb-1 inline-flex h-6 items-center self-start rounded-md border border-warning-border bg-warning-bg px-2 text-[12px] font-medium text-warning">{loopChip(tr, i18n.language, loop)}</span>}
                                        <span>{loopLine(tr, loop, name, i18n.language)}</span>
                                        {loop.kind === 'waiting_on_them' && (
                                            <span className="text-[12px] text-muted">{loopChip(tr, i18n.language, loop)}</span>
                                        )}
                                    </span>
                                </span>
                                {draftFor === loop.id && loop.kind === 'reply_owed' && (
                                    <PersonReplyDraft personId={personId} momentId={loop.id} onClose={() => setDraftFor(null)} />
                                )}
                            </li>
                        ))}
                    </ul>
                </section>
            )}

            {(detail.togetherFailed || detail.together.length > 0) && (
                <section className="flex flex-col gap-1.5">
                    <h3 className="text-[13px] font-semibold text-ink">{tr('detail.together')}</h3>
                    {detail.togetherFailed && <PeopleFailureLine text={tr('detail.togetherFailed')} onRetry={reread} />}
                    <ul className="flex flex-col">
                        {detail.together.map((item, i) => (
                            <li key={`${item.kind}:${item.at}:${i}`} className="flex gap-3 border-b border-border-divider py-[7px]">
                                <span className="w-16 shrink-0 text-[12px] text-muted">{dayWords(tr, i18n.language, item.at)}</span>
                                <span className="min-w-0 text-[13px] text-ink">{togetherWords(tr, item, name)}</span>
                            </li>
                        ))}
                    </ul>
                </section>
            )}

            {(detail.sharedTopics.length > 0 || detail.seenIn.length > 0) && (
                <section className="flex flex-col gap-1.5">
                    {detail.sharedTopics.length > 0 && (
                        <>
                            <h3 className="text-[13px] font-semibold text-ink">{tr('detail.sharedTopics')}</h3>
                            <ul className="flex flex-wrap gap-1.5">
                                {detail.sharedTopics.map((topic) => (
                                    <li key={topic.id} className="inline-flex h-[26px] items-center rounded-full border border-border bg-panel px-2.5 text-[12.5px] text-ink">
                                        {topic.name}
                                    </li>
                                ))}
                            </ul>
                        </>
                    )}
                    {detail.seenIn.length > 0 && (
                        <span className="text-[12px] text-muted">{tr('detail.seenIn', { apps: listWords(i18n.language, detail.seenIn.map((app) => appLabel(tr, app))) })}</span>
                    )}
                </section>
            )}

            {(principal?.notes || chatNotes.length > 0) && (
                <section className="flex flex-col gap-1.5">
                    <h3 className="text-[13px] font-semibold text-ink">{tr('notesHeading')}</h3>
                    {principal?.notes && <p className="whitespace-pre-wrap text-[13px] leading-relaxed text-ink">{principal.notes}</p>}
                    {/* Corrections said in Chat: read-only here, never in the editor's save; Chat's Undo removes them. */}
                    {chatNotes.map((note) => (
                        <div key={note.id} data-chat-note className="flex flex-col gap-0.5">
                            <span className="text-[12px] text-muted">{tr('who.chatNote')}</span>
                            <p className="m-0 whitespace-pre-wrap text-[13px] leading-relaxed text-ink">{note.text}</p>
                        </div>
                    ))}
                </section>
            )}

            {!isSelf && <PersonVoiceLine personId={personId} name={name} />}

            <div className="flex flex-col">
                {isSelf && principal && (
                    <DetailFold label={tr('voicePrintHeading')} defaultOpen>
                        <VoiceFingerprint principalId={principal.id} />
                    </DetailFold>
                )}
                {entry && entry.stats.recentMessages.length > 0 && (
                    <DetailFold label={tr('detail.recentMessages')} count={entry.stats.recentMessages.length}>
                        <RecentMessagesList stats={entry.stats} />
                    </DetailFold>
                )}
                {!isSelf && (
                    <DetailFold label={tr('dossierHeading')}>
                        <PersonDossier principalId={personId} name={name} onChanged={reread} />
                    </DetailFold>
                )}
                {devTools && principal && (
                    <DetailFold label={tr('technical.heading')}>
                        <PersonTechnicalDetails principal={principal} stats={entry?.stats} />
                    </DetailFold>
                )}
            </div>
        </article>
    );
}

function DetailSkeleton({ label }: { label: string }) {
    return (
        <div aria-busy="true" aria-label={label} className="flex flex-col gap-5">
            <div className="flex flex-col gap-2">
                <span className="h-3 w-full animate-pulse rounded bg-surface-active" />
                <span className="h-3 w-11/12 animate-pulse rounded bg-surface-active" />
                <span className="h-3 w-2/3 animate-pulse rounded bg-surface-active" />
            </div>
        </div>
    );
}

/** The person's own actions: edit or name, contact link, merge, delete. */
function PersonMoreMenu({ principal, name, actions }: { principal: PrincipalDTO; name: string; actions: PersonActions }) {
    const { t } = useTranslation('people');
    const tr: Translate = (key, options) => t(key as never, options as never) as unknown as string;
    const isSelf = principal.kind === 'self';
    return (
        <DropdownMenu>
            <DropdownMenuTrigger asChild>
                <Button variant="outline" size="icon" aria-label={tr('menu.personMore', { name })} className="shrink-0 border-transparent">
                    <MoreHorizontal className="size-4" />
                </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end">
                {principal.kind === 'anonymous' ? (
                    <DropdownMenuItem onSelect={() => actions.onPromote(principal)}>
                        <UserPlus />
                        {tr('promote')}
                    </DropdownMenuItem>
                ) : (
                    <DropdownMenuItem onSelect={() => actions.onEdit(principal)}>
                        <Pencil />
                        {tr('menu.edit')}
                    </DropdownMenuItem>
                )}
                {principal.contactLinked ? (
                    <DropdownMenuItem onSelect={() => actions.onUnlink(principal)}>
                        <Unlink />
                        {tr('menu.unlinkContact')}
                    </DropdownMenuItem>
                ) : (
                    <DropdownMenuItem onSelect={() => actions.onLink(principal)}>
                        <Link />
                        {tr('contactLinkTitle')}
                    </DropdownMenuItem>
                )}
                {!isSelf && !principal.contactLinked && (
                    <DropdownMenuItem onSelect={() => actions.onMerge(principal)}>
                        <GitMerge />
                        {tr('mergeButtonTitle')}
                    </DropdownMenuItem>
                )}
                {!isSelf && (
                    <>
                        <DropdownMenuSeparator />
                        <DropdownMenuItem onSelect={() => actions.onDelete(principal)}>
                            <Trash2 />
                            {tr('menu.delete')}
                        </DropdownMenuItem>
                    </>
                )}
            </DropdownMenuContent>
        </DropdownMenu>
    );
}
