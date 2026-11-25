import { useState } from "react";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

// lib/platform reads navigator.platform at import time.
vi.hoisted(() => {
    Object.defineProperty(window.navigator, "platform", { value: "MacIntel", configurable: true });
});

const mocks = vi.hoisted(() => ({
    getQuickChatHotkey: vi.fn(),
    setQuickChatHotkey: vi.fn(),
    setQuickChatHotkeyRecording: vi.fn(),
}));

vi.mock("../../wailsjs/go/main/App", () => ({
    GetQuickChatHotkey: mocks.getQuickChatHotkey,
    SetQuickChatHotkey: mocks.setQuickChatHotkey,
    SetQuickChatHotkeyRecording: mocks.setQuickChatHotkeyRecording,
}));

vi.mock("../../wailsjs/runtime/runtime", () => ({
    EventsOn: vi.fn(() => () => {}),
}));

import i18n from "@/i18n";
import enSettings from "@/i18n/locales/en/settings.json";
import zhSettings from "@/i18n/locales/zh-CN/settings.json";
import { useKeybindings } from "@/hooks/useKeybindings";
import { KeysSheet } from "./keys-sheet";

function Harness({ initiallyOpen = true }: { initiallyOpen?: boolean }) {
    const keybindings = useKeybindings();
    const [open, setOpen] = useState(initiallyOpen);
    const [vimMode, setVimMode] = useState(false);
    return (
        <>
            <button type="button" onClick={() => setOpen(true)}>
                opener
            </button>
            <KeysSheet
                open={open}
                onOpenChange={setOpen}
                keybindings={keybindings}
                vimMode={vimMode}
                onVimModeChange={setVimMode}
            />
        </>
    );
}

// Recording listeners attach 50 ms after the click so the click itself is
// not captured.
async function startRecording(name: string) {
    fireEvent.click(screen.getByRole("button", { name }));
    await act(async () => {
        await new Promise((resolve) => setTimeout(resolve, 60));
    });
}

function keyPaths(value: unknown, prefix = ""): string[] {
    if (!value || typeof value !== "object") return [prefix];
    return Object.entries(value as Record<string, unknown>).flatMap(([key, child]) =>
        keyPaths(child, prefix ? `${prefix}.${key}` : key),
    );
}

const SWITCHES_KEY = "__isoformFeatureSwitchesForTest";
/** The `chat` switch on for one test: the rollback brings back Quick chat,
 *  In Chat and the Vim keys (scope cut A2). */
function withChatOn<T>(run: () => T | Promise<T>): Promise<T> {
    const g = globalThis as Record<string, unknown>;
    const before = g[SWITCHES_KEY];
    g[SWITCHES_KEY] = { ...(before as object), chat: true };
    return Promise.resolve()
        .then(run)
        .finally(() => {
            g[SWITCHES_KEY] = before;
        });
}

// Scope cut A2: these guards run with Chat off, as the app does by default;
// the rollback cases turn the \`chat\` switch on.
beforeEach(() => {
    const g = globalThis as Record<string, unknown>;
    g.__isoformFeatureSwitchesForTest = { ...(g.__isoformFeatureSwitchesForTest as object), chat: false };
});

