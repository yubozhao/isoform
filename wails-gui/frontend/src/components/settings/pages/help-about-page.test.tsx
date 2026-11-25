import {
    act,
    fireEvent,
    render,
    screen,
    waitFor,
    within,
} from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { AboutInfo } from "@/lib/wails-shim-app";

const mocks = vi.hoisted(() => ({
    getAbout: vi.fn(),
    getVersion: vi.fn(),
    check: vi.fn(),
    download: vi.fn(),
    restart: vi.fn(),
    background: vi.fn(),
    saveLogs: vi.fn(),
    openFolder: vi.fn(),
    openURL: vi.fn(),
    getStartAtLogin: vi.fn(),
    modelChoices: vi.fn(),
    planModels: vi.fn(),
    getTelemetry: vi.fn(),
    setTelemetry: vi.fn(),
    handlers: {} as Record<string, (payload: unknown) => void>,
}));
vi.mock("@/lib/wails-shim-app", () => ({
    GetAboutInfo: mocks.getAbout,
    GetVersion: mocks.getVersion,
    CheckForUpdates: mocks.check,
    DownloadUpdate: mocks.download,
    RestartToUpdate: mocks.restart,
    SetBackgroundUpdates: mocks.background,
    SaveSupportLogs: mocks.saveLogs,
    OpenDataFolder: mocks.openFolder,
    OpenExternalURL: mocks.openURL,
    GetPerfSnapshot: vi.fn(),
    GetStartAtLogin: mocks.getStartAtLogin,
    SetStartAtLogin: vi.fn(async () => undefined),
    EnsureSelfProfile: vi.fn(async () => undefined),
    GetUserProfile: vi.fn(async () => ({ name: "Bo", display_name: "Bo" })),
    GetModelChoices: mocks.modelChoices,
    PlanSetupModelDownload: mocks.planModels,
    GetTelemetryTier: mocks.getTelemetry,
    SetTelemetryTier: mocks.setTelemetry,
}));
vi.mock("@/components/onboarding/models-panel", () => ({
    ModelsPanel: () => <div data-testid="about-models-panel" />,
}));
vi.mock("@/hooks/useTheme", () => ({
    useTheme: () => ({ preference: "system", setPreference: vi.fn() }),
}));
vi.mock("../../../../wailsjs/runtime/runtime", () => ({
    EventsOn: vi.fn((name: string, handler: (payload: unknown) => void) => {
        mocks.handlers[name] = handler;
        return () => {
            delete mocks.handlers[name];
        };
    }),
}));
import i18n from "@/i18n";
import en from "@/i18n/locales/en/settings.json";
import { HelpAboutPage } from "./help-about-page";

let info: AboutInfo;
const model = (id: string, job: string, sizeBytes: number) => ({
    id, job, displayName: id, line: "", evidence: "", sizeBytes, files: [], sources: [],
    licence: { name: "", url: "" }, recommended: true, languages: [], downloaded: false,
});
const modelChoices = {
    jobs: [
        { job: "privacy", chosenId: "pii", alternatives: [model("pii", "privacy", 400e6)], locked: true },
        { job: "memory", chosenId: "embed", alternatives: [model("embed", "memory", 600e6)], locked: true },
        { job: "live", chosenId: "asr", alternatives: [model("asr", "live", 2e9)], locked: false },
    ],
    totalBytesToDownload: 0,
    freeBytes: 50e9,
    modelsFolder: "",
};
const emptyPlan = {
    planId: "", choices: {}, requiredBytes: 0, items: [] as unknown[], totalBytes: 0,
    freeBytes: 50e9, enoughSpace: true, modelsFolder: "",
};
async function status(update: AboutInfo["update"]) {
    info = { ...info, update };
    await act(async () => {
        mocks.handlers["update:status"]?.(update);
    });
}

