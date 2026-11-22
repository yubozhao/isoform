import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, within } from "@testing-library/react";
import i18n from "@/i18n";
import en from "@/i18n/locales/en/app.json";
import type { AgentRow } from "./agents-model";

const setLearn = vi.hoisted(() => vi.fn(async () => {}));
vi.mock("@/lib/wails-shim-app", () => ({ SetLearnFromIt: setLearn }));

import { AgentCard } from "./AgentCard";

const base: AgentRow = { id: "codex", name: "Codex", kind: "code", job: "forCodeHere", state: "connected", where: "here", found: true, connected: true, connectedHere: true, chosenFor: "code", opens: true, learnFromIt: false, connectedAt: new Date(2026, 10, 3).getTime() };
const week = { handoffs: 1, back: 0, useful: 0 };

beforeEach(async () => {
    await i18n.changeLanguage("en");
});

function show(row: Partial<AgentRow> = {}, props: Partial<Parameters<typeof AgentCard>[0]> = {}) {
    const handlers = { onClose: vi.fn(), onChangeJob: vi.fn(), onDisconnect: vi.fn(), onLearnChanged: vi.fn() };
    render(<AgentCard row={{ ...base, ...row }} language="en" week={week} {...handlers} {...props} />);
    return { ...handlers, card: screen.getByRole("region", { name: row.name ?? "Codex" }) };
}

describe("AgentCard", () => {
    it("shows the badge, name, how it is connected, its job and this week", () => {
        const { card } = show();
        expect(within(card).getByText("Co")).toBeInTheDocument();
        expect(within(card).getByText("Connected Nov 3 · on this computer · runs when Isoform starts it")).toBeInTheDocument();
        expect(within(card).getByText(en.agents.card.jobCode)).toBeInTheDocument();
        expect(within(card).getByText("1 hand-off · 0 back · 0 useful")).toBeInTheDocument();
        // What it may open is said only where it can be said truthfully: not for an agent connected here.
        expect(within(card).queryByText(en.agents.card.mayOpenTitle)).not.toBeInTheDocument();
    });

    it("shows a joined coding agent's own words for what it may open, read-only", () => {
        const { card } = show({ connectedHere: false, connectionId: "ag_co", mayOpen: "acme/widgets", where: "device", device: "Devin VM" });
        expect(within(card).getByText("Only what each hand-off sends, and new branches in acme/widgets.")).toBeInTheDocument();
        expect(within(card).getByText(/Devin VM/)).toBeInTheDocument();
    });

    it("asks once before Disconnect", () => {
        const { card, onDisconnect } = show();
        fireEvent.click(within(card).getByRole("button", { name: en.agents.disconnect }));
        expect(within(card).getByText(/Disconnect Codex\? It stops getting hand-offs at once/)).toBeInTheDocument();
        expect(onDisconnect).not.toHaveBeenCalled();
        fireEvent.click(card.querySelector("[data-agents-disconnect-yes]")!);
        expect(onDisconnect).toHaveBeenCalledTimes(1);
    });

    it("has no Change its job or Disconnect for an agent that can't hold one", () => {
        const { card } = show({ id: "ag_x", name: "Ana's Helper", kind: "else", chosenFor: undefined, connectedHere: false, connectionId: undefined, state: "connected" });
        expect(within(card).queryByRole("button", { name: en.agents.card.changeJob })).not.toBeInTheDocument();
        expect(within(card).getByText(en.agents.card.jobNone)).toBeInTheDocument();
    });

    it("shows the takes-over line", () => {
        const { card } = show({}, { takesOver: { kind: "code", current: "Claude Code", agent: "Codex" } });
        expect(within(card).getByText("Claude Code does your code work now. Codex takes over; Claude Code stays connected.")).toBeInTheDocument();
    });

    it("has Learn from it, off, for an agent on this computer; not available elsewhere", () => {
        const { card } = show();
        expect(within(card).getByRole("switch", { name: en.agents.learn.title })).toHaveAttribute("aria-checked", "false");
        expect(within(card).getByText(/Off until you turn it on/)).toBeInTheDocument();
    });

    it("says Learn from it isn't available for a chat app", () => {
        const { card } = show({ id: "claude", name: "Claude", kind: "else", where: "itsApp", connectedHere: false, state: "notConnected" });
        expect(within(card).getByText(en.agents.learnNotHere)).toBeInTheDocument();
        expect(within(card).queryByRole("switch")).not.toBeInTheDocument();
    });
});
