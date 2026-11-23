import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import i18n from "@/i18n";
import en from "@/i18n/locales/en/settings.json";
import zh from "@/i18n/locales/zh-CN/settings.json";
import pageSource from "./your-watch-page.tsx?raw";
import type { WatchPhoneQuietView, WatchSettingsView } from "@/lib/wails-shim-app";
import { SHOW_WATCH_PLACE, YourWatchPage } from "./your-watch-page";

const mocks = vi.hoisted(() => ({
    settings: vi.fn(),
    quiet: vi.fn(),
    notes: vi.fn(),
    heart: vi.fn(),
    place: vi.fn(),
}));
vi.mock("@/lib/wails-shim-app", () => ({
    GetWatchSettings: mocks.settings,
    GetWatchPhoneQuiet: mocks.quiet,
    SetWatchNotes: mocks.notes,
    SetWatchHeartRate: mocks.heart,
    SetWatchPlace: mocks.place,
}));

const off = { on: false, chosen: false };
function settings(over: Partial<WatchSettingsView> = {}): WatchSettingsView {
    return {
        paired: true,
        lastHeardAt: new Date().toISOString(),
        listening: { off: false },
        notes: { on: true, chosen: false },
        heartRate: off,
        place: off,
        ...over,
    };
}
function quiet(over: Partial<WatchPhoneQuietView> = {}): WatchPhoneQuietView {
    return { quiet: false, since: new Date().toISOString(), listening: true, offPending: false, ...over };
}

