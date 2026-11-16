import { render, screen, waitFor } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import '@/i18n';
import type { WatchLaneHour } from '@/lib/wails-shim-app';
import { ActivityWatchLane, watchLaneHourHeight, watchLaneHourKind } from './activity-watch-lane';

const mocks = vi.hoisted(() => ({ lane: vi.fn() }));

vi.mock('@/lib/wails-shim-app', () => ({ GetWatchLane: mocks.lane }));

const day = new Date();
day.setHours(0, 0, 0, 0);
const at = (hour: number) => day.getTime() + hour * 3_600_000;

function hour(start: number, overrides: Partial<WatchLaneHour> = {}): WatchLaneHour {
    return { start, recordedSeconds: 0, talkSeconds: 0, pausedSeconds: 0, current: false, ...overrides };
}

describe('ActivityWatchLane', () => {
    beforeEach(() => {
        vi.clearAllMocks();
    });

    it('draws one column per hour: talk, a pause, a quiet hour and the hour not over yet', async () => {
        mocks.lane.mockResolvedValue({
            dayStart: day.getTime(),
            hours: [
                hour(at(9), { recordedSeconds: 1200, talkSeconds: 600 }),
                hour(at(10), { recordedSeconds: 3600, talkSeconds: 1200, pausedSeconds: 900 }),
                hour(at(11), { pausedSeconds: 1800 }),
                hour(at(12)),
                hour(at(13), { recordedSeconds: 1800, talkSeconds: 300, current: true }),
            ],
        });
        const { container } = render(<ActivityWatchLane />);
        expect(await screen.findByText('Your watch today')).toBeTruthy();
        const columns = container.querySelectorAll('[data-lane-hour]');
        expect(Array.from(columns).map((column) => column.getAttribute('data-lane-hour'))).toEqual([
            'talk',
            'talk',
            'paused',
            'quiet',
            'talk',
        ]);
        expect(screen.getByLabelText('9 AM: 10 min of talk')).toBeTruthy();
        expect(screen.getByLabelText('10 AM: 20 min of talk. Paused for 15 min.')).toBeTruthy();
        expect(screen.getByLabelText('11 AM: paused for 30 min')).toBeTruthy();
        expect(screen.getByLabelText('12 PM: quiet')).toBeTruthy();
        // No numbers are printed on the lane itself, only its two ends.
        expect(screen.getByText('9 AM')).toBeTruthy();
        expect(screen.getByText('Now')).toBeTruthy();
        expect(screen.queryByText(/min of talk/)).toBeNull();
    });

    it('shows nothing before the watch has recorded anything today, or when the lane cannot be read', async () => {
        mocks.lane.mockResolvedValue({ dayStart: day.getTime(), hours: [] });
        const first = render(<ActivityWatchLane />);
        await waitFor(() => expect(mocks.lane).toHaveBeenCalled());
        expect(first.container.querySelector('[data-testid="watch-lane"]')).toBeNull();
        first.unmount();

        mocks.lane.mockRejectedValue(new Error('no'));
        const second = render(<ActivityWatchLane />);
        await waitFor(() => expect(mocks.lane).toHaveBeenCalledTimes(2));
        expect(second.container.querySelector('[data-testid="watch-lane"]')).toBeNull();
    });

    it('sizes a column by its talk out of the whole hour', () => {
        expect(watchLaneHourHeight(hour(0, { talkSeconds: 1800 }))).toBe(50);
        expect(watchLaneHourHeight(hour(0, { talkSeconds: 9999 }))).toBe(100);
        expect(watchLaneHourHeight(hour(0))).toBe(0);
        expect(watchLaneHourKind(hour(0, { talkSeconds: 30, pausedSeconds: 30 }))).toBe('quiet');
        expect(watchLaneHourKind(hour(0, { talkSeconds: 61, pausedSeconds: 3000 }))).toBe('talk');
    });
});
