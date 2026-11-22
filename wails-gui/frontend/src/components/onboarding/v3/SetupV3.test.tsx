import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import i18n from "@/i18n";
import type { SetupPermissions } from "@/lib/setup-backend";
import { SetupV3 } from "./SetupV3";
import { seesRowState } from "./SeesStep";
import { resumeStageV3, SEES_V3_ROWS } from "./steps";

const events = vi.hoisted(() => new Map<string, (data?: unknown) => void>());
const mocks = vi.hoisted(() => ({
    permissions: vi.fn(),
    requestRow: vi.fn(),
    requestAll: vi.fn(),
    request: vi.fn(),
    load: vi.fn(),
    save: vi.fn(),
    close: vi.fn(),
    defer: vi.fn(),
    plan: vi.fn(),
    claims: vi.fn(),
    confirm: vi.fn(),
    notQuite: vi.fn(),
    work: vi.fn(),
    answerWork: vi.fn(),
    topic: vi.fn(),
    yourAgents: vi.fn(),
    setAgent: vi.fn(),
    connectHere: vi.fn(),
    start: vi.fn(),
    downloads: vi.fn(),
    signIn: vi.fn(),
    openURL: vi.fn(),
    install: vi.fn(),
    probe: { current: { agents: [] } as { agents: Array<{ id: string; installed: boolean; signedIn: boolean }> } },
}));
vi.mock("@/lib/wails-shim-app", () => ({
    GetSetupPermissions: mocks.permissions,
    RequestSetupPermissions: mocks.requestRow,
    RequestAllPermissions: mocks.requestAll,
    RequestPermission: mocks.request,
    OpenPermissionSettings: vi.fn().mockResolvedValue(undefined),
    GetScreenRecordingRelaunch: vi.fn().mockResolvedValue({ needed: false }),
    RelaunchApp: vi.fn(),
    LoadUIState: mocks.load,
    SaveUIState: mocks.save,
    CloseMainChatSetupSteps: mocks.close,
    SetSetupWindowMode: vi.fn().mockResolvedValue(undefined),
    RevealMainWindow: vi.fn().mockResolvedValue(undefined),
    SetMenuBarMarkVisible: vi.fn().mockResolvedValue(undefined),
    PlaySetupFinish: vi.fn(),
    GetTelemetryTier: vi.fn().mockResolvedValue("basic"),
    SetTelemetryTier: vi.fn().mockResolvedValue(undefined),
    GetMobileConnectionStatus: vi.fn().mockResolvedValue({ enabled: true, devices: [] }),
    GenerateMobileConnectionCode: vi.fn().mockResolvedValue({ enabled: true, devices: [], connectionCode: "isoform://pair#test" }),
    EnableMobileConnectionUsingRelay: vi.fn(),
    ConfirmMobilePairing: vi.fn(),
    GetModelChoices: vi.fn().mockResolvedValue({ jobs: [] }),
    PlanSetupModelDownload: mocks.plan,
    StartModelDownloads: mocks.start,
    GetModelDownloads: mocks.downloads,
    OpenAssistantSignIn: mocks.signIn,
    OpenExternalURL: mocks.openURL,
    SetupInstallAssistant: mocks.install,
    DeferModelDownloads: mocks.defer,
    BuildSetupClaims: mocks.claims,
    ConfirmMemoryEntry: mocks.confirm,
    NotQuiteMemoryEntry: mocks.notQuite,
    UndoNotQuiteMemoryEntry: vi.fn().mockResolvedValue(undefined),
    ReviseSetupClaim: vi.fn().mockResolvedValue(undefined),
    GetWorkList: mocks.work,
    AnswerWork: mocks.answerWork,
    UpdateUserTopic: mocks.topic,
    GetYourAgents: mocks.yourAgents,
    SetYourAgent: mocks.setAgent,
    ConnectAgentHere: mocks.connectHere,
    StartAgentJoin: vi.fn().mockResolvedValue({ message: "Join my Isoform: read isoform.ai/j/abc", link: "x", expiresAt: Date.now() + 600_000, local: false }),
    GetAgentJoin: vi.fn().mockResolvedValue({ state: "waiting", expiresAt: Date.now() + 600_000 }),
    AnswerAgentJoin: vi.fn().mockResolvedValue(undefined),
    CancelAgentJoin: vi.fn().mockResolvedValue(undefined),
    NativeClipboardSetText: vi.fn().mockResolvedValue(undefined),
}));
vi.mock("../v2/useAgentProbe", () => ({
    useAgentProbe: () => ({ probe: mocks.probe.current, ready: true, refresh: vi.fn() }),
}));
vi.mock("../v2/scenes/YourAIScene", () => ({ YourAIScene: () => <div data-testid="your-ai-stub" /> }));
vi.mock("../../../../wailsjs/runtime/runtime", () => ({
    EventsOn: vi.fn((name: string, fn: (data?: unknown) => void) => {
        events.set(name, fn);
        return () => events.delete(name);
    }),
}));