describe("Settings About", () => {
    beforeEach(async () => {
        vi.clearAllMocks();
        localStorage.clear();
        await i18n.changeLanguage("en");
        info = {
            version: "0.1.301",
            build: "staging",
            buildTime: "2026-10-22T17:42:00Z",
            computer: "Mac mini",
            os: "macOS 26.1",
            phones: ["My phone"],
            dataBytes: 1.4e9,
            backgroundUpdates: true,
            lastChecked: Date.now(),
            later: false,
            releaseNotesVersion: "0.1.301",
            releaseNotes: "",
            releaseNotesUrl: "https://example.com/releases",
            update: { status: "up-to-date" },
        };
        mocks.getAbout.mockImplementation(async () => info);
        mocks.getVersion.mockResolvedValue("0.1.301");
        mocks.check.mockResolvedValue({ available: false });
        mocks.restart.mockResolvedValue(undefined);
        mocks.download.mockResolvedValue(undefined);
        mocks.getStartAtLogin.mockResolvedValue(true);
        mocks.modelChoices.mockResolvedValue(modelChoices);
        mocks.planModels.mockResolvedValue({ ...emptyPlan });
        mocks.getTelemetry.mockResolvedValue("detailed");
        mocks.setTelemetry.mockResolvedValue(undefined);
        mocks.background.mockImplementation(async (enabled) => {
            info = { ...info, backgroundUpdates: enabled };
        });
    });
    it.each(["en", "zh-CN"])("shows a readable build date and an ink Discord action without badge helpers in %s", async (language) => {
        await i18n.changeLanguage(language);
        info.buildTime = new Date(2026, 9, 22, 22, 22).toISOString();
        render(<HelpAboutPage onReplayOnboarding={vi.fn()} />);
        const build = await screen.findByText(i18n.t("settings:aboutPolish.build"));
        const row = build.closest("[data-about-row]") ?? build.parentElement!;
        expect(row.textContent).toBe(language === "en" ? "BuildStaging · Oct 22 at 10:22 PM" : "构建预发布版 · 10月22日 22:22");
        fireEvent.click(screen.getByRole("button", { name: /Advanced|高级/ }));
        const discord = screen.getByRole("button", { name: i18n.t("settings:support.discordButton") });
        expect(discord).toHaveClass("text-ink", "border-border", "bg-background");
        expect(discord.querySelector("svg")).toHaveAttribute("fill", "currentColor");
    });
    it("shows the legal wording and links together in Chinese", async () => {
        await i18n.changeLanguage("zh-CN");
        render(<HelpAboutPage onReplayOnboarding={vi.fn()} />);
        expect(await screen.findByText("Isoform 根据 Functional Source License 开源。")).toBeInTheDocument();
        const license = screen.getByRole("button", { name: i18n.t("settings:aboutPolish.license") });
        const privacy = screen.getByRole("button", { name: i18n.t("settings:aboutPolish.privacy") });
        expect(license.parentElement).toBe(privacy.parentElement);
        expect(license.parentElement).toHaveClass("whitespace-nowrap");
    });
    it("shows board rows, build details, paired phone, Get help with its email row outside Advanced (ST26), and recovery controls", async () => {
        const setup = vi.fn();
        render(<HelpAboutPage onReplayOnboarding={setup} />);
        await screen.findByText("Isoform for your computer");
        for (const text of [
            "What is new",
            "Build",
            "Computer",
            "Data",
            "Support",
            "Legal",
        ])
            expect(screen.getByText(text)).toBeInTheDocument();
        expect(
            screen.getByText(/Mac mini · macOS 26.1 · paired with My phone/),
        ).toBeInTheDocument();
        expect(screen.getByText("Isoform is open source under the Functional Source License.")).toBeInTheDocument();
        const license = screen.getByRole("button", { name: "License" });
        const privacy = screen.getByRole("button", { name: "Privacy" });
        expect(license.parentElement).toBe(privacy.parentElement);
        expect(license.parentElement).toHaveClass("whitespace-nowrap");
        expect(screen.getByText(/1.3 GB/)).toBeInTheDocument();
        expect(screen.getByText("Email support")).toBeInTheDocument();
        expect(
            screen.getByText("Write to bo@isoform.ai. Your mail app opens a draft with your version."),
        ).toBeInTheDocument();
        expect(screen.getByRole("button", { name: "Send feedback" })).toBeInTheDocument();
        expect(
            screen.getByRole("button", { name: "Copy report" }),
        ).toBeInTheDocument();
        expect(
            screen.getByRole("button", { name: "Join Discord" }),
        ).toBeInTheDocument();
        fireEvent.click(screen.getByRole("button", { name: /Advanced/ }));
        fireEvent.click(
            screen.getByRole("button", { name: "Run setup again" }),
        );
        expect(setup).toHaveBeenCalledOnce();

        expect(
            screen.getByRole("heading", {
                name: "What the support report includes",
            }),
        ).toBeInTheDocument();
        fireEvent.click(screen.getByRole("button", { name: "Open folder" }));
        await waitFor(() => expect(mocks.openFolder).toHaveBeenCalledOnce());
        fireEvent.click(
            screen.getByRole("button", { name: "Save logs for support" }),
        );
        await waitFor(() => expect(mocks.saveLogs).toHaveBeenCalledOnce());
        fireEvent.click(screen.getByRole("button", { name: "Release notes" }));
        await waitFor(() =>
            expect(mocks.openURL).toHaveBeenCalledWith(info.releaseNotesUrl),
        );
    });
    it("uses the theme accent for the About mark", async () => {
        render(<HelpAboutPage onReplayOnboarding={vi.fn()} />);
        const title = await screen.findByRole("heading", {
            name: "Isoform for your computer",
        });
        const mark = title.closest("section")?.querySelector(
            'svg[viewBox="0 0 19 19"]',
        );
        expect(mark).toHaveClass("text-accent");
        expect(mark).not.toHaveClass("text-ink");
    });
    it.each(["update-available", "downloading", "update-downloaded"])(
        "shows the selected release summary and its own notes link while %s, even with a newer upstream release",
        async (state) => {
            const notesURL =
                "https://github.com/example/isoform-releases/releases/tag/v0.1.302";
            Object.assign(info, {
                releaseNotesVersion: "0.1.302",
                releaseNotes:
                    "Today with **topics**, the pinned main chat panel, one Search with commands.",
                releaseNotesUrl: notesURL,
                update: { status: state, version: "0.1.302", percent: 42 },
            });
            // The feed has moved on; reading About must use the selected metadata.
            mocks.check.mockResolvedValue({
                available: true,
                latest: "0.1.303",
                release_notes: "Different upstream notes.",
            });
            render(<HelpAboutPage onReplayOnboarding={vi.fn()} />);
            const row = (await screen.findByText("What is new")).parentElement!;
            expect(within(row).getByText("topics")).toHaveProperty(
                "tagName",
                "STRONG",
            );
            expect(within(row).getByText("topics").closest(".prose")).not.toBeNull();
            expect(row).toHaveTextContent(
                "v0.1.302: Today with topics, the pinned main chat panel, one Search with commands.",
            );
            expect(screen.queryByText("Different upstream notes.")).not.toBeInTheDocument();
            fireEvent.click(within(row).getByRole("button", { name: "Release notes" }));
            await waitFor(() => expect(mocks.openURL).toHaveBeenCalledWith(notesURL));
            expect(mocks.check).not.toHaveBeenCalled();
        },
    );
    it("checks, downloads without restarting, and only restarts on the explicit action", async () => {
        render(<HelpAboutPage onReplayOnboarding={vi.fn()} />);
        expect(
            await screen.findByText(/Up to date. Checked today at/),
        ).toBeInTheDocument();
        fireEvent.click(
            screen.getByRole("button", { name: "Check now" }),
        );
        await waitFor(() => expect(mocks.check).toHaveBeenCalledOnce());
        await status({ status: "checking" });
        expect(screen.getByText("Checking for updates…")).toBeInTheDocument();
        expect(
            screen.getByRole("button", { name: "Checking…" }),
        ).toBeDisabled();
        await status({ status: "update-available", version: "0.1.302" });
        fireEvent.click(
            screen.getByRole("button", { name: "Download update" }),
        );
        await waitFor(() => expect(mocks.download).toHaveBeenCalledOnce());
        expect(mocks.restart).not.toHaveBeenCalled();
        await status({
            status: "downloading",
            version: "0.1.302",
            percent: 42,
        });
        expect(screen.getByText(/42%/)).toBeInTheDocument();
        expect(screen.getByRole("progressbar")).toHaveAttribute(
            "aria-valuenow",
            "42",
        );
        await status({ status: "update-downloaded", version: "0.1.302" });
        expect(
            screen.getByText("Version 0.1.301"),
        ).toBeInTheDocument();
        // With the sidebar card gone (answers v1.0.0 S3), About is the one
        // place that says an update is ready: "{version} is ready · Restart now".
        expect(screen.getByTestId("about-update-ready")).toHaveTextContent(
            "0.1.302 is ready · Restart now",
        );
        fireEvent.click(
            screen.getByRole("button", { name: "Restart now" }),
        );
        expect(screen.getByTestId("about-update-ready")).toHaveTextContent("Restarting…");
        await waitFor(() => expect(mocks.restart).toHaveBeenCalledOnce());
        expect(screen.queryByRole("button", { name: "Restart now" })).not.toBeInTheDocument();
    });
    it("persists the background switch and keeps failures visible and retryable", async () => {
        render(<HelpAboutPage onReplayOnboarding={vi.fn()} />);
        const toggle = await screen.findByRole("switch");
        expect(toggle).toBeChecked();
        fireEvent.click(toggle);
        await waitFor(() =>
            expect(mocks.background).toHaveBeenCalledWith(false),
        );
        await waitFor(() => expect(toggle).not.toBeChecked());
        mocks.check.mockRejectedValue(new Error("network"));
        fireEvent.click(
            screen.getByRole("button", { name: "Check now" }),
        );
        expect(await screen.findByRole("alert")).toHaveTextContent(
            "This action could not finish",
        );
        expect(
            screen.getByRole("button", { name: "Check now" }),
        ).toBeEnabled();
    });
    it("explains the release-only rule without claiming staging is up to date", async () => {
        info.update = { status: "disabled" };
        render(<HelpAboutPage onReplayOnboarding={vi.fn()} />);
        expect(
            await screen.findByText(
                "Update checks are available in release builds.",
            ),
        ).toBeInTheDocument();
        expect(
            screen.getByRole("button", { name: "Check now" }),
        ).toBeDisabled();
        expect(mocks.check).not.toHaveBeenCalled();
    });
    it("refreshes the saved background policy without overwriting newer download progress", async () => {
        info.backgroundUpdates = false;
        info.update = { status: "update-available", version: "0.1.302" };
        render(<HelpAboutPage onReplayOnboarding={vi.fn()} />);
        const toggle = await screen.findByRole("switch");
        let finishMetadata: (value: AboutInfo) => void = () => {};
        mocks.getAbout.mockImplementationOnce(
            () =>
                new Promise<AboutInfo>((resolve) => {
                    finishMetadata = resolve;
                }),
        );
        fireEvent.click(toggle);
        await waitFor(() => expect(mocks.getAbout).toHaveBeenCalledTimes(2));
        await status({
            status: "downloading",
            version: "0.1.302",
            percent: 42,
        });
        await act(async () => {
            finishMetadata({
                ...info,
                backgroundUpdates: true,
                update: { status: "update-available", version: "0.1.302" },
            });
        });
        expect(toggle).toBeChecked();
        expect(screen.getByRole("progressbar")).toHaveAttribute(
            "aria-valuenow",
            "42",
        );
    });
    it("names a staging build by its short commit and a release build never by one (ST85)", async () => {
        info = { ...info, build: "staging", commit: "d1282d52", buildTime: new Date(2026, 9, 22, 22, 22).toISOString() };
        render(<HelpAboutPage onReplayOnboarding={vi.fn()} />);
        const build = await screen.findByText("Build");
        const row = build.closest("[data-about-row]") ?? build.parentElement!;
        expect(row.textContent).toBe("BuildStaging · d1282d52 · Oct 22 at 10:22 PM");
    });
    it("names a release build by the version people know, Isoform 1.0.32 (ST85)", async () => {
        info = { ...info, version: "1.0.32", build: "release", releaseNotesVersion: "1.0.32", commit: "d1282d52" };
        render(<HelpAboutPage onReplayOnboarding={vi.fn()} />);
        expect(await screen.findByText("Isoform 1.0.32")).toBeInTheDocument();
        expect(screen.queryByText(/d1282d52/)).toBeNull();
        expect(screen.queryByText(/v0\.1\.301/)).toBeNull();
    });
    it("keeps the version readable when richer About metadata is unavailable", async () => {
        mocks.getAbout.mockRejectedValue(
            new Error("native metadata unavailable"),
        );
        render(<HelpAboutPage onReplayOnboarding={vi.fn()} />);
        expect(await screen.findByText("Version 0.1.301")).toBeInTheDocument();
        expect(
            screen.getByRole("button", { name: "Retry" }),
        ).toBeInTheDocument();
    });
    it("offers a retry when About metadata fails", async () => {
        mocks.getAbout.mockRejectedValue(new Error("offline"));
        render(<HelpAboutPage onReplayOnboarding={vi.fn()} />);
        fireEvent.click(await screen.findByRole("button", { name: "Retry" }));
        await waitFor(() => expect(mocks.getAbout).toHaveBeenCalledTimes(2));
    });
    it("renders the same ready state and controls in Chinese", async () => {
        await i18n.changeLanguage("zh-CN");
        info.releaseNotesVersion = "0.1.302";
        info.releaseNotes = "Today with **topics**.";
        info.update = { status: "update-downloaded", version: "0.1.302" };
        render(<HelpAboutPage onReplayOnboarding={vi.fn()} />);
        expect(
            await screen.findByText("版本 0.1.301"),
        ).toBeInTheDocument();
        expect(screen.getByText("新功能").parentElement).toHaveTextContent(
            "v0.1.302： Today with topics.",
        );
        expect(
            screen.getByRole("button", { name: "立即重启" }),
        ).toBeInTheDocument();
        expect(
            screen.getByRole("switch", {
                name: "自动安装更新",
            }),
        ).toBeChecked();
        expect(
            screen.getByRole("button", { name: "保存支持日志" }),
        ).toBeInTheDocument();
    });

    describe("About's rows (answers v1.0.0 T2, v1.2.0 S11)", () => {
        const platform = navigator.platform;
        beforeEach(() => {
            Object.defineProperty(navigator, "platform", { value: "MacIntel", configurable: true });
        });
        afterEach(() => {
            Object.defineProperty(navigator, "platform", { value: platform, configurable: true });
        });

        it("holds Your name, Open at login, Language, Appearance, Version and updates, the models, then Help, in that order and in the design's words", async () => {
            const { container } = render(<HelpAboutPage onReplayOnboarding={vi.fn()} />);
            await screen.findByText("Isoform for your computer");
            await screen.findByRole("switch", { name: "Open at login" });
            await screen.findByText(/On this computer: models for privacy and for finding things · /);
            const words = [
                "Your name · used in your briefs and in hand-offs to your agents",
                "Open at login",
                "Language",
                "Appearance",
                "Isoform for your computer",
                "Install updates automatically",
                "On this computer: models for privacy and for finding things · 1.0 GB",
                "Help and Send feedback",
            ];
            const text = container.textContent ?? "";
            const at = words.map((word) => text.indexOf(word));
            expect(at.every((index) => index >= 0)).toBe(true);
            expect([...at].sort((a, b) => a - b)).toEqual(at);
            expect(screen.getByRole("switch", { name: "Open at login" })).toBeChecked();
            expect(screen.getByRole("switch", { name: "Install updates automatically" })).toBeChecked();
            expect(screen.getByRole("button", { name: "Check now" })).toBeInTheDocument();
            expect(screen.queryByText("Reading width")).not.toBeInTheDocument();
        });

        it("says what the privacy and search models wait for when they are short of disk", async () => {
            mocks.planModels.mockResolvedValue({
                ...emptyPlan,
                items: [{ id: "pii" }, { id: "embed" }],
                totalBytes: 1e9,
                freeBytes: 200e6,
                enoughSpace: false,
            });
            render(<HelpAboutPage onReplayOnboarding={vi.fn()} />);
            expect(await screen.findByTestId("about-models-short")).toHaveTextContent(
                /^Privacy and search models need 1(\.0)? GB free · 200 MB free now$/,
            );
            // Planned without the speech jobs: only privacy and memory count.
            expect(mocks.planModels).toHaveBeenCalledWith(
                expect.anything(),
                ["live", "speakerPack", "afterCall", "voice"],
            );
        });

        it("shows a failed models read as an error with Try again, never an empty line", async () => {
            mocks.modelChoices.mockRejectedValueOnce(new Error("down"));
            render(<HelpAboutPage onReplayOnboarding={vi.fn()} />);
            const models = await screen.findByTestId("about-models");
            await waitFor(() => expect(models).toHaveTextContent("Couldn't read this setting."));
            fireEvent.click(within(models).getByRole("button", { name: "Try again" }));
            expect(await screen.findByText(/On this computer: models for privacy and for finding things/)).toBeInTheDocument();
        });

        it("shows a failed Open at login read as an error with Try again, never a false Off", async () => {
            mocks.getStartAtLogin.mockRejectedValueOnce(new Error("down"));
            render(<HelpAboutPage onReplayOnboarding={vi.fn()} />);
            expect(await screen.findAllByText("Couldn't read this setting.")).not.toHaveLength(0);
            expect(screen.queryByRole("switch", { name: "Open at login" })).not.toBeInTheDocument();
        });

        it("shows a downloaded update as '{version} is ready · Restart now' and restarts on the click", async () => {
            info.update = { status: "update-downloaded", version: "1.2.0" };
            render(<HelpAboutPage onReplayOnboarding={vi.fn()} />);
            expect(await screen.findByTestId("about-update-ready")).toHaveTextContent("1.2.0 is ready · Restart now");
            fireEvent.click(screen.getByRole("button", { name: "Restart now" }));
            await waitFor(() => expect(mocks.restart).toHaveBeenCalledOnce());
        });
    });

    describe("what came from Privacy and Storage (settings batch D2)", () => {
        it("has no usage-counts switch: it lives on Your data's What leaves row (batch F, T13)", async () => {
            render(<HelpAboutPage onReplayOnboarding={vi.fn()} />);
            await screen.findByText("Isoform for your computer");
            expect(screen.queryByRole("switch", { name: "Share diagnostic data" })).not.toBeInTheDocument();
            expect(screen.queryByRole("switch", { name: /Usage counts/ })).not.toBeInTheDocument();
        });

        it("has no Ask before risky commands: Chat runs no commands (computer-conformance-answers v1.4.0 S25)", async () => {
            render(<HelpAboutPage onReplayOnboarding={vi.fn()} />);
            await screen.findByText("Isoform for your computer");
            fireEvent.click(screen.getByRole("button", { name: /Advanced/ }));
            await screen.findByText("What diagnostic data includes");
            expect(screen.queryByText(/risky commands/i)).not.toBeInTheDocument();
            expect(en.privacy).not.toHaveProperty("riskyCommands");
        });

        it("keeps what diagnostic data includes under Advanced, and the models panel out of it", async () => {
            render(<HelpAboutPage onReplayOnboarding={vi.fn()} />);
            await screen.findByText("Isoform for your computer");
            fireEvent.click(screen.getByRole("button", { name: /Advanced/ }));
            expect(screen.getByText("What diagnostic data includes")).toBeInTheDocument();
            expect(screen.queryByTestId("about-models-panel")).not.toBeInTheDocument();
        });

        it("opens Models › from the models line, with a way back to About (answers v1.5.0 S21)", async () => {
            render(<HelpAboutPage onReplayOnboarding={vi.fn()} />);
            await screen.findByText("Isoform for your computer");
            expect(screen.queryByTestId("about-models-panel")).not.toBeInTheDocument();
            fireEvent.click(await screen.findByRole("button", { name: "Models ›" }));
            expect(screen.getByTestId("about-models-panel")).toBeInTheDocument();
            expect(screen.queryByText("Isoform for your computer")).not.toBeInTheDocument();
            fireEvent.click(screen.getByRole("button", { name: "About" }));
            expect(await screen.findByText("Isoform for your computer")).toBeInTheDocument();
            expect(screen.queryByTestId("about-models-panel")).not.toBeInTheDocument();
        });
    });
});
