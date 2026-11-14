import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import i18n from "@/i18n";
import type { StreamItem } from "@/types";
import { CHAT_DRAFT_TOOL, chatDraftMailto, getChatDraft } from "@/lib/stream-helpers";
import { ChatDraftCard } from "./main-chat-report-card";

const mocks = vi.hoisted(() => ({ copy: vi.fn(async () => undefined), open: vi.fn(async () => undefined) }));
vi.mock("@/lib/wails-shim-app", () => ({
    NativeClipboardSetText: mocks.copy,
    OpenExternalURL: mocks.open,
    ResolveUpcomingConfirm: vi.fn(),
}));

function draftItem(payload: Record<string, unknown>): StreamItem {
    return { id: "draft:1", kind: "system", toolName: CHAT_DRAFT_TOOL, toolInput: payload, content: "Draft to Dana. Nothing was sent." } as StreamItem;
}

beforeEach(async () => {
    vi.clearAllMocks();
    await i18n.changeLanguage("en");
});

describe("ChatDraftCard", () => {
    it("shows the draft, says nothing was sent, and only copies or opens the mail app", async () => {
        render(<ChatDraftCard item={draftItem({ to: "dana@example.com", channel: "mail", subject: "Launch memo", text: "Hi Dana,\nThe memo is attached." })} />);
        expect(screen.getByText("Draft to dana@example.com")).toBeInTheDocument();
        expect(screen.getByText(/Nothing was sent\./)).toBeInTheDocument();
        expect(screen.getByText("Subject: Launch memo")).toBeInTheDocument();
        expect(screen.queryByRole("button", { name: /send/i })).toBeNull();

        fireEvent.click(screen.getByRole("button", { name: "Copy" }));
        await waitFor(() => expect(screen.getByRole("button", { name: "Copied" })).toBeInTheDocument());
        expect(mocks.copy).toHaveBeenCalledWith("Launch memo\n\nHi Dana,\nThe memo is attached.");

        fireEvent.click(screen.getByRole("button", { name: "Open in mail app" }));
        expect(mocks.open).toHaveBeenCalledWith("mailto:dana%40example.com?subject=Launch%20memo&body=Hi%20Dana%2C%0AThe%20memo%20is%20attached.");
    });

    it("offers no mail-app button for a Slack or text message", () => {
        render(<ChatDraftCard item={draftItem({ to: "Dana", channel: "slack", text: "Thanks for today!" })} />);
        expect(screen.getByRole("button", { name: "Copy" })).toBeInTheDocument();
        expect(screen.queryByRole("button", { name: "Open in mail app" })).toBeNull();
    });

    it("says so when Copy fails", async () => {
        mocks.copy.mockRejectedValueOnce(new Error("denied"));
        render(<ChatDraftCard item={draftItem({ to: "Dana", channel: "message", text: "On my way." })} />);
        fireEvent.click(screen.getByRole("button", { name: "Copy" }));
        expect(await screen.findByRole("alert")).toHaveTextContent("Couldn't copy it.");
    });

    it("reads in Chinese", async () => {
        await i18n.changeLanguage("zh-CN");
        render(<ChatDraftCard item={draftItem({ to: "Dana", channel: "mail", text: "你好" })} />);
        expect(screen.getByText("给 Dana 的草稿")).toBeInTheDocument();
        expect(screen.getByRole("button", { name: "在邮件应用中打开" })).toBeInTheDocument();
    });
});

describe("chat draft payload", () => {
    it("reads only a well-formed draft row, from an object or a JSON string", () => {
        expect(getChatDraft(draftItem({ to: "Dana", channel: "fax", text: "Hi" }))?.channel).toBe("other");
        expect(getChatDraft({ kind: "system", toolName: CHAT_DRAFT_TOOL, toolInput: JSON.stringify({ to: "Dana", text: "Hi" }) } as StreamItem)?.to).toBe("Dana");
        expect(getChatDraft(draftItem({ to: "Dana", text: "  " }))).toBeNull();
        expect(getChatDraft({ kind: "assistant", toolName: CHAT_DRAFT_TOOL, toolInput: { to: "Dana", text: "Hi" } } as StreamItem)).toBeNull();
    });

    it("leaves the address out of the mail link when the recipient is a name", () => {
        expect(chatDraftMailto({ to: "Dana", channel: "mail", subject: "", text: "Hi" })).toBe("mailto:?body=Hi");
    });
});
