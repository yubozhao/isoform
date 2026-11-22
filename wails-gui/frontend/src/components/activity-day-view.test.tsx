import { resetActivitySessionCache } from './timeline/activity-session-cache';
import { resetPageQueries } from '@/lib/page-queries';
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import i18n from '@/i18n';
import enActivity from '@/i18n/locales/en/activity.json';
import type { ActivityOutcome, ActivityThreadView, ActivityView, MeetingNotesView } from '@/lib/wails-shim-app';
import type { CronRunWithJobName } from './timeline/useCalendarData';
import { toast } from 'sonner';
import { resetTodayChatDrafts } from '@/lib/today-chat-prepared';
import { ActivityDayView, displayableRunResult } from './activity-day-view';
import { SHOW_THREAD_TIME_SPENT, threadMetaLine } from './activity-thread-row';

beforeEach(() => {
    resetActivitySessionCache();
    resetPageQueries();
    // jsdom has no layout or scrolling implementation.
    Element.prototype.scrollIntoView = vi.fn();
});

afterEach(() => vi.useRealTimers());

// Every Wails binding resolves through the shim (vite aliases wailsjs/go/main/App
// to it), so one Proxy mock covers the whole import graph of the Day view.
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

vi.mock('@/components/entry-content', () => ({
    EntryDetail: ({ content }: { content: string }) => <div data-testid="entry-detail">{content}</div>,
}));

vi.mock('./activity-meeting-notes', () => ({
    MeetingNotesCard: ({ meeting }: { meeting: MeetingNotesView }) => (
        <div data-testid="meeting-notes-card">{meeting.title}</div>
    ),
}));

vi.mock('sonner', () => ({ toast: Object.assign(vi.fn(), { error: vi.fn(), success: vi.fn() }) }));

async function emitRuntime(name: string, payload?: unknown) {
    await Promise.all([...(fixture.listeners.get(name) ?? [])].map((handler) => handler(payload)));
}

function binding(name: string) {
    if (!fixture.bindings.has(name)) fixture.bindings.set(name, vi.fn().mockResolvedValue([]));
    return fixture.bindings.get(name)!;
}

const DAY = new Date(2026, 9, 19); // Oct 19, 2026 — a past day
const DAY_START = new Date(2026, 9, 19, 9, 0).getTime();

function activity(overrides: Partial<ActivityView> = {}): ActivityView {
    return {
        id: 'activity-1',
        status: 'settled',
        title: 'Drafting the Halcyon studio guide',
        description: 'Reworked the welcome tour and answered opening questions.',
        latestChange: 'Wrapped up the welcome copy.',
        category: 'build',
        categoryColor: '',
        entities: [
            { type: 'person', name: 'Maya Chen' },
            { type: 'repo', name: 'isoform' },
        ],
        apps: [
            { appKey: 'com.claude.app', appName: 'Claude', activeMs: 3600_000 },
            { appKey: 'com.apple.safari', appName: 'Safari', activeMs: 1800_000 },
        ],
        activeMs: 5400_000,
        startMs: DAY_START,
        endMs: DAY_START + 5400_000,
        runCount: 74,
        thumbnails: [],
        inProgress: false,
        latestLink: { runId: 'run-9', reasonKind: 'sharedRepo', token: 'isoform' },
        ...overrides,
    };
}

function meeting(overrides: Partial<MeetingNotesView> = {}): MeetingNotesView {
    return {
        meetingKey: 'meeting-1',
        meetingApp: 'Halcyon Meet',
        title: 'Halcyon studio review',
        startTime: DAY_START + 3600_000,
        endTime: DAY_START + 5400_000,
        summary: 'Kept the welcome tour.',
        decisions: [],
        followups: [],
        userNotes: '',
        parts: [],
        ...overrides,
    };
}

function run(overrides: Partial<CronRunWithJobName> = {}): CronRunWithJobName {
    return {
        id: 'run-1',
        jobId: 'job-1',
        startedAt: DAY_START + 7200_000,
        finishedAt: DAY_START + 7260_000,
        status: 'ok',
        triggerKind: 'schedule',
        error: '',
        durationMs: 60_000,
        resultSummary: 'Sent the weekly recap to the team.',
        hasLogs: true,
        notifyResult: '',
        jobName: 'Weekly recap',
        ...overrides,
    };
}

function dailyData(overrides: Record<string, unknown> = {}) {
    return {
        date: '2026-10-19',
        aiSummary: 'You drafted the Halcyon studio guide and reviewed the opening plan.',
        aiSummaryStatus: 'done',
        aiSummaryLanguage: 'en',
        isPartial: false,
        summaryCreatedAt: DAY_START,
        totalActiveMs: 2 * 3600_000 + 5 * 60_000,
        activeAvgMs: 3600_000,
        sessionCount: 12,
        sessionAvg: 0,
        uniqueApps: 5,
        uniqueAppsAvg: 0,
        snapshotCount: 0,
        ocrPageCount: 0,
        hourlyActive: Array.from({ length: 24 }, (_, h) => (h === 10 ? 55 : 10)),
        appBreakdown: [
            { appName: 'Claude', bundleId: 'com.claude.app', activeMs: 4500_000, percentage: 60 },
            { appName: 'Safari', bundleId: 'com.apple.safari', activeMs: 3000_000, percentage: 40 },
            { appName: 'Finder', bundleId: 'com.apple.finder', activeMs: 60_000, percentage: 1 },
        ],
        moments: [],
        topOcrKeywords: [],
        topWindowTitles: [],
        ...overrides,
        // eslint-disable-next-line @typescript-eslint/no-explicit-any
    } as any;
}

function renderDay(overrides: Partial<Parameters<typeof ActivityDayView>[0]> = {}) {
    return render(
        <ActivityDayView
            date={DAY}
            searchQuery=""
            bridgePort={4242}
            dailyData={dailyData()}
            automationRuns={[run()]}
            onOpenRunDetail={vi.fn()}
            // These tests cover the list As recorded shows: today's What you
            // worked on, unchanged. Stitched by work has its own file.
            workedOnView="recorded"
            {...overrides}
        />,
    );
}

