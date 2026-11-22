import { act, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { AgentEvent, Project } from "@/types";
import { useAgentStreaming } from "./useAgentStreaming";

const listeners = vi.hoisted(() => [] as ((evt: AgentEvent) => void)[]);
const writes = vi.hoisted(() => ({
    inserted: [] as { id: string; kind: string; content: string; toolInput?: string }[],
    updated: [] as { id: string; content: string }[],
    stored: [] as { id: string; kind: string; content: string }[],
    // The stored rows a page read returns, by cursor (-1: newest).
    page: (_cursor: number) =>
        ({ items: [] as { id: string; kind: string; content: string }[] }) as {
            items: { id: string; kind: string; content: string }[];
            hasMore?: boolean;
            nextCursor?: number;
        },
}));

vi.mock("../../wailsjs/runtime/runtime", () => ({
    EventsOn: vi.fn((name: string, cb: (evt: AgentEvent) => void) => {
        if (name !== "agent:event") return () => {};
        listeners.push(cb);
        return () => listeners.splice(listeners.indexOf(cb), 1);
    }),
}));
vi.mock("../../wailsjs/go/main/App", () => ({
    PersistStreamItem: vi.fn(
        async (
            _tag: string,
            item: { id: string; kind: string; content: string },
        ) => {
            writes.inserted.push({
                id: item.id,
                kind: item.kind,
                content: item.content,
                toolInput: (item as { toolInput?: string }).toolInput,
            });
            return writes.inserted.length;
        },
    ),
    GrowStreamItemContent: vi.fn(
        async (_tag: string, id: string, content: string) => {
            writes.updated.push({ id, content });
            return true;
        },
    ),
    LoadStreamItemsPage: vi.fn(async (_tag: string, cursor: number) =>
        writes.page(cursor),
    ),
    UpdateSessionClaudeID: vi.fn(async () => {}),
}));

// One turn as emitAgentEvent stamps it: every event has its own event_id, and
// the rows it builds carry the backend's item_id (and item_seq for text).
const run = "run_e0ce1507";
const turn: AgentEvent[] = [
    { type: "run_created", tag: "main", run_id: run, event_id: "e1" },
    { type: "assistant", tag: "main", run_id: run, content: "I'll ", event_id: "e2", item_id: "a1", item_seq: 1 },
    { type: "assistant", tag: "main", run_id: run, content: "check.", event_id: "e3", item_id: "a1", item_seq: 2 },
    { type: "tool_use_start", tag: "main", run_id: run, tool_name: "Bash", tool_use_id: "t1", event_id: "e4", item_id: "tool1" },
    { type: "tool_use", tag: "main", run_id: run, tool_name: "Bash", tool_use_id: "t1", tool_input: { command: "ls" }, event_id: "e5", item_id: "tool1" },
    { type: "tool_result", tag: "main", run_id: run, tool_name: "Bash", tool_use_id: "t1", content: "[]", event_id: "e6" },
    { type: "assistant", tag: "main", run_id: run, content: "Done.", event_id: "e7", item_id: "a2", item_seq: 1 },
    { type: "done", tag: "main", run_id: run, event_id: "e8" },
];

const rows = [
    { id: "a1", kind: "assistant" },
    { id: "a2", kind: "assistant" },
    { id: "e1", kind: "system" },
    { id: "e6", kind: "result" },
    { id: "e8", kind: "system" },
    { id: "tool1", kind: "tool" },
];

function mountRenderer() {
    return renderHook(() => useAgentStreaming({ setProjects: vi.fn() }));
}

/** Sends events to every mounted renderer, or only to the one given. */
function send(events: AgentEvent[], only?: (evt: AgentEvent) => void) {
    act(() => {
        for (const evt of events) {
            for (const listener of only ? [only] : [...listeners]) {
                listener(evt);
            }
        }
    });
}

/** The distinct rows written, as storage keeps them (first write of an id). */
function storedRows() {
    const byId = new Map<string, { id: string; kind: string }>();
    for (const { id, kind } of writes.inserted) {
        if (!byId.has(id)) byId.set(id, { id, kind });
    }
    return [...byId.values()].sort((a, b) => a.id.localeCompare(b.id));
}

async function settle(
    ...renderers: ReturnType<typeof mountRenderer>[]
): Promise<void> {
    // Fire the 3 s debounced writes, then wait for every queued write.
    act(() => {
        vi.runAllTimers();
    });
    for (const r of renderers) {
        await r.result.current.waitForSessionPersistence("main");
    }
}

describe("useAgentStreaming", () => {
    beforeEach(() => {
        vi.useFakeTimers();
        writes.inserted.length = 0;
        writes.updated.length = 0;
        writes.stored.length = 0;
        writes.page = () => ({ items: writes.stored });
    });
    afterEach(() => {
        vi.useRealTimers();
    });

    it("clears the original work permission after another surface answers, including replay", async () => {
        let projects = [{ id: "p", workspaces: [{ id: "w", sessions: [
            { localId: "work", items: [], planMode: true },
            { localId: "other", items: [], permissionToolName: "Other tool" },
        ] }] }] as unknown as Project[];
        const setProjects = vi.fn((update: unknown) => {
            projects = typeof update === "function"
                ? (update as (previous: Project[]) => Project[])(projects)
                : update as Project[];
        });
        const renderer = renderHook(() => useAgentStreaming({ setProjects: setProjects as never }));
        send([{
            type: "permission_request", tag: "work", session_id: "agent-work", event_id: "permission-1",
            permission_request_id: "work-permission:request-1",
            tool_name: "ExitPlanMode", permission_options: [{ id: "acceptEdits", name: "Approve", kind: "allow_once" }],
            upcoming_approval: { title: "Review tests" },
        }]);
        const session = () => projects[0].workspaces[0].sessions[0];
        expect(session().permissionOptions).toHaveLength(1);
        expect(session().permissionRequestId).toBe("work-permission:request-1");
        expect(session().permissionToolName).toBe("ExitPlanMode");
        expect(session().permissionUpcoming).toEqual({ title: "Review tests" });
        expect(session().awaitingPlanApproval).toBe(true);
        const resolution: AgentEvent = { type: "permission_resolved", tag: "work", session_id: "agent-work", event_id: "resolved-1" };
        send([resolution, resolution]);
        expect(session().permissionOptions).toBeUndefined();
        expect(session().permissionRequestId).toBeUndefined();
        expect(session().permissionToolName).toBeUndefined();
        expect(session().permissionUpcoming).toBeUndefined();
        expect(session().awaitingPlanApproval).toBe(false);
        expect(projects[0].workspaces[0].sessions[1].permissionToolName).toBe("Other tool");
        expect(session().items).toEqual([]);
        await settle(renderer);
        expect(writes.inserted).toEqual([]);
        renderer.unmount();
    });

    it("drops a previous work permission identity when a new run starts", async () => {
        let projects = [{ id: "p", workspaces: [{ id: "w", sessions: [{
            localId: "work", items: [], permissionRequestId: "work-permission:old",
            permissionOptions: [{ id: "allow_once", name: "Approve", kind: "allow_once" }],
            permissionToolName: "Write", awaitingPlanApproval: true,
        }] }] }] as unknown as Project[];
        const renderer = renderHook(() => useAgentStreaming({ setProjects: (update) => {
            projects = typeof update === "function" ? update(projects) : update;
        } }));
        send([{ type: "run_created", tag: "work", session_id: "agent-work", run_id: "new-run", event_id: "new-run-event" }]);
        await settle(renderer);
        const session = projects[0].workspaces[0].sessions[0];
        expect(session.permissionRequestId).toBeUndefined();
        expect(session.permissionOptions).toBeUndefined();
        expect(session.permissionToolName).toBeUndefined();
        expect(session.awaitingPlanApproval).toBe(false);
        renderer.unmount();
    });

    // Regression: with the app window and a browser preview both open, each
    // renderer stored every row of a main chat turn under its own random id,
    // so the reply showed twice. Both now store each row under the id the
    // backend gave it, and storage keeps one row per id.
    // K1: the error row keeps Go's proven cause; a no-AI send writes no error row at all.
    it("stores an error's code and assistant, and writes no row for a kept no-AI send", async () => {
        const r = mountRenderer();
        send([
            { type: "error", tag: "main", run_id: "r-k1", error: "anthropic: HTTP 401", code: "signed_out", assistant: "Claude Code", event_id: "k1-err" },
            { type: "error", tag: "main", code: "no_assistant", event_id: "k1-none" },
        ]);
        await settle(r);
        const errors = writes.inserted.filter((row) => row.kind === "error");
        expect(errors).toHaveLength(1);
        expect(JSON.parse(errors[0].toolInput ?? "{}")).toEqual({ code: "signed_out", assistant: "Claude Code" });
        r.unmount();
    });

    // R75: a signed-out answer adds its error row and leaves the person's message alone:
    // nothing is removed, rewritten or marked failed, so the message stays to be sent again.
    it("keeps the person's message when a signed-out answer fails (R75)", async () => {
        const r = mountRenderer();
        send([{ type: "error", tag: "main", run_id: "r-r75", error: "anthropic: HTTP 401", code: "signed_out", assistant: "Claude Code", event_id: "r75-err" }]);
        await settle(r);
        expect(writes.inserted.map((row) => row.kind)).toEqual(["error"]);
        expect(writes.updated).toEqual([]);
        r.unmount();
    });

    it("stores each row under the backend's id, so two renderers write the same rows", async () => {
        const appWindow = mountRenderer();
        const browserPreview = mountRenderer();

        send(turn);
        await settle(appWindow, browserPreview);

        expect(writes.inserted).toHaveLength(rows.length * 2);
        expect(storedRows()).toEqual(rows);

        appWindow.unmount();
        browserPreview.unmount();
        expect(listeners).toHaveLength(0);
    });

    // A preview opened (or a window reloaded) mid-reply starts with empty
    // trackers. It still grows the reply's row, not a second one, and leaves
    // the stored text to the renderer that saw the row whole.
    it("a renderer that joins after the first chunk writes no second row and no partial text", async () => {
        const appWindow = mountRenderer();
        send(turn.slice(0, 2));
        const browserPreview = mountRenderer();
        send(turn.slice(2));
        await settle(appWindow, browserPreview);

        expect(storedRows()).toEqual(rows);
        const a1 = writes.updated.filter((u) => u.id === "a1");
        expect(a1.length).toBeGreaterThan(0);
        expect(a1.every((u) => u.content === "I'll check.")).toBe(true);

        appWindow.unmount();
        browserPreview.unmount();
    });

    it("a renderer that joins at tool_use_start stores the tool under the same row", async () => {
        const appWindow = mountRenderer();
        send(turn.slice(0, 4));
        const browserPreview = mountRenderer();
        send(turn.slice(4));
        await settle(appWindow, browserPreview);

        expect(storedRows()).toEqual(rows);
        expect(
            writes.inserted.filter((w) => w.kind === "tool").map((w) => w.id),
        ).toEqual(["tool1", "tool1"]);

        appWindow.unmount();
        browserPreview.unmount();
    });

    // The bridge drops events for a slow client. A renderer that sees a gap
    // in a row stops writing it, so it cannot cut down the stored reply.
    it("a renderer that misses a chunk stops writing that row", async () => {
        const appWindow = mountRenderer();
        const browserPreview = mountRenderer();
        const [windowListener] = listeners;
        send(turn.slice(0, 2));
        send(
            [{ ...turn[1], content: "really ", event_id: "e2b", item_seq: 2 }],
            windowListener,
        );
        send([{ ...turn[2], item_seq: 3 }]);
        send(turn.slice(3));
        await settle(appWindow, browserPreview);

        expect(storedRows()).toEqual(rows);
        const a1 = writes.updated.filter((u) => u.id === "a1");
        expect(a1.length).toBeGreaterThan(0);
        expect(a1.every((u) => u.content === "I'll really check.")).toBe(true);

        appWindow.unmount();
        browserPreview.unmount();
    });

    // A chat reopened (or a preview opened) mid-reply shows the stored start
    // plus the chunks it saw. Once the turn ends and the backend has stored
    // the row's whole text, the renderer shows that instead.
    it("a renderer that joined mid-row shows the stored text once the turn ends", async () => {
        let projects: Project[] = [
            {
                id: "p",
                workspaces: [
                    {
                        id: "w",
                        sessions: [
                            {
                                localId: "main",
                                items: [{ id: "a1", kind: "assistant", content: "I'll " }],
                            },
                        ],
                    },
                ],
            } as unknown as Project,
        ];
        const setProjects = vi.fn((update: unknown) => {
            projects =
                typeof update === "function"
                    ? (update as (p: Project[]) => Project[])(projects)
                    : (update as Project[]);
        });
        const reopened = renderHook(() =>
            useAgentStreaming({ setProjects: setProjects as never }),
        );
        const shownA1 = () =>
            projects[0].workspaces[0].sessions[0].items.find(
                (item) => item.id === "a1",
            )?.content;

        // It missed "check. " while the chat was closed.
        send([
            { type: "assistant", tag: "main", run_id: run, content: "now.", event_id: "e3b", item_id: "a1", item_seq: 3 },
        ]);
        act(() => {
            vi.advanceTimersByTime(200);
        });
        expect(shownA1()).toBe("I'll now.");
        expect(writes.updated.filter((u) => u.id === "a1")).toEqual([]);

        writes.stored.push({ id: "a1", kind: "assistant", content: "I'll check. now." });
        send([{ type: "done", tag: "main", run_id: run, event_id: "e9" }]);
        await act(async () => {
            await vi.advanceTimersByTimeAsync(2600);
        });
        expect(shownA1()).toBe("I'll check. now.");

        reopened.unmount();
    });

    // The same, for a row that ends mid-turn (a tool call follows it), one
    // older than the newest page of the chat, and a backend write that lands
    // late: the row is re-read 2.5 s after it ends and once more at 5 s,
    // paging back until it is found.
    it("re-reads a stale row when it ends mid-turn, paging back and retrying", async () => {
        let projects: Project[] = [
            {
                id: "p",
                workspaces: [
                    {
                        id: "w",
                        sessions: [
                            {
                                localId: "main",
                                items: [{ id: "a1", kind: "assistant", content: "I'll " }],
                            },
                        ],
                    },
                ],
            } as unknown as Project,
        ];
        const setProjects = vi.fn((update: unknown) => {
            projects =
                typeof update === "function"
                    ? (update as (p: Project[]) => Project[])(projects)
                    : (update as Project[]);
        });
        const reopened = renderHook(() =>
            useAgentStreaming({ setProjects: setProjects as never }),
        );
        const shownA1 = () =>
            projects[0].workspaces[0].sessions[0].items.find(
                (item) => item.id === "a1",
            )?.content;

        let a1Stored = "I'll ";
        const cursors: number[] = [];
        writes.page = (cursor: number) => {
            cursors.push(cursor);
            return cursor < 0
                ? { items: [{ id: "newer", kind: "result", content: "" }], hasMore: true, nextCursor: 100 }
                : { items: [{ id: "a1", kind: "assistant", content: a1Stored }], hasMore: false, nextCursor: 40 };
        };

        send([
            { type: "assistant", tag: "main", run_id: run, content: "now.", event_id: "x1", item_id: "a1", item_seq: 3 },
            { type: "tool_use_start", tag: "main", run_id: run, tool_name: "Bash", tool_use_id: "t9", event_id: "x2", item_id: "tool9" },
        ]);
        await act(async () => {
            await vi.advanceTimersByTimeAsync(2600);
        });
        // The backend's write was late: the first read still has only the
        // start, and the screen keeps what it shows.
        expect(cursors).toEqual([-1, 100]);
        expect(shownA1()).toBe("I'll now.");

        a1Stored = "I'll check. now.";
        await act(async () => {
            await vi.advanceTimersByTimeAsync(2500);
        });
        expect(shownA1()).toBe("I'll check. now.");

        reopened.unmount();
    });

    // A stale row the store never holds (rewound away) must not make each
    // re-read walk the whole chat.
    it("caps how far a re-read pages back", async () => {
        const reopened = mountRenderer();
        let reads = 0;
        writes.page = (cursor: number) => {
            reads++;
            const start = cursor < 0 ? 100000 : cursor;
            return {
                items: [{ id: `row-${start - 1}`, kind: "result", content: "" }],
                hasMore: true,
                nextCursor: start - 1,
            };
        };

        send([
            { type: "assistant", tag: "main", run_id: run, content: "x", event_id: "g1", item_id: "ghost", item_seq: 2 },
            { type: "tool_use_start", tag: "main", run_id: run, tool_name: "Bash", tool_use_id: "t8", event_id: "g2", item_id: "tool8" },
        ]);
        await act(async () => {
            await vi.advanceTimersByTimeAsync(2600);
        });
        expect(reads).toBe(10);

        reopened.unmount();
    });
});
