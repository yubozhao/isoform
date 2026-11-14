import type {
    AgentStatus,
    GitInfo,
    MainChatRowClass,
    RemoteEnv,
    StreamItem,
    StreamItemKind,
    StreamItemData,
    StreamOfferReference,
    Session,
    SessionData,
    SessionKind,
    Project,
    ProjectData,
    Workspace,
    WorkspaceData,
} from '@/types';
import i18n from '@/i18n';
import type { CommunicationDraftMetadata } from '@/lib/communication';
import { parseOpenAnswer } from '@/lib/open-answers';
import {
    SaveProjectState,
    SaveActiveProjectID,
    GetGitInfo,
    DetectProject,
    NextProjectSortOrder,
} from '../../wailsjs/go/main/App';

export const emptyStatus: AgentStatus = { running: false, ready: false, port: 0 };

export const logPersistError = (err: unknown) =>
    console.error('[persist]', err);

export const STANDALONE_CHAT_PROJECT_ID = '__standalone_chat__';
export const STANDALONE_CHAT_WORKSPACE_ID = '__standalone_workspace__';

/** toolName of the `system` item that opens a side chat with the main-chat
 *  message it branched from. Read by the Go side-chat context producer. */
export const SIDE_CHAT_ORIGIN_TOOL = 'side_chat_origin';
/** toolName of the `system` items background work posts into the main chat. */
export const MAIN_CHAT_REPORT_TOOL = 'main_chat_report';
export const MAIN_CHAT_PROACTIVE_TOOL = 'main_chat_proactive';
export const MAIN_CHAT_STATUS_TOOL = 'main_chat_status';
export const MAIN_CHAT_OBSERVATION_PAUSE_TOOL = 'main_chat_observation_pause';
/** toolName of the quiet line an answered setup ask leaves in the main chat. */
export const MAIN_CHAT_SETUP_TOOL = 'main_chat_setup';
/** toolName of the metadata-only receipt for context on a Today chat turn. */
export const TODAY_CONTEXT_TOOL = 'today_context';

/** New receipts have their own kind so older phones omit their metadata;
 *  retain rendering for receipts saved with the original system kind. */
export function isTodayContextItem(item: StreamItem): boolean {
    return (item.kind === 'context_status' || item.kind === 'system') && item.toolName === TODAY_CONTEXT_TOOL;
}
/** toolName of the `system` item a texted reply becomes in the main chat. Its
 *  sender is unverified, so it is never a user message. */
export const MAIN_CHAT_TEXT_REPLY_TOOL = 'main_chat_text_reply';

/** A texted reply kept in the main chat (mirrors Go mainChatTextReply). */
export interface TextedReply {
    text: string;
    receivedAt?: string;
}

/** The texted reply an item records, or null. */
export function getTextedReply(
    item: Pick<StreamItem, 'kind' | 'toolName' | 'toolInput' | 'content'>,
): TextedReply | null {
    if (item.kind !== 'system' || item.toolName !== MAIN_CHAT_TEXT_REPLY_TOOL) return null;
    const text = (item.content ?? '').trim();
    if (!text) return null;
    const raw = (item.toolInput && typeof item.toolInput === 'object' ? item.toolInput : {}) as {
        receivedAt?: string;
    };
    return { text, receivedAt: raw.receivedAt };
}

/** The answered setup step a record line describes, or null. */
export function getSetupRecord(
    item: Pick<StreamItem, 'kind' | 'toolName' | 'toolInput'>,
): { step: string; answer: string; name?: string } | null {
    if (item.kind !== 'system' || item.toolName !== MAIN_CHAT_SETUP_TOOL) return null;
    const raw = (item.toolInput && typeof item.toolInput === 'object' ? item.toolInput : {}) as {
        step?: string;
        answer?: string;
        name?: string;
    };
    if (!raw.step || !raw.answer) return null;
    // The name step's line carries the name the user gave.
    return typeof raw.name === 'string' && raw.name
        ? { step: raw.step, answer: raw.answer, name: raw.name }
        : { step: raw.step, answer: raw.answer };
}

/** What a main-chat report's action opens (mirrors Go mainChatTarget). */
export interface MainChatReportTarget {
    kind: 'session' | 'handoff' | 'knowledge' | 'memory' | 'automation' | 'today' | 'upcoming' | 'suggestion' | 'settings' | 'playbook' | 'calendar' | 'person' | 'mail' | 'thread' | 'tasks' | 'none';
    id?: string;
    runId?: string;
}

export interface MainChatProactiveEvidence {
    kind: string;
    id: string;
    at?: string;
    target: MainChatReportTarget;
}

/** Display-only copy for the main chat; surrounding frontend chrome is
 *  translated here. Mirrors Go mainChatProactive. */
export interface MainChatProactiveCopy {
    headline: string;
    body: string;
    actionLabels: Record<string, string>;
    helps?: string[];
}

export interface MainChatProactiveWork {
    handoffId: string;
    status: string;
    summary: string;
    result: string;
    deliverables: { label: string; url: string }[];
    blockedReason?: string;
    verification?: { met: number; total: number; note?: string };
}

export interface MainChatProactivePermission {
    requestId: string;
    sessionId: string;
    handoffId: string;
    toolName: string;
    details: string;
}

export interface MainChatProactiveDraft {
    to: string;
    subject: string;
    text: string;
    communication?: CommunicationDraftMetadata;
    channel?: 'mail' | 'slack';
    recipient?: string;
    principalId?: string;
    revision?: string;
}

export interface MainChatProactivePayload {
    momentId: string;
    kind: string;
    sentences?: string[];
    helps?: string[];
    offerCount?: number;
    firstReadConfirmedAt?: number;
    holdSource?: { thing: { title: string; sourceKind: string; sourceId: string }; due?: string };
    headline: string;
    body: string;
    draft: boolean;
    draftContent?: MainChatProactiveDraft;
    actionIds: string[];
    actionLabels: Record<string, string>;
    target: MainChatReportTarget;
    evidence: MainChatProactiveEvidence[];
    state: string;
    /** When Mail or Slack took a sent draft (unix ms). */
    sentAt?: number;
    /** When the outcome behind this card was refreshed (unix ms); the card keeps its place (chat v1.0.12). */
    updatedAt?: number;
    kindMuted?: boolean;
    items?: MainChatProactivePayload[];
    localized?: Record<string, MainChatProactiveCopy>;
    briefId?: string;
    captureStartAt?: number;
    captureEndAt?: number;
    captureSource?: string;
    /** Why capture stopped: "permission" or "stopped"; absent for a pause nobody needs told about. */
    captureCause?: string;
    briefDisplay?: { slot: string; asOf: string; timezone: string };
    /** The brief card's first words, read with the card. */
    briefLead?: string;
    personName?: string;
    event?: MainChatProactiveEvent;
    events?: MainChatProactiveEvent[];
    place?: MainChatProactivePlace;
    work?: MainChatProactiveWork;
    permission?: MainChatProactivePermission;
}

export interface MainChatProactiveEvent {
    title?: string;
    start?: string;
    end?: string;
    place?: string;
    people?: string[];
    conferenceHost?: string;
    private?: boolean;
    minutes?: number;
}

