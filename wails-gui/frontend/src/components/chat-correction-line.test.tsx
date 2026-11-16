import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import i18n from "@/i18n";
import type { StreamItem } from "@/types";
import { CHAT_CORRECTION_TOOL } from "@/lib/stream-helpers";
import { ChatCorrectionLine, ChatCorrectionUndoReply } from "./chat-correction-line";

const mocks = vi.hoisted(() => ({ undo: vi.fn(async () => "undone") }));
vi.mock("@/lib/wails-shim-app", () => ({ UndoChatCorrection: mocks.undo }));

function line(payload: Record<string, unknown>): StreamItem {
    return { id: "correction:main:toolu_1", kind: "system", toolName: CHAT_CORRECTION_TOOL, toolInput: JSON.stringify(payload), content: "" } as StreamItem;
}

beforeEach(async () => {
    vi.clearAllMocks();
    await i18n.changeLanguage("en");
});

describe("ChatCorrectionLine", () => {
    it("says what changed and where, and Undo takes it back", async () => {
        render(<ChatCorrectionLine item={line({ where: "Who you know › Lena, Priya", memoryId: "memory-1" })} />);
        expect(screen.getByText("Changed: Who you know › Lena, Priya")).toBeInTheDocument();
        fireEvent.click(screen.getByRole("button", { name: "Undo" }));
        await waitFor(() => expect(screen.getByRole("status")).toHaveTextContent("Changed: Who you know › Lena, Priya · Undone"));
        expect(mocks.undo).toHaveBeenCalledWith("correction:main:toolu_1");
    });

    it("an undone correction offers no Undo", () => {
        render(<ChatCorrectionLine item={line({ where: "About you", status: "undone" })} />);
        expect(screen.queryByRole("button", { name: "Undo" })).toBeNull();
    });

    it("says so when Undo fails", async () => {
        mocks.undo.mockRejectedValueOnce(new Error("no"));
        render(<ChatCorrectionLine item={line({ where: "About you" })} />);
        fireEvent.click(screen.getByRole("button", { name: "Undo" }));
        expect(await screen.findByRole("alert")).toHaveTextContent("Couldn't undo it.");
    });

    it("Undo answers in the thread", () => {
        render(<ChatCorrectionUndoReply item={{ id: "correction-undo:x", kind: "system", toolName: "main_chat_correction_undo", toolInput: JSON.stringify({ where: "Prep for Lena" }), content: "" } as StreamItem} />);
        expect(screen.getByText("Undone. Prep for Lena is as it was.")).toBeInTheDocument();
        expect(screen.getByText("Changed back: Prep for Lena")).toBeInTheDocument();
    });

    it("reads in Chinese", async () => {
        await i18n.changeLanguage("zh-CN");
        render(<ChatCorrectionLine item={line({ where: "About you" })} />);
        expect(screen.getByRole("button", { name: "撤销" })).toBeInTheDocument();
    });
});
