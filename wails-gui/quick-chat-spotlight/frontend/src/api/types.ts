/** Foreground app context Yansu detected when the global hotkey fired. */
export interface DetectionContext {
    /** Bundle ID on macOS, lowercased exe basename on Windows (e.g. "figma.exe"). */
    bundleID: string;
    /** Human-readable app name (e.g. "Figma"). */
    appName: string;
    /** The app's frontmost window title, when the host could read it. */
    windowTitle?: string;
}

/** Suggestion chip returned by /api/spotlight/suggestions. */
export interface Suggestion {
    title: string;
    prompt: string;
}

/**
 * AgentEvent mirrors `wails-gui/app.go::AgentEvent` field-for-field.
 * The spotlight stream forwards these unchanged so the React side can
 * run the same event-processing logic the main UI uses.
 *
 * Field names follow the Go JSON tags (snake_case). Anything in here
 * that the spotlight panel doesn't yet render is still kept on the
 * wire so we can light it up later without re-plumbing.
 */
export interface AgentEvent {
    type: string;
    event_id?: string;
    run_id?: string;
    session_id?: string;
    tag?: string;
    content?: string;
    tool_name?: string;
    tool_input?: unknown; // arbitrary JSON
    tool_use_id?: string;
    persisted?: boolean;
    available_commands?: AvailableCommand[];
    error?: string;
    permission_options?: PermissionOption[];
}

export interface AvailableCommand {
    name: string;
    description?: string;
}

export type PermissionOptionKind =
    | "allow_once"
    | "allow_always"
    | "reject_once"
    | "reject_always";

/** Mirrors `yansu-agent/agent/types.go::PermissionOption`. */
export interface PermissionOption {
    id: string;
    name: string;
    kind: PermissionOptionKind;
}

/** Streaming event flavors from /api/spotlight/stream/:id.
 *
 * Kind=`agent_event` carries the full AgentEvent payload — that's
 * the path used for everything emitted by the agent runner. The
 * remaining kinds are spotlight-only synthetic events used by the
 * Go side to echo the user prompt before the agent has produced
 * any tokens, and for the legacy deliverable hint.
 */
export type StreamEvent =
    | { kind: "agent_event"; data: AgentEvent }
    | { kind: "user_message"; data: { prompt: string } }
    | { kind: "assistant_started"; data: Record<string, never> }
    | { kind: "deliverable_proposed"; data: DeliverableProposal }
    | { kind: "error"; data: { message?: string } }
    | { kind: "done"; data: Record<string, never> }
    | { kind: "permission_cleared"; data: { eventId: string } };

export interface DeliverableProposal {
    deliverableID: string;
    title: string;
    steps: string[];
}
