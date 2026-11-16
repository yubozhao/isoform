import { resetPageQueries } from '@/lib/page-queries';
import { rememberActivityDate, resetActivitySessionCache } from '@/components/timeline/activity-session-cache';
import { act, cleanup, fireEvent, render, screen, within } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { Project, StreamItem } from '@/types';
import i18n from '@/i18n';
import { toast } from 'sonner';

// Mount the production App and exercise the Activity-page / Library-page
// navigation: the sidebar row, the menu bar events, notifications and the
// Today link. Same approach as App.main-chat-reports.test.tsx — only
// unrelated feature surfaces and native I/O are replaced.
const fixture = vi.hoisted(() => ({
    listeners: new Map<string, Set<(payload: any) => unknown>>(),
    bindings: new Map<string, ReturnType<typeof vi.fn>>(),
    initialProjects: [] as Project[],
    projects: [] as Project[],
    streamProps: null as null | { items: StreamItem[] },
    composerProps: null as null | Record<string, unknown>,
    sidebarProps: null as null | Record<string, any>,
    openMainChat: vi.fn(),
    prefill: vi.fn(),
    noop: vi.fn(),
    windowTitle: vi.fn(),
    pages: {} as Record<string, any>,
    // The last open dialog's onOpenChange: how a test closes it as Escape
    // or the Close button would.
    openDialogChange: null as null | ((open: boolean) => void),
}));

