import { useState } from "react";
import {
    act,
    fireEvent,
    render,
    screen,
    waitFor,
} from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import i18n from "@/i18n";
import { ToolDownloadSheet } from "./tool-download-sheet";
import type { ToolDownloadPlan } from "@/lib/wails-shim-app";

const mocks = vi.hoisted(() => ({
    plan: vi.fn(),
    start: vi.fn(),
}));
vi.mock("@/lib/wails-shim-app", () => ({
    PlanToolDownload: mocks.plan,
    StartToolDownload: mocks.start,
}));

function makePlan(token: string, expiresAt: number): ToolDownloadPlan {
    return {
        entry: {
            id: "windows-git",
            displayName: "Git for Windows (MinGit)",
            version: "2.47.1",
            sizeBytes: 47_241_394,
            source: "https://github.com/git-for-windows/git/releases/download/v2.47.1.windows.1/MinGit-2.47.1-64-bit.zip",
            licence: "GPL-2.0",
            licenceUrl: "https://github.com/git-for-windows/git/blob/v2.47.1.windows.1/COPYING",
            sha256: "sha",
        },
        folder: "C:\\Users\\test\\AppData\\Roaming\\Isoform\\git",
        token,
        expiresAt,
    };
}
const fresh = () => makePlan("token-fresh", Date.now() + 60_000);

function Harness({ onInstalled }: { onInstalled?: () => void }) {
    const [open, setOpen] = useState(true);
    return (
        <ToolDownloadSheet
            open={open}
            onOpenChange={setOpen}
            onInstalled={onInstalled}
        />
    );
}

beforeEach(async () => {
    await i18n.changeLanguage("en");
    vi.resetAllMocks();
    mocks.plan.mockResolvedValue(fresh());
    mocks.start.mockResolvedValue(undefined);
});

describe("shared tool download sheet", () => {
    it("plans, shows the manifest and destination, and confirms with the exact plan once", async () => {
        const planned = fresh();
        mocks.plan.mockResolvedValue(planned);
        const onInstalled = vi.fn();
        render(<Harness onInstalled={onInstalled} />);

        expect(
            await screen.findByText("Git for Windows (MinGit)"),
        ).toBeInTheDocument();
        expect(screen.getByText("Version 2.47.1")).toBeInTheDocument();
        expect(screen.getByText("GPL-2.0")).toBeInTheDocument();
        expect(screen.getByText(planned.entry.source)).toBeInTheDocument();
        expect(screen.getByText(planned.folder)).toBeInTheDocument();

        fireEvent.click(screen.getByTestId("tool-download-confirm"));
        await waitFor(() =>
            expect(mocks.start).toHaveBeenCalledWith(planned),
        );
        expect(mocks.start).toHaveBeenCalledTimes(1);
        await waitFor(() => expect(onInstalled).toHaveBeenCalledOnce());
    });

    it("never starts a download on cancel or dismissal", async () => {
        render(<Harness />);
        await screen.findByText("Git for Windows (MinGit)");

        fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
        expect(mocks.start).not.toHaveBeenCalled();
        expect(
            screen.queryByTestId("tool-download-plan"),
        ).not.toBeInTheDocument();
    });

    it("disables duplicate confirmation while Start is pending", async () => {
        let resolveStart: () => void = () => {};
        mocks.start.mockReturnValue(
            new Promise<void>((resolve) => {
                resolveStart = resolve;
            }),
        );
        render(<Harness />);
        const confirm = await screen.findByTestId("tool-download-confirm");

        fireEvent.click(confirm);
        await waitFor(() => expect(mocks.start).toHaveBeenCalledTimes(1));
        expect(screen.getByTestId("tool-download-confirm")).toBeDisabled();
        expect(screen.getByText("Installing…")).toBeInTheDocument();

        fireEvent.click(screen.getByTestId("tool-download-confirm"));
        expect(mocks.start).toHaveBeenCalledTimes(1);

        await act(async () => resolveStart());
    });

    it("failure requires a fresh plan and a new explicit confirmation", async () => {
        const first = fresh();
        const second = makePlan("token-second", Date.now() + 60_000);
        mocks.plan
            .mockResolvedValueOnce(first)
            .mockResolvedValueOnce(second);
        mocks.start.mockRejectedValueOnce(new Error("network down"));

        render(<Harness />);
        await screen.findByText("Git for Windows (MinGit)");
        fireEvent.click(screen.getByTestId("tool-download-confirm"));

        expect(await screen.findByRole("alert")).toBeInTheDocument();
        expect(mocks.start).toHaveBeenCalledWith(first);

        fireEvent.click(screen.getByRole("button", { name: "Retry" }));
        await waitFor(() => expect(mocks.plan).toHaveBeenCalledTimes(2));
        await screen.findByText("Git for Windows (MinGit)");

        fireEvent.click(screen.getByTestId("tool-download-confirm"));
        await waitFor(() => expect(mocks.start).toHaveBeenCalledWith(second));
    });

    it("shows a planning failure and retries without starting", async () => {
        mocks.plan.mockRejectedValueOnce(new Error("no plan"));
        render(<Harness />);

        expect(await screen.findByRole("alert")).toBeInTheDocument();
        expect(mocks.start).not.toHaveBeenCalled();

        fireEvent.click(screen.getByRole("button", { name: "Retry" }));
        await screen.findByText("Git for Windows (MinGit)");
        expect(mocks.plan).toHaveBeenCalledTimes(2);
        expect(mocks.start).not.toHaveBeenCalled();
    });

    it("never starts an expired plan and requires a new review", async () => {
        const expired = makePlan("token-expired", Date.now() - 1);
        const renewed = makePlan("token-renewed", Date.now() + 60_000);
        mocks.plan
            .mockResolvedValueOnce(expired)
            .mockResolvedValueOnce(renewed);

        render(<Harness />);
        await screen.findByText("Git for Windows (MinGit)");
        fireEvent.click(screen.getByTestId("tool-download-confirm"));

        expect(mocks.start).not.toHaveBeenCalled();
        expect(await screen.findByRole("alert")).toBeInTheDocument();

        fireEvent.click(screen.getByRole("button", { name: "Retry" }));
        await waitFor(() => expect(mocks.plan).toHaveBeenCalledTimes(2));
        await screen.findByText("Git for Windows (MinGit)");

        fireEvent.click(screen.getByTestId("tool-download-confirm"));
        await waitFor(() => expect(mocks.start).toHaveBeenCalledWith(renewed));
    });
});
