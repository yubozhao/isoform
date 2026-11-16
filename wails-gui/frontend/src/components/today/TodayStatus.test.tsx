import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import i18n from "@/i18n";
import en from "@/i18n/locales/en/today.json";
import zh from "@/i18n/locales/zh-CN/today.json";
import { TodayStatus, describeBriefTime } from "./TodayStatus";

const telemetry = vi.hoisted(() => ({ record: vi.fn(async () => undefined), finished: null as null | ((token: unknown, success: unknown) => void) }));
vi.mock("@/lib/wails-shim-app", () => ({ RecordTodayAction: telemetry.record }));
vi.mock("../../../wailsjs/runtime/runtime", () => ({ EventsOn: vi.fn((name: string, fn: (token: unknown, success: unknown) => void) => { if (name === "today:brief-request-finished") telemetry.finished = fn; return () => { telemetry.finished = null; }; }) }));
beforeEach(() => { telemetry.record.mockClear(); });

const now = new Date(2026, 9, 14, 12, 0, 0);
const at = (dayOffset: number, hour: number) =>
    new Date(2026, 9, 14 + dayOffset, hour, 2, 0).toISOString();

describe("TodayStatus", () => {
    it("shows the retry sentence while the slot is still trying (R55)", () => {
        const pendingAt = new Date(Date.now() - 6 * 3600_000).toISOString();
        const retryAt = new Date(Date.now() + 3600_000).toISOString();
        render(<TodayStatus brief={{ enabled: true, lastRunAt: "", lastSlot: "", nextRunAt: "", nextSlot: "", missedRuns: 0, failingSlot: "", failedAttempts: 0, latestBriefId: "", currentBriefId: "", pendingSlot: "morning", pendingAt, retryAt, pendingAttempts: 9, pendingCause: "timeout" }} date="Tuesday, November 3" />);
        expect(screen.getByTestId("today-status")).toHaveTextContent("Tried 9 times. It timed out. Isoform tries again at");
        expect(screen.queryByText(/didn't come/)).toBeNull();
    });

    it.each([["en", "Thursday, November 5 · next brief at 7:00 AM"], ["zh-CN", "11月5日星期四 · 下次简报在 7:00"]])("keeps the date and next brief while retrying in %s", async (language, expected) => {
        await i18n.changeLanguage(language);
        const now = new Date(2026, 10, 5, 6, 0);
        const brief = { enabled: true, lastRunAt: "", lastSlot: "", nextRunAt: new Date(2026, 10, 5, 7).toISOString(), nextSlot: "morning", missedRuns: 0, failingSlot: "", failedAttempts: 0, latestBriefId: "", currentBriefId: "", pendingSlot: "morning", pendingAt: new Date(2026, 10, 5, 5).toISOString(), retryAt: new Date(2026, 10, 5, 6, 15).toISOString(), pendingAttempts: 3, pendingCause: "signed_out" };
        const date = language === "en" ? "Thursday, November 5" : "11月5日星期四";
        const { rerender, unmount } = render(<TodayStatus brief={brief} now={now} date={date} />);
        expect(screen.getByTestId("today-status-date")).toHaveTextContent(expected);
        rerender(<TodayStatus brief={brief} now={now} date={date} building />);
        expect(screen.getByTestId("today-status-date")).toHaveTextContent(expected);
        unmount();
        await i18n.changeLanguage("en");
    });

    it("does not say the brief missed its time while a try is running", () => {
        render(<TodayStatus date="DATE" building brief={{ enabled: true, lastRunAt: "", lastSlot: "", nextRunAt: at(0, 18), nextSlot: "evening", missedRuns: 0, failingSlot: "morning", failedAttempts: 3, latestBriefId: "", currentBriefId: "", pendingSlot: "morning", pendingAt: at(0, 7), pendingAttempts: 3 }} />);
        expect(screen.getByTestId("today-status")).toHaveTextContent("DATE");
        expect(screen.getByTestId("today-status")).not.toHaveTextContent(/didn't come/);
        expect(screen.getByTestId("today-status")).toHaveTextContent("next brief at 6:02 PM");
    });

    it.each(["en", "zh-CN"])("shows the running state only during a build in %s", async (language) => {
        await i18n.changeLanguage(language);
        const clock = new Date(2026, 10, 4, 8, 30);
        const brief = { enabled: true, lastRunAt: "", lastSlot: "", nextRunAt: "", nextSlot: "", missedRuns: 0, failingSlot: "", failedAttempts: 0, latestBriefId: "", currentBriefId: "", pendingSlot: "morning", pendingAt: new Date(2026, 10, 4, 7).toISOString(), pendingAttempts: 6, pendingCause: "timeout", retryAt: new Date(2026, 10, 4, 8, 45).toISOString() };
        const { rerender } = render(<TodayStatus brief={brief} now={clock} building />);
        expect(screen.getByTestId("today-status")).toHaveTextContent(language === "en" ? "Making your morning brief now." : "正在生成你的早间简报。");
        rerender(<TodayStatus brief={brief} now={clock} />);
        expect(screen.getByTestId("today-status")).toHaveTextContent(language === "en" ? "Tried 6 times. It timed out. Isoform tries again at 8:45 AM." : "试了 6 次。超时了。Isoform 会在8:45再试。");
        rerender(<TodayStatus brief={{ ...brief, pendingCause: "signed_out" }} now={clock} />);
        expect(screen.getByTestId("today-status")).toHaveTextContent(language === "en" ? "Tried 6 times. Your AI is signed out. Isoform tries again at 8:45 AM." : "已尝试 6 次。你的 AI 已退出登录。Isoform 会在 8:45 再试一次。");
        rerender(<TodayStatus brief={{ ...brief, retryAt: "" }} now={clock} />);
        expect(screen.getByTestId("today-status")).not.toHaveTextContent(/didn't come|没有在|Making|正在生成/);
        await i18n.changeLanguage("en");

    });

    it.each(["en", "zh-CN"])("says what comes next once the slot has given way (today-review v1.1.22 R51) in %s", async (language) => {
        await i18n.changeLanguage(language);
        const now = new Date(2026, 10, 4, 12, 0);
        const nextRunAt = new Date(2026, 10, 4, 18, 0).toISOString();
        const base = { enabled: true, lastRunAt: "", lastSlot: "", nextRunAt, nextSlot: "evening", missedRuns: 0, failingSlot: "", failedAttempts: 0, latestBriefId: "", currentBriefId: "", pendingSlot: "morning", pendingAt: new Date(2026, 10, 4, 7, 0).toISOString(), pendingAttempts: 5, pendingCause: "timeout" };
        const { rerender } = render(<TodayStatus brief={{ ...base, retryAt: "" }} now={now} />);
        expect(screen.getByTestId("today-status-next")).toHaveTextContent(i18n.t("today:status.nextEveningAt", { time: language === "en" ? "6:00 PM" : "18:00" }));
        rerender(<TodayStatus brief={{ ...base, nextRunAt: new Date(2026, 10, 5, 7, 0).toISOString(), nextSlot: "morning", retryAt: "" }} now={new Date(2026, 10, 4, 23, 40)} />);
        expect(screen.getByTestId("today-status-next")).toHaveTextContent(i18n.t("today:status.nextMorningTomorrowAt", { time: "7:00" + (language === "en" ? " AM" : "") }));
        rerender(<TodayStatus brief={{ ...base, retryAt: new Date(2026, 10, 4, 12, 8).toISOString() }} now={now} />);
        expect(screen.queryByTestId("today-status-next")).toBeNull();
        await i18n.changeLanguage("en");
    });

    it("has no Make it now while the brief is missed: the card has the one button (v1.1.5 #2)", async () => {
        await i18n.changeLanguage("en");
        render(<TodayStatus brief={{ enabled: true, lastRunAt: "", lastSlot: "", nextRunAt: "", nextSlot: "", missedRuns: 1, failingSlot: "", failedAttempts: 0, latestBriefId: "", currentBriefId: "", pendingSlot: "morning", pendingAt: at(0, 7) }} now={now} onMissedWhy={vi.fn()} />);
        expect(screen.getByTestId("today-status")).toHaveTextContent("Your morning brief didn't come at 7:02 AM. · Why");
        expect(screen.queryByRole("button", { name: "Make it now" })).not.toBeInTheDocument();
    });
    it("opens the missed brief's own detail from Why (K3)", async () => {
        await i18n.changeLanguage("en");
        const why = vi.fn();
        render(<TodayStatus brief={{ enabled: true, lastRunAt: "", lastSlot: "", nextRunAt: "", nextSlot: "", missedRuns: 1, failingSlot: "", failedAttempts: 0, latestBriefId: "", currentBriefId: "", pendingSlot: "evening", pendingAt: at(0, 18) }} now={new Date(2026, 9, 15, 0, 30)} onMissedWhy={why} />);
        expect(screen.getByTestId("today-status")).toHaveTextContent("Your evening brief didn't come at 6:02 PM. · Why");
        fireEvent.click(screen.getByRole("button", { name: "Why" }));
        expect(why).toHaveBeenCalledWith("evening");
        await waitFor(() => expect(telemetry.record).toHaveBeenCalledWith("today", "T86", "open", "opened", expect.any(Number)));
    });
    it("shows the date and next time without historical missed counts", async () => {
        await i18n.changeLanguage("en");
        render(
            <TodayStatus
                brief={{ latestBriefId: "", currentBriefId: "",
                    enabled: true,
                    lastRunAt: at(0, 7),
                    lastSlot: "morning",
                    nextRunAt: at(0, 18),
                    nextSlot: "evening",
                    missedRuns: 2,
                    failingSlot: "",
                    failedAttempts: 0,
                }}
                now={now} date="Wednesday, October 14"
            />,
        );
        const status = screen.getByTestId("today-status");
        expect(status).toHaveTextContent("Wednesday, October 14 · next brief at 6:02 PM");
        expect(status).not.toHaveTextContent(/Last brief|missed/);
    });

    it("says the first brief is still to come, and hides the next run when the schedule is off", async () => {
        await i18n.changeLanguage("en");
        const { rerender } = render(
            <TodayStatus
                brief={{ latestBriefId: "", currentBriefId: "", enabled: true, lastRunAt: "", lastSlot: "", nextRunAt: at(1, 7), nextSlot: "morning", missedRuns: 0, failingSlot: "", failedAttempts: 0 }}
                now={now}
            />,
        );
        expect(screen.getByTestId("today-status")).toHaveTextContent("next brief tomorrow at 7:02 AM");
        rerender(
            <TodayStatus
                brief={{ latestBriefId: "", currentBriefId: "", enabled: false, lastRunAt: "", lastSlot: "", nextRunAt: at(1, 7), nextSlot: "morning", missedRuns: 0, failingSlot: "", failedAttempts: 0 }}
                now={now}
            />,
        );
        expect(screen.queryByTestId("today-status")).not.toBeInTheDocument();
    });

    it("describes times relative to today, in the one clock", () => {
        const t = (key: string, options?: Record<string, unknown>) => `${key}:${String(options?.time ?? "")}`;
        const clock = (key: string, options?: Record<string, unknown>) => String(i18n.t(key as never, { ...options, ns: "today", lng: "en" }));
        expect(describeBriefTime(at(0, 7), now, t)).toMatch(/^status\.todayAt:/);
        expect(describeBriefTime(at(-1, 18), now, t)).toMatch(/^status\.yesterdayAt:/);
        expect(describeBriefTime(at(1, 7), now, t)).toMatch(/^status\.tomorrowAt:/);
        expect(describeBriefTime(at(-3, 7), now, clock)).toBe("Sun, Oct 11 7:02 AM");
        expect(describeBriefTime("", now, t)).toBeNull();
        expect(describeBriefTime("not a time", now, t)).toBeNull();
    });

    it("no longer says how often Isoform spoke up this week; that line lives in Settings", async () => {
        await i18n.changeLanguage("en");
        render(<TodayStatus brief={{ latestBriefId: "", currentBriefId: "", enabled: true, lastRunAt: at(0, 7), lastSlot: "morning", nextRunAt: "", nextSlot: "", missedRuns: 0, failingSlot: "", failedAttempts: 0 }} now={now} />);
        expect(screen.queryByTestId("today-status")).not.toBeInTheDocument();
        expect((en.status as Record<string, string>).spokeUp_other).toBeUndefined();
    });

    it("adds one muted quiet line per quieted notice kind, only when there is one", async () => {
        await i18n.changeLanguage("en");
        const { rerender } = render(<TodayStatus brief={null} now={now} quiet={[]} />);
        expect(screen.queryByTestId("today-status")).not.toBeInTheDocument();
        rerender(
            <TodayStatus
                brief={null}
                now={now}
                capture={{ text: "Today Isoform saw 466 screens." }}
                quiet={[{ id: "meeting_notes", text: "Meeting notes ready: 3 today, shown here instead of notices." }, { id: "blank", text: "  " }]}
            />,
        );
        const lines = screen.getAllByTestId("today-quiet-line");
        expect(lines.map((line) => line.textContent)).toEqual(["Meeting notes ready: 3 today, shown here instead of notices."]);
        // Under every status line, the capture line included.
        expect(screen.getByTestId("today-capture").compareDocumentPosition(lines[0]) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    });

    it("keeps the missed-briefs line emphasised and plays nothing in the header", async () => {
        await i18n.changeLanguage("en");
        render(
            <TodayStatus
                brief={{
                    enabled: true, lastRunAt: at(0, 7), lastSlot: "morning", nextRunAt: "", nextSlot: "", missedRuns: 2,
                    failingSlot: "", failedAttempts: 0, latestBriefId: "2026-10-14-morning", currentBriefId: "2026-10-14-morning",
                    audio: { mime: "audio/mp4", bytes: 1200, durationSeconds: 70 },
                }}
                now={now}
            />,
        );
        expect(screen.queryByText(/briefs were missed/)).not.toBeInTheDocument();
        // The spoken brief plays from its own card (TodayBriefSection).
        expect(screen.queryByTestId("brief-audio-toggle")).not.toBeInTheDocument();
    });

    it("has the same status keys in English and Chinese", () => {
        expect(Object.keys(zh.status).sort()).toEqual(Object.keys(en.status).sort());
    });
});
it.each(['en', 'zh-CN'])('shows the current missed slot in %s without historical counts', async (language) => {
    await i18n.changeLanguage(language);
    render(<TodayStatus date="DATE" brief={{ enabled: true, lastRunAt: at(0, 7), lastSlot: 'morning', nextRunAt: at(0, 18), nextSlot: 'evening', missedRuns: 4, failingSlot: '', failedAttempts: 0, latestBriefId: '', currentBriefId: '', pendingSlot: 'morning', pendingAt: at(0, 7) }} now={now} />);
    const status = screen.getByTestId('today-status');
    expect(status).toHaveTextContent(language === 'en' ? "Your morning brief didn't come at" : '早间简报没有在');
    expect(status).not.toHaveTextContent(/DATE|Last brief|7 days/);
    expect(screen.queryByRole('button', { name: language === 'en' ? 'Make it now' : '现在生成' })).not.toBeInTheDocument();
});

describe("TodayStatus assistant sign-out (R75)", () => {
    const quietBrief = { enabled: false, lastRunAt: "", lastSlot: "", nextRunAt: "", nextSlot: "", missedRuns: 0, failingSlot: "", failedAttempts: 0, latestBriefId: "", currentBriefId: "", pendingSlot: "", pendingAt: "", retryAt: "", pendingAttempts: 0, pendingCause: "" };

    it("says one line under the date when the chosen assistant is signed out", async () => {
        await i18n.changeLanguage("en");
        render(<TodayStatus brief={quietBrief} date="Friday, November 7" signOut={{ assistant: "claude-code", signedOut: true }} />);
        const line = screen.getByTestId("today-signed-out");
        expect(line).toHaveTextContent("Claude Code is signed out, so briefs are shorter and Chat can't answer.");
        expect(screen.getByRole("button", { name: "Sign in" })).toBeInTheDocument();
    });

    it("names the assistant the person chose, in zh-CN", async () => {
        await i18n.changeLanguage("zh-CN");
        render(<TodayStatus brief={quietBrief} date="11月7日星期五" signOut={{ assistant: "codex", signedOut: true }} />);
        expect(screen.getByTestId("today-signed-out")).toHaveTextContent("Codex 已退出登录，所以简报会更简短，聊天也无法回答。");
        await i18n.changeLanguage("en");
    });

    it("shows nothing when no sign-out is confirmed", () => {
        render(<TodayStatus brief={quietBrief} date="Friday, November 7" signOut={null} />);
        expect(screen.queryByTestId("today-signed-out")).toBeNull();
    });

    it("does not repeat the line when the pending brief already says it is signed out", async () => {
        await i18n.changeLanguage("en");
        const pendingAt = new Date(2026, 10, 7, 5).toISOString();
        const retryAt = new Date(2026, 10, 7, 23).toISOString();
        render(<TodayStatus brief={{ ...quietBrief, enabled: true, pendingSlot: "morning", pendingAt, retryAt, pendingAttempts: 2, pendingCause: "signed_out", pendingAssistant: "claude-code" }} date="Friday, November 7" signOut={{ assistant: "claude-code", signedOut: true }} />);
        expect(screen.queryByTestId("today-signed-out")).toBeNull();
    });
});
