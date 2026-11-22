import { beforeEach, describe, expect, it, vi } from 'vitest';

const bindings = vi.hoisted(() => ({
    getAutoSpeakEnabled: vi.fn(),
    getAutoSpeakNotificationsEnabled: vi.fn(),
    prewarmTts: vi.fn(),
    splitSpeechText: vi.fn(),
    synthesizeSpeech: vi.fn(),
}));

vi.mock('./wails-shim-app', () => ({
    GetAutoSpeakEnabled: bindings.getAutoSpeakEnabled,
    GetAutoSpeakNotificationsEnabled: bindings.getAutoSpeakNotificationsEnabled,
    PrewarmTts: bindings.prewarmTts,
    SplitSpeechText: bindings.splitSpeechText,
    SynthesizeSpeech: bindings.synthesizeSpeech,
}));

class AudioBufferSourceNodeStub {
    buffer: AudioBuffer | null = null;
    onended: (() => void) | null = null;

    connect = vi.fn();
    disconnect = vi.fn();
    start = vi.fn();
    stop = vi.fn();
}

class GainNodeStub {
    gain = {
        value: 1,
        setValueAtTime: vi.fn(),
        linearRampToValueAtTime: vi.fn(),
    };

    connect = vi.fn();
    disconnect = vi.fn();
}

// Instances created during the current test, so assertions can reach the
// nodes the player wired up internally.
const createdSources: AudioBufferSourceNodeStub[] = [];
const createdGains: GainNodeStub[] = [];
const createdContexts: AudioContextStub[] = [];

class AudioContextStub {
    currentTime = 0;
    destination = {};
    state: AudioContextState = 'suspended';
    close = vi.fn(async (): Promise<void> => {
        this.state = 'closed';
    });
    resume = vi.fn(async (): Promise<void> => {
        this.state = 'running';
    });
    suspend = vi.fn(async (): Promise<void> => {
        this.state = 'suspended';
    });

    constructor() {
        createdContexts.push(this);
    }

    createBufferSource(): AudioBufferSourceNode {
        const src = new AudioBufferSourceNodeStub();
        createdSources.push(src);
        return src as unknown as AudioBufferSourceNode;
    }

    createGain(): GainNode {
        const gain = new GainNodeStub();
        createdGains.push(gain);
        return gain as unknown as GainNode;
    }

    decodeAudioData(): Promise<AudioBuffer> {
        return Promise.resolve({ duration: 0.1 } as AudioBuffer);
    }

}

