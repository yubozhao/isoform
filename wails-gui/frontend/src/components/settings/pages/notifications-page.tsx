import { useCallback, useEffect, useRef, useState } from "react";
import { Loader2 } from "lucide-react";
import { useTranslation } from "react-i18next";
import {
    GetPlatformPermissions,
    OpenPermissionSettings,
    RequestPermission,
} from "../../../../wailsjs/go/main/App";
import {
    GetEmailChannelStatus,
    GetMobileConnectionStatus,
    GetSMSChannelStatus,
    GetTextChannelStatus,
} from "@/lib/wails-shim-app";
import { EventsOn } from "../../../../wailsjs/runtime/runtime";
import { EmailSettings } from "@/components/email-settings";
import {
    NotificationChip,
    NotificationRow,
    NotificationsSettings,
    QuietHoursCard,
    useEngagementSettingsState,
} from "@/components/notifications-settings";
import { Button } from "@/components/ui/button";
import { isMac } from "@/lib/platform";
import { SettingsAdvanced } from "@/components/settings/settings-advanced";
import { AutoSpeakSettings } from "./read-aloud-settings";
import { SettingsCard } from "@/components/settings/settings-primitives";
import { openSettingsPage, type OpenSettingsPage } from "./capture-shared";
import { TopicProposalsCard } from "./topic-proposals-card";
import { BriefScheduleCard } from "@/components/brief-schedule-card";

// The Notifications item in GetPlatformPermissions (permissions_darwin.go).
const NOTIFICATIONS_PERMISSION_KEY = "notifications";
// Mirrors mobileScopePush in wails-gui/mobile_pairing.go: a paired phone with
// this scope is one Isoform can push notifications to.
const PUSH_SCOPE = "push";

/**
 * Whether macOS lets Isoform show notifications. Null while unknown, and on a
 * platform that has no such permission. Rechecked when the window regains
 * focus, so it updates after a trip to System Settings.
 */
function useMacNotificationPermission() {
    const [granted, setGranted] = useState<boolean | null>(null);
    const mounted = useRef(true);
    const readVersion = useRef(0);

    const refresh = useCallback(async (): Promise<boolean | null> => {
        const version = ++readVersion.current;
        try {
            const permissions = await GetPlatformPermissions();
            const item = permissions.items?.find((entry) => entry.key === NOTIFICATIONS_PERMISSION_KEY);
            const next = item ? item.granted : null;
            if (mounted.current && version === readVersion.current) setGranted(next);
            return next;
        } catch {
            return null;
        }
    }, []);

    useEffect(() => {
        mounted.current = true;
        void refresh();
        let timer: ReturnType<typeof setTimeout>;
        const onWindowFocus = () => {
            clearTimeout(timer);
            timer = setTimeout(() => void refresh(), 500);
        };
        window.addEventListener("focus", onWindowFocus);
        const off = EventsOn("permissions:changed", () => void refresh());
        return () => {
            off();
            mounted.current = false;
            readVersion.current += 1;
            clearTimeout(timer);
            window.removeEventListener("focus", onWindowFocus);
        };
    }, [refresh]);

    return { granted, refresh };
}

/**
 * Shown only while macOS blocks Isoform's notifications. Asks macOS first; if
 * that does not grant it (the user said no before), opens System Settings.
 */
