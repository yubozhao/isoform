import { describe, expect, it, vi } from 'vitest';
import { render, renderHook, screen } from '@testing-library/react';
import type { StreamItem } from '@/types';
import type { MainChatStreamView } from './main-chat-view-context';
import { dayRowsVisibility, useStreamListModel } from '@/components/grouped-stream-list';
import { chatThreadUpdates } from '@/lib/main-chat-view';
import { MainChatFilterRow } from './main-chat-filter-row';
import en from '@/i18n/locales/en/chat.json';

// Chat never speaks first (phone-chat v1.0.0 rule 3, computer-chat v1.0.0):
// Bo's Chat on build 3fca62059 was day dividers, "N updates were removed"
// lines and "N updates from Isoform" folds, with no conversation in view.
const TODAY = '2026-11-16';

function row(id: string, localDay: string, rowClass: StreamItem['rowClass'], kind: 'system' | 'user' = 'system', toolInput?: object): StreamItem {
    return {
        id, kind, content: kind === 'user' ? 'What did Sam say about pricing?' : `Update ${id}`,
        toolName: kind === 'system' ? 'main_chat_proactive' : undefined,
        toolInput: toolInput ?? (kind === 'system' ? { momentId: `m-${id}`, kind: 'decision_due', headline: 'H', body: 'B', state: 'pending', actionIds: [], actionLabels: {}, evidence: [], target: { kind: 'today', id: '' } } : undefined),
        sortOrder: Number(id.replace(/\D/g, '')), localDay, rowClass,
    } as unknown as StreamItem;
}

const withdrawn = (id: string, localDay: string) =>
    row(id, localDay, 'other', 'system', { momentId: `m-${id}`, kind: 'thread_update_unavailable', headline: '', body: '', state: 'pending', actionIds: [], actionLabels: {}, evidence: [], target: { kind: 'none', id: '' } });

describe("Isoform's updates stay out of Chat's thread", () => {
    it('is the default', () => {
        expect(chatThreadUpdates.shown).toBe(false);
    });

    it('gives a day a divider only when it has conversation', () => {
        expect(dayRowsVisibility({ items: [], routine: [{}, {}], withdrawn: 3, withheld: 1 }, false)).toEqual({ divider: false, fold: false, removed: false });
        expect(dayRowsVisibility({ items: [{}], routine: [{}], withdrawn: 2 }, false)).toEqual({ divider: true, fold: false, removed: false });
    });

    it('opens on the conversation: no fold, no removed-updates line, no empty day', () => {
        const items = [
            row('1', '2026-11-13', 'routine'), row('2', '2026-11-13', 'routine'),
            withdrawn('3', '2026-11-14'), withdrawn('4', '2026-11-14'),
            row('5', '2026-11-15', 'routine'),
            row('6', TODAY, 'other', 'user'),
        ];
        const view = { surface: 'chat', today: TODAY, hideRoutine: false, topicNames: new Map(), chips: {} } as unknown as MainChatStreamView;
        const { result } = renderHook(() => useStreamListModel(items, false, 0, view, false));
        const types = result.current.rows.map((r) => r.type);
        expect(types).not.toContain('day-fold');
        expect(types).not.toContain('day-removed');
        const dividers = result.current.rows.filter((r) => r.type === 'day-divider').map((r) => (r as { day: string }).day);
        expect(dividers).toEqual([TODAY]);
    });

    it('has no Hide or Show updates switch in the header', () => {
        render(<MainChatFilterRow filter={{ kind: 'all' }} onFilter={vi.fn()} topics={[]} hideRoutine={false} routineCount={4} hideBusy={false} hideFailed={false} onToggleHide={vi.fn()} />);
        expect(screen.queryByTestId('main-chat-hide-routine')).toBeNull();
        expect(screen.queryByText(en.mainChat.view.hideUpdates)).toBeNull();
    });
});
