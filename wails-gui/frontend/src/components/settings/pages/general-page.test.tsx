import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

// Open at login is offered on macOS and Windows only.
vi.hoisted(() => {
    Object.defineProperty(window.navigator, "platform", { value: "MacIntel", configurable: true });
});

const mocks = vi.hoisted(() => ({
    getStartAtLogin: vi.fn(),
    getAutoSpeakEnabled: vi.fn().mockResolvedValue(false),
    setStartAtLogin: vi.fn(),
    setThemePreference: vi.fn(),
    loadUIState: vi.fn().mockResolvedValue(""),
    saveUIState: vi.fn().mockResolvedValue(undefined),
}));

vi.mock("../../../../wailsjs/go/main/App", () => ({
    LoadUIState: mocks.loadUIState,
    SaveUIState: mocks.saveUIState,
    GetStartAtLogin: mocks.getStartAtLogin,
    SetStartAtLogin: mocks.setStartAtLogin,
    GetAutoSpeakEnabled: mocks.getAutoSpeakEnabled,
    SetAutoSpeakEnabled: vi.fn().mockResolvedValue(undefined),
    GetAutoSpeakAutomationEnabled: vi.fn().mockResolvedValue(false),
    SetAutoSpeakAutomationEnabled: vi.fn().mockResolvedValue(undefined),
    GetAutoSpeakNotificationsEnabled: vi.fn().mockResolvedValue(false),
    SetAutoSpeakNotificationsEnabled: vi.fn().mockResolvedValue(undefined),
    GetTtsModelStatus: vi.fn().mockResolvedValue({ zh: false, en: true }),
    GetThemePreference: vi.fn().mockResolvedValue(""),
    SetThemePreference: mocks.setThemePreference,
    GetQuickChatHotkey: vi.fn().mockResolvedValue(null),
    SetQuickChatHotkey: vi.fn().mockResolvedValue(undefined),
    SetQuickChatHotkeyRecording: vi.fn().mockResolvedValue(undefined),
}));

vi.mock("../../../../wailsjs/runtime/runtime", () => ({
    EventsOn: vi.fn(() => () => {}),
}));

import i18n from "@/i18n";
import { ThemeProvider } from "@/hooks/useTheme";
import { useKeybindings } from "@/hooks/useKeybindings";
import { GeneralPage, AppearanceSettings } from "./general-page";

// Opening Advanced loads the read-aloud switches and voices; wait for them
// so their state settles inside the test.
async function openAdvanced() {
    fireEvent.click(screen.getByRole("button", { name: /Advanced/ }));
    await screen.findByText("Chinese voice");
    await waitFor(() =>
        expect(screen.getByRole("switch", { name: "Read chat replies aloud" })).toBeEnabled(),
    );
}

function Harness() {
    const keybindings = useKeybindings();
    return (
        <ThemeProvider>
            <GeneralPage keybindings={keybindings} vimMode={false} onVimModeChange={vi.fn()} />
        </ThemeProvider>
    );
}

