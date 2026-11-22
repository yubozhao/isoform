import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import i18n from "@/i18n";
import { App } from "../../../quick-chat-spotlight/frontend/src/App";
import { ConversationPanel } from "../../../quick-chat-spotlight/frontend/src/components/ConversationPanel";
import type { StreamEvent } from "../../../quick-chat-spotlight/frontend/src/api/types";

const mocks = vi.hoisted(() => ({
    fetchReady: vi.fn(), openReady: vi.fn(), workOnIt: vi.fn(), submit: vi.fn(), subscribe: vi.fn(), resize: vi.fn(), hide: vi.fn(),
}));
vi.mock("../../../quick-chat-spotlight/frontend/src/api/yansu", () => ({
    fetchReady: mocks.fetchReady, openReady: mocks.openReady, workOnIt: mocks.workOnIt, submitPrompt: mocks.submit, subscribeStream: mocks.subscribe,
    fetchAppearance: vi.fn().mockResolvedValue(null), fetchSpotlightSnapshot: vi.fn(), newSpotlightRequestId: () => "req-1",
    dismiss: vi.fn(), cancel: vi.fn(), expand: vi.fn(), respondToPermission: vi.fn(),
}));
vi.mock("../../../quick-chat-spotlight/frontend/src/lib/wails-app-stub", () => ({
    HideWindow: mocks.hide, ResizeMode: mocks.resize, SetAutoHideOnBlur: vi.fn().mockResolvedValue(undefined),
    SetDarkMode: vi.fn().mockResolvedValue(undefined), SetSurfaceColor: vi.fn().mockResolvedValue(undefined),
}));
vi.mock("../../../quick-chat-spotlight/frontend/src/lib/spotlight-runtime", () => ({ EventsOn: () => () => {} }));
vi.mock("@/hooks/useVoiceInput", () => ({ useVoiceInput: () => ({ state: "idle", audioLevel: 0, error: null }) }));
vi.mock("@/components/typing-indicator", () => ({ TypingIndicator: () => null }));
vi.mock("@/components/voice-recording-indicator", () => ({ VoiceRecordingIndicator: () => null }));
vi.mock("@/components/grouped-stream-list", async () => {
    const { ChatSourcesLine } = await import("@/components/chat-sources-line");
    return {
        GroupedStreamList: ({ items }: { items: import("@/types").StreamItem[] }) => (
            <div>{items.map((item) => item.kind === "system" ? <ChatSourcesLine key={item.id} item={item} compact /> : <p key={item.id}>{item.content}</p>)}</div>
        ),
    };
});

const prep = { id: "prep:m1", title: "Prep for Lena", madeAt: "2026-11-21T09:40:00", forText: "For your meeting with Lena", forAt: "2026-11-21T15:00:00" };

beforeEach(async () => {
    await i18n.changeLanguage("en");
    mocks.resize.mockResolvedValue(undefined);
    mocks.hide.mockResolvedValue(undefined);
    Element.prototype.scrollIntoView = vi.fn();
});
afterEach(() => { cleanup(); vi.clearAllMocks(); });

function typeInPill(text: string) {
    const field = screen.getByPlaceholderText("Ask about your day, or correct Isoform");
    fireEvent.change(field, { target: { value: text } });
    return field;
}

// computer-chat v1.1.0 rule 7, redesign-work rule 15: typing "prep me for
// Lena" shows the prep that's already made, first, with Open and Open in Chat.
describe("Quick chat ready work first", { timeout: 30_000 }, () => {
    it("shows the made item with its words and opens it in Isoform or in Chat", async () => {
        mocks.fetchReady.mockResolvedValue(prep);
        mocks.openReady.mockResolvedValue(undefined);
        render(<App />);
        typeInPill("prep me for Lena");
        const row = await screen.findByTestId("quick-chat-ready");
        expect(mocks.fetchReady).toHaveBeenCalledWith("prep me for Lena", expect.anything());
        expect(row).toHaveTextContent(/^Ready · made at 9:40\sAM/);
        expect(row).toHaveTextContent("Prep for Lena");
        expect(row).toHaveTextContent("For your meeting with Lena");
        await waitFor(() => expect(mocks.resize).toHaveBeenCalledWith("ready"));
        fireEvent.click(screen.getByRole("button", { name: "Open" }));
        await waitFor(() => expect(mocks.openReady).toHaveBeenCalledWith("prep:m1", "prep me for Lena", false));
        await waitFor(() => expect(mocks.hide).toHaveBeenCalled());
        fireEvent.click(screen.getByRole("button", { name: "Open in Chat" }));
        await waitFor(() => expect(mocks.openReady).toHaveBeenCalledWith("prep:m1", "prep me for Lena", true));
    });

    it("still sends the question on Return", async () => {
        mocks.fetchReady.mockResolvedValue(prep);
        mocks.submit.mockResolvedValue({ sessionLocalId: "main" });
        mocks.subscribe.mockReturnValue(() => {});
        render(<App />);
        const field = typeInPill("prep me for Lena");
        await screen.findByTestId("quick-chat-ready");
        fireEvent.keyDown(field, { key: "Enter" });
        await waitFor(() => expect(mocks.submit).toHaveBeenCalledWith("prep me for Lena", "", undefined, undefined, "req-1"));
        expect(mocks.openReady).not.toHaveBeenCalled();
        await waitFor(() => expect(screen.queryByTestId("quick-chat-ready")).toBeNull());
    });

    it("shows nothing extra without a match", async () => {
        mocks.fetchReady.mockResolvedValue(null);
        render(<App />);
        typeInPill("what is on today");
        await waitFor(() => expect(mocks.fetchReady).toHaveBeenCalled());
        expect(screen.queryByTestId("quick-chat-ready")).toBeNull();
        expect(screen.queryByTestId("quick-chat-ready-failed")).toBeNull();
        expect(mocks.resize).not.toHaveBeenCalledWith("ready");
    });

    it("says a failed lookup, with Try again, never as nothing there", async () => {
        mocks.fetchReady.mockRejectedValueOnce(new Error("offline")).mockResolvedValueOnce(prep);
        render(<App />);
        typeInPill("prep me for Lena");
        expect(await screen.findByRole("alert")).toHaveTextContent("Couldn't check your ready work.");
        fireEvent.click(screen.getByRole("button", { name: "Try again" }));
        expect(await screen.findByTestId("quick-chat-ready")).toHaveTextContent("Prep for Lena");
    });
});

