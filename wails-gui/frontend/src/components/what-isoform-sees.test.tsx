import { fireEvent, render, screen, within } from "@testing-library/react";
import { beforeEach, expect, it, vi } from "vitest";
import i18n from "@/i18n";
import { WhatIsoformSeesSheet } from "./what-isoform-sees";

const mocks = vi.hoisted(() => ({ get: vi.fn(), emit: vi.fn() }));
vi.mock("@/lib/wails-shim-app", () => ({ GetWhatIsoformSees: mocks.get }));
vi.mock("@/lib/wails-shim-runtime", () => ({ EventsEmit: mocks.emit }));

beforeEach(() => {
    vi.clearAllMocks();
    mocks.get.mockResolvedValue({ assistant: "claude_code", sources: [
        { job: "mail", on: false }, { job: "calendar", on: true }, { job: "calls", on: true },
        { job: "screen", on: true }, { job: "assistant", on: true }, { job: "contacts", on: false },
    ] });
});

it.each([
    ["en", "What Isoform sees", "Mail", "Can't see your mail yet, so there are no drafts.", "Turn on", "What leaves your computer"],
    ["zh-CN", "Isoform 能看到什么", "邮件", "还看不到你的邮件，所以没有草稿。", "打开", "什么会离开你的电脑"],
])("renders the phone's jobs and copy in %s and opens the mail control", async (language, title, mail, detail, turnOn, leaves) => {
    await i18n.changeLanguage(language);
    const onOpenChange = vi.fn();
    render(<WhatIsoformSeesSheet open onOpenChange={onOpenChange} />);
    const dialog = await screen.findByRole("dialog", { name: title });
    expect(await within(dialog).findByText(detail)).toBeInTheDocument();
    expect(within(dialog).getByText(leaves)).toBeInTheDocument();
    const row = within(dialog).getByText(mail).parentElement!.parentElement!;
    fireEvent.click(within(row).getByRole("button", { name: turnOn }));
    expect(onOpenChange).toHaveBeenCalledWith(false);
    expect(mocks.emit).toHaveBeenCalledWith("settings:open", "activity", { anchor: "sees-mail" });
});

it("shows a failure instead of claiming permission state when the read fails", async () => {
    await i18n.changeLanguage("en");
    mocks.get.mockRejectedValueOnce(new Error("unavailable"));
    render(<WhatIsoformSeesSheet open onOpenChange={vi.fn()} />);
    expect(await screen.findByRole("alert")).toHaveTextContent("Could not load what Isoform sees.");
    expect(screen.queryByText("On")).not.toBeInTheDocument();
});

// shell-review v1.0.0 B1-B3: the computer's own words, and nothing that says
// screenshots or audio never leave when the WeChat crop does.
it.each([
    ["en", [
        "The text a task needs, to your AI, Claude Code.",
        "For WeChat, a cropped screenshot of the open chat, to your AI.",
        "Your briefs, cards and drafts, to your phone, encrypted end to end.",
        "Hears them to take notes. Audio is stored on your computer.",
        "Sees the app and window you're in, and the text you type. Screenshots are stored on your computer. For WeChat, a cropped screenshot goes to your AI.",
        "Only when you say so",
        "Isoform doesn't send, delete or accept anything until you say so.",
        "Sends only when you click Send.",
    ], ["never leave", "this phone", "until you tap", "tap Send", "Only with your tap", "Isoform on your computer"], "Change these in Settings › What Isoform sees"],
    ["zh-CN", [
        "音频存储在你的电脑上。",
        "以及你输入的文字",
        "简报、卡片和草稿，发到你的手机，端到端加密。",
    ], ["永远不会离开", "这部手机", "你点之前"], "在 设置 › Isoform 看到的 里修改"],
])("uses the computer's words in %s and opens Settings › What Isoform sees from the foot", async (language, present, absent, foot) => {
    await i18n.changeLanguage(language);
    mocks.get.mockResolvedValue({ assistant: "claude_code", phonePaired: true, sources: [
        { job: "mail", on: true }, { job: "calendar", on: true }, { job: "calls", on: true },
        { job: "screen", on: true }, { job: "assistant", on: true }, { job: "contacts", on: false },
    ] });
    const onOpenChange = vi.fn();
    render(<WhatIsoformSeesSheet open onOpenChange={onOpenChange} />);
    const dialog = await screen.findByRole("dialog");
    await within(dialog).findByRole("button", { name: foot });
    for (const text of present) expect(dialog.textContent).toContain(text);
    for (const text of absent) expect(dialog.textContent).not.toContain(text);
    fireEvent.click(within(dialog).getByRole("button", { name: foot }));
    expect(onOpenChange).toHaveBeenCalledWith(false);
    expect(mocks.emit).toHaveBeenCalledWith("settings:open", "activity");
});

it("names the phone only while one is paired", async () => {
    await i18n.changeLanguage("en");
    render(<WhatIsoformSeesSheet open onOpenChange={vi.fn()} />);
    const dialog = await screen.findByRole("dialog");
    await within(dialog).findByText("What leaves your computer");
    expect(dialog.textContent).not.toContain("to your phone");
    expect(dialog.textContent).toContain("For WeChat, a cropped screenshot of the open chat, to your AI.");
});

it("shows the failure in copper with a working Try again", async () => {
    await i18n.changeLanguage("en");
    mocks.get.mockRejectedValueOnce(new Error("unavailable"));
    render(<WhatIsoformSeesSheet open onOpenChange={vi.fn()} />);
    const alert = await screen.findByRole("alert");
    expect(alert.className).toContain("text-copper");
    expect(alert.className).not.toContain("text-error");
    fireEvent.click(within(alert).getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("What leaves your computer")).toBeInTheDocument();
    expect(mocks.get).toHaveBeenCalledTimes(2);
});
