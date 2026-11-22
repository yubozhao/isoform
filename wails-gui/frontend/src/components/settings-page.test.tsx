import { useContext } from "react";
import { fireEvent, render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import i18n from "@/i18n";
import { SettingsAdvancedRevealContext } from "@/components/settings/settings-advanced";
import { SettingsPage } from "./settings-page";

// The pages are covered by their own tests; here each one is a marker so the
// shell (navigation, header and deep links) is tested alone.
vi.mock("@/components/settings/pages/notifications-page", () => ({
    NotificationsPage: () => <div data-testid="page">notifications</div>,
}));
vi.mock("@/components/settings/pages/assistant-page", () => ({
    AssistantPage: () => <div data-testid="page">assistant</div>,
    AIProviderSettingsCard: () => null,
}));
vi.mock("@/components/settings/pages/observation-page", () => ({
    ObservationPage: ({ onOpenPage, onOpenPeople }: { onOpenPage?: (page: string) => void; onOpenPeople?: () => void }) => (
        <div data-testid="page">
            observation
            <button type="button" onClick={onOpenPeople}>
                open-people
            </button>
            <button type="button" onClick={() => onOpenPage?.("security")}>
                open-old-privacy
            </button>
        </div>
    ),
}));
vi.mock("@/components/settings/pages/closed-eyes-page", () => ({
    ClosedEyesPage: () => <div data-testid="page">closed-eyes</div>,
}));
vi.mock("@/components/settings/pages/memory-debug-page", () => ({
    MemoryDebugPage: () => <div data-testid="page">memory-debug</div>,
}));
vi.mock("@/components/settings/pages/iphone-page", () => ({
    IPhonePage: () => (
        <div data-testid="page">
            iphone{useContext(SettingsAdvancedRevealContext) ? ":advanced" : ""}
        </div>
    ),
}));
vi.mock("@/components/settings/pages/plugins-page", () => ({
    PluginsPage: () => <div data-testid="page">plugins</div>,
}));
vi.mock("@/components/settings/pages/tool-servers-page", () => ({
    ToolServersPage: () => <div data-testid="page">tool-servers</div>,
}));
vi.mock("@/components/settings/pages/chat-apps-page", () => ({
    ChatAppsPage: ({ selectedApp }: { selectedApp?: string }) => (
        <div data-testid="page">chat-apps:{selectedApp ?? "list"}</div>
    ),
}));
vi.mock("@/components/settings/pages/other-ai-apps-page", () => ({
    OtherAIAppsPage: () => <div data-testid="page">other-ai-apps</div>,
}));
vi.mock("@/components/settings/pages/help-about-page", () => ({
    HelpAboutPage: () => <div data-testid="page">help-about</div>,
}));

function baseProps() {
    return {
        onClose: vi.fn(),
        onReplayOnboarding: vi.fn(),
        onOpenPeople: vi.fn(),
    };
}

function renderSettings(
    initialTopic?: Parameters<typeof SettingsPage>[0]["initialTopic"],
    extra: Partial<Parameters<typeof SettingsPage>[0]> = {},
) {
    return render(<SettingsPage {...baseProps()} initialTopic={initialTopic} {...extra} />);
}

describe("SettingsPage", () => {
    beforeEach(async () => {
        await i18n.changeLanguage("en");
    });

    it("opens on What Isoform sees, first in the sidebar, above Closed Eyes", () => {
        renderSettings();
        expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent("What Isoform sees");
        expect(screen.getAllByText("Every source, on one page. Off means nothing from it is kept.")).toHaveLength(1);
        expect(screen.getByTestId("page")).toHaveTextContent("observation");
        const sees = screen.getByRole("button", { name: "What Isoform sees" });
        expect(sees).toHaveAttribute("aria-current", "page");
        const closedEyes = screen.getByRole("button", { name: "Closed Eyes" });
        expect(sees.compareDocumentPosition(closedEyes) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
        fireEvent.click(closedEyes);
        expect(screen.getByTestId("page")).toHaveTextContent("closed-eyes");
    });

    // Settings batch D1 (answers v1.0.0 T2, settings v1.0.0 r10): old links
    // to the pages that went land on the page that holds what stayed.
    it.each([
        ["overview", "What Isoform sees", "observation"],
        ["general", "About", "help-about"],
        ["profile", "About", "help-about"],
        ["appearance", "About", "help-about"],
        ["skills", "Your agents", "assistant"],
        ["read-aloud", "Briefs and notifications", "notifications"],
        ["memory", "What Isoform sees", "observation"],
    ] as const)("opens %s on %s, with no page of its own", (topic, title, page) => {
        renderSettings(topic);
        expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent(title);
        expect(screen.getByTestId("page")).toHaveTextContent(page);
    });

    it("lists none of the removed sections in the sidebar", () => {
        renderSettings();
        for (const label of ["Overview", "General", "Profile", "Appearance", "Skills", "Privacy", "Storage", "Learning and memory"]) {
            expect(screen.queryByRole("button", { name: label })).not.toBeInTheDocument();
        }
    });

    it("titles the About page with its own name", () => {
        renderSettings("about");
        expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent("About");
        expect(screen.getByTestId("page")).toHaveTextContent("help-about");
    });

    it.each([
        ["text-messages", "Briefs and notifications"],
        ["phone-calls", "What Isoform sees"],
    ] as const)("redirects the retired %s setup to %s", (topic, title) => {
        renderSettings(topic);
        expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent(title);
    });

    it("resolves legacy deep links to the page that holds their content", () => {
        const { rerender } = renderSettings("subscription");
        expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent("Your agents");

        const props = baseProps();
        // A deep link that arrives while Settings is open navigates there.
        rerender(<SettingsPage {...props} initialTopic="lark" />);
        expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent("Chat apps");
        expect(screen.getByTestId("page")).toHaveTextContent("chat-apps:lark");
        expect(screen.getByRole("button", { name: /Extensions/ })).toHaveAttribute("aria-expanded", "true");

    });

    it("returns to What Isoform sees after closing About and reopening without a topic", () => {
        const props = baseProps();
        const { rerender } = render(<SettingsPage {...props} initialTopic="about" initialOptions={{}} />);
        expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent("About");

        rerender(<SettingsPage {...props} active={false} initialTopic="about" initialOptions={{}} />);
        rerender(<SettingsPage {...props} active initialOptions={{}} />);
        expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent("What Isoform sees");
        expect(screen.getByTestId("page")).toHaveTextContent("observation");
    });

    it("returns to What Isoform sees for a no-topic open while Settings is already on About", () => {
        const { rerender } = renderSettings("about");
        expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent("About");

        rerender(<SettingsPage {...baseProps()} initialOptions={{}} />);
        expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent("What Isoform sees");
    });

    it("passes Who you know through to What Isoform sees for the Contacts row", () => {
        const onOpenPeople = vi.fn();
        renderSettings("activity", { onOpenPeople });
        fireEvent.click(screen.getByRole("button", { name: "open-people" }));
        expect(onOpenPeople).toHaveBeenCalledTimes(1);
    });

    it("lets a page open another page inside the dialog", () => {
        renderSettings("activity");
        // The old Privacy id lands on Your data (settings batch D2).
        fireEvent.click(screen.getByRole("button", { name: "open-old-privacy" }));
        expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent("Your data");
    });

    it("opens a page found by an Advanced-only term with Advanced expanded", () => {
        renderSettings();
        const search = screen.getByRole("searchbox", { name: "Search settings" });
        fireEvent.change(search, { target: { value: "relay" } });
        fireEvent.keyDown(search, { key: "Enter" });
        expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent("Your phone");
        expect(screen.getByTestId("page")).toHaveTextContent("iphone:advanced");

        // Opening it from the sidebar keeps the remembered state.
        fireEvent.click(screen.getByRole("button", { name: "About" }));
        fireEvent.click(screen.getByRole("button", { name: "Your phone" }));
        expect(screen.getByTestId("page")).toHaveTextContent(/^iphone$/);
    });

    it("shows Developer tools only while they are on, whatever the build", () => {
        const { unmount } = renderSettings(undefined, { devToolsUnlocked: false });
        expect(screen.queryByRole("button", { name: /Developer tools/ })).not.toBeInTheDocument();
        unmount();

        renderSettings(undefined, { devToolsUnlocked: true });
        expect(screen.getByRole("button", { name: /Developer tools/ })).toBeInTheDocument();
    });

    it("shows Memory and search, from Learning and memory, only with Developer tools on (settings batch D3)", () => {
        const on = renderSettings("memory-debug", { devToolsUnlocked: true });
        expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent("Memory and search");
        expect(screen.getByTestId("page")).toHaveTextContent("memory-debug");
        on.unmount();
        renderSettings("memory-debug");
        expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent("What Isoform sees");
        expect(screen.queryByRole("button", { name: "Memory and search" })).not.toBeInTheDocument();
    });

    it("does not open a Developer tools page while Developer tools are off", () => {
        renderSettings("notification-tests", { devToolsUnlocked: false });
        expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent("What Isoform sees");
    });

    it("warns under the browser preview switch that any local program can use it", () => {
        renderSettings("browser-bridge", { devToolsUnlocked: true });
        expect(
            screen.getByText(/any program on this computer can get its key/),
        ).toBeInTheDocument();
    });

    it("lets a deep link win over the remembered page", () => {
        const first = renderSettings();
        fireEvent.click(screen.getByRole("button", { name: "About" }));
        first.unmount();

        renderSettings("iphone");
        expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent("Your phone");
    });
});
