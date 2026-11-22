import { useCallback, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { Check } from "lucide-react";
import {
    GetScreenRecordingRelaunch,
    GetSetupPermissions,
    OpenPermissionSettings,
    RelaunchApp,
    RequestSetupPermissions,
} from "@/lib/wails-shim-app";
import { RequestPermission } from "../../../../wailsjs/go/main/App";
import { EventsOn } from "../../../../wailsjs/runtime/runtime";
import type { SetupPermissions } from "@/lib/setup-backend";
import { Button } from "@/components/ui/button";
import { setMenuBarMark } from "../v2/native";
import { SEES_V3_ROWS, type SeesRow } from "./steps";
import { ModelsLine } from "./ModelsLine";

// Step 1 · What Isoform sees (computer setup v1.0.0 rule 4): one row per
// permission with what it gives; Allow… asks that row's macOS prompts, one
// at a time, and nothing else. Messages and agents' history are never asked.
// Usage counts are not asked here (setup-answers answer 2): they live in
// Settings › Your data, off until turned on.

export type SeesRowState = "off" | "asking" | "on" | "denied" | "later";

/** Notifications are asked once their row's Allow… is pressed; 3 s bounds a stuck prompt. */
const NOTIFICATION_ASK_LIMIT_MS = 3000;

/** A row's state from the saved answers: denied wins, then on when every prompt is answered. */
export function seesRowState(row: SeesRow, perms: SetupPermissions | null, notifications: boolean | null): SeesRowState {
    if (row.id === "notifications") {
        return notifications === true ? "on" : "off";
    }
    const states = row.keys.map((key) => perms?.answers.find((a) => a.key === key)?.state ?? "off");
    if (states.some((s) => s === "denied")) return "denied";
    if (states.every((s) => s === "unavailable")) return "later";
    if (states.every((s) => s === "on" || s === "onAfterReopen" || s === "unavailable")) return "on";
    return "off";
}

const SO_FAR_ROWS: Array<SeesRow["id"]> = ["calendar", "mail", "microphone", "screen"];

export function SeesStep() {
    const { t, i18n } = useTranslation("onboarding");
    const [perms, setPerms] = useState<SetupPermissions | null>(null);
    const [asking, setAsking] = useState<SeesRow["id"] | null>(null);
    const [failed, setFailed] = useState<"permissions" | "relaunch" | null>(null);
    const askingRef = useRef<SeesRow["id"] | null>(null);

    const read = useCallback(async () => {
        try {
            setPerms(await GetSetupPermissions());
        } catch {
            /* Rows stay as they were. */
        }
    }, []);

    useEffect(() => {
        void read();
        const off = EventsOn("setup:permissions", (p: SetupPermissions) => {
            setPerms(p);
            if (!p.running && askingRef.current && askingRef.current !== "notifications") {
                void finishRow(askingRef.current);
            }
        });
        const offError = EventsOn("setup:permissions-error", () => {
            askingRef.current = null;
            setAsking(null);
            setFailed("permissions");
        });
        const offRelaunch = EventsOn("relaunch:failed", () => setFailed("relaunch"));
        window.addEventListener("focus", read);
        return () => {
            off();
            offError();
            offRelaunch();
            window.removeEventListener("focus", read);
        };
        // finishRow only reads refs and stable bindings.
        // eslint-disable-next-line react-hooks/exhaustive-deps
    }, [read]);

    // A row is done: watching starts, and Screen Recording reopens Isoform
    // when macOS needs it; the reopen comes back to this step.
    const finishRow = useCallback(async (id: SeesRow["id"]) => {
        askingRef.current = null;
        setAsking(null);
        if (id !== "screen") return;
        void setMenuBarMark(true);
        const relaunch = await GetScreenRecordingRelaunch().catch(() => null);
        if (!relaunch?.needed) return;
        try {
            await RelaunchApp();
        } catch {
            setFailed("relaunch");
        }
    }, []);

    const allow = useCallback(
        async (row: SeesRow) => {
            if (askingRef.current) return;
            askingRef.current = row.id;
            setAsking(row.id);
            setFailed(null);
            if (row.id === "notifications") {
                let timer: number | undefined;
                await Promise.race([
                    RequestPermission("notifications").catch(() => false),
                    new Promise<false>((resolve) => {
                        timer = window.setTimeout(() => resolve(false), NOTIFICATION_ASK_LIMIT_MS);
                    }),
                ]);
                window.clearTimeout(timer);
                await read();
                askingRef.current = null;
                setAsking(null);
                return;
            }
            try {
                await RequestSetupPermissions(row.keys);
            } catch {
                askingRef.current = null;
                setAsking(null);
                setFailed("permissions");
            }
        },
        [read],
    );

    const notifications = typeof perms?.notificationGranted === "boolean" ? perms.notificationGranted : null;
    const states = SEES_V3_ROWS.map((row) => (asking === row.id ? "asking" : seesRowState(row, perms, notifications)));
    const nextIndex = states.findIndex((s) => s === "off");
    const seen = SEES_V3_ROWS.filter((row, i) => SO_FAR_ROWS.includes(row.id) && states[i] === "on").map((row) =>
        t(`setup.soFarItems.${row.id as "calendar" | "mail" | "microphone" | "screen"}`),
    );
    const what = seen.length
        ? new Intl.ListFormat(i18n.language, { style: "long", type: "conjunction" }).format(seen)
        : t("setup.soFarNothing");

    return (
        <div className="sv3-step" data-testid="setup-v3-sees">
            <h2 className="sv3-step-title">{t("setup.step1")}</h2>
            <ul className="sv3-rows">
                {SEES_V3_ROWS.map((row, i) => {
                    const state = states[i];
                    return (
                        <li key={row.id} className="sv3-row" data-testid={`setup-v3-row-${row.id}`} data-state={state}>
                            <div>
                                <p className="sv3-strong">{t(`setup.rows.${row.id}.name` as const)}</p>
                                <p className="sv3-quiet">{t(`setup.rows.${row.id}.line` as const)}</p>
                                {row.id === "microphone" && <ModelsLine micOn={state === "on"} />}
                            </div>
                            {state === "on" ? (
                                <span className="sv3-allowed">
                                    <Check size={14} aria-hidden />
                                    {t("setup.allowed")}
                                </span>
                            ) : state === "later" ? (
                                <span className="sv3-quiet">{t("setup.mailLater")}</span>
                            ) : state === "denied" ? (
                                <Button
                                    variant="outline"
                                    size="sm"
                                    onClick={() => void OpenPermissionSettings(row.keys[row.keys.length - 1]).catch(() => {})}
                                >
                                    {t("setup.openSettings")}
                                </Button>
                            ) : (
                                <Button
                                    variant={i === nextIndex ? "default" : "outline"}
                                    size="sm"
                                    disabled={asking !== null}
                                    onClick={() => void allow(row)}
                                    data-testid={`setup-v3-allow-${row.id}`}
                                >
                                    {t("setup.allow")}
                                </Button>
                            )}
                        </li>
                    );
                })}
            </ul>
            <p className="sv3-quiet" data-testid="setup-v3-so-far">
                {t("setup.soFar", { what })}
            </p>
            {failed && (
                <p className="sv3-warn" role="alert">
                    {failed === "relaunch" ? t("v2.sees.reopenFailed") : t("v2.sees.failed")}
                </p>
            )}
        </div>
    );
}
