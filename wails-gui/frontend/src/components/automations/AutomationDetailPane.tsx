import { useEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { ArrowLeft, Loader2, MoreHorizontal, Pencil, Play, Trash2 } from 'lucide-react';
import {
    DropdownMenu,
    DropdownMenuContent,
    DropdownMenuItem,
    DropdownMenuSeparator,
    DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu';
import { ConfirmDialog } from '@/components/confirm-dialog';
import { cn } from '@/lib/utils';
import { CronJobDetail, type CronJobDetailHandle } from './CronJobDetail';
import { AskForChange } from './ScheduleAsk';
import { useFeatureSwitches } from '@/hooks/useFeatureSwitches';
import { createScheduleTextLabels, cronToHumanReadable, type ScheduleTextLabels } from './schedule-utils';
import type { CronJobState, CronRunState, RefusalOptions } from '@/hooks/useCronJobs';
import { FailureLine } from '@/components/tasks/FailureLine';
import { measureTasks, recordTasksAction } from '@/lib/tasks-telemetry';
import type { StreamItem } from '@/types';

interface AutomationDetailPaneProps {
    job: CronJobState;
    onClose: () => void;
    onToggleEnabled: (id: string, enabled: boolean, opts?: RefusalOptions) => Promise<boolean> | void;
    /** Resolves false when the delete didn't go through; the dialog stays. */
    onDelete: (id: string, opts?: RefusalOptions) => Promise<boolean> | void;
    onRunNow: (id: string, opts?: RefusalOptions) => Promise<boolean> | void;
    onUpdate: (id: string, patch: Record<string, unknown>) => Promise<void>;
    loadRuns: (jobId: string) => Promise<CronRunState[]>;
    loadRunLogs: (jobId: string, runId: string) => Promise<StreamItem[]>;
    sourceSessionLabel?: string;
    onNavigateToSourceSession?: () => void;
    /** Opens the main chat at an answer asked about a run. */
    onOpenChatMessage?: (sessionId: string, itemId: string) => void;
    wideMode?: boolean;
}

/** Split a trailing "(Zone/Name)" timezone group off a backend schedule
 *  description, e.g. "0 9 * * * (Asia/Shanghai)" → { expr: "0 9 * * *", tz: "Asia/Shanghai" }. */
function splitScheduleTimezone(description: string): { expr: string; tz: string } {
    const match = description.match(/^(.*?)\s*\(([^()]+)\)\s*$/);
    if (match) return { expr: match[1].trim(), tz: match[2].trim() };
    return { expr: description.trim(), tz: '' };
}

function scheduleSummary(job: CronJobState, labels: ScheduleTextLabels): string {
    if (job.scheduleKind === 'manual') return labels.runOnDemand;
    // scheduleDescription arrives from the backend as a raw cron expression with
    // an appended "(Timezone)" suffix — feeding that straight into the parser
    // yields six tokens and falls through to the raw string. Prefer the clean
    // cron expression in scheduleConfig and surface the timezone in plain words.
    const { expr: descExpr, tz } = splitScheduleTimezone(job.scheduleDescription || '');
    const cfg = job.scheduleConfig as { expr?: string } | null;
    const cronExpr = typeof cfg?.expr === 'string' && cfg.expr ? cfg.expr : descExpr;
    const base = cronToHumanReadable(cronExpr, labels) || descExpr || job.scheduleKind;
    if (!tz) return base;
    // Only spell out the timezone when it differs from the viewer's own, so the
    // common "runs in my zone" case stays clean (matches the config section).
    const localTz = Intl.DateTimeFormat().resolvedOptions().timeZone;
    return tz === localTz ? base : `${base} · ${tz}`;
}

/**
 * Pill-shaped toggle that shows enabled/paused state with a colored dot.
 * Lifted from the former AutomationsPanel so the main-panel detail header keeps
 * the same expressive control.
 */
function StatusToggle({
    enabled,
    onToggle,
}: {
    enabled: boolean;
    onToggle: () => void;
}) {
    const { t } = useTranslation('app');
    return (
        <button
            type="button"
            onClick={onToggle}
            className={cn(
                'inline-flex items-center gap-1.5 px-2.5 py-1 rounded-full text-[11px] font-medium transition-colors',
                enabled
                    ? 'bg-success-bg text-success hover:bg-success-border'
                    : 'bg-border/50 text-muted hover:text-ink hover:bg-border/70',
            )}
            title={enabled ? t('automations.pauseTooltip') : t('automations.activateTooltip')}
        >
            <span className="w-1.5 h-1.5 rounded-full bg-current" />
            {enabled ? t('automations.active') : t('automations.paused')}
        </button>
    );
}

/**
 * Single-automation detail view designed to fill the right main content column
 * (mirrors how a chat session renders in <main>). Renders the same header
 * controls the old two-pane AutomationsPanel used (Run now / status toggle /
 * delete / Save / Cancel) plus the shared CronJobDetail body — without the job
 * list, since the flat sidebar Automations list now drives selection.
 */
export function AutomationDetailPane({
    job,
    onClose,
    onToggleEnabled,
    onDelete,
    onRunNow,
    onUpdate,
    loadRuns,
    loadRunLogs,
    sourceSessionLabel,
    onNavigateToSourceSession,
    onOpenChatMessage,
    wideMode,
}: AutomationDetailPaneProps) {
    const { t } = useTranslation('app');
    const scheduleTextLabels = useMemo(() => createScheduleTextLabels(t), [t]);

    const [detailEditing, setDetailEditing] = useState(false);
    const [detailSaving, setDetailSaving] = useState(false);
    const detailRef = useRef<CronJobDetailHandle>(null);
    const [deleteTarget, setDeleteTarget] = useState<CronJobState | null>(null);
    // Scheduled's detail says every failure in place (C02-C04), never a toast.
    const [enabledShown, setEnabledShown] = useState<boolean | null>(null);
    const [toggleFailed, setToggleFailed] = useState(false);
    const [runRefused, setRunRefused] = useState<string | null>(null);
    const [deleteFailed, setDeleteFailed] = useState(false);
    const [deleting, setDeleting] = useState(false);
    const enabled = enabledShown ?? job.enabled;
    useEffect(() => { setEnabledShown(null); }, [job.enabled]);

    const toggle = async () => {
        const next = !enabled;
        setToggleFailed(false);
        setEnabledShown(next);
        let refused = false;
        const ok = await measureTasks('C03', 'do', next ? 'done' : 'stopped', async () =>
            (await onToggleEnabled(job.id, next, { onRefused: () => { refused = true; } })) !== false && !refused,
        ).catch(() => false);
        if (!ok) {
            setEnabledShown(!next);
            setToggleFailed(true);
        }
    };
    const runNow = async () => {
        setRunRefused(null);
        let reason: string | undefined;
        let refused = false;
        const ok = await measureTasks('C02', 'do', 'done', async () =>
            (await onRunNow(job.id, { onRefused: (why) => { refused = true; reason = why; } })) !== false && !refused,
        ).catch(() => false);
        if (!ok) setRunRefused(reason ?? t('tasks.failure.runNow'));
    };
    const confirmDelete = async () => {
        if (!deleteTarget || deleting) return;
        setDeleting(true);
        setDeleteFailed(false);
        let refused = false;
        const ok = await measureTasks('C04', 'do', 'done', async () =>
            (await onDelete(deleteTarget.id, { onRefused: () => { refused = true; } })) !== false && !refused,
        ).catch(() => false);
        setDeleting(false);
        if (ok) setDeleteTarget(null);
        else setDeleteFailed(true);
    };
    const [askOpen, setAskOpen] = useState(false);
    const chatOn = useFeatureSwitches().chat;

    // Escape: cancel edit → close detail (return to chat).
    useEffect(() => {
        const handler = (e: KeyboardEvent) => {
            if (e.key !== 'Escape') return;
            if (detailEditing) {
                detailRef.current?.cancelEdit();
                return;
            }
            if (askOpen) {
                setAskOpen(false);
                return;
            }
            (document.activeElement as HTMLElement)?.blur();
            onClose();
        };
        window.addEventListener('keydown', handler);
        return () => window.removeEventListener('keydown', handler);
    }, [onClose, detailEditing, askOpen]);

    return (
        <div className="flex-1 min-h-0 flex flex-col animate-rise-in">
            {/* Detail header — h-[48px] matches the sidebar header row so the
                bottom border aligns with the sidebar's divider. */}
            <div className="window-drag flex items-center border-b border-border shrink-0 h-[48px] px-6 gap-3">
                <button
                    type="button"
                    onClick={() => { recordTasksAction('C01', 'open', 'opened'); onClose(); }}
                    className="-ml-1.5 shrink-0 p-1.5 rounded-md text-muted hover:text-ink hover:bg-border/50 transition-colors cursor-pointer"
                    title={t('automations.back')}
                >
                    <ArrowLeft className="h-4 w-4" />
                </button>
                <div className="min-w-0 flex-1">
                    <h2 className={cn('text-sm font-medium truncate', enabled ? 'text-ink' : 'text-muted')}>
                        {job.name || t('automations.untitled')}
                    </h2>
                    <p className="text-[11px] text-muted truncate">
                        {enabled ? scheduleSummary(job, scheduleTextLabels) : t('automations.paused')}
                    </p>
                </div>
                <div className="flex items-center gap-2 shrink-0">
                    {detailEditing ? (
                        <>
                            <button
                                type="button"
                                onClick={() => detailRef.current?.cancelEdit()}
                                disabled={detailSaving}
                                className="px-2.5 py-1.5 rounded-md text-xs font-medium text-muted hover:text-ink hover:bg-border/50 transition-colors disabled:opacity-40"
                            >
                                {t('automations.cancel')}
                            </button>
                            <button
                                type="button"
                                onClick={() => void detailRef.current?.save()}
                                disabled={detailSaving}
                                className="inline-flex items-center gap-1 px-2.5 py-1.5 rounded-md text-xs font-medium text-ink bg-border/60 hover:bg-border transition-colors disabled:opacity-40"
                            >
                                {detailSaving && <Loader2 className="h-3 w-3 animate-spin" />}
                                {t('automations.save')}
                            </button>
                        </>
                    ) : (
                        <>
                            <button
                                type="button"
                                onClick={() => void runNow()}
                                className="inline-flex items-center gap-1.5 px-2.5 py-1.5 rounded-md text-xs font-medium text-ink hover:bg-border/50 transition-colors"
                                title={t('automations.runNow')}
                            >
                                <Play className="h-3 w-3" />
                                {t('automations.runNow')}
                            </button>
                            {/* Ask for a change goes through the main chat: none while Chat is off (scope cut 2026-11). */}
                            {chatOn && (
                                <button
                                    type="button"
                                    data-testid="schedule-ask-for-change"
                                    onClick={() => setAskOpen(true)}
                                    className="inline-flex items-center gap-1.5 px-2.5 py-1.5 rounded-md text-xs font-medium text-ink hover:bg-border/50 transition-colors"
                                >
                                    {t('scheduleAsk.askForChange')}
                                </button>
                            )}
                            <StatusToggle
                                enabled={enabled}
                                onToggle={() => void toggle()}
                            />
                            <DropdownMenu>
                                <DropdownMenuTrigger asChild>
                                    <button
                                        type="button"
                                        className="p-1.5 rounded-md text-muted hover:text-ink hover:bg-border/50 transition-colors"
                                        title={t('automations.moreActions')}
                                    >
                                        <MoreHorizontal className="h-3.5 w-3.5" />
                                    </button>
                                </DropdownMenuTrigger>
                                <DropdownMenuContent align="end">
                                    <DropdownMenuItem onSelect={() => { recordTasksAction('C05', 'open', 'opened'); detailRef.current?.enterEdit(); }}>
                                        <Pencil className="h-3.5 w-3.5" />
                                        {t('automations.edit')}
                                    </DropdownMenuItem>
                                    <DropdownMenuSeparator />
                                    <DropdownMenuItem
                                        destructive
                                        onSelect={() => { setDeleteFailed(false); setDeleteTarget(job); }}
                                    >
                                        <Trash2 className="h-3.5 w-3.5" />
                                        {t('automations.delete')}
                                    </DropdownMenuItem>
                                </DropdownMenuContent>
                            </DropdownMenu>
                        </>
                    )}
                </div>
            </div>

            {(toggleFailed || runRefused) && (
                <div className="shrink-0 border-b border-border px-6 py-2 flex flex-col gap-1 items-end" data-testid="schedule-detail-failures">
                    {runRefused && <FailureLine testId="schedule-run-refused" text={runRefused} />}
                    {toggleFailed && <FailureLine tone="copper" testId="schedule-toggle-failed" text={t('tasks.failure.save')} onRetry={() => void toggle()} />}
                </div>
            )}
            {chatOn && askOpen && (
                <AskForChange
                    job={job}
                    describe={(current) => scheduleSummary(current, scheduleTextLabels)}
                    onUpdate={onUpdate}
                    onClose={() => setAskOpen(false)}
                />
            )}

            {/* Detail body */}
            <CronJobDetail
                key={job.id}
                ref={detailRef}
                job={job}
                sourceSessionLabel={sourceSessionLabel}
                onNavigateToSourceSession={
                    onNavigateToSourceSession && sourceSessionLabel
                        ? onNavigateToSourceSession
                        : undefined
                }
                onUpdate={onUpdate}
                loadRuns={loadRuns}
                loadRunLogs={loadRunLogs}
                onOpenChatMessage={onOpenChatMessage}
                onEditingChange={setDetailEditing}
                onSavingChange={setDetailSaving}
                wideMode={wideMode}
            />

            <ConfirmDialog
                open={deleteTarget !== null}
                onOpenChange={(open) => { if (!open) { setDeleteTarget(null); setDeleteFailed(false); } }}
                title={t('automations.deleteTitle')}
                description={
                    <>
                        {t('automations.deleteDescriptionStart')} <span className="font-medium text-ink">{deleteTarget?.name || t('automations.deleteFallback')}</span>
                        {t('automations.deleteDescriptionEnd')}
                    </>
                }
                confirmLabel={t('automations.delete')}
                variant="danger"
                closeOnConfirm={false}
                confirmDisabled={deleting}
                notice={deleteFailed ? <FailureLine tone="copper" testId="schedule-delete-failed" text={t('tasks.failure.delete')} onRetry={() => void confirmDelete()} /> : undefined}
                onConfirm={() => void confirmDelete()}
            />
        </div>
    );
}
