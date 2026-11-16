import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import i18n from "@/i18n";
import en from "@/i18n/locales/en/settings.json";
import zh from "@/i18n/locales/zh-CN/settings.json";
import { AgentOfYouPage } from "./agent-of-you-page";
import { resetAoyChromeForTests, useAoyHeader, useAoyNavTag } from "./aoy-shared";
import {
    approvalJob,
    deliveredJob,
    fixtureUpload,
    ownerBalance,
    ownerBlocked,
    ownerJobs,
    ownerPayouts,
    ownerSpeeds,
    ownerStatus,
    profileView,
    reviewJob,
} from "./aoy-fixtures";

const mocks = vi.hoisted(() => ({
    snapshots: {} as Record<string, unknown>,
    profile: null as unknown,
    action: vi.fn(),
    decision: vi.fn(),
    onboarding: vi.fn(),
    download: vi.fn(),
    getProfile: vi.fn(),
    setRemoved: vi.fn(),
    keep: vi.fn(),
    preview: vi.fn(),
    approve: vi.fn(),
    build: vi.fn(),
    specialties: vi.fn(),
    copy: vi.fn(),
    getPage: vi.fn(),
    getReservation: vi.fn(),
}));

vi.mock("@/lib/wails-shim-app", () => ({
    GetAgentOwnerSnapshot: (kind: string, id: string) => {
        const key = id ? `${kind}:${id}` : kind;
        const data = mocks.snapshots[key];
        if (data instanceof Error) return Promise.reject(data);
        return Promise.resolve({ available: data !== undefined, data: data ?? null });
    },
    AgentOwnerAction: mocks.action,
    AgentOwnerDecision: mocks.decision,
    OpenAgentOwnerPayoutOnboarding: mocks.onboarding,
    DownloadAgentOwnerReviewFile: mocks.download,
    GetAgentOwnerNeeds: () => Promise.resolve([]),
    GetAgentOwnerNeedsWithDraft: () => Promise.resolve([]),
    MarkAgentOwnerPayoutNeedShown: () => Promise.resolve(),
    GetWorkProfile: mocks.getProfile,
    EditWorkProfileLine: vi.fn(),
    SetWorkProfileLineRemoved: mocks.setRemoved,
    AddWorkProfileLine: vi.fn(),
    SetWorkProfileSpecialties: mocks.specialties,
    RenameWorkProfileSpecialty: vi.fn(),
    KeepWorkProfileLine: mocks.keep,
    DiscardWorkProfileDraft: vi.fn(),
    PreviewWorkProfileUpload: mocks.preview,
    ApproveWorkProfile: mocks.approve,
    BuildWorkProfileDraft: mocks.build,
    GetYourAgentPage: mocks.getPage,
    GetYourAgentReservation: mocks.getReservation,
    CheckYourAgentName: vi.fn(),
    ReserveYourAgent: vi.fn(),
    VerifyYourAgentCode: vi.fn(),
    ResendYourAgentCode: vi.fn(),
    ChooseYourAgentPage: vi.fn(),
    CancelYourAgentRequest: vi.fn(),
    NativeClipboardSetText: mocks.copy,
    OpenExternalURL: vi.fn(() => Promise.resolve()),
}));

const reservation = { name: "alex.rivera", email: "alex@example.com", reservedAt: "2026-10-27T12:00:00Z", status: "verified", place: 214 };
const translate = i18n.t as unknown as (key: string, options?: Record<string, unknown>) => string;
const T = (key: string, options?: Record<string, unknown>) => translate(`settings:aoy.${key}`, options);

function setup(state: string, extra: Record<string, unknown> = {}) {
    mocks.snapshots = {
        status: state ? ownerStatus(state) : undefined,
        speeds: ownerSpeeds,
        jobs: ownerJobs(),
        balance: ownerBalance(),
        payouts: ownerPayouts("enabled"),
        blocked: [],
        [`job:${reviewJob().id}`]: reviewJob(),
        [`job:${approvalJob().id}`]: approvalJob(),
        [`job:${deliveredJob().id}`]: deliveredJob(),
        ...extra,
    };
    mocks.getProfile.mockResolvedValue(profileView("approved"));
}