const pageSwitch = vi.hoisted(() => ({ begun: [] as string[] }));
vi.mock('@/lib/page-switch-timing', async (importOriginal) => {
    const actual = await importOriginal<typeof import('@/lib/page-switch-timing')>();
    return { ...actual, beginPageSwitch: (page: string) => { pageSwitch.begun.push(page); actual.beginPageSwitch(page); } };
});
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
        return () => listeners.delete(handler);
    },
    LogInfo: vi.fn(), LogWarning: vi.fn(), WindowSetTitle: fixture.windowTitle,
    WindowGetPosition: vi.fn(), WindowGetSize: vi.fn(), WindowIsMaximised: vi.fn(),
}));
vi.mock('sonner', () => ({ toast: Object.assign(vi.fn(), { error: vi.fn(), success: vi.fn(), warning: vi.fn(), info: vi.fn() }) }));
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
            unreadSessionIds: new Set(), markSessionUnread: fixture.noop,
            handleSwitchSession: fixture.noop, handleRefreshContext: fixture.noop,
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
vi.mock('@/hooks/useKeybindings', async (importOriginal) => {
    const actual = await importOriginal<typeof import('@/hooks/useKeybindings')>();
    return {
        ...actual,
        useKeybindings: () => ({
            ...actual.useKeybindings(),
            // Command K is the only shortcut these navigation tests press.
            findMatchingAction: (e: KeyboardEvent) => (e.metaKey && e.key === 'k' ? 'command-palette' : null),
        }),
    };
});
vi.mock('@/hooks/useSidebarResize', () => ({ useSidebarResize: () => ({ sidebarWidth: 240 }) }));
vi.mock('@/hooks/useResizable', () => ({ useResizable: () => ({ size: 288 }) }));
vi.mock('@/hooks/useSidebarGroups', () => ({ useSidebarGroups: () => ({}) }));
vi.mock('@/hooks/useComposerInput', () => ({ useComposerInput: () => ({ prompt: '', images: [], files: [], queuedMessages: [], prefillTodayPrompt: fixture.prefill, getDraftRevision: () => 0, prepareTodayPrompt: fixture.prefill }) }));
vi.mock('@/hooks/useComposerDockSlide', () => ({ useComposerDockSlide: () => ({}) }));
vi.mock('@/hooks/useVoiceInput', () => ({ useVoiceInput: () => ({}) }));
vi.mock('@/hooks/useTerminal', () => ({ useTerminal: () => ({ terminalTabs: [], allTerminalTabs: [] }) }));
vi.mock('@/hooks/useStandaloneMode', () => ({ useStandaloneMode: () => false }));
vi.mock('@/hooks/useCronJobs', () => ({ useCronJobs: () => ({ jobs: [] }) }));
vi.mock('@/hooks/useUpcoming', () => ({ useUpcoming: () => ({ items: [], loading: false }) }));
vi.mock('@/hooks/useMainChatSetup', () => ({ useMainChatSetup: () => ({ current: null }) }));
vi.mock('@/hooks/useProductTelemetry', () => ({ useProductTelemetry: () => undefined }));
vi.mock('@/hooks/useInsightNotification', () => ({ useInsightNotification: () => undefined }));
vi.mock('@/hooks/useHandoffNotification', () => ({ useHandoffNotification: () => undefined }));
vi.mock('@/hooks/useTtsAnnounce', () => ({ useTtsAnnounce: () => undefined }));
vi.mock('@/components/relaunch-notice', () => ({ RelaunchNotice: () => null, useScreenRecordingRelaunch: () => ({ needed: false }) }));
vi.mock('@/components/ui/dialog', async () => {
    const { createContext, useContext } = await import('react');
    const Open = createContext(false);
    return {
        Dialog: ({ open, onOpenChange, children }: any) => {
            if (open && onOpenChange) fixture.openDialogChange = onOpenChange;
            return <Open.Provider value={open}>{children}</Open.Provider>;
        },
        DialogContent: ({ children }: any) => useContext(Open) ? children : null,
        DialogHeader: ({ children }: any) => children,
        DialogTitle: ({ children }: any) => children,
        DialogDescription: ({ children }: any) => children,
        DialogClose: ({ children }: any) => children,
    };
});
vi.mock('@/components/ui/popover', () => ({
    Popover: ({ children }: any) => children,
    PopoverTrigger: ({ children }: any) => children,
    PopoverContent: () => null,
}));
vi.mock('@/components/stream-body', () => ({ StreamBody: (props: any) => {
    fixture.streamProps = props;
    return <output data-testid="stream">{JSON.stringify(props.items)}</output>;
} }));
vi.mock('@/components/sidebar', () => ({ Sidebar: (props: any) => {
    fixture.sidebarProps = props;
    return null;
}, UserPopoverMenu: () => null }));
vi.mock('@/components/ui/avatar', () => ({ Avatar: () => null }));
vi.mock('@/components/composer', () => ({ Composer: (props: Record<string, unknown>) => { fixture.composerProps = props; return null; } }));
vi.mock('@/components/main-chat-setup-card', () => ({ MainChatSetupCard: () => null }));
vi.mock('@/components/onboarding/components/AgentChoice', () => ({ AgentChoice: () => null }));
vi.mock('@/components/activity-page', () => ({ ActivityPage: (props: any) => {
    fixture.pages.ActivityPage = props;
    return <output data-testid="ActivityPage" />;
} }));
vi.mock('@/components/memory-page', () => ({ MemoryPage: (props: any) => {
    fixture.pages.MemoryPage = props;
    return <input aria-label="MemoryPage selection" defaultValue="" />;
} }));
vi.mock('@/components/plan-review-card', () => ({ PlanReviewCard: () => null }));
vi.mock('@/components/permission-question-card', () => ({ PermissionQuestionCard: () => null }));
vi.mock('@/components/terminal-panel', () => ({ TerminalPanel: () => null }));
vi.mock('@/components/compaction-banner', () => ({ CompactionBanner: () => null }));
vi.mock('@/components/search-dialog', () => ({ SearchDialog: (props: any) => {
    fixture.pages.SearchDialog = props;
    return null;
} }));
vi.mock('@/components/goals/GoalsPage', () => ({ GoalsPage: (props: any) => {
    fixture.pages.GoalsPage = props;
    return <output data-testid="GoalsPage" />;
} }));
vi.mock('@/components/tts-download-notifications', () => ({ TtsDownloadNotifications: () => null }));
vi.mock('@/components/onboarding-screen', () => ({ OnboardingScreen: () => null }));
vi.mock('@/components/standalone-setup-screen', () => ({ StandaloneSetupScreen: () => null }));
vi.mock('@/components/app-toaster', () => ({ AppToaster: () => null }));
vi.mock('@/components/confirm-dialog', () => ({ ConfirmDialog: () => null }));
vi.mock('@/components/settings-page', () => ({ SettingsPage: (props: any) => {
    fixture.pages.SettingsPage = props;
    return <><output data-testid="SettingsPage" /><input aria-label="SettingsPage selection" defaultValue="" /></>;
} }));
vi.mock('@/components/knowledge-page', () => ({ KnowledgePage: (props: any) => {
    fixture.pages.KnowledgePage = props;
    return <><output data-testid="KnowledgePage" /><input aria-label="KnowledgePage selection" defaultValue="" /></>;
} }));
vi.mock('@/components/people-page', () => ({ PeoplePage: (props: any) => {
    fixture.pages.PeoplePage = props;
    return <><output data-testid="PeoplePage" /><input aria-label="PeoplePage selection" defaultValue="" /></>;
} }));
vi.mock('@/components/today/TodayPage', () => ({ TodayPage: (props: any) => {
    fixture.pages.TodayPage = props;
    return <output data-testid="TodayPage" />;
} }));
vi.mock('@/components/automations/AutomationDetailPane', () => ({ AutomationDetailPane: () => null }));
vi.mock('@/components/tasks/TasksPage', () => ({ TasksPage: (props: any) => {
    fixture.pages.TasksPage = props;
    return <output data-testid="TasksPage" />;
} }));
vi.mock('@/components/deliverables/DeliverablesPage', () => ({ DeliverablesPage: (props: any) => {
    fixture.pages.DeliverablesPage = props;
    return <output data-testid="DeliverablesPage" />;
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
            }],
        }],
    } as Project;
}

/** Mounts the app, which opens on Today, then opens the main chat the way
 *  the sidebar does (no main-chat open is counted), so every check below
 *  starts from the chat as before. */
async function mountApp() {
    const mounted = render(<App />);
    await screen.findByTestId('TodayPage');
    await act(async () => { fixture.sidebarProps!.onOpenMainChat(); });
    fixture.openMainChat.mockClear?.();
    await screen.findByTestId('stream');
    return mounted;
}