// computer-chat v1.2.0 rule 5: the panel's answer has its sources line.
describe("Quick chat sources", () => {
    it("shows the sources line the main chat wrote for this exchange", () => {
        let emit!: (event: StreamEvent, sequence: number) => void;
        mocks.subscribe.mockImplementation((_id, _after, callback) => { emit = callback; return () => {}; });
        render(<ConversationPanel detection={null} userPrompt="" sessionLocalId="main" onClose={() => {}} />);
        act(() => emit({ kind: "agent_event", data: { type: "assistant", content: "Sam asked for the deck." } }, 1));
        const row = { id: "sources:x", kind: "system", toolName: "main_chat_sources", sortOrder: 4,
            toolInput: JSON.stringify({ sources: [{ kind: "person", id: "p1", title: "Sam", target: { kind: "person", id: "p1" } }] }) };
        act(() => emit({ kind: "item", data: row } as StreamEvent, 2));
        act(() => emit({ kind: "item", data: row } as StreamEvent, 3));
        expect(screen.getByText("Sam asked for the deck.")).toBeInTheDocument();
        const lines = document.querySelectorAll("[data-chat-sources]");
        expect(lines).toHaveLength(1);
        expect(lines[0]).toHaveTextContent("Sam");
    });
});

// computer-chat v1.0.0 rule 11: work asked for in Quick chat shows Chat's card
// in the panel, and Hand it to opens the sheet there, not in Isoform's window.
describe("Quick chat hand-off card", () => {
    it("goes through the panel's route, and No answers the card", async () => {
        const fetchMock = vi.fn(async (_url: string, init?: RequestInit) => {
            const body = JSON.parse(String(init?.body ?? "{}")) as { op: string };
            const result = body.op === "enabled" ? true
                : body.op === "your_agents" ? { agents: [{ id: "claude", name: "Claude" }], choices: { else: "claude" } }
                : body.op === "answer_card" ? "declined" : null;
            return new Response(JSON.stringify({ result }), { status: 200 });
        });
        vi.stubGlobal("fetch", fetchMock);
        const { workOnIt } = await vi.importActual<typeof import("../../../quick-chat-spotlight/frontend/src/api/yansu")>("../../../quick-chat-spotlight/frontend/src/api/yansu");
        await expect(workOnIt("answer_card", { itemId: "card-1", answer: "declined" })).resolves.toBe("declined");
        const [url, init] = fetchMock.mock.calls[0];
        expect(String(url)).toContain("/api/spotlight/work-on-it");
        expect(JSON.parse(String(init?.body))).toEqual({ op: "answer_card", itemId: "card-1", answer: "declined" });
        fetchMock.mockResolvedValueOnce(new Response(JSON.stringify({ ok: false, error: "closed eyes is on" }), { status: 409 }));
        await expect(workOnIt("prepare", { input: {} })).rejects.toThrow("closed eyes");
        vi.unstubAllGlobals();
    });

    it("gives the card and the sheet real bindings in the panel", async () => {
        const stub = await vi.importActual<typeof import("../../../quick-chat-spotlight/frontend/src/lib/wails-app-stub")>("../../../quick-chat-spotlight/frontend/src/lib/wails-app-stub");
        mocks.workOnIt.mockResolvedValue("declined");
        await stub.AnswerChatHandoffCard("card-1", "declined");
        await stub.PrepareWorkOnIt({ sourceKind: "chat_card", sourceId: "card-1", title: "Draft", detail: "d" });
        await stub.SendWorkOnIt("task-1", "claude", "task_link");
        await stub.GetWorkOnItEnabled();
        await stub.GetYourAgents();
        await stub.GetWorkOnItAgents();
        await stub.NativeClipboardSetText("Draft");
        expect(mocks.workOnIt.mock.calls).toEqual([
            ["answer_card", { itemId: "card-1", answer: "declined" }],
            ["prepare", { input: { sourceKind: "chat_card", sourceId: "card-1", title: "Draft", detail: "d" } }],
            ["send", { taskId: "task-1", agentId: "claude", method: "task_link" }],
            ["enabled"],
            ["your_agents"],
            ["agents"],
            ["copy", { text: "Draft" }],
        ]);
    });
});
