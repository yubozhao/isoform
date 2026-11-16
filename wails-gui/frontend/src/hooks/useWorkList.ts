import { useCallback, useEffect, useRef, useState } from "react";
import { GetWorkList, onLocalDayChange, usePageQueryRefresh } from "@/lib/page-queries";
import { pageQueryCache } from "@/lib/query-cache";
import { AnswerWork, UndoWork, type WorkAnswerInput, type WorkItem, type WorkList } from "@/lib/wails-shim-app";

/** Why an answer didn't go through, from the Go errors (work_answers.go). */
export type WorkAnswerError = "gone" | "changed" | "undoClosed" | "invalid" | "agentAway" | "failed";

export function workAnswerError(error: unknown): WorkAnswerError {
    const text = error instanceof Error ? error.message : String(error ?? "");
    if (text.includes("no longer in Your work")) return "gone";
    if (text.includes("answered or changed")) return "changed";
    if (text.includes("can no longer be undone")) return "undoClosed";
    if (text.includes("doesn't fit this item")) return "invalid";
    if (text.includes("isn't connected")) return "agentAway";
    return "failed";
}

export interface UseWorkListResult {
    list: WorkList | null;
    loading: boolean;
    answer: (id: string, input: WorkAnswerInput) => Promise<WorkAnswerError | null>;
    /** The computer's own words for the last answer that failed. */
    failedText: string;
    undo: (id: string) => Promise<WorkAnswerError | null>;
}

/**
 * Your work on Today (computer-today v1.0.0, SH2; B2 and B3): the snapshot
 * from the shared page cache, read again on work:updated, work:answered and
 * the local day change. A failed read keeps what is shown (the cache retries
 * Today reads by itself). An answer replaces its item at once with the one
 * the computer returns, so the list never waits on the next read.
 */
export function useWorkList(enabled: boolean): UseWorkListResult {
    const [list, setList] = useState<WorkList | null>(() => pageQueryCache.peek("GetWorkList") ?? null);
    const [loading, setLoading] = useState(() => pageQueryCache.peek("GetWorkList") === undefined);
    const version = useRef(0);
    const [failedText, setFailedText] = useState("");
    const refresh = useCallback(async () => {
        if (!enabled) return;
        const current = ++version.current;
        try {
            const next = await GetWorkList();
            if (current === version.current && next) setList(next);
        } catch {
            // Keep the last list; the page cache reads it again by itself.
        } finally {
            if (current === version.current) setLoading(false);
        }
    }, [enabled]);
    usePageQueryRefresh(["GetWorkList"], refresh, enabled);
    useEffect(() => { void refresh(); }, [refresh]);
    useEffect(() => {
        if (!enabled) return;
        // A new day keeps the list on screen and reads it again (the
        // subscription above runs the read).
        return onLocalDayChange(() => pageQueryCache.invalidate(["GetWorkList"], false));
    }, [enabled, refresh]);

    const replace = useCallback((item: WorkItem) => {
        version.current++;
        setList((current) => current ? { ...current, items: current.items.map((row) => row.id === item.id ? item : row) } : current);
    }, []);
    const run = useCallback(async (call: () => Promise<WorkItem>): Promise<WorkAnswerError | null> => {
        try {
            replace(await call());
            return null;
        } catch (error) {
            void refresh();
            setFailedText(error instanceof Error ? error.message : String(error ?? ""));
            return workAnswerError(error);
        }
    }, [replace, refresh]);
    const answer = useCallback((id: string, input: WorkAnswerInput) => run(() => AnswerWork(id, input)), [run]);
    const undo = useCallback((id: string) => run(() => UndoWork(id)), [run]);
    return { list, loading, answer, undo, failedText };
}
