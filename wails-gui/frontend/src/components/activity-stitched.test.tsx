import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { formatTimeFromMs, formatTimeRangeFromMs } from './timeline/useCalendarData';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import i18n from '@/i18n';
import { toast } from 'sonner';
import { resetActivitySessionCache } from './timeline/activity-session-cache';
import { resetTodayChatDrafts } from '@/lib/today-chat-prepared';
import { ActivityDayView } from './activity-day-view';
import * as stitchedModel from './activity-stitched';
import {
    linkReasonText,
    pickUpNotice,
    resetPickUpLater,
    stitchThreads,
    stitchedHeadline,
    stitchedTitles,
    timelineRange,
    stitchedAxisTicks,
    stitchedRows,
    stitchedSubline,
    rowGapLabel,
    gapVisible,
    splitThreadTitle,
    pickUpBody,
    stitchedClock,
    type StitchedThread,
} from './activity-stitched';
import { stitchedCleanupFixture, stitchedFixture } from './activity-stitched.fixture';

beforeEach(() => {
    resetActivitySessionCache();
    resetPickUpLater();
    resetTodayChatDrafts();
    Element.prototype.scrollIntoView = vi.fn();
});

afterEach(() => vi.restoreAllMocks());

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

vi.mock('@/components/entry-content', () => ({ EntryDetail: () => null }));
vi.mock('./activity-meeting-notes', () => ({ MeetingNotesCard: () => null }));
vi.mock('sonner', () => ({ toast: Object.assign(vi.fn(), { error: vi.fn(), success: vi.fn() }) }));

function binding(name: string) {
    if (!fixture.bindings.has(name)) fixture.bindings.set(name, vi.fn().mockResolvedValue([]));
    return fixture.bindings.get(name)!;
}

const DAY = new Date(2026, 9, 19); // a past day
const { activities, threads } = stitchedFixture(DAY);
const t = () => i18n.getFixedT('en', 'activity');

function dayElement(overrides: Partial<Parameters<typeof ActivityDayView>[0]> = {}) {
    return (
        <ActivityDayView
            date={DAY}
            searchQuery=""
            bridgePort={4242}
            dailyData={null}
            automationRuns={[]}
            onOpenRunDetail={vi.fn()}
            section="workedOn"
            {...overrides}
        />
    );
}

function renderDay(overrides: Partial<Parameters<typeof ActivityDayView>[0]> = {}) {
    return render(dayElement(overrides));
}

function lane(id: string) {
    return document.querySelector(`[data-thread-lane="${id}"]`) as HTMLButtonElement;
}

function card(id: string) {
    return document.querySelector(`[data-thread-card="${id}"]`) as HTMLElement;
}

beforeEach(async () => {
    fixture.bindings.forEach((mock) => mock.mockReset().mockResolvedValue([]));
    fixture.listeners.clear();
    vi.mocked(toast.error).mockReset();
    binding('GetActivitiesInRange').mockResolvedValue(activities);
    binding('GetRecentMeetingNotes').mockResolvedValue([]);
    binding('GetDayThreads').mockResolvedValue({ grouped: true, threads });
    binding('ConfirmActivityThreadLink').mockResolvedValue(undefined);
    binding('RenameActivityThread').mockResolvedValue(undefined);
    binding('MoveActivityToThread').mockResolvedValue('th_new');
    vi.spyOn(Date, 'now').mockReturnValue(new Date(2026, 9, 19, 16, 0).getTime());
    vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockReturnValue({ width: 400 } as DOMRect);
    await i18n.changeLanguage('en');
});

