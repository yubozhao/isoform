import { act, fireEvent, render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import i18n from "@/i18n";
import { parseChatError, type ChatError } from "@/lib/chat-error";
import en from "@/i18n/locales/en/chat.json";
import zh from "@/i18n/locales/zh-CN/chat.json";
import componentSource from "./chat-error-card.tsx?raw";
import { ChatErrorCard } from "./chat-error-card";

const mocks = vi.hoisted(() => ({
    emit: vi.fn(),
    signIn: vi.fn(async () => undefined),
}));

vi.mock("../../wailsjs/runtime/runtime", () => ({
    EventsEmit: mocks.emit,
}));

vi.mock("@/lib/wails-shim-app", () => ({
    OpenAssistantSignIn: mocks.signIn,
}));

const quotaPeriod: ChatError = { kind: "quota_period", period: "weekly", detail: "HTTP 429" };
const quotaGeneric: ChatError = { kind: "quota_generic", detail: "HTTP 429" };
const authWords500 = 'anthropic: HTTP 500: {"error":"unauthorized: authentication failed, run claude login, reconnecting"}';

async function inChinese(run: () => void) {
    await act(async () => {
        await i18n.changeLanguage("zh-CN");
    });
    try {
        run();
    } finally {
        await act(async () => {
            await i18n.changeLanguage("en");
        });
    }
}

describe("ChatErrorCard", () => {
    beforeEach(async () => {
        vi.clearAllMocks();
        await i18n.changeLanguage("en");
    });

    // K1 box 4: a 401 shows Sign in; the same words in a 500's body are the AI's error.
    it("names signed out only from the code, and a 500 with auth words as the AI's error", () => {
        const { unmount } = render(<ChatErrorCard error={parseChatError("anthropic: HTTP 401: nope", "signed_out")} assistant="Claude Code" />);
        expect(screen.getByRole("status")).toHaveTextContent("Claude Code is signed out, so Isoform can't answer. Sign in, then send again. Sign in");
        fireEvent.click(screen.getByRole("button", { name: "Sign in" }));
        expect(mocks.signIn).toHaveBeenCalledWith("claude-code");
        expect(mocks.emit).not.toHaveBeenCalledWith("settings:open", "ai-provider");
        unmount();

        render(<ChatErrorCard error={parseChatError(authWords500, "assistant_error")} assistant="Claude Code" onRetry={() => {}} />);
        expect(screen.getByRole("status")).toHaveTextContent("Claude Code stopped with an error. · Try again · Details");
        expect(screen.queryByText(/Sign in/)).not.toBeInTheDocument();
    });

    // R75: after Sign in, Send again resends the same message with no retyping.
    it("offers Send again on a signed-out answer only when a resend is wired, and calls it", () => {
        const onRetry = vi.fn();
        const { unmount } = render(<ChatErrorCard error={parseChatError("x", "signed_out")} assistant="Claude Code" onRetry={onRetry} />);
        expect(screen.getByRole("status")).toHaveTextContent("Sign in, then send again. Sign in · Send again");
        fireEvent.click(screen.getByRole("button", { name: "Send again" }));
        expect(onRetry).toHaveBeenCalledTimes(1);
        unmount();

        render(<ChatErrorCard error={parseChatError("x", "signed_out")} assistant="Claude Code" />);
        expect(screen.queryByRole("button", { name: "Send again" })).toBeNull();
    });

    it("says plainly when no answer came back, with Send again", () => {
        const onRetry = vi.fn();
        const { unmount } = render(<ChatErrorCard error={parseChatError("No answer came back.", "no_reply")} onRetry={onRetry} />);
        expect(screen.getByRole("status")).toHaveTextContent("No answer came back. · Send again");
        fireEvent.click(screen.getByRole("button", { name: "Send again" }));
        expect(onRetry).toHaveBeenCalledTimes(1);
        unmount();

        render(<ChatErrorCard error={parseChatError("No answer came back.", "no_reply")} />);
        expect(screen.getByRole("status")).toHaveTextContent("No answer came back.");
        expect(screen.queryByRole("button", { name: "Send again" })).toBeNull();
    });

    it("never claims sign-in from words when no code proves it", () => {
        render(<ChatErrorCard error={parseChatError("401 unauthorized: run claude login, reconnecting")} />);
        expect(screen.queryByText(/Sign in/)).not.toBeInTheDocument();
        expect(screen.getByRole("status")).toHaveTextContent("Your AI stopped with an error.");
    });

    // K1 box 5: runner_unavailable's line, en and zh-CN.
    it("shows runner_unavailable's line in en and zh-CN", async () => {
        const error = parseChatError("the selected task runner is unavailable", "runner_unavailable");
        const { unmount } = render(<ChatErrorCard error={error} assistant="Codex" />);
        expect(screen.getByRole("status")).toHaveTextContent("Couldn't start it: Codex isn't available on your computer. · Set up");
        fireEvent.click(screen.getByRole("button", { name: "Set up" }));
        expect(mocks.emit).toHaveBeenCalledWith("settings:open", "ai-provider");
        unmount();
        await inChinese(() => {
            render(<ChatErrorCard error={error} assistant="Codex" />);
            expect(screen.getByRole("status")).toHaveTextContent("没能开始：你电脑上的 Codex 现在用不了。· 去设置");
        });
    });

    // K1 box 6: never red; Details unfolds the raw text and Hide folds it.
    it("is never red, and Details unfolds the raw text while Hide folds it", () => {
        const onRetry = vi.fn();
        const { container } = render(<ChatErrorCard error={parseChatError("boom: exit status 2")} onRetry={onRetry} />);
        expect(container.innerHTML).not.toMatch(/error-bg|text-error|border-error/);
        expect(screen.getByRole("status")).toHaveClass("text-subtle");
        expect(screen.queryByText("boom: exit status 2")).not.toBeInTheDocument();
        fireEvent.click(screen.getByRole("button", { name: "Details" }));
        expect(screen.getByText("boom: exit status 2")).toBeInTheDocument();
        fireEvent.click(screen.getByRole("button", { name: "Hide" }));
        expect(screen.queryByText("boom: exit status 2")).not.toBeInTheDocument();
        fireEvent.click(screen.getByRole("button", { name: "Try again" }));
        expect(onRetry).toHaveBeenCalledTimes(1);
    });

    it.each([
        ["signed out", parseChatError("x", "signed_out"), "text-warning"],
        ["no AI", parseChatError("", "no_assistant"), "text-warning"],
        ["a weekly limit", quotaPeriod, "text-warning"],
        ["a server error", parseChatError("x", "server"), "text-subtle"],
        ["a network error", parseChatError("dial tcp: connection refused"), "text-subtle"],
    ])("colors %s copper or gray", (_name, error, tone) => {
        const { container } = render(<ChatErrorCard error={error} assistant="Codex" />);
        expect(screen.getByRole("status")).toHaveClass(tone);
        expect(container.innerHTML).not.toMatch(/error-bg|text-error|border-error/);
    });

    it("sends a usage limit to Settings › Your AI and shows no Details", () => {
        render(<ChatErrorCard error={quotaGeneric} assistant="Claude Code" />);
        expect(screen.getByRole("status")).toHaveTextContent("Claude Code's limit is used up. · See usage");
        expect(screen.queryByRole("button", { name: "Details" })).not.toBeInTheDocument();
        fireEvent.click(screen.getByRole("button", { name: "See usage" }));
        expect(mocks.emit).toHaveBeenCalledWith("settings:open", "ai-provider");
    });

    it("renders the Chinese lines in the zh-CN locale", async () => {
        await inChinese(() => {
            render(<ChatErrorCard error={parseChatError("x", "signed_out")} assistant="Claude Code" />);
            expect(screen.getByRole("status")).toHaveTextContent("Claude Code 已退出登录，所以 Isoform 无法回答。登录后再发送。 登录");
        });
    });

    it("uses only keys both locales define and no red palette", () => {
        const used = [...componentSource.matchAll(/'(errors\.[A-Za-z.]+)'/g)].map((match) => match[1]);
        expect(used).toContain("errors.cause.signedOut");
        const lookup = (source: unknown, key: string) =>
            key.split(".").reduce<unknown>((node, part) => (node as Record<string, unknown>)?.[part], source);
        for (const key of used) {
            expect(typeof lookup(en, key), `en ${key}`).toBe("string");
            expect(typeof lookup(zh, key), `zh-CN ${key}`).toBe("string");
        }
        expect(componentSource).not.toMatch(/text-error|bg-error|border-error|toast/);
        expect(en.errors).not.toHaveProperty("auth");
        expect(zh.errors).not.toHaveProperty("auth");
    });
});
