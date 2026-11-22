import { pageQueryCache } from '@/lib/query-cache';
import { resetPageQueries } from '@/lib/page-queries';
import { onPagePrivacyWithdrawal } from '@/lib/page-privacy-withdrawal';
import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { Project, StreamItem } from '@/types';
import type { MainChatReportTarget } from '@/lib/stream-helpers';
import i18n from '@/i18n';

// Mount the production App and its report hook. Only unrelated feature surfaces
// and native I/O are replaced, so removing App's subscription or route prop
// breaks these tests even when the navigation helpers still work in isolation.
const fixture = vi.hoisted(() => ({
    listeners: new Map<string, Set<(payload: any) => unknown>>(),
    persistentListeners: new Map<string, Set<(payload: any) => unknown>>(),
    bindings: new Map<string, ReturnType<typeof vi.fn>>(),
    initialProjects: [] as Project[],
    projects: [] as Project[],
    streamProps: null as null | { items: StreamItem[]; onOpenReportTarget: (target: MainChatReportTarget) => void },
    unread: vi.fn(),
    switchSession: vi.fn(),
    sidebarProps: null as null | Record<string, any>,
    openMainChat: vi.fn(),
    noop: vi.fn(),
    windowTitle: vi.fn(),
    toastError: vi.fn(),
    pages: {} as Record<string, any>,
    composerProps: null as null | Record<string, any>,
    replacePrompt: vi.fn(),
    sendImmediate: vi.fn(),
    handleCancel: vi.fn(),
    removeQueued: vi.fn(),
    editQueued: vi.fn(),
    queuedMessages: [] as { prompt: string; images: []; files: [] }[],
}));

