import { act, fireEvent, render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import i18n from "@/i18n";
import zh from "@/i18n/locales/zh-CN/app.json";
import type { DoingNow } from "@/lib/wails-shim-app";
import { doingNowKey } from "@/hooks/useDoingNow";
import { ObservingStatus } from "./observing-status";
import { resetPageQueries } from "@/lib/page-queries";

const mocks = vi.hoisted(() => ({
    handlers: new Map<string, Set<(...args: unknown[]) => void>>(),
    getPermissions: vi.fn(),
    getListening: vi.fn(),
    getDoingNow: vi.fn(),
    pause: vi.fn(),
    resume: vi.fn(),
    counts: vi.fn(),
}));

vi.mock("@/lib/wails-shim-app", () => ({
    GetPlatformPermissions: mocks.getPermissions,
    GetBackgroundListening: mocks.getListening,
    GetDoingNow: mocks.getDoingNow,
    PauseObservationFor: mocks.pause,
    ResumeObservation: mocks.resume,
    GetCaptureCounts: mocks.counts,
}));

vi.mock("../../wailsjs/runtime/runtime", () => ({
    EventsOn: (name: string, handler: (...args: unknown[]) => void) => {
        const set = mocks.handlers.get(name) ?? new Set();
        set.add(handler);
        mocks.handlers.set(name, set);
        return () => set.delete(handler);
    },
}));

function emit(name: string, ...args: unknown[]) {
    act(() => {
        for (const handler of mocks.handlers.get(name) ?? []) handler(...args);
    });
}

async function openTooltip() {
    const trigger = await screen.findByRole("button", { name: /^(Background observing|后台观察)/ });
    fireEvent.focus(trigger);
    return screen.findByTestId("observing-doing-now");
}

beforeEach(async () => {
        resetPageQueries();
        vi.clearAllMocks();
        mocks.handlers.clear();
        await i18n.changeLanguage("en");
        mocks.getPermissions.mockResolvedValue({ items: [] });
        mocks.getListening.mockResolvedValue(true);
        mocks.getDoingNow.mockResolvedValue({ kind: "listening" });
        mocks.pause.mockResolvedValue(undefined);
        mocks.resume.mockResolvedValue(undefined);
        mocks.counts.mockResolvedValue({ sources: [{ source: "screens", today: 785 }, { source: "audioMinutes", today: 114 }] });
});

describe("ObservingStatus doing-now line", () => {
    it("leads the tooltip with what Isoform is doing, and follows changes", async () => {
        render(<ObservingStatus />);
        const line = await openTooltip();
        expect(line).toHaveTextContent("Listening quietly");

        emit("doing-now:changed", { kind: "meeting", app: "Zoom" });
        expect(line).toHaveTextContent("Listening to your Zoom call");
        emit("doing-now:changed", { kind: "working", title: "Draft the Q2 deck" });
        expect(line).toHaveTextContent("Working on: Draft the Q2 deck");
        emit("doing-now:changed", { kind: "working" });
        expect(line).toHaveTextContent("Working on a task");
        emit("doing-now:changed", { kind: "paused" });
        expect(screen.getAllByText("Observing paused")).toHaveLength(2);
    });

    it("keeps a change that arrives before the first read answers", async () => {
        let answer: (view: DoingNow) => void = () => {};
        mocks.getDoingNow.mockReturnValue(new Promise<DoingNow>((resolve) => (answer = resolve)));
        render(<ObservingStatus />);
        const trigger = await screen.findByRole("button", { name: /^(Background observing|后台观察)/ });
        emit("doing-now:changed", { kind: "paused" });
        await act(async () => answer({ kind: "watching" }));
        fireEvent.focus(trigger);
        expect(await screen.findAllByText("Observing paused")).toHaveLength(2);
    });

    it("shows no line when it cannot be read", async () => {
        mocks.getDoingNow.mockRejectedValue(new Error("no app"));
        render(<ObservingStatus />);
        const trigger = await screen.findByRole("button", { name: /^(Background observing|后台观察)/ });
        fireEvent.focus(trigger);
        expect(await screen.findAllByText("Observing in background")).toHaveLength(2);
        expect(screen.queryByTestId("observing-doing-now")).not.toBeInTheDocument();
    });

    it("has words for every line in both languages", async () => {
        const views: DoingNow[] = [
            { kind: "paused" },
            { kind: "meeting", app: "Zoom" },
            { kind: "meeting" },
            { kind: "calendar" },
            { kind: "working", title: "X" },
            { kind: "working" },
            { kind: "watching" },
            { kind: "listening" },
            { kind: "idle" },
        ];
        const keys = new Set(views.map(doingNowKey));
        expect(keys.size).toBe(Object.keys(zh.observingStatus.doingNow).length);
        for (const key of keys) {
            expect(i18n.exists(`app:observingStatus.doingNow.${key}`, { lng: "en" })).toBe(true);
            expect(i18n.exists(`app:observingStatus.doingNow.${key}`, { lng: "zh-CN" })).toBe(true);
        }

        await i18n.changeLanguage("zh-CN");
        mocks.getDoingNow.mockResolvedValue({ kind: "meeting", app: "Zoom" });
        render(<ObservingStatus />);
        const trigger = await screen.findByRole("button", { name: /^后台观察：/ });
        fireEvent.focus(trigger);
        expect(await screen.findByTestId("observing-doing-now")).toHaveTextContent("正在听你的 Zoom 通话");
    });
});


describe("Observation controls", () => {
    it.each(["en", "zh-CN"])("shows the shared states, counts and pauses in %s", async (language) => {
        await i18n.changeLanguage(language);
        const t = i18n.getFixedT(language, "app");
        mocks.getPermissions.mockResolvedValue({ items: [] });
        mocks.getListening.mockResolvedValue(true);
        mocks.getDoingNow.mockResolvedValue({ kind: "watching" });
        mocks.counts.mockResolvedValue({ sources: [{ source: "screens", today: 785 }, { source: "audioMinutes", today: 114 }] });
        mocks.pause.mockResolvedValue(undefined);
        const onOpenSettings = vi.fn();
        render(<ObservingStatus onOpenSettings={onOpenSettings} />);
        fireEvent.focus(await screen.findByRole("button", { name: /^(Background observing|后台观察)/ }));
        expect(await screen.findByText(t("observingStatus.counts", { screens: 785, minutes: 114 }))).toBeInTheDocument();
        fireEvent.click(screen.getByText(t("observingStatus.pause15")));
        await act(async () => {});
        expect(mocks.pause).toHaveBeenLastCalledWith(15);
        fireEvent.click(screen.getByText(t("observingStatus.pause60")));
        await act(async () => {});
        expect(mocks.pause).toHaveBeenLastCalledWith(60);
        emit("doing-now:changed", { kind: "paused", pausedUntil: new Date(2026, 9, 22, 15, 15).getTime() });
        expect(screen.getByText(t("observingStatus.resume"))).toBeInTheDocument();
        expect(screen.queryByText(t("observingStatus.counts", { screens: 785, minutes: 114 }))).toBeNull();
        expect(screen.queryByText(/Watching quietly|Context awareness/)).toBeNull();
        fireEvent.click(screen.getByText(t("observingStatus.resume")));
        await act(async () => {});
        expect(mocks.resume).toHaveBeenCalled();
        fireEvent.click(screen.getByText(t("observingStatus.settings")));
        expect(onOpenSettings).toHaveBeenCalledOnce();
    });

    it("keeps Not observing reachable when no observation source is on", async () => {
        await i18n.changeLanguage("en");
        mocks.getPermissions.mockResolvedValue({ items: [] });
        mocks.getListening.mockResolvedValue(false);
        mocks.getDoingNow.mockResolvedValue({ kind: "idle" });
        render(<ObservingStatus collapsed />);
        expect(await screen.findByTitle("Not observing")).toBeInTheDocument();
    });
});


describe("Folded observing dot", () => {
    it.each(["en", "zh-CN"])("shows only the dot and names each full state in %s", async (language) => {
        await i18n.changeLanguage(language);
        mocks.getPermissions.mockResolvedValue({ items: [] });
        mocks.counts.mockResolvedValue({ sources: [] });
        const t = i18n.getFixedT(language, "app");
        for (const state of ["watching", "paused", "idle"] as const) {
            mocks.getListening.mockResolvedValue(state !== "idle");
            mocks.getDoingNow.mockResolvedValue({ kind: state, ...(state === "paused" ? { pausedUntil: new Date(2026, 9, 22, 15, 15).getTime() } : {}) });
            const { unmount } = render(<ObservingStatus collapsed />);
            const expected = state === "paused" ? t("observingStatus.pausedUntil", { time: language === "en" ? "3:15 PM" : "15:15" }) : t(state === "idle" ? "observingStatus.notObserving" : "observingStatus.label");
            const trigger = await screen.findByRole("button", { name: expected });
            expect(trigger).toHaveAttribute("title", expected);
            expect(trigger.textContent).toBe("");
            expect(trigger.parentElement).toHaveClass("px-3", "w-full");
            expect(trigger).toHaveClass("w-8", "p-0", "justify-center");
            expect(trigger.querySelector("span span")).toHaveClass("size-2");
            fireEvent.click(trigger);
            const layer = screen.getByText(expected).closest('[role="dialog"]')!;
            expect(layer).toHaveClass("w-[320px]");
            expect(layer).toHaveAttribute("data-side", "top");
            expect(layer).toHaveAttribute("data-align", "start");
            expect(trigger.parentElement!.contains(layer)).toBe(false);
            expect(trigger).toBeVisible();
            expect(document.body.style.pointerEvents).not.toBe("none");
            unmount();
            resetPageQueries();
        }
    });
});
