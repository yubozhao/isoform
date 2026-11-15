import { recordTodayAction } from "@/lib/today-telemetry";
import { useCallback, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { Button } from "@/components/ui/button";
import { PortraitShape } from "@/components/about-you/portrait";
import { GetPortraitTodayCard, usePagePrivacyWithdrawal, usePageQueryRefresh } from "@/lib/page-queries";
import { pageQueryCache } from "@/lib/query-cache";
import { DismissPortraitTodayCard, type PortraitTodayCard } from "@/lib/wails-shim-app";

/** Today's portrait card (Go: GetPortraitTodayCard), read again when a
 *  portrait is written and withdrawn on any privacy sweep. */
export function useTodayPortraitCard(enabled: boolean) {
    const [card, setCard] = useState<PortraitTodayCard | null>(() => pageQueryCache.peek<PortraitTodayCard | null>("GetPortraitTodayCard") ?? null);
    const [reload, setReload] = useState(0);
    const privacyVersion = useRef(0);
    const refresh = useCallback(() => setReload((value) => value + 1), []);
    useEffect(() => {
        if (!enabled) return;
        let current = true;
        GetPortraitTodayCard()
            .then((next) => {
                if (current) setCard(next ?? null);
            })
            // A failed read keeps the card shown until the next read.
            .catch(() => {});
        return () => {
            current = false;
        };
    }, [enabled, reload]);
    usePageQueryRefresh(["GetPortraitTodayCard"], refresh, enabled);
    usePagePrivacyWithdrawal(useCallback(() => {
        privacyVersion.current++;
        setCard(null);
        refresh();
    }, [refresh]));
    const dismiss = useCallback(async () => {
        const version = privacyVersion.current;
        await DismissPortraitTodayCard();
        if (version !== privacyVersion.current) return;
        setCard(null);
        pageQueryCache.invalidate(["GetPortraitTodayCard"]);
    }, []);
    return { card: enabled ? card : null, dismiss };
}

/** "Your portrait is ready": the name, sentence 1, Read it and Not now.
 *  Shown once, and again only when the name changes (portrait v1.0.0, §10). */
export function TodayPortraitCard({ card, onRead, onDismiss }: {
    card: PortraitTodayCard;
    onRead: () => void;
    onDismiss: () => void | Promise<void>;
}) {
    const { t } = useTranslation(["today", "memory"]);
    const [busy, setBusy] = useState(false);
    const [failed, setFailed] = useState(false);
    const dismiss = async (read: boolean) => {
        if (busy) return;
        const started = performance.now();
        setBusy(true);
        setFailed(false);
        try {
            await onDismiss();
            if (read) onRead();
            recordTodayAction(read ? "T40" : "T41", read ? "open" : "do", read ? "opened" : "done", started);
        } catch {
            recordTodayAction(read ? "T40" : "T41", read ? "open" : "do", "failed", started);
            setFailed(true);
        } finally {
            setBusy(false);
        }
    };
    const name = t("memory:aboutYou.portrait.name", {
        first: t(`memory:aboutYou.portrait.words.${card.name[0]}` as "memory:aboutYou.portrait.words.blueprint"),
        second: t(`memory:aboutYou.portrait.words.${card.name[1]}` as "memory:aboutYou.portrait.words.builder"),
    });
    return (
        <section aria-label={t("today:portraitCard.ready")} data-testid="today-portrait-card" className="rounded-xl border border-border bg-panel px-[18px] py-4 shadow-[var(--shadow-card)]">
            <div className="flex items-center gap-3">
                <PortraitShape sides={card.sides} size={36} />
                <div className="min-w-0">
                    <div className="text-[11px] font-semibold uppercase tracking-[0.07em] text-accent">
                        {t(card.renamed ? "today:portraitCard.renamed" : "today:portraitCard.ready")}
                    </div>
                    <div className="text-[17px] font-semibold leading-[1.3] text-ink">{name}</div>
                </div>
            </div>
            <p className="m-0 mt-2.5 text-[13.5px] leading-[1.5] text-muted">{card.sentence.map((part) => part.text).join("")}</p>
            <div className="mt-3 flex flex-wrap gap-2">
                <Button size="sm" className="h-7 rounded-full px-3 text-[12.5px] font-medium" disabled={busy} onClick={() => void dismiss(true)}>{t("today:portraitCard.readIt")}</Button>
                <Button size="sm" variant="outline" className="h-7 rounded-full px-3 text-[12.5px] font-medium text-ink" disabled={busy} onClick={() => void dismiss(false)}>{t("today:portraitCard.notNow")}</Button>
            </div>
            {failed && <p role="alert" className="mt-2 text-[12px] text-error">{t("today:portraitCard.dismissFailed")}</p>}
        </section>
    );
}
