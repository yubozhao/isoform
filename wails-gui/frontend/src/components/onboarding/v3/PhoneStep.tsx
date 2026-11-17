import { useTranslation } from "react-i18next";
import { QRCodeSVG } from "qrcode.react";
import { Check } from "lucide-react";
import { Button } from "@/components/ui/button";
import { usePhonePairing } from "./usePhonePairing";

// Step 5 · Your phone and watch (computer setup v1.0.0 rule 8): the code the
// phone scans (phone-setup v1.1.0's Scan the code), where to get the app,
// the watch line, and a line while it waits. A pairing is approved here.
export function PhoneStep() {
    const { t } = useTranslation("onboarding");
    const { card, relayUnreachable, pairFailed, confirm, retry } = usePhonePairing();

    return (
        <div className="sv3-step" data-testid="setup-v3-phone">
            <h2 className="sv3-step-title">{t("setup.step5")}</h2>
            <p className="sv3-body">{t("setup.phoneLead")}</p>
            <div className="sv3-pair">
                <div className="sv3-code" aria-label={t("v2.reaches.codeLabel")}>
                    {card.kind === "code" && card.code ? (
                        <QRCodeSVG value={card.code} size={148} bgColor="transparent" fgColor="currentColor" />
                    ) : card.kind === "paired" ? (
                        <Check size={32} aria-hidden />
                    ) : null}
                </div>
                <div className="sv3-pair-words">
                    {card.kind === "paired" ? (
                        <>
                            <p className="sv3-strong">{t("v2.reaches.paired", { phone: card.phone })}</p>
                            <p className="sv3-quiet">{t("v2.reaches.pairedLine")}</p>
                        </>
                    ) : card.kind === "pending" ? (
                        <>
                            <p className="sv3-strong">{t("v2.reaches.pending", { phone: card.phone })}</p>
                            <p className="sv3-quiet">{t("v2.reaches.pendingCode", { code: card.code })}</p>
                            <div className="sv3-answers">
                                <Button size="sm" onClick={() => void confirm(card.pairingId, true)}>
                                    {t("v2.reaches.approve")}
                                </Button>
                                <Button size="sm" variant="outline" onClick={() => void confirm(card.pairingId, false)}>
                                    {t("v2.reaches.decline")}
                                </Button>
                            </div>
                        </>
                    ) : (
                        <>
                            <p className="sv3-strong">{t("setup.scan")}</p>
                            <p className="sv3-quiet">{t("setup.noApp")}</p>
                            <p className="sv3-quiet">{t("setup.watchLine")}</p>
                            <p className="sv3-quiet" data-testid="setup-v3-waiting">
                                {t("setup.waitingPhone")}
                            </p>
                        </>
                    )}
                    {relayUnreachable && (
                        <p className="sv3-warn" role="alert">
                            {t("v2.reaches.relayUnreachable")}{" "}
                            <Button variant="link" size="sm" onClick={retry}>
                                {t("v2.reaches.relayRetry")}
                            </Button>
                        </p>
                    )}
                    {pairFailed && (
                        <p className="sv3-warn" role="alert">
                            {t("v2.reaches.pairFailed")}
                        </p>
                    )}
                </div>
            </div>
        </div>
    );
}
