import { fireEvent, render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import i18n from "@/i18n";
import type { StreamItem } from "@/types";
import { CHAT_SOURCES_TOOL } from "@/lib/stream-helpers";
import { ChatSourcesLine } from "./chat-sources-line";

vi.mock("@/lib/wails-shim-app", () => ({}));

function line(payload: Record<string, unknown>): StreamItem {
    return { id: "sources:main:toolu_1", kind: "system", toolName: CHAT_SOURCES_TOOL, toolInput: JSON.stringify(payload), content: "" } as StreamItem;
}
const payload = {
    sources: [
        { kind: "memory", id: "memory-1", title: "Pricing with Sam", target: { kind: "memory", id: "memory-1" } },
        { kind: "person", id: "p1", title: "Sam Lee", target: { kind: "person", id: "p1" } },
    ],
    claims: [{ text: "three tiers", source: 0 }],
};

beforeEach(async () => {
    await i18n.changeLanguage("en");
});

describe("ChatSourcesLine", () => {
    it("says where the answer came from and opens each source", () => {
        const open = vi.fn();
        render(<ChatSourcesLine item={line(payload)} onOpenTarget={open} />);
        fireEvent.click(screen.getByRole("button", { name: "Source: your notes, Pricing with Sam" }));
        expect(open).toHaveBeenCalledWith({ kind: "memory", id: "memory-1" });
        expect(screen.getByRole("button", { name: "Source: Who you know, Sam Lee" })).toBeInTheDocument();
    });

    it("shows nothing for a row without sources", () => {
        const { container } = render(<ChatSourcesLine item={line({ sources: [] })} />);
        expect(container.firstChild).toBeNull();
    });

    it("reads in Chinese", async () => {
        await i18n.changeLanguage("zh-CN");
        render(<ChatSourcesLine item={line(payload)} />);
        expect(screen.getByText("来自你的笔记 · Pricing with Sam ›")).toBeInTheDocument();
    });
});
