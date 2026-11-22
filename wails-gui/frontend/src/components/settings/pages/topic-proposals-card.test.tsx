import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import i18n from "@/i18n";
import en from "@/i18n/locales/en/settings.json";
import zh from "@/i18n/locales/zh-CN/settings.json";
import { TopicProposalsCard } from "./topic-proposals-card";

const mocks = vi.hoisted(() => ({
    enabled: false,
    answering: "claude-code",
    set: vi.fn(async (enabled: boolean) => ({ enabled, topics: [] })),
}));

vi.mock("@/lib/wails-shim-app", () => ({
    GetAssistantChoice: vi.fn(async () => ({ answering: mocks.answering })),
    GetDailyTopics: vi.fn(async () => ({ enabled: mocks.enabled, topics: [] })),
    SetDailyTopicsEnabled: mocks.set,
}));

describe("Settings › Proposals from your topics", () => {
    beforeEach(async () => {
        await i18n.changeLanguage("en");
        mocks.enabled = false;
        mocks.answering = "claude-code";
        mocks.set.mockClear();
    });

    it("reads its state from GetDailyTopics and turns on with SetDailyTopicsEnabled", async () => {
        render(<TopicProposalsCard />);
        const toggle = screen.getByRole("switch", { name: en.topicProposals.title });
        await waitFor(() => expect(toggle).not.toBeDisabled());
        expect(toggle).not.toBeChecked();
        fireEvent.click(toggle);
        await waitFor(() => expect(mocks.set).toHaveBeenCalledWith(true));
        await waitFor(() => expect(toggle).toBeChecked());
    });

    it("shows on when topics are on, and turns off", async () => {
        mocks.enabled = true;
        render(<TopicProposalsCard />);
        const toggle = screen.getByRole("switch", { name: en.topicProposals.title });
        await waitFor(() => expect(toggle).toBeChecked());
        fireEvent.click(toggle);
        await waitFor(() => expect(mocks.set).toHaveBeenCalledWith(false));
        await waitFor(() => expect(toggle).not.toBeChecked());
    });

    // Never less than the code sends: proposeDailyTopicWork sends each topic's
    // name with its why, latest and next lines (redacted) and declined titles.
    it("says that topic names and what Isoform noted about each go to the chosen assistant", async () => {
        render(<TopicProposalsCard />);
        expect(
            await screen.findByText(
                "Topics offer their next piece of work in Your work. To write it, your topic names and what Isoform noted about each go to Claude Code.",
            ),
        ).toBeInTheDocument();
    });

    it("names the AI you chose when the assistant is not Claude Code, Codex or DeepSeek", async () => {
        mocks.answering = "isoform";
        render(<TopicProposalsCard />);
        expect(await screen.findByText(/what Isoform noted about each go to the AI you chose\.$/)).toBeInTheDocument();
    });

    it("keeps both locales in step", () => {
        expect(Object.keys(zh.topicProposals).sort()).toEqual(Object.keys(en.topicProposals).sort());
    });
});