describe("GeneralPage", () => {
    beforeEach(async () => {
        vi.clearAllMocks();
        localStorage.clear();
        await i18n.changeLanguage("en");
        mocks.getStartAtLogin.mockResolvedValue(true);
        mocks.setStartAtLogin.mockResolvedValue(undefined);
        mocks.setThemePreference.mockResolvedValue(undefined);
        window.matchMedia = vi.fn().mockImplementation((query: string) => ({
            matches: false,
            media: query,
            addEventListener: vi.fn(),
            removeEventListener: vi.fn(),
        }));
    });

    it("moves reading width into Appearance and preserves the existing setting", async () => {
        localStorage.setItem("yansu.chat-wide-mode", "false");
        render(<ThemeProvider><AppearanceSettings readingWidth /></ThemeProvider>);
        expect(screen.getByRole("radio", { name: "Centered" })).toBeChecked();
        fireEvent.click(screen.getByRole("radio", { name: "Full" }));
        expect(localStorage.getItem("yansu.chat-wide-mode")).toBe("true");
        expect(screen.getByRole("radio", { name: "Full" })).toBeChecked();
    });

    it("opens with the everyday choices and keeps the rest under Advanced", async () => {
        render(<Harness />);

        expect(screen.getByText("Open Isoform when you log in")).toBeInTheDocument();
        expect(screen.getByText("Appearance")).toBeInTheDocument();
        expect(screen.getByText("Language")).toBeInTheDocument();
        expect(screen.getByText("Quick Chat shortcut")).toBeInTheDocument();
        // The misleading "Menu bar" title is gone.
        expect(screen.queryByText("Menu bar")).not.toBeInTheDocument();

        expect(screen.getByText("Keyboard shortcuts, read aloud and voices, tool steps in chat")).toBeInTheDocument();
        expect(screen.queryByText("Keyboard shortcuts")).not.toBeInTheDocument();

        await openAdvanced();
        expect(screen.getByRole("heading", { name: "Keyboard shortcuts" })).toBeInTheDocument();
        expect(screen.getByText("Read aloud")).toBeInTheDocument();
        expect(screen.getByText("Show tool steps in chat")).toBeInTheDocument();
        // Voice model names are secondary text only.
        expect(screen.getByText("Chinese voice")).toBeInTheDocument();
        expect(screen.getByText("MeloTTS, about 160 MB")).toBeInTheDocument();
    });

    it("reports a failed login setting read and retries without showing a guessed switch", async () => {
        mocks.getStartAtLogin.mockRejectedValueOnce(new Error("offline"));
        render(<Harness />);
        expect(await screen.findByText("Couldn't read this setting.")).toBeInTheDocument();
        expect(screen.queryByRole("switch", { name: "Open Isoform when you log in" })).toBeNull();
        fireEvent.click(screen.getByRole("button", { name: "Try again" }));
        expect(await screen.findByRole("switch", { name: "Open Isoform when you log in" })).toBeChecked();
    });

    it("keeps the login switch disabled while its save runs", async () => {
        let finish: () => void = () => {};
        mocks.setStartAtLogin.mockReturnValueOnce(new Promise<void>((resolve) => { finish = resolve; }));
        render(<Harness />);
        const toggle = await screen.findByRole("switch", { name: "Open Isoform when you log in" });
        await waitFor(() => expect(toggle).toBeChecked());

        fireEvent.click(toggle);
        expect(toggle).toBeDisabled();
        fireEvent.click(toggle);
        expect(mocks.setStartAtLogin).toHaveBeenCalledTimes(1);
        finish();
        await waitFor(() => expect(toggle).toBeEnabled());
        expect(toggle).not.toBeChecked();
    });

    it("reports a failed read-aloud setting read instead of showing it off", async () => {
        mocks.getAutoSpeakEnabled.mockRejectedValueOnce(new Error("offline"));
        render(<Harness />);
        fireEvent.click(screen.getByRole("button", { name: /Advanced/ }));
        expect(await screen.findByText("Couldn't read this setting.")).toBeInTheDocument();
        expect(screen.queryByRole("switch", { name: "Read chat replies aloud" })).toBeNull();
        fireEvent.click(screen.getByRole("button", { name: "Try again" }));
        expect(await screen.findByRole("switch", { name: "Read chat replies aloud" })).not.toBeChecked();
    });

    it("turns opening at login off", async () => {
        render(<Harness />);
        const toggle = await screen.findByRole("switch", { name: "Open Isoform when you log in" });
        await waitFor(() => expect(toggle).toBeChecked());

        fireEvent.click(toggle);
        expect(mocks.setStartAtLogin).toHaveBeenCalledWith(false);
    });

    it("offers Light, Dark and Match system, and keeps the Go side on a concrete theme", async () => {
        render(<Harness />);

        const system = screen.getByRole("radio", { name: "Match system" });
        fireEvent.click(system);
        expect(system).toHaveAttribute("aria-checked", "true");
        expect(localStorage.getItem("yansu.theme")).toBe("system");
        await waitFor(() => expect(mocks.setThemePreference).toHaveBeenCalledWith("paper"));
        expect(mocks.setThemePreference).not.toHaveBeenCalledWith("system");

        fireEvent.click(screen.getByRole("radio", { name: "Dark" }));
        expect(document.documentElement.getAttribute("data-theme")).toBe("onedark");
        expect(mocks.setThemePreference).toHaveBeenLastCalledWith("onedark");
    });

    it("shows System default until a language is picked", () => {
        const { unmount } = render(<Harness />);
        expect(screen.getByRole("combobox", { name: "Language" })).toHaveTextContent("System default");
        unmount();

        localStorage.setItem("i18nextLng", "zh-CN");
        render(<Harness />);
        expect(screen.getByRole("combobox", { name: "Language" })).toHaveTextContent("简体中文");
    });

    it("persists Show tool steps independently of debug mode", async () => {
        render(<Harness />);
        await openAdvanced();

        fireEvent.click(screen.getByRole("switch", { name: "Show tool steps in chat" }));
        expect(localStorage.getItem("yansu.show-tool-steps")).toBe("1");
        expect(localStorage.getItem("yansu.debug-mode-tools")).toBeNull();
    });
});

it.each([
    ['en', 'Texture'],
    ['zh-CN', '纹理'],
])('ignores retired Texture values and omits the setting in %s', async (language, label) => {
    localStorage.clear();
    localStorage.setItem('yansu.appearance.texture', 'true');
    mocks.loadUIState.mockClear();
    mocks.loadUIState.mockResolvedValue('true');
    await i18n.changeLanguage(language);
    render(<ThemeProvider><AppearanceSettings readingWidth /></ThemeProvider>);
    expect(screen.queryByRole('switch', { name: label })).toBeNull();
    expect(screen.queryByText(label)).toBeNull();
    expect(mocks.loadUIState).not.toHaveBeenCalledWith('yansu.appearance.texture');
});
