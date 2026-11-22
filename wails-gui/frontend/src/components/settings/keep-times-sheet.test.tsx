import "@/i18n";
import { Children, isValidElement, type ReactElement, type ReactNode } from "react";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import en from "@/i18n/locales/en/settings.json";
import zh from "@/i18n/locales/zh-CN/settings.json";
import sheetSource from "./keep-times-sheet.tsx?raw";
import recordingStorageSource from "./pages/recording-storage.ts?raw";
import { KeepTimesSheet } from "./keep-times-sheet";

const mocks = vi.hoisted(() => ({
    getRetention: vi.fn(),
    setRetention: vi.fn(),
    getMaxGB: vi.fn(),
    setMaxGB: vi.fn(),
    storageStats: vi.fn(),
    usage: vi.fn(),
    getScreenshotDays: vi.fn(),
    setScreenshotDays: vi.fn(),
    budget: vi.fn(),
    confirmLimit: vi.fn(),
    toast: { error: vi.fn(), success: vi.fn(), warning: vi.fn() },
}));

vi.mock("@/lib/wails-shim-app", () => ({
    GetActivityRetentionDays: mocks.getRetention,
    SetActivityRetentionDays: mocks.setRetention,
    GetActivityMaxStorageGB: mocks.getMaxGB,
    SetActivityMaxStorageGB: mocks.setMaxGB,
    GetActivityStorageStats: mocks.storageStats,
    GetActivityStorageUsage: mocks.usage,
    GetScreenshotRetentionDays: mocks.getScreenshotDays,
    SetScreenshotRetentionDays: mocks.setScreenshotDays,
    GetActivityStorageBudget: mocks.budget,
    ConfirmActivityStorageLimit: mocks.confirmLimit,
}));

vi.mock("@/components/onboarding/models-panel", () => ({ ModelsPanel: () => <div data-testid="storage-models" /> }));

vi.mock("sonner", () => ({ toast: mocks.toast }));

// Radix Select needs pointer APIs jsdom lacks; a native <select> keeps the
// same value / onValueChange contract and the trigger's accessible name.
vi.mock("@/components/ui/select", () => {
    const SelectTrigger = (_: { "aria-label"?: string; className?: string; children?: ReactNode }) => null;
    const SelectValue = () => null;
    const SelectContent = ({ children }: { children?: ReactNode }) => <>{children}</>;
    const SelectItem = ({ value, children }: { value: string; children?: ReactNode }) => (
        <option value={value}>{children}</option>
    );
    const Select = ({
        value,
        onValueChange,
        disabled,
        children,
    }: {
        value: string;
        onValueChange: (value: string) => void;
        disabled?: boolean;
        children?: ReactNode;
    }) => {
        const trigger = Children.toArray(children).find(
            (child): child is ReactElement<{ "aria-label"?: string }> =>
                isValidElement(child) && child.type === SelectTrigger,
        );
        return (
            <select
                aria-label={trigger?.props["aria-label"]}
                value={value}
                disabled={disabled}
                onChange={(event) => onValueChange(event.target.value)}
            >
                {children}
            </select>
        );
    };
    return { Select, SelectTrigger, SelectValue, SelectContent, SelectItem };
});

const GIB = 1024 * 1024 * 1024;

const USAGE = {
    screenshotBytes: 39 * GIB,
    screenshotFiles: 117239,
    screenshotDays: 144,
    oldestScreenshotMs: new Date(2026, 2, 5).getTime(),
    audioBytes: 18 * GIB,
    audioFiles: 442,
    databaseBytes: 8 * GIB,
    otherBytes: 0,
    totalBytes: 65 * GIB,
    measuredAtMs: Date.now(),
};

const EMPTY_ITEM = { count: 0, bytes: 0, afterBytes: 0, oldestMs: 0, newestMs: 0 };

function leafKeys(value: unknown, prefix = ""): string[] {
    if (value === null || typeof value !== "object") return [prefix];
    return Object.entries(value as Record<string, unknown>).flatMap(([key, child]) =>
        leafKeys(child, prefix ? `${prefix}.${key}` : key),
    );
}

async function renderPage() {
    render(<KeepTimesSheet open onOpenChange={() => {}} />);
    await screen.findByRole("combobox", { name: "Storage limit" });
    await screen.findByRole("combobox", { name: "Keep screenshots for" });
    await screen.findByText("65 GB of 20 GB");
}

