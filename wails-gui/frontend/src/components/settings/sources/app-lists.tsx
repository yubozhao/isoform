import { useEffect, useMemo, useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { Plus, X } from "lucide-react";
import { Button } from "@/components/ui/button";
import { ConfirmDialog } from "@/components/confirm-dialog";
import { SettingsFailureLine, SettingsPlaceholderRows, useSettingsFailure } from "@/components/settings/settings-failure";
import { invalidateActivitySessionCache } from "@/components/timeline/activity-session-cache";
import {
    AddSensitiveApp,
    AddVoiceInputApp,
    GetSensitiveApps,
    GetVoiceInputApps,
    ListSystemApps,
    RemoveSensitiveApp,
    RemoveVoiceInputApp,
} from "../../../../wailsjs/go/main/App";
import { SettingsReason, openSettingsPage, type OpenSettingsPage } from "@/components/settings/pages/capture-shared";

// The per-app lists: the apps Isoform never sees (blurred in screen
// recordings) and the apps it doesn't listen to. Shown in What Isoform sees'
// Screen and Calls and meetings details, and on Privacy until that page goes.
// Taking an app off either list asks first, because Isoform then records or
// transcribes it again.

export type SystemAppInfo = { name: string; englishName?: string; bundleId: string; icon: string };

/** The installed apps the pickers offer. An unreadable list leaves the
 *  pickers empty; the lists themselves still load. */
export function useSystemApps(): SystemAppInfo[] {
    const [systemApps, setSystemApps] = useState<SystemAppInfo[]>([]);
    useEffect(() => {
        ListSystemApps()
            .then((list) => setSystemApps(list ?? []))
            .catch(() => {});
    }, []);
    return systemApps;
}

function AppIcon({ src }: { src?: string }) {
    return src ? (
        <img src={src} alt="" className="h-6 w-6 shrink-0 rounded" />
    ) : (
        <div className="h-6 w-6 shrink-0 rounded bg-ink/[0.06]" />
    );
}

function AppListRow({ icon, name, muted, trailing }: { icon?: string; name: string; muted?: boolean; trailing: ReactNode }) {
    return (
        <div className={`flex items-center justify-between py-2.5 gap-3${muted ? " opacity-60" : ""}`}>
            <div className="flex items-center gap-2.5 min-w-0">
                <AppIcon src={icon} />
                <span className="settings-row-title truncate">{name}</span>
            </div>
            {trailing}
        </div>
    );
}

/** An app waiting for the user to confirm its removal from one of the lists. */
interface PendingRemoval {
    id: string;
    name: string;
}

/** Asks before an app leaves an ignore list: removing it lets Isoform record
 *  or transcribe that app again. */
function ConfirmRemoveAppDialog({
    pending,
    onClose,
    onConfirm,
    copy,
}: {
    pending: PendingRemoval | null;
    onClose: () => void;
    onConfirm: (id: string, name: string) => void;
    copy: { title: string; description: string; confirm: string };
}) {
    return (
        <ConfirmDialog
            open={pending !== null}
            onOpenChange={(open) => {
                if (!open) onClose();
            }}
            title={copy.title}
            description={copy.description}
            confirmLabel={copy.confirm}
            variant="danger"
            onConfirm={() => {
                if (pending) onConfirm(pending.id, pending.name);
            }}
        />
    );
}

function RemoveAppButton({ onClick }: { onClick: () => void }) {
    const { t } = useTranslation("settings");
    const label = t("privacy.ignoredApps.remove");
    return (
        <button
            type="button"
            onClick={onClick}
            className="flex items-center justify-center h-6 w-6 rounded-md text-muted hover:text-ink hover:bg-ink/[0.04] transition-colors shrink-0"
            title={label}
            aria-label={label}
        >
            <X className="h-3.5 w-3.5" />
        </button>
    );
}

function matchesSearch(app: SystemAppInfo, search: string): boolean {
    if (!search) return true;
    const q = search.toLowerCase();
    return (
        app.name.toLowerCase().includes(q) ||
        (app.englishName ?? "").toLowerCase().includes(q) ||
        app.bundleId.toLowerCase().includes(q)
    );
}

/** "Add app" button that turns into a searchable list of installed apps. */
function AppPicker({ candidates, onPick }: { candidates: SystemAppInfo[]; onPick: (app: SystemAppInfo) => void }) {
    const { t } = useTranslation("settings");
    const [open, setOpen] = useState(false);
    const [search, setSearch] = useState("");
    const matches = useMemo(() => candidates.filter((app) => matchesSearch(app, search)), [candidates, search]);

    if (!open) {
        return (
            <Button
                size="sm"
                variant="outline"
                className="self-start"
                onClick={() => {
                    setSearch("");
                    setOpen(true);
                }}
            >
                <Plus className="h-3.5 w-3.5" />
                {t("privacy.ignoredApps.addApp")}
            </Button>
        );
    }

    return (
        <div className="flex flex-col gap-2 pt-1">
            <input
                type="text"
                value={search}
                onChange={(e) => setSearch(e.target.value)}
                placeholder={t("privacy.ignoredApps.searchPlaceholder")}
                aria-label={t("privacy.ignoredApps.searchPlaceholder")}
                autoFocus
                className="h-8 rounded-md border border-border bg-background px-2.5 text-xs outline-none focus:ring-1 focus:ring-ring"
            />
            <div className="flex flex-col max-h-48 overflow-y-auto divide-y divide-border/30 pr-2.5 [scrollbar-gutter:stable]">
                {matches.map((app) => (
                    <button
                        key={app.bundleId}
                        type="button"
                        onClick={() => onPick(app)}
                        className="flex items-center gap-2.5 py-2 px-1 text-left hover:bg-ink/[0.04] rounded transition-colors w-full"
                    >
                        <AppIcon src={app.icon} />
                        <span className="settings-row-title truncate flex-1">{app.name}</span>
                        <Plus className="h-3.5 w-3.5 text-muted shrink-0" />
                    </button>
                ))}
                {matches.length === 0 && (
                    <span className="text-xs text-muted py-2">{t("privacy.ignoredApps.noMatches")}</span>
                )}
            </div>
            <div className="flex justify-end pt-1">
                <Button size="sm" onClick={() => setOpen(false)}>
                    {t("privacy.ignoredApps.done")}
                </Button>
            </div>
        </div>
    );
}

function AppListHeading({ title, description }: { title: string; description: string }) {
    return (
        <div className="flex flex-col">
            <h3 className="settings-row-title">{title}</h3>
            <p className="settings-row-desc">{description}</p>
        </div>
    );
}

// Display names for the built-in password-manager prefixes when the app is
// not found among installed apps.
const DEFAULT_APP_LABELS: Record<string, string> = {
    "com.1password": "1Password",
    "com.agilebits": "1Password",
    "com.bitwarden": "Bitwarden",
    "com.lastpass": "LastPass",
    "com.dashlane": "Dashlane",
    "org.keepassxc": "KeePassXC",
    "com.enpass": "Enpass",
    "com.nordpass": "NordPass",
    "com.roboform": "RoboForm",
    "de.marcels-it-blog.KeePass": "KeePass",
    "com.apple.keychainaccess": "Keychain Access",
    "com.apple.Passwords": "Passwords",
};

interface SensitiveAppEntry {
    bundlePrefix: string;
    category: string;
    isDefault: boolean;
    icon?: string;
}

/** Apps whose windows are blurred in screen recordings. Password managers are
 *  always hidden; the user adds the rest. */
export function HiddenAppsSection({ systemApps }: { systemApps: SystemAppInfo[] }) {
    const { t } = useTranslation("settings");
    const { failure: changeFailure, fail: failChange } = useSettingsFailure();
    const [apps, setApps] = useState<SensitiveAppEntry[]>([]);
    const [loading, setLoading] = useState(true);
    const [loadFailed, setLoadFailed] = useState(false);
    const [readCount, setReadCount] = useState(0);
    const [pendingRemoval, setPendingRemoval] = useState<PendingRemoval | null>(null);

    useEffect(() => {
        setLoading(true);
        setLoadFailed(false);
        GetSensitiveApps()
            .then((list: SensitiveAppEntry[]) => setApps(list ?? []))
            .catch(() => setLoadFailed(true))
            .finally(() => setLoading(false));
    }, [readCount]);

    const handleAdd = async (app: SystemAppInfo) => {
        try {
            await AddSensitiveApp(app.bundleId, app.name);
            setApps((prev) => {
                const next: SensitiveAppEntry[] = [
                    ...prev.filter((a) => a.isDefault || a.bundlePrefix !== app.bundleId),
                    { bundlePrefix: app.bundleId, category: app.name, isDefault: false, icon: app.icon },
                ];
                next.sort((a, b) => a.bundlePrefix.localeCompare(b.bundlePrefix));
                return next;
            });
        } catch {
            failChange(t("sensitiveApps.toasts.addFailed", { name: app.name }), () => void handleAdd(app));
        } finally {
            invalidateActivitySessionCache(true);
        }
    };

    const handleRemove = (bundlePrefix: string, name = bundlePrefix) => {
        RemoveSensitiveApp(bundlePrefix)
            .then(() => {
                setApps((prev) => prev.filter((a) => a.isDefault || a.bundlePrefix !== bundlePrefix));
            })
            .catch(() => failChange(t("sensitiveApps.toasts.removeFailed", { name }), () => handleRemove(bundlePrefix, name)))
            .finally(() => invalidateActivitySessionCache(true));
    };

    const userApps = useMemo(() => apps.filter((a) => !a.isDefault), [apps]);
    const systemAppByBundleId = useMemo(
        () =>
            systemApps.reduce<Record<string, SystemAppInfo>>((acc, app) => {
                acc[app.bundleId.toLowerCase()] = app;
                return acc;
            }, {}),
        [systemApps],
    );
    // Only the password managers that are actually installed, once each.
    const installedDefaults = useMemo(() => {
        const seen = new Set<string>();
        return apps
            .filter((a) => a.isDefault)
            .filter((app) => {
                const lower = app.bundlePrefix.toLowerCase();
                const installed = systemApps.find((sa) => sa.bundleId.toLowerCase().startsWith(lower));
                if (!installed || seen.has(installed.name)) return false;
                seen.add(installed.name);
                return true;
            });
    }, [apps, systemApps]);
    const candidates = useMemo(() => {
        const prefixes = apps.map((a) => a.bundlePrefix.toLowerCase());
        return systemApps.filter((sa) => {
            const bid = sa.bundleId.toLowerCase();
            return !prefixes.some((p) => bid.startsWith(p) || p === bid);
        });
    }, [apps, systemApps]);

    const renderRow = (app: SensitiveAppEntry) => {
        const lower = app.bundlePrefix.toLowerCase();
        const installed = app.isDefault
            ? systemApps.find((sa) => sa.bundleId.toLowerCase().startsWith(lower))
            : systemAppByBundleId[lower];
        const name =
            installed?.name ?? (app.isDefault ? DEFAULT_APP_LABELS[app.bundlePrefix] : undefined) ?? app.category;
        return (
            <AppListRow
                key={`${app.isDefault ? "default" : "user"}-${app.bundlePrefix}`}
                icon={installed?.icon ?? app.icon}
                name={name}
                muted={app.isDefault}
                trailing={
                    app.isDefault ? (
                        <span className="text-[11px] text-muted shrink-0">{t("privacy.ignoredApps.alwaysHidden")}</span>
                    ) : (
                        <RemoveAppButton onClick={() => setPendingRemoval({ id: app.bundlePrefix, name })} />
                    )
                }
            />
        );
    };

    return (
        <section className="flex flex-col gap-2">
            <AppListHeading
                title={t("privacy.ignoredApps.hiddenTitle")}
                description={t("privacy.ignoredApps.hiddenDescription")}
            />
            {loadFailed ? (
                <SettingsFailureLine failure={{ text: t("sensitiveApps.toasts.loadFailed"), retry: () => setReadCount((n) => n + 1) }} />
            ) : loading ? (
                <SettingsPlaceholderRows />
            ) : (
                (installedDefaults.length > 0 || userApps.length > 0) && (
                    <div className="flex flex-col divide-y divide-border/50">
                        {installedDefaults.map(renderRow)}
                        {userApps.map(renderRow)}
                    </div>
                )
            )}
            <AppPicker candidates={candidates} onPick={(app) => void handleAdd(app)} />
            <ConfirmRemoveAppDialog
                pending={pendingRemoval}
                onClose={() => setPendingRemoval(null)}
                onConfirm={handleRemove}
                copy={{
                    title: t("privacy.ignoredApps.confirmUnhide.title", { name: pendingRemoval?.name ?? "" }),
                    description: t("privacy.ignoredApps.confirmUnhide.description"),
                    confirm: t("privacy.ignoredApps.confirmUnhide.confirm"),
                }}
            />
            <SettingsFailureLine failure={changeFailure} />
        </section>
    );
}

interface VoiceInputAppEntry {
    bundleId: string;
    appName: string;
    isDefault: boolean;
}

/** Apps Isoform does not listen to while they are in front, such as dictation
 *  tools. Only meaningful while listening is on, so it gives way to a reason
 *  and a link to Observation while listening is off. */
export function NotListenedAppsSection({
    systemApps,
    listeningOff,
    onOpenPage,
    hideOpenLink = false,
}: {
    systemApps: SystemAppInfo[];
    listeningOff: boolean;
    onOpenPage?: OpenSettingsPage;
    /** True inside Calls and meetings' details, where listening's switch is
     *  the row above: no link back to the page it is already on. */
    hideOpenLink?: boolean;
}) {
    const { t } = useTranslation("settings");
    const { failure: changeFailure, fail: failChange } = useSettingsFailure();
    const [apps, setApps] = useState<VoiceInputAppEntry[]>([]);
    const [loading, setLoading] = useState(true);
    const [loadFailed, setLoadFailed] = useState(false);
    const [readCount, setReadCount] = useState(0);
    const [pendingRemoval, setPendingRemoval] = useState<PendingRemoval | null>(null);

    useEffect(() => {
        setLoading(true);
        setLoadFailed(false);
        GetVoiceInputApps()
            .then((list: VoiceInputAppEntry[]) => setApps(list ?? []))
            .catch(() => setLoadFailed(true))
            .finally(() => setLoading(false));
    }, [readCount]);

    const handleAdd = async (app: SystemAppInfo) => {
        try {
            await AddVoiceInputApp(app.bundleId, app.name);
            setApps((prev) => {
                const next: VoiceInputAppEntry[] = [
                    ...prev.filter((a) => a.bundleId !== app.bundleId),
                    { bundleId: app.bundleId, appName: app.name, isDefault: false },
                ];
                next.sort((a, b) => a.appName.localeCompare(b.appName));
                return next;
            });
        } catch {
            failChange(t("voiceInputApps.toasts.addFailed", { name: app.name }), () => void handleAdd(app));
        }
    };

    const handleRemove = (bundleId: string, name = bundleId) => {
        RemoveVoiceInputApp(bundleId)
            .then(() => {
                setApps((prev) => prev.filter((a) => a.bundleId !== bundleId));
            })
            .catch(() => failChange(t("voiceInputApps.toasts.removeFailed", { name }), () => handleRemove(bundleId, name)));
    };

    const userApps = useMemo(() => apps.filter((a) => !a.isDefault), [apps]);
    const systemAppByBundleId = useMemo(
        () =>
            systemApps.reduce<Record<string, SystemAppInfo>>((acc, app) => {
                acc[app.bundleId.toLowerCase()] = app;
                return acc;
            }, {}),
        [systemApps],
    );
    const candidates = useMemo(() => {
        const listed = new Set(apps.map((a) => a.bundleId.toLowerCase()));
        return systemApps.filter((sa) => !listed.has(sa.bundleId.toLowerCase()));
    }, [apps, systemApps]);

    return (
        <section className="flex flex-col gap-2">
            <AppListHeading
                title={t("privacy.ignoredApps.notListenedTitle")}
                description={t("privacy.ignoredApps.notListenedDescription")}
            />
            {listeningOff ? (
                <div className="flex flex-col gap-2">
                    <SettingsReason>{t("privacy.ignoredApps.notListenedOff")}</SettingsReason>
                    {!hideOpenLink && <Button
                        size="sm"
                        variant="outline"
                        className="self-start"
                        onClick={() => openSettingsPage("activity", onOpenPage)}
                    >
                        {t("privacy.ignoredApps.openObservation")}
                    </Button>}
                </div>
            ) : (
                <>
                    {loadFailed ? (
                        <SettingsFailureLine failure={{ text: t("voiceInputApps.toasts.loadFailed"), retry: () => setReadCount((n) => n + 1) }} />
                    ) : loading ? (
                        <SettingsPlaceholderRows />
                    ) : (
                        userApps.length > 0 && (
                            <div className="flex flex-col divide-y divide-border/50">
                                {userApps.map((app) => {
                                    const installed = systemAppByBundleId[app.bundleId.toLowerCase()];
                                    const name = installed?.name ?? app.appName;
                                    return (
                                        <AppListRow
                                            key={app.bundleId}
                                            icon={installed?.icon}
                                            name={name}
                                            trailing={
                                                <RemoveAppButton
                                                    onClick={() => setPendingRemoval({ id: app.bundleId, name })}
                                                />
                                            }
                                        />
                                    );
                                })}
                            </div>
                        )
                    )}
                    <AppPicker candidates={candidates} onPick={(app) => void handleAdd(app)} />
                </>
            )}
            <ConfirmRemoveAppDialog
                pending={pendingRemoval}
                onClose={() => setPendingRemoval(null)}
                onConfirm={handleRemove}
                copy={{
                    title: t("privacy.ignoredApps.confirmListen.title", { name: pendingRemoval?.name ?? "" }),
                    description: t("privacy.ignoredApps.confirmListen.description"),
                    confirm: t("privacy.ignoredApps.confirmListen.confirm"),
                }}
            />
            <SettingsFailureLine failure={changeFailure} />
        </section>
    );
}
