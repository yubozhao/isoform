import { FailedSendContext, type FailedSendActions } from '@/lib/failed-send-context';
import { displayModelCopy } from "@/lib/model-display";
import { memo, useState, useCallback, useMemo, useRef, useEffect, useContext } from 'react';
import { ChatFailureLine } from '@/components/chat-failure-line';
import { cn } from '@/lib/utils';
import { MAIN_CHAT_OBSERVATION_PAUSE_TOOL, formatToolInput, getMainChatProactive, getMainChatStatus, getMainChatReport, getSetupRecord, getSideChatOrigin, getTextedReply, getUpcomingConfirm, getChatDraft, isTodayContextItem } from '@/lib/stream-helpers';
import type { MainChatReportTarget } from '@/lib/stream-helpers';
import { ChatDraftCard, MainChatReportCard, SideChatOriginCard, TextedReplyCard, UpcomingConfirmCard } from './main-chat-report-card';
import { MainChatProactiveCard, MainChatStatusRow } from './main-chat-proactive-card';
import { SetupRecordLine } from './main-chat-setup-card';
import { TodayContextStatus } from './today-context-status';
import { getToolMeta, getToolSummary, isCommandTool, isWriteMarkdownTool } from '@/lib/tool-icons';
import { Button } from '@/components/ui/button';
import { MarkdownContent } from './markdown-content';
import { parseTranscript, TranscriptContent } from './transcript-content';
import { ClaudeIcon } from './icons/executor-icons';
import { Copy, Check, RotateCcw, Pencil, X, Send, FileText, ChevronDown, Volume2, Loader2, FolderOpen } from 'lucide-react';
import { useTranslation } from 'react-i18next';
import { FromPhoneLabel, isFromPhone } from './from-phone-label';
import { toast } from 'sonner';
import { getFileManagerName } from '@/lib/platform';
import { toggleSpeak, useTtsStatus, prewarmTtsForText } from '@/lib/tts-player';
import { OpenAutomationDeliverable } from '../../wailsjs/go/main/App';
import { extractAgentBlocks, type AgentDeliverable } from '@/lib/agent-deliverables';
// Type-only import from the shim itself: these interfaces are new and not yet
// in the generated wailsjs bindings, so resolve them where they're defined.
import type { DeliverableInfo, DeliverableOpenWithApp } from '@/lib/wails-shim-app';
import {
    DropdownMenu,
    DropdownMenuTrigger,
    DropdownMenuContent,
    DropdownMenuItem,
    DropdownMenuSeparator,
} from '@/components/ui/dropdown-menu';
import { useShowToolSteps } from '@/hooks/useShowToolSteps';
import { clearStreamEditDraft, setStreamEditDraft, useStreamEditDraft } from '@/lib/stream-edit-drafts';
import { ToolDebugPopover } from './tool-debug-popover';
import { ChatErrorCard, openYourAI } from './chat-error-card';
import { parseChatError, isJsonRpcError, chatErrorCauseOf } from '@/lib/chat-error';
import type { StreamItem } from '@/types';
import { FileAttachmentList } from './file-attachment-preview';
import type { ExecutorType } from '@/lib/stream-helpers';
import { AnswerReadyCard, OpenAnswerPromiseLine } from './open-answer-ui';
import { useMainChatStreamView } from './main-chat/main-chat-view-context';
import { MainChatWeekAnswer } from './main-chat/main-chat-week-answer';
import { MainChatObservationPause } from './main-chat/main-chat-observation-pause';

// Backwards-compatible re-exports for callers that still import from
// stream-card (processing-block.tsx, automations/CronJobDetail.tsx).
// New code should call `parseChatError` from '@/lib/chat-error' directly.
export { parseQuotaError, friendlyErrorMessage, isJsonRpcError } from '@/lib/chat-error';

/** Test-only render probe: guard tests set it to count row renders. */
export const streamCardRenderProbe: { current: ((itemId: string) => void) | null } = { current: null };

// Agent outputs from scheduled automations can wrap metadata in XML-ish tags:
//   <deliverables>/abs/path.pptx</deliverables>  — files the agent produced
//   <delivery_target>platform: slack ...</delivery_target>  — routing hint
// The backend consumes both (parseDeliverables / parseDeliveryTarget) for
// notification and routing, but the raw stream still carries the tags, so the
// frontend must strip them before rendering. Deliverable paths are surfaced
// as file chips below the message; delivery_target is already reflected by
// the notify badge in the run header, so it's just removed.
function pathBasename(p: string): string {
    const trimmed = p.replace(/[\\/]+$/, '');
    const idx = Math.max(trimmed.lastIndexOf('/'), trimmed.lastIndexOf('\\'));
    return idx === -1 ? trimmed : trimmed.slice(idx + 1);
}

// Rich deliverable actions, all bound to the owning session/job by the caller
// (e.g. App.tsx binds them to the active chat's localId). When provided, each
// deliverable renders as a Cola-style card with a split "Open" button, an
// "Open with <app>" sub-menu, and "Reveal in Finder". When absent, deliverables
// fall back to the simple click-to-open chip used by automation run history.
export interface DeliverableActions {
    open: (path: string) => void;
    reveal: (path: string) => void;
    openWith: (path: string, appPath: string) => void;
    listOpenWith: (path: string) => Promise<DeliverableOpenWithApp[]>;
    getInfo: (path: string) => Promise<DeliverableInfo>;
}

