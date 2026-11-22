import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import i18n from "@/i18n";
import en from "@/i18n/locales/en/settings.json";
import zh from "@/i18n/locales/zh-CN/settings.json";
import { YourDataPage } from "./your-data-page";

const mocks = vi.hoisted(() => ({
    summary: { retentionDays: 30, ignoredApps: 3, handoffsThisWeek: 4, pairedDevices: 1, answers: 12, answersSince: "2026-11-02T17:00:00Z", screenBytes: 2 * 1024 ** 3, audioBytes: 300 * 1024 ** 2, usedBytes: 3 * 1024 ** 3, capBytes: 10 * 1024 ** 3, messagesNotes: 5, messagesBytes: 2048, watchBytes: 50 * 1024 ** 2, agentsHistoryItems: 4, agentsHistoryBytes: 4096 } as Record<string, number | string>,
    fail: false,
    exportAnswers: vi.fn(async () => true),
    deleteAnswers: vi.fn(async () => {}),
    startForget: vi.fn(async () => ({ state: "running", steps: [{ id: "memory", state: "waiting" }] })),
    forgetStatus: { state: "idle", steps: [] } as Record<string, unknown>,
    workProfile: { approvedVersion: 0 } as Record<string, unknown>,
    workProfileFails: false,
}));
vi.mock("@/lib/work-profile", () => ({
    getWorkProfile: vi.fn(async () => {
        if (mocks.workProfileFails) throw new Error("read timed out");
        return mocks.workProfile;
    }),
}));
vi.mock("@/lib/wails-shim-app", () => ({
    GetYourDataSummary: vi.fn(async () => {
        if (mocks.fail) throw new Error("store closed");
        return mocks.summary;
    }),
    ForgetVoices: vi.fn(async () => {}),
    ExportAnswers: mocks.exportAnswers,
    DeleteAnswers: mocks.deleteAnswers,
    StartForgetEverything: mocks.startForget,
    GetForgetEverything: vi.fn(async () => mocks.forgetStatus),
}));
vi.mock("../../../../wailsjs/runtime/runtime", () => ({ EventsOn: vi.fn(() => () => {}) }));
// The keep-times sheet is tested on its own (keep-times-sheet.test.tsx).
vi.mock("@/components/settings/keep-times-sheet", () => ({
    KeepTimesSheet: ({ open }: { open: boolean }) => (open ? <div data-testid="keep-times-sheet" /> : null),
}));

const row = (key: string) => document.querySelector(`[data-your-data-row=${key}]`) as HTMLElement;

