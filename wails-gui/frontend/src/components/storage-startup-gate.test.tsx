import { useEffect } from "react";
import { act, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import i18n from "@/i18n";
import en from "@/i18n/locales/en/app.json";
import zhCN from "@/i18n/locales/zh-CN/app.json";
import type { StorageStartupStatus } from "@/lib/wails-shim-app";
import { StorageStartupGate } from "./storage-startup-gate";

const mocks = vi.hoisted(() => ({
    status: vi.fn(),
    quit: vi.fn(),
    on: vi.fn((_event: string, _cb: () => void) => () => {}),
    emit: vi.fn(),
    mounted: vi.fn(),
}));

vi.mock("@/lib/wails-shim-app", () => ({
    GetStorageStartupStatus: mocks.status,
}));

vi.mock("../../wailsjs/runtime/runtime", () => ({
    Quit: mocks.quit,
    EventsOn: mocks.on,
    EventsEmit: mocks.emit,
}));

function AppProbe() {
    useEffect(() => {
        mocks.mounted();
    }, []);
    return <span>app-loaded</span>;
}

function renderGate() {
    return render(<StorageStartupGate><AppProbe /></StorageStartupGate>);
}

describe("StorageStartupGate", () => {
    beforeEach(async () => {
        vi.clearAllMocks();
        await i18n.changeLanguage("en");
    });

    afterEach(() => vi.useRealTimers());

    it("keeps showing progress while the database opens, then mounts the app", async () => {
        vi.useFakeTimers();
        mocks.status.mockResolvedValueOnce({ code: "checking" }).mockResolvedValueOnce({ code: "" });
        renderGate();
        await act(async () => {});
        expect(screen.getByRole("status")).toHaveTextContent(en.storageStartup.checking);
        expect(mocks.mounted).not.toHaveBeenCalled();

        await act(async () => vi.advanceTimersByTimeAsync(500));
        expect(screen.getByText("app-loaded")).toBeInTheDocument();
        expect(mocks.mounted).toHaveBeenCalledTimes(1);
    });

    it("reports first paint only after storage succeeds and two animation frames", async () => {
        vi.useFakeTimers();
        mocks.status.mockResolvedValue({ code: "" });
        renderGate();
        await act(async () => {});
        expect(mocks.emit).not.toHaveBeenCalled();
        await act(async () => vi.advanceTimersByTimeAsync(16));
        expect(mocks.emit).not.toHaveBeenCalled();
        await act(async () => vi.advanceTimersByTimeAsync(20));
        expect(mocks.emit).toHaveBeenCalledTimes(1);
        expect(mocks.emit).toHaveBeenCalledWith("pages:first-paint");
    });

    it("changes from progress to a storage error without mounting the app", async () => {
        vi.useFakeTimers();
        mocks.status.mockResolvedValueOnce({ code: "checking" }).mockResolvedValueOnce({
            code: "schema_too_new", storedVersion: 59, supportedVersion: 58,
        });
        renderGate();
        await act(async () => {});
        await act(async () => vi.advanceTimersByTimeAsync(500));

        expect(screen.getByRole("alert")).toHaveTextContent(en.storageStartup.newerTitle);
        expect(mocks.mounted).not.toHaveBeenCalled();
        await act(async () => vi.advanceTimersByTimeAsync(1000));
        expect(mocks.status).toHaveBeenCalledTimes(2);
    });

    it("stops polling when the progress window unmounts", async () => {
        vi.useFakeTimers();
        mocks.status.mockResolvedValue({ code: "checking" });
        const { unmount } = renderGate();
        await act(async () => {});
        unmount();
        await act(async () => vi.advanceTimersByTimeAsync(1000));
        expect(mocks.status).toHaveBeenCalledTimes(1);
    });

    it("mounts the app only after storage reports success", async () => {
        let finish!: (status: StorageStartupStatus) => void;
        mocks.status.mockReturnValue(new Promise<StorageStartupStatus>((resolve) => {
            finish = resolve;
        }));
        renderGate();
        expect(screen.getByRole("status")).toHaveTextContent(en.storageStartup.checking);
        expect(mocks.mounted).not.toHaveBeenCalled();

        await act(async () => finish({ code: "" }));
        expect(screen.getByText("app-loaded")).toBeInTheDocument();
        expect(mocks.mounted).toHaveBeenCalledTimes(1);
    });

    it.each([
        ["en", en.storageStartup],
        ["zh-CN", zhCN.storageStartup],
    ] as const)("blocks newer schemas with %s copy and a quit action", async (language, copy) => {
        await i18n.changeLanguage(language);
        mocks.status.mockResolvedValue({ code: "schema_too_new", storedVersion: 59, supportedVersion: 58 });
        renderGate();

        const alert = await screen.findByRole("alert");
        expect(alert).toHaveTextContent(copy.newerTitle);
        expect(alert).toHaveTextContent(copy.newerDescription
            .replace("{{storedVersion}}", "59")
            .replace("{{supportedVersion}}", "58"));
        expect(mocks.mounted).not.toHaveBeenCalled();
        fireEvent.click(screen.getByRole("button", { name: copy.quit }));
        expect(mocks.quit).toHaveBeenCalledTimes(1);
    });

    it("keeps the app blocked when the status request fails", async () => {
        mocks.status.mockRejectedValue(new Error("bridge unavailable"));
        renderGate();

        expect(await screen.findByRole("alert")).toHaveTextContent(en.storageStartup.unavailableDescription);
        expect(mocks.mounted).not.toHaveBeenCalled();
    });

    it("shows cut recovery failure and resumes startup when retry succeeds", async () => {
        mocks.status.mockResolvedValueOnce({ code: "cut_recovery_failed" }).mockResolvedValueOnce({ code: "" });
        renderGate();
        expect(await screen.findByRole("alert")).toHaveTextContent(en.storageStartup.recoveryFailed);
        fireEvent.click(screen.getByRole("button", { name: en.storageStartup.retry }));
        expect(mocks.emit).toHaveBeenCalledWith("storage:retry-cut");
        const subscription = mocks.on.mock.calls.find(([event]) => event === "storage:retry-cut-started");
        await act(async () => subscription![1]());
        expect(screen.getByText("app-loaded")).toBeInTheDocument();
    });

    it("keeps the app blocked for other storage failures", async () => {
        mocks.status.mockResolvedValue({ code: "storage_unavailable" });
        renderGate();

        expect(await screen.findByRole("alert")).toHaveTextContent(en.storageStartup.unavailableTitle);
        expect(mocks.mounted).not.toHaveBeenCalled();
    });
});
