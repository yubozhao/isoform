import { fireEvent, render, screen, within } from '@testing-library/react';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import i18n from '@/i18n';
import { TodayBriefSection } from './TodayBriefSection';
import { TodayStatus } from './TodayStatus';
import { todayNextBrief, dayShapeFixture } from './today-next.fixture';

vi.mock('@/hooks/useFirstBriefSeen', () => ({ useFirstBriefSeen: () => ({ current: null }) }));
const handlers = { onStartChat: vi.fn(), onOpenTopic: vi.fn(), onOpenBriefConversation: vi.fn(async () => true), onRunNow: vi.fn(), onOpenMemory: vi.fn(), onOpenAutomations: vi.fn(), onOpenActivity: vi.fn(), onOpenPeople: vi.fn() };
function show(overrides = {}) {
    return render(<TodayBriefSection view={{ brief: todayNextBrief, building: false, enabled: true, nextAt: '', nextSlot: '', empty: false, missing: [] }} loading={false} make="idle" nextAllowedAt="" onMakeNow={vi.fn()} handlers={handlers} dayShape={{ ...dayShapeFixture, hold: null }} dayShapeDate="2026-10-23" {...overrides} />);
}
beforeEach(async () => {
    vi.clearAllMocks();
    // A brief shows in full only on its own day (R52).
    vi.useFakeTimers({ toFake: ['Date'] });
    vi.setSystemTime(new Date(todayNextBrief.asOf));
    await i18n.changeLanguage('en');
});
afterEach(() => { vi.useRealTimers(); });

it('keeps paragraph and actionable chip text byte-identical, with topic buttons inside the prose', () => {
    const narrative = todayNextBrief.narrative.map((p, i) => i === 0 ? { ...p, text: `  ${p.text}\n` } : p);
    show({ view: { brief: { ...todayNextBrief, narrative }, enabled: true, empty: false, missing: [] } });
    const text = [...screen.getByTestId('brief-narrative').querySelectorAll('p'), screen.getByTestId('brief-closing-question')].map((p) => p.textContent);
    expect(text).toEqual(narrative.map((p) => p.text));
    expect(screen.getAllByTestId('brief-question-chip').map((c) => c.textContent)).toEqual(['Review the Halcyon launch memo']);
    expect(within(screen.getByTestId('brief-narrative').querySelector('p')!).getByRole('button', { name: 'Priya' })).toBeInTheDocument();
    expect(screen.getByTestId('brief-audio-toggle')).toHaveAccessibleDescription('Morning brief · 7:30 AM · 0:48');
});

it('renders the brief without unsupported meeting prep chips', () => {
    show();
    const card = screen.getByTestId('brief-card').cloneNode(true) as HTMLElement;
    card.querySelector('[data-testid="today-day-strip"]')?.remove();
    // The private-items line sits at the card's foot (v1.1.5 #4).
    expect(card.lastElementChild?.getAttribute('data-testid')).toBe('brief-sensitive-excluded');
    card.querySelector('[data-testid="brief-sensitive-excluded"]')?.remove();
    const paragraphs = todayNextBrief.narrative.slice(0, -1).map((p, index) => p.text + todayNextBrief.cards!.filter((c) => c.kind === 'question' && c.paragraph === index && c.label === 'Review the Halcyon launch memo').map((c) => c.label).join('')).join('');
    expect(card.textContent).toBe('Hear your briefMorning brief · 7:30 AM · 0:48' + paragraphs
        + todayNextBrief.narrative.at(-1)!.text + 'Draft itNot now');
});

it('places the strip after the recording and before the first paragraph; removes the legacy text box', () => {
    show();
    const strip = screen.getByTestId('today-day-strip');
    expect(screen.getByTestId('brief-card')).toContainElement(strip);
    expect(screen.getByTestId('brief-audio-toggle').compareDocumentPosition(strip) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(strip.compareDocumentPosition(screen.getByTestId('brief-narrative')) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(screen.queryByText('The shape of today')).not.toBeInTheDocument();
    expect(screen.queryByText('Legacy shape times must not repeat.')).not.toBeInTheDocument();
});

it('hides a stored hold question, its answers, and its strip block', () => {
    const narrative = todayNextBrief.narrative.map((paragraph, index) => index === todayNextBrief.narrative.length - 1
        ? { ...paragraph, text: 'Hold the memo at 3:45 PM?' } : paragraph);
    show({ dayShape: dayShapeFixture, view: { brief: { ...todayNextBrief, narrative }, enabled: true, empty: false, missing: [] } });
    expect(screen.queryByTestId('today-day-hold')).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Draft it' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Not now' })).not.toBeInTheDocument();
    expect(screen.queryByTestId('brief-closing-question')).not.toBeInTheDocument();
    expect(screen.queryByTestId('brief-audio-toggle')).not.toBeInTheDocument();
    expect(handlers.onStartChat).not.toHaveBeenCalled();
});

it('hides a tagged hold even when the day shape is unavailable', () => {
    const narrative = todayNextBrief.narrative.map((paragraph, index) => index === todayNextBrief.narrative.length - 1
        ? { ...paragraph, hold: dayShapeFixture.hold } : paragraph);
    show({ dayShape: null, view: { brief: { ...todayNextBrief, narrative }, enabled: true, empty: false, missing: [] } });
    expect(screen.queryByRole('button', { name: 'Draft it' })).not.toBeInTheDocument();
    expect(screen.queryByTestId('brief-closing-question')).not.toBeInTheDocument();
});