function NavTag() {
    const header = useAoyHeader();
    return (
        <>
            <span data-testid="tag">{useAoyNavTag() ?? "none"}</span>
            <span data-testid="lead">{header?.lead ?? ""}</span>
        </>
    );
}

async function renderPage() {
    const result = render(
        <>
            <AgentOfYouPage />
            <NavTag />
        </>,
    );
    await act(async () => {
        await Promise.resolve();
    });
    return result;
}

const runtimeListeners = new Map<string, Set<() => void>>();
function emitRuntime(event: string) {
    for (const listener of runtimeListeners.get(event) ?? []) listener();
}

beforeEach(async () => {
    vi.clearAllMocks();
    runtimeListeners.clear();
    (window as unknown as { runtime: unknown }).runtime = {
        EventsOn: (name: string, callback: () => void) => {
            if (!runtimeListeners.has(name)) runtimeListeners.set(name, new Set());
            runtimeListeners.get(name)!.add(callback);
            return () => runtimeListeners.get(name)?.delete(callback);
        },
    };
    resetAoyChromeForTests();
    await i18n.changeLanguage("en");
    mocks.action.mockResolvedValue({});
    mocks.decision.mockResolvedValue({ accepted: true, current: null });
    mocks.getPage.mockResolvedValue({ specialties: [{ name: "Client SOWs", seen: 9 }], taste: ["Price first."], stillLearning: false, nameCandidates: [], reservation, online: true, computed: true });
    mocks.getReservation.mockResolvedValue({ online: true, reservation });
    mocks.copy.mockResolvedValue(undefined);
});
afterEach(() => {
    try {
        window.localStorage.clear();
    } catch {
        // ignore
    }
});

describe("Agent of You before Bo approves", () => {
    it("keeps the reserved-name page and adds Agent of You is opening with Ask to join (o01)", async () => {
        setup("not_joined");
        await renderPage();
        expect(await screen.findByTestId("aoy-opening")).toBeTruthy();
        expect(screen.getByText(T("openL"))).toBeTruthy();
        expect(screen.getByText(T("nothingLeaves"))).toBeTruthy();
        // Everything the coming-soon page shows stays.
        expect(screen.getByTestId("your-agent-page")).toBeTruthy();
        expect(screen.getByText(i18n.t("settings:yourAgent.comingSoon"))).toBeTruthy();
        expect(screen.getByTestId("your-agent-sent")).toBeTruthy();
        expect(screen.getByTestId("tag").textContent).toBe("none");
        fireEvent.click(screen.getByRole("button", { name: T("askJoin") }));
        await waitFor(() => expect(mocks.action).toHaveBeenCalledWith("join", "", "", null));
    });

    it("shows You asked to join with the email and Cancel the request (o02)", async () => {
        setup("asked");
        await renderPage();
        const card = await screen.findByTestId("aoy-asked");
        expect(within(card).getByText(T("askedL", { email: "alex@example.com" }))).toBeTruthy();
        fireEvent.click(within(card).getByRole("button", { name: T("cancelAsk") }));
        await waitFor(() => expect(mocks.action).toHaveBeenCalledWith("cancel_join", "", "", null));
    });

    it("shows the coming-soon page alone when the owner client can't be read", async () => {
        setup("", { status: new Error("no hub") });
        await renderPage();
        expect(await screen.findByTestId("your-agent-sent")).toBeTruthy();
        expect(screen.queryByTestId("aoy-opening")).toBeNull();
    });
});

