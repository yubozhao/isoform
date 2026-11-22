import i18n from "@/i18n";
import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { AssistantHistoryView, CalendarFirstDay, HistoryProject, WatchedToday } from "@/lib/wails-shim-app";
import type { FirstDay } from "@/hooks/useFirstDay";
import { firstDayStarters, meetingStarter, projectWithProblem, type StarterText } from "@/lib/first-day-starters";
import { GettingToKnowYou, firstDayLine, watchedDuration, type GettingToKnowYouProps } from "./today/TodayFirstDay";
import { CalendarPeopleSection, nextMeetingLine } from "./people-first-day";
import { FirstDayChatStarters } from "./main-chat/first-day-starters";

const mocks = vi.hoisted(() => ({ firstDay: null as unknown as FirstDay, permission: vi.fn() }));

vi.mock("./today/TodayPermissionControl", () => ({ TodayPermissionControl: ({ permission }: { permission: string }) => <button onClick={() => mocks.permission(permission)}>Turn on</button> }));
beforeEach(() => { mocks.permission.mockClear(); });

vi.mock("@/hooks/useFirstDay", async (importOriginal) => ({
    ...(await importOriginal<typeof import("@/hooks/useFirstDay")>()),
    useFirstDay: () => mocks.firstDay,
}));

// Tuesday 2026-10-27, 1:00 PM local.
const NOW = new Date(2026, 9, 27, 13, 0);
const at = (h: number, m = 0, day = 27) => new Date(2026, 9, day, h, m).getTime();

function project(over: Partial<HistoryProject> = {}): HistoryProject {
    return {
        id: "p1", name: "isoform", path: "/src/isoform", assistants: ["claude"], sessionsThisWeek: 4, sessions14d: 9,
        lastAt: at(11, 30), status: "", openProblem: "", openProblemAt: 0, openQuestion: "", openChip: "", ...over,
    };
}

function history(over: Partial<AssistantHistoryView> = {}): AssistantHistoryView {
    return {
        enabled: true, state: "ready", assistants: ["claude"], projects: [project()], beliefCount: 3, knowHowCount: 2,
        scannedAt: at(12), pass2Done: true, ...over,
    };
}

function calendar(over: Partial<CalendarFirstDay> = {}): CalendarFirstDay {
    return {
        connected: true, paused: false, readsOff: false, meetingsLeftToday: 2, peopleLeftToday: 3, peopleThisWeek: 5, people: [],
        nextMeeting: { title: "Design sync", start: at(14, 30), attendeeNames: ["Maya Chen"] },
        freeRanges: [{ start: at(15), end: at(16) }, { start: at(16, 30), end: at(18, 30) }, { start: at(18, 30), end: at(18, 50) }],
        ...over,
    };
}

const text: StarterText = {
    t: (key, options) => `${key}${options ? ` ${JSON.stringify(options)}` : ""}`,
    fromCalendar: "From your calendar",
    clock: (ms) => new Date(ms).toTimeString().slice(0, 5),
    language: "en",
};

describe("first-day starters", () => {
    it("lists the next meeting, the open problem and the free hours, in that order", () => {
        const withProblem = history({ projects: [project({ openProblem: "tests fail", openQuestion: "Why are the onboarding tests failing?", openProblemAt: at(10) })] });
        const starters = firstDayStarters(calendar(), withProblem, NOW, text);
        expect(starters.map((s) => s.id)).toEqual(["meeting", "problem", "plan"]);
        expect(starters[0].title).toContain("firstDay.prepMeeting");
        expect(starters[0].title).toContain("\"name\":\"Maya\"");
        expect(starters[1].title).toBe("Why are the onboarding tests failing?");
        expect(starters[2].title).toBe("firstDay.planAfternoon");
        // The 20-minute gap is not an hour free.
        expect(starters[2].source).not.toContain("18:50");
    });

    it("has fewer starters with fewer sources, and none without any", () => {
        expect(firstDayStarters(calendar({ freeRanges: [] }), history(), NOW, text).map((s) => s.id)).toEqual(["meeting"]);
        expect(firstDayStarters(null, history(), NOW, text)).toEqual([]);
        expect(firstDayStarters(calendar({ connected: false }), null, NOW, text)).toEqual([]);
    });

    it("names the meeting by its title when nobody else is on it", () => {
        expect(meetingStarter(calendar({ nextMeeting: { title: "Focus", start: at(14), attendeeNames: [] } }), text)).toContain("firstDay.prepMeetingTitle");
    });

    it("takes an open problem only once the history is read and the question is plain", () => {
        const plain = project({ openProblem: "x", openQuestion: "Why?" });
        expect(projectWithProblem(history({ state: "reading", projects: [plain] }))).toBeNull();
        expect(projectWithProblem(history({ projects: [project({ openProblem: "x" })] }))).toBeNull();
        expect(projectWithProblem(history({ projects: [plain] }))?.id).toBe("p1");
    });
});

