import type { AgentEvent, ReadyItem, Suggestion, StreamEvent } from "./types";
import type { StreamItemData } from "@/types";

/**
 * Yansu's host-bridge port + the per-session token are both assigned
 * at startup and different every launch, so we can't bake them in.
 * The Go side (main.go) parses them from --yansu-port and --ipc-token
 * CLI flags Yansu's spotlight supervisor passes when spawning us,
 * then exposes them via the Wails binding `GetConfig`. We cache the
 * result so subsequent calls are essentially free.
 */
interface SpotlightConfig {
    yansuPort: number;
    token: string;
}
let cachedConfig: SpotlightConfig | null = null;
let pendingConfig: Promise<SpotlightConfig> | null = null;

/**
 * readHostConfig returns the host-bridge port + IPC token without any
 * generated wailsjs/ dependency:
 *   - macOS native panel: injected window.__spotlightConfig (document-start)
 *   - Windows/Linux Wails: the window.go.main.app.GetConfig() binding
 * Returns null when neither is present (standalone `vite dev`).
 */
async function readHostConfig(): Promise<{ yansuPort?: number; token?: string } | null> {
    const injected = (
        window as unknown as { __spotlightConfig?: { yansuPort?: number; token?: string } }
    ).__spotlightConfig;
    if (injected) return injected;
    const binding = (
        window as unknown as {
            go?: { main?: { app?: { GetConfig?: () => Promise<{ yansuPort?: number; token?: string }> } } };
        }
    ).go?.main?.app?.GetConfig;
    if (typeof binding === "function") return binding();
    return null;
}

async function getConfig(): Promise<SpotlightConfig> {
    if (cachedConfig) return cachedConfig;
    if (pendingConfig) return pendingConfig;
    pendingConfig = (async () => {
        const c = await readHostConfig().catch(() => null);
        if (c) {
            cachedConfig = {
                yansuPort: Number(c.yansuPort ?? 0),
                token: String(c.token ?? ""),
            };
        } else {
            // Standalone dev fallback (`vite dev` without supervisor):
            // VITE_YANSU_PORT lets developers point at a manually running
            // Yansu host-bridge.
            const fallback = Number(import.meta.env.VITE_YANSU_PORT ?? 0);
            cachedConfig = { yansuPort: fallback, token: "" };
        }
        return cachedConfig;
    })();
    return pendingConfig;
}

async function url(path: string): Promise<string> {
    const cfg = await getConfig();
    return `http://127.0.0.1:${cfg.yansuPort}${path}`;
}

async function headers(extra?: Record<string, string>): Promise<HeadersInit> {
    const cfg = await getConfig();
    const h: Record<string, string> = { "Content-Type": "application/json" };
    if (cfg.token) h["X-Yansu-Spotlight-Token"] = cfg.token;
    if (extra) Object.assign(h, extra);
    return h;
}

export async function fetchSuggestions(bundleID: string, lang = ""): Promise<Suggestion[]> {
    const params = new URLSearchParams();
    if (bundleID) params.set("bundleID", bundleID);
    if (lang) params.set("lang", lang);
    const res = await fetch(await url(`/api/spotlight/suggestions?${params}`), {
        headers: await headers(),
    });
    if (!res.ok) throw new Error(`suggestions ${res.status}`);
    const body = (await res.json()) as { suggestions: Suggestion[] };
    return body.suggestions ?? [];
}

export function newSpotlightRequestId(): string {
    if (typeof crypto !== "undefined" && typeof crypto.randomUUID === "function") return crypto.randomUUID();
    return `${Date.now().toString(16)}-${Math.random().toString(16).slice(2)}-${Math.random().toString(16).slice(2)}`;
}

/**
 * submitPrompt drives a spotlight chat turn.
 *
 * - First call: omit sessionLocalId; the server mints one and returns it.
 * - Follow-up: pass the returned sessionLocalId — appends a new user
 *   row + runs the next turn.
 * - Edit/reset: pass sessionLocalId AND { editItemId, editFromSortOrder }
 *   from `editOf`. The server rewrites the existing user row at that
 *   slot and truncates every row with sort_order > editFromSortOrder
 *   before running the new turn. Without this the persisted history
 *   would accumulate one extra user row per edit/reset, causing the
 *   main UI (which loads from the DB) to show duplicate user bubbles.
 */
export async function submitPrompt(
    prompt: string,
    bundleID: string,
    sessionLocalId?: string,
    editOf?: { itemId: string; sortOrder: number },
    requestId?: string,
): Promise<{ sessionLocalId: string; userItemId?: string; sortOrder?: number }> {
    const body: Record<string, unknown> = { prompt, bundleID };
    if (sessionLocalId) body.sessionLocalId = sessionLocalId;
    if (requestId) body.requestId = requestId;
    if (editOf) {
        body.editItemId = editOf.itemId;
        body.editFromSortOrder = editOf.sortOrder;
    }
    const res = await fetch(await url("/api/spotlight/submit"), {
        method: "POST",
        headers: await headers(),
        body: JSON.stringify(body),
    });
    if (!res.ok) throw new Error(`submit ${res.status}`);
    return (await res.json()) as { sessionLocalId: string; userItemId?: string; sortOrder?: number };
}

