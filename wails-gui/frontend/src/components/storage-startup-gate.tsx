import { useEffect, useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import {
    GetStorageStartupStatus,
    type StorageStartupStatus,
} from "@/lib/wails-shim-app";
import { EventsEmit, Quit } from "../../wailsjs/runtime/runtime";
import { Button } from "@/components/ui/button";
import { afterPaint } from "@/lib/launch-perf";

// Do not mount App's hooks until storage has opened successfully. In
// particular, a newer schema must never look like an empty first-run account.
export function StorageStartupGate({ children }: { children: ReactNode }) {
    const { t } = useTranslation("app");
    const [status, setStatus] = useState<StorageStartupStatus | null>(null);

    useEffect(() => {
        let cancelled = false;
        let timer: ReturnType<typeof setTimeout> | undefined;
        const refresh = async () => {
            try {
                const result = await GetStorageStartupStatus();
                if (cancelled) return;
                setStatus(result);
                if (result.code === "checking") timer = setTimeout(refresh, 50);
            } catch {
                if (!cancelled) setStatus({ code: "storage_unavailable" });
            }
        };
        void refresh();
        return () => {
            cancelled = true;
            clearTimeout(timer);
        };
    }, []);

    useEffect(() => {
        if (status?.code !== "") return;
        // afterPaint also fires for a window that launched hidden, which gets
        // no animation frames: page snapshots and first reads wait for this.
        return afterPaint(() => EventsEmit("pages:first-paint"));
    }, [status?.code]);

    if (status?.code === "") return <>{children}</>;

    const newerSchema = status?.code === "schema_too_new";
    return (
        <main className="min-h-screen bg-background flex items-center justify-center p-6">
            <div className="w-full max-w-lg flex flex-col gap-4">
                {status === null || status.code === "checking" ? (
                    <p role="status" className="text-sm text-muted">
                        {t("storageStartup.checking")}
                    </p>
                ) : (
                    <div role="alert" className="flex flex-col gap-4">
                        <h1 className="text-xl font-semibold text-foreground">
                            {t(newerSchema ? "storageStartup.newerTitle" : "storageStartup.unavailableTitle")}
                        </h1>
                        <p className="text-sm text-muted leading-relaxed">
                            {newerSchema
                                ? t("storageStartup.newerDescription", {
                                    storedVersion: status.storedVersion,
                                    supportedVersion: status.supportedVersion,
                                })
                                : t("storageStartup.unavailableDescription")}
                        </p>
                        <Button className="self-start" onClick={() => Quit()}>
                            {t("storageStartup.quit")}
                        </Button>
                    </div>
                )}
            </div>
        </main>
    );
}