describe("first-day lines", () => {
    const tr = (key: string, options?: Record<string, unknown>) => `${key}${options ? ` ${JSON.stringify(options)}` : ""}`;

    it("says when the next brief comes, today or tomorrow", () => {
        expect(firstDayLine(0, new Date(at(18)).toISOString(), NOW, "en", tr)).toContain("firstDay.line ");
        expect(firstDayLine(0, new Date(at(7, 0, 28)).toISOString(), NOW, "en", tr)).toContain("firstDay.lineTomorrow");
        expect(firstDayLine(1, new Date(at(18)).toISOString(), NOW, "en", tr)).toContain("firstDay.lineDay2 ");
        expect(firstDayLine(0, undefined, NOW, "en", tr)).toBeNull();
    });

    it("writes watched time in minutes and hours", () => {
        expect(watchedDuration(5, tr)).toBe("firstDay.know.minutes {\"count\":5}");
        expect(watchedDuration(120, tr)).toBe("firstDay.know.hours {\"count\":2}");
        expect(watchedDuration(85, tr)).toContain("firstDay.know.hoursMinutes");
    });
});

function know(over: Partial<GettingToKnowYouProps> = {}) {
    const props: GettingToKnowYouProps = {
        day: 0, history: history(), calendar: calendar(),
        watched: { startedAt: at(12), minutes: 25, screenRecording: true, paused: false } satisfies WatchedToday,
        topicNames: [], evening: { at: new Date(at(18)).toISOString(), ready: false }, placement: "side",
        onOpenPeople: vi.fn(), onOpenAboutYou: vi.fn(),
        onOpenTopics: vi.fn(), onOpenBrief: vi.fn(), onImport: vi.fn(), ...over,
    };
    render(<GettingToKnowYou {...props} />);
    const card = screen.getByTestId("getting-to-know-you");
    const rows = Array.from(card.querySelectorAll("[data-know-row]")).map((row) => row.getAttribute("data-know-row"));
    return { props, card, rows };
}

describe("Getting to know you", () => {
    it("shows a row for each source that exists, with real counts", () => {
        const { card, rows } = know();
        expect(rows).toEqual(["calendar", "history", "watching", "topics", "evening", "portrait"]);
        expect(card.querySelector("[data-know-row=history]")).toHaveTextContent("Claude Code history");
        expect(card).toHaveTextContent("Day 1");
        expect(card).toHaveTextContent("Screenshots and audio never leave this computer.");
    });

    it("names the whole week's people and opens Who you know in both languages", async () => {
        const { card, props } = know();
        const row = card.querySelector("[data-know-row=calendar]") as HTMLElement;
        expect(row).toHaveTextContent("Your calendar");
        expect(row).toHaveTextContent("5 people this week");
        fireEvent.click(within(row).getByRole("button"));
        expect(props.onOpenPeople).toHaveBeenCalledOnce();
        try {
            await act(async () => { await i18n.changeLanguage("zh-CN"); });
            expect(row).toHaveTextContent("你的日历");
            expect(row).toHaveTextContent("本周 5 人");
        } finally {
            await act(async () => { await i18n.changeLanguage("en"); });
        }
    });

    it("leaves out the history and calendar when there are none", () => {
        const { rows } = know({ history: null, calendar: null });
        expect(rows).toEqual(["watching", "topics", "evening", "portrait"]);
    });

    it("offers to turn the calendar on when it is off", () => {
        const { card } = know({ calendar: calendar({ connected: false }) });
        fireEvent.click(within(card).getByRole("button", { name: "Turn on" }));
        expect(mocks.permission).toHaveBeenCalledWith("calendars");
    });

    it("offers the screen permission control when observing cannot see", () => {
        const { card } = know({ watched: { startedAt: 0, minutes: 0, screenRecording: false, paused: false } });
        const row = card.querySelector("[data-know-row=watching]") as HTMLElement;
        fireEvent.click(within(row).getByRole("button", { name: "Turn on" }));
        expect(mocks.permission).toHaveBeenCalledWith("screen_recording");
    });

    it("keeps unavailable topics and evening sections as plain rows with the time", () => {
        const { card } = know({ topicNames: ["Project"], evening: { ready: true, at: new Date(at(18)).toISOString() }, onOpenTopics: undefined, onOpenBrief: undefined });
        const topics = card.querySelector("[data-know-row=topics]") as HTMLElement;
        const evening = card.querySelector("[data-know-row=evening]") as HTMLElement;
        expect(within(topics).queryByRole("button")).toBeNull();
        expect(within(evening).queryByRole("button")).toBeNull();
        expect(evening).toHaveTextContent("6:00 PM");
    });

    it("distinguishes paused observation and disabled calendar reads from a disconnected calendar", () => {
        const { card } = know({ calendar: calendar({ connected: true, paused: true }) });
        const row = card.querySelector("[data-know-row=calendar]") as HTMLElement;
        expect(row).toHaveTextContent("Paused");
        expect(within(row).queryByRole("button", { name: "Turn on" })).toBeNull();
        expect(mocks.permission).not.toHaveBeenCalled();
    });

    it("offers calendar settings when calendar reads are disabled", () => {
        const { card } = know({ calendar: calendar({ connected: true, readsOff: true }) });
        const row = card.querySelector("[data-know-row=calendar]") as HTMLElement;
        expect(row).toHaveTextContent("Off");
        fireEvent.click(within(row).getByRole("button", { name: "Turn on" }));
        expect(mocks.permission).toHaveBeenCalledWith("calendars");
    });

    it("drops the evening row after day 0 and hides the import line once imported", () => {
        const { rows, card } = know({ day: 1, imported: true });
        expect(rows).not.toContain("evening");
        expect(within(card).queryByTestId("know-import")).toBeNull();
    });

    it("says where the evening brief is when it is ready", () => {
        const { card, props } = know({ evening: { at: null, ready: true }, placement: "below" });
        const row = card.querySelector("[data-know-row=evening]")!;
        expect(row).toHaveTextContent("Ready above");
        fireEvent.click(within(row as HTMLElement).getByRole("button"));
        expect(props.onOpenBrief).toHaveBeenCalled();
    });
});

