import { act, fireEvent, render } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import i18n from '@/i18n';
import { StreamBody } from './stream-body';
import { FailedSendContext } from '@/lib/failed-send-context';
import { GroupedStreamList } from './grouped-stream-list';
import { streamCardRenderProbe } from './stream-card';
import { virtuosoMockRecord, resetVirtuosoMockRecord } from '@/test-utils/virtuoso-mock';
import { clearStreamEditDraft } from '@/lib/stream-edit-drafts';
import { resetRenderTierStores } from '@/lib/privacy-reset';
import type { MainChatStreamView } from './main-chat/main-chat-view-context';
import type { StreamItem } from '@/types';

// Guard tests for the split chat stream: older history windows through
// react-virtuoso; the most recent rows render fully (native find and
// cross-message copy). react-virtuoso renders nothing under jsdom, so the
// full-render mock stands in and records the integration contract.
vi.mock('react-virtuoso', async () => {
    const { virtuosoMockModule } = await import('@/test-utils/virtuoso-mock');
    return virtuosoMockModule();
});

const shim = vi.hoisted(() => ({ payload: {} as Record<string, unknown>, listeners: new Map<string, Set<(payload: unknown) => void>>() }));
vi.mock('@/lib/wails-shim-app', () => new Proxy({}, {
    has: () => true,
    get: (_target, name: string) => {
        if (name === 'then') return undefined;
        return (..._args: unknown[]) => Promise.resolve(shim.payload[name] ?? []);
    },
}));
vi.mock('../../wailsjs/runtime/runtime', () => ({
    EventsOn: (name: string, handler: (payload: unknown) => void) => {
        const set = shim.listeners.get(name) ?? new Set();
        set.add(handler);
        shim.listeners.set(name, set);
        return () => set.delete(handler);
    },
    EventsEmit: vi.fn(),
    BrowserOpenURL: vi.fn(),
    ClipboardSetText: vi.fn(),
}));
vi.mock('sonner', () => ({ toast: Object.assign(vi.fn(), { error: vi.fn(), success: vi.fn() }) }));

function firePrivacyEvent(name: string) {
    act(() => {
        shim.listeners.get(name)?.forEach((handler) => handler(null));
    });
}

function firePrivacyEventWith(name: string, payload: unknown) {
    act(() => {
        shim.listeners.get(name)?.forEach((handler) => handler(payload));
    });
}

function makeTurns(pairs: number, startIndex = 0, localDay?: string): StreamItem[] {
    const items: StreamItem[] = [];
    for (let i = startIndex; i < startIndex + pairs; i++) {
        items.push({ id: `u-${i}`, kind: 'user', content: `Question ${i}`, sortOrder: i * 2, ...(localDay ? { localDay } : {}) });
        items.push({ id: `a-${i}`, kind: 'assistant', content: `Answer ${i} with **markdown**`, sortOrder: i * 2 + 1, ...(localDay ? { localDay } : {}) });
    }
    return items;
}

const mainView: MainChatStreamView = { today: '2026-10-23', hideRoutine: false, topicNames: new Map(), chips: {} };

function viewport(): HTMLElement {
    const el = document.querySelector<HTMLElement>('[data-radix-scroll-area-viewport]');
    if (!el) throw new Error('radix viewport missing');
    return el;
}

async function settle() {
    await act(async () => { await new Promise((resolve) => setTimeout(resolve, 60)); });
    // The focus-scroll chain is a transition followed by two animation frames.
    await act(async () => { await new Promise((resolve) => requestAnimationFrame(() => resolve(null))); });
    await act(async () => { await new Promise((resolve) => requestAnimationFrame(() => resolve(null))); });
}

beforeEach(async () => {
    await i18n.changeLanguage('en');
    Element.prototype.scrollIntoView = vi.fn();
    globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
    resetVirtuosoMockRecord();
    streamCardRenderProbe.current = null;
    resetRenderTierStores();
    for (const id of ['u-0', 'u-1', 'u-2', 'u-3', 'u-4', 'u-100', 'u-101', 'u-102']) clearStreamEditDraft(id);
});

