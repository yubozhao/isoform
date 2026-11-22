import { act, renderHook, waitFor } from '@testing-library/react';
import { expect, it, vi } from 'vitest';
import { useMainChatReports } from './useMainChatReports';

const mocks = vi.hoisted(() => ({ open: vi.fn(), toast: vi.fn() }));
vi.mock('react-i18next', async (original) => ({ ...await original<typeof import('react-i18next')>(), useTranslation: () => ({ t: (key: string) => key }) }));
vi.mock('sonner', () => ({ toast: { error: mocks.toast } }));
vi.mock('@/lib/page-queries', () => ({ PageEventsOn: () => () => {}, LoadStreamItemsPage: vi.fn() }));
vi.mock('@/lib/wails-shim-app', () => ({ OpenExternalURL: vi.fn() }));
vi.mock('@/lib/main-chat-navigation', () => ({ openMainChatTarget: mocks.open, loadMainChatItemUpdates: vi.fn() }));

it('reports a failed Today target to its inline handler instead of a toast', async () => {
    mocks.open.mockRejectedValueOnce(new Error('unavailable'));
    const noop = vi.fn();
    const { result } = renderHook(() => useMainChatReports({ projects: [], updateProject: noop, openTasksUpNext: noop, activeSessionId: null, markSessionUnread: noop, openSession: noop, openHandoff: noop, openPlaybook: noop, setPendingInsightKnowledgeId: noop, setKnowledgeOpen: noop, setPendingInsightMemoryId: noop, setMemoryOpen: noop, openScheduled: noop, openToday: noop, setAutomationsSelectedJobId: noop, openSettings: noop, openActivity: noop, setPeopleInitialSelection: noop, setPeopleOpen: noop, openThread: noop, openActivitySession: noop, openWatch: noop }));
    const inline = vi.fn();
    act(() => result.current({ kind: 'session', id: 'missing' }, inline));
    await waitFor(() => expect(inline).toHaveBeenCalledOnce());
    expect(mocks.toast).not.toHaveBeenCalled();
});
