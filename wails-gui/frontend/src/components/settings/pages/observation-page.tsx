import { useCallback, useEffect, useMemo, useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import type { TFunction } from "i18next";
import { ChevronLeft, ChevronRight, Loader2, RefreshCw } from "lucide-react";
import { activeInterval } from "@/lib/app-activity";
import { SettingsFailureLine, useSettingsFailure, type SettingsFailure } from "@/components/settings/settings-failure";
import { Button } from "@/components/ui/button";
import { ConfirmDialog } from "@/components/confirm-dialog";
import { Combobox } from "@/components/ui/combobox";
import { Switch } from "@/components/ui/switch";
import {
    GetActivityMonitorStatus,
    GetBackgroundAudioStatus,
    SetPermissionEnabled,
} from "../../../../wailsjs/go/main/App";
import {
    AddFilesPrivateFolder,
    GetAfterCallSpeechModel,
    GetAssistantChoice,
    GetActivityVision,
    SetActivityVisionEnabled,
    type ActivityVisionView,
    GetModelChoices,
    PlanSetupModelDownload,
    GetWatchListening,
    GetBrowsersRead,
    TurnOffWatchListening,
    type WatchListeningView,
    GetCalendarOverview,
    GetFilesOverview,
    ForgetMessages,
    GetMailOverview,
    GetMessagesOverview,
    GetRemindersOverview,
    ListPrincipals,
    SetPeopleLeftOut,
    GetSystemAudioOutsideCallsEnabled,
    IsClosedEyesModeEnabled,
    RemoveAfterCallSpeechModel,
    SetAfterCallSpeechModelEnabled,
    SetAutomaticMeetingRecordingEnabled,
    SetCalendarPeekEnabled,
    RemoveFilesPrivateFolder,
    SetSystemAudioOutsideCallsEnabled,
    SyncCalendarsNow,
    SyncFilesNow,
    SyncMailNow,
    SyncMessagesNow,
    SyncRemindersNow,
    type AfterCallSpeechModelView,
    type CalendarOverview,
    type CalendarSourceView,
    type FilesFolderView,
    type FilesOverview,
    type MailOverview,
    type MessagesOverview,
    type PrincipalDTO,
    type ReminderListView,
    type RemindersOverview,
} from "@/lib/wails-shim-app";
import { EventsOn } from "../../../../wailsjs/runtime/runtime";
import { GetAutomaticMeetingRecordingEnabled } from "@/lib/page-queries";
import type { main } from "../../../../wailsjs/go/models";
import { usePermissions } from "@/hooks/usePermissions";
import { isWailsEnvironment } from "@/lib/browser-bridge";
import type { PermissionItem, PlatformPermissions } from "@/types";
import { SettingsCard, SettingsRow, SettingsSectionHeader } from "@/components/settings/settings-primitives";
import { formatBytes } from "@/components/settings/settings-format";
import { settingsAnchor } from "@/components/settings/settings-advanced";
import { HiddenAppsSection, NotListenedAppsSection, useSystemApps } from "@/components/settings/sources/app-lists";
import { AssistantHistoryCard, AssistantHistoryStopping, useAssistantHistory } from "@/components/settings/sources/assistant-history";
import { assistantName } from "./topic-proposals-card";
import { QUIET_JOBS, wireChoices } from "@/components/onboarding/v2/models";
import { formatModelBytes } from "@/components/onboarding/v2/models";
import type { ModelDownloadPlan } from "@/lib/setup-backend";
import {
    MissingPermissionRow,
    SettingsControlCard,
    SettingsReason,
    useListening,
    type ListeningState,
    type OpenSettingsPage,
} from "./capture-shared";
import { ContactsPermissionRow } from "@/components/settings/sources/contacts-permission-row";

// ── What Isoform sees (page id "activity") ──
//
// Every source on one page, one row each with its line and one switch, in
// computer-settings v1.0.0 rule 2's order: Screen; Calls and meetings; Mail,
// calendar and reminders; Messages; Your watch; Your agents' history. Each
// row opens its details in place, which hold the cards that were on this
// page: Screen has Watch my screen, the apps Isoform never sees and Files;
// Calls and meetings has Listen with its microphone, computer audio, meeting
// and speech model rows, and the apps it doesn't listen to; Mail, calendar
// and reminders has the three cards; Messages its card; Your agents' history
// the Claude Code and Codex switch. Copy and calendar holds went (answers
// v1.0.0 T2). The captured counts went to Your data's source rows (answers
// v1.5.0 T16).

const CLOSED_EYES_CHANGED_EVENT = "closed-eyes:changed";
const CALENDAR_SYNCED_EVENT = "calendar:synced";
const REMINDERS_SYNCED_EVENT = "reminders:synced";
const FILES_READ_EVENT = "files:read";
const MAIL_SYNCED_EVENT = "mail:synced";
const MESSAGES_SYNCED_EVENT = "imessage:synced";
const CALENDARS_PERMISSION_KEY = "calendars";
const REMINDERS_PERMISSION_KEY = "reminders";
const FILES_PERMISSION_KEY = "files";
const MAIL_PERMISSION_KEY = "automation_mail";
const MESSAGES_PERMISSION_KEY = "messages_history";

// Switchable items that are not part of "Watch my screen": the microphone,
// which is the listening switch, and recent files, calendars, reminders, mail
// and Messages, which have their own cards in the sources' details.
const NOT_SCREEN_PERMISSION_KEYS = new Set([
    "microphone",
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

// ── Closed Eyes ──

/** Whether Closed Eyes is on, shared with the menu bar, the tray, the overlay
 *  and the hotkey. Go emits closed-eyes:changed from all of them. Read only:
 *  Closed Eyes has its own page. */
function useClosedEyes(): { paused: boolean | null } {
    const [paused, setPausedState] = useState<boolean | null>(null);

    const reload = useCallback(() => {
        IsClosedEyesModeEnabled()
            .then((value) => setPausedState(value))
            .catch(() => setPausedState(null));
    }, []);

    useEffect(() => {
        reload();
        const off = EventsOn(CLOSED_EYES_CHANGED_EVENT, (next: unknown) => setPausedState(Boolean(next)));
        window.addEventListener("focus", reload);
        return () => {
            off();
            window.removeEventListener("focus", reload);
        };
    }, [reload]);

    return { paused };
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
                <KnownSwitch items={items} retry={() => void onChanged()}>
                <Switch
                    aria-label={title}
                    checked={pending ?? watching}
                    disabled={!permissions || paused || missing.length > 0 || pending !== null}
                    onCheckedChange={(next) => void handleChange(next)}
                />
                </KnownSwitch>
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
    // A switch that couldn't be read is never described as off.
    let readingDescription: string | undefined = item?.enabledUnknown ? undefined : t("observation.files.readingOff");
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
                    <KnownSwitch items={[item]} retry={() => void onChanged()}>
                    <Switch
                        aria-label={readingTitle}
                        checked={reading}
                        disabled={pendingRead !== null || paused}
                        onCheckedChange={(next) => void handleRead(next)}
                    />
                    </KnownSwitch>
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
 *  reading switch, and web calendar reading while the Mac is idle. Calendar
 *  holds aren't shown here (answers v1.0.0 T2: they go behind their switch,
 *  and the rework of November 14 turned them off). */
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
    const [loadFailed, setLoadFailed] = useState(false);
    const [refreshing, setRefreshing] = useState(false);
    const [pendingRead, setPendingRead] = useState<boolean | null>(null);
    const [pendingPeek, setPendingPeek] = useState<boolean | null>(null);

    const reload = useCallback(() => {
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
                            : item?.enabledUnknown
                              ? undefined
                              : t("observation.calendars.readingOff")
                    }
                    control={
                        <KnownSwitch items={[item]} retry={() => void onChanged()}>
                        <Switch
                            aria-label={t("observation.calendars.readingTitle")}
                            checked={reading}
                            disabled={pendingRead !== null || paused}
                            onCheckedChange={(next) => void handleRead(next)}
                        />
                        </KnownSwitch>
                    }
                />
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
    // A switch that couldn't be read is never described as off.
    let readingDescription: string | undefined = item?.enabledUnknown ? undefined : t("observation.reminders.readingOff");
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
                        <KnownSwitch items={[item]} retry={() => void onChanged()}>
                        <Switch
                            aria-label={t("observation.reminders.readingTitle")}
                            checked={reading}
                            disabled={pendingRead !== null || paused}
                            onCheckedChange={(next) => void handleRead(next)}
                        />
                        </KnownSwitch>
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
    // A switch that couldn't be read is never described as off.
    let readingDescription: string | undefined = item?.enabledUnknown ? undefined : t("observation.mail.readingOff");
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
                        <KnownSwitch items={[item]} retry={() => void onChanged()}>
                        <Switch
                            aria-label={t("observation.mail.readingTitle")}
                            checked={reading}
                            disabled={pendingRead !== null || paused}
                            onCheckedChange={(next) => void handleRead(next)}
                        />
                        </KnownSwitch>
                    }
                />
            )}
            <SettingsFailureLine failure={changeFailure} />
        </SettingsCard>
    );
}

