package main

import (
	"os"
	"strings"
)

// Features turned off in the rework (docs/rework-2026-11.md, step 1). Their
// code is kept and can come back one at a time once the core is solid. With a
// switch off a feature runs no background loop, shows no UI entry and makes
// no model call.
//
// Each switch is off by default. YANSU_FEATURES=automations,playbooks turns
// the named ones back on (comma separated; "all" turns every one on).
type featureSwitch string

const (
	// featureAutomations: user automations and cron prompts. Internal
	// one-shot jobs that chat and Tasks schedule on the same scheduler (open
	// answer checks, handoffs, topic finds) keep running.
	featureAutomations featureSwitch = "automations"
	// featurePlaybooks: playbooks, SOPs, deliverables and goals.
	featurePlaybooks featureSwitch = "playbooks"
	// featureProactiveVoice: proactive voice cards in chat.
	featureProactiveVoice featureSwitch = "proactive-voice"
	// featureCalendarHolds: calendar holds.
	featureCalendarHolds featureSwitch = "calendar-holds"
)

const featureSwitchesEnv = "YANSU_FEATURES"

var allFeatureSwitches = []featureSwitch{featureAutomations, featurePlaybooks, featureProactiveVoice, featureCalendarHolds, featureWatchNotes, featureChat}

// featureOn reports whether a turned-off feature has been turned back on.
func featureOn(feature featureSwitch) bool {
	for _, name := range strings.Split(os.Getenv(featureSwitchesEnv), ",") {
		switch strings.TrimSpace(strings.ToLower(name)) {
		case "all", string(feature):
			return true
		}
	}
	return false
}

// FeatureSwitches is what the frontend reads to hide the entries of features
// that are off.
type FeatureSwitches struct {
	Automations    bool `json:"automations"`
	Playbooks      bool `json:"playbooks"`
	ProactiveVoice bool `json:"proactiveVoice"`
	CalendarHolds  bool `json:"calendarHolds"`
	Chat           bool `json:"chat"`
}

// GetFeatureSwitches is the Wails binding for FeatureSwitches.
func (a *App) GetFeatureSwitches() FeatureSwitches {
	return FeatureSwitches{
		Automations:    featureOn(featureAutomations),
		Playbooks:      featureOn(featurePlaybooks),
		ProactiveVoice: featureOn(featureProactiveVoice),
		CalendarHolds:  featureOn(featureCalendarHolds),
		Chat:           featureOn(featureChat),
	}
}

// errFeatureOff is returned by the bindings of a feature that is off.
type errFeatureOff featureSwitch

func (e errFeatureOff) Error() string { return "feature_off: " + string(e) }