function formatDeliverableSize(bytes: number): string {
    if (bytes >= 1024 * 1024) return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
    if (bytes >= 1024) return `${(bytes / 1024).toFixed(0)} KB`;
    return `${bytes} B`;
}

export function RichDeliverableCard({
    path,
    description,
    actions,
    unavailableMode,
}: {
    path: string;
    /** Agent-written one-sentence description of what the file is. When
     *  present it becomes the subtitle (the type/size metadata line is the
     *  fallback) so users can tell files apart at a glance. */
    description?: string;
    actions: DeliverableActions;
    // When set, the card shows a disabled state if the file can't be loaded:
    // 'pending' = produced by an in-progress task (not ready yet — optimistic,
    // hover explains it'll be available once the task completes); 'invalid' = a
    // finished deliverable whose file is gone. Omitted by chat/automation
    // callers, which keep the original always-clickable Open button.
    unavailableMode?: 'pending' | 'invalid';
}) {
    const { t, i18n } = useTranslation('chat');
    const name = pathBasename(path);
    const [info, setInfo] = useState<DeliverableInfo | null>(null);
    const [infoState, setInfoState] = useState<'loading' | 'ready' | 'error'>('loading');
    const [apps, setApps] = useState<DeliverableOpenWithApp[] | null>(null);
    const [loadingApps, setLoadingApps] = useState(false);

    useEffect(() => {
        let alive = true;
        setInfoState('loading');
        actions
            .getInfo(path)
            .then((i) => {
                if (alive) {
                    setInfo(i);
                    setInfoState('ready');
                }
            })
            .catch(() => {
                if (alive) setInfoState('error');
            });
        return () => {
            alive = false;
        };
    }, [actions, path]);

    // Treat the file as unavailable only when the caller opted into the
    // semantic AND the info probe actually failed. While loading we stay
    // optimistic so the Open button doesn't flicker into a disabled state.
    const unavailable = unavailableMode !== undefined && infoState === 'error';

    const subtitle = useMemo(() => {
        if (unavailable) {
            return t(
                unavailableMode === 'pending'
                    ? 'deliverables.pendingLabel'
                    : 'deliverables.unavailableLabel',
            );
        }
        if (description) return description;
        const parts = [t('deliverables.fileLabel')];
        if (info?.ext) parts.push(info.ext);
        if (info && info.sizeBytes > 0)
            parts.push(formatDeliverableSize(info.sizeBytes));
        return parts.join(' · ');
    }, [unavailable, unavailableMode, description, info, t]);

    // Lazy-load the "open with" app list the first time the menu opens —
    // enumerating LaunchServices handlers per card on mount would be wasteful.
    const handleOpenChange = useCallback(
        (open: boolean) => {
            if (open && apps === null && !loadingApps) {
                setLoadingApps(true);
                actions
                    .listOpenWith(path)
                    .then((a) => setApps(a ?? []))
                    .catch(() => setApps([]))
                    .finally(() => setLoadingApps(false));
            }
        },
        [apps, loadingApps, actions, path],
    );

    return (
        <div className="flex w-full max-w-md items-center gap-3 rounded-lg border border-border/60 bg-panel px-3 py-2.5">
            <div className="flex size-9 shrink-0 items-center justify-center rounded-md bg-ink/[0.04] text-muted/70">
                <FileText className="size-4" />
            </div>
            <div className="min-w-0 flex-1">
                <p className="m-0 truncate text-sm font-medium text-ink">{name}</p>
                <p className="m-0 mt-0.5 truncate text-xs text-muted" title={subtitle}>
                    {subtitle}
                </p>
            </div>
            {unavailable ? (
                <span
                    className="shrink-0 cursor-not-allowed rounded-md border border-border/60 px-3 py-1.5 text-xs font-medium text-muted/50"
                    title={t(
                        unavailableMode === 'pending'
                            ? 'deliverables.pendingHint'
                            : 'deliverables.unavailableHint',
                    )}
                >
                    {t('deliverables.openButton')}
                </span>
            ) : (
            <div className="flex shrink-0 items-stretch overflow-hidden rounded-md border border-border/60">
                <button
                    type="button"
                    onClick={(e) => {
                        e.stopPropagation();
                        actions.open(path);
                    }}
                    className="px-3 py-1.5 text-xs font-medium text-ink hover:bg-ink/5 transition-colors cursor-pointer"
                    aria-label={t('deliverables.openAria', { name })}
                >
                    {t('deliverables.openButton')}
                </button>
                <DropdownMenu onOpenChange={handleOpenChange}>
                    <DropdownMenuTrigger asChild>
                        <button
                            type="button"
                            onClick={(e) => e.stopPropagation()}
                            className="flex items-center border-l border-border/60 px-1.5 text-muted hover:bg-ink/5 transition-colors cursor-pointer"
                            aria-label={t('deliverables.moreActions', { name })}
                        >
                            <ChevronDown className="size-3.5" />
                        </button>
                    </DropdownMenuTrigger>
                    <DropdownMenuContent align="end" className="min-w-[200px]">
                        {loadingApps && (
                            <div className="flex items-center gap-2 px-2 py-1.5 text-xs text-muted">
                                <Loader2 className="size-3.5 animate-spin" />
                                {t('deliverables.loadingApps')}
                            </div>
                        )}
                        {apps?.map((app) => (
                            <DropdownMenuItem
                                key={app.path}
                                onClick={() => actions.openWith(path, app.path)}
                                className="gap-2"
                            >
                                {app.icon ? (
                                    <img
                                        src={app.icon}
                                        alt=""
                                        className="size-4 shrink-0"
                                    />
                                ) : (
                                    <FileText className="size-4 shrink-0 text-muted/70" />
                                )}
                                <span className="truncate">{app.name}</span>
                            </DropdownMenuItem>
                        ))}
                        {apps && apps.length > 0 && <DropdownMenuSeparator />}
                        <DropdownMenuItem
                            onClick={() => actions.reveal(path)}
                            className="gap-2"
                        >
                            <FolderOpen className="size-4 shrink-0 text-muted/70" />
                            <span>{t('deliverables.revealInFinder', { fileManager: getFileManagerName(i18n.language) })}</span>
                        </DropdownMenuItem>
                    </DropdownMenuContent>
                </DropdownMenu>
            </div>
            )}
        </div>
    );
}

