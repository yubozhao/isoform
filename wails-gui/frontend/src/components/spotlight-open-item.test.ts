import { describe, expect, it } from 'vitest';
import appSource from '../App.tsx?raw';

// Quick chat's Open (computer-chat v1.1.0 rule 7) opens the item itself on
// Today, through Today's own opener, not just the Today page.
describe("Quick chat's Open", () => {
    it('opens the matched item on Today with its id', () => {
        const handler = appSource.slice(appSource.indexOf('EventsOn("spotlight:open-work"'), appSource.indexOf('EventsOn("spotlight:chat-work"'));
        expect(handler).toContain('openTodayItemRef.current?.(itemId)');
        expect(handler).not.toContain('openTodayRef.current?.()');
    });
});
