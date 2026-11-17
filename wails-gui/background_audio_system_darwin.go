//go:build darwin

package main

/*
#cgo LDFLAGS: -framework CoreAudio -framework Foundation -framework AppKit
#include <stdlib.h>
#include <stdbool.h>
#include <string.h>
#include <stdatomic.h>
#include <stdint.h>
#include <unistd.h>

#import <Foundation/Foundation.h>
#import <AppKit/AppKit.h>
#import <CoreAudio/AudioHardware.h>
#import <CoreAudio/AudioHardwareTapping.h>
#import <CoreAudio/CATapDescription.h>
#import <CoreAudioTypes/CoreAudioBaseTypes.h>

// Implemented in Go (see media_playback_apps_export_darwin.go). Returns true
// when the given bundle ID is a known dedicated media-playback app whose
// audio should be excluded from the system audio process tap.
extern bool goIsMediaPlaybackApp(char *bundleID);

typedef struct {
    char     riffTag[4];
    uint32_t fileSize;
    char     waveTag[4];
    char     fmtTag[4];
    uint32_t fmtSize;
    uint16_t audioFormat;
    uint16_t numChannels;
    uint32_t sampleRate;
    uint32_t byteRate;
    uint16_t blockAlign;
    uint16_t bitsPerSample;
    char     dataTag[4];
    uint32_t dataSize;
} SystemAudioWavHeader;

#define SYSTEM_AUDIO_TARGET_RATE 16000
#define SYSTEM_AUDIO_RING_SECONDS 60
#define SYSTEM_AUDIO_RING_CAPACITY (SYSTEM_AUDIO_TARGET_RATE * SYSTEM_AUDIO_RING_SECONDS)

// The process-tap IOProc runs on a CoreAudio realtime thread. Keep capture to
// preallocated C memory and atomics; ReadChunk handles waiting, logging, and
// WAV allocation on a normal Go-owned thread.
typedef struct {
    AudioObjectID                  tapID;
    AudioObjectID                  aggregateDeviceID;
    AudioDeviceIOProcID            ioProcID;
    int16_t                       *ringBuffer;
    uint32_t                       ringCapacity;
    AudioStreamBasicDescription    format;
    double                         resamplePosition;
    _Atomic(bool)                  recording;
    _Atomic(bool)                  unsupportedFormat;
    _Atomic(bool)                  overflowed;
    _Atomic(uint64_t)              ringReadIndex;
    _Atomic(uint64_t)              ringWriteIndex;
} SystemAudioRecorderState;

static void _system_audio_log(const char *fmt, ...) {
    char buf[1024];
    va_list ap;
    va_start(ap, fmt);
    vsnprintf(buf, sizeof(buf), fmt, ap);
    va_end(ap);
    NSLog(@"%s", buf);
}

static bool SystemAudioRecorder_Supported(void) {
    if (@available(macOS 14.2, *)) {
        return true;
    }
    return false;
}

static bool _system_audio_query_process_object_id(pid_t pid, AudioObjectID *outProcessID) {
    if (!outProcessID) return false;
    *outProcessID = kAudioObjectUnknown;

    AudioObjectPropertyAddress addr = {
        kAudioHardwarePropertyTranslatePIDToProcessObject,
        kAudioObjectPropertyScopeGlobal,
        kAudioObjectPropertyElementMain
    };
    UInt32 size = sizeof(AudioObjectID);
    pid_t qualifier = pid;
    OSStatus status = AudioObjectGetPropertyData(
        kAudioObjectSystemObject,
        &addr,
        sizeof(qualifier),
        &qualifier,
        &size,
        outProcessID
    );
    return status == noErr && *outProcessID != kAudioObjectUnknown;
}

static NSString* _system_audio_copy_tap_uid(AudioObjectID tapID) {
    AudioObjectPropertyAddress addr = {
        kAudioTapPropertyUID,
        kAudioObjectPropertyScopeGlobal,
        kAudioObjectPropertyElementMain
    };
    CFStringRef uidRef = NULL;
    UInt32 size = sizeof(uidRef);
    OSStatus status = AudioObjectGetPropertyData(tapID, &addr, 0, NULL, &size, &uidRef);
    if (status != noErr || uidRef == NULL) {
        return nil;
    }
    NSString *uid = [((NSString *)uidRef) copy];
    CFRelease(uidRef);
    return uid;
}

static bool _system_audio_copy_tap_format(AudioObjectID tapID, AudioStreamBasicDescription *outFormat) {
    if (!outFormat) return false;
    memset(outFormat, 0, sizeof(*outFormat));

    AudioObjectPropertyAddress addr = {
        kAudioTapPropertyFormat,
        kAudioObjectPropertyScopeGlobal,
        kAudioObjectPropertyElementMain
    };
    UInt32 size = sizeof(*outFormat);
    OSStatus status = AudioObjectGetPropertyData(tapID, &addr, 0, NULL, &size, outFormat);
    return status == noErr;
}

static bool _system_audio_create_aggregate_device(NSString *tapUID, AudioObjectID *outDeviceID) {
    if (!tapUID || !outDeviceID) return false;

    NSDictionary *tapEntry = @{
        @kAudioSubTapUIDKey: tapUID,
        @kAudioSubTapDriftCompensationKey: @YES,
    };
    NSDictionary *aggregateDesc = @{
        @kAudioAggregateDeviceNameKey: @"Isoform System Audio Capture",
        @kAudioAggregateDeviceUIDKey: [[NSUUID UUID] UUIDString],
        @kAudioAggregateDeviceTapListKey: @[tapEntry],
        @kAudioAggregateDeviceIsPrivateKey: @YES,
    };

    OSStatus status = AudioHardwareCreateAggregateDevice((__bridge CFDictionaryRef)aggregateDesc, outDeviceID);
    if (status != noErr) {
        _system_audio_log("[background-audio] AudioHardwareCreateAggregateDevice failed: %d", (int)status);
        return false;
    }
    return true;
}

static uint64_t _system_audio_available_samples(SystemAudioRecorderState *state) {
    uint64_t readIndex = atomic_load_explicit(&state->ringReadIndex, memory_order_acquire);
    uint64_t writeIndex = atomic_load_explicit(&state->ringWriteIndex, memory_order_acquire);
    if (writeIndex < readIndex) return 0;
    return writeIndex - readIndex;
}

static void _system_audio_ring_write(SystemAudioRecorderState *state, int16_t sample) {
    if (!state || !state->ringBuffer || state->ringCapacity == 0) return;

    uint64_t writeIndex = atomic_load_explicit(&state->ringWriteIndex, memory_order_relaxed);
    uint64_t readIndex = atomic_load_explicit(&state->ringReadIndex, memory_order_acquire);
    if (writeIndex - readIndex >= state->ringCapacity) {
        atomic_store_explicit(&state->overflowed, true, memory_order_release);
    }

    state->ringBuffer[writeIndex % state->ringCapacity] = sample;
    atomic_store_explicit(&state->ringWriteIndex, writeIndex + 1, memory_order_release);
}

static float _system_audio_read_sample(const void *data, UInt32 index, const AudioStreamBasicDescription *format, bool *ok) {
    bool isFloat = (format->mFormatFlags & kAudioFormatFlagIsFloat) != 0;
    bool isSignedInt = (format->mFormatFlags & kAudioFormatFlagIsSignedInteger) != 0;

    if (isFloat && format->mBitsPerChannel == 32) {
        return ((const float *)data)[index];
    }
    if (isFloat && format->mBitsPerChannel == 64) {
        return (float)((const double *)data)[index];
    }
    if (isSignedInt && format->mBitsPerChannel == 16) {
        return ((float)((const int16_t *)data)[index]) / 32768.0f;
    }
    if (isSignedInt && format->mBitsPerChannel == 32) {
        return ((float)((const int32_t *)data)[index]) / 2147483648.0f;
    }

    if (ok) *ok = false;
    return 0.0f;
}

static bool _system_audio_buffer_shape(const AudioBufferList *bufferList,
                                       const AudioStreamBasicDescription *format,
                                       bool *outNonInterleaved,
                                       UInt32 *outChannels,
                                       UInt32 *outBytesPerSample,
                                       UInt32 *outFrames) {
    if (!bufferList || !format || !outNonInterleaved || !outChannels || !outBytesPerSample || !outFrames) {
        return false;
    }
    if (format->mFormatID != kAudioFormatLinearPCM || format->mSampleRate <= 0 || bufferList->mNumberBuffers == 0) {
        return false;
    }

    UInt32 bytesPerSample = format->mBitsPerChannel / 8;
    if (bytesPerSample == 0) {
        return false;
    }

    bool nonInterleaved = (format->mFormatFlags & kAudioFormatFlagIsNonInterleaved) != 0;
    UInt32 channels = format->mChannelsPerFrame;
    UInt32 frames = 0;

    if (nonInterleaved) {
        UInt32 limit = channels;
        if (limit == 0 || limit > bufferList->mNumberBuffers) {
            limit = bufferList->mNumberBuffers;
        }

        UInt32 usedBuffers = 0;
        frames = UINT32_MAX;
        for (UInt32 ch = 0; ch < limit; ch++) {
            const AudioBuffer *buffer = &bufferList->mBuffers[ch];
            if (!buffer->mData) continue;

            UInt32 bufferFrames = buffer->mDataByteSize / bytesPerSample;
            if (bufferFrames == 0) continue;
            if (bufferFrames < frames) frames = bufferFrames;
            usedBuffers++;
        }
        if (usedBuffers == 0 || frames == UINT32_MAX || frames == 0) {
            return false;
        }
        channels = limit;
    } else {
        const AudioBuffer *buffer = &bufferList->mBuffers[0];
        if (!buffer->mData) {
            return false;
        }
        if (channels == 0) {
            channels = buffer->mNumberChannels;
        }
        if (channels == 0) {
            return false;
        }
        frames = buffer->mDataByteSize / (bytesPerSample * channels);
        if (frames == 0) {
            return false;
        }
    }

    *outNonInterleaved = nonInterleaved;
    *outChannels = channels;
    *outBytesPerSample = bytesPerSample;
    *outFrames = frames;
    return true;
}

static bool _system_audio_mono_sample(const AudioBufferList *bufferList,
                                      const AudioStreamBasicDescription *format,
                                      bool nonInterleaved,
                                      UInt32 channels,
                                      UInt32 bytesPerSample,
                                      UInt32 frame,
                                      float *outSample) {
    if (!bufferList || !format || !outSample || channels == 0 || bytesPerSample == 0) {
        return false;
    }

    bool supported = true;
    float mixed = 0.0f;
    UInt32 usedChannels = 0;

    if (nonInterleaved) {
        UInt32 limit = channels < bufferList->mNumberBuffers ? channels : bufferList->mNumberBuffers;
        for (UInt32 ch = 0; ch < limit; ch++) {
            const AudioBuffer *buffer = &bufferList->mBuffers[ch];
            const void *samples = buffer->mData;
            if (!samples) continue;
            if (frame >= buffer->mDataByteSize / bytesPerSample) continue;

            mixed += _system_audio_read_sample(samples, frame, format, &supported);
            usedChannels++;
        }
    } else {
        const AudioBuffer *buffer = &bufferList->mBuffers[0];
        const void *samples = buffer->mData;
        if (!samples) return false;
        if (frame >= buffer->mDataByteSize / (bytesPerSample * channels)) return false;

        for (UInt32 ch = 0; ch < channels; ch++) {
            mixed += _system_audio_read_sample(samples, (frame * channels) + ch, format, &supported);
            usedChannels++;
        }
    }

    if (!supported || usedChannels == 0) return false;
    *outSample = mixed / (float)usedChannels;
    return true;
}

static int16_t _system_audio_float_to_int16(float sample) {
    if (sample > 1.0f) sample = 1.0f;
    if (sample < -1.0f) sample = -1.0f;
    return (int16_t)(sample * 32767.0f);
}

static OSStatus _system_audio_io_proc(AudioObjectID inDevice,
                                      const AudioTimeStamp *inNow,
                                      const AudioBufferList *inInputData,
                                      const AudioTimeStamp *inInputTime,
                                      AudioBufferList *outOutputData,
                                      const AudioTimeStamp *inOutputTime,
                                      void *inClientData) {
    (void)inDevice;
    (void)inNow;
    (void)inInputTime;
    (void)outOutputData;
    (void)inOutputTime;

    SystemAudioRecorderState *state = (SystemAudioRecorderState *)inClientData;
    if (!state || !atomic_load_explicit(&state->recording, memory_order_acquire) || !inInputData) {
        return noErr;
    }

    bool nonInterleaved = false;
    UInt32 channels = 0;
    UInt32 bytesPerSample = 0;
    UInt32 sourceFrames = 0;
    if (!_system_audio_buffer_shape(inInputData, &state->format, &nonInterleaved, &channels, &bytesPerSample, &sourceFrames)) {
        atomic_store_explicit(&state->unsupportedFormat, true, memory_order_release);
        return noErr;
    }

    double ratio = state->format.mSampleRate / (double)SYSTEM_AUDIO_TARGET_RATE;
    if (ratio <= 0) {
        atomic_store_explicit(&state->unsupportedFormat, true, memory_order_release);
        return noErr;
    }

    double position = state->resamplePosition;
    while (position < (double)sourceFrames) {
        UInt32 frame = (UInt32)position;
        if (frame >= sourceFrames) break;

        float sample = 0.0f;
        if (!_system_audio_mono_sample(inInputData, &state->format, nonInterleaved, channels, bytesPerSample, frame, &sample)) {
            atomic_store_explicit(&state->unsupportedFormat, true, memory_order_release);
            return noErr;
        }

        _system_audio_ring_write(state, _system_audio_float_to_int16(sample));
        position += ratio;
    }
    state->resamplePosition = position - (double)sourceFrames;
    return noErr;
}

void* SystemAudioRecorder_Create(void) {
    @autoreleasepool {
        if (!SystemAudioRecorder_Supported()) {
            return NULL;
        }

        SystemAudioRecorderState *state = (SystemAudioRecorderState *)calloc(1, sizeof(SystemAudioRecorderState));
        if (!state) return NULL;

        state->tapID = kAudioObjectUnknown;
        state->aggregateDeviceID = kAudioObjectUnknown;
        state->ioProcID = NULL;
        state->ringCapacity = SYSTEM_AUDIO_RING_CAPACITY;
        state->ringBuffer = (int16_t *)calloc(state->ringCapacity, sizeof(int16_t));
        if (!state->ringBuffer) {
            free(state);
            return NULL;
        }
        atomic_init(&state->recording, false);
        atomic_init(&state->unsupportedFormat, false);
        atomic_init(&state->overflowed, false);
        atomic_init(&state->ringReadIndex, 0);
        atomic_init(&state->ringWriteIndex, 0);
        return state;
    }
}

static void _system_audio_stop_device(SystemAudioRecorderState *state) {
    if (!state) return;
    atomic_store_explicit(&state->recording, false, memory_order_release);

    if (state->aggregateDeviceID != kAudioObjectUnknown && state->ioProcID != NULL) {
        AudioDeviceStop(state->aggregateDeviceID, state->ioProcID);
        AudioDeviceDestroyIOProcID(state->aggregateDeviceID, state->ioProcID);
        state->ioProcID = NULL;
    }

    if (state->aggregateDeviceID != kAudioObjectUnknown) {
        AudioHardwareDestroyAggregateDevice(state->aggregateDeviceID);
        state->aggregateDeviceID = kAudioObjectUnknown;
    }

    if (state->tapID != kAudioObjectUnknown) {
        AudioHardwareDestroyProcessTap(state->tapID);
        state->tapID = kAudioObjectUnknown;
    }
}

bool SystemAudioRecorder_Start(void *ref) {
    @autoreleasepool {
        if (!ref) return false;
        if (!SystemAudioRecorder_Supported()) {
            _system_audio_log("[background-audio] system audio capture requires macOS 14.2+");
            return false;
        }

        SystemAudioRecorderState *state = (SystemAudioRecorderState *)ref;
        _system_audio_stop_device(state);

        NSMutableArray<NSNumber *> *excluded = [NSMutableArray array];
        AudioObjectID processObjectID = kAudioObjectUnknown;
        if (_system_audio_query_process_object_id(getpid(), &processObjectID)) {
            [excluded addObject:@(processObjectID)];
        }

        // Exclude known media-playback apps (Spotify, VLC, IINA, Apple Music,
        // Bilibili desktop, etc.) at OS level so their audio never reaches
        // the transcription pipeline. The bundle ID list lives on the Go
        // side (media_playback_apps.go) so it can be shared with the
        // post-capture filter in activity_triage.go — single source of
        // truth. Conservative by design: browsers and chat apps are NOT in
        // the list because they also carry legitimate meeting audio.
        //
        // Known limitation: this only excludes apps already running when
        // the tap is created. Media apps launched after Start() are not
        // covered until the tap is rebuilt (follow-up PR).
        NSArray<NSRunningApplication *> *running = [[NSWorkspace sharedWorkspace] runningApplications];
        int excludedMediaAppCount = 0;
        for (NSRunningApplication *app in running) {
            NSString *bundleID = app.bundleIdentifier;
            if (bundleID == nil) continue;

            const char *bundleIDC = [bundleID UTF8String];
            if (bundleIDC == NULL) continue;
            if (!goIsMediaPlaybackApp((char *)bundleIDC)) continue;

            AudioObjectID mediaProcID = kAudioObjectUnknown;
            if (_system_audio_query_process_object_id(app.processIdentifier, &mediaProcID)) {
                [excluded addObject:@(mediaProcID)];
                excludedMediaAppCount++;
                _system_audio_log(
                    "[background-audio] excluding %s (pid=%d) from system tap",
                    bundleIDC, app.processIdentifier
                );
            }
        }
        _system_audio_log(
            "[background-audio] system tap excluded %d media app(s) at startup",
            excludedMediaAppCount
        );

        CATapDescription *tapDescription = [[CATapDescription alloc] initMonoGlobalTapButExcludeProcesses:excluded];
        tapDescription.name = @"Isoform System Audio Tap";
        tapDescription.UUID = [NSUUID UUID];
        tapDescription.privateTap = YES;
        tapDescription.muteBehavior = CATapUnmuted;

        OSStatus status = AudioHardwareCreateProcessTap(tapDescription, &state->tapID);
        [tapDescription release];
        if (status != noErr) {
            _system_audio_log("[background-audio] AudioHardwareCreateProcessTap failed: %d", (int)status);
            return false;
        }

        if (!_system_audio_copy_tap_format(state->tapID, &state->format)) {
            _system_audio_log("[background-audio] failed to read tap format");
            _system_audio_stop_device(state);
            return false;
        }
        if (state->format.mSampleRate <= 0 || state->format.mFormatID == 0) {
            _system_audio_log("[background-audio] invalid tap format: formatID=%u rate=%.0f",
                (unsigned)state->format.mFormatID,
                state->format.mSampleRate
            );
            _system_audio_stop_device(state);
            return false;
        }

        NSString *tapUID = _system_audio_copy_tap_uid(state->tapID);
        if (!tapUID) {
            _system_audio_log("[background-audio] failed to read tap UID");
            _system_audio_stop_device(state);
            return false;
        }

        bool aggregateOK = _system_audio_create_aggregate_device(tapUID, &state->aggregateDeviceID);
        [tapUID release];
        if (!aggregateOK) {
            _system_audio_stop_device(state);
            return false;
        }

        state->resamplePosition = 0;
        atomic_store_explicit(&state->unsupportedFormat, false, memory_order_release);
        atomic_store_explicit(&state->overflowed, false, memory_order_release);
        atomic_store_explicit(&state->ringReadIndex, 0, memory_order_release);
        atomic_store_explicit(&state->ringWriteIndex, 0, memory_order_release);

        status = AudioDeviceCreateIOProcID(state->aggregateDeviceID, _system_audio_io_proc, state, &state->ioProcID);
        if (status != noErr) {
            _system_audio_log("[background-audio] AudioDeviceCreateIOProcID failed: %d", (int)status);
            _system_audio_stop_device(state);
            return false;
        }

        atomic_store_explicit(&state->recording, true, memory_order_release);
        status = AudioDeviceStart(state->aggregateDeviceID, state->ioProcID);
        if (status != noErr) {
            _system_audio_log("[background-audio] AudioDeviceStart failed: %d", (int)status);
            _system_audio_stop_device(state);
            return false;
        }

        _system_audio_log(
            "[background-audio] system audio recorder started: rate=%.0f ch=%u bits=%u flags=%u",
            state->format.mSampleRate,
            (unsigned)state->format.mChannelsPerFrame,
            (unsigned)state->format.mBitsPerChannel,
            (unsigned)state->format.mFormatFlags
        );
        return true;
    }
}

void SystemAudioRecorder_Stop(void *ref) {
    @autoreleasepool {
        if (!ref) return;
        SystemAudioRecorderState *state = (SystemAudioRecorderState *)ref;
        _system_audio_stop_device(state);
    }
}

void SystemAudioRecorder_Destroy(void *ref) {
    @autoreleasepool {
        if (!ref) return;
        SystemAudioRecorderState *state = (SystemAudioRecorderState *)ref;
        _system_audio_stop_device(state);

        if (state->ringBuffer) {
            free(state->ringBuffer);
            state->ringBuffer = NULL;
        }
        free(state);
    }
}

void* SystemAudioRecorder_ReadChunk(void *ref, int seconds, int *outLen) {
    @autoreleasepool {
        if (!outLen) return NULL;
        *outLen = 0;
        if (!ref || seconds <= 0) return NULL;
        SystemAudioRecorderState *state = (SystemAudioRecorderState *)ref;

        uint32_t targetSamples = (uint32_t)(seconds * SYSTEM_AUDIO_TARGET_RATE);
        int maxWaitIterations = (seconds + 1) * 50;
        int waited = 0;
        while (atomic_load_explicit(&state->recording, memory_order_acquire) &&
               _system_audio_available_samples(state) < targetSamples &&
               waited < maxWaitIterations) {
            usleep(20000);
            waited++;
        }

        if (atomic_exchange_explicit(&state->unsupportedFormat, false, memory_order_acq_rel)) {
            _system_audio_log(
                "[background-audio] unsupported system audio format: formatID=%u flags=%u bits=%u channels=%u rate=%.0f",
                (unsigned)state->format.mFormatID,
                (unsigned)state->format.mFormatFlags,
                (unsigned)state->format.mBitsPerChannel,
                (unsigned)state->format.mChannelsPerFrame,
                state->format.mSampleRate
            );
        }
        if (atomic_exchange_explicit(&state->overflowed, false, memory_order_acq_rel)) {
            _system_audio_log("[background-audio] system audio ring buffer overflowed; dropped oldest samples");
        }

        uint64_t readIndex = atomic_load_explicit(&state->ringReadIndex, memory_order_acquire);
        uint64_t writeIndex = atomic_load_explicit(&state->ringWriteIndex, memory_order_acquire);
        if (writeIndex < readIndex) {
            return NULL;
        }
        uint64_t available = writeIndex - readIndex;
        if (available > state->ringCapacity) {
            readIndex = writeIndex - state->ringCapacity;
            available = state->ringCapacity;
            atomic_store_explicit(&state->ringReadIndex, readIndex, memory_order_release);
        }
        if (available == 0) {
            return NULL;
        }

        uint32_t sampleCount = (uint32_t)available;
        if (sampleCount > targetSamples) sampleCount = targetSamples;
        uint32_t dataSize = sampleCount * sizeof(int16_t);
        uint32_t fileSize = 36 + dataSize;
        uint32_t totalSize = sizeof(SystemAudioWavHeader) + dataSize;

        void *wavBuf = malloc(totalSize);
        if (!wavBuf) {
            return NULL;
        }

        SystemAudioWavHeader hdr;
        memcpy(hdr.riffTag, "RIFF", 4);
        hdr.fileSize = fileSize;
        memcpy(hdr.waveTag, "WAVE", 4);
        memcpy(hdr.fmtTag, "fmt ", 4);
        hdr.fmtSize = 16;
        hdr.audioFormat = 1;
        hdr.numChannels = 1;
        hdr.sampleRate = 16000;
        hdr.byteRate = 32000;
        hdr.blockAlign = 2;
        hdr.bitsPerSample = 16;
        memcpy(hdr.dataTag, "data", 4);
        hdr.dataSize = dataSize;

        memcpy(wavBuf, &hdr, sizeof(SystemAudioWavHeader));
        int16_t *pcmOut = (int16_t *)((char *)wavBuf + sizeof(SystemAudioWavHeader));
        for (uint32_t i = 0; i < sampleCount; i++) {
            pcmOut[i] = state->ringBuffer[(readIndex + i) % state->ringCapacity];
        }
        atomic_store_explicit(&state->ringReadIndex, readIndex + sampleCount, memory_order_release);

        *outLen = (int)totalSize;
        return wavBuf;
    }
}

void SystemAudioRecorder_Free(void *buf) {
    free(buf);
}
*/
import "C"

