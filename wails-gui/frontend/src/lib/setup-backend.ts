// Setup's wire contract. Display copy belongs to the localized screen layer.
export type ModelJob = 'live' | 'afterCall' | 'voice' | 'privacy' | 'memory' | 'speakerPack';
export interface ModelLicence { name: string; url: string; nonCommercial: boolean }
export interface ModelSource { kind: 'mirror' | 'huggingface' | 'github'; url: string }
export interface ModelFile {
    path: string;
    sha256: string | null;
    bytes: number | null;
    urls: string[];
    archive?: {
        format: 'tar.bz2';
        destination: string;
        root: string;
        legacy?: { marker: string; required: string[] };
        files: Array<{ path: string; sha256: string; bytes: number }>;
    };
    unknownReason?: string;
}
export interface ModelEntry {
    id: string;
    job: ModelJob;
    displayName: string;
    line: string;
    evidence: string;
    sizeBytes: number | null;
    files: ModelFile[];
    sources: ModelSource[];
    licence: ModelLicence;
    recommended: boolean;
    languages: string[];
    downloaded: boolean;
}
export interface ModelChoices {
    jobs: Array<{ job: ModelJob; chosenId: string; alternatives: ModelEntry[]; locked: boolean }>;
    totalBytesToDownload: number;
    freeBytes: number;
    modelsFolder: string;
}
export interface ModelDownloadPlan {
    planId: string;
    choices: Partial<Record<ModelJob, string>>;
    requiredBytes: number;
    items: Array<{ id: string; bytes: number; fileCount: number; source: ModelSource; host: string; licence: ModelLicence; nonCommercial: boolean }>;
    totalBytes: number;
    freeBytes: number;
    enoughSpace: boolean;
    modelsFolder: string;
}
export interface ModelDownload {
    id: string;
    state: 'queued' | 'downloading' | 'paused' | 'done' | 'failed';
    pauseReason: string;
    bytesDone: number;
    bytesTotal: number;
    error: string;
    /** Confirmed with Later: waits until the computer is plugged in and idle. */
    deferred?: boolean;
}
export interface SetupAgent {
    id: 'claude' | 'codex' | 'ollama';
    installed: boolean;
    version: string;
    signedIn: boolean;
    plan: string;
    notFoundYet: boolean;
    probedAt: string;
}
export interface SetupAgentProbe {
    agents: SetupAgent[];
    magpie: import('./wails-shim-app').MagpieInstallView & { installed: boolean; signedIn: boolean; plan: string; notFoundYet: boolean; probedAt: string; downloadable?: boolean; downloadBytes?: number; required?: boolean };
    npm: boolean;
    git: import('./wails-shim-app').GitStatus;
    probedAt: string;
}
export interface SetupPermissions {
    platform?: string;
    answers: Array<{ key: string; state: 'off' | 'on' | 'denied' | 'onAfterReopen' | 'unavailable' | 'unsupported' }>;
    running: boolean;
    resumeStep: 'sees';
    next: number;
    notificationGranted?: boolean | null;
}

export interface ModelNetworkState {
    known: boolean;
    offline: boolean;
    expensive: boolean;
    constrained: boolean;
}
