import { cleanup, fireEvent, render, screen, within, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import i18n from "@/i18n";
import { reconcileTodayTopics } from "@/hooks/useTodayTopics";
import { TodayTopicsGrid, topicWhy } from "./TodayTopicsGrid";
import type { TodayTopic } from "./topics";

const NOW = new Date(2026, 9, 21, 9, 0);
const work: TodayTopic = {
    id: "halcyon", name: "Halcyon ERP sync", kind: "work", status: "waiting", pinned: false,
    latest: { text: "They want the sync moved up to Oct 30.", at: new Date(2026, 9, 21, 8, 0).toISOString() }, next: { text: "standup", at: new Date(2026, 9, 21, 14, 0).toISOString() },
};
const followed: TodayTopic = {
    id: "wearables", name: "Wearable AI", kind: "followed", status: "news", newCount: 1, pinned: false,
    latest: { text: "A teardown of an always-on recorder.", at: new Date(2026, 9, 20, 18, 0).toISOString() }, lastSeenAt: new Date(2026, 9, 20, 18, 0).toISOString(),
};

function renderGrid(props: Partial<Parameters<typeof TodayTopicsGrid>[0]> = {}) {
    const actions = { onPick: vi.fn(), onRetry: vi.fn(), onEdit: vi.fn() };
    render(<TodayTopicsGrid topics={[work, followed]} status="ready" selectedId="halcyon" now={NOW} {...actions} {...props} />);
    return actions;
}

beforeEach(async () => {
    await i18n.changeLanguage("en");
});

describe("TodayTopicsGrid (RC 1)", () => {
    it.each(["Opened a page, clicked another, then visited a third page", 'Read "Window title \\ project"', "A".repeat(60)])("uses a day-count line for raw or long activity: %s", (text) => {
        renderGrid({ topics: [{ ...work, latest: { text, at: work.latest!.at } }] });
        const line = screen.getByTestId("today-topic-line").textContent ?? "";
        expect(line).toBe("1 day this week · Last: 8:00 AM");
        expect([...line].length).toBeLessThanOrEqual(50);
    });
    it("retains topics and offers a retry with the last successful time", () => {
        const actions = renderGrid({ refreshFailed: true, lastUpdatedAt: new Date(2026, 9, 30, 10, 12).getTime() });
        expect(screen.getByRole("alert")).toHaveTextContent("Couldn't refresh · showing 10:12 AM · Try again");
        expect(screen.getAllByTestId("today-topic-card")).toHaveLength(2);
        fireEvent.click(screen.getByRole("button", { name: "Try again" }));
        expect(actions.onRetry).toHaveBeenCalledOnce();
    });

    it("lets one or two topics fill the row instead of leaving empty columns", () => {
        renderGrid({ topics: [work] });
        expect(screen.getByTestId("today-topic-card").parentElement).toHaveAttribute("data-count", "1");
        expect(screen.getByTestId("today-topic-card")).toHaveClass("today-topic-card-alone");
        cleanup();
        renderGrid();
        expect(screen.getAllByTestId("today-topic-card")[0].parentElement).toHaveAttribute("data-count", "2");
        expect(screen.getAllByTestId("today-topic-card")[0]).not.toHaveClass("today-topic-card-alone");
    });

    it("shows a new find on its topic card", () => {
        renderGrid({ topics: [{ ...work, latestFind: { text: "Halcyon posted Q3 results", at: work.latest!.at } }] });
        const card = screen.getByTestId("today-topic-card");
        expect(within(card).getByText(/^New: Halcyon posted Q3 results · /)).toHaveClass("text-accent");
        expect(card).not.toHaveTextContent("They want the sync");
    });

    it("shows the new-find label in Chinese", async () => {
        await i18n.changeLanguage("zh-CN");
        renderGrid({ topics: [{ ...work, latestFind: { text: "Q3 results", at: work.latest!.at } }] });
        expect(screen.getByTestId("today-topic-card")).toHaveTextContent(/新动态：Q3 results · /);
    });
    it.each(["en", "zh-CN"])("keeps a short timed line for an overlong or multiline update (%s)", async (language) => {
        await i18n.changeLanguage(language);
        for (const text of ["A".repeat(100), "First click\nSecond click"]) {
            const view = render(<TodayTopicsGrid topics={[{ ...work, latest: { text, at: work.latest!.at } }]} status="ready" selectedId={null} now={NOW} onPick={vi.fn()} onRetry={vi.fn()} onEdit={vi.fn()} />);
            const line = within(screen.getByTestId("today-topic-card")).getByTestId("today-topic-line");
            expect(line).toHaveTextContent(language === "en" ? /^1 day this week · Last: 8:00 AM$/ : /^本周 1 天 · 最近：8:00$/);
            expect([...(line.textContent ?? "")].length).toBeLessThanOrEqual(50);
            view.unmount();
        }
    });
    it.each(["en", "zh-CN"])("keeps the card for an older moment, with its date and no day count (%s)", async (language) => {
        await i18n.changeLanguage(language);
        renderGrid({ topics: [{ ...work, latest: { text: "Opened a page, clicked another", at: new Date(2026, 9, 10, 8).toISOString() } }], selectedId: null });
        const line = screen.getByTestId("today-topic-line").textContent ?? "";
        expect(line).toBe(language === "en" ? "Last: Oct 10" : "最近：10月10日");
        expect(line).not.toMatch(/day/);
    });
    it("leads with what took most of the week, not the first topic listed", () => {
        const game = { ...work, id: "game", name: "Moxgate Simulator", status: undefined, activityDays: ["2026-10-21", "2026-10-20"] };
        const project = { ...work, id: "project", name: "Atlas launch", status: undefined, activityDays: ["2026-10-21", "2026-10-20", "2026-10-19", "2026-10-18", "2026-10-17"] };
        renderGrid({ topics: [game, project], selectedId: null });
        const names = screen.getAllByTestId("today-topic-card").map((card) => card.textContent);
        expect(names[0]).toMatch(/^Atlas launch/);
        expect(names[1]).toMatch(/^Moxgate Simulator/);
    });
    it.each(["en", "zh-CN"])("says how many days this week and when the last one was, never a bare status (%s)", async (language) => {
        await i18n.changeLanguage(language);
        renderGrid({ topics: [{ ...work, latest: { text: "Opened a page", at: work.latest!.at }, activityDays: ["2026-10-21", "2026-10-20", "2026-10-18"] }], selectedId: null });
        const line = screen.getByTestId("today-topic-line").textContent ?? "";
        expect(line).toBe(language === "en" ? "3 days this week · Last: today" : "本周 3 天 · 最近：今天");
        expect(line).not.toMatch(/^(Active|有活动|Last moved|最近变动)/);
    });
    it.each(["en", "zh-CN"])("shows a grounded quiet line for a pinned topic and omits an ungrounded card (%s)", async (language) => {
        await i18n.changeLanguage(language);
        renderGrid({ topics: [{ ...work, pinned: true, latest: { text: "Updated", at: new Date(2026, 9, 20, 8).toISOString() } }, { ...followed, id: "missing", latest: undefined, lastSeenAt: undefined }] });
        const cards = screen.getAllByTestId("today-topic-card");
        expect(cards).toHaveLength(1);
        expect(within(cards[0]).getByTestId("today-topic-line")).toHaveTextContent(language === "en" ? "Quiet since Tuesday" : "自星期二以来没有新动态");
    });
    it("shows only the name and a timed change, with no kind, status or next step", () => {
        const actions = renderGrid();
        const cards = screen.getAllByTestId("today-topic-card");
        expect(cards[0]).not.toHaveTextContent("Project");
        expect(cards[0]).not.toHaveTextContent("Waiting on you");
        expect(cards[0]).toHaveTextContent("They want the sync moved up to Oct 30.");
        expect(cards[0]).toHaveTextContent(/8:00\sAM/);
        expect(cards[0]).not.toHaveTextContent("standup");
        expect(cards[0]).toHaveAttribute("aria-pressed", "true");
        expect(cards[1]).toHaveTextContent("Wearable AI");
        expect(cards[1]).not.toHaveTextContent("Following");
        expect(cards[1]).toHaveTextContent("A teardown");
        expect(cards[1]).toHaveAttribute("aria-pressed", "false");
        fireEvent.click(cards[1]);
        expect(actions.onPick).toHaveBeenCalledWith(followed);
        fireEvent.click(screen.getByRole("button", { name: "Edit topics" }));
        expect(actions.onEdit).toHaveBeenCalledTimes(1);
        fireEvent.click(screen.getByRole("button", { name: "Refresh topics" }));
        expect(actions.onRetry).toHaveBeenCalledTimes(1);
    });

    it.each([
        [new Date(2026, 9, 21, 7, 0), "You spent time on Wearable AI today"],
        [new Date(2026, 9, 20, 7, 0), "You spent time on Wearable AI yesterday"],
        [new Date(2026, 9, 18, 7, 0), "You spent time on Wearable AI on Sunday"],
        [new Date(2026, 9, 1, 7, 0), "You spent time on Wearable AI on October 1"],
    ])("says when the user last spent time on a followed topic (%s)", (seen, line) => {
        expect(topicWhy({ ...followed, lastSeenAt: seen.toISOString() }, NOW, "en", (key, options) => String(i18n.t(key as never, { ...options, ns: "today" })))).toBe(line);
    });

    it("leaves a followed topic's why line out when the time is withheld", () => {
        renderGrid({ topics: [{ ...followed, lastSeenAt: undefined }] });
        const card = screen.getByTestId("today-topic-card");
        expect(card).toHaveTextContent("Wearable AI");
        expect(card).not.toHaveTextContent("You spent time on");
    });

    it("draws grey cards while the topics are first read", () => {
        renderGrid({ topics: [], status: "loading" });
        expect(screen.getByTestId("today-topics-loading")).toHaveAttribute("aria-busy", "true");
    });

    it.each(["en", "zh-CN"])("shows the gathering message before loading on first run (%s)", async (language) => {
        await i18n.changeLanguage(language);
        renderGrid({ topics: [], status: "loading", refreshing: true, firstRun: true });
        expect(screen.getByTestId("today-topics-gathering")).toHaveTextContent(
            language === "en" ? "Isoform is gathering your topics." : "Isoform 正在收集你的话题。",
        );
        expect(screen.queryByTestId("today-topics-loading")).not.toBeInTheDocument();
        expect(screen.queryByText(/not available/i)).not.toBeInTheDocument();
    });

    it("keeps readable rows visible while more topics refresh", async () => {
        renderGrid({ refreshing: true });
        expect(screen.getAllByTestId("today-topic-card")).toHaveLength(2);
        expect(screen.queryByRole("status")).not.toBeInTheDocument();
        await waitFor(() => expect(screen.getByRole("status")).toHaveTextContent("Refreshing your topics"), { timeout: 1500 });
        expect(screen.queryByTestId("today-topics-empty")).not.toBeInTheDocument();
    });

    it("says it could not load the topics, with Try again", () => {
        const actions = renderGrid({ topics: [], status: "failed" });
        expect(screen.getByRole("alert")).toHaveTextContent("Could not load your topics.");
        fireEvent.click(screen.getByRole("button", { name: "Try again" }));
        expect(actions.onRetry).toHaveBeenCalledTimes(1);
    });

    it("keeps the topics on screen when a later read fails", () => {
        renderGrid({ status: "failed" });
        expect(screen.getAllByTestId("today-topic-card")).toHaveLength(2);
        expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    });

    it("shows the published empty state when a snapshot filters to zero topics", () => {
        renderGrid({ topics: [], status: "ready", firstRun: false });
        expect(screen.getByTestId("today-topics-empty")).toBeInTheDocument();
        expect(screen.queryByTestId("today-topics-gathering")).not.toBeInTheDocument();
        expect(screen.queryByText(/not available/i)).not.toBeInTheDocument();
    });

    it("says Isoform does not know the topics yet", () => {
        renderGrid({ topics: [] });
        expect(screen.getByTestId("today-topics-empty")).toHaveTextContent("Your topics show up after about an hour of work on this computer.");
    });

    it("reads in Chinese", async () => {
        await i18n.changeLanguage("zh-CN");
        renderGrid();
        const grid = screen.getByTestId("today-topics");
        expect(within(grid).getByRole("heading", { name: "你的话题" })).toBeInTheDocument();
        expect(within(grid).getAllByTestId("today-topic-card")[0]).not.toHaveTextContent("等你处理");
    });

    it("preserves the card object and DOM node for an unchanged topic", () => {
        const { rerender } = render(
            <TodayTopicsGrid topics={[work, followed]} status="ready" selectedId="halcyon" now={NOW} onPick={vi.fn()} onRetry={vi.fn()} onEdit={vi.fn()} />,
        );
        const cardA = screen.getAllByTestId("today-topic-card")[0];
        const next = reconcileTodayTopics([work, followed], [structuredClone(work), { ...followed, name: "Wearable AI updated" }]);
        expect(next[0]).toBe(work);
        rerender(
            <TodayTopicsGrid topics={next} status="ready" selectedId="halcyon" now={NOW} onPick={vi.fn()} onRetry={vi.fn()} onEdit={vi.fn()} />,
        );
        expect(screen.getAllByTestId("today-topic-card")[0]).toBe(cardA);
    });
});

it('omits redacted topics and the chat itself from Today cards', () => {
    renderGrid({ topics: [{ ...work, name: '[REDACTE…' }, { ...followed, name: 'Isoform Main chat' }] });
    expect(screen.queryAllByTestId('today-topic-card')).toHaveLength(0);
});
