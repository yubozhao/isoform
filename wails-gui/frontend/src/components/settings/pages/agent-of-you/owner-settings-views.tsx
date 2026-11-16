// Rate, speed and effort (o07, o08) and Your say in each job (o12).
// Each change is sent as the full settings and applies to the next job.
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { Button } from "@/components/ui/button";
import { Switch } from "@/components/ui/switch";
import { SettingsCard, SettingsRow } from "@/components/settings/settings-primitives";
import { SettingsFailureLine, useSettingsFailure } from "@/components/settings/settings-failure";
import {
    agentOwnerAction,
    type AgentOwnerSettings,
    type AgentOwnerSettingsInput,
    type AgentOwnerSpeed,
    type AgentOwnerStatus,
} from "@/lib/agent-owner";
import {
    AOY_MINIMUM_CHARGE_CENTS,
    EFFORTS,
    SPEEDS,
    effortLabel,
    exampleJob,
    money,
    speedLabel,
    useOwnerSnapshot,
} from "./aoy-shared";
import { AOY_FIELD, AOY_LABEL, AOY_LINE, AoyDialog, AoyPlaceholder } from "./aoy-ui";

function inputOf(settings: AgentOwnerSettings, change: Partial<AgentOwnerSettingsInput>): AgentOwnerSettingsInput {
    const next: AgentOwnerSettingsInput = {
        rateCentsPerMinute: settings.rateCentsPerMinute ?? 0,
        speed: settings.speed,
        effort: settings.effort,
        approveJobs: settings.approveJobs,
        reviewResults: settings.reviewResults,
        ...change,
    };
    // The delivered-notification setting is sent only where the server has it.
    if (settings.deliveredNotification === undefined && change.deliveredNotification === undefined) delete next.deliveredNotification;
    return next;
}

function useSaveSettings(settings: AgentOwnerSettings) {
    const { t } = useTranslation("settings");
    const failure = useSettingsFailure();
    const [busy, setBusy] = useState(false);
    const save = async (change: Partial<AgentOwnerSettingsInput>) => {
        failure.clear();
        setBusy(true);
        try {
            await agentOwnerAction("settings", "", "", inputOf(settings, change));
        } catch {
            failure.fail(t("overview.changeFailed"), () => void save(change));
        } finally {
            setBusy(false);
        }
    };
    return { save, busy, failure: failure.failure };
}

function find(speeds: AgentOwnerSpeed[], speed: string, effort: string): AgentOwnerSpeed | undefined {
    return speeds.find((item) => item.speed === speed && item.effort === effort);
}

function Segmented<T extends string>({ value, options, label, onChange, disabled }: { value: T; options: { value: T; label: string; sub?: string }[]; label: string; onChange: (value: T) => void; disabled?: boolean }) {
    return (
        <div role="radiogroup" aria-label={label} className="grid gap-1 rounded-xl bg-ink/[0.04] p-1" style={{ gridTemplateColumns: `repeat(${options.length}, minmax(0, 1fr))` }}>
            {options.map((option) => {
                const selected = option.value === value;
                return (
                    <button
                        key={option.value}
                        type="button"
                        role="radio"
                        aria-checked={selected}
                        disabled={disabled}
                        onClick={() => onChange(option.value)}
                        className={`flex flex-col items-center gap-0.5 rounded-lg px-3 py-2 text-sm transition-colors ${selected ? "bg-panel font-semibold text-ink shadow-soft" : "text-muted hover:text-ink"}`}
                    >
                        <span>{option.label}</span>
                        {option.sub && <span className="text-xs font-normal text-muted">{option.sub}</span>}
                    </button>
                );
            })}
        </div>
    );
}