import (
	"fmt"
	"log"
	"sync"
	"time"
	"unsafe"

	"wails-gui/internal/platform/activitymon"
)

type darwinSystemAudioRecorder struct {
	mu     sync.Mutex
	ref    unsafe.Pointer
	readWG sync.WaitGroup
}

func darwinSystemAudioCaptureSupported() bool {
	return bool(C.SystemAudioRecorder_Supported())
}

func newDarwinSystemAudioRecorder() audioRecorder {
	return &darwinSystemAudioRecorder{}
}

func (r *darwinSystemAudioRecorder) Start() error {
	if !darwinSystemAudioCaptureSupported() {
		return fmt.Errorf("system audio background listening requires macOS 14.2 or later")
	}
	// Creating the process tap is what shows the System Audio Recording
	// dialog. Until the user has granted it once (recorded in config after
	// the first working capture), only a user-driven request may create it.
	if !loadConfig().SystemAudioPermissionGranted && !permissionPromptsAllowed() {
		log.Printf("[background-audio] system audio permission not granted yet; not asking outside a user request")
		recordAudioCaptureEvent(AudioCaptureEvent{
			Source: "system_audio", Kind: "skipped",
			Message: "system audio permission not granted yet; grant it in Settings > What Isoform sees",
		})
		return fmt.Errorf("system audio: %w", errPermissionNotRequested)
	}

	ref := C.SystemAudioRecorder_Create()
	if ref == nil {
		return fmt.Errorf("failed to create system audio recorder")
	}
	if !C.SystemAudioRecorder_Start(ref) {
		C.SystemAudioRecorder_Destroy(ref)
		return fmt.Errorf("failed to start system audio recorder (check system audio recording permission)")
	}

	r.mu.Lock()
	r.ref = ref
	r.mu.Unlock()
	return nil
}

