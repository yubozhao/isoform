import { act, cleanup, fireEvent, render, screen } from '@testing-library/react';
import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest';
import { type ComponentProps } from 'react';
import i18n from '@/i18n';
import { Composer } from './composer';
import type { StreamItem } from '@/types';
import { StreamBody } from './stream-body';

vi.mock('./stream-card', () => ({ StreamCard: ({ item }: { item: StreamItem }) => <p>{item.content}</p> }));
vi.mock('./processing-block', () => ({ ProcessingBlock: () => <div data-testid="folded" /> }));
vi.mock('@/hooks/useUserProfile', () => ({ useUserProfile: () => ({ profile: null }) }));

const scrolled: Element[] = [];

beforeAll(() => {
    Element.prototype.scrollIntoView = function scrollIntoView(this: Element) {
        scrolled.push(this);
    };
    globalThis.ResizeObserver ??= class {
        observe() {}
        unobserve() {}
        disconnect() {}
    } as unknown as typeof ResizeObserver;
});
afterEach(() => {
    cleanup();
    scrolled.length = 0;
});

const ITEMS: StreamItem[] = [
    { id: 'u1', kind: 'user', content: 'Move the Halcyon sync up?' },
    { id: 't1', kind: 'tool', content: 'looked something up', toolName: 'search' },
    { id: 'a1', kind: 'assistant', content: 'Yes, to Oct 30.' },
    { id: 'u2', kind: 'user', content: 'Later question' },
];

async function frame() {
    await act(async () => {
        await new Promise((resolve) => requestAnimationFrame(() => resolve(null)));
    });
}

describe('StreamBody scroll to a message (Search)', () => {
    it('scrolls the found message to the middle, once per request', async () => {
        const { rerender } = render(<StreamBody items={ITEMS} streaming={false} hasProject focusItem={{ itemId: 'a1', signal: 1 }} />);
        await frame();
        expect(scrolled.map((el) => el.textContent)).toContain('Yes, to Oct 30.');
        scrolled.length = 0;
        // New items do not scroll back to it; a new request does.
        rerender(<StreamBody items={[...ITEMS, { id: 'a2', kind: 'assistant', content: 'New' }]} streaming={false} hasProject focusItem={{ itemId: 'a1', signal: 1 }} />);
        await frame();
        expect(scrolled.map((el) => el.textContent)).not.toContain('Yes, to Oct 30.');
        rerender(<StreamBody items={ITEMS} streaming={false} hasProject focusItem={{ itemId: 'a1', signal: 2 }} />);
        await frame();
        expect(scrolled.map((el) => el.textContent)).toContain('Yes, to Oct 30.');
    });

    it('says when it has shown the message', async () => {
        const shown = vi.fn();
        render(<StreamBody items={ITEMS} streaming={false} hasProject focusItem={{ itemId: 'a1', signal: 1 }} onFocusItemShown={shown} />);
        await frame();
        expect(shown).toHaveBeenCalledTimes(1);
    });

    it('scrolls to the turn\'s prompt when the message sits in a folded block', async () => {
        render(<StreamBody items={ITEMS} streaming={false} hasProject focusItem={{ itemId: 't1', signal: 1 }} />);
        await frame();
        expect(scrolled.map((el) => el.textContent)).toContain('Move the Halcyon sync up?');
    });
});

const composerProps: ComponentProps<typeof Composer> = {
    prompt: '', onPromptChange: vi.fn(), onSend: vi.fn(), onCancel: vi.fn(), onKeyDown: vi.fn(),
    executorGroups: [], streaming: false, ready: true, hasProject: true,
    images: [], onImagesChange: vi.fn(), files: [], onFilesChange: vi.fn(), executorType: 'claude',
    queuedMessages: [], onRemoveQueuedMessage: vi.fn(), onEditQueuedMessage: vi.fn(), availableCommands: [],
};
function Reader({ items, streaming = false }: { items: StreamItem[]; streaming?: boolean }) {
    return <>
        <StreamBody items={items} streaming={streaming} hasProject vimMode={false} />
        <Composer {...composerProps} />
    </>;
}

