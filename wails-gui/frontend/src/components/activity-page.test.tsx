import { resetActivitySessionCache } from './timeline/activity-session-cache';
import { resetPageQueries } from '@/lib/page-queries';
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { useState } from 'react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import i18n from '@/i18n';
import enActivity from '@/i18n/locales/en/activity.json';
import { ActivityPage, eventActionLabel, EventFramePopup, SessionPeopleChips, SessionSpeakerChips, type ActivitySectionId } from './activity-page';
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
function Harness({ initialSection = 'yourDay' as ActivitySectionId, initialDate = null as string | null, initialFocus = null as MobileBriefActivityTarget | null }) {
    const [section, setSection] = useState<ActivitySectionId>(initialSection);
    const [focus, setFocus] = useState(initialFocus);
    return (
        <ActivityPage
            initialDate={initialDate}
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

    it('shows one section at a time, and the section rows pick it', async () => {
        await act(async () => {
            render(<Harness initialSection="yourDay" />);
        });
        // Your day shows; the other day sections do not.
        expect(screen.getByRole('heading', { name: enActivity.day.yourDay })).toBeInTheDocument();
        expect(screen.queryByRole('heading', { name: enActivity.day.workedOn })).toBeNull();

        // Clicking "What you worked on" swaps the section.
        fireEvent.click(document.querySelector('[data-activity-section="workedOn"]') as HTMLElement);
        await waitFor(() => {
            expect(screen.queryByRole('heading', { name: enActivity.day.yourDay })).toBeNull();
            expect(screen.getByRole('heading', { name: enActivity.day.workedOn })).toBeInTheDocument();
        });
        // The section row shows the current pick.
        expect((document.querySelector('[data-activity-section="workedOn"]') as HTMLElement).getAttribute('aria-current')).toBe('page');
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

    it('shows the agents tab as the timeline of agent rows only', async () => {
        const at = new Date().setHours(9, 0, 0, 0);
        binding('GetActivityFeed').mockResolvedValue([
            { id: 'run-1', kind: 'agent', startMs: at, endMs: at + 600_000, activeMs: 600_000, app: 'Codex' },
            { id: 'm-1', kind: 'screen', startMs: at - 60_000, endMs: at, title: 'Pricing page copy', app: 'Safari' },
        ]);
        await act(async () => {
            render(<Harness initialSection="agents" />);
        });
        expect(await screen.findByText('Your agents · Codex')).toBeInTheDocument();
        expect(screen.queryByText('Pricing page copy')).toBeNull();
        expect(document.querySelector('[data-activity-section="agents"]')).toHaveAttribute('aria-current', 'page');
        binding('GetActivityFeed').mockResolvedValue([]);
    });

    it('opens Watch in place of the day', async () => {
        binding('GetWatchRecordings').mockResolvedValue([{
            id: 'r1', source: 'watch', state: 'transcribed', startedAt: new Date().toISOString(), durationSeconds: 75,
            words: 3, ownerVoice: '', sessionId: 'rec-r1', hasAudio: false, parts: [],
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
        await waitFor(() => expect(screen.getByText('1:15 · ' + enActivity.watch.state.transcribed)).toBeInTheDocument());
        expect(door.getAttribute('aria-current')).toBe('page');
        expect(screen.queryByRole('heading', { name: enActivity.day.yourDay })).toBeNull();
        binding('GetWatchRecordings').mockResolvedValue([]);
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
            expect(document.querySelector('[data-activity-section="workedOn"]')).toHaveAttribute('aria-current', 'page');
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

    it.each(['follow_up', 'routine'] as const)('opens the focused %s editor inside Your day without saving', async (action) => {
        const focus = { date: '2026-10-15', suggestionId: 'suggestion-1', action };
        binding('GetDayNoticed').mockResolvedValue([{
            id: focus.suggestionId, kind: action, text: 'Follow up on the review', why: 'A decision is waiting.',
            momentId: 'moment-1', momentTitle: 'Reviewing the plan', momentStartMs: new Date(2026, 9, 15, 10).getTime(),
            routine: { name: 'Weekly review', when: 'weekly fri 16:00', steps: ['Read the plan'], will: ['Prepare notes'], wont: ['Send anything'] },
        }]);
        render(<Harness initialDate={focus.date} initialFocus={focus} />);
        expect(await screen.findByRole('heading', { name: enActivity.noticed.title })).toBeInTheDocument();
        expect(binding('GetDayNoticed')).toHaveBeenCalledWith(focus.date);
        if (action === 'follow_up') {
            expect(await screen.findByRole('textbox', { name: 'Follow-up' })).toHaveValue('Follow up on the review');
        } else {
            const dialog = await screen.findByRole('dialog');
            fireEvent.click(within(dialog).getByRole('button', { name: 'Cancel' }));
        }
        expect(binding('AddActivityFollowUp')).not.toHaveBeenCalled();
        expect(binding('SaveNoticedRoutine')).not.toHaveBeenCalled();
        expect(binding('GetActivitySessions')).not.toHaveBeenCalled();
        fireEvent.click(document.querySelector('[data-activity-section="workedOn"]') as HTMLElement);
        fireEvent.click(document.querySelector('[data-activity-section="yourDay"]') as HTMLElement);
        expect(await screen.findByRole('heading', { name: enActivity.noticed.title })).toBeInTheDocument();
        expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
        expect(screen.queryByRole('textbox', { name: 'Follow-up' })).not.toBeInTheDocument();
    });

    it.each([false, true])('reveals a suggestion source even before moments and threads load (threaded: %s)', async (threaded) => {
        const startMs = new Date(2026, 9, 15, 10).getTime();
        const moment = {
            id: 'moment-1', status: 'settled', title: 'Reviewing the plan', description: 'The review details.',
            startMs, endMs: startMs + 60_000, activeMs: 60_000, category: 'build', categoryColor: '',
            entities: [], apps: [], thumbnails: [], runCount: 1, inProgress: false,
        };
        let finishMoments!: (rows: unknown[]) => void;
        let finishThreads!: (rows: unknown) => void;
        binding('GetActivitiesInRange').mockReturnValue(new Promise((resolve) => { finishMoments = resolve; }));
        binding('GetDayThreads').mockReturnValue(new Promise((resolve) => { finishThreads = resolve; }));
        const groups = { grouped: threaded, threads: threaded ? [{
            id: 'thread-1', title: 'Planning', kind: 'project', userTitled: false,
            startMs, endMs: startMs + 60_000, timeSpentMs: 60_000, activityIds: ['moment-1', 'moment-2'],
        }] : [] };
        binding('GetDayNoticed').mockResolvedValue([{
            id: 'suggestion-1', kind: 'follow_up', text: 'Follow up on the review', why: '',
            momentId: moment.id, momentTitle: moment.title, momentStartMs: startMs,
        }]);
        render(<Harness initialDate="2026-10-15" />);
        const source = await screen.findByRole('button', { name: /From .*Reviewing the plan/ });
        fireEvent.click(source);
        expect(document.querySelector('[data-activity-section="workedOn"]')).toHaveAttribute('aria-current', 'page');
        await act(async () => {
            finishMoments(threaded ? [moment, { ...moment, id: 'moment-2', title: 'Preparing notes' }] : [moment]);
        });
        await act(async () => { finishThreads(groups); });
        expect(await screen.findByRole('button', { name: 'Delete from history' })).toBeInTheDocument();
        expect(document.querySelector('[data-activity-section="workedOn"]')).toHaveAttribute('aria-current', 'page');
        expect(screen.queryByRole('heading', { name: enActivity.noticed.title })).not.toBeInTheDocument();
        expect(binding('GetActivitySessions')).not.toHaveBeenCalled();
        fireEvent.click(screen.getByText(moment.title).closest('button')!);
        expect(screen.queryByRole('button', { name: 'Delete from history' })).not.toBeInTheDocument();
        binding('GetActivitiesInRange').mockResolvedValue(threaded ? [{ ...moment }, { ...moment, id: 'moment-2', title: 'Preparing notes' }] : [{ ...moment }]);
        binding('GetDayThreads').mockResolvedValue(groups);
        await act(async () => {
            fixture.listeners.get('activity:activities-updated')?.forEach((handler) => handler(undefined));
        });
        expect(screen.queryByRole('button', { name: 'Delete from history' })).not.toBeInTheDocument();
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

    it('keeps the selected section while the digest loads and shows no menu counts', async () => {
        let finishSummary!: (value: unknown) => void;
        let finishKnowledge!: (value: unknown) => void;
        let finishMemory!: (value: unknown) => void;
        binding('EnsureDailySummary').mockReturnValue(new Promise((resolve) => { finishSummary = resolve; }));
        binding('GetKnowledgeEntries').mockReturnValue(new Promise((resolve) => { finishKnowledge = resolve; }));
        binding('GetMemoryEntries').mockReturnValue(new Promise((resolve) => { finishMemory = resolve; }));
        binding('GetActivitiesInRange').mockResolvedValue([{
            id: 'fast-row', title: 'Public fixture row', startMs: new Date(2026, 9, 15, 9).getTime(), endMs: new Date(2026, 9, 15, 10).getTime(),
            apps: [], entities: [], thumbnails: [], runCount: 0, activeMs: 60_000,
        }]);
        render(<Harness initialDate="2026-10-15" />);
        expect(await screen.findByRole('heading', { name: enActivity.day.yourDay })).toBeInTheDocument();
        expect(screen.queryByText('Public fixture row')).not.toBeInTheDocument();
        expect(document.querySelector('[data-activity-section="yourDay"]')).toHaveAttribute('aria-current', 'page');
        for (const row of document.querySelectorAll('[data-activity-section]')) expect(row.textContent).not.toMatch(/\d/);
        expect(binding('GetKnowledgeEntries')).not.toHaveBeenCalled();
        expect(binding('GetWeeklySummary')).not.toHaveBeenCalled();
        expect(binding('GetActivityMonthStats')).not.toHaveBeenCalled();
        await act(async () => finishSummary({ aiSummary: 'Fixture digest', hourlyActive: [], appBreakdown: [] }));
        expect(await screen.findByText('Fixture digest')).toBeInTheDocument();
        await act(async () => { finishKnowledge?.([]); finishMemory?.([]); });
        expect(document.querySelector('[data-activity-section="yourDay"]')).toHaveAttribute('aria-current', 'page');
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

it('settles a failed digest and thread read so the existing summary and source controls remain usable', async () => {
    await i18n.changeLanguage('en');
    resetActivitySessionCache();
    fixture.listeners.clear();
    for (const mock of fixture.bindings.values()) mock.mockReset().mockResolvedValue([]);
    binding('EnsureDailySummary').mockRejectedValue(new Error('temporary read failure'));
    binding('GetDayThreads').mockRejectedValue(new Error('temporary read failure'));
    binding('GetActivitiesInRange').mockResolvedValue([{
        id: 'recoverable-row', title: 'Recoverable public row', startMs: new Date(2026, 9, 15, 9).getTime(), endMs: new Date(2026, 9, 15, 10).getTime(),
        apps: [], entities: [], thumbnails: [], runCount: 0, activeMs: 60000,
    }]);
    binding('GetDayNoticed').mockResolvedValue([{
        id: 'source', kind: 'follow_up', momentId: 'recoverable-row', momentTitle: 'Recoverable public row', momentStartMs: new Date(2026, 9, 15, 9).getTime(), text: 'Check the public row', why: '',
    }]);
    render(<Harness initialDate="2026-10-15" />);
    // AR63: a failed read is never an answer. Write it and "isn't written" wait
    // for the automatic retry; the day then says it couldn't load, with a retry.
    expect(await screen.findByText(enActivity.summary.card.loadFailed, {}, { timeout: 5000 })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: enActivity.summary.card.writeIt })).toBeNull();
    expect(screen.queryByText(enActivity.summary.card.dayNotWritten)).toBeNull();
    expect(screen.getByRole('button', { name: enActivity.summary.card.tryAgain })).toBeEnabled();
    fireEvent.click(await screen.findByRole('button', { name: /From .*Recoverable public row/ }));
    expect(await screen.findByRole('button', { name: 'Delete from history' })).toBeInTheDocument();
});

// AR63 guard: a failed read followed by a successful retry shows the saved day,
// and the placeholder never says the day is unwritten in between.
it('holds the day through a failed read and shows the saved summary after its retry', async () => {
    await i18n.changeLanguage('en');
    resetActivitySessionCache();
    fixture.listeners.clear();
    for (const mock of fixture.bindings.values()) mock.mockReset().mockResolvedValue([]);
    binding('EnsureDailySummary')
        .mockRejectedValueOnce(new Error('temporary read failure'))
        .mockResolvedValue({ aiSummary: 'Saved after the retry', aiSummaryStatus: 'ready', hourlyActive: [], appBreakdown: [] });
    render(<Harness initialDate="2026-10-15" />);
    expect(screen.queryByText(enActivity.summary.card.dayNotWritten)).toBeNull();
    expect(screen.queryByRole('button', { name: enActivity.summary.card.writeIt })).toBeNull();
    expect(await screen.findByText('Saved after the retry', {}, { timeout: 5000 })).toBeInTheDocument();
    expect(screen.queryByText(enActivity.summary.card.loadFailed)).toBeNull();
    expect(binding('EnsureDailySummary').mock.calls.length).toBeGreaterThanOrEqual(2);
});
