import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, expect, it, vi } from "vitest";
import i18n from "@/i18n";
import type { SetupAgentProbe } from "@/lib/setup-backend";
import { GatewaySetupNotice } from "./gateway-setup-notice";

const mocks = vi.hoisted(() => ({ connect: vi.fn(), downloaded: undefined as (() => void) | undefined }));
vi.mock("@/lib/wails-shim-app", () => ({ ReconnectModelGateway: mocks.connect }));
vi.mock("./tool-download-sheet", () => ({
    ToolDownloadSheet: ({ open, toolId, onInstalled }: { open: boolean; toolId: string; onInstalled: () => void }) => {
        mocks.downloaded = onInstalled;
        return open ? <div data-testid="gateway-review">{toolId}</div> : null;
    },
}));

const gateway = (installed = false) => ({ installed, downloadable: true, downloadBytes: 74073600 }) as SetupAgentProbe["magpie"];

beforeEach(() => { mocks.connect.mockReset(); mocks.connect.mockResolvedValue(undefined); });

it.each(["en", "zh-CN"])("offers an explicit gateway download in %s, then connects after install", async (lang) => {
    await i18n.changeLanguage(lang);
    const ready = vi.fn();
    render(<GatewaySetupNotice gateway={gateway()} onReady={ready} />);
    expect(mocks.connect).not.toHaveBeenCalled();
    expect(screen.queryByTestId("gateway-review")).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: lang === "en" ? "Download and connect (74 MB)" : "下载并连接（74 MB）" }));
    expect(screen.getByTestId("gateway-review")).toHaveTextContent("model-gateway");
    expect(mocks.connect).not.toHaveBeenCalled();
    await act(async () => mocks.downloaded?.());
    await waitFor(() => expect(ready).toHaveBeenCalledOnce());
    expect(mocks.connect).toHaveBeenCalledOnce();
});

it("retries an installed gateway connection without downloading it again", async () => {
    await i18n.changeLanguage("en");
    const ready = vi.fn();
    mocks.connect.mockRejectedValueOnce(new Error("not ready"));
    render(<GatewaySetupNotice gateway={gateway(true)} onReady={ready} />);
    fireEvent.click(screen.getByRole("button", { name: "Connect assistants" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("connection did not start");
    expect(ready).not.toHaveBeenCalled();
    expect(screen.queryByTestId("gateway-review")).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Connect assistants" }));
    await waitFor(() => expect(ready).toHaveBeenCalledOnce());
});
