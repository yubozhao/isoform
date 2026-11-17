import { render, renderHook, waitFor } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import type { StreamItem } from '@/types';
import { MAIN_CHAT_PROACTIVE_TOOL } from '@/lib/stream-helpers';
import { useMainChatView } from '@/hooks/useMainChatView';
import { useStreamListModel } from '@/components/grouped-stream-list';
import type { MainChatStreamView } from './main-chat-view-context';
import { MainChatStreamBody } from './main-chat-stream-body';

// Bo's Chat on build 1e5a05a59: the newest page of 50 rows held only
// Isoform's cards (his last words were on Nov 2), so with the cards out of
// Chat it showed one Nov 9 card and an empty Nov 13 day, and the older page
// with his conversation never loaded: nothing could be scrolled to load it.

vi.mock('@/lib/wails-shim-app', () => new Proxy({}, {
    has: () => true,
    get: (_target, name: string) => (name === 'then' ? undefined : vi.fn().mockResolvedValue({})),
}));
vi.mock('@/lib/wails-shim-runtime', () => ({
    EventsOn: () => () => {}, EventsEmit: vi.fn(), ClipboardSetText: vi.fn(), LogInfo: vi.fn(), LogWarning: vi.fn(),
}));

const TODAY = '2026-11-17';
globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;

function card(id: string, localDay: string, rowClass: StreamItem['rowClass'], fields: object): StreamItem {
    return {
        id, kind: 'system', toolName: MAIN_CHAT_PROACTIVE_TOOL, rowClass, localDay,
        toolInput: { momentId: `m-${id}`, headline: 'H', body: 'B', draft: false, actionIds: [], actionLabels: {}, target: { kind: 'today' }, state: 'pending', evidence: [], ...fields },
    } as StreamItem;
}

const ISOFORM_ONLY: StreamItem[] = [
    card('c1', '2026-11-12', 'routine', { kind: 'brief_ready', briefId: 'b1' }),
    card('c2', '2026-11-13', 'other', { kind: 'weekly_recap', briefId: 'b2' }),
    card('c3', '2026-11-13', 'other', { kind: 'first_read', state: 'acted' }),
    card('c4', '2026-11-16', 'routine', { kind: 'meeting_prep' }),
];

function Chat({ items, hasMore, onLoadMore }: { items: StreamItem[]; hasMore: boolean; onLoadMore: () => void }) {
    const view = useMainChatView({ enabled: true, items });
    return <MainChatStreamBody mainChat={view} onPrepare={() => {}} items={items} streaming={false} hasProject focusItem={null} hasMore={hasMore} loadingMore={false} onLoadMore={onLoadMore} />;
}

describe('Chat opens on the conversation even when the newest page is all updates', () => {
    it('loads older pages while no conversation row is loaded', async () => {
        const onLoadMore = vi.fn();
        render(<Chat items={ISOFORM_ONLY} hasMore onLoadMore={onLoadMore} />);
        await waitFor(() => expect(onLoadMore).toHaveBeenCalled());
    });

    it('stops once the conversation is loaded, or nothing older is left', () => {
        const onLoadMore = vi.fn();
        const user = { id: 'u1', kind: 'user', content: 'What did Sam say?', rowClass: 'conversation', localDay: '2026-11-02' } as StreamItem;
        const { unmount } = render(<Chat items={[user, ...ISOFORM_ONLY]} hasMore onLoadMore={onLoadMore} />);
        unmount();
        render(<Chat items={ISOFORM_ONLY} hasMore={false} onLoadMore={onLoadMore} />);
        expect(onLoadMore).not.toHaveBeenCalled();
    });

    it('keeps no divider for a day whose cards Chat draws as nothing', () => {
        const view = { surface: 'chat', today: TODAY, hideRoutine: false, topicNames: new Map(), chips: {} } as unknown as MainChatStreamView;
        const { result } = renderHook(() => useStreamListModel(ISOFORM_ONLY, false, 0, view, false));
        expect(result.current.rows).toEqual([]);
    });
});
