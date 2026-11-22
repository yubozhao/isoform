import { fireEvent, render, renderHook, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => Object.defineProperty(window.navigator, "platform", { value: "Win32", configurable: true }));
const bindings = vi.hoisted(() => ({ get: vi.fn(), set: vi.fn(), record: vi.fn() }));
vi.mock("../../wailsjs/go/main/App", () => ({
    GetQuickChatHotkey: bindings.get, SetQuickChatHotkey: bindings.set, SetQuickChatHotkeyRecording: bindings.record,
}));
vi.mock("../../wailsjs/runtime/runtime", () => ({ EventsOn: vi.fn(() => () => {}) }));
import i18n from "@/i18n";
import { useKeybindings } from "@/hooks/useKeybindings";
import { QuickChatShortcutCard } from "./keybindings-settings";
function Harness() { return <QuickChatShortcutCard keybindings={useKeybindings()} />; }

describe("Windows Quick Chat presets", () => {
    beforeEach(async () => {
        vi.resetAllMocks();
        localStorage.clear();
        Element.prototype.scrollIntoView = vi.fn();
        await i18n.changeLanguage("en");
        bindings.get.mockResolvedValue(null);
        bindings.set.mockResolvedValue(undefined);
    });
    it.each(["en", "zh-CN"])("names the Windows global pause Closed Eyes in %s", async (language) => {
        await i18n.changeLanguage(language);
        const { result } = renderHook(() => useKeybindings());
        const action = result.current.actions.find((entry) => entry.id === "pause-observing")!;
        // A product term: zh-CN keeps it in English.
        expect(action.label).toBe("Closed Eyes");
        expect(action.defaultCombo).toEqual({ meta: false, ctrl: true, alt: true, shift: true, key: "e" });
    });
    it.each(["en", "zh-CN"])("preserves the Windows default and offers only supported shortcuts in %s", async (language) => {
        await i18n.changeLanguage(language);
        render(<Harness />);
        const select = screen.getByRole("combobox");
        await waitFor(() => expect(select).toBeEnabled());
        expect(select).toHaveTextContent(i18n.t("settings:keybindings.quickChat.presets.altSpace"));
        fireEvent.keyDown(select, { key: "ArrowDown" });
        expect(screen.getAllByRole("option").map((row) => row.textContent)).toEqual([
            i18n.t("settings:keybindings.quickChat.presets.altSpace"),
            i18n.t("settings:keybindings.quickChat.presets.controlSpace"),
            i18n.t("settings:keybindings.quickChat.presets.windowsShiftSpace"),
            i18n.t("settings:keybindings.quickChat.presets.off"),
        ]);
        expect(screen.queryByRole("option", { name: i18n.t("settings:keybindings.quickChat.presets.optionTwice") })).not.toBeInTheDocument();
        expect(bindings.set).not.toHaveBeenCalled();
        expect(bindings.record).not.toHaveBeenCalled();
    });
    it.each([
        ["Alt and Space", { meta: false, ctrl: false, alt: true, shift: false, key: " " }],
        ["Control and Space", { meta: false, ctrl: true, alt: false, shift: false, key: " " }],
        ["Windows, Shift and Space", { meta: true, ctrl: false, alt: false, shift: true, key: " " }],
        ["Off", { meta: false, ctrl: false, alt: false, shift: false, key: "off" }],
    ])("recovers a saved unsupported binding by selecting %s", async (label, combo) => {
        bindings.get.mockResolvedValue({ meta: false, ctrl: false, alt: false, shift: false, key: "opt-opt" });
        render(<Harness />);
        const select = screen.getByRole("combobox");
        await waitFor(() => expect(select).toBeEnabled());
        expect(select).toHaveTextContent("Previously saved shortcut");
        fireEvent.keyDown(select, { key: "ArrowDown" });
        fireEvent.click(screen.getByRole("option", { name: label }));
        await waitFor(() => expect(bindings.set).toHaveBeenCalledWith(combo));
        await waitFor(() => expect(select).toHaveTextContent(label));
        expect(JSON.parse(localStorage.getItem("yansu.keybindings") ?? "{}")["quick-chat-global"]).toEqual(combo);
    });
});
