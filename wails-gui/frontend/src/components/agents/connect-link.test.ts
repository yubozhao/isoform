import { describe, expect, it } from "vitest";
import type { AgentJoinStart } from "@/lib/wails-shim-app";
import { initialLinkState, linkTransition, type LinkEffect, type LinkEvent, type LinkState } from "./connect-link";

const local: AgentJoinStart = { message: "m", link: "http://127.0.0.1/j/a", expiresAt: 1, local: true, mcpAddress: "http://127.0.0.1/mcp" };
const address: AgentJoinStart = { message: "m", link: "https://x.agents.isoform.ai/j/b", expiresAt: 2, local: false, mcpAddress: "https://x.agents.isoform.ai/mcp" };

function run(events: LinkEvent[]): { state: LinkState; effects: LinkEffect[] } {
    let state = initialLinkState;
    const effects: LinkEffect[] = [];
    for (const event of events) {
        const [next, out] = linkTransition(state, event);
        state = next;
        effects.push(...out);
    }
    return { state, effects };
}

describe("the connect sheet's link (v1.5.0 G10, G11)", () => {
    it("opens with a private link and opens no address", () => {
        const { state, effects } = run([{ type: "open" }, { type: "made", local: true, start: local }]);
        expect(state).toEqual({ kind: "local", start: local });
        expect(effects).toEqual([{ type: "startLocal" }]);
    });

    it("opens the address only on Connect, passing the acceptance", () => {
        const { state, effects } = run([
            { type: "open" },
            { type: "made", local: true, start: local },
            { type: "choose", need: { kind: "address", for: "claude" } },
        ]);
        expect(state).toEqual({ kind: "confirm", for: "claude" });
        expect(effects).toEqual([{ type: "startLocal" }]);
        const [opening, out] = linkTransition(state, { type: "connect", acceptTerms: true });
        expect(opening).toEqual({ kind: "opening", for: "claude" });
        expect(out).toEqual([{ type: "startAddress", app: "claude", acceptTerms: true }]);
        expect(linkTransition(opening, { type: "made", local: false, start: address })[0]).toEqual({ kind: "address", for: "claude", start: address });
    });

    it("Any other agent on this computer keeps the private link; somewhere else asks first", () => {
        const here = run([{ type: "open" }, { type: "made", local: true, start: local }, { type: "choose", need: { kind: "local" } }]);
        expect(here.state.kind).toBe("local");
        expect(here.effects).toEqual([{ type: "startLocal" }]);
        const elsewhere = run([{ type: "open" }, { type: "made", local: true, start: local }, { type: "choose", need: { kind: "address", for: "other" } }, { type: "connect", acceptTerms: false }]);
        expect(elsewhere.effects.at(-1)).toEqual({ type: "startAddress", app: "other", acceptTerms: false });
    });

    it("an open address serves another app without asking again", () => {
        const { state, effects } = run([
            { type: "open" },
            { type: "choose", need: { kind: "address", for: "claude" } },
            { type: "connect", acceptTerms: true },
            { type: "made", local: false, start: address },
            { type: "choose", need: { kind: "address", for: "gemini" } },
        ]);
        expect(state).toEqual({ kind: "address", for: "gemini", start: address });
        expect(effects.filter((e) => e.type === "startAddress")).toHaveLength(1);
    });

    it("going back to this computer makes a new private link", () => {
        const { state, effects } = run([
            { type: "open" },
            { type: "choose", need: { kind: "address", for: "other" } },
            { type: "connect", acceptTerms: true },
            { type: "made", local: false, start: address },
            { type: "choose", need: { kind: "local" } },
        ]);
        expect(state).toEqual({ kind: "makingLocal" });
        expect(effects.at(-1)).toEqual({ type: "startLocal" });
    });

    it("drops a link that arrives after the person chose something else", () => {
        const { state } = run([
            { type: "open" },
            { type: "choose", need: { kind: "address", for: "claude" } },
            { type: "connect", acceptTerms: true },
            { type: "choose", need: { kind: "none" } },
            { type: "made", local: false, start: address },
        ]);
        expect(state).toEqual({ kind: "none" });
        const stale = run([{ type: "open" }, { type: "choose", need: { kind: "address", for: "claude" } }, { type: "made", local: true, start: local }]);
        expect(stale.state).toEqual({ kind: "confirm", for: "claude" });
    });

    it("a failed open can be tried again from its Connect", () => {
        const { state, effects } = run([
            { type: "open" },
            { type: "choose", need: { kind: "address", for: "grok" } },
            { type: "connect", acceptTerms: true },
            { type: "failed", local: false },
            { type: "connect", acceptTerms: true },
        ]);
        expect(state).toEqual({ kind: "opening", for: "grok" });
        expect(effects.filter((e) => e.type === "startAddress")).toHaveLength(2);
    });

    it("a new link on an address goes back to Connect, and on a private link makes one", () => {
        expect(linkTransition({ kind: "address", for: "other", start: address }, { type: "retry" })).toEqual([{ kind: "confirm", for: "other" }, []]);
        expect(linkTransition({ kind: "local", start: local }, { type: "retry" })).toEqual([{ kind: "makingLocal" }, [{ type: "startLocal" }]]);
        expect(linkTransition({ kind: "localFailed" }, { type: "retry" })).toEqual([{ kind: "makingLocal" }, [{ type: "startLocal" }]]);
    });
});