export interface MainChatProactivePlace {
    app?: string;
    bundleId?: string;
    window?: string;
    at?: string;
}

export interface MainChatStatusPayload {
    period: string;
    entries: { id: string; momentId?: string; kind: string; text: string; target: MainChatReportTarget; captureStartAt?: number; captureEndAt?: number; captureSource?: string; captureCause?: string; briefDay?: string; briefSlot?: string; briefTime?: string; briefCause?: string; localized?: Record<string, string> }[];
}

function mainChatTarget(raw: unknown): MainChatReportTarget {
    if (!raw || typeof raw !== 'object') return { kind: 'none' };
    const target = raw as Partial<MainChatReportTarget>;
    return { kind: target.kind ?? 'none', id: target.id, runId: target.runId };
}

function proactiveTarget(kind: string, raw: unknown): MainChatReportTarget {
    const target = mainChatTarget(raw);
    // Repair the two incorrect destinations in cards saved before routing
    // was corrected. Keep other stored destinations unchanged.
    if (kind === 'capture_paused' && target.kind === 'settings' && target.id === 'notifications') return { kind: 'settings', id: 'activity' };
    if (kind === 'waiting_on_reply' && target.kind === 'today') return { kind: 'mail' };
    return target;
}

function localizedProactiveCopy(raw: unknown): Record<string, MainChatProactiveCopy> | undefined {
    if (!raw || typeof raw !== 'object') return undefined;
    return Object.fromEntries(Object.entries(raw).filter(([, value]) => {
        if (!value || typeof value !== 'object') return false;
        const copy = value as Partial<MainChatProactiveCopy>;
        return typeof copy.headline === 'string' && typeof copy.body === 'string'
            && copy.actionLabels && typeof copy.actionLabels === 'object'
            && Object.values(copy.actionLabels).every((label) => typeof label === 'string');
    }));
}

function proactiveWork(raw: unknown): MainChatProactiveWork | undefined {
    if (!raw || typeof raw !== 'object') return undefined;
    const work = raw as Partial<MainChatProactiveWork>;
    if (typeof work.handoffId !== 'string' || !work.handoffId || typeof work.status !== 'string') return undefined;
    const verification = work.verification;
    const validVerification = verification && Number.isInteger(verification.met) && Number.isInteger(verification.total)
        && verification.met >= 0 && verification.total >= verification.met;
    return {
        handoffId: work.handoffId,
        status: work.status,
        summary: typeof work.summary === 'string' ? work.summary : '',
        result: typeof work.result === 'string' ? work.result : '',
        deliverables: Array.isArray(work.deliverables) ? work.deliverables.filter((link) => link && typeof link.label === 'string' && typeof link.url === 'string') : [],
        blockedReason: typeof work.blockedReason === 'string' ? work.blockedReason : undefined,
        verification: validVerification ? {
            met: verification.met,
            total: verification.total,
            note: typeof verification.note === 'string' ? verification.note : undefined,
        } : undefined,
    };
}

function proactivePermission(raw: unknown): MainChatProactivePermission | undefined {
    if (!raw || typeof raw !== 'object') return undefined;
    const permission = raw as Partial<MainChatProactivePermission>;
    if (typeof permission.requestId !== 'string' || !permission.requestId || typeof permission.sessionId !== 'string' || !permission.sessionId) return undefined;
    if (typeof permission.handoffId !== 'string' || !permission.handoffId || typeof permission.toolName !== 'string' || typeof permission.details !== 'string') return undefined;
    return { requestId: permission.requestId, sessionId: permission.sessionId, handoffId: permission.handoffId, toolName: permission.toolName, details: permission.details };
}

function proactiveCommunication(raw: unknown): CommunicationDraftMetadata | undefined {
    if (!raw || typeof raw !== 'object') return undefined;
    const communication = raw as Partial<CommunicationDraftMetadata>;
    if (!Array.isArray(communication.beliefIds) || communication.beliefIds.length === 0
        || !communication.beliefIds.every((id) => typeof id === 'string' && id.trim())
        || typeof communication.recipientName !== 'string' || !communication.recipientName.trim()
        || typeof communication.language !== 'string' || !communication.language.trim()
        || !Number.isInteger(communication.sentences) || (communication.sentences ?? 0) <= 0
        || typeof communication.greeting !== 'boolean') return undefined;
    return {
        beliefIds: communication.beliefIds,
        recipientName: communication.recipientName,
        language: communication.language,
        sentences: communication.sentences!,
        greeting: communication.greeting,
    };
}

function proactiveDraft(raw: unknown): MainChatProactiveDraft | undefined {
    if (!raw || typeof raw !== 'object') return undefined;
    const draft = raw as Partial<MainChatProactiveDraft>;
    if (typeof draft.to !== 'string' || !draft.to.trim() || typeof draft.subject !== 'string'
        || typeof draft.text !== 'string' || !draft.text.trim()) return undefined;
    const communication = proactiveCommunication(draft.communication);
    return {
        to: draft.to,
        subject: draft.subject,
        text: draft.text,
        ...(communication ? { communication } : {}),
        channel: draft.channel === 'mail' || draft.channel === 'slack' ? draft.channel : undefined,
        recipient: typeof draft.recipient === 'string' ? draft.recipient : undefined,
        principalId: typeof draft.principalId === 'string' ? draft.principalId : undefined,
        revision: typeof draft.revision === 'string' ? draft.revision : undefined,
    };
}

/** Sending requires the visible draft and every piece of server-owned send metadata. */
export function canSendMainChatProactiveDraft(draft: MainChatProactiveDraft | undefined): draft is MainChatProactiveDraft & {
    channel: 'mail' | 'slack';
    recipient: string;
    principalId: string;
    revision: string;
} {
    return Boolean(draft && (draft.channel === 'mail' || draft.channel === 'slack')
        && (draft.channel !== 'mail' || draft.subject.trim())
        && draft.recipient?.trim() && draft.principalId?.trim() && draft.revision?.trim());
}