export function RateView({ status }: { status: AgentOwnerStatus }) {
    const { t } = useTranslation("settings");
    const speeds = useOwnerSnapshot("speeds");
    const settings = status.settings;
    const { save, busy, failure } = useSaveSettings(settings);
    const [rateText, setRateText] = useState(settings.rateCentsPerMinute != null ? money(settings.rateCentsPerMinute) : "");
    const [ask, setAsk] = useState<{ speed: string; effort: string; cost: number; min: number } | null>(null);
    useEffect(() => {
        if (settings.rateCentsPerMinute != null) setRateText(money(settings.rateCentsPerMinute));
    }, [settings.rateCentsPerMinute]);

    const list = speeds.data ?? [];
    const current = find(list, settings.speed, settings.effort);
    const rate = settings.rateCentsPerMinute ?? 0;

    const choose = (speed: string, effort: string) => {
        const option = find(list, speed, effort);
        if (option && rate > 0 && option.minimumRateCents > rate) {
            setAsk({ speed, effort, cost: option.typicalCentsPerMinute, min: option.minimumRateCents });
            return;
        }
        void save({ speed, effort });
    };
    const commitRate = () => {
        const cents = Math.round(Number.parseFloat(rateText.replace(/[^0-9.]/g, "")) * 100);
        if (!Number.isFinite(cents) || cents <= 0) {
            setRateText(settings.rateCentsPerMinute != null ? money(settings.rateCentsPerMinute) : "");
            return;
        }
        if (cents === settings.rateCentsPerMinute) return;
        if (current && cents < current.minimumRateCents) {
            setAsk({ speed: settings.speed, effort: settings.effort, cost: current.typicalCentsPerMinute, min: current.minimumRateCents });
            return;
        }
        void save({ rateCentsPerMinute: cents });
    };

    const example = current && rate > 0 ? exampleJob(rate, current.typicalCentsPerMinute) : null;
    return (
        <div className="flex flex-col gap-5" data-testid="aoy-rate">
            <section className="flex flex-col gap-2">
                <h2 className="settings-card-title">{t("aoy.sRate")}</h2>
                <div className="flex items-center gap-2">
                    <label className={`${AOY_FIELD} flex h-10 w-32 items-center gap-1 text-base`}>
                        <span className="text-ink">$</span>
                        <input
                            aria-label={t("aoy.sRate")}
                            inputMode="decimal"
                            className="min-w-0 flex-1 bg-transparent font-semibold focus:outline-none"
                            value={rateText}
                            disabled={busy}
                            onChange={(event) => setRateText(event.target.value)}
                            onBlur={commitRate}
                            onKeyDown={(event) => {
                                if (event.key === "Enter") (event.target as HTMLInputElement).blur();
                            }}
                        />
                    </label>
                    <span className="text-sm text-muted">{t("aoy.perMin")}</span>
                </div>
                <p className={AOY_LINE}>{t("aoy.rateL", { min: money(AOY_MINIMUM_CHARGE_CENTS) })}</p>
            </section>

            <section className="flex flex-col gap-2">
                <h2 className="settings-card-title">{t("aoy.sSpeed")}</h2>
                {!speeds.loaded ? (
                    <AoyPlaceholder rows={1} />
                ) : (
                    <Segmented
                        label={t("aoy.sSpeed")}
                        value={settings.speed}
                        disabled={busy}
                        onChange={(speed) => choose(speed, settings.effort)}
                        options={SPEEDS.map((speed) => {
                            const option = find(list, speed, settings.effort);
                            return { value: speed, label: speedLabel(t, speed), sub: option ? t("aoy.spCost", { c: money(option.typicalCentsPerMinute) }) : undefined };
                        })}
                    />
                )}
            </section>

            <section className="flex flex-col gap-2">
                <h2 className="settings-card-title">{t("aoy.sEffort")}</h2>
                <Segmented
                    label={t("aoy.sEffort")}
                    value={settings.effort}
                    disabled={busy}
                    onChange={(effort) => choose(settings.speed, effort)}
                    options={EFFORTS.map((effort) => ({ value: effort, label: effortLabel(t, effort) }))}
                />
                {current && (
                    <p className={AOY_LINE} data-testid="aoy-floor">
                        {t("aoy.costL", { speed: speedLabel(t, current.speed), effort: effortLabel(t, current.effort), c: money(current.typicalCentsPerMinute), m: money(current.minimumRateCents) })}
                    </p>
                )}
            </section>

            {example && (
                <div className="settings-card settings-card--padded flex flex-col gap-2" data-testid="aoy-example">
                    <span className={AOY_LABEL}>{t("aoy.exT", { rate: money(rate) })}</span>
                    <div className="grid grid-cols-[1fr_auto] gap-y-1.5 text-sm tabular-nums">
                        <span className="text-ink">{t("aoy.exPay")}</span>
                        <span className="text-right text-ink">${money(example.pay)}</span>
                        <span className="text-muted">{t("aoy.exModel")}</span>
                        <span className="text-right text-muted">−${money(example.model)}</span>
                        <span className="text-muted">{t("aoy.exFee")}</span>
                        <span className="text-right text-muted">−${money(example.fee)}</span>
                        <span className="text-muted">{t("aoy.exIso")}</span>
                        <span className="text-right text-muted">−${money(example.isoform)}</span>
                        <span className="border-t border-border pt-2 font-semibold text-ink">{t("aoy.exYou")}</span>
                        <span className="border-t border-border pt-2 text-right font-semibold text-ink">${money(example.keep)}</span>
                    </div>
                </div>
            )}
            <SettingsFailureLine failure={failure} />
            <p className={AOY_LINE}>{t("aoy.applyNext")}</p>

            <AoyDialog
                open={ask !== null}
                onOpenChange={(open) => !open && setAsk(null)}
                title={ask ? t("aoy.minT", { speed: speedLabel(t, ask.speed), effort: effortLabel(t, ask.effort) }) : ""}
                description={ask ? t("aoy.minL", { c: money(ask.cost), m: money(ask.min) }) : undefined}
                testId="aoy-min-dialog"
                footer={
                    <>
                        <Button variant="ghost" onClick={() => {
                            setAsk(null);
                            setRateText(settings.rateCentsPerMinute != null ? money(settings.rateCentsPerMinute) : "");
                        }}>{t("aoy.cancel")}</Button>
                        <Button
                            disabled={busy}
                            onClick={() => {
                                if (!ask) return;
                                const choice = ask;
                                setAsk(null);
                                void save({ speed: choice.speed, effort: choice.effort, rateCentsPerMinute: choice.min });
                            }}
                        >
                            {ask ? t("aoy.minRaise", { m: money(ask.min) }) : ""}
                        </Button>
                    </>
                }
            />
        </div>
    );
}

