import { BuildWorkProfileDraft, AddWorkProfileLine, ApproveWorkProfile, DiscardWorkProfileDraft, EditWorkProfileLine, GetWorkProfile, KeepWorkProfileLine, PreviewWorkProfileUpload, RenameWorkProfileSpecialty, SetWorkProfileLineRemoved, SetWorkProfileSpecialties } from './wails-shim-app';

export interface WorkProfileLine {
    id: string;
    group?: string;
    text: string;
    previous?: string;
    status: string;
    filtered: boolean;
    removed: boolean;
    lastSeen?: string;
    kept?: boolean;
}
export interface WorkProfileSpecialty { id: string; name: string; selected: boolean }
export interface WorkProfileDraft {
    id: string;
    madeAt: string;
    learnedThrough: string;
    basedOnVersion: number;
    fingerprint: string;
    specialties: WorkProfileSpecialty[];
    lines: WorkProfileLine[];
    edited: boolean;
}
export interface WorkProfileView {
    approvedVersion: number;
    approvedAt?: string;
    approved?: WorkProfileDraft;
    pending?: WorkProfileDraft;
    newerReady: boolean;
    building: boolean;
    lastError?: string;
    lastDraftAt?: string;
    nextDraftAt: string;
}
export interface WorkProfileUpload { lines: string[]; publicSpecialties: string[]; learnedThrough?: string; expectedVersion: number }
export interface WorkProfileApprovalResult { view: WorkProfileView; conflictVersion?: number }

export const getWorkProfile = () => GetWorkProfile();
export const editWorkProfileLine = (draftId: string, id: string, text: string) => EditWorkProfileLine(draftId, id, text);
export const setWorkProfileLineRemoved = (draftId: string, id: string, removed: boolean) => SetWorkProfileLineRemoved(draftId, id, removed);
export const addWorkProfileLine = (draftId: string, group: string, text: string) => AddWorkProfileLine(draftId, group, text);
export const setWorkProfileSpecialties = (draftId: string, ids: string[]) => SetWorkProfileSpecialties(draftId, ids);
export const renameWorkProfileSpecialty = (draftId: string, id: string, name: string) => RenameWorkProfileSpecialty(draftId, id, name);
export const keepWorkProfileLine = (draftId: string, id: string) => KeepWorkProfileLine(draftId, id);
export const discardWorkProfileDraft = (draftId: string) => DiscardWorkProfileDraft(draftId);
export const previewWorkProfileUpload = (draftId: string) => PreviewWorkProfileUpload(draftId);
export const approveWorkProfile = (draftId: string, preview: WorkProfileUpload) => ApproveWorkProfile(draftId, preview);

export const buildWorkProfileDraft = () => BuildWorkProfileDraft();
