import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import {
    SelectWorkDir,
    FetchProjectContext,
    DetectProject,
    CancelStream,
    CancelByTag,
    RespondToPermission,
    CancelPermission,
    SaveProjectState,
    DeleteProjectData,
    SaveSessionState,
    DeleteSessionData,
    PersistStreamItem,
    SaveActiveProjectID,
    UpdateProjectActiveSession,
    UpdateProjectActiveWorkspace,
    UpdateWorkspaceActiveSession,
    SaveWorkspaceState,
    CreateWorkspace as CreateWorkspaceBackend,
    RemoveWorkspace,
    MergeWorkspace,
    GetGitInfo,
    PullKnowledge,
    PushKnowledge,
    OpenProjectURL,
    OpenProjectFolder,
    OpenInApp,
    DetectInstalledApps,
    RewindSession,
    UpdateStreamItemContent,
    ReorderProjects,
    ReorderWorkspaces,
    NextProjectSortOrder,
    PrewarmSession,
    LoadUIState,
    SaveUIState,
} from "@/lib/wails-shim-app";
import { loadStreamItemsPageWithOpenAnswerDelivery } from "@/lib/open-answer-delivery";
import type {
    Project,
    ProjectContext,
    StreamItem,
    Session,
    InstalledApp,
} from "@/types";
import {
    buildId,
    createStandaloneChatProject,
    createSession,
    createWorkspace,
    fromStreamItemData,
    getExecutorType,
    splitModelEffort,
    STANDALONE_CHAT_PROJECT_ID,
    STANDALONE_CHAT_WORKSPACE_ID,
    toProjectData,
    toSessionData,
    toWorkspaceData,
    toStreamItemData,
    logPersistError,
    getCurrentUILanguage,
    convertSessionFromStorage,
    findMainChatSession,
    isMainChatSession,
    isSideChatSession,
} from '@/lib/stream-helpers';
import { toast } from 'sonner';
import i18n from '@/i18n';
import { notifyImportant } from '@/lib/notify';
import { EventsOn } from '../../wailsjs/runtime/runtime';
import { randomName } from '@/lib/random-name';
import { sendFirstMessage, sendSessionPrompt } from '@/hooks/useComposerInput';
import type { HandoffAcceptResult, SopRunResult } from '@/lib/wails-shim-app';
import { ForkSession, GetMainChat } from '@/lib/wails-shim-app';
import { CHAT_FIRST_PAGE_SIZE } from '@/lib/chat-page-size';

const UNREAD_SESSION_IDS_KEY = 'yansu.unreadSessionIds.v1';

// ── Workspace-aware utility functions ──

/** Find a session across all workspaces in a project. */
function findSessionInProject(
    p: Project,
    sessionId: string,
): Session | undefined {
    for (const w of p.workspaces) {
        const s = w.sessions.find((s) => s.localId === sessionId);
        if (s) return s;
    }
    return undefined;
}

/** Map over a specific session within the workspace hierarchy. */
function mapSessionInProject(
    p: Project,
    sessionId: string,
    updater: (s: Session) => Session,
): Project {
    return {
        ...p,
        workspaces: p.workspaces.map((w) => ({
            ...w,
            sessions: w.sessions.map((s) =>
                s.localId === sessionId ? updater(s) : s,
            ),
        })),
    };
}

/** A delayed answer must not clear a newer request's controls. */
function mapPermissionSessionInProject(
    project: Project,
    sessionId: string,
    requestId: string | undefined,
    updater: (session: Session) => Session,
): Project {
    return mapSessionInProject(project, sessionId, (session) =>
        session.permissionRequestId === requestId ? updater(session) : session,
    );
}

function removeWorkspaceFromProject(
    p: Project,
    workspaceId: string,
): Project {
    const removedWorkspace = p.workspaces.find((w) => w.id === workspaceId);
    const remaining = p.workspaces.filter((w) => w.id !== workspaceId);
    const fallbackWorkspace =
        remaining.find((w) => w.isDefault) ?? remaining[0];
    const removedActiveSession =
        removedWorkspace?.sessions.some((s) => s.localId === p.activeSessionId) ??
        false;
    const shouldSwitchWorkspace =
        p.activeWorkspaceId === workspaceId || removedActiveSession;

    return {
        ...p,
        workspaces: remaining,
        activeWorkspaceId: shouldSwitchWorkspace
            ? (fallbackWorkspace?.id ?? null)
            : p.activeWorkspaceId,
        activeSessionId: shouldSwitchWorkspace
            ? (fallbackWorkspace?.activeSessionId ??
                fallbackWorkspace?.sessions[0]?.localId ??
                null)
            : p.activeSessionId,
    };
}

function localizedPrompt(en: string, zhCN: string): string {
    return getCurrentUILanguage() === 'zh-CN' ? zhCN : en;
}

/** How handleStartHandoffExecution treats a chat that already has a run. */
export interface HandoffStartOptions {
    /**
     * Send the prompt again even when the chat already holds an earlier run.
     * Only Start again on blocked or needs-review work sets it: accepting
     * that work reuses the chat, so the default already-started check would
     * see the earlier run and send nothing.
     */
    restart?: boolean;
}

interface UseProjectsDeps {
    model: string;
    setModel: React.Dispatch<React.SetStateAction<string>>;
    setError: (e: string | null) => void;
}