const plan = (id: string, over: Partial<{ freeBytes: number; enoughSpace: boolean }> = {}) => ({
    planId: id,
    choices: {},
    requiredBytes: 0,
    items: [{ id, bytes: 1e9 }],
    totalBytes: 1e9,
    freeBytes: 5e10,
    enoughSpace: true,
    modelsFolder: "",
    ...over,
});
const quietPlan = () => plan("privacy-m");
const speechPlan = (over: Partial<{ freeBytes: number; enoughSpace: boolean }> = {}) => ({ ...plan("m", over), planId: "speech" });
const SPEECH_SKIP = ["live", "speakerPack", "afterCall", "voice"];
const QUIET_SKIP = ["privacy", "memory"];
const micOn = () =>
    perms([
        { key: "microphone", state: "on" },
        { key: "system_audio", state: "on" },
    ]);

const perms = (answers: SetupPermissions["answers"], running = false): SetupPermissions => ({
    answers,
    running,
    resumeStep: "sees",
    next: 0,
});

beforeEach(async () => {
    vi.clearAllMocks();
    events.clear();
    await i18n.changeLanguage("en");
    mocks.permissions.mockResolvedValue(perms([]));
    mocks.requestRow.mockResolvedValue(undefined);
    mocks.request.mockResolvedValue(false);
    mocks.load.mockResolvedValue("");
    mocks.save.mockResolvedValue(undefined);
    mocks.close.mockResolvedValue(undefined);
    mocks.defer.mockResolvedValue(undefined);
    mocks.claims.mockResolvedValue({ status: "nothing", claims: [] });
    mocks.confirm.mockResolvedValue({});
    mocks.notQuite.mockResolvedValue(42);
    mocks.work.mockResolvedValue({ toAnswer: 0, items: [] });
    mocks.answerWork.mockResolvedValue({});
    mocks.topic.mockResolvedValue({ topics: [], removed: [] });
    const found = { id: "claude-code", name: "Claude Code", kind: "code", reach: "here", found: true, connected: false };
    mocks.yourAgents.mockResolvedValue({ choices: { code: "", else: "" }, agents: [found] });
    mocks.connectHere.mockResolvedValue({ choices: { code: "", else: "" }, agents: [{ ...found, connected: true }] });
    mocks.setAgent.mockResolvedValue({ choices: { code: "claude-code", else: "" }, agents: [{ ...found, connected: true }] });
    mocks.start.mockResolvedValue(undefined);
    mocks.downloads.mockResolvedValue([]);
    mocks.signIn.mockResolvedValue(undefined);
    mocks.install.mockResolvedValue({ id: "claude", installed: true, signedIn: false });
    mocks.probe.current = { agents: [] };
    mocks.openURL.mockResolvedValue(undefined);
    // The quiet plan (speech jobs skipped) holds privacy and memory; the
    // speech plan (privacy and memory skipped) holds the speech model "m".
    mocks.plan.mockImplementation(async (_choices: unknown, skip: string[]) =>
        skip.includes("live") ? quietPlan() : speechPlan(),
    );
});

