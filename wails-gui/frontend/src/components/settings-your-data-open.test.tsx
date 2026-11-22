import { useState } from "react";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import i18n from "@/i18n";
import en from "@/i18n/locales/en/settings.json";
import { Dialog } from "@/components/ui/dialog";
import { RetainedDialogContent } from "@/components/retained-dialog-content";
import { SettingsPage } from "./settings-page";

// Settings › Your data opens inside the Settings dialog and leaves it open
// (gap walk 2026-11-21 #33). The dialog stack is the one App.tsx mounts: the
// Radix Dialog, RetainedDialogContent and the real SettingsPage, with the
// real Your data page and its cards; the other pages are markers.
const mocks = vi.hoisted(() => ({
    marker: (name: string) => () => <div data-testid="page">{name}</div>,
    summary: vi.fn(async () => ({
        retentionDays: 30, ignoredApps: 3, handoffsThisWeek: 0, pairedDevices: 0, answers: 0,
        screenBytes: 0, audioBytes: 0, usedBytes: 0, capBytes: 0, messagesNotes: 0, messagesBytes: 0,
        watchBytes: 0, agentsHistoryItems: 0, agentsHistoryBytes: 0,
    })),
}));

vi.mock("@/lib/wails-shim-app", async (importOriginal) => ({
    ...(await importOriginal<Record<string, unknown>>()),
    GetYourDataSummary: mocks.summary,
    ForgetVoices: vi.fn(async () => {}),
    ExportAnswers: vi.fn(async () => true),
    DeleteAnswers: vi.fn(async () => {}),
    StartForgetEverything: vi.fn(async () => ({ state: "idle", steps: [] })),
    GetForgetEverything: vi.fn(async () => ({ state: "idle", steps: [] })),
}));
vi.mock("../../wailsjs/runtime/runtime", async (importOriginal) => ({
    ...(await importOriginal<Record<string, unknown>>()),
    EventsOn: vi.fn(() => () => {}),
}));
vi.mock("@/components/settings/pages/notifications-page", () => ({ NotificationsPage: mocks.marker("notifications") }));
vi.mock("@/components/settings/pages/assistant-page", () => ({
    AssistantPage: mocks.marker("assistant"),
    AIProviderSettingsCard: () => null,
}));
vi.mock("@/components/settings/pages/observation-page", () => ({ ObservationPage: mocks.marker("observation") }));
vi.mock("@/components/settings/pages/closed-eyes-page", () => ({ ClosedEyesPage: mocks.marker("closed-eyes") }));
vi.mock("@/components/settings/pages/iphone-page", () => ({ IPhonePage: mocks.marker("iphone") }));
vi.mock("@/components/settings/pages/plugins-page", () => ({ PluginsPage: mocks.marker("plugins") }));
vi.mock("@/components/settings/pages/tool-servers-page", () => ({ ToolServersPage: mocks.marker("tool-servers") }));
vi.mock("@/components/settings/pages/chat-apps-page", () => ({ ChatAppsPage: mocks.marker("chat-apps") }));
vi.mock("@/components/settings/pages/other-ai-apps-page", () => ({ OtherAIAppsPage: mocks.marker("other-ai-apps") }));
vi.mock("@/components/settings/pages/help-about-page", () => ({ HelpAboutPage: mocks.marker("help-about") }));

function SettingsDialog({ onClosed }: { onClosed: () => void }) {
    const [open, setOpen] = useState(true);
    const close = () => {
        setOpen(false);
        onClosed();
    };
    return (
        <Dialog open={open} onOpenChange={(next) => { if (!next) close(); }}>
            <RetainedDialogContent open={open} retain={false} page="settings" aria-describedby={undefined}>
                <SettingsPage
                    active={open}
                    onClose={close}
                    onReplayOnboarding={vi.fn()}
                    initialTopic="closed-eyes"
                    onOpenPeople={vi.fn()}
                />
            </RetainedDialogContent>
        </Dialog>
    );
}

describe("Settings › Your data in the Settings dialog", () => {
    beforeEach(async () => {
        await i18n.changeLanguage("en");
        mocks.summary.mockClear();
        globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
    });

    it("opens Your data from the sidebar and keeps Settings open", async () => {
        const onClosed = vi.fn();
        const user = userEvent.setup();
        render(<SettingsDialog onClosed={onClosed} />);
        expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent("Closed Eyes");

        await user.click(screen.getByRole("button", { name: en.pages["your-data"].label }));

        expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent(en.pages["your-data"].label);
        await screen.findByText(en.yourData.keeps);
        await waitFor(() => expect(mocks.summary).toHaveBeenCalled());
        expect(screen.getByRole("dialog")).toBeInTheDocument();
        expect(onClosed).not.toHaveBeenCalled();

        // The pages after it in the sidebar stay reachable from Your data.
        await user.click(screen.getByRole("button", { name: en.pages.notifications.label }));
        expect(screen.getByTestId("page")).toHaveTextContent("notifications");
        expect(onClosed).not.toHaveBeenCalled();
    });

    it("keeps Settings open when the window regains focus on Your data", async () => {
        const onClosed = vi.fn();
        const user = userEvent.setup();
        render(<SettingsDialog onClosed={onClosed} />);
        await user.click(screen.getByRole("button", { name: en.pages["your-data"].label }));
        await screen.findByText(en.yourData.keeps);

        window.dispatchEvent(new Event("blur"));
        window.dispatchEvent(new Event("focus"));

        await waitFor(() => expect(mocks.summary).toHaveBeenCalledTimes(2));
        expect(screen.getByRole("dialog")).toBeInTheDocument();
        expect(onClosed).not.toHaveBeenCalled();
    });
});