// ── Messages Isoform can see ──

/** Asks and commitments read from the Messages app: Full Disk Access (Grant
 *  only opens System Settings, and only when tapped), the reading switch, the
 *  people left out, and Forget. */
function MessagesCard({
    permissions,
    paused,
    onGrant,
    onChanged,
    onTurnedOn,
    onOpenPeople,
}: {
    permissions: PlatformPermissions | null;
    paused: boolean;
    onGrant: (item: PermissionItem) => void;
    onChanged: () => Promise<unknown>;
    /** After reading is turned on, for the one-time ask (rule 4). */
    onTurnedOn: () => void;
    /** Opens Who you know, where Contacts access is asked for. */
    onOpenPeople?: () => void;
}) {
    const { t, i18n } = useTranslation("settings");
    const { failure: changeFailure, fail: failChange } = useSettingsFailure();
    const [overview, setOverview] = useState<MessagesOverview | null>(null);
    const [people, setPeople] = useState<PrincipalDTO[]>([]);
    const [loadFailed, setLoadFailed] = useState(false);
    const [refreshing, setRefreshing] = useState(false);
    const [changing, setChanging] = useState(false);
    const [pendingRead, setPendingRead] = useState<boolean | null>(null);
    const [askingForget, setAskingForget] = useState(false);

    const reload = useCallback(() => {
        GetMessagesOverview()
            .then((next) => {
                setOverview(next);
                setLoadFailed(false);
            })
            .catch(() => setLoadFailed(true));
    }, []);

    useEffect(() => {
        reload();
        const offs = [MESSAGES_SYNCED_EVENT, "permissions:changed"].map((name) => EventsOn(name, () => reload()));
        window.addEventListener("focus", reload);
        return () => {
            offs.forEach((off) => off());
            window.removeEventListener("focus", reload);
        };
    }, [reload]);

    const item = (permissions?.items ?? []).find((entry) => entry.key === MESSAGES_PERMISSION_KEY) ?? null;
    const granted = item?.granted ?? false;
    const reading = pendingRead ?? item?.enabled ?? false;

    // People are listed only once the picker can be used.
    useEffect(() => {
        if (!item || !granted) return;
        ListPrincipals()
            .then((rows) => setPeople(rows ?? []))
            .catch(() => setPeople([]));
    }, [item, granted]);

    const leftOut = overview?.leftOut ?? [];
    const leftOutIds = useMemo(() => new Set(leftOut.map((person) => person.id)), [leftOut]);
    const options = useMemo(
        () =>
            people
                .filter((person) => !["self", "anonymous", "content_subject"].includes(person.kind) && person.displayName.trim() !== "" && !leftOutIds.has(person.id))
                .map((person) => ({ value: person.id, label: person.displayName }))
                .sort((a, b) => a.label.localeCompare(b.label, i18n.language)),
        [people, leftOutIds, i18n.language],
    );

    if (!item) return null;

    const handleRead = async (next: boolean) => {
        setPendingRead(next);
        try {
            await SetPermissionEnabled(MESSAGES_PERMISSION_KEY, next);
            if (next) onTurnedOn();
        } catch {
            failChange(t("observation.messages.toggleFailed"), () => void handleRead(next));
        } finally {
            await onChanged();
            setPendingRead(null);
            reload();
        }
    };

    const handleRefresh = async () => {
        setRefreshing(true);
        try {
            await SyncMessagesNow();
        } catch {
            failChange(t("observation.messages.refreshFailed"), () => void handleRefresh());
        } finally {
            setRefreshing(false);
            reload();
        }
    };

    const saveLeftOut = async (ids: string[]) => {
        setChanging(true);
        try {
            await SetPeopleLeftOut(ids);
        } catch {
            failChange(t("observation.messages.leftOut.changeFailed"), () => void saveLeftOut(ids));
        } finally {
            setChanging(false);
            reload();
        }
    };

    const forgetTitle = t("sees.forgetSource", { source: t("sees.messages") });
    const handleForget = async () => {
        setChanging(true);
        try {
            await ForgetMessages();
        } catch {
            failChange(t("observation.messages.forget.failed"), () => void handleForget());
        } finally {
            setChanging(false);
            reload();
        }
    };

    const lastSync = formatWhen(overview?.lastReadAt ?? "", i18n.language);
    // A switch that couldn't be read is never described as off.
    let readingDescription: string | undefined = item?.enabledUnknown ? undefined : t("observation.messages.readingOff");
    if (reading) {
        const parts: string[] = [];
        if (overview) {
            parts.push(t("observation.messages.asks", { count: overview.asks }));
            parts.push(t("observation.messages.commitments", { count: overview.commitments }));
        }
        if (lastSync) parts.push(t("observation.messages.lastSync", { when: lastSync }));
        readingDescription = parts.join(" \u00b7 ") || t("observation.messages.readingDescription");
    }
    const readingTitle = t("observation.messages.readingTitle");
    const kept = (overview?.asks ?? 0) + (overview?.commitments ?? 0);

    return (
        <SettingsCard
            title={t("observation.messages.title")}
            description={t("observation.messages.description")}
            headerRight={
                <Button
                    size="sm"
                    variant="outline"
                    onClick={() => void handleRefresh()}
                    disabled={!granted || !reading || refreshing || paused}
                >
                    <RefreshCw className="h-3.5 w-3.5" />
                    {t("observation.messages.refresh")}
                </Button>
            }
        >
            {loadFailed && <SettingsFailureLine failure={{ text: t("observation.messages.loadFailed"), retry: reload }} />}
            {paused && granted && <SettingsReason>{t("observation.pausedReason")}</SettingsReason>}
            {!granted && (
                <>
                    <SettingsReason>{t("observation.messages.needsAccess")}</SettingsReason>
                    <MissingPermissionRow item={item} onGrant={onGrant} />
                </>
            )}
            {granted && (
                <SettingsRow
                    title={readingTitle}
                    description={readingDescription}
                    control={
                        <KnownSwitch items={[item]} retry={() => void onChanged()}>
                        <Switch
                            aria-label={readingTitle}
                            checked={reading}
                            disabled={pendingRead !== null || paused}
                            onCheckedChange={(next) => void handleRead(next)}
                        />
                        </KnownSwitch>
                    }
                />
            )}
            {granted && (
                <SettingsRow
                    title={t("observation.messages.leftOut.title")}
                    description={t("observation.messages.leftOut.description")}
                    control={
                        <Combobox
                            options={options}
                            value=""
                            onValueChange={(id) => {
                                if (id) void saveLeftOut([...leftOut.map((person) => person.id), id]);
                            }}
                            placeholder={t("observation.messages.leftOut.add")}
                            searchPlaceholder={t("observation.messages.leftOut.search")}
                            emptyMessage={t("observation.messages.leftOut.empty")}
                            disabled={!overview || changing}
                            className="w-48"
                        />
                    }
                />
            )}
            {/* Contacts, Messages' own line: who "people you know" are, its
                state and what Messages does without it (answers v1.5.0 S15). */}
            {onOpenPeople && <ContactsPermissionRow onOpenPeople={onOpenPeople} />}
            {leftOut.map((person) => {
                const name = person.name || t("observation.messages.leftOut.unnamed");
                return (
                    <SettingsRow
                        key={person.id}
                        title={name}
                        control={
                            <Button
                                size="sm"
                                variant="outline"
                                aria-label={t("observation.messages.leftOut.removeLabel", { name })}
                                onClick={() => void saveLeftOut(leftOut.filter((other) => other.id !== person.id).map((other) => other.id))}
                                disabled={changing}
                            >
                                {t("observation.messages.leftOut.remove")}
                            </Button>
                        }
                    />
                );
            })}
            {/* computer-settings v1.0.0 rule 3 (kept by answers v1.5.0 T16):
                the details end with Forget everything from the source, which
                asks first with what it removes. Only while something is kept. */}
            {kept > 0 && (
                <>
                    <SettingsRow
                        title={forgetTitle}
                        description={t("observation.messages.forget.description")}
                        control={
                            <Button size="sm" variant="outline" onClick={() => setAskingForget(true)} disabled={changing}>
                                {t("sees.forget")}
                            </Button>
                        }
                    />
                    <ConfirmDialog
                        open={askingForget}
                        onOpenChange={setAskingForget}
                        title={forgetTitle}
                        description={t("observation.messages.forget.description")}
                        confirmLabel={t("sees.forget")}
                        variant="danger"
                        onConfirm={() => {
                            setAskingForget(false);
                            void handleForget();
                        }}
                    />
                </>
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
    only,
    paused,
    watchingOn,
    systemAudioSupported,
}: {
    /** Screen's details show the screen's row; Calls and meetings' the audio. */
    only: "screen" | "audio";
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
                    {only === "screen" && <SettingsRow
                        title={t("observation.capture.screen")}
                        description={screenReason}
                        control={<CaptureStateLabel state={screenState} />}
                    />}
                    {only === "audio" && <SettingsRow
                        title={t("observation.capture.microphone")}
                        description={
                            audio?.microphonePaused && !AUDIO_SUSPENSION_REASONS.has(suspendedReason)
                                ? t("observation.capture.reason.microphone_paused")
                                : (audioReason ?? undefined)
                        }
                        control={<CaptureStateLabel state={audioCaptureState(audio, "microphone")} />}
                    />}
                    {only === "audio" && systemAudioSupported && (
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

// ── The page: one row per source, each opening to its details ──

export type SourceId = "screen" | "calls" | "mail" | "messages" | "agents";

/** Deep-link anchors (openSettings("activity", { anchor })). Each opens its
 *  source's details; the row and the details carry the same anchor, so the
 *  scroll lands either way. */
const SOURCE_FOR_ANCHOR: Record<string, SourceId> = {
    "sees-screen": "screen",
    "sees-files": "screen",
    "sees-calls": "calls",
    "sees-mail": "mail",
    "sees-calendar": "mail",
    "sees-reminders": "mail",
    "sees-messages": "messages",
    "sees-agents": "agents",
    "sees-assistant": "agents",
};

export function sourceForAnchor(anchor: string | undefined): SourceId | null {
    return (anchor && SOURCE_FOR_ANCHOR[anchor]) || null;
}

/** One source: its name, the line saying what it is, a state hint when one
 *  applies, and one switch. The words open its details. */
function SourceRow({
    id,
    title,
    line,
    hint,
    warning = false,
    failure,
    below,
    control,
    onOpen,
}: {
    id: string;
    title: string;
    line: string;
    hint?: string;
    /** Under the words, outside the button that opens the details, for a
     *  line with its own action (Undo). */
    below?: ReactNode;
    /** The hint is in amber: on, but something it needs is missing. */
    warning?: boolean;
    failure?: SettingsFailure | null;
    control?: ReactNode;
    onOpen?: () => void;
}) {
    const { t } = useTranslation("settings");
    const words = (
        <>
            <span className="settings-row-title">{title}</span>
            <span className="settings-row-desc">{line}</span>
            {hint && (
                <span className={warning ? "settings-row-desc text-warning" : "settings-row-desc"} data-testid={`sees-hint-${id}`}>
                    {hint}
                </span>
            )}
        </>
    );
    return (
        <div className="settings-row" data-testid={`sees-row-${id}`} {...settingsAnchor(`sees-${id}`)}>
            <div className="flex flex-col min-w-0 flex-1">
                {onOpen ? (
                    <button
                        type="button"
                        className="flex flex-col min-w-0 text-left rounded-md focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring"
                        aria-label={t("sees.open", { source: title })}
                        onClick={onOpen}
                    >
                        {words}
                    </button>
                ) : (
                    words
                )}
                {below}
                <SettingsFailureLine failure={failure ?? null} />
            </div>
            <div className="shrink-0 flex items-center gap-2">
                {control}
                {onOpen && (
                    <button
                        type="button"
                        tabIndex={-1}
                        aria-hidden
                        className="flex h-6 w-6 items-center justify-center rounded-md text-muted hover:text-ink hover:bg-ink/[0.04] transition-colors"
                        onClick={onOpen}
                    >
                        <ChevronRight className="h-3.5 w-3.5" />
                    </button>
                )}
            </div>
        </div>
    );
}

/** A read of the permissions failed: never shown as off. */
function readFailure(t: TFunction<"settings">, retry: () => void): SettingsFailure {
    return { text: t("sees.readFailed"), retry };
}

/** A source switch whose saved state Go couldn't read (enabledUnknown) shows
 *  the read failure with Try again in its place, never on or off. */
function KnownSwitch({ items, retry, children }: {
    items: readonly (PermissionItem | null | undefined)[];
    retry: () => void;
    children: ReactNode;
}) {
    const { t } = useTranslation("settings");
    if (!items.some((item) => item?.enabledUnknown)) return <>{children}</>;
    return <SettingsFailureLine failure={readFailure(t, retry)} />;
}

/** Writes every item so they always agree; the page reads the permissions
 *  again either way. */
async function setItemsEnabled(items: PermissionItem[], next: boolean): Promise<void> {
    for (const item of items) {
        if (item.enabled !== next) await SetPermissionEnabled(item.key, next);
    }
}

/** A row whose switch is one or more permission items. */
function usePermissionSwitch(failedText: string, onChanged: () => Promise<unknown>) {
    const { failure, fail } = useSettingsFailure();
    const [pending, setPending] = useState<boolean | null>(null);
    const change = async (items: PermissionItem[], next: boolean, after?: () => void) => {
        setPending(next);
        try {
            await setItemsEnabled(items, next);
            after?.();
        } catch {
            fail(failedText, () => void change(items, next, after));
        } finally {
            await onChanged();
            setPending(null);
        }
    };
    return { pending, failure, change };
}

/** Screen's line names the browsers whose history is read on this computer
 *  (answers v1.5.0 T16), from the reader's own list (GetBrowsersRead). With
 *  none, it says only what's on screen. Until the names are read, or if the
 *  read fails, it keeps the interim line, which says more, never less. */
function useScreenLine(): string {
    const { t, i18n } = useTranslation("settings");
    const [browsers, setBrowsers] = useState<string[] | null>(null);
    useEffect(() => {
        let live = true;
        GetBrowsersRead()
            .then((names) => {
                if (live) setBrowsers(Array.isArray(names) ? names : null);
            })
            .catch(() => {
                if (live) setBrowsers(null);
            });
        return () => {
            live = false;
        };
    }, []);
    if (browsers === null) return t("sees.screenSubInterim");
    if (browsers.length === 0) return t("sees.screenSubNone");
    const list = new Intl.ListFormat(i18n.language, { style: "long", type: "conjunction" }).format(browsers);
    return t("sees.screenSub", { browsers: list });
}

function ScreenRow({ line, permissions, readFailed, paused, onChanged, onOpen }: {
    line: string;
    permissions: PlatformPermissions | null;
    readFailed: boolean;
    paused: boolean;
    onChanged: () => Promise<unknown>;
    onOpen: () => void;
}) {
    const { t } = useTranslation("settings");
    const items = screenPermissionItems(permissions);
    const sw = usePermissionSwitch(t("observation.watch.toggleFailed"), onChanged);
    if (permissions && items.length === 0) return null;
    const title = t("sees.screen");
    const watching = items.length > 0 && items.every((item) => item.enabled);
    const missing = items.filter((item) => item.actionable && !item.granted);
    return (
        <SourceRow
            id="screen"
            title={title}
            line={line}
            hint={permissions && missing.length > 0 ? t("sees.needsAccess") : undefined}
            failure={!permissions && readFailed ? readFailure(t, () => void onChanged()) : sw.failure}
            control={permissions && (
                <KnownSwitch items={items} retry={() => void onChanged()}>
                <Switch
                    aria-label={title}
                    checked={sw.pending ?? watching}
                    disabled={paused || missing.length > 0 || sw.pending !== null}
                    onCheckedChange={(next) => void sw.change(items, next)}
                />
                </KnownSwitch>
            )}
            onOpen={onOpen}
        />
    );
}

/** Whether speech recognition is on this computer, and if not, whether it
 *  waits for disk (computer-conformance answers v1.1.0 S9). The same plan
 *  setup's models line reads (#1443): models already here are left out, so
 *  an empty plan means the model is here. */
function useSpeechSpace(enabled: boolean) {
    const { i18n } = useTranslation("settings");
    const [plan, setPlan] = useState<ModelDownloadPlan | null>(null);
    const [failed, setFailed] = useState(false);
    const read = useCallback(() => {
        setFailed(false);
        GetModelChoices()
            .then((choices) => PlanSetupModelDownload(wireChoices(choices, i18n.language), QUIET_JOBS))
            .then((next) => setPlan(next))
            .catch(() => {
                setPlan(null);
                setFailed(true);
            });
    }, [i18n.language]);
    useEffect(() => {
        if (!enabled) return;
        read();
        window.addEventListener("focus", read);
        return () => window.removeEventListener("focus", read);
    }, [enabled, read]);
    const short = plan !== null && plan.items.length > 0 && !plan.enoughSpace;
    // Not here yet, short of disk or not: what's heard is kept until it is.
    const missing = plan !== null && plan.items.length > 0;
    return { plan: short ? plan : null, missing, failed, read };
}

/** Until the speech model is here and while there's no room for it: the
 *  amber need, then that calls and the watch's talk wait to be transcribed.
 *  Only while listening is on, since nothing is kept while it's off. */
function SpeechSpaceHint({ space }: { space: ReturnType<typeof useSpeechSpace> }) {
    const { t } = useTranslation("onboarding");
    const { t: ts } = useTranslation("settings");
    if (space.failed) {
        return <SettingsFailureLine failure={{ text: ts("sees.speechSpaceFailed"), retry: space.read }} />;
    }
    if (!space.plan) return null;
    return (
        <div data-testid="sees-speech-space">
            <span className="settings-row-desc block text-warning">
                {t("setup.diskShort", { size: formatModelBytes(space.plan.totalBytes), free: formatModelBytes(space.plan.freeBytes) })}
            </span>
            <span className="settings-row-desc block">{t("setup.diskKept")}</span>
        </div>
    );
}

function CallsRow({ listening, permissions, paused, onOpen }: {
    listening: ListeningState;
    permissions: PlatformPermissions | null;
    paused: boolean;
    onOpen: () => void;
}) {
    const { t } = useTranslation("settings");
    const title = t("sees.calls");
    const on = listening.enabled === true;
    const missing = on
        ? (permissions?.items ?? []).filter((item) => AUDIO_PERMISSION_KEYS.has(item.key) && item.actionable && !item.granted)
        : [];
    const space = useSpeechSpace(on && isWailsEnvironment());
    return (
        <SourceRow
            id="calls"
            title={title}
            line={t("sees.callsSub")}
            hint={missing.length > 0 ? t("sees.needsAccess") : undefined}
            below={on && isWailsEnvironment() ? <SpeechSpaceHint space={space} /> : undefined}
            failure={listening.loadFailed ? readFailure(t, listening.reload) : listening.changeFailure}
            control={listening.enabled !== null && (
                <Switch
                    aria-label={title}
                    checked={on}
                    disabled={listening.saving || paused}
                    onCheckedChange={(next) => void listening.setEnabled(next)}
                />
            )}
            onOpen={onOpen}
        />
    );
}

const MAIL_CALENDAR_REMINDERS_KEYS = [MAIL_PERMISSION_KEY, CALENDARS_PERMISSION_KEY, REMINDERS_PERMISSION_KEY];

/** One switch for the three: on while any of them reads, so it never says
 *  off while one still does. Each keeps its own switch in the details. */
function MailCalendarRemindersRow({ permissions, readFailed, paused, onChanged, onOpen }: {
    permissions: PlatformPermissions | null;
    readFailed: boolean;
    paused: boolean;
    onChanged: () => Promise<unknown>;
    onOpen: () => void;
}) {
    const { t } = useTranslation("settings");
    const items = (permissions?.items ?? []).filter((item) => MAIL_CALENDAR_REMINDERS_KEYS.includes(item.key));
    const sw = usePermissionSwitch(t("observation.mail.toggleFailed"), onChanged);
    if (permissions && items.length === 0) return null;
    const title = t("sees.mailCalRem");
    const reading = items.some((item) => item.enabled);
    const noneGranted = items.filter((item) => item.enabled).every((item) => !item.granted);
    return (
        <SourceRow
            id="mail"
            title={title}
            line={t("sees.mailCalRemSub")}
            hint={permissions && reading && noneGranted ? t("sees.needsAccess") : undefined}
            failure={!permissions && readFailed ? readFailure(t, () => void onChanged()) : sw.failure}
            control={permissions && (
                <KnownSwitch items={items} retry={() => void onChanged()}>
                <Switch
                    aria-label={title}
                    checked={sw.pending ?? reading}
                    disabled={paused || sw.pending !== null}
                    onCheckedChange={(next) => void sw.change(items, next)}
                />
                </KnownSwitch>
            )}
            onOpen={onOpen}
        />
    );
}

/** Messages (rule 4): off until turned on; turning it on without Full Disk
 *  Access asks once. The switch shows what is stored, and the hint what
 *  reading still needs. */
function MessagesRow({ permissions, readFailed, paused, onChanged, onTurnedOn, onOpen }: {
    permissions: PlatformPermissions | null;
    readFailed: boolean;
    paused: boolean;
    onChanged: () => Promise<unknown>;
    onTurnedOn: () => void;
    onOpen: () => void;
}) {
    const { t } = useTranslation("settings");
    const item = (permissions?.items ?? []).find((entry) => entry.key === MESSAGES_PERMISSION_KEY) ?? null;
    const sw = usePermissionSwitch(t("observation.messages.toggleFailed"), onChanged);
    if (permissions && !item) return null;
    const title = t("sees.messages");
    const on = sw.pending ?? item?.enabled ?? false;
    let hint: string | undefined;
    if (item && !item.granted) hint = on ? t("sees.needsFda") : t("sees.needsFdaOff");
    return (
        <SourceRow
            id="messages"
            title={title}
            line={t("sees.messagesSub")}
            hint={hint}
            warning={on && item !== null && !item.granted}
            failure={!permissions && readFailed ? readFailure(t, () => void onChanged()) : sw.failure}
            control={item && (
                <KnownSwitch items={[item]} retry={() => void onChanged()}>
                <Switch
                    aria-label={title}
                    checked={on}
                    disabled={paused || sw.pending !== null}
                    onCheckedChange={(next) => void sw.change([item], next, next ? onTurnedOn : undefined)}
                />
                </KnownSwitch>
            )}
            onOpen={onOpen}
        />
    );
}

/** Your watch: the phone's "Listen on your watch" (answers T1). Like the
 *  phone, the computer can turn it off but never on: the wearer starts
 *  listening on the watch. No details: the watch's other switches are in
 *  Your watch. */
function WatchRow() {
    const { t } = useTranslation("settings");
    const { failure, fail, clear } = useSettingsFailure();
    const [view, setView] = useState<WatchListeningView | null>(null);
    const [readFailed, setReadFailed] = useState(false);
    const [saving, setSaving] = useState(false);

    const read = useCallback(() => {
        setReadFailed(false);
        GetWatchListening()
            .then((next) => setView(next))
            .catch(() => {
                setView(null);
                setReadFailed(true);
            });
    }, []);
    useEffect(() => {
        read();
        window.addEventListener("focus", read);
        return () => window.removeEventListener("focus", read);
    }, [read]);

    const turnOff = async () => {
        clear();
        setSaving(true);
        try {
            setView(await TurnOffWatchListening());
        } catch {
            fail(t("sees.watchOffFailed"), () => void turnOff());
        } finally {
            setSaving(false);
        }
    };

    // While the watch listens and speech recognition isn't here yet, its talk
    // waits to be transcribed (answers v1.5.0 S14). Listen on the computer
    // doesn't decide it.
    const watchListening = view !== null && !view.off && view.hasWatch;
    const space = useSpeechSpace(watchListening && isWailsEnvironment());
    const title = t("sees.watch");
    let hint: string | undefined;
    let control: ReactNode = null;
    if (view && !view.off && !view.hasWatch) {
        hint = t("sees.noWatch");
    } else if (view) {
        if (view.off) hint = t("sees.watchOff");
        control = (
            <Switch
                aria-label={title}
                checked={!view.off}
                disabled={view.off || saving}
                onCheckedChange={(next) => {
                    if (!next) void turnOff();
                }}
            />
        );
    }
    return (
        <SourceRow
            id="watch"
            title={title}
            line={t("sees.watchSub")}
            hint={hint}
            below={watchListening && isWailsEnvironment() ? (
                space.failed ? (
                    <SettingsFailureLine failure={{ text: t("sees.speechSpaceFailed"), retry: space.read }} />
                ) : space.missing ? (
                    <span className="settings-row-desc" data-testid="sees-watch-speech-wait">{t("sees.watchSpeechWait")}</span>
                ) : undefined
            ) : undefined}
            failure={readFailed ? readFailure(t, read) : failure}
            control={control}
        />
    );
}

/** Your agents' history: for now the one switch "Read my Claude Code and
 *  Codex history", on unless turned off (the default is Bo's to set, and per
 *  agent is batch D's). */
function AgentsHistoryRow({ onOpen }: { onOpen: () => void }) {
    const { t } = useTranslation("settings");
    const history = useAssistantHistory();
    const title = t("sees.agentsHistory");
    return (
        <SourceRow
            id="agents"
            title={title}
            line={t("sees.agentsHistorySub")}
            below={<AssistantHistoryStopping history={history} />}
            failure={history.readFailed ? readFailure(t, history.read) : history.changeFailure}
            control={history.enabled !== null && !history.readFailed && (
                <Switch aria-label={title} checked={history.enabled} onCheckedChange={history.change} />
            )}
            onOpen={onOpen}
        />
    );
}

/** The one-time ask when Messages is turned on without Full Disk Access
 *  (computer-settings rule 4, text from data-leaves v1.1.0). */
function MessagesAsk({ open, onClose, onOpenSystem }: { open: boolean; onClose: () => void; onOpenSystem: () => void }) {
    const { t } = useTranslation("settings");
    const [answering, setAnswering] = useState<string | null>(null);
    useEffect(() => {
        if (!open) return;
        GetAssistantChoice()
            .then((view) => setAnswering(view?.answering ?? null))
            .catch(() => setAnswering(null));
    }, [open]);
    return (
        <ConfirmDialog
            open={open}
            onOpenChange={(next) => {
                if (!next) onClose();
            }}
            title={t("sees.messages")}
            description={t("sees.messagesAsk", { assistant: assistantName(answering, t) })}
            confirmLabel={t("sees.openSystem")}
            cancelLabel={t("sees.notNow")}
            onConfirm={onOpenSystem}
        />
    );
}

function AppListCard({ children }: { children: ReactNode }) {
    return (
        <div className="settings-card settings-card--padded">
            <div className="flex flex-col gap-5 pb-1">{children}</div>
        </div>
    );
}

/** What Isoform sees (page id "activity"): every source on one page, one row
 *  each (computer-settings v1.0.0 rules 2-4, answers v1.0.0 T2). A row opens
 *  its source's details in place, as Chat apps opens an app: the page keeps
 *  its title, and a back link returns to the rows. */
export function ObservationPage({
    anchor,
    anchorKey,
    onOpenPeople,
}: {
    onOpenPage?: OpenSettingsPage;
    /** Opens Who you know (Messages' Contacts row). */
    onOpenPeople?: () => void;
    /** The deep link's anchor: a source's anchor opens its details. */
    anchor?: string;
    /** Changes with every navigation, so the same link opens it again. */
    anchorKey?: unknown;
} = {}) {
    const { t } = useTranslation("settings");
    const { permissions, readFailed, refresh, requestPermission } = usePermissions();
    const { paused: closedEyes } = useClosedEyes();
    const listening = useListening();
    const screenLine = useScreenLine();
    const paused = closedEyes === true;
    const [open, setOpen] = useState<SourceId | null>(() => sourceForAnchor(anchor));
    const [asking, setAsking] = useState(false);

    useEffect(() => {
        setOpen(sourceForAnchor(anchor));
    }, [anchor, anchorKey]);

    const onGrant = (item: PermissionItem) => void requestPermission(item);
    const messagesItem = (permissions?.items ?? []).find((entry) => entry.key === MESSAGES_PERMISSION_KEY) ?? null;
    // The ask is for access: with Full Disk Access given, turning on just reads.
    const askForMessages = () => {
        if (messagesItem && !messagesItem.granted) setAsking(true);
    };
    const screenItems = screenPermissionItems(permissions);
    const watchingOn = permissions ? screenItems.length > 0 && screenItems.every((item) => item.enabled) : null;

    const closedEyesLine = paused && <SettingsReason>{t("sees.closedEyesOn")}</SettingsReason>;
    const ask = (
        <MessagesAsk
            open={asking}
            onClose={() => setAsking(false)}
            onOpenSystem={() => {
                setAsking(false);
                if (messagesItem) onGrant(messagesItem);
            }}
        />
    );

    if (open) {
        const titles: Record<SourceId, [string, string]> = {
            screen: [t("sees.screen"), screenLine],
            calls: [t("sees.calls"), t("sees.callsSub")],
            mail: [t("sees.mailCalRem"), t("sees.mailCalRemSub")],
            messages: [t("sees.messages"), t("sees.messagesSub")],
            agents: [t("sees.agentsHistory"), t("sees.agentsHistorySub")],
        };
        const [title, line] = titles[open];
        return (
            <div className="flex flex-col gap-4" data-testid={`sees-details-${open}`} {...settingsAnchor(`sees-${open}`)}>
                <button
                    type="button"
                    onClick={() => setOpen(null)}
                    className="self-start inline-flex items-center gap-1 text-xs text-muted hover:text-ink transition-colors"
                >
                    <ChevronLeft className="h-3.5 w-3.5" />
                    {t("sees.back")}
                </button>
                <SettingsSectionHeader title={title} description={line} />
                {closedEyesLine}
                {open === "screen" && (
                    <ScreenDetails permissions={permissions} paused={paused} watchingOn={watchingOn} systemAudioSupported={listening.systemAudioSupported} onGrant={onGrant} onChanged={refresh} />
                )}
                {open === "calls" && (
                    <CallsDetails listening={listening} permissions={permissions} paused={paused} watchingOn={watchingOn} onGrant={onGrant} onChanged={refresh} />
                )}
                {open === "mail" && (
                    <>
                        <MailCard permissions={permissions} paused={paused} onGrant={onGrant} onChanged={refresh} />
                        <div {...settingsAnchor("sees-calendar")}>
                            <CalendarsCard permissions={permissions} paused={paused} onGrant={onGrant} onChanged={refresh} />
                        </div>
                        <div {...settingsAnchor("sees-reminders")}>
                            <RemindersCard permissions={permissions} paused={paused} onGrant={onGrant} onChanged={refresh} />
                        </div>
                    </>
                )}
                {open === "messages" && (
                    <MessagesCard permissions={permissions} paused={paused} onGrant={onGrant} onChanged={refresh} onTurnedOn={askForMessages} onOpenPeople={onOpenPeople} />
                )}
                {open === "agents" && <AssistantHistoryCard />}
                {ask}
            </div>
        );
    }

    return (
        <>
            {closedEyesLine}
            {/* computer-settings v1.0.0 rule 2's order, with Reminders in
                Mail and calendar and Files in Screen (answers v1.0.0 T2). */}
            <div className="settings-card settings-card--padded" data-testid="sees-rows">
                <ScreenRow line={screenLine} permissions={permissions} readFailed={readFailed} paused={paused} onChanged={refresh} onOpen={() => setOpen("screen")} />
                <CallsRow listening={listening} permissions={permissions} paused={paused} onOpen={() => setOpen("calls")} />
                <MailCalendarRemindersRow permissions={permissions} readFailed={readFailed} paused={paused} onChanged={refresh} onOpen={() => setOpen("mail")} />
                <MessagesRow permissions={permissions} readFailed={readFailed} paused={paused} onChanged={refresh} onTurnedOn={askForMessages} onOpen={() => setOpen("messages")} />
                <WatchRow />
                <AgentsHistoryRow onOpen={() => setOpen("agents")} />
            </div>
            {ask}
        </>
    );
}

/** WeChat pictures (answers v1.4.0 S18, S13): its own switch, apart from the
 *  Activity timeline's. Pictures wait for the privacy model; text doesn't. */
function WeChatPicturesCard() {
    const { t } = useTranslation("settings");
    const { failure, fail, clear } = useSettingsFailure();
    const [view, setView] = useState<ActivityVisionView | null>(null);
    const [readFailed, setReadFailed] = useState(false);
    const [saving, setSaving] = useState(false);
    const [answering, setAnswering] = useState<string | null>(null);

    const read = useCallback(() => {
        setReadFailed(false);
        GetActivityVision()
            .then((next) => setView(next))
            .catch(() => {
                setView(null);
                setReadFailed(true);
            });
    }, []);
    useEffect(() => {
        read();
        GetAssistantChoice()
            .then((choice) => setAnswering(choice?.answering ?? null))
            .catch(() => setAnswering(null));
        window.addEventListener("focus", read);
        return () => window.removeEventListener("focus", read);
    }, [read]);

    const change = async (next: boolean) => {
        clear();
        setSaving(true);
        try {
            setView(await SetActivityVisionEnabled(next));
        } catch {
            fail(t("sees.wechatPicFailed"), () => void change(next));
        } finally {
            setSaving(false);
        }
    };

    const words = t("sees.wechatPic", { assistant: assistantName(answering, t) });
    return (
        <div className="settings-card settings-card--padded" data-testid="sees-wechat-pictures">
            <div className="settings-row">
                <div className="flex flex-col min-w-0">
                    <span className="settings-row-title">{words}</span>
                    {view && !view.privacyModelReady && (
                        <span className="settings-row-desc">{t("sees.wechatPicWaiting")}</span>
                    )}
                    <SettingsFailureLine failure={readFailed ? readFailure(t, read) : failure} />
                </div>
                <div className="shrink-0 flex items-center gap-2">
                    {view && (
                        <Switch
                            aria-label={words}
                            checked={view.enabled}
                            disabled={saving}
                            onCheckedChange={(next) => void change(next)}
                        />
                    )}
                </div>
            </div>
        </div>
    );
}

function ScreenDetails({ permissions, paused, watchingOn, systemAudioSupported, onGrant, onChanged }: {
    permissions: PlatformPermissions | null;
    paused: boolean;
    watchingOn: boolean | null;
    systemAudioSupported: boolean;
    onGrant: (item: PermissionItem) => void;
    onChanged: () => Promise<unknown>;
}) {
    const systemApps = useSystemApps();
    return (
        <>
            <WatchScreenCard permissions={permissions} paused={paused} onGrant={onGrant} onChanged={onChanged} />
            <AppListCard>
                <HiddenAppsSection systemApps={systemApps} />
            </AppListCard>
            <div {...settingsAnchor("sees-files")}>
                <FilesCard permissions={permissions} paused={paused} onChanged={onChanged} />
            </div>
            <div {...settingsAnchor("sees-wechat")}>
                <WeChatPicturesCard />
            </div>
            <CaptureStatusCard only="screen" paused={paused} watchingOn={watchingOn} systemAudioSupported={systemAudioSupported} />
        </>
    );
}

function CallsDetails({ listening, permissions, paused, watchingOn, onGrant, onChanged }: {
    listening: ListeningState;
    permissions: PlatformPermissions | null;
    paused: boolean;
    watchingOn: boolean | null;
    onGrant: (item: PermissionItem) => void;
    onChanged: () => Promise<unknown>;
}) {
    const systemApps = useSystemApps();
    return (
        <>
            <ListenCard
                listening={listening}
                paused={paused}
                permissions={permissions}
                onGrant={onGrant}
                onChanged={onChanged}
                showMeetings={isWailsEnvironment()}
            />
            <AppListCard>
                <NotListenedAppsSection systemApps={systemApps} listeningOff={listening.enabled === false} hideOpenLink />
            </AppListCard>
            <CaptureStatusCard only="audio" paused={paused} watchingOn={watchingOn} systemAudioSupported={listening.systemAudioSupported} />
        </>
    );
}