vi.mock('@/lib/wails-shim-app', () => new Proxy({}, {
    has: () => true,
    get: (_target, name: string) => {
        if (name === 'then') return undefined;
        if (!fixture.bindings.has(name)) fixture.bindings.set(name, vi.fn().mockResolvedValue({}));
        return fixture.bindings.get(name);
    },
}));
vi.mock('@/lib/wails-shim-runtime', () => ({
    EventsOn: (name: string, handler: (payload: any) => unknown) => {
        const listeners = fixture.listeners.get(name) ?? new Set();
        listeners.add(handler);
        fixture.listeners.set(name, listeners);
        const persistent = fixture.persistentListeners.get(name) ?? new Set();
        persistent.add(handler);
        fixture.persistentListeners.set(name, persistent);
        return () => listeners.delete(handler);
    },
    LogInfo: vi.fn(), LogWarning: vi.fn(), WindowSetTitle: fixture.windowTitle,
    WindowGetPosition: vi.fn(), WindowGetSize: vi.fn(), WindowIsMaximised: vi.fn(),
}));
vi.mock('../../wailsjs/runtime/runtime', () => ({
    EventsOn: (name: string, handler: (payload: any) => unknown) => {
        const listeners = fixture.listeners.get(name) ?? new Set();
        listeners.add(handler);
        fixture.listeners.set(name, listeners);
        const persistent = fixture.persistentListeners.get(name) ?? new Set();
        persistent.add(handler);
        fixture.persistentListeners.set(name, persistent);
        return () => listeners.delete(handler);
    },
    EventsEmit: vi.fn(),
    LogInfo: vi.fn(),
    LogWarning: vi.fn(),
    LogError: vi.fn(),
    WindowSetTitle: fixture.windowTitle,
    WindowGetPosition: vi.fn(),
    WindowGetSize: vi.fn(),
    WindowIsMaximised: vi.fn(),
}));
vi.mock('sonner', () => ({ toast: { error: fixture.toastError, success: vi.fn(), warning: vi.fn(), info: vi.fn() } }));
vi.mock('@/hooks/useProjects', async () => {
    const { useCallback, useState } = await import('react');
    return { useProjects: () => {
        const [projects, setProjects] = useState(fixture.initialProjects);
        fixture.projects = projects;
        const updateProject = useCallback((id: string, update: (project: Project) => Project) => {
            setProjects((current) => current.map((project) => project.id === id ? update(project) : project));
        }, []);
        const project = projects[0];
        const workspace = project.workspaces[0];
        return {
            projects, setProjects, updateProject, activeProject: project, activeProjectId: project.id,
            activeWorkspace: workspace, activeSession: workspace.sessions[0], projectsHydrated: true,
            unreadSessionIds: new Set(), markSessionUnread: fixture.unread,
            handleSwitchSession: fixture.switchSession,
            handleOpenMainChat: fixture.openMainChat,
            markProjectsHydrated: fixture.noop, markSessionRead: fixture.noop,
            setActiveProjectId: fixture.noop,
        };
    } };
});
vi.mock('@/hooks/useSettings', async () => {
    const { useState } = await import('react');
    return { useSettings: () => {
        const [settingsOpen, setSettingsOpen] = useState(false);
        return { settingsOpen, setSettingsOpen, vimMode: false };
    } };
});
vi.mock('@/hooks/useAgentStreaming', () => ({ useAgentStreaming: () => ({
    status: { ready: true }, setStatus: fixture.noop, flushAllPendingContent: fixture.noop,
}) }));
vi.mock('@/hooks/useUserProfile', () => ({ useUserProfile: () => ({ profile: null }) }));
vi.mock('@/hooks/useEnvironmentSwitch', () => ({ useEnvironmentSwitch: () => ({}) }));
vi.mock('@/hooks/useDevToolsUnlock', () => ({ useDevToolsUnlock: () => false }));
vi.mock('@/hooks/useTheme', () => ({ useTheme: () => ({ terminalTheme: {} }) }));
vi.mock('@/hooks/useKeybindings', () => ({ PAGE_KEYS: ['today', 'chat', 'agents', 'activity', 'you'], useKeybindings: () => ({ getCombo: fixture.noop, findMatchingAction: () => null }) }));
vi.mock('@/hooks/useSidebarResize', () => ({ useSidebarResize: () => ({ sidebarWidth: 240 }) }));
vi.mock('@/hooks/useResizable', () => ({ useResizable: () => ({ size: 288 }) }));
vi.mock('@/hooks/useSidebarGroups', () => ({ useSidebarGroups: () => ({}) }));
vi.mock('@/hooks/useComposerInput', () => ({ useComposerInput: () => ({
    prompt: '', images: [], files: [], queuedMessages: fixture.queuedMessages, replacePrompt: fixture.replacePrompt, sendImmediate: fixture.sendImmediate,
    handleCancel: fixture.handleCancel, removeQueuedMessage: fixture.removeQueued, editQueuedMessage: fixture.editQueued,
}) }));
vi.mock('@/hooks/useComposerDockSlide', () => ({ useComposerDockSlide: () => ({}) }));
vi.mock('@/hooks/useVoiceInput', () => ({ useVoiceInput: () => ({}) }));
vi.mock('@/hooks/useTerminal', () => ({ useTerminal: () => ({ terminalTabs: [], allTerminalTabs: [] }) }));
vi.mock('@/hooks/useStandaloneMode', () => ({ useStandaloneMode: () => false }));
vi.mock('@/hooks/useCronJobs', () => ({ useCronJobs: () => ({ jobs: [{ id: 'job-1', name: 'Weekly review' }] }) }));
vi.mock('@/hooks/useUpcoming', () => ({ useUpcoming: () => ({ items: [], loading: false }) }));
vi.mock('@/hooks/useMainChatSetup', () => ({ useMainChatSetup: () => ({ current: null }) }));
vi.mock('@/hooks/useProductTelemetry', () => ({ useProductTelemetry: () => undefined }));
vi.mock('@/hooks/useInsightNotification', () => ({ useInsightNotification: () => undefined }));
vi.mock('@/hooks/useHandoffNotification', () => ({ useHandoffNotification: () => undefined }));
vi.mock('@/hooks/useTtsAnnounce', () => ({ useTtsAnnounce: () => undefined }));
vi.mock('@/components/relaunch-notice', () => ({ RelaunchNotice: () => null, useScreenRecordingRelaunch: () => ({ needed: false }) }));
vi.mock('@/components/ui/dialog', () => ({
    Dialog: ({ open, children }: any) => open ? children : null,
    DialogContent: ({ children }: any) => children,
    DialogHeader: ({ children }: any) => children,
    DialogTitle: ({ children }: any) => children,
    DialogDescription: ({ children }: any) => children,
    DialogClose: ({ children }: any) => children,
}));
vi.mock('@/components/ui/popover', () => ({
    Popover: ({ children }: any) => children,
    PopoverTrigger: ({ children }: any) => children,
    PopoverContent: () => null,
}));
vi.mock('@/components/stream-body', () => ({ StreamBody: (props: any) => {
    fixture.streamProps = props;
    return <>{props.toolbar}{props.header}<output data-testid="stream">{JSON.stringify(props.items)}</output></>;
} }));
vi.mock('@/components/sidebar', () => ({ Sidebar: (props: any) => {
    fixture.sidebarProps = props;
    return null;
} }));
vi.mock('@/components/ui/avatar', () => ({ Avatar: () => null }));
vi.mock('@/components/composer', () => ({ Composer: (props: any) => {
    fixture.composerProps = props;
    return null;
} }));
vi.mock('@/components/main-chat-setup-card', () => ({ MainChatSetupCard: () => null }));
vi.mock('@/components/onboarding/components/AgentChoice', () => ({ AgentChoice: () => null }));
vi.mock('@/components/activity-page', () => ({ ActivityPage: (props: any) => {
    fixture.pages.ActivityPage = props;
    return <output data-testid="ActivityPage" />;
} }));
vi.mock('@/components/memory-page', () => ({ MemoryPage: (props: any) => {
    fixture.pages.MemoryPage = props;
    return <output data-testid="MemoryPage" />;
} }));
vi.mock('@/components/plan-review-card', () => ({ PlanReviewCard: () => null }));
vi.mock('@/components/permission-question-card', () => ({ PermissionQuestionCard: () => null }));
vi.mock('@/components/terminal-panel', () => ({ TerminalPanel: () => null }));
vi.mock('@/components/compaction-banner', () => ({ CompactionBanner: () => null }));
vi.mock('@/components/search-dialog', () => ({ SearchDialog: () => null }));
vi.mock('@/components/tts-download-notifications', () => ({ TtsDownloadNotifications: () => null }));
vi.mock('@/components/deliverables/DeliverablesPage', () => ({ DeliverablesPage: () => null }));
vi.mock('@/components/onboarding-screen', () => ({ OnboardingScreen: () => null }));
vi.mock('@/components/standalone-setup-screen', () => ({ StandaloneSetupScreen: () => null }));
vi.mock('@/components/app-toaster', () => ({ AppToaster: () => null }));
vi.mock('@/components/confirm-dialog', () => ({ ConfirmDialog: () => null }));
vi.mock('@/components/settings-page', () => ({ SettingsPage: (props: any) => {
    fixture.pages.SettingsPage = props;
    return <output data-testid="SettingsPage" />;
} }));
vi.mock('@/components/knowledge-page', () => ({ KnowledgePage: (props: any) => {
    fixture.pages.KnowledgePage = props;
    return <output data-testid="KnowledgePage" />;
} }));
vi.mock('@/components/people-page', () => ({ PeoplePage: (props: any) => {
    fixture.pages.PeoplePage = props;
    return <output data-testid="PeoplePage" />;
} }));
vi.mock('@/components/today/TodayPage', () => ({ TodayPage: (props: any) => {
    fixture.pages.TodayPage = props;
    return <output data-testid="TodayPage" />;
} }));
vi.mock('@/components/automations/AutomationDetailPane', () => ({ AutomationDetailPane: (props: any) => {
    fixture.pages.AutomationDetailPane = props;
    return <output data-testid="AutomationDetailPane" />;
} }));
vi.mock('@/components/tasks/TasksPage', () => ({ TasksPage: (props: any) => {
    fixture.pages.TasksPage = props;
    return <output data-testid="TasksPage" />;
} }));