describe("Agent of You after Bo approves", () => {
    it("shows the four steps, the optional fifth, and a dimmed switch until the first three are done (o03)", async () => {
        setup("setting_up", { payouts: ownerPayouts("not_started") });
        await renderPage();
        expect(await screen.findByTestId("aoy-setup")).toBeTruthy();
        expect(screen.getByText(T("inL", { name: "alex.rivera" }))).toBeTruthy();
        for (const key of ["st1", "st2", "st3", "st4", "stPay"]) expect(screen.getByText(T(key))).toBeTruthy();
        expect((screen.getByRole("switch", { name: T("st4") }) as HTMLButtonElement).disabled).toBe(true);
        expect(screen.getByTestId("tag").textContent).toBe("setup");
    });

    it("reads How you're paid and agrees and turns on once steps one and two are done (o09)", async () => {
        setup("setting_up", { status: ownerStatus("setting_up", { profile: { version: 1, approvedAt: "2026-11-09T12:00:00Z" }, settings: { ...ownerStatus("on").settings } }) });
        await renderPage();
        fireEvent.click(await screen.findByRole("button", { name: T("read") }));
        const dialog = await screen.findByTestId("aoy-how-paid");
        expect(within(dialog).getByText(T("pay2"))).toBeTruthy();
        fireEvent.click(within(dialog).getByRole("button", { name: T("payAgree") }));
        await waitFor(() => expect(mocks.action).toHaveBeenCalledWith("agree", "", "agent-of-you-beta-2.1.3", null));
        await waitFor(() => expect(mocks.action).toHaveBeenCalledWith("on", "", "", null));
    });

    it("opens the browser only after the step before Stripe (o22)", async () => {
        setup("setting_up", { payouts: ownerPayouts("not_started") });
        mocks.onboarding.mockResolvedValue(undefined);
        await renderPage();
        fireEvent.click(await screen.findByRole("button", { name: T("setUp") }));
        const dialog = await screen.findByTestId("aoy-payout-terms");
        expect(within(dialog).getByText(T("pt2"))).toBeTruthy();
        expect(mocks.onboarding).not.toHaveBeenCalled();
        fireEvent.click(within(dialog).getByRole("button", { name: T("ptBtn") }));
        await waitFor(() => expect(mocks.onboarding).toHaveBeenCalledWith("agent-of-you-beta-2.1.3"));
    });

    it("shows On: the switch, Needs you, three figures and six rows (o10)", async () => {
        setup("on");
        mocks.getProfile.mockResolvedValue(profileView("weekly"));
        await renderPage();
        expect(await screen.findByTestId("aoy-on")).toBeTruthy();
        expect((screen.getByRole("switch", { name: T("takeJobs") }) as HTMLButtonElement).getAttribute("aria-checked")).toBe("true");
        expect(screen.getByText(T("onL", { name: "alex.rivera" }))).toBeTruthy();
        const needs = screen.getByTestId("aoy-needs");
        expect(within(needs).getByText("One-page SOW for a bakery website rebuild")).toBeTruthy();
        expect(within(needs).getByText("Hiring plan for two engineers")).toBeTruthy();
        expect(screen.getByText("$47.67")).toBeTruthy();
        expect(screen.getByText(T("mEarned"))).toBeTruthy();
        await waitFor(() => expect(screen.getByText(T("draftReady"))).toBeTruthy());
        expect(screen.getByText(T("st2Done", { rate: "2.00", speed: T("spBal"), effort: T("efMed") }))).toBeTruthy();
        expect(screen.getByText(T("optsVal", { a: T("off"), r: T("on") }))).toBeTruthy();
        expect(screen.getByText(T("none"))).toBeTruthy();
        expect(screen.getByTestId("tag").textContent).toBe("on");
        // computer-settings v1.1.0 rule 3: the section's one line under its title.
        expect(screen.getByTestId("lead").textContent).toBe(T("lead", { name: "alex.rivera" }));
    });

    it("asks before pausing only when jobs wait for the owner's OK (o28)", async () => {
        setup("on");
        await renderPage();
        fireEvent.click(await screen.findByRole("switch", { name: T("takeJobs") }));
        const dialog = await screen.findByTestId("aoy-pause-dialog");
        expect(within(dialog).getByText(T("pauseL"))).toBeTruthy();
        fireEvent.click(within(dialog).getByRole("button", { name: T("pause") }));
        await waitFor(() => expect(mocks.action).toHaveBeenCalledWith("pause", "", "", null));
    });

    it("pauses at once when nothing waits, and turns back on from Paused (o11)", async () => {
        setup("on", { jobs: [deliveredJob()] });
        const { unmount } = await renderPage();
        fireEvent.click(await screen.findByRole("switch", { name: T("takeJobs") }));
        await waitFor(() => expect(mocks.action).toHaveBeenCalledWith("pause", "", "", null));
        expect(screen.queryByTestId("aoy-pause-dialog")).toBeNull();
        unmount();
        setup("paused", { jobs: [deliveredJob()] });
        await renderPage();
        expect(await screen.findByTestId("aoy-paused")).toBeTruthy();
        expect(screen.getByTestId("tag").textContent).toBe("paused");
        fireEvent.click(screen.getByRole("switch", { name: T("takeJobs") }));
        await waitFor(() => expect(mocks.action).toHaveBeenCalledWith("on", "", "", null));
    });

    it("re-reads on agent-owner:changed instead of polling", async () => {
        setup("asked");
        await renderPage();
        await screen.findByTestId("aoy-asked");
        mocks.snapshots.status = ownerStatus("setting_up");
        await act(async () => {
            emitRuntime("agent-owner:changed");
            await Promise.resolve();
        });
        expect(await screen.findByTestId("aoy-setup")).toBeTruthy();
    });
});

