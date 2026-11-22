import type { ComponentProps } from 'react';
import { fireEvent, render, screen } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import i18n from '@/i18n';
import enToday from '@/i18n/locales/en/today.json';
import zhToday from '@/i18n/locales/zh-CN/today.json';
import { Composer } from './composer';
import type { TodayChatPreview } from '@/lib/today-chat-navigation';

vi.mock('@/hooks/useComposerSuggestions', () => ({
    useComposerSuggestions: () => ({
        suggestions: [], activeHandoffs: [], isLoading: false, isRefreshing: false, refresh: vi.fn(),
    }),
}));

function mount(todayPreview?: TodayChatPreview) {
    const props: ComponentProps<typeof Composer> = {
        prompt: 'Where do I stand on "Launch plan" and what is the next step?',
        todayPreview,
        onPromptChange: vi.fn(), onSend: vi.fn(), onCancel: vi.fn(), onKeyDown: vi.fn(),
        executorGroups: [], streaming: false,
        ready: true, hasProject: true, images: [], onImagesChange: vi.fn(), files: [], onFilesChange: vi.fn(),
        executorType: 'claude',
        queuedMessages: [], onRemoveQueuedMessage: vi.fn(), onEditQueuedMessage: vi.fn(), availableCommands: [],
    };
    return { props, view: render(<Composer {...props} />) };
}

describe('Today draft in the composer', () => {
    beforeEach(async () => { await i18n.changeLanguage('en'); });

    it.each(['en', 'zh-CN'])('uses the panel invitation in the main message box in %s', async (language) => {
        await i18n.changeLanguage(language);
        mount();
        const locale = language === 'en' ? enToday : zhToday;
        expect(screen.getByRole('textbox')).toHaveAttribute('placeholder', locale.mainChatPanel.placeholder);
    });

    it('takes the first-day placeholder when App passes one', () => {
        const { props, view } = mount();
        view.rerender(<Composer {...props} prompt="" placeholder="Ask about your day, a project or a person..." />);
        expect(screen.getByRole('textbox')).toHaveAttribute('placeholder', 'Ask about your day, a project or a person...');
    });

    it('shows the About chip with × and Backspace taking it off, and the topic placeholder (T11)', () => {
        const onRemove = vi.fn();
        const { props, view } = mount();
        view.rerender(<Composer {...props} prompt="" placeholder="Ask about Halcyon import" about={{ title: 'Halcyon import', onRemove }} />);
        expect(screen.getByTestId('composer-about-chip')).toHaveTextContent('About: Halcyon import');
        expect(screen.getByRole('textbox')).toHaveAttribute('placeholder', 'Ask about Halcyon import');
        fireEvent.click(screen.getByRole('button', { name: 'Take Halcyon import off this message' }));
        expect(onRemove).toHaveBeenCalledTimes(1);
        fireEvent.keyDown(screen.getByRole('textbox'), { key: 'Backspace' });
        expect(onRemove).toHaveBeenCalledTimes(2);
        expect(props.onKeyDown).not.toHaveBeenCalled();
        // With words in the field, Backspace edits them.
        view.rerender(<Composer {...props} prompt="What" about={{ title: 'Halcyon import', onRemove }} />);
        fireEvent.keyDown(screen.getByRole('textbox'), { key: 'Backspace' });
        expect(onRemove).toHaveBeenCalledTimes(2);
    });

    it('shows one tertiary context line before sending and leaves the named draft editable', () => {
        const { props, view } = mount({ status: 'ready', counts: { memories: 3, activities: 2, people: 1 } });
        const line = screen.getByText('Isoform adds what it knows about this: 3 memories, 2 activities, 1 person');
        expect(line).toHaveClass('text-muted/70');
        expect(line).toHaveAttribute('role', 'status');
        expect(view.container.querySelectorAll('[data-today-context-preview]')).toHaveLength(1);
        expect(screen.getByRole('textbox')).toHaveValue(props.prompt);
        expect(screen.getByRole('textbox')).toBeEnabled();
        expect(props.onSend).not.toHaveBeenCalled();
        fireEvent.change(screen.getByRole('textbox'), { target: { value: 'What is the smallest next step?' } });
        expect(props.onPromptChange).toHaveBeenCalledWith('What is the smallest next step?');
        expect(props.onSend).not.toHaveBeenCalled();
        view.rerender(<Composer {...props} prompt="" todayPreview={undefined} />);
        expect(screen.queryByText(/Isoform adds/)).not.toBeInTheDocument();
    });

    it.each([
        ['en', 'Isoform adds what it knows about this: nothing on file yet'],
        ['zh-CN', 'Isoform 会补充它了解的相关信息：目前没有相关记录'],
    ])('shows truthful empty context in %s', async (language, expected) => {
        await i18n.changeLanguage(language);
        mount({ status: 'ready', counts: { memories: 0, activities: 0, people: 0 } });
        expect(screen.getByText(expected)).toBeVisible();
    });

    it('localizes the counts in Chinese', async () => {
        await i18n.changeLanguage('zh-CN');
        mount({ status: 'ready', counts: { memories: 3, activities: 2, people: 1 } });
        expect(screen.getByText('Isoform 会补充它了解的相关信息：3 条记忆，2 项活动，1 位相关人员')).toBeVisible();
    });

    it('allows typing while context loads and disables sending until it resolves', () => {
        const { props, view } = mount({ status: 'loading' });
        expect(screen.getByRole('textbox')).toBeEnabled();
        expect(screen.getByTitle('Send message')).toBeDisabled();
        expect(screen.getByText('Isoform is checking what it knows about this.')).toBeVisible();
        view.rerender(<Composer {...props} todayPreview={{ status: 'ready', counts: { memories: 1, activities: 1, people: 0 } }} />);
        expect(screen.getByTitle('Send message')).toBeEnabled();
    });

    it('shows an unavailable preview honestly instead of claiming zero sources', () => {
        mount({ status: 'unavailable' });
        expect(screen.getByText('Isoform could not check what it knows about this. Try opening the item from Today again.')).toBeVisible();
        expect(screen.queryByText(/nothing on file yet/)).not.toBeInTheDocument();
    });

    it.each([
        ['en', { memories: 2, activities: 4, people: 1 }, "Isoform could not load this item's details, so it adds today's brief instead."],
        ['en', undefined, "Isoform could not load this item's details, so it answers from what it already knows."],
        ['zh-CN', { memories: 2, activities: 4, people: 1 }, 'Isoform 没能读取这一项的详情，改为附上今天的简报。'],
        ['zh-CN', undefined, 'Isoform 没能读取这一项的详情，会根据它已了解的情况来回答。'],
    ] as const)('says quietly what a %s draft carries when its item could not be prepared', async (language, counts, note) => {
        await i18n.changeLanguage(language);
        mount(counts ? { status: 'fallback', counts } : { status: 'fallback' });
        expect(screen.getByText(note)).toBeVisible();
        // The draft stays sendable: the note never blocks the request.
        expect(screen.getByRole('button', { name: language === 'en' ? 'Send message' : /发送/ })).toBeEnabled();
    });

    it.each([
        ['en', 'Observation is paused, so Isoform answers without your recent activity.'],
        ['zh-CN', '观察已暂停，Isoform 回答时不会用到你最近的活动。'],
    ] as const)('says quietly in %s that a paused draft carries no observed context', async (language, note) => {
        await i18n.changeLanguage(language);
        mount({ status: 'paused' });
        expect(screen.getByText(note)).toBeVisible();
        expect(screen.getByRole('button', { name: language === 'en' ? 'Send message' : /发送/ })).toBeEnabled();
    });

    it('does not add a context line to an ordinary draft', () => {
        const { view } = mount();
        expect(view.container.querySelector('[data-today-context-preview]')).not.toBeInTheDocument();
    });

    it.each([
        ['en', 'Review before sending'],
        ['zh-CN', '发送前请先检查'],
    ])('marks a recovered rejected draft for review in %s', async (language, expected) => {
        await i18n.changeLanguage(language);
        const { props, view } = mount();
        view.rerender(<Composer {...props} queuedMessages={[{ prompt: 'Review launch plan', images: [], files: [], awaitingReview: true }]} />);
        expect(screen.getByText(expected)).toBeVisible();
    });
});