it.each(['en', 'zh-CN'])('counts unseen replies and jumps from a floating stream control with Vim off in %s', async (language) => {
    await i18n.changeLanguage(language);
    const view = render(<Reader items={ITEMS} />);
    await frame();
    const viewport = view.container.querySelector('[data-radix-scroll-area-viewport]')!;
    Object.defineProperties(viewport, { scrollHeight: { configurable: true, value: 2000 }, clientHeight: { configurable: true, value: 200 } });
    viewport.scrollTop = 100;
    fireEvent.wheel(viewport);
    fireEvent.scroll(viewport);
    scrolled.length = 0;
    const first: StreamItem = { id: 'new-reply', kind: 'assistant', content: 'The new reply.' };
    view.rerender(<Reader items={[...ITEMS, first]} streaming />);
    const one = language === 'en' ? '1 new reply' : '1 条新回复';
    const two = language === 'en' ? '2 new replies' : '2 条新回复';
    const button = screen.getByRole('button', { name: one });
    expect(button).toHaveClass('h-8', 'rounded-lg');
    expect(screen.getByTestId('composer-toolbar')).not.toContainElement(button);
    expect(button.parentElement).toHaveClass('absolute', 'bottom-3', 'left-1/2', '-translate-x-1/2', 'bg-panel', 'border', 'border-border', 'shadow-soft');
    expect(screen.getByRole('textbox').parentElement).not.toContainElement(button);
    expect(view.container.querySelector('button.absolute, button.fixed')).toBeNull();
    // Streaming growth of the same message and older history do not add replies.
    view.rerender(<Reader items={[{ id: 'older', kind: 'assistant', content: 'Earlier history.' }, ...ITEMS, { ...first, content: 'More words in the new reply.' }]} streaming />);
    expect(screen.getByRole('button', { name: one })).toBeInTheDocument();
    const next = [...ITEMS, first, { id: 'second-reply', kind: 'assistant', content: 'A second reply.' } as StreamItem];
    view.rerender(<Reader items={next} />);
    await frame();
    expect(scrolled).toHaveLength(0);
    fireEvent.click(screen.getByRole('button', { name: two }));
    expect(scrolled).toHaveLength(1);
    expect(screen.queryByRole('button', { name: two })).not.toBeInTheDocument();
    // Following resumes after the explicit jump.
    view.rerender(<Reader items={[...next, { id: 'third-reply', kind: 'assistant', content: 'A third reply.' }]} />);
    await frame();
    expect(scrolled).toHaveLength(2);
    expect(screen.queryByRole('button', { name: one })).not.toBeInTheDocument();
});


it('clears unseen replies on manual return, conversation change and unmount without counting tool rows', async () => {
    const view = render(<StreamBody items={ITEMS} streaming={false} hasProject vimMode={false} />);
    await frame();
    const viewport = view.container.querySelector('[data-radix-scroll-area-viewport]')!;
    Object.defineProperties(viewport, { scrollHeight: { configurable: true, value: 2000 }, clientHeight: { configurable: true, value: 200 } });
    viewport.scrollTop = 100;
    fireEvent.wheel(viewport);
    fireEvent.scroll(viewport);
    const tool: StreamItem = { id: 'new-tool', kind: 'tool', toolName: 'search', content: 'Working.' };
    view.rerender(<StreamBody items={[...ITEMS, tool]} streaming hasProject vimMode={false} />);
    expect(screen.queryByRole('button', { name: /new repl/ })).not.toBeInTheDocument();
    const reply: StreamItem = { id: 'new-answer', kind: 'assistant', content: 'Ready to read.' };
    view.rerender(<StreamBody items={[...ITEMS, tool, reply]} streaming={false} hasProject vimMode={false} />);
    expect(screen.getByRole('button', { name: /1 new reply|1 条新回复/ })).toBeInTheDocument();
    viewport.scrollTop = 1800;
    fireEvent.wheel(viewport);
    fireEvent.scroll(viewport);
    expect(screen.queryByRole('button', { name: /new repl|新回复/ })).not.toBeInTheDocument();
    viewport.scrollTop = 100;
    fireEvent.wheel(viewport);
    fireEvent.scroll(viewport);
    const another: StreamItem = { id: 'another', kind: 'assistant', content: 'One more reply.' };
    view.rerender(<StreamBody items={[...ITEMS, tool, reply, another]} streaming={false} hasProject vimMode={false} />);
    expect(screen.getByRole('button', { name: /1 new reply|1 条新回复/ })).toBeInTheDocument();
    view.rerender(<StreamBody items={[{ id: 'other-session', kind: 'user', content: 'Another conversation.' }]} streaming={false} hasProject vimMode={false} />);
    expect(screen.queryByRole('button', { name: /new repl|新回复/ })).not.toBeInTheDocument();
    view.unmount();
    expect(screen.queryByRole('button', { name: /new repl|新回复/ })).not.toBeInTheDocument();
});

it('offers a bare down arrow only beyond a screen, then hides on manual return', async () => {
    await i18n.changeLanguage('en');
    const view = render(<StreamBody items={ITEMS} streaming={false} hasProject vimMode={false} />);
    await frame();
    const viewport = view.container.querySelector('[data-radix-scroll-area-viewport]')!;
    Object.defineProperties(viewport, { scrollHeight: { configurable: true, value: 2000 }, clientHeight: { configurable: true, value: 200 } });
    viewport.scrollTop = 1650;
    fireEvent.wheel(viewport);
    fireEvent.scroll(viewport);
    expect(screen.queryByTestId('jump-to-newest')).not.toBeInTheDocument();
    viewport.scrollTop = 100;
    fireEvent.wheel(viewport);
    fireEvent.scroll(viewport);
    const button = screen.getByTestId('jump-to-newest');
    expect(button.querySelector('svg')).toBeInTheDocument();
    expect(button.textContent).toBe('');
    viewport.scrollTop = 1800;
    fireEvent.wheel(viewport);
    fireEvent.scroll(viewport);
    expect(screen.queryByTestId('jump-to-newest')).not.toBeInTheDocument();
});