describe("computer setup v1.0.0", () => {
    it("shows the four screens before anything is asked", async () => {
        render(<SetupV3 onFinish={vi.fn()} onSkip={vi.fn()} />);
        for (const n of [1, 2, 3, 4]) {
            expect(await screen.findByTestId(`setup-v3-intro-${n}`)).toBeTruthy();
            fireEvent.click(screen.getByTestId("setup-v3-continue"));
        }
        expect(await screen.findByTestId("setup-v3-sees")).toBeTruthy();
        expect(mocks.requestRow).not.toHaveBeenCalled();
        expect(mocks.requestAll).not.toHaveBeenCalled();
        expect(mocks.request).not.toHaveBeenCalled();
    });

    it("asks only the row whose Allow was pressed", async () => {
        mocks.load.mockResolvedValue("sees");
        render(<SetupV3 onFinish={vi.fn()} onSkip={vi.fn()} />);
        fireEvent.click(await screen.findByTestId("setup-v3-allow-mail"));
        await waitFor(() => expect(mocks.requestRow).toHaveBeenCalledWith(["automation_mail"]));
        expect(mocks.requestRow).toHaveBeenCalledTimes(1);
        expect(mocks.requestAll).not.toHaveBeenCalled();
        act(() => {
            events.get("setup:permissions")?.(perms([{ key: "automation_mail", state: "on" }]));
        });
        expect(await screen.findByText("Allowed")).toBeTruthy();
        expect(screen.getByTestId("setup-v3-so-far").textContent).toContain("your mail");
    });

    it("lets every step be skipped and finishes on the last one", async () => {
        const onFinish = vi.fn();
        mocks.load.mockResolvedValue("sees");
        render(<SetupV3 onFinish={onFinish} onSkip={vi.fn()} />);
        await screen.findByTestId("setup-v3-sees");
        // Step 2 with nothing allowed moves on by itself (setup-answers answer 6).
        for (const id of ["setup-v3-today", "setup-v3-agents", "setup-v3-phone"]) {
            fireEvent.click(screen.getByTestId("setup-v3-skip-step"));
            expect(await screen.findByTestId(id, {}, { timeout: 3000 })).toBeTruthy();
        }
        // Microphone was skipped: no speech model is planned or started, and
        // privacy and memory are still deferred quietly.
        expect(mocks.plan).toHaveBeenCalledTimes(1);
        expect(mocks.plan).toHaveBeenCalledWith(expect.anything(), SPEECH_SKIP);
        expect(mocks.plan).not.toHaveBeenCalledWith(expect.anything(), QUIET_SKIP);
        expect(mocks.start).not.toHaveBeenCalled();
        expect(mocks.defer).toHaveBeenCalledTimes(1);
        expect(mocks.defer).toHaveBeenCalledWith(expect.objectContaining({ planId: "privacy-m" }));
        // A skipped step 1 leaves the main chat's asks open for later.
        expect(mocks.close).not.toHaveBeenCalledWith(expect.arrayContaining(["calendars"]));
        expect(screen.getByTestId("setup-v3-next").textContent).toContain("Finish setup");
        fireEvent.click(screen.getByTestId("setup-v3-next"));
        expect(onFinish).toHaveBeenCalledTimes(1);
    });
});

