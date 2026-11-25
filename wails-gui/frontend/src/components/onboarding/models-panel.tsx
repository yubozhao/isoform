import { Fragment, useMemo, useState, useEffect } from "react";
import { Check, LockKeyhole } from "lucide-react";
import { useTranslation } from "react-i18next";
import { MarkdownContent } from "@/components/markdown-content";
import { ConfirmDialog } from "@/components/confirm-dialog";
import { Button } from "@/components/ui/button";
import {
    Dialog,
    DialogContent,
    DialogDescription,
    DialogTitle,
} from "@/components/ui/dialog";
import {
    Select,
    SelectContent,
    SelectItem,
    SelectTrigger,
    SelectValue,
} from "@/components/ui/select";
import { RemoveModel } from "@/lib/wails-shim-app";
import {
    planSetupDownloads,
    changeSetupModel,
    startSetupDownloads,
} from "@/lib/setup-model-adapter";
import { useSetupModels } from "@/hooks/useSetupModels";
import type {
    ModelChoices,
    ModelDownload,
    ModelDownloadPlan,
    ModelDownloadRequest,
    SetupModel,
} from "@/lib/setup-model-types";
import {
    modelName,
    modelDescription,
    modelEvidence,
    modelSourceName,
    modelEvaluationTable,
} from "@/lib/setup-model-copy";
import { formatBytes } from "@/components/settings/settings-format";
import { useSettingsDevTools } from "@/components/settings/settings-dev-tools";
import { SetupBody, SetupFooter, SetupHeading } from "./setup-layout";
// Settings › Storage › Models keeps the setup board styles it was drawn with.
import "./setup.css";
import {
    DownloadProgressLine,
    ModelNetworkNotice,
} from "./model-download-progress";

const JOBS = ["live", "after-call", "voice", "always"] as const;
type ChoiceJob = Exclude<(typeof JOBS)[number], "always">;
/** A model's tag in Settings: on disk, confirmed with Later and waiting for
 * power and idle (setup v2.2.0 §2), or not downloaded. */
function downloadTag(model: SetupModel, downloads: ModelDownload[]) {
    if (model.downloaded) return "models.downloaded" as const;
    return downloads.some((job) => job.modelId === model.id && job.deferred)
        ? ("models.waitingPowerIdle" as const)
        : ("models.notDownloaded" as const);
}
function selectedModels(choices: ModelChoices): SetupModel[] {
    const selected = new Set(Object.values(choices.wireChoices));
    return choices.models.filter((model) => selected.has(model.id));
}
/** The model a job uses in Settings. A chosen model that is not on disk is
 * never shown as in use: the job falls back to a downloaded model of the same
 * job (recommended first), or to none when nothing is downloaded. */
function servingModel(
    models: SetupModel[],
    job: string,
    chosen: SetupModel | undefined,
): SetupModel | undefined {
    if (chosen?.downloaded) return chosen;
    return models
        .filter((model) => model.job === job && model.downloaded)
        .sort((a, b) => Number(b.recommended) - Number(a.recommended))[0];
}

