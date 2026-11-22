import { fireEvent, render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import i18n from "@/i18n";
import en from "@/i18n/locales/en/app.json";
import zh from "@/i18n/locales/zh-CN/app.json";
import { TroubleLine } from "./trouble-line";
import source from "./trouble-line.tsx?raw";

const mocks = vi.hoisted(() => ({
    openFix: vi.fn(async (_kind: string) => {}),
    signIn: vi.fn(async (_id: string) => {}),
}));
vi.mock("../../wailsjs/runtime/runtime", () => ({ EventsOn: vi.fn(() => () => {}) }));
vi.mock("@/lib/wails-shim-app", () => ({
    GetTrouble: vi.fn(async () => ({ items: [] })),
    OpenTroubleFix: mocks.openFix,
    OpenAssistantSignIn: mocks.signIn,
}));

const since = new Date(2026, 10, 15, 14, 10).toISOString();

describe("TroubleLine (computer-trouble v1.0.0)", () => {
    beforeEach(async () => {
        await i18n.changeLanguage("en");
        mocks.openFix.mockClear();
        mocks.signIn.mockClear();
    });

    it("says nothing when all is well", () => {
        const { container } = render(<TroubleLine lines={[]} />);
        expect(container).toBeEmptyDOMElement();
    });

    it("shows the first by cost, its one fix, and the rest behind more", () => {
        render(<TroubleLine lines={[{ kind: "screen", since }, { kind: "phone", since }]} />);
        expect(screen.getByText("Isoform hasn't seen your screen since 2:10 PM.")).toBeInTheDocument();
        expect(screen.getByText(/Screen Recording is off for Isoform\./)).toBeInTheDocument();
        expect(document.querySelector("[data-trouble-kind=phone]")).toBeNull();
        fireEvent.click(screen.getByRole("button", { name: en.trouble.openSettings }));
        expect(mocks.openFix).toHaveBeenCalledWith("screen");
        fireEvent.click(screen.getByText("1 more ›"));
        expect(document.querySelector("[data-trouble-kind=phone]")).toHaveTextContent("Your phone hasn't been in touch since 2:10 PM, so nothing from your watch has come since.");
        // Nothing to press for the phone.
        expect(document.querySelector("[data-trouble-kind=phone] button")).toBeNull();
    });

    it("signs the assistant in from its line", () => {
        render(<TroubleLine lines={[{ kind: "signed_out", since, assistant: "claude-code", fix: "sign_in" }]} />);
        expect(screen.getByText("Claude Code has been signed out since 2:10 PM, so briefs are shorter and Chat can't answer.")).toBeInTheDocument();
        fireEvent.click(screen.getByRole("button", { name: en.trouble.signIn }));
        expect(mocks.signIn).toHaveBeenCalledWith("claude-code");
    });

    it("keeps both locales in step and draws no edge bar", () => {
        expect(Object.keys(zh.trouble).sort()).toEqual(Object.keys(en.trouble).sort());
        expect(source).not.toMatch(/border-l\b|w-0\.5|w-\[2px\]/);
    });
});