/**
 * subscribeStream opens an SSE connection to /api/spotlight/stream/:id
 * and invokes `onEvent` for each numbered event after the cursor. A dropped
 * stream reports failure so the caller can replay or request a snapshot.
 * Returns a cancel function.
 *
 * EventSource doesn't support custom headers, so we use a manual
 * fetch + reader to forward the X-Yansu-Spotlight-Token.
 */
export function subscribeStream(
    sessionLocalId: string,
    after: number,
    onEvent: (event: StreamEvent, sequence: number) => void,
    onDisconnect: (historyUnavailable: boolean) => void,
): () => void {
    const ctrl = new AbortController();
    (async () => {
        let cursor = after;
        let historyUnavailable = false;
        try {
            const res = await fetch(await url(`/api/spotlight/stream/${encodeURIComponent(sessionLocalId)}?after=${after}`), {
                headers: await headers({ Accept: "text/event-stream" }),
                signal: ctrl.signal,
            });
            historyUnavailable = res.status === 409;
            if (!res.ok || !res.body) throw new Error("stream unavailable");
            const reader = res.body.getReader();
            const decoder = new TextDecoder();
            let buffer = "";
            while (true) {
                const { done, value } = await reader.read();
                if (done) throw new Error("stream ended");
                buffer += decoder.decode(value, { stream: true });
                let boundary = buffer.indexOf("\n\n");
                while (boundary >= 0) {
                    const chunk = buffer.slice(0, boundary);
                    buffer = buffer.slice(boundary + 2);
                    boundary = buffer.indexOf("\n\n");
                    const parsed = parseChunk(chunk);
                    if (!parsed) continue;
                    if (parsed.sequence !== cursor + 1) throw new Error("stream sequence gap");
                    onEvent(parsed.event, parsed.sequence);
                    cursor = parsed.sequence;
                }
            }
        } catch {
            if (!ctrl.signal.aborted) onDisconnect(historyUnavailable);
        }
    })();
    return () => ctrl.abort();
}

function parseChunk(chunk: string): { event: StreamEvent; sequence: number } | null {
    let event = "";
    let data = "";
    let sequence = 0;
    for (const line of chunk.split("\n")) {
        if (line.startsWith("id:")) sequence = Number(line.slice(3).trim());
        else if (line.startsWith("event:")) event = line.slice(6).trim();
        else if (line.startsWith("data:")) data += line.slice(5).trim();
    }
    if (!event) return null;
    if (!Number.isSafeInteger(sequence) || sequence < 1) throw new Error("invalid stream sequence");
    const parsed = data ? JSON.parse(data) : {};
    return { event: { kind: event, data: parsed } as StreamEvent, sequence };
}

export async function fetchSpotlightSnapshot(sessionLocalId: string): Promise<{ items: StreamItemData[]; sequence: number; streaming: boolean; runId?: string; sessionId?: string; permission?: AgentEvent }> {
    const res = await fetch(await url(`/api/spotlight/snapshot?sessionLocalId=${encodeURIComponent(sessionLocalId)}`), { headers: await headers() });
    if (!res.ok) throw new Error("spotlight history unavailable");
    return await res.json() as { items: StreamItemData[]; sequence: number; streaming: boolean; runId?: string; sessionId?: string; permission?: AgentEvent };
}

/**
 * fetchReady matches what the person typed against work Isoform already
 * made (ready work first). The words go in a POST body, never a URL. A
 * failed read throws: it is shown with Try again, never as "nothing there".
 */
export async function fetchReady(text: string, signal?: AbortSignal): Promise<ReadyItem | null> {
    const res = await fetch(await url("/api/spotlight/ready"), {
        method: "POST",
        headers: await headers({ "Content-Type": "application/json" }),
        body: JSON.stringify({ text }),
        signal,
    });
    if (!res.ok) throw new Error("ready work unavailable");
    const body = await res.json() as { item?: ReadyItem | null };
    return body.item ?? null;
}

/**
 * openReady opens a made item in Isoform: in Today, or in Chat with the item
 * attached and the typed words in the box. Throws when it could not open.
 */
export async function openReady(id: string, text: string, inChat: boolean): Promise<void> {
    const res = await fetch(await url("/api/spotlight/ready/open"), {
        method: "POST",
        headers: await headers({ "Content-Type": "application/json" }),
        body: JSON.stringify({ id, text, inChat }),
    });
    if (!res.ok) throw new Error("ready work could not be opened");
}

/**
 * workOnIt runs one of the hand-off card's or the Work on it sheet's
 * bindings in Isoform (quick_chat_handoff.go), so both open in this panel,
 * over the app you're in. A refusal throws with Isoform's own words (the
 * sheet tells Closed Eyes apart by them).
 */