function proactivePayload(raw: unknown, nested = false): MainChatProactivePayload | null {
    if (!raw || typeof raw !== 'object') return null;
    const payload = raw as Partial<MainChatProactivePayload>;
    if (typeof payload.momentId !== 'string' || !payload.momentId || typeof payload.kind !== 'string') return null;
    if (typeof payload.headline !== 'string' || typeof payload.body !== 'string') return null;
    const labels = payload.actionLabels && typeof payload.actionLabels === 'object' ? payload.actionLabels : {};
    return {
        momentId: payload.momentId,
        kind: payload.kind,
        sentences: Array.isArray(payload.sentences) ? payload.sentences.filter((s): s is string => typeof s === 'string').slice(0, 2) : undefined,
        helps: Array.isArray(payload.helps) ? payload.helps.filter((s): s is string => typeof s === 'string').slice(0, 3) : undefined,
        offerCount: typeof payload.offerCount === 'number' ? Math.max(0, Math.min(3, payload.offerCount)) : 0,
        firstReadConfirmedAt: typeof payload.firstReadConfirmedAt === 'number' ? payload.firstReadConfirmedAt : undefined,
        holdSource: payload.holdSource && typeof payload.holdSource.thing?.title === 'string' && typeof payload.holdSource.thing?.sourceKind === 'string' && typeof payload.holdSource.thing?.sourceId === 'string' ? payload.holdSource : undefined,
        headline: payload.headline,
        body: payload.body,
        draft: payload.draft === true,
        draftContent: payload.draft === true ? proactiveDraft(payload.draftContent) : undefined,
        actionIds: Array.isArray(payload.actionIds) ? payload.actionIds.filter((id) => typeof id === 'string' && typeof labels[id] === 'string' && labels[id].trim() !== '') : [],
        actionLabels: labels,
        localized: localizedProactiveCopy(payload.localized),
        work: proactiveWork(payload.work),
        permission: proactivePermission(payload.permission),
        target: proactiveTarget(payload.kind, payload.target),
        evidence: Array.isArray(payload.evidence) ? payload.evidence.filter((ref) => ref && typeof ref.kind === 'string' && typeof ref.id === 'string').map((ref) => ({ ...ref, target: proactiveTarget(payload.kind!, ref.target) })) : [],
        state: typeof payload.state === 'string' ? payload.state : 'pending',
        sentAt: typeof payload.sentAt === 'number' && payload.sentAt > 0 ? payload.sentAt : undefined,
        updatedAt: typeof payload.updatedAt === 'number' && payload.updatedAt > 0 ? payload.updatedAt : undefined,
        kindMuted: payload.kindMuted === true,
        captureSource: typeof payload.captureSource === 'string' ? payload.captureSource : undefined,
        captureCause: typeof payload.captureCause === 'string' && payload.captureCause ? payload.captureCause : undefined,
        captureStartAt: typeof payload.captureStartAt === 'number' && Number.isFinite(new Date(payload.captureStartAt).getTime()) && payload.captureStartAt > 0 ? payload.captureStartAt : undefined,
        captureEndAt: typeof payload.captureEndAt === 'number' && Number.isFinite(new Date(payload.captureEndAt).getTime()) && payload.captureEndAt > 0 ? payload.captureEndAt : undefined,
        briefId: typeof payload.briefId === 'string' ? payload.briefId : undefined,
        briefLead: typeof payload.briefLead === 'string' && payload.briefLead.trim() ? payload.briefLead : undefined,
        briefDisplay: payload.briefDisplay && typeof payload.briefDisplay.slot === 'string' && typeof payload.briefDisplay.asOf === 'string' && typeof payload.briefDisplay.timezone === 'string' ? payload.briefDisplay : undefined,
        personName: typeof payload.personName === 'string' ? payload.personName : undefined,
        event: proactiveEvent(payload.event),
        events: Array.isArray(payload.events) ? payload.events.map(proactiveEvent).filter((event): event is MainChatProactiveEvent => event !== undefined) : undefined,
        place: proactivePlace(payload.place),
        items: !nested && Array.isArray(payload.items) ? payload.items.map((child) => proactivePayload(child, true)).filter((child): child is MainChatProactivePayload => child !== null) : undefined,
    };
}

function proactiveEvent(raw: unknown): MainChatProactiveEvent | undefined {
    if (!raw || typeof raw !== 'object') return undefined;
    const event = raw as Partial<MainChatProactiveEvent>;
    const people = (event as { people?: unknown }).people;
    return {
        title: typeof event.title === 'string' ? event.title : undefined,
        start: typeof event.start === 'string' ? event.start : undefined,
        end: typeof event.end === 'string' ? event.end : undefined,
        place: typeof event.place === 'string' ? event.place : undefined,
        people: typeof people === 'string'
            ? people.split(/,\s*/).filter(Boolean)
            : Array.isArray(people) ? people.filter((person): person is string => typeof person === 'string') : undefined,
        conferenceHost: typeof event.conferenceHost === 'string' ? event.conferenceHost : undefined,
        private: event.private === true,
        minutes: typeof event.minutes === 'number' ? event.minutes : undefined,
    };
}

function proactivePlace(raw: unknown): MainChatProactivePlace | undefined {
    if (!raw || typeof raw !== 'object') return undefined;
    const place = raw as Partial<MainChatProactivePlace>;
    return {
        app: typeof place.app === 'string' ? place.app : undefined,
        bundleId: typeof place.bundleId === 'string' ? place.bundleId : undefined,
        window: typeof place.window === 'string' ? place.window : undefined,
        at: typeof place.at === 'string' ? place.at : undefined,
    };
}

export function getMainChatProactive(item: Pick<StreamItem, 'kind' | 'toolName' | 'toolInput'>): MainChatProactivePayload | null {
    return item.kind === 'system' && item.toolName === MAIN_CHAT_PROACTIVE_TOOL ? proactivePayload(item.toolInput) : null;
}

export function getMainChatStatus(item: Pick<StreamItem, 'kind' | 'toolName' | 'toolInput'>): MainChatStatusPayload | null {
    if (item.kind !== 'system' || item.toolName !== MAIN_CHAT_STATUS_TOOL) return null;
    const raw = item.toolInput;
    if (!raw || typeof raw !== 'object') return null;
    const payload = raw as Partial<MainChatStatusPayload>;
    if (typeof payload.period !== 'string' || !Array.isArray(payload.entries)) return null;
    return {
        period: payload.period,
        entries: payload.entries.filter((entry) => entry && typeof entry.id === 'string' && typeof entry.text === 'string').map((entry) => ({ ...entry, momentId: typeof entry.momentId === 'string' && entry.momentId ? entry.momentId : undefined, captureStartAt: typeof entry.captureStartAt === 'number' && Number.isFinite(new Date(entry.captureStartAt).getTime()) && entry.captureStartAt > 0 ? entry.captureStartAt : undefined, captureEndAt: typeof entry.captureEndAt === 'number' && Number.isFinite(new Date(entry.captureEndAt).getTime()) && entry.captureEndAt > 0 ? entry.captureEndAt : undefined, captureSource: typeof entry.captureSource === 'string' ? entry.captureSource : undefined, target: proactiveTarget(entry.kind, entry.target), localized: entry.localized && typeof entry.localized === 'object' ? Object.fromEntries(Object.entries(entry.localized).filter(([, text]) => typeof text === 'string')) : undefined })),
    };
}

/** The structured payload of a main-chat report (mirrors Go mainChatReport). */
export interface MainChatReportPayload {
    source: 'handoff' | 'schedule' | 'meeting' | 'engagement' | 'side_chat' | 'message_send';
    key?: string;
    action: 'review' | 'open' | 'needs_you' | '';
    target: MainChatReportTarget;
    title?: string;
    status?: string;
    summary?: string;
    channel?: 'mail' | 'slack';
    sentAt?: number;
    principalId?: string;
    /** An Upcoming item the renderer names itself: brief, brief_morning,
     *  brief_evening, news. */
    item?: string;
    /** The cron expression an Upcoming item runs on after a change. */
    schedule?: string;
    /** Display parameters for an engagement sentence; excludes mail subjects. */
    params?: Record<string, string>;
}

