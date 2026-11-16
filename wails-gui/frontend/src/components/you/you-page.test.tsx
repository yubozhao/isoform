import { fireEvent, render, screen } from '@testing-library/react';
import { useState } from 'react';
import { describe, expect, it } from 'vitest';
import '@/i18n';
import enMemory from '@/i18n/locales/en/memory.json';
import { YouPage, type YouPageTab } from './you-page';
import { claimKeyAction } from '@/components/about-you/new-this-week';

function Harness({ initial = 'about' as YouPageTab }) {
    const [tab, setTab] = useState<YouPageTab>(initial);
    return <YouPage tab={tab} onTabChange={setTab}><div data-testid="tab-body">{tab}</div></YouPage>;
}

describe('YouPage', () => {
    // computer-you v1.0.0 rules 1, 2 and 5.
    it('is one page with the four tabs, and Agent of You says only what it will be', () => {
        render(<Harness />);
        expect(screen.getByRole('heading', { name: enMemory.you.title })).toBeInTheDocument();
        expect(screen.getByText(enMemory.you.sub)).toBeInTheDocument();
        const tabs = screen.getAllByRole('tab').map((tab) => tab.textContent);
        expect(tabs).toEqual([enMemory.you.tabAbout, enMemory.you.tabHowYouDo, enMemory.you.tabPeople, enMemory.you.tabAgent + enMemory.you.soon]);
        expect(screen.getByTestId('tab-body')).toHaveTextContent('about');
        fireEvent.click(screen.getByRole('tab', { name: /Agent of You/ }));
        expect(screen.getByTestId('you-agent-soon')).toHaveTextContent(enMemory.you.agentSoon);
        expect(screen.queryByTestId('tab-body')).toBeNull();
        fireEvent.click(screen.getByRole('tab', { name: enMemory.you.tabPeople }));
        expect(screen.getByTestId('tab-body')).toHaveTextContent('people');
    });

    it('moves between tabs with the arrow keys', () => {
        render(<Harness initial="people" />);
        fireEvent.keyDown(screen.getByRole('tab', { selected: true }), { key: 'ArrowRight' });
        expect(screen.getByRole('tab', { selected: true })).toHaveTextContent(enMemory.you.tabAgent);
        fireEvent.keyDown(screen.getByRole('tab', { selected: true }), { key: 'ArrowRight' });
        expect(screen.getByRole('tab', { selected: true })).toHaveTextContent(enMemory.you.tabAbout);
    });
});

describe('claimKeyAction', () => {
    const key = (k: string, extra: Partial<KeyboardEvent> = {}, target: Partial<HTMLElement> = { tagName: 'ARTICLE' }) =>
        claimKeyAction({ key: k, metaKey: false, ctrlKey: false, altKey: false, target: target as unknown as EventTarget, ...extra });
    it('answers Y, N and E, moves with the arrows, and leaves typing alone', () => {
        expect(key('y')).toBe('yes');
        expect(key('N')).toBe('no');
        expect(key('e')).toBe('edit');
        expect(key('ArrowDown')).toBe('next');
        expect(key('ArrowUp')).toBe('previous');
        expect(key('y', { metaKey: true })).toBeNull();
        expect(key('y', {}, { tagName: 'TEXTAREA' })).toBeNull();
        expect(key('x')).toBeNull();
        expect(key('y', { isComposing: true } as Partial<KeyboardEvent>)).toBeNull();
        expect(key('y', {}, { tagName: 'SELECT' })).toBeNull();
    });
});
