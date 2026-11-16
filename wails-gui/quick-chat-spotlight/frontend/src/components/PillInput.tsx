import React, { useEffect, useRef, useState } from "react";
import { useTranslation } from "@/i18n";
import { ArrowUp, Loader2, Mic } from "lucide-react";
import { HideWindow } from "../lib/wails-app-stub";
import { useVoiceInput } from "@/hooks/useVoiceInput";
import { LogoIcon } from "@/assets/icons/LogoIcon";
import { VoiceRecordingIndicator } from "@/components/voice-recording-indicator";
import { DetectionChip } from "./DetectionChip";
import type { DetectionContext } from "../api/types";

interface Props {
    detection: DetectionContext | null;
    /** withScreen: the screen chip was still there when it was sent. */
    onSubmit: (prompt: string, withScreen: boolean) => void;
    /** Bumps every time the panel surfaces so the textarea re-focuses (Wails only). */
    showNonce?: number;
}

// True only in the macOS native NSPanel helper (the spotlight WKScriptMessage
// handler is registered there). Wails (Windows/Linux) lacks it. Matches the
// detection used by the bridge shims.
function isNativeSpotlight(): boolean {
    return Boolean(
        (
            window as unknown as {
                webkit?: { messageHandlers?: { spotlight?: unknown } };
            }
        ).webkit?.messageHandlers?.spotlight,
    );
}

/**
 * PillInput is the spotlight's "form 1" entry-point card.
 *
 * Layout (matches design 1.png — single flat surface, no nested card):
 *   ┌───────────────────────────────────────────────────────────┐
 *   │ [Y]  [App – X detected]                                   │  ← top row
 *   │ Describe…                                  [mic] [send]   │  ← bottom row
 *   └───────────────────────────────────────────────────────────┘
 *   width 768, height 88, radius 16, shared panel ground, padding 10/20.
 *
 * We deliberately do NOT reuse main UI's `<Composer>` here — it's
 * built for the full chat surface (model selector, plan/thinking
 * toggles, image upload, slash commands, queue) and brings its own
 * rounded card + border that conflicts with the flat pill design.
 *
 * What we DO reuse: `useVoiceInput`, the hook the main composer
 * uses, so mic/recording behavior is identical (host-bridge sherpa-onnx,
 * download progress, permission prompts). Form 2 (ConversationPanel)
 * uses the full Composer because it has the height + features for it.
 */