/** Returns the report payload when the item is a main-chat report, else null. */
export function getMainChatReport(
    item: Pick<StreamItem, 'kind' | 'toolName' | 'toolInput'>,
): MainChatReportPayload | null {
    if (item.kind !== 'system' || item.toolName !== MAIN_CHAT_REPORT_TOOL) return null;
    const raw = item.toolInput;
    if (!raw || typeof raw !== 'object') return null;
    const payload = raw as Partial<MainChatReportPayload>;
    if (!payload.source || !payload.target || typeof payload.target !== 'object') return null;
    return {
        source: payload.source,
        key: payload.key,
        action: payload.action ?? '',
        target: { kind: payload.target.kind ?? 'none', id: payload.target.id, runId: payload.target.runId },
        title: payload.title,
        status: payload.status,
        summary: payload.summary,
        channel: payload.channel === 'mail' || payload.channel === 'slack' ? payload.channel : undefined,
        sentAt: typeof payload.sentAt === 'number' && Number.isFinite(payload.sentAt) ? payload.sentAt : undefined,
        principalId: typeof payload.principalId === 'string' ? payload.principalId : undefined,
        item: payload.item,
        schedule: payload.schedule,
        params: payload.params,
    };
}

/** toolName of the confirm card the main chat posts before removing a
 *  schedule from Upcoming (Go upcomingConfirmTool). */
export const UPCOMING_CONFIRM_TOOL = 'upcoming_confirm';

// A message the main chat drafted on request (chat_draft.go). It never
// sends: the card offers Copy and, for mail, Open in mail app.
export const CHAT_DRAFT_TOOL = 'main_chat_draft';

export interface ChatDraftPayload {
    to: string;
    channel: 'mail' | 'message' | 'slack' | 'other';
    subject: string;
    text: string;
}

export function getChatDraft(item: Pick<StreamItem, 'kind' | 'toolName' | 'toolInput'>): ChatDraftPayload | null {
    if (item.kind !== 'system' || item.toolName !== CHAT_DRAFT_TOOL) return null;
    let raw: unknown = item.toolInput;
    if (typeof raw === 'string') {
        try { raw = JSON.parse(raw); } catch { return null; }
    }
    if (!raw || typeof raw !== 'object') return null;
    const payload = raw as Partial<ChatDraftPayload>;
    if (typeof payload.to !== 'string' || typeof payload.text !== 'string' || !payload.text.trim()) return null;
    const channel = payload.channel === 'mail' || payload.channel === 'message' || payload.channel === 'slack' ? payload.channel : 'other';
    return { to: payload.to, channel, subject: typeof payload.subject === 'string' ? payload.subject : '', text: payload.text };
}

/** A mailto: link that opens the draft in the person's mail app, unsent. */
export function chatDraftMailto(draft: ChatDraftPayload): string {
    const to = /^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(draft.to.trim()) ? encodeURIComponent(draft.to.trim()) : '';
    const params = [draft.subject ? `subject=${encodeURIComponent(draft.subject)}` : '', `body=${encodeURIComponent(draft.text)}`].filter(Boolean).join('&');
    return `mailto:${to}?${params}`;
}

export type UpcomingConfirmStatus = 'pending' | 'removed' | 'kept' | 'expired' | 'gone';

/** The confirm card's payload (mirrors Go upcomingConfirmPayload). */
export interface UpcomingConfirmPayload {
    action: 'remove';
    jobId: string;
    title: string;
    status: UpcomingConfirmStatus;
    expiresAt: number;
}

/** Returns the confirm card's payload when the item is one, else null. */
export function getUpcomingConfirm(
    item: Pick<StreamItem, 'kind' | 'toolName' | 'toolInput'>,
): UpcomingConfirmPayload | null {
    if (item.kind !== 'system' || item.toolName !== UPCOMING_CONFIRM_TOOL) return null;
    const raw = item.toolInput;
    if (!raw || typeof raw !== 'object') return null;
    const payload = raw as Partial<UpcomingConfirmPayload>;
    if (payload.action !== 'remove' || !payload.jobId) return null;
    return {
        action: 'remove',
        jobId: payload.jobId,
        title: payload.title ?? '',
        status: payload.status ?? 'pending',
        expiresAt: typeof payload.expiresAt === 'number' ? payload.expiresAt : 0,
    };
}

/** The main-chat message a side chat branched from, when the item records one. */
export interface SideChatOrigin {
    parentId: string;
    itemId: string;
    excerpt: string;
}

export function getSideChatOrigin(
    item: Pick<StreamItem, 'kind' | 'toolName' | 'toolInput' | 'content'>,
): SideChatOrigin | null {
    if (item.kind !== 'system' || item.toolName !== SIDE_CHAT_ORIGIN_TOOL) return null;
    const raw = (item.toolInput && typeof item.toolInput === 'object' ? item.toolInput : {}) as {
        parentId?: string;
        itemId?: string;
    };
    return { parentId: raw.parentId ?? '', itemId: raw.itemId ?? '', excerpt: item.content ?? '' };
}

/** Merges rows the backend appended (session:items-appended) into the loaded
 *  items: most rows already present by id are skipped, so a reply the renderer
 *  streamed itself is never duplicated, and the rest are appended in sort
 *  order. Returns the same array when nothing is new. */
export function mergeAppendedItems(existing: StreamItem[], fresh: StreamItem[]): StreamItem[] {
    const known = new Set(existing.map((i) => i.id));
    const updates = new Map(fresh.filter((item) => item.kind === 'system' && (item.toolName === MAIN_CHAT_PROACTIVE_TOOL || item.toolName === MAIN_CHAT_STATUS_TOOL || item.toolName === MAIN_CHAT_OBSERVATION_PAUSE_TOOL)).map((item) => [item.id, item]));
    let changed = false;
    const merged = existing.map((item) => {
        const update = updates.get(item.id);
        if (!update || (update.sortOrder === item.sortOrder && update.content === item.content && JSON.stringify(update.toolInput) === JSON.stringify(item.toolInput))) return item;
        // Overlapping refreshes can finish out of order: an older response never
        // moves a status row backwards.
        if (update.sortOrder !== undefined && item.sortOrder !== undefined && update.sortOrder < item.sortOrder) return item;
        changed = true;
        return update;
    });
    const added = fresh
        .filter((i) => !known.has(i.id))
        .sort((a, b) => (a.sortOrder ?? 0) - (b.sortOrder ?? 0));
    if (added.length === 0 && !changed) return existing;
    const combined = [...merged, ...added];
    const ordered = combined.filter((item) => item.sortOrder !== undefined).sort((a, b) => a.sortOrder! - b.sortOrder!);
    let orderedIndex = 0;
    // Keep unsaved streamed items in their current slots. Persisted status
    // rows move by their new order without moving a live assistant reply.
    return combined.map((item) => item.sortOrder === undefined ? item : ordered[orderedIndex++]);
}

/** The newest report lines of the main chat, oldest first, for the hub. */
export function selectHubReports(items: StreamItem[], limit: number): StreamItem[] {
    const reports = items.filter((item) => getMainChatReport(item) !== null);
    return reports.slice(Math.max(0, reports.length - limit));
}