function MacPermissionCard({ recheck }: { recheck: () => Promise<boolean | null> }) {
    const { t } = useTranslation("settings");
    const [busy, setBusy] = useState(false);
    const [message, setMessage] = useState<"stillBlocked" | "failed" | null>(null);

    const allow = async () => {
        setBusy(true);
        setMessage(null);
        try {
            const granted = await RequestPermission(NOTIFICATIONS_PERMISSION_KEY);
            const nowGranted = await recheck();
            if (!granted || nowGranted !== true) {
                await OpenPermissionSettings(NOTIFICATIONS_PERMISSION_KEY).catch(() => undefined);
                setMessage("stillBlocked");
            }
        } catch {
            setMessage("failed");
        } finally {
            setBusy(false);
        }
    };

    return (
        <SettingsCard
            title={t("proactiveNotifications.macPermission.title")}
            description={t("proactiveNotifications.macPermission.description")}
        >
            {/* The same call-to-action as the Computer Use setup card. */}
            <button
                type="button"
                onClick={() => void allow()}
                disabled={busy}
                className="flex items-center justify-between gap-3 px-4 py-3 rounded-lg bg-accent-2/[0.08] border border-accent-2/20 text-left cursor-pointer hover:bg-accent-2/[0.12] transition-colors w-full disabled:cursor-default disabled:opacity-60"
            >
                <div className="flex flex-col">
                    <span className="text-sm font-semibold text-ink">
                        {t("proactiveNotifications.macPermission.action")}
                    </span>
                    <span className="text-xs text-muted">
                        {t("proactiveNotifications.macPermission.actionHint")}
                    </span>
                </div>
                <span className="flex items-center gap-1.5 text-xs font-semibold text-accent-1">
                    {busy && <Loader2 className="size-3.5 animate-spin" />}
                    {t("proactiveNotifications.macPermission.open")}
                </span>
            </button>
            {message === "stillBlocked" && (
                <p role="status" className="pt-2 text-xs text-muted">
                    {t("proactiveNotifications.macPermission.stillBlocked")}
                </p>
            )}
            {message === "failed" && (
                <p role="alert" className="pt-2 text-xs text-copper">
                    {t("proactiveNotifications.macPermission.failed")}
                </p>
            )}
        </SettingsCard>
    );
}

type ChannelState = "ready" | "notAllowed" | "notSetUp" | "unsupported";

/**
 * Where a notification goes, in the order Isoform tries the channels
 * (engagement.ChannelFor in wails-gui/internal/engagement/channel.go), each
 * with whether it can be used right now. A status that cannot be read shows
 * no chip.
 */
