import type { StreamItem } from '@/types';
import { getMainChatProactive, getMainChatStatus, MAIN_CHAT_OBSERVATION_PAUSE_TOOL } from '@/lib/stream-helpers';

/** What the main chat shows (RC 2 D1): everything, or the main chat filtered
 *  to one work topic. Waiting cards stay in the stream; Tasks › Needs you is
 *  the one list of them (chat-review-v1.0.1 K10). A filter only changes
 *  what is shown; the main chat stays one conversation. */
export type MainChatFilter =
    | { kind: 'all' }
    | { kind: 'topic'; topicId: string };

export const MAIN_CHAT_FILTER_ALL: MainChatFilter = { kind: 'all' };

/** Reads a filter defensively: a retired kind such as the old Waiting filter,
 *  or anything unknown, opens as All. */
export function normalizeMainChatFilter(raw: unknown): MainChatFilter {
    const wire = (raw ?? {}) as { kind?: unknown; topicId?: unknown };
    if (wire.kind === 'topic' && typeof wire.topicId === 'string' && wire.topicId) return { kind: 'topic', topicId: wire.topicId };
    return MAIN_CHAT_FILTER_ALL;
}

/** Today's numbers for the main chat (GetMainChatOverview), read defensively:
 *  a missing field stays unavailable. */
export interface MainChatOverview {
    sessionId: string;
    day: string;
    conversations: number | null;
    waiting: number | null;
    finished: number | null;
    routine: number | null;
    hideRoutine: boolean;
}

export function toMainChatOverview(raw: unknown): MainChatOverview {
    const wire = (raw ?? {}) as Partial<Record<keyof MainChatOverview, unknown>>;
    const count = (value: unknown) => (typeof value === 'number' && Number.isFinite(value) ? Math.max(0, Math.floor(value)) : null);
    return {
        sessionId: typeof wire.sessionId === 'string' ? wire.sessionId : '',
        day: typeof wire.day === 'string' ? wire.day : '',
        conversations: count(wire.conversations),
        waiting: count(wire.waiting),
        finished: count(wire.finished),
        routine: count(wire.routine),
        hideRoutine: wire.hideRoutine === true,
    };
}

/** A card action changed what waits on the user; the main chat's numbers
 *  read again. */
export const MAIN_CHAT_VIEW_CHANGED = 'isoform:main-chat-view-changed';

export function notifyMainChatViewChanged() {
    window.dispatchEvent(new Event(MAIN_CHAT_VIEW_CHANGED));
}

/** "YYYY-MM-DD" in this computer's time zone, the same form as a row's
 *  localDay. */