export function ModelsPanel({
    settings = false,
    onNext,
    onBack,
}: {
    settings?: boolean;
    onNext?: () => void;
    onBack?: () => void;
}) {
    const { t } = useTranslation("onboarding");
    const devTools = useSettingsDevTools();
    const { choices, downloads, network, refresh, error, unavailable } =
        useSetupModels();
    const [sourceDefaults, setSourceDefaults] = useState<
        Record<string, string>
    >(() => {
        try {
            return JSON.parse(
                localStorage.getItem("isoform.models.source-defaults.v1") ??
                    "{}",
            );
        } catch {
            return {};
        }
    });
    const [customMirror, setCustomMirror] = useState(() => {
        try {
            return (
                localStorage.getItem("isoform.models.custom-mirror.v1") ?? ""
            );
        } catch {
            return "";
        }
    });
    const [sourceSaveFailed, setSourceSaveFailed] = useState(false);
    const saveDefault = (id: string, source: string) => {
        const next = { ...sourceDefaults, [id]: source };
        try {
            localStorage.setItem(
                "isoform.models.source-defaults.v1",
                JSON.stringify(next),
            );
            setSourceDefaults(next);
            setSourceSaveFailed(false);
        } catch {
            setSourceSaveFailed(true);
        }
    };
    const [evaluationOpen, setEvaluationOpen] = useState(false);
    const [expanded, setExpanded] = useState<ChoiceJob | null>(null);
    const [busy, setBusy] = useState(false);
    const [actionError, setActionError] = useState(false);
    const [removeTarget, setRemoveTarget] = useState<SetupModel | null>(null);
    const [planValid, setPlanValid] = useState(false);
    const [planChoices, setPlanChoices] = useState<ModelChoices | null>(null);
    const [plan, setPlan] = useState<ModelDownloadPlan | null>(null);
    const [sources, setSources] = useState<Record<string, string>>({});
    const [folder, setFolder] = useState("");
    const [editingFolder, setEditingFolder] = useState(false);
    const mirrorIsValid = (() => {
        try {
            const url = new URL(customMirror);
            return (
                url.protocol === "https:" &&
                !url.username &&
                !url.password &&
                !url.search &&
                !url.hash
            );
        } catch {
            return false;
        }
    })();
    const chosen = useMemo(
        () => (choices ? selectedModels(choices) : []),
        [choices],
    );
    const allInstalled =
        chosen.length > 0 &&
        chosen.every((model) => model.downloaded || model.files.length === 0);
    const missing = chosen.filter(
        (model) =>
            !model.downloaded &&
            model.files.length > 0 &&
            !downloads.some((job) => job.modelId === model.id),
    );
    const [summary, setSummary] = useState<ModelDownloadPlan | null>(null);
    // The primary's total comes from the planner too, including existing
    // files and dependency packs. Render never starts a download.
    const selectionKey = chosen
        .map((model) => `${model.id}:${model.downloaded}`)
        .join(",");
    // Keep async planner responses from older choices from replacing totals.
    const totals = useMemo(
        () =>
            choices
                ? {
                      folder: choices.modelsFolder,
                      sources: {},
                  }
                : null,
        [selectionKey, choices?.modelsFolder],
    );
    usePlanSummary(totals, choices, setSummary);
    const act = async (operation: () => Promise<unknown>) => {
        if (busy) return;
        setBusy(true);
        setActionError(false);
        try {
            await operation();
            await refresh();
        } catch {
            setActionError(true);
        } finally {
            setBusy(false);
        }
    };
    const review = async (target?: SetupModel) => {
        if (!choices) return;
        if (!target && !missing.length) {
            onNext?.();
            return;
        }
        await act(async () => {
            const nextChoices = target
                ? {
                      ...choices,
                      wireChoices: {
                          ...choices.wireChoices,
                          [target.job === "after-call"
                              ? "afterCall"
                              : target.job === "speaker"
                                ? "speakerPack"
                                : target.job]: target.id,
                      },
                  }
                : choices;
            const next = await planSetupDownloads(
                {
                    folder: choices.modelsFolder,
                    sources: sourceDefaults,
                },
                nextChoices,
            );
            setPlanChoices(nextChoices);
            setPlanValid(true);
            setSources(
                Object.fromEntries(
                    next.items.map((model) => [
                        model.id,
                        model.sources[0]?.id ?? "",
                    ]),
                ),
            );
            setFolder(next.folder);
            setEditingFolder(false);
            setPlan(next);
        });
    };
    const request = (): ModelDownloadRequest => ({
        folder,
        sources,
    });
    const replan = async (nextFolder: string) => {
        if (nextFolder !== choices?.modelsFolder) {
            setActionError(true);
            return;
        }
        await act(async () => {
            setPlan(
                await planSetupDownloads(
                    {
                        ...request(),
                        folder: nextFolder,
                    },
                    planChoices ?? choices!,
                ),
            );
            setPlanValid(true);
            setEditingFolder(false);
        });
    };
    const changeSources = async (nextSources: Record<string, string>) => {
        // Keep the shown host and opaque plan together. A failed replacement
        // restores the last reviewed display and requires a successful retry.
        setPlanValid(false);
        await act(async () => {
            const next = await planSetupDownloads(
                { ...request(), sources: nextSources },
                planChoices ?? choices!,
            );
            setPlan(next);
            setSources(
                Object.fromEntries(
                    next.items.map((item) => [
                        item.id,
                        item.sources[0]?.id ?? "",
                    ]),
                ),
            );
            setPlanValid(true);
        });
    };
    const confirm = async () => {
        if (!plan || !planValid || editingFolder || !plan.enoughSpace || busy)
            return;
        await act(async () => {
            await startSetupDownloads(plan);
            setPlan(null);
            onNext?.();
        });
    };
    const rows = choices && (
        <div
            className={`setup-list ${expanded ? "setup-models-expanded" : ""}`}
        >
            {JOBS.map((job) => {
                const models =
                    job === "always"
                        ? chosen.filter(
                              (model) =>
                                  model.job === "privacy" ||
                                  model.job === "memory",
                          )
                        : choices.models.filter(
                              (model) => model.id === choices.choices[job],
                          );
                // Settings names the model in use: the chosen one once it is
                // on disk, else the real fallback. Setup keeps the choice.
                const inUse =
                    job === "always" || !settings
                        ? models[0]
                        : servingModel(choices.models, job, models[0]);
                const model = inUse;
                const downloaded =
                    settings && job !== "always"
                        ? Boolean(inUse?.downloaded)
                        : models.length > 0 &&
                          models.every((item) => item.downloaded);
                // Each model gets one row: the in-use model sits on the job row
                // and the chosen model, when it is not in use, keeps its asset row.
                const listed = new Set(
                    [model?.id, ...models.map((item) => item.id)].filter(
                        (id): id is string => Boolean(id),
                    ),
                );
                const alternatives =
                    job === "always"
                        ? []
                        : choices.models.filter(
                              (item) =>
                                  item.job === job && !listed.has(item.id),
                          );
                // Asset rows list the chosen model unless its row is the in-use one.
                const assetModels =
                    settings && job !== "always"
                        ? models.filter((item) => job !== "live" && item.id !== inUse?.id)
                        : models;
                const sizedModels =
                    settings && job !== "always"
                        ? inUse
                            ? [inUse]
                            : []
                        : models;
                const inUseActions =
                    settings &&
                    job !== "always" &&
                    inUse &&
                    inUse.files.length > 0 ? (
                        <span
                            data-testid={`model-asset-${inUse.id}`}
                            className="flex items-center gap-2"
                        >
                            <span
                                className={`setup-tag ${inUse.downloaded ? "chosen" : ""}`}
                            >
                                {t(downloadTag(inUse, downloads))}
                            </span>
                            {!inUse.downloaded && (
                                <Button
                                    variant="quiet"
                                    size="sm"
                                    disabled={busy}
                                    onClick={() => void review(inUse)}
                                >
                                    {t("models.downloadAsset")}
                                </Button>
                            )}
                        </span>
                    ) : null;
                return (
                    <Fragment key={job}>
                        <div
                            className="setup-row setup-model-row"
                            data-testid={`model-job-${job}`}
                        >
                            <span className="setup-model-job">
                                {t(`models.jobs.${job}`)}
                            </span>
                            <span
                                className={`flex size-4 shrink-0 items-center justify-center rounded-[4px] ${job === "always" || downloaded || (settings && !inUse) ? "bg-panel-warm text-muted" : "bg-accent text-solid-btn-fg"}`}
                                aria-hidden
                            >
                                {job === "always" || downloaded || (settings && !inUse) ? (
                                    <LockKeyhole className="size-3" />
                                ) : (
                                    <Check className="size-3" />
                                )}
                            </span>
                            <div className="flex-1 min-w-0">
                                <div className="setup-name flex items-center gap-2 flex-wrap">
                                    {job === "always"
                                        ? t("models.alwaysName")
                                        : model
                                          ? modelName(model, t)
                                          : t("models.none")}
                                    {!settings && job !== "always" && model && (
                                        <span
                                            className={`setup-tag ${downloaded || model.recommended ? "chosen" : ""}`}
                                        >
                                            {t(
                                                downloaded
                                                    ? "models.downloaded"
                                                    : model.recommended
                                                      ? "models.recommended"
                                                      : "models.selected",
                                            )}
                                        </span>
                                    )}
                                </div>
                                <p className="setup-description">
                                    {job === "always"
                                        ? t("models.alwaysDescription")
                                        : model
                                          ? modelDescription(model, t)
                                          : t("models.noneDescription")}
                                </p>
                            </div>
                            <div className="flex items-center gap-2 shrink-0">
                                <span className="text-[12.5px] text-muted">
                                    {formatBytes(
                                        sizedModels.reduce(
                                            (total, item) =>
                                                total + (item.sizeBytes ?? 0),
                                            0,
                                        ),
                                    )}
                                </span>
                                {inUseActions}
                                {job !== "always" && !(settings && job === "live") && (
                                    <Button
                                        variant="quiet"
                                        size="sm"
                                        disabled={busy}
                                        aria-label={t("models.changeJob", {
                                            taskLabel: t(`models.jobs.${job}`),
                                        })}
                                        aria-expanded={expanded === job}
                                        onClick={() =>
                                            setExpanded(
                                                expanded === job ? null : job,
                                            )
                                        }
                                    >
                                        {t(
                                            expanded === job
                                                ? "models.chosen"
                                                : "models.change",
                                        )}
                                    </Button>
                                )}
                            </div>
                        </div>
                        {job !== "always" && expanded === job && (
                            <>
                                {model && modelEvidence(model, t) && (
                                    <div className="setup-model-evidence">
                                        {modelEvidence(model, t)}
                                    </div>
                                )}
                                {alternatives.map((item) => (
                                    <div
                                        key={item.id}
                                        className="setup-model-alternative"
                                    >
                                        <input
                                            type="radio"
                                            name={`model-${job}`}
                                            aria-label={modelName(item, t)}
                                            checked={false}
                                            disabled={busy}
                                            onChange={() =>
                                                void act(async () => {
                                                    await changeSetupModel(
                                                        job,
                                                        item.id,
                                                    );
                                                })
                                            }
                                            className="size-4 accent-[var(--color-accent)]"
                                        />
                                        <div className="flex-1">
                                            <div className="setup-name">
                                                {modelName(item, t)}
                                                {item.downloaded && (
                                                    <span className="setup-tag ml-2 chosen">
                                                        {t("models.downloaded")}
                                                    </span>
                                                )}
                                            </div>
                                            <p className="setup-description">
                                                {modelDescription(item, t)}{" "}
                                                <span className="setup-note">
                                                    {modelEvidence(item, t)}
                                                </span>
                                            </p>
                                        </div>
                                        <span className="text-xs text-muted">
                                            {item.sizeBytes === null
                                                ? t("models.unknownSize")
                                                : formatBytes(item.sizeBytes)}
                                        </span>
                                        {settings && item.downloaded && (
                                            <Button
                                                variant="quiet"
                                                size="sm"
                                                disabled={busy}
                                                onClick={() =>
                                                    setRemoveTarget(item)
                                                }
                                            >
                                                {t("models.remove")}
                                            </Button>
                                        )}
                                    </div>
                                ))}
                            </>
                        )}
                        {settings &&
                            assetModels.map((item) => (
                                <div
                                    key={item.id}
                                    data-testid={`model-asset-${item.id}`}
                                    className="flex items-center gap-2 px-4 pb-2"
                                >
                                    <span className="setup-note flex-1">
                                        {modelName(item, t)}
                                    </span>
                                    <span className="setup-note">
                                        {item.sizeBytes === null
                                            ? t("models.unknownSize")
                                            : formatBytes(item.sizeBytes)}
                                    </span>
                                    <span
                                        className={`setup-tag ${item.downloaded ? "chosen" : ""}`}
                                    >
                                        {t(downloadTag(item, downloads))}
                                    </span>
                                    {item.downloaded ? (
                                        <Button
                                            variant="quiet"
                                            size="sm"
                                            disabled={busy}
                                            onClick={() =>
                                                setRemoveTarget(item)
                                            }
                                        >
                                            {t("models.remove")}
                                        </Button>
                                    ) : (
                                        item.files.length > 0 && (
                                            <Button
                                                variant="quiet"
                                                size="sm"
                                                disabled={busy}
                                                onClick={() =>
                                                    void review(item)
                                                }
                                            >
                                                {t("models.downloadAsset")}
                                            </Button>
                                        )
                                    )}
                                </div>
                            ))}
                    </Fragment>
                );
            })}
        </div>
    );
    const content = (
        <>
            {settings ? (
                <div className="flex flex-col gap-1">
                    <h2 className="text-[13px] font-semibold text-ink">{t("models.settingsTitle")}</h2>
                    <p className="text-[12.5px] text-muted">{t("models.settingsDescription")}</p>
                </div>
            ) : (
                <SetupHeading title={t("models.heading")} description={t("models.description")} />
            )}
            <ModelNetworkNotice network={network} />
            {rows}
            {settings &&
                choices?.models
                    .filter((item) => item.job === "speaker")
                    .map((item) => (
                        <div
                            key={item.id}
                            data-testid={`model-asset-${item.id}`}
                            className="setup-row rounded-lg border border-border"
                        >
                            <div className="flex-1">
                                <div className="setup-name">
                                    {modelName(item, t)}
                                </div>
                                <p className="setup-description">
                                    {modelDescription(item, t)}
                                </p>
                            </div>
                            <span className="setup-note">
                                {item.sizeBytes === null
                                    ? t("models.unknownSize")
                                    : formatBytes(item.sizeBytes)}
                            </span>
                            <span
                                className={`setup-tag ${item.downloaded ? "chosen" : ""}`}
                            >
                                {t(downloadTag(item, downloads))}
                            </span>
                            {item.downloaded ? (
                                <Button
                                    variant="quiet"
                                    size="sm"
                                    disabled={busy}
                                    onClick={() => setRemoveTarget(item)}
                                >
                                    {t("models.remove")}
                                </Button>
                            ) : (
                                <Button
                                    variant="quiet"
                                    size="sm"
                                    disabled={busy}
                                    onClick={() => void review(item)}
                                >
                                    {t("models.downloadAsset")}
                                </Button>
                            )}
                        </div>
                    ))}
            {unavailable && <p className="setup-note">{t("models.legacy")}</p>}
            {settings && (error || actionError) && (
                <p role="alert" data-settings-failure className="text-[12px] text-copper">
                    {t("models.settingsLoadFailed")}{" "}
                    <button type="button" className="text-accent hover:underline" onClick={() => void refresh()}>
                        {t("models.tryAgain")}
                    </button>
                </p>
            )}
            {!settings && (error || actionError) && (
                <p role="alert" className="text-xs text-error">
                    {t("models.loadFailed")}{" "}
                    <Button
                        size="sm"
                        variant="quiet"
                        onClick={() => void refresh()}
                    >
                        {t("models.retry")}
                    </Button>
                </p>
            )}
            {!settings && <p className="setup-note">{t("models.note")}</p>}
            {settings && choices && (
                <>
                    <div className="text-[12.5px] text-muted">
                        {t("models.folder")}{" "}
                        <code className="text-ink">{choices.modelsFolder}</code>
                    </div>
                    <div className="flex flex-col gap-2">
                        <h3 className="text-[13px] font-semibold">
                            {t("models.sources")}
                        </h3>
                        {chosen
                            .filter((model) => model.files.length > 0)
                            .map((model) => (
                                <div
                                    className="flex items-center gap-2 text-xs"
                                    key={model.id}
                                >
                                    <span className="flex-1 text-muted">
                                        {modelName(model, t)}
                                    </span>
                                    <Select
                                        value={
                                            sourceDefaults[model.id] ??
                                            model.sources[0]?.id
                                        }
                                        onValueChange={(source) =>
                                            saveDefault(model.id, source)
                                        }
                                    >
                                        <SelectTrigger
                                            className="w-[260px] h-8 text-xs"
                                            aria-label={t(
                                                "models.defaultSourceFor",
                                                { name: modelName(model, t) },
                                            )}
                                        >
                                            <SelectValue />
                                        </SelectTrigger>
                                        <SelectContent>
                                            {model.sources.map((source) => (
                                                <SelectItem
                                                    value={source.id}
                                                    key={source.id}
                                                >
                                                    {modelSourceName(
                                                        source.host,
                                                        source.name,
                                                        t,
                                                    )}
                                                </SelectItem>
                                            ))}
                                            {mirrorIsValid && (
                                                <SelectItem
                                                    value={customMirror}
                                                >
                                                    {t("models.customMirror")}
                                                </SelectItem>
                                            )}
                                        </SelectContent>
                                    </Select>
                                </div>
                            ))}
                        {/* The custom mirror is a Developer tools detail
                            (computer-conformance answers v1.5.0 S21); a saved
                            one stays a source to pick above. */}
                        {devTools && (
                        <>
                        <label
                            className="text-xs text-muted"
                            htmlFor="setup-custom-mirror"
                        >
                            {t("models.customMirror")}
                        </label>
                        <div className="flex gap-2">
                            <input
                                id="setup-custom-mirror"
                                className="flex-1 border border-border rounded-lg bg-panel px-3 py-2 text-xs"
                                placeholder={t(
                                    "models.customMirrorPlaceholder",
                                )}
                                value={customMirror}
                                onChange={(event) =>
                                    setCustomMirror(event.target.value)
                                }
                            />
                            <Button
                                size="sm"
                                variant="outline"
                                disabled={!!customMirror && !mirrorIsValid}
                                onClick={() => {
                                    try {
                                        localStorage.setItem(
                                            "isoform.models.custom-mirror.v1",
                                            customMirror,
                                        );
                                        setSourceSaveFailed(false);
                                    } catch {
                                        setSourceSaveFailed(true);
                                    }
                                }}
                            >
                                {t("models.saveFolder")}
                            </Button>
                        </div>
                        </>
                        )}
                        {sourceSaveFailed && (
                            <p role="alert" className="text-xs text-error">
                                {t("models.sourceSaveFailed")}
                            </p>
                        )}
                    </div>
                    <Button
                        variant="link"
                        className="self-start p-0 h-auto"
                        onClick={() => setEvaluationOpen(true)}
                    >
                        {t("models.evaluation")}
                    </Button>
                    <DownloadProgressLine
                        detailed
                        downloads={downloads}
                        network={network}
                        onRetry={() => void refresh()}
                    />
                    <Button
                        className="self-end"
                        disabled={busy || !summary}
                        onClick={() => void review()}
                    >
                        {t("models.reviewDownloads")}
                    </Button>
                </>
            )}
        </>
    );
    return (
        <div className="flex flex-col flex-1 min-h-0">
            {settings ? (
                <section className="flex flex-col gap-3 py-4">
                    {content}
                </section>
            ) : (
                <>
                    <SetupBody>{content}</SetupBody>
                    <SetupFooter onBack={onBack}>
                        <DownloadProgressLine
                            downloads={downloads}
                            network={network}
                            onRetry={() => void refresh()}
                        />
                        {choices && allInstalled && (
                            <span className="text-[12.5px] text-muted">
                                {t("models.alreadyHere", {
                                    size: formatBytes(
                                        chosen.reduce(
                                            (total, item) =>
                                                total + (item.sizeBytes ?? 0),
                                            0,
                                        ),
                                    ),
                                })}
                            </span>
                        )}
                        {(!!missing.length || error) && (
                            <Button variant="outline" onClick={onNext}>
                                {t("models.skip")}
                            </Button>
                        )}
                        <Button
                            data-testid="onboarding-continue"
                            disabled={
                                busy ||
                                (!unavailable &&
                                    (!choices ||
                                        (missing.length > 0 && !summary)))
                            }
                            onClick={() =>
                                unavailable ? onNext?.() : void review()
                            }
                        >
                            {!missing.length
                                ? t("actions.continue")
                                : t("models.downloadContinue", {
                                      size: formatBytes(
                                          summary?.totalBytes ?? 0,
                                      ),
                                  })}
                        </Button>
                    </SetupFooter>
                </>
            )}
            <ConfirmDialog
                open={!!removeTarget}
                onOpenChange={(open) => {
                    if (!open) setRemoveTarget(null);
                }}
                title={t("models.removeTitle", {
                    name: removeTarget ? modelName(removeTarget, t) : "",
                })}
                description={t(
                    removeTarget?.job === "privacy"
                        ? "models.removePrivacyDescription"
                        : removeTarget?.job === "memory"
                          ? "models.removeMemoryDescription"
                          : "models.removeDescription",
                    {
                        name: removeTarget ? modelName(removeTarget, t) : "",
                        size:
                            removeTarget?.sizeBytes == null
                                ? t("models.unknownSize")
                                : formatBytes(removeTarget.sizeBytes),
                    },
                )}
                confirmLabel={t("models.remove")}
                variant="danger"
                onConfirm={() => {
                    if (removeTarget)
                        void act(() => RemoveModel(removeTarget.id));
                }}
            />
            <Dialog open={evaluationOpen} onOpenChange={setEvaluationOpen}>
                <DialogContent
                    className="w-[900px] max-w-[calc(100vw-32px)] p-6"
                    aria-describedby={undefined}
                >
                    <DialogTitle>{t("models.evaluationTitle")}</DialogTitle>
                    <div className="overflow-auto py-4">
                        <MarkdownContent content={modelEvaluationTable(t)} />
                    </div>
                    <Button
                        variant="outline"
                        className="self-end"
                        onClick={() => setEvaluationOpen(false)}
                    >
                        {t("models.evaluationClose")}
                    </Button>
                </DialogContent>
            </Dialog>
            <Dialog
                open={!!plan}
                onOpenChange={(open) => {
                    if (!open && !busy) setPlan(null);
                }}
            >
                <DialogContent
                    className="w-[560px] max-w-[calc(100vw-32px)] max-h-[92dvh]"
                    onEscapeKeyDown={(event) => event.stopPropagation()}
                >
                    {plan && (
                        <>
                            <div className="px-[22px] pt-4 pb-2 flex flex-col gap-2.5">
                                <DialogTitle>
                                    {t("models.sheetTitle", {
                                        count: plan.items.length,
                                    })}
                                </DialogTitle>
                                <DialogDescription className="text-[15px] font-semibold text-ink">
                                    {t("models.diskSummary", {
                                        size: formatBytes(plan.totalBytes),
                                        free: formatBytes(plan.freeBytes),
                                    })}
                                </DialogDescription>
                                <div className="flex items-center gap-2 text-[12.5px] text-muted">
                                    <span>{t("models.saveTo")}</span>
                                    {editingFolder ? (
                                        <input
                                            aria-label={t("models.folder")}
                                            value={folder}
                                            onChange={(event) =>
                                                setFolder(event.target.value)
                                            }
                                            className="flex-1 min-w-0 border border-border rounded-md px-2 bg-background text-ink"
                                        />
                                    ) : (
                                        <code
                                            className="flex-1 truncate text-ink"
                                            title={folder}
                                        >
                                            {folder}
                                        </code>
                                    )}
                                    <Button
                                        variant="quiet"
                                        size="sm"
                                        disabled={busy}
                                        onClick={() =>
                                            editingFolder
                                                ? void replan(folder)
                                                : setEditingFolder(true)
                                        }
                                    >
                                        {t(
                                            editingFolder
                                                ? "models.saveFolder"
                                                : "models.changeFolder",
                                        )}
                                    </Button>
                                </div>
                            </div>
                            <div className="px-[22px] pb-4 overflow-y-auto flex flex-col gap-3">
                                <ModelNetworkNotice network={network} />
                                <DownloadProgressLine
                                    detailed
                                    downloads={downloads}
                                    network={network}
                                    onRetry={() => void refresh()}
                                />
                                <div className="setup-list">
                                    {plan.items.map((item) => {
                                        const source =
                                            item.sources.find(
                                                (source) =>
                                                    source.id ===
                                                    sources[item.id],
                                            ) ?? item.sources[0];
                                        return (
                                            <div
                                                key={item.id}
                                                className="px-3.5 py-2.5 border-t first:border-t-0 border-border-subtle flex flex-col gap-1"
                                            >
                                                <div className="flex justify-between gap-2 text-[12.5px]">
                                                    <span className="font-semibold">
                                                        {modelName(item, t)}
                                                    </span>
                                                    <span className="text-muted">
                                                        {t("models.itemSize", {
                                                            size: formatBytes(
                                                                item.sizeBytes ??
                                                                    0,
                                                            ),
                                                            count: item.files
                                                                .length,
                                                        })}
                                                    </span>
                                                </div>
                                                <div className="flex items-center gap-2 text-xs text-muted">
                                                    <span>
                                                        {t("models.source")}
                                                    </span>
                                                    <Select
                                                        value={source?.id}
                                                        disabled={busy}
                                                        onValueChange={(id) => {
                                                            const nextSources =
                                                                {
                                                                    ...sources,
                                                                    [item.id]:
                                                                        id,
                                                                };
                                                            void changeSources(
                                                                nextSources,
                                                            );
                                                        }}
                                                    >
                                                        <SelectTrigger
                                                            className="w-auto max-w-[160px] h-6 text-xs px-2"
                                                            aria-label={t(
                                                                "models.sourceFor",
                                                                {
                                                                    name: item.name,
                                                                },
                                                            )}
                                                        >
                                                            <SelectValue />
                                                        </SelectTrigger>
                                                        <SelectContent>
                                                            {item.sources.map(
                                                                (source) => (
                                                                    <SelectItem
                                                                        key={
                                                                            source.id
                                                                        }
                                                                        value={
                                                                            source.id
                                                                        }
                                                                    >
                                                                        {modelSourceName(
                                                                            source.host,
                                                                            source.name,
                                                                            t,
                                                                        )}
                                                                    </SelectItem>
                                                                ),
                                                            )}
                                                            {mirrorIsValid &&
                                                                !item.sources.some(
                                                                    (source) =>
                                                                        source.id ===
                                                                        customMirror,
                                                                ) && (
                                                                    <SelectItem
                                                                        value={
                                                                            customMirror
                                                                        }
                                                                    >
                                                                        {t(
                                                                            "models.customMirror",
                                                                        )}
                                                                    </SelectItem>
                                                                )}
                                                        </SelectContent>
                                                    </Select>
                                                    <span
                                                        className="truncate text-[11.5px]"
                                                        title={source?.url}
                                                    >
                                                        {source?.host}
                                                    </span>
                                                    <a
                                                        className="ml-auto whitespace-nowrap text-xs"
                                                        href={item.licence.url}
                                                        target="_blank"
                                                        rel="noreferrer"
                                                    >
                                                        {item.licence.name}
                                                    </a>
                                                </div>
                                            </div>
                                        );
                                    })}
                                </div>
                                <p className="setup-note">
                                    {t("models.sourceNote")}
                                </p>
                                {plan.items
                                    .filter(
                                        (item) => item.licence.nonCommercial,
                                    )
                                    .map((item) => (
                                        <div
                                            key={item.id}
                                            className="setup-callout waiting"
                                        >
                                            <p className="setup-description">
                                                {t("models.nonCommercial", {
                                                    name: item.name,
                                                    licence: item.licence.name,
                                                })}
                                            </p>
                                        </div>
                                    ))}
                                {!plan.enoughSpace && (
                                    <p
                                        role="alert"
                                        className="text-sm text-error"
                                    >
                                        {t(
                                            plan.shortfallBytes > 0
                                                ? "models.shortfall"
                                                : "models.spaceForExtraction",
                                            {
                                                size: formatBytes(
                                                    plan.shortfallBytes,
                                                ),
                                            },
                                        )}
                                    </p>
                                )}
                                {actionError && (
                                    <p
                                        role="alert"
                                        className="text-xs text-error"
                                    >
                                        {t(
                                            editingFolder
                                                ? "models.folderUnavailable"
                                                : !planValid
                                                  ? "models.sourcePlanFailed"
                                                  : "models.startFailed",
                                        )}
                                        {!planValid && !editingFolder && (
                                            <Button
                                                variant="quiet"
                                                size="sm"
                                                disabled={busy}
                                                onClick={() =>
                                                    void changeSources(sources)
                                                }
                                            >
                                                {t("models.retry")}
                                            </Button>
                                        )}
                                    </p>
                                )}
                            </div>
                            <div className="border-t border-border-subtle px-[22px] py-4 flex justify-end gap-2">
                                <Button
                                    variant="outline"
                                    disabled={busy}
                                    onClick={() => setPlan(null)}
                                >
                                    {t("models.cancel")}
                                </Button>
                                <Button
                                    disabled={
                                        busy ||
                                        !planValid ||
                                        editingFolder ||
                                        !plan.enoughSpace
                                    }
                                    onClick={() => void confirm()}
                                >
                                    {t(
                                        plan.items.some(
                                            (item) =>
                                                item.licence.nonCommercial,
                                        )
                                            ? "models.understandDownload"
                                            : "models.download",
                                        { size: formatBytes(plan.totalBytes) },
                                    )}
                                </Button>
                            </div>
                        </>
                    )}
                </DialogContent>
            </Dialog>
        </div>
    );
}

function usePlanSummary(
    request: ModelDownloadRequest | null,
    choices: ModelChoices | null,
    update: (plan: ModelDownloadPlan | null) => void,
) {
    useEffect(() => {
        let live = true;
        update(null);
        if (request && choices)
            void planSetupDownloads(request, choices)
                .then((plan) => {
                    if (live) update(plan);
                })
                .catch(() => {});
        return () => {
            live = false;
        };
    }, [request, update]);
}
