import { LogoIcon } from "@/assets/icons/LogoIcon";
import { useEffect, useState } from "react";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";
import { Trans, useTranslation } from "react-i18next";
import { Button } from "@/components/ui/button";
import { SettingsPlaceholderRows } from "@/components/settings/settings-failure";
import { formatBytes } from "@/components/settings/settings-format";
import { Switch } from "@/components/ui/switch";
import { useAboutInfo } from "@/hooks/use-about-info";
import { briefClockTime, briefDateTime, briefDateAtTime } from "@/lib/brief-time";
import {
    CheckForUpdates,
    GetVersion,
    DownloadUpdate,
    SetBackgroundUpdates,
    RestartToUpdate,
    SaveSupportLogs,
    OpenDataFolder,
    OpenExternalURL,
    AcknowledgeRollbackNotice,
} from "@/lib/wails-shim-app";

const LICENSE_URL = "https://github.com/yubozhao/isoform/blob/main/LICENSE.md";
const PRIVACY_URL = "https://github.com/yubozhao/isoform#privacy";

export function AboutUpdates() {
    const { t, i18n } = useTranslation("settings");
    const { t: clock } = useTranslation("today");
    const { info, failed, refresh } = useAboutInfo();
    const [error, setError] = useState(false);
    const [busy, setBusy] = useState(false);
    // Shown from the click until the app quits; cleared only on failure.
    const [restarting, setRestarting] = useState(false);
    // The rollback notice is shown once: it is kept here after the backend clears it.
    const [rollbackNotice, setRollbackNotice] = useState("");
    const backendNotice = info?.rollbackNotice ?? "";
    useEffect(() => {
        if (!backendNotice) return;
        setRollbackNotice(backendNotice);
        void AcknowledgeRollbackNotice().catch(() => undefined);
    }, [backendNotice]);
    async function restart() {
        setRestarting(true);
        try {
            await RestartToUpdate();
        } catch (err) {
            setRestarting(false);
            throw err;
        }
    }
    async function perform(action: () => Promise<unknown>) {
        setBusy(true);
        setError(false);
        try {
            await action();
            await refresh();
        } catch {
            setError(true);
        } finally {
            setBusy(false);
        }
    }
    if (!info && !failed) return <SettingsPlaceholderRows rows={2} />;
    if (!info)
        return (
            <div role="status">
                {t(failed ? "aboutPolish.loadFailed" : "aboutPolish.loading")}
                {failed && <AboutVersionFallback />}
                {failed && (
                    <Button
                        size="sm"
                        variant="outline"
                        onClick={() => void refresh()}
                    >
                        {t("aboutPolish.retry")}
                    </Button>
                )}
            </div>
        );
    const status = info.update.status;
    const ready = status === "update-downloaded";
    const checking = status === "checking";
    const checked = info.lastChecked
        ? briefDateTime(info.lastChecked, i18n.language, clock)
        : null;
    const today =
        info.lastChecked &&
        new Date(info.lastChecked).toDateString() === new Date().toDateString();
    const checkedTime = today
        ? briefClockTime(
              new Date(info.lastChecked).toISOString(),
              i18n.language,
              clock,
          )
        : checked;
    let statusLine = t("aboutPolish.idle");
    switch (status) {
        case "disabled":
            statusLine = t("aboutPolish.releaseOnly");
            break;
        case "checking":
            statusLine = t("aboutPolish.checking");
            break;
        case "up-to-date":
            statusLine = checkedTime
                ? t(
                      today
                          ? "aboutPolish.checkedToday"
                          : "aboutPolish.checked",
                      { time: checkedTime },
                  )
                : t("aboutPolish.upToDate");
            break;
        case "update-downloaded":
            // Shown as "{version} is ready · Restart now" below (answers
            // v1.0.0 S3): with the sidebar card gone, this is the one place
            // on screen that says an update is ready.
            break;
        case "update-available":
            statusLine = t("aboutPolish.available", {
                version: info.update.version,
            });
            break;
        case "downloading":
            statusLine = t("aboutPolish.downloading", {
                percent: Math.round(info.update.percent ?? 0),
            });
            break;
        case "error":
            statusLine = t("aboutPolish.failed");
            break;
    }
    return (
        <>
            <section className="flex flex-col gap-3.5 rounded-xl border border-border bg-panel px-5 py-[18px]">
                <div className="flex flex-wrap items-center gap-3.5">
                    <LogoIcon size={19} className="mb-3.5 shrink-0 text-accent" />
                    <div className="flex-1">
                        <h2 className="text-[15px] font-semibold text-ink">
                            {t("aboutPolish.product")}
                        </h2>
                        <p className="text-[12.5px] text-muted">
                            {t("aboutPolish.version", {
                                version: info.version,
                            })}
                        </p>
                    </div>
                    <Button
                        size="sm"
                        variant="outline"
                        disabled={
                            busy ||
                            checking ||
                            status === "downloading" ||
                            status === "disabled"
                        }
                        onClick={() =>
                            void perform(async () => {
                                const result = await CheckForUpdates();
                                if (result.error) throw new Error(result.error);
                            })
                        }
                    >
                        {t(
                            checking
                                ? "aboutPolish.checkingButton"
                                : "about.checkNow",
                        )}
                    </Button>
                </div>
                {ready ? (
                    <p role="status" className="text-[12.5px] text-ink" data-testid="about-update-ready">
                        {restarting ? (
                            t("aboutPolish.restarting")
                        ) : (
                            <Trans
                                t={t}
                                i18nKey="about.updateReady"
                                values={{ version: info.update.version }}
                                components={{
                                    restart: (
                                        <Button
                                            size="sm"
                                            variant="link"
                                            className="h-auto p-0 text-[12.5px]"
                                            disabled={busy}
                                            onClick={() => void perform(restart)}
                                        />
                                    ),
                                }}
                            />
                        )}
                    </p>
                ) : (
                    <p role="status" className="text-[12.5px] text-muted">
                        {statusLine}
                    </p>
                )}
                {rollbackNotice && (
                    <p className="text-[12.5px] text-muted">{rollbackNotice}</p>
                )}
                {status === "update-available" && (
                    <Button
                        className="self-start"
                        size="sm"
                        variant="outline"
                        disabled={busy}
                        onClick={() => void perform(DownloadUpdate)}
                    >
                        {t("aboutPolish.download")}
                    </Button>
                )}
                {status === "downloading" && (
                    <div
                        role="progressbar"
                        aria-label={t("aboutPolish.download")}
                        aria-valuemin={0}
                        aria-valuemax={100}
                        aria-valuenow={Math.max(
                            0,
                            Math.min(100, info.update.percent ?? 0),
                        )}
                        className="h-1.5 w-full overflow-hidden rounded-full bg-border"
                    >
                        <div
                            className="h-full rounded-full bg-accent-2"
                            style={{
                                width: `${Math.max(0, Math.min(100, info.update.percent ?? 0))}%`,
                            }}
                        />
                    </div>
                )}
                <label className="flex items-center gap-2.5 text-[13px] text-ink">
                    <Switch
                        checked={info.backgroundUpdates}
                        disabled={busy}
                        onCheckedChange={(value) =>
                            void perform(() => SetBackgroundUpdates(value))
                        }
                    />
                    {t("about.autoUpdate")}
                </label>
            </section>
            {error && (
                <p role="alert" className="text-sm text-copper">
                    {t("aboutPolish.actionFailed")}
                </p>
            )}
            <div className="divide-y divide-border border-t border-border">
                <AboutRow
                    title={t("aboutPolish.whatsNew")}
                    description={
                        info.releaseNotes ? (
                            <div className="prose leading-relaxed [&_ul]:list-disc [&_ul]:pl-4 [&_ol]:list-decimal [&_ol]:pl-4 [&_p]:mb-2 [&_p:last-child]:mb-0">
                                <span>
                                    {t("aboutPolish.notesVersion", {
                                        version: info.releaseNotesVersion,
                                    })}{" "}
                                </span>
                                <div className="inline [&>p:first-child]:inline">
                                    <ReactMarkdown
                                        remarkPlugins={[remarkGfm]}
                                        components={{
                                            a: ({ href, children }) => (
                                                <Button
                                                    size="sm"
                                                    variant="link"
                                                    onClick={() => {
                                                        if (href) {
                                                            void perform(() => OpenExternalURL(href));
                                                        }
                                                    }}
                                                >
                                                    {children}
                                                </Button>
                                            ),
                                        }}
                                    >
                                        {info.releaseNotes}
                                    </ReactMarkdown>
                                </div>
                            </div>
                        ) : (
                            t("aboutPolish.productVersion", {
                                version: (info.releaseNotesVersion || info.version).replace(/^v/, ""),
                            })
                        )
                    }
                    control={
                        <Button
                            size="sm"
                            variant="link"
                            onClick={() =>
                                void perform(() =>
                                    OpenExternalURL(info.releaseNotesUrl),
                                )
                            }
                        >
                            {t("aboutPolish.releaseNotes")}
                        </Button>
                    }
                />
                <AboutRow
                    title={t("aboutPolish.build")}
                    description={
                        <>
                            {t(
                                `aboutPolish.builds.${info.build === "release" ? "release" : info.build === "staging" ? "staging" : "development"}`,
                            )}
                            {info.build === "staging" &&
                                info.commit &&
                                t("aboutPolish.buildCommit", { commit: info.commit })}
                            {info.buildTime &&
                                t("aboutPolish.buildDate", {
                                    date:
                                        briefDateAtTime(
                                            info.buildTime,
                                            i18n.language,
                                            clock,
                                        ) ?? "",
                                })}
                        </>
                    }
                />
                <AboutRow
                    title={t("aboutPolish.computer")}
                    description={t("aboutPolish.computerDetails", {
                        model: info.computer,
                        os: info.os,
                        phone: info.phones.length
                            ? t("aboutPolish.paired", {
                                  phone: info.phones.join(", "),
                              })
                            : t("aboutPolish.noPhone"),
                    })}
                />
                <AboutRow
                    title={t("aboutPolish.data")}
                    description={t("aboutPolish.dataDescription", {
                        size: formatBytes(info.dataBytes),
                    })}
                    control={
                        <Button
                            size="sm"
                            variant="link"
                            onClick={() => void perform(OpenDataFolder)}
                        >
                            {t("aboutPolish.openFolder")}
                        </Button>
                    }
                />
                <AboutRow
                    title={t("aboutPolish.support")}
                    description={t("aboutPolish.supportDescription")}
                    control={
                        <Button
                            size="sm"
                            variant="link"
                            disabled={busy}
                            onClick={() => void perform(SaveSupportLogs)}
                        >
                            {t("aboutPolish.saveLogs")}
                        </Button>
                    }
                />
                <AboutRow
                    title={t("aboutPolish.legal")}
                    description={t("aboutPolish.legalDescription")}
                    control={
                        <div className="flex items-center whitespace-nowrap">
                            <Button size="sm" variant="link" onClick={() => void perform(() => OpenExternalURL(LICENSE_URL))}>
                                {t("aboutPolish.license")}
                            </Button>
                            <Button size="sm" variant="link" onClick={() => void perform(() => OpenExternalURL(PRIVACY_URL))}>
                                {t("aboutPolish.privacy")}
                            </Button>
                        </div>
                    }
                />
            </div>
        </>
    );
}

function AboutRow({
    title,
    description,
    control,
}: {
    title: string;
    description: React.ReactNode;
    control?: React.ReactNode;
}) {
    return (
        <div className="grid grid-cols-[minmax(90px,140px)_minmax(0,1fr)_auto] items-center gap-4 py-3 text-[13.5px] sm:grid-cols-[180px_minmax(0,1fr)_auto]">
            <span className="text-muted">{title}</span>
            <div className="min-w-0 text-ink">{description}</div>
            {control}
        </div>
    );
}

// The version binding remains available when richer device metadata cannot
// load, including the browser bridge's intentionally limited read surface.
function AboutVersionFallback() {
    const { t } = useTranslation("settings");
    const [version, setVersion] = useState("");
    useEffect(() => {
        void GetVersion()
            .then(setVersion)
            .catch(() => {});
    }, []);
    return version ? (
        <p className="my-2 text-[12.5px] text-muted">
            {t("aboutPolish.version", { version })}
        </p>
    ) : null;
}