export function localDayKey(date: Date): string {
    const pad = (value: number) => String(value).padStart(2, '0');
    return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}`;
}

function dayDate(day: string): Date | null {
    const match = /^(\d{4})-(\d{2})-(\d{2})$/.exec(day);
    if (!match) return null;
    const date = new Date(Number(match[1]), Number(match[2]) - 1, Number(match[3]));
    return Number.isNaN(date.getTime()) ? null : date;
}

/** How a day divider names a day: today, yesterday, or the date. */
export type DayName = { kind: 'today' } | { kind: 'yesterday' } | { kind: 'date'; text: string };

export function dayName(day: string, today: string, language: string | undefined): DayName {
    if (day === today) return { kind: 'today' };
    const date = dayDate(day);
    const now = dayDate(today);
    if (date && now) {
        const yesterday = new Date(now.getFullYear(), now.getMonth(), now.getDate() - 1);
        if (localDayKey(yesterday) === day) return { kind: 'yesterday' };
        const sameYear = date.getFullYear() === now.getFullYear();
        return {
            kind: 'date',
            text: date.toLocaleDateString(language, sameYear
                ? { weekday: 'short', month: 'short', day: 'numeric' }
                : { year: 'numeric', month: 'short', day: 'numeric' }),
        };
    }
    return { kind: 'date', text: day };
}

/**
 * A thread update the computer withdrew: its thread moved on (a newer
 * outcome, new activity, the thread past retention, or capture paused), so
 * the card can only say "Thread update unavailable." It says nothing the user
 * can act on and is not shown (polish sweep 2026-10-27: four of them in a
 * row read as noise). A newer update for the same thread arrives as its own
 * card.
 */
/** A card the computer keeps out of Chat without a removed source (for example a
 *  first-week card about a person): its day stays, nothing is said about it. */
export function isWithheldCard(item: StreamItem): boolean {
    return getMainChatProactive(item)?.kind === 'withheld';
}

export function isWithdrawnThreadUpdate(item: StreamItem): boolean {
    const card = getMainChatProactive(item);
    if (!card) return false;
    if (card.kind === 'digest') return Boolean(card.items?.length) && card.items!.every((child) => child.kind === 'thread_update_unavailable');
    return card.kind === 'thread_update_unavailable';
}

/** How many updates a routine row holds: each entry of a status row counts
 *  once, like the overview's routine count. */
export function routineUpdateCount(item: StreamItem): number {
    const status = getMainChatStatus(item);
    return status ? status.entries.length : 1;
}

/** One local day of the main chat: its routine rows (folded into one row)
 *  and everything else, in the order the chat holds them. `withdrawn` counts
 *  the thread updates the computer withdrew: they are not shown, but their
 *  day still is (R63). */
export interface MainChatDay {
    day: string;
    routine: StreamItem[];
    routineCount: number;
    items: StreamItem[];
    /** Updates removed because their source is gone: the day keeps a quiet line. */
    withdrawn: number;
    /** Cards held back from Chat with no removed source: the day keeps its divider only. */
    withheld: number;
}

/**
 * Splits the main chat into local days, oldest first. A row keeps the day the
 * computer gave it; a row without one (words sent or streamed since the page
 * was read) is today's. Routine rows (rowClass "routine") leave the flow for
 * their own day's fold, wherever they sit (an updated status row moves to the
 * end of the chat but keeps its day), even a day with nothing else. A
 * conversation row never reopens an earlier day, so turns stay in order;
 * cards keep their own day even if updated later. Nothing is merged. A thread
 * update the computer withdrew (see isWithdrawnThreadUpdate) is not shown, but
 * its day is kept (counted in `withdrawn`), so a day never vanishes with it (R63).
 */
export function buildMainChatDays(items: readonly StreamItem[], today: string): MainChatDay[] {
    const byDay = new Map<string, MainChatDay>();
    const section = (day: string) => {
        let found = byDay.get(day);
        if (!found) {
            found = { day, routine: [], routineCount: 0, items: [], withdrawn: 0, withheld: 0 };
            byDay.set(day, found);
        }
        return found;
    };
    let flowDay = '';
    for (const item of items) {
        const day = item.localDay || today;
        if (isWithheldCard(item)) {
            section(day).withheld += 1;
            continue;
        }
        if (isWithdrawnThreadUpdate(item)) {
            section(day).withdrawn += 1;
            continue;
        }
        if (item.rowClass === 'routine') {
            const target = section(day);
            target.routine.push(item);
            target.routineCount += routineUpdateCount(item);
            continue;
        }
        if (item.rowClass === 'conversation' || item.kind === 'user' || (item.kind === 'assistant' && !item.toolName)) {
            if (day > flowDay) flowDay = day;
            section(flowDay).items.push(item);
        } else {
            section(day).items.push(item);
        }
    }
    return [...byDay.values()].sort((a, b) => (a.day < b.day ? -1 : a.day > b.day ? 1 : 0));
}

/**
 * The main chat's rows as its stream shows them, wherever it shows: the Chat
 * page and Today's panel fold this one list with buildMainChatDays, and the
 * phone reads the same row class and day (chat-review v1.0.3 #2). Only run
 * bookkeeping is dropped.
 */
export function mainChatStreamRows(items: readonly StreamItem[]): StreamItem[] {
    const filtered = items.filter((item) => {
        if (item.kind === 'result') return false;
        // A pause the person chose is not an update once it ends; one still
        // going stays its card with Resume (chat-review v1.0.10 H2).
        if (item.toolName === MAIN_CHAT_OBSERVATION_PAUSE_TOOL && endedObservationPause(item.toolInput)) return false;
        if (item.kind === 'system') {
            const content = item.content ?? '';
            if (content.includes('Run completed') || content.includes('Run created')) return false;
        }
        return true;
    });
    // A delivered status and a retained card can be two projections of the
    // same moment. Keep the status line once, where it first appeared, and
    // give it the end any projection recorded (chat-review v1.0.9 #2); do not
    // merge unrelated pauses.
    type PauseValue = { momentId?: string; captureStartAt?: number; captureEndAt?: number; captureSource?: string; captureCause?: string };
    // One stop is one event, whichever sources it took down (H1).
    const pauseKeys = (value: PauseValue) => [value.momentId, value.captureStartAt ? `capture:${value.captureStartAt}` : undefined,
        value.captureStartAt && (value.captureSource === 'microphone' || value.captureSource === 'system_audio') ? `listening:${Math.floor(value.captureStartAt / 60_000)}` : undefined]
        .filter((key): key is string => !!key);
    // Only a stop nobody chose is an update, and it states its cause (H2).
    // Sleep, a locked screen, the person's own switch, or an old notice with
    // no recorded cause shows nothing.
    const unchosen = (value: PauseValue) => value.captureCause === 'permission' || value.captureCause === 'stopped';
    const statusMoments = new Set<string>();
    const pauseEnds = new Map<string, number>();
    const notePause = (value: PauseValue) => {
        for (const key of pauseKeys(value)) if (value.captureEndAt && !pauseEnds.has(key)) pauseEnds.set(key, value.captureEndAt);
    };
    const pauseEnd = (value: PauseValue) => value.captureEndAt ?? pauseKeys(value).map((key) => pauseEnds.get(key)).find((end) => end !== undefined);
    for (const item of filtered) {
        const status = getMainChatStatus(item);
        for (const entry of status?.entries ?? []) {
            if (entry.kind !== 'capture_paused' || !unchosen(entry)) continue;
            for (const key of pauseKeys(entry)) statusMoments.add(key);
            notePause(entry);
        }
        const card = getMainChatProactive(item);
        if (card?.kind === 'capture_paused') notePause(card);
    }
    const seenStatusMoments = new Set<string>();
    const seenCards = new Set<string>();
    return filtered.flatMap((item) => {
        const card = getMainChatProactive(item);
        if (card?.kind === 'capture_paused' && !unchosen(card)) return [];
        if (card?.kind === 'capture_paused' && pauseKeys(card).some((key) => statusMoments.has(key))) return [];
        if (card?.kind === 'capture_paused' && pauseKeys(card).length > 0) {
            const keys = pauseKeys(card);
            if (keys.some((key) => seenCards.has(key))) return [];
            for (const key of keys) seenCards.add(key);
            const end = pauseEnd(card);
            if (end && !card.captureEndAt && item.toolInput && typeof item.toolInput === 'object') item = { ...item, toolInput: { ...(item.toolInput as object), captureEndAt: end } };
        }
        const status = getMainChatStatus(item);
        if (!status) return card?.kind === 'capture_paused' && card.captureStartAt
            ? [{ ...item, localDay: localDayKey(new Date(card.captureStartAt)) }]
            : [item];
        let ended = false;
        const entries = status.entries.filter((entry) => {
            const keys = pauseKeys(entry);
            if (entry.kind === 'capture_paused' && !unchosen(entry)) return false;
            if (entry.kind !== 'capture_paused' || keys.length === 0) return true;
            if (keys.some((key) => seenStatusMoments.has(key))) return false;
            for (const key of keys) seenStatusMoments.add(key);
            return true;
        }).map((entry) => {
            const end = entry.kind === 'capture_paused' && !entry.captureEndAt ? pauseEnd(entry) : undefined;
            if (!end) return entry;
            ended = true;
            return { ...entry, captureEndAt: end };
        });
        if (entries.length === 0) return [];
        const grouped = new Map<string, typeof entries>();
        for (const entry of entries) {
            const day = entry.kind === 'capture_paused' && entry.captureStartAt ? localDayKey(new Date(entry.captureStartAt)) : item.localDay ?? '';
            const group = grouped.get(day) ?? [];
            group.push(entry);
            grouped.set(day, group);
        }
        if (!ended && grouped.size === 1 && entries.length === status.entries.length && grouped.has(item.localDay ?? '')) return [item];
        return [...grouped].map(([day, group]) => ({ ...item, id: grouped.size === 1 ? item.id : `${item.id}:${day}`, localDay: day || undefined, toolInput: { ...status, entries: group } }));
    });
}

function endedObservationPause(input: unknown): boolean {
    if (!input || typeof input !== 'object') return false;
    const { endAt, resumedUnknown } = input as { endAt?: unknown; resumedUnknown?: unknown };
    return (typeof endAt === 'number' && endAt > 0) || resumedUnknown === true;
}

/** The answers of the main chat's latest turn: assistant rows with words
 *  after the last message the user sent. Prepared chips show only there. */
export function latestAnswerIds(items: readonly StreamItem[]): string[] {
    const ids: string[] = [];
    for (let index = items.length - 1; index >= 0; index--) {
        const item = items[index];
        if (item.kind === 'user') break;
        if (item.kind === 'assistant' && item.content?.trim() && !item.id.startsWith('__')) ids.push(item.id);
    }
    return ids.reverse();
}

/** Questions remain actionable in older loaded turns; ordinary suggestions stay
 *  limited to the latest turn. The backend validates each persisted row. */
export function answerChipIds(items: readonly StreamItem[]): string[] {
    const latest = new Set(latestAnswerIds(items));
    return items.filter((item) => item.kind === 'assistant' && !item.toolName && !item.id.startsWith('__') && (
        latest.has(item.id) || /[?？]["'”’」』*_\s]*$/.test(item.content ?? '')
    )).map((item) => item.id);
}

/**
 * Chat never speaks first (phone-chat v1.0.0 rule 3, computer-chat v1.0.0):
 * Isoform's own updates stay out of the thread, so it opens on the person's
 * conversation and the field. `shown` is the rollback switch: true brings back
 * the day folds, the "removed because their source is gone" lines and the
 * Hide or Show updates switch.
 */
export const chatThreadUpdates = { shown: false };
