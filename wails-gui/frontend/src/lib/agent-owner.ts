import { DownloadAgentOwnerReviewFile, AgentOwnerAction, AgentOwnerDecision, GetAgentOwnerNeeds, GetAgentOwnerNeedsWithDraft, GetAgentOwnerSnapshot, MarkAgentOwnerPayoutNeedShown, OpenAgentOwnerPayoutOnboarding, UploadApprovedProfile } from './wails-shim-app';

export type AgentOwnerSnapshotKind = 'status' | 'speeds' | 'profile' | 'jobs' | 'job' | 'balance' | 'payouts' | 'blocked';

export interface AgentOwnerSnapshot<T> {
    available: boolean;
    data: T | null;
}

export interface AgentOwnerSettings {
    rateCentsPerMinute: number | null;
    speed: string;
    effort: string;
    approveJobs: boolean;
    reviewResults: boolean;
    version: number;
    deliveredNotification?: boolean;
}

export type AgentOwnerSettingsInput = Omit<AgentOwnerSettings, 'version' | 'rateCentsPerMinute'> & { rateCentsPerMinute: number };

export interface AgentOwnerProfile {
    version: number;
    approvedAt: string;
    learnedThrough?: string | null;
    publicSpecialties?: string[];
}

export interface AgentOwnerStatus {
    state: string;
    name: string;
    settings: AgentOwnerSettings;
    profile: AgentOwnerProfile | null;
    minimumRateCents: number;
    payout: { status: string };
    paymentAgreement?: { termsVersion: string; acceptedAt: string } | null;
}

export interface AgentOwnerSpeed {
    speed: string;
    effort: string;
    typicalCentsPerMinute: number;
    minimumRateCents: number;
}

export interface AgentOwnerJobMessage {
    id: string;
    chatId: string;
    kind: string;
    text: string;
    fileIds: string[];
    createdAt: string;
    deliverable?: string;
    estimatedMinutes?: number;
    rateCentsPerMinute?: number;
    minimumChargeCents?: number;
    profileUpdatedAt?: string;
    receipt?: { passId: string; chargeCents: number; workedSeconds: number; rateCentsPerMinute: number };
}

export interface AgentOwnerJob {
    id: string;
    state: string;
    clientLabel: string | null;
    clientDisplayName?: string | null;
    title?: string;
    createdAt: string;
    updatedAt: string;
    messageCount?: number;
    messages?: AgentOwnerJobMessage[];
    clientId?: string;
    approval?: AgentOwnerCard;
    review?: AgentOwnerCard & { workedSeconds: number; limitCents: number; files: { path: string; size: number }[]; minutesLeft?: number | null; centsLeft?: number | null; canRedo?: boolean };
    passes?: AgentOwnerPass[];
}

export interface AgentOwnerCard {
    passId: string;
    decisionVersion?: number;
    deadline: string;
}

export interface AgentOwnerJobSplit {
    clientChargeCents: number;
    modelCostCents: number;
    cardFeeCents: number;
    isoformCents: number;
    ownerShareCents: number;
}

export interface AgentOwnerPass {
    id: string;
    state: string;
    decisionVersion?: number;
    profileVersion: number;
    speed: string;
    effort: string;
    rateCentsPerMinute: number;
    split: AgentOwnerJobSplit | null;
}

export interface AgentOwnerBlockedClient {
    clientId: string;
    clientDisplayName?: string | null;
    blockedAt?: string;
}

export interface AgentOwnerMovement {
    id: number;
    passId: string | null;
    clientId: string | null;
    kind: string;
    chargeCents: number;
    modelCostNanocents: number;
    cardFeeCents: number;
    isoformCents: number;
    ownerShareCents: number;
    debtCents: number;
    createdAt: string;
}

export interface AgentOwnerBalance {
    earnedCents: number;
    owedCents: number;
    transferredCents: number;
    balanceCents: number;
    lines: AgentOwnerMovement[];
    availableCents?: number;
    onTheWayCents?: number;
}

export interface AgentOwnerPayouts {
    status: string;
    requirements: string[];
    balanceCents: number;
}

