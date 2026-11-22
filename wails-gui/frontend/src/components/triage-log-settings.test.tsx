import { act, fireEvent, render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import i18n from "@/i18n";
import { SettingsPage } from "./settings-page";

const bridge = vi.hoisted(() => ({
    getTriageLogs: vi.fn(),
    listeners: new Map<string, (entry: unknown) => void>(),
}));

vi.mock("../../wailsjs/go/main/App", async (importOriginal) => ({
    ...await importOriginal<typeof import("../../wailsjs/go/main/App")>(),
    GetTriageLogs: bridge.getTriageLogs,
}));
vi.mock("../../wailsjs/runtime/runtime", async (importOriginal) => ({
    ...await importOriginal<typeof import("../../wailsjs/runtime/runtime")>(),
    EventsOn: (name: string, callback: (entry: unknown) => void) => {
        bridge.listeners.set(name, callback);
        return () => bridge.listeners.delete(name);
    },
}));

function openLearningDecisions() {
    return render(
        <SettingsPage
            onClose={vi.fn()}
            onReplayOnboarding={vi.fn()}
            onOpenPeople={vi.fn()}
            devToolsUnlocked
            initialTopic="triage"
        />,
    );
}

const privateEntry = {
    timestamp: "2026-10-16 10:15:32.123",
    source: "knowledge",
    id: "private-panel-entry",
    decision: "triage",
    reason: "title_bytes=22 description_bytes=28",
    title: "Planning the next call",
    description: "Prepare the follow-up agenda",
    worthMemory: true,
};

describe("Learning decisions content", () => {
    beforeEach(async () => {
        bridge.getTriageLogs.mockReset().mockResolvedValue([]);
        bridge.listeners.clear();
        await i18n.changeLanguage("en");
    });

    it("renders structured titles and descriptions from a default-mode reload", async () => {
        bridge.getTriageLogs.mockResolvedValue([privateEntry]);
        openLearningDecisions();
        const title = await screen.findByText(privateEntry.title);
        expect(title.closest("summary")).toHaveTextContent(privateEntry.title);
        expect(screen.getByText(privateEntry.description)).toBeInTheDocument();

        bridge.getTriageLogs.mockResolvedValue([{
            ...privateEntry,
            title: "Reloaded private title",
            description: "Reloaded private description",
        }]);
        fireEvent.click(screen.getByRole("button", { name: "Refresh" }));
        expect(await screen.findByText("Reloaded private title")).toBeInTheDocument();
        expect(screen.getByText("Reloaded private description")).toBeInTheDocument();
        expect(screen.queryByText(privateEntry.title)).not.toBeInTheDocument();
    });

    it("renders structured content delivered by the live triage event", async () => {
        openLearningDecisions();
        await act(async () => {});
        const listener = bridge.listeners.get("triage:log");
        expect(listener).toBeDefined();
        bridge.getTriageLogs.mockResolvedValue([privateEntry]);
        await act(async () => listener?.({ ...privateEntry, title: "Stale event title" }));
        expect(screen.queryByText("Stale event title")).not.toBeInTheDocument();
        expect(screen.getByText(privateEntry.title)).toBeInTheDocument();
        expect(screen.getByText(privateEntry.description)).toBeInTheDocument();
    });

    it("keeps gated Activity previews across live reads and clears them on reload or withdrawal", async () => {
        openLearningDecisions();
        await act(async () => {});
        const preview = { ...privateEntry, source: "activity", id: "activity-preview",
            title: "Debug Activity title", description: "Debug Activity details",
            reason: 'title="Debug Activity title" description="Debug Activity details"' };
        const metadata = { ...preview, title: undefined, description: undefined, reason: "reason_bytes=74" };
        bridge.getTriageLogs.mockResolvedValue([metadata]);
        await act(async () => bridge.listeners.get("triage:log")?.(preview));
        expect(screen.getByText(preview.title)).toBeInTheDocument();
        expect(screen.getByText(preview.description)).toBeInTheDocument();

        // A saved-entry refresh must preserve the already-authorized transient
        // Activity preview while ignoring the saved entry's event title.
        bridge.getTriageLogs.mockResolvedValue([metadata, privateEntry]);
        await act(async () => bridge.listeners.get("triage:log")?.({ ...privateEntry, title: "Stale saved title" }));
        expect(screen.getByText(preview.title)).toBeInTheDocument();
        expect(screen.getByText(privateEntry.title)).toBeInTheDocument();
        expect(screen.queryByText("Stale saved title")).not.toBeInTheDocument();

        fireEvent.click(screen.getByRole("button", { name: "Refresh" }));
        await act(async () => {});
        expect(screen.queryByText(preview.title)).not.toBeInTheDocument();
        expect(screen.queryByText(preview.description)).not.toBeInTheDocument();
        await act(async () => bridge.listeners.get("triage:log")?.(preview));
        expect(screen.getByText(preview.title)).toBeInTheDocument();
        // Clear the raw reason too, before the replacement read resolves.
        bridge.getTriageLogs.mockReturnValue(new Promise(() => {}));
        act(() => bridge.listeners.get("memory:updated")?.(undefined));
        expect(screen.queryByText(preview.title)).not.toBeInTheDocument();
        expect(screen.queryByText(preview.description)).not.toBeInTheDocument();
    });

    it.each(["en", "zh-CN"])("renders fixed dismissal reasons on reload and live refresh in %s", async (locale) => {
        await i18n.changeLanguage(locale);
        const dismissal = { ...privateEntry, source: "activity", title: undefined, description: undefined,
            decision: "dismiss", reason: "too few events: 3 < 5" };
        bridge.getTriageLogs.mockResolvedValue([dismissal]);
        openLearningDecisions();
        const expected = i18n.t("triage.reasons.tooFewEvents", { ns: "settings", current: 3, minimum: 5 });
        expect(await screen.findByText(expected)).toBeInTheDocument();
        bridge.getTriageLogs.mockResolvedValue([{ ...dismissal, id: "second" }]);
        await act(async () => bridge.listeners.get("triage:log")?.(dismissal));
        expect(screen.getByText(expected)).toBeInTheDocument();
        expect(screen.queryByText("reason_bytes=21")).not.toBeInTheDocument();
    });

    it.each(["memory:updated", "knowledge:updated", "closed-eyes:changed"])("drops titles immediately on %s and rejects stale reads", async (event) => {
        bridge.getTriageLogs.mockResolvedValue([privateEntry]);
        const view = openLearningDecisions();
        expect(await screen.findByText(privateEntry.title)).toBeInTheDocument();
        let resolveOld!: (entries: unknown[]) => void;
        let resolveCurrent!: (entries: unknown[]) => void;
        bridge.getTriageLogs.mockReturnValueOnce(new Promise((resolve) => { resolveOld = resolve; }))
            .mockReturnValueOnce(new Promise((resolve) => { resolveCurrent = resolve; }));
        fireEvent.click(screen.getByRole("button", { name: "Refresh" }));
        act(() => bridge.listeners.get(event)?.(undefined));
        expect(screen.queryByText(privateEntry.title)).not.toBeInTheDocument();
        await act(async () => resolveOld([privateEntry]));
        expect(screen.queryByText(privateEntry.title)).not.toBeInTheDocument();
        await act(async () => resolveCurrent([{ ...privateEntry, title: undefined, description: undefined }]));
        expect(screen.queryByText(privateEntry.title)).not.toBeInTheDocument();
        view.unmount();
        expect(bridge.listeners.has(event)).toBe(false);
    });

    it("keeps the quoted-reason fallback for retained legacy records", async () => {
        bridge.getTriageLogs.mockResolvedValue([{
            ...privateEntry,
            source: "activity",
            title: undefined,
            description: undefined,
            reason: 'title="Legacy planning" description="Legacy agenda"',
        }]);
        openLearningDecisions();
        expect(await screen.findByText("Legacy planning")).toBeInTheDocument();
        expect(screen.getByText("Legacy agenda")).toBeInTheDocument();
    });
});