describe('Stitched by work: reading the threads', () => {
    it('sorts by active time, longest first', () => {
        expect(stitchThreads(threads, activities, 0, false).map((s) => s.thread.id)).toEqual(['hal', 'mer', 'nw', 'msg']);
    });

    it('runs a live thread on to now only on today', () => {
        const live = activities.map((a) => (a.id === 'a7' ? { ...a, inProgress: true } : a));
        const now = new Date(2026, 9, 19, 13, 5).getTime();
        const mer = (isToday: boolean, at = now) => stitchThreads(threads, live, at, isToday).find((s) => s.thread.id === 'mer')!;
        expect(mer(true).live).toBe(true);
        expect(mer(true).stretches[1].end).toBe(now);
        expect(mer(false).stretches[1].end).toBe(new Date(2026, 9, 19, 12, 55).getTime());
        // Idle past the away rule is not focused work: the bar ends with the work.
        expect(mer(true, new Date(2026, 9, 19, 13, 30).getTime()).stretches[1].end).toBe(new Date(2026, 9, 19, 12, 55).getTime());
    });

    // The walk-through (activity review v1.0.0 P0 #2): a thread begun late
    // yesterday was one bar from 12 AM to 2 PM and "picked back up" at
    // midnight. Go now sends only today's sittings; the row says so.
    it('starts a carried thread at its first stretch today, marked From yesterday', () => {
        const day = new Date(2026, 9, 20).getTime();
        const at = (h: number, m = 0) => new Date(2026, 9, 20, h, m).getTime();
        const harnessMoment = { ...activities[0], id: 'h1', title: 'Isoform · DeepSeek Harness', startMs: new Date(2026, 9, 19, 23, 30).getTime(), endMs: at(14), activeMs: 70 * 60_000 };
        const feedMoment = { ...activities[1], id: 'f1', title: 'GitHub feed', startMs: at(10), endMs: at(10, 30), activeMs: 30 * 60_000 };
        const harness = { ...threads[0], id: 'harness', title: 'Isoform: DeepSeek Harness', startMs: harnessMoment.startMs, endMs: at(14), activityIds: ['h1'], activeMs: 70 * 60_000, timeSpentMs: 70 * 60_000,
            stretches: [
                { start: at(8, 59), end: at(9, 40), activeMs: 41 * 60_000, activityIds: ['h1'], resumePoint: null },
                { start: at(13, 29), end: at(14), activeMs: 29 * 60_000, activityIds: ['h1'], resumePoint: { kind: 'path' as const, label: '/agents/example' } },
            ],
            gaps: [{ start: at(9, 40), end: at(13, 29), minutes: 229, label: 'GitHub feed', threadId: 'feed' }] };
        const feed = { ...threads[2], id: 'feed', title: 'GitHub feed', startMs: at(10), endMs: at(10, 30), activityIds: ['f1'], activeMs: 30 * 60_000, timeSpentMs: 30 * 60_000,
            stretches: [{ start: at(10), end: at(10, 30), activeMs: 30 * 60_000, activityIds: ['f1'], resumePoint: null }], gaps: [] };
        const items = stitchThreads([harness, feed], [harnessMoment, feedMoment], at(16), false, day, new Date(2026, 9, 21).getTime());
        const row = items.find((item) => item.thread.id === 'harness')!;
        expect(row.continuesBefore).toBe(true);
        expect(row.stretches[0].start).toBe(at(8, 59));
        expect(items.every((item) => item.stretches.every((stretch) => stretch.start !== day))).toBe(true);
        expect(timelineRange(items).start).toBe(at(8, 59));
        expect(stitchedHeadline(t(), items)).toBe('2 pieces of work, 1 picked back up after an interruption');
        // The same thread with only one sitting today was never interrupted today.
        const oneSitting = { ...harness, stretches: [harness.stretches[1]], gaps: [] };
        expect(stitchedHeadline(t(), stitchThreads([oneSitting, feed], [harnessMoment, feedMoment], at(16), false, day, new Date(2026, 9, 21).getTime()))).toBe('2 pieces of work');
    });

    it('says the thing you were in at Left off at, never a duration or a raw path', () => {
        expect(stitchedModel.pointWhat('20 s')).toBe('');
        expect(stitchedModel.pointWhat('1h 6m')).toBe('');
        expect(stitchedModel.pointWhat('/agents/example')).toBe('');
        expect(stitchedModel.pointWhat('/agents/example', 'path')).toBe('');
        expect(stitchedModel.pointWhat('~/isoform-launch/film-v1.0.0/storyboard.key', 'path')).toBe('storyboard.key');
        expect(stitchedModel.pointWhat('Meridian memo, section 2', 'path')).toBe('Meridian memo, section 2');
        expect(stitchedModel.pointWhat('PR #212')).toBe('PR #212');
        expect(stitchedModel.pointWhat('Launch film storyboard')).toBe('Launch film storyboard');
    });

    it('clips the same midnight-spanning thread in both days and keeps every range in agreement', () => {
        const start = new Date(2026, 9, 19, 23).getTime();
        const edge = new Date(2026, 9, 20).getTime();
        const end = new Date(2026, 9, 20, 2).getTime();
        const moment = { ...activities[0], startMs: start, endMs: end, inProgress: true };
        const thread = { ...threads[0], startMs: start, endMs: end, activityIds: [moment.id],
            stretches: [{ start, end, activeMs: end - start, activityIds: [moment.id], resumePoint: null }], gaps: [] };
        const yesterday = stitchThreads([thread], [moment], end, false, new Date(2026, 9, 19).getTime(), edge);
        const today = stitchThreads([thread], [moment], end, true, edge, new Date(2026, 9, 21).getTime());
        expect(yesterday[0].thread.id).toBe(today[0].thread.id);
        expect(yesterday[0].stretches[0]).toMatchObject({ start, end: edge });
        expect(today[0].stretches[0]).toMatchObject({ start: edge, end });
        expect(yesterday[0]).toMatchObject({ live: false, continuesBefore: false, continuesAfter: true });
        expect(today[0]).toMatchObject({ live: true, continuesBefore: true, continuesAfter: false });
        expect(timelineRange(yesterday)).toMatchObject({ start, end: edge });
        expect(stitchedSubline(t(), yesterday)).toContain(`From ${stitchedClock(start)} to ${stitchedClock(edge)} · `);
        const ticks = stitchedAxisTicks(start, edge, 760, (ms) => stitchedClock(ms));
        expect(ticks.every((tick) => tick.at >= start && tick.at <= edge)).toBe(true);
    });

    it.each(['en', 'zh-CN'])('puts hour labels on the hour inside the window and drops colliding ones at 760px in %s', (language) => {
        const start = new Date(2026, 9, 19, 10).getTime();
        const end = new Date(2026, 9, 20).getTime();
        // 760px viewport minus the two label columns and panel padding.
        const width = 760 - 200 - 170 - 40;
        const ticks = stitchedAxisTicks(start, end, width, (ms) => stitchedClock(ms, language));
        expect(ticks.length).toBeGreaterThan(1);
        ticks.forEach((tick, i) => {
            expect(tick.left).toBeGreaterThanOrEqual(0);
            expect(tick.left + tick.width).toBeLessThanOrEqual(width);
            if (i) expect(tick.left).toBeGreaterThanOrEqual(ticks[i - 1].left + ticks[i - 1].width + 8);
            expect(new Date(tick.at).getMinutes()).toBe(0);
            expect(tick.at).toBeGreaterThanOrEqual(start);
            expect(tick.at).toBeLessThanOrEqual(end);
        });
    });

    it('renders both sides of midnight with continuation marks and one shared day range', async () => {
        const start = new Date(2026, 9, 19, 23).getTime();
        const end = new Date(2026, 9, 20, 2).getTime();
        const moment = { ...activities[0], startMs: start, endMs: end, inProgress: true };
        const thread = { ...threads[0], startMs: start, endMs: end, activityIds: [moment.id], activeMs: end - start,
            stretches: [{ start, end, activeMs: end - start, activityIds: [moment.id], resumePoint: null }], gaps: [] };
        // Each Go reply already owns its source time for the requested day.
        const allocated = (day: number) => day < new Date(2026, 9, 20).getTime() ? 3_600_000 : 7_200_000;
        binding('GetActivitiesInRange').mockImplementation(async (day: number) => [{ ...moment, activeMs: allocated(day) }]);
        binding('GetDayThreads').mockImplementation(async (day: number) => ({ grouped: true, threads: [{ ...thread,
            activeMs: allocated(day), timeSpentMs: allocated(day), stretches: thread.stretches.map((s) => ({ ...s, activeMs: allocated(day) })) }] }));
        const yesterday = renderDay();
        await screen.findByRole('heading', { name: '11:00 PM–12:00 AM' });
        expect(screen.getByText('From 11:00 PM to 12:00 AM · 1h active')).toBeInTheDocument();
        expect(screen.getByTestId('stitched-continues-after')).toBeInTheDocument();
        expect(screen.getByLabelText('Continues into the next day')).toBeInTheDocument();
        expect(screen.queryByText('In progress')).toBeNull();
        const segments = screen.getAllByTestId('stitched-stretch');
        expect(segments[0].getAttribute('title')).toContain('11:00 PM–12:00 AM');
        yesterday.unmount();
        renderDay({ date: new Date(2026, 9, 20) });
        await screen.findByRole('heading', { name: '12:00–2:00 AM' });
        expect(screen.getByText('From 12:00 AM to 2:00 AM · 2h active')).toBeInTheDocument();
        expect(screen.getByTestId('stitched-continues-before')).toHaveTextContent('From yesterday');
        expect(within(lane('hal')).getByText('From yesterday')).toBeInTheDocument();
        expect(screen.queryByText('In progress')).toBeNull();
    });

    // Activity review v1.0.4: Left off at is the thing you were in or the
    // time alone, never the moment's summary.
    it('says only the time in Left off at when the stretch has no resume point', async () => {
        binding('GetActivitiesInRange').mockResolvedValue(activities.map((moment) => moment.id === 'a4'
            ? { ...moment, description: 'You answered three emails and archived the rest.', latestChange: 'Archived the rest' } : moment));
        renderDay();
        await screen.findByTestId('stitched-axis');
        const leftOff = within(card('msg')).getByText('Left off at').nextSibling;
        expect(leftOff).toHaveTextContent(/^11:55\sAM · Mail$/);
        expect(within(card('msg')).queryByText(/answered three emails|Archived the rest|Email triage/)).toBeNull();
    });

    it('does not offer thread controls for an ungrouped overnight moment', async () => {
        const startMs = new Date(2026, 9, 19, 23).getTime();
        const endMs = new Date(2026, 9, 20, 2).getTime();
        binding('GetActivitiesInRange').mockResolvedValue([{ ...activities[0], startMs, endMs }]);
        binding('GetDayThreads').mockResolvedValue({ grouped: false, threads: [] });
        renderDay();
        await screen.findByTestId('stitched-axis');
        expect(document.querySelector('[data-thread-card]')).toBeNull();
        expect(screen.queryByRole('button', { name: 'Looks right' })).toBeNull();
    });

    it('gives a moment Go has not grouped yet a row of its own', () => {
        const fresh = { ...activities[6], id: 'a8', title: 'Sketching the Q4 plan', startMs: activities[6].endMs, endMs: activities[6].endMs + 600_000 };
        const items = stitchThreads(threads, [...activities, fresh], 0, false);
        const lone = items.find((s) => s.thread.id === 'moment:a8')!;
        expect(lone.thread.title).toBe('Sketching the Q4 plan');
        expect(lone.stretches).toHaveLength(1);
        expect(lone.interruption).toBe(false);
        expect(items).toHaveLength(5);
    });

    it('draws a thread from an older computer as one stretch', () => {
        const old = { ...threads[0], stretches: undefined, gaps: undefined } as unknown as typeof threads[0];
        const [item] = stitchThreads([old], activities, 0, false);
        expect(item.stretches).toEqual([{ start: old.startMs, end: old.endMs, activeMs: old.timeSpentMs, activityIds: old.activityIds, resumePoint: null }]);
        expect(item.gaps).toEqual([]);
    });

    it('uses the exact data range without extending an idle day to now', () => {
        const items = stitchThreads(threads, activities, 0, false);
        const range = timelineRange(items);
        expect(new Date(range.start).getHours()).toBe(9);
        expect(range.end).toBe(Math.max(...items.flatMap((item) => item.stretches.map((stretch) => stretch.end))));
        expect(range.step).toBe(3);
        const today = timelineRange(items);
        expect(today.end).toBe(range.end);
        expect(today.step).toBe(3);
    });

    // Activity review v1.0.1 #1: the window is 9:12 AM–12:55 PM, so the
    // ticks are the whole hours inside it, on the bars' own scale.
    it('labels each whole hour inside the window with a fixed 170px right column in both languages', async () => {
        renderDay();
        await screen.findByTestId('stitched-axis');
        const axis = screen.getByTestId('stitched-axis');
        const texts = () => [...axis.children].map((c) => c.textContent);
        expect(texts()).toEqual(['10 AM', '11', '12 PM']);
        const lefts = [...axis.children].map((c) => parseFloat((c as HTMLElement).style.left));
        expect(lefts[1]).toBeGreaterThan(lefts[0]);
        expect(lefts[2]).toBeGreaterThan(lefts[1]);
        expect(axis.nextElementSibling).toHaveClass('w-[170px]', 'shrink-0');
        // Activity review v1.0.3 #3: hour lines only at the labeled hours.
        const hourLines = [...lane('mer').querySelectorAll('[data-testid="stitched-hour-line"]')].map((el) => (el as HTMLElement).style.left);
        expect(hourLines).toEqual(['21.525%', '48.43%', '75.336%']);
        await act(async () => { await i18n.changeLanguage('zh-CN'); });
        expect(texts()).toEqual(['10:00', '11:00', '12:00']);
    });

    it('bounds disambiguated titles and puts only the unused words in the meta line', async () => {
        const duplicate = threads.map((thread, i) => ({ ...thread, title: i === 0 ? 'Isoform: brand style guide and typography' : i === 1 ? 'Isoform: morning brief delivery review' : thread.title }));
        binding('GetDayThreads').mockResolvedValue({ grouped: true, threads: duplicate });
        renderDay();
        await screen.findAllByText('Isoform · brand style guide');
        const row = lane(duplicate[0].id);
        expect(within(row).getByText('and typography')).toBeInTheDocument();
        expect(row.textContent?.match(/brand style guide/g)).toHaveLength(1);
        const titles = stitchedTitles(duplicate, t());
        for (const title of titles.values()) expect(Array.from(title).length).toBeLessThanOrEqual(32);
    });

    it.each(['en', 'zh-CN'])('keeps seven resumes short in %s', async (language) => {
        await i18n.changeLanguage(language);
        const item = stitchThreads(threads, activities, 0, false)[0];
        const gaps = Array.from({ length: 7 }, (_, i) => ({ start: i * 4_000_000, end: i * 4_000_000 + 66 * 60_000, minutes: 66, threadId: item.thread.id, label: 'away' }));
        expect(rowGapLabel(i18n.getFixedT(language, 'activity'), { ...item, gaps }, [])).toBe(language === 'en' ? 'Away 7h 42m in all' : '共离开 7 小时 42 分钟');
    });

    it('disambiguates repeated leading thread titles', async () => {
        const duplicate = threads.map((thread, i) => ({ ...thread, title: i === 0 ? 'Isoform: brand style' : i === 1 ? 'Isoform: morning brief' : thread.title }));
        binding('GetDayThreads').mockResolvedValue({ grouped: true, threads: duplicate });
        renderDay();
        await screen.findAllByText('Isoform · brand style');
        expect(within(lane(duplicate[1].id)).getByText('Isoform · morning brief')).toBeInTheDocument();
    });

    it.each([false, true])('keeps a unique colon title in the full thread card (user titled: %s)', async (userTitled) => {
        const unique = threads.map((thread, i) => i === 0 ? { ...thread, title: 'Release: staging checklist', userTitled } : thread);
        binding('GetDayThreads').mockResolvedValue({ grouped: true, threads: unique });
        renderDay();
        await waitFor(() => expect(card(unique[0].id)).toBeInTheDocument());
        expect(within(card(unique[0].id)).getByText('Release: staging checklist')).toBeInTheDocument();
    });

    // Activity review v1.0.3 #1: an interruption is 10 minutes or more.
    it('never labels a gap shorter than ten minutes', () => {
        const item = stitchThreads(threads, activities, 0, false)[0];
        const short = { ...item, gaps: [{ ...item.gaps[0], start: 0, end: 10 * 60_000 - 1 }] };
        expect(rowGapLabel(t(), short, [short])).toBe('');
        expect(rowGapLabel(t(), { ...short, gaps: [{ ...short.gaps[0], end: 10 * 60_000 }] }, [])).toContain('10m');
    });

    // Activity review v1.0.3 #2: an away gap names the work that filled it.
    it('names the drawn work that filled an away gap, and says Away alone only when nothing did', () => {
        const items = stitchThreads(threads, activities, 0, false);
        const msg = items.find((s) => s.thread.id === 'msg')!;
        expect(rowGapLabel(t(), msg, items)).toBe('Away 25m · Writing Halcyon launch memo');
        expect(rowGapLabel(t(), msg, [msg])).toBe('Away 25m');
    });

    it('explains a link in words without deciding it', () => {
        // Thursday A1: "Picked back up" only after an interruption (10 min or more).
        expect(linkReasonText(t(), threads[0].linkReason, 'en')).toBe('Linked because: same PR and files. Picked back up 28 min later.');
        expect(linkReasonText(t(), threads[1].linkReason, 'en')).toBe('Linked because: the same memo document.');
        expect(linkReasonText(t(), { kind: 'user', keys: [], text: '' }, 'en')).toBe('Linked because: you put them together.');
        expect(linkReasonText(t(), { kind: 'model', keys: [], text: '' }, 'en')).toBe('Linked because: they continue the same work.');
        expect(linkReasonText(t(), { kind: 'rule', keys: ['person:Ana', 'unknown:x'], text: '' }, 'en')).toBe('Linked because: same person.');
        expect(linkReasonText(t(), threads[1].linkReason, 'zh-CN')).not.toMatch(/Isoform|分钟后接着做/);
    });

    it('counts pieces of work in the headline', () => {
        const items = stitchThreads(threads, activities, 0, false);
        expect(stitchedHeadline(t(), items)).toBe('4 pieces of work, 2 picked back up after an interruption');
        expect(stitchedHeadline(t(), items.filter((s) => s.thread.id !== 'mer').concat({ ...items[1], stretches: [items[1].stretches[0]] }))).toBe(
            '4 pieces of work, 2 picked back up after an interruption',
        );
        expect(stitchedHeadline(t(), [])).toBeNull();
    });

    it('translates the carry-over label in both supported languages', () => {
        expect(i18n.getFixedT('en', 'activity')('stitched.continuesBefore')).toBe('From yesterday');
        expect(i18n.getFixedT('zh-CN', 'activity')('stitched.continuesBefore')).toBe('从昨天继续');
    });

    it('does not count a boundary or overlapping stretches as a pick-up', () => {
        const item = stitchThreads(threads, activities, 0, false)[0];
        const first = item.stretches[0];
        const second = { ...item.stretches[1], start: first.end };
        const boundary = { ...item, stretches: [first, second], gaps: [{ ...item.gaps[0], start: first.end, end: first.end }] };
        expect(stitchedHeadline(t(), [boundary])).toBe('1 piece of work');
        expect(stitchedHeadline(t(), [{ ...boundary, gaps: [] }])).toBe('1 piece of work');
        expect(stitchedHeadline(t(), [item])).toBe('1 piece of work, 1 picked back up after an interruption');
    });

    it('offers recent substantial work that is not running', () => {
        const items = stitchThreads(threads, activities, 0, false);
        const notice = pickUpNotice(items, '2026-10-19', Date.now())!;
        expect(notice.threadId).toBe('mer');
        expect(notice.label).toBe('Halcyon launch memo, section 3');
        expect(notice.momentId).toBe('a7');
        const running = stitchThreads(threads, activities.map((a) => (a.id === 'a7' ? { ...a, inProgress: true } : a)), 0, false);
        expect(pickUpNotice(running, '2026-10-19', Date.now())).toBeNull();
    });
});