export function useProjects({ model, setModel, setError }: UseProjectsDeps) {
    const { t } = useTranslation('chat');
    const [projects, setProjects] = useState<Project[]>([]);
    const [activeProjectId, setActiveProjectIdState] = useState<string | null>(null);
    // Explicit selections supersede deferred main-chat loads, including
    // selections made by project and workspace actions outside the sidebar.
    const selectionRevision = useRef(0);
    const getSelectionRevision = useCallback(() => selectionRevision.current, []);
    const setActiveProjectId = useCallback((value: React.SetStateAction<string | null>) => {
        selectionRevision.current += 1;
        setActiveProjectIdState(value);
    }, []);
    const [fetchingContext, setFetchingContext] = useState(false);
    const [installedApps, setInstalledApps] = useState<InstalledApp[]>([]);
    const [projectsHydrated, setProjectsHydrated] = useState(false);
    const [unreadSessionIds, setUnreadSessionIds] = useState<Set<string>>(new Set());
    const unreadSessionIdsRef = useRef<Set<string>>(new Set());

    const replaceUnreadSessionIds = useCallback(
        (next: Set<string>, persist = true) => {
            unreadSessionIdsRef.current = next;
            setUnreadSessionIds(next);
            if (persist) {
                SaveUIState(
                    UNREAD_SESSION_IDS_KEY,
                    JSON.stringify(Array.from(next)),
                ).catch(logPersistError);
            }
        },
        [],
    );

    useEffect(() => {
        let cancelled = false;
        LoadUIState(UNREAD_SESSION_IDS_KEY)
            .then((raw) => {
                if (cancelled || !raw) return;
                const parsed: unknown = JSON.parse(raw);
                if (!Array.isArray(parsed)) return;
                const ids = parsed.filter(
                    (value): value is string => typeof value === 'string',
                );
                const merged = new Set(ids);
                for (const id of unreadSessionIdsRef.current) merged.add(id);
                replaceUnreadSessionIds(merged, false);
            })
            .catch(logPersistError);
        return () => {
            cancelled = true;
        };
    }, [replaceUnreadSessionIds]);

    useEffect(() => {
        DetectInstalledApps()
            .then((apps) => setInstalledApps(apps as InstalledApp[]))
            .catch(() => {});
    }, []);

    const activeProject = useMemo(
        () => projects.find((p) => p.id === activeProjectId) ?? null,
        [projects, activeProjectId],
    );

    const activeWorkspace = useMemo(
        () =>
            activeProject?.workspaces.find(
                (w) => w.id === activeProject.activeWorkspaceId,
            ) ??
            activeProject?.workspaces[0] ??
            null,
        [activeProject],
    );

    const activeSession = useMemo(
        () =>
            activeWorkspace?.sessions.find(
                (s) => s.localId === activeWorkspace.activeSessionId,
            ) ??
            activeWorkspace?.sessions[0] ??
            null,
        [activeWorkspace],
    );

    useEffect(() => {
        if (!projectsHydrated) return;

        // Use functional updater to atomically check-and-create.
        // This eliminates the TOCTOU race between reading `projects`
        // from the closure and the actual state at update time, and is
        // safe under React StrictMode (double-fire on mount).
        let created: ReturnType<typeof createStandaloneChatProject> | null = null;
        setProjects((prev) => {
            if (prev.some((p) => p.id === STANDALONE_CHAT_PROJECT_ID)) return prev;
            created = createStandaloneChatProject(model);
            return [created, ...prev];
        });
        if (created) {
            SaveProjectState(toProjectData(created) as any).catch(
                logPersistError,
            );
        }
    }, [projectsHydrated, model]);

    useEffect(() => {
        if (projects.length === 0) return;
        const activeStillExists =
            !!activeProjectId &&
            projects.some((p) => p.id === activeProjectId);
        if (activeStillExists) return;

        const fallbackId =
            projects.find((p) => p.id === STANDALONE_CHAT_PROJECT_ID)?.id ??
            projects[0].id;
        setActiveProjectId(fallbackId);
        SaveActiveProjectID(fallbackId).catch(logPersistError);
    }, [projects, activeProjectId]);

    const updateProject = useCallback(
        (projectId: string, updater: (p: Project) => Project) => {
            setProjects((prev) =>
                prev.map((p) => (p.id === projectId ? updater(p) : p)),
            );
        },
        [],
    );

    // Backend-initiated session deletion (e.g. cleanupCronJobChatSession when a
    // scheduled automation is deleted) emits "session:deleted" with the session
    // local id. Mirror it into the in-memory `projects` state so the Chat list
    // stops showing the now-orphaned "Scheduled: …" row that the user can
    // otherwise still click — which then fails with "session not found" because
    // the backend session is already gone.
    useEffect(() => {
        const unsub = EventsOn('session:deleted', (sessionLocalId: string) => {
            if (!sessionLocalId) return;
            setProjects((prev) =>
                prev.map((p) => ({
                    ...p,
                    workspaces: p.workspaces.map((w) => {
                        if (!w.sessions.some((s) => s.localId === sessionLocalId)) {
                            return w;
                        }
                        const remaining = w.sessions.filter(
                            (s) => s.localId !== sessionLocalId,
                        );
                        const newActiveId =
                            w.activeSessionId === sessionLocalId
                                ? (remaining[0]?.localId ?? null)
                                : w.activeSessionId;
                        return {
                            ...w,
                            sessions: remaining,
                            activeSessionId: newActiveId,
                        };
                    }),
                    activeSessionId:
                        p.activeSessionId === sessionLocalId
                            ? null
                            : p.activeSessionId,
                })),
            );
        });
        return () => unsub();
    }, []);

    const markProjectsHydrated = useCallback(() => {
        setProjectsHydrated(true);
    }, []);

    const markSessionRead = useCallback(
        (sessionLocalId: string) => {
            const current = unreadSessionIdsRef.current;
            if (!current.has(sessionLocalId)) return;
            const next = new Set(current);
            next.delete(sessionLocalId);
            replaceUnreadSessionIds(next);
        },
        [replaceUnreadSessionIds],
    );

    const markSessionUnread = useCallback(
        (sessionLocalId: string) => {
            const current = unreadSessionIdsRef.current;
            if (current.has(sessionLocalId)) return;
            const next = new Set(current);
            next.add(sessionLocalId);
            replaceUnreadSessionIds(next);
        },
        [replaceUnreadSessionIds],
    );

    const shutdownWorkspaceSessions = useCallback(
        (proj: Project, workspaceId: string) => {
            const ws = proj.workspaces.find((w) => w.id === workspaceId);
            if (!ws) return null;

            for (const session of ws.sessions) {
                if (session.streaming) {
                    if (session.currentRunId) {
                        CancelStream(session.currentRunId);
                    } else {
                        CancelByTag(session.localId);
                    }
                }
            }

            return ws;
        },
        [],
    );

    const persistProjectSelection = useCallback(
        (previous: Project, next: Project) => {
            if (next.activeWorkspaceId !== previous.activeWorkspaceId) {
                UpdateProjectActiveWorkspace(
                    previous.id,
                    next.activeWorkspaceId ?? "",
                ).catch(logPersistError);
            }
            if (next.activeSessionId !== previous.activeSessionId) {
                UpdateProjectActiveSession(
                    previous.id,
                    next.activeSessionId ?? "",
                ).catch(logPersistError);
            }
        },
        [],
    );

    // Self-heal: ensure active project always has a usable workspace + session.
    useEffect(() => {
        if (!activeProject) return;

        const proj = activeProject;
        let needsUpdate = false;
        let updatedProject = { ...proj };

        // Case 1: project has no workspaces at all → create default workspace + session.
        if (proj.workspaces.length === 0) {
            const defaultWs = createWorkspace("Default", true);
            const firstSession = createSession(model, defaultWs.id);
            defaultWs.sessions = [firstSession];
            defaultWs.activeSessionId = firstSession.localId;
            updatedProject = {
                ...updatedProject,
                workspaces: [defaultWs],
                activeWorkspaceId: defaultWs.id,
                activeSessionId: firstSession.localId,
            };
            needsUpdate = true;
        } else {
            // Case 2: activeWorkspaceId doesn't match any workspace → fix pointer.
            const wsMatch = proj.workspaces.find(
                (w) => w.id === proj.activeWorkspaceId,
            );
            if (!wsMatch) {
                const fallbackWs = proj.workspaces[0];
                updatedProject = {
                    ...updatedProject,
                    activeWorkspaceId: fallbackWs.id,
                    activeSessionId:
                        fallbackWs.activeSessionId ??
                        fallbackWs.sessions[0]?.localId ??
                        null,
                };
                needsUpdate = true;
            }
        }

        // Case 3: activeSessionId doesn't match any session → fix pointer.
        const resolvedWs = updatedProject.workspaces.find(
            (w) => w.id === updatedProject.activeWorkspaceId,
        );
        if (resolvedWs) {
            const sessionMatch = resolvedWs.sessions.find(
                (s) => s.localId === resolvedWs.activeSessionId,
            );
            if (!sessionMatch && resolvedWs.sessions.length > 0) {
                const fallbackSession = resolvedWs.sessions[0];
                updatedProject = {
                    ...updatedProject,
                    workspaces: updatedProject.workspaces.map((w) =>
                        w.id !== resolvedWs.id
                            ? w
                            : {
                                  ...w,
                                  activeSessionId: fallbackSession.localId,
                              },
                    ),
                    activeSessionId: fallbackSession.localId,
                };
                needsUpdate = true;
            } else if (!sessionMatch && resolvedWs.sessions.length === 0) {
                // All sessions deleted — clear active pointers.
                updatedProject = {
                    ...updatedProject,
                    workspaces: updatedProject.workspaces.map((w) =>
                        w.id !== resolvedWs.id
                            ? w
                            : { ...w, activeSessionId: null },
                    ),
                    activeSessionId: null,
                };
                needsUpdate = true;
            }
        }

        if (needsUpdate) {
            updateProject(proj.id, () => updatedProject);
            SaveProjectState(toProjectData(updatedProject) as any).catch(
                logPersistError,
            );
        }
    }, [
        activeProject?.id,
        activeProject?.workspaces.length,
        activeProject?.activeWorkspaceId,
        model,
        updateProject,
    ]);

    // ── Handlers ──

    const handleOpenProject = async () => {
        try {
            const dir = await SelectWorkDir();
            if (!dir) return;

            const existing = projects.find((p) => p.path === dir);
            if (existing) {
                setActiveProjectId(existing.id);
                return;
            }

            const detected = await DetectProject(dir);
            const dirName = dir.split("/").pop() || dir;
            const projectId = buildId();

            const defaultWs = createWorkspace("Default", true);
            const firstSession = createSession(model, defaultWs.id);
            defaultWs.sessions = [firstSession];
            defaultWs.activeSessionId = firstSession.localId;

            // Detect git info for workspace features.
            let gitInfo: import("@/types").GitInfo | undefined;
            try {
                const info = await GetGitInfo(dir);
                if (info.isGitRepo) gitInfo = info as import("@/types").GitInfo;
            } catch {
                /* not a git repo */
            }

            // Get the next sort order so the new project appears at the end.
            let sortOrder = projects.length;
            try {
                sortOrder = await NextProjectSortOrder();
            } catch {
                /* fallback to list length */
            }

            const newProject: Project = {
                id: projectId,
                name: detected.project_name || dirName,
                path: dir,
                organization: detected.organization,
                product: detected.product,
                isYansuProject: detected.is_yansu_project,
                gitInfo,
                workspaces: [defaultWs],
                activeWorkspaceId: defaultWs.id,
                activeSessionId: firstSession.localId,
                sortOrder,
            };

            setProjects((prev) => [...prev, newProject]);
            setActiveProjectId(projectId);
            setError(null);

            await SaveProjectState(toProjectData(newProject) as any);
            await SaveActiveProjectID(projectId);
        } catch (err) {
            const message =
                err instanceof Error ? err.message : String(err);
            toast.error(`Failed to open project: ${message}`);
        }
    };

    const handleCloseProject = (projectId: string) => {
        if (projectId === STANDALONE_CHAT_PROJECT_ID) {
            return;
        }

        const proj = projects.find((p) => p.id === projectId);
        // A project holding the main chat cannot be closed (the store refuses
        // the cascade), so do not drop it from state either.
        if (proj && findMainChatSession([proj])) return;
        if (proj) {
            for (const ws of proj.workspaces) {
                for (const session of ws.sessions) {
                    if (session.streaming) {
                        if (session.currentRunId) {
                            CancelStream(session.currentRunId);
                        } else {
                            CancelByTag(session.localId);
                        }
                    }
                }
            }
        }
        if (activeProjectId === projectId) {
            const idx = projects.findIndex((p) => p.id === projectId);
            const remaining = projects.filter((p) => p.id !== projectId);
            const nextId =
                remaining.length > 0
                    ? (remaining[Math.max(0, idx - 1)]?.id ?? remaining[0].id)
                    : null;
            setActiveProjectId(nextId);
            SaveActiveProjectID(nextId ?? "").catch(logPersistError);
        }
        setProjects((prev) => prev.filter((p) => p.id !== projectId));
        DeleteProjectData(projectId).catch(logPersistError);
    };

    const handleSwitchProject = (projectId: string) => {
        selectionRevision.current += 1;
        setActiveProjectId(projectId);
        setError(null);
        SaveActiveProjectID(projectId).catch(logPersistError);
    };

    const handleNewSession = async (
        projectId: string,
        requirePersistence = false,
    ): Promise<string | null> => {
        const proj = projects.find((p) => p.id === projectId);
        if (!proj) return null;
        const wsId = proj.activeWorkspaceId;
        if (!wsId) return null;

        // Reuse an existing empty session (no messages) instead of creating a
        // new one. The main chat is never reused here (handleOpenMainChat opens
        // it), and neither is a side chat left from before side chats went.
        const ws = proj.workspaces.find((w) => w.id === wsId);
        const emptySession = ws?.sessions.find(
            (s) =>
                !isMainChatSession(s) &&
                !isSideChatSession(s) &&
                s.items.length === 0 &&
                s.streamItemCount === 0,
        );
        if (emptySession) {
            try {
                await UpdateWorkspaceActiveSession(wsId, emptySession.localId);
                await UpdateProjectActiveSession(projectId, emptySession.localId);
                await SaveActiveProjectID(projectId);
            } catch (err) {
                logPersistError(err);
                if (requirePersistence) {
                    return null;
                }
            }
            updateProject(projectId, (p) => ({
                ...p,
                workspaces: p.workspaces.map((w) =>
                    w.id !== wsId
                        ? w
                        : {
                              ...w,
                              activeSessionId: emptySession.localId,
                              sessions: w.sessions.map((s) =>
                                  s.localId === emptySession.localId
                                      ? { ...s, itemsLoaded: true, itemsLoading: false }
                                      : s,
                              ),
                          },
                ),
                activeSessionId: emptySession.localId,
            }));
            setActiveProjectId(projectId);
            return emptySession.localId;
        }

        const newSession = createSession(model, wsId);

        // Pre-warm the session so the runner is ready while the user types.
        PrewarmSession(proj.path || "", model, false).catch((err) => {
            console.error("PrewarmSession failed:", err);
        });

        try {
            await SaveSessionState(projectId, toSessionData(newSession) as any);
            await UpdateWorkspaceActiveSession(wsId, newSession.localId);
            await UpdateProjectActiveSession(projectId, newSession.localId);
            await SaveActiveProjectID(projectId);
        } catch (err) {
            logPersistError(err);
            if (requirePersistence) {
                return null;
            }
        }
        updateProject(projectId, (p) => ({
            ...p,
            workspaces: p.workspaces.map((w) =>
                w.id !== wsId
                    ? w
                    : {
                          ...w,
                          sessions: [...w.sessions, newSession],
                          activeSessionId: newSession.localId,
                      },
            ),
            activeSessionId: newSession.localId,
        }));
        setActiveProjectId(projectId);
        setError(null);
        return newSession.localId;
    };

    // ── Main chat and side chats ──
    //
    // The main chat is the one Isoform-owned session (kind "main") the user
    // keeps talking in. Go creates it on startup; ensureMainChatLoaded only
    // falls back to GetMainChat when the loaded state has none (a reset or a
    // stale snapshot) and splices it into the standalone project.
    const ensureMainChatLoaded = async (isCurrent: () => boolean): Promise<{
        projectId: string;
        workspaceId: string;
        session: Session;
    } | null> => {
        const found = findMainChatSession(projects);
        if (found) {
            return {
                projectId: found.project.id,
                workspaceId: found.workspace.id,
                session: found.session,
            };
        }
        try {
            const info = await GetMainChat();
            if (!isCurrent()) return null;
            const session = convertSessionFromStorage(info.session);
            const projectId = info.projectId || STANDALONE_CHAT_PROJECT_ID;
            setProjects((prev) => {
                if (!isCurrent()) return prev;
                const existing = prev.find((p) => p.id === projectId);
                if (!existing) {
                    // Fresh install before hydration: materialize the
                    // standalone project around the main chat.
                    const workspace = {
                        ...createWorkspace('General Chat', true),
                        id: info.workspaceId,
                        activeSessionId: session.localId,
                        sessions: [session],
                    };
                    const project: Project = {
                        ...createStandaloneChatProject(model),
                        id: projectId,
                        activeWorkspaceId: workspace.id,
                        activeSessionId: session.localId,
                        workspaces: [workspace],
                    };
                    return [project, ...prev];
                }
                const hasWorkspace = existing.workspaces.some((w) => w.id === info.workspaceId);
                return prev.map((p) =>
                    p.id !== projectId
                        ? p
                        : {
                              ...p,
                              workspaces: hasWorkspace
                                  ? p.workspaces.map((w) =>
                                        w.id !== info.workspaceId ||
                                        w.sessions.some((s) => s.localId === session.localId)
                                            ? w
                                            : { ...w, sessions: [session, ...w.sessions] },
                                    )
                                  : [
                                        ...p.workspaces,
                                        {
                                            ...createWorkspace('General Chat', true),
                                            id: info.workspaceId,
                                            activeSessionId: session.localId,
                                            sessions: [session],
                                        },
                                    ],
                          },
                );
            });
            return { projectId, workspaceId: info.workspaceId, session };
        } catch (err) {
            logPersistError(err);
            return null;
        }
    };

    const handleOpenMainChat = async (stillCurrent: () => boolean = () => true): Promise<string | null> => {
        const revision = ++selectionRevision.current;
        const isCurrent = () => selectionRevision.current === revision && stillCurrent();
        const main = await ensureMainChatLoaded(isCurrent);
        if (!main || !isCurrent()) return null;
        handleSwitchSession(main.projectId, main.session.localId, { isCurrent, workspaceId: main.workspaceId });
        return main.session.localId;
    };

    const automationCreationRef = useRef(false);

    const handleNewAutomationChat = async (projectId: string) => {
        if (automationCreationRef.current) return;
        automationCreationRef.current = true;

        let createdSessionLocalId: string | null = null;

        try {
            const proj = projects.find((p) => p.id === projectId);
            if (!proj) return;
            const wsId = proj.activeWorkspaceId;
            if (!wsId) return;
            const ws = proj.workspaces.find((w) => w.id === wsId);
            if (!ws) return;

            const newSession = { ...createSession(model, wsId), automationResult: true };
            createdSessionLocalId = newSession.localId;

            updateProject(projectId, (p) => ({
                ...p,
                workspaces: p.workspaces.map((w) =>
                    w.id !== wsId ? w : {
                        ...w,
                        sessions: [...w.sessions, newSession],
                        activeSessionId: newSession.localId,
                    },
                ),
                activeSessionId: newSession.localId,
            }));
            setActiveProjectId(projectId);
            setError(null);

            await SaveSessionState(projectId, toSessionData(newSession) as any);

            try {
                await UpdateWorkspaceActiveSession(wsId, newSession.localId);
                await UpdateProjectActiveSession(projectId, newSession.localId);
                await SaveActiveProjectID(projectId);
            } catch (err) {
                logPersistError(err);
            }

            await sendFirstMessage({
                project: proj,
                workspace: ws,
                session: newSession,
                promptText: localizedPrompt(
                    'I want to create a new automation',
                    '我想创建一个新的定时自动化',
                ),
                model,
                updateProject,
            });
        } catch (err) {
            if (createdSessionLocalId) {
                updateProject(projectId, (p) =>
                    mapSessionInProject(p, createdSessionLocalId!, (s) => ({
                        ...s,
                        streaming: false,
                    })),
                );
            }
            toast.error('Failed to start automation creation chat');
        } finally {
            automationCreationRef.current = false;
        }
    };


    // One start per handoff session at a time. The already-started check below
    // reads the saved stream items before sending, so two callers that start
    // the same session together (a phone approval event and the user opening
    // the item in One-time work) would both see an empty chat and both send
    // the prompt. Later callers share the first call's result, except that a
    // restart never shares an idempotent start (which may return without
    // sending): it waits for that start to settle, then sends.
    const handoffStartsInFlightRef = useRef<
        Map<string, { start: Promise<boolean>; restart: boolean }>
    >(new Map());

    const handleStartHandoffExecution = (
        handoff: HandoffAcceptResult,
        options: HandoffStartOptions = {},
    ): Promise<boolean> => {
        const restart = options.restart === true;
        const key = handoff.sessionId || handoff.handoffId;
        const inFlight = handoffStartsInFlightRef.current.get(key);
        if (inFlight && (inFlight.restart || !restart)) return inFlight.start;
        const run = () => startHandoffExecution(handoff, { restart });
        const start = (inFlight ? inFlight.start.then(run, run) : run()).finally(
            () => {
                if (handoffStartsInFlightRef.current.get(key)?.start === start) {
                    handoffStartsInFlightRef.current.delete(key);
                }
            },
        );
        handoffStartsInFlightRef.current.set(key, { start, restart });
        return start;
    };

    const startHandoffExecution = async (
        handoff: HandoffAcceptResult,
        { restart = false }: HandoffStartOptions = {},
    ): Promise<boolean> => {
        const proj = projects.find((p) => p.id === STANDALONE_CHAT_PROJECT_ID);
        if (!proj) return false;
        const ws = proj.workspaces.find((w) => w.id === STANDALONE_CHAT_WORKSPACE_ID);
        if (!ws) return false;

        const labelBase = `Handoff: ${handoff.title}`;
        const sessionLabel =
            labelBase.length > 60 ? `${labelBase.slice(0, 57)}...` : labelBase;
        const existingSession = ws.sessions.find(
            (s) => s.localId === handoff.sessionId,
        );
        const session =
            existingSession ??
            {
                ...createSession(
                    handoff.model || model,
                    STANDALONE_CHAT_WORKSPACE_ID,
                    sessionLabel,
                ),
                localId: handoff.sessionId,
            };

        try {
            updateProject(STANDALONE_CHAT_PROJECT_ID, (p) => ({
                ...p,
                workspaces: p.workspaces.map((w) =>
                    w.id !== STANDALONE_CHAT_WORKSPACE_ID
                        ? w
                        : {
                              ...w,
                              sessions: w.sessions.some(
                                  (s) => s.localId === handoff.sessionId,
                              )
                                  ? w.sessions
                                  : [...w.sessions, session],
                              activeSessionId: handoff.sessionId,
                          },
                ),
                activeSessionId: handoff.sessionId,
            }));
            setActiveProjectId(STANDALONE_CHAT_PROJECT_ID);
            setError(null);

            await SaveSessionState(
                STANDALONE_CHAT_PROJECT_ID,
                toSessionData(session) as any,
            );
            await UpdateWorkspaceActiveSession(
                STANDALONE_CHAT_WORKSPACE_ID,
                handoff.sessionId,
            );
            await UpdateProjectActiveSession(
                STANDALONE_CHAT_PROJECT_ID,
                handoff.sessionId,
            );
            await SaveActiveProjectID(STANDALONE_CHAT_PROJECT_ID);

            const page = await loadStreamItemsPageWithOpenAnswerDelivery(handoff.sessionId, -1, CHAT_FIRST_PAGE_SIZE);
            const persistedItems = (page.items ?? []).map(fromStreamItemData);
            const alreadyStarted =
                persistedItems.some(
                    (item) =>
                        item.handoffContext === true &&
                        item.handoffId === handoff.handoffId,
                ) || persistedItems.some((item) => item.kind === 'user');
            if (alreadyStarted) {
                updateProject(STANDALONE_CHAT_PROJECT_ID, (p) =>
                    mapSessionInProject(p, handoff.sessionId, (s) => ({
                        ...s,
                        items: persistedItems,
                        streamItemCount: Math.max(
                            s.streamItemCount ?? 0,
                            persistedItems.length,
                        ),
                        itemsLoaded: true,
                        itemsLoading: false,
                        hasMore: page.hasMore,
                        nextCursor: page.nextCursor,
                    })),
                );
                // A restart keeps the earlier run on screen and sends the
                // prompt again below it.
                if (!restart) return true;
            }

            await sendFirstMessage({
                project: proj,
                workspace: ws,
                session,
                promptText: handoff.prompt,
                model: handoff.model || model,
                updateProject,
                userItemFields: {
                    handoffContext: true,
                    handoffId: handoff.handoffId,
                },
                skipAutoRename: true,
                projectPathOverride: handoff.workdir,
            });
            return true;
        } catch (err) {
            updateProject(STANDALONE_CHAT_PROJECT_ID, (p) =>
                mapSessionInProject(p, handoff.sessionId, (s) => ({
                    ...s,
                    streaming: false,
                })),
            );
            toast.error(i18n.t('app:handoffToast.chatStartFailed'));
            return false;
        }
    };

    // Mirrors handleStartHandoffExecution for SOP runs: adopt the session the
    // Go side created (RunSOP), switch to it, and send the SOP prompt through
    // the normal chat send path. SOPs are reusable, so there is no
    // already-started resume check beyond an existing user turn.
    const handleStartSopExecution = async (run: SopRunResult) => {
        const proj = projects.find((p) => p.id === STANDALONE_CHAT_PROJECT_ID);
        if (!proj) return;
        const ws = proj.workspaces.find((w) => w.id === STANDALONE_CHAT_WORKSPACE_ID);
        if (!ws) return;

        const labelBase = `SOP: ${run.title}`;
        const sessionLabel =
            labelBase.length > 60 ? `${labelBase.slice(0, 57)}...` : labelBase;
        const existingSession = ws.sessions.find(
            (s) => s.localId === run.sessionId,
        );
        const session =
            existingSession ??
            {
                ...createSession(
                    run.model || model,
                    STANDALONE_CHAT_WORKSPACE_ID,
                    sessionLabel,
                ),
                localId: run.sessionId,
            };

        try {
            updateProject(STANDALONE_CHAT_PROJECT_ID, (p) => ({
                ...p,
                workspaces: p.workspaces.map((w) =>
                    w.id !== STANDALONE_CHAT_WORKSPACE_ID
                        ? w
                        : {
                              ...w,
                              sessions: w.sessions.some(
                                  (s) => s.localId === run.sessionId,
                              )
                                  ? w.sessions
                                  : [...w.sessions, session],
                              activeSessionId: run.sessionId,
                          },
                ),
                activeSessionId: run.sessionId,
            }));
            setActiveProjectId(STANDALONE_CHAT_PROJECT_ID);
            setError(null);

            await SaveSessionState(
                STANDALONE_CHAT_PROJECT_ID,
                toSessionData(session) as any,
            );
            await UpdateWorkspaceActiveSession(
                STANDALONE_CHAT_WORKSPACE_ID,
                run.sessionId,
            );
            await UpdateProjectActiveSession(
                STANDALONE_CHAT_PROJECT_ID,
                run.sessionId,
            );
            await SaveActiveProjectID(STANDALONE_CHAT_PROJECT_ID);

            await sendFirstMessage({
                project: proj,
                workspace: ws,
                session,
                promptText: run.prompt,
                model: run.model || model,
                updateProject,
                skipAutoRename: true,
            });
        } catch (err) {
            updateProject(STANDALONE_CHAT_PROJECT_ID, (p) =>
                mapSessionInProject(p, run.sessionId, (s) => ({
                    ...s,
                    streaming: false,
                })),
            );
            toast.error('Failed to start SOP chat');
        }
    };

    const handleSwitchSession = (projectId: string, sessionId: string, pending?: { isCurrent: () => boolean; workspaceId: string }) => {
        if (!pending) selectionRevision.current += 1;
        const isCurrent = pending?.isCurrent ?? (() => true);
        if (!isCurrent()) return;
        // The caller's projectId may be stale (e.g. sidebar Recent passes
        // standalone for a transcript that actually lives in proactive).
        // Resolve the real owning project by searching every project's
        // workspaces — without this, setActiveProjectId(projectId) below
        // would silently break the active session pointer when the hint is
        // wrong, which surfaces as "clicking a transcript does nothing".
        const ownerProject = projects.find((p) =>
            p.workspaces.some((w) =>
                w.sessions.some((s) => s.localId === sessionId),
            ),
        );
        const effectiveProjectId = ownerProject?.id ?? projectId;

        setActiveProjectIdState((previous) => isCurrent() ? effectiveProjectId : previous);
        updateProject(effectiveProjectId, (p) => {
            if (!isCurrent()) return p;
            // Find which workspace contains this session.
            const ws = p.workspaces.find((w) =>
                w.sessions.some((s) => s.localId === sessionId),
            );
            if (!ws) return { ...p, activeSessionId: sessionId };
            return {
                ...p,
                activeWorkspaceId: ws.id,
                activeSessionId: sessionId,
                workspaces: p.workspaces.map((w) =>
                    w.id !== ws.id ? w : { ...w, activeSessionId: sessionId },
                ),
            };
        });
        setError(null);

        SaveActiveProjectID(effectiveProjectId).catch(logPersistError);
        UpdateProjectActiveSession(effectiveProjectId, sessionId).catch(logPersistError);
        // Also update workspace active session.
        const proj = ownerProject ?? projects.find((p) => p.id === effectiveProjectId);
        const ws = proj?.workspaces.find((w) =>
            w.sessions.some((s) => s.localId === sessionId),
        );
        const workspaceId = ws?.id ?? pending?.workspaceId;
        if (workspaceId) {
            UpdateWorkspaceActiveSession(workspaceId, sessionId).catch(
                logPersistError,
            );
        }

        // Pre-warm the agent session if this session hasn't been used yet
        // (no existing sessionId means the runner hasn't yet initialized it).
        if (proj) {
            const session = ws?.sessions.find((s) => s.localId === sessionId);
            if (session && !session.sessionId) {
                PrewarmSession(proj.path || "", session.model || model, false).catch((err) => {
                    console.error("PrewarmSession failed on session switch:", err);
                });
            }
        }
    };

    const handleCloseSession = (projectId: string, sessionId: string) => {
        const proj = projects.find((p) => p.id === projectId);
        const session = proj
            ? findSessionInProject(proj, sessionId)
            : undefined;
        // The main chat is never closed; the store refuses too, and removing
        // it from state before the backend answers would blank the sidebar.
        if (session && isMainChatSession(session)) return;
        if (session?.streaming) {
            if (session.currentRunId) {
                CancelStream(session.currentRunId);
            } else {
                CancelByTag(session.localId);
            }
        }
        // Remember where the session sat so a failed backend delete can put
        // it back exactly where it was.
        const homeWs = proj?.workspaces.find((w) =>
            w.sessions.some((s) => s.localId === sessionId),
        );
        const removed =
            homeWs && session
                ? {
                      workspaceId: homeWs.id,
                      index: homeWs.sessions.findIndex(
                          (s) => s.localId === sessionId,
                      ),
                      session,
                      wasActive: homeWs.activeSessionId === sessionId,
                      fallbackActiveId:
                          homeWs.activeSessionId === sessionId
                              ? (homeWs.sessions.find(
                                    (s) => s.localId !== sessionId,
                                )?.localId ?? null)
                              : homeWs.activeSessionId,
                  }
                : null;
        updateProject(projectId, (p) => ({
            ...p,
            workspaces: p.workspaces.map((w) => {
                if (!w.sessions.some((s) => s.localId === sessionId)) return w;
                const remaining = w.sessions.filter(
                    (s) => s.localId !== sessionId,
                );
                const newActiveId =
                    w.activeSessionId === sessionId
                        ? (remaining[0]?.localId ?? null)
                        : w.activeSessionId;
                return {
                    ...w,
                    sessions: remaining,
                    activeSessionId: newActiveId,
                };
            }),
        }));
        markSessionRead(sessionId);
        DeleteSessionData(sessionId).catch((err) => {
            logPersistError(err);
            // The backend refused or failed: put the chat back where it was.
            if (removed) {
                updateProject(projectId, (p) => ({
                    ...p,
                    workspaces: p.workspaces.map((w) => {
                        if (w.id !== removed.workspaceId) return w;
                        if (w.sessions.some((s) => s.localId === sessionId))
                            return w;
                        const sessions = [...w.sessions];
                        sessions.splice(
                            Math.min(removed.index, sessions.length),
                            0,
                            {
                                ...removed.session,
                                streaming: false,
                                currentRunId: null,
                            },
                        );
                        return {
                            ...w,
                            sessions,
                            activeSessionId:
                                removed.wasActive &&
                                w.activeSessionId === removed.fallbackActiveId
                                    ? sessionId
                                    : w.activeSessionId,
                        };
                    }),
                }));
            }
            toast.error(i18n.t('app:sessionItem.deleteFailed'));
        });
    };

    // Guard against concurrent permission responses (e.g. rapid double-clicks
    // on approve/dismiss buttons). The backend's lock-and-delete is safe, but
    // this prevents redundant UI state updates.
    const permissionRespondingRef = useRef(false);

    const appendPermissionConfirmation = (projectId: string, tag: string, item: StreamItem) => {
        updateProject(projectId, (project) => mapSessionInProject(project, tag, (session) => ({
            ...session,
            items: [...session.items, item],
            streamItemCount: Math.max(session.streamItemCount, session.items.length + 1),
            itemsLoaded: true,
            itemsLoading: false,
        })));
        PersistStreamItem(tag, toStreamItemData(item)).catch(logPersistError);
    };

    const handleApprovePlan = async () => {
        if (!activeProject || !activeSession) return;
        if (permissionRespondingRef.current) return;
        permissionRespondingRef.current = true;
        const tag = activeSession.localId;
        const permissionTarget = activeSession.permissionRequestId || activeSession.sessionId || '';
        const requestId = activeSession.permissionRequestId;

        try {
            await RespondToPermission(permissionTarget, 'acceptEdits');
            updateProject(activeProject.id, (p) =>
                mapPermissionSessionInProject(p, tag, requestId, (s) => ({
                    ...s,
                    awaitingPlanApproval: false,
                    planContent: undefined,
                    permissionOptions: undefined,
                    permissionToolName: undefined,
                    permissionUpcoming: undefined,
                    streaming: true,
                    streamingStartedAt: Date.now(),
                }))
            );
        } catch (err) {
            const message =
                err instanceof Error ? err.message : String(err);
            toast.error(`Plan approval failed: ${message}`);
            updateProject(activeProject.id, (p) =>
                mapPermissionSessionInProject(p, tag, requestId, (s) => ({ ...s, streaming: false }))
            );
        } finally {
            permissionRespondingRef.current = false;
        }
    };

    const handleDismissPlanReview = async () => {
        if (!activeProject || !activeSession) return;
        if (permissionRespondingRef.current) return;
        permissionRespondingRef.current = true;
        const tag = activeSession.localId;
        const permissionTarget = activeSession.permissionRequestId || activeSession.sessionId || '';
        const requestId = activeSession.permissionRequestId;
        const hasPending = (activeSession.permissionOptions?.length ?? 0) > 0;

        try {
            if (hasPending) {
                await RespondToPermission(permissionTarget, "plan");
            }
            updateProject(activeProject.id, (p) =>
                mapPermissionSessionInProject(p, tag, requestId, (s) => ({
                    ...s,
                    awaitingPlanApproval: false,
                    planContent: undefined,
                    planSessionId: undefined,
                    permissionOptions: undefined,
                    permissionToolName: undefined,
                    permissionUpcoming: undefined,
                    streaming: hasPending,
                })),
            );
        } catch (err) {
            const message =
                err instanceof Error ? err.message : String(err);
            toast.error(`Dismiss plan failed: ${message}`);
            updateProject(activeProject.id, (p) =>
                mapPermissionSessionInProject(p, tag, requestId, (s) => ({ ...s, streaming: false })),
            );
        } finally {
            permissionRespondingRef.current = false;
        }
    };

    const handleRejectPlan = async () => {
        if (!activeProject || !activeSession || permissionRespondingRef.current) return;
        permissionRespondingRef.current = true;
        const tag = activeSession.localId;
        const requestId = activeSession.permissionRequestId;
        const hasPending = (activeSession.permissionOptions?.length ?? 0) > 0;
        const rejectItem: StreamItem = {
            id: buildId(),
            kind: "system",
            content: t('permissionQuestion.planRejected'),
        };
        try {
            if (hasPending) await RespondToPermission(requestId || activeSession.sessionId || '', "plan");
            updateProject(activeProject.id, (p) =>
                mapPermissionSessionInProject(p, tag, requestId, (s) => ({
                    ...s,
                    awaitingPlanApproval: false,
                    planContent: undefined,
                    planSessionId: undefined,
                    permissionOptions: undefined,
                    permissionToolName: undefined,
                    permissionUpcoming: undefined,
                    streaming: hasPending,
                })),
            );
            appendPermissionConfirmation(activeProject.id, tag, rejectItem);
        } catch (err) {
            setError(err instanceof Error ? err.message : String(err));
        } finally {
            permissionRespondingRef.current = false;
        }
    };

    // Respond to a pending AskUserQuestion or ExitPlanMode permission request.
    //
    // AskUserQuestion free-text protocol:
    //   Option IDs follow the pattern "q<N>_opt<M>" for preset choices and
    //   "q<N>_other" for the free-text "Other" option (N = question index 0-3).
    //   When the user provides free text (either via the "Other" input in the
    //   permission card, or by typing directly in the composer — see
    //   useComposerInput.ts), the optionId is encoded as "q<N>_other:<text>".
    //   The server-side parser validates this format.
    const handleRespondToPermission = async (optionId: string): Promise<boolean> => {
        if (!activeProject || !activeSession) return false;
        if (permissionRespondingRef.current) return false;
        permissionRespondingRef.current = true;
        const tag = activeSession.localId;
        const permissionTarget = activeSession.permissionRequestId || activeSession.sessionId || '';
        const requestId = activeSession.permissionRequestId;

        // Find the selected option label for the system message.
        // For "Other" free-text, optionId is "q<N>_other:user text" — extract the text.
        const otherMatch = optionId.match(/^(q\d+_other):(.+)$/);
        let answerLabel: string;
        if (otherMatch) {
            answerLabel = otherMatch[2];
        } else {
            const selected = activeSession.permissionOptions?.find(
                (o) => o.id === optionId,
            );
            answerLabel = selected?.name ?? optionId;
        }
        const answerItem: StreamItem = {
            id: buildId(),
            kind: "system",
            content: t('permissionQuestion.answered', { answer: answerLabel }),
        };

        try {
            await RespondToPermission(permissionTarget, optionId);
            updateProject(activeProject.id, (p) =>
                mapPermissionSessionInProject(p, tag, requestId, (s) => ({
                    ...s,
                    permissionOptions: undefined,
                    permissionToolName: undefined,
                    permissionUpcoming: undefined,
                    itemsLoaded: true,
                    itemsLoading: false,
                    streaming: true,
                })),
            );
            appendPermissionConfirmation(activeProject.id, tag, answerItem);
            return true;
        } catch (err) {
            const message = err instanceof Error ? err.message : String(err);
            setError(message);
            updateProject(activeProject.id, (p) =>
                mapPermissionSessionInProject(p, tag, requestId, (s) => ({
                    ...s,
                    streaming: false,
                })),
            );
            return false;
        } finally {
            permissionRespondingRef.current = false;
        }
    };

    const handleSkipPermission = async () => {
        if (!activeProject || !activeSession) return;
        if (permissionRespondingRef.current) return;
        permissionRespondingRef.current = true;
        const tag = activeSession.localId;
        const agentSessionId = activeSession.sessionId || '';

        const skipItem: StreamItem = {
            id: buildId(),
            kind: "system",
            content: "Skipped question",
        };

        updateProject(activeProject.id, (p) =>
            mapSessionInProject(p, tag, (s) => ({
                ...s,
                permissionOptions: undefined,
                permissionToolName: undefined,
                permissionUpcoming: undefined,
                items: [...s.items, skipItem],
                streamItemCount: Math.max(s.streamItemCount, s.items.length + 1),
                itemsLoaded: true,
                itemsLoading: false,
                streaming: true,
            })),
        );

        PersistStreamItem(tag, toStreamItemData(skipItem)).catch(
            logPersistError,
        );

        try {
            await CancelPermission(agentSessionId);
        } catch (err) {
            const message = err instanceof Error ? err.message : String(err);
            setError(message);
            updateProject(activeProject.id, (p) =>
                mapSessionInProject(p, tag, (s) => ({
                    ...s,
                    streaming: false,
                })),
            );
        } finally {
            permissionRespondingRef.current = false;
        }
    };

    const handleModelChange = (newModel: string) => {
        setModel(newModel);
        if (!activeProject || !activeWorkspace || !activeSession) return;

        const currentModel = activeSession.model || model;
        const currentExecutor = getExecutorType(currentModel);
        const newExecutor = getExecutorType(newModel);
        const sameExecutor = currentExecutor === newExecutor;

        // Pre-warm the agent session for the new model so the runner is
        // ready while the user composes their next message.
        if (!sameExecutor) {
            PrewarmSession(activeProject.path || "", newModel, false).catch(
                (err) => {
                    console.error("PrewarmSession failed on model change:", err);
                },
            );
        }

        const sessionId = activeSession.localId;
        updateProject(activeProject.id, (p) =>
            mapSessionInProject(p, sessionId, (s) => {
                if (sameExecutor) return { ...s, model: newModel };
                return {
                    ...s,
                    model: newModel,
                    thinkingLevel: newExecutor === "codex" ? "xhigh" : "",
                    thinkingEnabled: newExecutor === "claude",
                };
            }),
        );
        const updated = sameExecutor
            ? { ...activeSession, model: newModel }
            : {
                  ...activeSession,
                  model: newModel,
                  thinkingLevel: newExecutor === "codex" ? "xhigh" : "",
                  thinkingEnabled: newExecutor === "claude",
              };
        SaveSessionState(
            activeProject.id,
            toSessionData(updated) as any,
        ).catch(logPersistError);
    };

    const handleTogglePin = (projectId: string, sessionId: string) => {
        const proj = projects.find((p) => p.id === projectId);
        const session = proj
            ? findSessionInProject(proj, sessionId)
            : undefined;
        if (!session) return;

        const newPinned = !session.pinned;
        const newPinnedAt = newPinned ? Date.now() : 0;

        updateProject(projectId, (p) =>
            mapSessionInProject(p, sessionId, (s) => ({
                ...s,
                pinned: newPinned,
                pinnedAt: newPinnedAt,
            })),
        );

        SaveSessionState(
            projectId,
            toSessionData({
                ...session,
                pinned: newPinned,
                pinnedAt: newPinnedAt,
            }) as any,
        ).catch(logPersistError);
    };

    const handleRenameSession = (
        projectId: string,
        sessionId: string,
        newLabel: string,
    ) => {
        const trimmed = newLabel.trim();
        if (!trimmed) return;

        const proj = projects.find((p) => p.id === projectId);
        const session = proj
            ? findSessionInProject(proj, sessionId)
            : undefined;
        if (!session) return;

        updateProject(projectId, (p) =>
            mapSessionInProject(p, sessionId, (s) => ({
                ...s,
                label: trimmed,
            })),
        );

        SaveSessionState(
            projectId,
            toSessionData({ ...session, label: trimmed }) as any,
        ).catch(logPersistError);
    };

    const handleForkSession = useCallback(
        async (projectId: string, sessionId: string): Promise<string | null> => {
            const project = projects.find((candidate) => candidate.id === projectId);
            const source = project
                ? findSessionInProject(project, sessionId)
                : undefined;
            if (!project || !source) return null;
            if (source.streaming) {
                toast.info(
                    localizedPrompt(
                        'Wait for the response to finish before branching this chat.',
                        '请等待当前回复完成后再基于此聊天新建。',
                    ),
                );
                return null;
            }

            try {
                const branchLabel = localizedPrompt(
                    `${source.label} · Branch`,
                    `${source.label} · 分支`,
                );
                const result = await ForkSession(sessionId, branchLabel);
                const forkedSession = convertSessionFromStorage(result.session);
                const ownerProjectId = result.projectId || projectId;
                const workspaceId = forkedSession.workspaceId;

                updateProject(ownerProjectId, (currentProject) => ({
                    ...currentProject,
                    activeWorkspaceId: workspaceId,
                    activeSessionId: forkedSession.localId,
                    workspaces: currentProject.workspaces.map((workspace) =>
                        workspace.id !== workspaceId
                            ? workspace
                            : {
                                  ...workspace,
                                  activeSessionId: forkedSession.localId,
                                  sessions: [...workspace.sessions, forkedSession],
                              },
                    ),
                }));
                setActiveProjectId(ownerProjectId);
                setError(null);
                markSessionRead(forkedSession.localId);

                void Promise.all([
                    SaveActiveProjectID(ownerProjectId),
                    UpdateProjectActiveSession(
                        ownerProjectId,
                        forkedSession.localId,
                    ),
                    UpdateProjectActiveWorkspace(ownerProjectId, workspaceId),
                    UpdateWorkspaceActiveSession(
                        workspaceId,
                        forkedSession.localId,
                    ),
                ]).catch(logPersistError);

                PrewarmSession(
                    project.path || '',
                    forkedSession.model || model,
                    false,
                ).catch((err) => {
                    console.error('PrewarmSession failed for branched chat:', err);
                });
                return forkedSession.localId;
            } catch (err) {
                const message = err instanceof Error ? err.message : String(err);
                toast.error(
                    localizedPrompt(
                        `Failed to branch chat: ${message}`,
                        `基于此聊天新建失败：${message}`,
                    ),
                );
                return null;
            }
        },
        [projects, updateProject, setError, markSessionRead, model],
    );

    const handlePullKnowledge = async (projectId: string) => {
        const proj = projects.find((p) => p.id === projectId);
        if (!proj) return;
        try {
            const result = await PullKnowledge(proj.path);
            const created = result.knowledge_created ?? 0;
            const updated = result.knowledge_updated ?? 0;
            const deleted = result.knowledge_deleted ?? 0;
            notifyImportant(
                'Pull complete',
                `${created} created, ${updated} updated, ${deleted} deleted`,
                'success',
            );
        } catch (err) {
            const message = err instanceof Error ? err.message : String(err);
            notifyImportant('Pull knowledge failed', message, 'error');
        }
    };

    const handlePushKnowledge = async (projectId: string) => {
        const proj = projects.find((p) => p.id === projectId);
        if (!proj) return;
        try {
            const result = await PushKnowledge(proj.path);
            const created = result.created ?? 0;
            const updated = result.updated ?? 0;
            notifyImportant(
                'Push complete',
                `${created} created, ${updated} updated`,
                'success',
            );
        } catch (err) {
            const message = err instanceof Error ? err.message : String(err);
            notifyImportant('Push knowledge failed', message, 'error');
        }
    };

    const handleRefreshContext = useCallback(async () => {
        if (!activeProject?.isYansuProject) return;
        setFetchingContext(true);
        setError(null);
        try {
            const result = await FetchProjectContext(activeProject.path);
            const context: ProjectContext = {
                is_yansu_project: result.is_yansu_project,
                project_name: result.project_name,
                organization: result.organization,
                product: result.product,
                prd: result.prd,
                prd_status: result.prd_status,
                scenarios: result.scenarios,
                scenarios_status: result.scenarios_status,
                error: result.error,
                fetchedAt: Date.now(),
            };
            if (context.error) {
                setError(context.error);
            }
            updateProject(activeProject.id, (p) => ({ ...p, context }));
        } catch (err) {
            const message =
                err instanceof Error ? err.message : String(err);
            toast.error(`Failed to refresh context: ${message}`);
        } finally {
            setFetchingContext(false);
        }
    }, [activeProject, updateProject]);

    const handleOpenProjectURL = async (projectId: string) => {
        const proj = projects.find((p) => p.id === projectId);
        if (!proj) return;
        try {
            await OpenProjectURL(proj.path);
        } catch (err) {
            const message = err instanceof Error ? err.message : String(err);
            toast.error(`Open project URL failed: ${message}`);
        }
    };

    const handleRewind = async (sortOrder: number, userContent: string) => {
        if (!activeProject || !activeSession) return;
        if (activeSession.streaming) {
            return;
        }
        const workDir = activeWorkspace?.worktreePath || activeProject.path;
        try {
            await RewindSession(
                activeSession.localId,
                activeSession.sessionId,
                workDir,
                sortOrder,
                userContent,
            );
            // Reload items from DB (which already has correct state after DELETE WHERE sort_order >= ?)
            // instead of filtering in-memory (streaming items have undefined sortOrder).
            const page = await loadStreamItemsPageWithOpenAnswerDelivery(
                activeSession.localId,
                -1,
                500,
            );
            const reloadedItems: StreamItem[] = (page.items ?? []).map(fromStreamItemData);
            updateProject(activeProject.id, (p) =>
                mapSessionInProject(p, activeSession.localId, (s) => ({
                    ...s,
                    items: reloadedItems,
                    streamItemCount: reloadedItems.length,
                    itemsLoaded: true,
                    itemsLoading: false,
                    hasMore: page.hasMore,
                    nextCursor: page.nextCursor,
                    updatedAt: Date.now(),
                })),
            );
        } catch (err) {
            const message = err instanceof Error ? err.message : String(err);
            console.warn("reset failed", message);
        }
    };

    const handleEditMessageImpl = async (itemId: string, sortOrder: number, newContent: string): Promise<boolean> => {
        if (!activeProject || !activeSession) return false;
        if (activeSession.streaming) {
            return false;
        }
        const workDir = activeWorkspace?.worktreePath || activeProject.path;
        // Chat K3 (C046): the old history stays until the edit is sent; a
        // failed edit puts it back and the edit box says "Didn't save".
        const previousItems = activeSession.items;
        const previousContent = previousItems.find((item) => item.id === itemId)?.content;
        let rewound = false;
        try {
            // 1. Update the message content in DB
            await UpdateStreamItemContent(itemId, newContent);

            // 2. Delete everything after this message + truncate CLI session
            await RewindSession(
                activeSession.localId,
                activeSession.sessionId,
                workDir,
                sortOrder + 1,
                newContent,
            );
            rewound = true;

            // 3. Reload items from DB
            const page = await loadStreamItemsPageWithOpenAnswerDelivery(
                activeSession.localId,
                -1,
                500,
            );
            const reloadedItems: StreamItem[] = (page.items ?? []).map(fromStreamItemData);

            // 4. Set streaming state and update items
            const tag = activeSession.localId;
            updateProject(activeProject.id, (p) =>
                mapSessionInProject(p, tag, (s) => ({
                    ...s,
                    items: reloadedItems,
                    streamItemCount: reloadedItems.length,
                    itemsLoaded: true,
                    itemsLoading: false,
                    hasMore: page.hasMore,
                    nextCursor: page.nextCursor,
                    streaming: true,
                    streamingStartedAt: Date.now(),
                    currentRunId: null,
                    updatedAt: Date.now(),
                })),
            );

            // 5. Send the edited content to AI
            let sessionModel = model;
            const sessionId = activeSession.sessionId || '';
            const projectPath = workDir;
            const sessionPlanMode = false;
            const sessionThinkingEnabled = false;
            const sessionMode = activeSession.sessionMode ?? '';

            let sessionReasoningEffort = '';
            if (getExecutorType(sessionModel) === 'codex') {
                const [baseModel, effortFromModel] = splitModelEffort(sessionModel);
                sessionModel = baseModel;
                sessionReasoningEffort = effortFromModel;
            }

            await sendSessionPrompt({
                session: activeSession,
                promptText: newContent,
                sessionModel,
                sessionId,
                tag,
                projectPath,
                sessionPlanMode,
                sessionThinkingEnabled,
                sessionReasoningEffort,
                sessionMode,
            });
            return true;
        } catch (err) {
            const message = err instanceof Error ? err.message : String(err);
            console.warn("edit failed", message);
            if (previousContent !== undefined) void UpdateStreamItemContent(itemId, previousContent).catch(() => undefined);
            // After the rewind the later rows are gone from the database, so
            // show what is stored rather than history that would vanish on reload.
            let restored = previousItems;
            let paging: { hasMore: boolean; nextCursor: number } | undefined;
            if (rewound) {
                try {
                    const page = await loadStreamItemsPageWithOpenAnswerDelivery(activeSession.localId, -1, 500);
                    restored = (page.items ?? []).map(fromStreamItemData).map((item) =>
                        item.id === itemId && previousContent !== undefined ? { ...item, content: previousContent } : item);
                    paging = { hasMore: page.hasMore, nextCursor: page.nextCursor };
                } catch {
                    restored = previousItems.filter((item) => (item.sortOrder ?? 0) <= sortOrder);
                }
            }
            updateProject(activeProject.id, (p) =>
                mapSessionInProject(p, activeSession.localId, (s) => ({
                    ...s,
                    items: restored,
                    streamItemCount: restored.length,
                    streaming: false,
                    ...paging,
                })),
            );
            return false;
        }
    };

    // Stable identity for the stream's memo boundaries: the latest closure
    // (with fresh session state) is always invoked through the same wrapper.
    const editMessageRef = useRef(handleEditMessageImpl);
    editMessageRef.current = handleEditMessageImpl;
    const handleEditMessage = useCallback(
        (itemId: string, sortOrder: number, newContent: string) => editMessageRef.current(itemId, sortOrder, newContent),
        [],
    );

    /** Resolve the effective filesystem path: worktree path for non-default workspaces, project path otherwise. */
    const getEffectivePath = (proj: Project): string => {
        const activeWs = proj.workspaces.find(
            (w) => w.id === proj.activeWorkspaceId,
        );
        return activeWs && !activeWs.isDefault && activeWs.worktreePath
            ? activeWs.worktreePath
            : proj.path;
    };

    const handleOpenFolder = async (projectId: string) => {
        const proj = projects.find((p) => p.id === projectId);
        if (!proj) return;
        try {
            await OpenProjectFolder(getEffectivePath(proj));
        } catch (err) {
            const message = err instanceof Error ? err.message : String(err);
            toast.error(`Open folder failed: ${message}`);
        }
    };

    const handleOpenInApp = async (projectId: string, appBundle: string) => {
        const proj = projects.find((p) => p.id === projectId);
        if (!proj) return;
        try {
            await OpenInApp(appBundle, getEffectivePath(proj));
        } catch (err) {
            const message = err instanceof Error ? err.message : String(err);
            toast.error(`Failed to open in app: ${message}`);
        }
    };

    // ── Workspace CRUD ──

    const handleNewWorkspace = async (projectId: string) => {
        const proj = projects.find((p) => p.id === projectId);
        if (!proj) return;

        const gitInfo = await GetGitInfo(proj.path);
        if (!gitInfo.isGitRepo) {
            toast.error("Cannot create workspace: not a git repository");
            return;
        }

        const baseBranch = gitInfo.currentBranch || "HEAD";
        const wsId = buildId();
        const label = randomName();

        try {
            const info = await CreateWorkspaceBackend(
                proj.path,
                projectId,
                wsId,
                label,
                baseBranch,
            );

            const firstSession = createSession(model, wsId);

            const newWs = createWorkspace(info.label || label, false);
            // Override auto-generated ID with the one we sent to the backend.
            newWs.id = wsId;
            newWs.worktreePath = info.path;
            newWs.worktreeBranch = info.branch;
            newWs.baseBranch = info.baseBranch;
            newWs.sessions = [firstSession];
            newWs.activeSessionId = firstSession.localId;

            updateProject(projectId, (p) => ({
                ...p,
                workspaces: [...p.workspaces, newWs],
                activeWorkspaceId: wsId,
                activeSessionId: firstSession.localId,
            }));
            setActiveProjectId(projectId);

            await SaveWorkspaceState(projectId, toWorkspaceData(newWs) as any);
            await SaveSessionState(
                projectId,
                toSessionData(firstSession) as any,
            );
            await UpdateProjectActiveWorkspace(projectId, wsId);
            await UpdateProjectActiveSession(projectId, firstSession.localId);
            toast.success(`Workspace "${info.label || label}" created`);

            // Listen for async setup command completion.
            const offSetup = EventsOn(
                'worktree:setup-complete',
                (data: { workspaceId: string; success: boolean; output?: string; error?: string }) => {
                    if (data.workspaceId !== wsId) return;
                    offSetup();
                    if (data.success) {
                        notifyImportant('Workspace setup completed', 'Your workspace is ready.', 'success');
                    } else {
                        const tail = data.output ? data.output.slice(-500) : '';
                        notifyImportant(
                            'Workspace setup failed',
                            `${data.error}${tail ? '\n' + tail : ''}`,
                            'error',
                        );
                    }
                }
            );
            // Auto-cleanup listener after 15 min to prevent leaks.
            setTimeout(() => offSetup(), 15 * 60 * 1000);
        } catch (err) {
            const message = err instanceof Error ? err.message : String(err);
            toast.error(`Create workspace failed: ${message}`);
        }
    };

    const handleSwitchWorkspace = (projectId: string, workspaceId: string) => {
        selectionRevision.current += 1;
        setActiveProjectId(projectId);
        updateProject(projectId, (p) => {
            const ws = p.workspaces.find((w) => w.id === workspaceId);
            return {
                ...p,
                activeWorkspaceId: workspaceId,
                activeSessionId: ws?.activeSessionId ?? p.activeSessionId,
            };
        });
        setError(null);
        UpdateProjectActiveWorkspace(projectId, workspaceId).catch(
            logPersistError,
        );
        SaveActiveProjectID(projectId).catch(logPersistError);
    };

    const handleCloseWorkspace = async (
        projectId: string,
        workspaceId: string,
    ) => {
        const proj = projects.find((p) => p.id === projectId);
        if (!proj) return;
        const ws = proj.workspaces.find((w) => w.id === workspaceId);
        if (!ws || ws.isDefault) {
            toast.error("Cannot close the default workspace");
            return;
        }

        try {
            await RemoveWorkspace(proj.path, workspaceId);
        } catch (err) {
            const message = err instanceof Error ? err.message : String(err);
            toast.error(`Close workspace failed: ${message}`);
            return;
        }

        shutdownWorkspaceSessions(proj, workspaceId);
        const nextProject = removeWorkspaceFromProject(proj, workspaceId);
        updateProject(projectId, () => nextProject);
        persistProjectSelection(proj, nextProject);
        toast.success("Workspace closed");
    };

    const handleMergeWorkspace = async (
        projectId: string,
        workspaceId: string,
    ) => {
        const proj = projects.find((p) => p.id === projectId);
        if (!proj) return;
        const ws = proj.workspaces.find((w) => w.id === workspaceId);
        if (!ws || ws.isDefault || !ws.worktreePath || !ws.worktreeBranch) {
            toast.error("Cannot merge the default workspace");
            return;
        }

        try {
            await MergeWorkspace(
                proj.path,
                workspaceId,
                ws.worktreePath,
                ws.worktreeBranch,
            );
            shutdownWorkspaceSessions(proj, workspaceId);
            const nextProject = removeWorkspaceFromProject(proj, workspaceId);
            updateProject(projectId, () => nextProject);
            persistProjectSelection(proj, nextProject);
            toast.success(`Merged "${ws.label}" into base branch`);
        } catch (err) {
            const message = err instanceof Error ? err.message : String(err);
            toast.error(`Merge failed: ${message}`);
        }
    };

    const handleReorderProjects = useCallback((reorderedIds: string[]) => {
        const normalIds = reorderedIds.filter(
            (id) => id !== STANDALONE_CHAT_PROJECT_ID,
        );

        // Optimistic update: reorder local state immediately.
        setProjects((prev) => {
            const standalone =
                prev.find((p) => p.id === STANDALONE_CHAT_PROJECT_ID) ?? null;
            const normalProjects = prev.filter(
                (p) => p.id !== STANDALONE_CHAT_PROJECT_ID,
            );
            const byId = new Map(normalProjects.map((p) => [p.id, p]));
            const reorderedNormal: Project[] = [];

            for (const id of normalIds) {
                const p = byId.get(id);
                if (p) {
                    reorderedNormal.push({
                        ...p,
                        sortOrder: reorderedNormal.length,
                    });
                }
            }

            // Append any projects not in the reordered list (shouldn't happen, but safe).
            for (const p of normalProjects) {
                if (!normalIds.includes(p.id)) {
                    reorderedNormal.push({
                        ...p,
                        sortOrder: reorderedNormal.length,
                    });
                }
            }

            if (!standalone) return reorderedNormal;

            return [
                { ...standalone, sortOrder: -1 },
                ...reorderedNormal,
            ];
        });

        // Persist to backend.
        ReorderProjects(normalIds).catch((err) => {
            logPersistError(err);
            toast.error("Failed to save project order");
        });
    }, []);

    const handleReorderWorkspaces = useCallback((projectId: string, reorderedIds: string[]) => {
        // Filter out default workspace (it should not be reordered).
        const proj = projects.find((p) => p.id === projectId);
        if (!proj) return;

        // Optimistic update: reorder local state immediately.
        updateProject(projectId, (p) => {
            const byId = new Map(p.workspaces.map((w) => [w.id, w]));
            const reorderedWorkspaces: typeof p.workspaces = [];

            // Default workspace always comes first (not in reorderedIds).
            const defaultWs = p.workspaces.find((w) => w.isDefault);
            if (defaultWs) {
                reorderedWorkspaces.push({ ...defaultWs, sortOrder: -1 });
            }

            // Add non-default workspaces in the new order.
            for (let i = 0; i < reorderedIds.length; i++) {
                const ws = byId.get(reorderedIds[i]);
                if (ws && !ws.isDefault) {
                    reorderedWorkspaces.push({
                        ...ws,
                        sortOrder: i,
                    });
                }
            }

            // Append any workspaces not in the reordered list (shouldn't happen, but safe).
            for (const ws of p.workspaces) {
                if (!ws.isDefault && !reorderedIds.includes(ws.id)) {
                    reorderedWorkspaces.push({
                        ...ws,
                        sortOrder: reorderedWorkspaces.length - 1,
                    });
                }
            }

            return {
                ...p,
                workspaces: reorderedWorkspaces,
            };
        });

        // Persist to backend.
        ReorderWorkspaces(projectId, reorderedIds).catch((err) => {
            logPersistError(err);
            toast.error("Failed to save workspace order");
        });
    }, [projects, updateProject]);

    return {
        projects,
        projectsHydrated,
        setProjects,
        activeProjectId,
        setActiveProjectId,
        activeProject,
        activeWorkspace,
        activeSession,
        updateProject,
        fetchingContext,
        handleOpenProject,
        handleCloseProject,
        handleSwitchProject,
        handleNewSession,
        handleOpenMainChat,
        getSelectionRevision,
        handleNewAutomationChat,
        handleStartHandoffExecution,
        handleStartSopExecution,
        handleSwitchSession,
        handleCloseSession,
        handleApprovePlan,
        handleDismissPlanReview,
        handleRejectPlan,
        handleRespondToPermission,
        handleSkipPermission,
        handleModelChange,
        handlePullKnowledge,
        handlePushKnowledge,
        handleRefreshContext,
        handleOpenProjectURL,
        handleOpenFolder,
        handleOpenInApp,
        installedApps,
        handleTogglePin,
        handleRenameSession,
        handleForkSession,
        handleRewind,
        handleEditMessage,
        handleNewWorkspace,
        handleSwitchWorkspace,
        handleCloseWorkspace,
        handleMergeWorkspace,
        handleReorderProjects,
        handleReorderWorkspaces,
        markProjectsHydrated,
        unreadSessionIds,
        markSessionRead,
        markSessionUnread,
    };
}
