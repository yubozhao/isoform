import { useSyncExternalStore } from 'react';

import {
    PrewarmTts,
    SplitSpeechText,
    SynthesizeSpeech,
} from './wails-shim-app';

export type TtsStatus = 'idle' | 'loading' | 'playing';

interface TtsState {
    activeId: string | null;
    status: TtsStatus;
}

type SynthesisResult =
    | { ok: true; audio: string }
    | { ok: false; error: unknown };

// Module-level singleton: only one message plays at a time across the whole UI.
// Any new toggleSpeak() stops whatever was playing/loading before it.
let state: TtsState = { activeId: null, status: 'idle' };
// One AudioContext for the app's lifetime, created lazily and never closed.
// Opening/closing the context per message starts/stops the OS audio output
// unit, and on macOS that device transition itself can pop on some hardware,
// regardless of sample content — deferring close() only moved the pop 100ms
// past speech end. Instead the context stays open and is merely suspended
// after an idle delay (so the silent render callback doesn't cost CPU
// forever), well clear of the speech tail.
let audioCtx: AudioContext | null = null;
let masterGain: GainNode | null = null;
let scheduled: AudioBufferSourceNode[] = [];
// Monotonic token identifying the current playback. reset() bumps it, which
// invalidates any in-flight pipeline (they captured the previous value).
let playbackGen = 0;
let suspendTimer: number | null = null;
const listeners = new Set<() => void>();

// A manual stop mid-waveform is a hard cut at a non-zero sample — an audible
// pop. Ramp the master gain to silence first, then stop the sources once the
// ramp (and the hardware output buffer) has drained.
const STOP_RAMP_SECONDS = 0.03;
const TEARDOWN_DELAY_MS = 100;
const SUSPEND_AFTER_IDLE_MS = 5000;

function emit(): void {
    listeners.forEach((l) => l());
}

function setState(next: TtsState): void {
    state = next;
    emit();
}

function getAudioContext(): AudioContext {
    if (!audioCtx || audioCtx.state === 'closed') {
        audioCtx = new AudioContext();
    }
    return audioCtx;
}

// Park the shared context once playback has been idle for a while. Suspending
// right at speech end would put the device-stop transition (a pop source on
// some hardware) back next to the audio; doing it seconds later, with the
// output pinned at zero, keeps any transition artifact decoupled from speech.
function scheduleIdleSuspend(): void {
    if (suspendTimer !== null) {
        window.clearTimeout(suspendTimer);
    }
    suspendTimer = window.setTimeout(() => {
        suspendTimer = null;
        if (state.status === 'idle' && audioCtx && audioCtx.state === 'running') {
            audioCtx.suspend().catch(() => {});
        }
    }, SUSPEND_AFTER_IDLE_MS);
}

function reset(): void {
    playbackGen++;
    const ctx = audioCtx;
    const gain = masterGain;
    const sources = scheduled;
    masterGain = null;
    scheduled = [];
    setState({ activeId: null, status: 'idle' });
    if (!ctx || ctx.state === 'closed') return;

    try {
        if (gain) {
            gain.gain.setValueAtTime(gain.gain.value, ctx.currentTime);
            gain.gain.linearRampToValueAtTime(0, ctx.currentTime + STOP_RAMP_SECONDS);
        }
    } catch {
        // context may already be closed
    }
    // Deferred teardown: stopping sources the instant playback ends truncates
    // whatever is still in the OS output buffer — itself a pop. The shared
    // context is never closed here; it gets suspended after the idle delay.
    window.setTimeout(() => {
        for (const src of sources) {
            try {
                src.onended = null;
                src.stop();
                src.disconnect();
            } catch {
                // already stopped/ended
            }
        }
        try {
            gain?.disconnect();
        } catch {
            // already disconnected
        }
    }, TEARDOWN_DELAY_MS);
    scheduleIdleSuspend();
}

/** Stop any in-flight or playing speech. Safe to call when idle. */
export function stopSpeaking(): void {
    if (state.status !== 'idle') {
        reset();
    }
}

// Prewarm the model(s) for a message ahead of an actual click — e.g. on hover —
// so the first synthesis doesn't pay model-load latency. The backend inspects
// the text and warms only the language(s) it will use (Chinese -> MeloTTS,
// English -> Kokoro), and only when that model is already downloaded — hover
// never triggers a fresh download. Deduped per message text; best-effort.
const prewarmedTexts = new Set<string>();
export function prewarmTtsForText(text: string): void {
    if (!text || prewarmedTexts.has(text)) return;
    prewarmedTexts.add(text);
    PrewarmTts(text).catch(() => {
        prewarmedTexts.delete(text);
    });
}

function base64ToArrayBuffer(b64: string): ArrayBuffer {
    const bin = atob(b64);
    const bytes = new Uint8Array(bin.length);
    for (let i = 0; i < bin.length; i++) {
        bytes[i] = bin.charCodeAt(i);
    }
    return bytes.buffer;
}

