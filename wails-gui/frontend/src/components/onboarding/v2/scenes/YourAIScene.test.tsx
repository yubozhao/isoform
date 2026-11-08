import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import i18n from "@/i18n";
import en from "@/i18n/locales/en/onboarding.json";
import type { AssistantSetupCode, AssistantSetupResult } from "@/lib/wails-shim-app";
import type { SetupAgentProbe } from "@/lib/setup-backend";
import { YourAIScene } from "./YourAIScene";

// Your AI past the happy path: an assistant that isn't installed or signed
// in. Every outcome must leave the card with its real next step, and a card
// turns on only after the one-token test answered.

const mocks = vi.hoisted(() => ({
    probe: vi.fn(),
    install: vi.fn(),
    check: vi.fn(),
    signIn: vi.fn(),
    installTerminal: vi.fn(),
    node: vi.fn(),
    order: vi.fn(),
    events: new Map<string, (payload: unknown) => void>(),
}));
vi.mock("@/lib/wails-shim-app", () => ({
    GetAgentProbe: mocks.probe,
    SetSetupWelcomeVisible: () => Promise.resolve(),
    GetModelAccountsStatus: () => Promise.resolve({ consent: { accepted: false }, accountOrder: [] }),
    SetManagedAccountOrder: mocks.order,
    AcceptManagedModelConsent: () => Promise.resolve({ consent: { accepted: true } }),
    RefreshAssistantHistory: () => Promise.resolve(),
    SetupInstallAssistant: mocks.install,
    SetupCheckAssistant: mocks.check,
    OpenAssistantSignIn: mocks.signIn,
    OpenAssistantInstallTerminal: mocks.installTerminal,
    OpenNodeDownload: mocks.node,
}));
vi.mock("../../../../../wailsjs/runtime/runtime", () => ({
    EventsOn: (key: string, fn: (payload: unknown) => void) => {
        mocks.events.set(key, fn);
        return () => mocks.events.delete(key);
    },
}));

const probe = (claude: { installed: boolean; signedIn: boolean }): SetupAgentProbe =>
    ({
        agents: [
            { id: "claude", plan: "", ...claude },
            { id: "codex", installed: false, signedIn: false, plan: "" },
        ],
        npm: false,
    }) as unknown as SetupAgentProbe;

const result = (code: AssistantSetupCode, more: Partial<AssistantSetupResult> = {}): AssistantSetupResult => ({
    id: "claude",
    installed: !["needsNode", "installDenied", "installTimeout", "installFailed", "notFound"].includes(code),
    signedIn: !["needsNode", "installDenied", "installTimeout", "installFailed", "notFound", "signedOut"].includes(code),
    tested: code.startsWith("test") || code === "gatewayDown" || code === "",
    working: code === "" || code === "testLimit",
    version: "",
    plan: "",
    code,
    ...more,
});

const card = () => screen.getByTestId("setup-v2-card-claude");
const action = () => within(card()).getByTestId("setup-v2-card-claude-action");
const fine = () => document.querySelector(".act .fine.wide") as HTMLElement;

async function renderScene(start = { installed: false, signedIn: false }) {
    mocks.probe.mockResolvedValue(probe(start));
    render(<YourAIScene onNext={() => {}} reduced enter="" />);
    await waitFor(() => expect(card()).toBeTruthy());
}

beforeEach(async () => {
    await i18n.changeLanguage("en");
    localStorage.clear();
    for (const m of [mocks.probe, mocks.install, mocks.check, mocks.signIn, mocks.installTerminal, mocks.node, mocks.order])
        m.mockReset();
    mocks.order.mockResolvedValue({ consent: { accepted: true } });
    mocks.signIn.mockResolvedValue(undefined);
    mocks.installTerminal.mockResolvedValue(undefined);
    mocks.node.mockResolvedValue(undefined);
});

