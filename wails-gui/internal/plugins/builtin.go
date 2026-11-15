package plugins

import (
	"slices"
	"strings"

	"wails-gui/internal/engagement"
)

// Builtin describes an existing switch. StateKey is an existing ui_state key;
// switches stored elsewhere are read by the app adapter. Setter documents the
// sole existing writer. Reading this catalogue never installs or starts code.
type Builtin struct {
	Manifest
	Platforms []string
	Kind      string
	StateKey  string
	Setter    string
	DefaultOn bool
	Decode    func(string) bool
}

// Builtins is the complete feature-switch catalogue. Privacy gates and
// per-account/per-schedule records are deliberately not plugin switches; see
// docs/plugins-builtin-switches.md for that inventory.
func Builtins() []Builtin {
	makeBuiltin := func(id, kind, stateKey, setter string, defaultOn bool, requires ...string) Builtin {
		return Builtin{
			Manifest: Manifest{ID: id, Version: "1", Source: "builtin", Kinds: []string{kind},
				NameKey: "plugins.builtins." + id + ".name", DescriptionKey: "plugins.builtins." + id + ".description", Requires: requires},
			Kind: kind, StateKey: stateKey, Setter: setter, DefaultOn: defaultOn,
		}
	}
	rows := []Builtin{}
	for _, id := range []string{"authority", "cron-api", "notification-pacing"} {
		row := makeBuiltin(id, "builtin", "", "", true)
		row.Essential = true
		rows = append(rows, row)
	}
	rows = append(rows,
		makeBuiltin("activity-monitor", "source", "", "SetActivityMonitorEnabled", false),
		makeBuiltin("background-listening", "source", "", "SetBackgroundListening", true),
		makeBuiltin("background-microphone", "source", "", "SetPermissionEnabled", true),
		makeBuiltin("meeting-recording", "source", "", "SetAutomaticMeetingRecordingEnabled", true),
		makeBuiltin("calendar-sync", "source", "permission_enabled:calendars", "SetPermissionEnabled", true),
		makeBuiltin("calendar-peek", "source", "calendar_peek_enabled", "SetCalendarPeekEnabled", true),
		makeBuiltin("mail-sync", "source", "permission_enabled:automation_mail", "SetPermissionEnabled", true),
		makeBuiltin("reminders-sync", "source", "permission_enabled:reminders", "SetPermissionEnabled", true),
		makeBuiltin("files-activity", "source", "permission_enabled:files", "SetPermissionEnabled", false),
		makeBuiltin("clipboard", "source", "permission_enabled:clipboard", "SetPermissionEnabled", true),
		makeBuiltin("triage-memory", "builtin", "triage_enable_memory", "SaveUIState", true),
		makeBuiltin("triage-knowledge", "builtin", "triage_enable_knowledge", "SaveUIState", true),
		makeBuiltin("triage-shadow", "builtin", "triage_enable_shadow", "SaveUIState", true),
		makeBuiltin("triage-sop", "builtin", "triage_enable_sop", "SaveUIState", true),
		makeBuiltin("triage-events", "builtin", "triage_enable_events", "SaveUIState", true),
		makeBuiltin("shadow-confidence", "builtin", "shadow_confidence_adjustment_enabled", "SaveUIState", true, "triage-shadow"),
		makeBuiltin("work-pattern-profile", "builtin", "work_pattern_profile_influence_enabled", "SaveUIState", false, "triage-shadow"),
		makeBuiltin("memory-dream", "builtin", "", "MemoryDreamService.SetSwitch", true),
		makeBuiltin("memory-dream-llm", "builtin", "", "MemoryDreamService.SetSwitch", true, "memory-dream"),
		makeBuiltin("topic-finds", "card", "", "SetTopicFindsEnabled", true),
		makeBuiltin("auto-speak", "builtin", "", "SetAutoSpeakEnabled", false),
		makeBuiltin("auto-speak-automation", "builtin", "", "SetAutoSpeakAutomationEnabled", false),
		makeBuiltin("auto-speak-notifications", "builtin", "", "SetAutoSpeakNotificationsEnabled", false),
		makeBuiltin("browser-bridge", "tool", "", "SetBrowserBridgeEnabled", false),
		makeBuiltin("start-at-login", "builtin", "", "SetStartAtLogin", true),
		makeBuiltin("text-channel", "builtin", "", "SetTextChannel", false),
		makeBuiltin("mobile-connection", "builtin", "", "EnableMobileConnection/DisableMobileConnection", false),
		makeBuiltin("mobile-brief", "card", "", "SetMobileBriefSchedule", true),
		makeBuiltin("brief-podcast", "source", "", "SetMobileBriefSchedule", false),
	)
	for i := range rows {
		// These switches are backed by the platform permission catalogue in
		// permissions_{darwin,windows,linux}.go. Do not probe OS permission
		// state while listing the catalogue: some probes initialize services.
		switch rows[i].ID {
		case "calendar-sync", "mail-sync", "reminders-sync", "files-activity", "clipboard":
			rows[i].Platforms = []string{"darwin"}
		case "background-microphone":
			rows[i].Platforms = []string{"darwin", "windows"}
		}
		if rows[i].ID == "work-pattern-profile" {
			rows[i].Decode = func(raw string) bool { return strings.EqualFold(strings.TrimSpace(raw), "true") }
		}
	}
	for _, kind := range engagement.AllKinds() {
		spec, _ := engagement.Spec(kind)
		rows = append(rows, makeBuiltin("notify-"+string(kind), "builtin", "", "SetEngagementKindEnabled", spec.DefaultOn))
	}
	return rows
}

// StoredOn preserves each legacy reader's interpretation, including exact
// "false" checks. It does not normalize or write the original stored string.
func (b Builtin) StoredOn(raw string) bool {
	if b.Decode != nil {
		return b.Decode(raw)
	}
	if raw == "" {
		return b.DefaultOn
	}
	return raw != "false"
}

// BuiltinsForPlatform hides permission-backed switches whose existing setter
// cannot operate on the host platform. Other controls remain platform-neutral.
func BuiltinsForPlatform(platform string) []Builtin {
	rows := Builtins()
	return slices.DeleteFunc(rows, func(row Builtin) bool {
		return len(row.Platforms) > 0 && !slices.Contains(row.Platforms, platform)
	})
}