export interface AgentOwnerSnapshotData {
    status: AgentOwnerStatus;
    speeds: AgentOwnerSpeed[];
    profile: AgentOwnerProfile;
    jobs: AgentOwnerJob[];
    job: AgentOwnerJob;
    balance: AgentOwnerBalance;
    payouts: AgentOwnerPayouts;
    blocked: AgentOwnerBlockedClient[];
}

const observedSnapshots = new Map<string, { kind: AgentOwnerSnapshotKind; id: string }>();
let foregroundRefreshInstalled = false;

function refreshObservedSnapshots(): void {
    if (document.visibilityState !== 'visible') return;
    for (const { kind, id } of observedSnapshots.values()) {
        void GetAgentOwnerSnapshot(kind, id).catch(() => undefined);
    }
}

function observeSnapshot(kind: AgentOwnerSnapshotKind, id: string): void {
    observedSnapshots.set(JSON.stringify([kind, id]), { kind, id });
    if (!foregroundRefreshInstalled && typeof window !== 'undefined' && typeof document !== 'undefined') {
        foregroundRefreshInstalled = true;
        window.addEventListener('focus', refreshObservedSnapshots);
        document.addEventListener('visibilitychange', refreshObservedSnapshots);
    }
}

export function getAgentOwnerSnapshot<K extends AgentOwnerSnapshotKind>(kind: K, id = ''): Promise<AgentOwnerSnapshot<AgentOwnerSnapshotData[K]>> {
    observeSnapshot(kind, id);
    return GetAgentOwnerSnapshot(kind, id) as Promise<AgentOwnerSnapshot<AgentOwnerSnapshotData[K]>>;
}

export function uploadApprovedProfile(lines: string[], publicSpecialties: string[], learnedThrough: string, expectedVersion: number): Promise<AgentOwnerProfile> {
    return UploadApprovedProfile(lines, publicSpecialties, learnedThrough, expectedVersion) as Promise<AgentOwnerProfile>;
}

export type AgentOwnerActionName = 'join' | 'cancel_join' | 'pause' | 'on' | 'agree' | 'settings' | 'block' | 'unblock';

export interface AgentOwnerDecisionInput { action: 'accept' | 'decline' | 'send' | 'redo'; jobId: string; passId: string; decisionVersion: number; note?: string }
export interface AgentOwnerDecisionResult { accepted: boolean; data?: unknown; error?: string; current: { passId: string; state: string; decisionVersion: number } | null }

export function decideAgentOwnerJob(input: AgentOwnerDecisionInput): Promise<AgentOwnerDecisionResult> {
    return AgentOwnerDecision(input);
}

export function agreeToAgentOwnerPaymentTerms(termsVersion: string): Promise<unknown> {
    return AgentOwnerAction('agree', '', termsVersion, null);
}

export function agentOwnerAction(action: AgentOwnerActionName, id = '', note = '', settings: AgentOwnerSettingsInput | null = null): Promise<unknown> {
    return AgentOwnerAction(action, id, note, settings);
}

export function openAgentOwnerPayoutOnboarding(termsVersion: string): Promise<void> {
    return OpenAgentOwnerPayoutOnboarding(termsVersion);
}

export interface AgentOwnerNeed { kind: 'approval' | 'review' | 'draft' | 'payouts'; id?: string; amountCents?: number }

function observeNeedSnapshots(): void {
    for (const kind of ['jobs', 'payouts', 'status', 'balance'] as const) observeSnapshot(kind, '');
}

export function getAgentOwnerNeeds(): Promise<AgentOwnerNeed[]> {
    observeNeedSnapshots();
    return GetAgentOwnerNeeds() as Promise<AgentOwnerNeed[]>;
}

export function getAgentOwnerNeedsWithDraft(draftId: string): Promise<AgentOwnerNeed[]> {
    observeNeedSnapshots();
    return GetAgentOwnerNeedsWithDraft(draftId) as Promise<AgentOwnerNeed[]>;
}

export function markAgentOwnerPayoutNeedShown(): Promise<void> {
    return MarkAgentOwnerPayoutNeedShown();
}

export function downloadAgentOwnerReviewFile(jobId: string, path: string): Promise<boolean> {
    return DownloadAgentOwnerReviewFile(jobId, path);
}