describe('Stitched by work: the page', () => {
    it('opens on Stitched by work with one row per thread and the headline', async () => {
        renderDay();
        expect(await screen.findByText('4 pieces of work, 2 picked back up after an interruption')).toBeInTheDocument();
        expect(screen.getByRole('tab', { name: 'Stitched by work' })).toHaveAttribute('aria-selected', 'true');
        expect([...document.querySelectorAll('[data-thread-lane]')].map((el) => el.getAttribute('data-thread-lane'))).toEqual(['mer', 'hal', 'nw', 'msg']);
        expect(screen.getByRole('heading', { name: '9:12 AM–12:55 PM' })).toBeInTheDocument();
    });

    it('draws a thread with two stretches as one row joined by a labelled connector', async () => {
        renderDay();
        await screen.findByText('Work threads');
        const hal = lane('hal');
        expect(hal.querySelectorAll('[class*="rounded-md"][title]')).toHaveLength(2);
        expect(within(hal).getByText('Away 28m · Northwind call')).toBeInTheDocument();
        // An 8-minute hop is a plain gap: no dashed line, no Away label.
        expect(within(lane('mer')).queryAllByTestId('stitched-gap-link')).toHaveLength(0);
        expect(within(lane('mer')).queryByTestId('stitched-gap-label')).toBeNull();
        expect(within(card('mer')).queryByText(/Away/)).toBeNull();
        expect(within(card('mer')).queryByText('Picked back up')).toBeNull();
        // Interruptions sit on their own lighter rows, without connectors.
        expect(lane('msg')).toHaveAttribute('data-interruption', 'true');
        expect(lane('msg').querySelectorAll('.bg-faint')).toHaveLength(2);
        expect(within(lane('msg')).getByTestId('stitched-gap-label')).toHaveTextContent('Away 25m · Writing Halcyon launch memo');
        expect(within(card('msg')).getByText('Away · Writing Halcyon launch memo')).toBeInTheDocument();
    });

    it('shows active time on rows and preserves times in detail cards', async () => {
        renderDay();
        await screen.findByText('Work threads');
        expect(within(lane('hal')).getByText('2h active')).toBeInTheDocument();
        expect(within(lane('nw')).getByText('28m active')).toBeInTheDocument();
        expect(within(lane('msg')).getByText('23m active')).toBeInTheDocument();
        expect(within(card('hal')).getByText('9:12–11:40 AM')).toBeInTheDocument();
        expect(within(card('hal')).getByText('2h active across 2 stretches · away 28m · VS Code, GitHub')).toBeInTheDocument();
    });

    // The walk-through (P1 #5): Your day said 3h 3m, this view 1h 46m.
    it('shows the day one active time under the headline, and the rows time while searching', async () => {
        const dailyData = { totalActiveMs: 183 * 60_000, appBreakdown: [], hourlyActive: [] } as unknown as Parameters<typeof ActivityDayView>[0]['dailyData'];
        const view = renderDay({ dailyData });
        expect(await screen.findByText(/· 3h 3m active$/)).toBeInTheDocument();
        view.unmount();
        renderDay({ dailyData, searchQuery: 'erp' });
        await screen.findByText('Work threads');
        expect(screen.queryByText(/· 3h 3m active$/)).toBeNull();
    });

    // Activity review v1.0.1: idle inside one moment splits the bar but is no
    // interruption: no link, no Away label, no count, no Left off at.
    it('draws idle inside the same work as a plain gap that is never a pick-up', async () => {
        const updated = threads.map((thread) => thread.id === 'mer'
            ? { ...thread, gaps: thread.gaps.map((gap) => ({ ...gap, label: 'away', threadId: '', idle: true })) } : thread);
        binding('GetDayThreads').mockResolvedValue({ grouped: true, threads: updated });
        renderDay();
        await screen.findByText('Work threads');
        expect(screen.getByText('4 pieces of work, 2 picked back up after an interruption')).toBeInTheDocument();
        expect(within(lane('mer')).queryAllByTestId('stitched-gap-link')).toHaveLength(0);
        expect(within(lane('mer')).queryByTestId('stitched-gap-label')).toBeNull();
        const mer = card('mer');
        expect(within(mer).queryByText('Left off at')).toBeNull();
        expect(within(mer).queryByText('Picked back up')).toBeNull();
        expect(within(mer).queryByText(/Away/)).toBeNull();
        expect(within(mer).getByText(/^47m active · /)).toBeInTheDocument();
        expect(mer.querySelectorAll('[data-work-stretch]')).toHaveLength(2);
    });

    it('shows the time alone at Left off at when all Isoform has is a duration or a path', async () => {
        const updated = threads.map((thread) => thread.id === 'hal' ? {
            ...thread, stretches: thread.stretches.map((stretch) => ({ ...stretch, resumePoint: { kind: 'path', label: '/agents/example' } })),
        } : thread);
        const moments = activities.map((moment) => updated.find((thread) => thread.id === 'hal')!.activityIds.includes(moment.id)
            ? { ...moment, latestChange: '20 s', description: '', title: '/agents/example' } : moment);
        binding('GetActivitiesInRange').mockResolvedValue(moments);
        binding('GetDayThreads').mockResolvedValue({ grouped: true, threads: updated });
        renderDay();
        await screen.findByText('Work threads');
        const hal = card('hal');
        expect(within(hal).queryByText(/20 s/)).toBeNull();
        expect(within(hal).queryByText(/\/agents\/example/)).toBeNull();
        expect(within(hal).getByText('Left off at').nextSibling).toHaveTextContent(/^10:02\sAM · VS Code$/);
    });

    it('shows the source active duration in the thread bar when its span is longer', async () => {
        const updated = threads.map((thread) => thread.id === 'hal' ? {
            ...thread, stretches: thread.stretches.map((stretch, index) => index === 0 ? { ...stretch, activeMs: 10 * 60_000 } : stretch),
        } : thread);
        binding('GetDayThreads').mockResolvedValue({ grouped: true, threads: updated });
        renderDay();
        await screen.findByText('Work threads');
        expect(within(card('hal')).getByText('9:12 AM · 10m')).toBeInTheDocument();
        expect(within(card('hal')).queryByText('9:12 AM · 50m')).toBeNull();
    });

    it('does not show Picked back up for adjacent stretches without an interruption', async () => {
        const updated = threads.map((thread) => thread.id === 'hal' ? {
            ...thread, stretches: [thread.stretches[0], { ...thread.stretches[1], start: thread.stretches[0].end }], gaps: [],
        } : thread);
        binding('GetDayThreads').mockResolvedValue({ grouped: true, threads: updated });
        renderDay();
        await screen.findByText('Work threads');
        expect(within(card('hal')).queryByText('Picked back up')).toBeNull();
        expect(within(card('msg')).getByText('Picked back up')).toBeInTheDocument();
    });

    it('selecting a row dims others, highlights its surfaces and draws its stretches in ink', async () => {
        renderDay();
        await screen.findByText('Work threads');
        fireEvent.click(lane('hal'));
        expect(lane('hal')).toHaveAttribute('aria-pressed', 'true');
        expect(lane('hal').className).not.toContain('opacity-[0.55]');
        for (const id of ['mer', 'nw', 'msg']) expect(lane(id).className).toContain('opacity-[0.55]');
        // Design system: selected is color-surface-active, not an ink fill or border.
        expect(lane('hal')).toHaveClass('bg-surface-active');
        expect(card('hal')).toHaveClass('bg-surface-active', 'border-border');
        expect(card('mer')).toHaveClass('bg-panel');
        expect(card('mer')).not.toHaveClass('bg-surface-active');
        expect(lane('hal')).not.toHaveClass('bg-ink', 'border-ink');
        expect(card('hal')).not.toHaveClass('bg-ink', 'border-ink');
        expect(lane('hal').querySelectorAll('.bg-ink')).toHaveLength(2);
        fireEvent.click(lane('hal'));
        expect(lane('mer').className).not.toContain('opacity-[0.55]');
        expect(card('hal')).not.toHaveClass('bg-surface-active');
    });

    it('marks the selected view with the selected fill', async () => {
        renderDay();
        const stitchedTab = await screen.findByRole('tab', { name: 'Stitched by work' });
        expect(stitchedTab).toHaveClass('bg-surface-active');
        expect(stitchedTab.className).not.toContain('shadow');
        expect(screen.getByRole('tab', { name: 'As recorded' })).not.toHaveClass('bg-surface-active');
    });

    it('shows where work stopped and where it was picked back up, and why it was linked', async () => {
        renderDay();
        await screen.findByText('Work threads');
        const hal = within(card('hal'));
        expect(hal.getByText('10:02 AM · PR #212, invoice_mapping.go')).toBeInTheDocument();
        // Picked back up names the thing you were in (v1.0.1 #3).
        expect(hal.getByText('10:30 AM · PR #212')).toBeInTheDocument();
        expect(hal.getByText('Linked because: same PR and files. Picked back up 28 min later.')).toBeInTheDocument();
        // Interruptions have no card of their own.
        expect(card('nw')).toBeNull();
    });

    it('Looks right confirms the link and says so', async () => {
        renderDay();
        await screen.findByText('Work threads');
        fireEvent.click(within(card('hal')).getByRole('button', { name: 'Looks right' }));
        await waitFor(() => expect(binding('ConfirmActivityThreadLink')).toHaveBeenCalledWith('hal'));
        expect(await within(card('hal')).findByText('You confirmed this link.')).toBeInTheDocument();
    });

    it('a confirmed thread shows it is confirmed', async () => {
        binding('GetDayThreads').mockResolvedValue({ grouped: true, threads: threads.map((th) => (th.id === 'mer' ? { ...th, reviewed: true } : th)) });
        renderDay();
        await screen.findByText('Work threads');
        expect(within(card('mer')).getByText('You confirmed this link.')).toBeInTheDocument();
        expect(within(card('hal')).getByRole('button', { name: 'Looks right' })).toBeInTheDocument();
    });

    it('says plainly when a correction could not be saved', async () => {
        binding('ConfirmActivityThreadLink').mockRejectedValue(new Error('store unavailable'));
        renderDay();
        await screen.findByText('Work threads');
        fireEvent.click(within(card('hal')).getByRole('button', { name: 'Looks right' }));
        expect(await within(card('hal')).findByRole('alert')).toHaveTextContent("Couldn't save your correction. Try again.");
        expect(within(card('hal')).getByRole('button', { name: 'Try again' })).toBeInTheDocument();
        expect(toast.error).not.toHaveBeenCalled();
        expect(within(card('hal')).getByRole('button', { name: 'Looks right' })).toBeInTheDocument();
    });

    it('Not right keeps rename', async () => {
        renderDay();
        await screen.findByText('Work threads');
        fireEvent.pointerDown(within(card('hal')).getByRole('button', { name: 'Not right' }), { button: 0, pointerType: 'mouse' });
        fireEvent.click(await screen.findByText('Rename thread'));
        const input = screen.getByRole('textbox', { name: 'Thread title' });
        fireEvent.change(input, { target: { value: 'Halcyon review' } });
        fireEvent.click(screen.getByRole('button', { name: 'Save' }));
        await waitFor(() => expect(binding('RenameActivityThread')).toHaveBeenCalledWith('hal', 'Halcyon review'));
    });

    it('Not right moves a stretch to its own thread, moment by moment', async () => {
        // The second stretch holds two moments: the first makes the new
        // thread, the second follows it there.
        binding('GetDayThreads').mockResolvedValue({
            grouped: true,
            threads: threads.map((th) =>
                th.id === 'hal' ? { ...th, stretches: [th.stretches[0], { ...th.stretches[1], activityIds: ['a3', 'a4'] }] } : th,
            ),
        });
        binding('MoveActivityToThread').mockResolvedValueOnce('th_new').mockResolvedValueOnce('th_new');
        renderDay();
        await screen.findByText('Work threads');
        fireEvent.pointerDown(within(card('hal')).getByRole('button', { name: 'Not right' }), { button: 0, pointerType: 'mouse' });
        const sub = await screen.findByText('Move the 10:30 AM stretch');
        fireEvent.pointerMove(sub);
        fireEvent.keyDown(sub, { key: 'ArrowRight' });
        fireEvent.click(await screen.findByText('Its own thread'));
        await waitFor(() => expect(binding('MoveActivityToThread')).toHaveBeenCalledTimes(2));
        expect(binding('MoveActivityToThread').mock.calls).toEqual([['a3', ''], ['a4', 'th_new']]);
    });

    it('draws no connector across a gap with no length', async () => {
        binding('GetDayThreads').mockResolvedValue({
            grouped: true,
            threads: threads.map((th) => (th.id === 'mer' ? { ...th, gaps: [{ ...th.gaps[0], end: th.gaps[0].start, minutes: 0 }] } : th)),
        });
        renderDay();
        await screen.findByText('Work threads');
        expect(within(lane('mer')).queryByTestId('stitched-gap-label')).toBeNull();
        expect(within(lane('hal')).getByTestId('stitched-gap-label')).toBeInTheDocument();
    });

    it('moves between the two views with the arrow keys', async () => {
        renderDay();
        const stitchedTab = await screen.findByRole('tab', { name: 'Stitched by work' });
        expect(stitchedTab).toHaveAttribute('tabindex', '0');
        fireEvent.keyDown(stitchedTab, { key: 'ArrowRight' });
        const recordedTab = screen.getByRole('tab', { name: 'As recorded' });
        expect(recordedTab).toHaveAttribute('aria-selected', 'true');
        expect(recordedTab).toHaveFocus();
        expect(screen.getByRole('tabpanel')).toHaveAttribute('aria-labelledby', recordedTab.id);
    });

    it('As recorded is the list the day shows today', async () => {
        const onWorkedOnViewChange = vi.fn();
        renderDay({ onWorkedOnViewChange });
        await screen.findByText('Work threads');
        fireEvent.click(screen.getByRole('tab', { name: 'As recorded' }));
        expect(onWorkedOnViewChange).toHaveBeenCalledWith('recorded');
        expect(screen.queryByText('Work threads')).toBeNull();
        // Threads with two or more moments stay one row; the rest are moments.
        expect(screen.getAllByText('Reviewing ERP connector v0.3').length).toBe(1);
        expect(screen.getByText('Northwind call')).toBeInTheDocument();
        expect(screen.getByText('9:12–11:40 AM · 2h active')).toBeInTheDocument();
        expect(screen.getByText('Everything Isoform recorded, newest first.')).toBeInTheDocument();
        // Activity review v1.0.6 AR40: newest first by start, and every row's
        // second line is its time and active time, never a summary.
        const starts = [...document.querySelectorAll('li > button > time')].map((el) => el.textContent?.replace(/\s/g, ' '));
        expect(starts).toEqual(['12:00 PM', '11:40 AM', '10:02 AM', '9:12 AM']);
        expect(screen.getByText('10:02–10:30 AM · 28m active')).toBeInTheDocument();
        expect(screen.queryByText(/You joined the Northwind call/)).toBeNull();
    });

    it('filters the threads by the search box', async () => {
        renderDay({ searchQuery: 'launch memo' });
        await screen.findByText('Work threads');
        expect([...document.querySelectorAll('[data-thread-lane]')].map((el) => el.getAttribute('data-thread-lane'))).toEqual(['mer']);
        expect(screen.getByRole('heading', { name: '12:00–12:55 PM' })).toBeInTheDocument();
        expect(screen.getByText('From 12:00 PM to 12:55 PM · 47m active')).toBeInTheDocument();
    });

    it('says when nothing matches the search', async () => {
        renderDay({ searchQuery: 'zebra' });
        expect(await screen.findByText('Nothing matches "zebra" on this day.')).toBeInTheDocument();
    });
});