export function PillInput({
    detection,
    onSubmit,
    showNonce,
}: Props) {
    const { t } = useTranslation("chat");
    // What's on screen is offered as a chip and goes with the question only
    // while it is there (computer-chat v1.0.0 rule 8). Each summon offers it
    // again.
    const [screenChip, setScreenChip] = useState(true);
    useEffect(() => { setScreenChip(true); }, [detection?.bundleID, showNonce]);
    const withScreen = screenChip && Boolean(detection?.bundleID);
    const textareaRef = useRef<HTMLTextAreaElement>(null);
    const [text, setText] = useState("");

    const voice = useVoiceInput({
        onTranscriptUpdate: (transcript) => setText(transcript),
    });

    /*
     * Autofocus on every fresh summons — Wails (Windows/Linux) ONLY. There
     * the helper is a regular activating window whose WebView forwards focus
     * to the document a frame or two late, so we retry at 50/200/500ms.
     *
     * On macOS the panel is a non-activating NSPanel that must NOT steal
     * focus from the user's app, so we skip autofocus entirely; the user
     * clicks the pill to move focus here (the panel keys on first click).
     */
    useEffect(() => {
        if (isNativeSpotlight()) return;
        const timers: ReturnType<typeof setTimeout>[] = [];
        for (const delay of [50, 200, 500]) {
            timers.push(
                setTimeout(() => {
                    textareaRef.current?.focus({ preventScroll: true });
                }, delay),
            );
        }
        return () => {
            for (const t of timers) clearTimeout(t);
        };
    }, [detection?.bundleID, showNonce]);

    // Esc dismisses (hides) the spotlight.
    useEffect(() => {
        const onKey = async (e: KeyboardEvent) => {
            if (e.key === "Escape") {
                try {
                    await HideWindow();
                } catch {
                    /* ignore */
                }
            }
        };
        window.addEventListener("keydown", onKey);
        return () => window.removeEventListener("keydown", onKey);
    }, []);

    const submit = () => {
        const trimmed = text.trim();
        if (!trimmed) return;
        onSubmit(trimmed, withScreen);
        setText("");
    };

    const onKeyDown = (e: React.KeyboardEvent<HTMLTextAreaElement>) => {
        // Skip IME composition so Enter confirms the Pinyin/IME candidate
        // instead of submitting the prompt. keyCode 229 is the fallback for
        // engines that don't set isComposing on the confirming keydown
        // (mirrors the main composer's guard).
        if (e.nativeEvent.isComposing || e.nativeEvent.keyCode === 229) {
            return;
        }
        // Backspace in an empty field removes the screen chip.
        if (e.key === "Backspace" && withScreen && e.currentTarget.selectionStart === 0 && e.currentTarget.selectionEnd === 0) {
            e.preventDefault();
            setScreenChip(false);
            return;
        }
        if (
            e.key === "Enter" &&
            !e.shiftKey &&
            !e.metaKey &&
            !e.ctrlKey &&
            !e.altKey
        ) {
            e.preventDefault();
            submit();
        }
    };

    const handleVoiceClick = () => {
        if (voice.state === "recording") {
            void voice.stopAndTranscribe();
        } else if (voice.state === "idle") {
            void voice.startRecording();
        } else if (voice.error && voice.needsMicrophonePermission) {
            void voice.openMicrophoneSettings();
        } else if (voice.error) {
            void voice.retryInitialization();
        }
    };

    const placeholder = t("quickChat.placeholder");

    // Wails draggable convention: outer card drags, interactive
    // inner regions opt out via --wails-draggable: no-drag.
    const drag = { "--wails-draggable": "drag" } as React.CSSProperties;
    const noDrag = { "--wails-draggable": "no-drag" } as React.CSSProperties;
    return (
        <div className="pill" style={drag}>
            <div className="pill__top">
                <span className="pill__yansu" aria-label={t("quickChat.brand")}>
                    <LogoIcon size={22} />
                </span>
                <div style={noDrag}>
                    <DetectionChip
                        detection={withScreen ? detection : null}
                        onRemove={() => setScreenChip(false)}
                    />
                </div>
            </div>
            <div className="pill__bottom" style={noDrag}>
                <textarea
                    ref={textareaRef}
                    className="pill__input"
                    rows={1}
                    placeholder={placeholder}
                    value={text}
                    onChange={(e) => setText(e.target.value)}
                    onKeyDown={onKeyDown}
                />
                {voice.state === "recording" ? (
                    <VoiceRecordingIndicator
                        audioLevel={voice.audioLevel ?? 0}
                        onClick={handleVoiceClick}
                    />
                ) : (
                    <button
                        type="button"
                        className="pill__icon-btn"
                        onClick={handleVoiceClick}
                        disabled={
                            voice.state === "transcribing" ||
                            voice.state === "initializing"
                        }
                        title={voice.error ? voice.error : t("quickChat.voiceInput")}
                    >
                        {voice.state === "transcribing" ||
                        voice.state === "initializing" ? (
                            <Loader2 className="pill__icon pill__icon--spin" />
                        ) : (
                            <Mic className="pill__icon" />
                        )}
                    </button>
                )}
                <button
                    type="button"
                    className="pill__send"
                    onClick={submit}
                    disabled={!text.trim()}
                    title={t("quickChat.send")}
                >
                    <ArrowUp className="pill__icon-light" />
                </button>
            </div>
            <style>{pillStyles}</style>
        </div>
    );
}

