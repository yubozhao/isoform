import { describe, expect, it, afterEach, beforeEach } from 'vitest';
import { chatThreadUpdates } from '@/lib/main-chat-view';
import { renderHook } from '@testing-library/react';
import type { StreamItem } from '@/types';
import type { MainChatStreamView } from '@/components/main-chat/main-chat-view-context';
import { useStreamListModel, dayRowsVisibility } from '@/components/grouped-stream-list';
import { buildMainChatDays, mainChatStreamRows } from './main-chat-view';

// These cover the rollback switch: Isoform's updates back in Chat's thread.
beforeEach(() => { chatThreadUpdates.shown = true; });
afterEach(() => { chatThreadUpdates.shown = false; });


// Bo's first page on 2026-11-05 (sessions.db, newest 50 rows): 44 system cards,
// six of them from 2026-11-04. Every card is a main_chat_proactive system row.
const TODAY = '2026-11-05';

function card(sortOrder: number, localDay: string, rowClass: StreamItem['rowClass']): StreamItem {
    return {
        id: `card-${sortOrder}`,
        kind: 'system',
        content: `Card ${sortOrder}`,
        toolName: 'main_chat_proactive',
        toolInput: { momentId: `moment-${sortOrder}`, kind: 'decision_due', headline: `Headline ${sortOrder}`, body: 'Body', state: 'pending', actionIds: [], actionLabels: {}, evidence: [], target: { kind: 'today', id: '' } },
        sortOrder,
        localDay,
        rowClass,
    } as unknown as StreamItem;
}

function firstPage(rowClass: StreamItem['rowClass']): StreamItem[] {
    const items: StreamItem[] = [];
    for (let i = 0; i < 38; i++) items.push(card(300 + i, i < 20 ? '2026-10-31' : '2026-11-03', rowClass));
    for (let i = 0; i < 6; i++) items.push(card(400 + i, '2026-11-04', rowClass));
    return items;
}

describe('the first page keeps the newest day', () => {
    it('shows the Nov 4 cards when they are conversation rows', () => {
        const days = buildMainChatDays(mainChatStreamRows(firstPage('other')), TODAY);
        const newest = days.find((day) => day.day === '2026-11-04');
        expect(newest).toBeDefined();
        expect(newest!.items.length + newest!.routine.length).toBe(6);
    });

    it('keeps the Nov 4 day when its cards are folded routine rows', () => {
        const days = buildMainChatDays(mainChatStreamRows(firstPage('routine')), TODAY);
        const newest = days.find((day) => day.day === '2026-11-04');
        expect(newest).toBeDefined();
        expect(newest!.routineCount).toBeGreaterThan(0);
    });
});

/** The Go projection withdraws a thread update whose outcome no longer
 *  matches its card, and a person-only first week card (R63): the kind
 *  arrives as thread_update_unavailable, the row is not shown. */
function withdrawnCard(sortOrder: number, localDay: string): StreamItem {
    return {
        ...card(sortOrder, localDay, 'other'),
        toolInput: { momentId: `moment-${sortOrder}`, kind: 'thread_update_unavailable', headline: '', body: '', state: 'pending', actionIds: [], actionLabels: {}, evidence: [], target: { kind: 'none', id: '' } },
    } as unknown as StreamItem;
}

/** Bo's first page as the Go projection hands it over on 2026-11-05: 38 older
 *  cards, then six Nov 4 cards that were all withdrawn. */
function withdrawnNewestPage(): StreamItem[] {
    const items: StreamItem[] = [];
    for (let i = 0; i < 38; i++) items.push(card(300 + i, i < 20 ? '2026-10-31' : '2026-11-03', 'other'));
    for (let i = 0; i < 6; i++) items.push(withdrawnCard(400 + i, '2026-11-04'));
    return items;
}