function DeliverableChips({
    files,
    onOpen,
    actions,
}: {
    files: AgentDeliverable[];
    onOpen?: (path: string) => void;
    actions?: DeliverableActions;
}) {
    const { t } = useTranslation('chat');
    if (files.length === 0) return null;
    const interactive = Boolean(onOpen);
    return (
        <div className="mt-3">
            <p className="text-[10px] font-medium text-muted uppercase tracking-wider mb-1.5">
                {t('deliverables.title')}
            </p>
            {actions ? (
                <div className="flex flex-col items-start gap-1.5">
                    {files.map((f, i) => (
                        <RichDeliverableCard
                            key={`${f.path}-${i}`}
                            path={f.path}
                            description={f.description || undefined}
                            actions={actions}
                        />
                    ))}
                </div>
            ) : (
                <div className="flex flex-wrap gap-1.5">
                    {files.map(({ path: p, description }, i) => {
                        const name = pathBasename(p);
                        const content = (
                            <>
                                <FileText className="h-4 w-4 shrink-0 text-muted/70" />
                                <span className="truncate max-w-[280px]">{name}</span>
                            </>
                        );
                        const baseClass = 'inline-flex items-center gap-2 rounded-md border border-border/60 bg-panel px-3 py-2 text-xs text-muted';
                        if (!interactive) {
                            return (
                                <span
                                    key={`${p}-${i}`}
                                    className={baseClass}
                                    title={description || p}
                                >
                                    {content}
                                </span>
                            );
                        }
                        return (
                            <button
                                key={`${p}-${i}`}
                                type="button"
                                onClick={(e) => {
                                    e.stopPropagation();
                                    onOpen?.(p);
                                }}
                                className={`${baseClass} hover:bg-surface-hover hover:text-ink transition-colors cursor-pointer focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent-2/40`}
                                title={t('deliverables.open', { name })}
                                aria-label={t('deliverables.openAria', { name })}
                            >
                                {content}
                            </button>
                        );
                    })}
                </div>
            )}
        </div>
    );
}

/**
 * Renders an assistant/result message body: strips agent-only XML blocks,
 * renders the cleaned markdown, appends any deliverable chips, and
 * optionally shows a hover-reveal copy button (copy text matches what's
 * displayed, not the raw tagged source).
 */
function AssistantMarkdown({
    rawContent,
    speakId,
    showHoverCopy = false,
    onOpenDeliverable,
    deliverableActions,
}: {
    rawContent: string;
    speakId?: string;
    showHoverCopy?: boolean;
    onOpenDeliverable?: (path: string) => void;
    deliverableActions?: DeliverableActions;
}) {
    const { content, deliverables } = useMemo(
        () => extractAgentBlocks(rawContent),
        [rawContent],
    );
    // Transcript session bodies arrive as `[time] speaker: text` plaintext;
    // render those as a structured transcript instead of a raw text wall.
    const transcript = useMemo(() => parseTranscript(content), [content]);
    const playbackId = speakId ?? content;
    // Keep the action row visible (not hover-gated) while this message is being
    // read aloud, so the stop/playing icon stays on screen like a media control.
    const speaking = useTtsStatus(playbackId) !== 'idle';
    return (
        <>
            {transcript ? (
                <TranscriptContent lines={transcript} />
            ) : (
                <div className="text-ink" data-isoform-line><MarkdownContent content={content} /></div>
            )}
            <DeliverableChips
                files={deliverables}
                onOpen={onOpenDeliverable}
                actions={deliverableActions}
            />
            {showHoverCopy && content && (
                <div
                    className={cn(
                        'mt-1.5 flex gap-0.5 transition-opacity',
                        speaking
                            ? 'opacity-100'
                            : 'opacity-0 group-hover:opacity-100 focus-within:opacity-100',
                    )}
                >
                    <MessageCopyButton text={content} absolute={false} />
                    <MessageSpeakButton text={content} speakId={playbackId} />
                </div>
            )}
        </>
    );
}

