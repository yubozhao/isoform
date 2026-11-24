import type { AgentJoinStart } from "@/lib/wails-shim-app";

/**
 * Which join link the connect sheet holds (computer-conformance-answers
 * v1.4.0 G7, v1.5.0 G10 and G11). An agent on this computer gets a private
 * link that opens no address. An agent in its own app, or any other agent
 * that runs somewhere else, needs the agent address, which opens only on the
 * person's Connect: the button is the acceptance of Let's Encrypt's
 * agreement the first time, so the sheet asks before it opens anything.
 */

/** The current Let's Encrypt Subscriber Agreement and its past versions. */
export const LETS_ENCRYPT_AGREEMENT_URL = "https://letsencrypt.org/repository/";

/** What the address is opened for: an app, or any other agent elsewhere. */
export type AddressFor = "claude" | "chatgpt" | "gemini" | "grok" | "other";

/** The link a choice needs. */
export type LinkNeed = { kind: "local" } | { kind: "none" } | { kind: "address"; for: AddressFor };

export type LinkState =
    | { kind: "makingLocal" }
    | { kind: "local"; start: AgentJoinStart }
    | { kind: "localFailed" }
    | { kind: "none" }
    | { kind: "confirm"; for: AddressFor }
    | { kind: "opening"; for: AddressFor }
    | { kind: "address"; for: AddressFor; start: AgentJoinStart }
    | { kind: "addressFailed"; for: AddressFor };

export type LinkEvent =
    | { type: "open" }
    | { type: "choose"; need: LinkNeed }
    | { type: "connect"; acceptTerms: boolean }
    | { type: "made"; local: boolean; start: AgentJoinStart }
    | { type: "failed"; local: boolean }
    | { type: "retry" };

export type LinkEffect = { type: "startLocal" } | { type: "startAddress"; app: AddressFor; acceptTerms: boolean };

type Step = [LinkState, LinkEffect[]];

export const initialLinkState: LinkState = { kind: "makingLocal" };

function choose(state: LinkState, need: LinkNeed): Step {
    switch (need.kind) {
        case "none":
            return [{ kind: "none" }, []];
        case "local":
            switch (state.kind) {
                case "makingLocal":
                case "local":
                    return [state, []];
                case "localFailed":
                case "none":
                case "confirm":
                case "opening":
                case "address":
                case "addressFailed":
                    return [{ kind: "makingLocal" }, [{ type: "startLocal" }]];
            }
        case "address":
            switch (state.kind) {
                // An open address serves any app and any agent elsewhere alike.
                case "address":
                    return [{ ...state, for: need.for }, []];
                case "opening":
                    return [{ ...state, for: need.for }, []];
                case "makingLocal":
                case "local":
                case "localFailed":
                case "none":
                case "confirm":
                case "addressFailed":
                    return [{ kind: "confirm", for: need.for }, []];
            }
    }
}

/** The one transition function: pure, every state and event named. */
export function linkTransition(state: LinkState, event: LinkEvent): Step {
    switch (event.type) {
        case "open":
            return [{ kind: "makingLocal" }, [{ type: "startLocal" }]];
        case "choose":
            return choose(state, event.need);
        case "connect":
            switch (state.kind) {
                case "confirm":
                case "addressFailed":
                    return [{ kind: "opening", for: state.for }, [{ type: "startAddress", app: state.for, acceptTerms: event.acceptTerms }]];
                case "makingLocal":
                case "local":
                case "localFailed":
                case "none":
                case "opening":
                case "address":
                    return [state, []];
            }
        case "made":
            // A link made for a choice the person has since left is dropped.
            if (event.local) return state.kind === "makingLocal" ? [{ kind: "local", start: event.start }, []] : [state, []];
            return state.kind === "opening" ? [{ kind: "address", for: state.for, start: event.start }, []] : [state, []];
        case "failed":
            if (event.local) return state.kind === "makingLocal" ? [{ kind: "localFailed" }, []] : [state, []];
            return state.kind === "opening" ? [{ kind: "addressFailed", for: state.for }, []] : [state, []];
        case "retry":
            switch (state.kind) {
                case "localFailed":
                case "local":
                    return [{ kind: "makingLocal" }, [{ type: "startLocal" }]];
                case "address":
                    // A new link for the same choice: back to its Connect.
                    return [{ kind: "confirm", for: state.for }, []];
                case "makingLocal":
                case "none":
                case "confirm":
                case "opening":
                case "addressFailed":
                    return [state, []];
            }
    }
}

/** The link the sheet shows now, if any. */
export function linkStart(state: LinkState): AgentJoinStart | null {
    return state.kind === "local" || state.kind === "address" ? state.start : null;
}