async function emit(event: string, payload?: unknown) {
    const listeners = fixture.listeners.get(event);
    expect(listeners?.size).toBeGreaterThan(0);
    await act(async () => {
        await Promise.all([...listeners!].map((listener) => listener(payload)));
    });
}

function today(): string {
    const now = new Date();
    return `${now.getFullYear()}-${String(now.getMonth() + 1).padStart(2, '0')}-${String(now.getDate()).padStart(2, '0')}`;
}

describe('App Activity/Library navigation', () => {
    beforeEach(async () => {
        resetPageQueries();
        resetActivitySessionCache();
        localStorage.clear();
        fixture.listeners.clear();
        fixture.streamProps = null;
        fixture.sidebarProps = null;
        fixture.pages = {};
        fixture.initialProjects = [projectFixture()];
        fixture.openMainChat.mockReset().mockResolvedValue('main-1');
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

    it('debounces resize through one native snapshot and cancels pending saves on unmount', async () => {
        const mounted = await mountApp();
        vi.useFakeTimers();
        try {
            const capture = binding('CaptureAndSaveWindowState');
            capture.mockClear().mockResolvedValue(undefined);
            fireEvent(window, new Event('resize'));
            fireEvent(window, new Event('resize'));
            await act(async () => { vi.advanceTimersByTime(499); });
            expect(capture).not.toHaveBeenCalled();
            await act(async () => { vi.advanceTimersByTime(1); });
            expect(capture).toHaveBeenCalledTimes(1);
            expect(capture).toHaveBeenCalledWith();
            expect(binding('SaveWindowState')).not.toHaveBeenCalled();
            fireEvent(window, new Event('resize'));
            mounted.unmount();
            await act(async () => { vi.advanceTimersByTime(500); });
            expect(capture).toHaveBeenCalledTimes(1);
        } finally {
            vi.useRealTimers();
        }
    });

    it('does not toast when an Activity summary becomes visible on the page', async () => {
        await mountApp();
        vi.mocked(toast).mockClear();
        await act(async () => {
            for (const handler of fixture.listeners.get('activity:summary-ready') ?? []) handler('2026-10-23');
        });
        expect(toast).not.toHaveBeenCalled();
    });

    it('keeps reply navigation out of the composer', async () => {
        await mountApp();
        expect(fixture.composerProps).not.toHaveProperty('replyNavigation');
        expect(fixture.streamProps).not.toHaveProperty('onReplyNavigationChange');
    });

    it('keeps the chat column centered under the saved Full reading preference', async () => {
        await mountApp();
        const column = screen.getByTestId('main-chat-column');
        expect(column).toHaveClass('mx-auto', 'max-w-[1188px]', 'w-full');
        expect(column.firstElementChild).toHaveClass('max-w-[768px]');
    });

    it('keeps every control out of the Chat title strip even before app access is connected', async () => {
        await mountApp();
        const strip = document.querySelector('main > div')!;
        expect(strip.querySelector('button, svg')).toBeNull();
        expect(strip.textContent).toBe('');
    });

    it('mounts no Changes or Context panel, no panel buttons and no session tabs in a project chat (K7, K8)', async () => {
        const project = fixture.initialProjects[0];
        project.isYansuProject = true;
        project.gitInfo = { isGitRepo: true } as Project['gitInfo'];
        project.workspaces[0].sessions[0].kind = undefined;
        await mountApp();
        expect(screen.queryByTestId('project-session-toolbar')).not.toBeInTheDocument();
        expect(screen.queryByRole('button', { name: /Toggle (changes|context) panel/ })).not.toBeInTheDocument();
        expect(screen.queryByRole('tablist')).not.toBeInTheDocument();
        fireEvent.keyDown(window, { key: 'i', metaKey: true });
        expect(document.querySelector('[data-testid="project-changes-panel"], [data-testid="project-context-panel"]')).toBeNull();
    });

    it('has no project panel controls on the main Chat page', async () => {
        fixture.initialProjects[0].isYansuProject = true;
        fixture.initialProjects[0].gitInfo = { isGitRepo: true } as Project['gitInfo'];
        await mountApp();
        expect(screen.queryByTestId('project-session-toolbar')).not.toBeInTheDocument();
        expect(screen.queryByRole('button', { name: /Toggle (changes|context) panel/ })).not.toBeInTheDocument();
    });

    it('removes the redundant New chat bubble from the drag strip', async () => {
        await mountApp();
        expect(screen.queryByRole('button', { name: 'New chat' })).not.toBeInTheDocument();
    });

    it('updates the native window title when navigating from Today to Main chat and Activity', async () => {
        render(<App />);
        await screen.findByTestId('TodayPage');
        expect(fixture.windowTitle).toHaveBeenLastCalledWith('Today · Isoform');
        await act(async () => fixture.sidebarProps!.onOpenMainChat());
        expect(fixture.windowTitle).toHaveBeenLastCalledWith('Chat · Isoform');
        await act(async () => fixture.sidebarProps!.onOpenActivity());
        expect(fixture.windowTitle).toHaveBeenLastCalledWith('Activity · Isoform');
    });


    it("routes the sidebar and Today entry points to Tasks and Goals", async () => {
        await mountApp();
        await act(async () => fixture.sidebarProps!.onOpenAgents());
        expect(screen.getByTestId("TasksPage")).toBeInTheDocument();
        expect(localStorage.getItem("yansu.appMode")).toBe("agents");
        await act(async () => fixture.sidebarProps!.onOpenToday());
        // Goals are a group in About you now (sidebar handoff, section 5).
        await act(async () => fixture.pages.TodayPage.handlers.onOpenGoals());
        expect(screen.getByLabelText("MemoryPage selection")).toBeVisible();
        expect(localStorage.getItem("yansu.appMode")).toBe("memory");
        expect(fixture.pages.GoalsPage).toBeUndefined();
        await act(async () => fixture.sidebarProps!.onOpenToday());
        await act(async () => fixture.pages.TodayPage.handlers.onOpenAutomations());
        expect(screen.getByTestId("TasksPage")).toBeVisible();
        expect(fixture.pages.TasksPage.focus).toMatchObject({ section: "scheduled" });

        await act(async () => fixture.sidebarProps!.onOpenToday());
        await act(async () => fixture.pages.TodayPage.handlers.onOpenTasks());
        expect(screen.getByTestId("TasksPage")).toBeInTheDocument();
    });

    it("preserves a handoff selected in Search while opening Tasks", async () => {
        await mountApp();
        await act(async () => fixture.pages.SearchDialog.actions.openHandoff("selected-work"));
        expect(fixture.pages.TasksPage.openHandoffId).toBe("selected-work");
        expect(localStorage.getItem("yansu.appMode")).toBe("agents");
        await act(async () => fixture.sidebarProps!.onOpenAgents());
        expect(fixture.pages.TasksPage.openHandoffId).toBeNull();
    });

    it("maps restored Handoffs state to Tasks", async () => {
        localStorage.setItem("yansu.appMode", "handoffs");
        render(<App />);
        expect(await screen.findByTestId("TasksPage")).toBeInTheDocument();
    });

    it('opens on Today at every launch, whatever page was open last', async () => {
        localStorage.setItem('yansu.appMode', 'activity');
        render(<App />);
        expect(await screen.findByTestId('TodayPage')).toBeInTheDocument();
        expect(screen.queryByTestId('ActivityPage')).toBeNull();
        expect(screen.queryByTestId('stream')).toBeNull();
        // A later switch still records the page, as before.
        await act(async () => { fixture.sidebarProps!.onOpenActivity(); });
        expect(screen.getByTestId('ActivityPage')).toBeInTheDocument();
        expect(localStorage.getItem('yansu.appMode')).toBe('activity');
    });

    it('retains Today and chat nodes and keeps one event handler through three round trips', async () => {
        await mountApp();
        const todayNode = screen.getByTestId('TodayPage');
        const chatNode = screen.getByTestId('stream');
        const counts = new Map([...fixture.listeners].map(([name, handlers]) => [name, handlers.size]));
        for (let i = 0; i < 3; i++) {
            await act(async () => fixture.sidebarProps!.onOpenToday());
            expect(chatNode.closest('[data-retained-page]')).toHaveStyle({ display: 'none' });
            await act(async () => fixture.sidebarProps!.onOpenMainChat());
            expect(todayNode.closest('[data-retained-page]')).toHaveStyle({ display: 'none' });
        }
        expect(screen.getByTestId('TodayPage')).toBe(todayNode);
        expect(screen.getByTestId('stream')).toBe(chatNode);
        for (const [name, count] of counts) expect(fixture.listeners.get(name)?.size, name).toBe(count);
    });

    it('opens Activity as a full page from the sidebar row, on today', async () => {
        await mountApp();
        expect(screen.queryByTestId('ActivityPage')).toBeNull();
        expect(fixture.sidebarProps?.onOpenActivity).toBeTypeOf('function');
        await act(async () => { fixture.sidebarProps!.onOpenActivity(); });
        expect(screen.getByTestId('ActivityPage')).toBeInTheDocument();
        expect(fixture.pages.ActivityPage.initialDate).toBe(today());
        expect(fixture.pages.ActivityPage.section).toBe('yourDay');
        expect(localStorage.getItem('yansu.appMode')).toBe('activity');
        // The chat remains mounted and hidden while Activity is the main view.
        expect(screen.getByTestId('stream').closest('[data-retained-page]')).toHaveStyle({ display: 'none' });
    });

    it('answers in place on Activity and opens the main chat only from Open in Chat', async () => {
        await mountApp();
        await act(async () => fixture.sidebarProps!.onOpenActivity());
        expect(fixture.pages.ActivityPage.onAskAboutDate).toBeUndefined();
        expect(fixture.pages.ActivityPage.onOpenChat).toBeTypeOf('function');
        expect(localStorage.getItem('yansu.appMode')).toBe('activity');
        await act(async () => fixture.pages.ActivityPage.onOpenChat());
        expect(localStorage.getItem('yansu.appMode')).toBe('chat');
    });

    it('opens on today from the menu bar "Open Activity" event', async () => {
        await mountApp();
        await emit('activity:open');
        expect(screen.getByTestId('ActivityPage')).toBeInTheDocument();
        expect(fixture.pages.ActivityPage.initialDate).toBe(today());
    });

    it('restores the last viewed day from the sidebar and menu while Today links explicitly choose today', async () => {
        rememberActivityDate(new Date(2026, 9, 12));
        await mountApp();
        await act(async () => { fixture.sidebarProps!.onOpenActivity(); });
        expect(fixture.pages.ActivityPage.initialDate).toBe('2026-10-12');
        await emit('activity:open');
        expect(fixture.pages.ActivityPage.initialDate).toBe('2026-10-12');
        await emit('notification:clicked', 'today:x');
        await act(async () => { fixture.pages.TodayPage.handlers.onOpenActivity(); });
        expect(fixture.pages.ActivityPage.initialDate).toBe(today());
    });

    it('opens on the notification’s day for an activity-day click', async () => {
        await mountApp();
        await emit('notification:clicked', 'activity-day:2026-10-12');
        expect(screen.getByTestId('ActivityPage')).toBeInTheDocument();
        expect(fixture.pages.ActivityPage.initialDate).toBe('2026-10-12');
    });

    it('opens on today from Today’s “What you worked on” link', async () => {
        await mountApp();
        await emit('notification:clicked', 'today:x');
        expect(screen.getByTestId('TodayPage')).toBeInTheDocument();
        await act(async () => { fixture.pages.TodayPage.handlers.onOpenActivity(); });
        expect(screen.getByTestId('ActivityPage')).toBeInTheDocument();
        expect(fixture.pages.ActivityPage.initialDate).toBe(today());
    });

    it('lands a chat session link on the Screen timeline section', async () => {
        await mountApp();
        await emit('nav:open-activity-session', 'session-abc');
        expect(screen.getByTestId('ActivityPage')).toBeInTheDocument();
        expect(fixture.pages.ActivityPage.initialSessionId).toBe('session-abc');
        expect(fixture.pages.ActivityPage.section).toBe('timeline');
    });

    it.each(['workedOn', 'learned', 'timeline'])(
        'opens Today suggestions on Your day after remembering %s', async (section) => {
            await mountApp();
            await emit('activity:open');
            await act(async () => { fixture.pages.ActivityPage.onSectionChange(section); });
            await emit('notification:clicked', 'today:x');
            const target = { date: '2026-10-14', suggestionId: 'suggestion-1', action: 'follow_up' };
            await act(async () => { fixture.pages.TodayPage.handlers.onOpenActivitySuggestion(target); });
            expect(fixture.pages.ActivityPage.initialDate).toBe(target.date);
            expect(fixture.pages.ActivityPage.section).toBe('yourDay');
            expect(fixture.pages.ActivityPage.noticedFocus).toBe(target);
            await act(async () => { fixture.pages.ActivityPage.onNoticedFocusConsumed(target); });
            expect(fixture.pages.ActivityPage.noticedFocus).toBeNull();
        },
    );

    it('keeps a newer suggestion focus when an older request is consumed', async () => {
        await mountApp();
        await emit('notification:clicked', 'today:x');
        const target = { date: '2026-10-14', suggestionId: 'routine-1', action: 'routine' };
        await act(async () => { fixture.pages.TodayPage.handlers.onOpenActivitySuggestion(target); });
        expect(fixture.pages.ActivityPage.section).toBe('yourDay');
        await act(async () => { fixture.pages.ActivityPage.onNoticedFocusConsumed({ ...target, suggestionId: 'old' }); });
        expect(fixture.pages.ActivityPage.noticedFocus).toBe(target);
        await emit('activity:open');
        expect(fixture.pages.ActivityPage.initialDate).toBe(today());
        expect(fixture.pages.ActivityPage.noticedFocus).toBeNull();
    });

    it.each(['MemoryPage', 'KnowledgePage', 'PeoplePage', 'SettingsPage'])(
        'returns to the same selected entry or run in %s', async (page) => {
            await mountApp();
            if (page === 'MemoryPage') await emit('notification:clicked', 'memory:entry-2');
            if (page === 'KnowledgePage') await emit('notification:clicked', 'knowledge:entry-2');
            if (page === 'PeoplePage') await act(async () => fixture.sidebarProps!.onOpenYou('people'));
            if (page === 'SettingsPage') await emit('notification:clicked', 'settings:activity-pipeline');
            const field = screen.getByRole('textbox', { name: `${page} selection` });
            fireEvent.change(field, { target: { value: 'Selected entry, history, draft or expanded run' } });
            await act(async () => {
                if (page === 'SettingsPage') fixture.pages[page].onJumpToActivity('session-1', [], 77, ['session-1']);
                else fixture.pages[page].onOpenActivitySession('session-1');
            });
            expect(fixture.pages.ActivityPage.initialSessionId).toBe('session-1');
            expect(screen.queryByRole('textbox', { name: `${page} selection` })).toBeNull();
            await act(async () => fixture.pages.ActivityPage.backButton.onClick());
            expect(screen.getByRole('textbox', { name: `${page} selection` })).toBe(field);
            expect(field).toHaveValue('Selected entry, history, draft or expanded run');
        },
    );

    it.each(['en', 'zh-CN'])('opens Observation from a capture-paused toast in %s', async (language) => {
        await i18n.changeLanguage(language);
        await mountApp();
        await emit('notification:clicked', 'settings:activity');
        expect(screen.getByTestId('SettingsPage')).toBeInTheDocument();
        expect(fixture.pages.SettingsPage.initialTopic).toBe('activity');
    });

    it('clears People anchors before a later pipeline jump', async () => {
        await mountApp();
        await act(async () => fixture.sidebarProps!.onOpenYou('people'));
        await act(async () => fixture.pages.PeoplePage.onOpenActivitySession('person-session', 12345, 'Alice'));
        expect(fixture.pages.ActivityPage.initialEventTimestamp).toBe(12345);
        await act(async () => fixture.pages.ActivityPage.backButton.onClick());
        expect(screen.getByTestId('PeoplePage')).toBeVisible();
        // Who you know is a main view now: leaving it hides the page.
        await act(async () => fixture.sidebarProps!.onOpenToday());
        expect(screen.getByTestId('PeoplePage')).not.toBeVisible();
        await emit('notification:clicked', 'settings:activity-pipeline');
        await act(async () => fixture.pages.SettingsPage.onJumpToActivity('pipeline-session', [], 77, ['pipeline-session']));
        expect(fixture.pages.ActivityPage.initialEventTimestamp).toBeNull();
        expect(fixture.pages.ActivityPage.highlightedSenderName).toBeNull();
        expect(fixture.pages.ActivityPage.initialSessionId).toBe('pipeline-session');
    });

    it('clears a one-time People profile selection on a fresh Activity open', async () => {
        await mountApp();
        await emit('notification:clicked', 'settings:activity-pipeline');
        await act(async () => fixture.pages.SettingsPage.onOpenProfile());
        expect(fixture.pages.PeoplePage.initialSelection).toBe('self');
        await emit('activity:open');
        await act(async () => fixture.sidebarProps!.onOpenYou('people'));
        expect(fixture.pages.PeoplePage.initialSelection).toBeUndefined();
    });

    it('times the Agents place under the name its page reports (tasks)', async () => {
        await mountApp();
        pageSwitch.begun.length = 0;
        await act(async () => fixture.sidebarProps!.onOpenAgents());
        expect(pageSwitch.begun).toContain('tasks');
        expect(pageSwitch.begun).not.toContain('agents');
    });

    it('has no Library menu: the library:open event is not listened to, and nothing opens', async () => {
        await mountApp();
        // Library is gone (sidebar handoff, section 3), menu bar included.
        expect(fixture.listeners.get('library:open')?.size ?? 0).toBe(0);
        expect(screen.getByTestId('stream')).toBeInTheDocument();
        expect(localStorage.getItem('yansu.appMode')).toBe('chat');

    });

    it('gives How you do things no way to fill the main chat box (design review v1.0.0 W7, W8)', async () => {
        await mountApp();
        await emit('notification:clicked', 'knowledge:entry-2');
        expect(screen.getByTestId('KnowledgePage')).toBeInTheDocument();
        expect(fixture.pages.KnowledgePage.onPrepareChat).toBeUndefined();
        expect(fixture.prefill).not.toHaveBeenCalled();
    });

});

describe('App Search and commands', () => {
    beforeEach(async () => {
        resetPageQueries();
        localStorage.clear();
        fixture.listeners.clear();
        fixture.streamProps = null;
        fixture.sidebarProps = null;
        fixture.pages = {};
        fixture.initialProjects = [projectFixture()];
        fixture.openMainChat.mockReset().mockResolvedValue('main-1');
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

    it('passes the same resolved saved bindings to Sidebar and Search', async () => {
        localStorage.setItem('yansu.keybindings', JSON.stringify({
            'command-palette': { meta: false, ctrl: true, alt: false, shift: true, key: 'f' },
            'open-settings': { meta: false, ctrl: false, alt: true, shift: false, key: 's' },
            'keyboard-shortcuts': { meta: false, ctrl: true, alt: true, shift: false, key: 'h' },
        }));
        await mountApp();
        const shortcutFor = fixture.sidebarProps!.shortcutFor;
        expect(shortcutFor).toBeTypeOf('function');
        expect(shortcutFor).toBe(fixture.pages.SearchDialog.shortcutFor);
        const { comboToDisplayString } = await import('@/hooks/useKeybindings');
        expect(shortcutFor('command-palette')).toBe(comboToDisplayString({ meta: false, ctrl: true, alt: false, shift: true, key: 'f' }));
        expect(shortcutFor('open-settings')).toBe(comboToDisplayString({ meta: false, ctrl: false, alt: true, shift: false, key: 's' }));
        expect(shortcutFor('keyboard-shortcuts')).toBe(comboToDisplayString({ meta: false, ctrl: true, alt: true, shift: false, key: 'h' }));
    });

    it('opens the one dialog from Command K (Search is no sidebar row)', async () => {
        await mountApp();
        expect(fixture.pages.SearchDialog.open).toBe(false);
        expect(fixture.sidebarProps!.onOpenSearch).toBeUndefined();
        // Command K toggles the dialog.
        fireEvent.keyDown(window, { key: 'k', metaKey: true });
        expect(fixture.pages.SearchDialog.open).toBe(true);
        fireEvent.keyDown(window, { key: 'k', metaKey: true });
        expect(fixture.pages.SearchDialog.open).toBe(false);
    });

    it('opens a person on People with that person selected', async () => {
        await mountApp();
        await act(async () => fixture.pages.SearchDialog.actions.openPerson('person-7'));
        expect(screen.getByTestId('PeoplePage')).toBeInTheDocument();
        expect(fixture.pages.PeoplePage.initialSelection).toEqual({ personId: 'person-7' });
    });

    it('opens a goal on its story, once', async () => {
        await mountApp();
        await act(async () => fixture.pages.SearchDialog.actions.openGoal('goal-3'));
        // Goals live in About you: the goal opens there on its story.
        expect(screen.getByLabelText('MemoryPage selection')).toBeVisible();
        expect(fixture.pages.MemoryPage.openGoalId).toBe('goal-3');
        await act(async () => fixture.pages.MemoryPage.onOpenGoalHandled());
        expect(fixture.pages.MemoryPage.openGoalId).toBeNull();
        expect(fixture.pages.GoalsPage).toBeUndefined();
    });

    it('opens a searched entry once: leaving forgets it, so a plain open shows the list', async () => {
        await mountApp();
        await act(async () => fixture.pages.SearchDialog.actions.openKnowHow('entry-4'));
        expect(fixture.pages.KnowledgePage.initialSelectedId).toBe('entry-4');
        await act(async () => fixture.sidebarProps!.onOpenToday());
        expect(screen.getByTestId('KnowledgePage')).not.toBeVisible();
        await act(async () => fixture.sidebarProps!.onOpenYou('howYouDo'));
        expect(fixture.pages.KnowledgePage.initialSelectedId).toBeNull();

        await act(async () => fixture.pages.SearchDialog.actions.openAboutYou('memory-5'));
        expect(fixture.pages.MemoryPage.initialSelectedId).toBe('memory-5');
        await act(async () => fixture.sidebarProps!.onOpenToday());
        await act(async () => fixture.sidebarProps!.onOpenYou('about'));
        expect(fixture.pages.MemoryPage.initialSelectedId).toBeNull();

        // The notification path forgets its entry the same way.
        await act(async () => fixture.sidebarProps!.onOpenToday());
        await emit('notification:clicked', 'knowledge:entry-2');
        expect(fixture.pages.KnowledgePage.initialSelectedId).toBe('entry-2');
        await act(async () => fixture.sidebarProps!.onOpenToday());
        await act(async () => fixture.sidebarProps!.onOpenYou('howYouDo'));
        expect(fixture.pages.KnowledgePage.initialSelectedId).toBeNull();
    });

    it("routes Search's page rows to Library's new homes, plus know-how, About you and a schedule", async () => {
        await mountApp();
        const actions = fixture.pages.SearchDialog.actions;
        await act(async () => actions.openLibraryPage('tasks'));
        expect(screen.getByTestId('TasksPage')).toBeVisible();
        await act(async () => actions.openLibraryPage('automations'));
        expect(screen.getByTestId('TasksPage')).toBeVisible();
        expect(fixture.pages.TasksPage.focus).toMatchObject({ section: 'scheduled' });
        await act(async () => actions.openLibraryPage('deliverables'));
        expect(screen.getByTestId('TasksPage')).toBeVisible();
        expect(fixture.pages.TasksPage.focus).toMatchObject({ section: 'all' });
        await act(async () => actions.openLibraryPage('sops'));
        expect(screen.getByTestId('KnowledgePage')).toBeVisible();
        expect(fixture.pages.KnowledgePage.openPlaybookId).toBeNull();
        await act(async () => actions.openGoal(''));
        expect(screen.getByLabelText('MemoryPage selection')).toBeVisible();
        await act(async () => actions.openKnowHow('entry-4'));
        expect(fixture.pages.KnowledgePage.initialSelectedId).toBe('entry-4');
        await act(async () => actions.openAboutYou('memory-5'));
        expect(screen.getByTestId('KnowledgePage')).not.toBeVisible();
        expect(fixture.pages.MemoryPage.initialSelectedId).toBe('memory-5');
        await act(async () => actions.openAutomation('job-9'));
        expect(screen.getByTestId('TasksPage')).toBeVisible();
        expect(fixture.pages.TasksPage.focus).toMatchObject({ section: 'scheduled' });
        for (const page of ['GoalsPage', 'DeliverablesPage']) expect(fixture.pages[page]).toBeUndefined();
    });

    it('opens a topic beside Today, as a pinned topic does', async () => {
        await mountApp();
        const topic = { id: 'topic-2', name: 'Halcyon ERP sync', kind: 'work', pinned: false };
        await act(async () => fixture.pages.SearchDialog.actions.openTopic(topic));
        expect(screen.getByTestId('TodayPage')).toBeInTheDocument();
        expect(fixture.pages.TodayPage.topicRequest).toMatchObject({ topic });
    });

    it('opens a menu bar waiting item beside Chat and closes it with Escape', async () => {
        const session = fixture.initialProjects[0].workspaces[0].sessions[0];
        session.items.push({ id: 'waiting-1', kind: 'system', toolName: 'main_chat_proactive',
            toolInput: { momentId: 'moment-1', kind: 'waiting_on_reply', headline: 'A reply is waiting.', body: 'Review the draft.', state: 'pending', actionIds: [], actionLabels: {} } });
        binding('FindStreamItemCursor').mockResolvedValue(5);
        await mountApp();
        await act(async () => {
            for (const listener of fixture.listeners.get('menubar:waiting') ?? []) {
                (listener as (...args: string[]) => unknown)('main-1', 'waiting-1');
            }
        });
        const panel = screen.getByRole('complementary', { name: 'Item' });
        expect(within(panel).getByText('A reply is waiting.')).toBeInTheDocument();
        expect(screen.getByTestId('stream')).toBeInTheDocument();
        fireEvent.keyDown(window, { key: 'Escape' });
        expect(screen.queryByRole('complementary', { name: 'Item' })).toBeNull();
    });

    // Polish sweep 2026-10-27: Make my brief now from the menu bar was
    // silent during cooldown and while a brief was being made.
    it('answers Make my brief now from the menu bar', async () => {
        await mountApp();
        const fire = async (...args: string[]) => act(async () => {
            for (const listener of fixture.listeners.get('menubar:make-brief') ?? []) {
                (listener as (...a: string[]) => unknown)(...args);
            }
        });
        vi.mocked(toast).mockClear();
        await fire('cooldown', new Date(Date.now() + 12 * 60_000).toISOString());
        expect(vi.mocked(toast)).toHaveBeenCalledWith(expect.stringMatching(/12/));
        await fire('running', '');
        expect(screen.getByTestId('TodayPage')).toBeInTheDocument();
        await fire('failed', '');
        expect(vi.mocked(toast.error)).toHaveBeenCalled();
    });

    it('opens a chat message: loads back to it with no gap and scrolls there', async () => {
        const base = projectFixture();
        const session = base.workspaces[0].sessions[0];
        fixture.initialProjects = [{
            ...base,
            workspaces: [{ ...base.workspaces[0], sessions: [{ ...session, hasMore: true, nextCursor: 4 } as typeof session] }],
        }];
        binding('FindStreamItemCursor').mockResolvedValue(2);
        binding('LoadStreamItemsPage').mockImplementation(async (_id: string, cursor: number) => cursor === 4
            ? { items: [
                { id: 'item-9', kind: 'user', content: 'Move the Halcyon sync up?', sortOrder: 1 },
                { id: 'item-10', kind: 'assistant', content: 'Yes.', sortOrder: 3 },
            ], hasMore: false, nextCursor: 1 }
            : { items: [] });
        await mountApp();
        await act(async () => fixture.pages.SearchDialog.actions.openChatMessage('main-1', 'item-9'));
        expect(binding('FindStreamItemCursor')).toHaveBeenCalledWith('main-1', 'item-9');
        // Everything between the oldest loaded row and the message, plus a
        // little above it.
        expect(binding('LoadStreamItemsPage')).toHaveBeenCalledWith('main-1', 4, 13);
        expect(fixture.streamProps!.items.map((item) => item.id)).toEqual(['item-9', 'item-10', 'status-1']);
        expect((fixture.streamProps as any).focusItem).toMatchObject({ itemId: 'item-9' });
    });

    it('opens an answer-ready notification at its chat message', async () => {
        const base = projectFixture();
        const session = base.workspaces[0].sessions[0];
        fixture.initialProjects = [{
            ...base,
            workspaces: [{ ...base.workspaces[0], sessions: [{ ...session, hasMore: true, nextCursor: 4 } as typeof session] }],
        }];
        binding('FindStreamItemCursor').mockResolvedValue(2);
        binding('LoadStreamItemsPage').mockImplementation(async (_id: string, cursor: number) => cursor === 4
            ? { items: [
                { id: 'answer-post', kind: 'assistant', content: 'The result is in.', sortOrder: 3 },
            ], hasMore: false, nextCursor: 1 }
            : { items: [] });
        await mountApp();
        await emit('notification:clicked', 'chat-message:main-1:answer-post');
        expect(binding('FindStreamItemCursor')).toHaveBeenCalledWith('main-1', 'answer-post');
        expect((fixture.streamProps as any).focusItem).toMatchObject({ itemId: 'answer-post' });
    });
});
