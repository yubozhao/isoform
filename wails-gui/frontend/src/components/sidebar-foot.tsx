import { useCallback, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { EyeOff, Eye, Settings, Smartphone } from "lucide-react";
import { EventsOn } from "../../wailsjs/runtime/runtime";
import {
    GetMobileConnectionStatus,
    GetWatchRecordings,
    IsClosedEyesModeEnabled,
    SetClosedEyesModeEnabled,
    type MobileConnectionStatus,
    type WatchRecording,
} from "@/lib/wails-shim-app";
import { Button } from "@/components/ui/button";
import { MarkPose, type MarkPoseName } from "@/components/mark-motion/MarkPose";
import { PAUSE_OBSERVING_COMBO, comboToDisplayString } from "@/hooks/useKeybindings";

/** A watch recording this recent means the watch is listening now: it
 *  records by the hour (computer-map v1.0.0 §2, the watch line). */
const WATCH_LISTENING_WINDOW_MS = 75 * 60 * 1000;

export type PhoneLineState = "none" | "connected" | "offline";

export function phoneLineState(status: MobileConnectionStatus | null): PhoneLineState {
    if (!status || !status.enabled || (status.devices ?? []).length === 0) return "none";
    return status.relayState === "connected" ? "connected" : "offline";
}

/** The watch line shows only once a watch has sent audio. */
export function watchLineState(recordings: WatchRecording[] | null, now = Date.now()):
    { kind: "none" } | { kind: "listening" } | { kind: "heard"; at: Date } {
    const latest = (recordings ?? [])
        .map((r) => new Date(r.startedAt))
        .filter((d) => !Number.isNaN(d.getTime()))
        .sort((a, b) => b.getTime() - a.getTime())[0];
    if (!latest) return { kind: "none" };
    return now - latest.getTime() <= WATCH_LISTENING_WINDOW_MS ? { kind: "listening" } : { kind: "heard", at: latest };
}

/** The Closed Eyes state, kept in step with every surface that changes it. */
export function useClosedEyes() {
    const [closed, setClosed] = useState(false);
    useEffect(() => {
        IsClosedEyesModeEnabled().then((v) => setClosed(!!v)).catch(() => {});
        return EventsOn("closed-eyes:changed", (paused: boolean) => setClosed(!!paused));
    }, []);
    const toggle = useCallback(() => {
        setClosed((current) => {
            const next = !current;
            SetClosedEyesModeEnabled(next).catch(() => setClosed(current));
            return next;
        });
    }, []);
    return { closed, toggle };
}

const lineClass = "flex items-center gap-2 w-full px-2 h-7 text-[12px] text-muted rounded-lg";

/** The sidebar's foot (computer-map v1.0.0 §2.2): the phone and the watch in
 *  one line each, Closed Eyes as a button with its shortcut, and Settings. */
export function SidebarFoot({ collapsed, onOpenSettings, onOpenPhone, closedEyes, onToggleClosedEyes }: {
    collapsed: boolean;
    onOpenSettings: () => void;
    onOpenPhone: () => void;
    closedEyes: boolean;
    onToggleClosedEyes: () => void;
}) {
    const { t, i18n } = useTranslation("app");
    const [mobile, setMobile] = useState<MobileConnectionStatus | null>(null);
    const [recordings, setRecordings] = useState<WatchRecording[] | null>(null);

    useEffect(() => {
        const load = () => GetMobileConnectionStatus().then(setMobile).catch(() => {});
        load();
        return EventsOn("mobile:updated", load);
    }, []);
    useEffect(() => {
        const load = () => GetWatchRecordings().then(setRecordings).catch(() => {});
        load();
        window.addEventListener("focus", load);
        return () => window.removeEventListener("focus", load);
    }, []);

    const phone = phoneLineState(mobile);
    const watch = watchLineState(recordings);
    // The watch line's still pose (pass-questions v1.0.0 rule 4).
    const watchPose: MarkPoseName = closedEyes ? "closedEyes" : watch.kind === "listening" ? "listening" : "notListening";
    const poseLabel = t(`sidebar.pose.${watchPose}`);
    const phoneText = t(`sidebar.phone.${phone}`);
    const watchText = closedEyes
        ? t("sidebar.watch.closed")
        : watch.kind === "listening"
          ? t("sidebar.watch.listening")
          : watch.kind === "heard"
            ? t("sidebar.watch.heard", { time: watch.at.toLocaleTimeString(i18n.language, { hour: "numeric", minute: "2-digit" }) })
            : "";
    // One Closed Eyes shortcut, system-wide and in the app (design,
    // pass-questions v1.0.0): the hotkey Go installs, shown on the button.
    const shortcut = PAUSE_OBSERVING_COMBO ? comboToDisplayString(PAUSE_OBSERVING_COMBO) : "";
    const eyesLabel = closedEyes ? t("sidebar.closedEyesOn") : t("sidebar.closedEyes");
    const EyesIcon = closedEyes ? EyeOff : Eye;

    if (collapsed) {
        return (
            <div data-sidebar-foot className="flex flex-col items-center gap-1 w-full pb-1">
                <Button variant="quiet" size="icon" aria-label={phoneText} title={phoneText} onClick={onOpenPhone} data-phone-line={phone} className="relative">
                    <Smartphone className="size-[15px]" />
                    {/* The rail keeps the line's state as a dot: green connected, grey not (computer-window rule 4). */}
                    <span aria-hidden data-rail-dot={phone === "connected" ? "on" : "off"} className={`absolute right-1.5 top-1.5 size-1.5 rounded-full ${phone === "connected" ? "bg-success" : "bg-border-strong"}`} />
                </Button>
                {watch.kind !== "none" && (
                    <span role="img" aria-label={watchText} title={watchText} data-watch-line={watch.kind} data-watch-pose={watchPose} className="relative inline-flex size-8 items-center justify-center text-muted">
                        <MarkPose name={watchPose} />
                        <span aria-hidden data-rail-dot={watch.kind === "listening" && !closedEyes ? "on" : "off"} className={`absolute right-1.5 top-1.5 size-1.5 rounded-full ${watch.kind === "listening" && !closedEyes ? "bg-success" : "bg-border-strong"}`} />
                    </span>
                )}
                <Button variant="quiet" size="icon" aria-label={eyesLabel} aria-pressed={closedEyes} title={shortcut ? `${eyesLabel} ${shortcut}` : eyesLabel} onClick={onToggleClosedEyes} data-closed-eyes-button className={closedEyes ? "bg-surface-active text-ink" : ""}>
                    {/* Closed Eyes on the rail, with the shut mark while eyes are closed; never hidden. */}
                    {closedEyes ? <MarkPose name="closedEyes" size={15} label={t("sidebar.pose.closedEyes")} /> : <EyesIcon className="size-[15px]" />}
                </Button>
                <Button variant="quiet" size="icon" aria-label={t("userMenu.settings")} title={t("userMenu.settings")} onClick={onOpenSettings} data-sidebar-settings>
                    <Settings className="size-[15px]" />
                </Button>
            </div>
        );
    }

    return (
        <div data-sidebar-foot className="px-3 flex flex-col gap-0.5 pb-1">
            <button type="button" className={`${lineClass} hover:bg-surface-hover cursor-pointer text-left`} onClick={onOpenPhone} data-phone-line={phone}>
                <Smartphone className="size-[14px] shrink-0" />
                <span className="flex-1 min-w-0 truncate">{phoneText}</span>
                {phone === "connected" && <span aria-hidden className="size-1.5 shrink-0 rounded-full bg-success" />}
            </button>
            {watch.kind !== "none" && (
                <div className={lineClass} data-watch-line={watch.kind} data-watch-pose={watchPose}>
                    <MarkPose name={watchPose} label={poseLabel} className="shrink-0 text-accent" />
                    <span className="flex-1 min-w-0 truncate">{watchText}</span>
                </div>
            )}
            <Button variant="quiet" size="sm" aria-pressed={closedEyes} onClick={onToggleClosedEyes} data-closed-eyes-button
                className={`flex items-center justify-start font-normal gap-2 w-full px-2 h-8 text-[13px] rounded-lg transition-none cursor-pointer ${closedEyes ? "bg-surface-active text-ink" : "text-muted hover:bg-surface-hover"}`}>
                <EyesIcon className="size-[15px] shrink-0" />
                <span className="flex-1 min-w-0 text-left truncate">{eyesLabel}</span>
                {shortcut && <span className="ml-auto text-[11.5px] text-muted" data-closed-eyes-shortcut>{shortcut}</span>}
            </Button>
            <Button variant="quiet" size="sm" onClick={onOpenSettings} data-sidebar-settings
                className="flex items-center justify-start font-normal gap-2 w-full px-2 h-8 text-[13px] text-muted rounded-lg hover:bg-surface-hover transition-none cursor-pointer">
                <Settings className="size-[15px] shrink-0" />
                <span className="flex-1 min-w-0 text-left truncate">{t("userMenu.settings")}</span>
            </Button>
        </div>
    );
}
