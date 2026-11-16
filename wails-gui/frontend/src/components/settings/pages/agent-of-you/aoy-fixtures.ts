// Fixtures of the owner bindings' results, drawn from design's example
// (handoff v2.1.3): Alex Rivera's page, Maria Chen's bakery SOW at $2.00 a
// minute. Used by the component tests and the screenshot harness.
import type {
    AgentOwnerBalance,
    AgentOwnerBlockedClient,
    AgentOwnerJob,
    AgentOwnerPayouts,
    AgentOwnerSpeed,
    AgentOwnerStatus,
} from "@/lib/agent-owner";
import type { WorkProfileDraft, WorkProfileView } from "@/lib/work-profile";

const HOUR = 60 * 60 * 1000;
export const fixtureNow = () => Date.now();
const iso = (offsetHours: number) => new Date(fixtureNow() + offsetHours * HOUR).toISOString();

export function ownerStatus(state: string, overrides: Partial<AgentOwnerStatus> = {}): AgentOwnerStatus {
    const ready = state === "on" || state === "paused";
    return {
        state,
        name: "alex.rivera",
        settings: { rateCentsPerMinute: ready ? 200 : null, speed: "balanced", effort: "medium", approveJobs: false, reviewResults: ready, version: 3 },
        profile: ready ? { version: 4, approvedAt: "2026-11-02T17:00:00Z", learnedThrough: "2026-11-01T00:00:00Z", publicSpecialties: ["Client SOWs", "Weekly updates", "Hiring plans"] } : null,
        minimumRateCents: 30,
        payout: { status: "not_started" },
        paymentAgreement: ready ? { termsVersion: "agent-of-you-beta-2.1.3", acceptedAt: "2026-11-02T17:05:00Z" } : null,
        ...overrides,
    };
}

const COSTS: Record<string, Record<string, number>> = {
    fast: { low: 2, medium: 4, high: 6, xhigh: 8 },
    balanced: { low: 10, medium: 15, high: 22, xhigh: 30 },
    best: { low: 40, medium: 60, high: 95, xhigh: 156 },
};
export const ownerSpeeds: AgentOwnerSpeed[] = Object.entries(COSTS).flatMap(([speed, efforts]) =>
    Object.entries(efforts).map(([effort, typical]) => ({ speed, effort, typicalCentsPerMinute: typical, minimumRateCents: typical * 2 })),
);

export function reviewJob(overrides: Partial<AgentOwnerJob> = {}): AgentOwnerJob {
    return {
        id: "chat_bakery",
        state: "review",
        clientLabel: "Maria Chen",
        title: "One-page SOW for a bakery website rebuild",
        createdAt: iso(-3),
        updatedAt: iso(-1),
        clientId: "client_maria",
        messages: [
            { id: "m1", chatId: "chat_bakery", kind: "client", text: "We need a one-page SOW for rebuilding Lumen Bakery's website: online orders, a menu page and a blog.", fileIds: [], createdAt: iso(-3) },
            { id: "m2", chatId: "chat_bakery", kind: "quote", text: "", fileIds: [], createdAt: iso(-2.9), deliverable: "A one-page SOW with scope, milestones and a blank price line", estimatedMinutes: 15, rateCentsPerMinute: 200 },
            { id: "m3", chatId: "chat_bakery", kind: "agent", text: "Here's your SOW. Online orders go live in week 3, the menu and blog in week 5, and the price line is blank for you. Want changes? Say so here.", fileIds: [], createdAt: iso(-1) },
        ],
        review: {
            passId: "pass_1",
            decisionVersion: 2,
            deadline: iso(22),
            workedSeconds: 14 * 60,
            limitCents: 4500,
            files: [
                { path: "Lumen-website-SOW.pdf", size: 212 * 1024 },
                { path: "Lumen-website-SOW.docx", size: 84 * 1024 },
            ],
            minutesLeft: 8,
            centsLeft: 1700,
            canRedo: true,
        },
        passes: [{ id: "pass_1", state: "review", decisionVersion: 2, profileVersion: 4, speed: "balanced", effort: "medium", rateCentsPerMinute: 200, split: null }],
        ...overrides,
    };
}

