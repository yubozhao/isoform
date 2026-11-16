import { act } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { onBriefPlaybackProgress, prepareBriefAudio, prepareLatestBriefAudio, stopBriefPlayback, toggleBriefPlayback } from "./brief-audio";
import { onPrivacyReset, resetRenderTierStores } from "./privacy-reset";

const mocks = vi.hoisted(() => ({
    getAudio: vi.fn(),
    health: vi.fn(),
    status: vi.fn(),
    events: new Map<string, Set<(payload: unknown) => void>>(),
}));
vi.mock("@/lib/wails-shim-app", () => ({ GetMobileBriefAudio: mocks.getAudio, GetMobileBriefHealth: mocks.health, GetModelAccountsStatus: mocks.status }));
vi.mock("../../wailsjs/runtime/runtime", () => ({
    EventsOn: (name: string, listener: (payload: unknown) => void) => {
        const listeners = mocks.events.get(name) ?? new Set();
        listeners.add(listener);
        mocks.events.set(name, listeners);
        return () => listeners.delete(listener);
    },
}));

const account = { provider: "claude", kind: "subscription", name: "Bo", on: true, windows: [{ used: 10 }] };
function accountsUpdated(accounts: unknown[]) {
    mocks.events.get("model-accounts:updated")?.forEach(listener => listener({ accounts }));
}
// Other render stores already subscribe while App remains mounted. Exercise
// their real reset broadcaster even before the audio-store fix is applied.
onPrivacyReset(() => {});

