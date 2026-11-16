import { render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it } from "vitest";
import i18n from "@/i18n";
import { TypingIndicator } from "./typing-indicator";

beforeEach(async () => {
    await i18n.changeLanguage("en");
});

describe("TypingIndicator", () => {
    it("says Isoform is starting up while a sent message waits for the agent", () => {
        render(<TypingIndicator startingUp />);
        expect(screen.getByRole("status")).toHaveTextContent("Starting up. Your message is answered as soon as Isoform is ready.");
    });

    it("is only the dots once the agent is ready", () => {
        render(<TypingIndicator />);
        expect(screen.queryByRole("status")).toBeNull();
    });
});