describe('displayableRunResult', () => {
    it('keeps a plain one-line result', () => {
        expect(displayableRunResult('Sent the weekly recap.')).toBe('Sent the weekly recap.');
    });

    it('drops markup and JSON output', () => {
        expect(displayableRunResult('<news_json>{"items":[]}</news_json>')).toBeNull();
        expect(displayableRunResult('<p>Hello</p>')).toBeNull();
        expect(displayableRunResult('{"items":[]}')).toBeNull();
        expect(displayableRunResult('["a"]')).toBeNull();
        expect(displayableRunResult('prefix <news_json> …')).toBeNull();
    });

    it('drops multi-line and empty output', () => {
        expect(displayableRunResult('line one\nline two')).toBeNull();
        expect(displayableRunResult('')).toBeNull();
        expect(displayableRunResult('   ')).toBeNull();
        expect(displayableRunResult(undefined)).toBeNull();
    });
});

describe('Activity Day view', () => {
    beforeEach(async () => {
        resetActivitySessionCache();
        fixture.bindings.forEach((mock) => mock.mockReset().mockResolvedValue([]));
        fixture.listeners.clear();
        binding('GetActivitiesInRange').mockResolvedValue([activity()]);
        binding('GetRecentMeetingNotes').mockResolvedValue([meeting()]);
        await i18n.changeLanguage('en');
    });

    // computer-conformance-answers A4: Your day is the record only.
    it('renders Your day as the feed with no summary, numbers line, details or noticed, then the merged day list', async () => {
        binding('GetDayNoticed').mockResolvedValue([{ id: 's', kind: 'follow_up', text: 'Fixture notice', why: '', momentId: 'x', momentTitle: 'X', momentStartMs: 0 }]);
        renderDay();
        expect(await screen.findByTestId('activity-feed')).toBeInTheDocument();
        expect(screen.queryByText(/You drafted the Halcyon studio guide/)).not.toBeInTheDocument();
        expect(screen.queryByText(/2h 5m active/)).not.toBeInTheDocument();
        expect(screen.queryByRole('button', { name: 'Details' })).not.toBeInTheDocument();
        expect(screen.queryByText('Fixture notice')).not.toBeInTheDocument();
        expect(binding('GetDayNoticed')).not.toHaveBeenCalled();
        expect(binding('GenerateDailySummary')).not.toHaveBeenCalled();
        expect(screen.getByText('What you worked on')).toBeInTheDocument();
        // Activities, meetings and scheduled runs share one list.
        expect(await screen.findByText('Drafting the Halcyon studio guide')).toBeInTheDocument();
        expect(screen.getByText('Halcyon studio review')).toBeInTheDocument();
        expect(screen.getByText('Isoform ran Weekly recap')).toBeInTheDocument();
        expect(screen.getByText('Sent the weekly recap to the team.')).toBeInTheDocument();
    });

    // Conformance answers v1.1.0 P1 (AC9): agent runs the screen saw are the
    // Screen tab's own rows, after yours, and not counted as your work.
    it("lists the day's screen-seen agent runs in What you worked on, apart from your rows", async () => {
        binding('GetActivityFeed').mockResolvedValue([
            { id: 'run-1', kind: 'agent', startMs: DAY.getTime() + 15 * 3_600_000, endMs: DAY.getTime() + 15.5 * 3_600_000, activeMs: 1_200_000, app: 'Claude', basis: { setting: 'screen', agent: 'Claude' } },
            { id: 'm-1', kind: 'screen', startMs: DAY.getTime() + 9 * 3_600_000, endMs: DAY.getTime() + 9.5 * 3_600_000, title: 'Feed screen row', basis: { setting: 'screen' } },
        ]);
        renderDay({ section: 'workedOn' });
        const runs = await screen.findByTestId('worked-on-agent-runs');
        expect(within(runs).getByText('Screen · Claude')).toBeInTheDocument();
        expect(within(runs).getByText('Claude working on its own · not counted as your work')).toBeInTheDocument();
        expect(within(runs).queryByText('Feed screen row')).toBeNull();
        expect(runs.querySelector('[data-feed-dot]')!.className).toMatch(/\bbg-app-indigo-text\b/);
        // After your own rows, and not one of them.
        const own = await screen.findByText('Drafting the Halcyon studio guide');
        expect(own.compareDocumentPosition(runs) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
        expect(runs.contains(own)).toBe(false);
    });

    it('says when the agent runs could not be read, with Try again', async () => {
        binding('GetActivityFeed').mockRejectedValueOnce(new Error('timeout')).mockResolvedValueOnce([
            { id: 'run-1', kind: 'agent', startMs: DAY.getTime(), endMs: DAY.getTime() + 60_000, activeMs: 60_000, app: 'Codex', basis: { setting: 'screen', agent: 'Codex' } },
        ]);
        renderDay({ section: 'workedOn' });
        const runs = await screen.findByTestId('worked-on-agent-runs');
        expect(within(runs).getByText(enActivity.day.agentRunsFailed)).toBeInTheDocument();
        fireEvent.click(within(runs).getByRole('button', { name: 'Try again' }));
        expect(await within(runs).findByText('Screen · Codex')).toBeInTheDocument();
    });

    it('shows no agent runs on a day without them', async () => {
        renderDay({ section: 'workedOn' });
        await screen.findByText('Drafting the Halcyon studio guide');
        expect(screen.queryByTestId('worked-on-agent-runs')).toBeNull();
    });

    it('shows only the feed on the Your day section', async () => {
        renderDay({ section: 'yourDay' });
        expect(await screen.findByTestId('activity-feed')).toBeInTheDocument();
        expect(screen.queryByText('What you worked on')).not.toBeInTheDocument();
        expect(screen.queryByText('Drafting the Halcyon studio guide')).not.toBeInTheDocument();
    });

    it('shows no run counts, keyword chips, link reasons or comparison stats', async () => {
        renderDay();
        await screen.findByText('Drafting the Halcyon studio guide');
        // No "74 runs" run count from the activity card.
        expect(screen.queryByText(/74/)).not.toBeInTheDocument();
        expect(screen.queryByText(/\bruns?\b/)).not.toBeInTheDocument();
        // No clickable keyword chips for entities.
        expect(screen.queryByRole('button', { name: 'Maya Chen' })).not.toBeInTheDocument();
        expect(screen.queryByRole('button', { name: 'isoform' })).not.toBeInTheDocument();
        // No "Linked because ..." line and no "Not related" correction.
        expect(screen.queryByText(/Linked because/)).not.toBeInTheDocument();
        expect(screen.queryByText('Not related')).not.toBeInTheDocument();
        // No session counts, no peak, no vs-average percentages.
        expect(screen.queryByText(/12 sessions?/)).not.toBeInTheDocument();
        expect(screen.queryByText(/peak/i)).not.toBeInTheDocument();
        expect(screen.queryByText(/vs .*avg/i)).not.toBeInTheDocument();
    });

    it('hides markup run output behind an Open result link', async () => {
        renderDay({
            automationRuns: [
                run({ id: 'run-json', resultSummary: '<news_json>{"items":[]}</news_json>' }),
            ],
        });
        await screen.findByText('Isoform ran Weekly recap');
        expect(screen.queryByText(/news_json/)).not.toBeInTheDocument();
        const openResult = screen.getByText('Show');
        expect(openResult).toBeInTheDocument();
        // The row opens the run's detail dialog via the parent.
        fireEvent.click(openResult.closest('button')!);
        expect(screen.getByText('Isoform ran Weekly recap')).toBeInTheDocument();
    });

    // Activity review v1.0.7 AR16 (v1.0.0 #16): the topic finds run reads in words.
    it('says Isoform updated your topics for the topic finds run', async () => {
        renderDay({ automationRuns: [run({ id: 'run-topics', jobName: 'New on your topics', resultSummary: '' })] });
        expect(await screen.findByText('Isoform updated your topics')).toBeInTheDocument();
        expect(screen.queryByText(/Isoform ran New on your topics/)).toBeNull();
        expect(screen.getByText('Show')).toBeInTheDocument();
    });

    it('opens one row at a time and shows its details in place', async () => {
        binding('GetActivitiesInRange').mockResolvedValue([
            activity(),
            activity({ id: 'activity-2', title: 'Design review notes', description: 'Reviewed the new onboarding flow.', entities: [] }),
        ]);
        renderDay();
        const first = await screen.findByText('Drafting the Halcyon studio guide');
        fireEvent.click(first.closest('button')!);
        expect(screen.getByText('People: Maya Chen')).toBeInTheDocument();
        expect(screen.getByText('Links: isoform')).toBeInTheDocument();
        fireEvent.click(screen.getByText('Design review notes').closest('button')!);
        expect(screen.queryByText('People: Maya Chen')).not.toBeInTheDocument();
        expect(screen.getAllByText('Reviewed the new onboarding flow.').length).toBeGreaterThan(0);
    });

    it('expands a meeting row into its notes card', async () => {
        renderDay();
        fireEvent.click((await screen.findByText('Halcyon studio review')).closest('button')!);
        expect(screen.getByTestId('meeting-notes-card')).toHaveTextContent('Halcyon studio review');
    });

    // First run 1.0: an empty What you worked on today says when it fills;
    // another day keeps its plain empty line.
    it("says when today's What you worked on fills, and leaves other days alone", async () => {
        binding('GetActivitiesInRange').mockResolvedValue([]);
        binding('GetRecentMeetingNotes').mockResolvedValue([]);
        const view = renderDay({ date: new Date(), section: 'workedOn', automationRuns: [] });
        expect(await screen.findByText('What you worked on shows up after about 30 minutes.')).toBeInTheDocument();
        view.unmount();
        renderDay({ section: 'workedOn', automationRuns: [] });
        expect(await screen.findByText('Nothing here for this day.')).toBeInTheDocument();
    });

    it('filters the day lists by the search box', async () => {
        renderDay({ searchQuery: 'guide' });
        expect(await screen.findByText('Drafting the Halcyon studio guide')).toBeInTheDocument();
        expect(screen.queryByText('Halcyon studio review')).not.toBeInTheDocument();
        expect(screen.queryByText('Isoform ran Weekly recap')).not.toBeInTheDocument();
    });
});

// Behaviors the old Activities-tab card carried, kept in the Day view's
// opened row (desk review round 1: none of these was on the removal list).
describe('Activity Day view — restored row behaviors', () => {
    beforeEach(async () => {
        resetActivitySessionCache();
        fixture.bindings.forEach((mock) => mock.mockReset().mockResolvedValue([]));
        fixture.listeners.clear();
        binding('GetActivitiesInRange').mockResolvedValue([activity()]);
        binding('GetRecentMeetingNotes').mockResolvedValue([]);
        binding('DeleteActivity').mockResolvedValue(undefined);
        await i18n.changeLanguage('en');
    });

    afterEach(() => {
        vi.useRealTimers();
    });

    const openFirstRow = async () => {
        fireEvent.click((await screen.findByText('Drafting the Halcyon studio guide')).closest('button')!);
    };

    it('deletes the activity from history after the confirm step', async () => {
        renderDay();
        await openFirstRow();
        fireEvent.click(screen.getByRole('button', { name: 'Delete from history' }));
        fireEvent.click(screen.getByRole('button', { name: 'Delete' }));
        await waitFor(() => expect(binding('DeleteActivity')).toHaveBeenCalledWith('activity-1'));
        await waitFor(() => expect(screen.queryByText('Drafting the Halcyon studio guide')).not.toBeInTheDocument());
    });

    it('keeps the activity when the delete is cancelled', async () => {
        renderDay();
        await openFirstRow();
        fireEvent.click(screen.getByRole('button', { name: 'Delete from history' }));
        fireEvent.click(screen.getByRole('button', { name: 'Cancel' }));
        expect(binding('DeleteActivity')).not.toHaveBeenCalled();
        expect(screen.getByText('Drafting the Halcyon studio guide')).toBeInTheDocument();
        // The confirm step collapses back to the quiet action.
        expect(screen.getByRole('button', { name: 'Delete from history' })).toBeInTheDocument();
    });

    function outcome(overrides: Partial<ActivityOutcome> = {}): ActivityOutcome {
        return {
            id: 'outcome-1',
            activityId: 'activity-1',
            runId: 'run-chat',
            status: 'reported',
            reason: 'message_sent',
            occurredAt: DAY_START,
            target: 'Claude',
            evidence: [],
            ...overrides,
        };
    }

    it('shows the outcome line with its destination, time and evidence reason — without calling it done', async () => {
        binding('GetActivitiesInRange').mockResolvedValue([activity({ latestOutcome: outcome() })]);
        renderDay();
        await openFirstRow();
        const time = new Date(DAY_START).toLocaleTimeString('en', { hour: 'numeric', minute: '2-digit' });
        expect(time).toBe('9:00 AM');
        expect(
            screen.getByText(`Reported to Claude at ${time}. A message with this task's details was sent.`),
        ).toBeInTheDocument();
        expect(screen.queryByText(/^Done/)).not.toBeInTheDocument();
    });

    it.each([
        ['fixed', 'target_changed', 'Fixed at', 'A later observation showed the target changed.'],
        ['done', 'user_completed', 'Done at', 'You marked this task complete in chat.'],
        ['done', 'completion_click', 'Done at', "A completion action included this task's details."],
        ['abandoned', 'quiet_timeout', 'Abandoned at', 'The task stayed paused without new evidence.'],
    ] as const)('shows %s with its %s reason', async (status, reason, label, explanation) => {
        binding('GetActivitiesInRange').mockResolvedValue([activity({ latestOutcome: outcome({ status, reason }) })]);
        renderDay();
        await openFirstRow();
        expect(screen.getByText((text) => text.startsWith(label) && text.endsWith(explanation))).toBeInTheDocument();
    });

    it('translates the outcome line into Chinese', async () => {
        await i18n.changeLanguage('zh-CN');
        binding('GetActivitiesInRange').mockResolvedValue([activity({ latestOutcome: outcome() })]);
        renderDay();
        await openFirstRow();
        expect(screen.getByText(/向 Claude 汇报。已发送包含此任务信息的消息。/)).toBeInTheDocument();
    });

    it('shows the Live badge on every activity still in progress today', async () => {
        vi.useFakeTimers({ shouldAdvanceTime: true });
        vi.setSystemTime(new Date(2026, 9, 19, 12));
        binding('GetActivitiesInRange').mockResolvedValue([
            activity({ inProgress: true }),
            activity({ id: 'activity-2', title: 'Another recent task', inProgress: true }),
        ]);
        renderDay();
        await screen.findByText('Another recent task');
        expect(screen.getAllByText('In progress')).toHaveLength(2);
    });

    it('does not mark a settled activity live merely because it is listed', async () => {
        renderDay();
        await screen.findByText('Drafting the Halcyon studio guide');
        expect(screen.queryByText('In progress')).not.toBeInTheDocument();
    });

    // Snappy 1.0: no 60-second refetch. The badge expires when the session's
    // capture event arrives, applied at most every 5 minutes.
    it('never refetches an in-progress activity on a timer; its badge expires with the capture event', async () => {
        vi.useFakeTimers({ shouldAdvanceTime: true });
        vi.setSystemTime(new Date(2026, 9, 19, 12));
        binding('GetActivitiesInRange')
            .mockResolvedValueOnce([activity({ inProgress: true })])
            .mockResolvedValue([activity()]);
        renderDay();
        await screen.findByText('In progress');
        await act(async () => {
            await vi.advanceTimersByTimeAsync(10 * 60_000);
        });
        const dayReads = () => binding('GetActivitiesInRange').mock.calls.filter(([start]) => start === DAY.getTime()).length;
        expect(dayReads()).toBe(1);
        expect(screen.getByText('In progress')).toBeInTheDocument();
        await act(async () => {
            await emitRuntime('activity:session-saved');
            await vi.advanceTimersByTimeAsync(0);
        });
        await waitFor(() => expect(screen.queryByText('In progress')).not.toBeInTheDocument());
        expect(dayReads()).toBe(2);
    });

    it('clears the minute refresh timer on unmount', async () => {
        vi.useFakeTimers({ shouldAdvanceTime: true });
        vi.setSystemTime(new Date(2026, 9, 19, 12));
        binding('GetActivitiesInRange').mockResolvedValue([activity({ inProgress: true })]);
        const { unmount } = renderDay();
        await screen.findByText('In progress');
        const callsBeforeUnmount = binding('GetActivitiesInRange').mock.calls.length;
        unmount();
        await act(async () => {
            await vi.advanceTimersByTimeAsync(180_000);
        });
        expect(binding('GetActivitiesInRange')).toHaveBeenCalledTimes(callsBeforeUnmount);
    });
});

describe('Activity Day view — row actions', () => {
    const LONG_TITLE =
        'Review and merge isoform PRs, direct Kimi workers, and plan desktop app release in the isoform hub';

    beforeEach(async () => {
        resetActivitySessionCache();
        fixture.bindings.forEach((mock) => mock.mockReset().mockResolvedValue([]));
        fixture.listeners.clear();
        vi.mocked(toast).mockReset();
        vi.mocked(toast.success).mockReset();
        vi.mocked(toast.error).mockReset();
        binding('GetActivitiesInRange').mockResolvedValue([
            activity({ title: LONG_TITLE, followUpText: 'Follow up on review and merge isoform PRs' }),
        ]);
        binding('GetRecentMeetingNotes').mockResolvedValue([]);
        await i18n.changeLanguage('en');
    });

    const openRow = async (title = LONG_TITLE) => {
        fireEvent.click((await screen.findByText(title)).closest('button')!);
    };

    it('keeps Delete from history, the outcome line and the Live badge beside the new actions today', async () => {
        vi.useFakeTimers({ shouldAdvanceTime: true });
        vi.setSystemTime(new Date(2026, 9, 19, 12));
        binding('GetActivitiesInRange').mockResolvedValue([
            activity({
                title: LONG_TITLE,
                inProgress: true,
                latestOutcome: {
                    id: 'o1', activityId: 'activity-1', runId: 'r1', status: 'reported', reason: 'message_sent',
                    occurredAt: DAY_START, target: 'Claude', evidence: [],
                },
            }),
        ]);
        renderDay();
        await openRow();
        expect(screen.getByText('In progress')).toBeInTheDocument();
        expect(screen.getByText(/Reported to Claude at/)).toBeInTheDocument();
        for (const name of ['Add follow-up', 'Remind me', 'Not right', 'Delete from history']) {
            expect(screen.getByRole('button', { name })).toBeInTheDocument();
        }
    });

    it('opens a one-line field with the short prepared text, not the long AI title, and adds the edited line', async () => {
        const added = { openLoopId: 'loop-1', eventId: 'event-1', text: 'Follow up with Kimi about the PRs' };
        binding('AddActivityFollowUp').mockResolvedValue(added);
        binding('UndoActivityFollowUp').mockResolvedValue(undefined);
        renderDay();
        await openRow();
        fireEvent.click(screen.getByRole('button', { name: 'Add follow-up' }));
        const field = screen.getByRole('textbox', { name: 'Follow-up' }) as HTMLInputElement;
        expect(field.value).toBe('Follow up on review and merge isoform PRs');
        expect(field.value.length).toBeLessThanOrEqual(80);
        expect(binding('AddActivityFollowUp')).not.toHaveBeenCalled();

        fireEvent.change(field, { target: { value: 'Follow up with Kimi about the PRs' } });
        fireEvent.keyDown(field, { key: 'Enter' });
        await waitFor(() =>
            expect(binding('AddActivityFollowUp')).toHaveBeenCalledWith(
                'activity-1',
                'Follow up with Kimi about the PRs',
                expect.any(String),
            ),
        );
        expect(await screen.findByText('Follow-up added')).toBeInTheDocument();
        expect(screen.queryByRole('button', { name: 'Add follow-up' })).not.toBeInTheDocument();
        expect(screen.getByRole('status')).toHaveTextContent('Added to What needs your attention.');

        // Undo, in place, takes it back.
        fireEvent.click(screen.getByRole('button', { name: 'Undo' }));
        await waitFor(() => expect(binding('UndoActivityFollowUp')).toHaveBeenCalledWith('loop-1', 'event-1'));
        expect(await screen.findByRole('button', { name: 'Add follow-up' })).toBeInTheDocument();
        expect(screen.getByRole('status')).toHaveTextContent('Follow-up removed.');
        expect(toast).not.toHaveBeenCalled();
    });

    it('Esc closes the follow-up field without adding anything', async () => {
        renderDay();
        await openRow();
        fireEvent.click(screen.getByRole('button', { name: 'Add follow-up' }));
        fireEvent.keyDown(screen.getByRole('textbox', { name: 'Follow-up' }), { key: 'Escape' });
        expect(screen.queryByRole('textbox', { name: 'Follow-up' })).not.toBeInTheDocument();
        expect(binding('AddActivityFollowUp')).not.toHaveBeenCalled();
    });

    it('says plainly when a follow-up could not be added', async () => {
        binding('AddActivityFollowUp').mockRejectedValue(new Error('Purpose storage is unavailable'));
        renderDay();
        await openRow();
        fireEvent.click(screen.getByRole('button', { name: 'Add follow-up' }));
        fireEvent.click(screen.getByRole('button', { name: 'Add' }));
        expect(await screen.findByRole('alert')).toHaveTextContent("Couldn't add the follow-up. Try again.");
        expect(screen.getByRole('textbox', { name: 'Follow-up' })).toBeInTheDocument();
        expect(toast.error).not.toHaveBeenCalled();
        binding('AddActivityFollowUp').mockResolvedValue({ openLoopId: 'l', eventId: 'e', text: 'x' });
        fireEvent.click(within(screen.getByRole('alert')).getByRole('button', { name: 'Try again' }));
        await waitFor(() => expect(binding('AddActivityFollowUp')).toHaveBeenCalledTimes(2));
    });

    it('shows an open follow-up as added', async () => {
        binding('GetActivitiesInRange').mockResolvedValue([
            activity({ title: LONG_TITLE, followUp: { openLoopId: 'l', eventId: 'e', text: 'Follow up on the PRs' } }),
        ]);
        renderDay();
        await openRow();
        expect(screen.getByText('Follow-up added')).toBeInTheDocument();
        expect(screen.queryByRole('button', { name: 'Add follow-up' })).not.toBeInTheDocument();
    });

    it("offers Go's quick reminder times and sets the one picked", async () => {
        const tomorrow = new Date();
        tomorrow.setDate(tomorrow.getDate() + 1);
        tomorrow.setHours(9, 0, 0, 0);
        binding('GetActivityReminderChoices').mockResolvedValue([{ id: 'tomorrow_morning', atMs: tomorrow.getTime() }]);
        binding('SetActivityReminder').mockResolvedValue(tomorrow.getTime());
        renderDay();
        await openRow();
        fireEvent.keyDown(screen.getByRole('button', { name: 'Remind me' }), { key: 'Enter' });
        const item = await screen.findByRole('menuitem', { name: /^Tomorrow morning, tomorrow at 9:00/ });
        expect(screen.getByRole('menuitem', { name: 'Pick a time' })).toBeInTheDocument();
        fireEvent.click(item);
        await waitFor(() => expect(binding('SetActivityReminder')).toHaveBeenCalledWith('activity-1', tomorrow.getTime()));
        expect((await screen.findAllByText(/^Reminder set for tomorrow at 9:00/)).length).toBeGreaterThan(0);
        expect(screen.getByRole('status')).toHaveTextContent('It will reach your computer and phone.');
        expect(toast.success).not.toHaveBeenCalled();
    });

    it('shows a pending reminder and cancels it', async () => {
        const later = new Date();
        later.setDate(later.getDate() + 1);
        later.setHours(9, 0, 0, 0);
        binding('GetActivitiesInRange').mockResolvedValue([activity({ title: LONG_TITLE, reminderAt: later.getTime() })]);
        binding('CancelActivityReminder').mockResolvedValue(undefined);
        renderDay();
        await openRow();
        expect(screen.getByText(/^Reminder set for tomorrow at 9:00/)).toBeInTheDocument();
        fireEvent.click(screen.getByRole('button', { name: 'Cancel reminder' }));
        await waitFor(() => expect(binding('CancelActivityReminder')).toHaveBeenCalledWith('activity-1'));
        await waitFor(() => expect(screen.queryByText(/^Reminder set for/)).not.toBeInTheDocument());
    });

    it('Not right edits the title and summary in place and saves them', async () => {
        binding('CorrectActivity').mockResolvedValue(undefined);
        renderDay();
        await openRow();
        fireEvent.click(screen.getByRole('button', { name: 'Not right' }));
        const title = screen.getByRole('textbox', { name: 'Title' }) as HTMLInputElement;
        const summary = screen.getByRole('textbox', { name: 'Summary' }) as HTMLInputElement;
        expect(title.value).toBe(LONG_TITLE);
        expect(summary.value).toBe('Reworked the welcome tour and answered opening questions.');
        fireEvent.change(title, { target: { value: 'Merging the isoform PRs' } });
        fireEvent.click(screen.getByRole('button', { name: 'Save' }));
        await waitFor(() =>
            expect(binding('CorrectActivity')).toHaveBeenCalledWith(
                'activity-1',
                'Merging the isoform PRs',
                'Reworked the welcome tour and answered opening questions.',
            ),
        );
        expect(screen.getByRole('status')).toHaveTextContent('Saved. Isoform will name things your way.');
        expect(screen.queryByRole('textbox', { name: 'Title' })).not.toBeInTheDocument();
    });

    it('Not right will not save an empty title', async () => {
        renderDay();
        await openRow();
        fireEvent.click(screen.getByRole('button', { name: 'Not right' }));
        fireEvent.change(screen.getByRole('textbox', { name: 'Title' }), { target: { value: '   ' } });
        expect(screen.getByRole('button', { name: 'Save' })).toBeDisabled();
    });

    it('a private moment says why it has no follow-up or reminder and keeps Not right and Delete', async () => {
        binding('GetActivitiesInRange').mockResolvedValue([activity({ title: LONG_TITLE, actionsWithheld: 'private' })]);
        renderDay();
        await openRow();
        expect(screen.getByText(/keeps it out of follow-ups and reminders/)).toBeInTheDocument();
        expect(screen.queryByRole('button', { name: 'Add follow-up' })).not.toBeInTheDocument();
        expect(screen.queryByRole('button', { name: 'Remind me' })).not.toBeInTheDocument();
        expect(screen.getByRole('button', { name: 'Not right' })).toBeInTheDocument();
        expect(screen.getByRole('button', { name: 'Delete from history' })).toBeInTheDocument();
    });

    it('translates the row actions into Chinese', async () => {
        await i18n.changeLanguage('zh-CN');
        renderDay();
        await openRow();
        for (const name of ['添加跟进', '提醒我', '不对']) {
            expect(screen.getByRole('button', { name })).toBeInTheDocument();
        }
    });
});

describe('Activity Day view — threads', () => {
    const brief = activity({
        id: 'brief',
        title: 'Fix morning briefing generation',
        startMs: new Date(2026, 9, 19, 9, 10).getTime(),
        endMs: new Date(2026, 9, 19, 9, 51).getTime(),
        latestOutcome: undefined,
    });
    const workers = activity({
        id: 'workers',
        title: 'Coordinate Claude workers',
        startMs: new Date(2026, 9, 19, 11, 41).getTime(),
        endMs: new Date(2026, 9, 19, 11, 45).getTime(),
        inProgress: true,
    });
    const hats = activity({
        id: 'hats',
        title: 'Research wearable hats',
        startMs: new Date(2026, 9, 19, 9, 21).getTime(),
        endMs: new Date(2026, 9, 19, 9, 27).getTime(),
    });
    const isoformThread: ActivityThreadView = {
        id: 'th_brief',
        title: 'Isoform: brief fix, workers',
        kind: 'work',
        userTitled: false,
        startMs: brief.startMs,
        endMs: workers.endMs,
        timeSpentMs: 17 * 60_000,
        activityIds: ['brief', 'workers'],
        stretches: [
            { start: brief.startMs, end: brief.endMs, activeMs: 13 * 60_000, activityIds: ['brief'], resumePoint: null },
            { start: workers.startMs, end: workers.endMs, activeMs: 4 * 60_000, activityIds: ['workers'], resumePoint: null },
        ],
        gaps: [{ start: brief.endMs, end: workers.startMs, minutes: 110, label: 'away', threadId: '' }],
        activeMs: 17 * 60_000,
        linkReason: { kind: 'rule', keys: ['file:brief.go'], text: '', resumedWithinMinutes: 110 },
        interruption: false,
        reviewed: false,
        unfinished: false,
    };
    const hatsThread: ActivityThreadView = {
        ...isoformThread,
        id: 'th_hats',
        title: 'Research wearable hats',
        startMs: hats.startMs,
        endMs: hats.endMs,
        timeSpentMs: 5 * 60_000,
        activityIds: ['hats'],
        stretches: [{ start: hats.startMs, end: hats.endMs, activeMs: 5 * 60_000, activityIds: ['hats'], resumePoint: null }],
        gaps: [],
        activeMs: 5 * 60_000,
        interruption: true,
    };

    beforeEach(async () => {
        resetActivitySessionCache();
        fixture.bindings.forEach((mock) => mock.mockReset().mockResolvedValue([]));
        fixture.listeners.clear();
        binding('GetActivitiesInRange').mockResolvedValue([brief, hats, workers]);
        binding('GetRecentMeetingNotes').mockResolvedValue([]);
        resetTodayChatDrafts();
        binding('GetDayThreads').mockResolvedValue({ grouped: true, threads: [isoformThread, hatsThread] });
        binding('MoveActivityToThread').mockResolvedValue('th_workers');
        binding('RenameActivityThread').mockResolvedValue(undefined);
        await i18n.changeLanguage('en');
    });

    it('shows one row per thread with its span, active time and moment count', async () => {
        renderDay({ automationRuns: [] });
        expect(await screen.findByText('Isoform: brief fix, workers')).toBeInTheDocument();
        expect(screen.getByText('9:10–11:45 AM · 17m active')).toBeInTheDocument();
        // The thread's moments stay inside it until it is opened.
        expect(screen.queryByText('Fix morning briefing generation')).not.toBeInTheDocument();
        // Historical source status never makes a past-day thread live.
        expect(screen.queryByText('In progress')).toBeNull();
    });

    it('shows agent work as one labelled line, apart from the threads', async () => {
        binding('GetDayThreads').mockResolvedValue({
            grouped: true,
            threads: [isoformThread, hatsThread],
            agents: { runs: 3, activeMs: 42 * 60_000 },
        });
        renderDay({ automationRuns: [], workedOnView: 'stitched' });
        expect(await screen.findByText('Isoform: brief fix, workers')).toBeInTheDocument();
        expect(screen.getByTestId('activity-agents-line')).toHaveTextContent(
            'Your agents worked 42m in 3 sessions. It is not counted as your work.',
        );
    });

    it('shows no agent line on a day without agent work', async () => {
        renderDay({ automationRuns: [], workedOnView: 'stitched' });
        expect(await screen.findByText('Isoform: brief fix, workers')).toBeInTheDocument();
        expect(screen.queryByTestId('activity-agents-line')).toBeNull();
    });

    it('clips the recorded thread span at the same local day edge', async () => {
        binding('GetDayThreads').mockResolvedValue({ grouped: true, threads: [{ ...isoformThread,
            endMs: new Date(2026, 9, 20, 12).getTime() }] });
        renderDay({ automationRuns: [], workedOnView: 'recorded' });
        expect(await screen.findByText('9:10 AM–12:00 AM · 17m active')).toBeInTheDocument();
        expect(screen.queryByText('In progress')).toBeNull();
    });

    it('keeps a one-moment thread as the plain moment row', async () => {
        renderDay({ automationRuns: [] });
        expect(await screen.findByText('Research wearable hats')).toBeInTheDocument();
        expect(screen.queryByText(/1 moment/)).not.toBeInTheDocument();
    });

    it('opens a thread to its moments, which keep their own row actions', async () => {
        renderDay({ automationRuns: [] });
        fireEvent.click((await screen.findByText('Isoform: brief fix, workers')).closest('button')!);
        fireEvent.click(screen.getByText('Fix morning briefing generation').closest('button')!);
        expect(screen.getByRole('button', { name: 'Delete from history' })).toBeInTheDocument();
        expect(screen.getByRole('button', { name: 'Add follow-up' })).toBeInTheDocument();
        expect(screen.getByRole('button', { name: 'Remind me' })).toBeInTheDocument();
        expect(screen.getAllByRole('button', { name: 'Not right' })).toHaveLength(2);
        expect(screen.getByText('Coordinate Claude workers')).toBeInTheDocument();
    });

    it('offers Chat about this on a moment inside a thread, with that moment as context', async () => {
        const onStartChat = vi.fn();
        renderDay({ automationRuns: [], onStartChat });
        fireEvent.click((await screen.findByText('Isoform: brief fix, workers')).closest('button')!);
        fireEvent.click(screen.getByText('Fix morning briefing generation').closest('button')!);
        fireEvent.click(screen.getByRole('button', { name: 'Chat about this' }));
        const field = screen.getByRole('textbox', { name: 'Ask anything about this' });
        expect(field).toHaveFocus();
        binding('GetMainChat').mockResolvedValue({ session: { localId: 'main-1' } });
        fireEvent.change(field, { target: { value: 'What was left?' } });
        fireEvent.click(screen.getByRole('button', { name: 'Ask' }));
        await waitFor(() => expect(binding('AskMainChatWithContext')).toHaveBeenCalledWith('What was left?', {
            sourceKind: 'activity',
            sourceId: 'brief',
            dayStartMs: new Date(2026, 9, 19).getTime(),
        }));
        expect(onStartChat).not.toHaveBeenCalled();
        // Every moment, threaded or not, was prepared when the day loaded.
        await waitFor(() =>
            expect(binding('PrepareTodayChatDrafts')).toHaveBeenCalledWith(
                expect.arrayContaining([
                    { sourceKind: 'activity', sourceId: 'brief', dayStartMs: new Date(2026, 9, 19).getTime() },
                    { sourceKind: 'activity', sourceId: 'workers', dayStartMs: new Date(2026, 9, 19).getTime() },
                    { sourceKind: 'activity', sourceId: 'hats', dayStartMs: new Date(2026, 9, 19).getTime() },
                ]),
            ),
        );
    });

    it('moves a moment into its own thread through Not right', async () => {
        renderDay({ automationRuns: [] });
        fireEvent.click((await screen.findByText('Isoform: brief fix, workers')).closest('button')!);
        fireEvent.click(screen.getByText('Coordinate Claude workers').closest('button')!);
        // The thread header has its own Not right; the moment's is the last one.
        const notRight = screen.getAllByRole('button', { name: 'Not right' });
        fireEvent.click(notRight[notRight.length - 1]);
        const trigger = screen.getByRole('button', { name: 'Move to another thread' });
        fireEvent.pointerDown(trigger, { button: 0, pointerType: 'mouse' });
        fireEvent.click(await screen.findByText('Its own thread'));
        await waitFor(() => expect(binding('MoveActivityToThread')).toHaveBeenCalledWith('workers', ''));
    });

    it('renames a thread through Not right', async () => {
        renderDay({ automationRuns: [] });
        fireEvent.click((await screen.findByText('Isoform: brief fix, workers')).closest('button')!);
        fireEvent.pointerDown(screen.getAllByRole('button', { name: 'Not right' })[0], { button: 0, pointerType: 'mouse' });
        fireEvent.click(await screen.findByText('Rename thread'));
        const input = screen.getByRole('textbox', { name: 'Thread title' });
        fireEvent.change(input, { target: { value: 'Isoform hub' } });
        fireEvent.click(screen.getByRole('button', { name: 'Save' }));
        await waitFor(() => expect(binding('RenameActivityThread')).toHaveBeenCalledWith('th_brief', 'Isoform hub'));
    });

    it('opens the thread that holds a moment matching the search', async () => {
        renderDay({ automationRuns: [], searchQuery: 'claude workers' });
        expect(await screen.findByText('Coordinate Claude workers')).toBeInTheDocument();
        expect(screen.getByText('Isoform: brief fix, workers')).toBeInTheDocument();
    });

    it('shows moments ungrouped while a past day waits for its first grouping', async () => {
        binding('GetDayThreads').mockResolvedValue({ grouped: false, threads: [] });
        renderDay({ automationRuns: [] });
        expect(await screen.findByText('Fix morning briefing generation')).toBeInTheDocument();
        expect(screen.getByText('Coordinate Claude workers')).toBeInTheDocument();
    });

    it('reloads threads when Go reports new grouping', async () => {
        binding('GetDayThreads').mockResolvedValue({ grouped: false, threads: [] });
        renderDay({ automationRuns: [] });
        await screen.findByText('Fix morning briefing generation');
        binding('GetDayThreads').mockResolvedValue({ grouped: true, threads: [isoformThread] });
        act(() => fixture.listeners.get('activity:threads-updated')?.forEach((handler) => handler(undefined)));
        expect(await screen.findByText('Isoform: brief fix, workers')).toBeInTheDocument();
    });

    it('translates the thread line into Chinese', async () => {
        await i18n.changeLanguage('zh-CN');
        renderDay({ automationRuns: [] });
        // 24-hour range and Chinese duration (activity review v1.0.6 AR42).
        expect(await screen.findByText(/^\d{1,2}:\d{2}–\d{1,2}:\d{2} · 活跃 17 分钟$/)).toBeInTheDocument();
    });

    it('shows active time on thread rows now that the day line counts reading time', () => {
        const t = i18n.getFixedT('en', 'activity');
        // "10:02–10:30 AM · 28m active": the range, then the active time (activity review v1.0.6 AR40, AR48).
        expect(threadMetaLine(t, isoformThread)).toMatch(/^\d{1,2}:\d{2}( [AP]M)?–\d{1,2}:\d{2} [AP]M · 17m active$/);
        // Under a minute reads as "0m active"; the span says enough.
        expect(threadMetaLine(t, { ...isoformThread, timeSpentMs: 30_000 })).not.toMatch(/active/);
        expect(threadMetaLine(t, isoformThread, false)).not.toMatch(/active/);
        expect(SHOW_THREAD_TIME_SPENT).toBe(true);
    });
});

describe('Activity Day view — Chat about this', () => {
    beforeEach(async () => {
        resetActivitySessionCache();
        fixture.bindings.forEach((mock) => mock.mockReset().mockResolvedValue([]));
        fixture.listeners.clear();
        resetTodayChatDrafts();
        binding('GetActivitiesInRange').mockResolvedValue([activity()]);
        binding('GetRecentMeetingNotes').mockResolvedValue([]);
        await i18n.changeLanguage('en');
    });

    const openRow = async () => {
        fireEvent.click((await screen.findByText('Drafting the Halcyon studio guide')).closest('button')!);
    };

    it('prepares every moment for chat in one read when the day loads', async () => {
        renderDay({ onStartChat: vi.fn() });
        await waitFor(() =>
            expect(binding('PrepareTodayChatDrafts')).toHaveBeenCalledWith([
                { sourceKind: 'activity', sourceId: 'activity-1', dayStartMs: new Date(2026, 9, 19).getTime() },
            ]),
        );
    });

    it('opens Chat about this in place under the moment and sends nothing until Ask', async () => {
        const onStartChat = vi.fn();
        renderDay({ onStartChat });
        await openRow();
        fireEvent.click(screen.getByRole('button', { name: 'Chat about this' }));
        const field = screen.getByRole('textbox', { name: 'Ask anything about this' });
        expect(field).toHaveFocus();
        expect(screen.getByRole('button', { name: 'Ask' })).toBeDisabled();
        expect(binding('AskMainChatWithContext')).not.toHaveBeenCalled();
        expect(onStartChat).not.toHaveBeenCalled();
        binding('GetMainChat').mockResolvedValue({ session: { localId: 'main-1' } });
        fireEvent.change(field, { target: { value: 'What did I change?' } });
        fireEvent.keyDown(field, { key: 'Enter' });
        await waitFor(() => expect(binding('AskMainChatWithContext')).toHaveBeenCalledWith('What did I change?', {
            sourceKind: 'activity', sourceId: 'activity-1', dayStartMs: new Date(2026, 9, 19).getTime(),
        }));
        expect(binding('SendPromptWithTodayContext')).not.toHaveBeenCalled();
    });

    it('offers no chat for a private moment, and none where there is no main chat', async () => {
        binding('GetActivitiesInRange').mockResolvedValue([activity({ actionsWithheld: 'private' })]);
        const { unmount } = renderDay({ onStartChat: vi.fn() });
        await openRow();
        expect(screen.queryByRole('button', { name: 'Chat about this' })).not.toBeInTheDocument();
        expect(binding('PrepareTodayChatDrafts')).not.toHaveBeenCalled();
        unmount();

        binding('GetActivitiesInRange').mockResolvedValue([activity()]);
        renderDay();
        await openRow();
        expect(screen.queryByRole('button', { name: 'Chat about this' })).not.toBeInTheDocument();
    });

    it('translates Chat about this into Chinese', async () => {
        await i18n.changeLanguage('zh-CN');
        renderDay({ onStartChat: vi.fn() });
        await openRow();
        expect(screen.getByRole('button', { name: '聊聊这个' })).toBeInTheDocument();
    });
});