describe('Stitched by work: Pick up where you left off', () => {
    // Isoform noticed left Activity (A4): the pick-up lives on Stitched only.
    it('sits on top of Stitched, and Your day shows no pick-up', async () => {
        renderDay();
        const prompt = await screen.findByTestId('stitched-pick-up');
        expect(within(prompt).getByText('Pick up where you left off: Writing Halcyon launch memo.')).toBeInTheDocument();
        expect(within(prompt).getByText('You stepped away at 12:55 PM, after 47 min.')).toBeInTheDocument();
        cleanup();
        renderDay({ section: 'yourDay' });
        expect(await screen.findByTestId('activity-feed')).toBeInTheDocument();
        expect(screen.queryByTestId('noticed-pick-up')).toBeNull();
        expect(screen.queryByTestId('stitched-pick-up')).toBeNull();
        expect(binding('GetDayNoticed')).not.toHaveBeenCalled();
    });

    it('Show where you left off focuses the thread and sends nothing', async () => {
        const onSectionChange = vi.fn();
        const onWorkedOnViewChange = vi.fn();
        const onStartChat = vi.fn();
        renderDay({ onSectionChange, onWorkedOnViewChange, onStartChat, workedOnView: 'stitched' });
        const prompt = await screen.findByTestId('stitched-pick-up');
        fireEvent.click(within(prompt).getByRole('button', { name: 'Show where you left off' }));
        expect(onSectionChange).toHaveBeenCalledWith('workedOn');
        expect(onWorkedOnViewChange).toHaveBeenCalledWith('stitched');
        expect(onStartChat).not.toHaveBeenCalled();
        const sent = [...fixture.bindings.keys()].filter((name) => /^Send|Post|Notify|Push/.test(name));
        expect(sent).toEqual([]);
    });

    it('selects the thread when shown from the Stitched card', async () => {
        renderDay();
        const prompt = await screen.findByTestId('stitched-pick-up');
        fireEvent.click(within(prompt).getByRole('button', { name: 'Show where you left off' }));
        expect(lane('mer')).toHaveAttribute('aria-pressed', 'true');
        await waitFor(() => expect(Element.prototype.scrollIntoView).toHaveBeenCalled());
    });

    it('Later hides it for the session', async () => {
        renderDay();
        const prompt = await screen.findByTestId('stitched-pick-up');
        fireEvent.click(within(prompt).getByRole('button', { name: 'Not now' }));
        expect(screen.queryByTestId('stitched-pick-up')).toBeNull();
        cleanup();
        renderDay();
        await screen.findByText('Work threads');
        expect(screen.queryByTestId('stitched-pick-up')).toBeNull();
    });

    it('stays off the Stitched view while the search hides its thread', async () => {
        renderDay({ searchQuery: 'erp' });
        await screen.findByText('Work threads');
        expect(screen.queryByTestId('stitched-pick-up')).toBeNull();
    });

    it('does not show after the four-hour window', async () => {
        vi.spyOn(Date, 'now').mockReturnValue(new Date(2026, 9, 19, 20, 0).getTime());
        renderDay();
        await screen.findByText('Work threads');
        expect(screen.queryByTestId('stitched-pick-up')).toBeNull();
    });
});

