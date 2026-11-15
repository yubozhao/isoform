// "These profiles look like one person": a banner per duplicate group the
// computer found on hard signals (the same app account under different
// names, the same email). Merge into one hides the other profiles behind an
// Undo receipt; Review each lists them so each can be opened; Not the same
// says they are different people, and Isoform never suggests them again.
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { MergePeople, NotSamePeople, UndoMerge, UndoNotSamePeople, type DuplicateGroup, type PrincipalWithStatsDTO } from '@/lib/wails-shim-app';
import { Avatar } from '../ui/avatar';
import { Button } from '../ui/button';
import { cn } from '@/lib/utils';
import { appLabel, listWords, type Translate } from './people-format';
import { PeopleFailureLine } from './people-failure';

export interface MergeReceipt {
    mergeId: string;
    count: number;
    name: string;
}

/** At most this many banners show before "Show N more". */
const VISIBLE_GROUPS = 2;

export function personName(entry: PrincipalWithStatsDTO | undefined, fallback: string): string {
    return entry?.principal.contactName || entry?.principal.displayName || fallback;
}

export function MergeBanners({
    groups,
    people,
    onMerged,
    onNotSame,
    onReview,
}: {
    groups: DuplicateGroup[];
    people: Map<string, PrincipalWithStatsDTO>;
    onMerged: (receipt: MergeReceipt, targetID: string) => void;
    /** Not the same was saved for these profiles. */
    onNotSame: (ids: string[]) => void;
    onReview: (id: string) => void;
}) {
    const { t } = useTranslation('people');
    const tr: Translate = (key, options) => t(key as never, options as never) as unknown as string;
    const [showAll, setShowAll] = useState(false);
    if (groups.length === 0) return null;
    const shown = showAll ? groups : groups.slice(0, VISIBLE_GROUPS);
    return (
        <div className="flex flex-col gap-3">
            {shown.map((group) => (
                <MergeBanner key={group.id} group={group} people={people} onMerged={onMerged} onNotSame={onNotSame} onReview={onReview} />
            ))}
            {!showAll && groups.length > VISIBLE_GROUPS && (
                <Button variant="link" size="sm" className="self-start" onClick={() => setShowAll(true)}>
                    {tr('merge.showMore', { count: groups.length - VISIBLE_GROUPS })}
                </Button>
            )}
        </div>
    );
}

function MergeBanner({
    group,
    people,
    onMerged,
    onNotSame,
    onReview,
}: {
    group: DuplicateGroup;
    people: Map<string, PrincipalWithStatsDTO>;
    onMerged: (receipt: MergeReceipt, targetID: string) => void;
    onNotSame: (ids: string[]) => void;
    onReview: (id: string) => void;
}) {
    const { t, i18n } = useTranslation('people');
    const tr: Translate = (key, options) => t(key as never, options as never) as unknown as string;
    const [state, setState] = useState<'idle' | 'running' | 'failed' | 'linked' | 'notSameFailed'>('idle');
    const [reviewing, setReviewing] = useState(false);

    const unnamed = tr('unnamed');
    const targetName = personName(people.get(group.suggestedTargetId), unnamed);
    const sources = group.memberIds.filter((id) => id !== group.suggestedTargetId);
    const names = group.memberIds.map((id) => `“${personName(people.get(id), unnamed)}”`);
    const quoted = names.length > 3 ? tr('merge.namesMore', { names: listWords(i18n.language, names.slice(0, 3)), count: names.length - 3 }) : listWords(i18n.language, names);
    const reason = group.reason === 'same_email'
        ? tr('merge.reasonEmail', { names: quoted })
        : tr(group.reason === 'same_name' ? 'merge.reasonName' : 'merge.reasonHandle', { app: appLabel(tr, group.app), names: quoted });

    const merge = async () => {
        setState('running');
        try {
            const result = await MergePeople(group.suggestedTargetId, sources);
            onMerged({ mergeId: result.mergeId, count: group.memberIds.length, name: result.target?.name || targetName }, group.suggestedTargetId);
        } catch (err: unknown) {
            // The store refuses to fold a profile linked to a contact and
            // says so; keep that reason instead of a generic line.
            setState(/unlink the contact/i.test(String(err)) ? 'linked' : 'failed');
        }
    };

    const notSame = async () => {
        setState('running');
        try {
            await NotSamePeople(group.memberIds);
            onNotSame(group.memberIds);
        } catch {
            setState('notSameFailed');
        }
    };

    // While a merge or Not the same runs, the buttons keep their labels.
    const running = state === 'running';
    return (
        <section
            aria-label={tr('merge.title', { count: group.memberIds.length, name: targetName })}
            className="flex flex-col gap-3 rounded-xl border border-border bg-panel px-4 py-3.5 shadow-[var(--shadow-card)]"
        >
            <div className="flex flex-wrap items-center gap-x-4 gap-y-3">
                <div className="flex" aria-hidden="true">
                    {group.memberIds.slice(0, 2).map((id, i) => {
                        const entry = people.get(id);
                        return <Avatar key={id} src={entry?.principal.contactPhoto} alt="" fallback={personName(entry, '?')} className={cn('h-[30px] w-[30px] border-2 border-panel text-[11px]', i > 0 && '-ml-2')} />;
                    })}
                    {group.memberIds.length > 2 && (
                        <span className="-ml-2 flex h-[30px] w-[30px] items-center justify-center rounded-full border-2 border-panel bg-surface-active text-[11px] font-medium text-ink">
                            +{group.memberIds.length - 2}
                        </span>
                    )}
                </div>
                <div className="flex min-w-0 flex-[1_1_320px] flex-col gap-0.5">
                    <span className="text-[14px] font-semibold text-ink">{targetName}</span>
                    <span className="text-[13px] text-muted">{reason}</span>
                </div>
                <div className="flex items-center gap-1">
                    <Button size="sm" onClick={() => void merge()} disabled={running} aria-busy={running}>
                        {state === 'failed' ? tr('overview.retry') : tr('merge.mergeIntoOne')}
                    </Button>
                    <Button variant="outline" size="sm" onClick={() => setReviewing((v) => !v)} aria-expanded={reviewing} disabled={running}>
                        {tr('merge.reviewEach')}
                    </Button>
                    <Button variant="outline" size="sm" onClick={() => void notSame()} disabled={running}>
                        {tr('merge.notSame')}
                    </Button>
                </div>
            </div>
            {state === 'failed' && <PeopleFailureLine text={tr('merge.failed')} />}
            {state === 'linked' && <PeopleFailureLine text={tr('merge.failedLinked')} />}
            {state === 'notSameFailed' && <PeopleFailureLine text={tr('merge.notSameFailed')} onRetry={notSame} />}
            {reviewing && (
                <ul className="flex flex-col border-t border-border-divider pt-2">
                    {group.memberIds.map((id) => {
                        const entry = people.get(id);
                        const messages = entry?.stats.totalMessages ?? 0;
                        return (
                            <li key={id}>
                                <button
                                    type="button"
                                    onClick={() => onReview(id)}
                                    className="flex w-full items-center gap-3 rounded-lg px-2 py-2 text-left transition-colors hover:bg-surface-hover focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent-2/40"
                                >
                                    <Avatar src={entry?.principal.contactPhoto} alt="" fallback={personName(entry, '?')} size="md" />
                                    <span className="min-w-0 flex-1 truncate text-[13px] font-medium text-ink">{personName(entry, unnamed)}</span>
                                    {messages > 0 && <span className="text-[12px] text-muted">{tr('rowMessages', { count: messages })}</span>}
                                    {id === group.suggestedTargetId && <span className="text-[12px] text-muted">{tr('merge.keeps')}</span>}
                                </button>
                            </li>
                        );
                    })}
                </ul>
            )}
        </section>
    );
}