export function approvalJob(): AgentOwnerJob {
    return {
        id: "chat_hiring",
        state: "waiting_owner",
        clientLabel: "Sam Patel",
        title: "Hiring plan for two engineers",
        createdAt: iso(-0.5),
        updatedAt: iso(-0.4),
        clientId: "client_sam",
        messages: [
            { id: "h1", chatId: "chat_hiring", kind: "client", text: "A hiring plan for two backend engineers by the end of Q1, with a budget table.", fileIds: [], createdAt: iso(-0.5) },
            { id: "h2", chatId: "chat_hiring", kind: "quote", text: "", fileIds: [], createdAt: iso(-0.45), deliverable: "A two-page hiring plan with roles, timeline and a budget table", estimatedMinutes: 12, rateCentsPerMinute: 200 },
        ],
        approval: { passId: "pass_h", decisionVersion: 1, deadline: iso(23), ...({ limitCents: 4000, estimatedMinutes: 12 } as object) },
        passes: [{ id: "pass_h", state: "waiting_owner", decisionVersion: 1, profileVersion: 4, speed: "balanced", effort: "medium", rateCentsPerMinute: 200, split: null }],
    };
}

export function deliveredJob(): AgentOwnerJob {
    return {
        ...reviewJob(),
        id: "chat_delivered",
        state: "delivered",
        title: "One-page SOW for a bakery website rebuild",
        updatedAt: "2026-11-08T21:21:00Z",
        review: undefined,
        messages: [
            { id: "d1", chatId: "chat_delivered", kind: "client", text: "We need a one-page SOW.", fileIds: [], createdAt: iso(-30) },
            { id: "d2", chatId: "chat_delivered", kind: "receipt", text: "", fileIds: [], createdAt: iso(-26), receipt: { passId: "pass_d", chargeCents: 2800, workedSeconds: 14 * 60, rateCentsPerMinute: 200 } },
        ],
        passes: [{ id: "pass_d", state: "delivered", profileVersion: 4, speed: "balanced", effort: "medium", rateCentsPerMinute: 200, split: { clientChargeCents: 2800, modelCostCents: 210, cardFeeCents: 111, isoformCents: 620, ownerShareCents: 1859 } }],
    };
}

export const ownerJobs = (): AgentOwnerJob[] => [
    reviewJob(),
    approvalJob(),
    { id: "chat_update", state: "working", clientLabel: "Jordan Lee", title: "Weekly update for the platform team", createdAt: iso(-0.2), updatedAt: iso(-0.1), clientId: "client_jordan" },
    deliveredJob(),
    { id: "chat_declined", state: "declined", clientLabel: null, title: "Investor update draft", createdAt: iso(-50), updatedAt: iso(-49) },
];

export function ownerBalance(overrides: Partial<AgentOwnerBalance> = {}): AgentOwnerBalance {
    const thisMonth = new Date();
    const at = (day: number) => new Date(thisMonth.getFullYear(), thisMonth.getMonth(), day, 14).toISOString();
    return {
        earnedCents: 21460,
        owedCents: 0,
        transferredCents: 16693,
        balanceCents: 4767,
        availableCents: 4767,
        onTheWayCents: 1859,
        lines: [
            { id: 4, passId: "pass_d", clientId: "client_maria", kind: "job", chargeCents: 2800, modelCostNanocents: 0, cardFeeCents: 111, isoformCents: 620, ownerShareCents: 1859, debtCents: 0, createdAt: at(Math.min(8, thisMonth.getDate())) },
            { id: 3, passId: null, clientId: null, kind: "free_questions", chargeCents: 0, modelCostNanocents: 0, cardFeeCents: 0, isoformCents: 0, ownerShareCents: -12, debtCents: 0, createdAt: at(Math.min(6, thisMonth.getDate())) },
            { id: 2, passId: "pass_x", clientId: "client_x", kind: "review_expired", chargeCents: 0, modelCostNanocents: 0, cardFeeCents: 0, isoformCents: 0, ownerShareCents: -210, debtCents: 0, createdAt: at(Math.min(4, thisMonth.getDate())) },
            { id: 1, passId: "pass_y", clientId: "client_y", kind: "job", chargeCents: 28000, modelCostNanocents: 0, cardFeeCents: 842, isoformCents: 6000, ownerShareCents: 19813, debtCents: 0, createdAt: at(1) },
        ],
        ...overrides,
    };
}

export const ownerPayouts = (status: string, balanceCents = 4767): AgentOwnerPayouts => ({ status, requirements: status === "restricted" ? ["individual.verification.document"] : [], balanceCents });

export const ownerBlocked = (): AgentOwnerBlockedClient[] => [{ clientId: "client_x", clientDisplayName: "Chris Doe", blockedAt: "2026-11-05T18:00:00Z" }];