describe('Stitched by work: busy and failure states', () => {
    it('shows grey rows while the day is read', async () => {
        binding('GetDayThreads').mockReturnValue(new Promise(() => {}));
        renderDay();
        expect(await screen.findByTestId('stitched-loading')).toHaveAttribute('aria-busy', 'true');
    });

    it('a failed read reads as a failure with Try again, never as an empty day', async () => {
        binding('GetDayThreads').mockRejectedValue(new Error('temporary read failure'));
        renderDay();
        expect(await screen.findByText("Couldn't load what you worked on.")).toBeInTheDocument();
        expect(screen.queryByText('Nothing here for this day.')).toBeNull();
        binding('GetDayThreads').mockResolvedValue({ grouped: true, threads });
        fireEvent.click(screen.getByRole('button', { name: 'Try again' }));
        expect(await screen.findByText('Work threads')).toBeInTheDocument();
    });

    it('As recorded also says the read failed instead of showing an empty day', async () => {
        binding('GetActivitiesInRange').mockRejectedValue(new Error('temporary read failure'));
        renderDay({ workedOnView: 'recorded' });
        expect(await screen.findByText("Couldn't load what you worked on.")).toBeInTheDocument();
        expect(screen.queryByText('Nothing here for this day.')).toBeNull();
    });

    it('says stretches are still linking and shows the moments it has as rows', async () => {
        binding('GetDayThreads').mockResolvedValue({ grouped: false, threads: [] });
        renderDay();
        expect(await screen.findByText('Linking stretches of work…')).toBeInTheDocument();
        expect(document.querySelectorAll('[data-thread-lane]')).toHaveLength(0);
        expect(document.querySelectorAll('[data-aggregate-lane]')).toHaveLength(1);
        fireEvent.click(screen.getByText('Short moments · 218 min in all'));
        expect(screen.getAllByRole('listitem')).toHaveLength(7);
    });

    it('a failed thread read is never an empty day, even with no moments', async () => {
        binding('GetActivitiesInRange').mockResolvedValue([]);
        binding('GetDayThreads').mockRejectedValue(new Error('temporary read failure'));
        renderDay({ section: undefined });
        expect(await screen.findByText("Couldn't load what you worked on.")).toBeInTheDocument();
        expect(screen.queryByText('Nothing recorded for this day.')).toBeNull();
    });

    it('says an empty day is empty', async () => {
        binding('GetActivitiesInRange').mockResolvedValue([]);
        binding('GetDayThreads').mockResolvedValue({ grouped: true, threads: [] });
        renderDay();
        expect(await screen.findByText('Nothing here for this day.')).toBeInTheDocument();
    });

    it('shows what it is given for an empty day, but never for an empty search', () => {
        const props = {
            items: [], threads: [], status: 'ready' as const, selectedThreadId: null, onSelectThread: vi.fn(), scrollThreadId: null,
            onScrolled: vi.fn(), pickUp: null, onPickUpShow: vi.fn(), onPickUpLater: vi.fn(), onRetry: vi.fn(),
            empty: <p>Right now: Cursor</p>,
        };
        const view = render(<stitchedModel.ActivityStitchedView {...props} query="" />);
        expect(screen.getByText('Right now: Cursor')).toBeInTheDocument();
        expect(screen.queryByText('Nothing here for this day.')).toBeNull();
        view.rerender(<stitchedModel.ActivityStitchedView {...props} query="deck" />);
        expect(screen.queryByText('Right now: Cursor')).toBeNull();
    });

    it('never marks a past-day thread in progress', async () => {
        binding('GetActivitiesInRange').mockResolvedValue(activities.map((a) => (a.id === 'a7' ? { ...a, inProgress: true } : a)));
        renderDay();
        await screen.findByText('Work threads');
        expect(within(card('mer')).queryByText('In progress')).toBeNull();
        expect(screen.queryByTestId('stitched-pick-up')).toBeNull();
    });
});

describe('Stitched by work in Chinese', () => {
    it('translates the switch, the rows and the card', async () => {
        await i18n.changeLanguage('zh-CN');
        renderDay();
        expect(await screen.findByRole('tab', { name: '按工作串起' })).toBeInTheDocument();
        expect(screen.getByRole('tab', { name: '按记录' })).toBeInTheDocument();
        expect(within(lane('hal')).getByText('离开 28 分钟 · Northwind call')).toBeInTheDocument();
        expect(within(card('hal')).getByText('2 段共活跃 2 小时 · 离开 28 分钟 · VS Code, GitHub')).toBeInTheDocument();
        expect(within(card('hal')).getByRole('button', { name: '没错' })).toBeInTheDocument();
        expect(screen.getByText('从上次停下的地方继续：Writing Halcyon launch memo')).toBeInTheDocument();
        expect(screen.getByRole('heading', { name: '9:12–12:55' })).toBeInTheDocument();
        expect(screen.getByText('你在 12:55 离开，此前已活跃 47 分钟。')).toBeInTheDocument();
        expect(document.body).not.toHaveTextContent('stitched.clock.');
        expect(document.body).not.toHaveTextContent('todayBrief.clock.');
        await act(async () => { await i18n.changeLanguage('en'); });
    });
});