describe("step 2 claims", () => {
    it("answers each claim as You does, with Y and N on the first open one", async () => {
        mocks.load.mockResolvedValue("right");
        mocks.permissions.mockResolvedValue(perms([{ key: "calendars", state: "on" }]));
        mocks.claims.mockResolvedValue({
            status: "ready",
            claims: [
                { id: "memory-setup-a", text: "You meet Lena most weeks.", source: "calendar", at: Date.UTC(2026, 10, 14) },
                { id: "memory-setup-b", text: "Priya runs Thursday's review.", source: "mail", at: Date.UTC(2026, 10, 13) },
            ],
        });
        render(<SetupV3 onFinish={vi.fn()} onSkip={vi.fn()} />);
        expect(await screen.findByText("You meet Lena most weeks.")).toBeTruthy();
        expect(screen.getByText(/From your calendar, /)).toBeTruthy();
        fireEvent.keyDown(window, { key: "y" });
        await waitFor(() => expect(mocks.confirm).toHaveBeenCalledWith("memory-setup-a"));
        fireEvent.keyDown(window, { key: "n" });
        await waitFor(() => expect(mocks.notQuite).toHaveBeenCalledWith("memory-setup-b"));
        expect(await screen.findByText("Kept")).toBeTruthy();
    });

    it("with calendar allowed and nothing read, marks the rail Later · on Today and opens step 3", async () => {
        mocks.load.mockResolvedValue("right");
        mocks.permissions.mockResolvedValue(perms([{ key: "calendars", state: "on" }]));
        render(<SetupV3 onFinish={vi.fn()} onSkip={vi.fn()} />);
        expect(await screen.findByTestId("setup-v3-right-reading")).toBeTruthy();
        expect(await screen.findByTestId("setup-v3-today", {}, { timeout: 3000 })).toBeTruthy();
        expect(mocks.claims).toHaveBeenCalledTimes(1);
        expect(screen.getByTestId("setup-v3-rail-right-note").textContent).toBe("Later · on Today");
        expect(screen.queryByText(/Nothing to check yet/)).toBeNull();
    });

    it("with calendar and mail not allowed, says the step needs them and reads nothing", async () => {
        mocks.load.mockResolvedValue("right");
        mocks.permissions.mockResolvedValue(
            perms([
                { key: "calendars", state: "denied" },
                { key: "automation_mail", state: "off" },
            ]),
        );
        render(<SetupV3 onFinish={vi.fn()} onSkip={vi.fn()} />);
        expect(await screen.findByTestId("setup-v3-today", {}, { timeout: 3000 })).toBeTruthy();
        expect(screen.getByTestId("setup-v3-rail-right-note").textContent).toBe("Needs calendar and mail");
        expect(mocks.claims).not.toHaveBeenCalled();
    });

    it("shows a failed read with Try again instead of skipping", async () => {
        mocks.load.mockResolvedValue("right");
        mocks.permissions.mockResolvedValue(perms([{ key: "automation_mail", state: "on" }]));
        mocks.claims.mockRejectedValueOnce(new Error("store"));
        render(<SetupV3 onFinish={vi.fn()} onSkip={vi.fn()} />);
        expect(await screen.findByTestId("setup-v3-right-failed")).toBeTruthy();
        expect(screen.queryByTestId("setup-v3-today")).toBeNull();
        mocks.claims.mockResolvedValueOnce({
            status: "ready",
            claims: [{ id: "memory-setup-a", text: "You meet Lena most weeks.", source: "mail", at: Date.UTC(2026, 10, 14) }],
        });
        fireEvent.click(screen.getByText("Try again"));
        expect(await screen.findByText("You meet Lena most weeks.")).toBeTruthy();
    });
});

describe("step 3 day one", () => {
    it("shows only day-one work, answers it with Y, and adds a named topic", async () => {
        mocks.load.mockResolvedValue("today");
        mocks.work.mockResolvedValue({
            toAnswer: 2,
            items: [
                { id: "w1", source: "day_one", work: { title: "Prepare notes for your 2:00 with Lena" }, noticed: "From your calendar" },
                { id: "w2", source: "brief", work: { title: "Not day one" }, noticed: "" },
            ],
        });
        render(<SetupV3 onFinish={vi.fn()} onSkip={vi.fn()} />);
        expect(await screen.findByText("Prepare notes for your 2:00 with Lena")).toBeTruthy();
        expect(screen.queryByText("Not day one")).toBeNull();
        fireEvent.keyDown(window, { key: "y" });
        await waitFor(() => expect(mocks.answerWork).toHaveBeenCalledWith("w1", { action: "yes" }));
        fireEvent.change(screen.getByLabelText("What are you working on, or following?"), { target: { value: "Halcyon import" } });
        fireEvent.click(screen.getByTestId("setup-v3-add-topic"));
        await waitFor(() => expect(mocks.topic).toHaveBeenCalledWith({ id: "", action: "add", label: "Halcyon import" }));
    });

    it("shows only the field when there is nothing to offer", async () => {
        mocks.load.mockResolvedValue("today");
        render(<SetupV3 onFinish={vi.fn()} onSkip={vi.fn()} />);
        expect(await screen.findByText(/Nothing to offer yet/)).toBeTruthy();
        expect(screen.getByLabelText("What are you working on, or following?")).toBeTruthy();
    });
});