/** The side chats of the main chat, newest first, for the hub. */
export function selectHubSideChats(sessions: Session[], limit: number): Session[] {
    return sessions
        .filter(isSideChatSession)
        .sort((a, b) => (b.updatedAt || b.createdAt || 0) - (a.updatedAt || a.createdAt || 0))
        .slice(0, limit);
}

/** System items that stand on their own in the chat (report lines, side-chat
 *  origins) rather than being status rows inside a processing block. */
export function isStandaloneSystemItem(item: StreamItem): boolean {
    return (
        item.kind === 'system' &&
        (item.toolName === MAIN_CHAT_REPORT_TOOL ||
            item.toolName === MAIN_CHAT_PROACTIVE_TOOL ||
            item.toolName === MAIN_CHAT_STATUS_TOOL ||
            item.toolName === MAIN_CHAT_OBSERVATION_PAUSE_TOOL ||
            item.toolName === SIDE_CHAT_ORIGIN_TOOL ||
            item.toolName === MAIN_CHAT_SETUP_TOOL ||
            item.toolName === MAIN_CHAT_TEXT_REPLY_TOOL)
    );
}

export type UILanguage = 'en' | 'zh-CN';

export function getCurrentUILanguage(): UILanguage {
    const language = i18n.resolvedLanguage || i18n.language || 'en';
    return language.toLowerCase().startsWith('zh') ? 'zh-CN' : 'en';
}

// ── ID generation ──

export const buildId = () =>
    `${Date.now()}-${Math.random().toString(16).slice(2)}`;

// ── Codex thinking levels ──

export const CODEX_SESSION_MODES = [
    { id: 'full-auto', label: 'Full Auto' },
    { id: 'auto-edit', label: 'Auto Edit' },
    { id: 'suggest', label: 'Suggest' },
] as const;

/** Split a composite model ID (e.g. "gpt-5.2-codex:xhigh") into [base, effort]. */
export function splitModelEffort(model: string): [string, string] {
    const idx = model.indexOf(':');
    if (idx === -1) return [model, ''];
    return [model.slice(0, idx), model.slice(idx + 1)];
}

/** Combine base model and effort into a composite model ID. */
export function combineModelEffort(base: string, effort: string): string {
    if (!effort) return base;
    return `${base}:${effort}`;
}

// ── Session factory ──

export interface CreateSessionOptions {
    /** 'side' creates a side chat; parentId must then be the main chat. */
    kind?: SessionKind;
    parentId?: string;
}

export const createSession = (
    model: string,
    workspaceId: string,
    label?: string,
    options: CreateSessionOptions = {},
): Session => {
    const executor = getExecutorType(model);
    return {
        localId: buildId(),
        workspaceId,
        kind: options.kind ?? '',
        parentId: options.parentId ?? '',
        sessionId: '',
        label:
            label ??
            `Session ${new Date().toLocaleTimeString('en-US', {
                hour: '2-digit',
                minute: '2-digit',
            })}`,
        items: [],
        model,
        createdAt: Date.now(),
        currentRunId: null,
        streaming: false,
        streamingStartedAt: 0,
        thinkingLevel: '',
        thinkingEnabled: false,
        planMode: false,
        sessionMode: executor === 'codex' ? 'full-auto' : '',
        awaitingPlanApproval: false,
        slashCommandsLoading: false,
        slashCommandsLoaded: false,
        pinned: false,
        pinnedAt: 0,
        updatedAt: Date.now(),
        streamItemCount: 0,
        itemsLoaded: true,
        itemsLoading: false,
        hasMore: false,
        nextCursor: 0,
        loadingMore: false,
        compactionState: 'idle',
    };
};

export const createWorkspace = (label?: string, isDefault = false): Workspace => ({
    id: buildId(),
    label: label ?? 'Default',
    isDefault,
    activeSessionId: null,
    sessions: [],
    createdAt: Date.now(),
    updatedAt: Date.now(),
});

export const createStandaloneChatProject = (model: string): Project => {
    const defaultWs = createWorkspace('General Chat', true);
    defaultWs.id = STANDALONE_CHAT_WORKSPACE_ID;

    const firstSession = createSession(model, defaultWs.id, 'General Chat');
    defaultWs.sessions = [firstSession];
    defaultWs.activeSessionId = firstSession.localId;

    return {
        id: STANDALONE_CHAT_PROJECT_ID,
        name: 'General Chat',
        path: '',
        isYansuProject: false,
        workspaces: [defaultWs],
        activeWorkspaceId: defaultWs.id,
        activeSessionId: firstSession.localId,
        sortOrder: -1,
    };
};

// ── Format helpers ──

export const formatToolInput = (input: unknown) => {
    if (input === undefined || input === null) {
        return '';
    }
    try {
        return JSON.stringify(input, null, 2);
    } catch {
        return String(input);
    }
};

// ── Persistence conversion helpers ──

export function tryParseJSON(str: string): unknown {
    try {
        return JSON.parse(str);
    } catch {
        return str;
    }
}

export function normalizeSessionKind(kind: string | undefined): SessionKind {
    return kind === 'main' || kind === 'side' ? kind : '';
}

export const isMainChatSession = (s: Pick<Session, 'kind'>): boolean => s.kind === 'main';
export const isSideChatSession = (s: Pick<Session, 'kind'>): boolean => s.kind === 'side';

/** Locates the main chat (kind 'main') anywhere in the loaded projects. */
export function findMainChatSession(
    projects: Project[],
): { project: Project; workspace: Workspace; session: Session } | null {
    for (const project of projects) {
        for (const workspace of project.workspaces) {
            const session = workspace.sessions.find(isMainChatSession);
            if (session) return { project, workspace, session };
        }
    }
    return null;
}

export function convertSessionFromStorage(sd: SessionData): Session {
    // Backwards compat: old sessions may store composite model IDs like "gpt-5.2-codex:xhigh".
    let sessionModel = sd.model;
    let thinkingLevel = sd.thinkingLevel ?? '';
    if (!thinkingLevel && sessionModel.includes(':')) {
        const [base, effort] = splitModelEffort(sessionModel);
        sessionModel = base;
        thinkingLevel = effort;
    }

    const rawItems = Array.isArray(sd.items) ? sd.items : [];
    const items: StreamItem[] = rawItems.map(fromStreamItemData);
    const itemsLoaded = Array.isArray(sd.items);
    const streamItemCount = Math.max(sd.streamItemCount ?? 0, items.length);

    // Items are loaded by conversation turns (not a fixed item count), so we
    // detect older history by checking if the earliest loaded item has a
    // sort_order above 0 — meaning there are items before it in the DB.
    const firstSortOrder = items.length > 0 ? (items[0].sortOrder ?? 0) : 0;
    const hasMore = itemsLoaded
        ? items.length > 0 && firstSortOrder > 0
        : streamItemCount > 0;

    return {
        localId: sd.localId,
        workspaceId: sd.workspaceId ?? '',
        sessionId: sd.sessionId,
        label: sd.label,
        items,
        model: sessionModel,
        createdAt: sd.createdAt,
        currentRunId: null,
        streaming: false,
        streamingStartedAt: 0,
        thinkingLevel,
        thinkingEnabled: sd.thinkingEnabled ?? false,
        planMode: sd.planMode ?? false,
        sessionMode: getExecutorType(sessionModel) === 'codex' ? 'full-auto' : '',
        awaitingPlanApproval: false,
        slashCommandsLoading: false,
        slashCommandsLoaded: false,
        automationResult: sd.automationResult,
        meetingId: sd.meetingId,
        kind: normalizeSessionKind(sd.kind),
        parentId: sd.parentId ?? '',
        pinned: sd.pinned ?? false,
        pinnedAt: sd.pinnedAt ?? 0,
        updatedAt: sd.updatedAt ?? sd.createdAt,
        streamItemCount,
        itemsLoaded,
        itemsLoading: false,
        hasMore,
        nextCursor: firstSortOrder,
        loadingMore: false,
        compactionState: 'idle',
    };
}

