import { act, renderHook } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { Project } from '@/types';
import { createSession } from '@/lib/stream-helpers';
import i18n from '@/i18n';
import { useProjects } from './useProjects';

const mocks = vi.hoisted(() => ({ bindings: new Map<string, ReturnType<typeof vi.fn>>(), setError: vi.fn() }));
vi.mock('@/lib/wails-shim-app', () => new Proxy({}, {
    has: () => true,
    get: (_target, name: string) => {
        if (name === 'then') return undefined;
        if (!mocks.bindings.has(name)) mocks.bindings.set(name, vi.fn().mockResolvedValue(undefined));
        return mocks.bindings.get(name);
    },
}));
vi.mock('../../wailsjs/runtime/runtime', () => ({ EventsOn: () => () => {} }));
const send = vi.hoisted(() => vi.fn());
vi.mock('@/hooks/useComposerInput', () => ({ sendFirstMessage: vi.fn(), sendSessionPrompt: send }));
vi.mock('@/lib/notify', () => ({ notifyImportant: vi.fn() }));
vi.mock('sonner', () => ({ toast: { error: vi.fn() } }));

function binding(name: string) {
    if (!mocks.bindings.has(name)) mocks.bindings.set(name, vi.fn());
    return mocks.bindings.get(name)!;
}

async function mountProjects() {
    const session = {
        ...createSession('model', 'workspace'), localId: 'work-chat', sessionId: 'agent-session',
        items: [{ id: 'ask', type: 'user', content: 'Old question', sortOrder: 600 }],
        itemsLoaded: true, hasMore: false, nextCursor: 0,
    };
    const project = {
        id: 'project', name: 'Project', path: '', activeWorkspaceId: 'workspace', activeSessionId: session.localId,
        workspaces: [{ id: 'workspace', isDefault: true, activeSessionId: session.localId, sessions: [session] }],
    } as unknown as Project;
    const renderer = renderHook(() => useProjects({ model: 'model', setModel: vi.fn(), setError: mocks.setError }));
    await act(async () => {
        renderer.result.current.setProjects([project]);
        renderer.result.current.setActiveProjectId(project.id);
    });
    return renderer;
}

describe('a failed edit after the rewind', () => {
    beforeEach(async () => {
        await i18n.changeLanguage('en');
        for (const mock of mocks.bindings.values()) mock.mockReset().mockResolvedValue(undefined);
        binding('DetectInstalledApps').mockResolvedValue([]);
        send.mockReset().mockRejectedValue(new Error('offline'));
    });

    it('shows the stored rows and keeps older pages reachable', async () => {
        binding('LoadStreamItemsPage').mockRejectedValueOnce(new Error('busy')).mockResolvedValueOnce({
            items: [{ id: 'ask', type: 'user', content: 'New question', sortOrder: 600 }],
            hasMore: true,
            nextCursor: 101,
        });
        const renderer = await mountProjects();
        let ok: boolean | undefined;
        await act(async () => { ok = await renderer.result.current.handleEditMessage('ask', 600, 'New question'); });
        expect(ok).toBe(false);
        const session = renderer.result.current.projects[0].workspaces[0].sessions[0];
        expect(session.items.map((item) => [item.id, item.content])).toEqual([['ask', 'Old question']]);
        expect(session.hasMore).toBe(true);
        expect(session.nextCursor).toBe(101);
        expect(session.streaming).toBe(false);
        renderer.unmount();
    });
});
