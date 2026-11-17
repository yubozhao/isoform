import { describe, expect, it, vi, afterEach, beforeEach } from 'vitest';
import { chatThreadUpdates } from '@/lib/main-chat-view';
import { renderHook } from '@testing-library/react';
import { useStreamListModel } from '@/components/grouped-stream-list';
import { panelRows } from '@/hooks/useMainChatPanel';
import { buildMainChatDays } from '@/lib/main-chat-view';
import { MAIN_CHAT_PROACTIVE_TOOL, MAIN_CHAT_REPORT_TOOL, MAIN_CHAT_STATUS_TOOL } from '@/lib/stream-helpers';
import type { GroupedStreamItem } from '@/lib/stream-helpers';
import type { StreamItem } from '@/types';

// These cover the rollback switch: Isoform's updates back in Chat's thread.
beforeEach(() => { chatThreadUpdates.shown = true; });
afterEach(() => { chatThreadUpdates.shown = false; });


vi.mock('@/lib/wails-shim-app', () => ({}));

const status = (id: string, localDay: string): StreamItem => ({
    id, kind: 'system', toolName: MAIN_CHAT_STATUS_TOOL, rowClass: 'routine', localDay,
    toolInput: { period: 'day', entries: [{ id: `${id}-1`, kind: 'observing', text: 'Watched Mail' }, { id: `${id}-2`, kind: 'observing', text: 'Watched Slack' }] },
});
const report = (id: string, localDay: string, rowClass: StreamItem['rowClass'], source: string): StreamItem => ({
    id, kind: 'system', toolName: MAIN_CHAT_REPORT_TOOL, rowClass, localDay, content: 'Report',
    toolInput: { source, action: 'review', target: { kind: 'session', id: 'work-1' }, status: 'completed' },
});
const card = (id: string, localDay: string, rowClass: StreamItem['rowClass'], kind: string): StreamItem => ({
    id, kind: 'system', toolName: MAIN_CHAT_PROACTIVE_TOOL, rowClass, localDay,
    toolInput: { momentId: id, kind, state: 'pending', headline: 'Card', body: 'Body' },
});

// Rows shaped like Bo's Oct 29-30 stream: routine reports from other
// sources, briefs, and a weekly recap the panel used to drop.
const stream: StreamItem[] = [
    { id: 'u1', kind: 'user', content: 'What is due?', localDay: '2026-10-29', rowClass: 'conversation' },
    { id: 'a1', kind: 'assistant', content: 'PT is due.', localDay: '2026-10-29', rowClass: 'conversation' },
    status('s1', '2026-10-29'),
    report('r1', '2026-10-29', 'routine', 'schedule'),
    report('r2', '2026-10-29', 'routine', 'handoff'),
    card('b1', '2026-10-29', 'routine', 'brief_ready'),
    { id: 'res', kind: 'result', content: 'done', localDay: '2026-10-29' },
    { id: 'run', kind: 'system', content: 'Run completed', localDay: '2026-10-29' },
    card('evening', '2026-10-30', 'routine', 'brief_ready'),
    report('week', '2026-10-30', 'finished', 'weekly_recap'),
    card('m1', '2026-10-30', 'waiting', 'meeting_prep'),
    status('s2', '2026-10-31'),
];

describe('one stream, folded one way (chat-review v1.0.3 #2)', () => {
    it('gives the Today panel and the Chat page the same day counts and open cards', () => {
        const today = '2026-11-01';
        const view = { today, hideRoutine: false, topicNames: new Map<string, string>(), chips: {} };
        const { result } = renderHook(() => useStreamListModel(stream, false, 0, view, false));
        const pageFolds = Object.fromEntries(result.current.rows.flatMap((row) => row.type === 'day-fold' ? [[row.day, row.routineCount]] : []));
        const flat = (group: GroupedStreamItem): StreamItem[] => group.type === 'single' ? [group.item] : group.type === 'tool-group' ? group.items : [group.thinkingItem, ...group.tools];
        const pageOpen = result.current.rows.flatMap((row) => row.type === 'turn'
            ? [...(row.turn.user ? [row.turn.user] : []), ...row.turn.segments.flatMap((segment) => [...segment.processing, ...segment.finals])]
                .flatMap(flat).filter((item) => item.kind === 'system').map((item) => item.id)
            : []);

        const panelDays = buildMainChatDays(panelRows(stream), today);
        const panelFolds = Object.fromEntries(panelDays.filter((day) => day.routine.length > 0).map((day) => [day.day, day.routineCount]));
        const panelOpen = panelDays.flatMap((day) => day.items.filter((item) => item.kind === 'system').map((item) => item.id));

        expect(panelFolds).toEqual(pageFolds);
        expect(panelFolds).toEqual({ '2026-10-29': 5, '2026-10-30': 1, '2026-10-31': 2 });
        expect(panelOpen).toEqual(pageOpen);
        expect(panelOpen).toEqual(['week', 'm1']);
    });

    it('keeps the walk-through history and all nine Thursday updates at both widths', () => {
        const thursday = '2026-10-29';
        const rows: StreamItem[] = [
            { id: 'pt-user', kind: 'user', content: 'Keep my PT work out of the brief.', localDay: thursday, rowClass: 'conversation' },
            { id: 'pt-question', kind: 'assistant', content: 'Should I keep PT out of your briefs, or include it when it is due?', localDay: thursday, rowClass: 'conversation' },
            ...Array.from({ length: 4 }, (_, index) => status(`status-${index}`, thursday)),
            card('thursday-brief', thursday, 'routine', 'brief_ready'),
            card('friday-evening', '2026-10-30', 'routine', 'brief_ready'),
            status('saturday', '2026-10-31'),
            { id: 'week-answer', kind: 'user', content: 'release isoform 1.0', localDay: '2026-11-02', rowClass: 'conversation' },
        ];
        const view = { today: '2026-11-02', hideRoutine: false, topicNames: new Map<string, string>(), chips: { 'pt-question': ['Keep it out', "Include it when it's due"] } };
        const page = renderHook(() => useStreamListModel(rows, false, 0, view, false));
        const panel = renderHook(() => useStreamListModel(panelRows(rows), false, 0, view, false));
        expect(page.result.current.rows).toEqual(panel.result.current.rows);
        const thursdayFold = page.result.current.rows.find((row) => row.type === 'day-fold' && row.day === thursday);
        expect(thursdayFold).toMatchObject({ routineCount: 9 });
        expect(page.result.current.rowIndexByItemId.has('pt-question')).toBe(true);
        expect(panel.result.current.rowIndexByItemId.has('pt-question')).toBe(true);
        expect(view.chips['pt-question']).toEqual(['Keep it out', "Include it when it's due"]);
    });
});
