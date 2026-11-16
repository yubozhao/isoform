import type React from "react";
import { X } from "lucide-react";
import { useTranslation } from "@/i18n";
import type { DetectionContext } from "../api/types";

interface Props {
    detection: DetectionContext | null;
    onRemove?: () => void;
}

/**
 * The screen chip inside the pill (computer-chat v1.0.0 rule 8):
 *   "About: what's on screen · {app}, {title}"  [x]
 * What's on screen goes with the question only while the chip is there;
 * x (or Backspace in an empty field) removes it. With no app context (Closed
 * Eyes on, a sensitive app, or the chip removed) the neutral Isoform label
 * shows instead.
 */
export function DetectionChip({ detection, onRemove }: Props) {
    const { t } = useTranslation("chat");
    if (!detection || !detection.appName) {
        return (
            <div className="detection-chip detection-chip--neutral" aria-hidden>
                <span className="detection-chip__yansu">{t("quickChat.brand")}</span>
            </div>
        );
    }

    const title = detection.windowTitle?.trim() ?? "";
    const label = title
        ? t("quickChat.aboutScreen", { app: detection.appName, title })
        : t("quickChat.aboutScreenApp", { app: detection.appName });

    return (
        <div
            className="detection-chip"
            data-screen-chip
            style={{ "--wails-draggable": "no-drag" } as React.CSSProperties}
        >
            <span className="detection-chip__icon" aria-hidden>
                <AppIcon name={detection.appName} />
            </span>
            <span className="detection-chip__label" title={label}>{label}</span>
            {onRemove ? (
                <button
                    type="button"
                    className="detection-chip__remove"
                    aria-label={t("quickChat.removeScreen")}
                    title={t("quickChat.removeScreen")}
                    onClick={onRemove}
                >
                    <X aria-hidden size={12} />
                </button>
            ) : null}
        </div>
    );
}

function AppIcon({ name }: { name: string }) {
    // Tiny inline SVG seed icons. Real product would map common app
    // names → vendor logos; for v1 we render a colored letter tile so
    // every app gets *something*.
    const initial = name.trim().charAt(0).toUpperCase() || "?";
    return (
        <svg width="14" height="14" viewBox="0 0 14 14" fill="none">
            <rect x="0.5" y="0.5" width="13" height="13" rx="3" fill="currentColor" opacity="0.18" />
            <text
                x="7"
                y="10"
                textAnchor="middle"
                fontSize="9"
                fontWeight="700"
                fill="currentColor"
            >
                {initial}
            </text>
        </svg>
    );
}
