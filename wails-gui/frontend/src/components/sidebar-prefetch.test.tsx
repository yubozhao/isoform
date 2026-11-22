import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import i18n from '@/i18n';
import { Sidebar } from './sidebar';
import { STANDALONE_CHAT_PROJECT_ID } from '@/lib/stream-helpers';
import { resetPageQueries } from '@/lib/page-queries';
import type { Project, Session } from '@/types';

// Hover/keyboard-focus prefetch: a pointer or keyboard pass over a navigation
// row starts that page's primary reads so the click paints from cache.
const fixture = vi.hoisted(() => ({ bindings: new Map<string, ReturnType<typeof vi.fn>>(), calls: [] as string[] }));
vi.mock('@/lib/wails-shim-app', () => new Proxy({}, {
    has: () => true,
    get: (_target, name: string) => {
        if (name === 'then') return undefined;
        if (!fixture.bindings.has(name)) fixture.bindings.set(name, vi.fn(async () => {
            fixture.calls.push(name);
            if (name === 'GetMainChat') return { session: { localId: 'main' } };
            if (name === 'GetAboutInfo') return { update: { status: 'idle', version: '0.0.0' }, later: false };
            return [];
        }));
        return fixture.bindings.get(name);
    },
}));
vi.mock('../../wailsjs/runtime/runtime', () => ({ EventsOn: () => () => {} }));
vi.mock('@/hooks/useUserProfile', () => ({ useUserProfile: () => ({ profile: null, loading: false }) }));

function buildProjects(sessions: Session[]): Project[] {
    return [{
        id: STANDALONE_CHAT_PROJECT_ID,
        name: 'General Chat',
        path: '',
        activeSessionId: null,
        activeWorkspaceId: 'ws-1',
        sortOrder: 0,
        workspaces: [{ id: 'ws-1', label: 'General Chat', isDefault: true, activeSessionId: null, sessions, createdAt: 1, updatedAt: 1 }],
    } as unknown as Project];
}

function renderSidebar(sessions: Session[] = []) {
    const noop = () => {};
    return render(
        <Sidebar
            projects={buildProjects(sessions)}
            activeProjectId={STANDALONE_CHAT_PROJECT_ID}
            collapsed={false}
            width={260}
            onToggleCollapse={noop}
            onOpenSettings={noop}
            onOpenActivity={noop}
            onOpenYou={noop}
            onOpenToday={noop}
            onOpenAgents={noop}
            onOpenMainChat={noop}
            closedEyes={false}
            onToggleClosedEyes={noop}
            appMode="today"
        />,
    );
}

beforeEach(async () => {
    await i18n.changeLanguage('en');
    resetPageQueries();
    fixture.calls = [];
});
afterEach(() => {
    cleanup();
    resetPageQueries();
});

describe('sidebar row prefetch', () => {
    it.each([
        ['Activity', ['EnsureDailySummary', 'GetActivitiesInRange', 'GetDayThreads']],
        ['Today', ['GetTodayTopics', 'GetLatestBrief']],
        ['Chat', ['GetMainChat']],
    ] as const)('pointerenter on the %s row starts its primary reads', async (label, expected) => {
        renderSidebar();
        fireEvent.pointerEnter(screen.getByRole('button', { name: label }));
        await vi.waitFor(() => {
            for (const name of expected) expect(fixture.calls).toContain(name);
        });
    });

    it('keyboard focus on the Activity row starts its reads', async () => {
        renderSidebar();
        fireEvent.focus(screen.getByRole('button', { name: 'Activity' }));
        await vi.waitFor(() => expect(fixture.calls).toContain('EnsureDailySummary'));
    });
});

describe('You row prefetch', () => {
    it('pointerenter on the You row starts the About you reads', async () => {
        renderSidebar();
        fireEvent.pointerEnter(screen.getByRole('button', { name: 'You' }));
        await vi.waitFor(() => {
            for (const name of ['GetAboutYou', 'GetMemoryEntries', 'GetMemoryStatus', 'GetAboutYouPortrait']) expect(fixture.calls).toContain(name);
        });
    });
});