describe("Your AI past the happy path", () => {
    it("installs, signs in through Terminal, re-checks on focus, then tests before turning on", async () => {
        await renderScene();
        expect(card().textContent).toContain("Not installed");
        mocks.install.mockResolvedValue(result("signedOut", { command: "claude auth login" }));
        fireEvent.click(action());
        await waitFor(() => expect(card().textContent).toContain("Not signed in"));
        expect(mocks.install).toHaveBeenCalledWith("claude");

        fireEvent.click(action());
        await waitFor(() => expect(mocks.signIn).toHaveBeenCalledWith("claude"));
        expect(card().textContent).toContain("Finish in Terminal");
        expect(fine().textContent).toBe("Sign in to Claude Code in Terminal. Isoform checks again by itself.");

        mocks.check.mockImplementation((_id: string, test: boolean) =>
            Promise.resolve(test ? result("") : result("", { tested: false, working: false })),
        );
        await act(async () => {
            fireEvent.focus(window);
        });
        await waitFor(() => expect(card().textContent).toContain("Signed in · Working"));
        expect(mocks.check).toHaveBeenCalledWith("claude", false);
        expect(mocks.check).toHaveBeenCalledWith("claude", true);
        expect(card().className).not.toContain("off");
    });

    it("tests a sign-in the launch check sees finish, once", async () => {
        await renderScene({ installed: true, signedIn: false });
        fireEvent.click(action());
        await waitFor(() => expect(card().textContent).toContain("Finish in Terminal"));
        let answer!: (r: AssistantSetupResult) => void;
        mocks.check.mockReturnValue(new Promise((r) => (answer = r)));
        mocks.probe.mockResolvedValue(probe({ installed: true, signedIn: true }));
        await act(async () => {
            mocks.events.get("setup:agents-updated")?.({});
        });
        await waitFor(() => expect(card().textContent).toContain("Checking it answers"));
        await act(async () => {
            fireEvent.focus(window);
        });
        await act(async () => answer(result("")));
        await waitFor(() => expect(card().textContent).toContain("Signed in · Working"));
        expect(mocks.check.mock.calls.filter(([, test]) => test)).toHaveLength(1);
    });

    it("needs Node.js: opens the download, then installs again when the window comes back", async () => {
        await renderScene();
        mocks.install.mockResolvedValueOnce(result("needsNode"));
        fireEvent.click(action());
        await waitFor(() => expect(card().textContent).toContain("Needs Node.js"));
        expect(fine().textContent).toContain("Claude Code installs with Node.js");
        expect(fine().className).toContain("error");
        fireEvent.click(action());
        expect(mocks.node).toHaveBeenCalled();
        mocks.install.mockResolvedValueOnce(result("signedOut"));
        await act(async () => {
            fireEvent.focus(window);
        });
        await waitFor(() => expect(card().textContent).toContain("Not signed in"));
        expect(mocks.install).toHaveBeenCalledTimes(2);
    });

    it.each(["installDenied", "installTimeout", "installFailed", "notFound"] as const)(
        "%s: offers the install in Terminal and picks it up afterwards",
        async (code) => {
            await renderScene();
            mocks.install.mockResolvedValue(result(code, { command: "curl -fsSL https://claude.ai/install.sh | bash" }));
            fireEvent.click(action());
            await waitFor(() => expect(action().textContent).toBe("Install in Terminal"));
            expect(fine().textContent).toContain("Install it in Terminal");
            fireEvent.click(action());
            await waitFor(() => expect(mocks.installTerminal).toHaveBeenCalledWith("claude"));
            expect(card().textContent).toContain("Finish in Terminal");
            expect(fine().textContent).toBe(
                "Terminal runs curl -fsSL https://claude.ai/install.sh | bash. Isoform checks again by itself.",
            );
            mocks.check.mockResolvedValue(result("signedOut"));
            await act(async () => {
                fireEvent.focus(window);
            });
            await waitFor(() => expect(card().textContent).toContain("Not signed in"));
        },
    );

    it("a sign-in that didn't take asks for the sign-in again", async () => {
        await renderScene({ installed: true, signedIn: false });
        fireEvent.click(action());
        mocks.check.mockImplementation((_id: string, test: boolean) =>
            Promise.resolve(test ? result("testSignedOut") : result("", { tested: false, working: false })),
        );
        await act(async () => {
            fireEvent.focus(window);
        });
        await waitFor(() => expect(card().textContent).toContain("Sign-in didn't take"));
        expect(action().textContent).toBe("Sign in");
        expect(fine().textContent).toBe("Claude Code says it isn't signed in. Sign in again in Terminal.");
    });

    it("an assistant that didn't answer can be checked again", async () => {
        await renderScene({ installed: true, signedIn: false });
        fireEvent.click(action());
        mocks.check.mockImplementation((_id: string, test: boolean) =>
            Promise.resolve(test ? result("testFailed", { detail: "HTTP 502" }) : result("", { tested: false, working: false })),
        );
        await act(async () => {
            fireEvent.focus(window);
        });
        await waitFor(() => expect(card().textContent).toContain("Didn't answer"));
        expect(action().textContent).toBe("Check again");
        mocks.check.mockResolvedValue(result(""));
        fireEvent.click(action());
        await waitFor(() => expect(card().textContent).toContain("Signed in · Working"));
    });

    it("an assistant at its limit is set up and selectable", async () => {
        await renderScene({ installed: true, signedIn: false });
        fireEvent.click(action());
        mocks.check.mockImplementation((_id: string, test: boolean) =>
            Promise.resolve(test ? result("testLimit") : result("", { tested: false, working: false })),
        );
        await act(async () => {
            fireEvent.focus(window);
        });
        await waitFor(() => expect(card().textContent).toContain("Signed in · Working"));
        await waitFor(() => expect(fine().textContent).toContain("at its usage limit"));
        expect(fine().className).not.toContain("error");
    });

    it("says how to sign in by hand when Terminal won't open", async () => {
        await renderScene({ installed: true, signedIn: false });
        mocks.signIn.mockRejectedValue(new Error("open failed"));
        fireEvent.click(action());
        await waitFor(() =>
            expect(fine().textContent).toBe("Couldn't open Terminal. Open it and run claude auth login."),
        );
        expect(card().textContent).toContain("Not signed in");
    });

    it("never leaves a card on a bare Try again", async () => {
        const codes: AssistantSetupCode[] = [
            "needsNode",
            "installDenied",
            "installTimeout",
            "installFailed",
            "notFound",
            "signedOut",
        ];
        for (const code of codes) {
            mocks.probe.mockResolvedValue(probe({ installed: false, signedIn: false }));
            mocks.install.mockResolvedValue(result(code));
            const { unmount } = render(<YourAIScene onNext={() => {}} reduced enter="" />);
            await waitFor(() => expect(card()).toBeTruthy());
            fireEvent.click(action());
            await waitFor(() => expect(mocks.install).toHaveBeenCalled());
            await waitFor(() => expect(card().textContent).not.toContain("Installing"));
            expect(document.body.textContent).not.toMatch(/try again/i);
            expect(within(card()).queryByTestId("setup-v2-card-claude-action")).toBeTruthy();
            unmount();
            mocks.install.mockReset();
        }
    });
});

