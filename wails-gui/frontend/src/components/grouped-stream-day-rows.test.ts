import { describe, expect, it, afterEach, beforeEach } from 'vitest';
import { chatThreadUpdates } from '@/lib/main-chat-view';
import { dayRowsVisibility } from './grouped-stream-list';

// These cover the rollback switch: Isoform's updates back in Chat's thread.
beforeEach(() => { chatThreadUpdates.shown = true; });
afterEach(() => { chatThreadUpdates.shown = false; });


describe('a day keeps its divider while its updates are hidden', () => {
    it('shows a routine-only day with the fold when updates are shown', () => {
        expect(dayRowsVisibility({ items: [], routine: [{}] }, false)).toEqual({ divider: true, fold: true, removed: false });
    });
    it('keeps the divider of a routine-only day when updates are hidden (R63)', () => {
        expect(dayRowsVisibility({ items: [], routine: [{}, {}] }, true)).toEqual({ divider: true, fold: false, removed: false });
    });
    it('shows a day of conversation rows with no fold', () => {
        expect(dayRowsVisibility({ items: [{}], routine: [] }, false)).toEqual({ divider: true, fold: false, removed: false });
    });
    it('drops only a day with nothing in it', () => {
        expect(dayRowsVisibility({ items: [], routine: [] }, false)).toEqual({ divider: false, fold: false, removed: false });
    });
});
