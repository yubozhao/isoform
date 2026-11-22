import { useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { Button } from "@/components/ui/button";
import {
    DeferModelDownloads,
    GetModelChoices,
    GetModelDownloads,
    OpenExternalURL,
    PlanSetupModelDownload,
    StartModelDownloads,
} from "@/lib/wails-shim-app";
import { SaveUIState } from "../../../../wailsjs/go/main/App";
import { EventsOn } from "../../../../wailsjs/runtime/runtime";
import type { ModelChoices, ModelDownload, ModelDownloadPlan } from "@/lib/setup-backend";
import { QUIET_JOBS, SPEECH_JOBS, formatModelBytes, wireChoices } from "../v2/models";

// setup-answers v1.0.0 answer 1: allowing Microphone already said yes to
// transcribing on this computer, so the speech models (calls, after-call and
// voice) start downloading at once, shown as one line under the Microphone
// row with no question. The download carries on after setup. With Microphone
// skipped or denied, no speech model is planned, started or deferred.
//
// The privacy and memory models are not speech (shell owner, 2026-11-21):
// PII redaction and memory need them whatever the microphone says, so they
// are deferred quietly to the plugged-in-and-idle download, as before, with
// no line and no question.
//
// Short of disk (computer conformance answers v1.1.0 S9): the line turns
// amber with Open Storage Settings, setup re-plans every minute and starts
// once there is room, and the request is saved so the desktop retries at
// each launch and hourly after setup (setup_speech_waiting.go). Under it,
// calls and the watch's talk are kept and transcribed once the model is
// here, which the speech-model arrival hook makes true (setup_models.go).
//
// The privacy and search models short of disk wait too, with the same line
// named for them (answers v1.2.0 S11), whatever the microphone says.


/** Saved while speech recognition waits for disk (setup_speech_waiting.go). */
export const SPEECH_WAITING_KEY = "isoform.setup.speech-waiting";
/** How often setup re-plans a download that is short of disk. */
export const SPEECH_SPACE_POLL_MS = 60_000;
const STORAGE_SETTINGS_URL = "x-apple.systempreferences:com.apple.settings.Storage";

/** The plan's share already on disk, 0 to 100, from the running jobs. */
export function planPercent(plan: ModelDownloadPlan, downloads: ModelDownload[]) {
    let done = 0;
    let total = 0;
    for (const item of plan.items) {
        const job = downloads.find((d) => d.id === item.id);
        const bytes = job?.bytesTotal || item.bytes;
        total += bytes;
        done += job?.state === "done" ? bytes : (job?.bytesDone ?? 0);
    }
    return total > 0 ? Math.min(100, Math.floor((done / total) * 100)) : 0;
}

function planFromJobs(jobs: ModelDownload[]): ModelDownloadPlan {
    return {
        planId: "",
        choices: {},
        requiredBytes: 0,
        items: jobs.map((job) => ({
            id: job.id,
            bytes: job.bytesTotal,
            fileCount: 0,
            source: { kind: "mirror", url: "" },
            host: "",
            licence: { name: "", url: "", nonCommercial: false },
            nonCommercial: false,
        })),
        totalBytes: jobs.reduce((sum, job) => sum + job.bytesTotal, 0),
        freeBytes: 0,
        enoughSpace: true,
        modelsFolder: "",
    };
}

/** A job is handled when it is queued, deferred, running or done; failed is not. */
const handled = (downloads: ModelDownload[], id: string) =>
    downloads.some((d) => d.id === id && d.state !== "failed");

type LineState = "planning" | "downloading" | "noSpace" | "failed";

export function ModelsLine({ micOn }: { micOn: boolean }) {
    const { t, i18n } = useTranslation("onboarding");
    const [choices, setChoices] = useState<ModelChoices | null>(null);
    const [plan, setPlan] = useState<ModelDownloadPlan | null>(null);
    const [state, setState] = useState<LineState>("planning");
    const [downloads, setDownloads] = useState<ModelDownload[]>([]);
    const [attempt, setAttempt] = useState(0);
    const started = useRef(false);
    const quiet = useRef(false);
    const [quietShort, setQuietShort] = useState<ModelDownloadPlan | null>(null);

    useEffect(() => {
        let live = true;
        GetModelChoices()
            .then((next) => live && setChoices(next))
            .catch(() => live && micOn && setState("failed"));
        return () => {
            live = false;
        };
        // Read once; a failure only shows once the microphone is on.
        // eslint-disable-next-line react-hooks/exhaustive-deps
    }, []);

    const wire = useMemo(() => (choices ? wireChoices(choices, i18n.language) : null), [choices, i18n.language]);

    // Privacy and memory: deferred quietly, microphone on or off.
    useEffect(() => {
        if (!wire || quiet.current) return;
        quiet.current = true;
        void (async () => {
            try {
                const next = await PlanSetupModelDownload(wire, SPEECH_JOBS);
                if (next.items.length === 0) return;
                // They still wait for their idle download; the line says so.
                if (!next.enoughSpace) setQuietShort(next);
                const jobs = (await GetModelDownloads().catch(() => [] as ModelDownload[])) ?? [];
                if (next.items.some((item) => handled(jobs, item.id))) return;
                await DeferModelDownloads(next);
            } catch {
                /* They are asked again on the next setup or in Settings › Models. */
            }
        })();
    }, [wire]);

    // Speech: planned and started the moment Microphone is allowed; short of
    // disk, re-planned every minute until it fits.
    useEffect(() => {
        if (!micOn || !wire || started.current) return;
        let live = true;
        let timer: number | undefined;
        void (async () => {
            try {
                // Coming back to step 1 (the reopen after Screen Recording) finds
                // the download already running: show it, never start it twice.
                const jobs = (await GetModelDownloads().catch(() => [] as ModelDownload[])) ?? [];
                const running = jobs.filter(
                    (job) => !job.deferred && job.state !== "failed" && job.state !== "done",
                );
                const next = await PlanSetupModelDownload(wire, QUIET_JOBS);
                if (!live) return;
                const speechRunning = running.filter((job) => next.items.some((item) => item.id === job.id));
                if (speechRunning.length > 0) {
                    started.current = true;
                    setPlan(planFromJobs(speechRunning));
                    setState("downloading");
                    return;
                }
                setPlan(next);
                if (next.items.length === 0) {
                    started.current = true;
                    return;
                }
                if (!next.enoughSpace) {
                    setState("noSpace");
                    void SaveUIState(SPEECH_WAITING_KEY, JSON.stringify({ choices: wire, skip: QUIET_JOBS })).catch(() => {});
                    timer = window.setTimeout(() => live && setAttempt((n) => n + 1), SPEECH_SPACE_POLL_MS);
                    return;
                }
                await StartModelDownloads(next);
                started.current = true;
                void SaveUIState(SPEECH_WAITING_KEY, "").catch(() => {});
                if (live) setState("downloading");
            } catch {
                if (live) setState("failed");
            }
        })();
        return () => {
            live = false;
            window.clearTimeout(timer);
        };
    }, [micOn, wire, attempt]);

    // Real progress from the download manager, the same source as the menu bar.
    useEffect(() => {
        if (state !== "downloading") return;
        let live = true;
        const off = EventsOn("models:downloads", (next: ModelDownload[]) => live && setDownloads(next ?? []));
        void GetModelDownloads()
            .then((next) => live && setDownloads(next ?? []))
            .catch(() => {});
        return () => {
            live = false;
            off();
        };
    }, [state]);

    const openStorage = (testId: string) => (
        <Button
            size="sm"
            variant="outline"
            onClick={() => void OpenExternalURL(STORAGE_SETTINGS_URL).catch(() => {})}
            data-testid={testId}
        >
            {t("setup.openStorage")}
        </Button>
    );
    const quietLine = quietShort ? (
        <div className="sv3-model-short" data-testid="setup-v3-quiet-models-short">
            <p className="sv3-warn">
                {t("setup.modelsShort", { size: formatModelBytes(quietShort.totalBytes), free: formatModelBytes(quietShort.freeBytes) })}
            </p>
            {openStorage("setup-v3-quiet-open-storage")}
        </div>
    ) : null;
    return (
        <>
            {micOn ? speechLine() : null}
            {quietLine}
        </>
    );

    function speechLine() {
        if (state === "failed") {
            return (
                <p className="sv3-warn sv3-model-line" role="alert" data-testid="setup-v3-model-line">
                    {t("setup.modelFailed")}
                </p>
            );
        }
        if (!plan || plan.items.length === 0) return null;
        const size = formatModelBytes(plan.totalBytes);
        if (state === "noSpace") {
            return (
                <div className="sv3-model-short" data-testid="setup-v3-model-line">
                    <p className="sv3-warn">{t("setup.diskShort", { size, free: formatModelBytes(plan.freeBytes) })}</p>
                    {openStorage("setup-v3-open-storage")}
                    <p className="sv3-quiet" data-testid="setup-v3-disk-kept">{t("setup.diskKept")}</p>
                </div>
            );
        }
        return (
            <p className="sv3-quiet sv3-model-line" data-testid="setup-v3-model-line">
                {t("setup.modelLine", { size, n: planPercent(plan, downloads) })}
            </p>
        );
    }
}