describe('Stitched cleanup: row policy and story', () => {
    const dense = stitchedCleanupFixture(DAY);
    const items = () => stitchThreads(dense.threads, dense.activities, Date.now(), false);

    it('keeps five-minute and resumed threads, folds nine short moments, and caps at eight', () => {
        const rows = stitchedRows(items());
        expect(rows.threads.map((item) => item.thread.id)).toEqual(Array.from({ length: 8 }, (_, i) => `work-${i}`));
        expect(rows.otherWork.map((item) => item.thread.id)).toEqual(['work-8', 'work-9']);
        expect(rows.shortMoments).toHaveLength(9);
        expect(stitchedRows(items().filter((item) => item.thread.id === 'work-9')).threads).toHaveLength(1);
        const tinyResumed = { ...items()[0], thread: { ...items()[0].thread, activeMs: 60_000 } };
        expect(stitchedRows([tinyResumed]).threads).toHaveLength(1);
    });

    it('promotes the resume thread ahead of longer work before applying the cap', () => {
        const rows = stitchedRows(items(), 'work-9');
        expect(rows.threads[0].thread.id).toBe('work-9');
        expect(rows.threads.slice(1).map((item) => item.thread.id)).toEqual(Array.from({ length: 7 }, (_, i) => `work-${i}`));
        expect(rows.otherWork.map((item) => item.thread.id)).toEqual(['work-7', 'work-8']);
    });

    it('uses one summary per row, even with three gaps on a two-stretch projection', () => {
        expect(rowGapLabel(t(), items()[0], items())).toBe('Away 4h 29m in all');
        const gap = { ...items()[0].gaps[0], end: items()[0].gaps[0].start + 53 * 60_000, threadId: 'isoform', label: 'ignored, list, of, apps' };
        const interrupter = { ...items()[1], thread: { ...items()[1].thread, id: 'isoform', title: 'Isoform hub: Claude Code sessions' } };
        expect(rowGapLabel(t(), { ...items()[0], gaps: [gap] }, [interrupter])).toBe('Away 53m · Isoform hub');
    });

    it('suppresses links shorter than four actual pixels', () => {
        const gap = { ...items()[0].gaps[0], start: 0, end: 399 };
        expect(gapVisible(gap, 10_000, 100)).toBe(false);
        expect(gapVisible({ ...gap, end: 400 }, 10_000, 100)).toBe(true);
        expect(gapVisible({ ...gap, end: 0 }, 10_000, 100)).toBe(false);
    });

    it('splits only the first colon and uses the shared clock in both locales', () => {
        expect(splitThreadTitle('Isoform hub: Claude Code sessions: review')).toEqual({ title: 'Isoform hub', detail: 'Claude Code sessions: review' });
        expect(splitThreadTitle('OpenClaw cleanup')).toEqual({ title: 'OpenClaw cleanup', detail: '' });
        const at = new Date(2026, 9, 19, 15, 52).getTime();
        expect(stitchedClock(at)).toBe('3:52 PM');
        expect(stitchedClock(at, 'zh-CN')).toBe('15:52');
    });

    it('counts visible thread rows only, and puts the time range and active time in the subline', () => {
        const sample = items().slice(0, 6).map((item, i) => ({ ...item, stretches: i < 4 ? [item.stretches[0], item.stretches[0]] : [item.stretches[0]] }));
        expect(stitchedHeadline(t(), sample)).toBe('6 pieces of work');
        const eightHours = [{ ...items()[0], thread: { ...items()[0].thread, activeMs: 134 * 60_000 }, stretches: [{ ...items()[0].stretches[0], start: 0, end: 8 * 3_600_000 }] }];
        expect(stitchedSubline(t(), eightHours)).toBe(`From ${stitchedClock(0)} to ${stitchedClock(8 * 3_600_000)} · 2h 14m active`);
    });

    it('renders aggregate lists, minimum tick widths, wrapped titles, links and the exact legend', async () => {
        binding('GetActivitiesInRange').mockResolvedValue(dense.activities);
        binding('GetDayThreads').mockResolvedValue({ grouped: true, threads: dense.threads });
        const onCountsChange = vi.fn();
        renderDay({ onCountsChange });
        await screen.findByText('8 pieces of work');
        expect(document.querySelectorAll('[data-thread-lane]')).toHaveLength(8);
        expect(document.querySelectorAll('[data-aggregate-lane]')).toHaveLength(2);
        expect(onCountsChange).toHaveBeenLastCalledWith({ workedOn: 8 });
        const title = within(lane('work-0')).getByText('OpenClaw cleanup');
        expect(title).toHaveClass('break-words');
        expect(title).toHaveAttribute('title', dense.threads[0].title);
        expect(title.parentElement?.className).toContain('min-w-[200px]');
        expect(title.parentElement?.className).toContain('min-[1440px]:w-[240px]');
        expect(within(lane('work-0')).getByTitle('Remove settings and verify the fresh shell · 50m active')).toBeInTheDocument();
        expect(within(lane('work-0')).getAllByTestId('stitched-gap-label')).toHaveLength(1);
        expect(within(lane('work-0')).getByTestId('stitched-gap-label').previousElementSibling).toHaveClass('overflow-hidden');
        expect([...screen.getByTestId('stitched-axis').children].map((c) => c.textContent)).toEqual(['8 AM', '9', '10', '11', '12 PM', '1', '2']);
        // Gap links appear after the timeline measures its mounted axis.
        const links = await within(lane('work-0')).findAllByTestId('stitched-gap-link');
        expect(links).toHaveLength(3);
        expect(links[0]).toHaveAttribute('title', 'Away 1h 36m · 8:25–10:01 AM · Project 1: Claude Code sessions');
        expect(screen.getByTestId('stitched-legend').textContent).toBe('Focused workInterruptionSame work resumed');
        fireEvent.click(screen.getByText('Short moments · 12 min in all'));
        const shortList = screen.getByRole('list', { name: 'Short moments · 12 min in all' });
        expect(within(shortList).getAllByRole('listitem')).toHaveLength(9);
        expect(within(shortList).getByText('Quick check 0')).toBeInTheDocument();
        expect(within(shortList).getByText('2:00 PM')).toBeInTheDocument();
        for (const tick of document.querySelectorAll('[data-aggregate-lane="aggregate:short"] [data-testid="stitched-tick"]')) {
            expect(tick).toHaveStyle({ width: '6px' });
            expect(tick).not.toHaveAttribute('title');
        }
        fireEvent.click(screen.getByText('Other work · 2 · 23 min'));
        expect(screen.queryByRole('list', { name: 'Short moments · 12 min in all' })).toBeNull();
        const otherList = screen.getByRole('list', { name: 'Other work · 2 · 23 min' });
        expect(within(otherList).getAllByRole('listitem')).toHaveLength(2);
        expect(within(otherList).getByText('Project 9: Claude Code sessions')).toBeInTheDocument();
    });

    it('keeps an all-short day visible with a zero thread count', async () => {
        binding('GetActivitiesInRange').mockResolvedValue(dense.activities.filter((moment) => moment.id.startsWith('short-')));
        binding('GetDayThreads').mockResolvedValue({ grouped: true, threads: [] });
        const onCountsChange = vi.fn();
        renderDay({ onCountsChange });
        expect(await screen.findByText('0 pieces of work')).toBeInTheDocument();
        expect(screen.getByText('Short moments · 12 min in all')).toBeInTheDocument();
        expect(document.querySelectorAll('[data-thread-lane]')).toHaveLength(0);
        expect(onCountsChange).toHaveBeenLastCalledWith({ workedOn: 0 });
        expect(screen.queryByTestId('stitched-pick-up')).toBeNull();
    });

    it('draws a zero-minute qualifying stretch as a six-pixel tick without a label', async () => {
        const item = { ...dense.threads[0], stretches: dense.threads[0].stretches.map((stretch, i) => i === 0 ? { ...stretch, end: stretch.start, activeMs: 0 } : stretch) };
        binding('GetActivitiesInRange').mockResolvedValue(dense.activities);
        binding('GetDayThreads').mockResolvedValue({ grouped: true, threads: [item] });
        renderDay();
        await screen.findByText('Work threads');
        const tick = within(lane('work-0')).getAllByTestId('stitched-stretch')[0];
        expect(tick.getAttribute('style')).toContain('6px');
        expect(tick).toHaveTextContent('');
    });
});

describe('Stitched cleanup: resume eligibility', () => {
    const at = (h: number, m = 0) => new Date(2026, 9, 19, h, m).getTime();
    const item = (activeMinutes: number, end = at(15, 52)): StitchedThread => {
        const original = stitchThreads(threads, activities, at(16), false)[0];
        return { ...original, thread: { ...original.thread, title: 'Isoform hub: Claude Code sessions', activeMs: activeMinutes * 60_000, endMs: end }, moments: [], stretches: [{ ...original.stretches[0], end, activeMs: activeMinutes * 60_000 }] };
    };

    it('requires ten minutes active and an end within the last four hours, including boundaries', () => {
        expect(pickUpNotice([item(9.99)], '2026-10-19', at(16))).toBeNull();
        expect(pickUpNotice([item(10, at(12))], '2026-10-19', at(16))).not.toBeNull();
        expect(pickUpNotice([item(10, at(12) - 1)], '2026-10-19', at(16))).toBeNull();
        expect(pickUpNotice([item(10, at(16) + 1)], '2026-10-19', at(16))).toBeNull();
    });

    it('rejects running work and activity newer than the last stretch', () => {
        expect(pickUpNotice([{ ...item(29), live: true }], '2026-10-19', at(16))).toBeNull();
        const stale = item(29);
        stale.moments = [{ ...activities[0], endMs: at(15, 53) }];
        expect(pickUpNotice([stale], '2026-10-19', at(16))).toBeNull();
        expect(pickUpNotice([{ ...item(29), thread: { ...item(29).thread, endMs: at(15, 53) } }], '2026-10-19', at(16))).toBeNull();
    });

    it('chooses longest-active qualifying work and says the thread words and active minutes', () => {
        const shorter = { ...item(12, at(15, 59)), thread: { ...item(12).thread, id: 'shorter', activeMs: 12 * 60_000, endMs: at(15, 59) } };
        const notice = pickUpNotice([shorter, item(29)], '2026-10-19', at(16))!;
        expect(notice.title).toBe('Isoform hub · Claude Code sessions');
        const list = item(29);
        list.thread.title = 'Isoform: publish decision, PR review, launch video';
        expect(pickUpNotice([list], '2026-10-19', at(16))?.title).toBe('Isoform · publish decision');
        expect(t()('stitched.pickUp.title', { title: notice.title }) + ' ' + pickUpBody(t(), notice)).toBe('Pick up where you left off: Isoform hub · Claude Code sessions. You stepped away at 3:52 PM, after 29 min.');
    });
});

describe('Stitched cleanup: caller and boundary guards', () => {
    it('expires the resume card while viewing a past day as the clock advances', async () => {
        const ticks: (() => void)[] = [];
        const interval = vi.spyOn(window, 'setInterval').mockImplementation(((callback: TimerHandler) => {
            ticks.push(callback as () => void);
            return 42;
        }) as typeof window.setInterval);
        try {
            renderDay();
            await screen.findByTestId('stitched-pick-up');
            vi.spyOn(Date, 'now').mockReturnValue(new Date(2026, 9, 19, 17, 0).getTime());
            act(() => ticks.forEach((tick) => tick()));
            expect(screen.queryByTestId('stitched-pick-up')).toBeNull();
        } finally {
            interval.mockRestore();
        }
    });

    it('keeps the resume thread first, with its card on Stitched, when the sections stack', async () => {
        renderDay({ section: undefined });
        await screen.findByText('Work threads');
        expect(document.querySelector('[data-thread-lane]')).toHaveAttribute('data-thread-lane', 'mer');
        expect(screen.getByTestId('stitched-pick-up')).toBeInTheDocument();
        expect(screen.queryByTestId('noticed-pick-up')).toBeNull();
    });

    it('lists exactly three overflow threads with their total active minutes', async () => {
        const dense = stitchedCleanupFixture(DAY);
        const more = { ...dense.threads[9], id: 'eleventh', title: 'Another project', activeMs: 18 * 60_000 };
        binding('GetActivitiesInRange').mockResolvedValue(dense.activities);
        binding('GetDayThreads').mockResolvedValue({ grouped: true, threads: [...dense.threads, more] });
        renderDay();
        fireEvent.click(await screen.findByText('Other work · 3 · 41 min'));
        expect(within(screen.getByRole('list', { name: 'Other work · 3 · 41 min' })).getAllByRole('listitem')).toHaveLength(3);
    });

    it('keeps ungrouped long observations in the aggregate and offers no synthetic resume thread', () => {
        const ungrouped = stitchThreads([], [activities[0]], Date.now(), false);
        expect(stitchedRows(ungrouped).threads).toHaveLength(0);
        expect(stitchedRows(ungrouped).shortMoments).toHaveLength(1);
        expect(pickUpNotice(ungrouped, '2026-10-19', activities[0].endMs + 60_000)).toBeNull();
    });

    it('shows the exact full gap tooltip while naming its interrupter once in the margin', async () => {
        const at = (h: number, m: number) => new Date(2026, 9, 19, h, m).getTime();
        const modified = threads.map((thread) => thread.id === 'hal' ? { ...thread, gaps: [{ start: at(11, 20), end: at(12, 56), minutes: 96, threadId: 'nw', label: 'unused, list' }] }
            : thread.id === 'nw' ? { ...thread, title: 'Isoform hub: Claude Code sessions' } : thread);
        binding('GetDayThreads').mockResolvedValue({ grouped: true, threads: modified });
        renderDay();
        await screen.findByText('Work threads');
        expect(within(lane('hal')).getByTestId('stitched-gap-link')).toHaveAttribute('title', 'Away 1h 36m · 11:20 AM–12:56 PM · Isoform hub: Claude Code sessions');
        expect(within(lane('hal')).getByTestId('stitched-gap-label')).toHaveTextContent('Away 1h 36m · Isoform hub');
    });
});

