import { useCallback, useEffect, useRef, useState } from "react";
import i18n from "@/i18n";
import { EventsOn } from "./lib/spotlight-runtime";
import {
    ResizeMode,
    SetAutoHideOnBlur,
    SetDarkMode,
    SetSurfaceColor,
} from "./lib/wails-app-stub";
import { PillInput } from "./components/PillInput";
import { ConversationPanel } from "./components/ConversationPanel";
import { fetchAppearance, submitPrompt, newSpotlightRequestId } from "./api/yansu";
import type { DetectionContext } from "./api/types";

type View = "pill" | "conversation";

interface SpotlightShowEvent {
    bundleID?: string | null;
    appName?: string | null;
    windowTitle?: string | null;
}

/**
 * App is the spotlight's root component. Owns the view state (pill
 * vs. conversation), the detection context the Go side pushed via
 * `spotlight:show`, and routes prompt submissions through Yansu's
 * host bridge.
 *
 * Both views are mounted simultaneously and crossfaded via CSS so
 * the pill → conversation transition reads as one continuous motion
 * rather than a hard component swap. The Wails window dimension
 * tween (main.go::ResizeMode) runs in parallel with the CSS fade —
 * total ~200ms.
 */
export function App() {
    const [view, setView] = useState<View>("pill");
    const [detection, setDetection] = useState<DetectionContext | null>(null);
    const [submittedPrompt, setSubmittedPrompt] = useState<string>("");
    const [sessionLocalId, setSessionLocalId] = useState<string | null>(null);
    const [firstUserAck, setFirstUserAck] = useState<{ clientId: string; itemId: string; sortOrder: number } | null>(null);
    const [firstSendFailed, setFirstSendFailed] = useState(false);
    const [firstSendPending, setFirstSendPending] = useState(false);
    const firstRequestRef = useRef<string | null>(null);
    const firstRequestPendingRef = useRef(false);
    const conversationVersionRef = useRef(0);
    const [showNonce, setShowNonce] = useState(0);
    const appearanceRequestRef = useRef(0);
    const appearancePendingRef = useRef<Promise<void> | null>(null);

    /*
     * Theme and language sync with the main Isoform UI's selected preferences,
     * not the OS dark-mode setting — main UI lets the user override
     * system dark mode in settings, and we want spotlight to follow
     * that override.
     *
     * Source of truth: main app's themePref (persisted to disk by
     * App.SetThemePreference, served via GET /api/spotlight/theme).
     * Language comes from the ui.language state saved by the main UI.
     * Spotlight queries them on mount, every spotlight:show, and focus.
     * Live cross-window sync belongs to a follow-up. This helper stays flat
     * with the same flat surfaces as the main window.
     *
     * Application: <html data-theme> attribute swap (the imported main
     * UI index.css already defines the full :root[data-theme="onedark"]
     * palette) + Win32 BackgroundColour update so dark-mode users
     * don't see a light flash on resize.
     */
    const applyTheme = useCallback((theme: "paper" | "onedark") => {
        const dark = theme === "onedark";
        document.documentElement.setAttribute("data-theme", theme);
        // Win32 BackgroundColour paints any region WebView2 hasn't
        // drawn yet. Match --spotlight-surface in index.css.
        const surface = getComputedStyle(document.documentElement).getPropertyValue('--color-panel').trim();
        // Production CSS can shorten a six-digit token to three digits.
        const digits = surface.replace(/^#/, '');
        const expanded = digits.length === 3 ? digits.split('').map((digit) => digit + digit).join('') : digits;
        if (/^[\da-f]{6}$/i.test(expanded)) {
            const hex = Number.parseInt(expanded, 16);
            void SetSurfaceColor((hex >> 16) & 255, (hex >> 8) & 255, hex & 255).catch(() => {
                /* standalone dev fallback */
            });
        }
        // DWMWA_USE_IMMERSIVE_DARK_MODE: tells DWM to paint the 1px
        // top frame border in the dark palette. Without this, dark
        // mode users see a thin light strip at the top of the pill
        // because Windows defaults the WS_THICKFRAME border to the
        // system-light colour even when our surface is dark.
        void SetDarkMode(dark).catch(() => {
            /* standalone dev fallback */
        });
    }, []);

    const syncAppearance = useCallback(async () => {
        const request = ++appearanceRequestRef.current;
        let pending = fetchAppearance().then(async (appearance) => {
            if (!appearance || request !== appearanceRequestRef.current) return;
            applyTheme(appearance.theme);
            if (appearance.language && appearance.language !== i18n.resolvedLanguage) {
                await i18n.changeLanguage(appearance.language);
            }
        });
        appearancePendingRef.current = pending;
        // A focus refresh can supersede a show refresh. Wait for the newest
        // language to settle too.
        for (;;) {
            await pending;
            const latest = appearancePendingRef.current;
            if (!latest || latest === pending) return;
            pending = latest;
        }
    }, [applyTheme]);

    useEffect(() => {
        const onFocus = () => void syncAppearance();
        void syncAppearance();
        window.addEventListener("focus", onFocus);
        return () => {
            window.removeEventListener("focus", onFocus);
            appearanceRequestRef.current++;
        };
    }, [syncAppearance]);

    /*
     * Tag <html> with the host platform so index.css can apply
     * platform-specific tweaks. The current consumer is the body
     * background rule: Windows wants body painted with the surface
     * colour to mask the WebView2 default white that bleeds through
     * the pill's rounded-corner cutouts (the "white edge in dark
     * mode" symptom). macOS keeps body transparent so the OS-level
     * translucent window can show through.
     */
    useEffect(() => {
        const ua = navigator.userAgent;
        const platform = ua.includes("Windows")
            ? "platform-windows"
            : ua.includes("Mac")
              ? "platform-mac"
              : "platform-linux";
        document.documentElement.classList.add(platform);
        return () => {
            document.documentElement.classList.remove(platform);
        };
    }, []);

    useEffect(() => {
        const off = EventsOn(
            "spotlight:show",
            async (payload: SpotlightShowEvent) => {
                const next: DetectionContext = {
                    bundleID: payload?.bundleID ?? "",
                    appName: payload?.appName ?? "",
                    windowTitle: payload?.windowTitle ?? "",
                };
                setDetection(next);
                setView("pill");
                // Each global-hotkey summon starts a fresh conversation —
                // resetting the persisted prompt + session id (the
                // ConversationPanel is keyed off `showNonce`, so bumping
                // it remounts the panel and clears its in-memory items).
                // Continuing the conversation only happens via the panel's
                // own follow-up textarea, which calls submitPrompt with
                // the existing sessionLocalId.
                conversationVersionRef.current += 1;
                firstRequestRef.current = null;
                firstRequestPendingRef.current = false;
                setFirstUserAck(null);
                setFirstSendFailed(false);
                setFirstSendPending(false);
                setSubmittedPrompt("");
                setSessionLocalId(null);
                setShowNonce((n) => n + 1);
                // Each summon picks up Settings changes from the host, whose
                // language may differ from this helper's OS/storage defaults.
                await syncAppearance();
            },
        );
        return off;
    }, [syncAppearance]);

    // withScreen: the screen chip was still there when the person sent it
    // (computer-chat v1.0.0 rule 8). A retry keeps the first send's choice.
    const firstWithScreenRef = useRef(true);
    const handleSubmit = async (prompt: string, retry = false, withScreen = true) => {
        if (firstRequestPendingRef.current) return;
        const trimmed = prompt.trim();
        if (!trimmed) return;
        // Kick off the CSS cross-fade BEFORE awaiting the OS resize so
        // both motions overlap. Order matters: the conv layer is
        // bottom-anchored with a fixed 650px height (see stageStyles),
        // so even while the OS window is still 88px tall the panel is
        // already laid out at its final size — no squash / reflow.
        // What the user actually sees:
        //   t=0  setView fires → cross-fade starts (pill 1→0, conv 0→1)
        //   t≈5  await ResizeMode resolves → atomic SetWindowPos grows
        //        the window to 650; the bottom edge stays put, so the
        //        pill (at bottom:0 of its layer) doesn't visually
        //        shift, and the conv (already there, fading in) just
        //        becomes more of itself visible as the window grows up
        //   t=220 fade complete
        // The earlier "resize-then-setView" order produced a visible
        // gap where the pill sat at the bottom of an already-resized
        // window with empty surface area above it before the conv
        // layer rendered — the residual flicker QA noticed.
        if (!retry) {
            firstRequestRef.current = newSpotlightRequestId();
            firstWithScreenRef.current = withScreen;
        }
        const requestId = firstRequestRef.current!;
        const version = conversationVersionRef.current;
        firstRequestPendingRef.current = true;
        setFirstSendPending(true);
        setFirstSendFailed(false);
        setSubmittedPrompt(trimmed);
        setView("conversation");
        try {
            // Conv (long-lived chat) shouldn't auto-hide on focus
            // loss — disable that before the window is committed to
            // the conversation surface.
            await SetAutoHideOnBlur(false);
        } catch {
            /* standalone dev fallback */
        }
        try {
            await ResizeMode("conversation");
        } catch {
            /* standalone dev fallback */
        }
        try {
            const response = await submitPrompt(
                trimmed,
                firstWithScreenRef.current ? detection?.bundleID ?? "" : "",
                undefined,
                undefined,
                requestId,
            );
            if (version === conversationVersionRef.current) {
                if (response.userItemId && typeof response.sortOrder === "number") {
                    setFirstUserAck({ clientId: requestId, itemId: response.userItemId, sortOrder: response.sortOrder });
                }
                setSessionLocalId(response.sessionLocalId);
            }
        } catch {
            if (version === conversationVersionRef.current) setFirstSendFailed(true);
        } finally {
            if (version === conversationVersionRef.current) {
                firstRequestPendingRef.current = false;
                setFirstSendPending(false);
            }
        }
    };

    const handleClose = async () => {
        conversationVersionRef.current += 1;
        firstRequestRef.current = null;
        firstRequestPendingRef.current = false;
        setFirstUserAck(null);
        setFirstSendFailed(false);
        setFirstSendPending(false);
        setView("pill");
        setSubmittedPrompt("");
        setSessionLocalId(null);
        try {
            await SetAutoHideOnBlur(true);
        } catch {
            /* ignore */
        }
        try {
            await ResizeMode("pill");
        } catch {
            /* ignore */
        }
    };

    return (
        <div className="stage" data-view={view}>
            <div
                className="stage__layer stage__layer--pill"
                aria-hidden={view !== "pill"}
            >
                <PillInput
                    detection={detection}
                    onSubmit={(prompt, withScreen) => void handleSubmit(prompt, false, withScreen)}
                    showNonce={showNonce}
                />
            </div>
            <div
                className="stage__layer stage__layer--conversation"
                aria-hidden={view !== "conversation"}
            >
                <ConversationPanel
                    /*
                     * Keying the panel off `showNonce` makes every Alt+Space
                     * summon a fresh conversation: React unmounts the
                     * previous panel and remounts a new one with empty
                     * StreamState. Without this, ConversationPanel keeps
                     * its `items` array across show/hide cycles and the
                     * second prompt would render alongside the first
                     * conversation's history. Follow-ups within the same
                     * panel session reuse the same key (showNonce only
                     * bumps on `spotlight:show`) so they accumulate as
                     * one chat.
                     */
                    key={showNonce}
                    detection={detection}
                    userPrompt={submittedPrompt}
                    sessionLocalId={sessionLocalId}
                    firstRequestId={firstRequestRef.current}
                    firstUserAck={firstUserAck}
                    firstSendFailed={firstSendFailed}
                    firstSendPending={firstSendPending}
                    onRetryFirst={() => void handleSubmit(submittedPrompt, true)}
                    onClose={handleClose}
                />
            </div>
            <style>{stageStyles}</style>
        </div>
    );
}

/*
 * Pill ↔ conversation swap is an opacity cross-fade between two
 * always-mounted, bottom-anchored layers. Why bottom-anchored: with
 * the OS window itself bottom-anchored (see main.go's
 * spotlightBottomFraction), the input pill sits at the window bottom
 * during pill phase. After the atomic SetWindowPos resize, the window
 * grows UP — anchoring the pill layer to bottom:0 with its natural
 * 88px height keeps the pill visually at the same y position as the
 * window expands, so it doesn't appear to "drop to the top" of the
 * new larger frame. The conversation layer, also bottom:0, naturally
 * lines up its input bar with where the pill was.
 *
 * Both layers always render (opacity 0 for the inactive one, not
 * display:none) so the conversation panel is already laid out at its
 * intrinsic 650px height before the cross-fade starts — no
 * mid-animation layout reflow even though the OS window is changing
 * size at roughly the same time. The CSS transition handles smoothing.
 *
 * Earlier attempts (FLIP slides, multi-frame window tweens, plain
 * display:block swap) each had their own artefacts; this combination
 * — atomic OS resize + always-mounted bottom-anchored layers +
 * opacity cross-fade — is the one that holds up.
 */
const stageStyles = `
.stage {
    position: relative;
    width: 100vw;
    height: 100vh;
    overflow: hidden;
}
.stage__layer {
    position: absolute;
    left: 0;
    right: 0;
    bottom: 0;
    transition: opacity 220ms ease;
    opacity: 0;
    pointer-events: none;
}
.stage__layer--pill {
    height: 88px;
    /* On top so the pill is the surface user sees first; the conv
       layer fades up from underneath. */
    z-index: 2;
}
.stage__layer--conversation {
    height: 650px;
    z-index: 1;
}
.stage[data-view="pill"] .stage__layer--pill,
.stage[data-view="conversation"] .stage__layer--conversation {
    opacity: 1;
    pointer-events: auto;
}
`;
