import { measureToday } from "@/lib/today-telemetry";
import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { X } from "lucide-react";
import { Button } from "@/components/ui/button";
import { ListPastBriefs, type MobileBriefSummary } from "@/lib/wails-shim-app";
import { onPagePrivacyWithdrawal } from "@/lib/page-privacy-withdrawal";
import { briefKind } from "@/lib/brief-title";
import { clockTime } from "@/lib/brief-time";

type Translate = (key: string, options?: Record<string, unknown>) => string;

/** A past brief's row title: its kind, its day and its time. */
export function pastBriefTitle(row: Pick<MobileBriefSummary, "slot" | "asOf">, language: string, tr: Translate): string {
    const at = new Date(row.asOf);
    if (Number.isNaN(at.getTime())) return tr("todayBrief.slot.on_request");
    const kind = briefKind({ slot: row.slot, asOf: row.asOf });
    return tr("todayBrief.pending.lastBriefTitle", {
        brief: tr(`todayBrief.slot.${kind === "morning" || kind === "evening" ? kind : "on_request"}`),
        day: at.toLocaleDateString(language, { weekday: "short", month: "short", day: "numeric" }),
        time: clockTime(at, language, tr),
    });
}

/** The past briefs, newest first, read when shown (the panel beside Today
 *  and the brief in full's right column). */
export function PastBriefsList({ currentBriefId, onOpen, onWithdrawn }: {
    currentBriefId?: string;
    onOpen: (briefId: string, title: string) => void;
    onWithdrawn?: () => void;
}) {
    const { t, i18n } = useTranslation("today");
    const tr: Translate = (key, options) => t(key as never, options as never) as unknown as string;
    const [rows, setRows] = useState<MobileBriefSummary[] | null>(null);
    const [failed, setFailed] = useState(false);
    const [attempt, setAttempt] = useState(0);
    const withdrawnRef = useRef(onWithdrawn);
    withdrawnRef.current = onWithdrawn;
    useEffect(() => {
        let live = true;
        setFailed(false);
        Promise.resolve().then(() => ListPastBriefs()).then((listed) => {
            if (live) setRows(listed ?? []);
        }).catch(() => { if (live) setFailed(true); });
        const withdraw = onPagePrivacyWithdrawal(() => { live = false; setRows(null); withdrawnRef.current?.(); });
        return () => { live = false; withdraw(); };
    }, [attempt]);
    const language = i18n.language;
    const shown = (rows ?? []).filter((row) => row.briefId !== currentBriefId);
    return (
        <div className="flex flex-col gap-1">
            {shown.map((row) => {
                const title = pastBriefTitle(row, language, tr);
                return (
                    <button
                        key={row.briefId}
                        type="button"
                        data-testid="past-brief-row"
                        className="flex flex-col gap-0.5 rounded-lg px-2.5 py-2 text-left outline-none hover:bg-surface-hover focus-visible:ring-2 focus-visible:ring-border-focus"
                        onClick={() => measureToday("T05", "open", "opened", () => onOpen(row.briefId, title))}
                    >
                        <span className="text-[13px] font-medium text-ink">{title}</span>
                        {row.lead && <span className="line-clamp-2 text-[12.5px] leading-snug text-muted">{row.lead}</span>}
                    </button>
                );
            })}
            {rows && shown.length === 0 && !failed && <p data-testid="past-briefs-empty" className="px-2.5 text-[13px] text-muted">{tr("ctoday.pastBriefsEmpty")}</p>}
            {failed && <p role="alert" data-testid="past-briefs-failed" className="px-2.5 text-[13px] text-copper">{tr("ctoday.pastBriefsFailed")}{" "}<button type="button" className="font-medium text-accent hover:underline" onClick={() => setAttempt((n) => n + 1)}>{tr("ctoday.tryAgain")}</button></p>}
        </div>
    );
}

/**
 * Past briefs (computer-today v1.0.0, SH1), beside Today: the briefs the
 * phone's history lists, newest first. The brief on Today is left out; a row
 * opens its brief in this panel's place. Read only when opened, never on
 * Today's load.
 */
export function TodayPastBriefsPanel({ currentBriefId, onOpen, onClose }: {
    currentBriefId?: string;
    onOpen: (briefId: string, title: string) => void;
    onClose: () => void;
}) {
    const { t } = useTranslation("today");
    const { t: common } = useTranslation("common");
    const tr: Translate = (key, options) => t(key as never, options as never) as unknown as string;
    return (
        <aside data-testid="today-past-briefs-panel" className="flex h-full min-h-0 flex-col bg-background-accent">
            <header className="flex items-center justify-between gap-3 border-b border-border px-4 py-3">
                <h2 className="min-w-0 truncate text-sm font-semibold text-ink">{tr("ctoday.pastBriefs")}</h2>
                <Button size="icon" variant="quiet" aria-label={common("close")} onClick={() => measureToday("T05", "open", "opened", onClose)}><X className="size-4" /></Button>
            </header>
            <div className="flex min-h-0 flex-1 flex-col overflow-y-auto px-2 py-3">
                <PastBriefsList currentBriefId={currentBriefId} onOpen={onOpen} onWithdrawn={onClose} />
            </div>
        </aside>
    );
}
