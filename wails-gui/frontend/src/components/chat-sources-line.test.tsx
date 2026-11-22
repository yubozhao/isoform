import { fireEvent, render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import i18n from "@/i18n";
import type { StreamItem } from "@/types";
import { CHAT_SOURCES_TOOL } from "@/lib/stream-helpers";
import { computerDayClock, setComputerClock } from "@/lib/brief-time";
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

    describe("sources beyond notes and people (computer-chat v1.0.0 rule 3)", () => {
        // Today at 2:05 PM local time, so the chip shows the time alone.
        const at = (() => { const d = new Date(); d.setHours(14, 5, 0, 0); return d.getTime(); })();
        const kinds = [
            { kind: "watch", target: { kind: "watch", id: "w1" }, en: "your watch", zh: "你的手表" },
            { kind: "activity", target: { kind: "activity", id: "s1" }, en: "your screen", zh: "你的屏幕" },
            { kind: "call", target: { kind: "activity", id: "s2" }, en: "a call", zh: "一次通话" },
            { kind: "mail", target: { kind: "mail" }, en: "your mail", zh: "你的邮件" },
            { kind: "calendar", target: { kind: "calendar" }, en: "your calendar", zh: "你的日历" },
            { kind: "brief", target: { kind: "today", id: "b1" }, en: "your brief", zh: "你的简报" },
        ];
        const timed = (kind: (typeof kinds)[number]) => ({ sources: [{ kind: kind.kind, id: `${kind.kind}-1`, title: "Pricing with Sam", at, target: kind.target }] });

        beforeEach(() => setComputerClock("12h"));

        it.each(kinds)("says when, from $kind, and opens it", (kind) => {
            const open = vi.fn();
            render(<ChatSourcesLine item={line(timed(kind))} onOpenTarget={open} />);
            const button = screen.getByRole("button", { name: `Source: ${kind.en}, Pricing with Sam, 2:05 PM` });
            expect(button).toHaveTextContent(`From ${kind.en} · Pricing with Sam, 2:05 PM ›`);
            fireEvent.click(button);
            // A watch conversation opens on its own day (#1458's openActivityConversation).
            const d = new Date(at);
            const day = `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;
            expect(open).toHaveBeenCalledWith(kind.target.kind === "watch" ? { ...kind.target, day } : kind.target);
        });

        it.each(kinds)("reads $kind in Chinese", async (kind) => {
            await i18n.changeLanguage("zh-CN");
            render(<ChatSourcesLine item={line(timed(kind))} />);
            const time = computerDayClock(new Date(at), "zh-CN");
            expect(screen.getByText(`来自${kind.zh} · Pricing with Sam，${time} ›`)).toBeInTheDocument();
        });

        it("uses the form without a time when a source has none", () => {
            render(<ChatSourcesLine item={line({ sources: [{ kind: "watch", id: "w1", title: "Pricing with Sam", target: { kind: "watch", id: "w1" } }] })} />);
            expect(screen.getByText("From your watch · Pricing with Sam ›")).toBeInTheDocument();
        });

        it("drops a kind it does not know, as an older row shows", () => {
            const { container } = render(<ChatSourcesLine item={line({ sources: [{ kind: "rumor", id: "x", title: "Made up", target: { kind: "none" } }] })} />);
            expect(container.firstChild).toBeNull();
        });
    });

    it("opens a watch conversation on its own day (Activity's openActivityConversation)", () => {
        const open = vi.fn();
        const at = new Date(2026, 10, 19, 14, 5).getTime();
        render(<ChatSourcesLine item={line({ sources: [{ kind: "watch", id: "conv-1", title: "Pricing with Sam", at, target: { kind: "watch", id: "conv-1" } }] })} onOpenTarget={open} />);
        fireEvent.click(screen.getByRole("button", { name: /Pricing with Sam/ }));
        expect(open).toHaveBeenCalledWith({ kind: "watch", id: "conv-1", day: "2026-11-19" });
    });
});
