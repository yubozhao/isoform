import { SettingsFailureLine, SettingsPlaceholderRows, useSettingsFailure } from "@/components/settings/settings-failure";
import { useCallback, useEffect, useRef, useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import type { TFunction } from "i18next";
import { Button } from "@/components/ui/button";
import {
    Select,
    SelectContent,
    SelectItem,
    SelectTrigger,
    SelectValue,
} from "@/components/ui/select";
import { ConfirmDialog } from "@/components/confirm-dialog";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import {
    ConfirmActivityStorageLimit,
    GetActivityStorageBudget,
    GetActivityStorageUsage,
    GetScreenshotRetentionDays,
    SetScreenshotRetentionDays,
    type ActivityStorageUsage,
    type StorageBudgetStatus,
} from "@/lib/wails-shim-app";
import { SettingsCard, SettingsRow } from "@/components/settings/settings-primitives";
import { formatBytes } from "@/components/settings/settings-format";
import { SettingsControlCard, SettingsReason } from "./pages/capture-shared";
import {
    GIB,
    RETENTION_DAYS,
    STORAGE_CAP_CUSTOM,
    STORAGE_CAP_MAX,
    STORAGE_CAP_MIN,
    STORAGE_CAP_PRESETS_GB,
    formatEarliest,
    retentionLabel,
    shortensRetention,
    useRecordingStorage,
    type RecordingStorage,
} from "./pages/recording-storage";

// ── Your data's Change sheet ──
//
// The keep times and the cap (computer-data v1.0.0 rules 2-3, answers v1.0.0
// T2): how long recordings and screenshots are kept, the storage limit, and
// what each kind of data takes against it. Your data's Change buttons open
// it. These cards came from Settings › Storage and the recording keep time
// from Settings › Privacy (settings batch D2), with the same controls and
// saved values; "clean up now" went.

function formatDay(ms: number): string {
    if (!ms || ms <= 0) return "";
    const d = new Date(ms);
    return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;
}

/** A file count with thousands separators ("35,747"). */
function formatCount(n: number): string {
    return n.toLocaleString();
}

/** Screenshot time limit value -1: no screenshot limit chosen, so images go
 *  with their recordings at the recording time limit. */
const SCREENSHOTS_WITH_RECORDINGS = -1;

/** Screenshot time limit label: -1 follows the recording time limit. */
function screenshotDaysLabel(t: TFunction<"settings">, days: number, recordingDays: number): string {
    if (days === SCREENSHOTS_WITH_RECORDINGS) {
        return t("storage.screenshots.withRecordings", { period: retentionLabel(t, recordingDays) });
    }
    return retentionLabel(t, days);
}

/** The screenshot times worth offering: only ones shorter than the recording
 *  time limit, since the whole recording, its text included, goes at that
 *  limit first. */
function screenshotDayOptions(recordingDays: number): number[] {
    return [
        SCREENSHOTS_WITH_RECORDINGS,
        ...RETENTION_DAYS.filter((d) => d > 0 && (recordingDays === 0 || d < recordingDays)),
    ];
}

// ── Keep recordings for ──

function RecordingRetentionCard({ storage }: { storage: RecordingStorage }) {
    const { t } = useTranslation("settings");
    const [confirmDays, setConfirmDays] = useState<number | null>(null);
    const title = t("privacy.retention.title");
    const { stats } = storage;

    const handleSelect = (value: string) => {
        const next = Number(value);
        if (!Number.isFinite(next) || next === storage.days) return;
        // While usage is unknown (still loading, or it failed) a shorter
        // time limit asks first too.
        if (shortensRetention(storage.days, next) && (stats === null || stats.retainedSessionCount > 0)) {
            setConfirmDays(next);
            return;
        }
        storage.saveDays(next);
    };

    // Clamp to 100% so data above a newly lowered limit doesn't read as
    // "120% used" before the next rotation pass catches up.
    const capBytes = storage.maxGB * GIB;
    const usageRatio = stats && capBytes > 0 ? Math.min(1, stats.retainedBytes / capBytes) : 0;
    const earliest = stats ? formatEarliest(stats.earliestRetainedMs, t) : "";

    let summary: ReactNode = null;
    if (stats && stats.retainedSessionCount > 0) {
        summary = (
            <div className="flex flex-col gap-2 pt-1">
                <SettingsReason>
                    {earliest
                        ? t("privacy.retention.summaryWithOldest", {
                              used: formatBytes(stats.retainedBytes),
                              maxGB: storage.maxGB,
                              count: stats.retainedSessionCount,
                              oldest: earliest,
                          })
                        : t("privacy.retention.summary", {
                              used: formatBytes(stats.retainedBytes),
                              maxGB: storage.maxGB,
                              count: stats.retainedSessionCount,
                          })}
                </SettingsReason>
                {/* Subtle by design: the numbers are the source of truth, the
                    bar is a quick "am I close to the limit?" read. */}
                <div className="h-1 w-full rounded-full bg-ink/[0.06] overflow-hidden">
                    <div
                        className="h-full bg-accent-2/70 transition-[width] duration-300"
                        style={{ width: `${Math.round(usageRatio * 100)}%` }}
                    />
                </div>
            </div>
        );
    } else if (stats) {
        summary = <SettingsReason>{t("privacy.retention.empty")}</SettingsReason>;
    }

    const confirmPeriod = confirmDays === null ? "" : retentionLabel(t, confirmDays);

    return (
        <>
            <SettingsControlCard
                title={title}
                description={t("privacy.retention.description")}
                control={
                    storage.loading ? null : (
                        <Select
                            value={String(storage.days)}
                            onValueChange={handleSelect}
                            disabled={storage.saving}
                        >
                            <SelectTrigger aria-label={title} className="w-[140px] h-8 text-xs">
                                <SelectValue />
                            </SelectTrigger>
                            <SelectContent>
                                {RETENTION_DAYS.map((d) => (
                                    <SelectItem key={d} value={String(d)}>
                                        {retentionLabel(t, d)}
                                    </SelectItem>
                                ))}
                            </SelectContent>
                        </Select>
                    )
                }
            >
                {summary}
                <SettingsFailureLine failure={storage.failure} />
            </SettingsControlCard>
            <ConfirmDialog
                open={confirmDays !== null}
                onOpenChange={(open) => {
                    if (!open) setConfirmDays(null);
                }}
                title={t("privacy.retention.confirmTitle", { period: confirmPeriod })}
                description={t("privacy.retention.confirmDescription", { period: confirmPeriod })}
                confirmLabel={t("privacy.retention.confirmAction")}
                variant="danger"
                onConfirm={() => {
                    if (confirmDays !== null) storage.saveDays(confirmDays);
                }}
            />
        </>
    );
}

// ── Space used ──

/** Over the limit on a profile the old count let grow (Isoform waits for a
 *  yes), or over it with nothing left the limit may remove. */
function BudgetNotice({ budget, onConfirmed }: { budget: StorageBudgetStatus; onConfirmed: () => void }) {
    const { t } = useTranslation("settings");
    const [confirming, setConfirming] = useState(false);
    const { failure, fail, clear } = useSettingsFailure();
    if (budget.state === "unreachable") {
        return <SettingsReason>{t("storage.budget.unreachable", { kept: formatBytes(budget.keptBytes) })}</SettingsReason>;
    }
    if (budget.state !== "confirm") return null;
    const count = budget.screenshots.count + budget.recordings.count;
    const bytes = budget.screenshots.bytes + budget.recordings.bytes;
    const oldest = Math.min(...[budget.screenshots.oldestMs, budget.recordings.oldestMs].filter((ms) => ms > 0));
    const newest = Math.max(budget.screenshots.newestMs, budget.recordings.newestMs);
    const confirm = () => {
        clear();
        ConfirmActivityStorageLimit()
            .then(onConfirmed)
            .catch(() => fail(t("storage.budget.confirmFailed"), confirm));
    };
    return (
        <div className="flex flex-col gap-2">
            <SettingsReason>
                {t("storage.budget.confirmText", {
                    count,
                    n: formatCount(count),
                    size: formatBytes(bytes),
                    from: formatDay(Number.isFinite(oldest) ? oldest : 0),
                    to: formatDay(newest),
                })}
            </SettingsReason>
            <div className="flex justify-end">
                <Button size="sm" variant="outline" onClick={() => setConfirming(true)}>
                    {t("storage.budget.confirmButton")}
                </Button>
            </div>
            <SettingsFailureLine failure={failure} />
            <ConfirmDialog
                open={confirming}
                onOpenChange={setConfirming}
                title={t("storage.budget.confirmDialogTitle", { size: formatBytes(bytes) })}
                description={t("storage.budget.confirmDialogDescription")}
                confirmLabel={t("storage.budget.confirmDialogAction")}
                variant="danger"
                onConfirm={confirm}
            />
        </div>
    );
}

function UsageCard({
    usage,
    limitGB,
    budget,
    onConfirmed,
    failed = false,
    onRetry,
}: {
    usage: ActivityStorageUsage | null;
    limitGB: number;
    budget: StorageBudgetStatus | null;
    onConfirmed: () => void;
    failed?: boolean;
    onRetry?: () => void;
}) {
    const { t } = useTranslation("settings");
    // settings-review ST63: the numbers or the failure line, never an empty card.
    if (!usage) {
        return (
            <SettingsCard title={t("storage.usage.title")} description={t("storage.usage.description")}>
                {failed
                    ? <SettingsFailureLine failure={{ text: t("storage.usage.failed"), retry: onRetry }} />
                    : <SettingsPlaceholderRows rows={3} />}
            </SettingsCard>
        );
    }
    const limitBytes = limitGB * GIB;
    const ratio = limitBytes > 0 ? Math.min(1, usage.totalBytes / limitBytes) : 0;
    const over = limitBytes > 0 && usage.totalBytes > limitBytes;
    const size = (bytes: number) => <span className="text-xs text-muted tabular-nums">{formatBytes(bytes)}</span>;
    return (
        <SettingsCard title={t("storage.usage.title")} description={t("storage.usage.description")}>
            {usage.totalBytes <= 0 ? (
                <SettingsReason>{t("storage.usage.empty")}</SettingsReason>
            ) : (
                <div className="flex flex-col gap-2 pb-2">
                    <span className="settings-row-title tabular-nums">
                        {t("storage.usage.totalOfLimit", {
                            used: formatBytes(usage.totalBytes),
                            limit: t("storage.limit.gbValue", { value: limitGB }),
                        })}
                    </span>
                    {/* The numbers are the source of truth; the bar is a quick
                        "how close to the limit" read. */}
                    <div className="h-1 w-full rounded-full bg-ink/[0.06] overflow-hidden">
                        <div
                            className="h-full bg-accent-2/70 transition-[width] duration-300"
                            style={{ width: `${Math.round(ratio * 100)}%` }}
                        />
                    </div>
                    {budget && budget.state !== "ok" ? (
                        <BudgetNotice budget={budget} onConfirmed={onConfirmed} />
                    ) : (
                        over && <SettingsReason>{t("storage.usage.overLimit")}</SettingsReason>
                    )}
                </div>
            )}
            <SettingsRow
                title={t("storage.usage.screenshots")}
                description={
                    usage.screenshotDays > 0
                        ? t("storage.usage.screenshotsDetail", {
                              count: usage.screenshotDays,
                              date: formatDay(usage.oldestScreenshotMs),
                          })
                        : undefined
                }
                control={size(usage.screenshotBytes)}
            />
            <SettingsRow
                title={t("storage.usage.recordings")}
                description={
                    usage.audioFiles > 0
                        ? t("storage.usage.recordingsDetail", { count: usage.audioFiles, n: formatCount(usage.audioFiles) })
                        : undefined
                }
                control={size(usage.audioBytes)}
            />
            <SettingsRow
                title={t("storage.usage.database")}
                description={t("storage.usage.databaseDetail")}
                control={size(usage.databaseBytes)}
            />
            {usage.otherBytes > 0 && (
                <SettingsRow
                    title={t("storage.usage.other")}
                    description={t("storage.usage.otherDetail")}
                    control={size(usage.otherBytes)}
                />
            )}
        </SettingsCard>
    );
}

// ── Storage limit ──

interface StorageCapControlProps {
    valueGB: number;
    onCommit: (gb: number) => void;
    disabled: boolean;
}

// StorageCapControl renders the Select-or-typed-number combo for the storage
// limit. It manages two pieces of local state on top of the persisted
// valueGB:
//   - mode: "preset" shows the Select; "custom" swaps in a number input.
//   - draft: the unsaved input string (lets users freely type "1" while
//     aiming at "12" without us racing to commit "1" mid-keystroke).
// A value that doesn't match any preset auto-routes to "custom" mode on
// mount so users who set, say, 47 GB previously see their value in an
// editable input rather than a Select that silently snaps to the nearest
// preset.
function StorageCapControl({ valueGB, onCommit, disabled }: StorageCapControlProps) {
    const { t } = useTranslation("settings");
    const isPreset = (gb: number) => STORAGE_CAP_PRESETS_GB.some((preset) => preset === gb);
    const [mode, setMode] = useState<"preset" | "custom">(isPreset(valueGB) ? "preset" : "custom");
    const [draft, setDraft] = useState(String(valueGB));

    // Keep the draft synced when the upstream value changes (e.g. after a
    // successful save round-trip refetches the persisted GB).
    useEffect(() => {
        setDraft(String(valueGB));
        setMode(STORAGE_CAP_PRESETS_GB.some((preset) => preset === valueGB) ? "preset" : "custom");
    }, [valueGB]);

    const handleSelectChange = (value: string) => {
        if (value === STORAGE_CAP_CUSTOM) {
            setMode("custom");
            return;
        }
        const gb = Number(value);
        if (Number.isFinite(gb)) {
            onCommit(gb);
        }
    };

    const commitDraft = () => {
        const parsed = Math.floor(Number(draft));
        if (!Number.isFinite(parsed) || parsed < STORAGE_CAP_MIN || parsed > STORAGE_CAP_MAX) {
            // Reject silently and revert; the parent toasts on backend
            // rejection, but this catches obvious local typos like empty
            // input or "abc" without involving the backend.
            setDraft(String(valueGB));
            return;
        }
        if (parsed !== valueGB) {
            onCommit(parsed);
        }
    };

    if (mode === "custom") {
        return (
            <div className="flex items-center gap-1.5">
                <input
                    type="number"
                    min={STORAGE_CAP_MIN}
                    max={STORAGE_CAP_MAX}
                    step={1}
                    value={draft}
                    aria-label={t("storage.limit.title")}
                    onChange={(e) => setDraft(e.target.value)}
                    onBlur={commitDraft}
                    onKeyDown={(e) => {
                        if (e.key === "Enter") {
                            e.currentTarget.blur();
                        } else if (e.key === "Escape") {
                            setDraft(String(valueGB));
                            e.currentTarget.blur();
                        }
                    }}
                    disabled={disabled}
                    className="w-16 h-8 rounded-md border border-border bg-background px-2 text-xs text-ink placeholder:text-muted/50 focus:outline-none focus:ring-1 focus:ring-accent-2/40"
                />
                <span className="text-xs text-muted">{t("storage.limit.gbUnit")}</span>
                <button
                    type="button"
                    onClick={() => {
                        setMode("preset");
                        // Snap back to the default if the current value isn't
                        // a preset, so the Select has something sensible to
                        // display when we re-render.
                        if (!isPreset(valueGB)) {
                            onCommit(10);
                        }
                    }}
                    disabled={disabled}
                    className="text-xs text-accent hover:text-accent underline-offset-2 hover:underline"
                    title={t("activityRetention.storageCapPresetTitle")}
                >
                    {t("activityRetention.storageCapPresetButton")}
                </button>
            </div>
        );
    }

    return (
        <Select value={String(valueGB)} onValueChange={handleSelectChange} disabled={disabled}>
            <SelectTrigger aria-label={t("storage.limit.title")} className="w-[140px] h-8 text-xs">
                <SelectValue />
            </SelectTrigger>
            <SelectContent>
                {STORAGE_CAP_PRESETS_GB.map((gb) => (
                    <SelectItem key={gb} value={String(gb)}>
                        {t("storage.limit.gbValue", { value: gb })}
                    </SelectItem>
                ))}
                <SelectItem value={STORAGE_CAP_CUSTOM}>{t("activityRetention.storageCapCustom")}</SelectItem>
            </SelectContent>
        </Select>
    );
}

function StorageLimitCard({ storage, usedBytes }: { storage: RecordingStorage; usedBytes: number | null }) {
    const { t } = useTranslation("settings");
    const [confirmGB, setConfirmGB] = useState<number | null>(null);
    // Remounts the control after the confirmation closes, so a cancelled
    // custom value snaps back to what is saved.
    const [controlKey, setControlKey] = useState(0);

    const handleCommit = (gb: number) => {
        // While usage is unknown (still loading, or it failed) a lower limit
        // asks first too.
        if (gb < storage.maxGB && (usedBytes === null || usedBytes > gb * GIB)) {
            setConfirmGB(gb);
            return;
        }
        storage.saveMaxGB(gb);
    };

    return (
        <>
            <SettingsControlCard
                title={t("storage.limit.title")}
                description={t("storage.limit.description")}
                control={
                    storage.loading ? null : (
                        <StorageCapControl
                            key={controlKey}
                            valueGB={storage.maxGB}
                            onCommit={handleCommit}
                            disabled={storage.saving}
                        />
                    )
                }
            >
                <SettingsFailureLine failure={storage.failure} />
            </SettingsControlCard>
            <ConfirmDialog
                open={confirmGB !== null}
                onOpenChange={(open) => {
                    if (open) return;
                    setConfirmGB(null);
                    setControlKey((k) => k + 1);
                }}
                title={t("storage.limit.confirmTitle", { value: confirmGB ?? 0 })}
                description={t("storage.limit.confirmDescription", {
                    used: usedBytes === null ? "?" : formatBytes(usedBytes),
                })}
                confirmLabel={t("storage.limit.confirmAction")}
                variant="danger"
                onConfirm={() => {
                    if (confirmGB !== null) storage.saveMaxGB(confirmGB);
                }}
            />
        </>
    );
}


// ── Keep screenshots for ──

function ScreenshotDaysCard({ onSaved, recordingDays }: { onSaved: () => void; recordingDays: number }) {
    const { t } = useTranslation("settings");
    const [days, setDays] = useState<number | null>(null);
    const [saving, setSaving] = useState(false);
    const [confirmDays, setConfirmDays] = useState<number | null>(null);
    const { failure, fail, clear } = useSettingsFailure();
    const title = t("storage.screenshots.title");

    const read = useCallback(() => {
        GetScreenshotRetentionDays()
            .then((d) => setDays(d))
            .catch(() => {
                setDays(null);
                fail(t("storage.screenshots.readFailed"), read);
            });
    }, [fail, t]);
    useEffect(read, [read]);

    const save = (next: number) => {
        const previous = days;
        clear();
        setDays(next);
        setSaving(true);
        SetScreenshotRetentionDays(next)
            .then(() => {
                onSaved();
            })
            .catch(() => {
                setDays(previous);
                fail(t("storage.screenshots.failedToast"), () => save(next));
            })
            .finally(() => setSaving(false));
    };

    // Following recordings keeps images as long as the recording time limit;
    // a time not shorter than that limit is the same thing.
    const effective = (d: number) => (d === SCREENSHOTS_WITH_RECORDINGS ? recordingDays : d);
    const shown =
        days === null || days === SCREENSHOTS_WITH_RECORDINGS || (recordingDays > 0 && days >= recordingDays)
            ? SCREENSHOTS_WITH_RECORDINGS
            : days;

    const handleSelect = (value: string) => {
        const next = Number(value);
        if (!Number.isFinite(next) || next === shown || days === null) return;
        if (shortensRetention(effective(shown), effective(next))) {
            setConfirmDays(next);
            return;
        }
        save(next);
    };

    const confirmPeriod = confirmDays === null ? "" : retentionLabel(t, effective(confirmDays));
    let description = t("storage.screenshots.description");
    if (shown === SCREENSHOTS_WITH_RECORDINGS) {
        description =
            recordingDays === 0
                ? t("storage.screenshots.withRecordingsUnlimitedDescription")
                : t("storage.screenshots.withRecordingsDescription", { period: retentionLabel(t, recordingDays) });
    }

    return (
        <>
            <SettingsControlCard
                title={title}
                description={description}
                control={
                    days === null ? null : (
                        <Select value={String(shown)} onValueChange={handleSelect} disabled={saving}>
                            <SelectTrigger aria-label={title} className="w-auto min-w-[180px] h-8 text-xs">
                                <SelectValue />
                            </SelectTrigger>
                            <SelectContent>
                                {screenshotDayOptions(recordingDays).map((d) => (
                                    <SelectItem key={d} value={String(d)}>
                                        {screenshotDaysLabel(t, d, recordingDays)}
                                    </SelectItem>
                                ))}
                            </SelectContent>
                        </Select>
                    )
                }
            >
                {failure && <SettingsFailureLine failure={failure} />}
            </SettingsControlCard>
            <ConfirmDialog
                open={confirmDays !== null}
                onOpenChange={(open) => {
                    if (!open) setConfirmDays(null);
                }}
                title={t("storage.screenshots.confirmTitle", { period: confirmPeriod })}
                description={t("storage.screenshots.confirmDescription", { period: confirmPeriod })}
                confirmLabel={t("storage.screenshots.confirmAction")}
                variant="danger"
                onConfirm={() => {
                    if (confirmDays !== null) save(confirmDays);
                }}
            />
        </>
    );
}

// ── Sheet ──

/** The sheet's cards. Mounted only while the sheet is open, so its reads
 *  run each time it opens. */
function KeepTimesBody() {
    const storage = useRecordingStorage();
    const [usage, setUsage] = useState<ActivityStorageUsage | null>(null);
    const [usageFailed, setUsageFailed] = useState(false);
    const [usageAttempt, setUsageAttempt] = useState(0);
    const [budget, setBudget] = useState<StorageBudgetStatus | null>(null);
    const { refreshKey, refresh } = storage;

    useEffect(() => {
        setUsageFailed(false);
        GetActivityStorageUsage()
            .then((u) => {
                setUsage(u ?? null);
                setUsageFailed(!u);
            })
            // A lower limit still asks first while usage is unknown.
            .catch(() => {
                setUsage(null);
                setUsageFailed(true);
            });
        GetActivityStorageBudget()
            .then((b) => setBudget(b))
            .catch(() => setBudget(null));
    }, [refreshKey, usageAttempt]);

    // Settings changes start a rotation pass; read usage again once it has
    // had a moment. The timer is cleared when the page closes.
    const refreshTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
    const refreshSoon = useCallback(() => {
        if (refreshTimerRef.current !== null) clearTimeout(refreshTimerRef.current);
        refreshTimerRef.current = setTimeout(() => {
            refreshTimerRef.current = null;
            refresh();
        }, 1500);
    }, [refresh]);
    useEffect(
        () => () => {
            if (refreshTimerRef.current !== null) clearTimeout(refreshTimerRef.current);
        },
        [],
    );

    return (
        <>
            <RecordingRetentionCard storage={storage} />
            <UsageCard usage={usage} limitGB={storage.maxGB} budget={budget} onConfirmed={refreshSoon} failed={usageFailed} onRetry={() => setUsageAttempt((n) => n + 1)} />
            <StorageLimitCard storage={storage} usedBytes={usage ? usage.totalBytes : null} />
            <ScreenshotDaysCard onSaved={refreshSoon} recordingDays={storage.days} />
        </>
    );
}

export function KeepTimesSheet({ open, onOpenChange }: { open: boolean; onOpenChange: (open: boolean) => void }) {
    const { t } = useTranslation("settings");
    return (
        <Dialog open={open} onOpenChange={onOpenChange}>
            <DialogContent className="w-[560px] max-w-[90vw]" data-testid="keep-times-sheet">
                <DialogHeader className="flex-col items-start gap-1.5">
                    <DialogTitle>{t("yourData.changeTitle")}</DialogTitle>
                    <DialogDescription>{t("yourData.changeSub")}</DialogDescription>
                </DialogHeader>
                <div className="min-h-0 space-y-5 overflow-y-auto px-6 py-5">{open && <KeepTimesBody />}</div>
            </DialogContent>
        </Dialog>
    );
}