import App from './App';

function binding(name: string) {
    if (!fixture.bindings.has(name)) fixture.bindings.set(name, vi.fn());
    return fixture.bindings.get(name)!;
}

function projectFixture(): Project {
    return {
        id: 'project-1', name: 'Example', path: '', activeWorkspaceId: 'workspace-1',
        workspaces: [{
            id: 'workspace-1', isDefault: true, activeSessionId: 'main-1',
            sessions: [{
                localId: 'main-1', kind: 'main', label: 'Main chat', model: '', itemsLoaded: true, streamItemCount: 1,
                items: [{ id: 'status-1', kind: 'system', toolName: 'main_chat_status', sortOrder: 4, content: 'Original status.' }],
            }, {
                localId: 'other-1', kind: 'side', label: 'Other chat', model: '', itemsLoaded: false, streamItemCount: 0, items: [],
            }],
        }],
    } as Project;
}

/** Mounts the app, which opens on Today, then opens the main chat the way
 *  the sidebar does, so every check below starts from the chat as before.
 *  The session switch this takes is not counted. */
async function mountApp() {
    const mounted = render(<App />);
    await screen.findByTestId('TodayPage');
    await act(async () => { fixture.sidebarProps!.onOpenMainChat(); });
    fixture.openMainChat.mockClear?.();
    fixture.switchSession.mockClear();
    await screen.findByTestId('stream');
    return mounted;
}

