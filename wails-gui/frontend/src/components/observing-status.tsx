import { useCallback, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { EventsOn } from "../../wailsjs/runtime/runtime";
import {
    GetPlatformPermissions,
    PauseObservationFor,
    ResumeObservation,
} from "../../wailsjs/go/main/App";
import { GetBackgroundListening } from "@/lib/page-queries";
import {
    Popover,
    PopoverContent,
    PopoverTrigger,
} from "@/components/ui/popover";
import type { PermissionItem, PlatformPermissions } from "@/types";
import { Button } from "@/components/ui/button";
import { useCaptureCounts } from "@/hooks/useCaptureCounts";
import { briefClockTime } from "@/lib/brief-time";
import { doingNowKey, useDoingNow } from "@/hooks/useDoingNow";

const HOVER_CLOSE_DELAY_MS = 120;
// Hover-intent delay before the popover opens. Long enough that brushing the
// cursor past the row while navigating the sidebar doesn't pop the tooltip;
// keyboard focus still opens it immediately (deliberate intent).
const HOVER_OPEN_DELAY_MS = 500;

// The native activity monitor only runs when BOTH a screen-capture permission
// and an input-observation permission are enabled (see
// startActivityMonitorIfPermitted in the Go side). Mirror that pairing here so
// the badge reflects what's actually happening, not just intent.
const SCREEN_KEYS = new Set(["screen_recording", "screen_capture", "pipewire"]);
const INPUT_KEYS = new Set(["accessibility", "input_automation", "uinput"]);

function isScreenObserving(items: PermissionItem[] | undefined): boolean {
    if (!items) return false;
    const enabledToggle = (keys: Set<string>) =>
        items.some((p) => keys.has(p.key) && p.toggleable && p.enabled);
    return enabledToggle(SCREEN_KEYS) && enabledToggle(INPUT_KEYS);
}

/**
 * Sidebar status shown while Isoform is actively observing the desktop in the
 * background. Appears when the user has enabled microphone background
 * listening or any platform screen-observation toggle in Security & Privacy.
 * The popover leads with the observation state shared with the menu bar,
 * followed by the existing details and the timed pause controls.
 */
export function ObservingStatus({ collapsed = false, onOpenSettings }: { collapsed?: boolean; onOpenSettings?: () => void }) {
    const { i18n } = useTranslation();
    const { t: tt } = useTranslation("today");
    const { t } = useTranslation("app");
    const [permissions, setPermissions] = useState<PlatformPermissions | null>(
        null,
    );
    const [backgroundListening, setBackgroundListening] = useState(false);
    const doingNow = useDoingNow();
    // Pause the dot's pulse while the window is hidden: an infinite animation
    // keeps the WKWebView render loop awake (and burning CPU) even when nobody
    // can see it. visibilitychange fires when the Wails window is hidden/minimized.
    const [hidden, setHidden] = useState(
        typeof document !== "undefined" && document.hidden,
    );
    useEffect(() => {
        const onVisibility = () => setHidden(document.hidden);
        document.addEventListener("visibilitychange", onVisibility);
        return () =>
            document.removeEventListener("visibilitychange", onVisibility);
    }, []);

    const refreshPermissions = useCallback(() => {
        GetPlatformPermissions()
            .then(setPermissions)
            .catch(() => {});
    }, []);

    const refreshBackgroundListening = useCallback(() => {
        GetBackgroundListening()
            .then((v) => setBackgroundListening(!!v))
            .catch(() => {});
    }, []);

    useEffect(() => {
        refreshPermissions();
        refreshBackgroundListening();

        const offPerm = EventsOn("permissions:changed", () =>
            refreshPermissions(),
        );
        const offAudio = EventsOn(
            "background-listening:changed",
            (enabled: boolean) => {
                setBackgroundListening(!!enabled);
            },
        );

        const onFocus = refreshPermissions;
        const onVisibility = () => {
            if (document.visibilityState === "visible") onFocus();
        };
        window.addEventListener("focus", onFocus);
        document.addEventListener("visibilitychange", onVisibility);

        return () => {
            offPerm();
            offAudio();
            window.removeEventListener("focus", onFocus);
            document.removeEventListener("visibilitychange", onVisibility);
        };
    }, [refreshPermissions, refreshBackgroundListening]);

    const screenObserving = isScreenObserving(permissions?.items);
    const observing = backgroundListening || screenObserving;

    let description: string;
    if (backgroundListening && screenObserving) {
        description = t("observingStatus.description.screenAndAudio");
    } else if (screenObserving) {
        description = t("observingStatus.description.screen");
    } else {
        description = t("observingStatus.description.audio");
    }

    const doingNowText = doingNow
        ? (t(`observingStatus.doingNow.${doingNowKey(doingNow)}` as never, {
              app: doingNow.app ?? "",
              title: doingNow.title ?? "",
          } as never) as unknown as string)
        : null;

    const [open, setOpen] = useState(false);
    const openTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
    const closeTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);

    const cancelOpen = useCallback(() => {
        if (openTimerRef.current) {
            clearTimeout(openTimerRef.current);
            openTimerRef.current = null;
        }
    }, []);

    const cancelClose = useCallback(() => {
        if (closeTimerRef.current) {
            clearTimeout(closeTimerRef.current);
            closeTimerRef.current = null;
        }
    }, []);

    // Open only after the hover-intent delay elapses, so a quick fly-over
    // doesn't trigger the tooltip.
    const scheduleOpen = useCallback(() => {
        cancelOpen();
        openTimerRef.current = setTimeout(() => {
            setOpen(true);
            openTimerRef.current = null;
        }, HOVER_OPEN_DELAY_MS);
    }, [cancelOpen]);

    const scheduleClose = useCallback(() => {
        cancelClose();
        // Leaving aborts any pending open so a quick in-and-out can't pop later.
        cancelOpen();
        closeTimerRef.current = setTimeout(() => {
            setOpen(false);
            closeTimerRef.current = null;
        }, HOVER_CLOSE_DELAY_MS);
    }, [cancelClose, cancelOpen]);

    useEffect(
        () => () => {
            cancelOpen();
            cancelClose();
        },
        [cancelOpen, cancelClose],
    );

    const { counts, loadFailed, refresh } = useCaptureCounts(open);
    const [changing, setChanging] = useState(false);
    const [changeFailed, setChangeFailed] = useState(false);
    const paused = doingNow?.kind === "paused";
    const until = doingNow?.pausedUntil ? briefClockTime(new Date(doingNow.pausedUntil).toISOString(), i18n.language, tt) : null;
    const stateLabel = paused ? (until ? t("observingStatus.pausedUntil", { time: until }) : t("observingStatus.paused")) : observing ? t("observingStatus.label") : t("observingStatus.notObserving");
    const change = async (minutes?: number) => {
        setChanging(true);
        setChangeFailed(false);
        try {
            if (minutes) await PauseObservationFor(minutes);
            else await ResumeObservation();
            refresh();
        } catch { setChangeFailed(true); }
        finally { setChanging(false); }
    };

    // Fixed above the user row so status stays visible while lists scroll.
    return (
        <div className="shrink-0 pt-2 px-3 w-full">
            <Popover modal={false} open={open} onOpenChange={setOpen}>
                <PopoverTrigger asChild>
                    <Button variant="quiet" size={collapsed ? "icon" : "sm"}
                        type="button"
                        onMouseEnter={() => {
                            cancelClose();
                            scheduleOpen();
                        }}
                        onClick={(event) => { event.preventDefault(); cancelOpen(); cancelClose(); setOpen(true); }}
                        onMouseLeave={scheduleClose}
                        onFocus={() => {
                            cancelClose();
                            cancelOpen();
                            setOpen(true);
                        }}
                        className={`group/observing flex items-center font-normal text-left rounded-lg outline-none hover:bg-surface-hover transition-colors focus-visible:ring-1 focus-visible:ring-border-focus ${collapsed ? "w-8 h-7 p-0 justify-center" : "justify-start gap-2 w-full px-2 h-[30px]"}`}
                        aria-label={collapsed ? stateLabel : t("observingStatus.ariaLabel", { state: stateLabel })}
                        title={stateLabel}
                    >
                        {/* Green "live" dot (macOS recording convention),
                            centered in the same 14px slot the nav row icons
                            use so the status sits on the sidebar grid instead
                            of floating beside it. The label stays quiet
                            chrome gray — it's a status, not an action. */}
                        <span
                            aria-hidden
                            className="flex size-3.5 shrink-0 items-center justify-center"
                        >
                            <span
                                className={`size-2 rounded-full ${paused ? "bg-warning" : observing ? "observing-dot bg-automation" : "bg-muted"}`}
                                style={{
                                    animationPlayState: hidden
                                        ? "paused"
                                        : "running",
                                }}
                            />
                        </span>
                        {!collapsed && <span className="text-[12.5px] text-muted transition-colors group-hover/observing:text-ink">{stateLabel}</span>}
                    </Button>
                </PopoverTrigger>
                <PopoverContent
                    side="top"
                    align="start"
                    sideOffset={8}
                    avoidCollisions={false}
                    collisionPadding={12}
                    onMouseEnter={cancelClose}
                    onMouseLeave={scheduleClose}
                    onOpenAutoFocus={(e) => e.preventDefault()}
                    onCloseAutoFocus={(e) => e.preventDefault()}
                    className="w-[320px] max-h-[var(--radix-popover-content-available-height)] overflow-y-auto p-3.5"
                >
                    <div className="text-[13.5px] font-semibold text-ink mb-2">
                        {stateLabel}
                    </div>
                    {doingNowText && !["watching", "idle", "paused"].includes(doingNow?.kind ?? "") && (
                        <p
                            data-testid="observing-doing-now"
                            className="text-[11px] leading-[1.55] text-ink mb-1"
                        >
                            {doingNowText}
                        </p>
                    )}
                    <p className="text-[12.5px] leading-[1.5] text-muted">
                        {description}
                    </p>
                    {!paused && counts && !counts.paused && !counts.notReady && <p className="text-[12.5px] text-muted mt-2">{t("observingStatus.counts", { screens: counts.sources?.find((row) => row.source === "screens")?.today ?? 0, minutes: counts.sources?.find((row) => row.source === "audioMinutes")?.today ?? 0 })}</p>}
                    {loadFailed && <p className="text-[12px] text-muted mt-2">{t("observingStatus.countsFailed")}</p>}
                    {changeFailed && <p role="alert" className="text-[12px] text-error mt-2">{t("observingStatus.changeFailed")}</p>}
                    <div className="flex flex-wrap gap-1.5 mt-3">
                        {paused ? <Button variant="outline" size="sm" disabled={changing} onClick={() => void change()}>{t("observingStatus.resume")}</Button> : <>
                            <Button variant="outline" size="sm" disabled={changing} onClick={() => void change(15)}>{t("observingStatus.pause15")}</Button>
                            <Button variant="outline" size="sm" disabled={changing} onClick={() => void change(60)}>{t("observingStatus.pause60")}</Button>
                        </>}
                        <Button variant="quiet" size="sm" onClick={() => { setOpen(false); onOpenSettings?.(); }}>{t("observingStatus.settings")}</Button>
                    </div>
                </PopoverContent>
            </Popover>
        </div>
    );
}
