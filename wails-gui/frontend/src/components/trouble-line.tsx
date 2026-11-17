import { useCallback, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { Button } from "@/components/ui/button";
import { OpenAssistantSignIn, OpenTroubleFix, type TroubleLine as Trouble } from "@/lib/wails-shim-app";
import { GetTrouble, usePageQueryRefresh } from "@/lib/page-queries";
import { pageQueryCache } from "@/lib/query-cache";
import { EventsOn } from "../../wailsjs/runtime/runtime";

/** Trouble is checked every minute and at once when Isoform comes to the
 *  front (computer-trouble v1.0.0 rule 7). */
const TROUBLE_EVERY_MS = 60_000;

const ASSISTANT_NAMES: Record<string, string> = { "claude-code": "Claude Code", codex: "Codex" };

/** Reads go through Today's page reads (lib/page-queries): the shared cache,
 *  the locked-screen gate and the retry backoff. The minute's check and
 *  trouble:changed only invalidate; the cache decides when to read. A failed
 *  read keeps the last list. */
export function useTrouble(): Trouble[] {
    const [lines, setLines] = useState<Trouble[]>([]);
    const read = useCallback(() => {
        GetTrouble().then((next) => setLines(next?.items ?? [])).catch(() => {});
    }, []);
    usePageQueryRefresh(["GetTrouble"], read);
    useEffect(() => {
        read();
        const recheck = () => pageQueryCache.invalidate(["GetTrouble"], false);
        const timer = window.setInterval(recheck, TROUBLE_EVERY_MS);
        const off = EventsOn("trouble:changed", recheck);
        return () => {
            window.clearInterval(timer);
            off();
        };
    }, [read]);
    return lines;
}

/** One line under Today's date, on an amber wash with no edge bar: what's
 *  wrong and since when, why, and the one button that fixes it; "{n} more ›"
 *  opens the rest (computer-trouble v1.0.0 rule 3). Nothing when all is well. */
export function TroubleLine({ lines: given }: { lines?: Trouble[] } = {}) {
    const read = useTrouble();
    const lines = given ?? read;
    const [open, setOpen] = useState(false);
    if (lines.length === 0) return null;
    const shown = open ? lines : lines.slice(0, 1);
    return (
        <div role="status" data-trouble-line className="rounded-lg bg-warning-bg px-3 py-2 text-[13px] text-ink">
            {shown.map((line) => <TroubleRow key={line.kind} line={line} />)}
            {!open && lines.length > 1 && <MoreButton count={lines.length - 1} onClick={() => setOpen(true)} />}
        </div>
    );
}

function MoreButton({ count, onClick }: { count: number; onClick: () => void }) {
    const { t } = useTranslation("app");
    return (
        <button type="button" data-trouble-more className="mt-1 text-[12px] text-warning hover:underline" onClick={onClick}>
            {t("trouble.more", { count })}
        </button>
    );
}

function TroubleRow({ line }: { line: Trouble }) {
    const { t, i18n } = useTranslation("app");
    const at = new Date(line.since);
    const time = Number.isNaN(at.getTime()) ? "" : at.toLocaleTimeString(i18n.language, { hour: "numeric", minute: "2-digit" });
    let what = "";
    let why = "";
    let fix: { label: string; run: () => void } | null = null;
    switch (line.kind) {
        case "screen":
            what = t("trouble.screen", { time });
            why = t("trouble.screenWhy");
            fix = { label: t("trouble.openSettings"), run: () => void OpenTroubleFix("screen").catch(() => {}) };
            break;
        case "signed_out": {
            const assistant = ASSISTANT_NAMES[line.assistant ?? ""] ?? line.assistant ?? "";
            what = t("trouble.signedOut", { assistant, time });
            if (line.assistant) fix = { label: t("trouble.signIn"), run: () => void OpenAssistantSignIn(line.assistant!).catch(() => {}) };
            break;
        }
        case "offline":
            what = t("trouble.offline", { time });
            break;
        case "phone":
            what = t("trouble.phone", { time });
            break;
    }
    return (
        <div data-trouble-kind={line.kind} className="flex items-center gap-3 py-0.5">
            <p className="flex-1 min-w-0">
                <b className="font-semibold">{what}</b>
                {why && <span className="text-muted"> {why}</span>}
            </p>
            {fix && (
                <Button size="sm" variant="outline" className="shrink-0" onClick={fix.run}>
                    {fix.label}
                </Button>
            )}
        </div>
    );
}
