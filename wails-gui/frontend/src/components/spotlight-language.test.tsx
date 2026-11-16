import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import i18n, { useTranslation } from "@/i18n";
import { App } from "../../../quick-chat-spotlight/frontend/src/App";

// Keep this unit test focused on the actual helper root's hydration and
// event wiring, without starting voice input or a chat session.
vi.mock("../../../quick-chat-spotlight/frontend/src/components/PillInput", () => ({
    PillInput: ({ onSubmit }: { onSubmit: (prompt: string) => void }) => {
        const { t } = useTranslation("chat");
        return <><input data-testid="prompt" placeholder={t("quickChat.placeholder")} /><button data-testid="open-conversation" onClick={() => onSubmit("Fixture question")}>Open fixture conversation</button></>;
    },
}));
vi.mock("../../../quick-chat-spotlight/frontend/src/components/ConversationPanel", () => ({
    ConversationPanel: () => null,
}));

type EventHandler = (payload: unknown) => Promise<void> | void;
const handlers = new Map<string, EventHandler>();
const fetchMock = vi.fn<typeof fetch>();

function setupPlatform(platform: "native" | "wails") {
    vi.stubGlobal("__spotlightConfig", { yansuPort: 4242, token: "helper-token" });
    vi.stubGlobal("webkit", platform === "native" ? { messageHandlers: { spotlight: { postMessage: vi.fn() } } } : undefined);
    vi.stubGlobal("runtime", {
        EventsOn: (event: string, handler: EventHandler) => {
            handlers.set(event, handler);
            return () => handlers.delete(event);
        },
    });
}

function respondAppearance(body: unknown) {
    fetchMock.mockImplementation(async (input) => new Response(JSON.stringify(
        String(input).includes("/theme") ? body : { suggestions: [] },
    ), { status: 200 }));
}

async function show(platform: "native" | "wails") {
    await act(async () => {
        const payload = { bundleID: "com.example", appName: "Example" };
        if (platform === "native") {
            window.dispatchEvent(new CustomEvent("spotlight:show", { detail: payload }));
        } else {
            await handlers.get("spotlight:show")?.(payload);
        }
    });
}

beforeEach(async () => {
    localStorage.clear();
    localStorage.setItem("i18nextLng", "en");
    await i18n.changeLanguage("en");
    handlers.clear();
    fetchMock.mockReset();
    vi.stubGlobal("fetch", fetchMock);
    setupPlatform("native");
});

afterEach(async () => {
    cleanup();
    document.documentElement.style.removeProperty("--color-panel");
    vi.useRealTimers();
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
    localStorage.clear();
    await i18n.changeLanguage("en");
});

describe("Spotlight app language", () => {
    it.each(["native", "wails"] as const)("uses the saved host language and refreshes on %s show", async (platform) => {
        setupPlatform(platform);
        respondAppearance({ theme: "paper", language: "zh-CN" });
        render(<App />);
        await waitFor(() => expect(screen.getByTestId("prompt")).toHaveAttribute("placeholder", "问问你的一天，或纠正 Isoform"));
        // The host preference wins without writing into the helper's separate
        // storage origin or changing its remembered/system-language default.
        expect(localStorage.getItem("i18nextLng")).toBe("en");
        expect(fetchMock).toHaveBeenCalledWith("http://127.0.0.1:4242/api/spotlight/theme", expect.objectContaining({
            headers: expect.objectContaining({ "X-Yansu-Spotlight-Token": "helper-token" }),
        }));

        respondAppearance({ theme: "paper", language: "en" });
        await show(platform);
        await waitFor(() => expect(screen.getByTestId("prompt")).toHaveAttribute("placeholder", "Ask about your day, or correct Isoform"));
    });

    it("refreshes an already-running helper when it regains focus after Settings", async () => {
        respondAppearance({ theme: "paper", language: "en" });
        render(<App />);
        await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));
        respondAppearance({ theme: "onedark", language: "zh-CN" });
        await act(async () => window.dispatchEvent(new Event("focus")));
        await waitFor(() => expect(screen.getByTestId("prompt")).toHaveAttribute("placeholder", "问问你的一天，或纠正 Isoform"));
        expect(document.documentElement).toHaveAttribute("data-theme", "onedark");
    });

    it("retains its current language when the host omits it or the bridge fails", async () => {
        respondAppearance({ theme: "paper", language: "zh-CN" });
        render(<App />);
        await waitFor(() => expect(i18n.resolvedLanguage).toBe("zh-CN"));

        respondAppearance({ theme: "onedark" });
        await act(async () => window.dispatchEvent(new Event("focus")));
        await waitFor(() => expect(document.documentElement).toHaveAttribute("data-theme", "onedark"));
        expect(i18n.resolvedLanguage).toBe("zh-CN");

        fetchMock.mockRejectedValue(new Error("bridge unavailable"));
        await act(async () => window.dispatchEvent(new Event("focus")));
        expect(i18n.resolvedLanguage).toBe("zh-CN");
        expect(document.documentElement).toHaveAttribute("data-theme", "onedark");
    });

    it("settles on a superseding focus refresh's language", async () => {
        respondAppearance({ theme: "paper", language: "en" });
        render(<App />);
        await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));

        let resolveShow!: (response: Response) => void;
        let resolveFocus!: (response: Response) => void;
        fetchMock
            .mockImplementationOnce(() => new Promise((resolve) => { resolveShow = resolve; }))
            .mockImplementationOnce(() => new Promise((resolve) => { resolveFocus = resolve; }));
        await show("native");
        await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
        await act(async () => window.dispatchEvent(new Event("focus")));
        await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(3));

        await act(async () => resolveShow(new Response(JSON.stringify({ theme: "paper", language: "en" }))));
        await act(async () => resolveFocus(new Response(JSON.stringify({ theme: "paper", language: "zh-CN" }))));
        await waitFor(() => expect(screen.getByTestId("prompt")).toHaveAttribute("placeholder", "问问你的一天，或纠正 Isoform"));
        // Quick chat no longer asks for app suggestions (computer-chat v1.0.0).
        expect(fetchMock.mock.calls.some(([url]) => String(url).includes("/suggestions"))).toBe(false);
    });

    it("ignores a stale appearance response after a newer refresh", async () => {
        let resolveFirst!: (response: Response) => void;
        fetchMock.mockImplementationOnce(() => new Promise((resolve) => { resolveFirst = resolve; }));
        render(<App />);
        await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));
        respondAppearance({ theme: "onedark", language: "zh-CN", texture: false });
        await act(async () => window.dispatchEvent(new Event("focus")));
        await waitFor(() => expect(i18n.resolvedLanguage).toBe("zh-CN"));
        await act(async () => resolveFirst(new Response(JSON.stringify({ theme: "paper", language: "en", texture: true }))));
        expect(i18n.resolvedLanguage).toBe("zh-CN");
        expect(document.documentElement).toHaveAttribute("data-theme", "onedark");
        expect(document.documentElement).not.toHaveAttribute("data-texture");
    });
});