/** After a merge: what happened, with Undo until the page closes. */
export function MergeReceiptLine({ receipt, onUndone }: { receipt: MergeReceipt; onUndone: () => void | Promise<void> }) {
    const { t } = useTranslation('people');
    const tr: Translate = (key, options) => t(key as never, options as never) as unknown as string;
    const [state, setState] = useState<'idle' | 'undoing' | 'failed'>('idle');
    const undo = async () => {
        setState('undoing');
        try {
            await UndoMerge(receipt.mergeId);
            await onUndone();
        } catch {
            setState('failed');
        }
    };
    const undoing = state === 'undoing';
    return (
        <div role="status" className="flex flex-wrap items-center gap-2 text-[13px] text-muted">
            <span>{tr('merge.merged', { name: receipt.name })}</span>
            <Button variant="outline" size="sm" onClick={() => void undo()} disabled={undoing} aria-busy={undoing}>
                {state === 'failed' ? tr('overview.retry') : tr('merge.undo')}
            </Button>
            {state === 'failed' && <PeopleFailureLine className="basis-full" text={tr('merge.undoFailed')} />}
        </div>
    );
}

/** After Not the same: what Isoform will do, with Undo until the page closes. */
export function NotSameReceiptLine({ ids, onUndone }: { ids: string[]; onUndone: () => void | Promise<void> }) {
    const { t } = useTranslation('people');
    const tr: Translate = (key, options) => t(key as never, options as never) as unknown as string;
    const [state, setState] = useState<'idle' | 'undoing' | 'failed'>('idle');
    const undo = async () => {
        setState('undoing');
        try {
            await UndoNotSamePeople(ids);
            await onUndone();
        } catch {
            setState('failed');
        }
    };
    const undoing = state === 'undoing';
    // The banner itself becomes the answer, where Not the same was pressed.
    return (
        <section role="status" className="answer-line flex flex-wrap items-center gap-2 rounded-xl border border-border bg-panel px-4 py-3.5 text-[13px] text-ink shadow-[var(--shadow-card)]">
            <span className="flex-1">{tr('merge.notSameDone')}</span>
            <Button variant="outline" size="sm" onClick={() => void undo()} disabled={undoing} aria-busy={undoing}>
                {state === 'failed' ? tr('overview.retry') : tr('merge.undo')}
            </Button>
            {state === 'failed' && <PeopleFailureLine className="basis-full" text={tr('merge.notSameUndoFailed')} />}
        </section>
    );
}