export function convertFromStorage(data: ProjectData[]): Project[] {
    return data.map((pd) => ({
        id: pd.id,
        name: pd.name,
        path: pd.path,
        organization: pd.organization,
        product: pd.product,
        isYansuProject: pd.isYansuProject,
        workspaces: (pd.workspaces ?? []).map((wd) => ({
            id: wd.id,
            label: wd.label,
            isDefault: wd.isDefault,
            worktreePath: wd.worktreePath,
            worktreeBranch: wd.worktreeBranch,
            baseBranch: wd.baseBranch,
            activeSessionId: wd.activeSessionId || null,
            remoteEnv: parseRemoteEnv(wd.remoteEnv),
            sessions: (wd.sessions ?? []).map(convertSessionFromStorage),
            createdAt: wd.createdAt,
            updatedAt: wd.updatedAt,
        })),
        activeWorkspaceId: pd.activeWorkspaceId || null,
        activeSessionId: pd.activeSessionId || null,
        sortOrder: pd.sortOrder ?? 0,
    }));
}

export function toProjectData(p: Project): ProjectData {
    return {
        id: p.id,
        name: p.name,
        path: p.path,
        organization: p.organization,
        product: p.product,
        isYansuProject: p.isYansuProject,
        activeSessionId: p.activeSessionId ?? '',
        activeWorkspaceId: p.activeWorkspaceId ?? '',
        sortOrder: p.sortOrder ?? 0,
        workspaces: p.workspaces.map(toWorkspaceData),
    };
}

export function toWorkspaceData(w: Workspace): WorkspaceData {
    return {
        id: w.id,
        label: w.label,
        isDefault: w.isDefault,
        worktreePath: w.worktreePath,
        worktreeBranch: w.worktreeBranch,
        baseBranch: w.baseBranch,
        activeSessionId: w.activeSessionId ?? '',
        remoteEnv: w.remoteEnv ? JSON.stringify(w.remoteEnv) : undefined,
        sessions: w.sessions.map((s) => toSessionData(s)),
        createdAt: w.createdAt,
        updatedAt: w.updatedAt,
    };
}

/** Parse a JSON-encoded RemoteEnv string from persistence. */
function parseRemoteEnv(raw?: string): RemoteEnv | undefined {
    if (!raw) return undefined;
    try {
        const parsed = JSON.parse(raw);
        if (parsed && typeof parsed === 'object' && parsed.type && parsed.type !== 'local') {
            return parsed as RemoteEnv;
        }
        return undefined;
    } catch {
        return undefined;
    }
}

export function toSessionData(s: Session): SessionData {
    return {
        localId: s.localId,
        workspaceId: s.workspaceId,
        sessionId: s.sessionId,
        label: s.label,
        model: s.model,
        createdAt: s.createdAt,
        prdEnabled: false,
        automationResult: s.automationResult,
        meetingId: s.meetingId,
        kind: s.kind ?? '',
        parentId: s.parentId ?? '',
        thinkingLevel: s.thinkingLevel ?? '',
        thinkingEnabled: s.thinkingEnabled,
        planMode: s.planMode,
        pinned: s.pinned,
        pinnedAt: s.pinnedAt,
        updatedAt: s.updatedAt,
        streamItemCount: Math.max(s.streamItemCount ?? 0, s.items.length),
        items: [],
    };
}

export function toStreamItemData(item: StreamItem): StreamItemData {
    const context: {
        output?: string;
        jobId?: string;
        handoff?: boolean;
        handoffId?: string;
        offerReferences?: StreamOfferReference[];
    } = item.automationResultOutput
        ? { output: item.automationResultOutput, jobId: item.automationJobId ?? '' }
        : item.handoffContext
          ? { handoff: true, handoffId: item.handoffId ?? '' }
          : {};
    if (item.offerReferences?.length) context.offerReferences = item.offerReferences;
    return {
        id: item.id,
        kind: item.kind,
        content: item.content,
        toolName: item.toolName,
        toolInput:
            item.toolInput !== undefined
                ? JSON.stringify(item.toolInput)
                : undefined,
        toolUseId: item.toolUseId,
        runId: item.runId,
        sessionId: item.sessionId,
        createdAt: item.createdAt,
        openAnswer: item.openAnswer,
        cardType: item.cardType,
        sortOrder: item.sortOrder ?? 0,
        // JSON-encode attachments so they survive the RPC boundary and
        // round-trip through SQLite. Empty array collapses to undefined
        // so we don't store "[]" for every item.
        images:
            item.images && item.images.length > 0
                ? JSON.stringify(item.images)
                : undefined,
        files:
            item.files && item.files.length > 0
                ? JSON.stringify(item.files)
                : undefined,
        // Serialize message metadata into a single flat column so special
        // render states survive reload.
        automationContext: item.offerSourceContext ?? (Object.keys(context).length ? JSON.stringify(context) : undefined),
    };
}

// parseStreamItemImages reverses the JSON encoding from toStreamItemData
// when loading items back from the DB. Tolerant of malformed data: a
// bad blob just yields no images instead of breaking the whole session
// load. Exported for the hydration paths in App.tsx / useProjects.ts.
export function parseStreamItemImages(raw: string | undefined): StreamItem['images'] {
    if (!raw) return undefined;
    try {
        const parsed = JSON.parse(raw);
        if (!Array.isArray(parsed) || parsed.length === 0) return undefined;
        const valid = parsed.filter(
            (x) =>
                x &&
                typeof x === 'object' &&
                typeof x.mediaType === 'string' &&
                typeof x.dataBase64 === 'string',
        );
        return valid.length > 0 ? valid : undefined;
    } catch {
        return undefined;
    }
}

export function parseStreamItemFiles(raw: string | undefined): StreamItem['files'] {
    if (!raw) return undefined;
    try {
        const parsed = JSON.parse(raw);
        if (!Array.isArray(parsed) || parsed.length === 0) return undefined;
        const valid = parsed.filter(
            (x: unknown) =>
                x &&
                typeof x === 'object' &&
                typeof (x as Record<string, unknown>).id === 'string' &&
                typeof (x as Record<string, unknown>).fileName === 'string',
        );
        return valid.length > 0 ? valid : undefined;
    } catch {
        return undefined;
    }
}