describe("Who you know from the calendar", () => {
    const person = { key: "maya", name: "Maya Chen", email: "maya@example.com", principalId: "pr-1", nextMeetingTitle: "1:1", nextMeetingAt: at(14, 30), recurring: true, meetingsThisWeek: 2 };

    it("lists each person with the next meeting, and opens a known person", () => {
        const onSelect = vi.fn();
        render(<CalendarPeopleSection people={[person, { ...person, key: "sam", name: "Sam", principalId: "" }]} known={(id) => id === "pr-1"} onSelect={onSelect} now={NOW} />);
        const section = screen.getByTestId("people-calendar");
        expect(section).toHaveTextContent("From this week's calendar");
        expect(section).toHaveTextContent("1:1 today at");
        expect(section).toHaveTextContent("recurring");
        fireEvent.click(within(section).getByRole("button", { name: /Maya Chen/ }));
        expect(onSelect).toHaveBeenCalledWith("pr-1");
        expect(within(section).queryByRole("button", { name: /Sam/ })).toBeNull();
    });

    it("names the day for a meeting that isn't today", () => {
        const line = nextMeetingLine({ ...person, recurring: false, nextMeetingAt: at(10, 0, 29) }, NOW, "en", (key, o) => `${key} ${JSON.stringify(o)}`, () => "10:00");
        expect(line).toContain("firstDay.nextOn");
        expect(line).toContain("Thursday");
    });
});

describe("First-day chat starters", () => {
    beforeEach(() => {
        mocks.firstDay = { day: 0, firstDays: true, history: history(), calendar: calendar() };
    });

    it("sends a starter at once and takes it off the row (Chat K4)", () => {
        const onPrepare = vi.fn();
        render(<FirstDayChatStarters items={[]} onPrepare={onPrepare} />);
        expect(screen.getByText("A few things I can do right now. Pick one and I'll start.")).toBeInTheDocument();
        const meeting = document.querySelector("[data-starter=meeting]") as HTMLElement;
        fireEvent.click(meeting);
        expect(onPrepare).toHaveBeenCalledWith(expect.stringContaining("Maya"));
        expect(document.querySelector("[data-starter=meeting]")).toBeNull();
        expect(screen.getByTestId("first-day-local-line")).toHaveTextContent("this computer");
    });

    it("doesn't show without a starter, after day 1, or once the person has sent something", () => {
        mocks.firstDay = { day: 0, firstDays: true, history: null, calendar: null };
        const { rerender } = render(<FirstDayChatStarters items={[]} onPrepare={vi.fn()} />);
        expect(screen.queryByTestId("first-day-starters")).toBeNull();

        mocks.firstDay = { day: 2, firstDays: false, history: null, calendar: null };
        rerender(<FirstDayChatStarters items={[]} onPrepare={vi.fn()} />);
        expect(screen.queryByTestId("first-day-starters")).toBeNull();

        mocks.firstDay = { day: 0, firstDays: true, history: history(), calendar: calendar() };
        rerender(<FirstDayChatStarters items={[{ kind: "user" } as never]} onPrepare={vi.fn()} />);
        expect(screen.queryByTestId("first-day-starters")).toBeNull();
    });
});