// Gapless streaming playback. Synthesize chunks in order and schedule each
// decoded buffer to start exactly when the previous one ends (sample-accurate
// via the Web Audio clock), so the only pauses are the natural ones already in
// the speech — no clip-switch stutter. Synthesis runs faster than real time
// (RTF < 1), so each buffer is ready well before its scheduled start, keeping
// the timeline continuous. The first (short) chunk starts immediately for a
// quick time-to-first-audio.
async function playPipeline(id: string, sentences: string[], gen: number): Promise<void> {
    const ctx = getAudioContext();
    if (suspendTimer !== null) {
        window.clearTimeout(suspendTimer);
        suspendTimer = null;
    }
    // All sources play through one master gain so reset() can fade the whole
    // stream out instead of hard-cutting the active source.
    const gain = ctx.createGain();
    gain.connect(ctx.destination);
    masterGain = gain;
    try {
        await ctx.resume();
    } catch {
        // resume may reject if not allowed yet; start() still works on gesture
    }

    // Keep a few synthesis requests in flight ahead of playback so the engine
    // never idles while we decode/schedule, and buffers are ready before their
    // scheduled start (minimizing gaps). The backend serializes synthesis, but
    // queuing requests keeps it continuously busy.
    const LOOKAHEAD = 3;
    const pending: Array<Promise<SynthesisResult> | undefined> = new Array(sentences.length);
    const kick = (i: number) => {
        if (i < sentences.length && !pending[i]) {
            // Attach the rejection handler immediately. A reset can make the
            // pipeline return before every lookahead promise is awaited.
            pending[i] = SynthesizeSpeech(sentences[i]).then(
                (audio) => ({ ok: true, audio }),
                (error: unknown) => ({ ok: false, error }),
            );
        }
    };
    for (let i = 0; i < LOOKAHEAD; i++) kick(i);

    let nextStart = ctx.currentTime;
    let started = false;
    let pipelineFinished = false;
    let activeSources = 0;
    let lastSynthesisError: unknown;
    const finishIfComplete = () => {
        if (
            pipelineFinished &&
            activeSources === 0 &&
            state.activeId === id &&
            playbackGen === gen
        ) {
            reset();
        }
    };

    for (let i = 0; i < sentences.length; i++) {
        const result = await pending[i]!;
        if (state.activeId !== id || playbackGen !== gen) return;
        if (!result.ok) {
            lastSynthesisError = result.error;
            kick(i + LOOKAHEAD);
            continue; // skip a chunk that failed to synthesize
        }
        kick(i + LOOKAHEAD);

        let buffer: AudioBuffer;
        try {
            buffer = await ctx.decodeAudioData(base64ToArrayBuffer(result.audio));
        } catch {
            continue;
        }
        if (state.activeId !== id || playbackGen !== gen) return;

        const src = ctx.createBufferSource();
        src.buffer = buffer;
        src.connect(gain);
        const startAt = Math.max(nextStart, ctx.currentTime);
        src.start(startAt);
        nextStart = startAt + buffer.duration;
        scheduled.push(src);
        activeSources++;
        src.onended = () => {
            activeSources--;
            finishIfComplete();
        };

        if (!started) {
            started = true;
            setState({ activeId: id, status: 'playing' });
        }
    }

    if (state.activeId !== id || playbackGen !== gen) return;
    pipelineFinished = true;
    if (!started) {
        reset();
        if (lastSynthesisError) {
            throw lastSynthesisError;
        }
        return;
    }
    finishIfComplete();
}

/**
 * Toggle speech for a message. Clicking the currently active (loading or
 * playing) message stops it; clicking any other message stops the previous one
 * and speaks the new text, streamed gaplessly.
 */
export async function toggleSpeak(id: string, text: string): Promise<void> {
    if (state.activeId === id && state.status !== 'idle') {
        reset();
        return;
    }
    reset();
    const gen = playbackGen;
    setState({ activeId: id, status: 'loading' });

    let sentences: string[];
    try {
        sentences = await SplitSpeechText(text);
    } catch (err) {
        if (state.activeId === id && playbackGen === gen) {
            setState({ activeId: null, status: 'idle' });
        }
        throw err;
    }

    if (state.activeId !== id || playbackGen !== gen) return;
    if (sentences.length === 0) {
        reset();
        return;
    }
    await playPipeline(id, sentences, gen);
}

function subscribe(cb: () => void): () => void {
    listeners.add(cb);
    return () => {
        listeners.delete(cb);
    };
}

function getSnapshot(): TtsState {
    return state;
}

/** React hook: returns the TTS status for a given message id. */
export function useTtsStatus(id: string): TtsStatus {
    const snap = useSyncExternalStore(subscribe, getSnapshot);
    return snap.activeId === id ? snap.status : 'idle';
}

// Monotonic counter giving each automatic announcement a unique playback id.
let autoSpeakSeq = 0;

/**
 * Speak text with a fresh id on every call — for automatic, non-toggle
 * playback (automation announcements, notifications) that has no manual
 * speaker button to toggle against. Unlike toggleSpeak keyed by content, a
 * repeat of the SAME text never matches the active id, so it interrupts the
 * previous auto-speech and plays, instead of cancelling itself and going
 * silent. The id is internal (no UI subscribes to it via useTtsStatus).
 */
export async function speakOnce(text: string): Promise<void> {
    const trimmed = text?.trim();
    if (!trimmed) return;
    autoSpeakSeq += 1;
    await toggleSpeak(`auto-speak:${autoSpeakSeq}`, trimmed);
}