// fromStreamItemData deserializes a wire-format StreamItemData (as
// returned by LoadStreamItemsPage) into the StreamItem shape the UI
// renders. Handles `toolInput` JSON parsing and image deserialization
// so every call site doesn't re-implement the same mapping.
// parseAutomationContext reverses the JSON encoding from toStreamItemData.
// Tolerant of malformed data: a bad blob yields no context instead of
// breaking the whole session load.
function parseAutomationContext(
    raw: string | undefined,
): { output?: string; jobId?: string; handoff?: boolean; handoffId?: string; offerReferences?: StreamOfferReference[]; offerSourceContext?: string; weekAnswerBriefId?: string } {
    if (!raw) return {};
    try {
        const parsed = JSON.parse(raw);
        if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) return { offerSourceContext: raw };
        const output = typeof parsed.output === 'string' ? parsed.output : undefined;
        const jobId = typeof parsed.jobId === 'string' && parsed.jobId ? parsed.jobId : undefined;
        const handoff = parsed.handoff === true;
        const handoffId = typeof parsed.handoffId === 'string' && parsed.handoffId ? parsed.handoffId : undefined;
        const offerReferences = Array.isArray(parsed.offerReferences)
            ? parsed.offerReferences.filter((reference: unknown): reference is StreamOfferReference => {
                  if (!reference || typeof reference !== 'object') return false;
                  const value = reference as Partial<StreamOfferReference>;
                  return typeof value.id === 'string' && value.id.length > 0
                      && typeof value.version === 'string' && value.version.length > 0;
              }).map(({ id, version }: StreamOfferReference) => ({ id, version }))
            : undefined;
        const weekAnswerBriefId = typeof parsed.weekAnswerBriefId === 'string' && /^\d{4}-\d{2}-\d{2}-(?:morning|on_request-\d{6})$/.test(parsed.weekAnswerBriefId) ? parsed.weekAnswerBriefId : undefined;
        const offerSourceContext = weekAnswerBriefId || Object.keys(parsed).some((key) => /^(offerReferences|offerSources)$/iu.test(key)) ? raw : undefined;
        return { output, jobId, handoff, handoffId, offerReferences: offerReferences?.length ? offerReferences : undefined, offerSourceContext, weekAnswerBriefId };
    } catch {
        // Keep unreadable context opaque. Dropping a truncated source envelope
        // would turn a server-denied row into apparently ordinary user text.
        return { offerSourceContext: raw };
    }
}

// extractCuaScreenshotPath pulls a window-screenshot path out of a
// cua_driver tool result so the chat can render the capture inline. The
// cua_driver agent tool (yansu-agent/agent/tools/cua_driver.go) returns
// the BARE result object — not an {ok,result} envelope — so the path
// lives at the top level. Three shapes carry one:
//   - get_window_state → { ..., screenshot: { path, width, height } }
//     (the daemon's native object shape)
//   - the screenshot/shot tool → { path, width, height }
//   - a flattened { ..., screenshot: "<path>" } string, in case an
//     upstream reshape ever surfaces it that way
// A defensive {result:{...}} unwrap covers any caller that does wrap.
// Returns undefined for any non-cua / pathless result. The on-disk file
// is read lazily at render time via the ReadCuaScreenshotBase64 binding.
// Shared by live streaming (useAgentStreaming) AND history reload
// (fromStreamItemData below): screenshotPath is derived, never persisted,
// so both paths must run the same extraction or reopened sessions lose
// their thumbnails.
export function extractCuaScreenshotPath(content: string | undefined): string | undefined {
    if (!content) return undefined;
    const trimmed = content.trim();
    if (!trimmed.startsWith('{')) return undefined;
    let parsed: unknown;
    try {
        parsed = JSON.parse(trimmed);
    } catch {
        return undefined;
    }
    if (!parsed || typeof parsed !== 'object') return undefined;
    let obj = parsed as Record<string, unknown>;
    if (obj.result && typeof obj.result === 'object') {
        obj = obj.result as Record<string, unknown>;
    }
    const isImagePath = (p: unknown): p is string =>
        typeof p === 'string' && /\.(jpe?g|png)$/i.test(p);
    // get_window_state: nested screenshot object { path, width, height }.
    if (obj.screenshot && typeof obj.screenshot === 'object') {
        const shot = obj.screenshot as Record<string, unknown>;
        if (isImagePath(shot.path)) return shot.path;
    }
    // Flattened string form.
    if (isImagePath(obj.screenshot)) return obj.screenshot;
    // screenshot / shot tool: top-level { path, width, height }.
    if (isImagePath(obj.path) && 'width' in obj && 'height' in obj) {
        return obj.path;
    }
    return undefined;
}

export function fromStreamItemData(d: {
    id: string;
    kind: string;
    content?: string;
    toolName?: string;
    toolInput?: string;
    toolUseId?: string;
    runId?: string;
    sessionId?: string;
    openAnswer?: unknown;
    cardType?: string;
    createdAt?: number;
    sortOrder?: number;
    images?: string;
    automationContext?: string;
    weekAnswerBriefId?: string;
    files?: string;
    rowClass?: string;
    topicIds?: string[];
    localDay?: string;
}): StreamItem {
    const automationCtx = parseAutomationContext(d.automationContext);
    return {
        id: d.id,
        kind: d.kind as StreamItemKind,
        content: d.content,
        toolName: d.toolName,
        toolInput: d.toolInput ? tryParseJSON(d.toolInput) : undefined,
        toolUseId: d.toolUseId,
        runId: d.runId,
        sessionId: d.sessionId,
        createdAt: typeof d.createdAt === "number" ? d.createdAt : undefined,
        openAnswer: parseOpenAnswer(d.openAnswer),
        cardType: typeof d.cardType === 'string' ? d.cardType : undefined,
        sortOrder: d.sortOrder,
        images: parseStreamItemImages(d.images),
        files: parseStreamItemFiles(d.files),
        // Derived, not persisted: recover CU screenshot thumbnails on
        // history reload the same way live streaming does.
        screenshotPath:
            d.kind === 'result' || d.kind === 'tool_result'
                ? extractCuaScreenshotPath(d.content)
                : undefined,
        automationResultOutput: automationCtx.output,
        automationJobId: automationCtx.jobId,
        handoffContext: automationCtx.handoff,
        handoffId: automationCtx.handoffId,
        offerReferences: automationCtx.offerReferences,
        offerSourceContext: automationCtx.offerSourceContext,
        weekAnswerBriefId: automationCtx.weekAnswerBriefId ?? (typeof d.weekAnswerBriefId === 'string' && /^\d{4}-\d{2}-\d{2}-morning$/.test(d.weekAnswerBriefId) ? d.weekAnswerBriefId : undefined),
        // Main chat view fields (absent for every other session).
        ...(d.rowClass ? { rowClass: d.rowClass as MainChatRowClass } : {}),
        ...(d.topicIds?.length ? { topicIds: d.topicIds } : {}),
        ...(d.localDay ? { localDay: d.localDay } : {}),
    };
}


// ── Grouped item type for rendering ──

