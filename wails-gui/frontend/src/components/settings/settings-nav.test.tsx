import { fireEvent, render, screen, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import i18n from "@/i18n";
import { SettingsNav } from "@/components/settings/settings-nav";
import componentSource from "./settings-nav.tsx?raw";

function renderNav(props: Partial<Parameters<typeof SettingsNav>[0]> = {}) {
    const onSelect = vi.fn();
    const result = render(
        <div className="settings-surface">
            <SettingsNav activePage="general" onSelect={onSelect} showDevTools={false} {...props} />
        </div>,
    );
    return { ...result, onSelect };
}

describe("SettingsNav", () => {
    beforeEach(async () => {
        await i18n.changeLanguage("en");
    });

    it.each(["en", "zh-CN"])("omits future channels from navigation and search in %s", async (language) => {
        await i18n.changeLanguage(language);
        renderNav();
        for (const label of ["Text messages", "Phone calls", "短信", "电话"]) {
            expect(screen.queryByRole("button", { name: label })).not.toBeInTheDocument();
        }
        fireEvent.change(screen.getByRole("searchbox"), { target: { value: "phone calls" } });
        expect(screen.queryByRole("button", { name: /Phone calls/ })).not.toBeInTheDocument();
    });

    it.each([
        ["en", "Agent of You", "Reservations open"],
        ["zh-CN", "出租智能体", "预留已开放"],
    ])("tags Agent of You with Reservations open in %s, under its full name (ST24)", async (language, label, tag) => {
        await i18n.changeLanguage(language);
        renderNav();
        const row = screen.getByRole("button", { name: new RegExp(label) });
        expect(within(row).getByText(label)).toBeInTheDocument();
        expect(within(row).getByText(tag)).toBeInTheDocument();
        expect(row).not.toHaveTextContent(/Soon|即将推出/);
    });

    it("groups pages under explained headings with Extensions collapsed", () => {
        renderNav();

        for (const label of ["What Isoform sees", "Closed Eyes", "Your agents", "Your phone", "Briefs and notifications", "Overview", "General", "Learning and memory", "Privacy", "About"]) {
            expect(screen.getByRole("button", { name: label })).toBeInTheDocument();
        }
        expect(screen.getByText("Personal AI")).toBeInTheDocument();
        expect(
            screen.getByText(
                "How Isoform thinks, what it notices and remembers, how it uses your apps and what stays private.",
            ),
        ).toBeInTheDocument();

        const extensions = screen.getByRole("button", { name: /Extensions/ });
        expect(extensions).toHaveAttribute("aria-expanded", "false");
        expect(extensions).toHaveTextContent(
            "Optional add-ons that give Isoform more skills, tools and places to reach you.",
        );
        expect(screen.queryByRole("button", { name: "Skills" })).not.toBeInTheDocument();

        fireEvent.click(extensions);
        for (const label of ["Plugins", "Skills", "Tool servers", "Chat apps", "Other AI apps"]) {
            expect(screen.getByRole("button", { name: label })).toBeInTheDocument();
        }
    });

    it("opens Extensions when the active page is inside it", () => {
        renderNav({ activePage: "chat-apps" });
        expect(screen.getByRole("button", { name: /Extensions/ })).toHaveAttribute("aria-expanded", "true");
        expect(screen.getByRole("button", { name: "Chat apps" })).toHaveAttribute("data-active", "true");
    });

    it("marks the active row with data-active only", () => {
        renderNav({ activePage: "memory" });
        expect(screen.getByRole("button", { name: "Learning and memory" })).toHaveAttribute("data-active", "true");
        expect(screen.getByRole("button", { name: "General" })).toHaveAttribute("data-active", "false");
        // CLAUDE.md rule 12: no left-edge tree lines or accent bars.
        expect(componentSource).not.toMatch(/border-l\b|border-l-|w-0\.5|w-\[2px\]/);
    });

    it("filters pages by keyword and opens the first match on Enter", () => {
        const { onSelect } = renderNav();
        const search = screen.getByRole("searchbox", { name: "Search settings" });

        fireEvent.change(search, { target: { value: "theme" } });
        expect(screen.queryByText("Personal AI")).not.toBeInTheDocument();
        expect(screen.getByRole("button", { name: "General" })).toBeInTheDocument();
        expect(screen.queryByRole("button", { name: "Privacy" })).not.toBeInTheDocument();

        fireEvent.change(search, { target: { value: "mcp" } });
        fireEvent.keyDown(search, { key: "Enter" });
        // "mcp" matched the page's description, not its name, so the page
        // opens with Advanced expanded.
        expect(onSelect).toHaveBeenCalledWith("mcp", { advanced: true });
        expect(search).toHaveValue("");
    });

    it("opens a page found by name without expanding Advanced", () => {
        const { onSelect } = renderNav();
        const search = screen.getByRole("searchbox", { name: "Search settings" });

        fireEvent.change(search, { target: { value: "phone" } });
        // Notifications also mentions the phone; the page named Your phone wins.
        const rows = screen.getAllByRole("button").map((button) => button.textContent);
        expect(rows.indexOf("Your phone")).toBeGreaterThan(-1);
        expect(rows.indexOf("Your phone")).toBeLessThan(rows.indexOf("Briefs and notifications"));
        fireEvent.keyDown(search, { key: "Enter" });
        expect(onSelect).toHaveBeenCalledWith("iphone");
    });

    it("finds a page by what it keeps under Advanced and reveals it", () => {
        const { onSelect } = renderNav();
        const search = screen.getByRole("searchbox", { name: "Search settings" });

        fireEvent.change(search, { target: { value: "relay" } });
        fireEvent.click(screen.getByRole("button", { name: "Your phone" }));
        expect(onSelect).toHaveBeenCalledWith("iphone", { advanced: true });
    });

    it("says when nothing matches", () => {
        renderNav();
        fireEvent.change(screen.getByRole("searchbox", { name: "Search settings" }), {
            target: { value: "zzzz-no-such-setting" },
        });
        expect(screen.getByText("No matching settings")).toBeInTheDocument();
    });

    it("shows Developer tools only when unlocked", () => {
        renderNav();
        expect(screen.queryByRole("button", { name: /Developer tools/ })).not.toBeInTheDocument();

        renderNav({ showDevTools: true, activePage: "notification-tests" });
        const devTools = screen.getByRole("button", { name: /Developer tools/ });
        expect(devTools).toHaveAttribute("aria-expanded", "true");
        const group = devTools.parentElement as HTMLElement;
        expect(within(group).getByRole("button", { name: "Notification checks" })).toHaveAttribute(
            "data-active",
            "true",
        );
    });
});