describe('tts player', () => {
    beforeEach(async () => {
        for (const ctx of createdContexts) {
            ctx.state = 'closed';
        }
        vi.stubGlobal('AudioContext', AudioContextStub);
        const { stopSpeaking } = await import('./tts-player');
        stopSpeaking();
        vi.clearAllMocks();
        createdSources.length = 0;
        createdGains.length = 0;
        createdContexts.length = 0;
    });

    it('does not advance lookahead after playback is cancelled', async () => {
        let rejectFirst!: (reason: Error) => void;
        const first = new Promise<string>((_resolve, reject) => {
            rejectFirst = reject;
        });
        bindings.splitSpeechText.mockResolvedValue(['one', 'two', 'three', 'four']);
        bindings.synthesizeSpeech
            .mockReturnValueOnce(first)
            .mockResolvedValue('');

        const { stopSpeaking, toggleSpeak } = await import('./tts-player');
        const playback = toggleSpeak('message', 'text');
        await vi.waitFor(() => {
            expect(bindings.synthesizeSpeech).toHaveBeenCalledTimes(3);
        });

        stopSpeaking();
        rejectFirst(new Error('cancelled request failed'));
        await expect(playback).resolves.toBeUndefined();
        expect(bindings.synthesizeSpeech).toHaveBeenCalledTimes(3);
    });

    it('does not revive a stale split request when the same message restarts', async () => {
        let resolveFirst!: (sentences: string[]) => void;
        let resolveSecond!: (sentences: string[]) => void;
        const firstSplit = new Promise<string[]>((resolve) => {
            resolveFirst = resolve;
        });
        const secondSplit = new Promise<string[]>((resolve) => {
            resolveSecond = resolve;
        });
        bindings.splitSpeechText
            .mockReturnValueOnce(firstSplit)
            .mockReturnValueOnce(secondSplit);
        bindings.synthesizeSpeech.mockResolvedValue('');

        const { toggleSpeak } = await import('./tts-player');
        const stalePlayback = toggleSpeak('message', 'stale text');
        await vi.waitFor(() => {
            expect(bindings.splitSpeechText).toHaveBeenCalledTimes(1);
        });

        await toggleSpeak('message', 'stale text'); // stop while loading
        const currentPlayback = toggleSpeak('message', 'current text');
        resolveSecond(['current']);
        await currentPlayback;

        resolveFirst(['stale']);
        await stalePlayback;

        expect(bindings.synthesizeSpeech).toHaveBeenCalledTimes(1);
        expect(bindings.synthesizeSpeech).toHaveBeenCalledWith('current');
        expect(createdSources).toHaveLength(1);
        expect(createdGains).toHaveLength(1);
    });

    it('fades out and defers teardown instead of hard-cutting on stop', async () => {
        vi.useFakeTimers();
        try {
            bindings.splitSpeechText.mockResolvedValue(['one']);
            bindings.synthesizeSpeech.mockResolvedValue('');

            const { stopSpeaking, toggleSpeak } = await import('./tts-player');
            await toggleSpeak('message', 'text');

            const gain = createdGains[0];
            const src = createdSources[0];
            expect(gain).toBeDefined();
            expect(src.connect).toHaveBeenCalledWith(gain);

            stopSpeaking();

            // The fade to silence is scheduled immediately (stub currentTime
            // is 0, so the ramp must land at the 30ms mark)...
            expect(gain.gain.linearRampToValueAtTime).toHaveBeenCalledWith(0, 0.03);
            // ...but the source must outlive the ramp and the OS output buffer.
            expect(src.stop).not.toHaveBeenCalled();

            vi.advanceTimersByTime(100);
            expect(src.stop).toHaveBeenCalled();
        } finally {
            vi.useRealTimers();
        }
    });

    it('suspends the shared context after the idle delay', async () => {
        vi.useFakeTimers();
        try {
            bindings.splitSpeechText.mockResolvedValue(['one']);
            bindings.synthesizeSpeech.mockResolvedValue('');

            const { toggleSpeak } = await import('./tts-player');
            await toggleSpeak('message', 'text');

            const src = createdSources[0];
            const ctx = createdContexts[0];
            expect(src.onended).toBeTypeOf('function');

            src.onended!();
            expect(ctx.suspend).not.toHaveBeenCalled();

            vi.advanceTimersByTime(5000);
            expect(ctx.suspend).toHaveBeenCalled();
            expect(ctx.close).not.toHaveBeenCalled();
        } finally {
            vi.useRealTimers();
        }
    });

    it('waits for synthesis to finish when an earlier source ends', async () => {
        vi.useFakeTimers();
        try {
            let resolveSecond!: (audio: string) => void;
            const second = new Promise<string>((resolve) => {
                resolveSecond = resolve;
            });
            bindings.splitSpeechText.mockResolvedValue(['one', 'two']);
            bindings.synthesizeSpeech
                .mockResolvedValueOnce('')
                .mockReturnValueOnce(second);

            const { toggleSpeak } = await import('./tts-player');
            const playback = toggleSpeak('message', 'text');
            await vi.waitFor(() => {
                expect(createdSources).toHaveLength(1);
            });

            const first = createdSources[0];
            const ctx = createdContexts[0];
            expect(first.onended).toBeTypeOf('function');
            first.onended!();
            expect(ctx.suspend).not.toHaveBeenCalled();

            resolveSecond('');
            await playback;
            expect(createdSources).toHaveLength(2);

            const secondSource = createdSources[1];
            expect(secondSource.onended).toBeTypeOf('function');
            secondSource.onended!();
            vi.advanceTimersByTime(5000);
            expect(ctx.suspend).toHaveBeenCalled();
        } finally {
            vi.useRealTimers();
        }
    });

    it('rejects when every speech chunk fails to synthesize', async () => {
        bindings.splitSpeechText.mockResolvedValue(['one', 'two']);
        bindings.synthesizeSpeech.mockRejectedValue(new Error('model download failed'));

        const { toggleSpeak } = await import('./tts-player');

        await expect(toggleSpeak('message', 'text')).rejects.toThrow('model download failed');
    });

    // Regression guard for the automation/notification auto-speak paths: these
    // have no manual speaker button, so they must NOT inherit toggleSpeak's
    // "same id while active -> stop" semantics. speakOnce gives every call a
    // fresh id; identical announcement text must still interrupt-and-play, not
    // cancel itself into silence.
    it('speakOnce replays identical text instead of toggling itself off', async () => {
        // Hold the first synthesis open so the first playback is still active
        // when the second identical-text call arrives — the exact overlap a
        // text-keyed id would mis-handle as a stop.
        let resolveFirst!: (audio: string) => void;
        const first = new Promise<string>((resolve) => {
            resolveFirst = resolve;
        });
        bindings.splitSpeechText.mockResolvedValue(['same']);
        bindings.synthesizeSpeech
            .mockReturnValueOnce(first)
            .mockResolvedValue('');

        const { speakOnce } = await import('./tts-player');
        const firstPlayback = speakOnce('same announcement');
        await vi.waitFor(() => {
            expect(bindings.synthesizeSpeech).toHaveBeenCalledTimes(1);
        });

        // Second call, identical text, while the first is still active.
        await speakOnce('same announcement');

        // The second call reached synthesis — it interrupted and played rather
        // than short-circuiting via toggle-off.
        expect(bindings.synthesizeSpeech).toHaveBeenCalledTimes(2);

        resolveFirst('');
        await firstPlayback;
    });

    // Documents the hazard speakOnce avoids: a text/id-keyed call repeated
    // while active is a deliberate stop (needed for the manual button), which
    // is why auto-speak paths use speakOnce instead.
    it('toggleSpeak with the same id while active stops without replaying', async () => {
        let resolveFirst!: (audio: string) => void;
        const first = new Promise<string>((resolve) => {
            resolveFirst = resolve;
        });
        bindings.splitSpeechText.mockResolvedValue(['same']);
        bindings.synthesizeSpeech
            .mockReturnValueOnce(first)
            .mockResolvedValue('');

        const { toggleSpeak } = await import('./tts-player');
        const firstPlayback = toggleSpeak('dup-id', 'same text');
        await vi.waitFor(() => {
            expect(bindings.synthesizeSpeech).toHaveBeenCalledTimes(1);
        });

        // Same id while active -> stop, no second synthesis.
        await toggleSpeak('dup-id', 'same text');
        expect(bindings.synthesizeSpeech).toHaveBeenCalledTimes(1);

        resolveFirst('');
        await firstPlayback;
    });
});