export type GroupedStreamItem =
    | { type: 'single'; item: StreamItem }
    | { type: 'tool-group'; toolName: string; items: StreamItem[] }
    | {
          type: 'thinking-group';
          thinkingItem: StreamItem;
          tools: StreamItem[];
      };

// Convert raw stream items into renderable items without changing their
// chronological order. Tool calls are wrapped so the processing block can use
// the shared tool-row renderer, but each call remains its own row.
export function groupStreamItems(items: StreamItem[]): GroupedStreamItem[] {
    const result: GroupedStreamItem[] = [];

    for (const item of items) {
        if (item.kind === 'tool') {
            result.push({
                type: 'tool-group',
                toolName: item.toolName ?? 'tool',
                items: [item],
            });
        } else {
            result.push({ type: 'single', item });
        }
    }

    return result;
}

// A Turn groups items from one user message to the next.
//
// Each turn renders as one chronological processing block plus the trailing
// assistant summary. Reasoning, tool calls, system/status rows, errors, and any
// assistant text that happened before more work are kept inside processing; only
// the final contiguous assistant run is surfaced outside the collapsed block.
export interface TurnSegment {
    processing: GroupedStreamItem[];
    finals: GroupedStreamItem[];
}

export interface Turn {
    user: GroupedStreamItem | null;
    segments: TurnSegment[];
}

export function splitIntoTurns(
    grouped: GroupedStreamItem[],
    streaming: boolean
): Turn[] {
    // First pass: split on user messages into raw (user, items) groups.
    interface RawTurn { user: GroupedStreamItem | null; items: GroupedStreamItem[] }
    const raw: RawTurn[] = [];
    let currentRaw: RawTurn = { user: null, items: [] };

    for (const g of grouped) {
        if (g.type === 'single' && g.item.kind === 'user') {
            if (currentRaw.user || currentRaw.items.length > 0) {
                raw.push(currentRaw);
            }
            currentRaw = { user: g, items: [] };
            continue;
        }
        currentRaw.items.push(g);
    }
    if (currentRaw.user || currentRaw.items.length > 0) {
        raw.push(currentRaw);
    }

    // Second pass: within each raw turn, keep one big processing block and only
    // surface the final contiguous assistant run after that turn is complete.
    // While the last turn is still streaming, keep assistant text inside
    // processing so later tool calls cannot yank already-visible text back into
    // the collapsed details.
    // Treat both assistant text AND error items as turn-ending finals. Errors
    // (quota/auth/server) are the user's primary signal that something went
    // wrong and must surface as full cards, not hide inside a collapsed steps
    // block alongside the tool history.
    // Main-chat report lines and side-chat origins are standalone cards too:
    // a report that lands after a reply must not pull that reply back into
    // the collapsed processing block.
    const isFinal = (g: GroupedStreamItem) =>
        g.type === 'single' &&
        (g.item.kind === 'assistant' ||
            g.item.kind === 'error' ||
            isStandaloneSystemItem(g.item));

    const turns: Turn[] = [];
    for (const [index, r] of raw.entries()) {
        const segments: TurnSegment[] = [];
        const isActiveStreamingTurn = streaming && index === raw.length - 1;
        let finalStart = r.items.length;

        if (!isActiveStreamingTurn) {
            while (finalStart > 0 && isFinal(r.items[finalStart - 1])) {
                finalStart--;
            }
        }

        const seg: TurnSegment = {
            processing: r.items.slice(0, finalStart),
            finals: r.items.slice(finalStart),
        };

        if (seg.processing.length > 0 || seg.finals.length > 0) {
            segments.push(seg);
        }
        turns.push({ user: r.user, segments });
    }

    return turns;
}

// turnHasFinals reports whether any segment in this turn has surfaced an
// assistant message. Used to gate user-message edit/reset affordances
// (only meaningful once the agent has responded) and split-label render
// conditions.
export function turnHasFinals(turn: Turn): boolean {
    for (const s of turn.segments) {
        if (s.finals.length > 0) return true;
    }
    return false;
}

// turnHasContent reports whether the turn produced any agent-side
// output (processing or finals) at all.
export function turnHasContent(turn: Turn): boolean {
    for (const s of turn.segments) {
        if (s.processing.length > 0 || s.finals.length > 0) return true;
    }
    return false;
}

// ── Model types ──

export interface ModelOption {
    id: string;
    name: string;
    executor: string;
}

export interface ExecutorGroup {
    name: string;
    available: boolean;
    models: ModelOption[];
    default: string;
}

export const FALLBACK_MODELS: ModelOption[] = [
    { id: 'sonnet', name: 'Claude', executor: 'claude' },
];

export const DEFAULT_MODEL = 'sonnet';

// ── Executor type helpers ──

export type ExecutorType = 'claude' | 'codex';

export function getExecutorType(model: string | undefined | null): ExecutorType {
    if (!model) return 'claude';
    // Strip reasoning effort suffix for composite IDs (e.g. "gpt-5.2-codex:xhigh")
    const base = model.split(':')[0].toLowerCase();
    if (
        base.startsWith('gpt') ||
        base.startsWith('codex') ||
        base.startsWith('o3') ||
        base.startsWith('o4')
    )
        return 'codex';
    return 'claude';
}

// ── Project registration ──

/**
 * registerProject creates a fully hydrated Project object from a directory path,
 * persists it, and returns it. Used by all "add project" flows (local open, Isoform clone, URL clone).
 */
export async function registerProject(
    path: string,
    model: string,
    overrides?: {
        name?: string;
        organization?: string;
        product?: string;
        isYansuProject?: boolean;
    },
): Promise<Project> {
    // 1. Detect if it's a Isoform project (unless overrides say so)
    const detected = overrides?.isYansuProject !== undefined
        ? null
        : await DetectProject(path);

    const dirName = path.split('/').pop() || path;
    const projectId = buildId();

    // 2. Create default workspace + session
    const defaultWs = createWorkspace('Default', true);
    const firstSession = createSession(model, defaultWs.id);
    defaultWs.sessions = [firstSession];
    defaultWs.activeSessionId = firstSession.localId;

    // 3. Get git info (best-effort)
    let gitInfo: GitInfo | undefined;
    try {
        const info = await GetGitInfo(path);
        if (info.isGitRepo) gitInfo = info as GitInfo;
    } catch { /* not a git repo */ }

    // 4. Get the next sort order (best-effort, fallback keeps backward compatibility).
    let sortOrder = 0;
    try {
        sortOrder = await NextProjectSortOrder();
    } catch { /* fallback to 0 */ }

    // 5. Build project
    const project: Project = {
        id: projectId,
        name: overrides?.name || detected?.project_name || dirName,
        path,
        organization: overrides?.organization || detected?.organization,
        product: overrides?.product || detected?.product,
        isYansuProject: overrides?.isYansuProject ?? (detected?.is_yansu_project || false),
        gitInfo,
        workspaces: [defaultWs],
        activeWorkspaceId: defaultWs.id,
        activeSessionId: firstSession.localId,
        sortOrder,
    };

    // 6. Persist
    await SaveProjectState(toProjectData(project) as any);
    await SaveActiveProjectID(projectId);

    return project;
}