describe("Settings › Your data", () => {
    beforeEach(async () => {
        await i18n.changeLanguage("en");
        mocks.summary = { retentionDays: 30, ignoredApps: 3, handoffsThisWeek: 4, pairedDevices: 1, answers: 12, answersSince: "2026-11-02T17:00:00Z", screenBytes: 2 * 1024 ** 3, audioBytes: 300 * 1024 ** 2, usedBytes: 3 * 1024 ** 3, capBytes: 10 * 1024 ** 3, messagesNotes: 5, messagesBytes: 2048, watchBytes: 50 * 1024 ** 2, agentsHistoryItems: 4, agentsHistoryBytes: 4096, callsOn: true, messagesOn: true, agentsHistoryOn: true } as never;
        mocks.fail = false;
        mocks.workProfile = { approvedVersion: 0 };
        mocks.workProfileFails = false;
    });

    it("keeps the six sources of What Isoform sees in its order, with keep times, sizes and the whole", async () => {
        render(<YourDataPage />);
        await screen.findByText(/3 apps never seen/);
        const rows = [...document.querySelectorAll("[data-your-data-row]")].map((el) => el.getAttribute("data-your-data-row"));
        expect(rows.slice(0, 7)).toEqual(["screen", "calls", "mail", "messages", "watch", "agentsHistory", "total"]);
        expect(row("screen")).toHaveTextContent("The apps you use · 3 apps never seen · kept 30 days · 2");
        expect(row("calls")).toHaveTextContent("Recorded calls on this computer, you and the other side · kept 30 days · 300");
        expect(row("mail")).toHaveTextContent("Read where they are; not copied · —");
        expect(row("watch")).toHaveTextContent("All day, in hours you can pause · kept until you delete it");
        expect(row("messages")).toHaveTextContent("5 notes");
        expect(row("agentsHistory")).toHaveTextContent("4 things learned");
        expect(row("total")).toHaveTextContent("of 10");
        expect(row("total")).toHaveTextContent("When it's full, the oldest goes first.");
        expect(row("answers")).toHaveTextContent("12 answers since November 2");
        expect(row("agents")).toHaveTextContent("Only what each hand-off's What went lists");
        expect(row("devices")).toHaveTextContent(en.yourData.devicesSub);
        expect(row("notifications")).toHaveTextContent(en.yourData.notificationsSub);
        expect(screen.getByText(en.privacy.voices.title)).toBeInTheDocument();
        const keeps = screen.getByText(en.yourData.keeps);
        const leaves = screen.getByText(en.yourData.leaves);
        expect(keeps.compareDocumentPosition(leaves) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    });

    it("says kept until deleted when recordings are kept without a limit, and no phone when none is paired", async () => {
        mocks.summary = { retentionDays: 0, ignoredApps: 1, handoffsThisWeek: 1, pairedDevices: 0, answers: 0 };
        render(<YourDataPage />);
        expect(await screen.findByText("The apps you use · 1 app never seen · kept until you delete it")).toBeInTheDocument();
        expect(row("agents")).toHaveTextContent("this week 1");
        expect(row("devices")).toHaveTextContent(en.yourData.devicesNone);
        expect(row("answers")).toHaveTextContent(en.yourData.answersNone);
    });

    it("counts this week's hand-offs per agent in the design's words (computer-data v1.0.0 rule 5)", async () => {
        mocks.summary = { ...mocks.summary, handoffsThisWeek: 4, handoffsByAgent: { codex: 3, "": 1 } } as never;
        render(<YourDataPage />);
        await screen.findByText(/3 apps never seen/);
        expect(row("agents")).toHaveTextContent("Only what each hand-off's What went lists · this week 4: Codex 3, the AI you chose 1");
    });

    it("says Off with Turn on for a source whose switch is off, and opens that source", async () => {
        mocks.summary = { ...mocks.summary, callsOn: false, messagesOn: false, agentsHistoryOn: false } as never;
        const onOpenPage = vi.fn();
        render(<YourDataPage onOpenPage={onOpenPage} />);
        await screen.findByText(/3 apps never seen/);
        for (const key of ["calls", "messages", "agentsHistory"]) {
            expect(row(key)).toHaveTextContent(/^.*Off/);
            expect(row(key)).not.toHaveTextContent("kept 30 days");
        }
        expect(row("messages")).not.toHaveTextContent(en.yourData.messagesSub);
        fireEvent.click(within(row("calls")).getByRole("button", { name: en.yourData.turnOn }));
        fireEvent.click(within(row("messages")).getByRole("button", { name: en.yourData.turnOn }));
        fireEvent.click(within(row("agentsHistory")).getByRole("button", { name: en.yourData.turnOn }));
        expect(onOpenPage.mock.calls).toEqual([
            ["activity", { anchor: "sees-calls" }],
            ["activity", { anchor: "sees-messages" }],
            ["activity", { anchor: "sees-agents" }],
        ]);
    });

    it("never says agents' history is off while its switch is on (it is on unless turned off)", async () => {
        mocks.summary = { ...mocks.summary, agentsHistoryOn: true } as never;
        render(<YourDataPage />);
        await screen.findByText(/3 apps never seen/);
        expect(row("agentsHistory")).toHaveTextContent(en.yourData.agentsHistoryOnSub);
        expect(row("agentsHistory")).not.toHaveTextContent(/Off/);
        expect(JSON.stringify(en.yourData)).not.toContain("Off for every agent");
    });

    it("lists Agent of You under What leaves only once a work profile was approved, with its claim count", async () => {
        render(<YourDataPage />);
        await screen.findByText(/3 apps never seen/);
        expect(row("agentOfYou")).toBeNull();
        cleanup();
        mocks.workProfile = { approvedVersion: 2, approved: { lines: [{ id: "a", removed: false, filtered: false }, { id: "b", removed: true, filtered: false }, { id: "c", removed: false, filtered: false }] } };
        render(<YourDataPage />);
        expect(await screen.findByText("The 2 claims Agent of You can say")).toBeInTheDocument();
        expect(row("agentOfYou")).toHaveTextContent(en.yourData.aoy);
    });

    it("shows a switch it couldn't read as a failure with Try again, never as off or on", async () => {
        const { messagesOn: _m, agentsHistoryOn: _h, ...unread } = mocks.summary as Record<string, unknown>;
        mocks.summary = unread as never; // messagesOn and agentsHistoryOn couldn't be read
        render(<YourDataPage />);
        await screen.findByText(/3 apps never seen/);
        for (const key of ["messages", "agentsHistory"]) {
            expect(row(key)).toHaveTextContent(en.yourData.switchReadFailed);
            expect(row(key)).not.toHaveTextContent(/\bOff\b/);
            expect(within(row(key)).queryByRole("button", { name: en.yourData.turnOn })).toBeNull();
            expect(within(row(key)).getByRole("button", { name: /Try again/ })).toBeInTheDocument();
        }
        expect(row("agentsHistory")).not.toHaveTextContent(en.yourData.agentsHistoryOnSub);
        expect(row("messages")).not.toHaveTextContent(en.yourData.messagesSub);
    });

    it("says it couldn't read Agent of You's profile rather than hiding the row", async () => {
        mocks.workProfileFails = true;
        render(<YourDataPage />);
        expect(await screen.findByText(en.yourData.aoyReadFailed)).toBeInTheDocument();
        expect(row("agentOfYou")).toHaveTextContent(en.yourData.aoy);
    });

    it("opens the page that changes each source", async () => {
        const onOpenPage = vi.fn();
        render(<YourDataPage onOpenPage={onOpenPage} />);
        await screen.findByText(/3 apps never seen/);
        // Change on a keep time or the total opens the keep-times sheet here
        // (settings batch D2: Storage went); Messages opens its source.
        expect(screen.queryByTestId("keep-times-sheet")).not.toBeInTheDocument();
        for (const key of ["screen", "calls", "total"]) {
            fireEvent.click(within(row(key)).getByRole("button", { name: en.yourData.change }));
            expect(screen.getByTestId("keep-times-sheet")).toBeInTheDocument();
        }
        fireEvent.click(within(row("messages")).getByRole("button", { name: en.yourData.change }));
        expect(onOpenPage.mock.calls.map((call) => call[0])).toEqual(["activity"]);
    });

    it("says so when the numbers can't be read, and keeps the rows", async () => {
        mocks.fail = true;
        render(<YourDataPage />);
        expect(await screen.findByText(en.yourData.loadFailed)).toBeInTheDocument();
        expect(row("screen")).toHaveTextContent(en.yourData.screenSubPlain);
    });

    it("exports your answers from their row, and only when there are some", async () => {
        render(<YourDataPage />);
        await screen.findByText(/12 answers since/);
        fireEvent.click(within(row("answers")).getByRole("button", { name: en.yourData.exportLabel }));
        expect(mocks.exportAnswers).toHaveBeenCalledOnce();
        cleanup();
        mocks.summary = { ...mocks.summary, answers: 0 };
        render(<YourDataPage />);
        await screen.findByText(en.yourData.answersNone);
        expect(within(row("answers")).queryByRole("button")).toBeNull();
    });

    it("keeps both locales in step", () => {
        expect(Object.keys(zh.yourData).sort()).toEqual(Object.keys(en.yourData).sort());
    });

    it("deletes your answers only after the confirmation", async () => {
        render(<YourDataPage />);
        fireEvent.click(await screen.findByRole("button", { name: en.yourData.deleteLabel }));
        expect(mocks.deleteAnswers).not.toHaveBeenCalled();
        const dialog = await screen.findByRole("dialog");
        expect(dialog).toHaveTextContent("12 answers");
        fireEvent.click(within(dialog).getByRole("button", { name: en.yourData.deleteConfirmButton }));
        expect(mocks.deleteAnswers).toHaveBeenCalledTimes(1);
    });

    it("forgets everything only after the confirmation, last on the page", async () => {
        mocks.forgetStatus = { state: "idle", steps: [] };
        render(<YourDataPage />);
        const rows = () => [...document.querySelectorAll("[data-your-data-row]")].map((el) => el.getAttribute("data-your-data-row"));
        fireEvent.click(await screen.findByRole("button", { name: en.yourData.forgetButton }));
        expect(rows().at(-1)).toBe("forget");
        expect(mocks.startForget).not.toHaveBeenCalled();
        const dialog = await screen.findByRole("dialog");
        fireEvent.click(within(dialog).getByRole("button", { name: en.yourData.forgetConfirmButton }));
        expect(mocks.startForget).toHaveBeenCalledTimes(1);
    });

    it("says which part waits while forgetting is pending", async () => {
        mocks.forgetStatus = { state: "pending", steps: [{ id: "memory", state: "done" }, { id: "agentOfYou", state: "failed", error: "server" }] };
        render(<YourDataPage />);
        expect(await screen.findByText("Waiting: Agent of You profile")).toBeTruthy();
        expect(screen.getByRole("button", { name: en.yourData.forgetRetry })).toBeTruthy();
    });
});