describe("shared brief playback", () => {
    beforeEach(async () => {
        stopBriefPlayback();
        vi.clearAllMocks();
        mocks.status.mockResolvedValue({ accounts: [account] });
        resetRenderTierStores();
        await act(async () => {});
        accountsUpdated([account]);
        vi.spyOn(window.HTMLMediaElement.prototype, "play").mockResolvedValue(undefined);
        vi.spyOn(window.HTMLMediaElement.prototype, "pause").mockImplementation(() => {});
        vi.spyOn(window.HTMLMediaElement.prototype, "load").mockImplementation(() => {});
        mocks.getAudio.mockImplementation(async (id: string) => ({ briefId: id, mime: "audio/mp4", base64: "AAAA" }));
    });
    afterEach(() => { stopBriefPlayback(); vi.restoreAllMocks(); });

    // Menu bar 1.0: the popover shows the same player's state and position.
    it("reports the playing brief and its progress for the menu bar", async () => {
        const seen: { briefId: string; state: string }[] = [];
        const off = onBriefPlaybackProgress(progress => seen.push({ briefId: progress.briefId, state: progress.state }));
        await toggleBriefPlayback("latest");
        await toggleBriefPlayback("latest");
        off();
        expect(seen).toEqual([
            { briefId: "", state: "idle" },
            { briefId: "latest", state: "loading" },
            { briefId: "latest", state: "playing" },
            { briefId: "latest", state: "idle" },
        ]);
    });
    it("lets Today pause the exact file started by the menu bar", async () => {
        await toggleBriefPlayback("latest");
        await toggleBriefPlayback("latest");
        expect(window.HTMLMediaElement.prototype.play).toHaveBeenCalledTimes(1);
        expect(window.HTMLMediaElement.prototype.pause).toHaveBeenCalledTimes(1);
        expect(mocks.getAudio).toHaveBeenCalledTimes(1);
        await toggleBriefPlayback("latest");
        expect(mocks.getAudio).toHaveBeenCalledTimes(1);
    });
    it("rejects a late file after privacy pause", async () => {
        let resolve!: (data: unknown) => void;
        mocks.getAudio.mockReturnValue(new Promise(r => { resolve = r; }));
        const pending = toggleBriefPlayback("old");
        stopBriefPlayback();
        resolve({ mime: "audio/mp4", base64: "AAAA" });
        await pending;
        expect(window.HTMLMediaElement.prototype.play).not.toHaveBeenCalled();
    });
    it("keeps the newest brief when an older read finishes later", async () => {
        let resolve!: (data: unknown) => void;
        mocks.getAudio.mockImplementationOnce(() => new Promise(r => { resolve = r; }));
        const old = toggleBriefPlayback("old");
        await toggleBriefPlayback("new");
        resolve({ mime: "audio/mp4", base64: "AAAA" });
        await old;
        expect(window.HTMLMediaElement.prototype.play).toHaveBeenCalledTimes(1);
        expect(mocks.getAudio.mock.calls.map(args => args[0])).toEqual(["old", "new"]);
    });
    it("rejects a late play promise after stopping", async () => {
        let resolve!: () => void;
        vi.mocked(window.HTMLMediaElement.prototype.play).mockImplementationOnce(() => new Promise<void>(r => { resolve = r; }));
        const pending = toggleBriefPlayback("old");
        await act(async () => {});
        stopBriefPlayback();
        resolve();
        await pending;
        expect(window.HTMLMediaElement.prototype.pause).toHaveBeenCalledTimes(2);
    });
    it("desk guard: sign-out reset stops shared playback", async () => {
        await toggleBriefPlayback("latest");
        resetRenderTierStores();
        expect(window.HTMLMediaElement.prototype.pause).toHaveBeenCalledTimes(1);
    });
    it("desk guard: sign-out reset fences a pending audio read", async () => {
        let resolve!: (data: unknown) => void;
        mocks.getAudio.mockReturnValue(new Promise(r => { resolve = r; }));
        const pending = toggleBriefPlayback("old");
        resetRenderTierStores();
        resolve({ mime: "audio/mp4", base64: "AAAA" });
        await pending;
        expect(window.HTMLMediaElement.prototype.play).not.toHaveBeenCalled();
    });
    it.each([{ accounts: [] }, { accounts: [{ ...account, on: false }] }])("disposes active audio on account withdrawal $accounts", async ({ accounts }) => {
        await toggleBriefPlayback("latest");
        const audio = vi.mocked(window.HTMLMediaElement.prototype.play).mock.instances[0] as unknown as HTMLAudioElement;
        expect(audio.src).toContain("base64,AAAA");
        accountsUpdated(accounts);
        expect(window.HTMLMediaElement.prototype.pause).toHaveBeenCalledTimes(1);
        expect(audio.hasAttribute("src")).toBe(false);
        expect(window.HTMLMediaElement.prototype.load).toHaveBeenCalledTimes(1);
        // Restoring the identity permits a new explicit play, with a fresh read.
        accountsUpdated([account]);
        await toggleBriefPlayback("latest");
        expect(mocks.getAudio).toHaveBeenCalledTimes(2);
    });
    it("fences an audio file that completes after account withdrawal", async () => {
        let resolve!: (data: unknown) => void;
        mocks.getAudio.mockReturnValue(new Promise(r => { resolve = r; }));
        const pending = toggleBriefPlayback("old");
        accountsUpdated([]);
        resolve({ mime: "audio/mp4", base64: "AAAA" });
        await pending;
        expect(window.HTMLMediaElement.prototype.play).not.toHaveBeenCalled();
    });
    it("fences a play promise that completes after account withdrawal", async () => {
        let resolve!: () => void;
        vi.mocked(window.HTMLMediaElement.prototype.play).mockImplementationOnce(() => new Promise<void>(r => { resolve = r; }));
        const pending = toggleBriefPlayback("old");
        await act(async () => {});
        const audio = vi.mocked(window.HTMLMediaElement.prototype.play).mock.instances[0] as unknown as HTMLAudioElement;
        accountsUpdated([]);
        expect(audio.hasAttribute("src")).toBe(false);
        resolve();
        await pending;
        expect(window.HTMLMediaElement.prototype.pause).toHaveBeenCalledTimes(2);
        accountsUpdated([account]);
        await toggleBriefPlayback("old");
        expect(mocks.getAudio).toHaveBeenCalledTimes(2);
    });
    // Bo, 2026-10-27: Play from the menu bar was slow to start. A prepared
    // brief plays on the click without another audio read.
    it("plays a prepared brief on the click without reading it again", async () => {
        prepareBriefAudio("latest");
        await act(async () => {});
        await toggleBriefPlayback("latest");
        expect(window.HTMLMediaElement.prototype.play).toHaveBeenCalledTimes(1);
        expect(mocks.getAudio).toHaveBeenCalledTimes(1);
        expect(mocks.getAudio).toHaveBeenCalledWith("latest");
    });
    it("prepares the newest brief from the health read and skips a brief without audio", async () => {
        mocks.health.mockResolvedValueOnce({ latestBriefId: "2026-10-27-evening", currentBriefId: "2026-10-27-evening", audio: { bytes: 10 } });
        await prepareLatestBriefAudio();
        await act(async () => {});
        await toggleBriefPlayback("2026-10-27-evening");
        expect(mocks.getAudio).toHaveBeenCalledTimes(1);
        mocks.health.mockResolvedValueOnce({ latestBriefId: "2026-10-27-morning", currentBriefId: "2026-10-27-morning" });
        await prepareLatestBriefAudio();
        expect(mocks.getAudio).toHaveBeenCalledTimes(1);
    });
    it("reads again on the click when the prepared read failed", async () => {
        mocks.getAudio.mockRejectedValueOnce(new Error("missing"));
        prepareBriefAudio("latest");
        await act(async () => {});
        await toggleBriefPlayback("latest");
        expect(mocks.getAudio).toHaveBeenCalledTimes(2);
        expect(window.HTMLMediaElement.prototype.play).toHaveBeenCalledTimes(1);
    });
    it("drops the prepared brief on a privacy reset", async () => {
        prepareBriefAudio("latest");
        await act(async () => {});
        resetRenderTierStores();
        await toggleBriefPlayback("latest");
        expect(mocks.getAudio).toHaveBeenCalledTimes(2);
    });
    it("preserves active playback and pending completions on quota-only refreshes", async () => {
        const quotaRefresh = () => accountsUpdated([{ ...account, windows: [{ used: 90 }] }]);
        await toggleBriefPlayback("active");
        quotaRefresh();
        expect(window.HTMLMediaElement.prototype.pause).not.toHaveBeenCalled();
        await toggleBriefPlayback("active");
        expect(window.HTMLMediaElement.prototype.pause).toHaveBeenCalledTimes(1);
        stopBriefPlayback();
        vi.clearAllMocks();
        let resolveRead!: (data: unknown) => void;
        let resolvePlay!: () => void;
        mocks.getAudio.mockReturnValueOnce(new Promise(r => { resolveRead = r; }));
        vi.mocked(window.HTMLMediaElement.prototype.play).mockImplementationOnce(() => new Promise<void>(r => { resolvePlay = r; }));
        const pending = toggleBriefPlayback("pending");
        quotaRefresh();
        resolveRead({ mime: "audio/mp4", base64: "AAAA" });
        await act(async () => {});
        expect(window.HTMLMediaElement.prototype.play).toHaveBeenCalledTimes(1);
        quotaRefresh();
        resolvePlay();
        await pending;
        expect(window.HTMLMediaElement.prototype.pause).not.toHaveBeenCalled();
        await toggleBriefPlayback("pending");
        expect(window.HTMLMediaElement.prototype.pause).toHaveBeenCalledTimes(1);
        expect(mocks.getAudio).toHaveBeenCalledTimes(1);
    });

});