async function openRow(title: string) {
    fireEvent.click(await screen.findByText(title));
    await act(async () => {
        await Promise.resolve();
    });
}

describe("Rate, speed and effort (o07, o08)", () => {
    it("shows each speed's cost, the floor line and the worked example", async () => {
        setup("on");
        await renderPage();
        await openRow(T("rse"));
        expect(await screen.findByText(T("spCost", { c: "0.15" }))).toBeTruthy();
        expect(screen.getByTestId("aoy-floor").textContent).toBe(T("costL", { speed: T("spBal"), effort: T("efMed"), c: "0.15", m: "0.30" }));
        const example = screen.getByTestId("aoy-example");
        for (const value of ["$20.00", "−$1.50", "−$0.88", "−$4.41", "$13.21"]) expect(within(example).getByText(value)).toBeTruthy();
        expect(screen.getByText(T("applyNext"))).toBeTruthy();
    });

    it("asks and offers to raise the rate when a choice needs more (o08)", async () => {
        setup("on", { status: ownerStatus("on", { settings: { ...ownerStatus("on").settings, speed: "best" } }) });
        await renderPage();
        await openRow(T("rse"));
        fireEvent.click(await screen.findByRole("radio", { name: T("efX") }));
        const dialog = await screen.findByTestId("aoy-min-dialog");
        expect(within(dialog).getByText(T("minT", { speed: T("spBest"), effort: T("efX") }))).toBeTruthy();
        fireEvent.click(within(dialog).getByRole("button", { name: T("minRaise", { m: "3.12" }) }));
        await waitFor(() => expect(mocks.action).toHaveBeenCalledWith("settings", "", "", expect.objectContaining({ speed: "best", effort: "xhigh", rateCentsPerMinute: 312 })));
    });
});

describe("Your say in each job (o12)", () => {
    it("says what On costs, saves each switch, and hides the delivered switch the server lacks", async () => {
        setup("on");
        await renderPage();
        await openRow(T("opts"));
        expect(await screen.findByText(T("oApproveOff"))).toBeTruthy();
        expect(screen.getByText(T("oReviewOn"))).toBeTruthy();
        expect(screen.queryByText(T("oNote"))).toBeNull();
        fireEvent.click(screen.getByRole("switch", { name: T("oApprove") }));
        await waitFor(() => expect(mocks.action).toHaveBeenCalledWith("settings", "", "", expect.objectContaining({ approveJobs: true, reviewResults: true })));
    });

    it("shows the delivered notification when the server has it", async () => {
        setup("on", { status: ownerStatus("on", { settings: { ...ownerStatus("on").settings, deliveredNotification: false } }) });
        await renderPage();
        await openRow(T("opts"));
        expect(await screen.findByText(T("oNoteL"))).toBeTruthy();
    });
});

