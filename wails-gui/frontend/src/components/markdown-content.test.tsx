import { act, cleanup, render, waitFor } from '@testing-library/react';
import { beforeEach, expect, it, vi } from 'vitest';
import i18n from '@/i18n';
import { highlightCacheProbe, MarkdownContent } from './markdown-content';

// Deferred syntax highlighting: a sealed code block paints plain first (the
// streaming path's plain-then-highlighted swap), then Prism colors land one
// block per macrotask so a screenful of fences never lands in one commit.
vi.mock('@/lib/wails-shim-app', () => new Proxy({}, {
    has: () => true,
    get: (_target, name: string) => {
        if (name === 'then') return undefined;
        return (..._args: unknown[]) => Promise.resolve([]);
    },
}));
const listeners = vi.hoisted(() => new Map<string, Set<(payload: unknown) => void>>());
vi.mock('../../wailsjs/runtime/runtime', () => ({
    EventsOn: (name: string, handler: (payload: unknown) => void) => {
        const set = listeners.get(name) ?? new Set();
        set.add(handler);
        listeners.set(name, set);
        return () => set.delete(handler);
    },
    EventsEmit: vi.fn(),
    BrowserOpenURL: vi.fn(),
    ClipboardSetText: vi.fn(),
}));

const FENCED = 'Before the code.\n\n```ts\nexport function demo(input: number): number {\n    return input + 1;\n}\n```\n\nAfter the code.';

beforeEach(async () => {
    await i18n.changeLanguage('en');
});

it('paints a sealed code block plain first, then highlights it off the commit', async () => {
    const view = render(<MarkdownContent content={FENCED} />);
    // First paint: the code is all there, unhighlighted (no Prism token spans).
    expect(view.container.textContent).toContain('export function demo');
    expect(view.container.querySelectorAll('.token').length).toBe(0);
    // The deferred highlight lands on a later macrotask, not in the open commit.
    // Prism itself loads after the first paint (code-highlighter.tsx).
    await waitFor(() => expect(view.container.querySelectorAll('.token').length).toBeGreaterThan(0), { timeout: 5000 });
});

it('defers every mount the same way, including remounts', async () => {
    const first = render(<MarkdownContent content={FENCED} />);
    await waitFor(() => expect(first.container.querySelectorAll('.token').length).toBeGreaterThan(0), { timeout: 5000 });
    first.unmount();
    // A remount pays no Prism in its commit either: plain first, colors after.
    const second = render(<MarkdownContent content={FENCED} />);
    expect(second.container.querySelectorAll('.token').length).toBe(0);
    await act(async () => { await new Promise((resolve) => setTimeout(resolve, 50)); });
    expect(second.container.querySelectorAll('.token').length).toBeGreaterThan(0);
});

it('clears the highlight markers and pending work on the privacy resets', async () => {
    const account = { provider: 'claude', kind: 'subscription', name: 'Bo', on: true, windows: [{ used: 10 }] };
    render(<MarkdownContent content={FENCED} />);
    await act(async () => { await new Promise((resolve) => setTimeout(resolve, 50)); });
    expect(highlightCacheProbe.marked()).toBeGreaterThan(0);
    // An ordinary quota refresh: same signed-in account, new numbers.
    act(() => { listeners.get('model-accounts:updated')?.forEach((handler) => handler({ accounts: [{ ...account, windows: [{ used: 90 }] }] })); });
    expect(highlightCacheProbe.marked()).toBeGreaterThan(0);
    for (const [name, payload] of [
        ['model-accounts:updated', { accounts: [] }],
        ['memory:withdrawn', null],
        ['sensitive-apps:updated', null],
        ['closed-eyes:changed', null],
    ] as const) {
        // More fences than one macrotask can drain: work is still queued.
        render(<MarkdownContent content={FENCED + FENCED + FENCED} />);
        const queuedBefore = highlightCacheProbe.queued();
        act(() => { listeners.get(name)?.forEach((handler) => handler(payload)); });
        expect(highlightCacheProbe.marked()).toBe(0);
        expect(highlightCacheProbe.queued()).toBeLessThanOrEqual(queuedBefore);
        expect(highlightCacheProbe.queued()).toBe(0);
        cleanup();
        render(<MarkdownContent content={FENCED} />);
        act(() => { listeners.get('model-accounts:updated')?.forEach((handler) => handler({ accounts: [account] })); });
        await act(async () => { await new Promise((resolve) => setTimeout(resolve, 50)); });
        expect(highlightCacheProbe.marked()).toBeGreaterThan(0);
    }
});
