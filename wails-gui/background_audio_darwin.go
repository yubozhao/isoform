//go:build darwin

package main

/*
#cgo LDFLAGS: -framework AVFoundation -framework Foundation -framework CoreAudio
#include <stdlib.h>
#include <stdbool.h>
#include <stdatomic.h>
#include <stdint.h>
#include <string.h>
#include <unistd.h>
#include <dispatch/dispatch.h>

#import <AVFoundation/AVFoundation.h>
#import <CoreAudio/CoreAudio.h>

// WAV header for 16 kHz mono 16-bit PCM.
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
} WavHeader;

#define CORE_AUDIO_INPUT_TARGET_RATE 16000
#define CORE_AUDIO_INPUT_RING_SECONDS 60
#define CORE_AUDIO_INPUT_RING_CAPACITY (CORE_AUDIO_INPUT_TARGET_RATE * CORE_AUDIO_INPUT_RING_SECONDS)

// Forward declaration for the Go-exported event sink. Implemented in
// audio_capture_events_export_darwin.go; invoked by native recorders so the
// DevTools panel can show capture lifecycle events in real time.
extern void goAudioCaptureEvent(char *source, char *kind, char *outcome, char *message);

static void _audio_log(const char *fmt, ...) {
    char buf[1024];
    va_list ap;
    va_start(ap, fmt);
    vsnprintf(buf, sizeof(buf), fmt, ap);
    va_end(ap);
    NSLog(@"%s", buf);
}

// _emit_audio_event is a small wrapper around goAudioCaptureEvent that
// handles the C-string allocations for the four fields. The Go side
// appends to a ring buffer and fires a Wails event.
static void _emit_audio_event(const char *source, const char *kind, const char *outcome, const char *message) {
    char *cSource  = source  ? strdup(source)  : strdup("");
    char *cKind    = kind    ? strdup(kind)    : strdup("");
    char *cOutcome = outcome ? strdup(outcome) : strdup("");
    char *cMessage = message ? strdup(message) : strdup("");
    goAudioCaptureEvent(cSource, cKind, cOutcome, cMessage);
    free(cSource);
    free(cKind);
    free(cOutcome);
    free(cMessage);
}

// CoreAudioInputRecorder_CheckMicPermission returns the current authorization
// status. 0 = notDetermined, 1 = restricted, 2 = denied, 3 = authorized.
int CoreAudioInputRecorder_CheckMicPermission(void) {
    @autoreleasepool {
#if __MAC_OS_X_VERSION_MAX_ALLOWED >= 101400
        if (@available(macOS 10.14, *)) {
            AVAuthorizationStatus status = [AVCaptureDevice authorizationStatusForMediaType:AVMediaTypeAudio];
            const char *statusStr = "unknown";
            switch (status) {
                case AVAuthorizationStatusNotDetermined: statusStr = "notDetermined"; break;
                case AVAuthorizationStatusRestricted:    statusStr = "restricted"; break;
                case AVAuthorizationStatusDenied:        statusStr = "denied"; break;
                case AVAuthorizationStatusAuthorized:    statusStr = "authorized"; break;
            }
            _audio_log("[background-audio] mic auth status: %s (%d)", statusStr, (int)status);
            return (int)status;
        }
#endif
        _audio_log("[background-audio] mic auth status: authorized (pre-10.14)");
        return 3;
    }
}

// _audio_default_input_device returns the system's current default input
// device, or kAudioObjectUnknown on failure.
static AudioDeviceID _audio_default_input_device(void) {
    AudioDeviceID id = kAudioObjectUnknown;
    AudioObjectPropertyAddress addr = {
        kAudioHardwarePropertyDefaultInputDevice,
        kAudioObjectPropertyScopeGlobal,
        kAudioObjectPropertyElementMain
    };
    UInt32 size = sizeof(id);
    AudioObjectGetPropertyData(kAudioObjectSystemObject, &addr, 0, NULL, &size, &id);
    return id;
}

// _audio_transport_type returns the transport type of the given device, or 0
// on failure. Relevant values include kAudioDeviceTransportTypeBluetooth,
// kAudioDeviceTransportTypeBluetoothLE, kAudioDeviceTransportTypeBuiltIn.
static UInt32 _audio_transport_type(AudioDeviceID dev) {
    UInt32 transport = 0;
    AudioObjectPropertyAddress addr = {
        kAudioDevicePropertyTransportType,
        kAudioObjectPropertyScopeGlobal,
        kAudioObjectPropertyElementMain
    };
    UInt32 size = sizeof(transport);
    AudioObjectGetPropertyData(dev, &addr, 0, NULL, &size, &transport);
    return transport;
}

// _audio_find_input_by_transport scans all audio devices for one matching
// the given transport type that has at least one input stream. Returns
// kAudioObjectUnknown if nothing matches.
static AudioDeviceID _audio_find_input_by_transport(UInt32 transportType) {
    AudioObjectPropertyAddress devAddr = {
        kAudioHardwarePropertyDevices,
        kAudioObjectPropertyScopeGlobal,
        kAudioObjectPropertyElementMain
    };
    UInt32 size = 0;
    if (AudioObjectGetPropertyDataSize(kAudioObjectSystemObject, &devAddr, 0, NULL, &size) != noErr || size == 0) {
        return kAudioObjectUnknown;
    }
    UInt32 count = size / sizeof(AudioDeviceID);
    AudioDeviceID *devices = (AudioDeviceID *)malloc(size);
    if (!devices) return kAudioObjectUnknown;
    if (AudioObjectGetPropertyData(kAudioObjectSystemObject, &devAddr, 0, NULL, &size, devices) != noErr) {
        free(devices);
        return kAudioObjectUnknown;
    }

    AudioDeviceID result = kAudioObjectUnknown;
    for (UInt32 i = 0; i < count; i++) {
        AudioDeviceID dev = devices[i];

        AudioObjectPropertyAddress streamAddr = {
            kAudioDevicePropertyStreams,
            kAudioDevicePropertyScopeInput,
            kAudioObjectPropertyElementMain
        };
        UInt32 streamSize = 0;
        if (AudioObjectGetPropertyDataSize(dev, &streamAddr, 0, NULL, &streamSize) != noErr || streamSize == 0) {
            continue;
        }
        if (_audio_transport_type(dev) == transportType) {
            result = dev;
            break;
        }
    }
    free(devices);
    return result;
}

// CoreAudioInputRecorder_* is the background microphone recorder used by the
// Go darwinAudioRecorder below. It intentionally avoids AVAudioEngine/AUHAL:
// release builds crashed when AVAudioEngine rebound its IO unit while a system
// audio process tap was active and the user switched playback devices.
typedef struct {
    AudioDeviceID                  deviceID;
    AudioDeviceIOProcID            ioProcID;
    int16_t                       *ringBuffer;
    uint32_t                       ringCapacity;
    AudioStreamBasicDescription    format;
    double                         resamplePosition;
    _Atomic(bool)                  recording;
    _Atomic(bool)                  needsRestart;
    _Atomic(bool)                  unsupportedFormat;
    _Atomic(bool)                  overflowed;
    _Atomic(uint64_t)              ringReadIndex;
    _Atomic(uint64_t)              ringWriteIndex;
    bool                           defaultInputListenerInstalled;
    bool                           streamFormatListenerInstalled;
    bool                           sampleRateListenerInstalled;
} CoreAudioInputRecorderState;

static UInt32 _core_audio_input_channel_count(AudioDeviceID deviceID) {
    AudioObjectPropertyAddress addr = {
        kAudioDevicePropertyStreamConfiguration,
        kAudioDevicePropertyScopeInput,
        kAudioObjectPropertyElementMain
    };
    UInt32 size = 0;
    if (AudioObjectGetPropertyDataSize(deviceID, &addr, 0, NULL, &size) != noErr || size == 0) {
        return 0;
    }

    AudioBufferList *bufferList = (AudioBufferList *)malloc(size);
    if (!bufferList) return 0;
    UInt32 channels = 0;
    if (AudioObjectGetPropertyData(deviceID, &addr, 0, NULL, &size, bufferList) == noErr) {
        for (UInt32 i = 0; i < bufferList->mNumberBuffers; i++) {
            channels += bufferList->mBuffers[i].mNumberChannels;
        }
    }
    free(bufferList);
    return channels;
}

static bool _core_audio_input_copy_device_format(AudioDeviceID deviceID, AudioStreamBasicDescription *outFormat) {
    if (!outFormat) return false;
    memset(outFormat, 0, sizeof(*outFormat));

    AudioObjectPropertyAddress addr = {
        kAudioDevicePropertyStreamFormat,
        kAudioDevicePropertyScopeInput,
        kAudioObjectPropertyElementMain
    };
    UInt32 size = sizeof(*outFormat);
    OSStatus status = AudioObjectGetPropertyData(deviceID, &addr, 0, NULL, &size, outFormat);
    if (status == noErr && outFormat->mSampleRate > 0 && outFormat->mFormatID != 0) {
        if (outFormat->mChannelsPerFrame == 0) {
            outFormat->mChannelsPerFrame = _core_audio_input_channel_count(deviceID);
        }
        return outFormat->mChannelsPerFrame > 0;
    }

    AudioObjectPropertyAddress streamAddr = {
        kAudioDevicePropertyStreams,
        kAudioDevicePropertyScopeInput,
        kAudioObjectPropertyElementMain
    };
    size = 0;
    if (AudioObjectGetPropertyDataSize(deviceID, &streamAddr, 0, NULL, &size) != noErr || size == 0) {
        return false;
    }

    AudioStreamID *streams = (AudioStreamID *)malloc(size);
    if (!streams) return false;
    bool ok = false;
    if (AudioObjectGetPropertyData(deviceID, &streamAddr, 0, NULL, &size, streams) == noErr) {
        UInt32 count = size / sizeof(AudioStreamID);
        for (UInt32 i = 0; i < count; i++) {
            AudioStreamBasicDescription format;
            memset(&format, 0, sizeof(format));
            AudioObjectPropertyAddress fmtAddr = {
                kAudioStreamPropertyVirtualFormat,
                kAudioObjectPropertyScopeGlobal,
                kAudioObjectPropertyElementMain
            };
            UInt32 fmtSize = sizeof(format);
            status = AudioObjectGetPropertyData(streams[i], &fmtAddr, 0, NULL, &fmtSize, &format);
            if (status == noErr && format.mSampleRate > 0 && format.mFormatID != 0) {
                if (format.mChannelsPerFrame == 0) {
                    format.mChannelsPerFrame = _core_audio_input_channel_count(deviceID);
                }
                if (format.mChannelsPerFrame > 0) {
                    *outFormat = format;
                    ok = true;
                    break;
                }
            }
        }
    }
    free(streams);
    return ok;
}

static float _core_audio_read_sample(const void *data, UInt32 index, const AudioStreamBasicDescription *format, bool *ok) {
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

static uint64_t _core_audio_input_available_samples(CoreAudioInputRecorderState *state) {
    uint64_t readIndex = atomic_load_explicit(&state->ringReadIndex, memory_order_acquire);
    uint64_t writeIndex = atomic_load_explicit(&state->ringWriteIndex, memory_order_acquire);
    if (writeIndex < readIndex) return 0;
    return writeIndex - readIndex;
}

static void _core_audio_input_ring_write(CoreAudioInputRecorderState *state, int16_t sample) {
    if (!state || !state->ringBuffer || state->ringCapacity == 0) return;

    uint64_t writeIndex = atomic_load_explicit(&state->ringWriteIndex, memory_order_relaxed);
    uint64_t readIndex = atomic_load_explicit(&state->ringReadIndex, memory_order_acquire);
    if (writeIndex - readIndex >= state->ringCapacity) {
        atomic_store_explicit(&state->ringReadIndex, writeIndex - state->ringCapacity + 1, memory_order_release);
        atomic_store_explicit(&state->overflowed, true, memory_order_release);
    }

    state->ringBuffer[writeIndex % state->ringCapacity] = sample;
    atomic_store_explicit(&state->ringWriteIndex, writeIndex + 1, memory_order_release);
}

static bool _core_audio_input_buffer_shape(const AudioBufferList *bufferList,
                                           const AudioStreamBasicDescription *format,
                                           bool *outNonInterleaved,
                                           UInt32 *outChannels,
                                           UInt32 *outBytesPerSample,
                                           UInt32 *outFrames) {
    if (!bufferList || !format || !outNonInterleaved || !outChannels || !outBytesPerSample || !outFrames) {
        return false;
    }
    if (format->mFormatID != kAudioFormatLinearPCM || format->mSampleRate <= 0) {
        return false;
    }

    UInt32 bytesPerSample = format->mBitsPerChannel / 8;
    if (bytesPerSample == 0 || bufferList->mNumberBuffers == 0) {
        return false;
    }

    bool nonInterleaved = (format->mFormatFlags & kAudioFormatFlagIsNonInterleaved) != 0;
    UInt32 channels = format->mChannelsPerFrame;
    UInt32 frames = 0;

    if (nonInterleaved) {
        if (bufferList->mBuffers[0].mData == NULL) return false;
        frames = bufferList->mBuffers[0].mDataByteSize / bytesPerSample;
        if (channels == 0 || channels > bufferList->mNumberBuffers) {
            channels = bufferList->mNumberBuffers;
        }
    } else {
        if (bufferList->mBuffers[0].mData == NULL) return false;
        if (channels == 0) {
            channels = bufferList->mBuffers[0].mNumberChannels;
        }
        if (channels == 0) return false;
        frames = bufferList->mBuffers[0].mDataByteSize / (bytesPerSample * channels);
    }

    if (frames == 0 || channels == 0) return false;

    *outNonInterleaved = nonInterleaved;
    *outChannels = channels;
    *outBytesPerSample = bytesPerSample;
    *outFrames = frames;
    return true;
}

static bool _core_audio_input_mono_sample(const AudioBufferList *bufferList,
                                          const AudioStreamBasicDescription *format,
                                          bool nonInterleaved,
                                          UInt32 channels,
                                          UInt32 frame,
                                          float *outSample) {
    if (!outSample) return false;

    bool supported = true;
    float mixed = 0.0f;
    UInt32 usedChannels = 0;

    if (nonInterleaved) {
        UInt32 limit = channels < bufferList->mNumberBuffers ? channels : bufferList->mNumberBuffers;
        for (UInt32 ch = 0; ch < limit; ch++) {
            const void *samples = bufferList->mBuffers[ch].mData;
            if (!samples) continue;
            mixed += _core_audio_read_sample(samples, frame, format, &supported);
            usedChannels++;
        }
    } else {
        const void *samples = bufferList->mBuffers[0].mData;
        if (!samples) return false;
        for (UInt32 ch = 0; ch < channels; ch++) {
            mixed += _core_audio_read_sample(samples, (frame * channels) + ch, format, &supported);
            usedChannels++;
        }
    }

    if (!supported || usedChannels == 0) return false;
    *outSample = mixed / (float)usedChannels;
    return true;
}

static int16_t _core_audio_input_float_to_int16(float sample) {
    if (sample > 1.0f) sample = 1.0f;
    if (sample < -1.0f) sample = -1.0f;
    return (int16_t)(sample * 32767.0f);
}

static OSStatus _core_audio_input_config_changed(AudioObjectID inObjectID,
                                                 UInt32 inNumberAddresses,
                                                 const AudioObjectPropertyAddress *inAddresses,
                                                 void *inClientData) {
    (void)inObjectID;
    (void)inNumberAddresses;
    (void)inAddresses;

    CoreAudioInputRecorderState *state = (CoreAudioInputRecorderState *)inClientData;
    if (!state) return noErr;

    atomic_store_explicit(&state->needsRestart, true, memory_order_release);
    atomic_store_explicit(&state->recording, false, memory_order_release);
    return noErr;
}

static void _core_audio_input_install_listeners(CoreAudioInputRecorderState *state) {
    if (!state) return;

    AudioObjectPropertyAddress defaultInputAddr = {
        kAudioHardwarePropertyDefaultInputDevice,
        kAudioObjectPropertyScopeGlobal,
        kAudioObjectPropertyElementMain
    };
    if (!state->defaultInputListenerInstalled &&
        AudioObjectAddPropertyListener(kAudioObjectSystemObject, &defaultInputAddr, _core_audio_input_config_changed, state) == noErr) {
        state->defaultInputListenerInstalled = true;
    }

    if (state->deviceID == kAudioObjectUnknown) return;

    AudioObjectPropertyAddress streamFormatAddr = {
        kAudioDevicePropertyStreamFormat,
        kAudioDevicePropertyScopeInput,
        kAudioObjectPropertyElementMain
    };
    if (!state->streamFormatListenerInstalled &&
        AudioObjectAddPropertyListener(state->deviceID, &streamFormatAddr, _core_audio_input_config_changed, state) == noErr) {
        state->streamFormatListenerInstalled = true;
    }

    AudioObjectPropertyAddress sampleRateAddr = {
        kAudioDevicePropertyNominalSampleRate,
        kAudioObjectPropertyScopeGlobal,
        kAudioObjectPropertyElementMain
    };
    if (!state->sampleRateListenerInstalled &&
        AudioObjectAddPropertyListener(state->deviceID, &sampleRateAddr, _core_audio_input_config_changed, state) == noErr) {
        state->sampleRateListenerInstalled = true;
    }
}

static void _core_audio_input_remove_listeners(CoreAudioInputRecorderState *state) {
    if (!state) return;

    AudioObjectPropertyAddress defaultInputAddr = {
        kAudioHardwarePropertyDefaultInputDevice,
        kAudioObjectPropertyScopeGlobal,
        kAudioObjectPropertyElementMain
    };
    if (state->defaultInputListenerInstalled) {
        AudioObjectRemovePropertyListener(kAudioObjectSystemObject, &defaultInputAddr, _core_audio_input_config_changed, state);
        state->defaultInputListenerInstalled = false;
    }

    if (state->deviceID == kAudioObjectUnknown) {
        state->streamFormatListenerInstalled = false;
        state->sampleRateListenerInstalled = false;
        return;
    }

    AudioObjectPropertyAddress streamFormatAddr = {
        kAudioDevicePropertyStreamFormat,
        kAudioDevicePropertyScopeInput,
        kAudioObjectPropertyElementMain
    };
    if (state->streamFormatListenerInstalled) {
        AudioObjectRemovePropertyListener(state->deviceID, &streamFormatAddr, _core_audio_input_config_changed, state);
        state->streamFormatListenerInstalled = false;
    }

    AudioObjectPropertyAddress sampleRateAddr = {
        kAudioDevicePropertyNominalSampleRate,
        kAudioObjectPropertyScopeGlobal,
        kAudioObjectPropertyElementMain
    };
    if (state->sampleRateListenerInstalled) {
        AudioObjectRemovePropertyListener(state->deviceID, &sampleRateAddr, _core_audio_input_config_changed, state);
        state->sampleRateListenerInstalled = false;
    }
}

static OSStatus _core_audio_input_io_proc(AudioObjectID inDevice,
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

    CoreAudioInputRecorderState *state = (CoreAudioInputRecorderState *)inClientData;
    if (!state || !atomic_load(&state->recording) || !inInputData) {
        return noErr;
    }

    bool nonInterleaved = false;
    UInt32 channels = 0;
    UInt32 bytesPerSample = 0;
    UInt32 sourceFrames = 0;
    if (!_core_audio_input_buffer_shape(inInputData, &state->format, &nonInterleaved, &channels, &bytesPerSample, &sourceFrames)) {
        atomic_store_explicit(&state->unsupportedFormat, true, memory_order_release);
        return noErr;
    }
    (void)bytesPerSample;

    double ratio = state->format.mSampleRate / (double)CORE_AUDIO_INPUT_TARGET_RATE;
    if (ratio <= 0) {
        atomic_store_explicit(&state->unsupportedFormat, true, memory_order_release);
        return noErr;
    }

    double position = state->resamplePosition;
    while (position < (double)sourceFrames) {
        UInt32 frame = (UInt32)position;
        if (frame >= sourceFrames) break;

        float sample = 0.0f;
        if (!_core_audio_input_mono_sample(inInputData, &state->format, nonInterleaved, channels, frame, &sample)) {
            atomic_store_explicit(&state->unsupportedFormat, true, memory_order_release);
            return noErr;
        }

        _core_audio_input_ring_write(state, _core_audio_input_float_to_int16(sample));
        position += ratio;
    }
    state->resamplePosition = position - (double)sourceFrames;
    return noErr;
}

int CoreAudioInputRecorder_RequestMicPermission(void) {
    @autoreleasepool {
#if __MAC_OS_X_VERSION_MAX_ALLOWED >= 101400
        if (@available(macOS 10.14, *)) {
            AVAuthorizationStatus status = [AVCaptureDevice authorizationStatusForMediaType:AVMediaTypeAudio];
            if (status != AVAuthorizationStatusNotDetermined) {
                return (int)status;
            }

            __block bool granted = false;
            dispatch_semaphore_t sema = dispatch_semaphore_create(0);
            [AVCaptureDevice requestAccessForMediaType:AVMediaTypeAudio
                                     completionHandler:^(BOOL allowed) {
                granted = allowed;
                dispatch_semaphore_signal(sema);
            }];
            dispatch_semaphore_wait(sema, DISPATCH_TIME_FOREVER);
            return granted ? (int)AVAuthorizationStatusAuthorized : (int)AVAuthorizationStatusDenied;
        }
#endif
        return 3;
    }
}

void* CoreAudioInputRecorder_Create(void) {
    @autoreleasepool {
        CoreAudioInputRecorderState *state = (CoreAudioInputRecorderState *)calloc(1, sizeof(CoreAudioInputRecorderState));
        if (!state) return NULL;
        state->deviceID = kAudioObjectUnknown;
        state->ioProcID = NULL;
        state->ringCapacity = CORE_AUDIO_INPUT_RING_CAPACITY;
        state->ringBuffer = (int16_t *)calloc(state->ringCapacity, sizeof(int16_t));
        if (!state->ringBuffer) {
            free(state);
            return NULL;
        }
        atomic_init(&state->recording, false);
        atomic_init(&state->needsRestart, false);
        atomic_init(&state->unsupportedFormat, false);
        atomic_init(&state->overflowed, false);
        atomic_init(&state->ringReadIndex, 0);
        atomic_init(&state->ringWriteIndex, 0);
        return state;
    }
}

static void _core_audio_input_stop_device(CoreAudioInputRecorderState *state) {
    if (!state) return;
    atomic_store(&state->recording, false);
    _core_audio_input_remove_listeners(state);

    if (state->deviceID != kAudioObjectUnknown && state->ioProcID != NULL) {
        AudioDeviceStop(state->deviceID, state->ioProcID);
        AudioDeviceDestroyIOProcID(state->deviceID, state->ioProcID);
        state->ioProcID = NULL;
    }
    state->deviceID = kAudioObjectUnknown;
}

bool CoreAudioInputRecorder_Start(void *ref) {
    @autoreleasepool {
        if (!ref) return false;
        CoreAudioInputRecorderState *state = (CoreAudioInputRecorderState *)ref;
        _core_audio_input_stop_device(state);

        AudioDeviceID defaultInput = _audio_default_input_device();
        AudioDeviceID builtInInput = _audio_find_input_by_transport(kAudioDeviceTransportTypeBuiltIn);
        AudioDeviceID deviceID = defaultInput;
        const char *selection = "default";

        if (defaultInput != kAudioObjectUnknown) {
            UInt32 transport = _audio_transport_type(defaultInput);
            bool defaultIsBluetooth = (transport == kAudioDeviceTransportTypeBluetooth ||
                                       transport == kAudioDeviceTransportTypeBluetoothLE);
            if (defaultIsBluetooth && builtInInput != kAudioObjectUnknown && builtInInput != defaultInput) {
                deviceID = builtInInput;
                selection = "built-in";
                _audio_log("[background-audio] default input is Bluetooth; using built-in mic to preserve playback quality");
            }
        } else if (builtInInput != kAudioObjectUnknown) {
            deviceID = builtInInput;
            selection = "built-in";
        }

        if (deviceID == kAudioObjectUnknown) {
            _audio_log("[background-audio] no microphone input device available");
            _emit_audio_event("microphone", "started", "failed", "no microphone input device available");
            return false;
        }

        if (!_core_audio_input_copy_device_format(deviceID, &state->format)) {
            _audio_log("[background-audio] failed to read microphone input format");
            _emit_audio_event("microphone", "started", "failed", "failed to read microphone input format");
            return false;
        }

        state->deviceID = deviceID;
        state->resamplePosition = 0;
        atomic_store(&state->needsRestart, false);
        atomic_store(&state->unsupportedFormat, false);
        atomic_store(&state->overflowed, false);
        // The ring contains canonical 16 kHz PCM, independent of the device
        // format. Keep unread samples across a configuration restart.
        OSStatus status = AudioDeviceCreateIOProcID(state->deviceID, _core_audio_input_io_proc, state, &state->ioProcID);
        if (status != noErr) {
            _audio_log("[background-audio] microphone AudioDeviceCreateIOProcID failed: %d", (int)status);
            _emit_audio_event("microphone", "started", "failed", "AudioDeviceCreateIOProcID failed");
            state->deviceID = kAudioObjectUnknown;
            return false;
        }

        _core_audio_input_install_listeners(state);
        atomic_store(&state->recording, true);
        status = AudioDeviceStart(state->deviceID, state->ioProcID);
        if (status != noErr) {
            atomic_store(&state->recording, false);
            _core_audio_input_remove_listeners(state);
            _audio_log("[background-audio] microphone AudioDeviceStart failed: %d", (int)status);
            _emit_audio_event("microphone", "started", "failed", "AudioDeviceStart failed");
            AudioDeviceDestroyIOProcID(state->deviceID, state->ioProcID);
            state->ioProcID = NULL;
            state->deviceID = kAudioObjectUnknown;
            return false;
        }

        _audio_log(
            "[background-audio] CoreAudio microphone recorder started: selection=%s device=%u rate=%.0f ch=%u bits=%u flags=%u",
            selection,
            (unsigned)state->deviceID,
            state->format.mSampleRate,
            (unsigned)state->format.mChannelsPerFrame,
            (unsigned)state->format.mBitsPerChannel,
            (unsigned)state->format.mFormatFlags
        );
        return true;
    }
}

void CoreAudioInputRecorder_Stop(void *ref) {
    @autoreleasepool {
        if (!ref) return;
        CoreAudioInputRecorderState *state = (CoreAudioInputRecorderState *)ref;
        _core_audio_input_stop_device(state);
        _emit_audio_event("microphone", "stopped", "", "caller requested stop");
    }
}

void CoreAudioInputRecorder_Destroy(void *ref) {
    @autoreleasepool {
        if (!ref) return;
        CoreAudioInputRecorderState *state = (CoreAudioInputRecorderState *)ref;
        _core_audio_input_stop_device(state);

        if (state->ringBuffer) {
            free(state->ringBuffer);
            state->ringBuffer = NULL;
        }
        free(state);
    }
}

bool CoreAudioInputRecorder_NeedsRestart(void *ref) {
    if (!ref) return false;
    CoreAudioInputRecorderState *state = (CoreAudioInputRecorderState *)ref;
    return atomic_load_explicit(&state->needsRestart, memory_order_acquire);
}

bool CoreAudioInputRecorder_RestartIfNeeded(void *ref) {
    if (!ref) return false;
    CoreAudioInputRecorderState *state = (CoreAudioInputRecorderState *)ref;
    if (!atomic_load_explicit(&state->needsRestart, memory_order_acquire)) {
        return true;
    }

    _audio_log("[background-audio] microphone input configuration changed, restarting CoreAudio recorder");
    _emit_audio_event("microphone", "restart_attempt", "", "input device configuration changed");
    bool ok = CoreAudioInputRecorder_Start(ref);
    if (!ok) {
        atomic_store_explicit(&state->needsRestart, true, memory_order_release);
    }
    _emit_audio_event("microphone", "restart", ok ? "success" : "failed", ok ? "" : "CoreAudio restart failed");
    return ok;
}

void* CoreAudioInputRecorder_ReadChunk(void *ref, int seconds, int *outLen) {
    @autoreleasepool {
        *outLen = 0;
        if (!ref || seconds <= 0) return NULL;
        CoreAudioInputRecorderState *state = (CoreAudioInputRecorderState *)ref;

        uint32_t targetSamples = (uint32_t)(seconds * CORE_AUDIO_INPUT_TARGET_RATE);
        int maxWaitIterations = (seconds + 1) * 50;
        int waited = 0;
        while (atomic_load(&state->recording) &&
               !atomic_load_explicit(&state->needsRestart, memory_order_acquire) &&
               _core_audio_input_available_samples(state) < targetSamples &&
               waited < maxWaitIterations) {
            usleep(20000);
            waited++;
        }

        if (atomic_exchange_explicit(&state->unsupportedFormat, false, memory_order_acq_rel)) {
            _audio_log(
                "[background-audio] unsupported microphone format: formatID=%u flags=%u bits=%u channels=%u rate=%.0f",
                (unsigned)state->format.mFormatID,
                (unsigned)state->format.mFormatFlags,
                (unsigned)state->format.mBitsPerChannel,
                (unsigned)state->format.mChannelsPerFrame,
                state->format.mSampleRate
            );
        }
        if (atomic_exchange_explicit(&state->overflowed, false, memory_order_acq_rel)) {
            _audio_log("[background-audio] microphone ring buffer overflowed; dropped oldest samples");
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
        uint32_t totalSize = sizeof(WavHeader) + dataSize;

        void *wavBuf = malloc(totalSize);
        if (!wavBuf) {
            return NULL;
        }

        WavHeader hdr;
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

        memcpy(wavBuf, &hdr, sizeof(WavHeader));
        int16_t *pcmOut = (int16_t *)((char *)wavBuf + sizeof(WavHeader));
        for (uint32_t i = 0; i < sampleCount; i++) {
            pcmOut[i] = state->ringBuffer[(readIndex + i) % state->ringCapacity];
        }
        atomic_store_explicit(&state->ringReadIndex, readIndex + sampleCount, memory_order_release);

        *outLen = (int)totalSize;
        return wavBuf;
    }
}

void CoreAudioInputRecorder_Free(void *buf) {
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

// emitAudioCaptureEventFromNative reports an event through the C entry point
// the native recorders use (_emit_audio_event, which calls the
// goAudioCaptureEvent export), so tests can exercise the cgo callback path.
func emitAudioCaptureEventFromNative(source, kind, outcome, message string) {
	cSource := C.CString(source)
	cKind := C.CString(kind)
	cOutcome := C.CString(outcome)
	cMessage := C.CString(message)
	defer C.free(unsafe.Pointer(cSource))
	defer C.free(unsafe.Pointer(cKind))
	defer C.free(unsafe.Pointer(cOutcome))
	defer C.free(unsafe.Pointer(cMessage))
	C._emit_audio_event(cSource, cKind, cOutcome, cMessage)
}

// darwinAudioRecorder captures microphone audio via a CoreAudio input IOProc on macOS.
type darwinAudioRecorder struct {
	mu     sync.Mutex
	ref    unsafe.Pointer
	readWG sync.WaitGroup
}

// darwinHasMicrophoneInputDevice returns true when the system has at least one
// usable audio input device (built-in mic, USB mic, Bluetooth headset, etc.).
// Pure CoreAudio query — no AVFoundation, no TCC prompt, safe on any thread.
func darwinHasMicrophoneInputDevice() bool {
	defaultInput := C._audio_default_input_device()
	if defaultInput != C.AudioDeviceID(C.kAudioObjectUnknown) {
		return true
	}
	builtIn := C._audio_find_input_by_transport(C.UInt32(C.kAudioDeviceTransportTypeBuiltIn))
	return builtIn != C.AudioDeviceID(C.kAudioObjectUnknown)
}

func isBackgroundListeningSourceSupported(source backgroundListeningSource) bool {
	switch source {
	case backgroundListeningSourceBoth:
		return darwinSystemAudioCaptureSupported()
	case backgroundListeningSourceMicrophone:
		return true
	case backgroundListeningSourceSystem:
		return darwinSystemAudioCaptureSupported()
	default:
		return false
	}
}

func newPlatformAudioRecorder(source backgroundListeningSource) audioRecorder {
	switch source {
	case backgroundListeningSourceBoth:
		return &combinedAudioRecorder{newRecorders: newDarwinCombinedRecorders}
	case backgroundListeningSourceSystem:
		return newDarwinSystemAudioRecorder()
	default:
		return &darwinAudioRecorder{}
	}
}

// newDarwinCombinedRecorders returns the candidate recorders for the
// combined source on macOS. Start order matters: system audio first so the
// process tap is ready before the independent microphone recorder begins
// feeding chunks.
func newDarwinCombinedRecorders() []audioRecorder {
	candidates := make([]audioRecorder, 0, 2)
	if darwinSystemAudioCaptureSupported() {
		candidates = append(candidates, newDarwinSystemAudioRecorder())
	}
	candidates = append(candidates, &darwinAudioRecorder{})
	return candidates
}

func (r *darwinAudioRecorder) Start() error {
	// Pre-flight: verify at least one input device exists before touching
	// AVFoundation permissions. On Mac mini (and other headless Macs) with
	// no microphone, requesting mic permission triggers a TCC dialog whose
	// interaction with the Wails main run loop causes a SIGSEGV. The
	// device check is pure CoreAudio and safe to call on any thread.
	if !darwinHasMicrophoneInputDevice() {
		log.Printf("[background-audio] no microphone input device available, skipping mic recorder")
		recordAudioCaptureEvent(AudioCaptureEvent{
			Source: "microphone", Kind: "error", Outcome: "failed",
			Message: "no microphone input device available",
		})
		return fmt.Errorf("no microphone input device available")
	}

	// Check microphone permission. AVAuthorizationStatus values:
	// 0=notDetermined, 1=restricted, 2=denied, 3=authorized
	status := int(C.CoreAudioInputRecorder_CheckMicPermission())
	log.Printf("[background-audio] mic permission status: %d", status)

	switch status {
	case 3: // authorized
		// Good to go.
	case 2: // denied
		recordAudioCaptureEvent(AudioCaptureEvent{
			Source: "microphone", Kind: "error", Outcome: "failed",
			Message: "microphone permission denied",
		})
		return fmt.Errorf("microphone access denied - enable in System Settings > Privacy & Security > Microphone")
	case 1: // restricted
		recordAudioCaptureEvent(AudioCaptureEvent{
			Source: "microphone", Kind: "error", Outcome: "failed",
			Message: "microphone access restricted by system policy",
		})
		return fmt.Errorf("microphone access restricted by system policy")
	case 0: // notDetermined
		if !permissionPromptsAllowed() {
			// Listening started on its own (launch, unlock): never show the
			// dialog from here. The user grants it from onboarding or
			// Settings > What Isoform sees, which starts listening again.
			log.Printf("[background-audio] mic permission not determined; not asking outside a user request")
			recordAudioCaptureEvent(AudioCaptureEvent{
				Source: "microphone", Kind: "skipped",
				Message: "microphone permission not granted yet; grant it in Settings > What Isoform sees",
			})
			return fmt.Errorf("microphone: %w", errPermissionNotRequested)
		}
		log.Printf("[background-audio] mic permission not determined, requesting access")
		status = int(C.CoreAudioInputRecorder_RequestMicPermission())
		if status != 3 {
			recordAudioCaptureEvent(AudioCaptureEvent{
				Source: "microphone", Kind: "error", Outcome: "failed",
				Message: "microphone permission denied",
			})
			return fmt.Errorf("microphone access denied - enable in System Settings > Privacy & Security > Microphone")
		}
	}

	ref := C.CoreAudioInputRecorder_Create()
	if ref == nil {
		recordAudioCaptureEvent(AudioCaptureEvent{
			Source: "microphone", Kind: "error", Outcome: "failed",
			Message: "CoreAudioInputRecorder_Create returned NULL",
		})
		return fmt.Errorf("failed to create audio recorder")
	}
	if !C.CoreAudioInputRecorder_Start(ref) {
		C.CoreAudioInputRecorder_Destroy(ref)
		// A "started failed" event was already emitted from Obj-C with
		// the CoreAudio start error. Don't double-emit.
		return fmt.Errorf("failed to start audio recorder (check microphone permission)")
	}
	recordAudioCaptureEvent(AudioCaptureEvent{
		Source: "microphone", Kind: "started", Outcome: "success",
	})
	r.mu.Lock()
	r.ref = ref
	r.mu.Unlock()
	return nil
}

func (r *darwinAudioRecorder) ReadChunk(d time.Duration) (capturedAudioChunk, error) {
	r.mu.Lock()
	ref := r.ref
	if ref == nil {
		r.mu.Unlock()
		return capturedAudioChunk{}, fmt.Errorf("audio recorder not initialized")
	}
	r.readWG.Add(1)
	r.mu.Unlock()
	defer r.readWG.Done()

	seconds := int(d.Seconds())
	if seconds < 1 {
		seconds = 1
	}

	var outLen C.int
	buf := C.CoreAudioInputRecorder_ReadChunk(ref, C.int(seconds), &outLen)
	if buf == nil || outLen <= 0 {
		if C.CoreAudioInputRecorder_NeedsRestart(ref) {
			r.mu.Lock()
			current := r.ref
			r.mu.Unlock()
			if current == ref && !C.CoreAudioInputRecorder_RestartIfNeeded(ref) {
				return capturedAudioChunk{Source: backgroundListeningSourceMicrophone}, fmt.Errorf("failed to restart microphone recorder after input configuration change")
			}
		}
		return capturedAudioChunk{Source: backgroundListeningSourceMicrophone}, nil
	}
	defer C.CoreAudioInputRecorder_Free(buf)

	// CapturedAt set here in the recorder goroutine — this is the closest
	// we can get to "audio finished recording" wall-clock time. Setting
	// it later (in captureLoop) would reflect channel-consumer time and
	// is subject to scheduling ordering between the mic and system_audio
	// readLoops, defeating the echo-window comparison.
	return capturedAudioChunk{
		CaptureID:   nextAudioCaptureIdentity(),
		Source:      backgroundListeningSourceMicrophone,
		WavData:     C.GoBytes(buf, outLen),
		Duration:    time.Duration(int(outLen)-wavHeaderSize) * time.Second / meetingAudioBytesPerSecond,
		CapturedAt:  time.Now(),
		CapturedApp: activitymon.FrontmostAppInfo(),
	}, nil
}

func (r *darwinAudioRecorder) Stop() {
	r.mu.Lock()
	ref := r.ref
	if ref == nil {
		r.mu.Unlock()
		return
	}
	r.ref = nil
	r.mu.Unlock()

	C.CoreAudioInputRecorder_Stop(ref)
	r.readWG.Wait()
	C.CoreAudioInputRecorder_Destroy(ref)
}

func (r *darwinAudioRecorder) ActiveSources() []backgroundListeningSource {
	return []backgroundListeningSource{backgroundListeningSourceMicrophone}
}

// requestMicrophoneBeforeListening shows the microphone dialog on its own
// before the recorders start, so the system audio prompt that follows never
// overlaps it. Only called from the user's listening switch.
func requestMicrophoneBeforeListening() {
	if int(C.CoreAudioInputRecorder_CheckMicPermission()) != 0 {
		return
	}
	if !darwinHasMicrophoneInputDevice() {
		return
	}
	requestMicrophonePermission()
}
