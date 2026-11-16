import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import i18n from "@/i18n";
import en from "@/i18n/locales/en/settings.json";
import zh from "@/i18n/locales/zh-CN/settings.json";
import { ClosedEyesPage } from "./closed-eyes-page";

const mocks = vi.hoisted(() => ({
    closed: false,
    resumeAt: "",
    listeners: new Map<string, (value: unknown) => void>(),
    pauseFor: vi.fn(async (_minutes: number) => {}),
    setClosed: vi.fn(async (_closed: boolean) => {}),
}));

vi.mock("@/lib/wails-shim-app", () => ({
    IsClosedEyesModeEnabled: vi.fn(async () => mocks.closed),
    GetClosedEyesResumeAt: vi.fn(async () => mocks.resumeAt),
    PauseObservationFor: mocks.pauseFor,
    SetClosedEyesModeEnabled: mocks.setClosed,
}));
vi.mock("../../../../wailsjs/runtime/runtime", () => ({
    EventsOn: vi.fn((name: string, handler: (value: unknown) => void) => {
        mocks.listeners.set(name, handler);
        return () => mocks.listeners.delete(name);
    }),
}));

describe("Settings › Closed Eyes", () => {
    beforeEach(async () => {
        await i18n.changeLanguage("en");
        mocks.closed = false;
        mocks.resumeAt = "";
        mocks.pauseFor.mockClear();
        mocks.setClosed.mockClear();
    });

    it("asks for how long when turned on, and closes for 15 minutes", async () => {
        render(<ClosedEyesPage />);
        const toggle = screen.getByRole("switch", { name: en.closedEyes.title });
        await waitFor(() => expect(toggle).not.toBeDisabled());
        expect(screen.queryByRole("group", { name: en.closedEyes.howLong })).toBeNull();

        fireEvent.click(toggle);
        const choices = screen.getByRole("group", { name: en.closedEyes.howLong });
        expect(choices).toHaveTextContent("15 minutes");
        expect(choices).toHaveTextContent("60 minutes");
        expect(choices).toHaveTextContent(en.closedEyes.untilIEnd);
        expect(mocks.pauseFor).not.toHaveBeenCalled();

        mocks.closed = true;
        mocks.resumeAt = new Date(Date.now() + 15 * 60_000).toISOString();
        await act(async () => fireEvent.click(screen.getByRole("button", { name: "15 minutes" })));
        expect(mocks.pauseFor).toHaveBeenCalledWith(15);
        expect(await screen.findByText(/^Closed Eyes until /)).toBeInTheDocument();
        expect(screen.queryByRole("group", { name: en.closedEyes.howLong })).toBeNull();
    });

    it("closes until you end it, and opens again from the switch", async () => {
        render(<ClosedEyesPage />);
        const toggle = screen.getByRole("switch", { name: en.closedEyes.title });
        await waitFor(() => expect(toggle).not.toBeDisabled());
        fireEvent.click(toggle);
        mocks.closed = true;
        await act(async () => fireEvent.click(screen.getByRole("button", { name: en.closedEyes.untilIEnd })));
        expect(mocks.setClosed).toHaveBeenCalledWith(true);
        expect(await screen.findByText(en.closedEyes.untilEnded)).toBeInTheDocument();

        mocks.closed = false;
        await act(async () => fireEvent.click(screen.getByRole("switch", { name: en.closedEyes.title })));
        expect(mocks.setClosed).toHaveBeenLastCalledWith(false);
        await waitFor(() => expect(screen.getByRole("switch", { name: en.closedEyes.title })).not.toBeChecked());
    });

    it("follows Closed Eyes changed from the menu bar, the phone or the watch", async () => {
        render(<ClosedEyesPage />);
        const toggle = screen.getByRole("switch", { name: en.closedEyes.title });
        await waitFor(() => expect(toggle).not.toBeChecked());
        mocks.closed = true;
        await act(async () => mocks.listeners.get("closed-eyes:changed")?.(true));
        await waitFor(() => expect(toggle).toBeChecked());
    });

    it("cancels the question when switched back off before choosing", async () => {
        render(<ClosedEyesPage />);
        const toggle = screen.getByRole("switch", { name: en.closedEyes.title });
        await waitFor(() => expect(toggle).not.toBeDisabled());
        fireEvent.click(toggle);
        fireEvent.click(toggle);
        expect(screen.queryByRole("group", { name: en.closedEyes.howLong })).toBeNull();
        expect(mocks.setClosed).not.toHaveBeenCalled();
    });

    it("keeps both locales in step", () => {
        expect(Object.keys(zh.closedEyes).sort()).toEqual(Object.keys(en.closedEyes).sort());
    });
});
