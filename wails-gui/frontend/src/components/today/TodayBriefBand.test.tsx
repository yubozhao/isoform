import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import i18n from "@/i18n";
import type { MobileBriefDocument, TodayBriefView } from "@/lib/wails-shim-app";
import { TodayBriefBand, TodayFirstBriefBand, firstSentences } from "./TodayBriefBand";

const mocks = vi.hoisted(() => ({ toggle: vi.fn() }));

vi.mock("@/lib/wails-shim-app", () => ({ GetMobileBriefAudio: vi.fn(), RecordTodayAction: vi.fn().mockResolvedValue(undefined) }));
vi.mock("@/lib/brief-audio", async (importOriginal) => ({
    ...(await importOriginal<typeof import("@/lib/brief-audio")>()),
    toggleBriefPlayback: mocks.toggle,
}));
vi.mock("@/lib/today-telemetry", () => ({ measureToday: (_id: string, _kind: string, _outcome: string, run: () => unknown) => run() }));

function brief(overrides: Partial<MobileBriefDocument> = {}): MobileBriefDocument {
    return {
        briefId: "2026-11-15-evening",
        slot: "evening",
        asOf: "2026-11-15T18:00:00-08:00",
        timezone: "America/Los_Angeles",
        narrativeStatus: "ok",
        narrative: [
            { text: "Daily test brief: the Halcyon review is the first thing today. Priya answered about the budget." },
            { text: "Lena moved the call to ten. The test plan is still open. Nothing else came in." },
        ],
        sections: { moved: [], doing: [], watching: [], meetings: [{ title: "Halcyon review", detail: "", at: "2026-11-16T09:00:00-08:00" }, { title: "Lena", detail: "", at: "2026-11-16T10:00:00-08:00" }], people: [], openLoops: [], learned: [], deliverables: [], schedules: [] },
        cards: [
            { id: "c1", kind: "item", mode: "send", label: "Reply to Priya", message: "Reply to Priya" },
            { id: "c2", kind: "action", mode: "send", label: "Plan next steps", message: "Plan" },
        ],
        audio: { status: "ok", bytes: 1200, mime: "audio/mp4", durationSeconds: 120 },
        ...overrides,
    } as unknown as MobileBriefDocument;
}

function view(overrides: Partial<TodayBriefView> = {}): TodayBriefView {
    return { brief: brief(), building: false, enabled: true, nextAt: "", nextSlot: "", empty: false, missing: [], ...overrides } as TodayBriefView;
}

function band(props: Partial<Parameters<typeof TodayBriefBand>[0]> = {}) {
    const onReadAll = vi.fn();
    const onMakeNow = vi.fn();
    const onOpenPastBriefs = vi.fn();
    const v = props.view ?? view();
    render(<TodayBriefBand brief={v.brief!} view={v} make="idle" onMakeNow={onMakeNow} readAll={false} onReadAll={onReadAll} onOpenPastBriefs={onOpenPastBriefs} active {...props} />);
    return { onReadAll, onMakeNow, onOpenPastBriefs };
}

describe("TodayBriefBand", () => {
    beforeEach(async () => {
        await i18n.changeLanguage("en");
        mocks.toggle.mockReset().mockResolvedValue(undefined);
    });
    afterEach(cleanup);

    it("reads the first three sentences across paragraphs", () => {
        expect(firstSentences(["One. Two.", "Three. Four."])).toBe("One. Two. Three.");
        expect(firstSentences(["", "Only one."])).toBe("Only one.");
    });

    it("leads with Hear it, the first three sentences, Read all and what is in the brief", () => {
        const { onReadAll, onOpenPastBriefs } = band();
        expect(screen.getByTestId("brief-audio-toggle")).toHaveAttribute("aria-pressed", "false");
        const lead = screen.getByTestId("brief-band-lead");
        expect(lead).toHaveTextContent("Daily test brief: the Halcyon review is the first thing today.");
        expect(lead).toHaveTextContent("Lena moved the call to ten.");
        expect(lead).not.toHaveTextContent("The test plan is still open.");
        expect(screen.getByText("In this brief")).toBeInTheDocument();
        expect(screen.getByText("2 meetings tomorrow")).toBeInTheDocument();
        expect(screen.getByText("1 thing to answer")).toBeInTheDocument();
        fireEvent.click(screen.getByTestId("brief-band-read-all"));
        expect(onReadAll).toHaveBeenCalledTimes(1);
        fireEvent.click(screen.getByTestId("brief-band-past"));
        expect(onOpenPastBriefs).toHaveBeenCalledTimes(1);
    });

    it("plays and pauses with Space on Today, but not while typing or with a modifier", () => {
        band();
        act(() => { fireEvent.keyDown(document.body, { key: " ", code: "Space" }); });
        expect(mocks.toggle).toHaveBeenCalledWith("2026-11-15-evening");
        const field = document.createElement("textarea");
        document.body.appendChild(field);
        act(() => { fireEvent.keyDown(field, { key: " ", code: "Space" }); });
        act(() => { fireEvent.keyDown(document.body, { key: " ", code: "Space", altKey: true }); });
        expect(mocks.toggle).toHaveBeenCalledTimes(1);
        field.remove();
    });

    it("leaves Space alone while Today is not in front", () => {
        band({ active: false });
        act(() => { fireEvent.keyDown(document.body, { key: " ", code: "Space" }); });
        expect(mocks.toggle).not.toHaveBeenCalled();
    });

    it("keeps the brief while the next one is being made", () => {
        band({ view: view({ building: true, pendingSlot: "morning" }) });
        expect(screen.getByTestId("brief-band-making")).toHaveTextContent("Your morning brief is being made");
        expect(screen.getByTestId("brief-band-lead")).toBeInTheDocument();
    });

    it("says a brief couldn't be made, with Try again, and keeps the brief", () => {
        const { onMakeNow } = band({ view: view({ pendingSlot: "morning", pendingAttempts: 2, pendingCause: "timeout" }) });
        expect(screen.getByTestId("brief-band-couldnt")).toHaveTextContent("Couldn't make the morning brief: it took too long.");
        expect(screen.queryByText(/wakes/)).toBeNull();
        fireEvent.click(screen.getByText("Try again"));
        expect(onMakeNow).toHaveBeenCalledTimes(1);
        expect(screen.getByTestId("brief-band-lead")).toBeInTheDocument();
    });

    it("shows no lead or Read all while today's full card is under the band", () => {
        band({ showLead: false });
        expect(screen.queryByTestId("brief-band-lead")).toBeNull();
        expect(screen.queryByTestId("brief-band-read-all")).toBeNull();
        expect(screen.getByTestId("brief-audio-toggle")).toBeInTheDocument();
    });

    it("hides the lead once Read all opened the full brief", () => {
        band({ readAll: true });
        expect(screen.queryByTestId("brief-band-lead")).toBeNull();
        expect(screen.getByTestId("brief-band-read-all")).toHaveAttribute("aria-expanded", "true");
    });

    it("says when the first brief comes, before there is one", () => {
        render(<TodayFirstBriefBand nextAt={new Date(2026, 10, 15, 18, 0).toISOString()} />);
        const first = screen.getByTestId("today-first-brief-band");
        expect(first).toHaveTextContent("Your first brief");
        expect(first).toHaveTextContent(/Your first brief comes at 6:00\sPM, made from today on this computer\./);
    });
});