describe('windowed stream', () => {
    it('renders the header, windows older history and mounts all recent rows', async () => {
        render(
            <StreamBody
                items={makeTurns(200)}
                streaming={false}
                hasProject
                sessionLocalId="s1"
                header={<div>In your chat today</div>}
            />,
        );
        await settle();
        expect(document.querySelector('[data-testid="virtuoso-mock"]')).not.toBeNull();
        // Only the older 50 rows go through the window; the recent 150 are plain.
        expect(virtuosoMockRecord.lastProps?.rowCount).toBe(50);
        expect(virtuosoMockRecord.lastProps?.firstItemIndex).toBe(1_000_000);
        expect(document.body.textContent).toContain('In your chat today');
        expect(document.querySelectorAll('[data-stream-turn]').length).toBe(200);
        // Native find covers recent history...
        expect(document.body.textContent).toContain('Answer 100 with markdown');
        expect(document.body.textContent).toContain('Answer 199 with markdown');
        // ...and the windowed old section renders through the same row view.
        expect(document.body.textContent).toContain('Answer 0 with markdown');
    });

    it('keeps the prepend anchor: stable row indices and no manual scrollTop adjustment', async () => {
        const view = render(<StreamBody items={makeTurns(200)} streaming={false} hasProject sessionLocalId="s1" />);
        await settle();
        const before = virtuosoMockRecord.lastProps?.firstItemIndex;
        const keptRow = document.querySelector('[data-stream-item-id="a-7"]');
        expect(keptRow).not.toBeNull();
        // The reader sits mid-stream; older history arrives above them.
        const vp = viewport();
        Object.defineProperty(vp, 'scrollHeight', { configurable: true, value: 5000 });
        vp.scrollTop = 1200;
        view.rerender(<StreamBody items={[...makeTurns(10, -10), ...makeTurns(200)]} streaming={false} hasProject sessionLocalId="s1" />);
        await settle();
        // Index space lowered by exactly the prepended count — a real prepend —
        // so the windowed list keeps the same rows at the same indices.
        expect(virtuosoMockRecord.lastProps?.firstItemIndex).toBe(before! - 10);
        expect(document.querySelector('[data-stream-item-id="a-7"]')).toBe(keptRow);
        expect(vp.scrollTop).toBe(1200);
    });

    it('does not lower the index space on appends (reading position must not move)', async () => {
        const view = render(<StreamBody items={makeTurns(200)} streaming={false} hasProject sessionLocalId="s1" />);
        await settle();
        const before = virtuosoMockRecord.lastProps?.firstItemIndex;
        view.rerender(<StreamBody items={[...makeTurns(200), ...makeTurns(20, 200)]} streaming={false} hasProject sessionLocalId="s1" />);
        await settle();
        expect(virtuosoMockRecord.lastProps?.firstItemIndex).toBe(before);
    });

    it('shows the jump pill with the new-reply count and jumps back to the newest', async () => {
        const view = render(<StreamBody items={makeTurns(200)} streaming={false} hasProject sessionLocalId="s1" />);
        await settle();
        const vp = viewport();
        Object.defineProperty(vp, 'scrollHeight', { configurable: true, value: 5000 });
        Object.defineProperty(vp, 'clientHeight', { configurable: true, value: 800 });
        // The reader scrolls up with the wheel; only their own input counts as
        // scrolling away from the bottom.
        fireEvent.wheel(vp);
        vp.scrollTop = 1000;
        fireEvent.scroll(vp);
        expect(document.querySelector('[data-testid="jump-to-newest"]')).not.toBeNull();
        view.rerender(<StreamBody items={[...makeTurns(200), { id: 'a-new', kind: 'assistant', content: 'Fresh reply', sortOrder: 400 }]} streaming={false} hasProject sessionLocalId="s1" />);
        await settle();
        const pill = document.querySelector('[data-testid="jump-to-newest"]');
        expect(pill?.textContent).toContain('1');
        fireEvent.click(pill!);
        await settle();
        expect(Element.prototype.scrollIntoView).toHaveBeenCalled();
        expect(document.querySelector('[data-testid="jump-to-newest"]')).toBeNull();
    });

    it('scrolls a focused message in the windowed section through scrollToIndex', async () => {
        const shown = vi.fn();
        render(
            <StreamBody
                items={makeTurns(200)}
                streaming={false}
                hasProject
                sessionLocalId="s1"
                focusItem={{ itemId: 'a-10', signal: 1 }}
                onFocusItemShown={shown}
            />,
        );
        await settle();
        expect(virtuosoMockRecord.scrollToIndexCalls).toContainEqual({ index: 10, align: 'center', behavior: 'auto' });
        expect(shown).toHaveBeenCalled();
    });

    it('scrolls a focused message in the recent section directly, without scrollToIndex', async () => {
        const shown = vi.fn();
        render(
            <StreamBody
                items={makeTurns(200)}
                streaming={false}
                hasProject
                sessionLocalId="s1"
                focusItem={{ itemId: 'a-190', signal: 1 }}
                onFocusItemShown={shown}
            />,
        );
        await settle();
        expect(virtuosoMockRecord.scrollToIndexCalls).toEqual([]);
        expect(shown).toHaveBeenCalled();
        expect(Element.prototype.scrollIntoView).toHaveBeenCalled();
    });

    it('drops withdrawn days cleanly (globally unique row keys)', async () => {
        // Both days start with promptless turns. Before day-scoped item-id
        // keys, those leading turns collided on `turn-0` across days and a
        // withdrawn day could leave its text on screen.
        const promptless = (day: string, label: string) => [0, 1, 2].map((i) => ({
            id: `${label}-lead-${i}`,
            kind: 'assistant' as const,
            content: `${label} answer ${i}`,
            localDay: day,
            sortOrder: i,
        }));
        const pairs = (day: string, label: string, count: number) => makeTurns(count, 0, day)
            .map((item, i) => ({ ...item, id: `${label}-${item.kind}-${i}` }));
        const dayOne = [...promptless('2026-10-22', 'First day'), ...pairs('2026-10-22', 'first', 80)];
        const dayTwo = [...promptless('2026-10-23', 'Second day'), ...pairs('2026-10-23', 'second', 80)];
        const view = render(
            <StreamBody items={[...dayOne, ...dayTwo]} streaming={false} hasProject sessionLocalId="s1" mainChatView={mainView} />,
        );
        await settle();
        expect(document.body.textContent).toContain('First day answer 1');
        expect(document.body.textContent).toContain('Second day answer 1');
        view.rerender(<StreamBody items={dayTwo} streaming={false} hasProject sessionLocalId="s1" mainChatView={mainView} />);
        await settle();
        expect(document.body.textContent).not.toContain('First day answer 1');
        expect(document.body.textContent).not.toContain('First day answer 0');
        expect(document.body.textContent).toContain('Second day answer 1');
    });
});

