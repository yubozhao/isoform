import { fireEvent, render, screen, within } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import { TopicComposer } from './TopicComposer';

// Layout/component guard only: synthetic labels, no backend integration.
describe('Topic composer bottom row', () => {
    it('keeps the text line free and detaches the topic from the bottom toolbar without sending', () => {
        const send = vi.fn();
        const remove = vi.fn();
        render(<TopicComposer draft="" prepared={false} onDraftChange={vi.fn()} onSend={send} sending={false}
            inputLabel="Message" placeholder="Message Isoform" sendLabel="Send" note="Topic attached"
            chip={{ name: 'Halcyon ERP sync', removeLabel: 'Remove topic', onRemove: remove }} />);
        const text = screen.getByRole('textbox');
        expect(text.parentElement?.querySelector('button, svg')).toBeNull();
        const toolbar = screen.getByTestId('topic-composer-toolbar');
        expect(within(toolbar).getByText('Halcyon ERP sync')).toBeInTheDocument();
        expect(within(toolbar).getByRole('button', { name: 'Send' })).toBeInTheDocument();
        fireEvent.click(within(toolbar).getByRole('button', { name: 'Remove topic' }));
        expect(remove).toHaveBeenCalledOnce();
        expect(send).not.toHaveBeenCalled();
    });
});
