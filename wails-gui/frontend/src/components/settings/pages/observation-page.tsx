import { useCallback, useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { notifyHoldSettingsChanged } from "@/lib/hold-settings-events";
import { Loader2, RefreshCw } from "lucide-react";
import { activeInterval } from "@/lib/app-activity";
import { SettingsFailureLine, useSettingsFailure, type SettingsFailure } from "@/components/settings/settings-failure";
import { Button } from "@/components/ui/button";
import { Switch } from "@/components/ui/switch";
import {
    GetActivityMonitorStatus,
    GetActivityRetentionDays,
    GetBackgroundAudioStatus,
    SetPermissionEnabled,
} from "../../../../wailsjs/go/main/App";
import {
    AddFilesPrivateFolder,
    GetAfterCallSpeechModel,
    GetCalendarOverview,
    GetHoldSettings,
    SetOfferHolds,
    SetHoldCalendar,
    GetFilesOverview,
    GetMailOverview,
    GetRemindersOverview,
    GetSystemAudioOutsideCallsEnabled,
    IsClosedEyesModeEnabled,
    RemoveAfterCallSpeechModel,
    SetAfterCallSpeechModelEnabled,
    SetAutomaticMeetingRecordingEnabled,
    SetCalendarPeekEnabled,
    RemoveFilesPrivateFolder,
    SetClosedEyesModeEnabled,
    SetSystemAudioOutsideCallsEnabled,
    SyncCalendarsNow,
    SyncFilesNow,
    SyncMailNow,
    SyncRemindersNow,
    type AfterCallSpeechModelView,
    type CalendarOverview,
    type HoldSettings,
    type CalendarSourceView,
    type FilesFolderView,
    type FilesOverview,
    type MailOverview,
    type ReminderListView,
    type RemindersOverview,
} from "@/lib/wails-shim-app";
import { EventsOn } from "../../../../wailsjs/runtime/runtime";
import { GetAutomaticMeetingRecordingEnabled } from "@/lib/page-queries";
import type { main } from "../../../../wailsjs/go/models";
import { usePermissions } from "@/hooks/usePermissions";
import { PAUSE_OBSERVING_COMBO, comboToDisplayString, type KeyCombo } from "@/hooks/useKeybindings";
import { isWailsEnvironment } from "@/lib/browser-bridge";
import type { PermissionItem, PlatformPermissions } from "@/types";
import { SettingsCard, SettingsRow } from "@/components/settings/settings-primitives";
import { formatBytes } from "@/components/settings/settings-format";
import { SettingsAdvanced, settingsAnchor } from "@/components/settings/settings-advanced";
import {
    MissingPermissionRow,
    SettingsControlCard,
    SettingsReason,
    openSettingsPage,
    useListening,
    type ListeningState,
    type OpenSettingsPage,
} from "./capture-shared";
import { CapturedCountsCard } from "./captured-counts-card";

// ── Observation page (page id "activity") ──
//
// What Isoform sees and hears on this computer. Everyday cards: a temporary
// Pause, the real on/off for watching the screen, listening with meeting
// recording nested under it, and the calendars; then the read-only "What
// Isoform captured" counts (anchor "captured", linked from Today). Capture
// status and links to "Apps Isoform does not listen to" and "Keep recordings
// for" (both on Privacy) sit under Advanced, with the same names they have
// there.

const CLOSED_EYES_CHANGED_EVENT = "closed-eyes:changed";
const CALENDAR_SYNCED_EVENT = "calendar:synced";
const REMINDERS_SYNCED_EVENT = "reminders:synced";
const FILES_READ_EVENT = "files:read";
const MAIL_SYNCED_EVENT = "mail:synced";
const CALENDARS_PERMISSION_KEY = "calendars";
const REMINDERS_PERMISSION_KEY = "reminders";
const CLIPBOARD_PERMISSION_KEY = "clipboard";
const FILES_PERMISSION_KEY = "files";
const MAIL_PERMISSION_KEY = "automation_mail";
const MESSAGES_PERMISSION_KEY = "messages_history";

// Switchable items that are not part of "Watch my screen": the microphone,
// which is the listening switch, and the clipboard, recent files, calendars,
// reminders, mail and Messages, which have their own cards below.
const NOT_SCREEN_PERMISSION_KEYS = new Set([
    "microphone",
    CLIPBOARD_PERMISSION_KEY,
    FILES_PERMISSION_KEY,
    CALENDARS_PERMISSION_KEY,
    REMINDERS_PERMISSION_KEY,
    MAIL_PERMISSION_KEY,
    MESSAGES_PERMISSION_KEY,
]);

// Permission items that make up "Watch my screen": every switchable item
// except the ones above. That is Screen Recording and Accessibility on macOS,
// Screen Capture and Keyboard & Mouse Automation on Windows, and the PipeWire
// / X11 and input keys on Linux.
export function screenPermissionItems(permissions: PlatformPermissions | null): PermissionItem[] {
    return (permissions?.items ?? []).filter((item) => item.toggleable && !NOT_SCREEN_PERMISSION_KEYS.has(item.key));
}

const AUDIO_PERMISSION_KEYS = new Set(["microphone", "system_audio"]);

// ── Pause observing ──

interface ObservationPauseState {
    /** null until read. */
    paused: boolean | null;
    loadFailed: boolean;
    saving: boolean;
    setPaused: (next: boolean) => Promise<void>;
    changeFailure: SettingsFailure | null;
    reload: () => void;
}

/** The temporary pause shared with the menu bar's Pause Observation item, the
 *  tray, the overlay and the hotkey. Go emits closed-eyes:changed from all of
 *  them, so the switch follows whichever surface changed it. */
function useObservationPause(): ObservationPauseState {
    const { t } = useTranslation("settings");
    const { failure: changeFailure, fail: failChange } = useSettingsFailure();
    const [paused, setPausedState] = useState<boolean | null>(null);
    const [loadFailed, setLoadFailed] = useState(false);
    const [saving, setSaving] = useState(false);

    const reload = useCallback(() => {
        IsClosedEyesModeEnabled()
            .then((value) => {
                setPausedState(value);
                setLoadFailed(false);
            })
            .catch(() => setLoadFailed(true));
    }, []);

    useEffect(() => {
        reload();
        const off = EventsOn(CLOSED_EYES_CHANGED_EVENT, (next: unknown) => {
            setPausedState(Boolean(next));
            setLoadFailed(false);
        });
        window.addEventListener("focus", reload);
        return () => {
            off();
            window.removeEventListener("focus", reload);
        };
    }, [reload]);

    const setPaused = useCallback(
        async (next: boolean) => {
            setPausedState(next);
            setSaving(true);
            try {
                await SetClosedEyesModeEnabled(next);
            } catch {
                setPausedState(!next);
                failChange(t("observation.pause.toggleFailed"), () => void setPaused(next));
            } finally {
                setSaving(false);
            }
        },
        [t],
    );

    return { paused, loadFailed, saving, setPaused, changeFailure, reload };
}

function KeyChord({ combo }: { combo: KeyCombo }) {
    return (
        <span className="inline-flex items-center px-2.5 py-1 rounded-md text-xs font-mono bg-ink/5 text-muted">
            {comboToDisplayString(combo)}
        </span>
    );
}

function PauseObservingCard({ pause }: { pause: ObservationPauseState }) {
    const { t } = useTranslation("settings");
    const title = t("observation.pause.title");
    return (
        <SettingsControlCard
            title={title}
            description={t("observation.pause.description")}
            control={
                <Switch
                    aria-label={title}
                    checked={pause.paused === true}
                    disabled={pause.paused === null || pause.saving}
                    onCheckedChange={(next) => void pause.setPaused(next)}
                />
            }
        >
            {pause.loadFailed && <SettingsFailureLine failure={{ text: t("observation.pause.loadFailed"), retry: pause.reload }} />}
            <SettingsFailureLine failure={pause.changeFailure} />
            {pause.paused === true && <SettingsReason>{t("observation.pause.paused")}</SettingsReason>}
            {PAUSE_OBSERVING_COMBO && (
                <SettingsRow
                    title={t("observation.pause.shortcutTitle")}
                    description={t("observation.pause.shortcutDescription")}
                    control={<KeyChord combo={PAUSE_OBSERVING_COMBO} />}
                />
            )}
        </SettingsControlCard>
    );
}

// ── Watch my screen ──

function WatchScreenCard({
    permissions,
    paused,
    onGrant,
    onChanged,
}: {
    permissions: PlatformPermissions | null;
    paused: boolean;
    onGrant: (item: PermissionItem) => void;
    onChanged: () => Promise<unknown>;
}) {
    const { t } = useTranslation("settings");
    const { failure: changeFailure, fail: failChange } = useSettingsFailure();
    const items = useMemo(() => screenPermissionItems(permissions), [permissions]);
    // The value being written, shown until the refreshed permissions arrive.
    const [pending, setPending] = useState<boolean | null>(null);

    if (permissions && items.length === 0) return null;

    const watching = items.length > 0 && items.every((item) => item.enabled);
    const missing = items.filter((item) => item.actionable && !item.granted);
    const title = t("observation.watch.title");

    // Write every key so they always agree: someone who earlier turned off
    // Accessibility alone would otherwise see "on" while capture stays off.
    const handleChange = async (next: boolean) => {
        setPending(next);
        try {
            for (const item of items) {
                if (item.enabled !== next) {
                    await SetPermissionEnabled(item.key, next);
                }
            }
        } catch {
            failChange(t("observation.watch.toggleFailed"), () => void handleChange(next));
        } finally {
            await onChanged();
            setPending(null);
        }
    };

    let reason: string | null = null;
    if (paused) reason = t("observation.pausedReason");
    else if (missing.length > 0) reason = t("observation.watch.needsAccess");

    return (
        <SettingsControlCard
            title={title}
            description={t("observation.watch.description")}
            control={
                <Switch
                    aria-label={title}
                    checked={pending ?? watching}
                    disabled={!permissions || paused || missing.length > 0 || pending !== null}
                    onCheckedChange={(next) => void handleChange(next)}
                />
            }
        >
            {reason && <SettingsReason>{reason}</SettingsReason>}
            {!paused &&
                missing.map((item) => (
                    <MissingPermissionRow key={item.key} item={item} onGrant={onGrant} />
                ))}
            <SettingsFailureLine failure={changeFailure} />
        </SettingsControlCard>
    );
}

// ── Listen, with the microphone and meeting recording nested under it ──

/** The microphone source of listening on macOS: off, Isoform hears only the
 *  Mac's own sound. Go keeps it apart from the Listen switch. */
function MicrophoneSourceRow({
    item,
    paused,
    onChanged,
}: {
    item: PermissionItem;
    paused: boolean;
    onChanged: () => Promise<unknown>;
}) {
    const { t } = useTranslation("settings");
    const { failure: changeFailure, fail: failChange } = useSettingsFailure();
    const [pending, setPending] = useState<boolean | null>(null);
    const title = t("observation.listen.microphone.title");

    const handleChange = async (next: boolean) => {
        setPending(next);
        try {
            await SetPermissionEnabled("microphone", next);
        } catch {
            failChange(t("observation.listen.microphone.toggleFailed"), () => void handleChange(next));
        } finally {
            await onChanged();
            setPending(null);
        }
    };

    return (
        <SettingsRow
            title={title}
            failure={changeFailure}
            description={t("observation.listen.microphone.description")}
            control={
                <Switch
                    aria-label={title}
                    checked={pending ?? item.enabled}
                    disabled={paused || pending !== null}
                    onCheckedChange={(next) => void handleChange(next)}
                />
            }
        />
    );
}

/** The computer's own sound outside calls. Off unless turned on; during a
 *  call both sides are heard while meeting recording is on. Needs
 *  listening, like meetings. */
export function SystemAudioOutsideCallsRow({ listeningOn }: { listeningOn: boolean }) {
    const { t } = useTranslation("settings");
    const { failure: changeFailure, fail: failChange } = useSettingsFailure();
    const [enabled, setEnabled] = useState<boolean | null>(null);
    const [loading, setLoading] = useState(true);
    const [loadFailed, setLoadFailed] = useState(false);

    useEffect(() => {
        GetSystemAudioOutsideCallsEnabled()
            .then((value) => {
                setEnabled(value);
                setLoadFailed(false);
            })
            .catch(() => setLoadFailed(true))
            .finally(() => setLoading(false));
    }, []);

    const handleToggle = () => {
        if (enabled === null) return;
        const next = !enabled;
        setEnabled(next);
        setLoading(true);
        SetSystemAudioOutsideCallsEnabled(next)
            .catch(() => {
                setEnabled(!next);
                failChange(t("observation.listen.systemAudioOutsideCalls.toggleFailed"), () => void handleToggle());
            })
            .finally(() => setLoading(false));
    };

    const title = t("observation.listen.systemAudioOutsideCalls.title");
    let description = t("observation.listen.systemAudioOutsideCalls.description");
    if (loadFailed) description = t("observation.listen.loadFailed");
    else if (!listeningOn) description = t("observation.meetings.needsListening");

    return (
        <SettingsRow
            title={title}
            failure={changeFailure}
            description={description}
            control={
                <Switch
                    checked={enabled ?? false}
                    disabled={loading || enabled === null || loadFailed || !listeningOn}
                    aria-label={title}
                    onCheckedChange={handleToggle}
                />
            }
        />
    );
}

/** The model that transcribes a call again once it has ended. It downloads
 *  without a notice after a call; this row says what it is, how large, where
 *  it stands and its last error, and turns it off or removes it. */
export function SpeechModelRow() {
    const { t } = useTranslation("settings");
    const { failure: changeFailure, fail: failChange } = useSettingsFailure();
    const [view, setView] = useState<AfterCallSpeechModelView | null>(null);
    const [loadFailed, setLoadFailed] = useState(false);
    const [busy, setBusy] = useState(false);

    const load = useCallback(
        () =>
            GetAfterCallSpeechModel()
                .then((next) => {
                    setView(next);
                    setLoadFailed(false);
                })
                .catch(() => setLoadFailed(true)),
        [],
    );
    useEffect(() => {
        void load();
    }, [load]);
    useEffect(() => {
        if (view?.status !== "downloading") return;
        // Download progress while the window is looked at; paused while hidden.
        return activeInterval(() => void load(), 3000);
    }, [view?.status, load]);

    if (!loadFailed && !view?.supported) return null;

    const title = t("observation.listen.speechModel.title");
    const name = view?.name ?? "";
    const size = formatBytes(view?.downloadBytes ?? 0);
    let description: string;
    if (loadFailed || !view) description = t("observation.listen.loadFailed");
    else if (!view.enabled) description = t("observation.listen.speechModel.off", { name });
    else if (view.status === "ready") description = t("observation.listen.speechModel.ready", { name, size: formatBytes(view.diskBytes) });
    else if (view.status === "downloading")
        description = t("observation.listen.speechModel.downloading", {
            name,
            percent: view.total > 0 ? Math.round((view.downloaded / view.total) * 100) : 0,
            size,
        });
    else if (view.status === "low_disk") description = t("observation.listen.speechModel.lowDisk", { name, error: view.lastError });
    else if (view.status === "failed") description = t("observation.listen.speechModel.failed", { name, error: view.lastError });
    else description = t("observation.listen.speechModel.absent", { name, size });

    const change = async (action: () => Promise<void>) => {
        setBusy(true);
        try {
            await action();
        } catch {
            failChange(t("observation.listen.speechModel.changeFailed"), () => void change(action));
        } finally {
            await load();
            setBusy(false);
        }
    };

    return (
        <SettingsRow
            title={title}
            failure={changeFailure}
            description={description}
            control={
                <div className="flex items-center gap-2">
                    {view?.removable && (
                        <Button
                            size="sm"
                            variant="outline"
                            disabled={busy}
                            onClick={() => void change(() => RemoveAfterCallSpeechModel())}
                        >
                            {t("observation.listen.speechModel.remove")}
                        </Button>
                    )}
                    <Switch
                        aria-label={title}
                        checked={view?.enabled ?? false}
                        disabled={busy || !view}
                        onCheckedChange={(next) => void change(() => SetAfterCallSpeechModelEnabled(next))}
                    />
                </div>
            }
        />
    );
}

/** Record meetings automatically. It needs listening, so it is disabled with
 *  a reason while Listen is off. */
export function MeetingRecordingRow({ listeningOn }: { listeningOn: boolean }) {
    const { t } = useTranslation("settings");
    const { failure: changeFailure, fail: failChange } = useSettingsFailure();
    const [enabled, setEnabled] = useState<boolean | null>(null);
    const [loading, setLoading] = useState(true);
    const [loadFailed, setLoadFailed] = useState(false);

    useEffect(() => {
        let cancelled = false;
        GetAutomaticMeetingRecordingEnabled()
            .then((value) => {
                if (cancelled) return;
                setEnabled(value);
                setLoadFailed(false);
            })
            .catch(() => { if (!cancelled) setLoadFailed(true); })
            .finally(() => { if (!cancelled) setLoading(false); });
        const off = EventsOn("automatic-meeting-recording:changed", (value: boolean) => {
            setEnabled(Boolean(value));
            setLoadFailed(false);
            setLoading(false);
        });
        return () => {
            cancelled = true;
            off();
        };
    }, []);

    const handleToggle = () => {
        if (enabled === null) return;
        const next = !enabled;
        setEnabled(next);
        setLoading(true);
        SetAutomaticMeetingRecordingEnabled(next)
            .catch(() => {
                setEnabled(!next);
                failChange(t("meetingRecording.toggleFailed"), () => void handleToggle());
            })
            .finally(() => setLoading(false));
    };

    const title = t("observation.meetings.title");
    let description = t("observation.meetings.description");
    if (loadFailed) description = t("meetingRecording.loadFailed");
    else if (!listeningOn) description = t("observation.meetings.needsListening");

    return (
        <SettingsRow
            title={title}
            failure={changeFailure}
            description={description}
            control={
                <Switch
                    checked={enabled ?? false}
                    disabled={loading || enabled === null || loadFailed || !listeningOn}
                    aria-label={title}
                    onCheckedChange={handleToggle}
                />
            }
        />
    );
}

function ListenCard({
    listening,
    paused,
    permissions,
    onGrant,
    onChanged,
    showMeetings,
}: {
    listening: ListeningState;
    paused: boolean;
    permissions: PlatformPermissions | null;
    onGrant: (item: PermissionItem) => void;
    onChanged: () => Promise<unknown>;
    showMeetings: boolean;
}) {
    const { t } = useTranslation("settings");
    const on = listening.enabled === true;
    const title = t("observation.listen.title");
    const missing =
        on && !paused
            ? (permissions?.items ?? []).filter(
                  (item) => AUDIO_PERMISSION_KEYS.has(item.key) && item.actionable && !item.granted,
              )
            : [];

    let reason: string | null = null;
    if (paused && !listening.loadFailed) reason = t("observation.pausedReason");

    // On macOS the microphone item's switch is the microphone source alone;
    // on Windows the same item is the Listen switch itself.
    const microphone =
        permissions?.platform === "darwin"
            ? ((permissions.items ?? []).find((item) => item.key === "microphone" && item.toggleable && item.granted) ??
              null)
            : null;

    return (
        <SettingsControlCard
            title={title}
            description={
                listening.systemAudioSupported
                    ? t("observation.listen.descriptionWithSystemAudio")
                    : t("observation.listen.descriptionMicOnly")
            }
            control={
                <Switch
                    aria-label={title}
                    checked={on}
                    disabled={listening.enabled === null || listening.saving || paused}
                    onCheckedChange={(next) => void listening.setEnabled(next)}
                />
            }
        >
            {reason && <SettingsReason>{reason}</SettingsReason>}
            {listening.loadFailed && (
                <SettingsFailureLine failure={{ text: t("observation.listen.loadFailed"), retry: listening.reload }} />
            )}
            <SettingsFailureLine failure={listening.changeFailure} />
            {missing.map((item) => (
                <MissingPermissionRow key={item.key} item={item} onGrant={onGrant} />
            ))}
            {microphone && <MicrophoneSourceRow item={microphone} paused={paused} onChanged={onChanged} />}
            {showMeetings && <MeetingRecordingRow listeningOn={on} />}
            {listening.systemAudioSupported && <SystemAudioOutsideCallsRow listeningOn={on} />}
            {showMeetings && <SpeechModelRow />}
        </SettingsControlCard>
    );
}

// ── Note what I copy ──

/** The clipboard switch. No macOS permission sits behind it, so there is
 *  nothing to grant: only the switch, on by default, paused with observing. */
function ClipboardCard({
    permissions,
    paused,
    onChanged,
}: {
    permissions: PlatformPermissions | null;
    paused: boolean;
    onChanged: () => Promise<unknown>;
}) {
    const { t } = useTranslation("settings");
    const { failure: changeFailure, fail: failChange } = useSettingsFailure();
    const [pending, setPending] = useState<boolean | null>(null);
    const item = (permissions?.items ?? []).find((entry) => entry.key === CLIPBOARD_PERMISSION_KEY) ?? null;
    if (!item) return null;

    const title = t("observation.clipboard.title");
    const handleChange = async (next: boolean) => {
        setPending(next);
        try {
            await SetPermissionEnabled(CLIPBOARD_PERMISSION_KEY, next);
        } catch {
            failChange(t("observation.clipboard.toggleFailed"), () => void handleChange(next));
        } finally {
            await onChanged();
            setPending(null);
        }
    };

    return (
        <SettingsControlCard
            title={title}
            description={t("observation.clipboard.description")}
            control={
                <Switch
                    aria-label={title}
                    checked={pending ?? item.enabled}
                    disabled={paused || pending !== null}
                    onCheckedChange={(next) => void handleChange(next)}
                />
            }
        >
            {paused && <SettingsReason>{t("observation.pausedReason")}</SettingsReason>}
            <SettingsFailureLine failure={changeFailure} />
        </SettingsControlCard>
    );
}

// ── Recent files ──

function FilesFolderRow({ folder }: { folder: FilesFolderView }) {
    const { t } = useTranslation("settings");
    let description: string;
    if (!folder.checked) description = t("observation.files.notReadYet");
    else if (!folder.available) description = t("observation.files.notAvailable");
    else description = t("observation.files.folderRecent", { count: folder.recent });
    return <SettingsRow title={t(`observation.files.folders.${folder.key}` as never)} description={description} />;
}

/** The documents opened and edited lately, from Spotlight: the switch, the
 *  folders Isoform can see, and the folders kept private. No permission
 *  sits behind it, and a folder macOS has not opened to Isoform is shown as
 *  not available rather than asked for. */
function FilesCard({
    permissions,
    paused,
    onChanged,
}: {
    permissions: PlatformPermissions | null;
    paused: boolean;
    onChanged: () => Promise<unknown>;
}) {
    const { t, i18n } = useTranslation("settings");
    const { failure: changeFailure, fail: failChange } = useSettingsFailure();
    const [overview, setOverview] = useState<FilesOverview | null>(null);
    const [loadFailed, setLoadFailed] = useState(false);
    const [refreshing, setRefreshing] = useState(false);
    const [pendingRead, setPendingRead] = useState<boolean | null>(null);
    const [changingFolders, setChangingFolders] = useState(false);

    const reload = useCallback(() => {
        GetFilesOverview()
            .then((next) => {
                setOverview(next);
                setLoadFailed(false);
            })
            .catch(() => setLoadFailed(true));
    }, []);

    useEffect(() => {
        reload();
        const offs = [FILES_READ_EVENT, "permissions:changed"].map((name) => EventsOn(name, () => reload()));
        window.addEventListener("focus", reload);
        return () => {
            offs.forEach((off) => off());
            window.removeEventListener("focus", reload);
        };
    }, [reload]);

    const item = (permissions?.items ?? []).find((entry) => entry.key === FILES_PERMISSION_KEY) ?? null;
    if (!item) return null;
    const reading = pendingRead ?? item.enabled;

    const handleRead = async (next: boolean) => {
        setPendingRead(next);
        try {
            await SetPermissionEnabled(FILES_PERMISSION_KEY, next);
        } catch {
            failChange(t("observation.files.toggleFailed"), () => void handleRead(next));
        } finally {
            await onChanged();
            setPendingRead(null);
            reload();
        }
    };

    const handleRefresh = async () => {
        setRefreshing(true);
        try {
            await SyncFilesNow();
        } catch {
            failChange(t("observation.files.refreshFailed"), () => void handleRefresh());
        } finally {
            setRefreshing(false);
            reload();
        }
    };

    const handleAddPrivate = async () => {
        setChangingFolders(true);
        try {
            await AddFilesPrivateFolder(t("observation.files.private.pickerTitle"));
        } catch {
            failChange(t("observation.files.private.changeFailed"), () => void handleAddPrivate());
        } finally {
            setChangingFolders(false);
            reload();
        }
    };

    const handleRemovePrivate = async (id: string) => {
        setChangingFolders(true);
        try {
            await RemoveFilesPrivateFolder(id);
        } catch {
            failChange(t("observation.files.private.changeFailed"), () => void handleRemovePrivate(id));
        } finally {
            setChangingFolders(false);
            reload();
        }
    };

    const lastRead = formatWhen(overview?.lastReadAt ?? "", i18n.language);
    let readingDescription = t("observation.files.readingOff");
    if (reading) {
        const parts: string[] = [];
        if (overview) {
            parts.push(t("observation.files.today", { count: overview.todayCount }));
            if (overview.linkedCount > 0) parts.push(t("observation.files.linked", { count: overview.linkedCount }));
        }
        if (lastRead) parts.push(t("observation.files.lastRead", { when: lastRead }));
        readingDescription = parts.join(" \u00b7 ") || t("observation.files.readingDescription");
    }
    const readingTitle = t("observation.files.readingTitle");

    return (
        <SettingsCard
            title={t("observation.files.title")}
            description={t("observation.files.description")}
            headerRight={
                <Button
                    size="sm"
                    variant="outline"
                    onClick={() => void handleRefresh()}
                    disabled={!reading || refreshing || paused}
                >
                    <RefreshCw className="h-3.5 w-3.5" />
                    {t("observation.files.refresh")}
                </Button>
            }
        >
            {loadFailed && <SettingsFailureLine failure={{ text: t("observation.files.loadFailed"), retry: reload }} />}
            {paused && <SettingsReason>{t("observation.pausedReason")}</SettingsReason>}
            <SettingsRow
                title={readingTitle}
                description={readingDescription}
                control={
                    <Switch
                        aria-label={readingTitle}
                        checked={reading}
                        disabled={pendingRead !== null || paused}
                        onCheckedChange={(next) => void handleRead(next)}
                    />
                }
            />
            {reading && (overview?.folders ?? []).map((folder) => <FilesFolderRow key={folder.key} folder={folder} />)}
            <SettingsRow
                title={t("observation.files.private.title")}
                description={t("observation.files.private.description")}
                control={
                    <Button
                        size="sm"
                        variant="outline"
                        onClick={() => void handleAddPrivate()}
                        disabled={!overview || changingFolders}
                    >
                        {t("observation.files.private.add")}
                    </Button>
                }
            />
            {(overview?.privateFolders ?? []).map((folder) => (
                <SettingsRow
                    key={folder.id}
                    title={folder.displayPath}
                    control={
                        <Button
                            size="sm"
                            variant="outline"
                            aria-label={t("observation.files.private.removeLabel", { folder: folder.displayPath })}
                            onClick={() => void handleRemovePrivate(folder.id)}
                            disabled={changingFolders}
                        >
                            {t("observation.files.private.remove")}
                        </Button>
                    }
                />
            ))}
            <SettingsFailureLine failure={changeFailure} />
        </SettingsCard>
    );
}

// ── Calendars Isoform can see ──

/** "2 hours ago" for an RFC 3339 time, or null when it is empty or unreadable. */
function formatWhen(value: string, locale: string, now = Date.now()): string | null {
    if (!value) return null;
    const time = Date.parse(value);
    if (Number.isNaN(time)) return null;
    const seconds = Math.round((time - now) / 1000);
    const formatter = new Intl.RelativeTimeFormat(locale, { numeric: "auto" });
    const absolute = Math.abs(seconds);
    if (absolute < 60) return formatter.format(seconds, "second");
    if (absolute < 3_600) return formatter.format(Math.round(seconds / 60), "minute");
    if (absolute < 86_400) return formatter.format(Math.round(seconds / 3_600), "hour");
    return formatter.format(Math.round(seconds / 86_400), "day");
}

function CalendarSourceRow({ source }: { source: CalendarSourceView }) {
    const { t, i18n } = useTranslation("settings");
    const title =
        source.title && source.title.toLowerCase() !== source.providerLabel.toLowerCase()
            ? `${source.providerLabel} \u00b7 ${source.title}`
            : source.providerLabel;
    const parts: string[] = [];
    if (source.kind === "eventkit" && source.calendars > 0) {
        parts.push(t("observation.calendars.calendars", { count: source.calendars }));
    }
    parts.push(t(`observation.calendars.detectedBy.${source.detectedBy}` as never) as string);
    const read = formatWhen(source.lastReadAt, i18n.language);
    if (read) parts.push(t("observation.calendars.lastRead", { when: read }));
    else if (source.kind === "web") {
        const seen = formatWhen(source.lastSeenAt, i18n.language);
        if (seen) parts.push(t("observation.calendars.lastSeen", { when: seen }));
    } else parts.push(t("observation.calendars.neverRead"));
    return (
        <SettingsRow
            title={title}
            description={parts.join(" \u00b7 ")}
            control={
                <span className="inline-flex items-center rounded-full bg-ink/[0.05] px-2 py-0.5 text-[10px] font-medium text-ink/55">
                    {t(`observation.calendars.sourceKind.${source.kind}` as never)}
                </span>
            }
        />
    );
}

/** The calendars on this Mac and the web calendars seen in the browser, the
 *  reading switch, and web calendar reading while the Mac is idle. */
function CalendarsCard({
    permissions,
    paused,
    onGrant,
    onChanged,
}: {
    permissions: PlatformPermissions | null;
    paused: boolean;
    onGrant: (item: PermissionItem) => void;
    onChanged: () => Promise<unknown>;
}) {
    const { t, i18n } = useTranslation("settings");
    const { failure: changeFailure, fail: failChange } = useSettingsFailure();
    const [overview, setOverview] = useState<CalendarOverview | null>(null);
    const [holds, setHolds] = useState<HoldSettings | null>(null);
    const [holdPending, setHoldPending] = useState(false);
    const [loadFailed, setLoadFailed] = useState(false);
    const [refreshing, setRefreshing] = useState(false);
    const [pendingRead, setPendingRead] = useState<boolean | null>(null);
    const [pendingPeek, setPendingPeek] = useState<boolean | null>(null);

    const reload = useCallback(() => {
        GetHoldSettings().then(setHolds).catch(() => setHolds(null));
        GetCalendarOverview()
            .then((next) => {
                setOverview(next);
                setLoadFailed(false);
            })
            .catch(() => setLoadFailed(true));
    }, []);

    useEffect(() => {
        reload();
        const offs = [CALENDAR_SYNCED_EVENT, "permissions:changed"].map((name) => EventsOn(name, () => reload()));
        window.addEventListener("focus", reload);
        return () => {
            offs.forEach((off) => off());
            window.removeEventListener("focus", reload);
        };
    }, [reload]);

    const item = (permissions?.items ?? []).find((entry) => entry.key === CALENDARS_PERMISSION_KEY) ?? null;
    const granted = item?.granted ?? false;
    const reading = pendingRead ?? item?.enabled ?? false;

    const handleRead = async (next: boolean) => {
        setPendingRead(next);
        try {
            await SetPermissionEnabled(CALENDARS_PERMISSION_KEY, next);
        } catch {
            failChange(t("observation.calendars.toggleFailed"), () => void handleRead(next));
        } finally {
            await onChanged();
            setPendingRead(null);
            reload();
        }
    };

    const handlePeek = async (next: boolean) => {
        setPendingPeek(next);
        try {
            await SetCalendarPeekEnabled(next);
            setOverview((prev) => (prev ? { ...prev, peekEnabled: next } : prev));
        } catch {
            failChange(t("observation.calendars.peek.toggleFailed"), () => void handlePeek(next));
        } finally {
            setPendingPeek(null);
        }
    };

    const handleOfferHolds = async (next: boolean) => {
        setHoldPending(true);
        try {
            await SetOfferHolds(next);
            notifyHoldSettingsChanged();
            setHolds((previous) => previous ? { ...previous, offerHolds: next } : previous);
        } catch {
            failChange(t("observation.calendars.holds.changeFailed"), () => void handleOfferHolds(next));
        } finally {
            setHoldPending(false);
        }
    };

    const handleHoldCalendar = async (id: string) => {
        setHoldPending(true);
        try {
            await SetHoldCalendar(id);
            notifyHoldSettingsChanged();
            setHolds((previous) => previous ? { ...previous, calendarId: id } : previous);
        } catch {
            failChange(t("observation.calendars.holds.changeFailed"), () => void handleHoldCalendar(id));
        } finally {
            setHoldPending(false);
        }
    };

    const handleRefresh = async () => {
        setRefreshing(true);
        try {
            await SyncCalendarsNow();
        } catch {
            failChange(t("observation.calendars.refreshFailed"), () => void handleRefresh());
        } finally {
            setRefreshing(false);
            reload();
        }
    };

    // Platforms without a calendar store and without the calendars permission
    // item (Windows, Linux) have nothing to show here.
    if (overview && !overview.supported && !item) return null;

    const sources = overview?.sources ?? [];
    const peekEnabled = pendingPeek ?? overview?.peekEnabled ?? true;
    let peekNote: string;
    if (overview && overview.peekProviders.length > 0) {
        peekNote = t("observation.calendars.peek.willRead", {
            providers: overview.peekProviders
                .map((p) => t(`observation.calendars.providers.${p}` as never) as string)
                .join(", "),
        });
    } else {
        peekNote = t("observation.calendars.peek.nothingToRead");
    }
    const lastPeek = formatWhen(overview?.peekLastAt ?? "", i18n.language);
    const lastSync = formatWhen(overview?.lastSyncAt ?? "", i18n.language);

    return (
        <SettingsCard
            title={t("observation.calendars.title")}
            description={t("observation.calendars.description")}
            headerRight={
                <Button size="sm" variant="outline" onClick={() => void handleRefresh()} disabled={!granted || refreshing || paused}>
                    <RefreshCw className="h-3.5 w-3.5" />
                    {t("observation.calendars.refresh")}
                </Button>
            }
        >
            {loadFailed && <SettingsFailureLine failure={{ text: t("observation.calendars.loadFailed"), retry: reload }} />}
            {item && !granted && (
                <>
                    <SettingsReason>{t("observation.calendars.needsAccess")}</SettingsReason>
                    <MissingPermissionRow item={item} onGrant={onGrant} />
                </>
            )}
            {item && granted && (
                <SettingsRow
                    title={t("observation.calendars.readingTitle")}
                    description={
                        reading
                            ? [
                                  overview ? t("observation.calendars.upcoming", { count: overview.upcomingCount }) : "",
                                  lastSync ? t("observation.calendars.lastSync", { when: lastSync }) : "",
                              ]
                                  .filter(Boolean)
                                  .join(" \u00b7 ") || t("observation.calendars.readingDescription")
                            : t("observation.calendars.readingOff")
                    }
                    control={
                        <Switch
                            aria-label={t("observation.calendars.readingTitle")}
                            checked={reading}
                            disabled={pendingRead !== null || paused}
                            onCheckedChange={(next) => void handleRead(next)}
                        />
                    }
                />
            )}
            {item && granted && holds && (
                <>
                    <SettingsRow
                        title={t("observation.calendars.holds.offerTitle")}
                        description={t("observation.calendars.holds.offerDescription")}
                        control={<Switch aria-label={t("observation.calendars.holds.offerTitle")} checked={holds.offerHolds} disabled={holdPending || !reading} onCheckedChange={(next) => void handleOfferHolds(next)} />}
                    />
                    <SettingsRow
                        title={t("observation.calendars.holds.calendarTitle")}
                        description={<>{t("observation.calendars.holds.calendarDescription")}{overview?.peekProviders.map((provider) => <span key={provider}> {t("observation.calendars.holds.browserOnly", { account: t(`observation.calendars.providers.${provider}` as never) })}</span>)}</>}
                        control={
                            <select aria-label={t("observation.calendars.holds.calendarTitle")} value={holds.calendarId} disabled={holdPending || !reading || holds.calendars.length === 0} onChange={(event) => void handleHoldCalendar(event.target.value)} className="rounded-md border border-ink/15 bg-transparent px-2 py-1 text-sm text-ink">
                                {holds.calendarId === "" && <option value="">{t(holds.calendars.length ? "observation.calendars.holds.chooseCalendar" : "observation.calendars.holds.noCalendar")}</option>}
                                {[...new Set(holds.calendars.map((calendar) => calendar.sourceTitle))].map((account) => (
                                    <optgroup key={account} label={account}>
                                        {holds.calendars.filter((calendar) => calendar.sourceTitle === account).map((calendar) => <option key={calendar.id} value={calendar.id}>{calendar.title}</option>)}
                                    </optgroup>
                                ))}
                            </select>
                        }
                    />
                </>
            )}
            {sources.map((source) => (
                <CalendarSourceRow key={source.id} source={source} />
            ))}
            {overview && sources.length === 0 && <SettingsReason>{t("observation.calendars.empty")}</SettingsReason>}
            <SettingsRow
                title={t("observation.calendars.peek.title")}
                description={`${t("observation.calendars.peek.description")} ${peekNote}${
                    lastPeek ? ` ${t("observation.calendars.peek.lastPeek", { when: lastPeek })}` : ""
                }`}
                control={
                    <Switch
                        aria-label={t("observation.calendars.peek.title")}
                        checked={peekEnabled}
                        disabled={!overview || pendingPeek !== null}
                        onCheckedChange={(next) => void handlePeek(next)}
                    />
                }
            />
            <SettingsFailureLine failure={changeFailure} />
        </SettingsCard>
    );
}

// ── Reminders Isoform can see ──

function ReminderListRow({ list }: { list: ReminderListView }) {
    const { t } = useTranslation("settings");
    const parts = [t("observation.reminders.listOpen", { count: list.open })];
    if (list.overdue > 0) parts.push(t("observation.reminders.overdue", { count: list.overdue }));
    return (
        <SettingsRow
            title={list.title || t("observation.reminders.untitledList")}
            description={parts.join(" \u00b7 ")}
        />
    );
}

/** The reminders on this Mac: access, the reading switch, and the open
 *  reminders per list. Open reminders are the user's commitments. */
function RemindersCard({
    permissions,
    paused,
    onGrant,
    onChanged,
}: {
    permissions: PlatformPermissions | null;
    paused: boolean;
    onGrant: (item: PermissionItem) => void;
    onChanged: () => Promise<unknown>;
}) {
    const { t, i18n } = useTranslation("settings");
    const { failure: changeFailure, fail: failChange } = useSettingsFailure();
    const [overview, setOverview] = useState<RemindersOverview | null>(null);
    const [loadFailed, setLoadFailed] = useState(false);
    const [refreshing, setRefreshing] = useState(false);
    const [pendingRead, setPendingRead] = useState<boolean | null>(null);

    const reload = useCallback(() => {
        GetRemindersOverview()
            .then((next) => {
                setOverview(next);
                setLoadFailed(false);
            })
            .catch(() => setLoadFailed(true));
    }, []);

    useEffect(() => {
        reload();
        const offs = [REMINDERS_SYNCED_EVENT, "permissions:changed"].map((name) => EventsOn(name, () => reload()));
        window.addEventListener("focus", reload);
        return () => {
            offs.forEach((off) => off());
            window.removeEventListener("focus", reload);
        };
    }, [reload]);

    const item = (permissions?.items ?? []).find((entry) => entry.key === REMINDERS_PERMISSION_KEY) ?? null;
    const granted = item?.granted ?? false;
    const reading = pendingRead ?? item?.enabled ?? false;

    const handleRead = async (next: boolean) => {
        setPendingRead(next);
        try {
            await SetPermissionEnabled(REMINDERS_PERMISSION_KEY, next);
        } catch {
            failChange(t("observation.reminders.toggleFailed"), () => void handleRead(next));
        } finally {
            await onChanged();
            setPendingRead(null);
            reload();
        }
    };

    const handleRefresh = async () => {
        setRefreshing(true);
        try {
            await SyncRemindersNow();
        } catch {
            failChange(t("observation.reminders.refreshFailed"), () => void handleRefresh());
        } finally {
            setRefreshing(false);
            reload();
        }
    };

    // Platforms without a reminders store and without the reminders
    // permission item (Windows, Linux) have nothing to show here.
    if (!item && (!overview || !overview.supported)) return null;

    const lists = overview?.lists ?? [];
    const lastSync = formatWhen(overview?.lastSyncAt ?? "", i18n.language);
    let readingDescription = t("observation.reminders.readingOff");
    if (reading) {
        const parts: string[] = [];
        if (overview) {
            parts.push(t("observation.reminders.open", { count: overview.openCount }));
            if (overview.overdueCount > 0) {
                parts.push(t("observation.reminders.overdue", { count: overview.overdueCount }));
            }
        }
        if (lastSync) parts.push(t("observation.reminders.lastSync", { when: lastSync }));
        readingDescription = parts.join(" \u00b7 ") || t("observation.reminders.readingDescription");
    }

    return (
        <SettingsCard
            title={t("observation.reminders.title")}
            description={t("observation.reminders.description")}
            headerRight={
                <Button
                    size="sm"
                    variant="outline"
                    onClick={() => void handleRefresh()}
                    disabled={!granted || !reading || refreshing || paused}
                >
                    <RefreshCw className="h-3.5 w-3.5" />
                    {t("observation.reminders.refresh")}
                </Button>
            }
        >
            {loadFailed && <SettingsFailureLine failure={{ text: t("observation.reminders.loadFailed"), retry: reload }} />}
            {item && !granted && (
                <>
                    <SettingsReason>{t("observation.reminders.needsAccess")}</SettingsReason>
                    <MissingPermissionRow item={item} onGrant={onGrant} />
                </>
            )}
            {item && granted && (
                <SettingsRow
                    title={t("observation.reminders.readingTitle")}
                    description={readingDescription}
                    control={
                        <Switch
                            aria-label={t("observation.reminders.readingTitle")}
                            checked={reading}
                            disabled={pendingRead !== null || paused}
                            onCheckedChange={(next) => void handleRead(next)}
                        />
                    }
                />
            )}
            {granted && reading && lists.map((list) => <ReminderListRow key={list.title} list={list} />)}
            {granted && reading && overview && lists.length === 0 && (
                <SettingsReason>{t("observation.reminders.empty")}</SettingsReason>
            )}
            <SettingsFailureLine failure={changeFailure} />
        </SettingsCard>
    );
}

// ── Mail Isoform can see ──

/** The headers of the last week's mail, read from Mail while it is open:
 *  access (asked for only while Mail is open), the reading switch, and how
 *  many messages and waiting replies Isoform sees. */
function MailCard({
    permissions,
    paused,
    onGrant,
    onChanged,
}: {
    permissions: PlatformPermissions | null;
    paused: boolean;
    onGrant: (item: PermissionItem) => void;
    onChanged: () => Promise<unknown>;
}) {
    const { t, i18n } = useTranslation("settings");
    const { failure: changeFailure, fail: failChange } = useSettingsFailure();
    const [overview, setOverview] = useState<MailOverview | null>(null);
    const [loadFailed, setLoadFailed] = useState(false);
    const [refreshing, setRefreshing] = useState(false);
    const [pendingRead, setPendingRead] = useState<boolean | null>(null);

    const reload = useCallback(() => {
        GetMailOverview()
            .then((next) => {
                setOverview(next);
                setLoadFailed(false);
            })
            .catch(() => setLoadFailed(true));
    }, []);

    useEffect(() => {
        reload();
        const offs = [MAIL_SYNCED_EVENT, "permissions:changed"].map((name) => EventsOn(name, () => reload()));
        window.addEventListener("focus", reload);
        return () => {
            offs.forEach((off) => off());
            window.removeEventListener("focus", reload);
        };
    }, [reload]);

    const item = (permissions?.items ?? []).find((entry) => entry.key === MAIL_PERMISSION_KEY) ?? null;
    const granted = item?.granted ?? false;
    const reading = pendingRead ?? item?.enabled ?? false;
    const mailOpen = overview?.mailOpen ?? false;

    const handleRead = async (next: boolean) => {
        setPendingRead(next);
        try {
            await SetPermissionEnabled(MAIL_PERMISSION_KEY, next);
        } catch {
            failChange(t("observation.mail.toggleFailed"), () => void handleRead(next));
        } finally {
            await onChanged();
            setPendingRead(null);
            reload();
        }
    };

    const handleRefresh = async () => {
        setRefreshing(true);
        try {
            await SyncMailNow();
        } catch {
            failChange(t("observation.mail.refreshFailed"), () => void handleRefresh());
        } finally {
            setRefreshing(false);
            reload();
        }
    };

    // Platforms without Mail and without the mail permission item (Windows,
    // Linux) have nothing to show here.
    if (!item && (!overview || !overview.supported)) return null;

    const lastSync = formatWhen(overview?.lastReadAt ?? "", i18n.language);
    let readingDescription = t("observation.mail.readingOff");
    if (reading) {
        const parts: string[] = [];
        if (overview && !mailOpen) parts.push(t("observation.mail.notOpen"));
        if (overview) {
            parts.push(t("observation.mail.received", { count: overview.received }));
            if (overview.waiting > 0) parts.push(t("observation.mail.waiting", { count: overview.waiting }));
        }
        if (lastSync) parts.push(t("observation.mail.lastSync", { when: lastSync }));
        readingDescription = parts.join(" \u00b7 ") || t("observation.mail.readingDescription");
    }

    return (
        <SettingsCard
            title={t("observation.mail.title")}
            description={t("observation.mail.description")}
            headerRight={
                <Button
                    size="sm"
                    variant="outline"
                    onClick={() => void handleRefresh()}
                    disabled={!granted || !reading || refreshing || paused || !mailOpen}
                >
                    <RefreshCw className="h-3.5 w-3.5" />
                    {t("observation.mail.refresh")}
                </Button>
            }
        >
            {loadFailed && <SettingsFailureLine failure={{ text: t("observation.mail.loadFailed"), retry: reload }} />}
            {paused && item && granted && <SettingsReason>{t("observation.pausedReason")}</SettingsReason>}
            {item && granted && reading && overview?.stalled && (
                <SettingsReason>{t("observation.mail.stalled")}</SettingsReason>
            )}
            {item && !granted && !mailOpen && <SettingsReason>{t("observation.mail.openMailFirst")}</SettingsReason>}
            {item && !granted && mailOpen && (
                <>
                    <SettingsReason>{t("observation.mail.needsAccess")}</SettingsReason>
                    <MissingPermissionRow item={item} onGrant={onGrant} />
                </>
            )}
            {item && granted && (
                <SettingsRow
                    title={t("observation.mail.readingTitle")}
                    description={readingDescription}
                    control={
                        <Switch
                            aria-label={t("observation.mail.readingTitle")}
                            checked={reading}
                            disabled={pendingRead !== null || paused}
                            onCheckedChange={(next) => void handleRead(next)}
                        />
                    }
                />
            )}
            <SettingsFailureLine failure={changeFailure} />
        </SettingsCard>
    );
}

// ── Advanced: capture status ──

type CaptureState = "running" | "paused" | "off" | "stopped";

const CAPTURE_STATE_DOT: Record<CaptureState, string> = {
    running: "bg-accent",
    paused: "bg-warning",
    off: "bg-ink/25",
    stopped: "bg-error",
};

const AUDIO_SUSPENSION_REASONS = new Set([
    "desktop_locked",
    "privacy_pause",
    "disabled",
    "not_running",
    "microphone_paused",
]);

function audioCaptureState(status: main.BackgroundAudioStatus | null, source: string): CaptureState {
    if (status?.activeSources?.includes(source)) return "running";
    // The Microphone switch is off on purpose: off, not a fault.
    if (source === "microphone" && status?.microphonePaused) return "off";
    switch (status?.suspendedReason) {
        case "desktop_locked":
        case "privacy_pause":
            return "paused";
        case "disabled":
        case "microphone_paused":
            return "off";
        default:
            return "stopped";
    }
}

function CaptureStateLabel({ state }: { state: CaptureState }) {
    const { t } = useTranslation("settings");
    return (
        <span className="inline-flex items-center gap-1.5 text-xs text-muted">
            <span aria-hidden className={`inline-block h-2 w-2 rounded-full ${CAPTURE_STATE_DOT[state]}`} />
            {t(`observation.capture.state.${state}` as never)}
        </span>
    );
}

/** Read-only: what is being captured right now and why. The user-safe part
 *  of Developer tools > Audio capture, plus the screen. */
function CaptureStatusCard({
    paused,
    watchingOn,
    systemAudioSupported,
}: {
    paused: boolean;
    /** null until the permissions are read. */
    watchingOn: boolean | null;
    systemAudioSupported: boolean;
}) {
    const { t } = useTranslation("settings");
    const [audio, setAudio] = useState<main.BackgroundAudioStatus | null>(null);
    const [screenRunning, setScreenRunning] = useState<boolean | null>(null);
    const [failed, setFailed] = useState(false);
    const [reading, setReading] = useState(false);

    const reload = useCallback(() => {
        setReading(true);
        void Promise.all([GetBackgroundAudioStatus(), GetActivityMonitorStatus()])
            .then(([audioStatus, screenStatus]) => {
                setAudio(audioStatus);
                setScreenRunning(Boolean(screenStatus?.monitoring));
                setFailed(false);
            })
            .catch(() => {
                setAudio(null);
                setScreenRunning(null);
                setFailed(true);
            })
            .finally(() => setReading(false));
    }, []);

    useEffect(() => {
        reload();
        const offs = [
            "audio-capture:event",
            CLOSED_EYES_CHANGED_EVENT,
            "background-listening:changed",
            "permissions:changed",
        ].map((name) => EventsOn(name, () => reload()));
        return () => offs.forEach((off) => off());
    }, [reload]);

    let screenState: CaptureState = "stopped";
    let screenReason: string | undefined;
    if (screenRunning) {
        screenState = "running";
    } else if (paused) {
        screenState = "paused";
        screenReason = t("observation.capture.reason.privacy_pause");
    } else if (watchingOn === false) {
        screenState = "off";
        screenReason = t("observation.capture.screenOff");
    } else if (watchingOn && screenRunning === false) {
        screenReason = t("observation.capture.screenNotRunning");
    }

    const suspendedReason = audio?.suspendedReason ?? "";
    const audioReason =
        audio && !audio.running && AUDIO_SUSPENSION_REASONS.has(suspendedReason)
            ? (t(`observation.capture.reason.${suspendedReason}` as never) as string)
            : null;

    return (
        <SettingsCard
            title={t("observation.capture.title")}
            description={t("observation.capture.description")}
            headerRight={
                <Button size="sm" variant="outline" onClick={reload} disabled={reading}>
                    {reading ? (
                        <Loader2 className="h-3.5 w-3.5 animate-spin" data-testid="capture-reading" />
                    ) : (
                        <RefreshCw className="h-3.5 w-3.5" />
                    )}
                    {t("observation.capture.refresh")}
                </Button>
            }
        >
            {failed ? (
                <SettingsFailureLine failure={{ text: t("observation.capture.loadFailed"), retry: reload }} />
            ) : (
                <>
                    <SettingsRow
                        title={t("observation.capture.screen")}
                        description={screenReason}
                        control={<CaptureStateLabel state={screenState} />}
                    />
                    <SettingsRow
                        title={t("observation.capture.microphone")}
                        description={
                            audio?.microphonePaused && !AUDIO_SUSPENSION_REASONS.has(suspendedReason)
                                ? t("observation.capture.reason.microphone_paused")
                                : (audioReason ?? undefined)
                        }
                        control={<CaptureStateLabel state={audioCaptureState(audio, "microphone")} />}
                    />
                    {systemAudioSupported && (
                        <SettingsRow
                            title={t("observation.capture.systemAudio")}
                            control={<CaptureStateLabel state={audioCaptureState(audio, "system_audio")} />}
                        />
                    )}
                </>
            )}
        </SettingsCard>
    );
}

// ── Advanced: what lives on the Privacy page ──

function PrivacyLinksCard({ onOpenPage }: { onOpenPage?: OpenSettingsPage }) {
    const { t } = useTranslation("settings");
    const [days, setDays] = useState<number | null>(null);

    useEffect(() => {
        GetActivityRetentionDays()
            .then(setDays)
            .catch(() => setDays(null));
    }, []);

    const openPrivacy = (
        <Button size="sm" variant="outline" onClick={() => openSettingsPage("security", onOpenPage)}>
            {t("observation.related.open")}
        </Button>
    );

    let retention: string | undefined;
    if (days === 0) retention = t("observation.related.retentionUnlimited");
    else if (days !== null) retention = t("observation.related.retentionDays", { count: days });

    return (
        <SettingsCard title={t("observation.related.title")} description={t("observation.related.description")}>
            <SettingsRow
                title={t("observation.related.ignoredAppsTitle")}
                description={t("observation.related.ignoredAppsDescription")}
                control={openPrivacy}
            />
            <SettingsRow
                title={t("observation.related.retentionTitle")}
                description={retention}
                control={openPrivacy}
            />
        </SettingsCard>
    );
}

// ── Page ──

/** Observation (page id "activity"): what Isoform sees and hears. */
export function ObservationPage({ onOpenPage }: { onOpenPage?: OpenSettingsPage } = {}) {
    const { t } = useTranslation("settings");
    const { permissions, refresh, requestPermission } = usePermissions();
    const pause = useObservationPause();
    const listening = useListening();
    const paused = pause.paused === true;
    const screenItems = screenPermissionItems(permissions);
    const watchingOn = permissions
        ? screenItems.length > 0 && screenItems.every((item) => item.enabled)
        : null;

    return (
        <>
            <PauseObservingCard pause={pause} />
            <div {...settingsAnchor("sees-screen")}><WatchScreenCard
                permissions={permissions}
                paused={paused}
                onGrant={(item) => void requestPermission(item)}
                onChanged={refresh}
            /></div>
            <div {...settingsAnchor("sees-calls")}><ListenCard
                listening={listening}
                paused={paused}
                permissions={permissions}
                onGrant={(item) => void requestPermission(item)}
                onChanged={refresh}
                showMeetings={isWailsEnvironment()}
            /></div>
            <ClipboardCard permissions={permissions} paused={paused} onChanged={refresh} />
            <FilesCard permissions={permissions} paused={paused} onChanged={refresh} />
            <div {...settingsAnchor("sees-calendar")}><CalendarsCard
                permissions={permissions}
                paused={paused}
                onGrant={(item) => void requestPermission(item)}
                onChanged={refresh}
            /></div>
            <RemindersCard
                permissions={permissions}
                paused={paused}
                onGrant={(item) => void requestPermission(item)}
                onChanged={refresh}
            />
            <div {...settingsAnchor("sees-mail")}><MailCard
                permissions={permissions}
                paused={paused}
                onGrant={(item) => void requestPermission(item)}
                onChanged={refresh}
            /></div>
            <CapturedCountsCard />
            <SettingsAdvanced pageId="activity" hint={t("observation.advanced.hint")}>
                <CaptureStatusCard
                    paused={paused}
                    watchingOn={watchingOn}
                    systemAudioSupported={listening.systemAudioSupported}
                />
                <PrivacyLinksCard onOpenPage={onOpenPage} />
            </SettingsAdvanced>
        </>
    );
}