describe("KeepTimesSheet (Your data › Change)", () => {
    beforeEach(() => {
        vi.clearAllMocks();
        mocks.getRetention.mockResolvedValue(0);
        mocks.getMaxGB.mockResolvedValue(20);
        mocks.setMaxGB.mockResolvedValue(undefined);
        mocks.storageStats.mockResolvedValue({ retainedBytes: 65 * GIB, retainedSessionCount: 6881, earliestRetainedMs: 0 });
        mocks.usage.mockResolvedValue(USAGE);
        mocks.getScreenshotDays.mockResolvedValue(30);
        mocks.setScreenshotDays.mockResolvedValue(undefined);
        mocks.budget.mockResolvedValue({ state: "ok", limitBytes: 20 * GIB, projectedBytes: 0, keptBytes: 0, screenshots: EMPTY_ITEM, recordings: EMPTY_ITEM, checkedAtMs: 1 });
        mocks.confirmLimit.mockResolvedValue(undefined);
    });

    it("never shows an empty Space used card: a copper failure line whose Try again reads again (ST63)", async () => {
        mocks.usage.mockRejectedValueOnce(new Error("disk busy"));
        render(<KeepTimesSheet open onOpenChange={() => {}} />);
        const line = await screen.findByText(/Couldn't measure the space used\./);
        expect(line).toHaveClass("text-copper");
        const calls = mocks.usage.mock.calls.length;
        fireEvent.click(within(line).getByRole("button", { name: "Try again" }));
        await waitFor(() => expect(mocks.usage.mock.calls.length).toBeGreaterThan(calls));
        await waitFor(() => expect(screen.queryByText(/Couldn't measure the space used\./)).not.toBeInTheDocument());
        expect(zh.storage.usage.failed).toBe("无法计算已用空间。");
    });

    it("shows what each kind of data takes against the limit", async () => {
        await renderPage();
        expect(screen.getByText("Screenshots")).toBeInTheDocument();
        expect(screen.getByText("144 days, since 2026-03-05")).toBeInTheDocument();
        expect(screen.getByText("39 GB")).toBeInTheDocument();
        expect(screen.getByText("Call recordings")).toBeInTheDocument();
        expect(screen.getByText("442 files")).toBeInTheDocument();
        expect(screen.getByText("Activity database")).toBeInTheDocument();
        expect(screen.getByText("8.0 GB")).toBeInTheDocument();
        expect(screen.getByText(/Over the limit/)).toBeInTheDocument();
    });

    it("asks before a lower storage limit removes screenshots", async () => {
        mocks.usage.mockResolvedValue({ ...USAGE, totalBytes: 7 * GIB });
        render(<KeepTimesSheet open onOpenChange={() => {}} />);
        const select = await screen.findByRole("combobox", { name: "Storage limit" });
        await screen.findByText("7.0 GB of 20 GB");
        expect(within(select).getByRole("option", { name: "5 GB" })).toBeInTheDocument();

        fireEvent.change(select, { target: { value: "10" } });
        await waitFor(() => expect(mocks.setMaxGB).toHaveBeenCalledWith(10));

        fireEvent.change(select, { target: { value: "5" } });
        expect(await screen.findByText("Lower the storage limit to 5 GB?")).toBeInTheDocument();
        expect(mocks.setMaxGB).toHaveBeenCalledTimes(1);
    });

    it("says a failed screenshot-time change in place, puts the old time back and retries it (ST9/ST11)", async () => {
        await renderPage();
        const select = screen.getByRole("combobox", { name: "Keep screenshots for" });
        mocks.setScreenshotDays.mockRejectedValueOnce(new Error("store closed"));
        fireEvent.change(select, { target: { value: "90" } });
        const line = await screen.findByText("Couldn't change how long screenshots are kept.");
        expect(line.closest("[data-settings-failure]")).toHaveClass("text-copper");
        expect(mocks.toast.error).not.toHaveBeenCalled();
        mocks.setScreenshotDays.mockResolvedValueOnce(undefined);
        fireEvent.click(within(line.closest("[data-settings-failure]") as HTMLElement).getByRole("button", { name: "Try again" }));
        await waitFor(() => expect(mocks.setScreenshotDays).toHaveBeenCalledTimes(2));
        expect(screen.queryByText("Couldn't change how long screenshots are kept.")).not.toBeInTheDocument();
    });

    it("asks before a shorter screenshot time and saves a longer one directly", async () => {
        await renderPage();
        const select = screen.getByRole("combobox", { name: "Keep screenshots for" });
        expect(within(select).getByRole("option", { name: "As long as their recordings" })).toBeInTheDocument();

        fireEvent.change(select, { target: { value: "90" } });
        await waitFor(() => expect(mocks.setScreenshotDays).toHaveBeenCalledWith(90));

        fireEvent.change(select, { target: { value: "14" } });
        expect(await screen.findByText("Keep screenshots for 14 days?")).toBeInTheDocument();
        expect(mocks.setScreenshotDays).toHaveBeenCalledTimes(1);
        fireEvent.click(screen.getByRole("button", { name: "Remove older screenshots" }));
        await waitFor(() => expect(mocks.setScreenshotDays).toHaveBeenCalledWith(14));
    });

});

describe("KeepTimesSheet holds Storage's and Privacy's keep times (settings batch D2)", () => {
    beforeEach(() => {
        vi.clearAllMocks();
        mocks.getRetention.mockResolvedValue(30);
        mocks.getMaxGB.mockResolvedValue(20);
        mocks.setRetention.mockResolvedValue(undefined);
        mocks.storageStats.mockResolvedValue({ retainedBytes: 0, retainedSessionCount: 0, earliestRetainedMs: 0 });
        mocks.usage.mockResolvedValue(USAGE);
        mocks.getScreenshotDays.mockResolvedValue(-1);
        mocks.budget.mockResolvedValue({ state: "ok", limitBytes: 0, projectedBytes: 0, keptBytes: 0, screenshots: EMPTY_ITEM, recordings: EMPTY_ITEM, checkedAtMs: 1 });
    });

    it("shows the recording keep time, the storage limit and the screenshot time with their saved values, and no cleanup", async () => {
        render(<KeepTimesSheet open onOpenChange={() => {}} />);
        expect(await screen.findByRole("combobox", { name: "Keep recordings for" })).toHaveValue("30");
        expect(await screen.findByRole("combobox", { name: "Storage limit" })).toHaveValue("20");
        expect(await screen.findByRole("combobox", { name: "Keep screenshots for" })).toHaveValue("-1");
        expect(screen.queryByRole("button", { name: "Check" })).not.toBeInTheDocument();
        expect(en.storage).not.toHaveProperty("cleanup");
    });

    it("saves a longer recording keep time through the same binding", async () => {
        render(<KeepTimesSheet open onOpenChange={() => {}} />);
        const select = await screen.findByRole("combobox", { name: "Keep recordings for" });
        fireEvent.change(select, { target: { value: "90" } });
        await waitFor(() => expect(mocks.setRetention).toHaveBeenCalledWith(90));
    });

    // Carried over from Privacy's tests for the same card.
    it("asks before a shorter recording keep time deletes recordings", async () => {
        mocks.storageStats.mockResolvedValue({ retainedBytes: 3 * GIB, retainedSessionCount: 12, earliestRetainedMs: 0 });
        render(<KeepTimesSheet open onOpenChange={() => {}} />);
        const select = await screen.findByRole("combobox", { name: "Keep recordings for" });
        await screen.findByText(/for 12 sessions/);
        fireEvent.change(select, { target: { value: "7" } });
        expect(await screen.findByText("Keep recordings for 7 days?")).toBeInTheDocument();
        expect(mocks.setRetention).not.toHaveBeenCalled();
        fireEvent.click(await screen.findByRole("button", { name: "Delete older recordings" }));
        await waitFor(() => expect(mocks.setRetention).toHaveBeenCalledWith(7));
    });

    it("asks before a shorter recording keep time while usage is unknown", async () => {
        mocks.storageStats.mockRejectedValue(new Error("busy"));
        render(<KeepTimesSheet open onOpenChange={() => {}} />);
        const select = await screen.findByRole("combobox", { name: "Keep recordings for" });
        fireEvent.change(select, { target: { value: "7" } });
        expect(await screen.findByText("Keep recordings for 7 days?")).toBeInTheDocument();
        expect(mocks.setRetention).not.toHaveBeenCalled();
    });

    it("puts the recording keep time back with Try again when saving fails", async () => {
        mocks.setRetention.mockRejectedValueOnce(new Error("disk busy"));
        render(<KeepTimesSheet open onOpenChange={() => {}} />);
        const select = await screen.findByRole("combobox", { name: "Keep recordings for" });
        fireEvent.change(select, { target: { value: "90" } });
        // The keep time and the limit share one saved-settings hook, so both
        // cards show its failure line.
        const [line] = await screen.findAllByText("Couldn't change this.", { exact: false });
        expect(line.closest("[data-settings-failure]")).toHaveClass("text-copper");
        await waitFor(() => expect((select as HTMLSelectElement).value).toBe("30"));
    });

    it("reads nothing while closed", () => {
        render(<KeepTimesSheet open={false} onOpenChange={() => {}} />);
        expect(mocks.usage).not.toHaveBeenCalled();
        expect(screen.queryByTestId("keep-times-sheet")).not.toBeInTheDocument();
    });
});

describe("KeepTimesSheet limit states", () => {
    beforeEach(() => {
        vi.clearAllMocks();
        mocks.getRetention.mockResolvedValue(30);
        mocks.getMaxGB.mockResolvedValue(50);
        mocks.storageStats.mockResolvedValue({ retainedBytes: 65 * GIB, retainedSessionCount: 1, earliestRetainedMs: 0 });
        mocks.usage.mockResolvedValue(USAGE);
        mocks.getScreenshotDays.mockResolvedValue(-1);
        mocks.setScreenshotDays.mockResolvedValue(undefined);
        mocks.setMaxGB.mockResolvedValue(undefined);
        mocks.confirmLimit.mockResolvedValue(undefined);
    });

    it("asks before removing anything on a profile the old count let grow", async () => {
        mocks.budget.mockResolvedValue({
            state: "confirm",
            limitBytes: 50 * GIB,
            projectedBytes: 55 * GIB,
            keptBytes: 0,
            screenshots: { count: 1200, bytes: 4 * GIB, afterBytes: 0, oldestMs: new Date(2026, 2, 5).getTime(), newestMs: new Date(2026, 3, 1).getTime() },
            recordings: { count: 3, bytes: 1 * GIB, afterBytes: 0, oldestMs: new Date(2026, 2, 9).getTime(), newestMs: new Date(2026, 2, 20).getTime() },
            checkedAtMs: 1,
        });
        render(<KeepTimesSheet open onOpenChange={() => {}} />);
        expect(await screen.findByText(/1,203 files, 5\.0 GB, 2026-03-05 to 2026-04-01/)).toBeInTheDocument();
        expect(mocks.confirmLimit).not.toHaveBeenCalled();
        fireEvent.click(screen.getByRole("button", { name: "Remove them" }));
        fireEvent.click(await screen.findByRole("button", { name: "Remove and keep the limit" }));
        await waitFor(() => expect(mocks.confirmLimit).toHaveBeenCalledTimes(1));
    });

    it("says when the limit cannot be reached", async () => {
        mocks.budget.mockResolvedValue({
            state: "unreachable",
            limitBytes: 5 * GIB,
            projectedBytes: 9 * GIB,
            keptBytes: 8 * GIB,
            screenshots: EMPTY_ITEM,
            recordings: EMPTY_ITEM,
            checkedAtMs: 1,
        });
        render(<KeepTimesSheet open onOpenChange={() => {}} />);
        expect(await screen.findByText(/Nothing is removed for the limit\. Raise it to more than 8\.0 GB\./)).toBeInTheDocument();
    });

    it("asks before a lower limit while usage is unknown", async () => {
        mocks.usage.mockRejectedValue(new Error("busy"));
        mocks.budget.mockResolvedValue({ state: "ok", limitBytes: 0, projectedBytes: 0, keptBytes: 0, screenshots: EMPTY_ITEM, recordings: EMPTY_ITEM, checkedAtMs: 1 });
        render(<KeepTimesSheet open onOpenChange={() => {}} />);
        const select = await screen.findByRole("combobox", { name: "Storage limit" });
        fireEvent.change(select, { target: { value: "20" } });
        expect(await screen.findByText("Lower the storage limit to 20 GB?")).toBeInTheDocument();
        expect(mocks.setMaxGB).not.toHaveBeenCalled();
    });

    it("says screenshots go with their recordings, text included, until a time is chosen", async () => {
        mocks.budget.mockResolvedValue({ state: "ok", limitBytes: 0, projectedBytes: 0, keptBytes: 0, screenshots: EMPTY_ITEM, recordings: EMPTY_ITEM, checkedAtMs: 1 });
        render(<KeepTimesSheet open onOpenChange={() => {}} />);
        const select = await screen.findByRole("combobox", { name: "Keep screenshots for" });
        expect(within(select).getByRole("option", { name: "As long as their recordings" })).toBeInTheDocument();
        // Only times shorter than the recording limit are offered.
        expect(within(select).queryByRole("option", { name: "30 days" })).not.toBeInTheDocument();
        expect(within(select).queryByRole("option", { name: "90 days" })).not.toBeInTheDocument();
        expect(screen.getByText(/after 30 days the whole recording, its text included, is removed/)).toBeInTheDocument();
        fireEvent.change(select, { target: { value: "14" } });
        expect(await screen.findByText("Keep screenshots for 14 days?")).toBeInTheDocument();
    });
});

describe("Storage copy", () => {
    it("has every key in both languages", () => {
        expect(leafKeys(zh.storage).sort()).toEqual(leafKeys(en.storage).sort());
    });

    it("never falls back to inline English", () => {
        for (const source of [sheetSource, recordingStorageSource]) {
            expect(source).not.toContain("defaultValue");
            expect(source).not.toMatch(/\bt\(\s*["'`][^"'`]+["'`]\s*,\s*["'`]/);
        }
    });
});
