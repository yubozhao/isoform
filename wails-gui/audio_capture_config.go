package main

import (
	"log"
	"sync/atomic"
)

type audioCaptureConfig struct {
	cfg    yansuConfig
	exists bool
	err    error
}

func readAudioConfig() audioCaptureConfig {
	cfg, exists, err := readConfigFile()
	return audioCaptureConfig{cfg, exists, err}
}

type audioConfigGate struct {
	source     string
	unreadable atomic.Bool
}

var (
	listeningGate        = audioConfigGate{source: "background listening"}
	microphoneGate       = audioConfigGate{source: "microphone"}
	outsideCallsGate     = audioConfigGate{source: "system audio outside calls"}
	meetingGate          = audioConfigGate{source: "meeting recording"}
	systemPermissionGate = audioConfigGate{source: "system audio permission"}
)

func (g *audioConfigGate) read(value bool, err error) (on, known bool) {
	if err != nil {
		if !g.unreadable.Swap(true) {
			log.Printf("[capture] %s skipped: config unreadable", g.source)
		}
		return false, false
	}
	g.unreadable.Store(false)
	return value, true
}

func (c audioCaptureConfig) listening() (bool, bool) {
	return listeningGate.read(resolveBackgroundListeningEnabled(c.cfg, c.exists), c.err)
}

func (c audioCaptureConfig) microphone() (bool, bool) {
	return microphoneGate.read(!c.cfg.BackgroundMicrophonePaused, c.err)
}

func (c audioCaptureConfig) outsideCalls() (bool, bool) {
	return outsideCallsGate.read(c.cfg.BackgroundSystemAudioOutsideCalls, c.err)
}

func (c audioCaptureConfig) meeting() (bool, bool) {
	return meetingGate.read(!c.cfg.AutomaticMeetingRecordingDisabled, c.err)
}

func (c audioCaptureConfig) systemPermission() (bool, bool) {
	return systemPermissionGate.read(c.cfg.SystemAudioPermissionGranted, c.err)
}

func (c audioCaptureConfig) source() backgroundListeningSource {
	if _, known := c.microphone(); !known {
		return backgroundListeningSourceNone
	}
	return resolveBackgroundListeningSource(c.cfg)
}

func (c audioCaptureConfig) allowsSource(source backgroundListeningSource) bool {
	on, _ := c.listening()
	if !on {
		return false
	}
	if source == backgroundListeningSourceMicrophone {
		on, _ = c.microphone()
	}
	return on
}

// A batch can wait in the ASR queue across a config outage or a switch
// change. Check every chunk before sending the merged audio to the model.
func (a *App) allowBackgroundAudioBatch(batch []scheduledAudioChunk) bool {
	cfg := readAudioConfig()
	if !cfg.allowsSource(batch[0].source) {
		return false
	}
	for _, chunk := range batch {
		if chunk.inMeeting || chunk.candidate {
			if !a.automaticMeetingRecordingEpochIsCurrent(chunk.recordingEpoch) {
				return false
			}
		} else if !cfg.listenOutsideCalls(chunk.source) {
			return false
		}
	}
	return true
}