func (r *darwinSystemAudioRecorder) ReadChunk(d time.Duration) (capturedAudioChunk, error) {
	r.mu.Lock()
	ref := r.ref
	if ref == nil {
		r.mu.Unlock()
		return capturedAudioChunk{}, fmt.Errorf("system audio recorder not initialized")
	}
	r.readWG.Add(1)
	r.mu.Unlock()
	defer r.readWG.Done()

	seconds := int(d.Seconds())
	if seconds < 1 {
		seconds = 1
	}

	var outLen C.int
	buf := C.SystemAudioRecorder_ReadChunk(ref, C.int(seconds), &outLen)
	if buf == nil || outLen <= 0 {
		return capturedAudioChunk{Source: backgroundListeningSourceSystem}, nil
	}
	defer C.SystemAudioRecorder_Free(buf)

	// CapturedAt set here in the recorder goroutine — see the matching
	// comment in darwinAudioRecorder.ReadChunk. Consumer-side timestamps
	// are subject to channel arrival order between the two readLoops in
	// darwinCombinedAudioRecorder and break echo-window pairing.
	return capturedAudioChunk{
		CaptureID:   nextAudioCaptureIdentity(),
		Source:      backgroundListeningSourceSystem,
		WavData:     C.GoBytes(buf, outLen),
		Duration:    time.Duration(int(outLen)-wavHeaderSize) * time.Second / meetingAudioBytesPerSecond,
		CapturedAt:  time.Now(),
		CapturedApp: activitymon.FrontmostAppInfo(),
	}, nil
}

func (r *darwinSystemAudioRecorder) Stop() {
	r.mu.Lock()
	ref := r.ref
	if ref == nil {
		r.mu.Unlock()
		return
	}
	r.ref = nil
	r.mu.Unlock()

	C.SystemAudioRecorder_Stop(ref)
	r.readWG.Wait()
	C.SystemAudioRecorder_Destroy(ref)
}

func (r *darwinSystemAudioRecorder) ActiveSources() []backgroundListeningSource {
	return []backgroundListeningSource{backgroundListeningSourceSystem}
}
