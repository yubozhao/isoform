import type {
    ModelChoices,
    ModelDownload,
    ModelDownloadPlan,
    ModelEntry,
    ModelJob,
    ModelNetworkState,
} from "@/lib/setup-backend";
import type { SetupIconId } from "./SetupIcons";

// "Runs on this computer" (handoff-2026-10-29-1523-setup-v2.2.0 §2): the local
// models as a choice by job, with names and sizes from this computer's own
// plan. Nothing downloads before Download or Later.

export type ModelRowId = "calls" | "afterCall" | "voice" | "always";

export interface ModelRowSpec {
    id: ModelRowId;
    icon: SetupIconId;
    jobs: ModelJob[];
    /** False for the privacy filter and memory search: always on. */
    switchable: boolean;
    /** Off until "Your calls" is on (screen 3). */
    needsCalls: boolean;
}

export const MODEL_ROWS: ModelRowSpec[] = [
    { id: "calls", icon: "i-mic", jobs: ["live", "speakerPack"], switchable: true, needsCalls: true },
    { id: "afterCall", icon: "i-doc", jobs: ["afterCall"], switchable: true, needsCalls: true },
    { id: "voice", icon: "i-play", jobs: ["voice"], switchable: true, needsCalls: false },
    { id: "always", icon: "i-lock", jobs: ["privacy", "memory"], switchable: false, needsCalls: false },
];

export type RowsOn = Record<ModelRowId, boolean>;

/** The model each job would use: the saved choice, MeloTTS for a Chinese
 * interface, and the recommended after-call model when it was turned off. */
export function chosenModels(choices: ModelChoices, language: string): Record<string, ModelEntry> {
    const out: Record<string, ModelEntry> = {};
    for (const job of choices.jobs) {
        let id = job.chosenId;
        if (job.job === "voice" && language.startsWith("zh")) {
            id = job.alternatives.find((e) => e.id === "melotts-zh-en")?.id ?? id;
        }
        if (job.job === "afterCall" && id === "none") {
            id = job.alternatives.find((e) => e.recommended)?.id ?? id;
        }
        const entry = job.alternatives.find((e) => e.id === id);
        if (entry) out[job.job] = entry;
    }
    return out;
}

/** What one row would download, by the catalog's sizes. */
export function rowBytes(row: ModelRowSpec, chosen: Record<string, ModelEntry>) {
    return row.jobs.reduce((sum, job) => sum + (chosen[job]?.sizeBytes ?? 0), 0);
}

/** A model name without its parameter count ("Qwen3-ASR 0.6B" → "Qwen3-ASR"). */
/** The speech jobs: they follow the microphone (setup-answers v1.0.0 answer 1). */
export const SPEECH_JOBS: ModelJob[] = ["live", "speakerPack", "afterCall", "voice"];
/** The privacy and memory jobs: they download in idle time, microphone on or
 *  off (computer conformance answers v1.2.0 S11). */
export const QUIET_JOBS: ModelJob[] = ["privacy", "memory"];

/** Every job's model id, with after-call "none" kept when it was turned off.
 *  Setup's models line and What Isoform sees' Calls and meetings row plan with it. */
export function wireChoices(choices: ModelChoices, language: string) {
    const out: Partial<Record<ModelJob, string>> = {};
    for (const [job, entry] of Object.entries(chosenModels(choices, language))) out[job as ModelJob] = entry.id;
    if (choices.jobs.find((job) => job.job === "afterCall")?.chosenId === "none") out.afterCall = "none";
    return out;
}

export function shortModelName(entry: ModelEntry | undefined) {
    return (entry?.displayName ?? "").replace(/\s+\d+(\.\d+)?B$/, "");
}

/** The planner's request for the rows that are on. A row turned off keeps
 * its choice and only leaves its download out; the after-call row turns the
 * after-call model off, as Settings does. */
export function planRequest(chosen: Record<string, ModelEntry>, on: RowsOn) {
    const choices: Partial<Record<ModelJob, string>> = {};
    for (const [job, entry] of Object.entries(chosen)) choices[job as ModelJob] = entry.id;
    choices.afterCall = on.afterCall && chosen.afterCall ? chosen.afterCall.id : "none";
    const skip: ModelJob[] = [];
    if (!on.calls) skip.push("live", "speakerPack");
    if (!on.voice) skip.push("voice");
    return { choices, skip };
}

/** Each row's share already on disk, from the confirmed jobs. A row with
 * nothing to download (already here) is full. */
export function rowProgress(
    row: ModelRowSpec,
    plan: ModelDownloadPlan,
    chosen: Record<string, ModelEntry>,
    downloads: ModelDownload[],
) {
    const ids = row.jobs.map((job) => chosen[job]?.id).filter(Boolean) as string[];
    const items = plan.items.filter((item) => ids.includes(item.id));
    if (items.length === 0) return 1;
    let done = 0;
    let total = 0;
    for (const item of items) {
        const job = downloads.find((d) => d.id === item.id);
        total += job?.bytesTotal || item.bytes;
        done += job?.state === "done" ? job.bytesTotal || item.bytes : (job?.bytesDone ?? 0);
    }
    return total > 0 ? Math.min(1, done / total) : 0;
}

/** Sizes as the design writes them: "270 MB", "2.2 GB", "412 GB". */
export function formatModelBytes(bytes: number) {
    if (bytes < 1e9) return `${Math.max(0, Math.round(bytes / 1e6))} MB`;
    const gb = bytes / 1e9;
    return `${gb >= 100 ? Math.round(gb) : gb.toFixed(1)} GB`;
}

/** The existing reason a confirmed download is waiting on the network
 * (models.network*), or null when the path is fine. */
export function networkReasonKey(
    downloads: ModelDownload[],
    network: ModelNetworkState | null,
):
    | "models.networkWaiting"
    | "models.networkOffline"
    | "models.networkLowData"
    | "models.networkMetered"
    | null {
    const paused = downloads.find((d) => d.state === "paused" && !d.deferred)?.pauseReason;
    if (paused === "Paused while offline" || network?.offline) return "models.networkOffline";
    if (paused === "Paused on a metered connection" || network?.constrained || network?.expensive) {
        return network?.constrained ? "models.networkLowData" : "models.networkMetered";
    }
    if (paused === "Waiting for network status") return "models.networkWaiting";
    return null;
}
