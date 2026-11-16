import { useContext } from "react";
import { fireEvent, render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import i18n from "@/i18n";
import { SettingsAdvancedRevealContext } from "@/components/settings/settings-advanced";
import type { useKeybindings } from "@/hooks/useKeybindings";
import { SettingsPage } from "./settings-page";

// The pages are covered by their own tests; here each one is a marker so the
// shell (navigation, header and deep links) is tested alone.
vi.mock("@/components/settings/pages/overview-page", () => ({
    OverviewPage: () => <div data-testid="page">overview</div>,
}));
vi.mock("@/components/settings/pages/general-page", () => ({
    GeneralPage: () => <div data-testid="page">general</div>,
    AppearanceSettings: () => <div data-testid="page">appearance</div>,
}));
vi.mock("@/components/settings/pages/notifications-page", () => ({
    NotificationsPage: () => <div data-testid="page">notifications</div>,
}));
vi.mock("@/components/settings/pages/assistant-page", () => ({
    AssistantPage: () => <div data-testid="page">assistant</div>,
    AIProviderSettingsCard: () => null,
}));
vi.mock("@/components/settings/pages/observation-page", () => ({
    ObservationPage: ({ onOpenPage }: { onOpenPage?: (page: string) => void }) => (
        <div data-testid="page">
            observation
            <button type="button" onClick={() => onOpenPage?.("security")}>
                open-privacy
            </button>
        </div>
    ),
}));
vi.mock("@/components/settings/pages/closed-eyes-page", () => ({
    ClosedEyesPage: () => <div data-testid="page">closed-eyes</div>,
}));
vi.mock("@/components/settings/pages/learning-memory-page", () => ({
    LearningMemoryPage: ({
        onOpenMemory,
        onOpenPeople,
        onOpenProfile,
    }: {
        onOpenMemory: () => void;
        onOpenPeople: () => void;
        onOpenProfile: () => void;
    }) => (
        <div data-testid="page">
            memory
            <button type="button" onClick={onOpenMemory}>open-memory</button>
            <button type="button" onClick={onOpenPeople}>open-people</button>
            <button type="button" onClick={onOpenProfile}>open-profile</button>
        </div>
    ),
}));
vi.mock("@/components/settings/pages/privacy-page", () => ({
    PrivacyPage: () => <div data-testid="page">privacy</div>,
}));
vi.mock("@/components/settings/pages/iphone-page", () => ({
    IPhonePage: () => (
        <div data-testid="page">
            iphone{useContext(SettingsAdvancedRevealContext) ? ":advanced" : ""}
        </div>
    ),
}));
vi.mock("@/components/settings/pages/skills-page", () => ({
    SkillsPage: () => <div data-testid="page">skills</div>,
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
        vimMode: false,
        onVimModeChange: vi.fn(),
        keybindings: {} as ReturnType<typeof useKeybindings>,
        onOpenMemory: vi.fn(),
        onOpenPeople: vi.fn(),
        onOpenProfile: vi.fn(),
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
        expect(screen.getAllByText("Every source on one page, and off means nothing from it is kept.")).toHaveLength(1);
        expect(screen.getByTestId("page")).toHaveTextContent("observation");
        const sees = screen.getByRole("button", { name: "What Isoform sees" });
        expect(sees).toHaveAttribute("aria-current", "page");
        const closedEyes = screen.getByRole("button", { name: "Closed Eyes" });
        expect(sees.compareDocumentPosition(closedEyes) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
        fireEvent.click(closedEyes);
        expect(screen.getByTestId("page")).toHaveTextContent("closed-eyes");
    });

    it("keeps Overview as a page with its own title", () => {
        renderSettings("overview");
        expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent("How Isoform works for you");
        expect(screen.getByTestId("page")).toHaveTextContent("overview");
    });

    it("titles General with its own name and subtitle", () => {
        renderSettings("general");
        expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent("General");
        expect(
            screen.getAllByText(
                "Choose how Isoform starts and looks, which language it uses, and your keyboard shortcuts.",
            ),
        ).toHaveLength(1);
        expect(screen.getByTestId("page")).toHaveTextContent("general");
    });

    it("titles the About page with its own name", () => {
        renderSettings("about");
        expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent("About");
        expect(screen.getByTestId("page")).toHaveTextContent("help-about");
    });

    it.each(["text-messages", "phone-calls"] as const)("redirects the retired %s setup to Overview", (topic) => {
        renderSettings(topic);
        expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent("How Isoform works for you");
    });

    it("opens Plugins from its registered settings topic", () => {
        renderSettings("plugins");
        expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent("Plugins");
        expect(screen.getByTestId("page")).toHaveTextContent("plugins");
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

    it("returns to What Isoform sees after closing Appearance and reopening without a topic", () => {
        const props = baseProps();
        const { rerender } = render(<SettingsPage {...props} initialTopic="appearance" initialOptions={{}} />);
        expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent("Appearance");

        rerender(<SettingsPage {...props} active={false} initialTopic="appearance" initialOptions={{}} />);
        rerender(<SettingsPage {...props} active initialOptions={{}} />);
        expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent("What Isoform sees");
        expect(screen.getByTestId("page")).toHaveTextContent("observation");
    });

    it("returns to What Isoform sees for a no-topic open while Settings is already on Appearance", () => {
        const { rerender } = renderSettings("appearance");
        expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent("Appearance");

        rerender(<SettingsPage {...baseProps()} initialOptions={{}} />);
        expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent("What Isoform sees");
    });

    it("passes the knowledge links through to Learning and memory", () => {
        const onOpenMemory = vi.fn();
        const onOpenPeople = vi.fn();
        const onOpenProfile = vi.fn();
        renderSettings("memory", { onOpenMemory, onOpenPeople, onOpenProfile });

        fireEvent.click(screen.getByRole("button", { name: "open-memory" }));
        fireEvent.click(screen.getByRole("button", { name: "open-people" }));
        fireEvent.click(screen.getByRole("button", { name: "open-profile" }));
        expect(onOpenMemory).toHaveBeenCalledTimes(1);
        expect(onOpenPeople).toHaveBeenCalledTimes(1);
        expect(onOpenProfile).toHaveBeenCalledTimes(1);
    });

    it("lets a page open another page inside the dialog", () => {
        renderSettings("activity");
        fireEvent.click(screen.getByRole("button", { name: "open-privacy" }));
        expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent("Privacy");
    });

    it("opens a page found by an Advanced-only term with Advanced expanded", () => {
        renderSettings();
        const search = screen.getByRole("searchbox", { name: "Search settings" });
        fireEvent.change(search, { target: { value: "relay" } });
        fireEvent.keyDown(search, { key: "Enter" });
        expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent("Your phone");
        expect(screen.getByTestId("page")).toHaveTextContent("iphone:advanced");

        // Opening it from the sidebar keeps the remembered state.
        fireEvent.click(screen.getByRole("button", { name: "General" }));
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
        fireEvent.click(screen.getByRole("button", { name: "Privacy" }));
        first.unmount();

        renderSettings("iphone");
        expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent("Your phone");
    });
});
