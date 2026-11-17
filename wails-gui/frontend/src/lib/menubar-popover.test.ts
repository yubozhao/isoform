import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it, vi } from "vitest";

const template = readFileSync(resolve(process.cwd(), "../menubar_window_assets/popover.html"), "utf8");

function page(overrides: Record<string, unknown> = {}, wordOverrides: Record<string, string> = {}) {
    // The 1.0 page's model (menubar_model.go menuBarView): words echo their keys.
    const words = Object.fromEntries([
        "observing", "paused", "off", "starting", "pause", "resume", "turnOn", "playing",
        "waiting", "seeAll", "empty", "failed", "make", "open", "openShortcut", "update", "restart",
        "settings", "footerQuit", "onThisMac", "name", "closedEyes", "openEyes", "yes", "no", "openToday",
        "allDone", "stillAnswer", "quickChat",
    ].map(key => [key, key]));
    Object.assign(words, wordOverrides);
    const model = { language: "en", theme: "paper", words, status: "observing", state: "observing", paused: false,
        waiting: 5, brief: { kind: "ready", id: "brief", title: "brief", meta: "7:00 AM · 0:48", seconds: 48 },
        items: [{ id: "row", title: "title", detail: "detail" }],
        next: { id: "row", line: "line", title: "title", detail: "detail" }, label: "Isoform", ...overrides };
    const document = new DOMParser().parseFromString(template.replace("/*__MODEL__*/null", JSON.stringify(model)), "text/html");
    const postMessage = vi.fn();
    const host = { webkit: { messageHandlers: { menubar: { postMessage } } } };
    // Run the production page script against an isolated unit-test document.
    // The native bridge is a unit boundary; no app or application API runs.
    const script = document.querySelector("script")!.textContent!;
    new Function("window", "document", "setInterval", script)(host, document, vi.fn());
    postMessage.mockClear();
    return { document, postMessage };
}