function NotificationChannels({
    macGranted,
    onOpenPage,
}: {
    macGranted: boolean | null;
    onOpenPage?: OpenSettingsPage;
}) {
    const { t } = useTranslation("settings");
    const [iphone, setIphone] = useState<ChannelState | null>(null);
    const [text, setText] = useState<ChannelState | null>(null);
    const [email, setEmail] = useState<ChannelState | null>(null);
    // null until read; the late-notice line shows only when this Mac's relay has no push gateway.
    const [instantPush, setInstantPush] = useState<boolean | null>(null);

    useEffect(() => {
        let alive = true;
        GetMobileConnectionStatus()
            .then((status) => {
                if (!alive) return;
                const paired = status.devices.some((device) => device.scopes.includes(PUSH_SCOPE));
                setIphone(status.enabled && paired ? "ready" : "notSetUp");
                setInstantPush(status.instantPush);
            })
            .catch(() => undefined);
        // A text goes through the relay when a number is subscribed, and through
        // Messages on this Mac otherwise; either one makes the row ready.
        Promise.all([
            GetTextChannelStatus().catch(() => null),
            GetSMSChannelStatus().catch(() => null),
        ]).then(([messages, sms]) => {
            if (!alive || (messages === null && sms === null)) return;
            if (sms?.state === "active" && sms.available) {
                setText("ready");
                return;
            }
            if (messages && !messages.supported) {
                setText(sms?.available ? "notSetUp" : "unsupported");
                return;
            }
            const ready =
                messages !== null &&
                messages.enabled &&
                messages.number !== "" &&
                messages.consentAccepted &&
                messages.permissionGranted;
            setText(ready ? "ready" : "notSetUp");
        });
        GetEmailChannelStatus()
            .then((status) => {
                if (!alive) return;
                if (status.lastError === "not_configured") {
                    setEmail("unsupported");
                    return;
                }
                setEmail(status.available && status.state === "active" ? "ready" : "notSetUp");
            })
            .catch(() => undefined);
        return () => {
            alive = false;
        };
    }, []);

    const chipLabel: Record<ChannelState, string> = {
        ready: t("proactiveNotifications.channels.ready"),
        notAllowed: t("proactiveNotifications.channels.notAllowed"),
        notSetUp: t("proactiveNotifications.channels.notSetUp"),
        unsupported: t("proactiveNotifications.channels.unsupported"),
    };
    const chip = (state: ChannelState | null) =>
        state ? <NotificationChip>{chipLabel[state]}</NotificationChip> : undefined;
    // Phone pairing stays available; new text-message setup is Coming soon.
    const withSetUpLink = (state: ChannelState | null) =>
        state === "notSetUp" ? (
            <>
                {chip(state)}
                <Button size="sm" variant="outline" onClick={() => openSettingsPage("iphone", onOpenPage)}>
                    {t("proactiveNotifications.channels.openIphone")}
                </Button>
            </>
        ) : (
            chip(state)
        );
    const mac: ChannelState | null = macGranted === null ? null : macGranted ? "ready" : "notAllowed";

    return (
        <SettingsCard
            title={t("proactiveNotifications.channels.title")}
            description={t("proactiveNotifications.channels.description")}
        >
            <NotificationRow
                title={
                    isMac
                        ? t("proactiveNotifications.channels.macTitle")
                        : t("proactiveNotifications.channels.computerTitle")
                }
                description={t("proactiveNotifications.channels.macDescription")}
                control={chip(mac)}
            />
            <NotificationRow
                title={t("proactiveNotifications.channels.iphoneTitle")}
                description={t("proactiveNotifications.channels.iphoneDescription")}
                note={instantPush === false ? t("proactiveNotifications.channels.iphoneLateNote") : undefined}
                control={withSetUpLink(iphone)}
            />
            <NotificationRow
                title={t("proactiveNotifications.channels.textTitle")}
                description={t("proactiveNotifications.channels.textDescription")}
                control={text === "notSetUp" ? <NotificationChip>{t("nav.sections.comingSoon.heading")}</NotificationChip> : chip(text)}
            />
            <NotificationRow
                title={t("proactiveNotifications.channels.emailTitle")}
                description={t("proactiveNotifications.channels.emailDescription")}
                control={chip(email)}
            />
        </SettingsCard>
    );
}

/**
 * Notifications: what Isoform tells you about, ranked by whether it earned
 * your attention, and when it stays quiet (RC 1). The page header's subtitle
 * is the only introduction. The page asks for the macOS permission only while
 * it is missing, and keeps quiet hours and limits and where each
 * notification goes (with the order Isoform tries) under Advanced.
 */
export function NotificationsPage({ onOpenPage }: { onOpenPage?: OpenSettingsPage } = {}) {
    const { t } = useTranslation("settings");
    const permission = useMacNotificationPermission();
    const engagement = useEngagementSettingsState();

    return (
        <>
            {/* computer-settings v1.3.0 rule 1: proposals first, then the
                brief (v1.0.0 rule 6: the brief times), then notifications. */}
            <TopicProposalsCard />
            <BriefScheduleCard onOpenAssistant={() => openSettingsPage("ai-provider", onOpenPage, { advanced: true })} />
            {permission.granted === false && <MacPermissionCard recheck={permission.refresh} />}
            <NotificationsSettings state={engagement} onOpenPage={onOpenPage} />
            <EmailSettings />
            {/* Quiet hours and the voice for Hear it sit with the rest
                (computer-conformance-answers v1.0.0 T2), out of Advanced. */}
            <QuietHoursCard state={engagement} />
            <AutoSpeakSettings />
            <SettingsAdvanced
                pageId="notifications"
                hint={t("proactiveNotifications.advancedHint")}
            >
                <NotificationChannels macGranted={permission.granted} onOpenPage={onOpenPage} />
            </SettingsAdvanced>
        </>
    );
}
