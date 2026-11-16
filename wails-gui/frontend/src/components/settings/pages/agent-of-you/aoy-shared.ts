// Agent of You, the owner's side: the data hooks, the page header store and
// the number and date formats every owner screen shares (handoff v2.1.3 §5).
//
// Data rule: every read returns the Go client's cached snapshot at once and
// the client emits `agent-owner:changed` (and `work-profile:changed`) when a
// background refresh or an action changed something. Screens subscribe to
// those events; they never poll.
import { useCallback, useEffect, useRef, useState, useSyncExternalStore } from "react";
import type { TFunction } from "i18next";
import { EventsOn } from "@/lib/wails-shim-runtime";
import {
    getAgentOwnerSnapshot,
    type AgentOwnerSnapshotData,
    type AgentOwnerSnapshotKind,
} from "@/lib/agent-owner";
import { getWorkProfile, type WorkProfileView } from "@/lib/work-profile";

export const AGENT_OWNER_CHANGED = "agent-owner:changed";
export const WORK_PROFILE_CHANGED = "work-profile:changed";
/** The terms version recorded with "Agree and turn on" and "Agree and continue to Stripe". */
export const AOY_TERMS_VERSION = "agent-of-you-beta-2.1.3";
/** The least a client pays each time the agent starts (server: minimumChargeCents). */
export const AOY_MINIMUM_CHARGE_CENTS = 300;
/** Payouts go out at this balance or more. */
export const AOY_PAYOUT_MINIMUM_CENTS = 1000;
/** The full terms page. Empty until the page exists, so the link stays hidden (v2.1.1). */
export const AOY_FULL_TERMS_URL = "";
export const AOY_PAGE_PREFIX = "isoform.ai/agents/";

export type OwnerState = "setting_up" | "on" | "paused";
export function isOwnerState(state: string | undefined | null): state is OwnerState {
    return state === "setting_up" || state === "on" || state === "paused";
}

function safeOn(event: string, handler: () => void): () => void {
    try {
        const off = EventsOn(event, handler);
        return typeof off === "function" ? off : () => undefined;
    } catch {
        return () => undefined;
    }
}

export interface SnapshotState<T> {
    /** False until the first cached read comes back. */
    loaded: boolean;
    /** True when the cache holds this snapshot. */
    available: boolean;
    data: T | null;
    /** The read itself failed (no reservation, no server configured). */
    failed: boolean;
    reload: () => void;
}

/** One owner snapshot: the cached copy now, then again on every change event. */
export function useOwnerSnapshot<K extends AgentOwnerSnapshotKind>(kind: K, id = "", enabled = true): SnapshotState<AgentOwnerSnapshotData[K]> {
    const [state, setState] = useState<Omit<SnapshotState<AgentOwnerSnapshotData[K]>, "reload">>({ loaded: false, available: false, data: null, failed: false });
    const live = useRef(true);
    const read = useCallback(() => {
        if (!enabled) return;
        let pending: Promise<{ available: boolean; data: AgentOwnerSnapshotData[K] | null }>;
        try {
            pending = getAgentOwnerSnapshot(kind, id);
        } catch {
            setState({ loaded: true, available: false, data: null, failed: true });
            return;
        }
        pending
            .then((snapshot) => {
                if (!live.current) return;
                setState({ loaded: true, available: !!snapshot?.available, data: (snapshot?.data ?? null) as AgentOwnerSnapshotData[K] | null, failed: false });
            })
            .catch(() => {
                if (live.current) setState((current) => ({ ...current, loaded: true, failed: !current.available }));
            });
    }, [kind, id, enabled]);
    useEffect(() => {
        live.current = true;
        read();
        const off = safeOn(AGENT_OWNER_CHANGED, read);
        return () => {
            live.current = false;
            off();
        };
    }, [read]);
    return { ...state, reload: read };
}

export interface WorkProfileState {
    loaded: boolean;
    view: WorkProfileView | null;
    failed: boolean;
    set: (view: WorkProfileView) => void;
    reload: () => void;
}

/** The local work profile: approved version, the waiting draft, and the build status. */
export function useWorkProfile(enabled = true): WorkProfileState {
    const [view, setView] = useState<WorkProfileView | null>(null);
    const [loaded, setLoaded] = useState(false);
    const [failed, setFailed] = useState(false);
    const live = useRef(true);
    const read = useCallback(() => {
        if (!enabled) return;
        let pending: Promise<WorkProfileView>;
        try {
            pending = getWorkProfile();
        } catch {
            setLoaded(true);
            setFailed(true);
            return;
        }
        pending
            .then((next) => {
                if (!live.current) return;
                setView(next);
                setLoaded(true);
                setFailed(false);
            })
            .catch(() => {
                if (!live.current) return;
                setLoaded(true);
                setFailed(true);
            });
    }, [enabled]);
    useEffect(() => {
        live.current = true;
        read();
        const off = safeOn(WORK_PROFILE_CHANGED, read);
        return () => {
            live.current = false;
            off();
        };
    }, [read]);
    return { loaded, view, failed, set: setView, reload: read };
}

// ── The page header and the Settings row tag ──
// Settings draws the page title above the scroll area. Once Bo approves, the
// owner's screens set it here: "Agent of You", or a breadcrumb and a title.

export interface AoyCrumb {
    label: string;
    onClick?: () => void;
}
export interface AoyHeader {
    crumbs: AoyCrumb[];
    title: string;
    /** The section's one line under the title (computer-settings v1.1.0 rule 3); the home view only. */
    lead?: string;
    trailing?: React.ReactNode;
}
export type AoyNavTag = "setup" | "on" | "paused" | null;

interface AoyChrome {
    header: AoyHeader | null;
    tag: AoyNavTag;
}

