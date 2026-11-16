import { act, renderHook } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { workAnswerError } from "./useWorkList";

const mocks = vi.hoisted(() => ({ get: vi.fn(), answer: vi.fn(), undo: vi.fn() }));
vi.mock("@/lib/page-queries", () => ({ GetWorkList: mocks.get, onLocalDayChange: () => () => {}, usePageQueryRefresh: () => {} }));
vi.mock("@/lib/query-cache", () => ({ pageQueryCache: { peek: () => undefined, invalidate: vi.fn() } }));
vi.mock("@/lib/wails-shim-app", () => ({ AnswerWork: mocks.answer, UndoWork: mocks.undo }));

const item = (state: string) => ({ id: "a", state, order: 0 });

describe("useWorkList", () => {
    beforeEach(() => { mocks.get.mockReset(); mocks.answer.mockReset(); mocks.undo.mockReset(); });

    it("maps the computer's answer errors", () => {
        expect(workAnswerError(new Error("this item is no longer in Your work"))).toBe("gone");
        expect(workAnswerError("this item was answered or changed")).toBe("changed");
        expect(workAnswerError(new Error("this answer can no longer be undone"))).toBe("undoClosed");
        expect(workAnswerError(new Error("this answer doesn't fit this item"))).toBe("invalid");
        expect(workAnswerError(new Error("disk full"))).toBe("failed");
    });

    it("replaces the answered item at once and keeps the list when a read fails", async () => {
        const { useWorkList } = await import("./useWorkList");
        mocks.get.mockResolvedValueOnce({ items: [item("open")], toAnswer: 1, backFromAgents: 0 });
        const { result } = renderHook(() => useWorkList(true));
        await act(async () => {});
        expect(result.current.list?.items[0].state).toBe("open");
        mocks.answer.mockResolvedValueOnce(item("answered"));
        mocks.get.mockRejectedValueOnce(new Error("down"));
        let failed: unknown = "unset";
        await act(async () => { failed = await result.current.answer("a", { action: "yes" }); });
        expect(failed).toBeNull();
        expect(mocks.answer).toHaveBeenCalledWith("a", { action: "yes" });
        expect(result.current.list?.items[0].state).toBe("answered");
    });
});
