import { useTranslation } from "@/i18n";
import { Button } from "@/components/ui/button";
import type { ReadyItem } from "../api/types";

/** The clock time a stamp says, in the person's language ("9:40 AM"). */
export function readyClock(at: string | undefined, language: string): string {
    const date = at ? new Date(at) : null;
    if (!date || Number.isNaN(date.getTime())) return "";
    return date.toLocaleTimeString(language, { hour: "numeric", minute: "2-digit" });
}

/** When the item is for: the time today, else the weekday and time. */
export function readyWhen(at: string | undefined, now: Date, language: string): string {
    const date = at ? new Date(at) : null;
    if (!date || Number.isNaN(date.getTime())) return "";
    if (date.toDateString() === now.toDateString()) return readyClock(at, language);
    return date.toLocaleString(language, { weekday: "short", hour: "numeric", minute: "2-digit" });
}

interface Props {
    item: ReadyItem | null;
    /** The lookup or an Open failed: shown with Try again, never as nothing. */
    failed: "read" | "open" | null;
    busy?: boolean;
    onOpen: () => void;
    onOpenInChat: () => void;
    onRetry: () => void;
    now?: Date;
}

/**
 * ReadyRow is ready work first (computer-chat v1.1.0 rule 7): when what the
 * person typed matches work Isoform already made, the item sits above the
 * pill, one line: "Ready · made at {time}", its title and when it's for,
 * with Open and Open in Chat. Return still sends the question.
 */
export function ReadyRow({ item, failed, busy = false, onOpen, onOpenInChat, onRetry, now = new Date() }: Props) {
    const { t, i18n } = useTranslation("chat");
    const language = i18n.language || "en";
    if (failed === "read" || (failed === "open" && !item)) {
        return (
            <div className="ready" role="alert" data-testid="quick-chat-ready-failed">
                <span className="ready__meta">{t(failed === "read" ? "quickChat.readyFailed" : "quickChat.readyOpenFailed")}</span>
                <button type="button" className="ready__link" onClick={onRetry}>{t("errors.actions.tryAgain")}</button>
                <style>{readyStyles}</style>
            </div>
        );
    }
    if (!item) return null;
    const made = readyClock(item.madeAt, language);
    const when = readyWhen(item.forAt, now, language);
    const forLine = [item.forText, when].filter(Boolean).join(" · ");
    return (
        <div className="ready" data-testid="quick-chat-ready" data-item-id={item.id}>
            <span className="ready__meta">{t("quickChat.ready", { time: made })}</span>
            <span className="ready__title" title={item.title}>{item.title}</span>
            {forLine && <span className="ready__meta ready__for">{forLine}</span>}
            {failed === "open" && <span className="ready__meta" role="alert">{t("quickChat.readyOpenFailed")}</span>}
            <span className="ready__actions">
                <Button type="button" size="sm" variant="quiet" className="text-[12.5px]" disabled={busy} onClick={onOpen}>{t("quickChat.open")}</Button>
                <button type="button" className="ready__link" disabled={busy} onClick={onOpenInChat}>{t("quickChat.openInChat")}</button>
            </span>
            <style>{readyStyles}</style>
        </div>
    );
}

const readyStyles = `
.ready {
    width: 768px;
    height: 52px;
    box-sizing: border-box;
    border-radius: 16px;
    background: var(--spotlight-surface);
    padding: 0 12px 0 20px;
    display: flex;
    align-items: center;
    gap: 10px;
    font-size: 13px;
    color: var(--ink);
    white-space: nowrap;
}
.ready__meta {
    color: var(--muted);
    font-size: 12.5px;
    flex-shrink: 0;
}
.ready__title {
    font-weight: 600;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
}
.ready__for {
    flex-shrink: 1;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
}
.ready__actions {
    margin-left: auto;
    display: inline-flex;
    align-items: center;
    gap: 4px;
    flex-shrink: 0;
}
.ready__link {
    border: none;
    background: transparent;
    padding: 0 8px;
    font: inherit;
    font-size: 12.5px;
    font-weight: 500;
    color: var(--muted);
    cursor: pointer;
}
.ready__link:hover:not(:disabled) {
    color: var(--ink);
}
.ready__link:disabled {
    opacity: 0.5;
    cursor: default;
}
`;
