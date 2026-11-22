import { resetPageQueries } from '@/lib/page-queries';
import { resetActivitySessionCache } from '@/components/timeline/activity-session-cache';
import { act, cleanup, render, screen } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { Project, StreamItem } from '@/types';
import i18n from '@/i18n';

// A clean-profile first run (computer setup v1.0.0 as the default, hub
// 2026-11-21): the production App with no saved onboarding state mounts
// setup v3, a failed version read is v3 too (the Go default), the rollback
// (GetSetupVersion 2, ISOFORM_SETUP_V3=0) mounts setup v2, and Finish setup
// lands on Today. The harness is App.activity-nav.test.tsx's: only unrelated
// feature surfaces and native I/O are replaced.

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
vi.mock('@/components/onboarding-screen', () => ({ OnboardingScreen: () => <output data-testid="setup-v2" /> }));
vi.mock('@/components/onboarding/v3/SetupV3', () => ({ SetupV3: (props: { onFinish: () => void }) => {
    fixture.pages.SetupV3 = props;
    return <output data-testid="setup-v3" />;
} }));
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
vi.mock('@/components/agents/AgentsPage', () => ({ AgentsPage: (props: any) => {
    fixture.pages.AgentsPage = props;
    return <output data-testid="AgentsPage" />;
} }));
vi.mock('@/components/deliverables/DeliverablesPage', () => ({ DeliverablesPage: (props: any) => {
    fixture.pages.DeliverablesPage = props;
    return <output data-testid="DeliverablesPage" />;
} }));

import App from './App';

function projectFixture(): Project {
    return {
        id: 'project-1', name: 'Example', path: '', activeWorkspaceId: 'workspace-1',
        workspaces: [{ id: 'workspace-1', isDefault: true, activeSessionId: '', sessions: [] }],
    } as unknown as Project;
}

function binding(name: string) {
    if (!fixture.bindings.has(name)) fixture.bindings.set(name, vi.fn());
    return fixture.bindings.get(name)!;
}

describe('App first run', () => {
    beforeEach(async () => {
        resetPageQueries();
        resetActivitySessionCache();
        localStorage.clear();
        fixture.listeners.clear();
        fixture.sidebarProps = null;
        fixture.pages = {};
        fixture.initialProjects = [projectFixture()];
        fixture.openMainChat.mockReset().mockResolvedValue('main-1');
        for (const mock of fixture.bindings.values()) mock.mockReset().mockResolvedValue({});
        // A clean profile: nothing saved, onboarding state included.
        binding('LoadUIState').mockResolvedValue('');
        binding('LoadAllProjects').mockResolvedValue(null);
        binding('LoadActiveProjectID').mockResolvedValue('');
        binding('ListAvailableModels').mockResolvedValue([{ default: 'codex', models: [{ id: 'codex' }] }]);
        binding('GetVersion').mockResolvedValue('test');
        binding('LoadStreamItemsPage').mockResolvedValue({ items: [] });
        await i18n.changeLanguage('en');
    });
    afterEach(cleanup);

    it('mounts setup v3 on a clean profile', async () => {
        binding('GetSetupVersion').mockResolvedValue(3);
        render(<App />);
        expect(await screen.findByTestId('setup-v3')).toBeTruthy();
        expect(screen.queryByTestId('setup-v2')).toBeNull();
    });

    it('mounts setup v3 when the version read fails, matching the Go default', async () => {
        binding('GetSetupVersion').mockRejectedValue(new Error('no binding'));
        render(<App />);
        expect(await screen.findByTestId('setup-v3')).toBeTruthy();
    });

    it('mounts setup v2 on the rollback switch', async () => {
        binding('GetSetupVersion').mockResolvedValue(2);
        render(<App />);
        expect(await screen.findByTestId('setup-v2')).toBeTruthy();
        expect(screen.queryByTestId('setup-v3')).toBeNull();
    });

    it('lands on Today after Finish setup', async () => {
        binding('GetSetupVersion').mockResolvedValue(3);
        render(<App />);
        await screen.findByTestId('setup-v3');
        await act(async () => {
            fixture.pages.SetupV3.onFinish();
        });
        expect(await screen.findByTestId('TodayPage')).toBeTruthy();
        expect(binding('SaveUIState')).toHaveBeenCalledWith('yansu.onboarding.state', expect.stringContaining('"completed":true'));
    });
});
