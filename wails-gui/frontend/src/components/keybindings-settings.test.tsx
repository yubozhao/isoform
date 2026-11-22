import { fireEvent, render, renderHook, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

// The shortcut list is platform-aware (Pause observing exists on macOS and
// Windows only), and lib/platform reads navigator.platform at import time.
vi.hoisted(() => {
    Object.defineProperty(window.navigator, "platform", { value: "MacIntel", configurable: true });
});

const mocks = vi.hoisted(() => ({
    getQuickChatHotkey: vi.fn(),
    setQuickChatHotkey: vi.fn(),
    setQuickChatHotkeyRecording: vi.fn(),
    toastSuccess: vi.fn(),
    toastError: vi.fn(),
}));

vi.mock("../../wailsjs/go/main/App", () => ({
    GetQuickChatHotkey: mocks.getQuickChatHotkey,
    SetQuickChatHotkey: mocks.setQuickChatHotkey,
    SetQuickChatHotkeyRecording: mocks.setQuickChatHotkeyRecording,
}));

vi.mock("../../wailsjs/runtime/runtime", () => ({
    EventsOn: vi.fn(() => () => {}),
}));

vi.mock("sonner", () => ({
    toast: { success: mocks.toastSuccess, error: mocks.toastError, info: vi.fn() },
}));

import i18n from "@/i18n";
import { useKeybindings } from "@/hooks/useKeybindings";
import { QuickChatShortcutCard } from "./keybindings-settings";

function Harness() {
    return <QuickChatShortcutCard keybindings={useKeybindings()} />;
}

describe("Keyboard shortcuts", () => {
    beforeEach(async () => {
        vi.clearAllMocks();
        localStorage.clear();
        Element.prototype.scrollIntoView = vi.fn();
        await i18n.changeLanguage("en");
        mocks.getQuickChatHotkey.mockResolvedValue(null);
        mocks.setQuickChatHotkey.mockResolvedValue(undefined);
        mocks.setQuickChatHotkeyRecording.mockResolvedValue(undefined);
    });

    it.each([
        ["en", ["Find", "Closed Eyes", "Quick chat", "Send", "Settings", "Show or hide the sidebar", "Today", "Chat", "Agents", "Activity", "You"]],
        ["zh-CN", ["查找", "Closed Eyes", "快速对话", "发送", "设置", "显示或隐藏侧边栏", "今日", "聊天", "Agents", "活动", "你"]],
    ])("uses the app's names for the keys in %s", async (language, names) => {
        await i18n.changeLanguage(language);
        const { result } = renderHook(() => useKeybindings());
        const label = (id: string) => result.current.actions.find((action) => action.id === id)?.label;
        expect(
            ["command-palette", "pause-observing", "quick-chat-global", "send-prompt", "open-settings", "toggle-sidebar", "go-today", "go-chat", "go-agents", "go-activity", "go-you"].map(label),
        ).toEqual(names);
    });

    it("has no New chat key: Command T does nothing", () => {
        const { result } = renderHook(() => useKeybindings());
        expect(result.current.actions.some((action) => action.id === "new-chat")).toBe(false);
        expect(result.current.findMatchingAction(new KeyboardEvent("keydown", { key: "t", metaKey: true }))).toBeNull();
        expect(i18n.exists("settings:keybindings.commands.new-chat.label")).toBe(false);
    });

    it("opens keyboard shortcuts with Command slash", () => {
        const { result } = renderHook(() => useKeybindings());
        const event = new KeyboardEvent("keydown", { key: "/", metaKey: true });
        expect(result.current.findMatchingAction(event)).toBe("keyboard-shortcuts");
    });

    it.each(["en", "zh-CN"])("offers exactly four Quick Chat presets in human words in %s", async (language) => {
        await i18n.changeLanguage(language);
        render(<Harness />);
        const select = screen.getByRole("combobox", { name: i18n.t("settings:keybindings.quickChat.title") });
        await waitFor(() => expect(select).toBeEnabled());
        expect(select).toHaveTextContent(i18n.t("settings:keybindings.quickChat.presets.optionTwice"));
        fireEvent.keyDown(select, { key: "ArrowDown" });
        expect(screen.getAllByRole("option").map((option) => option.textContent)).toEqual([
            i18n.t("settings:keybindings.quickChat.presets.optionTwice"),
            i18n.t("settings:keybindings.quickChat.presets.controlSpace"),
            i18n.t("settings:keybindings.quickChat.presets.commandShiftSpace"),
            i18n.t("settings:keybindings.quickChat.presets.off"),
        ]);
        expect(select.textContent).not.toMatch(/[⌥⌘⌃⇧]/);
        expect(mocks.setQuickChatHotkeyRecording).not.toHaveBeenCalled();
    });

    it.each([
        ["Control and Space", { meta: false, ctrl: true, alt: false, shift: false, key: " " }],
        ["Command, Shift and Space", { meta: true, ctrl: false, alt: false, shift: true, key: " " }],
        ["Off", { meta: false, ctrl: false, alt: false, shift: false, key: "off" }],
        ["Tap Option twice", { meta: false, ctrl: false, alt: false, shift: false, key: "opt-opt" }],
    ])("saves %s using the existing binding model", async (name, combo) => {
        mocks.getQuickChatHotkey.mockResolvedValue({ meta: true, ctrl: false, alt: false, shift: false, key: "j" });
        render(<Harness />);
        const select = screen.getByRole("combobox", { name: "Quick Chat shortcut" });
        await waitFor(() => expect(select).toBeEnabled());
        expect(select).toHaveTextContent("Previously saved shortcut");
        expect(mocks.setQuickChatHotkey).not.toHaveBeenCalled();
        fireEvent.keyDown(select, { key: "ArrowDown" });
        fireEvent.click(screen.getByRole("option", { name }));
        await waitFor(() => expect(mocks.setQuickChatHotkey).toHaveBeenCalledWith(combo));
        await waitFor(() => expect(select).toHaveTextContent(name));
        expect(mocks.setQuickChatHotkeyRecording).not.toHaveBeenCalled();
        expect(JSON.parse(localStorage.getItem("yansu.keybindings") ?? "{}")["quick-chat-global"]).toEqual(combo);
    });

    it("keeps the previous selection if applying a preset fails", async () => {
        mocks.setQuickChatHotkey.mockRejectedValue(new Error("unavailable"));
        render(<Harness />);
        const select = screen.getByRole("combobox", { name: "Quick Chat shortcut" });
        await waitFor(() => expect(select).toBeEnabled());
        fireEvent.keyDown(select, { key: "ArrowDown" });
        fireEvent.click(screen.getByRole("option", { name: "Off" }));
        const line = await screen.findByRole("alert");
        expect(line).toHaveTextContent("Couldn't change this. Try again");
        expect(line).toHaveClass("text-copper");
        expect(mocks.toastError).not.toHaveBeenCalled();
        expect(select).toHaveTextContent("Tap Option twice");
        expect(JSON.parse(localStorage.getItem("yansu.keybindings") ?? "{}")).toEqual({});

        mocks.setQuickChatHotkey.mockResolvedValue(undefined);
        fireEvent.click(screen.getByRole("button", { name: "Try again" }));
        await waitFor(() => expect(select).toHaveTextContent("Off"));
        expect(mocks.setQuickChatHotkey).toHaveBeenCalledTimes(2);
        expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    });
});