describe("step 4 your agents", () => {
    it("opens on the assistant row, signed in", async () => {
        mocks.load.mockResolvedValue("agents");
        mocks.probe.current = { agents: [{ id: "claude", installed: true, signedIn: true }] };
        render(<SetupV3 onFinish={vi.fn()} onSkip={vi.fn()} />);
        const row = await screen.findByTestId("setup-v3-assistant");
        expect(row.textContent).toContain("Claude Code reads your day and writes your briefs and answers");
        expect(row.textContent).toContain("Signed in · checked just now");
        expect(row.textContent).not.toMatch(/thinks with|agent/i);
    });

    it("offers Sign in, which opens the assistant's own sign-in", async () => {
        mocks.load.mockResolvedValue("agents");
        mocks.probe.current = { agents: [{ id: "codex", installed: true, signedIn: false }] };
        render(<SetupV3 onFinish={vi.fn()} onSkip={vi.fn()} />);
        expect((await screen.findByTestId("setup-v3-assistant")).textContent).toContain("Codex reads your day");
        fireEvent.click(screen.getByTestId("setup-v3-assistant-sign-in"));
        await waitFor(() => expect(mocks.signIn).toHaveBeenCalledWith("codex"));
    });

    it("offers Install when no assistant is on this computer", async () => {
        mocks.load.mockResolvedValue("agents");
        render(<SetupV3 onFinish={vi.fn()} onSkip={vi.fn()} />);
        expect((await screen.findByTestId("setup-v3-assistant")).textContent).toContain("Claude Code reads your day");
        fireEvent.click(screen.getByTestId("setup-v3-assistant-install"));
        await waitFor(() => expect(mocks.install).toHaveBeenCalledWith("claude"));
        expect(screen.queryByTestId("your-ai-stub")).toBeNull();
    });

    it("connects an agent found here with one click and its confirmation, and gives it the job its kind suggests", async () => {
        mocks.load.mockResolvedValue("agents");
        render(<SetupV3 onFinish={vi.fn()} onSkip={vi.fn()} />);
        const row = await screen.findByText("Claude Code");
        const item = row.closest("[data-connect-here]") as HTMLElement;
        fireEvent.click(item.querySelector("button") as HTMLElement);
        expect(mocks.connectHere).not.toHaveBeenCalled();
        await waitFor(() => expect(item.querySelector("[data-connect-here-yes]")).toBeTruthy());
        fireEvent.click(item.querySelector("[data-connect-here-yes]") as HTMLElement);
        await waitFor(() => expect(mocks.connectHere).toHaveBeenCalledWith("claude-code"));
        await waitFor(() => expect(mocks.setAgent).toHaveBeenCalledWith("code", "claude-code"));
        expect(await screen.findByTestId("setup-v3-job-claude-code")).toBeTruthy();
        expect(screen.getByText(/Join my Isoform/)).toBeTruthy();
    });
});

