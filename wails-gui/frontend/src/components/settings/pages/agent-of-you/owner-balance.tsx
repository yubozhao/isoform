// Balance and payouts (o21 to o24): Available and On its way, the four payout
// states in their own words, and one list of everything that moved the balance.
import { useState } from "react";
import { useTranslation } from "react-i18next";
import type { TFunction } from "i18next";
import { Button } from "@/components/ui/button";
import { SettingsCard } from "@/components/settings/settings-primitives";
import { SettingsFailureLine, useSettingsFailure } from "@/components/settings/settings-failure";
import { openAgentOwnerPayoutOnboarding, type AgentOwnerMovement } from "@/lib/agent-owner";
import { AOY_PAYOUT_MINIMUM_CENTS, AOY_TERMS_VERSION, formatDay, money, useOwnerSnapshot } from "./aoy-shared";
import { AOY_LINE, AoyPlaceholder, AoyTile } from "./aoy-ui";
import { PayoutTermsDialog } from "./owner-home";

function movementLabel(t: TFunction<"settings">, line: AgentOwnerMovement): string {
    const kind = line.kind.toLowerCase();
    if (kind.includes("payout")) return t("aoy.lgPayout", { fee: money(line.cardFeeCents) });
    if (kind.includes("free")) return t("aoy.lgFree", { n: 1 });
    if (kind.includes("expired")) return t("aoy.lgExpired");
    if (kind.includes("refund") || kind.includes("dispute")) return t("aoy.lgRefund");
    if (kind.includes("adjust") || kind.includes("change")) return t("aoy.lgChange");
    return t("aoy.lgJob");
}

function movementCents(line: AgentOwnerMovement): number {
    if (line.ownerShareCents) return line.ownerShareCents;
    if (line.debtCents) return -line.debtCents;
    return 0;
}

export function BalanceView() {
    const { t, i18n } = useTranslation("settings");
    const balance = useOwnerSnapshot("balance");
    const payouts = useOwnerSnapshot("payouts");
    const failure = useSettingsFailure();
    const [termsOpen, setTermsOpen] = useState(false);
    if (!balance.loaded || !payouts.loaded) return <AoyPlaceholder rows={4} testId="aoy-balance-placeholder" />;

    const data = balance.data;
    const status = payouts.data?.status ?? "not_started";
    const total = data?.balanceCents ?? payouts.data?.balanceCents ?? 0;
    const owed = total < 0 ? -total : data?.owedCents ?? 0;
    const lines = data?.lines ?? [];
    const continueStripe = async () => {
        failure.clear();
        try {
            await openAgentOwnerPayoutOnboarding(AOY_TERMS_VERSION);
        } catch {
            failure.fail(t("yourAgent.browserFailed"), () => void continueStripe());
        }
    };

    const figures = status === "enabled" ? (
        <div className="grid grid-cols-2 gap-3">
            <AoyTile value={`$${money(data?.availableCents ?? Math.max(total, 0))}`} label={t("aoy.balAvail")} />
            {data?.onTheWayCents != null && <AoyTile value={`$${money(data.onTheWayCents)}`} label={t("aoy.balPending")} />}
        </div>
    ) : (
        <div className="grid grid-cols-2 gap-3">
            <AoyTile value={`${total < 0 ? "−" : ""}$${money(total)}`} label={t("aoy.balYours")} />
        </div>
    );

    return (
        <div className="flex flex-col gap-4" data-testid={`aoy-balance-${status}`}>
            {figures}
            {owed > 0 && <p className="text-sm text-copper" data-testid="aoy-owed">{t("aoy.balOwed", { amt: money(owed) })}</p>}

            {status === "not_started" && (
                <SettingsCard title={t("aoy.balSetT")} description={t("aoy.balSetL")}>
                    <Button onClick={() => setTermsOpen(true)}>{t("aoy.balSetBtn")}</Button>
                </SettingsCard>
            )}
            {(status === "pending" || status === "restricted") && (
                <SettingsCard title={t("aoy.balNeedsT")}>
                    <Button onClick={() => void continueStripe()}>{t("aoy.balNeedsBtn")}</Button>
                </SettingsCard>
            )}
            {status === "unsupported" && <p className="settings-card-desc">{t("aoy.balCountry")}</p>}
            {status === "enabled" && (
                <p className={AOY_LINE}>
                    {(payouts.data as { last4?: string } | null)?.last4
                        ? t("aoy.balTo", { last4: (payouts.data as { last4?: string }).last4, min: money(AOY_PAYOUT_MINIMUM_CENTS) })
                        : t("aoy.balBelow", { min: money(AOY_PAYOUT_MINIMUM_CENTS) })}
                </p>
            )}
            <SettingsFailureLine failure={failure.failure} />

            {lines.length > 0 && (
                <SettingsCard title={t("aoy.ledger")}>
                    <div className="flex flex-col" data-testid="aoy-ledger">
                        {lines.map((line) => {
                            const cents = movementCents(line);
                            return (
                                <div key={line.id} className="settings-row py-2.5">
                                    <div className="flex min-w-0 flex-col">
                                        <span className="settings-row-title">{movementLabel(t, line)}</span>
                                        <span className="settings-row-desc">{formatDay(line.createdAt, i18n.language)}</span>
                                    </div>
                                    <span className={`text-sm tabular-nums ${cents < 0 ? "text-muted" : "text-ink"}`}>{cents < 0 ? "−" : "+"}${money(cents)}</span>
                                </div>
                            );
                        })}
                    </div>
                </SettingsCard>
            )}
            <PayoutTermsDialog open={termsOpen} onOpenChange={setTermsOpen} />
        </div>
    );
}