describe('stream render boundaries', () => {
    // Stable, like the app's useProjects wrapper: the render boundaries rely
    // on the edit handler keeping its identity across flushes.
    const onEditMessage = () => {};

    function collectRenders() {
        const renders: string[] = [];
        streamCardRenderProbe.current = (id) => renders.push(id);
        return renders;
    }

    it('re-renders only the streamed row when a token flush lands', () => {
        const items = makeTurns(5);
        const view = render(<GroupedStreamList items={items} streaming onEditMessage={onEditMessage} />);
        const renders = collectRenders();
        const last = items[items.length - 1];
        view.rerender(
            <GroupedStreamList
                items={[...items.slice(0, -1), { ...last, content: `${last.content} more tokens` }]}
                streaming
                onEditMessage={onEditMessage}
            />,
        );
        expect(renders).toEqual([last.id]);
        streamCardRenderProbe.current = null;
    });

    it('re-renders no stream rows when the parent re-renders with unchanged items (composer typing)', () => {
        const items = makeTurns(5);
        const view = render(<GroupedStreamList items={items} streaming={false} onEditMessage={onEditMessage} />);
        const renders = collectRenders();
        view.rerender(<GroupedStreamList items={items} streaming={false} onEditMessage={onEditMessage} />);
        expect(renders).toEqual([]);
        streamCardRenderProbe.current = null;
    });
});

