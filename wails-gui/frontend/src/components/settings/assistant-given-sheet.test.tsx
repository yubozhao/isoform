import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import i18n from "@/i18n";
import en from "@/i18n/locales/en/settings.json";
import zh from "@/i18n/locales/zh-CN/settings.json";
import { AssistantGivenSheet } from "./assistant-given-sheet";

const at = (h: number, m = 0) => new Date(2026, 10, 20, h, m).getTime();

const mocks = vi.hoisted(() => ({
    view: null as unknown,
    fail: false,
    emit: vi.fn(),
}));
vi.mock("@/lib/wails-shim-app", () => ({
    GetAssistantGiven: vi.fn(async () => {
        if (mocks.fail) throw new Error("store closed");
        return mocks.view;
    }),
}));
vi.mock("../../../wailsjs/runtime/runtime", () => ({ EventsEmit: mocks.emit, EventsOn: vi.fn(() => () => {}) }));

describe("See them", () => {
    beforeEach(async () => {
        await i18n.changeLanguage("en");
        mocks.fail = false;
        mocks.emit.mockClear();
        mocks.view = {
            stretches: [{ from: at(9), to: at(11), times: 12, screen: true, everyMinutes: 10, wechat: 3, calls: 0, messages: 0 }],
            watch: [{ from: at(14), to: at(15), name: "Pricing with Sam" }, { from: at(19), to: at(19, 49) }],
            briefs: [{ briefId: "brief-1", at: at(8), sources: [{ kind: "mail", count: 4 }, { kind: "calendar", count: 1 }] }],
            answers: [{ itemId: "item-1", at: at(16), question: "What did Sam ask for?", sources: [{ kind: "memory", count: 2 }], removable: true }],
            proposals: [{ proposalId: "prop-1", at: at(7), title: "Draft the pricing note" }],
            // Timeline summaries that name their sessions: removable.
            sense: [{ group: "sense", from: at(12), to: at(13), times: 2, sources: [{ kind: "screen", count: 3 }], removable: true }],
            // A kind-only hour of learning about you that made claims: by the hour.
            about: [{ group: "about", from: at(10), to: at(11), times: 3, made: 2, sources: [], removable: false }],
            work: [],
            passes: [{ pass: "weekly", times: 2 }, { pass: "work_profile", times: 1 }],
            held: 5,
        };
    });
    afterEach(cleanup);

    it("lists each group by name and time, never the text", async () => {
        render(<AssistantGivenSheet open onOpenChange={() => {}} assistant="Claude" />);
        expect(await screen.findByText("What Claude was given, this week")).toBeInTheDocument();
        expect(screen.getByText("By name and time, never the text. Each entry is kept as long as what it came from.")).toBeInTheDocument();
        expect(screen.getByText("12 times")).toBeInTheDocument();
        expect(screen.getByText("From: text on your screen, about every 10 minutes while you worked, 3 WeChat windows, as pictures")).toBeInTheDocument();
        expect(screen.getByText("From: your watch · Pricing with Sam, 1 h")).toBeInTheDocument();
        // An unnamed watch row reads by its hour, as the phone does (design T15).
        expect(screen.getByText(/From: your watch · 7\sPM, 49 min of talk/)).toBeInTheDocument();
        // Counts carry their unit.
        expect(screen.getByText("From: Mail (4 threads), Calendar (1 event)")).toBeInTheDocument();
        // You's tabs hold lines (design v1.5.0 T19).
        expect(screen.getByText("From: About you (2 lines)")).toBeInTheDocument();
        expect(screen.getByText("“What did Sam ask for?”")).toBeInTheDocument();
        expect(screen.getByText("Draft the pricing note")).toBeInTheDocument();
        expect(screen.getAllByText(/Forget something in Activity, and its entries here go with it\. What Claude's provider keeps/).length).toBeGreaterThan(0);
    });

    it("shows four groups, with the Forget line only where it's true", async () => {
        render(<AssistantGivenSheet open onOpenChange={() => {}} assistant="Claude" />);
        await screen.findByText("Learning about you");
        const groups = [...document.querySelectorAll("[data-given-group]")];
        const titles = groups.map((g) => g.querySelector("h3")?.textContent ?? "");
        expect(titles.map((x) => x.replace(/\s*\d+$/, ""))).toEqual([
            "Making sense of your day", "Learning about you", "Work made for you", "Briefs", "Answers in Chat", "Proposals from your topics",
        ]);
        // The kind-only hour: "10 AM · Learning about you · 3 times", what it made, and see.byHour, not the Forget line.
        const about = groups[1] as HTMLElement;
        expect(about).toHaveTextContent(/10\sAM · Learning about you · 3 times/);
        expect(about).toHaveTextContent("From: what Isoform saw that hour · Made: 2 claims to check");
        expect(about.querySelector("[data-given-by-hour]")).toHaveTextContent("Counted by the hour, so Forget on one thing doesn't remove these yet.");
        expect(about.querySelector("[data-given-foot]")).toBeNull();
        // Every row of the day names what it read: the Forget line, no byHour.
        const sense = groups[0] as HTMLElement;
        expect(sense.querySelector("[data-given-foot]")).not.toBeNull();
        expect(sense.querySelector("[data-given-by-hour]")).toBeNull();
        // An empty group carries neither.
        expect(groups[2].querySelector("[data-given-foot], [data-given-by-hour]")).toBeNull();
    });

    it("counts the calls no group shows yet, and says nothing when there are none", async () => {
        render(<AssistantGivenSheet open onOpenChange={() => {}} assistant="Claude" />);
        const held = await screen.findByText("And 5 more times, to suggest questions and replies, counted by the hour.");
        expect(held.closest("[data-given-group]")).toBeNull();
        expect(held.querySelector("button")).toBeNull();
        expect(held.tagName).toBe("P");
        cleanup();
        (mocks.view as { held: number }).held = 0;
        render(<AssistantGivenSheet open onOpenChange={() => {}} assistant="Claude" />);
        await screen.findByText("Learning about you");
        expect(document.querySelector("[data-given-held]")).toBeNull();
        cleanup();
        mocks.fail = true;
        (mocks.view as { held: number }).held = 5;
        render(<AssistantGivenSheet open onOpenChange={() => {}} assistant="Claude" />);
        await screen.findByRole("alert");
        expect(document.querySelector("[data-given-held]")).toBeNull();
    });

    it("names the summary passes in Learning about you, after its hours, with their note and no Forget line", async () => {
        render(<AssistantGivenSheet open onOpenChange={() => {}} assistant="Claude" />);
        await screen.findByText("Learning about you");
        const about = [...document.querySelectorAll("[data-given-group]")][1] as HTMLElement;
        const rows = [...about.querySelectorAll("[data-given-row]")];
        expect(rows.map((r) => r.hasAttribute("data-given-pass"))).toEqual([false, true, true]);
        expect(rows[1]).toHaveTextContent("Your week, summed up · 2 times · From: summaries Isoform already wrote");
        expect(rows[2]).toHaveTextContent("Your work profile · 1 time · From: summaries Isoform already wrote");
        expect(about.querySelector("[data-given-pass-note]")).toHaveTextContent(
            "These read summaries, not single moments. Forget everything removes them, and they go as those summaries age out.",
        );
        expect(about.querySelector("[data-given-foot]")).toBeNull();
        expect(about.querySelector("h3")).toHaveTextContent("3");
    });

    it("drops the Forget line under Answers when one answer names nothing it read", async () => {
        (mocks.view as { answers: unknown[] }).answers.push({ at: at(17), sources: [{ kind: "chat", count: 1 }], removable: false });
        render(<AssistantGivenSheet open onOpenChange={() => {}} assistant="Claude" />);
        await screen.findByText("Answers in Chat");
        const answers = [...document.querySelectorAll("[data-given-group]")][4] as HTMLElement;
        expect(answers.querySelector("[data-given-foot]")).toBeNull();
        expect(answers.querySelector("[data-given-by-hour]")).toBeNull();
    });

    it("opens a brief and an answer where they live", async () => {
        const onOpenChange = vi.fn();
        render(<AssistantGivenSheet open onOpenChange={onOpenChange} assistant="Claude" />);
        const opens = await screen.findAllByText("Open ›");
        fireEvent.click(opens[0]);
        expect(mocks.emit).toHaveBeenLastCalledWith("notification:clicked", "memory:");
        fireEvent.click(opens[1]);
        expect(mocks.emit).toHaveBeenLastCalledWith("notification:clicked", "today:brief:brief-1");
        fireEvent.click(opens[2]);
        expect(mocks.emit).toHaveBeenLastCalledWith("notification:clicked", "main-chat-item:item-1");
        expect(onOpenChange).toHaveBeenCalledWith(false);
    });

    it("stops the Answers group from growing while Chat is off, and drops it once empty (scope cut A5)", async () => {
        (globalThis as Record<string, unknown>).__isoformFeatureSwitchesForTest = { automations: true, playbooks: true, proactiveVoice: true, calendarHolds: true, chat: false };
        const view = render(<AssistantGivenSheet open onOpenChange={() => {}} assistant="Claude" />);
        // What was sent stays listed until it ages out, with no link into Chat.
        expect(await screen.findByText("Answers in Chat")).toBeInTheDocument();
        expect(screen.getByText(/What did Sam ask for/)).toBeInTheDocument();
        expect(screen.getAllByText("Open ›")).toHaveLength(2);
        view.unmount();
        (mocks.view as { answers: unknown[] }).answers = [];
        render(<AssistantGivenSheet open onOpenChange={() => {}} assistant="Claude" />);
        expect(await screen.findByText("Briefs")).toBeInTheDocument();
        expect(screen.queryByText("Answers in Chat")).toBeNull();
    });

    it("shows a failed read with Try again, never an empty list", async () => {
        mocks.fail = true;
        render(<AssistantGivenSheet open onOpenChange={() => {}} assistant="Claude" />);
        expect(await screen.findByRole("alert")).toHaveTextContent("Couldn't read what Claude was given.");
        expect(document.querySelector("[data-given-group]")).toBeNull();
        mocks.fail = false;
        fireEvent.click(screen.getByText(en.settingRead.retry));
        expect(await screen.findByText("12 times")).toBeInTheDocument();
    });

    it("counts You's tabs in lines and topics in names (design v1.5.0 T19)", () => {
        const t = i18n.getFixedT("en", "settings");
        expect(t("given.counts.memory", { count: 3 })).toBe("About you (3 lines)");
        expect(t("given.counts.knowledge", { count: 2 })).toBe("How you do things (2 lines)");
        expect(t("given.counts.topic_names", { count: 3 })).toBe("Your topics (3 names)");
        expect(t("given.counts.topic_names", { count: 1 })).toBe("Your topics (1 name)");
    });

    it("has every given string in both languages", () => {
        const keys = (o: Record<string, unknown>, p = ""): string[] =>
            Object.entries(o).flatMap(([k, v]) => (v && typeof v === "object" ? keys(v as Record<string, unknown>, `${p}${k}.`) : [`${p}${k}`]));
        expect(keys(zh.given as Record<string, unknown>).sort()).toEqual(keys(en.given as Record<string, unknown>).sort());
        expect(keys(zh.see as Record<string, unknown>).sort()).toEqual(keys(en.see as Record<string, unknown>).sort());
    });
});