describe.each(['en', 'zh-CN'])('Stitched shared Today clock in %s', (language) => {
    it.each([
        [7, 0, '7:00 AM', '7:00'],
        [12, 30, '12:30 PM', '12:30'],
        [15, 52, '3:52 PM', '15:52'],
        [0, 5, '12:05 AM', '0:05'],
    ])('formats %s:%s using the shared catalog', (hour, minute, en, zh) => {
        const at = new Date(2026, 9, 19, hour, minute).getTime();
        expect(stitchedClock(at, language)).toBe(language === 'en' ? en : zh);
    });
});


describe('Polish 6 Activity roles', () => {
    it('records work in ink, interruptions in faint and only the resume stitch in accent', async () => {
        renderDay();
        await screen.findByText('Work threads');
        for (const bar of within(lane('hal')).getAllByTestId('stitched-stretch')) expect(bar).toHaveClass('bg-ink');
        for (const bar of within(lane('msg')).getAllByTestId('stitched-stretch')) expect(bar).toHaveClass('bg-faint');
        for (const link of screen.getAllByTestId('stitched-gap-link')) expect(link).toHaveClass('border-accent');
        const marks = screen.getByTestId('stitched-legend').querySelectorAll('[aria-hidden]');
        expect(marks[0]).toHaveClass('bg-ink');
        expect(marks[1]).toHaveClass('bg-faint');
        expect(marks[2]).toHaveClass('border-accent', 'border-dashed');
        const strip = card('hal');
        expect(strip.querySelector('[data-work-stretch]')).toHaveClass('bg-ink', 'text-background');
        expect(strip.querySelector('[data-work-away]')).toHaveClass('bg-panel-warm');
        fireEvent.click(lane('msg'));
        for (const bar of within(lane('msg')).getAllByTestId('stitched-stretch')) expect(bar).toHaveClass('bg-faint');
    });
});


it('keeps work and interruption roles on both Short moments and Other work ticks', async () => {
    const data = stitchedCleanupFixture(DAY);
    const other = data.threads.slice(-2);
    const marked = new Set([other[0].id]);
    const short = data.activities.find((moment) => moment.id.startsWith('short-'))!;
    data.threads.push({ ...other[1], id: 'short-interruption', activityIds: [short.id], interruption: true,
        activeMs: short.activeMs, timeSpentMs: short.activeMs, startMs: short.startMs, endMs: short.endMs,
        stretches: [{ start: short.startMs, end: short.endMs, activeMs: short.activeMs, activityIds: [short.id], resumePoint: null }], gaps: [] });
    binding('GetActivitiesInRange').mockResolvedValue(data.activities);
    binding('GetDayThreads').mockResolvedValue({ grouped: true, threads: data.threads.map((thread) => marked.has(thread.id) ? { ...thread, interruption: true } : thread) });
    renderDay();
    await screen.findByText('Work threads');
    for (const id of ['aggregate:short', 'aggregate:other']) {
        const row = document.querySelector(`[data-aggregate-lane="${id}"]`)!;
        const ticks = within(row as HTMLElement).getAllByTestId('stitched-tick');
        expect(ticks.length).toBeGreaterThan(1);
        for (const tick of ticks) expect(tick).toHaveClass(tick.getAttribute('data-interruption') === 'true' ? 'bg-faint' : 'bg-ink');
        expect(ticks.some((tick) => tick.classList.contains('bg-ink'))).toBe(true);
        expect(ticks.some((tick) => tick.classList.contains('bg-faint'))).toBe(true);
    }
});

// Go's real-SQLite guard supplies the source-derived 10m/20m allocation.
// The renderer must preserve it for both grouped and ungrouped rows.
it.each([false, true])('preserves sparse overnight allocation in every view (grouped=%s)', async (grouped) => {
    const { clipDayActivity, activeTime } = await import('./activity-stitched');
    const start = new Date(2026, 9, 19, 23).getTime();
    const edge = new Date(2026, 9, 20).getTime();
    const end = new Date(2026, 9, 20, 2).getTime();
    let recorded = 0, stitched = 0;
    for (const [day, dayEnd, activeMs] of [[new Date(2026, 9, 19).getTime(), edge, 600_000], [edge, new Date(2026, 9, 21).getTime(), 1200_000]]) {
        const moment = { ...activities[0], startMs: start, endMs: end, activeMs };
        const thread = { ...threads[0], startMs: start, endMs: end, activeMs, timeSpentMs: activeMs,
            activityIds: [moment.id], gaps: [], stretches: [{ start, end, activeMs, activityIds: [moment.id], resumePoint: null }] };
        const clip = clipDayActivity(moment, day, dayEnd, false);
        const items = stitchThreads(grouped ? [thread] : [], [moment], end, false, day, dayEnd);
        expect(clip.activeMs).toBe(activeMs);
        expect(items[0].moments[0].activeMs).toBe(activeMs);
        expect(items[0].stretches[0].activeMs).toBe(activeMs);
        expect(activeTime(items[0])).toBe(activeMs);
        recorded += clip.activeMs; stitched += activeTime(items[0]);
    }
    expect(recorded).toBe(1800_000);
    expect(stitched).toBe(recorded);
});
// Appended to the branch's stitched-view test in the disposable snapshot.
it('translated axis boxes stay disjoint for narrow and wide open/collapsed sidebars', async () => {
    const { stitchedAxisClock } = await import('./activity-stitched');
    for (const language of ['en','zh-CN']) for (const viewport of [760,1160,1440]) for (const sidebar of [56,240]) {
        const width = Math.max(300, viewport - sidebar - 170 - 170 - 80);
        const start = new Date(2026,9,19,0).getTime();
        const end = new Date(2026,9,20,0).getTime();
        const labels = stitchedAxisTicks(start,end,width,at => stitchedAxisClock(at,language,i18n.getFixedT(language,'activity')));
        expect(labels.length).toBeGreaterThan(1);
        for (let i=0; i<labels.length; i++) {
            expect(labels[i].left).toBeGreaterThanOrEqual(0);
            expect(labels[i].left+labels[i].width).toBeLessThanOrEqual(width);
            if(i) expect(labels[i].left).toBeGreaterThanOrEqual(labels[i-1].left+labels[i-1].width+8);
        }
    }
});

it.each([false, true])('renders the source-owned sparse total on both days (grouped=%s)', async (grouped) => {
    const start = new Date(2026, 9, 19, 23).getTime(), edge = new Date(2026, 9, 20).getTime(), end = new Date(2026, 9, 20, 2).getTime();
    const allocation = (day: number) => day < edge ? 600_000 : 1200_000;
    binding('GetActivitiesInRange').mockImplementation(async (day: number) => [{ ...activities[0], startMs: start, endMs: end, activeMs: allocation(day) }]);
    binding('GetDayThreads').mockImplementation(async (day: number) => ({ grouped: true, threads: grouped ? [{ ...threads[0], startMs: start, endMs: end,
        activityIds: [activities[0].id], activeMs: allocation(day), timeSpentMs: allocation(day), gaps: [],
        stretches: [{ start, end, activeMs: allocation(day), activityIds: [activities[0].id], resumePoint: null }] }] : [] }));
    const first = renderDay();
    await screen.findByText('From 11:00 PM to 12:00 AM · 10m active');
    expect(screen.queryByText('In progress')).toBeNull(); first.unmount();
    const second = renderDay({ date: new Date(2026, 9, 20) });
    await screen.findByText('From 12:00 AM to 2:00 AM · 20m active'); second.unmount();
});