let chrome: AoyChrome = { header: null, tag: null };
const listeners = new Set<() => void>();
function emitChrome(next: Partial<AoyChrome>) {
    chrome = { ...chrome, ...next };
    for (const listener of listeners) listener();
}
function subscribeChrome(listener: () => void) {
    listeners.add(listener);
    return () => listeners.delete(listener);
}

export function setAoyHeader(header: AoyHeader | null) {
    emitChrome({ header });
}
export function useAoyHeader(): AoyHeader | null {
    return useSyncExternalStore(subscribeChrome, () => chrome.header, () => null);
}

export function tagForState(state: string | undefined | null): AoyNavTag {
    switch (state) {
        case "setting_up":
            return "setup";
        case "on":
            return "on";
        case "paused":
            return "paused";
        default:
            return null;
    }
}
export function setAoyNavTag(tag: AoyNavTag) {
    if (chrome.tag !== tag) emitChrome({ tag });
}

let navStarted = false;
function startNavTag() {
    if (navStarted) return;
    navStarted = true;
    const read = () => {
        try {
            getAgentOwnerSnapshot("status")
                .then((snapshot) => setAoyNavTag(tagForState((snapshot?.data as { state?: string } | null)?.state)))
                .catch(() => undefined);
        } catch {
            // No owner client in this build or test: the row keeps its coming-soon tag.
        }
    };
    read();
    safeOn(AGENT_OWNER_CHANGED, read);
}

/** The Settings row's tag: Set up, On or Paused once Bo approves; null before. */
export function useAoyNavTag(): AoyNavTag {
    useEffect(startNavTag, []);
    return useSyncExternalStore(subscribeChrome, () => chrome.tag, () => null);
}

/** Test seam: forget the row tag and header between tests. */
export function resetAoyChromeForTests() {
    chrome = { header: null, tag: null };
    navStarted = false;
}

// ── Formats ──

/** Cents to "28.00"; the copy carries the dollar sign. */
export function money(cents: number | null | undefined): string {
    const value = Math.abs(Math.round(cents ?? 0)) / 100;
    return value.toFixed(2);
}

/** "1 minute" or "14 minutes", the {mins} placeholder (v2.1.1). */
export function minsText(t: TFunction<"settings">, minutes: number): string {
    const n = Math.max(0, Math.round(minutes));
    return n === 1 ? t("aoy.mins1") : t("aoy.minsN", { n });
}

export function secondsToMinutes(seconds: number | null | undefined): number {
    return Math.max(0, Math.ceil((seconds ?? 0) / 60));
}

function parse(value: string | null | undefined): Date | null {
    if (!value) return null;
    const date = new Date(value);
    return Number.isNaN(date.getTime()) ? null : date;
}

/** A day: "Nov 2" / "11月2日". */
export function formatDay(value: string | null | undefined, language: string): string {
    const date = parse(value);
    if (!date) return "";
    return new Intl.DateTimeFormat(language, { month: "short", day: "numeric" }).format(date);
}

/** A deadline: "Tue 2:21 PM" / "周二 14:21". */
export function formatDeadline(value: string | null | undefined, language: string): string {
    const date = parse(value);
    if (!date) return "";
    return new Intl.DateTimeFormat(language, { weekday: "short", hour: "numeric", minute: "2-digit" }).format(date);
}

/** A time with its day: "Nov 9, 3:24 PM". */
export function formatWhen(value: string | null | undefined, language: string): string {
    const date = parse(value);
    if (!date) return "";
    return new Intl.DateTimeFormat(language, { month: "short", day: "numeric", hour: "numeric", minute: "2-digit" }).format(date);
}

export const SPEEDS = ["fast", "balanced", "best"] as const;
export const EFFORTS = ["low", "medium", "high", "xhigh"] as const;

export function speedLabel(t: TFunction<"settings">, speed: string): string {
    switch (speed) {
        case "fast":
            return t("aoy.spFast");
        case "best":
            return t("aoy.spBest");
        default:
            return t("aoy.spBal");
    }
}

export function effortLabel(t: TFunction<"settings">, effort: string): string {
    switch (effort) {
        case "low":
            return t("aoy.efLow");
        case "high":
            return t("aoy.efHigh");
        case "xhigh":
            return t("aoy.efX");
        default:
            return t("aoy.efMed");
    }
}

export interface JobExample {
    pay: number;
    model: number;
    fee: number;
    isoform: number;
    keep: number;
}

/**
 * The worked example on the rate page: a 10-minute job at the owner's rate.
 * The model cost and the card fee (2.9% + 30¢) come out first; Isoform keeps
 * 25% of what's left and never less than 10% of what the client paid.
 */
export function exampleJob(rateCents: number, costCentsPerMinute: number, minutes = 10): JobExample {
    const pay = Math.max(rateCents * minutes, AOY_MINIMUM_CHARGE_CENTS);
    return { pay, ...splitFor(pay, costCentsPerMinute * minutes) };
}

/** What a payment splits into: the card fee, Isoform's share and what the owner keeps. */
export function splitFor(payCents: number, modelCents: number): Omit<JobExample, "pay"> {
    const fee = Math.round(payCents * 0.029 + 30);
    const left = payCents - modelCents - fee;
    const isoform = Math.max(Math.round(left * 0.25), Math.round(payCents * 0.1));
    return { model: modelCents, fee, isoform, keep: left - isoform };
}

/** The client's name, or "A client" where none is on record. */
export function clientName(t: TFunction<"settings">, job: { clientDisplayName?: string | null; clientLabel?: string | null } | null | undefined): string {
    const name = (job?.clientDisplayName ?? job?.clientLabel ?? "").trim();
    return name || t("aoy.noName");
}