const pillStyles = `
.pill {
    width: 768px;
    height: 88px;
    box-sizing: border-box;
    border-radius: 16px;
    background: var(--spotlight-surface);
    padding: 10px 20px;
    display: flex;
    flex-direction: column;
    gap: 6px;
}
.pill__top {
    display: flex;
    align-items: center;
    gap: 10px;
    height: 32px;
    flex-shrink: 0;
}
.pill__yansu {
    /* SVG glyph has no internal padding, so the 22px size lands the
       visible logo at the same optical weight the old 32px PNG did
       (whose visible glyph was ~22px inside its 32px bounding box).
       Inline-flex centres it within a 32px box so the row stays
       vertically aligned with the detection chip. */
    display: inline-flex;
    align-items: center;
    justify-content: flex-start;
    width: 22px;
    height: 32px;
    flex-shrink: 0;
    user-select: none;
    /* currentColor on the SVG paths means the logo follows the ink
       token, which auto-swaps in dark mode via [data-theme="onedark"]. */
    color: var(--ink);
}
.pill__bottom {
    display: flex;
    align-items: center;
    gap: 6px;
    flex: 1;
    min-height: 0;
}
.pill__input {
    flex: 1;
    min-width: 0;
    height: 32px;
    border: none;
    outline: none;
    background: transparent;
    color: var(--ink);
    font-size: 15px;
    font-family: inherit;
    resize: none;
    padding: 0;
    line-height: 32px;
    overflow: hidden;
}
.pill__input::placeholder {
    color: var(--muted);
}
.pill__icon-btn {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 32px;
    height: 32px;
    border: none;
    background: transparent;
    border-radius: 8px;
    color: var(--ink);
    cursor: pointer;
    flex-shrink: 0;
    padding: 0;
}
.pill__icon-btn:hover:not(:disabled) {
    background: var(--color-surface-hover);
}
.pill__icon-btn:disabled {
    opacity: 0.5;
    cursor: default;
}
.pill__icon {
    width: 18px;
    height: 18px;
}
.pill__icon--spin {
    animation: pill-spin 1s linear infinite;
}
@keyframes pill-spin {
    to { transform: rotate(360deg); }
}
.pill__send {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 32px;
    height: 32px;
    border: none;
    background: var(--color-solid-btn-bg);
    border-radius: 8px;
    color: var(--color-solid-btn-fg);
    cursor: pointer;
    flex-shrink: 0;
    padding: 0;
    transition: filter 120ms ease;
}
.pill__send:hover:not(:disabled) {
    filter: brightness(0.92);
}
.pill__send:disabled {
    opacity: 0.4;
    cursor: default;
}
.pill__icon-light {
    width: 18px;
    height: 18px;
    color: var(--color-solid-btn-fg);
}

/* DetectionChip — matches design 1.png:
   border #0099FF4D / 1px solid, radius 8, padding 4/6,
   font 12px / #0099FF, font-family inherits from the spotlight html
   (which @imports main UI's index.css, so it tracks main UI's font
   stack). */
.detection-chip {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    padding: 4px 6px;
    background: transparent;
    border: 1px solid var(--color-sample-border);
    color: var(--color-sample-text);
    border-radius: 8px;
    font-size: 12px;
    font-weight: 500;
    font-family: inherit;
    line-height: 1;
    white-space: nowrap;
    max-width: 560px;
    cursor: default;
}
.detection-chip__icon {
    display: inline-flex;
    align-items: center;
    /* SVG glyph baseline vs CSS font baseline don't naturally align —
       the AppIcon's letter sits ~1px above the geometric centre of its
       SVG box, while the chip label's letter sits at the line-box
       centre. Nudging the icon down 1px puts both glyphs on the same
       optical baseline. */
    transform: translateY(1px);
}
.detection-chip__icon svg,
.detection-chip svg {
    display: block;
    vertical-align: middle;
}
.detection-chip__label {
    display: block;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    line-height: 1.2;
}
.detection-chip__remove {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 16px;
    height: 16px;
    margin-left: 2px;
    padding: 0;
    border: 0;
    border-radius: 4px;
    background: transparent;
    color: inherit;
    opacity: 0.7;
    cursor: pointer;
}
.detection-chip__remove:hover,
.detection-chip__remove:focus-visible {
    opacity: 1;
    background: var(--color-sample-border);
}
.detection-chip:disabled {
    cursor: default;
    opacity: 0.85;
}
.detection-chip--neutral {
    color: var(--accent);
    border-color: var(--accent-soft);
}
.detection-chip__icon {
    display: inline-flex;
}
`;
