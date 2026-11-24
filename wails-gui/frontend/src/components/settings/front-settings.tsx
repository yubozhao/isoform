import { useCallback, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { Switch } from "@/components/ui/switch";
import { SettingsCard, SettingsRow } from "@/components/settings/settings-primitives";
import { useSettingsChange } from "@/components/settings/settings-failure";
import { SettingReadError } from "@/components/settings/pages/setting-read-error";
import { GetFrontStatus, SetFrontEnabled, type FrontStatus } from "@/lib/wails-shim-app";
import { BrowserOpenURL } from "../../../wailsjs/runtime/runtime";

import { LETS_ENCRYPT_AGREEMENT_URL } from "@/components/agents/connect-link";

/**
 * Agents reach this computer (front_service.go): a public address for the
 * person's own agents, served through the Isoform front with TLS ending on
 * this computer. Turning it on gets a certificate from Let's Encrypt, which
 * means accepting their subscriber agreement, so the row says so and links
 * to it. Certificates for the address that this computer never asked for
 * are listed here (front_ct_watch.go).
 */
export function FrontSettings() {
    const { t } = useTranslation("settings");
    const [status, setStatus] = useState<FrontStatus | null>(null);
    const [readFailed, setReadFailed] = useState(false);
    const { failure, run } = useSettingsChange(t("front.toggleFailed"));

    const read = useCallback(() => {
        setReadFailed(false);
        GetFrontStatus()
            .then(setStatus)
            .catch(() => setReadFailed(true));
    }, []);
    useEffect(read, [read]);

    const toggle = (next: boolean) => {
        const before = status;
        void run(() => SetFrontEnabled(next), {
            apply: () => setStatus((s) => ({ ...(s ?? { running: false, hostname: "" }), enabled: next })),
            revert: () => setStatus(before),
            onDone: read,
        });
    };

    const alerts = status?.unexpectedCertificates ?? [];
    return (
        <SettingsCard title={t("front.title")} description={t("front.description")}>
            <SettingsRow
                title={t("front.switch.title")}
                failure={failure}
                description={
                    <>
                        {t("front.switch.agreement")}{" "}
                        <button
                            type="button"
                            className="text-accent hover:underline"
                            onClick={() => BrowserOpenURL(LETS_ENCRYPT_AGREEMENT_URL)}
                        >
                            {t("front.switch.agreementLink")}
                        </button>
                    </>
                }
                control={
                    readFailed ? (
                        <SettingReadError retry={read} />
                    ) : (
                        <Switch
                            checked={Boolean(status?.enabled)}
                            disabled={status === null}
                            onCheckedChange={toggle}
                            aria-label={t("front.switch.title")}
                        />
                    )
                }
            />
            {status?.enabled && (
                <SettingsRow
                    title={t("front.address.title")}
                    description={status.running ? status.hostname : t("front.address.notRunning")}
                />
            )}
            {alerts.length > 0 && (
                <div role="alert" data-front-alerts className="rounded-lg border border-border bg-panel px-3 py-2 text-[13px] text-ink">
                    <p className="font-medium text-copper">{t("front.alerts.title", { count: alerts.length })}</p>
                    <p className="mt-1 text-muted">{t("front.alerts.body")}</p>
                    <ul className="mt-2 flex flex-col gap-1 text-[12.5px] text-muted">
                        {alerts.map((a) => (
                            <li key={a.id}>{t("front.alerts.item", { issuer: a.issuer || t("front.alerts.unknownIssuer"), date: a.notBefore ? a.notBefore.slice(0, 10) : "" })}</li>
                        ))}
                    </ul>
                </div>
            )}
        </SettingsCard>
    );
}