/**
 * Collapsible "run output" quote shown above the follow-up question when a
 * chat was started from a scheduled automation's output ("continue in chat").
 * Renders the raw run output through AssistantMarkdown so markdown is rendered
 * and <deliverables>/<delivery_target> tags become file chips / are stripped,
 * instead of leaking as plaintext in the user bubble.
 */
function AutomationResultQuote({ output, jobId }: { output: string; jobId?: string }) {
    const { t } = useTranslation('chat');
    const [collapsed, setCollapsed] = useState(false);
    // Deliverable chips become clickable only when we know the owning job, so
    // OpenAutomationDeliverable can resolve the file against that automation.
    const handleOpenDeliverable = useMemo(
        () =>
            jobId
                ? (path: string) => {
                      OpenAutomationDeliverable(jobId, path).catch((err: unknown) => {
                          toast.error(err instanceof Error ? err.message : String(err));
                      });
                  }
                : undefined,
        [jobId],
    );
    return (
        <div className="rounded-lg border border-border/60 bg-panel overflow-hidden">
            <button
                type="button"
                onClick={() => setCollapsed((v) => !v)}
                className="flex w-full items-center gap-1.5 px-3 py-2 text-[10px] font-medium uppercase tracking-wider text-muted hover:bg-ink/[0.03] transition-colors"
            >
                <ChevronDown className={cn('h-3 w-3 transition-transform', collapsed && '-rotate-90')} />
                {t('automationResultQuote.title')}
            </button>
            {!collapsed && (
                <div className="px-3 pb-3 text-sm text-ink max-h-[300px] overflow-y-auto">
                    <AssistantMarkdown rawContent={output} onOpenDeliverable={handleOpenDeliverable} />
                </div>
            )}
        </div>
    );
}

interface StreamCardProps {
    item: StreamItem;
    executorType?: ExecutorType;
    /** Render in compact mode (no icon, no bubble) for use inside processing blocks */
    compact?: boolean;
    /** Called when the user clicks the retry button on a user message */
    onReset?: () => void;
    /** Called when the user confirms an inline edit — triggers rewind + resend.
     *  A false result (the App rejected it, e.g. a reply started streaming)
     *  keeps the draft and the editor open. */
    onEdit?: (newContent: string) => unknown;
    /** Called when the user clicks a deliverable file chip to open it. */
    onOpenDeliverable?: (path: string) => void;
    /** Rich deliverable actions (open / open-with / reveal + metadata). When
     *  provided, deliverables render as Cola-style cards instead of plain chips. */
    deliverableActions?: DeliverableActions;
    /** tool_use_id → matching tool_result, used by tool details to pair raw
     *  outputs with their originating tool call. */
    resultsByToolUseId?: Map<string, StreamItem>;
    /** Opens what a main-chat report line or a side-chat origin points at. */
    onOpenReportTarget?: (target: MainChatReportTarget) => void;
    /** "Use as my message" on a texted reply: puts its words in the composer. */
    onUseTextedReply?: (text: string) => void;
    onSendOpenAnswer?: (sessionId: string, text: string) => Promise<unknown>;
}

function MessageCopyButton({ text, absolute = true }: { text: string; absolute?: boolean }) {
    const [copied, setCopied] = useState(false);
    const timerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
    useEffect(() => () => { if (timerRef.current) clearTimeout(timerRef.current); }, []);
    const handleCopy = useCallback(() => {
        navigator.clipboard.writeText(text);
        setCopied(true);
        if (timerRef.current) clearTimeout(timerRef.current);
        timerRef.current = setTimeout(() => setCopied(false), 2000);
    }, [text]);
    return (
        <button
            className={cn(
                'size-7 flex items-center justify-center rounded-md text-muted hover:text-ink hover:bg-surface-hover transition-opacity cursor-pointer',
                absolute && 'absolute top-1.5 right-1.5 opacity-0 group-hover:opacity-100'
            )}
            onClick={handleCopy}
            aria-label={copied ? 'Copied' : 'Copy message'}
        >
            {copied ? <Check className="size-3.5" /> : <Copy className="size-3.5" />}
        </button>
    );
}

function MessageSpeakButton({ text, speakId }: { text: string; speakId: string }) {
    const { t } = useTranslation('chat');
    const status = useTtsStatus(speakId);
    const handleClick = useCallback(() => {
        toggleSpeak(speakId, text).catch(() => {
            toast.error(t('ttsDownload.failed'), {
                id: 'tts-model-download',
                description: t('ttsDownload.retry'),
            });
        });
    }, [speakId, t, text]);
    return (
        <button
            className="size-7 flex items-center justify-center rounded-md text-muted hover:text-ink hover:bg-surface-hover transition-opacity cursor-pointer"
            onClick={handleClick}
            onMouseEnter={() => prewarmTtsForText(text)}
            aria-label={status === 'playing' ? t('messageActions.stop') : t('messageActions.speak')}
        >
            {status === 'loading' ? (
                <Loader2 className="size-3.5 animate-spin" />
            ) : status === 'playing' ? (
                <span className="tts-bars" aria-hidden>
                    <span />
                    <span />
                    <span />
                    <span />
                </span>
            ) : (
                <Volume2 className="size-3.5" />
            )}
        </button>
    );
}

