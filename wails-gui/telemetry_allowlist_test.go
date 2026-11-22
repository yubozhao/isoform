package main

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/posthog/posthog-go"
)

const telemetryFreeText = "Lena Park wrote /Users/x/secret.txt https://example.com/?q=launch"

// Read the production dispatch, including grouped cases. Collect indexed keys
// from the entire file so a new optional copy and nested source keys are probed
// even before anyone adds them to the reviewed fixtures.
func telemetryContractInputs(t *testing.T) ([]string, map[string]any) {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "telemetry.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	poison := map[string]any{"unreviewed": telemetryFreeText}
	var events []string
	ast.Inspect(file, func(node ast.Node) bool {
		index, ok := node.(*ast.IndexExpr)
		if ok {
			lit, ok := index.Index.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				t.Fatal("review a new computed index in the telemetry boundary")
			}
			key, err := strconv.Unquote(lit.Value)
			if err != nil {
				t.Fatal(err)
			}
			poison[key] = telemetryFreeText
		}
		return true
	})
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "productTelemetryProperties" {
			continue
		}
		for _, stmt := range fn.Body.List {
			dispatch, ok := stmt.(*ast.SwitchStmt)
			if !ok {
				continue
			}
			tag, ok := dispatch.Tag.(*ast.Ident)
			if !ok || tag.Name != "event" {
				continue
			}
			for _, stmt := range dispatch.Body.List {
				for _, expr := range stmt.(*ast.CaseClause).List {
					lit, ok := expr.(*ast.BasicLit)
					if !ok {
						t.Fatal("review new telemetry event expression", expr)
					}
					name, err := strconv.Unquote(lit.Value)
					if err != nil {
						t.Fatal(err)
					}
					events = append(events, name)
				}
			}
		}
	}
	if len(events) == 0 {
		t.Fatal("could not enumerate the production telemetry dispatch")
	}
	return events, poison
}

func TestTelemetryAllowlistCarriesNoFreeText(t *testing.T) {
	type fields = map[string]any
	const session = "749c7d44-cbda-4d70-8b64-b55bcf4132e1"
	action := func(id string) fields {
		return fields{"action": id, "kind": "open", "outcome": "opened", "ms_to_result": 1.0}
	}
	// Each fixture is a valid input and the exact event-specific wire shape.
	valid := map[string]fields{
		"app.open":                   {},
		"brief.first_seen":           {},
		"iphone.paired":              {},
		"playbook.run":               {},
		"phone_chat.queued":          {},
		"app.active_time":            {"active_seconds": 5.0, "session_id": session},
		"app.daily_active":           {"date": "2026-11-21"},
		"chat.turn_sent":             {"count": 1.0, "source": fields{"surface": "today", "action": "T10"}},
		"today.action":               action("T10"),
		"phone_today.action":         action("P036"),
		"tasks.action":               action("C03"),
		"today.section_seen":         {"section": "brief", "rows": 2.0},
		"tasks.section_seen":         {"section": "scheduled", "rows": 2.0},
		"assistant.signed_out":       {"assistant": "codex"},
		"assistant.signed_out_fixed": {"assistant": "codex", "seconds": 5.0},
		"phone_chat.sent_after_wait": {"wait_minutes": 2.0},
		"phone_chat.undone":          {"count": 2.0},
		"update.checked":             {"result": "error", "error_class": "http"},
		"update.staged":              {"from": "1.0.0", "to": "1.1.0"},
		"update.installed":           {"from": "1.0.0", "to": "1.1.0", "how": "quit", "waited_days": 2.0},
		"update.rolled_back":         {"from": "1.1.0", "to": "1.0.0", "attempts": 2.0},
		"update.quit_probe":          {"result": "failed", "reason": "timeout"},
		"update.running":             {"version": "1.1.0", "how": "restart_now", "release_to_run_minutes": 2.0},
	}
	events, poison := telemetryContractInputs(t)
	for _, key := range []string{"distinct_id", "$device_id", "$user_id", "$lib", "$lib_version", "$os", "$os_version", "$os_distro", "$go_version"} {
		poison[key] = telemetryFreeText
	}
	for _, event := range events {
		t.Run(event, func(t *testing.T) {
			fixture, ok := valid[event]
			if !ok {
				t.Fatal("add a reviewed valid fixture for this event")
			}
			app, received := newHTTPTelemetry(t, TelemetryTierDetailed)
			app.telemetry.setLocale(telemetryFreeText)
			input := maps.Clone(poison)
			maps.Copy(input, fixture)
			app.telemetry.Capture(event, input) // Positive control with hostile extras.
			app.telemetry.Capture(event, poison)
			for key := range fixture {
				oneBad := maps.Clone(input)
				oneBad[key] = telemetryFreeText
				app.telemetry.Capture(event, oneBad)
			}
			if source, ok := fixture["source"].(fields); ok {
				for _, key := range []string{"", "action", "surface"} {
					nested := maps.Clone(poison)
					maps.Copy(nested, source)
					if key != "" {
						nested[key] = telemetryFreeText
					}
					withSource := maps.Clone(input)
					withSource["source"] = nested
					app.telemetry.Capture(event, withSource)
				}
			}
			app.telemetry.Close()
			batch := received()
			if len(batch) == 0 {
				t.Fatal("valid control never reached the loopback receiver")
			}
			want := fields{
				"app_version": strings.TrimSpace(Version), "flavor": appFlavor(), "build_channel": telemetryBuildChannel(),
				"os": runtime.GOOS, "os_version": app.telemetry.osVersion, "locale": "en", "telemetry_tier": "detailed",
				"$process_person_profile": false, "$geoip_disable": true,
				"$lib": "posthog-go", "$lib_version": "1.0.0", "$go_version": runtime.Version(),
			}
			// The pinned SDK obtains these three values from the host at
			// serialization time. Caller maps must never override them.
			host := (posthog.Capture{}).APIfy().(posthog.CaptureInApi).Properties
			for _, key := range []string{"$os", "$os_version", "$os_distro"} {
				if value, present := host[key]; present {
					want[key] = value
				}
			}
			maps.Copy(want, fixture)
			if !reflect.DeepEqual(batch[0].Properties, want) {
				t.Fatalf("valid control properties = %#v, want %#v", batch[0].Properties, want)
			}
			for _, sent := range batch {
				payload, err := json.Marshal(sent)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(payload), telemetryFreeText) {
					t.Fatal("free-text sentinel reached the wire")
				}
				id, err := uuid.Parse(sent.DistinctID)
				if sent.Event != event || err != nil || id.Version() != 4 || sent.DistinctID != app.telemetry.anonymousID {
					t.Fatalf("unexpected capture identity for %s", sent.Event)
				}
				for key, value := range sent.Properties {
					expected, allowed := want[key]
					if !allowed || !reflect.DeepEqual(value, expected) {
						t.Errorf("unreviewed outgoing %s = %#v", key, value)
					}
				}
			}
		})
	}
}
