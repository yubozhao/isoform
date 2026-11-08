import { useCallback, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import {
    GetScreenRecordingRelaunch,
    GetSetupPermissions,
    OpenPermissionSettings,
    RelaunchApp,
    RequestAllPermissions,
} from "@/lib/wails-shim-app";
import { EventsOn } from "../../../../../wailsjs/runtime/runtime";
import type { SetupPermissions } from "@/lib/setup-backend";
import { SetupIcon, type SetupIconId } from "../SetupIcons";
import { Run, reflow } from "../motion";
import type { SceneProps } from "../types";
import { TIMINGS } from "../timings";
import { setMenuBarMark } from "../native";

// Screen 3 (handoff §4, "Screen 3 · Sees"): a small screen swept by a
// violet line; private things blur to "Skipped" and what stays drops into
// "This Mac". Turn on asks every macOS prompt in turn, Screen Recording
// last, then Isoform reopens itself and comes back here with the rows On.
// Turn on only asks: the local models are chosen on screen 4 (v2.2.0 §2).

type RowId = "screen" | "calls" | "day" | "apps";
export const SEES_ROWS: Array<{ id: RowId; icon: SetupIconId; keys: string[] }> = [
    { id: "screen", icon: "i-screen", keys: ["screen_recording"] },
    { id: "calls", icon: "i-mic", keys: ["microphone", "system_audio"] },
    {
        id: "day",
        icon: "i-cal",
        keys: ["calendars", "reminders", "contacts", "automation_mail"],
    },
    { id: "apps", icon: "i-cursor", keys: ["accessibility"] },
];
const ANSWERED = new Set(["on", "onAfterReopen", "unavailable"]);

export type RowState = "off" | "on" | "denied";

/** A row is On when each of its prompts is answered yes (or couldn't be shown). */
const T = TIMINGS.sees;

export function rowStates(p: SetupPermissions | null): Record<RowId, RowState> {
    const out = {} as Record<RowId, RowState>;
    for (const row of SEES_ROWS) {
        const answers = row.keys.map(
            (key) => p?.answers.find((a) => a.key === key)?.state ?? "off",
        );
        out[row.id] = answers.some((s) => s === "denied")
            ? "denied"
            : answers.every((s) => ANSWERED.has(s))
              ? "on"
              : "off";
    }
    return out;
}

/** The sequence ran to the end: every key got an answer. */
export function sequenceDone(p: SetupPermissions | null) {
    return !!p && !p.running && p.answers.length > 0 && p.answers.every((a) => a.state !== "off");
}

const STILL_ON: SetupPermissions = {
    answers: SEES_ROWS.flatMap((r) => r.keys.map((key) => ({ key, state: "on" }))),
    running: false,
    resumeStep: "sees",
    next: 8,
};

export function SeesScene({ onNext, reduced, still, enter }: SceneProps) {
    const { t } = useTranslation("onboarding");
    const frozen = !!still;
    const [perms, setPerms] = useState<SetupPermissions | null>(
        still === "sees-on" ? STILL_ON : null,
    );
    // Rows flip On one at a time, at least 0.38 s apart, even when macOS
    // answers faster (a reopen, or prompts answered before).
    const [shown, setShown] = useState<Record<RowId, RowState>>(() =>
        rowStates(still === "sees-on" ? STILL_ON : null),
    );
    const [phase, setPhase] = useState<"off" | "busy" | "done">(
        still === "sees-on" ? "done" : "off",
    );
    const [failed, setFailed] = useState<"permissions" | "relaunch" | null>(null);
    const mini = useRef<HTMLDivElement>(null);
    const scan = useRef<HTMLDivElement>(null);
    const bank = useRef<HTMLSpanElement>(null);
    const pw = useRef<HTMLDivElement>(null);
    const n1 = useRef<HTMLSpanElement>(null);
    const n2 = useRef<HTMLSpanElement>(null);

    // The picture: the sweep passes in 2.3 s, rests 1.4 s, and repeats.
    useEffect(() => {
        const miniEl = mini.current;
        const scanEl = scan.current;
        if (!miniEl || !scanEl) return;
        const hide = [bank.current, pw.current];
        const notes = [n1.current, n2.current];
        const run = new Run(reduced);
        if (reduced || still) {
            miniEl.classList.add("in");
            hide.forEach((el) => el?.classList.add("hid"));
            notes.forEach((el) => el?.classList.add("show"));
            return () => run.cancel();
        }
        reflow(miniEl);
        miniEl.classList.add("in");
        run.go(async () => {
            await run.wait(T.start);
            for (;;) {
                scanEl.classList.add("on");
                await run.tween(T.sweep, (p) => {
                    const y = p * 128;
                    scanEl.style.transform = `translateY(${y}px)`;
                    if (y > 12) bank.current?.classList.add("hid");
                    if (y > 46) n1.current?.classList.add("show");
                    if (y > 58) n2.current?.classList.add("show");
                    if (y > 74) pw.current?.classList.add("hid");
                });
                scanEl.classList.remove("on");
                await run.wait(T.rest);
            }
        });
        return () => run.cancel();
    }, [reduced, still]);

    // What the sequence already answered: after the reopen this screen
    // comes back with the rows On and watching started.
    useEffect(() => {
        if (frozen) return;
        let live = true;
        void GetSetupPermissions()
            .then((p) => {
                if (!live) return;
                setPerms(p);
                if (sequenceDone(p)) {
                    setShown(rowStates(p));
                    setPhase("done");
                    void setMenuBarMark(true);
                }
            })
            .catch(() => {});
        const off = EventsOn("setup:permissions", (p: SetupPermissions) => {
            if (live) setPerms(p);
        });
        const offError = EventsOn("setup:permissions-error", () => {
            if (!live) return;
            setFailed("permissions");
            setPhase("off");
        });
        const offRelaunch = EventsOn("relaunch:failed", () => {
            if (live) setFailed("relaunch");
        });
        return () => {
            live = false;
            off();
            offError();
            offRelaunch();
        };
    }, [frozen]);

    // Flip rows toward what macOS answered, one every 0.38 s.
    useEffect(() => {
        if (frozen || phase === "off") return;
        const target = rowStates(perms);
        const next = SEES_ROWS.find((row) => shown[row.id] !== target[row.id]);
        if (!next) return;
        const run = new Run(reduced);
        run.go(async () => {
            await run.wait(reduced ? 0 : T.rowFlip);
            setShown((cur) => ({ ...cur, [next.id]: target[next.id] }));
        });
        return () => run.cancel();
    }, [perms, shown, phase, frozen, reduced]);

    const allShown = SEES_ROWS.every((row) => shown[row.id] === rowStates(perms)[row.id]);

    // The sequence finished: reopen when Screen Recording needs it, else
    // watching starts here.
    const finishing = useRef(false);
    useEffect(() => {
        if (frozen || phase !== "busy" || !sequenceDone(perms) || !allShown) return;
        if (finishing.current) return;
        finishing.current = true;
        const run = new Run(reduced);
        run.go(async () => {
            await run.wait(reduced ? 0 : T.reopen);
            const relaunch = await GetScreenRecordingRelaunch().catch(() => null);
            if (relaunch?.needed) {
                try {
                    await RelaunchApp();
                    return;
                } catch {
                    setFailed("relaunch");
                }
            }
            setPhase("done");
            void setMenuBarMark(true);
        });
        return () => {
            run.cancel();
            finishing.current = false;
        };
    }, [perms, phase, allShown, frozen, reduced]);

    const turnOn = useCallback(async () => {
        if (phase !== "off") return;
        setFailed(null);
        setPhase("busy");
        try {
            await RequestAllPermissions();
        } catch {
            setFailed("permissions");
            setPhase("off");
        }
    }, [phase]);

    const everythingOn = phase === "done" && SEES_ROWS.every((r) => shown[r.id] === "on");
    const firstDenied = (row: (typeof SEES_ROWS)[number]) =>
        row.keys.find((key) => perms?.answers.find((a) => a.key === key)?.state === "denied") ??
        row.keys[0];

    return (
        <section className={`scene ${enter}`} data-testid="setup-v2-sees">
            <h2 className="t">{t("v2.sees.title")}</h2>
            <p className="s">{t("v2.sees.line")}</p>
            <div className="ill">
                <div className="see">
                    <div className="mini" ref={mini}>
                        <div className="mbar">
                            <i />
                            <i />
                            <i />
                            <div className="tabs">
                                <span className="tab on">{t("v2.sees.deckTab")}</span>
                                <span className="tab skip" ref={bank}>
                                    <span className="c">{t("v2.sees.bankTab")}</span>
                                    <span className="lbl">
                                        <SetupIcon id="i-lock" />
                                        {t("v2.sees.skipped")}
                                    </span>
                                </span>
                            </div>
                        </div>
                        <div className="mbody">
                            <div className="doc">
                                <b>{t("v2.sees.deckTitle")}</b>
                                <span className="ln" style={{ width: "92%" }} />
                                <span className="ln" style={{ width: "74%" }} />
                                <span className="ln" style={{ width: "84%" }} />
                                <span className="ln" style={{ width: "58%" }} />
                            </div>
                            <div>
                                <div className="msgx">{t("v2.sees.priyaMessage")}</div>
                                <div className="pw skip" ref={pw}>
                                    <span className="c">{t("v2.sees.password")}</span>
                                    <span className="lbl">
                                        <SetupIcon id="i-lock" />
                                        {t("v2.sees.skipped")}
                                    </span>
                                </div>
                            </div>
                        </div>
                        <div className="scan" ref={scan} />
                    </div>
                    <div className="vault">
                        <span className="vb">
                            <SetupIcon id="i-lock" />
                            {t("v2.sees.thisMac")}
                        </span>
                        <span className="chip note" ref={n1}>
                            {t("v2.sees.noteHalcyon")}
                        </span>
                        <span className="chip note" ref={n2}>
                            {t("v2.sees.notePriya")}
                        </span>
                    </div>
                    <div className="perm">
                        {SEES_ROWS.map((row) => {
                            const state = shown[row.id];
                            const name = t(`v2.sees.rows.${row.id}`);
                            return (
                                <div
                                    key={row.id}
                                    className={`pr${state === "on" ? " on" : ""}`}
                                    data-testid={`setup-v2-row-${row.id}`}
                                    data-state={state}
                                >
                                    <SetupIcon id={row.icon} />
                                    {name}
                                    <span className="sv">
                                        {state === "denied" && phase === "done" ? (
                                            <button
                                                className="cact"
                                                type="button"
                                                aria-label={t("v2.sees.openSettingsNamed", { name })}
                                                onClick={() =>
                                                    void Promise.resolve(
                                                        OpenPermissionSettings(firstDenied(row)),
                                                    ).catch(() => {})
                                                }
                                            >
                                                {t("v2.sees.openSettings")}
                                            </button>
                                        ) : (
                                            <>
                                                <SetupIcon id="i-check" />
                                                <em>{t(state === "on" ? "v2.sees.on" : "v2.sees.off")}</em>
                                            </>
                                        )}
                                    </span>
                                </div>
                            );
                        })}
                    </div>
                </div>
            </div>
            <div className="act">
                <div
                    className={`doneline${everythingOn || failed ? " show" : ""}${failed ? " error" : ""}`}
                    aria-live="polite"
                >
                    {failed === "permissions"
                        ? t("v2.sees.failed")
                        : failed === "relaunch"
                          ? t("v2.sees.reopenFailed")
                          : t("v2.sees.done")}
                </div>
                <button
                    className="btn primary"
                    type="button"
                    data-testid="setup-v2-continue"
                    disabled={phase === "busy"}
                    onClick={() => (phase === "done" ? onNext() : void turnOn())}
                >
                    {t(phase === "done" ? "v2.continue" : "v2.sees.turnOn")}
                </button>
            </div>
        </section>
    );
}
