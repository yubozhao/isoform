import { useCallback, useEffect, useRef, useState, type ReactNode } from "react";
import { Check, Copy, Loader2, RefreshCw } from "lucide-react";
import { describeMobileStatus, isLocalRelay } from "@/lib/mobile-status";
import { activeInterval } from "@/lib/app-activity";
import { QRCodeSVG } from "qrcode.react";
import { SettingsFailureLine } from "@/components/settings/settings-failure";
import { useTranslation } from "react-i18next";
import { NativeClipboardSetText } from "../../wailsjs/go/main/App";
import { BrowserOpenURL, ClipboardSetText, EventsOn } from "../../wailsjs/runtime/runtime";
import { IPHONE_APP_LINK } from "@/lib/iphone-app-link";
// The shim declares the iPhone bindings with the PROTOCOL.md section 12 types,
// so the calls and the state below agree on one shape.
import {
    ConfirmMobilePairing,
    ConfirmMobileScopeRequest,
    DisableMobileConnection,
    EnableMobileConnectionUsingRelay,
    GenerateMobileConnectionCode,
    GetMobileConnectionStatus,
    GetMobileKeepAwake,
    ResetMobileRemoteAccess,
    RevokeMobileDevice,
    SetMobileKeepAwake,
    type MobileBriefSchedule,
    type MobileConnectionStatus,
    type MobileDeviceView,
    type MobileKeepAwakeStatus,
    type MobilePendingPairing,
} from "@/lib/wails-shim-app";
import { SettingsAdvanced } from "./settings/settings-advanced";
import { SettingsCard } from "./settings/settings-primitives";
import { Button } from "./ui/button";
import {
    Dialog,
    DialogContent,
    DialogDescription,
    DialogHeader,
    DialogTitle,
} from "./ui/dialog";
import { Switch } from "./ui/switch";

const STATUS_POLL_MS = 15_000;
const CODE_TICK_MS = 1_000;
const IDLE_TICK_MS = 30_000;
// Go zero time ("0001-01-01T00:00:00Z") marks "never"; anything before 2000 is treated as unset.
const EARLIEST_TIMESTAMP = Date.UTC(2000, 0, 1);
export const CLOCK_PATTERN = /^([01]\d|2[0-3]):[0-5]\d$/;

// settings-review ST69: brief times read "7:00 AM", never the time field's "07:00 AM".
export function clockOptions(current: string): string[] {
    const clocks: string[] = [];
    for (let minutes = 0; minutes < 24 * 60; minutes += 15) {
        clocks.push(`${String(Math.floor(minutes / 60)).padStart(2, "0")}:${String(minutes % 60).padStart(2, "0")}`);
    }
    if (CLOCK_PATTERN.test(current) && !clocks.includes(current)) {
        clocks.push(current);
        clocks.sort();
    }
    return clocks;
}

export function clockLabel(clock: string, locale: string): string {
    const [hours, minutes] = clock.split(":").map(Number);
    return new Date(2000, 0, 1, hours, minutes).toLocaleTimeString(locale, { hour: "numeric", minute: "2-digit", hour12: true });
}
const REMOTE_ACCESS_STATE_DETAIL_TRANSLATION_PREFIX = "mobileConnection.remoteAccessStateDetail";
const REMOTE_ACCESS_OFF_TRANSLATION_PREFIX = "mobileConnection.remoteAccessOff";
export const TIME_INPUT_CLASS =
    "rounded-md border border-border bg-panel px-3 py-1.5 text-xs tabular-nums text-ink focus:outline-none focus:ring-1 focus:ring-accent disabled:opacity-50";
const RELAY_INPUT_CLASS =
    "w-full rounded-md border border-border bg-panel px-3 py-1.5 font-mono text-xs text-ink placeholder:text-muted focus:outline-none focus:ring-1 focus:ring-accent disabled:opacity-50";

function relayHostname(relayUrl: string): string {
    try {
        return new URL(relayUrl).hostname;
    } catch {
        return "";
    }
}

// Plain names for what a paired iPhone may do (PROTOCOL.md sections 10, 15.1
// and 20.1). desk:read and decide are retired: older phones still hold them, no
// route accepts them, so they are not listed.
const SCOPE_LABELS = {
    "brief:read": "mobileConnection.scopeBriefs",
    "brief:refresh": "mobileConnection.scopeRefresh",
    "presence:read": "mobileConnection.scopePresence",
    push: "mobileConnection.scopeNotifications",
    "chat:read": "mobileConnection.scopeChatRead",
    "chat:write": "mobileConnection.scopeChatWrite",
    "memory:correct": "mobileConnection.scopeMemoryCorrect",
    "handoff:create": "mobileConnection.scopeHandoff",
    "brief:feedback": "mobileConnection.scopeBriefFeedback",
    "offers:read": "mobileConnection.scopeOffersRead",
    "offers:decide": "mobileConnection.scopeOffersDecide",
    "goals:read": "mobileConnection.scopeGoalsRead",
    "goals:write": "mobileConnection.scopeGoalsWrite",
    "library:read": "mobileConnection.scopeLibraryRead",
    "chat:correct": "mobileConnection.scopeChatCorrect",
} as const;

