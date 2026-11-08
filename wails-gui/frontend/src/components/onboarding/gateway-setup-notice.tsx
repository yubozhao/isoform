import { useState } from "react";
import { useTranslation } from "react-i18next";
import { ReconnectModelGateway } from "@/lib/wails-shim-app";
import type { SetupAgentProbe } from "@/lib/setup-backend";
import { ToolDownloadSheet } from "./tool-download-sheet";
import { formatModelBytes } from "./v2/models";
import { Button } from "@/components/ui/button";

export function GatewaySetupNotice({ gateway, onReady, compact = false }: {
    gateway: SetupAgentProbe["magpie"];
    onReady: () => void;
    compact?: boolean;
}) {
    const { t } = useTranslation("onboarding");
    const [open, setOpen] = useState(false);
    const [downloaded, setDownloaded] = useState(false);
    const [connecting, setConnecting] = useState(false);
    const [failed, setFailed] = useState(false);
    const installed = gateway.installed || downloaded;

    const connect = async () => {
        if (connecting) return;
        setConnecting(true);
        setFailed(false);
        try {
            await ReconnectModelGateway();
            onReady();
        } catch {
            setFailed(true);
        } finally {
            setConnecting(false);
        }
    };

    return (
        <div className={compact ? "flex flex-col items-start gap-2 py-2" : "gateway-setup"} data-testid="setup-gateway">
            <p className={compact ? `settings-row-desc${failed ? " text-copper" : ""}` : `fine wide${failed ? " error" : ""}`} role={failed ? "alert" : "status"}>
                {t(failed ? "v2.ai.gatewayConnectFailed" : installed ? "v2.ai.gatewayInstalled" : !gateway.downloadable ? "v2.ai.gatewayUnsupported" : "v2.ai.gatewayMissing")}
            </p>
            <Button className={compact ? undefined : "btn primary"} type="button" disabled={connecting || (!installed && !gateway.downloadable)}
                onClick={() => installed ? void connect() : setOpen(true)}>
                {t(connecting ? "v2.ai.gatewayConnecting" : installed ? "v2.ai.gatewayConnect" : !gateway.downloadable ? "v2.ai.gatewayUnavailable" : "v2.ai.gatewayDownload", {
                    size: formatModelBytes(gateway.downloadBytes ?? 0),
                })}
            </Button>
            <ToolDownloadSheet open={open} onOpenChange={setOpen} toolId="model-gateway" onInstalled={() => {
                setDownloaded(true);
                void connect();
            }} />
        </div>
    );
}
