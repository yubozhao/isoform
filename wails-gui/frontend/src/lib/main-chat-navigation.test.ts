import { describe, expect, it, vi } from 'vitest';
import type { StreamItem, StreamItemData } from '@/types';
import { loadMainChatItemUpdates, mainChatTargetOpens, openMainChatTarget, TODAY_BRIEF_STATUS_TARGET, type MainChatTargetHandlers } from './main-chat-navigation';
import { MAIN_CHAT_STATUS_TOOL } from './stream-helpers';

function handlers(): MainChatTargetHandlers {
    return {
        openSession: vi.fn(), openHandoff: vi.fn(), openKnowledge: vi.fn(), openMemory: vi.fn(), openAutomation: vi.fn(), openToday: vi.fn(),
        openSettings: vi.fn(), openPlaybook: vi.fn(), openCalendar: vi.fn(), openPeople: vi.fn(), openThread: vi.fn(), openExternalURL: vi.fn().mockResolvedValue(undefined),
    };
}

describe('main chat App routing', () => {
    it('treats only a saved destination as openable', () => {
        for (const target of [{ kind: 'handoff', id: 'w' }, { kind: 'thread', id: 't' }, { kind: 'calendar', id: 'e' }, { kind: 'mail' }, { kind: 'knowledge', id: 'f' }, { kind: 'today' }] as const) {
            expect(mainChatTargetOpens(target)).toBe(true);
        }
        for (const target of [undefined, { kind: 'none' }, { kind: '' }, { kind: 'handoff' }, { kind: 'thread' }, { kind: 'session' }, { kind: 'playbook' }, { kind: 'memory' }, { kind: 'unknown' }] as never[]) {
            expect(mainChatTargetOpens(target)).toBe(false);
        }
    });

    it('opens the requested brief on Today', async () => {
        const h = handlers();
        await openMainChatTarget({ kind: 'today', id: '2026-10-23-evening' }, h);
        expect(h.openToday).toHaveBeenCalledWith('2026-10-23-evening');
    });

    it("opens Today on the brief's state for a missed brief, never as a brief id", async () => {
        const h = handlers();
        await openMainChatTarget({ kind: 'today', id: TODAY_BRIEF_STATUS_TARGET }, h);
        expect(h.openToday).toHaveBeenCalledWith(undefined, 'status');
    });

    it('opens capture controls in Observation rather than notification settings', async () => {
        const h = handlers();
        await openMainChatTarget({ kind: 'settings', id: 'activity' }, h);
        expect(h.openSettings).toHaveBeenCalledTimes(1);
        expect(h.openSettings).toHaveBeenCalledWith('activity');
    });

    it('opens the exact communication belief and ignores an incomplete memory target', async () => {
        const h = handlers();
        await openMainChatTarget({ kind: 'memory', id: 'belief-1' }, h);
        await openMainChatTarget({ kind: 'memory' }, h);
        expect(h.openMemory).toHaveBeenCalledOnce();
        expect(h.openMemory).toHaveBeenCalledWith('belief-1');
        expect(h.openKnowledge).not.toHaveBeenCalled();
    });

    it('opens People and Calendar as lists without pretending to select a record', async () => {
        const h = handlers();
        await openMainChatTarget({ kind: 'person', id: 'person-1' }, h);
        await openMainChatTarget({ kind: 'calendar', id: 'event-1' }, h);
        expect(h.openPeople).toHaveBeenCalledTimes(1);
        expect(h.openPeople).toHaveBeenCalledWith();
        expect(h.openCalendar).toHaveBeenCalledTimes(1);
        expect(h.openCalendar).toHaveBeenCalledWith();
        expect(h.openToday).not.toHaveBeenCalled();
    });

    it('opens a thread target on the Activity page and ignores an empty id', async () => {
        const h = handlers();
        await openMainChatTarget({ kind: 'thread', id: 'thread-1' }, h);
        await openMainChatTarget({ kind: 'thread' }, h);
        expect(h.openThread).toHaveBeenCalledOnce();
        expect(h.openThread).toHaveBeenCalledWith('thread-1');
        expect(h.openToday).not.toHaveBeenCalled();
    });

    it('opens a watch source on its conversation and day, and the Watch tab without a day', async () => {
        const h = { ...handlers(), openWatch: vi.fn(), openActivityConversation: vi.fn() };
        await openMainChatTarget({ kind: 'watch', id: 'conv-1', day: '2026-11-19' }, h);
        expect(h.openActivityConversation).toHaveBeenCalledWith('2026-11-19', 'conv-1');
        expect(h.openWatch).not.toHaveBeenCalled();
        await openMainChatTarget({ kind: 'watch', id: 'conv-1' }, h);
        expect(h.openWatch).toHaveBeenCalledOnce();
    });

    it('opens only the installed Mail app and never uses message data as a URL', async () => {
        const h = handlers();
        await openMainChatTarget({ kind: 'mail', id: 'mailto:someone@example.com?body=private', runId: 'https://untrusted.invalid' }, h);
        expect(h.openExternalURL).toHaveBeenCalledTimes(1);
        expect(h.openExternalURL).toHaveBeenCalledWith('/System/Applications/Mail.app');
        expect(h.openToday).not.toHaveBeenCalled();
    });

    it('propagates an app launch failure to App localized error feedback', async () => {
        const h = handlers();
        vi.mocked(h.openExternalURL).mockRejectedValue(new Error('launch failed'));
        await expect(openMainChatTarget({ kind: 'mail' }, h)).rejects.toThrow('launch failed');
    });

    it('preserves the existing report destinations and the Upcoming reset', async () => {
        const h = handlers();
        await openMainChatTarget({ kind: 'session', id: 'chat-1' }, h);
        await openMainChatTarget({ kind: 'handoff', id: 'work-1' }, h);
        await openMainChatTarget({ kind: 'knowledge', id: 'note-1' }, h);
        await openMainChatTarget({ kind: 'automation', id: 'job-1' }, h);
        await openMainChatTarget({ kind: 'upcoming' }, h);
        await openMainChatTarget({ kind: 'playbook', id: 'book-1' }, h);
        expect(h.openSession).toHaveBeenCalledWith('chat-1');
        expect(h.openHandoff).toHaveBeenCalledWith('work-1');
        expect(h.openKnowledge).toHaveBeenCalledWith('note-1');
        expect(h.openAutomation).toHaveBeenNthCalledWith(1, 'job-1');
        expect(h.openAutomation).toHaveBeenNthCalledWith(2, null);
        expect(h.openPlaybook).toHaveBeenCalledWith('book-1');
    });
});