describe('message editing', () => {
    it('locks edit and retry on every turn while a reply streams', () => {
        const items = makeTurns(3);
        const view = render(<GroupedStreamList items={items} streaming onEditMessage={() => {}} />);
        expect(document.querySelectorAll('[aria-label="Edit message"]')).toHaveLength(0);
        expect(document.querySelectorAll('[aria-label="Retry this message"]')).toHaveLength(0);
        view.rerender(<GroupedStreamList items={items} streaming={false} onEditMessage={() => {}} />);
        expect(document.querySelectorAll('[aria-label="Edit message"]')).toHaveLength(3);
        expect(document.querySelectorAll('[aria-label="Retry this message"]')).toHaveLength(3);
    });

    it('keeps an unsent draft when the row unmounts and remounts', async () => {
        const items = makeTurns(200);
        const first = render(<StreamBody items={items} streaming={false} hasProject sessionLocalId="s1" onEditMessage={() => {}} />);
        await settle();
        const editButtons = document.querySelectorAll('[aria-label="Edit message"]');
        fireEvent.click(editButtons[editButtons.length - 1]!);
        const textarea = document.querySelector('textarea')!;
        fireEvent.change(textarea, { target: { value: 'unsent draft words' } });
        first.unmount();
        render(<StreamBody items={items} streaming={false} hasProject sessionLocalId="s1" onEditMessage={() => {}} />);
        await settle();
        const reopened = document.querySelector('textarea')!;
        expect(reopened.value).toBe('unsent draft words');
        // Tidy: the draft belongs to this test only.
        fireEvent.keyDown(reopened, { key: 'Escape' });
    });

    it('keeps the draft when the edit is rejected, clears it when accepted', async () => {
        const items = makeTurns(3);
        const rejected = vi.fn(async () => false);
        const view = render(<GroupedStreamList items={items} streaming={false} onEditMessage={rejected} />);
        fireEvent.click(document.querySelectorAll('[aria-label="Edit message"]')[0]!);
        const textarea = document.querySelector('textarea')!;
        fireEvent.change(textarea, { target: { value: 'edited words' } });
        fireEvent.keyDown(textarea, { key: 'Enter' });
        await act(async () => { await new Promise((resolve) => setTimeout(resolve, 20)); });
        expect(rejected).toHaveBeenCalledWith('u-0', 0, 'edited words');
        // Rejected: the editor stays open with the draft.
        expect(document.querySelector('textarea')?.value).toBe('edited words');
        fireEvent.keyDown(document.querySelector('textarea')!, { key: 'Escape' });

        const accepted = vi.fn(async () => true);
        view.rerender(<GroupedStreamList items={items} streaming={false} onEditMessage={accepted} />);
        fireEvent.click(document.querySelectorAll('[aria-label="Edit message"]')[0]!);
        const second = document.querySelector('textarea')!;
        fireEvent.change(second, { target: { value: 'accepted words' } });
        fireEvent.keyDown(second, { key: 'Enter' });
        await act(async () => { await new Promise((resolve) => setTimeout(resolve, 20)); });
        expect(document.querySelector('textarea')).toBeNull();
    });

    it('keeps drafts through ordinary quota refreshes, drops them on real withdrawals', async () => {
        const items = makeTurns(3);
        render(<GroupedStreamList items={items} streaming={false} onEditMessage={() => {}} />);
        const openDraft = () => {
            fireEvent.click(document.querySelectorAll('[aria-label="Edit message"]')[0]!);
            fireEvent.change(document.querySelector('textarea')!, { target: { value: 'private words' } });
            expect(document.querySelector('textarea')?.value).toBe('private words');
        };
        openDraft();
        // While the baseline is unknown, even a signed-in payload is a
        // possible withdrawal: the first event fails closed.
        firePrivacyEventWith('model-accounts:updated', { accounts: [{ provider: 'claude', kind: 'subscription', name: 'Bo', on: true, windows: [{ used: 10 }] }] });
        expect(document.querySelector('textarea')).toBeNull();
        // Baseline now known: the 60-second quota poll with new numbers,
        // same signed-in account, keeps the editor and its words.
        openDraft();
        firePrivacyEventWith('model-accounts:updated', { accounts: [{ provider: 'claude', kind: 'subscription', name: 'Bo', on: true, windows: [{ used: 80 }] }] });
        expect(document.querySelector('textarea')?.value).toBe('private words');
        // Actual sign-out: the account is gone.
        firePrivacyEventWith('model-accounts:updated', { accounts: [] });
        expect(document.querySelector('textarea')).toBeNull();
        // Forget, protect-app and Closed Eyes clear as before.
        for (const name of ['memory:updated', 'sensitive-apps:updated', 'closed-eyes:changed']) {
            openDraft();
            firePrivacyEvent(name);
            expect(document.querySelector('textarea')).toBeNull();
        }
    });
});

