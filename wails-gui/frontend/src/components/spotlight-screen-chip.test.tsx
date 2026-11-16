import { fireEvent, render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import i18n from "@/i18n";
import { PillInput } from "../../../quick-chat-spotlight/frontend/src/components/PillInput";

vi.mock("@/hooks/useVoiceInput", () => ({
    useVoiceInput: () => ({ state: "idle", audioLevel: 0, error: null, startRecording: vi.fn(), stopAndTranscribe: vi.fn() }),
}));
vi.mock("../../../quick-chat-spotlight/frontend/src/lib/wails-app-stub", () => ({ HideWindow: vi.fn() }));

const mail = { bundleID: "com.apple.mail", appName: "Mail", windowTitle: "Priya Shah" };

beforeEach(async () => {
    await i18n.changeLanguage("en");
});

// computer-chat v1.0.0 rule 8: what's on screen is offered as a chip and
// goes with the question only while the chip is there.
describe("Quick chat screen chip", () => {
    it("offers what's on screen and sends it while the chip is there", () => {
        const onSubmit = vi.fn();
        render(<PillInput detection={mail} onSubmit={onSubmit} showNonce={1} />);
        expect(screen.getByText("About: what's on screen · Mail, Priya Shah")).toBeInTheDocument();
        const field = screen.getByPlaceholderText("Ask about your day, or correct Isoform");
        fireEvent.change(field, { target: { value: "What did Priya ask for?" } });
        fireEvent.keyDown(field, { key: "Enter" });
        expect(onSubmit).toHaveBeenCalledWith("What did Priya ask for?", true);
    });

    it("x removes the chip, and the question goes without the screen", () => {
        const onSubmit = vi.fn();
        render(<PillInput detection={mail} onSubmit={onSubmit} showNonce={1} />);
        fireEvent.click(screen.getByRole("button", { name: "Don't send what's on screen" }));
        expect(screen.queryByText(/what's on screen/)).toBeNull();
        const field = screen.getByPlaceholderText("Ask about your day, or correct Isoform");
        fireEvent.change(field, { target: { value: "Plan my afternoon" } });
        fireEvent.keyDown(field, { key: "Enter" });
        expect(onSubmit).toHaveBeenCalledWith("Plan my afternoon", false);
    });

    it("Backspace in an empty field removes the chip", () => {
        render(<PillInput detection={mail} onSubmit={vi.fn()} showNonce={1} />);
        fireEvent.keyDown(screen.getByPlaceholderText("Ask about your day, or correct Isoform"), { key: "Backspace" });
        expect(screen.queryByText(/what's on screen/)).toBeNull();
    });

    it("shows no chip without app context, and sends no screen", () => {
        const onSubmit = vi.fn();
        render(<PillInput detection={{ bundleID: "", appName: "" }} onSubmit={onSubmit} showNonce={1} />);
        expect(screen.queryByText(/what's on screen/)).toBeNull();
        const field = screen.getByPlaceholderText("Ask about your day, or correct Isoform");
        fireEvent.change(field, { target: { value: "Hi" } });
        fireEvent.keyDown(field, { key: "Enter" });
        expect(onSubmit).toHaveBeenCalledWith("Hi", false);
    });
});