describe('main chat live item refresh', () => {
    const existing: StreamItem[] = [{ id: 'status-1', kind: 'system', toolName: MAIN_CHAT_STATUS_TOOL, sortOrder: 4 }];
    const changed: StreamItemData = { id: 'status-1', kind: 'system', toolName: MAIN_CHAT_STATUS_TOOL, sortOrder: 250, content: 'Updated status.' };

    it('fetches a loaded row outside the latest page at its new event position', async () => {
        const loadPage = vi.fn().mockResolvedValueOnce({ items: [] }).mockResolvedValueOnce({ items: [changed] });
        const fresh = await loadMainChatItemUpdates('main-1', existing, { itemId: 'status-1', sortOrder: 250 }, loadPage);
        expect(loadPage).toHaveBeenNthCalledWith(1, 'main-1', -1, 50);
        expect(loadPage).toHaveBeenNthCalledWith(2, 'main-1', 251, 1);
        expect(fresh).toHaveLength(1);
        expect(fresh[0]).toMatchObject({ id: 'status-1', sortOrder: 250, content: 'Updated status.' });
    });

    it('uses the loaded position for older events that lack sortOrder', async () => {
        const loadPage = vi.fn().mockResolvedValueOnce({ items: [] }).mockResolvedValueOnce({ items: [{ ...changed, sortOrder: 4 }] });
        await loadMainChatItemUpdates('main-1', existing, { itemId: 'status-1' }, loadPage);
        expect(loadPage).toHaveBeenNthCalledWith(2, 'main-1', 5, 1);
    });

    it('does not fetch the same row twice or load an unseen old conversation', async () => {
        const loadPage = vi.fn().mockResolvedValue({ items: [changed] });
        await loadMainChatItemUpdates('main-1', existing, { itemId: 'status-1', sortOrder: 250 }, loadPage);
        expect(loadPage).toHaveBeenCalledTimes(1);
        loadPage.mockClear().mockResolvedValue({ items: [] });
        expect(await loadMainChatItemUpdates('main-1', [], { itemId: 'unloaded', sortOrder: 2 }, loadPage)).toEqual([]);
        expect(loadPage).toHaveBeenCalledTimes(1);
    });
});