function ExecutorAvatar({ executorType: _executorType }: { executorType: ExecutorType }) {
    return <ClaudeIcon className="size-6 shrink-0 mt-0.5 rounded-full" />;
}

export function ToolCard({
    item,
    open,
    onToggle,
    result,
    showToolSteps = false,
}: {
    item: StreamItem;
    open: boolean;
    onToggle: () => void;
    /** Paired tool_result for this tool call, fed into the debug popover. */
    result?: StreamItem;
    /** When true, surface a hover-popover trigger that reveals raw input,
     *  paired tool_result, and tool_use_id / run_id. Driven by the tool-steps preference. */
    showToolSteps?: boolean;
}) {
    const meta = useMemo(() => getToolMeta(item.toolName ?? ''), [item.toolName]);
    const summary = useMemo(
        () => getToolSummary(item.toolName ?? '', item.toolInput, 0),
        [item.toolName, item.toolInput]
    );
    const isCommand = useMemo(() => isCommandTool(item.toolInput), [item.toolInput]);
    const Icon = meta.icon;

    return (
        <div className="p-4 rounded-lg border border-border bg-panel-code border-l-4 border-l-accent/25 animate-rise-in">
            <div className="flex justify-between gap-3 items-center">
                <div className="flex items-center gap-2.5 min-w-0">
                    <div className={`flex size-7 shrink-0 items-center justify-center rounded-md ${meta.color} bg-ink/5`}>
                        <Icon className="size-3.5" />
                    </div>
                    <div className="min-w-0">
                        <p className="m-0 font-semibold text-sm">
                            {item.toolName}
                        </p>
                        <p className={cn('mt-0.5 mb-0 text-muted text-xs truncate', !isCommand && 'font-mono')}>
                            {summary}
                        </p>
                    </div>
                </div>
                <div className="flex items-center gap-1 shrink-0">
                    {showToolSteps && <ToolDebugPopover item={item} result={result} />}
                    {!isCommand && (
                        <Button
                            variant="ghost"
                            size="sm"
                            onClick={onToggle}
                        >
                            {open ? 'Hide' : 'Show'} details
                        </Button>
                    )}
                </div>
            </div>
            {!isCommand && open && (
                isWriteMarkdownTool(item.toolName, item.toolInput) ? (
                    <div className="mt-3 rounded-xl border border-border bg-panel-warm p-3 max-h-[400px] overflow-y-auto">
                        <MarkdownContent content={(item.toolInput as Record<string, unknown>).content as string} />
                    </div>
                ) : (
                    <pre className="mt-3 bg-code-bg text-code-fg p-3 rounded-xl text-xs overflow-x-auto">
                        {formatToolInput(item.toolInput)}
                    </pre>
                )
            )}
        </div>
    );
}