const specialties = (selected = true) => [
    { id: "sp_sows", name: "Client SOWs", selected },
    { id: "sp_updates", name: "Weekly updates", selected },
    { id: "sp_hiring", name: "Hiring plans", selected },
    { id: "sp_investor", name: "Investor updates", selected: false },
];

function line(id: string, group: string, text: string, status = "new", extra: Partial<WorkProfileDraft["lines"][number]> = {}) {
    return { id, group, text, status, filtered: false, removed: false, ...extra };
}

export function firstDraft(): WorkProfileDraft {
    return {
        id: "draft_1",
        madeAt: "2026-11-09T15:00:00Z",
        learnedThrough: "2026-11-08T00:00:00Z",
        basedOnVersion: 0,
        fingerprint: "f1",
        edited: false,
        specialties: specialties(),
        lines: [
            line("l1", "sp_sows", "Price and dates go in the first paragraph."),
            line("l2", "sp_sows", "List what's in and what's out side by side."),
            line("l3", "sp_sows", "Two review rounds by default; more are billed as changes."),
            line("l4", "sp_sows", "Send the draft to the client with a day to reply by.", "new", { filtered: true }),
            line("l5", "sp_updates", "Three parts: shipped, slipped, next."),
            line("l6", "sp_updates", "Every slipped item gets a new date and an owner."),
            line("l7", "sp_hiring", "Start from the problem the hire solves, then the role."),
            line("l8", "", "Lead with what changed, not what happened."),
            line("l9", "", "Put the decision you need in the first line."),
            line("l10", "", "Short sentences. Use the reader's words, not your own jargon."),
        ],
    };
}

export function weeklyDraft(): WorkProfileDraft {
    const base = firstDraft();
    return {
        ...base,
        id: "draft_5",
        basedOnVersion: 4,
        lines: [
            line("w1", "sp_sows", "State the payment schedule next to the price.", "new"),
            line("w2", "sp_sows", "Two review rounds by default; a third is billed as a change.", "changed", { previous: "Two review rounds by default; more are billed as changes." }),
            line("w3", "sp_sows", "Price and dates go in the first paragraph.", "unchanged"),
            line("w4", "sp_sows", "List what's in and what's out side by side.", "unchanged"),
            line("w5", "sp_sows", "Send the draft to the client with a day to reply by.", "unchanged"),
            line("w6", "sp_updates", "Three parts: shipped, slipped, next.", "unchanged"),
            line("w7", "sp_hiring", "Start from the problem the hire solves, then the role.", "unchanged"),
            line("w8", "", "Short sentences. Use the reader's words, not your own jargon.", "suggested_removal", { lastSeen: "2026-09-01T00:00:00Z" }),
            line("w9", "", "Lead with what changed, not what happened.", "unchanged"),
            line("w10", "", "Put the decision you need in the first line.", "unchanged"),
            line("w11", "", "Name one owner for every open item.", "unchanged"),
        ],
    };
}

export function profileView(kind: "first" | "weekly" | "learning" | "approved"): WorkProfileView {
    const next = iso(72);
    switch (kind) {
        case "first":
            return { approvedVersion: 0, pending: firstDraft(), newerReady: false, building: false, nextDraftAt: next };
        case "weekly":
            return { approvedVersion: 4, approvedAt: "2026-11-02T17:00:00Z", approved: { ...firstDraft(), id: "approved_4", basedOnVersion: 3 }, pending: weeklyDraft(), newerReady: false, building: false, lastDraftAt: iso(-30), nextDraftAt: next };
        case "learning":
            return { approvedVersion: 0, newerReady: false, building: false, nextDraftAt: next };
        case "approved":
            return { approvedVersion: 4, approvedAt: "2026-11-02T17:00:00Z", approved: { ...firstDraft(), id: "approved_4" }, newerReady: false, building: false, lastDraftAt: iso(-3), nextDraftAt: next };
    }
}

export const fixtureUpload = (draft: WorkProfileDraft) => {
    const names = new Map(draft.specialties.map((specialty) => [specialty.id, specialty.name]));
    return {
        lines: draft.lines.filter((item) => !item.removed && item.status !== "suggested_removal").map((item) => (names.get(item.group ?? "") ? `${names.get(item.group ?? "")}: ${item.text}` : item.text)),
        publicSpecialties: draft.specialties.filter((specialty) => specialty.selected).map((specialty) => specialty.name),
        learnedThrough: draft.learnedThrough,
        expectedVersion: draft.basedOnVersion,
    };
};