export function OptionsView({ status }: { status: AgentOwnerStatus }) {
    const { t } = useTranslation("settings");
    const settings = status.settings;
    const { save, busy, failure } = useSaveSettings(settings);
    return (
        <div className="flex flex-col gap-4" data-testid="aoy-opts">
            <SettingsCard>
                <SettingsRow
                    title={t("aoy.oApprove")}
                    description={settings.approveJobs ? t("aoy.oApproveOn") : t("aoy.oApproveOff")}
                    control={<Switch aria-label={t("aoy.oApprove")} checked={settings.approveJobs} disabled={busy} onCheckedChange={(approveJobs) => void save({ approveJobs })} />}
                />
                <SettingsRow
                    title={t("aoy.oReview")}
                    description={settings.reviewResults ? t("aoy.oReviewOn") : t("aoy.oReviewOff")}
                    control={<Switch aria-label={t("aoy.oReview")} checked={settings.reviewResults} disabled={busy} onCheckedChange={(reviewResults) => void save({ reviewResults })} />}
                />
                {settings.deliveredNotification !== undefined && (
                    <SettingsRow
                        title={t("aoy.oNote")}
                        description={t("aoy.oNoteL")}
                        control={<Switch aria-label={t("aoy.oNote")} checked={!!settings.deliveredNotification} disabled={busy} onCheckedChange={(deliveredNotification) => void save({ deliveredNotification })} />}
                    />
                )}
            </SettingsCard>
            <SettingsFailureLine failure={failure} />
            <p className={AOY_LINE}>{t("aoy.applyNext")}</p>
        </div>
    );
}