export const StreamCard = memo(function StreamCard({
    item: rawItem,
    executorType = 'claude',
    compact = false,
    onReset,
    onEdit,
    onOpenDeliverable,
    deliverableActions,
    resultsByToolUseId,
    onOpenReportTarget,
    onUseTextedReply,
    onSendOpenAnswer,
}: StreamCardProps) {
    const { i18n } = useTranslation("common");
    const surface = useMainChatStreamView()?.surface ?? 'chat';
    const failedSend = useContext(FailedSendContext);
    const item = useMemo(() => displayModelCopy(rawItem), [rawItem, i18n.resolvedLanguage]);
    streamCardRenderProbe.current?.(item.id);
    const [open, setOpen] = useState(true);
    // The draft lives above the windowed list (lib/stream-edit-drafts.ts):
    // scrolling the row out of the window must not lose an unsent edit.
    const editText = useStreamEditDraft(item.id);
    const editing = editText !== null;
    const [handoffContextExpanded, setHandoffContextExpanded] = useState(false);
    const [showToolSteps] = useShowToolSteps();
    const isHandoffContext = item.handoffContext === true;

    const textareaRef = useCallback((node: HTMLTextAreaElement | null) => {
        if (node) {
            node.focus();
            node.setSelectionRange(node.value.length, node.value.length);
            node.style.height = 'auto';
            node.style.height = node.scrollHeight + 'px';
        }
    }, []);

    const handleStartEdit = useCallback(() => {
        setStreamEditDraft(item.id, item.content ?? '');
    }, [item.id, item.content]);

    const { t: tFail } = useTranslation('chat');
    const [editFailed, setEditFailed] = useState(false);
    const handleCancelEdit = useCallback(() => {
        setEditFailed(false);
        clearStreamEditDraft(item.id);
    }, [item.id]);

    const handleConfirmEdit = useCallback(async () => {
        const trimmed = (editText ?? '').trim();
        if (!trimmed) {
            handleCancelEdit();
            return;
        }
        if (trimmed === (item.content ?? '').trim()) {
            handleCancelEdit();
            return;
        }
        const accepted = await Promise.resolve(onEdit?.(trimmed));
        // Rejected (a reply started streaming, or the send failed): keep the
        // draft and the editor so the words survive.
        if (accepted === false) {
            setEditFailed(true);
            return false;
        }
        setEditFailed(false);
        clearStreamEditDraft(item.id);
    }, [editText, item.content, item.id, onEdit, handleCancelEdit]);

    const handleTextareaInput = useCallback((e: React.ChangeEvent<HTMLTextAreaElement>) => {
        setStreamEditDraft(item.id, e.target.value);
        e.target.style.height = 'auto';
        e.target.style.height = e.target.scrollHeight + 'px';
    }, [item.id]);

    const handleTextareaKeyDown = useCallback((e: React.KeyboardEvent<HTMLTextAreaElement>) => {
        if (e.key === 'Enter' && !e.shiftKey) {
            e.preventDefault();
            void handleConfirmEdit();
        }
        if (e.key === 'Escape') {
            e.preventDefault();
            handleCancelEdit();
        }
    }, [handleConfirmEdit, handleCancelEdit]);

    // ── Tool card ──
    if (item.kind === 'tool') {
        if (compact) {
            const meta = getToolMeta(item.toolName ?? '');
            const Icon = meta.icon;
            return (
                <p className="m-0 animate-rise-in text-xs font-semibold text-muted flex items-center gap-1.5">
                    <Icon className={`size-3 ${meta.color}`} />
                    {item.toolName}
                </p>
            );
        }
        return (
            <ToolCard
                item={item}
                open={open}
                onToggle={() => setOpen((prev) => !prev)}
                showToolSteps={showToolSteps}
                result={
                    showToolSteps && item.toolUseId
                        ? resultsByToolUseId?.get(item.toolUseId)
                        : undefined
                }
            />
        );
    }

    // ── User message ──
    if (item.kind === 'user' && item.weekAnswerBriefId && item.content) {
        return <MainChatWeekAnswer briefId={item.weekAnswerBriefId} answer={item.content} />;
    }
    if (item.kind === 'user') {
        if (compact) {
            if (isHandoffContext) {
                return (
                    <div className="animate-rise-in px-3 py-2 rounded-md border border-border bg-panel-user text-sm">
                        <p className="m-0 flex items-center gap-1.5 text-muted">
                            <FileText className="size-3.5" />
                            Handoff details
                        </p>
                    </div>
                );
            }
            return (
                <div className="animate-rise-in px-3 py-2 rounded-md border border-border bg-panel-user text-sm">
                    <p className="m-0 whitespace-pre-wrap leading-relaxed">{item.content}</p>
                </div>
            );
        }
        return (
            <>
                {item.automationResultOutput && (
                    <AutomationResultQuote output={item.automationResultOutput} jobId={item.automationJobId} />
                )}
            {isFromPhone(item) && <div className="flex justify-end px-1 pb-1"><FromPhoneLabel /></div>}
            <div className="group/row flex justify-end animate-rise-in">
                <div className={cn('max-w-[80%]', editing ? 'w-full' : 'flex flex-col items-end')}>
                    <div
                        className={cn(
                            'group relative overflow-x-auto rounded-xl select-text bg-panel-user',
                            item.sendFailed && 'opacity-60',
                            editing ? 'px-3 py-2.5' : 'max-w-full px-3.5 py-1.5'
                        )}
                    >
                        {item.images && item.images.length > 0 && (
                            <div className="flex flex-wrap gap-2 mb-2">
                                {item.images.map((img, i) => (
                                    <img
                                        key={i}
                                        src={`data:${img.mediaType};base64,${img.dataBase64}`}
                                        alt={`Attachment ${i + 1}`}
                                        className="max-h-32 rounded-lg border border-border object-cover"
                                    />
                                ))}
                            </div>
                        )}
                        {item.files && item.files.length > 0 && (
                            <FileAttachmentList
                                files={item.files}
                                compact
                                className="mb-2"
                            />
                        )}
                        {editing ? (
                            <>
                                <textarea
                                    ref={textareaRef}
                                    className="w-full max-h-64 resize-none bg-transparent text-sm font-normal text-ink leading-relaxed outline-none overflow-y-auto"
                                    value={editText ?? ''}
                                    onChange={handleTextareaInput}
                                    onKeyDown={handleTextareaKeyDown}
                                    rows={1}
                                    spellCheck={false}
                                    autoCorrect="off"
                                    autoCapitalize="off"
                                />
                                {editFailed && (
                                    <ChatFailureLine testId="edit" className="mt-1.5" text={tFail('failure.save')} onRetry={handleConfirmEdit} />
                                )}
                                <div className="flex items-center justify-end gap-1 mt-2 pt-2 border-t border-border/50">
                                    <button
                                        className="size-7 flex items-center justify-center rounded-md text-muted hover:text-ink hover:bg-ink/5 cursor-pointer transition-colors"
                                        onClick={handleCancelEdit}
                                        aria-label="Cancel"
                                        title="Cancel (Esc)"
                                    >
                                        <X className="size-3.5" />
                                    </button>
                                    <button
                                        className="size-7 flex items-center justify-center rounded-md bg-accent/15 text-accent hover:bg-accent/25 cursor-pointer transition-colors"
                                        onClick={handleConfirmEdit}
                                        aria-label="Send edited message"
                                        title="Send (Enter)"
                                    >
                                        <Send className="size-3.5" />
                                    </button>
                                </div>
                            </>
                        ) : isHandoffContext ? (
                            <div className="min-w-[260px] max-w-full text-sm text-ink">
                                <button
                                    type="button"
                                    className="flex w-full items-center gap-2 text-left text-sm font-medium text-ink"
                                    onClick={() => setHandoffContextExpanded((v) => !v)}
                                    aria-expanded={handoffContextExpanded}
                                >
                                    <FileText className="size-4 shrink-0 text-accent" />
                                    <span className="flex-1 truncate">
                                        Handoff details
                                    </span>
                                    <ChevronDown
                                        className={cn(
                                            'size-4 shrink-0 text-muted transition-transform',
                                            !handoffContextExpanded && '-rotate-90',
                                        )}
                                    />
                                </button>
                                {handoffContextExpanded && (
                                    <div className="mt-2 max-h-[360px] overflow-y-auto border-t border-border/50 pt-2 text-sm leading-relaxed">
                                        <MarkdownContent content={item.content ?? ''} />
                                    </div>
                                )}
                            </div>
                        ) : (
                            <p className="m-0 whitespace-pre-wrap leading-relaxed text-sm font-normal text-ink">
                                {item.content}
                            </p>
                        )}
                    </div>
                    {item.sendFailed && failedSend && <KeptSendLine item={item} actions={failedSend} />}
                    {/* Action buttons — below the bubble, visible on hover (mirrors the assistant action row) */}
                    {!editing && !isHandoffContext && (onEdit || onReset || item.content) && (
                        <div className="mt-1.5 flex justify-end gap-0.5 opacity-0 group-hover/row:opacity-100 focus-within:opacity-100 transition-opacity">
                            {onReset && (
                                <button
                                    className="size-7 flex items-center justify-center rounded-md text-muted hover:text-ink hover:bg-surface-hover cursor-pointer transition-colors"
                                    onClick={onReset}
                                    aria-label="Retry this message"
                                    title="Retry"
                                >
                                    <RotateCcw className="size-3.5" />
                                </button>
                            )}
                            {onEdit && (
                                <button
                                    className="size-7 flex items-center justify-center rounded-md text-muted hover:text-ink hover:bg-surface-hover cursor-pointer transition-colors"
                                    onClick={handleStartEdit}
                                    aria-label="Edit message"
                                    title="Edit"
                                >
                                    <Pencil className="size-3.5" />
                                </button>
                            )}
                            {item.content && <MessageCopyButton text={item.content} absolute={false} />}
                        </div>
                    )}
                </div>
            </div>
            </>
        );
    }

    // ── Assistant message ──
    // Fall through to the error branch below when the assistant payload is
    // actually a JSON-RPC error that leaked onto this channel.
    if (item.kind === 'assistant' && !isJsonRpcError(item.content)) {
        if (item.cardType === 'answer_ready' && item.openAnswer) return <AnswerReadyCard item={item} />;
        const promise = item.openAnswer
            ? <OpenAnswerPromiseLine answer={item.openAnswer} onSend={onSendOpenAnswer} />
            : null;
        if (compact) {
            return (
                <div className="animate-rise-in select-text text-[13.5px] leading-relaxed text-ink/85">
                    <AssistantMarkdown rawContent={item.content ?? ''} onOpenDeliverable={onOpenDeliverable} deliverableActions={deliverableActions} />
                    {promise}
                </div>
            );
        }
        return (
            <div className="animate-rise-in">
                <div className="group relative min-w-0 overflow-x-auto select-text text-base font-normal text-ink">
                    <AssistantMarkdown
                        rawContent={item.content ?? ''}
                        speakId={item.id}
                        showHoverCopy
                        onOpenDeliverable={onOpenDeliverable}
                        deliverableActions={deliverableActions}
                    />
                    {promise}
                </div>
            </div>
        );
    }

    // ── Result message ──
    // Fall through to the error branch below when the result payload is
    // actually a JSON-RPC error that leaked onto this channel.
    if (item.kind === 'result' && !isJsonRpcError(item.content)) {
        if (compact) {
            return (
                <div className="animate-rise-in select-text text-[13.5px] leading-relaxed text-ink/85">
                    <AssistantMarkdown rawContent={item.content ?? ''} onOpenDeliverable={onOpenDeliverable} deliverableActions={deliverableActions} />
                </div>
            );
        }
        return (
            <div className="flex items-start gap-3 animate-rise-in">
                <ExecutorAvatar executorType={executorType} />
                <div className="group relative flex-1 min-w-0 overflow-x-auto px-4 py-3 rounded-2xl rounded-tl-md bg-panel-result border border-border">
                    <AssistantMarkdown rawContent={item.content ?? ''} speakId={item.id} showHoverCopy onOpenDeliverable={onOpenDeliverable} deliverableActions={deliverableActions} />
                </div>
            </div>
        );
    }

    if (isTodayContextItem(item)) {
        return <TodayContextStatus content={item.content} />;
    }

    // ── Error message ──
    // Also catches JSON-RPC error payloads that arrive on assistant/result
    // streams so users see a typed error card instead of the raw JSON.
    if (item.kind === 'error' || isJsonRpcError(item.content)) {
        const cause = chatErrorCauseOf(item);
        // R75: only the chat's latest signed-out or no-reply answer offers Send again.
        const resend = (cause.code === 'signed_out' || cause.code === 'no_reply') && failedSend && failedSend.resendItemId === item.id ? failedSend.resendLast : undefined;
        return <ChatErrorCard error={parseChatError(item.content, cause.code)} assistant={cause.assistant} compact={compact} onRetry={resend} />;
    }

    // ── Main-chat report line / side-chat origin: quiet cards with one action ──
    if (getMainChatProactive(item)) {
        return <MainChatProactiveCard item={item} onOpenTarget={onOpenReportTarget} compact={compact} panelSurface={surface} />;
    }
    if (getMainChatStatus(item)) {
        return <MainChatStatusRow item={item} onOpenTarget={onOpenReportTarget} compact={compact} panelSurface={surface} />;
    }
    if (getMainChatReport(item)) {
        return <MainChatReportCard item={item} onOpenTarget={onOpenReportTarget} compact={compact} />;
    }
    if (getSetupRecord(item)) {
        return <SetupRecordLine item={item} />;
    }
    if (getSideChatOrigin(item)) {
        return <SideChatOriginCard item={item} onOpenTarget={onOpenReportTarget} compact={compact} />;
    }
    if (getUpcomingConfirm(item)) {
        return <UpcomingConfirmCard item={item} compact={compact} />;
    }
    if (getChatDraft(item)) {
        return <ChatDraftCard item={item} compact={compact} />;
    }
    if (getTextedReply(item)) {
        return <TextedReplyCard item={item} onUse={onUseTextedReply} compact={compact} />;
    }

    // ── System message (may contain markdown, e.g. plan content) ──
    if (item.kind === 'system') {
        if (item.toolName === MAIN_CHAT_OBSERVATION_PAUSE_TOOL) {
            return <MainChatObservationPause input={item.toolInput} />;
        }
        if (compact) {
            return (
                <div className="animate-rise-in text-[13.5px] leading-relaxed text-ink/85">
                    <MarkdownContent content={item.content ?? ''} />
                </div>
            );
        }
        return (
            <div className="animate-rise-in max-w-[90%] mx-auto">
                <div className="px-4 py-3 rounded-lg border border-border bg-panel-warm text-sm">
                    <MarkdownContent content={item.content ?? ''} />
                </div>
            </div>
        );
    }

    // ── Thinking / fallback ──
    if (compact) {
        return (
            <p className="m-0 animate-rise-in text-xs text-muted whitespace-pre-wrap leading-relaxed">
                {item.kind === 'thinking' && <span className="font-semibold mr-1">Thinking:</span>}
                {item.content}
            </p>
        );
    }
    return (
        <div className="animate-rise-in max-w-[90%] mx-auto">
            <div
                className={cn(
                    'px-4 py-2.5 rounded-lg border text-sm text-center text-muted',
                    item.kind === 'thinking'
                        ? 'bg-panel-warm border-accent/15 italic'
                        : 'bg-panel-warm border-border'
                )}
            >
                {item.kind === 'thinking' && (
                    <span className="text-[11px] tracking-widest uppercase block mb-1 not-italic">
                        Thinking
                    </span>
                )}
                <p className="m-0 whitespace-pre-wrap leading-relaxed">
                    {item.content}
                </p>
            </div>
        </div>
    );
});