describe('a day whose updates are withdrawn keeps its divider (R63)', () => {
    it('keeps the Nov 4 day with a count of withdrawn updates and no visible rows', () => {
        const days = buildMainChatDays(mainChatStreamRows(withdrawnNewestPage()), TODAY);
        const newest = days.find((day) => day.day === '2026-11-04');
        expect(newest).toBeDefined();
        expect(newest!.withdrawn).toBe(6);
        expect(newest!.items.length + newest!.routine.length).toBe(0);
        expect(dayRowsVisibility(newest!, false).divider).toBe(true);
    });

    it('renders the Nov 4 divider in the stream model', () => {
        const view = { surface: 'chat', today: TODAY, hideRoutine: false, topicNames: new Map(), chips: {} } as unknown as MainChatStreamView;
        const { result } = renderHook(() => useStreamListModel(withdrawnNewestPage(), false, 0, view, false));
        const dividers = result.current.rows.filter((row) => row.type === 'day-divider').map((row) => (row as { day: string }).day);
        expect(dividers).toContain('2026-11-04');
        expect(dividers).toContain('2026-11-03');
    });

    it('keeps a routine-only newest day with its fold line', () => {
        const items: StreamItem[] = [];
        for (let i = 0; i < 38; i++) items.push(card(300 + i, i < 20 ? '2026-10-31' : '2026-11-03', 'other'));
        for (let i = 0; i < 6; i++) items.push(card(400 + i, '2026-11-04', 'routine'));
        const view = { surface: 'chat', today: TODAY, hideRoutine: false, topicNames: new Map(), chips: {} } as unknown as MainChatStreamView;
        const { result } = renderHook(() => useStreamListModel(items, false, 0, view, false));
        const rows = result.current.rows;
        expect(rows.some((row) => row.type === 'day-divider' && row.day === '2026-11-04')).toBe(true);
        expect(rows.some((row) => row.type === 'day-fold' && row.day === '2026-11-04' && row.routineCount === 6)).toBe(true);
    });
});

/** A first-week card about a person, held back by the projection (chat v1.0.12). */
function withheldCard(sortOrder: number, localDay: string): StreamItem {
    return {
        ...card(sortOrder, localDay, 'other'),
        toolInput: { momentId: `moment-${sortOrder}`, kind: 'withheld', headline: '', body: '', state: 'expired', actionIds: [], actionLabels: {}, evidence: [], target: { kind: 'none', id: '' } },
    } as unknown as StreamItem;
}

describe('removed and held-back updates (chat v1.0.12)', () => {
    it('keeps a day with only a held-back card as a divider, with no removed line', () => {
        const items: StreamItem[] = [];
        for (let i = 0; i < 38; i++) items.push(card(300 + i, i < 20 ? '2026-10-31' : '2026-11-03', 'other'));
        items.push(withheldCard(406, '2026-11-04'));
        const days = buildMainChatDays(mainChatStreamRows(items), TODAY);
        const newest = days.find((day) => day.day === '2026-11-04');
        expect(newest).toBeDefined();
        expect(newest!.withheld).toBe(1);
        expect(newest!.withdrawn).toBe(0);
        expect(dayRowsVisibility(newest!, false).divider).toBe(true);
    });

    it('says how many updates were removed under the day, singular and plural', () => {
        const view = { surface: 'chat', today: TODAY, hideRoutine: false, topicNames: new Map(), chips: {} } as unknown as MainChatStreamView;
        const one = renderHook(() => useStreamListModel(withdrawnNewestPage().slice(0, 38).concat([withdrawnCard(400, '2026-11-04')]), false, 0, view, false));
        expect(one.result.current.rows).toContainEqual(expect.objectContaining({ type: 'day-removed', day: '2026-11-04', count: 1 }));
        const six = renderHook(() => useStreamListModel(withdrawnNewestPage(), false, 0, view, false));
        expect(six.result.current.rows).toContainEqual(expect.objectContaining({ type: 'day-removed', day: '2026-11-04', count: 6 }));
    });
});
