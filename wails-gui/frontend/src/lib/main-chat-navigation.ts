import type { StreamItem, StreamItemData } from '@/types';
import { fromStreamItemData, type MainChatReportTarget } from './stream-helpers';
import { CHAT_FIRST_PAGE_SIZE } from './chat-page-size';

/** "status": Today scrolls to the brief's state and starts the brief that is
 *  on its way, for a missed-brief notice's Show. */
export type TodayBriefFocus = 'status';

/** The Today target a missed-brief notice's Show opens (TodayBriefFocus). */
export const TODAY_BRIEF_STATUS_TARGET = 'brief-status';

export interface MainChatTargetHandlers {
    openSession: (id: string) => void;
    openHandoff: (id: string) => void;
    openKnowledge: (id?: string) => void;
    openMemory: (id: string) => void;
    openAutomation: (id?: string | null) => void;
    openToday: (briefId?: string, focus?: TodayBriefFocus) => void;
    openSettings: (topic?: 'activity' | 'notifications') => void;
    openPlaybook: (id: string) => void;
    openCalendar: () => void;
    openPeople: () => void;
    openThread: (id: string) => void;
    /** Opens Activity on this session (a chat source's Activity session or call). */
    openActivitySession?: (id: string) => void;
    /** Opens Activity on the watch's conversations (a chat source from the watch). */
    openWatch?: () => void;
    /** Opens Activity › Watch on a day with one conversation open. */
    openActivityConversation?: (day: string, conversationId: string) => void;
    openTasksUpNext?: () => void;
    openExternalURL: (url: string) => Promise<void>;
}

/** Whether a saved target is a real destination that openMainChatTarget
 *  opens. A line without one shows no Show (chat-review v1.0.7). */
export function mainChatTargetOpens(target: MainChatReportTarget | undefined): boolean {
    switch (target?.kind) {
        case 'session':
        case 'handoff':
        case 'memory':
        case 'playbook':
        case 'thread':
        case 'activity':
        case 'watch':
            return Boolean(target.id);
        case 'knowledge':
        case 'automation':
        case 'today':
        case 'suggestion':
        case 'settings':
        case 'calendar':
        case 'person':
        case 'mail':
        case 'upcoming':
        case 'tasks':
            return true;
        default:
            return false;
    }
}

/** Shared by App's main-chat report, proactive action and evidence links.
 *  Calendar targets open the Activity page (Calendar merged into it); People
 *  opens its list — neither surface accepts an item id. */
export async function openMainChatTarget(target: MainChatReportTarget, handlers: MainChatTargetHandlers): Promise<void> {
    switch (target.kind) {
        case 'session':
            if (target.id) handlers.openSession(target.id);
            return;
        case 'handoff':
            if (target.id) handlers.openHandoff(target.id);
            return;
        case 'knowledge':
            handlers.openKnowledge(target.id);
            return;
        case 'memory':
            if (target.id) handlers.openMemory(target.id);
            return;
        case 'automation':
            handlers.openAutomation(target.id);
            return;
        case 'today':
            if (target.id === TODAY_BRIEF_STATUS_TARGET) handlers.openToday(undefined, 'status');
            else handlers.openToday(target.id);
            return;
        case 'suggestion':
            handlers.openToday();
            return;
        case 'settings':
            handlers.openSettings(target.id === 'activity' ? 'activity' : target.id === 'notifications' ? 'notifications' : undefined);
            return;
        case 'playbook':
            if (target.id) handlers.openPlaybook(target.id);
            return;
        case 'calendar':
            handlers.openCalendar();
            return;
        case 'person':
            handlers.openPeople();
            return;
        case 'thread':
            if (target.id) handlers.openThread(target.id);
            return;
        case 'activity':
            if (target.id) handlers.openActivitySession?.(target.id);
            return;
        case 'watch':
            // That conversation on its day (Activity's openActivityConversation);
            // without a day or the handler, the Watch tab.
            if (target.id && target.day && handlers.openActivityConversation) handlers.openActivityConversation(target.day, target.id);
            else handlers.openWatch?.();
            return;
        case 'mail':
            // Open the installed app only. Message ids and text are never
            // interpreted as a URL, thread link or compose request.
            await handlers.openExternalURL('/System/Applications/Mail.app');
            return;
        case 'upcoming':
            handlers.openAutomation(null);
            return;
        case 'tasks':
            handlers.openTasksUpNext?.();
            return;
        default:
            return;
    }
}

export interface MainChatItemsChanged {
    sessionId?: string;
    itemId?: string;
    sortOrder?: number;
}

type LoadItemPage = (sessionId: string, cursor: number, limit: number) => Promise<{ items?: StreamItemData[] }>;

/** Refreshes the live tail plus an updated row already loaded farther back.
 *  A folded status can move, so its event's new order takes precedence over
 *  the old position. This never pages in unloaded older conversation. */
export async function loadMainChatItemUpdates(
    sessionId: string,
    existing: StreamItem[],
    event: MainChatItemsChanged | null,
    loadPage: LoadItemPage,
): Promise<StreamItem[]> {
    const page = await loadPage(sessionId, -1, CHAT_FIRST_PAGE_SIZE);
    const fresh = (page.items ?? []).map(fromStreamItemData);
    const changedItem = existing.find((item) => item.id === event?.itemId);
    const changedOrder = event?.sortOrder ?? changedItem?.sortOrder;
    if (changedItem && typeof changedOrder === 'number' && Number.isFinite(changedOrder) && !fresh.some((item) => item.id === changedItem.id)) {
        const changedPage = await loadPage(sessionId, changedOrder + 1, 1);
        const changed = (changedPage.items ?? []).find((item) => item.id === changedItem.id);
        if (changed) fresh.push(fromStreamItemData(changed));
    }
    return fresh;
}

/** The place a folded status line is about, from its kind and saved target. */
export function statusEntryPlace(entry: { kind: string; target: { kind: string } }): string | null {
    const { kind } = entry;
    if (kind === 'capture_paused' || kind.startsWith('observ')) return 'observing';
    if (kind === 'brief_ready' || kind === 'brief_missed' || kind === 'weekly_recap') return 'brief';
    if (entry.target.kind === 'calendar' || kind.startsWith('meeting_') || kind === 'calendar_conflict') return 'calendar';
    if (entry.target.kind === 'mail' || kind.startsWith('mail_') || kind === 'waiting_on_reply') return 'mail';
    if (kind.startsWith('slack_')) return 'slack';
    if (entry.target.kind === 'automation' || entry.target.kind === 'handoff' || entry.target.kind === 'tasks'
        || kind.startsWith('schedule_') || kind === 'work_finished' || kind === 'work_blocked' || kind === 'playbook_result') return 'tasks';
    return null;
}
