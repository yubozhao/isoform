// The Tasks walk (hub item 6): the real Tasks page over an in-memory backend
// that moves tasks the way Go does (active, in_progress, then completed,
// blocked or aborted). Test data only; nothing reaches the desktop app, its
// browser bridge or ~/.isoform-agent.
import React from 'react';
import { createRoot } from 'react-dom/client';
import '@/index.css';
import { installFixture, reads } from './launch-demo/fixture';

type Task = { id: string; title: string; status: string; updatedAt: number; body: string; blockedReason?: string; stoppedBy?: string; chatSessionId?: string };
const RUN_MS = 1500;
let tasks: Task[] = [];
let next = 0;
const emit = (name: string, payload?: unknown) => (window as any).fixtureEmit(name, payload);
const changed = () => { emit('handoff:updated'); emit('today:needs-changed'); };
const set = (id: string, patch: Partial<Task>) => {
    tasks = tasks.map((task) => task.id === id ? { ...task, ...patch, updatedAt: Date.now() } : task);
    changed();
};

localStorage.setItem('i18nextLng', 'en');
installFixture();
Object.assign(reads, {
    ListHandoffs: () => tasks,
    GetNeedsYou: () => {
        const items = tasks.filter((task) => ['active', 'needs_review', 'blocked'].includes(task.status)).map((task) => ({
            id: task.id, kind: task.status === 'blocked' ? 'work_blocked' : 'review', sentence: task.title, createdAt: 0, targetKind: 'handoff', targetId: task.id,
        }));
        return { items, total: items.length };
    },
    ListDesktopTaskThreads: () => ({ tasks: [] }),
    ListTaskSuggestions: [], ListOpenAnswers: [], ListDeliverables: [], ListSOPs: [],
    GetPlaybooksState: { plans: [], channels: {}, defaultChannels: [] },
    FindMoreTaskSuggestions: { offers: [] },
    GetFirstWeekDay: 12,
    ParseBrainDump: (text: string) => ({ items: text.split('\n').map((line) => line.trim()).filter(Boolean).map((title, i) => ({ id: `item-${i}`, title, who: 'claude_code' })) }),
    AddBrainDump: (_key: string, items: { title: string }[]) => {
        const ids = items.map((item) => {
            const id = `task-${++next}`;
            tasks = [...tasks, { id, title: item.title, status: 'active', updatedAt: Date.now(), body: item.title }];
            return id;
        });
        emit('handoff:created');
        changed();
        return ids;
    },
    AcceptHandoff: (id: string) => ({ handoffId: id, sessionId: `chat-${id}` }),
    AbortHandoff: (id: string) => set(id, { status: 'aborted', stoppedBy: 'person' }),
});

// Go's execution path: the accepted task runs, then finishes. A title that
// asks to fail stops on a sign-in the person has to give.
async function onStartHandoff(result: { handoffId: string; sessionId: string }) {
    const id = result.handoffId;
    set(id, { status: 'in_progress', chatSessionId: result.sessionId });
    setTimeout(() => {
        const task = tasks.find((row) => row.id === id);
        if (!task || task.status !== 'in_progress') return;
        if (/fail/i.test(task.title)) set(id, { status: 'blocked', blockedReason: 'Needs your sign-in to the export site.' });
        else if (!/long/i.test(task.title)) set(id, { status: 'completed' });
    }, RUN_MS);
    return true;
}

const { default: i18n } = await import('@/i18n');
await i18n.changeLanguage('en');
const { TasksPage } = await import('@/components/tasks/TasksPage');
createRoot(document.getElementById('root')!).render(
    <main className="h-screen bg-background">
        <TasksPage onStartHandoff={onStartHandoff} onStartInPlace={onStartHandoff} upcoming={{ items: [], loading: false }} />
    </main>,
);
