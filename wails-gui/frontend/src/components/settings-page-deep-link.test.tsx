import { act, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import i18n from "@/i18n";
import { SettingsAdvanced, advancedStorageKey, settingsAnchor } from "@/components/settings/settings-advanced";
import { SettingsPage } from "./settings-page";

// How deep links and the sidebar search open a page's Advanced section. The
// pages are stand-ins built on the real SettingsAdvanced, so the reveal goes
// through the same context the real pages read.
const pages = vi.hoisted(() => ({
    marker: (name: string) => () => <div data-testid="page">{name}</div>,
}));

vi.mock("@/components/settings/pages/assistant-page", () => ({
    AssistantPage: () => (
        <SettingsAdvanced pageId="ai-provider">
            <p>Own API key</p>
        </SettingsAdvanced>
    ),
    AIProviderSettingsCard: () => null,
}));
vi.mock("@/components/settings/pages/iphone-page", () => ({
    IPhonePage: () => (
        <SettingsAdvanced pageId="iphone">
            <div {...settingsAnchor("server-address")}>Server address</div>
        </SettingsAdvanced>
    ),
}));
vi.mock("@/components/settings/pages/notifications-page", () => ({ NotificationsPage: pages.marker("notifications") }));
vi.mock("@/components/settings/pages/observation-page", () => ({
    ObservationPage: ({ anchor }: { anchor?: string }) => <div data-testid="page">{`observation ${anchor ?? ""}`.trim()}</div>,
}));
vi.mock("@/components/settings/pages/closed-eyes-page", () => ({ ClosedEyesPage: pages.marker("closed-eyes") }));
vi.mock("@/components/settings/pages/tool-servers-page", () => ({ ToolServersPage: pages.marker("tool-servers") }));
vi.mock("@/components/settings/pages/plugins-page", () => ({ PluginsPage: pages.marker("plugins") }));
vi.mock("@/components/settings/pages/chat-apps-page", () => ({
    // The list has no Advanced; each app's drill-in does.
    ChatAppsPage: ({ selectedApp, onSelectApp }: { selectedApp?: string; onSelectApp: (app?: string) => void }) =>
        selectedApp ? (
            <>
                <button type="button" onClick={() => onSelectApp(undefined)}>
                    Back to chat apps
                </button>
                <SettingsAdvanced pageId="chat-apps">
                    <p>Setup steps</p>
                </SettingsAdvanced>
            </>
        ) : (
            <>
                <button type="button" onClick={() => onSelectApp("slack")}>
                    Open Slack
                </button>
                <button type="button" onClick={() => onSelectApp("discord")}>
                    Open Discord
                </button>
            </>
        ),
}));
vi.mock("@/components/settings/pages/other-ai-apps-page", () => ({ OtherAIAppsPage: pages.marker("other-ai-apps") }));
vi.mock("@/components/settings/pages/help-about-page", () => ({ HelpAboutPage: pages.marker("help-about") }));

type Props = Parameters<typeof SettingsPage>[0];

function props(extra: Partial<Props> = {}): Props {
    return {
        onClose: vi.fn(),
        onReplayOnboarding: vi.fn(),
        onOpenPeople: vi.fn(),
        ...extra,
    };
}

function advancedToggle() {
    return screen.getByRole("button", { name: /^Advanced/ });
}

function search(query: string) {
    const box = screen.getByRole("searchbox", { name: "Search settings" });
    fireEvent.change(box, { target: { value: query } });
    fireEvent.keyDown(box, { key: "Enter" });
}

describe("SettingsPage Advanced deep links", () => {
    const scrolled: string[] = [];
    const originalScroll = Element.prototype.scrollIntoView;

    beforeEach(async () => {
        window.localStorage.clear();
        scrolled.length = 0;
        Element.prototype.scrollIntoView = function (this: Element) {
            scrolled.push(this.getAttribute("data-settings-anchor") ?? "");
        };
        await i18n.changeLanguage("en");
    });

    afterEach(() => {
        Element.prototype.scrollIntoView = originalScroll;
    });

    it("opens a deep link with Advanced expanded, again after the person collapsed it", () => {
        const first = { advanced: true };
        const { rerender } = render(<SettingsPage {...props()} initialTopic="ai-provider" initialOptions={first} />);
        expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent("Your agents");
        expect(advancedToggle()).toHaveAttribute("aria-expanded", "true");
        expect(screen.getByText("Own API key")).toBeInTheDocument();
        expect(scrolled).toEqual(["advanced"]);

        fireEvent.click(advancedToggle());
        expect(advancedToggle()).toHaveAttribute("aria-expanded", "false");

        // A re-render of the same link changes nothing ...
        rerender(<SettingsPage {...props()} initialTopic="ai-provider" initialOptions={first} />);
        expect(advancedToggle()).toHaveAttribute("aria-expanded", "false");

        // ... the same link asked for again (App passes a new object per
        // openSettings call) expands it again.
        rerender(<SettingsPage {...props()} initialTopic="ai-provider" initialOptions={{ advanced: true }} />);
        expect(advancedToggle()).toHaveAttribute("aria-expanded", "true");

        // The reveal is for that visit only; the collapse stays remembered.
        expect(window.localStorage.getItem(advancedStorageKey("ai-provider"))).toBe("0");
    });

    it("navigates again when the same page is asked for after moving away", () => {
        const { rerender } = render(<SettingsPage {...props()} initialTopic="iphone" initialOptions={{}} />);
        fireEvent.click(screen.getByRole("button", { name: "About" }));
        expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent("About");

        rerender(<SettingsPage {...props()} initialTopic="iphone" initialOptions={{}} />);
        expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent("Your phone");
        expect(advancedToggle()).toHaveAttribute("aria-expanded", "false");
    });

    it("re-opens Advanced for a repeated search on the page already open", () => {
        render(<SettingsPage {...props()} initialTopic="iphone" />);
        expect(advancedToggle()).toHaveAttribute("aria-expanded", "false");

        search("relay");
        expect(advancedToggle()).toHaveAttribute("aria-expanded", "true");

        fireEvent.click(advancedToggle());
        expect(advancedToggle()).toHaveAttribute("aria-expanded", "false");

        search("relay");
        expect(advancedToggle()).toHaveAttribute("aria-expanded", "true");
        expect(screen.getByText("Server address")).toBeInTheDocument();
    });

    it("opens About as it is for the old Keyboard shortcuts link", () => {
        // App.tsx sends `keybindings` to the Cmd+/ sheet (opensKeysSheet);
        // if it ever reaches Settings, About opens as it is.
        render(<SettingsPage {...props()} initialTopic="keybindings" />);
        expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent("About");
        expect(screen.getByTestId("page")).toHaveTextContent("help-about");
    });

    it("scrolls to a named anchor inside Advanced", () => {
        vi.useFakeTimers();
        try {
            render(
                <SettingsPage
                    {...props()}
                    initialTopic="iphone"
                    initialOptions={{ advanced: true, anchor: "server-address" }}
                />,
            );
            act(() => {
                vi.advanceTimersByTime(100);
            });
            expect(screen.getByText("Server address")).toBeInTheDocument();
            expect(scrolled).toEqual(["server-address"]);
        } finally {
            vi.useRealTimers();
        }
    });

    it("hands What Isoform sees its anchor, so the link opens that source's details", () => {
        const { rerender } = render(<SettingsPage {...props()} initialTopic="activity" initialOptions={{ anchor: "sees-calls" }} />);
        expect(screen.getByTestId("page")).toHaveTextContent("observation sees-calls");
        rerender(<SettingsPage {...props()} initialTopic="activity" initialOptions={{ anchor: "sees-messages" }} />);
        expect(screen.getByTestId("page")).toHaveTextContent("observation sees-messages");
    });

    it("leaves Advanced and the scroll alone for a plain link", () => {
        render(<SettingsPage {...props()} initialTopic="iphone" initialOptions={{}} />);
        expect(advancedToggle()).toHaveAttribute("aria-expanded", "false");
        expect(scrolled).toEqual([]);
    });

    it("carries a search for a chat app's Advanced term into the app opened from the list", () => {
        render(<SettingsPage {...props()} />);
        search("Socket Mode");
        expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent("Chat apps");

        fireEvent.click(screen.getByRole("button", { name: "Open Slack" }));
        expect(advancedToggle()).toHaveAttribute("aria-expanded", "true");
        expect(screen.getByText("Setup steps")).toBeInTheDocument();

        // Back on the list and into another app is an ordinary visit.
        fireEvent.click(screen.getByRole("button", { name: "Back to chat apps" }));
        fireEvent.click(screen.getByRole("button", { name: "Open Discord" }));
        expect(advancedToggle()).toHaveAttribute("aria-expanded", "false");
    });

    it("opens a chat app from a plain visit to the list with Advanced as remembered", () => {
        render(<SettingsPage {...props()} initialTopic="chat-apps" />);
        fireEvent.click(screen.getByRole("button", { name: "Open Slack" }));
        expect(advancedToggle()).toHaveAttribute("aria-expanded", "false");
    });
});