it('attaches navigation when a conversation opens after the empty project state', async () => {
    const view = render(<StreamBody items={null} streaming={false} hasProject={false} />);
    view.rerender(<StreamBody items={ITEMS} streaming={false} hasProject />);
    await frame();
    const viewport = view.container.querySelector('[data-radix-scroll-area-viewport]')!;
    Object.defineProperties(viewport, { scrollHeight: { configurable: true, value: 2000 }, clientHeight: { configurable: true, value: 200 } });
    viewport.scrollTop = 100;
    fireEvent.wheel(viewport);
    fireEvent.scroll(viewport);
    expect(screen.getByTestId('jump-to-newest')).toBeInTheDocument();
});

it.each([false, true])('announces the current answer growing and finishing 100px above bottom in a 400px viewport (Vim %s)', async vimMode => {
    await i18n.changeLanguage('en');
    const answer: StreamItem = { id: 'current-answer', kind: 'assistant', content: 'First words' };
    const draw = (content: string, streaming: boolean) => <StreamBody items={[...ITEMS, { ...answer, content }]} streaming={streaming} hasProject vimMode={vimMode} />;
    const view = render(draw(answer.content!, true));
    await frame();
    const viewport = view.container.querySelector('[data-radix-scroll-area-viewport]')!;
    Object.defineProperties(viewport, { scrollHeight: { configurable: true, value: 2000 }, clientHeight: { configurable: true, value: 400 } });
    viewport.scrollTop = 1500;
    fireEvent.wheel(viewport);
    fireEvent.scroll(viewport);
    expect(screen.queryByTestId('jump-to-newest')).not.toBeInTheDocument();
    scrolled.length = 0;
    view.rerender(draw('First words and more streamed words', true));
    expect(screen.getByRole('button', { name: '1 new reply' })).toBeInTheDocument();
    view.rerender(draw('First words and even more streamed words', true));
    view.rerender(draw('First words and even more streamed words', false));
    await frame();
    expect(scrolled).toHaveLength(0);
    expect(screen.getByRole('button', { name: '1 new reply' })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: '2 new replies' })).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: '1 new reply' }));
    expect(screen.queryByTestId('jump-to-newest')).not.toBeInTheDocument();
    view.rerender(draw('First words and even more streamed words', false));
    expect(screen.queryByTestId('jump-to-newest')).not.toBeInTheDocument();
});

it('announces completion without a new row or final content change, and acknowledges the same reply on manual return', async () => {
    await i18n.changeLanguage('en');
    const items: StreamItem[] = [...ITEMS, { id: 'current-answer', kind: 'assistant', content: 'Answer in progress' }];
    const draw = (streaming: boolean) => <StreamBody items={items} streaming={streaming} hasProject />;
    const view = render(draw(true));
    await frame();
    const viewport = view.container.querySelector('[data-radix-scroll-area-viewport]')!;
    Object.defineProperties(viewport, { scrollHeight: { configurable: true, value: 2000 }, clientHeight: { configurable: true, value: 400 } });
    viewport.scrollTop = 1500;
    fireEvent.wheel(viewport);
    fireEvent.scroll(viewport);
    view.rerender(draw(false));
    expect(screen.getByRole('button', { name: '1 new reply' })).toBeInTheDocument();
    viewport.scrollTop = 1600;
    fireEvent.wheel(viewport);
    fireEvent.scroll(viewport);
    expect(screen.queryByTestId('jump-to-newest')).not.toBeInTheDocument();
    viewport.scrollTop = 1500;
    fireEvent.wheel(viewport);
    fireEvent.scroll(viewport);
    view.rerender(<StreamBody items={[...items.slice(0, -1), { ...items.at(-1)!, content: 'Later growth' }]} streaming hasProject />);
    view.rerender(draw(false));
    expect(screen.queryByTestId('jump-to-newest')).not.toBeInTheDocument();
});

describe('StreamBody for screen readers (pass-access v1.0.0, Chat 1)', () => {
    it('is a polite log, busy only while an answer streams', async () => {
        const { rerender } = render(<StreamBody items={ITEMS} streaming hasProject />);
        const log = await screen.findByRole('log', { name: 'Messages' });
        expect(log).toHaveAttribute('aria-live', 'polite');
        expect(log).toHaveAttribute('aria-busy', 'true');
        rerender(<StreamBody items={ITEMS} streaming={false} hasProject />);
        expect(screen.getByRole('log', { name: 'Messages' })).not.toHaveAttribute('aria-busy');
    });
});
