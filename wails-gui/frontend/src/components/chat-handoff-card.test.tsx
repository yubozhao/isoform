import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import i18n from "@/i18n";
import type { StreamItem } from "@/types";
import { CHAT_HANDOFF_TOOL } from "@/lib/stream-helpers";
import { resetWorkOnItEnabled } from "@/components/work-on-it/work-on-it";
import { ChatHandoffCard, resetChatHandoffAgents } from "./chat-handoff-card";

const mocks = vi.hoisted(() => ({
    enabled: vi.fn(async () => true),
    agents: vi.fn(async () => [{ id: "claude", name: "Claude", methods: ["task_link"], default: true }]),
    yours: vi.fn(async () => ({ choices: { code: "codex", else: "" }, agents: [{ id: "codex", name: "Codex", kind: "code" }] })),
    prepare: vi.fn(async () => ({ taskId: "t1", text: "Draft the Q4 plan outline", agents: [{ id: "claude", name: "Claude", methods: ["task_link"], default: true }] })),
    send: vi.fn(async () => ({ recordId: "r1", method: "task_link", copied: false, via: "open_copy" })),
    answer: vi.fn(async (_id: string, value: string) => value),
    copy: vi.fn(async () => undefined),
}));
vi.mock("@/lib/wails-shim-app", () => ({
    GetWorkOnItEnabled: mocks.enabled,
    GetWorkOnItAgents: mocks.agents,
    GetYourAgents: mocks.yours,
    PrepareWorkOnIt: mocks.prepare,
    SendWorkOnIt: mocks.send,
    SetWorkOnItHandle: vi.fn(),
    AnswerChatHandoffCard: mocks.answer,
    NativeClipboardSetText: mocks.copy,
}));

function card(payload: Record<string, unknown>): StreamItem {
    return { id: "handoff-card:main:toolu_1", kind: "system", toolName: CHAT_HANDOFF_TOOL, toolInput: JSON.stringify(payload), content: "" } as StreamItem;
}
const q4 = { title: "Draft the Q4 plan outline", detail: "Outline the plan.", done: "An outline.", gets: ["your notes from Friday", "last quarter's plan"] };

beforeEach(async () => {
    vi.clearAllMocks();
    resetWorkOnItEnabled();
    resetChatHandoffAgents();
    await i18n.changeLanguage("en");
});

describe("ChatHandoffCard", () => {
    it("says what would go and to whom, and runs nothing until the sheet sends it", async () => {
        render(<ChatHandoffCard item={card(q4)} />);
        expect(screen.getByText("Draft the Q4 plan outline")).toBeInTheDocument();
        expect(await screen.findByText("For your agent: it goes to Claude with your notes from Friday and last quarter's plan.")).toBeInTheDocument();
        expect(mocks.prepare).not.toHaveBeenCalled();
        expect(mocks.send).not.toHaveBeenCalled();

        fireEvent.click(screen.getByRole("button", { name: "Hand it to Claude" }));
        await waitFor(() => expect(mocks.prepare).toHaveBeenCalledWith({ sourceKind: "chat_card", sourceId: "handoff-card:main:toolu_1", title: q4.title, detail: q4.detail, done: q4.done, kind: "else" }));
        expect(mocks.send).not.toHaveBeenCalled();
        fireEvent.click(await screen.findByRole("button", { name: "Send to Claude" }));
        await waitFor(() => expect(mocks.answer).toHaveBeenCalledWith("handoff-card:main:toolu_1", "handed"));
    });

    it("No closes the card without handing anything over", async () => {
        render(<ChatHandoffCard item={card(q4)} />);
        fireEvent.click(await screen.findByRole("button", { name: "No" }));
        expect(await screen.findByText("Not handed over.")).toBeInTheDocument();
        expect(mocks.answer).toHaveBeenCalledWith("handoff-card:main:toolu_1", "declined");
        expect(mocks.prepare).not.toHaveBeenCalled();
    });

    it("an answered card shows its answer and no buttons", async () => {
        render(<ChatHandoffCard item={card({ ...q4, status: "handed" })} />);
        expect(await screen.findByText("Handed to Claude.")).toBeInTheDocument();
        expect(screen.queryByRole("button", { name: "No" })).toBeNull();
    });

    it("offers Copy when no agent can take it", async () => {
        mocks.enabled.mockResolvedValueOnce(false);
        render(<ChatHandoffCard item={card(q4)} />);
        await act(async () => {});
        expect(screen.queryByRole("button", { name: /Hand it to/ })).toBeNull();
        fireEvent.click(screen.getByRole("button", { name: "Copy" }));
        await waitFor(() => expect(mocks.copy).toHaveBeenCalledWith("Draft the Q4 plan outline\n\nOutline the plan.\n\nAn outline."));
    });

    it("names the person's code agent for code work", async () => {
        render(<ChatHandoffCard item={card({ ...q4, title: "Fix the login bug", kind: "code" })} />);
        expect(await screen.findByRole("button", { name: "Hand it to Codex" })).toBeInTheDocument();
    });

    it("reads in Chinese", async () => {
        await i18n.changeLanguage("zh-CN");
        render(<ChatHandoffCard item={card(q4)} />);
        expect(await screen.findByRole("button", { name: "交给 Claude" })).toBeInTheDocument();
    });
});