async function emit(payload: unknown) {
    const listeners = fixture.listeners.get('session:items-appended');
    // The chat projection and frame share one native cache dispatcher.
    expect(listeners?.size).toBe(1);
    await act(async () => {
        await Promise.all([...listeners!].map((listener) => listener(payload)));
    });
}

async function openTarget(target: MainChatReportTarget) {
    expect(fixture.streamProps?.onOpenReportTarget).toBeTypeOf('function');
    await act(async () => { fixture.streamProps!.onOpenReportTarget(target); });
}

describe('App main-chat report wiring', () => {
    beforeEach(async () => {
        resetPageQueries();
        onPagePrivacyWithdrawal(() => {})();
        localStorage.clear();
        fixture.listeners.clear();
        fixture.streamProps = null;
        fixture.pages = {};
        fixture.initialProjects = [projectFixture()];
        fixture.unread.mockReset();
        fixture.switchSession.mockReset();
        fixture.openMainChat.mockReset().mockResolvedValue('main-1');
        fixture.toastError.mockReset();
        for (const mock of fixture.bindings.values()) mock.mockReset().mockResolvedValue({});
        binding('LoadUIState').mockImplementation(async (key: string) => key === 'yansu.onboarding.state'
            ? JSON.stringify({ completed: true, permissionsComplete: true }) : 'true');
        binding('LoadAllProjects').mockResolvedValue(null);
        binding('LoadActiveProjectID').mockResolvedValue('');
        binding('ListAvailableModels').mockResolvedValue([{ default: 'codex', models: [{ id: 'codex' }] }]);
        binding('GetVersion').mockResolvedValue('test');
        binding('LoadStreamItemsPage').mockResolvedValue({ items: [] });
        await i18n.changeLanguage('en');
    });
    afterEach(cleanup);

    it.each([false, true])('revalidates every cached row type without replacing live rows; streaming=%s', async (streaming) => {
        const rows = ['user', 'assistant', 'thinking', 'tool_use', 'tool_result', 'system'].map((kind, i) => ({ id: `row-${i}`, kind, sortOrder: i, content: `Kept ${kind}` })) as StreamItem[];
        const session = fixture.initialProjects[0].workspaces[0].sessions[0];
        session.items = rows;
        session.streaming = streaming;
        const mounted = await mountApp();
        await act(async () => {
            pageQueryCache.set('LoadStreamItemsPage', ['main-1', -1, 50], { items: rows.map((row) => ({ ...row, content: `Refreshed ${row.kind}` })), hasMore: false, nextCursor: 0 });
        });
        expect(fixture.streamProps!.items.map((row) => row.kind)).toEqual(rows.map((row) => row.kind));
        expect(fixture.streamProps!.items.map((row) => row.content)).toEqual(rows.map((row) => `${streaming ? 'Kept' : 'Refreshed'} ${row.kind}`));
        mounted.unmount();
    });

    it('refreshes an updated row outside the newest page through the native event and App project state', async () => {
        const mounted = await mountApp();
        const loadPage = binding('LoadStreamItemsPage');
        loadPage.mockResolvedValueOnce({ items: [] }).mockResolvedValueOnce({ items: [{
            id: 'status-1', kind: 'system', toolName: 'main_chat_status', sortOrder: 250, content: 'Updated status.',
        }] });
        await emit({ sessionId: 'main-1', itemId: 'status-1', sortOrder: 250 });
        expect(loadPage).toHaveBeenNthCalledWith(1, 'main-1', -1, 50);
        expect(loadPage).toHaveBeenNthCalledWith(2, 'main-1', 251, 1);
        expect(fixture.streamProps!.items).toEqual([expect.objectContaining({
            id: 'status-1', sortOrder: 250, content: 'Updated status.',
        })]);
        expect(screen.getByTestId('stream')).toHaveTextContent('Updated status.');
        expect(fixture.unread).not.toHaveBeenCalled();

        // Read the latest project state after rerender; old events lacking a
        // position must use the row's new position, not its mount-time value.
        loadPage.mockClear().mockResolvedValueOnce({ items: [] }).mockResolvedValueOnce({ items: [{
            id: 'status-1', kind: 'system', toolName: 'main_chat_status', sortOrder: 250, content: 'Updated again.',
        }] });
        await emit({ sessionId: 'main-1', itemId: 'status-1' });
        expect(loadPage).toHaveBeenNthCalledWith(2, 'main-1', 251, 1);
        expect(screen.getByTestId('stream')).toHaveTextContent('Updated again.');
        mounted.unmount();
        // One renderer invalidator survives; the unmounted page does no IO.
        const calls = binding('LoadStreamItemsPage').mock.calls.length;
        await emit({ sessionId: 'main' });
        expect(binding('LoadStreamItemsPage')).toHaveBeenCalledTimes(calls);
    });

    it('marks an unloaded inactive chat unread without fetching its transcript', async () => {
        await mountApp();
        await emit({ sessionId: 'other-1', itemId: 'new-1', sortOrder: 1 });
        expect(binding('LoadStreamItemsPage')).not.toHaveBeenCalled();
        expect(fixture.projects[0].workspaces[0].sessions[1].streamItemCount).toBe(1);
        expect(fixture.unread).toHaveBeenCalledWith('other-1');
    });

    it('opens the main chat from a work notification and dismisses the current overlay', async () => {
        await mountApp();
        await openTarget({ kind: 'handoff', id: 'work-1' });
        expect(screen.getByTestId('TasksPage')).toBeInTheDocument();
        await openTarget({ kind: 'settings', id: 'notifications' });
        expect(screen.getByTestId('SettingsPage')).toBeInTheDocument();
        const listeners = fixture.listeners.get('notification:clicked');
        expect(listeners?.size).toBe(1);
        await act(async () => {
            for (const listener of listeners!) listener('main-chat:work');
        });
        expect(fixture.openMainChat).toHaveBeenCalledTimes(1);
        expect(fixture.switchSession).not.toHaveBeenCalled();
        expect(localStorage.getItem('yansu.appMode')).toBe('chat');
        expect(screen.queryByTestId('TasksPage')).not.toBeInTheDocument();
        expect(screen.queryByTestId('SettingsPage')).not.toBeInTheDocument();
        expect(screen.getByTestId('stream')).toBeInTheDocument();
    });

    it('loads and opens a proactive item from a main-chat-item notification', async () => {
        binding('LoadMainChatProactiveItem').mockResolvedValue({
            id: 'proactive-1',
            kind: 'system',
            toolName: 'main_chat_proactive',
            toolInput: JSON.stringify({
                momentId: 'moment-1',
                kind: 'brief_ready',
                headline: 'Your brief is ready.',
                body: '',
                draft: false,
                actionIds: ['open'],
                actionLabels: { open: 'Open' },
                target: { kind: 'today' },
                evidence: [{ kind: 'engagement', id: 'moment-1', target: { kind: 'today' } }],
                state: 'pending',
                briefId: 'brief-1',
            }),
            content: 'Your brief is ready.',
            sessionId: 'main-1',
            sortOrder: 5,
        });
        await mountApp();
        await act(async () => {
            for (const listener of fixture.listeners.get('notification:clicked')!) listener('main-chat-item:moment-1');
        });
        await waitFor(() => expect(binding('LoadMainChatProactiveItem')).toHaveBeenCalledWith('moment-1'));
        expect(fixture.openMainChat).toHaveBeenCalled();
        expect(screen.getByTestId('main-chat-item-panel')).toHaveAttribute('data-panel-open', 'moment-1');
        fireEvent.click(screen.getByLabelText('Close details'));
        expect(screen.queryByTestId('main-chat-item-panel')).not.toBeInTheDocument();
    });

    it('restarts an unloaded notification open after an activity update', async () => {
        const completions: Array<(value: unknown) => void> = [];
        binding('LoadMainChatProactiveItem').mockImplementation(() => new Promise(resolve => completions.push(resolve)));
        await mountApp();
        await act(async () => {
            for (const listener of fixture.listeners.get('notification:clicked')!) listener('main-chat-item:moment-1');
        });
        await waitFor(() => expect(completions).toHaveLength(1));
        await act(async () => {
            for (const listener of fixture.persistentListeners.get('activity:activities-updated') ?? []) listener(undefined);
        });
        await waitFor(() => expect(completions).toHaveLength(2));
        await act(async () => completions[0]({ id: 'old', kind: 'system', toolName: 'main_chat_proactive', toolInput: JSON.stringify({ momentId: 'moment-1', kind: 'brief_ready', headline: 'Old', body: '', actionIds: ['open'], actionLabels: { open: 'Open' }, target: { kind: 'today' }, state: 'pending' }) }));
        expect(screen.queryByTestId('main-chat-item-panel')).not.toBeInTheDocument();
        await act(async () => completions[1]({ id: 'new', kind: 'system', toolName: 'main_chat_proactive', toolInput: JSON.stringify({ momentId: 'moment-1', kind: 'brief_ready', headline: 'Fresh notification', body: '', actionIds: ['open'], actionLabels: { open: 'Open' }, target: { kind: 'today' }, state: 'pending' }) }));
        expect(
            within(screen.getByTestId('main-chat-item-panel'))
                .getAllByRole('heading', { name: 'Fresh notification' })[0],
        ).toBeInTheDocument();
    });

    it('does not open an unloaded notification after privacy withdrawal', async () => {
        let finish!: (value: unknown) => void;
        binding('LoadMainChatProactiveItem').mockImplementation(() => new Promise(resolve => { finish = resolve; }));
        await mountApp();
        await act(async () => {
            for (const listener of fixture.listeners.get('notification:clicked')!) listener('main-chat-item:moment-1');
        });
        await waitFor(() => expect(binding('LoadMainChatProactiveItem')).toHaveBeenCalledWith('moment-1'));
        await act(async () => {
            for (const listener of fixture.persistentListeners.get('memory:withdrawn') ?? []) listener(undefined);
            finish({ id: 'late', kind: 'system', toolName: 'main_chat_proactive', toolInput: JSON.stringify({ momentId: 'moment-1', kind: 'brief_ready', headline: 'Late', body: '', actionIds: ['open'], actionLabels: { open: 'Open' }, target: { kind: 'today' }, state: 'pending' }) });
        });
        expect(screen.queryByTestId('main-chat-item-panel')).not.toBeInTheDocument();
    });

    it('closes the shared item panel on Escape', async () => {
        binding('LoadMainChatProactiveItem').mockResolvedValue({
            id: 'proactive-1',
            kind: 'system',
            toolName: 'main_chat_proactive',
            toolInput: JSON.stringify({
                momentId: 'moment-1',
                kind: 'brief_ready',
                headline: 'Your brief is ready.',
                body: '',
                draft: false,
                actionIds: ['open'],
                actionLabels: { open: 'Open' },
                target: { kind: 'today' },
                evidence: [{ kind: 'engagement', id: 'moment-1', target: { kind: 'today' } }],
                state: 'pending',
                briefId: 'brief-1',
            }),
            content: 'Your brief is ready.',
            sessionId: 'main-1',
            sortOrder: 5,
        });
        await mountApp();
        await act(async () => {
            for (const listener of fixture.listeners.get('notification:clicked')!) listener('main-chat-item:moment-1');
        });
        await screen.findByTestId('main-chat-item-panel');
        fireEvent.keyDown(window, { key: 'Escape' });
        expect(screen.queryByTestId('main-chat-item-panel')).not.toBeInTheDocument();
    });

    it('keeps historical handoff notification clicks routed to their saved work', async () => {
        await mountApp();
        await act(async () => {
            for (const listener of fixture.listeners.get('notification:clicked')!) listener('handoff:work-history');
        });
        expect(screen.getByTestId('TasksPage')).toBeInTheDocument();
        expect(fixture.pages.TasksPage.openHandoffId).toBe('work-history');
        expect(fixture.openMainChat).not.toHaveBeenCalled();
    });

    it.each([
        [{ kind: 'settings', id: 'activity' }, 'SettingsPage', 'initialTopic', 'activity'],
        [{ kind: 'settings', id: 'notifications' }, 'SettingsPage', 'initialTopic', 'notifications'],
        [{ kind: 'knowledge', id: 'note-1' }, 'KnowledgePage', 'initialSelectedId', 'note-1'],
        [{ kind: 'memory', id: 'belief-1' }, 'MemoryPage', 'initialSelectedId', 'belief-1'],
        [{ kind: 'person', id: 'person-1' }, 'PeoplePage', 'initialSelection', undefined],
        [{ kind: 'today' }, 'TodayPage', null, null],
        [{ kind: 'upcoming' }, 'TasksPage', null, null],
        [{ kind: 'automation', id: 'job-1' }, 'AutomationDetailPane', 'job', { id: 'job-1' }],
        [{ kind: 'handoff', id: 'handoff-1' }, 'TasksPage', 'openHandoffId', 'handoff-1'],
        [{ kind: 'playbook', id: 'playbook-1' }, 'KnowledgePage', 'openPlaybookId', 'playbook-1'],
    ] as const)('routes the StreamBody target %j through App to %s', async (target, page, prop, expected) => {
        await mountApp();
        await openTarget(target);
        expect(await screen.findByTestId(page)).toBeInTheDocument();
        if (prop !== null) {
            if (typeof expected === 'object' && expected !== null) {
                expect(fixture.pages[page][prop]).toMatchObject(expected);
            } else {
                expect(fixture.pages[page][prop]).toBe(expected);
            }
        }
    });

    it('opens a playbook Isoform still offers in Tasks, where Up next shows the offer', async () => {
        binding('GetPlaybooksState').mockResolvedValue({ plans: [{ id: 'playbook-2', proposal: { id: 'proposal-1' } }] });
        await mountApp();
        await openTarget({ kind: 'playbook', id: 'playbook-2' });
        expect(await screen.findByTestId('TasksPage')).toBeInTheDocument();
        expect(screen.queryByTestId('KnowledgePage')).not.toBeInTheDocument();
        binding('GetPlaybooksState').mockResolvedValue({ plans: [{ id: 'playbook-2', proposal: { id: 'proposal-1', answer: 'accepted' } }] });
        await openTarget({ kind: 'playbook', id: 'playbook-2' });
        expect(await screen.findByTestId('KnowledgePage')).toBeInTheDocument();
        expect(fixture.pages.KnowledgePage.openPlaybookId).toBe('playbook-2');
    });

    it('routes upcoming and playbook targets to their current homes', async () => {
        await mountApp();
        await openTarget({ kind: 'upcoming' });
        expect(await screen.findByTestId('TasksPage')).toBeInTheDocument();
        await openTarget({ kind: 'playbook', id: 'playbook-1' });
        await screen.findByTestId('KnowledgePage');

    });

    it('opens the noticed suggestion on its day and clears focus on ordinary Activity navigation', async () => {
        await mountApp();
        await openTarget({ kind: 'today' });
        const target = { date: '2026-10-19', suggestionId: 'suggestion-1', action: 'follow_up' };
        await act(async () => fixture.pages.TodayPage.handlers.onOpenActivitySuggestion(target));
        expect(screen.getByTestId('ActivityPage')).toBeInTheDocument();
        expect(fixture.pages.ActivityPage.initialDate).toBe(target.date);
        expect(fixture.pages.ActivityPage.noticedFocus).toEqual(target);
        expect(binding('PrepareTodayChatDraft')).not.toHaveBeenCalled();
        await act(async () => fixture.pages.ActivityPage.onNoticedFocusConsumed(target));
        expect(fixture.pages.ActivityPage.noticedFocus).toBeNull();
        await openTarget({ kind: 'today' });
        await openTarget({ kind: 'calendar', id: 'event-1' });
        expect(fixture.pages.ActivityPage.noticedFocus).toBeNull();
    });

    it('opens the merged Activity page on today for the old Calendar target', async () => {
        await mountApp();
        await openTarget({ kind: 'calendar', id: 'event-1' });
        expect(screen.getByTestId('ActivityPage')).toBeInTheDocument();
        const now = new Date();
        const today = `${now.getFullYear()}-${String(now.getMonth() + 1).padStart(2, '0')}-${String(now.getDate()).padStart(2, '0')}`;
        expect(fixture.pages.ActivityPage.initialDate).toBe(today);
    });

    it('routes session links through App session selection and opens only Mail for mail evidence', async () => {
        await mountApp();
        await openTarget({ kind: 'session', id: 'other-1' });
        expect(fixture.switchSession).toHaveBeenCalledWith('project-1', 'other-1');
        await openTarget({ kind: 'mail', id: 'private-thread' });
        expect(binding('OpenExternalURL')).toHaveBeenCalledWith('/System/Applications/Mail.app');
    });

    it('gives the main chat its header outside the scroll area without the retired summary', async () => {
        await mountApp();
        // Chat review v1.0.5 removes the summary; the header (title, sub-line) remains.
        await waitFor(() => expect(fixture.streamProps).toEqual(expect.objectContaining({
            toolbar: expect.anything(),
            mainChatView: expect.objectContaining({ hideRoutine: false, onFillChip: expect.any(Function) }),
        })));
        expect(screen.queryByTestId('main-chat-summary')).toBeNull();
        expect(screen.getByTestId('stream')).toBeInTheDocument();
        // The field says what Chat is for (computer-chat v1.0.0 rule 2).
        expect(fixture.composerProps!.placeholder).toBe('Ask about your day, or correct Isoform');
    });

    it('sends an empty-chat suggestion pill or an answer chip at once, leaving nothing in the box (Chat K4)', async () => {
        await mountApp();
        fixture.replacePrompt.mockClear();
        fixture.sendImmediate.mockClear();
        act(() => fixture.composerProps!.onSelectSuggestion('Plan my afternoon'));
        expect(fixture.sendImmediate).toHaveBeenCalledWith('Plan my afternoon');
        act(() => (fixture.streamProps as any).mainChatView.onFillChip('Hold 3:00 to 4:30 for the memo'));
        expect(fixture.sendImmediate).toHaveBeenLastCalledWith('Hold 3:00 to 4:30 for the memo');
        expect(fixture.replacePrompt).not.toHaveBeenCalled();
    });

    it('uses localized error feedback when the App Mail binding rejects', async () => {
        await mountApp();
        binding('OpenExternalURL').mockRejectedValue(new Error('native open failed'));
        await openTarget({ kind: 'mail' });
        expect(await screen.findByText(i18n.t('chat:failure.openIt'))).toBeInTheDocument();
        expect(fixture.toastError).not.toHaveBeenCalled();
    });
});