describe('Integration 2213: day bounds with render memoization', () => {
    afterEach(() => {
        vi.useRealTimers();
        vi.unstubAllGlobals();
    });

    function overnightSource() {
        const start = new Date(2026, 9, 19, 23).getTime();
        const edge = new Date(2026, 9, 20).getTime();
        const end = new Date(2026, 9, 20, 2).getTime();
        // Both days legitimately own 15m, so the same immutable reply can be
        // reused. The selected civil day must still invalidate clipped models.
        const moment = { ...activities[0], id: 'overnight-2213', title: 'Writing overnight report',
            startMs: start, endMs: end, activeMs: 900_000, inProgress: true };
        const thread = { ...threads[0], id: 'overnight-2213', title: moment.title,
            startMs: start, endMs: end, activeMs: moment.activeMs, timeSpentMs: moment.activeMs,
            activityIds: [moment.id], stretches: [{ start, end, activeMs: moment.activeMs, activityIds: [moment.id], resumePoint: null }], gaps: [] };
        const cards = [moment];
        const reply = { grouped: true, threads: [thread] };
        binding('GetActivitiesInRange').mockResolvedValue(cards);
        binding('GetDayThreads').mockResolvedValue(reply);
        return { start, edge, end, moment, thread, cards, reply };
    }

    it('keeps settled clipped inputs stable through search and minute ticks', async () => {
        vi.mocked(Date.now).mockReturnValue(new Date(2026, 9, 23, 20).getTime());
        vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] });
        const model = vi.spyOn(stitchedModel, 'stitchThreads');
        const view = renderDay();
        await screen.findByTestId('stitched-axis');
        await act(async () => {});
        const calls = model.mock.calls.length;
        expect(calls).toBeGreaterThan(0);
        expect(model.mock.calls.at(-1)?.[0]).toBe(threads);
        expect(model.mock.calls.at(-1)?.[1]).toBe(activities);
        view.rerender(dayElement({ searchQuery: 'launch memo' }));
        await screen.findByRole('heading', { name: '12:00–12:55 PM' });
        vi.mocked(Date.now).mockReturnValue(new Date(2026, 9, 23, 20, 1).getTime());
        await act(async () => { vi.advanceTimersByTime(60_000); });
        expect(model).toHaveBeenCalledTimes(calls);
        expect(screen.queryByText('In progress')).toBeNull();
    });

    it('reclips shared activity and thread replies on day change and uses the current Chat day', async () => {
        const source = overnightSource();
        const onStartChat = vi.fn();
        const view = renderDay({ onStartChat });
        await screen.findByRole('heading', { name: '11:00 PM–12:00 AM' });
        expect(screen.getByText('From 11:00 PM to 12:00 AM · 15m active')).toBeInTheDocument();
        expect(card(source.thread.id)).not.toBeNull();
        expect(screen.getByTestId('stitched-continues-after')).toBeInTheDocument();
        view.rerender(dayElement({ date: new Date(source.edge), onStartChat }));
        await screen.findByRole('heading', { name: '12:00–2:00 AM' });
        expect(screen.getByText('From 12:00 AM to 2:00 AM · 15m active')).toBeInTheDocument();
        expect(screen.getByTestId('stitched-continues-before')).toBeInTheDocument();
        expect(screen.queryByTestId('stitched-continues-after')).toBeNull();
        expect(screen.queryByText('In progress')).toBeNull();
        expect(screen.getAllByTestId('stitched-stretch')[0].title).toContain('12:00–2:00 AM');
        expect(source.moment.startMs).toBe(source.start);
        expect(source.thread.stretches[0].end).toBe(source.end);
        view.rerender(dayElement({ date: new Date(source.edge), onStartChat, workedOnView: 'recorded' }));
        fireEvent.click((await screen.findByText(source.moment.title)).closest('button')!);
        fireEvent.click(screen.getByRole('button', { name: 'Chat about this' }));
        binding('GetMainChat').mockResolvedValue({ session: { localId: 'main-1' } });
        fireEvent.change(screen.getByRole('textbox', { name: 'Ask anything about this' }), { target: { value: 'Where was I?' } });
        fireEvent.click(screen.getByRole('button', { name: 'Ask' }));
        await waitFor(() => expect(binding('AskMainChatWithContext')).toHaveBeenLastCalledWith('Where was I?', {
            sourceKind: 'activity', sourceId: source.moment.id, dayStartMs: source.edge,
        }));
    });

    it('updates cached header and moment span with the visible query while retaining total row counts', async () => {
        const onCountsChange = vi.fn();
        const view = renderDay({ searchQuery: 'launch memo', onCountsChange });
        await screen.findByRole('heading', { name: '12:00–12:55 PM' });
        expect(screen.getByText('From 12:00 PM to 12:55 PM · 47m active')).toBeInTheDocument();
        const counts = onCountsChange.mock.calls.at(-1)?.[0];
        expect(counts).toBeDefined();
        view.rerender(dayElement({ searchQuery: 'erp', onCountsChange }));
        await screen.findByRole('heading', { name: '9:12–11:40 AM' });
        expect(screen.getByText('From 9:12 AM to 11:40 AM · 2h active')).toBeInTheDocument();
        expect(screen.queryByText('From 12:00 PM to 12:55 PM · 47m active')).toBeNull();
        expect([...document.querySelectorAll('[data-thread-lane]')].map((row) => row.getAttribute('data-thread-lane'))).toEqual(['hal']);
        expect(onCountsChange.mock.calls.at(-1)?.[0]).toEqual(counts);
    });

    it('withdraws a memoized one-stretch continuation immediately on pause and recovers from fresh rows', async () => {
        const source = overnightSource();
        renderDay();
        await screen.findByTestId('stitched-continues-after');
        const oldCard = card(source.thread.id);
        let release!: (value: typeof source.cards) => void;
        binding('GetActivitiesInRange').mockImplementation(() => new Promise<typeof source.cards>((resolve) => { release = resolve; }));
        binding('GetDayThreads').mockResolvedValue({ grouped: true, threads: [] });
        await act(async () => { fixture.listeners.get('background-listening:changed')?.forEach((notify) => notify({ enabled: false })); });
        expect(oldCard).not.toBeInTheDocument();
        expect(card(source.thread.id)).toBeNull();
        await waitFor(() => expect(release).toBeTypeOf('function'));
        await act(async () => { release([]); });
        await screen.findByText('Nothing here for this day.');
        binding('GetActivitiesInRange').mockResolvedValue(source.cards);
        binding('GetDayThreads').mockResolvedValue(source.reply);
        await act(async () => { fixture.listeners.get('background-listening:changed')?.forEach((notify) => notify({ enabled: true })); });
        await screen.findByRole('heading', { name: '11:00 PM–12:00 AM' });
        expect(screen.getByTestId('stitched-continues-after')).toBeInTheDocument();
        expect(screen.queryByText('In progress')).toBeNull();
    });

    it('removes the memoized work card when an overnight source becomes ungrouped', async () => {
        const source = overnightSource();
        renderDay();
        await screen.findByTestId('stitched-continues-after');
        binding('GetDayThreads').mockResolvedValue({ grouped: false, threads: [] });
        await act(async () => { fixture.listeners.get('activity:activities-updated')?.forEach((notify) => notify(undefined)); });
        await waitFor(() => expect(card(source.thread.id)).toBeNull());
        expect(screen.queryByRole('button', { name: 'Looks right' })).toBeNull();
        expect(document.querySelector('[data-thread-card]')).toBeNull();
        expect(screen.getByRole('heading', { name: '11:00 PM–12:00 AM' })).toBeInTheDocument();
        expect(screen.getByText('Short moments · 15 min in all')).toBeInTheDocument();
    });

    it('refreshes memoized clipped hour tick boxes after resize and language change', async () => {
        const source = overnightSource();
        const start = DAY.getTime();
        source.moment.startMs = start - 3_600_000;
        source.thread.startMs = source.moment.startMs;
        source.thread.stretches[0].start = source.moment.startMs;
        vi.stubGlobal('ResizeObserver', undefined);
        let width = 700;
        vi.mocked(HTMLElement.prototype.getBoundingClientRect).mockImplementation(() => ({ width } as DOMRect));
        renderDay();
        const axis = await screen.findByTestId('stitched-axis');
        function assertTicks(language: string) {
            const expected = stitchedAxisTicks(start, source.edge, width, (at, withPeriod) => stitchedModel.stitchedAxisClock(at, language, i18n.getFixedT(language, 'activity'), withPeriod));
            const boxes = [...axis.children] as HTMLElement[];
            expect(boxes.map((box) => box.textContent)).toEqual(expected.map((tick) => tick.text));
            boxes.forEach((box, index) => {
                expect(parseFloat(box.style.left)).toBeCloseTo(expected[index].left);
                expect(parseFloat(box.style.width)).toBeCloseTo(expected[index].width);
                expect(parseFloat(box.style.left)).toBeGreaterThanOrEqual(0);
                expect(parseFloat(box.style.left) + parseFloat(box.style.width)).toBeLessThanOrEqual(width);
                if (index) expect(parseFloat(box.style.left)).toBeGreaterThanOrEqual(parseFloat(boxes[index - 1].style.left) + parseFloat(boxes[index - 1].style.width) + 8);
            });
            expect(expected.every((tick) => tick.at >= start && tick.at <= source.edge)).toBe(true);
            return boxes.length;
        }
        await waitFor(() => expect(axis.children.length).toBeGreaterThan(2));
        const wide = assertTicks('en');
        width = 300;
        fireEvent(window, new Event('resize'));
        await waitFor(() => expect(axis.children.length).toBeLessThan(wide));
        assertTicks('en');
        await act(async () => { await i18n.changeLanguage('zh-CN'); });
        assertTicks('zh-CN');
    });

    it('reuses lane tooltip models on selection but translates clipped tips and continuation marks', async () => {
        const source = overnightSource();
        const translations = vi.spyOn(i18n, 't');
        const countTips = () => translations.mock.calls.filter(([key]) => String(key).endsWith('stitched.segmentTip')).length;
        const view = renderDay();
        await screen.findByTestId('stitched-continues-after');
        const before = countTips();
        expect(before).toBeGreaterThan(0);
        const stretch = screen.getAllByTestId('stitched-stretch')[0];
        expect(stretch.title).toContain('11:00 PM–12:00 AM');
        fireEvent.click(lane(source.thread.id));
        view.rerender(dayElement());
        expect(countTips()).toBe(before);
        await act(async () => { await i18n.changeLanguage('zh-CN'); });
        expect(countTips()).toBeGreaterThan(before);
        const translate = i18n.getFixedT('zh-CN', 'activity');
        expect(stretch.title).toContain(`${stitchedClock(source.start, 'zh-CN')}–${stitchedClock(source.edge, 'zh-CN')}`);
        expect(screen.getByLabelText(translate('stitched.continuesAfter'))).toBeInTheDocument();
    });
});

// Thursday A2: no leading zeros or seconds anywhere in Activity.
describe('Activity clock times', () => {
    const at = (h: number, m: number) => new Date(2026, 10, 2, h, m, 37).getTime();
    it('says 9:12 AM, never 09:12 or seconds', () => {
        expect(formatTimeFromMs(at(9, 12))).toBe('9:12 AM');
        expect(stitchedClock(at(9, 12))).toBe('9:12 AM');
    });
    it('writes a range once: 9:12–10:02 AM', () => {
        expect(formatTimeRangeFromMs(at(9, 12), at(10, 2), 'en')).toBe('9:12–10:02 AM');
        expect(formatTimeRangeFromMs(at(11, 40), at(12, 55), 'en')).toBe('11:40 AM–12:55 PM');
        expect(formatTimeRangeFromMs(at(9, 12), at(10, 2), 'zh-CN')).not.toMatch(/0\d:|:\d\d:\d\d/);
    });
});

// AR62: a redaction tag never reaches a title on any Activity surface.
describe('Redaction tags never show in a thread title', () => {
    it('writes around the tag, so the pick-up title reads Isoform release', () => {
        expect(splitThreadTitle('Isoform · [REDACTED:code] release').title).toBe('Isoform release');
        expect(splitThreadTitle('Isoform: [REDACTED:code] release')).toEqual({ title: 'Isoform release', detail: '' });
    });

    it('leaves a title that is only a tag empty, so the view shows a private item', () => {
        expect(splitThreadTitle('[REDACTED:api_key]').title).toBe('');
    });
});
