import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
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
    StartModelDownloads: vi.fn().mockResolvedValue(undefined),
    DeferModelDownloads: mocks.defer,
    BuildSetupClaims: mocks.claims,
    ConfirmMemoryEntry: mocks.confirm,
    NotQuiteMemoryEntry: mocks.notQuite,
    UndoNotQuiteMemoryEntry: vi.fn().mockResolvedValue(undefined),
    ReviseSetupClaim: vi.fn().mockResolvedValue(undefined),
    GetWorkList: mocks.work,
    AnswerWork: mocks.answerWork,
    UpdateUserTopic: mocks.topic,
}));
vi.mock("../v2/useAgentProbe", () => ({ useAgentProbe: () => ({ probe: { agents: [] }, ready: true }) }));
vi.mock("../v2/scenes/YourAIScene", () => ({ YourAIScene: () => <div data-testid="your-ai-stub" /> }));
vi.mock("../../../../wailsjs/runtime/runtime", () => ({
    EventsOn: vi.fn((name: string, fn: (data?: unknown) => void) => {
        events.set(name, fn);
        return () => events.delete(name);
    }),
}));

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
    mocks.plan.mockResolvedValue({ planId: "p", choices: {}, requiredBytes: 0, items: [{ id: "m" }], totalBytes: 1e9, freeBytes: 5e10, enoughSpace: true, modelsFolder: "" });
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
        await screen.findByTestId("setup-v3-models");
        for (const id of ["setup-v3-right", "setup-v3-today", "setup-v3-agents", "setup-v3-phone"]) {
            fireEvent.click(screen.getByTestId("setup-v3-skip-step"));
            expect(await screen.findByTestId(id)).toBeTruthy();
            // With no agent signed in, step 4 opens on Your AI.
            if (id === "setup-v3-agents") expect(screen.getByTestId("your-ai-stub")).toBeTruthy();
        }
        // Leaving step 1 without Download now or Later is Later: the models still come.
        expect(mocks.defer).toHaveBeenCalledTimes(1);
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

    it("says there is nothing to check when nothing was read", async () => {
        mocks.load.mockResolvedValue("right");
        render(<SetupV3 onFinish={vi.fn()} onSkip={vi.fn()} />);
        expect(await screen.findByText(/Nothing to check yet/)).toBeTruthy();
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