describe("step 1 speech recognition", () => {
    it("starts the download when Microphone is allowed, with one line and no question", async () => {
        mocks.load.mockResolvedValue("sees");
        render(<SetupV3 onFinish={vi.fn()} onSkip={vi.fn()} />);
        fireEvent.click(await screen.findByTestId("setup-v3-allow-microphone"));
        await waitFor(() => expect(mocks.requestRow).toHaveBeenCalledWith(["microphone", "system_audio"]));
        expect(mocks.plan).not.toHaveBeenCalledWith(expect.anything(), QUIET_SKIP);
        act(() => {
            events.get("setup:permissions")?.(micOn());
        });
        await waitFor(() => expect(mocks.start).toHaveBeenCalledTimes(1));
        expect(mocks.plan).toHaveBeenCalledWith(expect.anything(), QUIET_SKIP);
        expect(mocks.start).toHaveBeenCalledWith(expect.objectContaining({ planId: "speech" }));
        expect(mocks.save).toHaveBeenCalledWith("isoform.setup.speech-waiting", "");
        const line = await screen.findByTestId("setup-v3-model-line");
        expect(line.textContent).toBe("Getting speech recognition for this computer · 1.0 GB · 0%");
        expect(within(screen.getByTestId("setup-v3-row-microphone")).getByTestId("setup-v3-model-line")).toBeTruthy();
        expect(screen.queryByText("Download now")).toBeNull();
        expect(screen.queryByText("Later")).toBeNull();
        act(() => {
            events.get("models:downloads")?.([{ id: "m", state: "downloading", bytesDone: 4e8, bytesTotal: 1e9, pauseReason: "", error: "" }]);
        });
        expect(screen.getByTestId("setup-v3-model-line").textContent).toBe(
            "Getting speech recognition for this computer · 1.0 GB · 40%",
        );
        // Only privacy and memory are deferred, never the speech plan.
        expect(mocks.defer).toHaveBeenCalledTimes(1);
        expect(mocks.defer).toHaveBeenCalledWith(expect.objectContaining({ planId: "privacy-m" }));
    });

    it("never starts a download twice when step 1 opens again", async () => {
        mocks.load.mockResolvedValue("sees");
        mocks.permissions.mockResolvedValue(micOn());
        mocks.downloads.mockResolvedValue([{ id: "m", state: "downloading", bytesDone: 5e8, bytesTotal: 1e9, pauseReason: "", error: "" }]);
        render(<SetupV3 onFinish={vi.fn()} onSkip={vi.fn()} />);
        expect((await screen.findByTestId("setup-v3-model-line")).textContent).toContain("50%");
        expect(mocks.start).not.toHaveBeenCalled();
    });

    it("short of disk, says so in amber with Open Storage Settings and starts once there is room", async () => {
        vi.useFakeTimers({ shouldAdvanceTime: true });
        try {
            mocks.load.mockResolvedValue("sees");
            mocks.permissions.mockResolvedValue(micOn());
            let room = false;
            mocks.plan.mockImplementation(async (_choices: unknown, skip: string[]) =>
                skip.includes("live")
                    ? quietPlan()
                    : speechPlan(room ? {} : { freeBytes: 2e8, enoughSpace: false }),
            );
            render(<SetupV3 onFinish={vi.fn()} onSkip={vi.fn()} />);
            const line = await screen.findByTestId("setup-v3-model-line");
            expect(line.querySelector(".sv3-warn")?.textContent).toBe(
                "Speech recognition needs 1.0 GB free · 200 MB free now",
            );
            // Calls and the watch's talk wait for the model (answers v1.1.0 S9).
            expect(screen.getByTestId("setup-v3-disk-kept").textContent).toBe(
                "Calls and your watch's talk are kept, and transcribed once it's here.",
            );
            expect(mocks.start).not.toHaveBeenCalled();
            expect(mocks.save).toHaveBeenCalledWith(
                "isoform.setup.speech-waiting",
                JSON.stringify({ choices: {}, skip: QUIET_SKIP }),
            );
            fireEvent.click(screen.getByTestId("setup-v3-open-storage"));
            expect(mocks.openURL).toHaveBeenCalledWith("x-apple.systempreferences:com.apple.settings.Storage");
            room = true;
            await act(async () => {
                await vi.advanceTimersByTimeAsync(60_000);
            });
            await waitFor(() => expect(mocks.start).toHaveBeenCalledTimes(1));
            expect((await screen.findByTestId("setup-v3-model-line")).textContent).toContain(
                "Getting speech recognition for this computer",
            );
        } finally {
            vi.useRealTimers();
        }
    });

    // Answers v1.2.0 S11: the privacy and search models wait for room with
    // their own line, microphone on or off, and are still deferred.
    it("says the privacy and search models wait for room, even with the microphone skipped", async () => {
        mocks.load.mockResolvedValue("sees");
        mocks.plan.mockImplementation(async (_choices: unknown, skip: string[]) =>
            skip.includes("live") ? { ...quietPlan(), freeBytes: 3e8, enoughSpace: false } : speechPlan(),
        );
        render(<SetupV3 onFinish={vi.fn()} onSkip={vi.fn()} />);
        const short = await screen.findByTestId("setup-v3-quiet-models-short");
        expect(short.querySelector(".sv3-warn")?.textContent).toBe(
            "Privacy and search models need 1.0 GB free · 300 MB free now",
        );
        fireEvent.click(within(short).getByTestId("setup-v3-quiet-open-storage"));
        expect(mocks.openURL).toHaveBeenCalledWith("x-apple.systempreferences:com.apple.settings.Storage");
        await waitFor(() => expect(mocks.defer).toHaveBeenCalledWith(expect.objectContaining({ planId: "privacy-m" })));
        // The microphone was never allowed: no speech line, no speech download.
        expect(screen.queryByTestId("setup-v3-model-line")).toBeNull();
        expect(mocks.start).not.toHaveBeenCalled();
    });

    it("shows no models line when there is room for the privacy and search models", async () => {
        mocks.load.mockResolvedValue("sees");
        render(<SetupV3 onFinish={vi.fn()} onSkip={vi.fn()} />);
        await waitFor(() => expect(mocks.defer).toHaveBeenCalled());
        expect(screen.queryByTestId("setup-v3-quiet-models-short")).toBeNull();
    });

    it("keeps usage counts out of setup", async () => {
        mocks.load.mockResolvedValue("sees");
        render(<SetupV3 onFinish={vi.fn()} onSkip={vi.fn()} />);
        await screen.findByTestId("setup-v3-sees");
        expect(screen.queryByRole("switch")).toBeNull();
        expect(screen.queryByText(/usage/i)).toBeNull();
    });

    it("words the notifications row for this computer", async () => {
        mocks.load.mockResolvedValue("sees");
        render(<SetupV3 onFinish={vi.fn()} onSkip={vi.fn()} />);
        const row = await screen.findByTestId("setup-v3-row-notifications");
        expect(row.textContent).toContain("The brief and meeting prep, while you're at this computer.");
        expect(row.textContent).not.toContain("finished work");
    });
});

