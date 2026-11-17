import { act, cleanup, fireEvent, render, screen } from '@testing-library/react';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { chatThreadUpdates } from '@/lib/main-chat-view';
import i18n from '@/i18n';
import { MainChatFilterRow } from './main-chat-filter-row';
import type { TodayTopic } from '../today/topics';

// These cover the rollback switch: Isoform's updates back in Chat's thread.
beforeEach(() => { chatThreadUpdates.shown = true; });
afterEach(() => { chatThreadUpdates.shown = false; });


let width = 768;
let resize: () => void;
const topics: TodayTopic[] = Array.from({ length: 12 }, (_, i) => ({
    id: `t${i}`, name: `Topic ${i}`, kind: 'work', pinned: false,
    status: i === 8 ? 'waiting' : 'moving', latest: { text: 'Updated', at: new Date(2026, 9, i + 1).toISOString() },
}));
beforeEach(async () => {
    await i18n.changeLanguage('en');
    vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockImplementation(function (this: HTMLElement) {
        const element = this;
        const size = element.hasAttribute('data-header-row') ? width : element.hasAttribute('data-topic-measure') ? 76 : element.hasAttribute('data-header-fixed') ? 230 : element.hasAttribute('data-header-hide') ? 150 : 70;
        return { width: size, height: 32, x: 0, y: 0, top: 0, left: 0, right: size, bottom: 32, toJSON() {} };
    });
    vi.stubGlobal('ResizeObserver', class {
        constructor(callback: () => void) { resize = callback; }
        observe() {} unobserve() {} disconnect() {}
    });
});
afterEach(() => { cleanup(); vi.restoreAllMocks(); vi.unstubAllGlobals(); });
function header(list = topics, filter: Parameters<typeof MainChatFilterRow>[0]['filter'] = { kind: 'all' }) {
    const onFilter = vi.fn();
    const view = render(<MainChatFilterRow filter={filter} onFilter={onFilter} topics={list} hideRoutine={false} routineCount={2} hideBusy={false} hideFailed={false} onToggleHide={vi.fn()} />);
    return { ...view, onFilter };
}
it.each([768, 420, 360])('keeps the six topic cards in one row at %i with ordered overflow', (size) => {
    width = size;
    const { container, onFilter } = header();
    expect(container.querySelector('.flex-wrap')).toBeNull();
    const visible = [...container.querySelectorAll('[data-main-chat-filter^="topic:"]')].filter(el => !el.closest('[aria-hidden="true"]'));
    const more = screen.getByRole('button', { name: 'More' });
    expect(more).toBeInTheDocument();
    fireEvent.pointerDown(more, { button: 0, ctrlKey: false });
    const hidden = screen.getAllByRole('menuitem');
    expect([...visible.map(el => el.textContent), ...hidden.map(el => el.textContent)]).toEqual(['Topic 8', 'Topic 11', 'Topic 10', 'Topic 9', 'Topic 7', 'Topic 6']);
    fireEvent.click(hidden[0]);
    expect(onFilter).toHaveBeenCalledWith({ kind: 'topic', topicId: hidden[0].getAttribute('data-topic-id') });
});
it('recalculates overflow when the column resizes', () => {
    width = 768;
    header();
    const count = () => [...document.querySelectorAll('[data-main-chat-filter^="topic:"]')].filter(el => !el.closest('[aria-hidden="true"]')).length;
    const wide = count();
    width = 420;
    act(() => resize());
    expect(count()).toBeLessThan(wide);
});
it('shows only the six topics Today shows, pinned first, by name (today-review v1.1.4 #8)', () => {
    const now = new Date();
    const day = (back: number) => { const d = new Date(now.getFullYear(), now.getMonth(), now.getDate() - back); return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`; };
    width = 4000;
    header([
        { ...topics[0], name: 'ftb.ca.gov', activityDays: [day(0), day(1)] },
        { ...topics[1], name: 'msg', activityDays: [day(0), day(2)] },
        { ...topics[2], name: 'Linen trousers', activityDays: [day(0)] },
        { ...topics[3], name: 'Riffado', activityDays: [day(0), day(3)] },
        { ...topics[4], name: 'Pinned plan', pinned: true, activityDays: [] },
    ]);
    const names = [...document.querySelectorAll('[data-main-chat-filter^="topic:"]')].filter(el => !el.closest('[aria-hidden="true"]')).map(el => el.textContent);
    expect(names).toEqual(['Pinned plan', 'Riffado']);
});
it('omits redacted titles and the chat itself', () => {
    header([{ ...topics[0], name: '[REDACTED: private]' }, { ...topics[1], name: 'Isoform Main chat' }, topics[2]]);
    expect(screen.queryByText(/REDACT|Isoform Main chat/)).not.toBeInTheDocument();
});
it('keeps a topic when it exactly fits beside More', () => {
    width = 544; // Fixed controls 380, outer gaps 12, topic 76, gap 6, More 70.
    const { container } = header();
    const visible = [...container.querySelectorAll('[data-main-chat-filter^="topic:"]')].filter(el => !el.closest('[aria-hidden="true"]'));
    expect(visible.map(el => el.textContent)).toEqual(['Topic 8']);
});

it('announces the selected overflow topic to screen readers', () => {
    width = 420;
    header(topics, { kind: 'topic', topicId: 't6' });
    fireEvent.pointerDown(screen.getByRole('button', { name: 'More' }), { button: 0, ctrlKey: false });
    expect(screen.getByRole('menuitem', { name: 'Topic 6' })).toHaveAttribute('aria-current', 'true');
    expect(screen.getByRole('menuitem', { name: 'Topic 8' })).not.toHaveAttribute('aria-current');
});
