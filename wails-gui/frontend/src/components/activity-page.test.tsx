import { resetActivitySessionCache } from './timeline/activity-session-cache';
import { resetPageQueries } from '@/lib/page-queries';
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { useState } from 'react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import i18n from '@/i18n';
import enActivity from '@/i18n/locales/en/activity.json';
import { ActivityPage, eventActionLabel, timelineGroupAt, EventFramePopup, SessionPeopleChips, SessionSpeakerChips, type ActivitySectionId } from './activity-page';
import type { activitymon } from '../../wailsjs/go/models';
import type { CronJobsProp } from './timeline/useCalendarData';
import type { MobileBriefActivityTarget } from '@/lib/wails-shim-app';

// Every Wails binding resolves through the shim (vite aliases wailsjs/go/main/App
// to it), so one Proxy mock covers the whole import graph of the page — same
// pattern as activity-day-view.test.tsx.
const fixture = vi.hoisted(() => ({
    bindings: new Map<string, ReturnType<typeof vi.fn>>(),
    listeners: new Map<string, Set<(payload: unknown) => void>>(),
}));

vi.mock('@/lib/wails-shim-app', () => new Proxy({}, {
    has: () => true,
    get: (_target, name: string) => {
        if (name === 'then') return undefined;
        if (!fixture.bindings.has(name)) fixture.bindings.set(name, vi.fn().mockResolvedValue([]));
        return fixture.bindings.get(name);
    },
}));

vi.mock('../../wailsjs/runtime/runtime', () => ({
    EventsOn: (name: string, handler: (payload: unknown) => void) => {
        const listeners = fixture.listeners.get(name) ?? new Set();
        listeners.add(handler);
        fixture.listeners.set(name, listeners);
        return () => listeners.delete(handler);
    },
    ClipboardSetText: vi.fn(),
}));

vi.mock('sonner', () => ({ toast: Object.assign(vi.fn(), { error: vi.fn(), success: vi.fn() }) }));

function binding(name: string) {
    if (!fixture.bindings.has(name)) fixture.bindings.set(name, vi.fn().mockResolvedValue([]));
    return fixture.bindings.get(name)!;
}

const cronJobs: CronJobsProp = {
    jobs: [],
    loading: false,
    toggleEnabled: vi.fn(),
    deleteJob: vi.fn(),
    runNow: vi.fn(),
    updateJob: vi.fn(),
    loadRuns: vi.fn(),
    loadRunLogs: vi.fn(),
};

/** Harness holding the section state the way App does: the page is
 *  controlled, and the pick is remembered outside it. */
function Harness({ initialSection = 'yourDay' as ActivitySectionId, initialDate = null as string | null, initialFocus = null as MobileBriefActivityTarget | null, initialConversationId = null as string | null }) {
    const [section, setSection] = useState<ActivitySectionId>(initialSection);
    const [focus, setFocus] = useState(initialFocus);
    return (
        <ActivityPage
            initialDate={initialDate}
            initialConversationId={initialConversationId}
            noticedFocus={focus}
            onNoticedFocusConsumed={() => setFocus(null)}
            cronJobs={cronJobs}
            section={section}
            onSectionChange={setSection}
        />
    );
}

