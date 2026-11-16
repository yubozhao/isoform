import { measureToday, recordTodayAction } from "@/lib/today-telemetry";
import { useCallback, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { Button } from "@/components/ui/button";
import { CanUndoTodayWeekBelief, CorrectTodayWeekBelief, UndoTodayWeekBelief } from "@/lib/wails-shim-app";
import { onPagePrivacyWithdrawal } from "@/lib/page-privacy-withdrawal";

type Receipt = { id: string; token: string; corrected: boolean; phase: "saving" | "saved" | "undoing" | "failed" };
let nextCorrelation = 0;

/** A receipt has no belief text. The owner's own memory event refreshes the
 * brief, while any other privacy event clears the receipt and pending result. */
export function useTodayWeekBelief(active: boolean) {
    const [receipt, setReceipt] = useState<Receipt | null>(null);
    const receiptRef = useRef<Receipt | null>(null);
    const generation = useRef(0);
    const ownEvents = useRef(new Set<string>());
    const verifyPending = useRef(false);
    const publish = useCallback((next: Receipt | null) => { receiptRef.current = next; setReceipt(next); }, []);
    const verify = useCallback(async (token: string, current: number) => {
        do {
            verifyPending.current = false;
            try { if (!await CanUndoTodayWeekBelief(token)) return false; } catch { return false; }
        } while (verifyPending.current && generation.current === current);
        return generation.current === current;
    }, []);
    useEffect(() => onPagePrivacyWithdrawal((event, payload) => {
        const update = payload as { correlation?: unknown; reason?: unknown } | null;
        const correlation = update?.correlation;
        if (event === "memory:withdrawn" && typeof correlation === "string" && ownEvents.current.has(correlation)) return;
        if (event === "memory:updated" && update?.reason === "index") {
            const shown = receiptRef.current;
            if (!shown) return;
            verifyPending.current = true;
            if (shown.phase === "saving" || shown.phase === "undoing") return;
            if (!shown.token) { publish(null); return; }
            const current = ++generation.current;
            publish({ ...shown, phase: "saving" });
            void verify(shown.token, current).then((ok) => {
                if (generation.current === current) publish(ok ? shown : null);
            });
            return;
        }
        generation.current++;
        verifyPending.current = false;
        ownEvents.current.clear();
        publish(null);
    }), [publish, verify]);
    useEffect(() => {
        if (!active) { generation.current++; verifyPending.current = false; ownEvents.current.clear(); publish(null); }
    }, [active, publish]);
    useEffect(() => () => { generation.current++; ownEvents.current.clear(); }, []);

    const save = useCallback(async (briefId: string, id: string, expectedText: string, words: string) => {
        const started = performance.now();
        const current = ++generation.current;
        verifyPending.current = false;
        if (ownEvents.current.size > 32) ownEvents.current.clear();
        const correlation = `today-week-${Date.now()}-${++nextCorrelation}`;
        ownEvents.current.add(correlation);
        publish({ id, token: "", corrected: !!words.trim(), phase: "saving" });
        try {
            const token = await CorrectTodayWeekBelief(briefId, id, expectedText, words, correlation);
            recordTodayAction("T83", "do", "done", started);
            const valid = !verifyPending.current || await verify(token, current);
            if (generation.current === current) publish(valid ? { id, token, corrected: !!words.trim(), phase: "saved" } : null);
        } catch {
            recordTodayAction("T83", "do", "failed", started);
            if (generation.current === current) publish({ id, token: "", corrected: false, phase: "failed" });
        }
    }, [publish, verify]);
    const undo = useCallback(async () => {
        if (!receipt || !receipt.token || (receipt.phase !== "saved" && receipt.phase !== "failed")) return;
        const started = performance.now();
        const current = ++generation.current;
        verifyPending.current = false;
        if (ownEvents.current.size > 32) ownEvents.current.clear();
        const correlation = `today-week-${Date.now()}-${++nextCorrelation}`;
        ownEvents.current.add(correlation);
        publish({ ...receipt, phase: "undoing" });
        try {
            await UndoTodayWeekBelief(receipt.token, correlation);
            recordTodayAction("T83", "do", "undone", started);
            if (generation.current === current) publish(null);
        } catch {
            recordTodayAction("T83", "do", "failed", started);
            const valid = !verifyPending.current || await verify(receipt.token, current);
            if (generation.current === current) publish(valid ? { ...receipt, phase: "failed" } : null);
        }
    }, [receipt, publish, verify]);
    const clear = useCallback(() => { generation.current++; verifyPending.current = false; publish(null); }, [publish]);
    return { receipt, save, undo, clear };
}

export type TodayWeekBeliefReview = ReturnType<typeof useTodayWeekBelief>;

export function TodayWeekBeliefReceipt({ review }: { review: TodayWeekBeliefReview }) {
    const { t } = useTranslation("memory");
    const receipt = review.receipt;
    if (!receipt) return null;
    if (receipt.phase === "saving" || receipt.phase === "undoing") return <span role="status" aria-busy="true" className="text-[13px] text-muted" />;
    if (receipt.phase === "failed") return <span role="alert" className="text-[13px] text-error">{t("aboutYou.review.answerFailed")} <Button variant="quiet" size="sm" onClick={() => receipt.token ? void review.undo() : measureToday("T83", "do", "stopped", review.clear)}>{t(receipt.token ? "aboutYou.review.undo" : "aboutYou.review.cancel")}</Button></span>;
    return <div className="flex flex-wrap items-center gap-1.5 text-[13px] text-muted" role="status">
        <span>{t(receipt.corrected ? "aboutYou.review.corrected" : "aboutYou.review.stoppedUsing")}</span>
        <Button variant="quiet" size="sm" onClick={() => void review.undo()}>{t("aboutYou.review.undo")}</Button>
    </div>;
}
