import { render, screen } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import i18n from '@/i18n';
import type { StreamItem } from '@/types';
import { fromStreamItemData, TODAY_CONTEXT_TOOL } from '@/lib/stream-helpers';
import { StreamCard } from './stream-card';
import { GroupedStreamList } from './grouped-stream-list';

vi.mock('../../wailsjs/go/main/App', () => ({ OpenAutomationDeliverable: vi.fn() }));
vi.mock('../../wailsjs/runtime/runtime', () => ({ EventsOn: vi.fn(() => () => {}), EventsEmit: vi.fn() }));

function receipt(content = JSON.stringify({ memories: 2, activities: 3, people: 4 }), kind: 'context_status' | 'system' = 'context_status'): StreamItem {
    return { id: 'today-context-1', kind, toolName: TODAY_CONTEXT_TOOL, content };
}

describe('Today context status', () => {
    beforeEach(async () => {
        await i18n.changeLanguage('en');
    });

    it('names what the message was attached to', async () => {
        const counts = JSON.stringify({ memories: 1, activities: 0, people: 1 });
        const { unmount } = render(<StreamCard item={{ ...receipt(counts), toolInput: JSON.stringify({ userItemId: 'u1', about: 'Prep for Lena', aboutKind: 'handoff' }) }} />);
        expect(screen.getByText('About: Prep for Lena')).toHaveAttribute('data-chat-about');
        unmount();
        render(<StreamCard item={{ ...receipt(counts), toolInput: { userItemId: 'u2', about: 'The Thursday review is slipping.', aboutKind: 'brief' } }} />);
        expect(screen.getByText('About your brief: \u201cThe Thursday review is slipping.\u201d')).toBeInTheDocument();
    });

    it('shows no About line for a receipt without one', () => {
        const { container } = render(<StreamCard item={{ ...receipt(), toolInput: JSON.stringify({ userItemId: 'u1' }) }} />);
        expect(container.querySelector('[data-chat-about]')).toBeNull();
    });

    it.each(['context_status', 'system'] as const)('renders %s metadata as a localized tertiary line, without raw JSON', (kind) => {
        const item = receipt(JSON.stringify({ memories: 2, activities: 3, people: 4, sourceText: 'private source' }), kind);
        render(<StreamCard item={item} />);
        const line = screen.getByText('Context: 2 memories, 3 activities, 4 people');
        expect(line).toHaveAttribute('data-today-context-status');
        expect(line).toHaveClass('text-muted/70');
        expect(screen.queryByText(/private source|memories":/)).not.toBeInTheDocument();
    });

    it('uses singular counts and keeps a truthful zero count', () => {
        render(<StreamCard item={receipt(JSON.stringify({ memories: 1, activities: 1, people: 0 }))} />);
        expect(screen.getByText('Context: 1 memory, 1 activity, 0 people')).toBeInTheDocument();
    });

    it('localizes every count in Chinese', async () => {
        await i18n.changeLanguage('zh-CN');
        render(<StreamCard item={receipt()} />);
        expect(screen.getByText('上下文：2 条记忆，3 项活动，4 位相关人员')).toBeInTheDocument();
    });

    it.each([
        'not JSON',
        'null',
        '[]',
        '{"memories":2,"activities":3}',
        '{"memories":"2","activities":3,"people":4}',
        '{"memories":-1,"activities":3,"people":4}',
        '{"memories":1.5,"activities":3,"people":4}',
        '{"memories":1e30,"activities":3,"people":4}',
        '{"error":{"code":-1,"message":"private source"}}',
    ])('hides a malformed receipt instead of exposing its content: %s', (content) => {
        const { container } = render(<StreamCard item={receipt(content)} />);
        expect(container).toBeEmptyDOMElement();
    });

    it.each([false, true])('keeps counts visible once beside the response when streaming=%s', (streaming) => {
        const { container } = render(
            <GroupedStreamList
                streaming={streaming}
                items={[
                    { id: 'u1', kind: 'user', content: 'Tell me about the launch.' },
                    receipt(),
                    { id: 't1', kind: 'thinking', content: 'Look up relevant details.' },
                    { id: 'a1', kind: 'assistant', content: 'The launch is ready.' },
                ]}
            />,
        );
        expect(screen.getByText('Context: 2 memories, 3 activities, 4 people')).toBeVisible();
        expect(container.querySelectorAll('[data-today-context-status]')).toHaveLength(1);
        expect(screen.queryByText(/memories":/)).not.toBeInTheDocument();
    });

    it('attaches a late receipt to its originating turn, also after saved metadata reloads', () => {
        const item = receipt();
        const savedReceipt = fromStreamItemData({
            id: item.id, kind: item.kind, content: item.content, toolName: item.toolName,
            sortOrder: 0,
            toolInput: JSON.stringify({ userItemId: 'u1' }),
        });
        render(
            <GroupedStreamList
                streaming={false}
                items={[
                    { id: 'u1', kind: 'user', content: 'Tell me about the launch.' },
                    { id: 'a1', kind: 'assistant', content: 'The launch is ready.' },
                    { id: 'u2', kind: 'user', content: 'A separate question.' },
                    { id: 'a2', kind: 'assistant', content: 'A separate answer.' },
                    savedReceipt,
                ]}
            />,
        );
        const line = screen.getByText('Context: 2 memories, 3 activities, 4 people');
        expect(line.parentElement).toContainElement(screen.getByText('The launch is ready.'));
        expect(line.parentElement).not.toContainElement(screen.getByText('A separate answer.'));
        expect(screen.getAllByText('Context: 2 memories, 3 activities, 4 people')).toHaveLength(1);
    });

    it('does not attach a receipt to an unrelated turn when its user row is not loaded', () => {
        render(
            <GroupedStreamList
                streaming={false}
                items={[
                    { id: 'u2', kind: 'user', content: 'A separate question.' },
                    { ...receipt(), toolInput: { userItemId: 'u1' } },
                ]}
            />,
        );
        expect(screen.queryByText(/Context:/)).not.toBeInTheDocument();
    });
});