const KEPT_LINK_CLASS = 'cursor-pointer font-medium underline-offset-2 hover:underline focus-visible:underline';

/**
 * The line under a kept message. A send kept for want of an AI reads
 * "Didn't send: no AI is signed in. · Set up" in copper until an AI is
 * configured; then, like any failed send, "Didn't send · Try again · Edit".
 * A Try again that fails too reads "Still didn't work." Nothing resends by itself.
 */
export function KeptSendLine({ item, actions }: { item: StreamItem; actions: FailedSendActions }) {
    const { t } = useTranslation('chat');
    if (item.sendFailedCode === 'no_assistant' && actions.aiReady !== true) {
        return (
            <p role="status" className="m-0 mt-1 text-right text-xs text-warning">
                <span>{t('errors.cause.noAssistantKept')}</span>
                <span aria-hidden="true">{t('errors.cause.separator')}</span>
                <button type="button" className={KEPT_LINK_CLASS} onClick={openYourAI}>{t('errors.actions.setUp')}</button>
            </p>
        );
    }
    return (
        <p role="status" className="m-0 mt-1 text-right text-xs text-subtle">
            <span>{item.sendRetryFailed ? t('failure.still') : t('errors.failedSend.line')}</span>
            <span aria-hidden="true">{t('errors.cause.separator')}</span>
            <button type="button" className={KEPT_LINK_CLASS} onClick={() => actions.retry(item.id)}>{t('errors.actions.tryAgain')}</button>
            <span aria-hidden="true">{t('errors.cause.separator')}</span>
            <button type="button" className={KEPT_LINK_CLASS} onClick={() => actions.edit(item.id)}>{t('errors.failedSend.edit')}</button>
        </p>
    );
}