it('puts chat scope next to attachments in the bottom row, outside the text line', () => {
    const { props, view } = mount();
    view.rerender(<Composer {...props} scopeControl={<button type="button" aria-label="Scope">Scope</button>} />);
    const toolbar = screen.getByTestId('composer-toolbar');
    expect(toolbar).toContainElement(screen.getByRole('button', { name: 'Scope' }));
    expect(toolbar).toContainElement(screen.getByRole('button', { name: i18n.t('app:composer.attachments.attachFiles') }));
    expect(screen.getByRole('textbox').parentElement).not.toContainElement(screen.getByRole('button', { name: 'Scope' }));
});

it.each(['paper', 'onedark'])('assigns the primary fill only to the enabled send control in %s', (theme) => {
    document.documentElement.dataset.theme = theme;
    const { props, view } = mount();
    const send = () => screen.getByRole('button', { name: i18n.t('app:composer.actions.sendMessage') });
    expect(send()).toHaveClass('patina-verdigris');
    expect(view.container.querySelectorAll('.patina-verdigris')).toHaveLength(1);
    view.rerender(<Composer {...props} prompt="" />);
    expect(send()).toBeDisabled();
    expect(send()).not.toHaveClass('patina-verdigris');
    view.rerender(<Composer {...props} streaming prompt="" />);
    expect(view.container.querySelector('.patina-verdigris')).toBeNull();
});