describe("locales", () => {
    it("has the same onboarding keys in en and zh-CN", async () => {
        const flat = (o: Record<string, unknown>, p = ""): string[] =>
            Object.entries(o).flatMap(([k, v]) =>
                v && typeof v === "object" ? flat(v as Record<string, unknown>, `${p}${k}.`) : [`${p}${k}`],
            );
        const en = (await import("@/i18n/locales/en/onboarding.json")).default;
        const zh = (await import("@/i18n/locales/zh-CN/onboarding.json")).default;
        expect(flat(zh).sort()).toEqual(flat(en).sort());
    });
});

describe("step 1 rows", () => {
    it("reads denied, waiting for Mail, and allowed", () => {
        const calendar = SEES_V3_ROWS.find((r) => r.id === "calendar")!;
        const mail = SEES_V3_ROWS.find((r) => r.id === "mail")!;
        expect(seesRowState(calendar, perms([{ key: "calendars", state: "on" }]), null)).toBe("off");
        expect(
            seesRowState(
                calendar,
                perms([
                    { key: "calendars", state: "on" },
                    { key: "reminders", state: "denied" },
                    { key: "contacts", state: "on" },
                ]),
                null,
            ),
        ).toBe("denied");
        expect(seesRowState(mail, perms([{ key: "automation_mail", state: "unavailable" }]), null)).toBe("later");
        expect(seesRowState(SEES_V3_ROWS.find((r) => r.id === "notifications")!, null, true)).toBe("on");
    });

    it("keeps Screen last, because granting it reopens Isoform", () => {
        expect(SEES_V3_ROWS[SEES_V3_ROWS.length - 1].keys).toContain("screen_recording");
    });

    it("resumes the step a reopen saved", () => {
        expect(resumeStageV3("sees")).toEqual({ kind: "step", step: "sees" });
        expect(resumeStageV3("phone")).toEqual({ kind: "step", step: "phone" });
        expect(resumeStageV3("notices")).toBeNull();
    });
});