describe("menu bar embedded page", () => {
    it("opens Isoform with the footer's Command Shift I shortcut", () => {
        const { document, postMessage } = page();
        const event = new KeyboardEvent("keydown", { key: "I", metaKey: true, shiftKey: true, cancelable: true });
        document.dispatchEvent(event);
        expect(event.defaultPrevented).toBe(true);
        expect(postMessage).toHaveBeenCalledTimes(1);
        expect(postMessage).toHaveBeenCalledWith({ command: "open" });
    });
    it("leaves unmodified and other modified I keys alone", () => {
        const { document, postMessage } = page();
        for (const modifiers of [{}, { metaKey: true }, { metaKey: true, shiftKey: true, altKey: true }, { metaKey: true, shiftKey: true, ctrlKey: true }]) {
            document.dispatchEvent(new KeyboardEvent("keydown", { key: "i", ...modifiers, cancelable: true }));
        }
        expect(postMessage).not.toHaveBeenCalled();
    });
    it("dismisses the popover with Escape", () => {
        const { document, postMessage } = page();
        const event = new KeyboardEvent("keydown", { key: "Escape", cancelable: true });
        document.dispatchEvent(event);
        expect(event.defaultPrevented).toBe(true);
        expect(postMessage).toHaveBeenCalledTimes(1);
        expect(postMessage).toHaveBeenCalledWith({ command: "close" });
    });
    // Computer redesign: the Next row answers here (Yes, No, Open Today),
    // Closed Eyes is the first control, and Quick chat has its own row.
    it("keeps Next, playback, Closed Eyes, Quick chat, Make my brief now and the footer wired", () => {
        const { document, postMessage } = page();
        for (const selector of [".it", "#br", "#yes", "#no", "#openToday", "#qc", "#make", "#open", "#settings", "#quit", "#hb"]) {
            (document.querySelector(selector) as HTMLButtonElement).click();
        }
        // Closed Eyes changes the header at once, which reports its new height.
        expect(postMessage.mock.calls.map(([message]) => message.command).filter((command) => command !== "resize")).toEqual(
            ["today:row", "play:brief", "workYes:row", "workNo:row", "today:row", "quickChat", "makeHere", "open", "settings", "quit", "closedEyes"]);
        expect(document.querySelector("#menu")).toBeNull();
        expect(document.querySelector("#more")).toBeNull();
    });
    it("reads a brief with no recording instead of playing it", () => {
        const { document, postMessage } = page({ brief: { kind: "read", id: "evening", title: "Read", meta: "7:30 PM" } });
        (document.querySelector("#br") as HTMLButtonElement).click();
        expect(postMessage).toHaveBeenCalledWith({ command: "read:evening" });
    });
    // shell-review v1.0.0 B5: what's saved shows at once; until Isoform
    // knows, the section isn't there. Never placeholders.
    it("shows the saved Next row at once and leaves the section out until it knows", () => {
        const saved = page();
        expect(saved.document.querySelector("#wsec")!.hasAttribute("hidden")).toBe(false);
        expect(saved.document.querySelector(".it")!.textContent).toContain("title");
        expect(saved.document.querySelector(".it .ln")!.textContent).toBe("line");
        for (const state of [{ loading: true }, { withdrawn: true }, { waitingPending: true }, { error: "failed", next: null }]) {
            const { document } = page(state);
            expect(document.querySelector("#wsec")!.hasAttribute("hidden")).toBe(true);
            expect(document.querySelectorAll(".ph, .phb")).toHaveLength(0);
        }
    });
    // computer-settings v1.2.0: every slot says what's true. With nothing left
    // to answer, the slot says so and names the next brief; no count anywhere.
    it("shows one Next row with no count, and That's everything when nothing is left", () => {
        const listed = page({ waiting: 4 });
        expect(listed.document.querySelectorAll(".it")).toHaveLength(1);
        expect(listed.document.querySelector("#count")).toBeNull();
        expect(listed.document.querySelector("#answerRow")!.hasAttribute("hidden")).toBe(false);
        expect(listed.document.querySelector("#done")!.hasAttribute("hidden")).toBe(true);
        const none = page({ waiting: 0, items: [], next: null, nextBrief: "Next brief at 7:00 AM" });
        expect(none.document.querySelector("#wsec")!.hasAttribute("hidden")).toBe(false);
        expect(none.document.querySelector("#answerRow")!.hasAttribute("hidden")).toBe(true);
        expect(none.document.querySelector("#doneText")!.textContent).toBe("allDone");
        expect(none.document.querySelector("#doneNext")!.textContent).toBe("Next brief at 7:00 AM");
    });
    // While eyes are closed, the Next row stays answerable and says so; the
    // item's first line wraps instead of being cut.
    it("says you can still answer while eyes are closed, and wraps the item's first line", () => {
        const paused = page({ paused: true, status: "paused", state: "paused" });
        expect(paused.document.querySelector("#still")!.hasAttribute("hidden")).toBe(false);
        expect(paused.document.querySelector("#still")!.textContent).toBe("stillAnswer");
        expect(page().document.querySelector("#still")!.hasAttribute("hidden")).toBe(true);
        const css = template.slice(template.indexOf("<style"), template.indexOf("</style>"));
        const title = css.match(/\.it b \{[^}]*\}/)![0];
        expect(title).not.toMatch(/nowrap|ellipsis|height:56px/);
        expect(css).not.toMatch(/\.it \{ height:/);
    });

    it("shows no brief placeholder before the first read", () => {
        const { document } = page({ loading: true });
        expect(document.querySelector("#br")!.hasAttribute("hidden")).toBe(true);
        expect(document.querySelectorAll(".ph, .phb")).toHaveLength(0);
    });
    // B4: while today's brief is still being tried, the row is the tries line.
    it("shows the tries line as a row that does nothing", () => {
        const { document, postMessage } = page({ brief: { kind: "trying", title: "Tried 13 times without finishing. Isoform tries again at 4:35 PM." } });
        const row = document.querySelector("#br") as HTMLButtonElement;
        expect(row.disabled).toBe(true);
        expect(document.querySelector("#briefTitle")!.textContent).toBe("Tried 13 times without finishing. Isoform tries again at 4:35 PM.");
        row.click();
        expect(postMessage).not.toHaveBeenCalledWith(expect.objectContaining({ command: expect.stringMatching(/^(play|read):/) }));
        expect((document.querySelector("#make") as HTMLButtonElement).hidden).toBe(false);
    });
    it("offers the affected assistant sign-in in the brief line", () => {
        const { document, postMessage } = page({ brief: { kind: "sign_in", id: "codex", title: "Tried 3 times. Your AI is signed out." } }, { signIn: "Sign in" });
        const row = document.querySelector("#br") as HTMLButtonElement;
        expect(row.textContent).toContain("Tried 3 times. Your AI is signed out. Sign in");
        expect((document.querySelector("#make") as HTMLButtonElement).hidden).toBe(true);
        row.click();
        expect(postMessage).toHaveBeenCalledWith({ command: "signIn:codex" });
    });
    // B6: the footer link names the sheet it opens.
    it("labels the footer link with the sheet's name", () => {
        const { document } = page();
        expect(document.querySelector("#sees")!.textContent).toBe("onThisMac");
    });
    // Closed Eyes is the first control: it closes at once, then reads Open
    // eyes, filled, with its one shortcut shown while eyes are open.
    it("closes eyes at once from the header and then offers Open eyes, filled", () => {
        const { document, postMessage } = page({ closedEyesKey: "⌃⌥⌘E" });
        const hb = document.getElementById("hb") as HTMLButtonElement;
        expect(hb.textContent).toBe("closedEyes⌃⌥⌘E");
        hb.click();
        expect(postMessage).toHaveBeenCalledWith({ command: "closedEyes" });
        expect(document.getElementById("state")!.textContent).toBe("paused");
        expect(hb.textContent).toBe("openEyes⌃⌥⌘E");
        expect(hb.classList.contains("fill")).toBe(true);
        expect(hb.getAttribute("aria-pressed")).toBe("true");
        hb.click();
        expect(postMessage).toHaveBeenCalledWith({ command: "resume" });
        expect(hb.classList.contains("fill")).toBe(false);
    });
});