describe("Your AI, signed in at launch", () => {
    it("shows the card as today and turns it to Working when the background test answers", async () => {
        let answer!: (r: AssistantSetupResult) => void;
        mocks.check.mockReturnValue(new Promise((r) => (answer = r)));
        await renderScene({ installed: true, signedIn: true });
        expect(card().textContent).toContain("Signed in");
        expect(card().textContent).not.toContain("Checking");
        // Continue never waits for the test: the only assistant fills the slot.
        await waitFor(() => expect(screen.getByTestId("setup-v2-continue")).not.toBeDisabled());
        expect(mocks.check).toHaveBeenCalledWith("claude", true);
        await act(async () => answer(result("")));
        await waitFor(() => expect(card().textContent).toContain("Signed in · Working"));
        expect(mocks.check).toHaveBeenCalledTimes(1);
    });

    it("shows the failure's own state when the background test fails", async () => {
        mocks.check.mockResolvedValue(result("testSignedOut"));
        await renderScene({ installed: true, signedIn: true });
        await waitFor(() => expect(card().textContent).toContain("Sign-in didn't take"));
        expect(action().textContent).toBe("Sign in");
        expect(screen.getByTestId("setup-v2-continue")).toBeTruthy();
    });

    it("waits for a gateway that is still starting before saying it didn't answer", async () => {
        vi.useFakeTimers({ shouldAdvanceTime: true });
        try {
            mocks.check
                .mockResolvedValueOnce(result("gatewayDown"))
                .mockResolvedValueOnce(result("gatewayDown"))
                .mockResolvedValueOnce(result(""));
            await renderScene({ installed: true, signedIn: true });
            await act(async () => {
                await vi.advanceTimersByTimeAsync(6000);
            });
            await waitFor(() => expect(card().textContent).toContain("Signed in · Working"));
            expect(mocks.check).toHaveBeenCalledTimes(3);
            expect(document.body.textContent).not.toContain("Didn't answer");
        } finally {
            vi.useRealTimers();
        }
    });
});

describe("Your AI copy", () => {
    it("has no bare Try again left in the assistant lines", () => {
        const lines = Object.entries(en.v2.ai).filter(([key]) => key !== "errorSetUp");
        for (const [key, value] of lines) {
            if (typeof value === "string") expect(`${key}: ${value}`).not.toMatch(/try again/i);
        }
    });
});
