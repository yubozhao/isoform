import { useCallback, useEffect, useRef } from 'react';
import { reportChatPlaceFailure } from '@/lib/chat-failure';
import { useTranslation } from 'react-i18next';
import { PageEventsOn as EventsOn, LoadStreamItemsPage } from '@/lib/page-queries';
import { OpenExternalURL } from '@/lib/wails-shim-app';
import { loadMainChatItemUpdates, openMainChatTarget, type MainChatItemsChanged, type TodayBriefFocus } from '@/lib/main-chat-navigation';
import { mergeAppendedItems, type MainChatReportTarget } from '@/lib/stream-helpers';
import type { Project, Session } from '@/types';

interface MainChatReportsOptions {
    projects: Project[];
    updateProject: (id: string, updater: (project: Project) => Project) => void;
    activeSessionId: string | null;
    markSessionUnread: (id: string) => void;
    openSession: (id: string) => void;
    openHandoff: (id: string) => void;
    openPlaybook: (id: string) => void;
    setPendingInsightKnowledgeId: (id: string) => void;
    setKnowledgeOpen: (open: boolean) => void;
    setPendingInsightMemoryId: (id: string) => void;
    setMemoryOpen: (open: boolean) => void;
    /** Opens Tasks scrolled to Scheduled. */
    openScheduled: () => void;
    openTasksUpNext: () => void;
    openToday: (briefId?: string, focus?: TodayBriefFocus) => void;
    setAutomationsSelectedJobId: (id: string | null) => void;
    openSettings: (topic?: 'activity' | 'notifications') => void;
    /** Opens the Activity page on today (the old Calendar report target lands
     *  here now that Calendar and Activity are one page). */
    openActivity: () => void;
    setPeopleInitialSelection: (selection: 'self' | undefined) => void;
    setPeopleOpen: (open: boolean) => void;
    /** Opens the Activity page's "What you worked on" for an activity thread. */
    openThread: (id: string) => void;
    /** Opens the Activity page on one session (a chat source's screen time or call). */
    openActivitySession: (id: string) => void;
    /** Opens the Activity page on the watch's conversations. */
    openWatch: () => void;
    /** Opens one watch conversation in Activity › Watch, on its day (YYYY-MM-DD). */
    openActivityConversation?: (day: string, conversationId: string) => void;
}

/** App's native report-event subscription and navigation adapter. Keep the
 * subscription stable while reading the latest project state and handlers. */
export function useMainChatReports(options: MainChatReportsOptions) {
    const current = useRef(options);
    current.current = options;
    const { t } = useTranslation('chat');

    useEffect(() => {
        const unsub = EventsOn('session:items-appended', async (data: MainChatItemsChanged | null) => {
            const sessionId = data?.sessionId;
            if (!sessionId) return;
            let owner: { projectId: string; session: Session } | null = null;
            for (const project of current.current.projects) {
                for (const workspace of project.workspaces) {
                    const session = workspace.sessions.find((candidate) => candidate.localId === sessionId);
                    if (session) {
                        owner = { projectId: project.id, session };
                        break;
                    }
                }
                if (owner) break;
            }
            if (!owner) return;
            if (owner.session.itemsLoaded) {
                try {
                    const fresh = await loadMainChatItemUpdates(sessionId, owner.session.items, data, LoadStreamItemsPage);
                    current.current.updateProject(owner.projectId, (project) => ({
                        ...project,
                        workspaces: project.workspaces.map((workspace) => ({
                            ...workspace,
                            sessions: workspace.sessions.map((session) => {
                                if (session.localId !== sessionId) return session;
                                const items = mergeAppendedItems(session.items, fresh);
                                if (items === session.items) return session;
                                return { ...session, items, streamItemCount: Math.max(session.streamItemCount, items.length), updatedAt: Date.now() };
                            }),
                        })),
                    }));
                } catch (error) {
                    console.error('[main-chat] reload after append failed:', error);
                }
            } else {
                current.current.updateProject(owner.projectId, (project) => ({
                    ...project,
                    workspaces: project.workspaces.map((workspace) => ({
                        ...workspace,
                        sessions: workspace.sessions.map((session) => session.localId !== sessionId ? session : {
                            ...session, streamItemCount: session.streamItemCount + 1, updatedAt: Date.now(),
                        }),
                    })),
                }));
            }
            if (current.current.activeSessionId !== sessionId) current.current.markSessionUnread(sessionId);
        });
        return () => unsub();
    }, []);

    return useCallback((target: MainChatReportTarget, onError?: () => void) => {
        const app = current.current;
        void openMainChatTarget(target, {
            openSession: app.openSession,
            openHandoff: app.openHandoff,
            openKnowledge: (id) => {
                if (id) app.setPendingInsightKnowledgeId(id);
                app.setKnowledgeOpen(true);
            },
            openMemory: (id) => {
                app.setPendingInsightMemoryId(id);
                app.setMemoryOpen(true);
            },
            openAutomation: (id) => {
                app.openScheduled();
                if (id !== undefined) app.setAutomationsSelectedJobId(id);
            },
            openToday: app.openToday,
            openTasksUpNext: app.openTasksUpNext,
            openSettings: app.openSettings,
            openPlaybook: app.openPlaybook,
            openCalendar: app.openActivity,
            openPeople: () => {
                app.setPeopleInitialSelection(undefined);
                app.setPeopleOpen(true);
            },
            openThread: app.openThread,
            openActivitySession: app.openActivitySession,
            openWatch: app.openWatch,
            openActivityConversation: app.openActivityConversation,
            openExternalURL: OpenExternalURL,
        }).catch(() => { if (onError) onError(); else reportChatPlaceFailure({ key: 'failure.openIt' }); });
    }, [t]);
}
