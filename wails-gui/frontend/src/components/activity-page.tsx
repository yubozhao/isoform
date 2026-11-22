import { lastActivityDate, lastActivityScroll, lastTimelineFilters, rememberActivityDate, rememberActivityScroll, rememberTimelineFilters } from './timeline/activity-session-cache';
import { useCallback, useEffect, useId, useLayoutEffect, useMemo, useRef, useState } from 'react';
import * as DialogPrimitive from '@radix-ui/react-dialog';
import { useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';
import {
    X,
    MousePointer,
    Keyboard,
    AppWindow,
    Focus,
    ChevronLeft,
    ChevronRight,
    AlertTriangle,
    Film,
    Play,
    Pause,
    Maximize2,
    Minimize2,
    ChevronDown,
    ChevronUp,
    Check,
    Loader2,
    Calendar as CalendarIcon,
    CalendarDays,
    BookOpen,
    Mic,
    MicOff,
    Activity as ActivityIcon,
    Info,
    ClipboardCopy,
} from 'lucide-react';
import { ActivityOpenChatContext } from './activity-ask-inline';
import { ActivityInlineNotice, type ActivityNotice } from './activity-inline-notice';
import { SearchIcon } from '@/assets/icons/SearchIcon';
import { RedactionShieldIcon } from '@/components/icons/redaction-shield-icon';
import { ActivityDayView, type ActivityDaySection } from './activity-day-view';
import type { WorkedOnView } from './activity-stitched';
import type { MobileBriefActivityTarget } from '@/lib/wails-shim-app';
import { AutomationDetailDialog } from '@/components/automations/AutomationDetailDialog';
import {
    formatDateStr,
    formatRoundedDurationMs,
    formatTimeFromMs,
    formatTimeRangeFromMs,
    useCalendarData,
    type CronJobsProp,
} from './timeline/useCalendarData';
import { SnapshotImage } from './snapshot-image';
import { useSnapshotFallback } from '../hooks/useSnapshotFallback';
import { ScrollArea } from '@/components/ui/scroll-area';
import { Virtuoso } from 'react-virtuoso';
import {
    DropdownMenu,
    DropdownMenuTrigger,
    DropdownMenuContent,
    DropdownMenuItem,
    DropdownMenuSeparator,
    DropdownMenuLabel,
} from '@/components/ui/dropdown-menu';
import { Popover, PopoverTrigger, PopoverContent } from '@/components/ui/popover';
import { Calendar as CalendarWidget } from '@/components/ui/calendar';
import type { TodayChatContextRef } from '@/lib/today-chat-navigation';
import {
    GetActivitySessions,
    GetActivitySessionDetail,
    GetActivityOCRMatches,
    GetSnapshotOCRText,
    GetOCRHighlightBoxes,
    GetHostBridgePort,
    GetHostBridgeMediaToken,
    GetKnowledgeForSession,
    SemanticSearchActivitySessions,
    AddVoiceInputApp,
} from '../../wailsjs/go/main/App';
import {
    ChatSenderVerdictDTO,
    ClassifyActivityEvents,
    EventIdentityVerdictDTO,
    ListSessionPrincipals,
    ListSessionSpeakerClusters,
    SessionSpeakerClusterDTO,
    ResolveChatSender,
    SessionPrincipalDTO,
    GetActivityTimelineGroups,
    GetActivityTimelineGroupsForSessions,
    GenerateActivityTimelineGroupSummary,
    ActivityTimelineGroup,
} from '../lib/wails-shim-app';
import { cn } from '../lib/utils';
import { hostBridgeMediaURL, setHostBridgeMediaToken, snapshotImageURL } from '../lib/host-bridge-media';
import { ActivityMeetingNotes } from './activity-meeting-notes';
import { ActivityWatchRecordings } from './activity-watch-recordings';
import { ActivityFeed } from './activity-feed';
import { ActivityAnswers } from './activity-answers';
import { MarkdownContent } from '@/components/markdown-content';
import { CopyButton } from '@/components/copy-button';
import { isWindows } from '@/lib/platform';
import type { TodayTopic } from '@/components/today/topics';
import { EventsOn } from '../../wailsjs/runtime/runtime';
import type { main, activitymon } from '../../wailsjs/go/models';

/** The Activity page's sections (computer-activity v1.0.0 tabs: Your day,
 *  Screen, Watch, Agents, Your answers). 'workedOn' and 'timeline' are the
 *  Screen tab's two views. 'agents' is the feed's agent rows. */
export type ActivitySectionId = 'yourDay' | 'workedOn' | 'timeline' | 'watch' | 'agents' | 'answers';

/** The header's tabs, in order; Screen opens on What you worked on. */
const ACTIVITY_TABS = [
    { id: 'yourDay', labelKey: 'day.yourDay' },
    { id: 'workedOn', labelKey: 'act.tabScreen' },
    { id: 'watch', labelKey: 'act.tabWatch' },
    { id: 'agents', labelKey: 'act.tabAgents' },
    { id: 'answers', labelKey: 'act.tabAnswers' },
] as const satisfies readonly { id: ActivitySectionId; labelKey: string }[];
type ActivityTabId = (typeof ACTIVITY_TABS)[number]['id'];

interface ActivityPageProps {
    /** Optional close handler (Escape ladder's last rung). The page is a main
     *  view now, so App leaves it unset — Escape still closes popups and
     *  exits fullscreen. */
    onClose?: () => void;
    initialSessionId?: string | null;
    /** The day Your day opens on (YYYY-MM-DD). Defaults to today.
     *  This is what callers that used to open the Calendar page now drive. */
    initialDate?: string | null;
    /** Today's "open in Activity" for a suggestion. Isoform noticed left
     *  Activity (computer-conformance-answers A4), so it opens Your day on
     *  the target's date and is consumed at once. */
    noticedFocus?: MobileBriefActivityTarget | null;
    onNoticedFocusConsumed?: (focus: MobileBriefActivityTarget) => void;
    /** A thread the Day view's "What you worked on" opens and scrolls to. */
    initialThreadId?: string | null;
    onInitialThreadConsumed?: (threadId: string) => void;
    /** A watch conversation the Watch tab opens on arrival (App's
     *  openActivityConversation, with initialDate its day). */
    initialConversationId?: string | null;
    /** A row's topic opens beside Today (App's openTopic). */
    onOpenTopic?: (topic: TodayTopic) => void;
    /** Scheduled automations — powers the Day view's "Open result" dialog. */
    cronJobs: CronJobsProp;
    backButton?: {
        label: string;
        onClick: () => void;
    };
    /** The section the panel shows, and the change handler. Held by the
     *  parent (App) so the pick is remembered for the app session even
     *  though the page unmounts when another main view shows. */
    section: ActivitySectionId;
    onSectionChange: (section: ActivitySectionId) => void;
    /** Open in Chat under an in-place answer: shows the main chat. */
    onOpenChat?: () => void;
    /** Optional: "Chat about this" on a Day view moment. The parent leaves the
     *  page and puts the prepared words in the main chat box, unsent, with
     *  the moment as the chat's context (the Today route, item 64). */
    onStartChat?: (prompt: string, context: TodayChatContextRef) => void;
    /** Snapshots that should be visually flagged as "used by the triage
     *  prompt" — set when the Activity Pipeline settings page jumps here so
     *  an operator can audit the sampling strategy against the rest of the
     *  group. Each entry is matched against the visible session detail by
     *  (sessionId, snapshotId). `kind` distinguishes frames whose content
     *  was rendered into the prompt (parsed / ocr → green/sky tick) from
     *  dedup siblings whose content was represented by a winner snapshot
     *  (parsed_sibling → muted gray tick) and frames the pipeline actively
     *  considered then dropped (parsed_skipped / ocr_skipped → amber tick;
     *  skipReason explains why). */
    highlightedFrames?: {
        kind: string;
        sessionId: string;
        snapshotId: number;
        /** What this frame actually wrote into the triage prompt — already
         *  dedup'd and rune-aware truncated. Used by the "As seen by LLM"
         *  inline panel so the operator can read exactly what the model
         *  received instead of guessing from raw snapshot data. */
        promptText?: string;
        /** For non-winner kinds (parsed_sibling / parsed_skipped /
         *  ocr_skipped), the machine-readable reason code emitted by the
         *  triager. See SelectedFrame.SkipReason in
         *  wails-gui/activity_pipeline_runs.go for the canonical list. */
        skipReason?: string;
        /** P1 quality metadata — observability-only. See the field doc on
         *  SelectedFrame in wails-gui/activity_pipeline_runs.go. */
        ocrConfidence?: number;
        parsedSiblingCount?: number;
        browserUrl?: string;
        dedupChainLen?: number;
    }[];
    /** Pipeline run ID labelled on the per-snapshot badge so the operator
     *  knows which run drove the highlight. Only meaningful when
     *  highlightedFrames is non-empty. */
    highlightedPipelineRunId?: number | null;
    /** When non-empty, the sidebar shows ONLY these sessions instead of every
     *  session in the active date filter. Set by the Activity Pipeline jump
     *  so operators see exactly the merged group's session list — including
     *  members that contributed 0 frames (which therefore won't appear in
     *  highlightedFrames). Sessions outside the current time range are
     *  fetched on demand via GetActivitySessionDetail. */
    restrictToSessionIds?: string[];
    /** When set, the page jumps to the snapshot whose timestamp is
     *  closest to this value after the session detail loads. Used by
     *  the Who you know page when the user clicks an Activity session row —
     *  the timestamp is that person's last_seen_at in the session, so
     *  the user lands on the key frame where Isoform actually saw them
     *  instead of the session's first snapshot. */
    initialEventTimestamp?: number | null;
    /** When set, every chat message inside the right-pane parsed-content
     *  panels whose sender name contains this string (case-insensitive
     *  substring match) is rendered with a terracotta accent. Lets a
     *  People → Activity jump immediately point at the message that
     *  triggered it, without forcing the user to scan. */
    highlightedSenderName?: string | null;
}

/** Format ms timestamp to HH:MM:SS */
function formatTime(ms: number): string {
    const d = new Date(ms);
    return d.toLocaleTimeString([], { hour: 'numeric', minute: '2-digit' });
}

/** Extract a short, human-readable preview from a snapshot row for the
 *  timeline hover card. Goal is to let an operator scan all frames in a
 *  session by hovering, without opening the popup for each one. */
function extractSnapshotPreview(snap: main.ActivitySnapshotRow): {
    app: string;
    parsedKind: string;
    text: string;
} {
    let parsedKind = '';
    if (snap.parsedJson) {
        try {
            const parsed = JSON.parse(snap.parsedJson);
            if (typeof parsed?.dedupOfHash === 'string' && parsed.dedupOfHash.length > 0) {
                parsedKind = `dedup → ${String(parsed.dedupOfHash).slice(0, 8)}`;
            } else if (typeof parsed?.contentType === 'string') {
                parsedKind = parsed.contentType;
            } else if (typeof snap.contentType === 'string' && snap.contentType.length > 0) {
                parsedKind = snap.contentType;
            }
        } catch {
            // parsed_json malformed — fall back to row's contentType
            if (snap.contentType) parsedKind = snap.contentType;
        }
    }
    const text = (snap.axText || '').replace(/\s+/g, ' ').trim().slice(0, 240);
    return { app: snap.app || '', parsedKind, text };
}

/** Format ms timestamp to HH:MM */
function formatTimeShort(ms: number): string {
    return formatTimeFromMs(ms);
}

/** A session's length: "3 min" under an hour, "1h 10m" past it, rounded to
 *  the minute as the list rounds (activity review v1.0.5 V2, v1.0.7 AR14). */
function formatSessionDuration(ms: number, minutes: (count: number) => string): string {
    const min = Math.max(1, Math.round(ms / 60_000));
    return min < 60 ? minutes(min) : formatRoundedDurationMs(ms);
}

/** A transcript too thin to be a step (activity review v1.0.5 V6), as in Go's
 *  timelineSpeechIsScrap: under four words, or under six characters in a
 *  language written without spaces. */
export function isSpeechScrap(text?: string): boolean {
    const trimmed = (text ?? '').trim();
    const words = trimmed.split(/\s+/).filter(Boolean);
    if (words.length > 1) return words.length < 4;
    return [...trimmed].length < 6;
}

/** Minutes only, rounded; under a minute reads "1m" (activity review v1.0.7 AR14). */
function formatTimelineGroupDuration(ms: number): string {
    return formatRoundedDurationMs(ms);
}

/** Format bytes to human-readable string */
function formatBytes(bytes: number): string {
    if (bytes < 1024) return `${bytes} B`;
    if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
    return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

const browserBundleIds = new Set([
    'com.google.Chrome',
    'com.brave.Browser',
    'com.microsoft.edgemac',
    'com.apple.Safari',
    'com.apple.SafariTechnologyPreview',
    'com.vivaldi.Vivaldi',
    'company.thebrowser.Browser',
    'org.mozilla.firefox',
    'org.mozilla.firefoxdeveloperedition',
]);

function snapshotMatchesEventBundle(
    snapshot: main.ActivitySnapshotRow,
    snapshotBundleId: string | undefined,
    eventBundleId: string,
): boolean {
    if (snapshotBundleId === eventBundleId) return true;
    if (snapshotBundleId) return false;
    return browserBundleIds.has(eventBundleId) && Boolean(snapshot.browserUrl);
}

function groupTimelineGroupsByDate(
    timelineGroups: ActivityTimelineGroup[],
    todayLabel: string,
    yesterdayLabel: string,
): Map<string, ActivityTimelineGroup[]> {
    const groups = new Map<string, ActivityTimelineGroup[]>();
    const today = new Date();
    today.setHours(0, 0, 0, 0);
    const yesterday = new Date(today);
    yesterday.setDate(yesterday.getDate() - 1);

    for (const group of timelineGroups) {
        const d = new Date(group.startTime);
        d.setHours(0, 0, 0, 0);
        let label: string;
        if (d.getTime() === today.getTime()) {
            label = todayLabel;
        } else if (d.getTime() === yesterday.getTime()) {
            label = yesterdayLabel;
        } else {
            label = d.toLocaleDateString([], { month: 'short', day: 'numeric', year: 'numeric' });
        }
        const existing = groups.get(label) ?? [];
        existing.push(group);
        groups.set(label, existing);
    }
    return groups;
}


/** Icon for event kind */
function EventKindIcon({ kind }: { kind: string }) {
    switch (kind) {
        case 'click':
            return <MousePointer className="h-3.5 w-3.5" />;
        case 'keyboard':
            return <Keyboard className="h-3.5 w-3.5" />;
        case 'app_switch':
            return <AppWindow className="h-3.5 w-3.5" />;
        case 'focus':
            return <Focus className="h-3.5 w-3.5" />;
        case 'speech':
            return <Mic className="h-3.5 w-3.5" />;
        case 'calendar_peek':
            return <CalendarDays className="h-3.5 w-3.5" />;
        case 'device_in_use':
        case 'session_state':
            return <ActivityIcon className="h-3.5 w-3.5" />;
        case 'clipboard':
            return <ClipboardCopy className="h-3.5 w-3.5" />;
        default:
            return <Film className="h-3.5 w-3.5" />;
    }
}

// EventIdentityBadge renders the identity-system verdict for one captured
// event. It's gated behind the Activity page's "Show identity verdicts"
// toggle so end users never see it — the badge is a debug surface for the
// developer to verify the classifier is doing what they expect.
//
// Color coding mirrors the conceptual hazard ladder:
//   - performed → green: the user did this, attribute freely
//   - received  → amber: a known counterparty did this, attribute with care
//   - observed  → blue:  user was consuming media, do not attribute
//   - ambient   → gray:  environmental noise, skip
//   - unknown   → zinc:  classifier doesn't know, treat conservatively
function EventIdentityBadge({ verdict }: { verdict: EventIdentityVerdictDTO }) {
    const { t } = useTranslation('activity');
    const tone = identityToneFor(verdict.contentMode);
    const hints = verdict.speechHints;
    return (
        <Popover>
            <PopoverTrigger asChild>
                <button
                    type="button"
                    className={cn(
                        'inline-flex items-center gap-1 h-4 px-1.5 rounded text-[9px] font-semibold uppercase tracking-wide shrink-0 transition-opacity',
                        tone.chip,
                    )}
                    title={verdict.reasoning}
                    aria-label={t('identityDebug.badgeAria', { mode: verdict.contentMode }) as string}
                    onClick={(e) => e.stopPropagation()}
                >
                    {verdict.contentMode}
                </button>
            </PopoverTrigger>
            <PopoverContent
                side="left"
                align="start"
                className="w-[340px] p-3 text-[12px] leading-relaxed"
                onClick={(e) => e.stopPropagation()}
            >
                <div className="flex flex-col gap-2">
                    <div className="flex items-center gap-1.5">
                        <span
                            className={cn(
                                'inline-flex items-center h-4 px-1.5 rounded text-[9px] font-semibold uppercase tracking-wide',
                                tone.chip,
                            )}
                        >
                            {verdict.contentMode}
                        </span>
                        <span className="text-muted">{t('identityDebug.modeHeader')}</span>
                    </div>
                    <p className="text-ink/85">{verdict.reasoning}</p>
                    {(hints.source !== undefined ||
                        hints.likelyEcho ||
                        hints.nonUserSpeech ||
                        hints.mediaApp ||
                        hints.browserMedia ||
                        hints.meetingContext) && (
                        <div className="border-t border-border pt-2 flex flex-col gap-0.5 text-muted">
                            <p className="text-[10px] font-semibold uppercase tracking-wide text-ink/70">
                                {t('identityDebug.speechHintsHeader')}
                            </p>
                            {hints.source && (
                                <p>
                                    <span className="text-ink/70">source</span>: {hints.source}
                                </p>
                            )}
                            {hints.likelyEcho && <p>{t('identityDebug.hintLikelyEcho')}</p>}
                            {hints.nonUserSpeech && <p>{t('identityDebug.hintNonUserSpeech')}</p>}
                            {hints.mediaApp && <p>{t('identityDebug.hintMediaApp')}</p>}
                            {hints.browserMedia && <p>{t('identityDebug.hintBrowserMedia')}</p>}
                            {hints.meetingContext && <p>{t('identityDebug.hintMeetingContext')}</p>}
                        </div>
                    )}
                </div>
            </PopoverContent>
        </Popover>
    );
}

function identityToneFor(mode: string): { chip: string } {
    switch (mode) {
        case 'performed':
            return { chip: 'bg-panel-user text-ink' };
        case 'received':
            return { chip: 'bg-panel-warm text-muted' };
        case 'observed':
            return { chip: 'bg-info/15 text-info' };
        case 'ambient':
            return { chip: 'bg-surface-hover/15 text-muted dark:text-muted' };
        default:
            return { chip: 'bg-surface-hover/10 text-muted' };
    }
}

// SessionPeopleChips renders the row of people Isoform identified in
// the current Activity session. Clicking a chip toggles a local
// sender-highlight on/off; the highlight propagates into every
// ParsedContentSection on the page so the user can quickly see
// every message that person sent during the session. Selection stays
// on a neutral surface with ink for both the user and other people.
export function SessionPeopleChips({
    principals,
    selectedName,
    onSelect,
}: {
    principals: SessionPrincipalDTO[];
    selectedName: string | null;
    onSelect: (name: string) => void;
}) {
    const { t } = useTranslation('activity');
    return (
        <div className="flex items-center gap-1.5 flex-wrap pt-0.5">
            <span className="text-[11px] uppercase tracking-wide text-muted font-semibold mr-1">
                {t('session.peopleHeading', { count: principals.length })}
            </span>
            {principals.map((p) => {
                const isSelected =
                    selectedName !== null && selectedName.toLowerCase() === p.displayName.toLowerCase();
                const isSelf = p.kind === 'self';
                const label = p.displayName;
                return (
                    <button
                        key={p.principalId}
                        type="button"
                        onClick={() => onSelect(p.displayName)}
                        aria-pressed={isSelected}
                        className={cn(
                            'inline-flex items-center gap-1 h-5 px-2 rounded-full text-[11px] font-medium transition-colors',
                            isSelected
                                ? 'bg-surface-active text-ink ring-1 ring-accent'
                                : isSelf
                                  ? 'bg-surface-active text-ink hover:bg-surface-hover'
                                  : 'bg-panel-warm text-ink/80 hover:bg-border',
                        )}
                        title={
                            isSelected
                                ? (t('session.peopleChipClearTitle') as string)
                                : (t('session.peopleChipSelectTitle', { name: label }) as string)
                        }
                    >
                        <span className="truncate max-w-[180px]">{label}</span>
                    </button>
                );
            })}
        </div>
    );
}

// SessionSpeakerChips renders one chip per distinct voice detected in
// the session ("You" + "Speaker 1" / "Speaker 2" / ...). Clicking a
// chip toggles a local selection that the parent uses to dim
// unrelated speech events in the timeline. Distinct from
// SessionPeopleChips — that surfaces the resolved chat-sender
// identity from text parsers; this surfaces the voice-fingerprint
// clustering on captured mic audio.
export function SessionSpeakerChips({
    clusters,
    selectedLabel,
    onSelect,
}: {
    clusters: SessionSpeakerClusterDTO[];
    selectedLabel: string | null;
    onSelect: (label: string) => void;
}) {
    const { t } = useTranslation('activity');
    return (
        <div className="flex items-center gap-1.5 flex-wrap pt-0.5">
            <span className="text-[11px] uppercase tracking-wide text-muted font-semibold mr-1">
                {t('session.voicesHeading', { count: clusters.length })}
            </span>
            {clusters.map((c) => {
                const isSelected =
                    selectedLabel !== null && selectedLabel === c.label;
                return (
                    <button
                        key={c.label}
                        type="button"
                        onClick={() => onSelect(c.label)}
                        aria-pressed={isSelected}
                        className={cn(
                            'inline-flex items-center gap-1 h-5 px-2 rounded-full text-[11px] font-medium transition-colors',
                            isSelected
                                ? 'bg-surface-active text-ink ring-1 ring-accent'
                                : c.isSelf
                                  ? 'bg-surface-active text-ink hover:bg-surface-hover'
                                  : 'bg-panel-warm text-ink/80 hover:bg-border',
                        )}
                        title={
                            t('session.voiceChipTitle', {
                                label: c.label,
                                count: c.utteranceCount,
                            }) as string
                        }
                    >
                        <span>{c.label}</span>
                        <span className="text-[10px] opacity-70 tabular-nums">
                            {c.utteranceCount}
                        </span>
                    </button>
                );
            })}
        </div>
    );
}

// ChatSenderBadge resolves a single chat sender via ResolveChatSender and
// renders the resulting prompt-style label inline next to the raw sender
// name. Used by ParsedContentSection when the identity debug toggle is on
// so the developer can see how each message's author would resolve in the
// real triage pipeline.
function ChatSenderBadge({ app, rawSender }: { app: string; rawSender: string }) {
    const [verdict, setVerdict] = useState<ChatSenderVerdictDTO | null>(null);
    useEffect(() => {
        let cancelled = false;
        ResolveChatSender(app, rawSender)
            .then((v) => {
                if (!cancelled) setVerdict(v);
            })
            .catch(() => {
                if (!cancelled) setVerdict(null);
            });
        return () => {
            cancelled = true;
        };
    }, [app, rawSender]);
    if (!verdict) return null;
    const tone =
        verdict.kind === 'self'
            ? 'bg-surface-active text-ink'
            : verdict.kind === 'contact'
              ? 'bg-panel-warm text-ink'
              : verdict.kind === 'anonymous'
                ? 'bg-panel-warm text-muted'
                : 'bg-surface-hover/15 text-muted';
    return (
        <span
            className={cn(
                'ml-1.5 inline-flex items-center h-4 px-1.5 rounded text-[9px] font-medium tracking-wide',
                tone,
            )}
            title={
                verdict.resolved
                    ? `${verdict.kind} (via ${verdict.attributionMethod}, confidence ${verdict.confidence.toFixed(2)})`
                    : 'no matching principal in store'
            }
        >
            {verdict.label}
        </span>
    );
}

/** Trigger kind to readable badge. Returns empty string for kinds we hide from the UI.
 *
 * Note on idle_transition / speech_idle_transition: these describe the
 * trigger that *ended* a session (no input for 30s, or no speech for
 * 5min during listening). They're the default boundary for ordinary
 * work, so tagging every such session "Idle" both floods the UI with
 * noise and misreads — a session full of work events gets labeled with
 * a word that implies the user wasn't working. We drop the badge for
 * these cases and keep it only for signals the user actually cares
 * about (Meeting, Scheduled). */
function triggerLabel(kind: string, t: (key: string) => string): string {
    switch (kind) {
        case 'pre_meeting_flush':
        case 'meeting_flush':
        case 'meeting_ended':
        case 'shutdown_in_meeting':
            return t('triggerLabels.meeting');
        case 'scheduled':
        case 'catchup':
            return t('triggerLabels.scheduled');
        default:
            return '';
    }
}

// ── Event frame popup ─────────────────────────────────────────────────
interface EventPopupData {
    frameSrc: string;
    evt: activitymon.ActivityEvent;
    snapshotPath?: string; // relative path to snapshot file for OCR highlight
    // Full snapshot row when this event maps to a snapshot-based session.
    // Carries everything we need to surface "what did we capture from this
    // frame?" — parsedJson (app-specific structured content like Slack
    // messages), axText (raw accessibility tree dump), contentType, app.
    // Legacy HEVC-segment events leave this undefined.
    snapshot?: main.ActivitySnapshotRow;
    // Sibling snapshots in the same session, used to resolve dedup stubs
    // (parsedJson with `dedupOfHash`) against the original full payload
    // whose metadata.contentHash matches.
    siblingSnapshots?: main.ActivitySnapshotRow[];
}

// Mirror of the Go ParsedContent struct
// (wails-gui/internal/platform/activitymon/appparser/appparser.go).
// Defined here as a TS interface so we don't depend on regenerated bindings
// for transport-only payloads — snapshot.parsedJson is a serialized blob.
interface ParsedConversationMessage {
    sender?: string;
    content: string;
    timestamp?: string;
    // Set by chat parsers (currently slack) when this message is a thread
    // reply. Format: "<parentSender>@<parentTimestamp>" — used purely as
    // a visual indent / reference hint in the UI, not for structural
    // re-grouping (the backend already places replies directly under
    // their parent in the messages array).
    threadParent?: string;
}
interface ParsedContent {
    contentType?: string;
    title?: string;
    body?: string;
    url?: string;
    channel?: string;
    messages?: ParsedConversationMessage[];
    metadata?: Record<string, string>;
    // Set when the backend elided a redundant payload to a stub. The
    // hash points at a sibling snapshot in the same session burst whose
    // metadata.contentHash matches — render that body instead.
    dedupOfHash?: string;
}

function timelineSnapshotBundleId(snapshot: main.ActivitySnapshotRow): string | null {
    if (!snapshot.parsedJson) return null;
    try {
        const parsed = JSON.parse(snapshot.parsedJson) as ParsedContent;
        return parsed.metadata?.bundleId ?? null;
    } catch {
        return null;
    }
}

function timelineGroupMatchesSnapshot(group: ActivityTimelineGroup, snapshot: main.ActivitySnapshotRow): boolean {
    if (snapshot.timestamp < group.startTime || snapshot.timestamp > group.endTime) {
        return false;
    }

    const parsedBundle = timelineSnapshotBundleId(snapshot);
    if (parsedBundle) {
        return parsedBundle === group.appKey || parsedBundle === group.appKey.replace(/^app:/, '');
    }

    if (snapshot.app) {
        return snapshot.app.localeCompare(group.appName, undefined, { sensitivity: 'accent' }) === 0;
    }

    // Older rows may not have parsed metadata. Keep the time-window fallback so
    // selecting a short app run still has a representative frame instead of an
    // empty Recording section.
    return true;
}

function buildTimelineGroupDetail(
    group: ActivityTimelineGroup,
    sessionDetails: main.ActivitySessionDetail[],
): main.ActivitySessionDetail {
    const snapshots = sessionDetails
        .flatMap((d) => d.snapshots ?? [])
        .filter((snapshot) => timelineGroupMatchesSnapshot(group, snapshot))
        .sort((a, b) => a.timestamp - b.timestamp);
    const segments = sessionDetails
        .flatMap((d) => d.segments ?? [])
        .filter((segment) => segment.endTime >= group.startTime && segment.startTime <= group.endTime)
        .sort((a, b) => a.startTime - b.startTime);
    const events = (group.events ?? [])
        .slice()
        .sort((a, b) => a.timestamp - b.timestamp)
        .map((event) => event as activitymon.ActivityEvent);
    const triggerKind = (group.triggerKinds ?? []).join(', ');

    return {
        id: group.id,
        startTime: group.startTime,
        endTime: group.endTime,
        triggerKind,
        appNames: [group.appName],
        eventCount: group.eventCount,
        snapshotCount: snapshots.length,
        triageStatus: '',
        ocrStatus: '',
        events,
        segments,
        snapshots,
    } as main.ActivitySessionDetail;
}

// Index of the snapshot whose timestamp sits closest to `ts`. Used to land a
// People/knowledge jump on the moment the caller cares about instead of the
// session's first frame.
function closestSnapshotIndexTo(snapshots: ReadonlyArray<{ timestamp: number }>, ts: number): number {
    let bestIdx = 0;
    let bestDist = Infinity;
    for (let i = 0; i < snapshots.length; i++) {
        const dist = Math.abs(snapshots[i].timestamp - ts);
        if (dist < bestDist) {
            bestDist = dist;
            bestIdx = i;
        }
    }
    return bestIdx;
}

/** Restores the timeline session list's scroll offset after a revisit once
 *  the list has rows again. Renders a marker inside the Radix viewport; the
 *  closest() walk finds the viewport around it. Deep links skip the restore
 *  (their anchor scroll owns the position). */
function SessionListScrollRestore({ enabled, ready }: { enabled: boolean; ready: boolean }) {
    const markerRef = useRef<HTMLSpanElement>(null);
    const doneRef = useRef(false);
    useLayoutEffect(() => {
        if (!enabled || !ready || doneRef.current) return;
        const saved = lastActivityScroll('timeline-sessions');
        const viewport = markerRef.current?.closest('[data-radix-scroll-area-viewport]');
        if (saved && viewport) viewport.scrollTop = saved;
        doneRef.current = true;
    }, [enabled, ready]);
    return <span ref={markerRef} hidden />;
}

function timelineEventId(event: activitymon.ActivityEvent): string | null {
    const maybe = event as activitymon.ActivityEvent & { eventId?: string };
    return maybe.eventId ?? null;
}

// mergeTimelineGroups unions extra groups (fetched by session id for anchored
// entry points) into the range-bound list, deduped by stable group id and
// re-sorted newest-first for the sidebar.
function mergeTimelineGroups(
    base: ActivityTimelineGroup[],
    extra: ActivityTimelineGroup[],
): ActivityTimelineGroup[] {
    if (extra.length === 0) return base;
    const seen = new Set(base.map((g) => g.id));
    const merged = base.slice();
    for (const group of extra) {
        if (seen.has(group.id)) continue;
        seen.add(group.id);
        merged.push(group);
    }
    merged.sort((a, b) => b.startTime - a.startTime);
    return merged;
}

function splitSemanticAction(action: string): { verb: string; rest: string } {
    const trimmed = action.trim();
    if (!trimmed) {
        return { verb: '', rest: '' };
    }
    const match = trimmed.match(/^(\S+)(?:\s+(.*))?$/);
    return {
        verb: match?.[1] ?? trimmed,
        rest: match?.[2] ?? '',
    };
}

// Renders the parsed structured content extracted from a snapshot
// (Slack messages, browser page bodies, terminal output, etc.). Layout
// branches on contentType — chat-style for "conversation", title/body
// for everything else. Falls back to a body dump if the parser only
// produced unstructured text.
function ParsedContentSection({
    parsed,
    keyword,
    identityDebugOn = false,
    highlightedSenderName,
}: {
    parsed: ParsedContent;
    keyword: string;
    identityDebugOn?: boolean;
    /** When non-empty, every chat message whose sender contains this
     *  string (case-insensitive substring match — tolerant of parser
     *  leaks like "yetone Server Tag: CHRM" while the principal name
     *  is just "yetone") gets a terracotta accent on the sender label
     *  and a soft tint on the row, so the user lands on the exact
     *  message that drove the Who you know → Activity jump. */
    highlightedSenderName?: string | null;
}) {
    const { contentType, title, body, url, channel, messages, metadata } = parsed;
    const app = metadata?.app ?? '';

    if (contentType === 'conversation' && messages && messages.length > 0) {
        return (
            <div>
                {channel && (
                    <div className="text-[11px] text-muted font-medium mb-2 truncate">
                        <HighlightText text={channel} keyword={keyword} />
                    </div>
                )}
                <div className="space-y-2.5">
                    {messages.map((m, i) => {
                        const isReply = !!m.threadParent;
                        const hl = (highlightedSenderName ?? '').trim().toLowerCase();
                        const senderMatchesHighlight =
                            hl !== '' && (m.sender ?? '').toLowerCase().includes(hl);
                        return (
                            <div
                                key={i}
                                className={cn(
                                    'text-sm leading-relaxed',
                                    isReply && 'pl-4 border-l-2 border-border/60',
                                    senderMatchesHighlight &&
                                        '-mx-2 px-2 py-1 rounded-md bg-surface-active',
                                )}
                            >
                                <div className="flex items-baseline gap-2 text-[11px] text-muted">
                                    {isReply && (
                                        <span
                                            aria-hidden
                                            className="text-muted/70 select-none"
                                            title={`Reply in thread to ${m.threadParent}`}
                                        >
                                            ↳
                                        </span>
                                    )}
                                    <span
                                        className={cn(
                                            'font-medium',
                                            senderMatchesHighlight
                                                ? 'text-accent-2 font-semibold'
                                                : 'text-ink',
                                        )}
                                    >
                                        <HighlightText text={m.sender || '—'} keyword={keyword} />
                                    </span>
                                    {identityDebugOn && app && m.sender && (
                                        <ChatSenderBadge app={app} rawSender={m.sender} />
                                    )}
                                    {m.timestamp && (
                                        <span className="tabular-nums">{m.timestamp}</span>
                                    )}
                                </div>
                                <div className="text-ink whitespace-pre-wrap break-words">
                                    <HighlightText text={m.content} keyword={keyword} />
                                </div>
                            </div>
                        );
                    })}
                </div>
            </div>
        );
    }

    // Generic shape: title + url + body (covers document / task_list /
    // events / terminal / web_page / unknown).
    const hasAny = title || url || body;
    if (!hasAny) return null;
    return (
        <div className="space-y-1.5">
            {title && (
                <div className="text-sm font-medium text-ink">
                    <HighlightText text={title} keyword={keyword} />
                </div>
            )}
            {url && (
                <div className="text-[11px] text-muted truncate font-mono">
                    <HighlightText text={url} keyword={keyword} />
                </div>
            )}
            {body && (
                <div className="text-sm text-ink whitespace-pre-wrap break-words leading-relaxed">
                    <HighlightText text={body} keyword={keyword} />
                </div>
            )}
        </div>
    );
}

/**
 * Collapsible text section used by the event popup for OCR and the
 * platform-native accessibility/UIA text. Renders a chevron-up/down toggle
 * instead of plain "Expand/Collapse" labels so the popup chrome stays
 * compact. Shows `emptyLabel` italicized when `text` is empty.
 *
 * The expanded body is bounded with max-h + overflow-auto so a UIA dump
 * carrying 20-30 KB of text (Slack threads, GitHub commits pages) doesn't
 * inflate the popup DOM. WebView2 on Windows is sensitive to enormous
 * <pre> nodes and a freezing popup has historically been able to drag
 * the tray message pump down with it.
 */
function CollapsibleTextSection({
    title,
    text,
    emptyLabel,
    keyword,
    defaultOpen = true,
    bodyClassName = 'text-xs text-ink whitespace-pre-wrap break-words font-sans leading-relaxed',
}: {
    title: string;
    text: string;
    emptyLabel?: string;
    keyword: string;
    defaultOpen?: boolean;
    bodyClassName?: string;
}) {
    const [open, setOpen] = useState(defaultOpen);
    return (
        <section className="border-b border-border/60">
            <button
                type="button"
                onClick={() => setOpen((o) => !o)}
                className="w-full px-4 py-3 flex items-center justify-between text-[11px] uppercase tracking-wide text-muted font-semibold hover:bg-surface-hover cursor-pointer"
            >
                <span>{title}</span>
                {open ? (
                    <ChevronUp className="h-3 w-3 shrink-0" />
                ) : (
                    <ChevronDown className="h-3 w-3 shrink-0" />
                )}
            </button>
            {open &&
                (text ? (
                    <pre
                        className={`px-4 pb-3 max-h-80 overflow-y-auto ${bodyClassName}`}
                    >
                        <HighlightText text={text} keyword={keyword} />
                    </pre>
                ) : emptyLabel ? (
                    <div className="px-4 pb-3 text-xs text-muted italic">{emptyLabel}</div>
                ) : null)}
        </section>
    );
}

function CollapsibleParsedSection({
    snapshot,
    evt,
    parsed,
    hasParsedRender,
    dedupResolved,
    dedupUnresolved,
    keyword,
    identityDebugOn,
    highlightedSenderName,
}: {
    snapshot: main.ActivitySnapshotRow;
    evt: activitymon.ActivityEvent;
    parsed: ParsedContent | null;
    hasParsedRender: boolean;
    dedupResolved: boolean;
    dedupUnresolved: boolean;
    keyword: string;
    identityDebugOn?: boolean;
    highlightedSenderName?: string | null;
}) {
    const { t } = useTranslation('activity');
    const [open, setOpen] = useState(false);
    return (
        <section className="border-b border-border/60">
            <button
                type="button"
                onClick={() => setOpen((o) => !o)}
                className="w-full px-4 py-3 flex items-center justify-between text-[11px] uppercase tracking-wide text-muted hover:bg-surface-hover cursor-pointer"
            >
                <div className="flex items-center gap-2">
                    <span className="font-semibold">{t('popup.parsedContent')}</span>
                    {snapshot.app ? (
                        <span className="px-1.5 py-0.5 rounded bg-panel-warm text-ink font-mono normal-case tracking-normal">
                            {snapshot.app}
                        </span>
                    ) : evt.appName ? (
                        <span className="px-1.5 py-0.5 rounded bg-panel-warm text-muted font-mono normal-case tracking-normal">
                            {evt.appName}
                        </span>
                    ) : null}
                    {snapshot.contentType && (
                        <span className="text-muted normal-case tracking-normal">
                            {snapshot.contentType}
                        </span>
                    )}
                    {dedupResolved && (
                        <span
                            className="px-1.5 py-0.5 rounded bg-panel-warm text-muted normal-case tracking-normal"
                            title={t('popup.parsedDeduped') as string}
                        >
                            {t('popup.parsedDeduped')}
                        </span>
                    )}
                </div>
                {open ? (
                    <ChevronUp className="h-3 w-3 shrink-0" />
                ) : (
                    <ChevronDown className="h-3 w-3 shrink-0" />
                )}
            </button>
            {open && (
                <div className="px-4 pb-3">
                    {hasParsedRender && parsed ? (
                        <ParsedContentSection
                            parsed={parsed}
                            keyword={keyword}
                            identityDebugOn={identityDebugOn}
                            highlightedSenderName={highlightedSenderName}
                        />
                    ) : (
                        <div className="text-xs text-muted italic">
                            {dedupUnresolved
                                ? t('popup.parsedDedupedUnresolved')
                                : parsed
                                  ? t('popup.parsedEmpty')
                                  : t('popup.parserMiss')}
                        </div>
                    )}
                </div>
            )}
        </section>
    );
}

export function EventFramePopup({
    data: incomingData,
    keyword,
    onClose,
    onPrev,
    onNext,
    identityDebugOn = false,
    highlightedSenderName,
}: {
    data: EventPopupData;
    keyword: string;
    onClose: () => void;
    /** Navigate to the previous/next event in the session timeline.
     *  Undefined means there is nothing to navigate to (first/last event,
     *  or the popup is showing a synthetic event like an OCR match that
     *  isn't part of detail.events). Buttons render as disabled in that
     *  case rather than disappearing so the affordance stays predictable. */
    onPrev?: () => void;
    onNext?: () => void;
    identityDebugOn?: boolean;
    /** Forwarded into ParsedContentSection so chat messages from this
     *  principal get a terracotta accent. See ParsedContentSection. */
    highlightedSenderName?: string | null;
}) {
    const { t } = useTranslation('activity');

    // Lag the data prop behind a hidden image preload so navigation
    // doesn't swap the visible <img>'s src out from under a half-painted
    // frame. Without this, even with `loaded` left at true and the prior
    // bitmap kept in place, the browser briefly paints empty space on
    // some swaps (cache miss + decode race) — the "occasional flicker"
    // users see. Holding the entire `data` object (not just frameSrc)
    // until the new image is in the decode cache means parsed-content,
    // OCR text, header app/title — everything visible in the popup —
    // swap to the new event atomically, so the user never sees a
    // mixed-event state (e.g. new title above old image). The image
    // load + parsed/OCR fetches all hit the same backend so the
    // preload usually finishes before the slowest of those is needed.
    const [data, setData] = useState<EventPopupData>(incomingData);
    useEffect(() => {
        if (incomingData === data) return;
        let cancelled = false;
        const preload = new Image();
        const finish = () => {
            if (cancelled) return;
            setData(incomingData);
        };
        preload.onload = finish;
        // On preload error we still advance to the new data — the
        // visible <img> will hit the same URL and surface its own
        // error state through the existing onError handler.
        preload.onerror = finish;
        preload.src = incomingData.frameSrc;
        return () => {
            cancelled = true;
        };
    }, [incomingData, data]);

    const [loaded, setLoaded] = useState(false);
    const [error, setError] = useState(false);
    const [highlightBoxes, setHighlightBoxes] = useState<main.OCRHighlightBox[]>([]);
    const imgRef = useRef<HTMLImageElement>(null);
    const containerRef = useRef<HTMLDivElement>(null);
    const [imgLayout, setImgLayout] = useState<{ left: number; top: number; width: number; height: number } | null>(null);

    // Zoom & pan state
    const [scale, setScale] = useState(1);
    const [translate, setTranslate] = useState({ x: 0, y: 0 });
    const isPanning = useRef(false);
    const panStart = useRef({ x: 0, y: 0 });
    const translateStart = useRef({ x: 0, y: 0 });

    // Fetch OCR highlight boxes when snapshot path + keyword are available
    useEffect(() => {
        if (!keyword || !data.snapshotPath) {
            setHighlightBoxes([]);
            return;
        }
        GetOCRHighlightBoxes(data.snapshotPath, keyword)
            .then((boxes) => setHighlightBoxes(boxes ?? []))
            .catch(() => setHighlightBoxes([]));
    }, [data.snapshotPath, keyword]);

    // Per-frame OCR text (separate from the keyword-bounded highlight boxes
    // above). Empty string means the OCR worker either ran and extracted
    // nothing meaningful (text shorter than ocrMinTextLen is dropped at write
    // time) or hasn't processed this snapshot yet — both are surfaced as a
    // single "no OCR" affordance below.
    const [ocrText, setOcrText] = useState<string>('');
    const [ocrLoaded, setOcrLoaded] = useState(false);
    const [ocrFailed, setOcrFailed] = useState(false);
    const [ocrAttempt, setOcrAttempt] = useState(0);
    useEffect(() => {
        const snap = data.snapshot;
        if (!snap?.id || !snap.sessionId) {
            setOcrText('');
            setOcrLoaded(true);
            return;
        }
        setOcrLoaded(false);
        setOcrFailed(false);
        GetSnapshotOCRText(snap.sessionId, snap.id)
            .then((text) => setOcrText(text ?? ''))
            .catch((err) => {
                console.error('[activity] snapshot OCR read failed:', err);
                setOcrText('');
                setOcrFailed(true);
            })
            .finally(() => setOcrLoaded(true));
    }, [data.snapshot?.id, data.snapshot?.sessionId, ocrAttempt]);

    // App-specific parser output (Slack messages, browser page bodies, etc.).
    // Stored on the snapshot row as a JSON blob; deserialize once per frame.
    // When the blob is a `dedupOfHash` stub, resolve it against sibling
    // snapshots — the backend keeps one full payload per (bundle, content)
    // pair within a session burst and elides the redundant repeats.
    //
    // The hash → ParsedContent index is computed separately from the
    // current-snapshot decode so it only rebuilds when the sibling array
    // identity changes (i.e. the user navigated to a different session).
    // Switching between snapshots within the same session reuses the
    // same map for O(1) stub resolution instead of re-parsing every
    // sibling on each click.
    const siblingParsedByHash = useMemo<Map<string, ParsedContent>>(() => {
        const map = new Map<string, ParsedContent>();
        for (const s of data.siblingSnapshots ?? []) {
            if (!s.parsedJson) continue;
            try {
                const pc = JSON.parse(s.parsedJson) as ParsedContent;
                if (pc.dedupOfHash) continue;
                const h = pc.metadata?.contentHash;
                if (h) map.set(h, pc);
            } catch {
                continue;
            }
        }
        return map;
    }, [data.siblingSnapshots]);

    const { parsed, dedupResolved } = useMemo<{
        parsed: ParsedContent | null;
        dedupResolved: boolean;
    }>(() => {
        const raw = data.snapshot?.parsedJson;
        if (!raw) return { parsed: null, dedupResolved: false };
        let direct: ParsedContent;
        try {
            direct = JSON.parse(raw) as ParsedContent;
        } catch {
            return { parsed: null, dedupResolved: false };
        }
        if (!direct.dedupOfHash) return { parsed: direct, dedupResolved: false };

        const resolved = siblingParsedByHash.get(direct.dedupOfHash);
        if (resolved) return { parsed: resolved, dedupResolved: true };
        return { parsed: direct, dedupResolved: false };
    }, [data.snapshot?.parsedJson, siblingParsedByHash]);

    const axText = data.snapshot?.axText ?? '';
    // True when the snapshot is a dedup stub but no sibling held the
    // matching full payload (e.g. session crossed a process boundary).
    const dedupUnresolved = !!parsed?.dedupOfHash && !dedupResolved;
    // True when ParsedContentSection would emit non-empty output. Mirrors
    // its internal branching so the wrapper section can decide between
    // "render parsed" vs "render parser-miss stub" without rendering twice.
    const hasParsedRender = !!parsed && !dedupUnresolved && (
        (parsed.contentType === 'conversation' && !!parsed.messages?.length) ||
        !!(parsed.title || parsed.url || parsed.body)
    );
    // The parsed section is shown for any snapshot-backed event so users can
    // tell apart "parser ran and got nothing" from "parser hit but the UI
    // hid it" — invisible misses are exactly the bug this surface is meant
    // to debug. OCR-search-driven popups don't carry a snapshot and stay
    // unaffected.
    const hasDetail = !!data.snapshot || !!ocrText || !!axText;

    // Compute the actual rendered position of the image within its object-contain container
    const updateImgLayout = useCallback(() => {
        const img = imgRef.current;
        const container = containerRef.current;
        if (!img || !container || !img.naturalWidth || !img.naturalHeight) return;

        const containerRect = container.getBoundingClientRect();
        const cw = containerRect.width;
        const ch = containerRect.height;
        const nw = img.naturalWidth;
        const nh = img.naturalHeight;

        // object-contain: scale to fit within container preserving aspect ratio
        const scale = Math.min(cw / nw, ch / nh);
        const renderW = nw * scale;
        const renderH = nh * scale;
        const offsetX = (cw - renderW) / 2;
        const offsetY = (ch - renderH) / 2;

        setImgLayout({ left: offsetX, top: offsetY, width: renderW, height: renderH });
    }, []);

    useEffect(() => {
        if (loaded) updateImgLayout();
    }, [loaded, updateImgLayout]);

    // Update layout on resize
    useEffect(() => {
        if (!loaded) return;
        window.addEventListener('resize', updateImgLayout);
        return () => window.removeEventListener('resize', updateImgLayout);
    }, [loaded, updateImgLayout]);

    // When the user navigates to a sibling event, the src swaps but we keep
    // `loaded` true on purpose so the browser shows the previous decoded
    // bitmap until the new one paints — resetting `loaded` would flash the
    // "Loading frame…" placeholder on every prev/next click. We still need
    // to drop transient state that no longer applies to the new frame:
    // clear the prior image's measured layout (OCR overlay positions
    // depend on it and would otherwise sit on stale coordinates briefly),
    // reset zoom/pan so navigation doesn't carry a half-zoomed view onto
    // the next image, and clear `error` so a previously failed frame
    // doesn't keep the error label up.
    useEffect(() => {
        setImgLayout(null);
        setError(false);
        setScale(1);
        setTranslate({ x: 0, y: 0 });
    }, [data.frameSrc]);

    // Close on Escape; arrow keys navigate between sibling events when
    // the parent supplied onPrev/onNext. The capture-phase listener
    // matches the existing Escape handling so nested editors inside the
    // popup don't swallow the keypress.
    useEffect(() => {
        const handleKey = (e: KeyboardEvent) => {
            if (e.key === 'Escape') {
                e.preventDefault();
                e.stopPropagation();
                (document.activeElement as HTMLElement)?.blur();
                onClose();
                return;
            }
            if (e.key === 'ArrowLeft' && onPrev) {
                e.preventDefault();
                e.stopPropagation();
                onPrev();
                return;
            }
            if (e.key === 'ArrowRight' && onNext) {
                e.preventDefault();
                e.stopPropagation();
                onNext();
            }
        };
        window.addEventListener('keydown', handleKey, true);
        return () => window.removeEventListener('keydown', handleKey, true);
    }, [onClose, onPrev, onNext]);

    // Pinch-to-zoom (trackpad pinch fires as wheel+ctrlKey in browsers/WebView)
    const handleWheel = useCallback((e: React.WheelEvent) => {
        if (!e.ctrlKey) return;
        e.preventDefault();
        e.stopPropagation();
        setScale((prev) => {
            const next = prev * (1 - e.deltaY * 0.01);
            return Math.min(Math.max(next, 0.5), 10);
        });
    }, []);

    // Drag to pan when zoomed
    const handlePointerDown = useCallback((e: React.PointerEvent) => {
        if (scale <= 1) return;
        isPanning.current = true;
        panStart.current = { x: e.clientX, y: e.clientY };
        translateStart.current = { ...translate };
        (e.target as HTMLElement).setPointerCapture(e.pointerId);
    }, [scale, translate]);

    const handlePointerMove = useCallback((e: React.PointerEvent) => {
        if (!isPanning.current) return;
        setTranslate({
            x: translateStart.current.x + (e.clientX - panStart.current.x),
            y: translateStart.current.y + (e.clientY - panStart.current.y),
        });
    }, []);

    const handlePointerUp = useCallback(() => {
        isPanning.current = false;
    }, []);

    // Double-click to reset zoom
    const handleDoubleClick = useCallback(() => {
        setScale(1);
        setTranslate({ x: 0, y: 0 });
    }, []);

    return (
        <div
            className="fixed inset-0 z-50 flex items-center justify-center bg-code-bg/60"
            onClick={onClose}
        >
            <div
                className="relative w-[90%] max-w-[900px] rounded-xl overflow-hidden bg-panel shadow-xl border border-border"
                onClick={(e) => e.stopPropagation()}
            >
                {/* Header */}
                <div className="flex items-center justify-between px-4 py-2.5 border-b border-border">
                    <div className="flex items-center gap-2 text-xs text-muted min-w-0">
                        <EventKindIcon kind={data.evt.kind} />
                        <span className="font-medium text-ink truncate">
                            <HighlightText text={data.evt.appName || (t('popup.unknownApp') as string)} keyword={keyword} />
                        </span>
                        <span className="truncate">
                            <HighlightText text={data.evt.windowTitle} keyword={keyword} />
                        </span>
                        <span className="shrink-0 tabular-nums">{formatTime(data.evt.timestamp)}</span>
                        {/*
                         * Snapshot temporal-distance hint. Screen capture
                         * occasionally fails for several seconds at a
                         * time (GDI can't read DComp / Direct3D surfaces
                         * on hardware-accelerated apps), so an event the
                         * user clicked may map to the nearest *available*
                         * snapshot rather than one captured at the exact
                         * moment. Surface the gap so the visible frame
                         * isn't quietly misattributed to a different
                         * moment's content.
                         */}
                        {data.snapshot && (() => {
                            const deltaMs = data.snapshot.timestamp - data.evt.timestamp;
                            const absSec = Math.round(Math.abs(deltaMs) / 1000);
                            if (absSec < 5) return null;
                            const label = deltaMs < 0
                                ? t('popup.snapshotBeforeEvent', { seconds: absSec })
                                : t('popup.snapshotAfterEvent', { seconds: absSec });
                            const tone = absSec > 30
                                ? 'bg-accent-light text-accent border-accent/30'
                                : 'bg-ink/[0.06] text-muted border-border';
                            return (
                                <span
                                    title={t('popup.snapshotDistanceHint') as string}
                                    className={`shrink-0 px-1.5 py-0.5 rounded border text-[10px] font-medium normal-case tabular-nums ${tone}`}
                                >
                                    {label as string}
                                </span>
                            );
                        })()}
                    </div>
                    <button
                        onClick={onClose}
                        className="p-1 rounded-md text-muted hover:text-ink hover:bg-border/50 transition-colors shrink-0 ml-2"
                    >
                        <X className="h-3.5 w-3.5" />
                    </button>
                </div>
                {/* Frame image with highlight overlays — supports pinch-to-zoom & drag */}
                <div
                    ref={containerRef}
                    className="relative bg-code-bg min-h-[200px] flex items-center justify-center overflow-hidden"
                    onWheel={handleWheel}
                    onPointerDown={handlePointerDown}
                    onPointerMove={handlePointerMove}
                    onPointerUp={handlePointerUp}
                    onPointerCancel={handlePointerUp}
                    onDoubleClick={handleDoubleClick}
                    style={{ cursor: scale > 1 ? (isPanning.current ? 'grabbing' : 'grab') : 'default', touchAction: 'none' }}
                >
                    {!loaded && !error && (
                        <span className="text-xs text-code-fg/50">{t('popup.loadingFrame')}</span>
                    )}
                    {error && (
                        <span className="text-xs text-code-fg/50">{t('popup.failedFrame')}</span>
                    )}
                    <div style={{ transform: `translate(${translate.x}px, ${translate.y}px) scale(${scale})`, transformOrigin: 'center center', transition: isPanning.current ? 'none' : 'transform 0.1s ease-out' }}>
                        <img
                            ref={imgRef}
                            src={data.frameSrc}
                            alt={t('popup.frameAlt') as string}
                            className={`w-full ${hasDetail ? 'max-h-[50vh]' : 'max-h-[75vh]'} object-contain ${loaded ? '' : 'hidden'}`}
                            onLoad={() => {
                                setLoaded(true);
                                // After navigation the `loaded` flag is
                                // already true, so the layout effect that's
                                // keyed on `loaded` won't refire — call the
                                // layout pass directly so OCR overlay and
                                // focus-rect math use the new image's
                                // natural dimensions instead of the prior
                                // frame's.
                                updateImgLayout();
                            }}
                            onError={() => setError(true)}
                            draggable={false}
                        />
                        {/* OCR highlight overlays */}
                        {loaded && imgLayout && highlightBoxes.map((box, i) => (
                            <div
                                key={i}
                                className="absolute border-2 border-accent bg-accent-light rounded-sm pointer-events-none"
                                style={{
                                    left: imgLayout.left + box.x * imgLayout.width,
                                    top: imgLayout.top + box.y * imgLayout.height,
                                    width: box.width * imgLayout.width,
                                    height: box.height * imgLayout.height,
                                }}
                            />
                        ))}
                        {/* Focus-window rect: drawn whenever the capture
                            pipeline persisted bounds for this snapshot.
                            Same imgLayout math as the OCR highlight boxes
                            so the dashed border lines up with the image
                            content even under object-contain letterboxing.
                            The legend reads "Focused window" (a capture-
                            time fact) so we don't lie about OCR scope
                            here. The per-row ocrUsedFocusRect flag is
                            still persisted for stricter downstream audit
                            but is intentionally NOT gated against — that
                            gate stripped the box from every OCR-sampler-
                            skipped frame, which is ~half the snapshots in
                            a typical session and read as "feature broken"
                            when capture had worked correctly. */}
                        {loaded && imgLayout && data.snapshot && (() => {
                            const fr = data.snapshot as unknown as {
                                focusRectX?: number;
                                focusRectY?: number;
                                focusRectW?: number;
                                focusRectH?: number;
                            };
                            const fw = fr.focusRectW ?? 0;
                            const fh = fr.focusRectH ?? 0;
                            // Per-window captures fill the frame (rect ≈ 1×1), so the box
                            // would just outline the whole image. Only draw it for full-screen
                            // fallback frames whose rect marks a real sub-region.
                            if (fw <= 0 || fh <= 0 || (fw >= 0.98 && fh >= 0.98)) return null;
                            return (
                                <div
                                    className="absolute border border-dashed rounded-sm pointer-events-none"
                                    style={{
                                        left: imgLayout.left + (fr.focusRectX ?? 0) * imgLayout.width,
                                        top: imgLayout.top + (fr.focusRectY ?? 0) * imgLayout.height,
                                        width: fw * imgLayout.width,
                                        height: fh * imgLayout.height,
                                        borderColor: 'var(--color-accent)',
                                    }}
                                />
                            );
                        })()}
                    </div>
                    {/* Legend pill so first-time viewers know what the dashed
                        orange box means. Placed outside the zoom/pan transform
                        wrapper so it stays pinned to the viewport corner and
                        legible at any zoom level. */}
                    {loaded && data.snapshot && (() => {
                        const fr = data.snapshot as unknown as {
                            focusRectW?: number;
                            focusRectH?: number;
                        };
                        const w = fr.focusRectW ?? 0;
                        const h = fr.focusRectH ?? 0;
                        // Hide the legend when the rect fills the frame (per-window capture).
                        return w > 0 && h > 0 && (w < 0.98 || h < 0.98);
                    })() && (
                        <div
                            className="absolute bottom-2 right-2 flex items-center gap-1.5 px-2 py-0.5 rounded-md bg-code-bg/60 text-code-fg/90 text-[11px] font-medium pointer-events-none"
                            title={t('session.focusRectHint') as string}
                        >
                            <span
                                aria-hidden="true"
                                className="inline-block w-3 h-2 border border-dashed"
                                style={{ borderColor: 'var(--color-accent)' }}
                            />
                            {t('session.focusRectLegend')}
                        </div>
                    )}
                    {/* Prev/next event arrows. Styling mirrors the snapshot
                        playback chevrons in the session timeline so the two
                        navigation surfaces feel like the same control. The
                        buttons sit outside the zoom/pan transform so they
                        stay pinned to the container edges at any scale, and
                        stopPropagation on pointerDown prevents a click on a
                        zoomed-in frame from also starting a pan. Buttons
                        stay visible-but-disabled at the ends of the list
                        rather than disappearing — the affordance is more
                        predictable for users navigating with the mouse. */}
                    {(onPrev || onNext) && (
                        <>
                            <button
                                type="button"
                                onClick={(e) => {
                                    e.stopPropagation();
                                    onPrev?.();
                                }}
                                onPointerDown={(e) => e.stopPropagation()}
                                disabled={!onPrev}
                                aria-label={t('popup.prevEvent') as string}
                                title={t('popup.prevEvent') as string}
                                className="absolute left-2 top-1/2 -translate-y-1/2 p-1.5 rounded-md bg-code-bg/60 text-code-fg/80 hover:text-code-fg hover:bg-code-bg/80 disabled:opacity-30 disabled:hover:bg-code-bg/60 disabled:hover:text-code-fg/80 transition-opacity"
                            >
                                <ChevronLeft className="h-5 w-5" />
                            </button>
                            <button
                                type="button"
                                onClick={(e) => {
                                    e.stopPropagation();
                                    onNext?.();
                                }}
                                onPointerDown={(e) => e.stopPropagation()}
                                disabled={!onNext}
                                aria-label={t('popup.nextEvent') as string}
                                title={t('popup.nextEvent') as string}
                                className="absolute right-2 top-1/2 -translate-y-1/2 p-1.5 rounded-md bg-code-bg/60 text-code-fg/80 hover:text-code-fg hover:bg-code-bg/80 disabled:opacity-30 disabled:hover:bg-code-bg/60 disabled:hover:text-code-fg/80 transition-opacity"
                            >
                                <ChevronRight className="h-5 w-5" />
                            </button>
                        </>
                    )}
                </div>
                {/* Per-frame extracted content — surfaces what the capture
                    pipeline actually pulled out of this screenshot so the
                    user can debug "did Slack messages get parsed?" type
                    questions. Three layers, top to bottom: app-parser
                    structured output, OCR text, raw AX tree text. */}
                {hasDetail && (
                    <div className="border-t border-border max-h-[35vh] overflow-y-auto">
                        {/*
                         * Parsed-content section: rendered only when there's
                         * something to show, OR on platforms where the "no
                         * parser matched" message is informative.
                         *
                         * The app-specific parsers (Slack / Feishu / Asana /
                         * code editors / Office / browser chrome stripper)
                         * all read macOS Accessibility trees — they never
                         * fire on Windows or Linux. Surfacing "no parser
                         * matched" on every Windows snapshot would be
                         * misleading (suggests something failed when in
                         * reality the feature isn't available), so the
                         * section is hidden when (a) we're on Windows AND
                         * (b) there's no parsed content to display.
                         */}
                        {data.snapshot && (
                            (hasParsedRender && parsed) ||
                            dedupUnresolved ||
                            !isWindows
                        ) && (
                            <CollapsibleParsedSection
                                snapshot={data.snapshot}
                                evt={data.evt}
                                parsed={parsed}
                                hasParsedRender={hasParsedRender}
                                dedupResolved={dedupResolved}
                                dedupUnresolved={dedupUnresolved}
                                keyword={keyword}
                                identityDebugOn={identityDebugOn}
                                highlightedSenderName={highlightedSenderName}
                            />
                        )}
                        {/*
                         * Native-accessibility text (macOS AX tree / Windows
                         * UIA tree). When the platform walker produced
                         * something, surface it FIRST and default-open: it is
                         * the higher-fidelity signal compared to OCR. The
                         * label tracks the platform — Windows users see
                         * "UI Automation text", everyone else sees
                         * "Accessibility text".
                         *
                         * Exception: when the parser flagged this snapshot as
                         * AX-metadata-only (Monaco editors etc.), the AX dump
                         * is chrome / menus / status bar and OCR is the only
                         * source of body text — default-close the AX section
                         * so the user lands on OCR instead.
                         *
                         * When axText is empty we render nothing for this
                         * section (no placeholder) — the user asked for the
                         * OCR-only fallback in that case.
                         */}
                        {axText && !isWindows && (
                            <CollapsibleTextSection
                                title={t('popup.axText')}
                                text={axText}
                                keyword={keyword}
                                defaultOpen={false}
                            />
                        )}
                        {ocrFailed && (
                            <ActivityInlineNotice notice={{ tone: 'error', text: t('popup.ocrFailed'), onAction: () => setOcrAttempt((n) => n + 1) }} />
                        )}
                        {ocrLoaded && !ocrFailed && (
                            <CollapsibleTextSection
                                title={t('popup.ocrText')}
                                text={ocrText}
                                emptyLabel={t('popup.ocrEmpty') as string}
                                keyword={keyword}
                                defaultOpen={false}
                            />
                        )}
                    </div>
                )}
            </div>
        </div>
    );
}

// ── Time filter presets ───────────────────────────────────────────────
type TimePreset = 'all' | 'today' | 'yesterday' | 'last7' | 'last30' | 'custom';

const TIME_PRESET_KEYS: TimePreset[] = ['all', 'today', 'yesterday', 'last7', 'last30', 'custom'];

function computeTimeRange(preset: TimePreset): { startMs: number; endMs: number } {
    if (preset === 'all') return { startMs: 0, endMs: 0 };
    const now = new Date();
    const todayStart = new Date(now);
    todayStart.setHours(0, 0, 0, 0);

    switch (preset) {
        case 'today':
            return { startMs: todayStart.getTime(), endMs: 0 };
        case 'yesterday': {
            const ydStart = new Date(todayStart);
            ydStart.setDate(ydStart.getDate() - 1);
            return { startMs: ydStart.getTime(), endMs: todayStart.getTime() };
        }
        case 'last7': {
            const d = new Date(todayStart);
            d.setDate(d.getDate() - 7);
            return { startMs: d.getTime(), endMs: 0 };
        }
        case 'last30': {
            const d = new Date(todayStart);
            d.setDate(d.getDate() - 30);
            return { startMs: d.getTime(), endMs: 0 };
        }
        default:
            return { startMs: 0, endMs: 0 };
    }
}

/** Highlight matching keyword in text */
function HighlightText({ text, keyword }: { text: string; keyword: string }) {
    if (!keyword || !text) return <>{text}</>;
    const escaped = keyword.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
    const parts = text.split(new RegExp(`(${escaped})`, 'gi'));
    if (parts.length === 1) return <>{text}</>;
    return (
        <>
            {parts.map((part, i) =>
                part.toLowerCase() === keyword.toLowerCase() ? (
                    <mark key={i} className="bg-surface-active text-inherit rounded-sm px-0.5">
                        {part}
                    </mark>
                ) : (
                    <span key={i}>{part}</span>
                ),
            )}
        </>
    );
}

/** Check if an event contains the keyword in any text field */
function eventMatchesKeyword(evt: activitymon.ActivityEvent, keyword: string): boolean {
    if (!keyword) return false;
    const kw = keyword.toLowerCase();
    return (
        (evt.appName?.toLowerCase().includes(kw) ?? false) ||
        (evt.windowTitle?.toLowerCase().includes(kw) ?? false) ||
        (evt.clickLabel?.toLowerCase().includes(kw) ?? false) ||
        (evt.clickRole?.toLowerCase().includes(kw) ?? false) ||
        ((evt.keyChars?.join('') ?? evt.keyChar ?? '').toLowerCase().includes(kw)) ||
        (evt.typedText?.toLowerCase().includes(kw) ?? false) ||
        (evt.focusLabel?.toLowerCase().includes(kw) ?? false) ||
        (evt.focusRole?.toLowerCase().includes(kw) ?? false)
    );
}

/** Known special key names produced by the native monitor */
const SPECIAL_KEYS = new Set([
    'Return', 'Tab', 'Space', 'Delete', 'Escape', 'Fwd Delete',
    'Left', 'Right', 'Up', 'Down',
    'Home', 'End', 'Page Up', 'Page Down',
]);

/** Modifier symbol prefixes used in combo strings like "⌘V", "⌃⌥Delete" */
const MODIFIER_SYMBOLS = ['⌃', '⌥', '⇧', '⌘'];

/** Check if a keyChar represents a special/function key or modifier combo rather than typed text */
function isSpecialKey(keyChar: string): boolean {
    if (!keyChar) return false;
    // Strip leading modifier symbols
    let rest = keyChar;
    for (const mod of MODIFIER_SYMBOLS) {
        while (rest.startsWith(mod)) rest = rest.slice(mod.length);
    }
    // Check if remainder is a special key name, single uppercase letter (combo), or Fn key
    if (SPECIAL_KEYS.has(rest)) return true;
    if (/^F\d+$/.test(rest)) return true;
    // Modifier + single char like "⌘V" — has modifier prefix and short remainder
    if (rest !== keyChar && rest.length <= 2) return true;
    return false;
}

/** Parse a keyChar into modifier symbols and the key name */
function parseKeyCombo(keyChar: string): { modifiers: string[]; key: string } {
    const modifiers: string[] = [];
    let rest = keyChar;
    for (const mod of MODIFIER_SYMBOLS) {
        while (rest.startsWith(mod)) {
            modifiers.push(mod);
            rest = rest.slice(mod.length);
        }
    }
    return { modifiers, key: rest || keyChar };
}

/** Render a single key token as a kbd badge or plain text */
function KbdToken({ token, keyword }: { token: string; keyword: string }) {
    if (isSpecialKey(token)) {
        const { modifiers, key } = parseKeyCombo(token);
        return (
            <span className="inline-flex items-center gap-0.5">
                {modifiers.map((mod, idx) => (
                    <kbd
                        key={idx}
                        className="inline-flex items-center justify-center min-w-[18px] h-[18px] px-1 rounded bg-panel-warm border border-border text-[9px] font-mono text-muted shadow-[var(--shadow-soft)]"
                    >
                        {mod}
                    </kbd>
                ))}
                <kbd className="inline-flex items-center justify-center min-w-[18px] h-[18px] px-1.5 rounded bg-panel-warm border border-border text-[9px] font-mono text-muted shadow-[var(--shadow-soft)]">
                    <HighlightText text={key} keyword={keyword} />
                </kbd>
            </span>
        );
    }
    return null;
}

/** Render a keyChars array (each element is one keystroke) with proper kbd badges */
function KeyCharDisplay({ keyChars, keyword }: { keyChars: string[]; keyword: string }) {
    // Group consecutive plain chars into runs, special keys stay individual
    const groups: { type: 'text' | 'special'; value: string }[] = [];
    for (const kc of keyChars) {
        if (!isSpecialKey(kc)) {
            const last = groups[groups.length - 1];
            if (last?.type === 'text') {
                last.value += kc;
            } else {
                groups.push({ type: 'text', value: kc });
            }
        } else {
            groups.push({ type: 'special', value: kc });
        }
    }

    return (
        <span className="inline-flex items-center gap-0.5 flex-wrap max-w-[280px]">
            {groups.map((g, i) =>
                g.type === 'text' ? (
                    <span key={i} className="bg-panel-warm px-1.5 py-0.5 rounded text-[10px] font-mono max-w-[160px] truncate">
                        <HighlightText text={g.value} keyword={keyword} />
                    </span>
                ) : (
                    <KbdToken key={i} token={g.value} keyword={keyword} />
                ),
            )}
        </span>
    );
}

const AX_ROLE_TOKEN = /\bAX[A-Z][A-Za-z]*\b/;
const ROLE_WORDS = new Set(['group', 'button', 'statictext', 'text', 'textfield', 'textarea', 'image', 'cell', 'row', 'link',
    'window', 'menuitem', 'checkbox', 'radiobutton', 'popupbutton', 'scrollarea', 'webarea', 'toolbar', 'list', 'outline',
    'table', 'tab', 'tabgroup', 'splitter', 'unknown']);

/** No name a person can read: empty, or only an accessibility role such as
 *  "AXGroup", "Button" or "StaticText" (live walk AR57), as in Go's
 *  timelineReadableLabel. */
export function isRoleName(value?: string): boolean {
    const v = (value ?? '').trim();
    return !v || AX_ROLE_TOKEN.test(v) || ROLE_WORDS.has(v.replace(/\s+/g, '').toLowerCase());
}

/** The events that are steps: a click or focus needs a readable name, the
 *  focus a click causes folds into that click, and speech scraps drop
 *  (activity review v1.0.5 V6, live walk AR57). */
export function stepEventIndexes(events: activitymon.ActivityEvent[]): number[] {
    return events.flatMap((evt, idx) => {
        if (evt.kind === 'speech' && isSpeechScrap(evt.speechText)) return [];
        if (evt.kind === 'click' && isRoleName(evt.clickLabel)) return [];
        if (evt.kind !== 'focus') return [idx];
        if (isRoleName(evt.focusLabel)) return [];
        const label = evt.focusLabel!.trim();
        const clickTwin = [events[idx - 1], events[idx + 1]].some((o) => o?.kind === 'click' && o.clickLabel?.trim() === label
            && o.appName === evt.appName && Math.abs(o.timestamp - evt.timestamp) <= 5_000);
        return clickTwin ? [] : [idx];
    });
}

function stepObject(label: string | undefined, app: string | undefined, t: TFunction<'activity'>): string {
    const object = isRoleName(label) ? '' : label!.trim();
    return object && app ? t('stepVerbs.inApp', { object, app }) : object;
}

export function eventActionLabel(evt: activitymon.ActivityEvent, t: TFunction<'activity'>): { verb: string; object: string } {
    switch (evt.kind) {
        case 'click':
            return {
                verb: t('stepVerbs.clicked'),
                object: stepObject(evt.clickLabel, evt.appName, t),
            };
        case 'keyboard':
            // typedText is the committed text recovered from focused-field
            // diffs (IME-composed); raw keyChars render separately.
            return { verb: t('stepVerbs.typed'), object: evt.typedText ?? '' };
        case 'focus':
            return {
                verb: t('stepVerbs.focused'),
                object: stepObject(evt.focusLabel, evt.appName, t),
            };
        case 'speech':
            return {
                verb: evt.speechSource === 'microphone' ? t('stepVerbs.said') : t('stepVerbs.heard'),
                object: evt.speechText || '',
            };
        case 'app_switch':
            return { verb: t('stepVerbs.switchedApp'), object: evt.appName || '' };
        // Events Isoform itself produced are rendered from their fields, so
        // the line follows the UI language.
        case 'calendar_peek': {
            const providerKey = evt.provider === 'google' || evt.provider === 'outlook' ? evt.provider : 'other';
            return {
                verb: t('systemEvents.verb'),
                object: t('systemEvents.calendarPeek', {
                    count: evt.count ?? 0,
                    provider: t(`systemEvents.providers.${providerKey}` as never),
                }),
            };
        }
        case 'device_in_use': {
            const device = evt.device === 'camera' ? 'camera' : 'microphone';
            return {
                verb: t('systemEvents.verb'),
                object: t(`systemEvents.device.${device}_${evt.inUse ? 'on' : 'off'}` as never),
            };
        }
        case 'clipboard': {
            // The scrubbed preview when macOS let Isoform read the copy,
            // otherwise only what kind of thing it was.
            const known = ['text', 'url', 'file', 'image'];
            const kind = evt.clipboardKind && known.includes(evt.clipboardKind) ? evt.clipboardKind : 'other';
            return {
                verb: t('systemEvents.copied'),
                object: evt.clipboardPreview || t(`systemEvents.clipboard.${kind}` as never),
            };
        }
        case 'session_state': {
            const known = ['locked', 'unlocked', 'sleep', 'wake', 'idle', 'active', 'battery', 'power'];
            const state = evt.stateName && known.includes(evt.stateName) ? evt.stateName : 'other';
            return { verb: t('systemEvents.verb'), object: t(`systemEvents.state.${state}` as never) };
        }
        default:
            return { verb: t('stepVerbs.recorded'), object: evt.windowTitle || evt.appName || '' };
    }
}

/** Simple debounce hook */
function useDebounce<T>(value: T, delayMs: number): T {
    const [debounced, setDebounced] = useState(value);
    useEffect(() => {
        const id = setTimeout(() => setDebounced(value), delayMs);
        return () => clearTimeout(id);
    }, [value, delayMs]);
    return debounced;
}

/** Dropdown wrapper with click-outside-to-close (for multi-select app filter) */
function AppFilterDropdown({
    trigger,
    open,
    onOpenChange,
    children,
}: {
    trigger: React.ReactNode;
    open: boolean;
    onOpenChange: (open: boolean) => void;
    children: React.ReactNode;
}) {
    const ref = useRef<HTMLDivElement>(null);

    useEffect(() => {
        if (!open) return;
        const handleClick = (e: MouseEvent) => {
            if (ref.current && !ref.current.contains(e.target as Node)) {
                onOpenChange(false);
            }
        };
        document.addEventListener('mousedown', handleClick);
        return () => document.removeEventListener('mousedown', handleClick);
    }, [open, onOpenChange]);

    return (
        <div ref={ref} className="relative">
            <button
                onClick={() => onOpenChange(!open)}
                className="flex items-center gap-1 px-2 py-1 text-[11px] rounded-md bg-panel-warm text-muted hover:text-ink hover:bg-surface-hover transition-colors truncate"
            >
                {trigger}
                <ChevronDown className="h-3 w-3 shrink-0" />
            </button>
            {open && (
                <div className="absolute top-full left-0 mt-1 z-50 min-w-[160px] rounded-lg border border-border bg-panel shadow-lg py-1">
                    {children}
                </div>
            )}
        </div>
    );
}

/** Extract hostname from a URL for compact display in the audit UI.
 *  Returns the original string when parsing fails so we never lose
 *  signal — operators would rather see "weird:string" than nothing. */
function hostnameOf(url: string): string {
    try {
        return new URL(url).hostname.replace(/^www\./, '');
    } catch {
        return url.length > 40 ? url.slice(0, 40) + '…' : url;
    }
}

/** SelectedFrame.skipReason → activity-namespace i18n key. Single source
 *  of truth for the mapping; the call site uses t() on the resolved key
 *  so unknown reasons surface as a translated "Dropped (reason not
 *  recorded)" rather than the raw enum string. See SelectedFrame.SkipReason
 *  in wails-gui/activity_pipeline_runs.go for the canonical list. */
const SKIP_REASON_I18N_KEY = {
    parsed_dedup_to_winner: 'session.skipReason.parsed_dedup_to_winner',
    parsed_oversize: 'session.skipReason.parsed_oversize',
    ocr_covered_by_parsed: 'session.skipReason.ocr_covered_by_parsed',
    ocr_prefix_dedup: 'session.skipReason.ocr_prefix_dedup',
    ocr_over_cap: 'session.skipReason.ocr_over_cap',
} as const satisfies Record<string, string>;
type KnownSkipReason = keyof typeof SKIP_REASON_I18N_KEY;
function isKnownSkipReason(reason: string): reason is KnownSkipReason {
    return reason in SKIP_REASON_I18N_KEY;
}

/** Header date label: "Today" / "Yesterday" / "Mon, Oct 20". */
function formatDayNavLabel(date: Date, todayLabel: string, yesterdayLabel: string): string {
    const today = new Date();
    today.setHours(0, 0, 0, 0);
    const d = new Date(date);
    d.setHours(0, 0, 0, 0);
    const diff = Math.round((today.getTime() - d.getTime()) / 86400000);
    if (diff === 0) return todayLabel;
    if (diff === 1) return yesterdayLabel;
    return d.toLocaleDateString([], { weekday: 'short', month: 'short', day: 'numeric' });
}

// ── Main activity page ───────────────────────────────────────────────
export function ActivityPage({ onClose, initialSessionId, initialDate, noticedFocus, onNoticedFocusConsumed, initialThreadId, onInitialThreadConsumed, initialConversationId, onOpenTopic, cronJobs, backButton, section, onSectionChange, onOpenChat, onStartChat, highlightedFrames, highlightedPipelineRunId, restrictToSessionIds, initialEventTimestamp, highlightedSenderName }: ActivityPageProps) {
    const { t } = useTranslation('activity');
    // One day at a time (computer-conformance-answers A4): previous and next beside the
    // date steps through days. The Screen timeline has its own time filter.
    // A session jump (memory / knowledge / people / chat link) lands
    // directly on the Screen timeline, as before.
    // A plain revisit (no deep link in any prop) restores the timeline
    // filters the last visit left; a deep link starts fresh.
    const freshOpen = Boolean(initialSessionId || initialDate || noticedFocus || initialThreadId || highlightedFrames?.length || highlightedPipelineRunId || restrictToSessionIds?.length || initialEventTimestamp || highlightedSenderName);
    // A suggestion link from Today opens Your day on its date (App sets
    // initialDate with it); there is nothing on the day to focus any more.
    useEffect(() => {
        if (!noticedFocus) return;
        if (section !== 'yourDay') onSectionChange('yourDay');
        onNoticedFocusConsumed?.(noticedFocus);
    }, [noticedFocus, section, onSectionChange, onNoticedFocusConsumed]);
    // Shorthand that keeps the old viewMode conditionals readable: the
    // Screen timeline section shows the snapshot view.
    const isTimeline = section === 'timeline';
    // The Day view's section; unreachable while the timeline shows.
    // 'watch' is From your watch: the recordings the watch sent. Its door
    // shows only once one has arrived, so a computer without a watch looks
    // as it did.
    const isWatch = section === 'watch';
    const isAgents = section === 'agents';
    const isAnswers = section === 'answers';
    const daySection: ActivityDaySection = isTimeline || isWatch || isAgents || isAnswers ? 'yourDay' : section;
    // The day Your day, Screen and Agents show. The header's previous and next buttons and date
    // picker drive it; useCalendarData keeps the month heatmap (day dots)
    // and the day's scheduled runs.
    const [selectedDate, setSelectedDate] = useState<Date>(() =>
        initialDate ? new Date(initialDate + 'T00:00:00') : lastActivityDate ?? new Date(),
    );
    const [datePopoverOpen, setDatePopoverOpen] = useState(false);
    useEffect(() => rememberActivityDate(selectedDate), [selectedDate]);
    const calendarData = useCalendarData(cronJobs, selectedDate, setSelectedDate, datePopoverOpen);
    // The scheduled run whose detail dialog is open (Day view "Open result").
    const [detailJobId, setDetailJobId] = useState<string | null>(null);
    const [sessionsFailed, setSessionsFailed] = useState(false);
    const refreshSessionsRef = useRef<() => void>(() => {});
    const [speechNotice, setSpeechNotice] = useState<{ key: string; notice: ActivityNotice } | null>(null);
    const [sessions, setSessions] = useState<main.ActivitySessionSummary[]>([]);
    const [timelineGroups, setTimelineGroups] = useState<ActivityTimelineGroup[]>([]);
    const [expandedSemanticTimelineItems, setExpandedSemanticTimelineItems] = useState<Set<string>>(() => new Set());
    const [selectedId, setSelectedId] = useState<string | null>(initialSessionId ?? null);
    const [selectedTimelineGroupId, setSelectedTimelineGroupId] = useState<string | null>(null);
    const [detail, setDetail] = useState<main.ActivitySessionDetail | null>(null);
    const [segmentIndex, setSegmentIndex] = useState(0);
    const [snapshotIndex, setSnapshotIndex] = useState(0);
    // People/knowledge jumps auto-land the operator on a specific moment (see
    // initialEventTimestamp). The detail loader re-applies that anchor on every
    // (re)load so a background timeline refresh can't yank the frame back to 0;
    // this ref flips true once the user deliberately scrubs, after which reloads
    // stop re-anchoring and leave manual navigation alone.
    const anchorReleasedRef = useRef(false);
    const [loading, setLoading] = useState(true);
    const [bridgePort, setBridgePort] = useState(0);
    const [isFullscreen, setIsFullscreen] = useState(false);
    const [eventPopup, setEventPopup] = useState<EventPopupData | null>(null);
    const [isPlaying, setIsPlaying] = useState(false);
    const [playbackFps, setPlaybackFps] = useState(4);

    // ── Identity debug overlay ──
    // Toggle persisted to localStorage so the developer can leave it on
    // across sessions. When on, ClassifyActivityEvents runs after each
    // detail load and the verdicts decorate every timeline row (plus chat
    // sender resolution lights up inside the popup).
    const [identityDebugOn, setIdentityDebugOn] = useState<boolean>(() => {
        try {
            return localStorage.getItem('yansu.activity.identity_debug') === 'true';
        } catch {
            return false;
        }
    });
    const [identityVerdicts, setIdentityVerdicts] = useState<EventIdentityVerdictDTO[] | null>(null);

    // Principals Isoform identified in the currently selected session. Used
    // to render the "People in this session" chip row above Recording and
    // — when the user clicks a chip — to set a LOCAL sender highlight
    // that overrides the (optional) one passed in via the prop. Cleared
    // when selectedId changes so the local pick doesn't leak across
    // sessions.
    const [sessionPrincipals, setSessionPrincipals] = useState<SessionPrincipalDTO[]>([]);
    const [locallySelectedPersonName, setLocallySelectedPersonName] = useState<string | null>(null);
    const [speakerClusters, setSpeakerClusters] = useState<SessionSpeakerClusterDTO[]>([]);
    const [selectedSpeakerLabel, setSelectedSpeakerLabel] = useState<string | null>(null);
    const selectedSpeakerTimestamps = useMemo<Set<number> | null>(() => {
        if (!selectedSpeakerLabel) return null;
        const cluster = speakerClusters.find((c) => c.label === selectedSpeakerLabel);
        if (!cluster) return null;
        return new Set(cluster.eventTimestamps);
    }, [selectedSpeakerLabel, speakerClusters]);
    const effectiveHighlightedSenderName =
        locallySelectedPersonName ?? highlightedSenderName ?? null;

    // OCR-derived rectangles on the current snapshot for the highlighted
    // person's name. Empty when no person is selected, when the OCR
    // engine ran but found nothing, or while the fetch is in flight.
    // Re-fetched any time the highlighted name OR the current snapshot
    // changes — debounced via an in-flight cancel ref so rapid chip
    // swaps don't stamp older results over newer ones.
    const [personHighlightBoxes, setPersonHighlightBoxes] = useState<main.OCRHighlightBox[]>([]);

    // Fullscreen snapshot zoom + pan. All state lives in refs so the
    // handlers never trigger a React re-render — the wrapper div's
    // transform is mutated DIRECTLY via ref so trackpad pinch and drag
    // stay at 60+ fps regardless of how heavy the rest of the tree is
    // (img + multiple SVG overlays + buttons would otherwise re-render
    // on every pointermove / wheel tick). Reset on entering fullscreen
    // and on snapshot change so a stale zoom doesn't carry over.
    const fsTransformedRef = useRef<HTMLDivElement>(null);
    const fsScaleRef = useRef(1);
    const fsTranslateRef = useRef({ x: 0, y: 0 });
    const fsIsPanningRef = useRef(false);
    const fsPanStartRef = useRef({ x: 0, y: 0 });
    const fsTranslateStartRef = useRef({ x: 0, y: 0 });
    const fsCursorOwnerRef = useRef<HTMLElement | null>(null);

    const applyFsTransform = useCallback(() => {
        const node = fsTransformedRef.current;
        if (!node) return;
        const { x, y } = fsTranslateRef.current;
        const s = fsScaleRef.current;
        // translate3d + explicit scale promotes the element to its own
        // GPU compositor layer so the browser doesn't repaint the img +
        // SVG overlays on every frame — only the layer transform.
        node.style.transform = `translate3d(${x}px, ${y}px, 0) scale(${s})`;
    }, []);
    const updateCursorFor = useCallback((el: HTMLElement | null) => {
        const owner = fsCursorOwnerRef.current ?? el;
        if (!owner) return;
        fsCursorOwnerRef.current = owner;
        owner.style.cursor =
            fsIsPanningRef.current && fsScaleRef.current > 1
                ? 'grabbing'
                : fsScaleRef.current > 1
                  ? 'grab'
                  : 'default';
    }, []);

    useEffect(() => {
        fsScaleRef.current = 1;
        fsTranslateRef.current = { x: 0, y: 0 };
        fsIsPanningRef.current = false;
        // Wait one frame so the DialogContent + wrapper are mounted
        // before we touch their style.
        const raf = requestAnimationFrame(() => {
            applyFsTransform();
            updateCursorFor(null);
        });
        return () => cancelAnimationFrame(raf);
    }, [isFullscreen, snapshotIndex, applyFsTransform, updateCursorFor]);

    const handleFsWheel = useCallback((e: React.WheelEvent) => {
        e.preventDefault();
        e.stopPropagation();
        // Exponential factor — keeps the perceived zoom rate constant
        // across mouse-wheel notches (deltaY ~= ±100) AND trackpad
        // pinch ticks (deltaY ~= ±2-20). 0.0025 maps a typical pinch
        // tick to ~0.5-5% per event, which feels right on macOS.
        const factor = Math.exp(-e.deltaY * 0.0025);
        const next = Math.min(Math.max(fsScaleRef.current * factor, 0.5), 12);
        fsScaleRef.current = next;
        applyFsTransform();
        updateCursorFor(e.currentTarget as HTMLElement);
    }, [applyFsTransform, updateCursorFor]);
    const handleFsPointerDown = useCallback((e: React.PointerEvent) => {
        if (e.button !== 0) return; // primary button only
        if (fsScaleRef.current <= 1) return; // nothing to pan
        fsIsPanningRef.current = true;
        fsPanStartRef.current = { x: e.clientX, y: e.clientY };
        fsTranslateStartRef.current = { ...fsTranslateRef.current };
        (e.target as HTMLElement).setPointerCapture(e.pointerId);
        updateCursorFor(e.currentTarget as HTMLElement);
    }, [updateCursorFor]);
    const handleFsPointerMove = useCallback((e: React.PointerEvent) => {
        if (!fsIsPanningRef.current) return;
        fsTranslateRef.current = {
            x: fsTranslateStartRef.current.x + (e.clientX - fsPanStartRef.current.x),
            y: fsTranslateStartRef.current.y + (e.clientY - fsPanStartRef.current.y),
        };
        applyFsTransform();
    }, [applyFsTransform]);
    const handleFsPointerUp = useCallback((e: React.PointerEvent) => {
        fsIsPanningRef.current = false;
        updateCursorFor(e.currentTarget as HTMLElement);
    }, [updateCursorFor]);
    const handleFsDoubleClick = useCallback((e: React.MouseEvent) => {
        fsScaleRef.current = 1;
        fsTranslateRef.current = { x: 0, y: 0 };
        applyFsTransform();
        updateCursorFor(e.currentTarget as HTMLElement);
    }, [applyFsTransform, updateCursorFor]);

    useEffect(() => {
        if (!selectedId) {
            setSessionPrincipals([]);
            setLocallySelectedPersonName(null);
            return;
        }
        let cancelled = false;
        setLocallySelectedPersonName(null);
        ListSessionPrincipals(selectedId, 50)
            .then((rows) => {
                if (!cancelled) setSessionPrincipals(rows ?? []);
            })
            .catch(() => {
                if (!cancelled) setSessionPrincipals([]);
            });
        return () => {
            cancelled = true;
        };
    }, [selectedId]);

    // Voice diarization (R20): cluster every mic-source speech event's
    // cached embedding so the user can see "You + N other voices" in
    // this session even when none of the others are enrolled.
    useEffect(() => {
        if (!selectedId) {
            setSpeakerClusters([]);
            setSelectedSpeakerLabel(null);
            return;
        }
        let cancelled = false;
        setSelectedSpeakerLabel(null);
        ListSessionSpeakerClusters(selectedId)
            .then((rows) => {
                if (!cancelled) setSpeakerClusters(rows ?? []);
            })
            .catch(() => {
                if (!cancelled) setSpeakerClusters([]);
            });
        return () => {
            cancelled = true;
        };
    }, [selectedId]);
    const toggleIdentityDebug = useCallback(() => {
        setIdentityDebugOn((prev) => {
            const next = !prev;
            try {
                localStorage.setItem('yansu.activity.identity_debug', next ? 'true' : 'false');
            } catch {
                // localStorage may be disabled — toggle still applies for this session.
            }
            return next;
        });
    }, []);

    // Re-classify whenever the toggle is on and a new detail loads.
    // ActivitySessionDetail does not currently expose isMeeting to the
    // frontend, so the classifier is invoked with inMeeting=false; the
    // SpeechHints.meetingContext flag in the verdict still surfaces the
    // actual meeting state when one is detected.
    useEffect(() => {
        if (!identityDebugOn || !detail?.events?.length) {
            setIdentityVerdicts(null);
            return;
        }
        let cancelled = false;
        ClassifyActivityEvents(detail.events as unknown[], false)
            .then((rows) => {
                if (!cancelled) setIdentityVerdicts(rows ?? []);
            })
            .catch((err: unknown) => {
                if (!cancelled) {
                    console.warn('[activity] ClassifyActivityEvents failed:', err);
                    setIdentityVerdicts([]);
                }
            });
        return () => {
            cancelled = true;
        };
    }, [identityDebugOn, detail]);

    // ── Filter state ──
    const rememberedFilters = useMemo(() => (freshOpen ? undefined : lastTimelineFilters()), []); // eslint-disable-line react-hooks/exhaustive-deps
    const [searchText, setSearchText] = useState(rememberedFilters?.searchText ?? '');
    const debouncedKeyword = useDebounce(searchText, 300);
    const [timePreset, setTimePreset] = useState<TimePreset>((rememberedFilters?.timePreset as TimePreset | undefined) ?? 'today');
    const [customStart, setCustomStart] = useState(rememberedFilters?.customStart ?? '');
    const [customEnd, setCustomEnd] = useState(rememberedFilters?.customEnd ?? '');
    const [selectedApps, setSelectedApps] = useState<Set<string> | null>(() => rememberedFilters?.selectedApps ? new Set(rememberedFilters.selectedApps) : null); // null = all
    const [appDropdownOpen, setAppDropdownOpen] = useState(false);
    useEffect(() => {
        rememberTimelineFilters({ searchText, timePreset, customStart, customEnd, selectedApps: selectedApps ? [...selectedApps] : null });
    }, [searchText, timePreset, customStart, customEnd, selectedApps]);
    // When we arrive with an `initialSessionId` from memory / knowledge, the
    // sidebar should list sessions from the same day as that session — not
    // "today". Hold off the first sessions fetch until we've resolved the
    // anchor session's start time, otherwise the user lands on today's list
    // with the jumped-to session orphaned at the top.
    const [anchorReady, setAnchorReady] = useState<boolean>(!initialSessionId);
    // Ref attached to the currently-selected session button so we can scroll
    // it into view on initial anchor. Only fires once per mount — subsequent
    // user-driven selections keep the native click-scroll behavior.
    const selectedSessionRef = useRef<HTMLButtonElement | null>(null);
    const didAnchorScrollRef = useRef<boolean>(false);

    // Semantic search results: session IDs boosted by embedding similarity
    const [semanticMatches, setSemanticMatches] = useState<Map<string, { score: number; matchText: string }>>(new Map());

    // Compute effective time range
    const timeRange = useMemo(() => {
        if (timePreset === 'custom') {
            return {
                startMs: customStart ? new Date(customStart).getTime() : 0,
                endMs: customEnd ? new Date(customEnd + 'T23:59:59.999').getTime() : 0,
            };
        }
        return computeTimeRange(timePreset);
    }, [timePreset, customStart, customEnd]);

    // Open a capture session in the Screen timeline section. Used by the Day
    // view's learned-entry session links. Mirrors the initialSessionId anchor
    // flow: resolve the session's day first so the sidebar lists its siblings,
    // then select it. Falls back to the current range when the detail fetch
    // fails so the jump never dead-ends.
    const openSessionInTimeline = useCallback((sessionId: string) => {
        const select = () => {
            setSelectedTimelineGroupId(null);
            setSelectedId(sessionId);
            onSectionChange('timeline');
        };
        GetActivitySessionDetail(sessionId)
            .then((d) => {
                if (d && d.startTime) {
                    const day = formatDateStr(new Date(d.startTime));
                    setCustomStart(day);
                    setCustomEnd(day);
                    setTimePreset('custom');
                }
                select();
            })
            .catch(select);
    }, [onSectionChange]);

    // A watch conversation from a row of Your day opens in place: the Watch
    // tab, on the conversation's own day, with its page open.
    const [openConversationId, setOpenConversationId] = useState<string | null>(initialConversationId ?? null);
    const openConversation = useCallback((conversationId: string, startMs: number) => {
        const at = new Date(startMs);
        setSelectedDate(new Date(at.getFullYear(), at.getMonth(), at.getDate()));
        setOpenConversationId(conversationId);
        onSectionChange('watch');
    }, [onSectionChange]);
    // Leaving the Watch tab drops the arrival, so coming back shows the list.
    useEffect(() => {
        if (section !== 'watch') setOpenConversationId(null);
    }, [section]);

    // Fetch bridge port on mount, retrying if the bridge isn't ready yet.
    useEffect(() => {
        let cancelled = false;
        const fetchPort = (retries = 5) => {
            Promise.all([GetHostBridgePort(), GetHostBridgeMediaToken()])
                .then(([port, token]) => {
                    if (cancelled) return;
                    if (port > 0 && token) {
                        setHostBridgeMediaToken(token);
                        setBridgePort(port);
                    } else if (retries > 0) {
                        setTimeout(() => fetchPort(retries - 1), 500);
                    }
                })
                .catch(() => {
                    if (!cancelled && retries > 0) {
                        setTimeout(() => fetchPort(retries - 1), 500);
                    }
                });
        };
        fetchPort();
        return () => { cancelled = true; };
    }, []);

    // Resolve the anchor session (when opened via a session-jump from memory
    // or knowledge). Reads its start time and switches the time filter to
    // that session's day so the sidebar shows sibling sessions from the same
    // day, not "today". Runs once on mount.
    useEffect(() => {
        if (!initialSessionId) return;
        let cancelled = false;
        GetActivitySessionDetail(initialSessionId)
            .then((d) => {
                if (cancelled) return;
                if (d && d.startTime) {
                    const start = new Date(d.startTime);
                    const pad = (n: number) => String(n).padStart(2, '0');
                    const day = `${start.getFullYear()}-${pad(start.getMonth() + 1)}-${pad(start.getDate())}`;
                    setCustomStart(day);
                    setCustomEnd(day);
                    setTimePreset('custom');
                }
                setAnchorReady(true);
            })
            .catch(() => {
                if (!cancelled) setAnchorReady(true);
            });
        return () => {
            cancelled = true;
        };
        // eslint-disable-next-line react-hooks/exhaustive-deps
    }, []);

    // Load sessions when filters change — the Screen timeline's data. It is
    // the page's one heavy load, so it waits until the timeline section is
    // picked; the Day/Week sections never trigger it.
    useEffect(() => {
        if (!isTimeline) return;
        if (!anchorReady) return;
        setLoading(true);
        Promise.all([
            GetActivitySessions(200, 0, timeRange.startMs, timeRange.endMs, debouncedKeyword),
            GetActivityTimelineGroups(200, 0, timeRange.startMs, timeRange.endMs, debouncedKeyword),
        ])
            .then(async ([result, groupResult]) => {
                let list = result ?? [];
                let groups = groupResult ?? [];
                setSelectedApps(null);

                // If navigating to a specific session that isn't in the loaded list,
                // fetch its detail and inject a summary so the sidebar shows it.
                let initialSessionFound = initialSessionId
                    ? list.some((s) => s.id === initialSessionId)
                    : false;
                if (initialSessionId && !initialSessionFound) {
                    try {
                        const d = await GetActivitySessionDetail(initialSessionId);
                        if (d && d.id) {
                            // Derive a summary from the detail (ActivitySessionDetail embeds ActivitySessionSummary)
                            const summary = {
                                id: d.id,
                                startTime: d.startTime,
                                endTime: d.endTime,
                                triggerKind: d.triggerKind,
                                appNames: d.appNames ?? [],
                                eventCount: d.eventCount,
                                snapshotCount: d.snapshotCount,
                                triageStatus: d.triageStatus,
                                ocrStatus: d.ocrStatus,
                            } as main.ActivitySessionSummary;
                            list = [summary, ...list];
                            initialSessionFound = true;
                        }
                    } catch {
                        // Session no longer exists — will fall through to auto-select
                    }
                }

                // Session-anchored entry (Who you know page rows, memory/knowledge
                // jumps): the anchored session's timeline group can fall
                // outside the range-bound fetch (e.g. a run straddling
                // midnight). Fetch it by session id and merge so the timeline
                // selects the right run instead of an unrelated newest one.
                if (initialSessionId && !groups.some((g) => (g.sessionIds ?? []).includes(initialSessionId))) {
                    try {
                        const anchored = await GetActivityTimelineGroupsForSessions([initialSessionId]);
                        groups = mergeTimelineGroups(groups, anchored ?? []);
                    } catch {
                        // Session has no persisted run (purged) — session-list
                        // fallback below still shows it.
                    }
                }

                // Pipeline-jump restriction: keep ONLY the merged group's
                // sessions and fetch any that fell outside the current date
                // filter. Without this the operator sees adjacent unrelated
                // sessions and has to mentally subtract them from the group.
                if (restrictToSessionIds && restrictToSessionIds.length > 0) {
                    const allowed = new Set(restrictToSessionIds);
                    list = list.filter((s) => allowed.has(s.id));
                    groups = groups.filter((g) => (g.sessionIds ?? []).some((id) => allowed.has(id)));
                    const coveredByGroups = new Set(groups.flatMap((g) => g.sessionIds ?? []));
                    const missingGroupSessions = restrictToSessionIds.filter((id) => !coveredByGroups.has(id));
                    if (missingGroupSessions.length > 0) {
                        try {
                            const extra = await GetActivityTimelineGroupsForSessions(missingGroupSessions);
                            groups = mergeTimelineGroups(groups, extra ?? []);
                        } catch {
                            // Sessions without persisted runs fall back to the
                            // session list injection below.
                        }
                    }
                    const have = new Set(list.map((s) => s.id));
                    const missing = restrictToSessionIds.filter((id) => !have.has(id));
                    if (missing.length > 0) {
                        const details = await Promise.all(
                            missing.map(async (id) => {
                                try {
                                    const d = await GetActivitySessionDetail(id);
                                    return d && d.id ? d : null;
                                } catch {
                                    return null;
                                }
                            }),
                        );
                        for (const d of details) {
                            if (!d || !d.id) continue;
                            list.push({
                                id: d.id,
                                startTime: d.startTime,
                                endTime: d.endTime,
                                triggerKind: d.triggerKind,
                                appNames: d.appNames ?? [],
                                eventCount: d.eventCount,
                                snapshotCount: d.snapshotCount,
                                triageStatus: d.triageStatus,
                                ocrStatus: d.ocrStatus,
                            } as main.ActivitySessionSummary);
                        }
                    }
                    // GetActivitySessions returns newest-first; reapply that
                    // ordering after the merge so the date headers come out
                    // sensible regardless of where individual rows came from.
                    list.sort((a, b) => b.startTime - a.startTime);
                    initialSessionFound = initialSessionId
                        ? list.some((s) => s.id === initialSessionId)
                        : initialSessionFound;
                }

                setSessions(list);
                setTimelineGroups(groups);

                setSelectedTimelineGroupId((prev) => {
                    if (prev && groups.some((group) => group.id === prev)) {
                        return prev;
                    }
                    if (initialSessionId) {
                        const anchoredGroup = groups.find((group) => (group.sessionIds ?? []).includes(initialSessionId));
                        if (anchoredGroup) {
                            return anchoredGroup.id;
                        }
                    }
                    return groups[0]?.id ?? null;
                });

                setSelectedId((prev) => {
                    // Keep initialSessionId only if the session actually exists
                    if (prev === initialSessionId && initialSessionFound) {
                        return prev;
                    }
                    if (groups.length > 0 && groups[0].primarySessionId) {
                        return groups[0].primarySessionId;
                    }
                    // Otherwise auto-select the first result
                    if (list.length > 0) {
                        return list[0].id;
                    }
                    return null;
                });
            })
            .then(() => setSessionsFailed(false))
            .catch((err) => {
                console.error('[activity] load sessions failed:', err);
                setSessionsFailed(true);
            })
            .finally(() => setLoading(false));

        // Fire semantic search in parallel (non-blocking, best-effort)
        if (debouncedKeyword.trim().length >= 3) {
            SemanticSearchActivitySessions(debouncedKeyword, 20)
                .then((results) => {
                    const map = new Map<string, { score: number; matchText: string }>();
                    for (const r of results ?? []) {
                        map.set(r.sessionId, { score: r.score, matchText: r.matchText });
                    }
                    setSemanticMatches(map);
                })
                .catch(() => setSemanticMatches(new Map()));
        } else {
            setSemanticMatches(new Map());
        }
    }, [isTimeline, timeRange.startMs, timeRange.endMs, debouncedKeyword, anchorReady, restrictToSessionIds]);

    // Scroll the anchor session into the sidebar viewport once — after the
    // sessions list has loaded and the selected button has been mounted. Only
    // fires for the initial jump so manual user filtering / clicks after that
    // don't yank the scroll position.
    useEffect(() => {
        if (didAnchorScrollRef.current) return;
        if (!initialSessionId) return;
        if (selectedId !== initialSessionId) return;
        const el = selectedSessionRef.current;
        if (!el) return;
        el.scrollIntoView({ block: 'center', behavior: 'auto' });
        didAnchorScrollRef.current = true;
    }, [initialSessionId, selectedId, timelineGroups]);

    // Auto-refresh session list when a new session is saved by the backend,
    // or when the backend signals that startup is complete (the initial fetch
    // may have arrived before the activity store was initialized). Timeline
    // data only loads while the Screen timeline section shows, so the
    // subscription waits for it too.
    useEffect(() => {
        if (!isTimeline) return;
        // Pipeline-jump mode shows a fixed group; an incoming `session-saved`
        // event would clobber the filtered list with the unfiltered query
        // result, snapping the user back to the date-filter view. Skip the
        // refresh subscription entirely while restricted.
        if (restrictToSessionIds && restrictToSessionIds.length > 0) return;
        const refresh = () => {
            Promise.all([
                GetActivitySessions(200, 0, timeRange.startMs, timeRange.endMs, debouncedKeyword),
                GetActivityTimelineGroups(200, 0, timeRange.startMs, timeRange.endMs, debouncedKeyword),
            ])
                .then(([sessionResult, groupResult]) => {
                    setSessions(sessionResult ?? []);
                    setTimelineGroups(groupResult ?? []);
                    setSelectedTimelineGroupId((prev) => {
                        const groups = groupResult ?? [];
                        if (prev && groups.some((group) => group.id === prev)) {
                            return prev;
                        }
                        return groups[0]?.id ?? null;
                    });
                })
                .then(() => setSessionsFailed(false))
                .catch((err) => {
                    console.error('[activity] refresh sessions failed:', err);
                    setSessionsFailed(true);
                });
        };
        refreshSessionsRef.current = refresh;
        const unsub1 = EventsOn('activity:session-saved', refresh);
        const unsub2 = EventsOn('app:ready', refresh);
        // The backend summary worker (and the on-demand path) signal run/
        // summary changes here; group ids are stable, so the keep-if-present
        // logic above preserves the user's selection across refreshes.
        const unsub3 = EventsOn('activity:timeline-groups-updated', refresh);
        return () => { unsub1(); unsub2(); unsub3(); };
    }, [isTimeline, timeRange.startMs, timeRange.endMs, debouncedKeyword, restrictToSessionIds]);

    const selectedTimelineGroup = useMemo(() => {
        if (!selectedTimelineGroupId) return null;
        return timelineGroups.find((group) => group.id === selectedTimelineGroupId) ?? null;
    }, [selectedTimelineGroupId, timelineGroups]);
    // Stable identity for the detail loader below. Background patches to
    // `timelineGroups` (summary backfill, groups-updated events) replace the
    // array and hand the memo a fresh object even when nothing the loader
    // cares about changed; keying the loader on the object would re-fetch the
    // detail and reset snapshotIndex to 0, yanking the event list away from a
    // Who you know jump anchor moments after it landed (the anchor guard is
    // one-shot). Only id + membership matter to the fetch.
    const selectedTimelineGroupKey = selectedTimelineGroup
        ? `${selectedTimelineGroup.id}:${(selectedTimelineGroup.sessionIds ?? []).join(',')}`
        : null;
    const selectedTimelineSummary = selectedTimelineGroup?.summary ?? null;
    // The capture sessions behind the open row: a meeting's notes show when
    // one of them is a slice of that meeting.
    const meetingNotesSessionIds = useMemo(
        () => selectedTimelineGroup?.sessionIds ?? (selectedId ? [selectedId] : []),
        [selectedTimelineGroup, selectedId],
    );
    // The group detail loads async after a group is selected, so for a beat
    // the summary timeline renders while its event ids can't be resolved yet
    // (or resolve against the previous group's detail). During that window
    // rows hold a blank icon slot instead of flashing the 'focus' fallback
    // that the real kind then replaces.
    const timelineEventsPending = !!selectedTimelineGroup && detail?.id !== selectedTimelineGroup.id;
    const stepEventRows = stepEventIndexes(detail?.events ?? []);
    const stepEventSet = new Set(stepEventRows);
    const visibleDurationMs = selectedTimelineGroup ? selectedTimelineGroup.activeMs : (detail ? detail.endTime - detail.startTime : 0);

    useEffect(() => {
        setExpandedSemanticTimelineItems(new Set());
    }, [selectedTimelineGroup?.id]);

    // On-demand summary kick for the selected group. Summaries are normally
    // produced by the backend worker; this covers runs outside its
    // auto-summarize horizon (old backfilled runs) and accelerates the
    // selected one. The backend's claim makes duplicate calls no-ops and all
    // rate limiting (retry backoff, permanent failures) lives backend-side;
    // results also flow through 'activity:timeline-groups-updated', but the
    // local patch below keeps pipeline-jump mode working, where that
    // subscription is disabled.
    useEffect(() => {
        if (!isTimeline) return;
        if (!selectedTimelineGroup || selectedTimelineGroup.summary) return;
        const status = selectedTimelineGroup.summaryStatus;
        if (status !== 'none' && status !== 'failed_transient') return;
        const groupId = selectedTimelineGroup.id;
        GenerateActivityTimelineGroupSummary(groupId)
            .then((summary) => {
                setTimelineGroups((prev) =>
                    prev.map((item) =>
                        item.id === groupId
                            ? { ...item, summary, summaryStatus: 'done' }
                            : item,
                    ),
                );
            })
            .catch(() => {
                // Expected for in-progress/backoff/permanent states; status
                // changes arrive via the groups-updated event instead.
            });
    }, [isTimeline, selectedTimelineGroup]);

    useEffect(() => {
        if (!isTimeline) return;
        if (selectedTimelineGroup || timelineGroups.length === 0) return;

        const bySelectedSession = selectedId
            ? timelineGroups.find((group) => (group.sessionIds ?? []).includes(selectedId))
            : null;
        setSelectedTimelineGroupId(bySelectedSession?.id ?? timelineGroups[0].id);
    }, [isTimeline, selectedTimelineGroup, selectedId, timelineGroups]);

    // Load the visible detail. The Screen timeline section uses app-run
    // groups; other session-oriented entry points can still fall back to the
    // raw session. Nothing loads while a Day/Week section shows.
    const [detailError, setDetailError] = useState<string | null>(null);
    useEffect(() => {
        if (!isTimeline) return;
        if (selectedTimelineGroup) {
            let cancelled = false;
            setDetailError(null);
            // Keep the anchored session when it belongs to this group — the
            // People/voice surfaces (ListSessionPrincipals,
            // ListSessionSpeakerClusters) key off selectedId, and a People
            // jump targets a specific member session, not necessarily the
            // group's primary one.
            setSelectedId((prev) =>
                prev && (selectedTimelineGroup.sessionIds ?? []).includes(prev)
                    ? prev
                    : selectedTimelineGroup.primarySessionId,
            );
            Promise.all(
                (selectedTimelineGroup.sessionIds ?? [selectedTimelineGroup.primarySessionId]).map((id) =>
                    GetActivitySessionDetail(id),
                ),
            )
                .then((results) => {
                    if (cancelled) return;
                    const groupDetail = buildTimelineGroupDetail(
                        selectedTimelineGroup,
                        results.filter((result): result is main.ActivitySessionDetail => Boolean(result)),
                    );
                    setDetail(groupDetail);
                    setSegmentIndex(0);
                    // Land on the Who you know jump anchor when this group still holds
                    // the anchored session and the user hasn't scrubbed away —
                    // otherwise the first frame. Re-applied on every reload so a
                    // background groups refresh can't yank the anchor to 0.
                    const anchorActive =
                        !anchorReleasedRef.current &&
                        !!initialSessionId &&
                        (selectedTimelineGroup.sessionIds ?? []).includes(initialSessionId);
                    setSnapshotIndex(
                        anchorActive && initialEventTimestamp && groupDetail.snapshots?.length
                            ? closestSnapshotIndexTo(groupDetail.snapshots, initialEventTimestamp)
                            : 0,
                    );
                })
                .catch((err) => {
                    if (cancelled) return;
                    console.error('[activity] load timeline group detail failed:', err);
                    setDetail(null);
                    setDetailError(t('session.missingError', { id: selectedTimelineGroup.id }) as string);
                });
            return () => {
                cancelled = true;
            };
        }

        if (!selectedId) {
            setDetail(null);
            setDetailError(null);
            return;
        }
        let cancelled = false;
        setDetailError(null);
        GetActivitySessionDetail(selectedId)
            .then((result) => {
                if (cancelled) return;
                setDetail(result);
                setSegmentIndex(0);
                // Same Who you know jump anchor as the timeline branch: land on the
                // moment the caller passed instead of the first frame, and keep
                // re-applying it across reloads until the user scrubs away.
                const anchorActive = !anchorReleasedRef.current && selectedId === initialSessionId;
                setSnapshotIndex(
                    anchorActive && initialEventTimestamp && result?.snapshots?.length
                        ? closestSnapshotIndexTo(result.snapshots, initialEventTimestamp)
                        : 0,
                );
            })
            .catch((err) => {
                if (cancelled) return;
                console.error('[activity] load detail failed:', err);
                setDetail(null);
                setDetailError(t('session.missingError', { id: selectedId }) as string);
            });
        return () => {
            cancelled = true;
        };
        // `selectedTimelineGroup` is read from the render closure on purpose:
        // depending on the object identity would re-run this loader on every
        // background patch to `timelineGroups` (see selectedTimelineGroupKey).
    }, [isTimeline, selectedId, selectedTimelineGroupKey, t]);

    // The Who you know jump anchor (initialEventTimestamp) is applied inside the
    // detail loader above, at the moment each detail resolves — see the
    // `anchorActive` blocks. Doing it there (rather than in a separate effect
    // that reacts to `detail`) means the anchor and the frame reset are one
    // atomic write, so a background reload can't briefly flash frame 0 or
    // strand the operator there when the one-shot guard had already fired.
    //
    // Re-arm on a fresh anchor: a new jump (new session/timestamp) should land
    // on its moment even if the view stayed mounted from a prior jump the user
    // had scrubbed away from. Harmless no-op on the initial mount.
    useEffect(() => {
        anchorReleasedRef.current = false;
    }, [initialSessionId, initialEventTimestamp]);

    // ── OCR match state ──
    const [ocrMatches, setOcrMatches] = useState<main.OCRMatchFrame[]>([]);

    // Load OCR matches when selected session or keyword changes
    useEffect(() => {
        if (!selectedId || !debouncedKeyword) {
            setOcrMatches([]);
            return;
        }
        GetActivityOCRMatches(selectedId, debouncedKeyword)
            .then((result) => setOcrMatches(result ?? []))
            .catch(() => setOcrMatches([]));
    }, [selectedId, debouncedKeyword]);

    // Escape key: close popup → exit fullscreen → close page (when the parent
    // gives a close handler; as a main view the page has none).
    useEffect(() => {
        const handleKeyDown = (e: KeyboardEvent) => {
            if (e.key === 'Escape') {
                // The frame-preview lightbox (Activities tab) owns Escape while
                // open and closes itself; don't run this ladder underneath it,
                // or the whole page closes behind the preview.
                if (
                    document.body.hasAttribute('data-activity-lightbox-open')
                ) {
                    return;
                }
                e.preventDefault();
                if (eventPopup) {
                    setEventPopup(null);
                } else if (isFullscreen) {
                    setIsFullscreen(false);
                } else {
                    onClose?.();
                }
            }
        };
        window.addEventListener('keydown', handleKeyDown);
        return () => window.removeEventListener('keydown', handleKeyDown);
    }, [onClose, isFullscreen, eventPopup]);

    // Extract unique app names for the app filter dropdown. Drives selectedApps
    // over the loaded app-run groups.
    const allAppNames = useMemo(() => {
        const set = new Set<string>();
        for (const group of timelineGroups) {
            if (group.appName) set.add(group.appName);
        }
        return Array.from(set).sort();
    }, [timelineGroups]);

    // Apply client-side app filter, then group by date
    const filteredTimelineGroups = useMemo(() => {
        if (!selectedApps) return timelineGroups;
        return timelineGroups.filter((group) => selectedApps.has(group.appName));
    }, [timelineGroups, selectedApps]);

    const grouped = useMemo(
        () => groupTimelineGroupsByDate(filteredTimelineGroups, t('groupHeaders.today') as string, t('groupHeaders.yesterday') as string),
        [filteredTimelineGroups, t],
    );

    const currentSegment = detail?.segments?.[segmentIndex] ?? null;
    const segmentCount = detail?.segments?.length ?? 0;

    const videoSrc = useMemo(() => {
        if (!currentSegment || !bridgePort) return null;
        return hostBridgeMediaURL(bridgePort, 'video', { path: currentSegment.filePath });
    }, [currentSegment, bridgePort]);

    const handlePrevSegment = useCallback(() => {
        setSegmentIndex((i) => Math.max(0, i - 1));
    }, []);

    const handleNextSegment = useCallback(() => {
        setSegmentIndex((i) => Math.min(segmentCount - 1, i + 1));
    }, [segmentCount]);

    const videoContainerRef = useRef<HTMLDivElement>(null);

    // Snapshot-based session support
    const snapshots = detail?.snapshots ?? [];
    const isSnapshotSession = snapshots.length > 0;
    const currentSnapshot = snapshots[snapshotIndex] ?? null;

    // Refresh OCR boxes for the highlighted person's name whenever the
    // current snapshot or the highlighted name changes. Falls back to
    // empty boxes when either is missing. Same GetOCRHighlightBoxes
    // path the EventFramePopup uses for keyword highlights — the
    // sherpa worker is fast (~10-50ms per snapshot) and the result is
    // already cached by snapshotPath in the host bridge, so we don't
    // bother debouncing further.
    useEffect(() => {
        const name = effectiveHighlightedSenderName?.trim();
        if (!name || !currentSnapshot?.filePath) {
            setPersonHighlightBoxes([]);
            return;
        }
        let cancelled = false;
        GetOCRHighlightBoxes(currentSnapshot.filePath, name)
            .then((boxes) => {
                if (!cancelled) setPersonHighlightBoxes(boxes ?? []);
            })
            .catch(() => {
                if (!cancelled) setPersonHighlightBoxes([]);
            });
        return () => {
            cancelled = true;
        };
    }, [currentSnapshot?.filePath, effectiveHighlightedSenderName]);

    // Snapshots flagged "used by the triage prompt" for the active session,
    // mapped down to the indices we can render on the timeline + the badge.
    // highlightedFrames carries (sessionId, snapshotId) for every frame in
    // the whole group, so we filter to the visible session first.
    // Active frames (kind=parsed / kind=ocr) — what actually went into the
    // prompt. These get the bold green ticks + the "Used in triage" badge.
    // Used frames are split by kind so the timeline can color-code them:
    // parsed (emerald) and OCR (sky). A single snapshot can carry both
    // kinds (the same row produced parsed_json AND was OCR'd), in which
    // case we draw both ticks at that index — parsed on top.
    const parsedSnapshotIdsForSession = useMemo(() => {
        if (!highlightedFrames || highlightedFrames.length === 0) return new Set<number>();
        const out = new Set<number>();
        for (const f of highlightedFrames) {
            if (f.kind !== 'parsed') continue;
            if (f.sessionId === selectedId) out.add(f.snapshotId);
        }
        return out;
    }, [highlightedFrames, selectedId]);
    const ocrSnapshotIdsForSession = useMemo(() => {
        if (!highlightedFrames || highlightedFrames.length === 0) return new Set<number>();
        const out = new Set<number>();
        for (const f of highlightedFrames) {
            if (f.kind !== 'ocr') continue;
            if (f.sessionId === selectedId) out.add(f.snapshotId);
        }
        return out;
    }, [highlightedFrames, selectedId]);
    const parsedSnapshotIndices = useMemo(() => {
        if (parsedSnapshotIdsForSession.size === 0) return [] as number[];
        const out: number[] = [];
        for (let i = 0; i < snapshots.length; i++) {
            if (parsedSnapshotIdsForSession.has(snapshots[i].id)) out.push(i);
        }
        return out;
    }, [parsedSnapshotIdsForSession, snapshots]);
    const ocrSnapshotIndices = useMemo(() => {
        if (ocrSnapshotIdsForSession.size === 0) return [] as number[];
        const out: number[] = [];
        for (let i = 0; i < snapshots.length; i++) {
            if (ocrSnapshotIdsForSession.has(snapshots[i].id)) out.push(i);
        }
        return out;
    }, [ocrSnapshotIdsForSession, snapshots]);
    // Union of both kinds — used by the cross-session "is current frame
    // anywhere in the triage prompt" sibling banner gate.
    const highlightedSnapshotIdsForSession = useMemo(() => {
        const out = new Set<number>();
        parsedSnapshotIdsForSession.forEach((id) => out.add(id));
        ocrSnapshotIdsForSession.forEach((id) => out.add(id));
        return out;
    }, [parsedSnapshotIdsForSession, ocrSnapshotIdsForSession]);
    const currentSnapshotIsHighlighted = currentSnapshot
        ? highlightedSnapshotIdsForSession.has(currentSnapshot.id)
        : false;
    const currentSnapshotKind: 'parsed' | 'ocr' | 'both' | null = currentSnapshot
        ? (() => {
              const inParsed = parsedSnapshotIdsForSession.has(currentSnapshot.id);
              const inOcr = ocrSnapshotIdsForSession.has(currentSnapshot.id);
              if (inParsed && inOcr) return 'both';
              if (inParsed) return 'parsed';
              if (inOcr) return 'ocr';
              return null;
          })()
        : null;

    // Normalized [0,1] focused-window bounds attached to the snapshot row by
    // the capture path. Zero W/H means "unknown" (legacy snapshot, multi-
    // display fallback, non-darwin or race-detected mismatch — all cases
    // where the capture pipeline deliberately cleared the rect). We render
    // the box whenever the rect is known: the legend reads "Focused window"
    // (a capture-time fact), so showing it does NOT lie about OCR scope.
    // The companion ocrUsedFocusRect flag is still persisted by the OCR
    // worker for downstream consumers that want stricter audit ("did OCR
    // actually scope its input here") — gating the visual on it stripped
    // the box from every frame the OCR sampler skipped (~half the
    // snapshots, since the per-group sampler picks ~one frame per 15s),
    // which read as "feature broken" even when capture worked correctly.
    // The cast smooths over the Wails TS bindings, which only regenerate
    // on the next `wails dev` run.
    const currentFocusRect = currentSnapshot
        ? (currentSnapshot as unknown as {
              focusRectX?: number;
              focusRectY?: number;
              focusRectW?: number;
              focusRectH?: number;
              ocrUsedFocusRect?: boolean;
          })
        : null;
    // Per-window captures fill the frame (rect ≈ 1×1); the box would just outline
    // the whole image. Only draw it for full-screen fallback frames whose rect
    // marks a real sub-region.
    const hasFocusRect = !!(
        currentFocusRect &&
        (currentFocusRect.focusRectW ?? 0) > 0 &&
        (currentFocusRect.focusRectH ?? 0) > 0 &&
        ((currentFocusRect.focusRectW ?? 0) < 0.98 || (currentFocusRect.focusRectH ?? 0) < 0.98)
    );

    // Per-kind winner metadata for the current snapshot. Used by the
    // inline "As seen by LLM" panel to show:
    //   - the exact promptText (what the LLM saw, post-dedup/truncation)
    //   - quality fields (siblingCount, ocrConfidence, browserUrl) so
    //     operators can judge "is this a representative winner" without
    //     opening a second view.
    // Returns one entry per kind ("parsed" / "ocr") if the current
    // snapshot was a winner of that kind in the current session; null
    // otherwise.
    type WinnerMeta = {
        promptText: string;
        siblingCount?: number;
        ocrConfidence?: number;
        browserUrl?: string;
    };
    const currentSnapshotWinnerMeta = useMemo(() => {
        const out: { parsed: WinnerMeta | null; ocr: WinnerMeta | null } = { parsed: null, ocr: null };
        if (!currentSnapshot || !highlightedFrames) return out;
        for (const f of highlightedFrames) {
            if (f.sessionId !== selectedId) continue;
            if (f.snapshotId !== currentSnapshot.id) continue;
            if (f.kind === 'parsed' && !out.parsed) {
                out.parsed = {
                    promptText: f.promptText ?? '',
                    siblingCount: f.parsedSiblingCount,
                    browserUrl: f.browserUrl,
                };
            } else if (f.kind === 'ocr' && !out.ocr) {
                out.ocr = {
                    promptText: f.promptText ?? '',
                    ocrConfidence: f.ocrConfidence,
                    browserUrl: f.browserUrl,
                };
            }
        }
        return out;
    }, [currentSnapshot, highlightedFrames, selectedId]);

    // Dedup siblings (kind=parsed_sibling) — frames whose content the
    // prompt represented through a winner snapshot. Rendered as muted
    // gray ticks so operators see "these were considered & folded in,
    // not silently dropped." Tracked separately from the active set so
    // banner / counter math stays grounded in what truly hit the prompt.
    //
    // Winners take priority: a snapshot that's ALSO a parsed/ocr winner
    // in this session should not paint a slate sibling tick on top —
    // that would imply two stories ("this frame both contributed AND was
    // folded") which is incoherent to read. The winner marker already
    // says "this frame contributed".
    const siblingSnapshotIdsForSession = useMemo(() => {
        if (!highlightedFrames || highlightedFrames.length === 0) return new Set<number>();
        const out = new Set<number>();
        for (const f of highlightedFrames) {
            if (f.kind !== 'parsed_sibling') continue;
            if (f.sessionId !== selectedId) continue;
            if (parsedSnapshotIdsForSession.has(f.snapshotId)) continue;
            if (ocrSnapshotIdsForSession.has(f.snapshotId)) continue;
            out.add(f.snapshotId);
        }
        return out;
    }, [highlightedFrames, selectedId, parsedSnapshotIdsForSession, ocrSnapshotIdsForSession]);
    const siblingSnapshotIndices = useMemo(() => {
        if (siblingSnapshotIdsForSession.size === 0) return [] as number[];
        const out: number[] = [];
        for (let i = 0; i < snapshots.length; i++) {
            if (siblingSnapshotIdsForSession.has(snapshots[i].id)) out.push(i);
        }
        return out;
    }, [siblingSnapshotIdsForSession, snapshots]);
    const currentSnapshotIsSibling = currentSnapshot
        ? siblingSnapshotIdsForSession.has(currentSnapshot.id)
        : false;

    // Skipped frames (kind=parsed_skipped / ocr_skipped) — frames the
    // pipeline considered but actively dropped. Rendered as hollow rose
    // ticks so operators can distinguish "the pipeline saw this and
    // chose not to use it" (audit signal) from siblings ("represented by
    // a winner") and from snapshots that were never considered at all
    // (no tick). skipReason on each frame disambiguates which gate
    // dropped it (parsed_oversize, ocr_covered_by_parsed,
    // ocr_prefix_dedup, ocr_over_cap).
    //
    // Priority: a snapshot can legitimately carry BOTH a sibling record
    // (its parsed got folded into a winner) AND an ocr_covered_by_parsed
    // skip record (the OCR for the same snapshot was dropped because
    // parsed covered it). For operator audit purposes the two facts have
    // the same upshot — "this frame's content reached the LLM via the
    // winner, not via this snapshot directly" — so we suppress the hollow
    // tick when winner/sibling already paint the snapshot. The drop
    // counter in the banner still includes these so totals stay honest.
    // Pairs the hollow tick's skip reason with the P1 dedup chain length
    // (only meaningful for ocr_prefix_dedup) so the popup badge can
    // append "N prior frames had this text" — distinguishes a single
    // near-duplicate from a screen the user stared at for minutes.
    type SkipInfo = { reason: string; dedupChainLen?: number };
    const skippedSnapshotInfoForSession = useMemo(() => {
        const out = new Map<number, SkipInfo>();
        if (!highlightedFrames || highlightedFrames.length === 0) return out;
        for (const f of highlightedFrames) {
            if (f.kind !== 'parsed_skipped' && f.kind !== 'ocr_skipped') continue;
            if (f.sessionId !== selectedId) continue;
            if (parsedSnapshotIdsForSession.has(f.snapshotId)) continue;
            if (ocrSnapshotIdsForSession.has(f.snapshotId)) continue;
            if (siblingSnapshotIdsForSession.has(f.snapshotId)) continue;
            // First reason wins; snapshots that show up under multiple
            // reasons (rare; a snapshot is at most one parsed group, but
            // its OCR sibling could theoretically also skip) keep the
            // earliest decision the pipeline made.
            if (!out.has(f.snapshotId)) {
                out.set(f.snapshotId, {
                    reason: f.skipReason ?? '',
                    dedupChainLen: f.dedupChainLen,
                });
            }
        }
        return out;
    }, [highlightedFrames, selectedId, parsedSnapshotIdsForSession, ocrSnapshotIdsForSession, siblingSnapshotIdsForSession]);
    const skippedSnapshotIndices = useMemo(() => {
        if (skippedSnapshotInfoForSession.size === 0) return [] as number[];
        const out: number[] = [];
        for (let i = 0; i < snapshots.length; i++) {
            if (skippedSnapshotInfoForSession.has(snapshots[i].id)) out.push(i);
        }
        return out;
    }, [skippedSnapshotInfoForSession, snapshots]);
    const currentSnapshotSkipInfo = currentSnapshot
        ? skippedSnapshotInfoForSession.get(currentSnapshot.id) ?? null
        : null;

    // Cross-session distribution for the pipeline-jump banner. A pipeline
    // "group" can span multiple ActivitySessionRows, so the frames the
    // triage prompt sampled may live in a session OTHER than the one we
    // landed on. Without surfacing that, an operator hits "View 6 used
    // frames" → lands on session A → sees zero ticks → assumes the feature
    // is broken. The banner shows the per-session breakdown and lets them
    // jump to the session that actually carries the frames.
    const pipelineFrameDistribution = useMemo(() => {
        if (!highlightedFrames || highlightedFrames.length === 0) return null;
        const counts = new Map<string, number>();
        for (const f of highlightedFrames) {
            // Only count frames that actually contributed to the prompt.
            // Siblings were folded into a winner; skipped frames were
            // dropped by oversize / coverage / dedup / cap gates.
            // Counting either in the banner / Switch buttons would
            // overstate per-session footprint and either double-count
            // information the winner already carries (siblings) or
            // suggest content the LLM never saw (skipped).
            if (f.kind !== 'parsed' && f.kind !== 'ocr') continue;
            counts.set(f.sessionId, (counts.get(f.sessionId) ?? 0) + 1);
        }
        return counts;
    }, [highlightedFrames]);
    const pipelineCurrentSessionCount = pipelineFrameDistribution && selectedId
        ? pipelineFrameDistribution.get(selectedId) ?? 0
        : 0;
    const pipelineOtherSessions = useMemo(() => {
        if (!pipelineFrameDistribution) return [] as { sessionId: string; count: number; label: string }[];
        const out: { sessionId: string; count: number; label: string }[] = [];
        for (const [sid, count] of pipelineFrameDistribution.entries()) {
            if (sid === selectedId) continue;
            const sess = sessions.find((s) => s.id === sid);
            const label = sess
                ? t('session.triageBannerSessionLabel', {
                    start: formatTimeShort(sess.startTime),
                    end: formatTimeShort(sess.endTime),
                  }) as string
                : sid.slice(0, 8);
            out.push({ sessionId: sid, count, label });
        }
        out.sort((a, b) => b.count - a.count);
        return out;
    }, [pipelineFrameDistribution, selectedId, sessions, t]);
    const pipelineTotalFrames = useMemo(
        () => (highlightedFrames ?? []).filter((f) => f.kind === 'parsed' || f.kind === 'ocr').length,
        [highlightedFrames],
    );
    const pipelineTotalSiblings = useMemo(
        () => (highlightedFrames ?? []).filter((f) => f.kind === 'parsed_sibling').length,
        [highlightedFrames],
    );
    const pipelineTotalSkipped = useMemo(
        () =>
            (highlightedFrames ?? []).filter(
                (f) => f.kind === 'parsed_skipped' || f.kind === 'ocr_skipped',
            ).length,
        [highlightedFrames],
    );

    // A frame marked image-missing, or whose file fails to load (removed by
    // storage rotation, or not written yet), falls back to the "No snapshot"
    // placeholder and hides the overlays drawn on top of the image.
    const currentSnapshotURL = useMemo(() => {
        if (!currentSnapshot || !bridgePort) return null;
        return snapshotImageURL(bridgePort, currentSnapshot);
    }, [currentSnapshot, bridgePort]);
    const { visibleSrc: snapshotSrc, onError: handleSnapshotError } = useSnapshotFallback(currentSnapshotURL);

    // Fullscreen shows the current frame; with no frame to show, leave it
    // instead of keeping an empty overlay armed for the next frame.
    useEffect(() => {
        if (isSnapshotSession && !snapshotSrc) setIsFullscreen(false);
    }, [isSnapshotSession, snapshotSrc]);

    // User-driven snapshot navigation. Releases the Who you know jump anchor so a
    // later background detail reload leaves the operator on the frame they
    // scrubbed to instead of snapping back to the auto-jumped moment.
    const navigateSnapshot = useCallback((next: React.SetStateAction<number>) => {
        anchorReleasedRef.current = true;
        setSnapshotIndex(next);
    }, []);

    const handlePrevSnapshot = useCallback(() => {
        navigateSnapshot((i) => Math.max(0, i - 1));
    }, [navigateSnapshot]);

    const handleNextSnapshot = useCallback(() => {
        navigateSnapshot((i) => Math.min(snapshots.length - 1, i + 1));
    }, [snapshots.length, navigateSnapshot]);

    // Auto-play slideshow for snapshot sessions
    useEffect(() => {
        if (!isPlaying || !isSnapshotSession || snapshots.length <= 1) return;
        const interval = setInterval(() => {
            setSnapshotIndex((i) => {
                if (i >= snapshots.length - 1) {
                    setIsPlaying(false);
                    return i;
                }
                return i + 1;
            });
        }, 1000 / playbackFps);
        return () => clearInterval(interval);
    }, [isPlaying, isSnapshotSession, snapshots.length, playbackFps]);

    const togglePlay = useCallback(() => {
        // Starting playback is deliberate navigation — release the anchor so the
        // auto-advancing frame isn't yanked back by a background reload.
        anchorReleasedRef.current = true;
        setIsPlaying((prev) => {
            if (!prev && snapshotIndex >= snapshots.length - 1) {
                // Restart from beginning if at end
                setSnapshotIndex(0);
            }
            return !prev;
        });
    }, [snapshotIndex, snapshots.length]);

    // Stop playback and leave fullscreen when switching sessions
    useEffect(() => {
        setIsPlaying(false);
        setIsFullscreen(false);
    }, [selectedId]);

    const toggleFullscreen = useCallback(() => {
        setIsFullscreen((prev) => !prev);
    }, []);

    // Per-snapshot bundleId, parsed lazily from parsedJson.metadata.bundleId.
    // Snapshots where no app parser matched have parsedJson === "" and end up
    // as undefined — those rows can't constrain app-matching and stay eligible
    // only via the closest-by-time fallback.
    const snapshotBundleIds = useMemo<(string | undefined)[]>(() => {
        const out: (string | undefined)[] = [];
        for (const s of detail?.snapshots ?? []) {
            if (!s.parsedJson) {
                out.push(undefined);
                continue;
            }
            try {
                const pc = JSON.parse(s.parsedJson) as ParsedContent;
                out.push(pc.metadata?.bundleId);
            } catch {
                out.push(undefined);
            }
        }
        return out;
    }, [detail?.snapshots]);

    // ── Find closest snapshot index for a given timestamp ──
    //
    // When `bundleId` is supplied (event click path), prefer snapshots whose
    // parsedJson metadata reports the same bundle — otherwise sparse capture
    // (e.g. one snapshot per several seconds) silently routes a click on app
    // X to app Y's nearest-in-time snapshot, and the popup shows Y's frame
    // and parsed content under X's header.
    const findClosestSnapshotIndex = useCallback(
        (timestamp: number, bundleId?: string): number => {
            if (!detail?.snapshots?.length) return 0;
            const snaps = detail.snapshots;

            const pickClosest = (eligible: (i: number) => boolean): number => {
                let bestIdx = -1;
                let bestDist = Infinity;
                for (let i = 0; i < snaps.length; i++) {
                    if (!eligible(i)) continue;
                    const dist = Math.abs(snaps[i].timestamp - timestamp);
                    if (dist < bestDist) {
                        bestDist = dist;
                        bestIdx = i;
                    }
                }
                return bestIdx;
            };

            if (bundleId) {
                // Constrained to the event's OWN app. If that app has no snapshot in
                // this session, return -1 instead of falling back to another app's
                // frame — surfacing app-Y's screenshot for an app-X event is wrong and
                // misleading (this honors the doc above). Callers treat -1 as "no
                // frame for this event" (row becomes non-clickable).
                return pickClosest((i) =>
                    snapshotMatchesEventBundle(snaps[i], snapshotBundleIds[i], bundleId),
                );
            }
            // No bundle (e.g. OCR text-match jumps): closest of any app is acceptable.
            const any = pickClosest(() => true);
            return any === -1 ? 0 : any;
        },
        [detail, snapshotBundleIds],
    );

    // Precompute per-event media availability once when detail loads, so the
    // Virtuoso itemContent renderer does an O(1) map lookup. We only need "does a
    // same-app snapshot exist", NOT the closest one, so this builds a snapshot-app
    // index once (O(snapshots)) and does an O(1) check per event — overall
    // O(events + snapshots) instead of O(events × snapshots), which otherwise
    // blocked the main thread on long sessions.
    const eventHasMedia = useMemo(() => {
        const m = new Map<number, boolean>();
        if (!detail || bridgePort <= 0) {
            return m;
        }
        const snaps = detail.snapshots ?? [];
        if (snaps.length === 0) {
            // No snapshots: availability is segment-based (legacy HEVC sessions).
            const hasSegs = (detail.segments?.length ?? 0) > 0;
            detail.events?.forEach((_, idx) => m.set(idx, hasSegs));
            return m;
        }
        // Snapshot-app coverage (mirrors snapshotMatchesEventBundle): the set of
        // bundle ids that have a snapshot, plus whether any bundle-less snapshot
        // carries a browserUrl (the browser-fallback case).
        const snapBundles = new Set<string>();
        let hasBrowserFallback = false;
        snaps.forEach((s, i) => {
            const b = snapshotBundleIds[i];
            if (b) {
                snapBundles.add(b);
            } else if (s.browserUrl) {
                hasBrowserFallback = true;
            }
        });
        detail.events?.forEach((evt, idx) => {
            const b = evt.appBundleId;
            m.set(
                idx,
                b
                    ? snapBundles.has(b) || (hasBrowserFallback && browserBundleIds.has(b))
                    : true, // no bundle constraint → any snapshot qualifies (matches the old no-bundle fallback)
            );
        });
        return m;
    }, [detail, bridgePort, snapshotBundleIds]);

    const timelineEventIndexById = useMemo(() => {
        const map = new Map<string, number>();
        detail?.events?.forEach((event, idx) => {
            const id = timelineEventId(event);
            if (id) {
                map.set(id, idx);
            }
        });
        return map;
    }, [detail]);

    // Track which event index is "active" based on current snapshot timestamp
    const activeEventIndex = useMemo(() => {
        if (!detail?.events?.length || !detail?.snapshots?.length) return -1;
        const currentTs = detail.snapshots[snapshotIndex]?.timestamp;
        if (!currentTs) return -1;
        let best = -1;
        for (let i = 0; i < detail.events.length; i++) {
            if (detail.events[i].timestamp <= currentTs) {
                best = i;
            } else {
                break;
            }
        }
        return best;
    }, [detail, snapshotIndex]);

    // Ref for Virtuoso to allow programmatic scrolling
    const virtuosoRef = useRef<import('react-virtuoso').VirtuosoHandle>(null);

    // Measure the right-pane ScrollArea so the event list can fill the
    // panel's remaining vertical space. Radix ScrollArea wraps children
    // in an internal block-level div with no defined height, which
    // breaks `min-h-full` and percentage-height flex chains — so we
    // pin the content column's minHeight explicitly from JS, and pass
    // the Virtuoso wrapper's resolved pixel height to Virtuoso.
    const scrollAreaRef = useRef<HTMLDivElement>(null);
    const [scrollAreaHeight, setScrollAreaHeight] = useState(0);
    useEffect(() => {
        const el = scrollAreaRef.current;
        if (!el) return;
        const ro = new ResizeObserver((entries) => {
            for (const entry of entries) {
                setScrollAreaHeight(entry.contentRect.height);
            }
        });
        ro.observe(el);
        return () => ro.disconnect();
    }, []);

    const eventsListContainerRef = useRef<HTMLDivElement>(null);
    const [eventsListHeight, setEventsListHeight] = useState(480);
    useEffect(() => {
        const el = eventsListContainerRef.current;
        if (!el) return;
        const ro = new ResizeObserver((entries) => {
            for (const entry of entries) {
                const h = entry.contentRect.height;
                if (h > 0) setEventsListHeight(h);
            }
        });
        ro.observe(el);
        return () => ro.disconnect();
    }, [selectedId]);
    // Auto-scroll event list to active event when snapshot changes.
    //
    // This effect is meant for snapshot-driven highlight moves (playback
    // advancing, popup prev/next stepping through siblings) — in those
    // cases the active row may be off-screen and the scroll keeps it
    // visible. It is NOT meant for user-initiated clicks on rows in the
    // list itself: the user just clicked a row that was by definition
    // already in view, and re-centering it yanks the list out from under
    // them. The list-row onClick handler sets `skipNextActiveScrollRef`
    // so this effect swallows exactly that one pending scroll.
    const skipNextActiveScrollRef = useRef(false);
    useEffect(() => {
        if (skipNextActiveScrollRef.current) {
            skipNextActiveScrollRef.current = false;
            return;
        }
        const activeRow = stepEventRows.indexOf(activeEventIndex);
        if (activeRow >= 0 && virtuosoRef.current) {
            virtuosoRef.current.scrollToIndex({
                index: activeRow,
                align: 'center',
                behavior: 'smooth',
            });
        }
    }, [activeEventIndex]);

    // ── Event click → jump to closest snapshot frame + open popup ──
    const handleEventClick = useCallback(
        (evt: activitymon.ActivityEvent) => {
            if (!bridgePort || !detail) return;

            // Snapshot-based session: jump to closest frame, then open popup.
            // The closest-by-time call is constrained to the event's bundle
            // first so a click on an app-X event doesn't surface an app-Y
            // snapshot when capture is sparse. Frame URL also goes through the
            // resolved snapshot's filePath instead of `?session&ts=` — the
            // backend's nearest-by-ts path would otherwise re-pick app Y.
            if ((detail.snapshots?.length ?? 0) > 0) {
                const closestIdx = findClosestSnapshotIndex(evt.timestamp, evt.appBundleId);
                // -1 = this event's app has no snapshot in the session. Don't open a
                // popup showing another app's frame. The row is already non-clickable
                // (hasMedia); this guards the prev/next navigation paths too.
                if (closestIdx < 0) return;
                navigateSnapshot(closestIdx);

                const closestSnap = detail.snapshots![closestIdx];
                const frameSrc = hostBridgeMediaURL(bridgePort, 'snapshot', { path: closestSnap.filePath });
                setEventPopup({
                    frameSrc,
                    evt,
                    snapshotPath: closestSnap.filePath,
                    snapshot: closestSnap,
                    siblingSnapshots: detail.snapshots,
                });
                return;
            }

            // Legacy HEVC: find matching segment
            const segments = detail.segments;
            if (!segments || segments.length === 0) return;

            const getRecorderStart = (s: typeof segments[number]): number => {
                const m = s.filePath.match(/\/(\d{13,})-/);
                return m ? parseInt(m[1], 10) : s.startTime;
            };

            let segIdx = -1;
            let bestDist = Infinity;
            segments.forEach((s, i) => {
                const recStart = getRecorderStart(s);
                if (recStart <= evt.timestamp) {
                    const dist = evt.timestamp - recStart;
                    if (dist < bestDist) {
                        bestDist = dist;
                        segIdx = i;
                    }
                }
            });
            if (segIdx === -1) return;

            const seg = segments[segIdx];
            const frameSrc = hostBridgeMediaURL(bridgePort, 'frame', { path: seg.filePath, ts: evt.timestamp });
            setEventPopup({ frameSrc, evt });
        },
        [detail, bridgePort, findClosestSnapshotIndex],
    );

    // ── Header date label + navigation (one day at a time) ──
    const dayNavLabel = formatDayNavLabel(
        selectedDate,
        t('dayNav.today') as string,
        t('dayNav.yesterday') as string,
    );
    const dayStep = 1;
    const shiftSelectedDate = (days: number) => {
        const next = new Date(selectedDate);
        next.setDate(next.getDate() + days);
        setSelectedDate(next);
    };
    const nextDayDisabled = (() => {
        const next = new Date(selectedDate);
        next.setDate(next.getDate() + dayStep);
        const today = new Date();
        today.setHours(0, 0, 0, 0);
        next.setHours(0, 0, 0, 0);
        return next.getTime() > today.getTime();
    })();

    // The header's tabs: Screen holds both its views (What you worked on and
    // the Screen timeline); arrow keys move between tabs, as on You.
    const tabIds = useId();
    const activeTab: ActivityTabId = isTimeline ? 'workedOn' : section;
    const moveTab = (from: ActivityTabId, step: 1 | -1) => {
        const index = ACTIVITY_TABS.findIndex((tab) => tab.id === from);
        const next = ACTIVITY_TABS[(index + step + ACTIVITY_TABS.length) % ACTIVITY_TABS.length].id;
        onSectionChange(next);
        document.getElementById(`${tabIds}-tab-${next}`)?.focus();
    };

    // What you worked on: Stitched by work or As recorded, kept across days.
    const [workedOnView, setWorkedOnView] = useState<WorkedOnView>('stitched');

    return (
        <div className="flex min-h-0 flex-1 flex-col">
            {/* Header: the title, its sub-line, then the tabs with the date
                beside them (computer-activity v1.0.0 rules 1-2; the same tab
                bar as You). */}
            <header className="flex flex-col gap-2 px-10 pt-9 pb-4 shrink-0">
                {backButton && (
                    <button
                        type="button"
                        onClick={backButton.onClick}
                        className="min-w-0 w-fit inline-flex items-center gap-1.5 text-sm font-semibold text-ink hover:text-accent-2 transition-colors"
                    >
                        <ChevronLeft className="h-4 w-4 shrink-0" />
                        <span className="truncate">{backButton.label}</span>
                    </button>
                )}
                <h1 className="m-0 text-[26px] font-normal leading-[1.3] text-ink">{t('title')}</h1>
                <p className="m-0 text-sm text-muted">{t('act.sub')}</p>
                <div className="mt-3 flex flex-wrap items-center gap-3">
                    <div
                        role="tablist"
                        aria-label={t('sections.label') as string}
                        className="flex w-fit max-w-full flex-wrap gap-0.5 rounded-[10px] border border-border bg-panel-warm p-[3px]"
                    >
                        {ACTIVITY_TABS.map(({ id, labelKey }) => {
                            const active = id === activeTab;
                            return (
                                <button
                                    key={id}
                                    id={`${tabIds}-tab-${id}`}
                                    type="button"
                                    role="tab"
                                    data-activity-section={id}
                                    aria-selected={active}
                                    aria-controls={`${tabIds}-panel`}
                                    tabIndex={active ? 0 : -1}
                                    onClick={() => onSectionChange(id)}
                                    onKeyDown={(event) => {
                                        if (event.key !== 'ArrowLeft' && event.key !== 'ArrowRight') return;
                                        event.preventDefault();
                                        moveTab(id, event.key === 'ArrowRight' ? 1 : -1);
                                    }}
                                    className={cn(
                                        'flex h-[30px] items-center rounded-lg px-3 text-[13px] font-medium text-ink transition-colors duration-[120ms]',
                                        'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-border-focus',
                                        active ? 'bg-surface-active' : 'hover:bg-surface-hover',
                                    )}
                                >
                                    {t(labelKey)}
                                </button>
                            );
                        })}
                    </div>
                    <div className="flex-1" />
                    {/* The date with previous and next for the tabs that show one day; Watch
                        and Your answers have none, and the Screen timeline
                        has its own time filter in its sidebar. */}
                    {!isTimeline && !isWatch && !isAnswers && (
                        <div className="flex items-center gap-0.5" data-testid="activity-day-nav">
                            <button
                                type="button"
                                onClick={() => shiftSelectedDate(-dayStep)}
                                className="p-1 rounded-md text-muted hover:text-ink hover:bg-surface-hover focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-border-focus transition-colors"
                                title={t('dayNav.previous') as string}
                                aria-label={t('dayNav.previous') as string}
                            >
                                <ChevronLeft className="h-3.5 w-3.5" />
                            </button>
                            <Popover open={datePopoverOpen} onOpenChange={setDatePopoverOpen}>
                                <PopoverTrigger asChild>
                                    <button
                                        type="button"
                                        className="flex min-w-0 items-center justify-center gap-1.5 px-2 py-1 text-[13px] font-medium text-ink rounded-md hover:bg-surface-hover focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-border-focus transition-colors"
                                        title={t('dayNav.pickDate') as string}
                                    >
                                        <span className="truncate">{dayNavLabel}</span>
                                        <ChevronDown className="h-3 w-3 text-muted/60 shrink-0" />
                                    </button>
                                </PopoverTrigger>
                                <PopoverContent className="w-auto p-0" align="end">
                                    <CalendarWidget
                                        mode="single"
                                        selected={selectedDate}
                                        onSelect={(d) => {
                                            if (d) {
                                                setSelectedDate(d);
                                                setDatePopoverOpen(false);
                                            }
                                        }}
                                        month={calendarData.displayMonth}
                                        onMonthChange={calendarData.setDisplayMonth}
                                        weekStartsOn={1}
                                        disabled={{ after: new Date() }}
                                        components={{
                                            DayButton: ({ day, modifiers, ...buttonProps }) => {
                                                const dateStr = formatDateStr(day.date);
                                                const stats = calendarData.heatmapData.get(dateStr);
                                                const hasActivity = stats
                                                    ? stats.activeMs > 0 ||
                                                      stats.knowledgeCount > 0 ||
                                                      stats.memoryCount > 0 ||
                                                      stats.automationCreatedCount > 0 ||
                                                      stats.automationRunCount > 0
                                                    : false;
                                                return (
                                                    <button {...buttonProps}>
                                                        {buttonProps.children}
                                                        {hasActivity && (
                                                            <span
                                                                aria-hidden
                                                                className={`pointer-events-none absolute bottom-1 left-1/2 -translate-x-1/2 h-[3px] w-[3px] rounded-full ${modifiers.selected ? 'bg-panel' : 'bg-accent/70'}`}
                                                            />
                                                        )}
                                                    </button>
                                                );
                                            },
                                        }}
                                    />
                                </PopoverContent>
                            </Popover>
                            <button
                                type="button"
                                onClick={() => shiftSelectedDate(dayStep)}
                                disabled={nextDayDisabled}
                                className="p-1 rounded-md text-muted hover:text-ink hover:bg-surface-hover focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-border-focus disabled:text-muted/40 disabled:hover:bg-transparent transition-colors"
                                title={t('dayNav.next') as string}
                                aria-label={t('dayNav.next') as string}
                            >
                                <ChevronRight className="h-3.5 w-3.5" />
                            </button>
                        </div>
                    )}
                    {/* Search — the existing activity search: it filters the
                        Screen timeline's session list (backend keyword +
                        semantic match) and What you worked on. */}
                    <div className="relative w-52 shrink-0">
                        <SearchIcon className="absolute left-2 top-1/2 -translate-y-1/2 h-3.5 w-3.5 text-muted" />
                        <input
                            type="text"
                            placeholder={t('searchPlaceholder') as string}
                            value={searchText}
                            onChange={(e) => setSearchText(e.target.value)}
                            className="w-full pl-7 pr-2 py-1.5 text-[12px] rounded-md bg-panel text-ink placeholder:text-muted/60 outline-none focus:ring-1 focus:ring-accent-2/40 transition-colors"
                        />
                        {searchText && (
                            <button
                                onClick={() => setSearchText('')}
                                className="absolute right-1.5 top-1/2 -translate-y-1/2 p-0.5 rounded text-muted hover:text-ink"
                            >
                                <X className="h-3 w-3" />
                            </button>
                        )}
                    </div>
                </div>
            </header>

            <div
                id={`${tabIds}-panel`}
                role="tabpanel"
                aria-labelledby={`${tabIds}-tab-${activeTab}`}
                className="flex-1 min-h-0 flex flex-row border-t border-border"
                style={{ '--wails-draggable': 'no-drag' } as React.CSSProperties}
            >
                {/* ── Left sidebar: the Screen timeline's session list. The Day
                    and Week views navigate by date via the section panel, so
                    the session-oriented filters and list would be unrelated
                    noise there. ── */}
                {isTimeline && (
                <nav className="w-[280px] shrink-0 flex flex-col border-r border-border">

                    {/* ── Filter bar ── */}
                    <div className="px-3 py-3 space-y-2 border-b border-border">
                        {/* Time + App filter row */}
                        <div className="flex items-center gap-1.5">
                            {/* Time filter dropdown (Radix DropdownMenu) */}
                            <DropdownMenu>
                                <DropdownMenuTrigger asChild>
                                    <button className="flex items-center gap-1.5 px-2 py-1 text-[11px] rounded-md bg-panel-warm text-muted hover:text-ink hover:bg-surface-hover transition-colors truncate">
                                        <CalendarIcon className="h-3 w-3 shrink-0 opacity-60" />
                                        <span className="truncate">{t(`filters.presets.${timePreset}` as never) as string}</span>
                                        <ChevronDown className="h-3 w-3 shrink-0 opacity-50" />
                                    </button>
                                </DropdownMenuTrigger>
                                <DropdownMenuContent align="start" className="min-w-[180px]">
                                    <DropdownMenuLabel>{t('filters.timeRange')}</DropdownMenuLabel>
                                    {TIME_PRESET_KEYS.filter((k) => k !== 'custom').map((key) => (
                                        <DropdownMenuItem
                                            key={key}
                                            onClick={() => setTimePreset(key)}
                                            className="gap-2 text-[12px]"
                                        >
                                            <Check className={`h-3 w-3 shrink-0 ${timePreset === key ? 'opacity-100' : 'opacity-0'}`} />
                                            {t(`filters.presets.${key}` as never) as string}
                                        </DropdownMenuItem>
                                    ))}
                                    <DropdownMenuSeparator />
                                    <DropdownMenuItem
                                        onClick={() => setTimePreset('custom')}
                                        className="gap-2 text-[12px]"
                                    >
                                        <Check className={`h-3 w-3 shrink-0 ${timePreset === 'custom' ? 'opacity-100' : 'opacity-0'}`} />
                                        {t('filters.customRange')}
                                    </DropdownMenuItem>
                                </DropdownMenuContent>
                            </DropdownMenu>

                            {/* App filter dropdown — drives the sessions
                                `selectedApps` set. */}
                            {allAppNames.length > 0 && (
                                <AppFilterDropdown
                                    trigger={
                                        <span className="truncate">
                                            {!selectedApps
                                                ? t('filters.allApps')
                                                : t('filters.appCount', { count: selectedApps.size })}
                                        </span>
                                    }
                                    open={appDropdownOpen}
                                    onOpenChange={setAppDropdownOpen}
                                >
                                    <div className="flex items-center gap-1 px-3 py-1.5 border-b border-border">
                                        <button
                                            onClick={() => setSelectedApps(null)}
                                            className="text-[10px] text-accent-2 hover:underline"
                                        >
                                            {t('filters.selectAll')}
                                        </button>
                                        <span className="text-[10px] text-muted">/</span>
                                        <button
                                            onClick={() => setSelectedApps(new Set())}
                                            className="text-[10px] text-accent-2 hover:underline"
                                        >
                                            {t('filters.selectNone')}
                                        </button>
                                    </div>
                                    <div className="max-h-[200px] overflow-y-auto">
                                        {allAppNames.map((name) => {
                                            const checked = !selectedApps || selectedApps.has(name);
                                            return (
                                                <button
                                                    key={name}
                                                    onClick={() => {
                                                        setSelectedApps((prev) => {
                                                            const current = prev ?? new Set(allAppNames);
                                                            const next = new Set(current);
                                                            if (next.has(name)) {
                                                                next.delete(name);
                                                            } else {
                                                                next.add(name);
                                                            }
                                                            // If all selected, reset to null (= "All Apps")
                                                            if (next.size === allAppNames.length) return null;
                                                            return next;
                                                        });
                                                    }}
                                                    className="flex items-center gap-2 w-full px-3 py-1.5 text-[11px] text-left text-muted hover:text-ink hover:bg-surface-hover transition-colors"
                                                >
                                                    <div className={`flex items-center justify-center h-3.5 w-3.5 rounded border shrink-0 ${
                                                        checked ? 'bg-accent-2 border-accent-2' : 'border-border'
                                                    }`}>
                                                        {checked && <Check className="h-2.5 w-2.5 text-solid-btn-fg" />}
                                                    </div>
                                                    <span className="truncate">{name}</span>
                                                </button>
                                            );
                                        })}
                                    </div>
                                </AppFilterDropdown>
                            )}
                        </div>

                        {/* Custom date range inputs (shown below filter row when custom is selected) */}
                        {timePreset === 'custom' && (
                            <div className="flex items-center gap-1.5">
                                <input
                                    type="date"
                                    value={customStart}
                                    onChange={(e) => setCustomStart(e.target.value)}
                                    className="flex-1 text-[11px] px-2 py-1 rounded-md border border-border bg-panel text-ink outline-none focus:ring-1 focus:ring-accent-2/40 transition-colors [color-scheme:dark]"
                                />
                                <span className="text-[10px] text-muted">{t('filters.to')}</span>
                                <input
                                    type="date"
                                    value={customEnd}
                                    onChange={(e) => setCustomEnd(e.target.value)}
                                    className="flex-1 text-[11px] px-2 py-1 rounded-md border border-border bg-panel text-ink outline-none focus:ring-1 focus:ring-accent-2/40 transition-colors [color-scheme:dark]"
                                />
                            </div>
                        )}
                    </div>

                    {/* Session list — Timeline only; the Events view is a
                        standalone card stream and has no per-session sidebar. */}
                    {isTimeline && (
                        // The Radix viewport inside scrolls; a capture-phase
                        // listener sees its scroll events from here.
                        <ScrollArea className="flex-1" onScrollCapture={(event) => rememberActivityScroll('timeline-sessions', (event.target as HTMLElement).scrollTop)}>
                            <SessionListScrollRestore enabled={!freshOpen} ready={sessions.length > 0} />
                            <div className="py-2 px-2">
                            {/* Pipeline-jump filter chip — only shown when the
                                sidebar is restricted to a triage group, so the
                                operator knows the list is intentionally short
                                (vs. "the date filter is too narrow"). */}
                            {restrictToSessionIds && restrictToSessionIds.length > 0 && (
                                <div className="mx-2 mb-2 px-2 py-1.5 rounded-md bg-surface-active border border-accent/30 text-[11px] text-accent flex items-center gap-1.5">
                                    <ActivityIcon className="h-3 w-3 text-accent shrink-0" />
                                    <span>{t('sessionList.triageGroupFilter', { count: restrictToSessionIds.length })}</span>
                                </div>
                            )}
                            {loading && (
                                <div className="px-3 py-8 flex flex-col items-center gap-2 text-muted">
                                    <Loader2 className="h-4 w-4 animate-spin" />
                                    <span className="text-xs">{t('sessionList.loading')}</span>
                                </div>
                            )}
                            {!loading && filteredTimelineGroups.length === 0 && (
                                <div className="px-3 py-4 text-sm text-muted text-center">
                                    {timelineGroups.length === 0 && !debouncedKeyword && timePreset === 'all'
                                        ? t('sessionList.emptyAll')
                                        : t('sessionList.emptyFiltered')}
                                </div>
                            )}
                            {Array.from(grouped.entries()).map(([dateLabel, dateGroups]) => (
                                <div key={dateLabel} className="mb-2">
                                    <div className="flex items-center gap-2 px-3 py-2">
                                        <div className="h-px flex-1 bg-border" />
                                        <span className="text-[10px] uppercase tracking-[0.18em] text-muted/70 font-medium shrink-0">
                                            {dateLabel}
                                        </span>
                                        <div className="h-px flex-1 bg-border" />
                                    </div>
                                    <div className="flex flex-col gap-0.5">
                                        {dateGroups.map((group) => {
                                            const isActive = selectedTimelineGroupId === group.id;
                                            const firstFrameSrc = group.firstFrame && bridgePort
                                                ? snapshotImageURL(bridgePort, { filePath: group.firstFrame.path })
                                                : null;
                                            const title = group.summary?.title || group.appName || (t('sessionList.unknownApp') as string);
                                            // Time first, the app once: "9:52 PM · Claude" (activity review v1.0.6 AR41).
                                            const anchors = (group.identityAnchors ?? []).filter((anchor) => anchor !== group.appName).slice(0, 2).join(' · ');
                                            const subtitleBase = group.summary?.title ? `${formatTimeShort(group.startTime)} · ${group.appName}` : formatTimeShort(group.startTime);
                                            const subtitle = anchors ? `${subtitleBase} · ${anchors}` : subtitleBase;
                                            const isSummarizing = !group.summary && group.summaryStatus === 'generating';
                                            return (
                                                <button
                                                    key={group.id}
                                                    ref={isActive ? selectedSessionRef : undefined}
                                                    onClick={() => {
                                                        setSelectedTimelineGroupId(group.id);
                                                        setSelectedId(group.primarySessionId);
                                                        onSectionChange('timeline');
                                                    }}
                                                    className={`grid ${firstFrameSrc ? 'grid-cols-[3.5rem_minmax(0,1fr)]' : 'grid-cols-[minmax(0,1fr)]'} items-start gap-x-2 gap-y-0.5 px-3 py-2 rounded-lg text-left transition-colors ${
                                                        isActive
                                                            ? 'bg-active-row-bg text-ink'
                                                            : 'text-muted hover:text-ink hover:bg-surface-hover'
                                                    }`}
                                                >
                                                    {firstFrameSrc && (
                                                        <img
                                                            src={firstFrameSrc}
                                                            alt=""
                                                            aria-hidden
                                                            loading="lazy"
                                                            draggable={false}
                                                            data-testid="timeline-first-frame"
                                                            className="row-span-2 h-9 w-14 rounded-md border border-border object-cover"
                                                        />
                                                    )}
                                                    <span className="min-w-0 flex items-center gap-1.5 text-[13px] font-medium">
                                                        <span className="min-w-0 truncate" title={title}>{title}</span>
                                                        {isSummarizing && (
                                                            <Loader2
                                                                className="h-3 w-3 shrink-0 animate-spin opacity-60"
                                                                aria-label={t('sessionList.summarizing') as string}
                                                            />
                                                        )}
                                                    </span>
                                                    <div className={cn('min-w-0 grid grid-cols-[minmax(0,1fr)_4.25rem] items-center gap-2 text-[11px] opacity-70', firstFrameSrc && 'col-start-2')}>
                                                        <span className="min-w-0 truncate tabular-nums">
                                                            {subtitle}
                                                        </span>
                                                        <span className="justify-self-end whitespace-nowrap tabular-nums text-right">
                                                            {formatTimelineGroupDuration(group.activeMs)}
                                                        </span>
                                                    </div>
                                                    {group.sessionIds?.some((id) => semanticMatches.has(id)) && (
                                                        <span
                                                            className={cn('flex items-center gap-0.5 text-[9px] text-muted/80', firstFrameSrc ? 'col-start-2' : 'justify-self-start')}
                                                            title={t('sessionList.semanticMatchTitle', {
                                                                text: (semanticMatches.get(group.sessionIds.find((id) => semanticMatches.has(id)) ?? '')?.matchText ?? '').slice(0, 80),
                                                            }) as string}
                                                        >
                                                            <BookOpen className="h-2.5 w-2.5" />
                                                        </span>
                                                    )}
                                                </button>
                                            );
                                        })}
                                    </div>
                                </div>
                            ))}
                        </div>
                    </ScrollArea>
                    )}

                    {/* ── Privacy footer ── */}
                    <div className="shrink-0 border-t border-border px-4 py-3 flex items-start gap-2.5 text-[10.5px] leading-snug text-muted">
                        <RedactionShieldIcon size={14} className="mt-[1px]" />
                        <p className="min-w-0">
                            <span className="text-ink/85 font-medium">{t('privacyNotice.title')}</span>
                        </p>
                    </div>
                </nav>
                )}

                {/* ── Right content area ── */}
                <div className="flex-1 min-w-0 flex flex-col">
                    <ActivityOpenChatContext.Provider value={onOpenChat}>
                    {isWatch ? (
                        <ActivityWatchRecordings
                            bridgePort={bridgePort}
                            onOpenSession={openSessionInTimeline}
                            openConversationId={openConversationId}
                        />
                    ) : isAnswers ? (
                        <ActivityAnswers />
                    ) : isAgents ? (
                        <div className="flex-1 overflow-y-auto">
                            <div className="mx-auto max-w-2xl px-6 py-6">
                                <ActivityFeed date={selectedDate} kinds={['handoff']} emptyText={t('feed.emptyAgents')} />
                            </div>
                        </div>
                    ) : !isTimeline ? (
                        <ActivityDayView
                            date={selectedDate}
                            initialThreadId={initialThreadId}
                            onInitialThreadConsumed={onInitialThreadConsumed}
                            searchQuery={debouncedKeyword}
                            bridgePort={bridgePort}
                            dailyData={calendarData.dailyData}
                            automationRuns={calendarData.automationRuns}
                            onStartChat={onStartChat}
                            onOpenRunDetail={setDetailJobId}
                            section={daySection}
                            onSectionChange={onSectionChange}
                            workedOnView={workedOnView}
                            onWorkedOnViewChange={setWorkedOnView}
                            onOpenTimeline={() => { setSelectedApps(null); onSectionChange('timeline'); }}
                            onOpenConversation={openConversation}
                            onOpenTopic={onOpenTopic}
                            onOpenRecording={openSessionInTimeline}
                        />
                    ) : null}
                    </ActivityOpenChatContext.Provider>
                    {!isTimeline ? null : (
                    <>
                    {sessionsFailed && (
                        <ActivityInlineNotice notice={{ tone: 'error', text: t('timeline.sessionsFailed'), onAction: () => refreshSessionsRef.current() }} className="px-8 pt-4" />
                    )}
                    {/* Session summary header */}
                    <div className="px-8 pt-7 pb-4 shrink-0">
                        <div className="flex items-start justify-between">
                            <div className="flex-1 min-w-0">
                                {detail ? (
                                    <div className="space-y-3">
                                        <div className="flex items-center gap-3">
                                            <h1
                                                className="text-[20px] font-semibold tracking-tight text-ink truncate min-w-0"
                                                title={selectedTimelineSummary?.title || undefined}
                                            >
                                                {selectedTimelineSummary?.title || formatTimeRangeFromMs(detail.startTime, detail.endTime)}
                                            </h1>
                                            {triggerLabel(detail.triggerKind, t as (key: string) => string) && (
                                                <span className="text-[10px] bg-panel-warm px-1.5 py-0.5 rounded font-medium text-muted">
                                                    {triggerLabel(detail.triggerKind, t as (key: string) => string)}
                                                </span>
                                            )}
                                        </div>
                                        {/* Summary stats row */}
                                        <div className="flex flex-wrap items-center gap-x-4 gap-y-1 text-[11px] text-muted">
                                            <span className="min-w-0 truncate" data-testid="session-meta" title={detail.appNames?.join(', ') || undefined}>
                                                {[
                                                    selectedTimelineSummary?.title ? formatTimeRangeFromMs(detail.startTime, detail.endTime) : '',
                                                    formatSessionDuration(visibleDurationMs, (count) => t('session.minutes', { count })),
                                                    detail.appNames?.join(', ') ?? '',
                                                ].filter(Boolean).join(' · ')}
                                            </span>
                                        </div>
                                        {/* People Isoform identified in this session. Click a chip
                                            to highlight every message that person sent in the
                                            parsed-content panels below. Click again to clear.
                                            Hidden when the session never resolved a sender (most
                                            non-chat sessions). */}
                                        {sessionPrincipals.length > 0 && (
                                            <SessionPeopleChips
                                                principals={sessionPrincipals}
                                                selectedName={locallySelectedPersonName ?? highlightedSenderName ?? null}
                                                onSelect={(name) => {
                                                    const wasSelected =
                                                        locallySelectedPersonName === name;
                                                    setLocallySelectedPersonName((prev) =>
                                                        prev === name ? null : name,
                                                    );
                                                    // Only navigate when SELECTING a person (not
                                                    // deselecting); jumping to a frame on un-select
                                                    // would feel like the chip "moved" the user.
                                                    if (
                                                        !wasSelected &&
                                                        detail?.snapshots?.length
                                                    ) {
                                                        const principal = sessionPrincipals.find(
                                                            (p) => p.displayName === name,
                                                        );
                                                        // Prefer firstSeenAt — landing on the moment
                                                        // they first appeared in the session reads
                                                        // more naturally than the last moment.
                                                        const anchor =
                                                            principal?.firstSeenAt ??
                                                            principal?.lastSeenAt ??
                                                            0;
                                                        if (anchor > 0) {
                                                            let bestIdx = 0;
                                                            let bestDist = Infinity;
                                                            for (
                                                                let i = 0;
                                                                i < detail.snapshots.length;
                                                                i++
                                                            ) {
                                                                const dist = Math.abs(
                                                                    detail.snapshots[i].timestamp -
                                                                        anchor,
                                                                );
                                                                if (dist < bestDist) {
                                                                    bestDist = dist;
                                                                    bestIdx = i;
                                                                }
                                                            }
                                                            navigateSnapshot(bestIdx);
                                                        }
                                                    }
                                                }}
                                            />
                                        )}
                                        {/* Voice diarization (R20): chip per distinct
                                            voice in this session. "You" + "Speaker 1/2/...".
                                            Computed by clustering the cached embedding
                                            on each mic-source speech event. Hidden when
                                            no mic speech / pre-R20 session. */}
                                        {speakerClusters.length > 0 && (
                                            <SessionSpeakerChips
                                                clusters={speakerClusters}
                                                selectedLabel={selectedSpeakerLabel}
                                                onSelect={(label) => {
                                                    setSelectedSpeakerLabel((prev) =>
                                                        prev === label ? null : label,
                                                    );
                                                }}
                                            />
                                        )}
                                    </div>
                                ) : (
                                    <h1 className="text-lg font-semibold text-ink">{t('title')}</h1>
                                )}
                            </div>
                        </div>
                    </div>

                    {pipelineTotalFrames > 0 && (
                        <div className="mx-8 my-3 flex items-start gap-3 px-3 py-2 rounded-md bg-surface-active border border-accent/30 text-[12px] text-accent">
                            <ActivityIcon className="h-4 w-4 text-accent mt-0.5 shrink-0" />
                            <div className="flex-1 min-w-0">
                                <div className="font-medium">
                                    {highlightedPipelineRunId != null
                                        ? t('session.triageBanner', { id: highlightedPipelineRunId, total: pipelineTotalFrames })
                                        : t('session.triageBannerNoRun', { total: pipelineTotalFrames })}
                                </div>
                                <div className="flex flex-wrap items-center gap-x-3 gap-y-1 mt-0.5 text-accent/80">
                                    <span>{t('session.triageBannerHere', { count: pipelineCurrentSessionCount })}</span>
                                    {pipelineOtherSessions.length > 0 && (
                                        <span>
                                            {t('session.triageBannerOther', {
                                                count: pipelineOtherSessions.reduce((sum, s) => sum + s.count, 0),
                                                sessionCount: pipelineOtherSessions.length,
                                            })}
                                        </span>
                                    )}
                                    {pipelineTotalSiblings > 0 && (
                                        <span className="text-muted">
                                            {t('session.triageBannerSiblings', { count: pipelineTotalSiblings })}
                                        </span>
                                    )}
                                    {pipelineTotalSkipped > 0 && (
                                        <span className="text-error">
                                            {t('session.triageBannerSkipped', { count: pipelineTotalSkipped })}
                                        </span>
                                    )}
                                </div>
                                {pipelineOtherSessions.length > 0 && (
                                    <div className="flex flex-wrap items-center gap-1.5 mt-1.5">
                                        {pipelineOtherSessions.map((s) => (
                                            <button
                                                key={s.sessionId}
                                                type="button"
                                                onClick={() => {
                                                    setSelectedTimelineGroupId(null);
                                                    setSelectedId(s.sessionId);
                                                }}
                                                className="px-2 py-0.5 rounded bg-surface-active hover:bg-surface-active text-accent text-[11px] font-medium"
                                            >
                                                {t('session.triageBannerSwitchTo', { label: s.label, count: s.count })}
                                            </button>
                                        ))}
                                    </div>
                                )}
                                {/* Collapsible legend — explains every tick color so
                                    operators don't have to remember the encoding.
                                    Using <details>/<summary> for native a11y
                                    (keyboard toggle, screen-reader semantics) and
                                    zero state management. The legend is INSIDE the
                                    banner because the banner is what brought the
                                    operator here from the pipeline run page —
                                    placing it elsewhere would force a hunt. */}
                                <details className="mt-2 group/legend">
                                    <summary className="cursor-pointer select-none text-[11px] text-accent/90 hover:text-accent inline-flex items-center gap-1">
                                        <span className="inline-block transition-transform group-open/legend:rotate-90">▸</span>
                                        {t('session.legend.toggle')}
                                    </summary>
                                    <div className="mt-2 space-y-2 text-[11px] text-ink/85 bg-panel border border-accent/20 rounded p-2">
                                        <div className="flex items-start gap-2">
                                            <span className="mt-1 inline-block w-2 h-3 rounded-sm bg-accent ring-1 ring-border/90 shadow-sm shrink-0" />
                                            <div>
                                                <span className="font-medium">{t('session.legend.parsedUsedLabel')}</span>
                                                <span className="text-ink/70"> — {t('session.legend.parsedUsedDesc')}</span>
                                            </div>
                                        </div>
                                        <div className="flex items-start gap-2">
                                            <span className="mt-1 inline-block w-2 h-3 rounded-sm bg-info ring-1 ring-border/90 shadow-sm shrink-0" />
                                            <div>
                                                <span className="font-medium">{t('session.legend.ocrUsedLabel')}</span>
                                                <span className="text-ink/70"> — {t('session.legend.ocrUsedDesc')}</span>
                                            </div>
                                        </div>
                                        <div className="flex items-start gap-2">
                                            <span className="mt-1 inline-block w-1 h-3 rounded-sm bg-surface-hover/70 shrink-0" />
                                            <div>
                                                <span className="font-medium">{t('session.legend.siblingLabel')}</span>
                                                <span className="text-ink/70"> — {t('session.legend.siblingDesc')}</span>
                                            </div>
                                        </div>
                                        <div className="flex items-start gap-2">
                                            <span className="mt-1 inline-block w-1.5 h-3 rounded-sm bg-transparent ring-1 ring-error/70 shrink-0" />
                                            <div>
                                                <span className="font-medium">{t('session.legend.skippedLabel')}</span>
                                                <span className="text-ink/70"> — {t('session.legend.skippedDesc')}</span>
                                            </div>
                                        </div>
                                        <div className="mt-2 pt-2 border-t border-ink/10">
                                            <div className="font-medium text-ink/70 mb-1">{t('session.legend.skipReasonsTitle')}</div>
                                            <ul className="space-y-0.5 text-ink/65">
                                                <li>• {t('session.skipReason.parsed_oversize')}</li>
                                                <li>• {t('session.skipReason.ocr_covered_by_parsed')}</li>
                                                <li>• {t('session.skipReason.ocr_prefix_dedup')}</li>
                                                <li>• {t('session.skipReason.ocr_over_cap')}</li>
                                            </ul>
                                        </div>
                                        <div className="mt-2 pt-2 border-t border-ink/10 text-ink/60">
                                            {t('session.legend.howToInspect')}
                                        </div>
                                        <div className="mt-1 text-ink/55 italic">
                                            {t('session.legend.priorityNote')}
                                        </div>
                                    </div>
                                </details>
                            </div>
                        </div>
                    )}

                    <ScrollArea ref={scrollAreaRef} className="flex-1">
                        <div
                            key={selectedTimelineGroup?.id ?? selectedId ?? 'empty'}
                            className="px-8 pb-10 space-y-8 animate-[fade-in_200ms_ease-out] flex flex-col"
                            style={scrollAreaHeight > 0 ? { minHeight: scrollAreaHeight } : undefined}
                        >
                            {!detail && !loading && (
                                <p className="text-sm text-muted">
                                    {detailError ?? (t('session.selectPrompt') as string)}
                                </p>
                            )}

                            {detail && (
                                <>
                                    {/* ── Meeting notes section ── */}
                                    <ActivityMeetingNotes sessionIds={meetingNotesSessionIds} bridgePort={bridgePort} />

                                    {/* ── Recording section: none when no screenshot was kept ── */}
                                    {(isSnapshotSession || segmentCount > 0) && (
                                    <div className="space-y-3 shrink-0">
                                        <h2 className="text-[11px] font-semibold uppercase tracking-[0.08em] text-muted">
                                            {t('session.recording')}
                                        </h2>
                                        {isSnapshotSession ? (
                                            /* ── Snapshot slideshow ── */
                                            !bridgePort ? (
                                                <div className="rounded-lg border border-border bg-panel p-6 flex items-center justify-center gap-2 text-sm text-muted">
                                                    <AlertTriangle className="h-4 w-4" />
                                                    {t('session.hostBridgeUnavailable')}
                                                </div>
                                            ) : (
                                                <div className="space-y-2">
                                                    {/* Fullscreen overlay is portaled to document.body so it
                                                        escapes the Radix DialogContent's transform-induced
                                                        containing block — without the portal, position:fixed
                                                        inside DialogContent positions relative to the dialog
                                                        instead of the viewport, so the "fullscreen" view
                                                        only filled the dialog area. */}
                                                    {/* Fullscreen view is its own Radix Dialog so it
                                                        registers as a nested DismissableLayer.
                                                        Earlier attempts (commits 6dada9e1e, 74cf34763,
                                                        106b511d4) tried various event-blocking on a
                                                        document.body-portaled div, but couldn't beat
                                                        the parent Dialog's document-level capture-
                                                        phase listeners — clicks on the Minimize
                                                        button inside the portal always dismissed the
                                                        outer Activity dialog. A nested Dialog gives
                                                        Radix's layer stack the explicit signal it
                                                        needs: events inside this layer are "inside",
                                                        not "outside the parent". */}
                                                    {snapshotSrc && (
                                                        <DialogPrimitive.Root open={isFullscreen} onOpenChange={setIsFullscreen}>
                                                            <DialogPrimitive.Portal>
                                                                <DialogPrimitive.Overlay className="fixed inset-0 z-[100] bg-code-bg" />
                                                                <DialogPrimitive.Content
                                                                    className="fixed inset-0 z-[100] flex items-center justify-center bg-code-bg outline-none overflow-hidden"
                                                                    onClick={() => setIsFullscreen(false)}
                                                                    onWheel={handleFsWheel}
                                                                    onDoubleClick={handleFsDoubleClick}
                                                                    style={{ touchAction: 'none' }}
                                                                    aria-describedby={undefined}
                                                                >
                                                                    <DialogPrimitive.Title className="sr-only">
                                                                        {t('session.fullscreen')}
                                                                    </DialogPrimitive.Title>
                                                                    {/* Inner relative wrapper carries the
                                                                        img + every overlay and the
                                                                        zoom/pan transform. SVG viewBox
                                                                        math lines up overlays with the
                                                                        letterboxed img content. */}
                                                                    <div
                                                                        ref={fsTransformedRef}
                                                                        className="relative w-full h-full flex items-center justify-center"
                                                                        onClick={(e) => e.stopPropagation()}
                                                                        onPointerDown={handleFsPointerDown}
                                                                        onPointerMove={handleFsPointerMove}
                                                                        onPointerUp={handleFsPointerUp}
                                                                        onPointerCancel={handleFsPointerUp}
                                                                        style={{
                                                                            transformOrigin: 'center center',
                                                                            willChange: 'transform',
                                                                        }}
                                                                    >
                                                                        <img
                                                                            src={snapshotSrc}
                                                                            alt={t('session.snapshotAlt', { index: snapshotIndex + 1 }) as string}
                                                                            className="max-w-full max-h-full object-contain block"
                                                                            draggable={false}
                                                                            onError={handleSnapshotError}
                                                                        />
                                                                        {hasFocusRect && currentSnapshot &&
                                                                            currentSnapshot.width > 0 && currentSnapshot.height > 0 && currentFocusRect && (
                                                                            <svg
                                                                                className="absolute inset-0 w-full h-full pointer-events-none"
                                                                                viewBox={`0 0 ${currentSnapshot.width} ${currentSnapshot.height}`}
                                                                                preserveAspectRatio="xMidYMid meet"
                                                                                aria-hidden="true"
                                                                            >
                                                                                <rect
                                                                                    x={(currentFocusRect.focusRectX ?? 0) * currentSnapshot.width}
                                                                                    y={(currentFocusRect.focusRectY ?? 0) * currentSnapshot.height}
                                                                                    width={(currentFocusRect.focusRectW ?? 0) * currentSnapshot.width}
                                                                                    height={(currentFocusRect.focusRectH ?? 0) * currentSnapshot.height}
                                                                                    fill="none"
                                                                                    stroke="var(--color-accent)"
                                                                                    strokeWidth={1.5}
                                                                                    strokeDasharray="6 4"
                                                                                    vectorEffect="non-scaling-stroke"
                                                                                />
                                                                            </svg>
                                                                        )}
                                                                        {currentSnapshot &&
                                                                            currentSnapshot.width > 0 &&
                                                                            currentSnapshot.height > 0 &&
                                                                            personHighlightBoxes.length > 0 && (
                                                                            <svg
                                                                                className="absolute inset-0 w-full h-full pointer-events-none"
                                                                                viewBox={`0 0 ${currentSnapshot.width} ${currentSnapshot.height}`}
                                                                                preserveAspectRatio="xMidYMid meet"
                                                                                aria-hidden="true"
                                                                            >
                                                                                {personHighlightBoxes.map((box, i) => (
                                                                                    <rect
                                                                                        key={i}
                                                                                        x={box.x * currentSnapshot.width}
                                                                                        y={box.y * currentSnapshot.height}
                                                                                        width={box.width * currentSnapshot.width}
                                                                                        height={box.height * currentSnapshot.height}
                                                                                        fill="var(--color-accent)"
                                                                                        fillOpacity={0.18}
                                                                                        stroke="var(--color-accent)"
                                                                                        strokeWidth={2}
                                                                                        vectorEffect="non-scaling-stroke"
                                                                                        rx={2}
                                                                                    />
                                                                                ))}
                                                                            </svg>
                                                                        )}
                                                                    </div>
                                                                    <DialogPrimitive.Close asChild>
                                                                        <button
                                                                            className="absolute top-4 right-4 p-2 rounded-md bg-code-bg/60 text-code-fg/80 hover:text-code-fg hover:bg-code-bg/80 transition-colors"
                                                                            title={t('session.exitFullscreen') as string}
                                                                        >
                                                                            <Minimize2 className="h-5 w-5" />
                                                                        </button>
                                                                    </DialogPrimitive.Close>
                                                                </DialogPrimitive.Content>
                                                            </DialogPrimitive.Portal>
                                                        </DialogPrimitive.Root>
                                                    )}
                                                    <div
                                                        ref={videoContainerRef}
                                                        className="group/video relative bg-code-bg overflow-hidden rounded-lg border border-border"
                                                    >
                                                        {snapshotSrc ? (
                                                            <img
                                                                src={snapshotSrc}
                                                                alt={t('session.snapshotAlt', { index: snapshotIndex + 1 }) as string}
                                                                className="w-full max-h-[400px] object-contain"
                                                                draggable={false}
                                                                onError={handleSnapshotError}
                                                            />
                                                        ) : (
                                                            <div className="w-full h-[200px] flex items-center justify-center text-sm text-code-fg/50">
                                                                {t('session.noSnapshot')}
                                                            </div>
                                                        )}
                                                        {/* OCR scope overlay: dashed rect over the focused-window region
                                                            the OCR engine actually scanned. Uses the same
                                                            preserveAspectRatio as the <img>'s object-contain so the
                                                            rect aligns with the image content even with letterboxing.
                                                            Hidden when the rect is unknown (legacy snapshot,
                                                            multi-display fallback, non-darwin) or the snapshot has
                                                            no recorded pixel dimensions. */}
                                                        {snapshotSrc && hasFocusRect && currentSnapshot &&
                                                            currentSnapshot.width > 0 && currentSnapshot.height > 0 && currentFocusRect && (() => {
                                                            const w = currentSnapshot.width;
                                                            const h = currentSnapshot.height;
                                                            return (
                                                                <svg
                                                                    className="absolute inset-0 w-full h-full pointer-events-none"
                                                                    viewBox={`0 0 ${w} ${h}`}
                                                                    preserveAspectRatio="xMidYMid meet"
                                                                    aria-hidden="true"
                                                                >
                                                                    <rect
                                                                        x={(currentFocusRect.focusRectX ?? 0) * w}
                                                                        y={(currentFocusRect.focusRectY ?? 0) * h}
                                                                        width={(currentFocusRect.focusRectW ?? 0) * w}
                                                                        height={(currentFocusRect.focusRectH ?? 0) * h}
                                                                        fill="none"
                                                                        stroke="var(--color-accent)"
                                                                        strokeWidth={1.5}
                                                                        strokeDasharray="6 4"
                                                                        vectorEffect="non-scaling-stroke"
                                                                    />
                                                                </svg>
                                                            );
                                                        })()}
                                                        {/* Person-name OCR highlights: drawn whenever
                                                            the user has selected a person (via
                                                            People chip OR People→Activity drill)
                                                            AND the OCR worker found their name in
                                                            this snapshot. Same viewBox math as the
                                                            focus-rect SVG above so coordinates line
                                                            up with the image content under
                                                            object-contain letterboxing. Solid
                                                            terracotta border + soft fill so it
                                                            stands out from the dashed orange
                                                            focus-rect. */}
                                                        {snapshotSrc && currentSnapshot &&
                                                            currentSnapshot.width > 0 &&
                                                            currentSnapshot.height > 0 &&
                                                            personHighlightBoxes.length > 0 && (() => {
                                                            const w = currentSnapshot.width;
                                                            const h = currentSnapshot.height;
                                                            return (
                                                                <svg
                                                                    className="absolute inset-0 w-full h-full pointer-events-none"
                                                                    viewBox={`0 0 ${w} ${h}`}
                                                                    preserveAspectRatio="xMidYMid meet"
                                                                    aria-hidden="true"
                                                                >
                                                                    {personHighlightBoxes.map((box, i) => (
                                                                        <rect
                                                                            key={i}
                                                                            x={box.x * w}
                                                                            y={box.y * h}
                                                                            width={box.width * w}
                                                                            height={box.height * h}
                                                                            fill="var(--color-accent)"
                                                                            fillOpacity={0.18}
                                                                            stroke="var(--color-accent)"
                                                                            strokeWidth={2}
                                                                            vectorEffect="non-scaling-stroke"
                                                                            rx={2}
                                                                        />
                                                                    ))}
                                                                </svg>
                                                            );
                                                        })()}
                                                        {/* Legend pill so first-time viewers know what the dashed
                                                            orange box represents. Static corner placement instead of
                                                            attaching to the rect itself — the rect can sit near any
                                                            edge of the snapshot, and a floating label would clip. */}
                                                        {snapshotSrc && hasFocusRect && (
                                                            <div
                                                                className="absolute bottom-2 right-2 flex items-center gap-1.5 px-2 py-0.5 rounded-md bg-code-bg/60 text-code-fg/90 text-[11px] font-medium pointer-events-none"
                                                                title={t('session.focusRectHint') as string}
                                                            >
                                                                <span
                                                                    aria-hidden="true"
                                                                    className="inline-block w-3 h-2 border border-dashed"
                                                                    style={{ borderColor: 'var(--color-accent)' }}
                                                                />
                                                                {t('session.focusRectLegend')}
                                                            </div>
                                                        )}
                                                        {/* Play/Pause overlay center button */}
                                                        {snapshots.length > 1 && !isPlaying && (
                                                            <button
                                                                onClick={togglePlay}
                                                                className="absolute inset-0 flex items-center justify-center opacity-0 group-hover/video:opacity-100 transition-opacity"
                                                            >
                                                                <div className="p-3 rounded-full bg-code-bg/60 text-code-fg/90 hover:bg-code-bg/80 hover:text-code-fg transition-colors">
                                                                    <Play className="h-8 w-8" />
                                                                </div>
                                                            </button>
                                                        )}
                                                        {/* Prev/Next overlay arrows */}
                                                        {snapshots.length > 1 && !isPlaying && (
                                                            <>
                                                                <button
                                                                    onClick={handlePrevSnapshot}
                                                                    disabled={snapshotIndex === 0}
                                                                    className="absolute left-2 top-1/2 -translate-y-1/2 p-1.5 rounded-md bg-code-bg/60 text-code-fg/80 hover:text-code-fg hover:bg-code-bg/80 opacity-0 group-hover/video:opacity-100 disabled:opacity-0 transition-opacity"
                                                                >
                                                                    <ChevronLeft className="h-5 w-5" />
                                                                </button>
                                                                <button
                                                                    onClick={handleNextSnapshot}
                                                                    disabled={snapshotIndex === snapshots.length - 1}
                                                                    className="absolute right-2 top-1/2 -translate-y-1/2 p-1.5 rounded-md bg-code-bg/60 text-code-fg/80 hover:text-code-fg hover:bg-code-bg/80 opacity-0 group-hover/video:opacity-100 disabled:opacity-0 transition-opacity"
                                                                >
                                                                    <ChevronRight className="h-5 w-5" />
                                                                </button>
                                                            </>
                                                        )}
                                                        {/* Triage-used badge: signals to the operator that this
                                                            exact snapshot was sampled into the prompt for a specific
                                                            Activity Pipeline run. Top-left so it doesn't collide with
                                                            the fullscreen button (top-right) or play overlay (center). */}
                                                        {currentSnapshotIsHighlighted && (
                                                            <div className="absolute top-2 left-2 px-2 py-1 rounded-md bg-solid-btn-bg text-solid-btn-fg text-[10px] font-medium pointer-events-none">
                                                                {highlightedPipelineRunId != null
                                                                    ? t('session.usedInTriageRun', { id: highlightedPipelineRunId })
                                                                    : t('session.usedInTriage')}
                                                            </div>
                                                        )}
                                                        {/* Sibling-of-triage badge: this frame's content was folded
                                                            into a winner via dedup. An opaque neutral surface keeps
                                                            its explanation readable over any screenshot. */}
                                                        {!currentSnapshotIsHighlighted && currentSnapshotIsSibling && (
                                                            <div className="absolute top-2 left-2 px-2 py-1 rounded-md bg-panel text-ink text-[10px] font-medium pointer-events-none">
                                                                {t('session.siblingOfTriage')}
                                                            </div>
                                                        )}
                                                        {/* Skip-reason badge: this frame was actively dropped by
                                                            one of the prompt-assembly gates (size cap / coverage /
                                                            dedup / over-cap). An opaque neutral badge explains the
                                                            skip over any screenshot without implying failure.
                                                            Sibling has priority
                                                            on the unusual case where a snapshot appears in both
                                                            classifications — sibling already explains the drop. */}
                                                        {!currentSnapshotIsHighlighted && !currentSnapshotIsSibling && currentSnapshotSkipInfo !== null && (
                                                            <div className="absolute top-2 left-2 px-2 py-1 rounded-md bg-panel text-ink text-[10px] font-medium pointer-events-none max-w-[80%]">
                                                                <div>
                                                                    {isKnownSkipReason(currentSnapshotSkipInfo.reason)
                                                                        ? t(SKIP_REASON_I18N_KEY[currentSnapshotSkipInfo.reason])
                                                                        : t('session.skipReason.unknown')}
                                                                </div>
                                                                {/* P1: chain length adds saturation context — a drop
                                                                    with chain=20 reads very differently from chain=1
                                                                    (intentional dedup vs a near-miss). */}
                                                                {currentSnapshotSkipInfo.reason === 'ocr_prefix_dedup' && currentSnapshotSkipInfo.dedupChainLen !== undefined && currentSnapshotSkipInfo.dedupChainLen > 0 && (
                                                                    <div className="text-[9px] opacity-90 mt-0.5">
                                                                        {t('session.quality.dedupChain', { count: currentSnapshotSkipInfo.dedupChainLen })}
                                                                    </div>
                                                                )}
                                                            </div>
                                                        )}
                                                        {/* Fullscreen toggle overlay button — enters the portal
                                                            view above. The in-flow container itself never goes
                                                            "fullscreen"; this just sets isFullscreen which mounts
                                                            the portal. */}
                                                        {snapshotSrc && (
                                                            <button
                                                                onClick={() => setIsFullscreen(true)}
                                                                className="absolute top-2 right-2 p-1.5 rounded-md bg-code-bg/60 text-code-fg/80 hover:text-code-fg hover:bg-code-bg/80 transition-opacity opacity-0 group-hover/video:opacity-100"
                                                                title={t('session.fullscreen') as string}
                                                            >
                                                                <Maximize2 className="h-4 w-4" />
                                                            </button>
                                                        )}
                                                    </div>
                                                    {/* Timeline scrubber with event markers */}
                                                    {snapshots.length > 1 && (
                                                        <div
                                                            className="relative h-6 cursor-pointer group/timeline"
                                                            onClick={(e) => {
                                                                const rect = e.currentTarget.getBoundingClientRect();
                                                                const pct = Math.max(0, Math.min(1, (e.clientX - rect.left) / rect.width));
                                                                navigateSnapshot(Math.round(pct * (snapshots.length - 1)));
                                                            }}
                                                        >
                                                            {/* Track background */}
                                                            <div className="absolute left-0 right-0 top-1/2 -translate-y-1/2 h-1.5 bg-border rounded-full">
                                                                <div
                                                                    className="h-full bg-accent-2 rounded-full transition-[width] duration-100"
                                                                    style={{ width: `${((snapshotIndex) / (snapshots.length - 1)) * 100}%` }}
                                                                />
                                                            </div>
                                                            {/* Dedup sibling markers — render FIRST so the Isoform
                                                                "really used" ticks layer above any sibling that
                                                                shares a position (rare, but possible on a
                                                                clustered timeline). A neutral narrow rectangle,
                                                                shorter than used frames, with lower z, reads as a
                                                                quiet "this snapshot was considered & folded into
                                                                a winner" hint rather than a primary signal. */}
                                                            {siblingSnapshotIndices.map((idx) => {
                                                                const pct = snapshots.length > 1 ? (idx / (snapshots.length - 1)) * 100 : 0;
                                                                return (
                                                                    <button
                                                                        key={`sibling-${idx}`}
                                                                        type="button"
                                                                        onClick={(e) => {
                                                                            e.stopPropagation();
                                                                            navigateSnapshot(idx);
                                                                        }}
                                                                        className="absolute top-1/2 -translate-y-1/2 -translate-x-1/2 w-1 h-3 rounded-sm bg-muted hover:bg-ink z-10"
                                                                        style={{ left: `${pct}%` }}
                                                                        title={t('session.siblingTickTooltip') as string}
                                                                    />
                                                                );
                                                            })}
                                                            {/* Skipped-by-pipeline markers — hollow neutral outline, same
                                                                shape as sibling. Distinguishes "pipeline saw this and
                                                                actively dropped it" from "folded into a winner"
                                                                (sibling, solid neutral ink) and "never considered" (no
                                                                tick). Hover/title carries the translated skip reason
                                                                so operators can see why without opening the popup. */}
                                                            {skippedSnapshotIndices.map((idx) => {
                                                                const pct = snapshots.length > 1 ? (idx / (snapshots.length - 1)) * 100 : 0;
                                                                const info = skippedSnapshotInfoForSession.get(snapshots[idx].id);
                                                                const reason = info?.reason ?? '';
                                                                const reasonLabel = isKnownSkipReason(reason)
                                                                    ? t(SKIP_REASON_I18N_KEY[reason])
                                                                    : t('session.skipReason.unknown');
                                                                return (
                                                                    <button
                                                                        key={`skipped-${idx}`}
                                                                        type="button"
                                                                        onClick={(e) => {
                                                                            e.stopPropagation();
                                                                            navigateSnapshot(idx);
                                                                        }}
                                                                        className="absolute top-1/2 -translate-y-1/2 -translate-x-1/2 w-1.5 h-3 rounded-sm bg-transparent ring-1 ring-muted hover:ring-ink z-10"
                                                                        style={{ left: `${pct}%` }}
                                                                        title={t('session.skippedTickTooltip', { reason: reasonLabel })}
                                                                    />
                                                                );
                                                            })}
                                                            {/* Triage-used frame markers use Isoform's accent.
                                                                OCR renders first so parsed sits on top when a
                                                                snapshot carries both kinds; the offset below keeps
                                                                the two sources visible. */}
                                                            {ocrSnapshotIndices.map((idx, position) => {
                                                                const pct = snapshots.length > 1 ? (idx / (snapshots.length - 1)) * 100 : 0;
                                                                const isShared = parsedSnapshotIdsForSession.has(snapshots[idx].id);
                                                                return (
                                                                    <button
                                                                        key={`used-ocr-${idx}`}
                                                                        type="button"
                                                                        onClick={(e) => {
                                                                            e.stopPropagation();
                                                                            navigateSnapshot(idx);
                                                                        }}
                                                                        className={`absolute inset-y-0 -translate-x-1/2 ${isShared ? 'w-1.5' : 'w-2'} rounded-sm bg-info ring-1 ring-border/90 shadow-sm hover:bg-info z-20`}
                                                                        style={{ left: `${pct}%` }}
                                                                        title={t('session.usedInTriageTickOcr', { index: position + 1, total: ocrSnapshotIndices.length }) as string}
                                                                    />
                                                                );
                                                            })}
                                                            {parsedSnapshotIndices.map((idx, position) => {
                                                                const pct = snapshots.length > 1 ? (idx / (snapshots.length - 1)) * 100 : 0;
                                                                const isShared = ocrSnapshotIdsForSession.has(snapshots[idx].id);
                                                                // When a snapshot carries both parsed AND ocr, render
                                                                // the parsed half offset right of center so both halves
                                                                // are visible side-by-side. Otherwise center it.
                                                                const offsetClass = isShared ? 'translate-x-0' : '-translate-x-1/2';
                                                                return (
                                                                    <button
                                                                        key={`used-parsed-${idx}`}
                                                                        type="button"
                                                                        onClick={(e) => {
                                                                            e.stopPropagation();
                                                                            navigateSnapshot(idx);
                                                                        }}
                                                                        className={`absolute inset-y-0 ${offsetClass} ${isShared ? 'w-1.5' : 'w-2'} rounded-sm bg-accent ring-1 ring-border/90 shadow-sm hover:bg-accent z-30`}
                                                                        style={{ left: `${pct}%` }}
                                                                        title={t('session.usedInTriageTickParsed', { index: position + 1, total: parsedSnapshotIndices.length }) as string}
                                                                    />
                                                                );
                                                            })}
                                                            {/* Activity density heatmap + app_switch ticks */}
                                                            {detail.events?.length > 0 && (() => {
                                                                const tStart = snapshots[0].timestamp;
                                                                const tEnd = snapshots[snapshots.length - 1].timestamp;
                                                                const dur = tEnd - tStart;
                                                                if (dur <= 0) return null;
                                                                // Bucket events into segments for density visualization
                                                                const bucketCount = 40;
                                                                const buckets = new Array(bucketCount).fill(0);
                                                                const appSwitches: { pct: number; appName: string; time: string }[] = [];
                                                                for (const evt of detail.events) {
                                                                    const pct = (evt.timestamp - tStart) / dur;
                                                                    if (pct < 0 || pct > 1) continue;
                                                                    const idx = Math.min(bucketCount - 1, Math.floor(pct * bucketCount));
                                                                    buckets[idx]++;
                                                                    if (evt.kind === 'app_switch') {
                                                                        appSwitches.push({ pct: pct * 100, appName: evt.appName || (t('session.unknownApp') as string), time: formatTime(evt.timestamp) });
                                                                    }
                                                                }
                                                                const maxDensity = Math.max(...buckets, 1);
                                                                return (
                                                                    <>
                                                                        {/* Density bars */}
                                                                        {buckets.map((count, i) => {
                                                                            if (count === 0) return null;
                                                                            const intensity = count / maxDensity;
                                                                            return (
                                                                                <div
                                                                                    key={`d${i}`}
                                                                                    className="absolute top-1/2 -translate-y-1/2 rounded-sm bg-accent-2"
                                                                                    style={{
                                                                                        left: `${(i / bucketCount) * 100}%`,
                                                                                        width: `${Math.max(100 / bucketCount, 2)}%`,
                                                                                        height: `${Math.round(2 + intensity * 6)}px`,
                                                                                        opacity: 0.15 + intensity * 0.45,
                                                                                    }}
                                                                                />
                                                                            );
                                                                        })}
                                                                        {/* App switch tick marks */}
                                                                        {appSwitches.map((sw, i) => (
                                                                            <div
                                                                                key={`s${i}`}
                                                                                className="absolute top-0 bottom-0 w-px bg-surface-active"
                                                                                style={{ left: `${sw.pct}%` }}
                                                                                title={t('session.switchTitle', { app: sw.appName, time: sw.time }) as string}
                                                                            />
                                                                        ))}
                                                                    </>
                                                                );
                                                            })()}
                                                            {/* Playhead thumb */}
                                                            <div
                                                                className="absolute top-1/2 -translate-y-1/2 w-3 h-3 rounded-full bg-accent-2 border-2 border-panel shadow-sm transition-[left] duration-100"
                                                                style={{ left: `${((snapshotIndex) / (snapshots.length - 1)) * 100}%`, marginLeft: '-6px' }}
                                                            />
                                                            {/* Per-snapshot hover preview zones. Sit ABOVE every other
                                                                marker (z-40) so the operator can hover any snapshot
                                                                — used or not — and scan its content without opening
                                                                the popup. Click still drives the scrubber to that
                                                                index. The tooltip uses `pointer-events-none` so
                                                                moving the cursor onto the card doesn't break the
                                                                hover; the card aligns left / right / center based
                                                                on idx position so it doesn't clip at the edges. */}
                                                            {snapshots.map((snap, idx) => {
                                                                const pct = snapshots.length > 1 ? (idx / (snapshots.length - 1)) * 100 : 0;
                                                                const preview = extractSnapshotPreview(snap);
                                                                const isParsed = parsedSnapshotIdsForSession.has(snap.id);
                                                                const isOcr = ocrSnapshotIdsForSession.has(snap.id);
                                                                const isSibling = siblingSnapshotIdsForSession.has(snap.id);
                                                                let cardAlign = 'left-1/2 -translate-x-1/2';
                                                                if (snapshots.length > 4) {
                                                                    if (idx < snapshots.length / 4) cardAlign = 'left-0 translate-x-0';
                                                                    else if (idx > (3 * snapshots.length) / 4) cardAlign = 'right-0 left-auto translate-x-0';
                                                                }
                                                                return (
                                                                    <button
                                                                        key={`hover-${idx}`}
                                                                        type="button"
                                                                        onClick={(e) => {
                                                                            e.stopPropagation();
                                                                            navigateSnapshot(idx);
                                                                        }}
                                                                        className="group/snap absolute inset-y-0 -translate-x-1/2 w-2 z-40"
                                                                        style={{ left: `${pct}%` }}
                                                                        aria-label={t('session.hoverPreviewAria', { index: idx + 1, total: snapshots.length }) as string}
                                                                    >
                                                                        <span className="block w-full h-full bg-transparent hover:bg-surface-hover rounded-sm transition-colors" />
                                                                        <div
                                                                            className={`pointer-events-none absolute bottom-full ${cardAlign} mb-2 w-72 rounded-lg border border-border bg-panel shadow-[var(--shadow-card)] p-3 text-left opacity-0 invisible group-hover/snap:opacity-100 group-hover/snap:visible transition-opacity z-50`}
                                                                        >
                                                                            <div className="flex items-center justify-between text-[10px] text-muted mb-1.5">
                                                                                <span className="tabular-nums">{formatTime(snap.timestamp)} · {idx + 1}/{snapshots.length}</span>
                                                                                {preview.app && <span className="font-medium text-ink/80">{preview.app}</span>}
                                                                            </div>
                                                                            <div className="flex items-center gap-1 mb-2 flex-wrap">
                                                                                {isParsed && (
                                                                                    <span className="inline-flex items-center gap-1 text-[10px] px-1.5 py-px rounded bg-accent-light text-accent">
                                                                                        <span className="w-1.5 h-1.5 rounded-full bg-accent" />{t('session.usedBadgeParsed')}
                                                                                    </span>
                                                                                )}
                                                                                {isOcr && (
                                                                                    <span className="inline-flex items-center gap-1 text-[10px] px-1.5 py-px rounded bg-info/15 text-info">
                                                                                        <span className="w-1.5 h-1.5 rounded-full bg-info" />{t('session.usedBadgeOcr')}
                                                                                    </span>
                                                                                )}
                                                                                {isSibling && (
                                                                                    <span className="inline-flex items-center gap-1 text-[10px] px-1.5 py-px rounded bg-surface-hover/20 text-muted">
                                                                                        <span className="w-1.5 h-1.5 rounded-full bg-surface-hover" />{t('session.siblingBadgeShort')}
                                                                                    </span>
                                                                                )}
                                                                                {!isParsed && !isOcr && !isSibling && (
                                                                                    <span className="inline-flex items-center text-[10px] px-1.5 py-px rounded bg-border/50 text-muted">
                                                                                        {t('session.hoverNotSelected')}
                                                                                    </span>
                                                                                )}
                                                                                {preview.parsedKind && (
                                                                                    <span className="text-[10px] text-muted/80">
                                                                                        {preview.parsedKind}
                                                                                    </span>
                                                                                )}
                                                                            </div>
                                                                            {/* Source label — operators compared this card to the
                                                                                "AS SEEN BY LLM" panel below and (rightly) got confused
                                                                                when the two showed different content for OCR frames.
                                                                                The two views answer different questions:
                                                                                  - This card: macOS Accessibility tree of the focused
                                                                                    window when the snapshot was captured.
                                                                                  - AS SEEN BY LLM: the exact text injected into the
                                                                                    triage prompt (parsed rendering for parsed frames,
                                                                                    OCR-of-the-full-screen for OCR frames).
                                                                                Surfacing the source name removes the "why don't they
                                                                                match" cognitive load. */}
                                                                            <div className="flex items-center justify-between text-[10px] uppercase tracking-wide text-muted/80 mb-1">
                                                                                <span className="font-semibold">{t('session.hoverPreviewSource')}</span>
                                                                                <span className="normal-case tracking-normal text-muted/60">{t('session.hoverPreviewSourceHint')}</span>
                                                                            </div>
                                                                            <div className="text-xs leading-snug text-ink/80 max-h-32 overflow-hidden whitespace-pre-wrap break-words">
                                                                                {preview.text || (
                                                                                    <span className="text-muted italic">{t('session.hoverNoAXText')}</span>
                                                                                )}
                                                                            </div>
                                                                        </div>
                                                                    </button>
                                                                );
                                                            })}
                                                        </div>
                                                    )}
                                                    {/* Snapshot navigation + info */}
                                                    <div className="flex items-center justify-between text-xs text-muted">
                                                        <div className="flex items-center gap-1.5">
                                                            {snapshots.length > 1 && (
                                                                <>
                                                                    <button
                                                                        onClick={togglePlay}
                                                                        className="p-1 rounded hover:bg-surface-hover transition-colors"
                                                                        title={isPlaying ? (t('session.pause') as string) : (t('session.play') as string)}
                                                                    >
                                                                        {isPlaying ? (
                                                                            <Pause className="h-3.5 w-3.5" />
                                                                        ) : (
                                                                            <Play className="h-3.5 w-3.5" />
                                                                        )}
                                                                    </button>
                                                                    <button
                                                                        onClick={handlePrevSnapshot}
                                                                        disabled={snapshotIndex === 0}
                                                                        className="p-1 rounded hover:bg-surface-hover disabled:opacity-30 transition-colors"
                                                                    >
                                                                        <ChevronLeft className="h-3.5 w-3.5" />
                                                                    </button>
                                                                    <span className="tabular-nums min-w-[80px] text-center">
                                                                        {snapshotIndex + 1} / {snapshots.length}
                                                                        {currentSnapshotKind === 'parsed' && (
                                                                            <span className="ml-1 inline-flex items-center gap-1 px-1 py-px rounded bg-surface-active text-accent text-[10px] font-medium">
                                                                                <span className="w-1.5 h-1.5 rounded-full bg-accent" />
                                                                                {t('session.usedBadgeParsed')}
                                                                            </span>
                                                                        )}
                                                                        {currentSnapshotKind === 'ocr' && (
                                                                            <span className="ml-1 inline-flex items-center gap-1 px-1 py-px rounded bg-panel-warm text-info text-[10px] font-medium">
                                                                                <span className="w-1.5 h-1.5 rounded-full bg-info" />
                                                                                {t('session.usedBadgeOcr')}
                                                                            </span>
                                                                        )}
                                                                        {currentSnapshotKind === 'both' && (
                                                                            <span className="ml-1 inline-flex items-center gap-1 px-1 py-px rounded bg-surface-active text-accent text-[10px] font-medium">
                                                                                <span className="w-1.5 h-1.5 rounded-full bg-accent" />
                                                                                <span className="w-1.5 h-1.5 rounded-full bg-info -ml-0.5" />
                                                                                {t('session.usedBadgeBoth')}
                                                                            </span>
                                                                        )}
                                                                    </span>
                                                                    <button
                                                                        onClick={handleNextSnapshot}
                                                                        disabled={snapshotIndex === snapshots.length - 1}
                                                                        className="p-1 rounded hover:bg-surface-hover disabled:opacity-30 transition-colors"
                                                                    >
                                                                        <ChevronRight className="h-3.5 w-3.5" />
                                                                    </button>
                                                                    {/* Playback speed selector */}
                                                                    <DropdownMenu>
                                                                        <DropdownMenuTrigger asChild>
                                                                            <button className="px-1.5 py-0.5 rounded hover:bg-surface-hover transition-colors text-[10px] tabular-nums">
                                                                                {playbackFps}x
                                                                            </button>
                                                                        </DropdownMenuTrigger>
                                                                        <DropdownMenuContent align="start" className="min-w-[80px]">
                                                                            <DropdownMenuLabel className="text-[10px]">{t('session.speed')}</DropdownMenuLabel>
                                                                            <DropdownMenuSeparator />
                                                                            {[1, 2, 4, 8, 16].map((fps) => (
                                                                                <DropdownMenuItem
                                                                                    key={fps}
                                                                                    onClick={() => setPlaybackFps(fps)}
                                                                                    className="text-xs"
                                                                                >
                                                                                    {fps === playbackFps && <Check className="h-3 w-3 mr-1" />}
                                                                                    {fps}x
                                                                                </DropdownMenuItem>
                                                                            ))}
                                                                        </DropdownMenuContent>
                                                                    </DropdownMenu>
                                                                </>
                                                            )}
                                                        </div>
                                                        <div className="flex items-center gap-2">
                                                            {currentSnapshot && (
                                                                <span>
                                                                    {formatTime(currentSnapshot.timestamp)}
                                                                </span>
                                                            )}
                                                            {snapshotSrc && (
                                                                <button
                                                                    onClick={toggleFullscreen}
                                                                    className="p-1 rounded hover:bg-surface-hover transition-colors"
                                                                    title={t('session.fullscreen') as string}
                                                                >
                                                                    <Maximize2 className="h-3.5 w-3.5" />
                                                                </button>
                                                            )}
                                                        </div>
                                                    </div>
                                                    {/* "As seen by LLM" inline panel — surfaces the exact text
                                                        this frame contributed to the triage prompt so operators
                                                        can compare the raw snapshot against what the model
                                                        actually consumed (post-dedup, post-truncation). Only
                                                        renders for currently-selected used frames. */}
                                                    {currentSnapshotKind && (currentSnapshotWinnerMeta.parsed?.promptText || currentSnapshotWinnerMeta.ocr?.promptText) && (
                                                        <div className="rounded-lg border border-border bg-panel p-3 space-y-2 text-xs">
                                                            <div className="flex items-center gap-2 text-[10px] uppercase tracking-wide text-muted">
                                                                <span className="font-semibold">{t('session.asSeenByLLM')}</span>
                                                                <span className="text-muted/70 normal-case tracking-normal">
                                                                    {t('session.asSeenByLLMHint')}
                                                                </span>
                                                            </div>
                                                            {currentSnapshotWinnerMeta.parsed?.promptText && (
                                                                <div>
                                                                    <div className="flex flex-wrap items-center gap-1.5 mb-1">
                                                                        <span className="inline-flex items-center gap-1 text-[10px] px-1.5 py-px rounded bg-accent-light text-accent font-medium">
                                                                            <span className="w-1.5 h-1.5 rounded-full bg-accent" />
                                                                            {t('session.usedBadgeParsed')}
                                                                        </span>
                                                                        <span className="text-[10px] text-muted/80 tabular-nums">
                                                                            {currentSnapshotWinnerMeta.parsed.promptText.length} chars · ~{Math.ceil(currentSnapshotWinnerMeta.parsed.promptText.length / 4)} tokens
                                                                        </span>
                                                                        {/* P1 quality metadata: sibling count tells operators
                                                                            how many dedup losers this winner represents — a
                                                                            high count (10+) means this is a stable winner of
                                                                            a long stretch; a 0 means the winner only speaks
                                                                            for itself. Zero is a load-bearing value (its own
                                                                            "singleton" label), so render distinctly from
                                                                            undefined (legacy / data missing → no label). */}
                                                                        {currentSnapshotWinnerMeta.parsed.siblingCount !== undefined && (
                                                                            currentSnapshotWinnerMeta.parsed.siblingCount > 0 ? (
                                                                                <span className="text-[10px] text-muted tabular-nums">
                                                                                    {t('session.quality.siblingsFolded', { count: currentSnapshotWinnerMeta.parsed.siblingCount })}
                                                                                </span>
                                                                            ) : (
                                                                                <span className="text-[10px] text-muted/60 italic">
                                                                                    {t('session.quality.singletonWinner')}
                                                                                </span>
                                                                            )
                                                                        )}
                                                                        {currentSnapshotWinnerMeta.parsed.browserUrl && (
                                                                            <span className="text-[10px] text-info/80 truncate max-w-[200px]" title={currentSnapshotWinnerMeta.parsed.browserUrl}>
                                                                                {hostnameOf(currentSnapshotWinnerMeta.parsed.browserUrl)}
                                                                            </span>
                                                                        )}
                                                                    </div>
                                                                    <pre className="whitespace-pre-wrap break-words text-[11px] leading-snug max-h-48 overflow-y-auto text-ink/85 font-mono bg-panel rounded border border-border/60 p-2">
                                                                        {currentSnapshotWinnerMeta.parsed.promptText}
                                                                    </pre>
                                                                </div>
                                                            )}
                                                            {currentSnapshotWinnerMeta.ocr?.promptText && (
                                                                <div>
                                                                    <div className="flex flex-wrap items-center gap-1.5 mb-1">
                                                                        <span className="inline-flex items-center gap-1 text-[10px] px-1.5 py-px rounded bg-info/15 text-info font-medium">
                                                                            <span className="w-1.5 h-1.5 rounded-full bg-info" />
                                                                            {t('session.usedBadgeOcr')}
                                                                        </span>
                                                                        <span className="text-[10px] text-muted/80 tabular-nums">
                                                                            {currentSnapshotWinnerMeta.ocr.promptText.length} chars · ~{Math.ceil(currentSnapshotWinnerMeta.ocr.promptText.length / 4)} tokens
                                                                        </span>
                                                                        {/* Confidence < 0.7 colored amber as a soft warning —
                                                                            this is the threshold below which OCR output
                                                                            commonly contains drift errors that mislead the
                                                                            triage. Hard rule of thumb, not a gate. */}
                                                                        {currentSnapshotWinnerMeta.ocr.ocrConfidence !== undefined && currentSnapshotWinnerMeta.ocr.ocrConfidence > 0 && (
                                                                            <span className={`text-[10px] tabular-nums ${currentSnapshotWinnerMeta.ocr.ocrConfidence < 0.7 ? 'text-accent' : 'text-muted/80'}`}>
                                                                                {t('session.quality.ocrConfidence', { value: (currentSnapshotWinnerMeta.ocr.ocrConfidence * 100).toFixed(0) })}
                                                                            </span>
                                                                        )}
                                                                        {currentSnapshotWinnerMeta.ocr.browserUrl && (
                                                                            <span className="text-[10px] text-info/80 truncate max-w-[200px]" title={currentSnapshotWinnerMeta.ocr.browserUrl}>
                                                                                {hostnameOf(currentSnapshotWinnerMeta.ocr.browserUrl)}
                                                                            </span>
                                                                        )}
                                                                    </div>
                                                                    <pre className="whitespace-pre-wrap break-words text-[11px] leading-snug max-h-32 overflow-y-auto text-ink/85 font-mono bg-panel rounded border border-border/60 p-2">
                                                                        {currentSnapshotWinnerMeta.ocr.promptText}
                                                                    </pre>
                                                                </div>
                                                            )}
                                                        </div>
                                                    )}
                                                </div>
                                            )
                                        ) : segmentCount === 0 ? (
                                            <div className="rounded-lg border border-border bg-panel p-6 flex items-center justify-center gap-2 text-sm text-muted">
                                                <Film className="h-4 w-4" />
                                                {t('session.noVideoSegments')}
                                            </div>
                                        ) : !bridgePort ? (
                                            <div className="rounded-lg border border-border bg-panel p-6 flex items-center justify-center gap-2 text-sm text-muted">
                                                <AlertTriangle className="h-4 w-4" />
                                                {t('session.hostBridgeUnavailable')}
                                            </div>
                                        ) : (
                                            /* ── Legacy HEVC video player ── */
                                            <div className="space-y-2">
                                                {/* Fullscreen overlay portaled to document.body so it escapes
                                                    the Radix DialogContent transform that breaks fixed positioning.
                                                    A fresh <video> element starts from t=0 — playback position is
                                                    not preserved across enter/exit, but the layout actually works
                                                    (the previous fixed-inset attempt was constrained to the
                                                    dialog area). */}
                                                {videoSrc && (
                                                    <DialogPrimitive.Root open={isFullscreen} onOpenChange={setIsFullscreen}>
                                                        <DialogPrimitive.Portal>
                                                            <DialogPrimitive.Overlay className="fixed inset-0 z-[100] bg-code-bg" />
                                                            <DialogPrimitive.Content
                                                                className="fixed inset-0 z-[100] flex items-center justify-center bg-code-bg outline-none"
                                                                onClick={() => setIsFullscreen(false)}
                                                                aria-describedby={undefined}
                                                            >
                                                                <DialogPrimitive.Title className="sr-only">
                                                                    {t('session.fullscreen')}
                                                                </DialogPrimitive.Title>
                                                                <video
                                                                    controls
                                                                    autoPlay
                                                                    className="max-w-full max-h-full"
                                                                    src={videoSrc}
                                                                    onClick={(e) => e.stopPropagation()}
                                                                >
                                                                    {t('session.videoUnsupported')}
                                                                </video>
                                                                <DialogPrimitive.Close asChild>
                                                                    <button
                                                                        className="absolute top-4 right-4 p-2 rounded-md bg-code-bg/60 text-code-fg/80 hover:text-code-fg hover:bg-code-bg/80 transition-colors"
                                                                        title={t('session.exitFullscreen')}
                                                                    >
                                                                        <Minimize2 className="h-5 w-5" />
                                                                    </button>
                                                                </DialogPrimitive.Close>
                                                            </DialogPrimitive.Content>
                                                        </DialogPrimitive.Portal>
                                                    </DialogPrimitive.Root>
                                                )}
                                                <div
                                                    ref={videoContainerRef}
                                                    className="group/video relative bg-code-bg overflow-hidden rounded-lg border border-border"
                                                >
                                                    <video
                                                        key={videoSrc}
                                                        controls
                                                        className="w-full max-h-[400px]"
                                                        src={videoSrc ?? undefined}
                                                    >
                                                        {t('session.videoUnsupported')}
                                                    </video>
                                                    <button
                                                        onClick={() => setIsFullscreen(true)}
                                                        className="absolute top-2 right-2 p-1.5 rounded-md bg-code-bg/60 text-code-fg/80 hover:text-code-fg hover:bg-code-bg/80 transition-opacity opacity-0 group-hover/video:opacity-100"
                                                        title={t('session.fullscreen')}
                                                    >
                                                        <Maximize2 className="h-4 w-4" />
                                                    </button>
                                                </div>
                                                <div className="flex items-center justify-between text-xs text-muted">
                                                    <div className="flex items-center gap-2">
                                                        {segmentCount > 1 && (
                                                            <>
                                                                <button
                                                                    onClick={handlePrevSegment}
                                                                    disabled={segmentIndex === 0}
                                                                    className="p-1 rounded hover:bg-surface-hover disabled:opacity-30 transition-colors"
                                                                >
                                                                    <ChevronLeft className="h-3.5 w-3.5" />
                                                                </button>
                                                                <span>
                                                                    {t('session.segment', { current: segmentIndex + 1, total: segmentCount })}
                                                                </span>
                                                                <button
                                                                    onClick={handleNextSegment}
                                                                    disabled={segmentIndex === segmentCount - 1}
                                                                    className="p-1 rounded hover:bg-surface-hover disabled:opacity-30 transition-colors"
                                                                >
                                                                    <ChevronRight className="h-3.5 w-3.5" />
                                                                </button>
                                                            </>
                                                        )}
                                                    </div>
                                                    <div className="flex items-center gap-2">
                                                        {currentSegment && (
                                                            <span>
                                                                {formatBytes(currentSegment.sizeBytes)}
                                                                {currentSegment.codec && ` · ${currentSegment.codec.toUpperCase()}`}
                                                            </span>
                                                        )}
                                                        <button
                                                            onClick={toggleFullscreen}
                                                            className="p-1 rounded hover:bg-surface-hover transition-colors"
                                                            title={t('session.fullscreen') as string}
                                                        >
                                                            <Maximize2 className="h-3.5 w-3.5" />
                                                        </button>
                                                    </div>
                                                </div>
                                            </div>
                                        )}
                                    </div>
                                    )}

                                    {/* ── Event timeline ── */}
                                    <div className="space-y-3 flex-1 min-h-[320px] flex flex-col">
                                        <div className="flex items-center justify-between gap-2 shrink-0">
                                            <h2 className="text-[11px] font-semibold uppercase tracking-[0.08em] text-muted">
                                                {t('session.stepsHeading')}
                                            </h2>
                                            <button
                                                type="button"
                                                onClick={toggleIdentityDebug}
                                                data-active={identityDebugOn}
                                                className={cn(
                                                    'inline-flex items-center gap-1.5 h-7 px-2.5 rounded-md text-[11px] font-medium transition-colors',
                                                    identityDebugOn
                                                        ? 'bg-surface-active text-ink hover:bg-surface-hover'
                                                        : 'text-muted hover:text-ink hover:bg-surface-hover',
                                                )}
                                                title={t('identityDebug.toggleTooltip') as string}
                                            >
                                                <Info className="h-3 w-3 shrink-0 -translate-y-[0.5px]" />
                                                {identityDebugOn
                                                    ? t('identityDebug.toggleOn')
                                                    : t('identityDebug.toggleOff')}
                                            </button>
                                        </div>
                                        {selectedTimelineSummary?.timeline?.length ? (
                                            <div className="flex-1 min-h-0 overflow-y-auto pr-1">
                                                {selectedTimelineSummary.timeline.map((item, idx) => {
                                                    const itemKey = `${idx}:${(item.eventIds ?? []).join(',')}`;
                                                    const expanded = expandedSemanticTimelineItems.has(itemKey);
                                                    const sourceEvents = (item.eventIds ?? [])
                                                        .map((eventId) => {
                                                            const eventIdx = timelineEventIndexById.get(eventId);
                                                            if (eventIdx === undefined || !detail.events?.[eventIdx]) return null;
                                                            return { event: detail.events[eventIdx], eventIdx };
                                                        })
                                                        .filter((entry): entry is { event: activitymon.ActivityEvent; eventIdx: number } => Boolean(entry));
                                                    if (AX_ROLE_TOKEN.test(item.action)) return null;
                                                    const steps = sourceEvents.filter(({ eventIdx }) => stepEventSet.has(eventIdx));
                                                    if (sourceEvents.length > 0 && steps.length === 0) return null;
                                                    const semanticIconKind = sourceEvents[0]?.event.kind ?? (timelineEventsPending ? null : 'focus');
                                                    const actionParts = splitSemanticAction(item.action);
                                                    const toggleExpanded = () => {
                                                        setExpandedSemanticTimelineItems((prev) => {
                                                            const next = new Set(prev);
                                                            if (next.has(itemKey)) {
                                                                next.delete(itemKey);
                                                            } else {
                                                                next.add(itemKey);
                                                            }
                                                            return next;
                                                        });
                                                    };
                                                    return (
                                                        <div key={itemKey} className="min-w-0 pb-3">
                                                            <button
                                                                type="button"
                                                                onClick={toggleExpanded}
                                                                className="group flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left transition-colors hover:bg-surface-hover hover:ring-1 hover:ring-inset hover:ring-border/60"
                                                                aria-expanded={expanded}
                                                            >
                                                                <span className="-ml-1 flex h-5 w-5 items-center justify-center rounded text-muted transition-colors group-hover:text-ink shrink-0">
                                                                    {expanded ? <ChevronDown className="h-3.5 w-3.5" /> : <ChevronRight className="h-3.5 w-3.5" />}
                                                                </span>
                                                                <span className="flex h-5 w-5 items-center justify-center text-muted shrink-0">
                                                                    {semanticIconKind !== null && <EventKindIcon kind={semanticIconKind} />}
                                                                </span>
                                                                <span className="flex min-w-0 flex-1 items-center gap-2 text-xs">
                                                                    <span className="min-w-0 truncate">
                                                                        <span className="font-semibold text-ink">
                                                                            <HighlightText text={actionParts.verb} keyword={debouncedKeyword} />
                                                                        </span>
                                                                        {actionParts.rest && (
                                                                            <span className="font-normal text-ink">
                                                                                {' '}
                                                                                <HighlightText text={actionParts.rest} keyword={debouncedKeyword} />
                                                                            </span>
                                                                        )}
                                                                    </span>
                                                                    {item.target && (
                                                                        <span className="min-w-0 truncate text-ink/70">
                                                                            <HighlightText text={item.target} keyword={debouncedKeyword} />
                                                                        </span>
                                                                    )}
                                                                </span>
                                                            </button>
                                                                {expanded && (
                                                                    <div className="ml-7 mt-1 space-y-1 rounded-md bg-panel px-2 py-1.5">
                                                                        {item.details && item.details.length > 0 && (
                                                                            <div className="space-y-0.5 text-[11px] leading-snug text-muted/75">
                                                                            {item.details.map((detailText, detailIdx) => (
                                                                                <div key={`${detailText}-${detailIdx}`} className="truncate">
                                                                                    <HighlightText text={detailText} keyword={debouncedKeyword} />
                                                                                </div>
                                                                            ))}
                                                                            </div>
                                                                        )}
                                                                        {steps.map(({ event, eventIdx }) => {
                                                                            const action = eventActionLabel(event, t);
                                                                            const keyboardChars = event.keyChars ?? (event.keyChar ? [event.keyChar] : []);
                                                                            const hasMedia = eventHasMedia.get(eventIdx) ?? false;
                                                                            return (
                                                                                <button
                                                                                    key={`${timelineEventId(event) ?? event.timestamp}-${eventIdx}`}
                                                                                    type="button"
                                                                                    onClick={hasMedia ? () => handleEventClick(event) : undefined}
                                                                                    className={cn(
                                                                                        'flex w-full items-start gap-2 rounded px-1.5 py-1 text-left text-[11px] transition-colors',
                                                                                        hasMedia ? 'hover:bg-background/70 cursor-pointer' : 'cursor-default',
                                                                                    )}
                                                                                >
                                                                                    <span className="mt-0.5 text-muted/70 shrink-0">
                                                                                        <EventKindIcon kind={event.kind} />
                                                                                    </span>
                                                                                    <span className="min-w-0 flex-1">
                                                                                        <span className="font-medium text-ink/80">
                                                                                            {action.verb}
                                                                                        </span>
                                                                                        {event.kind === 'keyboard' && event.typedText ? (
                                                                                            <span className="ml-1 text-muted/80">
                                                                                                <HighlightText text={event.typedText} keyword={debouncedKeyword} />
                                                                                            </span>
                                                                                        ) : event.kind === 'keyboard' && keyboardChars.length > 0 ? (
                                                                                            <span className="ml-1 text-muted/80">
                                                                                                {keyboardChars.join('')}
                                                                                            </span>
                                                                                        ) : action.object ? (
                                                                                            <span className="ml-1 text-muted/80">
                                                                                                <HighlightText text={action.object} keyword={debouncedKeyword} />
                                                                                            </span>
                                                                                        ) : null}
                                                                                        {event.windowTitle && (
                                                                                            <span className="ml-1 text-muted/55">
                                                                                                {event.windowTitle}
                                                                                            </span>
                                                                                        )}
                                                                                    </span>
                                                                                    <span className="shrink-0 tabular-nums text-muted/60">
                                                                                        {formatTime(event.timestamp)}
                                                                                    </span>
                                                                                </button>
                                                                            );
                                                                        })}
                                                                    </div>
                                                                )}
                                                        </div>
                                                    );
                                                })}
                                            </div>
                                        ) : stepEventRows.length === 0 ? (
                                            <p className="text-sm text-muted">{t('session.noEventsRecorded')}</p>
                                        ) : (
                                            <div
                                                ref={eventsListContainerRef}
                                                className="relative flex-1 min-h-0"
                                            >
                                                <Virtuoso
                                                    ref={virtuosoRef}
                                                    style={{ height: eventsListHeight }}
                                                    totalCount={stepEventRows.length}
                                                    itemContent={(row) => {
                                                        const idx = stepEventRows[row];
                                                        const evt = detail.events[idx];
                                                        const prevEvt = row > 0 ? detail.events[stepEventRows[row - 1]] : null;
                                                        // O(1) lookup of precomputed media availability (see eventHasMedia)
                                                        // — avoids an O(N) findClosestSnapshotIndex scan per visible row on
                                                        // every scroll render. A row is clickable only when THIS event's
                                                        // app has a frame (same-app snapshot), never another app's.
                                                        const hasMedia = eventHasMedia.get(idx) ?? false;
                                                        const isMatch = debouncedKeyword ? eventMatchesKeyword(evt, debouncedKeyword) : false;
                                                        const isActive = idx === activeEventIndex;
                                                        const isSpeakerHighlight =
                                                            selectedSpeakerTimestamps !== null &&
                                                            selectedSpeakerTimestamps.has(evt.timestamp);
                                                        const isAppSwitch = prevEvt && prevEvt.appName !== evt.appName;
                                                        const action = eventActionLabel(evt, t);
                                                        const keyboardChars = evt.keyChars ?? (evt.keyChar ? [evt.keyChar] : []);
                                                        return (
                                                        <>
                                                            {/* App transition separator */}
                                                            {isAppSwitch && (
                                                                <div className="flex items-center gap-2 py-1 -ml-1">
                                                                    <div className="flex-1 h-px bg-border" />
                                                                    <span className="text-[9px] text-muted/60 uppercase tracking-wider shrink-0">
                                                                        {evt.appName || (t('session.unknownApp') as string)}
                                                                    </span>
                                                                    <div className="flex-1 h-px bg-border" />
                                                                </div>
                                                            )}
                                                        <div
                                                            className={`group relative flex items-start gap-2 px-2 py-1 rounded-md transition-colors ring-inset ${
                                                                isActive ? 'bg-active-row-bg ring-1 ring-border-strong' :
                                                                isSpeakerHighlight ? 'bg-surface-active ring-1 ring-accent-2/30' :
                                                                isMatch ? 'bg-surface-active ring-1 ring-accent/20' : ''
                                                            } ${
                                                                hasMedia
                                                                    ? 'cursor-pointer hover:bg-surface-hover hover:ring-1 hover:ring-border/60'
                                                                    : ''
                                                            }`}
                                                            onClick={hasMedia ? () => {
                                                                // Clicked row is already on screen — suppress the
                                                                // activeEventIndex-driven re-center that would
                                                                // otherwise fire when handleEventClick mutates
                                                                // snapshotIndex below. Popup prev/next paths
                                                                // intentionally do NOT set this flag, so closing
                                                                // the popup leaves the list aligned with the
                                                                // last-viewed event.
                                                                skipNextActiveScrollRef.current = true;
                                                                handleEventClick(evt);
                                                                // Fallback clear in case the row the user just
                                                                // clicked happens to resolve to the SAME
                                                                // activeEventIndex (e.g. they clicked the row
                                                                // that's already highlighted). In that case
                                                                // snapshotIndex doesn't change, so the
                                                                // auto-scroll effect doesn't fire, and the flag
                                                                // would otherwise stay stuck at true and
                                                                // swallow the next legitimate scroll. A
                                                                // setTimeout(0) is reliably ordered AFTER React
                                                                // commits this click's renders and runs any
                                                                // pending passive effects, so when the effect
                                                                // DID fire (normal case) it has already cleared
                                                                // the flag and this is a harmless no-op.
                                                                setTimeout(() => {
                                                                    skipNextActiveScrollRef.current = false;
                                                                }, 0);
                                                            } : undefined}
                                                        >
                                                            {/* Event content. Speech events without an appBundleId
                                                                were captured before per-event filtering was wired up
                                                                (or before FrontmostAppInfo was implemented on Windows);
                                                                dim the mic icon and add a tooltip so users understand
                                                                why hovering doesn't reveal the silence-app button on
                                                                those rows. */}
                                                            <div className="flex items-center gap-2 text-muted">
                                                                {evt.kind === 'speech' && !evt.appBundleId ? (
                                                                    <span
                                                                        className="opacity-40"
                                                                        title={t('speechEvent.unfilterableTooltip') as string}
                                                                    >
                                                                        <EventKindIcon kind={evt.kind} />
                                                                    </span>
                                                                ) : (
                                                                    <EventKindIcon kind={evt.kind} />
                                                                )}
                                                            </div>
                                                            <div className="flex-1 min-w-0">
                                                                <div className="flex items-center gap-2 text-xs min-w-0">
                                                                    <span className="font-semibold text-ink shrink-0">
                                                                        {action.verb}
                                                                    </span>
                                                                    {evt.kind === 'keyboard' && evt.typedText ? (
                                                                        <span className="text-ink/80 truncate">
                                                                            <HighlightText text={evt.typedText} keyword={debouncedKeyword} />
                                                                        </span>
                                                                    ) : evt.kind === 'keyboard' && keyboardChars.length > 0 ? (
                                                                        <KeyCharDisplay keyChars={keyboardChars} keyword={debouncedKeyword} />
                                                                    ) : action.object ? (
                                                                        <span className="text-ink/80 truncate">
                                                                            <HighlightText text={action.object} keyword={debouncedKeyword} />
                                                                        </span>
                                                                    ) : null}
                                                                </div>
                                                                {evt.windowTitle && (
                                                                    <div className="flex items-center gap-1.5 text-[11px] text-muted/70 mt-0.5">
                                                                        {evt.windowTitle && (
                                                                            <span className="truncate text-muted/60">
                                                                                <HighlightText text={evt.windowTitle} keyword={debouncedKeyword} />
                                                                            </span>
                                                                        )}
                                                                    </div>
                                                                )}
                                                            </div>
                                                            {evt.kind === 'speech' && evt.appBundleId && (
                                                                <button
                                                                    type="button"
                                                                    onClick={(e) => {
                                                                        e.stopPropagation();
                                                                        const bundleID = evt.appBundleId!;
                                                                        const appName = evt.appName || bundleID;
                                                                        AddVoiceInputApp(bundleID, appName)
                                                                            .then(() => setSpeechNotice({ key: bundleID, notice: { tone: 'done', text: t('speechEvent.toasts.added', { name: appName }) } }))
                                                                            .catch(() => setSpeechNotice({ key: bundleID, notice: { tone: 'error', text: t('speechEvent.toasts.addFailed') } }));
                                                                    }}
                                                                    className="opacity-0 group-hover:opacity-60 hover:!opacity-100 transition-opacity shrink-0 text-muted hover:text-ink p-0.5 rounded"
                                                                    title={t('speechEvent.ignoreAppTooltip', { name: evt.appName || (t('session.unknownApp') as string) })}
                                                                    aria-label={t('speechEvent.ignoreAppTooltip', { name: evt.appName || (t('session.unknownApp') as string) }) as string}
                                                                >
                                                                    <MicOff className="h-3 w-3" />
                                                                </button>
                                                            )}
                                                            {evt.kind === 'speech' && speechNotice && speechNotice.key === evt.appBundleId && (
                                                                <ActivityInlineNotice notice={speechNotice.notice} />
                                                            )}
                                                            {hasMedia && (
                                                                <Play className="h-3 w-3 text-muted opacity-0 group-hover:opacity-60 transition-opacity shrink-0" />
                                                            )}
                                                            <span className="text-[10px] text-muted shrink-0 tabular-nums">
                                                                {formatTime(evt.timestamp)}
                                                            </span>
                                                            {identityDebugOn && identityVerdicts && identityVerdicts[idx] && (
                                                                <EventIdentityBadge verdict={identityVerdicts[idx]} />
                                                            )}
                                                        </div>
                                                        </>
                                                        );
                                                    }}
                                                />
                                            </div>
                                        )}
                                    </div>

                                    {/* ── OCR matches section ── */}
                                    {ocrMatches.length > 0 && bridgePort > 0 && (
                                        <div className="space-y-3">
                                            <h2 className="text-[11px] font-semibold uppercase tracking-[0.08em] text-muted">
                                                {t('session.ocrSection', { count: ocrMatches.length })}
                                            </h2>
                                            <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
                                                {ocrMatches.map((match, idx) => {
                                                    let frameSrc: string;
                                                    let matchTimestamp: number;

                                                    let matchSnapshotPath: string | undefined;
                                                    if (isSnapshotSession) {
                                                        // For snapshot sessions, segmentId is actually a snapshot ID
                                                        const snap = snapshots.find((s) => s.id === match.segmentId);
                                                        const snapURL = snap ? snapshotImageURL(bridgePort, snap) : null;
                                                        if (!snap || !snapURL) return null;
                                                        frameSrc = snapURL;
                                                        matchTimestamp = snap.timestamp;
                                                        matchSnapshotPath = snap.filePath;
                                                    } else {
                                                        // Legacy HEVC: find the segment for this match
                                                        const seg = detail.segments?.find(
                                                            (s) => s.id === match.segmentId,
                                                        );
                                                        if (!seg) return null;
                                                        frameSrc = hostBridgeMediaURL(bridgePort, 'frame', { path: seg.filePath, t: match.frameTime.toFixed(1) });
                                                        matchTimestamp = seg.startTime + match.frameTime * 1000;
                                                    }

                                                    const snippet = match.text.length > 120
                                                        ? match.text.slice(0, 120) + '...'
                                                        : match.text;

                                                    return (
                                                        <button
                                                            key={idx}
                                                            className="flex flex-col rounded-lg border border-border overflow-hidden hover:border-accent-2/40 transition-colors text-left"
                                                            onClick={() => {
                                                                setEventPopup({
                                                                    frameSrc,
                                                                    evt: {
                                                                        timestamp: matchTimestamp,
                                                                        kind: 'click',
                                                                        appBundleId: '',
                                                                        appName: t('session.ocrMatchAppName') as string,
                                                                        windowTitle: t('session.ocrMatchWindowTitle', { time: match.frameTime.toFixed(1) }) as string,
                                                                    } as activitymon.ActivityEvent,
                                                                    snapshotPath: matchSnapshotPath,
                                                                });
                                                            }}
                                                        >
                                                            <div className="bg-code-bg h-[100px] flex items-center justify-center">
                                                                <SnapshotImage
                                                                    src={frameSrc}
                                                                    alt={t('session.ocrFrameAlt', { value: match.frameTime.toFixed(1) }) as string}
                                                                    className="w-full h-full object-contain"
                                                                    loading="lazy"
                                                                    placeholderClassName="w-full h-full text-code-fg/50"
                                                                />
                                                            </div>
                                                            <div className="px-2.5 py-2 space-y-1">
                                                                <div className="flex items-center gap-1.5">
                                                                    <span className="text-[9px] uppercase tracking-wider font-medium text-accent-2 bg-surface-active px-1 py-0.5 rounded">
                                                                        OCR
                                                                    </span>
                                                                    <span className="text-[10px] text-muted tabular-nums">
                                                                        {t('session.ocrTimeSeconds', { value: match.frameTime.toFixed(1) })}
                                                                    </span>
                                                                    <span className="text-[10px] text-muted">
                                                                        {t('session.ocrConfidence', { value: Math.round(match.confidence * 100) })}
                                                                    </span>
                                                                </div>
                                                                <p className="text-[11px] text-muted line-clamp-2 leading-relaxed">
                                                                    <HighlightText text={snippet} keyword={debouncedKeyword} />
                                                                </p>
                                                            </div>
                                                        </button>
                                                    );
                                                })}
                                            </div>
                                        </div>
                                    )}

                                    {/* ── Knowledge section ── */}
                                    <SessionKnowledge sessionId={detail.id} />
                                </>
                            )}
                        </div>
                    </ScrollArea>
                    </>
                    )}
                </div>
            </div>

            {/* ── Scheduled run detail (Day view "Open result") — look up the
                job from the day's freshly loaded created-automations list and
                the parent-held cronJobs.jobs, same as the old Calendar page. ── */}
            <AutomationDetailDialog
                job={detailJobId
                    ? calendarData.createdAutomations.find((j) => j.id === detailJobId)
                        ?? cronJobs.jobs.find((j) => j.id === detailJobId)
                        ?? null
                    : null}
                open={!!detailJobId}
                onOpenChange={(open) => { if (!open) setDetailJobId(null); }}
                onToggleEnabled={cronJobs.toggleEnabled}
                onDelete={(id) => {
                    calendarData.handleDeleteAutomation(id);
                    setDetailJobId(null);
                }}
                onRunNow={cronJobs.runNow}
                onUpdate={cronJobs.updateJob}
                loadRuns={cronJobs.loadRuns}
                loadRunLogs={cronJobs.loadRunLogs}
            />
            {/* ── Event video popup ── */}
            {eventPopup && (() => {
                // The popup can be opened from two places: a click on a row
                // in detail.events (reference-identical to one of the array
                // entries) OR the OCR-match grid below, which synthesizes a
                // fresh ActivityEvent that isn't in the events array. The
                // reference search returns -1 for the synthetic case, which
                // leaves both callbacks undefined and renders the prev/next
                // buttons as disabled — correct, since there's no
                // surrounding event sequence to walk.
                const events = detail?.events ?? [];
                const idx = events.indexOf(eventPopup.evt);
                const prevEvt = idx > 0 ? events[idx - 1] : null;
                const nextEvt = idx >= 0 && idx < events.length - 1 ? events[idx + 1] : null;
                return (
                    <EventFramePopup
                        data={eventPopup}
                        keyword={debouncedKeyword}
                        onClose={() => setEventPopup(null)}
                        onPrev={prevEvt ? () => handleEventClick(prevEvt) : undefined}
                        onNext={nextEvt ? () => handleEventClick(nextEvt) : undefined}
                        identityDebugOn={identityDebugOn}
                        highlightedSenderName={effectiveHighlightedSenderName}
                    />
                );
            })()}
        </div>
    );
}

/**
 * Strip the leading frontmatter block from a knowledge markdown body so the
 * preview shows only descriptive content. Drops the title, `- **Key**: Value`
 * metadata lines, and — when `Activity Session` spans a nested bullet list —
 * the indented session bullets beneath it.
 */
function stripKnowledgeFrontmatter(content: string): string {
    const lines = content.split('\n');
    const out: string[] = [];
    let skippingSessionBlock = false;
    for (const line of lines) {
        if (skippingSessionBlock) {
            if (/^\s+- /.test(line) || line.trim() === '') continue;
            skippingSessionBlock = false;
        }
        if (line.startsWith('# ')) continue;
        if (line.startsWith('- **Date')) continue;
        if (line.startsWith('- **Apps')) continue;
        if (line.startsWith('- **Activity Session')) {
            skippingSessionBlock = true;
            continue;
        }
        out.push(line);
    }
    return out.join('\n').trim();
}

/** Inline knowledge section for a specific activity session */
function SessionKnowledge({ sessionId }: { sessionId: string }) {
    const { t } = useTranslation(['activity', 'common']);
    const [entries, setEntries] = useState<main.KnowledgeEntry[]>([]);

    useEffect(() => {
        GetKnowledgeForSession(sessionId).then((result) => {
            setEntries(result ?? []);
        });
    }, [sessionId]);

    if (entries.length === 0) return null;

    return (
        <div className="space-y-3">
            <h2 className="text-[11px] font-semibold uppercase tracking-[0.08em] text-muted">
                {t('session.knowledgeSection', { count: entries.length })}
            </h2>
            <div className="space-y-3">
                {entries.map((entry) => (
                    <div
                        key={entry.id}
                        className="group relative rounded-lg border border-border bg-panel p-4 space-y-2 select-text"
                    >
                        <h3 className="text-xs font-medium text-ink pr-7">{entry.title}</h3>
                        {entry.content && (
                            <div className="text-[11px] text-muted leading-relaxed">
                                <MarkdownContent
                                    content={stripKnowledgeFrontmatter(entry.content)}
                                />
                            </div>
                        )}
                        <div className="absolute top-2.5 right-2.5 opacity-0 group-hover:opacity-100 group-focus-within:opacity-100 transition-opacity">
                            <CopyButton
                                text={entry.content}
                                label=""
                                copiedLabel=""
                                title={t('common:copyTooltip') as string}
                                className="p-1 rounded-md text-muted hover:text-ink hover:bg-border/50"
                                iconClassName="h-3 w-3"
                            />
                        </div>
                    </div>
                ))}
            </div>
        </div>
    );
}