export async function workOnIt<T = unknown>(op: string, args: Record<string, unknown> = {}): Promise<T> {
    const res = await fetch(await url("/api/spotlight/work-on-it"), {
        method: "POST",
        headers: await headers({ "Content-Type": "application/json" }),
        body: JSON.stringify({ op, ...args }),
    });
    const body = await res.json().catch(() => ({})) as { result?: T; error?: string };
    if (!res.ok) throw new Error(body.error || "work on it unavailable");
    return body.result as T;
}

export async function dismiss(): Promise<void> {
    try {
        await fetch(await url("/api/spotlight/dismiss"), {
            method: "POST",
            headers: await headers(),
        });
    } catch {
        // Best effort.
    }
}

/**
 * respondToPermission unblocks the agent loop after it emitted a
 * permission_request event. sessionID is the Anthropic session id
 * (NOT the spotlight tag) — RespondToPermission keys its waiter map
 * by it. The runner reads optionID and either runs the tool or
 * cancels it.
 */
export async function respondToPermission(
    sessionID: string,
    optionID: string,
    sessionLocalId?: string,
): Promise<void> {
    const res = await fetch(await url("/api/spotlight/permission"), {
        method: "POST",
        headers: await headers(),
        body: JSON.stringify({ sessionID, optionID, sessionLocalId }),
    });
    if (!res.ok) throw new Error(`permission ${res.status}`);
}

export interface SpotlightAppearance {
    theme: "paper" | "onedark";
    language?: "en" | "zh-CN";
}

/** The host's resolved appearance, including its saved ui.language.
 *  Failed or slow responses keep the palette/language intact and cannot
 *  hold up suggestions. Spotlight always stays flat. */
export async function fetchAppearance(): Promise<SpotlightAppearance | null> {
    const controller = new AbortController();
    let timeout: ReturnType<typeof setTimeout> | undefined;
    try {
        const deadline = new Promise<null>((resolve) => {
            timeout = setTimeout(() => {
                controller.abort();
                resolve(null);
            }, 500);
        });
        const request = (async (): Promise<SpotlightAppearance | null> => {
            const res = await fetch(await url("/api/spotlight/theme"), {
                headers: await headers(),
                signal: controller.signal,
            });
            if (!res.ok) return null;
            const body = (await res.json()) as { theme?: unknown; language?: unknown };
            return {
                theme: body.theme === "onedark" ? "onedark" : "paper",
                language: body.language === "en" || body.language === "zh-CN" ? body.language : undefined,
            };
        })();
        return await Promise.race([request, deadline]);
    } catch {
        return null;
    } finally {
        clearTimeout(timeout);
    }
}

/**
 * expand surfaces the main Yansu window and asks it to navigate to
 * this spotlight chat session. Backed by App.expandSpotlightToMain;
 * the main UI emits a router transition to the standalone-chat
 * project keyed by sessionLocalId. After this resolves the spotlight
 * helper should hide itself so the main window takes focus.
 */
export async function expand(sessionLocalId: string): Promise<void> {
    try {
        await fetch(await url("/api/spotlight/expand"), {
            method: "POST",
            headers: await headers(),
            body: JSON.stringify({ sessionLocalId }),
        });
    } catch (err) {
        console.error("[spotlight] expand failed:", err);
    }
}

/**
 * cancel stops the in-flight agent run for this spotlight session.
 * Backed by App.CancelByTag — it cancels the goroutine driving
 * executePrompt; the agent emits a synthetic done+close so the
 * panel exits the streaming state cleanly.
 */
export async function cancel(sessionLocalId: string): Promise<void> {
    try {
        await fetch(await url("/api/spotlight/cancel"), {
            method: "POST",
            headers: await headers(),
            body: JSON.stringify({ sessionLocalId }),
        });
    } catch (err) {
        console.error("[spotlight] cancel failed:", err);
    }
}

/**
 * transcribeAudio uploads a base64-encoded WAV blob to Yansu's host
 * bridge, which runs it through the same sherpa-onnx engine the main
 * composer uses. Spotlight WebView2 doesn't have sherpa-onnx itself
 * (cgo binding lives only in the main Yansu binary), so this HTTP
 * round-trip is how we share the offline STT.
 */
export async function transcribeAudio(wavBase64: string): Promise<string> {
    const res = await fetch(await url("/api/spotlight/voice/transcribe"), {
        method: "POST",
        headers: await headers(),
        body: JSON.stringify({ wavBase64 }),
    });
    if (!res.ok) throw new Error(`transcribe ${res.status}`);
    const body = (await res.json()) as { text: string };
    return body.text ?? "";
}

/** Warm up the speech engine on the main side so first transcription
 *  doesn't have to wait on model load. Best-effort: errors propagate. */
export async function initVoiceInput(): Promise<void> {
    const res = await fetch(await url("/api/spotlight/voice/init"), {
        method: "POST",
        headers: await headers(),
    });
    if (!res.ok) throw new Error(`voice/init ${res.status}`);
}
