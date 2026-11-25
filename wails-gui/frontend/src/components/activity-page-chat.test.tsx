import { act, render } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import '@/i18n';
import { ActivityPage } from './activity-page';
import { resetFeatureSwitchesForTest } from '@/hooks/useFeatureSwitches';
import type { CronJobsProp } from './timeline/useCalendarData';

// Scope cut A4: with the `chat` switch off, Activity hands no way into Chat
// to its rows (no Chat about this), and with it on the page passes it on.
const seen = vi.hoisted(() => ({ dayProps: [] as Array<Record<string, unknown>> }));

vi.mock('@/lib/wails-shim-app', () => new Proxy({}, {
    has: () => true,
    get: (_target, name: string) => (name === 'then' ? undefined : vi.fn().mockResolvedValue([])),
}));
vi.mock('../../wailsjs/runtime/runtime', () => ({ EventsOn: () => () => {}, ClipboardSetText: vi.fn() }));
vi.mock('sonner', () => ({ toast: Object.assign(vi.fn(), { error: vi.fn(), success: vi.fn() }) }));
vi.mock('./activity-day-view', async (importOriginal) => {
    const actual = await importOriginal<typeof import('./activity-day-view')>();
    return {
        ...actual,
        ActivityDayView: (props: Record<string, unknown>) => {
            seen.dayProps.push(props);
            return null;
        },
    };
});

const cronJobs = { jobs: [], loading: false, toggleEnabled: vi.fn(), deleteJob: vi.fn(), runNow: vi.fn(), updateJob: vi.fn(), loadRuns: vi.fn(), loadRunLogs: vi.fn() } as unknown as CronJobsProp;

function switches(chat: boolean) {
    (globalThis as Record<string, unknown>).__isoformFeatureSwitchesForTest = { automations: true, playbooks: true, proactiveVoice: true, calendarHolds: true, chat, activityExtras: true };
    resetFeatureSwitchesForTest();
}

async function renderOn(section: 'yourDay' | 'workedOn') {
    seen.dayProps.length = 0;
    await act(async () => {
        render(<ActivityPage cronJobs={cronJobs} section={section} onSectionChange={vi.fn()} onStartChat={vi.fn()} onOpenChat={vi.fn()} />);
    });
    return seen.dayProps.at(-1);
}

describe('Activity with Chat hidden (scope cut A4)', () => {
    beforeEach(() => {
        Element.prototype.scrollIntoView = vi.fn();
    });

    it('gives the day no way into Chat while Chat is hidden', async () => {
        switches(false);
        const props = await renderOn('workedOn');
        expect(props).toBeDefined();
        expect(props?.onStartChat).toBeUndefined();
    });

    it('passes Chat on while the switch is on', async () => {
        switches(true);
        const props = await renderOn('workedOn');
        expect(typeof props?.onStartChat).toBe('function');
    });
});