describe("The work profile (o04, o05, o06, o26)", () => {
    it("shows the first run: lead, the chip note, three groups in order, and Also seen often (o04)", async () => {
        setup("setting_up");
        mocks.getProfile.mockResolvedValue(profileView("first"));
        await renderPage();
        fireEvent.click(await screen.findByRole("button", { name: T("review") }));
        expect(await screen.findByTestId("aoy-profile-first")).toBeTruthy();
        expect(screen.getByText(T("wpSrc"))).toBeTruthy();
        expect(screen.getByText(T("wpFirst"))).toBeTruthy();
        expect(screen.getByText("a client")).toBeTruthy();
        const titles = Array.from(document.querySelectorAll("[data-testid^='aoy-group-'] > h2")).map((node) => node.textContent);
        expect(titles).toEqual([T("grpBest"), T("grpHow", { spec: "Client SOWs" }), T("grpHow", { spec: "Weekly updates" }), T("grpHow", { spec: "Hiring plans" }), T("grpAll")]);
        expect(screen.getByText(T("alsoSeen", { spec: "Investor updates" }))).toBeTruthy();
        expect(screen.getByText(T("wpCount", { n: 10 }))).toBeTruthy();
        expect(screen.getAllByRole("button", { name: T("rename") })).toHaveLength(3);
    });

    it("removes a line with undo and never sends more than the preview (o05)", async () => {
        setup("setting_up");
        const view = profileView("first");
        mocks.getProfile.mockResolvedValue(view);
        const removed = { ...view, pending: { ...view.pending!, lines: view.pending!.lines.map((line) => (line.id === "l2" ? { ...line, removed: true } : line)) } };
        mocks.setRemoved.mockResolvedValue(removed);
        const upload = fixtureUpload(removed.pending!);
        mocks.preview.mockResolvedValue(upload);
        mocks.approve.mockResolvedValue({ view: profileView("approved") });
        await renderPage();
        fireEvent.click(await screen.findByRole("button", { name: T("review") }));
        const row = await screen.findByTestId("aoy-line-l2");
        fireEvent.click(within(row).getByRole("button", { name: T("remove") }));
        await waitFor(() => expect(mocks.setRemoved).toHaveBeenCalledWith("draft_1", "l2", true));
        expect(await within(screen.getByTestId("aoy-line-l2")).findByRole("button", { name: T("undo") })).toBeTruthy();
        fireEvent.click(screen.getByRole("button", { name: T("preview") }));
        const dialog = await screen.findByTestId("aoy-preview");
        expect(within(dialog).getByText(T("pvL", { n: 9, s: 3 }))).toBeTruthy();
        expect(within(dialog).getByText("Client SOWs: Price and dates go in the first paragraph.")).toBeTruthy();
        expect(within(dialog).queryByText(/List what's in/)).toBeNull();
        fireEvent.click(within(dialog).getByRole("button", { name: T("approveSend") }));
        await waitFor(() => expect(mocks.approve).toHaveBeenCalledWith("draft_1", upload));
    });

    it("shows the version conflict as a newer draft and keeps the draft", async () => {
        setup("setting_up");
        mocks.getProfile.mockResolvedValue(profileView("first"));
        mocks.preview.mockResolvedValue(fixtureUpload(profileView("first").pending!));
        mocks.approve.mockResolvedValue({ view: profileView("first"), conflictVersion: 5 });
        await renderPage();
        fireEvent.click(await screen.findByRole("button", { name: T("review") }));
        fireEvent.click(await screen.findByRole("button", { name: T("approve") }));
        expect(await screen.findByText(T("newer"))).toBeTruthy();
        expect(screen.getByTestId("aoy-profile-first")).toBeTruthy();
    });

    it("shows a failed send with Try again", async () => {
        setup("setting_up");
        mocks.getProfile.mockResolvedValue(profileView("first"));
        mocks.preview.mockRejectedValue(new Error("network"));
        await renderPage();
        fireEvent.click(await screen.findByRole("button", { name: T("review") }));
        fireEvent.click(await screen.findByRole("button", { name: T("approve") }));
        const problem = await screen.findByTestId("aoy-profile-problem");
        expect(problem.textContent).toContain(T("sendFail").split(".")[0]);
    });

    it("shows the weekly draft with only what changed, folded unchanged lines and Keep (o26)", async () => {
        setup("on");
        mocks.getProfile.mockResolvedValue(profileView("weekly"));
        mocks.keep.mockResolvedValue(profileView("weekly"));
        await renderPage();
        await openRow(T("wp"));
        expect(await screen.findByText(T("wkL", { a: 1, b: 1, c: 1, v: 4 }))).toBeTruthy();
        expect(screen.getByText(T("tagNew"))).toBeTruthy();
        expect(screen.getByText(T("tagChanged"))).toBeTruthy();
        expect(screen.getByText("Two review rounds by default; more are billed as changes.")).toBeTruthy();
        expect(screen.getByText(T("tagSuggest"))).toBeTruthy();
        expect(screen.getAllByText(T("unchanged", { n: 3 }))).toHaveLength(2);
        fireEvent.click(screen.getByRole("button", { name: T("keep") }));
        await waitFor(() => expect(mocks.keep).toHaveBeenCalledWith("draft_5", "w8"));
        expect(screen.getByRole("button", { name: T("discard") })).toBeTruthy();
        expect(screen.getByRole("button", { name: T("approveChanges") })).toBeTruthy();
    });

    it("says Still learning with one line to add (o06)", async () => {
        setup("setting_up");
        mocks.getProfile.mockResolvedValue(profileView("learning"));
        await renderPage();
        fireEvent.click(await screen.findByRole("button", { name: T("review") }));
        expect(await screen.findByTestId("aoy-profile-learning")).toBeTruthy();
        expect(screen.getByText(T("wpLearning"))).toBeTruthy();
        expect(screen.getByRole("button", { name: T("addLine") })).toBeTruthy();
    });

    it("offers Draft now once in 24 hours", async () => {
        setup("on");
        mocks.getProfile.mockResolvedValue(profileView("approved"));
        await renderPage();
        await openRow(T("wp"));
        const line = await screen.findByTestId("aoy-draft-now");
        expect(line.textContent).toContain(T("draftWait", { when: "" }).trim().slice(0, 12));
        expect(within(line).queryByRole("button", { name: T("draftNow") })).toBeNull();
    });
});

describe("Jobs (o13 to o20)", () => {
    it("groups Open and Done with each job's state (o13)", async () => {
        setup("on");
        await renderPage();
        await openRow(T("jobs"));
        expect(await screen.findByText(T("jOpen"))).toBeTruthy();
        expect(screen.getByText(T("jDone"))).toBeTruthy();
        expect(screen.getByText(T("jDelivered", { min: 14, mins: "14 minutes", amt: "18.59" }))).toBeTruthy();
        expect(screen.getByText(T("jDeclined"))).toBeTruthy();
        expect(screen.getAllByText(T("noName", {}), { exact: false }).length).toBeGreaterThan(0);
    });

    it("shows the empty list", async () => {
        setup("on", { jobs: [] });
        await renderPage();
        await openRow(T("jobs"));
        expect(await screen.findByTestId("aoy-jobs-empty")).toBeTruthy();
    });

    it("accepts or declines with an optional note on the approval card (o14, o15)", async () => {
        setup("on");
        await renderPage();
        await openRow("Hiring plan for two engineers");
        const card = await screen.findByTestId("aoy-approval-card");
        expect(within(card).getByText(T("apAsk", { client: "Sam Patel" }))).toBeTruthy();
        expect(within(card).getByText(/A two-page hiring plan/)).toBeTruthy();
        fireEvent.click(within(card).getByRole("button", { name: T("decline") }));
        const dialog = await screen.findByTestId("aoy-decline-dialog");
        expect(within(dialog).getByText(T("dcL", { client: "Sam Patel" }))).toBeTruthy();
        fireEvent.change(within(dialog).getByRole("textbox"), { target: { value: "Not this quarter." } });
        fireEvent.click(within(dialog).getByRole("button", { name: T("decline") }));
        await waitFor(() => expect(mocks.decision).toHaveBeenCalledWith({ action: "decline", jobId: "chat_hiring", passId: "pass_h", decisionVersion: 1, note: "Not this quarter." }));
    });

    it("decides from the waiting pass when the server sends no approval card", async () => {
        const job = { ...approvalJob(), approval: undefined };
        setup("on", { [`job:${job.id}`]: job });
        await renderPage();
        await openRow("Hiring plan for two engineers");
        const card = await screen.findByTestId("aoy-approval-card");
        fireEvent.click(within(card).getByRole("button", { name: T("accept") }));
        await waitFor(() => expect(mocks.decision).toHaveBeenCalledWith({ action: "accept", jobId: "chat_hiring", passId: "pass_h", decisionVersion: 1, note: undefined }));
    });

    it("sends, redoes with what's left, or declines at review (o16, o17, o18)", async () => {
        setup("on");
        await renderPage();
        await openRow("One-page SOW for a bakery website rebuild");
        const card = await screen.findByTestId("aoy-review-card");
        expect(within(card).getByText("Lumen-website-SOW.pdf")).toBeTruthy();
        expect(screen.getByText(T("rvFacts", { mins: "14 minutes", client: "Maria Chen", cost: "28.00", amt: "18.59" }))).toBeTruthy();
        fireEvent.click(screen.getByRole("button", { name: T("redo") }));
        expect(await screen.findByText(T("rdL", { mins: "8 minutes", amt: "17.00", client: "Maria Chen" }))).toBeTruthy();
        fireEvent.change(screen.getByLabelText(T("rdT")), { target: { value: "Add a payment schedule." } });
        fireEvent.click(screen.getByRole("button", { name: T("rdBtn") }));
        await waitFor(() => expect(mocks.decision).toHaveBeenCalledWith({ action: "redo", jobId: "chat_bakery", passId: "pass_1", decisionVersion: 2, note: "Add a payment schedule." }));
        fireEvent.click(screen.getByRole("button", { name: T("decline") }));
        const dialog = await screen.findByTestId("aoy-decline-review-dialog");
        fireEvent.click(within(dialog).getByRole("button", { name: T("dvBtn") }));
        await waitFor(() => expect(mocks.decision).toHaveBeenCalledWith(expect.objectContaining({ action: "decline", passId: "pass_1" })));
        fireEvent.click(screen.getByRole("button", { name: T("send") }));
        await waitFor(() => expect(mocks.decision).toHaveBeenCalledWith(expect.objectContaining({ action: "send" })));
    });

    it("replaces Redo with a line when too little is left", async () => {
        const job = reviewJob();
        setup("on", { [`job:${job.id}`]: reviewJob({ review: { ...job.review!, canRedo: false } }) });
        await renderPage();
        await openRow(job.title!);
        expect(await screen.findByText(T("rdNone", { client: "Maria Chen" }))).toBeTruthy();
        expect(screen.queryByRole("button", { name: T("redo") })).toBeNull();
    });

    it("shows the current state when a decision is stale (409)", async () => {
        setup("on");
        mocks.decision.mockResolvedValue({ accepted: false, current: { passId: "pass_1", state: "delivered", decisionVersion: 3 } });
        await renderPage();
        await openRow("One-page SOW for a bakery website rebuild");
        fireEvent.click(await screen.findByRole("button", { name: T("send") }));
        await waitFor(() => expect(screen.getByRole("status")).toBeTruthy());
    });

    it("shows what a delivered job paid and the terms it ran on (o19)", async () => {
        setup("on");
        await renderPage();
        await openRow(T("jobs"));
        fireEvent.click(await screen.findByTestId("aoy-job-chat_delivered"));
        const split = await screen.findByTestId("aoy-split");
        for (const value of ["$28.00", "−$2.10", "−$1.11", "−$6.20", "$18.59"]) expect(within(split).getByText(value)).toBeTruthy();
        expect(screen.getByText(T("profV", { v: 4, speed: T("spBal"), effort: T("efMed"), rate: "2.00" }))).toBeTruthy();
    });
});

describe("Blocked clients (o20, o25)", () => {
    it("lists blocked clients with Unblock", async () => {
        setup("on", { blocked: ownerBlocked() });
        await renderPage();
        await openRow(T("blocked"));
        expect(await screen.findByText("Chris Doe")).toBeTruthy();
        fireEvent.click(screen.getByRole("button", { name: T("unblock") }));
        await waitFor(() => expect(mocks.action).toHaveBeenCalledWith("unblock", "client_x", "", null));
    });

    it("says nobody is blocked", async () => {
        setup("on");
        await renderPage();
        await openRow(T("blocked"));
        expect(await screen.findByText(T("blNone"))).toBeTruthy();
    });
});

describe("Balance and payouts (o21 to o24)", () => {
    it.each([
        ["not_started", "balSetT"],
        ["restricted", "balNeedsT"],
        ["unsupported", "balCountry"],
        ["enabled", "balBelow"],
    ])("shows the %s state in its own words", async (status, key) => {
        setup("on", { payouts: ownerPayouts(status) });
        await renderPage();
        await openRow(T("bal"));
        expect(await screen.findByText(T(key, { min: "10.00" }))).toBeTruthy();
    });

    it("shows Available, On its way and the activity list when payouts are set up (o23)", async () => {
        setup("on");
        await renderPage();
        await openRow(T("bal"));
        expect(await screen.findByText(T("balPending"))).toBeTruthy();
        const ledger = screen.getByTestId("aoy-ledger");
        expect(within(ledger).getAllByText(T("lgJob"))).toHaveLength(2);
        expect(within(ledger).getByText(T("lgExpired"))).toBeTruthy();
    });

    it("says what is owed below zero", async () => {
        setup("on", { balance: ownerBalance({ balanceCents: -210, availableCents: undefined }), payouts: ownerPayouts("not_started", -210) });
        await renderPage();
        await openRow(T("bal"));
        expect(await screen.findByText(T("balOwed", { amt: "2.10" }))).toBeTruthy();
    });
});

describe("Agent of You words", () => {
    it("has the same keys in en and zh-CN, and keeps the product name in English", () => {
        const enKeys = Object.keys((en as { aoy: Record<string, string> }).aoy).sort();
        const zhKeys = Object.keys((zh as { aoy: Record<string, string> }).aoy).sort();
        expect(zhKeys).toEqual(enKeys);
        expect((zh as { aoy: Record<string, string> }).aoy.aoy).toBe("Agent of You");
        // The short job rows say "{min} min" in English and the whole quantity in zh-CN (v2.1.1).
        const vars = (value: string) => (value.match(/\{\{\w+\}\}/g) ?? []).map((name) => (name === "{{min}}" ? "{{mins}}" : name)).sort();
        for (const key of enKeys) {
            const left = vars((en as { aoy: Record<string, string> }).aoy[key]);
            const right = vars((zh as { aoy: Record<string, string> }).aoy[key]);
            expect([...new Set(right)], key).toEqual([...new Set(left)]);
        }
    });

    it("draws the On page in zh-CN", async () => {
        await i18n.changeLanguage("zh-CN");
        setup("on");
        await renderPage();
        expect(await screen.findByText("接受新委托")).toBeTruthy();
        expect(screen.getByText("需要你")).toBeTruthy();
    });
});