it('shows a failed send as one gray line and retries or edits the original message', () => {
    const retry = vi.fn();
    const edit = vi.fn();
    const view = render(
        <FailedSendContext.Provider value={{ retry, edit, resendLast: vi.fn(), resendItemId: null, aiReady: true }}>
            <GroupedStreamList items={[{ id: 'failed-user', kind: 'user', content: 'Keep this draft', sendFailed: true }]} streaming={false} />
        </FailedSendContext.Provider>,
    );
    expect(view.getByText('Keep this draft')).toBeInTheDocument();
    const line = view.getByRole('status');
    expect(line).toHaveTextContent("Didn't send · Try again · Edit");
    expect(line).toHaveClass('text-subtle');
    expect(view.container.innerHTML).not.toMatch(/text-error|bg-error|border-error/);
    fireEvent.click(view.getByRole('button', { name: 'Try again' }));
    expect(retry).toHaveBeenCalledWith('failed-user');
    fireEvent.click(view.getByRole('button', { name: 'Edit' }));
    expect(edit).toHaveBeenCalledWith('failed-user');
});

// K1 box 1 + 3: a send kept for want of an AI stays dimmed with the no-AI line,
// and once an AI is configured reads as an ordinary failed send; nothing resends.
it('keeps a no-AI send dimmed, then shows ordinary failed-send controls once an AI is set up', () => {
    const retry = vi.fn();
    const edit = vi.fn();
    const items = [{ id: 'kept-user', kind: 'user' as const, content: 'Plan my week', sendFailed: true, sendFailedCode: 'no_assistant' }];
    const view = render(
        <FailedSendContext.Provider value={{ retry, edit, resendLast: vi.fn(), resendItemId: null, aiReady: false }}>
            <GroupedStreamList items={items} streaming={false} />
        </FailedSendContext.Provider>,
    );
    expect(view.getByText('Plan my week').closest('.opacity-60')).not.toBeNull();
    expect(view.getByRole('status')).toHaveTextContent("Didn't send: no AI is signed in. · Set up");
    expect(view.getByRole('status')).toHaveClass('text-warning');
    view.rerender(
        <FailedSendContext.Provider value={{ retry, edit, resendLast: vi.fn(), resendItemId: null, aiReady: true }}>
            <GroupedStreamList items={items} streaming={false} />
        </FailedSendContext.Provider>,
    );
    expect(view.getByRole('status')).toHaveTextContent("Didn't send · Try again · Edit");
    expect(retry).not.toHaveBeenCalled();
});

// R75: only the chat's latest signed-out row offers Send again; an older row does not.
it('offers Send again only on the latest signed-out answer', () => {
    const resendLast = vi.fn();
    const items = [
        { id: 'old-err', kind: 'error' as const, content: 'anthropic: HTTP 401', toolInput: { code: 'signed_out', assistant: 'Claude Code' } },
        { id: 'msg-2', kind: 'user' as const, content: 'Second try' },
        { id: 'new-err', kind: 'error' as const, content: 'anthropic: HTTP 401', toolInput: { code: 'signed_out', assistant: 'Claude Code' } },
    ];
    const view = render(
        <FailedSendContext.Provider value={{ retry: vi.fn(), edit: vi.fn(), resendLast, resendItemId: 'new-err', aiReady: true }}>
            <GroupedStreamList items={items} streaming={false} />
        </FailedSendContext.Provider>,
    );
    expect(view.getAllByRole('button', { name: 'Send again' })).toHaveLength(1);
    fireEvent.click(view.getByRole('button', { name: 'Send again' }));
    expect(resendLast).toHaveBeenCalledTimes(1);
});