describe("Settings › Your watch", () => {
    beforeEach(async () => {
        vi.clearAllMocks();
        await i18n.changeLanguage("en");
        mocks.settings.mockResolvedValue(settings());
        mocks.quiet.mockResolvedValue(quiet());
    });

    it("says there's no watch, and nothing else, when none is paired", async () => {
        mocks.settings.mockResolvedValue(settings({ paired: false }));
        render(<YourWatchPage />);
        expect(await screen.findByTestId("your-watch-none")).toHaveTextContent(en.sees.noWatch);
        expect(screen.queryByRole("switch")).not.toBeInTheDocument();
    });

    it("shows an unknown pairing as a failure line with Try again, never as no watch", async () => {
        mocks.settings.mockResolvedValue(settings({ paired: null }));
        render(<YourWatchPage />);
        expect(await screen.findByText(en.yourWatch.pairingUnknown)).toBeInTheDocument();
        expect(screen.queryByText(en.sees.noWatch)).not.toBeInTheDocument();
        mocks.settings.mockResolvedValue(settings());
        fireEvent.click(screen.getByRole("button", { name: "Try again" }));
        expect(await screen.findByTestId("your-watch-state")).toBeInTheDocument();
    });

    it("shows a failed read with Try again, never a guessed state", async () => {
        mocks.quiet.mockRejectedValueOnce(new Error("down"));
        render(<YourWatchPage />);
        expect(await screen.findByText(en.yourWatch.readFailed)).toBeInTheDocument();
        expect(screen.queryByRole("switch")).not.toBeInTheDocument();
    });

    it("shows Place, now that the phone and watch follow the computer (PR 1466)", async () => {
        expect(SHOW_WATCH_PLACE).toBe(true);
        render(<YourWatchPage />);
        expect(await screen.findByRole("switch", { name: "Place" })).toBeInTheDocument();
        expect(screen.getByText(en.yourWatch.place.description)).toBeInTheDocument();
    });

    it("shows the state line and the three switches with their words ", async () => {
        render(<YourWatchPage />);
        expect(await screen.findByTestId("your-watch-state")).toHaveTextContent(/^Listening · last heard /);
        for (const key of ["notes", "heartRate", "place"] as const) {
            expect(screen.getByRole("switch", { name: en.yourWatch[key].title })).toBeInTheDocument();
            expect(screen.getByText(en.yourWatch[key].description)).toBeInTheDocument();
        }
        expect(en.yourWatch.heartRate.description).toBe('Adds "heart rate raised" beside what was said. Never the numbers.');
        expect(en.yourWatch.place.description).toBe('Adds the area, like "Mission District". Never the address.');
        // Listening isn't repeated here (T1): it's What Isoform sees › Your watch.
        expect(screen.queryByRole("switch", { name: en.sees.watch })).not.toBeInTheDocument();
    });

    it("shows the phone-quiet line only when the computer says it's past the threshold", async () => {
        const since = new Date(Date.now() - 3 * 60 * 60 * 1000).toISOString();
        mocks.quiet.mockResolvedValue(quiet({ quiet: false, since }));
        const first = render(<YourWatchPage />);
        await screen.findByTestId("your-watch-state");
        expect(screen.queryByTestId("your-watch-phone-quiet")).not.toBeInTheDocument();
        first.unmount();
        mocks.quiet.mockResolvedValue(quiet({ quiet: true, since }));
        render(<YourWatchPage />);
        expect(await screen.findByTestId("your-watch-phone-quiet")).toHaveTextContent(
            /^Your phone hasn't been in touch since .+, so nothing from your watch has come since\. Open Isoform on your phone to send it\.$/,
        );
    });

    it("shows the waiting-to-reach line while listening off is pending", async () => {
        mocks.settings.mockResolvedValue(settings({ listening: { off: true, since: new Date().toISOString() } }));
        mocks.quiet.mockResolvedValue(quiet({ offPending: true, listening: false }));
        render(<YourWatchPage />);
        expect(await screen.findByTestId("your-watch-off-pending")).toHaveTextContent(
            /^Waiting to reach your watch · your phone was last in touch at .+\. Anything it hears until then is deleted when it arrives\.$/,
        );
        expect(screen.getByTestId("your-watch-state")).toHaveTextContent(en.sees.watchOff);
    });

    it("calls each switch's own setter", async () => {
        mocks.notes.mockResolvedValue(settings({ notes: { on: false, chosen: true } }));
        mocks.heart.mockResolvedValue(settings({ heartRate: { on: true, chosen: true } }));
        mocks.place.mockResolvedValue(settings({ place: { on: true, chosen: true } }));
        render(<YourWatchPage />);
        fireEvent.click(await screen.findByRole("switch", { name: "Notes for long talks" }));
        await waitFor(() => expect(mocks.notes).toHaveBeenCalledWith(false));
        fireEvent.click(screen.getByRole("switch", { name: "Heart rate" }));
        await waitFor(() => expect(mocks.heart).toHaveBeenCalledWith(true));
        await waitFor(() => expect(screen.getByRole("switch", { name: "Place" })).not.toBeDisabled());
        fireEvent.click(screen.getByRole("switch", { name: "Place" }));
        await waitFor(() => expect(mocks.place).toHaveBeenCalledWith(true));
    });

    it("says a failed change in place with Try again", async () => {
        mocks.heart.mockRejectedValueOnce(new Error("store"));
        render(<YourWatchPage />);
        fireEvent.click(await screen.findByRole("switch", { name: "Heart rate" }));
        const line = await screen.findByText(en.yourWatch.changeFailed);
        mocks.heart.mockResolvedValueOnce(settings({ heartRate: { on: true, chosen: true } }));
        fireEvent.click(within(line.closest("[data-settings-failure]") as HTMLElement).getByRole("button", { name: "Try again" }));
        await waitFor(() => expect(mocks.heart).toHaveBeenCalledTimes(2));
    });

    it("shows the allow-on-the-watch line under Heart rate and Place only while each is on", async () => {
        mocks.settings.mockResolvedValue(settings({ heartRate: { on: true, chosen: true }, place: off }));
        const first = render(<YourWatchPage />);
        expect(await screen.findByTestId("your-watch-interim-heartRate")).toHaveTextContent(
            "Your watch asks you to allow it the first time you open it there.",
        );
        expect(screen.queryByTestId("your-watch-interim-place")).not.toBeInTheDocument();
        expect(screen.queryByTestId("your-watch-apple-place")).not.toBeInTheDocument();
        expect(screen.queryByTestId("your-watch-interim-notes")).not.toBeInTheDocument();
        first.unmount();
        mocks.settings.mockResolvedValue(settings({ heartRate: off, place: { on: true, chosen: true } }));
        render(<YourWatchPage />);
        expect(await screen.findByTestId("your-watch-interim-place")).toHaveTextContent(en.yourWatch.allowOnWatch);
        expect(screen.queryByTestId("your-watch-interim-heartRate")).not.toBeInTheDocument();
    });

    it("says under Place, while it's on, that Apple names the area from a rounded position", async () => {
        mocks.settings.mockResolvedValue(settings({ place: { on: true, chosen: true } }));
        render(<YourWatchPage />);
        expect(await screen.findByTestId("your-watch-apple-place")).toHaveTextContent(
            "Apple names the area from where you were, rounded to about a kilometre, once for each new place.",
        );
        expect(screen.queryByTestId("your-watch-apple-heartRate")).not.toBeInTheDocument();
    });

    it("still hides Place behind its switch when the switch is off", async () => {
        render(<YourWatchPage showPlace={false} />);
        expect(await screen.findByRole("switch", { name: "Heart rate" })).toBeInTheDocument();
        expect(screen.queryByRole("switch", { name: "Place" })).not.toBeInTheDocument();
    });

    it("keeps every string in both languages and none inline", () => {
        const leaves = (value: unknown, prefix = ""): string[] =>
            value && typeof value === "object"
                ? Object.entries(value).flatMap(([key, child]) => leaves(child, prefix ? `${prefix}.${key}` : key))
                : [prefix];
        expect(leaves(zh.yourWatch).sort()).toEqual(leaves(en.yourWatch).sort());
        expect(leaves(zh.watch).sort()).toEqual(leaves(en.watch).sort());
        expect(pageSource).not.toMatch(/\bt\(\s*["'`][^"'`]+["'`]\s*,\s*["'`]/);
        expect(pageSource).not.toContain("defaultValue");
    });
});