it('keeps the privacy line at the foot of its brief while retaining chips when observing is paused', () => {
    show({ observationPaused: true });
    const card = screen.getByTestId('brief-card');
    const note = screen.getByTestId('brief-sensitive-excluded');
    expect(card).toContainElement(note);
    expect(card.lastElementChild).toBe(note);
    expect(screen.queryByTestId('today-quiet-footer')).not.toBeInTheDocument();
    expect(screen.queryByTestId('brief-paused-cards')).not.toBeInTheDocument();
    expect(screen.getAllByTestId('brief-question-chip')).toHaveLength(1);
    expect(screen.queryByTestId('today-more')).not.toBeInTheDocument();
});

it('joins date and statuses on one meta line, with Make it now only in the card', () => {
    vi.setSystemTime(new Date(2026, 9, 23, 13));
    render(<TodayStatus date="Friday, October 23" brief={{ enabled: true, lastRunAt: '', lastSlot: '', nextRunAt: '', nextSlot: '', pendingSlot: 'morning', pendingAt: '2026-10-23T07:00:00-07:00', latestBriefId: '', currentBriefId: '', missedRuns: 2, failingSlot: '', failedAttempts: 0 }} />);
    expect(screen.getByTestId('today-status')).toHaveClass('flex-wrap');
    expect(screen.getByTestId('today-status')).toHaveTextContent("Your morning brief didn't come at 7:00 AM.");
    expect(screen.queryByRole('button', { name: 'Make it now' })).not.toBeInTheDocument();
});

it.each(['Hold the memo at 3:45 PM?', '要为备忘录预留时间吗？'])('hides an untagged stored hold question: %s', (text) => {
    const narrative = todayNextBrief.narrative.map((paragraph, index) => index === todayNextBrief.narrative.length - 1
        ? { ...paragraph, text } : paragraph);
    show({ dayShape: null, view: { brief: { ...todayNextBrief, narrative }, enabled: true, empty: false, missing: [] } });
    expect(screen.queryByTestId('brief-closing-question')).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Draft it' })).not.toBeInTheDocument();
});

it.each(['en', 'zh-CN'])('preserves exact paragraph, closing and chip copy in %s', async (language) => {
    await i18n.changeLanguage(language);
    const paragraph = '  Review at 9:00 AM.\nKeep this wording.  ';
    const closing = '  Draft the memo?  ';
    const chip = 'Review at 9:00 AM';
    const brief = { ...todayNextBrief, narrative: [{ text: paragraph, refs: [] }, { text: closing, refs: [] }],
        // A closing question shows only with its answers (today-review v1.1.10 C).
        cards: [{ id: 'verbatim-chip', kind: 'question', mode: 'send', label: chip, message: 'Review the memo.', paragraph: 0, refs: [] },
            { id: 'verbatim-answer', kind: 'action', mode: 'send', label: 'Draft it', message: 'Draft the memo.', answerTo: 'closing', refs: [] }],
    };
    show({ view: { brief, enabled: true, empty: false, missing: [] } });
    expect(screen.getByTestId('brief-narrative').querySelector('p')?.textContent).toBe(paragraph);
    expect(screen.getByTestId('brief-closing-question').textContent).toBe(closing);
    expect(screen.getByTestId('brief-question-chip').textContent).toBe(chip);
    expect(screen.queryByTestId('brief-section-moved')).not.toBeInTheDocument();
});
it('keeps an accessible topic action when evidence links a differently named topic', () => {
    const paragraph = "Halcyon's launch memo is due Tuesday.";
    show({ view: { brief: { ...todayNextBrief, narrative: [{ text: paragraph, refs: [], topicId: 'halcyon', topicName: 'Halcyon launch memo' }], cards: [] }, enabled: true, empty: false, missing: [] } });
    expect(screen.getByTestId('brief-narrative').querySelector('p')?.textContent).toBe(paragraph);
    fireEvent.click(screen.getByRole('button', { name: 'Halcyon launch memo' }));
    expect(handlers.onOpenTopic).toHaveBeenCalledWith('halcyon');
});
it('keeps the missed-brief link in the shared accent color', () => {
    vi.setSystemTime(new Date(2026, 9, 23, 13));
    render(<TodayStatus onMissedWhy={vi.fn()} brief={{ enabled: true, lastRunAt: '', lastSlot: '', nextRunAt: '', nextSlot: '', pendingSlot: 'morning', pendingAt: '2026-10-23T07:00:00-07:00', latestBriefId: '', currentBriefId: '', missedRuns: 2, failingSlot: '', failedAttempts: 0 }} />);
    const link = screen.getByRole('button', { name: 'Why' });
    expect(link).toHaveClass('text-accent');
    expect(link).not.toHaveClass('text-ink');
});

it('uses Not now for the closing decline in English and Chinese', async () => {
    const view = show();
    expect(within(screen.getByTestId('brief-conversation-actions')).getByRole('button', { name: 'Not now' })).toBeInTheDocument();
    await i18n.changeLanguage('zh-CN');
    expect(within(screen.getByTestId('brief-conversation-actions')).getByRole('button', { name: '暂不' })).toBeInTheDocument();
    view.unmount();
});