describe('ActivityPage section panel', () => {
    beforeEach(async () => {
        resetActivitySessionCache();
        resetPageQueries();
        await i18n.changeLanguage('en');
        localStorage.clear();
        fixture.listeners.clear();
        for (const mock of fixture.bindings.values()) mock.mockReset().mockResolvedValue([]);
        Element.prototype.scrollIntoView = vi.fn();
        // The timeline detail measures its scroll area.
        globalThis.ResizeObserver ??= class {
            observe() {}
            unobserve() {}
            disconnect() {}
        } as unknown as typeof ResizeObserver;
    });

    it.each(['paper', 'onedark'])('exposes the shared app ground through the whole Activity page in %s', async (theme) => {
        document.documentElement.dataset.theme = theme;
        let container!: HTMLElement;
        await act(async () => { ({ container } = render(<main className="patina-ground"><Harness /></main>)); });
        const ground = container.querySelector('main')!;
        const page = ground.firstElementChild!;
        expect(ground).toHaveClass('patina-ground');
        expect(page).not.toHaveClass('bg-background', 'bg-panel', 'patina-ground');
        expect((page as HTMLElement).style.backgroundColor).toBe('');
        expect(ground.querySelector('.patina-ground')).toBeNull();
    });

    it.each([true, false])('uses neutral ink when selecting a person (self: %s)', (self) => {
        const onSelect = vi.fn();
        const name = self ? 'You' : 'Alex';
        render(<SessionPeopleChips principals={[{ principalId: 'person', displayName: name, kind: self ? 'self' : 'person', qualifiedDisplayName: name, firstSeenAt: 0, lastSeenAt: 1 }]} selectedName={name} onSelect={onSelect} />);
        const selected = screen.getByRole('button', { name, pressed: true });
        expect(selected).toHaveClass('bg-surface-active', 'text-ink', 'ring-accent');
        expect(selected).not.toHaveClass('bg-accent-2', 'text-solid-btn-fg');
        fireEvent.click(selected);
        expect(onSelect).toHaveBeenCalledWith(name);
    });

    it.each([true, false])('uses neutral ink when selecting a voice (self: %s)', (self) => {
        const onSelect = vi.fn();
        const label = self ? 'You' : 'Speaker 1';
        render(<SessionSpeakerChips clusters={[{ label, isSelf: self, utteranceCount: 3, firstSeenAt: 0, lastSeenAt: 1, eventTimestamps: [] }]} selectedLabel={label} onSelect={onSelect} />);
        const selected = screen.getByRole('button', { pressed: true });
        expect(selected).toHaveClass('bg-surface-active', 'text-ink', 'ring-accent');
        expect(selected).not.toHaveClass('bg-accent-2', 'text-solid-btn-fg');
        fireEvent.click(selected);
        expect(onSelect).toHaveBeenCalledWith(label);
    });

    it('shows one section at a time, and the tabs pick it', async () => {
        await act(async () => {
            render(<Harness initialSection="yourDay" />);
        });
        // Your day is the feed; What you worked on is the Screen tab's.
        expect(screen.getByTestId('activity-feed')).toBeInTheDocument();
        expect(screen.queryByRole('heading', { name: enActivity.day.workedOn })).toBeNull();

        fireEvent.click(document.querySelector('[data-activity-section="workedOn"]') as HTMLElement);
        await waitFor(() => {
            expect(screen.queryByTestId('activity-feed')).toBeNull();
            expect(screen.getByRole('heading', { name: enActivity.day.workedOn })).toBeInTheDocument();
        });
        expect(document.querySelector('[data-activity-section="workedOn"]')).toHaveAttribute('aria-selected', 'true');
        expect(document.querySelector('[data-activity-section="yourDay"]')).toHaveAttribute('aria-selected', 'false');
    });

    // computer-activity v1.0.0 rule 1: the title with its sub-line under it.
    it('heads the page with Activity and its sub-line, and no section panel', async () => {
        await act(async () => {
            render(<Harness initialSection="yourDay" />);
        });
        expect(screen.getByRole('heading', { level: 1, name: enActivity.title })).toBeInTheDocument();
        expect(screen.getByText(enActivity.act.sub)).toBeInTheDocument();
        // The tabs replace the left section nav.
        expect(screen.queryByRole('navigation', { name: enActivity.sections.label })).toBeNull();
        expect(screen.getByRole('tablist', { name: enActivity.sections.label })).toBeInTheDocument();
    });

    it('moves between the five tabs with the arrow keys, wrapping at the ends', async () => {
        await act(async () => {
            render(<Harness initialSection="yourDay" />);
        });
        const tab = (id: string) => document.querySelector(`[data-activity-section="${id}"]`) as HTMLElement;
        expect(tab('yourDay')).toHaveAttribute('tabindex', '0');
        expect(tab('workedOn')).toHaveAttribute('tabindex', '-1');
        fireEvent.keyDown(tab('yourDay'), { key: 'ArrowRight' });
        await waitFor(() => expect(tab('workedOn')).toHaveAttribute('aria-selected', 'true'));
        expect(document.activeElement).toBe(tab('workedOn'));
        fireEvent.keyDown(tab('workedOn'), { key: 'ArrowLeft' });
        await waitFor(() => expect(tab('yourDay')).toHaveAttribute('aria-selected', 'true'));
        fireEvent.keyDown(tab('yourDay'), { key: 'ArrowLeft' });
        await waitFor(() => expect(tab('answers')).toHaveAttribute('aria-selected', 'true'));
        expect(document.activeElement).toBe(tab('answers'));
    });

    // computer-conformance-answers A4: one day at a time, previous and next beside the date.
    it('steps the day with previous and next on Your day and has no Day/Week switch', async () => {
        await act(async () => {
            render(<Harness initialDate="2026-10-15" />);
        });
        const picker = screen.getByTitle(enActivity.dayNav.pickDate);
        expect(picker).toHaveTextContent('Oct 15');
        fireEvent.click(screen.getByRole('button', { name: enActivity.dayNav.previous }));
        await waitFor(() => expect(screen.getByTitle(enActivity.dayNav.pickDate)).toHaveTextContent('Oct 14'));
        expect(binding('GetActivityFeed')).toHaveBeenLastCalledWith(new Date(2026, 9, 14).getTime(), new Date(2026, 9, 15).getTime());
        fireEvent.click(screen.getByRole('button', { name: enActivity.dayNav.next }));
        await waitFor(() => expect(screen.getByTitle(enActivity.dayNav.pickDate)).toHaveTextContent('Oct 15'));
        expect(screen.queryByRole('button', { name: 'Week' })).toBeNull();
        expect(screen.queryByText('Week')).toBeNull();
        expect(binding('GetWeeklySummary')).not.toHaveBeenCalled();
    });

    it('cannot step past today', async () => {
        await act(async () => {
            render(<Harness />);
        });
        expect(screen.getByTitle(enActivity.dayNav.pickDate)).toHaveTextContent(enActivity.dayNav.today);
        expect(screen.getByRole('button', { name: enActivity.dayNav.next })).toBeDisabled();
        fireEvent.click(screen.getByRole('button', { name: enActivity.dayNav.previous }));
        await waitFor(() => expect(screen.getByTitle(enActivity.dayNav.pickDate)).toHaveTextContent(enActivity.dayNav.yesterday));
    });

    it.each([
        ['yourDay', true], ['workedOn', true], ['agents', true], ['watch', false], ['answers', false], ['timeline', false],
    ] as const)('shows the date on %s: %s', async (section, shown) => {
        await act(async () => {
            render(<Harness initialSection={section} />);
        });
        if (shown) expect(screen.getByTestId('activity-day-nav')).toBeInTheDocument();
        else expect(screen.queryByTestId('activity-day-nav')).toBeNull();
    });

    // computer-activity v1.0.0 rule 2: the tabs are Your day, Screen, Watch
    // and Agents. What it learned moved to You.
    it('offers the tabs Your day, Screen, Watch, Agents and Your answers, and no What it learned', async () => {
        binding('GetWatchRecordings').mockResolvedValue([]);
        await act(async () => {
            render(<Harness initialSection="yourDay" />);
        });
        const tabs = [...document.querySelectorAll('[data-activity-section]')].map((row) => row.textContent);
        expect(tabs).toEqual([enActivity.day.yourDay, enActivity.act.tabScreen, enActivity.act.tabWatch, enActivity.act.tabAgents, enActivity.act.tabAnswers]);
        expect(document.querySelector('[data-activity-section="learned"]')).toBeNull();
    });

    // Scope cut B3: without Activity's extras three tabs stay, and a Screen
    // or Agents section left from before shows Your day.
    describe('without the Activity extras', () => {
        beforeEach(() => {
            (globalThis as Record<string, unknown>).__isoformFeatureSwitchesForTest = { automations: true, playbooks: true, proactiveVoice: true, calendarHolds: true, activityExtras: false };
        });

        it('offers three tabs: Your day, Watch and Your answers', async () => {
            binding('GetWatchRecordings').mockResolvedValue([]);
            await act(async () => {
                render(<Harness initialSection="yourDay" />);
            });
            const tabs = [...document.querySelectorAll('[data-activity-section]')].map((row) => row.textContent);
            expect(tabs).toEqual([enActivity.day.yourDay, enActivity.act.tabWatch, enActivity.act.tabAnswers]);
        });

        it.each(['workedOn', 'agents'] as const)('shows Your day for a %s section', async (section) => {
            await act(async () => {
                render(<Harness initialSection={section} />);
            });
            expect(screen.getByTestId('activity-feed')).toBeInTheDocument();
            expect(screen.queryByRole('heading', { name: enActivity.day.workedOn })).toBeNull();
            expect(document.querySelector('[data-activity-section="yourDay"]')).toHaveAttribute('aria-selected', 'true');
        });

        it('opens a screen row in the Screen timeline at the group holding its stretch', async () => {
            const at = (h: number, m: number) => new Date(2026, 10, 2, h, m).getTime();
            binding('GetActivityFeed').mockResolvedValue([
                { id: 'm-1', kind: 'screen', startMs: at(10, 8), endMs: at(10, 18), title: 'Invoice sync notes', app: 'Google Chrome' },
            ]);
            binding('GetActivityTimelineGroups').mockResolvedValue([
                { id: 'g1', appKey: 'com.microsoft.VSCode', appName: 'VS Code', startTime: at(9, 12), endTime: at(10, 2), activeMs: 50 * 60_000,
                    eventCount: 0, sessionIds: ['s1'], triggerKinds: [], primarySessionId: 's1', summaryStatus: 'done',
                    summary: { title: 'Reviewing ERP connector v0.3' } },
                { id: 'g2', appKey: 'com.google.Chrome', appName: 'Google Chrome', startTime: at(10, 5), endTime: at(10, 20), activeMs: 15 * 60_000,
                    eventCount: 0, sessionIds: ['s2'], triggerKinds: [], primarySessionId: 's2', summaryStatus: 'done',
                    summary: { title: 'Northwind invoice sync notes' } },
            ]);
            await act(async () => { render(<Harness initialSection="yourDay" initialDate="2026-11-02" />); });
            fireEvent.click(await screen.findByRole('button', { name: 'Open: Invoice sync notes' }));
            const group = async (title: string) => (await screen.findAllByText(title)).map((el) => el.closest('button')).find(Boolean)!;
            await waitFor(async () => expect(await group('Northwind invoice sync notes')).toHaveClass('bg-active-row-bg'));
            expect(await group('Reviewing ERP connector v0.3')).not.toHaveClass('bg-active-row-bg');
            // The timeline is the detail behind Your day: no Screen tab.
            expect(document.querySelector('[data-activity-section="workedOn"]')).toBeNull();
            expect(document.querySelector('[data-activity-section="yourDay"]')).toHaveAttribute('aria-selected', 'true');
        });

        it('picks the group holding a time, else the next one, else the last', () => {
            const groups = [{ id: 'a', startTime: 10, endTime: 20 }, { id: 'b', startTime: 40, endTime: 50 }];
            expect(timelineGroupAt(groups, 15)?.id).toBe('a');
            expect(timelineGroupAt(groups, 30)?.id).toBe('b');
            expect(timelineGroupAt(groups, 60)?.id).toBe('b');
            expect(timelineGroupAt([], 15)).toBeNull();
        });
    });

    // computer-conformance-answers v1.1.0 P1: screen-seen agent runs are
    // Screen rows; the Agents tab lists hand-offs only.
    it('shows the agents tab as hand-offs only, not screen-seen agent runs or screen rows', async () => {
        const at = new Date().setHours(9, 0, 0, 0);
        binding('GetActivityFeed').mockResolvedValue([
            { id: 'h-1', kind: 'handoff', startMs: at + 60_000, endMs: at + 120_000, title: 'Write the test plan', app: 'Claude Code' },
            { id: 'run-1', kind: 'agent', startMs: at, endMs: at + 600_000, activeMs: 600_000, app: 'Codex' },
            { id: 'm-1', kind: 'screen', startMs: at - 60_000, endMs: at, title: 'Pricing page copy', app: 'Safari' },
        ]);
        await act(async () => {
            render(<Harness initialSection="agents" />);
        });
        expect(await screen.findByText('Write the test plan')).toBeInTheDocument();
        expect(screen.queryByText('Your agents · Codex')).toBeNull();
        expect(screen.queryByText('Worked for 10m')).toBeNull();
        expect(screen.queryByText('Pricing page copy')).toBeNull();
        expect(document.querySelector('[data-activity-section="agents"]')).toHaveAttribute('aria-selected', 'true');
        binding('GetActivityFeed').mockResolvedValue([]);
    });

    it('opens Watch in place of the day', async () => {
        binding('GetWatchRecordings').mockResolvedValue([{
            id: 'r1', source: 'watch', state: 'transcribed', startedAt: new Date().toISOString(), durationSeconds: 75,
            words: 3, ownerVoice: '', sessionId: 'rec-r1', hasAudio: false, parts: [],
        }]);
        binding('GetActivityWatchRows').mockResolvedValue([{
            id: 'dev:1', kind: 'watch', startMs: Date.now() - 60_000, endMs: Date.now(), title: 'Pricing with Sam',
            conversation: true, speakers: ['you'], device: 'watch', basis: { setting: 'watch' },
        }]);
        await act(async () => {
            render(<Harness initialSection="yourDay" />);
        });
        const door = await waitFor(() => {
            const found = document.querySelector('[data-activity-section="watch"]');
            expect(found).not.toBeNull();
            return found as HTMLElement;
        });
        fireEvent.click(door);
        await waitFor(() => expect(screen.getByText('Pricing with Sam')).toBeInTheDocument());
        expect(door).toHaveAttribute('aria-selected', 'true');
        expect(screen.queryByTestId('activity-feed')).toBeNull();
        binding('GetWatchRecordings').mockResolvedValue([]);
        binding('GetActivityWatchRows').mockResolvedValue([]);
    });

    it('says in place when a frame screen text cannot be read, with Try again', async () => {
        binding('GetSnapshotOCRText').mockRejectedValueOnce(new Error('ocr down')).mockResolvedValueOnce('Quarterly plan');
        render(
            <EventFramePopup
                data={{ frameSrc: '', evt: { timestamp: Date.now(), app: 'Notes' }, snapshot: { id: 's1', sessionId: 'sess1' } } as never}
                keyword=""
                onClose={vi.fn()}
            />,
        );
        const alert = await screen.findByRole('alert');
        expect(alert).toHaveTextContent("Couldn't read the text on this screen.");
        fireEvent.click(within(alert).getByRole('button', { name: 'Try again' }));
        await waitFor(() => expect(screen.queryByText("Couldn't read the text on this screen.")).not.toBeInTheDocument());
        expect(binding('GetSnapshotOCRText')).toHaveBeenCalledTimes(2);
    });

    it('says in place when the Screen timeline sessions fail to load, with Try again', async () => {
        binding('GetActivitySessions').mockRejectedValueOnce(new Error('db locked'));
        await act(async () => {
            render(<Harness initialSection="timeline" />);
        });
        const alert = await screen.findByRole('alert');
        expect(alert).toHaveTextContent("Couldn't load your sessions.");
        fireEvent.click(within(alert).getByRole('button', { name: 'Try again' }));
        await waitFor(() => expect(screen.queryByText("Couldn't load your sessions.")).not.toBeInTheDocument());
        expect(binding('GetActivitySessions')).toHaveBeenCalledTimes(2);
    });

    it('does not load the Screen timeline until its section is picked', async () => {
        await act(async () => {
            render(<Harness initialSection="yourDay" />);
        });
        // The heavy timeline queries wait for the timeline section.
        expect(binding('GetActivitySessions')).not.toHaveBeenCalled();
        expect(binding('GetActivityTimelineGroups')).not.toHaveBeenCalled();

        // The Screen tab holds the timeline beside What you worked on.
        fireEvent.click(document.querySelector('[data-activity-section="workedOn"]') as HTMLElement);
        fireEvent.click(await screen.findByTestId('activity-open-timeline'));
        await waitFor(() => {
            expect(document.querySelector('[data-activity-section="workedOn"]')).toHaveAttribute('aria-selected', 'true');
            expect(binding('GetActivitySessions')).toHaveBeenCalled();
            expect(binding('GetActivityTimelineGroups')).toHaveBeenCalled();
        });
    });

    // Thursday A3 and A1: each session row shows its first kept frame or no
    // thumbnail space, and the session meta drops system counts.
    it('shows each session with its first frame or no thumbnail space, and no system counts', async () => {
        const at = (h: number, m: number) => new Date(2026, 10, 2, h, m).getTime();
        binding('GetHostBridgePort').mockResolvedValue(4317);
        binding('GetHostBridgeMediaToken').mockResolvedValue('token');
        binding('GetActivityTimelineGroups').mockResolvedValue([
            { id: 'g1', appKey: 'com.microsoft.VSCode', appName: 'VS Code', startTime: at(9, 12), endTime: at(10, 2), activeMs: 50 * 60_000,
                eventCount: 0, sessionIds: ['s1'], triggerKinds: [], primarySessionId: 's1', summaryStatus: 'done',
                summary: { title: 'Reviewing ERP connector v0.3' },
                firstFrame: { timestamp: at(9, 12), path: '/Users/ana/Isoform/snap-1.jpg', focusX: 0, focusY: 0, focusW: 0, focusH: 0 } },
            { id: 'g2', appKey: 'com.google.Chrome', appName: 'Google Chrome', startTime: at(10, 5), endTime: at(10, 20), activeMs: 15 * 60_000,
                eventCount: 0, sessionIds: ['s2'], triggerKinds: [], primarySessionId: 's2', summaryStatus: 'done',
                summary: { title: 'Northwind invoice sync notes' } },
        ]);
        binding('GetActivitySessionDetail').mockResolvedValue({ id: 's1', startTime: at(9, 12), endTime: at(10, 2), triggerKind: '',
            appNames: ['VS Code'], eventCount: 0, snapshotCount: 0, triageStatus: '', ocrStatus: '', events: [], segments: [], snapshots: [] });
        await act(async () => { render(<Harness initialSection="timeline" initialDate="2026-11-02" />); });
        const row = async (title: string) => (await screen.findAllByText(title)).map((el) => el.closest('button')).find(Boolean)!;
        const memo = await row('Reviewing ERP connector v0.3');
        const notes = await row('Northwind invoice sync notes');
        await waitFor(() => expect(within(memo).getByTestId('timeline-first-frame')).toBeInTheDocument());
        expect(within(notes).queryByTestId('timeline-first-frame')).toBeNull();
        expect(notes.querySelector('img, [aria-hidden="true"].animate-pulse')).toBeNull();
        expect(memo).toHaveTextContent('9:12 AM');
        expect(memo).not.toHaveTextContent(/09:12/);
        // Activity review v1.0.6 AR41 and v1.0.7 AR14: time first, the app
        // once, and the length in rounded minutes.
        expect(within(memo).getByText(/^9:12\sAM · VS Code$/)).toBeInTheDocument();
        expect(within(memo).getByText('50m')).toBeInTheDocument();
        fireEvent.click(memo);
        expect(await screen.findByTestId('session-meta')).toHaveTextContent('9:12–10:02 AM');
        expect(screen.queryByText(/\d+ events|Local only|:\d\d:\d\d/)).toBeNull();
    });

    // activity review v1.0.5 V2, V3, V5, V6: the session view reads
    // "9:12–10:02 AM · 50 min · VS Code", has no Recording box without a
    // screenshot, a "Steps" heading with no count, and no speech scraps.
    it('writes the session meta in words and drops the empty Recording box, step counts and speech scraps', async () => {
        const at = (h: number, m: number) => new Date(2026, 10, 2, h, m).getTime();
        binding('GetActivityTimelineGroups').mockResolvedValue([
            { id: 'g1', appKey: 'com.microsoft.VSCode', appName: 'VS Code', startTime: at(9, 12), endTime: at(10, 2), activeMs: 50 * 60_000,
                eventCount: 1, sessionIds: ['s1'], triggerKinds: [], primarySessionId: 's1', summaryStatus: 'done',
                summary: { title: 'Reviewed ERP connector v0.3' } },
        ]);
        binding('GetActivitySessionDetail').mockResolvedValue({ id: 's1', startTime: at(9, 12), endTime: at(10, 2), triggerKind: '',
            appNames: ['VS Code'], eventCount: 1, snapshotCount: 0, triageStatus: '', ocrStatus: '', segments: [], snapshots: [],
            events: [{ kind: 'speech', speechSource: 'microphone', speechText: 'There the two.', timestamp: at(9, 13), appName: 'VS Code' }] });
        await act(async () => { render(<Harness initialSection="timeline" initialDate="2026-11-02" />); });
        const row = (await screen.findAllByText('Reviewed ERP connector v0.3')).map((el) => el.closest('button')).find(Boolean)!;
        fireEvent.click(row);
        expect(await screen.findByTestId('session-meta')).toHaveTextContent('9:12–10:02 AM · 50 min · VS Code');
        expect(screen.getByText('Steps')).toBeInTheDocument();
        expect(screen.queryByText(/Steps \(/)).toBeNull();
        expect(screen.queryByText('Recording')).toBeNull();
        expect(screen.queryByText('No screenshots were captured for this session.')).toBeNull();
        expect(screen.queryByText(/There the two/)).toBeNull();
    });

    // activity review v1.0.6 AR43: a session with screenshots keeps its replay above Steps.
    it('keeps the replay above Steps for a session with screenshots', async () => {
        const at = (h: number, m: number) => new Date(2026, 10, 2, h, m).getTime();
        binding('GetActivityTimelineGroups').mockResolvedValue([
            { id: 'g1', appKey: 'com.microsoft.VSCode', appName: 'VS Code', startTime: at(9, 12), endTime: at(10, 2), activeMs: 50 * 60_000,
                eventCount: 0, sessionIds: ['s1'], triggerKinds: [], primarySessionId: 's1', summaryStatus: 'done',
                summary: { title: 'Reviewed ERP connector v0.3' },
                firstFrame: { timestamp: at(9, 12), path: '/Users/ana/frames/a.jpg', focusX: 0, focusY: 0, focusW: 0, focusH: 0 } },
        ]);
        binding('GetActivitySessionDetail').mockResolvedValue({ id: 's1', startTime: at(9, 12), endTime: at(10, 2), triggerKind: '',
            appNames: ['VS Code'], eventCount: 0, snapshotCount: 1, triageStatus: '', ocrStatus: '', segments: [], events: [],
            snapshots: [{ id: 1, sessionId: 's1', timestamp: at(9, 13), filePath: '/Users/ana/frames/a.jpg', width: 1440, height: 900,
                sizeBytes: 0, trigger: 'interval', storageTier: 'full', axText: '', browserUrl: '', app: 'VS Code' }] });
        await act(async () => { render(<Harness initialSection="timeline" initialDate="2026-11-02" />); });
        const row = (await screen.findAllByText('Reviewed ERP connector v0.3')).map((el) => el.closest('button')).find(Boolean)!;
        fireEvent.click(row);
        const recording = await screen.findByText('Recording');
        const steps = screen.getByText('Steps');
        expect(recording.compareDocumentPosition(steps) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    });

    it('states only the supported privacy line on the Screen timeline', async () => {
        await act(async () => { render(<Harness initialSection="timeline" />); });
        expect(screen.getByText('Screenshots and audio are stored on your computer.')).toBeInTheDocument();
        expect(screen.queryByText(/redact|passwords|API keys/i)).not.toBeInTheDocument();
    });

    it('localizes the Screen timeline privacy line', async () => {
        await i18n.changeLanguage('zh-CN');
        await act(async () => { render(<Harness initialSection="timeline" />); });
        expect(screen.getByText('屏幕截图和音频存储在你的电脑上。')).toBeInTheDocument();
        expect(screen.queryByText(/脱敏|密码|API 密钥/)).not.toBeInTheDocument();
    });

    it('writes the step verbs in the UI language', async () => {
        await i18n.changeLanguage('zh-CN');
        const t = i18n.getFixedT('zh-CN', 'activity') as unknown as Parameters<typeof eventActionLabel>[1];
        const verb = (evt: Partial<activitymon.ActivityEvent>) => eventActionLabel(evt as activitymon.ActivityEvent, t).verb;
        expect(verb({ kind: 'focus', focusLabel: 'connector/retry.go' })).toBe('查看');
        expect(verb({ kind: 'keyboard', typedText: 'retry on 429 with backoff' })).toBe('输入');
        expect(verb({ kind: 'click', clickLabel: 'Merge' })).toBe('点击了');
        expect(verb({ kind: 'speech', speechSource: 'microphone', speechText: "Let's ship the connector on Friday." })).toBe('说');
        expect(verb({ kind: 'app_switch', appName: 'Slack' })).toBe('切换应用');
    });

    it('opens on the day the caller asked for', async () => {
        await act(async () => {
            render(<Harness initialDate="2026-10-15" />);
        });
        // The date navigation in the panel names the picked day (Oct 15, 2026
        // was a Thursday), not today.
        expect(screen.getByTitle(enActivity.dayNav.pickDate)).toHaveTextContent('Oct 15');
    });

    it('opens straight on the Screen timeline for a session jump', async () => {
        await act(async () => {
            render(<Harness initialSection="timeline" />);
        });
        await waitFor(() => {
            expect(binding('GetActivityTimelineGroups')).toHaveBeenCalled();
        });
        // The day section rows and the date navigation hide on the timeline.
        expect(document.querySelector('[data-activity-section="workedOn"]')).not.toBeNull();
        expect(screen.queryByTitle(enActivity.dayNav.pickDate)).toBeNull();
    });

    // Isoform noticed left Activity (A4): Today's suggestion link opens Your
    // day on its date and is consumed, and nothing on the day reads it.
    it('opens an old suggestion link on Your day for its date and consumes it', async () => {
        const focus = { date: '2026-10-15', suggestionId: 'suggestion-1', action: 'follow_up' as const };
        const consumed = vi.fn();
        function FocusHarness() {
            const [section, setSection] = useState<ActivitySectionId>('workedOn');
            return (
                <ActivityPage
                    initialDate={focus.date}
                    noticedFocus={focus}
                    onNoticedFocusConsumed={consumed}
                    cronJobs={cronJobs}
                    section={section}
                    onSectionChange={setSection}
                />
            );
        }
        await act(async () => {
            render(<FocusHarness />);
        });
        await waitFor(() => expect(consumed).toHaveBeenCalledWith(focus));
        expect(document.querySelector('[data-activity-section="yourDay"]')).toHaveAttribute('aria-selected', 'true');
        expect(screen.getByTitle(enActivity.dayNav.pickDate)).toHaveTextContent('Oct 15');
        expect(screen.getByTestId('activity-feed')).toBeInTheDocument();
        expect(binding('GetDayNoticed')).not.toHaveBeenCalled();
    });
});

describe('Activity first view and session reuse', () => {
    beforeEach(async () => {
        resetActivitySessionCache();
        resetPageQueries();
        fixture.listeners.clear();
        for (const mock of fixture.bindings.values()) mock.mockReset().mockResolvedValue([]);
        await i18n.changeLanguage('en');
    });

    // computer-conformance-answers A4: Your day is the record only.
    it('shows Your day as the feed alone: no summary card, Isoform noticed or learned, and no tab counts', async () => {
        binding('EnsureDailySummary').mockResolvedValue({ aiSummary: 'Fixture digest', aiSummaryStatus: 'ready', totalActiveMs: 3_600_000, hourlyActive: [], appBreakdown: [{ appName: 'Safari', activeMs: 3_600_000 }] });
        binding('GetDayNoticed').mockResolvedValue([{ id: 's', kind: 'follow_up', text: 'Fixture notice', why: '', momentId: 'm', momentTitle: 'M', momentStartMs: 0 }]);
        binding('GetActivitiesInRange').mockResolvedValue([{
            id: 'fast-row', title: 'Public fixture row', startMs: new Date(2026, 9, 15, 9).getTime(), endMs: new Date(2026, 9, 15, 10).getTime(),
            apps: [], entities: [], thumbnails: [], runCount: 0, activeMs: 60_000,
        }]);
        const at = new Date(2026, 9, 15, 9).getTime();
        binding('GetActivityFeed').mockResolvedValue([
            { id: 'f-1', kind: 'screen', startMs: at, endMs: at + 60_000, title: 'Feed fixture row', app: 'Safari' },
        ]);
        render(<Harness initialDate="2026-10-15" />);
        expect(await screen.findByText('Feed fixture row')).toBeInTheDocument();
        await act(async () => { await Promise.resolve(); });
        expect(screen.queryByText('Fixture digest')).not.toBeInTheDocument();
        expect(screen.queryByText('Fixture notice')).not.toBeInTheDocument();
        expect(screen.queryByText('Public fixture row')).not.toBeInTheDocument();
        expect(screen.queryByText(/1h/)).not.toBeInTheDocument();
        expect(document.querySelector('[data-activity-section="yourDay"]')).toHaveAttribute('aria-selected', 'true');
        for (const row of document.querySelectorAll('[data-activity-section]')) expect(row.textContent).not.toMatch(/\d/);
        expect(binding('GetDayNoticed')).not.toHaveBeenCalled();
        expect(binding('GetWeeklySummary')).not.toHaveBeenCalled();
        expect(binding('GetActivityMonthStats')).not.toHaveBeenCalled();
    });

    it('restores the selected day and its rows synchronously while revalidating', async () => {
        const startMs = new Date(2026, 9, 15, 9).getTime();
        binding('GetActivitiesInRange').mockResolvedValue([{
            id: 'kept-row', title: 'Kept public fixture row', startMs, endMs: startMs + 300_000, apps: [], entities: [], thumbnails: [], runCount: 0, activeMs: 300_000,
        }]);
        binding('GetDayThreads').mockResolvedValue({ grouped: true, threads: [{
            id: 'kept-thread', title: 'Kept public thread', kind: 'work', userTitled: false, startMs, endMs: startMs + 300_000,
            timeSpentMs: 300_000, activityIds: ['kept-row'], activeMs: 300_000, gaps: [], interruption: false, reviewed: false, unfinished: false,
            stretches: [{ start: startMs, end: startMs + 300_000, activeMs: 300_000, activityIds: ['kept-row'], resumePoint: null }],
            linkReason: { kind: 'rule', keys: [], text: '' },
        }] });
        const first = render(<Harness initialSection="workedOn" initialDate="2026-10-15" />);
        // Stitched by work first, then the same rows as recorded.
        expect((await screen.findAllByText('Kept public thread')).length).toBeGreaterThan(0);
        fireEvent.click(document.querySelector('[data-worked-on-view="recorded"]') as HTMLElement);
        await screen.findByText('Kept public fixture row');
        first.unmount();
        binding('GetActivitiesInRange').mockReturnValue(new Promise(() => {}));
        binding('GetDayThreads').mockReturnValue(new Promise(() => {}));
        render(<Harness initialSection="workedOn" />);
        expect(screen.getAllByText('Kept public thread').length).toBeGreaterThan(0);
        fireEvent.click(document.querySelector('[data-worked-on-view="recorded"]') as HTMLElement);
        expect(screen.getByText('Kept public fixture row')).toBeInTheDocument();
        expect(binding('GetActivitiesInRange')).toHaveBeenLastCalledWith(new Date(2026, 9, 15).getTime(), new Date(2026, 9, 16).getTime());
    });
});

// Ground rule (hub, 2026-11-21): a failed read shows an error with Try
// again, never an empty day.
it('says in place when Your day cannot be read, with Try again, and never as an empty day', async () => {
    await i18n.changeLanguage('en');
    resetActivitySessionCache();
    fixture.listeners.clear();
    for (const mock of fixture.bindings.values()) mock.mockReset().mockResolvedValue([]);
    const at = new Date(2026, 9, 15, 9).getTime();
    binding('GetActivityFeed')
        .mockRejectedValueOnce(new Error('temporary read failure'))
        .mockResolvedValue([{ id: 'f-1', kind: 'screen', startMs: at, endMs: at + 60_000, title: 'Recovered feed row', app: 'Safari' }]);
    render(<Harness initialDate="2026-10-15" />);
    expect(await screen.findByText(enActivity.feed.loadFailed)).toBeInTheDocument();
    expect(screen.queryByText(enActivity.feed.empty)).toBeNull();
    fireEvent.click(screen.getByRole('button', { name: enActivity.ask.tryAgain }));
    expect(await screen.findByText('Recovered feed row')).toBeInTheDocument();
    expect(screen.queryByText(enActivity.feed.loadFailed)).toBeNull();
});

// B7: a watch conversation opens in place from Your day, and from a link
// elsewhere (App's openActivityConversation, #1418).
describe('ActivityPage watch conversations', () => {
    const today = new Date();
    const startMs = new Date(today.getFullYear(), today.getMonth(), today.getDate(), 10, 0).getTime();
    const row = { id: 'dev:1', kind: 'watch', startMs, endMs: startMs + 36 * 60_000, title: 'Pricing with Sam', conversation: true, speakers: ['you'], basis: { setting: 'watch' } };
    const conversation = {
        id: 'dev:1', name: 'Pricing with Sam', startAt: startMs, endAt: startMs + 36 * 60_000, recordingIds: [], words: 4,
        speakers: ['you'], lines: [{ at: startMs, speaker: 'you', text: 'Friday works for me', marked: false }],
        summary: '', decisions: [], followups: [], speakerNames: {},
    };

    beforeEach(async () => {
        resetActivitySessionCache();
        resetPageQueries();
        await i18n.changeLanguage('en');
        localStorage.clear();
        fixture.listeners.clear();
        for (const mock of fixture.bindings.values()) mock.mockReset().mockResolvedValue([]);
        Element.prototype.scrollIntoView = vi.fn();
        binding('GetActivityFeed').mockResolvedValue([row]);
        binding('GetActivityWatchRows').mockResolvedValue([row]);
        binding('GetWatchConversations').mockResolvedValue([conversation]);
    });

    it('opens a conversation from a Your day row on the Watch tab, with its page open', async () => {
        await act(async () => { render(<Harness />); });
        fireEvent.click(await screen.findByRole('button', { name: 'Open: Pricing with Sam' }));
        const page = await screen.findByTestId('watch-row-page');
        expect(page.getAttribute('data-conversation-id')).toBe('dev:1');
        expect(await within(page).findByText('Friday works for me')).toBeInTheDocument();
        expect(screen.getByRole('tab', { name: enActivity.act.tabWatch })).toHaveAttribute('aria-selected', 'true');
    });

    it('opens the conversation a link asked for, on its day', async () => {
        const day = `${today.getFullYear()}-${String(today.getMonth() + 1).padStart(2, '0')}-${String(today.getDate()).padStart(2, '0')}`;
        await act(async () => { render(<Harness initialSection="watch" initialDate={day} initialConversationId="dev:1" />); });
        const page = await screen.findByTestId('watch-row-page');
        expect(await within(page).findByText('Friday works for me')).toBeInTheDocument();
    });

    it('shows the Watch tab with a quiet line when the linked conversation is gone', async () => {
        await act(async () => { render(<Harness initialSection="watch" initialConversationId="dev:forgotten" />); });
        expect(await screen.findByTestId('watch-conversation-gone')).toHaveTextContent(enActivity.act.conversationGone);
        expect(screen.queryByTestId('watch-row-page')).toBeNull();
    });
});