// Same chips as the Chat apps list: green while connected, neutral otherwise.
const STATUS_CHIP_ON_CLASS =
    "inline-flex items-center rounded-full bg-success-bg px-2 py-0.5 text-[10px] font-medium text-success";
const STATUS_CHIP_CLASS =
    "inline-flex items-center rounded-full bg-ink/[0.05] px-2 py-0.5 text-[10px] font-medium text-ink/55";

type KnownScope = keyof typeof SCOPE_LABELS;

type ErrorScope =
    | "statusLoad"
    | "relay"
    | "code"
    | "copy"
    | "approval"
    | "scopeRequest"
    | "revoke"
    | "remoteReset"
    | "remoteSwitch"
    | "keepAwake";

type StatusAction =
    | "enable"
    | "disable"
    | "code"
    | "approve"
    | "decline"
    | "scope-allow"
    | "scope-decline"
    | "revoke"
    | "remote-reset";

export type ScheduleDraft = Pick<MobileBriefSchedule, "enabled" | "morning" | "evening" | "feedEnabled" | "adaptiveTiming">;

function isKnownScope(scope: string): scope is KnownScope {
    return Object.prototype.hasOwnProperty.call(SCOPE_LABELS, scope);
}

export function scheduleDraftOf(schedule: MobileBriefSchedule): ScheduleDraft {
    return {
        enabled: schedule.enabled,
        morning: schedule.morning,
        evening: schedule.evening,
        feedEnabled: schedule.feedEnabled,
        adaptiveTiming: schedule.adaptiveTiming,
    };
}

export function sameDraft(a: ScheduleDraft, b: ScheduleDraft): boolean {
    return a.enabled === b.enabled && a.morning === b.morning && a.evening === b.evening && a.feedEnabled === b.feedEnabled && a.adaptiveTiming === b.adaptiveTiming;
}

function parseTimestamp(value: string): number | null {
    if (!value) return null;
    const time = Date.parse(value);
    if (Number.isNaN(time) || time < EARLIEST_TIMESTAMP) return null;
    return time;
}

function formatRelative(time: number, now: number, locale: string): string {
    const seconds = Math.round((time - now) / 1000);
    const formatter = new Intl.RelativeTimeFormat(locale, { numeric: "auto" });
    const absolute = Math.abs(seconds);
    if (absolute < 60) return formatter.format(seconds, "second");
    if (absolute < 3_600) return formatter.format(Math.round(seconds / 60), "minute");
    if (absolute < 86_400) return formatter.format(Math.round(seconds / 3_600), "hour");
    return formatter.format(Math.round(seconds / 86_400), "day");
}

function formatDate(value: string, locale: string): string | null {
    const time = parseTimestamp(value);
    if (time === null) return null;
    return new Date(time).toLocaleDateString(locale, {
        year: "numeric",
        month: "short",
        day: "numeric",
    });
}

export function formatDateTime(value: string, locale: string): string | null {
    const time = parseTimestamp(value);
    if (time === null) return null;
    return new Date(time).toLocaleString(locale, {
        month: "short",
        day: "numeric",
        hour: "numeric",
        minute: "2-digit",
    });
}

export function formatClock(value: string, locale: string): string | null {
    const time = parseTimestamp(value);
    if (time === null) return null;
    return new Date(time).toLocaleTimeString(locale, {
        hour: "numeric",
        minute: "2-digit",
    });
}

function formatCountdown(seconds: number): string {
    return `${Math.floor(seconds / 60)}:${String(seconds % 60).padStart(2, "0")}`;
}

function formatPairingCode(code: string): string {
    return /^\d{6}$/.test(code) ? `${code.slice(0, 3)} ${code.slice(3)}` : code;
}

/** Returns the origin of an https relay (or a local http relay for development), or null when invalid. */
function relayOriginFrom(value: string): string | null {
    try {
        const url = new URL(value);
        const local =
            url.hostname === "localhost" || url.hostname === "127.0.0.1" || url.hostname === "[::1]";
        const secure = url.protocol === "https:" || (url.protocol === "http:" && local);
        if (!secure || url.pathname !== "/" || url.search || url.hash || url.username || url.password) {
            return null;
        }
        return url.origin;
    } catch {
        return null;
    }
}

export async function copyText(value: string): Promise<boolean> {
    try {
        const nativeRuntime = (window as unknown as {
            runtime?: { ClipboardSetText?: unknown };
        }).runtime?.ClipboardSetText;
        if (nativeRuntime) {
            await NativeClipboardSetText(value);
        } else if (!(await ClipboardSetText(value))) {
            return false;
        }
        return true;
    } catch {
        return false;
    }
}

export function SettingRow({
    title,
    description,
    control,
    children,
}: {
    title: ReactNode;
    description?: ReactNode;
    control?: ReactNode;
    children?: ReactNode;
}) {
    return (
        <div className="settings-row">
            <div className="flex min-w-0 flex-col gap-1">
                <span className="settings-row-title break-words">{title}</span>
                {description && <div className="settings-row-desc">{description}</div>}
                {children}
            </div>
            {control && <div className="flex shrink-0 items-center gap-2">{control}</div>}
        </div>
    );
}