describe("Keyboard shortcuts sheet", () => {
    beforeEach(async () => {
        vi.clearAllMocks();
        localStorage.clear();
        await i18n.changeLanguage("en");
        mocks.getQuickChatHotkey.mockResolvedValue(null);
        mocks.setQuickChatHotkey.mockResolvedValue(undefined);
        mocks.setQuickChatHotkeyRecording.mockResolvedValue(undefined);
    });

    const names = (group: string) =>
        Array.from(within(screen.getByRole("region", { name: group })).getAllByText(/./, { selector: "span.text-ink" })).map(
            (node) => node.textContent,
        );

    // Scope cut A2: Chat and Quick chat are gone, so is the In Chat group.
    it("lists four groups in the design's order with the app's names, and no Chat", () => {
        render(<Harness />);
        const dialog = screen.getByRole("dialog", { name: "Keyboard shortcuts" });
        const groups = within(dialog).getAllByRole("region").map((group) => group.getAttribute("aria-label"));
        expect(groups).toEqual(["Anywhere on this computer", "In Isoform", "On an item in Your work", "The brief"]);
        expect(names("Anywhere on this computer")).toEqual(["Closed Eyes"]);
        expect(names("In Isoform")).toEqual([
            "Today",
            "Agents",
            "Activity",
            "You",
            "Find",
            "Show or hide the sidebar",
            "Settings",
            "Keyboard shortcuts",
        ]);
        expect(names("On an item in Your work")).toEqual(["Move", "Yes", "No", "Not mine", "Edit", "Undo"]);
        expect(names("The brief")).toEqual(["Hear it, or pause"]);
        expect(within(dialog).queryByText("Quick chat")).not.toBeInTheDocument();
        expect(within(dialog).queryByRole("switch", { name: "Vim keys in Chat" })).not.toBeInTheDocument();
        // Never listed: the developer replay, the zoom keys and the old New chat.
        expect(within(dialog).queryByText("Run setup again")).not.toBeInTheDocument();
        expect(within(dialog).queryByText("Zoom in")).not.toBeInTheDocument();
        expect(within(dialog).queryByText("New chat")).not.toBeInTheDocument();
        expect(within(dialog).queryByText("⌘T")).not.toBeInTheDocument();
    });

    it("brings back Quick chat, Chat, In Chat and the Vim keys while the chat switch is on", () =>
        withChatOn(() => {
            render(<Harness />);
            const dialog = screen.getByRole("dialog", { name: "Keyboard shortcuts" });
            expect(within(dialog).getAllByRole("region").map((group) => group.getAttribute("aria-label"))).toEqual([
                "Anywhere on this computer",
                "In Isoform",
                "On an item in Your work",
                "The brief",
                "In Chat",
            ]);
            expect(names("Anywhere on this computer")).toEqual(["Quick chat", "Closed Eyes"]);
            expect(names("In Isoform").slice(0, 5)).toEqual(["Today", "Agents", "Activity", "You", "Chat"]);
            expect(names("In Chat")).toEqual(["Send", "New line"]);
            expect(screen.getByRole("button", { name: "Send, Return, Change" })).toBeInTheDocument();
            const vim = screen.getByRole("switch", { name: "Vim keys in Chat" });
            expect(vim).toHaveAttribute("aria-checked", "false");
            fireEvent.click(vim);
            expect(vim).toHaveAttribute("aria-checked", "true");
        }));

    it("shows Closed Eyes as always this one, with no Change, and the foot", () => {
        render(<Harness />);
        const anywhere = screen.getByRole("region", { name: "Anywhere on this computer" });
        expect(within(anywhere).getByText("always this one")).toBeInTheDocument();
        expect(within(anywhere).getByLabelText("Closed Eyes, Control Option Command E")).toBeInTheDocument();
        expect(within(anywhere).queryAllByRole("button", { name: /Change$/ })).toHaveLength(0);
        expect(within(screen.getByRole("region", { name: "On an item in Your work" })).queryByRole("button")).toBeNull();

        expect(screen.queryByText("j and k to scroll, i to type, outside the box")).not.toBeInTheDocument();
        expect(
            screen.getByText("Y, N, M and E work only on the item in focus, so typing anywhere else never answers it."),
        ).toBeInTheDocument();
    });

    it("puts focus on the first Change and reads each row with its keys as words", async () => {
        render(<Harness />);
        const first = screen.getByRole("button", { name: "Today, Command 1, Change" });
        await waitFor(() => expect(first).toHaveFocus());
        expect(screen.getByRole("button", { name: "Agents, Command 2, Change" })).toBeInTheDocument();
        expect(screen.getByRole("button", { name: "You, Command 4, Change" })).toBeInTheDocument();
    });

    it("records new keys and announces how to keep the old ones", async () => {
        render(<Harness />);
        await startRecording("Find, Command K, Change");
        expect(screen.getByText("Press the new keys, or Escape to keep Command K.")).toBeInTheDocument();
        fireEvent.keyDown(window, { key: "j", metaKey: true });
        expect(screen.getByRole("button", { name: "Find, Command J, Change" })).toBeInTheDocument();
        expect(JSON.parse(localStorage.getItem("yansu.keybindings") ?? "{}")["command-palette"]).toEqual({
            meta: true,
            ctrl: false,
            alt: false,
            shift: false,
            key: "j",
        });
    });

    it("says which row a clash belongs to and saves nothing", async () => {
        render(<Harness />);
        await startRecording("Find, Command K, Change");
        fireEvent.keyDown(window, { key: "e", metaKey: true, ctrlKey: true, altKey: true });
        expect(screen.getByRole("alert")).toHaveTextContent("Already used by Closed Eyes.");
        expect(screen.getByRole("button", { name: "Find, Command K, Change" })).toBeInTheDocument();
        expect(localStorage.getItem("yansu.keybindings") ?? "{}").not.toContain("command-palette");

        await startRecording("Find, Command K, Change");
        fireEvent.keyDown(window, { key: "1", metaKey: true });
        expect(screen.getByRole("alert")).toHaveTextContent("Already used by Today.");
    });

    it.each([
        [{ key: "y" }, "Yes"],
        [{ key: "n" }, "No"],
        [{ key: "m" }, "Not mine"],
        [{ key: "e" }, "Edit"],
        [{ key: "ArrowUp" }, "Move"],
        [{ key: "ArrowDown" }, "Move"],
        [{ key: "z", metaKey: true }, "Undo"],
        [{ key: " " }, "Hear it, or pause"],
        [{ key: "Enter", shiftKey: true }, "New line"],
    ])("refuses a listed fixed key %o and names its row", async (keys, row) => {
        render(<Harness />);
        await startRecording("Find, Command K, Change");
        fireEvent.keyDown(window, keys);
        expect(screen.getByRole("alert")).toHaveTextContent(`Already used by ${row}.`);
        expect(screen.getByRole("button", { name: "Find, Command K, Change" })).toBeInTheDocument();
        expect(localStorage.getItem("yansu.keybindings") ?? "{}").not.toContain("command-palette");
    });

    it("keeps the Quick chat OS hook in step when it is changed from the sheet (chat switch on)", () =>
        withChatOn(async () => {
        render(<Harness />);
        await startRecording("Quick chat, Option twice, Change");
        await waitFor(() => expect(mocks.setQuickChatHotkeyRecording).toHaveBeenCalledWith(true));
        fireEvent.keyDown(window, { key: " ", ctrlKey: true });
        const combo = { meta: false, ctrl: true, alt: false, shift: false, key: " " };
        await waitFor(() => expect(mocks.setQuickChatHotkey).toHaveBeenCalledWith(combo));
        await waitFor(() =>
            expect(screen.getByRole("button", { name: "Quick chat, Control Space, Change" })).toBeInTheDocument(),
        );
        }));

    it("Escape while recording keeps the keys and the sheet; Escape again closes it and returns focus", async () => {
        render(<Harness initiallyOpen={false} />);
        const opener = screen.getByRole("button", { name: "opener" });
        opener.focus();
        fireEvent.click(opener);
        await startRecording("Find, Command K, Change");
        fireEvent.keyDown(window, { key: "Escape" });
        expect(screen.getByRole("dialog")).toBeInTheDocument();
        expect(screen.getByRole("button", { name: "Find, Command K, Change" })).toBeInTheDocument();

        fireEvent.keyDown(document.activeElement ?? document.body, { key: "Escape" });
        await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
        await waitFor(() => expect(opener).toHaveFocus());
    });

    it("Done closes the sheet", async () => {
        render(<Harness />);
        fireEvent.click(screen.getByRole("button", { name: "Done" }));
        await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    });

    it("has the same keys.* words in en and zh-CN", () => {
        expect(keyPaths(zhSettings.keys).sort()).toEqual(keyPaths(enSettings.keys).sort());
        expect(keyPaths(enSettings.keys)).toEqual(
            expect.arrayContaining([
                "title",
                "anywhere",
                "inApp",
                "onItem",
                "brief",
                "chat",
                "always",
                "hear",
                "newLine",
                "vim",
                "vimSub",
                "focusNote",
                "clash",
                "recording",
            ]),
        );
    });
});