describe.each(['paper', 'onedark'])('Flat Spotlight in %s', (theme) => {
    it.each(['native', 'wails'] as const)('ignores either saved host Texture value on %s startup, focus and summon', async (platform) => {
        setupPlatform(platform);
        vi.useFakeTimers();
        // Simulate a stale helper cache/root and the main window saving on.
        localStorage.setItem('yansu.appearance.texture', 'true');
        document.documentElement.removeAttribute('data-texture');
        respondAppearance({ theme, language: 'en', texture: true });
        const { container, unmount } = render(<App />);
        expect(document.documentElement).not.toHaveAttribute('data-texture');
        await act(async () => {});
        expect(document.documentElement).not.toHaveAttribute('data-texture');
        expect(document.documentElement).toHaveAttribute('data-theme', theme);
        expect(handlers.has('spotlight:hide')).toBe(false);
        await act(async () => fireEvent.click(screen.getByTestId('open-conversation')));
        expect(container.querySelector('.stage')).toHaveAttribute('data-view', 'conversation');
        const reads = fetchMock.mock.calls.length;
        await act(async () => { await vi.advanceTimersByTimeAsync(10000); });
        expect(fetchMock).toHaveBeenCalledTimes(reads);
        expect(vi.getTimerCount()).toBe(0);
        for (const texture of [false, true]) {
            respondAppearance({ theme, language: 'en', texture });
            await act(async () => window.dispatchEvent(new Event('focus')));
            expect(document.documentElement).not.toHaveAttribute('data-texture');
            await show(platform);
            expect(document.documentElement).not.toHaveAttribute('data-texture');
        }
        unmount();
        expect(vi.getTimerCount()).toBe(0);
    });

    it.each([false, true])('stays flat before and after a delayed host startup read (saved Texture: %s)', async (texture) => {
        document.documentElement.removeAttribute('data-texture');
        let resolveAppearance!: (response: Response) => void;
        fetchMock.mockImplementationOnce(() => new Promise((resolve) => { resolveAppearance = resolve; }));
        render(<App />);
        expect(document.documentElement).not.toHaveAttribute('data-texture');
        await waitFor(() => expect(resolveAppearance).toBeTypeOf('function'));
        await act(async () => resolveAppearance(new Response(JSON.stringify({ theme, texture }))));
        expect(document.documentElement).not.toHaveAttribute('data-texture');
        expect(document.documentElement).toHaveAttribute('data-theme', theme);
    });
});


describe('Native Spotlight surface tokens', () => {
    it.each([
        ['fff', [255, 255, 255]],
        ['ffffff', [255, 255, 255]],
        ['1a1a21', [26, 26, 33]],
    ] as const)('resolves the compiled %s panel token before painting the native surface', async (digits, rgb) => {
        setupPlatform('wails');
        const surface = vi.fn().mockResolvedValue(undefined);
        vi.stubGlobal('go', { main: { app: { SetSurfaceColor: surface } } });
        document.documentElement.style.setProperty('--color-panel', String.fromCharCode(35) + digits);
        respondAppearance({ theme: 'paper', language: 'en' });
        render(<App />);
        await waitFor(() => expect(surface).toHaveBeenCalledWith(...rgb));
    });

    it('keeps the native fallback when styles have not supplied a panel yet', async () => {
        setupPlatform('wails');
        const surface = vi.fn().mockResolvedValue(undefined);
        vi.stubGlobal('go', { main: { app: { SetSurfaceColor: surface } } });
        respondAppearance({ theme: 'paper', language: 'en' });
        render(<App />);
        await waitFor(() => expect(fetchMock).toHaveBeenCalled());
        await act(async () => {});
        expect(surface).not.toHaveBeenCalled();
    });
});