export function LoadState({
    failed,
    loadingText,
    failedText,
    onRetry,
}: {
    failed: boolean;
    loadingText: string;
    failedText: string;
    onRetry: () => void;
}) {
    const { t } = useTranslation("settings");
    if (!failed) {
        return (
            <p role="status" className="settings-row-desc py-2">
                {loadingText}
            </p>
        );
    }
    return (
        <div className="flex flex-wrap items-center gap-3 py-2">
            <p role="alert" className="text-xs text-copper">
                {failedText}
            </p>
            <Button type="button" variant="outline" size="sm" onClick={onRetry}>
                {t("mobileConnection.retry")}
            </Button>
        </div>
    );
}

/**
 * The iPhone page: the connection status, pairing and paired iPhones, then
 * the text-message card (the brief's schedule moved to Briefs and
 * notifications, brief-schedule-card.tsx), with the server address, what each
 * iPhone may do and other details under one Advanced disclosure.
 */
export function MobileConnectionSettings({
    textMessages,
    advancedExtra,
}: {
    /** Rendered after the paired phones: the text-message essentials. */
    textMessages?: ReactNode;
    /** Rendered last inside Advanced: the text-message details. */
    advancedExtra?: ReactNode;
} = {}) {
    const { t, i18n } = useTranslation("settings");
    const locale = i18n.resolvedLanguage ?? i18n.language;

    const [status, setStatus] = useState<MobileConnectionStatus | null>(null);
    const [errors, setErrors] = useState<Partial<Record<ErrorScope, string>>>({});
    const [action, setAction] = useState<StatusAction | null>(null);
    // null means "show the saved value"; a string is the user's edit.
    const [relayDraft, setRelayDraft] = useState<string | null>(null);
    const [copied, setCopied] = useState(false);
    const [now, setNow] = useState(() => Date.now());
    const [dismissedPairingId, setDismissedPairingId] = useState<string | null>(null);
    const [revokeTarget, setRevokeTarget] = useState<MobileDeviceView | null>(null);
    const [remoteResetOpen, setRemoteResetOpen] = useState(false);
    const [remoteSwitchOpen, setRemoteSwitchOpen] = useState(false);
    // The saved "Keep this computer awake for your phone" switch; null until it is first read.
    const [keepAwake, setKeepAwake] = useState<MobileKeepAwakeStatus | null>(null);

    const mounted = useRef(false);
    // Bumped by every read or write; a read whose version is stale when it resolves is dropped.
    const statusVersion = useRef(0);
    const keepAwakeVersion = useRef(0);
    const actionRef = useRef<StatusAction | null>(null);

    const setError = useCallback((scope: ErrorScope, message?: string) => {
        setErrors((current) => ({ ...current, [scope]: message }));
    }, []);

    const refreshStatus = useCallback(async () => {
        // A status-changing request owns the status until it resolves.
        if (actionRef.current) return;
        const version = ++statusVersion.current;
        try {
            const next = await GetMobileConnectionStatus();
            if (mounted.current && version === statusVersion.current) {
                setStatus(next);
                setError("statusLoad");
            }
        } catch {
            if (mounted.current && version === statusVersion.current) {
                setError("statusLoad", t("mobileConnection.statusFailed"));
            }
        }
    }, [setError, t]);


    const refreshKeepAwake = useCallback(async () => {
        const version = ++keepAwakeVersion.current;
        try {
            const next = await GetMobileKeepAwake();
            if (mounted.current && version === keepAwakeVersion.current) setKeepAwake(next);
        } catch {
            // The switch keeps its last value; the status card reports connection trouble.
        }
    }, []);

    // Turning the switch on or off applies at once here; the computer holds or
    // releases the assertion and the phone reads the same value on its next refresh.
    const setKeepAwakeOn = useCallback(
        async (on: boolean) => {
            const previous = keepAwake;
            const version = ++keepAwakeVersion.current;
            setKeepAwake((current) => (current ? { ...current, on } : current));
            try {
                const next = await SetMobileKeepAwake(on);
                if (mounted.current && version === keepAwakeVersion.current) {
                    setKeepAwake(next);
                    setError("keepAwake");
                }
            } catch {
                if (mounted.current && version === keepAwakeVersion.current) {
                    setKeepAwake(previous);
                    setError("keepAwake", t("mobileConnection.keepAwakeFailed"));
                }
            }
        },
        [keepAwake, setError, t],
    );

    const refreshAll = useCallback(() => {
        void refreshStatus();
        void refreshKeepAwake();
    }, [refreshKeepAwake, refreshStatus]);

    useEffect(() => {
        mounted.current = true;
        refreshAll();
        const unsubscribe = EventsOn("mobile:updated", () => refreshAll());
        const onFocus = () => refreshAll();
        window.addEventListener("focus", onFocus);
        return () => {
            mounted.current = false;
            statusVersion.current += 1;
            window.removeEventListener("focus", onFocus);
            unsubscribe();
        };
    }, [refreshAll]);

    const connectionCode = status?.connectionCode ?? "";
    const hasCode = connectionCode !== "";

    useEffect(() => {
        setNow(Date.now());
        return activeInterval(() => setNow(Date.now()), hasCode ? CODE_TICK_MS : IDLE_TICK_MS);
    }, [hasCode]);

    // Fallback refresh while a code is on screen, so a claim that arrives without an event still shows up.
    useEffect(() => {
        if (!hasCode) return;
        return activeInterval(() => void refreshStatus(), STATUS_POLL_MS);
    }, [hasCode, refreshStatus]);

    useEffect(() => {
        setCopied(false);
    }, [connectionCode]);

    const runStatusAction = async (
        kind: StatusAction,
        call: () => Promise<MobileConnectionStatus>,
        scope: ErrorScope,
        failure: string,
        onSuccess?: () => void,
    ) => {
        if (actionRef.current) return;
        actionRef.current = kind;
        statusVersion.current += 1;
        setAction(kind);
        setError(scope);
        try {
            const next = await call();
            if (mounted.current) {
                setStatus(next);
                setError("statusLoad");
                onSuccess?.();
            }
        } catch (error) {
            const unreachable = kind === "code" && String(error).includes("remote access is not ready");
            if (mounted.current) setError(scope, unreachable ? t("mobileConnection.relayUnreachable") : failure);
        } finally {
            actionRef.current = null;
            if (mounted.current) setAction(null);
        }
    };

    const enabled = Boolean(status?.enabled);
    const busy = action !== null;
    const devices = status?.devices ?? [];
    const hasDevices = devices.length > 0;
    const pending: MobilePendingPairing | null = status?.pendingPairing ?? null;
    const expiresAt = parseTimestamp(status?.expiresAt ?? "");
    const secondsLeft = expiresAt === null ? 0 : Math.max(0, Math.ceil((expiresAt - now) / 1000));
    const codeLive = hasCode && secondsLeft > 0;
    const codeExpired = hasCode && secondsLeft === 0;
    const showCode = enabled && codeLive && pending === null;
    const approvalOpen = pending !== null && dismissedPairingId !== pending.pairingId;

    const relayValue = relayDraft ?? status?.savedRelayUrl ?? "";
    // The wire message remains available to older clients. Desktop copy uses
    // the current frontend language, even if the Mac generated it in another.
    const connectionWarning = status?.warningCode === "relay_loopback" && status.warningHost
        ? t("mobileConnection.loopbackWarning", { host: status.warningHost })
        : null;


    const turnOn = () => {
        const raw = relayValue.trim();
        let relay = "";
        if (raw !== "") {
            const origin = relayOriginFrom(raw);
            if (origin === null) {
                setError(
                    "relay",
                    t("mobileConnection.serverInvalidAdvanced"),
                );
                return;
            }
            relay = origin;
        }
        void runStatusAction(
            "enable",
            () => EnableMobileConnectionUsingRelay(relay),
            "relay",
            t("mobileConnection.turnOnFailed"),
            () => setRelayDraft(null),
        );
    };

    const turnOff = () => {
        void runStatusAction(
            "disable",
            () => DisableMobileConnection(),
            "relay",
            t("mobileConnection.turnOffFailed"),
        );
    };

    const createCode = () => {
        void runStatusAction(
            "code",
            () => GenerateMobileConnectionCode(),
            "code",
            t("mobileConnection.generateFailed"),
        );
    };

    const copyLink = async () => {
        if (!connectionCode) return;
        setError("copy");
        const ok = await copyText(connectionCode);
        if (!mounted.current) return;
        if (ok) {
            setCopied(true);
        } else {
            setError("copy", t("mobileConnection.copyFailed"));
        }
    };

    const decide = (approve: boolean) => {
        if (!pending) return;
        const { pairingId } = pending;
        void runStatusAction(
            approve ? "approve" : "decline",
            () => ConfirmMobilePairing(pairingId, approve),
            "approval",
            t("mobileConnection.approvalFailed"),
        );
    };

    // A paired phone asks to save corrections from Chat (chat:correct).
    const answerScopeRequest = (deviceId: string, approve: boolean) => {
        void runStatusAction(
            approve ? "scope-allow" : "scope-decline",
            () => ConfirmMobileScopeRequest(deviceId, approve),
            "scopeRequest",
            t("mobileConnection.scopeRequestFailed"),
        );
    };
    const scopeAsks = devices.filter((device) => device.scopeRequest?.state === "waiting");

    const deferApproval = () => {
        if (busy || !pending) return;
        setDismissedPairingId(pending.pairingId);
    };

    const revoke = () => {
        if (!revokeTarget) return;
        const { deviceId } = revokeTarget;
        void runStatusAction(
            "revoke",
            () => RevokeMobileDevice(deviceId),
            "revoke",
            t("mobileConnection.revokeFailed"),
            () => setRevokeTarget(null),
        );
    };

    const closeRevoke = () => {
        if (action === "revoke") return;
        setRevokeTarget(null);
        setError("revoke");
    };

    const resetRemoteAccess = () => {
        void runStatusAction(
            "remote-reset",
            () => ResetMobileRemoteAccess(),
            "remoteReset",
            t("mobileConnection.remoteResetFailed"),
            () => setRemoteResetOpen(false),
        );
    };

    const closeRemoteReset = () => {
        if (action === "remote-reset") return;
        setRemoteResetOpen(false);
        setError("remoteReset");
    };

    const switchToRemoteAccess = () => {
        void runStatusAction(
            "enable",
            () => EnableMobileConnectionUsingRelay(""),
            "remoteSwitch",
            t("mobileConnection.remoteSwitchFailed"),
            () => {
                setRelayDraft(null);
                setRemoteSwitchOpen(false);
            },
        );
    };

    const closeRemoteSwitch = () => {
        if (action === "enable") return;
        setRemoteSwitchOpen(false);
        setError("remoteSwitch");
    };

    const deviceName = (device: { deviceName: string }) =>
        device.deviceName || t("mobileConnection.unnamedDevice");

    const connection = status ? describeMobileStatus(status, now, locale, t) : null;
    const pendingName = pending ? deviceName(pending) : "";
    const revokeName = revokeTarget ? deviceName(revokeTarget) : "";

    const loadState = (
        <LoadState
            failed={Boolean(errors.statusLoad)}
            loadingText={t("mobileConnection.loading")}
            failedText={t("mobileConnection.statusFailed")}
            onRetry={() => void refreshStatus()}
        />
    );

    // The address input while the connection is off, the saved address while it is on.
    const serverField =
        status === null ? null : (
            <div className="flex flex-col gap-2 pt-1 pb-1">
                {enabled ? (
                    <>
                        <p className="break-all font-mono text-xs text-ink">
                            {status.remoteAccess
                                ? t("mobileConnection.serverRemoteAccess")
                                : status.relayUrl || t("mobileConnection.serverNotSet")}
                        </p>
                        <p className="settings-row-desc">{t("mobileConnection.serverLockedHint")}</p>
                    </>
                ) : (
                    <>
                        {/* Under Advanced the card title already names the field. */}
                        <label
                            htmlFor="mobile-relay-url"
                            className="sr-only"
                        >
                            {t("mobileConnection.serverTitle")}
                        </label>
                        <input
                            id="mobile-relay-url"
                            type="url"
                            inputMode="url"
                            autoComplete="off"
                            spellCheck={false}
                            placeholder={status.relayUrl || t("mobileConnection.serverPlaceholder")}
                            value={relayValue}
                            disabled={busy}
                            onChange={(event) => {
                                setRelayDraft(event.target.value);
                                setError("relay");
                            }}
                            className={RELAY_INPUT_CLASS}
                        />
                    </>
                )}
            </div>
        );

    return (
        <>
            <SettingsCard>
                {status === null || connection === null ? (
                    loadState
                ) : (
                    <div className="flex flex-col">
                        <SettingRow
                            title={
                                <span className="inline-flex flex-wrap items-center gap-2">
                                    {t("mobileConnection.statusLabel")}
                                    <span className={connection.on ? STATUS_CHIP_ON_CLASS : STATUS_CHIP_CLASS}>
                                        {connection.label}
                                    </span>
                                </span>
                            }
                            description={connection.description}
                            control={
                                enabled ? (
                                    <Button
                                        type="button"
                                        variant="outline"
                                        size="sm"
                                        disabled={busy}
                                        onClick={turnOff}
                                    >
                                        {action === "disable" && <Loader2 className="size-3.5 animate-spin" />}
                                        {t("mobileConnection.turnOff")}
                                    </Button>
                                ) : (
                                    <Button
                                        type="button"
                                        size="sm"
                                        disabled={busy}
                                        onClick={turnOn}
                                    >
                                        {action === "enable" && <Loader2 className="size-3.5 animate-spin" />}
                                        {t("mobileConnection.turnOn")}
                                    </Button>
                                )
                            }
                        />
                        {connectionWarning && (
                            <p role="status" className="pb-2 text-xs text-warning">{connectionWarning}</p>
                        )}
                        {errors.relay && (
                            <p role="alert" className="pb-2 text-xs text-copper">
                                {errors.relay}
                            </p>
                        )}
                    </div>
                )}
            </SettingsCard>

            {status !== null && !(status.relayState === "connected" && !status.remoteAccess && !isLocalRelay(status.relayUrl, status.warningCode)) && (
                <SettingsCard
                    title={t("mobileConnection.remoteAccessTitle")}
                    description={t("mobileConnection.remoteAccessDescription")}
                >
                    <SettingRow
                        title={t("mobileConnection.remoteAccessStatus")}
                        description={
                            status.remoteAccess
                                ? t(`${REMOTE_ACCESS_STATE_DETAIL_TRANSLATION_PREFIX}.${status.remoteAccess.state}`)
                                : status.remoteAccessOffReason
                                  ? t(`${REMOTE_ACCESS_OFF_TRANSLATION_PREFIX}.${status.remoteAccessOffReason}`, {
                                        host: relayHostname(status.relayUrl),
                                    })
                                  : t("mobileConnection.remoteAccessStateDetail.off")
                        }
                        control={
                            status.remoteAccess ? (
                                <Button
                                    type="button"
                                    variant="outline"
                                    size="sm"
                                    disabled={busy || !enabled}
                                    onClick={() => {
                                        setError("remoteReset");
                                        setRemoteResetOpen(true);
                                    }}
                                >
                                    {t("mobileConnection.remoteAccessReset")}
                                </Button>
                            ) : status.remoteAccessOffReason === "saved_relay" ? (
                                <Button
                                    type="button"
                                    variant="outline"
                                    size="sm"
                                    disabled={busy}
                                    onClick={() => {
                                        setError("remoteSwitch");
                                        setRemoteSwitchOpen(true);
                                    }}
                                >
                                    {t("mobileConnection.remoteAccessUseButton")}
                                </Button>
                            ) : null
                        }
                    >
                        {status.remoteAccess &&
                            (status.remoteAccess.state === "failed" ||
                                status.remoteAccess.state === "restarting") && (
                            <>
                                <SettingsFailureLine
                                    failure={{
                                        text: t("mobileConnection.remoteAccessUnavailable"),
                                        retry: () => void refreshStatus(),
                                    }}
                                />
                                <p className="text-xs text-muted">{t("mobileConnection.remoteAccessUnavailableDetail")}</p>
                            </>
                            )}
                    </SettingRow>
                </SettingsCard>
            )}

            <SettingsCard
                title={hasDevices ? t("mobileConnection.devicesTitle") : t("mobileConnection.pairTitle")}
                description={
                    hasDevices ? t("mobileConnection.devicesDescription") : t("mobileConnection.pairDescription")
                }
            >
                {status === null ? (
                    loadState
                ) : (
                    <div className="flex flex-col gap-4 pb-1">
                        <button
                            type="button"
                            className="self-start text-xs text-accent hover:underline"
                            onClick={() => BrowserOpenURL(IPHONE_APP_LINK)}
                        >
                            {t("mobileConnection.getApp")}
                        </button>
                        {hasDevices && (
                            <div className="flex flex-col">
                                {devices.map((device) => {
                                    const name = deviceName(device);
                                    const seenAt = parseTimestamp(device.lastSeenAt);
                                    return (
                                        <SettingRow
                                            key={device.deviceId}
                                            title={name}
                                            description={
                                                seenAt === null
                                                    ? t("mobileConnection.neverSeen")
                                                    : t("mobileConnection.lastSeen", {
                                                          time: formatRelative(seenAt, now, locale),
                                                      })
                                            }
                                            control={
                                                <Button
                                                    type="button"
                                                    variant="destructive"
                                                    size="sm"
                                                    disabled={busy}
                                                    aria-label={t("mobileConnection.disconnectNamed", { name })}
                                                    onClick={() => {
                                                        setError("revoke");
                                                        setRevokeTarget(device);
                                                    }}
                                                >
                                                    {t("mobileConnection.disconnect")}
                                                </Button>
                                            }
                                        />
                                    );
                                })}
                            </div>
                        )}

                        {scopeAsks.map((device) => (
                            <div
                                key={`scope-${device.deviceId}`}
                                className="flex flex-wrap items-center justify-between gap-3 rounded-lg border border-border bg-background-accent/30 p-3"
                            >
                                <p className="text-sm text-ink">
                                    {t("mobileConnection.scopeRequestCorrections", { name: deviceName(device) })}
                                </p>
                                <div className="flex gap-2">
                                    <Button
                                        type="button"
                                        variant="outline"
                                        size="sm"
                                        disabled={busy}
                                        onClick={() => answerScopeRequest(device.deviceId, false)}
                                    >
                                        {action === "scope-decline" && <Loader2 className="size-3.5 animate-spin" />}
                                        {t("mobileConnection.scopeRequestNotNow")}
                                    </Button>
                                    <Button
                                        type="button"
                                        size="sm"
                                        disabled={busy}
                                        onClick={() => answerScopeRequest(device.deviceId, true)}
                                    >
                                        {action === "scope-allow" && <Loader2 className="size-3.5 animate-spin" />}
                                        {t("mobileConnection.scopeRequestAllow")}
                                    </Button>
                                </div>
                            </div>
                        ))}
                        {errors.scopeRequest && (
                            <p role="alert" className="text-xs text-copper">
                                {errors.scopeRequest}
                            </p>
                        )}

                        {pending !== null && !approvalOpen && (
                            <div className="flex flex-wrap items-center justify-between gap-3 rounded-lg border border-border bg-background-accent/30 p-3">
                                <p className="text-sm text-ink">
                                    {t("mobileConnection.waitingForApproval", { name: pendingName })}
                                </p>
                                <Button
                                    type="button"
                                    variant="outline"
                                    size="sm"
                                    onClick={() => setDismissedPairingId(null)}
                                >
                                    {t("mobileConnection.review")}
                                </Button>
                            </div>
                        )}

                        {showCode && (
                            <div className="flex flex-col items-center gap-3 rounded-lg border border-border bg-background-accent/30 p-4">
                                <div className="rounded-md bg-panel p-3">
                                    <QRCodeSVG
                                        value={connectionCode}
                                        size={220}
                                        level="M"
                                        marginSize={4}
                                        title={t("mobileConnection.qrTitle")}
                                    />
                                </div>
                                <p className="text-center text-sm text-ink">
                                    {t("mobileConnection.scanInstruction")}
                                </p>
                                {connectionWarning && (
                                    <p role="status" className="text-center text-xs text-warning">{connectionWarning}</p>
                                )}
                                <p className="text-xs tabular-nums text-muted">
                                    {t("mobileConnection.expiresIn", { time: formatCountdown(secondsLeft) })}
                                </p>
                                <Button type="button" variant="outline" size="sm" onClick={() => void copyLink()}>
                                    {copied ? <Check className="size-3.5" /> : <Copy className="size-3.5" />}
                                    {copied ? t("mobileConnection.copiedLink") : t("mobileConnection.copyLink")}
                                </Button>
                                <p className="max-w-md text-center text-xs text-muted">
                                    {t("mobileConnection.linkHint")}
                                </p>
                                {errors.copy && (
                                    <p role="alert" className="text-xs text-copper">
                                        {errors.copy}
                                    </p>
                                )}
                            </div>
                        )}

                        {!enabled && (
                            <p className="settings-row-desc">{t("mobileConnection.pairOffHint")}</p>
                        )}
                        {enabled && pending === null && !showCode && (
                            codeExpired ? (
                                <p role="status" className="settings-row-desc">
                                    {t("mobileConnection.expired")}
                                </p>
                            ) : (
                                !hasDevices && (
                                    <p className="settings-row-desc">{t("mobileConnection.pairEmpty")}</p>
                                )
                            )
                        )}

                        <div className="flex flex-wrap items-center gap-3">
                            <Button
                                type="button"
                                variant={hasDevices && !showCode ? "outline" : "default"}
                                size={hasDevices && !showCode ? "sm" : "default"}
                                disabled={!enabled || busy || pending !== null}
                                onClick={createCode}
                            >
                                {action === "code" ? (
                                    <Loader2 className="size-4 animate-spin" />
                                ) : showCode ? (
                                    <RefreshCw className="size-4" />
                                ) : null}
                                {errors.code
                                    ? t("mobileConnection.retryPairing")
                                    : showCode
                                    ? t("mobileConnection.newCode")
                                    : hasDevices
                                      ? t("mobileConnection.pairAnother")
                                      : t("mobileConnection.showCode")}
                            </Button>
                            {errors.code && (
                                <p role="alert" className="text-xs text-copper">
                                    {errors.code}
                                </p>
                            )}
                        </div>
                        {hasDevices && keepAwake !== null && (
                            <SettingRow
                                title={t("mobileConnection.keepAwakeTitle")}
                                description={keepAwake.hasBattery
                                    ? t("mobileConnection.keepAwakeDescription")
                                    : t("mobileConnection.keepAwakeDescriptionDesktop")}
                                control={
                                    <Switch
                                        aria-label={t("mobileConnection.keepAwakeTitle")}
                                        checked={keepAwake.on}
                                        onCheckedChange={(checked) => void setKeepAwakeOn(checked)}
                                    />
                                }
                            >
                                {errors.keepAwake && (
                                    <p role="alert" className="text-xs text-copper">
                                        {errors.keepAwake}
                                    </p>
                                )}
                            </SettingRow>
                        )}
                    </div>
                )}
            </SettingsCard>

            {textMessages}

            <SettingsAdvanced pageId="iphone" hint={t("mobileConnection.advancedHint")}>
                {status !== null && (
                    <SettingsCard
                        title={t("mobileConnection.serverTitle")}
                        description={t("mobileConnection.serverDescription")}
                    >
                        {serverField}
                    </SettingsCard>
                )}

                {hasDevices && (
                    <SettingsCard
                        title={t("mobileConnection.accessTitle")}
                        description={t("mobileConnection.accessDescription")}
                    >
                        <div className="flex flex-col">
                            {devices.map((device) => {
                                const pairedOn = formatDate(device.createdAt, locale);
                                const accessUntil = formatDate(device.expiresAt, locale);
                                const labels = (device.scopes ?? []).flatMap((scope) =>
                                    isKnownScope(scope) ? [t(SCOPE_LABELS[scope])] : [],
                                );
                                return (
                                    <SettingRow
                                        key={device.deviceId}
                                        title={deviceName(device)}
                                        description={
                                            <span className="flex flex-wrap gap-x-3">
                                                {pairedOn && (
                                                    <span>{t("mobileConnection.pairedOn", { date: pairedOn })}</span>
                                                )}
                                                {accessUntil && (
                                                    <span>
                                                        {t("mobileConnection.accessUntil", { date: accessUntil })}
                                                    </span>
                                                )}
                                            </span>
                                        }
                                    >
                                        {labels.length > 0 ? (
                                            <ul className="mt-1 flex list-disc flex-col gap-0.5 pl-4 text-xs text-ink">
                                                {labels.map((label) => (
                                                    <li key={label}>{label}</li>
                                                ))}
                                            </ul>
                                        ) : (
                                            <p className="text-xs text-muted">{t("mobileConnection.accessNone")}</p>
                                        )}
                                    </SettingRow>
                                );
                            })}
                        </div>
                    </SettingsCard>
                )}

                {advancedExtra}
            </SettingsAdvanced>

            <Dialog
                open={approvalOpen}
                onOpenChange={(open) => {
                    if (!open) deferApproval();
                }}
            >
                <DialogContent>
                    <DialogHeader>
                        <DialogTitle>{t("mobileConnection.approvalTitle")}</DialogTitle>
                    </DialogHeader>
                    {pending && (
                        <>
                            <div className="px-6 py-5">
                                <DialogDescription>
                                    {t("mobileConnection.approvalDescription", { name: pendingName })}
                                </DialogDescription>
                                <p className="mt-5 text-center text-4xl font-semibold tabular-nums tracking-wider text-ink">
                                    {formatPairingCode(pending.code)}
                                </p>
                                <p className="mt-4 text-center text-xs text-muted">
                                    {t("mobileConnection.approvalCompare")}
                                </p>
                                <p className="mt-3 text-center text-xs text-muted">
                                    {t("mobileConnection.keepAwakeLastStep")}
                                </p>
                                {errors.approval && (
                                    <p role="alert" className="mt-3 text-xs text-copper">
                                        {errors.approval}
                                    </p>
                                )}
                            </div>
                            <div className="flex flex-wrap justify-end gap-2 border-t border-border px-6 py-4">
                                <Button type="button" variant="outline" disabled={busy} onClick={deferApproval}>
                                    {t("mobileConnection.decideLater")}
                                </Button>
                                <Button type="button" variant="outline" disabled={busy} onClick={() => decide(false)}>
                                    {action === "decline" && <Loader2 className="size-3.5 animate-spin" />}
                                    {t("mobileConnection.decline")}
                                </Button>
                                <Button type="button" disabled={busy} onClick={() => decide(true)}>
                                    {action === "approve" && <Loader2 className="size-3.5 animate-spin" />}
                                    {t("mobileConnection.approve")}
                                </Button>
                            </div>
                        </>
                    )}
                </DialogContent>
            </Dialog>

            <Dialog
                open={Boolean(revokeTarget)}
                onOpenChange={(open) => {
                    if (!open) closeRevoke();
                }}
            >
                <DialogContent>
                    <DialogHeader>
                        <DialogTitle>{t("mobileConnection.disconnectTitle")}</DialogTitle>
                    </DialogHeader>
                    <div className="px-6 py-5">
                        <DialogDescription>
                            {t("mobileConnection.disconnectDescription", { name: revokeName })}
                        </DialogDescription>
                        {errors.revoke && (
                            <p role="alert" className="mt-3 text-xs text-copper">
                                {errors.revoke}
                            </p>
                        )}
                    </div>
                    <div className="flex justify-end gap-2 border-t border-border px-6 py-4">
                        <Button type="button" variant="outline" disabled={busy} onClick={closeRevoke}>
                            {t("mobileConnection.cancel")}
                        </Button>
                        <Button type="button" variant="destructive" disabled={busy} onClick={revoke}>
                            {action === "revoke" && <Loader2 className="size-3.5 animate-spin" />}
                            {t("mobileConnection.confirmDisconnect")}
                        </Button>
                    </div>
                </DialogContent>
            </Dialog>

            <Dialog
                open={remoteResetOpen}
                onOpenChange={(open) => {
                    if (!open) closeRemoteReset();
                }}
            >
                <DialogContent>
                    <DialogHeader>
                        <DialogTitle>{t("mobileConnection.remoteResetTitle")}</DialogTitle>
                    </DialogHeader>
                    <div className="px-6 py-5">
                        <DialogDescription>
                            {t("mobileConnection.remoteResetDescription")}
                        </DialogDescription>
                        {errors.remoteReset && (
                            <p role="alert" className="mt-3 text-xs text-copper">
                                {errors.remoteReset}
                            </p>
                        )}
                    </div>
                    <div className="flex justify-end gap-2 border-t border-border px-6 py-4">
                        <Button type="button" variant="outline" disabled={busy} onClick={closeRemoteReset}>
                            {t("mobileConnection.cancel")}
                        </Button>
                        <Button type="button" variant="outline" disabled={busy} onClick={resetRemoteAccess}>
                            {action === "remote-reset" && <Loader2 className="size-3.5 animate-spin" />}
                            {t("mobileConnection.remoteResetConfirm")}
                        </Button>
                    </div>
                </DialogContent>
            </Dialog>

            <Dialog
                open={remoteSwitchOpen}
                onOpenChange={(open) => {
                    if (!open) closeRemoteSwitch();
                }}
            >
                <DialogContent>
                    <DialogHeader>
                        <DialogTitle>{t("mobileConnection.remoteSwitchTitle")}</DialogTitle>
                    </DialogHeader>
                    <div className="px-6 py-5">
                        <DialogDescription>
                            {t("mobileConnection.remoteSwitchDescription", {
                                host: relayHostname(status?.relayUrl ?? ""),
                            })}
                        </DialogDescription>
                        {errors.remoteSwitch && (
                            <p role="alert" className="mt-3 text-xs text-copper">
                                {errors.remoteSwitch}
                            </p>
                        )}
                    </div>
                    <div className="flex justify-end gap-2 border-t border-border px-6 py-4">
                        <Button type="button" variant="outline" disabled={busy} onClick={closeRemoteSwitch}>
                            {t("mobileConnection.cancel")}
                        </Button>
                        <Button type="button" variant="outline" disabled={busy} onClick={switchToRemoteAccess}>
                            {action === "enable" && <Loader2 className="size-3.5 animate-spin" />}
                            {t("mobileConnection.remoteSwitchConfirm")}
                        </Button>
                    </div>
                </DialogContent>
            </Dialog>
        </>
    );
}
